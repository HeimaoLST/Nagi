import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { pathToFileURL } from 'node:url';

const distDir = process.env.NAGI_WORKER_DIST;
if (!distDir) {
  throw new Error('NAGI_WORKER_DIST must point to the built worker dist directory');
}

const load = name => import(pathToFileURL(join(distDir, name)).href);

const [
  { LiteralMatch, OutputRing, RuntimeSessions, TerminalProjection },
  { ProductSQLite },
] = await Promise.all([
  load('session-supervisor.js'),
  load('product-sqlite.js'),
]);

function setupSessionStorage(t) {
  const root = mkdtempSync(join(realpathSync(tmpdir()), 'xuhu-session-unit-'));
  const profileDir = join(root, 'profile');
  const workspace = join(root, 'workspace');
  mkdirSync(profileDir, { recursive: true });
  mkdirSync(workspace, { recursive: true });
  const profilePath = join(profileDir, 'contacts.json');
  const credentialsPath = join(profileDir, 'connection.json');
  const botId = 'bot-sess-1';
  const conversationId = 'conv-sess-1';
  const otherConversationId = 'conv-sess-2';
  const activityId = 'act-sess-1';
  const profile = {
    version: 1,
    bots: [
      {
        botId,
        currentConversationId: conversationId,
        profileRevision: 1,
        archived: false,
        profile: {
          name: 'Session Bot',
          role: 'Operator',
          purpose: 'Verify session unit invariants',
          instructions: 'Run safely',
          workspace,
          codingTools: null,
          toolPreset: 'workspace',
        },
      },
    ],
    conversations: [
      { conversationId, botId, revision: 1, draft: { revision: 0, text: '', materials: [] } },
      { conversationId: otherConversationId, botId, revision: 1, draft: { revision: 0, text: '', materials: [] } },
    ],
    activities: [
      {
        activityId,
        ownerBotId: botId,
        originConversationId: conversationId,
        revision: 1,
        state: 'running',
        goal: 'Inspect runtime sessions',
        requirements: [],
        questions: [],
        materials: [],
        inputFiles: [],
        executionIds: [],
        results: [],
      },
    ],
    executions: [],
    operations: [],
    connections: [],
    savedRevision: null,
    appliedRevision: null,
  };
  writeFileSync(profilePath, JSON.stringify(profile) + '\n');
  const storage = new ProductSQLite(profilePath, credentialsPath);
  storage.open({ profilePath, credentialsPath, fresh: false }, undefined, 'unit-installation-1');
  storage.recoverRuntimeSessions();
  t.after(() => {
    storage.close();
    rmSync(root, { recursive: true, force: true });
  });
  return { storage, workspace, botId, conversationId, otherConversationId, activityId };
}

test('TerminalProjection strips ANSI sequences, normalizes carriage returns, and handles split UTF-8', () => {
  const proj = new TerminalProjection();
  assert.equal(proj.push(Buffer.from('\x1b[31mred\x1b[0m\r\n')), 'red\n');
  assert.equal(proj.push(Buffer.from('\x1b]0;title\x07step1\rstep2\x1b')), 'step1\nstep2');
  assert.equal(proj.push(Buffer.from('[2Kdone')), 'done');
  const cjk = Buffer.from('你好');
  assert.equal(proj.push(cjk.subarray(0, 2)), '');
  assert.equal(proj.push(Buffer.concat([cjk.subarray(2), Buffer.from('\r')])), '你好');
  assert.equal(proj.push(), '\n');
});

test('OutputRing preserves UTF-8 boundaries across reads, evictions, empty writes, and clears', () => {
  const ring = new OutputRing();
  assert.equal(ring.append('pty', Buffer.alloc(0)), 0);
  assert.equal(ring.firstAvailableOffset, 0);
  assert.equal(ring.nextOffset, 0);

  ring.append('stdout', Buffer.from('alpha '));
  ring.append('stdout', Buffer.from('beta'));
  ring.append('stderr', Buffer.from('err'));
  const combined = ring.read(0, 1024);
  assert.deepEqual(combined.chunks, [
    { stream: 'stdout', offset: 0, nextOffset: 10, text: 'alpha beta' },
    { stream: 'stderr', offset: 10, nextOffset: 13, text: 'err' },
  ]);

  const evictedRing = new OutputRing();
  evictedRing.append('pty', Buffer.concat([Buffer.from('中'), Buffer.alloc(256 * 1024 - 4, 'a')]));
  evictedRing.append('pty', Buffer.from('中'));
  assert.equal(evictedRing.firstAvailableOffset, 3);
  const sliced = evictedRing.read(256 * 1024 - 2, 2);
  assert.equal(sliced.chunks[0].text, 'a');
  assert.equal(sliced.nextOffset, 256 * 1024 - 1);
  const withRune = evictedRing.read(256 * 1024 - 1, 8);
  assert.equal(withRune.chunks[0].text, '中');

  evictedRing.clear();
  assert.equal(evictedRing.firstAvailableOffset, evictedRing.nextOffset);
  assert.deepEqual(evictedRing.read(0, 64).chunks, []);
});

