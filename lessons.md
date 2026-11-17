# Nagi development lessons

## Validate composed pages rather than isolated controls

**Problem:** Settings navigation shrank to label width, the scrollbar followed a narrow content column, and an embedded editor had a different width despite passing component checks. A later UI progress report emphasized source/test maintenance instead of visual inspection.
**Cause or correction:** Start alignment, a non-filling scroll viewport and an auto-width nested form lacked a shared page-width contract. The user requires visual inspection; source, geometry and test results do not establish visual quality. The obsolete session editor is now removed. A later native 720-DIP review confirmed that settings rows compared the whole window width from MyGo Context.Size with the breakpoint instead of the width remaining after the sidebar.
**Prevention:** Inspect actual rendered page combinations against Qoder with one full-pane scroll viewport and a separate bounded content column. Check minimum/default/wide layouts, scrolling and relevant states. Distinguish inspected headless images from native-window observations and preserve screenshots; do not infer visual acceptance from assertions or screenshot counts.

## Use the current candidate without disturbing dirty ownership

**Problem:** The main checkout is an old baseline with preexisting dirty documents and is not the current delivered UI candidate.
**Cause or correction:** A repository path does not establish current source or writer ownership. PR30 contains the later UI work; its source and gate status must be treated explicitly.
**Prevention:** Read remote branch/PR identity, local status and active writers first. Use a clean task worktree from the selected source and preserve the root's dirty work and other owners' worktrees.

## Keep hypotheses separate from product decisions

**Problem:** A primary-Bot/child-Agent conjecture was incorrectly acknowledged as a user-selected product architecture.
**Cause or correction:** The user withdrew that interpretation. The accepted experience does not select permanent hierarchy, Worker/Branch names, fork policy or migration architecture.
**Prevention:** Record the exact accepted behavior and authorization scope. Preserve retractions alongside history; do not promote an experiment, proposal or coordinator interpretation into a requirement.

## Visual password masking does not establish secure accessibility

**Problem:** A synthetic key was visually masked in the Pi lab but remained literal in its accessibility snapshot.
**Cause or correction:** Product-native exposure is Unconfirmed. Product TextInputBase.Password and lab TextInput.Password share the same editor. Read-only inspection found a Darwin superclass value-cache path and a disabled-state secure-trait gap; visual bullets do not establish secure AX semantics.
**Prevention:** Inspect the actual product control and matching accessibility bridge, then validate with a synthetic key under an exact native scope. Do not enter real credentials or claim readiness until that path is established.

## Preserve legacy data without executing the retired engine

**Problem:** Existing Nagi records contain opaque Saki session/execution references that Pi cannot be assumed to understand.
**Cause or correction:** Product records and engine transcripts have different owners. No destructive conversion, journal parsing, silent history loss or uncertain-effect replay is authorized.
**Prevention:** Inventory legacy fields and live capabilities before deleting adapters. Preserve IDs/drafts/requirements/receipts and opaque references; expose unsupported history and require an explicit caller-visible mapping decision. The pending legacy-history choice holds legacy-data activation and reader deletion, not unrelated product wiring or isolated fresh-data E2E.

## Distinguish rendered duplication from duplicate business state

**Problem:** The lab's final native screenshot repeated one answered-choice label while saved state and AX contained one answer.
**Cause or correction:** Unconfirmed. This is not proof of duplicate answers or a toolkit defect, and prototype visual acceptance was limited.
**Prevention:** Compare actual product render, AX and persisted question/receipt identities. Do not copy the prototype wholesale or report a clean native result without checking the product scene.

## Keep development verification at integration boundaries

**Problem:** Earlier development included failure-first unit edits and unit-suite validation under the previous process.
**Cause or correction:** The current user rules prohibit developer TDD and developer unit-test edits; development verification is integration/E2E only. Necessary unit maintenance belongs to the test owner.
**Prevention:** Fence test ownership explicitly, reuse observable integration/E2E cases and send unit compatibility work to the test owner. Do not weaken required gates to hide an incomplete migration.

## Keep explanations outside implementation bodies

