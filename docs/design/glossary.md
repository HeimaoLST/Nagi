# Nagi Domain Glossary & Naming Standard

This document is the single authoritative domain vocabulary for Nagi across product requirements, SQLite storage, Go/TypeScript wire RPCs, events, and model tool schemas.

## Canonical Entities

| Canonical Term | Status | Definition | Stable Identifier | Storage / Wire / Tool Anchor |
| --- | --- | --- | --- | --- |
| **Bot** | Shipped | A long-lived member of the Bot Team. Built-in `PandoraBox` and user Bots share one uniform model. | `botId` | SQLite `bots`, wire `bot.*`, tools `bot_*` |
| **Conversation** | Shipped | The continuous chat stream between the user and one Bot (current editable chat or read-only previous chat). | `conversationId` | SQLite `conversations`, wire `conversation.*`, planned read tools `conversation_*` |
| **Activity** | Shipped | An independently tracked unit of work delegated to a Bot (e.g. researching a doc, implementing code, auditing a release). Persists across Bot switches and new chats. | `activityId` | SQLite `activities`, wire `activity.*`, tools `activity_*` (see legacy note below) |
| **Execution** | Shipped | One concrete model/runtime run inside a Conversation turn or an Activity start/continue cycle, with its own settlement and receipts. | `executionId` | SQLite `executions`, wire `execution.*` |
| **Question** | Shipped | A structured blocking question raised by an Activity when genuine human input is required. | `questionId` | Embedded on `Activity`, wire `question.answer` |
| **Material** | Shipped | Captured text snippets and file-reference snapshots attached to a Conversation draft or Activity. | `materialId` | `Draft.materials`, `Activity.materials` |
| **Memory** | Shipped | Strictly per-Bot lasting rules (L1.5), pending review learnings, and searchable dated work logs. | `ruleId`, `learningId`, `logId` | SQLite `bot_memory_*`, wire `memory.*`, tools `memory_*` |
| **Skill** | Planned ([RFC-0001](../rfcs/0001-multi-brain-and-capabilities.md)) | A reusable playbook (`SKILL.md`, M1) with optional OS-sandboxed Node 22 scripts (`scripts/*.mjs`, M3); global, workspace-scoped, or Bot-authored. | `skillId` | Skill catalog + `bots/<botId>/skills/`, wire `skill.*`, tools `skill_*` |
| **MCP** | Planned ([RFC-0001](../rfcs/0001-multi-brain-and-capabilities.md)) | A globally configured external service connector (`stdio` or Streamable HTTP), pooled by the worker and mounted per Bot with scoped tool permissions. | `serverId` | SQLite `mcp_servers` (nonsecret) + private credential vault, wire `mcp.*` |

## Naming Conventions

- **JSONL Wire RPCs & Events**: `<domain>.<verb>` in camelCase for both method names and payload fields (e.g. `activity.list`, `conversation.saveDraft`, `memory.reviewLearning`, `execution.settled`).
- **Model Tool Names & Inputs**: `<domain>_<verb>` sorted alphabetically for KV-cache determinism, with `snake_case` input parameters (e.g. `memory_save_rule`, `bot_inspect_team`, `activity_get(activity_id)`).
- **UI Terminology**: Use **Bot Team**, **Bot**, **Materials**, **Activity**, **Previous chats**, **Settings**, and **Memory & Learnings** consistently; localized display strings live exclusively in `locales/`.

## Retirement of Legacy `issue` Terminology

The term `issue` was an early storage name for **Activity** and is retired as a product concept.

- **Allowed read-only historical scope**: Legacy `issue` fields remain strictly confined to read-only historical admission and transcript readers — specifically the v10 `contacts.json` decoder (`internal/store/store.go`, `internal/pi/worker/product-profile.ts`), retained v1 import metadata, and historical Pi transcript readers (`readImportedActivityResult` and `nagi.issue_tools.*` filtering in `internal/pi/worker/product.ts`).
- **Active migration tracked in [RFC-0001](../rfcs/0001-multi-brain-and-capabilities.md)**:
  - **M0a**: Replace remaining live model tool names (`delegate_activity`, `issue_list`, `issue_get(issue_id)`, `issue_action(issue_id)`, `issue_wait`) with `activity_delegate`, `activity_list`, `activity_get(activity_id)`, `activity_action(activity_id)`, and `activity_ask`.
  - **M0b**: Normalize remaining live Go runtime symbols under `internal/app/` from `Issue` to `Activity` while keeping `internal/store`'s v10 legacy decoder intact.
