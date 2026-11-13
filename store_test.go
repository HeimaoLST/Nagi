package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestContactStoreCreatesDefaultAndStableID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, "/tmp/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.state.Contacts) != 1 {
		t.Fatalf("contacts = %d", len(store.state.Contacts))
	}
	first := store.state.Contacts[0]
	if first.Name != "Saki" || first.SessionID != "" || first.Workspace != "/tmp/workspace" {
		t.Fatalf("default contact = %+v", first)
	}
	if store.state.Selected != first.ID {
		t.Fatalf("selected = %q, want %q", store.state.Selected, first.ID)
	}
	reloaded, err := openContactStore(path, "/ignored")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Contacts[0].ID != first.ID {
		t.Fatalf("default contact id changed: %q -> %q", first.ID, reloaded.state.Contacts[0].ID)
	}
}

func TestContactStoreRoundTripKeepsProductState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "contacts.json")
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	contacts := []*contact{
		{ID: "a", Name: "Saki", Role: "Assistant", Purpose: "p", Instructions: "i", Workspace: "/work", SessionID: "s-1", Draft: "draft a"},
		{ID: "b", Name: "New contact 2", Role: "Assistant", Draft: "draft b"},
	}
	if err := store.save(contacts, "b"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Selected != "b" || len(reloaded.state.Contacts) != 2 {
		t.Fatalf("state = %+v", reloaded.state)
	}
	got := reloaded.state.Contacts[0]
	if got.SessionID != "s-1" || got.Draft != "draft a" || got.Workspace != "/work" {
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
	a, _ := newIssueToolTestApp(t)
	if !a.createIssue("Keep 原文 and IDs", "A real saved requirement") {
		t.Fatal("normal issue producer failed")
	}
	var state storedState
	if err := json.Unmarshal(recoverySavedBytes(t, a.store.path), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestStoreRecoveryAcceptsOrdinaryProducerCopies(t *testing.T) {
	for _, producer := range []string{"local", "configured", "mcp-first-issue"} {
		t.Run(producer, func(t *testing.T) {
			root := t.TempDir()
			isolateConnectionApp(t, root, filepath.Join(root, "never-launched"))
			t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", "")
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
			if producer == "mcp-first-issue" {
				scope, err := a.issueToolNewScope(ct.ID, "saved-conversation", "saved-turn", false)
				if err != nil {
					t.Fatal(err)
				}
				activateIssueToolTestScope(t, a, scope)
				response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{
					Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "MCP first issue 原文"}),
				})
				if err != nil || response.IsError || len(ct.Issues) != 1 || ct.SelectedIssueID != "" {
					t.Fatalf("ordinary MCP producer did not retain its optional selection: response=%+v err=%v", response, err)
				}
				ct.ActiveTaskID, ct.SendState = "", sendIdle
			}
			original := recoverySavedBytes(t, guiStorePath())
			copy, err := inspectStoreRecoveryCopy(guiStorePath())
			if err != nil || copy.Contacts != 1 || copy.Issues != len(ct.Issues) {
				t.Fatalf("ordinary %s copy rejected: %+v %v", producer, copy, err)
			}
			if producer != "mcp-first-issue" && !bytes.Contains(original, []byte(`"issue_tool_operations": {}`)) {
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
			if err := json.Unmarshal(recoverySavedBytes(t, targetPath), &installed); err != nil || installed.Contacts[0].SelectedIssueID != ct.SelectedIssueID {
				t.Fatal("installation filled a derived issue selection")
			}
			reopened, err := openContactStore(targetPath, "/ignored")
			if err != nil || reopened.state.Selected != ct.ID {
				t.Fatal("normal reopen lost the original selected contact")
			}
			if a.rt != nil || a.runtimeHandshake || a.runtimeHelperChecked || a.runtimeBinary != "" {
				t.Fatal("copy/restore launched Runtime")
			}
		})
	}
}

func TestStoreRecoveryStrictCompatibilityAndLegacyLoading(t *testing.T) {
	for _, kind := range []string{"old-version", "future-version", "unknown-top", "unknown-nested", "invalid-utf8", "multiple-values", "selected-contact", "duplicate-contact", "duplicate-issue", "unknown-owner", "dangling-issue", "missing-ledger", "invalid-scope", "duplicate-scope", "legacy-requirements", "revision", "requirement-state", "blank-requirement", "nil-question", "blank-question", "issue-status", "execution-id", "execution-status", "pending-execution", "accepted-without-task", "running-without-task", "submitting-intent", "unknown-intent-without-session", "dispatching-batch", "appearance", "layout"} {
		t.Run(kind, func(t *testing.T) {
			state := recoveryProducedState(t)
			issue := &state.Issues[0]
			var extra string
			switch kind {
			case "old-version":
				state.Version = 8
			case "future-version":
				state.Version = 10
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
				state.IssueToolScopes = []IssueToolScope{{Ref: "bad"}}
			case "duplicate-scope":
				scope := IssueToolScope{Ref: "same", ContactID: state.Selected, RuntimeSessionID: "session", TurnID: "turn"}
				state.IssueToolScopes = []IssueToolScope{scope, scope}
			case "legacy-requirements":
				issue.RequirementItems, issue.Requirements = nil, "legacy requirement"
			case "revision":
				issue.RequirementsRevision = 0
			case "requirement-state":
				issue.RequirementItems[0].State = "invalid"
			case "blank-requirement":
				issue.RequirementItems[0].Text = " "
			case "nil-question":
				issue.Questions = []*issueQuestion{nil}
			case "blank-question":
				issue.Questions = []*issueQuestion{{Ref: " "}}
			case "issue-status":
				issue.Status = "open"
			case "execution-id", "execution-status", "pending-execution", "accepted-without-task", "running-without-task":
				execution := storedIssueExecution{ID: "execution", Status: issueExecutionUnknown}
				switch kind {
				case "execution-id":
					execution.ID = ""
				case "execution-status":
					execution.Status = ""
				case "pending-execution":
					execution.Status = issueExecutionPending
				case "accepted-without-task":
					execution.Status = issueExecutionAccepted
				case "running-without-task":
					execution.Status = issueExecutionRunning
				}
				issue.Executions = []storedIssueExecution{execution}
			case "submitting-intent":
				state.Contacts[0].SessionID = "session"
				state.Contacts[0].PendingSend = &pendingSend{State: "submitting", Draft: "uncertain"}
			case "unknown-intent-without-session":
				state.Contacts[0].PendingSend = &pendingSend{State: "unknown", Draft: "uncertain"}
			case "dispatching-batch":
				issue.Supplements.Batches = []*issueSupplementBatch{{ID: "batch", State: supplementBatchDispatching}}
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
				}
				original, _ = json.Marshal(object)
				if extra == "multiple-values" {
					original = append(original, []byte(" {}")...)
				}
				if extra == "invalid-utf8" {
					original = bytes.Replace(original, []byte("Saki"), []byte{0xff}, 1)
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
	for version := 1; version <= contactStoreVersion; version++ {
		t.Run(fmt.Sprintf("ordinary-v%d", version), func(t *testing.T) {
			state := recoveryProducedState(t)
			state.Version, state.Selected, state.IssueToolOperations = version, "missing", nil
			state.IssueToolScopes = []IssueToolScope{{Ref: "invalid"}}
			path := filepath.Join(t.TempDir(), "legacy.json")
			original := recoveryWriteState(t, path, state)
			store, err := openContactStore(path, "/ignored")
			if err != nil || store.state.Selected != state.Contacts[0].ID || store.issueToolLedger == nil || len(store.issueToolScopes) != 0 || !bytes.Equal(original, recoverySavedBytes(t, path)) {
				t.Fatalf("ordinary legacy behavior changed: %+v %v", store, err)
			}
		})
	}
}

func TestStoreRecoveryExportPausedRestoreAndNormalReopen(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, filepath.Join(root, "never-launched"))
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", "")
	if err := saveConnectionConfig(connectionConfigPath(guiStorePath()), testConnectionConfig(t)); err != nil {
		t.Fatal(err)
	}
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	ct := a.selectedContact()
	ct.SessionID, ct.Draft = "independent-missing-conversation", "Saved draft 原文"
	ct.PreviousSessions = []string{"independent-previous-session"}
	ct.Context.items = []taskContextItem{{ID: "snapshot", Name: "原文", Text: `{"opaque":"text"}`, Revision: 4}}
	ct.pendingSend = &pendingSend{State: "unknown", Draft: "Maybe dispatched", Context: ct.Context.snapshot()}
	if !a.createIssue("Keep issue 原文", "Requirement keeps its ID") {
		t.Fatal("normal issue creation failed")
	}
	issue := ct.Issues[0]
	issue.WorkSessionID = "independent-missing-work"
	issue.Executions = []*issueExecution{{ID: "unknown-execution", Status: issueExecutionUnknown, Prompt: "Original uncertain task", Detail: "Association remains unknown", Context: ct.Context.snapshot()}}
	issue.Questions = []*issueQuestion{{Ref: "question", Text: "原始问题", State: issueQuestionOpen, RequirementsRevision: 1}}
	issue.Supplements.Items = []*issueSupplement{
		{ID: "pending", ContactID: ct.ID, IssueID: issue.ID, Text: "Not automatically sent", RequirementsRevision: 1, State: supplementStatePending},
		{ID: "unknown", ContactID: ct.ID, IssueID: issue.ID, Text: "May already have effects", RequirementsRevision: 1, State: supplementStateUnknown},
	}
	issue.Supplements.Batches = []*issueSupplementBatch{{ID: "unknown-batch", State: supplementBatchUnknown, Text: "原文", ExecutionID: "unknown-execution"}}
	a.appearanceThemeIDs = appearanceThemeIDs{Light: "missing-custom-theme"}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	original := recoverySavedBytes(t, guiStorePath())
	var expected storedState
	if err := json.Unmarshal(original, &expected); err != nil {
		t.Fatal(err)
	}
	if !expected.Issues[0].Supplements.AutoContinue {
		t.Fatal("fixture did not begin with automatic continuation enabled")
	}
	copy, err := inspectStoreRecoveryCopy(guiStorePath())
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "Nagi 恢复副本.json")
	if _, err := writeStoreRecoveryCopy(copy, output); err != nil {
		t.Fatal(err)
	}
	selected, err := inspectStoreRecoveryCopy(output)
	if err != nil {
		t.Fatal(err)
	}
	damaged := []byte{'{', 'b', 'r', 'o', 'k', 'e', 'n', 0xff, '\n'}
	if err := os.WriteFile(guiStorePath(), damaged, 0600); err != nil {
		t.Fatal(err)
	}
	_, startupErr := newRuntimeApp()
	if startupErr == nil {
		t.Fatal("damaged canonical store did not enter unavailable startup")
	}
	fallback := newUnavailableApp(startupErr)
	target, err := inspectStoreRecoveryTarget(guiStorePath())
	if err != nil {
		t.Fatal(err)
	}
	result, err := restoreStoreRecoveryCopy(selected, target)
	if err != nil || result.Outcome != storeRecoveryVerifiedInstalled || result.OutputPath != guiStorePath() || result.ArchivePath == "" {
		t.Fatalf("paused restore = %+v %v", result, err)
	}
	if !bytes.Equal(recoverySavedBytes(t, result.ArchivePath), damaged) || !bytes.Equal(recoverySavedBytes(t, output), original) {
		t.Fatal("restore changed selected source or archived original bytes")
	}
	installed := recoverySavedBytes(t, guiStorePath())
	var got storedState
	if err := json.Unmarshal(installed, &got); err != nil {
		t.Fatal(err)
	}
	for i := range expected.Issues {
		if expected.Issues[i].Supplements != nil {
			expected.Issues[i].Supplements.AutoContinue = false
		}
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("installed facts changed beyond confirmed pause:\ngot=%+v\nexpected=%+v", got, expected)
	}
	if fallback.store != nil || fallback.storeInitAllowed || fallback.rt != nil || fallback.persist() == nil || !bytes.Equal(installed, recoverySavedBytes(t, guiStorePath())) {
		t.Fatal("old unavailable instance activated or overwrote installed data")
	}
	reopened, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	restored := reopened.selectedContact()
	if restored.ID != ct.ID || restored.Draft != ct.Draft || restored.SessionID != ct.SessionID || restored.SelectedIssueID != ct.SelectedIssueID || restored.pendingSend == nil || restored.pendingSend.State != "unknown" || restored.SendState != sendUnknown {
		t.Fatal("normal reopen lost IDs, draft, references or uncertain intent")
	}
	if reopened.rt != nil || reopened.runtimeHandshake || reopened.runtimeHelperChecked || !bytes.Equal(installed, recoverySavedBytes(t, guiStorePath())) {
		t.Fatal("reopen launched Runtime or rewrote the supported paused copy")
	}
	if restored.Issues[0].Executions[0].Status != issueExecutionUnknown || restored.Issues[0].Executions[0].TaskID != "" || restored.Issues[0].Supplements.pendingCount() != 1 {
		t.Fatal("reopen cleared uncertainty or invented dispatch association")
	}
	// Exercise the host's continuation entry after a controlled readiness/readback
	// boundary; there is no subprocess or live Runtime behind this client.
	reopened.rt = &runtimeClient{}
	reopened.runtimeStatus = runtimeStateReady
	reopened.runtimeCapabilitiesLoaded, reopened.runtimeMaxActiveTasks = true, 2
	reopened.maybeAutoContinueAllIssues()
	if reopened.rt.nextID != 0 || len(restored.Issues[0].Executions) != 1 || restored.Issues[0].Supplements.AutoContinue {
		t.Fatal("paused restored work automatically dispatched")
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
			state.IssueToolOperations = &IssueToolOperationLedger{}
			state.Contacts[0].SelectedIssueID = ""
			issue := &state.Issues[0]
			switch kind {
			case "unknown-execution":
				issue.Executions = []storedIssueExecution{{ID: "unknown", Status: issueExecutionUnknown, Detail: "No trusted association"}}
			case "accepted-no-session", "running-no-session":
				status := issueExecutionAccepted
				if kind == "running-no-session" {
					status = issueExecutionRunning
				}
				issue.Executions = []storedIssueExecution{{ID: "known-task", Status: status, TaskID: "task"}}
			case "unknown-intent":
				state.Contacts[0].SessionID = "session"
				state.Contacts[0].PendingSend = &pendingSend{State: "unknown", Draft: "May have been sent"}
			case "unknown-batch":
				issue.Supplements.Batches = []*issueSupplementBatch{{ID: "unknown", State: supplementBatchUnknown}}
			case "associated-dispatching-batch":
				issue.Supplements.Batches = []*issueSupplementBatch{{ID: "dispatching", State: supplementBatchDispatching, ExecutionID: "recorded-execution"}}
			}
			path := filepath.Join(t.TempDir(), "copy.json")
			original := recoveryWriteState(t, path, state)
			copy, err := inspectStoreRecoveryCopy(path)
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
