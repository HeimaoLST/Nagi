package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func isolateConnectionApp(t *testing.T, root, payload string) {
	t.Helper()
	if payload == "" {
		payload = filepath.Join(root, "missing-pi-payload")
	}
	t.Setenv("NAGI_GUI_STORE", filepath.Join(root, "gui", "contacts.json"))
	t.Setenv("NAGI_GUI_WORKDIR", root)
	t.Setenv("NAGI_PI_PAYLOAD", payload)
}

func TestConnectionFirstUseDoesNotPersistWrongDefaultWorkspace(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.connectionForm == nil || !a.settingsOpen || a.settingsTab != settingsTabConnection {
		t.Fatalf("an empty profile must open the connection settings page: form=%v open=%v tab=%q", a.connectionForm != nil, a.settingsOpen, a.settingsTab)
	}
	if _, err := os.Stat(guiStorePath()); !os.IsNotExist(err) {
		t.Fatal("first use persisted a contact before the user selected a working directory")
	}
	if a.pi != nil {
		t.Fatal("first use started a worker before configuration")
	}
}

func TestConnectionSavedDefaultsDoNotRewriteExistingContact(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	oldWorkspace := t.TempDir()
	store, err := openContactStore(guiStorePath(), oldWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	store.state.Contacts[0].Draft = "retain the existing draft"
	if err := store.saveState(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(guiStorePath())
	if err != nil {
		t.Fatal(err)
	}
	config := testConnectionConfig(t)
	if err := saveConnectionConfig(filepath.Join(filepath.Dir(guiStorePath()), "connection.json"), config); err != nil {
		t.Fatal(err)
	}
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.workDir != config.WorkingDirectory || a.contacts[0].Workspace != oldWorkspace || a.contacts[0].Draft != "retain the existing draft" {
		t.Fatal("loading defaults changed an existing contact instead of retaining its workspace and draft")
	}
	after, err := os.ReadFile(guiStorePath())
	if err != nil || string(after) != string(before) {
		t.Fatal("reading settings rewrote the contact store")
	}
}

func TestConnectionInvalidSavedSourceRemainsRepairable(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	config := testConnectionConfig(t)
	path := filepath.Join(filepath.Dir(guiStorePath()), "connection.json")
	if err := saveConnectionConfig(path, config); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(config.WorkingDirectory); err != nil {
		t.Fatal(err)
	}
	t.Setenv("API_KEY", "external-fixture-key")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.connectionForm == nil || a.connectionForm.Draft.Model != config.Model || a.connectionForm.Draft.WorkingDirectory != config.WorkingDirectory || a.connectionForm.Error == "" {
		t.Fatal("invalid saved settings must open for correction with their previous values")
	}
	if a.pi != nil || !a.settingsOpen || a.settingsTab != settingsTabConnection {
		t.Fatalf("invalid GUI configuration did not open the repair page: pi=%v open=%v tab=%q", a.pi != nil, a.settingsOpen, a.settingsTab)
	}
}

type folderChoiceReply struct {
	paths []string
	err   error
}

// Only the native chooser is replaced; its worker still returns through updateUI.
func controlledFolderChoice(t *testing.T, a *appModel) (<-chan mygo.OpenDialogOptions, chan<- folderChoiceReply, <-chan func()) {
	t.Helper()
	requests := make(chan mygo.OpenDialogOptions, 4)
	replies := make(chan folderChoiceReply, 4)
	updates := make(chan func(), 4)
	a.chooseFolder = func(opts mygo.OpenDialogOptions) ([]string, error) {
		requests <- opts
		reply := <-replies
		return reply.paths, reply.err
	}
	a.setUIHook(func(fn func()) { updates <- fn })
	t.Cleanup(func() { close(replies) })
	return requests, replies, updates
}

func awaitFolderChoice[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("folder chooser did not reach its controlled boundary")
		var zero T
		return zero
	}
}

func TestConnectionFolderChooserDraftCancelErrorAndRetry(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.uiLocale = "en"
	config := testConnectionConfig(t)
	a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
	a.connectionSaved, a.connectionEffective = &config, &config
	a.workDir, ct.Workspace = config.WorkingDirectory, config.WorkingDirectory
	a.win = &mygo.Window{}
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	form := a.connectionForm
	form.Draft.Model = "unsaved-model"
	form.Error, form.Status = "previous save error", connStatusSavedApplied
	form.Errors = map[string]string{"WorkingDirectory": connectionValidationWorkspaceDirectory, "Model": connectionValidationRequired}
	form.folderError = "previous chooser error"
	before := form.Draft
	requests, replies, updates := controlledFolderChoice(t, a)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	label := locales.In("en").WorkFolderChoose()
	if err := clickSettingsAction(tr, label); err != nil {
		t.Fatal(err)
	}
	opts := awaitFolderChoice(t, requests)
	if opts.Parent != a.win || opts.DefaultPath != config.WorkingDirectory || !form.Busy {
		t.Fatalf("chooser did not use the current folder/window: %+v", opts)
	}
	replies <- folderChoiceReply{}
	awaitFolderChoice(t, updates)()
	tr.Frame()
	if form.Draft != before || form.Error != "previous save error" || form.Status != connStatusSavedApplied || form.folderError != "previous chooser error" || len(form.Errors) != 2 || !tr.Focused(label) {
		t.Fatal("cancel changed draft/error state or lost the initiating control focus")
	}
	missing := filepath.Join(t.TempDir(), "removed")
	for _, reply := range []folderChoiceReply{{err: errors.New("dialog unavailable")}, {paths: []string{missing}}} {
		a.chooseConnectionWorkFolder()
		awaitFolderChoice(t, requests)
		replies <- reply
		awaitFolderChoice(t, updates)()
		if form.Draft != before || form.folderError == "" || form.Busy || form.Error != "previous save error" {
			t.Fatal("chooser failure lost edits or did not permit retry")
		}
	}
	chosen := filepath.Join(t.TempDir(), "工作 notes")
	if err := os.Mkdir(chosen, 0o700); err != nil {
		t.Fatal(err)
	}
	a.chooseConnectionWorkFolder()
	awaitFolderChoice(t, requests)
	replies <- folderChoiceReply{paths: []string{chosen}}
	apply := awaitFolderChoice(t, updates)
	if form.Draft != before {
		t.Fatal("the chooser worker edited state before the UI callback")
	}
	apply()
	if form.Draft.WorkingDirectory != chosen || form.Draft.Model != before.Model || form.folderError != "" || form.Errors["WorkingDirectory"] != "" || form.Errors["Model"] == "" {
		t.Fatalf("retry did not update only the folder draft: %+v", form)
	}
	if config.WorkingDirectory == chosen || ct.Workspace == chosen || a.workDir == chosen || ct.SessionID != "s1" || a.connectionApplying || a.connectionTesting || a.pi != nil {
		t.Fatal("folder choice saved/applied configuration or retargeted existing work")
	}
}

func TestConnectionFolderChooserRejectsLateTargets(t *testing.T) {
	for _, change := range []string{"editor", "tab-roundtrip", "close-roundtrip", "shutdown"} {
		t.Run(change, func(t *testing.T) {
			a, _ := workspaceTestApp(t)
			config := testConnectionConfig(t)
			a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
			a.openSettingsTab(settingsTabConnection, settingsSourceRail)
			form := a.connectionForm
			requests, replies, updates := controlledFolderChoice(t, a)
			a.chooseConnectionWorkFolder()
			awaitFolderChoice(t, requests)
			switch change {
			case "editor":
				a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
			case "tab-roundtrip":
				a.selectSettingsTab(settingsTabAppearance)
				a.selectSettingsTab(settingsTabConnection)
			case "close-roundtrip":
				a.closeSettings()
				a.openSettingsTab(settingsTabConnection, settingsSourceRail)
			case "shutdown":
				a.shutdownStarted = true
			}
			replies <- folderChoiceReply{paths: []string{t.TempDir()}}
			awaitFolderChoice(t, updates)()
			if form.Draft.WorkingDirectory != config.WorkingDirectory || form.focusFolder || form.Busy || a.connectionForm.Draft.WorkingDirectory != config.WorkingDirectory {
				t.Fatal("late result changed or focused a stale Settings editor")
			}
		})
	}
}

func TestConnectionFolderChooserInitialFolderAndInvalidSelection(t *testing.T) {
	valid := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ draft, effective, want string }{
		{valid, t.TempDir(), valid},
		{"relative", valid, valid},
		{filepath.Join(valid, "missing"), "relative", home},
	} {
		a, _ := workspaceTestApp(t)
		a.workDir = tc.effective
		a.connectionForm = newConnectionSettings(nil, tc.draft)
		a.openSettingsTab(settingsTabConnection, settingsSourceRail)
		requests, replies, updates := controlledFolderChoice(t, a)
		a.chooseConnectionWorkFolder()
		if got := awaitFolderChoice(t, requests).DefaultPath; got != tc.want {
			t.Fatalf("initial folder = %q, want %q", got, tc.want)
		}
		file := filepath.Join(t.TempDir(), "not-a-folder")
		if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		replies <- folderChoiceReply{paths: []string{file}}
		awaitFolderChoice(t, updates)()
		if a.connectionForm.Draft.WorkingDirectory != tc.draft || a.connectionForm.folderError == "" {
			t.Fatal("a non-directory choice replaced the folder draft")
		}
	}
}

