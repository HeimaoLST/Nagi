import { readFileSync } from 'node:fs';
import type { Model } from '@earendil-works/pi-ai';
import type { ModelRole, ProviderInput, ThinkingLevel } from './model-router.js';

export type CostTier = 'free' | 'low' | 'mid' | 'high' | 'flagship_pro' | 'unknown';
export type Capabilities = { reasoning: boolean; thinkingLevelMap: NonNullable<Model<'openai-completions'>['thinkingLevelMap']>;
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

export function modelSupply(account: ProviderInput, id: string, legacyDefault = false) {
  const endpoint = new URL(account.baseUrl).href.replace(/\/+$/, '').replace(/^https:\/\/api\.deepseek\.com\/v1$/, 'https://api.deepseek.com');
  const source = ambiguousEndpoints.has(endpoint) ? undefined : endpointSources.get(endpoint);
  const exact = source ? routeModels.get(key(endpoint, account.wireApi, id)) : undefined;
  const supplementalRoute = source ? supplement.get(key(source, account.wireApi, id)) : undefined;
  const evidence = supplementalRoute?.requiresBuiltin && !exact ? undefined : supplementalRoute;
  const knownIdentity = !!source && (endpointModels.has(key(endpoint, id)) || !!evidence);
  const canonicalId = source && knownIdentity ? aliases.get(key(source, id)) ?? `${source}/${id}` :
    `custom:${Buffer.byteLength(account.providerId)}:${account.providerId}:${id}`;
  const modelName = id.split('/').at(-1)!;
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
  const contextTokens = Math.min(account.contextTokens, exact?.contextWindow ?? evidence?.context ?? Infinity, override?.contextTokens ?? Infinity);
  const outputTokens = Math.min(account.outputTokens, exact?.maxTokens ?? evidence?.output ?? Infinity, override?.outputTokens ?? Infinity);
  const cost = exact?.cost ?? (evidence?.inputPrice !== undefined && evidence.outputPrice !== undefined ?
    { input: evidence.inputPrice, output: evidence.outputPrice, cacheRead: 0, cacheWrite: 0 } : undefined);
  const tier = channelCost(cost, id, source);
  const priceProtected = rank?.priceProtected === true || tier === 'flagship_pro';
  const recommended = isChat === true && toolCall === true && !!family?.recommended && !priceProtected && contextTokens - outputTokens >= 4;
  const capabilities: Capabilities = { reasoning, thinkingLevelMap, ...(exact ? { input: exact.input } : {}),
    ...(compat ? { compat } : {}), ...(cost ? { cost } : {}) };
  return { id, canonicalId, isChat, toolCall, reasoning, tier, priceProtected, recommended, contextTokens, outputTokens, family, supported, capabilities };
}

export function describeModel(account: ProviderInput, id: string) {
  const { family, supported, capabilities, ...view } = modelSupply(account, id);
  return view;
}
