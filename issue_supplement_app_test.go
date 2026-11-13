package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestIssueSupplementBusyAdmissionKeepsPendingAndAutomaticIntent(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("Busy goal", "Keep the supplement pending") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	if issue == nil {
		t.Fatal("issue missing")
	}
	target := issueSupplementTargetFor(ct, issue)
	if err := a.addIssueSupplement(ct, issue.ID, "wait for capacity"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "execution-busy", SessionID: "work-session", Prompt: "pending", Status: issueExecutionPending, RequirementsRevision: issue.RequirementsRevision}
	issue.Executions = append(issue.Executions, execution)
	issue.Work.SendState = sendSubmitting
	issue.Work.pendingExecutionID = execution.ID
	issue.Status = issueStatusInProgress
	if _, err := issue.Supplements.PrepareDispatch(target, execution, a.persist); err != nil {
		t.Fatal(err)
	}
	a.failIssueSubmit(ct.ID, issue.ID, &runtimeError{Code: "busy", Reason: "capacity_exceeded", Message: "runtime task capacity is full"}, true)
	if !issue.Supplements.AutoContinue || issue.Supplements.pendingCount() != 1 ||
		len(issue.Supplements.Batches) != 1 || issue.Supplements.Batches[0].State != supplementBatchCanceled ||
		issue.Supplements.Items[0].State != supplementStatePending ||
		execution.Status != issueExecutionNotDispatched || issue.Work.SendState != sendIdle ||
		issue.Work.SendError == "" || issue.Status != issueStatusInProgress {
		t.Fatalf("busy admission state = auto=%v pending=%d batch=%+v item=%+v execution=%+v work=%v/%q issue=%q",
			issue.Supplements.AutoContinue, issue.Supplements.pendingCount(), issue.Supplements.Batches,
			issue.Supplements.Items[0], execution, issue.Work.SendState, issue.Work.SendError, issue.Status)
	}
}

func TestIssueSupplementConfirmedRejectionStopsIntentAndKeepsText(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("Reject goal", "Keep rejected text") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	target := issueSupplementTargetFor(ct, issue)
	if err := a.addIssueSupplement(ct, issue.ID, "rejected text"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "execution-reject", SessionID: "work-session", Prompt: "pending", Status: issueExecutionPending, RequirementsRevision: issue.RequirementsRevision}
	issue.Executions = append(issue.Executions, execution)
	issue.Work.SendState = sendSubmitting
	issue.Work.pendingExecutionID = execution.ID
	issue.Status = issueStatusInProgress
	if _, err := issue.Supplements.PrepareDispatch(target, execution, a.persist); err != nil {
		t.Fatal(err)
	}
	a.failIssueSubmit(ct.ID, issue.ID, &runtimeError{Code: "invalid_request", Message: "provider rejected"}, true)
	if issue.Supplements.AutoContinue || issue.Supplements.Items[0].Text != "rejected text" ||
		issue.Supplements.Items[0].State != supplementStateRejected || execution.Status != issueExecutionRejected ||
		issue.Status != issueStatusNeedsAttention {
		t.Fatalf("confirmed rejection state = auto=%v item=%+v execution=%+v issue=%q",
			issue.Supplements.AutoContinue, issue.Supplements.Items[0], execution, issue.Status)
	}
}

func TestIssueSupplementAcceptedSaveFailureDisablesAutoWithoutBatch(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("Accept save failure", "Do not auto retry") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	if err := a.addIssueSupplement(ct, issue.ID, "pending after accepted failure"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "execution-accept-failure", SessionID: "work-session", Prompt: "prompt", Status: issueExecutionPending, RequirementsRevision: issue.RequirementsRevision}
	issue.Executions = append(issue.Executions, execution)
	issue.Work.SendState = sendSubmitting
	issue.Work.pendingExecutionID = execution.ID
	issue.Status = issueStatusInProgress
	badParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := writeBlockedPathForTest(badParent); err != nil {
		t.Fatal(err)
	}
	oldPath := a.store.path
	a.store.path = filepath.Join(badParent, "contacts.json")
	a.acceptIssueTask(ct, issue, execution, "prompt", runtimeSendResult{TaskID: "task-accepted", MessageID: "message-accepted"})
	a.store.path = oldPath
	if issue.Supplements.AutoContinue || issue.Supplements.pendingCount() != 1 ||
		execution.Status != issueExecutionUnknown || execution.TaskID != "task-accepted" ||
		execution.MessageID != "message-accepted" || issue.Work.SendError == "" {
		t.Fatalf("accept save failure = auto=%v pending=%d execution=%+v work=%q",
			issue.Supplements.AutoContinue, issue.Supplements.pendingCount(), execution, issue.Work.SendError)
	}
}

