import { Type, type Static, type TSchema } from 'typebox';
import { Value } from 'typebox/value';
import type { Writable } from 'node:stream';
import { SYSTEM_KIND_BOT_ARCHITECT } from './bot-template.js';
import { memoryChangedSchema, memoryDate, memoryRuleSchema, memorySearchLimit, memorySearchQuery, memoryText,
  pendingLearningSchema, workLogSchema } from './bot-memory.js';
import { activityRouting, discoveredModel, modelCapabilities, overridesPatchSchema, overridesSchema, policySchema, providerAccountView,
  providerInput, roleStatusSchema, routeSchema, thinkingSchema } from './model-router.js';

export const MAX_FRAME = 8 * 1024 * 1024;
export const id = Type.String({ minLength: 1 });
const stringList = Type.Array(Type.String());
const target = { contextId: id, runId: id };
const content = Type.Array(Type.Union([Type.Object({ type: Type.Literal('text'), text: Type.String() }), Type.Object({ type: Type.Literal('image'), data: Type.String(), mimeType: id })]));
const domainTool = Type.Object({ name: id, description: Type.String(), parameters: Type.Record(Type.String(), Type.Unknown()) });
const mcpServer = Type.Object({ name: id, sourceId: id, config: Type.Record(Type.String(), Type.Unknown()) });
const request = Type.Object({ v: Type.Literal(1), id, method: id, params: Type.Record(Type.String(), Type.Unknown()) });
const positiveInteger = Type.Integer({ minimum: 1, maximum: Number.MAX_SAFE_INTEGER });
const revision = Type.Integer({ minimum: 0, maximum: Number.MAX_SAFE_INTEGER });
export const inspectionPage = {
  cursor: Type.Optional(Type.String()),
  limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 50 })),
  maxBytes: Type.Optional(Type.Integer({ minimum: 2048, maximum: 32768 })),
};
const page = {
  cursor: Type.Optional(id),
  limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 200 })),
};
export const codingToolPresets = {
  none: [], readonly: ['find', 'grep', 'ls', 'read'], workspace: ['bash', 'edit', 'find', 'grep', 'ls', 'read', 'write'],
};
export const toolPresetSchema = Type.Union([Type.Literal('none'), Type.Literal('readonly'), Type.Literal('workspace')]);
export const skillSlugPattern = '^[a-z0-9][a-z0-9-]{0,62}$';
export const skillID = Type.String({ pattern: skillSlugPattern });
const digest = Type.String({ pattern: '^[a-f0-9]{64}$' });
const externalSkillSource = Type.Union([Type.Literal('global'), Type.Literal('workspace'), Type.Literal('local')]);
export const writableSkillSource = Type.Union([Type.Literal('global'), Type.Literal('private')]);
export const skillSource = Type.Union([externalSkillSource, Type.Literal('private')]);
const mountedSkill = Type.Object({ source: externalSkillSource, skillId: skillID, expectedDigest: Type.Optional(digest) }, { additionalProperties: false });
export const skillText = { name: Type.String({ minLength: 1, maxLength: 128 }), description: Type.String({ minLength: 1, maxLength: 256 }),
  content: Type.String({ maxLength: 32768 }) };
const skillStrict = { additionalProperties: false };
export const skillScriptName = Type.String({ pattern: '^[a-z0-9][a-z0-9_-]{0,62}$' });
export const skillScriptPath = Type.String({ pattern: '^scripts/[a-z0-9][a-z0-9_-]{0,62}\\.mjs$' });
export const skillPermissions = Type.Object({ workspaceRead: Type.Optional(Type.Boolean()), workspaceWrite: Type.Optional(Type.Boolean()),
  networkOrigins: Type.Optional(Type.Array(id, { maxItems: 16 })), credentialRefs: Type.Optional(Type.Array(id, { maxItems: 16 })) }, skillStrict);
const skillScript = Type.Object({ entry: skillScriptPath, description: Type.String({ minLength: 1, maxLength: 512 }),
  timeoutMs: Type.Optional(Type.Integer({ minimum: 1, maximum: 30000 })), permissions: skillPermissions }, skillStrict);
const skillScripts = Type.Record(skillScriptName, skillScript, { minProperties: 1, maxProperties: 8 });
export const skillManifest = Type.Object({ version: Type.Literal(1), scripts: skillScripts }, skillStrict);
export const skillExecutable = { manifest: Type.Optional(Type.Union([skillManifest, Type.Null()])),
  scripts: Type.Optional(Type.Record(skillScriptPath, Type.String({ maxLength: 65536 }), { maxProperties: 8 })) };
const skillApprovalMode = Type.Union([Type.Literal('pure_compute_tested'), Type.Literal('user_approved')]);
const skillApprovalStatus = Type.Union([Type.Literal('none'), skillApprovalMode, Type.Literal('approval_required')]);
export const skillApprovalRecord = Type.Object({ grantKey: digest, ownerBotId: id, source: skillSource, skillId: skillID,
  approvalDigest: digest, permissionsDigest: digest, mode: skillApprovalMode, installationId: id, approvedAt: id,
  testedScripts: Type.Optional(Type.Array(skillScriptName, { maxItems: 8, uniqueItems: true })),
  credentialRefs: Type.Optional(Type.Array(id, { maxItems: 128, uniqueItems: true })) }, skillStrict);
export type SkillManifest = Static<typeof skillManifest>;
export type SkillPermissions = Static<typeof skillPermissions>;
export type SkillApproval = Static<typeof skillApprovalRecord>;
export type SkillApprovalStatus = Static<typeof skillApprovalStatus>;
const skillReview = { scripts: Type.Optional(skillScripts), approvalDigest: Type.Optional(digest), approvalStatus: Type.Optional(skillApprovalStatus),
  canonicalWorkspace: Type.Optional(Type.String()), permissionsDigest: Type.Optional(digest), testedScripts: Type.Optional(Type.Array(skillScriptName, { maxItems: 8 })) };
const skillKey = { source: skillSource, botId: Type.Optional(id), skillId: skillID };
const skillSummary = Type.Object({ source: skillSource, skillId: skillID, name: id, description: id, sha256: digest, ...skillReview });
const skillDetail = Type.Object({ ...skillSummary.properties, content: Type.String(), markdown: Type.String(),
  files: Type.Optional(Type.Record(Type.Union([Type.Literal('SKILL.md'), Type.Literal('manifest.json'), skillScriptPath]), Type.String(), { maxProperties: 10 })) });
const skillListing = Type.Object({ global: Type.Array(skillSummary), workspace: Type.Array(skillSummary), local: Type.Array(skillSummary),
  private: Type.Array(skillSummary), mounted: Type.Array(Type.Object({ ...mountedSkill.properties,
    status: Type.Union([Type.Literal('available'), Type.Literal('missing')]), skill: Type.Optional(skillSummary) })), discoverLocalSkills: Type.Boolean() });
const skillChange = Type.Object({ source: skillSource, botId: Type.Optional(id), skillId: Type.String(),
  change: Type.Union([Type.Literal('saved'), Type.Literal('deleted'), Type.Literal('promoted'), Type.Literal('preferences'),
    Type.Literal('approved'), Type.Literal('revoked'), Type.Literal('tested')]) });
