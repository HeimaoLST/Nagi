# Nagi development lessons

## UI and Native Interaction

### Review composed pages and record a verdict

**Problem:** Component checks, geometry and captures can pass while the composed page is misaligned, hides a control, repeats a notice, collapses primary/secondary/danger buttons into borderless ghost text, or lets 14px helper prose overpower setting labels and section headers. An uninspected capture is not evidence.

**Cause or correction:** Visual acceptance depends on the rendered page, not source, test status or image count. Capture and review are separate steps, and unbordered ghost actions plus verbose disclaimers destroy visual hierarchy.

**Prevention:** Inspect screenshots of actual composed pages at applicable sizes, themes, languages and states against `docs/design/desktop.md` and `docs/design/ui-checklist.md`. Enforce explicit four-tier typography (page title > section header > setting label > muted 1-line helper), distinct Primary/bordered Secondary/red Danger/Link controls, control-first Settings (segmented switches, presets and concise disclosures over stacked textboxes/prose), and centered wide-screen alignment. Check control bounds and scroll position, keep a failure frame when a click cannot be verified, and record the reviewer and verdict against the frozen candidate.

### Verify native app, window and chooser identity

**Problem:** A new window can appear after the owned app exits, a chooser launcher can lose the original process, and a duplicate bundle identifier can show an unrelated window. The same file can appear as `/tmp` or `/private/tmp`.

**Cause or correction:** Window existence, launcher success and path spelling do not identify a process or prove profile binding. A new window after an owned exit has an Unconfirmed cause.

**Prevention:** Record the launch PID and compare the device/inode of the kernel-reported executable with the bundled file. Bind the disposable profile and chooser driver to one launch, verify the visible confirmation, returned path, originating focus and original PID, and check normal quit and child disappearance separately. Recheck identity after close observations; do not retry an unchanged binding.

### Use pinned MyGo components and reacquire controls

**Problem:** Unkeyed controls can change identity after a structural update, causing tests to activate captions, wrong rows or stale editors. Handwritten rendering can duplicate an existing MyGo control.

**Cause or correction:** MyGo derives unkeyed element identities from sibling position. `Tester.Find` can return clipped captions and `HasText` uses substring matching. Navigation can replace the editor while preserving the Bot ID.

**Prevention:** Reuse pinned components, official examples and shared helpers; record the concrete unsupported behavior before custom UI. Reacquire the action after navigation, verify expansion/current editor state and actual text-input versus button focus, and use exact labels with positive viewport geometry. Prefer the real pane or bounded keyboard traversal, and inspect all affected helpers rather than fixing one control.

### Drive PopoverMenu overlays and modal opener focus explicitly

**Problem:** Switching a row menu from native `ContextMenu` to MyGo’s themed `PopoverMenu` broke `Tester.ChooseMenuItem` fixtures, duplicate panel headings captured `Tester.Click`, and dismissing a `DialogBase`/`Modal` lost return focus to the opener.

**Cause or correction:** `Tester.ChooseMenuItem` queries only the native host menu, whereas `PopoverMenu` renders ordinary in-window overlay elements. `DialogBase`/`Modal` restores focus to whichever control held focus when it opened.

**Prevention:** Drive `PopoverMenu` through in-window pointer clicks (or keyboard navigation / menu geometry when another surface shares an item label). Focus the triggering button before opening a modal so Cancel, Escape and backdrop dismissal automatically restore focus cleanly.

### Control compact TextArea height, keying and caret inset explicitly

**Problem:** Replacing a tall fixed-height composer with stock `ui.TextArea` kept an unintended 80-DIP vertical minimum, calling `.Key()` after constructing a stateful textarea risked identity errors, and removing horizontal padding broke the 2-DIP caret-inset geometry check.

**Cause or correction:** MyGo’s stock `ui.TextArea` applies a default minimum height and stateful editors bind identity at construction time; zeroing all padding removes the horizontal caret margin while vertical slack only comes from top/bottom padding.

**Prevention:** Key stateful editors with `c.Key(...)` before calling `ui.TextAreaBase`, use `Lines(1, 6)` with `Padding(0, 2)` for compact auto-growing composers, and verify net editable width (`>= 440 DIP` at `960x640` with profile open) plus resting height directly on rendered geometry.

### Move caret explicitly after programmatic TextArea draft fill

