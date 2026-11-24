import { Type, type Static } from 'typebox';

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

export const providerInput = Type.Object({ providerId, name: key, wireApi, baseUrl: Type.String({ minLength: 1, maxLength: 2048 }),
  models: Type.Optional(Type.Array(key, { maxItems: 128, uniqueItems: true })), defaultModel: key, contextTokens: count, outputTokens: count,
  enabled: Type.Boolean() }, strict);
export const providerAccount = Type.Object({ ...providerInput.properties, credentialSourceId: key }, strict);
export const providerAccountView = Type.Object({ ...providerAccount.properties,
  credentialStatus: Type.Union([Type.Literal('ok'), Type.Literal('credential_missing')]) }, strict);
export const routeSchema = Type.Object({ providerId, credentialSourceId: key, resolvedModel: key, role: roleSchema, thinkingLevel: thinkingSchema }, strict);
export const capturedRoute = Type.Object({ ...routeSchema.properties, effectiveRevision: count,
  wireApi, baseUrl: providerInput.properties.baseUrl, contextTokens: count, outputTokens: count }, strict);
export const escalationSchema = Type.Object({ activityId: key, priorExecutionId: key, escalatedExecutionId: key,
  fromRole: Type.Literal('utility'), toRole: Type.Union([Type.Literal('coding'), Type.Literal('reasoning')]), at: key }, strict);

export type ProviderInput = Static<typeof providerInput>;
export type ProviderAccount = Static<typeof providerAccount>;
export type RoutingPolicy = Static<typeof policySchema>;
export type ModelRole = Static<typeof roleSchema>;
export type CapturedRoute = Static<typeof capturedRoute>;
export type ActivityRouting = Static<typeof activityRouting>;
export type Escalation = Static<typeof escalationSchema>;

const roles = ['dialogue', 'coding', 'reasoning', 'utility'] as const;
const matches: Record<ModelRole, RegExp> = {
  dialogue: /(?:^|[-_.:/])(?:dialogue|chat|gpt-4o|sonnet|flash)(?:$|[-_.:/])/i,
  coding: /(?:^|[-_.:/])(?:codex|coder|coding|deepseek-v3|sonnet)(?:$|[-_.:/])/i,
  reasoning: /(?:^|[-_.:/])(?:reasoning|reasoner|thinking|deepseek-r1|o[134](?:-mini)?|gpt-5|opus|gemini-[\d.]+-pro)(?:$|[-_.:/])/i,
  utility: /(?:^|[-_.:/])(?:utility|nano|mini|small|haiku|flash)(?:$|[-_.:/])/i,
};
const premium = /(?:^|[-_.:/])(?:pro|opus|flagship|ultra)(?:$|[-_.:/])/i;
export const providerCacheTtlMs = 5 * 60 * 1000;

export function providerEndpoint(baseUrl: string): URL | undefined {
  const url = URL.parse(baseUrl);
  if (url && (url.protocol === 'http:' || url.protocol === 'https:') && !url.username && !url.password && !url.search && !url.hash) return url;
}

export function validProvider(account: ProviderInput): boolean {
  return !!providerEndpoint(account.baseUrl) && !!account.name.trim() && !!account.defaultModel.trim() &&
    account.contextTokens - account.outputTokens >= 4 && (account.models?.every(model => !!model.trim()) ?? true);
}

export function activityRole(routing: ActivityRouting | undefined, hasMcp: boolean): ModelRole {
  if (routing?.kind === 'quick' || routing?.complexity === 'light') return 'utility';
  if (routing?.kind === 'coding') return 'coding';
  if (routing?.kind === 'research' || routing?.complexity === 'deep' || hasMcp || routing?.constraints?.length) return 'reasoning';
  return 'utility';
}

export function resolveRole(accounts: readonly ProviderAccount[], effectiveRevision: number, policy: RoutingPolicy,
  role: ModelRole, available: (account: ProviderAccount) => boolean, deep = false): CapturedRoute | null {
  let best: { account: ProviderAccount; model: string; score: number } | undefined;
  for (const account of accounts) {
    if (!account.enabled || !available(account)) continue;
    for (const model of new Set([account.defaultModel, ...account.models ?? []])) {
      const specialized = matches[role].test(model);
      const inexpensive = matches.utility.test(model);
      const score = (specialized ? 20 : 0) + (model === account.defaultModel ? 1 : 0) +
        (policy === 'economy' && inexpensive ? 8 : 0) + (policy === 'quality' && premium.test(model) && role !== 'utility' ? 8 : 0);
      if (!best || score > best.score) best = { account, model, score };
    }
  }
  if (!best) return null;
  const { providerId, credentialSourceId, wireApi, baseUrl, contextTokens, outputTokens } = best.account;
  const supportsThinking = matches.reasoning.test(best.model) || matches.coding.test(best.model) && /codex|coding|coder/i.test(best.model);
  const thinkingLevel = !supportsThinking || role === 'utility' ? 'off' : role === 'dialogue' ? (policy === 'economy' ? 'off' : 'low') : deep ? 'high' : 'medium';
  return { providerId, credentialSourceId, role, resolvedModel: best.model, thinkingLevel,
    effectiveRevision, wireApi, baseUrl, contextTokens, outputTokens };
}

export function resolvedRoles(accounts: readonly ProviderAccount[], effectiveRevision: number, policy: RoutingPolicy,
  available: (account: ProviderAccount) => boolean) {
  return Object.fromEntries(roles.map(role => {
    const route = resolveRole(accounts, effectiveRevision, policy, role, available);
    if (!route) return [role, null];
    const { providerId, credentialSourceId, resolvedModel, thinkingLevel } = route;
    return [role, { providerId, credentialSourceId, resolvedModel, role, thinkingLevel }];
  }));
}
