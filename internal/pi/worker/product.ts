/// <reference lib="es2024.promise" />

import { createHash, randomUUID } from 'node:crypto';
import { stat } from 'node:fs/promises';
import { isAbsolute, resolve } from 'node:path';
import type { SessionEntry, ToolDefinition } from '@earendil-works/pi-coding-agent';
import type { AssistantMessage, ToolCall } from '@earendil-works/pi-ai';
import { Type } from 'typebox';
import { Value } from 'typebox/value';
import {
  MAX_FRAME, RequestError, canonicalJSON, draftSchema, frameBytes, profileSchema,
  type ActivityState, type BotProfile, type Connection, type Draft, type Execution,
  type Material, type OpenParams, type Operation, type ProductCommand,
} from './protocol.js';
import { openProductProfile, readPrivateConnection, saveCredential, writeProductFile, type PrivateConnection, type ProductProfilePaths, type StoredConnection } from './product-profile.js';

const emptyDraft = (): Draft => ({ revision: 0, text: '', materials: [] });
const newId = randomUUID;
const live = (execution?: StoredExecution) => execution?.state === 'starting' || execution?.state === 'running';
const unknown = (execution?: StoredExecution) => execution?.state === 'unknown';
const maxDraftBytes = 512 * 1024;
const maxPartialBytes = 512 * 1024;

export type ProductRunObservers = {
  started: () => void;
  text: (text: string) => void;
  settled: (state: 'completed' | 'failed' | 'interrupted', text: string, error?: string) => void;
  file: (path: string, callId: string) => void;
  toolFinished: (name: string, callId: string, isError: boolean) => void;
};

export type ProductSession = {
  sessionRef: string;
  history: () => SessionEntry[];
  start: (executionId: string, draft: Draft, observers: ProductRunObservers) => void;
  stop: (executionId: string) => void;
};

