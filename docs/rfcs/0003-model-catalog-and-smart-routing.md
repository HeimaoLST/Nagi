# RFC-0003: Model Catalog, Ranked Multi-Provider Routing & Health Failover

- **Status**: `Partially Implemented` *(Astra-6.2 deep technical review resolved; all backend milestones `RT-1`–`RT-2` shipped on SQLite v11 and promoted to `docs/design/product-protocol.md`; consolidated frontend UI handoff delivered to `t1vb`)*
- **Baseline**: `dev` @ `24519ac` (RFC-0001 `M0a`–`M4`, RFC-0002 `CP-1`–`CP-3`, and RFC-0003 `RT-1` merged; pinned Pi SDK `1.5.2`)
- **Authors / Reviewers**: Backend Architecture (`t21d`), Deep Technical Review (`t1jr` / Astra-6.2, including offline Node `22.23.5` / `@earendil-works/pi-ai` `1.5.2` catalog & scoring probes)
- **Canonical Vocabulary**: [docs/design/glossary.md](../design/glossary.md)
- **Promotion Targets**: [product-protocol.md](../design/product-protocol.md), [glossary.md](../design/glossary.md), [desktop.md](../design/desktop.md), [README.md](../../README.md)

## Milestone Tracker

- [x] **RT-1** *(SQLite v11, strictly after CP-3 v10)* — Deterministic Offline Baked Catalog (public `@earendil-works/pi-ai/providers/all` exports + pinned `models.dev` snapshot & MIT notice in `build-pi-worker.py` + curated `model-ranks.json`), Source-Scoped Canonical Identity & Supply-Route Capabilities, Safe Default Active Model Pool (`*[]string` `enabledModels`), Provider Priority Ordering (`provider.reorder` over `sort_order`), `fallbackOnly`, Calibrated Capability-Shortlist Weighted Auto-Routing, Lower-Only Thinking Clamping & Actual Pi Registration Compat, and Legacy M4 Editor Compatibility
- [x] **RT-2** — Four-Slot Manual Overrides (`routing.setOverrides`: `auto` / `pinned_model` / `pinned_route` with Fail-Closed Exact Pins & Per-Role `pinStatus` Readback), Per-Route Gateway `429`/`5xx` Cooldown & Half-Open Single-Lease New-Turn Failover (Same-Model-First), Orthogonal Runtime Hard Gates (`requiresTools`, `minContextTokens`, future `requiresVision`), and Unmodified M4 Go Client Event-Stream Compatibility

---

## Problem Statement

RFC-0001 M4 shipped the private `connection.json` v3 multi-Provider pool, Save/Apply/Revoke semantics, four cognitive roles (`dialogue`, `coding`, `reasoning`, `utility`), and hot-conversation stickiness. However, the initial router (`internal/pi/worker/model-router.ts:37-82`) relies on 45 lines of regex heuristics that break down under realistic multi-Provider setups combining official accounts, aggregators (**Magpie**, **OpenRouter**, relays), and local **Ollama**:

1. **Aggregator Model Overlap & Identity Conflation**: Multiple Providers frequently expose the *same* underlying model under distinct vendor/gateway names (`openai/gpt-5.4`, `gpt-5.4`). Flattening `(providerId, wireModelId)` causes duplicate choices, arbitrary tie-breaks, and accidental cross-Provider switching that invalidates Provider-cluster KV Prompt Cache—while naive wildcard/date stripping risks falsely merging incompatible endpoints or different model generations.
2. **Unfiltered Discovery Admitting Non-Chat & Price-Protected SKUs**: Current UI/worker behavior copies up to 128 discovered `/models` IDs straight into `account.models` on Save (`providers_ui.go:258-307`, `product.ts:534-574`), allowing embeddings (`text-embedding-3-small`), audio/image generators, and `$150/M` Pro SKUs (`o1-pro`) to enter automatic routing unintentionally.
3. **Hardcoded Regex Misses Mainstream Models & Guesses `thinkingLevel`**: Regex matching misses Chinese and newer global families (`deepseek-v4-pro`, `qwen3-coder-plus`, `glm-4.7`, `kimi-k2-*`, `gpt-6.*`, local `qwen3:8b`), ignores rich bundled `@earendil-works/pi-ai` metadata (43 providers / 1,643 chat models) and `models.dev` specs, and registers a synthetic text-only Pi provider with zero thinking map or adapter compat (`worker.ts:185-205`).
4. **No Explicit User Control Over Channel Order, Active Pool or Slot Overrides**: Channel economics differ dramatically across subscriptions, enterprise quotas, discounted gateways, and official pay-as-you-go backup. Users need explicit Provider priority ordering, per-Provider enabled model control, `fallbackOnly` channels, and per-slot `Auto` vs `Manual` overrides.
5. **No Transient `429`/`5xx` New-Run Failover**: While mid-execution switching must remain strictly prohibited (M4 invariant), an aggregator route hitting `429` or `5xx` currently receives new turns indefinitely instead of cooling down that exact route and failing over new admissions to the next healthy Provider supplying the same model.

