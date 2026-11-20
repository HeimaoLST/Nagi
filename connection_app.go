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

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

type connectionStartup struct {
	Path       string
	Config     *connectionConfig
	FormConfig *connectionConfig
	LoadErr    error
}

func connectionConfigPath(storePath string) string {
	return filepath.Join(filepath.Dir(storePath), "connection.json")
}

func productConnectionConfig(view *pi.ProductConnectionView) *connectionConfig {
	if view == nil {
		return nil
	}
	api := connectionAPIChatCompletions
	if view.WireAPI == pi.OpenAIResponses {
		api = connectionAPIResponses
	}
	return &connectionConfig{API: api, Endpoint: view.BaseURL, Model: view.Model, ContextTokens: view.ContextTokens,
		OutputTokens: view.OutputTokens, WorkingDirectory: view.WorkingDirectory, MaxActiveTasks: view.MaxActiveTasks}
}

func (a *appModel) applyProductConnection(status pi.ProductConnectionStatus) {
	host := a.productHost()
	host.productConn = status
	a.connectionSaved, a.connectionEffective = productConnectionConfig(status.Saved), productConnectionConfig(status.Effective)
	a.connectionManaged = status.Saved != nil
	a.runtimeStatus = runtimeStateReady
	a.runtimeErr, a.runtimeErrKind = "", ""
	if effective := a.connectionEffective; effective != nil {
		a.workDir = effective.WorkingDirectory
		a.runtimeMaxActiveTasks = effective.MaxActiveTasks
		a.runtimeCapabilitiesLoaded = true
		return
	}
	a.runtimeCapabilitiesLoaded = false
	if status.Saved == nil {
		a.runtimeErrKind = runtimeNoticeConnectionRequired
	}
}

func (a *appModel) productConnectionPending() bool {
	host := a.productHost()
	if host == nil || host.productConn.SavedRevision == nil {
		return false
	}
	effective := host.productConn.EffectiveRevision
	return effective == nil || *effective != *host.productConn.SavedRevision
}

func productConnectionInput(config connectionConfig) pi.ProductConnectionInput {
	return pi.ProductConnectionInput{APIKey: config.APIKey, ProductConnectionSettings: pi.ProductConnectionSettings{
		WireAPI: piWireAPI(config.API), BaseURL: config.Endpoint, Model: config.Model, ContextTokens: config.ContextTokens,
		OutputTokens: config.OutputTokens, WorkingDirectory: config.WorkingDirectory, MaxActiveTasks: config.MaxActiveTasks,
	}}
}

func (a *appModel) changeProductConnection(host *piAppHost, config *connectionConfig, apply bool) {
	form := a.connectionForm
	form.Busy = true
	form.Error, form.ErrorDetail, form.ReloadBlocked = "", "", ""
	if config != nil {
		form.Status = connStatusSaving
	} else {
		form.Status = connStatusChecking
	}
	a.connectionSaveInFlight = true
	known := host.productConn.SavedRevision
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var err error
		saved := known
		if config != nil {
			result, failure := host.product.SaveConnection(ctx, pi.ProductConnectionSaveParams{OperationID: newContactID(), Configuration: productConnectionInput(*config)})
			saved, err = result.SavedRevision, failure
		}
		if err == nil && apply && saved != nil {
			_, err = host.product.ApplyConnection(ctx, pi.ProductConnectionApplyParams{OperationID: newContactID(), SavedRevision: *saved})
		}
		projection, readErr := host.product.Connection(ctx)
		a.updateUI(func() {
			a.connectionSaveInFlight = false
			form.Busy = false
			form.Status = ""
			if a.pi != host {
				return
			}
			if readErr == nil {
				a.applyProductConnection(projection)
			}
			if config != nil && saved != nil {
				form.HasSaved = true
				form.initial = form.Draft
			}
			if err != nil {
				form.Error = a.connectionSafeMessage(err)
			} else if readErr != nil {
				form.Error = a.connectionSafeMessage(readErr)
			} else if apply {
				form.Status = connStatusApplied
			}
			if a.shutdownStarted {
				a.finishShutdown()
			}
		})
	}()
}

func (a *appModel) testProductConnection(host *piAppHost, config connectionConfig) {
	form := a.connectionForm
	form.Busy, a.connectionTesting = true, true
	form.Error, form.ErrorDetail, form.Status = "", "", connStatusTesting
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	a.connectionTestCancel = cancel
	go func() {
		err := host.product.TestConnection(ctx, productConnectionInput(config))
		cancel()
		a.updateUI(func() {
			a.connectionTesting, form.Busy, a.connectionTestCancel = false, false, nil
			if err != nil {
				form.Status, form.Error, form.ErrorDetail = "", connErrorTestFailed, a.connectionSafeMessage(err)
			} else {
				form.Status = connStatusTestCompleted
				form.StatusArgs = []string{config.Model, config.Endpoint, fmt.Sprint(config.ContextTokens), fmt.Sprint(config.OutputTokens)}
			}
			if a.shutdownStarted {
				a.finishShutdown()
			}
		})
	}()
}

