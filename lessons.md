# Nagi development lessons

## Validate composed pages rather than isolated controls

**Problem:** Settings navigation shrank to label width, the scrollbar followed a narrow content column, and an embedded editor had a different width despite passing component checks. A later UI progress report emphasized source/test maintenance instead of visual inspection. Recovery action labels still appeared adjoining after their notice container became full-width.
**Cause or correction:** Start alignment, a non-filling scroll viewport and an auto-width nested form lacked a shared page-width contract. The user requires visual inspection; source, geometry and test results do not establish visual quality. The obsolete session editor is now removed. A later native 720-DIP review confirmed that settings rows compared the whole window width from MyGo Context.Size with the breakpoint instead of the width remaining after the sidebar. The user subsequently approved all phase1 readability, hierarchy and layout corrections in the UI checklist, superseding the corresponding Qoder values; phase2 styling remains a proposal. The first corrected headless render still showed flush vertical edges: percent100 FillHeight plus margins under MyGo's default centered Row oversized the card and canceled the visible vertical margins. The remaining recovery action-spacing cause is Unconfirmed.
**Prevention:** Inspect actual rendered page combinations against the current Qoder-based contract with one full-pane scroll viewport and a separate bounded content column. Let parent flex allocation account for outer margins with explicit stretch and MinHeight0 rather than combining percent100 height with extra margins. Use the row's available width for responsive composition and one title/content skeleton across Settings categories. Inspect actual action separation; a declared gap or full-width container does not establish it. Synchronize approved corrections into the existing contract before implementation, preserving source-reference facts and historical evidence. Inspect the selected fixture source before running previews: a rich-table or copy screenshot may contain no ordinary parsed link and cannot validate body-link styling. Check minimum/default/wide layouts, scrolling and relevant states. Distinguish inspected headless images from native-window observations and preserve screenshots; do not infer visual acceptance from assertions or screenshot counts.

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

**Problem:** The lab's final native screenshot repeated one answered-choice label while saved state and AX contained one answer. Later controlled crash screenshots repeated the same uncertainty guidance in red and amber.
**Cause or correction:** The prototype cause remains Unconfirmed. The earlier composer rendered the unknown summary directly and again through recoveryNotice; that did not establish duplicate execution or receipt state. The approved correction gives one eligible uncertainty notice ownership of both distinct original diagnostics, while preserving folded disclosure, fallback and unknown delivery. Existing locale tests still expected separate disclosures and therefore rejected the new contract.
**Prevention:** Compare actual product render, AX and persisted question/receipt identities. Trace all visible notice producers before changing ledger logic. Do not copy the prototype wholesale or report a clean native result without checking the product scene. Maintain the existing regression through its test owner when notice ownership changes; require all original diagnostics and no-effect assertions under the new disclosure instead of preserving duplicate presentation.

## Keep development verification at integration boundaries

**Problem:** Earlier development included failure-first unit edits and unit-suite validation under the previous process.
**Cause or correction:** The current user rules prohibit developer TDD and developer unit-test edits; development verification is integration/E2E only. Necessary unit maintenance belongs to the test owner.
**Prevention:** Fence test ownership explicitly, reuse observable integration/E2E cases and send unit compatibility work to the test owner. Do not weaken required gates to hide an incomplete migration.

## Keep explanations outside implementation bodies

**Problem:** Earlier code conventions allowed inline and implementation-body explanations. A patch report also mistook unchanged upstream comment context for newly added comments.
**Cause or correction:** The current user rules prohibit new end-of-line and implementation-body comments. Necessary contract documentation must explain what callers cannot infer from names/types/signatures.
**Prevention:** Use clear names and small cohesive functions; inspect added source for prohibited comments and place only necessary independent explanations outside implementation bodies. In patch files distinguish added lines from unchanged upstream context; patch syntax adds a leading space before Go tabs, so verify source whitespace after application rather than rejecting required context prefixes.

## Export child-process outcomes separately

