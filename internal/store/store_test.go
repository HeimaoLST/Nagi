package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
)

func validRecoveryStateFixture() StoredState {
	emptyTools := []string{}
	return StoredState{
		Version:  ContactStoreVersion,
		Selected: "bot-1",
		Contacts: []StoredContact{
			{
				ID:           "bot-1",
				Name:         "Writer",
				BuiltinTools: &emptyTools,
				SessionID:    "sess-1",
				PiBinding: &PiConversationBinding{
					Engine: "pi",
					Context: pi.ContextBinding{
						ContextID:         "ctx-1",
						BotID:             "bot-1",
						Purpose:           pi.Conversation,
						SelectionRevision: 1,
						CWD:               "/tmp/work",
						SessionRef:        "sess-1",
					},
					Selection:     PiSelection{CWD: "/tmp/work", CodingTools: []string{"read"}},
					Configuration: PiConfiguration{WireAPI: pi.OpenAICompletions, BaseURL: "https://example.com/v1", Model: "gpt-4o", ContextTokens: 8192, OutputTokens: 1024, CredentialSource: "src-1"},
				},
				TaskContext: []TaskContextItem{{ID: "c1", Name: "Spec", Text: "notes", Revision: 1}},
			},
			{
				ID:           "bot-2",
				Name:         "Reviewer",
				BuiltinTools: nil,
			},
		},
		Issues: []StoredIssue{
			{
				ID:                   "act-1",
				ContactID:            "bot-1",
				Goal:                 "Draft summary",
				RequirementsRevision: 1,
				RequirementItems:     []StoredRequirement{{ID: "r1", Text: "Keep short", State: RequirementUnverified}},
				Status:               IssueStatusInProgress,
				WorkSessionID:        "act-sess-1",
				PiBinding: &PiActivityBinding{
					Engine: "pi",
					Context: pi.ContextBinding{
						ContextID:         "ctx-act-1",
						BotID:             "bot-1",
						Purpose:           pi.Activity,
						ActivityID:        "act-1",
						SelectionRevision: 1,
						CWD:               "/tmp/work",
						SessionRef:        "act-sess-1",
					},
					Selection:     PiSelection{CWD: "/tmp/work"},
					Configuration: PiConfiguration{WireAPI: pi.OpenAICompletions, BaseURL: "https://example.com/v1", Model: "gpt-4o", ContextTokens: 8192, OutputTokens: 1024, CredentialSource: "src-1"},
				},
				Executions: []StoredIssueExecution{
					{
						ID:        "exec-1",
						SessionID: "act-sess-1",
						TaskID:    "run-1",
						Status:    IssueExecutionCompleted,
						PiIntent:  true,
						PiRun: &PiActivityRun{
							WorkerEpoch: "epoch-1",
							ContextID:   "ctx-act-1",
							SessionRef:  "act-sess-1",
							RunID:       "run-1",
							Admitted:    true,
						},
					},
				},
			},
		},
		IssueToolOperations: &IssueToolOperationLedger{
			Operations: []*IssueToolOperation{
				{
					ID:       "op-1",
					ScopeRef: "scope-1",
					TurnID:   "turn-1",
					Action:   IssueToolActionCreate,
					Receipt:  &IssueToolReceipt{State: IssueToolReceiptSaved, IssueID: "act-1"},
				},
			},
		},
		IssueToolScopes: []IssueToolScope{
			{
				Ref:              "scope-1",
				ContactID:        "bot-1",
				RuntimeSessionID: "sess-1",
				TurnID:           "turn-1",
				AllowedIssueIDs:  []string{"act-1"},
				CreatedAt:        100,
			},
		},
		Layout:     &StoredLayout{LeftWidth: theme.DefaultLeftPaneWidth, RightWidth: theme.DefaultRightPaneWidth, WorkspaceOpen: true},
		ThemeIDs:   &theme.AppearanceThemeIDs{Light: "light", Dark: "dark"},
		Appearance: theme.AppearanceSystem,
	}
}

func TestCloneStoredStateIsolationAndNilVsEmpty(t *testing.T) {
	orig := validRecoveryStateFixture()
	cloned := CloneStoredState(orig)

	if cloned.Contacts[0].BuiltinTools == nil || len(*cloned.Contacts[0].BuiltinTools) != 0 {
		t.Fatalf("expected non-nil empty BuiltinTools slice preserved, got %#v", cloned.Contacts[0].BuiltinTools)
	}
	if cloned.Contacts[1].BuiltinTools != nil {
		t.Fatalf("expected nil BuiltinTools preserved, got %#v", cloned.Contacts[1].BuiltinTools)
	}

	cloned.Contacts[0].Name = "Modified"
	*cloned.Contacts[0].BuiltinTools = append(*cloned.Contacts[0].BuiltinTools, "bash")
	cloned.Contacts[0].TaskContext[0].Text = "mutated"
	cloned.Contacts[0].PiBinding.Selection.CodingTools[0] = "write"
	cloned.Issues[0].RequirementItems[0].Text = "mutated"
	cloned.Issues[0].Executions[0].PiRun.RunID = "mutated"
	cloned.IssueToolOperations.Operations[0].Receipt.State = IssueToolReceiptUnknown
	cloned.IssueToolScopes[0].AllowedIssueIDs[0] = "act-other"
	cloned.Layout.LeftWidth = theme.MaxLeftPaneWidth
	cloned.ThemeIDs.Light = "other"

	if orig.Contacts[0].Name != "Writer" || len(*orig.Contacts[0].BuiltinTools) != 0 ||
		orig.Contacts[0].TaskContext[0].Text != "notes" ||
		orig.Contacts[0].PiBinding.Selection.CodingTools[0] != "read" ||
		orig.Issues[0].RequirementItems[0].Text != "Keep short" ||
		orig.Issues[0].Executions[0].PiRun.RunID != "run-1" ||
		orig.IssueToolOperations.Operations[0].Receipt.State != IssueToolReceiptSaved ||
		orig.IssueToolScopes[0].AllowedIssueIDs[0] != "act-1" ||
		orig.Layout.LeftWidth != theme.DefaultLeftPaneWidth ||
		orig.ThemeIDs.Light != "light" {
		t.Fatalf("mutating CloneStoredState modified original state: %+v", orig)
	}
}

