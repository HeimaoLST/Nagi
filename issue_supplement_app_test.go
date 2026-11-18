package main

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
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

func TestIssueSupplementUITesterPreviewCopyAndSaveFailure(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("UI supplement goal", "UI supplement requirement") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	issueID := issue.ID
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 1600)
	tr.Frame()
	expandIssueDetails(t, tr)
	if err := tr.Click("Add follow-up"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	*a.issueSupplementDraft(issueID) = "exact supplement text\n第二行"
	if err := tr.Click("Save follow-up"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Supplements == nil || issue.Supplements.pendingCount() != 1 {
		t.Fatal("UI add did not persist pending supplement")
	}
	if err := tr.Click("Preview full text"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Copy follow-up text"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != "exact supplement text\n第二行" {
		t.Fatalf("supplement copy = %q", got)
	}
	if err := tr.Click("Add follow-up"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	*a.issueSupplementDraft(issueID) = "must survive save failure"
	oldStore := a.store.path
	a.store.path = t.TempDir()
	if err := tr.Click("Save follow-up"); err != nil {
		t.Fatal(err)
	}
	a.store.path = oldStore
	tr.Frame()
	if *a.issueSupplementDraft(issueID) != "must survive save failure" ||
		issue.Supplements.pendingCount() != 1 {
		t.Fatalf("save failure lost draft/added item: %q / %d", *a.issueSupplementDraft(issueID), issue.Supplements.pendingCount())
	}
}

func TestIssueSupplementOwnerDraftsStayIsolatedAcrossContactSwitch(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("owner draft goal", "owner draft requirement") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	issueID := issue.ID
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 1600)
	tr.Frame()
	expandIssueDetails(t, tr)
	if err := tr.Click("Add follow-up"); err != nil {
		t.Fatal(err)
	}
	a.windowWidth = 1040
	*a.issueSupplementDraft(issueID) = "draft for X owner"
	a.addRuntimeContact()
	if a.selectedContact().ID == ct.ID {
		t.Fatal("owner switch did not select second contact")
	}
	a.selectContact(0)
	if got := *a.issueSupplementDraft(issueID); got != "draft for X owner" {
		t.Fatalf("owner-1 draft after switch = %q", got)
	}
}

func TestIssueSupplementUnknownDeliveryKeepsReceiptAndOffersNoRetry(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("unknown UI goal", "unknown UI requirement") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
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
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 1600)
	tr.Frame()
	expandIssueDetails(t, tr)
	if !tr.HasText(locales.In("en").SupplementUnknown()) || tr.HasText(locales.In("en").SupplementRetry()) || batch.State != supplementBatchUnknown || issue.Supplements.AutoContinue {
		t.Fatalf("unknown supplement did not preserve its visible delivery state: %v", tr.Texts())
	}
}

func TestIssueSupplementRetryWorkCapacityShowsCanonicalText(t *testing.T) {
	a, ct := taskContextApp(t)
	a.pi = &piAppHost{client: &pi.Client{}}
	a.runtimeStatus = runtimeStateReady
	a.runtimeMaxActiveTasks = 2
	if !a.createIssue("Capacity retry goal", "Keep the capacity notice localized") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	if issue == nil {
		t.Fatal("issue missing")
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
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	clickAfterBoundedScroll(t, tr, "Retry follow-up", 1040, 720)
	tr.Frame()

	rawCanonical := "Work capacity is full; one runtime slot is reserved for conversation."
	displayCanonical := "Work capacity is full; one runtime slot is reserved for chat."
	if tr.HasText(blockWorkCapacity) {
		t.Fatalf("bare blocker token rendered instead of canonical text: %v", tr.Texts())
	}
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
	tr.Scroll(850, 300, 0, -2000)
	tr.Frame()
	if r, ok := tr.Find(displayCanonical); !ok || r.W <= 0 || r.H <= 0 || r.X < 700 || r.X+r.W > 1040 || r.Y < 0 || r.Y+r.H > 720 {
		t.Fatalf("canonical blocked text is not visible after scrolling back: rect=%+v ok=%v", r, ok)
	}

	tr.SetLocale("zh-Hans")
	tr.Frame()
	if tr.HasText(blockWorkCapacity) || !tr.HasText("工作容量已满；一个运行时槽位保留给对话。") {
		t.Fatalf("Chinese capacity notice missing or leaked token: %v", tr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	if tr.HasText(blockWorkCapacity) || !tr.HasText(displayCanonical) {
		t.Fatalf("English capacity notice did not return: %v", tr.Texts())
	}
}
