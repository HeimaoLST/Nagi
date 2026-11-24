import { Type, type Static } from 'typebox';
import { Value } from 'typebox/value';
import type { ProductProfile } from './product.js';
import { id, RequestError, traceItemSchema, type TraceItem } from './protocol.js';
import { clipEncodedText, clipUTF8 } from './product-redactor.js';

export const maxExecutionTraces = 512;
export const maxProfileTraces = 16384;
const maxDetailBytes = 1024;
const maxExcerptBytes = 384;
const maxPathBytes = 256;
const owner = { activityId: id, executionId: id };
const traceCoverageSchema = Type.Object({ ...owner,
  coverage: Type.Union([Type.Literal('recorded'), Type.Literal('unrecorded')]),
  evictedThroughSeq: Type.Integer({ minimum: 0, maximum: Number.MAX_SAFE_INTEGER }),
}, { additionalProperties: false });
export const traceBackupSchema = Type.Object({ version: Type.Literal(1),
  traces: Type.Array(traceItemSchema, { maxItems: maxProfileTraces }), traceCoverage: Type.Array(traceCoverageSchema),
}, { additionalProperties: false });

export type TraceBackup = Static<typeof traceBackupSchema>;
export type TraceCoverage = Static<typeof traceCoverageSchema>;
export type TraceUpdate = { activityId: string; executionId: string; lastSeq: number };
type TracePayload<T = TraceItem> = T extends TraceItem ? Omit<T, 'seq' | 'at' | 'activityId' | 'executionId'> : never;
export type TraceObservation = TracePayload & { activityId: string; executionId: string };

export function directlyOwns(activity: ProductProfile['activities'][number], botId: string, originBotId: string | undefined): boolean {
  return activity.ownerBotId === botId || originBotId === botId;
}

export function normalizeActivityGrants(ids: string[], code: string): string[] {
  if (Buffer.byteLength(JSON.stringify(ids)) > 8192) throw new RequestError(code, 'Linked Activity IDs exceed the raw byte limit');
  const normalized = [...new Set(ids)].sort();
  if (normalized.length > 32) throw new RequestError(code, 'Too many distinct linked Activity IDs');
  return normalized;
}

export function traceDetail(row: TraceItem | TraceObservation): string {
  const { activityId, executionId, kind, ...fields } = row;
  if ('seq' in fields) delete (fields as Partial<TraceItem>).seq;
  if ('at' in fields) delete (fields as Partial<TraceItem>).at;
  return JSON.stringify(fields);
}

export function redactTrace<T extends TraceItem | TraceObservation>(row: T, redact: (text: string) => string): T {
  if (row.kind === 'state_changed') return row;
  if (redact(row.callId) !== row.callId) throw new RequestError('entry_too_large', 'A trace identifier cannot be exposed');
  const display = (text: string, bytes: number, base: TraceItem | TraceObservation) =>
    clipEncodedText(clipUTF8(redact(text), bytes), maxDetailBytes - Buffer.byteLength(traceDetail(base)));
  if (row.kind === 'file_confirmed') {
    const base = { ...row, path: '' };
    return { ...base, path: display(row.path, maxPathBytes, base) };
  }
  const tool = clipUTF8(redact(row.tool), maxPathBytes);
  if (row.kind === 'tool_started') {
    const target = row.target;
    return { ...row, tool, ...(target === undefined ? {} : { target: display(target, maxPathBytes, { ...row, tool, target: '' }) }) };
  }
  const excerpt = row.excerpt;
  return { ...row, tool, ...(excerpt === undefined ? {} : { excerpt: display(excerpt, maxExcerptBytes, { ...row, tool, excerpt: '' }) }) };
}

export function decodeTraceBackup(profile: ProductProfile, data: unknown): TraceBackup {
  const invalid = (): never => { throw new RequestError('profile_unavailable', 'The recovery trace envelope has invalid records or associations'); };
  if (!Value.Check(traceBackupSchema, data)) return invalid();
  const owners = new Map<string, string>();
  for (const { activityId, executionIds } of profile.activities) {
    for (const executionId of executionIds) owners.set(executionId, activityId);
  }
  const meta = new Map<string, TraceCoverage>();
  const evictions = new Set<number>();
  for (const row of data.traceCoverage) {
    if (owners.get(row.executionId) !== row.activityId || meta.has(row.executionId) ||
        (row.evictedThroughSeq !== 0 && (row.coverage !== 'recorded' || evictions.has(row.evictedThroughSeq)))) invalid();
    meta.set(row.executionId, row);
    if (row.evictedThroughSeq !== 0) evictions.add(row.evictedThroughSeq);
  }
  let previous = 0;
  const counts = new Map<string, number>();
  for (const row of data.traces) {
    const coverage = meta.get(row.executionId);
    const count = (counts.get(row.executionId) ?? 0) + 1;
    if (!coverage || coverage.activityId !== row.activityId || coverage.coverage !== 'recorded' ||
        row.seq <= Math.max(previous, coverage.evictedThroughSeq) || evictions.has(row.seq) || count > maxExecutionTraces ||
        !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/.test(row.at) || !Number.isFinite(Date.parse(row.at)) ||
        Buffer.byteLength(traceDetail(row)) > maxDetailBytes ||
        ('target' in row && Buffer.byteLength(row.target ?? '') > maxPathBytes) ||
        ('path' in row && Buffer.byteLength(row.path) > maxPathBytes) ||
        ('excerpt' in row && Buffer.byteLength(row.excerpt ?? '') > maxExcerptBytes)) invalid();
    previous = row.seq;
    counts.set(row.executionId, count);
  }
  return data;
}