func TestFirstUseWorkFolderRequiresExplicitChoiceBeforeActions(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, filepath.Join(root, "unused-runtime"))
	t.Setenv("NAGI_GUI_WORKDIR", "")
	t.Chdir(root)
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	ct := a.selectedContact()
	ct.Workspace = t.TempDir()
	originalID, originalFolder := ct.ID, ct.Workspace
	ct.Draft = "Keep local draft"
	a.contacts = append(a.contacts, &contact{ID: "inherited-contact", Name: "Inherited", Role: "Assistant"})
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	form := a.connectionForm
	fixture := testConnectionConfig(t)
	form.Draft.Endpoint, form.Draft.APIKey, form.Draft.Model = fixture.Endpoint, fixture.APIKey, fixture.Model
	requests, replies, updates := controlledFolderChoice(t, a)
	var saved, tested, applied []connectionConfig
	actions := connectionSettingsActions{
		Save:         func(config connectionConfig) { saved = append(saved, config) },
		Test:         func(config connectionConfig) { tested = append(tested, config) },
		SaveAndApply: func(config connectionConfig) { applied = append(applied, config) },
		ChooseFolder: a.chooseConnectionWorkFolder,
	}
	tr := connectionSettingsTester(form, actions, 580, 1800)
	tr.Frame()
	assertBlocked := func() {
		t.Helper()
		for _, action := range []string{"Save configuration", "Test connection and model", "Save and apply"} {
			if err := tr.Click(action); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
		}
		if len(saved)+len(tested)+len(applied) != 0 {
			t.Fatalf("fresh GUI silently accepted launch folder %q", form.Draft.WorkingDirectory)
		}
		if form.Errors["WorkingDirectory"] == "" || a.pi != nil || a.connectionProbe != nil || a.connectionApplying || a.connectionTesting {
			t.Fatal("unconfirmed work folder did not block GUI actions")
		}
	}
	assertBlocked()
	for _, reply := range []folderChoiceReply{{}, {err: errors.New("folder access unavailable")}} {
		if err := tr.Click("Choose folder…"); err != nil {
			t.Fatal(err)
		}
		opts := awaitFolderChoice(t, requests)
		home, err := os.UserHomeDir()
		if err != nil || opts.DefaultPath != home {
			t.Fatalf("fresh chooser suggestion = %q, want home %q: %v", opts.DefaultPath, home, err)
		}
		replies <- reply
		awaitFolderChoice(t, updates)()
		tr.Frame()
		assertBlocked()
	}
	chosen := t.TempDir()
	if err := tr.Click("Choose folder…"); err != nil {
		t.Fatal(err)
	}
	awaitFolderChoice(t, requests)
	replies <- folderChoiceReply{paths: []string{chosen}}
	awaitFolderChoice(t, updates)()
	tr.Frame()
	for _, action := range []string{"Save configuration", "Test connection and model", "Save and apply"} {
		if err := tr.Click(action); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
	}
	for _, configs := range [][]connectionConfig{saved, tested, applied} {
		if len(configs) != 1 || configs[0].WorkingDirectory != chosen {
			t.Fatal("the confirmed folder did not reach a connection action")
		}
	}
	if a.selectedContact() != ct || ct.ID != originalID || ct.Workspace != originalFolder || ct.Draft != "Keep local draft" || a.contactByID("inherited-contact").Workspace != "" {
		t.Fatal("choosing the default folder changed an existing contact, draft or workspace override")
	}
}

