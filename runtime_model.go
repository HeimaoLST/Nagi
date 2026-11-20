package main

// Runtime-backed app wiring: configuration, startup/restore, the real send
// path, event application and history rendering. All UI state changes go
// through win.Update; I/O and JSON decoding stay off the UI thread.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

	startupStageLocalStore = "Local contact store"
)

func newRuntimeApp() (*appModel, error) {
	storePath := guiStorePath()
	defaultWorkDir := guiWorkDir()
	startup := readConnectionStartup(storePath)
	workDir := defaultWorkDir
	formWorkDir := defaultWorkDir
	if startup.Config != nil {
		workDir = startup.Config.WorkingDirectory
		formWorkDir = workDir
	} else if startup.FormConfig != nil && startup.FormConfig.WorkingDirectory != "" {
		formWorkDir = startup.FormConfig.WorkingDirectory
	}

	_, statErr := os.Stat(storePath)
	storeExists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	var store *contactStore
	var err error
	switch {
	case storeExists:
		store, err = openContactStore(storePath, workDir)
	case startup.Config != nil:
		store, err = openContactStore(storePath, startup.Config.WorkingDirectory)
	}
	if err != nil {
		return nil, err
	}

	a := &appModel{
		store:               store,
		storePath:           storePath,
		workDir:             workDir,
		runtimeStatus:       runtimeStateStarting,
		workspaceOpen:       false,
		scrollTo:            -1,
		connectionPath:      startup.Path,
		connectionSaved:     startup.Config,
		connectionEffective: startup.Config,
		connectionManaged:   startup.Config != nil,
	}
	if store != nil {
		applyStoreSelection(a, store)
		a.loadStoredTaskContexts(store)
	} else {
		// Local edits must not confirm the launch-folder fallback as an override.
		a.contacts = []*contact{placeholderContact("")}
		a.selected = 0
	}
	a.bindTaskContexts()
	a.normalizeRestoredSupplementBatches()
	if a.issueToolLedger == nil {
		a.issueToolLedger = &IssueToolOperationLedger{}
	}
	if a.issueToolScopes == nil {
		a.issueToolScopes = map[string]IssueToolScope{}
	}
	if a.issueToolWaiters == nil {
		a.issueToolWaiters = map[string]chan IssueToolReceipt{}
	}
	if a.issueToolShutdown == nil {
		a.issueToolShutdown = make(chan struct{})
	}
	formConfig := startup.Config
	if formConfig == nil {
		formConfig = startup.FormConfig
	}
	a.connectionForm = newConnectionSettings(formConfig, formWorkDir)
	if startup.Config == nil {
		a.connectionForm.HasSaved = false
		switch {
		case startup.LoadErr != nil:
			a.connectionForm.Error = connErrorSavedNeedsRepair
			a.connectionForm.ErrorDetail = ""
			a.runtimeStatus = runtimeStateFailed
			a.runtimeStage = "Connection configuration"
			a.runtimeErrKind = runtimeNoticeSavedNeedsRepair
			a.runtimeErr = ""
			a.runtimeHintKind = startupHintFixConnection
			a.runtimeHintArgs = nil
		default:
			a.runtimeStatus = runtimeStateFailed
			a.runtimeStage = "Connection configuration"
			a.runtimeErrKind = runtimeNoticeConnectionRequired
			a.runtimeErr = ""
			a.runtimeHintKind = startupHintEnterConnection
			a.runtimeHintArgs = nil
		}
	}
	if startup.Config == nil {
		a.openSettingsTab(settingsTabConnection, settingsSourceConnection)
	}
	// Local saves retain the placeholder's identity on a first launch without
	// a connection. Read/stat failures never enable missing-store initialization.
	if a.store == nil && a.storePath != "" {
		a.storeInitAllowed = true
	}
	a.reloadAppearanceThemes()
	return a, nil
}

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
		runtimeStatus: runtimeStateStarting, scrollTo: -1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.openProductProfile(ctx, errors.Is(err, os.ErrNotExist), 1); err != nil {
		return nil, err
	}
	if a.connectionSaved == nil {
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
}

