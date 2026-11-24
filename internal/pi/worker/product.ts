/// <reference lib="es2024.promise" />

import { createHash, randomUUID } from 'node:crypto';
import { mkdir, stat } from 'node:fs/promises';
import { realpathSync } from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { setImmediate } from 'node:timers/promises';
import type { ToolDefinition } from '@earendil-works/pi-coding-agent';
import type { AssistantMessage, ToolCall } from '@earendil-works/pi-ai';
import { Type, type Static, type TSchema } from 'typebox';
import { Value } from 'typebox/value';
import {
  MAX_FRAME, RequestError, canonicalJSON, draftSchema, frameBytes, hasCanonicalQuoteText, id, inspectionPage, profileSchema, resolveCodingTools, skillExecutable, skillID, skillScriptName, skillSource, skillText, toolPresetSchema,
  type ActivityState, type BotChange, type BotProfile, type Connection, type Draft, type Execution,
  type Material, type McpServerRecord, type McpServerView, type OpenParams, type Operation, type ProductCommand, type ProductParams, type SkillApproval,
  type SkillChange, type SkillDetail, type SkillListing, type SkillPermissions, type SkillSource, type TraceItem,
} from './protocol.js';
import { backupProductProfile, privateCredentialKeys, redactBackupProfile, saveCredential, type PrivateConnection, type ProductProfilePaths, type StoredConnection } from './product-profile.js';
import { ProductSQLite, type RoutingWrite, type SkillApprovalChange } from './product-sqlite.js';
import { composeBotSystemPrompt, formatTightBotInstructions, readTightBotInstructions,
  PANDORABOX_BOT_ID, SYSTEM_KIND_BOT_ARCHITECT, type BotSystemKind } from './bot-template.js';
import { completedWorkSummary, formatLastingRulesBlock, lastingRulesFit, MEMORY_COLD_TTL_MS, MAX_LOG_BYTES, MAX_PENDING_LEARNINGS,
  MAX_RULE_BYTES, MAX_WORK_LOGS, memoryDate, memorySearchLimit, memorySearchQuery, memoryTags, memoryText, newestLogFirst, oldestRuleFirst,
  recurringPreference, validMemoryDate, validateMemoryCandidate, type BotMemory, type BotMemoryMeta, type BotMemoryRule,
  type BotPendingLearning, type BotWorkLog } from './bot-memory.js';
import { directlyOwns, normalizeActivityGrants, redactTrace, type TraceObservation, type TraceUpdate } from './product-trace.js';
import { cursorScope, decodeCursor, encodeCursor, encodedBytes, inspectConversation, inspectionQuery, invalidCursor, oversizedEntry,
  publicConversationMessage, redactQuote, type InspectionParams } from './product-inspection.js';
import { transcriptEntries, turnEntryType, type Transcript, type TranscriptOwner } from './product-transcript.js';
import { backupSkillFiles, BotSkills, bundleDigest, canonicalSkillOrigin, checkedSkillFiles, formatSkill, framedSkillDigest, pureSkill, skillGrantKey,
  type SkillBundle, type SkillWrite } from './bot-skills.js';
import { containsMcpSecret, maxMcpServers, McpConnectionManager, uniqueMcpRegistry, validMcpBindings, validMcpServer } from './mcp-broker.js';
import { McpVault, mcpSecretKeys, mcpSecretsComplete, mergeMcpSecrets, type McpVaultWrite } from './mcp-vault.js';
import { projectionRedactor } from './product-redactor.js';
import { loadSkillInstallation, SkillSandbox } from './skill-sandbox.js';
import { activityRole, activityRouting, providerCacheTtlMs, providerEndpoint, resolveRole, resolvedRoles, validProvider,
  type ActivityRouting, type CapturedRoute, type ModelRole, type ProviderAccount, type RoutingPolicy } from './model-router.js';

const emptyDraft = (): Draft => ({ revision: 0, text: '', materials: [] });
const newId = randomUUID;
const live = (execution?: StoredExecution) => execution?.state === 'starting' || execution?.state === 'running';
const unknown = (execution?: StoredExecution) => execution?.state === 'unknown';
const maxDraftBytes = 512 * 1024;
const maxPartialBytes = 512 * 1024;
const readOnlyTools: Record<string, true> = { read: true, grep: true, find: true, ls: true, activity_list: true, activity_get: true,
  activity_trace_read: true, conversation_read: true, conversation_search: true, memory_list_rules: true, memory_search_log: true,
  skill_list: true, skill_read: true, bot_list: true, bot_inspect_team: true };
const providerChanges = { 'provider.save': 'saved', 'provider.delete': 'deleted', 'provider.apply': 'applied',
  'provider.revokeCredential': 'revoked', 'routing.setPolicy': 'policy' } as const;
type ProviderMutation = Extract<ProductCommand, { method: keyof typeof providerChanges }>;

export type ProductRunObservers = {
  started: () => void;
  text: (text: string) => void;
  settled: (state: 'completed' | 'failed' | 'interrupted', text: string, error?: string) => void;
  confirmedFileEffect: (path: string, callId: string) => void;
  toolStarted: (name: string, callId: string, target?: string) => void;
  toolFinished: (name: string, callId: string, isError: boolean, excerpt?: string) => void;
  authorizeProvider?: () => void;
  credentialRejected?: () => void;
  admitTool?: (name: string, callId: string) => void;
};

export type ProductSession = {
  sessionRef: string;
  start: (executionId: string, draft: Draft, observers: ProductRunObservers) => void;
  stop: (executionId: string) => void;
};

export type ProductEngine = {
  open: (params: OpenParams, tools: ToolDefinition[]) => Promise<ProductSession>;
  refreshSession: (params: OpenParams, tools: ToolDefinition[]) => Promise<ProductSession>;
  conversationPreview: (sessionRef: string, contextId: string, botId: string) => Promise<{ preview?: string; timestamp?: string }>;
  readTranscript: (sessionRef: string, owner: TranscriptOwner) => Promise<Transcript>;
  redactor: (keys: string[]) => (text: string) => string;
  streamingRedactor: (keys: string[]) => (text: string, final?: boolean) => string;
  test: (configuration: Connection) => Promise<void>;
  drain: () => Promise<void>;
  disconnect: () => void;
  cleanMessage: (error: unknown) => string;
};

export function confirmedFileEffect(cwd: string, name: string, input: unknown, content: unknown): string | undefined {
  if (name !== 'write' && name !== 'edit' || !input || typeof input !== 'object' || !('path' in input) ||
      typeof input.path !== 'string' || !input.path || input.path.includes('\0') || !Array.isArray(content) || content.length !== 1) return;
  const receipt: unknown = content[0];
  if (!receipt || typeof receipt !== 'object' || !('type' in receipt) || receipt.type !== 'text' || !('text' in receipt) || typeof receipt.text !== 'string') return;
  const requested = input.path;
  if (name === 'write') {
    if (receipt.text !== `Successfully wrote to ${requested}`) return;
  } else {
    const prefix = 'Successfully replaced ';
    const suffix = ` block(s) in ${requested}.`;
    if (!receipt.text.startsWith(prefix) || !receipt.text.endsWith(suffix) || !/^[1-9][0-9]*$/.test(receipt.text.slice(prefix.length, -suffix.length))) return;
  }
  const path = requested.replace(/^@/, '').replace(/[\u00a0\u2000-\u200a\u202f\u205f\u3000]/g, ' ');
  if (!path || path === '~' || path.startsWith('~/') || path.startsWith('file://')) return;
  return resolve(cwd, path);
}

type SessionBinding = {
  contextId: string; sessionRef: string; connectionRevision?: number; route?: CapturedRoute; profileRevision: number;
  cwd: string; instructions: string; codingTools: string[] | null; used: boolean;
  memoryRulesRevision?: number;
  lastTurnAt?: string;
};
type StoredExecution = Execution & {
  operationId: string;
  input: Draft;
  error?: string;
  engineRunId?: string;
  importedActivityId?: string;
  retained?: object;
};
type Conversation = {
  conversationId: string; botId: string; revision: number; draft: Draft; binding?: SessionBinding;
  latestExecutionId?: string;
};
type Bot = { botId: string; profile: BotProfile; profileRevision: number; archived: boolean; currentConversationId: string;
  systemKind?: BotSystemKind; retained?: object };
type Question = {
  questionId: string; executionId: string; text: string; revision: number;
  state: 'open' | 'answered' | 'superseded';
  answer?: { text: string; materials: Material[] };
  delivery?: 'saved' | 'dispatching' | 'delivered' | 'unknown' | 'canceled' | 'unavailable';
  deliveryBlocker?: 'credential_unavailable';
};
type ResultFile = { path: string; callId: string };
type ActivityResult = {
  activityId: string; executionId: string; text: string; delivery: 'available'; files: ResultFile[];
};
type Activity = {
  activityId: string; ownerBotId: string; originConversationId: string; revision: number;
  state: ActivityState; goal: string; requirements: string[]; questions: Question[];
  materials: Material[]; inputFiles: string[]; executionIds: string[]; results: ActivityResult[]; binding?: SessionBinding;
  grantedLinkedActivityIds?: string[];
  retained?: object;
  routing?: ActivityRouting;
};
type StoredOperation = {
  method: string; fingerprint: string; receipt: Operation; reply: object;
  replyError?: { code: string; message: string; details?: object };
  connectionRevision?: number;
  retained?: object;
};
export type ProductProfile = {
  version: 1;
  bots: Bot[];
  conversations: Conversation[];
  activities: Activity[];
  executions: StoredExecution[];
  operations: StoredOperation[];
  connections: StoredConnection[];
  savedRevision: number | null;
  appliedRevision: number | null;
  preferences?: { selectedBotId?: string; layout?: object; themeIds?: object; appearance?: string; hideBuiltinArchitect?: boolean; discoverLocalSkills?: boolean };
  issueToolScopes?: object[];
  untargetedReceipts?: object[];
  recovery?: { id: string; ui?: object };
  memory?: BotMemory;
  mcpServers?: McpServerRecord[];
  providerAccounts?: ProviderAccount[];
  providerEffectiveAccounts?: ProviderAccount[];
  routingPolicy?: RoutingPolicy;
  providerEffectivePolicy?: RoutingPolicy;
  providerSavedRevision?: number;
  providerEffectiveRevision?: number;
};
type ProductActor = { conversationId: string } | { activityId: string };
type LiveExecution = {
  owner: ProductActor;
  session: ProductSession;
  offset: number;
  partial: string | null;
  files: ResultFile[];
  learningToolUsed?: boolean;
};
type AnswerWaiter = { resolve: (answer: { text: string; materials: Material[] }) => void };

function fingerprint(parameters: object): string {
  return createHash('sha256').update(canonicalJSON(parameters)).digest('hex');
}

function stateError(code: string, text: string): never {
  throw new RequestError(code, text);
}

function requireRevision(actual: number, expected: number): void {
  if (actual !== expected) throw new RequestError('stale_revision', 'The saved revision has changed', undefined, { currentRevision: actual });
}

function page<T>(items: T[], key: (item: T) => string, owner: string, cursor?: string, limit = 25) {
  const prefix = Buffer.from(owner).toString('base64url') + '.';
  let start = 0;
  if (cursor) {
    if (!cursor.startsWith(prefix)) stateError('invalid_cursor', 'The cursor belongs to a different query');
    start = items.findIndex(item => key(item) === cursor.slice(prefix.length)) + 1;
    if (!start) stateError('invalid_cursor', 'The cursor is no longer available');
  }
  const end = Math.min(items.length, start + limit);
  return { items: items.slice(start, end), nextCursor: end < items.length ? prefix + key(items[end - 1]) : null };
}

function validateDraft(draft: Draft): void {
  if (!Value.Check(draftSchema, draft) || frameBytes(draft) > maxDraftBytes) stateError('invalid_params', 'The draft exceeds the size limit for saved text and Materials');
  if (draft.quote && !hasCanonicalQuoteText(draft.quote)) stateError('invalid_params', 'The quote exceeds the size limit for its author or text');
  if (new Set(draft.materials.map(item => item.id)).size !== draft.materials.length) stateError('invalid_params', 'Material IDs must be unique');
}

function publicExecution(execution: StoredExecution): Execution {
  return { executionId: execution.executionId, revision: execution.revision, state: execution.state };
}

function nonsecret(configuration: Connection) {
  const { apiKey, ...publicFields } = configuration;
  return { ...publicFields, credentialPresent: Boolean(apiKey) };
}

type ToolTextResult = { content: { type: 'text'; text: string }[]; details: Record<string, never>; isError: boolean };

const strictTool = { additionalProperties: false };
const modelInspectionPage = { cursor: inspectionPage.cursor, limit: inspectionPage.limit, max_bytes: inspectionPage.maxBytes };
const traceToolParams = Type.Object({ activity_id: id, execution_id: Type.Optional(id), ...modelInspectionPage }, strictTool);
const readToolParams = Type.Object({ conversation_id: id, ...modelInspectionPage, tail: Type.Optional(Type.Boolean()) }, strictTool);
const searchToolParams = Type.Object({ conversation_id: id, ...modelInspectionPage, query: Type.String() }, strictTool);
const delegateInput = Type.Object({ goal: id, key: id,
  assignee_id: Type.Optional(Type.String()), requirements: Type.Optional(Type.Array(Type.String())),
  files: Type.Optional(Type.Array(Type.String(), { maxItems: 16 })), linked_activity_ids: Type.Optional(Type.Array(id)),
  ...activityRouting.properties }, strictTool);

function projectionJSON(value: unknown, redact: (text: string) => string, key = ''): unknown {
  if (typeof value === 'string') {
    if (['code', 'delivery', 'state', 'system_kind', 'systemKind'].includes(key)) return value;
    const clean = redact(value);
    if (clean !== value && /(?:^id$|Id$|Ids$|_id$|_ids$)/.test(key)) return oversizedEntry();
    return clean;
  }
  if (Array.isArray(value)) return value.map(item => projectionJSON(item, redact, key));
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([field, item]) => [field, projectionJSON(item, redact, field)]));
  return value;
}

function toolText(value: unknown, isError = false): ToolTextResult {
  return { content: [{ type: 'text' as const, text: typeof value === 'string' ? value : JSON.stringify(value) }], details: {}, isError };
}

const architectProfile: BotProfile = {
  name: 'PandoraBox', role: 'Bot Architect', purpose: 'Design, create, and refine focused Nagi Bots through conversation.',
  instructions: '', workspace: '', codingTools: [],
  starterPrompts: ['Create a writing and editing Bot', 'Build a strict code review Bot', 'Audit and refine my existing Bot Team'],
};

const requiredText = Type.String({ minLength: 1, pattern: '\\S' });
const architectInspect = Type.Object({}, { additionalProperties: false });
const architectCapabilities = { tool_preset: Type.Optional(toolPresetSchema), skill_ids: Type.Optional(Type.Array(skillID, { maxItems: 200 })) };
const architectCreate = Type.Object({ name: requiredText, purpose: requiredText, job: requiredText, voice: requiredText,
  anti_jobs: Type.Array(requiredText, { minItems: 1 }), workflow: Type.Optional(Type.String()), role: Type.Optional(Type.String()),
  starter_prompts: Type.Optional(Type.Array(Type.String())),
  ...architectCapabilities,
}, { additionalProperties: false });
const architectUpdate = Type.Object({ bot_id: requiredText, name: Type.Optional(requiredText), purpose: Type.Optional(Type.String()),
  role: Type.Optional(Type.String()), job: Type.Optional(requiredText), voice: Type.Optional(requiredText),
  anti_jobs: Type.Optional(Type.Array(requiredText)), workflow: Type.Optional(Type.String()), instructions: Type.Optional(Type.String()),
  starter_prompts: Type.Optional(Type.Array(Type.String())),
  ...architectCapabilities,
}, { additionalProperties: false });

function architectResult(bot: Bot) {
  return { bot_id: bot.botId, name: bot.profile.name, purpose: bot.profile.purpose, profile_revision: bot.profileRevision };
}

export class Product {
  private readonly storage: ProductSQLite;
  private readonly skills: BotSkills;
  private readonly sandbox: SkillSandbox;
  private readonly installationId: string;
  private readonly vault: McpVault;
  private readonly mcp = new McpConnectionManager();
  private readonly skillReload = new Set<string>();
  private readonly backgroundTasks = new Set<Promise<void>>();
  private storageClosed = false;
  private profile: ProductProfile;
  private credentials: PrivateConnection | undefined;
  private readonly retiredKeys = new Set<string>();
  private readonly rejectedSources = new Set<string>();
  private stream?: { streamId: string; seq: number };
  private effectiveRevision: number | null;
  private closing = false;
  private readonly active = new Map<string, LiveExecution>();
  private readonly sessions = new Map<string, { session: ProductSession; instructions: string; mcpSignature: string }>();
  private readonly waiters = new Map<string, AnswerWaiter>();
  private readonly pendingTraceUpdates = new Map<string, TraceUpdate>();
  private traceUpdateTimer?: ReturnType<typeof globalThis.setImmediate>;

