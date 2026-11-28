# RFC-0004: Stateful Session Handles, Event Watch Substrate & Scoped Autonomous Orchestration

- **Status**: `Implemented` (`BG-1`–`BG-3` on SQLite v12–v14, with desktop Terminal sessions, Scheduled work, timeline notices, decision cards, Background work drawer, and native hide/quit lifecycle)
- **Baseline**: `dev` @ `1f3d4f5` (RFC-0001 `M0a`–`M4`, RFC-0002 `CP-1`–`CP-3`, RFC-0003 `RT-1`–`RT-2` full frontend/backend parity baseline; SQLite `v11` -> `v12` in `BG-1` -> `v13` in `BG-2` -> `v14` in `BG-3`; Pi SDK `1.5.2`; Node `22.23.5`)
- **Authors / Reviewers**: Backend Architecture (`t21d`), Deep Technical Review (`t1jr` / Astra-6.2, including offline macOS arm64 `forkpty` vs `/usr/bin/script` & Pi `1.5.2` `SessionManager` probes)
- **Canonical Vocabulary**: [docs/design/glossary.md](../design/glossary.md)
- **Promotion Targets**: [product-protocol.md](../design/product-protocol.md), [pi-integration.md](../design/pi-integration.md), [glossary.md](../design/glossary.md), [desktop.md](../design/desktop.md), [README.md](../../README.md)

> **Execution Note**: `BG-1` (`SQLite v12`), `BG-2` (`SQLite v13`), and `BG-3` (`SQLite v14`, including `BG-3a` native lifecycle and `BG-3b` desktop UI) are shipped on `dev`; see [product-protocol.md](../design/product-protocol.md), [pi-integration.md](../design/pi-integration.md), [glossary.md](../design/glossary.md), and [desktop.md](../design/desktop.md) for canonical shipped contracts.

## Milestone Tracker

- [x] **BG-1** *(SQLite v12 — Shipped)* — Worker-Owned Stateful Session Handles (`session_open`, `session_read`, `session_write`, `session_signal`, `session_resize`, `session_eof`, `session_close`, `session_list`), Bundled Native Darwin `forkpty` Supervisor & Pipe Supervisor (`env -i PATH=/nonexistent`, zero `node-gyp`), Incremental Normalized Streaming Redactor, Read-Only GUI Inspection / Kill (`session.*`), and Durable `session_watch` Triggers
- [x] **BG-2** *(SQLite v13 — Shipped)* — Standing Local Execution Grants, Constrained `schedule.precheck` Observation Diffs, Durable Timers & Recurring Schedules (DST/missed-slot/sleep-wake policy), Staged At-Most-Once Queue Claims (`bot_event_inbox`), and Dormant Backup/Import Policy
- [x] **BG-3** *(SQLite v14 — Shipped)* — Dedicated Private `OrchestrationActor` (`SessionManager.inMemory` generations + compact CAS checkpoints + full-request token budgeting + RFC-0003 `utility` default), Human-Queued `maxActiveTasks` Priority, Three-Way Attention Gate, Draft-Safe Product Timeline Notices (`conversation_notify`), Causal Lack-of-Progress Backoff, and Native Window Hide vs Explicit `Cmd-Q` Shutdown Separation

---

## Problem Statement & Domain-Agnostic Principle

Xuhu's shipped backend covers foreground chat and single-shot delegated `Activity` execution, leaving two structural runtime gaps plus one superseded framing in [RFC-0002](0002-bot-activity-control-plane.md):

1. **Tools Are Stateless Single-Turn Invocations**: Current tools (`bash`, `skill_exec`, MCP `callTool`) run synchronously within one LLM turn, hold an active provider slot while blocking, and exit before returning. Multi-turn workflows needing a persistent interactive process, REPL, stream, or child session cannot hold a live handle across turns.
2. **All Turns Require Foreground Human Triggers**: Bots stay dormant without a foreground user send. There is no unified runtime substrate to wait asynchronously without consuming provider concurrency (process exit, stream output match, delayed timer, or state-diff schedule) and enqueue durable wakeup events when warranted.
3. **Retiring Passive-Only Orchestration Framing**: Users expect an authorized Bot holding an open goal to **continue collaborating, operating stateful handles, delegating/monitoring background work, and waking across multi-hour or overnight spans even when the human steps away or closes the window**. Cost is controlled by running autonomous wakes on a **bounded, compact orchestration context** and selecting efficient models (`utility` via [RFC-0003](0003-model-catalog-and-smart-routing.md)), **not** by banning unprompted LLM calls or halting when the user is away.