test('LiteralMatch handles split chunks, overlapping KMP prefixes, and startOffset', () => {
  const overlap = new LiteralMatch('aab');
  assert.equal(overlap.feed(Buffer.from('aa'), 0, 0), undefined);
  assert.equal(overlap.feed(Buffer.from('ab'), 2, 0), 1);

  const bounded = new LiteralMatch('ready');
  assert.equal(bounded.feed(Buffer.from('ready early ready late'), 0, 10), 12);
});

test('RuntimeSessions enforces actor visibility, generation checks, read gaps, and watch arm validation', t => {
  const { storage, workspace, botId, conversationId, otherConversationId, activityId } = setupSessionStorage(t);
  let secrets = ['sk-session-secret-1234'];
  const events = [];
  const sessions = new RuntimeSessions(storage, () => secrets, (event, value) => events.push({ event, value }), () => {});
  const nowIso = '2026-11-27T12:00:00.000Z';

  const convRow = {
    sessionId: 'sess-conv-1',
    ownerBotId: botId,
    originConversationId: conversationId,
    creatingOperationId: 'op-1',
    generation: 2,
    revision: 1,
    commandSummary: 'echo ok',
    cwd: workspace,
    mode: 'pipe',
    state: 'exited',
    transportStatus: 'eof',
    firstAvailableOffset: 12,
    nextOffset: 24,
    eof: true,
    evictedBytes: 12,
    watches: [],
    createdAt: nowIso,
    updatedAt: nowIso,
    settledAt: nowIso,
    epoch: storage.sessionEpoch,
  };
  const actRow = {
    ...convRow,
    sessionId: 'sess-act-1',
    originConversationId: conversationId,
    originActivityId: activityId,
  };
  const restoredRow = {
    ...convRow,
    sessionId: 'sess-restored-1',
    epoch: undefined,
  };
  for (const session of [convRow, actRow, restoredRow]) {
    storage.saveRuntimeSession({ session });
  }

  assert.equal(sessions.get(botId, 'sess-conv-1', { botId, conversationId }, true).sessionId, 'sess-conv-1');
  assert.throws(() => sessions.get(botId, 'sess-conv-1', { botId, conversationId: otherConversationId }, true), { code: 'scope_denied' });
  assert.throws(() => sessions.get(botId, 'sess-act-1', { botId, conversationId }, true), { code: 'scope_denied' });
  assert.equal(sessions.get(botId, 'sess-act-1', { botId, conversationId }, false).sessionId, 'sess-act-1');
  assert.throws(() => sessions.get(botId, 'missing-session'), { code: 'not_found' });

  const current = sessions.get(botId, 'sess-conv-1');
  assert.equal(current.firstAvailableOffset, 24);
  assert.equal(current.evictedBytes, 24);
  assert.throws(() => sessions.generation(current, 1), { code: 'stale_generation' });

  const readGap = sessions.read(current, 4, 16);
  assert.equal(readGap.gap, true);
  assert.equal(readGap.gapBytes, 20);
  assert.equal(readGap.highWaterOffset, 24);

  assert.throws(() => sessions.arm(restoredRow, { on_exit: true }), { code: 'scope_denied' });
  assert.throws(() => sessions.arm(current, {}), { code: 'invalid_params' });
  assert.throws(() => sessions.arm(current, { match_literal: 'token sk-session-secret-1234' }), { code: 'invalid_params' });
  assert.throws(() => sessions.arm(current, { on_exit: true, start_offset: 11 }), { code: 'invalid_params' });
  assert.throws(() => sessions.arm(current, { on_exit: true, start_offset: 25 }), { code: 'invalid_params' });

  const armed = sessions.arm(current, { on_exit: true });
  assert.equal(armed.state, 'fired');
  assert.equal(armed.firedKind, 'exit');
  assert.equal(events.some(entry => entry.event === 'session.changed'), true);
});
