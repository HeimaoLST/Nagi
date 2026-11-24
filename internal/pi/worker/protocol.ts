import { Type, type Static, type TSchema } from 'typebox';
import { Value } from 'typebox/value';
import type { Writable } from 'node:stream';
import { SYSTEM_KIND_BOT_ARCHITECT } from './bot-template.js';

export const MAX_FRAME = 8 * 1024 * 1024;
const id = Type.String({ minLength: 1 });
const stringList = Type.Array(Type.String());
const target = { contextId: id, runId: id };
const content = Type.Array(Type.Union([Type.Object({ type: Type.Literal('text'), text: Type.String() }), Type.Object({ type: Type.Literal('image'), data: Type.String(), mimeType: id })]));
const domainTool = Type.Object({ name: id, description: Type.String(), parameters: Type.Record(Type.String(), Type.Unknown()) });
const mcpServer = Type.Object({ name: id, sourceId: id, config: Type.Record(Type.String(), Type.Unknown()) });
const request = Type.Object({ v: Type.Literal(1), id, method: id, params: Type.Record(Type.String(), Type.Unknown()) });
const positiveInteger = Type.Integer({ minimum: 1, maximum: Number.MAX_SAFE_INTEGER });
const revision = Type.Integer({ minimum: 0, maximum: Number.MAX_SAFE_INTEGER });
const page = {
  cursor: Type.Optional(id),
  limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 200 })),
};
export const profileSchema = Type.Object({
  name: id, role: Type.String(), purpose: Type.String(), instructions: Type.String(),
  workspace: Type.String(), codingTools: Type.Union([Type.Null(), stringList]),
});
export const botSystemKindSchema = Type.Literal(SYSTEM_KIND_BOT_ARCHITECT);
const botResult = Type.Object({ botId: id, profile: profileSchema, profileRevision: revision, archived: Type.Boolean(),
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
const targetSchema = Type.Union([
  Type.Object({ conversationId: id }), Type.Object({ botId: id }),
  Type.Object({ activityId: id, questionId: Type.Optional(id) }), Type.Object({ connection: Type.Literal(true) }),
]);
const operationTarget = { operationId: id, expectedRevision: revision };
export const materialSchema = Type.Object({
  id, revision, name: id, text: Type.String(), source: Type.Optional(Type.String()),
});
export const draftSchema = Type.Object({
  revision, text: Type.String(), materials: Type.Array(materialSchema),
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
  Type.Object({ method: Type.Literal('conversation.list'), params: Type.Object({ botId: id, ...page }) }),
  Type.Object({ method: Type.Literal('conversation.get'), params: Type.Object({ conversationId: id }) }),
  Type.Object({ method: Type.Literal('conversation.saveDraft'), params: Type.Object({
    conversationId: id, operationId: id, expectedRevision: revision, text: Type.String(), materials: Type.Array(materialSchema),
  }) }),
  Type.Object({ method: Type.Literal('conversation.send'), params: Type.Object({ conversationId: id, operationId: id, draftRevision: revision }) }),
  Type.Object({ method: Type.Literal('conversation.stop'), params: Type.Object({ conversationId: id, executionId: id, operationId: id }) }),
  Type.Object({ method: Type.Literal('conversation.history'), params: Type.Object({ conversationId: id, ...page }) }),
  Type.Object({ method: Type.Literal('activity.list'), params: Type.Object({ botId: id, states: Type.Optional(Type.Array(activityStateSchema)), ...page }) }),
  Type.Object({ method: Type.Literal('activity.get'), params: Type.Object({ activityId: id }) }),
  Type.Object({ method: Type.Literal('activity.result'), params: Type.Object({ activityId: id, executionId: id }) }),
  Type.Object({ method: Type.Literal('activity.continue'), params: Type.Object({
    activityId: id, ...operationTarget, text: Type.String(), materials: Type.Array(materialSchema),
  }) }),
  Type.Object({ method: Type.Literal('activity.stop'), params: Type.Object({ activityId: id, executionId: id, operationId: id }) }),
  Type.Object({ method: Type.Literal('question.answer'), params: Type.Object({
    activityId: id, executionId: id, questionId: id, questionRevision: revision, operationId: id,
    text: id, materials: Type.Array(materialSchema),
  }) }),
  Type.Object({ method: Type.Literal('connection.get'), params: Type.Object({}) }),
  Type.Object({ method: Type.Literal('connection.test'), params: Type.Object({ configuration: connectionSchema }) }),
  Type.Object({ method: Type.Literal('connection.save'), params: Type.Object({ configuration: connectionSchema, operationId: id }) }),
  Type.Object({ method: Type.Literal('connection.apply'), params: Type.Object({ savedRevision: revision, operationId: id }) }),
  Type.Object({ method: Type.Literal('operation.get'), params: Type.Object({ operationId: id }) }),
  Type.Object({ method: Type.Literal('profile.backup'), params: Type.Object({ destination: id }) }),
  Type.Object({ method: Type.Literal('app.shutdown'), params: Type.Object({}) }),
]);
const executionOwner = Type.Union([Type.Object({ conversationId: id }), Type.Object({ activityId: id })]);
const executionIdentity = { executionId: id, operationId: id };
const executionEvent = (fields: object) => Type.Intersect([executionOwner, Type.Object({ ...executionIdentity, ...fields })]);
export const productEvents = Type.Union([
  Type.Object({ event: Type.Literal('bot.changed'), data: Type.Object({ bot: botResult, change: botChangeSchema }) }),
  Type.Object({ event: Type.Literal('execution.started'), data: executionEvent({ revision }) }),
  Type.Object({ event: Type.Literal('execution.textDelta'), data: executionEvent({ offset: revision, text: Type.String() }) }),
  Type.Object({ event: Type.Literal('execution.settled'), data: executionEvent({
    revision, state: Type.Union([Type.Literal('completed'), Type.Literal('interrupted'), Type.Literal('failed')]),
    error: Type.Optional(Type.String()),
  }) }),
  Type.Object({ event: Type.Literal('conversation.changed'), data: Type.Object({ conversationId: id, revision }) }),
  Type.Object({ event: Type.Literal('activity.changed'), data: Type.Object({
    activityId: id, ownerBotId: id, originConversationId: id, revision, state: activityStateSchema,
    goal: Type.String(), openQuestionIds: Type.Array(id),
  }) }),
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
const activityFields = { activityId: id, ownerBotId: id, originConversationId: id, revision, state: activityStateSchema,
  goal: Type.String(), openQuestionIds: Type.Array(id) };
const questionDelivery = Type.Union([Type.Literal('saved'), Type.Literal('dispatching'), Type.Literal('delivered'),
  Type.Literal('unknown'), Type.Literal('canceled'), Type.Literal('unavailable')]);
const questionResult = Type.Object({ questionId: id, executionId: id, text: Type.String(), revision,
  state: Type.Union([Type.Literal('open'), Type.Literal('answered'), Type.Literal('superseded')]),
  answer: Type.Optional(Type.Object({ text: Type.String(), materials: Type.Array(materialSchema) })), delivery: Type.Optional(questionDelivery) });
const connectionRevisions = { savedRevision: nullable(revision), effectiveRevision: nullable(revision) };
const connectionView = Type.Object({ ...Type.Omit(connectionSchema, ['apiKey']).properties, credentialPresent: Type.Boolean() });
const connectionResult = Type.Object({ ...connectionRevisions, saved: Type.Union([connectionView, Type.Null()]), effective: Type.Union([connectionView, Type.Null()]) });
const stopResult = Type.Object({ executionId: id, cancelRequested: Type.Literal(true) });
export const productResults = {
  'app.open': Type.Object({ version: Type.Literal(1), streamId: id, eventSeq: revision, snapshot: Type.Object({ bots: Type.Array(botResult),
    nextBotCursor: nullable(id), connection: connectionResult, preferences: Type.Optional(Type.Object({ selectedBotId: Type.Optional(id),
      layout: Type.Optional(Type.Record(Type.String(), Type.Unknown())), themeIds: Type.Optional(Type.Record(Type.String(), Type.Unknown())),
      appearance: Type.Optional(Type.String()) })),
    recovery: Type.Optional(Type.Object({ id, ui: Type.Optional(Type.Record(Type.String(), Type.Unknown())) })) }) }),
  'bot.list': productPage(botResult),
  'bot.ensureBuiltin': botResult,
  'bot.create': botResult,
  'bot.update': botResult,
  'bot.archive': botResult,
  'bot.restore': botResult,
  'conversation.list': productPage(Type.Object({ ...conversationFields,
    preview: Type.Optional(id), timestamp: Type.Optional(id) })),
  'conversation.get': Type.Object({ ...conversationFields, draft: Type.Union([draftSchema, Type.Null()]), latestExecution: Type.Union([activeExecution, Type.Null()]) }),
  'conversation.saveDraft': draftSchema,
  'conversation.send': operationSchema,
  'conversation.stop': stopResult,
  'conversation.history': productPage(Type.Object({ messageId: id, role: Type.Union([Type.Literal('user'), Type.Literal('assistant')]),
    timestamp: id, text: Type.String(), materials: Type.Array(materialSchema) })),
  'activity.list': productPage(Type.Object(activityFields)),
  'activity.get': Type.Object({ ...activityFields, requirements: stringList, materials: Type.Array(materialSchema), questions: Type.Array(questionResult), executions: Type.Array(activeExecution) }),
  'activity.result': Type.Object({ activityId: id, executionId: id, text: Type.String(), delivery: Type.Literal('available'), files: Type.Array(Type.Object({ path: id, callId: id })) }),
  'activity.continue': operationSchema,
  'activity.stop': stopResult,
  'question.answer': Type.Object({ activityId: id, executionId: id, questionId: id, revision, answerSaved: Type.Literal(true), delivery: questionDelivery }),
  'connection.get': connectionResult,
  'connection.test': Type.Object({ verified: Type.Literal(true) }),
  'connection.save': Type.Object(connectionRevisions),
  'connection.apply': Type.Object(connectionRevisions),
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
export type Draft = Static<typeof draftSchema>;
export type Execution = Static<typeof executionSchema>;
export type Operation = Static<typeof operationSchema>;
export type BotProfile = Static<typeof profileSchema>;
export type Connection = Static<typeof connectionSchema>;
export type ActivityState = Static<typeof activityStateSchema>;
export type ProductParams<M extends ProductCommand['method']> = Extract<ProductCommand, { method: M }>['params'];
const modelSettings = Type.Object({ model: id, contextTokens: positiveInteger, outputTokens: positiveInteger });
const configuration = Type.Object({ wireApi: Type.Union([Type.Literal('openai-completions'), Type.Literal('openai-responses')]), baseUrl: id, apiKey: id, credentialSource: id, ...modelSettings.properties });
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
