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
  effects,
  providers,
  activityControl,
  modelRouter,
  botMemory,
  redactor,
  botSkills,
  mcpVault,
  mcpBroker,
] = await Promise.all([
  load('product-effects.js'),
  load('product-providers.js'),
  load('activity-control.js'),
  load('model-router.js'),
  load('bot-memory.js'),
  load('product-redactor.js'),
  load('bot-skills.js'),
  load('mcp-vault.js'),
  load('mcp-broker.js'),
]);

test('confirmedFileEffect validates write and edit receipts and rejects unsafe paths', () => {
  const cwd = '/tmp/nagi-work';
  assert.equal(effects.confirmedFileEffect(cwd, 'write', { path: 'src/a.txt' }, [{ type: 'text', text: 'Successfully wrote to src/a.txt' }]), '/tmp/nagi-work/src/a.txt');
  assert.equal(effects.confirmedFileEffect(cwd, 'edit', { path: '@src/a.txt' }, [{ type: 'text', text: 'Successfully replaced 3 block(s) in @src/a.txt.' }]), '/tmp/nagi-work/src/a.txt');
  assert.equal(effects.confirmedFileEffect(cwd, 'write', { path: '~/secret.txt' }, [{ type: 'text', text: 'Successfully wrote to ~/secret.txt' }]), undefined);
  assert.equal(effects.confirmedFileEffect(cwd, 'write', { path: 'file:///tmp/x' }, [{ type: 'text', text: 'Successfully wrote to file:///tmp/x' }]), undefined);
  assert.equal(effects.confirmedFileEffect(cwd, 'write', { path: 'a\0b' }, [{ type: 'text', text: 'Successfully wrote to a\0b' }]), undefined);
  assert.equal(effects.confirmedFileEffect(cwd, 'edit', { path: 'a.txt' }, [{ type: 'text', text: 'Successfully replaced 0 block(s) in a.txt.' }]), undefined);
  assert.equal(effects.confirmedFileEffect(cwd, 'read', { path: 'a.txt' }, [{ type: 'text', text: 'Successfully wrote to a.txt' }]), undefined);
  assert.deepEqual(effects.toolText({ ok: true }), {
    content: [{ type: 'text', text: '{"ok":true}' }],
    details: {},
    isError: false,
  });
});

test('readBoundedBytes and extractCatalogModels enforce limits and redact secrets', async () => {
  const makeStream = chunks => new ReadableStream({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(Buffer.from(chunk));
      controller.close();
    },
  });

  const exact = await providers.readBoundedBytes(makeStream(['ab', 'cd']), 4, 'too_large', 'too large');
  assert.equal(exact.toString('utf8'), 'abcd');

  await assert.rejects(
    providers.readBoundedBytes(makeStream(['abc', 'def']), 5, 'response_too_large', 'exceeded'),
    error => error.code === 'response_too_large',
  );

  const redact = value => value.replace(/secret-token/g, '[credential]');
  const many = Array.from({ length: 140 }, (_, i) => ({ id: `model-${i}` }));
  const models = providers.extractCatalogModels({
    data: [
      { id: 'gpt-4o' },
      { id: 'gpt-4o' },
      { id: '   ' },
      { id: 'has-secret-token-inside' },
      { id: 'x'.repeat(257) },
      ...many,
    ],
  }, redact);
  assert.equal(models.length, 141);
  assert.equal(models[0], 'gpt-4o');
  assert.equal(models.includes('has-secret-token-inside'), false);
  assert.throws(() => providers.extractCatalogModels({ invalid: true }, redact), error => error.code === 'provider_test_failed');
});

