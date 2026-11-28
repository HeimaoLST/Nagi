# Nagi development lessons

## UI and Native Interaction

### Review composed pages and record a verdict

**Problem:** Component checks, geometry and captures can pass while the composed page is misaligned, hides a control, repeats a notice, collapses primary/secondary/danger buttons into borderless ghost text, or lets 14px helper prose overpower setting labels and section headers. An uninspected capture is not evidence.

**Cause or correction:** Visual acceptance depends on the rendered page, not source, test status or image count. Capture and review are separate steps, and unbordered ghost actions plus verbose disclaimers destroy visual hierarchy.

**Prevention:** Inspect screenshots of actual composed pages at applicable sizes, themes, languages and states against `docs/design/desktop.md` and `docs/design/ui-checklist.md`. Enforce explicit four-tier typography (page title > section header > setting label > muted concise wrapped helper), distinct Primary/bordered Secondary/red Danger/Link controls, control-first Settings (segmented switches, presets and concise disclosures over stacked textboxes/prose), and centered wide-screen alignment. Check control bounds and scroll position, keep a failure frame when a click cannot be verified, and record the reviewer and verdict against the frozen candidate.

### Sample periodic animations across non-symmetric phases

**Problem:** Checking a sinusoidal animation with a single quarter-period step (`450 ms` on an `1800 ms` cycle) produces identical start and end opacity whenever wall-clock start lands near a symmetric pair (`sin(pi/4) == sin(3*pi/4)`).

**Cause or correction:** Comparing only two points separated by `pi/2` or `pi` on a wall-clock `UnixMilli` sinusoid leaves a symmetric zero-delta window.

**Prevention:** Advance and sample across at least two asymmetric steps (such as `+300 ms` and `+600 ms` on an `1800 ms` cycle) and take the maximum pixel delta so normal-motion tests stay deterministic across CI runners while `ReduceMotion` still checks `0`.

### Keep control text and background in the same theme during a switch

**Problem:** Immediately after switching from Moss Dark to Paper Light, Memory approval rendered dark text on the previous dark button fill, and any control calling `.Transition(ControlTransition)` directly (bypassing `theme.TransitionControl`) kept interpolating the prior theme's background or border across the 180 ms transition.

**Cause or correction:** MyGo interpolates background and border colors while applying new text colors immediately. The 180 ms interaction transition carried the old palette into the new one whenever a control registered `ControlTransition` unconditionally without checking a cached theme palette.

**Prevention:** Route every animated control helper (including `actionButton`, `NavigationControl`, and app-level button builders such as `providerDirectoryButton`) through `theme.TransitionControl` so theme switches snap on the first frame while intra-palette hover/press/toggle states remain animated. Inspect the first frame after changing themes in both directions, with populated controls visible.

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

### Align bordered credential strips with form inputs

**Problem:** The saved API key strip sat below the label’s vertical centerline in the provider modal.

**Cause or correction:** Vertical padding and a border around 32 DIP `TextAction` buttons made the strip taller than the input height used to align `providerFormRow` labels.

**Prevention:** Keep the bordered strip close to the height of adjacent inputs and run `TestVisualGuardProviderModalFormAlignment` in English and Simplified Chinese.

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

### Classify connection setup from explicit startup signals

**Problem:** Retiring the legacy form misclassified unconfigured launches and disconnected legacy profiles in the connection status.

**Cause or correction:** Runtime failure and the absence of a saved connection do not determine first-use setup. For a runtime that has not exited and has no saved connection, the connection-required notice or enter-connection startup hint identifies setup.

**Prevention:** Preserve the startup signals when removing form state. Verify the rendered status for an unconfigured launch, a legacy profile without a live host, and a local save failure alongside connection diagnostics.

### Bind mutations and answers to captured ownership

**Problem:** The Answer action opened a generic supplement form, a superseded question could receive an answer, and Pi callers retained retired client checks or replaced canonical rejection reasons.