**Problem:** The lab exported native app/fixture exit zero but not the native worker's individual exit status. The first product-control wrapper also omitted its raw child outcome when interrupted. A later lead preview read recorder results before the validation owner announced terminal release.
**Cause or correction:** App exit, a mapped helper status and disappearance of a child do not prove that child's exit code. Unexported child outcomes remain unknown. Recorder output files can appear before the actual HC terminal receipt and lease release; their existence is not process-completion evidence.
**Prevention:** Correlate each owned child with its actual wait result and export raw return code and signal before mapping a helper exit, including interrupted runs. Keep app, worker, provider and cleanup outcomes separate. Release test gates, complete owned process/server cleanup, then snapshot final provider request counts; an earlier snapshot can miss requests during failure cleanup. The lead must also wait for the actual terminal and owner release before reading results for manual analysis.

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
**Cause or correction:** JSON true/false are not Python literals; serializing data did not make it executable Python. HC also rejects program names over32characters before launch. A later CI collector received empty logs because gh rejected terminal escape sequences; retrieving the same immutable jobs with its explicit raw-output option succeeded without a CI retry.
**Prevention:** Read metadata with json.loads or a data file and validate HC name constraints before launch. Collect escape-bearing CI logs directly into evidence files with the CLI's explicit raw-output option; keep the original collection error separate from CI outcomes. Produce the bounded inventory before mutations and preserve actual failures. Follow the current explicit resource scope: the user now requests direct deletion of confirmed obsolete worktrees and branches without new archives; preserve reserved data, live resources and original evidence.

## Verify owner identity from actual metadata

**Problem:** A coordination message copied a displayed redaction token as its sender identity. A later color assignment also included a display placeholder instead of the approved value and was corrected before treating it as source data. A separate check guessed a role value and stopped before sending because the actual metadata differed. One root-launched recorder also declared a task-owner parent although its actual HC parent was null.
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

**Problem:** A package-only build stopped before compilation because GOTOOLCHAIN=local selected Go1.27.1; a second bootstrap attempt failed with GOSUMDB disabled. A later formatting phase probed the system1.27.1 formatter instead of using the already established cached1.27.3 executable and stopped before formatting. Subsequent QA maintenance used /usr/local/go/bin/gofmt under a cached label, without establishing that it was the assigned formatter.
**Cause or correction:** The module requires Go1.27.3, which was already cached. Environment restrictions changed toolchain selection and verification before source compilation. The system and assigned formatter binaries differed; root's explicit cached formatter check produced no source diff, without establishing a version for the system binary.
**Prevention:** Resolve the required cached compiler and formatter paths before an offline phase, reuse their exact argv in subsequent owner fences, put the compiler bin directory on PATH and keep normal checksum handling. Record the actual executable path rather than treating a system installation or a cached label as pinned-tool evidence. Preserve setup failures separately from format, compiler or behavior failures.

## Separate isolated declaration setup from SDK defects

**Problem:** The memory-credential export preparation first typechecked isolated copies without dependency resolution and produced124 missing-module diagnostics.
**Cause or correction:** Packet-local resolution fixed setup. Strict baseline/proposed checks then had the same44 diagnostics; the public caller passed with upstream library checking skipped. No full declaration or runtime PASS was established.
**Prevention:** Resolve isolated dependencies read-only, compare baseline and proposed diagnostics, and report setup, caller and runtime results separately. Do not change SDK code or versions to conceal an unchanged baseline failure.

## Keep the IPC type map in one declaration set

**Problem:** The first compiled Go client contained duplicate MaxFrameBytes and history/inspection type declarations.
**Cause or correction:** The duplicate declarations were in the assembled types.go source, not a Runtime or product behavior failure.
**Prevention:** Keep one canonical typed wire map, check symbol uniqueness after assembling edits, and compile the actual package before assigning its consumers. Preserve compiler failures without presenting them as behavioral RED evidence.

## Preserve active work after a rejected shutdown

**Problem:** Source review found that the first Pi client closed stdin after every shutdown reply, including a proved busy rejection; this would abort admitted work. No dynamic failure was claimed. A later actual activity run reaped its worker successfully but retained a tool.result not_found bridge-drain error during failure cleanup.
**Cause or correction:** Request rejection is not permission to terminate the worker. A busy reply must leave the client usable for the existing run's answer/cancel/drain path. A later product Quit path requested activity cancellation but only waited for active chat, so a stalled chat could prevent drain indefinitely; this was also found in source review before execution. The actual tool-result failure ordering is Unconfirmed; source inspection found that asynchronous authorization could return after stop and still originate a domain-error reply.
**Prevention:** Keep busy shutdown rejection separate from transport-unknown teardown. Product Quit must request cancellation through each captured active owner, including a chat admitted during shutdown, then observe settlement, callback completion and reaping. Keep cancellation ownership idempotent without suppressing ordinary retry after a failed Stop. Recheck the captured bridge stop signal after asynchronous authorization before originating an error reply, while retaining every error from an attempted RPC. A pre-send check does not close the in-flight race or justify treating arbitrary not_found as successful delivery. Exercise a held tool call, rejected shutdown, targeted answer and later successful shutdown in actual process integration.

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