export type SkillSource = Static<typeof skillSource>;
export type MountedSkill = Static<typeof mountedSkill>;
export type SkillDetail = Static<typeof skillDetail>;
export type SkillListing = Static<typeof skillListing>;
export type SkillChange = Static<typeof skillChange>;

export const skillApprovalRequestSchema = Type.Object({ requestId: id, botId: id, activityId: id, executionId: id, callId: id,
  source: skillSource, skillId: skillID, script: skillScriptName, approvalDigest: digest, permissionsDigest: digest,
  canonicalWorkspace: id, installationId: id, permissions: skillPermissions, revision: positiveInteger, createdAt: id,
  state: Type.Union([Type.Literal('open'), Type.Literal('approved'), Type.Literal('denied'), Type.Literal('expired'), Type.Literal('cancelled')]),
  reason: Type.Optional(Type.String({ maxLength: 512 })) }, skillStrict);
export type SkillApprovalRequest = Static<typeof skillApprovalRequestSchema>;

const mcpStrict = { additionalProperties: false };
const mcpServerId = Type.String({ pattern: '^[a-z0-9][a-z0-9_-]{0,62}$' });
const mcpIsolation = Type.Union([Type.Literal('shared'), Type.Literal('per_workspace'), Type.Literal('per_bot')]);
const mcpKeyList = Type.Optional(Type.Array(id, { maxItems: 64, uniqueItems: true }));
const mcpFields = { serverId: mcpServerId, name: Type.String({ pattern: '^[A-Za-z0-9_-]{1,63}$' }), isolation: mcpIsolation, enabled: Type.Boolean() };
const mcpPublicValues = Type.Optional(Type.Record(Type.String(), Type.String(), { maxProperties: 64 }));
const mcpStdio = { ...mcpFields, transport: Type.Literal('stdio'), command: id, args: Type.Optional(stringList), cwd: Type.Optional(id),
  envKeys: mcpKeyList, secretEnvKeys: mcpKeyList, env: mcpPublicValues };
const mcpHttp = { ...mcpFields, transport: Type.Literal('http'), url: id, headerKeys: mcpKeyList, secretHeaderKeys: mcpKeyList, headers: mcpPublicValues };
const mcpServerInput = Type.Union([Type.Object({ ...mcpStdio, revision: Type.Optional(revision) }, mcpStrict),
  Type.Object({ ...mcpHttp, revision: Type.Optional(revision) }, mcpStrict)]);
const mcpTool = Type.Object({ name: id, description: Type.String(), inputSchema: Type.Optional(Type.Unknown()),
  readOnlyHint: Type.Optional(Type.Boolean()), destructiveHint: Type.Optional(Type.Boolean()) }, mcpStrict);
const mcpCatalog = Type.Object({ tools: Type.Array(mcpTool, { maxItems: 128 }), fetchedAt: id, error: Type.Optional(id) }, mcpStrict);
const mcpRecordFields = { revision, catalog: Type.Optional(mcpCatalog) };
export const mcpServerRecord = Type.Union([Type.Object({ ...mcpStdio, ...mcpRecordFields }, mcpStrict),
  Type.Object({ ...mcpHttp, ...mcpRecordFields }, mcpStrict)]);
const mcpPool = Type.Object({ serverId: mcpServerId, scopeKey: id, isolation: mcpIsolation,
  state: Type.Union([Type.Literal('connecting'), Type.Literal('connected'), Type.Literal('closing')]), refCount: revision, pid: Type.Optional(positiveInteger) }, mcpStrict);
const mcpViewFields = { ...mcpRecordFields, credentialStatus: Type.Union([Type.Literal('ok'), Type.Literal('credential_missing')]), pools: Type.Array(mcpPool) };
const mcpServerView = Type.Union([Type.Object({ ...mcpStdio, ...mcpViewFields }, mcpStrict), Type.Object({ ...mcpHttp, ...mcpViewFields }, mcpStrict)]);
const mcpSecretValues = Type.Optional(Type.Union([Type.Record(Type.String(), Type.Union([Type.String(), Type.Null()]), { maxProperties: 64 }), Type.Null()]));
const mcpSecretInput = Type.Optional(Type.Union([Type.Object({ env: mcpSecretValues, headers: mcpSecretValues }, mcpStrict), Type.Null()]));
const mcpTestResult = Type.Object({ ok: Type.Boolean(), tools: Type.Array(mcpTool), error: Type.Optional(id), latencyMs: revision }, mcpStrict);
const mcpChange = Type.Object({ serverId: mcpServerId,
  change: Type.Union([Type.Literal('saved'), Type.Literal('deleted'), Type.Literal('tested')]) }, mcpStrict);
export const mcpBinding = Type.Object({ serverId: mcpServerId, policy: Type.Union([Type.Literal('all'), Type.Literal('readonly'), Type.Literal('allowlist')]),
  allowedTools: Type.Optional(Type.Array(id, { maxItems: 128, uniqueItems: true })) }, mcpStrict);
export type McpBinding = Static<typeof mcpBinding>;
export type McpServerInput = Static<typeof mcpServerInput>;
export type McpServerRecord = Static<typeof mcpServerRecord>;
export type McpServerView = Static<typeof mcpServerView>;
export type McpPoolView = Static<typeof mcpPool>;
export type McpTool = Static<typeof mcpTool>;
export type McpSecretInput = Static<typeof mcpSecretInput>;
export type McpTestResult = Static<typeof mcpTestResult>;

export const profileSchema = Type.Object({
  name: id, role: Type.String(), purpose: Type.String(), instructions: Type.String(),
  workspace: Type.String(), codingTools: Type.Union([Type.Null(), stringList]),
  starterPrompts: Type.Optional(Type.Array(Type.String())),
  toolPreset: Type.Optional(Type.Union([toolPresetSchema, Type.Literal('custom')])),
  mountedSkills: Type.Optional(Type.Array(mountedSkill, { maxItems: 200 })),
  mcpBindings: Type.Optional(Type.Array(mcpBinding, { maxItems: 64 })),
});
export const botSystemKindSchema = Type.Literal(SYSTEM_KIND_BOT_ARCHITECT);
const botResult = Type.Object({ botId: id, profile: profileSchema, profileRevision: revision, archived: Type.Boolean(),
  pendingLearnings: Type.Optional(revision),
  currentConversationId: id, systemKind: Type.Optional(botSystemKindSchema) });
const botChangeSchema = Type.Union([Type.Literal('created'), Type.Literal('updated'), Type.Literal('archived'), Type.Literal('restored')]);
export type BotChange = Static<typeof botChangeSchema>;
export const connectionSchema = Type.Object({
  wireApi: Type.Union([Type.Literal('openai-completions'), Type.Literal('openai-responses')]),
  baseUrl: id, apiKey: Type.String(), model: id, contextTokens: positiveInteger, outputTokens: positiveInteger,
  workingDirectory: id, maxActiveTasks: positiveInteger,
});
export const activityStateSchema = Type.Union([
  Type.Literal('notStarted'), Type.Literal('running'), Type.Literal('waitingForAnswer'),
  Type.Literal('needsAttention'), Type.Literal('completed'), Type.Literal('cancelled'),
]);
const progressStep = Type.Integer({ minimum: 1, maximum: 1000 });
const progressFields = { executionId: id, progressSeq: positiveInteger, stage: Type.String({ minLength: 1, maxLength: 160 }),
  step: Type.Optional(progressStep), totalSteps: Type.Optional(progressStep), at: id };
