package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func isolateConnectionApp(t *testing.T, root, binary string) {
	t.Helper()
	for key := range filteredConnectionEnv {
		t.Setenv(key, "")
	}
	t.Setenv("SAKI_GUI_STORE", filepath.Join(root, "gui", "contacts.json"))
	t.Setenv("SAKI_GUI_WORKDIR", root)
	t.Setenv("SAKI_SESSION_DIR", filepath.Join(root, "sessions"))
	t.Setenv("SAKI_RUNTIME_BIN", binary)
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
	if a.rt != nil {
		t.Fatal("first use started an unconfigured helper")
	}
}

func TestConnectionLateReadyDoesNotReopenDrainingApp(t *testing.T) {
	a := &appModel{rt: &runtimeClient{}, runtimeStatus: runtimeStateDraining, shutdownStarted: true}
	a.handleRuntimeReady(0, runtimeReady{API: connectionAPIResponses})
	if a.runtimeStatus != runtimeStateDraining || a.runtimeHandshake {
		t.Fatal("a late ready notification reopened the app during quit")
	}
}

func TestConnectionStoreCreationFailureCanBeRepaired(t *testing.T) {
	isolateConnectionApp(t, t.TempDir(), "")
	h := openConnectionHarness(t)
	if err := os.MkdirAll(guiStorePath(), 0o700); err != nil {
		t.Fatal(err)
	}
	config := testConnectionConfig(t)
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&config, config.WorkingDirectory).Draft
		h.app.saveConnection(config)
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.connectionForm.Error == "" || h.app.connectionReloadBlocked() == "" {
			t.Error("a failed contact store did not stop Apply")
		}
		h.app.applyConnection()
		if h.app.connectionApplying || h.app.rt != nil {
			t.Error("Apply started work without a usable contact store")
		}
	})
	if _, err := loadConnectionConfig(connectionConfigPath(guiStorePath())); err != nil {
		t.Fatal("the successfully saved configuration was lost")
	}
	if err := os.Remove(guiStorePath()); err != nil {
		t.Fatal(err)
	}
	var clickErr error
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&config, config.WorkingDirectory).Draft
		h.app.openSettingsTab(settingsTabConnection, settingsSourceRail)
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.Frame()
		clickErr = clickSettingsAction(tr, "Save configuration")
	})
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.store == nil || h.app.connectionReloadBlocked() != "" {
			t.Errorf("GUI Save did not recover: storeNil=%v blocked=%q formErr=%q", h.app.store == nil, h.app.connectionReloadBlocked(), h.app.connectionForm.Error)
		}
	})
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
	if a.rt != nil || !a.settingsOpen || a.settingsTab != settingsTabConnection {
		t.Fatalf("invalid GUI configuration did not open the repair page: rt=%v open=%v tab=%q", a.rt, a.settingsOpen, a.settingsTab)
	}
}

