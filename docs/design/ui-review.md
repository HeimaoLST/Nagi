# UI review workflow

[AGENTS.md](../../AGENTS.md) requires this review for every change that affects the visible interface or how users interact with it. Use the [UI design checklist](ui-checklist.md) for criteria, evidence grades and verdicts. The approved phase 1 corrections take precedence over the corresponding values in the [current visual contract](desktop.md#qoder-complete-layout-20261116). Phase 2 remains a proposal.

1. **Scope the review.** List the changed surfaces and checklist IDs before collecting evidence. Follow shared components and state producers to the other pages they affect. Choose the relevant window sizes, themes, languages, data, scroll positions and interaction states from §§4–6 and §12. Include extremes that can change the result, such as a narrow pane or a long translated label. For whole-app sign-off, cover the full applicable matrix, including the four independent first-look exercises in A-6.
2. **Freeze and capture.** Record the full commit and keep the candidate fixed while collecting evidence. Use a clean checkout and isolated data. Prefer an existing whole-page preview for headless evidence. For a native review, record the application package hash, OS, isolated profile and fixture, and the observed window dimensions in DIP and display scale. Follow §12 for the required actions, original frames and other evidence. Use the same isolated profile on independent relaunches and verify the observed window belongs to the intended process. Keep baseline and candidate captures comparable by matching the scene, theme, locale, scale and scroll position.
3. **Inspect.** Open the originals at their captured resolution. Read the full page and inspect the controls and states named in the scope. Compare against the adopted contract and the matched baseline where applicable. For hover, keyboard focus, hit targets, menus, IME, resizing and other action-dependent claims, record the control, action and outcome. Use native evidence where required. Record a blocked or unobserved step as unverified with the scene needed to complete it. Run the A-6 exercises with a reviewer who did not implement the change.
4. **Record the decision.** Use the form below and §7.3 of the checklist. Apply §7.2 to the recorded scope. Keep P2 failures in the record and include a repair plan with an owner or issue. On a correction, freeze the new commit and check the affected scenes again. Record the verdict and evidence location in the PR or delivery record. UI acceptance and merge require a passing review. Keep a draft while applicable review remains pending or has failed.

## Capture headless previews

Run from the root of the clean frozen checkout. Long local captures use the owned HC execution procedure in [AGENTS.md](../../AGENTS.md). Choose an unused output path outside the checkout.

```sh
python3 scripts/capture-ui-review.py "$TMPDIR/nagi-ui-review" \
  --test TestPreviewSettingsPage --test TestPreviewMarkdownLinks
```

Without `--test`, the command runs the existing `TestPreviewImages` suite, which renders chat, new contact, Settings, sources and history, among other appearance scenes. Select additional existing preview roots for the change. `TestPreviewI18nSelectedSlice` covers localized Settings, history and copy scenes. `TestPreviewInputStates` covers representative form states. Inspect each fixture and its output to confirm which scene is present. A filename beginning with `native-` from a headless preview root still has grade H.

The command uses the existing MyGo test wrapper, writes original PNGs and raw logs, and records the commit, selected tests, test outcomes and image dimensions and hashes in `capture.json`. PNG dimensions are pixel dimensions. Record logical size and scale from the fixture or native window in the review. The CI GUI job also publishes this capture for the candidate when setup succeeds, including available evidence from failed captures. Find the originals and logs under the `nagi-headless-previews` artifact. Capture status reports the preview command and available files. The reviewer records the visual verdict separately.

## Review record

Keep the filled record with the evidence outside the checkout. Copy its summary and evidence link to the PR. Add one row per rule or checklist item in each scene needed to establish the claim. Use `pass`, `fail`, `unverified` or `not applicable` for rows, and `pass`, `fail` or `pending` for the review. Give a reason for each `not applicable` item and name the missing scene or action for each `unverified` item.

```markdown
Candidate commit:
Reviewer and review date:
Scope and checklist IDs:
Baseline commit and evidence:
Native package hash, OS and isolated profile (for native review):
Capture command, exit and evidence location:
Review verdict:

| Checklist ID | Page, state and action | DIP × DIP @ scale | Theme and language | Data fixture | Evidence grade and original / action record | Result | Severity | Observation, next scene or repair plan |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| | | | | | | | | |

Blocking P0/P1 findings:
P2 repair plans:
Pending scenes and reasons:
```
