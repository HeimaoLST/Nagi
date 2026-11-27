# Nagi Subsystem Reference Index

> Single source of truth for how Nagi is architected and behaves today.

## Rules for Subsystem Docs

1. **Read before edit**: Read the owning subsystem doc before changing code in that subsystem.
2. **Update in the same PR**: Any PR that changes responsibilities, interfaces, state transitions, invariants, failure modes, or compatibility must update the affected subsystem doc in the same PR.
3. **Current truth only**: Document what is live today. Keep forward-looking proposals in [docs/rfcs/](../rfcs/) or PRs — never mix unbuilt behavior into normative subsystem docs.
4. **Link paths and symbols.** Use relative file links and name the relevant declaration, such as `LoadProviderCatalog`.
5. **Preserve deliberate decisions**: Document intentional trade-offs explicitly (including quiet visual choices such as hairline resting input borders) and do not silently revert them without review and doc updates.
6. **Verify link & index integrity**: Run `python3 scripts/check-governance.py --strict .` whenever adding or editing docs.

## Subsystem Index

| Subsystem | Document | Primary Owner / Paths | Source of Truth |
| :--- | :--- | :--- | :--- |
| Desktop UI & Design System 2.0 | [GUI Workbench](gui-workbench.md), [Desktop Design](../design/desktop.md), [UI Checklist](../design/ui-checklist.md), [UI Review](../design/ui-review.md), [Code Standards](../code-standards.md) | `internal/app/`, `internal/theme/`, `locales/` | MyGo layout, semantic theme tokens, shared controls, `TestVisualGuard*` |
| Provider Catalog & Multi-Provider Routing | [Desktop Design](../design/desktop.md), [Product Protocol](../design/product-protocol.md), [RFC-0003](../rfcs/0003-model-catalog-and-smart-routing.md) | `internal/connection/`, `internal/app/providers_ui.go`, `internal/app/provider_routing.go` | Layered JSON catalog (`catalog/providers.json` + profile plugins), worker provider store & routing health |
| Pi Runtime & Product Protocol | [Product Protocol](../design/product-protocol.md), [Pi Integration](../design/pi-integration.md), [Glossary](../design/glossary.md) | `internal/pi/`, `internal/pi/sessions.go`, `internal/pi/watches.go`, `internal/pi/worker/`, `internal/pi/worker/session-supervisor.ts`, `internal/pi/worker/pty-supervisor.c`, `internal/pi/worker/watch-scheduler.ts` | Go/TypeScript JSONL RPC boundary, receipts, events, owned worker, native session supervisor, and Seatbelt watch scheduler lifecycle |
| Profile Store, Materials & Recovery | [Product Protocol](../design/product-protocol.md#current-profile-admission), [Desktop Requirements](../requirements/desktop.md#v1) | `internal/store/`, `internal/materials/`, `internal/connection/config.go` | SQLite WAL profile store, backups, no-replay admission, reference integrity |
| Capabilities (Skills & MCP) | [Desktop Design](../design/desktop.md), [RFC-0001](../rfcs/0001-multi-brain-and-capabilities.md) | `internal/app/capabilities_ui.go`, `internal/app/bot_capabilities_view.go`, `internal/pi/skills.go` | Skill library, executable skill approval digests, MCP server pools & bindings |
| Packaging, Signing & CI Gates | [Pi Integration](../design/pi-integration.md), [CI Workflow](../../.github/workflows/ci.yml) | `scripts/build-pi-worker.py`, `scripts/build-macos-app.py`, `scripts/gui-go.py`, `.github/pi-runtime.json`, `bin/nagi-pty-supervisor` | Pinned Node + Pi worker payload, deterministic native helper signing, ad-hoc signed standalone `Nagi.app`, required CI checks |

---

## Subsystem Doc Template

Copy this structure when adding `docs/subsystems/<subsystem-name>.md`:

```markdown
# Subsystem: <Name>

## Responsibilities & Boundaries
- Owns:
- Explicitly does NOT own:

## Source of Truth
- Authoritative state/files/tables and owner IDs:

## Primary Execution Paths
- Entry points (file path + symbol name) → processing flow → side effects:

## State Transitions & Invariants
- Allowed states, transitions, and invariants that must always hold:

## Failure Modes & Compatibility
- Behavior on corrupt/missing input, partial failure, restart, and legacy data:

## Deliberate Decisions
- Intentional trade-offs that must NOT be silently reverted (with rationale):

## Verification Commands
- Fast local check:
- Guard / integration checks:
```