**Problem:** Filling a bound `*string` draft directly when clicking a starter prompt left the focused `ui.TextAreaBase` caret stranded at rune offset `0` on the far left instead of at the end of the inserted text.

**Cause or correction:** When an existing MyGo `TextAreaBase` syncs a changed `*string` value via `ed.setText(*value)`, it clamps the prior caret with `min(caret, len)`, which preserves offset `0` from an empty composer.

**Prevention:** Whenever a UI action programmatically populates a `TextAreaBase` and expects the user to continue typing at the end, pair `input.Focus()` with `input.SetTextSelection(math.MaxInt, math.MaxInt)` (which clamps cleanly across ASCII, CJK and multi-byte runes and resets the caret even on identical-text re-click) and verify by typing after the fill in headless tests.

### Audit pinned MyGo patches, parser defaults and licenses on minor upgrades

**Problem:** Minor MyGo upgrades can silently enable new default Markdown extensions (`ExtMentions`, `ExtReferences`) and partially fix upstream drag/accessibility paths while leaving disabled-state or Darwin superclass cache leaks behind.

**Cause or correction:** Upstream defaults evolve across minor releases, and a partial upstream accessibility guard does not guarantee every getter, setter and disabled early-return path scrubs secrets.

**Prevention:** Diff every pinned patch against the exact target MyGo source before dropping or rebasing, lock input/output SHA-256 hashes with `--fuzz=0` in `scripts/gui-go.py`, opt out of unused default Markdown extensions explicitly, and verify every linked module supplies a license notice for standalone macOS bundling.

### Keep multi-bubble projection consistent across transcript, rail and width bounds

**Problem:** Projecting a streamed reply into multiple bubbles hid an incomplete trailing separator in the chat transcript while leaking it in the Bot Team rail preview, and unconstrained bubble markdown readers overflowed wide four-column tables at 1040 and 720 widths.

**Cause or correction:** The transcript and rail summary read separate projections of the same raw reply string, and a bubble container without a chat-width ceiling lets wide tables expand past the column.

**Prevention:** Keep raw persisted/streamed text intact for byte-offset and history contracts, project both the chat transcript and Bot Team preview through the same fence-aware, incomplete-marker-suppressing splitter, and constrain bubble markdown readers to their containing chat width across minimum and medium viewports.

### Handle SegmentedBase clamping, focus and border jitter

**Problem:** An invalid stored segment can silently appear as option 0 while the repair path is suppressed, so clicking option 0 cannot correct it. Focus can target the track instead of a segment, and a focus border can change layout width.

**Cause or correction:** `coreSegmentedBase` clamps `*selected` into `[0, n-1]` and suppresses `OnChange` when the index does not change. Focus must target a focusable segment radio button, not `parts.Track`. A `FocusVisible` border implemented as `0 -> 2` changes layout. Separately, MyGo `ui.RGBA` takes normalized alpha (`0..1`) rather than byte alpha (`0..255`).

**Prevention:** Represent an invalid selection explicitly or provide a repair path independent of an unchanged index. Focus a segment radio button and keep constant border width (transparent at rest) or draw the ring inside `Draw`. Pass normalized `0..1` alpha to `ui.RGBA` and verify rendered hover pixels, option-0 repair and stable focus geometry.

### Keep validation errors visible and standalone actions aligned

**Problem:** Collapsing an optional path or capacity disclosure can hide active field errors while the summary banner still says fields are highlighted. A direct button child of `ui.Column(c).FillWidth()` stretches across the card.

**Cause or correction:** Disclosure visibility and field-error visibility are independent. A `FillWidth` column stretches direct button children unless alignment is constrained.

**Prevention:** Keep the error region visible or repeat exact field errors when a section collapses, and verify summary and fields agree. Wrap a standalone action in a left-aligned `Row` or set `AlignSelf(ui.Start)`; check width after collapse and at minimum window size.

### Bind Bot management and first-use folder choice

**Problem:** Bot profiles can appear under global Settings and a cached selection can edit the wrong Bot. An empty first-use form can treat the launch directory as the selected work folder.

**Cause or correction:** Management belongs to the clicked Bot Team row; Settings owns global preferences. A fresh connection form must require an explicit folder and must not inherit the launch directory.

**Prevention:** Bind menus, profile actions and drafts to the row’s stable Bot ID; right-click a different Bot and verify the edited Bot, drafts and return focus. Exercise a fresh form with no saved credentials or folder override: Test and Save must not change provider counts or revisions until a folder is chosen, then verify inheritance and overrides across reopen.

