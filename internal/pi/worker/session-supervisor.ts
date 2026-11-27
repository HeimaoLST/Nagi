/// <reference lib="es2024.promise" />

import { spawn, type ChildProcessByStdio } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { constants as fsConstants, accessSync, statSync } from 'node:fs';
import { constants as osConstants } from 'node:os';
import { isAbsolute } from 'node:path';
import type { Readable, Writable } from 'node:stream';
import { fileURLToPath } from 'node:url';
import { RequestError, type SessionChunk, type SessionCloseResult, type SessionReadResult, type SessionSummary,
  type SessionWatchOccurrence, type SessionWatchSummary } from './protocol.js';
import { clipUTF8, projectionRedactor, streamingProjectionRedactor, type StreamingProjectionRedactor } from './product-redactor.js';
import type { ProductSQLite, RuntimeSession, RuntimeSessionWrite } from './product-sqlite.js';

const HEADER = 5;
const MAX_FRAME = 8192;
export const MAX_SESSION_INPUT = 16384;
const OUTPUT_CAPACITY = 256 * 1024;
const MAX_RETAINED_SESSIONS = 64;
const MAX_ACTIVE_PER_BOT = 8;
const MAX_ACTIVE_GLOBAL = 32;
const MAX_ARMED_WATCHES = 32;
const AUTH_MESSAGE = 'Do not enter passwords or secrets into Nagi chat/prompts; complete authentication externally or close this session.';
const authPrompt = /(?:password\s*:|passphrase[^\n]{0,80}:|enter\s+(?:a\s+)?pin\b)/i;
const controlErrors = [null, 'auth_paused', 'input_closed', 'unsupported_operation', 'write_unknown'] as const;
export const sessionSignals = ['SIGINT', 'SIGTERM', 'SIGHUP', 'SIGKILL'] as const;
type Stream = SessionChunk['stream'];
type CloseReason = Extract<SessionSummary['transportStatus'], `closed_${string}`>;
type NativeOutcome = { childReaped: boolean; exitCode?: number; exitSignal?: string; execFailed?: boolean };
type SessionActor = { botId: string; conversationId?: string; activityId?: string };
type NativeCommand = Pick<SessionSummary, 'mode' | 'cwd' | 'cols' | 'rows'> & { command: string; args: string[] };
type OpenSession = NativeCommand & Pick<SessionSummary, 'label' | 'creatingOperationId' | 'originConversationId' | 'originActivityId' | 'ownerBotId'>;
type WatchInput = { start_offset?: number; on_exit?: boolean; match_literal?: string; idle_ms?: number; timeout_ms?: number };
type CloseCommit = (write: RuntimeSessionWrite, result: SessionCloseResult) => void;
type WatchKind = NonNullable<SessionWatchSummary['firedKind']>;
const now = () => new Date().toISOString();

export function supervisorPath(): string {
  const bundled = fileURLToPath(new URL('../bin/nagi-pty-supervisor', import.meta.url));
  const path = statSync(bundled, { throwIfNoEntry: false }) ? bundled : process.env.NAGI_PTY_SUPERVISOR;
  if (!path || !isAbsolute(path) || !statSync(path, { throwIfNoEntry: false })?.isFile()) {
    throw new RequestError('exec_failed', 'The native session supervisor is unavailable');
  }
  accessSync(path, fsConstants.X_OK);
  return path;
}

export class NativeSession {
  private readonly process: ChildProcessByStdio<Writable, Readable, null>;
  private buffer: Buffer = Buffer.alloc(0);
  private outcome: NativeOutcome = { childReaped: false };
  private readonly started = Promise.withResolvers<boolean>();
  private readonly settled = Promise.withResolvers<NativeOutcome>();
  private pending?: PromiseWithResolvers<number>;
  private endRequested = false;
  readonly ready = this.started.promise;
  readonly done = this.settled.promise;