export type ProductEngine = {
  open: (params: OpenParams, tools: ToolDefinition[]) => Promise<ProductSession>;
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
  contextId: string; sessionRef: string; connectionRevision: number; profileRevision: number;
  cwd: string; instructions: string; codingTools: string[] | null; used: boolean;
};
type StoredExecution = Execution & {
  operationId: string;
  input: Draft;
  error?: string;
  engineRunId?: string;
  retained?: object;
};
type Conversation = {
  conversationId: string; botId: string; revision: number; draft: Draft; binding?: SessionBinding;
  latestExecutionId?: string;
};
type Bot = { botId: string; profile: BotProfile; profileRevision: number; archived: boolean; currentConversationId: string; retained?: object };
type Question = {
  questionId: string; executionId: string; text: string; revision: number;
  state: 'open' | 'answered' | 'superseded';
  answer?: { text: string; materials: Material[] };
  delivery?: 'saved' | 'dispatching' | 'delivered' | 'unknown' | 'canceled' | 'unavailable';
};
type ResultFile = { path: string; callId: string };
type ActivityResult = {
  activityId: string; executionId: string; text: string; delivery: 'available'; files: ResultFile[];
};
type Activity = {
  activityId: string; ownerBotId: string; originConversationId: string; revision: number;
  state: ActivityState; goal: string; requirements: string[]; questions: Question[];
  materials: Material[]; inputFiles: string[]; executionIds: string[]; results: ActivityResult[]; binding?: SessionBinding;
  retained?: object;
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
  preferences?: { selectedBotId?: string; layout?: object; themeIds?: object; appearance?: string };
  issueToolScopes?: object[];
  untargetedReceipts?: object[];
};
type ProductActor = { conversationId: string } | { activityId: string };
type LiveExecution = {
  owner: ProductActor;
  session: ProductSession;
  offset: number;
  partial: string | null;
  files: ResultFile[];
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
  if (!Value.Check(draftSchema, draft) || frameBytes(draft) > maxDraftBytes) stateError('invalid_params', 'The draft exceeds the saved text and materials limit');
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

function toolText(value: unknown, isError = false): ToolTextResult {
  return { content: [{ type: 'text' as const, text: typeof value === 'string' ? value : JSON.stringify(value) }], details: {}, isError };
}

export class Product {
  private profile: ProductProfile;
  private credentials: PrivateConnection | undefined;
  private stream?: { streamId: string; seq: number };
  private effectiveRevision: number | null;
  private closing = false;
  private readonly active = new Map<string, LiveExecution>();
  private readonly sessions = new Map<string, ProductSession>();
  private readonly waiters = new Map<string, AnswerWaiter>();

  constructor(private paths: ProductProfilePaths, private engine: ProductEngine, private sendFrame: (frame: object) => Promise<void>) {
    this.credentials = readPrivateConnection(paths.credentialsPath);
    this.profile = openProductProfile(paths, this.credentials);
    this.effectiveRevision = this.profile.appliedRevision;
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

  private commit<T>(mutate: (candidate: ProductProfile) => T): T {
    const candidate = structuredClone(this.profile);
    const result = mutate(candidate);
    writeProductFile(this.paths.profilePath, candidate);
    this.profile = candidate;
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
      ...this.activityView(activity), requirements: activity.requirements, materials: activity.materials, questions: activity.questions,
      executions: activity.executionIds.map(id => this.viewExecution(this.execution(this.profile, id))),
    };
  }

  private visibleActivities(botId: string) {
    this.bot(this.profile, botId);
    return this.profile.activities.filter(activity => activity.ownerBotId === botId || this.conversation(this.profile, activity.originConversationId).botId === botId);
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

  private emit(event: string, data: object): void {
    const stream = this.stream;
    if (!stream) return;
    void this.sendFrame({ v: 1, type: 'event', streamId: stream.streamId, seq: ++stream.seq, event, data }).catch(() => this.engine.disconnect());
  }

  async dispatch(command: ProductCommand, respond: (result: object) => Promise<void>): Promise<void> {
    const method = command.method;
    if (this.closing) stateError('closing', 'The app is shutting down');
    if (method === 'app.open') {
      if (!command.params.supportedVersions.includes(1)) stateError('unsupported_version', 'This worker supports product protocol v1');
      const stream = { streamId: newId(), seq: 0 };
      const bots = page(this.profile.bots, item => item.botId, 'bots');
      this.stream = stream;
      await respond({ version: 1, streamId: stream.streamId, eventSeq: stream.seq, snapshot: {
        bots: bots.items, nextBotCursor: bots.nextCursor, connection: this.connectionState(),
        ...(this.profile.preferences ? { preferences: this.profile.preferences } : {}),
      } });
      return;
    }
    if (!this.stream) stateError('not_open', 'Open the app protocol first');
    if (method === 'app.shutdown') {
      this.closing = true;
      await this.engine.drain();
      await respond({ drained: true });
      return;
    }
    if (method === 'connection.test') {
      await this.engine.test(command.params.configuration);
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
        return page(this.profile.bots, bot => bot.botId, 'bots', command.params.cursor, command.params.limit);
      case 'bot.create': {
        const params = command.params;
        const prior = this.sameOperation(method, params.operationId, params);
        if (prior) return prior.reply;
        this.validateProfile(params.profile);
        return this.commit(candidate => {
          const botId = newId();
          const conversationId = newId();
          const bot = { botId, profile: params.profile, profileRevision: 1, archived: false, currentConversationId: conversationId };
          candidate.bots.push(bot);
          candidate.conversations.push({ conversationId, botId, revision: 1, draft: emptyDraft() });
          return this.record(candidate, method, params, { operationId: params.operationId, state: 'accepted', target: { botId }, result: bot }, bot);
        });
      }
      case 'bot.update': {
        const params = command.params;
        return this.change(method, params, { botId: params.botId }, candidate => {
          const bot = this.bot(candidate, params.botId);
          requireRevision(bot.profileRevision, params.expectedRevision);
          if (bot.archived) stateError('conversation_read_only', 'Restore this Bot before editing');
          this.validateProfile(params.profile);
          if (params.startNewConversation) {
            const old = this.conversation(candidate, bot.currentConversationId);
            const execution = this.execution(candidate, old.latestExecutionId);
            if (live(execution) || unknown(execution)) stateError('busy', 'The current conversation execution is unresolved');
            const conversationId = newId();
            candidate.conversations.push({ conversationId, botId: bot.botId, revision: 1, draft: old.draft });
            old.draft = emptyDraft();
            old.revision++;
            bot.currentConversationId = conversationId;
          }
          bot.profile = params.profile;
          bot.profileRevision++;
          return bot;
        });
      }
      case 'bot.archive':
      case 'bot.restore': {
        const params = command.params;
        return this.change(method, params, { botId: params.botId }, candidate => {
          const bot = this.bot(candidate, params.botId);
          requireRevision(bot.profileRevision, params.expectedRevision);
          if (method === 'bot.archive') {
            const conversation = this.conversation(candidate, bot.currentConversationId);
            const related = [conversation.latestExecutionId, ...candidate.activities.filter(item => item.ownerBotId === bot.botId).map(item => item.executionIds.at(-1))];
            if (related.some(id => live(this.execution(candidate, id)) || unknown(this.execution(candidate, id)))) stateError('busy', 'The Bot has unresolved work');
          }
          bot.archived = method === 'bot.archive';
          bot.profileRevision++;
          return bot;
        });
      }
      case 'conversation.list': {
        const { botId, cursor, limit } = command.params;
        const bot = this.bot(this.profile, botId);
        const items = this.profile.conversations.filter(item => item.botId === botId).map(item => ({
          conversationId: item.conversationId, botId, revision: item.revision,
          mode: item.conversationId === bot.currentConversationId && !bot.archived ? 'current' : 'readOnly',
        }));
        return page(items, item => item.conversationId, `conversations:${botId}`, cursor, limit);
      }
      case 'conversation.get':
        return this.conversationView(this.conversation(this.profile, command.params.conversationId));
      case 'conversation.saveDraft': {
        const params = command.params;
        return this.change(method, params, { conversationId: params.conversationId }, candidate => {
          const conversation = this.current(candidate, params.conversationId);
          requireRevision(conversation.draft.revision, params.expectedRevision);
          const draft = { revision: conversation.draft.revision + 1, text: params.text, materials: params.materials };
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
      case 'operation.get':
        return this.operation(command.params.operationId);
    }
  }

  private validateProfile(profile: BotProfile): void {
    if (!Value.Check(profileSchema, profile) || !profile.name.trim() || profile.workspace && !isAbsolute(profile.workspace)) {
      stateError('invalid_params', 'The Bot requires a name and an absolute workspace when specified');
    }
  }

  private saveConnection(params: { operationId: string; configuration: Connection }) {
    const config = params.configuration;
    const visible = { operationId: params.operationId, configuration: nonsecret(config) };
    const prior = this.sameOperation('connection.save', params.operationId, visible, config.apiKey);
    if (prior) return prior.reply;
    const endpoint = URL.parse(config.baseUrl);
    if (!endpoint || !['http:', 'https:'].includes(endpoint.protocol) || endpoint.username || endpoint.password ||
        config.contextTokens - config.outputTokens < 4 || !isAbsolute(config.workingDirectory)) {
      stateError('invalid_params', 'The connection requires an HTTP(S) endpoint, a working directory and valid limits');
    }
    this.credentials = saveCredential(this.paths.credentialsPath, this.credentials, config);
    const { apiKey: _, ...configuration } = config;
    const sourceId = this.credentials.credential_source;
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
    if (!configuration) stateError('not_configured', 'Apply a connection first');
    if (this.active.size >= configuration.maxActiveTasks) stateError('busy', 'The configured execution capacity is full');
  }

  private async prepareSession(owner: ProductActor, existing: SessionBinding | undefined, bot: Bot) {
    const contextId = existing?.contextId ?? newId();
    const cached = this.sessions.get(contextId);
    if (cached && existing) return { session: cached, binding: existing };
    const configured = this.connection(this.profile, existing?.connectionRevision ?? this.effectiveRevision);
    if (!configured) stateError('not_configured', 'The captured connection is unavailable');
    const { configuration, revision: connectionRevision } = configured;
    const source = this.credentials?.sources[configured.sourceId];
    if (!source?.api_key) stateError('connection_unavailable', 'The captured credential source is unavailable');
    const activityId = 'activityId' in owner ? owner.activityId : undefined;
    const profileRevision = existing?.profileRevision ?? bot.profileRevision;
    const cwd = existing?.cwd ?? (bot.profile.workspace || configuration.workingDirectory);
    const instructions = existing?.instructions ?? [bot.profile.purpose, bot.profile.instructions].filter(Boolean).join('\n\n');
    const codingTools = existing ? existing.codingTools : bot.profile.codingTools;
    const session = await this.engine.open({
      contextId, botId: bot.botId, purpose: activityId ? 'activity' : 'conversation', activityId,
      selectionRevision: profileRevision, cwd, instructions, resumeRef: existing?.sessionRef,
      used: existing?.used ?? false, codingTools, domainTools: null,
      configuration: {
        wireApi: configuration.wireApi, baseUrl: configuration.baseUrl, apiKey: source.api_key,
        model: configuration.model, contextTokens: configuration.contextTokens, outputTokens: configuration.outputTokens,
        credentialSource: configured.sourceId,
      },
    }, this.tools(owner));
    this.sessions.set(contextId, session);
    return { session, binding: { contextId, sessionRef: session.sessionRef, connectionRevision, profileRevision, cwd, instructions, codingTools, used: existing?.used ?? false } };
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
    this.capacity();
    const { session, binding } = await this.prepareSession(owner, conversation.binding, this.bot(this.profile, conversation.botId));
    this.capacity();
    const execution = this.newExecution(params.operationId, conversation.draft);
    const receipt: Operation = { operationId: params.operationId, state: 'accepted', target: owner, execution: publicExecution(execution) };
    this.commit(candidate => {
      const current = this.current(candidate, params.conversationId);
      requireRevision(current.draft.revision, params.draftRevision);
      current.binding = binding;
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
    try {
      session.start(executionId, execution.input, {
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
        text: text => {
          const size = Buffer.byteLength(text);
          const offset = current.offset;
          current.offset += size;
          current.partial = current.partial !== null && current.offset <= maxPartialBytes ? current.partial + text : null;
          this.emit('execution.textDelta', { ...target, offset, text });
        },
        settled: (state, text, error) => guarded(() => this.settle(owner, executionId, state, text, current.files, error)),
        file: (path, callId) => current.files.push({ path, callId }),
        toolFinished: (name, callId, isError) => guarded(() => {
          if ('activityId' in owner && name === 'issue_wait') this.finishAnswerDelivery(owner.activityId, executionId, callId, isError);
        }),
      });
    } catch (error) {
      guarded(() => this.settle(owner, executionId, 'failed', '', [], this.engine.cleanMessage(error)));
    }
  }

  private finishAnswerDelivery(activityId: string, executionId: string, callId: string, isError: boolean): void {
    const id = `${executionId}:${callId}`;
    const activity = this.activity(this.profile, activityId);
    const pending = activity.questions.find(question => question.questionId === id);
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

  private settle(owner: ProductActor, executionId: string, state: 'completed' | 'failed' | 'interrupted', text: string, files: ResultFile[], error?: string): void {
    const run = this.commit(candidate => {
      const execution = this.execution(candidate, executionId)!;
      execution.state = state;
      execution.revision++;
      execution.error = error;
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
        activity.results.push({ activityId: owner.activityId, executionId, text, delivery: 'available', files });
      }
      return execution;
    });
    this.active.delete(executionId);
    this.emit('execution.settled', { ...owner, executionId, operationId: run.operationId, revision: run.revision, state, ...(error ? { error } : {}) });
    if ('activityId' in owner) this.emit('activity.changed', this.activityView(this.activity(this.profile, owner.activityId)));
  }

  private async readImportedActivityResult(activity: Activity, executionId: string): Promise<ActivityResult> {
    const execution = this.execution(this.profile, executionId);
    if (!execution?.engineRunId || !activity.binding?.used || live(execution) || unknown(execution)) {
      stateError('result_unavailable', 'This execution has no verified result');
    }
    const { session, binding } = await this.prepareSession({ activityId: activity.activityId }, activity.binding, this.bot(this.profile, activity.ownerBotId));
    let found = false;
    let final: AssistantMessage | undefined;
    const calls = new Map<string, ToolCall | null>();
    const files: ResultFile[] = [];
    for (const entry of session.history()) {
      if (entry.type === 'custom' && entry.customType === 'nagi-turn-v1') {
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
    return { activityId: activity.activityId, executionId, text, files, delivery: 'available' };
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
    if (!conversation.binding?.used) return { items: [], nextCursor: null };
    const { session } = await this.prepareSession({ conversationId }, conversation.binding, this.bot(this.profile, conversation.botId));
    const messages: { messageId: string; role: 'user' | 'assistant'; timestamp: string; text: string; materials: Material[] }[] = [];
    for (const entry of session.history()) {
      if (entry.type === 'custom' && entry.customType === 'nagi-turn-v1') {
        const turn = entry.data as { text: string; materials?: Material[]; context?: { name: string; text: string }[] };
        const materials = turn.materials ?? (turn.context ?? []).filter(item => !item.name.startsWith('nagi.issue_tools.')).map((item, index) => ({ id: `${entry.id}:${index}`, revision: 0, name: item.name, text: item.text }));
        messages.push({ messageId: entry.id, role: 'user', timestamp: entry.timestamp, text: turn.text, materials });
      } else if (entry.type === 'message' && entry.message.role === 'assistant') {
        const text = entry.message.content.filter(part => part.type === 'text').map(part => part.text).join('');
        if (text) messages.push({ messageId: entry.id, role: 'assistant', timestamp: entry.timestamp, text, materials: [] });
      }
    }
    for (let count = limit; count > 0; count--) {
      const result = page(messages, item => item.messageId, `history:${conversationId}`, cursor, count);
      if (frameBytes(result) < MAX_FRAME - 1024) return result;
    }
    stateError('entry_too_large', 'An individual history entry exceeds the frame limit');
  }

  private async continueActivity(params: { activityId: string; operationId: string; expectedRevision: number; text: string; materials: Material[] }) {
    return this.startActivity('activity.continue', params);
  }

  private async startActivity(method: string, params: { activityId: string; operationId: string; expectedRevision: number; text: string; materials: Material[] }) {
    const prior = this.sameOperation(method, params.operationId, params);
    if (prior) return this.operation(params.operationId);
    const owner = { activityId: params.activityId };
    const activity = this.activity(this.profile, params.activityId);
    requireRevision(activity.revision, params.expectedRevision);
    const bot = this.bot(this.profile, activity.ownerBotId);
    const last = this.execution(this.profile, activity.executionIds.at(-1));
    if (bot.archived || live(last) || unknown(last)) stateError('busy', 'The activity is not ready to continue');
    this.capacity();
    const input = { revision: activity.revision, text: [activity.goal, ...activity.requirements, params.text, ...activity.inputFiles.map(path => `Input file: ${path}`)].filter(Boolean).join('\n'),
      materials: params.materials };
    validateDraft(input);
    const { session, binding } = await this.prepareSession(owner, activity.binding, bot);
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
    });
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
    const result = this.change(method, params, target, candidate => {
      const activity = this.activity(candidate, activityId);
      const question = this.question(activity, questionId);
      requireRevision(question.revision, params.questionRevision);
      if (question.executionId !== executionId || question.state !== 'open') stateError('question_conflict', 'The answer does not target this unanswered execution');
      question.answer = { text, materials };
      question.state = 'answered';
      question.delivery = 'saved';
      question.revision++;
      activity.revision++;
      return { ...target, executionId, revision: question.revision, answerSaved: true, delivery: question.delivery };
    });
    this.emit('activity.changed', this.activityView(this.activity(this.profile, activityId)));
    if (waiter) {
      this.waiters.delete(questionId);
      waiter.resolve({ text, materials });
    }
    return result;
  }

  private tools(owner: ProductActor): ToolDefinition[] {
    if ('activityId' in owner) return [{
      name: 'issue_wait', label: 'Ask about the activity', description: 'Ask the user for information needed to finish this activity.',
      parameters: Type.Object({ text: Type.String({ minLength: 1 }) }),
      execute: async (callId, input, signal) => {
        if (!Value.Check(Type.Object({ text: Type.String({ minLength: 1 }) }), input)) return toolText('A question requires text', true);
        return this.waitForAnswer(owner.activityId, callId, input.text, signal);
      },
    }];
    const list = Type.Object({});
    const target = Type.Object({ issue_id: Type.String({ minLength: 1 }) });
    const handoff = Type.Object({ goal: Type.String({ minLength: 1 }), key: Type.String({ minLength: 1 }),
      assignee_id: Type.Optional(Type.String()), requirements: Type.Optional(Type.Array(Type.String())), files: Type.Optional(Type.Array(Type.String(), { maxItems: 16 })) });
    const change = Type.Object({ issue_id: Type.String({ minLength: 1 }), action: Type.Union([Type.Literal('stop'), Type.Literal('add_supplement'), Type.Literal('resume')]), text: Type.Optional(Type.String()) });
    const botId = () => this.conversation(this.profile, owner.conversationId).botId;
    const authorized = (activityId: string) => this.visibleActivities(botId()).find(activity => activity.activityId === activityId) ?? stateError('scope_denied', 'The activity is outside this conversation scope');
    return [
      { name: 'bot_list', label: 'List Bots', description: 'Find the exact ID of an available Bot before an explicit handoff.', parameters: list,
        execute: async () => toolText(this.profile.bots.filter(bot => !bot.archived && bot.botId !== botId()).map(({ botId, profile }) => ({ bot_id: botId, name: profile.name, role: profile.role }))) },
      { name: 'issue_list', label: 'List activities', description: 'Read activity state for this Bot.', parameters: list,
        execute: async () => toolText(this.visibleActivities(botId()).map(activity => this.activityView(activity))) },
      { name: 'issue_get', label: 'Read activity', description: 'Read the exact activity, questions, and execution results.', parameters: target,
        execute: async (_callId, input) => {
          if (!Value.Check(target, input)) return toolText('An activity ID is required', true);
          const activity = authorized(input.issue_id);
          return toolText({ ...this.activityDetails(activity), results: activity.results });
        } },
      { name: 'delegate_activity', label: 'Delegate activity', description: 'Start independently tracked work only when the user assigns it. Use the optional receiving Bot ID for an explicit handoff.', parameters: handoff,
        execute: async (_callId, input) => {
          if (!Value.Check(handoff, input)) return toolText('A stable key and a goal are required', true);
          return toolText(await this.delegate(owner.conversationId, input));
        } },
      { name: 'issue_action', label: 'Act on activity', description: 'Stop or continue the exact activity requested by the user.', parameters: change,
        execute: async (callId, input) => {
          if (!Value.Check(change, input)) return toolText('An activity ID and action are required', true);
          const activity = authorized(input.issue_id);
          const executionId = activity.executionIds.at(-1);
          const source = this.conversation(this.profile, owner.conversationId);
          const operationId = `${source.latestExecutionId}:${callId}`;
          if (input.action === 'stop') {
            if (!executionId) return toolText('The activity has no execution', true);
            return toolText(this.stop('activity.stop', { operationId, executionId }, { activityId: activity.activityId }));
          }
          return toolText(await this.continueActivity({ operationId, activityId: activity.activityId, expectedRevision: activity.revision,
            text: input.text ?? '', materials: this.execution(this.profile, source.latestExecutionId)?.input.materials ?? [] }));
        } },
    ];
  }

  private async delegate(originConversationId: string, input: { key: string; goal: string; assignee_id?: string; files?: string[]; requirements?: string[] }) {
    const conversation = this.conversation(this.profile, originConversationId);
    const source = this.execution(this.profile, conversation.latestExecutionId);
    if (!source || !this.active.has(source.executionId)) stateError('scope_denied', 'The conversation execution no longer owns this delegation');
    const operationId = `${source.executionId}:delegate:${input.key}`;
    const params = { originConversationId, ...input };
    const prior = this.sameOperation('activity.delegate', operationId, params);
    if (prior) return this.operation(operationId);
    const bot = this.bot(this.profile, input.assignee_id || conversation.botId);
    if (bot.archived) stateError('scope_denied', 'The receiving Bot is archived');
    const files = input.files ?? [];
    const permitted = new Set([...source.input.materials.map(item => item.source), ...this.visibleActivities(conversation.botId).flatMap(activity => activity.results.flatMap(result => result.files.map(file => file.path)))]);
    if (files.some(path => !isAbsolute(path) || !permitted.has(path))) stateError('scope_denied', 'A delegated input file must come from user materials or a visible result');
    const activity: Activity = { activityId: newId(), ownerBotId: bot.botId, originConversationId, revision: 1, state: 'notStarted',
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
        resolve(toolText(answer));
      } catch (error) {
        reject(error);
      }
    } });
    signal?.addEventListener('abort', abort, { once: true });
    this.emit('activity.changed', this.activityView(this.activity(this.profile, activityId)));
    return promise;
  }
}
