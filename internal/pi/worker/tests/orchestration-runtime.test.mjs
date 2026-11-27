import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';
import { pathToFileURL } from 'node:url';

const distDir = process.env.NAGI_WORKER_DIST || join(dirname(dirname(process.execPath)), 'dist');
const load = name => import(pathToFileURL(join(distDir, name)).href);

const [
  { OrchestrationRuntime },
  { ProductSQLite },
  { decodeProductBackup, inspectProductRecovery },
  { anchoredTranscriptEntries, liveTranscript, turnEntryType },
  { canonicalJSON, parseProductRequest, productEventSchema, productResults },
  { Value },
] = await Promise.all([
  load('orchestration-runtime.js'),
  load('product-sqlite.js'),
  load('product-profile.js'),
  load('product-transcript.js'),
  load('protocol.js'),
  import(pathToFileURL(join(dirname(distDir), 'node_modules', 'typebox', 'build', 'value', 'index.mjs')).href),
]);

function setupStorage(t) {
  const root = mkdtempSync(join(realpathSync(tmpdir()), 'nagi-orch-unit-'));
  const profileDir = join(root, 'profile');
  const workspace = join(root, 'workspace');
  mkdirSync(profileDir, { recursive: true });
  mkdirSync(workspace, { recursive: true });
  const profilePath = join(profileDir, 'contacts.json');
  const credentialsPath = join(profileDir, 'connection.json');
  const botId = 'bot-orch-1';
  const siblingBotId = 'bot-orch-2';
  const conversationId = 'conv-orch-1';
  const siblingConvId = 'conv-orch-2';
  const profile = {
    version: 1,
    bots: [
      {
        botId,
        currentConversationId: conversationId,
        profileRevision: 1,
        archived: false,
        profile: { name: 'OrchOne', role: 'Observer', purpose: 'Unit test', instructions: 'Observe.', workspace, codingTools: null, toolPreset: 'workspace' },
      },
      {
        botId: siblingBotId,
        currentConversationId: siblingConvId,
        profileRevision: 1,
        archived: false,
        profile: { name: 'OrchTwo', role: 'Worker', purpose: 'Sibling', instructions: 'Work.', workspace, codingTools: null, toolPreset: 'workspace' },
      },
    ],
    conversations: [
      { conversationId, botId, revision: 1, draft: { revision: 0, text: '', materials: [] } },
      { conversationId: siblingConvId, botId: siblingBotId, revision: 1, draft: { revision: 0, text: '', materials: [] } },
    ],
    activities: [],
    executions: [
      { executionId: 'exec-done-1', revision: 1, state: 'completed', operationId: 'op-exec-1', input: { revision: 0, text: 'hi', materials: [] } },
    ],
    operations: [
      {
        method: 'conversation.send',
        fingerprint: createHash('sha256').update('{}').digest('hex'),
        receipt: { operationId: 'op-exec-1', state: 'accepted', target: { conversationId }, execution: { executionId: 'exec-done-1', revision: 1, state: 'completed' } },
        reply: { operationId: 'op-exec-1', state: 'accepted', target: { conversationId }, execution: { executionId: 'exec-done-1', revision: 1, state: 'completed' } },
      },
    ],
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
  return { root, workspace, profilePath, credentialsPath, botId, siblingBotId, conversationId, siblingConvId, storage };
}

test('protocol validates BG-3 commands, strict results/events, and additionalProperties rejections', () => {
  const sendReq = (method, params) => parseProductRequest(Buffer.from(JSON.stringify({ v: 1, type: 'request', id: 'req-1', method, params })));

  assert.equal(sendReq('conversation.timeline', { conversationId: 'c1', limit: 25 }).command.method, 'conversation.timeline');
  assert.equal(sendReq('orchestration.list', { botId: 'b1', states: ['active', 'waiting_decision'], limit: 10 }).command.method, 'orchestration.list');
  assert.equal(sendReq('orchestration.get', { botId: 'b1', mandateId: 'm1' }).command.method, 'orchestration.get');
  assert.equal(sendReq('orchestration.decide', { botId: 'b1', mandateId: 'm1', decisionId: 'd1', expectedRevision: 1, outcome: 'approve', selectedOptionId: 'opt1', operationId: 'op1' }).command.method, 'orchestration.decide');
  assert.equal(sendReq('orchestration.stop', { botId: 'b1', mandateId: 'm1', expectedRevision: 2, generation: 1, runId: 'r1', operationId: 'op2' }).command.method, 'orchestration.stop');

  assert.throws(() => sendReq('conversation.timeline', { conversationId: 'c1', extra: true }), { code: 'invalid_params' });
  assert.throws(() => sendReq('orchestration.list', { botId: 'b1', unexpected: 1 }), { code: 'invalid_params' });
  assert.throws(() => sendReq('orchestration.get', { botId: 'b1', mandateId: 'm1', extra: 1 }), { code: 'invalid_params' });
  assert.throws(() => sendReq('orchestration.decide', { botId: 'b1', mandateId: 'm1', decisionId: 'd1', expectedRevision: 0, outcome: 'approve', operationId: 'op1' }), { code: 'invalid_params' });
  assert.throws(() => sendReq('orchestration.stop', { botId: 'b1', mandateId: 'm1', operationId: 'op2', extra: true }), { code: 'invalid_params' });

  const validPendingSend = {
    operationId: 'op-send-1',
    state: 'pending',
    target: { conversationId: 'c1' },
    execution: { executionId: 'e1', revision: 1, state: 'starting' },
    result: { queued: true, queuePosition: 1, reason: 'provider_capacity', draftRevision: 2, route: { model: 'gpt-4.1' } },
  };
  assert.equal(Value.Check(productResults['conversation.send'], validPendingSend), true);

  const validTimelineChanged = {
    v: 1,
    type: 'event',
    streamId: 's1',
    seq: 1,
    event: 'conversation.timelineChanged',
    data: { conversationId: 'c1', timelineRevision: 1, noticeId: 'n1', at: '2026-11-27T00:00:00.000Z' },
  };
  assert.equal(Value.Check(productEventSchema, validTimelineChanged), true);
  assert.equal(Value.Check(productEventSchema, { ...validTimelineChanged, data: { ...validTimelineChanged.data, extra: 1 } }), false);

  const validOrchChanged = {
    v: 1,
    type: 'event',
    streamId: 's1',
    seq: 2,
    event: 'orchestration.changed',
    data: { botId: 'b1', change: 'queue_updated', conversationId: 'c1', executionId: 'e1', operationId: 'op1', at: '2026-11-27T00:00:00.000Z' },
  };
  assert.equal(Value.Check(productEventSchema, validOrchChanged), true);
  assert.equal(Value.Check(productEventSchema, { ...validOrchChanged, data: { ...validOrchChanged.data, badField: true } }), false);

  const validTimeline = {
    conversationId: 'c1',
    timelineRevision: 2,
    transcriptAvailable: false,
    items: [
      {
        kind: 'notice',
        messageId: 'n1',
        timestamp: '2026-11-27T00:00:00.000Z',
        anchorSeq: 1,
        text: 'Notice body',
        notice: {
          noticeId: 'n1',
          botId: 'b1',
          mandateId: 'm1',
          runId: 'r1',
          factDigest: 'a'.repeat(64),
          anchorKind: 'standalone',
          sourceRefs: [{ kind: 'watch', id: 'w1', label: 'Daily check' }],
          urgency: 'normal',
          dormant: false,
          createdAt: '2026-11-27T00:00:00.000Z',
        },
      },
    ],
    nextCursor: null,
  };
  assert.equal(Value.Check(productResults['conversation.timeline'], validTimeline), true);
  assert.equal(Value.Check(productResults['conversation.timeline'], { ...validTimeline, extra: 1 }), false);
});

test('OrchestrationRuntime handles request budget, checkpoint fallback rotation, causal backoff, and sibling isolation', async t => {
  const prevHold = process.env.NAGI_ORCHESTRATION_HOLD;
  delete process.env.NAGI_ORCHESTRATION_HOLD;
  t.after(() => {
    if (prevHold === undefined) delete process.env.NAGI_ORCHESTRATION_HOLD;
    else process.env.NAGI_ORCHESTRATION_HOLD = prevHold;
  });

  const { storage, botId, siblingBotId, conversationId, siblingConvId, workspace } = setupStorage(t);
  let now = Date.parse('2026-11-27T10:00:00.000Z');
  const changes = [];
  const timelineEvents = [];
  let fatal = 0;
  let sessionHistoryBytes = 0;
  let contextTokensBudget = 2000;
  const openSessions = [];
  const prompts = [];
  const handlers = new Map();

  const runtime = new OrchestrationRuntime(storage, {
    now: () => now,
    freeSlot: () => true,
    slotReleased: () => {},
    prepare: async (mandate, _tools, _bytes) => ({
      params: {
        contextId: randomUUID(),
        botId: mandate.ownerBotId,
        purpose: 'orchestration',
        inMemory: true,
        cwd: workspace,
        instructions: 'Process orchestration events safely.',
        selectionRevision: 1,
        used: false,
        codingTools: [],
        domainTools: null,
        configuration: {
          wireApi: 'openai-completions',
          baseUrl: 'http://127.0.0.1:9/v1',
          apiKey: 'unit-secret-xyz',
          credentialSource: 'unit-src',
          model: 'gpt-4.1-mini',
          contextTokens: contextTokensBudget,
          outputTokens: 200,
        },
      },
      route: { role: 'utility', resolvedModel: 'gpt-4.1-mini' },
      authorize: () => {},
      attempt: () => ({ headers() {}, settle() {} }),
    }),
    open: async (params, tools) => {
      let closed = false;
      const sess = {
        sessionRef: '',
        cwd: params.cwd,
        historyBytes: () => sessionHistoryBytes,
        close: async () => { closed = true; },
        get isClosed() { return closed; },
        start: (executionId, draft, observers) => {
          queueMicrotask(async () => {
            prompts.push({ text: draft.text, botId: params.botId });
            try {
              observers.authorizeProvider();
              observers.checkRequestBytes(Buffer.byteLength(params.instructions) + Buffer.byteLength(draft.text));
              observers.onModelSettlement({ inputTokens: 100, outputTokens: 20, totalTokens: 120, costUsd: 0.001 });
              const payloadLine = draft.text.split('\n')[1];
              const parsed = JSON.parse(payloadLine);
              const fn = handlers.get(parsed.mandateId);
              const reply = fn ? await fn(parsed, tools, observers, executionId) : { text: 'Raw assistant text not stored' };
              observers.settled('completed', reply.text ?? 'Private reply');
            } catch (err) {
              observers.settled('failed', '', err.code ?? err.message);
            }
          });
        },
        stop: () => {},
        checkSteer: () => {},
        steer: () => {},
        nominatePreference: async () => undefined,
      };
      openSessions.push(sess);
      return sess;
    },
    ownerAvailable: () => true,
    activeForeground: () => undefined,
    redact: (text, limit) => text.replaceAll('unit-secret-xyz', '[credential]').slice(0, limit),
    emit: change => changes.push(change),
    timelineChanged: change => timelineEvents.push(change),
    disarm: () => {},
    fatal: () => { fatal++; },
  });
  runtime.start();
  t.after(async () => { await runtime.drain(); });

  const createWatch = (targetBotId, targetConvId, watchId, purpose) => {
    storage.writeWatches({
      watches: [{
        watchId, ownerBotId: targetBotId, targetConversationId: targetConvId, kind: 'schedule',
        label: purpose, purpose, schedule: { cadenceKind: 'interval', intervalSeconds: 10, timeZone: 'UTC', jitterSeconds: 0 },
        revision: 1, source: 'user', state: 'active', armed: true, nextDueAt: new Date(now + 10_000).toISOString(),
        consecutiveFailures: 0, createdAt: new Date(now).toISOString(), updatedAt: new Date(now).toISOString(),
      }],
    });
  };
  const enqueueEvent = (targetBotId, targetConvId, watchId, occurrenceId, payload) => {
    storage.orchestrationTransaction(() => {
      storage.insertInbox({
        ownerBotId: targetBotId,
        targetConversationId: targetConvId,
        mandateId: 'watch:' + watchId,
        sourceKind: 'schedule_tick',
        sourceId: watchId,
        occurrenceId,
        at: new Date(now).toISOString(),
        payload,
      });
    });
    runtime.wake();
  };
  const waitSettledRun = async (mandateId, wantWakes) => {
    const deadline = Date.now() + 5000;
    while (Date.now() < deadline) {
      const m = storage.mandate(mandateId);
      const sum = storage.orchestrationSummary(m);
      if (sum.metrics.totalWakes >= wantWakes && !m.currentRunId) return sum;
      await delay(10);
    }
    throw new Error(`Timed out waiting for ${mandateId} wake #${wantWakes}`);
  };

  createWatch(botId, conversationId, 'w-stuck', 'Stuck with <angle> tag');
  createWatch(siblingBotId, siblingConvId, 'w-sibling', 'Sibling active watch');

  handlers.set('watch:w-stuck', async (event, tools, observers) => {
    const finishTool = tools.find(tool => tool.name === 'orchestration_finish');
    const step = prompts.filter(p => p.text.includes('watch:w-stuck')).length;
    const actionClass = step === 2 ? 'err_b' : 'err_a';
    const callId = 'call-finish-' + step;
    observers.admitTool('orchestration_finish', callId, {});
    await finishTool.execute(callId, {
      summary: 'No change unit-secret-xyz',
      observedStateHash: step === 2 ? 'hash_b' : 'hash_a',
      actionClass,
      progressMade: false,
    });
    observers.toolFinished('orchestration_finish', callId, false);
    return { text: 'Private raw failure trace should stay hidden' };
  });

  enqueueEvent(botId, conversationId, 'w-stuck', 'occ-stuck-1', { tick: 1 });
  const stuck1 = await waitSettledRun('watch:w-stuck', 1);
  assert.equal(stuck1.metrics.consecutiveNoProgress, 1);
  assert.equal(stuck1.metrics.backoffDelayMs, 0);
  assert(prompts[0].text.includes('\\u003cangle>'));

  now += 1000;
  enqueueEvent(botId, conversationId, 'w-stuck', 'occ-stuck-2', { tick: 2 });
  const stuck2 = await waitSettledRun('watch:w-stuck', 2);
  assert.equal(stuck2.metrics.consecutiveNoProgress, 1);

  now += 1000;
  enqueueEvent(botId, conversationId, 'w-stuck', 'occ-stuck-3', { tick: 3 });
  const stuck3 = await waitSettledRun('watch:w-stuck', 3);
  assert.equal(stuck3.metrics.consecutiveNoProgress, 2);
  assert.equal(stuck3.metrics.backoffDelayMs, 1000);
  assert.equal(stuck3.metrics.backoffReason, 'no_progress');
  assert.equal(stuck3.lastDisposition, 'notified');

  now += 1500;
  enqueueEvent(botId, conversationId, 'w-stuck', 'occ-stuck-4', { tick: 4 });
  const stuck4 = await waitSettledRun('watch:w-stuck', 4);
  assert.equal(stuck4.metrics.consecutiveNoProgress, 3);
  assert.equal(stuck4.metrics.backoffDelayMs, 2000);
  assert.equal(stuck4.lastDisposition, 'silent');

  const notices = storage.conversationNotices(conversationId);
  assert.equal(notices.length, 1);

  enqueueEvent(siblingBotId, siblingConvId, 'w-sibling', 'occ-sib-1', { step: 1 });
  const sib1 = await waitSettledRun('watch:w-sibling', 1);
  assert.equal(sib1.metrics.backoffDelayMs, 0);
  assert.equal(sib1.lastDisposition, 'silent');
  const sibCheckpoint = storage.checkpoint('watch:w-sibling');
  assert(!sibCheckpoint.workingSummary.includes('Private assistant text'));
  assert(sibCheckpoint.workingSummary.includes('silent: schedule_tick'));

  let notifySecondErr;
  let notifyIds = [];
  handlers.set('watch:w-sibling', async (_event, tools, observers) => {
    const notifyTool = tools.find(tool => tool.name === 'conversation_notify');
    const decisionTool = tools.find(tool => tool.name === 'orchestration_request_decision');
    for (const callId of ['call-n-1', 'call-n-2']) {
      observers.admitTool('conversation_notify', callId, {});
      const out = await notifyTool.execute(callId, { text: 'Deduplicated notice', urgency: 'normal' });
      observers.toolFinished('conversation_notify', callId, Boolean(out.isError));
      notifyIds.push(JSON.parse(out.content[0].text).noticeId);
    }
    observers.admitTool('orchestration_request_decision', 'call-dec-conflict', {});
    const decOut = await decisionTool.execute('call-dec-conflict', { summary: 'Conflict after notice' });
    observers.toolFinished('orchestration_request_decision', 'call-dec-conflict', Boolean(decOut.isError));
    notifySecondErr = JSON.parse(decOut.content[0].text);
    return { text: 'Notice done' };
  });
  now += 1000;
  enqueueEvent(siblingBotId, siblingConvId, 'w-sibling', 'occ-sib-notify', { step: 'n' });
  const sibNotify = await waitSettledRun('watch:w-sibling', 2);
  assert.equal(notifyIds[0], notifyIds[1]);
  assert.equal(notifySecondErr.code, 'invalid_state');
  assert.equal(sibNotify.lastDisposition, 'notified');
  assert.equal(storage.conversationNotices(siblingConvId).length, 1);

  handlers.delete('watch:w-sibling');

  now += 5000;
  handlers.set('watch:w-stuck', async (_event, tools, observers) => {
    const finishTool = tools.find(tool => tool.name === 'orchestration_finish');
    const callId = 'call-progress';
    observers.admitTool('orchestration_finish', callId, {});
    await finishTool.execute(callId, { summary: 'Resolved cleanly', observedStateHash: 'changed', progressMade: true });
    observers.toolFinished('orchestration_finish', callId, false);
    return { text: 'OK' };
  });
  enqueueEvent(botId, conversationId, 'w-stuck', 'occ-stuck-5', { tick: 5 });
  const stuck5 = await waitSettledRun('watch:w-stuck', 5);
  assert.equal(stuck5.metrics.consecutiveNoProgress, 0);
  assert.equal(stuck5.metrics.backoffDelayMs, 0);

  sessionHistoryBytes = 6000;
  contextTokensBudget = 1800;
  const sessionsBeforeRotate = openSessions.length;
  enqueueEvent(siblingBotId, siblingConvId, 'w-sibling', 'occ-sib-2', { step: 2 });
  const sibRotated = await waitSettledRun('watch:w-sibling', 3);
  assert.equal(sibRotated.generation, 2);
  assert.equal(openSessions.length, sessionsBeforeRotate + 1);
  assert.equal(openSessions[1].isClosed, true);
  assert.equal(fatal, 0);
});

test('Timeline notice interleaving, anchoredTranscriptEntries, and redacted dormant backup inspection work end-to-end', async t => {
  const { storage, botId, siblingBotId, conversationId, siblingConvId, profilePath, credentialsPath, workspace } = setupStorage(t);
  const nowIso = '2026-11-27T11:00:00.000Z';
  const mandate = {
    mandateId: 'm-timeline-1',
    ownerBotId: botId,
    targetConversationId: conversationId,
    sourceKind: 'timer',
    sourceId: 't-1',
    purpose: 'Track deploy sk-secret-value-1234',
    state: 'active',
    revision: 1,
    generation: 1,
    dormant: false,
    createdAt: nowIso,
    updatedAt: nowIso,
  };
  storage.orchestrationTransaction(() => {
    storage.putMandate(mandate);
    storage.putCheckpoint({ mandateId: mandate.mandateId, generation: 1, revision: 1, workingSummary: 'Summary with sk-secret-value-1234', updatedAt: nowIso });
    storage.putRun({
      runId: 'run-notice-1', mandateId: mandate.mandateId, ownerBotId: botId, generation: 1, state: 'completed',
      queueDelayMs: 0, coalescedCount: 1, calls: 1, inputTokens: 10, outputTokens: 5, totalTokens: 15, costUsd: 0, startedAt: nowIso, settledAt: nowIso,
    });
    const rev1 = storage.appendNotice({
      noticeId: 'notice-1',
      conversationId,
      botId,
      mandateId: mandate.mandateId,
      runId: 'run-notice-1',
      factDigest: 'b'.repeat(64),
      anchorKind: 'post_settlement',
      anchorExecutionId: 'exec-done-1',
      text: 'Post-settlement update sk-secret-value-1234',
      sourceRefs: [{ kind: 'watch', id: 't-1', label: 'Label sk-secret-value-1234' }],
      urgency: 'normal',
      dormant: false,
      createdAt: nowIso,
    });
    assert.equal(rev1, undefined);
    assert.equal(storage.conversationNotices(conversationId).length, 0);
    storage.putDecision({
      decisionId: 'dec-1',
      mandateId: mandate.mandateId,
      ownerBotId: botId,
      runId: 'run-notice-1',
      revision: 1,
      state: 'open',
      kind: 'approval',
      purpose: 'Deploy sk-secret-value-1234',
      summary: 'Confirm sk-secret-value-1234',
      targetConversationId: conversationId,
      options: [{ id: 'opt-1', label: 'Proceed sk-secret-value-1234', description: 'Details sk-secret-value-1234' }],
      dormant: false,
      createdAt: nowIso,
      updatedAt: nowIso,
    });
  });

  const currentProfile = JSON.parse(readFileSync(profilePath, 'utf8'));
  storage.write(currentProfile, currentProfile, undefined, undefined, undefined, undefined, {
    timelineOutcomes: [{ executionId: 'exec-done-1', state: 'unknown' }],
  });
  const finalized = storage.conversationNotices(conversationId);
  assert.equal(finalized.length, 1);
  assert.equal(finalized[0].anchorOutcome, 'unknown');
  assert.equal(storage.timelineRevision(conversationId), 1);

  const entries = [
    { type: 'custom', id: 'c-turn', parentId: null, timestamp: '2026-11-27T10:59:58.000Z', customType: turnEntryType, data: { runId: 'exec-done-1', text: 'hi' } },
    { type: 'message', id: 'm-user', parentId: 'c-turn', timestamp: '2026-11-27T10:59:58.500Z', message: { role: 'user', content: 'hi', timestamp: 1 } },
    { type: 'message', id: 'm-asst', parentId: 'm-user', timestamp: '2026-11-27T10:59:59.000Z', message: { role: 'assistant', content: [{ type: 'text', text: 'ok' }], timestamp: 2 } },
  ];
  const anchored = [];
  for await (const row of anchoredTranscriptEntries(liveTranscript('unit', entries))) {
    anchored.push({ id: row.entry.id, executionId: row.executionId });
  }
  assert.deepEqual(anchored, [
    { id: 'c-turn', executionId: 'exec-done-1' },
    { id: 'm-user', executionId: 'exec-done-1' },
    { id: 'm-asst', executionId: 'exec-done-1' },
  ]);

  const exported = storage.captureBackup(text => text.replaceAll('sk-secret-value-1234', '[credential]'));
  const serialized = JSON.stringify({ ...exported.profile, recovery: { id: randomUUID() }, backup: exported.backup });
  assert(!serialized.includes('sk-secret-value-1234'));
  assert(!serialized.includes('orchestrationRuns'));
  assert(!serialized.includes('orchestrationAdmissions'));

  const decoded = decodeProductBackup(JSON.parse(serialized));
  assert.equal(decoded.backup.orchestrationMandates[0].state, 'restored_readonly');
  assert.equal(decoded.backup.orchestrationMandates[0].dormant, true);
  assert.equal(decoded.backup.orchestrationDecisions[0].dormant, true);
  assert.equal(decoded.backup.conversationNotices[0].dormant, true);

  const tampered = JSON.parse(serialized);
  tampered.backup.conversationNotices[0].conversationId = siblingConvId;
  assert.throws(() => decodeProductBackup(tampered), { code: 'profile_unavailable' });

  const counts = inspectProductRecovery(JSON.parse(serialized));
  assert.deepEqual(counts, {
    contacts: 2,
    activities: 0,
    timers: 0,
    schedules: 0,
    dormantWatches: 0,
    notices: 1,
    decisions: 1,
    dormantMandates: 1,
  });

  const restoredDir = join(dirname(workspace), 'restored');
  mkdirSync(restoredDir, { recursive: true });
  const restoredProfile = join(restoredDir, 'contacts.json');
  writeFileSync(restoredProfile, serialized + '\n');
  const restoredDB = new ProductSQLite(restoredProfile, credentialsPath);
  restoredDB.open({ profilePath: restoredProfile, credentialsPath, fresh: false }, undefined, 'unit-installation-2');
  assert.equal(restoredDB.mandate(mandate.mandateId).state, 'restored_readonly');
  assert.equal(restoredDB.readyMandates(nowIso).length, 0);
  restoredDB.close();
});
