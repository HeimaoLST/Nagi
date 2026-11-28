import { createHash, createHmac, randomUUID } from 'node:crypto';
import { chmodSync, lstatSync, mkdirSync, readdirSync, renameSync, rmSync, rmdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { SessionEntry } from '@earendil-works/pi-coding-agent';
import { Value } from 'typebox/value';
import { RequestError, hasCanonicalQuoteText, productQuote, quoteAuthorMaxBytes, quoteTextMaxBytes, type Material, type ProductQuote } from './protocol.js';
import { clipEncodedText, clipUTF8 } from './product-redactor.js';
import { turnEntryType, type Transcript } from './product-transcript.js';

export type InspectionParams = { cursor?: string; limit?: number; maxBytes?: number };
export const encodedBytes = (value: unknown) => Buffer.byteLength(JSON.stringify(value));
const scanEntries = 256;
const scanBytes = 4 * 1024 * 1024;

type Position = { high: number; position: number; tail: boolean; highEdge: string; positionEdge: string };
export type InspectionCursor = Pick<Position, 'high' | 'position' | 'tail'> & Partial<Pick<Position, 'highEdge' | 'positionEdge'>>;

export function invalidCursor(): never {
  throw new RequestError('invalid_cursor', 'The inspection cursor is invalid or no longer available');
}

export function oversizedEntry(): never {
  throw new RequestError('entry_too_large', 'An inspection entry cannot fit in the response');
}

export function cursorScope(method: string, target: string, source: string, executionId?: string, query?: string): string {
  return createHash('sha256').update(JSON.stringify([method, target, source, executionId, query])).digest('base64url');
}

export function decodeCursor(cursor: string | undefined, scope: string, tail?: boolean): Position | undefined {
  if (cursor === undefined) return;
  if (Buffer.byteLength(cursor) > 1024 || !/^[A-Za-z0-9_-]+$/.test(cursor)) return invalidCursor();
  try {
    const bytes = Buffer.from(cursor, 'base64url');
    if (bytes.toString('base64url') !== cursor) return invalidCursor();
    const data: unknown = JSON.parse(bytes.toString('utf8'));
    if (!Array.isArray(data) || data.length !== 7) return invalidCursor();
    const [version, high, position, reverse, highEdge, positionEdge, signature] = data;
    if (version !== 1 || !Number.isSafeInteger(high) || !Number.isSafeInteger(position) ||
        position <= 0 || position >= high || typeof reverse !== 'boolean' ||
        (tail !== undefined && reverse !== tail) || typeof highEdge !== 'string' || typeof positionEdge !== 'string') return invalidCursor();
    const expected = createHmac('sha256', scope).update(JSON.stringify(data.slice(0, -1))).digest('base64url');
    if (signature !== expected) return invalidCursor();
    return { high, position, tail: reverse, highEdge, positionEdge };
  } catch {
    return invalidCursor();
  }
}

export function encodeCursor(scope: string, { high, position, tail, highEdge = '', positionEdge = '' }: InspectionCursor): string {
  const fields = [1, high, position, tail, highEdge, positionEdge];
  const signature = createHmac('sha256', scope).update(JSON.stringify(fields)).digest('base64url');
  const cursor = Buffer.from(JSON.stringify([...fields, signature])).toString('base64url');
  if (cursor.length > 1024) return oversizedEntry();
  return cursor;
}

type Message = { messageId: string; role: 'user' | 'assistant'; timestamp: string; text: string; materials: Material[]; quote?: ProductQuote };

export function publicConversationMessage(entry: SessionEntry): Message | undefined {
  const base = { messageId: entry.id, timestamp: entry.timestamp };
  if (entry.type === 'custom' && entry.customType === turnEntryType) {
    const turn = entry.data as { text: string; materials?: Material[]; context?: { name: string; text: string }[]; quote?: unknown };
    const materials = turn.materials ?? (turn.context ?? []).filter(item => !item.name.startsWith('xuhu.issue_tools.')).map((item, index) => ({ id: `${entry.id}:${index}`, revision: 0, name: item.name, text: item.text }));
    const quote = Value.Check(productQuote, turn.quote) && hasCanonicalQuoteText(turn.quote) ? turn.quote : undefined;
    return { ...base, role: 'user' as const, text: turn.text, materials, ...(quote ? { quote } : {}) };
  }
  if (entry.type === 'message' && entry.message.role === 'assistant') {
    const text = entry.message.content.filter(part => part.type === 'text').map(part => part.text).join('');
    if (text) return { ...base, role: 'assistant' as const, text, materials: [] };
  }
}

type Turn = Omit<Message, 'materials'> & { materials: (Omit<Material, 'source'> & { truncated: boolean })[]; truncated: boolean };
type Hit = Pick<Message, 'messageId' | 'role' | 'timestamp'> & { field: 'text' | 'material'; materialId?: string; snippet: string; truncated: boolean };

export function redactQuote(quote: ProductQuote, redact: (text: string) => string): ProductQuote | undefined {
  if (redact(quote.messageId) !== quote.messageId) return;
  const authorName = clipUTF8(redact(quote.authorName).trim(), quoteAuthorMaxBytes);
  const text = clipUTF8(redact(quote.text).trim(), quoteTextMaxBytes);
  if (authorName && text) return { ...quote, authorName, text };
}

function redactMessage(message: Message, redact: (text: string) => string): Message {
  if (redact(message.messageId) !== message.messageId || message.materials.some(item => redact(item.id) !== item.id)) return oversizedEntry();
  const { quote, ...row } = message;
  const publicQuote = quote && redactQuote(quote, redact);
  return { ...row, text: redact(row.text), materials: row.materials.map(({ id, revision, name, text }) => ({ id, revision, name: redact(name), text: redact(text) })),
    ...(publicQuote ? { quote: publicQuote } : {}) };
}
function fitTurn(message: Message, budget: number): Turn | undefined {
  const { text, materials, quote, ...identity } = message;
  const turn: Turn = { ...identity, text: '', materials: [], truncated: true };
  let remaining = budget - encodedBytes(turn) - 1;
  if (remaining < 0) return;
  turn.text = clipEncodedText(text, remaining);
  remaining -= encodedBytes(turn.text) - 2;
  for (const { name, text, ...base } of materials) {
    const material = { ...base, name: '', text: '', truncated: true };
    const overhead = encodedBytes(material) + Number(turn.materials.length > 0) + 1;
    if (overhead > remaining) break;
    remaining -= overhead;
    material.name = clipEncodedText(name, remaining);
    remaining -= encodedBytes(material.name) - 2;
    material.text = clipEncodedText(text, remaining);
    remaining -= encodedBytes(material.text) - 2;
    material.truncated = material.name !== name || material.text !== text;
    turn.materials.push(material);
  }
  if (quote && encodedBytes({ quote }) - 1 <= remaining) turn.quote = quote;
  turn.truncated = turn.text !== text || turn.materials.length !== materials.length || turn.materials.some(material => material.truncated) || turn.quote !== quote;
  return turn;
}

function searchMessage(message: Message, query: string): Hit | undefined {
  const { messageId, role, timestamp, text, materials } = message;
  const lower = text.toLowerCase();
  let source = text;
  let index = lower.indexOf(query);
  let materialId: string | undefined;
  if (index === -1) {
    for (const material of materials) {
      source = material.name;
      index = source.toLowerCase().indexOf(query);
      if (index === -1) {
        source = material.text;
        index = source.toLowerCase().indexOf(query);
      }
      if (index !== -1) { materialId = material.id; break; }
    }
  }
  if (index === -1) return;
  let offset = index;
  if (source.length !== source.toLowerCase().length) {
    offset = 0;
    for (const point of source) {
      if (index <= 0) break;
      index -= point.toLowerCase().length;
      offset += point.length;
    }
  }
  let start = Math.max(0, offset - 20);
  if (start > 0 && /[\uDC00-\uDFFF]/u.test(source[start])) start--;
  const snippet = (start > 0 ? '…' : '') + clipUTF8(source.slice(start), start > 0 ? 509 : 512);
  return { messageId, role, timestamp, field: materialId === undefined ? 'text' : 'material',
    ...(materialId === undefined ? {} : { materialId }), snippet, truncated: start > 0 || snippet !== source };
}

export function inspectionQuery(value: string): string {
  const query = value.trim();
  const bytes = Buffer.byteLength(query);
  if (bytes < 1 || bytes > 256) throw new RequestError('invalid_params', 'The search query must be 1 to 256 UTF-8 bytes');
  return query.toLowerCase();
}

export async function inspectConversation(transcript: Transcript | undefined, conversationId: string,
    params: InspectionParams & { tail?: boolean }, redact: (text: string) => string, query?: string) {
  if (!transcript) {
    if (params.cursor !== undefined) return invalidCursor();
    return { items: [], nextCursor: null };
  }
  try {
    const scope = cursorScope(query === undefined ? 'conversation_read' : 'conversation_search', conversationId, transcript.source, undefined, query);
    const cursor = decodeCursor(params.cursor, scope, query === undefined ? params.tail : false);
    const tail = cursor?.tail ?? params.tail ?? false;
    const high = cursor?.high ?? transcript.high;
    if (high > transcript.high) return invalidCursor();
    const highEdge = await transcript.edge(high);
    let position = cursor?.position ?? (tail ? high : 0);
    if (cursor && (highEdge !== cursor.highEdge || await transcript.edge(position) !== cursor.positionEdge)) return invalidCursor();
    const done = (at: number) => tail ? at === 0 : at === high;
    const items: (Turn | Hit)[] = [];
    let nextCursor: string | null = null;
    let itemBytes = 0;
    let scannedBytes = 0;
    for (let scanned = 0; !done(position) && scanned < scanEntries && scannedBytes < scanBytes && items.length < (params.limit ?? 20); scanned++) {
      const step = await transcript.step(position, high, tail);
      scannedBytes += step.bytes;
      const next = done(step.position) ? null : encodeCursor(scope, { high, position: step.position, tail, highEdge, positionEdge: await transcript.edge(step.position) });
      const remaining = (params.maxBytes ?? 8192) - encodedBytes({ items: [], nextCursor: next }) - itemBytes;
      if (remaining < 0) break;
      const message = step.entry && publicConversationMessage(step.entry);
      if (message) {
        const clean = redactMessage(message, redact);
        if (!step.bytes) scannedBytes += Buffer.byteLength(clean.text) + (clean.quote ? encodedBytes(clean.quote) : 0) +
          clean.materials.reduce((size, item) => size + Buffer.byteLength(item.name) + Buffer.byteLength(item.text), 0);
        const available = remaining - Number(items.length > 0);
        const hit = query === undefined ? undefined : searchMessage(clean, query);
        if (hit && encodedBytes(hit) > available && !items.length) {
          const original = hit.snippet;
          hit.snippet = '';
          hit.truncated = true;
          hit.snippet = clipEncodedText(original, available - encodedBytes(hit));
        }
        const item = query === undefined ? fitTurn(clean, available) : hit;
        if (query === undefined || hit) {
          if (!item || encodedBytes(item) > available) {
            if (!items.length) return oversizedEntry();
            break;
          }
          itemBytes += encodedBytes(item) + Number(items.length > 0);
          items.push(item);
        }
      }
      position = step.position;
      nextCursor = next;
    }
    if (tail) items.reverse();
    return { items, nextCursor };
  } finally {
    await transcript.close();
  }
}

export type ResultSlice = { start_line?: number; end_line?: number; byte_offset?: number; cursor?: string; max_bytes?: number };
export type ResultFile = { path: string; callId: string };
export const resultPreviewBytes = 4096;
const overflowTTL = 60 * 60 * 1000;
const overflowFile = /^[A-Za-z0-9_-]+-[a-f0-9]{8}\.txt(?:\.[a-f0-9-]{36}\.tmp)?$/;

export function resultLineCount(text: string): number {
  if (!text) return 0;
  let lines = 1;
  for (let index = text.indexOf('\n'); index !== -1; index = text.indexOf('\n', index + 1)) lines++;
  return lines;
}

export class ActivityOverflow {
  private readonly directory: string;
  private readonly timer: NodeJS.Timeout;

  constructor(stateDir: string) {
    this.directory = join(stateDir, 'overflow');
    this.reap();
    this.timer = setInterval(() => this.reap(), overflowTTL);
    this.timer.unref();
  }

  write(executionId: string, redacted: string): string {
    const digest = createHash('sha256').update(redacted).digest('hex').slice(0, 8);
    const filename = /^[A-Za-z0-9_-]{1,192}$/.test(executionId) ? executionId : createHash('sha256').update(executionId).digest('hex');
    const path = join(this.directory, `${filename}-${digest}.txt`);
    const temp = `${path}.${randomUUID()}.tmp`;
    mkdirSync(this.directory, { recursive: true, mode: 0o700 });
    const directory = lstatSync(this.directory);
    if (!directory.isDirectory()) throw new RequestError('result_unavailable', 'The overflow directory is unavailable');
    if ((directory.mode & 0o777) !== 0o700) chmodSync(this.directory, 0o700);
    try {
      writeFileSync(temp, redacted, { flag: 'wx', mode: 0o600, flush: true });
      renameSync(temp, path);
      return path;
    } finally { rmSync(temp, { force: true }); }
  }

  private reap(all = false): void {
    try {
      if (!lstatSync(this.directory, { throwIfNoEntry: false })?.isDirectory()) return;
      const cutoff = Date.now() - overflowTTL;
      for (const entry of readdirSync(this.directory, { withFileTypes: true })) {
        if (!entry.isFile() || !overflowFile.test(entry.name)) continue;
        const path = join(this.directory, entry.name);
        const status = lstatSync(path, { throwIfNoEntry: false });
        if (status?.isFile() && (all || status.mtimeMs <= cutoff)) rmSync(path, { force: true });
      }
      if (all) rmdirSync(this.directory);
    } catch (error) {
      if (!['ENOENT', 'ENOTEMPTY'].includes((error as NodeJS.ErrnoException).code ?? '')) {
        process.stderr.write('Unable to reap Activity result overflow files.\n');
      }
    }
  }

  close(): void {
    clearInterval(this.timer);
    this.reap(true);
  }
}

type ActivityResultSource = { activityId: string; executionId: string; text: string; files: ResultFile[] };

function lineBytes(content: Buffer, startLine: number, endLine: number | undefined): [number, number] {
  let line = 1;
  let start = 0;
  while (line < startLine && start < content.length) {
    const newline = content.indexOf(10, start);
    start = newline < 0 ? content.length : newline + 1;
    line++;
  }
  let end = start;
  if (endLine === undefined) return [start, content.length];
  while (line <= endLine && end < content.length) {
    const newline = content.indexOf(10, end);
    end = newline < 0 ? content.length : newline + 1;
    line++;
  }
  return [start, end];
}

export function sliceActivityResult({ activityId, executionId, text, files }: ActivityResultSource, generation: string, params: ResultSlice) {
  const { start_line: startLine, end_line: endLine, byte_offset: byteOffset, max_bytes: budget = 8192 } = params;
  const lineMode = startLine !== undefined || endLine !== undefined;
  if (lineMode && (byteOffset !== undefined || params.cursor !== undefined) || params.cursor !== undefined && byteOffset !== undefined ||
      endLine !== undefined && endLine < (startLine ?? 1)) {
    throw new RequestError('invalid_params', 'Use one line range, byte offset, or cursor');
  }
  const content = Buffer.from(text, 'utf8');
  const digest = createHash('sha256').update(content).update('\0').update(JSON.stringify(files)).digest('hex');
  const scope = cursorScope('activity_result_read', activityId, generation, executionId, digest);
  const cursor = decodeCursor(params.cursor, scope, false);
  let [position, end] = lineMode ? lineBytes(content, startLine ?? 1, endLine) : [byteOffset ?? 0, content.length];
  if (cursor) {
    end = Number(cursor.highEdge);
    if (cursor.positionEdge || !Number.isSafeInteger(end) || end < 0 || end > content.length || cursor.high !== end + files.length) return invalidCursor();
    position = cursor.position;
  }
  const boundary = (offset: number) => offset === content.length || (content[offset] & 0xc0) !== 0x80;
  if (!boundary(end) || position < end && !boundary(position) || !cursor && (position > end || !boundary(position))) {
    if (cursor) return invalidCursor();
    throw new RequestError('invalid_params', 'The byte offset must be a UTF-8 code point boundary within the result');
  }
  const high = end + files.length;
  const next = (at: number) => at < high && at > 0 ? encodeCursor(scope, { high, position: at, tail: false, highEdge: String(end) }) : null;
  const response = { activityId, executionId, text: '', byteOffset: Math.min(position, end), nextByteOffset: Math.min(position, end),
    totalLines: resultLineCount(text), totalBytes: content.length, totalFiles: files.length, files: [] as ResultFile[],
    truncated: true, nextCursor: null as string | null };
  const sizeAt = (at: number) => encodedBytes({ ...response, nextByteOffset: Math.min(at, end),
    truncated: at < high, nextCursor: next(at) });
  const start = position;
  if (position < end) {
    const available = budget - Math.max(sizeAt(position), sizeAt(Math.max(position, end - 1)), sizeAt(end));
    let remaining = available;
    let limit = Math.min(end, position + budget + 4);
    while (!boundary(limit)) limit--;
    const fragment = content.subarray(position, limit).toString('utf8');
    let chars = 0;
    for (const point of fragment) {
      const cost = encodedBytes(point) - 2;
      const after = position + Buffer.byteLength(point);
      if (cost > remaining && (chars || sizeAt(after) + cost > budget)) break;
      remaining -= cost;
      chars += point.length;
      position = after;
    }
    response.text = fragment.slice(0, chars);
  }
  for (; position >= end && position < high; position++) {
    response.files.push(files[position - end]);
    if (sizeAt(position + 1) > budget) {
      response.files.pop();
      break;
    }
  }
  response.nextByteOffset = Math.min(position, end);
  response.nextCursor = next(position);
  response.truncated = position < high;
  if (encodedBytes(response) > budget || start === position && position < high) return oversizedEntry();
  return response;
}
