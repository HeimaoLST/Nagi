import { readFileSync } from 'node:fs';
import type { Model } from '@earendil-works/pi-ai';
import type { ModelRole, ProviderInput, ProviderTestInput, ThinkingLevel } from './model-router.js';

export type CostTier = 'free' | 'low' | 'mid' | 'high' | 'flagship_pro' | 'unknown';
export type DiscoveredModelLimits = { contextTokens?: number; outputTokens?: number };
type Capabilities = { reasoning: boolean; thinkingLevelMap: NonNullable<Model<'openai-completions'>['thinkingLevelMap']>;
  compat?: NonNullable<Model<'openai-completions'>['compat']> | NonNullable<Model<'openai-responses'>['compat']>;
  cost?: Model<'openai-completions'>['cost']; input?: Model<'openai-completions'>['input'] };
type Builtin = { source: string; api: string; id: string; baseUrl: string; reasoning: boolean; input: NonNullable<Capabilities['input']>;
  contextWindow: number; maxTokens: number; supportedThinkingLevels: string[];
  thinkingLevelMap?: Capabilities['thinkingLevelMap']; compat?: Capabilities['compat']; cost: NonNullable<Capabilities['cost']> };
type Supplement = { source: string; id: string; api: string; chat: boolean; tools?: boolean; reasoning?: boolean; midHistorySystem?: boolean;
  requiresBuiltin?: boolean; compat?: Capabilities['compat'];
  thinkingLevels?: string[]; context?: number; output?: number; inputPrice?: number; outputPrice?: number };
type Family = { pattern: string; rank: Record<ModelRole, number>; speed: number; costTier: CostTier; recommended?: boolean; priceProtected?: boolean };

const load = (name: string): unknown => JSON.parse(readFileSync(new URL(name, import.meta.url), 'utf8'));
const catalog = load('./model-catalog.json') as { models: Builtin[]; supplement: Supplement[] };
const ranks = load('./model-ranks.json') as { aliases: [string, string, string][]; families: Family[] };
const key = (...parts: string[]) => JSON.stringify(parts);
const aliases = new Map(ranks.aliases.map(([source, wire, canonical]) => [key(source, wire), canonical]));
const supplement = new Map(catalog.supplement.map(row => [key(row.source, row.api, row.id), row]));
const families = ranks.families.map(row => ({ ...row, matches: new RegExp(row.pattern, 'i') }));
const routeModels = new Map<string, Builtin>();
const endpointModels = new Map<string, Builtin>();
const endpointSources = new Map<string, string>([
  ['https://api.minimax.io/v1', 'minimax'],
  ['https://api.minimaxi.com/v1', 'minimax'],
  ['https://api.minimax.chat/v1', 'minimax'],
  ['https://generativelanguage.googleapis.com/v1beta/openai', 'google'],
  ['https://api.mistral.ai/v1', 'mistral'],
  ['https://open.bigmodel.cn/api/paas/v4', 'zhipuai'],
  ['https://api.z.ai/api/paas/v4', 'zai'],
  ['https://dashscope.aliyuncs.com/compatible-mode/v1', 'alibaba-cn'],
  ['https://dashscope-intl.aliyuncs.com/compatible-mode/v1', 'alibaba'],
  ['https://api.githubcopilot.com', 'github-copilot'],
]);
const ambiguousEndpoints = new Set<string>();
for (const row of catalog.models) {
  const endpoint = row.baseUrl.replace(/\/+$/, '');
  if (!row.api.startsWith('openai-') || !endpoint.startsWith('https://') || endpoint.includes('{')) continue;
  const previous = endpointSources.get(endpoint);
  if (previous !== undefined && previous !== row.source) ambiguousEndpoints.add(endpoint);
  endpointSources.set(endpoint, row.source);
  routeModels.set(key(endpoint, row.api, row.id), row);
  endpointModels.set(key(endpoint, row.id), row);
}
const officialSources: Record<string, true> = { openai: true, deepseek: true, anthropic: true, google: true, xai: true, mistral: true,
  'moonshotai-cn': true, moonshotai: true, zhipuai: true, zai: true, 'alibaba-cn': true, alibaba: true, minimax: true, xiaomi: true, meta: true };