func TestConnectionSaveAndApplySaveFailureDoesNotApply(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	blocked := filepath.Join(root, "blocked")
	if err := os.Mkdir(blocked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	h.pump.do(func() {
		h.app.connectionPath = filepath.Join(blocked, "connection.json")
		h.app.connectionForm.Draft = newConnectionSettings(&config, config.WorkingDirectory).Draft
		h.app.saveAndApplyConnection(config)
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	var saved *connectionConfig
	var applying bool
	var rt *runtimeClient
	var formError, errorDetail, draftModel, status string
	var reloadBlocked string
	h.pump.do(func() {
		saved, applying, rt = h.app.connectionSaved, h.app.connectionApplying, h.app.rt
		formError, errorDetail, draftModel = h.app.connectionForm.Error, h.app.connectionForm.ErrorDetail, h.app.connectionForm.Draft.Model
		status, reloadBlocked = h.app.connectionForm.Status, h.app.connectionForm.ReloadBlocked
	})
	if saved != nil || applying || rt != nil {
		t.Fatal("save failure reached apply or committed saved state")
	}
	if formError == "" || errorDetail == "" || !strings.Contains(errorDetail, "permission") && !strings.Contains(errorDetail, "operation not permitted") {
		t.Fatalf("save failure detail = error:%q detail:%q", formError, errorDetail)
	}
	if draftModel != config.Model {
		t.Fatal("save failure lost the edited draft")
	}
	if status != "" || reloadBlocked != "" {
		t.Fatalf("save failure duplicated state: status=%q blocked=%q", status, reloadBlocked)
	}
	h.shutdown(t)
}

func TestConnectionApplyFailureRetryOnlyAppliesWithoutRewriting(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing-Saki-runtime")
	isolateConnectionApp(t, root, missing)
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	saveAppConnection(t, h, config)
	path := connectionConfigPath(guiStorePath())
	oldTime := time.Unix(1700000000, 0)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	var clickErr error
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&config, config.WorkingDirectory).Draft
		h.app.openSettingsTab(settingsTabConnection, settingsSourceRail)
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.Frame()
		clickErr = clickSettingsAction(tr, "Apply saved")
	})
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	if h.app.connectionForm.Error != connErrorStart || h.app.connectionForm.ErrorDetail == "" {
		t.Fatalf("apply failure = %q / %q", h.app.connectionForm.Error, h.app.connectionForm.ErrorDetail)
	}
	h.pump.do(func() {
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.Frame()
		clickErr = clickSettingsAction(tr, "Apply saved")
	})
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(oldTime) {
		t.Fatalf("Retry Apply rewrote connection.json: %s", info.ModTime())
	}
	var saved *connectionConfig
	var applying bool
	var rt *runtimeClient
	h.pump.do(func() { saved, applying, rt = h.app.connectionSaved, h.app.connectionApplying, h.app.rt })
	if saved == nil || applying || rt != nil {
		t.Fatal("retry apply changed saved/runtime state unexpectedly")
	}
	h.shutdown(t)
}