**Cause or correction:** A supplement is not an answer. Readiness, history reload and send admission are distinct, and a held question has no delivery state until an answer is saved.

**Prevention:** Capture owner, issue, question reference, revision and origin call; reject noncurrent targets before saving and preserve the draft. Persist answer and receipt before showing saved, dispatching, resolved, unknown or canceled; resolve only the exact held call and never deliver to a replacement run. Reuse canonical admission and readiness decisions, keep exited-host and first-use setup distinct, and render the raw reason rather than English diagnostic text. Compare operation and execution IDs at their documented locations, and count provider calls or durable effects before and after recovery.

### Reject Activity detail reads after the drawer target changes

**Problem:** Returning to the Activity list while a close-triggered detail read was in flight could repopulate the closed detail and hide the list.

**Cause or correction:** The response guard treated the empty Activity ID as an exception, and the renderer displayed a nonnil detail even after Back cleared its target.

**Prevention:** Apply each detail response only when the captured target still equals the selected Activity ID, including the empty list target. Verify Back after a lifecycle mutation while a read is pending.

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

### Pass the locale directory to the standalone catalog generator

**Problem:** The standalone catalog generator scanned the repository root for languages. The patched MyGo CLI generator failed in readonly mode because its RPM dependency checksum was absent from Nagi's application module.

**Cause or correction:** The standalone generator resolves `*.json` relative to its first argument. `scripts/gui-go.py` builds MyGo with a temporary copy of the application's module and checksum files.

**Prevention:** Pass the `locales` directory to the standalone generator. For the patched MyGo CLI, run `GOFLAGS=-mod=mod python3 scripts/gui-go.py run github.com/egoist/mygo/cmd/mygo generate .` from the checkout root and verify the generated accessors against both catalogs. The wrapper resolves generator-only dependencies in its temporary module.

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

### Verify mid-history system-message positions on complete outbound wire bodies

**Problem:** A Pi `context` extension that edits a cloned message in place can preserve mid-conversation `system` messages inside the extension runner while the downstream provider adapter still folds them to the front during wire serialization, silently altering the provider request prefix.

**Cause or correction:** Extension-layer message preservation and provider-adapter serialization are independent steps; both `openai-completions` and `openai-responses` adapters fold mid-history system messages unless model compatibility (`compat.supportsMidConvoSystemMessages`) is explicitly configured.

**Prevention:** Configure adapter compatibility explicitly for foreground sessions, never replace visible message objects or append synthetic messages in `context` handlers, and verify complete serialized outbound request bodies across both wire APIs rather than checking only extension arrays or `systemPrompt`.

### Separate Pi steer continuation from Nagi escalation in tests

**Problem:** Injecting a provider error while a queued steer was still pending caused a second provider request inside the same Pi SDK run during escalation verification, obscuring whether a durable replacement execution had been admitted.

**Cause or correction:** Pi can continue the active SDK run to process a queued steering message after an assistant error, so raw provider request count and durable Nagi replacement execution count measure different mechanisms.

**Prevention:** Trigger controlled provider failures after observing steer consumption when testing M4 escalation exclusion, and assert durable `activity.executions` count, routed models, and `activity_escalations` rows separately from raw HTTP request counts; also dispatch mock MCP HTTP stream probes by method before parsing request bodies.

### Project complete provider defaults, case-insensitive protection and sibling scoring in model catalogs

**Problem:** A cherry-picked catalog supplement left most built-in OpenAI-compatible models without tool evidence, marked Responses-only OpenAI Pro SKUs as non-chat because they inherit `provider.toml` defaults instead of repeating `[api.*]`, dropped DeepSeek `toggle=true` `off` support, compiled family/price-protection regexes case-sensitively so uppercase Pro IDs bypassed opt-in and mixed-case families failed to match, let lexicographical deduplication shadow same-provider `:free` siblings on `economy`, and wiped freshly supplied `modelOverrides` on endpoint repoints.

