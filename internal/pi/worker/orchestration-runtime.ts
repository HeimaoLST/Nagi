import { createHash, randomUUID } from 'node:crypto';
import type { ToolDefinition } from '@earendil-works/pi-coding-agent';
import { Type, type Static, type TSchema } from 'typebox';
import { Value } from 'typebox/value';
import { canonicalJSON, decisionOption, noticeSourceRef, noticeUrgency, RequestError, type Operation, type OrchestrationChanged,
  type OrchestrationDecision, type OrchestrationMandate, type ProductOpenParams, type ProductParams, type StoredConversationNotice,
  type TimelineChanged } from './protocol.js';
import { emptyOrchestrationUsage, type EventBatch, type InboxEvent, type OrchestrationActor, type OrchestrationBackoff,
  type ProductSQLite, type RuntimeWatch } from './product-sqlite.js';
import type { ProductSession } from './product.js';
import type { ProviderAttempt } from './route-health.js';

const strict = { additionalProperties: false };
const summary = Type.String({ minLength: 1, maxLength: 2048, pattern: '\\S' });
const notify = Type.Object({ text: summary, urgency: Type.Optional(noticeUrgency), sourceRefs: Type.Optional(Type.Array(noticeSourceRef, { maxItems: 8 })) }, strict);
const requestDecision = Type.Object({ kind: Type.Optional(Type.String({ minLength: 1, maxLength: 64 })),
  purpose: Type.Optional(Type.String({ maxLength: 256 })), summary, options: Type.Optional(Type.Array(decisionOption, { maxItems: 8 })) }, strict);
const finish = Type.Object({ summary, observedStateHash: Type.String({ minLength: 1, maxLength: 128 }), actionClass: Type.Optional(Type.String({ maxLength: 128 })),
  progressMade: Type.Optional(Type.Boolean()) }, strict);
const hash = (data: unknown) => createHash('sha256').update(canonicalJSON(data)).digest('hex');
const date = (now: number) => new Date(now).toISOString();
const integerSetting = (name: string, fallback: number) => {
  const value = Number(process.env[name]);
  return Number.isSafeInteger(value) && value > 0 ? value : fallback;
};
const maxContextTokens = integerSetting('NAGI_ORCHESTRATION_MAX_CONTEXT_TOKENS', Number.MAX_SAFE_INTEGER);
const backoffBase = Math.min(integerSetting('NAGI_ORCHESTRATION_BACKOFF_BASE_MS', 1000), 86_400_000);
const backoffMax = Math.max(backoffBase, Math.min(integerSetting('NAGI_ORCHESTRATION_BACKOFF_MAX_MS', 300000), 86_400_000));
const toolResult = (value: object, isError = false) => ({ content: [{ type: 'text' as const, text: JSON.stringify(value) }], details: undefined, isError });

type PreparedRun = { params: ProductOpenParams; route?: object; authorize: () => void; attempt: () => ProviderAttempt; release?: () => void };
type Dependencies = {
  now: () => number;
  freeSlot: () => boolean;
  slotReleased: () => void;
  prepare: (mandate: OrchestrationMandate, tools: ToolDefinition[], bytes: number) => Promise<PreparedRun>;
  open: (params: ProductOpenParams, tools: ToolDefinition[]) => Promise<ProductSession>;
  ownerAvailable: (mandate: OrchestrationMandate) => boolean;
  activeForeground: (conversationId: string) => string | undefined;
  redact: (text: string, limit: number) => string;
  emit: (change: OrchestrationChanged) => void;
  timelineChanged: (change: TimelineChanged) => void;
  disarm: (mandate: OrchestrationMandate, watch?: RuntimeWatch) => void;
  fatal: () => void;
};
type RetainedSession = { session: ProductSession; generation: number; identity: string; turns: number; touched: number };
type Running = { mandateId: string; canceled: boolean; done: Promise<void>; stop?: () => void };
type Wake = { actor: OrchestrationActor; batch: EventBatch; events: InboxEvent[]; answer?: Static<typeof finish> };

