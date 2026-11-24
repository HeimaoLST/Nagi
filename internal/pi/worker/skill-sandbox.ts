/// <reference lib="es2024.promise" />

import { spawn, type ChildProcess } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { chmodSync, closeSync, constants, fstatSync, lstatSync, mkdirSync, mkdtempSync, openSync, readFileSync, realpathSync,
  rmSync, writeFileSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import type { Readable, Writable } from 'node:stream';
import { bundleDigest, canonicalSkillOrigin, type SkillBundle } from './bot-skills.js';
import { clipUTF8 } from './product-redactor.js';
import { RequestError, type SkillPermissions, type SkillSource } from './protocol.js';

const sandbox = '/usr/bin/sandbox-exec';
const outputLimit = 64 * 1024;
const requestLimit = 256 * 1024;
const responseLimit = 256 * 1024;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const nodeArgs = ['--permission', '--allow-fs-read=*', '--allow-fs-write=*', '--input-type=module', '--eval'];
const probeProgram = String.raw`
import { createReadStream, readFileSync, writeFileSync } from 'node:fs';
import { createConnection } from 'node:net';
const stream = createReadStream('/dev/null', { fd: 5, encoding: 'utf8' });
const p = JSON.parse((await Array.fromAsync(stream)).join(''));
let file = false;
try { readFileSync(p.denied); } catch (e) { file = e.code === 'EPERM' || e.code === 'EACCES'; }
const socketResult = Promise.withResolvers();
const connection = createConnection({ host: '127.0.0.1', port: 1 });
connection.once('error', error => socketResult.resolve(error.code === 'EPERM' || error.code === 'EACCES'));
connection.once('connect', () => { connection.destroy(); socketResult.resolve(false); });
const socket = await socketResult.promise;
if (file && socket) writeFileSync(3, 'ready\n');`;
const runProgram = String.raw`
import { writeSync, createReadStream } from 'node:fs';
import { registerHooks, isBuiltin } from 'node:module';
import { createInterface } from 'node:readline';
import { Socket } from 'node:net';
import { pathToFileURL } from 'node:url';
const inputStream = createReadStream('/dev/null', { fd: 5, encoding: 'utf8' });
const { files, entry, input, tree, workspace } = JSON.parse((await Array.fromAsync(inputStream)).join(''));
const base = pathToFileURL(tree + '/').href;
const sources = new Map(Object.entries(files).filter(([path]) => path.endsWith('.mjs')).map(([path, source]) => [new URL(path, base).href, source]));
registerHooks({
  resolve(specifier, context, next) {
    if (isBuiltin(specifier)) return next(specifier, context);
    const url = new URL(specifier, context.parentURL || base).href;
    if (!sources.has(url)) throw new Error('Only bundled script modules can be imported');
    return { url, shortCircuit: true };
  },
  load(url, context, next) {
    return sources.has(url) ? { format: 'module', source: sources.get(url), shortCircuit: true } : next(url, context);
  },
});
const send = (fd, data) => {
  const bytes = Buffer.from(data);
  for (let offset = 0; offset < bytes.length;) offset += writeSync(fd, bytes, offset, bytes.length - offset);
};
const replyStream = new Socket({ fd: 4, readable: true, writable: false });
const replies = createInterface({ input: replyStream });
const pending = new Map();
let nextId = 0;
replies.on('line', line => {
  const reply = JSON.parse(line);
  const task = pending.get(reply.id);
  if (!task) return;
  pending.delete(reply.id);
  task(reply);
});
const ctx = Object.freeze({ workspace, fetch: async (url, options = {}) => {
  const id = ++nextId;
  const { promise, resolve } = Promise.withResolvers();
  const wire = JSON.stringify({ id, url: String(url), options });
  if (Buffer.byteLength(wire) > 256 * 1024) throw new Error('Mediated request exceeds the size limit');
  pending.set(id, resolve);
  send(3, wire + '\n');
  const reply = await promise;
  if (reply.error) throw Object.assign(new Error(reply.error), { code: reply.code });
  return { status: reply.status, ok: reply.status >= 200 && reply.status < 300,
    text: async () => reply.body, json: async () => JSON.parse(reply.body) };
} });
send(3, 'ready\n');
try {
  const script = await import(new URL(entry, base).href);
  if (typeof script.default !== 'function') throw new Error('The skill script must export a default function');
  const result = await script.default(input, ctx);
  if (result !== undefined) console.log(JSON.stringify(result));
} finally { replies.close(); replyStream.destroy(); }`;

type Scope = { botId: string; source: SkillSource; skillId: string };
export type SkillCredentials = (origin: string, refs: string[]) => Record<string, string>;
type SandboxResult = { ok: boolean; stdout: string; stderr: string };
type Stage = { root: string; tree: string; scratch: string; files: Record<string, string>; approvalDigest: string };

export function loadSkillInstallation(profileDir: string): string {
  const path = join(profileDir, '.skill-installation');
  try { writeFileSync(path, randomUUID(), { mode: 0o600, flag: 'wx', flush: true }); }
  catch (error) { if ((error as NodeJS.ErrnoException).code !== 'EEXIST') throw error; }
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const stat = fstatSync(fd);
    if (!stat.isFile() || stat.size !== 36 || (stat.mode & 0o777) !== 0o600) throw new Error('Invalid installation record');
    const value = readFileSync(fd, 'utf8');
    if (uuid.test(value)) return value;
    throw new Error('Invalid installation identity');
  } finally { closeSync(fd); }
}

