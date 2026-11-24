import { realpath } from 'node:fs/promises';
import { isAbsolute, resolve } from 'node:path';
import { Type, type TSchema } from 'typebox';
import type { ToolDefinition } from '@earendil-works/pi-coding-agent';
import { McpClient, StdioTransport, StreamableHttpTransport, createMcpToolName, limitMcpContent, mcpNamespace,
  normalizeMcpInputSchema, toLlmContent, validateMcpServerConfig, type Tool } from '@earendil-works/pi-mcp';
import { RequestError, canonicalJSON, message, type McpBinding, type McpPoolView, type McpServerInput, type McpServerRecord,
  type McpTestResult, type McpTool } from './protocol.js';
import { declaredMcpSecrets, mcpSecretsComplete, type McpSecretValues } from './mcp-vault.js';
import { clipUTF8 } from './product-redactor.js';

export const maxMcpServers = 64;
const connectMs = 6000;
const callMs = 15000;
const maxCatalogBytes = 48 * 1024;
const configuredIdle = Number(process.env.NAGI_MCP_IDLE_TTL_MS);
const idleMs = Number.isSafeInteger(configuredIdle) && configuredIdle > 0 && configuredIdle <= 2147483647 ? configuredIdle : 5 * 60 * 1000;

export function validMcpServer(server: McpServerInput): boolean {
  if (validateMcpServerConfig(server.name, server).kind !== server.transport || Buffer.byteLength(JSON.stringify(server)) > 16 * 1024) return false;
  const stdio = server.transport === 'stdio';
  const plain = stdio ? server.env : server.headers;
  const normalize = (key: string) => stdio ? key : key.toLowerCase();
  const validKey = stdio ? /^[A-Za-z_][A-Za-z0-9_]*$/ : /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
  const secret = declaredMcpSecrets(server);
  const plainKeys = Object.keys(plain ?? {}).map(normalize);
  const named = (stdio ? server.envKeys : server.headerKeys)?.map(normalize) ?? [];
  if ([...secret, ...plainKeys, ...named].some(key => !validKey.test(key)) || new Set(secret).size !== secret.length ||
      new Set(plainKeys).size !== plainKeys.length || new Set(named).size !== named.length || plainKeys.some(key => secret.includes(key)) ||
      Object.values(plain ?? {}).some(value => /[\0\r\n]/.test(value))) return false;
  if (stdio) return isAbsolute(server.command) && ![server.command, server.cwd, ...server.args ?? []].some(value => value?.includes('\0'));
  const url = new URL(server.url);
  return !url.username && !url.password;
}

export function validMcpBindings(bindings: readonly McpBinding[]): boolean {
  return new Set(bindings.map(binding => binding.serverId)).size === bindings.length &&
    bindings.every(binding => binding.policy === 'allowlist' || !binding.allowedTools?.length);
}

export function uniqueMcpRegistry(servers: readonly McpServerRecord[]): boolean {
  return new Set(servers.map(server => server.serverId)).size === servers.length &&
    new Set(servers.map(server => mcpNamespace(server.name))).size === servers.length;
}

export function containsMcpSecret(value: unknown, redact: (text: string) => string): boolean {
  if (typeof value === 'string') return redact(value) !== value;
  if (Array.isArray(value)) return value.some(item => containsMcpSecret(item, redact));
  return value !== null && typeof value === 'object' && Object.entries(value).some(([key, item]) => redact(key) !== key || containsMcpSecret(item, redact));
}

type Entry = {
  serverId: string; scopeKey: string; isolation: McpServerRecord['isolation']; revision: number;
  transport: OwnedStdio | OwnedHttp; client: McpClient; tools: Promise<Tool[]>;
  refCount: number; state: McpPoolView['state']; timer?: ReturnType<typeof setTimeout>; closed?: Promise<void>;
};
type Actor = { botId: string; cwd: string };
type Mounted = { server: McpServerRecord; binding: McpBinding; secrets: McpSecretValues };

class OwnedStdio extends StdioTransport {
  private shutdown?: Promise<void>;
  override close(): Promise<void> { return this.shutdown ??= super.close(); }
}

class OwnedHttp extends StreamableHttpTransport {
  private shutdown?: Promise<void>;
  override close(): Promise<void> { return this.shutdown ??= super.close(); }
}

function permitted(binding: McpBinding, tool: Tool): boolean {
  if (binding.policy === 'readonly') return tool.annotations?.readOnlyHint === true && tool.annotations?.destructiveHint !== true;
  return binding.policy === 'all' || binding.allowedTools?.includes(tool.name) === true;
}

function publicTool(tool: Tool, redact: (text: string) => string): McpTool | undefined {
  if (Buffer.byteLength(tool.name) > 512 || redact(tool.name) !== tool.name || containsMcpSecret(tool.inputSchema, redact) ||
      Buffer.byteLength(JSON.stringify(tool.inputSchema ?? {})) > 16 * 1024) return;
  return { name: tool.name, description: clipUTF8(redact(tool.description ?? ''), 512), inputSchema: tool.inputSchema,
    readOnlyHint: tool.annotations?.readOnlyHint, destructiveHint: tool.annotations?.destructiveHint };
}

