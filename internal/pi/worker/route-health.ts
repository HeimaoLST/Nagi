import { endpointFingerprint, hasUsableModel, type CapturedRoute, type HealthSummary, type ProviderAccount, type RouteHealthView } from './model-router.js';

export type RouteOrigin = Pick<CapturedRoute, 'providerId' | 'baseUrl' | 'wireApi' | 'credentialSourceId' | 'resolvedModel'>;
export type ProviderAttempt = {
  headers: (response: Response) => void;
  settle: (outcome: 'success' | 'error' | 'aborted', error?: unknown) => void;
};
type Failure = 'rate_limited' | 'provider_failure' | 'network' | 'stream_error';
type ScopeState = { generation: number; reason?: Failure; failures: number; retry?: number; lease?: symbol };
type Endpoint = { state: ScopeState; models: Map<string, ScopeState> };
const scope = (): ScopeState => ({ generation: 0, failures: 0 });
const connectErrors = new Set(['ECONNREFUSED', 'ENOTFOUND', 'EAI_AGAIN', 'EHOSTUNREACH', 'ENETUNREACH', 'UND_ERR_CONNECT_TIMEOUT']);
const setting = (key: string, fallback: number) => {
  const value = Number(process.env[key]);
  return Number.isFinite(value) && value > 0 ? value : fallback;
};
const maxCooldown = setting('NAGI_HEALTH_MAX_COOLDOWN_MS', 900_000);
const minCooldown = Math.min(maxCooldown, setting('NAGI_HEALTH_MIN_COOLDOWN_MS', 15_000));
const backoffScale = setting('NAGI_HEALTH_BACKOFF_SCALE', 1);
const schedule = { rate_limited: [60_000, 180_000, 300_000], provider_failure: [30_000, 120_000, 300_000],
  stream_error: [30_000, 120_000, 300_000], network: [30_000, 120_000, 300_000] };

function retryDelay(header: string | null, now: number): number | undefined {
  if (!header) return;
  const value = header.trim();
  const delay = /^\d+$/.test(value) ? Number(value) * 1_000 : Date.parse(value) - now;
  if (!Number.isNaN(delay)) return Math.max(minCooldown, Math.min(maxCooldown, delay));
}

function isConnectError(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false;
  if ('code' in error && typeof error.code === 'string' && connectErrors.has(error.code)) return true;
  if (error instanceof AggregateError) return error.errors.length > 0 && error.errors.every(isConnectError);
  return error instanceof Error && error.cause !== undefined && error.cause !== error && isConnectError(error.cause);
}

export class RouteHealth {
  private readonly rejected = new Set<string>();
  private readonly endpoints = new Map<string, Endpoint>();

  authFailed(sourceId: string): boolean { return this.rejected.has(sourceId); }

  private key(route: Omit<RouteOrigin, 'resolvedModel' | 'credentialSourceId'>): string {
    return JSON.stringify([route.providerId, endpointFingerprint(route)]);
  }

  cooldown(route: RouteOrigin, now = Date.now()): number | undefined {
    const endpoint = this.endpoints.get(this.key(route));
    if (!endpoint) return;
    const blocked = [endpoint.state, endpoint.models.get(route.resolvedModel)]
      .filter(state => state?.retry !== undefined && (state.retry > now || state.lease !== undefined));
    if (blocked.length) return Math.max(...blocked.map(state => state!.retry!));
  }

  claim(route: RouteOrigin, now = Date.now()): (() => void) | undefined {
    const endpoint = this.endpoints.get(this.key(route));
    const scopes = [endpoint?.state, endpoint?.models.get(route.resolvedModel)].filter((state): state is ScopeState => state?.retry !== undefined);
    if (scopes.some(state => state.lease || state.retry! > now)) return;
    const lease = Symbol();
    for (const state of scopes) state.lease = lease;
    return () => {
      for (const state of scopes) if (state.lease === lease) delete state.lease;
    };
  }