export class OrchestrationRuntime {
  private readonly active = new Map<string, Running>();
  private readonly sessions = new Map<string, RetainedSession>();
  private readonly currentWake = new Map<string, Wake>();
  private timer?: ReturnType<typeof setInterval>;
  private scheduled = false;
  private closing = false;
  private fenced = 0;

  constructor(private readonly storage: ProductSQLite, private readonly deps: Dependencies) {}

  get size(): number { return this.active.size; }

  start(): void {
    if (this.timer || this.closing || process.env.NAGI_ORCHESTRATION_HOLD === '1') return;
    this.timer = setInterval(() => this.wake(), 250).unref();
    this.wake();
  }

  wake(): void {
    if (this.closing || this.fenced || !this.timer || this.scheduled) return;
    this.scheduled = true;
    queueMicrotask(() => {
      this.scheduled = false;
      if (this.closing || this.fenced || !this.deps.freeSlot()) return;
      let at: string;
      try { at = date(this.deps.now()); }
      catch { return; }
      for (const mandate of this.storage.readyMandates(at)) {
        if (!this.deps.freeSlot()) break;
        if (this.active.has(mandate.mandateId) || !this.deps.ownerAvailable(mandate)) continue;
        const active: Running = { mandateId: mandate.mandateId, canceled: false, done: Promise.resolve() };
        this.active.set(mandate.mandateId, active);
        active.done = this.run(mandate, active).catch(() => this.deps.fatal()).finally(() => {
          this.active.delete(mandate.mandateId);
          this.deps.slotReleased();
          this.wake();
        });
      }
    });
  }

  sourceChanged(mandateId: string): void {
    const mandate = this.storage.mandate(mandateId);
    if (mandate) this.changed('mandate_updated', mandate);
    this.wake();
  }

  async withBackupFence<T>(action: () => Promise<T>): Promise<T> {
    this.fenced++;
    try { return await action(); }
    finally { this.fenced--; this.wake(); }
  }

  async drain(): Promise<void> {
    this.closing = true;
    clearInterval(this.timer);
    for (const row of this.active.values()) this.cancelRun(row);
    await Promise.all([...this.active.values()].map(row => row.done));
    await Promise.all([...this.sessions.keys()].map(key => this.closeSession(key)));
  }

  private async closeSession(mandateId: string): Promise<void> {
    const entry = this.sessions.get(mandateId);
    this.sessions.delete(mandateId);
    if (entry) await entry.session.close();
  }

  private cancelRun(run: Running): void {
    run.canceled = true;
    run.stop?.();
  }

  private changed(change: OrchestrationChanged['change'], row: OrchestrationMandate, extra: Partial<OrchestrationChanged> = {}): void {
    this.deps.emit({ botId: row.ownerBotId, mandateId: row.mandateId, mandateRevision: row.revision, state: row.state,
      conversationId: row.targetConversationId, runId: row.currentRunId, change, at: new Date().toISOString(), ...extra });
  }

  requireMandate(botId: string, mandateId: string): OrchestrationMandate {
    const row = this.storage.mandate(mandateId);
    if (row?.ownerBotId !== botId) throw new RequestError('not_found', 'The mandate does not belong to this Bot');
    return row;
  }

  private receipt(method: string, params: { operationId: string }, target: Operation['target'], result: Record<string, unknown>): Operation {
    const receipt = { operationId: params.operationId, target, state: 'accepted' as const, result };
    this.storage.saveOrchestrationOperation({ method, fingerprint: hash(params), receipt, reply: receipt });
    return receipt;
  }