**Cause or correction:** Catalog generators must read provider-level transport defaults, `base_model` inheritance, nested IDs, and `toggle` semantics across all supported providers rather than cherry-picking samples; cost-protection patterns must be case-insensitive; canonical deduplication must preserve applied provider order across distinct providers while scoring siblings within the same provider; and repoint invalidation must drop only omitted or unchanged copied overrides.

**Prevention:** Project the full pinned upstream inventory for every supported OpenAI-compatible provider with deterministic allowlists, compile and validate family regexes case-insensitively, test both same-account and cross-account canonical siblings plus mixed-case Pro SKUs and endpoint repoints with fresh overrides, and keep an unchanged-editor 200+ ID discovery integration gate green.

### Distinguish caller cancellation from SDK cleanup aborts and transport failures from semantic escalation

**Problem:** An interrupted HTTP 200 model stream can look like a local fetch abort because Pi's OpenAI SDK cancels its internal `AbortController` during stream cleanup, and older utility-escalation fixtures triggered HTTP 5xx provider errors that conflict with the zero-transport-replay rule.

**Cause or correction:** Only explicit caller cancellation or shutdown signals represent non-poisoning user aborts; a stream that drops or errors after flushing HTTP 200 is a route-level transport/stream failure. Likewise, durable operation admission (`accepted`) and recovered execution state (`unknown`) are independent facts, and M4 utility escalation applies only to zero-mutating semantic failures (such as `finish_reason: "length"` truncation), never to retrying transient `429`/`5xx`/network errors on another Provider.

**Prevention:** Classify user abort exclusively from the caller/run cancellation signal rather than the SDK's internal fetch signal, verify flushed-200 interrupted streams across both wire APIs, and keep separate integration assertions for transport-failure zero-escalation versus semantic utility escalation.

### Elevate concrete user examples into general runtime primitives

**Problem:** Architecture drafts framed around a user's concrete example (such as remote SSH shells, CI watchers, or external agent monitoring) narrow core schemas and runtime boundaries to single use cases, while shortcutting macOS PTY hosting with `/usr/bin/script` or Pi `sendCustomMessage` fails worker stdio and transcript-role invariants.

**Cause or correction:** Concrete workflows are downstream compositions of three scenario-agnostic runtime primitives—worker-supervised stateful session handles (using a bundled native `forkpty` supervisor rather than `node-gyp` or `/usr/bin/script`, which exits 1 on socket stdio), standing-grant-gated asynchronous watches/schedules, and a private orchestration actor projecting product-owned timeline notices (since `sendCustomMessage` enters Pi provider payloads as role `user`).

**Prevention:** Keep core schemas, RPCs, tools, and acceptance fixtures completely domain-agnostic, verify standalone macOS OS mechanisms with bundled Node and empty `PATH`, and bind unattended command schedules to explicit installation-local grants imported paused from backups.

### Enable continuous autonomous Bot orchestration in a scoped compact context

**Problem:** Treating UI "zero-LLM digest hints" as a global rule against unprompted autonomous turns—or bounding autonomous runs with low turn caps—causes Bots to stall whenever the human leaves the foreground window, whereas running wakes directly on full public Conversation transcripts inflates token consumption and pollutes user chat history.

**Cause or correction:** Users want authorized Bots to continue collaborating, operating handles, and scheduling multi-hour or overnight work autonomously while the human is away, surfacing only what warrants attention and controlling cost through compact scoped orchestration contexts and efficient routing rather than halting execution.

**Prevention:** Isolate autonomous orchestration in a dedicated private `OrchestrationActor` with rolling checkpoints and full projected-request budgets, queue human sends cleanly ahead of subsequent autonomous turns, split window hide from explicit `Cmd-Q` shutdown, and apply exponential backoff strictly per causal failure/no-progress loop rather than halting productive Bots.

