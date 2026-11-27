import assert from 'node:assert/strict';
import { join } from 'node:path';
import test from 'node:test';
import { pathToFileURL } from 'node:url';

const distDir = process.env.NAGI_WORKER_DIST;
if (!distDir) {
  throw new Error('NAGI_WORKER_DIST must point to the built worker dist directory');
}

const load = name => import(pathToFileURL(join(distDir, name)).href);

const [
  { RouteHealth },
  modelRouter,
  modelCatalog,
  activityControl,
] = await Promise.all([
  load('route-health.js'),
  load('model-router.js'),
  load('model-catalog.js'),
  load('activity-control.js'),
]);

const openAiAccount = (overrides = {}) => ({
  providerId: 'p-openai',
  credentialSourceId: 'cred-openai',
  name: 'OpenAI',
  wireApi: 'openai-responses',
  baseUrl: 'https://api.openai.com/v1',
  defaultModel: 'gpt-4o-mini',
  models: ['gpt-5', 'gpt-4o-mini'],
  contextTokens: 200000,
  outputTokens: 16384,
  enabled: true,
  ...overrides,
});

const routeFor = (account, resolvedModel = account.defaultModel) => ({
  providerId: account.providerId,
  credentialSourceId: account.credentialSourceId,
  wireApi: account.wireApi,
  baseUrl: account.baseUrl,
  resolvedModel,
});

test('RouteHealth tracks auth failures, 429 Retry-After clamping, and shared 5xx/stream streaks', () => {
  let now = 1_700_000_000_000;
  const health = new RouteHealth(() => now);
  const account = openAiAccount();
  const gpt5 = routeFor(account, 'gpt-5');
  const mini = routeFor(account, 'gpt-4o-mini');

  const authAttempt = health.begin(account.credentialSourceId, gpt5, () => true);
  authAttempt.headers(new Response(null, { status: 401 }));
  assert.equal(health.authFailed(account.credentialSourceId), true);
  const afterAuth = health.begin(account.credentialSourceId, gpt5, () => true);
  afterAuth.headers(new Response(null, { status: 200 }));
  afterAuth.settle('success');
  assert.equal(health.authFailed(account.credentialSourceId), true);
  assert.equal(health.view(account, now).healthSummary, 'auth_failed');

  const h2 = new RouteHealth(() => now);
  const acc2 = openAiAccount({ credentialSourceId: 'cred-2' });
  const r2 = routeFor(acc2, 'gpt-5');

  const rate1 = h2.begin(acc2.credentialSourceId, r2, () => true);
  rate1.headers(new Response(null, { status: 429 }));
  rate1.settle('error');
  assert.equal(h2.cooldown(r2, now), now + 60_000);
  assert.equal(h2.view(acc2, now).healthSummary, 'degraded');

  const rateClamped = h2.begin(acc2.credentialSourceId, r2, () => true);
  rateClamped.headers(new Response(null, { status: 429, headers: { 'retry-after': '5' } }));
  assert.equal(h2.cooldown(r2, now), now + 60_000);

  now += 60_000;
  assert.equal(h2.cooldown(r2, now), undefined);
  const rateDate = h2.begin(acc2.credentialSourceId, r2, () => true);
  rateDate.headers(new Response(null, { status: 429, headers: { 'retry-after': new Date(now + 240_000).toUTCString() } }));
  assert.equal(h2.cooldown(r2, now), now + 240_000);

  now += 240_000;
  const rateMax = h2.begin(acc2.credentialSourceId, r2, () => true);
  rateMax.headers(new Response(null, { status: 429, headers: { 'retry-after': '9999' } }));
  assert.equal(h2.cooldown(r2, now), now + 900_000);

  const err1 = h2.begin(acc2.credentialSourceId, mini, () => true);
  err1.headers(new Response(null, { status: 502 }));
  err1.settle('error');
  assert.equal(h2.cooldown(mini, now), undefined);
  assert.equal(h2.view(acc2, now).routeHealth?.some(row => row.wireModelId === 'gpt-4o-mini' && row.state === 'degraded'), true);

  const err2 = h2.begin(acc2.credentialSourceId, mini, () => true);
  err2.headers(new Response(null, { status: 200 }));
  err2.settle('error');
  assert.equal(h2.cooldown(mini, now), now + 30_000);
  assert.equal(h2.view(acc2, now).healthSummary, 'cooldown');
});