**Problem:** Handoffs announced placeholder digests, and readbacks assumed a compiler version or complete SDK inventory instead of reading the actual records. A later payload report stored displayed redaction placeholders in digest fields. An inverse-source verification also reported a mismatch after its reconstruction inserted an extra newline.
**Cause or correction:** These were metadata or verification errors. The prepared SDK tree covered113 matching files; the fresh package also contained3 unchanged documentation/notice files. Correcting the inverse reconstruction restored the expected hash without changing source.
**Prevention:** Freeze actual bytes, derive versions/counts/digests programmatically from records and compare explicit file sets, reporting additional files. Finish all ownership and budget checks before exporting and hashing their evidence object; a preparation review caught a later check omitted from an earlier export. Preserve exact block separators in source reconstruction and diagnose the verifier before editing already-correct code. Keep machine-readable digest fields exact; suppress display when necessary instead of replacing stored data with transcript placeholders. Inspect captured image magic bytes and dimensions before selecting an extension or MIME type; the native capture API returned JPEG despite an initial PNG assumption. Label incomplete work without a digest and preserve metadata corrections without rerunning unchanged production work.

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
**Prevention:** Reject noncurrent unanswered targets before saving; preserve drafts on rejection and deduplicate identical saved answers. A held question has an empty delivery state until an answer is durably saved, including when an unanswered call is canceled or its worker exits. Show saved, dispatching, resolved, unknown or canceled only for a recorded answer, and never infer delivery from saved text. Verify the actual GUI save and model-tool path against one durable association without claiming that form coverage proves the separate activity-call lifecycle.

## Discover source paths before targeted inspection

**Problem:** Initial symbol searches named absent guessed question files, and later inspections assumed absent source, SDK, official example and external recorder paths. An edit anchor was also rejected before mutation because the relevant range had not been read. An activity review later cited the wrong commit, a nonexistent draft and store/event names absent from the reviewed source.
**Cause or correction:** Repository symbol/path discovery located the actual files; none of these failed lookups established a product defect. The activity report contained unsupported source claims and was held for correction rather than imported as a contract.
**Prevention:** Use rg --files and symbol discovery before targeted reads or edits, including pinned SDK examples, payloads and external evidence directories. Resolve the exact declaration and read the actual callee before replacing a symbol: a keyword match found a profile notice, but the tested archive guard returned a different enum. Include methods when mapping call sites to their enclosing declarations, and inspect completed search output before publishing an exact caller count or writer fence. Verify the full supplied commit and each cited existing symbol/event against the actual producer; label proposed names as proposals. Observe the discovered path before issuing the dependent read, then replace a complete balanced block when changing its delimiters. Do not assume an example directory from a component name. Pass filename patterns through quoted rg --glob filters rather than unmatched shell globs. Keep lookup/setup failures separate from compilation and behavior results.

## Select process tests explicitly in CI

**Problem:** The GUI lane runs default root tests without pinned worker inputs, while new process E2E roots require those inputs and must fail when they are missing.
**Cause or correction:** Test selection and runtime-input requirements are different contracts. The new product process file uses the integration build tag, selected explicitly by the Pi lane; it does not skip on missing inputs.
**Prevention:** Review every caller when adding process tests. Keep the ordinary GUI lane independent, execute supplemental form coverage explicitly, and leave all required product roots and missing/failure rules unchanged.

## Reacquire form targets after structural updates