  constructor(path: string, input: NativeCommand, receive: (type: number, data: Buffer) => void, failed: () => void, environment?: NodeJS.ProcessEnv) {
    const { HOME, TMPDIR, LANG, PATH } = process.env;
    const env = environment ?? { HOME, TMPDIR, LANG, PATH: PATH || '/usr/bin:/bin:/usr/sbin:/sbin',
      ...(input.mode === 'pty' ? { TERM: 'xterm-256color' } : {}) };
    this.process = spawn(path, ['1', input.mode, String(input.cols ?? 80), String(input.rows ?? 24), input.command, ...input.args],
      { cwd: input.cwd, env, stdio: ['pipe', 'pipe', 'ignore'] });
    this.process.stdin.on('error', () => this.pending?.resolve(4));
    this.process.stdout.on('error', () => this.release());
    this.process.on('error', () => { this.outcome.execFailed = true; });
    this.process.on('close', () => {
      this.started.resolve(false);
      this.pending?.resolve(4);
      this.settled.resolve(this.outcome);
    });
    this.process.stdout.on('data', (chunk: Buffer) => {
      try {
        this.buffer = this.buffer.length ? Buffer.concat([this.buffer, chunk]) : chunk;
        while (this.buffer.length >= HEADER) {
          const bytes = this.buffer.readUInt32BE(1);
          if (bytes > MAX_FRAME) throw new Error('Oversized supervisor frame');
          if (this.buffer.length < HEADER + bytes) break;
          const type = this.buffer[0];
          const data = this.buffer.subarray(HEADER, HEADER + bytes);
          this.buffer = this.buffer.subarray(HEADER + bytes);
          if (type === 16 && bytes === 0) {
            receive(type, data);
            this.started.resolve(true);
          } else if (type >= 17 && type <= 19 && bytes || type === 20 && bytes === 1 && data[0] <= 3) {
            receive(type, data);
          } else if (type === 21 && bytes === 1 && data[0] < controlErrors.length && this.pending) {
            this.pending.resolve(data[0]);
          } else if (type === 22 && bytes === 2 && data[0] <= 2) {
            this.outcome = { childReaped: data[0] !== 0, ...(data[0] === 1 ? { exitCode: data[1] } : {}),
              ...(data[0] === 2 ? { exitSignal: Object.entries(osConstants.signals).find(([, code]) => code === data[1])?.[0] } : {}) };
          } else if (type === 23 && bytes === 1) {
            this.outcome = { childReaped: true, execFailed: true };
          } else {
            throw new Error('Invalid supervisor frame');
          }
        }
      } catch {
        this.release();
        failed();
      }
    });
  }

  async control(type: number, data: Buffer): Promise<void> {
    if (this.endRequested || this.pending) throw new RequestError('session_busy', 'The session is closing or has an input operation in flight');
    const reply = this.pending = Promise.withResolvers<number>();
    const frame = Buffer.allocUnsafe(HEADER + data.length);
    frame[0] = type;
    frame.writeUInt32BE(data.length, 1);
    data.copy(frame, HEADER);
    this.process.stdin.write(frame, error => { if (error) reply.resolve(4); });
    const timeout = setTimeout(() => { this.release(); reply.resolve(4); }, 1000);
    try {
      const error = controlErrors[await reply.promise];
      if (error) throw new RequestError(error, error === 'auth_paused' ? AUTH_MESSAGE : 'The supervisor could not confirm the requested session operation');
    } finally {
      clearTimeout(timeout);
      this.pending = undefined;
    }
  }

  async close(): Promise<NativeOutcome> {
    if (!this.endRequested) {
      this.endRequested = true;
      this.process.stdin.write(Buffer.from([5, 0, 0, 0, 0]), error => { if (error) this.release(); });
    }
    const release = setTimeout(() => this.release(), 750);
    const deadline = Promise.withResolvers<NativeOutcome>();
    const timeout = setTimeout(() => deadline.reject(new RequestError('reap_unconfirmed', 'The session supervisor did not confirm termination')), 2000);
    try {
      return await Promise.race([this.done, deadline.promise]);
    } finally {
      clearTimeout(release);
      clearTimeout(timeout);
    }
  }

  release(): void {
    this.endRequested = true;
    this.process.stdin.destroy();
    this.process.kill('SIGTERM');
  }
}

export class TerminalProjection {
  private readonly decoder = new TextDecoder();
  private escape: 'text' | 'esc' | 'intermediate' | 'csi' | 'string' | 'string_escape' = 'text';
  private cr = false;

