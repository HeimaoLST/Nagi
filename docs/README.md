# Nagi Documentation

This directory holds Nagi's product requirements, implemented design contracts, and forward-looking architecture RFCs.

## Documentation Map

### Current Implemented Truth

- **Requirements**: [Desktop v1](requirements/desktop.md#v1) — current user-visible flows, from first connection to retrieving work after reopening.
- **Canonical Glossary**: [Domain Glossary](design/glossary.md) — single authoritative vocabulary across product docs, storage, wire RPCs, events, and model tools.
- **Product Protocol**: [Product Protocol v1](design/product-protocol.md) — implemented Go/TypeScript JSONL wire contract, receipts, events, and profile admission.
- **Pi Runtime**: [Pi Integration](design/pi-integration.md) — process ownership, startup/shutdown, packaging, and standalone verification.
- **Native Desktop UI**: [Desktop Design](design/desktop.md), [UI Checklist](design/ui-checklist.md), and [UI Review Workflow](design/ui-review.md) — implemented MyGo layout, visual tokens, and visual review gates.
- **Engineering & UI Standards**: [Code and UI Standards](code-standards.md) — fix-the-class rules, machine-checkable GUI red lines, Fail-Before-Fix, and verification taxonomy.
- **Subsystem Reference Index**: [Subsystem Index](subsystems/README.md) — single-source-of-truth map of live subsystems and update rules.

### Architecture RFCs

- [RFC-0001: Multi-Brain Execution, Direct Data Inspection & Unified Capabilities](rfcs/0001-multi-brain-and-capabilities.md) — `Implemented`.
- [RFC-0002: Bot-Activity Control Plane v2](rfcs/0002-bot-activity-control-plane.md) — `Implemented`.
- [RFC-0003: Model Catalog, Ranked Multi-Provider Routing & Health Failover](rfcs/0003-model-catalog-and-smart-routing.md) — `Implemented`.
- [RFC-0004: Stateful Session Handles, Event Watch Substrate & Scoped Autonomous Orchestration](rfcs/0004-stateful-sessions-and-autonomous-orchestration.md) — `Partially Implemented` *(`BG-1`–`BG-2`, `BG-3` backend SQLite `v14`, and `BG-3a` native lifecycle shipped; `BG-3b` UI integration pending).*

### Repository Root References

- [README](../README.md) — product overview, usage, and development build commands.
- [AGENTS.md](../AGENTS.md) — AI contributor and orchestration rules.
- [lessons.md](../lessons.md) — concrete past mistakes, causes, and prevention rules.

---

## Documentation Maintenance Standard

1. **Separate Current Truth from Future Proposals**:
   - `docs/requirements/` and `docs/design/` document **only what is currently implemented** (or implemented in the same PR). Never mix unbuilt roadmap behavior into `docs/design/` as if it were live.
   - Multi-milestone designs and major architectural decisions start as numbered RFCs under `docs/rfcs/NNNN-short-slug.md` (named by domain capability rather than frontend/backend team split) with an explicit header (`Status`, `Baseline`, `Promotion Targets`, `Milestones`) and a concise **Decision Rationale** section explaining why alternatives were rejected.
2. **RFC Lifecycle & Promotion Rule**:
   - Allowed RFC statuses: `Draft` → `Review` → `Accepted` → `Partially Implemented` → `Implemented` (or `Superseded` / `Rejected`).
   - When a milestone merges to `dev`, **promote its shipped contracts into the canonical `docs/design/`, `docs/requirements/`, and `README.md` documents in the same PR**, and check off that milestone in the RFC.
   - Once all milestones ship, flip the RFC to `Implemented` with links to the canonical `docs/design/` sections; keep it read-only as architectural history rather than a competing normative spec.
3. **Single Source of Truth**:
   - Define each domain term, wire contract, UI token, or build command in exactly one authoritative document (`docs/design/glossary.md`, `docs/design/product-protocol.md`, `docs/design/desktop.md`, `README.md`). Other documents link to it rather than duplicating tables.
   - Remove superseded normative instructions promptly rather than preserving competing designs.
4. **PR Synchronization Gate**:
   - Any PR that changes user-visible flows, wire RPCs/events, model tool names, SQLite schema, backup/restore boundaries, security guarantees, or build steps must update the corresponding canonical doc in the same PR (checked via `.github/pull_request_template.md`).
5. **Language Consistency**:
   - English is the default canonical language for engineering, protocol, glossary, and RFC documents. Existing Chinese desktop product docs (`docs/requirements/desktop.md`, `docs/design/desktop.md`) remain valid in place until localized before public release. Keep each individual document in one consistent language; product UI copy belongs exclusively in `locales/`.
6. **External Evidence Only**:
   - Keep build outputs, raw verification logs, QA reports, and original screenshot captures outside Git (e.g. under `/private/tmp/...`) and summarize commands, exit codes, and verdicts in the PR record.
