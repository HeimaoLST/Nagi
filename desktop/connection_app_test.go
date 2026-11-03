package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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
	h.pump.do(func() { h.app.saveConnection(config) })
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
	if !strings.Contains(h.app.connectionForm.Error, "Could not start the new runtime") {
		t.Fatalf("apply failure = %q", h.app.connectionForm.Error)
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