  decide(params: ProductParams<'orchestration.decide'>): Operation {
    const { botId, mandateId, decisionId, selectedOptionId, outcome } = params;
    const { receipt, mandate } = this.storage.orchestrationTransaction(() => {
      const prior = this.requireMandate(botId, mandateId);
      const card = this.storage.orchestrationDecision(decisionId);
      if (card?.mandateId !== mandateId || card.ownerBotId !== botId) throw new RequestError('not_found', 'The decision does not belong to this mandate');
      if (prior.dormant || card.dormant || prior.state !== 'waiting_decision' || card.state !== 'open') throw new RequestError('invalid_state', 'The decision is no longer open');
      if (card.revision !== params.expectedRevision) throw new RequestError('stale_revision', 'The decision revision changed');
      if (selectedOptionId && !card.options.some(option => option.id === selectedOptionId)) throw new RequestError('invalid_params', 'The decision option does not exist');
      const at = date(this.deps.now());
      const decision: OrchestrationDecision = { ...card, state: outcome === 'approve' ? 'approved' : 'rejected', outcome, selectedOptionId,
        responseText: params.text === undefined ? undefined : this.deps.redact(params.text, 2048), revision: card.revision + 1, updatedAt: at, resolvedAt: at };
      const mandate: OrchestrationMandate = { ...prior, state: outcome === 'approve' ? 'active' : 'paused', revision: prior.revision + 1, updatedAt: at };
      this.storage.putDecision(decision);
      this.storage.putMandate(mandate);
      this.storage.clearBackoff(mandateId);
      if (outcome === 'approve') this.storage.insertInbox({ ownerBotId: botId, targetConversationId: prior.targetConversationId,
        mandateId, sourceKind: 'decision_resolved', sourceId: decisionId, occurrenceId: decisionId, at, payload: { decisionId, outcome, selectedOptionId,
          responseText: decision.responseText } });
      else this.storage.cancelMandateEvents(mandateId);
      return { mandate, receipt: this.receipt('orchestration.decide', params, { botId, mandateId, decisionId },
        { mandate: this.storage.orchestrationSummary(mandate), decision }) };
    });
    this.changed('decision_resolved', mandate, { decisionId, operationId: params.operationId });
    this.wake();
    return receipt;
  }

  stop(params: ProductParams<'orchestration.stop'>): Operation {
    const { botId, mandateId, generation, expectedRevision, runId } = params;
    const result = this.storage.orchestrationTransaction(() => {
      const prior = this.requireMandate(botId, mandateId);
      if (prior.dormant) throw new RequestError('invalid_state', 'A restored mandate is read-only');
      if (expectedRevision !== undefined && expectedRevision !== prior.revision || generation !== undefined && generation !== prior.generation ||
          runId !== undefined && runId !== prior.currentRunId) throw new RequestError('stale_revision', 'The mandate changed');
      const at = date(this.deps.now());
      const mandate: OrchestrationMandate = { ...prior, state: 'stopped', lastDisposition: 'stopped', statusReason: 'stopped_by_user',
        revision: prior.revision + 1, updatedAt: at, settledAt: at };
      this.storage.putMandate(mandate);
      this.storage.cancelMandateEvents(mandateId);
      const card = this.storage.pendingDecision(mandateId);
      if (card) this.storage.putDecision({ ...card, state: 'cancelled', revision: card.revision + 1, updatedAt: at, resolvedAt: at, statusReason: 'mandate_stopped' });
      const watch = this.storage.disarmMandateWatch(mandate, at);
      const active = this.active.get(mandateId);
      const receipt = this.receipt('orchestration.stop', params, { botId, mandateId }, { mandate: this.storage.orchestrationSummary(mandate),
        cancelRequested: !!active, stoppedRunId: prior.currentRunId });
      return { active, watch, receipt, mandate };
    });
    if (result.active) this.cancelRun(result.active);
    else void this.closeSession(mandateId).catch(() => this.deps.fatal());
    this.deps.disarm(result.mandate, result.watch);
    this.changed('stopped', result.mandate, { operationId: params.operationId });
    return result.receipt;
  }

  private context(wake: Wake) {
    const { actor } = wake;
    const mandate = this.requireMandate(actor.botId, actor.mandateId);
    const run = this.storage.orchestrationRun(actor.runId);
    if (mandate.currentRunId !== actor.runId || mandate.generation !== actor.generation || !this.deps.ownerAvailable(mandate) ||
        !run || run.state !== 'running' || !['active', 'waiting_decision'].includes(mandate.state)) {
      throw new RequestError('invalid_state', 'The orchestration run is no longer active');
    }
    return { mandate, run };
  }