func writeBlockedPathForTest(path string) error {
	return os.WriteFile(path, []byte("not a directory"), 0o600)
}

func TestIssueSupplementConfirmNotDispatchedSaveFailureRestoresAllWorkState(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("confirm save failure", "keep state") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	if err := a.addIssueSupplement(ct, issue.ID, "unknown text"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "execution-confirm-fail", Status: issueExecutionUnknown, Detail: "unknown"}
	issue.Executions = append(issue.Executions, execution)
	issue.Work.SendState = sendUnknown
	issue.Work.setSendError("unknown delivery", "send-uncertain", "unknown detail")
	issue.Work.pendingExecutionID = execution.ID
	issue.Status = issueStatusNeedsAttention
	oldAuto := issue.Supplements.AutoContinue
	oldStore := a.store.path
	badParent := filepath.Join(t.TempDir(), "blocked-confirm-save")
	if err := writeBlockedPathForTest(badParent); err != nil {
		t.Fatal(err)
	}
	a.store.path = filepath.Join(badParent, "contacts.json")
	ok := a.confirmIssueExecutionNotDispatched(issue.ID, execution.ID)
	a.store.path = oldStore
	if ok || execution.Status != issueExecutionUnknown || execution.Detail != "unknown" ||
		issue.Work.SendState != sendUnknown || issue.Work.SendError != "unknown delivery" ||
		issue.Work.SendErrorKind != "send-uncertain" || issue.Work.SendErrorDetail != "unknown detail" ||
		issue.Work.pendingExecutionID != execution.ID || issue.Supplements.AutoContinue != oldAuto {
		t.Fatalf("failed confirm changed state: ok=%v execution=%+v work=%v/%q/%q/%q auto=%v",
			ok, execution, issue.Work.SendState, issue.Work.SendError, issue.Work.SendErrorKind,
			issue.Work.SendErrorDetail, issue.Supplements.AutoContinue)
	}
	openIssueDetailForTest(a, ct, issue.ID)
	a.setWorkspaceOpen(true)
	before := recoveryRecordSnapshot(t, ct)
	r := ui.NewTester(a.view, 1040, 720)
	r.SetLocale("zh-Hans")
	r.Frame()
	if !r.HasText(locales.In("zh-Hans").SendDisconnectedState()) || r.HasText(issue.Work.SendError) {
		t.Fatalf("confirm rollback lost the localized work notice: %v", r.Texts())
	}
	r = recoveryIssueTester(a, ct, issue, 1040, 900)
	r.SetLocale("zh-Hans")
	r.Frame()
	if err := r.Click(locales.In("zh-Hans").DetailsAction()); err != nil {
		t.Fatal(err)
	}
	r.Frame()
	if !r.HasText("unknown delivery") || recoveryRecordSnapshot(t, ct) != before || a.submissionInFlight != 0 {
		t.Fatal("issue-send disclosure lost the rolled-back diagnostic or changed work state")
	}
}

func supplementRawHistoryRecords(t *testing.T, taskID, messageID, body string, contexts []taskContextItem, withTaskStart bool) []historyRecord {
	t.Helper()
	parts := []map[string]any{{"type": "text", "text": body}}
	for _, item := range contexts {
		envelope, err := json.Marshal(map[string]any{
			"type": "caller_context", "label": "Caller-provided context (user data)",
			"name": item.Name, "text": item.Text,
		})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, map[string]any{"type": "text", "text": string(envelope)})
	}
	content, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	var records []historyRecord
	if withTaskStart {
		records = append(records, historyRecord{Record: journalRecord{Type: "task_started", TaskID: taskID, MessageID: messageID}})
	}
	records = append(records, historyRecord{Record: journalRecord{
		Type:  "messages",
		Items: []inputItem{{Type: "message", Role: "user", Content: content}},
	}})
	records = append(records, historyRecord{Record: journalRecord{Type: "task_finished", TaskID: taskID, MessageID: messageID, Status: "completed"}})
	return records
}