**Problem:** Earlier code conventions allowed inline and implementation-body explanations. A patch report also mistook unchanged upstream comment context for newly added comments.
**Cause or correction:** The current user rules prohibit new end-of-line and implementation-body comments. Necessary contract documentation must explain what callers cannot infer from names/types/signatures.
**Prevention:** Use clear names and small cohesive functions; inspect added source for prohibited comments and place only necessary independent explanations outside implementation bodies. In patch files distinguish added lines from unchanged upstream context; patch syntax adds a leading space before Go tabs, so verify source whitespace after application rather than rejecting required context prefixes.

## Export child-process outcomes separately

**Problem:** The lab exported native app/fixture exit zero but not the native worker's individual exit status. The first product-control wrapper also omitted its raw child outcome when interrupted.
**Cause or correction:** App exit, a mapped helper status and disappearance of a child do not prove that child's exit code. Unexported child outcomes remain unknown.
**Prevention:** Correlate each owned child with its actual wait result and export raw return code and signal before mapping a helper exit, including interrupted runs. Keep app, worker, provider and cleanup outcomes separate.

## Contain the entire optional observer boundary

**Problem:** Source review of the MCP hook found that optional-hook lookup and its undefined fast-path initially sat outside error containment.
**Cause or correction:** Reading an instrumentation property can throw before dispatch. The final isolated patch moves lookup, capture and scheduled dispatch into one contained boundary; this was source review, not a reproduced runtime failure.
**Prevention:** Keep optional instrumentation from changing the observed operation's error path. State scheduling and loss limits, observe actual direct-child event fields and verify behavior separately from static patch replay.

## Persist completed work beyond worktrees

**Problem:** Temporary files and worktrees can be removed during routine workspace cleanup.
**Cause or correction:** They are disposable development environments, not the authoritative project record.
**Prevention:** Commit/push coherent safe checkpoints, retain essential contracts and actual validation summaries in Git/PR records, and merge completed work after required gates. Export controlled process receipts before test TempDir cleanup, including on failure. Do not rely on a temporary evidence path alone.

## Inspect process path dependencies beyond executable names

**Problem:** Historical cleanup filtering by expected executable names missed a Python process whose working directory referenced a retained task path.
**Cause or correction:** A dependency can exist in any process's cwd, executable, arguments or open files. Nonempty lsof output still matters when lsof exits nonzero.
**Prevention:** Inspect all-process exact-path references before retirement. Preserve any observed live dependency, evaluate lsof output separately from its exit code, and never signal an uncertain owner process.

## Decode maintenance metadata as data

**Problem:** A cleanup classifier embedded JSON booleans directly in Python source and failed with NameError before any deletion.
**Cause or correction:** JSON true/false are not Python literals; serializing data did not make it executable Python. HC also rejects program names over32characters before launch.
**Prevention:** Read metadata with json.loads or a data file and validate HC name constraints before launch. Produce the bounded inventory before mutations and preserve actual failures. Follow the current explicit resource scope: the user now requests direct deletion of confirmed obsolete worktrees and branches without new archives; preserve reserved data, live resources and original evidence.

## Verify owner identity from actual metadata

**Problem:** A coordination message copied a displayed redaction token as its sender identity. A subsequent check guessed a role value and stopped before sending because the actual metadata differed.
**Cause or correction:** Display substitutions are not routable identities, and role values must come from the current owner metadata rather than a naming assumption.
**Prevention:** Read the current HC identity, project, role, task and state before dispatch. Never copy a displayed redaction token into a message or command; use the verified owner description and current address when the identity cannot be displayed literally.

## Prefer MyGo components and examples

**Problem:** UI work lacked an explicit requirement to begin with the component library and examples rather than custom rendering.
**Cause or correction:** The user requires reuse of MyGo components and matching examples; handwritten UI is justified only when necessary.
**Prevention:** Inspect the pinned component API, official examples and existing shared product path before implementation. Record the concrete unsupported behavior and keep any necessary custom code narrow.

## Verify the active model after switching

**Problem:** A cleanup brief and HC labels claimed Flash while the UI owner observed Sol in the actual OMP session and correctly stopped before editing.
**Cause or correction:** Unconfirmed why the runtime selection changed. Launch arguments and descriptive labels do not establish the current model after an interactive switch.
**Prevention:** Verify the live model footer or current session metadata immediately before model-specific work. Keep mismatch and correction receipts; do not relabel an unexecuted task as a Flash result.