func TestFirstUseWorkFolderRetainsSavedChoice(t *testing.T) {
	for _, source := range []string{"saved", "invalid-saved"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			isolateConnectionApp(t, root, "")
			config := testConnectionConfig(t)
			if err := saveConnectionConfig(connectionConfigPath(guiStorePath()), config); err != nil {
				t.Fatal(err)
			}
			if source == "invalid-saved" {
				if err := os.Remove(config.WorkingDirectory); err != nil {
					t.Fatal(err)
				}
			}
			a, err := newRuntimeApp()
			if err != nil {
				t.Fatal(err)
			}
			a.openSettingsTab(settingsTabConnection, settingsSourceRail)
			if a.connectionForm.Draft.WorkingDirectory != config.WorkingDirectory || a.pi != nil || a.connectionProbe != nil || a.connectionApplying || a.connectionTesting {
				t.Fatalf("%s folder choice was cleared or started Pi: %+v", source, a.connectionForm.Draft)
			}
		})
	}
}

func TestC8RecoveryOwnedCompletionDrainsQuit(t *testing.T) {
	for _, restore := range []bool{false, true} {
		for _, leave := range []bool{false, true} {
			t.Run(fmt.Sprintf("restore-%t/leave-%t", restore, leave), func(t *testing.T) {
				a := c8SavedApp(t)
				if restore {
					a = c8UnavailableApp(t)
				}
				req, reply, updates := c8ControlledPicker(t, a)
				source := filepath.Join(t.TempDir(), "copy.json")
				recoveryWriteState(t, source, recoveryProducedState(t))
				a.chooseDataRecovery(restore)
				awaitFolderChoice(t, req)
				selection := t.TempDir()
				if restore {
					selection = source
				}
				reply <- folderChoiceReply{paths: []string{selection}}
				awaitFolderChoice(t, updates)()
				completion := awaitFolderChoice(t, updates)
				if restore {
					completion()
					a.confirmDataRecovery()
					completion = awaitFolderChoice(t, updates)
				}
				// File I/O is finished and its owned final UI result remains queued.
				if leave {
					a.closeSettings()
				}
				ct := a.selectedContact()
				ct.Draft = "intervening live edit"
				quitCount := 0
				a.requestQuit = func() { quitCount++ }
				a.beginShutdown()
				a.beginShutdown()
				a.finishShutdown()
				if a.quitReady || quitCount != 0 {
					t.Fatal("Quit escaped owned recovery completion")
				}
				completion()
				if !a.quitReady || quitCount != 1 || a.dataRecovery.inFlight || ct.Draft != "intervening live edit" || a.pi != nil {
					t.Fatal("final recovery result did not release exactly once and preserve live edits")
				}
				if restore {
					if a.dataRecovery.result.Outcome != storeRecoveryVerifiedInstalled || a.dataRecovery.result.ArchivePath == "" || a.store != nil || a.storeInitAllowed {
						t.Fatal("shutdown lost the terminal restore result or activated fallback")
					}
				} else if a.dataRecovery.output == "" {
					t.Fatal("shutdown lost completed copy location")
				}
			})
		}
	}
}

