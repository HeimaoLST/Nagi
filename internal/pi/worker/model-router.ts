import { Type, type Static } from 'typebox';
import { compareModelIds, lowerThinking, modelSupply, type CostTier } from './model-catalog.js';

const strict = { additionalProperties: false };
const key = Type.String({ minLength: 1, maxLength: 256 });
const providerId = Type.String({ pattern: '^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$' });
const count = Type.Integer({ minimum: 1, maximum: Number.MAX_SAFE_INTEGER });
const wireApi = Type.Union([Type.Literal('openai-completions'), Type.Literal('openai-responses')]);
export const policySchema = Type.Union([Type.Literal('economy'), Type.Literal('balanced'), Type.Literal('quality')]);
export const roleSchema = Type.Union([Type.Literal('dialogue'), Type.Literal('coding'), Type.Literal('reasoning'), Type.Literal('utility')]);
export const thinkingSchema = Type.Union([Type.Literal('off'), Type.Literal('low'), Type.Literal('medium'), Type.Literal('high')]);
export const activityKind = Type.Union([Type.Literal('coding'), Type.Literal('research'), Type.Literal('writing'), Type.Literal('ops'), Type.Literal('quick')]);
export const activityComplexity = Type.Union([Type.Literal('light'), Type.Literal('standard'), Type.Literal('deep')]);
export const activityRouting = Type.Object({ kind: Type.Optional(activityKind), complexity: Type.Optional(activityComplexity),
  constraints: Type.Optional(Type.Array(Type.String({ maxLength: 1024 }), { maxItems: 32 })) }, strict);

const modelList = Type.Array(key, { maxItems: 128, uniqueItems: true });
const modelOverride = Type.Object({ id: key, isChat: Type.Optional(Type.Boolean()), toolCall: Type.Optional(Type.Boolean()),
  contextTokens: Type.Optional(count), outputTokens: Type.Optional(count) }, strict);
const providerFields = { providerId, name: key, wireApi, baseUrl: Type.String({ minLength: 1, maxLength: 2048 }),
  models: Type.Optional(modelList), defaultModel: key, contextTokens: count, outputTokens: count, enabled: Type.Boolean() };
export const privateProviderAccount = Type.Object({ ...providerFields, credentialSourceId: key }, strict);
export const providerInput = Type.Object({ ...providerFields, enabledModels: Type.Optional(modelList), fallbackOnly: Type.Optional(Type.Boolean()),
  modelOverrides: Type.Optional(Type.Array(modelOverride, { maxItems: 128 })) }, strict);
export const providerAccount = Type.Object({ ...providerInput.properties, credentialSourceId: key }, strict);
export const providerAccountView = Type.Object({ ...providerAccount.properties, priority: Type.Optional(count),
  credentialStatus: Type.Union([Type.Literal('ok'), Type.Literal('credential_missing')]) }, strict);
const costTier = Type.Union([Type.Literal('free'), Type.Literal('low'), Type.Literal('mid'), Type.Literal('high'), Type.Literal('flagship_pro'), Type.Literal('unknown')]);
const evidence = Type.Union([Type.Boolean(), Type.Null()]);
export const discoveredModel = Type.Object({ id: key, canonicalId: Type.String(), isChat: evidence, toolCall: evidence,
  reasoning: Type.Boolean(), tier: costTier, priceProtected: Type.Boolean(), recommended: Type.Boolean(), contextTokens: count, outputTokens: count }, strict);
const flag = Type.Optional(Type.Boolean());
const effort = Type.Optional(Type.Union([Type.String(), Type.Null()]));
const adapterCompat = Type.Object({ supportsStore: flag, supportsDeveloperRole: flag, supportsReasoningEffort: flag, supportsUsageInStreaming: flag,
  maxTokensField: Type.Optional(Type.Union([Type.Literal('max_tokens'), Type.Literal('max_completion_tokens')])),
  thinkingFormat: Type.Optional(Type.Union([Type.Literal('ant-ling'), Type.Literal('baseten'), Type.Literal('chat-template'), Type.Literal('deepseek'),
    Type.Literal('openai'), Type.Literal('openrouter'), Type.Literal('qwen'), Type.Literal('qwen-chat-template'), Type.Literal('string-thinking'), Type.Literal('together'), Type.Literal('zai')])),
  requiresReasoningContentOnAssistantMessages: flag, supportsStrictMode: flag, supportsMidConvoSystemMessages: flag, supportsReasoningSummary: flag,
  supportsAdditionalTools: flag, supportsMidConvoEffort: flag,
  promptCacheKeyMode: Type.Optional(Type.Union([Type.Literal('auto'), Type.Literal('enabled'), Type.Literal('disabled')])) }, strict);