  push(data?: Buffer): string {
    const text = this.decoder.decode(data, { stream: data !== undefined });
    let output = '';
    for (const point of text) {
      const code = point.charCodeAt(0);
      switch (this.escape) {
        case 'text':
          if (point === '\x1b') this.escape = 'esc';
          else if (code === 0x9b) this.escape = 'csi';
          else if (code === 0x90 || code === 0x98 || code === 0x9d || code === 0x9e || code === 0x9f) this.escape = 'string';
          else if (point === '\r') this.cr = true;
          else if (point === '\n' || point === '\t' || code >= 0x20 && (code < 0x7f || code > 0x9f)) {
            if (this.cr && point !== '\n') output += '\n';
            this.cr = false;
            output += point;
          }
          break;
        case 'esc':
          if (point === '[') this.escape = 'csi';
          else if (']PX^_'.includes(point)) this.escape = 'string';
          else if (code >= 0x20 && code <= 0x2f) this.escape = 'intermediate';
          else if (point !== '\x1b') this.escape = 'text';
          break;
        case 'intermediate':
        case 'csi':
          if (point === '\x1b') this.escape = 'esc';
          else if (code === 0x18 || code === 0x1a || code >= (this.escape === 'csi' ? 0x40 : 0x30)) this.escape = 'text';
          break;
        case 'string':
          if (point === '\x1b') this.escape = 'string_escape';
          else if (code === 0x07 || code === 0x9c || code === 0x18 || code === 0x1a) this.escape = 'text';
          break;
        case 'string_escape':
          if (point === '\\' || code === 0x07 || code === 0x9c) this.escape = 'text';
          else if (point !== '\x1b') this.escape = 'string';
          break;
      }
    }
    if (data === undefined && this.cr) {
      this.cr = false;
      output += '\n';
    }
    return output;
  }
}

type Segment = { stream: Stream; offset: number; data: Buffer };

function utf8Boundary(bytes: Buffer, index: number, direction: 1 | -1): number {
  while (index > 0 && index < bytes.length && (bytes[index] & 0xc0) === 0x80) index += direction;
  return index;
}

class OutputRing {
  private segments: (Segment | undefined)[] = [];
  private head = 0;
  firstAvailableOffset = 0;
  nextOffset = 0;

  append(stream: Stream, data: Buffer): number {
    const offset = this.nextOffset;
    this.nextOffset += data.length;
    this.segments.push({ stream, offset, data });
    const minimum = Math.max(this.nextOffset - OUTPUT_CAPACITY, 0);
    let segment = this.segments[this.head]!;
    while (segment.offset + segment.data.length <= minimum) {
      this.segments[this.head++] = undefined;
      segment = this.segments[this.head]!;
    }
    const trim = utf8Boundary(segment.data, Math.max(minimum - segment.offset, 0), 1);
    if (trim) {
      segment.offset += trim;
      segment.data = segment.data.subarray(trim);
    }
    this.firstAvailableOffset = segment.offset;
    if (this.head >= 1024) {
      this.segments = this.segments.slice(this.head);
      this.head = 0;
    }
    return offset;
  }

  clear(): void {
    this.segments = [];
    this.head = 0;
    this.firstAvailableOffset = this.nextOffset;
  }

  *from(offset: number): Iterable<Segment> {
    for (let index = this.head; index < this.segments.length; index++) {
      const segment = this.segments[index]!;
      if (segment.offset + segment.data.length <= offset) continue;
      const start = Math.max(offset - segment.offset, 0);
      yield { ...segment, offset: segment.offset + start, data: segment.data.subarray(start) };
    }
  }

  read(requested: number, limit: number): { fromOffset: number; nextOffset: number; chunks: SessionChunk[] } {
    let fromOffset = Math.min(Math.max(requested, this.firstAvailableOffset), this.nextOffset);
    for (let index = this.head; index < this.segments.length; index++) {
      const { offset, data } = this.segments[index]!;
      if (fromOffset >= offset + data.length) continue;
      fromOffset = offset + utf8Boundary(data, fromOffset - offset, 1);
      break;
    }
    let nextOffset = fromOffset;
    const chunks: SessionChunk[] = [];
    for (const { stream, offset, data } of this.from(fromOffset)) {
      const end = utf8Boundary(data, Math.min(data.length, fromOffset + limit - offset), -1);
      if (end <= 0) break;
      const text = data.toString('utf8', 0, end);
      nextOffset = offset + end;
      const last = chunks.at(-1);
      if (last?.stream === stream) {
        last.text += text;
        last.nextOffset = nextOffset;
      } else {
        chunks.push({ stream, offset, nextOffset, text });
      }
      if (end < data.length || nextOffset === fromOffset + limit) break;
    }
    return { fromOffset, nextOffset, chunks };
  }
}

class LiteralMatch {
  private readonly prefix: number[] = [0];
  private readonly bytes: Buffer;
  private matched = 0;