// newUnavailableApp keeps the window usable when the local contact store cannot
// be read or created. It retains the actual paths, labels the local store stage,
// keeps store=nil, and never silently creates a replacement store.
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
		scrollTo:        -1,
		contacts: []*contact{{
			ID:        newContactID(),
			Name:      "Nagi",
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
	return filepath.Join(dir, "Nagi", "contacts.json")
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
	runtimeNoticeSavedNeedsRepair   = "saved-needs-repair"
	runtimeNoticeConnectionRequired = "connection-required"
	runtimeNoticeStart              = "start"
	runtimeNoticeConnectionLost     = "connection-lost"
	runtimeNoticeStoreSave          = "store-save"
	runtimeNoticeRuntimeShutdown    = "runtime-shutdown"
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

var diagnosticSecret = regexp.MustCompile(`(?i)\b(api[_-]?key|token)\s*[:=]\s*[^\s,;]+|\bauthorization\s*:\s*(?:bearer\s+)?[^\s,;]+|\bbearer\s+[^\s,;]+`)
var diagnosticJSONSecret = regexp.MustCompile(`(?i)"(?:api[_-]?key|token|secret|password|authorization)"\s*:\s*"[^"]*"`)
var diagnosticSkKey = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`)
var diagnosticURLCred = regexp.MustCompile(`://[^/\s@]+@`)

func safeDiagnostic(text string) string {
	text = strings.TrimSpace(text)
	text = diagnosticSecret.ReplaceAllString(text, "[redacted]")
	text = diagnosticJSONSecret.ReplaceAllString(text, "[redacted]")
	text = diagnosticSkKey.ReplaceAllString(text, "[redacted]")
	text = diagnosticURLCred.ReplaceAllString(text, "://[redacted]@")
	return truncate(text, 500)
}

func (a *appModel) clearBuiltinToolCatalog() {
	a.runtimeBuiltinTools = nil
	a.runtimeBuiltinDefaults = nil
	a.runtimeBuiltinToolsLoaded = false
	a.runtimeBuiltinToolsErr = ""
}

// beginShutdown is the single quit path for the window close button and
// Cmd+Q. It prevents the default close, drains the runtime, then asks the app
// to quit. The UI thread is never blocked on the process wait.
func (a *appModel) beginShutdown() {
	if a.shutdownStarted {
		return
	}
	a.shutdownStarted = true
	if a.issueToolShutdown != nil {
		close(a.issueToolShutdown)
	}
	a.persist()
	if a.connectionTestCancel != nil {
		a.connectionTestCancel()
		a.connectionTestCancel = nil
	}
	a.finishShutdown()
}

func (a *appModel) setUIHook(hook func(func())) {
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
	if a.win != nil {
		a.win.Update(fn)
		return
	}
	fn()
}

func (a *appModel) capacityKnown() bool {
	return a.demo || a.runtimeCapabilitiesLoaded
}

func (a *appModel) maxActiveTasks() int {
	if a.runtimeMaxActiveTasks > 0 {
		return a.runtimeMaxActiveTasks
	}
	return 1
}

func (a *appModel) workTaskLimit() int {
	if max := a.maxActiveTasks(); max > 1 {
		return max - 1
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
		for _, issue := range ct.Issues {
			if issue.WorkSessionID != "" || sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) {
				if sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) {
					count++
				}
			}
		}
	}
	return count
}

func (a *appModel) activeWorkTaskCount() int {
	count := 0
	for _, ct := range a.contacts {
		for _, issue := range ct.Issues {
			if sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) {
				count++
			}
		}
	}
	return count
}