## Use concise product and branch names

**Problem:** Worktree and branch names repeated the product, runtime implementation, date and task prefixes. The first proposed replacements still named the product workspace after Pi or the runtime role.
**Cause or correction:** Nagi is one product. The user selected main for stable releases, dev for development and integration for integration testing; runtime identity is not a workspace naming requirement.
**Prevention:** Use the product checkout and short conventional branch names. Name necessary temporary worktrees for their actual task and remove them when that task ends.

## Complete the worktree lifecycle

**Problem:** Completed tasks left historical worktrees and local branches behind. A proposed fixed two-worktree limit also misread the user's intent.
**Cause or correction:** Worktrees isolate conflicting features, fixes and validation; their number follows actual needs. Testing must use a committed frozen candidate. The user requested direct deletion of confirmed obsolete resources without new archives.
**Prevention:** Assign every temporary worktree an owner, purpose and completion condition. Stop writes before validation, collect the result, then remove completed worktrees and temporary branches after checking live path dependencies. Preserve product data and original acceptance evidence.

## Show requested team sessions in the product Space

**Problem:** The rebuilt OMP team initially ran only as background HC children and was not visible in the user's Space.
**Cause or correction:** Starting an HC terminal does not give it a visible Herdr tab. The user expects team members to be visible alongside the lead.
**Prevention:** Inspect the current Space, create or attach named team tabs there, and verify the actual visible layout. Put each role, model and thinking level in its tab name.

## Reserve Astra for difficult work

**Problem:** Astra was assigned routine onboarding and quality inventory work despite being the user's most expensive model.
**Cause or correction:** The user reserves Astra for the most challenging tasks or problems that other models repeatedly fail to solve.
**Prevention:** Use the shared DeepSeek Flash model for bounded mechanical operations, Sol for routine work and Terra for demanding implementation. UI remains a peer Sol implementation role; use the separate user-created Flash operator for shared quick work. Keep the visible role/model labels accurate. Escalate a concrete difficult problem to Astra with the existing failure evidence rather than staffing routine tasks with it.

## Verify exact SDK types before implementing an adapter

**Problem:** The first worker compile used a nonexistent settings setter and treated credential metadata as credential contents. A lab-only TypeScript erasure restriction also conflicted with the compiled-JavaScript delivery path.
**Cause or correction:** Pinned Pi provides SettingsManager.applyOverrides and an exported InMemoryCredentialStore; credential enumeration is metadata only. Production delivers compiled JavaScript rather than the lab's direct TypeScript entry.
**Prevention:** Read exact pinned declarations and scan existing exported helpers before coding an adapter. Compile the smallest boundary, preserve actual compiler failures, and align compiler options with the delivered runtime entry.

## Bind extension defaults to the owned SDK directory

**Problem:** The public MCP extension has separate default credential/log paths. Source review initially missed the worker's owned-directory override, then retained the narrower implicit OAuth-from-owned-file finding.
**Cause or correction:** The directory override confines paths but does not isolate authentication. Inject the SDK's empty in-memory MCP credential store separately from the model store; no unrelated credential access or runtime failure was observed.
**Prevention:** Inspect extension defaults and complete startup ordering. Keep paths owned and credentials explicit, then verify anonymous/static-header HTTP against synthetic saved tokens without claiming that memory disables OAuth protocol.

## Select the cached compiler explicitly for offline phases

**Problem:** A package-only build stopped before compilation because GOTOOLCHAIN=local selected Go1.27.1; a second bootstrap attempt failed with GOSUMDB disabled.
**Cause or correction:** The module requires Go1.27.3, which was already cached. Environment restrictions changed toolchain selection and verification before source compilation.
**Prevention:** Resolve the required cached compiler path before an offline phase, put its bin directory on PATH and keep normal checksum handling. Preserve setup failures separately from compiler or behavior failures.

## Separate isolated declaration setup from SDK defects