  constructor(readonly literal: string) {
    this.bytes = Buffer.from(literal);
    for (let index = 1, length = 0; index < this.bytes.length; index++) {
      while (length && this.bytes[index] !== this.bytes[length]) length = this.prefix[length - 1];
      if (this.bytes[index] === this.bytes[length]) length++;
      this.prefix.push(length);
    }
  }

  feed(data: Buffer, offset: number, startOffset: number): number | undefined {
    for (let index = Math.max(startOffset - offset, 0); index < data.length; index++) {
      const byte = data[index];
      while (this.matched && byte !== this.bytes[this.matched]) this.matched = this.prefix[this.matched - 1];
      if (byte === this.bytes[this.matched]) this.matched++;
      if (this.matched === this.bytes.length) return offset + index + 1 - this.bytes.length;
    }
  }
}

type ArmedWatch = { watchId: string; matcher?: LiteralMatch; idle?: ReturnType<typeof setTimeout>; timeout?: ReturnType<typeof setTimeout> };

type LiveHandle = {
  row: RuntimeSession;
  native: NativeSession;
  ring: OutputRing;
  projection: Map<Stream, TerminalProjection>;
  redactor: StreamingProjectionRedactor;
  origins: { stream: Stream; length: number }[];
  echo: number;
  prompt: string;
  watches: Map<string, ArmedWatch>;
  done: Promise<RuntimeSession>;
  closing?: { reason: CloseReason; persist?: CloseCommit };
  outputTimer?: ReturnType<typeof setImmediate>;
};

export class RuntimeSessions {
  private readonly live = new Map<string, LiveHandle>();
  private readonly retained = new Map<string, OutputRing>();
  private draining = false;
  private recheckTimer?: ReturnType<typeof setInterval>;

  constructor(private readonly storage: ProductSQLite, private readonly keys: () => string[],
    private readonly emit: (event: string, value: object) => void, private readonly disconnect: () => void) {}

  static summary({ epoch, childReaped, ...row }: RuntimeSession): SessionSummary {
    return row;
  }

  get(botId: string, sessionId: string, actor?: SessionActor, mutate = false): RuntimeSession {
    this.storage.assertSessionEpoch();
    const current = this.live.get(sessionId)?.row;
    const row = current?.ownerBotId === botId ? current : this.storage.runtimeSessions(botId, sessionId)[0];
    if (!row || actor && !this.visible(row, actor, mutate)) {
      throw new RequestError(actor ? 'scope_denied' : 'not_found', 'The session is unavailable in this scope');
    }
    return this.withWatermark(row);
  }

  list(botId: string, actor?: SessionActor): RuntimeSession[] {
    this.storage.assertSessionEpoch();
    return this.storage.runtimeSessions(botId).filter(row => !actor || this.visible(row, actor, false)).map(row => this.withWatermark(row));
  }

  generation(row: RuntimeSession, generation: number): void {
    if (row.generation !== generation) throw new RequestError('stale_generation', 'The session generation has changed');
  }

  private visible(row: RuntimeSession, actor: SessionActor, mutate: boolean): boolean {
    return row.ownerBotId === actor.botId && (actor.activityId ? row.originActivityId === actor.activityId :
      !mutate || !row.originActivityId && row.originConversationId === actor.conversationId);
  }

  private withWatermark(row: RuntimeSession): RuntimeSession {
    const ring = this.retained.get(row.sessionId);
    const nextOffset = ring?.nextOffset ?? row.nextOffset;
    const firstAvailableOffset = ring?.firstAvailableOffset ?? nextOffset;
    return { ...row, firstAvailableOffset, nextOffset, evictedBytes: firstAvailableOffset };
  }

  read(row: RuntimeSession, offset?: number, limit = 16384): SessionReadResult {
    const { sessionId, generation, revision, mode, state, firstAvailableOffset, nextOffset: highWaterOffset, eof, authPause } = row;
    const requested = offset ?? Math.max(firstAvailableOffset, highWaterOffset - limit);
    const gapBytes = Math.max(firstAvailableOffset - requested, 0);
    const result = this.retained.get(sessionId)?.read(requested, limit) ?? { fromOffset: highWaterOffset, nextOffset: highWaterOffset, chunks: [] };
    return { sessionId, generation, revision, mode, state, firstAvailableOffset, highWaterOffset, eof, authPause,
      ...result, gap: gapBytes > 0, gapBytes };
  }