### Pair single-clock health guards and key UI metadata by stable identifier

**Problem:** Checking half-open route availability with separate `Date.now()` calls between `claim` and `cooldown` could observe an expired cooldown after claim failure and pass `undefined` into `new Date()`, sorting caller arrays in place silently reordered caller telemetry, indexing UI tool descriptions positionally against `pi.ResolveCodingTools` coupled presentation order to cross-package tool sorting, and empty-locale fallback (`LocalizedText.Resolve("")`) matched `"-"`-prefixed keys while empty route-health reasons left leading `" · "` separators.

**Cause or correction:** Clock progression between consecutive guard checks breaks coupled invariants, `Array.prototype.sort` mutates its receiver, positional indexing assumes two independent lists share length and order, unguarded `strings.Cut` yields an empty prefix that matches `"-"` keys, and unconditional slice initialization treats empty fields as populated text.

**Prevention:** Share a single timestamp snapshot across `claim` and `cooldown` fallbacks (with injectable deterministic clocks for unit tests), sort shallow array copies in pure projection helpers, key UI tool descriptions by stable tool name, guard empty language prefixes before prefix matching, and omit empty fragments before joining UI badges/notes.

### Preserve Mach-O LC_UUID on macOS 27, prefix C enums against SDK macros, and read SDK provenance from linked Mach-O headers

**Problem:** Compiling the native PTY helper with `-Wl,-no_uuid` caused macOS 27 `dyld` to abort launch with `SIGABRT` (`missing LC_UUID load command`), unprefixed C enum constant `MAX_INPUT` collided with macOS 15 Xcode 16.4 `<sys/syslimits.h>` (`#define MAX_INPUT 1024`) even when macOS 27 CLT omitted that transitive include, `xcrun --show-sdk-version` failed under the active CLT configuration even when `clang` compiled, and standalone integration test helpers failed when CLI runs passed `NAGI_TEST_NODE` without setting `NAGI_PI_PAYLOAD`.

**Cause or correction:** Apple's linker generates a deterministic content-based `LC_UUID` when compiling relative source paths with `-fno-ident` and omitting `-Wl,-no_uuid`; macOS SDK headers reserve short unprefixed C macro names across minor SDK releases (`syslimits.h`); the linked binary's `LC_BUILD_VERSION` (`vtool -show-build`) reliably records the resolved macOS SDK version; and `NAGI_PI_PAYLOAD` sits predictably two directories above `NAGI_TEST_NODE` (`.../pi/bin/node`).

**Prevention:** Keep Apple's default deterministic `LC_UUID` on bundled macOS helpers, include `<limits.h>` and `<sys/syslimits.h>` explicitly while prefixing all internal C enum/macro identifiers (`NAGI_*`), verify byte-identity across independent build directories with `cmp`, inspect SDK provenance from linked Mach-O build metadata (`vtool -show-build`) when CLT omits `xcrun --show-sdk-version`, and fall back from `NAGI_PI_PAYLOAD` to `dirname(dirname(NAGI_TEST_NODE))` consistently across integration test helpers.

### Stage precheck executables before Seatbelt launch and hash full redacted observations past visible truncation caps

**Problem:** Hashing only the visible `64 KiB` output slice let tail-only output changes appear identical, running a Seatbelt `executable` precheck directly on the mutable workspace binary opened a TOCTOU swap window, and a `1000ms` cold Seatbelt fixture timeout timed out during isolated bundle launch under `-race`.

**Cause or correction:** Visible excerpt truncation (`1024` bytes / `64 KiB` `truncated` flag) serves UI/LLM payload bounds whereas digest change detection needs incremental hashing up to the `1 MiB` safety ceiling; staging verified binary bytes (`O_NOFOLLOW`, SHA-256 check, `0500`) into an isolated scratch directory prevents swapped-binary execution while `nagi-pty-supervisor` preserves worker-crash reaping; and cold Seatbelt process setup under `-race` needs realistic fixture deadlines.