### Preserve notice ownership and remove machine codes

**Problem:** One uncertainty or recovery condition can render through several producers, repeating guidance, while ordinary notices expose internal stage or engine-history wording. Removing one prefix can leave related messages inconsistent.

**Cause or correction:** Visible notices have multiple producers, and the composer can select a summary independently of stored diagnostic text. Capability holds need plain copy; machine codes belong in technical contracts.

**Prevention:** Trace each producer through its formatter to the rendered surface, assign one eligible notice, and keep distinct local-save and unknown-delivery diagnostics reachable. Remove internal codes and stage wording together, preserve saved/delivered distinctions and callbacks, and verify exact inverse substitutions across related notices.

### Do not infer secure credentials from masking

**Problem:** A synthetic key was visually masked but remained readable in the accessibility snapshot.

**Cause or correction:** The product and lab password controls share the editor. Source inspection found a Darwin superclass value-cache path and a disabled secure-trait gap; product-native exposure is Unconfirmed.

**Prevention:** Inspect the actual product control and accessibility bridge, then validate a synthetic key under the exact native scope. Visual bullets are not secure AX semantics; do not enter real credentials or claim readiness until value/selection/range reads are verified.

### Retire obsolete panels and their tests

**Problem:** A retired panel and its tests were adapted into capability-hold behavior instead of removed, and a surviving read-only summary lost its full-value tooltip.

**Cause or correction:** Removed features are not requirements to maintain during migration. Source review can miss surviving read-only information.

**Prevention:** Remove the retired panel, entry points, state and dedicated tests together. Inspect remaining read-only information separately and retain its full-value tooltip, shared helpers and live behavior. Do not add placeholder panels or replacement tests for a feature the user no longer wants.

## Product State and Recovery

### Keep admitted work, backup and unknown delivery distinct

**Problem:** Backup was disabled for a settled unknown send, and worker exit marked admitted activity records unknown while host replacement rejected every unresolved execution. A stalled chat could also block drain.

**Cause or correction:** Live work, a settled unknown receipt and an exited host are different states. Replacing an exited host is a connection operation, not permission to resend unknown work.

**Prevention:** Permit backup once execution reaches a saved boundary, preserving the unknown receipt without replay. Permit explicit Apply only for the current owned host after reaping, callback completion and exit state; retain live, pending, store and legacy guards. Verify Apply and reopen make no model request, and count provider calls or durable effects before and after recovery.

### Bind mutations and answers to captured ownership

**Problem:** The Answer action opened a generic supplement form, a superseded question could receive an answer, and Pi callers retained retired client checks or replaced canonical rejection reasons.

**Cause or correction:** A supplement is not an answer. Readiness, history reload and send admission are distinct, and a held question has no delivery state until an answer is saved.

**Prevention:** Capture owner, issue, question reference, revision and origin call; reject noncurrent targets before saving and preserve the draft. Persist answer and receipt before showing saved, dispatching, resolved, unknown or canceled; resolve only the exact held call and never deliver to a replacement run. Reuse canonical admission and readiness decisions, keep exited-host and first-use setup distinct, and render the raw reason rather than English diagnostic text. Compare operation and execution IDs at their documented locations, and count provider calls or durable effects before and after recovery.

### Separate product decisions, domain persistence and legacy admission

**Problem:** A conjecture was treated as architecture, and legacy-shaped fixtures were admitted without checking current persistence. Opaque legacy records can fail before the intended assertion or mask a missing-session case.

**Cause or correction:** Product decisions come from accepted user behavior or a current action, not proposals, old tests or past implementations. Legacy executions remain read-only; current activity admission requires explicitly marked fresh Pi intent, binding and run facts.

**Prevention:** Record accepted behavior and retractions, and trace features/tests to a current action or supported contract. Check admission, save errors and reopen/rerun assertions. Use current synthetic records only for their stated boundary; never retag legacy data, bypass the guard, change history policy for green tests, or infer folder/credential state from launch environment. Set session presence explicitly in recovery fixtures. Remove retired adapters, callers, gates and fixtures together; verify asynchronous preference writes do not recreate deleted domains while preserving current Pi backups and no-replay.

### Restore materials when persistence fails

**Problem:** Pi RunStarted cleared the draft and submitted context before saving; a failed save left unknown state without restoring the user’s materials.

**Cause or correction:** The clear path compacts its backing slice and changes preview selection, so a shallow copy cannot restore the original.

