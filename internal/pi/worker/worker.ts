import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readFile, realpath, stat } from 'node:fs/promises';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import {
  createAgentSessionFromServices, createAgentSessionRuntime, createAgentSessionServices,
  createReadToolDefinition, createBashToolDefinition, createEditToolDefinition, createWriteToolDefinition,
  createGrepToolDefinition, createFindToolDefinition, createLsToolDefinition,
  ModelRuntime, SessionManager, SettingsManager, createMcpExtension, McpOAuthCredentialStore, InMemoryAuthStorageBackend,
  parseSessionEntries,
  type AgentSessionEvent, type AgentSessionRuntime, type ToolDefinition, type McpServerEntry, type McpTransportFactory,
} from '@earendil-works/pi-coding-agent';
import { InMemoryCredentialStore, type AssistantMessage } from '@earendil-works/pi-ai';
import { StdioTransport, StreamableHttpTransport, validateMcpServerConfig, mcpNamespace } from '@earendil-works/pi-mcp';
import type { TSchema } from 'typebox';
import {
  MAX_FRAME, RequestError, Writer, canonicalJSON, frameBytes, message, parseProductRequest, parseRequest,
  type Command, type ConfigureParams, type Connection, type Draft, type OpenParams, type StartParams, type ToolResult,
} from './protocol.js';
import { Product, confirmedFileEffect, type ProductRunObservers, type ProductSession } from './product.js';
import type { ProductProfilePaths } from './product-profile.js';
import { isNagiBotSystemPrompt } from './bot-template.js';

const BINDING_TYPE = 'nagi-context-v1';
const CONFIGURATION_TYPE = 'nagi-configuration-v1';
const MCP_TYPE = 'nagi-mcp-v1';
const TURN_TYPE = 'nagi-turn-v1';
const PROVIDER = 'nagi';
type Binding = Pick<OpenParams, 'contextId' | 'botId' | 'purpose' | 'activityId' | 'selectionRevision' | 'cwd'>;
type Run = {
  id: string; seq: number; cancelled: boolean; sdkStarted: boolean; sdkSettled: boolean; sdkAborted: boolean;
  final?: AssistantMessage; correlation: StartParams['correlation']; done: Promise<void>;
  pending: Map<string, (result: ToolResult) => void>; inputs: Map<string, unknown>;
  product?: { observers: ProductRunObservers; draft: Draft };
};
type Context = {
  binding: Binding; configuration: ConfigureParams; instructions: string; codingTools: OpenParams['codingTools']; domainTools: OpenParams['domainTools']; mcpServers: NonNullable<OpenParams['mcpServers']>;
  manager: SessionManager; runtime: AgentSessionRuntime; used: boolean; runIds: Set<string>;
  active?: Run; observationSeq: number;
};

