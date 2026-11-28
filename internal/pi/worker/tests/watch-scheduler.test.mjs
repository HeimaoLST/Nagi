import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';
import { pathToFileURL } from 'node:url';

const distDir = process.env.NAGI_WORKER_DIST;
if (!distDir) {
  throw new Error('NAGI_WORKER_DIST must point to the built worker dist directory');
}

const load = name => import(pathToFileURL(join(distDir, name)).href);

const [
  { WatchScheduler },
  { ProductSQLite },
  { decodeProductBackup, inspectProductRecovery },
  { parseProductRequest, validateWatchDefinition },
  { SkillSandbox },
] = await Promise.all([
  load('watch-scheduler.js'),
  load('product-sqlite.js'),
  load('product-profile.js'),
  load('protocol.js'),
  load('skill-sandbox.js'),
]);

function setupHarness(t, initialNow) {
  const previousClockFile = process.env.NAGI_TEST_CLOCK_FILE;
  delete process.env.NAGI_TEST_CLOCK_FILE;
  const root = mkdtempSync(join(realpathSync(tmpdir()), 'xuhu-watch-unit-'));
  const profileDir = join(root, 'profile');
  const workspace = join(root, 'workspace');
  mkdirSync(profileDir, { recursive: true });
  mkdirSync(workspace, { recursive: true });
  const profilePath = join(profileDir, 'contacts.json');
  const credentialsPath = join(profileDir, 'connection.json');
  const botId = 'bot-unit-1';
  const conversationId = 'conv-unit-1';
  const profile = {
    version: 1,
    bots: [
      {
        botId,
        currentConversationId: conversationId,
        profileRevision: 1,
        archived: false,
        profile: {
          name: 'Watch Unit Bot',
          role: 'Observer',
          purpose: 'Verify scheduler determinism',
          instructions: 'Inspect workspace state',
          workspace,
          codingTools: null,
          toolPreset: 'workspace',
        },
      },
    ],
    conversations: [
      {
        conversationId,
        botId,
        revision: 1,
        draft: { revision: 0, text: '', materials: [] },
      },
    ],
    activities: [],
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

  let now = initialNow;
  const emitted = [];
  let fatalFailures = 0;
  const scheduler = new WatchScheduler(
    storage,
    {
      installationId: 'unit-installation-1',
      privatePaths: [profileDir],
      sandbox: new SkillSandbox(),
      owner: id => {
        if (id !== botId) throw new Error(`unknown bot ${id}`);
        return { botId, currentConversationId: conversationId, archived: false, workspace };
      },
      ownsConversation: (ownerId, convId) => ownerId === botId && convId === conversationId,
      skill: () => {
        throw new Error('skill lookup not configured in unit harness');
      },
      keys: () => [],
      emit: change => emitted.push(change),
      failure: () => {
        fatalFailures++;
      },
    },
    () => now,
  );

  t.after(async () => {
    await scheduler.drain();
    storage.close();
    if (previousClockFile === undefined) delete process.env.NAGI_TEST_CLOCK_FILE;
    else process.env.NAGI_TEST_CLOCK_FILE = previousClockFile;
    rmSync(root, { recursive: true, force: true });
  });

  return {
    root,
    workspace,
    botId,
    conversationId,
    storage,
    scheduler,
    emitted,
    getNow: () => now,
    setNow: value => {
      now = value;
    },
    advance: ms => {
      now += ms;
      return now;
    },
    fatalFailures: () => fatalFailures,
  };
}

function compileUnitObserver(workspace) {
  const source = `#include <stdio.h>
#include <string.h>
#include <unistd.h>
int main(void) {
  FILE *f = fopen("state.txt", "r");
  if (!f) return 6;
  int ch = fgetc(f);
  fclose(f);
  if (ch == 'T') { sleep(5); return 12; }
  if (ch == 'F') { fputs("unit failure\\n", stderr); return 7; }
  if (ch == 'O') {
    char buf[4096];
    memset(buf, 'y', sizeof(buf));
    for (int i = 0; i < 300; i++) fwrite(buf, 1, sizeof(buf), stdout);
    return 0;
  }
  char buf[1024];
  memset(buf, 'x', sizeof(buf));
  for (int i = 0; i < 72; i++) fwrite(buf, 1, sizeof(buf), stdout);
  printf("tail %c\\n", ch);
  return 0;
}
`;
  const dir = dirnameOfTempSource(workspace);
  const cPath = join(dir, 'observer.c');
  const binPath = join(dir, 'observer');
  writeFileSync(cPath, source);
  execFileSync('/usr/bin/xcrun', ['clang', '-std=c11', '-O2', '-Wall', '-Wextra', '-Werror', cPath, '-o', binPath]);
  return binPath;
}

function dirnameOfTempSource(workspace) {
  return join(workspace, '..');
}

async function waitOccurrenceSettled(scheduler, botId, watchId, scheduledAt, timeoutMs = 8000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const detail = scheduler.detail(botId, watchId);
    const match = detail.recentOccurrences.find(row => row.scheduledAt === scheduledAt && row.state !== 'running');
    if (match) return { detail, occurrence: match };
    await delay(15);
  }
  throw new Error(`Timed out waiting for occurrence at ${scheduledAt}`);
}