export const modelCapabilities = Type.Object({ reasoning: Type.Boolean(), compat: Type.Optional(adapterCompat),
  thinkingLevelMap: Type.Object({ off: effort, minimal: effort, low: effort, medium: effort, high: effort, xhigh: effort, max: effort }, strict),
  input: Type.Optional(Type.Array(Type.Union([Type.Literal('text'), Type.Literal('image')]), { minItems: 1, maxItems: 2, uniqueItems: true })),
  cost: Type.Optional(Type.Object({ input: Type.Number({ minimum: 0 }), output: Type.Number({ minimum: 0 }),
    cacheRead: Type.Number({ minimum: 0 }), cacheWrite: Type.Number({ minimum: 0 }) }, strict)) }, strict);
export const routeSchema = Type.Object({ providerId, credentialSourceId: key, resolvedModel: key, role: roleSchema, thinkingLevel: thinkingSchema,
  canonicalModelId: Type.Optional(Type.String()), source: Type.Optional(Type.Literal('auto')), costTier: Type.Optional(costTier),
  reason: Type.Optional(Type.String()), providerName: Type.Optional(key) }, strict);
export const capturedRoute = Type.Object({ ...routeSchema.properties, effectiveRevision: count,
  wireApi, baseUrl: providerInput.properties.baseUrl, contextTokens: count, outputTokens: count, capabilities: Type.Optional(modelCapabilities) }, strict);
export const escalationSchema = Type.Object({ activityId: key, priorExecutionId: key, escalatedExecutionId: key,
  fromRole: Type.Literal('utility'), toRole: Type.Union([Type.Literal('coding'), Type.Literal('reasoning')]), at: key }, strict);

export type ProviderInput = Static<typeof providerInput>;
export type ProviderAccount = Static<typeof providerAccount>;
export type RoutingPolicy = Static<typeof policySchema>;
export type ModelRole = Static<typeof roleSchema>;
export type ThinkingLevel = Static<typeof thinkingSchema>;
export type CapturedRoute = Static<typeof capturedRoute>;
export type ActivityRouting = Static<typeof activityRouting>;
export type Escalation = Static<typeof escalationSchema>;

const roles = ['dialogue', 'coding', 'reasoning', 'utility'] as const;
const costScores: Record<CostTier, number> = { free: 100, low: 85, mid: 60, high: 30, flagship_pro: 5, unknown: 55 };
const policies = { quality: { margin: 10, weights: [65, 10, 5, 20] }, balanced: { margin: 20, weights: [45, 15, 15, 25] },
  economy: { margin: 40, weights: [25, 20, 35, 20] } };
export const providerCacheTtlMs = 5 * 60 * 1000;

export function providerEndpoint(baseUrl: string): URL | undefined {
  const url = URL.parse(baseUrl);
  if (url && (url.protocol === 'http:' || url.protocol === 'https:') && !url.username && !url.password && !url.search && !url.hash) return url;
}

export function validProvider(account: ProviderInput): boolean {
  const models = new Set([account.defaultModel, ...account.models ?? [], ...account.enabledModels ?? []]);
  const overrides = account.modelOverrides ?? [];
  return !!providerEndpoint(account.baseUrl) && !!account.name.trim() && [...models].every(model => !!model.trim()) &&
    account.contextTokens - account.outputTokens >= 4 && new Set(overrides.map(row => row.id)).size === overrides.length &&
    overrides.every(row => !!row.id.trim() && !(row.isChat === false && row.toolCall === true) &&
      Math.min(row.contextTokens ?? Infinity, account.contextTokens) - Math.min(row.outputTokens ?? Infinity, account.outputTokens) >= 4);
}

