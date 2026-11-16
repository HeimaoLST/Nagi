# Nagi development lessons

## Validate composed pages rather than isolated controls

**Problem:** Settings navigation shrank to label width, the scrollbar followed a narrow content column, and an embedded session editor had a different width despite passing component checks.
**Cause or correction:** Start alignment, a non-filling navigation group/scroll viewport and an auto-width nested form were composed without a shared page-width contract.
**Prevention:** Check actual loaded page combinations with one full-pane scroll viewport and a separate bounded content column. Inspect minimum/default/wide layouts and the current-session editor, not only individual control targets.

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
**Prevention:** Inventory legacy fields and live capabilities before deleting adapters. Preserve IDs/drafts/requirements/receipts and opaque references; expose unsupported history and require an explicit caller-visible mapping decision.

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

**Problem:** The lab exported native app/fixture exit zero but not the native worker's individual exit status.
**Cause or correction:** App exit and disappearance of a child do not prove that child's exit code. The unexported native worker outcome remained unknown.
**Prevention:** Correlate each owned child with its wait/reap result and export the actual exit code. Keep app, worker, provider and cleanup outcomes separate.

## Persist completed work beyond worktrees

**Problem:** Temporary files and worktrees can be removed during routine workspace cleanup.
**Cause or correction:** They are disposable development environments, not the authoritative project record.
**Prevention:** Commit/push coherent safe checkpoints, retain essential contracts and actual validation summaries in Git/PR records, and merge completed work after required gates. Do not rely on a temporary evidence path alone.

## Inspect process path dependencies beyond executable names

**Problem:** Historical cleanup filtering by expected executable names missed a Python process whose working directory referenced a retained task path.
**Cause or correction:** A dependency can exist in any process's cwd, executable, arguments or open files. Nonempty lsof output still matters when lsof exits nonzero.
**Prevention:** Inspect all-process exact-path references before retirement. Preserve any observed live dependency, evaluate lsof output separately from its exit code, and never signal an uncertain owner process.

## Decode maintenance metadata as data

**Problem:** A cleanup classifier embedded JSON booleans directly in Python source and failed with NameError before any deletion.
**Cause or correction:** JSON true/false are not Python literals; serializing data did not make it executable Python.
**Prevention:** Read metadata with json.loads or a data file. Produce the bounded inventory before mutations, preserve actual failures, and require verified archives before destructive retirement.

## Verify exact SDK types before implementing an adapter

**Problem:** The first worker compile used a nonexistent settings setter and treated credential metadata as credential contents. A lab-only TypeScript erasure restriction also conflicted with the compiled-JavaScript delivery path.
**Cause or correction:** Pinned Pi provides SettingsManager.applyOverrides and an exported InMemoryCredentialStore; credential enumeration is metadata only. Production delivers compiled JavaScript rather than the lab's direct TypeScript entry.
**Prevention:** Read exact pinned declarations and scan existing exported helpers before coding an adapter. Compile the smallest boundary, preserve actual compiler failures, and align compiler options with the delivered runtime entry.

## Select the cached compiler explicitly for offline phases

**Problem:** A package-only build stopped before compilation because GOTOOLCHAIN=local selected Go1.27.1; a second bootstrap attempt failed with GOSUMDB disabled.
**Cause or correction:** The module requires Go1.27.3, which was already cached. Environment restrictions changed toolchain selection and verification before source compilation.
**Prevention:** Resolve the required cached compiler path before an offline phase, put its bin directory on PATH and keep normal checksum handling. Preserve setup failures separately from compiler or behavior failures.

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