**Prevention:** Capture independent context items and the current draft/preview before clearing. Restore them on save failure while retaining admitted facts, pending intent and unknown/no-replay semantics. Preserve later edits and verify through the actual reducer and process E2E.

### Compare persisted state by its contract

**Problem:** A subset-persistence assertion rejected valid indented JSON, and a review misread an empty/null snapshot distinction as irrelevant.

**Cause or correction:** Whitespace is not part of the selection contract; decoded presence, nullability and values are. A missing `omitempty` keeps nil and nonnil-empty distinct. Original legacy files are a byte-level contract.

**Prevention:** Inspect serialization tags and decode current data to assert values plus omitted/null/empty distinctions. Use the existing nil-preserving clone path, not wrappers or restoration scans. Reserve exact byte comparisons for byte-preserving contracts, and normalize empty material arrays when checking saved answers.

### Keep supported tool catalogs separate from defaults

**Problem:** A keyboard regression supplied supported tools but expected a host-default label without the independent default list; a Retry fixture used a retired stub.

**Cause or correction:** Supported tools and default selections are distinct ready facts. Configured idle Retry presentation does not require a live host; a retired stub proves neither configuration nor Pi readiness.

**Prevention:** Supply explicit independent defaults, preserve nil/empty/subset semantics and exercise actual keyboard controls. Use a fresh current store with saved valid configuration for offline Retry presentation, and keep unconfigured blocking separate from process-level Retry evidence.

### Track saved worker credentials separately from plaintext form drafts

**Problem:** Reopening Settings displayed a saved API key as unset and forced users to re-enter it before saving or testing other fields, while relaxing validation globally weakened standalone plaintext forms.

**Cause or correction:** The product worker omits plaintext keys from IPC snapshots and reports `credentialPresent`, whereas standalone forms supply plaintext keys directly.

**Prevention:** Propagate `CredentialPresent` into the form independently of `Draft.APIKey`, render masked summary bullets and a blank keep-key password input when present, resolve blank keys against the stored credential inside the worker on Save and Test, clear submitted plaintext keys from the form after an accepted Save, and keep blank-key rejection strict whenever a form starts from plaintext or lacks a saved credential.

### Guard SQLite WAL permissions, NUL-safe IDs and mirror recovery

**Problem:** Enabling SQLite WAL mode did not immediately create `-wal`/`-shm` sidecars for permission tightening, CLI startup created a fresh profile parent directory as `0755` before SQLite ran, raw SQLite text bindings do not preserve embedded NULs in arbitrary IDs, and an interrupted JSON mirror publish after a SQLite commit could look like an external recovery edit.

**Cause or correction:** WAL sidecar files appear on the first write transaction, worker CLI directory setup runs before `ProductSQLite`, pinned Node SQLite text bindings require safe encoding for arbitrary IDs, and a durable mirror needs a pre-publish digest marker to distinguish crash recovery from user-restored snapshots.

**Prevention:** Ensure both worker CLI startup and `ProductSQLite` create profile directories with `0700` and apply `0600` to the database plus `-wal`/`-shm` after the first WAL write transaction. JSON-encode arbitrary ID columns, refuse databases whose `PRAGMA user_version` exceeds the supported schema version, commit SQLite before publishing the recovery mirror, and record the prior mirror digest so reopen repairs interrupted publishes while still admitting genuine external recovery copies.

### Protect KV prefix caching and async profile footers for Bot memory

**Problem:** Placing per-Bot identity ahead of shared instructions or sorting tool arrays inconsistently busts LLM KV prefix cache across Bots and turns; hot-reloading memory mid-conversation busts active cache; and rendering an async Memory section above the Archive button shifts the destructive button mid-click.

**Cause or correction:** Providers hash prompts in strict `Tools -> System -> Messages` prefix order, and an asynchronous `memory.get` response expands the scrollable profile content height when loaded.

**Prevention:** Sort all SDK and product tool definitions alphabetically, keep the static L0 Nagi Bot master template first before per-Bot L1 profile and thin L1.5 lasting rules, reload L1.5 only at cold idle boundaries, compaction or explicit user rule edits/approvals while preserving the same Pi transcript, and anchor Bot management (Archive/Restore) in a fixed footer outside the scrolling memory region.

## Pi, SDK, and Process Integration

### Verify exact SDK types and owned defaults

