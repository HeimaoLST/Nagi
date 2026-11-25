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

const literals = <const T extends string[]>(...values: T) => Type.Enum(values);
const modelPin = Type.Object({ mode: Type.Literal('model'), canonicalModelId: Type.String({ minLength: 1, maxLength: 1024 }) }, strict);
const routePinFields = { mode: Type.Literal('route'), providerId, model: key };
const routePin = Type.Object(routePinFields, strict);
const storedRoutePin = Type.Object({ ...routePinFields, endpointFingerprint: Type.String({ minLength: 1, maxLength: 8192 }) }, strict);
const savedPin = Type.Union([modelPin, storedRoutePin]);
const pinPatch = Type.Optional(Type.Union([modelPin, routePin, Type.Null()]));
export const roles = ['dialogue', 'coding', 'reasoning', 'utility'] as const;
export const overridesPatchSchema = Type.Object({ dialogue: pinPatch, coding: pinPatch, reasoning: pinPatch, utility: pinPatch }, strict);
const optionalPin = Type.Optional(savedPin);
export const overridesSchema = Type.Object({ dialogue: optionalPin, coding: optionalPin, reasoning: optionalPin, utility: optionalPin }, strict);
const failoverSchema = literals('same_model_cooldown', 'model_cooldown');
const unavailableReason = literals('cooldown', 'provider_deleted', 'provider_disabled', 'endpoint_changed', 'model_disabled',
  'incompatible', 'credential_missing', 'auth_failed', 'empty_pool');
const roleStatus = Type.Object({ mode: literals('auto', 'model', 'route'),
  status: literals('ready', 'cooldown', 'auth_failed', 'credential_missing', 'pin_unavailable', 'empty_pool', 'incompatible'),
  unavailableReason: Type.Optional(unavailableReason), retryAt: Type.Optional(key), failover: Type.Optional(failoverSchema) }, strict);
export const roleStatusSchema = Type.Object({ dialogue: roleStatus, coding: roleStatus, reasoning: roleStatus, utility: roleStatus }, strict);
const routeHealth = Type.Object({ wireModelId: Type.Optional(key), scope: literals('credential', 'endpoint', 'route'),
  state: literals('degraded', 'cooldown', 'half_open', 'auth_failed'), reason: literals('rate_limited', 'provider_failure', 'network', 'stream_error', 'auth_failed'),
  retryAt: Type.Optional(key), consecutiveFailures: Type.Optional(count) }, strict);
const healthSummary = literals('healthy', 'degraded', 'cooldown', 'auth_failed');

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
  credentialStatus: Type.Union([Type.Literal('ok'), Type.Literal('credential_missing')]),
  healthSummary: Type.Optional(healthSummary), routeHealth: Type.Optional(Type.Array(routeHealth)) }, strict);
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
  canonicalModelId: Type.Optional(Type.String()), source: Type.Optional(literals('auto', 'pinned_model', 'pinned_route')), costTier: Type.Optional(costTier),
  reason: Type.Optional(Type.String()), providerName: Type.Optional(key), failover: Type.Optional(failoverSchema) }, strict);
export const capturedRoute = Type.Object({ ...routeSchema.properties, effectiveRevision: count,
  wireApi, baseUrl: providerInput.properties.baseUrl, contextTokens: count, outputTokens: count, capabilities: Type.Optional(modelCapabilities),
  toolCall: Type.Optional(evidence) }, strict);
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

export type RoutingOverrides = Static<typeof overridesSchema>;
export type RoutingOverride = Static<typeof savedPin>;
export type RoutingOverridesPatch = Static<typeof overridesPatchSchema>;
export type RoleStatus = Static<typeof roleStatus>;
export type RouteHealthView = Static<typeof routeHealth>;
export type HealthSummary = Static<typeof healthSummary>;
export type RouteRequirements = { requiresTools: boolean; inputTokens: number };
export type RouteDecision = { route: CapturedRoute | null; status: RoleStatus };
type CredentialStatus = 'ready' | 'credential_missing' | 'auth_failed';
type Scope = Pick<CapturedRoute, 'providerId' | 'baseUrl' | 'wireApi'>;

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

export function endpointFingerprint(account: Pick<ProviderAccount, 'wireApi' | 'baseUrl'>): string {
  return `${account.wireApi}|${providerEndpoint(account.baseUrl)!.href.replace(/\/+$/, '')}`;
}