  constructor(private paths: ProductProfilePaths, private engine: ProductEngine, private sendFrame: (frame: object) => Promise<void>, private stateDir: string) {
    this.skills = new BotSkills(dirname(paths.profilePath));
    this.vault = new McpVault(paths.credentialsPath);
    this.storage = new ProductSQLite(paths.profilePath, paths.credentialsPath, this.skills, this.vault);
    try {
      this.credentials = this.storage.credentials();
      this.installationId = loadSkillInstallation(dirname(paths.profilePath));
      this.sandbox = new SkillSandbox([dirname(paths.profilePath), dirname(paths.credentialsPath), stateDir]);
      this.profile = this.storage.open(paths, this.credentials, this.installationId);
      this.effectiveRevision = this.profile.appliedRevision;
      this.recoverInterruptedWork();
    } catch (error) {
      this.close();
      throw error;
    }
  }

  private recoverInterruptedWork(): void {
    if (this.profile.executions.some(live) || this.profile.activities.some(activity => activity.questions.some(question => question.delivery === 'dispatching'))) {
      this.commit(candidate => {
        for (const execution of candidate.executions.filter(live)) {
          execution.state = 'unknown';
          execution.revision++;
        }
        for (const activity of candidate.activities) {
          let changed = false;
          const last = this.execution(candidate, activity.executionIds.at(-1));
          if (last?.state === 'unknown' && (activity.state === 'running' || activity.state === 'waitingForAnswer')) {
            activity.state = 'needsAttention';
            changed = true;
          }
          for (const question of activity.questions) {
            if (question.delivery === 'dispatching') {
              question.delivery = 'unknown';
              question.revision++;
              changed = true;
            }
          }
          if (changed) activity.revision++;
        }
      });
    }
  }

  close(): void {
    this.closing = true;
    this.flushTraceUpdates();
    if (!this.storageClosed) {
      this.storage.close();
      this.storageClosed = true;
    }
  }

  async drainChildren(): Promise<void> { await Promise.all([this.mcp.shutdown(), this.sandbox.shutdown()]); }

  killChildrenOnExit(): void { this.mcp.killOnExit(); this.sandbox.killOnExit(); }

  private commit<T>(mutate: (candidate: ProductProfile) => T, skillWrite?: SkillWrite, mcpWrite?: McpVaultWrite, approval?: SkillApprovalChange,
    routing?: RoutingWrite): T {
    const candidate = structuredClone(this.profile);
    const result = mutate(candidate);
    let updates: TraceUpdate[];
    try { updates = this.storage.write(candidate, this.profile, skillWrite, mcpWrite, approval, routing); }
    catch (error) {
      if (routing?.credentials) {
        this.closing = true;
        this.engine.disconnect();
      }
      throw error;
    }
    this.profile = candidate;
    for (const update of updates) this.queueTraceUpdate(update);
    return result;
  }

  private bot(profile: ProductProfile, botId: string): Bot {
    return profile.bots.find(bot => bot.botId === botId) ?? stateError('not_found', 'The Bot does not exist');
  }

  private conversation(profile: ProductProfile, conversationId: string): Conversation {
    return profile.conversations.find(item => item.conversationId === conversationId) ?? stateError('not_found', 'The conversation does not exist');
  }

  private current(profile: ProductProfile, conversationId: string): Conversation {
    const conversation = this.conversation(profile, conversationId);
    const bot = this.bot(profile, conversation.botId);
    if (bot.currentConversationId !== conversationId || bot.archived) stateError('conversation_read_only', 'This conversation is read-only');
    return conversation;
  }

  private activity(profile: ProductProfile, activityId: string): Activity {
    return profile.activities.find(activity => activity.activityId === activityId) ?? stateError('not_found', 'The activity does not exist');
  }

  private execution(profile: ProductProfile, executionId?: string): StoredExecution | undefined {
    return executionId ? profile.executions.find(execution => execution.executionId === executionId) : undefined;
  }

  private question(activity: Activity, questionId: string): Question {
    return activity.questions.find(question => question.questionId === questionId) ?? stateError('not_found', 'The question does not exist');
  }

  private connection(profile: ProductProfile, revision: number | null) {
    return profile.connections.find(item => item.revision === revision);
  }

  private providerKey(binding: Pick<CapturedRoute, 'providerId' | 'credentialSourceId' | 'baseUrl' | 'wireApi'>): string | undefined {
    if (this.rejectedSources.has(binding.credentialSourceId)) return;
    return this.credentials?.accounts?.find(row => row.credentialSourceId === binding.credentialSourceId &&
      row.providerId === binding.providerId && row.baseUrl === binding.baseUrl && row.wireApi === binding.wireApi)?.apiKey;
  }

  private bindingKey(binding: SessionBinding): string | undefined {
    if (binding.route) return this.providerKey(binding.route);
    const connection = this.connection(this.profile, binding.connectionRevision ?? null);
    return connection && !this.rejectedSources.has(connection.sourceId) ? this.credentials?.sources[connection.sourceId]?.api_key : undefined;
  }

  private providerRevisions(profile = this.profile) {
    return { savedRevision: profile.providerSavedRevision ?? null, effectiveRevision: profile.providerEffectiveRevision ?? null };
  }

  private routingState() {
    const { providerEffectiveAccounts, providerEffectiveRevision, providerEffectivePolicy, routingPolicy } = this.profile;
    const effectivePolicy = providerEffectivePolicy ?? 'balanced';
    return { ...this.providerRevisions(), policy: routingPolicy ?? 'balanced', effectivePolicy,
      resolvedRoles: resolvedRoles(providerEffectiveAccounts ?? [], providerEffectiveRevision ?? 0, effectivePolicy, account => !!this.providerKey(account)) };
  }

  private changeProvider(command: ProviderMutation) {
    const { method, params } = command;
    const { operationId } = params;
    const visible = command.method === 'provider.save' ? { operationId, account: command.params.account } : params;
    const prior = this.sameOperation(method, operationId, visible);
    if (prior) return prior.reply;
    const next = { ...this.credentials, version: 3 as const, sources: this.credentials?.sources ?? {}, accounts: this.credentials?.accounts ?? [] };
    const routing: RoutingWrite = {};
    const reply = this.commit(candidate => {
      switch (command.method) {
        case 'provider.save': {
          const { account, apiKey } = command.params;
          const previous = candidate.providerAccounts?.find(row => row.providerId === account.providerId);
          const sameEndpoint = previous?.wireApi === account.wireApi && previous.baseUrl === account.baseUrl;
          const existingKey = sameEndpoint ? this.providerKey(previous) : undefined;
          const key = apiKey?.trim() ? apiKey : existingKey;
          if (!validProvider(account) || containsMcpSecret(account, projectionRedactor([...this.credentialKeys(), ...(key ? [key] : [])]))) {
            stateError('invalid_params', 'The Provider requires an HTTP(S) endpoint, nonsecret metadata and valid model limits');
          }
          if (!previous && (candidate.providerAccounts?.length ?? 0) >= 64) stateError('busy', 'The Provider pool is full');
          const credentialSourceId = sameEndpoint && key === existingKey ? previous.credentialSourceId : newId();
          const row = { ...account, credentialSourceId };
          candidate.providerAccounts = previous ? candidate.providerAccounts!.map(item => item.providerId === account.providerId ? row : item) :
            [...candidate.providerAccounts ?? [], row];
          if (key) {
            next.accounts = [...next.accounts.filter(item => item.credentialSourceId !== credentialSourceId), { ...row, apiKey: key }];
            routing.credentials = next;
          }
          break;
        }
        case 'provider.delete': {
          const accounts = candidate.providerAccounts?.filter(row => row.providerId !== command.params.providerId);
          if (!candidate.providerAccounts || accounts?.length === candidate.providerAccounts.length) stateError('not_found', 'The Provider does not exist');
          candidate.providerAccounts = accounts?.length ? accounts : undefined;
          break;
        }
        case 'routing.setPolicy':
          candidate.routingPolicy = command.params.policy;
          break;
        case 'provider.apply': {
          const saved = candidate.providerSavedRevision;
          requireRevision(saved ?? 0, command.params.savedRevision);
          if (saved === undefined) stateError('not_found', 'There is no saved Provider revision');
          candidate.providerEffectiveRevision = saved;
          candidate.providerEffectivePolicy = candidate.routingPolicy ?? 'balanced';
          candidate.providerEffectiveAccounts = candidate.providerAccounts;
          const savedIds = new Set(candidate.providerAccounts?.map(row => row.providerId));
          next.accounts = next.accounts.filter(row => savedIds.has(row.providerId));
          break;
        }
        case 'provider.revokeCredential': {
          const { providerId } = command.params;
          if (![...candidate.providerAccounts ?? [], ...candidate.providerEffectiveAccounts ?? []].some(row => row.providerId === providerId)) {
            stateError('not_found', 'The Provider does not exist');
          }
          next.accounts = next.accounts.filter(row => row.providerId !== providerId);
          break;
        }
      }
      if (method === 'provider.apply' || method === 'provider.revokeCredential') {
        if (next.accounts.length !== (this.credentials?.accounts?.length ?? 0)) routing.credentials = next;
      } else {
        candidate.routingPolicy ??= 'balanced';
        candidate.providerSavedRevision = (candidate.providerSavedRevision ?? 0) + 1;
      }
      const result = this.providerRevisions(candidate);
      return this.record(candidate, method, visible, { operationId, state: 'accepted', target: { connection: true }, result }, result);
    }, undefined, undefined, undefined, routing);
    if (routing.credentials) {
      const retained = new Set(privateCredentialKeys(next));
      for (const key of privateCredentialKeys(this.credentials)) if (!retained.has(key)) this.retiredKeys.add(key);
      this.credentials = next;
    }
    this.emit('provider.changed', { ...this.providerRevisions(), change: providerChanges[method] });
    return reply;
  }

  private async testProvider({ account, apiKey }: ProductParams<'provider.test'>) {
    if (!validProvider(account)) stateError('invalid_params', 'The Provider requires an HTTP(S) endpoint and valid model limits');
    const previous = this.profile.providerAccounts?.find(row => row.providerId === account.providerId);
    const key = apiKey?.trim() ? apiKey : previous?.baseUrl === account.baseUrl && previous.wireApi === account.wireApi ? this.providerKey(previous) : undefined;
    if (!key) stateError('credential_unavailable', 'The Provider credential is unavailable');
    const started = Date.now();
    try {
      const endpoint = providerEndpoint(account.baseUrl)!;
      endpoint.pathname = endpoint.pathname.replace(/\/$/, '') + '/models';
      const response = await fetch(endpoint, { redirect: 'manual', signal: AbortSignal.timeout(5000), headers: { Authorization: `Bearer ${key}` } });
      if (response.ok && response.body) {
        const reader = response.body.getReader();
        const chunks: Uint8Array[] = [];
        let size = 0;
        while (true) {
          const { value, done } = await reader.read();
          if (done) break;
          size += value.byteLength;
          if (size > 1024 * 1024) {
            await reader.cancel();
            stateError('provider_test_failed', 'The Provider model catalog exceeds the probe limit');
          }
          chunks.push(value);
        }
        const catalog: unknown = JSON.parse(Buffer.concat(chunks, size).toString('utf8'));
        if (!catalog || typeof catalog !== 'object' || !('data' in catalog) || !Array.isArray(catalog.data)) stateError('provider_test_failed', 'The Provider model catalog is invalid');
        const redact = this.engine.redactor([...this.credentialKeys(), key]);
        const models = [...new Set(catalog.data.map(row => row?.id).filter((model): model is string =>
          typeof model === 'string' && model.length <= 256 && !!model.trim() && redact(model) === model))].slice(0, 128);
        return { ok: true, models, latencyMs: Date.now() - started };
      }
      await response.body?.cancel();
      if (response.status !== 404 && response.status !== 405 && response.status !== 501) stateError('provider_test_failed', `The Provider rejected the probe (HTTP ${response.status})`);
      await this.engine.test({ ...account, model: account.defaultModel, apiKey: key, workingDirectory: this.stateDir, maxActiveTasks: 1 });
      return { ok: true, models: [], latencyMs: Date.now() - started };
    } catch (error) {
      const redact = this.engine.redactor([...this.credentialKeys(), key]);
      return { ok: false, models: [], latencyMs: Date.now() - started,
        error: error instanceof RequestError ? redact(error.message) : 'The Provider probe could not be completed' };
    }
  }

  private selectRoute(role: ModelRole, deep = false): CapturedRoute | null {
    return resolveRole(this.profile.providerEffectiveAccounts ?? [], this.profile.providerEffectiveRevision ?? 0,
      this.profile.providerEffectivePolicy ?? 'balanced', role, account => !!this.providerKey(account), deep);
  }

  private connectionState(profile = this.profile) {
    const saved = this.connection(profile, profile.savedRevision);
    const effective = this.connection(profile, this.effectiveRevision);
    return {
      savedRevision: profile.savedRevision,
      effectiveRevision: this.effectiveRevision,
      saved: saved ? { ...saved.configuration, credentialPresent: Boolean(this.credentials?.sources[saved.sourceId]?.api_key) } : null,
      effective: effective ? { ...effective.configuration, credentialPresent: Boolean(this.credentials?.sources[effective.sourceId]?.api_key) } : null,
    };
  }

  private viewExecution(execution?: StoredExecution) {
    if (!execution) return null;
    const transient = this.active.get(execution.executionId);
    return {
      ...publicExecution(execution), operationId: execution.operationId,
      ...(execution.error ? { error: execution.error } : {}),
      ...(transient ? { partial: transient.partial, textOffset: transient.offset } : {}),
    };
  }

  private conversationView(conversation: Conversation) {
    const bot = this.bot(this.profile, conversation.botId);
    const current = bot.currentConversationId === conversation.conversationId && !bot.archived;
    return {
      conversationId: conversation.conversationId, botId: conversation.botId, mode: current ? 'current' : 'readOnly',
      revision: conversation.revision, draft: current ? conversation.draft : null,
      latestExecution: this.viewExecution(this.execution(this.profile, conversation.latestExecutionId)),
    };
  }

  private activityView(activity: Activity) {
    return {
      activityId: activity.activityId, ownerBotId: activity.ownerBotId, originConversationId: activity.originConversationId,
      revision: activity.revision, state: activity.state, goal: activity.goal,
      openQuestionIds: activity.questions.filter(question => question.state === 'open').map(question => question.questionId),
    };
  }

  private activityDetails(activity: Activity) {
    return {
      ...this.activityView(activity), ...activity.routing, requirements: activity.requirements, materials: activity.materials, questions: activity.questions,
      executions: activity.executionIds.map(id => this.viewExecution(this.execution(this.profile, id))),
    };
  }

  private visibleActivities(botId: string) {
    this.bot(this.profile, botId);
    return this.profile.activities.filter(activity => directlyOwns(activity, botId, this.conversation(this.profile, activity.originConversationId).botId));
  }

  private operation(operationId: string) {
    const saved = this.profile.operations.find(item => item.receipt.operationId === operationId);
    if (!saved) stateError('not_found', 'The operation receipt was not found');
    const execution = this.execution(this.profile, saved.receipt.execution?.executionId);
    return execution ? { ...saved.receipt, execution: publicExecution(execution) } : saved.receipt;
  }

  private sameOperation(method: string, operationId: string, parameters: object, apiKey?: string): StoredOperation | undefined {
    const prior = this.profile.operations.find(item => item.receipt.operationId === operationId);
    if (!prior) return;
    const sourceId = this.connection(this.profile, prior.connectionRevision ?? null)?.sourceId;
    if (prior.method !== method || prior.fingerprint !== fingerprint(parameters) ||
        (apiKey !== undefined && (!sourceId || this.credentials?.sources[sourceId]?.api_key !== apiKey))) {
      stateError('operation_conflict', 'The operation ID belongs to different input');
    }
    if (prior.replyError) throw new RequestError(prior.replyError.code, prior.replyError.message, undefined, prior.replyError.details);
    return prior;
  }

  private record(candidate: ProductProfile, method: string, parameters: object, receipt: Operation, reply: object, connectionRevision?: number) {
    candidate.operations.push({ method, fingerprint: fingerprint(parameters), receipt, reply, connectionRevision });
    return reply;
  }

