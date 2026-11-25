/// <reference lib="es2024.collection" />

import { randomUUID } from 'node:crypto';
import { closeSync, fstatSync, lstatSync, openSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { extname, isAbsolute } from 'node:path';
import { Type } from 'typebox';
import { Value } from 'typebox/value';
import { RequestError, activityStateSchema, botSystemKindSchema, connectionSchema, draftSchema, executionSchema, hasCanonicalQuoteText, materialSchema, mcpServerRecord,
  operationSchema, profileSchema, quoteAuthorMaxBytes, quoteTextMaxBytes, type ActivityState, type Connection, type Draft,
  type Execution, type Material, type Operation } from './protocol.js';
import type { ProductProfile } from './product.js';
import { activityLifecycleFields, activityProgressRecord, activitySteerReceipt, activitySteerRecord, canonicalJSON, frameBytes,
  maxDraftBytes, questionResult, skillApprovalRecord, skillApprovalRequestSchema, triageAccounting as triageSchema,
  optionDescriptionBytes, optionLabelBytes, questionTextBytes, type ActivityProgressRecord, type ActivitySteerRecord,
  type SkillApproval, type SkillApprovalRequest, type TriageAccounting } from './protocol.js';
import { lastingRulesFit, maxLogBytes, maxPendingLearnings, maxRuleBytes, maxWorkLogs,
  memorySnapshotSchema, validMemoryDate } from './bot-memory.js';
import { decodeTraceBackup, directlyOwns, normalizeActivityGrants, type TraceBackup } from './product-trace.js';
import { backupSkillFiles, bundleDigest, executableBundle, privateSkillBundleSchema, pureSkill, skillGrantKey, validateSkillBundles, type PrivateSkillBundle } from './bot-skills.js';
import { activityRouting, capturedRoute, escalationSchema, policySchema, privateProviderAccount, providerAccount, validProvider, type Escalation, type ProviderAccount } from './model-router.js';
import { maxMcpServers, uniqueMcpRegistry, validMcpBindings, validMcpServer } from './mcp-broker.js';
import { clipUTF8 } from './product-redactor.js';
import { triageDomain, validateQuestionFields } from './activity-control.js';

export type ProductProfilePaths = {
  profilePath: string;
  credentialsPath: string;
  fresh: boolean;
};

export type ProductBackup = TraceBackup & { privateSkillBundles?: PrivateSkillBundle[]; skillApprovals?: SkillApproval[];
  activityEscalations?: Escalation[]; activityProgress?: ActivityProgressRecord[]; skillApprovalRequests?: SkillApprovalRequest[];
  triageAccounting?: TriageAccounting[]; activitySteers?: ActivitySteerRecord[] };

type Credential = { api: 'openai-responses' | 'openai-chat-completions'; endpoint: string; api_key: string };
type LegacyConnection = {
  credential_source: string; model: string; context_tokens: number; output_tokens: number;
  working_directory: string; max_active_tasks: number;
};
type PrivateProviderCredential = Omit<ProviderAccount, 'enabledModels' | 'fallbackOnly' | 'modelOverrides'> & { apiKey: string };

export function privateProviderCredential(account: ProviderAccount, apiKey: string): PrivateProviderCredential {
  const { providerId, name, wireApi, baseUrl, models, defaultModel, contextTokens, outputTokens, enabled, credentialSourceId } = account;
  return { providerId, name, wireApi, baseUrl, models, defaultModel, contextTokens, outputTokens, enabled, credentialSourceId, apiKey };
}
export type PrivateConnection = Partial<LegacyConnection> & {
  version: 2 | 3; sources: Record<string, Credential>; accounts?: PrivateProviderCredential[]; publicationId?: string;
};
export const privateCredentialKeys = (store?: PrivateConnection) => [
  ...Object.values(store?.sources ?? {}).map(source => source.api_key), ...store?.accounts?.map(account => account.apiKey) ?? [],
];

export type StoredConnection = {
  revision: number; sourceId: string; configuration: Omit<Connection, 'apiKey'>;
};

type OldBinding = {
  engine: string;
  context: { contextId: string; botId: string; purpose: string; activityId?: string; selectionRevision: number; cwd: string; sessionRef: string; used: boolean };
  selection: { cwd: string; instructions: string; coding_tools: string[] | null };
  configuration: { wire_api: Connection['wireApi']; base_url: string; model: string; context_tokens: number; output_tokens: number; credential_source: string };
};

type OldScope = { ref: string; contact_id: string; runtime_session_id: string; runtime_task_id?: string;
  turn_id: string; mode?: string; allowed_issue_ids?: string[] };
type OldContact = {
  id: string; name: string; role?: string; purpose?: string; instructions?: string; workspace?: string; builtin_tools?: string[];
  draft?: string; task_context?: Material[]; archived?: boolean; session_id?: string; previous_pi_bindings?: OldBinding[];
  pi_binding?: OldBinding; pending_send?: { draft?: string; context?: Material[]; state?: string; tool_scope_ref?: string };
  selected_issue_id?: string; archived_at?: number; updated_at?: number;
};
type OldExecution = {
  id: string; task_id?: string; status?: string; detail?: string; prompt?: string; context?: Material[]; tool_operation_id?: string;
  pi_run?: { run_id: string; admitted: boolean }; returned_at?: number; requirements_revision?: number;
};
type OldIssue = {
  id: string; contact_id: string; goal: string; status?: string; requirements_revision?: number; requirement_items?: { text: string }[];
  task_context?: Material[]; archived?: boolean; pi_binding?: OldBinding; executions?: OldExecution[];
  questions?: { ref: string; text: string; requirements_revision: number; source_task_id?: string; answer_text?: string; state: string;
    answer_context?: Material[]; delivery_state?: string; pi_call?: { execution_id: string; run_id?: string } }[];
  handoff?: { source_bot_id: string; source_session_ref: string; source_turn_id: string; files?: string[] };
};
type OldReceipt = { state: string; issue_id?: string; execution_id?: string; question_ref?: string };
type OldProfile = {
  version: 10; selected?: string; contacts: OldContact[]; issues?: OldIssue[];
  issue_tool_scopes?: OldScope[];
  issue_tool_operations?: { operations?: { id: string; action: string; target_issue_id?: string; receipt?: OldReceipt }[] };
  layout?: object; theme_ids?: object; appearance?: string;
};

const recoveryId = Type.String({ minLength: 1 });
const counter = Type.Integer({ minimum: 0, maximum: Number.MAX_SAFE_INTEGER });
const revisionRef = Type.Union([counter, Type.Null()]);
const opaque = Type.Record(Type.String(), Type.Unknown());
const strict = { additionalProperties: false };
const bindingSchema = Type.Object({ contextId: recoveryId, sessionRef: recoveryId, connectionRevision: Type.Optional(counter), route: Type.Optional(capturedRoute),
  profileRevision: counter, cwd: recoveryId, instructions: Type.String(), codingTools: profileSchema.properties.codingTools,
  used: Type.Boolean(), memoryRulesRevision: Type.Optional(counter), lastTurnAt: Type.Optional(recoveryId) }, strict);
const privateSkillBundles = Type.Array(privateSkillBundleSchema, { maxItems: 8192 });
const executionRecord = Type.Object({ ...executionSchema.properties, operationId: recoveryId,
  input: draftSchema, error: Type.Optional(Type.String()), engineRunId: Type.Optional(recoveryId),
  importedActivityId: Type.Optional(recoveryId), retained: Type.Optional(opaque) }, strict);
const questionRecord = Type.Object({ ...questionResult.properties, triageRulesRevision: Type.Optional(counter) }, strict);
const activityRecord = Type.Object({ activityId: recoveryId, ownerBotId: recoveryId, originConversationId: recoveryId,
  routing: Type.Optional(activityRouting),
  revision: counter, state: activityStateSchema, goal: Type.String(), requirements: Type.Array(Type.String()),
  ...activityLifecycleFields,
  updatedAt: Type.Optional(recoveryId),
  questions: Type.Array(questionRecord), materials: Type.Array(materialSchema), inputFiles: Type.Array(Type.String()),
  executionIds: Type.Array(recoveryId), binding: Type.Optional(bindingSchema), retained: Type.Optional(opaque),
  grantedLinkedActivityIds: Type.Optional(Type.Array(recoveryId)),
  results: Type.Array(Type.Object({ activityId: recoveryId, executionId: recoveryId, text: Type.String(), delivery: Type.Literal('available'),
    files: Type.Array(Type.Object({ path: recoveryId, callId: recoveryId }, strict)) }, strict)) }, strict);
const recoveryUISchema = Type.Object({ selected: Type.Optional(recoveryId), layout: opaque, themeIds: opaque,
  appearance: Type.Optional(Type.String()), recoveryId: Type.Optional(recoveryId),
  hideBuiltinArchitect: Type.Optional(Type.Boolean()),
  pending: Type.Optional(Type.Record(recoveryId, Type.Object({ conversationId: recoveryId, operationId: recoveryId }, strict))),
  pending_activities: Type.Optional(Type.Record(recoveryId, Type.Object({ operation_id: recoveryId, bot_id: recoveryId, activity_id: recoveryId,
    action: Type.Union([Type.Literal('continue'), Type.Literal('answer'), Type.Literal('stop')]), revision: counter,
    execution_id: Type.Optional(recoveryId), question_id: Type.Optional(recoveryId), text: Type.Optional(Type.String()),
    materials: Type.Optional(Type.Array(materialSchema)) }, strict))) }, strict);
const savedProfileSchema = Type.Object({ version: Type.Literal(1),
  bots: Type.Array(Type.Object({ botId: recoveryId, profile: profileSchema, profileRevision: counter, archived: Type.Boolean(),
    currentConversationId: recoveryId, systemKind: Type.Optional(botSystemKindSchema), retained: Type.Optional(opaque) }, strict)),
  conversations: Type.Array(Type.Object({ conversationId: recoveryId, botId: recoveryId, revision: counter, draft: draftSchema,
    binding: Type.Optional(bindingSchema), latestExecutionId: Type.Optional(recoveryId) }, strict)),
  activities: Type.Array(activityRecord), executions: Type.Array(executionRecord),
  operations: Type.Array(Type.Object({ method: recoveryId, fingerprint: Type.String(), receipt: operationSchema, reply: opaque,
    replyError: Type.Optional(Type.Object({ code: recoveryId, message: Type.String(), details: Type.Optional(opaque) }, strict)),
    connectionRevision: Type.Optional(counter), retained: Type.Optional(opaque) }, strict)),
  connections: Type.Array(Type.Object({ revision: counter, sourceId: recoveryId, configuration: Type.Omit(connectionSchema, ['apiKey'], strict) }, strict)),
  savedRevision: revisionRef, appliedRevision: revisionRef,
  preferences: Type.Optional(Type.Object({ selectedBotId: Type.Optional(recoveryId), layout: Type.Optional(opaque),
    themeIds: Type.Optional(opaque), appearance: Type.Optional(Type.String()), hideBuiltinArchitect: Type.Optional(Type.Boolean()),
    discoverLocalSkills: Type.Optional(Type.Boolean()) }, strict)),
  mcpServers: Type.Optional(Type.Array(mcpServerRecord, { maxItems: maxMcpServers })),
  providerAccounts: Type.Optional(Type.Array(providerAccount, { maxItems: 64 })),
  providerEffectiveAccounts: Type.Optional(Type.Array(providerAccount, { maxItems: 64 })),
  routingPolicy: Type.Optional(policySchema), providerEffectivePolicy: Type.Optional(policySchema),
  providerSavedRevision: Type.Optional(counter), providerEffectiveRevision: Type.Optional(counter),
  issueToolScopes: Type.Optional(Type.Array(opaque)), untargetedReceipts: Type.Optional(Type.Array(opaque)),
  recovery: Type.Optional(Type.Object({ id: recoveryId, ui: Type.Optional(recoveryUISchema) }, strict)),
  memory: Type.Optional(memorySnapshotSchema),
}, strict);

export function decodeProductProfile(data: unknown): ProductProfile {
  if (!Value.Check(savedProfileSchema, data)) {
    throw new RequestError('profile_unavailable', 'The saved product profile has an unsupported record shape');
  }
  const invalid = (): never => { throw new RequestError('profile_unavailable', 'The saved product profile has an invalid record association'); };
  const admitDraftQuote = (draft: Draft) => {
    if (draft.quote === null) delete draft.quote;
    else if (draft.quote && !hasCanonicalQuoteText(draft.quote)) {
      throw new RequestError('profile_unavailable', 'The saved product profile has an invalid quote');
    }
  };
  if (data.mcpServers) {
    if (!uniqueMcpRegistry(data.mcpServers) || data.mcpServers.some(({ catalog, ...server }) => !validMcpServer(server))) invalid();
    if (data.mcpServers.length === 0) delete data.mcpServers;
  }
  for (const accounts of [data.providerAccounts, data.providerEffectiveAccounts]) {
    if (accounts && (new Set(accounts.map(row => row.providerId)).size !== accounts.length || accounts.some(row => !validProvider(row)))) invalid();
  }
  if (data.providerSavedRevision === undefined ? !!(data.providerAccounts || data.routingPolicy || data.providerEffectiveRevision) :
      data.providerSavedRevision < 1 || (data.providerEffectiveRevision ?? 0) > data.providerSavedRevision) invalid();
  if (data.providerEffectiveRevision === undefined && (data.providerEffectiveAccounts || data.providerEffectivePolicy)) invalid();
  const serverIds = new Set(data.mcpServers?.map(server => server.serverId));
  const bots = new Map(data.bots.map(bot => [bot.botId, bot]));
  for (const { profile } of data.bots) {
    const mounted = profile.mountedSkills ?? [];
    if (new Set(mounted.map(ref => `${ref.source}:${ref.skillId}`)).size !== mounted.length) invalid();
    if (profile.mcpBindings) {
      if (!validMcpBindings(profile.mcpBindings) || profile.mcpBindings.some(binding => !serverIds.has(binding.serverId))) invalid();
      if (!profile.mcpBindings.length) delete profile.mcpBindings;
    }
  }
  const conversations = new Map(data.conversations.map(conversation => [conversation.conversationId, conversation]));
  const executions = new Map(data.executions.map(execution => [execution.executionId, execution]));
  const operations = new Map(data.operations.map(operation => [operation.receipt.operationId, operation]));
  const activities = new Map(data.activities.map(activity => [activity.activityId, activity]));
  const connections = new Set(data.connections.map(connection => connection.revision));
  if (bots.size !== data.bots.length || conversations.size !== data.conversations.length || activities.size !== data.activities.length ||
      executions.size !== data.executions.length || operations.size !== data.operations.length || connections.size !== data.connections.length ||
      [data.savedRevision, data.appliedRevision].some(revision => revision !== null && !connections.has(revision))) invalid();
  type Owner = typeof data.conversations[number] | typeof data.activities[number];
  const owners = new Map<string, Owner>();
  const assignOwner = (executionId: string, owner: Owner | undefined) => {
    const previous = owners.get(executionId);
    if (!owner || !executions.has(executionId) || (previous && previous !== owner)) return invalid();
    owners.set(executionId, owner);
  };
  for (const { receipt: { target, execution } } of data.operations) {
    if (!execution) continue;
    const owner = 'conversationId' in target ? conversations.get(target.conversationId) :
      'activityId' in target ? activities.get(target.activityId) : undefined;
    assignOwner(execution.executionId, owner);
  }
  for (const execution of data.executions) {
    admitDraftQuote(execution.input);
    if (execution.importedActivityId) assignOwner(execution.executionId, activities.get(execution.importedActivityId));
    if (execution.retained?.id === execution.executionId) continue;
    const admission = operations.get(execution.operationId);
    if (!admission || admission.receipt.execution?.executionId !== execution.executionId) return invalid();
    const owner = owners.get(execution.executionId);
    const isActivityStart = admission.method === 'activity.continue';
    const isConversationSend = admission.method === 'conversation.send' || admission.method === 'imported.conversation.send';
    if (!owner || ('activityId' in owner ? !isActivityStart : !isConversationSend)) invalid();
  }
  const linkedExecutions = new Set<string>();
  const linkExecution = (executionId: string, owner: Owner) => {
    if (linkedExecutions.has(executionId) || (!owners.has(executionId) && 'conversationId' in owner)) invalid();
    assignOwner(executionId, owner);
    linkedExecutions.add(executionId);
  };
  const contextIds = new Set<string>();
  const sessionRefs = new Set<string>();
  const binding = (value?: typeof data.conversations[number]['binding']) => {
    if (!value) return;
    if ((value.route ? value.connectionRevision !== undefined || value.route.effectiveRevision > (data.providerEffectiveRevision ?? 0) || !validProvider({
      ...value.route, name: value.route.providerId, defaultModel: value.route.resolvedModel, enabled: true,
    }) : value.connectionRevision === undefined || !connections.has(value.connectionRevision)) ||
      contextIds.has(value.contextId) || sessionRefs.has(value.sessionRef)) invalid();
    contextIds.add(value.contextId);
    sessionRefs.add(value.sessionRef);
  };
  for (const bot of data.bots) {
    if (conversations.get(bot.currentConversationId)?.botId !== bot.botId) invalid();
  }
  for (const conversation of data.conversations) {
    admitDraftQuote(conversation.draft);
    if (!bots.has(conversation.botId)) invalid();
    if (conversation.latestExecutionId) linkExecution(conversation.latestExecutionId, conversation);
    binding(conversation.binding);
  }
  for (const activity of data.activities) {
    const ids = new Set(activity.executionIds);
    const questionIds = new Set(activity.questions.map(question => question.questionId));
    if (!bots.has(activity.ownerBotId) || !conversations.has(activity.originConversationId) || ids.size !== activity.executionIds.length ||
        questionIds.size !== activity.questions.length ||
        activity.questions.some(question => !ids.has(question.executionId)) ||
        activity.results.some(result => result.activityId !== activity.activityId || !ids.has(result.executionId))) invalid();
    if (activity.updatedAt !== undefined && !Number.isFinite(Date.parse(activity.updatedAt))) invalid();
    for (const question of activity.questions) {
      if (question.options !== undefined || question.allowFreeText !== undefined || question.requiresUserDecision !== undefined || question.preferenceKey !== undefined) {
        try { validateQuestionFields(question.text, question.options, question.allowFreeText); }
        catch { invalid(); }
      }
      const selection = question.options?.find(row => row.id === question.selectedOptionId);
      if (question.selectedOptionId !== undefined && (!selection || !question.answer) ||
          question.allowFreeText === false && question.answer && (!selection || selection.label !== question.answer.text) ||
          question.provenance !== undefined && (!question.answer || question.state !== 'answered') ||
          (question.provenance === 'bot_auto' ? !question.evidenceId || !question.evidenceRevision || !selection ||
            question.answer?.text !== selection.label || question.answer.materials.length !== 0 :
            question.evidenceId !== undefined || question.evidenceRevision !== undefined) ||
          question.state === 'triage_pending' && (question.triageRulesRevision === undefined || question.answer || !triageDomain(question) ||
            !['starting', 'running', 'unknown'].includes(executions.get(question.executionId)?.state ?? ''))) invalid();
    }
    if ((activity.acknowledgedAt === undefined) !== (activity.acknowledgedExecutionId === undefined) ||
        activity.acknowledgedAt !== undefined && (!Number.isFinite(Date.parse(activity.acknowledgedAt)) ||
          activity.acknowledgedExecutionId !== activity.executionIds.at(-1) ||
          ['starting', 'running'].includes(executions.get(activity.acknowledgedExecutionId!)?.state ?? '')) ||
        activity.closedAt !== undefined && (!Number.isFinite(Date.parse(activity.closedAt)) ||
          activity.state === 'running' || activity.state === 'waitingForAnswer')) invalid();
    if (activity.grantedLinkedActivityIds !== undefined) {
      const originBotId = conversations.get(activity.originConversationId)!.botId;
      const grants = normalizeActivityGrants(activity.grantedLinkedActivityIds, 'profile_unavailable');
      for (const id of grants) {
        const target = activities.get(id);
        if (id === activity.activityId || !target || !directlyOwns(target, originBotId, conversations.get(target.originConversationId)?.botId)) invalid();
      }
      if (grants.length) activity.grantedLinkedActivityIds = grants;
      else delete activity.grantedLinkedActivityIds;
    }
    for (const id of ids) linkExecution(id, activity);
    binding(activity.binding);
  }
  for (const id of executions.keys()) {
    const owner = owners.get(id);
    if (!owner || ('activityId' in owner && !linkedExecutions.has(id))) invalid();
  }
  if (data.memory) {
    const { rules, workLogs, pendingLearnings, meta } = data.memory;
    const knownMeta = new Map(meta.map(row => [row.botId, row]));
    const foreign = (row: { botId: string; executionId?: string }) => {
      if (!bots.has(row.botId)) return true;
      if (row.executionId === undefined) return false;
      const owner = owners.get(row.executionId);
      return !owner || ('conversationId' in owner ? owner.botId : owner.ownerBotId) !== row.botId;
    };
    if (knownMeta.size !== meta.length || meta.some(foreign) ||
        new Set(rules.map(row => row.ruleId)).size !== rules.length || rules.some(row => foreign(row) || Buffer.byteLength(row.text) > maxRuleBytes) ||
        new Set(workLogs.map(row => row.logId)).size !== workLogs.length || workLogs.some(row => foreign(row) || !validMemoryDate(row.date) || Buffer.byteLength(row.summary) > maxLogBytes) ||
        new Set(pendingLearnings.map(row => row.learningId)).size !== pendingLearnings.length ||
        pendingLearnings.some(row => foreign(row) || (row.state === 'pending') !== (row.reviewedAt === undefined))) invalid();
    const rulesByBot = Map.groupBy(rules, row => row.botId);
    const logsByBot = Map.groupBy(workLogs, row => row.botId);
    const pendingByBot = Map.groupBy(pendingLearnings.filter(row => row.state === 'pending'), row => row.botId);
    for (const [botId, owned] of rulesByBot) {
      if (!knownMeta.get(botId)?.rulesRevision || !lastingRulesFit(owned)) invalid();
    }
    if ([...logsByBot.values()].some(items => items.length > maxWorkLogs) ||
        [...pendingByBot.values()].some(items => items.length > maxPendingLearnings)) invalid();
    for (const target of [...data.conversations, ...data.activities]) {
      const botId = 'botId' in target ? target.botId : target.ownerBotId;
      if ((target.binding?.memoryRulesRevision ?? 0) > (knownMeta.get(botId)?.rulesRevision ?? 0)) invalid();
    }
  } else if ([...data.conversations, ...data.activities].some(row => (row.binding?.memoryRulesRevision ?? 0) > 0)) invalid();
  for (const [botId, pending] of Object.entries(data.recovery?.ui?.pending ?? {})) {
    if (conversations.get(pending.conversationId)?.botId !== botId) invalid();
  }
  for (const [id, pending] of Object.entries(data.recovery?.ui?.pending_activities ?? {})) {
    const activity = activities.get(id);
    if (!activity || pending.activity_id !== id ||
        (pending.bot_id !== activity.ownerBotId && pending.bot_id !== conversations.get(activity.originConversationId)?.botId) ||
        (pending.action === 'continue' ? pending.execution_id !== undefined : !pending.execution_id || !activity.executionIds.includes(pending.execution_id)) ||
        (pending.action === 'answer' ? !activity.questions.some(question => question.questionId === pending.question_id && question.executionId === pending.execution_id) : pending.question_id !== undefined)) invalid();
  }
  if (data.recovery?.ui?.selected && !bots.has(data.recovery.ui.selected)) invalid();
  return data;
}

export function decodeProductBackup(data: unknown): { profile: ProductProfile; backup?: ProductBackup } {
  if (data && typeof data === 'object' && 'backup' in data) {
    const { backup, ...records } = data;
    const profile = decodeProductProfile(records);
    if (!backup || typeof backup !== 'object') return { profile, backup: decodeTraceBackup(profile, backup) };
    const { privateSkillBundles: bundles, skillApprovals: approvals, activityEscalations, activityProgress, skillApprovalRequests,
      triageAccounting, activitySteers, ...traces } = backup as Record<string, unknown>;
    const activities = new Map(profile.activities.map(row => [row.activityId, row]));
    const steerOperations = new Map(profile.operations.filter(row => row.method === 'activity.steer').map(row => [row.receipt.operationId, row]));
    if (activitySteers !== undefined && !Value.Check(Type.Array(activitySteerRecord), activitySteers)) {
      throw new RequestError('profile_unavailable', 'The recovery copy has invalid steering receipts');
    }
    const steerIds = new Set<string>();
    for (const row of activitySteers ?? []) {
      const { steerId, operationId, activityId, executionId, state, revision, createdAt, settledAt, payload, fingerprint } = row;
      const operation = steerOperations.get(operationId);
      const { text, materials } = payload;
      const original = { steerId, operationId, activityId, executionId, state: 'pending_enqueue', revision: 1 };
      if (steerIds.has(steerId) || !activities.get(activityId)?.executionIds.includes(executionId) || !operation ||
          operation.fingerprint !== fingerprint || operation.receipt.state !== 'accepted' ||
          'activityId' in operation.receipt.target && operation.receipt.target.activityId !== activityId ||
          !('activityId' in operation.receipt.target) || !Value.Check(activitySteerReceipt, operation.reply) ||
          canonicalJSON(operation.reply) !== canonicalJSON(original) || canonicalJSON(operation.receipt.result) !== canonicalJSON(original) ||
          state === 'pending_enqueue' || state === 'queued' || revision < 2 || revision > 3 ||
          !Number.isFinite(Date.parse(createdAt)) || !settledAt || !Number.isFinite(Date.parse(settledAt)) ||
          !text.trim() || text !== text.trim() || frameBytes({ revision: 0, ...payload }) > maxDraftBytes ||
          new Set(materials.map(item => item.id)).size !== materials.length) {
        throw new RequestError('profile_unavailable', 'The recovery copy has inconsistent steering receipts');
      }
      steerIds.add(steerId);
      steerOperations.delete(operationId);
    }
    if (steerOperations.size) throw new RequestError('profile_unavailable', 'The recovery copy is missing steering receipts');
    if (triageAccounting !== undefined && (!Value.Check(Type.Array(triageSchema), triageAccounting) ||
        new Set(triageAccounting.map(row => row.activityId)).size !== triageAccounting.length || triageAccounting.some(row =>
          !activities.has(row.activityId) || row.autoAnswersUsed > row.attemptsUsed || !Number.isFinite(Date.parse(row.updatedAt))))) {
      throw new RequestError('profile_unavailable', 'The recovery copy has invalid triage accounting');
    }
    const budgets = new Map((triageAccounting ?? []).map(row => [row.activityId, row]));
    for (const activity of activities.values()) {
      const answers = activity.questions.filter(row => row.provenance === 'bot_auto').length;
      const pending = activity.questions.filter(row => row.state === 'triage_pending').length;
      const budget = budgets.get(activity.activityId);
      if (answers !== (budget?.autoAnswersUsed ?? 0) || answers + pending > (budget?.attemptsUsed ?? 0)) {
        throw new RequestError('profile_unavailable', 'The recovery copy has inconsistent triage accounting');
      }
    }
    if (activityProgress !== undefined && (!Value.Check(Type.Array(activityProgressRecord), activityProgress) ||
        new Set(activityProgress.map(row => row.activityId)).size !== activityProgress.length || activityProgress.some(row =>
          activities.get(row.activityId)?.executionIds.at(-1) !== row.executionId || !Number.isFinite(Date.parse(row.at)) ||
          row.stage !== row.stage.trim() || Buffer.byteLength(row.stage) > 160 || (row.totalSteps ?? 1000) < (row.step ?? 1)))) {
      throw new RequestError('profile_unavailable', 'The recovery copy has invalid activity progress');
    }
    if (skillApprovalRequests !== undefined && (!Value.Check(Type.Array(skillApprovalRequestSchema), skillApprovalRequests) ||
        new Set(skillApprovalRequests.map(row => row.requestId)).size !== skillApprovalRequests.length || skillApprovalRequests.some(row => {
          const activity = activities.get(row.activityId);
          return activity?.ownerBotId !== row.botId || !activity.executionIds.includes(row.executionId) ||
            row.requestId !== `${row.executionId}:${row.callId}` || (row.revision === 1) !== (row.state === 'open') ||
            !Number.isFinite(Date.parse(row.createdAt)) || !isAbsolute(row.canonicalWorkspace);
        }))) {
      throw new RequestError('profile_unavailable', 'The recovery copy has invalid skill approval requests');
    }
    if (activityEscalations !== undefined) {
      if (!Value.Check(Type.Array(escalationSchema), activityEscalations) ||
          new Set(activityEscalations.map(row => row.activityId)).size !== activityEscalations.length) {
        throw new RequestError('profile_unavailable', 'The recovery copy has invalid activity escalations');
      }
      for (const row of activityEscalations) {
        const ids = profile.activities.find(activity => activity.activityId === row.activityId)?.executionIds;
        const prior = ids?.indexOf(row.priorExecutionId) ?? -1;
        const state = profile.executions.find(run => run.executionId === row.priorExecutionId)?.state;
        if (prior < 0 || ids?.[prior + 1] !== row.escalatedExecutionId || !Number.isFinite(Date.parse(row.at)) ||
            state !== 'failed' && state !== 'interrupted') throw new RequestError('profile_unavailable', 'An escalation does not match its executions');
      }
    }
    const botIds = new Set(profile.bots.map(bot => bot.botId));
    if (bundles !== undefined) {
      if (!Value.Check(privateSkillBundles, bundles)) throw new RequestError('profile_unavailable', 'The recovery copy has an invalid private skill list');
      validateSkillBundles(bundles, botIds);
    }
    if (approvals !== undefined) {
      if (!Value.Check(Type.Array(skillApprovalRecord, { maxItems: 8192 }), approvals) ||
          new Set(approvals.map(row => row.grantKey)).size !== approvals.length) {
        throw new RequestError('profile_unavailable', 'The recovery copy has invalid skill approvals');
      }
      const privateFiles = new Map((bundles ?? []).map(row => [JSON.stringify([row.ownerBotId, row.skillId]), backupSkillFiles(row)]));
      for (const row of approvals) {
        const files = privateFiles.get(JSON.stringify([row.ownerBotId, row.skillId]));
        const bundle = row.source === 'private' && files ? executableBundle(files) : undefined;
        if (!botIds.has(row.ownerBotId) || skillGrantKey(row) !== row.grantKey || !Number.isFinite(Date.parse(row.approvedAt)) ||
            row.mode === 'pure_compute_tested' && (!row.testedScripts?.length || row.credentialRefs?.length) ||
            row.source === 'private' && (!bundle || bundleDigest(row.ownerBotId, row.source, row.skillId, bundle.files) !== row.approvalDigest ||
              row.testedScripts?.some(name => !Object.hasOwn(bundle.manifest.scripts, name) || !pureSkill(bundle.manifest.scripts[name].permissions)))) {
          throw new RequestError('profile_unavailable', 'A recovery skill approval does not match its bundle');
        }
      }
    }
    return { profile, backup: { ...decodeTraceBackup(profile, traces), privateSkillBundles: bundles, skillApprovals: approvals, activityEscalations,
      activityProgress, skillApprovalRequests, triageAccounting, activitySteers } };
  }
  return { profile: decodeProductProfile(data) };
}

export function inspectProductRecovery(data: unknown): { contacts: number; activities: number } {
  const { profile, backup } = decodeProductBackup(data);
  if (!backup && profile.operations.some(row => row.method === 'activity.steer')) {
    throw new RequestError('profile_unavailable', 'The recovery copy is missing steering receipts');
  }
  if (!profile.bots.length || profile.executions.some(execution => execution.state === 'running' || execution.state === 'starting') ||
      profile.activities.some(activity => activity.questions.some(question => question.delivery === 'dispatching' || question.state === 'triage_pending'))) {
    throw new RequestError('profile_unavailable', 'The recovery copy contains work that had not reached a saved boundary');
  }
  return { contacts: profile.bots.length, activities: profile.activities.length };
}

function redactBackupProfile(snapshot: ProductProfile & { backup: ProductBackup }, redact: (text: string) => string): void {
  const bounded = (text: string, max: number) => clipUTF8(text.trim(), max, '').trimEnd() || redact('…') || '�';
  const visit = (value: object) => {
    for (const [key, item] of Object.entries(value)) {
      if (item && typeof item === 'object') visit(item);
      else if (typeof item === 'string') {
        const clean = redact(item);
        if (clean !== item) Object.assign(value, { [key]: clean });
      }
    }
  };
  visit(snapshot);
  for (const draft of [...snapshot.conversations.map(row => row.draft), ...snapshot.executions.map(row => row.input)]) {
    if (draft.quote) {
      draft.quote.authorName = bounded(draft.quote.authorName, quoteAuthorMaxBytes);
      draft.quote.text = bounded(draft.quote.text, quoteTextMaxBytes);
    }
  }
  for (const activity of snapshot.activities) {
    if (activity.routing?.constraints) activity.routing.constraints = activity.routing.constraints.map(text => clipUTF8(text, 1024, ''));
    for (const question of activity.questions) {
      if (question.options !== undefined || question.allowFreeText !== undefined || question.requiresUserDecision !== undefined || question.preferenceKey !== undefined) {
        question.text = bounded(question.text, questionTextBytes);
      }
      for (const option of question.options ?? []) {
        option.label = bounded(option.label, optionLabelBytes);
        if (option.description !== undefined) option.description = clipUTF8(option.description, optionDescriptionBytes, '');
      }
      if (question.answer && (question.allowFreeText === false || question.provenance === 'bot_auto')) {
        const option = question.options?.find(row => row.id === question.selectedOptionId);
        if (option) question.answer.text = option.label;
      }
    }
  }
  for (const row of snapshot.backup.activityProgress ?? []) row.stage = bounded(row.stage, 160);
  for (const request of snapshot.backup.skillApprovalRequests ?? []) {
    if (request.reason !== undefined) request.reason = clipUTF8(request.reason, 512, '');
  }
}

export function backupProductProfile(paths: ProductProfilePaths, profile: ProductProfile, backup: ProductBackup, destination: string,
  redact: (text: string) => string, checkSecrets: (snapshot: object) => void): { path: string } {
  if (!isAbsolute(destination) || extname(destination).toLowerCase() !== '.json') {
    throw new RequestError('invalid_params', 'Choose an absolute JSON location for the recovery copy');
  }
  let ui = profile.recovery?.ui;
  try {
    const saved: unknown = JSON.parse(readFileSync(`${paths.profilePath}.ui.json`, 'utf8'));
    if (!Value.Check(recoveryUISchema, saved)) throw new RequestError('profile_unavailable', 'The saved workbench preferences cannot be backed up');
    if (saved.recoveryId === profile.recovery?.id) ui = saved;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
  }
  const snapshot = { ...profile, recovery: { id: randomUUID(), ui }, backup };
  redactBackupProfile(snapshot, redact);
  checkSecrets(snapshot);
  inspectProductRecovery(snapshot);
  const data = Buffer.from(JSON.stringify(snapshot) + '\n');
  const fd = openSync(destination, 'wx', 0o600);
  const identity = fstatSync(fd);
  try {
    try { writeFileSync(fd, data, { flush: true }); }
    finally { closeSync(fd); }
    const saved = lstatSync(destination);
    const readback = readFileSync(destination);
    if (!saved.isFile() || saved.dev !== identity.dev || saved.ino !== identity.ino || !data.equals(readback)) {
      throw new Error('Recovery copy readback did not match the saved profile');
    }
    inspectProductRecovery(JSON.parse(readback.toString('utf8')));
  } catch (error) {
    const current = lstatSync(destination, { throwIfNoEntry: false });
    if (current?.dev === identity.dev && current.ino === identity.ino) rmSync(destination);
    throw error;
  }
  return { path: destination };
}

export function writeProductFile(path: string, value: object): void {
  const temp = `${path}.${randomUUID()}.tmp`;
  try {
    writeFileSync(temp, JSON.stringify(value) + '\n', { flag: 'wx', mode: 0o600, flush: true });
    renameSync(temp, path);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'EEXIST') rmSync(temp, { force: true });
    throw error;
  }
}

