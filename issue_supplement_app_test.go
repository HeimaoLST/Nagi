package main

import (
	"testing"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func TestIssueSupplementConfirmedRejectionStopsIntentAndKeepsText(t *testing.T) {
	a, ct, _ := piRunStartedTestApp(t)
	ct.ActiveTaskID, ct.SendState = "", sendIdle
	issue, err := a.createIssueFor(ct, "Reject goal", "Keep rejected text")
	if err != nil {
		t.Fatal(err)
	}
	target := issueSupplementTargetFor(ct, issue)
	if err := a.addIssueSupplement(ct, issue.ID, "rejected text"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: newIssueExecutionID(), PiIntent: true, Prompt: "pending", Status: issueExecutionPending, RequirementsRevision: issue.RequirementsRevision}
	issue.Executions = append(issue.Executions, execution)
	issue.Work.SendState = sendSubmitting
	issue.Work.pendingExecutionID = execution.ID
	issue.Status = issueStatusInProgress
	if _, err := issue.Supplements.PrepareDispatch(target, execution, a.persist); err != nil {
		t.Fatal(err)
	}
	a.failPiActivity(a.pi, ct.ID, issue.ID, execution.ID, &pi.CommandError{
		Method: "run.start", Delivery: pi.Rejected,
		Err: &pi.RemoteError{Code: "invalid_request", Message: "provider rejected"},
	}, false)
	if issue.Supplements.AutoContinue || issue.Supplements.Items[0].Text != "rejected text" ||
		issue.Supplements.Items[0].State != supplementStateRejected || execution.Status != issueExecutionRejected ||
		issue.Status != issueStatusNeedsAttention {
		t.Fatalf("confirmed rejection state = auto=%v item=%+v execution=%+v issue=%q",
			issue.Supplements.AutoContinue, issue.Supplements.Items[0], execution, issue.Status)
	}
}

func TestIssueSupplementSaveFailureKeepsSavedItems(t *testing.T) {
	a, ct := taskContextApp(t)
	issue, err := a.createIssueFor(ct, "Supplement goal", "Keep saved material")
	if err != nil {
		t.Fatal(err)
	}
	const text = "exact supplement text\n第二行"
	if err := a.addIssueSupplement(ct, issue.ID, text); err != nil {
		t.Fatal(err)
	}
	a.store.path = t.TempDir()
	if err := a.addIssueSupplement(ct, issue.ID, "save must fail"); err == nil {
		t.Fatal("supplement save unexpectedly succeeded")
	}
	if len(issue.Supplements.Items) != 1 || issue.Supplements.Items[0].Text != text || issue.Supplements.pendingCount() != 1 {
		t.Fatalf("failed save changed the original supplement: %+v", issue.Supplements)
	}
}