test('RouteHealth isolates endpoint connect errors, leases single half-open probes, and guards epochs', () => {
  let now = 1_700_000_000_000;
  const health = new RouteHealth(() => now);
  const account = openAiAccount();
  const gpt5 = routeFor(account, 'gpt-5');
  const mini = routeFor(account, 'gpt-4o-mini');

  const netAttempt = health.begin(account.credentialSourceId, gpt5, () => true);
  const agg = new AggregateError([
    Object.assign(new Error('v4'), { code: 'ECONNREFUSED' }),
    Object.assign(new Error('v6'), { code: 'ENETUNREACH' }),
  ]);
  netAttempt.settle('error', new Error('fetch failed', { cause: agg }));
  assert.equal(health.cooldown(gpt5, now), now + 30_000);
  assert.equal(health.cooldown(mini, now), now + 30_000);
  assert.equal(health.claim(gpt5, now), undefined);
  assert.equal(health.view(account, now).healthSummary, 'cooldown');

  now += 30_000;
  assert.equal(health.cooldown(gpt5, now), undefined);
  assert.equal(health.view(account, now).routeHealth?.[0]?.state, 'half_open');

  const release = health.claim(gpt5, now);
  assert.ok(release);
  assert.equal(health.claim(gpt5, now), undefined);
  assert.equal(health.cooldown(gpt5, now), now);
  release();
  assert.equal(health.cooldown(gpt5, now), undefined);

  const probe = health.begin(account.credentialSourceId, gpt5, () => true, true);
  probe.headers(new Response(null, { status: 200 }));
  probe.settle('success');
  assert.equal(health.view(account, now).routeHealth?.some(row => row.scope === 'endpoint'), true);

  const live = health.begin(account.credentialSourceId, gpt5, () => true, false);
  live.headers(new Response(null, { status: 200 }));
  live.settle('success');
  assert.equal(health.view(account, now).healthSummary, 'healthy');

  const stale = health.begin(account.credentialSourceId, gpt5, () => true);
  stale.headers(new Response(null, { status: 200 }));
  const newer = health.begin(account.credentialSourceId, gpt5, () => true);
  newer.headers(new Response(null, { status: 429 }));
  stale.settle('success');
  assert.equal(health.cooldown(gpt5, now), now + 60_000);

  now += 60_000;
  const aborted = health.begin(account.credentialSourceId, gpt5, () => true);
  aborted.headers(new Response(null, { status: 200 }));
  aborted.settle('aborted');
  assert.equal(health.view(account, now).routeHealth?.some(row => row.wireModelId === 'gpt-5'), true);
});