test('validateWatchDefinition and parseProductRequest enforce cadence, timezone, and executable arg bounds', () => {
  assert.throws(
    () => parseProductRequest(Buffer.from(JSON.stringify({
      v: 1,
      type: 'request',
      id: '1',
      method: 'watch.save',
      params: {
        botId: 'b1',
        kind: 'schedule',
        purpose: 'Too fast',
        schedule: { cadenceKind: 'interval', intervalSeconds: 9, timeZone: 'UTC' },
        operationId: 'op-1',
      },
    }))),
    err => err.code === 'invalid_params',
  );

  assert.throws(
    () => parseProductRequest(Buffer.from(JSON.stringify({
      v: 1,
      type: 'request',
      id: '2',
      method: 'watch.save',
      params: {
        botId: 'b1',
        kind: 'schedule',
        purpose: 'Bad timeout',
        schedule: { cadenceKind: 'interval', intervalSeconds: 10, timeZone: 'UTC' },
        precheck: { kind: 'executable', executable: '/usr/bin/true', timeoutMs: 999 },
        operationId: 'op-2',
      },
    }))),
    err => err.code === 'invalid_params',
  );

  assert.throws(
    () => validateWatchDefinition({
      kind: 'schedule',
      purpose: 'Fixed offset zone',
      schedule: { cadenceKind: 'calendar', frequency: 'daily', hour: 9, minute: 0, timeZone: '+08:00' },
    }),
    err => err.code === 'invalid_params',
  );

  assert.throws(
    () => validateWatchDefinition({
      kind: 'schedule',
      purpose: 'Unknown zone',
      schedule: { cadenceKind: 'calendar', frequency: 'daily', hour: 9, minute: 0, timeZone: 'Invalid/Timezone' },
    }),
    err => err.code === 'invalid_params',
  );

  assert.throws(
    () => validateWatchDefinition({
      kind: 'schedule',
      purpose: 'Daily without hour',
      schedule: { cadenceKind: 'calendar', frequency: 'daily', minute: 15, timeZone: 'UTC' },
    }),
    err => err.code === 'invalid_params',
  );

  assert.throws(
    () => validateWatchDefinition({
      kind: 'timer',
      purpose: 'Timer with schedule',
      timer: { fireAt: '2026-03-08T00:00:00.000Z' },
      schedule: { cadenceKind: 'interval', intervalSeconds: 10, timeZone: 'UTC' },
    }),
    err => err.code === 'invalid_params',
  );

  assert.throws(
    () => validateWatchDefinition({
      kind: 'schedule',
      purpose: 'NUL byte in arg',
      schedule: { cadenceKind: 'interval', intervalSeconds: 10, timeZone: 'UTC' },
      precheck: { kind: 'executable', executable: '/usr/bin/true', allowedArgs: ['bad\0arg'] },
    }),
    err => err.code === 'invalid_params',
  );
});