  private change(method: string, params: { operationId: string }, target: Operation['target'], mutate: (candidate: ProductProfile) => Record<string, unknown>) {
    const prior = this.sameOperation(method, params.operationId, params);
    if (prior) return prior.reply;
    return this.commit(candidate => {
      const result = mutate(candidate);
      return this.record(candidate, method, params, { operationId: params.operationId, target, state: 'accepted', result }, result);
    });
  }

  private changeBot<P extends { operationId: string }>(method: string, params: P, mutate: (candidate: ProductProfile) => { bot: Bot; change?: BotChange }): Bot {
    const prior = this.sameOperation(method, params.operationId, params);
    if (prior) return prior.reply as Bot;
    const before = this.profile;
    const { bot, change } = this.commit(candidate => {
      const result = mutate(candidate);
      const { retained, ...publicBot } = result.bot;
      this.record(candidate, method, params, { operationId: params.operationId, state: 'accepted', target: { botId: publicBot.botId }, result: publicBot }, publicBot);
      return { bot: publicBot, change: result.change };
    });
    const old = before.bots.find(row => row.botId === bot.botId);
    if (old && canonicalJSON(old.profile.mountedSkills) !== canonicalJSON(bot.profile.mountedSkills)) this.skillReload.add(bot.botId);
    if (change) this.emit('bot.changed', { bot, change });
    return bot;
  }

  private insertBot(candidate: ProductProfile, profile: BotProfile, systemKind?: BotSystemKind): Bot {
    const botId = systemKind ? PANDORABOX_BOT_ID : newId();
    const conversationId = newId();
    const bot: Bot = { botId, profile: this.validateProfile(profile), profileRevision: 1, archived: false, currentConversationId: conversationId };
    if (systemKind) {
      if (candidate.bots.some(existing => existing.botId === botId)) stateError('operation_conflict', 'A saved Bot already uses the reserved built-in ID');
      bot.systemKind = systemKind;
      candidate.bots.unshift(bot);
    } else {
      candidate.bots.push(bot);
    }
    candidate.conversations.push({ conversationId, botId, revision: 1, draft: emptyDraft() });
    return bot;
  }

  private updateBotProfile(candidate: ProductProfile, bot: Bot, profile: BotProfile, startNewConversation = false): void {
    if (bot.archived) stateError('conversation_read_only', 'Restore this Bot before editing');
    profile = this.validateProfile(profile, bot.profile.workspace);
    if (startNewConversation) {
      const old = this.conversation(candidate, bot.currentConversationId);
      const execution = this.execution(candidate, old.latestExecutionId);
      if (live(execution) || unknown(execution)) stateError('busy', 'The current conversation execution is unresolved');
      const conversationId = newId();
      candidate.conversations.push({ conversationId, botId: bot.botId, revision: 1, draft: old.draft });
      old.draft = emptyDraft();
      old.revision++;
      bot.currentConversationId = conversationId;
    }
    bot.profile = profile;
    bot.profileRevision++;
  }

  private emit(event: string, data: object): void {
    const stream = this.stream;
    if (!stream) return;
    void this.sendFrame({ v: 1, type: 'event', streamId: stream.streamId, seq: ++stream.seq, event, data }).catch(() => this.engine.disconnect());
  }

  private queueTraceUpdate(update: TraceUpdate): void {
    if (!this.stream) return;
    this.pendingTraceUpdates.set(update.executionId, update);
    this.traceUpdateTimer ??= globalThis.setImmediate(() => this.flushTraceUpdates());
  }

  private flushTraceUpdates(): void {
    if (this.traceUpdateTimer) clearImmediate(this.traceUpdateTimer);
    this.traceUpdateTimer = undefined;
    for (const update of this.pendingTraceUpdates.values()) this.emit('activity.traceUpdated', update);
    this.pendingTraceUpdates.clear();
  }

  private observeTrace(row: TraceObservation): void {
    const redact = this.engine.redactor(this.credentialKeys());
    if (redact(row.activityId) !== row.activityId || redact(row.executionId) !== row.executionId ||
        ('callId' in row && redact(row.callId) !== row.callId)) return;
    const update = this.storage.appendTrace(redactTrace(row, redact));
    if (update) this.queueTraceUpdate(update);
  }

  async dispatch(command: ProductCommand, respond: (result: object) => Promise<void>): Promise<void> {
    const method = command.method;
    if (this.closing) stateError('closing', 'The app is shutting down');
    if (method === 'app.open') {
      if (!command.params.supportedVersions.includes(1)) stateError('unsupported_version', 'This worker supports product protocol v1');
      this.flushTraceUpdates();
      const stream = { streamId: newId(), seq: 0 };
      const bots = this.listBots();
      this.stream = stream;
      await respond({ version: 1, streamId: stream.streamId, eventSeq: stream.seq, snapshot: {
        bots: bots.items, nextBotCursor: bots.nextCursor, connection: this.connectionState(),
        ...(this.profile.preferences ? { preferences: this.profile.preferences } : {}),
        ...(this.profile.recovery ? { recovery: this.profile.recovery } : {}),
      } });
      return;
    }
    if (!this.stream) stateError('not_open', 'Open the app protocol first');
    if (method === 'app.shutdown') {
      this.closing = true;
      await this.engine.drain();
      this.close();
      await respond({ drained: true });
      return;
    }
    if (method === 'connection.test') {
      await this.engine.test(this.resolveCredential(command.params.configuration));
      await respond({ verified: true });
      return;
    }
    const result = await this.command(command);
    await respond(result);
  }

  private async command(command: Exclude<ProductCommand, { method: 'app.open' | 'app.shutdown' | 'connection.test' }>): Promise<object> {
    const method = command.method;
    switch (method) {
      case 'bot.list':
        return this.listBots(command.params.cursor, command.params.limit);
      case 'bot.ensureBuiltin': {
        const params = command.params;
        return this.changeBot(method, params, candidate => {
          const existing = candidate.bots.find(bot => bot.systemKind === params.kind);
          return existing ? { bot: existing } : { bot: this.insertBot(candidate, architectProfile, params.kind), change: 'created' };
        });
      }
      case 'bot.create':
        return this.changeBot(method, command.params, candidate => ({ bot: this.insertBot(candidate, command.params.profile), change: 'created' }));
      case 'bot.update': {
        const params = command.params;
        return this.changeBot(method, params, candidate => {
          const bot = this.bot(candidate, params.botId);
          requireRevision(bot.profileRevision, params.expectedRevision);
          this.updateBotProfile(candidate, bot, params.profile, params.startNewConversation);
          return { bot, change: 'updated' };
        });
      }
      case 'bot.archive':
      case 'bot.restore': {
        const params = command.params;
        return this.changeBot(method, params, candidate => {
          const bot = this.bot(candidate, params.botId);
          if (method === 'bot.archive' && bot.systemKind) stateError('scope_denied', 'Built-in system Bots cannot be archived');
          requireRevision(bot.profileRevision, params.expectedRevision);
          if (method === 'bot.archive') {
            const conversation = this.conversation(candidate, bot.currentConversationId);
            const related = [conversation.latestExecutionId, ...candidate.activities.filter(item => item.ownerBotId === bot.botId).map(item => item.executionIds.at(-1))];
            if (related.some(id => live(this.execution(candidate, id)) || unknown(this.execution(candidate, id)))) stateError('busy', 'The Bot has unresolved work');
          }
          bot.archived = method === 'bot.archive';
          bot.profileRevision++;
          return { bot, change: bot.archived ? 'archived' : 'restored' };
        });
      }
      case 'memory.get': {
        const { botId } = command.params;
        this.bot(this.profile, botId);
        return { botId, rulesRevision: this.memoryStatus(this.profile, botId).rulesRevision, rules: this.lastingRules(botId),
          pendingLearnings: this.profile.memory?.pendingLearnings.filter(item => item.botId === botId && item.state === 'pending') ?? [],
          workLogs: this.storage.searchWorkLogs(botId, '', undefined, undefined, MAX_WORK_LOGS) };
      }
      case 'memory.searchLogs':
        return { items: this.searchWorkLogs(command.params) };
      case 'memory.addRule': {
        const { text } = command.params;
        return this.changeMemory(method, command.params, (memory, bot, now) => { this.saveRule(memory, bot, text, 'manual', now); });
      }
      case 'memory.updateRule': {
        const { ruleId, text } = command.params;
        return this.changeMemory(method, command.params, (memory, bot, now) => {
          const rule = this.ownedMemoryItem(memory.rules, bot.botId, 'ruleId', ruleId);
          this.saveRule(memory, bot, text, rule.source, now, rule);
        });
      }
      case 'memory.deleteRule': {
        const { ruleId } = command.params;
        return this.changeMemory(method, command.params, (memory, bot) => this.removeRule(memory, bot.botId, ruleId, true));
      }
      case 'memory.deleteLog': {
        const { logId } = command.params;
        return this.changeMemory(method, command.params, (memory, bot) => {
          const row = this.ownedMemoryItem(memory.workLogs, bot.botId, 'logId', logId);
          memory.workLogs.splice(memory.workLogs.indexOf(row), 1);
        });
      }
      case 'memory.reviewLearning': {
        const { learningId, decision, text } = command.params;
        return this.changeMemory(method, command.params, (memory, bot, now) => {
          const row = this.ownedMemoryItem(memory.pendingLearnings, bot.botId, 'learningId', learningId);
          if (row.state !== 'pending') stateError('memory_reviewed', 'This learning has already been reviewed');
          row.reviewedAt = now;
          row.state = decision === 'approve' ? 'approved' : 'rejected';
          if (decision === 'approve') {
            const approved = text ?? row.text;
            row.text = row.kind === 'rule' ? this.saveRule(memory, bot, approved, 'approved', now).text :
              this.saveWorkLog(memory, bot, approved, now, [], row.executionId).summary;
          }
        });
      }
      case 'conversation.list': {
        const { botId, cursor, limit } = command.params;
        const bot = this.bot(this.profile, botId);
        const items = this.profile.conversations.filter(item => item.botId === botId);
        const result = page(items, item => item.conversationId, `conversations:${botId}`, cursor, limit);
        const summaries = [];
        for (const item of result.items) {
          const preview = item.binding?.used
            ? await this.engine.conversationPreview(item.binding.sessionRef, item.binding.contextId, botId)
            : {};
          summaries.push({
            conversationId: item.conversationId, botId, revision: item.revision,
            mode: item.conversationId === bot.currentConversationId && !bot.archived ? 'current' : 'readOnly',
            ...preview,
          });
        }
        return { items: summaries, nextCursor: result.nextCursor };
      }
      case 'conversation.get':
        return this.conversationView(this.conversation(this.profile, command.params.conversationId));
      case 'conversation.saveDraft': {
        const { quote, ...fields } = command.params;
        const params = { ...fields, ...(quote ? { quote: { ...quote, authorName: quote.authorName.trim(), text: quote.text.trim() } } : {}) };
        return this.change(method, params, { conversationId: params.conversationId }, candidate => {
          const conversation = this.current(candidate, params.conversationId);
          requireRevision(conversation.draft.revision, params.expectedRevision);
          const draft: Draft = { revision: conversation.draft.revision + 1, text: params.text, materials: params.materials,
            ...(params.quote ? { quote: params.quote } : {}) };
          validateDraft(draft);
          conversation.draft = draft;
          conversation.revision++;
          return draft;
        });
      }
      case 'conversation.send':
        return this.sendConversation(command.params);
      case 'conversation.stop':
        return this.stop(method, command.params, { conversationId: command.params.conversationId });
      case 'conversation.history':
        return this.history(command.params.conversationId, command.params.cursor, command.params.limit);
      case 'activity.list': {
        const { botId, states, cursor, limit } = command.params;
        const items = this.visibleActivities(botId).filter(item => !states?.length || states.includes(item.state));
        return page(items.map(item => this.activityView(item)), item => item.activityId, canonicalJSON(['activities', botId, [...new Set(states ?? [])].sort()]), cursor, limit);
      }
      case 'activity.get':
        return this.activityDetails(this.activity(this.profile, command.params.activityId));
      case 'activity.trace':
        return this.readTrace(command.params);
      case 'activity.result': {
        const { activityId, executionId } = command.params;
        const activity = this.activity(this.profile, activityId);
        if (!activity.executionIds.includes(executionId)) stateError('not_found', 'The execution does not belong to this activity');
        const result = activity.results.find(result => result.executionId === executionId);
        return result ?? this.readImportedActivityResult(activity, executionId);
      }
      case 'activity.continue':
        return this.continueActivity(command.params);
      case 'activity.stop':
        return this.stop(method, command.params, { activityId: command.params.activityId });
      case 'question.answer':
        return this.answer(command.params);
      case 'connection.get':
        return this.connectionState();
      case 'connection.save':
        return this.saveConnection(command.params);
      case 'connection.apply': {
        const params = command.params;
        const prior = this.sameOperation(method, params.operationId, params);
        if (prior) return prior.reply;
        if (this.active.size) stateError('busy', 'Stop or finish active work before applying a connection');
        requireRevision(this.profile.savedRevision ?? 0, params.savedRevision);
        const saved = this.connection(this.profile, params.savedRevision);
        if (!saved) stateError('not_found', 'The saved connection does not exist');
        const directory = await stat(saved.configuration.workingDirectory).catch(() => undefined);
        if (!directory?.isDirectory()) stateError('working_directory_unavailable', 'Choose an available working directory before applying this connection');
        const reply = this.change(method, params, { connection: true }, candidate => {
          candidate.appliedRevision = params.savedRevision;
          return { savedRevision: candidate.savedRevision, effectiveRevision: candidate.appliedRevision };
        });
        this.effectiveRevision = params.savedRevision;
        return reply;
      }
      case 'routing.get':
        return this.routingState();
      case 'provider.list':
        return { ...this.routingState(), accounts: (this.profile.providerAccounts ?? []).map(account => ({ ...account,
          credentialStatus: this.providerKey(account) ? 'ok' : 'credential_missing' })) };
      case 'provider.test':
        return this.testProvider(command.params);
      case 'provider.save':
      case 'provider.delete':
      case 'provider.apply':
      case 'provider.revokeCredential':
      case 'routing.setPolicy':
        return this.changeProvider(command);
      case 'skill.list': {
        const owner = command.params.botId === undefined ? undefined : this.bot(this.profile, command.params.botId);
        return this.modelProjection(this.skillListing(owner));
      }
      case 'skill.get': {
        const { source, botId, skillId } = command.params;
        const owner = this.skillOwner(source, botId);
        const skill = this.skills.get({ source, botId, skillId }, this.skillWorkspace(owner), this.discoverLocalSkills);
        if (!skill) return stateError('skill_missing', 'The skill is missing');
        return { skill: this.modelProjection({ ...skill, ...this.skillReview(owner, skill, this.skillWorkspace(owner), true, true) }) };
      }
      case 'skill.save':
        return this.saveSkill(command.params);
      case 'skill.delete':
        return this.deleteSkill(command.params);
      case 'skill.approve':
        return this.approveSkill(command.params);
      case 'skill.revokeApproval': {
        const { botId, source, skillId, operationId } = command.params;
        this.bot(this.profile, botId);
        return this.changeApproval(method, command.params, { botId, source, skillId, change: 'revoked' }, { botId, source, skillId }, operationId);
      }
      case 'skill.promoteGlobal': {
        const params = command.params;
        const prior = this.sameOperation(method, params.operationId, params);
        if (prior) return prior.reply;
        const { botId, skillId, targetSkillId = skillId } = params;
        this.skillOwner('private', botId);
        const skill = this.skills.get({ source: 'private', botId, skillId }, '', false);
        if (!skill) return stateError('skill_missing', 'The private skill is missing');
        const redact = this.engine.redactor(this.credentialKeys());
        if ([skill.name, skill.description, skill.content].some(text => redact(text) !== text)) return stateError('skill_secret', 'Remove saved credentials before promoting the skill');
        const bundle = this.skills.bundle({ source: 'private', botId, skillId }, '', false);
        const files = bundle && Object.fromEntries(Object.entries(bundle.files).filter(([path]) => path !== 'SKILL.md'));
        const write = this.skills.plan('global', undefined, targetSkillId, redact, skill.markdown, true, files);
        return this.changeSkill(method, params, { source: 'global', botId, skillId: targetSkillId, change: 'promoted' }, write);
      }
      case 'skill.setPreferences': {
        const params = command.params;
        const prior = this.sameOperation(method, params.operationId, params);
        if (prior) return prior.reply;
        const changed: SkillChange = { source: 'global', skillId: '', change: 'preferences' };
        const result = this.change(method, params, { connection: true }, candidate => {
          candidate.preferences = { ...candidate.preferences, discoverLocalSkills: params.discoverLocalSkills };
          return changed;
        });
        this.emit('skill.changed', changed);
        return result;
      }
      case 'mcp.list': {
        const pools = this.mcp.status();
        return { servers: (this.profile.mcpServers ?? []).map(server => this.mcpView(server, pools)) };
      }
      case 'mcp.status':
        return { pools: this.mcp.status() };
      case 'mcp.save':
        return this.saveMcpServer(command.params);
      case 'mcp.delete':
        return this.deleteMcpServer(command.params);
      case 'mcp.test':
        return this.testMcpServer(command.params);
      case 'profile.backup': {
        if (this.active.size) stateError('busy', 'Finish or stop active work before saving a recovery copy');
        const redact = this.engine.redactor(this.credentialKeys());
        const { profile, backup } = this.storage.captureBackup(redact);
        redactBackupProfile(profile, redact);
        const bundles = this.skills.backup(profile.bots, redact);
        const files = new Map(bundles.map(row => [canonicalJSON([row.ownerBotId, row.skillId]), backupSkillFiles(row)]));
        const skillApprovals = this.storage.skillApprovals().filter(row => {
          if (row.source !== 'private') return true;
          const bundle = files.get(canonicalJSON([row.ownerBotId, row.skillId]));
          return bundle && bundleDigest(row.ownerBotId, row.source, row.skillId, bundle) === row.approvalDigest;
        });
        return backupProductProfile(this.paths, profile, { ...backup, ...(bundles.length ? { privateSkillBundles: bundles } : {}),
          ...(skillApprovals.length ? { skillApprovals } : {}) }, command.params.destination, snapshot => {
          const vaultKeys = this.vault.keys();
          if (vaultKeys.length && containsMcpSecret(snapshot, projectionRedactor(vaultKeys))) {
            stateError('mcp_secret', 'Remove the MCP credential from saved product data before making a recovery copy');
          }
          const providerKeys = [...privateCredentialKeys(this.credentials), ...this.retiredKeys];
          if (providerKeys.length && containsMcpSecret(snapshot, projectionRedactor(providerKeys))) {
            stateError('provider_secret', 'Remove the Provider credential from saved product data before making a recovery copy');
          }
        });
      }
      case 'operation.get':
        return this.operation(command.params.operationId);
    }
  }

