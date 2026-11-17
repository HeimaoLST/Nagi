package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"github.com/HeimaoLST/Nagi/locales"
)

type connectionStartup struct {
	Path       string
	Config     *connectionConfig
	FormConfig *connectionConfig
	LoadErr    error
	Missing    bool
	External   bool
}

func connectionConfigPath(storePath string) string {
	return filepath.Join(filepath.Dir(storePath), "connection.json")
}

func readConnectionStartup(storePath string, workDir string) connectionStartup {
	startup := connectionStartup{Path: connectionConfigPath(storePath)}
	config, err := loadConnectionConfig(startup.Path)
	if err == nil {
		startup.Config = &config
		startup.FormConfig = &config
		return startup
	}
	if errors.Is(err, os.ErrNotExist) {
		startup.Missing = true
		initial := defaultConnectionConfig("")
		startup.FormConfig = &initial
		startup.LoadErr = nil
		return startup
	}
	startup.LoadErr = err
	if config.WorkingDirectory != "" || config.Endpoint != "" || config.APIKey != "" || config.Model != "" {
		startup.FormConfig = &config
	}
	return startup
}

func contactsFromStore(store *contactStore) []*contact {
	contacts := make([]*contact, 0, len(store.state.Contacts))
	for _, stored := range store.state.Contacts {
		ct := &contact{
			ID: stored.ID, Name: stored.Name, Role: stored.Role,
			Purpose: stored.Purpose, Instructions: stored.Instructions,
			Workspace: stored.Workspace, BuiltinTools: cloneBuiltinTools(stored.BuiltinTools),
			SessionID: stored.SessionID, Draft: stored.Draft,
			Archived: stored.Archived, ArchivedAt: stored.ArchivedAt,
			PreviousSessions:   append([]string(nil), stored.PreviousSessions...),
			PiBinding:          clonePiConversationBinding(stored.PiBinding),
			PreviousPiBindings: stored.PreviousPiBindings,
			Status:             statusReady, WorkLabel: "Preparing a reply",
		}
		ct.Issues = issuesForStoredContact(store.state.Issues, ct.ID)
		if stored.SelectedIssueID != "" {
			ct.SelectedIssueID = stored.SelectedIssueID
		}
		if issue := ct.selectedIssue(); issue != nil {
			ct.SelectedIssueID = issue.ID
		}
		contacts = append(contacts, ct)
	}
	return contacts
}

func placeholderContact(workDir string) *contact {
	return &contact{
		ID: newContactID(), Name: "Saki", Role: "Assistant",
		Workspace: workDir, Status: statusReady, WorkLabel: "Preparing a reply",
	}
}

func applyStoreSelection(a *appModel, store *contactStore) {
	a.store = store
	a.issueToolLedger = store.issueToolLedger
	a.issueToolScopes = store.issueToolScopes
	a.contacts = contactsFromStore(store)
	a.selected = 0
	for i, ct := range a.contacts {
		if ct.ID == store.state.Selected {
			a.selected = i
			break
		}
	}
	if len(a.contacts) == 0 {
		a.contacts = []*contact{placeholderContact(a.workDir)}
		a.selected = 0
	}
	if layout := store.state.Layout; layout != nil {
		a.leftPaneWidth = clampPaneWidth(layout.LeftWidth, defaultLeftPaneWidth, minLeftPaneWidth, maxLeftPaneWidth)
		a.rightPaneWidth = clampPaneWidth(layout.RightWidth, defaultRightPaneWidth, minRightPaneWidth, maxRightPaneWidth)
		a.workspaceOpen = layout.WorkspaceOpen
	} else {
		a.leftPaneWidth = 0
		a.rightPaneWidth = 0
		a.workspaceOpen = false
	}
	a.appearanceMode = normalizeAppearanceMode(store.state.Appearance)
	a.appearanceThemeIDs = appearanceThemeIDs{}
	if store.state.ThemeIDs != nil {
		a.appearanceThemeIDs = *store.state.ThemeIDs
	}
}

func (a *appModel) ensureConnectionForm() {
	if a.connectionForm != nil {
		return
	}
	config := a.connectionSaved
	if config == nil {
		config = readConnectionStartup(a.storePath, a.workDir).FormConfig
	}
	a.connectionForm = newConnectionSettings(config, a.workDir)
	if a.connectionSaved == nil {
		a.connectionForm.HasSaved = false
	}
}