**Problem:** The memory-credential export preparation first typechecked isolated copies without dependency resolution and produced124 missing-module diagnostics.
**Cause or correction:** Packet-local resolution fixed setup. Strict baseline/proposed checks then had the same44 diagnostics; the public caller passed with upstream library checking skipped. No full declaration or runtime PASS was established.
**Prevention:** Resolve isolated dependencies read-only, compare baseline and proposed diagnostics, and report setup, caller and runtime results separately. Do not change SDK code or versions to conceal an unchanged baseline failure.

## Keep the IPC type map in one declaration set

**Problem:** The first compiled Go client contained duplicate MaxFrameBytes and history/inspection type declarations.
**Cause or correction:** The duplicate declarations were in the assembled types.go source, not a Runtime or product behavior failure.
**Prevention:** Keep one canonical typed wire map, check symbol uniqueness after assembling edits, and compile the actual package before assigning its consumers. Preserve compiler failures without presenting them as behavioral RED evidence.

## Preserve active work after a rejected shutdown

**Problem:** Source review found that the first Pi client closed stdin after every shutdown reply, including a proved busy rejection; this would abort admitted work. No dynamic failure was claimed.
**Cause or correction:** Request rejection is not permission to terminate the worker. A busy reply must leave the client usable for the existing run's answer/cancel/drain path.
**Prevention:** Keep busy shutdown rejection separate from transport-unknown teardown. Exercise a held tool call, rejected shutdown, targeted answer and later successful shutdown in actual process integration.

## Accept authoritative binding facts without losing ownership

**Problem:** Source review found exact-string cwd and used-flag comparisons that rejected canonical path aliases or an SDK-proven unused-to-used upgrade.
**Cause or correction:** A same-host canonical path can represent the selected directory, and saved public entries can establish use beyond the host's conservative knowledge. Owner/context/purpose correlation must still match.
**Prevention:** Compare canonical path identity, accept only monotonic use evidence, and keep missing used sessions unavailable. Test actual temporary-directory reopen instead of mirroring raw request fields.

## Preserve context notification sequences across reopen

**Problem:** Source review of the new MCP integration found a notification counter created for each open, while the client validates sequence monotonicity for the whole worker epoch and context ID.
**Cause or correction:** Closing and reopening that context would reset the producer counter while the consumer retained its last value. No dynamic failure was claimed.
**Prevention:** Keep context notification counters with the worker until its epoch ends, and exercise close/reopen with actual process events. Distinguish attempts with explicit IDs instead of resetting or weakening sequence validation.

## Bind MCP configuration beyond an open-context cache

**Problem:** Source review found that used MCP reopen compared saved server/source labels, while full configuration checks covered only currently open contexts.
**Cause or correction:** After close/restart, reusing a source label could retarget the old context to different configuration. No dynamic failure was claimed.
**Prevention:** Store a canonical configuration integrity digest with the existing public binding metadata, retain opaque source identity separately and reject drift before creating transports. Keep raw private values outside history and exercise same-label/different-configuration reopen.

## Decode public SDK history by message role

**Problem:** The first MCP process E2E failed while decoding every public message with a tool-result content-array schema, after the real MCP call succeeded.
**Cause or correction:** The SDK emitted a valid system message with string content and tool-declaration metadata. This was a fixture decoder failure, not a demonstrated worker or MCP defect.
**Prevention:** Read the entry/message discriminator first, then decode the matching role's content. Keep strict call/receipt assertions for tool results and preserve heterogeneous SDK records instead of fabricating a legacy transcript shape.

## Keep new defaults separate from bound context settings

**Problem:** The initial worker globally updated every existing context when defaults changed and lacked effective per-context configuration readback.
**Cause or correction:** Existing product callers require bound model/limits; endpoint, wire and credential source must not be silently repointed either.
**Prevention:** Capture an immutable connection source and independent SDK configuration per context. Change model/limits only through an explicit idle-target operation, report actual nonsecret readback, and observe A remaining bound after B/default changes.

## Reject unavailable history before a mutating SDK open

**Problem:** Source review found that opening an existing empty used Pi session caused the SDK to write a fresh header before Nagi rejected its missing binding.
**Cause or correction:** SessionManager.open intentionally initializes empty files. Nonempty invalid files already fail unchanged; Nagi must classify used missing/empty references before this call.
**Prevention:** Check file metadata first, report used history unavailable and preserve original bytes. Recreate only durably proven-unused bindings with a new reference, using public SDK APIs without parsing journals.

