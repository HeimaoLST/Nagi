import { createHash, createHmac } from 'node:crypto';
import type { SessionEntry } from '@earendil-works/pi-coding-agent';
import { RequestError, type Material } from './protocol.js';
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

type Message = { messageId: string; role: 'user' | 'assistant'; timestamp: string; text: string; materials: Material[] };

export function publicConversationMessage(entry: SessionEntry): Message | undefined {
  const base = { messageId: entry.id, timestamp: entry.timestamp };
  if (entry.type === 'custom' && entry.customType === turnEntryType) {
    const turn = entry.data as { text: string; materials?: Material[]; context?: { name: string; text: string }[] };
    const materials = turn.materials ?? (turn.context ?? []).filter(item => !item.name.startsWith('nagi.issue_tools.')).map((item, index) => ({ id: `${entry.id}:${index}`, revision: 0, name: item.name, text: item.text }));
    return { ...base, role: 'user' as const, text: turn.text, materials };
  }
  if (entry.type === 'message' && entry.message.role === 'assistant') {
    const text = entry.message.content.filter(part => part.type === 'text').map(part => part.text).join('');
    if (text) return { ...base, role: 'assistant' as const, text, materials: [] };
  }
}

type Turn = Omit<Message, 'materials'> & { materials: (Omit<Material, 'source'> & { truncated: boolean })[]; truncated: boolean };
type Hit = Pick<Message, 'messageId' | 'role' | 'timestamp'> & { field: 'text' | 'material'; materialId?: string; snippet: string; truncated: boolean };

function redactMessage(message: Message, redact: (text: string) => string): Message {
  if (redact(message.messageId) !== message.messageId || message.materials.some(item => redact(item.id) !== item.id)) return oversizedEntry();
  return { ...message, text: redact(message.text), materials: message.materials.map(({ id, revision, name, text }) => ({ id, revision, name: redact(name), text: redact(text) })) };
}


function fitTurn(message: Message, budget: number): Turn | undefined {
  const { text, materials, ...identity } = message;
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
  turn.truncated = turn.text !== text || turn.materials.length !== materials.length || turn.materials.some(material => material.truncated);
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
        if (!step.bytes) scannedBytes += Buffer.byteLength(clean.text) + clean.materials.reduce((size, item) => size + Buffer.byteLength(item.name) + Buffer.byteLength(item.text), 0);
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