class Worker {
  readonly epoch = randomUUID();
  readonly contexts = new Map<string, Context>();
  readonly writer = new Writer(process.stdout, () => { void this.disconnect(true); });
  private configured?: ConfigureParams;
  private mcpSequences = new Map<string, { id: string; seq: number }>();
  private closing = false;
  private disconnected = false;
  private shutdownComplete = false;
  private readonly product?: Product;
  readonly builtinCatalog: { supported: { name: string; description: string }[]; defaults: string[] };
  private readonly knownBuiltinTools: Set<string>;
  constructor(private stateDir: string, productPaths?: ProductProfilePaths) {
    const definitions = [createReadToolDefinition, createBashToolDefinition, createEditToolDefinition, createWriteToolDefinition,
      createGrepToolDefinition, createFindToolDefinition, createLsToolDefinition].map(factory => factory(stateDir));
    this.builtinCatalog = { supported: definitions.map(({ name, description }) => ({ name, description })),
      defaults: definitions.slice(0, 4).map(({ name }) => name) };
    this.knownBuiltinTools = new Set(this.builtinCatalog.supported.map(tool => tool.name));
    if (productPaths) this.product = new Product(productPaths, {
      open: (params, tools) => this.openProductSession(params, tools),
      refreshSession: (params, tools) => this.refreshProductSession(params, tools),
      conversationPreview: (sessionRef, contextId, botId) => this.conversationPreview(sessionRef, contextId, botId),
      test: configuration => this.testProductConnection(configuration),
      drain: () => this.drainProduct(),
      disconnect: () => { void this.disconnect(true); },
      cleanMessage: error => this.cleanMessage(error),
    }, frame => this.writer.send(frame));
  }
  private response(id: string, result: object): Promise<void> {
    return this.writer.send({ v: 1, type: 'response', id, ok: true, result });
  }
  private failure(id: string, error: unknown): Promise<void> {
    return this.writer.send({ v: 1, type: 'response', id, ok: false, error: {
      code: error instanceof RequestError ? error.code : 'request_failed',
      message: this.cleanMessage(error),
      ...(error instanceof RequestError && error.details ? { details: error.details } : {}),
    } });
  }
  private cleanMessage(error: unknown, apiKey?: string): string {
    const text = message(error);
    const keys = [apiKey, this.configured?.apiKey, ...[...this.contexts.values()].flatMap(context => [context.configuration.apiKey,
      ...context.mcpServers.flatMap(server => [server.config.env, server.config.headers].flatMap(values => values && typeof values === 'object' ? Object.values(values).filter((value): value is string => typeof value === 'string') : []))])];
    return keys.reduce<string>((value, key) => key ? value.split(key).join('[credential]') : value, text);
  }
  private event(context: Pick<Context, 'binding'>, run: Pick<Run, 'id' | 'seq'>, event: string, data: object): void {
    const value = { v: 1, type: 'event', workerEpoch: this.epoch, contextId: context.binding.contextId, runId: run.id, seq: ++run.seq, event, data };
    try { void this.writer.send(value).catch(() => this.disconnect(true)); }
    catch { void this.disconnect(true); }
  }
  private context(id: string): Context {
    const context = this.contexts.get(id);
    if (!context) throw new RequestError('not_found', 'Context is not open');
    return context;
  }
  private binding(context: Context): object {
    return { ...context.binding, sessionRef: context.manager.getSessionFile(), used: context.used };
  }
  private onSdkEvent(context: Context, event: AgentSessionEvent): void {
    context.observationSeq++;
    const run = context.active;
    if (!run) return;
    if (event.type === 'agent_start') {
      run.sdkStarted = true;
      run.product?.observers.started();
      if (run.cancelled || this.disconnected) void context.runtime.session.abort().catch(() => this.disconnect(true));
    }
    if (event.type === 'agent_settled') { run.sdkSettled = true; run.sdkAborted = event.aborted; }
    if (event.type === 'message_end' && event.message.role === 'assistant') run.final = event.message;
    if (event.type === 'message_update' && event.assistantMessageEvent.type === 'text_delta') {
      const text = event.assistantMessageEvent.delta;
      if (run.product) run.product.observers.text(text);
      else this.event(context, run, 'text.delta', { text });
    }
    if (event.type === 'tool_execution_start') run.inputs.set(event.toolCallId, event.args);
    if (event.type === 'tool_execution_end') {
      const input = run.inputs.get(event.toolCallId);
      if (!run.product) {
        this.event(context, run, 'tool.finished', { callId: event.toolCallId, name: event.toolName, input, isError: event.isError, result: event.result });
      } else if (!event.isError && event.result && typeof event.result === 'object' && 'content' in event.result) {
        const path = confirmedFileEffect(context.binding.cwd, event.toolName, input, event.result.content);
        if (path) run.product.observers.file(path, event.toolCallId);
      }
      run.product?.observers.toolFinished(event.toolName, event.toolCallId, event.isError);
      run.inputs.delete(event.toolCallId);
    }
  }
  private domainTool(contextId: string, tool: NonNullable<OpenParams['domainTools']>[number]): ToolDefinition {
    return {
      name: tool.name, label: tool.name, description: tool.description, parameters: tool.parameters as TSchema,
      execute: (callId, input, signal) => {
        const context = this.context(contextId);
        const run = context.active;
        if (!run || signal?.aborted) return Promise.reject(new Error('Tool cancelled'));
        return new Promise((accept, reject) => {
          const cleanup = () => { run.pending.delete(callId); signal?.removeEventListener('abort', abort); };
          const abort = () => { cleanup(); reject(new Error('Tool cancelled')); };
          run.pending.set(callId, result => {
            cleanup();
            if (result.error) reject(new Error(result.error.message));
            else accept({ content: result.content ?? [], details: result.details ?? {} });
          });
          signal?.addEventListener('abort', abort, { once: true });
          this.event(context, run, 'tool.request', { callId, name: tool.name, input });
        });
      },
    };
  }
  private validatedConfiguration(params: ConfigureParams): ConfigureParams {
    if (params.outputTokens >= params.contextTokens) throw new RequestError('invalid_params', 'Output limit must be below context limit');
    const endpoint = new URL(params.baseUrl);
    if (!['http:', 'https:'].includes(endpoint.protocol) || endpoint.username || endpoint.password) throw new RequestError('invalid_params', 'HTTP(S) endpoint required without URL credentials');
    const config = { ...params, baseUrl: endpoint.toString() };
    for (const context of this.contexts.values()) {
      const current = context.configuration;
      if (current.credentialSource === config.credentialSource && (current.wireApi !== config.wireApi || current.baseUrl !== config.baseUrl || current.apiKey !== config.apiKey)) throw new RequestError('source_conflict', 'Credential source is already bound to another connection snapshot');
    }
    return config;
  }
  private nonsecretConfiguration(config: ConfigureParams): object {
    return { wireApi: config.wireApi, baseUrl: config.baseUrl, model: config.model, contextTokens: config.contextTokens, outputTokens: config.outputTokens, credentialSource: config.credentialSource };
  }
  private configure(params: ConfigureParams): void {
    this.configured = this.validatedConfiguration(params);
  }
  private registerModel(models: ModelRuntime, config: ConfigureParams): void {
    models.registerProvider(PROVIDER, { api: config.wireApi, baseUrl: config.baseUrl, models: [{
      id: config.model, name: config.model, reasoning: false, input: ['text'],
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      contextWindow: config.contextTokens, maxTokens: config.outputTokens,
    }] });
  }
  private configuration(context: Context): object {
    const model = context.runtime.session.model;
    if (!model) throw new RequestError('not_configured', 'Context model is unavailable');
    return { contextId: context.binding.contextId, wireApi: model.api, baseUrl: model.baseUrl, model: model.id,
      contextTokens: model.contextWindow, outputTokens: model.maxTokens,
      credentialSource: context.configuration.credentialSource,
      credentialPresent: context.runtime.services.modelRuntime.hasConfiguredAuth(PROVIDER),
      activeTools: context.runtime.session.getActiveToolNames() };
  }
  private async configureContext(context: Context, settings: Extract<Command, { method: 'context.configure' }>['params']['settings']): Promise<void> {
    if (context.active) throw new RequestError('busy', 'Context is running');
    const config = this.validatedConfiguration({ ...context.configuration, ...settings });
    const models = context.runtime.services.modelRuntime;
    this.registerModel(models, config);
    const model = models.getModel(PROVIDER, config.model);
    if (!model) throw new Error('Configured model is unavailable');
    await context.runtime.session.setModel(model);
    this.configureSettings(context.runtime.services.settingsManager, config);
    context.configuration = config;
    context.manager.appendCustomEntry(CONFIGURATION_TYPE, this.nonsecretConfiguration(config));
    context.observationSeq++;
  }
  private configureSettings(settings: SettingsManager, params: ConfigureParams): void {
    settings.applyOverrides({
      compaction: { enabled: true, reserveTokens: params.outputTokens, keepRecentTokens: Math.min(8192, Math.floor((params.contextTokens - params.outputTokens) / 4)) },
      retry: { enabled: false, provider: { maxRetries: 0 } }, cacheWarming: 'off',
    });
  }
  private mcpEntries(servers: NonNullable<OpenParams['mcpServers']>): McpServerEntry[] {
    if (new Set(servers.map(server => mcpNamespace(server.name))).size !== servers.length) throw new RequestError('invalid_params', 'Conflicting MCP server names');
    return servers.map(server => {
      const validated = validateMcpServerConfig(server.name, server.config);
      if (validated.kind === 'invalid') throw new RequestError('invalid_params', validated.error);
      if (validated.config.oauth !== undefined || validated.config.auth !== undefined) throw new RequestError('unsupported_auth', 'MCP OAuth and provider-token authentication are not supported');
      if (validated.kind === 'stdio' && !isAbsolute(validated.config.command)) throw new RequestError('invalid_params', 'MCP command must be absolute');
      if (validated.kind === 'http') {
        const url = new URL(validated.config.url);
        if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) throw new RequestError('invalid_params', 'MCP requires HTTP(S) without URL credentials');
      }
      for (const context of this.contexts.values()) {
        const current = context.mcpServers.find(item => item.sourceId === server.sourceId);
        if (current && JSON.stringify(current) !== JSON.stringify(server)) throw new RequestError('source_conflict', 'MCP source is already bound to another configuration snapshot');
      }
      return { name: server.name, source: server.sourceId, scope: 'extension', config: { ...validated.config, exposure: 'direct', toolExposure: {} } };
    });
  }
  private mcpExtension(binding: Binding, servers: McpServerEntry[]) {
    let notifications = this.mcpSequences.get(binding.contextId);
    if (!notifications) {
      notifications = { id: '', seq: 0 };
      this.mcpSequences.set(binding.contextId, notifications);
    }
    const sequence = notifications;
    const createTransport: McpTransportFactory = (entry, cwd, authProvider) => {
      if ('url' in entry.config) return new StreamableHttpTransport({ url: entry.config.url, headers: entry.config.headers, authProvider });
      const attemptId = randomUUID();
      return new StdioTransport({ command: entry.config.command, args: entry.config.args, cwd: resolve(cwd, entry.config.cwd ?? '.'), env: entry.config.env, stderr: 'pipe',
        onProcessEvent: event => this.event({ binding }, sequence, 'mcp.process', { server: entry.name, sourceId: entry.source, attemptId, ...event,
          ...(event.kind === 'spawn_error' ? { error: { ...event.error, message: this.cleanMessage(event.error.message) } } : {}) }),
      });
    };
    return createMcpExtension({ loadConfig: () => ({ servers, autoEnableCodemode: false, errors: [] }), createTransport,
      credentials: new McpOAuthCredentialStore(new InMemoryAuthStorageBackend()),
      logPath: join(this.stateDir, 'agent', 'mcp.log'),
      openUrl: () => { throw new RequestError('unsupported_auth', 'MCP browser authentication is unavailable'); },
      updateConfig: () => { throw new RequestError('unsupported_operation', 'Change MCP configuration through Nagi'); },
    });
  }
  private async open(params: OpenParams, productTools: ToolDefinition[] = [], inMemory = false): Promise<Context> {
    const source = params.configuration ?? this.configured;
    if (!source) throw new RequestError('not_configured', 'Configure first');
    const config = this.validatedConfiguration(source);
    if (!isAbsolute(params.cwd)) throw new RequestError('invalid_params', 'Absolute project directory required');
    const cwd = await realpath(params.cwd);
    const binding: Binding = { contextId: params.contextId, botId: params.botId, purpose: params.purpose, ...(params.activityId ? { activityId: params.activityId } : {}), selectionRevision: params.selectionRevision, cwd };
    const mcpServers = params.mcpServers ?? [];
    const mcpEntries = this.mcpEntries(mcpServers);
    const mcpBinding = mcpServers.map(server => ({ name: server.name, sourceId: server.sourceId,
      configSha256: createHash('sha256').update(canonicalJSON(server.config)).digest('hex') }));
    const existing = this.contexts.get(params.contextId);
    if (existing) {
      if (existing.active) throw new RequestError('busy', 'Context is running');
      if (JSON.stringify(existing.binding) !== JSON.stringify(binding) || JSON.stringify(existing.configuration) !== JSON.stringify(config) || existing.instructions !== params.instructions || JSON.stringify(existing.codingTools) !== JSON.stringify(params.codingTools) || JSON.stringify(existing.domainTools) !== JSON.stringify(params.domainTools) || JSON.stringify(existing.mcpServers) !== JSON.stringify(mcpServers) || (params.resumeRef && existing.manager.getSessionFile() !== params.resumeRef)) throw new RequestError('binding_conflict', 'Open context configuration differs');
      return existing;
    }
    const directory = join(this.stateDir, 'sessions');
    let manager: SessionManager;
    let used = params.used;
    if (inMemory) {
      manager = SessionManager.inMemory(cwd);
    } else if (params.resumeRef) {
      const path = resolve(params.resumeRef);
      if (dirname(path) !== directory) throw new RequestError('invalid_resume', 'Session reference is outside the owned directory');
      let unavailable = false;
      try {
        const info = await stat(path);
        if (!info.isFile()) throw new RequestError('history_unavailable', 'Session reference is not a regular file');
        unavailable = info.size === 0;
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
        unavailable = true;
      }
      if (unavailable) {
        if (params.used) throw new RequestError('history_unavailable', 'A used session is missing or empty');
        manager = SessionManager.create(cwd, directory);
        manager.appendCustomEntry(BINDING_TYPE, binding);
        manager.appendCustomEntry(CONFIGURATION_TYPE, this.nonsecretConfiguration(config));
        if (mcpBinding.length) manager.appendCustomEntry(MCP_TYPE, mcpBinding);
      } else {
        if (await realpath(path) !== path) throw new RequestError('invalid_resume', 'Session reference is not canonical');
        if ([...this.contexts.values()].some(context => context.manager.getSessionFile() === path)) throw new RequestError('binding_conflict', 'Session already open');
        manager = SessionManager.open(path, directory, cwd);
        const saved = manager.getEntries().find(entry => entry.type === 'custom' && entry.customType === BINDING_TYPE);
        if (!saved || saved.type !== 'custom' || JSON.stringify(saved.data) !== JSON.stringify(binding)) throw new RequestError('binding_conflict', 'Saved context owner differs');
        const savedConfig = manager.getEntries().findLast(entry => entry.type === 'custom' && entry.customType === CONFIGURATION_TYPE);
        if (!savedConfig || savedConfig.type !== 'custom' || JSON.stringify(savedConfig.data) !== JSON.stringify(this.nonsecretConfiguration(config))) throw new RequestError('binding_conflict', 'Captured connection configuration differs from the saved context');
        const savedMcp = manager.getEntries().findLast(entry => entry.type === 'custom' && entry.customType === MCP_TYPE);
        if (JSON.stringify(savedMcp?.type === 'custom' ? savedMcp.data : []) !== JSON.stringify(mcpBinding)) throw new RequestError('binding_conflict', 'Captured MCP sources differ from the saved context');
        used = used || manager.getEntries().some(entry => entry.type === 'message');
      }
    } else {
      if (params.used) throw new RequestError('history_unavailable', 'A used context requires its session reference');
      manager = SessionManager.create(cwd, directory);
      manager.appendCustomEntry(BINDING_TYPE, binding);
      manager.appendCustomEntry(CONFIGURATION_TYPE, this.nonsecretConfiguration(config));
      if (mcpBinding.length) manager.appendCustomEntry(MCP_TYPE, mcpBinding);
    }
    const models = await ModelRuntime.create({ credentials: new InMemoryCredentialStore(), modelsPath: null, allowModelNetwork: false, refreshOnCreate: false });
    this.registerModel(models, config);
    await models.setRuntimeApiKey(PROVIDER, config.apiKey);
    const model = models.getModel(PROVIDER, config.model);
    if (!model) throw new Error('Configured model is unavailable');
    const customTools = [...(params.domainTools ?? []).map(tool => this.domainTool(params.contextId, tool)), ...productTools]
      .sort((a, b) => a.name.localeCompare(b.name));
    const codingTools = [...(params.codingTools ?? this.builtinCatalog.defaults)].sort((a, b) => a.localeCompare(b));
    if (codingTools.some(name => !this.knownBuiltinTools.has(name))) throw new RequestError('unsupported_tool', 'Unsupported coding tool selection');
    if (new Set(customTools.map(tool => tool.name)).size !== customTools.length || customTools.some(tool => this.knownBuiltinTools.has(tool.name))) throw new RequestError('invalid_params', 'Conflicting domain tool name');
    const tools = [...codingTools, ...customTools.map(tool => tool.name)].sort((a, b) => a.localeCompare(b));
    if (mcpEntries.length) tools.push('mcp__*');
    const productPrompt = productTools.length > 0 && isNagiBotSystemPrompt(params.instructions);
    const settings = SettingsManager.inMemory({ retry: { enabled: false, provider: { maxRetries: 0 } }, cacheWarming: 'off' });
    this.configureSettings(settings, config);
    const agentDir = join(this.stateDir, 'agent');
    const runtime = await createAgentSessionRuntime(async target => {
      const services = await createAgentSessionServices({
        ...target, modelRuntime: models, settingsManager: settings,
        resourceLoaderOptions: { noExtensions: true, noSkills: true, noPromptTemplates: true, noThemes: true, disabledBuiltinExtensions: ['mcp'],
          extensionFactories: mcpEntries.length ? [this.mcpExtension(binding, mcpEntries)] : [],
          ...(productPrompt ? { systemPrompt: params.instructions } : { appendSystemPrompt: params.instructions ? [params.instructions] : [] }) },
      });
      const result = await createAgentSessionFromServices({ services, sessionManager: target.sessionManager, model, thinkingLevel: 'off', tools, customTools });
      return { ...result, services, diagnostics: services.diagnostics };
    }, { cwd, agentDir, sessionManager: manager });
    const context: Context = { binding, configuration: config, instructions: params.instructions, codingTools: params.codingTools, domainTools: params.domainTools, mcpServers, manager, runtime, used, runIds: new Set(), observationSeq: 0 };
    runtime.session.subscribe(event => this.onSdkEvent(context, event));
    this.contexts.set(params.contextId, context);
    try { await runtime.session.bindExtensions({}); }
    catch (error) { await runtime.dispose(); this.contexts.delete(params.contextId); throw error; }
    return context;
  }
  private async execute(context: Context, run: Run, params: StartParams): Promise<void> {
    let failure: unknown;
    if (!run.product) this.event(context, run, 'run.started', { correlation: run.correlation });
    try {
      context.manager.appendCustomEntry(TURN_TYPE, { runId: run.id, text: params.text, context: params.context ?? [], correlation: run.correlation,
        ...(run.product ? { materials: run.product.draft.materials } : {}) });
      const snapshots = (params.context ?? []).map(item => `\n\n<context name=${JSON.stringify(item.name)}>\n${item.text}\n</context>`).join('');
      if (!run.cancelled && !this.disconnected) await context.runtime.session.prompt(params.text + snapshots);
      await context.runtime.session.waitForIdle();
    } catch (error) { failure = error; }
    const last = run.final;
    const completed = !failure && run.sdkSettled && !run.sdkAborted && last?.stopReason === 'stop';
    const status = run.product && completed ? 'completed' : run.cancelled || run.sdkAborted || last?.stopReason === 'aborted' ? 'interrupted'
      : completed ? 'completed' : 'failed';
    const text = last?.content.filter(item => item.type === 'text').map(item => item.text).join('') ?? '';
    const error = status === 'failed' ? { code: 'execution_failed', message: failure ? this.cleanMessage(failure) : this.cleanMessage(last?.errorMessage ?? `SDK outcome: ${last?.stopReason ?? 'missing settlement'}`) } : undefined;
    context.active = undefined;
    context.observationSeq++;
    if (run.product) run.product.observers.settled(status, text, error?.message);
    else this.event(context, run, 'run.settled', { status, text, ...(error ? { error } : {}), ...(last?.usage ? { usage: { ...last.usage, cost: undefined, scope: 'final_assistant', pricingKnown: false } } : {}) });
  }
  private async conversationPreview(sessionRef: string, contextId: string, botId: string) {
    const path = resolve(sessionRef);
    if (dirname(path) !== join(this.stateDir, 'sessions')) return {};
    const entries = await this.readConversationEntries(path);
    if (entries[0]?.type !== 'session') return {};
    const owner = entries.find(entry => entry?.type === 'custom' && entry.customType === BINDING_TYPE);
    if (owner?.type !== 'custom') return {};
    const binding = owner.data as Partial<Binding> | undefined;
    if (binding?.contextId !== contextId || binding.botId !== botId || binding.purpose !== 'conversation') return {};
    const turn = entries.find(entry => entry?.type === 'custom' && entry.customType === TURN_TYPE);
    if (turn?.type !== 'custom') return {};
    const text = (turn.data as { text?: unknown } | undefined)?.text;
    if (typeof text !== 'string') return {};
    let preview = '';
    let bytes = 0;
    let space = false;
    for (const char of text) {
      if (/\s/u.test(char)) {
        space = bytes > 0;
        continue;
      }
      bytes += Buffer.byteLength(char) + Number(space);
      if (bytes > 256) break;
      if (space) preview += ' ';
      preview += char;
      space = false;
    }
    if (!preview) return {};
    const time = typeof turn.timestamp === 'string' ? Date.parse(turn.timestamp) : NaN;
    const timestamp = Number.isFinite(time) ? new Date(time).toISOString() : undefined;
    return timestamp && /^\d{4}-/.test(timestamp) ? { preview, timestamp } : { preview };
  }
  private async readConversationEntries(path: string) {
    try {
      if (await realpath(path) !== path || !(await stat(path)).isFile()) return [];
      return parseSessionEntries(await readFile(path, 'utf8'));
    } catch {
      return [];
    }
  }
  private async openProductSession(params: OpenParams, tools: ToolDefinition[]): Promise<ProductSession> {
    const context = await this.open(params, tools);
    const sessionRef = context.manager.getSessionFile();
    if (!sessionRef) throw new RequestError('history_unavailable', 'Pi did not provide a session reference');
    return {
      sessionRef,
      history: () => context.manager.getEntries(),
      start: (executionId, draft, observers) => this.startProductRun(context, executionId, draft, observers),
      stop: executionId => {
        if (context.active?.id !== executionId) throw new RequestError('not_found', 'The execution has settled');
        context.active.cancelled = true;
        void context.runtime.session.abort().catch(() => this.disconnect(true));
      },
    };
  }

  private async refreshProductSession(params: OpenParams, tools: ToolDefinition[]): Promise<ProductSession> {
    const current = this.context(params.contextId);
    if (current.active) throw new RequestError('busy', 'Context is running');
    await current.runtime.dispose();
    this.contexts.delete(params.contextId);
    return this.openProductSession(params, tools);
  }

  private startProductRun(context: Context, executionId: string, draft: Draft, observers: ProductRunObservers): Run {
    if (context.active || this.closing) throw new RequestError('busy', 'The execution context is unavailable');
    const run: Run = { id: executionId, seq: 0, cancelled: false, sdkStarted: false, sdkSettled: false, sdkAborted: false,
      correlation: undefined, done: Promise.resolve(), pending: new Map(), inputs: new Map(), product: { draft, observers } };
    context.active = run;
    context.used = true;
    context.runIds.add(executionId);
    context.observationSeq++;
    run.done = this.execute(context, run, { contextId: context.binding.contextId, runId: executionId,
      text: draft.text, context: draft.materials.map(({ name, text }) => ({ name, text })) });
    return run;
  }

  private async testProductConnection(configuration: Connection): Promise<void> {
    try { await this.runProductConnectionTest(configuration); }
    catch (error) {
      const code = error instanceof RequestError ? error.code : 'connection_test_failed';
      throw new RequestError(code, this.cleanMessage(error, configuration.apiKey));
    }
  }

  private async runProductConnectionTest(configuration: Connection): Promise<void> {
    const id = randomUUID();
    const context = await this.open({ contextId: id, botId: id, purpose: 'conversation', selectionRevision: 1,
      cwd: configuration.workingDirectory, used: false, instructions: '', codingTools: [], domainTools: null,
      configuration: { ...configuration, credentialSource: id } }, [], true);
    const { promise, resolve, reject } = Promise.withResolvers<void>();
    const run = this.startProductRun(context, id, { revision: 0, text: 'Reply with OK.', materials: [] }, {
      started: () => {}, text: () => {}, file: () => {}, toolFinished: () => {},
      settled: (status, _text, error) => {
        if (status === 'completed') resolve();
        else reject(new RequestError('connection_test_failed', error ?? 'The test request did not complete'));
      },
    });
    const timeout = setTimeout(() => {
      run.cancelled = true;
      void context.runtime.session.abort().catch(() => this.disconnect(true));
      reject(new RequestError('connection_test_failed', 'The test request expired'));
    }, 15_000);
    try { await promise; }
    finally {
      clearTimeout(timeout);
      try { await context.runtime.dispose(); }
      finally { this.contexts.delete(id); }
    }
  }

  private async drainProduct(): Promise<void> {
    this.closing = true;
    for (const context of this.contexts.values()) {
      const run = context.active;
      if (run) {
        run.cancelled = true;
        await context.runtime.session.abort();
        await run.done;
      }
      await context.runtime.dispose();
    }
  }

  private async completeShutdown(): Promise<void> {
    await this.writer.flush();
    this.shutdownComplete = true;
    process.stdin.destroy();
  }

  private async handleFrame(data: Buffer): Promise<void> {
    let id: string | undefined;
    try {
      if (this.product) {
        const request = parseProductRequest(data);
        id = request.id;
        await this.product.dispatch(request.command, result => this.response(request.id, result));
        if (request.command.method === 'app.shutdown') await this.completeShutdown();
      } else {
        const request = parseRequest(data);
        id = request.id;
        await this.handle(id, request.command);
      }
    } catch (error) {
      id ??= error instanceof RequestError ? error.requestId : undefined;
      if (!id) throw error;
      await this.failure(id, error);
    }
  }
  private history(context: Context, cursor: string | undefined, limit: number): object {
    const entries = context.manager.getEntries();
    const offset = cursor ? entries.findIndex(entry => entry.id === cursor) + 1 : 0;
    if (cursor && !offset) throw new RequestError('invalid_cursor', 'History cursor is not in this session');
    const page = entries.slice(offset, offset + (limit || 50));
    const metadata = { leafId: context.manager.getLeafId(), sessionId: context.manager.getSessionId(), compactionAware: true };
    let result: { contextId: string; sessionRef: string | undefined; entries: typeof page; nextCursor?: string; projection: typeof metadata };
    do {
      const nextCursor = offset + page.length < entries.length ? page.at(-1)?.id : undefined;
      result = { contextId: context.binding.contextId, sessionRef: context.manager.getSessionFile(), entries: page, ...(nextCursor ? { nextCursor } : {}), projection: metadata };
      if (frameBytes({ v: 1, type: 'response', id: 'history', ok: true, result }) < MAX_FRAME - 1024) return result;
      page.pop();
    } while (page.length);
    throw new RequestError('entry_too_large', 'A history entry exceeds the frame limit');
  }
  private inspect(context: Context): object {
    const state = context.runtime.session.state;
    const result = {
      contextId: context.binding.contextId, sessionRef: context.manager.getSessionFile(),
      modelContext: { systemPrompt: context.runtime.session.systemPrompt, messages: context.manager.buildSessionContext().messages },
      finalizedMessages: context.manager.buildSessionContext().messages,
      ...(state.streamingMessage ? { partial: state.streamingMessage } : {}),
      ...(context.active ? { activeRun: { runId: context.active.id, cancelRequested: context.active.cancelled } } : {}),
      observationSeq: context.observationSeq, capturedAt: new Date().toISOString(), complete: true,
    };
    if (frameBytes(result) > MAX_FRAME - 1024) throw new RequestError('snapshot_too_large', 'Complete context exceeds frame limit; use paged history');
    return result;
  }
  private async handle(id: string, command: Command): Promise<void> {
    if (this.closing) throw new RequestError('closing', 'Worker is shutting down');
    switch (command.method) {
      case 'configure':
        this.configure(command.params);
        await this.response(id, { configured: true, workerEpoch: this.epoch, engineVersion: '1.5.2', pricingKnown: false, credentialSource: this.configured?.credentialSource });
        break;
      case 'context.open':
        await this.response(id, this.binding(await this.open(command.params)));
        break;
      case 'context.history':
        await this.response(id, this.history(this.context(command.params.contextId), command.params.cursor, command.params.limit));
        break;
      case 'context.inspect':
        await this.response(id, this.inspect(this.context(command.params.contextId)));
        break;
      case 'context.configuration':
        await this.response(id, this.configuration(this.context(command.params.contextId)));
        break;
      case 'context.configure': {
        const context = this.context(command.params.contextId);
        await this.configureContext(context, command.params.settings);
        await this.response(id, this.configuration(context));
        break;
      }
      case 'run.start': {
        const context = this.context(command.params.contextId);
        if (context.active) throw new RequestError('busy', 'Context is running');
        if (context.runIds.has(command.params.runId)) throw new RequestError('duplicate_run', 'Run ID already used');
        const run: Run = { id: command.params.runId, seq: 0, cancelled: false, sdkStarted: false, sdkSettled: false, sdkAborted: false, correlation: command.params.correlation, done: Promise.resolve(), pending: new Map(), inputs: new Map() };
        context.active = run; context.used = true; context.runIds.add(run.id); context.observationSeq++;
        await this.response(id, { contextId: context.binding.contextId, runId: run.id, admitted: true });
        run.done = this.execute(context, run, command.params);
        break;
      }
      case 'run.cancel': {
        const context = this.context(command.params.contextId);
        const run = context.active;
        if (!run || run.id !== command.params.runId) throw new RequestError('not_found', 'Active run not found');
        run.cancelled = true;
        await this.response(id, { contextId: context.binding.contextId, runId: run.id, cancelRequested: true });
        void context.runtime.session.abort().catch(() => this.disconnect(true));
        break;
      }
      case 'tool.result': {
        const context = this.context(command.params.contextId);
        const run = context.active;
        const finish = run?.id === command.params.runId && command.params.workerEpoch === this.epoch ? run.pending.get(command.params.callId) : undefined;
        if (!finish) throw new RequestError('not_found', 'Pending tool call not found');
        run?.pending.delete(command.params.callId);
        await this.response(id, { resolved: true });
        finish(command.params);
        break;
      }
      case 'context.close': {
        const context = this.context(command.params.contextId);
        if (context.active) throw new RequestError('busy', 'Context is running');
        await context.runtime.dispose();
        this.contexts.delete(context.binding.contextId);
        await this.response(id, { contextId: context.binding.contextId, closed: true });
        break;
      }
      case 'shutdown':
        if ([...this.contexts.values()].some(context => context.active)) throw new RequestError('busy', 'Drain or cancel admitted runs before shutdown');
        this.closing = true;
        for (const context of this.contexts.values()) await context.runtime.dispose();
        await this.response(id, { shuttingDown: true });
        await this.completeShutdown();
        break;
    }
  }
  private async disconnect(failed: boolean): Promise<void> {
    if (this.disconnected) return;
    this.disconnected = true; this.closing = true;
    if (failed) process.exitCode = 1;
    const outcomes = await Promise.allSettled([...this.contexts.values()].map(async context => {
      const run = context.active;
      if (run) run.cancelled = true;
      try {
        await context.runtime.session.abort();
        if (run) await run.done;
      } finally { await context.runtime.dispose(); }
    }));
    if (outcomes.some(outcome => outcome.status === 'rejected')) process.exitCode = 1;
    try { this.product?.close(); }
    catch { process.exitCode = 1; }
    await this.writer.flush().catch(() => {});
    process.stdin.destroy();
  }
  async read(): Promise<void> {
    let partial = Buffer.alloc(0);
    let commands: Promise<void> = Promise.resolve();
    try {
      for await (const chunk of process.stdin) {
        partial = Buffer.concat([partial, Buffer.from(chunk)]);
        let newline: number;
        while ((newline = partial.indexOf(10)) >= 0) {
          if (newline + 1 > MAX_FRAME) throw new Error('Input frame limit exceeded');
          const data = partial.subarray(0, newline);
          partial = partial.subarray(newline + 1);
          commands = commands.then(() => this.handleFrame(data));
          commands = commands.catch(() => this.disconnect(true));
        }
        if (partial.length >= MAX_FRAME) throw new Error('Input frame limit exceeded');
      }
      if (partial.length) throw new Error('Partial JSONL at EOF');
      await commands;
      if (!this.shutdownComplete) await this.disconnect(false);
    } catch { if (!this.shutdownComplete) await this.disconnect(true); }
  }
}