export function readPrivateConnection(path: string): PrivateConnection | undefined {
  let data: unknown;
  try { data = JSON.parse(readFileSync(path, 'utf8')); }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
    return;
  }
  const legacy = Type.Object({ credential_source: recoveryId, model: recoveryId, context_tokens: counter, output_tokens: counter,
    working_directory: recoveryId, max_active_tasks: counter });
  const source = Type.Object({ api: Type.Union([Type.Literal('openai-responses'), Type.Literal('openai-chat-completions')]),
    endpoint: recoveryId, api_key: Type.String() });
  const sources = Type.Record(Type.String(), source);
  const v2 = Type.Object({ version: Type.Literal(2), ...legacy.properties, sources });
  const v3 = Type.Object({ version: Type.Literal(3), ...Type.Partial(legacy).properties, sources: Type.Optional(sources),
    publicationId: Type.Optional(recoveryId), accounts: Type.Array(Type.Object({ ...privateProviderAccount.properties, apiKey: recoveryId }, strict)) });
  if (Value.Check(v2, data)) return data;
  if (Value.Check(v3, data) && data.accounts.every(validProvider) &&
      new Set(data.accounts.map(row => row.credentialSourceId)).size === data.accounts.length) return { ...data, sources: data.sources ?? {} };
  throw new RequestError('connection_unavailable', 'The private connection file is not a supported credential store');
}