## Report digests only after freezing actual bytes

**Problem:** Handoffs announced placeholder digests, and readbacks assumed a compiler version or complete SDK inventory instead of reading the actual records. A later payload report also stored displayed redaction placeholders in digest fields.
**Cause or correction:** These were metadata mistakes after successful builds. The prepared SDK tree covered113 matching files; the fresh package also contained3 unchanged documentation/notice files.
**Prevention:** Freeze actual bytes, derive versions/counts/digests programmatically from records and compare explicit file sets, reporting additional files. Keep machine-readable digest fields exact; suppress display when necessary instead of replacing stored data with transcript placeholders. Inspect captured image magic bytes and dimensions before selecting an extension or MIME type; the native capture API returned JPEG despite an initial PNG assumption. Label incomplete work without a digest and preserve metadata corrections without rerunning unchanged production work.

## Keep worker build payloads outside the checkout

**Problem:** The initial worker build copied the lab convention and emitted an ignored dist directory inside the task worktree despite the project rule requiring external outputs.
**Cause or correction:** A disposable ignored directory is not a delivery workspace. Package provenance must bind actual external compiled/runtime/dependency inputs.
**Prevention:** Require an explicit fresh external worker output, record its source/runtime/lock/notice hashes and use that payload for integration and packaging. Archive verified prior outputs before retiring the old generated directory; never substitute its historical test paths into later results.

## Bind the compiler that actually produces the payload

**Problem:** Source review found that the external builder still used a source-side installed TypeScript compiler and declarations without recording their actual identity.
**Cause or correction:** A tracked dependency lock describes intended inputs; it does not identify an existing mutable node_modules tree. No incorrect payload was observed.
**Prevention:** Install the locked dependency set in the fresh external build directory, compile against that tree, record the actual compiler/version/files and install receipt, then prune development dependencies. Do not claim the corrected builder is validated by a prior build.

## Parse orchestration code before launching a phase

**Problem:** A native build wrapper had a missing closing parenthesis and failed before invoking Go. A shell orchestration expression also failed before executing its intended commands.
**Cause or correction:** These were wrapper syntax failures, not product compilation or behavior failures.
**Prevention:** Parse generated Python and check shell/JavaScript quoting before starting a long HC phase. Preserve the failed wrapper and raw result, then make a bounded correction without relabeling the first attempt.

## Check formatting for tagged integration sources before pushing

**Problem:** The first Pi Draft's GUI lane stopped at formatting in the native-control integration file, before vet, tests or build ran.
**Cause or correction:** A successful tagged compilation and whitespace diff check did not establish canonical Go formatting. The correction changed only whitespace and the harness's bound source hash.
**Prevention:** Run the pinned gofmt listing over the actual committed tree, including tagged integration files and files whose last declaration was removed, before pushing. A last-function deletion can leave an extra EOF blank line even when retained declaration bytes are exact. Preserve original failures and do not count unexecuted later steps as passed.

## Keep window-binding failures separate from control observations

**Problem:** The desktop inventory succeeded but binding the unique synthetic-control app returned cgWindowNotFound. The fixture was stopped without input, Test or Save.
**Cause or correction:** Unconfirmed. No AX value or secure-entry assertion was reached; the failed controlled attempt does not establish credential exposure or a passing native control.
**Prevention:** Retain the exact app/PID, tool error and controlled stop outcome. Await a real environment change before a new isolated case; do not retry the unchanged binding, operate a reserved app or treat missing observations as success.

## Answer the captured current question

**Problem:** The GUI Answer action opened a generic supplement form, and answer extraction needed an explicit guard against a superseded question at the same requirements revision.
**Cause or correction:** A supplement is not a question answer. Capture the displayed owner, issue, question reference and revision, then share the existing atomic question/supplement mutation and receipt.
**Prevention:** Reject noncurrent unanswered targets before saving; preserve drafts on rejection and deduplicate identical saved answers. Verify the actual GUI save and model-tool path against one durable association without claiming that form coverage proves the separate activity-call lifecycle.

## Discover source paths before targeted inspection

