import { createHash, randomUUID } from 'node:crypto';
import { chmodSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { basename, dirname, join } from 'node:path';
import { DatabaseSync, type SQLOutputValue } from 'node:sqlite';
import { decodeProductProfile, importProductSnapshot, privateCredentialKeys, readPrivateConnection, type PrivateConnection, type ProductBackup, type ProductProfilePaths } from './product-profile.js';
import type { ProductProfile } from './product.js';
import { canonicalJSON, maxAutomaticAnswers, maxTriageAttempts, RequestError, type ActivityProgressRecord, type ActivitySteer,
  type ActivitySteerPayload, type ActivitySteerRecord, type OrchestrationCheckpoint, type OrchestrationDecision,
  type OrchestrationEffectReceipt, type OrchestrationMandate, type OrchestrationRun, type OrchestrationSummary,
  type SessionSummary, type SessionWatchOccurrence, type SkillApproval, type SkillApprovalRequest, type SkillSource,
  type StoredConversationNotice, type TimelineChanged, type TraceItem, type TriageAccounting, type WatchGrant,
  type WatchOccurrence, type WatchSummary } from './protocol.js';
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
`, 'SELECT 1;', `
  CREATE TABLE runtime_sessions (
    session_id TEXT PRIMARY KEY, owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED,
    origin_conversation_id TEXT REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    origin_activity_id TEXT REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED,
    generation INTEGER NOT NULL, revision INTEGER NOT NULL, state TEXT NOT NULL, updated_at TEXT NOT NULL, record_json TEXT NOT NULL
  ) STRICT;
  CREATE INDEX runtime_sessions_by_owner ON runtime_sessions(owner_bot_id, updated_at DESC, session_id);
  CREATE TABLE session_watches (
    watch_id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES runtime_sessions(session_id) DEFERRABLE INITIALLY DEFERRED,
    owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED,
    generation INTEGER NOT NULL, revision INTEGER NOT NULL, state TEXT NOT NULL, record_json TEXT NOT NULL
  ) STRICT;
  CREATE INDEX session_watches_by_session ON session_watches(session_id);
  CREATE TABLE runtime_watch_occurrences (
    occurrence_id TEXT PRIMARY KEY, watch_id TEXT NOT NULL UNIQUE REFERENCES session_watches(watch_id) DEFERRABLE INITIALLY DEFERRED,
    session_id TEXT NOT NULL REFERENCES runtime_sessions(session_id) DEFERRABLE INITIALLY DEFERRED,
    owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED,
    generation INTEGER NOT NULL, fired_kind TEXT NOT NULL, fired_at TEXT NOT NULL, record_json TEXT NOT NULL
  ) STRICT;
`, `
  CREATE TABLE watch_grants (
    grant_id TEXT PRIMARY KEY, installation_id TEXT NOT NULL, owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED,
    revision INTEGER NOT NULL, state TEXT NOT NULL, updated_at TEXT NOT NULL, record_json TEXT NOT NULL
  ) STRICT;
  CREATE INDEX watch_grants_by_owner ON watch_grants(installation_id, owner_bot_id, updated_at DESC, grant_id);
  CREATE TABLE runtime_watches (
    watch_id TEXT PRIMARY KEY, owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED,
    target_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    origin_activity_id TEXT REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED,
    kind TEXT NOT NULL CHECK (kind IN ('timer','schedule')), revision INTEGER NOT NULL, state TEXT NOT NULL,
    next_due_at TEXT, updated_at TEXT NOT NULL, record_json TEXT NOT NULL
  ) STRICT;
  CREATE INDEX runtime_watches_by_owner ON runtime_watches(owner_bot_id, updated_at DESC, watch_id);
  CREATE INDEX runtime_watches_by_due ON runtime_watches(next_due_at, watch_id) WHERE state = 'active';
  CREATE TABLE watch_occurrences (
    occurrence_id TEXT PRIMARY KEY, watch_id TEXT NOT NULL REFERENCES runtime_watches(watch_id) DEFERRABLE INITIALLY DEFERRED,
    owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, watch_revision INTEGER NOT NULL,
    scheduled_at TEXT NOT NULL, state TEXT NOT NULL, created_at TEXT NOT NULL, record_json TEXT NOT NULL,
    UNIQUE (watch_id, watch_revision, scheduled_at)
  ) STRICT;
  CREATE INDEX watch_occurrences_recent ON watch_occurrences(watch_id, scheduled_at DESC, occurrence_id);
  CREATE TABLE bot_event_batches (
    batch_id TEXT PRIMARY KEY, owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED,
    lease_id TEXT NOT NULL, stage TEXT NOT NULL CHECK (stage IN ('staged','dispatched','completed','released','unknown')),
    dispatched INTEGER NOT NULL CHECK (dispatched IN (0, 1)), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, record_json TEXT NOT NULL,
    UNIQUE (owner_bot_id, lease_id)
  ) STRICT;
  CREATE TABLE bot_event_inbox (
    event_id TEXT PRIMARY KEY, owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED,
    target_conversation_id TEXT REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    source_kind TEXT NOT NULL, source_id TEXT NOT NULL, occurrence_id TEXT NOT NULL UNIQUE, coalesce_key TEXT,
    coalesced_count INTEGER NOT NULL DEFAULT 1, state TEXT NOT NULL CHECK (state IN ('pending','claimed','consumed','unknown','cancelled')),
    batch_id TEXT REFERENCES bot_event_batches(batch_id) DEFERRABLE INITIALLY DEFERRED,
    created_at TEXT NOT NULL, updated_at TEXT NOT NULL, payload_json TEXT NOT NULL
  ) STRICT;
  CREATE INDEX bot_event_inbox_by_owner ON bot_event_inbox(owner_bot_id, state, created_at, event_id);
  CREATE INDEX bot_event_inbox_by_batch ON bot_event_inbox(batch_id) WHERE batch_id IS NOT NULL;
  CREATE INDEX bot_event_inbox_by_source ON bot_event_inbox(owner_bot_id, source_id, target_conversation_id, source_kind);
`, `
  CREATE TABLE orchestration_mandates (
    mandate_id TEXT PRIMARY KEY, owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED,
    target_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    source_kind TEXT NOT NULL, source_id TEXT, purpose TEXT NOT NULL, state TEXT NOT NULL, revision INTEGER NOT NULL,
    generation INTEGER NOT NULL, current_run_id TEXT, last_disposition TEXT, last_notice_id TEXT, last_receipt_state TEXT,
    last_error TEXT, status_reason TEXT, dormant INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
    settled_at TEXT, record_json TEXT NOT NULL
  ) STRICT;
  CREATE INDEX orchestration_mandates_by_owner ON orchestration_mandates(owner_bot_id, updated_at DESC, mandate_id);
  CREATE TABLE orchestration_checkpoints (
    mandate_id TEXT PRIMARY KEY REFERENCES orchestration_mandates(mandate_id) DEFERRABLE INITIALLY DEFERRED,
    generation INTEGER NOT NULL, revision INTEGER NOT NULL, working_summary TEXT NOT NULL, checkpoint_json TEXT NOT NULL, updated_at TEXT NOT NULL
  ) STRICT;
  CREATE TABLE orchestration_runs (
    run_id TEXT PRIMARY KEY, mandate_id TEXT NOT NULL REFERENCES orchestration_mandates(mandate_id) DEFERRABLE INITIALLY DEFERRED,
    owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, generation INTEGER NOT NULL,
    batch_id TEXT REFERENCES bot_event_batches(batch_id) DEFERRABLE INITIALLY DEFERRED, state TEXT NOT NULL, route_role TEXT,
    route_json TEXT, disposition TEXT, summary TEXT, notice_id TEXT, decision_id TEXT, error TEXT, queue_delay_ms INTEGER NOT NULL,
    coalesced_count INTEGER NOT NULL, calls INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
    total_tokens INTEGER NOT NULL, cost_usd REAL NOT NULL, dispatched_at TEXT, started_at TEXT NOT NULL, settled_at TEXT, record_json TEXT NOT NULL
  ) STRICT;
  CREATE INDEX orchestration_runs_by_mandate ON orchestration_runs(mandate_id, started_at DESC, run_id);
  CREATE TABLE orchestration_admissions (
    receipt_id TEXT PRIMARY KEY, mandate_id TEXT NOT NULL REFERENCES orchestration_mandates(mandate_id) DEFERRABLE INITIALLY DEFERRED,
    run_id TEXT NOT NULL REFERENCES orchestration_runs(run_id) DEFERRABLE INITIALLY DEFERRED, call_id TEXT NOT NULL,
    tool TEXT NOT NULL, target TEXT, tool_class TEXT NOT NULL CHECK (tool_class IN ('readonly','mutating')),
    state TEXT NOT NULL CHECK (state IN ('admitted','non_dispatched','settled_ok','settled_error','unknown')),
    reason TEXT, created_at TEXT NOT NULL, UNIQUE(run_id, call_id)
  ) STRICT;
  CREATE INDEX orchestration_admissions_by_mandate ON orchestration_admissions(mandate_id, created_at DESC, receipt_id);
  CREATE TABLE orchestration_decisions (
    decision_id TEXT PRIMARY KEY, mandate_id TEXT NOT NULL REFERENCES orchestration_mandates(mandate_id) DEFERRABLE INITIALLY DEFERRED,
    owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, run_id TEXT NOT NULL, revision INTEGER NOT NULL,
    state TEXT NOT NULL, kind TEXT NOT NULL, purpose TEXT NOT NULL, summary TEXT NOT NULL,
    target_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    options_json TEXT NOT NULL, outcome TEXT, selected_option_id TEXT, response_text TEXT, dormant INTEGER NOT NULL,
    status_reason TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, resolved_at TEXT, record_json TEXT NOT NULL, UNIQUE(run_id)
  ) STRICT;
  CREATE UNIQUE INDEX orchestration_decisions_open ON orchestration_decisions(mandate_id) WHERE state = 'open';
  CREATE TABLE conversation_notices (
    notice_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED,
    mandate_id TEXT NOT NULL REFERENCES orchestration_mandates(mandate_id) DEFERRABLE INITIALLY DEFERRED, run_id TEXT NOT NULL,
    fact_digest TEXT NOT NULL, timeline_seq INTEGER NOT NULL, anchor_kind TEXT NOT NULL,
    anchor_execution_id TEXT REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, anchor_outcome TEXT,
    text TEXT NOT NULL, source_refs_json TEXT NOT NULL, urgency TEXT NOT NULL, dormant INTEGER NOT NULL,
    created_at TEXT NOT NULL, record_json TEXT NOT NULL, UNIQUE(run_id, fact_digest), UNIQUE(run_id), UNIQUE(conversation_id, timeline_seq)
  ) STRICT;
  CREATE INDEX conversation_notices_by_execution ON conversation_notices(anchor_execution_id) WHERE anchor_outcome IS NULL;
  CREATE TABLE conversation_timelines (
    conversation_id TEXT PRIMARY KEY REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    timeline_revision INTEGER NOT NULL, next_seq INTEGER NOT NULL, updated_at TEXT NOT NULL
  ) STRICT;
  CREATE TABLE orchestration_backoff (
    mandate_id TEXT PRIMARY KEY REFERENCES orchestration_mandates(mandate_id) DEFERRABLE INITIALLY DEFERRED,
    source_id TEXT, action_or_error_class TEXT NOT NULL, observed_state_hash TEXT NOT NULL,
    consecutive_no_progress INTEGER NOT NULL, backoff_delay_ms INTEGER NOT NULL, backoff_reason TEXT,
    last_notice_episode_hash TEXT, next_eligible_at TEXT, updated_at TEXT NOT NULL, record_json TEXT NOT NULL
  ) STRICT;
  CREATE TABLE queued_conversation_sends (
    operation_id TEXT PRIMARY KEY REFERENCES operations(operation_id) DEFERRABLE INITIALLY DEFERRED,
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    execution_id TEXT NOT NULL UNIQUE REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED,
    draft_revision INTEGER NOT NULL, state TEXT NOT NULL, reason TEXT NOT NULL, route_json TEXT,
    dispatched_at TEXT, created_at TEXT NOT NULL, settled_at TEXT
  ) STRICT;
  ALTER TABLE bot_event_inbox ADD COLUMN mandate_id TEXT REFERENCES orchestration_mandates(mandate_id) DEFERRABLE INITIALLY DEFERRED;
  CREATE INDEX bot_event_inbox_by_mandate ON bot_event_inbox(mandate_id, state);
  ALTER TABLE activities ADD COLUMN origin_mandate_id TEXT;
`];
const LATEST_SCHEMA_VERSION = MIGRATIONS.length;

type Snapshot = { data: Buffer; digest: string };
type Mirror = { snapshot: Snapshot; previous: string | null; temp: string };
export type SkillApprovalChange = { put: SkillApproval } | { source: SkillSource; skillId: string; botId?: string } | { credentialServerId: string } | { workspaceBotId: string };

const encode = (value: unknown): string | null => JSON.stringify(value) ?? null;
const decode = (value: SQLOutputValue): unknown => value === null ? undefined : JSON.parse(String(value));
const sha256 = (data: Buffer) => createHash('sha256').update(data).digest('hex');

export type RoutingWrite = { credentials?: PrivateConnection; escalation?: Escalation };
export type QueuedSend = { operationId: string; conversationId: string; executionId: string; draftRevision: number;
  state: 'pending' | 'accepted' | 'rejected' | 'settled'; reason: string; route?: object; dispatchedAt?: string; createdAt: string; settledAt?: string };
export type ActivityControlWrite = { approvalRequests?: SkillApprovalRequest[]; resetProgress?: string;
  queuedSends?: QueuedSend[]; timelineOutcomes?: { executionId: string; state: NonNullable<StoredConversationNotice['anchorOutcome']> }[];
  triage?: { activityId: string; kind: 'attempt' | 'answer'; at: string };
  admitSteer?: ActivitySteerRecord; steerUpdates?: ActivitySteer[] };
export type RuntimeSession = SessionSummary & { epoch?: string; childReaped?: boolean };
export type RuntimeSessionWrite = { session: RuntimeSession; occurrences?: SessionWatchOccurrence[] };
export type RuntimeWatch = WatchSummary & { scheduledAt?: string; canonicalCwd?: string; lastFailureDigest?: string };
export type RuntimeWatchGrant = WatchGrant & { scopeDigest: string };
export type InboxSource = 'timer_fired' | 'schedule_tick' | 'schedule_precheck' | 'session_watch_fired' | 'target_lost' |
  'activity_milestone' | 'decision_resolved';
type InboxInput = { ownerBotId: string; targetConversationId?: string; sourceKind: InboxSource; sourceId: string; mandateId?: string;
  occurrenceId: string; coalesceKey?: string; at: string; payload: object };
type WatchOperation = ProductProfile['operations'][number];
type InboxState = 'pending' | 'claimed' | 'consumed' | 'unknown' | 'cancelled';
export type InboxEvent = Omit<InboxInput, 'at'> & { eventId: string; coalescedCount: number; state: InboxState; batchId?: string; createdAt: string; updatedAt: string };
export type OrchestrationActor = { botId: string; mandateId: string; generation: number; runId: string; batchId: string };
export type OrchestrationBackoff = { mandateId: string; sourceId?: string; actionClass: string; observedStateHash: string;
  previousSignature?: string; signature: string; consecutiveNoProgress: number; backoffDelayMs: number; backoffReason?: string;
  lastNoticeEpisodeHash?: string; nextEligibleAt?: string; updatedAt: string };
type StoredRun = OrchestrationRun & { ownerBotId: string; route?: object; dispatchedAt?: string };
type OrchestrationOperation = ProductProfile['operations'][number];
export const emptyOrchestrationUsage = () => ({ calls: 0, inputTokens: 0, outputTokens: 0, totalTokens: 0, costUsd: 0 });
export type EventBatch = { batchId: string; ownerBotId: string; targetConversationId?: string; mandateId?: string; leaseId: string;
  stage: 'staged' | 'dispatched' | 'completed' | 'released' | 'unknown'; dispatched: boolean;
  eventIds: string[]; createdAt: string; updatedAt: string };
export type WatchChanges = { watches?: RuntimeWatch[]; grants?: RuntimeWatchGrant[]; operation?: WatchOperation };
export function publicWatch({ scheduledAt, canonicalCwd, lastFailureDigest, ...watch }: RuntimeWatch): WatchSummary { return watch; }
export function publicWatchGrant({ scopeDigest, ...grant }: RuntimeWatchGrant): WatchGrant { return grant; }

function snapshotFor(profile: ProductProfile): Snapshot {
  const data = Buffer.from(canonicalJSON(profile) + '\n');
  return { data, digest: sha256(data) };
}

export class ProductSQLite {
  private readonly databasePath: string;
  private readonly db: DatabaseSync;
  private digest?: string;
  readonly sessionEpoch = randomUUID();

  constructor(private readonly profilePath: string, private readonly credentialsPath: string, private readonly skills?: BotSkills, private readonly vault?: McpVault) {
    const directory = dirname(profilePath);
    mkdirSync(directory, { recursive: true, mode: 0o700 });
    this.databasePath = basename(profilePath) === 'contacts.json' ? join(directory, 'xuhu.db') : `${profilePath}.sqlite`;
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
        this.db.exec('DELETE FROM runtime_watch_occurrences; DELETE FROM session_watches; DELETE FROM runtime_sessions;');
        this.db.exec("DELETE FROM bot_event_inbox; DELETE FROM bot_event_batches; DELETE FROM watch_occurrences; DELETE FROM runtime_watches; DELETE FROM watch_grants; DELETE FROM meta WHERE key GLOB 'watch_operation:*';");
        this.clearOrchestration();
        this.replaceProfile(profile, mirror);
        this.importOrchestration(backup);
        for (const watch of backup?.watches ?? []) this.putRuntimeWatch(watch);
        for (const occurrence of backup?.watchOccurrences ?? []) this.insertWatchOccurrence(occurrence);
        for (const row of backup?.runtimeSessions ?? []) this.putSession({ session: row });
        for (const row of backup?.runtimeWatchOccurrences ?? []) this.putSessionOccurrence(row);
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
    routing?: RoutingWrite, control?: ActivityControlWrite, runtime?: RuntimeSessionWrite): TraceUpdate[] {
    let mirror: Mirror | undefined;
    let staged: string | undefined;
    let stagedVault: string | undefined;
    let stagedCredentials: string | undefined;
    let committed = false;
    try {
      const updates = this.transaction(() => {
        if (this.meta('snapshot_sha256') !== this.digest) throw new RequestError('profile_unavailable', 'The SQLite profile changed in another worker');
        if (runtime) this.assertSessionEpoch();
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
        for (const row of control?.approvalRequests ?? []) {
          const isNew = row.state === 'open' && !this.approvalRequest(row.requestId);
          this.putApprovalRequest(row);
          if (isNew) {
            const activity = profile.activities.find(activity => activity.activityId === row.activityId)!;
            this.putActivityEvent(activity, 'approval_required', [row.requestId], { requestId: row.requestId, executionId: row.executionId }, row.createdAt);
          }
        }
        if (control?.admitSteer) this.insertSteer(control.admitSteer);
        for (const row of control?.steerUpdates ?? []) this.transitionSteer(row);
        for (const row of control?.queuedSends ?? []) this.putQueuedSend(row);
        for (const outcome of control?.timelineOutcomes ?? []) this.finishAnchoredNotices(outcome.executionId, outcome.state);
        for (const bot of profile.bots) {
          const old = before.bots.find(row => row.botId === bot.botId);
          if (old && (old.profile.workspace !== bot.profile.workspace || !old.archived && bot.archived)) {
            this.invalidateBotWatches(bot.botId, old.profile.workspace !== bot.profile.workspace ? 'workspace_changed' : 'bot_archived');
          }
        }
        if (runtime) this.putSession(runtime);
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

  private clearOrchestration(): void {
    this.db.exec(`DELETE FROM orchestration_admissions; DELETE FROM conversation_notices; DELETE FROM conversation_timelines;
      DELETE FROM orchestration_decisions; DELETE FROM orchestration_runs; DELETE FROM orchestration_checkpoints;
      DELETE FROM orchestration_backoff; DELETE FROM orchestration_mandates; DELETE FROM queued_conversation_sends;
      DELETE FROM meta WHERE key GLOB 'orchestration_operation:*';`);
  }

  private importOrchestration(backup?: ProductBackup): void {
    for (const row of backup?.orchestrationMandates ?? []) this.putMandate(row);
    for (const row of backup?.orchestrationCheckpoints ?? []) this.putCheckpoint(row);
    for (const row of backup?.orchestrationDecisions ?? []) this.putDecision(row);
    for (const row of backup?.conversationNotices ?? []) {
      this.putNotice(row);
      this.advanceTimeline(row.conversationId, row.createdAt, row.timelineSeq + 1);
    }
  }

  orchestrationTransaction<T>(action: () => T): T {
    return this.transaction(() => {
      this.assertSessionEpoch();
      return action();
    });
  }

  orchestrationOperation(operationId: string): OrchestrationOperation | undefined {
    return decode(this.meta('orchestration_operation:' + encode(operationId)) ?? null) as OrchestrationOperation | undefined;
  }

  saveOrchestrationOperation(row: OrchestrationOperation): void {
    this.db.prepare('INSERT INTO meta VALUES (?, ?)').run('orchestration_operation:' + encode(row.receipt.operationId), encode(row));
  }

  queuedSends(): QueuedSend[] {
    return this.db.prepare("SELECT * FROM queued_conversation_sends WHERE state IN ('pending','accepted') ORDER BY rowid").all().map(row => ({
      operationId: decode(row.operation_id) as string, conversationId: decode(row.conversation_id) as string,
      executionId: decode(row.execution_id) as string, draftRevision: Number(row.draft_revision), state: row.state as QueuedSend['state'],
      reason: String(row.reason), route: decode(row.route_json) as object | undefined, dispatchedAt: (row.dispatched_at as string | null) ?? undefined,
      createdAt: String(row.created_at), settledAt: (row.settled_at as string | null) ?? undefined,
    }));
  }

  private putQueuedSend(row: QueuedSend): void {
    this.db.prepare(`INSERT INTO queued_conversation_sends VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(operation_id) DO UPDATE SET state = excluded.state, reason = excluded.reason,
      dispatched_at = excluded.dispatched_at, settled_at = excluded.settled_at`).run(encode(row.operationId), encode(row.conversationId),
      encode(row.executionId), row.draftRevision, row.state, row.reason, encode(row.route), row.dispatchedAt ?? null, row.createdAt, row.settledAt ?? null);
  }

  private putOrchestrationRow(table: string, ...values: (string | number | null)[]): void {
    this.db.prepare(`INSERT OR REPLACE INTO ${table} VALUES (${values.map(() => '?').join(', ')})`).run(...values);
  }

  mandate(mandateId: string): OrchestrationMandate | undefined {
    return decode(this.db.prepare('SELECT record_json FROM orchestration_mandates WHERE mandate_id = ?')
      .get(encode(mandateId))?.record_json ?? null) as OrchestrationMandate | undefined;
  }

  mandates(botId: string): OrchestrationMandate[] {
    return this.db.prepare('SELECT record_json FROM orchestration_mandates WHERE owner_bot_id = ? ORDER BY updated_at DESC, mandate_id')
      .all(encode(botId)).map(row => decode(row.record_json) as OrchestrationMandate);
  }

  putMandate(row: OrchestrationMandate): void {
    this.putOrchestrationRow('orchestration_mandates', encode(row.mandateId), encode(row.ownerBotId), encode(row.targetConversationId),
      row.sourceKind, encode(row.sourceId), row.purpose, row.state, row.revision, row.generation, encode(row.currentRunId),
      row.lastDisposition ?? null, encode(row.lastNoticeId), row.lastReceiptState ?? null, row.lastError ?? null, row.statusReason ?? null,
      Number(row.dormant), row.createdAt, row.updatedAt, row.settledAt ?? null, encode(row));
  }

  private ensureMandate(source: Pick<OrchestrationMandate, 'mandateId' | 'ownerBotId' | 'targetConversationId' | 'sourceKind' | 'sourceId' | 'purpose'>,
    at: string, active: boolean): void {
    const prior = this.mandate(source.mandateId);
    if (!prior) {
      if (active) this.putMandate({ ...source, generation: 1, revision: 1, state: 'active', dormant: false, createdAt: at, updatedAt: at });
      return;
    }
    if (prior.dormant || prior.state === 'stopped') return;
    const state = active ? (prior.state === 'paused' ? this.pendingDecision(prior.mandateId) ? 'waiting_decision' : 'active' : prior.state) : 'paused';
    if (prior.state === state && prior.purpose === source.purpose) return;
    if (!active) this.cancelMandateEvents(prior.mandateId);
    this.putMandate({ ...prior, state, purpose: source.purpose, revision: prior.revision + 1, updatedAt: at });
  }

  private saveWatchMandate(row: RuntimeWatch): void {
    if (!row.targetConversationId || !this.eventTarget(row.ownerBotId, row.targetConversationId)) return;
    this.ensureMandate({ mandateId: 'watch:' + row.watchId, ownerBotId: row.ownerBotId, targetConversationId: row.targetConversationId,
      sourceKind: row.kind, sourceId: row.watchId, purpose: [row.purpose, row.prompt].filter(Boolean).join('\n').slice(0, 1024) },
      row.updatedAt, row.state === 'active');
  }

  cancelMandateEvents(mandateId: string): void {
    this.db.prepare("UPDATE bot_event_inbox SET state = 'cancelled' WHERE mandate_id = ? AND state = 'pending'").run(encode(mandateId));
  }

  checkpoint(mandateId: string): OrchestrationCheckpoint | undefined {
    return decode(this.db.prepare('SELECT checkpoint_json FROM orchestration_checkpoints WHERE mandate_id = ?')
      .get(encode(mandateId))?.checkpoint_json ?? null) as OrchestrationCheckpoint | undefined;
  }

  putCheckpoint(row: OrchestrationCheckpoint): void {
    this.putOrchestrationRow('orchestration_checkpoints', encode(row.mandateId), row.generation, row.revision, row.workingSummary, encode(row), row.updatedAt);
  }

  orchestrationDecision(decisionId: string): OrchestrationDecision | undefined {
    return decode(this.db.prepare('SELECT record_json FROM orchestration_decisions WHERE decision_id = ?')
      .get(encode(decisionId))?.record_json ?? null) as OrchestrationDecision | undefined;
  }

  pendingDecision(mandateId: string): OrchestrationDecision | undefined {
    return decode(this.db.prepare("SELECT record_json FROM orchestration_decisions WHERE mandate_id = ? AND state = 'open'")
      .get(encode(mandateId))?.record_json ?? null) as OrchestrationDecision | undefined;
  }

  putDecision(row: OrchestrationDecision): void {
    this.putOrchestrationRow('orchestration_decisions', encode(row.decisionId), encode(row.mandateId), encode(row.ownerBotId), encode(row.runId),
      row.revision, row.state, row.kind, row.purpose, row.summary, encode(row.targetConversationId), encode(row.options), row.outcome ?? null,
      encode(row.selectedOptionId), row.responseText ?? null, Number(row.dormant), row.statusReason ?? null, row.createdAt, row.updatedAt,
      row.resolvedAt ?? null, encode(row));
  }

  orchestrationBackoff(mandateId: string): OrchestrationBackoff | undefined {
    return decode(this.db.prepare('SELECT record_json FROM orchestration_backoff WHERE mandate_id = ?')
      .get(encode(mandateId))?.record_json ?? null) as OrchestrationBackoff | undefined;
  }

  putBackoff(row: OrchestrationBackoff): void {
    this.putOrchestrationRow('orchestration_backoff', encode(row.mandateId), encode(row.sourceId), row.actionClass, row.observedStateHash,
      row.consecutiveNoProgress, row.backoffDelayMs, row.backoffReason ?? null, row.lastNoticeEpisodeHash ?? null, row.nextEligibleAt ?? null,
      row.updatedAt, encode(row));
  }

  clearBackoff(mandateId: string): void {
    this.db.prepare('DELETE FROM orchestration_backoff WHERE mandate_id = ?').run(encode(mandateId));
  }

  orchestrationSummary(row: OrchestrationMandate): OrchestrationSummary {
    const metrics = this.db.prepare(`SELECT count(*) total, sum(calls) calls, sum(input_tokens) input, sum(output_tokens) output,
      sum(total_tokens) tokens, sum(cost_usd) cost FROM orchestration_runs WHERE mandate_id = ?`).get(encode(row.mandateId))!;
    const last = this.db.prepare('SELECT queue_delay_ms, coalesced_count FROM orchestration_runs WHERE mandate_id = ? ORDER BY rowid DESC LIMIT 1')
      .get(encode(row.mandateId));
    const backoff = this.orchestrationBackoff(row.mandateId);
    return { ...row, currentExecutionState: row.currentRunId ? this.orchestrationRun(row.currentRunId)?.state : undefined,
      pendingDecision: this.pendingDecision(row.mandateId), metrics: { totalWakes: Number(metrics.total), calls: Number(metrics.calls ?? 0),
        inputTokens: Number(metrics.input ?? 0), outputTokens: Number(metrics.output ?? 0), totalTokens: Number(metrics.tokens ?? 0),
        costUsd: Number(metrics.cost ?? 0), lastQueueDelayMs: Number(last?.queue_delay_ms ?? 0), lastCoalescedCount: Number(last?.coalesced_count ?? 0),
        backoffDelayMs: backoff?.backoffDelayMs ?? 0, backoffReason: backoff?.backoffReason,
        consecutiveNoProgress: backoff?.consecutiveNoProgress ?? 0, nextEligibleAt: backoff?.nextEligibleAt } };
  }

  orchestrationDetail(row: OrchestrationMandate) {
    const key = encode(row.mandateId);
    const recentRuns = this.db.prepare('SELECT record_json FROM orchestration_runs WHERE mandate_id = ? ORDER BY rowid DESC LIMIT 20')
      .all(key).map(value => {
        const { ownerBotId, route, dispatchedAt, ...run } = decode(value.record_json) as StoredRun;
        return run;
      });
    const decisions = this.db.prepare('SELECT record_json FROM orchestration_decisions WHERE mandate_id = ? ORDER BY created_at DESC, decision_id LIMIT 20')
      .all(key).map(value => decode(value.record_json) as OrchestrationDecision);
    const recentReceipts = this.db.prepare('SELECT * FROM orchestration_admissions WHERE mandate_id = ? ORDER BY rowid DESC LIMIT 20').all(key)
      .map((value): OrchestrationEffectReceipt => ({ receiptId: decode(value.receipt_id) as string, mandateId: row.mandateId,
        runId: decode(value.run_id) as string, callId: String(value.call_id), tool: String(value.tool), target: decode(value.target) as string | undefined,
        state: String(value.state), reason: (value.reason as string | null) ?? undefined, createdAt: String(value.created_at) }));
    return { ...this.orchestrationSummary(row), recentRuns, decisions, recentReceipts };
  }

  orchestrationRun(runId: string): StoredRun | undefined {
    return decode(this.db.prepare('SELECT record_json FROM orchestration_runs WHERE run_id = ?').get(encode(runId))?.record_json ?? null) as StoredRun | undefined;
  }

  putRun(row: StoredRun): void {
    this.putOrchestrationRow('orchestration_runs', encode(row.runId), encode(row.mandateId), encode(row.ownerBotId), row.generation,
      encode(row.batchId), row.state, row.routeRole ?? null, encode(row.route), row.disposition ?? null, row.summary ?? null,
      encode(row.noticeId), encode(row.decisionId), row.error ?? null, row.queueDelayMs, row.coalescedCount, row.calls, row.inputTokens,
      row.outputTokens, row.totalTokens, row.costUsd, row.dispatchedAt ?? null, row.startedAt, row.settledAt ?? null, encode(row));
  }

  timelineRevision(conversationId: string): number {
    return Number(this.db.prepare('SELECT timeline_revision FROM conversation_timelines WHERE conversation_id = ?')
      .get(encode(conversationId))?.timeline_revision ?? 0);
  }

  conversationNotices(conversationId: string): StoredConversationNotice[] {
    return this.db.prepare(`SELECT record_json FROM conversation_notices WHERE conversation_id = ?
      AND (anchor_kind != 'post_settlement' OR anchor_outcome IS NOT NULL) ORDER BY timeline_seq`).all(encode(conversationId))
      .map(row => decode(row.record_json) as StoredConversationNotice);
  }

  private advanceTimeline(conversationId: string, at: string, nextSeq: number): void {
    this.db.prepare(`INSERT INTO conversation_timelines VALUES (?, 1, ?, ?) ON CONFLICT(conversation_id) DO UPDATE SET
      timeline_revision = timeline_revision + 1, next_seq = max(next_seq, excluded.next_seq), updated_at = excluded.updated_at`)
      .run(encode(conversationId), nextSeq, at);
  }

  private putNotice(row: StoredConversationNotice): void {
    this.putOrchestrationRow('conversation_notices', encode(row.noticeId), encode(row.conversationId), encode(row.botId),
      encode(row.mandateId), encode(row.runId), row.factDigest, row.timelineSeq, row.anchorKind, encode(row.anchorExecutionId),
      row.anchorOutcome ?? null, row.text, encode(row.sourceRefs), row.urgency, Number(row.dormant), row.createdAt, encode(row));
  }

  appendNotice(row: Omit<StoredConversationNotice, 'timelineSeq'>): TimelineChanged | undefined {
    const next = this.db.prepare('SELECT next_seq, timeline_revision FROM conversation_timelines WHERE conversation_id = ?').get(encode(row.conversationId));
    const timelineSeq = Number(next?.next_seq ?? 1);
    this.putNotice({ ...row, timelineSeq });
    if (row.anchorKind === 'post_settlement' && !row.anchorOutcome) {
      this.db.prepare(`INSERT INTO conversation_timelines VALUES (?, 0, ?, ?) ON CONFLICT(conversation_id) DO UPDATE SET next_seq = excluded.next_seq`)
        .run(encode(row.conversationId), timelineSeq + 1, row.createdAt);
      return;
    }
    this.advanceTimeline(row.conversationId, row.createdAt, timelineSeq + 1);
    return { conversationId: row.conversationId, noticeId: row.noticeId, timelineRevision: Number(next?.timeline_revision ?? 0) + 1, at: row.createdAt };
  }

  private finishAnchoredNotices(executionId: string, state: NonNullable<StoredConversationNotice['anchorOutcome']>): void {
    const at = new Date().toISOString();
    for (const row of this.db.prepare('SELECT record_json FROM conversation_notices WHERE anchor_execution_id = ? AND anchor_outcome IS NULL')
      .all(encode(executionId))) {
      const notice = decode(row.record_json) as StoredConversationNotice;
      this.db.prepare('UPDATE conversation_notices SET anchor_outcome = ?, record_json = ? WHERE notice_id = ?')
        .run(state, encode({ ...notice, anchorOutcome: state }), encode(notice.noticeId));
      this.advanceTimeline(notice.conversationId, at, notice.timelineSeq + 1);
    }
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

  assertSessionEpoch(): void {
    if (this.meta('runtime_session_epoch') !== this.sessionEpoch || this.meta('snapshot_sha256') !== this.digest) {
      throw new RequestError('profile_unavailable', 'The worker no longer owns the profile session epoch');
    }
  }

  runtimeSessions(botId?: string, sessionId?: string): RuntimeSession[] {
    const select = 'SELECT record_json FROM runtime_sessions';
    const rows = sessionId === undefined ? (botId === undefined ? this.db.prepare(select).all() :
      this.db.prepare(`${select} WHERE owner_bot_id = ? ORDER BY updated_at DESC, session_id`).all(encode(botId))) :
      this.db.prepare(`${select} WHERE owner_bot_id = ? AND session_id = ?`).all(encode(botId), encode(sessionId));
    const watches = this.db.prepare('SELECT record_json FROM session_watches WHERE session_id = ? ORDER BY rowid');
    return rows.map(row => {
      const session = decode(row.record_json) as Omit<RuntimeSession, 'watches'>;
      return { ...session, watches: watches.all(encode(session.sessionId)).map(row => decode(row.record_json) as RuntimeSession['watches'][number]) };
    });
  }

  private putSession({ session, occurrences }: RuntimeSessionWrite): void {
    const { watches, ...record } = session;
    this.db.prepare('INSERT OR REPLACE INTO runtime_sessions VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)').run(encode(record.sessionId),
      encode(record.ownerBotId), encode(record.originConversationId), encode(record.originActivityId), record.generation,
      record.revision, record.state, record.updatedAt, encode(record));
    const watch = this.db.prepare(`INSERT INTO session_watches VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(watch_id) DO UPDATE SET
      revision = excluded.revision, state = excluded.state, record_json = excluded.record_json WHERE excluded.revision > session_watches.revision`);
    for (const row of watches) watch.run(encode(row.watchId), encode(row.sessionId), encode(record.ownerBotId), row.generation,
      row.revision, row.state, encode(row));
    const targetConversationId = this.eventTarget(record.ownerBotId, record.originConversationId);
    const liveOccurrence = occurrences?.some(row => row.firedKind !== 'target_lost');
    const mandateId = !record.originActivityId && record.epoch === this.sessionEpoch && targetConversationId ? 'session:' + record.sessionId : undefined;
    if (mandateId && targetConversationId && (liveOccurrence || watches.some(row => row.state === 'armed'))) {
      this.ensureMandate({ mandateId, ownerBotId: record.ownerBotId, targetConversationId, sourceKind: 'session', sourceId: record.sessionId,
        purpose: record.label || 'Observe a supervised session' }, record.updatedAt, true);
    }
    for (const row of occurrences ?? []) {
      this.putSessionOccurrence(row);
      this.insertInbox({ ownerBotId: row.ownerBotId, targetConversationId, mandateId: row.firedKind === 'target_lost' ? undefined : mandateId,
        sourceKind: row.firedKind === 'target_lost' ? 'target_lost' : 'session_watch_fired', sourceId: row.watchId,
        occurrenceId: row.occurrenceId, at: row.firedAt, payload: row });
    }
  }

  private putSessionOccurrence(row: SessionWatchOccurrence): void {
    this.db.prepare('INSERT INTO runtime_watch_occurrences VALUES (?, ?, ?, ?, ?, ?, ?, ?)').run(encode(row.occurrenceId),
      encode(row.watchId), encode(row.sessionId), encode(row.ownerBotId), row.generation, row.firedKind, row.firedAt, encode(row));
  }

  saveRuntimeSession(write: RuntimeSessionWrite): void {
    this.transaction(() => {
      this.assertSessionEpoch();
      this.putSession(write);
    });
  }

  recoverRuntimeSessions(): void {
    this.transaction(() => {
      if (this.meta('snapshot_sha256') !== this.digest) throw new RequestError('profile_unavailable', 'The SQLite profile changed in another worker');
      this.db.prepare("INSERT OR REPLACE INTO meta VALUES ('runtime_session_epoch', ?)").run(this.sessionEpoch);
      const at = new Date().toISOString();
      for (const session of this.runtimeSessions()) {
        const live = session.state === 'starting' || session.state === 'running';
        const occurrences: SessionWatchOccurrence[] = [];
        const watches = session.watches.map(watch => {
          if (watch.state !== 'armed') return watch;
          const fired = { occurrenceId: randomUUID(), firedKind: 'target_lost' as const, firedAt: at };
          occurrences.push({ ...fired, watchId: watch.watchId, sessionId: session.sessionId, ownerBotId: session.ownerBotId,
            generation: session.generation });
          return { ...watch, ...fired, state: 'target_lost' as const, revision: watch.revision + 1 };
        });
        if (live || occurrences.length) this.putSession({ session: { ...session, watches, revision: session.revision + 1, updatedAt: at,
          ...(live ? { state: 'disconnected', transportStatus: 'worker_restarted', eof: true, authPause: undefined,
            settledAt: at, firstAvailableOffset: session.nextOffset, evictedBytes: session.nextOffset, childReaped: false } : {}) }, occurrences });
      }
    });
  }

  runtimeWatches(botId?: string): RuntimeWatch[] {
    const columns = 'SELECT record_json FROM runtime_watches';
    const order = 'ORDER BY updated_at DESC, watch_id';
    const rows = botId === undefined ? this.db.prepare(`${columns} ${order}`).all() :
      this.db.prepare(`${columns} WHERE owner_bot_id = ? ${order}`).all(encode(botId));
    return rows.map(row => decode(row.record_json) as RuntimeWatch);
  }

  runtimeWatch(botId: string, watchId: string): RuntimeWatch | undefined {
    const row = this.db.prepare('SELECT record_json FROM runtime_watches WHERE owner_bot_id = ? AND watch_id = ?').get(encode(botId), encode(watchId));
    return decode(row?.record_json ?? null) as RuntimeWatch | undefined;
  }

  dueWatches(at: string): RuntimeWatch[] {
    return this.db.prepare("SELECT record_json FROM runtime_watches WHERE state = 'active' AND next_due_at <= ? ORDER BY next_due_at, watch_id")
      .all(at).map(row => decode(row.record_json) as RuntimeWatch);
  }

  watchGrants(installationId: string, botId: string): RuntimeWatchGrant[] {
    return this.db.prepare('SELECT record_json FROM watch_grants WHERE installation_id = ? AND owner_bot_id = ? ORDER BY updated_at DESC, grant_id')
      .all(installationId, encode(botId)).map(row => decode(row.record_json) as RuntimeWatchGrant);
  }

  watchGrant(installationId: string, botId: string, grantId?: string): RuntimeWatchGrant | undefined {
    if (grantId === undefined) return;
    const row = this.db.prepare('SELECT record_json FROM watch_grants WHERE installation_id = ? AND owner_bot_id = ? AND grant_id = ?')
      .get(installationId, encode(botId), encode(grantId));
    return decode(row?.record_json ?? null) as RuntimeWatchGrant | undefined;
  }

  watchOccurrences(watchId: string, limit = 20): WatchOccurrence[] {
    return this.db.prepare('SELECT record_json FROM watch_occurrences WHERE watch_id = ? ORDER BY scheduled_at DESC, occurrence_id LIMIT ?')
      .all(encode(watchId), limit).map(row => decode(row.record_json) as WatchOccurrence);
  }

  watchOperation(operationId: string): WatchOperation | undefined {
    const text = this.meta('watch_operation:' + encode(operationId));
    return decode(text ?? null) as WatchOperation | undefined;
  }

  private putRuntimeWatch(row: RuntimeWatch): void {
    this.db.prepare('INSERT OR REPLACE INTO runtime_watches VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)').run(encode(row.watchId), encode(row.ownerBotId),
      encode(row.targetConversationId), encode(row.originActivityId), row.kind, row.revision, row.state, row.nextDueAt ?? null, row.updatedAt, encode(row));
  }

  private putWatchGrant(row: RuntimeWatchGrant): void {
    this.db.prepare('INSERT OR REPLACE INTO watch_grants VALUES (?, ?, ?, ?, ?, ?, ?)').run(encode(row.grantId), row.installationId,
      encode(row.ownerBotId), row.revision, row.state, row.updatedAt, encode(row));
  }

  writeWatches({ watches, grants, operation }: WatchChanges): void {
    this.transaction(() => {
      this.assertSessionEpoch();
      for (const row of grants ?? []) this.putWatchGrant(row);
      for (const row of watches ?? []) {
        this.putRuntimeWatch(row);
        this.saveWatchMandate(row);
      }
      if (operation) {
        this.db.prepare('INSERT INTO meta (key, value) VALUES (?, ?)').run('watch_operation:' + encode(operation.receipt.operationId), encode(operation));
      }
    });
  }

  private invalidateBotWatches(botId: string, reason: 'bot_archived' | 'workspace_changed'): void {
    const at = new Date().toISOString();
    if (reason === 'workspace_changed') {
      const grants = this.db.prepare("SELECT record_json FROM watch_grants WHERE owner_bot_id = ? AND state = 'active'").all(encode(botId));
      for (const value of grants) {
        const row = decode(value.record_json) as RuntimeWatchGrant;
        this.putWatchGrant({ ...row, state: 'revoked', revision: row.revision + 1, updatedAt: at, revokedAt: at, revokedReason: reason });
      }
    }
    for (const row of this.runtimeWatches(botId)) {
      if (reason === 'bot_archived' ? row.state !== 'active' : !row.precheck || row.state === 'settled' || row.state === 'cancelled') continue;
      const next = { ...row, revision: row.revision + 1, state: reason === 'bot_archived' ? 'paused' as const : 'invalidated' as const,
        statusReason: reason, armed: false, nextDueAt: undefined, scheduledAt: undefined, updatedAt: at };
      this.putRuntimeWatch(next);
      this.saveWatchMandate(next);
    }
    if (reason === 'bot_archived') for (const mandate of this.mandates(botId)) {
      if (mandate.state !== 'active' && mandate.state !== 'waiting_decision') continue;
      this.cancelMandateEvents(mandate.mandateId);
      this.putMandate({ ...mandate, state: 'paused', revision: mandate.revision + 1, statusReason: reason, updatedAt: at });
    }
  }

  private insertWatchOccurrence(row: WatchOccurrence): void {
    this.db.prepare('INSERT INTO watch_occurrences VALUES (?, ?, ?, ?, ?, ?, ?, ?)').run(encode(row.occurrenceId), encode(row.watchId),
      encode(row.ownerBotId), row.watchRevision, row.scheduledAt, row.state, row.createdAt, encode(row));
  }

  private updateWatchOccurrence(row: WatchOccurrence): void {
    this.db.prepare('UPDATE watch_occurrences SET state = ?, record_json = ? WHERE occurrence_id = ?')
      .run(row.state, encode(row), encode(row.occurrenceId));
  }

  recordWatchTick(before: RuntimeWatch, next: RuntimeWatch, occurrence: WatchOccurrence, input?: InboxInput): RuntimeWatch | undefined {
    return this.transaction(() => {
      this.assertSessionEpoch();
      const current = this.runtimeWatch(before.ownerBotId, before.watchId);
      if (current?.state !== 'active' || current.revision !== before.revision || current.nextDueAt !== before.nextDueAt) return;
      this.insertWatchOccurrence(occurrence);
      return this.finishWatchTick(next, occurrence, input);
    });
  }

  settleWatchTick(next: RuntimeWatch, occurrence: WatchOccurrence, input?: InboxInput): RuntimeWatch | undefined {
    return this.transaction(() => {
      this.assertSessionEpoch();
      const pending = this.db.prepare('SELECT state FROM watch_occurrences WHERE occurrence_id = ?').get(encode(occurrence.occurrenceId));
      if (pending?.state !== 'running') return;
      const current = this.runtimeWatch(next.ownerBotId, next.watchId);
      if (current?.state !== 'active' || current.revision !== next.revision) {
        this.updateWatchOccurrence({ ...occurrence, state: 'cancelled', outcome: 'cancelled', digest: undefined, excerpt: undefined, error: undefined });
        return;
      }
      return this.finishWatchTick(next, occurrence, input);
    });
  }

  private finishWatchTick(next: RuntimeWatch, occurrence: WatchOccurrence, input?: InboxInput): RuntimeWatch {
    if (input) {
      const mandateId = 'watch:' + next.watchId;
      if (!this.mandate(mandateId) && next.targetConversationId) this.saveWatchMandate({ ...next, state: 'active' });
      const { eventId, coalesced } = this.insertInbox({ ...input, mandateId });
      occurrence = { ...occurrence, eventId, ...(coalesced ? { coalescedInto: eventId } : {}) };
      next = { ...next, lastEventId: eventId };
      this.updateWatchOccurrence(occurrence);
    } else if (occurrence.state !== 'running') this.updateWatchOccurrence(occurrence);
    this.putRuntimeWatch(next);
    if (['unchanged', 'suppressed_failure', 'baseline'].includes(occurrence.outcome)) {
      this.db.prepare(`DELETE FROM watch_occurrences WHERE occurrence_id IN (
        SELECT occurrence_id FROM watch_occurrences WHERE watch_id = ? AND state = 'settled'
        AND json_extract(record_json, '$.outcome') IN ('unchanged','suppressed_failure','baseline')
        ORDER BY scheduled_at DESC, occurrence_id LIMIT -1 OFFSET 20)`).run(encode(next.watchId));
    }
    return next;
  }

  private eventTarget(botId: string, conversationId?: string): string | undefined {
    if (conversationId === undefined) return;
    const row = this.db.prepare('SELECT bot_id FROM conversations WHERE conversation_id = ?').get(encode(conversationId));
    return row?.bot_id === encode(botId) ? conversationId : undefined;
  }

  private putActivityEvent(activity: ProductProfile['activities'][number], kind: string, identity: unknown[], extra: object, at: string): void {
    this.insertInbox({ ownerBotId: activity.ownerBotId, targetConversationId: this.eventTarget(activity.ownerBotId, activity.originConversationId),
      mandateId: activity.originMandateId,
      sourceKind: 'activity_milestone', sourceId: activity.activityId,
      occurrenceId: sha256(Buffer.from(canonicalJSON(['activity_milestone', activity.activityId, kind, ...identity]))), at,
      payload: { kind, activityId: activity.activityId, revision: activity.revision, originConversationId: activity.originConversationId, ...extra } });
  }

  insertInbox(input: InboxInput): { eventId: string; coalesced: boolean } {
    const mandate = input.mandateId ? this.mandate(input.mandateId) : undefined;
    const routed = mandate?.ownerBotId === input.ownerBotId && mandate.targetConversationId === input.targetConversationId &&
      !mandate.dormant && (mandate.state === 'active' || mandate.state === 'waiting_decision');
    const mandateKey = encode(routed ? mandate.mandateId : undefined);
    if (routed && input.sourceKind === 'schedule_precheck' && ['changed', 'recovered'].includes(String((input.payload as { transition?: string }).transition))) {
      this.clearBackoff(mandate.mandateId);
    }
    if (input.coalesceKey) {
      const latest = this.db.prepare(`SELECT event_id, state, coalesce_key FROM bot_event_inbox
        WHERE owner_bot_id = ? AND source_id = ? AND target_conversation_id IS ? AND source_kind = ? AND mandate_id IS ? ORDER BY rowid DESC LIMIT 1`)
        .get(encode(input.ownerBotId), encode(input.sourceId), encode(input.targetConversationId), input.sourceKind, mandateKey);
      const row = latest?.state === 'pending' && latest.coalesce_key === input.coalesceKey ? latest : undefined;
      if (row) {
        this.db.prepare('UPDATE bot_event_inbox SET coalesced_count = coalesced_count + 1, updated_at = ?, payload_json = ? WHERE event_id = ?')
          .run(input.at, encode({ ...input.payload, latestOccurrenceId: input.occurrenceId }), row.event_id);
        return { eventId: decode(row.event_id) as string, coalesced: true };
      }
    }
    const eventId = randomUUID();
    this.db.prepare(`INSERT INTO bot_event_inbox VALUES (?, ?, ?, ?, ?, ?, ?, 1, 'pending', NULL, ?, ?, ?, ?)`)
      .run(encode(eventId), encode(input.ownerBotId), encode(input.targetConversationId), input.sourceKind, encode(input.sourceId),
        encode(input.occurrenceId), input.coalesceKey ?? null, input.at, input.at, encode(input.payload), mandateKey);
    return { eventId, coalesced: false };
  }

  listEventInbox(botId: string, state?: InboxState): InboxEvent[] {
    const where = state === undefined ? 'owner_bot_id = ?' : 'owner_bot_id = ? AND state = ?';
    const ids = state === undefined ? [encode(botId)] : [encode(botId), state];
    return this.db.prepare(`SELECT * FROM bot_event_inbox WHERE ${where} ORDER BY rowid`).all(...ids).map(row => ({
      eventId: decode(row.event_id) as string, ownerBotId: botId, targetConversationId: decode(row.target_conversation_id) as string | undefined,
      mandateId: decode(row.mandate_id) as string | undefined,
      sourceKind: row.source_kind as InboxSource, sourceId: decode(row.source_id) as string, occurrenceId: decode(row.occurrence_id) as string,
      coalesceKey: typeof row.coalesce_key === 'string' ? row.coalesce_key : undefined, coalescedCount: Number(row.coalesced_count), state: row.state as InboxState,
      batchId: decode(row.batch_id) as string | undefined, createdAt: String(row.created_at), updatedAt: String(row.updated_at), payload: decode(row.payload_json) as object,
    }));
  }

  readyMandates(at: string): OrchestrationMandate[] {
    return this.db.prepare(`SELECT m.record_json FROM orchestration_mandates m LEFT JOIN orchestration_backoff b USING (mandate_id)
      WHERE m.state = 'active' AND m.dormant = 0 AND m.current_run_id IS NULL AND (b.next_eligible_at IS NULL OR b.next_eligible_at <= ?)
      AND EXISTS (SELECT 1 FROM bot_event_inbox i WHERE i.mandate_id = m.mandate_id AND i.state = 'pending')
      ORDER BY (SELECT min(i.rowid) FROM bot_event_inbox i WHERE i.mandate_id = m.mandate_id AND i.state = 'pending')`)
      .all(at).map(row => decode(row.record_json) as OrchestrationMandate);
  }

  wakeEvents(mandate: OrchestrationMandate): InboxEvent[] {
    return this.db.prepare(`SELECT * FROM bot_event_inbox WHERE mandate_id = ? AND owner_bot_id = ? AND target_conversation_id = ?
      AND state = 'pending' ORDER BY rowid LIMIT 8`).all(encode(mandate.mandateId), encode(mandate.ownerBotId), encode(mandate.targetConversationId))
      .map(row => ({ eventId: decode(row.event_id) as string, ownerBotId: mandate.ownerBotId, targetConversationId: mandate.targetConversationId,
        mandateId: mandate.mandateId, sourceKind: row.source_kind as InboxSource, sourceId: decode(row.source_id) as string,
        occurrenceId: decode(row.occurrence_id) as string, coalescedCount: Number(row.coalesced_count), state: 'pending',
        createdAt: String(row.created_at), updatedAt: String(row.updated_at), payload: decode(row.payload_json) as object }));
  }

  stageWake(mandate: OrchestrationMandate, runId: string, events: InboxEvent[], at: string): EventBatch {
    const batch: EventBatch = { batchId: randomUUID(), ownerBotId: mandate.ownerBotId, targetConversationId: mandate.targetConversationId,
      mandateId: mandate.mandateId, leaseId: runId, stage: 'staged', dispatched: false, createdAt: at, updatedAt: at, eventIds: events.map(row => row.eventId) };
    this.db.prepare('INSERT INTO bot_event_batches VALUES (?, ?, ?, ?, ?, ?, ?, ?)').run(encode(batch.batchId), encode(batch.ownerBotId),
      encode(runId), batch.stage, 0, at, at, encode(batch));
    const statement = this.db.prepare(`UPDATE bot_event_inbox SET state = 'claimed', batch_id = ?, updated_at = ?
      WHERE event_id = ? AND mandate_id = ? AND state = 'pending' AND coalesced_count = ? AND updated_at = ?`);
    for (const row of events) if (!statement.run(encode(batch.batchId), at, encode(row.eventId), encode(mandate.mandateId),
      row.coalescedCount, row.updatedAt).changes) {
      throw new RequestError('stale_revision', 'The orchestration event changed before admission');
    }
    return batch;
  }

  dispatchWake(actor: OrchestrationActor, batch: EventBatch, at: string): EventBatch {
    return this.orchestrationTransaction(() => {
      const mandate = this.mandate(actor.mandateId);
      const run = this.orchestrationRun(actor.runId);
      if (!run || run.state !== 'starting' || mandate?.currentRunId !== actor.runId || mandate.state !== 'active' || mandate.generation !== actor.generation) {
        throw new RequestError('invalid_state', 'The orchestration run is no longer admitted');
      }
      this.putRun({ ...run, state: 'running', dispatchedAt: at });
      return this.settleBatch(batch, 'dispatched');
    });
  }

  finishWake(batch: EventBatch, unknown: boolean): void {
    this.settleBatch(batch, !batch.dispatched ? 'released' : unknown ? 'unknown' : 'completed');
  }

  admitOrchestrationTool(actor: OrchestrationActor, callId: string, tool: string, target: string, mutating: boolean, at: string): void {
    this.orchestrationTransaction(() => {
      const mandate = this.mandate(actor.mandateId);
      const run = this.orchestrationRun(actor.runId);
      if (mandate?.ownerBotId !== actor.botId || mandate.currentRunId !== actor.runId || mandate.generation !== actor.generation ||
          !['active', 'waiting_decision'].includes(mandate.state) || run?.batchId !== actor.batchId || run.state !== 'running') {
        throw new RequestError('scope_denied', 'The orchestration tool is outside its admitted run');
      }
      this.db.prepare('INSERT INTO orchestration_admissions VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)')
        .run(encode(randomUUID()), encode(actor.mandateId), encode(actor.runId), callId, tool, encode(target), mutating ? 'mutating' : 'readonly',
          'admitted', null, at);
    });
  }

  settleOrchestrationTool(runId: string, callId: string, isError: boolean): void {
    this.db.prepare("UPDATE orchestration_admissions SET state = ? WHERE run_id = ? AND call_id = ? AND state = 'admitted'")
      .run(isError ? 'settled_error' : 'settled_ok', encode(runId), callId);
  }

  finishOrchestrationAdmissions(runId: string): boolean {
    const key = encode(runId);
    const uncertain = this.db.prepare("SELECT 1 FROM orchestration_admissions WHERE run_id = ? AND state = 'admitted' AND tool_class = 'mutating'").get(key);
    this.db.prepare("UPDATE orchestration_admissions SET state = 'unknown', reason = 'tool_settlement_unobserved' WHERE run_id = ? AND state = 'admitted'").run(key);
    return !!uncertain;
  }

  recordOrchestrationUsage(actor: OrchestrationActor, usage: Omit<ReturnType<typeof emptyOrchestrationUsage>, 'calls'>): void {
    this.orchestrationTransaction(() => {
      const run = this.orchestrationRun(actor.runId);
      if (run?.state !== 'running' || run.batchId !== actor.batchId) return;
      this.putRun({ ...run, calls: run.calls + 1, inputTokens: run.inputTokens + usage.inputTokens, outputTokens: run.outputTokens + usage.outputTokens,
        totalTokens: run.totalTokens + usage.totalTokens, costUsd: run.costUsd + usage.costUsd });
    });
  }

  disarmMandateWatch(row: OrchestrationMandate, at: string): RuntimeWatch | undefined {
    if (!row.sourceId || row.sourceKind === 'session' || row.sourceKind === 'user') return;
    const watch = this.runtimeWatch(row.ownerBotId, row.sourceId);
    if (!watch || watch.state !== 'active') return;
    this.putRuntimeWatch({ ...watch, revision: watch.revision + 1, state: 'paused', armed: false, statusReason: 'mandate_stopped',
      nextDueAt: undefined, scheduledAt: undefined, updatedAt: at });
    return watch;
  }

  private recoverOrchestration(): void {
    const at = new Date().toISOString();
    for (const value of this.db.prepare("SELECT record_json FROM orchestration_runs WHERE state IN ('starting','running')").all()) {
      const run = decode(value.record_json) as StoredRun;
      const state = run.dispatchedAt ? 'unknown' : 'failed';
      const mandate = this.mandate(run.mandateId)!;
      this.putRun({ ...run, state, disposition: state, error: 'worker_lost', settledAt: at });
      this.putMandate({ ...mandate, state: mandate.state === 'active' && state === 'unknown' ? 'unknown' : mandate.state,
        currentRunId: undefined, lastDisposition: state, lastReceiptState: state === 'unknown' ? 'unknown' : 'non_dispatched',
        lastError: 'worker_lost', revision: mandate.revision + 1, updatedAt: at, settledAt: at });
    }
    this.db.exec("UPDATE orchestration_admissions SET state = 'unknown', reason = 'worker_lost' WHERE state = 'admitted'");
  }

  private captureOrchestration(redact: (text: string) => string) {
    const records = <T>(table: string, column = 'record_json') => this.db.prepare(`SELECT ${column} FROM ${table} ORDER BY rowid`).all()
      .map(row => decode(row[column]) as T);
    const orchestrationMandates = records<OrchestrationMandate>('orchestration_mandates').map(row => ({ ...row,
      purpose: redact(row.purpose).slice(0, 1024), lastError: row.lastError && redact(row.lastError).slice(0, 2048), currentRunId: undefined,
      state: 'restored_readonly' as const, dormant: true, statusReason: 'restored_dormant' }));
    const orchestrationCheckpoints = records<OrchestrationCheckpoint>('orchestration_checkpoints', 'checkpoint_json')
      .map(row => ({ ...row, workingSummary: redact(row.workingSummary).slice(0, 2048) }));
    const orchestrationDecisions = records<OrchestrationDecision>('orchestration_decisions').map(row => ({ ...row,
      purpose: redact(row.purpose).slice(0, 256), kind: redact(row.kind), summary: redact(row.summary).slice(0, 2048),
      options: row.options.map(option => ({ ...option, label: redact(option.label).slice(0, 160), description: option.description && redact(option.description).slice(0, 512) })),
      responseText: row.responseText && redact(row.responseText).slice(0, 2048), dormant: true }));
    const conversationNotices = records<StoredConversationNotice>('conversation_notices').map(row => ({ ...row,
      text: redact(row.text).slice(0, 2048), sourceRefs: row.sourceRefs.map(ref => ({ ...ref, label: ref.label && redact(ref.label).slice(0, 160) })), dormant: true }));
    return orchestrationMandates.length ? { orchestrationMandates, orchestrationCheckpoints, orchestrationDecisions, conversationNotices } : {};
  }

  claimEventBatch(botId: string, leaseId: string, targetConversationId?: string): EventBatch | undefined {
    return this.transaction(() => {
      this.assertSessionEpoch();
      if (!leaseId || targetConversationId !== undefined && this.eventTarget(botId, targetConversationId) === undefined) {
        throw new RequestError('scope_denied', 'The event batch target is outside the Bot scope');
      }
      const prior = this.db.prepare('SELECT record_json FROM bot_event_batches WHERE owner_bot_id = ? AND lease_id = ?').get(encode(botId), encode(leaseId));
      if (prior) {
        const batch = decode(prior.record_json) as EventBatch;
        if (batch.targetConversationId !== targetConversationId) throw new RequestError('operation_conflict', 'The lease is bound to a different target');
        return batch;
      }
      if (this.db.prepare("SELECT 1 FROM bot_event_batches WHERE owner_bot_id = ? AND stage IN ('staged','dispatched')").get(encode(botId))) return;
      const filter = targetConversationId === undefined ? '' : ' AND target_conversation_id = ?';
      const ids = targetConversationId === undefined ? [encode(botId)] : [encode(botId), encode(targetConversationId)];
      const events = this.db.prepare(`SELECT event_id FROM bot_event_inbox WHERE owner_bot_id = ? AND state = 'pending'${filter}
        ORDER BY rowid LIMIT 32`).all(...ids);
      if (!events.length) return;
      const at = new Date().toISOString();
      const batch: EventBatch = { batchId: randomUUID(), ownerBotId: botId, targetConversationId, leaseId, stage: 'staged', dispatched: false,
        createdAt: at, updatedAt: at, eventIds: events.map(row => decode(row.event_id) as string) };
      this.db.prepare('INSERT INTO bot_event_batches VALUES (?, ?, ?, ?, ?, ?, ?, ?)').run(encode(batch.batchId), encode(botId), encode(leaseId),
        batch.stage, 0, at, at, encode(batch));
      const claim = this.db.prepare("UPDATE bot_event_inbox SET state = 'claimed', batch_id = ?, updated_at = ? WHERE event_id = ?");
      for (const row of events) claim.run(encode(batch.batchId), at, row.event_id);
      return batch;
    });
  }

  markBatchDispatched(batchId: string): EventBatch { return this.changeBatch(batchId, 'staged', 'dispatched'); }
  releaseUndispatchedBatch(batchId: string): EventBatch { return this.changeBatch(batchId, 'staged', 'released'); }
  completeEventBatch(batchId: string): EventBatch { return this.changeBatch(batchId, 'dispatched', 'completed'); }

  private changeBatch(batchId: string, from: EventBatch['stage'], to: EventBatch['stage']): EventBatch {
    return this.transaction(() => {
      this.assertSessionEpoch();
      const row = this.db.prepare('SELECT record_json FROM bot_event_batches WHERE batch_id = ?').get(encode(batchId));
      if (!row) throw new RequestError('not_found', 'The event batch does not exist');
      const batch = decode(row.record_json) as EventBatch;
      if (batch.stage !== from) throw new RequestError('invalid_state', 'The event batch cannot enter that stage');
      return this.settleBatch(batch, to);
    });
  }

  private settleBatch(batch: EventBatch, stage: EventBatch['stage']): EventBatch {
    const next = { ...batch, stage, dispatched: batch.dispatched || stage === 'dispatched', updatedAt: new Date().toISOString() };
    this.db.prepare('UPDATE bot_event_batches SET stage = ?, dispatched = ?, updated_at = ?, record_json = ? WHERE batch_id = ?')
      .run(next.stage, Number(next.dispatched), next.updatedAt, encode(next), encode(next.batchId));
    if (stage === 'released') {
      this.db.prepare("UPDATE bot_event_inbox SET state = 'pending', batch_id = NULL, updated_at = ? WHERE batch_id = ? AND state = 'claimed'")
        .run(next.updatedAt, encode(batch.batchId));
    } else if (stage === 'completed' || stage === 'unknown') {
      this.db.prepare("UPDATE bot_event_inbox SET state = ?, updated_at = ? WHERE batch_id = ? AND state = 'claimed'")
        .run(stage === 'completed' ? 'consumed' : 'unknown', next.updatedAt, encode(batch.batchId));
    }
    return next;
  }

  reconcileInterruptedBatches(): void {
    this.transaction(() => {
      this.assertSessionEpoch();
      for (const row of this.db.prepare("SELECT record_json FROM bot_event_batches WHERE stage IN ('staged','dispatched')").all()) {
        const batch = decode(row.record_json) as EventBatch;
        this.settleBatch(batch, batch.dispatched ? 'unknown' : 'released');
      }
      this.recoverOrchestration();
      for (const row of this.db.prepare("SELECT record_json FROM watch_occurrences WHERE state = 'running'").all()) {
        const occurrence = decode(row.record_json) as WatchOccurrence;
        const at = new Date().toISOString();
        this.updateWatchOccurrence({ ...occurrence, state: 'unknown', outcome: 'unknown', settledAt: at });
        const watch = this.runtimeWatch(occurrence.ownerBotId, occurrence.watchId);
        if (watch?.revision === occurrence.watchRevision) {
          this.putRuntimeWatch({ ...watch, lastOutcome: 'unknown', lastOccurrenceId: occurrence.occurrenceId, updatedAt: at });
        }
      }
    });
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
      const now = new Date().toISOString();
      const runtimeSessions = this.runtimeSessions().map(({ epoch, childReaped, authPause, ...session }): SessionSummary => ({ ...session,
        state: session.state === 'starting' || session.state === 'running' ? 'disconnected' : session.state,
        transportStatus: session.transportStatus === 'live' ? 'worker_restarted' : session.transportStatus,
        eof: true, firstAvailableOffset: session.nextOffset, evictedBytes: session.nextOffset, settledAt: session.settledAt ?? now,
        watches: session.watches.map(watch => watch.state === 'armed' ? { ...watch, state: 'cancelled', revision: watch.revision + 1 } : watch),
      }));
      const runtimeWatchOccurrences = this.db.prepare('SELECT record_json FROM runtime_watch_occurrences ORDER BY fired_at, occurrence_id').all()
        .map(row => decode(row.record_json) as SessionWatchOccurrence);
      const watches = this.runtimeWatches().map(row => {
        const watch = publicWatch(row);
        const state = watch.state === 'cancelled' || watch.kind === 'timer' && watch.state === 'settled' ? watch.state : 'restored';
        return { ...watch, state, armed: false, grantId: undefined, nextDueAt: undefined, lastEventId: undefined,
          statusReason: state === 'restored' ? 'restored_dormant' : watch.statusReason } satisfies WatchSummary;
      });
      const watchOccurrences = this.db.prepare("SELECT record_json FROM watch_occurrences WHERE state != 'running' ORDER BY scheduled_at, occurrence_id").all()
        .map(row => decode(row.record_json) as WatchOccurrence).map(({ eventId, coalescedInto, ...row }) => row);
      return { profile, backup: { version: 1, traces, traceCoverage, ...(activityEscalations.length ? { activityEscalations } : {}),
        ...(activityProgress.length ? { activityProgress } : {}), ...(skillApprovalRequests.length ? { skillApprovalRequests } : {}),
        ...(triageAccounting.length ? { triageAccounting } : {}), ...(activitySteers.length ? { activitySteers } : {}),
        ...(runtimeSessions.length ? { runtimeSessions, runtimeWatchOccurrences } : {}), ...(watches.length ? { watches, watchOccurrences } : {}),
        ...this.captureOrchestration(redact) } };
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
    for (const activity of profile.activities) {
      const { activityId, state, executionIds, revision } = activity;
      const previous = oldActivities.get(activityId);
      if (previous?.revision === revision && previous.executionIds.every(id => oldExecutions.get(id) === executions.get(id))) continue;
      const at = activity.updatedAt ?? new Date().toISOString();
      const executionId = executionIds.at(-1);
      if (previous?.state !== state && (state === 'completed' || state === 'needsAttention')) {
        this.putActivityEvent(activity, state === 'completed' ? 'completed' : 'needs_attention', [revision], { executionId }, at);
      }
      if (state === 'waitingForAnswer') {
        for (const question of activity.questions) {
          if (question.state === 'open' && !previous?.questions.some(row => row.questionId === question.questionId && row.state === 'open')) {
            this.putActivityEvent(activity, 'question_open', [question.questionId, question.revision], { executionId, questionId: question.questionId }, at);
          }
        }
      }
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
      originMandateId: decode(row.origin_mandate_id),
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
      results_json, binding_json, retained_json, granted_linked_activity_ids_json, routing_json, acknowledged_at, acknowledged_execution_id, closed_at,
      updated_at, origin_mandate_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`);
    profile.activities.forEach((row, order) => activities.run(encode(row.activityId), order, encode(row.ownerBotId), encode(row.originConversationId),
      row.revision, row.state, encode(row.goal), encode(row.requirements), encode(row.questions), encode(row.materials), encode(row.inputFiles),
      encode(row.executionIds), encode(row.results), encode(row.binding), encode(row.retained), encode(row.grantedLinkedActivityIds ?? []), encode(row.routing),
      row.acknowledgedAt ?? null, encode(row.acknowledgedExecutionId), row.closedAt ?? null, row.updatedAt ?? null, encode(row.originMandateId)));
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
