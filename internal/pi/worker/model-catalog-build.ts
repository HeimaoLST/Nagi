import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { getBuiltinModelDataGeneratedAt, getBuiltinModels, getBuiltinProviders } from '@earendil-works/pi-ai/providers/all';
import { getSupportedThinkingLevels } from '@earendil-works/pi-ai/models';

const hash = (bytes: string | Buffer) => createHash('sha256').update(bytes).digest('hex');
const compare = (a: string, b: string) => a < b ? -1 : a > b ? 1 : 0;
const here = (name: string) => new URL(name, import.meta.url);
const rankBytes = readFileSync(here('./model-ranks.json'));
const ranks = JSON.parse(rankBytes.toString('utf8'));
const providers = [...getBuiltinProviders()].sort(compare);
const supplementBytes = readFileSync(here('./model-catalog-supplement.json'));
const { rows: supplement, ...modelsDev } = JSON.parse(supplementBytes.toString('utf8'));
const sources = new Set<string>([...providers, ...supplement.map((row: { source: string }) => row.source)]);
const aliases = new Set<string>();
for (const [source, id, canonical] of ranks.aliases as string[][]) {
  const key = JSON.stringify([source, id]);
  if (!sources.has(source) || !id || !canonical?.includes('/') || aliases.has(key)) throw new Error(`Invalid or overlapping exact alias ${key}`);
  aliases.add(key);
}
for (const { pattern, rank, speed, costTier } of ranks.families) {
  new RegExp(pattern, 'i');
  const scores = [rank.dialogue, rank.coding, rank.reasoning, rank.utility, speed];
  if (!scores.every(value => Number.isInteger(value) && value >= 0 && value <= 100) ||
      !['free', 'low', 'mid', 'high', 'flagship_pro'].includes(costTier)) throw new Error(`Invalid model rank ${pattern}`);
}
const allowedCompat = ['supportsStore', 'supportsDeveloperRole', 'supportsReasoningEffort', 'supportsUsageInStreaming',
  'maxTokensField', 'thinkingFormat', 'requiresReasoningContentOnAssistantMessages', 'supportsStrictMode', 'supportsMidConvoSystemMessages',
  'supportsReasoningSummary', 'supportsAdditionalTools', 'supportsMidConvoEffort', 'promptCacheKeyMode'] as const;
const models = providers.flatMap(source => [...getBuiltinModels(source)].sort((a, b) => compare(a.id, b.id)).map(model => {
  const { api, id, baseUrl, reasoning, input, contextWindow, maxTokens, thinkingLevelMap, cost, compat } = model;
  return { source, api, id, baseUrl, reasoning, input, contextWindow, maxTokens,
    supportedThinkingLevels: getSupportedThinkingLevels(model), thinkingLevelMap,
    cost: { input: cost.input, output: cost.output, cacheRead: cost.cacheRead, cacheWrite: cost.cacheWrite },
    compat: compat && Object.fromEntries(allowedCompat.filter(key => Object.hasOwn(compat, key))
      .map(key => [key, (compat as Record<string, unknown>)[key]])) };
}));
const body = { schema: 1, piGeneratedAt: getBuiltinModelDataGeneratedAt(), piLockSha256: hash(readFileSync(here('../package-lock.json'))),
  modelsDev: { ...modelsDev, snapshotSha256: hash(supplementBytes) }, rankRevision: ranks.version, ranksSha256: hash(rankBytes), models, supplement };
const digest = hash(JSON.stringify(body));
writeFileSync(here('./model-catalog.json'), JSON.stringify({ ...body, digest }) + '\n');