const activityProgress = Type.Object(progressFields);
export const activityProgressRecord = Type.Object({ ...progressFields, activityId: id, callId: id }, { additionalProperties: false });
export type ActivityProgressRecord = Static<typeof activityProgressRecord>;
export const activityLifecycleFields = { acknowledgedAt: Type.Optional(id), acknowledgedExecutionId: Type.Optional(id), closedAt: Type.Optional(id) };
const activityFields = { activityId: id, ownerBotId: id, originConversationId: id, revision, state: activityStateSchema,
  goal: Type.String(), openQuestionIds: Type.Array(id), ...activityLifecycleFields,
  latestProgress: Type.Optional(activityProgress), pendingApprovalRequestIds: Type.Optional(Type.Array(id)) };
const approvalRequests = { approvalRequests: Type.Optional(Type.Array(skillApprovalRequestSchema)) };
const steerState = Type.Union([Type.Literal('pending_enqueue'), Type.Literal('queued'), Type.Literal('delivered'),
  Type.Literal('not_applied'), Type.Literal('unknown'), Type.Literal('rejected')]);
export const activitySteerReceipt = Type.Object({ operationId: id, steerId: id, activityId: id, executionId: id,
  state: steerState, revision: positiveInteger, reason: Type.Optional(id) }, skillStrict);
export type ActivitySteer = Static<typeof activitySteerReceipt>;
const steers = { steers: Type.Optional(Type.Array(activitySteerReceipt)) };
const activityDigest = Type.Object({ factId: id, activityId: id, ownerBotId: id, originConversationId: id,
  executionId: Type.Optional(id), revision, summary: Type.String(), fileCount: Type.Optional(revision), at: id,
  questionId: Type.Optional(id), requestId: Type.Optional(id),
  kind: Type.Union([Type.Literal('completed'), Type.Literal('needs_attention'), Type.Literal('question_open'),
    Type.Literal('question_auto_saved'), Type.Literal('approval_required')]) });
export type ActivityDigest = Static<typeof activityDigest>;
const targetSchema = Type.Union([
  Type.Object({ conversationId: id }), Type.Object({ botId: id, sessionId: Type.Optional(id) }),
  Type.Object({ activityId: id, questionId: Type.Optional(id) }), Type.Object({ connection: Type.Literal(true) }),
]);
const operationTarget = { operationId: id, expectedRevision: revision };
export const materialSchema = Type.Object({
  id, revision, name: id, text: Type.String(), source: Type.Optional(Type.String()),
});
export const maxDraftBytes = 512 * 1024;
export const activitySteerPayload = Type.Object({ text: Type.String({ minLength: 1 }), materials: Type.Array(materialSchema) }, skillStrict);
export type ActivitySteerPayload = Static<typeof activitySteerPayload>;
export const activitySteerRecord = Type.Object({ ...activitySteerReceipt.properties,
  fingerprint: Type.String({ pattern: '^[a-f0-9]{64}$' }), createdAt: id, settledAt: Type.Optional(id), payload: activitySteerPayload }, skillStrict);
export type ActivitySteerRecord = Static<typeof activitySteerRecord>;
export const questionTextBytes = 1024;
export const optionLabelBytes = 120;
export const optionDescriptionBytes = 256;
export const questionOptionId = Type.String({ pattern: '^[a-z0-9][a-z0-9_-]{0,63}$' });
export const questionOptions = Type.Array(Type.Object({ id: questionOptionId,
  label: Type.String({ minLength: 1, maxLength: optionLabelBytes }),
  description: Type.Optional(Type.String({ maxLength: optionDescriptionBytes })), recommended: Type.Optional(Type.Boolean()) }, skillStrict), { maxItems: 6 });
export const preferenceKey = Type.String({ pattern: '^[a-z][a-z0-9_]{0,63}$' });
export const maxTriageAttempts = 3;
export const maxAutomaticAnswers = 2;
export const triageAccounting = Type.Object({ activityId: id,
  attemptsUsed: Type.Integer({ minimum: 1, maximum: maxTriageAttempts }),
  autoAnswersUsed: Type.Integer({ minimum: 0, maximum: maxAutomaticAnswers }), updatedAt: id }, skillStrict);
export type TriageAccounting = Static<typeof triageAccounting>;
const answerProvenanceSchema = Type.Object({ provenance: Type.Optional(Type.Union([Type.Literal('user'), Type.Literal('bot_auto')])),
  evidenceId: Type.Optional(id), evidenceRevision: Type.Optional(revision), selectedOptionId: Type.Optional(questionOptionId) });
const answerProvenance = answerProvenanceSchema.properties;
export type AnswerProvenance = Static<typeof answerProvenanceSchema>;
export const quoteAuthorMaxBytes = 120;
export const quoteTextMaxBytes = 1024;
export const productQuote = Type.Object({
  messageId: id, bubbleIndex: Type.Optional(revision), sender: Type.Union([Type.Literal('user'), Type.Literal('bot')]),
  authorName: Type.String({ pattern: '\\S', maxLength: quoteAuthorMaxBytes }),
  text: Type.String({ pattern: '\\S', maxLength: quoteTextMaxBytes }),
}, { additionalProperties: false });
const optionalQuote = Type.Optional(Type.Union([productQuote, Type.Null()]));
export const draftSchema = Type.Object({
  revision, text: Type.String(), materials: Type.Array(materialSchema),
  quote: optionalQuote,
});
export const executionSchema = Type.Object({
  executionId: id,
  revision,
  state: Type.Union([
    Type.Literal('starting'), Type.Literal('running'), Type.Literal('completed'),
    Type.Literal('interrupted'), Type.Literal('failed'), Type.Literal('unknown'),
  ]),
});
export const operationSchema = Type.Object({
  operationId: id,
  state: Type.Union([Type.Literal('pending'), Type.Literal('accepted'), Type.Literal('rejected'), Type.Literal('unknown')]),
  target: targetSchema,
  execution: Type.Optional(executionSchema),
  result: Type.Optional(Type.Record(Type.String(), Type.Unknown())),
});
const traceIdentity = { seq: positiveInteger, activityId: id, executionId: id, at: id };
const toolCall = { tool: id, callId: id };
const strictTrace = { additionalProperties: false };
export const traceItemSchema = Type.Union([
  Type.Object({ ...traceIdentity, kind: Type.Literal('tool_started'), ...toolCall, target: Type.Optional(Type.String()) }, strictTrace),
  Type.Object({ ...traceIdentity, kind: Type.Literal('tool_finished'), ...toolCall, ok: Type.Boolean(), excerpt: Type.Optional(Type.String()) }, strictTrace),
  Type.Object({ ...traceIdentity, kind: Type.Literal('file_confirmed'), callId: id, path: Type.String() }, strictTrace),
  Type.Object({ ...traceIdentity, kind: Type.Literal('state_changed'), scope: Type.Literal('activity'), state: activityStateSchema }, strictTrace),
  Type.Object({ ...traceIdentity, kind: Type.Literal('state_changed'), scope: Type.Literal('execution'), state: executionSchema.properties.state }, strictTrace),
]);
export type TraceItem = Static<typeof traceItemSchema>;
const memoryOwner = { botId: id };
const memoryOperation = { ...memoryOwner, operationId: id };
const memorySearch = { ...memoryOwner, query: memorySearchQuery, dateFrom: Type.Optional(memoryDate), dateTo: Type.Optional(memoryDate),
  limit: Type.Optional(memorySearchLimit) };