  private mcpView(server: McpServerRecord, pools: ReturnType<McpConnectionManager['status']>): McpServerView {
    return { ...server, credentialStatus: mcpSecretsComplete(server, this.vault.get(server.serverId)) ? 'ok' : 'credential_missing',
      pools: pools.filter(pool => pool.serverId === server.serverId) };
  }

  private mcpServer(serverId: string): McpServerRecord {
    return this.profile.mcpServers?.find(server => server.serverId === serverId) ?? stateError('not_found', 'The MCP server does not exist');
  }

  private async saveMcpServer({ server, secrets, operationId }: ProductParams<'mcp.save'>): Promise<object> {
    const method = 'mcp.save';
    const { serverId } = server;
    const params = { server, operationId };
    const prior = this.sameOperation(method, operationId, params);
    const servers = this.profile.mcpServers ?? [];
    const previous = servers.find(row => row.serverId === serverId);
    const savedSecrets = this.vault.get(serverId);
    const values = mergeMcpSecrets(server, previous?.transport === server.transport ? savedSecrets : {}, secrets);
    if (prior) {
      const reply = prior.reply as { server: McpServerView };
      if (previous?.revision === reply.server.revision && secrets !== undefined && canonicalJSON(values) !== canonicalJSON(savedSecrets)) {
        stateError('operation_conflict', 'The operation ID belongs to different MCP credential input');
      }
      return reply;
    }
    if (server.revision !== undefined) requireRevision(previous?.revision ?? 0, server.revision);
    if (!validMcpServer(server)) stateError('invalid_params', 'The MCP server configuration is invalid');
    const redact = this.engine.redactor([...this.credentialKeys(), ...mcpSecretKeys(values)]);
    if (containsMcpSecret(server, redact)) stateError('mcp_secret', 'Put credential values only in the MCP secret fields');
    const { catalog, ...previousConfig } = previous ?? {};
    const changedSecrets = canonicalJSON(values) !== canonicalJSON(savedSecrets);
    const changedConfig = canonicalJSON({ ...server, revision: undefined }) !== canonicalJSON({ ...previousConfig, revision: undefined });
    const record = { ...server, revision: (previous?.revision ?? 0) + 1, ...(!changedConfig && !changedSecrets && catalog ? { catalog } : {}) };
    const next = previous ? servers.map(row => row.serverId === serverId ? record : row) : [...servers, record];
    if (next.length > maxMcpServers || !uniqueMcpRegistry(next)) stateError('invalid_params', 'MCP server namespaces must be unique and the registry is limited to 64 servers');
    const result = { server: { ...record, credentialStatus: mcpSecretsComplete(record, values) ? 'ok' : 'credential_missing', pools: [] } };
    this.commit(candidate => {
      candidate.mcpServers = next;
      this.record(candidate, method, params, { operationId, target: { connection: true }, state: 'accepted', result }, result);
    }, undefined, changedSecrets ? { serverId, values } : undefined, changedSecrets || changedConfig ? { credentialServerId: serverId } : undefined);
    await this.mcp.invalidate(serverId);
    this.emit('mcp.changed', { serverId, change: 'saved' });
    return result;
  }

  private async deleteMcpServer(params: ProductParams<'mcp.delete'>): Promise<object> {
    const method = 'mcp.delete';
    const { serverId, operationId } = params;
    const prior = this.sameOperation(method, operationId, params);
    if (prior) return prior.reply;
    this.mcpServer(serverId);
    const result = { deleted: true };
    const changedBots = this.commit(candidate => {
      const remaining = candidate.mcpServers!.filter(server => server.serverId !== serverId);
      if (remaining.length) candidate.mcpServers = remaining;
      else delete candidate.mcpServers;
      const changed: Bot[] = [];
      for (const bot of candidate.bots) {
        const bindings = bot.profile.mcpBindings;
        if (!bindings?.some(binding => binding.serverId === serverId)) continue;
        const retained = bindings.filter(binding => binding.serverId !== serverId);
        if (retained.length) bot.profile.mcpBindings = retained;
        else delete bot.profile.mcpBindings;
        bot.profileRevision++;
        changed.push(bot);
      }
      this.record(candidate, method, params, { operationId, target: { connection: true }, state: 'accepted', result }, result);
      return changed;
    }, undefined, Object.keys(this.vault.get(serverId)).length ? { serverId, values: {} } : undefined, { credentialServerId: serverId });
    await this.mcp.invalidate(serverId);
    for (const { retained, ...bot } of changedBots) this.emit('bot.changed', { bot, change: 'updated' });
    this.emit('mcp.changed', { serverId, change: 'deleted' });
    return result;
  }

  private async testMcpServer({ serverId, server, secrets }: ProductParams<'mcp.test'>): Promise<object> {
    if (Boolean(serverId) === Boolean(server)) stateError('invalid_params', 'Choose exactly one saved MCP server ID or draft configuration');
    const saved = serverId === undefined ? undefined : this.mcpServer(serverId);
    const draft = saved ?? { ...server!, revision: server!.revision ?? 0 };
    if (server && !validMcpServer(server)) stateError('invalid_params', 'The MCP server configuration is invalid');
    const previous = this.profile.mcpServers?.find(row => row.serverId === draft.serverId);
    const values = mergeMcpSecrets(draft, previous?.transport === draft.transport ? this.vault.get(draft.serverId) : {}, secrets);
    const redact = this.engine.redactor([...this.credentialKeys(), ...mcpSecretKeys(values)]);
    const result = await this.mcp.test(draft, values, this.connection(this.profile, this.effectiveRevision)?.configuration.workingDirectory ?? dirname(this.paths.profilePath), redact);
    if (saved) {
      this.commit(candidate => {
        const record = candidate.mcpServers!.find(row => row.serverId === saved.serverId)!;
        record.catalog = { tools: result.tools, fetchedAt: new Date().toISOString(), ...(result.error ? { error: result.error } : {}) };
      });
      this.emit('mcp.changed', { serverId: saved.serverId, change: 'tested' });
    }
    return result;
  }

  private validateProfile(profile: BotProfile, savedWorkspace = ''): BotProfile {
    if (!Value.Check(profileSchema, profile) || !profile.name.trim() ||
        profile.workspace && profile.workspace !== savedWorkspace && !isAbsolute(profile.workspace)) {
      stateError('invalid_params', 'The Bot requires a name and an absolute workspace when specified');
    }
    const mounts = profile.mountedSkills ?? [];
    if (new Set(mounts.map(ref => `${ref.source}:${ref.skillId}`)).size !== mounts.length) stateError('invalid_params', 'A skill can be mounted only once per source');
    if (profile.mcpBindings) {
      if (!validMcpBindings(profile.mcpBindings) || profile.mcpBindings.some(binding => !this.profile.mcpServers?.some(server => server.serverId === binding.serverId))) {
        stateError('invalid_params', 'MCP bindings must name distinct registered servers with a valid tool policy');
      }
      if (!profile.mcpBindings.length) {
        const { mcpBindings, ...rest } = profile;
        profile = rest;
      }
    }
    if (profile.mountedSkills?.length === 0) {
      const { mountedSkills, ...rest } = profile;
      profile = rest;
    }
    if (!profile.starterPrompts) return profile;
    const starterPrompts = new Set<string>();
    for (const item of profile.starterPrompts) {
      let prompt = '';
      let bytes = 0;
      for (const char of item.trim()) {
        bytes += Buffer.byteLength(char);
        if (bytes > 120) break;
        prompt += char;
      }
      if (prompt) starterPrompts.add(prompt.trimEnd());
      if (starterPrompts.size === 4) break;
    }
    return { ...profile, starterPrompts: [...starterPrompts] };
  }

  private resolveCredential(configuration: Connection): Connection {
    if (configuration.apiKey.trim()) return configuration;
    const saved = this.connection(this.profile, this.profile.savedRevision);
    const apiKey = saved && this.credentials?.sources[saved.sourceId]?.api_key;
    if (!apiKey?.trim()) stateError('invalid_params', 'An API key is required');
    return { ...configuration, apiKey };
  }

  private saveConnection(params: { operationId: string; configuration: Connection }) {
    const config = this.resolveCredential(params.configuration);
    const visible = { operationId: params.operationId, configuration: nonsecret(config) };
    const prior = this.sameOperation('connection.save', params.operationId, visible, config.apiKey);
    if (prior) return prior.reply;
    const endpoint = URL.parse(config.baseUrl);
    if (!endpoint || !['http:', 'https:'].includes(endpoint.protocol) || endpoint.username || endpoint.password ||
        config.contextTokens - config.outputTokens < 4 || !isAbsolute(config.workingDirectory)) {
      stateError('invalid_params', 'The connection requires an HTTP(S) endpoint, a working directory and valid limits');
    }
    const credentials = saveCredential(this.paths.credentialsPath, this.credentials, config);
    this.credentials = credentials;
    const { apiKey: _, ...configuration } = config;
    const sourceId = credentials.credential_source;
    return this.commit(candidate => {
      const revision = (candidate.connections.at(-1)?.revision ?? 0) + 1;
      candidate.connections.push({ revision, sourceId, configuration });
      candidate.savedRevision = revision;
      const result = { savedRevision: revision, effectiveRevision: this.effectiveRevision };
      return this.record(candidate, 'connection.save', visible, { operationId: params.operationId, state: 'accepted', target: { connection: true }, result }, result, revision);
    });
  }

  private capacity(): void {
    const configuration = this.connection(this.profile, this.effectiveRevision)?.configuration;
    if (!configuration && this.profile.providerEffectiveRevision === undefined) stateError('not_configured', 'Apply a connection first');
    if (this.active.size >= (configuration?.maxActiveTasks ?? 2)) stateError('busy', 'The configured execution capacity is full');
  }

  private listBots(cursor?: string, limit?: number) {
    const result = page(this.profile.bots, bot => bot.botId, 'bots', cursor, limit);
    return { ...result, items: result.items.map(bot => {
      const { pendingCount } = this.memoryStatus(this.profile, bot.botId);
      return pendingCount ? { ...bot, pendingLearnings: pendingCount } : bot;
    }) };
  }

  private memoryStatus(profile: ProductProfile, botId: string) {
    return { botId, rulesRevision: profile.memory?.meta.find(row => row.botId === botId)?.rulesRevision ?? 0,
      pendingCount: profile.memory?.pendingLearnings.filter(row => row.botId === botId && row.state === 'pending').length ?? 0 };
  }

  private ensureMemory(profile: ProductProfile): BotMemory {
    return profile.memory ??= { rules: [], workLogs: [], pendingLearnings: [], meta: [] };
  }

  private memoryMeta(memory: BotMemory, botId: string): BotMemoryMeta {
    let meta = memory.meta.find(row => row.botId === botId);
    if (!meta) {
      meta = { botId, rulesRevision: 0, forceReload: false };
      memory.meta.push(meta);
    }
    return meta;
  }

  private lastingRules(botId: string): BotMemoryRule[] {
    return this.profile.memory?.rules.filter(row => row.botId === botId).sort(oldestRuleFirst) ?? [];
  }

  private changeMemory<P extends { botId: string; operationId: string }>(method: string, params: P,
    mutate: (memory: BotMemory, bot: Bot, now: string) => void) {
    const prior = this.sameOperation(method, params.operationId, params);
    if (prior) return prior.reply;
    const { botId, operationId } = params;
    const result = this.commit(candidate => {
      const bot = this.bot(candidate, botId);
      mutate(this.ensureMemory(candidate), bot, new Date().toISOString());
      const changed = this.memoryStatus(candidate, botId);
      this.record(candidate, method, params, { operationId, target: { botId }, state: 'accepted' }, changed);
      return changed;
    });
    this.emit('memory.changed', result);
    return result;
  }

  private ownedMemoryItem<K extends string, T extends Record<K, string> & { botId: string }>(items: T[], botId: string, key: K, id: string): T {
    return items.find(row => row.botId === botId && row[key] === id) ?? stateError('not_found', 'The memory entry does not exist for this Bot');
  }

  credentialKeys(): string[] {
    return [...privateCredentialKeys(this.credentials), ...this.retiredKeys, ...this.vault.keys()];
  }

  private memoryCandidate(memory: BotMemory, bot: Bot, input: string, maxBytes: number,
    options: { ruleId?: string; date?: string; metadata?: string[] } = {}): string {
    const duplicates = memory.rules.filter(row => row.botId === bot.botId && row.ruleId !== options.ruleId).map(row => row.text);
    for (const row of memory.pendingLearnings) if (row.botId === bot.botId && row.state === 'pending') duplicates.push(row.text);
    if (options.date) {
      for (const row of memory.workLogs) if (row.botId === bot.botId && row.date === options.date) duplicates.push(row.summary);
    }
    const result = validateMemoryCandidate(input, { maxBytes, metadata: options.metadata, duplicates, instructions: bot.profile.instructions,
      credentials: this.credentialKeys() });
    if ('code' in result) stateError(result.code, result.message);
    return result.text;
  }

  private saveRule(memory: BotMemory, bot: Bot, input: string, source: BotMemoryRule['source'], now: string, existing?: BotMemoryRule): BotMemoryRule {
    const text = this.memoryCandidate(memory, bot, input, MAX_RULE_BYTES, { ruleId: existing?.ruleId });
    const rules = memory.rules.filter(row => row.botId === bot.botId);
    const latest = rules.reduce((time, row) => Math.max(time, Date.parse(row.createdAt)), 0);
    const createdAt = new Date(Math.max(latest + 1, Date.parse(now))).toISOString();
    const rule = existing ?? { botId: bot.botId, ruleId: newId(), text, source, createdAt, updatedAt: createdAt };
    if (!existing) memory.rules.push(rule);
    else Object.assign(rule, { text, updatedAt: new Date(Math.max(Date.parse(rule.createdAt), Date.parse(now))).toISOString() });
    if (!lastingRulesFit(existing ? rules : [...rules, rule])) stateError('memory_rule_limit', 'This Bot has reached its lasting rules limit');
    const meta = this.memoryMeta(memory, bot.botId);
    meta.rulesRevision++;
    if (source !== 'explicit' || existing) meta.forceReload = true;
    return rule;
  }

  private removeRule(memory: BotMemory, botId: string, ruleId: string, reload: boolean): void {
    const row = this.ownedMemoryItem(memory.rules, botId, 'ruleId', ruleId);
    memory.rules.splice(memory.rules.indexOf(row), 1);
    const meta = this.memoryMeta(memory, botId);
    meta.rulesRevision++;
    if (reload) meta.forceReload = true;
  }

