# Nagi code and UI standards

> **Design North Star:** *Nagi — one quiet paper/moss surface, authentic brand vectors, shared MyGo controls, zero unaligned or unguarded UI.*

Read this alongside [AGENTS.md](../AGENTS.md), [lessons.md](../lessons.md), [desktop design](design/desktop.md) and [UI checklist](design/ui-checklist.md) before modifying code or reviewing a PR.

## 1. Change discipline (fix the class, not the sample)

| Rule | What reviewers and CI verify |
| --- | --- |
| **Fix the whole class** | When a visual, layout, state or protocol defect is found on one screen, grep and fix every sibling surface (all Settings tabs, modals, light/dark themes, EN/zh-Hans locales, wide/narrow sizes) in the same change. |
| **Re-derive every dependent** | Changing a theme token, control radius, catalog schema or async transition requires updating all callers, design docs and guard tests that rely on the old assumption. |
| **Failed read/parse means unknown, never crash or wipe** | Malformed user/plugin JSON or incompatible external SVG assets degrade safely (skip + diagnostic warning or fallback icon); never panic, never overwrite unread user files as empty. |
| **Keep data out of Go code** | Vendor directories, presets, icons and future extensible catalogs belong in embedded JSON + SVG data files with layered profile/plugin loading, not hardcoded Go slices. |
| **Do not reverse deliberate decisions** | Adopted visual roles, ownership bindings, no-replay rules and single-primary-CTA constraints in `docs/design/desktop.md` stay intact unless explicitly revised. |
| **One coherent goal per PR** | Keep unrelated refactors out of feature PRs; preserve strict writer fences (`dev` production only, `review` tests/QA only, `orchestrator` Git/packaging only). |

## 2. Machine-checkable GUI red lines

Every GUI change must satisfy all six rules, enforced by `internal/app/visual_guard_test.go` and `internal/theme/theme_test.go`:

1. **Shared primitives only (`internal/theme/`).** Production views in `internal/app/` must use shared `theme` controls (`PreferenceInput`, `FormTextArea`, `PreferenceSelect`, `ToggleSwitch`, `TagPill`, `PrimaryButton`, `SecondaryButton`, `DangerButton`, `GhostButton`, `TextAction`, `BrandIcon`, `StyleFrostedModalBackdrop`) instead of calling raw `ui.TextInputBase`, `ui.TextAreaBase`, `ui.SelectBase` or `ui.SwitchBase` directly (documented exemptions such as the auto-growing chat composer must be explicitly allowlisted in the static guard).
2. **Unified geometry and border language.** `1 DIP` hairline borders; unified corner radius scale (`9 DIP` controls, `12 DIP` cards/groups, `16 DIP` frosted modals, `99 DIP` capsules and switches); `180ms` color/shadow transitions; interactive hit targets `>= 32×32 DIP`; zero colored left-border status stripes (mark state with semantic dots or pills).
3. **Zero alignment drift.** Across all Settings tabs, `PageHeading` and every `PreferenceSectionHeader` below it share the same content left edge (`|X_delta| <= 2.5 DIP`). In horizontal form rows, the label's vertical center aligns with its paired control's first-line vertical center (`<= 4 DIP`).
4. **Authentic vectors with zero unsupported SVG.** Brand logos come from licensed SVG files referenced by JSON catalog manifests (with license attribution under `assets/licenses/`). Every built-in SVG must parse via `ui.ParseSVG` with `len(svg.Unsupported()) == 0`; monochrome SVGs use `currentColor` so Paper Light and Moss Dark adapt automatically.
5. **Single Primary CTA and strict WCAG AA contrast.** At most one solid accent-filled Primary button is active per visible screen; opening a modal automatically demotes any background Primary CTA. All text, muted labels and semantic inks maintain `>= 4.5:1` contrast against actual rendered surfaces in both Paper Light and Moss Dark.
6. **100% i18n parity and zero viewport clipping.** No hardcoded user-facing strings; `locales/en.json` and `locales/zh-Hans.json` maintain exact key and placeholder parity. Every page and modal fits `1440×900`, `1040×720` and `720×640` with zero clipped footer buttons, zero leaked internal IDs and zero unintended scroll jumps.

## 3. Standard development and verification workflow

1. **Visual preview first (structural UI changes).** Produce a 2× Retina preview covering Light/Dark and realistic multi-item data states and confirm direction before coding.
2. **Production pass (`nagi-dev`).** Build or refine shared primitives in `internal/theme/` and data schemas first, then update screens. Zero `*_test.go` edits; run `vet`, `build`, `gofmt` and test-hash lock before handoff.
3. **Guard & test alignment (`nagi-review`).** Align affected tests, add or extend `TestVisualGuard*` invariants (verifying each new guard catches regressions when the invariant is violated), and run the full headless suite plus built-worker Pi integration.
4. **Real-data visual evidence & verdict.** Capture original PNGs on a clean frozen checkout via `scripts/capture-ui-review.py` using realistic seeded fixtures (multiple providers, models, bots, dark/light, EN/zh-Hans, wide/narrow). Open and inspect every original PNG and record an explicit visual verdict; P0/P1 findings block merge.
5. **CI & signed standalone delivery (orchestrator).** Require green `CI/gui`, `CI/pi-integration` and `CI/ci-required`, merge to `dev`, rebuild and sign standalone `Nagi.app`, and verify launched GUI + bundled worker PIDs and executable inodes.