const sessionStrict = { additionalProperties: false };
const sessionTarget = { botId: id, sessionId: id };
const sessionGeneration = { ...sessionTarget, generation: positiveInteger };
export const sessionState = Type.Union([Type.Literal('starting'), Type.Literal('running'), Type.Literal('exited'),
  Type.Literal('closed'), Type.Literal('disconnected'), Type.Literal('failed')]);
export const sessionMode = Type.Union([Type.Literal('pty'), Type.Literal('pipe')]);
export const sessionDimension = Type.Integer({ minimum: 1, maximum: 65535 });
export const sessionReadLimit = Type.Integer({ minimum: 1024, maximum: 65536 });
const sessionTransport = Type.Union([Type.Literal('live'), Type.Literal('eof'), Type.Literal('supervisor_lost'),
  Type.Literal('worker_restarted'), Type.Literal('closed_by_user'), Type.Literal('closed_by_bot'),
  Type.Literal('closed_on_shutdown'), Type.Literal('exec_failed')]);
const authPause = Type.Optional(Type.Object({ paused: Type.Boolean(), reason: Type.Optional(id), message: Type.Optional(id) }, sessionStrict));
const exitCode = Type.Optional(Type.Union([revision, Type.Null()]));
const exitSignal = Type.Optional(id);
const watchCondition = Type.Union([Type.Literal('exit'), Type.Literal('match'), Type.Literal('idle'), Type.Literal('timeout')]);
const watchFiredKind = Type.Union([...watchCondition.anyOf, Type.Literal('target_lost')]);
export const sessionWatchSummary = Type.Object({ watchId: id, sessionId: id, generation: positiveInteger, revision: positiveInteger,
  state: Type.Union([Type.Literal('armed'), Type.Literal('fired'), Type.Literal('cancelled'), Type.Literal('target_lost')]),
  conditions: Type.Array(watchCondition, { minItems: 1, maxItems: 4, uniqueItems: true }), startOffset: revision,
  matchLiteral: Type.Optional(id), idleMs: Type.Optional(positiveInteger), timeoutMs: Type.Optional(positiveInteger),
  firedKind: Type.Optional(watchFiredKind), occurrenceId: Type.Optional(id), firedAt: Type.Optional(id),
  firedOffset: Type.Optional(revision), matchedExcerpt: Type.Optional(Type.String()),
}, sessionStrict);
export const sessionWatchOccurrence = Type.Object({ occurrenceId: id, watchId: id, sessionId: id, ownerBotId: id,
  generation: positiveInteger, firedKind: watchFiredKind, firedAt: id, firedOffset: Type.Optional(revision),
  matchedExcerpt: Type.Optional(Type.String()) }, sessionStrict);
const sessionWatermark = { firstAvailableOffset: revision, nextOffset: revision, evictedBytes: revision, eof: Type.Boolean() };
export const sessionSummary = Type.Object({ sessionId: id, ownerBotId: id, originConversationId: Type.Optional(id), originActivityId: Type.Optional(id),
  creatingOperationId: id, generation: positiveInteger, revision: positiveInteger, label: Type.Optional(Type.String()), commandSummary: Type.String(),
  cwd: id, mode: sessionMode, cols: Type.Optional(sessionDimension), rows: Type.Optional(sessionDimension), state: sessionState,
  exitCode, exitSignal, transportStatus: sessionTransport, ...sessionWatermark, authPause, watches: Type.Array(sessionWatchSummary),
  error: Type.Optional(Type.String()), createdAt: id, updatedAt: id, settledAt: Type.Optional(id),
}, sessionStrict);
const sessionOutputUpdated = Type.Object({ sessionId: id, ownerBotId: id, generation: positiveInteger, ...sessionWatermark, at: id }, sessionStrict);
export const sessionChunk = Type.Object({ stream: Type.Union([Type.Literal('pty'), Type.Literal('stdout'), Type.Literal('stderr')]),
  offset: revision, nextOffset: revision, text: Type.String() }, sessionStrict);
const sessionReadResult = Type.Object({ sessionId: id, generation: positiveInteger, revision: positiveInteger, mode: sessionMode, state: sessionState,
  firstAvailableOffset: revision, fromOffset: revision, nextOffset: revision, highWaterOffset: revision, eof: Type.Boolean(),
  gap: Type.Boolean(), gapBytes: revision, authPause, chunks: Type.Array(sessionChunk) }, sessionStrict);
const sessionCloseResult = Type.Object({ sessionId: id, generation: positiveInteger, revision: positiveInteger, state: sessionState,
  closeDisposition: Type.Union([Type.Literal('terminated'), Type.Literal('already_settled')]), childReaped: Type.Boolean(),
  exitCode, exitSignal, transportStatus: sessionTransport }, sessionStrict);