**Problem:** An adapter used a nonexistent settings setter, treated credential metadata as contents, duplicated transcript types and manually flushed file descriptors. Default changes globally updated existing contexts.

**Cause or correction:** Pinned Pi exposes `SettingsManager.applyOverrides`, an `InMemoryCredentialStore`, public transcript types and a file-writer flush option. Production runs compiled JavaScript. Callers require bound model/limits and an immutable endpoint, wire and credential source per context.

**Prevention:** Read exact declarations and exported helpers before implementing an adapter. Compile the smallest boundary against the delivered runtime, use Pi types and TypeBox schemas, and preserve compiler failures. Capture connection source and per-context configuration; change model/limits only through an explicit idle-target operation, report nonsecret readback, and verify one context stays bound when defaults change.

### Keep one IPC type map and authoritative binding facts

**Problem:** The assembled Go client had duplicate frame/history types. Binding checks rejected canonical path aliases and SDK-proven unused-to-used upgrades, while recovery admission accepted foreign execution IDs.

**Cause or correction:** Duplicate declarations came from assembled source. A canonical path can represent one directory, and saved public entries can prove use; ownership correlation must still match.

**Prevention:** Keep one canonical typed wire map, check symbol uniqueness and compile the package before assigning consumers. Compare canonical path identity, accept only monotonic use evidence and keep missing used sessions unavailable. Correlate execution links with creating receipts or preserved import ownership, reject duplicate/foreign associations, and test actual temporary-directory reopen.

### Preserve active work through shutdown, cancellation and probe cleanup

**Problem:** The Pi client closed stdin after a proved-busy shutdown, Quit waited only for chat, and a stalled chat could block drain. Async authorization could emit a bridge-drain error after stop. A timed-out probe could receive busy and remain unretired. Readiness and cancellation used wrong values.

**Cause or correction:** A rejected shutdown is not permission to terminate the worker. Quit must cancel every captured owner; a pre-send check does not close an in-flight race. The worker reports `process.version` with a `v` prefix; cancellation must use the admitted owner/context/run.

**Prevention:** Keep busy rejection separate from transport-unknown teardown. Quit must cancel every captured owner, including chat admitted during shutdown, then observe settlement, callback completion and reaping. Recheck the stop signal after async authorization while retaining attempted-RPC errors. Retain a probe until actual reaping and callback completion. Match canonical readiness and exact cancellation targets.

### Preserve notification sequences and bind MCP configuration integrity

**Problem:** A notification counter was recreated per open while the consumer validated monotonicity across the worker epoch/context. Reusing an MCP source label after reopen could retarget an old context.

**Cause or correction:** Reopen must not reset producer sequence. Saved source labels are not full configuration identity; raw values must stay outside history.

**Prevention:** Keep context counters with the worker until its epoch ends and use explicit attempt IDs instead of weakening sequence validation. Store a canonical configuration digest with public binding metadata, retain opaque source identity separately and reject drift before transports. Exercise close/reopen and same-label/different-configuration reopen with actual events.

### Decode and admit public history safely

**Problem:** A decoder applied a tool-result schema to every SDK message, timestamps were dropped, an empty used session was opened before rejection, and app-context suffixes could be mistaken for user text.

**Cause or correction:** Public messages are discriminated by role. `SessionManager.open` initializes an empty file, so used missing/empty references must be rejected first. Model input and original user input differ.

**Prevention:** Read the entry/message discriminator before decoding content, and preserve heterogeneous SDK records. Map public timestamps through the existing formatter. Classify used missing/empty references before open and preserve bytes; recreate only durably proven-unused bindings. Project original input from proven public custom-entry metadata, retain actual entry IDs, and do not parse journals or strip user text heuristically.

### Contain optional observers and export child outcomes

**Problem:** Optional-hook lookup initially sat outside error containment. App/fixture exit did not prove worker exit, and recorder files could appear before terminal receipt and lease release.

**Cause or correction:** Reading an instrumentation property can throw before dispatch. A mapped helper status, missing child or existing file is not process completion.

**Prevention:** Contain lookup, capture and scheduled dispatch in one boundary, and state scheduling/loss limits. Correlate each owned child with its actual wait result and raw code/signal, including interruption, and keep app, worker, provider and cleanup outcomes separate. Release gates and complete cleanup before final provider counts; wait for terminal/owner release before manual analysis.

### Build worker payloads from verified inputs and compiler

