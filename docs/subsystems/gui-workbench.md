# Subsystem: GUI Workbench

## Responsibilities and boundaries

- `internal/app` composes the Bot Team rail, current chat and composer, Activity drawer, status notices, Previous chats, Bot Profile, Settings and dialogs. Secondary click anywhere in a Bot Team row opens management for that Bot's stable ID. Settings holds global preferences.
- `internal/theme` supplies the Workbench and Settings palettes, shared controls and layout metrics. `locales` supplies English and Simplified Chinese text. User text, Bot names and model names retain their original content.
- MyGo owns the native window, main-thread rendering, focus, scrolling, IME and system integration. `internal/app/app.go` brings up the product runtime and routes app exit through the worker drain. Go owns view state and user intent. The TypeScript worker owns product state and execution admission, and Pi owns execution and transcripts. [Product protocol](../design/product-protocol.md) · [Desktop design](../design/desktop.md)

## Source of truth

| State | Owner and use |
| --- | --- |
| `appModel` and `contact` | `appModel` owns visible selection, Settings category and scroll, profile editor, pane geometry and transient focus targets. Each `contact` holds the Bot ID, the selected Conversation binding, local draft and Materials, displayed messages, and active send projection. The worker owns durable conversation, draft and execution revisions. [Model](../../internal/app/model.go) · [Product bridge](../../internal/app/pi_app.go) |
| `productActivityPanel` | Per-Bot drawer state holds the selected Activity, its form, read/result/file state, request client and revision, and local scroll/focus. `bot_status.go` derives header and rail attention from the Bot's Activity projections. The Phase 3 candidate's `activityNoticeState` owns the separate per-Bot notice cache for the compact attention strip, keyed by Activity and rebuilt from durable worker reads. [Drawer](../../internal/app/workspace.go) · [Bot status](../../internal/app/bot_status.go) |
| `sessionWorkspaceState` | Per-Bot Session inspection state holds paginated session summaries, active run detail, projected PTY/pipe output buffering, read epochs, and persisted uncertain-close intents. Opens from the chat header, links to read-only Source chat/activity, and reconciles live `session.*` events and close receipts without auto-replaying. [Sessions](../../internal/app/session_inspection.go) |
| `scheduledWorkState` | Per-Bot Scheduled work state holds timers, recurring schedules, local standing grants, precheck activation reviews, occurrence history, and persisted pending operation lookup IDs. Opens from Bot Profile or the Bot row menu, reconciles `watch.changed` events without auto-replaying mutations, and surfaces validated backup timer/schedule/dormant counts in Settings recovery. [Scheduled work](../../internal/app/schedules_ui.go) |
| `dataRecoveryState` | General → Workbench data owns picker, captured source and target, preview, outcome, disclosure and focus. `dataRecoveryCurrent` matches the request token, view generation, selected owner and target path. `dataRecoveryBlocked` checks admission before a write. The store and worker own actual backup and restore bytes. [Settings](../../internal/app/settings_page.go) · [Store](../../internal/store/store.go) |

## Primary execution paths

- `view` resolves the current theme and lays out the Workbench. `bindBotRowMenu` and `profileDetail` address the Bot recorded when management was opened. `chat` composes the message list, Activity drawer and composer. `latestReadUserMessageIndex` shows a localized `Read` / `已读` indicator below only the latest Bot-received user message while no visible Bot reply has appeared after it, clearing all prior read indicators once a reply appears. `sendProductConversation` saves and submits the exact draft revision, then product events and authoritative history reads reconcile the active reply. [Workbench](../../internal/app/view.go) · [Navigation](../../internal/app/navigation.go) · [Product bridge](../../internal/app/pi_app.go)
- `refreshProductActivities` and `openProductActivity` bind reads to the captured Bot and Activity. Source chat opens the recorded Conversation in the read-only transcript browser. Results and files retain their owning Activity and run. Returning through Back or escape restores the corresponding surface and its source control. [Activity and history](../../internal/app/workspace.go)
- `openSettingsTab` records `settingsReturnFocus` from the `settingsSource*` opener only on first entry. Tab changes keep the original return target. `closeSettings` restores the matching rail, profile, connection, configuration, composer or auxiliary control. For a focused Markdown link, `captureLinkSettingsSource` records its document and link identity. `finishMarkdownLinkReturn` restores the exact link if it still exists, with one fallback to the active surface when it has disappeared. [Settings navigation](../../internal/app/settings_page.go) · [Link focus](../../internal/app/view.go)
- `chooseDataRecovery` selects and inspects the file, then the active preview presents the captured copy and its scope before `confirmDataRecovery` rechecks the target. The original bytes are preserved before installing a compatible recovery copy. The UI distinguishes failed install, verified install and unconfirmed readback. [Recovery flow](../../internal/app/settings_page.go)