func (a *appModel) canReloadPiConversation(ct *contact) bool {
	if a.productHost() != nil {
		return a.productProfileReady() && ct != nil && a.contactByID(ct.ID) == ct && ct.SessionID != ""
	}
	if a.demo || !a.runtimeAvailableForView() || ct == nil || a.contactByID(ct.ID) != ct || ct.PiBinding == nil {
		return false
	}
	binding := ct.PiBinding
	return binding.Engine == "pi" && binding.Context.BotID == ct.ID && binding.Context.Purpose == pi.Conversation &&
		binding.Context.ContextID != "" && binding.Context.SessionRef != "" && binding.Context.SessionRef == ct.SessionID
}

func (a *appModel) reloadContactByID(contactID string) {
	if host := a.productHost(); host != nil {
		a.reloadProductConversation(host, a.contactByID(contactID))
		return
	}
	ct := a.contactByID(contactID)
	if !a.canReloadPiConversation(ct) {
		return
	}
	ct.historyGen++
	historyGen := ct.historyGen
	host, generation, bindingOwner := a.pi, a.connectionGeneration, ct.PiBinding
	binding := *clonePiConversationBinding(bindingOwner)
	a.historyAsyncStarted()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		projection, err := host.loadPiHistory(ctx, binding)
		cancel()
		a.updateUI(func() {
			defer a.historyAsyncFinished()
			if a.pi != host || a.connectionGeneration != generation || a.contactByID(contactID) != ct ||
				ct.historyGen != historyGen || !a.canReloadPiConversation(ct) || ct.PiBinding != bindingOwner {
				return
			}
			current := ct.PiBinding
			if current.Context != binding.Context || current.Configuration != binding.Configuration ||
				current.Selection.CWD != binding.Selection.CWD || current.Selection.Instructions != binding.Selection.Instructions ||
				(current.Selection.CodingTools == nil) != (binding.Selection.CodingTools == nil) ||
				len(current.Selection.CodingTools) != len(binding.Selection.CodingTools) {
				return
			}
			for i, tool := range binding.Selection.CodingTools {
				if current.Selection.CodingTools[i] != tool {
					return
				}
			}
			if err != nil {
				ct.LoadErr = a.connectionSafeMessage(err)
				ct.Unavailable = ct.LoadErr
				ct.Loaded = false
				return
			}
			ct.Messages, ct.History = projection.messages, projection.entries
			ct.Loaded, ct.LoadErr, ct.Unavailable = true, "", ""
			ct.setStatusDetail(projection.detail, "")
		})
	}()
}

type historyProjection struct {
	messages []*message
	entries  []historyEntry
	detail   string
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
	m := &message{Key: key, Kind: msgContact, TaskID: taskID, ItemID: itemID, Time: nowTime()}
	ct.Messages = append(ct.Messages, m)
	if ct != a.selectedContact() {
		ct.Unread++
	}
	return m
}

type connectionViewState int

const (
	connectionViewHealthy connectionViewState = iota
	connectionViewApplying
	connectionViewConnecting
	connectionViewSaved
	connectionViewSetup
	connectionViewNotConnected
)

func (a *appModel) runtimeAvailableForView() bool {
	if a.demo {
		return true
	}
	if a.productHost() != nil {
		return a.productProfileReady() && !a.connectionApplying && a.connectionEffective != nil
	}
	host := a.pi
	if a.shutdownStarted || a.connectionApplying || host == nil || host.client == nil ||
		a.runtimeStatus != runtimeStateReady || host.generation != a.connectionGeneration {
		return false
	}
	select {
	case <-host.client.Done():
		return false
	default:
		return true
	}
}