**Problem:** App assembly used a hashing API unavailable in the packaging interpreter. One build wrote inside the checkout; another treated a finished payload as the runtime input. The compiler came from an unrecorded mutable tree.

**Cause or correction:** The worker needs a verified staged Node runtime and fresh external output. A lock describes intended inputs, not the actual compiler tree; output is not a runtime input.

**Prevention:** Use supported incremental hashing. Pass verified Node staging and a fresh external output; install locked dependencies externally and compile against that tree. Record actual compiler version/files/install receipt, verify pre/post input bytes, prune development dependencies, and do not claim a prior payload validates a corrected builder.

### Select pinned compiler and formatter, then parse wrappers

**Problem:** Offline phases selected system Go 1.27.1 instead of cached 1.27.3, and a formatting phase probed the wrong formatter. A wrapper had a missing parenthesis; a last-function deletion left an EOF blank line; shell quoting failed before execution.

**Cause or correction:** Cached labels and system installations are not pinned-tool evidence. Wrapper syntax and formatting are prerequisites, not product failures.

**Prevention:** Resolve required cached compiler/formatter paths, record exact executables and argv, and reuse them in owner fences. Run the pinned gofmt listing over the committed tree, including tagged files and final-declaration removals. Parse Python AST and check shell/JS quoting before a long phase; preserve failed wrappers and do not count unexecuted steps as passed.

## Verification and Evidence

### Keep development verification at integration boundaries

**Problem:** Earlier development used failure-first unit edits and unit-suite validation under the previous process.

**Cause or correction:** Current rules prohibit developer TDD and developer-written/modified unit tests. Development verification is integration/E2E; unit maintenance belongs to the test owner.

**Prevention:** Fence test ownership explicitly, reuse observable integration/E2E cases, and send unit compatibility work to the test owner. Do not weaken required gates to hide an incomplete migration.

### Freeze actual bytes before reporting digests

**Problem:** Handoffs announced placeholder digests, assumed compiler/SDK inventory, and stored displayed placeholders in digest fields. Inverse reconstruction and image-type checks were misdiagnosed.

**Cause or correction:** These were metadata or verifier errors; the corrected reconstruction matched without changing source.

**Prevention:** Freeze actual bytes, derive versions/counts/digests programmatically, and compare explicit file sets while reporting extras. Preserve exact block separators and diagnose the verifier before changing source. Keep machine-readable digest fields exact; suppress display instead of substituting placeholders. Check image magic bytes and dimensions before choosing an extension or MIME type.

### Separate setup failures from declaration or SDK defects

**Problem:** Isolated declaration checks first produced missing-module diagnostics, then equal strict diagnostics on baseline and proposed trees; the public caller passed only with upstream checking skipped.

**Cause or correction:** Packet-local resolution fixed setup. Baseline and proposed diagnostics were otherwise equal; neither established a full declaration/runtime PASS.

**Prevention:** Resolve isolated dependencies read-only, compare baseline and proposed diagnostics, and report setup, caller and runtime separately. Do not change SDK code/versions to conceal an unchanged baseline failure.

### Select process tests and guard preview outputs

**Problem:** Default headless CI ran without pinned Node/Pi inputs while process E2E roots require them and fail when absent. Moving tests into `internal/app` exposed an unguarded preview write that leaked `headless-*.png` into the source tree and broke clean-checkout checks.

**Cause or correction:** Headless package tests, Pi process integration and preview export have distinct runtime contracts; root-only ignores hid the leak.

**Prevention:** Scope the headless GUI job to non-Node packages and run Pi integration with `-tags=integration`, pinned inputs and required roots. Guard every preview write with `NAGI_PREVIEW_DIR != ""` and ignore `headless-*.png` in every directory. Do not skip missing inputs or weaken failure rules.

### Reassemble logs and match evidence to the product boundary

**Problem:** Receipt analysis decoded individual Go JSONL chunks and failed on strings split across events; a recorder copied obsolete field names. A brief demanded raw packets the product client does not expose.

**Cause or correction:** One log message can span events, and producers differ. Public SDK history, the model request carrying the result, delivery state and owned settlement can establish an outcome without an event subscriber.

**Prevention:** Concatenate each test’s output before decoding bounded records. Check current producer fields, retain observed counts when a final receipt is absent, and separate analysis errors from product failures. Specify the assertion and actual evidence before requiring instrumentation; keep unobserved packets explicit and do not add a test hook or import another fixture’s receipts.