  private appendNotice(mandate: OrchestrationMandate, runId: string, data: Static<typeof notify>): { noticeId: string; change?: TimelineChanged } {
    const sourceRefs = (data.sourceRefs ?? []).map(ref => ({ ...ref, label: ref.label && this.deps.redact(ref.label, 160) }));
    const text = this.deps.redact(data.text, 2048).trim();
    if (!text) throw new RequestError('invalid_params', 'A notice requires text');
    const noticeId = randomUUID();
    const anchorExecutionId = this.deps.activeForeground(mandate.targetConversationId);
    const change = this.storage.appendNotice({ noticeId, conversationId: mandate.targetConversationId, botId: mandate.ownerBotId,
      mandateId: mandate.mandateId, runId, factDigest: hash([text, sourceRefs.map(({ kind, id }) => [kind, id])]),
      anchorKind: anchorExecutionId ? 'post_settlement' : 'standalone', anchorExecutionId, text, sourceRefs, urgency: data.urgency ?? 'normal',
      dormant: false, createdAt: new Date().toISOString() });
    return { noticeId, change };
  }

  private notify(wake: Wake, data: Static<typeof notify>) {
    const result = this.storage.orchestrationTransaction(() => {
      const { mandate, run } = this.context(wake);
      if (run.noticeId) return { noticeId: run.noticeId, change: undefined };
      if (run.decisionId) throw new RequestError('invalid_state', 'The run already requested a decision');
      const validRef = (ref: StoredConversationNotice['sourceRefs'][number]) =>
        mandate.sourceId === ref.id && ref.kind === (mandate.sourceKind === 'session' ? 'session' : 'watch') ||
        wake.events.some(event => event.sourceId === ref.id && ((ref.kind === 'watch' && (event.sourceKind.startsWith('schedule_') || event.sourceKind === 'timer_fired' || event.sourceKind === 'session_watch_fired')) ||
          ref.kind === 'activity' && event.sourceKind === 'activity_milestone' || ref.kind === 'decision' && event.sourceKind === 'decision_resolved'));
      if (data.sourceRefs?.some(ref => !validRef(ref))) throw new RequestError('scope_denied', 'A notice source is outside this wake');
      const notice = this.appendNotice(mandate, run.runId, data);
      this.storage.putRun({ ...run, disposition: 'notified', noticeId: notice.noticeId });
      return notice;
    });
    if (result.change) this.deps.timelineChanged(result.change);
    return { noticeId: result.noticeId };
  }

  private openDecision(wake: Wake, data: Static<typeof requestDecision>) {
    const result = this.storage.orchestrationTransaction(() => {
      const { mandate: row, run } = this.context(wake);
      if (run.decisionId) return { decisionId: run.decisionId, mandate: undefined };
      if (run.noticeId) throw new RequestError('invalid_state', 'The run already published a notice');
      const options = (data.options ?? []).map(option => ({ ...option, label: this.deps.redact(option.label, 160),
        description: option.description && this.deps.redact(option.description, 512) }));
      if (new Set(options.map(option => option.id)).size !== options.length || options.some(option => this.deps.redact(option.id, 512) !== option.id)) {
        throw new RequestError('invalid_params', 'Decision options require unique nonsecret identifiers');
      }
      const decisionId = randomUUID();
      const at = date(this.deps.now());
      this.storage.putDecision({ decisionId, runId: run.runId, mandateId: row.mandateId, ownerBotId: row.ownerBotId,
        kind: this.deps.redact(data.kind ?? 'approval', 64), purpose: this.deps.redact(data.purpose ?? row.purpose, 256), summary: this.deps.redact(data.summary, 2048),
        targetConversationId: row.targetConversationId, options, revision: 1, state: 'open', dormant: false, createdAt: at, updatedAt: at });
      const mandate = { ...row, state: 'waiting_decision' as const, revision: row.revision + 1, updatedAt: at };
      this.storage.putMandate(mandate);
      this.storage.putRun({ ...run, decisionId, disposition: 'needs_decision' });
      return { decisionId, mandate };
    });
    if (result.mandate) this.changed('decision_opened', result.mandate, { decisionId: result.decisionId });
    return { decisionId: result.decisionId };
  }