  private saveWorkLog(memory: BotMemory, bot: Bot, input: string, now: string, tags: string[], executionId?: string,
    date = now.slice(0, 10), metadata = tags): BotWorkLog {
    if (!validMemoryDate(date)) stateError('memory_invalid', 'Use a calendar date in YYYY-MM-DD format');
    const summary = this.memoryCandidate(memory, bot, input, MAX_LOG_BYTES, { date, metadata });
    const row: BotWorkLog = { logId: newId(), botId: bot.botId, date, summary, tags, executionId, createdAt: now };
    memory.workLogs.push(row);
    const owned = memory.workLogs.filter(log => log.botId === bot.botId);
    if (owned.length > MAX_WORK_LOGS) {
      const remove = new Set(owned.sort(newestLogFirst).slice(MAX_WORK_LOGS).map(log => log.logId));
      memory.workLogs = memory.workLogs.filter(log => !remove.has(log.logId));
    }
    return row;
  }

  private proposeLearning(memory: BotMemory, bot: Bot, input: string, reason: string, now: string, executionId: string): BotPendingLearning {
    const text = this.memoryCandidate(memory, bot, input, MAX_RULE_BYTES, { metadata: [reason] });
    if (memory.pendingLearnings.filter(row => row.botId === bot.botId && row.state === 'pending').length >= MAX_PENDING_LEARNINGS) {
      stateError('memory_pending_limit', 'Review the pending learnings before adding more');
    }
    const row: BotPendingLearning = { learningId: newId(), botId: bot.botId, kind: 'rule', text, reason: reason.trim(), executionId, state: 'pending', createdAt: now };
    memory.pendingLearnings.push(row);
    return row;
  }

  private searchWorkLogs({ botId, query, dateFrom, dateTo, limit }: { botId: string; query: string; dateFrom?: string; dateTo?: string; limit?: number }) {
    this.bot(this.profile, botId);
    if ((dateFrom && !validMemoryDate(dateFrom)) || (dateTo && !validMemoryDate(dateTo)) || (dateFrom && dateTo && dateFrom > dateTo)) {
      stateError('memory_invalid', 'Use a valid date range');
    }
    return this.storage.searchWorkLogs(botId, query, dateFrom, dateTo, limit);
  }

  private async prepareSession(owner: ProductActor, existing: SessionBinding | undefined, bot: Bot, conversationBoundary = false,
    newRoute?: CapturedRoute) {
    if (existing && !this.bindingKey(existing)) stateError('credential_unavailable', 'The captured credential source is unavailable');
    const contextId = existing?.contextId ?? newId();
    const cached = this.sessions.get(contextId);
    const meta = 'conversationId' in owner ? this.profile.memory?.meta.find(row => row.botId === bot.botId) : undefined;
    const cold = !cached || !meta?.lastTurnAt || Date.now() - Date.parse(meta.lastTurnAt) >= MEMORY_COLD_TTL_MS;
    const reload = !!(existing && conversationBoundary && (this.skillReload.has(bot.botId) || cold ||
      meta?.forceReload && meta.rulesRevision !== (existing.memoryRulesRevision ?? 0)));
    const activityId = 'activityId' in owner ? owner.activityId : undefined;
    const activity = activityId ? this.activity(this.profile, activityId) : undefined;
    const role = activity ? activityRole(activity.routing, !!bot.profile.mcpBindings?.length) : 'dialogue';
    const lastTurnAt = existing?.lastTurnAt ?? meta?.lastTurnAt;
    const coldRoute = conversationBoundary && (!lastTurnAt || Date.now() - Date.parse(lastTurnAt) >= providerCacheTtlMs);
    const route = newRoute ?? ((existing ? coldRoute : true) ? this.selectRoute(role, activity?.routing?.complexity === 'deep') : null) ?? existing?.route;
    const routeChanged = canonicalJSON(route ?? null) !== canonicalJSON(existing?.route ?? null);
    if (cached && existing && !routeChanged && !reload && cached.instructions === existing.instructions && !cached.mcpSignature && !bot.profile.mcpBindings?.length) {
      return { session: cached.session, binding: existing };
    }
    const legacy = this.connection(this.profile, existing?.connectionRevision ?? this.effectiveRevision);
    const connectionRevision = route ? undefined : legacy?.revision;
    if (!route && (!legacy || !existing && this.profile.providerEffectiveRevision !== undefined)) {
      stateError(this.profile.providerEffectiveRevision === undefined ? 'not_configured' : 'credential_unavailable', 'No eligible Provider credential is available');
    }
    const apiKey = route ? this.providerKey(route) : this.credentials?.sources[legacy!.sourceId]?.api_key;
    if (!apiKey) stateError('credential_unavailable', 'The captured credential source is unavailable');
    const profileRevision = existing?.profileRevision ?? bot.profileRevision;
    const configuredCwd = existing?.cwd || bot.profile.workspace || legacy?.configuration.workingDirectory;
    const cwd = configuredCwd || join(this.stateDir, 'workspace');
    if (!configuredCwd) await mkdir(cwd, { recursive: true, mode: 0o700 });
    const instructions = existing && !reload ? existing.instructions : composeBotSystemPrompt({ profile: bot.profile, cwd, systemKind: bot.systemKind,
      capturedInstructions: existing?.instructions,
      lastingRules: meta ? formatLastingRulesBlock(this.lastingRules(bot.botId)) : undefined,
      skills: conversationBoundary ? this.skills.catalog(bot, cwd, this.discoverLocalSkills, this.engine.redactor(this.credentialKeys())) : undefined });
    const memoryRulesRevision = existing && !reload ? existing.memoryRulesRevision : meta?.rulesRevision;
    const codingTools = existing ? existing.codingTools : resolveCodingTools(bot.profile);
    const params: OpenParams = {
      contextId, botId: bot.botId, purpose: activityId ? 'activity' : 'conversation', activityId,
      selectionRevision: profileRevision, cwd, instructions, resumeRef: existing?.sessionRef,
      used: existing?.used ?? false, codingTools, domainTools: null,
      configuration: route ? { wireApi: route.wireApi, baseUrl: route.baseUrl, apiKey, model: route.resolvedModel,
        contextTokens: route.contextTokens, outputTokens: route.outputTokens, credentialSource: route.credentialSourceId,
        thinkingLevel: route.thinkingLevel, reasoning: route.thinkingLevel !== 'off' } : {
        wireApi: legacy!.configuration.wireApi, baseUrl: legacy!.configuration.baseUrl, apiKey, model: legacy!.configuration.model,
        contextTokens: legacy!.configuration.contextTokens, outputTokens: legacy!.configuration.outputTokens, credentialSource: legacy!.sourceId,
      },
    };
    const mounts = (bot.profile.mcpBindings ?? []).flatMap(binding => {
      const server = this.profile.mcpServers?.find(item => item.serverId === binding.serverId);
      return server ? [{ server, binding, secrets: this.vault.get(server.serverId) }] : [];
    });
    const mcp = await this.mcp.tools({ botId: bot.botId, cwd }, mounts, serverId => {
      const current = this.profile.bots.find(item => item.botId === bot.botId && !item.archived);
      const target = 'activityId' in owner ? this.profile.activities.find(item => item.activityId === owner.activityId) :
        this.profile.conversations.find(item => item.conversationId === owner.conversationId);
      const executionId = target && ('ownerBotId' in target ? target.executionIds.at(-1) : target.latestExecutionId);
      if (!current || !executionId || !this.active.has(executionId)) return;
      const binding = current.profile.mcpBindings?.find(item => item.serverId === serverId);
      const server = this.profile.mcpServers?.find(item => item.serverId === serverId);
      if (binding && server) return { server, binding, secrets: this.vault.get(serverId) };
    }, () => this.engine.redactor(this.credentialKeys()));
    const tools = [...this.tools(owner), ...mcp.tools];
    const refresh = !!(cached && (routeChanged || cached.instructions !== instructions || cached.mcpSignature !== mcp.signature));
    if (refresh) this.sessions.delete(contextId);
    const session = refresh ? await this.engine.refreshSession(params, tools) : await this.engine.open(params, tools);
    this.sessions.set(contextId, { session, instructions, mcpSignature: mcp.signature });
    if (conversationBoundary) this.skillReload.delete(bot.botId);
    return { session, binding: { contextId, sessionRef: session.sessionRef, connectionRevision, route, profileRevision, cwd, instructions, codingTools,
      used: existing?.used ?? false, memoryRulesRevision, lastTurnAt: existing?.lastTurnAt } };
  }

  private newExecution(operationId: string, input: Draft): StoredExecution {
    return { operationId, executionId: newId(), revision: 1, state: 'starting', input };
  }

  private async sendConversation(params: { conversationId: string; operationId: string; draftRevision: number }) {
    const prior = this.sameOperation('conversation.send', params.operationId, params);
    if (prior) return this.operation(params.operationId);
    const owner = { conversationId: params.conversationId };
    const conversation = this.current(this.profile, params.conversationId);
    if (live(this.execution(this.profile, conversation.latestExecutionId)) || unknown(this.execution(this.profile, conversation.latestExecutionId))) {
      stateError('busy', 'The previous conversation execution is unresolved');
    }
    requireRevision(conversation.draft.revision, params.draftRevision);
    if (!conversation.draft.text.trim()) stateError('invalid_params', 'A message requires text');
    const rejectCredential = () => this.commit(candidate => {
      const result = { code: 'credential_unavailable', delivery: 'non_dispatched' };
      const receipt: Operation = { operationId: params.operationId, state: 'rejected', target: owner, result };
      return this.record(candidate, 'conversation.send', params, receipt, receipt);
    });
    if (conversation.binding && !this.bindingKey(conversation.binding)) return rejectCredential();
    this.capacity();
    const { session, binding } = await this.prepareSession(owner, conversation.binding, this.bot(this.profile, conversation.botId), true);
    if (!this.bindingKey(binding)) return rejectCredential();
    this.capacity();
    const execution = this.newExecution(params.operationId, conversation.draft);
    const receipt: Operation = { operationId: params.operationId, state: 'accepted', target: owner, execution: publicExecution(execution) };
    this.commit(candidate => {
      const current = this.current(candidate, params.conversationId);
      requireRevision(current.draft.revision, params.draftRevision);
      current.binding = binding;
      const meta = candidate.memory?.meta.find(row => row.botId === current.botId);
      if (meta && meta.rulesRevision === binding.memoryRulesRevision) meta.forceReload = false;
      current.latestExecutionId = execution.executionId;
      current.revision++;
      candidate.executions.push(execution);
      this.record(candidate, 'conversation.send', params, receipt, receipt);
    });
    this.start(session, owner, execution);
    return receipt;
  }

  private start(session: ProductSession, owner: ProductActor, execution: StoredExecution): void {
    const executionId = execution.executionId;
    const current: LiveExecution = { owner, session, offset: 0, partial: '', files: [] };
    this.active.set(executionId, current);
    const target = { ...owner, executionId, operationId: execution.operationId };
    const guarded = (handler: () => void) => {
      try { handler(); }
      catch { this.engine.disconnect(); }
    };
    const redactChunk = this.engine.streamingRedactor(this.credentialKeys());
    const redactAdmission = this.engine.redactor(this.credentialKeys());
    const publishText = (text: string) => {
      if (!text) return;
      const offset = current.offset;
      current.offset += Buffer.byteLength(text);
      current.partial = current.partial !== null && current.offset <= maxPartialBytes ? current.partial + text : null;
      this.emit('execution.textDelta', { ...target, offset, text });
    };
    try {
      const binding = 'activityId' in owner ? this.activity(this.profile, owner.activityId).binding! : this.conversation(this.profile, owner.conversationId).binding!;
      const sourceId = binding.route?.credentialSourceId ?? this.connection(this.profile, binding.connectionRevision ?? null)?.sourceId;
      session.start(executionId, execution.input, {
        authorizeProvider: () => {
          if (this.closing || !this.active.has(executionId) || !this.bindingKey(binding)) stateError('credential_unavailable', 'The captured credential source is unavailable');
        },
        credentialRejected: () => { if (sourceId) this.rejectedSources.add(sourceId); },
        admitTool: (name, callId) => this.storage.admitTool(executionId, 'activityId' in owner ? owner.activityId : undefined,
          redactAdmission(callId), redactAdmission(name), Object.hasOwn(readOnlyTools, name) ? 'readonly' : 'mutating'),
        started: () => guarded(() => {
          const update = this.commit(candidate => {
            const run = this.execution(candidate, executionId)!;
            run.state = 'running';
            run.revision++;
            if ('conversationId' in owner) {
              const conversation = this.conversation(candidate, owner.conversationId);
              conversation.binding!.used = true;
              if (conversation.draft.revision === run.input.revision) {
                conversation.draft = { revision: run.input.revision + 1, text: '', materials: [] };
                conversation.revision++;
                return { run, conversation };
              }
              return { run };
            }
            const activity = this.activity(candidate, owner.activityId);
            activity.binding!.used = true;
            return { run };
          });
          this.emit('execution.started', { ...target, revision: update.run.revision });
          if (update.conversation) this.emit('conversation.changed', { ...owner, revision: update.conversation.revision });
        }),
        text: text => publishText(redactChunk(text)),
        settled: (state, text, error) => guarded(() => {
          publishText(redactChunk('', true));
          this.settle(owner, executionId, state, text, error);
        }),
        confirmedFileEffect: (path, callId) => guarded(() => {
          current.files.push({ path, callId });
          if ('activityId' in owner) this.observeTrace({ ...owner, executionId, kind: 'file_confirmed', callId, path });
        }),
        toolStarted: (tool, callId, target) => guarded(() => {
          if ('activityId' in owner) this.observeTrace({ ...owner, executionId, kind: 'tool_started', tool, callId, target });
        }),
        toolFinished: (tool, callId, isError, excerpt) => guarded(() => {
          this.storage.settleTool(executionId, redactAdmission(callId), isError);
          if ('activityId' in owner) {
            this.observeTrace({ ...owner, executionId, kind: 'tool_finished', tool, callId, ok: !isError, excerpt });
            if (tool === 'activity_ask') this.finishAnswerDelivery(owner.activityId, executionId, callId, isError);
          }
        }),
      });
    } catch (error) {
      guarded(() => this.settle(owner, executionId, 'failed', '', this.engine.cleanMessage(error)));
    }
  }

  private finishAnswerDelivery(activityId: string, executionId: string, callId: string, isError: boolean): void {
    const id = `${executionId}:${callId}`;
    const activity = this.activity(this.profile, activityId);
    const pending = activity.questions.find(question => question.questionId === id && question.executionId === executionId);
    if (!pending || pending.delivery !== 'dispatching') return;
    const updated = this.commit(candidate => {
      const current = this.activity(candidate, activityId);
      const question = this.question(current, id);
      question.delivery = isError ? 'unknown' : 'delivered';
      question.revision++;
      current.state = current.questions.some(item => item.state === 'open' && this.waiters.has(item.questionId)) ? 'waitingForAnswer' : 'running';
      current.revision++;
      return current;
    });
    this.emit('activity.changed', this.activityView(updated));
  }

  private settle(owner: ProductActor, executionId: string, state: 'completed' | 'failed' | 'interrupted', text: string, error?: string): void {
    const settledAt = new Date().toISOString();
    const active = this.active.get(executionId);
    const redact = this.engine.redactor(this.credentialKeys());
    const run = this.commit(candidate => {
      const execution = this.execution(candidate, executionId)!;
      execution.state = state;
      execution.revision++;
      execution.error = error && redact(error);
      if ('activityId' in owner) {
        const activity = this.activity(candidate, owner.activityId);
        activity.state = 'needsAttention';
        for (const question of activity.questions) {
          if (question.executionId === executionId && question.delivery === 'dispatching') {
            question.delivery = state === 'interrupted' ? 'canceled' : 'unknown';
            question.revision++;
          }
        }
        activity.revision++;
        activity.results.push({ activityId: owner.activityId, executionId, text: redact(text), delivery: 'available', files: active?.files ?? [] });
      } else {
        const conversation = this.conversation(candidate, owner.conversationId);
        if (conversation.binding) conversation.binding.lastTurnAt = settledAt;
        this.memoryMeta(this.ensureMemory(candidate), conversation.botId).lastTurnAt = settledAt;
      }
      return execution;
    });
    this.active.delete(executionId);
    this.emit('execution.settled', { ...owner, executionId, operationId: run.operationId, revision: run.revision, state, ...(run.error ? { error: run.error } : {}) });
    if ('activityId' in owner) {
      const activity = this.activity(this.profile, owner.activityId);
      this.emit('activity.changed', this.activityView(activity));
      if (state === 'failed') this.escalateActivity(activity, run);
    }
    if (state === 'completed') this.consolidateMemory(owner, run, text, settledAt, active?.learningToolUsed);
  }