### Decode tool metadata and inspect process dependencies

**Problem:** A cleanup classifier embedded JSON booleans as Python literals. A raw probe omitted the request type, an HC wait used an unsupported flag, and CI logs were empty after gh rejected escape sequences. Cleanup filtering by executable name missed a Python process whose cwd referenced a retained path.

**Cause or correction:** Serialized data is not executable code; HC has name/flag constraints. A dependency can exist in cwd, executable, arguments or open files, and nonempty `lsof` matters even on nonzero exit.

**Prevention:** Read metadata with a JSON loader or data file and validate tool names before launch. Build probes from the documented envelope, check wait flags, and collect escape-bearing logs with raw output while separating collection errors. Inspect all-process exact-path references before retirement, preserve live dependencies, and never signal an uncertain owner.

### Discover actual paths, close callers and run root tooling

**Problem:** Searches named absent files, edit anchors were used before reading the range, and changes passed selected checks but failed compilation on missing callers/imports.

**Cause or correction:** Path/symbol discovery and complete diagnostics locate the real surface; selected searches do not resolve the package.

**Prevention:** Discover files/symbols before targeted reads and verify cited symbols against their producer. Read the exact declaration and callee before replacement, include methods when mapping callers, and avoid keyword matches that find a different guard. Collect complete root-package diagnostics before another closure; check imports and avoid cosmetic changes after freeze. Run locale generation from the checkout root (`mygo.json`) and verify accessors against both catalogs.

## Ownership and Delivery

### Respect frozen candidates and writer ownership

**Problem:** A repository path was treated as the current candidate, and edits were attempted in a dirty root shared with other writers. A partially edited worktree was mistaken for a frozen-candidate failure.

**Cause or correction:** A path does not establish source identity or writer ownership. The selected branch, status and active owners define the usable candidate; missing symbols can reflect an in-progress edit.

**Prevention:** Read branch/PR identity, status and active writers first, then use a clean task worktree from the selected committed source while preserving dirty work and other owners’ worktrees. Wait for shared-code owners to release paths before building/editing, and treat frozen-candidate bytes as the verification basis.

### Complete the worktree lifecycle

**Problem:** Completed tasks left worktrees and branches behind, and a tagged build generated test runners while another writer removed test functions.

**Cause or correction:** Worktrees isolate conflicting work; their number follows actual need. Moving source cannot serve as frozen test input, and disposable resources are not project records.

**Prevention:** Assign every temporary worktree/branch an owner, purpose, source and completion condition. Stop writes before validation, collect the result, then remove resources after checking live path dependencies; preserve data, profiles and evidence. Build from the committed checkout, not a changing worktree.

### Persist completed work and evidence

**Problem:** Temporary files and worktrees can be removed during routine cleanup, losing contracts and validation summaries.

**Cause or correction:** Temporary paths are disposable, not the authoritative project record.

**Prevention:** Commit and push coherent checkpoints, keep contracts and validation summaries in Git/PR records, and merge after required gates. Export process receipts before test temporary directories are cleaned, including failures; do not rely on a temporary evidence path.

### Verify owner/model identity and use concise names

**Problem:** A displayed redaction token was copied as identity, a role was guessed, and a model label disagreed with the live session. Branch names repeated product/runtime/date/role prefixes. Team sessions ran invisibly, and expensive models handled routine inventory.

**Cause or correction:** Display substitutions and labels are not routable identity. Live model metadata defines the active model; branch identity is not a naming requirement. Team roles must be visible, with allocation proportional to difficulty.

**Prevention:** Read current HC identity, project, role, task, state and live model before dispatch; never copy redaction tokens or relabel an unexecuted task. Use short branch names. Keep team agents visible as named tabs in the product Space with role/model/thinking labels rather than hidden background subagents, using one full-stack writer per checkout plus an independent reviewer/QA verifier (Antigravity) and a shared DeepSeek Flash scout; spawn a second Dev worker in an isolated temporary worktree only for genuinely disjoint parallel tracks. Reserve Astra for escalated, evidence-backed blockers; bound xhigh screenshot batches and steer long workers once evidence is collected. Report completed user actions, blockers, owners and next steps, distinguishing native, integration and unit evidence.

### Keep explanations outside implementation bodies

**Problem:** Earlier code allowed inline/body explanations, and a patch report mistook unchanged upstream context for new comments.