test('WatchScheduler handles DST spring-forward, fall-back, weekly DST gap, and half-hour zones without double firing', t => {
  const h = setupHarness(t, Date.parse('2026-03-07T06:00:00.000Z'));

  const spring = h.scheduler.save({
    botId: h.botId,
    kind: 'schedule',
    purpose: 'Spring forward daily 02:30 NY',
    schedule: { cadenceKind: 'calendar', frequency: 'daily', hour: 2, minute: 30, timeZone: 'America/New_York' },
    operationId: randomUUID(),
  }).result.watch;
  assert.equal(spring.nextDueAt, '2026-03-07T07:30:00.000Z');

  h.setNow(Date.parse('2026-03-07T07:30:00.000Z'));
  h.scheduler.tick();
  const afterSpring = h.scheduler.detail(h.botId, spring.watchId);
  assert.equal(afterSpring.recentOccurrences.length, 1);
  assert.equal(afterSpring.recentOccurrences[0].scheduledAt, '2026-03-07T07:30:00.000Z');
  assert.equal(afterSpring.nextDueAt, '2026-03-09T06:30:00.000Z');
  h.scheduler.transition('watch.pause', {
    botId: h.botId,
    watchId: spring.watchId,
    expectedRevision: afterSpring.revision,
    operationId: randomUUID(),
  });

  h.setNow(Date.parse('2026-03-08T06:00:00.000Z'));
  const weekly = h.scheduler.save({
    botId: h.botId,
    kind: 'schedule',
    purpose: 'Weekly Sunday across spring gap',
    schedule: { cadenceKind: 'calendar', frequency: 'weekly', daysOfWeek: [0], hour: 2, minute: 30, timeZone: 'America/New_York' },
    operationId: randomUUID(),
  }).result.watch;
  assert.equal(weekly.nextDueAt, '2026-03-15T06:30:00.000Z');
  h.scheduler.transition('watch.pause', {
    botId: h.botId,
    watchId: weekly.watchId,
    expectedRevision: weekly.revision,
    operationId: randomUUID(),
  });

  h.setNow(Date.parse('2026-11-01T04:00:00.000Z'));
  const fall = h.scheduler.save({
    botId: h.botId,
    kind: 'schedule',
    purpose: 'Fall back daily 01:30 NY',
    schedule: { cadenceKind: 'calendar', frequency: 'daily', hour: 1, minute: 30, timeZone: 'America/New_York' },
    operationId: randomUUID(),
  }).result.watch;
  assert.equal(fall.nextDueAt, '2026-11-01T05:30:00.000Z');

  h.setNow(Date.parse('2026-11-01T05:30:00.000Z'));
  h.scheduler.tick();
  const afterFall = h.scheduler.detail(h.botId, fall.watchId);
  assert.equal(afterFall.recentOccurrences.length, 1);
  assert.equal(afterFall.recentOccurrences[0].scheduledAt, '2026-11-01T05:30:00.000Z');
  assert.equal(afterFall.nextDueAt, '2026-11-02T06:30:00.000Z');

  h.setNow(Date.parse('2026-11-01T06:30:00.000Z'));
  h.scheduler.tick();
  const repeatedHour = h.scheduler.detail(h.botId, fall.watchId);
  assert.equal(repeatedHour.recentOccurrences.length, 1);
  assert.equal(repeatedHour.nextDueAt, '2026-11-02T06:30:00.000Z');

  h.setNow(Date.parse('2026-03-07T00:00:00.000Z'));
  const india = h.scheduler.save({
    botId: h.botId,
    kind: 'schedule',
    purpose: 'Half-hour timezone check',
    schedule: { cadenceKind: 'calendar', frequency: 'daily', hour: 9, minute: 15, timeZone: 'Asia/Kolkata' },
    operationId: randomUUID(),
  }).result.watch;
  assert.equal(india.nextDueAt, '2026-03-07T03:45:00.000Z');
  assert.equal(h.fatalFailures(), 0);
});