export function saveCredential(path: string, existing: PrivateConnection | undefined, configuration: Connection): PrivateConnection & LegacyConnection {
  const api = configuration.wireApi === 'openai-responses' ? 'openai-responses' : 'openai-chat-completions';
  const source: Credential = { api, endpoint: configuration.baseUrl, api_key: configuration.apiKey };
  const sourceId = Object.entries(existing?.sources ?? {}).find(([, value]) => value.api === source.api && value.endpoint === source.endpoint && value.api_key === source.api_key)?.[0] ?? randomUUID();
  const next: PrivateConnection & LegacyConnection = {
    version: existing?.version ?? 2, accounts: existing?.accounts, credential_source: sourceId, model: configuration.model, context_tokens: configuration.contextTokens,
    output_tokens: configuration.outputTokens, working_directory: configuration.workingDirectory, max_active_tasks: configuration.maxActiveTasks,
    sources: { ...existing?.sources, [sourceId]: source },
  };
  writeProductFile(path, next);
  return next;
}

export function importProductSnapshot(data: unknown, credentials: PrivateConnection | undefined): { profile: ProductProfile; backup?: ProductBackup } {
  const empty: ProductProfile = {
    version: 1, bots: [], conversations: [], activities: [], executions: [], operations: [], connections: [], savedRevision: null, appliedRevision: null,
  };
  if (data === undefined) return { profile: empty };
  if (!data || typeof data !== 'object' || !('version' in data)) throw new RequestError('profile_unavailable', 'The product profile has an invalid format');
  if (data.version === 1) return decodeProductBackup(data);
  if (data.version !== 10 || !('contacts' in data) || !Array.isArray(data.contacts) || !data.contacts.length) {
    throw new RequestError('profile_unavailable', 'The product profile format is unsupported');
  }
  return { profile: decodeProductProfile(importCurrentProfile(data as OldProfile, empty, credentials)) };
}

