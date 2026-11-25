import { createHash, randomUUID } from 'node:crypto';
import { chmodSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { basename, dirname, join } from 'node:path';
import { DatabaseSync, type SQLOutputValue } from 'node:sqlite';
import { decodeProductProfile, importProductSnapshot, privateCredentialKeys, readPrivateConnection, type PrivateConnection, type ProductBackup, type ProductProfilePaths } from './product-profile.js';
import type { ProductProfile } from './product.js';
import { canonicalJSON, maxAutomaticAnswers, maxTriageAttempts, RequestError, type ActivityProgressRecord, type ActivitySteer,
  type ActivitySteerPayload, type ActivitySteerRecord, type TriageAccounting,
  type SkillApproval, type SkillApprovalRequest, type SkillSource, type TraceItem } from './protocol.js';
import { type BotMemory, type BotWorkLog } from './bot-memory.js';
import { maxExecutionTraces, maxProfileTraces, traceDetail, redactTrace, type TraceBackup, type TraceObservation, type TraceUpdate } from './product-trace.js';
import { projectionRedactor } from './product-redactor.js';
import { backupSkillFiles, checkedSkillFiles, type BotSkills, type SkillWrite } from './bot-skills.js';
import type { McpVault, McpVaultWrite } from './mcp-vault.js';

import type { Escalation } from './model-router.js';
const MIGRATIONS = [`
  CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
  CREATE TABLE bots (
    bot_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, profile_json TEXT NOT NULL,
    profile_revision INTEGER NOT NULL, archived INTEGER NOT NULL CHECK (archived IN (0, 1)),
    current_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    retained_json TEXT
  ) STRICT;
  CREATE TABLE conversations (
    conversation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE,
    bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, revision INTEGER NOT NULL,
    draft_json TEXT NOT NULL, binding_json TEXT,
    latest_execution_id TEXT REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED
  ) STRICT;
  CREATE TABLE activities (
    activity_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE,
    owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id), origin_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
    revision INTEGER NOT NULL, state TEXT NOT NULL, goal_json TEXT NOT NULL,
    requirements_json TEXT NOT NULL, questions_json TEXT NOT NULL, materials_json TEXT NOT NULL,
    input_files_json TEXT NOT NULL, execution_ids_json TEXT NOT NULL, results_json TEXT NOT NULL,
    binding_json TEXT, retained_json TEXT
  ) STRICT;
  CREATE TABLE executions (
    execution_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, revision INTEGER NOT NULL, state TEXT NOT NULL,
    operation_id TEXT NOT NULL, input_json TEXT NOT NULL, error_json TEXT, engine_run_id TEXT,
    imported_activity_id TEXT REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, retained_json TEXT
  ) STRICT;
  CREATE TABLE operations (
    operation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, method_json TEXT NOT NULL, fingerprint TEXT NOT NULL,
    receipt_json TEXT NOT NULL, reply_json TEXT NOT NULL, reply_error_json TEXT,
    connection_revision INTEGER REFERENCES connections(revision), retained_json TEXT
  ) STRICT;
  CREATE TABLE connections (
    revision INTEGER PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, source_id TEXT NOT NULL,
    configuration_json TEXT NOT NULL
  ) STRICT;
`, 'ALTER TABLE bots ADD COLUMN system_kind TEXT;', `
  CREATE TABLE bot_memory_rules (
    rule_id TEXT PRIMARY KEY, bot_id TEXT NOT NULL REFERENCES bots(bot_id) ON DELETE CASCADE, record_json TEXT NOT NULL
  ) STRICT;
  CREATE TABLE bot_work_logs (
    log_id TEXT PRIMARY KEY, bot_id TEXT NOT NULL REFERENCES bots(bot_id) ON DELETE CASCADE,
    date TEXT NOT NULL, created_at TEXT NOT NULL, summary TEXT NOT NULL, tags_text TEXT NOT NULL, record_json TEXT NOT NULL
  ) STRICT;
  CREATE INDEX bot_work_logs_by_date ON bot_work_logs(bot_id, date DESC, created_at DESC);
  CREATE VIRTUAL TABLE bot_work_logs_fts USING fts5(summary, tags_text, content='bot_work_logs', content_rowid='rowid');
  CREATE TRIGGER bot_work_logs_insert AFTER INSERT ON bot_work_logs BEGIN
    INSERT INTO bot_work_logs_fts(rowid, summary, tags_text) VALUES (new.rowid, new.summary, new.tags_text);
  END;
  CREATE TRIGGER bot_work_logs_delete AFTER DELETE ON bot_work_logs BEGIN
    INSERT INTO bot_work_logs_fts(bot_work_logs_fts, rowid, summary, tags_text) VALUES ('delete', old.rowid, old.summary, old.tags_text);
  END;
  CREATE TABLE bot_pending_learnings (
    learning_id TEXT PRIMARY KEY, bot_id TEXT NOT NULL REFERENCES bots(bot_id) ON DELETE CASCADE, record_json TEXT NOT NULL
  ) STRICT;
  CREATE TABLE bot_memory_meta (
    bot_id TEXT PRIMARY KEY REFERENCES bots(bot_id) ON DELETE CASCADE, record_json TEXT NOT NULL
  ) STRICT;
`, `
  ALTER TABLE activities ADD COLUMN granted_linked_activity_ids_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(granted_linked_activity_ids_json) AND json_type(granted_linked_activity_ids_json) = 'array');
  CREATE TABLE activity_traces (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    activity_id TEXT NOT NULL REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED,
    execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED,
    at TEXT NOT NULL, kind TEXT NOT NULL CHECK (kind IN ('tool_started','tool_finished','file_confirmed','state_changed')),
    detail_json TEXT NOT NULL CHECK (json_valid(detail_json) AND length(CAST(detail_json AS BLOB)) <= 1024)
  ) STRICT;
  CREATE INDEX activity_traces_by_activity ON activity_traces(activity_id, seq);
  CREATE INDEX activity_traces_by_execution ON activity_traces(activity_id, execution_id, seq);
  CREATE TABLE activity_trace_meta (
    execution_id TEXT PRIMARY KEY REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED,
    activity_id TEXT NOT NULL REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED,
    coverage TEXT NOT NULL CHECK (coverage IN ('recorded','unrecorded')),
    evicted_through_seq INTEGER NOT NULL DEFAULT 0
  ) STRICT;
  CREATE INDEX activity_trace_meta_by_activity ON activity_trace_meta(activity_id);
  INSERT INTO meta VALUES ('trace_generation', lower(hex(randomblob(16))));
`, `
  CREATE TABLE mcp_servers (server_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, config_json TEXT NOT NULL, catalog_json TEXT) STRICT;
`, `
  CREATE TABLE skill_approvals (grant_key TEXT PRIMARY KEY, owner_bot_id TEXT NOT NULL, source TEXT NOT NULL, skill_id TEXT NOT NULL,
    approval_digest TEXT NOT NULL, permissions_digest TEXT NOT NULL, mode TEXT NOT NULL CHECK (mode IN ('pure_compute_tested','user_approved')),
    installation_id TEXT NOT NULL, approved_at TEXT NOT NULL, record_json TEXT NOT NULL) STRICT;
`, `
  CREATE TABLE provider_accounts (provider_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, config_json TEXT NOT NULL) STRICT;
  INSERT OR IGNORE INTO meta VALUES ('routing_policy', '"balanced"');
  ALTER TABLE activities ADD COLUMN routing_json TEXT;
  CREATE TABLE execution_mutation_admissions (
    admission_id INTEGER PRIMARY KEY AUTOINCREMENT,
    execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED,
    activity_id TEXT REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED,
    call_id TEXT NOT NULL, tool_name TEXT NOT NULL, tool_class TEXT NOT NULL CHECK (tool_class IN ('readonly','mutating')),
    state TEXT NOT NULL CHECK (state IN ('admitted','non_dispatched','settled_ok','settled_error','unknown')), at TEXT NOT NULL
  ) STRICT;
  CREATE INDEX mutation_admissions_by_execution ON execution_mutation_admissions(execution_id, tool_class, call_id);
  CREATE TABLE execution_admission_coverage (
    execution_id TEXT PRIMARY KEY REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED
  ) STRICT;
  CREATE TABLE activity_escalations (
    activity_id TEXT PRIMARY KEY REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED,
    prior_execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED,
    escalated_execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED,
    from_role TEXT NOT NULL, to_role TEXT NOT NULL, at TEXT NOT NULL
  ) STRICT;
`, `
  ALTER TABLE activities ADD COLUMN acknowledged_at TEXT;
  ALTER TABLE activities ADD COLUMN acknowledged_execution_id TEXT;
  ALTER TABLE activities ADD COLUMN closed_at TEXT;
  CREATE TABLE activity_progress (
    activity_id TEXT PRIMARY KEY REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED,
    execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED,
    call_id TEXT NOT NULL, progress_seq INTEGER NOT NULL CHECK (progress_seq > 0), record_json TEXT NOT NULL
  ) STRICT;
  CREATE TABLE skill_approval_requests (
    request_id TEXT PRIMARY KEY,
    activity_id TEXT NOT NULL REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED,
    execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED,
    call_id TEXT NOT NULL, bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED,
    revision INTEGER NOT NULL CHECK (revision > 0),
    state TEXT NOT NULL CHECK (state IN ('open','approved','denied','expired','cancelled')), record_json TEXT NOT NULL,
    UNIQUE (execution_id, call_id)
  ) STRICT;
  CREATE INDEX skill_approval_requests_by_activity ON skill_approval_requests(activity_id, state);
`, `
  ALTER TABLE activities ADD COLUMN updated_at TEXT;
  CREATE TABLE activity_triage_accounting (
    activity_id TEXT PRIMARY KEY REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED,
    attempts_used INTEGER NOT NULL DEFAULT 0, auto_answers_used INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL
  ) STRICT;
`, `
  CREATE TABLE activity_steers (
    steer_id TEXT PRIMARY KEY, activity_id TEXT NOT NULL REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED,
    execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, operation_id TEXT NOT NULL UNIQUE,
    fingerprint TEXT NOT NULL, state TEXT NOT NULL CHECK (state IN ('pending_enqueue','queued','delivered','not_applied','unknown','rejected')),
    revision INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, settled_at TEXT, payload_json TEXT NOT NULL
  ) STRICT;
  CREATE INDEX activity_steers_by_execution ON activity_steers (activity_id, execution_id, created_at);
`, 'SELECT 1;'];
const LATEST_SCHEMA_VERSION = MIGRATIONS.length;

type Snapshot = { data: Buffer; digest: string };
type Mirror = { snapshot: Snapshot; previous: string | null; temp: string };
export type SkillApprovalChange = { put: SkillApproval } | { source: SkillSource; skillId: string; botId?: string } | { credentialServerId: string } | { workspaceBotId: string };

const encode = (value: unknown): string | null => JSON.stringify(value) ?? null;
const decode = (value: SQLOutputValue): unknown => value === null ? undefined : JSON.parse(String(value));
const sha256 = (data: Buffer) => createHash('sha256').update(data).digest('hex');

export type RoutingWrite = { credentials?: PrivateConnection; escalation?: Escalation };
export type ActivityControlWrite = { approvalRequests?: SkillApprovalRequest[]; resetProgress?: string;
  triage?: { activityId: string; kind: 'attempt' | 'answer'; at: string };
  admitSteer?: ActivitySteerRecord; steerUpdates?: ActivitySteer[] };
function snapshotFor(profile: ProductProfile): Snapshot {
  const data = Buffer.from(canonicalJSON(profile) + '\n');
  return { data, digest: sha256(data) };
}

export class ProductSQLite {
  private readonly databasePath: string;
  private readonly db: DatabaseSync;
  private digest?: string;

  constructor(private readonly profilePath: string, private readonly credentialsPath: string, private readonly skills?: BotSkills, private readonly vault?: McpVault) {
    const directory = dirname(profilePath);
    mkdirSync(directory, { recursive: true, mode: 0o700 });
    this.databasePath = basename(profilePath) === 'contacts.json' ? join(directory, 'nagi.db') : `${profilePath}.sqlite`;
    this.db = new DatabaseSync(this.databasePath);
    try {
      try { chmodSync(this.databasePath, 0o600); } catch {}
      this.db.exec('PRAGMA busy_timeout = 5000; PRAGMA foreign_keys = ON;');
      this.transaction(() => {
        const version = Number(this.db.prepare('PRAGMA user_version').get()?.user_version);
        if (version > LATEST_SCHEMA_VERSION) throw new RequestError('profile_unavailable', 'The SQLite profile uses a newer schema version');
        for (let index = version; index < LATEST_SCHEMA_VERSION; index++) {
          this.db.exec(MIGRATIONS[index]);
          this.db.exec(`PRAGMA user_version = ${index + 1}`);
        }
      });
      this.db.exec('PRAGMA journal_mode = WAL; PRAGMA synchronous = FULL;');
    } catch (error) {
      this.close();
      throw error;
    }
  }

  credentials(): PrivateConnection | undefined {
    this.publishCredentials();
    return readPrivateConnection(this.credentialsPath);
  }

  private publishCredentials(): void {
    this.transaction(() => {
      const token = this.meta('pending_provider_credentials');
      if (!token) return;
      if (!/^[0-9a-f-]{36}$/.test(token)) throw new RequestError('profile_unavailable', 'Invalid credential publication reference');
      const staged = `${this.credentialsPath}.${token}.tmp`;
      try { renameSync(staged, this.credentialsPath); }
      catch (error) {
        if ((error as NodeJS.ErrnoException).code !== 'ENOENT' || readPrivateConnection(this.credentialsPath)?.publicationId !== token) throw error;
      }
      this.db.exec("DELETE FROM meta WHERE key = 'pending_provider_credentials'");
    });
  }

  open(paths: ProductProfilePaths, credentials: PrivateConnection | undefined, installationId: string): ProductProfile {
    let mirror: Mirror | undefined;
    let staged: string | undefined;
    let committed = false;
    try {
      this.publishMcpVault();
      this.vault?.load();
      this.publishSkills();
      const profile = this.transaction(() => {
        for (const suffix of ['-wal', '-shm']) {
          try { chmodSync(this.databasePath + suffix, 0o600); } catch {}
        }
        const snapshot = this.readMirror();
        const previous = snapshot?.digest ?? null;
        const digest = this.meta('snapshot_sha256');
        const pending = this.meta('pending_snapshot_previous');
        if (digest !== undefined && (previous === null || previous === digest || pending === encode(previous))) {
          const profile = this.readProfile();
          this.digest = digest;
          if (pending !== undefined && previous !== digest) {
            const restored = snapshotFor(profile);
            if (restored.digest !== digest) throw new RequestError('profile_unavailable', 'The SQLite profile does not match its pending snapshot');
            mirror = this.stageMirror(restored, previous);
          } else if (pending !== undefined) {
            this.clearPending();
          }
          return profile;
        }
        if (!snapshot && !paths.fresh) throw new RequestError('profile_unavailable', 'The product profile is missing');
        const { profile, backup } = importProductSnapshot(snapshot && JSON.parse(snapshot.data.toString('utf8')), credentials);
        mirror = this.stageMirror(snapshotFor(profile), previous);
        this.db.exec('DELETE FROM execution_mutation_admissions; DELETE FROM execution_admission_coverage; DELETE FROM activity_escalations;');
        this.db.exec('DELETE FROM activity_progress; DELETE FROM skill_approval_requests; DELETE FROM activity_triage_accounting; DELETE FROM activity_steers;');
        this.replaceProfile(profile, mirror);
        for (const row of backup?.activityProgress ?? []) this.putProgress(row);
        for (const row of backup?.skillApprovalRequests ?? []) this.putApprovalRequest(row);
        for (const row of backup?.activityEscalations ?? []) this.putEscalation(row);
        for (const row of backup?.activitySteers ?? []) this.insertSteer(row);
        const triage = this.db.prepare('INSERT INTO activity_triage_accounting VALUES (?, ?, ?, ?)');
        for (const row of backup?.triageAccounting ?? []) triage.run(encode(row.activityId), row.attemptsUsed, row.autoAnswersUsed, row.updatedAt);
        const redact = projectionRedactor([...privateCredentialKeys(credentials), ...this.vault?.keys() ?? []]);
        const writes: SkillWrite[] = (backup?.privateSkillBundles ?? []).map(row => {
          const { 'SKILL.md': markdown, ...files } = backupSkillFiles(row);
          checkedSkillFiles({ ...files, 'SKILL.md': markdown }, redact);
          return { source: 'private', botId: row.ownerBotId, skillId: row.skillId, markdown, files };
        });
        if (writes.length) {
          staged = this.skills!.stage(writes);
          this.db.prepare("INSERT INTO meta (key, value) VALUES ('pending_skill_stage', ?)").run(staged);
        }
        this.replaceTraces(backup, redact);
        const trusted = new Map(this.skillApprovals().map(row => [row.grantKey, row]));
        this.db.exec('DELETE FROM skill_approvals');
        for (const row of backup?.skillApprovals ?? []) {
          if (row.mode === 'pure_compute_tested' || (row.installationId === installationId && canonicalJSON(trusted.get(row.grantKey)) === canonicalJSON(row))) {
            this.putApproval(row);
          }
        }
        return profile;
      });
      committed = true;
      this.publishSkills();
      if (mirror) this.publish(mirror);
      return profile;
    } catch (error) {
      if (staged && !committed) this.skills?.discard(staged);
      if (mirror) rmSync(mirror.temp, { force: true });
      if (error instanceof RequestError) throw error;
      throw new RequestError('profile_unavailable', 'The saved product profile could not be read or imported');
    }
  }

  write(profile: ProductProfile, before: ProductProfile, skillWrite?: SkillWrite, mcpWrite?: McpVaultWrite, approval?: SkillApprovalChange,
    routing?: RoutingWrite, control?: ActivityControlWrite): TraceUpdate[] {
    let mirror: Mirror | undefined;
    let staged: string | undefined;
    let stagedVault: string | undefined;
    let stagedCredentials: string | undefined;
    let committed = false;
    try {
      const updates = this.transaction(() => {
        if (this.meta('snapshot_sha256') !== this.digest) throw new RequestError('profile_unavailable', 'The SQLite profile changed in another worker');
        const previous = this.readMirror()?.digest ?? null;
        if (previous !== null && previous !== this.digest && this.meta('pending_snapshot_previous') !== encode(previous)) {
          throw new RequestError('profile_unavailable', 'The recovery snapshot changed while the profile was open');
        }
        if (skillWrite) {
          staged = this.skills!.stage([skillWrite]);
          this.db.prepare("INSERT INTO meta (key, value) VALUES ('pending_skill_stage', ?)").run(staged);
        }
        if (mcpWrite && this.vault) {
          stagedVault = this.vault.stage(mcpWrite);
          this.db.prepare("INSERT INTO meta (key, value) VALUES ('pending_mcp_vault', ?)").run(stagedVault);
        }
        if (routing?.credentials) {
          const token = randomUUID();
          const path = `${this.credentialsPath}.${token}.tmp`;
          stagedCredentials = path;
          writeFileSync(path, JSON.stringify({ ...routing.credentials, publicationId: token }) + '\n', { flag: 'wx', mode: 0o600, flush: true });
          this.db.prepare("INSERT INTO meta (key, value) VALUES ('pending_provider_credentials', ?)").run(token);
        }
        if (routing?.escalation && !this.canEscalate(routing.escalation.activityId, routing.escalation.priorExecutionId)) {
          throw new RequestError('escalation_unavailable', 'The prior execution does not admit escalation');
        }
        if (control?.triage) this.updateTriage(control.triage);
        mirror = this.stageMirror(snapshotFor(profile), previous);
        this.replaceProfile(profile, mirror);
        if (routing?.escalation) this.putEscalation(routing.escalation);
        if (approval) this.changeApproval(approval);
        if (control?.resetProgress) this.db.prepare('DELETE FROM activity_progress WHERE activity_id = ?').run(encode(control.resetProgress));
        for (const row of control?.approvalRequests ?? []) this.putApprovalRequest(row);
        if (control?.admitSteer) this.insertSteer(control.admitSteer);
        for (const row of control?.steerUpdates ?? []) this.transitionSteer(row);
        const workspaces = new Map(before.bots.map(bot => [bot.botId, bot.profile.workspace]));
        for (const bot of profile.bots) {
          if (workspaces.has(bot.botId) && workspaces.get(bot.botId) !== bot.profile.workspace) this.changeApproval({ workspaceBotId: bot.botId });
        }
        return this.recordTransitions(before, profile);
      });
      committed = true;
      if (stagedCredentials) this.publishCredentials();
      this.publishMcpVault();
      this.publishSkills();
      this.publish(mirror!);
      return updates;
    } catch (error) {
      if (staged && !committed) this.skills?.discard(staged);
      if (stagedVault && !committed) this.vault?.discard(stagedVault);
      if (stagedCredentials && !committed) rmSync(stagedCredentials, { force: true });
      if (mirror) rmSync(mirror.temp, { force: true });
      throw error;
    }
  }

  close(): void {
    this.db.close();
  }

  private publishSkills(): void {
    if (this.meta('pending_skill_stage') === undefined) return;
    const staged = this.transaction(() => {
      const name = this.meta('pending_skill_stage');
      if (name === undefined) return;
      if (!this.skills) throw new RequestError('profile_unavailable', 'A staged skill update requires the profile skill library');
      this.skills.publish(name);
      this.db.exec("DELETE FROM meta WHERE key = 'pending_skill_stage'");
      return name;
    });
    if (staged) this.skills!.discard(staged);
  }

  private publishMcpVault(): void {
    if (this.meta('pending_mcp_vault') === undefined) return;
    this.transaction(() => {
      const name = this.meta('pending_mcp_vault');
      if (name === undefined) return;
      if (!this.vault) throw new RequestError('profile_unavailable', 'The private MCP credential vault is unavailable');
      this.vault.publish(name);
      this.db.exec("DELETE FROM meta WHERE key = 'pending_mcp_vault'");
    });
  }

  appendTrace(row: TraceObservation): TraceUpdate | undefined {
    if (Buffer.byteLength(traceDetail(row)) > 1024) return;
    return this.transaction(() => {
      if (this.meta('snapshot_sha256') !== this.digest) throw new RequestError('profile_unavailable', 'The SQLite profile changed in another worker');
      return this.insertTrace(row);
    });
  }

  traceBounds(activityId: string, executionId?: string) {
    const where = executionId === undefined ? 'activity_id = ?' : 'activity_id = ? AND execution_id = ?';
    const ids = executionId === undefined ? [encode(activityId)] : [encode(activityId), encode(executionId)];
    const head = this.db.prepare(`SELECT max(seq) AS seq FROM activity_traces WHERE ${where}`).get(...ids);
    const meta = this.db.prepare(`SELECT count(*) AS recorded, max(evicted_through_seq) AS evicted
      FROM activity_trace_meta WHERE ${where} AND coverage = 'recorded'`).get(...ids);
    return { generation: this.meta('trace_generation')!, high: Number(head?.seq ?? 0),
      recorded: Number(meta?.recorded ?? 0), evicted: Number(meta?.evicted ?? 0) };
  }

  activityTraceTimestamp(activityId: string): string | undefined {
    const row = this.db.prepare('SELECT at FROM activity_traces WHERE activity_id = ? ORDER BY seq DESC LIMIT 1').get(encode(activityId));
    return row?.at as string | undefined;
  }
  admitTool(executionId: string, activityId: string | undefined, callId: string, tool: string, toolClass: 'readonly' | 'mutating'): void {
    this.transaction(() => {
      if (this.meta('snapshot_sha256') !== this.digest ||
          !this.db.prepare("SELECT 1 FROM executions WHERE execution_id = ? AND state IN ('starting', 'running')").get(encode(executionId))) {
        throw new RequestError('profile_unavailable', 'The execution is no longer admitted');
      }
      this.db.prepare(`INSERT INTO execution_mutation_admissions (execution_id, activity_id, call_id, tool_name, tool_class, state, at)
        VALUES (?, ?, ?, ?, ?, 'admitted', ?)`).run(encode(executionId), encode(activityId), callId, tool, toolClass, new Date().toISOString());
    });
  }

  settleTool(executionId: string, callId: string, isError: boolean): void {
    this.db.prepare(`UPDATE execution_mutation_admissions SET state = ? WHERE admission_id = (SELECT max(admission_id)
      FROM execution_mutation_admissions WHERE execution_id = ? AND call_id = ? AND state = 'admitted')`)
      .run(isError ? 'settled_error' : 'settled_ok', encode(executionId), callId);
  }

  uncertainMutation(executionId: string): boolean {
    return !!this.db.prepare(`SELECT 1 FROM execution_mutation_admissions WHERE execution_id = ? AND tool_class = 'mutating'
      AND state IN ('admitted', 'unknown') LIMIT 1`).get(encode(executionId));
  }

  progress(activityId: string): ActivityProgressRecord | undefined {
    const row = this.db.prepare('SELECT record_json FROM activity_progress WHERE activity_id = ?').get(encode(activityId));
    return decode(row?.record_json ?? null) as ActivityProgressRecord | undefined;
  }

  recordProgress(row: Omit<ActivityProgressRecord, 'progressSeq'>, interval: number): ActivityProgressRecord | undefined {
    return this.transaction(() => {
      if (this.meta('snapshot_sha256') !== this.digest) throw new RequestError('profile_unavailable', 'The SQLite profile changed in another worker');
      const previous = this.progress(row.activityId);
      if (previous && (previous.callId === row.callId ||
          previous.step === row.step && Date.parse(row.at) - Date.parse(previous.at) < interval)) return;
      const next = { ...row, progressSeq: (previous?.progressSeq ?? 0) + 1 };
      this.putProgress(next);
      return next;
    });
  }

  private putProgress(row: ActivityProgressRecord): void {
    this.db.prepare('INSERT OR REPLACE INTO activity_progress VALUES (?, ?, ?, ?, ?)').run(
      encode(row.activityId), encode(row.executionId), encode(row.callId), row.progressSeq, encode(row));
  }

  approvalRequests(activityId: string): SkillApprovalRequest[] {
    return this.db.prepare('SELECT record_json FROM skill_approval_requests WHERE activity_id = ? ORDER BY rowid')
      .all(encode(activityId)).map(row => decode(row.record_json) as SkillApprovalRequest);
  }

  approvalRequest(requestId: string): SkillApprovalRequest | undefined {
    const row = this.db.prepare('SELECT record_json FROM skill_approval_requests WHERE request_id = ?').get(encode(requestId));
    return decode(row?.record_json ?? null) as SkillApprovalRequest | undefined;
  }

  private putApprovalRequest(row: SkillApprovalRequest): void {
    this.db.prepare(`INSERT INTO skill_approval_requests VALUES (?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(request_id) DO UPDATE SET revision = excluded.revision, state = excluded.state, record_json = excluded.record_json`)
      .run(encode(row.requestId), encode(row.activityId), encode(row.executionId), encode(row.callId), encode(row.botId), row.revision, row.state, encode(row));
  }

  private steerReceipt(row: Record<string, SQLOutputValue>): ActivitySteer {
    return { steerId: decode(row.steer_id) as string, operationId: decode(row.operation_id) as string,
      activityId: decode(row.activity_id) as string, executionId: decode(row.execution_id) as string,
      state: row.state as ActivitySteer['state'], revision: Number(row.revision), ...(typeof row.reason === 'string' ? { reason: row.reason } : {}) };
  }

  steerStatus(steerId?: string, operationId?: string): ActivitySteer | undefined {
    const row = this.db.prepare(`SELECT steer_id, operation_id, activity_id, execution_id, state, revision, payload_json ->> '$.reason' AS reason
      FROM activity_steers WHERE ${steerId !== undefined ? 'steer_id' : 'operation_id'} = ?`).get(encode(steerId ?? operationId));
    if (row) return this.steerReceipt(row);
  }

  activitySteers(activityId: string): ActivitySteer[] {
    return this.db.prepare(`SELECT steer_id, operation_id, activity_id, execution_id, state, revision, payload_json ->> '$.reason' AS reason
      FROM activity_steers WHERE activity_id = ? ORDER BY rowid`).all(encode(activityId)).map(row => this.steerReceipt(row));
  }

  pendingSteers(): ActivitySteer[] {
    return this.db.prepare(`SELECT steer_id, operation_id, activity_id, execution_id, state, revision FROM activity_steers
      WHERE state IN ('pending_enqueue', 'queued') ORDER BY rowid`).all().map(row => this.steerReceipt(row));
  }

  private insertSteer(row: ActivitySteerRecord): void {
    this.db.prepare('INSERT INTO activity_steers VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)').run(encode(row.steerId), encode(row.activityId),
      encode(row.executionId), encode(row.operationId), row.fingerprint, row.state, row.revision, row.createdAt, row.settledAt ?? null,
      encode({ ...row.payload, reason: row.reason }));
  }

  private transitionSteer(row: ActivitySteer): void {
    const settledAt = row.state === 'queued' ? null : new Date().toISOString();
    const result = this.db.prepare(`UPDATE activity_steers SET state = ?, revision = ?, settled_at = ?,
      payload_json = CASE WHEN ? IS NULL THEN payload_json ELSE json_set(payload_json, '$.reason', ?) END
      WHERE steer_id = ? AND revision = ? AND state IN ('pending_enqueue','queued')`)
      .run(row.state, row.revision, settledAt, row.reason ?? null, row.reason ?? null, encode(row.steerId), row.revision - 1);
    if (result.changes !== 1) throw new RequestError('profile_unavailable', 'The steering receipt changed in another worker');
  }

  canEscalate(activityId: string, executionId: string): boolean {
    return !!this.db.prepare(`SELECT 1 FROM executions e JOIN execution_admission_coverage c USING (execution_id)
      JOIN activity_trace_meta t USING (execution_id)
      WHERE e.execution_id = ? AND t.activity_id = ? AND t.coverage = 'recorded' AND e.state IN ('failed', 'interrupted')
      AND NOT EXISTS (SELECT 1 FROM execution_mutation_admissions WHERE execution_id = e.execution_id AND tool_class = 'mutating')
      AND NOT EXISTS (SELECT 1 FROM activity_steers WHERE activity_id = t.activity_id AND execution_id = e.execution_id AND state != 'rejected')
      AND NOT EXISTS (SELECT 1 FROM activity_escalations WHERE activity_id = t.activity_id)`).get(encode(executionId), encode(activityId));
  }

  private putEscalation(row: Escalation): void {
    this.db.prepare('INSERT INTO activity_escalations VALUES (?, ?, ?, ?, ?, ?)').run(encode(row.activityId), encode(row.priorExecutionId),
      encode(row.escalatedExecutionId), row.fromRole, row.toRole, row.at);
  }


  traceRows(activityId: string, executionId: string | undefined, after: number, through: number, limit: number): TraceItem[] {
    const where = executionId === undefined ? 'activity_id = ?' : 'activity_id = ? AND execution_id = ?';
    const ids = executionId === undefined ? [encode(activityId)] : [encode(activityId), encode(executionId)];
    return this.db.prepare(`SELECT * FROM activity_traces WHERE ${where} AND seq > ? AND seq <= ? ORDER BY seq LIMIT ?`)
      .all(...ids, after, through, limit).map(row => this.traceItem(row));
  }

  skillApprovals(): SkillApproval[] {
    return this.db.prepare('SELECT record_json FROM skill_approvals ORDER BY grant_key').all().map(row => decode(row.record_json) as SkillApproval);
  }

  skillApproval(grantKey: string): SkillApproval | undefined {
    return decode(this.db.prepare('SELECT record_json FROM skill_approvals WHERE grant_key = ?').get(grantKey)?.record_json ?? null) as SkillApproval | undefined;
  }

  private putApproval(row: SkillApproval): void {
    this.db.prepare('INSERT OR REPLACE INTO skill_approvals VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)').run(row.grantKey, row.ownerBotId,
      row.source, row.skillId, row.approvalDigest, row.permissionsDigest, row.mode, row.installationId, row.approvedAt, encode(row));
  }

  private changeApproval(change: SkillApprovalChange): void {
    if ('put' in change) {
      this.db.prepare('DELETE FROM skill_approvals WHERE owner_bot_id = ? AND source = ? AND skill_id = ?').run(change.put.ownerBotId, change.put.source, change.put.skillId);
      this.putApproval(change.put);
    } else if ('workspaceBotId' in change) {
      this.db.prepare('DELETE FROM skill_approvals WHERE owner_bot_id = ?').run(change.workspaceBotId);
    } else if ('credentialServerId' in change) {
      this.db.prepare(`DELETE FROM skill_approvals WHERE EXISTS (SELECT 1 FROM json_each(record_json, '$.credentialRefs')
        WHERE instr(value, ?) = 1)`).run(`mcp:${change.credentialServerId}:`);
    } else if (change.botId === undefined) {
      this.db.prepare('DELETE FROM skill_approvals WHERE source = ? AND skill_id = ?').run(change.source, change.skillId);
    } else {
      this.db.prepare('DELETE FROM skill_approvals WHERE owner_bot_id = ? AND source = ? AND skill_id = ?').run(change.botId, change.source, change.skillId);
    }
  }

  triageAccounting(activityId: string): TriageAccounting | undefined {
    const row = this.db.prepare('SELECT * FROM activity_triage_accounting WHERE activity_id = ?').get(encode(activityId));
    if (row) return { activityId, attemptsUsed: Number(row.attempts_used), autoAnswersUsed: Number(row.auto_answers_used), updatedAt: String(row.updated_at) };
  }

  private updateTriage({ activityId, kind, at }: NonNullable<ActivityControlWrite['triage']>): void {
    const result = kind === 'attempt' ? this.db.prepare(`INSERT INTO activity_triage_accounting VALUES (?, 1, 0, ?)
      ON CONFLICT(activity_id) DO UPDATE SET attempts_used = attempts_used + 1, updated_at = excluded.updated_at
      WHERE attempts_used < ? AND auto_answers_used < ?`).run(encode(activityId), at, maxTriageAttempts, maxAutomaticAnswers) :
      this.db.prepare(`UPDATE activity_triage_accounting SET auto_answers_used = auto_answers_used + 1, updated_at = ?
        WHERE activity_id = ? AND auto_answers_used < ? AND auto_answers_used < attempts_used`).run(at, encode(activityId), maxAutomaticAnswers);
    if (result.changes !== 1) throw new RequestError('triage_budget', 'The Activity preference resolution budget is exhausted');
  }

  captureBackup(redact: (text: string) => string): { profile: ProductProfile; backup: ProductBackup } {
    return this.transaction(() => {
      if (this.meta('snapshot_sha256') !== this.digest) throw new RequestError('profile_unavailable', 'The SQLite profile changed in another worker');
      const profile = this.readProfile();
      const traces = this.db.prepare('SELECT * FROM activity_traces ORDER BY seq').all().map(row => redactTrace(this.traceItem(row), redact));
      const traceCoverage = this.db.prepare('SELECT * FROM activity_trace_meta ORDER BY execution_id').all().map(row => ({
        activityId: decode(row.activity_id) as string, executionId: decode(row.execution_id) as string,
        coverage: row.coverage as 'recorded' | 'unrecorded', evictedThroughSeq: Number(row.evicted_through_seq),
      }));
      const activityEscalations = this.db.prepare('SELECT * FROM activity_escalations ORDER BY activity_id').all().map(row => ({
        activityId: decode(row.activity_id), priorExecutionId: decode(row.prior_execution_id), escalatedExecutionId: decode(row.escalated_execution_id),
        fromRole: row.from_role, toRole: row.to_role, at: row.at,
      })) as Escalation[];
      const activityProgress = this.db.prepare('SELECT record_json FROM activity_progress ORDER BY activity_id').all()
        .map(row => decode(row.record_json) as ActivityProgressRecord);
      const skillApprovalRequests = this.db.prepare('SELECT record_json FROM skill_approval_requests ORDER BY request_id').all()
        .map(row => decode(row.record_json) as SkillApprovalRequest);
      const triageAccounting = this.db.prepare('SELECT * FROM activity_triage_accounting ORDER BY activity_id').all().map(row => ({
        activityId: decode(row.activity_id) as string, attemptsUsed: Number(row.attempts_used), autoAnswersUsed: Number(row.auto_answers_used), updatedAt: String(row.updated_at),
      }));
      const activitySteers: ActivitySteerRecord[] = this.db.prepare('SELECT * FROM activity_steers ORDER BY rowid').all().map(row => {
        const { reason, ...payload } = decode(row.payload_json) as ActivitySteerPayload & { reason?: string };
        return { ...this.steerReceipt(row), reason, payload, fingerprint: String(row.fingerprint), createdAt: String(row.created_at),
          settledAt: row.settled_at === null ? undefined : String(row.settled_at) };
      });
      return { profile, backup: { version: 1, traces, traceCoverage, ...(activityEscalations.length ? { activityEscalations } : {}),
        ...(activityProgress.length ? { activityProgress } : {}), ...(skillApprovalRequests.length ? { skillApprovalRequests } : {}),
        ...(triageAccounting.length ? { triageAccounting } : {}), ...(activitySteers.length ? { activitySteers } : {}) } };
    });
  }

  private traceItem(row: Record<string, SQLOutputValue>): TraceItem {
    return { ...(decode(row.detail_json) as object), seq: row.seq, activityId: decode(row.activity_id),
      executionId: decode(row.execution_id), at: row.at, kind: row.kind } as TraceItem;
  }

  private replaceTraces(backup: TraceBackup | undefined, redact: (text: string) => string): void {
    this.db.exec('DELETE FROM activity_traces; DELETE FROM activity_trace_meta;');
    this.db.prepare("UPDATE meta SET value = ? WHERE key = 'trace_generation'").run(randomUUID());
    if (!backup) return;
    const meta = this.db.prepare('INSERT INTO activity_trace_meta (execution_id, activity_id, coverage, evicted_through_seq) VALUES (?, ?, ?, ?)');
    const insert = this.db.prepare('INSERT INTO activity_traces (seq, activity_id, execution_id, at, kind, detail_json) VALUES (?, ?, ?, ?, ?, ?)');
    let high = 0;
    for (const row of backup.traceCoverage) {
      meta.run(encode(row.executionId), encode(row.activityId), row.coverage, row.evictedThroughSeq);
      high = Math.max(high, row.evictedThroughSeq);
    }
    for (const row of backup.traces) {
      insert.run(row.seq, encode(row.activityId), encode(row.executionId), row.at, row.kind, traceDetail(redactTrace(row, redact)));
    }
    if (high > 0) {
      this.db.prepare("INSERT INTO sqlite_sequence(name, seq) SELECT 'activity_traces', ? WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'activity_traces')").run(high);
      this.db.prepare("UPDATE sqlite_sequence SET seq = max(seq, ?) WHERE name = 'activity_traces'").run(high);
    }
  }

  private recordTransitions(before: ProductProfile, profile: ProductProfile): TraceUpdate[] {
    const oldActivities = new Map(before.activities.map(row => [row.activityId, row]));
    const oldExecutions = new Map(before.executions.map(row => [row.executionId, row.state]));
    const executions = new Map(profile.executions.map(row => [row.executionId, row.state]));
    const admitExecution = this.db.prepare('INSERT INTO execution_admission_coverage VALUES (?)');
    const markUnknown = this.db.prepare("UPDATE execution_mutation_admissions SET state = 'unknown' WHERE execution_id = ? AND state = 'admitted'");
    for (const [executionId, state] of executions) {
      const prior = oldExecutions.get(executionId);
      if (prior === undefined) admitExecution.run(encode(executionId));
      else if (prior !== state && state !== 'starting' && state !== 'running') markUnknown.run(encode(executionId));
    }
    const coverage = this.db.prepare("SELECT 1 FROM activity_trace_meta WHERE execution_id = ? AND coverage = 'recorded'");
    const admit = this.db.prepare("INSERT INTO activity_trace_meta (execution_id, activity_id, coverage) VALUES (?, ?, 'recorded')");
    const updates: TraceUpdate[] = [];
    for (const { activityId, state, executionIds, revision } of profile.activities) {
      const previous = oldActivities.get(activityId);
      if (previous?.revision === revision && previous.executionIds.every(id => oldExecutions.get(id) === executions.get(id))) continue;
      for (const executionId of executionIds) {
        const executionState = executions.get(executionId)!;
        const priorState = oldExecutions.get(executionId);
        const activityChanged = executionId === executionIds.at(-1) && previous?.state !== state;
        if (!activityChanged && priorState === executionState) continue;
        if (priorState === undefined) admit.run(encode(executionId), encode(activityId));
        else if (!coverage.get(encode(executionId))) continue;
        if (activityChanged) updates.push(this.insertTrace({ activityId, executionId, kind: 'state_changed', scope: 'activity', state }));
        if (priorState !== executionState) updates.push(this.insertTrace({ activityId, executionId, kind: 'state_changed', scope: 'execution', state: executionState }));
      }
    }
    return updates;
  }

  private insertTrace(row: TraceObservation): TraceUpdate {
    const activityId = encode(row.activityId);
    const executionId = encode(row.executionId);
    const result = this.db.prepare('INSERT INTO activity_traces (activity_id, execution_id, at, kind, detail_json) VALUES (?, ?, ?, ?, ?)')
      .run(activityId, executionId, new Date().toISOString(), row.kind, traceDetail(row));
    const old = this.db.prepare(`DELETE FROM activity_traces WHERE seq IN (SELECT seq FROM activity_traces
      WHERE activity_id = ? AND execution_id = ? ORDER BY seq DESC LIMIT -1 OFFSET ?) RETURNING execution_id, seq`)
      .all(activityId, executionId, maxExecutionTraces);
    const count = Number(this.db.prepare('SELECT count(*) AS total FROM activity_traces').get()?.total);
    if (count > maxProfileTraces) {
      old.push(...this.db.prepare(`DELETE FROM activity_traces WHERE seq IN (SELECT seq FROM activity_traces ORDER BY seq LIMIT ?)
        RETURNING execution_id, seq`).all(count - maxProfileTraces));
    }
    if (old.length) {
      const evict = this.db.prepare('UPDATE activity_trace_meta SET evicted_through_seq = max(evicted_through_seq, ?) WHERE execution_id = ?');
      for (const { execution_id, seq } of old) evict.run(seq, execution_id);
    }
    return { activityId: row.activityId, executionId: row.executionId, lastSeq: Number(result.lastInsertRowid) };
  }


  private transaction<T>(action: () => T): T {
    this.db.exec('BEGIN IMMEDIATE');
    try {
      const result = action();
      this.db.exec('COMMIT');
      return result;
    } catch (error) {
      this.db.exec('ROLLBACK');
      throw error;
    }
  }

  private meta(key: string): string | undefined {
    return this.db.prepare('SELECT value FROM meta WHERE key = ?').get(key)?.value as string | undefined;
  }

  private readMirror(): Snapshot | undefined {
    try {
      const data = readFileSync(this.profilePath);
      return { data, digest: sha256(data) };
    } catch (error) {
      const code = (error as NodeJS.ErrnoException).code;
      if (code === 'ENOENT') return;
      if (code === 'EISDIR') throw new Error(`Cannot rename a recovery snapshot over directory ${this.profilePath}`);
      throw error;
    }
  }

  private stageMirror(snapshot: Snapshot, previous: string | null): Mirror {
    const temp = `${this.profilePath}.${randomUUID()}.tmp`;
    try { writeFileSync(temp, snapshot.data, { flag: 'wx', mode: 0o600, flush: true }); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'EEXIST') rmSync(temp, { force: true });
      throw error;
    }
    return { snapshot, previous, temp };
  }

  private publish(mirror: Mirror): void {
    this.digest = mirror.snapshot.digest;
    try {
      this.transaction(() => {
        if (this.meta('snapshot_sha256') !== this.digest || this.meta('pending_snapshot_previous') === undefined) return;
        const current = this.readMirror()?.digest ?? null;
        if (current !== mirror.previous && current !== this.digest) throw new Error('The recovery snapshot changed before publication');
        if (current !== this.digest) renameSync(mirror.temp, this.profilePath);
        this.clearPending();
      });
    } catch {
      process.stderr.write('The committed SQLite profile still has a pending recovery snapshot.\n');
    } finally {
      try { rmSync(mirror.temp, { force: true }); } catch {}
    }
  }

  private clearPending(): void {
    this.db.exec("DELETE FROM meta WHERE key = 'pending_snapshot_previous'");
  }

  searchWorkLogs(botId: string, query: string, dateFrom?: string, dateTo?: string, limit = 50): BotWorkLog[] {
    const text = query.trim();
    const like = `%${text.replace(/[\\%_]/g, char => `\\${char}`)}%`;
    const hasTokens = /[\p{L}\p{N}]/u.test(text);
    const match = `"${text.replaceAll('"', '""')}"`;
    const fullText = hasTokens ? 'OR rowid IN (SELECT rowid FROM bot_work_logs_fts WHERE bot_work_logs_fts MATCH ?)' : '';
    const statement = this.db.prepare(`SELECT record_json FROM bot_work_logs
      WHERE bot_id = ? AND date BETWEEN ? AND ?
      ${text ? `AND (summary LIKE ? ESCAPE '\\' OR tags_text LIKE ? ESCAPE '\\' ${fullText})` : ''}
      ORDER BY date DESC, created_at DESC, log_id DESC LIMIT ?`);
    const search = text ? [like, like, ...(hasTokens ? [match] : [])] : [];
    return statement.all(encode(botId), dateFrom ?? '0000-01-01', dateTo ?? '9999-12-31', ...search, limit)
      .map(row => decode(row.record_json) as BotWorkLog);
  }

  private readMemory(): unknown {
    if (this.meta('memory_present') === undefined) return undefined;
    const records = (table: string) => this.db.prepare(`SELECT record_json FROM ${table} ORDER BY rowid`).all().map(row => decode(row.record_json));
    return { rules: records('bot_memory_rules'), workLogs: records('bot_work_logs'), pendingLearnings: records('bot_pending_learnings'), meta: records('bot_memory_meta') };
  }

  private writeMemory(memory: BotMemory): void {
    const rules = this.db.prepare('INSERT INTO bot_memory_rules VALUES (?, ?, ?)');
    for (const row of memory.rules) rules.run(encode(row.ruleId), encode(row.botId), encode(row));
    const logs = this.db.prepare('INSERT INTO bot_work_logs VALUES (?, ?, ?, ?, ?, ?, ?)');
    for (const row of memory.workLogs) logs.run(encode(row.logId), encode(row.botId), row.date, row.createdAt, row.summary, row.tags.join(' '), encode(row));
    const pending = this.db.prepare('INSERT INTO bot_pending_learnings VALUES (?, ?, ?)');
    for (const row of memory.pendingLearnings) pending.run(encode(row.learningId), encode(row.botId), encode(row));
    const meta = this.db.prepare('INSERT INTO bot_memory_meta VALUES (?, ?)');
    for (const row of memory.meta) meta.run(encode(row.botId), encode(row));
  }

  private readProfile(): ProductProfile {
    const bots = this.db.prepare('SELECT * FROM bots ORDER BY sort_order').all().map(row => ({
      botId: decode(row.bot_id), profile: decode(row.profile_json), profileRevision: row.profile_revision,
      archived: row.archived === 1, currentConversationId: decode(row.current_conversation_id), retained: decode(row.retained_json),
      systemKind: row.system_kind ?? undefined,
    }));
    const conversations = this.db.prepare('SELECT * FROM conversations ORDER BY sort_order').all().map(row => ({
      conversationId: decode(row.conversation_id), botId: decode(row.bot_id), revision: row.revision, draft: decode(row.draft_json),
      binding: decode(row.binding_json), latestExecutionId: decode(row.latest_execution_id),
    }));
    const activities = this.db.prepare('SELECT * FROM activities ORDER BY sort_order').all().map(row => ({
      activityId: decode(row.activity_id), ownerBotId: decode(row.owner_bot_id), originConversationId: decode(row.origin_conversation_id),
      revision: row.revision, state: row.state, goal: decode(row.goal_json), requirements: decode(row.requirements_json),
      questions: decode(row.questions_json), materials: decode(row.materials_json), inputFiles: decode(row.input_files_json),
      executionIds: decode(row.execution_ids_json), results: decode(row.results_json), binding: decode(row.binding_json), retained: decode(row.retained_json),
      grantedLinkedActivityIds: row.granted_linked_activity_ids_json === '[]' ? undefined : decode(row.granted_linked_activity_ids_json),
      routing: decode(row.routing_json),
      acknowledgedAt: row.acknowledged_at ?? undefined, acknowledgedExecutionId: decode(row.acknowledged_execution_id),
      closedAt: row.closed_at ?? undefined,
      updatedAt: row.updated_at ?? undefined,
    }));
    const executions = this.db.prepare('SELECT * FROM executions ORDER BY sort_order').all().map(row => {
      const { route, ...input } = decode(row.input_json) as ProductProfile['executions'][number]['input'] &
        Pick<ProductProfile['executions'][number], 'route'>;
      return { executionId: decode(row.execution_id), revision: row.revision, state: row.state, operationId: decode(row.operation_id),
        input, route, error: decode(row.error_json), engineRunId: decode(row.engine_run_id),
        importedActivityId: decode(row.imported_activity_id), retained: decode(row.retained_json) };
    });
    const operations = this.db.prepare('SELECT * FROM operations ORDER BY sort_order').all().map(row => ({
      method: decode(row.method_json), fingerprint: row.fingerprint, receipt: decode(row.receipt_json), reply: decode(row.reply_json),
      replyError: decode(row.reply_error_json), connectionRevision: row.connection_revision ?? undefined, retained: decode(row.retained_json),
    }));
    const connections = this.db.prepare('SELECT * FROM connections ORDER BY sort_order').all().map(row => ({
      revision: row.revision, sourceId: decode(row.source_id), configuration: decode(row.configuration_json),
    }));
    const mcpServers = this.db.prepare('SELECT config_json, catalog_json FROM mcp_servers ORDER BY sort_order').all().map(row => ({
      ...decode(row.config_json) as object, catalog: decode(row.catalog_json),
    }));
    const read = (key: string) => decode(this.meta(key) ?? null);
    const providerAccounts = this.db.prepare('SELECT config_json FROM provider_accounts ORDER BY sort_order').all().map(row => decode(row.config_json));
    const providerSavedRevision = read('provider_saved_revision');
    return decodeProductProfile({ version: 1, bots, conversations, activities, executions, operations, connections,
      savedRevision: read('saved_revision'), appliedRevision: read('applied_revision'), preferences: read('preferences_json'),
      issueToolScopes: read('issue_tool_scopes_json'), untargetedReceipts: read('untargeted_receipts_json'), recovery: read('recovery_json'),
      memory: this.readMemory(),
      ...(mcpServers.length ? { mcpServers } : {}),
      ...(providerAccounts.length ? { providerAccounts } : {}),
      providerEffectiveAccounts: read('provider_effective_accounts'), providerEffectivePolicy: read('provider_effective_policy'),
      routingPolicy: providerSavedRevision === undefined ? undefined : read('routing_policy'),
      providerSavedRevision, providerEffectiveRevision: read('provider_effective_revision'),
      routingOverrides: read('routing_overrides'), providerEffectiveOverrides: read('provider_effective_overrides'),
    });
  }

  private replaceProfile(profile: ProductProfile, mirror: Mirror): void {
    this.db.exec(`DELETE FROM operations; DELETE FROM activities; DELETE FROM conversations;
      DELETE FROM executions; DELETE FROM bots; DELETE FROM connections; DELETE FROM mcp_servers; DELETE FROM provider_accounts;`);
    const bots = this.db.prepare('INSERT INTO bots VALUES (?, ?, ?, ?, ?, ?, ?, ?)');
    profile.bots.forEach((row, order) => bots.run(encode(row.botId), order, encode(row.profile), row.profileRevision,
      Number(row.archived), encode(row.currentConversationId), encode(row.retained), row.systemKind ?? null));
    const conversations = this.db.prepare('INSERT INTO conversations VALUES (?, ?, ?, ?, ?, ?, ?)');
    profile.conversations.forEach((row, order) => conversations.run(encode(row.conversationId), order, encode(row.botId), row.revision,
      encode(row.draft), encode(row.binding), encode(row.latestExecutionId)));
    const activities = this.db.prepare(`INSERT INTO activities (activity_id, sort_order, owner_bot_id, origin_conversation_id,
      revision, state, goal_json, requirements_json, questions_json, materials_json, input_files_json, execution_ids_json,
      results_json, binding_json, retained_json, granted_linked_activity_ids_json, routing_json, acknowledged_at, acknowledged_execution_id, closed_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`);
    profile.activities.forEach((row, order) => activities.run(encode(row.activityId), order, encode(row.ownerBotId), encode(row.originConversationId),
      row.revision, row.state, encode(row.goal), encode(row.requirements), encode(row.questions), encode(row.materials), encode(row.inputFiles),
      encode(row.executionIds), encode(row.results), encode(row.binding), encode(row.retained), encode(row.grantedLinkedActivityIds ?? []), encode(row.routing),
      row.acknowledgedAt ?? null, encode(row.acknowledgedExecutionId), row.closedAt ?? null, row.updatedAt ?? null));
    const executions = this.db.prepare('INSERT INTO executions VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)');
    profile.executions.forEach((row, order) => executions.run(encode(row.executionId), order, row.revision, row.state, encode(row.operationId),
      encode(row.route ? { ...row.input, route: row.route } : row.input), encode(row.error), encode(row.engineRunId),
      encode(row.importedActivityId), encode(row.retained)));
    const connections = this.db.prepare('INSERT INTO connections VALUES (?, ?, ?, ?)');
    profile.connections.forEach((row, order) => connections.run(row.revision, order, encode(row.sourceId), encode(row.configuration)));
    if (profile.mcpServers?.length) {
      const servers = this.db.prepare('INSERT INTO mcp_servers VALUES (?, ?, ?, ?)');
      profile.mcpServers.forEach(({ catalog, ...config }, order) => servers.run(config.serverId, order, encode(config), encode(catalog)));
    }
    const providers = this.db.prepare('INSERT INTO provider_accounts VALUES (?, ?, ?)');
    profile.providerAccounts?.forEach((row, order) => providers.run(row.providerId, order, encode(row)));
    const operations = this.db.prepare('INSERT INTO operations VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)');
    profile.operations.forEach((row, order) => operations.run(encode(row.receipt.operationId), order, encode(row.method), row.fingerprint,
      encode(row.receipt), encode(row.reply), encode(row.replyError), row.connectionRevision ?? null, encode(row.retained)));
    if (profile.memory) this.writeMemory(profile.memory);
    const meta = this.db.prepare('INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)');
    const remove = this.db.prepare('DELETE FROM meta WHERE key = ?');
    for (const [key, value] of Object.entries({
      saved_revision: encode(profile.savedRevision), applied_revision: encode(profile.appliedRevision), preferences_json: encode(profile.preferences),
      issue_tool_scopes_json: encode(profile.issueToolScopes), untargeted_receipts_json: encode(profile.untargetedReceipts), recovery_json: encode(profile.recovery),
      memory_present: profile.memory ? '1' : null,
      routing_policy: encode(profile.routingPolicy ?? 'balanced'), provider_effective_policy: encode(profile.providerEffectivePolicy),
      provider_saved_revision: encode(profile.providerSavedRevision), provider_effective_revision: encode(profile.providerEffectiveRevision),
      provider_effective_accounts: encode(profile.providerEffectiveAccounts),
      routing_overrides: encode(profile.routingOverrides), provider_effective_overrides: encode(profile.providerEffectiveOverrides),
      snapshot_sha256: mirror.snapshot.digest, pending_snapshot_previous: encode(mirror.previous), updated_at: new Date().toISOString(),
    })) {
      if (value === null) remove.run(key);
      else meta.run(key, value);
    }
  }
}