  private escalateActivity(activity: Activity, run: StoredExecution): void {
    const original = activity.binding?.route;
    if (this.closing || original?.role !== 'utility' || !this.providerKey(original) ||
        !this.storage.canEscalate(activity.activityId, run.executionId)) return;
    const role = activity.routing?.kind === 'coding' ? 'coding' : 'reasoning';
    const route = this.selectRoute(role, true);
    if (!route) return;
    void this.startActivity('activity.continue', { activityId: activity.activityId, expectedRevision: activity.revision,
      operationId: `${run.executionId}:escalation`, text: '', materials: run.input.materials }, { route, prior: run }).catch(error => {
      if (!(error instanceof RequestError)) this.engine.disconnect();
    });
  }

  private consolidateMemory(owner: ProductActor, run: StoredExecution, result: string, settledAt: string, learningToolUsed?: boolean): void {
    if (this.closing) return;
    const task = setImmediate(undefined, { ref: false }).then(() => {
      if (this.closing) return;
      const { executionId } = run;
      if ('activityId' in owner) {
        const activity = this.activity(this.profile, owner.activityId);
        const summary = completedWorkSummary(activity.goal, result);
        this.changeMemory('memory.consolidateWork', { botId: activity.ownerBotId, executionId, operationId: `${executionId}:memory-log` }, (memory, bot, now) => {
          this.saveWorkLog(memory, bot, summary, now, ['activity'], executionId, settledAt.slice(0, 10), [activity.goal, result]);
          this.memoryMeta(memory, bot.botId).lastConsolidationAt = now;
        });
      } else if (!learningToolUsed) {
        const text = recurringPreference(run.input.text);
        if (!text) return;
        const { botId } = this.conversation(this.profile, owner.conversationId);
        this.changeMemory('memory.consolidateLearning', { botId, executionId, operationId: `${executionId}:memory-learning` }, (memory, bot, now) => {
          this.proposeLearning(memory, bot, text, 'Found a recurring preference in your message.', now, executionId);
          this.memoryMeta(memory, bot.botId).lastConsolidationAt = now;
        });
      }
    }).catch(() => {}).finally(() => this.backgroundTasks.delete(task));
    this.backgroundTasks.add(task);
  }

  private async readImportedActivityResult(activity: Activity, executionId: string): Promise<ActivityResult> {
    const execution = this.execution(this.profile, executionId);
    if (!execution?.engineRunId || !activity.binding?.used || live(execution) || unknown(execution)) {
      stateError('result_unavailable', 'This execution has no verified result');
    }
    const binding = activity.binding;
    const transcript = await this.readBoundTranscript(binding, activity.ownerBotId, activity.activityId);
    let found = false;
    let final: AssistantMessage | undefined;
    const calls = new Map<string, ToolCall | null>();
    const files: ResultFile[] = [];
    for await (const entry of transcriptEntries(transcript!)) {
      if (entry.type === 'custom' && entry.customType === turnEntryType) {
        if (found) break;
        const turn = entry.data as { runId?: string; correlation?: { executionId?: string } };
        const runMatches = turn.runId === execution.engineRunId;
        const executionMatches = turn.correlation?.executionId === executionId;
        if (runMatches !== executionMatches) stateError('result_unavailable', 'The saved result does not match its execution');
        found = runMatches;
      }
      if (!found || entry.type !== 'message') continue;
      const { message } = entry;
      if (message.role === 'assistant') {
        final = message;
        for (const part of message.content) {
          if (part.type === 'toolCall') {
            calls.set(part.id, calls.has(part.id) ? null : part);
          }
        }
      } else if (message.role === 'toolResult' && message.toolCallId) {
        const call = calls.get(message.toolCallId);
        calls.set(message.toolCallId, null);
        if (call && message.toolName === call.name && message.isError === false) {
          const path = confirmedFileEffect(binding.cwd, call.name, call.arguments, message.content);
          if (path) files.push({ path, callId: message.toolCallId });
        }
      }
    }
    if (!found || execution.state === 'completed' && final?.stopReason !== 'stop') stateError('result_unavailable', 'Pi history has no finalized result for this execution');
    const text = final?.content.filter(part => part.type === 'text').map(part => part.text).join('') ?? '';
    return { activityId: activity.activityId, executionId, text: this.engine.redactor(this.credentialKeys())(text), files, delivery: 'available' };
  }

  private stop(method: string, params: { operationId: string; executionId: string }, owner: ProductActor) {
    const prior = this.sameOperation(method, params.operationId, params);
    if (prior) return prior.reply;
    const active = this.active.get(params.executionId);
    if (!active || canonicalJSON(active.owner) !== canonicalJSON(owner)) stateError('not_found', 'The target execution is not active');
    const result = this.change(method, params, owner, () => ({ executionId: params.executionId, cancelRequested: true }));
    active.session.stop(params.executionId);
    return result;
  }

  private async history(conversationId: string, cursor?: string, limit = 25) {
    const conversation = this.conversation(this.profile, conversationId);
    const transcript = await this.readBoundTranscript(conversation.binding, conversation.botId);
    if (!transcript) return { items: [], nextCursor: null };
    const redact = this.engine.redactor(this.credentialKeys());
    const messages = [];
    for await (const entry of transcriptEntries(transcript)) {
      const message = publicConversationMessage(entry);
      if (!message) continue;
      const { quote, ...row } = message;
      const publicQuote = quote && redactQuote(quote, redact);
      messages.push({ ...row, text: redact(row.text), materials: row.materials.map(material => ({ ...material,
        text: redact(material.text), name: redact(material.name), source: material.source === undefined ? undefined : redact(material.source),
      })), ...(publicQuote ? { quote: publicQuote } : {}) });
    }
    for (let count = limit; count > 0; count--) {
      const result = page(messages, item => item.messageId, `history:${conversationId}`, cursor, count);
      if (frameBytes(result) < MAX_FRAME - 1024) return result;
    }
    stateError('entry_too_large', 'An individual history entry exceeds the frame limit');
  }

  private readBoundTranscript(binding: SessionBinding | undefined, botId: string, activityId?: string): Promise<Transcript> | undefined {
    if (!binding?.used) return;
    return this.engine.readTranscript(binding.sessionRef, { contextId: binding.contextId, botId, activityId,
      purpose: activityId === undefined ? 'conversation' : 'activity', selectionRevision: binding.profileRevision, cwd: binding.cwd });
  }

  private scopedActivity(activityId: string, executionId?: string, actor?: ProductActor): Activity {
    const target = this.profile.activities.find(item => item.activityId === activityId);
    let allowed = !actor;
    if (target && actor) {
      if ('activityId' in actor) {
        allowed = this.profile.activities.find(item => item.activityId === actor.activityId)?.grantedLinkedActivityIds?.includes(activityId) ?? false;
      } else {
        const caller = this.profile.conversations.find(item => item.conversationId === actor.conversationId);
        const origin = this.profile.conversations.find(item => item.conversationId === target.originConversationId);
        allowed = !!caller && directlyOwns(target, caller.botId, origin?.botId);
      }
    }
    if (!target || !allowed || (executionId !== undefined && !target.executionIds.includes(executionId))) {
      return stateError(actor ? 'scope_denied' : 'not_found', 'The Activity or execution is outside the requested scope');
    }
    return target;
  }

  private readTrace(params: ProductParams<'activity.trace'>, actor?: ProductActor) {
    const { activityId, executionId, limit = 20, maxBytes = 8192 } = params;
    const activity = this.scopedActivity(activityId, executionId, actor);
    const { generation, high, recorded, evicted } = this.storage.traceBounds(activityId, executionId);
    const scope = cursorScope('activity.trace', activityId, generation, executionId);
    const cursor = decodeCursor(params.cursor, scope, false);
    if (cursor && (cursor.high > high || cursor.position < evicted || cursor.highEdge || cursor.positionEdge ||
        !this.storage.traceRows(activityId, executionId, cursor.position - 1, cursor.position, 1).length)) return invalidCursor();
    const through = cursor?.high ?? high;
    const rows = this.storage.traceRows(activityId, executionId, cursor?.position ?? 0, through, limit + 1);
    const total = executionId === undefined ? activity.executionIds.length : 1;
    const result: { items: TraceItem[]; nextCursor: string | null; historyTruncated: boolean; coverage: 'unrecorded' | 'recorded' | 'mixed' } = {
      items: [], nextCursor: null, historyTruncated: evicted > 0,
      coverage: recorded === 0 ? 'unrecorded' : recorded === total ? 'recorded' : 'mixed',
    };
    const redact = this.engine.redactor(this.credentialKeys());
    for (let index = 0; index < rows.length && index < limit; index++) {
      const row = rows[index];
      if (redact(row.activityId) !== row.activityId || redact(row.executionId) !== row.executionId) return oversizedEntry();
      const nextCursor = index + 1 === rows.length ? null : encodeCursor(scope, { high: through, position: row.seq, tail: false });
      const item = redactTrace(row, redact);
      result.items.push(item);
      if (encodedBytes({ ...result, nextCursor }) > maxBytes) {
        if (result.items.length === 1) return oversizedEntry();
        result.items.pop();
        break;
      }
      result.nextCursor = nextCursor;
    }
    return result;
  }

  private async readConversation(actor: ProductActor, conversationId: string, params: InspectionParams & { tail?: boolean }, query?: string) {
    const target = this.profile.conversations.find(item => item.conversationId === conversationId);
    const allowed = 'activityId' in actor
      ? this.profile.activities.find(item => item.activityId === actor.activityId)?.originConversationId === conversationId
      : this.profile.conversations.find(item => item.conversationId === actor.conversationId)?.botId === target?.botId;
    if (!target || !allowed) stateError('scope_denied', 'The conversation is outside this actor scope');
    const normalized = query === undefined ? undefined : inspectionQuery(query);
    const transcript = await this.readBoundTranscript(target.binding, target.botId);
    return inspectConversation(transcript, conversationId, params, this.engine.redactor(this.credentialKeys()), normalized);
  }

  private inspectionTools(actor: ProductActor): ToolDefinition[] {
    const read = <T extends TSchema>(name: string, description: string, parameters: T, action: (input: Static<T>) => object | Promise<object>): ToolDefinition<T> => ({
      name, label: description, description, parameters,
      execute: async (_callId, input) => {
        try {
          if (!Value.Check(parameters, input)) stateError('invalid_params', 'Invalid inspection parameters');
          return toolText(await action(input));
        } catch (error) {
          if (!(error instanceof RequestError)) throw error;
          return toolText({ code: error.code, message: this.engine.cleanMessage(error) }, true);
        }
      },
    });
    return [
      read('activity_trace_read', 'Read committed trace observations for an authorized Activity.', traceToolParams,
        ({ activity_id, execution_id, cursor, limit, max_bytes }) => this.readTrace({ activityId: activity_id, executionId: execution_id, cursor, limit, maxBytes: max_bytes }, actor)),
      read('conversation_read', 'Read submitted turns and captured Materials in an authorized conversation.', readToolParams,
        ({ conversation_id, cursor, limit, max_bytes, tail }) => this.readConversation(actor, conversation_id, { cursor, limit, maxBytes: max_bytes, tail })),
      read('conversation_search', 'Find literal text in submitted turns and captured Materials in an authorized conversation.', searchToolParams,
        ({ conversation_id, cursor, limit, max_bytes, query }) => this.readConversation(actor, conversation_id, { cursor, limit, maxBytes: max_bytes }, query)),
    ];
  }

  private modelResult(value: unknown): ToolTextResult {
    return toolText(projectionJSON(value, this.engine.redactor(this.credentialKeys())));
  }

  private async continueActivity(params: { activityId: string; operationId: string; expectedRevision: number; text: string; materials: Material[] }) {
    return this.startActivity('activity.continue', params);
  }

  private async startActivity(method: string, params: { activityId: string; operationId: string; expectedRevision: number; text: string; materials: Material[] },
    escalation?: { route: CapturedRoute; prior: StoredExecution }) {
    const prior = this.sameOperation(method, params.operationId, params);
    if (prior) return this.operation(params.operationId);
    const owner = { activityId: params.activityId };
    const activity = this.activity(this.profile, params.activityId);
    requireRevision(activity.revision, params.expectedRevision);
    const bot = this.bot(this.profile, activity.ownerBotId);
    const last = this.execution(this.profile, activity.executionIds.at(-1));
    if (bot.archived || live(last) || unknown(last)) stateError('busy', 'The activity is not ready to continue');
    this.capacity();
    const input = escalation?.prior.input ?? { revision: activity.revision,
      text: [activity.goal, ...activity.requirements, ...activity.routing?.constraints ?? [], params.text,
        ...activity.inputFiles.map(path => `Input file: ${path}`)].filter(Boolean).join('\n'), materials: params.materials };
    validateDraft(input);
    const { session, binding } = await this.prepareSession(owner, escalation ? undefined : activity.binding, bot, false, escalation?.route);
    if (this.closing) stateError('closing', 'The app is shutting down');
    const latest = this.activity(this.profile, params.activityId);
    if (escalation && (!latest.binding || !this.bindingKey(latest.binding) || escalation.prior.executionId !== latest.executionIds.at(-1))) {
      stateError('escalation_unavailable', 'The prior execution is no longer eligible to escalate');
    }
    if (!this.bindingKey(binding)) stateError('credential_unavailable', 'The captured credential source is unavailable');
    this.capacity();
    const execution = this.newExecution(params.operationId, input);
    const receipt: Operation = { operationId: params.operationId, state: 'accepted', target: owner, execution: publicExecution(execution) };
    this.commit(candidate => {
      const current = this.activity(candidate, params.activityId);
      requireRevision(current.revision, params.expectedRevision);
      current.executionIds.push(execution.executionId);
      current.binding = binding;
      current.revision++;
      current.state = 'running';
      candidate.executions.push(execution);
      this.record(candidate, method, params, receipt, receipt);
    }, undefined, undefined, undefined, escalation ? { escalation: { activityId: owner.activityId, priorExecutionId: escalation.prior.executionId,
      escalatedExecutionId: execution.executionId, fromRole: 'utility', toRole: escalation.route.role === 'coding' ? 'coding' : 'reasoning',
      at: new Date().toISOString() } } : undefined);
    this.emit('activity.changed', this.activityView(this.activity(this.profile, owner.activityId)));
    this.start(session, owner, execution);
    return receipt;
  }

  private answer(params: { activityId: string; executionId: string; questionId: string; questionRevision: number; operationId: string; text: string; materials: Material[] }) {
    const method = 'question.answer';
    const prior = this.sameOperation(method, params.operationId, params);
    if (prior) return prior.reply;
    const { activityId, questionId, executionId, text, materials } = params;
    validateDraft({ revision: 0, text, materials });
    if (!text.trim()) stateError('invalid_params', 'An answer requires text');
    const waiter = this.waiters.get(questionId);
    const target = { activityId, questionId };
    const binding = this.activity(this.profile, activityId).binding;
    const deliveryBlocker = binding && !this.bindingKey(binding) ? 'credential_unavailable' : undefined;
    const result = this.change(method, params, target, candidate => {
      const activity = this.activity(candidate, activityId);
      const question = this.question(activity, questionId);
      requireRevision(question.revision, params.questionRevision);
      if (question.executionId !== executionId || question.state !== 'open') stateError('question_conflict', 'The answer does not target this unanswered execution');
      question.answer = { text, materials };
      question.state = 'answered';
      question.delivery = 'saved';
      if (deliveryBlocker) question.deliveryBlocker = deliveryBlocker;
      question.revision++;
      activity.revision++;
      return { ...target, executionId, revision: question.revision, answerSaved: true, delivery: question.delivery, ...(deliveryBlocker ? { deliveryBlocker } : {}) };
    });
    this.emit('activity.changed', this.activityView(this.activity(this.profile, activityId)));
    if (waiter) {
      this.waiters.delete(questionId);
      if (deliveryBlocker) this.active.get(executionId)?.session.stop(executionId);
      else if (live(this.execution(this.profile, executionId))) waiter.resolve({ text, materials });
    }
    return result;
  }

  private get discoverLocalSkills(): boolean {
    return this.profile.preferences?.discoverLocalSkills ?? false;
  }

  private skillWorkspace(owner?: Bot): string {
    return owner?.profile.workspace || this.connection(this.profile, this.effectiveRevision)?.configuration.workingDirectory ||
      (this.profile.providerEffectiveRevision === undefined ? '' : join(this.stateDir, 'workspace'));
  }