func (a *appModel) connectionViewState() connectionViewState {
	if a.demo {
		return connectionViewHealthy
	}
	if a.connectionApplying {
		return connectionViewApplying
	}
	if a.productHost() != nil {
		switch {
		case !a.productProfileReady():
			return connectionViewNotConnected
		case a.productConnectionPending():
			return connectionViewSaved
		case a.connectionEffective != nil:
			return connectionViewHealthy
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
	if a.runtimeStatus != runtimeStateExited && a.connectionForm != nil && !a.connectionForm.HasSaved && a.connectionSaved == nil {
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

// Send/start blockers are stable reasons. One condition function serves the
// composer and every work surface; rendering happens in the current language.
const (
	blockShutdown        = "shutdown"
	blockArchivedSend    = "archived-send"
	blockApplying        = "applying"
	blockRuntimeNotReady = "runtime-not-ready"
	blockHistoryLoading  = "history-loading"
	blockBusy            = "busy"
	blockCapacity        = "capacity"
	blockAnotherContact  = "other-contact-working"
	blockArchivedIssue   = "archived-issue"
	blockIssueActive     = "issue-active"
	blockWorkCapacity    = "work-capacity"
)

func sendBlockRaw(reason string) string {
	switch reason {
	case blockShutdown:
		return "The app is shutting down."
	case blockArchivedSend:
		return "This contact is archived. Restore it before sending a message."
	case blockApplying:
		return "Connection settings are being applied. Wait for Pi to become ready."
	case blockRuntimeNotReady:
		return "Runtime is not ready."
	case blockHistoryLoading:
		return "This contact is still loading its history."
	case blockBusy:
		return "This contact is busy."
	case blockCapacity:
		return "Runtime task capacity is full; retry after a task finishes."
	case blockAnotherContact:
		return "Another contact is working; wait for its task to finish before sending here."
	case blockArchivedIssue:
		return "This contact is archived. Restore it before starting issue work."
	case blockIssueActive:
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
	case blockApplying:
		return msgs.CanSendApplying()
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
	case blockArchivedIssue:
		return msgs.CanStartArchived()
	case blockIssueActive:
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
	if a.connectionApplying {
		return blockApplying
	}
	if !a.runtimeAvailableForView() {
		return blockRuntimeNotReady
	}
	if ct.SessionID != "" && !ct.Loaded {
		return blockHistoryLoading
	}
	if sessionBusy(ct.SendState, ct.ActiveTaskID) {
		return blockBusy
	}
	if a.activeTaskCount() >= a.maxActiveTasks() {
		if a.maxActiveTasks() <= 1 {
			return blockAnotherContact
		}
		return blockCapacity
	}
	return ""
}

func (a *appModel) canStartIssue(ct *contact, issue *productIssue) string {
	if ct == nil || issue == nil || a.contactByID(ct.ID) != ct || ct.issueByID(issue.ID) != issue {
		return "The issue owner is no longer available."
	}
	if a.shutdownStarted {
		return blockShutdown
	}
	if ct.Archived {
		return blockArchivedIssue
	}
	if a.demo {
		return ""
	}
	if a.connectionApplying {
		return blockApplying
	}
	if !a.runtimeAvailableForView() {
		return blockRuntimeNotReady
	}
	if sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) {
		return blockIssueActive
	}
	if a.activeTaskCount() >= a.maxActiveTasks() {
		return blockCapacity
	}
	if a.activeWorkTaskCount() >= a.workTaskLimit() {
		return blockWorkCapacity
	}
	return ""
}

func (a *appModel) startIssueTask(ct *contact, issue *productIssue, execution *issueExecution, prompt string) bool {
	if a.shutdownStarted {
		issue.Work.setSendError("The app is shutting down.", "shutdown", "")
		return false
	}
	if a.demo {
		issue.Work.setSendError("Runtime is not ready.", "runtime-not-ready", "")
		return false
	}
	return a.startPiIssueTask(ct, issue, execution, prompt)
}

// submissionAsyncStarted/Finished keep the submission owner visible to the
// shutdown drain until the final accepted/rejected/unknown callback has run.
// Both submit paths start after their admission state is saved and finish only
// inside their final UI callback, including early-return paths.
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
		return
	}
	if a.pi == nil || ct == nil || a.contactByID(ct.ID) != ct || ct.PiBinding == nil || taskID == "" || ct.ActiveTaskID != taskID {
		return
	}
	if a.shutdownStarted {
		if ct.shutdownCancelTaskID == taskID {
			return
		}
		ct.shutdownCancelTaskID = taskID
	}
	host, contactID, contextID := a.pi, ct.ID, ct.PiBinding.Context.ContextID
	generation := a.connectionGeneration
	ct.setSendError("Cancellation requested", "send-cancelled", "")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := host.client.CancelRun(ctx, pi.CancelRunParams{ContextID: contextID, RunID: taskID})
		cancel()
		a.updateUI(func() {
			ct := a.contactByID(contactID)
			if a.pi == host && a.connectionGeneration == generation && ct != nil && ct.PiBinding != nil && ct.PiBinding.Context.ContextID == contextID && ct.ActiveTaskID == taskID && err != nil {
				ct.setSendError(a.connectionSafeMessage(err), "", "")
			}
		})
	}()
}

func (a *appModel) cancelIssue(issueID string) bool {
	return a.cancelIssueFor(a.selectedContact(), issueID)
}

func (a *appModel) cancelIssueFor(ct *contact, issueID string) bool {
	if a.pi == nil || ct == nil {
		return false
	}
	return a.cancelPiIssue(ct, ct.issueByID(issueID))
}

func (a *appModel) reloadSelected() {
	ct := a.selectedContact()
	if !a.canReloadPiConversation(ct) {
		if ct != nil {
			ct.setSendError("Conversation history is unavailable for this contact or connection.", "", "")
		}
		return
	}
	a.reloadContactByID(ct.ID)
}

func (a *appModel) addRuntimeContact() {
	workspace := ""
	if a.demo || a.connectionEffective != nil {
		workspace = a.workDir
	}
	if host := a.productHost(); host != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bot, err := host.product.CreateBot(ctx, pi.ProductBotCreateParams{OperationID: newContactID(),
			Profile: pi.ProductBotProfile{Name: locales.In(a.uiLocale).NewContactName(len(a.contacts) + 1), Role: "Assistant", Workspace: workspace},
		})
		if err != nil {
			a.selectedContact().setSendError(a.connectionSafeMessage(err), "", "")
			return
		}
		ct := productContact(bot)
		ct.Loaded = true
		ct.Context.onChange = func() error { return a.saveProductDraft(ct) }
		host.productBots[ct.ID] = bot
		a.contacts = append(a.contacts, ct)
		a.selectContact(len(a.contacts) - 1)
		a.contactSearch, a.focusComposer = "", true
		return
	}
	c := &contact{
		ID:        newContactID(),
		Name:      locales.In(a.uiLocale).NewContactName(len(a.contacts) + 1),
		Role:      "Assistant",
		Status:    statusReady,
		WorkLabel: "Preparing a reply",
		Workspace: workspace,
	}
	c.Context.onChange = a.persist
	a.contacts = append(a.contacts, c)
	a.selected = len(a.contacts) - 1
	a.contactSearch = ""
	a.aux = ""
	a.focusComposer = true
	a.persist()
}

func profileSystemPrompt(ct *contact) string {
	var parts []string
	if strings.TrimSpace(ct.Purpose) != "" {
		parts = append(parts, "Purpose: "+strings.TrimSpace(ct.Purpose))
	}
	if strings.TrimSpace(ct.Instructions) != "" {
		parts = append(parts, "Instructions: "+ct.Instructions)
	}
	return strings.Join(parts, "\n")
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
		return errors.New("the local contact store is unavailable")
	}
	if len(a.contacts) == 0 {
		return errors.New("there is no current contact to save")
	}
	if _, err := os.Stat(a.storePath); err == nil {
		return fmt.Errorf("local contact store %s exists but has not been loaded", a.storePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	a.store = &contactStore{path: a.storePath, state: storedState{Version: contactStoreVersion}}
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
		a.store.setIssueToolState(a.issueToolLedger, a.issueToolScopes)
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
		err = a.store.saveWithPreferences(a.contacts, a.selectedID(), normalizeAppearanceMode(a.appearanceMode), themeIDs, layout)
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
