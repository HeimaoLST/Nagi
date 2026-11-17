package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestContactManagementVersionSixRemainsReadOnlyWithOwnedExecutionData(t *testing.T) {
	contactInput := taskContextItem{ID: "contact-input", Name: "Contact material", Text: "unsent contact material", Revision: 2}
	issueInput := taskContextItem{ID: "issue-input", Name: "Issue material", Text: "execution material", Revision: 4}
	legacy := storedState{
		Version: 6, Selected: "contact-legacy",
		Contacts: []storedContact{{
			ID: "contact-legacy", Name: "Legacy", SessionID: "conversation-legacy", Draft: "unsent draft",
			TaskContext: []taskContextItem{contactInput},
			PendingSend: &pendingSend{Draft: "unconfirmed send", Context: []taskContextItem{contactInput}, State: "unknown"},
		}},
		Issues: []storedIssue{{
			ID: "issue-legacy", ContactID: "contact-legacy", Goal: "Keep ownership", Status: issueStatusNeedsAttention,
			WorkSessionID: "work-legacy", RequirementsRevision: 3,
			RequirementItems: []storedRequirement{{ID: "requirement-legacy", Text: "Keep evidence", State: "unverified"}},
			TaskContext:      []taskContextItem{issueInput},
			Executions: []storedIssueExecution{{
				ID: "execution-legacy", SessionID: "work-legacy", TaskID: "task-legacy", Status: issueExecutionCompleted,
				RequirementsRevision: 2, Context: []taskContextItem{issueInput}, SupplementBatchID: "batch-legacy",
			}},
			Supplements: &issueSupplementLedger{
				Items: []*issueSupplement{{
					ID: "supplement-legacy", ContactID: "contact-legacy", IssueID: "issue-legacy", Text: "submitted material",
					RequirementsRevision: 2, State: supplementStateSubmitted, BatchID: "batch-legacy",
					SessionID: "work-legacy", TaskID: "task-legacy", MessageID: "message-legacy", ExecutionID: "execution-legacy",
				}},
				Batches: []*issueSupplementBatch{{
					ID: "batch-legacy", Target: issueSupplementTarget{ContactID: "contact-legacy", IssueID: "issue-legacy", RequirementsRevision: 2},
					Snapshot: []issueSupplementSnapshot{{ID: "supplement-legacy", Text: "submitted material", RequirementsRevision: 2}},
					State:    supplementBatchAccepted, ExecutionID: "execution-legacy", SessionID: "work-legacy", TaskID: "task-legacy", MessageID: "message-legacy",
				}},
			},
		}},
	}
	path := filepath.Join(t.TempDir(), "contacts.json")
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store, contacts: contactsFromStore(store), selected: 0}
	a.loadStoredTaskContexts(store)
	a.bindTaskContexts()
	before := recoverySavedBytes(t, path)
	a.openSettings()
	if a.saveContactProfileDraft(profileDraft{Name: "Legacy renamed"}, false) {
		t.Fatal("legacy data accepted a profile mutation before its history policy was selected")
	}
	if string(recoverySavedBytes(t, path)) != string(before) {
		t.Fatal("rejected legacy profile save changed the original bytes")
	}
	reloaded, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.state
	if got.Version != legacy.Version || got.Selected != legacy.Selected || len(got.Contacts) != 1 || len(got.Issues) != 1 {
		t.Fatalf("legacy read-only load changed ownership or schema: %+v", got)
	}
	ct := got.Contacts[0]
	if ct.Name != legacy.Contacts[0].Name || ct.SessionID != legacy.Contacts[0].SessionID || ct.Draft != legacy.Contacts[0].Draft ||
		!reflect.DeepEqual(ct.TaskContext, legacy.Contacts[0].TaskContext) || !reflect.DeepEqual(ct.PendingSend, legacy.Contacts[0].PendingSend) {
		t.Error("profile save over v6 changed the current session, draft, Context or unconfirmed send")
	}
	issue := got.Issues[0]
	want := legacy.Issues[0]
	if issue.ID != want.ID || issue.ContactID != want.ContactID || issue.WorkSessionID != want.WorkSessionID || issue.RequirementsRevision != want.RequirementsRevision ||
		!reflect.DeepEqual(issue.RequirementItems, want.RequirementItems) || !reflect.DeepEqual(issue.TaskContext, want.TaskContext) ||
		!reflect.DeepEqual(issue.Executions, want.Executions) || !reflect.DeepEqual(issue.Supplements, want.Supplements) {
		t.Error("v6 upgrade changed requirements, execution snapshots or supplement receipt associations")
	}
}