  private skillOwner(source: SkillSource, botId?: string, writable = false): Bot | undefined {
    if ((source === 'private' || source === 'workspace') && !botId) return stateError('invalid_params', 'A Bot ID is required for this skill source');
    const bot = botId === undefined ? undefined : this.bot(this.profile, botId);
    if (writable && bot?.archived) return stateError('scope_denied', 'Restore this Bot before changing its private skills');
    return bot;
  }

  private skillGrant(botId: string, source: SkillSource, skillId: string, bundle: SkillBundle, cwd: string) {
    const workspace = cwd ? realpathSync(cwd) : '';
    const approvalDigest = bundleDigest(botId, source, skillId, bundle.files);
    const permissions = Object.values(bundle.manifest.scripts).map(script => script.permissions);
    const origins = [...new Set(permissions.flatMap(rule => (rule.networkOrigins ?? []).map(canonicalSkillOrigin)))].sort();
    const credentialRefs = [...new Set(permissions.flatMap(rule => rule.credentialRefs ?? []))].sort();
    const credentialBindings = credentialRefs.map(ref => {
      const [, id] = ref.split(':');
      const server = this.profile.mcpServers?.find(row => row.serverId === id);
      return [ref, server?.transport === 'http' ? [server.url, server.enabled, server.secretHeaderKeys, server.revision] : null];
    });
    const permissionsDigest = framedSkillDigest([workspace, canonicalJSON(origins), canonicalJSON(credentialBindings)]);
    const identity = { ownerBotId: botId, source, skillId, approvalDigest, permissionsDigest };
    return { ...identity, grantKey: skillGrantKey(identity), workspace, credentialRefs };
  }

  private skillReview(owner: Bot | undefined, key: Pick<SkillDetail, 'source' | 'skillId'>, cwd: string, strict = false, includeFiles = false): Partial<SkillDetail> {
    try {
      const bundle = this.skills.bundle({ ...key, botId: owner?.botId }, cwd, this.discoverLocalSkills);
      if (!bundle) return {};
      const metadata: Partial<SkillDetail> = { scripts: bundle.manifest.scripts, approvalStatus: 'approval_required' };
      if (includeFiles) {
        checkedSkillFiles(bundle.files, this.engine.redactor(this.credentialKeys()));
        metadata.files = bundle.files;
      }
      if (!owner) return metadata;
      const review = this.skillGrant(owner.botId, key.source, key.skillId, bundle, cwd);
      const row = this.storage.skillApproval(review.grantKey);
      const approvalStatus = row?.mode === 'pure_compute_tested' || row?.installationId === this.installationId ? row.mode : 'approval_required';
      return { ...metadata, approvalDigest: review.approvalDigest, approvalStatus, canonicalWorkspace: review.workspace,
        permissionsDigest: review.permissionsDigest, ...(row?.testedScripts ? { testedScripts: row.testedScripts } : {}) };
    } catch (error) {
      if (strict) throw error;
      return { approvalStatus: 'approval_required' };
    }
  }

  private skillListing(owner?: Bot, cwd = this.skillWorkspace(owner)): SkillListing {
    const result = this.skills.list(owner, cwd, this.discoverLocalSkills);
    const review = <T extends Pick<SkillDetail, 'source' | 'skillId'>>(skill: T) => ({ ...skill, ...this.skillReview(owner, skill, cwd) });
    return { ...result, global: result.global.map(review), workspace: result.workspace.map(review), local: result.local.map(review),
      private: result.private.map(review), mounted: result.mounted.map(row => row.skill ? { ...row, skill: review(row.skill) } : row) };
  }

  private skillForBot(owner: Bot, skillId: string, cwd: string, source?: SkillSource): SkillDetail {
    if (source === undefined || source === 'private') {
      const own = this.skills.get({ source: 'private', botId: owner.botId, skillId }, cwd, false);
      if (own) return own;
    }
    const refs = (owner.profile.mountedSkills ?? []).filter(ref => ref.skillId === skillId && (source === undefined || ref.source === source));
    if (!refs.length) return stateError('scope_denied', 'The skill is outside the authorized scope');
    if (refs.length > 1) return stateError('invalid_params', 'Specify the source for this skill ID');
    const ref = refs[0];
    const skill = this.skills.get(ref, cwd, this.discoverLocalSkills);
    if (!skill || ref.expectedDigest && ref.expectedDigest !== skill.sha256) return stateError('skill_missing', 'The mounted skill is missing');
    return skill;
  }

  private skillCredential(origin: string, refs: string[]): Record<string, string> {
    const headers = new Map<string, string>();
    for (const ref of refs) {
      const [, serverId, header] = ref.split(':');
      const server = this.profile.mcpServers?.find(item => item.serverId === serverId);
      const field = header.toLowerCase();
      if (server?.transport !== 'http' || !server.enabled || !server.secretHeaderKeys?.some(key => key.toLowerCase() === field)) {
        return stateError('credential_missing', 'An approved MCP credential binding is unavailable');
      }
      if (canonicalSkillOrigin(new URL(server.url).origin) !== origin) continue;
      if (headers.has(field) || /^(?:host|connection|content-length|transfer-encoding|upgrade|proxy-authorization)$/i.test(field)) {
        return stateError('scope_denied', 'The approved credentials conflict with HTTP transport headers');
      }
      const value = this.vault.get(serverId)[field];
      if (typeof value !== 'string' || !value) return stateError('credential_missing', 'An approved MCP credential is unavailable');
      headers.set(field, value);
    }
    return Object.fromEntries(headers);
  }

  private changeApproval(method: string, params: object, result: SkillChange, change: SkillApprovalChange, operationId: string): object {
    const prior = this.sameOperation(method, operationId, params);
    if (prior) return prior.reply;
    this.commit(candidate => this.record(candidate, method, params, { operationId, target: { botId: result.botId! },
      state: 'accepted', result }, result), undefined, undefined, change);
    this.emit('skill.changed', result);
    return result;
  }

  private approveSkill(params: ProductParams<'skill.approve'>): object {
    const { botId, source, skillId, approvalDigest, permissionsDigest, operationId } = params;
    const prior = this.sameOperation('skill.approve', operationId, params);
    if (prior) return prior.reply;
    const owner = this.skillOwner(source, botId, true)!;
    const cwd = this.skillWorkspace(owner);
    this.skillForBot(owner, skillId, cwd, source);
    const bundle = this.skills.bundle({ botId, source, skillId }, cwd, this.discoverLocalSkills);
    if (!bundle) return stateError('invalid_skill', 'The skill has no executable scripts');
    checkedSkillFiles(bundle.files, this.engine.redactor(this.credentialKeys()));
    const { workspace, ...grant } = this.skillGrant(botId, source, skillId, bundle, cwd);
    if (grant.approvalDigest !== approvalDigest || permissionsDigest !== undefined && grant.permissionsDigest !== permissionsDigest) {
      return stateError('approval_required', 'The skill or its permissions changed after review');
    }
    for (const { permissions } of Object.values(bundle.manifest.scripts)) {
      for (const ref of permissions.credentialRefs ?? []) {
        const [, id] = ref.split(':');
        const server = this.profile.mcpServers?.find(row => row.serverId === id);
        const origin = server?.transport === 'http' ? canonicalSkillOrigin(new URL(server.url).origin) : undefined;
        if (!origin || !(permissions.networkOrigins ?? []).some(value => canonicalSkillOrigin(value) === origin)) {
          return stateError('invalid_skill', 'Each credential reference requires its server origin in the script permissions');
        }
        this.skillCredential(origin, [ref]);
      }
    }
    const row: SkillApproval = { ...grant, mode: 'user_approved', installationId: this.installationId, approvedAt: new Date().toISOString() };
    return this.changeApproval('skill.approve', params, { botId, source, skillId, change: 'approved' }, { put: row }, operationId);
  }

  private async runSkill(test: boolean, owner: Bot, { skill_id: skillId, source, script, input }: { skill_id: string; source?: SkillSource;
      script: string; input?: unknown }, operationId: string, signal?: AbortSignal): Promise<object> {
    const node = await this.sandbox.prepare();
    const currentOwner = this.bot(this.profile, owner.botId);
    if (currentOwner.archived || signal?.aborted) return stateError('scope_denied', 'The Bot no longer owns this skill call');
    const cwd = this.skillWorkspace(currentOwner);
    const skill = this.skillForBot(currentOwner, skillId, cwd, source);
    const bundle = this.skills.bundle({ botId: owner.botId, source: skill.source, skillId }, cwd, this.discoverLocalSkills);
    if (!bundle || !Object.hasOwn(bundle.manifest.scripts, script)) return stateError('invalid_skill', 'The script is not declared by the skill');
    const redact = this.engine.redactor(this.credentialKeys());
    checkedSkillFiles(bundle.files, redact);
    if (input !== undefined && (frameBytes({ input }) > 65536 || containsMcpSecret(input, redact))) {
      return stateError('invalid_params', 'The skill input exceeds its limit or contains credentials');
    }
    const { workspace, ...grant } = this.skillGrant(owner.botId, skill.source, skillId, bundle, cwd);
    const permissions = bundle.manifest.scripts[script].permissions;
    const accepted = this.storage.skillApproval(grant.grantKey);
    const result = await this.sandbox.run(node, { botId: owner.botId, source: skill.source, skillId }, bundle, script, input, workspace,
      test ? {} : permissions, digest => {
        if (digest !== grant.approvalDigest) return stateError('invalid_skill', 'The staged skill bytes changed');
        if (!test && !(accepted?.mode === 'user_approved' && accepted.installationId === this.installationId ||
            accepted?.mode === 'pure_compute_tested' && pureSkill(permissions) && accepted.testedScripts?.includes(script))) {
          throw new RequestError('approval_required', 'Review and approve the current skill permissions before execution', undefined,
            { approvalDigest: digest, permissions, permissionsDigest: grant.permissionsDigest, canonicalWorkspace: workspace });
        }
      }, (origin, refs) => {
        if (this.storage.skillApproval(grant.grantKey)?.mode !== 'user_approved') return stateError('approval_required', 'The approved credential binding changed');
        return this.skillCredential(origin, refs);
      }, redact, signal);
    if (test && result.ok && pureSkill(permissions) && !this.closing && !signal?.aborted) {
      const fresh = this.skills.bundle({ botId: owner.botId, source: skill.source, skillId }, cwd, this.discoverLocalSkills);
      const latest = this.bot(this.profile, owner.botId);
      const prior = this.storage.skillApproval(grant.grantKey);
      if (fresh && !latest.archived && this.skillWorkspace(latest) === cwd && bundleDigest(owner.botId, skill.source, skillId, fresh.files) === grant.approvalDigest &&
          prior?.mode !== 'user_approved') {
        const testedScripts = [...new Set([...(prior?.testedScripts ?? []), script])].sort();
        const { credentialRefs, ...pureGrant } = grant;
        const row: SkillApproval = { ...pureGrant, mode: 'pure_compute_tested', installationId: this.installationId, testedScripts, approvedAt: new Date().toISOString() };
        this.changeApproval('skill_test', { botId: owner.botId, skillId, script, approvalDigest: grant.approvalDigest },
          { botId: owner.botId, source: skill.source, skillId, change: 'tested' }, { put: row }, operationId);
      }
    }
    return result;
  }

  private modelProjection<T>(value: T): T {
    return projectionJSON(value, this.engine.redactor(this.credentialKeys())) as T;
  }

  private changeSkill(method: string, params: { operationId: string }, result: SkillChange, write: SkillWrite): SkillChange {
    const target = result.botId ? { botId: result.botId } : { connection: true as const };
    this.commit(candidate => this.record(candidate, method, params,
      { operationId: params.operationId, target, state: 'accepted', result }, result), write, undefined,
    { source: write.source, skillId: write.skillId, botId: write.botId });
    this.emit('skill.changed', result);
    return result;
  }

  private saveSkill(params: ProductParams<'skill.save'>): object {
    const prior = this.sameOperation('skill.save', params.operationId, params);
    if (prior) return prior.reply;
    const { source, botId, skillId, name, description, content, manifest, scripts } = params;
    this.skillOwner(source, botId, true);
    const redact = this.engine.redactor(this.credentialKeys());
    if ([name, description, content].some(text => redact(text) !== text)) return stateError('skill_secret', 'Remove saved credentials before saving the skill');
    if (scripts && !manifest) return stateError('invalid_skill', 'Saving executable scripts requires a manifest');
    const files = manifest === undefined ? undefined : manifest === null ? {} : { ...scripts, 'manifest.json': canonicalJSON(manifest) + '\n' };
    const write = this.skills.plan(source, botId, skillId, redact, formatSkill(name, description, content), false, files);
    return this.changeSkill('skill.save', params, { source, botId, skillId, change: 'saved' }, write);
  }

  private deleteSkill(params: ProductParams<'skill.delete'>, model = false): object {
    const prior = this.sameOperation('skill.delete', params.operationId, params);
    if (prior) return prior.reply;
    const { source, botId, skillId } = params;
    this.skillOwner(source, botId, true);
    if (model && !this.skills.get({ source, botId, skillId }, '', false)) return stateError('scope_denied', 'The skill is outside the authorized scope');
    const write = this.skills.plan(source, botId, skillId, this.engine.redactor(this.credentialKeys()));
    return this.changeSkill('skill.delete', params, { source, botId, skillId, change: 'deleted' }, write);
  }

  private architectMounts(ids: string[], workspace: string): NonNullable<BotProfile['mountedSkills']> {
    const mounts: NonNullable<BotProfile['mountedSkills']> = [];
    const cwd = workspace || this.skillWorkspace();
    for (const skillId of new Set(ids)) {
      for (const source of ['global', 'workspace', 'local'] as const) {
        if (!this.skills.get({ source, skillId }, cwd, this.discoverLocalSkills)) continue;
        mounts.push({ source, skillId });
        break;
      }
      if (mounts.at(-1)?.skillId !== skillId) stateError('skill_missing', 'An assigned skill is unavailable');
    }
    return mounts;
  }

  private skillTools(conversationId: string): ToolDefinition[] {
    const list = Type.Object({}, strictTool);
    const read = Type.Object({ skill_id: Type.String(), source: Type.Optional(skillSource) }, strictTool);
    const save = Type.Object({ skill_id: Type.String(), ...skillText, ...skillExecutable }, strictTool);
    const execute = Type.Object({ skill_id: Type.String(), source: Type.Optional(skillSource), script: skillScriptName, input: Type.Optional(Type.Unknown()) }, strictTool);
    const remove = Type.Object({ skill_id: Type.String() }, strictTool);
    const tool = <T extends TSchema>(name: string, description: string, parameters: T,
      handler: (input: Static<T>, owner: Bot, workspace: string, operationId: string, signal?: AbortSignal) => object | Promise<object>): ToolDefinition<T> => ({
      name, label: description, description, parameters,
      execute: async (callId, args, signal) => {
        const conversation = this.conversation(this.profile, conversationId);
        const owner = this.bot(this.profile, conversation.botId);
        const executionId = conversation.latestExecutionId;
        if (signal?.aborted || !executionId || !this.active.has(executionId)) stateError('scope_denied', 'The conversation no longer owns this call');
        if (!Value.Check(parameters, args)) stateError('invalid_params', 'Invalid skill parameters');
        if ('skill_id' in args && !Value.Check(skillID, args.skill_id)) stateError('scope_denied', 'The skill is outside the authorized scope');
        return this.modelResult(await handler(args, owner, conversation.binding?.cwd ?? this.skillWorkspace(owner), `${executionId}:${name}:${callId}`, signal));
      },
    });
    return [
      tool('skill_list', 'List mounted text skills and private skills for this Bot.', list, (_, owner, cwd) => ({
        mounted: this.skills.mounted(owner, cwd, this.discoverLocalSkills).map(row => row.skill ? { ...row, skill: { ...row.skill, ...this.skillReview(owner, row.skill, cwd) } } : row),
        private: this.skills.private(owner.botId).map(skill => ({ ...skill, ...this.skillReview(owner, skill, cwd) })),
      })),
      tool('skill_read', 'Read an authorized mounted or private SKILL.md on demand.', read, ({ skill_id, source }, owner, cwd) => {
        const skill = this.skillForBot(owner, skill_id, cwd, source);
        return { skill: { ...skill, ...this.skillReview(owner, skill, cwd, true) } };
      }),
      tool('skill_save', 'Save a private SKILL.md and optional executable bundle. Privileged scripts require user approval.', save, ({ skill_id, ...text }, owner, _cwd, operationId) =>
        this.saveSkill({ ...text, source: 'private', botId: owner.botId, skillId: skill_id, operationId })),
      tool('skill_delete', 'Delete a private skill of this Bot when the user explicitly requests it.', remove, ({ skill_id }, owner, _cwd, operationId) =>
        this.deleteSkill({ source: 'private', botId: owner.botId, skillId: skill_id, operationId }, true)),
      tool('skill_test', 'Test a declared skill script without workspace or HTTP access. Pure scripts activate only after a successful test.', execute,
        (args, owner, _, operationId, signal) => this.runSkill(true, owner, args, operationId, signal)),
      tool('skill_exec', 'Run a declared skill script with its verified approval.', execute,
        (args, owner, _, operationId, signal) => this.runSkill(false, owner, args, operationId, signal)),
    ];
  }