export type SessionSummary = Static<typeof sessionSummary>;
export type SessionWatchSummary = Static<typeof sessionWatchSummary>;
export type SessionWatchOccurrence = Static<typeof sessionWatchOccurrence>;
export type SessionChunk = Static<typeof sessionChunk>;
export type SessionReadResult = Static<typeof sessionReadResult>;
export type SessionCloseResult = Static<typeof sessionCloseResult>;
export const productCommands = Type.Union([
  Type.Object({ method: Type.Literal('app.open'), params: Type.Object({ supportedVersions: Type.Array(positiveInteger, { minItems: 1 }) }) }),
  Type.Object({ method: Type.Literal('bot.list'), params: Type.Object(page) }),
  Type.Object({ method: Type.Literal('bot.ensureBuiltin'), params: Type.Object({ kind: botSystemKindSchema, operationId: id }) }),
  Type.Object({ method: Type.Literal('bot.create'), params: Type.Object({ profile: profileSchema, operationId: id }) }),
  Type.Object({ method: Type.Literal('bot.update'), params: Type.Object({
    botId: id, ...operationTarget, profile: profileSchema, startNewConversation: Type.Boolean(),
  }) }),
  Type.Object({ method: Type.Literal('bot.archive'), params: Type.Object({ botId: id, ...operationTarget }) }),
  Type.Object({ method: Type.Literal('bot.restore'), params: Type.Object({ botId: id, ...operationTarget }) }),
  Type.Object({ method: Type.Literal('memory.get'), params: Type.Object(memoryOwner) }),
  Type.Object({ method: Type.Literal('memory.addRule'), params: Type.Object({ ...memoryOperation, text: memoryText }) }),
  Type.Object({ method: Type.Literal('memory.updateRule'), params: Type.Object({ ...memoryOperation, ruleId: id, text: memoryText }) }),
  Type.Object({ method: Type.Literal('memory.deleteRule'), params: Type.Object({ ...memoryOperation, ruleId: id }) }),
  Type.Object({ method: Type.Literal('memory.reviewLearning'), params: Type.Object({ ...memoryOperation, learningId: id,
    decision: Type.Union([Type.Literal('approve'), Type.Literal('reject')]), text: Type.Optional(memoryText) }) }),
  Type.Object({ method: Type.Literal('memory.deleteLog'), params: Type.Object({ ...memoryOperation, logId: id }) }),
  Type.Object({ method: Type.Literal('memory.searchLogs'), params: Type.Object(memorySearch) }),
  Type.Object({ method: Type.Literal('skill.list'), params: Type.Object({ botId: Type.Optional(id) }, { additionalProperties: false }) }),
  Type.Object({ method: Type.Literal('skill.get'), params: Type.Object(skillKey, { additionalProperties: false }) }),
  Type.Object({ method: Type.Literal('skill.save'), params: Type.Object({ ...skillKey, source: writableSkillSource, ...skillText, ...skillExecutable, operationId: id }, skillStrict) }),
  Type.Object({ method: Type.Literal('skill.delete'), params: Type.Object({ ...skillKey, source: writableSkillSource, operationId: id }, { additionalProperties: false }) }),
  Type.Object({ method: Type.Literal('skill.promoteGlobal'), params: Type.Object({ botId: id, skillId: skillID, targetSkillId: Type.Optional(skillID), operationId: id }, { additionalProperties: false }) }),
  Type.Object({ method: Type.Literal('skill.setPreferences'), params: Type.Object({ discoverLocalSkills: Type.Boolean(), operationId: id }, { additionalProperties: false }) }),
  Type.Object({ method: Type.Literal('skill.approve'), params: Type.Object({ source: skillSource, botId: id, skillId: skillID, approvalDigest: digest,
    permissionsDigest: Type.Optional(digest), requestId: Type.Optional(id), requestRevision: Type.Optional(positiveInteger),
    canonicalWorkspace: Type.Optional(id), installationId: Type.Optional(id), operationId: id }, skillStrict) }),
  Type.Object({ method: Type.Literal('skill.denyApprovalRequest'), params: Type.Object({ requestId: id, requestRevision: positiveInteger,
    operationId: id, reason: Type.Optional(Type.String({ maxLength: 512 })) }, skillStrict) }),
  Type.Object({ method: Type.Literal('skill.revokeApproval'), params: Type.Object({ source: skillSource, botId: id, skillId: skillID, operationId: id }, skillStrict) }),
  Type.Object({ method: Type.Literal('conversation.list'), params: Type.Object({ botId: id, ...page }) }),
  Type.Object({ method: Type.Literal('conversation.get'), params: Type.Object({ conversationId: id }) }),
  Type.Object({ method: Type.Literal('conversation.saveDraft'), params: Type.Object({
    conversationId: id, operationId: id, expectedRevision: revision, text: Type.String(), materials: Type.Array(materialSchema),
    quote: optionalQuote,
  }) }),
  Type.Object({ method: Type.Literal('conversation.send'), params: Type.Object({ conversationId: id, operationId: id, draftRevision: revision }) }),
  Type.Object({ method: Type.Literal('conversation.stop'), params: Type.Object({ conversationId: id, executionId: id, operationId: id }) }),
  Type.Object({ method: Type.Literal('conversation.history'), params: Type.Object({ conversationId: id, ...page }) }),
  Type.Object({ method: Type.Literal('activity.list'), params: Type.Object({ botId: id, states: Type.Optional(Type.Array(activityStateSchema)),
    unacknowledgedOnly: Type.Optional(Type.Boolean()), ...page }) }),
  Type.Object({ method: Type.Literal('activity.get'), params: Type.Object({ activityId: id }) }),
  Type.Object({ method: Type.Literal('activity.result'), params: Type.Object({ activityId: id, executionId: id }) }),
  Type.Object({ method: Type.Literal('activity.continue'), params: Type.Object({
    activityId: id, ...operationTarget, text: Type.String(), materials: Type.Array(materialSchema),
  }) }),
  Type.Object({ method: Type.Literal('activity.acknowledge'), params: Type.Object({ activityId: id, executionId: id, ...operationTarget }, skillStrict) }),
  Type.Object({ method: Type.Literal('activity.close'), params: Type.Object({ activityId: id, ...operationTarget }, skillStrict) }),
  Type.Object({ method: Type.Literal('activity.stop'), params: Type.Object({ activityId: id, executionId: id, operationId: id }) }),
  Type.Object({ method: Type.Literal('activity.steer'), params: Type.Object({ activityId: id, executionId: id,
    expectedRevision: Type.Optional(revision), operationId: id, ...activitySteerPayload.properties }, skillStrict) }),
  Type.Object({ method: Type.Literal('activity.steerStatus'), params: Type.Union([
    Type.Object({ operationId: id, steerId: Type.Optional(id) }, skillStrict),
    Type.Object({ steerId: id, operationId: Type.Optional(id) }, skillStrict),
  ]) }),
  Type.Object({ method: Type.Literal('activity.trace'), params: Type.Object({ activityId: id, executionId: Type.Optional(id), ...inspectionPage }, { additionalProperties: false }) }),
  Type.Object({ method: Type.Literal('question.answer'), params: Type.Object({
    activityId: id, executionId: id, questionId: id, questionRevision: revision, operationId: id,
    text: Type.String(), materials: Type.Array(materialSchema), selectedOptionId: Type.Optional(questionOptionId),
  }) }),
  Type.Object({ method: Type.Literal('connection.get'), params: Type.Object({}) }),
  Type.Object({ method: Type.Literal('connection.test'), params: Type.Object({ configuration: connectionSchema }) }),
  Type.Object({ method: Type.Literal('connection.save'), params: Type.Object({ configuration: connectionSchema, operationId: id }) }),
  Type.Object({ method: Type.Literal('connection.apply'), params: Type.Object({ savedRevision: revision, operationId: id }) }),
  Type.Object({ method: Type.Literal('provider.list'), params: Type.Object({}, mcpStrict) }),
  Type.Object({ method: Type.Literal('provider.test'), params: Type.Object({ account: providerInput, apiKey: Type.Optional(Type.String()) }, mcpStrict) }),
  Type.Object({ method: Type.Literal('provider.save'), params: Type.Object({ account: providerInput, apiKey: Type.Optional(Type.String()), operationId: id }, mcpStrict) }),
  Type.Object({ method: Type.Literal('provider.delete'), params: Type.Object({ providerId: providerInput.properties.providerId, operationId: id }, mcpStrict) }),
  Type.Object({ method: Type.Literal('provider.reorder'), params: Type.Object({ providerIds: Type.Array(providerInput.properties.providerId,
    { maxItems: 64, uniqueItems: true }), savedRevision: revision, operationId: id }, mcpStrict) }),
  Type.Object({ method: Type.Literal('provider.apply'), params: Type.Object({ savedRevision: revision, operationId: id }, mcpStrict) }),
  Type.Object({ method: Type.Literal('provider.revokeCredential'), params: Type.Object({ providerId: providerInput.properties.providerId, operationId: id }, mcpStrict) }),
  Type.Object({ method: Type.Literal('routing.get'), params: Type.Object({}, mcpStrict) }),
  Type.Object({ method: Type.Literal('routing.setPolicy'), params: Type.Object({ policy: policySchema, operationId: id }, mcpStrict) }),
  Type.Object({ method: Type.Literal('routing.setOverrides'), params: Type.Object({ savedRevision: revision, overrides: overridesPatchSchema, operationId: id }, mcpStrict) }),
  Type.Object({ method: Type.Literal('mcp.list'), params: Type.Object({}, mcpStrict) }),
  Type.Object({ method: Type.Literal('mcp.save'), params: Type.Object({ server: mcpServerInput, secrets: mcpSecretInput, operationId: id }, mcpStrict) }),
  Type.Object({ method: Type.Literal('mcp.delete'), params: Type.Object({ serverId: mcpServerId, operationId: id }, mcpStrict) }),
  Type.Object({ method: Type.Literal('mcp.test'), params: Type.Object({ serverId: Type.Optional(mcpServerId), server: Type.Optional(mcpServerInput), secrets: mcpSecretInput }, mcpStrict) }),
  Type.Object({ method: Type.Literal('mcp.status'), params: Type.Object({}, mcpStrict) }),
  Type.Object({ method: Type.Literal('session.list'), params: Type.Object({ botId: id,
    states: Type.Optional(Type.Array(sessionState, { minItems: 1, maxItems: 6, uniqueItems: true })),
    cursor: Type.Optional(id), limit: inspectionPage.limit }, sessionStrict) }),
  Type.Object({ method: Type.Literal('session.get'), params: Type.Object(sessionTarget, sessionStrict) }),
  Type.Object({ method: Type.Literal('session.read'), params: Type.Object({ ...sessionGeneration,
    offset: Type.Optional(revision), limit: Type.Optional(sessionReadLimit) }, sessionStrict) }),
  Type.Object({ method: Type.Literal('session.close'), params: Type.Object({ ...sessionGeneration,
    expectedRevision: Type.Optional(revision), operationId: id }, sessionStrict) }),
  Type.Object({ method: Type.Literal('operation.get'), params: Type.Object({ operationId: id }) }),
  Type.Object({ method: Type.Literal('profile.backup'), params: Type.Object({ destination: id }) }),
  Type.Object({ method: Type.Literal('app.shutdown'), params: Type.Object({}) }),
]);
const executionOwner = Type.Union([Type.Object({ conversationId: id }), Type.Object({ activityId: id })]);
const executionIdentity = { executionId: id, operationId: id };
const executionEvent = (fields: object) => Type.Intersect([executionOwner, Type.Object({ ...executionIdentity, ...fields })]);
export const productEvents = Type.Union([
  Type.Object({ event: Type.Literal('provider.changed'), data: Type.Object({ savedRevision: Type.Union([revision, Type.Null()]),
    effectiveRevision: Type.Union([revision, Type.Null()]), change: Type.Union([Type.Literal('saved'), Type.Literal('deleted'),
      Type.Literal('applied'), Type.Literal('revoked'), Type.Literal('policy')]) }) }),
  Type.Object({ event: Type.Literal('bot.changed'), data: Type.Object({ bot: botResult, change: botChangeSchema }) }),
  Type.Object({ event: Type.Literal('memory.changed'), data: memoryChangedSchema }),
  Type.Object({ event: Type.Literal('skill.changed'), data: skillChange }),
  Type.Object({ event: Type.Literal('mcp.changed'), data: mcpChange }),
  Type.Object({ event: Type.Literal('execution.started'), data: executionEvent({ revision }) }),
  Type.Object({ event: Type.Literal('execution.textDelta'), data: executionEvent({ offset: revision, text: Type.String() }) }),
  Type.Object({ event: Type.Literal('execution.settled'), data: executionEvent({
    revision, state: Type.Union([Type.Literal('completed'), Type.Literal('interrupted'), Type.Literal('failed')]),
    error: Type.Optional(Type.String()),
  }) }),
  Type.Object({ event: Type.Literal('conversation.changed'), data: Type.Object({ conversationId: id, revision }) }),
  Type.Object({ event: Type.Literal('activity.traceUpdated'), data: Type.Object({ activityId: id, executionId: id, lastSeq: positiveInteger }) }),
  Type.Object({ event: Type.Literal('activity.changed'), data: Type.Object({ ...activityFields, ...approvalRequests, ...steers }) }),
  Type.Object({ event: Type.Literal('activity.digest'), data: activityDigest }),
  Type.Object({ event: Type.Literal('session.changed'), data: sessionSummary }),
  Type.Object({ event: Type.Literal('session.outputUpdated'), data: sessionOutputUpdated }),
]);
export const productRequestSchema = Type.Intersect([
  Type.Object({ v: Type.Literal(1), type: Type.Literal('request'), id }), productCommands,
]);
export const productEventSchema = Type.Intersect([
  Type.Object({ v: Type.Literal(1), type: Type.Literal('event'), streamId: id, seq: positiveInteger }), productEvents,
]);
const nullable = <T extends TSchema>(schema: T) => Type.Union([schema, Type.Null()]);
const productPage = <T extends TSchema>(item: T) => Type.Object({ items: Type.Array(item), nextCursor: nullable(id) });
const conversationFields = { conversationId: id, botId: id, revision, mode: Type.Union([Type.Literal('current'), Type.Literal('readOnly')]) };
const activeExecution = Type.Object({ ...executionSchema.properties, operationId: id, partial: Type.Optional(nullable(Type.String())),
  textOffset: Type.Optional(revision), error: Type.Optional(Type.String()) });