const supplementLimits = new Map<string, Supplement>();
for (const row of catalog.supplement) {
  if (!row.chat || row.requiresBuiltin || (row.context === undefined && row.output === undefined)) continue;
  const id = row.id.toLowerCase();
  const bareId = id.slice(id.lastIndexOf('/') + 1);
  const bareKey = `${row.api}\0${bareId}`;
  const previous = supplementLimits.get(bareKey);
  if (!previous || (Object.hasOwn(officialSources, row.source) && !Object.hasOwn(officialSources, previous.source))) supplementLimits.set(bareKey, row);
  if (id !== bareId) {
    const exactKey = `${row.api}\0${id}`;
    if (!supplementLimits.has(exactKey)) supplementLimits.set(exactKey, row);
  }
}
const discoveredLimits = new Map<string, DiscoveredModelLimits>();
const maxDiscoveredLimits = 4096;
function normalizedEndpoint(baseUrl: string): string | undefined {
  const url = URL.parse(baseUrl);
  if (url) return url.href.replace(/\/+$/, '');
}
const levels = ['off', 'low', 'medium', 'high'] as const;
export const compareModelIds = (a: string, b: string) => a < b ? -1 : a > b ? 1 : 0;
const nonChat = /(?:^|[-_])(?:embeddings?|embed|rerank|moderation|whisper|transcrib\w*|transcriptions?|tts|dall-e|image-gen|gpt-image|realtime)(?=$|[-_:0-9.])/i;

export function lowerThinking(supported: readonly ThinkingLevel[], requested: ThinkingLevel): ThinkingLevel | undefined {
  return levels.findLast((level, index) => index <= levels.indexOf(requested) && supported.includes(level));
}

function channelCost(price: Builtin['cost'] | undefined, wire: string, source?: string): CostTier {
  if (source === 'openrouter' && wire.endsWith(':free') && price?.input === 0 && price.output === 0) return 'free';
  if (price && (price.input > 0 || price.output > 0)) {
    const blended = price.input * 0.8 + price.output * 0.2;
    return blended <= 1 ? 'low' : blended <= 4 ? 'mid' : blended <= 20 ? 'high' : 'flagship_pro';
  }
  return 'unknown';
}

export function positiveInt(value: unknown, min = 1): number | undefined {
  const number = typeof value === 'string' && /^\d+$/.test(value) ? Number(value) : value;
  if (typeof number === 'number' && Number.isSafeInteger(number) && number >= min) return number;
}

function validDiscoveredOutput(outputTokens: number | undefined, contextTokens: number | undefined): number | undefined {
  if (outputTokens !== undefined && outputTokens >= 1 && (contextTokens === undefined || outputTokens <= contextTokens - 4)) return outputTokens;
}

export function recordDiscoveredLimits(baseUrl: string, wireApi: string, limitsByModel: ReadonlyMap<string, DiscoveredModelLimits>): void {
  const endpoint = normalizedEndpoint(baseUrl);
  if (endpoint === undefined) return;
  const prefix = `${wireApi}|${endpoint}\0`;
  for (const cacheKey of discoveredLimits.keys()) if (cacheKey.startsWith(prefix)) discoveredLimits.delete(cacheKey);
  for (const [id, limits] of limitsByModel) {
    const contextTokens = positiveInt(limits.contextTokens, 16);
    const outputTokens = validDiscoveredOutput(positiveInt(limits.outputTokens), contextTokens);
    if (contextTokens !== undefined || outputTokens !== undefined) discoveredLimits.set(`${prefix}${id}`, { contextTokens, outputTokens });
  }
  for (const cacheKey of discoveredLimits.keys()) {
    if (discoveredLimits.size <= maxDiscoveredLimits) break;
    discoveredLimits.delete(cacheKey);
  }
}

export function clearDiscoveredLimits(): void {
  discoveredLimits.clear();
}