func TestDecodeAndValidateRecoveryState(t *testing.T) {
	valid := validRecoveryStateFixture()
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStoredState("contacts.json", data, true)
	if err != nil {
		t.Fatalf("DecodeStoredState valid failed: %v", err)
	}
	if err := ValidateRecoveryState(decoded); err != nil {
		t.Fatalf("ValidateRecoveryState valid failed: %v", err)
	}

	if _, err := DecodeStoredState("contacts.json", []byte{0xff, 0xfe, '{', '}'}, true); err == nil {
		t.Fatal("expected strict decode to reject invalid UTF-8")
	}
	withExtraField := strings.Replace(string(data), `"version":10`, `"version":10,"extra_unknown":1`, 1)
	if _, err := DecodeStoredState("contacts.json", []byte(withExtraField), true); err == nil {
		t.Fatal("expected strict decode to reject unknown JSON field")
	}
	if _, err := DecodeStoredState("contacts.json", append(slices.Clone(data), []byte(" {}")...), true); err == nil {
		t.Fatal("expected strict decode to reject trailing JSON object")
	}

	cases := []struct {
		name   string
		mutate func(*StoredState)
	}{
		{"wrong-version", func(s *StoredState) { s.Version = 9 }},
		{"missing-selected", func(s *StoredState) { s.Selected = "missing" }},
		{"submitting-pending-send", func(s *StoredState) { s.Contacts[0].PendingSend = &PendingSend{State: "submitting"} }},
		{"nil-operations-ledger", func(s *StoredState) { s.IssueToolOperations = nil }},
		{"duplicate-scope-ref", func(s *StoredState) { s.IssueToolScopes = append(s.IssueToolScopes, s.IssueToolScopes[0]) }},
		{"zero-requirements-rev", func(s *StoredState) { s.Issues[0].RequirementsRevision = 0 }},
		{"pending-execution", func(s *StoredState) { s.Issues[0].Executions[0].Status = IssueExecutionPending }},
		{"invalid-appearance", func(s *StoredState) { s.Appearance = "sepia" }},
		{"invalid-layout-left", func(s *StoredState) { s.Layout.LeftWidth = 10 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := CloneStoredState(valid)
			tc.mutate(&candidate)
			if err := ValidateRecoveryState(candidate); err == nil {
				t.Fatalf("expected ValidateRecoveryState failure for %s", tc.name)
			}
		})
	}
}

func TestStoredPiIssueValidAndToolReceipts(t *testing.T) {
	valid := validRecoveryStateFixture()
	if !StoredPiIssueValid(valid.Issues[0], valid.IssueToolOperations) {
		t.Fatal("expected valid issue to pass StoredPiIssueValid")
	}

	brokenWorkSession := CloneStoredState(valid)
	brokenWorkSession.Issues[0].PiBinding = nil
	if StoredPiIssueValid(brokenWorkSession.Issues[0], valid.IssueToolOperations) {
		t.Fatal("expected nil PiBinding with non-empty WorkSessionID to fail")
	}

	scope := valid.IssueToolScopes[0]
	if !scope.AllowsIssueID("act-1") || scope.AllowsIssueID("act-2") {
		t.Fatalf("AllowsIssueID unexpected behavior for %+v", scope)
	}
	if err := ValidateIssueToolReceipt(&IssueToolReceipt{State: IssueToolReceiptAccepted, IssueID: "act-1", ExecutionID: "exec-1"}); err == nil {
		t.Fatal("expected accepted receipt without PiRun to fail")
	}
	accepted := &IssueToolReceipt{
		State:       IssueToolReceiptAccepted,
		IssueID:     "act-1",
		ExecutionID: "exec-1",
		SessionID:   "sess-1",
		TaskID:      "run-1",
		PiRun: &PiActivityRun{
			WorkerEpoch: "epoch-1",
			ContextID:   "ctx-1",
			SessionRef:  "sess-1",
			RunID:       "run-1",
			Admitted:    true,
		},
	}
	if err := ValidateIssueToolReceipt(accepted); err != nil {
		t.Fatalf("expected valid accepted receipt, got %v", err)
	}
}

func TestProfileJSONAndProductUIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "contacts.json")
	if got, err := ReadProductUI(profilePath); err != nil || got != nil {
		t.Fatalf("missing ReadProductUI = (%+v, %v), want (nil, nil)", got, err)
	}

	uiPath := ProductUIPath(profilePath)
	want := ProductUIState{
		Selected:   "bot-1",
		Appearance: theme.AppearanceDark,
		Pending:    map[string]ProductPendingSend{"bot-1": {ConversationID: "c1", OperationID: "op-1"}},
	}
	if err := WriteProfileJSON(uiPath, want); err != nil {
		t.Fatalf("WriteProfileJSON failed: %v", err)
	}
	info, err := os.Stat(uiPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("ui file mode = %o, want 0600", info.Mode().Perm())
	}
	got, err := ReadProductUI(profilePath)
	if err != nil || got == nil || got.Selected != "bot-1" || got.Pending["bot-1"].OperationID != "op-1" {
		t.Fatalf("ReadProductUI round-trip = (%+v, %v)", got, err)
	}
}