**Scenario Independence Rule**: Remote administration protocols, external CI tools, and multi-agent CLIs are downstream emergent use cases. Core Xuhu schemas, SQLite tables, RPCs, model tools, and acceptance fixtures contain **no** service names, remote protocol fields, or specialized CLI assumptions—only three orthogonal general runtime primitives.

---

## Architecture Overview: Three-Layer System & Three General Primitives

```text
Layer 1 · Human Attention Surface (Timeline Notices, Decision Cards & Live Inspection)
  └─ Receives ONLY what the Main Bot decides merits human attention (`conversation_notify`
     timeline notices, typed confirmation/decision cards, M3 skill approval cards) plus
     read-only live visibility & emergency Kill controls over active handles and watches.
          ▲
          │ Explicit Terminal Attention Disposition:
          │  • Silent Digestion: execute authorized work, update checkpoint, finish quietly
          │  • Surface Update: commit bounded draft-safe notice on target Conversation timeline
          │  • Request Decision: park only that goal with a non-blocking typed card
          │
Layer 2 · Private Scoped Autonomous Cognition (`OrchestrationActor`)
  └─ Runs autonomously even while the human is away or window is hidden; executes on a
     dedicated compact `SessionManager.inMemory` generation partitioned by mandate/goal
     with bounded projected requests (schemas + results + event batch + checkpoint + headroom).
          ▲
          │ Single-Transaction Durable `bot_event_inbox` (`RuntimeEvent` + Staged Lease Claims)
          │
Layer 3 · Supervised Stateful Handles & Async Event / Schedule Substrate
  ├── Primitive 1 · Stateful Session Handles (`session_*` / `session.*`)
  │     • Bundled Darwin `forkpty` native supervisor (PTY) & process supervisor (pipe)
  │     • Independent worker ownership pipe: EOF reaps child tree even on worker SIGKILL
  │     • Incremental ANSI normalization + streaming `projectionRedactor` + offset cursor
  │
  └── Primitive 2 · Asynchronous Watches, Timers & Schedules (`watch_*` / `watch.*`)
        • `session_watch` (exit, linear-time match, idle, timeout on a handle generation)
        • Durable `timer` & `schedule` (UTC due, missed policy, local standing grants,
          constrained read-only `precheck` digest comparison)
```

---

## 1. Primitive 1: Cross-Turn Stateful Session Handles (`BG-1`, SQLite v12)

### 1.1 Zero-`node-gyp` Standalone macOS Supervisor (`forkpty` + Pipe)

- **Why a Bundled Native `forkpty` Supervisor (Verified Offline Against Pinned Packaging Constraints)**:
  - `.github/pi-runtime.json` and `scripts/build-pi-worker.py` enforce `npm ci --ignore-scripts` (zero `node-gyp` native npm addons) and `scripts/build-macos-app.py` executes with Node, Python, and compilers absent from `PATH`.
  - An offline probe on `darwin/arm64` under bundled Node `22.23.5` with `env -i PATH=/nonexistent` proved `/usr/bin/script -q /dev/null ...` exits `1` under worker-style stdio (`tcgetattr/ioctl: Operation not supported on socket`), whereas a small, single-threaded native supervisor compiled with Apple `clang -arch arm64 -mmacosx-version-min=13.0` using Darwin `forkpty`/`libutil` cleanly established slave TTY descriptors `0..2`, foreground process-group ownership, `SIGWINCH` (`101×41`), master slave-`ECHO=0` observation, canonical `SIGINT` vs raw `0x03`, and exact `waitpid` child exit `37`.
