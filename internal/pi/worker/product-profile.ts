import { randomUUID } from 'node:crypto';
import { readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { RequestError, type ActivityState, type Connection, type Draft, type Execution, type Material, type Operation } from './protocol.js';
import type { ProductProfile } from './product.js';

export type ProductProfilePaths = {
  profilePath: string;
  credentialsPath: string;
  fresh: boolean;
};

type Credential = { api: 'openai-responses' | 'openai-chat-completions'; endpoint: string; api_key: string };
export type PrivateConnection = {
  version: 2; credential_source: string; model: string; context_tokens: number; output_tokens: number;
  working_directory: string; max_active_tasks: number; sources: Record<string, Credential>;
};

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
  if (!data || typeof data !== 'object' || !('version' in data) || data.version !== 2 || !('sources' in data) || !data.sources || typeof data.sources !== 'object') {
    throw new RequestError('connection_unavailable', 'The private connection file is not a v2 source store');
  }
  return data as PrivateConnection;
}

export function saveCredential(path: string, existing: PrivateConnection | undefined, configuration: Connection): PrivateConnection {
  const api = configuration.wireApi === 'openai-responses' ? 'openai-responses' : 'openai-chat-completions';
  const source: Credential = { api, endpoint: configuration.baseUrl, api_key: configuration.apiKey };
  const sourceId = Object.entries(existing?.sources ?? {}).find(([, value]) => value.api === source.api && value.endpoint === source.endpoint && value.api_key === source.api_key)?.[0] ?? randomUUID();
  const next: PrivateConnection = {
    version: 2, credential_source: sourceId, model: configuration.model, context_tokens: configuration.contextTokens,
    output_tokens: configuration.outputTokens, working_directory: configuration.workingDirectory, max_active_tasks: configuration.maxActiveTasks,
    sources: { ...existing?.sources, [sourceId]: source },
  };
  writeProductFile(path, next);
  return next;
}

export function openProductProfile(paths: ProductProfilePaths, credentials: PrivateConnection | undefined): ProductProfile {
  const empty: ProductProfile = {
    version: 1, bots: [], conversations: [], activities: [], executions: [], operations: [], connections: [], savedRevision: null, appliedRevision: null,
  };
  let data: unknown;
  try { data = JSON.parse(readFileSync(paths.profilePath, 'utf8')); }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT' || !paths.fresh) throw error;
    writeProductFile(paths.profilePath, empty);
    return empty;
  }
  if (!data || typeof data !== 'object' || !('version' in data)) throw new RequestError('profile_unavailable', 'The product profile has an invalid format');
  if (data.version === 1 && ['bots', 'conversations', 'activities', 'executions', 'operations', 'connections'].every(key => Array.isArray((data as Record<string, unknown>)[key]))) return data as ProductProfile;
  if (data.version !== 10 || paths.fresh || !('contacts' in data) || !Array.isArray(data.contacts) || !data.contacts.length) {
    throw new RequestError('profile_unavailable', 'The product profile format is unsupported');
  }
  const profile = importCurrentProfile(data as OldProfile, empty, credentials);
  writeProductFile(paths.profilePath, profile);
  return profile;
}

function importCurrentProfile(old: OldProfile, profile: ProductProfile, credentials: PrivateConnection | undefined): ProductProfile {
  profile.preferences = { selectedBotId: old.selected, layout: old.layout, themeIds: old.theme_ids, appearance: old.appearance };
  profile.issueToolScopes = old.issue_tool_scopes;
  if (credentials) {
    const defaultSource = credentials.sources[credentials.credential_source];
    if (!defaultSource) throw new RequestError('connection_unavailable', 'The saved credential source is missing');
    profile.connections.push({ revision: 1, sourceId: credentials.credential_source, configuration: {
      wireApi: defaultSource.api === 'openai-responses' ? 'openai-responses' : 'openai-completions', baseUrl: defaultSource.endpoint,
      model: credentials.model, contextTokens: credentials.context_tokens, outputTokens: credentials.output_tokens,
      workingDirectory: credentials.working_directory, maxActiveTasks: credentials.max_active_tasks,
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
        engineRunId: execution.pi_run?.run_id ?? execution.task_id, retained: execution });
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