  private tools(active: () => Wake | undefined): ToolDefinition[] {
    const tool = <T extends TSchema>(name: string, description: string, parameters: T, run: (wake: Wake, input: Static<T>) => object): ToolDefinition => ({
      name, label: name, description, parameters,
      execute: async (_callId, input) => {
        try {
          const wake = active();
          if (!wake || !Value.Check(parameters, input)) throw new RequestError('invalid_params', 'Invalid orchestration tool input');
          return toolResult(run(wake, input));
        } catch (error) {
          if (!(error instanceof RequestError)) throw error;
          return toolResult({ code: error.code, message: error.message }, true);
        }
      },
    });
    return [
      tool('conversation_notify', 'Publish one factual notice to the conversation fixed by this mandate.', notify, (wake, data) => this.notify(wake, data)),
      tool('orchestration_request_decision', 'Ask the human for a decision and park the current mandate.', requestDecision, (wake, data) => this.openDecision(wake, data)),
      tool('orchestration_finish', 'Record a short checkpoint and report whether the observed situation changed.', finish, (wake, data) => {
        this.context(wake);
        wake.answer ??= { ...data, summary: this.deps.redact(data.summary, 2048),
          observedStateHash: hash(data.observedStateHash), actionClass: data.actionClass && this.deps.redact(data.actionClass, 128) };
        return { recorded: true };
      }),
    ];
  }

  private prompt(mandate: OrchestrationMandate, events: InboxEvent[], workingSummary?: string): string {
    const facts = events.map(row => ({ kind: row.sourceKind, sourceId: row.sourceId, occurrenceId: row.occurrenceId,
      coalescedCount: row.coalescedCount, observedAt: row.updatedAt, data: this.deps.redact(JSON.stringify(row.payload), 1200) }));
    const value = JSON.stringify({ provenance: 'internal', mandateId: mandate.mandateId, purpose: mandate.purpose,
      workingSummary, facts }).replaceAll('<', '\\u003c');
    return `<orchestration_event>\n${value}\n</orchestration_event>`;
  }

  private fitWakePrompt(mandate: OrchestrationMandate, events: InboxEvent[], workingSummary: string | undefined,
    fixedBytes: number, headroom: number): { events: InboxEvent[]; input: string; workingSummary?: string } {
    let fitted = events;
    let summary = workingSummary;
    let input = this.prompt(mandate, fitted, summary);
    while (fitted.length > 1 && Math.ceil((fixedBytes + Buffer.byteLength(input)) / 4) > headroom) {
      fitted = fitted.slice(0, -1);
      input = this.prompt(mandate, fitted, summary);
    }
    if (Math.ceil((fixedBytes + Buffer.byteLength(input)) / 4) > headroom && summary) {
      summary = undefined;
      input = this.prompt(mandate, fitted);
    }
    if (Math.ceil((fixedBytes + Buffer.byteLength(input)) / 4) > headroom) {
      throw new RequestError('context_limit', 'The orchestration request exceeds the route context budget');
    }
    return { events: fitted, input, workingSummary: summary };
  }

