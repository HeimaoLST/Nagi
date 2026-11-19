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

func TestIssueSupplementUnknownDeliveryKeepsReceiptAndOffersNoRetry(t *testing.T) {
	a, ct := taskContextApp(t)
	issue, err := a.createIssueFor(ct, "Unknown delivery goal", "Keep uncertain material")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.addIssueSupplement(ct, issue.ID, "unknown UI text"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "unknown-ui-exec", PiIntent: true, Prompt: "unknown UI prompt", Status: issueExecutionUnknown, RequirementsRevision: issue.RequirementsRevision}
	issue.Executions = append(issue.Executions, execution)
	batch, err := issue.Supplements.PrepareDispatch(issueSupplementTargetFor(ct, issue), execution, a.persist)
	if err != nil {
		t.Fatal(err)
	}
	if !issue.Supplements.Unknown(batch, "unknown UI delivery", a.persist) {
		t.Fatal("unknown transition failed")
	}
	if a.retryIssueSupplementBatch(ct, issue, batch) || batch.State != supplementBatchUnknown || issue.Supplements.AutoContinue || len(issue.Executions) != 1 {
		t.Fatalf("unknown supplement lost its receipt or admitted a retry: %+v", batch)
	}
}

func TestIssueSupplementRetryPreservesStateAtWorkCapacity(t *testing.T) {
	a, ct := taskContextApp(t)
	a.pi = &piAppHost{client: &pi.Client{}}
	a.runtimeStatus = runtimeStateReady
	a.runtimeMaxActiveTasks = 2
	issue, err := a.createIssueFor(ct, "Capacity retry goal", "Keep the pending supplement")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.addIssueSupplement(ct, issue.ID, "retry under work capacity"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "execution-capacity-retry", PiIntent: true, Prompt: "capacity retry", Status: issueExecutionRejected, RequirementsRevision: issue.RequirementsRevision}
	issue.Executions = append(issue.Executions, execution)
	batch, err := issue.Supplements.PrepareDispatch(issueSupplementTargetFor(ct, issue), execution, a.persist)
	if err != nil {
		t.Fatal(err)
	}
	if !issue.Supplements.Reject(batch, "controlled rejection", a.persist) {
		t.Fatal("controlled rejection failed")
	}
	issue.Status = issueStatusInProgress
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}

	active := &productIssue{ID: "issue-work-slot", ContactID: ct.ID, Goal: "active work slot", Status: issueStatusInProgress}
	attachIssueToolTestExecution(active)
	active.Executions[0].Status = issueExecutionRunning
	active.Work.ActiveTaskID, active.Work.SendState = active.Executions[0].PiRun.RunID, sendAccepted
	ct.Issues = append(ct.Issues, active)
	if got := a.canStartIssue(ct, issue); got != blockWorkCapacity {
		t.Fatalf("precondition canStartIssue = %q, want %q", got, blockWorkCapacity)
	}

	beforeExecutions := len(issue.Executions)
	if a.retryIssueSupplementBatch(ct, issue, batch) {
		t.Fatal("retry admitted work at full capacity")
	}
	const rawCanonical = "Work capacity is full; one runtime slot is reserved for conversation."
	if a.issueActionError != rawCanonical || a.issueActionNotice != blockWorkCapacity {
		t.Fatalf("retry notice raw/kind = %q / %q", a.issueActionError, a.issueActionNotice)
	}
	if issue.Work.SendError != rawCanonical || issue.Work.SendErrorKind != blockWorkCapacity {
		t.Fatalf("retry work raw/kind = %q / %q", issue.Work.SendError, issue.Work.SendErrorKind)
	}
	if len(issue.Executions) != beforeExecutions || issue.Work.ActiveTaskID != "" || issue.Work.SendState != sendIdle {
		t.Fatalf("refused retry changed execution/work facts: executions=%d/%d work=%+v", len(issue.Executions), beforeExecutions, issue.Work)
	}
	if batch.State != supplementBatchCanceled || issue.Supplements.pendingCount() != 1 {
		t.Fatalf("refused retry changed batch/items: batch=%q pending=%d", batch.State, issue.Supplements.pendingCount())
	}
}