test('model router validates endpoints, pins, captured compatibility, and role failover', () => {
  assert.equal(modelRouter.providerEndpoint('https://api.example.com/v1/')?.href, 'https://api.example.com/v1/');
  assert.equal(modelRouter.providerEndpoint('ftp://api.example.com/v1'), undefined);
  assert.equal(modelRouter.providerEndpoint('https://user:pass@api.example.com/v1'), undefined);
  assert.equal(modelRouter.providerEndpoint('https://api.example.com/v1?x=1#y'), undefined);
  assert.equal(modelRouter.endpointFingerprint({ wireApi: 'openai-responses', baseUrl: 'https://api.openai.com/v1/' }),
    'openai-responses|https://api.openai.com/v1');

  assert.equal(modelRouter.activityRole({ kind: 'quick', complexity: 'deep' }, true), 'utility');
  assert.equal(modelRouter.activityRole({ kind: 'coding', complexity: 'light' }, false), 'utility');
  assert.equal(modelRouter.activityRole({ kind: 'coding', complexity: 'standard' }, false), 'coding');
  assert.equal(modelRouter.activityRole({ kind: 'writing', constraints: ['strict'] }, false), 'reasoning');
  assert.equal(modelRouter.activityRole(undefined, true), 'reasoning');
  assert.equal(modelRouter.activityRole(undefined, false), 'utility');

  const custom = openAiAccount({
    providerId: 'p-custom',
    baseUrl: 'https://custom.example/v1',
    defaultModel: 'custom-model',
    models: ['custom-model'],
    enabledModels: ['custom-model'],
  });
  assert.equal(modelRouter.validProvider({ ...custom, modelOverrides: [{ id: 'custom-model' }, { id: 'custom-model' }] }), false);
  assert.equal(modelRouter.validProvider({ ...custom, modelOverrides: [{ id: 'custom-model', isChat: false, toolCall: true }] }), false);
  assert.equal(modelRouter.validProvider({ ...custom, modelOverrides: [{ id: 'custom-model', contextTokens: 100, outputTokens: 97 }] }), false);
  assert.equal(modelRouter.validProvider({ ...custom, modelOverrides: [{ id: 'custom-model', isChat: true, toolCall: true }] }), true);

  const primary = openAiAccount({ providerId: 'p1', credentialSourceId: 'c1', models: ['gpt-5', 'gpt-4o-mini'] });
  const secondary = openAiAccount({ providerId: 'p2', credentialSourceId: 'c2', name: 'OpenAI Backup', models: ['gpt-5'] });
  const fallback = openAiAccount({ providerId: 'p3', credentialSourceId: 'c3', name: 'DeepSeek Fallback', wireApi: 'openai-completions',
    baseUrl: 'https://api.deepseek.com/v1', defaultModel: 'deepseek-flash', models: ['deepseek-flash'], fallbackOnly: true });

  const routePin = modelRouter.validatePin([primary], { mode: 'route', providerId: 'p1', model: 'gpt-5' });
  assert.deepEqual(routePin, { mode: 'route', providerId: 'p1', model: 'gpt-5', endpointFingerprint: 'openai-responses|https://api.openai.com/v1' });
  const modelPin = modelRouter.validatePin([primary], { mode: 'model', canonicalModelId: 'openai/gpt-5' });
  assert.deepEqual(modelPin, { mode: 'model', canonicalModelId: 'openai/gpt-5' });

  assert.equal(modelRouter.resolveRole([secondary], 2, 'balanced', 'coding',
    { pin: routePin, credential: () => 'ready', cooldown: () => undefined }).status.unavailableReason, 'provider_deleted');
  assert.equal(modelRouter.resolveRole([{ ...primary, baseUrl: 'https://proxy.example/v1' }], 2, 'balanced', 'coding',
    { pin: routePin, credential: () => 'ready', cooldown: () => undefined }).status.unavailableReason, 'endpoint_changed');
  assert.equal(modelRouter.resolveRole([{ ...primary, enabled: false }], 2, 'balanced', 'coding',
    { pin: routePin, credential: () => 'ready', cooldown: () => undefined }).status.unavailableReason, 'provider_disabled');
  assert.equal(modelRouter.resolveRole([{ ...primary, enabledModels: ['gpt-4o-mini'] }], 2, 'balanced', 'coding',
    { pin: routePin, credential: () => 'ready', cooldown: () => undefined }).status.unavailableReason, 'model_disabled');
  assert.equal(modelRouter.resolveRole([primary], 2, 'balanced', 'coding',
    { pin: routePin, requirements: { requiresTools: true, inputTokens: 199000 }, credential: () => 'ready', cooldown: () => undefined }).status.unavailableReason, 'incompatible');
  assert.equal(modelRouter.resolveRole([primary], 2, 'balanced', 'coding',
    { pin: routePin, credential: () => 'auth_failed', cooldown: () => undefined }).status.unavailableReason, 'auth_failed');

  const sameModelFailover = modelRouter.resolveRole([primary, secondary], 4, 'quality', 'reasoning', {
    pin: modelPin,
    deep: true,
    credential: () => 'ready',
    cooldown: route => route.providerId === 'p1' ? 1_700_000_050_000 : undefined,
  });
  assert.equal(sameModelFailover.route?.providerId, 'p2');
  assert.equal(sameModelFailover.route?.failover, 'same_model_cooldown');
  assert.equal(sameModelFailover.route?.reason, 'model_pin');
  assert.equal(sameModelFailover.route?.thinkingLevel, 'high');

  const allPinnedCooldown = modelRouter.resolveRole([primary], 4, 'quality', 'reasoning', {
    pin: routePin,
    credential: () => 'ready',
    cooldown: () => 1_700_000_050_000,
  });
  assert.equal(allPinnedCooldown.route, null);
  assert.equal(allPinnedCooldown.status.status, 'cooldown');
  assert.equal(allPinnedCooldown.status.retryAt, '2023-11-14T22:14:10.000Z');

  const modelFailover = modelRouter.resolveRole([primary], 5, 'quality', 'coding', {
    credential: () => 'ready',
    cooldown: route => route.resolvedModel === 'gpt-5' ? 1_700_000_010_000 : undefined,
  });
  assert.equal(modelFailover.route?.resolvedModel, 'gpt-4o-mini');
  assert.equal(modelFailover.route?.failover, 'model_cooldown');
  assert.equal(modelFailover.route?.reason, 'ranked');

  const fallbackOnly = modelRouter.resolveRole([fallback], 5, 'balanced', 'utility', {
    credential: () => 'ready',
    cooldown: () => undefined,
  });
  assert.equal(fallbackOnly.route?.providerId, 'p3');
  assert.equal(fallbackOnly.route?.reason, 'fallback_only');

  assert.equal(modelRouter.capturedCompatible(fallbackOnly.route, { requiresTools: true, inputTokens: 1000 }), true);
  assert.equal(modelRouter.capturedCompatible({ ...fallbackOnly.route, contextTokens: 1000, outputTokens: 200 },
    { requiresTools: true, inputTokens: 801 }), false);
  assert.equal(modelRouter.capturedCompatible({ ...fallbackOnly.route, toolCall: false },
    { requiresTools: true, inputTokens: 10 }), false);
});

