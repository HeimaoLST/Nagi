# UI review workflow

Review every change that affects the visible interface or user interaction. Use the v1 flows to scope the product behavior and the current visual contract and checklist to judge it. [v1 requirements](../requirements/desktop.md#v1) · [Visual contract](desktop.md#qoder-complete-layout-20261116) · [Checklist](ui-checklist.md)

1. **Scope the review.** List changed user flows and checklist IDs. Follow shared views and state to other affected pages. Select window sizes, themes, locales, scroll positions and interaction states from the checklist. For whole-app review, include the A-6 first-look exercises. [Native v1 checklist](ui-checklist.md#native-v1-review)
2. **Freeze and capture.** Record the full commit and keep the candidate fixed. Use an isolated profile, the matching product worker, a controlled local provider and synthetic credentials for native scenarios. Record package hash, OS, fixture, actual content dimensions in DIP and display scale. Confirm the window belongs to the candidate process. Reuse the same isolated profile for reopen and match baseline and candidate conditions when comparing.
3. **Inspect.** Open original whole-window frames. Record the target, action and outcome for keyboard, scroll, hover, hit targets, menus, IME and resize. Record actor and object identity for activity routing, prior chats and recovery. Use native action evidence where the checklist requires it and identify blocked or unobserved actions. Have someone who did not implement the change perform the A-6 observations.
4. **Record the decision.** Apply §7.2 to the scoped findings. Record P2 repair owners or issues. Freeze a new candidate for corrections and repeat the affected scenarios. Attach the verdict and evidence location to the PR or delivery record. Acceptance and merge require a passing applicable UI review.

## Capture headless previews

Run from the root of the clean frozen checkout. Long local captures use the owned HC execution procedure in [AGENTS.md](../../AGENTS.md). Choose an unused output path outside the checkout.

```sh
python3 scripts/capture-ui-review.py "$TMPDIR/nagi-ui-review" \
  --test TestPreviewSettingsPage --test TestPreviewMarkdownLinks
```

The default `TestPreviewImages` capture covers chat, new contact and Settings. For targeted review, use `TestPreviewSettingsPage`, `TestPreviewContactProfile`, `TestPreviewConnectionSettings`, `TestPreviewMarkdownLinks`, `TestPreviewI18nSelectedSlice` or `TestPreviewInputStates`. Inspect the fixture and record the scenes actually captured. Use the native checklist for persisted history, independent activities and their results.

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
