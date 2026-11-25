import { Type, type Static } from 'typebox';

export const maxLastingRules = 40;
export const maxLastingRulesBytes = 4096;
export const maxRuleBytes = 300;
export const maxLogBytes = 1024;
export const maxWorkLogs = 500;
export const maxPendingLearnings = 50;
export const memoryColdTtlMs = Number(process.env.NAGI_MEMORY_COLD_TTL_MS) || 60 * 60 * 1000;

const id = Type.String({ minLength: 1 });
const strict = { additionalProperties: false };
const timestamp = Type.String({ pattern: '^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}\\.\\d{3}Z$' });
const revision = Type.Integer({ minimum: 0, maximum: Number.MAX_SAFE_INTEGER });
export const memoryDate = Type.String({ pattern: '^\\d{4}-\\d{2}-\\d{2}$' });
export const memoryText = Type.String({ maxLength: maxLogBytes });
export const memoryTags = Type.Array(Type.String({ maxLength: 48 }), { maxItems: 12 });
export const memorySearchLimit = Type.Integer({ minimum: 1, maximum: maxWorkLogs });
export const memorySearchQuery = Type.String({ maxLength: 256 });
export const memoryRuleSchema = Type.Object({ ruleId: id, botId: id, text: memoryText,
  source: Type.Union([Type.Literal('explicit'), Type.Literal('approved'), Type.Literal('manual')]), createdAt: timestamp, updatedAt: timestamp }, strict);
export const workLogSchema = Type.Object({ logId: id, botId: id, date: memoryDate, summary: memoryText,
  tags: memoryTags, executionId: Type.Optional(id), createdAt: timestamp }, strict);
export const pendingLearningSchema = Type.Object({ learningId: id, botId: id,
  kind: Type.Union([Type.Literal('rule'), Type.Literal('work_log')]), text: memoryText, reason: memoryText, executionId: Type.Optional(id),
  state: Type.Union([Type.Literal('pending'), Type.Literal('approved'), Type.Literal('rejected')]), createdAt: timestamp,
  reviewedAt: Type.Optional(timestamp) }, strict);
export const memoryMetaSchema = Type.Object({ botId: id, rulesRevision: revision, lastTurnAt: Type.Optional(timestamp),
  lastConsolidationAt: Type.Optional(timestamp), forceReload: Type.Boolean() }, strict);
export const memorySnapshotSchema = Type.Object({ rules: Type.Array(memoryRuleSchema), workLogs: Type.Array(workLogSchema),
  pendingLearnings: Type.Array(pendingLearningSchema), meta: Type.Array(memoryMetaSchema) }, strict);
export const memoryChangedSchema = Type.Object({ botId: id, rulesRevision: revision, pendingCount: revision }, strict);

export type BotMemoryRule = Static<typeof memoryRuleSchema>;
export type BotWorkLog = Static<typeof workLogSchema>;
export type BotPendingLearning = Static<typeof pendingLearningSchema>;
export type BotMemoryMeta = Static<typeof memoryMetaSchema>;
export type BotMemory = Static<typeof memorySnapshotSchema>;

export function oldestRuleFirst(a: BotMemoryRule, b: BotMemoryRule): number {
  return a.createdAt.localeCompare(b.createdAt) || a.ruleId.localeCompare(b.ruleId);
}

export function newestLogFirst(a: BotWorkLog, b: BotWorkLog): number {
  return b.date.localeCompare(a.date) || b.createdAt.localeCompare(a.createdAt) || b.logId.localeCompare(a.logId);
}

export function formatLastingRulesBlock(rules: readonly BotMemoryRule[]): string {
  return [...rules].sort(oldestRuleFirst).map(rule => `- ${rule.text}`).join('\n');
}

export function lastingRulesFit(rules: readonly BotMemoryRule[]): boolean {
  return rules.length <= maxLastingRules && Buffer.byteLength(formatLastingRulesBlock(rules)) <= maxLastingRulesBytes;
}

export function normalizeMemoryText(value: string): string {
  return value.normalize('NFKC').toLowerCase().replace(/[\p{P}\p{S}\s]+/gu, ' ').trim();
}

const credentialsPattern = /(?:\b(?:Bearer|Basic)\s+[a-z0-9+/_=.~-]+|\b(?:sk-|AIza|gh[po]_|github_pat_|xox[bp]-|AKIA)[a-z0-9_-]+|-----BEGIN [^\r\n]*PRIVATE KEY-----|\b(?:api[_-]?key|password|secret|access[_-]?token)\s*[:=]\s*\S)/i;
const temporaryPattern = /\b(?:currently running|right now|temporarily|for this turn only|in progress|just for now)\b|正在执行|临时|这次先|当前步骤/i;

export function validateMemoryCandidate(input: string, options: {
  credentials?: readonly string[]; instructions?: string; duplicates?: Iterable<string>; metadata?: readonly string[]; maxBytes: number;
}): { text: string } | { code: string; message: string } {
  const text = input.trim().replace(/\s+/gu, ' ');
  const normalized = normalizeMemoryText(text);
  if (!/[\p{L}\p{N}].*[\p{L}\p{N}]/u.test(normalized) || Buffer.byteLength(text) > options.maxBytes || /\p{Cc}/u.test(text)) {
    return { code: 'memory_invalid', message: 'Write a short, meaningful memory entry' };
  }
  for (const value of [input, ...(options.metadata ?? [])]) {
    const canonical = value.normalize('NFKC');
    if (credentialsPattern.test(canonical) || options.credentials?.some(key => key && (value.includes(key) || canonical.includes(key)))) {
      return { code: 'memory_secret', message: 'Memory cannot contain credentials or secrets' };
    }
    if (temporaryPattern.test(canonical)) return { code: 'memory_temporary', message: 'Memory is for lasting rules and completed work' };
  }
  const instructions = ` ${normalizeMemoryText(options.instructions ?? '')} `;
  const documented = /\p{Script=Han}/u.test(normalized) ? instructions.includes(normalized) : instructions.includes(` ${normalized} `);
  if (documented || (options.duplicates && Array.from(options.duplicates).some(value => normalizeMemoryText(value) === normalized))) {
    return { code: 'memory_duplicate', message: 'This memory is already recorded' };
  }
  return { text };
}

export function validMemoryDate(date: string): boolean {
  const parsed = Date.parse(`${date}T00:00:00.000Z`);
  return Number.isFinite(parsed) && new Date(parsed).toISOString().slice(0, 10) === date;
}

export function recurringPreference(text: string): string | undefined {
  const match = /(?:^|[.!?。！？\n]\s*)(from now on[ ,:]+[^\n.!?。！？]+|always remember to\s+[^\n.!?。！？]+|以后(?:每次都|不要|都)[^\n。！？]+)/iu.exec(text.trim());
  return match?.[1].trim();
}

export function completedWorkSummary(goal: string, result: string): string {
  const text = [goal.trim(), result.trim()].filter(Boolean).join(' — ');
  return Buffer.from(text).subarray(0, 360).toString().replace(/\uFFFD$/u, '').trim();
}