  private async acquireSession(mandate: OrchestrationMandate, identity: string, fixedBytes: number, inputBytes: number, headroom: number,
    checkpoint: { revision: number } | undefined, workingSummary: string | undefined, params: ProductOpenParams,
    tools: ToolDefinition[]): Promise<{ mandate: OrchestrationMandate; session: RetainedSession }> {
    const key = mandate.mandateId;
    let row = mandate;
    let session = this.sessions.get(key);
    const projected = fixedBytes + inputBytes + (session?.session.historyBytes() ?? 0);
    if (session && (session.generation !== row.generation || session.identity !== identity || session.turns >= 4 || projected / 4 > headroom * 0.75)) {
      const at = date(this.deps.now());
      row = this.storage.orchestrationTransaction(() => {
        const latest = this.requireMandate(row.ownerBotId, key);
        if (latest.revision !== row.revision || latest.generation !== row.generation) throw new RequestError('stale_revision', 'The mandate changed');
        const next = { ...latest, generation: latest.generation + 1, revision: latest.revision + 1, updatedAt: at };
        this.storage.putCheckpoint({ mandateId: key, generation: next.generation, revision: (checkpoint?.revision ?? 0) + 1,
          workingSummary: workingSummary ?? '', updatedAt: at });
        this.storage.putMandate(next);
        return next;
      });
      this.changed('mandate_updated', row);
      await this.closeSession(key);
      session = undefined;
    }
    if (!session) {
      const oldest = [...this.sessions.entries()].filter(([id]) => !this.active.has(id)).sort((a, b) => a[1].touched - b[1].touched)[0];
      if (oldest && this.sessions.size >= 32) await this.closeSession(oldest[0]);
      session = { session: await this.deps.open(params, tools), generation: row.generation, identity, turns: 0, touched: this.deps.now() };
      this.sessions.set(key, session);
    }
    return { mandate: row, session };
  }

  private async run(initial: OrchestrationMandate, active: Running): Promise<void> {
    const key = initial.mandateId;
    let wake: Wake | undefined;
    let release: (() => void) | undefined;
    let session: RetainedSession | undefined;
    let settled = false;
    let lastError: string | undefined;
    try {
      let row = initial;
      let events = this.storage.wakeEvents(row);
      if (!events.length) return;
      const checkpoint = this.storage.checkpoint(key);
      const initialInput = this.prompt(row, events, checkpoint?.workingSummary);
      const tools = this.tools(() => this.currentWake.get(key));
      const toolBytes = Buffer.byteLength(JSON.stringify(tools.map(({ name, description, parameters }) => ({ name, description, parameters }))));
      const prepared = await this.deps.prepare(row, tools, toolBytes + Buffer.byteLength(initialInput));
      release = prepared.release;
      if (active.canceled || this.closing || !this.deps.ownerAvailable(row) || this.storage.mandate(key)?.revision !== row.revision) return;
      const config = prepared.params.configuration!;
      const contextTokens = Math.min(config.contextTokens, maxContextTokens);
      const headroom = contextTokens - config.outputTokens;
      if (headroom <= 0) throw new RequestError('context_limit', 'The orchestration route has no input budget');
      const fixedBytes = Buffer.byteLength(prepared.params.instructions) + toolBytes + 128;
      const fitted = this.fitWakePrompt(row, events, checkpoint?.workingSummary, fixedBytes, headroom);
      events = fitted.events;
      const input = fitted.input;
      const identity = hash([prepared.params.cwd, prepared.params.instructions, config]);
      const acquired = await this.acquireSession(row, identity, fixedBytes, Buffer.byteLength(input), headroom, checkpoint, fitted.workingSummary, prepared.params, tools);
      row = acquired.mandate;
      session = acquired.session;
      if (active.canceled || this.closing || !this.deps.ownerAvailable(row) || this.storage.mandate(key)?.revision !== row.revision) return;
      const now = this.deps.now();
      const at = date(now);
      const runId = randomUUID();
      const { batch, mandate } = this.storage.orchestrationTransaction(() => {
        const batch = this.storage.stageWake(row, runId, events, at);
        const mandate = { ...row, currentRunId: runId, revision: row.revision + 1, updatedAt: at };
        this.storage.putRun({ runId, mandateId: key, ownerBotId: row.ownerBotId, generation: row.generation, batchId: batch.batchId,
          state: 'starting', routeRole: 'utility', route: prepared.route, queueDelayMs: Math.max(0, now - Date.parse(events[0].createdAt)),
          coalescedCount: events.reduce((count, event) => count + event.coalescedCount, 0), startedAt: at, ...emptyOrchestrationUsage() });
        this.storage.putMandate(mandate);
        return { batch, mandate };
      });
      const current: Wake = { actor: { botId: row.ownerBotId, mandateId: key, generation: row.generation, runId, batchId: batch.batchId }, events, batch };
      wake = current;
      this.currentWake.set(key, current);
      const context = session.session;
      const authorize = () => {
        const latest = this.requireMandate(row.ownerBotId, key);
        if (this.closing || active.canceled || latest.currentRunId !== runId || !['active', 'waiting_decision'].includes(latest.state) ||
            !this.deps.ownerAvailable(latest)) throw new RequestError('cancelled', 'The orchestration run is no longer eligible');
        prepared.authorize();
      };
      const { promise, resolve } = Promise.withResolvers<{ state: 'completed' | 'failed' | 'interrupted'; text: string; error?: string; uncertain?: boolean }>();
      active.stop = () => context.stop(runId);
      context.start(runId, { revision: 0, text: input, materials: [] }, {
        authorizeProvider: authorize, providerAttempt: prepared.attempt,
        checkRequestBytes: bytes => {
          if (Math.ceil(bytes / 4) > headroom) throw new RequestError('context_limit', 'The projected orchestration request exceeds its input budget');
          if (!current.batch.dispatched) {
            current.batch = this.storage.dispatchWake(current.actor, current.batch, date(this.deps.now()));
            this.changed('run_started', mandate, { runId });
          }
        },
        onModelSettlement: usage => this.storage.recordOrchestrationUsage(current.actor, usage),
        admitTool: (name, callId) => this.storage.admitOrchestrationTool(current.actor, this.deps.redact(callId, 256), name,
          row.targetConversationId, true, date(this.deps.now())),
        toolFinished: (_name, callId, isError) => this.storage.settleOrchestrationTool(runId, this.deps.redact(callId, 256), isError),
        started: () => {}, text: () => {}, confirmedFileEffect: () => {}, toolStarted: () => {},
        settled: (state, text, error, uncertain) => { active.stop = undefined; resolve({ state, text, error, uncertain }); },
      });
      const outcome = await promise;
      session.turns++;
      session.touched = this.deps.now();
      this.settle(current, outcome.state, outcome.text, outcome.error, outcome.uncertain);
      settled = true;
      if (outcome.state !== 'completed') await this.closeSession(key);
    } catch (error) {
      lastError = error instanceof RequestError ? error.code : 'orchestration_failed';
      if (wake && !settled) this.settle(wake, active.canceled ? 'interrupted' : 'failed', '', lastError);
      else if (!active.canceled && !this.closing && lastError !== 'stale_revision') this.delayUnavailable(initial, lastError);
      await this.closeSession(key);
    } finally {
      active.stop = undefined;
      release?.();
      this.currentWake.delete(key);
      if (active.canceled || this.closing) await this.closeSession(key);
    }
  }