- **Same Supervisor Contract for Both `pty` and `pipe` Modes**:
  - Run **both** `mode: "pty"` (interleaved stream) and `mode: "pipe"` (separate `stdout`/`stderr` channel labels) under a surviving supervisor reachable over a private, versioned, length-bounded control channel separate from data bytes and worker JSONL (`FD_CLOEXEC` across child `exec`).
  - Pass an **exclusive worker ownership channel**: when Node dies (`SIGKILL`/crash), supervisor detects EOF on the ownership channel, closes child input, terminates the supervised process tree, drains bounded output before deadline, and reaps direct children.
  - Record helper source, compiler/SDK metadata, target arch, minimum macOS, and binary digest in packaging receipts; nest-sign the helper before signing `Xuhu.app` in `build-macos-app.py`; validate helper path resolution strictly from the packaged bundle without `PATH`.
  - Document the supervised process-tree boundary honestly (group signaling alone cannot contain grandchildren that intentionally detach; report actual leader/descendant reaper status rather than claiming unconditional cleanup beyond OS process-group/supervisor ownership).

### 1.2 Operations, Authorization & Crash Safety

- **Distinct Control Operations** (`session_open`, `session_list`, `session_read`, `session_write`, `session_signal`, `session_resize`, `session_eof`, `session_close`):
  - Separate byte writes, window resize, explicit OS signals, stdin EOF, and full handle close (TTY line discipline behaves differently for `0x03` canonical `SIGINT` vs raw bytes vs pipe EOF vs PTY hangup).
  - Return child exit code or terminating signal separately from supervisor status, HUP, transport error, and unknown outcome. Publish `opened` only after exec/setup succeeds, serializing early exit and `session_watch` registration against retained terminal state.
  - A successful `session_write` receipt proves transport byte acceptance only; an interrupted write never auto-retries.
- **Scoped Access & Process Limits**:
  - Gate `session_open` explicitly by least-privilege capability (`workspace` preset / explicit session grant, denied to `none`/`readonly`).
  - Bind each handle strictly to `(creatingOperationId, actor, botId, mandateId/origin, canonicalCwd, grantId, epoch)` — sibling Activities do **not** inherit write access merely from sharing `ownerBotId`. Reauthorize reads, writes, signals, and close independently per handle generation. Enforce `maxActiveSessionsPerBot` and profile caps.
  - Never signal a PID recovered from an earlier worker epoch. On `app.shutdown`, fence new operations, drain, persist final state, and reap before ack; on crash recovery report `disconnected` until corroborated.

### 1.3 Normalized Projected Output Stream & Non-Secret Auth Prompt Rule

- Expose a bounded **projected** UTF-8 stream via cursor (`firstAvailableOffset`, `nextOffset`, `generation`, `eof`, explicit eviction/gap notice).
- Run incremental UTF-8 decoding → terminal-control/CR normalization → `streamingProjectionRedactor` (`product-redactor.ts`) continuously across chunk boundaries before buffering, SQLite checkpoints, event emissions, GUI views, or model tool results (never returning raw unredacted bytes or executable terminal escapes).
- **Non-Secret Auth Boundary**: Because `ECHO=0` also occurs in normal TUIs and secrets can be requested with echo on or over pipes, use echo state and prompt heuristics only to pause interactive input. **Never ask the user to enter a password into `activity_ask`, an orchestration card, or the composer** (which persist text and can transmit it to providers); surface a non-secret external authentication step or park the operation.
- Expose read-only GUI inspection and 1-click user Emergency Kill (`session.list`, `session.read`, `session.close`, `session.changed`, coalesced `session.outputUpdated` watermarks).

---

## 2. Primitive 2: Standing Grants, Watches, Schedules & Event Inbox (`BG-2`, SQLite v13)

### 2.1 Standing Local Execution Authority (Resolving Unattended Execution Risk)