---

## Five Routing Axioms

1. **Keep 4 Visible Slots + Orthogonal Runtime Hard Gates**: Preserve the 4 user-facing cognitive slots (`dialogue`, `coding`, `reasoning`, `utility`) for approachable Settings UX; handle `requiresTools`, `minContextTokens`, and future `requiresVision` (only once native image admission, Pi registration, and image transport ship) as runtime hard gates rather than adding permanent UI slots.
2. **Separate Identity, Supply-Route Capabilities & Role Ranks**: Keep transmitted `wireModelId` byte-for-byte. Store role fitness ranks at canonical/family level, resolve objective limits/tools/thinking/compat/price strictly at the **supply route** with explicit `known true` / `known false` / `unknown` evidence, and require build-time non-overlapping exact aliases for cross-Provider model equivalence.
3. **Offline-First Baked Metadata (Zero Startup Fetches)**: Generate a reproducible, attribution-complete offline catalog from public `@earendil-works/pi-ai/providers/all` exports + a pinned `models.dev` source commit (packaged with its MIT notice in `build-pi-worker.py`) + curated `model-ranks.json`. Defer runtime `catalog.refresh` beyond RT-2.
4. **Explicit User Authority & Non-Inverting Shortlist Scoring**: Users own Provider priority order, active model opt-in (including mandatory explicit opt-in for price-protected Pro SKUs in **all** policies), `fallbackOnly` channels, and per-slot `auto` / `pinned_model` / `pinned_route` overrides. In `auto` mode, apply capability-margin shortlists before integer-weighted scoring over applied Provider order, and rank authorized routes *independently* of transient health so same-model failover stays intact.
5. **Fail-Closed Exact Pins, Per-Route Gateway Cooldown & Zero Mid-Execution Switching**: Never switch Provider mid-execution. Exact `pinned_route` fails closed (`route_unavailable` / `credential_unavailable`); `pinned_model` fails over strictly across authorized supplies of that exact canonical ID. Scope gateway `429` (and aggregator `5xx`) cooldowns to the exact `(providerId, endpoint, wireApi, wireModelId)` route by default, preserve M4's closed `ProductProviderChange.valid()` wire stream, and expose volatile health on reads without bumping revisions.

---

## 1. Offline Baked Model Catalog, Identity & Route Capabilities

### 1.1 Reproducible Offline Catalog Provenance

- **Public Pi SDK Exports (Verified Offline)**:
  - Maintenance/build generator imports `@earendil-works/pi-ai/providers/all` (`getBuiltinProviders()`, `getBuiltinModels(provider)` — verified offline on bundled Node `22.23.5` with zero network calls: **43 providers / 1,643 chat models**, `getBuiltinModelDataGeneratedAt()`) and `@earendil-works/pi-ai/models` (`getSupportedThinkingLevels`, `clampThinkingLevel`).