  async open(input: OpenSession): Promise<SessionSummary> {
    this.checkAdmission();
    if (this.live.size >= MAX_ACTIVE_GLOBAL || [...this.live.values()].filter(handle => handle.row.ownerBotId === input.ownerBotId).length >= MAX_ACTIVE_PER_BOT) {
      throw new RequestError('session_capacity', 'The active session limit is reached');
    }
    const path = supervisorPath();
    const { command, args, mode, label, ...origin } = input;
    const redact = projectionRedactor(this.keys());
    const createdAt = now();
    const ring = new OutputRing();
    const row: RuntimeSession = { ...origin, mode, label: label === undefined ? undefined : clipUTF8(redact(label), 256),
      sessionId: randomUUID(), generation: 1, revision: 1, commandSummary: clipUTF8(redact([command, ...args].join(' ')), 1024),
      state: 'starting', transportStatus: 'live', firstAvailableOffset: 0, nextOffset: 0, evictedBytes: 0, eof: false,
      watches: [], createdAt, updatedAt: createdAt, epoch: this.storage.sessionEpoch };
    this.storage.saveRuntimeSession({ session: row });
    this.emit('session.changed', RuntimeSessions.summary(row));
    let native: NativeSession;
    try {
      native = new NativeSession(path, input, (type, data) => this.nativeEvent(handle, type, data), this.disconnect);
    } catch {
      this.persist({ ...row, revision: 2, state: 'failed', transportStatus: 'exec_failed', settledAt: now(), eof: true,
        error: 'The native session supervisor could not start' });
      throw new RequestError('exec_failed', 'The native session supervisor could not start');
    }
    const handle: LiveHandle = { row, native, ring, echo: 0, prompt: '', projection: new Map(), origins: [], watches: new Map(),
      redactor: streamingProjectionRedactor(this.keys(), (text, length, match) => this.publishText(handle, text, length, match)),
      done: native.done.then(outcome => this.finish(handle, outcome)) };
    void handle.done.catch(this.disconnect);
    this.live.set(row.sessionId, handle);
    this.retained.set(row.sessionId, ring);
    for (const sessionId of this.retained.keys()) {
      if (this.retained.size <= MAX_RETAINED_SESSIONS) break;
      if (!this.live.has(sessionId)) this.retained.delete(sessionId);
    }
    this.recheckTimer ??= setInterval(() => {
      try { this.storage.assertSessionEpoch(); }
      catch { this.killOnExit(); this.disconnect(); }
    }, 1000).unref();
    if (!await native.ready) {
      const settled = await handle.done;
      throw new RequestError(settled.transportStatus, settled.error ?? 'The session did not start');
    }
    return RuntimeSessions.summary(this.withWatermark(handle.row));
  }

  private checkAdmission(): void {
    if (this.draining) throw new RequestError('closing', 'Session admission is closed');
    this.storage.assertSessionEpoch();
  }

  private active(row: RuntimeSession): LiveHandle {
    this.checkAdmission();
    const handle = this.live.get(row.sessionId);
    if (!handle || handle.closing || handle.row.state !== 'running') throw new RequestError('session_settled', 'The session no longer accepts input');
    return handle;
  }

  async write(row: RuntimeSession, bytes: Buffer): Promise<object> {
    const handle = this.active(row);
    if (handle.row.authPause?.paused) throw new RequestError('auth_paused', AUTH_MESSAGE);
    await handle.native.control(1, bytes);
    return { acceptedBytes: bytes.length, sessionId: row.sessionId, generation: row.generation };
  }

  async resize(row: RuntimeSession, cols: number, rows: number): Promise<SessionSummary> {
    const handle = this.active(row);
    const bytes = Buffer.allocUnsafe(4);
    bytes.writeUInt16BE(cols);
    bytes.writeUInt16BE(rows, 2);
    await handle.native.control(2, bytes);
    if (!handle.row.eof) this.update(handle, { cols, rows });
    return RuntimeSessions.summary(this.withWatermark(handle.row));
  }

  async signal(row: RuntimeSession, signal: typeof sessionSignals[number]): Promise<object> {
    await this.active(row).native.control(3, Buffer.from([sessionSignals.indexOf(signal)]));
    return { sessionId: row.sessionId, generation: row.generation, signal };
  }

  async eof(row: RuntimeSession): Promise<object> {
    await this.active(row).native.control(4, Buffer.alloc(0));
    return { sessionId: row.sessionId, generation: row.generation, inputClosed: true };
  }

