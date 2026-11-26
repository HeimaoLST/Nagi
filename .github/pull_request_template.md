## Semantic Change Description

<!-- Repeat per affected subsystem. Reviewers verify each claim against the diff. -->

- **Subsystem**: `<name + link to https://github.com/HeimaoLST/Nagi/blob/dev/docs/subsystems/README.md>`
- **Trigger condition**: `<When does this path/behavior execute?>`
- **Before**: `<Observable behavior before this PR>`
- **After**: `<Observable behavior after this PR>`
- **Impact** (`interface` / `state` / `compatibility` / `failure modes` / `existing data`): `<Describe or state None>`
- **Linked issues / credits**: `<Fixes #..., Co-authored-by if applicable>`

## Fail-Before-Fix (Break It On Purpose) Proof

<!-- Required for every bug fix or invariant guard (compile error, panic, or SKIP does not count). State N/A + rationale for pure docs PRs. -->

- **Reverted / sabotaged change**: `<What exact fix or invariant was reverted/broken>`
- **Observed behavioral failure**: `<Exact command + failing test name + assertion message>`
- **After restore**: `<Exact command + PASS result>`

## Verification Evidence

<!-- Record ONLY commands actually completed against the frozen candidate SHA. Mark unrun tiers explicitly as `not run`. -->

- **Candidate SHA**: `<commit-sha>`

| Verification Level | Command | Exit Code | Observed Result / Gaps |
| :--- | :--- | :--- | :--- |
| Governance guard | `python3 scripts/check-governance.py --strict .` | | |
| Format & vet | `test -z "$(gofmt -l .)" && CGO_ENABLED=0 python3 scripts/gui-go.py vet ./... && CGO_ENABLED=0 python3 scripts/gui-go.py vet -tags=integration ./...` | | |
| Headless & Visual Guards `[H]` | `CGO_ENABLED=0 NAGI_TEST_ONLINE=0 python3 scripts/gui-go.py test -count=1 ./...` | | |
| Built-worker Pi E2E `-race` | `NAGI_TEST_ONLINE=0 CGO_ENABLED=1 ... gui-go.py test -tags=integration -race -count=1 ./internal/pi ./internal/app` | | |
| Native `[N]` / Standalone / Real Provider | `<command or not run>` | | |

## Documentation

Follow the [documentation maintenance standard](https://github.com/HeimaoLST/Nagi/blob/dev/docs/README.md) and [subsystem index](https://github.com/HeimaoLST/Nagi/blob/dev/docs/subsystems/README.md). Record updated canonical docs (`README.md`, `docs/requirements/`, `docs/design/`, `docs/subsystems/`, or `docs/rfcs/`), or explain why no update is needed.

## UI Review

Apply [code-standards.md](https://github.com/HeimaoLST/Nagi/blob/dev/docs/code-standards.md), [ui-checklist.md](https://github.com/HeimaoLST/Nagi/blob/dev/docs/design/ui-checklist.md), and [ui-review.md](https://github.com/HeimaoLST/Nagi/blob/dev/docs/design/ui-review.md) for any user-visible change; otherwise record why UI review is not applicable.

- **Candidate SHA & Reviewer**: `<sha / reviewer>`
- **Affected pages, states, viewports, themes, locales & checklist items**: `<matrix>`
- **Headless appearance evidence (outside checkout)**: `<capture command + external paths>`
- **Native / live interaction evidence (action -> result)**: `<action records or not run>`
- **Findings (`P0` / `P1` / `P2` + repair plans) & Open Items**: `<None or list>`
- **Visual Verdict**: `[ ] PASS` | `[ ] PENDING` | `[ ] FAIL` | `[ ] Not applicable`