  private tools(owner: ProductActor): ToolDefinition[] {
    const inspection = this.inspectionTools(owner);
    if ('activityId' in owner) {
      const question = Type.Object({ text: id }, strictTool);
      return [{
        name: 'activity_ask', label: 'Ask about the activity', description: 'Ask the user for information needed to finish this activity.',
        parameters: question,
        execute: async (callId, input, signal) => {
          if (!Value.Check(question, input)) return toolText('A question requires text', true);
          return this.waitForAnswer(owner.activityId, callId, input.text, signal);
        },
      }, ...inspection];
    }
    const list = Type.Object({}, strictTool);
    const target = Type.Object({ activity_id: id }, strictTool);
    const change = Type.Object({ activity_id: id, action: Type.Union([Type.Literal('stop'), Type.Literal('add_supplement'), Type.Literal('resume')]), text: Type.Optional(Type.String()) }, strictTool);
    const botId = () => this.conversation(this.profile, owner.conversationId).botId;
    const architect = this.bot(this.profile, botId()).systemKind === SYSTEM_KIND_BOT_ARCHITECT;
    const tools: ToolDefinition[] = [
      ...inspection,
      { name: 'bot_list', label: 'List Bots', description: 'Find the exact ID of an available Bot before an explicit handoff.', parameters: list,
        execute: async () => this.modelResult(this.profile.bots.filter(bot => !bot.archived && bot.botId !== botId()).map(({ botId, profile }) => ({ bot_id: botId, name: profile.name, role: profile.role }))) },
      { name: 'activity_list', label: 'List activities', description: 'Read activity state for this Bot.', parameters: list,
        execute: async () => this.modelResult(this.visibleActivities(botId()).map(activity => this.activityView(activity))) },
      { name: 'activity_get', label: 'Read activity', description: 'Read the exact activity, questions, and execution results.', parameters: target,
        execute: async (_callId, input) => {
          if (!Value.Check(target, input)) return toolText('An activity ID is required', true);
          const activity = this.scopedActivity(input.activity_id, undefined, owner);
          return this.modelResult({ ...this.activityDetails(activity), results: activity.results });
        } },
      { name: 'activity_delegate', label: 'Delegate activity', description: 'Start independently tracked work only when the user assigns it. Use kind, complexity and constraints to describe the work. Link only directly accessible Activity IDs.', parameters: delegateInput,
        execute: async (_callId, input) => {
          if (!Value.Check(delegateInput, input)) return toolText('A stable key and a goal are required', true);
          return this.modelResult(await this.delegate(owner.conversationId, input));
        } },
      { name: 'activity_action', label: 'Act on activity', description: 'Stop or continue the exact activity requested by the user.', parameters: change,
        execute: async (callId, input) => {
          if (!Value.Check(change, input)) return toolText('An activity ID and action are required', true);
          const activity = this.scopedActivity(input.activity_id, undefined, owner);
          const executionId = activity.executionIds.at(-1);
          const source = this.conversation(this.profile, owner.conversationId);
          const operationId = `${source.latestExecutionId}:${callId}`;
          if (input.action === 'stop') {
            if (!executionId) return toolText('The activity has no execution', true);
            return this.modelResult(this.stop('activity.stop', { operationId, executionId }, { activityId: activity.activityId }));
          }
          return this.modelResult(await this.continueActivity({ operationId, activityId: activity.activityId, expectedRevision: activity.revision,
            text: input.text ?? '', materials: this.execution(this.profile, source.latestExecutionId)?.input.materials ?? [] }));
        } },
    ];
    if (architect) tools.push(...this.architectTools(owner.conversationId));
    tools.push(...this.memoryTools(owner.conversationId), ...this.skillTools(owner.conversationId));
    return tools.sort((a, b) => a.name.localeCompare(b.name)).map(tool => ({ ...tool, execute: async (...args) => {
      try { return await tool.execute(...args); }
      catch (error) {
        if (!(error instanceof RequestError)) throw error;
        return toolText({ code: error.code, message: this.engine.cleanMessage(error), ...(error.details ? { details: this.modelProjection(error.details) } : {}) }, true);
      }
    } }));
  }

  private memoryTools(conversationId: string): ToolDefinition[] {
    const none = Type.Object({}, { additionalProperties: false });
    const ruleId = Type.Object({ rule_id: requiredText }, { additionalProperties: false });
    const rule = Type.Object({ text: memoryText }, { additionalProperties: false });
    const proposal = Type.Object({ text: memoryText, reason: Type.String({ minLength: 1, maxLength: MAX_LOG_BYTES, pattern: '\\S' }) }, { additionalProperties: false });
    const log = Type.Object({ summary: memoryText, date: Type.Optional(memoryDate), tags: Type.Optional(memoryTags) }, { additionalProperties: false });
    const search = Type.Object({ query: memorySearchQuery, date_from: Type.Optional(memoryDate), date_to: Type.Optional(memoryDate),
      limit: Type.Optional(memorySearchLimit) }, { additionalProperties: false });
    const scoped = <T extends TSchema>(name: string, description: string, parameters: T,
      action: (args: Static<T>, botId: string, executionId: string, operationId: string) => unknown): ToolDefinition<T> => ({
      name, label: description, description, parameters,
      execute: async (callId, args, signal) => {
        try {
          const source = this.current(this.profile, conversationId);
          const id = source.latestExecutionId;
          const run = id && this.active.get(id);
          if (signal?.aborted || !id || !run) stateError('scope_denied', 'The conversation no longer owns this call');
          if (name === 'memory_save_rule' || name === 'memory_propose_learning') run.learningToolUsed = true;
          if (!Value.Check(parameters, args)) stateError('memory_invalid', 'Invalid memory tool parameters');
          return this.modelResult(action(args, source.botId, id, `${id}:${name}:${callId}`));
        } catch (error) {
          if (!(error instanceof RequestError)) throw error;
          return toolText({ code: error.code, message: this.engine.cleanMessage(error) }, true);
        }
      },
    });
    return [
      scoped('memory_forget_rule', 'Remove a lasting rule by ID when the user asks to forget it.', ruleId, (input, botId, _id, operationId) =>
        this.changeMemory('memory_forget_rule', { ...input, botId, operationId }, memory => this.removeRule(memory, botId, input.rule_id, false))),
      scoped('memory_list_rules', 'Read the saved rules and count of pending learnings for this Bot.', none, (_input, botId) =>
        ({ rules: this.lastingRules(botId), pending_count: this.memoryStatus(this.profile, botId).pendingCount })),
      scoped('memory_log_work', 'Record a concise dated fact or completed work for this Bot.', log, (input, botId, id, operationId) =>
        this.changeMemory('memory_log_work', { ...input, botId, operationId }, (memory, bot, now) =>
          this.saveWorkLog(memory, bot, input.summary, now, input.tags ?? [], id, input.date))),
      scoped('memory_propose_learning', 'Queue an inferred lasting preference for the user to review.', proposal, (input, botId, id, operationId) =>
        this.changeMemory('memory_propose_learning', { ...input, botId, operationId }, (memory, bot, now) =>
          this.proposeLearning(memory, bot, input.text, input.reason, now, id))),
      scoped('memory_save_rule', 'Save a lasting rule when the user explicitly asks you to remember it.', rule, (input, botId, _id, operationId) =>
        this.changeMemory('memory_save_rule', { ...input, botId, operationId }, (memory, bot, now) => this.saveRule(memory, bot, input.text, 'explicit', now))),
      scoped('memory_search_log', 'Search the dates, summaries, and tags of this Bot’s work log.', search, (input, botId) =>
        ({ items: this.searchWorkLogs({ botId, query: input.query, dateFrom: input.date_from, dateTo: input.date_to, limit: input.limit }) })),
    ];
  }

  private architectTools(conversationId: string): ToolDefinition[] {
    const execute = <T extends TSchema>(schema: T, action: (input: Static<T>, executionId: string, callId: string) => unknown): ToolDefinition<T>['execute'] =>
      async (callId, input, signal) => {
        if (!Value.Check(schema, input)) return toolText({ code: 'invalid_params', message: 'Invalid Bot Architect tool parameters' }, true);
        try {
          const source = this.conversation(this.profile, conversationId);
          const bot = this.bot(this.profile, source.botId);
          const executionId = source.latestExecutionId;
          if (signal?.aborted || bot.systemKind !== SYSTEM_KIND_BOT_ARCHITECT || bot.archived || bot.currentConversationId !== conversationId ||
              !executionId || !this.active.has(executionId)) stateError('scope_denied', 'The Bot Architect conversation no longer owns this call');
          return this.modelResult(action(input, executionId, callId));
        } catch (error) {
          if (!(error instanceof RequestError)) throw error;
          return toolText({ code: error.code, message: this.engine.cleanMessage(error) }, true);
        }
      };
    return [
      { name: 'bot_inspect_team', label: 'Inspect Bot Team', description: 'Read the saved profiles of the available Bots before auditing or refining them.', parameters: architectInspect,
        execute: execute(architectInspect, () => this.profile.bots.filter(bot => !bot.archived).map(({ botId, profile, systemKind }) => ({
          bot_id: botId, name: profile.name, role: profile.role, purpose: profile.purpose, instructions: profile.instructions,
          starter_prompts: profile.starterPrompts, system_kind: systemKind ?? null,
          tool_preset: profile.toolPreset, skill_ids: profile.mountedSkills?.map(ref => ref.skillId),
        }))) },
      { name: 'bot_create', label: 'Create specialist Bot', description: 'Create the focused Bot blueprint after the user confirms it.', parameters: architectCreate,
        execute: execute(architectCreate, (input, executionId, callId) => {
          const params = { operationId: `${executionId}:bot_create:${callId}`, input };
          const profile: BotProfile = { name: input.name.trim(), purpose: input.purpose.trim(), role: input.role?.trim() ?? '', workspace: '', codingTools: null,
            ...(input.starter_prompts === undefined ? {} : { starterPrompts: input.starter_prompts }),
            ...(input.tool_preset === undefined ? {} : { toolPreset: input.tool_preset }),
            ...(input.skill_ids === undefined ? {} : { mountedSkills: this.architectMounts(input.skill_ids, '') }),
            instructions: formatTightBotInstructions({ job: input.job, voice: input.voice, antiJobs: input.anti_jobs, workflow: input.workflow }) };
          return architectResult(this.changeBot('bot_create', params, candidate => ({ bot: this.insertBot(candidate, profile), change: 'created' })));
        }) },
      { name: 'bot_update', label: 'Update specialist Bot', description: 'Update a saved specialist Bot after inspecting its profile and getting user confirmation.', parameters: architectUpdate,
        execute: execute(architectUpdate, (input, executionId, callId) => {
          const params = { operationId: `${executionId}:bot_update:${callId}`, input };
          return architectResult(this.changeBot('bot_update', params, candidate => {
            const bot = this.bot(candidate, input.bot_id);
            if (bot.systemKind || bot.archived) stateError('scope_denied', 'Only active specialist Bots can be updated by the Bot Architect');
            const { name, purpose, role, job, voice, anti_jobs, workflow } = input;
            let instructions = input.instructions ?? bot.profile.instructions;
            if (job !== undefined || voice !== undefined || anti_jobs !== undefined || workflow !== undefined) {
              const saved = readTightBotInstructions(bot.profile.instructions);
              instructions = formatTightBotInstructions({ job: job ?? saved.job, voice: voice ?? saved.voice,
                antiJobs: anti_jobs ?? saved.antiJobs, workflow: workflow ?? saved.workflow });
            }
            this.updateBotProfile(candidate, bot, { ...bot.profile, name: name?.trim() ?? bot.profile.name,
              purpose: purpose?.trim() ?? bot.profile.purpose, role: role?.trim() ?? bot.profile.role, instructions,
              ...(input.tool_preset === undefined ? {} : { toolPreset: input.tool_preset, codingTools: null }),
              ...(input.skill_ids === undefined ? {} : { mountedSkills: this.architectMounts(input.skill_ids, bot.profile.workspace) }),
              ...(input.starter_prompts === undefined ? {} : { starterPrompts: input.starter_prompts }) });
            return { bot, change: 'updated' };
          }));
        }) },
    ];
  }

  private async delegate(originConversationId: string, input: Static<typeof delegateInput>) {
    const conversation = this.conversation(this.profile, originConversationId);
    const source = this.execution(this.profile, conversation.latestExecutionId);
    if (!source || !this.active.has(source.executionId)) stateError('scope_denied', 'The conversation execution no longer owns this delegation');
    const operationId = `${source.executionId}:delegate:${input.key}`;
    const { linked_activity_ids, ...handoff } = input;
    const grants = normalizeActivityGrants(linked_activity_ids ?? [], 'invalid_params');
    const params = { originConversationId, ...handoff, ...(grants.length ? { linked_activity_ids: grants } : {}) };
    const activityId = newId();
    for (const id of grants) {
      if (id === activityId) stateError('scope_denied', 'The Activity or execution is outside the requested scope');
      this.scopedActivity(id, undefined, { conversationId: originConversationId });
    }
    const prior = this.sameOperation('activity.delegate', operationId, params);
    if (prior) return this.operation(operationId);
    const bot = this.bot(this.profile, input.assignee_id || conversation.botId);
    if (bot.archived) stateError('scope_denied', 'The receiving Bot is archived');
    const files = input.files ?? [];
    const permitted = new Set([...source.input.materials.map(item => item.source), ...this.visibleActivities(conversation.botId).flatMap(activity => activity.results.flatMap(result => result.files.map(file => file.path)))]);
    if (files.some(path => !isAbsolute(path) || !permitted.has(path))) stateError('scope_denied', 'A delegated input file must come from user-provided Materials or a visible result');
    const activity: Activity = { activityId, ownerBotId: bot.botId, originConversationId, revision: 1, state: 'notStarted',
      ...(grants.length ? { grantedLinkedActivityIds: grants } : {}),
      routing: input.kind || input.complexity || input.constraints?.length ? { kind: input.kind, complexity: input.complexity, constraints: input.constraints } : undefined,
      goal: input.goal, requirements: input.requirements ?? [], questions: [], materials: source.input.materials, inputFiles: files, executionIds: [], results: [] };
    this.commit(candidate => {
      candidate.activities.push(activity);
      const target = { activityId: activity.activityId };
      this.record(candidate, 'activity.delegate', params, { operationId, state: 'accepted', target }, target);
    });
    this.emit('activity.changed', this.activityView(activity));
    return this.startActivity('activity.continue', { activityId: activity.activityId, operationId: `${operationId}:execution`, expectedRevision: 1, text: '', materials: activity.materials });
  }

  private async waitForAnswer(activityId: string, callId: string, text: string, signal?: AbortSignal) {
    const activity = this.activity(this.profile, activityId);
    const executionId = activity.executionIds.at(-1);
    if (!executionId || !this.active.has(executionId) || signal?.aborted) return toolText('The execution is no longer active', true);
    const questionId = `${executionId}:${callId}`;
    const { promise, resolve, reject } = Promise.withResolvers<ToolTextResult>();
    const abort = () => {
      this.waiters.delete(questionId);
      reject(new Error('The question was interrupted'));
    };
    this.commit(candidate => {
      const current = this.activity(candidate, activityId);
      current.questions.push({ questionId, executionId, text, revision: 1, state: 'open' });
      current.state = 'waitingForAnswer';
      current.revision++;
    });
    this.waiters.set(questionId, { resolve: answer => {
      signal?.removeEventListener('abort', abort);
      try {
        this.commit(candidate => {
          const current = this.activity(candidate, activityId);
          const question = this.question(current, questionId);
          question.delivery = 'dispatching';
          question.revision++;
          current.revision++;
        });
        this.emit('activity.changed', this.activityView(this.activity(this.profile, activityId)));
        resolve(this.modelResult(answer));
      } catch (error) {
        reject(error);
      }
    } });
    signal?.addEventListener('abort', abort, { once: true });
    this.emit('activity.changed', this.activityView(this.activity(this.profile, activityId)));
    return promise;
  }
}