function stageBundle(bundle: SkillBundle, scope: Scope): Stage {
  const root = mkdtempSync(join(realpathSync(tmpdir()), 'nagi-skill-'));
  const tree = join(root, 'tree');
  const scratch = join(root, 'scratch');
  try {
    mkdirSync(join(tree, 'scripts'), { recursive: true, mode: 0o700 });
    mkdirSync(scratch, { mode: 0o700 });
    for (const [path, data] of Object.entries(bundle.files)) writeFileSync(join(tree, path), data, { flag: 'wx', mode: 0o400 });
    chmodSync(join(tree, 'scripts'), 0o500);
    chmodSync(tree, 0o500);
    const files = Object.fromEntries(Object.entries(bundle.files).map(([path, expected]) => {
      const fd = openSync(join(tree, path), constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
      try {
        const stat = fstatSync(fd);
        if (!stat.isFile() || stat.size !== Buffer.byteLength(expected) || stat.nlink !== 1) throw new Error('Invalid staged skill file');
        const bytes = readFileSync(fd);
        if (!bytes.equals(Buffer.from(expected))) throw new Error('The staged skill bytes changed');
        return [path, expected];
      } finally { closeSync(fd); }
    }));
    return { root, tree, scratch, files, approvalDigest: bundleDigest(scope.botId, scope.source, scope.skillId, files) };
  } catch (error) { removeStage(root); throw error; }
}

function removeStage(root: string): void {
  for (const path of [join(root, 'tree'), join(root, 'tree', 'scripts')]) {
    if (lstatSync(path, { throwIfNoEntry: false })?.isDirectory()) chmodSync(path, 0o700);
  }
  rmSync(root, { recursive: true, force: true });
}

function denied(message: string): never { throw new RequestError('scope_denied', message); }

export class SkillSandbox {
  private readonly runs = new Map<ChildProcess, { stop: () => void; done: Promise<void> }>();
  private available?: Promise<boolean>;
  private closing = false;

  constructor(private readonly privatePaths: string[]) {}

  private profile(node: string, stage: Pick<Stage, 'root' | 'tree' | 'scratch'>, workspace: string, permissions: SkillPermissions): string {
    const home = realpathSync(homedir());
    const protectedPaths = new Set([...this.privatePaths, join(home, '.ssh'), join(home, '.aws'), join(home, 'Library', 'Keychains')]);
    for (const path of [...protectedPaths]) {
      try { protectedPaths.add(realpathSync(path)); } catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error; }
    }
    const paths = (values: Iterable<string>) => [...values].map(path => `(subpath ${JSON.stringify(path)})`).join(' ');
    const workspaceRead = workspace && permissions.workspaceRead ? [workspace] : [];
    const workspaceWrite = workspace && permissions.workspaceWrite ? [workspace] : [];
    return `(version 1)
(deny default)
(deny network*)
(allow process-exec (literal ${JSON.stringify(node)}))
(allow sysctl-read)
(allow file-read* (literal "/") (literal "/dev/null") ${paths(['/usr/lib', '/System/Library', dirname(node), stage.tree, stage.scratch, ...workspaceRead])})
(allow file-write* ${paths([stage.scratch, ...workspaceWrite])})
(deny file-write* ${paths([stage.tree, dirname(node), '/usr/lib', '/System/Library'])})
(deny file-write* (require-all (subpath ${JSON.stringify(stage.root)}) (require-not (subpath ${JSON.stringify(stage.scratch)}))))
(deny file-read* file-write* ${paths(protectedPaths)})`;
  }

  private async enforce(node: string): Promise<boolean> {
    const root = mkdtempSync(join(realpathSync(tmpdir()), 'nagi-seatbelt-'));
    try {
      const tree = join(root, 'tree');
      const scratch = join(root, 'scratch');
      mkdirSync(tree); mkdirSync(scratch);
      const marker = join(root, 'protected');
      writeFileSync(marker, 'probe', { mode: 0o600 });
      const policy = this.profile(node, { root, tree, scratch }, '', {});
      const result = await this.launch(node, policy, probeProgram, { denied: marker }, { root, scratch }, 3000, () => '', undefined);
      return result.ok;
    } catch { return false; }
    finally { rmSync(root, { recursive: true, force: true }); }
  }

  async prepare(): Promise<string> {
    if (process.platform !== 'darwin' || process.env.NAGI_FORCE_SANDBOX_UNAVAILABLE === '1' || this.closing) {
      throw new RequestError('sandbox_unavailable', 'OS skill sandbox is unavailable');
    }
    const node = realpathSync(process.execPath);
    if (!await (this.available ??= this.enforce(node))) throw new RequestError('sandbox_unavailable', 'OS skill sandbox is unavailable');
    return node;
  }

  async run(node: string, scope: Scope, bundle: SkillBundle, scriptName: string, input: unknown, workspace: string, permissions: SkillPermissions,
      authorize: (approvalDigest: string) => void, credentials: SkillCredentials, redact: (text: string) => string, signal?: AbortSignal): Promise<SandboxResult> {
    const stage = stageBundle(bundle, scope);
    try {
      if (signal?.aborted || this.closing) throw new RequestError('cancelled', 'The skill run was cancelled');
      authorize(stage.approvalDigest);
      const script = bundle.manifest.scripts[scriptName];
      const profile = this.profile(node, stage, workspace, permissions);
      return await this.launch(node, profile, runProgram, { files: stage.files, entry: script.entry, input: input ?? null, tree: stage.tree,
        workspace: permissions.workspaceRead || permissions.workspaceWrite ? workspace : undefined }, stage, script.timeoutMs ?? 10000, redact, signal,
      (request, stopped) => this.request(request, permissions, credentials, redact, stopped));
    } finally { removeStage(stage.root); }
  }

  private async request(value: unknown, permissions: SkillPermissions, credentials: SkillCredentials, redact: (text: string) => string, signal: AbortSignal) {
    const request = value as { url?: unknown; options?: { method?: unknown; headers?: unknown; body?: unknown } };
    const url = typeof request?.url === 'string' ? URL.parse(request.url) : null;
    if (!url || !['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.hash || redact(url.href) !== url.href) denied('The HTTP request URL is not authorized');
    const origin = canonicalSkillOrigin(url.origin);
    if (!(permissions.networkOrigins ?? []).some(value => canonicalSkillOrigin(value) === origin)) denied('The HTTP origin is not approved');
    const options = request.options ?? {};
    const method = typeof options.method === 'string' ? options.method.toUpperCase() : 'GET';
    if (!['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE'].includes(method)) denied('The HTTP request method is not supported');
    if (options.headers !== undefined && (!options.headers || typeof options.headers !== 'object' || Array.isArray(options.headers))) denied('Invalid HTTP headers');
    const headers = new Headers();
    for (const [name, value] of Object.entries(options.headers ?? {})) {
      if (/^(?:authorization|proxy-authorization|cookie|host|connection|content-length|transfer-encoding|upgrade|x-api-key)$/i.test(name) ||
          typeof value !== 'string' || redact(value) !== value) denied('Scripts cannot supply credential or transport headers');
      headers.set(name, value);
    }
    for (const [name, value] of Object.entries(credentials(origin, permissions.credentialRefs ?? []))) headers.set(name, value);
    const body = options.body;
    if (body !== undefined && (typeof body !== 'string' || Buffer.byteLength(body) > requestLimit || redact(body) !== body)) denied('Invalid HTTP request body');
    const response = await fetch(url, { method, headers, body, redirect: 'manual', signal });
    if (response.status >= 300 && response.status < 400) {
      await response.body?.cancel();
      throw new RequestError('redirect_denied', 'Mediated HTTP redirects are disabled');
    }
    const chunks: Uint8Array[] = [];
    let size = 0;
    const reader = response.body?.getReader();
    if (reader) {
      try {
        for (;;) {
          const next = await reader.read();
          if (next.done) break;
          size += next.value.length;
          if (size > responseLimit) throw new RequestError('response_too_large', 'The mediated response exceeds the size limit');
          chunks.push(next.value);
        }
      } finally { await reader.cancel().catch(() => {}); }
    }
    let text = Buffer.concat(chunks, size).toString('utf8');
    if (response.headers.get('content-type')?.includes('json')) {
      try {
        const clean = (value: unknown): unknown => typeof value === 'string' ? redact(value) : Array.isArray(value) ? value.map(clean) :
          value !== null && typeof value === 'object' ? Object.fromEntries(Object.entries(value).map(([k, v]) => [redact(k), clean(v)])) : value;
        text = JSON.stringify(clean(JSON.parse(text)));
      } catch { throw new RequestError('invalid_response', 'The mediated JSON response could not be safely decoded'); }
    }
    return { status: response.status, body: clipUTF8(redact(text), responseLimit) };
  }

  private launch(node: string, policy: string, program: string, input: object, stage: { root: string; scratch: string }, timeoutMs: number,
      redact: (text: string) => string, signal?: AbortSignal, broker?: (request: unknown, signal: AbortSignal) => Promise<object>): Promise<SandboxResult> {
    if (this.closing || signal?.aborted) return Promise.reject(new RequestError('cancelled', 'The skill run was cancelled'));
    const child = spawn(sandbox, ['-f', '/dev/fd/6', node, ...nodeArgs, program], { cwd: stage.scratch, env: {}, detached: true,
      stdio: ['ignore', 'pipe', 'pipe', 'pipe', 'pipe', 'pipe', 'pipe'] });
    const pipe = (fd: 5 | 6, data: string) => {
      const stream = (child.stdio as ReadonlyArray<Readable | Writable | null | undefined>)[fd] as Writable;
      stream.on('error', () => {});
      stream.end(data);
    };
    pipe(5, JSON.stringify(input));
    pipe(6, policy);
    const stopped = new AbortController();
    let failure: RequestError | undefined;
    let ready = false;
    const stop = (code = 'cancelled', reason = 'The skill run was cancelled') => {
      if (failure) return;
      failure = new RequestError(code, reason);
      stopped.abort();
      if (child.pid !== undefined && child.exitCode === null && child.signalCode === null) {
        try { process.kill(-child.pid, 'SIGKILL'); }
        catch (error) {
          if (!['ESRCH', 'EPERM'].includes((error as NodeJS.ErrnoException).code ?? '')) throw error;
          child.kill('SIGKILL');
        }
      }
    };
    const done = Promise.withResolvers<void>();
    this.runs.set(child, { stop, done: done.promise });
    const timeout = setTimeout(() => stop('skill_timeout', 'The skill script timed out'), timeoutMs);
    const onAbort = () => stop();
    signal?.addEventListener('abort', onAbort, { once: true });
    if (signal?.aborted) onAbort();
    const stdout: Buffer[] = [], stderr: Buffer[] = [];
    let size = 0;
    for (const [stream, output] of [[child.stdout!, stdout], [child.stderr!, stderr]] as const) stream.on('data', (chunk: Buffer) => {
      size += chunk.length;
      if (size > outputLimit) stop('output_limit', 'The skill output exceeds the size limit');
      else output.push(chunk);
    });
    const requests = child.stdio[3] as Readable;
    const responses = child.stdio[4] as Writable;
    responses.on('error', () => {});
    requests.on('error', () => {});
    let buffer = '', inFlight = false, count = 0;
    requests.setEncoding('utf8').on('data', (chunk: string) => {
      buffer += chunk;
      if (Buffer.byteLength(buffer) > requestLimit + 64) { stop('invalid_request', 'The mediated request exceeds the size limit'); return; }
      for (let end = buffer.indexOf('\n'); end >= 0; end = buffer.indexOf('\n')) {
        const line = buffer.slice(0, end);
        buffer = buffer.slice(end + 1);
        if (!ready && line === 'ready') { ready = true; continue; }
        if (!ready || inFlight || ++count > 32) { stop('invalid_request', 'Invalid or excessive mediated requests'); return; }
        let request: { id?: number };
        try { request = JSON.parse(line); } catch { stop('invalid_request', 'Invalid mediated request'); return; }
        if (!Number.isSafeInteger(request?.id)) { stop('invalid_request', 'Invalid mediated request'); return; }
        inFlight = true;
        void Promise.resolve().then(() => broker ? broker(request, stopped.signal) : denied('Mediated network is unavailable')).then(
          result => ({ id: request.id, ...result }),
          error => ({ id: request.id, code: error instanceof RequestError ? error.code : 'request_failed',
            error: error instanceof RequestError ? redact(error.message) : 'Mediated HTTP request failed' }),
        ).then(result => {
          inFlight = false;
          if (!stopped.signal.aborted) responses.write(JSON.stringify(result) + '\n');
        });
      }
    });
    const result = Promise.withResolvers<SandboxResult>();
    child.once('error', () => { failure ??= new RequestError('sandbox_unavailable', 'OS skill sandbox could not launch'); });
    child.once('close', code => {
      clearTimeout(timeout);
      stopped.abort();
      signal?.removeEventListener('abort', onAbort);
      this.runs.delete(child);
      done.resolve();
      if (failure) result.reject(failure);
      else if (!ready) result.reject(new RequestError('sandbox_unavailable', 'OS skill sandbox did not establish isolation'));
      else result.resolve({ ok: code === 0, stdout: redact(Buffer.concat(stdout).toString('utf8')), stderr: redact(Buffer.concat(stderr).toString('utf8')) });
    });
    return result.promise;
  }

  async shutdown(): Promise<void> {
    this.closing = true;
    const runs = [...this.runs.values()];
    for (const run of runs) run.stop();
    await Promise.all(runs.map(run => run.done));
  }

  killOnExit(): void { for (const run of this.runs.values()) run.stop(); }
}