async function main(): Promise<void> {
  const args = process.argv.slice(2);
  const productPath = args[3];
  const credentialsPath = args[5];
  const fresh = args[6] === '--fresh-product-profile';
  if (args[0] !== '--state-dir' || !args[1] || !isAbsolute(args[1]) ||
      (args.length !== 2 && ((args.length !== 6 && !(args.length === 7 && fresh)) || args[2] !== '--product-profile' ||
        args[4] !== '--credentials' || !productPath || !credentialsPath || !isAbsolute(productPath) || !isAbsolute(credentialsPath) || resolve(productPath) === resolve(credentialsPath)))) {
    throw new Error('Invalid worker state or product profile arguments');
  }
  if (fresh) await mkdir(dirname(productPath), { recursive: true, mode: 0o700 });
  await mkdir(args[1], { recursive: true });
  const stateDir = await realpath(args[1]);
  process.env.PI_CODING_AGENT_DIR = join(stateDir, 'agent');
  await mkdir(join(stateDir, 'sessions'), { recursive: true });
  await mkdir(join(stateDir, 'agent'), { recursive: true });
  const worker = new Worker(stateDir, productPath ? { profilePath: productPath, credentialsPath, fresh } : undefined);
  await worker.writer.send({ v: 1, type: 'ready', workerEpoch: worker.epoch, engine: { name: 'pi', version: '1.5.2' }, runtime: { name: 'node', version: process.version }, builtinCatalog: worker.builtinCatalog });
  await worker.read();
}
main().catch(() => { process.stderr.write('Pi worker initialization failed\n'); process.exitCode = 1; });