- Interactive `BotProfile.codingTools` / `toolPreset: "workspace"` grants interactive turn-level permission inside a workspace, **not** standing unattended execution every N seconds or persistent interactive shell leases.
- Keep three boundaries explicit and separate:
  1. **Capability to Propose**: Whether a Bot may propose an executable watch/schedule or pre-check.
  2. **Installation-Local Standing Mandate Grant**: Direct user-saved schedules create a local grant bound to `(installationId, botId, owner/origin, purpose, operationClass, immutableExecutable/approvedSkillBundle, allowedArgv, canonicalCwd, resourcePolicy, minCadence)`. Model-created executable schedules start as unactivated **proposals** unless matched by an existing standing grant or approved explicitly in GUI. Revalidate on activation, every firing, and write boundaries; edits to script content, parameters, cadence tightening, workspace move, archive, or pause immediately invalidate/fence pending dispatches.
  3. **Constrained Pre-Check Execution Scope**: Never treat `shell: false` or an arbitrary `command` string as read-only safety. Constrain `schedule.precheck` to reviewed/approved immutable executables or constrained read-only observation facilities with explicit argv, scrubbed env, zero ambient credentials/network/write escalation, timeout, output cap, and OS enforcement where supported (fail closed when required policy cannot be enforced).
- **Dormant Backup & Import Contract**: Portable single-file `.json` backups and same-device external snapshot imports always load all executable schedules and wake-capable timers **paused and unarmed** (zero active grants, zero lease claims, zero automatic child/provider execution on open). Explicitly re-enable locally on the target installation.

### 2.2 Deterministic `session_watch`, Timers, Schedules & Pre-Check Diffs