function publicCatalog(tools: Tool[], redact: (text: string) => string): McpTool[] {
  const rows: McpTool[] = [];
  let bytes = 0;
  for (const tool of tools) {
    const row = publicTool(tool, redact);
    if (!row) continue;
    bytes += Buffer.byteLength(JSON.stringify(row));
    if (bytes > maxCatalogBytes || rows.length === 128) break;
    rows.push(row);
  }
  return rows;
}

function toolFailure(error: unknown, redact: (text: string) => string) {
  const code = error instanceof RequestError ? error.code : 'mcp_unavailable';
  return { content: [{ type: 'text' as const, text: JSON.stringify({ code, message: clipUTF8(redact(message(error)), 512) }) }],
    details: {}, isError: true };
}

export class McpConnectionManager {
  private readonly pool = new Map<string, Entry>();
  private readonly transports = new Set<Entry>();
  private closing = false;

  status(): McpPoolView[] {
    return [...this.pool.values()].map(({ serverId, scopeKey, isolation, state, refCount, transport }) => ({ serverId, scopeKey,
      isolation, state, refCount, ...(transport instanceof OwnedStdio && transport.pid !== undefined ? { pid: transport.pid } : {}) }));
  }

  private start(server: McpServerRecord, secrets: McpSecretValues, cwd: string, scopeKey: string): Entry {
    if (this.closing) throw new RequestError('closing', 'The MCP broker is shutting down');
    if (!mcpSecretsComplete(server, secrets)) throw new RequestError('credential_missing', 'The MCP server requires credentials');
    const credentials = Object.fromEntries(declaredMcpSecrets(server).map(key => [key, secrets[key]]));
    const transport = server.transport === 'stdio'
      ? new OwnedStdio({ command: server.command, args: server.args, cwd: resolve(cwd, server.cwd ?? '.'),
        env: { ...server.env, ...credentials }, inheritEnv: false, stderr: 'pipe', closeTimeoutMs: 250 })
      : new OwnedHttp({ url: server.url, headers: { ...server.headers, ...credentials },
        fetch: (url, init) => fetch(url, { ...init, redirect: 'error', signal: init?.signal ?
          AbortSignal.any([init.signal, AbortSignal.timeout(connectMs)]) : AbortSignal.timeout(connectMs) }) });
    const client = new McpClient({ name: 'Nagi', version: '1', requestTimeoutMs: connectMs });
    const entry: Entry = { serverId: server.serverId, scopeKey, isolation: server.isolation, revision: server.revision, transport, client,
      refCount: 0, state: 'connecting', tools: this.connect(client, transport) };
    this.transports.add(entry);
    client.onClose(() => { void this.retire(entry).catch(() => {}); });
    client.onNotification('notifications/tools/list_changed', () => {
      if (entry.state !== 'connected') return;
      entry.tools = this.listTools(client, AbortSignal.timeout(connectMs));
      void entry.tools.catch(() => { void this.retire(entry).catch(() => {}); });
    });
    void entry.tools.then(() => { if (entry.state === 'connecting') entry.state = 'connected'; }, () => { void this.retire(entry).catch(() => {}); });
    return entry;
  }

  private async connect(client: McpClient, transport: OwnedStdio | OwnedHttp): Promise<Tool[]> {
    const signal = AbortSignal.timeout(connectMs);
    const timeout = () => { void transport.close().catch(() => {}); };
    signal.addEventListener('abort', timeout, { once: true });
    try {
      await client.connect(transport);
      return await this.listTools(client, signal);
    } finally { signal.removeEventListener('abort', timeout); }
  }

  private async listTools(client: McpClient, signal: AbortSignal): Promise<Tool[]> {
    const tools = await client.listTools({ signal });
    if (new Set(tools.map(tool => tool.name)).size !== tools.length) throw new RequestError('mcp_unavailable', 'The MCP server returned duplicate tools');
    return tools.sort((a, b) => a.name.localeCompare(b.name));
  }

  private retire(entry: Entry): Promise<void> {
    if (entry.closed) return entry.closed;
    clearTimeout(entry.timer);
    entry.state = 'closing';
    if (this.pool.get(entry.scopeKey) === entry) this.pool.delete(entry.scopeKey);
    entry.closed = Promise.resolve().then(async () => {
      try { await entry.client.close(); }
      finally { await entry.transport.close(); this.transports.delete(entry); }
    });
    return entry.closed;
  }

  async invalidate(serverId: string): Promise<void> {
    await Promise.all([...this.transports].filter(entry => entry.serverId === serverId).map(entry => this.retire(entry)));
  }

  async shutdown(): Promise<void> {
    this.closing = true;
    await Promise.all([...this.transports].map(entry => this.retire(entry)));
  }

  killOnExit(): void {
    for (const { transport } of this.transports) {
      if (!(transport instanceof OwnedStdio) || transport.pid === undefined) continue;
      try { process.kill(process.platform === 'win32' ? transport.pid : -transport.pid, 'SIGKILL'); } catch {}
    }
  }