  async close(row: RuntimeSession, reason: CloseReason, persist?: CloseCommit): Promise<SessionCloseResult> {
    const handle = this.live.get(row.sessionId);
    if (!handle) return this.closeResult(row, false);
    const first = !handle.closing;
    handle.closing ??= { reason, persist };
    await handle.native.close();
    return this.closeResult(await handle.done, first);
  }

  closeResult(row: RuntimeSession, terminated: boolean): SessionCloseResult {
    const { sessionId, generation, revision, state, exitCode, exitSignal, transportStatus } = row;
    return { sessionId, generation, revision, state, closeDisposition: terminated ? 'terminated' : 'already_settled',
      childReaped: row.childReaped ?? false, exitCode: exitCode ?? null, exitSignal, transportStatus };
  }

  async drain(): Promise<void> {
    this.draining = true;
    clearInterval(this.recheckTimer);
    this.recheckTimer = undefined;
    const results = await Promise.all([...this.live.values()].map(handle => this.close(handle.row, 'closed_on_shutdown')));
    if (results.some(result => !result.childReaped && result.transportStatus !== 'exec_failed')) {
      throw new RequestError('reap_unconfirmed', 'A session supervisor did not confirm child reaping');
    }
  }

  killOnExit(): void {
    for (const handle of this.live.values()) handle.native.release();
  }

  refreshCredentials(): void {
    const keys = this.keys();
    const redact = projectionRedactor(keys);
    for (const [id, ring] of this.retained) {
      const handle = this.live.get(id);
      handle?.redactor.update(keys);
      const text = [...ring.from(ring.firstAvailableOffset)].map(segment => segment.data.toString()).join('');
      if (redact(text) === text) continue;
      ring.clear();
      if (handle) {
        for (const watch of handle.watches.values()) if (watch.matcher) watch.matcher = new LiteralMatch(watch.matcher.literal);
        this.queueOutput(handle);
      }
    }
  }

  private persist(row: RuntimeSession, occurrences?: SessionWatchOccurrence[]): void {
    this.storage.saveRuntimeSession({ session: row, occurrences });
    this.emit('session.changed', RuntimeSessions.summary(row));
  }

  private update(handle: LiveHandle, change: Partial<RuntimeSession>, occurrences?: SessionWatchOccurrence[]): void {
    const row = { ...this.withWatermark(handle.row), ...change, revision: handle.row.revision + 1, updatedAt: now() };
    this.persist(row, occurrences);
    handle.row = row;
  }

  private nativeEvent(handle: LiveHandle, type: number, data: Buffer): void {
    if (type === 16) {
      if (!handle.closing) this.update(handle, { state: 'running' });
      return;
    }
    if (type === 20) {
      handle.echo = data[0];
      handle.prompt = '';
      this.auth(handle, data[0] === 3 ? 'canonical_no_echo' : undefined);
      return;
    }
    const stream = type === 17 ? 'pty' : type === 18 ? 'stdout' : 'stderr';
    let projection = handle.projection.get(stream);
    if (!projection) {
      projection = new TerminalProjection();
      handle.projection.set(stream, projection);
    }
    this.project(handle, stream, projection.push(data));
  }

  private project(handle: LiveHandle, stream: Stream, text: string): void {
    if (!text) return;
    handle.origins.push({ stream, length: text.length });
    handle.redactor(text);
    if (handle.echo !== 1 && handle.echo !== 3 && !handle.closing) {
      handle.prompt = (handle.prompt + text).slice(-160);
      if (authPrompt.test(handle.prompt)) this.auth(handle, 'auth_prompt');
    }
  }

  private auth(handle: LiveHandle, reason?: string): void {
    if (handle.closing || handle.row.authPause?.reason === reason) return;
    this.update(handle, { authPause: reason ? { paused: true, reason, message: AUTH_MESSAGE } : undefined });
  }

  private publishText(handle: LiveHandle, text: string, length: number, match: boolean): void {
    if (match && text) this.append(handle, handle.origins[0].stream, text);
    let start = 0;
    while (length > 0) {
      const origin = handle.origins[0];
      const take = Math.min(length, origin.length);
      if (!match) this.append(handle, origin.stream, text.slice(start, start + take));
      origin.length -= take;
      if (!origin.length) handle.origins.shift();
      length -= take;
      start += take;
    }
  }