const questionDelivery = Type.Union([Type.Literal('saved'), Type.Literal('dispatching'), Type.Literal('delivered'),
  Type.Literal('unknown'), Type.Literal('canceled'), Type.Literal('unavailable')]);
export const questionResult = Type.Object({ questionId: id, executionId: id, text: Type.String(), revision,
  state: Type.Union([Type.Literal('open'), Type.Literal('answered'), Type.Literal('superseded'), Type.Literal('triage_pending')]),
  answer: Type.Optional(Type.Object({ text: Type.String(), materials: Type.Array(materialSchema) }, skillStrict)), delivery: Type.Optional(questionDelivery),
  deliveryBlocker: Type.Optional(Type.Literal('credential_unavailable')), options: Type.Optional(questionOptions),
  allowFreeText: Type.Optional(Type.Boolean()), requiresUserDecision: Type.Optional(Type.Boolean()), preferenceKey: Type.Optional(preferenceKey),
  ...answerProvenance }, skillStrict);
export type ProductQuestion = Static<typeof questionResult>;
const connectionRevisions = { savedRevision: nullable(revision), effectiveRevision: nullable(revision) };
const routingFields = { ...connectionRevisions, policy: policySchema, effectivePolicy: policySchema,
  resolvedRoles: Type.Object({ dialogue: nullable(routeSchema), coding: nullable(routeSchema), reasoning: nullable(routeSchema), utility: nullable(routeSchema) }),
  overrides: Type.Optional(overridesSchema), effectiveOverrides: Type.Optional(overridesSchema), roleStatus: Type.Optional(roleStatusSchema) };