export function activityRole(routing: ActivityRouting | undefined, hasMcp: boolean): ModelRole {
  if (routing?.kind === 'quick' || routing?.complexity === 'light') return 'utility';
  if (routing?.kind === 'coding') return 'coding';
  if (routing?.kind === 'research' || routing?.complexity === 'deep' || hasMcp || routing?.constraints?.length) return 'reasoning';
  return 'utility';
}

export function resolveRole(accounts: readonly ProviderAccount[], effectiveRevision: number, policy: RoutingPolicy,
  role: ModelRole, available: (account: ProviderAccount) => boolean, deep = false, requiresTools = true): CapturedRoute | null {
  const requested = role === 'utility' ? 'off' : role === 'dialogue' ? (policy === 'economy' ? 'off' : 'low') : deep ? 'high' : 'medium';
  const { margin, weights: [capWeight, speedWeight, costWeight, priorityWeight] } = policies[policy];
  const routes = accounts.flatMap((account, priority) => {
    if (!account.enabled || !available(account)) return [];
    const ids = new Set(account.enabledModels ?? [account.defaultModel, ...account.models ?? []]);
    const priorityScore = Math.round(100 * (accounts.length - priority) / accounts.length);
    return [...ids].flatMap(id => {
      const isDefault = id === account.defaultModel;
      const supply = modelSupply(account, id, isDefault && account.enabledModels === undefined);
      const thinkingLevel = lowerThinking(supply.supported, requested);
      if (supply.isChat !== true || requiresTools && supply.toolCall !== true || supply.contextTokens - supply.outputTokens < 4 || thinkingLevel === undefined ||
          account.enabledModels === undefined && (supply.priceProtected || !isDefault && !supply.recommended)) return [];
      const cap = supply.family?.rank[role] ?? 60;
      const score = capWeight * cap + speedWeight * (supply.family?.speed ?? 65) +
        costWeight * costScores[supply.tier] + priorityWeight * priorityScore;
      return [{ account, priority, supply, thinkingLevel, cap, score }];
    });
  });
  const ordinary = routes.filter(row => !row.account.fallbackOnly);
  const pool = ordinary.length ? ordinary : routes;
  if (!pool.length) return null;
  const preferred = new Map<string, typeof pool[number]>();
  for (const row of pool) {
    const prior = preferred.get(row.supply.canonicalId);
    if (!prior || row.priority === prior.priority && (row.score > prior.score ||
        row.score === prior.score && compareModelIds(row.supply.id, prior.supply.id) < 0)) {
      preferred.set(row.supply.canonicalId, row);
    }
  }
  const unique = [...preferred.values()];
  const minCap = Math.max(...unique.map(row => row.cap)) - margin;
  const [best] = unique.filter(row => row.cap >= minCap).sort((a, b) => b.score - a.score || a.priority - b.priority ||
    compareModelIds(a.supply.canonicalId, b.supply.canonicalId) || compareModelIds(a.supply.id, b.supply.id));
  const { providerId, credentialSourceId, name: providerName, wireApi, baseUrl } = best.account;
  const { id: resolvedModel, canonicalId: canonicalModelId, contextTokens, outputTokens, tier, capabilities } = best.supply;
  return { providerId, credentialSourceId, providerName, role, resolvedModel, thinkingLevel: best.thinkingLevel,
    effectiveRevision, wireApi, baseUrl, contextTokens, outputTokens, capabilities,
    canonicalModelId, source: 'auto', costTier: tier, reason: !ordinary.length ? 'fallback_only' : unique.length === 1 ? 'single_model' : 'ranked' };
}

export function resolvedRoles(accounts: readonly ProviderAccount[], effectiveRevision: number, policy: RoutingPolicy,
  available: (account: ProviderAccount) => boolean) {
  return Object.fromEntries(roles.map(role => {
    const route = resolveRole(accounts, effectiveRevision, policy, role, available);
    if (!route) return [role, null];
    const { providerId, credentialSourceId, providerName, resolvedModel, thinkingLevel, canonicalModelId, source, costTier, reason } = route;
    return [role, { providerId, credentialSourceId, providerName, resolvedModel, role, thinkingLevel, canonicalModelId, source, costTier, reason }];
  }));
}
