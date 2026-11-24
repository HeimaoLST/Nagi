import { randomUUID } from 'node:crypto';
import { closeSync, constants, fstatSync, mkdirSync, openSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { Type } from 'typebox';
import { Value } from 'typebox/value';
import { RequestError, type McpSecretInput, type McpServerInput } from './protocol.js';

const stageName = /^\.mcp-secrets-[a-f0-9-]{36}$/;
const maxVaultBytes = 40 * 1024 * 1024;
const valuesSchema = Type.Record(Type.String(), Type.String({ minLength: 1, maxLength: 8192 }), { maxProperties: 64 });
const vaultSchema = Type.Object({ version: Type.Literal(1), generation: Type.String({ pattern: stageName.source }),
  servers: Type.Record(Type.String({ pattern: '^[a-z0-9][a-z0-9_-]{0,62}$' }), valuesSchema, { maxProperties: 64 }),
}, { additionalProperties: false });
export type McpSecretValues = Record<string, string>;
export type McpVaultWrite = { serverId: string; values: McpSecretValues };

export function declaredMcpSecrets(server: McpServerInput): string[] {
  return server.transport === 'stdio' ? server.secretEnvKeys ?? [] : (server.secretHeaderKeys ?? []).map(key => key.toLowerCase());
}

export function mcpSecretsComplete(server: McpServerInput, values: McpSecretValues): boolean {
  return declaredMcpSecrets(server).every(key => Object.hasOwn(values, key));
}

export function mcpSecretKeys(values: McpSecretValues): string[] {
  return Object.entries(values).flatMap(([name, value]) => {
    const token = /^(?:authorization|proxy-authorization)$/i.test(name) ? /^(?:Bearer|Basic)\s+(.+)$/i.exec(value)?.[1] : undefined;
    return token ? [value, token] : [value];
  });
}

export function mergeMcpSecrets(server: McpServerInput, saved: McpSecretValues, input?: McpSecretInput): McpSecretValues {
  const env = server.transport === 'stdio';
  const declared = declaredMcpSecrets(server);
  const opposite = env ? input?.headers : input?.env;
  if (opposite !== undefined) throw new RequestError('invalid_params', 'Secret fields must match the MCP transport');
  const updates = env ? input?.env : input?.headers;
  const result: McpSecretValues = Object.fromEntries(input === null || updates === null ? [] : Object.entries(saved).filter(([key]) => declared.includes(key)));
  const seen = new Set<string>();
  for (const [key, value] of Object.entries(updates ?? {})) {
    const field = env ? key : key.toLowerCase();
    if (!declared.includes(field) || seen.has(field) || value && (Buffer.byteLength(value) > 8192 || /[\0\r\n]/.test(value))) {
      throw new RequestError('invalid_params', 'Enter valid values for declared MCP secret fields');
    }
    seen.add(field);
    if (value) Object.defineProperty(result, field, { value, enumerable: true, writable: true, configurable: true });
    else delete result[field];
  }
  return result;
}

export class McpVault {
  private readonly path: string;
  private servers: Record<string, McpSecretValues> = {};
  private readonly inventory = new Set<string>();

  constructor(credentialsPath: string) {
    this.path = join(dirname(credentialsPath), 'mcp-secrets.json');
  }

  private read(path: string) {
    let fd: number;
    try { fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return;
      throw new RequestError('profile_unavailable', 'The private MCP credential vault cannot be opened');
    }
    try {
      const info = fstatSync(fd);
      if (!info.isFile() || (info.mode & 0o777) !== 0o600 || info.size > maxVaultBytes) throw new Error('Invalid vault file');
      const data: unknown = JSON.parse(readFileSync(fd, 'utf8'));
      if (!Value.Check(vaultSchema, data)) throw new Error('Invalid vault data');
      return data;
    } catch {
      throw new RequestError('profile_unavailable', 'The private MCP credential vault must be a valid 0600 file');
    } finally { closeSync(fd); }
  }

  load(): void {
    this.servers = this.read(this.path)?.servers ?? {};
    for (const values of Object.values(this.servers)) for (const value of mcpSecretKeys(values)) this.inventory.add(value);
  }

  get(serverId: string): McpSecretValues {
    return Object.hasOwn(this.servers, serverId) ? this.servers[serverId] : {};
  }

  keys(): string[] {
    return [...this.inventory];
  }

  stage({ serverId, values }: McpVaultWrite): string {
    const generation = `.mcp-secrets-${randomUUID()}`;
    const directory = dirname(this.path);
    mkdirSync(directory, { recursive: true, mode: 0o700 });
    const stage = join(directory, generation);
    const servers = { ...this.servers, [serverId]: values };
    if (!Object.keys(values).length) delete servers[serverId];
    const data = JSON.stringify({ version: 1, generation, servers }) + '\n';
    if (Buffer.byteLength(data) > maxVaultBytes) throw new RequestError('invalid_params', 'The MCP credential vault exceeds its size limit');
    try { writeFileSync(stage, data, { flag: 'wx', mode: 0o600, flush: true }); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'EEXIST') rmSync(stage, { force: true });
      throw new RequestError('profile_unavailable', 'The private MCP credential vault could not be staged');
    }
    return generation;
  }

  publish(generation: string): void {
    if (!stageName.test(generation)) throw new RequestError('profile_unavailable', 'The staged MCP credential vault reference is invalid');
    const path = join(dirname(this.path), generation);
    if (this.read(path)?.generation === generation) renameSync(path, this.path);
    else if (this.read(this.path)?.generation !== generation) throw new RequestError('profile_unavailable', 'The staged MCP credential vault is missing');
    this.load();
  }

  discard(generation: string): void {
    rmSync(join(dirname(this.path), generation), { force: true });
  }
}