  private acquire(server: McpServerRecord, secrets: McpSecretValues, { cwd, botId }: Actor): Entry {
    if (!server.enabled) throw new RequestError('mcp_unavailable', 'The MCP server is disabled');
    if (!mcpSecretsComplete(server, secrets)) throw new RequestError('credential_missing', 'The MCP server requires credentials');
    const scopeKey = server.isolation === 'shared' ? server.serverId : `${server.serverId}:${server.isolation === 'per_workspace' ? cwd : botId}`;
    let entry = this.pool.get(scopeKey);
    if (!entry || entry.revision !== server.revision) {
      if (entry) void this.retire(entry).catch(() => {});
      entry = this.start(server, secrets, cwd, scopeKey);
      this.pool.set(scopeKey, entry);
    }
    clearTimeout(entry.timer);
    entry.refCount++;
    return entry;
  }

  private release(entry: Entry): void {
    if (--entry.refCount !== 0 || entry.state === 'closing') return;
    entry.timer = setTimeout(() => { void this.retire(entry).catch(() => {}); }, idleMs).unref();
  }

  async test(server: McpServerRecord, secrets: McpSecretValues, cwd: string, redact: (text: string) => string): Promise<McpTestResult> {
    const started = performance.now();
    let entry: Entry | undefined;
    try {
      entry = this.start(server, secrets, cwd, server.serverId);
      const tools = publicCatalog(await entry.tools, redact);
      return { ok: true, tools, latencyMs: Math.round(performance.now() - started) };
    } catch (error) {
      const reason = error instanceof RequestError && error.code === 'credential_missing' ? 'credential_missing' : clipUTF8(redact(message(error)), 512);
      return { ok: false, tools: [], error: reason || 'MCP probe failed', latencyMs: Math.round(performance.now() - started) };
    } finally { if (entry) await this.retire(entry); }
  }

  async tools(actor: Actor, mounts: Mounted[], current: (serverId: string) => Mounted | undefined, redactor: () => (text: string) => string) {
    if (!mounts.length) return { tools: [], signature: '' };
    actor = { ...actor, cwd: await realpath(actor.cwd) };
    const unavailable: string[] = [];
    const catalog = await Promise.all(mounts.map(async mount => {
      const { server, secrets, binding } = mount;
      let entry: Entry | undefined;
      try {
        entry = this.acquire(server, secrets, actor);
        return (await entry.tools).filter(tool => permitted(binding, tool)).map(tool => ({ tool, server }));
      } catch (error) {
        unavailable.push(`${server.serverId}:${error instanceof RequestError && error.code === 'credential_missing' ? 'credential_missing' : 'mcp_unavailable'}`);
        return [];
      } finally { if (entry) this.release(entry); }
    }));
    const redact = redactor();
    const names = new Set<string>();
    const definitions: ToolDefinition[] = [];
    const signature = [];
    for (const { tool, server } of catalog.flat()) {
      const row = publicTool(tool, redact);
      if (!row) continue;
      const name = createMcpToolName(server.name, tool.name, candidate => names.has(candidate));
      if (names.has(name)) continue;
      names.add(name);
      const description = row.description || name;
      const parameters = Type.Unsafe<Record<string, unknown>>(normalizeMcpInputSchema(tool.inputSchema ?? {}) as TSchema);
      signature.push({ name, revision: server.revision, description, parameters });
      definitions.push({ name, label: name, description, parameters,
        execute: async (_callId, input, signal) => {
          let entry: Entry | undefined;
          try {
            const mount = current(server.serverId);
            if (!mount || !mount.server.enabled || mount.server.revision !== server.revision || !permitted(mount.binding, tool) || signal?.aborted) {
              throw new RequestError('scope_denied', 'This execution no longer has access to the MCP tool');
            }
            entry = this.acquire(mount.server, mount.secrets, actor);
            const live = (await entry.tools).find(item => item.name === tool.name);
            const latest = current(server.serverId);
            if (!live || !latest || latest.server.revision !== server.revision || !permitted(latest.binding, live) || signal?.aborted) {
              throw new RequestError('scope_denied', 'This execution no longer has access to the MCP tool');
            }
            if (!input || typeof input !== 'object' || Array.isArray(input)) throw new RequestError('invalid_params', 'MCP tool arguments must be an object');
            const result = await entry.client.callTool(live.name, input as Record<string, unknown>, { signal, timeoutMs: callMs });
            const clean = redactor();
            const content = toLlmContent(result).map(item => item.type === 'text' ? { ...item, text: clean(item.text) } :
              clean(item.data) === item.data && clean(item.mimeType) === item.mimeType ? item : { type: 'text' as const, text: '[redacted MCP image]' });
            return { content: (await limitMcpContent(content)).content, details: {}, isError: result.isError === true };
          } catch (error) { return toolFailure(error, redactor()); }
          finally { if (entry) this.release(entry); }
        },
      });
    }
    return { tools: definitions, signature: canonicalJSON([signature, unavailable.sort()]) };
  }
}