func TestConnectionFeedbackClearsAfterSuccessfulSaveAndLocaleSwitch(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	oldFailure := "provider timeout while testing"
	var enTexts, zhTexts []string
	h.pump.do(func() {
		h.app.connectionForm.Error = connErrorTestFailed
		h.app.connectionForm.ErrorDetail = oldFailure
		h.app.connectionForm.Status = connStatusTesting
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.SetLocale("en")
		tr.Frame()
		enTexts = tr.Texts()
		if !tr.HasText(locales.In("en").RecoveryConnectionTestFailed()) || tr.HasText(oldFailure) {
			t.Errorf("English neutral test failure/disclosure missing: %v", enTexts)
		}
		if err := tr.Click(locales.In("en").DetailsAction()); err != nil {
			t.Error(err)
			return
		}
		tr.Frame()
		if !tr.HasText(oldFailure) || h.app.connectionForm.Error != connErrorTestFailed || h.app.connectionForm.ErrorDetail != oldFailure {
			t.Error("English disclosure lost the original test failure")
			return
		}
		if err := tr.Click(locales.In("en").HideDetailsAction()); err != nil {
			t.Error(err)
			return
		}
		tr.SetLocale("zh-Hans")
		tr.Frame()
		zhTexts = tr.Texts()
		if !tr.HasText(locales.In("zh-Hans").RecoveryConnectionTestFailed()) || tr.HasText(oldFailure) {
			t.Errorf("Chinese neutral test failure/disclosure missing: %v", zhTexts)
		}
		if err := tr.Click(locales.In("zh-Hans").DetailsAction()); err != nil {
			t.Error(err)
			return
		}
		tr.Frame()
		if !tr.HasText(oldFailure) || h.app.connectionForm.Error != connErrorTestFailed || h.app.connectionForm.ErrorDetail != oldFailure {
			t.Error("Chinese disclosure changed the raw test failure")
			return
		}
	})
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&config, config.WorkingDirectory).Draft
		h.app.saveConnection(config)
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	var keptError, keptDetail, savedModel string
	h.pump.do(func() {
		keptError, keptDetail = h.app.connectionForm.Error, h.app.connectionForm.ErrorDetail
		savedModel = h.app.connectionForm.Draft.Model
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.SetLocale("zh-Hans")
		tr.Frame()
		zhTexts = tr.Texts()
		tr.SetLocale("en")
		tr.Frame()
		enTexts = tr.Texts()
		if tr.HasText(locales.In("en").RecoveryConnectionTestFailed()) || tr.HasText(oldFailure) || h.app.connectionForm.RecoveryDetailsOpen {
			t.Error("save success kept the old summary or disclosure")
			return
		}
		h.app.connectionForm.Error = connErrorTestFailed
		h.app.connectionForm.ErrorDetail = "raw provider 500"
		tr.Frame()
		if !tr.HasText(locales.In("en").RecoveryConnectionTestFailed()) || tr.HasText("raw provider 500") {
			t.Errorf("next test failure was not initially collapsed: %v", tr.Texts())
		}
		if err := tr.Click(locales.In("en").DetailsAction()); err != nil {
			t.Error(err)
			return
		}
		tr.Frame()
		enTexts = tr.Texts()
	})
	if keptError != "" || keptDetail != "" {
		t.Fatalf("save success kept old failure: %q / %q", keptError, keptDetail)
	}
	if savedModel != config.Model {
		t.Fatalf("save success changed the saved draft: %q", savedModel)
	}
	if !containsText(enTexts, "raw provider 500") {
		t.Fatalf("next raw failure missing: %v", enTexts)
	}
	if containsText(enTexts, oldFailure) || containsText(zhTexts, locales.In("zh-Hans").RecoveryConnectionTestFailed()) {
		t.Fatalf("old failure revived: en=%v zh=%v", enTexts, zhTexts)
	}
	saved, err := loadConnectionConfig(connectionConfigPath(guiStorePath()))
	if err != nil || saved != config {
		t.Fatalf("successful save did not persist the confirmed configuration: %+v / %v", saved, err)
	}
	h.shutdown(t)
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
	if config.WorkingDirectory == chosen || ct.Workspace == chosen || a.workDir == chosen || ct.SessionID != "s1" || a.connectionApplying || a.connectionTesting || a.rt != nil {
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
	t.Setenv("SAKI_GUI_WORKDIR", "")
	t.Chdir(root)
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.closeIssueToolService() })
	ct := a.selectedContact()
	originalID, originalFolder := ct.ID, ct.Workspace
	ct.Draft, ct.SessionID = "Keep local draft", "existing-session"
	a.contacts = append(a.contacts, &contact{ID: "inherited-contact", Name: "Inherited", Role: "Assistant"})
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	form := a.connectionForm
	fixture := testConnectionConfig(t)
	form.Draft.Endpoint, form.Draft.APIKey, form.Draft.Model = fixture.Endpoint, fixture.APIKey, fixture.Model
	requests, replies, updates := controlledFolderChoice(t, a)
	saves, probes, applies := 0, 0, 0
	actions := connectionSettingsActions{
		Save: func(connectionConfig) { saves++ }, Test: func(connectionConfig) { probes++ },
		SaveAndApply: func(connectionConfig) { applies++ }, Apply: func() { applies++ },
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
		if saves != 0 || probes != 0 || applies != 0 {
			t.Fatalf("fresh GUI silently accepted launch folder %q: saves=%d probes=%d applies=%d", form.Draft.WorkingDirectory, saves, probes, applies)
		}
		if form.Errors["WorkingDirectory"] == "" || a.rt != nil || a.connectionApplying || a.connectionTesting {
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
	// Use the real save boundary now; no Test/Apply callback or Runtime is invoked.
	actions.Save = a.saveConnection
	tr = connectionSettingsTester(form, actions, 580, 1800)
	tr.Frame()
	if err := tr.Click("Save configuration"); err != nil {
		t.Fatal(err)
	}
	awaitFolderChoice(t, updates)()
	if form.Busy || form.Error != "" {
		t.Fatalf("chosen folder could not be saved: %+v", form)
	}
	saved, err := loadConnectionConfig(a.connectionPath)
	if err != nil || saved.WorkingDirectory != chosen {
		t.Fatalf("saved explicit folder = %q: %v", saved.WorkingDirectory, err)
	}
	reopened, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.closeIssueToolService() })
	if reopened.connectionForm.Draft.WorkingDirectory != chosen || !reopened.connectionForm.HasSaved || reopened.selectedID() != originalID {
		t.Fatal("reopening lost the confirmed connection choice or contact identity")
	}
	retained := reopened.contactByID(originalID)
	if retained.Workspace != originalFolder || retained.SessionID != "existing-session" || retained.Draft != "Keep local draft" || reopened.contactByID("inherited-contact").Workspace != "" {
		t.Fatal("connection choice rewrote an existing contact/session folder or inheritance")
	}
	if a.rt != nil || reopened.rt != nil || probes != 0 || applies != 0 {
		t.Fatal("first-use folder workflow started/tested/applied Runtime")
	}
}