**Problem:** GUI tests missed Save/Edit/Add follow-up/Back controls, copied empty text despite retained drafts, and failed compound focus/presentation guards without identifying the failed condition. A recovery-control assertion also used a substring match for Reload while the drawer contained Reload task.
**Cause or correction:** First activation and unreported guard conditions remain Unconfirmed. Pinned MyGo derives unkeyed controller identities from sibling position; an unsaved notice exposed an unkeyed Contact form. Save retains an open connection editor, repeated captions can precede real controllers, and a fixed wheel coordinate can scroll another pane. Current chat/composer ancestors are already keyed; a separate deferred Settings return-focus request can outlive manual composer focus. Tester.Find returns the first exact text/label record, including clipped captions, while HasText uses strings.Contains.
**Prevention:** Reacquire the intended action target after navigation, verify expansion/current editor state and actual text-input versus button focus, and retain activation/copy receipts. Observe continuous typing without refocusing to hide a transition failure. Inspect existing keys and focus requests before adding identity machinery; consume an obsolete return-focus request once the actual composer already owns focus. Use exact labels and positive viewport geometry for control assertions; reserve substring checks for diagnostic content. Keep caption bounds separate from uniquely identifiable editor bounds. Prefer the actual pane or bounded keyboard traversal, and inspect every affected helper call rather than fixing one button per run. Export each compound condition and a post-transition frame before Fatal across all related new guards. Preserve drafts, rejection/no-effect assertions and original failures; missing activation alone does not establish a toolkit defect.

## Reassemble streamed test output before parsing receipts

**Problem:** Receipt analysis tried decoding individual Go JSONL Output chunks and failed on an unterminated JSON string; partial-chunk filtering also emitted unrelated continuation data. A later pre-launch recorder copied obsolete receipt names and a provider limit field absent from the current producer.
**Cause or correction:** One test log message can span multiple Output events. Reassembling saved chunks by test recovered the original receipt lines without another execution. The activity producer emits activity-loopback and activity-budget with different fields from the earlier fixture; the recorder mismatch was corrected before execution.
**Prevention:** Concatenate each test's output before splitting complete log lines and decoding bounded receipt records. Check current producer names and fields before reusing a recorder. Retain observed request bodies and counts even when an early failure prevents the final budget receipt; leave unobserved worker totals unknown. Preserve raw JSONL and distinguish analysis/setup errors from test or product failures.

## Match evidence to the observable product boundary

**Problem:** An activity E2E brief demanded separate raw tool.finished and ToolResult response exports from the actual app-owned client, whose host exposes no independent event subscriber.
**Cause or correction:** The product gate requires an owner-correlated answer and settlement. Exact public SDK tool-result history, the actual model request carrying that result, captured delivery state and owned settlement can establish that outcome. They do not independently export either raw protocol packet.
**Prevention:** Specify the business assertion and its actual evidence sources before requiring instrumentation. Keep unobserved packets explicit, never import another fixture client's receipts, and avoid a production test-hook framework solely for redundant logging. Verify continued typing, selection and copying through public UI APIs rather than reflecting into private controller fields solely to compare identities.

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

**Problem:** A coordination brief used unsupported_stage ambiguously, and six user-visible notices included that literal machine-style prefix. A copy-only edit briefly added stray suffix characters before static parsing caught them. Later activity originals still exposed engineering-stage and engine-history wording in ordinary notices. Changing a raw exit detail did not change the visible localized recovery summary.
**Cause or correction:** Capability holds require plain explanatory copy; internal codes belong in machine contracts and technical details in explicit diagnostics. Removing a prefix alone did not address the related ordinary profile, task and history messages. For sendUnknown, composer selects unknownSummary independently of the stored diagnostic text.
**Prevention:** Trace each producer through its formatter to the actual rendered surface and apply the approved wording rule across related ordinary notices, including projection warnings; a helper named addDetail is not necessarily folded. Preserve diagnostic contracts, capability guards, saved/delivered distinctions and callbacks. Verify exact inverse substitutions and qualify English-only work separately from localization and visual acceptance.

## Retire obsolete panels instead of porting their tests

**Problem:** The first-use compatibility task adapted the obsolete session-model panel and its tests into capability-hold behavior instead of removing them.
**Cause or correction:** The user explicitly requested direct removal. The old editor is not a feature to maintain during Pi integration; normal connection model configuration remains required. Source review of removal also found the retained read-only model summary had lost its existing full-value tooltip.
**Prevention:** Remove the retired panel, its entry points/state and dedicated tests together. Inspect surviving read-only information separately; retain its full-value tooltip and shared configuration helpers. Avoid placeholder panels or replacement tests for a feature the user no longer wants.

## Bind window observations to the launched process