Closing the native window hides the existing Workbench and keeps the owned worker, armed watches and sessions running. Dock activation shows the same window with its current Bot, drafts, Materials, quote and scroll positions. The Dock also opens Scheduled work and Sessions for the selected Bot. Explicit Quit shows the shutdown status, fences admission and drains the worker before closing the window. On OS logout or shutdown, the app saves its UI state and synchronously reaps the worker within a bounded shutdown attempt. [Native lifecycle](../../internal/app/native_lifecycle.go)

## Layout, theme and focus invariants

- At `1440 × 900`, the full Bot Team rail, chat and Bot Profile can coexist. Full rail layout starts at `1180 DIP`. From `900` through `1179 DIP`, the open profile uses the compact avatar rail. Below `900 DIP`, the profile becomes an in-app page with Back. At the default `1040 × 720`, content keeps its centered reading column. At `960 × 640` with the profile open, the editable composer must retain at least `440 DIP` of net width. At the `720 × 640` minimum, the composer remains usable with Activity detail open. [Breakpoints and geometry](../design/desktop.md#adjustable-layout-values) · [Native matrix](../design/ui-checklist.md#native-v1-review)
- Settings uses a `248 DIP` sidebar, an `820 DIP` centered content column and a full-width scroll viewport. Below `640 DIP`, the sidebar uses wrapping top navigation. Back and category rows use intrinsic square icon slots and the shared control radius. The General and Advanced preference subtitles wrap with the available content width. Recovery confirmation occupies its own card below the backup controls. Preserve source focus and active scroll when changing appearance or language.
- `resolveAppearanceTheme` resolves system, explicit or imported preferences to a `theme.WorkbenchTheme` containing the MyGo `ui.Theme`. `theme.SettingsTheme` derives Settings surfaces and checks contrast. `theme.TransitionControl` applies a palette change on the first frame and keeps hover and press transitions within one palette. `setLanguageMode` applies MyGo's locale and localized UI uses the current render locale. [Appearance](../../internal/app/appearance.go) · [Settings](../../internal/app/settings_page.go) · [DS2 controls](../../internal/theme/ds2.go) · [Palette](../../internal/theme/theme.go)

## Failure modes and compatibility

- Keep each draft, Material, execution, question and result bound to its recorded Bot and target. A lost or stale reply triggers readback of its recorded identity. Unknown delivery stays visible and is never replayed on reopen. Focus recovery restores a control on the current surface when the original target no longer exists.
- Read-only history and diagnostics remain accessible when their data is available. A failed backup or restore keeps recoverable data and reports the actual outcome. Opening a restored profile does not dispatch pending work. [Profile admission](../design/product-protocol.md#current-profile-admission)
- The visual hierarchy uses neutral surfaces, semantic status ink, hairline input borders and one filled primary action per surface. Keep preference and diagnostic actions intrinsic. Use disclosures to keep secondary information out of the main action path. [Code and UI standards](../code-standards.md)

## Verification commands

```sh
CGO_ENABLED=0 python3 scripts/gui-go.py vet ./internal/app ./internal/theme
CGO_ENABLED=0 NAGI_TEST_ONLINE=0 python3 scripts/gui-go.py test -count=1 ./internal/app -run '^(TestSettings|TestAppearance|TestC8|TestVisualGuard|TestPublishedCatalogContract)'
CGO_ENABLED=0 NAGI_TEST_ONLINE=0 python3 scripts/gui-go.py test -count=1 ./internal/theme -run '^TestDS2'
CGO_ENABLED=0 NAGI_TEST_ONLINE=0 NAGI_PREVIEW_DIR=/absolute/external/output python3 scripts/gui-go.py test -count=1 ./internal/app -run '^TestPreviewSettingsPage$'
```

Use `TestPreview*` scenes for the affected composition, and inspect the original Light and Dark frames. `TestSettings*` exercises navigation, focus, drafts and preference behavior. `TestC8*` covers data recovery. `TestVisualGuard*` checks shared alignment and geometry, controls, brand assets and locale parity. Record native actions and an independent verdict against a frozen candidate. [UI review workflow](../design/ui-review.md)