  private append(handle: LiveHandle, stream: Stream, text: string): void {
    const bytes = Buffer.from(text);
    const offset = handle.ring.append(stream, bytes);
    if (!handle.closing) {
      for (const watch of handle.watches.values()) {
        const row = handle.row.watches.find(row => row.watchId === watch.watchId)!;
        const found = watch.matcher?.feed(bytes, offset, row.startOffset);
        if (found !== undefined) this.fire(handle, row, 'match', found, watch.matcher!.literal);
        else if (row.idleMs !== undefined) this.resetIdle(handle, watch, row);
      }
    }
    this.queueOutput(handle);
  }

  private queueOutput(handle: LiveHandle): void {
    handle.outputTimer ??= setImmediate(() => {
      handle.outputTimer = undefined;
      try {
        const row = this.withWatermark(handle.row);
        this.storage.saveRuntimeSession({ session: row });
        handle.row = row;
        this.emitOutput(row);
      } catch { this.disconnect(); }
    });
  }

  private emitOutput(row: RuntimeSession): void {
    const { sessionId, ownerBotId, generation, firstAvailableOffset, nextOffset, evictedBytes, eof } = row;
    this.emit('session.outputUpdated', { sessionId, ownerBotId, generation, firstAvailableOffset, nextOffset, evictedBytes, eof, at: now() });
  }

  arm(row: RuntimeSession, input: WatchInput): SessionWatchSummary {
    this.checkAdmission();
    if (!row.epoch) throw new RequestError('scope_denied', 'Restored session history cannot arm a watch');
    const handle = this.live.get(row.sessionId);
    if (handle?.closing) throw new RequestError('session_settled', 'The session is closing');
    if ((handle?.watches.size ?? 0) >= MAX_ARMED_WATCHES) throw new RequestError('watch_capacity', 'The session watch limit is reached');
    const { on_exit, match_literal, idle_ms, timeout_ms, start_offset = row.nextOffset } = input;
    const conditions: SessionWatchSummary['conditions'] = [];
    if (on_exit) conditions.push('exit');
    if (match_literal !== undefined) {
      if (!match_literal || Buffer.byteLength(match_literal) > 256 || projectionRedactor(this.keys())(match_literal) !== match_literal) {
        throw new RequestError('invalid_params', 'The match literal must be 1 to 256 non-secret UTF-8 bytes');
      }
      conditions.push('match');
    }
    if (idle_ms !== undefined) conditions.push('idle');
    if (timeout_ms !== undefined) conditions.push('timeout');
    if (!conditions.length || start_offset < row.firstAvailableOffset || start_offset > row.nextOffset) {
      throw new RequestError('invalid_params', 'Choose a condition and an available start offset');
    }
    let watch: SessionWatchSummary = { watchId: randomUUID(), sessionId: row.sessionId, generation: row.generation,
      revision: 1, state: 'armed', conditions, startOffset: start_offset, matchLiteral: match_literal, idleMs: idle_ms, timeoutMs: timeout_ms };
    const matcher = match_literal === undefined ? undefined : new LiteralMatch(match_literal);
    let fired: WatchKind | undefined;
    let offset: number | undefined;
    const ring = this.retained.get(row.sessionId);
    if (matcher && ring) {
      for (const segment of ring.from(start_offset)) {
        offset = matcher.feed(segment.data, segment.offset, start_offset);
        if (offset !== undefined) { fired = 'match'; break; }
      }
    }
    if (!fired && !handle) fired = on_exit ? 'exit' : 'target_lost';
    const occurrences: SessionWatchOccurrence[] = [];
    if (fired) watch = this.settleWatch(row, watch, fired, occurrences, offset, offset === undefined ? undefined : match_literal);
    const watches = [...row.watches, watch];
    if (handle) this.update(handle, { watches }, occurrences);
    else this.persist({ ...row, watches, revision: row.revision + 1, updatedAt: now() }, occurrences);
    if (handle && !fired) {
      const armed: ArmedWatch = { watchId: watch.watchId, matcher };
      handle.watches.set(watch.watchId, armed);
      if (idle_ms !== undefined) this.resetIdle(handle, armed, watch);
      if (timeout_ms !== undefined) armed.timeout = setTimeout(() => this.fire(handle, watch, 'timeout'), timeout_ms).unref();
    }
    return watch;
  }