**Prevention:** Stage and digest-verify every `executable` precheck binary before entering Seatbelt, hash full redacted normalized output up to the hard safety cap together with `truncated` and exit status while capping stored excerpts separately, and give Seatbelt process fixtures `>= 3000ms` runtime headroom (`10000ms` under `-race` integration suites) while keeping user-configurable floors explicit.

### Fall back from null codingTools to engine defaults before capability gating

**Problem:** Capability checks using `!resolveCodingTools(profile)?.includes('bash')` treated a `null` result—used by default and v10-migrated Bots to inherit all default engine coding tools including `bash`—as falsy, stripping `session_*` tools from tool inventory and runtime execution even when `bash` was active.

**Cause or correction:** `resolveCodingTools` returns `null` rather than the explicit array when no override or preset narrows the built-in tool list, whereas empty `[]` explicitly disables coding tools. Both the tool advertisement gate and runtime authorization guard in `sessionTools` (`internal/pi/worker/product.ts`) must resolve `resolveCodingTools(profile) ?? this.engine.defaultCodingTools` before checking tool membership.

**Prevention:** Never apply negative optional-chaining checks (`!arr?.includes(...)`) to nullable capability resolvers where `null` means full defaults; normalize nullable capability lists to the active default set in one helper or shared expression at every inventory and runtime enforcement site, and preserve built-in and `none`/`readonly` profile coverage alongside omitted-default (`nil`/`""`) Bots.

### Delegate field-caption clicks to inputs, return errors out of UI pump closures, and look up Bot indices dynamically

**Problem:** Clicking a form caption (`capabilityText`) that shared its string label with the nested `preferenceInput` below it satisfied MyGo `Tester.Click` on the static caption without focusing the input; calling `t.Fatal` inside `w.pump.do` terminated the UI pump goroutine before signaling completion and deadlocked the test; and hardcoding contact list indices (`0`/`1`) collided with the prepended built-in `PandoraBox` entry.

**Cause or correction:** Non-interactive caption labels earlier in layout order intercept text-based clicks unless the caption explicitly delegates focus to its associated field; `t.Fatal` (`runtime.Goexit`) must never run inside helper goroutines that own channel handshakes; and `openProductProfile` prepends system Bots before persisted user Contacts.

**Prevention:** Delegate `OnClick` on custom field captions to request `.Focus()` on their associated input, capture check errors from `w.pump.do` closures and call `t.Fatal` outside the closure on the test goroutine, and resolve test contacts by stable Bot ID via `slices.IndexFunc`.

### Keep private orchestration checkpoints structured, withhold post-settlement notices until turn finality, and separate pending non-dispatch from crash uncertainty

**Problem:** Falling back directly to raw private assistant text during orchestration checkpoint rotation risked persisting unbounded private reasoning, publishing `conversation_notify` during an active foreground send could overtake that send's settlement order or fail on empty transcripts, and treating queued `conversation.send` waits identically to dispatched runs conflated pre-dispatch interruption with uncertain external execution.

**Cause or correction:** Checkpoint fallback must synthesize structured disposition and source facts when no explicit advisory summary is available; `post_settlement` notices must remain withheld behind the active foreground execution anchor until `completed`, `failed`, `interrupted`, or `unknown` finality while `conversation.timeline` keeps transcript availability separate from committed product notices; and queued sends must record explicit `dispatched_at` transitions so pre-dispatch cancellations and worker crashes settle `pending` human sends as `rejected` (`delivery: "non_dispatched"`) with intact drafts and zero replay while leaving in-flight turns `unknown`.

**Prevention:** Record structured fallback summaries outside raw private Pi transcripts, serialize per-conversation timeline anchors across all send finality states without touching `Draft`, and track pre-dispatch queue state (`dispatched_at == null`) separately from admitted executions in both live cancellation and crash recovery.