test('WatchScheduler resists clock rollback, skips offline backlog cleanly, and clamps jitter with stable canonical scheduledAt', t => {
  const start = Date.parse('2026-04-01T00:00:00.000Z');
  const h = setupHarness(t, start);

  const periodic = h.scheduler.save({
    botId: h.botId,
    kind: 'schedule',
    purpose: 'Interval skip and rollback',
    schedule: { cadenceKind: 'interval', intervalSeconds: 10, timeZone: 'UTC', jitterSeconds: 0 },
    operationId: randomUUID(),
  }).result.watch;
  assert.equal(periodic.nextDueAt, '2026-04-01T00:00:10.000Z');

  h.setNow(start + 10_000);
  h.scheduler.tick();
  let detail = h.scheduler.detail(h.botId, periodic.watchId);
  assert.equal(detail.recentOccurrences.length, 1);
  assert.equal(detail.nextDueAt, '2026-04-01T00:00:20.000Z');

  h.setNow(start + 5_000);
  h.scheduler.tick();
  detail = h.scheduler.detail(h.botId, periodic.watchId);
  assert.equal(detail.recentOccurrences.length, 1);
  assert.equal(detail.nextDueAt, '2026-04-01T00:00:20.000Z');

  h.setNow(start + 145_000);
  h.scheduler.tick();
  detail = h.scheduler.detail(h.botId, periodic.watchId);
  assert.equal(detail.recentOccurrences.length, 2);
  assert.equal(detail.recentOccurrences[0].scheduledAt, '2026-04-01T00:00:20.000Z');
  assert.equal(detail.nextDueAt, '2026-04-01T00:02:30.000Z');

  const inbox = h.storage.listEventInbox(h.botId).filter(row => row.sourceId === periodic.watchId);
  assert.equal(inbox.length, 1);
  assert.equal(inbox[0].coalescedCount, 2);

  h.scheduler.transition('watch.pause', {
    botId: h.botId,
    watchId: periodic.watchId,
    expectedRevision: detail.revision,
    operationId: randomUUID(),
  });

  h.setNow(start + 200_000);
  const jittered = h.scheduler.save({
    botId: h.botId,
    kind: 'schedule',
    purpose: 'Clamped jitter schedule',
    schedule: { cadenceKind: 'interval', intervalSeconds: 40, timeZone: 'UTC', jitterSeconds: 60 },
    operationId: randomUUID(),
  }).result.watch;
  assert.equal(jittered.schedule.jitterSeconds, 10);

  for (let i = 0; i < 3; i++) {
    const stored = h.storage.runtimeWatch(h.botId, jittered.watchId);
    const scheduledMs = Date.parse(stored.scheduledAt);
    const dueMs = Date.parse(stored.nextDueAt);
    assert.ok(dueMs >= scheduledMs && dueMs <= scheduledMs + 10_000, `jitter out of bounds: ${stored.scheduledAt} -> ${stored.nextDueAt}`);
    h.setNow(dueMs);
    h.scheduler.tick();
    const after = h.scheduler.detail(h.botId, jittered.watchId);
    assert.equal(after.recentOccurrences[0].scheduledAt, stored.scheduledAt);
  }
  assert.equal(h.fatalFailures(), 0);
});

