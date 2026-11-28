# Xuhu code and UI standards

Read this alongside [AGENTS.md](../AGENTS.md), [lessons.md](../lessons.md), [desktop design](design/desktop.md) and [UI checklist](design/ui-checklist.md) before modifying code or reviewing a PR.

## 1. Change discipline

| Rule | What reviewers and CI verify |
| --- | --- |
| **Follow the whole path** | Start at the real GUI action. Follow captured object identity through Go state, persistence or the Pi RPC, readback and final rendering. Search for sibling callers, states, labels and surfaces affected by the same defect. Fix them together within the approved scope. |
| **Re-derive each dependency** | When a formula, layout threshold, theme token, schema or async step changes, check every caller, saved value, guard, fixture and document that relied on it. Include existing profiles and callbacks arriving after navigation or owner changes. |
| **Treat failed reads, parses and hashes as unknown** | Preserve the last valid state and original bytes. A failed read supplies no evidence of absence or equality. Fail closed where a permission, approval or integrity check depends on those bytes. Report the cause and a recovery action where available. |
| **Preserve explicit preferences** | Use defaults only for absent settings. Verify that opening, saving, reloading and migrating with an explicit value preserve its meaning. |
| **Keep data out of Go code** | Vendor directories, presets, icons and future extensible catalogs belong in embedded JSON + SVG data files with layered profile/plugin loading, not hardcoded Go slices. |
| **Do not reverse deliberate decisions** | Adopted visual roles, ownership bindings, no-replay rules and single-primary-CTA constraints in `docs/design/desktop.md` stay intact unless explicitly revised. |
| **One coherent goal per PR** | Keep unrelated changes outside the PR and respect the writer sets assigned by the lead. |

## 2. Machine-checkable GUI red lines

Every GUI change must satisfy all six rules, enforced by `internal/app/visual_guard_test.go` and `internal/theme/theme_test.go`:

1. **Shared primitives only (`internal/theme/`).** Production views in `internal/app/` must use shared `theme` controls (`PreferenceInput`, `FormTextArea`, `PreferenceSelect`, `ToggleSwitch`, `TagPill`, `PrimaryButton`, `SecondaryButton`, `DangerButton`, `GhostButton`, `TextAction`, `BrandIcon`, `StyleFrostedModalBackdrop`) instead of calling raw `ui.TextInputBase`, `ui.TextAreaBase`, `ui.SelectBase` or `ui.SwitchBase` directly (documented exemptions such as the auto-growing chat composer must be explicitly allowlisted in the static guard).
2. **Unified geometry and border language.** `1 DIP` hairline borders; unified corner radius scale (`9 DIP` controls, `12 DIP` cards/groups, `16 DIP` frosted modals, `99 DIP` capsules and switches); `180ms` color/shadow transitions; interactive hit targets `>= 32×32 DIP`; zero colored left-border status stripes (mark state with semantic dots or pills).
3. **Zero alignment drift.** Across all Settings tabs, `PageHeading` and every `PreferenceSectionHeader` below it share the same content left edge (`|X_delta| <= 2.5 DIP`). In horizontal form rows, the label's vertical center aligns with its paired control's first-line vertical center (`<= 4 DIP`).
4. **Authentic vectors with zero unsupported SVG.** Brand logos come from licensed SVG files referenced by JSON catalog manifests (with license attribution under `assets/licenses/`). Every built-in SVG must parse via `ui.ParseSVG` with `len(svg.Unsupported()) == 0`; monochrome SVGs use `currentColor` so Paper Light and Moss Dark adapt automatically.
5. **Single Primary CTA and strict WCAG AA contrast.** At most one solid accent-filled Primary button is active per visible screen; opening a modal automatically demotes any background Primary CTA. All text, muted labels and semantic inks maintain `>= 4.5:1` contrast against actual rendered surfaces in both Paper Light and Moss Dark.
6. **100% i18n parity and zero viewport clipping.** No hardcoded user-facing strings; `locales/en.json` and `locales/zh-Hans.json` maintain exact key and placeholder parity. Every page and modal fits `1440×900`, `1040×720` and `720×640` with zero clipped footer buttons, zero leaked internal IDs and zero unintended scroll jumps.

### Native control and layout details

- Keep inline actions intrinsic inside cards, drawers and preference rows. Use `AlignSelf(ui.Start)` for a standalone action in a filling column, or put actions in an inline wrapping `Row`. Sidebar category rows can fill their navigation group.
- Set a consistent square icon box beside labels and center both in the row. Correct off-center glyph ink within its own view box. Keep the same padding and gap across neighboring rows.
- Give preference rows clear titles, with a compact subtitle only when it helps explain the action or its effect. Allow the full subtitle to wrap beside the control. Stack the row when its available width calls for it. Use compact disclosures for secondary details and empty attachment sections while preserving visible validation and the user's scroll anchor.
- Keep scroll stable through clicks, typing, IME, redraw and disclosure changes. Scroll for explicit navigation or to expose an active keyboard target. Use the theme's `InputBorder` for rest-state inputs and MyGo themed `PopoverMenu` or shared pickers for application menus. Pair state color with text or a distinct shape. Check muted text at `≥ 4.5:1` on the actual Paper Light and Moss Dark surfaces.
- Use `TransitionControl` for palette-sensitive controls. Check the first frame after each direction of a theme switch and keep intra-palette hover/press transitions.

## 3. Standard development and verification workflow