  begin(sourceId: string, route: RouteOrigin | undefined, credentialPresent: () => boolean, probe = false): ProviderAttempt {
    let endpoint: Endpoint | undefined;
    let model: ScopeState | undefined;
    if (route) {
      const key = this.key(route);
      endpoint = this.endpoints.get(key);
      if (!endpoint && !probe) this.endpoints.set(key, endpoint = { state: scope(), models: new Map() });
      model = endpoint?.models.get(route.resolvedModel);
      if (endpoint && !model && !probe) endpoint.models.set(route.resolvedModel, model = scope());
    }
    const routeEpoch = model?.generation;
    const endpointEpoch = endpoint?.state.generation;
    let status: number | undefined;
    return {
      headers: response => {
        status = response.status;
        if (probe || !credentialPresent()) return;
        if (status === 401 || status === 403) this.rejected.add(sourceId);
        else if (model && status === 429) this.fail(model, 'rate_limited', response.headers.get('retry-after'));
        else if (model && status >= 500 && status <= 599) this.fail(model, 'provider_failure');
      },
      settle: (outcome, error) => {
        if (outcome === 'aborted' || !credentialPresent() || this.authFailed(sourceId)) return;
        if (outcome === 'success' && status !== undefined && status >= 200 && status < 300) {
          if (!probe && endpoint && endpoint.state.generation === endpointEpoch) this.clear(endpoint.state);
          if (model && model.generation === routeEpoch) this.clear(model);
          return;
        }
        if (probe || !model || !endpoint) return;
        if (status === undefined && isConnectError(error)) this.fail(endpoint.state, 'network');
        else if (status !== undefined && status >= 200 && status < 300) this.fail(model, 'stream_error');
      },
    };
  }

  private clear(state: ScopeState): void {
    state.failures = 0;
    delete state.reason;
    delete state.retry;
  }

  private fail(state: ScopeState, reason: Failure, retryAfter: string | null = null): void {
    const now = Date.now();
    state.generation++;
    const sameStreak = state.reason === reason || (state.reason === 'provider_failure' || state.reason === 'stream_error') &&
      (reason === 'provider_failure' || reason === 'stream_error');
    state.failures = sameStreak ? state.failures + 1 : 1;
    state.reason = reason;
    if (state.retry === undefined && state.failures === 1 && (reason === 'provider_failure' || reason === 'stream_error')) return;
    const index = reason === 'provider_failure' || reason === 'stream_error' ? state.failures - 2 : state.failures - 1;
    const backoff = schedule[reason][Math.max(0, Math.min(2, index))] * backoffScale;
    const delay = retryDelay(retryAfter, now) ?? Math.max(minCooldown, Math.min(maxCooldown, backoff));
    state.retry = Math.max(state.retry ?? 0, now + delay);
  }

  view(account: ProviderAccount, now = Date.now()): { healthSummary: HealthSummary; routeHealth?: RouteHealthView[] } {
    const endpoint = this.endpoints.get(this.key(account));
    const routeHealth: RouteHealthView[] = [];
    if (this.authFailed(account.credentialSourceId)) routeHealth.push({ scope: 'credential', state: 'auth_failed', reason: 'auth_failed' });
    const add = (state: ScopeState, wireModelId?: string) => {
      if (!state.reason) return;
      const retryAt = state.retry === undefined ? undefined : new Date(state.retry).toISOString();
      routeHealth.push({ ...(wireModelId === undefined ? {} : { wireModelId }), scope: wireModelId === undefined ? 'endpoint' : 'route',
        state: state.lease || state.retry !== undefined && state.retry <= now ? 'half_open' : state.retry === undefined ? 'degraded' : 'cooldown',
        reason: state.reason, consecutiveFailures: state.failures, ...(retryAt === undefined ? {} : { retryAt }) });
    };
    if (endpoint) {
      add(endpoint.state);
      for (const [model, state] of endpoint.models) add(state, model);
    }
    const healthSummary = this.authFailed(account.credentialSourceId) ? 'auth_failed' :
      routeHealth.some(row => row.state === 'cooldown') && !hasUsableModel(account, resolvedModel => this.cooldown({ ...account, resolvedModel }, now) === undefined)
        ? 'cooldown' : routeHealth.length ? 'degraded' : 'healthy';
    return { healthSummary, ...(routeHealth.length ? { routeHealth } : {}) };
  }
}
