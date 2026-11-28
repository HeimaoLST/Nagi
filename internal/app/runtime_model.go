package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

const (
	runtimeStateStarting = "starting"
	runtimeStateReady    = "ready"
	runtimeStateFailed   = "failed"
	runtimeStateExited   = "exited"
	runtimeStateDraining = "draining"

	startupStageLocalStore = "Local profile store"
)

func newProductRuntimeApp() (*appModel, error) {
	storePath, err := filepath.Abs(guiStorePath())
	if err != nil {
		return nil, err
	}
	_, err = os.Stat(storePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	a := &appModel{
		storePath: storePath, connectionPath: connectionConfigPath(storePath), workDir: guiWorkDir(),
		runtimeStatus: runtimeStateStarting,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.openProductProfile(ctx, errors.Is(err, os.ErrNotExist), 1); err != nil {
		return nil, err
	}
	if !a.hasAnySavedConnection() && !a.hasEffectiveProviderPool() {
		a.openSettingsTab(settingsTabConnection, settingsSourceConnection)
	}
	a.reloadAppearanceThemes()
	return a, nil
}

func (a *appModel) applyProductPreferences(preferences *pi.ProductPreferences) {
	if preferences == nil {
		return
	}
	for i, ct := range a.contacts {
		if ct.ID == preferences.SelectedBotID {
			a.selected = i
			break
		}
	}
	var layout storedLayout
	if len(preferences.Layout) != 0 && json.Unmarshal(preferences.Layout, &layout) == nil {
		a.leftPaneWidth = clampPaneWidth(layout.LeftWidth, defaultLeftPaneWidth, minLeftPaneWidth, maxLeftPaneWidth)
		a.rightPaneWidth = clampPaneWidth(layout.RightWidth, defaultRightPaneWidth, minRightPaneWidth, maxRightPaneWidth)
	}
	if len(preferences.ThemeIDs) != 0 {
		_ = json.Unmarshal(preferences.ThemeIDs, &a.appearanceThemeIDs)
	}
	a.appearanceMode = normalizeAppearanceMode(preferences.Appearance)
	a.hideBuiltinArchitect = preferences.HideBuiltinArchitect
}

func newUnavailableApp(err error) *appModel {
	storePath := guiStorePath()
	if absolute, err := filepath.Abs(storePath); err == nil {
		storePath = absolute
	}
	workDir := guiWorkDir()
	startup := readConnectionStartup(storePath)
	a := &appModel{
		runtimeStatus:   runtimeStateFailed,
		runtimeStage:    startupStageLocalStore,
		runtimeErr:      err.Error(),
		runtimeHintKind: startupHintExistingStore,
		runtimeHintArgs: []string{storePath},
		storePath:       storePath,
		connectionPath:  startup.Path,
		connectionSaved: startup.Config,
		workDir:         workDir,
		workspaceOpen:   false,
		contacts: []*contact{{
			ID:        newContactID(),
			Name:      "Xuhu",
			Role:      "Assistant",
			Status:    statusAttention,
			WorkLabel: "Preparing a reply",
		}},
	}
	a.reloadAppearanceThemes()
	a.contacts[0].Unavailable = err.Error()
	return a
}

func guiStorePath() string {
	if value := os.Getenv("NAGI_GUI_STORE"); value != "" {
		return value
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "Xuhu", "contacts.json")
}

func guiWorkDir() string {
	if value := os.Getenv("NAGI_GUI_WORKDIR"); value != "" {
		return value
	}
	if wd, err := os.Getwd(); err == nil && wd != "" && wd != "/" {
		return wd
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "."
}

const (
	startupHintFixConnection   = "fix-connection"
	startupHintEnterConnection = "enter-connection"
	startupHintRestartReload   = "restart-reload"
	startupHintExistingStore   = "existing-store"
)

const (
	runtimeNoticeSavedNeedsRepair      = "saved-needs-repair"
	runtimeNoticeConnectionRequired    = "connection-required"
	runtimeNoticeStart                 = "start"
	runtimeNoticeConnectionLost        = "connection-lost"
	runtimeNoticeStoreSave             = "store-save"
	runtimeNoticeRuntimeShutdown       = "runtime-shutdown"
	runtimeNoticeCredentialUnavailable = "provider-credential-unavailable"
	runtimeNoticeRouteUnavailable      = "provider-route-unavailable"
)

func (a *appModel) hasRuntimeError() bool {
	return a.runtimeErrKind != "" || a.runtimeErr != ""
}

func (a *appModel) runtimeStatusText(msgs locales.Messages) string {
	switch a.runtimeStatus {
	case runtimeStateStarting:
		return msgs.RuntimeStatusStarting()
	case runtimeStateReady:
		if a.connectionManaged {
			return msgs.RuntimeStatusConnectionOnly(msgs.RuntimeStatusReady())
		}
		return msgs.RuntimeStatusReady()
	case runtimeStateFailed:
		return msgs.RuntimeStatusFailed()
	case runtimeStateExited:
		return msgs.RuntimeStatusExited()
	case runtimeStateDraining:
		return msgs.RuntimeStatusDraining()
	default:
		return msgs.RuntimeStatusNotLoaded()
	}
}

func (a *appModel) runtimeStageDisplay(msgs locales.Messages) string {
	switch a.runtimeStage {
	case startupStageLocalStore:
		return msgs.RuntimeStageLocalStore()
	case "Connection configuration":
		return msgs.RuntimeStageConnectionConfiguration()
	case "Runtime start":
		return msgs.RuntimeStageRuntimeStart()
	case "":
		return msgs.RuntimeStageRuntimeLabel()
	default:
		return a.runtimeStage
	}
}

func runtimeErrorText(msgs locales.Messages, kind, detail string) string {
	switch kind {
	case runtimeNoticeSavedNeedsRepair:
		return msgs.RuntimeErrorSavedNeedsRepair()
	case runtimeNoticeConnectionRequired:
		return msgs.RuntimeErrorConnectionRequired()
	case runtimeNoticeStart:
		return msgs.RuntimeErrorStart(detail)
	case runtimeNoticeConnectionLost:
		if detail != "" {
			return msgs.RuntimeErrorConnectionLostDetail(detail)
		}
		return msgs.RuntimeErrorConnectionLost()
	case runtimeNoticeStoreSave:
		return msgs.RuntimeErrorStoreSave(detail)
	case runtimeNoticeRuntimeShutdown:
		return msgs.RuntimeErrorRuntimeShutdown(detail)
	default:
		return detail
	}
}

func (a *appModel) runtimeErrorDisplay(msgs locales.Messages) string {
	if a.runtimeErrKind == "" {
		return a.runtimeErr
	}
	return runtimeErrorText(msgs, a.runtimeErrKind, a.runtimeErr)
}

func runtimeHintText(msgs locales.Messages, kind string, args []string) string {
	switch kind {
	case startupHintFixConnection:
		return msgs.StartupHintFixConnection()
	case startupHintEnterConnection:
		return msgs.StartupHintEnterConnection()
	case startupHintRestartReload:
		return msgs.StartupHintRestartReload()
	case startupHintExistingStore:
		return msgs.StartupHintExistingStore(noticeArg(args, 0))
	default:
		return ""
	}
}

func (a *appModel) runtimeHintDisplay(msgs locales.Messages) string {
	if text := runtimeHintText(msgs, a.runtimeHintKind, a.runtimeHintArgs); text != "" {
		return text
	}
	return a.runtimeHint
}

func (a *appModel) clearBuiltinToolCatalog() {
	a.runtimeBuiltinTools = nil
	a.runtimeBuiltinDefaults = nil
	a.runtimeBuiltinToolsLoaded = false
	a.runtimeBuiltinToolsErr = ""
}

func (a *appModel) beginShutdown() {
	if a.shutdownStarted {
		return
	}
	a.shutdownStarted = true
	a.runtimeStatus = runtimeStateDraining
	a.refreshDockMenu()
	a.resetComposerOverlays()
	a.headerMenuOpen = false
	a.persist()
	a.stopProviderTimers()
	a.finishShutdown()
	a.invalidateNativeWindow()
}

func (a *appModel) stopProviderTimers() {
	if a.providers.cancelTest != nil {
		a.providers.cancelTest()
	}
	if timer := a.providers.healthTimer; timer != nil {
		timer.Stop()
		a.providers.healthTimer = nil
	}
}

func (a *appModel) setUIHook(hook func(func())) {
	a.directUIMu.Lock()
	defer a.directUIMu.Unlock()
	a.uiHookMu.Lock()
	a.uiHook = hook
	a.uiHookMu.Unlock()
}

func (a *appModel) getUIHook() func(func()) {
	a.uiHookMu.Lock()
	defer a.uiHookMu.Unlock()
	return a.uiHook
}

func (a *appModel) updateUI(fn func()) {
	if hook := a.getUIHook(); hook != nil {
		hook(fn)
		return
	}
	a.directUIMu.Lock()
	hook, win := a.getUIHook(), a.win
	if hook == nil && win == nil {
		defer a.directUIMu.Unlock()
		fn()
		return
	}
	a.directUIMu.Unlock()
	if hook != nil {
		hook(fn)
	} else {
		win.Update(fn)
	}
}

func (a *appModel) maxActiveTasks() int {
	if a.runtimeMaxActiveTasks > 0 {
		return a.runtimeMaxActiveTasks
	}
	return 1
}

func sessionBusy(send sendState, activeTaskID string) bool {
	return activeTaskID != "" || send != sendIdle
}

func (a *appModel) activeTaskCount() int {
	count := 0
	for _, ct := range a.contacts {
		if sessionBusy(ct.SendState, ct.ActiveTaskID) {
			count++
		}
		for _, activity := range ct.Activities {
			if activity.WorkSessionID != "" || sessionBusy(activity.Work.SendState, activity.Work.ActiveTaskID) {
				if sessionBusy(activity.Work.SendState, activity.Work.ActiveTaskID) {
					count++
				}
			}
		}
	}
	return count
}

func (a *appModel) canReloadPiConversation(ct *contact) bool {
	return a.productProfileReady() && ct != nil && a.contactByID(ct.ID) == ct && ct.SessionID != ""
}

func (a *appModel) reloadContactByID(contactID string) {
	if host := a.productHost(); host != nil {
		a.reloadProductConversation(host, a.contactByID(contactID))
	}
}

type historyProjection struct {
	messages        []*message
	entries         []historyEntry
	detail          string
	transcriptError bool
}

func formatMillis(ms int64) string {
	if ms <= 0 {
		return ""
	}
	value := time.UnixMilli(ms)
	now := time.Now()
	if value.Year() == now.Year() && value.YearDay() == now.YearDay() {
		return value.Format("15:04")
	}
	return value.Format("Jan 2 15:04")
}

func (a *appModel) liveText(ct *contact, taskID, itemID string) *message {
	key := "live:" + taskID + ":text:" + itemID
	for _, m := range ct.Messages {
		if m.Key == key {
			return m
		}
	}
	stamp := time.Now()
	m := &message{Key: key, Kind: msgContact, TaskID: taskID, ItemID: itemID, Time: stamp.Format("15:04"), Timestamp: stamp, liveReveal: true}
	ct.Messages = append(ct.Messages, m)
	if ct != a.selectedContact() {
		ct.Unread++
	}
	return m
}

type connectionViewState int

const (
	connectionViewHealthy connectionViewState = iota
	connectionViewConnecting
	connectionViewSaved
	connectionViewSetup
	connectionViewNotConnected
)

func (a *appModel) runtimeAvailableForView() bool {
	return a.demo || a.productProfileReady() && (a.hasUsableProviderRoute() || a.connectionEffective != nil)
}

func (a *appModel) connectionViewState() connectionViewState {
	if a.demo {
		return connectionViewHealthy
	}
	if a.productHost() != nil {
		switch {
		case !a.productProfileReady():
			return connectionViewNotConnected
		case a.productProvidersPending():
			return connectionViewSaved
		case a.hasUsableProviderRoute() || a.connectionEffective != nil:
			return connectionViewHealthy
		case a.hasEffectiveProviderPool():
			return connectionViewNotConnected
		default:
			return connectionViewSetup
		}
	}
	if a.pi != nil && a.runtimeStatus == runtimeStateReady {
		return connectionViewHealthy
	}
	if a.pi != nil && a.runtimeStatus == runtimeStateStarting {
		return connectionViewConnecting
	}
	if a.connectionSaved != nil && (a.connectionEffective == nil || *a.connectionSaved != *a.connectionEffective) {
		return connectionViewSaved
	}
	if a.runtimeStatus != runtimeStateExited && a.connectionSaved == nil && (a.runtimeErrKind == runtimeNoticeConnectionRequired || a.runtimeHintKind == startupHintEnterConnection) {
		return connectionViewSetup
	}
	return connectionViewNotConnected
}

func (a *appModel) connectionConfigurationBlocked() bool {
	switch a.connectionViewState() {
	case connectionViewSaved, connectionViewSetup, connectionViewNotConnected:
		return true
	default:
		return false
	}
}

const (
	blockShutdown         = "shutdown"
	blockArchivedSend     = "archived-send"
	blockRuntimeNotReady  = "runtime-not-ready"
	blockHistoryLoading   = "history-loading"
	blockBusy             = "busy"
	blockCapacity         = "capacity"
	blockAnotherContact   = "other-contact-working"
	blockArchivedActivity = "archived-issue"
	blockActivityActive   = "issue-active"
	blockWorkCapacity     = "work-capacity"
)

func sendBlockRaw(reason string) string {
	switch reason {
	case blockShutdown:
		return "The app is shutting down."
	case blockArchivedSend:
		return "This Bot is archived. Restore it before sending a message."
	case blockRuntimeNotReady:
		return "Runtime is not ready."
	case blockHistoryLoading:
		return "This Bot is still loading its history."
	case blockBusy:
		return "This Bot is busy."
	case blockCapacity:
		return "Runtime task capacity is full; retry after a task finishes."
	case blockAnotherContact:
		return "Another Bot is working. Wait for its task to finish before sending here."
	case blockArchivedActivity:
		return "This Bot is archived. Restore it before starting activity work."
	case blockActivityActive:
		return "This issue already has an active execution."
	case blockWorkCapacity:
		return "Work capacity is full; one runtime slot is reserved for conversation."
	default:
		return reason
	}
}

func sendBlockText(msgs locales.Messages, reason string) string {
	switch reason {
	case blockShutdown:
		return msgs.CanSendShutdown()
	case blockArchivedSend:
		return msgs.CanSendArchived()
	case blockRuntimeNotReady:
		return msgs.CanSendRuntimeNotReady()
	case blockHistoryLoading:
		return msgs.CanSendHistoryLoading()
	case blockBusy:
		return msgs.CanSendBusy()
	case blockCapacity:
		return msgs.CanSendCapacity()
	case blockAnotherContact:
		return msgs.CanSendAnotherContact()
	case blockArchivedActivity:
		return msgs.CanStartArchived()
	case blockActivityActive:
		return msgs.CanStartIssueActive()
	case blockWorkCapacity:
		return msgs.CanStartWorkCapacity()
	default:
		return reason
	}
}

func (a *appModel) canSend(ct *contact) string {
	if a.shutdownStarted {
		return blockShutdown
	}
	if ct.Archived {
		return blockArchivedSend
	}
	if a.demo {
		return ""
	}
	mayRequestAdmission := a.productProfileReady() && (a.runtimeErrKind == runtimeNoticeRouteUnavailable ||
		a.runtimeErrKind == runtimeNoticeCredentialUnavailable && ct.LastTaskID != "")
	if !a.runtimeAvailableForView() && !mayRequestAdmission {
		return blockRuntimeNotReady
	}
	if ct.SessionID != "" && !ct.Loaded {
		return blockHistoryLoading
	}
	if sessionBusy(ct.SendState, ct.ActiveTaskID) {
		return blockBusy
	}
	if a.productHost() == nil && a.activeTaskCount() >= a.maxActiveTasks() {
		if a.maxActiveTasks() <= 1 {
			return blockAnotherContact
		}
		return blockCapacity
	}
	return ""
}

func (a *appModel) submissionAsyncStarted() {
	a.submissionInFlight++
}

func (a *appModel) submissionAsyncFinished() {
	if a.submissionInFlight > 0 {
		a.submissionInFlight--
	}
	if a.shutdownStarted && !a.quitReady {
		a.finishShutdown()
	}
}

func (a *appModel) cancelSelected() {
	ct := a.selectedContact()
	if ct != nil {
		a.cancelRuntimeTask(ct, ct.ActiveTaskID)
	}
}

func (a *appModel) cancelRuntimeTask(ct *contact, taskID string) {
	if host := a.productHost(); host != nil {
		a.stopProductConversation(host, ct, taskID)
	}
}

func (a *appModel) reloadSelected() {
	ct := a.selectedContact()
	if !a.canReloadPiConversation(ct) {
		if ct != nil {
			ct.setSendError("Conversation history is unavailable for this Bot or connection.", "", "")
		}
		return
	}
	a.reloadContactByID(ct.ID)
	a.reconcilePendingConversationSend(a.productHost(), ct, true)
}

func (a *appModel) addRuntimeContact() {
	workspace := ""
	if a.demo || a.connectionEffective != nil {
		workspace = a.workDir
	}
	profile := pi.ProductBotProfile{Name: locales.In(a.uiLocale).NewContactName(len(a.contacts) + 1), Role: "Assistant", Workspace: workspace}
	if err := a.createContact(profile, newContactID()); err != nil {
		a.selectedContact().setSendError(a.connectionSafeMessage(err), "", "")
	}
}

func (a *appModel) createContact(profile pi.ProductBotProfile, operationID string) error {
	if host := a.productHost(); host != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bot, err := host.product.CreateBot(ctx, pi.ProductBotCreateParams{OperationID: operationID, Profile: profile})
		if err != nil {
			return err
		}
		ct := a.applyProductBot(host, bot, true)
		a.selectContact(slices.Index(a.contacts, ct))
	} else {
		ct := productContact(pi.ProductBot{BotID: newContactID(), Profile: profile})
		ct.WorkLabel = "Preparing a reply"
		ct.Context.OnChange = a.persist
		contacts, selected, store := a.contacts, a.selected, a.store
		a.contacts = append(a.contacts, ct)
		a.selected = len(a.contacts) - 1
		if err := a.persist(); err != nil {
			a.contacts, a.selected, a.store = contacts, selected, store
			return err
		}
		a.contextFileGeneration++
		a.priorChats = nil
		a.aux = ""
		a.resetComposerOverlays()
	}
	a.contactSearch, a.focusComposer = "", true
	return nil
}

func (a *appModel) normalizeRestoredSupplementBatches() {
	changed := false
	for _, ct := range a.contacts {
		for _, activity := range ct.Activities {
			ledger := activity.Supplements
			if ledger == nil {
				continue
			}
			for _, batch := range ledger.Batches {
				if batch == nil {
					continue
				}
				switch batch.State {
				case supplementBatchDispatching, supplementBatchUnknown:
					if ledger.AutoContinue {
						ledger.AutoContinue = false
						changed = true
					}
					if batch.ExecutionID == "" && batch.State == supplementBatchDispatching {
						batch.State = supplementBatchUnknown
						for _, snapshot := range batch.Snapshot {
							if item := ledger.ItemByID(snapshot.ID); item != nil && item.State == supplementStateDispatching {
								item.State = supplementStateUnknown
							}
						}
						changed = true
					}
				}
			}
		}
	}
	if changed {
		if err := a.persist(); err != nil {
			a.setActivityActionNotice("Could not save restored supplement integrity state: "+err.Error(), "supplement-restore-failed", err.Error())
		}
	}
}

func (a *appModel) effectiveContactWorkspace(ct *contact) string {
	if ct != nil && ct.Workspace != "" {
		return ct.Workspace
	}
	return a.workDir
}

func (a *appModel) selectedID() string {
	if a.selected < 0 || a.selected >= len(a.contacts) {
		return ""
	}
	return a.contacts[a.selected].ID
}

func (a *appModel) ensureContactStore() error {
	if a.store != nil {
		return nil
	}
	if !a.storeInitAllowed || strings.TrimSpace(a.storePath) == "" {
		return errors.New("the local profile store is unavailable")
	}
	if len(a.contacts) == 0 {
		return errors.New("there is no current Bot to save")
	}
	if _, err := os.Stat(a.storePath); err == nil {
		return fmt.Errorf("local profile store %s exists but has not been loaded", a.storePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	a.store = &contactStore{Path: a.storePath, State: storedState{Version: contactStoreVersion}}
	return nil
}

func (a *appModel) persist() error {
	if a.demo && a.store == nil {
		return nil
	}
	var err error
	if host := a.productHost(); host != nil {
		err = a.persistProductUI(host)
	} else if err = a.ensureContactStore(); err == nil {
		a.store.SetIssueToolState(a.activityToolLedger, a.activityToolScopes)
		var themeIDs *appearanceThemeIDs
		if a.appearanceThemeIDs != (appearanceThemeIDs{}) {
			ids := a.appearanceThemeIDs
			themeIDs = &ids
		}
		layout := &storedLayout{
			LeftWidth:     a.railWidth(),
			RightWidth:    a.workWidth(),
			WorkspaceOpen: a.workspaceOpen,
		}
		err = saveContactsWithPreferences(a.store, a.contacts, a.selectedID(), normalizeAppearanceMode(a.appearanceMode), themeIDs, layout)
	}
	if err != nil {
		if a.localSaveErr == "" {
			a.localSavePrevErr = a.runtimeErr
			a.localSavePrevKind = a.runtimeErrKind
		}
		a.localSaveErr = err.Error()
		a.runtimeErrKind = runtimeNoticeStoreSave
		a.runtimeErr = err.Error()
		return err
	}
	if a.localSaveErr != "" {
		if a.runtimeErr == a.localSaveErr && a.runtimeErrKind == runtimeNoticeStoreSave {
			a.runtimeErr = a.localSavePrevErr
			a.runtimeErrKind = a.localSavePrevKind
		}
		a.localSaveErr = ""
		a.localSavePrevErr = ""
		a.localSavePrevKind = ""
	}
	if a.appearanceError != "" {
		a.appearanceError = ""
		a.appearanceStatus = appearanceStatusSaved
	}
	return nil
}

func (a *appModel) draftChanged(ct *contact) {
	if a.productHost() != nil {
		ct.draftRev++
		if err := a.saveProductDraft(ct); err != nil {
			ct.setSendError(a.connectionSafeMessage(err), "", "")
		}
		return
	}
	ct.draftRev++
	a.persist()
}

func noticeArg(args []string, index int) string {
	if index < 0 || index >= len(args) {
		return ""
	}
	return args[index]
}
