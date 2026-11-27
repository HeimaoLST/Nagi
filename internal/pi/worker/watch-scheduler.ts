/// <reference lib="es2024.promise" />

import { createHash, randomUUID } from 'node:crypto';
import { accessSync, closeSync, constants, fstatSync, lstatSync, mkdirSync, mkdtempSync, openSync,
  readFileSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { dirname, isAbsolute, join } from 'node:path';
import type { SkillBundle } from './bot-skills.js';
import { clipUTF8, projectionRedactor, streamingProjectionRedactor } from './product-redactor.js';
import { publicWatch, publicWatchGrant, type InboxSource, type ProductSQLite, type RuntimeWatch, type RuntimeWatchGrant, type WatchChanges } from './product-sqlite.js';
import { canonicalJSON, RequestError, validateWatchDefinition, type Operation, type ProductParams, type SkillPermissions,
  type WatchChange, type WatchModelCreate, type WatchMutationResult, type WatchOccurrence, type WatchPrecheck, type WatchRevokeResult,
  type WatchSchedule } from './protocol.js';
import { NativeSession, supervisorPath, TerminalProjection } from './session-supervisor.js';
import type { SkillSandbox } from './skill-sandbox.js';

const sandboxPath = '/usr/bin/sandbox-exec';
const maxConcurrentPrechecks = 4;
const outputCeiling = 1024 * 1024;
const excerptLimit = 1024;
const truncatedAfter = 64 * 1024;
const defaultTimeout = 10000;
const hourMs = 3600000;
const dayMs = hourMs * 24;
const configuredPoll = Number(process.env.NAGI_WATCH_POLL_MS);
const pollInterval = Number.isSafeInteger(configuredPoll) && configuredPoll > 0 ? Math.max(25, configuredPoll) : 250;
const maximumExecutableSize = 64 * 1024 * 1024;
const iso = (time: number) => new Date(time).toISOString();
const hash = (value: unknown) => createHash('sha256').update(canonicalJSON(value)).digest('hex');

type Owner = { botId: string; currentConversationId: string; archived: boolean; workspace: string };
export type PreparedWatchSkill = { bundle: SkillBundle; permissions: SkillPermissions; approvalDigest: string; permissionsDigest: string };
type ScheduleDependencies = {
  installationId: string;
  privatePaths: string[];
  sandbox: SkillSandbox;
  owner: (botId: string) => Owner;
  ownsConversation: (botId: string, conversationId: string) => boolean;
  skill: (botId: string, precheck: Extract<WatchPrecheck, { kind: 'skill' }>, cwd: string) => PreparedWatchSkill;
  keys: () => string[];
  emit: (change: WatchChange) => void;
  failure: () => void;
};
type Executable = { path: string; bytes: Buffer; digest: string };
type Prepared = {
  precheck: WatchPrecheck;
  grant: Omit<RuntimeWatchGrant, 'grantId' | 'revision' | 'state' | 'createdAt' | 'updatedAt'>;
  executable?: Executable;
  skill?: PreparedWatchSkill;
};
type PrecheckClass = 'success' | 'exit' | 'timeout' | 'sandbox_unavailable' | 'output_limit' | 'cancelled' | 'error';
type Observation = { status: PrecheckClass; exitCode?: number; truncated: boolean; digest: string; excerpt: string };
type Run = { controller: AbortController; done: Promise<void>; native?: NativeSession };
type OccurrenceEvent = { sourceKind: InboxSource; payload: object; coalesceKey?: string };
type WatchTarget = { botId: string; watchId: string; expectedRevision: number };

function zonedClock(timeZone: string): Intl.DateTimeFormat {
  return new Intl.DateTimeFormat('en-US-u-ca-gregory-nu-latn', { timeZone, year: 'numeric', month: 'numeric', day: 'numeric',
    hour: 'numeric', minute: 'numeric', second: 'numeric', hourCycle: 'h23' });
}

function wallTime(format: Intl.DateTimeFormat, epoch: number): number {
  const fields = Object.fromEntries(format.formatToParts(epoch).map(part => [part.type, Number(part.value)]));
  const wall = new Date(0);
  wall.setUTCFullYear(fields.year, fields.month - 1, fields.day);
  wall.setUTCHours(fields.hour, fields.minute, fields.second, 0);
  return wall.valueOf();
}

function nextCalendar(schedule: Extract<WatchSchedule, { cadenceKind: 'calendar' }>, after: number): number {
  const zone = zonedClock(schedule.timeZone);
  const local = wallTime(zone, after);
  const hourly = schedule.frequency === 'hourly';
  const step = hourly ? hourMs : dayMs;
  const first = Math.floor(local / step) * step + (hourly ? 0 : schedule.hour! * hourMs) + schedule.minute * 60000;
  for (let index = 0; index < (hourly ? 48 : 15); index++) {
    const wall = first + index * step;
    if (schedule.daysOfWeek && !schedule.daysOfWeek.includes(new Date(wall).getUTCDay())) continue;
    const offsets = new Set([-dayMs * 2, 0, dayMs * 2].map(delta => wallTime(zone, wall + delta) - wall - delta));
    const matching = [...offsets].map(offset => wall - offset).filter(utc => wallTime(zone, utc) === wall);
    if (!matching.length) continue;
    const earliest = Math.min(...matching);
    if (earliest > after) return earliest;
  }
  throw new RequestError('invalid_params', 'The next calendar slot could not be resolved');
}

function cadence(schedule: WatchSchedule): number {
  return schedule.cadenceKind === 'interval' ? schedule.intervalSeconds : schedule.frequency === 'hourly' ? 3600 :
    schedule.frequency === 'weekly' && schedule.daysOfWeek?.length === 1 ? 604800 : 86400;
}

function nextDue(row: RuntimeWatch, after: number, previous?: number): Pick<RuntimeWatch, 'scheduledAt' | 'nextDueAt'> {
  if (row.timer) return { scheduledAt: row.timer.fireAt, nextDueAt: row.timer.fireAt };
  const schedule = row.schedule!;
  let scheduled: number;
  if (schedule.cadenceKind === 'calendar') scheduled = nextCalendar(schedule, after);
  else {
    const step = schedule.intervalSeconds * 1000;
    scheduled = previous === undefined ? after + step : previous + Math.max(1, Math.floor((after - previous) / step) + 1) * step;
  }
  const scheduledAt = iso(scheduled);
  const jitterSeconds = schedule.jitterSeconds ?? 0;
  const jitter = jitterSeconds ? parseInt(hash([row.watchId, row.revision, scheduledAt]).slice(0, 8), 16) % (jitterSeconds + 1) : 0;
  return { scheduledAt, nextDueAt: iso(scheduled + jitter * 1000) };
}

function executableFile(path: string): Executable {
  try {
    if (!isAbsolute(path)) throw new Error('Relative executable');
    const canonical = realpathSync(path);
    const fd = openSync(canonical, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    try {
      const opened = fstatSync(fd);
      if (!opened.isFile() || opened.size > maximumExecutableSize || !(opened.mode & 0o111)) throw new Error('Invalid executable');
      accessSync(canonical, constants.X_OK);
      const bytes = readFileSync(fd);
      const after = fstatSync(fd);
      const linked = lstatSync(canonical);
      if (!linked.isFile() || linked.dev !== opened.dev || linked.ino !== opened.ino || realpathSync(path) !== canonical ||
          bytes.length !== opened.size || after.size !== opened.size || after.mtimeMs !== opened.mtimeMs || after.ctimeMs !== opened.ctimeMs) {
        throw new Error('Executable changed while opening');
      }
      return { path: canonical, bytes, digest: createHash('sha256').update(bytes).digest('hex') };
    } finally { closeSync(fd); }
  } catch { throw new RequestError('executable_changed', 'The precheck executable is missing, unsafe, or changed'); }
}

function sandboxPolicy(executable: string, original: string, scratch: string, cwd: string, workspaceRead: boolean, privatePaths: string[]): string {
  const home = realpathSync(homedir());
  const protectedPaths = new Set([...privatePaths, join(home, '.ssh'), join(home, '.aws'), join(home, 'Library', 'Keychains')]);
  for (const path of [...protectedPaths]) {
    try { protectedPaths.add(realpathSync(path)); }
    catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error; }
  }
  const paths = (roots: Iterable<string>) => [...roots].map(path => `(subpath ${JSON.stringify(path)})`).join(' ');
  const readable = ['/usr/lib', '/System/Library', '/bin', '/usr/bin', dirname(original), scratch, ...(workspaceRead ? [cwd] : [])];
  return `(version 1)
(deny default)
(deny network*)
(allow process-exec (literal ${JSON.stringify(executable)}))
(allow sysctl-read)
(allow file-read* (literal "/") (literal "/dev/null") (literal ${JSON.stringify(executable)}) ${paths(readable)})
(allow file-write* (subpath ${JSON.stringify(scratch)}))
(deny file-read* file-write* ${paths(protectedPaths)})`;
}

class ObservationStream {
  private readonly digest = createHash('sha256');
  private readonly projection = { stdout: new TerminalProjection(), stderr: new TerminalProjection() };
  private readonly redact: (text: string, final?: boolean) => string;
  private rawBytes = 0;
  private bytes = 0;
  private excerpt = '';
  overflow = false;

  constructor(keys: string[], private readonly abort: () => void) {
    this.redact = streamingProjectionRedactor(keys);
  }

  output(channel: 'stdout' | 'stderr', data: Buffer): void {
    if (this.overflow) return;
    const available = outputCeiling - this.rawBytes;
    this.rawBytes += data.length;
    this.append(this.redact(this.projection[channel].push(data.subarray(0, available))));
    if (data.length > available || this.bytes > outputCeiling) {
      this.overflow = true;
      this.abort();
    }
  }

  private append(text: string): void {
    if (!text) return;
    const remaining = outputCeiling - this.bytes;
    this.bytes += Buffer.byteLength(text);
    const projected = clipUTF8(text, remaining, '');
    this.digest.update(projected);
    const excerptRemaining = excerptLimit - Buffer.byteLength(this.excerpt);
    if (excerptRemaining > 0) this.excerpt += clipUTF8(projected, excerptRemaining, '');
  }

  finish(status: PrecheckClass, exitCode?: number): Observation {
    if (!this.overflow) {
      this.append(this.redact(this.projection.stdout.push()));
      this.append(this.redact(this.projection.stderr.push()));
      this.append(this.redact('', true));
    }
    const truncated = this.overflow || this.bytes > truncatedAfter;
    return { status: this.overflow ? 'output_limit' : status, exitCode, truncated, digest: this.digest.digest('hex'), excerpt: this.excerpt };
  }
}

export class WatchScheduler {
  private timer?: ReturnType<typeof setInterval>;
  private lastPoll?: number;
  private closing = false;
  private backupFence = 0;
  private readonly inflight = new Map<string, Run>();

  constructor(private readonly storage: ProductSQLite, private readonly dependencies: ScheduleDependencies,
    private readonly clock: () => number = Date.now) {}

  now(): number {
    const file = process.env.NAGI_TEST_CLOCK_FILE;
    if (!file) return this.clock();
    try {
      const raw: unknown = JSON.parse(readFileSync(file, 'utf8'));
      const epoch = typeof raw === 'number' ? raw : raw && typeof raw === 'object' && 'nowMs' in raw ? raw.nowMs :
        raw && typeof raw === 'object' && 'nowIso' in raw && typeof raw.nowIso === 'string' ? Date.parse(raw.nowIso) : undefined;
      if (typeof epoch === 'number' && Number.isSafeInteger(epoch) && Number.isFinite(new Date(epoch).valueOf())) return epoch;
    } catch {}
    throw new RequestError('clock_unavailable', 'The scheduler clock could not be read');
  }

  start(): void {
    if (this.timer || this.closing) return;
    this.timer = setInterval(() => this.tick(), pollInterval).unref();
    this.tick();
  }

  get(botId: string, watchId: string): RuntimeWatch {
    const row = this.storage.runtimeWatch(botId, watchId);
    if (!row) throw new RequestError('not_found', 'The watch does not exist for this Bot');
    return row;
  }

  detail(botId: string, watchId: string) {
    const row = this.get(botId, watchId);
    const grant = this.grant(row);
    return { ...publicWatch(row), grant: grant && publicWatchGrant(grant), recentOccurrences: this.storage.watchOccurrences(watchId) };
  }

  private grant(row: RuntimeWatch): RuntimeWatchGrant | undefined {
    return this.storage.watchGrant(this.dependencies.installationId, row.ownerBotId, row.grantId);
  }

  private change(change: WatchChange['change'], row?: RuntimeWatch, grant?: RuntimeWatchGrant): void {
    this.dependencies.emit({ botId: row?.ownerBotId ?? grant!.ownerBotId, change, watchId: row?.watchId, watchRevision: row?.revision,
      state: row?.state, grantId: grant?.grantId ?? row?.grantId, grantRevision: grant?.revision, occurrenceId: change === 'fired' ? row?.lastOccurrenceId : undefined,
      nextDueAt: row?.nextDueAt, at: row?.updatedAt ?? grant!.updatedAt });
  }

  ownerChanged(botId: string, previous: RuntimeWatch[]): void {
    const revisions = new Map(previous.map(row => [row.watchId, row.revision]));
    for (const row of this.storage.runtimeWatches(botId)) {
      if (revisions.get(row.watchId) === row.revision) continue;
      this.inflight.get(row.watchId)?.controller.abort();
      this.change(row.state === 'paused' ? 'paused' : 'invalidated', row, this.grant(row));
    }
  }

  mandateDisarmed(botId: string, watchId: string): void {
    this.inflight.get(watchId)?.controller.abort();
    const row = this.get(botId, watchId);
    this.change('paused', row, this.grant(row));
  }

  private checkAdmission(): void {
    if (this.closing) throw new RequestError('closing', 'Watch admission is closed');
    this.storage.assertSessionEpoch();
  }

  private rejectsSecrets(input: unknown): void {
    const redact = projectionRedactor(this.dependencies.keys());
    const visit = (value: unknown): boolean => {
      if (typeof value === 'string') return redact(value) !== value;
      return !!value && typeof value === 'object' && Object.entries(value).some(([key, field]) => redact(key) !== key || visit(field));
    };
    if (visit(input)) throw new RequestError('secret_in_definition', 'Watch definitions cannot contain saved credentials');
  }

  private prepare(row: RuntimeWatch, owner: Owner, verify: boolean): Prepared {
    const definition = row.precheck!;
    this.rejectsSecrets({ label: row.label, purpose: row.purpose, prompt: row.prompt, precheck: definition });
    let canonicalCwd: string;
    try {
      canonicalCwd = realpathSync(owner.workspace);
      if (!owner.workspace || !statSync(canonicalCwd).isDirectory()) throw new Error('Invalid workspace');
    } catch { throw new RequestError('workspace_changed', 'The Bot workspace is unavailable'); }
    if (verify && row.canonicalCwd !== undefined && row.canonicalCwd !== canonicalCwd) {
      throw new RequestError('workspace_changed', 'The Bot workspace no longer matches the watch');
    }
    const maxTimeoutMs = definition.timeoutMs ?? defaultTimeout;
    const basic = { installationId: this.dependencies.installationId, ownerBotId: row.ownerBotId, purpose: row.purpose,
      canonicalCwd, minCadenceSeconds: cadence(row.schedule!), maxTimeoutMs };
    const origin = [row.targetConversationId, row.originActivityId] as const;
    if (definition.kind === 'executable') {
      const executable = executableFile(definition.executable);
      if (verify && definition.executableDigest && (executable.path !== definition.executable || definition.executableDigest !== executable.digest)) {
        throw new RequestError('executable_changed', 'The precheck executable no longer matches the approved bytes');
      }
      const allowedArgs = definition.allowedArgs ?? [];
      if (Buffer.byteLength(JSON.stringify(allowedArgs)) > 32768) throw new RequestError('invalid_params', 'Precheck arguments exceed the byte limit');
      const artifact = { executable: executable.path, executableDigest: executable.digest, allowedArgs, workspaceRead: definition.workspaceRead ?? false };
      const grant = { ...basic, ...artifact, artifactKind: 'executable' as const, operationClass: 'precheck_executable' as const, networkOrigins: [],
        scopeDigest: hash([origin, row.purpose, 'executable', canonicalCwd, artifact]) };
      return { precheck: { ...definition, ...artifact, timeoutMs: maxTimeoutMs }, grant, executable };
    }
    if (Buffer.byteLength(JSON.stringify(definition.input ?? null)) > 65536) throw new RequestError('invalid_params', 'Precheck input exceeds the byte limit');
    const skill = this.dependencies.skill(row.ownerBotId, definition, canonicalCwd);
    if (verify && definition.approvalDigest && definition.approvalDigest !== skill.approvalDigest) {
      throw new RequestError('executable_changed', 'The skill no longer matches the approved bytes');
    }
    if (skill.permissions.workspaceWrite || skill.permissions.credentialRefs?.length) {
      throw new RequestError('scope_denied', 'Precheck skills require read-only permissions and no credentials');
    }
    const artifact = { skillId: definition.skillId, skillSource: definition.skillSource, scriptName: definition.scriptName,
      approvalDigest: skill.approvalDigest, permissionsDigest: skill.permissionsDigest, workspaceRead: skill.permissions.workspaceRead ?? false,
      networkOrigins: [...skill.permissions.networkOrigins ?? []].sort() };
    const grant = { ...basic, ...artifact, artifactKind: 'skill' as const, operationClass: 'precheck_skill' as const,
      scopeDigest: hash([origin, row.purpose, 'skill', canonicalCwd, artifact, definition.input ?? null]) };
    return { precheck: { ...definition, approvalDigest: skill.approvalDigest, workspaceRead: artifact.workspaceRead, timeoutMs: maxTimeoutMs }, grant, skill };
  }

  private matching(grant: RuntimeWatchGrant | undefined, plan: Prepared): grant is RuntimeWatchGrant {
    return grant?.state === 'active' && grant.scopeDigest === plan.grant.scopeDigest &&
      grant.minCadenceSeconds <= plan.grant.minCadenceSeconds && grant.maxTimeoutMs >= plan.grant.maxTimeoutMs;
  }

  private chooseGrant(row: RuntimeWatch, plan: Prepared, approve: boolean, at: string, excluded?: string): RuntimeWatchGrant | undefined {
    const candidates = this.storage.watchGrants(this.dependencies.installationId, row.ownerBotId);
    const match = candidates.find(grant => grant.grantId !== excluded && this.matching(grant, plan));
    if (match || !approve) return match;
    return { ...plan.grant, grantId: randomUUID(), revision: 1, state: 'active', createdAt: at, updatedAt: at };
  }

  private revoke(grant: RuntimeWatchGrant, reason: string, at: string, except?: string): { grant: RuntimeWatchGrant; rows: RuntimeWatch[] } {
    const revoked: RuntimeWatchGrant = { ...grant, state: 'revoked', revision: grant.revision + 1, updatedAt: at, revokedAt: at, revokedReason: reason };
    const rows = this.storage.runtimeWatches(grant.ownerBotId).filter(row => row.watchId !== except && row.grantId === grant.grantId &&
      (row.state === 'active' || row.state === 'paused')).map(row => this.disarm(row, 'invalidated', reason, at));
    return { grant: revoked, rows };
  }

  private disarm(row: RuntimeWatch, state: RuntimeWatch['state'], reason: string | undefined, at: string): RuntimeWatch {
    return { ...row, revision: row.revision + 1, state, armed: false, nextDueAt: undefined, scheduledAt: undefined, statusReason: reason, updatedAt: at };
  }

  private emitRevoked(grant: RuntimeWatchGrant, rows: RuntimeWatch[]): void {
    this.change('grant_revoked', undefined, grant);
    for (const row of rows) {
      this.inflight.get(row.watchId)?.controller.abort();
      this.change('invalidated', row, grant);
    }
  }

  private saveReceipt(method: string, params: { operationId: string }, target: Operation['target'],
    result: WatchMutationResult | WatchRevokeResult, changes: WatchChanges): Operation {
    const receipt: Operation = { operationId: params.operationId, state: 'accepted', target, result };
    this.storage.writeWatches({ ...changes, operation: { method, fingerprint: hash(params), receipt, reply: receipt } });
    return receipt;
  }

  save(params: ProductParams<'watch.save'>, model?: { activityId?: string; conversationId: string }): Operation {
    this.checkAdmission();
    validateWatchDefinition(params);
    const { operationId, botId, watchId, expectedRevision, targetConversationId, kind, purpose, prompt, precheck } = params;
    const owner = this.dependencies.owner(botId);
    if (owner.archived) throw new RequestError('invalid_state', 'Archived Bots cannot save a watch');
    const previous = watchId === undefined ? undefined : this.get(botId, watchId);
    if (previous && (expectedRevision === undefined || previous.revision !== expectedRevision) || !previous && expectedRevision !== undefined) {
      throw new RequestError('stale_revision', 'The watch revision has changed', undefined, { currentRevision: previous?.revision });
    }
    if (previous?.state === 'cancelled' || previous?.state === 'settled') throw new RequestError('invalid_state', 'The watch is already settled');
    if (previous?.targetConversationId && targetConversationId && targetConversationId !== previous.targetConversationId) {
      throw new RequestError('invalid_params', 'An existing watch keeps its original conversation');
    }
    const target = targetConversationId ?? previous?.targetConversationId ?? model?.conversationId ?? owner.currentConversationId;
    if (!this.dependencies.ownsConversation(botId, target) || model && target !== model.conversationId) {
      throw new RequestError(model ? 'scope_denied' : 'not_found', 'The watch conversation is outside the owner scope');
    }
    this.rejectsSecrets({ label: params.label, purpose, prompt, precheck });
    const now = this.now();
    const at = iso(now);
    const id = previous?.watchId ?? randomUUID();
    const timer = params.timer && { fireAt: iso(Date.parse(params.timer.fireAt)), missedPolicy: params.timer.missedPolicy ?? 'skip' as const };
    const schedule = params.schedule && { ...params.schedule, jitterSeconds: Math.min(params.schedule.jitterSeconds ?? 0,
      Math.min(60, Math.floor(cadence(params.schedule) / 4))) };
    let row: RuntimeWatch = { watchId: id, ownerBotId: botId, targetConversationId: target, originActivityId: previous?.originActivityId ?? model?.activityId,
      revision: (previous?.revision ?? 0) + 1, kind, source: previous?.source ?? (model ? 'model' : 'user'), state: 'paused', armed: false,
      label: params.label ?? '', purpose: purpose.trim(), prompt, timer, schedule, precheck, consecutiveFailures: 0,
      lastFiredAt: previous?.lastFiredAt, lastOccurrenceId: previous?.lastOccurrenceId, lastEventId: previous?.lastEventId,
      createdAt: previous?.createdAt ?? at, updatedAt: at };
    const plan = precheck && this.prepare(row, owner, false);
    const oldGrant = previous && this.grant(previous);
    const superseded = oldGrant?.state === 'active' && (!plan || !this.matching(oldGrant, plan)) ? this.revoke(oldGrant, 'definition_changed', at, id) : undefined;
    const grant = plan && this.chooseGrant(row, plan, !model && params.approveGrant !== false, at, superseded?.grant.grantId);
    const newGrant = grant && !this.storage.watchGrant(this.dependencies.installationId, botId, grant.grantId) ? grant : undefined;
    if (plan) row = { ...row, precheck: plan.precheck, canonicalCwd: plan.grant.canonicalCwd, grantId: grant?.grantId };
    if (previous && grant && oldGrant?.grantId === grant.grantId && canonicalJSON(previous.precheck) === canonicalJSON(row.precheck)) {
      const { lastGoodDigest, lastFailureDigest, consecutiveFailures, lastOutcome } = previous;
      row = { ...row, lastGoodDigest, lastFailureDigest, consecutiveFailures, lastOutcome };
    }
    if (plan && !grant) row = { ...row, state: 'proposed', statusReason: 'grant_required' };
    else if (params.activate !== false) {
      if (timer && Date.parse(timer.fireAt) <= now) throw new RequestError('invalid_params', 'An activated timer needs a future instant');
      row = { ...row, state: 'active', armed: true, ...nextDue(row, now) };
    }
    const result = { watch: publicWatch(row), grant: grant && publicWatchGrant(grant) };
    const receipt = this.saveReceipt(model ? 'watch_create' : 'watch.save', params, { botId, watchId: id }, result,
      { watches: [row, ...superseded?.rows ?? []], grants: [...superseded ? [superseded.grant] : [], ...newGrant ? [newGrant] : []] });
    if (previous) this.inflight.get(id)?.controller.abort();
    if (superseded) this.emitRevoked(superseded.grant, superseded.rows);
    if (newGrant) this.change('grant_saved', row, newGrant);
    this.change('saved', row, grant);
    return receipt;
  }

  activate(params: ProductParams<'watch.activate'>): Operation {
    this.checkAdmission();
    const row = this.checkedTarget(params);
    const owner = this.dependencies.owner(row.ownerBotId);
    if (owner.archived || row.state === 'cancelled' || row.state === 'settled') throw new RequestError('invalid_state', 'The watch cannot be activated');
    const now = this.now();
    const at = iso(now);
    let plan: Prepared | undefined;
    if (row.precheck) {
      try { plan = this.prepare(row, owner, true); }
      catch (error) { this.invalidate(row, error, at); throw error; }
    }
    const grant = plan && this.chooseGrant(row, plan, params.approveGrant === true, at);
    if (plan && !grant) throw new RequestError('grant_required', 'Approve a standing grant before activating this precheck');
    if (row.timer && Date.parse(row.timer.fireAt) <= now) throw new RequestError('invalid_state', 'Set a new future instant before activating the timer');
    const newGrant = grant && !this.storage.watchGrant(this.dependencies.installationId, row.ownerBotId, grant.grantId) ? grant : undefined;
    let next: RuntimeWatch = { ...row, revision: row.revision + 1, state: 'active', armed: true,
      canonicalCwd: plan?.grant.canonicalCwd, grantId: grant?.grantId, precheck: plan?.precheck, statusReason: undefined, updatedAt: at };
    next = { ...next, ...nextDue(next, now) };
    const receipt = this.saveReceipt('watch.activate', params, { botId: row.ownerBotId, watchId: row.watchId },
      { watch: publicWatch(next), grant: grant && publicWatchGrant(grant) }, { watches: [next], grants: newGrant && [newGrant] });
    this.inflight.get(row.watchId)?.controller.abort();
    if (newGrant) this.change('grant_saved', next, newGrant);
    this.change('activated', next, grant);
    return receipt;
  }

  transition(method: 'watch.pause' | 'watch.cancel' | 'watch_pause' | 'watch_cancel', params: ProductParams<'watch.pause'>): Operation {
    this.checkAdmission();
    const row = this.checkedTarget(params);
    const cancel = method === 'watch.cancel' || method === 'watch_cancel';
    const settled = row.state === 'cancelled' || row.state === 'settled';
    if (settled && !cancel) throw new RequestError('invalid_state', 'The watch is already settled');
    const at = iso(this.now());
    const next = settled || !cancel && row.state === 'paused' ? row :
      { ...this.disarm(row, cancel ? 'cancelled' : 'paused', undefined, at), settledAt: cancel ? at : undefined };
    const grant = this.grant(row);
    const result = { watch: publicWatch(next), grant: grant && publicWatchGrant(grant), disposition: cancel ? settled ? 'already_settled' : 'cancelled' : undefined };
    const receipt = this.saveReceipt(method, params, { botId: row.ownerBotId, watchId: row.watchId }, result, { watches: next === row ? [] : [next] });
    if (next !== row) {
      this.inflight.get(row.watchId)?.controller.abort();
      this.change(cancel ? 'cancelled' : 'paused', next, grant);
    }
    return receipt;
  }

  private checkedTarget({ botId, watchId, expectedRevision }: WatchTarget): RuntimeWatch {
    const row = this.get(botId, watchId);
    if (row.revision !== expectedRevision) throw new RequestError('stale_revision', 'The watch revision has changed', undefined, { currentRevision: row.revision });
    return row;
  }

  revokeGrant(params: ProductParams<'watch.revokeGrant'>): Operation {
    this.checkAdmission();
    const { botId, grantId, expectedRevision } = params;
    const row = this.storage.watchGrant(this.dependencies.installationId, botId, grantId);
    if (!row) throw new RequestError('not_found', 'The grant does not exist for this Bot and installation');
    if (row.revision !== expectedRevision) throw new RequestError('stale_revision', 'The grant revision has changed', undefined, { currentRevision: row.revision });
    const { grant, rows } = row.state === 'revoked' ? { grant: row, rows: [] } : this.revoke(row, 'grant_revoked', iso(this.now()));
    const result = { grant: publicWatchGrant(grant), invalidatedWatchIds: rows.map(row => row.watchId) };
    const receipt = this.saveReceipt('watch.revokeGrant', params, { botId, grantId }, result, { watches: rows, grants: [grant] });
    if (grant !== row) this.emitRevoked(grant, rows);
    return receipt;
  }

  private invalidate(row: RuntimeWatch, error: unknown, at: string): void {
    const code = error instanceof RequestError ? error.code : 'executable_changed';
    const reason = ['workspace_changed', 'executable_changed', 'grant_required', 'secret_in_definition'].includes(code) ? code : 'executable_changed';
    const oldGrant = this.grant(row);
    const revoked = oldGrant?.state === 'active' ? this.revoke(oldGrant, reason, at, row.watchId) : undefined;
    const next = this.disarm(row, 'invalidated', reason, at);
    this.storage.writeWatches({ watches: [next, ...revoked?.rows ?? []], grants: revoked && [revoked.grant] });
    this.inflight.get(row.watchId)?.controller.abort();
    if (revoked) this.emitRevoked(revoked.grant, revoked.rows);
    this.change('invalidated', next, revoked?.grant);
  }

  private tick(): void {
    if (this.closing || this.backupFence) return;
    let now: number;
    try { now = this.now(); } catch { return; }
    const previousPoll = this.lastPoll;
    this.lastPoll = now;
    const at = iso(now);
    try {
      this.storage.assertSessionEpoch();
      for (const row of this.storage.dueWatches(at)) {
        if (this.inflight.has(row.watchId)) continue;
        const owner = this.dependencies.owner(row.ownerBotId);
        if (owner.archived || !this.dependencies.ownsConversation(row.ownerBotId, row.targetConversationId)) {
          const next = this.disarm(row, 'paused', 'scope_unavailable', at);
          this.storage.writeWatches({ watches: [next] });
          this.change('paused', next);
          continue;
        }
        const scheduledAt = row.scheduledAt ?? row.nextDueAt!;
        const occurrence: WatchOccurrence = { occurrenceId: hash([row.watchId, row.revision, scheduledAt]), watchId: row.watchId,
          ownerBotId: row.ownerBotId, watchRevision: row.revision, scheduledAt, createdAt: at, outcome: 'running', state: 'running' };
        if (row.timer) {
          const due = Date.parse(scheduledAt);
          const missed = now > due && (previousPoll === undefined || now - due > Math.max(1000, pollInterval * 2));
          const skip = missed && row.timer.missedPolicy !== 'fire_once_coalesced';
          const outcome = skip ? 'skipped_missed' : 'fired';
          const next = { ...this.disarm(row, 'settled', undefined, at), settledAt: at };
          this.commitTick(row, next, { ...occurrence, outcome, state: 'settled', settledAt: at }, skip ? undefined :
            { sourceKind: 'timer_fired', payload: { outcome, missed } });
          continue;
        }
        const next = { ...row, ...nextDue(row, now, Date.parse(scheduledAt)), updatedAt: at };
        if (!row.precheck) {
          this.commitTick(row, next, { ...occurrence, outcome: 'tick', state: 'settled', settledAt: at },
            { sourceKind: 'schedule_tick', payload: { outcome: 'tick' }, coalesceKey: hash(['schedule_tick', row.ownerBotId, row.targetConversationId, row.watchId, row.revision]) });
          continue;
        }
        if (this.inflight.size >= maxConcurrentPrechecks) continue;
        let plan: Prepared;
        try {
          plan = this.prepare(row, owner, true);
          if (!this.matching(this.grant(row), plan)) throw new RequestError('grant_required', 'The standing grant no longer matches the precheck');
        } catch (error) { this.invalidate(row, error, at); continue; }
        const admitted = this.storage.recordWatchTick(row, next, occurrence);
        if (!admitted) continue;
        const controller = new AbortController();
        const run: Run = { controller, done: Promise.resolve() };
        run.done = this.observe(admitted, occurrence, plan, run).catch(() => this.dependencies.failure()).finally(() => this.inflight.delete(row.watchId));
        this.inflight.set(row.watchId, run);
      }
    } catch { this.dependencies.failure(); }
  }

  private commitTick(before: RuntimeWatch | undefined, next: RuntimeWatch, occurrence: WatchOccurrence, event?: OccurrenceEvent): void {
    const row = { ...next, lastOutcome: occurrence.outcome, lastOccurrenceId: occurrence.occurrenceId,
      lastFiredAt: event ? occurrence.settledAt : next.lastFiredAt, updatedAt: occurrence.settledAt! };
    const input = event && { ownerBotId: row.ownerBotId, targetConversationId: row.targetConversationId, sourceKind: event.sourceKind,
      sourceId: row.watchId, occurrenceId: occurrence.occurrenceId, coalesceKey: event.coalesceKey, at: occurrence.settledAt!,
      payload: { watchId: row.watchId, watchRevision: occurrence.watchRevision, scheduledAt: occurrence.scheduledAt, ...event.payload } };
    const saved = before ? this.storage.recordWatchTick(before, row, occurrence, input) : this.storage.settleWatchTick(row, occurrence, input);
    if (saved) this.change('fired', saved);
  }

  private async observe(row: RuntimeWatch, occurrence: WatchOccurrence, plan: Prepared, run: Run): Promise<void> {
    const observation = await this.runPrecheck(row, plan, run);
    const at = iso(this.lastPoll ?? Date.parse(occurrence.createdAt));
    const settled = { ...occurrence, settledAt: at };
    if (observation.status === 'cancelled') {
      this.storage.settleWatchTick({ ...row, lastOutcome: 'cancelled', lastOccurrenceId: occurrence.occurrenceId, updatedAt: at },
        { ...settled, outcome: 'cancelled', state: 'cancelled' });
      return;
    }
    const success = observation.status === 'success';
    const signature = hash([observation.status, observation.exitCode ?? null, observation.truncated, observation.digest]);
    let transition: 'initial' | 'changed' | 'recovered' | 'failed' | undefined;
    let outcome: string;
    if (success) {
      if (row.consecutiveFailures) transition = 'recovered';
      else if (!row.lastGoodDigest && row.precheck?.emitInitial) transition = 'initial';
      else if (row.lastGoodDigest && row.lastGoodDigest !== signature) transition = 'changed';
      outcome = transition ?? (row.lastGoodDigest ? 'unchanged' : 'baseline');
    } else {
      if (!row.consecutiveFailures || row.lastFailureDigest !== signature) transition = 'failed';
      outcome = transition ?? 'suppressed_failure';
    }
    const next: RuntimeWatch = { ...row, lastGoodDigest: success ? signature : row.lastGoodDigest,
      lastFailureDigest: success ? undefined : signature, consecutiveFailures: success ? 0 : row.consecutiveFailures + 1 };
    const projected = { exitCode: observation.exitCode, truncated: observation.truncated, digest: signature,
      excerpt: projectionRedactor(this.dependencies.keys())(observation.excerpt), error: success ? undefined : observation.status };
    const event: OccurrenceEvent | undefined = transition && { sourceKind: 'schedule_precheck', payload: { transition, ...projected },
      coalesceKey: transition === 'changed' ? hash(['schedule_precheck', row.ownerBotId, row.targetConversationId, row.watchId, row.revision, signature]) : undefined };
    this.commitTick(undefined, next, { ...settled, ...projected, outcome, state: 'settled' }, event);
  }

  private async runPrecheck(row: RuntimeWatch, plan: Prepared, run: Run): Promise<Observation> {
    const { controller } = run;
    const output = new ObservationStream(this.dependencies.keys(), () => controller.abort());
    if (process.platform !== 'darwin' || process.env.NAGI_FORCE_SANDBOX_UNAVAILABLE === '1') return output.finish('sandbox_unavailable');
    try { accessSync(sandboxPath, constants.X_OK); } catch { return output.finish('sandbox_unavailable'); }
    const timeout = row.precheck?.timeoutMs ?? defaultTimeout;
    let deadlineReached = false;
    const deadline = setTimeout(() => { deadlineReached = true; controller.abort(); }, timeout);
    try {
      if (plan.executable && plan.precheck.kind === 'executable') {
        return await this.observeExecutable(plan.executable, plan.precheck, plan.grant.canonicalCwd, output, run, () => deadlineReached);
      }
      const definition = plan.precheck;
      if (definition.kind !== 'skill' || !plan.skill) return output.finish('error');
      const node = await this.dependencies.sandbox.prepare();
      if (controller.signal.aborted) return output.finish(deadlineReached ? 'timeout' : 'cancelled');
      this.storage.assertSessionEpoch();
      const skill = this.dependencies.skill(row.ownerBotId, definition, plan.grant.canonicalCwd);
      if (skill.approvalDigest !== plan.skill.approvalDigest || skill.permissionsDigest !== plan.skill.permissionsDigest) {
        throw new RequestError('executable_changed', 'The precheck skill changed during setup');
      }
      const result = await this.dependencies.sandbox.run(node, { botId: row.ownerBotId, source: definition.skillSource, skillId: definition.skillId },
        skill.bundle, definition.scriptName, definition.input, plan.grant.canonicalCwd, skill.permissions,
        digest => {
          this.storage.assertSessionEpoch();
          const current = this.storage.runtimeWatch(row.ownerBotId, row.watchId);
          if (current?.state !== 'active' || current.revision !== row.revision || !this.matching(this.grant(current), plan) || digest !== skill.approvalDigest) {
            throw new RequestError('cancelled', 'The precheck grant changed during setup');
          }
        }, () => ({}), projectionRedactor(this.dependencies.keys()), controller.signal,
        { timeoutMs: timeout, output: (stream, data) => output.output(stream, data) });
      return output.finish(result.ok ? 'success' : 'exit', result.exitCode);
    } catch (error) {
      if (deadlineReached || error instanceof RequestError && error.code === 'skill_timeout') return output.finish('timeout');
      if (controller.signal.aborted || error instanceof RequestError && error.code === 'cancelled') return output.finish('cancelled');
      if (error instanceof RequestError && error.code === 'executable_changed') {
        const current = this.storage.runtimeWatch(row.ownerBotId, row.watchId);
        if (current?.revision === row.revision && current.state === 'active') this.invalidate(current, error, iso(this.lastPoll ?? Date.parse(row.updatedAt)));
        return output.finish('cancelled');
      }
      return output.finish(error instanceof RequestError && ['sandbox_unavailable', 'exec_failed'].includes(error.code) ? 'sandbox_unavailable' : 'error');
    } finally { clearTimeout(deadline); }
  }

  private async observeExecutable(artifact: Executable, definition: Extract<WatchPrecheck, { kind: 'executable' }>, cwd: string,
    output: ObservationStream, run: Run, timedOut: () => boolean): Promise<Observation> {
    const root = mkdtempSync(join(realpathSync(tmpdir()), 'nagi-precheck-'));
    try {
      const scratch = join(root, 'scratch');
      const executable = join(root, 'observer');
      mkdirSync(scratch, { mode: 0o700 });
      writeFileSync(executable, artifact.bytes, { mode: 0o500, flag: 'wx' });
      const policy = sandboxPolicy(executable, artifact.path, scratch, cwd, definition.workspaceRead ?? false, this.dependencies.privatePaths);
      if (run.controller.signal.aborted) return output.finish(timedOut() ? 'timeout' : 'cancelled');
      const native = run.native = new NativeSession(supervisorPath(), { mode: 'pipe', cwd, command: sandboxPath,
        args: ['-p', policy, executable, ...definition.allowedArgs ?? []] }, (event, data) => {
        if (event === 18 || event === 19) output.output(event === 18 ? 'stdout' : 'stderr', data);
      }, () => {}, { PATH: '/usr/bin:/bin', LANG: 'C.UTF-8', HOME: scratch, TMPDIR: scratch });
      const abort = () => { void native.close().catch(() => native.release()); };
      run.controller.signal.addEventListener('abort', abort, { once: true });
      try {
        const result = await native.done;
        const status = timedOut() ? 'timeout' : run.controller.signal.aborted ? 'cancelled' :
          result.execFailed || !result.childReaped ? 'sandbox_unavailable' : result.exitCode === 0 ? 'success' : 'exit';
        return output.finish(status, result.exitCode);
      } finally { run.controller.signal.removeEventListener('abort', abort); }
    } finally { rmSync(root, { recursive: true, force: true }); }
  }

  async withBackupFence<T>(capture: () => T): Promise<T> {
    this.backupFence++;
    let deadline: ReturnType<typeof setTimeout> | undefined;
    try {
      if (this.inflight.size) {
        const { promise, reject } = Promise.withResolvers<void>();
        deadline = setTimeout(() => reject(new RequestError('busy', 'A precheck is still finishing')), 2000);
        await Promise.race([Promise.all([...this.inflight.values()].map(run => run.done)), promise]);
      }
      return capture();
    } finally { clearTimeout(deadline); this.backupFence--; }
  }

  async drain(): Promise<void> {
    this.closing = true;
    clearInterval(this.timer);
    const runs = [...this.inflight.values()];
    for (const run of runs) run.controller.abort();
    await Promise.all(runs.map(run => run.done));
  }

  killOnExit(): void {
    for (const run of this.inflight.values()) run.native?.release();
  }
}