func TestC8RecoveryHostAndThemeMutualAdmission(t *testing.T) {
	for _, phase := range []string{"chooser", "preview", "installed"} {
		t.Run(phase, func(t *testing.T) {
			a := c8UnavailableApp(t)
			req, reply, updates := c8ControlledPicker(t, a)
			source := filepath.Join(t.TempDir(), "copy.json")
			recoveryWriteState(t, source, recoveryProducedState(t))
			a.chooseDataRecovery(true)
			awaitFolderChoice(t, req)
			if phase != "chooser" {
				reply <- folderChoiceReply{paths: []string{source}}
				awaitFolderChoice(t, updates)()
				awaitFolderChoice(t, updates)()
				if phase == "installed" {
					a.confirmDataRecovery()
					awaitFolderChoice(t, updates)()
				}
			}
			before, form := recoveryRecordSnapshot(t, a.selectedContact()), *a.connectionForm
			a.chooseThemeFile = func(mygo.OpenDialogOptions) ([]string, error) {
				t.Error("recovery ownership admitted a theme dialog")
				return nil, nil
			}
			config := defaultConnectionConfig("")
			a.saveConnectionWithApply(config, true)
			a.testConnection(config)
			a.applyConnection()
			a.importAppearanceTheme()
			if a.connectionForm.Draft != form.Draft || a.connectionForm.Error != form.Error || a.connectionForm.Status != form.Status || a.connectionForm.ReloadBlocked != form.ReloadBlocked || a.connectionSaveInFlight || a.connectionApplying || a.connectionTesting || a.pi != nil || a.themeImport != nil || recoveryRecordSnapshot(t, a.selectedContact()) != before {
				t.Fatal("recovery ownership changed connection/theme/work through an actual entry method")
			}
			if _, err := os.Stat(connectionConfigPath(a.storePath)); !os.IsNotExist(err) {
				t.Fatal("recovery admission wrote connection settings")
			}
			if phase == "chooser" {
				reply <- folderChoiceReply{}
				awaitFolderChoice(t, updates)()
			}
		})
	}
	a := c8SavedApp(t)
	req, reply, updates := c8ControlledPicker(t, a)
	a.chooseThemeFile = a.chooseRecoveryFile
	a.importAppearanceTheme()
	awaitFolderChoice(t, req)
	a.chooseDataRecovery(false)
	if a.dataRecovery != nil && (a.dataRecovery.choosing || a.dataRecovery.inFlight) {
		t.Fatal("pending theme chooser admitted recovery")
	}
	reply <- folderChoiceReply{}
	awaitFolderChoice(t, updates)()
}