test('renderWhiteboard preserves input array order and sorts a copy deterministically', () => {
  const rows = [
    { activityId: 'act-low', executionId: 'e1', questionIds: [], requestIds: [], state: 'running', priority: 2, updatedAt: '2026-11-25T10:00:00Z', summary: 'lower priority' },
    { activityId: 'act-high', executionId: 'e2', questionIds: ['q-1'], requestIds: [], state: 'waitingForAnswer', priority: 1, updatedAt: '2026-11-25T09:00:00Z', summary: 'top secret-123' },
  ];
  const rendered = activityControl.renderWhiteboard(rows, text => text.replace(/secret-123/g, '[REDACTED]'));
  assert.equal(rows[0].activityId, 'act-low');
  assert.equal(rows[1].activityId, 'act-high');
  assert.ok(rendered);
  assert.ok(rendered.indexOf('act-high') < rendered.indexOf('act-low'));
  assert.equal(rendered.includes('secret-123'), false);

  const env = activityControl.steerEnvelope('steer-1', { text: 'focus' });
  assert.match(env, /<activity_steer id="steer-1" digest="[a-f0-9]{64}">/);

  assert.throws(
    () => activityControl.validateQuestionFields('Pick one', [{ id: 'a', label: 'A', recommended: true }, { id: 'b', label: 'B', recommended: true }], false),
    error => error.code === 'invalid_params',
  );
  const q = {
    questionId: 'q1',
    executionId: 'e1',
    text: 'Which naming convention should I use?',
    revision: 1,
    state: 'open',
    preferenceKey: 'naming',
    options: [
      { id: 'snake_case', label: 'snake_case' },
      { id: 'camel_case', label: 'camelCase' },
    ],
  };
  const domain = activityControl.triageDomain(q);
  assert.ok(domain);
  const prefs = activityControl.approvedPreferences(q, domain, [
    { ruleId: 'r1', botId: 'b1', text: 'naming: camelCase', source: 'approved', createdAt: '2026-11-25T00:00:00.000Z', updatedAt: '2026-11-25T00:00:00.000Z' },
  ]);
  assert.equal(prefs.length, 1);
  assert.equal(prefs[0].selectedOptionId, 'camel_case');
  assert.equal(activityControl.parseNomination(JSON.stringify({ evidenceId: 'r1', optionId: 'camel_case' }), prefs)?.evidenceId, 'r1');
  assert.equal(activityControl.parseNomination(JSON.stringify({ evidenceId: 'r1', extra: 1 }), prefs), undefined);
});

test('model router validates endpoints and selects routes by role and policy', () => {
  assert.equal(modelRouter.providerEndpoint('https://api.example.com/v1')?.origin, 'https://api.example.com');
  assert.equal(modelRouter.providerEndpoint('https://user:pass@api.example.com/v1'), undefined);
  assert.equal(modelRouter.providerEndpoint('https://api.example.com/v1?x=1'), undefined);

  assert.equal(modelRouter.activityRole({ kind: 'coding', complexity: 'light' }, true), 'utility');
  assert.equal(modelRouter.activityRole({ kind: 'coding', complexity: 'deep' }, false), 'coding');
  assert.equal(modelRouter.activityRole(undefined, true), 'reasoning');

  const customAccount = {
    providerId: 'p-custom',
    credentialSourceId: 'c-custom',
    name: 'Custom',
    wireApi: 'openai-completions',
    baseUrl: 'https://custom.example/v1',
    defaultModel: 'custom-model',
    contextTokens: 128000,
    outputTokens: 4096,
    enabled: true,
    enabledModels: ['custom-model'],
  };
  assert.equal(modelRouter.validProvider({ ...customAccount, contextTokens: 100, outputTokens: 97 }), false);
  assert.equal(modelRouter.resolveRole([customAccount], 1, 'balanced', 'dialogue', () => true), null);
  const overridden = modelRouter.resolveRole([
    { ...customAccount, modelOverrides: [{ id: 'custom-model', isChat: true, toolCall: true }] },
  ], 1, 'balanced', 'dialogue', () => true);
  assert.equal(overridden?.resolvedModel, 'custom-model');
  assert.equal(overridden?.thinkingLevel, 'off');

  const accounts = [
    {
      providerId: 'p1',
      credentialSourceId: 'c1',
      name: 'OpenAI',
      wireApi: 'openai-responses',
      baseUrl: 'https://api.openai.com/v1',
      defaultModel: 'gpt-4o-mini',
      contextTokens: 200000,
      outputTokens: 16384,
      enabled: true,
      models: ['gpt-5', 'gpt-4o-mini'],
    },
  ];
  const utility = modelRouter.resolveRole(accounts, 3, 'economy', 'utility', () => true, false);
  assert.equal(utility?.resolvedModel, 'gpt-4o-mini');
  assert.equal(utility?.thinkingLevel, 'off');
  const reasoning = modelRouter.resolveRole(accounts, 3, 'quality', 'reasoning', () => true, true);
  assert.equal(reasoning?.resolvedModel, 'gpt-5');
  assert.equal(reasoning?.canonicalModelId, 'openai/gpt-5');
  assert.equal(reasoning?.thinkingLevel, 'high');
});