**Problem:** Initial symbol searches named absent guessed question files, and later inspections assumed absent source, SDK and external recorder paths. An edit anchor was also rejected before mutation because the relevant range had not been read.
**Cause or correction:** Repository symbol/path discovery located the actual files; none of these failed lookups established a product defect.
**Prevention:** Use rg --files and symbol discovery before targeted reads or edits, including SDK payloads and external evidence directories, then read the actual edit range. Pass filename patterns through quoted rg --glob filters rather than unmatched shell globs. Keep lookup/setup failures separate from compilation and behavior results.

## Select process tests explicitly in CI

**Problem:** The GUI lane runs default root tests without pinned worker inputs, while new process E2E roots require those inputs and must fail when they are missing.
**Cause or correction:** Test selection and runtime-input requirements are different contracts. The new product process file uses the integration build tag, selected explicitly by the Pi lane; it does not skip on missing inputs.
**Prevention:** Review every caller when adding process tests. Keep the ordinary GUI lane independent, execute supplemental form coverage explicitly, and leave all required product roots and missing/failure rules unchanged.

## Reacquire form targets after structural updates

**Problem:** The captured-answer E2E reached a completed issue but observed no rejection notice while its draft stayed intact; it had not recorded whether Save was activated.
**Cause or correction:** First-run activation is Unconfirmed. The fixture assumed textarea focus survived a Frame that changed preceding controls; pinned MyGo derives unkeyed element IDs from sibling position. Source review still found the completed-target form and rejecting save path.
**Prevention:** Reacquire the actual form after structural updates, assert text-input focus and then the non-input Save target before activation, and retain focus/result receipts. Keep rejection, draft and no-effect assertions intact; do not infer a toolkit defect or add a production patch from missing activation evidence.

## Reassemble streamed test output before parsing receipts

**Problem:** Receipt analysis tried decoding individual Go JSONL Output chunks and failed on an unterminated JSON string; partial-chunk filtering also emitted unrelated continuation data.
**Cause or correction:** One test log message can span multiple Output events. Reassembling saved chunks by test recovered the original receipt lines without another execution.
**Prevention:** Concatenate each test's output before splitting complete log lines and decoding bounded receipt records. Preserve raw JSONL and distinguish analysis/setup errors from test or product failures.

## Preserve original input beside model context

**Problem:** The worker appends named app-context suffixes to its SDK prompt. A normal conversation-history caller would otherwise display those suffixes or try stripping user text heuristically.
**Cause or correction:** Model input and original user input are distinct. The worker can retain the original run text/context/correlation with the existing public SDK custom-entry API before prompting; no second transcript store is needed.
**Prevention:** Project original input from proven nagi-turn-v1 metadata and retain actual public entry IDs. Do not parse engine journals, strip arbitrary user-authored tags, or label recorded input as successful execution. Validate this changed worker in a fresh external payload with the real first-use flow.

## Bound isolated probe cleanup by actual reaping

**Problem:** Source inspection found that a timed-out connection Test could receive busy from Shutdown while the client exposed only Shutdown/Wait, leaving its isolated worker and state unretired.
**Cause or correction:** These are source gaps, not observed process failures. An owned-process Kill/wait fallback does not itself retain a failed probe owner or prove callback completion. Clearing the UI Test flag after an error-only return could release quit with cleanup unproved.
**Prevention:** Retain the specific probe client and state until actual reaping and callback completion, including after a bounded cleanup failure. Keep the existing quit barrier until both are observed. Exercise held synthetic cleanup, preserve raw outcomes and keep ordinary business cancellation and busy shutdown unchanged; a killed test is not successful or proof of descendant termination.

## Preserve public history timestamps in product projections

**Problem:** Source review found the first Pi history projection preserved entry IDs and text but left message times and history dates blank.
**Cause or correction:** The public SDK entry timestamp was omitted from decoding and projection even though the existing product renderer and search use those fields. No dynamic failure was claimed.
**Prevention:** Map actual public timestamps through the existing product formatter alongside original text, context and IDs. Include reopen, search and full-message display in first-use E2E; do not substitute the current time for missing history facts.

## Match actual readiness values and captured cancellation targets