test('WatchScheduler tracks precheck >64 KiB tail changes, >1 MiB output_limit abort, failure suppression, and recovery', async t => {
  const start = Date.parse('2026-05-01T00:00:00.000Z');
  const h = setupHarness(t, start);
  const binary = compileUnitObserver(h.workspace);

  const watch = h.scheduler.save({
    botId: h.botId,
    kind: 'schedule',
    purpose: 'Precheck digest state machine',
    schedule: { cadenceKind: 'interval', intervalSeconds: 10, timeZone: 'UTC', jitterSeconds: 0 },
    precheck: { kind: 'executable', executable: binary, allowedArgs: [], workspaceRead: true, timeoutMs: 3000, emitInitial: false },
    operationId: randomUUID(),
  }).result.watch;
  assert.equal(watch.state, 'active');

  const tickOne = async (targetWatch, modeChar, expectedOutcome) => {
    writeFileSync(join(h.workspace, 'state.txt'), modeChar);
    const before = h.scheduler.detail(h.botId, targetWatch);
    const at = before.nextDueAt;
    h.setNow(Date.parse(at));
    h.scheduler.tick();
    const res = await waitOccurrenceSettled(h.scheduler, h.botId, targetWatch, at);
    assert.equal(res.occurrence.outcome, expectedOutcome, JSON.stringify(res.occurrence));
    return res;
  };

  const baseline = await tickOne(watch.watchId, 'A', 'baseline');
  assert.equal(baseline.occurrence.exitCode, 0);
  assert.equal(baseline.occurrence.truncated, true);
  assert.equal(Buffer.byteLength(baseline.occurrence.excerpt), 1024);
  assert.equal(baseline.occurrence.eventId, undefined);
  assert.equal(h.storage.listEventInbox(h.botId).filter(r => r.sourceId === watch.watchId).length, 0);

  const unchanged = await tickOne(watch.watchId, 'A', 'unchanged');
  assert.equal(unchanged.occurrence.digest, baseline.occurrence.digest);
  assert.equal(unchanged.occurrence.eventId, undefined);

  const changed = await tickOne(watch.watchId, 'B', 'changed');
  assert.equal(changed.occurrence.truncated, true);
  assert.equal(changed.occurrence.excerpt, baseline.occurrence.excerpt);
  assert.notEqual(changed.occurrence.digest, baseline.occurrence.digest);
  assert.ok(changed.occurrence.eventId);

  const overflow = await tickOne(watch.watchId, 'O', 'failed');
  assert.equal(overflow.occurrence.error, 'output_limit');
  assert.equal(overflow.detail.lastGoodDigest, changed.occurrence.digest);
  assert.ok(overflow.occurrence.eventId);

  const exitFail = await tickOne(watch.watchId, 'F', 'failed');
  assert.equal(exitFail.occurrence.exitCode, 7);
  assert.equal(exitFail.detail.lastGoodDigest, changed.occurrence.digest);
  assert.ok(exitFail.occurrence.eventId);
  const inboxAfterExitFail = h.storage.listEventInbox(h.botId).filter(r => r.sourceId === watch.watchId).length;

  const suppressed = await tickOne(watch.watchId, 'F', 'suppressed_failure');
  assert.equal(suppressed.occurrence.eventId, undefined);
  assert.equal(h.storage.listEventInbox(h.botId).filter(r => r.sourceId === watch.watchId).length, inboxAfterExitFail);

  const timedOut = await tickOne(watch.watchId, 'T', 'failed');
  assert.equal(timedOut.occurrence.error, 'timeout');
  assert.equal(timedOut.detail.lastGoodDigest, changed.occurrence.digest);

  const recovered = await tickOne(watch.watchId, 'B', 'recovered');
  assert.equal(recovered.detail.consecutiveFailures, 0);
  assert.equal(recovered.occurrence.digest, changed.occurrence.digest);
  assert.ok(recovered.occurrence.eventId);

  h.scheduler.transition('watch.pause', {
    botId: h.botId,
    watchId: watch.watchId,
    expectedRevision: recovered.detail.revision,
    operationId: randomUUID(),
  });

  const initialWatch = h.scheduler.save({
    botId: h.botId,
    kind: 'schedule',
    purpose: 'Precheck emitInitial check',
    schedule: { cadenceKind: 'interval', intervalSeconds: 10, timeZone: 'UTC', jitterSeconds: 0 },
    precheck: { kind: 'executable', executable: binary, allowedArgs: [], workspaceRead: true, timeoutMs: 3000, emitInitial: true },
    operationId: randomUUID(),
  }).result.watch;
  const initRes = await tickOne(initialWatch.watchId, 'A', 'initial');
  assert.ok(initRes.occurrence.eventId);
  h.scheduler.transition('watch.pause', {
    botId: h.botId,
    watchId: initialWatch.watchId,
    expectedRevision: initRes.detail.revision,
    operationId: randomUUID(),
  });

  const timerFuture = new Date(h.getNow() + 60_000).toISOString();
  const pendingTimer = h.scheduler.save({
    botId: h.botId,
    kind: 'timer',
    purpose: 'Dormant recovery timer',
    timer: { fireAt: timerFuture, missedPolicy: 'fire_once_coalesced' },
    operationId: randomUUID(),
  }).result.watch;
  assert.equal(pendingTimer.state, 'active');

  const cancelledSched = h.scheduler.save({
    botId: h.botId,
    kind: 'schedule',
    purpose: 'Cancelled before backup',
    schedule: { cadenceKind: 'interval', intervalSeconds: 15, timeZone: 'UTC', jitterSeconds: 0 },
    operationId: randomUUID(),
  }).result.watch;
  h.scheduler.transition('watch.cancel', {
    botId: h.botId,
    watchId: cancelledSched.watchId,
    expectedRevision: cancelledSched.revision,
    operationId: randomUUID(),
  });

  const captured = h.storage.captureBackup(text => text);
  const wire = { ...captured.profile, backup: captured.backup };
  const summary = inspectProductRecovery(wire);
  assert.deepEqual(summary, { contacts: 1, activities: 0, timers: 1, schedules: 2, dormantWatches: 3, notices: 0, decisions: 0, dormantMandates: 4 });
  const decoded = decodeProductBackup(wire);
  assert.equal(decoded.backup.watchGrants, undefined);
  assert.equal(decoded.backup.botEventInbox, undefined);
  for (const row of decoded.backup.watches) {
    assert.equal(row.armed, false);
    assert.equal(row.grantId, undefined);
    assert.equal(row.nextDueAt, undefined);
    assert.equal(row.lastEventId, undefined);
    if (row.watchId === cancelledSched.watchId) {
      assert.equal(row.state, 'cancelled');
    } else {
      assert.equal(row.state, 'restored');
      assert.equal(row.statusReason, 'restored_dormant');
    }
  }

  const legacyRecovery = inspectProductRecovery({ ...captured.profile, backup: { version: 1, traces: [], traceCoverage: [] } });
  assert.deepEqual(legacyRecovery, { contacts: 1, activities: 0, timers: 0, schedules: 0, dormantWatches: 0, notices: 0, decisions: 0, dormantMandates: 0 });
  assert.equal(h.fatalFailures(), 0);
});