func (a *appModel) connectionActions() connectionSettingsActions {
	return connectionSettingsActions{
		Save:         a.saveConnection,
		SaveAndApply: a.saveAndApplyConnection,
		Apply:        a.applyConnection,
		Test:         a.testConnection,
		ChooseFolder: a.chooseConnectionWorkFolder,
	}
}

func workFolderStart(draft, effective string) string {
	for _, path := range []string{draft, effective} {
		if filepath.IsAbs(path) {
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				return path
			}
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

func chooseWorkFolder(pick func(mygo.OpenDialogOptions) ([]string, error), opts mygo.OpenDialogOptions) (string, error) {
	if pick == nil {
		pick = mygo.Dialog.Open
	}
	paths, err := pick(opts)
	if err != nil || len(paths) == 0 {
		return "", err
	}
	path := paths[0]
	if !filepath.IsAbs(path) {
		return "", errors.New("the selected folder path is not absolute")
	}
	folder, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer folder.Close()
	// Reading an entry also detects an inaccessible or removed directory; empty folders are valid.
	if _, err := folder.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return path, nil
}

func (a *appModel) chooseConnectionWorkFolder() {
	form := a.connectionForm
	if form == nil || form.Busy || a.shutdownStarted || !a.settingsOpen || a.settingsTab != settingsTabConnection {
		return
	}
	a.folderChoiceGeneration++
	generation := a.folderChoiceGeneration
	msgs := locales.In(a.uiLocale)
	opts := mygo.OpenDialogOptions{Parent: a.win, Directory: true,
		Title: msgs.WorkFolderChooseTitle(), ButtonLabel: msgs.WorkFolderChoose()}
	draft, effective := form.Draft.WorkingDirectory, a.workDir
	if form.initial.WorkingDirectory == "" && a.connectionSaved == nil && !a.connectionLegacy {
		effective = ""
	}
	pick := a.chooseFolder
	form.Busy = true
	go func() {
		opts.DefaultPath = workFolderStart(draft, effective)
		path, err := chooseWorkFolder(pick, opts)
		a.updateUI(func() {
			form.Busy = false
			if a.shutdownStarted || generation != a.folderChoiceGeneration || a.connectionForm != form || !a.settingsOpen || a.settingsTab != settingsTabConnection {
				return
			}
			form.focusFolder = true
			if err != nil {
				form.folderError = err.Error()
			} else if path != "" {
				form.Draft.WorkingDirectory = path
				delete(form.Errors, "WorkingDirectory")
				form.folderError = ""
			}
		})
	}()
}

func (a *appModel) saveConnection(config connectionConfig) {
	a.saveConnectionWithApply(config, false)
}

func (a *appModel) saveAndApplyConnection(config connectionConfig) {
	a.saveConnectionWithApply(config, true)
}

func (a *appModel) saveConnectionWithApply(config connectionConfig, applyAfterSave bool) {
	a.ensureConnectionForm()
	form := a.connectionForm
	if a.piDataReadOnly() {
		form.Error = "Legacy product data is read-only until its history policy is selected."
		return
	}
	if form.Busy || a.shutdownStarted || a.dataRecoveryBlocksChanges() {
		return
	}
	path := a.connectionPath
	if path == "" {
		path = connectionConfigPath(a.storePath)
		a.connectionPath = path
	}
	needStore := a.store == nil
	if needStore || a.localSaveErr != "" {
		// persist owns local state and its error; the connection write reports independently.
		_ = a.persist()
	}
	form.Busy = true
	form.Error = ""
	form.ErrorDetail = ""
	form.ReloadBlocked = ""
	form.Status = connStatusSaving
	a.connectionSaveInFlight = true
	go func() {
		err := saveConnectionConfig(path, config)
		if err != nil {
			a.updateUI(func() {
				a.connectionSaveInFlight = false
				form.Busy = false
				form.Status = ""
				form.ErrorDetail = a.connectionSafeMessage(err)
				form.Error = form.ErrorDetail
				if a.shutdownStarted {
					a.finishShutdown()
				}
			})
			return
		}
		a.updateUI(func() {
			a.connectionSaveInFlight = false
			form.Busy = false
			a.connectionSaved = &config
			a.connectionForm.HasSaved = true
			a.connectionForm.initial = a.connectionForm.Draft
			a.connectionForm.Status = ""
			if a.connectionApplied() && *a.connectionEffective == config {
				a.connectionForm.Status = connStatusSavedApplied
			}
			a.connectionForm.Error = ""
			a.connectionForm.ErrorDetail = ""
			if a.localSaveErr != "" {
				a.connectionForm.Error = connStatusSavedStoreFailed
				a.connectionForm.ErrorDetail = a.redactConnectionText(a.localSaveErr)
			} else if needStore {
				a.workDir = config.WorkingDirectory
				a.startupDetailsOpen = false
			}
			if applyAfterSave && !a.shutdownStarted {
				if reason := a.connectionReloadBlocked(); reason != "" {
					form.ReloadBlocked = reason
				} else {
					a.applyConnection()
				}
			}
			if a.shutdownStarted {
				a.finishShutdown()
			}
		})
	}()
}

func runtimeHasExited(rt *runtimeClient) bool {
	if rt == nil {
		return true
	}
	select {
	case <-rt.done:
		return true
	default:
		return false
	}
}

func (a *appModel) connectionReloadBlocked() string {
	if a.connectionApplying {
		return connBlockedApplying
	}
	if a.shutdownStarted {
		return connBlockedShutdown
	}
	if a.connectionSaved == nil {
		return connBlockedNoSaved
	}
	if a.store == nil || a.localSaveErr != "" {
		return connBlockedStore
	}
	if a.piDataReadOnly() {
		return connBlockedStore
	}
	for _, ct := range a.contacts {
		if ct.ActiveTaskID != "" || ct.SendState == sendSubmitting || ct.SendState == sendAccepted {
			return connBlockedActiveWork
		}
	}
	for _, ct := range a.contacts {
		for _, issue := range ct.Issues {
			if reason := issueRunBlocked(issue); reason != "" {
				return connBlockedUntrustedIssue
			}
		}
	}
	return ""
}

func (a *appModel) applyConnection() {
	a.ensureConnectionForm()
	form := a.connectionForm
	if form.Busy || a.shutdownStarted || a.dataRecoveryBlocksChanges() {
		return
	}
	if reason := a.connectionReloadBlocked(); reason != "" {
		form.ReloadBlocked = reason
		return
	}
	form.ReloadBlocked = ""
	form.Error = ""
	form.ErrorDetail = ""
	a.startPiHost()
}

func (a *appModel) runOnUI(fn func()) {
	done := make(chan struct{})
	a.updateUI(func() {
		fn()
		close(done)
	})
	<-done
}

func (a *appModel) testConnection(config connectionConfig) {
	a.ensureConnectionForm()
	form := a.connectionForm
	if form.Busy || a.connectionTesting || a.shutdownStarted || a.dataRecoveryBlocksChanges() || a.piDataReadOnly() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	a.connectionTesting = true
	a.connectionTestCancel = cancel
	probe := &piConnectionProbe{}
	a.connectionProbe = probe
	form.Busy = true
	form.Error = ""
	form.ErrorDetail = ""
	form.Status = connStatusTesting
	go func() {
		err := probePiConnection(ctx, config, probe)
		cancel()
		a.updateUI(func() {
			if a.connectionProbe != probe {
				return
			}
			a.connectionTestCancel = nil
			if !probe.cleanupComplete() {
				form.Status = ""
				form.Error = connErrorTestFailed
				form.ErrorDetail = a.connectionSafeMessage(err)
				if a.shutdownStarted {
					a.runtimeErrKind = runtimeNoticeRuntimeShutdown
					a.runtimeErr = form.ErrorDetail
				}
				go a.awaitPiConnectionProbe(probe)
				return
			}
			a.connectionTesting = false
			form.Busy = false
			if a.shutdownStarted {
				a.finishShutdown()
				return
			}
			if err != nil {
				form.Status = ""
				form.Error = connErrorTestFailed
				form.ErrorDetail = a.connectionSafeMessage(err)
				return
			}
			form.Status = connStatusTestCompleted
			form.StatusArgs = []string{config.Model, config.Endpoint, fmt.Sprint(config.ContextTokens), fmt.Sprint(config.OutputTokens)}
			form.Error = ""
		})
	}()
}

func (a *appModel) awaitPiConnectionProbe(probe *piConnectionProbe) {
	probe.mu.Lock()
	client, dir := probe.client, probe.stateDir
	probe.mu.Unlock()
	outcome, waitErr := client.Wait(context.Background())
	<-client.CallbacksDone()
	var removeErr error
	if outcome.Reaped {
		removeErr = os.RemoveAll(dir)
	}
	probe.mu.Lock()
	probe.outcome = outcome
	probe.callbacksDone = true
	probe.waitErr = errors.Join(probe.waitErr, waitErr)
	probe.removeErr = errors.Join(probe.removeErr, removeErr)
	probe.cleanupErr = errors.Join(probe.cleanupErr, waitErr, removeErr)
	probe.testErr = errors.Join(probe.testErr, waitErr, removeErr)
	testErr := probe.testErr
	probe.mu.Unlock()
	a.updateUI(func() {
		if a.connectionProbe != probe {
			return
		}
		if !outcome.Reaped {
			a.connectionForm.ErrorDetail = a.connectionSafeMessage(errors.Join(testErr, errors.New("connection test worker was not reaped")))
			return
		}
		a.connectionTesting = false
		a.connectionTestCancel = nil
		a.connectionForm.Busy = false
		a.connectionForm.Status = ""
		a.connectionForm.Error = connErrorTestFailed
		a.connectionForm.ErrorDetail = a.connectionSafeMessage(testErr)
		if a.shutdownStarted {
			a.finishShutdown()
		}
	})
}

func (a *appModel) runtimeHandlers(gen int, ready chan<- runtimeReady) runtimeHandlers {
	return runtimeHandlers{
		OnReady: func(value runtimeReady) {
			if ready != nil {
				select {
				case ready <- value:
				default:
				}
			}
			a.updateUI(func() { a.handleRuntimeReady(gen, value) })
		},
		OnEvent: func(event runtimeEventEnvelope) {
			a.updateUI(func() { a.handleRuntimeEvent(gen, event) })
		},
		OnExit: func(err error) {
			a.updateUI(func() { a.handleRuntimeExit(gen, err) })
		},
		OnProtocolError: func(message string) {
			a.updateUI(func() { a.handleRuntimeProtocolError(gen, message) })
		},
	}
}

func (a *appModel) handleRuntimeReady(gen int, ready runtimeReady) {
	if gen != a.connectionGeneration || a.rt == nil || a.shutdownStarted {
		return
	}
	a.runtimeStatus = runtimeStateReady
	a.runtimeErr = ""
	a.runtimeErrKind = ""
	a.runtimeStage = ""
	a.runtimeHint = ""
	a.runtimeHintKind = ""
	a.runtimeHintArgs = nil
	a.runtimeAPI = ready.API
	a.runtimeHandshake = true
	a.restoreContacts(a.rt, gen)
	a.restoreIssueWorks(a.rt, gen)
	a.startCapabilitiesLoad(a.rt, gen)
	if a.connectionManaged {
		a.startBuiltinToolsLoad(a.rt, gen)
	}
}

func (a *appModel) handleRuntimeEvent(gen int, event runtimeEventEnvelope) {
	if gen != a.connectionGeneration {
		return
	}
	a.applyRuntimeEvent(event)
}

func (a *appModel) handleRuntimeExit(gen int, err error) {
	if gen != a.connectionGeneration {
		return
	}
	a.applyRuntimeExit(err)
}

func (a *appModel) handleRuntimeProtocolError(gen int, message string) {
	if gen != a.connectionGeneration {
		return
	}
	a.protocolFailed(message)
}

func (a *appModel) connectionSafeMessage(err error) string {
	if err == nil {
		return ""
	}
	return a.redactConnectionText(err.Error())
}

func (a *appModel) redactConnectionText(text string) string {
	keys := make([]string, 0, 4)
	for _, config := range []*connectionConfig{a.connectionSaved, a.connectionEffective} {
		if config != nil && config.APIKey != "" {
			keys = append(keys, config.APIKey)
		}
	}
	if a.connectionForm != nil && a.connectionForm.Draft.APIKey != "" {
		keys = append(keys, a.connectionForm.Draft.APIKey)
	}
	for _, key := range keys {
		text = strings.ReplaceAll(text, key, "[redacted]")
	}
	return safeDiagnostic(text)
}

func (a *appModel) connectionRuntimeMessage(err error) string {
	return a.redactConnectionText(runtimeMessage(err))
}

func (a *appModel) connectionTaskFinishedDetail(payload *runtimeEvent) string {
	if payload == nil {
		return ""
	}
	copyPayload := *payload
	if payload.Error != nil {
		errorCopy := *payload.Error
		errorCopy.Message = a.redactConnectionText(payload.Error.Message)
		errorCopy.Reason = a.redactConnectionText(payload.Error.Reason)
		copyPayload.Error = &errorCopy
	}
	return taskFinishedDetail(&copyPayload)
}

func (a *appModel) configuredWorkCreate(rt *runtimeClient, configured bool, workspace, system string, builtin *[]string, issueTools bool) (runtimeSession, error) {
	if configured {
		selection, err := runtimeWorkSelection(rt, builtin, issueTools)
		if err != nil {
			return runtimeSession{}, err
		}
		return rt.createConfiguredConversationSession(workspace, system, selection)
	}
	if builtin != nil {
		return runtimeSession{}, errors.New("Built-in tool selection requires a managed Nagi connection; save and Apply a connection before creating a new work session.")
	}
	return rt.createSession(workspace, system)
}

func (c *runtimeClient) createConfiguredSession(workingDir, systemPrompt string) (runtimeSession, error) {
	return c.createConfiguredSessionWithTools(workingDir, systemPrompt, nil)
}

func (c *runtimeClient) createConfiguredConversationSession(workingDir, systemPrompt string, toolSelection map[string]any) (runtimeSession, error) {
	return c.createConfiguredSessionWithTools(workingDir, systemPrompt, toolSelection)
}

func (c *runtimeClient) createConfiguredSessionWithTools(workingDir, systemPrompt string, toolSelection map[string]any) (runtimeSession, error) {
	config := map[string]any{
		"connection_id": connectionID,
	}
	if workingDir != "" {
		config["working_directory"] = workingDir
	}
	if systemPrompt != "" {
		config["system_prompt"] = systemPrompt
	}
	if toolSelection != nil {
		config["tool_selection"] = toolSelection
	}
	var session runtimeSession
	err := c.call("session.create", map[string]any{
		"storage_schema": 2,
		"config":         config,
	}, &session, runtimeControlTimeout)
	if err != nil {
		return runtimeSession{}, err
	}
	if session.SessionID == "" {
		return runtimeSession{}, errors.New("runtime returned a session without session_id")
	}
	return session, nil
}

// finishShutdown drains Runtime only after in-flight history, metadata,
// issue/conversation submission, result reads, and theme installs have delivered
// their final UI callbacks; those callbacks re-enter this function.
func (a *appModel) finishShutdown() {
	if a.quitReady || a.shutdownDrainStarted || (a.dataRecovery != nil && a.dataRecovery.inFlight) || a.connectionSaveInFlight || a.connectionApplying || a.connectionTesting || a.contactHistoryInFlight > 0 || a.resultInFlight > 0 || a.submissionInFlight > 0 || a.issueToolReleaseInFlight > 0 || a.metadataInFlight > 0 || (a.themeImport != nil && a.themeImport.installInFlight) {
		return
	}
	if a.pi != nil {
		for _, ct := range a.contacts {
			if ct.ActiveTaskID != "" || ct.SendState == sendSubmitting {
				return
			}
		}
		a.shutdownDrainStarted = true
		a.runtimeStatus = runtimeStateDraining
		host := a.pi
		go a.finishPiShutdown(host)
		return
	}
	if a.rt == nil {
		if a.issueToolServer == nil {
			a.quitReady = true
			if a.requestQuit != nil {
				a.requestQuit()
			}
			return
		}
		a.shutdownDrainStarted = true
		go func() {
			err := a.closeIssueToolService()
			a.updateUI(func() {
				a.quitReady = true
				if err != nil {
					a.runtimeErrKind = runtimeNoticeIssueToolShutdown
					a.runtimeErr = a.connectionSafeMessage(err)
				}
				if a.requestQuit != nil {
					a.requestQuit()
				}
			})
		}()
		return
	}
	a.shutdownDrainStarted = true
	a.runtimeStatus = runtimeStateDraining
	a.runtimeErr = ""
	a.runtimeErrKind = ""
	rt := a.rt
	launch := a.connectionLaunch
	go func() {
		err := rt.shutdown()
		if launch != nil {
			_ = launch.Cleanup()
		}
		toolErr := a.closeIssueToolService()
		a.updateUI(func() {
			a.quitReady = true
			if err != nil {
				a.runtimeErrKind = runtimeNoticeRuntimeShutdown
				a.runtimeErr = a.connectionSafeMessage(err)
			}
			if toolErr != nil {
				a.runtimeErrKind = runtimeNoticeIssueToolShutdown
				a.runtimeErr = a.connectionSafeMessage(toolErr)
			}
			if a.requestQuit != nil {
				a.requestQuit()
			}
		})
	}()
}