  private delayUnavailable(mandate: OrchestrationMandate, error: string): void {
    const at = date(this.deps.now());
    const current = this.requireMandate(mandate.ownerBotId, mandate.mandateId);
    if (current.state !== 'active' || current.currentRunId) return;
    const row = this.storage.orchestrationTransaction(() => {
      const backoff = this.backoff(current, error, 'route', false, at, true);
      const next = { ...current, lastError: error, lastReceiptState: 'non_dispatched', revision: current.revision + 1, updatedAt: at };
      this.storage.putBackoff(backoff);
      this.storage.putMandate(next);
      return next;
    });
    this.changed('backoff_updated', row);
  }

  private backoff(row: OrchestrationMandate, actionClass: string, observedStateHash: string, progress: boolean, at: string, forceDelay = false): OrchestrationBackoff {
    const prior = this.storage.orchestrationBackoff(row.mandateId);
    const signature = hash([row.sourceId, actionClass, observedStateHash]);
    const repeated = !progress && !!prior && prior.consecutiveNoProgress > 0 && (prior.signature === signature || prior.previousSignature === signature);
    const consecutiveNoProgress = progress ? 0 : repeated ? prior.consecutiveNoProgress + 1 : 1;
    const backoffDelayMs = repeated || forceDelay ? Math.min(backoffMax, backoffBase * 2 ** Math.min(20, Math.max(0, consecutiveNoProgress - 2))) : 0;
    return { mandateId: row.mandateId, sourceId: row.sourceId, actionClass, observedStateHash, consecutiveNoProgress, signature,
      previousSignature: !progress && prior?.consecutiveNoProgress ? prior.signature : undefined, backoffDelayMs,
      backoffReason: backoffDelayMs ? (forceDelay ? actionClass : 'no_progress') : undefined,
      nextEligibleAt: backoffDelayMs ? date(Date.parse(at) + backoffDelayMs) : undefined,
      lastNoticeEpisodeHash: repeated ? prior.lastNoticeEpisodeHash : undefined, updatedAt: at };
  }