1. **Visual preview first (structural UI changes).** Produce a 2× Retina preview covering Light/Dark and realistic multi-item data states and confirm direction before coding.
2. **Production pass.** Reuse shared controls and domain schemas. Run formatting, targeted build/vet and existing observable checks under the assigned suite limits. The development writer leaves unit test files to the test owner.
3. **Independent guard and behavior review.** The test owner verifies affected invariants and coordinates comprehensive headless and pinned-worker integration checks in a frozen checkout.
4. **Real-data visual evidence and verdict.** Capture original PNGs via `scripts/capture-ui-review.py` on a clean frozen checkout. Inspect whole-window originals at applicable sizes, themes, locales and states. Record observable native interactions and an independent verdict. Confirmed P0/P1 findings block merge.
5. **CI and delivery.** The lead integrates reviewed source after `gui`, `pi-integration` and `ci-required` pass. When delivering a signed standalone bundle, record the actual app/worker process and executable identities.

## 4. Guards and verification

1. **Self-discovering guards over manual lists.** *A guard test reads what it guards from the code, not from a list someone has to remember to update.* Prefer Go `go/ast` / `go/parser`, reflection, JSON schema scans, catalog traversal, or UI-tree introspection over hand-maintained allowlists.
2. **Browserless guards run in CI.** Static governance, link integrity, schema, i18n, and visual-guard suites (`python3 scripts/check-governance.py --strict .`, `TestVisualGuard*`, `TestPublishedCatalogContract`) run automatically on every PR without requiring a desktop window or network access.
3. **Fail-Before-Fix (Break It On Purpose).** For every bug fix or invariant guard, `nagi-review` (the independent test/QA owner) proves the test catches the real defect: revert the fix or sabotage the guarded invariant, confirm a genuine behavioral `FAIL` (compile errors, panics, or `SKIP` do not count), restore the fix to confirm `PASS`, and record the proof in the PR description.
4. **Green required gates.** Diagnose every failure. A baseline failure records the exact tested commit, command and result and stays open for its owner until resolved. A rerun may supply evidence about nondeterminism. A rerun alone does not resolve the cause or turn a failed required gate into a pass.
5. **Deterministic timing.** Wait for observable state, counts or process outcomes. Inject delays or gate exact interleavings when proving a race fix. Coordinate race validation with the test owner and keep the worker's failure and cleanup outcomes observable.
6. **Isolated runs.** Run at most one heavy whole-package or `-race` suite on a machine at a time. Capture `GOPATH`, `GOMODCACHE` and `GOCACHE` from `go env` before setting a temporary `HOME` and isolated profile. Use controlled local fixtures and synthetic credentials. Cancel and wait for owned helpers, workers, fakes, timers and goroutines before the test ends.
7. **Evidence levels.** Record exact commands, exits, tested source identity, observed results and remaining gaps. Include the actual Fail-Before-Fix assertion for applicable bug fixes and invariant guards. Distinguish source, build/vet, headless UI, built-worker integration, native UI, standalone packaging, real-provider and human acceptance.

## 5. Data safety, concurrency and packaging guardrails

1. **Preserve data on failed admission.** Keep the valid in-memory state and retain the original corrupt profile or recovery bytes before authorized replacement. Follow the [product protocol](design/product-protocol.md#current-profile-admission) for admission and readback.
2. **Concurrency & ownership hygiene.** Never hold locks across blocking I/O, IPC, network calls, or callbacks; complete shared state initialization before publishing across goroutines; bind every Bot, draft, activity, question, receipt, and file to its recorded owner ID; never auto-replay uncertain operations across restarts.
3. **Path safety and packaging.** Resolve path aliases before containment checks. Build with the pinned MyGo wrapper and match worker/runtime provenance to the packaged source and `.github/pi-runtime.json`.

## 6. Worktrees, commits and review

- The lead assigns each concurrent writer a dedicated worktree created with `git worktree add` and records its owner, branch, base and allowed paths. The Git owner verifies `git show --stat HEAD` and `git diff HEAD^ HEAD --stat` list only the intended changes, then checks the committed tree against current `origin/dev` to catch stale sibling content. Keep isolated source, test outputs and profiles with their owners.
- Use one goal per commit. Record exact verification commands and exits, the applicable Fail-Before-Fix failure, and remaining gaps in the commit or PR body. Freeze the candidate during validation.
- Merge only after applicable tests, visual review and required CI are green. Match the merge to the reviewed SHA with `gh pr merge --match-head-commit <reviewed-sha>`. A new HEAD needs renewed review and verification.

## 7. Enforcing checks

| Rule / Class | Enforcing Guard | Authoritative Location |
| --- | --- | --- |
| Docs, budgets, relative links & PR/lessons schemas | `scripts/check-governance.py --strict .` | [CI Workflow](../.github/workflows/ci.yml) & [Subsystem Index](subsystems/README.md) |
| Settings/modal alignment, contrast, SVGs, shared primitives & locale parity | `TestVisualGuard*` (`internal/app/visual_guard_test.go`) | [Visual Guard Suite](../internal/app/visual_guard_test.go) |
| EN / zh-Hans translation key & placeholder contract | `TestPublishedCatalogContract` & i18n tests | [Locales](../locales/catalog_gen.go) |
| Layered catalog safety (path traversal, symlink escape, malformed plugins) | `internal/connection/catalog_test.go` | [Provider Catalog](../internal/connection/catalog.go) |
| Design System 2.0 primitives & WCAG AA ink contrast | `internal/theme/ds2_test.go` | [DS2 Primitives](../internal/theme/ds2.go) |
