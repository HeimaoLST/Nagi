package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func TestContactStoreCreatesDefaultAndStableID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, "/tmp/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.State.Contacts) != 1 {
		t.Fatalf("contacts = %d", len(store.State.Contacts))
	}
	first := store.State.Contacts[0]
	if first.Name != "Nagi" || first.SessionID != "" || first.Workspace != "/tmp/workspace" {
		t.Fatalf("default contact = %+v", first)
	}
	if store.State.Selected != first.ID {
		t.Fatalf("selected = %q, want %q", store.State.Selected, first.ID)
	}
	reloaded, err := openContactStore(path, "/ignored")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Contacts[0].ID != first.ID {
		t.Fatalf("default contact id changed: %q -> %q", first.ID, reloaded.State.Contacts[0].ID)
	}
}

func TestContactStoreRoundTripKeepsProductState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "contacts.json")
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	contacts := []*contact{
		{ID: "a", Name: "Nagi", Role: "Assistant", Purpose: "p", Instructions: "i", Workspace: "/work", SessionID: "s-1", PiBinding: recoveryConversationBinding("a", "/work", "s-1"), Draft: "draft a"},
		{ID: "b", Name: "New contact 2", Role: "Assistant", Draft: "draft b"},
	}
	if err := saveContacts(store, contacts, "b"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Selected != "b" || len(reloaded.State.Contacts) != 2 {
		t.Fatalf("state = %+v", reloaded.State)
	}
	got := reloaded.State.Contacts[0]
	if got.SessionID != "s-1" || !reflect.DeepEqual(got.PiBinding, contacts[0].PiBinding) || got.Draft != "draft a" || got.Workspace != "/work" {
		t.Fatalf("contact = %+v", got)
	}
}

func TestContactStoreRejectsCorruptFileWithoutOverwriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	original := []byte("{not json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := openContactStore(path, "/tmp")
	if err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("corrupt store must not be replaced")
	}
}

func TestContactStoreRejectsDuplicateIDsAndVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contacts.json")
	state := storedState{Version: contactStoreVersion, Selected: "a", Contacts: []storedContact{{ID: "a"}, {ID: "a"}}}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openContactStore(path, "/tmp"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error = %v", err)
	}

	state = storedState{Version: 99, Selected: "a", Contacts: []storedContact{{ID: "a"}}}
	data, _ = json.Marshal(state)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openContactStore(path, "/tmp"); err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("version error = %v", err)
	}
}

func recoverySavedBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func recoveryWriteState(t *testing.T, path string, state storedState) []byte {
	t.Helper()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func recoveryProducedState(t *testing.T) storedState {
	t.Helper()
	a, ct := taskContextApp(t)
	ct.SessionID, ct.PiBinding = "", nil
	if _, err := a.createActivityFor(ct, "Keep 原文 and IDs", "A real saved requirement"); err != nil {
		t.Fatal(err)
	}
	var state storedState
	if err := json.Unmarshal(recoverySavedBytes(t, a.store.Path), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestStoreRecoveryAcceptsOrdinaryProducerCopies(t *testing.T) {
	for _, producer := range []string{"local", "configured"} {
		t.Run(producer, func(t *testing.T) {
			root := t.TempDir()
			isolateConnectionApp(t, root, filepath.Join(root, "never-launched"))
			if producer == "configured" {
				if err := saveConnectionConfig(connectionConfigPath(guiStorePath()), testConnectionConfig(t)); err != nil {
					t.Fatal(err)
				}
			}
			a, err := newRuntimeApp()
			if err != nil {
				t.Fatal(err)
			}
			ct := a.selectedContact()
			if producer != "configured" {
				ct.Draft = "Saved before connection, with 中文"
				a.draftChanged(ct)
				if a.localSaveErr != "" {
					t.Fatal(a.localSaveErr)
				}
			}
			original := recoverySavedBytes(t, guiStorePath())
			copy, err := inspectStoreRecoveryCopy(guiStorePath())
			if err != nil || copy.Contacts != 1 || copy.Issues != len(ct.Activities) {
				t.Fatalf("ordinary %s copy rejected: %+v %v", producer, copy, err)
			}
			if !bytes.Contains(original, []byte(`"issue_tool_operations": {}`)) {
				t.Fatal("test did not exercise an actual emitted empty operation ledger")
			}
			editor := newContactProfileEditor(contactProfileDraft(ct))
			editor.Draft.Name = "Unsaved profile stays in memory"
			output := filepath.Join(t.TempDir(), "Nagi-workbench-recovery.json")
			path, err := writeStoreRecoveryCopy(copy, output)
			if err != nil || path != output || !bytes.Equal(recoverySavedBytes(t, output), original) || !bytes.Equal(recoverySavedBytes(t, guiStorePath()), original) || editor.Draft.Name != "Unsaved profile stays in memory" {
				t.Fatalf("export changed saved/live state or bytes: %q %v", path, err)
			}
			targetPath := filepath.Join(t.TempDir(), "contacts.json")
			if err := os.WriteFile(targetPath, []byte("{damaged"), 0600); err != nil {
				t.Fatal(err)
			}
			target, err := inspectStoreRecoveryTarget(targetPath)
			if err != nil {
				t.Fatal(err)
			}
			result, err := restoreStoreRecoveryCopy(copy, target)
			if err != nil || result.Outcome != storeRecoveryVerifiedInstalled {
				t.Fatalf("ordinary producer did not restore: %+v %v", result, err)
			}
			var installed storedState
			if err := json.Unmarshal(recoverySavedBytes(t, targetPath), &installed); err != nil || installed.Contacts[0].SelectedIssueID != ct.SelectedActivityID {
				t.Fatal("installation filled a derived issue selection")
			}
			reopened, err := openContactStore(targetPath, "/ignored")
			if err != nil || reopened.State.Selected != ct.ID {
				t.Fatal("normal reopen lost the original selected contact")
			}
			if a.pi != nil || a.connectionApplying {
				t.Fatal("copy or restore started a Pi worker")
			}
		})
	}
}

func TestStoreRecoveryRejectsUnsupportedCopies(t *testing.T) {
	for _, kind := range []string{"old-version", "future-version", "unknown-top", "unknown-nested", "invalid-utf8", "multiple-values", "selected-contact", "duplicate-contact", "duplicate-issue", "unknown-owner", "dangling-issue", "missing-ledger", "invalid-scope", "duplicate-scope", "legacy-requirements", "revision", "requirement-state", "blank-requirement", "nil-question", "blank-question", "issue-status", "execution-id", "execution-status", "pending-execution", "accepted-without-task", "running-without-task", "submitting-intent", "unknown-intent-without-session", "dispatching-batch", "appearance", "layout"} {
		t.Run(kind, func(t *testing.T) {
			state := recoveryProducedState(t)
			issue := &state.Issues[0]
			var extra string
			switch kind {
			case "old-version":
				state.Version = 8
			case "future-version":
				state.Version = contactStoreVersion + 1
			case "unknown-top":
				extra = "top"
			case "unknown-nested":
				extra = "nested"
			case "invalid-utf8", "multiple-values":
				extra = kind
			case "selected-contact":
				state.Selected = "absent"
			case "duplicate-contact":
				state.Contacts = append(state.Contacts, state.Contacts[0])
			case "duplicate-issue":
				state.Issues = append(state.Issues, *issue)
			case "unknown-owner":
				issue.ContactID = "absent"
			case "dangling-issue":
				state.Contacts[0].SelectedIssueID = "absent"
			case "missing-ledger":
				state.IssueToolOperations = nil
			case "invalid-scope":
				state.IssueToolScopes = []ActivityToolScope{{Ref: "bad"}}
			case "duplicate-scope":
				scope := ActivityToolScope{Ref: "same", ContactID: state.Selected, RuntimeSessionID: "session", TurnID: "turn"}
				state.IssueToolScopes = []ActivityToolScope{scope, scope}
			case "legacy-requirements":
				extra = kind
			case "revision":
				issue.RequirementsRevision = 0
			case "requirement-state":
				issue.RequirementItems[0].State = "invalid"
			case "blank-requirement":
				issue.RequirementItems[0].Text = " "
			case "nil-question":
				issue.Questions = []*activityQuestion{nil}
			case "blank-question":
				issue.Questions = []*activityQuestion{{Ref: " "}}
			case "issue-status":
				issue.Status = "open"
			case "execution-id", "execution-status", "pending-execution", "accepted-without-task", "running-without-task":
				execution := storedActivityExecution{ID: "execution", Status: activityExecutionUnknown}
				switch kind {
				case "execution-id":
					execution.ID = ""
				case "execution-status":
					execution.Status = ""
				case "pending-execution":
					execution.Status = activityExecutionPending
				case "accepted-without-task":
					execution.Status = activityExecutionAccepted
				case "running-without-task":
					execution.Status = activityExecutionRunning
				}
				issue.Executions = []storedActivityExecution{execution}
			case "submitting-intent":
				state.Contacts[0].SessionID = "session"
				state.Contacts[0].PendingSend = &pendingSend{State: "submitting", Draft: "uncertain"}
			case "unknown-intent-without-session":
				state.Contacts[0].PendingSend = &pendingSend{State: "unknown", Draft: "uncertain"}
			case "dispatching-batch":
				issue.Supplements.Batches = []*activitySupplementBatch{{ID: "batch", State: supplementBatchDispatching}}
			case "appearance":
				state.Appearance = "invalid"
			case "layout":
				state.Layout = &storedLayout{LeftWidth: 1}
			}
			path := filepath.Join(t.TempDir(), "unsupported.json")
			original := recoveryWriteState(t, path, state)
			if extra != "" {
				var object map[string]any
				if err := json.Unmarshal(original, &object); err != nil {
					t.Fatal(err)
				}
				switch extra {
				case "top":
					object["future_field"] = "do not drop me"
				case "nested":
					object["contacts"].([]any)[0].(map[string]any)["future_field"] = "do not drop me"
				case "legacy-requirements":
					object["issues"].([]any)[0].(map[string]any)["requirements"] = "legacy requirement"
				}
				original, _ = json.Marshal(object)
				if extra == "multiple-values" {
					original = append(original, []byte(" {}")...)
				}
				if extra == "invalid-utf8" {
					original = bytes.Replace(original, []byte("Nagi"), []byte{0xff}, 1)
				}
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if copy, err := inspectStoreRecoveryCopy(path); err == nil || copy != nil || err.Error() == "" {
				t.Fatalf("unsupported %s was not rejected: %+v %v", kind, copy, err)
			}
			if !bytes.Equal(original, recoverySavedBytes(t, path)) {
				t.Fatal("inspection repaired or changed unsupported bytes")
			}
		})
	}
}

func recoveryConversationBinding(contactID, cwd, session string) *piConversationBinding {
	return &piConversationBinding{
		Engine:        "pi",
		Context:       pi.ContextBinding{ContextID: session + "-context", BotID: contactID, Purpose: pi.Conversation, SessionRef: session, CWD: cwd, SelectionRevision: 1, Used: true},
		Selection:     piSelection{CWD: cwd, CodingTools: []string{}},
		Configuration: piConfiguration{WireAPI: pi.OpenAICompletions, BaseURL: "http://127.0.0.1:1/v1", Model: "recovery-fixture", ContextTokens: 65536, OutputTokens: 4096, CredentialSource: "recovery-fixture"},
	}
}

func TestStoreRecoveryCapturedBytesAndExclusiveOutput(t *testing.T) {
	state := recoveryProducedState(t)
	source := filepath.Join(t.TempDir(), "saved.json")
	original := recoveryWriteState(t, source, state)
	copy, err := inspectStoreRecoveryCopy(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("later source bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "copy.json")
	if _, err := writeStoreRecoveryCopy(copy, output); err != nil || !bytes.Equal(original, recoverySavedBytes(t, output)) {
		t.Fatal("export did not use its inspected point-in-time bytes")
	}
	if _, err := writeStoreRecoveryCopy(copy, output); err == nil || !bytes.Equal(original, recoverySavedBytes(t, output)) {
		t.Fatal("exclusive copy replaced an existing output")
	}
	if _, err := writeStoreRecoveryCopy(copy, source); err == nil || string(recoverySavedBytes(t, source)) != "later source bytes" {
		t.Fatal("export replaced the selected source")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := writeStoreRecoveryCopy(copy, source); err == nil {
		t.Fatal("export recreated its removed source")
	}
}

func TestStoreRecoveryOptionalDefaultsAndUnknownRecords(t *testing.T) {
	for _, kind := range []string{"empty-selection", "unknown-execution", "accepted-no-session", "running-no-session", "unknown-intent", "unknown-batch", "associated-dispatching-batch"} {
		t.Run(kind, func(t *testing.T) {
			state := recoveryProducedState(t)
			state.Appearance, state.Layout = "", nil
			state.ThemeIDs = &appearanceThemeIDs{Light: "missing-custom-theme"}
			state.IssueToolOperations = &ActivityToolOperationLedger{}
			state.Contacts[0].SelectedIssueID = ""
			issue := &state.Issues[0]
			switch kind {
			case "unknown-execution":
				issue.Executions = []storedActivityExecution{{ID: "unknown", PiIntent: true, Status: activityExecutionUnknown, Detail: "No trusted association"}}
			case "accepted-no-session", "running-no-session":
				status := activityExecutionAccepted
				if kind == "running-no-session" {
					status = activityExecutionRunning
				}
				issue.Executions = []storedActivityExecution{{ID: "known-task", PiIntent: true, Status: status, TaskID: "task"}}
			case "unknown-intent":
				state.Contacts[0].SessionID = "session"
				state.Contacts[0].PiBinding = recoveryConversationBinding(state.Contacts[0].ID, t.TempDir(), state.Contacts[0].SessionID)
				state.Contacts[0].PendingSend = &pendingSend{State: "unknown", Draft: "May have been sent"}
			case "unknown-batch":
				issue.Supplements.Batches = []*activitySupplementBatch{{ID: "unknown", State: supplementBatchUnknown}}
			case "associated-dispatching-batch":
				issue.Supplements.Batches = []*activitySupplementBatch{{ID: "dispatching", State: supplementBatchDispatching, ExecutionID: "recorded-execution"}}
			}
			path := filepath.Join(t.TempDir(), "copy.json")
			original := recoveryWriteState(t, path, state)
			copy, err := inspectStoreRecoveryCopy(path)
			if kind == "accepted-no-session" || kind == "running-no-session" {
				if err == nil || !strings.Contains(err.Error(), "invalid Pi execution record") || !bytes.Equal(recoverySavedBytes(t, path), original) {
					t.Fatalf("unbound active execution %s was not rejected without changing the copy: %v", kind, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("representable %s was rejected: %v", kind, err)
			}
			output := filepath.Join(t.TempDir(), "exact.json")
			if _, err := writeStoreRecoveryCopy(copy, output); err != nil || !bytes.Equal(recoverySavedBytes(t, output), original) {
				t.Fatal("copy dropped optional defaults or uncertain facts")
			}
		})
	}
}

func TestStoreRecoveryDoesNotReplaceItsSelectedSourceThroughAlias(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "selected.json")
	recoveryWriteState(t, source, recoveryProducedState(t))
	copy, err := inspectStoreRecoveryCopy(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, filepath.Join(root, "preserved-copy.json")); err != nil {
		t.Fatal(err)
	}
	damaged := []byte("{new original at the selected path")
	if err := os.WriteFile(source, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "same-directory")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	target, err := inspectStoreRecoveryTarget(filepath.Join(alias, "selected.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := restoreStoreRecoveryCopy(copy, target)
	if err == nil || result.Outcome != storeRecoveryNotInstalled || result.ArchivePath != "" || !bytes.Equal(recoverySavedBytes(t, source), damaged) {
		t.Fatalf("aliased target overwrote the selected source: %+v %v", result, err)
	}
}

func TestStoreRecoveryTargetChangeAndUnsupportedFiles(t *testing.T) {
	for _, change := range []string{"missing", "changed-bytes", "replaced-same-bytes", "became-readable", "directory", "symlink"} {
		t.Run(change, func(t *testing.T) {
			state := recoveryProducedState(t)
			source := filepath.Join(t.TempDir(), "copy.json")
			recoveryWriteState(t, source, state)
			copy, err := inspectStoreRecoveryCopy(source)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			path := filepath.Join(root, "contacts.json")
			damaged := []byte("{original damaged bytes")
			if err := os.WriteFile(path, damaged, 0600); err != nil {
				t.Fatal(err)
			}
			target, err := inspectStoreRecoveryTarget(path)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "changed-bytes":
				if err := os.WriteFile(path, []byte("new damaged original"), 0600); err != nil {
					t.Fatal(err)
				}
			case "replaced-same-bytes":
				other := filepath.Join(root, "replacement")
				if err := os.WriteFile(other, damaged, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(other, path); err != nil {
					t.Fatal(err)
				}
			case "became-readable":
				recoveryWriteState(t, path, state)
			case "directory", "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if change == "directory" {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(source, path); err != nil {
					t.Fatal(err)
				}
			}
			result, err := restoreStoreRecoveryCopy(copy, target)
			if err == nil || result.Outcome != storeRecoveryNotInstalled || result.ArchivePath != "" {
				t.Fatalf("changed target was replaced: %+v %v", result, err)
			}
			entries, err := os.ReadDir(root)
			wantEntries := 1
			if change == "missing" {
				wantEntries = 0
			}
			if err != nil || len(entries) != wantEntries {
				t.Fatal("rejected target created archive/stage/replacement files")
			}
			if change == "missing" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("missing target was silently initialized")
				}
			}
		})
	}
}

func TestStoreRecoveryReplacementFailureAndUnconfirmedReadback(t *testing.T) {
	for _, failure := range []string{"rename", "readback"} {
		t.Run(failure, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "copy.json")
			original := recoveryWriteState(t, source, recoveryProducedState(t))
			copy, err := inspectStoreRecoveryCopy(source)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			path := filepath.Join(root, "contacts.json")
			damaged := []byte("{damaged original")
			if err := os.WriteFile(path, damaged, 0600); err != nil {
				t.Fatal(err)
			}
			target, err := inspectStoreRecoveryTarget(path)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			result, err := restoreStoreRecoveryCopyWithRename(copy, target, func(stage, destination string) error {
				calls++
				if failure == "rename" {
					return os.ErrPermission
				}
				if err := os.Rename(stage, destination); err != nil {
					return err
				}
				// Simulate lost/unavailable post-commit readback deterministically.
				return os.Remove(destination)
			})
			if err == nil || calls != 1 || result.ArchivePath == "" || !bytes.Equal(recoverySavedBytes(t, result.ArchivePath), damaged) || !bytes.Equal(recoverySavedBytes(t, source), original) {
				t.Fatalf("replacement failure lost preservation/outcome: %+v %v", result, err)
			}
			if failure == "rename" {
				if result.Outcome != storeRecoveryNotInstalled || !errors.Is(err, os.ErrPermission) || !bytes.Equal(recoverySavedBytes(t, path), damaged) {
					t.Fatal("failed Rename was not safely not-installed")
				}
			} else if result.Outcome != storeRecoveryInstallUnconfirmed {
				t.Fatal("post-Rename uncertainty was reported safe to retry")
			}
			stages, err := filepath.Glob(filepath.Join(root, ".contacts-restore-*"))
			if err != nil || len(stages) != 0 {
				t.Fatal("operation left a partial stage or retried/rolled back")
			}
		})
	}
}

func TestStoreRecoveryReadAndPermissionFailuresPreserveFiles(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "saved.json")
	original := recoveryWriteState(t, source, recoveryProducedState(t))
	copy, err := inspectStoreRecoveryCopy(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectStoreRecoveryCopy(source); err == nil {
		t.Fatal("unreadable selected source was accepted")
	}
	if err := os.Chmod(source, 0600); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(recoverySavedBytes(t, source), original) {
		t.Fatal("failed inspect modified source")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "contacts.json")
	damaged := []byte("{keep original")
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	target, err := inspectStoreRecoveryTarget(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(directory, 0700)
	if _, err := writeStoreRecoveryCopy(copy, filepath.Join(directory, "export.json")); err == nil {
		t.Fatal("copy silently ignored an unwritable destination")
	}
	result, err := restoreStoreRecoveryCopy(copy, target)
	if err == nil || result.Outcome != storeRecoveryNotInstalled || result.ArchivePath != "" || !bytes.Equal(recoverySavedBytes(t, path), damaged) {
		t.Fatalf("archive failure changed original or claimed install: %+v %v", result, err)
	}
}