**Cause or correction:** Rules prohibit end-of-line and implementation-body comments. Contract documentation is allowed only where naming, types and signatures cannot express the caller-visible requirement.

**Prevention:** Use clear names and small functions, inspect added source for prohibited comments, and place necessary independent explanations outside implementation bodies. In patches, distinguish added lines from upstream context and verify whitespace after application.

### Preserve human drafts before terminal delivery

**Problem:** Sending screenshot materials through HC replaced an old unsent human draft; an idle attempt had rejected the observed status without typing.

**Cause or correction:** HC’s draft hold expires; it does not preserve an old draft indefinitely. Elapsed quiet time is not consent to replace input.

**Prevention:** Inspect actual terminal input before delivery. When a human draft exists, share material files/links without sending another prompt. If replacement occurs, restore the exact original without submission and verify the unchanged turn, unsent input and terminal size; named-key input cannot replace raw Unicode paste.

### SQLite parent rewrites require deferred non-cascading child foreign keys

**Problem:** `ProductSQLite.replaceProfile` deletes and reinserts parent rows (`bots`, `conversations`, `activities`, `executions`) on every ordinary save, so adding child tables like `activity_traces` with `ON DELETE CASCADE` would wipe trace history on every draft save and immediate FKs would abort saves; emitting new empty arrays on legacy rows also breaks `pending_snapshot_previous` digest verification during crash-repair reopen.

**Cause or correction:** Sidecar tables that survive parent rewrites must use `DEFERRABLE INITIALLY DEFERRED` (`NO ACTION`) foreign keys bound with `encode(id)` JSON-encoded keys and clear prior generations only on explicit snapshot import/replacement, while omitting empty additive fields on legacy canonical serialization.

**Prevention:** Audit `replaceProfile` and `pending_snapshot_previous` before adding any SQLite child table or Activity/Bot profile column, and include both unrelated-save retention and pending-mirror migration checks in integration tests.

### Cold reload must preserve captured L0 and L1 profile bytes

**Problem:** Recomputing the entire system prompt from current `bot.profile` during a cold reopen of an existing conversation binding overwrote the conversation's captured L1 identity and instructions after an in-place profile edit (`StartNewConversation: false`).

**Cause or correction:** Existing bindings capture L0 and L1 identity/behavior instructions through the working-directory block; only dynamic trailing blocks (`<available_skills>` metadata catalog and L1.5 `Lasting Rules`) may refresh at cold or explicit-reload boundaries, and unchanged composed prompts should skip runtime refresh altogether.

**Prevention:** Keep cold-reload prompt composition strictly additive over the captured L0+L1 prefix of `existing.instructions`, and run `TestPiProductSQLiteSavedKeyAndInitialInstructions` alongside new cold-reload integration tests.

### Redact detached backup snapshots before residual secret checks

**Problem:** Running a whole-profile secret scan directly inside `profile.backup` rejected backup creation whenever immutable append-only history (`operations`, `executions`, `activities`) contained a previously pasted active, inactive, or retired Provider/MCP credential, permanently locking the user out of recovery exports while earlier unredacted operation replies could otherwise carry raw secrets into backup files.

**Cause or correction:** Historical operation replies and execution inputs cannot be edited by the user, so defense-in-depth secret checks must inspect a detached export copy whose free-text user/historical payloads have already been redacted (with bounded UTF-8 quote normalization and strict `decodeProductProfile` preservation) rather than blocking on raw history or mutating live SQLite/mirror bytes.

**Prevention:** Redact free-text historical and draft fields on a detached export snapshot before running residual MCP/Provider secret checks across the complete backup envelope, verify strict readback and zero leaked secret bytes in the written backup file, and confirm the live `contacts.json` mirror stays byte-identical across backup.

### Read large Node stdin pipes asynchronously on macOS

**Problem:** Piping large single-JSON recovery backups (68 KiB+) from Go into a short-lived Node validator using `readFileSync(0, 'utf8')` failed intermittently on macOS with `EAGAIN`, even though validating the exact same bytes from disk succeeded.

**Cause or correction:** Synchronous `readFileSync(0, ...)` does not handle non-blocking partial reads on large OS pipe buffers on Darwin.

**Prevention:** Consume `process.stdin` asynchronously (for example with `await text(process.stdin)` from `node:stream/consumers`) in any Node helper invoked over Go `exec.Cmd` stdin pipes, and exercise recovery validation with multi-record 60 KiB+ single-JSON payloads.