function activeModels(account: ProviderAccount) {
  return new Set(account.enabledModels ?? [account.defaultModel, ...account.models ?? []]);
}

function authorizedSupply(account: ProviderAccount, id: string) {
  const isDefault = id === account.defaultModel;
  const supply = modelSupply(account, id, isDefault && account.enabledModels === undefined);
  const authorized = account.enabledModels !== undefined || (!supply.priceProtected && (isDefault || supply.recommended));
  return { supply, authorized };
}

export function hasUsableModel(account: ProviderAccount, available: (model: string) => boolean): boolean {
  return [...activeModels(account)].some(id => {
    if (!available(id)) return false;
    const { supply, authorized } = authorizedSupply(account, id);
    return authorized && supply.isChat === true && supply.toolCall === true && supply.contextTokens - supply.outputTokens >= 4;
  });
}

export function validatePin(accounts: readonly ProviderAccount[], pin: Exclude<RoutingOverridesPatch[ModelRole], null | undefined>): RoutingOverride | undefined {
  for (const account of accounts) {
    if (!account.enabled || pin.mode === 'route' && account.providerId !== pin.providerId) continue;
    for (const id of activeModels(account)) {
      if (pin.mode === 'route' && id !== pin.model) continue;
      const { supply, authorized } = authorizedSupply(account, id);
      if (!authorized || supply.isChat !== true || supply.contextTokens - supply.outputTokens < 4) continue;
      if (pin.mode === 'route') return { ...pin, endpointFingerprint: endpointFingerprint(account) };
      if (supply.canonicalId === pin.canonicalModelId) return pin;
    }
  }
}

function routeAccount(route: CapturedRoute): ProviderAccount {
  return { ...route, name: route.providerName ?? route.providerId, defaultModel: route.resolvedModel, enabled: true };
}

export function capturedCompatible(route: CapturedRoute, requirements: RouteRequirements): boolean {
  if (route.contextTokens - route.outputTokens < Math.max(4, requirements.inputTokens)) return false;
  return !requirements.requiresTools || (route.toolCall === undefined ? modelSupply(routeAccount(route), route.resolvedModel, true).toolCall : route.toolCall) === true;
}

export function publicRoute(route: CapturedRoute) {
  const { providerId, credentialSourceId, providerName, resolvedModel, thinkingLevel, canonicalModelId, source, costTier, reason, role, failover } = route;
  return { providerId, credentialSourceId, providerName, resolvedModel, role, thinkingLevel, canonicalModelId, source, costTier, reason, failover };
}

