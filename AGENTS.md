# Nagi contributor instructions

Updated: 2026-11-13.

## Ownership and boundaries

Nagi is an independent product repository. Its root Go module is `github.com/HeimaoLST/Nagi`. Product Go files, assets, locales, MyGo patches, desktop requirements and UI design belong here. Preserve the exported product behavior unless the task explicitly changes it; repository separation is not a UI redesign.

Saki owns the Agent execution loop, providers, tools, compaction, session storage and the [public Runtime contract](https://github.com/HeimaoLST/Saki/blob/dev/docs/design/runtime-api.md). Nagi consumes version 1 stdio JSONL and supplies its own business tools through MCP. Do not import or copy Saki execution implementations, parse journal files, compile a sibling Saki checkout, or make contacts/issues a Runtime prerequisite.

Keep product state and execution facts separate. Contact/issue/supplement/scope ledgers belong to Nagi; messages, tool results and task outcomes are projections of Runtime responses and history. Accepted delivery is not task completion; completed execution is not issue completion. Keep unknown delivery/persistence states and avoid automatic retries of uncertain side effects.

Preserve existing environment variables, data directories, IDs, binary lookup order, cancellation, busy/no-queue and EOF/shutdown drain behavior. Do not perform automatic data migration or introduce a daemon, remote service, background scheduling or new title/model calls as part of this split.

## Work and Git

Read the actual task and repository state before editing. Keep changes scoped, names conventional and each responsibility implemented once. Avoid speculative abstractions and repeated defensive guards. Do not operate other products' worktrees or old Saki checkouts.

Product owners maintain Go/assets, product docs, `gui-go.py`, `build-macos-app.sh` and macOS minimum support. QA maintains `.github/workflows`, `.github/saki-runtime.json`, artifact fetch support and `check-gui-runtime-results.py`. Each task declares a unique lead, stable agent identity, exact writer paths and Git authority. Coordinate task members through hc after checking current identity/task/status; do not route by remembered terminal numbers. Workers do not launch more agents or mutate Git/GitHub unless expressly delegated.

Use precise English for inter-agent task briefs, messages, reports and handoffs. User-facing updates may remain in Chinese.

Use scoped feature branches and reviewable PRs targeting `dev`; preserve original source history and imported evidence. Do not force-push, rewrite others’ branches, tag, publish Releases or deploy without explicit authorization. User approval and applicable existing authorization determine commit/push/merge scope; a green CI result alone does not grant it. Keep unfinished work in review and state the remaining gates. During #333, PM retains Git/integration ownership.

Worktrees and system temporary directories are disposable development environments, not durable project storage. Keep authoritative source, requirements, design constraints, notices and relevant validation summaries in the repository or its reviewable PR. At a completed scoped boundary, commit and push, complete applicable review and required CI, and integrate into `dev` promptly; do not accumulate finished work only in worktrees. For unfinished work, push a clearly labelled checkpoint/WIP branch at a safe source/validation boundary, without merging incomplete work. Bulk generated previews and raw logs may stay outside source as supplemental evidence, but must not be the only acceptance record. Before cleanup, verify that necessary commits are present remotely and completed work is integrated; preserve other owners’ unfinished changes.

Use hc background terminals with explicit repository cwd for long builds/tests and wait with hc. Do not use old tmux routes. Report actual command, exit code, files and limitations. No external-model tests, native GUI acceptance or old MVP resumption without explicit authorization. Historical product acceptance remains incomplete.

## Validation and documentation

Use `python3 scripts/gui-go.py build|test|vet` from the root; it preserves the pinned MyGo patches without mutating the module cache. Keep dependency versions unchanged unless requested. Build outputs and process evidence go outside the repository. Use the existing behavior tests; do not add tests for literal replacements or implementation trivia.

For product changes run the applicable Go tests and vet. Integration must explicitly use an independently built `SAKI_TEST_RUNTIME_BIN` and a same-checkout `SAKI_TEST_ISSUE_TOOL_HELPER`; a default test run may skip that coverage. Packaging requires a clean source commit and an explicit Runtime artifact; `VERSION` belongs to Nagi, Runtime version/source/hash are separate provenance.

Keep current facts in the existing topic docs and retain dated historical evidence as history. Link Runtime contracts to Saki rather than maintaining a second authoritative protocol specification. Do not imply imported CI or screenshots validate a changed product. Update caller-visible contracts, errors and constraints; avoid comments that narrate obvious code.