- **Pinned `models.dev` Snapshot & License Packaging**:
  - Pin an explicit upstream `anomalyco/models.dev` commit (e.g., `066eb88a4dbbe15d5274ad5528070ce5112bbe18`) and raw-input SHA-256 rather than hashing a mutable live URL.
  - Run a deterministic field-allowlist generator (`scripts/sync-model-catalog.py` / TS generator) producing a sorted JSON artifact recording raw-input digest, generator schema version, Pi lock digest, `model-ranks.json` revision, upstream MIT attribution, and output SHA-256.
  - Explicitly update `scripts/build-pi-worker.py` (currently copying top-level `.ts` and enumerated licenses) so the generated catalog JSON, `model-ranks.json`, and `models.dev` MIT notice are tracked in the Nagi input manifest and packaged cleanly into standalone payloads.
  - **Runtime Offline Guarantee**: Startup, discovery enrichment, routing, and recovery perform **zero** requests to `models.dev`. Arbitrary catalog or provider data can never inject executable config, alternate URLs, or credentials. Runtime `catalog.refresh` is explicitly deferred beyond RT-2.

### 1.2 Exact Source-Scoped Canonical Identity vs Family Ranks

- **Transmitted Wire ID**: Always send each Provider's exact `wireModelId` byte-for-byte.
- **Exact Identity Mapping (Used for Deduplication & `pinned_model`)**:
  - Resolved strictly via explicit, source-scoped exact aliases in a versioned table (build-time validated to reject any overlapping rules).
  - Recognized gateway adapters apply only for verified endpoints or explicit mappings—a user-chosen Provider display name, arbitrary proxy URL, or leading `openai/` alone cannot assert cross-vendor equivalence.
  - Preserve distinctness of fixed dated deployments vs moving aliases, Ollama sizes/tags/quantizations (`qwen3:8b` vs `qwen3:32b`), Azure deployment names, Bedrock inference profiles, and gateway `:thinking` / `:extended` / opaque suffixes. A verified `:free` sibling may share a canonical model ID only while keeping its own price, quotas, limits, wire ID, and independent opt-in authorization.
  - Unknown IDs map unambiguously to `(providerId, wireModelId)` (encoded as `custom:<len>:<providerId>:<wireModelId>` so two unrelated private `"default"` deployments never merge).
- **Family Patterns (Used Strictly for Role Fitness Ranks)**:
  - Family patterns in `model-ranks.json` supply `{ dialogue, coding, reasoning, utility }` fitness scores (`0..100`), `speed` (`0..100`), `costTier`, and `priceProtected: boolean` (`defaultEligible: false` for ultra-expensive Pro SKUs like `o1-pro`, `gpt-5.4-pro`) without ever merging distinct deployments into one canonical identity.

### 1.3 Supply-Route Capability Resolution & Lower-Only Thinking Clamping

- **Route-Level Objective Evidence (`known true` | `known false` | `unknown`)**:
  - Resolve `isChat`, `toolCall`, `reasoning`, `supportedThinkingLevels`, `inputModalities`, `contextWindow`, `maxOutputTokens`, `compat`, and channel `cost` **per supply route**, intersecting the exact selected Nagi wire adapter (`openai-completions` vs `openai-responses`), gateway restrictions, and account configured `contextTokens`/`outputTokens`. Never copy OpenAI's official limits or compat flags onto an arbitrary third-party proxy solely because a model ID matches.
  - Reject impossible input budgets after reserving `outputTokens` (accounting for system/tool/history overhead where prompt length drives `minContextTokens`).
  - `/models` advertisement proves existence only—not chat, tool calling, vision, or reasoning. Unknown reasoning stays `false` (`off` only). Gated requirements (`requiresTools`, future `requiresVision`) require positive evidence (`known true`) or an explicit user per-route capability declaration (`modelOverrides`), while explicitly documenting legacy M4 configured `defaultModel` compatibility on imported accounts without extending that assumption to every discovered ID.
  - Claim vision capability only once native image admission, Pi model registration (`input: ['text', 'image']`), and actual image transport are implemented and verified.
- **Lower-Only Thinking Level Clamping & Real Pi Registration (`worker.ts:185-205`)**:
  - Offline Pi `1.5.2` inspection showed `clampThinkingLevel` clamps DeepSeek `medium` **up to `high`** and Bedrock `off` **up to `minimal`**. Nagi MUST intersect supported adapter levels with Nagi's vocabulary (`off | low | medium | high`) and pick the **highest supported level at or below the requested level** (never escalating upward silently; if no level at or below the request exists, mark the route ineligible for that requirement).
  - Pass route-verified `reasoning`, compatible `thinkingLevelMap`, `input`, limits, and `compat` into Pi model registration and captured/resumed bindings (while unknown/private OpenAI-compatible routes use the verified plain request shape with unsupported reasoning parameters omitted on the wire).