**Problem:** A post-Quit window observation returned a new window after the owned visual app had exited0; an exact-path check found a distinct no-argument process with parent1.
**Cause or correction:** Unconfirmed. The original app exit does not identify the new instance or prove its profile binding. No further UI input was sent; the additional task-owned executable was identity-checked and stopped, with its exit code left unknown.
**Prevention:** Record the launch PID and verify identity changes around close/observation. Keep isolated profile selection in the bundle entrypoint so an independent launch cannot fall back to user data; do not resume native cases until that boundary is established. Preserve actual process outcomes and distinguish bounded visual observations from lifecycle acceptance.

## Resolve compatibility callers before claiming compilation

**Problem:** Test retirement passed formatting and a scoped import heuristic but the frozen compile failed on a retained launcher call, two missing imports and additional deleted-method callers outside the original fence. A later lead-only replacement of a valid purpose string with pi.Conversation failed compilation because its file did not import pi.
**Cause or correction:** Parsing and selected symbol searches do not resolve the complete test package. The first compiler page stopped after too many errors; its first closure left deleted failure-helper calls and changed history signatures unresolved. An uncapped root-package diagnostic established the complete remaining compiler list. A later shared activity type rename also collided with an existing package-level configuration factory; source review caught it before compilation, and the factory plus both callers received a distinct name. The later qualified purpose reference was unnecessary; restoring the existing valid string comparison removed the missing-import error without changing behavior.
**Prevention:** Preserve failed commands and source snapshots. When diagnostics are truncated, collect the complete root-package errors before assigning another closure; cached Go supports root-only -gcflags=-e. Check imports for every new package-qualified reference and avoid unnecessary cosmetic changes after an owner freeze. Check each removed declaration's callers, maintain accurate test names, and compile a committed unchanged candidate before executing tests. Give the test owner exact paths; do not restore an emulator or discard business assertions for green compilation. Compilation does not establish behavior or resolve an explicit domain/data-policy blocker.

## Separate domain persistence from legacy-data admission

**Problem:** Caller maintenance initially assumed that mapping a domain helper made rejection/non-dispatch persistence exercisable without checking legacy-data admission. Later fixtures added opaque work references and executions, ignored a scope-save error, then dereferenced the nil scope in default CI.
**Cause or correction:** Opaque legacy executions remain read-only. The new activity source admits explicitly marked fresh Pi intent, binding and run facts; it does not reinterpret old or ambiguous records. A legacy-shaped fixture can therefore fail before its intended authorization assertion. Ignoring that persistence error causes a test panic rather than evidence of a native-app failure.
**Prevention:** Inspect data admission and persistence results alongside domain callers before claiming a mapping is complete. Check creation/save errors and preserve persistence/reopen/rerun assertions. Use explicitly current synthetic records only for their stated regression boundary; never count them as actual SDK admission, retag legacy data, bypass the guard or change the pending history policy for green tests. Keep fresh first-use folder selection separate from held legacy-byte preservation; launch-directory or credential environment variables are not a persisted legacy configuration or a confirmed Pi folder.

## Reuse current ownership and canonical admission reasons

**Problem:** Pi conversation callers retained Saki-client checks in composer Reload while the Pi Send path replaced canonical rejection reasons with one generic message. A later recovery banner described an observed running-worker loss as a startup failure.
**Cause or correction:** Readiness, owned history reload and send admission have different conditions. Existing runtimeAvailableForView, canSend and sendBlockRaw already own related decisions; the new path did not consistently use them. Exit copy incorrectly implied that history reload would permit sending, while the Pi path preserves sendUnknown. The admission issues were source findings; C3 originals later directly showed the recovery-copy mismatch caused by a configuration-stage predicate overriding the exited state.
**Prevention:** Keep one live-host readiness decision and revalidate the captured Pi conversation at history dispatch/completion. Classify an actual exit separately from setup history while retaining specific startup and local-store failures. Keep both genuine failed-start and exited-with-stale-configuration-stage cases in recovery regressions; do not label an exited fixture as a startup failure. Reuse canonical admission reasons for owned contacts. Recheck durable cancellation before an activity crosses its first run-attempt boundary; a canceled preparation must not start work merely to cancel it after admission. Keep actually attempted or uncertain commands distinct from known non-dispatch. Preserve drafts and unknown delivery; never infer Pi ownership from an opaque legacy session reference or let history refresh replay Send. Describe history readback without promising that it resolves an unknown send or selecting a new recovery policy.