| Watch Kind | Exact Semantics & Safety Rules |
| --- | --- |
| `session_watch` | Binds to exact `sessionId`, `generation`, and starting `offset` under the same lock serializing sequence publication and exit (handling immediate readback if already exited). Supports `exit`, `match` (linear-time matcher or streaming literals with input bounds — never unbounded JS backtracking regex on event loop), `idle` (live elapsed monotonic clock), and `timeout` (`exactly one` winner per one-shot). Evaluates across chunk boundaries; reports truncation/ring eviction honestly; worker restart settles orphaned watches cleanly once as `target_lost` with zero reconnection. |
| `timer` | Persistent UTC instant (`fireAt`) + revision + unique `(watchId, revision, scheduledInstant)` occurrence ID; atomic `fired_at` prevents duplicate fire. Explicit user-visible missed policy (`skip` default or `fire_once_coalesced`) when machine slept or app was closed. |
| `schedule` | Minimum cadence floor (`>= 10s`), explicit DST/timezone/jitter/clock-rollback normalization preserving canonical `scheduledInstant`, at most one active pre-check per schedule, skipping missed offline intervals cleanly (zero catch-up storms). Plain due schedules *without* `precheck` enqueue `schedule_tick` directly whenever due (respecting intentional periodic reporting even when observations stay identical). |
| `schedule.precheck` | First clean run sets baseline digest unless `emitInitial: true`. Normalized + bounded + redacted output & result class SHA-256 comparison (marking truncated tails distinctly so tail changes aren't treated as equal). Emits on class transition (change, first failure, recovery), suppresses identical consecutive failures, and never lets a timeout overwrite last-known-good digest. |

### 2.3 Single-Transaction Inbox & Zero-Replay Claim State Machine

- **Atomic Producer Commit**: Commit the source transition (`Activity` milestone, session exit/match, timer/schedule next-due + digest) + unique occurrence ID + `bot_event_inbox` row in **one SQLite transaction** (closing crash windows between commit and async hint emission).
- **Safe Burst Coalescing**: Coalesce only unclaimed events sharing equivalent source, authority, and target, keeping count/range and latest watermark while **never** silently dropping human answers, approvals, terminal outcomes, failure/recovery transitions, or non-idempotent facts.
- **At-Most-Once Claim & `unknown` Rule**:
  1. Persist batch membership, run ID, and staged `claim` before dispatching a model turn or external effect.
  2. A claim proven **never dispatched** may return cleanly to `pending`.
  3. Once any provider call or mutating tool admission could have started, worker death or lease expiry **never proves non-dispatch** — settle that execution/batch explicitly as **`unknown` with an inspectable receipt**, never automatically repeating the uncertain effect!

---

## 3. Primitive 3: Private Autonomous Cognition & Attention Gate (`BG-3`, SQLite v14)

### 3.1 Dedicated `OrchestrationActor` (Isolating Public Conversations)

- Add a third explicit variant **`OrchestrationActor`** alongside Foreground `Conversation` and Background `Activity` (keeping existing foreground draft clearing, memory log triggers, `xuhu-turn-v1` user transcript rules, and current-user-turn relay tools strictly untouched):
  - Keyed by `(botId, mandateId, generation, runId, batchId)`, strictly **partitioned per goal/mandate** (never merging distinct permissions, destinations, or target conversations under one shared Bot prompt).
  - Own a dedicated orchestration admission ledger reusing M4's authoritative `execution_mutation_admissions` validation rules (zero silent bypasses, zero fake foreground `executions` rows).
- **Compact Bounded `SessionManager.inMemory` Execution**:
  - Maintain a compact in-memory Pi generation backed by CAS-committed SQLite checkpoints at completed turn boundaries (keeping private transcripts invisible to public `conversation.history`, preview, search, and backup).
  - Begin each wake turn with a structured non-command `<orchestration_event>` envelope (explicitly noted as internal transport role, zero human provenance).
  - **Bound the Complete Projected Request**: Budget the *entire* serialized request — static Bot L0/L1 prefix, allowed tool schemas, structured checkpoint (`workingSummary` + active handles/watches/activities + recent action ledger), coalesced event batch, recent turn tool outputs, and route `outputTokens` headroom — rotating to a fresh private generation cleanly once tool calls settle and before hitting route limits. Models' advisory summaries never mint authority, rules, or consent.
- **Priority & Concurrency Sharing**:
  - Autonomous orchestration shares `maxActiveTasks` with foreground and Activity work while idle handles/watches/timers/parked cards consume **zero** slots.
  - Enqueue waiting foreground human sends ahead of pending autonomous orchestration dispatches at safe turn boundaries (`maxActiveTasks = 1` finishes an already-dispatched request cleanly without mid-request abort and serves the waiting human turn next without starvation).
  - Default routine orchestration turns to `utility` via [RFC-0003](0003-model-catalog-and-smart-routing.md), selecting heavier roles cleanly at admitted new-turn boundaries when warranted; preserve captured-route immutability and no transport retries on uncertain calls.

### 3.2 Draft-Safe Product Timeline Notices (`conversation_notify`) & Native Close Split

- **Why Product Timeline Notices Instead of `Pi sendCustomMessage`**:
  - Offline inspection of Pi SDK `1.5.2` proved `sendCustomMessage(..., { triggerTurn: false })` stores an entry converted to role `user` in the provider payload while missing `xuhu-turn-v1` public transcript projection (`custom_message` alters provider message roles without rendering as intended).
  - **Contract**: `conversation_notify` writes a durable **product-owned timeline record** (`provenance: "orchestration"`, immutable target `conversationId` captured at mandate/watch creation, redacted bounded summary, verified source entity refs, deduplicated by `(sourceRunId, factDigest)`):
    - Works even on empty Conversations lacking a Pi session file; stays absent from foreground provider context unless explicitly quoted or inspected; never alters `Draft.text`, `Draft.materials`, `Draft.quote`, or `Draft.revision`.
    - Serialized per Conversation against foreground `conversation.send`: earlier notice takes a pre-send timeline anchor; concurrent notice created after an admitted send settles anchors strictly **after** that send settles (or at its recorded `unknown`/failed boundary) in commit sequence.
    - Tracked by an independent timeline revision so native UI reducer merges notices cleanly by `(kind, messageId)` without disrupting composer focus, draft edits, streaming assistant bubbles, or scroll position.
- **Terminal Attention Dispositions & Steer Hygiene**:
  - Each private orchestration run ends cleanly with one terminal disposition: `silent`, `notified` (single idempotent `conversation_notify`), or `needs_decision` (non-blocking typed card freeing the Bot/goal slot cleanly without stalling unrelated goals or Bots).
  - Preserve RFC-0002's current-user-turn requirement on user-facing `activity_answer` / `activity_action(steer)`. If future `BG-3` exposes mandate-bound autonomous Activity steering, implement it as a separate typed operation carrying `OrchestrationActor` provenance and identical streaming-only/consumption/disposal/no-replay proofs rather than spoofing user steer messages.
- **Window Close vs Quit Separation & Causal Lack-of-Progress Backoff**:
  - **Window Hide vs App Quit**: Separate closing the native desktop window (hide/detach UI while authorized overnight orchestration continues running inside worker, with discoverable reopen & Stop controls) from explicit **Quit (`Cmd-Q`)**, which drains, disarms sources, closes/reaps handles, and settles worker exit cleanly.
  - **Causal Lack-of-Progress Backoff (Zero Overnight Turn Caps)**: Never halt a whole Bot or impose arbitrary daily turn caps on productive work. Track progress causally per `(mandateId, watchId, actionOrErrorClass, observedStateHash)`: exponential backoff applies only when repeated wakes make zero state progress or hit alternating errors, immediately clearing on real progress, external change, or human action, with full per-wake metrics (`calls`, `tokens/cost`, `delay`, `coalescedCount`, `backoffReason`) observable in UI.

---

## 4. SQLite `v12`–`v14` Placement, Backup Policy & Phased Acceptance Gates

| Version / Phase | Live SQLite State (`DEFERRABLE INITIALLY DEFERRED` Encoded FKs) | Portable Single-JSON `.json` Backup Contract | Mandatory Frozen-Candidate Acceptance Gate (When Unheld) |
| --- | --- | --- | --- |
| **`v12` / `BG-1`** | Session handle metadata/tombstones, owner/mandate/epoch, bounded sanitized checkpoint & offset watermark, `session_watch` definitions + unique fired occurrences (`worker-lifetime` PIDs/FDs in RAM only). | Zero PIDs, PGIDs, descriptors, tokens, raw shell input/tails, or live watches. Sanitized historical receipts are read-only metadata. | Built standalone app with Node/Python/compiler absent from `PATH`; both `pty` (`forkpty` supervisor) and `pipe` transports; split ANSI/UTF-8/credential chunks; offset eviction; canonical `SIGINT` vs raw `0x03`, resize, HUP/EOF, worker `SIGKILL` reaper test; cross-Bot/sibling isolation; GUI emergency Kill; explicit crash `disconnected` + `unknown` write handling. |
| **`v13` / `BG-2`** | Extend watches for `timer`/`schedule`, install-local standing grants, `(watchId, revision, scheduledInstant)` occurrences, `precheck` digests, serialized `bot_event_inbox` with staged lease claims. | Nonsecret schedule definitions/history imported strictly **paused and unarmed** on restore/external snapshot replacement (zero grants/leases/child starts on open). | Controlled clock & race barriers (`arm vs exit`, `cancel vs fire`, DST/missed slots, sleep/wake coalescing); pre-check change/failure/recovery suppression vs no-precheck periodic tick; synthetic credential protection against arbitrary/modified commands; staged claim `unknown` no-replay across worker crash; `v11 -> v12 -> v13` migration & future-version refusal. |
| **`v14` / `BG-3`** | Goal/mandate grants, private orchestration checkpoints, generation/claim/effect admission ledgers, typed decision cards, durable timeline notices (`conversation_notify`), causal backoff state. | Sanitized goals/notices/cards/receipts imported dormant/read-only (revalidating targets, never auto-dispatching commands or provider turns on open). | Real pinned Pi multi-turn overnight autonomy without foreground input through rolling compaction & `utility` routing; causal backoff on stuck loop while second source progresses; human send preempting next autonomous slot at `maxActiveTasks=1`; `conversation_notify` vs active/failed/`unknown` send & draft/quote/Material races; window hide/reopen vs `Cmd-Q` reap; `v13 -> v14` migration & v15 refusal. |