func readConnectionStartup(storePath string) connectionStartup {
	startup := connectionStartup{Path: connectionConfigPath(storePath)}
	config, err := loadConnectionConfig(startup.Path)
	if err == nil {
		startup.Config = &config
		startup.FormConfig = &config
		return startup
	}
	if errors.Is(err, os.ErrNotExist) {
		initial := defaultConnectionConfig("")
		startup.FormConfig = &initial
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
			PiBinding:          clonePiConversationBinding(stored.PiBinding),
			PreviousPiBindings: clonePiConversationBindings(stored.PreviousPiBindings),
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
		ID: newContactID(), Name: "Nagi", Role: "Assistant",
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
	} else {
		a.leftPaneWidth = 0
		a.rightPaneWidth = 0
	}
	a.workspaceOpen = false
	a.detail = ""
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
		config = readConnectionStartup(a.storePath).FormConfig
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
	if form.initial.WorkingDirectory == "" && a.connectionSaved == nil {
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
	if form.Busy || a.shutdownStarted || a.dataRecoveryBlocksChanges() {
		return
	}
	if host := a.productHost(); host != nil {
		a.changeProductConnection(host, &config, applyAfterSave)
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
	if a.productHost() != nil {
		if !a.productProfileReady() && a.runtimeStatus != runtimeStateExited {
			return connBlockedStore
		}
		return ""
	}
	if a.store == nil || a.localSaveErr != "" {
		return connBlockedStore
	}
	recoverable := false
	if host := a.pi; host != nil && host.client != nil && host.generation == a.connectionGeneration && a.runtimeStatus == runtimeStateExited && host.exitApplied != nil {
		select {
		case <-host.client.Done():
			select {
			case <-host.client.CallbacksDone():
				select {
				case <-host.exitApplied:
					if host.mu.TryLock() {
						recoverable = host.outcome.Reaped && host.outcome.PID > 0
						host.mu.Unlock()
					}
				default:
				}
			default:
			}
		default:
		}
	}
	for _, ct := range a.contacts {
		if ct.ActiveTaskID != "" || ct.SendState == sendSubmitting || ct.SendState == sendAccepted {
			return connBlockedActiveWork
		}
		if recoverable && ct.SendState == sendUnknown && (ct.pendingSend == nil || ct.pendingSend.State != "unknown" || ct.PiBinding == nil || ct.PiBinding.Engine != "pi" || ct.PiBinding.Context.Purpose != "conversation" || ct.PiBinding.Context.BotID != ct.ID || ct.PiBinding.Context.ContextID == "" || ct.PiBinding.Context.SessionRef == "" || ct.PiBinding.Context.SessionRef != ct.SessionID || ct.PiBinding.Configuration.CredentialSource == "") {
			return connBlockedActiveWork
		}
	}
	for _, ct := range a.contacts {
		for _, issue := range ct.Issues {
			if issue.Work.ActiveTaskID != "" || issue.Work.SendState == sendSubmitting || issue.Work.SendState == sendAccepted {
				return connBlockedUntrustedIssue
			}
			for _, execution := range issue.Executions {
				if !unresolvedExecution(execution) {
					continue
				}
				if !recoverable || execution.Status != issueExecutionUnknown || !execution.PiIntent || !isPiActivityIssue(issue) || issue.ContactID != ct.ID || execution.PiRun == nil || execution.ID == "" || execution.PiRun.WorkerEpoch == "" || execution.PiRun.RunID == "" || execution.PiRun.ContextID != issue.PiBinding.Context.ContextID || execution.PiRun.SessionRef != issue.PiBinding.Context.SessionRef {
					return connBlockedUntrustedIssue
				}
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
	if host := a.productHost(); host != nil {
		if a.runtimeStatus == runtimeStateExited {
			a.reopenProductConnection(host)
			return
		}
		a.changeProductConnection(host, nil, true)
		return
	}
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
	if form.Busy || a.connectionTesting || a.shutdownStarted || a.dataRecoveryBlocksChanges() {
		return
	}
	if host := a.productHost(); host != nil {
		a.testProductConnection(host, config)
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

// finishShutdown waits for admitted operations and their final UI callbacks before draining Pi.
func (a *appModel) finishShutdown() {
	if a.quitReady || a.shutdownDrainStarted {
		return
	}
	if host := a.productHost(); host != nil {
		if (a.dataRecovery != nil && a.dataRecovery.inFlight) || a.connectionApplying || a.connectionSaveInFlight || a.connectionTesting || a.submissionInFlight > 0 || a.contactHistoryInFlight > 0 {
			return
		}
		a.shutdownDrainStarted = true
		a.runtimeStatus = runtimeStateDraining
		go a.finishProductShutdown(host)
		return
	}
	if a.pi != nil {
		active := false
		for _, ct := range a.contacts {
			for _, issue := range ct.Issues {
				if issue.Work.ActiveTaskID != "" {
					active = true
					if !issue.Work.StopRequestedTasks[issue.Work.ActiveTaskID] {
						a.cancelPiIssue(ct, issue)
					}
				}
			}
			if ct.ActiveTaskID != "" {
				active = true
				if ct.SendState != sendSubmitting || a.submissionInFlight == 0 {
					a.cancelRuntimeTask(ct, ct.ActiveTaskID)
				}
			} else if ct.SendState == sendSubmitting {
				active = true
			}
		}
		if active {
			return
		}
	}
	if (a.dataRecovery != nil && a.dataRecovery.inFlight) || a.connectionSaveInFlight || a.connectionApplying || a.connectionTesting || a.contactHistoryInFlight > 0 || a.submissionInFlight > 0 || (a.themeImport != nil && a.themeImport.installInFlight) {
		return
	}
	if a.pi != nil {
		a.shutdownDrainStarted = true
		a.runtimeStatus = runtimeStateDraining
		host := a.pi
		go a.finishPiShutdown(host)
		return
	}
	a.quitReady = true
	if a.requestQuit != nil {
		a.requestQuit()
	}
}