### Guard unsigned sizing, deterministic persistence and clock domains

**Problem:** Pre-sizing `strings.Builder` with `int(next - from)` on `uint64` stream offsets risks unsigned underflow and huge allocations when `from > next`, iterating Go maps directly when persisting `.ui.json` pending watch or session-close operations yields nondeterministic serialization, `OutputRing.append` crashes if called with an empty buffer at offset `0`, mixing a synthetic scheduler clock with Pi's wall-clock conversation timeline (`appendNotice` vs transcript messages) misorders notices into the past, and exported methods on unexported `internal/app` structs leak past the single-entrypoint package boundary.

**Cause or correction:** Unsigned subtraction wraps silently in Go, map iteration order is randomized, `OutputRing` assumed at least one segment exists after appending, scheduler/LRU clocks may advance on synthetic epochs (`deps.now()`) while `conversation_notices` interleave by `createdAt` against real transcript wall-clock entries (`new Date().toISOString()`), and Go package boundaries depend on unexporting internal UI helpers.

**Prevention:** Guard unsigned length calculations with `next > from` and an explicit cap before `Grow`, sort slices derived from Go maps before persisting `.ui.json` state, short-circuit empty writes in ring buffers, keep scheduler/backoff/session-LRU clocks separated from conversation/UI wall-clock timestamps, and verify `internal/app` non-test files export only `Run`.

### Distinguish queued cancellation from provider admission rejection

**Problem:** Treating every `delivery: non_dispatched` send receipt as a queued interruption hid provider admission errors behind a misleading cancellation message.

**Cause or correction:** Credential and routing failures use the same delivery value as sends cancelled or interrupted in the queue. Only the explicit queue reasons prove that outcome.

**Prevention:** Show the calm queue message only for `cancelled_before_dispatch` or `interrupted_before_dispatch`. Keep other rejection receipts on the provider admission path.

### Require worker readiness for enabled headless product controls

**Problem:** Decision options and timeline inspect actions appeared disabled or missing in headless captures with an unverified stub worker.

**Cause or correction:** Product mutation and navigation controls depend on the worker staying reachable, while the headless fixture did not establish readiness.

**Prevention:** Start the pinned worker with a disposable profile and wait for `Ready` before capturing enabled product controls. Exercise the actual option and field focus and record the observed outcomes.

### Seed Bot summaries before headless UI dispatch begins

**Problem:** The first headless frame raced with a detached startup orchestration refresh in `TestPiAppScheduledWorkAndGrants`.

**Cause or correction:** A worker callback used the direct `updateUI` fallback before the UI hook was installed and changed the orchestration slice concurrently with rendering.

**Prevention:** Read initial orchestration and active-session summaries before installing the profile. Serialize direct UI updates with hook installation and release the mutex before dispatching through the hook or native window.

### Keep Bot inspectors behind the overview menu

**Problem:** Permanent Terminal sessions and Background work buttons crowded the chat header. Opening either inspector replaced the conversation at widths that could show both. The Settings heading style also truncated inspector titles in the right drawer.

**Cause or correction:** Both actions were top-level header controls and both inspectors rendered inside the main chat column. `typePageTitle` and `lineSettingsTitle` resolve to 22 and 28, while drawer titles use 16 px type, weight 600, and a 24 px line height.

**Prevention:** Put Bot management actions in the overview menu and show inspectors in the Bot profile drawer. Use the existing narrow overlay breakpoint, restore focus to the opener, and check the full drawer title at the default width.

### Keep inspector rows stationary during background reloads

**Problem:** Clicking a Terminal sessions row could miss when a background reload finished at pointer-down. Background work had the same layout shift.

**Cause or correction:** Inserting a loading notice above populated content moved the click targets when the notice disappeared.

**Prevention:** Show the loading notice only while the current list or detail has no content. Keep existing content in place and use the Reload control's disabled state during refresh.