test('model catalog normalizes DeepSeek endpoints, price protection, free tiers, and thinking clamping', () => {
  const deepseek = openAiAccount({
    providerId: 'ds',
    wireApi: 'openai-completions',
    baseUrl: 'https://api.deepseek.com/v1',
    defaultModel: 'deepseek-flash',
    contextTokens: 200000,
    outputTokens: 16384,
  });
  const dsView = modelCatalog.describeModel(deepseek, 'deepseek-flash');
  assert.equal(dsView.canonicalId, 'deepseek/deepseek-flash');
  assert.equal(dsView.isChat, true);
  assert.equal(dsView.toolCall, true);
  assert.equal(dsView.reasoning, true);
  assert.equal(dsView.recommended, true);
  assert.equal(modelCatalog.lowerThinking(['off', 'low', 'high'], 'medium'), 'low');
  assert.equal(modelCatalog.lowerThinking(['high'], 'medium'), undefined);

  const proUpper = modelCatalog.describeModel(openAiAccount({ defaultModel: 'GPT-5.4-PRO' }), 'gpt-5.4-pro');
  assert.equal(proUpper.priceProtected, true);
  assert.equal(proUpper.recommended, false);
  const proMixed = modelCatalog.describeModel(openAiAccount(), 'GPT-5.4-PRO');
  assert.equal(proMixed.priceProtected, true);

  const embed = modelCatalog.describeModel(openAiAccount(), 'text-embedding-3-large');
  assert.equal(embed.isChat, false);
  assert.equal(embed.toolCall, false);

  const openRouterFree = modelCatalog.describeModel(openAiAccount({
    wireApi: 'openai-completions',
    baseUrl: 'https://openrouter.ai/api/v1',
    defaultModel: 'google/gemma-4-31b-it:free',
    contextTokens: 300000,
    outputTokens: 64000,
  }), 'google/gemma-4-31b-it:free');
  assert.equal(openRouterFree.canonicalId, 'openrouter/google/gemma-4-31b-it');
  assert.equal(openRouterFree.tier, 'free');

  const customOverridden = modelCatalog.describeModel(openAiAccount({
    providerId: 'my-local',
    baseUrl: 'http://127.0.0.1:8080/v1',
    defaultModel: 'local-coder',
    contextTokens: 32000,
    outputTokens: 8192,
    modelOverrides: [{ id: 'local-coder', isChat: true, toolCall: true, contextTokens: 16384, outputTokens: 4096 }],
  }), 'local-coder');
  assert.equal(customOverridden.canonicalId, 'custom:8:my-local:local-coder');
  assert.equal(customOverridden.contextTokens, 16384);
  assert.equal(customOverridden.outputTokens, 4096);
  assert.equal(customOverridden.isChat, true);
  assert.equal(customOverridden.toolCall, true);
});