---

## 2. User Control Plane & Safe Default Active Pool

### 2.1 Provider Priority, Active Pool & Legacy M4 Editor Compatibility

- **Single Source of Priority (`provider.reorder`)**:
  - Use SQLite `provider_accounts.sort_order` as the single saved priority and `provider_effective_accounts` array order as the applied priority (deriving an optional numeric `priority` ordinal on read).
  - Add `provider.reorder({ providerIds, savedRevision, operationId })` accepting an **exact permutation** of current saved `providerId`s, guarded by `savedRevision` CAS (idempotent duplicate `operationId` resolves before revision check), advancing `savedRevision` only, and emitting `provider.changed` with existing **`change: "saved"`** (preserving unmodified M4 Go readers!).
- **Safe Default Active Pool (Fixing the M4 Editor Discovery Trap)**:
  - Because the current M4 UI (`providers_ui.go:258-307`) copies up to 128 discovered `result.Models` into `editor.Models` on Save, `enabledModels ?? [defaultModel, ...models]` would silently authorize non-chat and Pro SKUs.
  - **Contract**:
    - Keep capped legacy `models` readback (`<= 128`) intact; add descriptive `discoveredModels?: DiscoveredModel[]` on `provider.test` (ranked with recommended chat models first before capping, carrying `id`, `canonicalId`, `isChat`, `toolCall`, `reasoning`, `tier`, `priceProtected`, `recommended`, `contextTokens`, `outputTokens`).
    - Represent `enabledModels` in Go as `*[]string` (`nil` = omitted, non-nil empty `&[]string{}` = **explicitly zero active models**, including default!).
    - When `enabledModels` is **omitted** (e.g., saved by an unchanged M4 editor): build the deterministic default active pool from **(a)** the explicitly configured `defaultModel` (if chat-eligible and NOT price-protected) PLUS **(b)** vetted `recommended` chat models already present in `account.models`.
    - **Mandatory Opt-In for Price-Protected SKUs**: Price-protected SKUs (`o1-pro`, `gpt-5.4-pro`, etc.) require explicit opt-in via `enabledModels` (or an explicit pin created for an enabled target) across **ALL** policies (`economy`, `balanced`, AND `quality`)—even if a solitary Pro SKU is the only model in a Provider! A probe, outage, empty pool, or `quality` selection never authorizes it automatically.
    - **Legacy Save Preservation**: On same-endpoint `provider.save` from an older Go client omitting RT fields (`enabledModels`, `fallbackOnly`, `modelOverrides`), merge those omitted RT fields from the prior saved account; invalidate endpoint-specific `modelOverrides` if the Provider endpoint/wireApi is repointed.
- **`fallbackOnly?: boolean`**: Default `false`. Excluded from primary auto-selection; activates automatically when primary routes for a preferred canonical model are cooled down, or when zero eligible ordinary routes exist.

### 2.2 Four-Slot Overrides (`routing.setOverrides`) & Fail-Closed Pin Semantics

- **Override Schema**:
  - `routing.setOverrides({ savedRevision, overrides, operationId })` advances `savedRevision` (promoted on `provider.apply`, emits `provider.changed` with **`change: "saved"`**).
  - Presence-aware patch semantics: omitted slot preserves current saved value; explicit `null` clears back to `auto`; `{ mode: 'model', canonicalModelId }` pins the exact canonical model across authorized healthy Providers in applied order; `{ mode: 'route', providerId, model, endpointFingerprint? }` pins the exact `(providerId, wireModelId)` (recording endpoint identity so deleting and recreating a recycled `providerId` pointing to a different URL never silently rebinds an existing exact pin!).
  - Validate new pins at creation against saved accounts; preserve existing pins visibly (even if their target is later disabled/deleted) as unavailable rather than silently erasing user intent.