func TestC8FailedRecoveryCompletionReleasesQuit(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore-%t", restore), func(t *testing.T) {
			a := c8SavedApp(t)
			if restore {
				a = c8UnavailableApp(t)
			}
			req, reply, updates := c8ControlledPicker(t, a)
			source := filepath.Join(t.TempDir(), "compatible.json")
			recoveryWriteState(t, source, recoveryProducedState(t))
			selection := filepath.Join(t.TempDir(), "not-a-folder")
			if err := os.WriteFile(selection, []byte("existing file"), 0600); err != nil {
				t.Fatal(err)
			}
			a.chooseDataRecovery(restore)
			awaitFolderChoice(t, req)
			if restore {
				selection = source
			}
			reply <- folderChoiceReply{paths: []string{selection}}
			awaitFolderChoice(t, updates)()
			completion := awaitFolderChoice(t, updates)
			if restore {
				completion()
				if err := os.WriteFile(a.storePath, []byte("{changed original"), 0600); err != nil {
					t.Fatal(err)
				}
				a.confirmDataRecovery()
				completion = awaitFolderChoice(t, updates)
			}
			quit := 0
			a.requestQuit = func() { quit++ }
			a.beginShutdown()
			if a.quitReady || quit != 0 {
				t.Fatal("quit escaped failed I/O completion")
			}
			completion()
			if !a.quitReady || quit != 1 || a.dataRecovery.inFlight || a.dataRecovery.problem == "" {
				t.Fatal("failed completion did not preserve error and release quit")
			}
			if restore && a.dataRecovery.result.Outcome != storeRecoveryNotInstalled {
				t.Fatal("failure outcome lost during shutdown")
			}
		})
	}
}

func TestC8RestoreAdmissionPreservesUnavailableEditsAndLocalRetry(t *testing.T) {
	for _, kind := range []string{"loaded", "local-save", "profile", "connection", "context", "contact-draft", "history", "submission", "connection-test", "contact-picker"} {
		t.Run(kind, func(t *testing.T) {
			a := c8UnavailableApp(t)
			if kind == "loaded" || kind == "local-save" {
				a = c8SavedApp(t)
			}
			switch kind {
			case "local-save":
				a.localSaveErr = "existing write failure"
			case "profile":
				a.openContactProfile(a.selectedID())
				a.profileEditorFor(a.selectedID()).Draft.Name = "keep profile"
			case "connection":
				a.connectionForm.Draft.Model = "keep model"
			case "context":
				a.selectedContact().Context.formText = "keep snippet"
			case "contact-draft":
				a.selectedContact().Draft = "keep draft"
			case "history":
				a.contactHistoryInFlight = 1
			case "submission":
				a.submissionInFlight = 1
			case "connection-test":
				a.connectionTesting = true
			case "contact-picker":
				a.openContactProfile(a.selectedID())
				a.profileEditorFor(a.selectedID()).Busy = true
			}
			original, record := recoverySavedBytes(t, a.storePath), recoveryRecordSnapshot(t, a.selectedContact())
			a.chooseRecoveryFile = func(mygo.OpenDialogOptions) ([]string, error) {
				t.Error("inadmissible restore opened a picker")
				return nil, nil
			}
			a.chooseDataRecovery(true)
			if a.dataRecovery != nil && (a.dataRecovery.choosing || a.dataRecovery.inFlight) || string(recoverySavedBytes(t, a.storePath)) != string(original) || recoveryRecordSnapshot(t, a.selectedContact()) != record || a.pi != nil {
				t.Fatal("rejected restore lost original/work or started an operation")
			}
			if kind == "local-save" {
				if err := a.persist(); err != nil || a.localSaveErr != "" {
					t.Fatal("restore presentation blocked existing local-only Retry")
				}
			}
		})
	}
}