const deliveryBlocker = Type.Optional(Type.Literal('credential_unavailable'));
const connectionView = Type.Object({ ...Type.Omit(connectionSchema, ['apiKey']).properties, credentialPresent: Type.Boolean() });
const connectionResult = Type.Object({ ...connectionRevisions, saved: Type.Union([connectionView, Type.Null()]), effective: Type.Union([connectionView, Type.Null()]) });
const stopResult = Type.Object({ executionId: id, cancelRequested: Type.Literal(true) });
export const productResults = {
  'app.open': Type.Object({ version: Type.Literal(1), streamId: id, eventSeq: revision, snapshot: Type.Object({ bots: Type.Array(botResult),
    nextBotCursor: nullable(id), connection: connectionResult, preferences: Type.Optional(Type.Object({ selectedBotId: Type.Optional(id),
      layout: Type.Optional(Type.Record(Type.String(), Type.Unknown())), themeIds: Type.Optional(Type.Record(Type.String(), Type.Unknown())),
      appearance: Type.Optional(Type.String()), hideBuiltinArchitect: Type.Optional(Type.Boolean()), discoverLocalSkills: Type.Optional(Type.Boolean()) })),
    recovery: Type.Optional(Type.Object({ id, ui: Type.Optional(Type.Record(Type.String(), Type.Unknown())) })) }) }),
  'bot.list': productPage(botResult),
  'bot.ensureBuiltin': botResult,
  'bot.create': botResult,
  'bot.update': botResult,
  'bot.archive': botResult,
  'bot.restore': botResult,
  'memory.get': Type.Object({ botId: id, rulesRevision: revision, rules: Type.Array(memoryRuleSchema),
    pendingLearnings: Type.Array(pendingLearningSchema), workLogs: Type.Array(workLogSchema) }),
  'memory.addRule': memoryChangedSchema,
  'memory.updateRule': memoryChangedSchema,
  'memory.deleteRule': memoryChangedSchema,
  'memory.reviewLearning': memoryChangedSchema,
  'memory.deleteLog': memoryChangedSchema,
  'memory.searchLogs': Type.Object({ items: Type.Array(workLogSchema) }),
  'skill.list': skillListing,
  'skill.get': Type.Object({ skill: skillDetail }),
  'skill.save': skillChange,
  'skill.delete': skillChange,
  'skill.promoteGlobal': skillChange,
  'skill.setPreferences': skillChange,
  'skill.approve': Type.Object({ ...skillChange.properties, request: Type.Optional(skillApprovalRequestSchema) }),
  'skill.denyApprovalRequest': skillApprovalRequestSchema,
  'skill.revokeApproval': skillChange,
  'conversation.list': productPage(Type.Object({ ...conversationFields,
    preview: Type.Optional(id), timestamp: Type.Optional(id) })),
  'conversation.get': Type.Object({ ...conversationFields, draft: Type.Union([draftSchema, Type.Null()]), latestExecution: Type.Union([activeExecution, Type.Null()]) }),
  'conversation.saveDraft': draftSchema,
  'conversation.send': operationSchema,
  'conversation.stop': stopResult,
  'conversation.history': productPage(Type.Object({ messageId: id, role: Type.Union([Type.Literal('user'), Type.Literal('assistant')]),
    timestamp: id, text: Type.String(), materials: Type.Array(materialSchema), quote: optionalQuote })),
  'activity.list': productPage(Type.Object(activityFields)),
  'activity.get': Type.Object({ ...activityFields, ...approvalRequests, ...steers, ...activityRouting.properties, requirements: stringList,
    materials: Type.Array(materialSchema), questions: Type.Array(questionResult), executions: Type.Array(activeExecution) }),
  'activity.result': Type.Object({ activityId: id, executionId: id, text: Type.String(), delivery: Type.Literal('available'), files: Type.Array(Type.Object({ path: id, callId: id })) }),
  'activity.trace': Type.Object({ ...productPage(traceItemSchema).properties, historyTruncated: Type.Boolean(),
    coverage: Type.Union([Type.Literal('recorded'), Type.Literal('unrecorded'), Type.Literal('mixed')]) }),
  'activity.continue': operationSchema,
  'activity.acknowledge': Type.Object(activityFields),
  'activity.close': Type.Object(activityFields),
  'activity.stop': stopResult,
  'activity.steer': activitySteerReceipt,
  'activity.steerStatus': activitySteerReceipt,
  'question.answer': Type.Object({ activityId: id, executionId: id, questionId: id, revision, answerSaved: Type.Literal(true),
    delivery: questionDelivery, deliveryBlocker, ...answerProvenance }),
  'connection.get': connectionResult,
  'connection.test': Type.Object({ verified: Type.Literal(true) }),
  'connection.save': Type.Object(connectionRevisions),
  'connection.apply': Type.Object(connectionRevisions),
  'provider.list': Type.Object({ ...routingFields, accounts: Type.Array(providerAccountView) }),
  'provider.test': Type.Object({ ok: Type.Boolean(), models: stringList, discoveredModels: Type.Optional(Type.Array(discoveredModel)), latencyMs: revision, error: Type.Optional(id) }),
  'provider.save': Type.Object(connectionRevisions),
  'provider.delete': Type.Object(connectionRevisions),
  'provider.reorder': Type.Object(connectionRevisions),
  'provider.apply': Type.Object(connectionRevisions),
  'provider.revokeCredential': Type.Object(connectionRevisions),
  'routing.get': Type.Object(routingFields),
  'routing.setPolicy': Type.Object(connectionRevisions),
  'routing.setOverrides': Type.Object(connectionRevisions),
  'mcp.list': Type.Object({ servers: Type.Array(mcpServerView) }),
  'mcp.save': Type.Object({ server: mcpServerView }),
  'mcp.delete': Type.Object({ deleted: Type.Literal(true) }),
  'mcp.test': mcpTestResult,
  'mcp.status': Type.Object({ pools: Type.Array(mcpPool) }),
  'session.list': Type.Object({ items: Type.Array(sessionSummary), nextCursor: nullable(id) }, sessionStrict),
  'session.get': sessionSummary,
  'session.read': sessionReadResult,
  'session.close': Type.Object({ operationId: id, state: operationSchema.properties.state,
    target: Type.Object(sessionTarget, sessionStrict), result: sessionCloseResult }, sessionStrict),
  'operation.get': operationSchema,
  'profile.backup': Type.Object({ path: id }),
  'app.shutdown': Type.Object({ drained: Type.Literal(true) }),
} satisfies Record<Static<typeof productCommands>['method'], object>;
export const productWireSchema = {
  $schema: 'https://json-schema.org/draft/2020-12/schema',
  $defs: {
    request: productRequestSchema,
    event: productEventSchema,
    error: Type.Object({ v: Type.Literal(1), type: Type.Literal('response'), id, ok: Type.Literal(false),
      error: Type.Object({ code: id, message: Type.String(), details: Type.Optional(Type.Record(Type.String(), Type.Unknown())) }) }),
    ...Object.fromEntries(Object.entries(productResults).map(([method, result]) => [method,
      Type.Object({ v: Type.Literal(1), type: Type.Literal('response'), id, ok: Type.Literal(true), result })])),
  },
};
export type ProductCommand = Static<typeof productCommands>;
export type Material = Static<typeof materialSchema>;
export type ProductQuote = Static<typeof productQuote>;
export function hasCanonicalQuoteText({ authorName, text }: ProductQuote): boolean {
  return authorName.length > 0 && authorName === authorName.trim() && Buffer.byteLength(authorName) <= quoteAuthorMaxBytes &&
    text.length > 0 && text === text.trim() && Buffer.byteLength(text) <= quoteTextMaxBytes;
}
export type Draft = Static<typeof draftSchema>;
export type Execution = Static<typeof executionSchema>;
export type Operation = Static<typeof operationSchema>;
export type BotProfile = Static<typeof profileSchema>;
export function resolveCodingTools(profile: Pick<BotProfile, 'codingTools' | 'toolPreset'>): string[] | null {
  if (profile.codingTools !== null) return profile.codingTools;
  const preset = profile.toolPreset;
  return preset && preset !== 'custom' ? codingToolPresets[preset] : null;
}
export type Connection = Static<typeof connectionSchema>;
export type ActivityState = Static<typeof activityStateSchema>;
export type ProductParams<M extends ProductCommand['method']> = Extract<ProductCommand, { method: M }>['params'];
const modelSettings = Type.Object({ model: id, contextTokens: positiveInteger, outputTokens: positiveInteger });
const configuration = Type.Object({ wireApi: connectionSchema.properties.wireApi, baseUrl: id, apiKey: id, credentialSource: id,
  ...modelSettings.properties, thinkingLevel: Type.Optional(thinkingSchema), reasoning: Type.Optional(Type.Boolean()), capabilities: Type.Optional(modelCapabilities) });