- **Pin Resolution & Unavailability Readback**:
  - Add per-role `roleStatus` (`pinStatus`, `unavailableReason`, `retryAt?`) **beside** nullable `resolvedRoles[role]` on `routing.get` and `provider.list` (since a field inside `null` cannot explain why a slot is unavailable).

| Effective Pin Condition | New Admission & Readback |
| --- | --- |
| Exact `pinned_route` in transient cooldown | `route_unavailable` (`reason: "cooldown"`, bounded `retryAt`), 0 requests until eligible for half-open lease |
| Exact `pinned_route` deleted, disabled, excluded, endpoint-changed, or incompatible | `route_unavailable` (specific reason), 0 requests |
| Missing, revoked, or `401/403`-rejected credential | `credential_unavailable` (existing M4 non-dispatched receipt & preserved draft) |
| `pinned_model` with another authorized healthy supply | Selects that supply in applied order (ordinary first, then `fallbackOnly`), preserving exact `canonicalModelId` |
| `pinned_model` exhausted | Returns blocker (`route_unavailable` / `credential_unavailable`), keeping pin intact with zero fallback to unrelated models |

---

## 3. Calibrated Two-Stage Weighted Auto-Routing & Same-Model Failover

### 3.1 Independent Ranking Before Health Failover (Preserving Preferred Identity)

1. **Stage 1 — Authorized Pool Compilation (Independent of Transient Cooldown)**:
   - Collect all `(provider, wireModelId)` routes where Provider is `enabled`, credential is locally present and not `401/403` `auth_failed`, model is authorized in the active pool, and route satisfies all runtime hard gates (`isChat`, `requiresTools`, `minContextTokens`, future `requiresVision`, compatible thinking level).
   - Keep `ordinary` (`!fallbackOnly`) and `fallbackOnly` supplies tagged separately; use `fallbackOnly` as primary pool only if zero eligible `ordinary` routes exist.
