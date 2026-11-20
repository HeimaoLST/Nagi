package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
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

func TestLocalStateFirstWriteFailureRetainsDraftAndRetries(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	id := a.selectedID()
	blocker := filepath.Dir(a.storePath)
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	ct := a.selectedContact()
	ct.Draft = "Keep the failed local draft"
	a.draftChanged(ct)
	if a.localSaveErr == "" || a.runtimeErrKind != runtimeNoticeStoreSave || ct.Draft != "Keep the failed local draft" {
		t.Fatal("first-save failure was silent or discarded the draft")
	}
	a.closeSettings()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	msgs := locales.In("en")
	if !tr.HasText(msgs.RecoveryLocalSaveFailed()) || !tr.HasText(msgs.SettingsRetrySave()) || !tr.HasText(msgs.ConnectionStatusSetupNeeded()) {
		t.Fatalf("pre-connection save failure/retry or connection guidance was hidden: %v", tr.Texts())
	}
	if tr.HasText(msgs.RuntimeErrorStoreSave(a.localSaveErr)) {
		t.Fatal("raw local diagnostic shown before disclosure")
	}
	if err := tr.Click(msgs.DetailsAction()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText(safeDiagnostic(a.localSaveErr)) {
		t.Fatal("local diagnostic inaccessible after disclosure")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := tr.Click(msgs.SettingsRetrySave()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.localSaveErr != "" || a.runtimeErrKind != runtimeNoticeConnectionRequired || !tr.HasText(msgs.ConnectionStatusSetupNeeded()) {
		t.Fatalf("local Retry save did not recover independently of connection: error=%q kind=%q", a.localSaveErr, a.runtimeErrKind)
	}
	restarted, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if restarted.selectedID() != id || restarted.selectedContact().Draft != ct.Draft || a.pi != nil || restarted.pi != nil {
		t.Fatal("retry lost local state or started Runtime")
	}
}

func TestLocalStateReadFailuresNeverInitializeReplacement(t *testing.T) {
	for _, kind := range []string{"decode", "read", "stat"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			isolateConnectionApp(t, root, "")
			path := guiStorePath()
			original := []byte("unreadable local state")
			if kind == "stat" {
				path = filepath.Dir(path)
			} else if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if kind == "read" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := newRuntimeApp(); err == nil {
				t.Fatal("existing state error was treated as initial absence")
			} else {
				a := newUnavailableApp(err)
				if saveErr := a.persist(); saveErr == nil || a.store != nil || a.storeInitAllowed {
					t.Fatal("unavailable local store claimed a successful save or initialized a replacement")
				}
			}
			if kind == "read" {
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatal("unreadable store directory changed")
				}
			} else if data, err := os.ReadFile(path); err != nil || string(data) != string(original) {
				t.Fatal("failed local-state read overwrote original bytes")
			}
		})
	}
}

