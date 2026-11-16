# Pi product integration

This is the current migration contract for [Nagi #31](https://github.com/HeimaoLST/Nagi/issues/31). The native Qoder-derived interface and product requirements remain authoritative in [desktop.md](desktop.md) and [the requirements](../requirements/desktop.md). This contract replaces execution-specific Saki assumptions; it is not an acceptance report.

## Source and product boundary

Remote dev `833403e1248e131dab09be254f17f1b8db4434b0` is an ancestor of PR30 `57e0a6784ba7f8ec4c37e5a76f6ab8cdccd9542c`. Integration inherits both, including PR30's Settings and single-list sidebar. PR30's historical timeout is not Pi evidence. The dirty main checkout, reserved preview apps and user profiles are outside this task.

Pi 1.5.2 owns model execution, coding tools, compaction and session transcripts. Nagi owns Bot identity, conversation/activity routing, admission, questions, product ledgers and durable effect receipts. A Bot can have concurrent conversation and activity contexts. This does not select a permanent parent/child hierarchy, autonomous clones, branch naming or a fork policy. Finishing a conversation turn or switching the selected contact does not cancel admitted activities. App-off continuation, a daemon and a general scheduler are outside scope.

There will be one concrete Pi implementation, not a backend interface or permanent fallback to Saki. During migration, an incomplete Draft must remain visibly incomplete. Legacy-history reading is reserved pending the user's explicit archive-versus-readable choice; do not remove that capability or reinterpret its references while the choice is pending.

## Cleanup and callers

| Existing responsibility | Treatment and replacement |
| --- | --- |
| Unused mascot embed/decoder, two PNGs and browser prototype | Remove. No live view consumes the bitmap. Keep immutable historical links; the native implementation is the product. |
| `runtime.go` Saki v1 framing/RPC DTOs | Retire at the actual execution cutover. Replace with `internal/pi` client and native Pi facts, not fabricated Saki journal records. Preserve correlation, bounded frames, disconnect outcomes and child reaping. |
| `runtime_model.go` binary search and Saki launch branches | Replace with the single bundled Node/Pi launch. Retain UI-actor ownership, stale-callback rejection, admission order, independent work and drain. Legacy reader removal remains fenced. |
| `connection_config.go` derived host.json and SAKI runtime injection | Remove after callers move. Keep GUI validation, saved/effective scopes, explicit Test/Save/Apply and existing user-owned data paths. Do not discover unrelated Pi auth/config or ancestor dotenv files for normal setup. |
| Issue helper CLI, Unix forwarding and engine-tool launch adapter | Replace internal Nagi tools with direct asynchronous callbacks. Retain schemas, scopes, handlers and receipts once. Do not remove live domain tools together with their old transport. |
| Contact/issue/history/context/result consumers | Adapt to public Pi facts once. Preserve IDs, drafts, snapshots, requirements, questions, operation receipts, unknown outcomes and raw copy. Assistant prose is not a successful file-effect receipt. |
| Saki artifact pin/fetch/checker and package binary assumptions | Replace with actual pinned Pi/Node build, integration and package gates. Preserve protected aggregate and live product coverage; missing replacement coverage fails rather than skips. |

The existing product store stays the sole product store. Existing `session_id`, previous-session and work/execution strings are legacy references unless an explicit engine-tagged binding proves otherwise. New bindings carry engine `pi`, Bot/contact ID, context purpose, optional issue ID, selection revision and opaque session reference. Do not infer engine from UUID/path syntax. Do not rewrite old execution associations, parse journals, delete legacy files or resend uncertain work. The history choice and strict recovery-version treatment must be settled before persisted-data activation.

## Modules and unique writers

| Module | Owner and boundary |
| --- | --- |
| `internal/pi/*.go` | Business owner: one process/client and typed wire values. No product store, renderer or Agent loop. |
| `internal/pi/worker/` | Lead: pinned SDK worker, package/lock/build. No fixture tools or copied lab product architecture. |
| Existing root product callers | Business owner after client boundary: existing actor/domain/state paths consume Pi facts. A second activity store is prohibited. |
| Native controls, MyGo patches and GUI setup | UI owner: existing controls, secure AX fix, correlated answer view; no parallel domain mutation implementation. |
| Workflow/checker and integration/E2E | Test owner: replacement gates and necessary unit compatibility maintenance. Developers do not edit unit tests or use TDD. |
| Product topic documents and Git integration | Lead. Each additional source writer receives exact paths before editing. |

## Owned process and typed IPC

One Go `pi.Client` owns one pinned Node worker for the app. It starts with explicit worker/state paths and receives configuration over stdin; keys never appear in argv or diagnostic output. Package startup resolves its own Resources, independently of PATH. Development overrides are explicit and are not a consumer prerequisite. Stdout is protocol only; diagnostic stderr must not contain credentials or model text by default.

The private JSONL request envelope is `{v:1,id,method,params}`. Responses are `{v:1,type:"response",id,ok,result}` or the same with `error:{code,message}`. Events are `{v:1,type:"event",workerEpoch,contextId,runId,seq,event,data}`. IDs are nonempty Nagi correlations, not provider-generated identifiers. Responses correlate by request ID; events correlate by epoch/context/run and increasing sequence. The maximum frame is 8 MiB including LF, matching the current product boundary; history is paged rather than silently shortened to fit. A malformed frame or unexpected EOF fails pending requests and leaves admitted unsettled runs unknown. No automatic command replay follows a timeout/disconnect.

| Method | Params and observable result |
| --- | --- |
| `configure` | `wireApi` (`openai-completions` or `openai-responses`), `baseUrl`, `apiKey`, `model`, positive `contextTokens`/`outputTokens`. Returns SDK version/worker epoch and locally installed configuration. This does not test a provider or establish MCP readiness. |
| `context.open` | `contextId`, `botId`, `purpose` (`conversation`/`activity`), optional `activityId`, `selectionRevision`, `instructions`, absolute `cwd`, optional `resumeRef`, `used`, `codingTools`, `domainTools`. Returns the exact binding and opaque reference. Each domain tool has name/description/JSON-schema parameters; its effects remain in Go. |
| `context.history` | `contextId`, optional stable entry cursor and bounded page limit. Returns SDK public entries, session identity, next cursor and projection metadata. Rendering uses original text/tool results; no direct journal read. |
| `context.inspect` | `contextId`. Returns actual public model context, finalized messages, separate partial streaming state, active run, observation sequence and captured time. This is the public SDK context projection, not a claim of byte-identical provider request serialization after hooks. A bounded result must disclose incompleteness; it cannot claim a complete snapshot after truncation. No model request is made. |
| `run.start` | `contextId`, unique `runId`, exact text and captured product turn/execution correlation. Returns admission before asynchronous execution. Already-busy contexts reject without a queue. The Go product owns global capacity and conversation reservation. |
| `run.cancel` | Exact `contextId`/`runId`. Acknowledges cancellation request only; settlement is a separate event. It must not abort another context. |
| `tool.result` | Exact epoch/context/run/call ID and success content or error. Resolves only that pending custom-tool Promise. Stale/duplicate answers never target a newer call. |
| `context.close` | Idle context only, with awaited extension/session disposal. Does not delete its session data. |
| `shutdown` | No new admission; caller has completed product drain. Await SDK runtime disposal, flush protocol and exit. Go exports the actual wait/reap result separately from app exit. |

Events include `run.started`, `text.delta`, `tool.request`, `tool.finished` and `run.settled`. A custom `tool.request` carries tool name, exact arguments and call identity; Go asynchronously invokes the existing actor/domain handler and returns `tool.result`. Neither the pipe reader nor UI actor waits synchronously for another context's completion. A settled event includes completed/interrupted/failed outcome, finalized assistant text/error and public usage when available. An accepted request, a successful tool receipt, a settled run and a completed business issue remain distinct.

SDK session methods provide history and compaction-aware context. Only durably proven-unused missing session bindings may be recreated. A used or uncertain missing session fails visibly. Pi execution IDs are correlated with Nagi's existing execution ledger; old task/message fields must not be populated with invented provider facts. Current streaming text is not a finalized outcome.

## First use, tools and questions

A new user opens native Settings, chooses Chat Completions or Responses, enters endpoint/key/model, chooses a working folder, then uses Save and Apply. Preserve working token/slot defaults and expose them as advanced controls; positive IPC values do not require first-time users to learn token-window settings. Exact raw Draft ownership, errors, saved defaults and effective context settings remain separate. Test is an explicit tools-disabled bounded request, not a side effect of Save/Apply. Existing two-slot default supports a chat and one activity; the two-activity-plus-chat acceptance scene explicitly configures three slots rather than silently changing the user's value.

The worker registers an owned provider and explicit credential store; it must not inherit unrelated user Pi profiles. GUI limits are user declarations, not discovery or verified pricing. Automatic model retry is disabled for this initial cutover; uncertain sends are not replayed. Pi compaction stays SDK-owned. Use the configured output reserve and preserve the current product recent-context budget min(8192, (context minus output) / 4); this is a product policy, not discovered provider capacity. Usage scope and unknown pricing must be reported honestly; no host transcript rewrite or model-generated title call is added.

Coding tools use actual Pi read/bash/edit/write in the selected project cwd, plus explicitly supported optional tools. Nil/default and explicitly empty selections remain distinct. Domain-tool registration is separate so disabling coding tools does not accidentally disable Nagi's promised coordination tools. Conversation tools retain issue_list/get/action semantics and existing scope/operation receipts; activity question handling retains issue ownership and exact question identity. Direct progress inspection reads public context/state without asking the activity model to generate a report.

For a question, persist the existing question/receipt association before showing it. The GUI captures owner, issue, question reference/revision and origin epoch/context/run/call. Answer uses one shared domain operation, persists the accepted answer, and resolves only that held tool call. A generic supplement save is not an answer. A cancelled or lost question may not be delivered to a replacement run automatically. Keep current legacy wait/cancel semantics until this Pi-specific correlation is wired; no duplicate question state machine is introduced.

General external MCP uses Pi's public MCP extension with Nagi-owned configuration, explicit direct exposure for small toolsets and awaited initialize/list/call/close. Internal Nagi domain tools use the direct callback above. No required user CLI/config file is added. Local worker readiness, external MCP readiness and provider readiness are separate. Real support is claimed only after actual product integration, not from SDK source inspection.

## Credentials, compatibility and acceptance

The actual product password editor shares the lab editor implementation. Source inspection found a Darwin AX superclass-cache path and a disabled-state trait gap; native product exposure is not yet observed. Correct the pinned bridge narrowly and verify with a synthetic sentinel through actual product Edit/input/Busy/readback. Value/selection/range/attributed AX reads must not expose the sentinel while the real Draft/save value stays exact. No real credential enters this path until that gate passes.

Keep current macOS 13.0/ARM64 support unless a concrete compatibility change is coordinated. The delivery pin selects official Node22.23.5 darwin-arm64, archive SHA256 e7131995759b61a0170b646010d2ad35a5d37b4cb0d471f3e8317eac6f742f3d. Its actual Mach-O deployment floor is11.0; this preserves the Nagi13.0 floor but does not promise current vendor support for every old OS. Record archive/binary/license provenance and package it. An arbitrary PATH Node or the lab's external Node is not standalone delivery. Preserve the existing MyGo patches and signed package readback.

Acceptance proceeds on the actual combined tree: (1) dead-code cleanup/static/build boundary; (2) actual pinned worker and coding tool in selected cwd, persistent context reopen and direct inspect; (3) existing domain scope/receipt bridge plus independently advancing activity and usable chat; (4) exact question/answer and supported external MCP round trip; (5) Stop, EOF/crash/restart/unknown/no-replay and actual child outcomes; (6) native GUI first use and synthetic-key AX, minimum/default Qoder layouts, one answer rendered once; (7) clean signed package launched without Node on PATH, lock/notices/build identity, protected CI with all retained product gates mapped. Controlled local providers establish mechanics only. Real endpoint/wire/model quality, billing and human acceptance remain separate until exact configuration/budget is supplied privately and secure entry is verified.