**Problem:** Source review found Node readiness compared against an unprefixed version and a bridge-error cancellation using the rejected incoming event's context/run IDs.
**Cause or correction:** The worker reports process.version with its v prefix. Cancellation after a rejected event must use the admitted bridge's captured owner/context/run, not the rejected packet. Both corrections preceded execution.
**Prevention:** Read the actual producer and admitted caller identity before implementing checks or failure handling. Validate canonical readiness and exact cancellation ownership in the frozen product E2E.

## Keep machine codes out of capability notices

**Problem:** A coordination brief used unsupported_stage ambiguously, and six user-visible notices included that literal machine-style prefix. A copy-only edit briefly added stray suffix characters before static parsing caught them.
**Cause or correction:** Capability holds require plain explanatory copy; internal error codes belong only to machine contracts. The final six prefix deletions were verified by exact inverse hashes.
**Prevention:** Specify displayed text separately from protocol codes, parse edits and verify their exact delta before freezing. Preserve the actual capability guard and reuse existing product controls.

## Retire obsolete panels instead of porting their tests

**Problem:** The first-use compatibility task adapted the obsolete session-model panel and its tests into capability-hold behavior instead of removing them.
**Cause or correction:** The user explicitly requested direct removal. The old editor is not a feature to maintain during Pi integration; normal connection model configuration remains required. Source review of removal also found the retained read-only model summary had lost its existing full-value tooltip.
**Prevention:** Remove the retired panel, its entry points/state and dedicated tests together. Inspect surviving read-only information separately; retain its full-value tooltip and shared configuration helpers. Avoid placeholder panels or replacement tests for a feature the user no longer wants.

## Bind window observations to the launched process

**Problem:** A post-Quit window observation returned a new window after the owned visual app had exited0; an exact-path check found a distinct no-argument process with parent1.
**Cause or correction:** Unconfirmed. The original app exit does not identify the new instance or prove its profile binding. No further UI input was sent; the additional task-owned executable was identity-checked and stopped, with its exit code left unknown.
**Prevention:** Record the launch PID and verify identity changes around close/observation. Keep isolated profile selection in the bundle entrypoint so an independent launch cannot fall back to user data; do not resume native cases until that boundary is established. Preserve actual process outcomes and distinguish bounded visual observations from lifecycle acceptance.

## Resolve compatibility callers before claiming compilation

**Problem:** Test retirement passed formatting and a scoped import heuristic but the frozen compile failed on a retained launcher call, two missing imports and additional deleted-method callers outside the original fence.
**Cause or correction:** Parsing and selected symbol searches do not resolve the complete test package. The first compiler page stopped after too many errors, so its list was incomplete.
**Prevention:** Preserve the first failed command and source snapshot. Check all callers of each removed declaration, maintain accurate test names, and compile one committed unchanged candidate before executing tests. Give the test owner exact follow-up paths; do not restore an emulator or discard live business assertions to obtain a green compile.

## Reuse current ownership and canonical admission reasons

**Problem:** Pi conversation callers retained Saki-client checks in composer Reload while the Pi Send path replaced canonical rejection reasons with one generic message.
**Cause or correction:** Readiness, owned history reload and send admission have different conditions. Existing runtimeAvailableForView, canSend and sendBlockRaw already own related decisions; the new path did not consistently use them. These are source findings, not executed failures.
**Prevention:** Keep one live-host readiness decision and revalidate the captured Pi conversation at history dispatch/completion. Reuse canonical admission reasons for owned contacts. Preserve drafts and unknown delivery; never infer Pi ownership from an opaque legacy session reference or let history refresh replay Send.

## Restore materials when admitted-run persistence fails

**Problem:** Pi RunStarted cleared the draft and submitted context before saving; a failed save changed the send state to unknown without restoring those materials.
**Cause or correction:** Source inspection found missing rollback. clearSubmitted also compacts its backing slice and changes preview selection, so a shallow context copy cannot preserve the original contents.
**Prevention:** Capture independent context items and the current draft/preview before clearing. Restore user materials on save failure while retaining known admitted run facts, pending intent and unknown/no-replay semantics. Preserve later edits and verify the existing business assertions through the actual Pi reducer and process E2E.