export function resolveRole(accounts: readonly ProviderAccount[], effectiveRevision: number, policy: RoutingPolicy, role: ModelRole,
  options: { pin?: RoutingOverride; deep?: boolean; requirements?: RouteRequirements;
    credential: (account: ProviderAccount) => CredentialStatus;
    cooldown: (route: Scope & { resolvedModel: string; credentialSourceId: string }) => number | undefined }): RouteDecision {
  const { pin, deep, requirements = { requiresTools: true, inputTokens: 0 }, credential, cooldown } = options;
  const mode = pin?.mode ?? 'auto';
  const unavailable = (reason: NonNullable<RoleStatus['unavailableReason']>, retryAt?: number): RouteDecision => ({ route: null,
    status: { mode, status: reason === 'auth_failed' || reason === 'credential_missing' || reason === 'cooldown' || reason === 'incompatible' || reason === 'empty_pool'
      ? reason : 'pin_unavailable', unavailableReason: reason, ...(retryAt === undefined ? {} : { retryAt: new Date(retryAt).toISOString() }) } });
  const pinnedAccount = pin?.mode === 'route' ? accounts.find(account => account.providerId === pin.providerId) : undefined;
  if (pin?.mode === 'route') {
    if (!pinnedAccount) return unavailable('provider_deleted');
    if (endpointFingerprint(pinnedAccount) !== pin.endpointFingerprint) return unavailable('endpoint_changed');
    if (!pinnedAccount.enabled) return unavailable('provider_disabled');
    if (!activeModels(pinnedAccount).has(pin.model) || !authorizedSupply(pinnedAccount, pin.model).authorized) return unavailable('model_disabled');
  }
  const requested = role === 'utility' ? 'off' : role === 'dialogue' ? (policy === 'economy' ? 'off' : 'low') : deep ? 'high' : 'medium';
  const { margin, weights: [capWeight, speedWeight, costWeight, priorityWeight] } = policies[policy];
  let incompatible = false;
  let authFailed = false;
  let missing = false;
  const routes = accounts.flatMap((account, priority) => {
    if (!account.enabled || pinnedAccount && account !== pinnedAccount) return [];
    const credentialState = credential(account);
    const priorityScore = Math.round(100 * (accounts.length - priority) / accounts.length);
    return [...activeModels(account)].flatMap(id => {
      if (pin?.mode === 'route' && id !== pin.model) return [];
      const { supply, authorized } = authorizedSupply(account, id);
      if (pin?.mode === 'model' && supply.canonicalId !== pin.canonicalModelId) return [];
      if (!authorized) return [];
      const thinkingLevel = lowerThinking(supply.supported, requested);
      if (supply.isChat !== true || requirements.requiresTools && supply.toolCall !== true || thinkingLevel === undefined ||
          supply.contextTokens - supply.outputTokens < Math.max(4, requirements.inputTokens)) {
        incompatible = true;
        return [];
      }
      if (credentialState !== 'ready') {
        authFailed ||= credentialState === 'auth_failed';
        missing ||= credentialState === 'credential_missing';
        return [];
      }
      const cap = supply.family?.rank[role] ?? 60;
      const score = capWeight * cap + speedWeight * (supply.family?.speed ?? 65) +
        costWeight * costScores[supply.tier] + priorityWeight * priorityScore;
      return [{ account, priority, supply, thinkingLevel, cap, score }];
    });
  });
  if (!routes.length) return unavailable(authFailed ? 'auth_failed' : missing ? 'credential_missing' : incompatible ? 'incompatible' :
    pin?.mode === 'model' ? 'model_disabled' : 'empty_pool');

  const order = (a: typeof routes[number], b: typeof routes[number]) => Number(!!a.account.fallbackOnly) - Number(!!b.account.fallbackOnly) ||
    a.priority - b.priority || b.score - a.score || compareModelIds(a.supply.id, b.supply.id);
  const groups = Map.groupBy(routes.sort(order), row => row.supply.canonicalId);
  let retryAt = Infinity;
  let modelFailed = false;
  const choose = (siblings: typeof routes, reason: string): RouteDecision | undefined => {
    for (const [index, row] of siblings.entries()) {
      const { providerId, credentialSourceId, name: providerName, wireApi, baseUrl } = row.account;
      const { id: resolvedModel, canonicalId: canonicalModelId, contextTokens, outputTokens, tier, capabilities, toolCall } = row.supply;
      const at = cooldown({ providerId, credentialSourceId, wireApi, baseUrl, resolvedModel });
      if (at !== undefined) { retryAt = Math.min(retryAt, at); continue; }
      const failover = modelFailed ? 'model_cooldown' : index > 0 ? 'same_model_cooldown' : undefined;
      return { route: { providerId, credentialSourceId, providerName, role, resolvedModel, thinkingLevel: row.thinkingLevel,
        effectiveRevision, wireApi, baseUrl, contextTokens, outputTokens, capabilities, toolCall,
        canonicalModelId, source: mode === 'route' ? 'pinned_route' : mode === 'model' ? 'pinned_model' : 'auto', costTier: tier,
        reason, ...(failover ? { failover } : {}) }, status: { mode, status: 'ready', ...(failover ? { failover } : {}) } };
    }
  };
  if (pin) return choose(routes, pin.mode === 'route' ? 'exact_pin' : 'model_pin') ?? unavailable('cooldown', retryAt);

  const preferred = [...groups.values()].map(siblings => siblings[0]);
  const ordinary = preferred.filter(row => !row.account.fallbackOnly);
  let remaining = ordinary.length ? ordinary : preferred;
  while (remaining.length) {
    const minCap = Math.max(...remaining.map(row => row.cap)) - margin;
    const shortlist = remaining.filter(row => row.cap >= minCap).sort((a, b) => b.score - a.score || a.priority - b.priority ||
      compareModelIds(a.supply.canonicalId, b.supply.canonicalId));
    for (const row of shortlist) {
      const decision = choose(groups.get(row.supply.canonicalId)!, !ordinary.length ? 'fallback_only' : preferred.length === 1 ? 'single_model' : 'ranked');
      if (decision) return decision;
      modelFailed = true;
    }
    remaining = remaining.filter(row => row.cap < minCap);
  }
  return unavailable('cooldown', retryAt);
}