2. **Stage 2 — Canonical Deduplication & Capability-Shortlist Weighted Scoring**:
   - Deduplicate by `canonicalModelId`, pairing each model with its preferred primary supply route strictly by **applied Provider order** (duplicate advertisements across 200 gateway variants never add votes).
   - **Anti-Inversion Capability Shortlist**:
     - Let `maxCap` be the highest `capabilityRank[role]` among eligible models in the active tier.
     - **`quality`**: shortlist models with `capabilityRank[role] >= maxCap - 10`.
     - **`balanced`**: shortlist models with `capabilityRank[role] >= maxCap - 20`.
     - **`economy`**: wider capability margin after hard gates (recompute shortlist on remaining lower models if all shortlisted models are cooled down).
   - **Integer-Numerator Weighted Score** (`scale 0..100` per factor; locale-independent tie-breaks by exact applied Provider ordinal, `canonicalModelId`, `wireModelId`):
     - `capabilityScore`: curated `rank[role]` (`0..100`); unranked known/private models receive one documented neutral estimate (`60`, never claiming absent price is free!).
     - `speedScore`: curated `speed` (`0..100`, neutral `65` if unranked).
     - `costScore`: explicit documented tier lookup over the preferred route's channel cost (`free/local: 100`, `low: 85`, `mid: 60`, `high: 30`, `flagship_pro: 5`, `unknown: 55`; public list price never infers subscription cost).
     - `providerPriorityScore`: strictly monotonic over exact applied pool position (`idx = 0..N-1`, e.g., `Math.round(100 * (N - idx) / N)` so Providers #7, #8, #9 never artificially tie!).
     - Policy integer weights (`sum = 100`):
       - `quality`: `65 * cap + 10 * speed + 5 * cost + 20 * prov`
       - `balanced`: `45 * cap + 15 * speed + 15 * cost + 25 * prov`
       - `economy`: `25 * cap + 20 * speed + 35 * cost + 20 * prov`
3. **Stage 3 — Same-Model-First Health Resolution**:
   - Walk the ranked canonical model list in order:
     1. Check the model's preferred `ordinary` supply route; if healthy, select it (`source: 'auto'`).
     2. If cooled down, try remaining healthy authorized routes for **that exact `canonicalModelId`** in applied Provider order—ordinary supplies first, then allowed `fallbackOnly` supplies (`source: 'auto'`, `failover: 'same_model_cooldown'`).
     3. Only when **all** authorized routes for that `canonicalModelId` are cooled down, advance to the next ranked `canonicalModelId` (`failover: 'model_cooldown'`).
   - **Affinity Precedence**:
     - Hot `Conversation` (`now - lastTurnAt < 5 min`) retains its exact captured `(providerId, wireModelId)` across Auto reorder/policy edits while healthy.
     - An **explicitly applied incompatible manual pin** takes effect on the next distinct turn (preserving transcript ownership and `sessionRef`; explicit cache tradeoff).
     - Existing M4 credential loss on a captured Conversation keeps returning `credential_unavailable` with draft intact; manual `activity.continue` stays on its captured binding; M4 utility escalation **never** retries transport/health failures onto another Provider.

---

## 4. Per-Route Health Classification, Half-Open Leases & Stream Compatibility

### 4.1 Scoped Health Classification (Generation-Serialized)

Track runtime health strictly in memory per worker lifetime (reset on worker restart; never written to backups or `contacts.json`). Serialize updates by request generation so a late response from an older request can never clear a newer failure or revive a revoked credential; inspect **both** HTTP headers and final stream settlement (a `200` stream can fail mid-body!):

| Observed Outcome | Health Scope & Action |
| --- | --- |
| HTTP `401 / 403` on execution | Preserve M4 source-wide dispatch block for that captured credential generation (unrelated Apply cannot clear it) |
| HTTP `429` (rate limit / quota) | Default cooldown on **exact route `(providerId, endpoint, wireApi, wireModelId)`** so sibling models on aggregators (Magpie/OpenRouter) remain healthy! Broaden to account scope only on verified account-wide quota evidence. Parse both delta-seconds and HTTP-date `Retry-After` clamped to `[15s, 15m]`, else backoff `60s → 180s → 300s`. |
| HTTP `5xx` | Per route on aggregators (endpoint scope only on verified endpoint-wide outage); enter cooldown (`30s → 120s → 300s`) after 2 consecutive attributable failures. |
| Connect refused / DNS / connect timeout | Endpoint scope `(providerId, endpoint, wireApi)`. User abort, app shutdown, and local validation NEVER poison health; post-transmission client deadline never implies non-dispatch. |
| HTTP `400 / 422` (bad prompt, context overflow, unsupported param) | Fail request only—NEVER poison Provider health! (Verified missing deployment marks only that route unavailable.) |
| Definitive successful model execution | Clear corresponding transient streak/cooldown scope only (keeping `401/403` and unrelated model failures independent). |

- **Half-Open Single-Lease Recovery**: When every eligible route is cooling, report temporary unavailability with earliest `retryAt`. At or after expiry, allow **one** newly user-authorized admission to acquire a half-open lease; concurrent callers pick another eligible route or receive the blocker. Reads, timers, and recovery NEVER emit background probes, NEVER retry failed operations, and NEVER bypass `Retry-After`.
- **`provider.test` Scope**: A successful `/models` GET proves discovery reachability only—it NEVER clears an execution model's `429` cooldown or an unrelated saved key's rejection. Only a matching successful model execution probe for the effective endpoint/source clears that transient route.

### 4.2 Unmodified M4 Go Event-Stream & Frontend Compatibility

- **Closed Event Switch (`internal/pi/routing.go:98`, `product_client.go:810-825`)**:
  - `ProductProviderChange.valid()` rejects unknown `change` values as protocol errors. Therefore:
    - `provider.reorder` and `routing.setOverrides` emit existing **`change: "saved"`** (advancing `savedRevision` only, `effectiveRevision` unchanged).
    - **NEVER emit `"health"` on the v1 `provider.changed` stream** and never fake config saves on cooldown. Expose per-route health + account summary (`healthy` / `degraded` / `cooldown` / `auth_failed`) on `provider.list` and `routing.get` reads without bumping revisions.
- **Explicit Frontend Alignment (`internal/app/providers_ui.go`)**:
  - All 8 M4 RPCs, nullable `resolvedRoles[role]`, and `credentialStatus` vocabulary remain 100% intact.
  - Because `providers_ui.go:92-102` treats nil `Dialogue` as not-ready and an empty pool as credentials-unavailable, frontend RT adoption will read the new sibling `roleStatus[role]` (`cooldown` + `retryAt`, `pin_unavailable`, `empty_pool`) and refresh locally at cooldown expiry without restart.

---

## 5. SQLite v11 Storage & Single-File Backup Portability

- **Strictly Monotonic Migration (`v11` after CP-3 `v10`)**:
  - Store `enabledModels`, `fallbackOnly`, and route `modelOverrides` inside existing `provider_accounts.config_json` / `provider_effective_accounts` JSON; store priority via `provider_accounts.sort_order` and effective array order; store optional saved/effective `routing_overrides` in SQLite `meta` and `ProductProfile`.
  - **Private `connection.json` v3 Purity**: Explicitly project only the existing v3 credential shape into `connection.json` (`product.ts:479-488`, `product-profile.ts:458-460`)—never write new routing metadata into `connection.json` or require a new private-file format for nonsecret edits!
  - **Legacy Mirror Digest Stability (`pending_snapshot_previous`)**: Omit absent RT fields on legacy reads (never eagerly writing default `priority`, `{}`, or empty overrides that mutate canonical pre-v11 JSON digests).
  - **Backup & Independent Restore**: Preserve saved/effective differences, provider order, `enabledModels`, `fallbackOnly`, historical captured routes, and dangling user pins across credential-free single-JSON restore with zero provider calls and blocked dispatch on missing keys; refuse `user_version > 11` fail-closed.

---

## 6. Mandatory Phased Acceptance Gates

| Milestone | Mandatory Evidence on Frozen Candidate |
| --- | --- |
| **RT-1** *(SQLite v11)* | Build packaged worker twice from pinned data and assert identical generated catalog digests (including `models.dev` MIT notice in manifest); launch offline with catalog endpoints blocked and assert 0 catalog fetches. Exercise both `openai-completions` and `openai-responses` asserting exact outbound `wireModelId`, lower-only effort encoding/omission, and route-specific limits across direct/relay GPT, versioned DeepSeek, overlapping GPT prefix variants, Ollama size tags, free/paid siblings, private duplicate `default`, and single-model collapse; tool-required run rejects known-incompatible/undeclared new route before transport. Drive a 200+ ID discovery (with embeddings & Pro SKUs) through an **unchanged M4 editor** Test → Save → Apply verifying cap, safe default active pool, explicit `enabledModels: []` vs omitted, price-protected opt-in in all policies, and old-client resave preserving RT fields. Freeze scoring fixtures covering capability-inversion prevention, only-Pro & fallback-only pools, duplicate providers, and reorder. Migrate a genuine v10 DB with separate saved/effective states + interrupted pending mirror, verify v12 refusal, and restore both new and legacy single-JSON backups with 0 provider calls. |
| **RT-2** | Exercise `pinned_route` and `pinned_model` across pending vs applied edits, deletion, disable, endpoint swap under recycled `providerId`, key rotation, `401/403` rejection, capability mismatch, cooldown, and recovery; verify `provider.list`, `routing.get` (`roleStatus` beside nil route), hot Auto stickiness vs explicitly applied incompatible pin, and credential-free restore. With controlled clocks and barriers: verify model-specific `429` on `A/X` leaves `A/Y` eligible; new `X` picks `B/X` (including `fallbackOnly`) before `C/Y`; exact pin sends 0 requests to `B`; endpoint outage cools endpoint scope; `400/422` never poisons health; `Retry-After` (delta + HTTP-date), `5xx` streak, network error, abort, and mid-stream error handled cleanly; simultaneous admissions at expiry allow 1 half-open lease; late success never clears newer failure; `/models` test never heals unrelated effective route; zero retries on original operations, answers, restarts, or M4 escalation. Drive an **unmodified M4 Go client** across reorders, override saves, cooldowns, and subsequent normal events verifying stream continuity without protocol errors. Co-sign native Settings/composer cooldown & expiry UX with frontend (`t1vb`). |