  private settle(wake: Wake, state: 'completed' | 'failed' | 'interrupted', text: string, error?: string, uncertain = false): void {
    const at = date(this.deps.now());
    const result = this.storage.orchestrationTransaction(() => {
      const run = this.storage.orchestrationRun(wake.actor.runId)!;
      const prior = this.requireMandate(wake.actor.botId, wake.actor.mandateId);
      if (prior.currentRunId !== run.runId) throw new RequestError('stale_revision', 'The orchestration run changed');
      const summary = this.deps.redact(wake.answer?.summary ?? text, 2048);
      const effectUncertain = this.storage.finishOrchestrationAdmissions(run.runId);
      const runState = (uncertain || effectUncertain) && wake.batch.dispatched ? 'unknown' : state;
      const stopped = prior.state === 'stopped';
      const disposition = stopped ? 'stopped' : run.disposition ?? (runState === 'completed' ? 'silent' : runState === 'unknown' ? 'unknown' : 'failed');
      const settled = { ...run, state: runState, disposition, summary, error: error && this.deps.redact(error, 2048), settledAt: at };
      const backoff = this.backoff(prior, wake.answer?.actionClass ?? (runState === 'completed' ? disposition : 'turn_failed'),
        wake.answer?.observedStateHash ?? hash(wake.events.map(event => [event.sourceKind, (event.payload as { digest?: string }).digest])),
        wake.answer?.progressMade === true, at, !wake.batch.dispatched && runState === 'failed');
      let change: TimelineChanged | undefined;
      if (prior.state === 'active' && backoff.backoffDelayMs && backoff.consecutiveNoProgress >= 2 && !backoff.lastNoticeEpisodeHash && !settled.noticeId && !settled.decisionId) {
        const notice = this.appendNotice(prior, run.runId, { text: 'Repeated orchestration wakes made no progress. This mandate will wait before trying again.', urgency: 'low' });
        settled.noticeId = notice.noticeId;
        settled.disposition = 'notified';
        backoff.lastNoticeEpisodeHash = backoff.signature;
        change = notice.change;
      }
      const checkpoint = this.storage.checkpoint(prior.mandateId);
      if (runState === 'completed' && !stopped) this.storage.putCheckpoint({ mandateId: prior.mandateId, generation: prior.generation,
        revision: (checkpoint?.revision ?? 0) + 1,
        workingSummary: wake.answer?.summary || checkpoint?.workingSummary || `${disposition}: ${wake.events.map(event => event.sourceKind).join(', ')}`,
        updatedAt: at });
      if (prior.state === 'active') this.storage.putBackoff(backoff);
      this.storage.putRun(settled);
      this.storage.finishWake(wake.batch, runState === 'unknown');
      if (stopped || prior.state === 'paused') this.storage.cancelMandateEvents(prior.mandateId);
      const mandate = { ...prior, state: prior.state === 'active' && runState === 'unknown' ? 'unknown' as const : prior.state,
        currentRunId: undefined, lastDisposition: settled.disposition, lastNoticeId: settled.noticeId ?? prior.lastNoticeId,
        lastReceiptState: !wake.batch.dispatched ? 'non_dispatched' : runState === 'unknown' ? 'unknown' : 'settled', lastError: settled.error,
        revision: prior.revision + 1, updatedAt: at, settledAt: at };
      this.storage.putMandate(mandate);
      return { change, mandate, backoff };
    });
    if (result.change) this.deps.timelineChanged(result.change);
    this.changed('run_settled', result.mandate, { runId: wake.actor.runId });
    if (result.backoff.backoffDelayMs) this.changed('backoff_updated', result.mandate);
  }
}