## Restore materials when admitted-run persistence fails

**Problem:** Pi RunStarted cleared the draft and submitted context before saving; a failed save changed the send state to unknown without restoring those materials.
**Cause or correction:** Source inspection found missing rollback. clearSubmitted also compacts its backing slice and changes preview selection, so a shallow context copy cannot preserve the original contents.
**Prevention:** Capture independent context items and the current draft/preview before clearing. Restore user materials on save failure while retaining known admitted run facts, pending intent and unknown/no-replay semantics. Preserve later edits and verify the existing business assertions through the actual Pi reducer and process E2E.

## Compare persisted JSON by its contract

**Problem:** A new subset-persistence assertion rejected a valid saved array because its JSON was indented rather than compact. A later source review incorrectly called a batch snapshot's empty/null distinction irrelevant after misreading its JSON tag as omitempty.
**Cause or correction:** Whitespace is not part of the selection contract; decoded presence, nullability and values are. The snapshot has no omitempty tag, so cloning must retain its nil versus nonnil-empty distinction. Original legacy-file preservation remains a byte-level contract.
**Prevention:** Inspect the actual serialization tag and decode current JSON to assert the required values and omitted/null/empty distinctions. Use the existing clone path with nil-preserving copies rather than wrappers or restoration scans. Reserve exact byte comparisons for contracts that preserve the original bytes.

## Keep supported tool catalogs separate from defaults

**Problem:** A keyboard regression supplied supported tools but expected a host-default label without supplying the independent default list; a Retry fixture used a retired client stub instead of current configuration eligibility.
**Cause or correction:** Supported tools and default selections are distinct ready-envelope facts. Configured idle Retry presentation does not require a live host, while a legacy client stub establishes neither configuration nor Pi readiness.
**Prevention:** Supply explicit independent fixture defaults, preserve nil/empty/subset semantics and exercise actual keyboard controls. Use a fresh current store and saved valid configuration for offline Retry presentation; keep unconfigured blocking and actual process-level Retry evidence separate.

## Preserve human drafts before terminal delivery

**Problem:** Sending screenshot materials through hc cleared an old unsent human draft. A preceding --if idle attempt rejected the observed done status without typing; a later default send delivered the materials but replaced the draft.
**Cause or correction:** HC's draft hold expires after a finite interval; it does not preserve an old draft indefinitely. The original text was restored through raw bracketed paste without Enter, and the unchanged message turn, exact unsent input and original terminal size were verified. Named-key input rejected Unicode without typing.
**Prevention:** Inspect the actual input before delivery. When a human draft exists, share material files and links without sending another terminal prompt; elapsed quiet time is not consent to replace it. Preserve the exact original and restore only that text without submission after an accidental replacement. Match original terminal dimensions before attaching.

## Inspect the control actually shown in a screenshot

**Problem:** A passing activity E2E emitted named answer/save/check scenes whose relevant controls were outside the captured viewport. The Check click returned an outside-window error, so unchanged state did not prove the disabled action or rejection path.
**Cause or correction:** Stage names and functional state receipts do not establish what the pixels show or whether a control received input. The original images remain valid evidence of their actual scroll positions.
**Prevention:** Inspect each original and qualify hidden controls explicitly. Capture the relevant visible control before claiming its visual or interaction coverage, and distinguish failure to hit a control from a successful no-effect activation. Do not promote a root PASS, filename or image count into whole-app visual acceptance.

## Allow explicit host recovery without replaying unknown work

**Problem:** Source review found that worker exit marks admitted activity records unknown, while the Apply predicate rejects every unresolved execution and therefore blocks the documented dead-host replacement path.
**Cause or correction:** Replacing a fully observed exited host is a connection operation, not permission to resend unknown work. Live admission and preserved uncertainty need distinct eligibility checks. Controlled C3 on4ed3cb1 passed with three requests and three workers, preserving unknown through explicit Apply, history readback and restart. Its deliberate owned Kill does not establish spontaneous crash or ToolResult reply-loss recovery.
**Prevention:** Permit explicit recovery only for the current owned host after actual reaping, callback completion and applied exit state, while retaining live/pending work, store and legacy guards. Keep unknown records and captured sources unchanged, and verify Apply/reopen produces no model request or automatic replay. Record deliberate abnormal test exits separately from normal drain.