test('memory validation, redaction, skills, and mcp helpers enforce contracts', () => {
  assert.equal(botMemory.validMemoryDate('2026-02-28'), true);
  assert.equal(botMemory.validMemoryDate('2026-02-30'), false);
  assert.equal(botMemory.validateMemoryCandidate('Bearer sk-123456', { maxBytes: 300 }).code, 'memory_secret');
  assert.equal(botMemory.validateMemoryCandidate('Currently running right now', { maxBytes: 300 }).code, 'memory_temporary');
  assert.deepEqual(botMemory.validateMemoryCandidate('Use snake_case in SQL', { maxBytes: 300 }), { text: 'Use snake_case in SQL' });
  assert.equal(botMemory.recurringPreference('Please check logs. From now on keep answers brief.'), 'From now on keep answers brief');

  assert.equal(Buffer.byteLength(redactor.clipUTF8('你好世界test', 9)), 9);
  const scrub = redactor.projectionRedactor(['sk-abc', 'sk-abcdef']);
  assert.equal(scrub('key=sk-abcdef'), 'key=[credential]');

  assert.equal(botSkills.canonicalSkillOrigin('https://Example.COM'), 'https://example.com:443');
  assert.throws(() => botSkills.canonicalSkillOrigin('https://example.com/path'), error => error.code === 'invalid_skill');
  assert.equal(botSkills.pureSkill({ workspaceRead: false, workspaceWrite: false }), true);
  assert.equal(botSkills.framedSkillDigest(['ab', 'c']) === botSkills.framedSkillDigest(['a', 'bc']), false);

  const stdioServer = {
    name: 'fs',
    transport: 'stdio',
    command: '/usr/local/bin/mcp-fs',
    secretEnvKeys: ['API_TOKEN'],
  };
  assert.equal(mcpVault.mcpSecretsComplete(stdioServer, { API_TOKEN: 'tok' }), true);
  assert.deepEqual(mcpVault.mergeMcpSecrets(stdioServer, { API_TOKEN: 'old' }, { env: { API_TOKEN: '' } }), {});
  assert.equal(mcpBroker.validMcpServer(stdioServer), true);
  assert.equal(mcpBroker.validMcpBindings([
    { serverId: 's1', policy: 'all', allowedTools: ['read'] },
  ]), false);
  assert.equal(mcpBroker.uniqueMcpRegistry([
    { serverId: 's1', name: 'my-tool', transport: 'stdio', command: '/usr/bin/true', isolation: 'shared', revision: 1 },
    { serverId: 's2', name: 'my_tool', transport: 'stdio', command: '/usr/bin/true', isolation: 'shared', revision: 1 },
  ]), false);
  assert.equal(mcpBroker.uniqueMcpRegistry([
    { serverId: 's1', name: 'my_tool', transport: 'stdio', command: '/usr/bin/true', isolation: 'shared', revision: 1 },
    { serverId: 's2', name: 'other_tool', transport: 'stdio', command: '/usr/bin/true', isolation: 'shared', revision: 1 },
  ]), true);
});
