package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HeimaoLST/Nagi/internal/connection"
	"github.com/HeimaoLST/Nagi/internal/pi"
)

type connectionStartup struct {
	Path    string
	Config  *connectionConfig
	LoadErr error
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
	a.connectionSaved, a.connectionEffective = productConnectionConfig(status.Saved), productConnectionConfig(status.Effective)
	a.connectionManaged = status.Saved != nil
	a.updateProductReadiness()
}

func (a *appModel) updateProductReadiness() {
	if a.shutdownStarted || !a.productProfileReady() {
		return
	}
	a.runtimeStatus = runtimeStateReady
	a.runtimeErr, a.runtimeErrKind = "", ""
	a.runtimeCapabilitiesLoaded = a.connectionEffective != nil || a.hasUsableProviderRoute()
	if effective := a.connectionEffective; effective != nil {
		a.workDir = effective.WorkingDirectory
		a.runtimeMaxActiveTasks = effective.MaxActiveTasks
	} else if a.hasUsableProviderRoute() {
		a.runtimeMaxActiveTasks = 2
	} else if a.hasEffectiveProviderPool() {
		switch a.productHost().productProviders.RoleStatus["dialogue"].Status {
		case "credential_missing", "auth_failed", "":
			a.runtimeErrKind = runtimeNoticeCredentialUnavailable
		default:
			a.runtimeErrKind = runtimeNoticeRouteUnavailable
		}
	} else if !a.hasAnySavedConnection() && !a.productProvidersPending() {
		a.runtimeErrKind = runtimeNoticeConnectionRequired
	}
}

func readConnectionStartup(storePath string) connectionStartup {
	startup := connectionStartup{Path: connectionConfigPath(storePath)}
	private, err := connection.ReadPrivateConfig(startup.Path)
	if err == nil {
		if private.Version == 3 && len(private.Accounts) > 0 && private.CredentialSource == "" {
			return startup
		}
		config, defaultsErr := private.Defaults()
		if defaultsErr == nil {
			if fields := validateConnectionConfig(config); len(fields) == 0 {
				startup.Config = &config
			} else {
				startup.LoadErr = fmt.Errorf("invalid connection config %s: %s", startup.Path, connectionValidationSummary(fields))
			}
			return startup
		}
		err = defaultsErr
	}
	if errors.Is(err, os.ErrNotExist) {
		return startup
	}
	startup.LoadErr = err
	return startup
}

func contactsFromStore(store *contactStore) []*contact {
	contacts := make([]*contact, 0, len(store.State.Contacts))
	for _, stored := range store.State.Contacts {
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
		ct.Activities = activitiesForStoredContact(store.State.Issues, ct.ID)
		if stored.SelectedIssueID != "" {
			ct.SelectedActivityID = stored.SelectedIssueID
		}
		if activity := ct.selectedActivity(); activity != nil {
			ct.SelectedActivityID = activity.ID
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
	a.activityToolLedger = store.IssueToolLedger
	a.activityToolScopes = store.IssueToolScopes
	a.contacts = contactsFromStore(store)
	a.selected = 0
	for i, ct := range a.contacts {
		if ct.ID == store.State.Selected {
			a.selected = i
			break
		}
	}
	if len(a.contacts) == 0 {
		a.contacts = []*contact{placeholderContact(a.workDir)}
		a.selected = 0
	}
	if layout := store.State.Layout; layout != nil {
		a.leftPaneWidth = clampPaneWidth(layout.LeftWidth, defaultLeftPaneWidth, minLeftPaneWidth, maxLeftPaneWidth)
		a.rightPaneWidth = clampPaneWidth(layout.RightWidth, defaultRightPaneWidth, minRightPaneWidth, maxRightPaneWidth)
	} else {
		a.leftPaneWidth = 0
		a.rightPaneWidth = 0
	}
	a.workspaceOpen = false
	a.detail = ""
	a.appearanceMode = normalizeAppearanceMode(store.State.Appearance)
	a.appearanceThemeIDs = appearanceThemeIDs{}
	if store.State.ThemeIDs != nil {
		a.appearanceThemeIDs = *store.State.ThemeIDs
	}
}

func (a *appModel) runOnUI(fn func()) {
	done := make(chan struct{})
	a.updateUI(func() {
		fn()
		close(done)
	})
	<-done
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
	if editor := a.providers.editor; editor != nil {
		if key := strings.TrimSpace(editor.APIKey); key != "" {
			keys = append(keys, key)
		}
	}
	keys = a.appendDraftMcpSecrets(keys)
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
		if (a.dataRecovery != nil && a.dataRecovery.inFlight) || a.providerBusy() || a.capabilitiesBusy() || a.submissionInFlight > 0 || a.contactHistoryInFlight > 0 {
			return
		}
		a.shutdownDrainStarted = true
		a.runtimeStatus = runtimeStateDraining
		go a.finishProductShutdown(host)
		return
	}
	if (a.dataRecovery != nil && a.dataRecovery.inFlight) || a.contactHistoryInFlight > 0 || a.submissionInFlight > 0 || (a.themeImport != nil && a.themeImport.installInFlight) {
		return
	}
	a.quitReady = true
	if a.requestQuit != nil {
		a.requestQuit()
	}
}