export function modelSupply(account: ProviderInput | ProviderTestInput, id: string, legacyDefault = false, discovered?: DiscoveredModelLimits) {
  const { wireApi } = account;
  const normalized = normalizedEndpoint(account.baseUrl);
  const endpoint = normalized === 'https://api.deepseek.com/v1' ? 'https://api.deepseek.com' : normalized ?? '';
  const source = ambiguousEndpoints.has(endpoint) ? undefined : endpointSources.get(endpoint);
  const exact = source ? routeModels.get(key(endpoint, account.wireApi, id)) : undefined;
  const supplementalRoute = source ? supplement.get(key(source, account.wireApi, id)) : undefined;
  const evidence = supplementalRoute?.requiresBuiltin && !exact ? undefined : supplementalRoute;
  const knownIdentity = !!source && (endpointModels.has(key(endpoint, id)) || !!evidence);
  const canonicalId = source && knownIdentity ? aliases.get(key(source, id)) ?? `${source}/${id}` :
    `custom:${Buffer.byteLength(account.providerId)}:${account.providerId}:${id}`;
  const modelName = id.slice(id.lastIndexOf('/') + 1);
  const rank = families.find(row => row.matches.test(modelName));
  const family = knownIdentity ? rank : undefined;
  const override = account.modelOverrides?.find(row => row.id === id);
  const isChat = nonChat.test(modelName) || evidence?.chat === false || exact && !exact.input.includes('text') ? false :
    override?.isChat ?? (exact || evidence?.chat || legacyDefault ? true : null);
  const toolCall = isChat !== true ? isChat : evidence?.tools === false ? false : override?.toolCall ?? evidence?.tools ?? (legacyDefault && !override ? true : null);
  const effort = exact?.thinkingLevelMap;
  const supplementsThinking = !exact && evidence?.reasoning === true && !!evidence.thinkingLevels?.length;
  const reasoning = isChat === true && evidence?.reasoning !== false && (exact?.reasoning === true || supplementsThinking);
  const supported = reasoning ? levels.filter(level => exact?.supportedThinkingLevels.includes(level) ??
    evidence?.thinkingLevels?.includes(level === 'off' ? 'none' : level)) : ['off'] as const;
  const thinkingLevelMap = Object.fromEntries(['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'].map(level => {
    const mapped = effort?.[level as keyof typeof effort];
    return [level, supported.some(item => item === level) ? mapped ?? (supplementsThinking && level === 'off' ? 'none' : undefined) : null];
  }));
  const compat = evidence?.midHistorySystem !== undefined || evidence?.compat || supplementsThinking && account.wireApi === 'openai-completions' ? {
    ...(evidence?.midHistorySystem === undefined ? {} : { supportsMidConvoSystemMessages: evidence.midHistorySystem }),
    ...evidence?.compat,
    ...(supplementsThinking && account.wireApi === 'openai-completions' ? { supportsReasoningEffort: true } : {}),
    ...exact?.compat,
  } : exact?.compat;
  const apiLimits = discovered ?? (normalized === undefined ? undefined : discoveredLimits.get(`${wireApi}|${normalized}\0${id}`));
  const bareName = modelName.toLowerCase();
  const fallbackLimits = (id === modelName ? undefined : supplementLimits.get(`${wireApi}\0${id.toLowerCase()}`)) ??
    supplementLimits.get(`${wireApi}\0${bareName}`) ??
    (wireApi === 'openai-responses' ? supplementLimits.get(`openai-completions\0${bareName}`) : undefined);
  const rawContext = override?.contextTokens ?? apiLimits?.contextTokens ?? exact?.contextWindow ?? evidence?.context ?? fallbackLimits?.context ?? Infinity;
  const contextTokens = Math.min(account.contextTokens, rawContext);
  const validApiOutput = validDiscoveredOutput(apiLimits?.outputTokens, apiLimits?.contextTokens);
  const rawOutput = override?.outputTokens ?? validApiOutput ?? exact?.maxTokens ?? evidence?.output ?? fallbackLimits?.output ?? Infinity;
  const cappedOutput = Math.min(account.outputTokens, rawOutput);
  const outputTokens = override?.outputTokens === undefined && contextTokens >= 8 && cappedOutput > contextTokens - 4 ?
    Math.max(1, Math.min(8192, Math.floor(contextTokens / 4))) : cappedOutput;
  const cost = exact?.cost ?? (evidence?.inputPrice !== undefined && evidence.outputPrice !== undefined ?
    { input: evidence.inputPrice, output: evidence.outputPrice, cacheRead: 0, cacheWrite: 0 } : undefined);
  const tier = channelCost(cost, id, source);
  const priceProtected = rank?.priceProtected === true || tier === 'flagship_pro';
  const recommended = isChat === true && toolCall === true && !!family?.recommended && !priceProtected && contextTokens - outputTokens >= 4;
  const capabilities: Capabilities = { reasoning, thinkingLevelMap, ...(exact ? { input: exact.input } : {}),
    ...(compat ? { compat } : {}), ...(cost ? { cost } : {}) };
  return { id, canonicalId, isChat, toolCall, reasoning, tier, priceProtected, recommended, contextTokens, outputTokens, family, supported, capabilities };
}

export function describeModel(account: ProviderInput | ProviderTestInput, id: string, discovered?: DiscoveredModelLimits) {
  const { family, supported, capabilities, ...view } = modelSupply(account, id, false, discovered);
  return view;
}
