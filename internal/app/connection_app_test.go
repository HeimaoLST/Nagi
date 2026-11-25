package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo"
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
	if !a.settingsOpen || a.settingsTab != settingsTabConnection {
		t.Fatalf("an empty profile must open the connection settings page: open=%v tab=%q", a.settingsOpen, a.settingsTab)
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
	store.State.Contacts[0].Draft = "retain the existing draft"
	if err := store.SaveState(); err != nil {
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
	if a.runtimeStatus != runtimeStateFailed || a.runtimeErrKind != runtimeNoticeSavedNeedsRepair {
		t.Fatalf("invalid saved settings did not enter repair state: status=%v kind=%q", a.runtimeStatus, a.runtimeErrKind)
	}
	if a.pi != nil || !a.settingsOpen || a.settingsTab != settingsTabConnection {
		t.Fatalf("invalid GUI configuration did not open the repair page: pi=%v open=%v tab=%q", a.pi != nil, a.settingsOpen, a.settingsTab)
	}
}

type folderChoiceReply struct {
	paths []string
	err   error
}

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
			before := recoveryRecordSnapshot(t, a.selectedContact())
			a.chooseThemeFile = func(mygo.OpenDialogOptions) ([]string, error) {
				t.Error("recovery ownership admitted a theme dialog")
				return nil, nil
			}
			a.addProvider()
			a.importAppearanceTheme()
			if a.providers.adding || a.providers.busy || a.providers.failure != (providerFeedback{}) || a.pi != nil || a.themeImport != nil || recoveryRecordSnapshot(t, a.selectedContact()) != before {
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
	for _, kind := range []string{"loaded", "local-save", "profile", "context", "contact-draft", "history", "submission", "connection-test", "contact-picker"} {
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
			case "context":
				a.selectedContact().Context.FormText = "keep snippet"
			case "contact-draft":
				a.selectedContact().Draft = "keep draft"
			case "history":
				a.contactHistoryInFlight = 1
			case "submission":
				a.submissionInFlight = 1
			case "connection-test":
				a.providers.busy = true
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

func testConnectionConfig(t *testing.T) connectionConfig {
	return connectionConfig{
		API:              connectionAPIResponses,
		Endpoint:         "https://example.com/v1",
		APIKey:           "test-connection-key",
		Model:            "test-model",
		ContextTokens:    65536,
		OutputTokens:     8192,
		WorkingDirectory: t.TempDir(),
		MaxActiveTasks:   2,
	}
}