func TestFirstUseWorkFolderRetainsLegacyAndSavedChoices(t *testing.T) {
	for _, source := range []string{"saved", "legacy", "invalid-saved"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			isolateConnectionApp(t, root, filepath.Join(root, "unused-runtime"))
			want := root
			if source == "legacy" {
				t.Setenv("API_KEY", "legacy-fixture-key")
			} else {
				config := testConnectionConfig(t)
				want = config.WorkingDirectory
				if err := saveConnectionConfig(connectionConfigPath(guiStorePath()), config); err != nil {
					t.Fatal(err)
				}
				if source == "invalid-saved" {
					if err := os.Remove(want); err != nil {
						t.Fatal(err)
					}
				}
			}
			a, err := newRuntimeApp()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.closeIssueToolService() })
			a.openSettingsTab(settingsTabConnection, settingsSourceRail)
			if a.connectionForm.Draft.WorkingDirectory != want || a.rt != nil {
				t.Fatalf("%s folder choice was cleared or started Runtime: %+v", source, a.connectionForm.Draft)
			}
		})
	}
}

func TestFirstConnectionSaveQueuedCompletionRetainsLiveLocalState(t *testing.T) {
	for _, fault := range []bool{false, true} {
		t.Run(fmt.Sprintf("local-save-failure=%v", fault), func(t *testing.T) {
			root := t.TempDir()
			isolateConnectionApp(t, root, filepath.Join(root, "unused-runtime"))
			a, err := newRuntimeApp()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.closeIssueToolService() })
			config := testConnectionConfig(t)
			a.connectionForm.Draft = newConnectionSettings(&config, config.WorkingDirectory).Draft
			updates := make(chan func(), 1)
			a.setUIHook(func(fn func()) { updates <- fn })
			a.saveConnection(config)
			complete := awaitFolderChoice(t, updates)
			// The worker has returned, but the queued completion has not touched live UI state.
			if a.connectionSaved != nil || !a.connectionSaveInFlight {
				t.Fatal("test did not stop at the first-save completion boundary")
			}
			if fault {
				// Remove only this test's old worker file so live autosave can own the store before a write fault.
				if err := os.Remove(a.storePath); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			if err := tr.Click("← Back to workbench"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			first := a.selectedContact()
			focusComposer(t, tr, locales.In("en").ComposerLabel(first.Name))
			tr.Type("Draft typed after the connection worker")
			tr.Frame()
			if err := tr.Click("Add contact"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			second := a.selectedContact()
			focusComposer(t, tr, locales.In("en").ComposerLabel(second.Name))
			tr.Type("Second live draft")
			tr.Frame()
			if fault {
				if err := os.Remove(a.storePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(a.storePath, 0o700); err != nil {
					t.Fatal(err)
				}
				second.Draft = "Retain this unsaved edit"
				a.draftChanged(second)
				if a.localSaveErr == "" {
					t.Fatal("directory collision did not produce the controlled local-save error")
				}
				if err := os.Remove(a.storePath); err != nil {
					t.Fatal(err)
				}
			}
			editor := a.ensureSettingsProfileEditor(second.ID)
			editor.Draft.Name = "Unsaved profile name"
			var currentRuntime *runtimeClient
			if !fault {
				currentRuntime = &runtimeClient{done: make(chan struct{})}
				a.rt = currentRuntime
				second.ActiveTaskID, second.SendState = "accepted-task", sendAccepted
			}
			firstID, secondID, selected := first.ID, second.ID, a.selectedID()
			firstDraft, secondDraft, localError := first.Draft, second.Draft, a.localSaveErr
			complete()
			if len(a.contacts) != 2 || a.contactByID(firstID) != first || a.contactByID(secondID) != second || a.selectedID() != selected || first.Draft != firstDraft || second.Draft != secondDraft {
				t.Fatal("first connection-save completion replaced live contact IDs, drafts or selection with its worker snapshot")
			}
			if a.localSaveErr != localError {
				t.Fatal("connection completion discarded/retried an independent local-save failure")
			}
			if fault && (a.connectionForm.Error != connStatusSavedStoreFailed || a.connectionReloadBlocked() != connBlockedStore) {
				t.Fatal("saved connection did not retain the local-store error and block Apply")
			}
			if !fault && a.connectionReloadBlocked() != connBlockedActiveWork {
				t.Fatal("completion lost intervening activity and enabled Apply")
			}
			if editor.Draft.Name != "Unsaved profile name" || second.Name == editor.Draft.Name {
				t.Fatal("connection Save discarded or force-saved the profile editor")
			}
			if a.connectionSaved == nil || a.connectionSaved.WorkingDirectory != config.WorkingDirectory || a.connectionSaveInFlight || a.rt != currentRuntime || a.connectionApplying {
				t.Fatal("Save lost its successful connection write or automatically applied it")
			}
			if fault {
				if err := a.persist(); err != nil || a.localSaveErr != "" || a.connectionReloadBlocked() != "" {
					t.Fatalf("explicit local Retry save did not recover: %v", err)
				}
				tr := ui.NewTester(a.view, 1040, 900)
				tr.Frame()
				if a.connectionForm.Error != "" || a.connectionForm.ErrorDetail != "" {
					t.Fatal("local Retry left owned Connection feedback stale")
				}
				a.connectionForm.Error, a.connectionForm.ErrorDetail = connErrorStart, "unrelated connection failure"
				tr.Frame()
				if a.connectionForm.Error != connErrorStart || a.connectionForm.ErrorDetail != "unrelated connection failure" || editor.Draft.Name != "Unsaved profile name" || second.Name == editor.Draft.Name || a.rt != currentRuntime || a.connectionApplying {
					t.Fatal("feedback recovery changed unrelated errors/profile/runtime state")
				}
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
				needsServiceClose := a.issueToolServer != nil
				if restore && needsServiceClose || !restore && os.Getenv("SAKI_TEST_ISSUE_TOOL_HELPER") != "" && !needsServiceClose {
					t.Fatal("saved/unavailable fixture has the wrong owned host-service precondition")
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
				if needsServiceClose {
					if a.quitReady || quitCount != 0 {
						t.Fatal("Quit escaped the owned host-service close completion")
					}
					awaitFolderChoice(t, updates)()
				}
				if !a.quitReady || quitCount != 1 || a.dataRecovery.inFlight || ct.Draft != "intervening live edit" || a.rt != nil {
					t.Fatal("final recovery result did not release exactly once and preserve live edits")
				}
				if restore {
					if a.dataRecovery.result.Outcome != storeRecoveryVerifiedInstalled || a.dataRecovery.result.ArchivePath == "" || a.store != nil || a.storeInitAllowed {
						t.Fatal("shutdown lost the terminal restore result or activated fallback")
					}
				} else if a.dataRecovery.output == "" {
					t.Fatal("shutdown lost completed copy location")
				}
				t.Logf("host-service close completion required=%t; final Quit callbacks=%d", needsServiceClose, quitCount)
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
			if a.connectionForm.Draft != form.Draft || a.connectionForm.Error != form.Error || a.connectionForm.Status != form.Status || a.connectionForm.ReloadBlocked != form.ReloadBlocked || a.connectionSaveInFlight || a.connectionApplying || a.connectionTesting || a.runtimeHelperChecked || a.rt != nil || a.themeImport != nil || recoveryRecordSnapshot(t, a.selectedContact()) != before {
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
			needsServiceClose := a.issueToolServer != nil
			if restore && needsServiceClose || !restore && os.Getenv("SAKI_TEST_ISSUE_TOOL_HELPER") != "" && !needsServiceClose {
				t.Fatal("saved/unavailable fixture has the wrong owned host-service precondition")
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
			if needsServiceClose {
				if a.quitReady || quit != 0 {
					t.Fatal("Quit escaped the owned host-service close completion")
				}
				awaitFolderChoice(t, updates)()
			}
			if !a.quitReady || quit != 1 || a.dataRecovery.inFlight || a.dataRecovery.problem == "" {
				t.Fatal("failed completion did not preserve error and release quit")
			}
			if restore && a.dataRecovery.result.Outcome != storeRecoveryNotInstalled {
				t.Fatal("failure outcome lost during shutdown")
			}
			t.Logf("host-service close completion required=%t; final Quit callbacks=%d", needsServiceClose, quit)
		})
	}
}

func TestC8RestoreAdmissionPreservesUnavailableEditsAndLocalRetry(t *testing.T) {
	for _, kind := range []string{"loaded", "local-save", "profile", "connection", "context", "contact-draft", "history", "result", "submission", "metadata", "connection-test", "model", "contact-picker"} {
		t.Run(kind, func(t *testing.T) {
			a := c8UnavailableApp(t)
			if kind == "loaded" || kind == "local-save" {
				a = c8SavedApp(t)
			}
			switch kind {
			case "local-save":
				a.localSaveErr = "existing write failure"
			case "profile":
				a.ensureSettingsProfileEditor(a.selectedID()).Draft.Name = "keep profile"
			case "connection":
				a.connectionForm.Draft.Model = "keep model"
			case "context":
				a.selectedContact().Context.formText = "keep snippet"
			case "contact-draft":
				a.selectedContact().Draft = "keep draft"
			case "history":
				a.contactHistoryInFlight = 1
			case "result":
				a.resultInFlight = 1
			case "submission":
				a.submissionInFlight = 1
			case "metadata":
				a.metadataInFlight = 1
			case "connection-test":
				a.connectionTesting = true
			case "model":
				a.modelOpActive = true
			case "contact-picker":
				a.ensureSettingsProfileEditor(a.selectedID()).Busy = true
			}
			original, record := recoverySavedBytes(t, a.storePath), recoveryRecordSnapshot(t, a.selectedContact())
			a.chooseRecoveryFile = func(mygo.OpenDialogOptions) ([]string, error) {
				t.Error("inadmissible restore opened a picker")
				return nil, nil
			}
			a.chooseDataRecovery(true)
			if a.dataRecovery != nil && (a.dataRecovery.choosing || a.dataRecovery.inFlight) || string(recoverySavedBytes(t, a.storePath)) != string(original) || recoveryRecordSnapshot(t, a.selectedContact()) != record || a.rt != nil {
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