function importCurrentProfile(old: OldProfile, profile: ProductProfile, credentials: PrivateConnection | undefined): ProductProfile {
  profile.preferences = { selectedBotId: old.selected, layout: old.layout, themeIds: old.theme_ids, appearance: old.appearance };
  profile.issueToolScopes = old.issue_tool_scopes;
  if (credentials?.credential_source) {
    const defaultSource = credentials.sources[credentials.credential_source];
    if (!defaultSource) throw new RequestError('connection_unavailable', 'The saved credential source is missing');
    profile.connections.push({ revision: 1, sourceId: credentials.credential_source, configuration: {
      wireApi: defaultSource.api === 'openai-responses' ? 'openai-responses' : 'openai-completions', baseUrl: defaultSource.endpoint,
      model: credentials.model!, contextTokens: credentials.context_tokens!, outputTokens: credentials.output_tokens!,
      workingDirectory: credentials.working_directory!, maxActiveTasks: credentials.max_active_tasks!,
    } });
    profile.savedRevision = 1;
    profile.appliedRevision = 1;
  }
  const botIds = new Set<string>();
  const contextIds = new Set<string>();
  const sessionRefs = new Set<string>();
  const conversationsBySession = new Map<string, string>();
  const activeConversationByBot = new Map<string, string>();
  const admitBinding = (oldBinding: OldBinding | undefined, botId: string, activityId?: string) => {
    if (!oldBinding) return;
    const { context, configuration, selection } = oldBinding;
    if (oldBinding.engine !== 'pi' || !context.contextId || !context.sessionRef || contextIds.has(context.contextId) || sessionRefs.has(context.sessionRef) ||
        context.botId !== botId || (activityId ? context.purpose !== 'activity' || context.activityId !== activityId : context.purpose !== 'conversation')) {
      throw new RequestError('profile_unavailable', 'The profile contains an invalid Pi binding');
    }
    const source = credentials?.sources[configuration.credential_source];
    const wireApi = source?.api === 'openai-responses' ? 'openai-responses' : 'openai-completions';
    if (!source?.api_key || source.endpoint !== configuration.base_url || wireApi !== configuration.wire_api) {
      throw new RequestError('connection_unavailable', 'A captured Pi credential source is unavailable');
    }
    contextIds.add(context.contextId);
    sessionRefs.add(context.sessionRef);
    const connection = profile.connections.find(item => item.sourceId === configuration.credential_source && item.configuration.model === configuration.model &&
      item.configuration.contextTokens === configuration.context_tokens && item.configuration.outputTokens === configuration.output_tokens);
    const revision = connection?.revision ?? (profile.connections.at(-1)?.revision ?? 0) + 1;
    if (!connection) profile.connections.push({ revision, sourceId: configuration.credential_source, configuration: {
      wireApi, baseUrl: configuration.base_url, model: configuration.model, contextTokens: configuration.context_tokens, outputTokens: configuration.output_tokens,
      workingDirectory: context.cwd, maxActiveTasks: credentials?.max_active_tasks ?? 2,
    } });
    return { contextId: context.contextId, sessionRef: context.sessionRef, connectionRevision: revision, profileRevision: context.selectionRevision,
      cwd: context.cwd || selection.cwd, instructions: selection.instructions, codingTools: selection.coding_tools, used: context.used };
  };
  for (const contact of old.contacts) {
    if (!contact.id || botIds.has(contact.id) || (contact.session_id && contact.pi_binding?.context.sessionRef !== contact.session_id)) throw new RequestError('profile_unavailable', 'The profile contains an invalid Bot');
    botIds.add(contact.id);
    const binding = admitBinding(contact.pi_binding, contact.id);
    const currentConversationId = binding?.contextId ?? randomUUID();
    const draft: Draft = { revision: 0, text: contact.draft ?? '', materials: contact.task_context ?? [] };
    const conversation: ProductProfile['conversations'][number] = { conversationId: currentConversationId, botId: contact.id, revision: 1, draft, binding };
    activeConversationByBot.set(contact.id, currentConversationId);
    if (binding) conversationsBySession.set(binding.sessionRef, currentConversationId);
    if (contact.pending_send) {
      const saved = contact.pending_send;
      const scope = old.issue_tool_scopes?.find(scope => scope.ref === saved.tool_scope_ref);
      const operationId = scope?.turn_id || randomUUID();
      const executionId = scope?.runtime_task_id || randomUUID();
      const execution: Execution = { executionId, revision: 1, state: 'unknown' };
      conversation.latestExecutionId = executionId;
      profile.executions.push({ ...execution, operationId, input: { revision: 0, text: saved.draft ?? '', materials: saved.context ?? [] }, retained: saved });
      const receipt: Operation = { operationId, target: { conversationId: currentConversationId }, state: 'unknown', execution };
      profile.operations.push({ method: 'imported.conversation.send', fingerprint: '', receipt, reply: receipt });
    }
    profile.conversations.push(conversation);
    for (const oldBinding of contact.previous_pi_bindings ?? []) {
      const previous = admitBinding(oldBinding, contact.id)!;
      profile.conversations.push({ conversationId: previous.contextId, botId: contact.id, revision: 1, draft: { revision: 0, text: '', materials: [] }, binding: previous });
      conversationsBySession.set(previous.sessionRef, previous.contextId);
    }
    profile.bots.push({ botId: contact.id, currentConversationId, profileRevision: Math.max(1, binding?.profileRevision ?? 1), archived: !!contact.archived,
      profile: { name: contact.name, role: contact.role ?? '', purpose: contact.purpose ?? '', instructions: contact.instructions ?? '', workspace: contact.workspace ?? '', codingTools: contact.builtin_tools ?? null },
      retained: { archivedAt: contact.archived_at, selectedIssueId: contact.selected_issue_id, updatedAt: contact.updated_at } });
  }
  const activityStates: Record<string, ActivityState> = { not_started: 'notStarted', in_progress: 'needsAttention', waiting_user: 'waitingForAnswer', needs_attention: 'needsAttention', completed: 'completed', canceled: 'cancelled' };
  const activityIds = new Set<string>();
  const executionIds = new Set(profile.executions.map(execution => execution.executionId));
  for (const issue of old.issues ?? []) {
    const handoff = issue.handoff;
    const originConversationId = handoff ? conversationsBySession.get(handoff.source_session_ref) : activeConversationByBot.get(issue.contact_id);
    const origin = profile.conversations.find(conversation => conversation.conversationId === originConversationId);
    const sourceScope = handoff && old.issue_tool_scopes?.find(scope => scope.mode !== 'work' && scope.contact_id === handoff.source_bot_id &&
      scope.runtime_session_id === handoff.source_session_ref && scope.turn_id === handoff.source_turn_id && scope.allowed_issue_ids?.includes(issue.id));
    if (!origin || (handoff && (origin.botId !== handoff.source_bot_id || !handoff.source_turn_id || !sourceScope)) ||
        !botIds.has(issue.contact_id) || !issue.id || activityIds.has(issue.id)) {
      throw new RequestError('profile_unavailable', 'The profile contains an activity without a verified source');
    }
    const binding = admitBinding(issue.pi_binding, issue.contact_id, issue.id);
    activityIds.add(issue.id);
    const execs = issue.executions ?? [];
    for (const execution of execs) {
      if (!execution.id || executionIds.has(execution.id)) throw new RequestError('profile_unavailable', 'The profile contains a duplicate execution');
      executionIds.add(execution.id);
      const state: Execution['state'] = execution.status === 'completed' ? 'completed' : execution.status === 'interrupted' ? 'interrupted' :
        execution.status === 'failed' || execution.status === 'rejected' || execution.status === 'not_dispatched' ? 'failed' : 'unknown';
      profile.executions.push({ executionId: execution.id, operationId: execution.tool_operation_id || randomUUID(), revision: 1, state,
        input: { revision: execution.requirements_revision ?? 0, text: execution.prompt ?? '', materials: execution.context ?? [] },
        engineRunId: execution.pi_run?.run_id ?? execution.task_id, importedActivityId: issue.id, retained: execution });
    }
    const questions = (issue.questions ?? []).map(question => {
      const originalTask = question.pi_call?.run_id ?? question.source_task_id;
      const matchingRuns = originalTask ? execs.filter(execution => execution.task_id === originalTask || execution.pi_run?.run_id === originalTask) : [];
      const callTarget = question.pi_call ? execs.find(execution => execution.id === question.pi_call?.execution_id) : undefined;
      const target = question.pi_call ? callTarget : matchingRuns.length === 1 ? matchingRuns[0] : undefined;
      if (!target || (question.pi_call?.run_id && target.pi_run?.run_id !== question.pi_call.run_id && target.task_id !== question.pi_call.run_id) ||
          !['open', 'answered', 'superseded'].includes(question.state)) {
        throw new RequestError('profile_unavailable', 'An imported question has no verified owning execution');
      }
      const answer = question.state === 'answered' ? { text: question.answer_text ?? '', materials: question.answer_context ?? [] } : undefined;
      let delivery: ProductProfile['activities'][number]['questions'][number]['delivery'];
      switch (question.delivery_state) {
        case undefined:
        case '': break;
        case 'saved': delivery = 'saved'; break;
        case 'resolved': delivery = 'delivered'; break;
        case 'unknown':
        case 'dispatching': delivery = 'unknown'; break;
        case 'canceled': delivery = 'canceled'; break;
        default: throw new RequestError('profile_unavailable', 'The imported question has an unsupported delivery state');
      }
      return { questionId: question.ref, executionId: target.id, text: question.text, state: question.state as 'open' | 'answered' | 'superseded',
        revision: Math.max(1, question.requirements_revision), answer, delivery };
    });
    const { id, contact_id, goal, status, requirements_revision, task_context, pi_binding, executions, questions: oldQuestions, ...retained } = issue;
    profile.activities.push({ activityId: issue.id, ownerBotId: issue.contact_id, originConversationId: origin.conversationId, revision: Math.max(1, issue.requirements_revision ?? 1),
      state: activityStates[issue.status ?? 'not_started'] ?? 'needsAttention', goal: issue.goal, requirements: (issue.requirement_items ?? []).map(item => item.text),
      questions, materials: issue.task_context ?? [], inputFiles: issue.handoff?.files ?? [], executionIds: execs.map(execution => execution.id), results: [], binding,
      retained: { ...retained, questions: oldQuestions } });
  }
  const operationIds = new Set(profile.operations.map(operation => operation.receipt.operationId));
  for (const operation of old.issue_tool_operations?.operations ?? []) {
    if (!operation.id || operationIds.has(operation.id)) throw new RequestError('profile_unavailable', 'The profile contains a duplicate operation receipt');
    operationIds.add(operation.id);
    const historical = operation.receipt;
    const activityId = historical?.issue_id || operation.target_issue_id;
    if (!activityId) {
      (profile.untargetedReceipts ??= []).push(operation);
      continue;
    }
    const execution = profile.executions.find(execution => execution.executionId === historical?.execution_id);
    const receipt: Operation = { operationId: operation.id, target: { activityId, questionId: historical?.question_ref },
      state: historical && ['accepted', 'saved', 'stop_requested'].includes(historical.state) ? 'accepted' : 'unknown',
      ...(execution ? { execution: { executionId: execution.executionId, revision: execution.revision, state: execution.state } } : {}) };
    profile.operations.push({ method: `imported.${operation.action}`, fingerprint: '', receipt, reply: receipt, retained: operation });
  }
  return profile;
}