  cancel(row: RuntimeSession, watchId: string): SessionWatchSummary {
    this.checkAdmission();
    const watch = row.watches.find(watch => watch.watchId === watchId);
    if (!watch) throw new RequestError('not_found', 'The watch does not exist in this session generation');
    const handle = this.live.get(row.sessionId);
    if (handle?.closing) throw new RequestError('session_settled', 'The session is closing');
    if (!handle || watch.state !== 'armed') return watch;
    const cancelled = { ...watch, state: 'cancelled' as const, revision: watch.revision + 1 };
    this.update(handle, { watches: row.watches.map(watch => watch.watchId === watchId ? cancelled : watch) });
    this.clearWatch(handle, watchId);
    return cancelled;
  }

  private resetIdle(handle: LiveHandle, armed: ArmedWatch, row: SessionWatchSummary): void {
    clearTimeout(armed.idle);
    armed.idle = setTimeout(() => this.fire(handle, row, 'idle'), row.idleMs).unref();
  }

  private clearWatch(handle: LiveHandle, watchId: string): void {
    const armed = handle.watches.get(watchId);
    if (!armed) return;
    clearTimeout(armed.idle);
    clearTimeout(armed.timeout);
    handle.watches.delete(watchId);
  }

  private settleWatch(row: RuntimeSession, watch: SessionWatchSummary, firedKind: WatchKind,
    occurrences: SessionWatchOccurrence[], firedOffset?: number, excerpt?: string): SessionWatchSummary {
    const fired = { occurrenceId: randomUUID(), firedKind, firedAt: now(), firedOffset,
      matchedExcerpt: excerpt === undefined ? undefined : projectionRedactor(this.keys())(excerpt) };
    occurrences.push({ ...fired, watchId: watch.watchId, sessionId: row.sessionId, ownerBotId: row.ownerBotId, generation: row.generation });
    return { ...watch, ...fired, state: firedKind === 'target_lost' ? 'target_lost' : 'fired', revision: watch.revision + 1 };
  }

  private fire(handle: LiveHandle, watch: SessionWatchSummary, kind: WatchKind, offset?: number, excerpt?: string): void {
    if (handle.closing || !handle.watches.has(watch.watchId)) return;
    try {
      const occurrences: SessionWatchOccurrence[] = [];
      const settled = this.settleWatch(handle.row, watch, kind, occurrences, offset, excerpt);
      this.update(handle, { watches: handle.row.watches.map(row => row.watchId === watch.watchId ? settled : row) }, occurrences);
      this.clearWatch(handle, watch.watchId);
    } catch { this.disconnect(); }
  }

  private finish(handle: LiveHandle, outcome: NativeOutcome): RuntimeSession {
    const { closing } = handle;
    const occurrences: SessionWatchOccurrence[] = [];
    try {
      for (const [stream, projection] of handle.projection) this.project(handle, stream, projection.push());
      handle.redactor('', true);
      const row = this.withWatermark(handle.row);
      const { childReaped, exitCode, exitSignal, execFailed } = outcome;
      const state = execFailed ? 'failed' : !childReaped ? 'disconnected' : closing ? 'closed' : 'exited';
      const transportStatus = execFailed ? 'exec_failed' : !childReaped ? 'supervisor_lost' : closing?.reason ?? 'eof';
      const at = now();
      const session: RuntimeSession = { ...row, state, transportStatus, childReaped, exitCode, exitSignal,
        error: execFailed ? 'The requested command could not start' : childReaped ? undefined : 'The supervisor exited without confirming child reaping',
        authPause: undefined, eof: true, revision: row.revision + 1, updatedAt: at, settledAt: at,
        watches: row.watches.map(watch => {
          if (watch.state !== 'armed') return watch;
          if (closing) return { ...watch, state: 'cancelled', revision: watch.revision + 1 };
          return this.settleWatch(row, watch, childReaped && watch.conditions.includes('exit') ? 'exit' : 'target_lost', occurrences);
        }) };
      if (closing?.persist) {
        closing.persist({ session, occurrences }, this.closeResult(session, true));
        this.emit('session.changed', RuntimeSessions.summary(session));
      } else this.persist(session, occurrences);
      handle.row = session;
      this.emitOutput(session);
      return session;
    } finally {
      clearImmediate(handle.outputTimer);
      for (const watchId of handle.watches.keys()) this.clearWatch(handle, watchId);
      this.live.delete(handle.row.sessionId);
      if (!this.live.size) {
        clearInterval(this.recheckTimer);
        this.recheckTimer = undefined;
      }
    }
  }
}