func TestLocalStateAppearingBeforeFirstSaveIsNotOverwritten(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(a.storePath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("existing state appeared after startup")
	if err := os.WriteFile(a.storePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.persist(); err == nil || a.store != nil {
		t.Fatal("first Save ignored an existing store")
	}
	if data, err := os.ReadFile(a.storePath); err != nil || string(data) != string(original) {
		t.Fatal("first Save replaced existing state")
	}
}

func testRuntimeModel(t *testing.T) (*appModel, *contact, *contact) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	bound := &contact{
		ID: "bound", Name: "Nagi", Role: "Assistant",
		Purpose: "old purpose", Instructions: "old instructions", Workspace: "/old",
		SessionID: "s1", Loaded: true, Draft: "keep me", Status: statusReady,
		PiBinding: recoveryConversationBinding("bound", "/old", "s1"),
		Messages:  []*message{{ID: 1, Key: "hist:s1:0:0", Kind: msgUser, Text: "old message"}},
		History:   []historyEntry{{ID: "hist:s1:0:0", Text: "old message"}},
	}
	unbound := &contact{ID: "new", Name: "New contact 1", Role: "Assistant", Workspace: "/tmp", Status: statusReady}
	a := &appModel{
		store: store, runtimeStatus: runtimeStateStarting,
		contacts: []*contact{bound, unbound}, selected: 0,
	}
	if err := store.save(a.contacts, a.selectedID()); err != nil {
		t.Fatal(err)
	}
	return a, bound, unbound
}

func presentationPiHost(a *appModel) *piAppHost {
	return &piAppHost{generation: a.connectionGeneration}
}

func TestProfileEditsFutureDefaultsWithoutReplacingCurrentSession(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	workspace := t.TempDir()
	bound.SessionWorkingDir = "/old"
	a.openContactProfile(bound.ID)
	if !a.saveContactProfileDraftFor(bound.ID, profileDraft{
		Name: "Renamed", Purpose: "new purpose", Instructions: "new instructions",
		Workspace: workspace,
	}, false) {
		t.Fatalf("save failed: %+v", a.profileEditorFor(bound.ID))
	}
	if bound.Name != "Renamed" || bound.Purpose != "new purpose" || bound.Instructions != "new instructions" || bound.Workspace != workspace {
		t.Fatalf("future profile = %+v", bound)
	}
	if bound.SessionID != "s1" || bound.SessionWorkingDir != "/old" || bound.Draft != "keep me" || len(bound.Messages) != 1 || len(bound.History) != 1 {
		t.Fatalf("current session snapshot changed: %+v", bound)
	}
	reloaded, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	stored := reloaded.state.Contacts[0]
	if stored.Name != "Renamed" || stored.Purpose != "new purpose" || stored.Workspace != workspace || stored.SessionID != "s1" {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestSettingsUnboundProfileIsUsedBeforeFirstSession(t *testing.T) {
	a, _, unbound := testRuntimeModel(t)
	workspace := t.TempDir()
	a.selected = 1
	a.openContactProfile(unbound.ID)
	if !a.saveContactProfileDraftFor(unbound.ID, profileDraft{Name: unbound.Name, Purpose: "p", Instructions: "i", Workspace: workspace}, false) {
		t.Fatalf("save failed: %+v", a.profileEditorFor(unbound.ID))
	}
	if unbound.Purpose != "p" || unbound.Instructions != "i" || unbound.Workspace != workspace {
		t.Fatalf("profile = %+v", unbound)
	}
	if unbound.SessionID != "" {
		t.Fatal("saving settings must not create a session")
	}
}

func TestSettingsDuringFirstCreateKeepsSubmittedSnapshot(t *testing.T) {
	a, _, unbound := testRuntimeModel(t)
	a.selected = 1
	unbound.SendState = sendSubmitting
	unbound.SubmittedDraft = "submitted snapshot"
	unbound.pendingSend = &pendingSend{Draft: "submitted snapshot", State: "submitting"}
	a.openContactProfile(unbound.ID)
	if !a.saveContactProfileDraftFor(unbound.ID, profileDraft{Name: "Still Editable", Purpose: "later edit", Workspace: unbound.Workspace}, false) {
		t.Fatalf("save failed: %+v", a.profileEditorFor(unbound.ID))
	}
	if unbound.Name != "Still Editable" || unbound.Purpose != "later edit" {
		t.Fatalf("future profile was not saved: %+v", unbound)
	}
	if unbound.SubmittedDraft != "submitted snapshot" || unbound.pendingSend == nil || unbound.pendingSend.Draft != "submitted snapshot" {
		t.Fatalf("submitted request snapshot changed: %+v", unbound.pendingSend)
	}
}

func piRunStartedTestApp(t *testing.T) (*appModel, *contact, pi.Event) {
	t.Helper()
	root := t.TempDir()
	store, err := openContactStore(filepath.Join(root, "contacts.json"), root)
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{
		store: store, contacts: contactsFromStore(store), selected: 0,
		runtimeStatus: runtimeStateReady, connectionGeneration: 1,
		pi: &piAppHost{generation: 1},
	}
	ct := a.selectedContact()
	ct.PiBinding = &piConversationBinding{
		Engine: "pi",
		Context: pi.ContextBinding{
			ContextID: "conversation-" + ct.ID, BotID: ct.ID, Purpose: pi.Conversation,
			SelectionRevision: 1, CWD: root, SessionRef: filepath.Join(root, "session.jsonl"), Used: true,
		},
		Selection: piSelection{CWD: root, CodingTools: []string{}},
		Configuration: piConfiguration{
			WireAPI: pi.OpenAICompletions, BaseURL: "http://127.0.0.1:1/v1", Model: "reducer-fixture",
			ContextTokens: 65536, OutputTokens: 4096, CredentialSource: "reducer-fixture",
		},
	}
	ct.SessionID, ct.Loaded = ct.PiBinding.Context.SessionRef, true
	a.bindTaskContexts()
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	event := pi.Event{
		Version: 1, Type: "event", WorkerEpoch: "reducer-fixture", ContextID: ct.PiBinding.Context.ContextID,
		RunID: "run-1", Seq: 1, Name: pi.RunStarted,
	}
	ct.ActiveTaskID, ct.SendState = event.RunID, sendSubmitting
	return a, ct, event
}

func applyTestProductEvent(t *testing.T, a *appModel, name string, event pi.ProductExecutionEvent) {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	a.applyProductEvent(a.pi, pi.ProductEvent{Name: name, Data: data})
}

func TestBeginShutdownIsIdempotentWithoutRuntime(t *testing.T) {
	a, _, _ := testRuntimeModel(t)
	quit := make(chan struct{}, 2)
	a.requestQuit = func() { quit <- struct{}{} }
	a.beginShutdown()
	if !a.shutdownStarted || !a.quitReady {
		t.Fatalf("state started=%v ready=%v", a.shutdownStarted, a.quitReady)
	}
	select {
	case <-quit:
	case <-time.After(time.Second):
		t.Fatal("quit was not requested")
	}
	a.beginShutdown()
	select {
	case <-quit:
		t.Fatal("shutdown ran twice")
	default:
	}
}

func TestReloadSelectedRequiresLiveRuntime(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	a.runtimeStatus = runtimeStateExited
	a.reloadSelected()
	if bound.SendError != "Conversation history is unavailable for this Bot or connection." || a.contactHistoryInFlight != 0 {
		t.Fatalf("disconnected reload message = %q", bound.SendError)
	}
}

func TestCanSendRejectsDisconnectedOrDrainingRuntime(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	a.pi = presentationPiHost(a)
	a.runtimeStatus = runtimeStateDraining
	if reason := a.canSend(bound); reason != blockRuntimeNotReady {
		t.Fatalf("draining runtime send guard = %q", reason)
	}
}