func TestIssueSupplementHistoryMatchesRealJournalShape(t *testing.T) {
	execution := &issueExecution{
		SessionID: "session-real", TaskID: "task-real", MessageID: "message-real", Prompt: "goal body",
		Context: []taskContextItem{{ID: "c1", Name: "ctx", Text: "context text", Revision: 1}},
	}
	records := supplementRawHistoryRecords(t, execution.TaskID, execution.MessageID, execution.Prompt, execution.Context, true)
	// The Messages journal record intentionally has no TaskID/MessageID fields:
	// ownership comes only from the preceding TaskStarted record.
	if records[1].Record.TaskID != "" || records[1].Record.MessageID != "" {
		t.Fatal("test fixture incorrectly put ownership on Messages")
	}
	if !supplementHistoryMatches(execution, records) {
		t.Fatal("real journal shape did not match exact Prompt/Context")
	}
	wrong := *execution
	wrong.Prompt = "different body"
	if supplementHistoryMatches(&wrong, records) {
		t.Fatal("incorrect body matched")
	}
	wrong = *execution
	wrong.Context = []taskContextItem{{ID: "c1", Name: "ctx", Text: "different context", Revision: 1}}
	if supplementHistoryMatches(&wrong, records) {
		t.Fatal("incorrect context matched")
	}
	if supplementHistoryMatches(execution, supplementRawHistoryRecords(t, execution.TaskID, execution.MessageID, execution.Prompt, execution.Context, false)) {
		t.Fatal("missing TaskStarted ownership matched")
	}
	assistantRecords := supplementRawHistoryRecords(t, execution.TaskID, execution.MessageID, execution.Prompt, execution.Context, true)
	assistantRecords[1].Record.Items[0].Role = "assistant"
	if supplementHistoryMatches(execution, assistantRecords) {
		t.Fatal("assistant text was accepted as recorded user input")
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

func TestIssueSupplementUnknownActionIsExposedWithoutFakeRecovery(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("unknown UI goal", "unknown UI requirement") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	if err := a.addIssueSupplement(ct, issue.ID, "unknown UI text"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "unknown-ui-exec", Prompt: "unknown UI prompt", Status: issueExecutionPending, RequirementsRevision: issue.RequirementsRevision}
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
	if err := tr.Click("Check run"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.issueActionError == "" || batch.State != supplementBatchUnknown {
		t.Fatalf("unknown action = error %q batch %q", a.issueActionError, batch.State)
	}
}

func TestIssueSupplementHistoryMatchesPersistedScopeWire(t *testing.T) {
	user := taskContextItem{ID: "ctx", Name: "user-note", Text: "user material", Revision: 1}
	scopeJSON := `{"ref":"scope-test","contact_id":"c1","conversation_session_id":"s1","turn_id":"t1","allow_create":true,"created_at":1}`
	scopeItem := taskContextItem{Name: issueToolScopeContextName, Text: scopeJSON}
	execution := &issueExecution{
		SessionID: "s1", TaskID: "task-1", MessageID: "message-1", Prompt: "body",
		Context: []taskContextItem{user}, ToolScopeRef: "scope-test", ToolScopeInput: scopeJSON,
	}
	records := supplementRawHistoryRecords(t, execution.TaskID, execution.MessageID, execution.Prompt, []taskContextItem{user, scopeItem}, true)
	if !supplementHistoryMatches(execution, records) {
		t.Fatal("persisted scope wire did not match exact history")
	}
	withoutScope := *execution
	withoutScope.ToolScopeRef, withoutScope.ToolScopeInput = "", ""
	if supplementHistoryMatches(&withoutScope, records) {
		t.Fatal("missing persisted scope was ignored")
	}
	tokenExecution := *execution
	tokenExecution.ToolScopeInput = "scope-test"
	tokenRecords := supplementRawHistoryRecords(t, execution.TaskID, execution.MessageID, execution.Prompt, []taskContextItem{user, taskContextItem{Name: issueToolScopeContextName, Text: "scope-test"}}, true)
	if !supplementHistoryMatches(&tokenExecution, tokenRecords) {
		t.Fatal("opaque token scope wire did not match exact history")
	}
}

func TestIssueSupplementRetryWorkCapacityShowsCanonicalText(t *testing.T) {
	a, ct := taskContextApp(t)
	a.rt = &runtimeClient{}
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
	execution := &issueExecution{ID: "execution-capacity-retry", SessionID: "work-capacity", Prompt: "capacity retry", Status: issueExecutionRejected, RequirementsRevision: issue.RequirementsRevision}
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

	active := &productIssue{ID: "issue-work-slot", ContactID: ct.ID, Goal: "active work slot", Status: issueStatusInProgress, WorkSessionID: "work-active-slot"}
	ct.Issues = append(ct.Issues, active)
	a.applyIssueEvent(ct, active, runtimeEventEnvelope{Version: 1, Type: "event", SessionID: "work-active-slot", TaskID: "task-active-slot", Event: &runtimeEvent{Type: "task_started"}})
	if active.Work.ActiveTaskID != "task-active-slot" || active.Work.SendState != sendAccepted {
		t.Fatalf("active work slot was not established: %+v", active.Work)
	}
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