const commands = Type.Union([
  Type.Object({ method: Type.Literal('configure'), params: configuration }),
  Type.Object({ method: Type.Literal('context.open'), params: Type.Object({ contextId: id, botId: id, purpose: Type.Union([Type.Literal('conversation'), Type.Literal('activity')]), activityId: Type.Optional(id), selectionRevision: Type.Integer({ minimum: 0, maximum: Number.MAX_SAFE_INTEGER }), instructions: Type.String(), cwd: id, resumeRef: Type.Optional(id), used: Type.Boolean(), codingTools: Type.Union([Type.Null(), stringList]), domainTools: Type.Union([Type.Null(), Type.Array(domainTool)]), mcpServers: Type.Optional(Type.Array(mcpServer)), configuration: Type.Optional(configuration) }) }),
  Type.Object({ method: Type.Literal('context.history'), params: Type.Object({ contextId: id, cursor: Type.Optional(id), limit: Type.Integer({ minimum: 0, maximum: 200 }) }) }),
  Type.Object({ method: Type.Literal('context.inspect'), params: Type.Object({ contextId: id }) }),
  Type.Object({ method: Type.Literal('context.configuration'), params: Type.Object({ contextId: id }) }),
  Type.Object({ method: Type.Literal('context.configure'), params: Type.Object({ contextId: id, settings: modelSettings }) }),
  Type.Object({ method: Type.Literal('context.close'), params: Type.Object({ contextId: id }) }),
  Type.Object({ method: Type.Literal('run.start'), params: Type.Object({ ...target, text: id, context: Type.Optional(Type.Array(Type.Object({ name: id, text: Type.String() }))), correlation: Type.Optional(Type.Object({ turnId: Type.Optional(id), executionId: Type.Optional(id), scopeRef: Type.Optional(id) })) }) }),
  Type.Object({ method: Type.Literal('run.cancel'), params: Type.Object(target) }),
  Type.Object({ method: Type.Literal('tool.result'), params: Type.Object({ ...target, workerEpoch: id, callId: id, content: Type.Optional(content), details: Type.Optional(Type.Unknown()), error: Type.Optional(Type.Object({ code: id, message: Type.String() })) }) }),
  Type.Object({ method: Type.Literal('shutdown'), params: Type.Object({}) }),
]);
export type Command = Static<typeof commands>;
export type OpenParams = Extract<Command, { method: 'context.open' }>['params'];
export type ConfigureParams = Extract<Command, { method: 'configure' }>['params'];
export type ToolResult = Extract<Command, { method: 'tool.result' }>['params'];
export type StartParams = Extract<Command, { method: 'run.start' }>['params'];
export class RequestError extends Error {
  constructor(readonly code: string, message: string, readonly requestId?: string, readonly details?: object) { super(message); }
}
export function parseRequest(data: Buffer): { id: string; command: Command } {
  const value: unknown = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(data));
  if (!Value.Check(request, value)) throw new Error('Invalid protocol envelope');
  if (!Value.Check(commands, value)) throw new RequestError('invalid_params', 'Unsupported method or invalid parameters', value.id);
  return { id: value.id, command: value };
}
export function parseProductRequest(data: Buffer): { id: string; command: ProductCommand } {
  const value: unknown = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(data));
  if (!Value.Check(productRequestSchema, value)) {
    if (Value.Check(request, value) && 'type' in value && value.type === 'request') {
      throw new RequestError('invalid_params', 'Unsupported method or invalid parameters', value.id);
    }
    throw new Error('Invalid product protocol envelope');
  }
  return { id: value.id, command: value };
}
export function message(error: unknown): string { return error instanceof Error ? error.message : String(error); }
export function frameBytes(value: object): number { return Buffer.byteLength(JSON.stringify(value)) + 1; }

export function canonicalJSON(value: unknown): string {
  return JSON.stringify(value, (_key, item: unknown) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return item;
    const fields = item as Record<string, unknown>;
    return Object.fromEntries(Object.keys(fields).sort().map(key => [key, fields[key]]));
  });
}

export class Writer {
  private queued = 0;
  private tail: Promise<void> = Promise.resolve();
  private failed = false;
  constructor(private output: Writable, private onFailure: () => void) {
    output.on('error', () => this.fail());
  }
  private fail(): void {
    if (this.failed) return;
    this.failed = true;
    this.onFailure();
  }
  send(value: object): Promise<void> {
    const data = Buffer.from(JSON.stringify(value) + '\n');
    if (data.length > MAX_FRAME) throw new RequestError('frame_too_large', 'Result exceeds protocol frame limit');
    if (this.failed || this.queued + data.length > 4 * MAX_FRAME) {
      this.fail();
      return Promise.reject(new Error('Protocol output unavailable'));
    }
    this.queued += data.length;
    const write = this.tail.then(() => new Promise<void>((resolve, reject) => {
      this.output.write(data, error => error ? reject(error) : resolve());
    })).finally(() => { this.queued -= data.length; });
    this.tail = write.catch(() => this.fail());
    return write;
  }
  async flush(): Promise<void> {
    await this.tail;
    if (this.failed) throw new Error('Protocol output unavailable');
  }
}