test('activity control renders non-mutating whiteboard, steer envelopes, and preference triage', () => {
  const rows = [
    { activityId: 'act-low', executionId: 'e1', questionIds: [], requestIds: [], state: 'running', priority: 2, updatedAt: '2026-11-25T10:00:00Z', summary: 'lower & <note>' },
    { activityId: 'act-leak', executionId: 'e-bad', questionIds: [], requestIds: [], state: 'running', priority: 0, updatedAt: '2026-11-25T11:00:00Z', summary: 'secret-token' },
    { activityId: 'act-high', executionId: 'e2', questionIds: ['q-1'], requestIds: [], state: 'waitingForAnswer', priority: 1, updatedAt: '2026-11-25T09:00:00Z', summary: 'top secret-123' },
  ];
  const snapshotOrder = rows.map(row => row.activityId);
  const rendered = activityControl.renderWhiteboard(rows, value => value.replace(/secret-123|e-bad/g, '[REDACTED]'));
  assert.deepEqual(rows.map(row => row.activityId), snapshotOrder);
  assert.ok(rendered);
  assert.equal(rendered.includes('act-leak'), false);
  assert.equal(rendered.includes('secret-123'), false);
  assert.equal(rendered.includes('lower &amp; &lt;note&gt;'), true);
  assert.ok(rendered.indexOf('act-high') < rendered.indexOf('act-low'));

  const env = activityControl.steerEnvelope('steer-1', { text: 'focus <now>' });
  assert.match(env, /^<activity_steer id="steer-1" digest="[a-f0-9]{64}">\n\{&quot;text&quot;:&quot;focus &lt;now&gt;&quot;\}\n<\/activity_steer>$/);

  assert.throws(
    () => activityControl.validateQuestionFields('Pick one', [{ id: 'a', label: 'A', recommended: true }, { id: 'b', label: 'B', recommended: true }], false),
    error => error.code === 'invalid_params',
  );
  const q = {
    questionId: 'q1',
    executionId: 'e1',
    text: 'Which output format should I use?',
    revision: 1,
    state: 'open',
    preferenceKey: 'formatting',
    options: [{ id: 'markdown', label: 'Markdown' }, { id: 'json', label: 'JSON' }],
  };
  const domain = activityControl.triageDomain(q);
  assert.ok(domain);
  assert.equal(activityControl.triageDomain({ ...q, requiresUserDecision: true }), undefined);
  const prefs = activityControl.approvedPreferences(q, domain, [
    { ruleId: 'r-explicit', botId: 'b1', text: 'formatting: JSON', source: 'explicit', createdAt: '2026-11-25T00:00:00Z', updatedAt: '2026-11-25T00:00:00Z' },
    { ruleId: 'r-1', botId: 'b1', text: 'formatting: Markdown', source: 'auto', createdAt: '2026-11-25T00:00:00Z', updatedAt: '2026-11-25T00:00:00Z' },
  ]);
  assert.equal(prefs.length, 1);
  assert.equal(prefs[0].evidenceId, 'r-1');
  assert.equal(prefs[0].selectedOptionId, 'markdown');
  assert.equal(activityControl.approvedPreferences(q, domain, [
    { ruleId: 'r-1', botId: 'b1', text: 'formatting: Markdown', source: 'auto', createdAt: '2026-11-25T00:00:00Z', updatedAt: '2026-11-25T00:00:00Z' },
    { ruleId: 'r-2', botId: 'b1', text: 'Prefer formatting: JSON', source: 'auto', createdAt: '2026-11-25T00:00:00Z', updatedAt: '2026-11-25T00:00:00Z' },
  ]).length, 0);
  assert.equal(activityControl.parseNomination('{"evidenceId":"r-1","optionId":"markdown"}', prefs)?.selectedOptionId, 'markdown');
  assert.equal(activityControl.parseNomination('{"evidenceId":"r-1","extra":1}', prefs), undefined);
});
