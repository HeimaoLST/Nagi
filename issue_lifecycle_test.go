package main

import (
	"strings"
	"testing"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func lifecycleTestIssue() (*appModel, *contact, *productIssue) {
	ct := &contact{ID: "c1", Name: "Saki", Loaded: true}
	issue := &productIssue{
		ID: "i1", ContactID: "c1", Goal: "implement login", Status: issueStatusNotStarted,
		RequirementsRevision: 1,
		Requirements:         parseIssueRequirements("support SMS\nshow errors"),
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	a := &appModel{contacts: []*contact{ct}, selected: 0, runtimeStatus: runtimeStateReady}
	return a, ct, issue
}

func TestIssueActionsRejectArchivedAndCompletedStart(t *testing.T) {
	_, _, issue := lifecycleTestIssue()
	issue.Archived = true
	if reason := issueLifecycleStartBlocked(issue); !strings.Contains(reason, "Restore") {
		t.Fatalf("archived start guard = %q", reason)
	}
	issue.Archived = false
	issue.Status = issueStatusCompleted
	if reason := issueLifecycleStartBlocked(issue); !strings.Contains(reason, "Reopen") {
		t.Fatalf("completed start guard = %q", reason)
	}
}

func TestStalePiSettlementPreservesActiveAndWaitingIssueStatus(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	a.pi = presentationPiHost(a)
	attachIssueToolTestExecution(issue)
	old := issue.Executions[0]
	old.Status = issueExecutionFailed
	bridge := &piIssueTools{
		epoch: old.PiRun.WorkerEpoch, binding: issue.PiBinding.Context,
		scope: IssueToolScope{WorkExecutionID: old.ID},
	}
	event := pi.Event{
		WorkerEpoch: old.PiRun.WorkerEpoch, ContextID: old.PiRun.ContextID, RunID: old.PiRun.RunID,
		Name: pi.RunSettled, Data: []byte(`{"status":"failed"}`),
	}
	issue.Status = issueStatusInProgress
	issue.Work.ActiveTaskID, issue.Work.SendState = "new-run", sendAccepted
	a.applyPiActivityEvent(a.pi, bridge, event)
	if issue.Status != issueStatusInProgress || issue.Work.ActiveTaskID != "new-run" || issue.Work.SendState != sendAccepted {
		t.Fatalf("stale Pi settlement changed active issue %s: status=%q work=%+v", issue.ID, issue.Status, issue.Work)
	}

	issue.Status = issueStatusWaitingUser
	issue.Work.ActiveTaskID, issue.Work.SendState = "", sendIdle
	a.applyPiActivityEvent(a.pi, bridge, event)
	if issue.Status != issueStatusWaitingUser || issue.Work.ActiveTaskID != "" || issue.Work.SendState != sendIdle {
		t.Fatalf("stale Pi settlement changed waiting issue %s: status=%q work=%+v", issue.ID, issue.Status, issue.Work)
	}
}
