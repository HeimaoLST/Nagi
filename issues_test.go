package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func attachIssueToolTestExecution(issue *productIssue) {
	root := "/tmp"
	run := &piActivityRun{WorkerEpoch: "fixture", ContextID: "activity-" + issue.ID, SessionRef: "work-session", RunID: "work-task", Admitted: true}
	issue.PiBinding = &piActivityBinding{Engine: "pi", Context: pi.ContextBinding{ContextID: run.ContextID, BotID: issue.ContactID, Purpose: pi.Activity, ActivityID: issue.ID, SessionRef: run.SessionRef, SelectionRevision: 1, CWD: root}, Selection: piSelection{CWD: root}, Configuration: piConfiguration{CredentialSource: "fixture"}}
	issue.WorkSessionID = run.SessionRef
	issue.Executions = append(issue.Executions, &issueExecution{
		ID: "execution-test", SessionID: run.SessionRef, TaskID: run.RunID, PiIntent: true, PiRun: run,
		Status: issueExecutionCompleted, RequirementsRevision: issue.RequirementsRevision,
	})
}

func TestIssueStoreRoundTripKeepsPiConversationAndDraft(t *testing.T) {
	a, ct, _ := piRunStartedTestApp(t)
	ct.ActiveTaskID, ct.SendState, ct.Draft = "", sendIdle, "preserved draft"
	path, sessionID := a.store.path, ct.SessionID
	issue, err := a.createIssueFor(ct, "Implement login", "Use SMS verification.")
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Version != contactStoreVersion {
		t.Fatalf("saved version = %d", reloaded.state.Version)
	}
	if len(reloaded.state.Issues) != 1 {
		t.Fatalf("stored issues = %+v", reloaded.state.Issues)
	}
	stored := reloaded.state.Issues[0]
	if stored.ID == "" || stored.ContactID != ct.ID || stored.Goal != "Implement login" ||
		len(stored.RequirementItems) != 1 ||
		stored.RequirementItems[0].Text != "Use SMS verification." ||
		stored.Status != issueStatusNotStarted {
		t.Fatalf("stored issue = %+v", stored)
	}
	if reloaded.state.Contacts[0].Draft != "preserved draft" || reloaded.state.Contacts[0].SessionID != sessionID || reloaded.state.Contacts[0].PiBinding == nil {
		t.Fatalf("saved Pi conversation or draft changed: %+v", reloaded.state.Contacts[0])
	}
	if stored.ID != issue.ID {
		t.Fatalf("reloaded issue ID = %q, want %q", stored.ID, issue.ID)
	}
}

func TestCreateIssueRequiresGoalAndPersistsBeforeAnyRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store, contacts: []*contact{{ID: "c1", Name: "Saki", Workspace: "/tmp"}}}
	ct := a.selectedContact()
	if _, err := a.createIssueFor(ct, "", "requirement"); err == nil || len(ct.Issues) != 0 {
		t.Fatalf("empty goal left state: issues=%v error=%v", ct.Issues, err)
	}
	issue, err := a.createIssueFor(ct, "Implement login", "Must support SMS.")
	if err != nil {
		t.Fatal(err)
	}
	if issue.ContactID != "c1" || issue.Goal != "Implement login" || issueRequirementsText(issue) != "Must support SMS." {
		t.Fatalf("issue fields = %+v", issue)
	}
	if len(issue.Executions) != 0 {
		t.Fatalf("creating an item must not create an execution: %+v", issue.Executions)
	}
	reopened, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.state.Issues) != 1 || reopened.state.Issues[0].ID != issue.ID || len(reopened.state.Issues[0].Executions) != 0 {
		t.Fatalf("saved issues = %+v", reopened.state.Issues)
	}
}

func TestCreateIssueFailureKeepsExistingIssues(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Nagi", Draft: "unsent chat"}
	a := &appModel{store: &contactStore{path: t.TempDir()}, contacts: []*contact{ct}}
	if _, err := a.createIssueFor(ct, "Implement login", "SMS required"); err == nil {
		t.Fatal("createIssueFor unexpectedly succeeded with a failing store")
	}
	if len(ct.Issues) != 0 || ct.Draft != "unsent chat" {
		t.Fatalf("failed save changed issues or chat draft: %+v", ct)
	}
}

func TestStoreRejectsIssueWithoutValidContact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	state := storedState{
		Version: contactStoreVersion, Selected: "c1",
		Contacts: []storedContact{{ID: "c1", Name: "Cedar"}},
		Issues:   []storedIssue{{ID: "i1", Goal: "orphan"}},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openContactStore(path, "/tmp"); err == nil || !strings.Contains(err.Error(), "unknown contact id") {
		t.Fatalf("orphan issue error = %v", err)
	}
}

func TestStoredPendingExecutionIsUnknownAfterRestart(t *testing.T) {
	issues := issuesForStoredContact([]storedIssue{{
		ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusNotStarted,
		Executions: []storedIssueExecution{{ID: "e1", TaskID: "task-possible", Prompt: "work", Status: issueExecutionPending}},
	}}, "c1")
	if len(issues) != 1 || len(issues[0].Executions) != 1 {
		t.Fatalf("issues = %+v", issues)
	}
	if got := issues[0].Executions[0].Status; got != issueExecutionUnknown {
		t.Fatalf("restart pending status = %q, want unknown", got)
	}
	if reason := issueRunBlocked(issues[0]); reason == "" {
		t.Fatal("unknown execution must block blind Run")
	}
}

func TestRestoredNonterminalExecutionWithoutTaskIsUnknown(t *testing.T) {
	for _, status := range []string{issueExecutionPending, issueExecutionAccepted, issueExecutionRunning} {
		issues := issuesForStoredContact([]storedIssue{{
			ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusNotStarted,
			Executions: []storedIssueExecution{{ID: "e1", Prompt: "work", Status: status}},
		}}, "c1")
		execution := issues[0].Executions[0]
		if execution.Status != issueExecutionUnknown || issueRunBlocked(issues[0]) == "" {
			t.Fatalf("%s without task restored as %q", status, execution.Status)
		}
	}
	issues := issuesForStoredContact([]storedIssue{{
		ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusNotStarted,
		Executions: []storedIssueExecution{{ID: "e1", TaskID: "t1", Prompt: "work", Status: issueExecutionRunning}},
	}}, "c1")
	if e := issues[0].Executions[0]; e.TaskID != "t1" || e.Status != issueExecutionUnknown || issueRunBlocked(issues[0]) == "" {
		t.Fatalf("unbound execution did not preserve its task ID and block replay: %+v", e)
	}
}

func TestContactActivityObservesIssueWorkTarget(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", Loaded: true}
	a := &appModel{contacts: []*contact{ct}, runtimeStatus: runtimeStateReady}
	if active, unknown := a.contactActivity(ct); active || unknown {
		t.Fatal("contact with no issue work was active")
	}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "work", WorkSessionID: "work-session"}
	issue.Work.SendState = sendAccepted
	issue.Work.ActiveTaskID = "task-1"
	ct.Issues = []*productIssue{issue}
	if active, unknown := a.contactActivity(ct); !active || unknown {
		t.Fatalf("active issue work was not observed: active=%v unknown=%v", active, unknown)
	}
}

func TestIssueWorkNoticeKeepsUnseenOlderExceptionAfterSuccess(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", Loaded: true}
	issue := &productIssue{ID: "i-old-exception", ContactID: ct.ID, Goal: "older failed work"}
	issue.Executions = []*issueExecution{
		{ID: "e-old", TaskID: "t-old", Status: issueExecutionFailed, Detail: "old unseen failure"},
		{ID: "e-new", TaskID: "t-new", Status: issueExecutionCompleted},
	}
	ct.Issues = []*productIssue{issue}
	a := &appModel{contacts: []*contact{ct}, selected: 0}
	notice := a.issueWorkNotice(ct)
	if !notice.Attention || notice.IssueID != issue.ID || !strings.Contains(notice.AttentionReason, "old unseen failure") {
		t.Fatalf("later success hid an unseen older exception: %+v", notice)
	}
	issue.Executions[0].AttentionSeenStatus = issueExecutionFailed
	if notice := a.issueWorkNotice(ct); notice.IssueID != "" {
		t.Fatalf("seen older exception was re-lit after success: %+v", notice)
	}
}

func TestIssueSeenUnknownWithActiveTaskIDDoesNotReAttention(t *testing.T) {
	ct := &contact{ID: "contact-seen-active-unknown", Name: "Seen Unknown", Loaded: true}
	execution := &issueExecution{
		ID: "run-seen-active-unknown", TaskID: "task-seen-active-unknown",
		Status: issueExecutionUnknown, Detail: "unknown already seen",
		AttentionSeenStatus: issueExecutionUnknown,
	}
	issue := &productIssue{
		ID: "issue-seen-active-unknown", ContactID: ct.ID, Goal: "seen unknown with active mapping",
		Status: issueStatusInProgress,
		Work: issueWorkSession{Loaded: true, ActiveTaskID: "task-seen-active-unknown", LastTaskID: "task-seen-active-unknown",
			SendState: sendUnknown, SendError: "delivery not confirmed"},
		Executions: []*issueExecution{execution},
	}
	ct.Issues = []*productIssue{issue}
	display := issueDisplayStatusFor(issue)
	if display.Attention {
		t.Fatalf("seen unknown with retained ActiveTaskID re-raised attention: %+v", display)
	}
	notice := (&appModel{}).issueWorkNotice(ct)
	if notice.Attention || notice.IssueID != "" {
		t.Fatalf("seen unknown with retained ActiveTaskID re-raised notice: %+v", notice)
	}
	if issue.Work.ActiveTaskID != "task-seen-active-unknown" || issue.Work.SendState != sendUnknown ||
		execution.AttentionSeenStatus != issueExecutionUnknown || execution.Status != issueExecutionUnknown {
		t.Fatalf("seen unknown state was rewritten: work=%+v run=%+v", issue.Work, execution)
	}
}

func TestIssueDisplayProjectionUsesExistingFacts(t *testing.T) {
	ct := &contact{ID: "contact-projection", Name: "Projection", Loaded: true}
	completed := &productIssue{ID: "issue-completed", ContactID: ct.ID, Goal: "completed goal", Status: issueStatusCompleted,
		Executions: []*issueExecution{{ID: "run-completed-old", Status: issueExecutionFailed, Detail: "old failure"}}}
	canceled := &productIssue{ID: "issue-canceled", ContactID: ct.ID, Goal: "canceled goal", Status: issueStatusCanceled,
		Executions: []*issueExecution{{ID: "run-canceled-old", Status: issueExecutionUnknown, Detail: "old unknown"}}}
	archived := &productIssue{ID: "issue-archived", ContactID: ct.ID, Goal: "archived goal", Status: issueStatusInProgress, Archived: true,
		Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-archived-active", SendState: sendAccepted}}
	waiting := &productIssue{ID: "issue-waiting", ContactID: ct.ID, Goal: "waiting goal", Status: issueStatusInProgress,
		Questions: []*issueQuestion{{Ref: "q-waiting", Text: "answer this", State: issueQuestionOpen}}}
	attention := &productIssue{ID: "issue-attention", ContactID: ct.ID, Goal: "attention goal", Status: issueStatusInProgress,
		Executions: []*issueExecution{{ID: "run-attention", Status: issueExecutionFailed, Detail: "unseen failure"}}}
	active := &productIssue{ID: "issue-active", ContactID: ct.ID, Goal: "active goal", Status: issueStatusInProgress,
		Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-active", SendState: sendAccepted}}
	ct.Issues = []*productIssue{completed, canceled, archived, waiting, attention, active}
	notice := (&appModel{}).issueWorkNotice(ct)
	if got := issueDisplayStatusFor(completed).Label; got != "Completed" {
		t.Fatalf("completed label = %q", got)
	}
	if got := issueDisplayStatusFor(canceled).Label; got != "Canceled" {
		t.Fatalf("canceled label = %q", got)
	}
	if got := issueDisplayStatusFor(archived); got.Label != "Archived" || got.Attention || got.Waiting || got.Active {
		t.Fatalf("archived projection = %+v", got)
	}
	if got := issueDisplayStatusFor(waiting); got.Label != issueStatusLabel(&productIssue{Status: issueStatusWaitingUser}) || !got.Waiting {
		t.Fatalf("waiting projection = %+v", got)
	}
	if got := issueDisplayStatusFor(attention); got.Label != "Needs attention" || !got.Attention {
		t.Fatalf("attention projection = %+v", got)
	}
	if got := issueDisplayStatusFor(active); got.Label != "In progress" || got.Status != issueStatusInProgress || !got.Active {
		t.Fatalf("active projection = %+v", got)
	}
	if notice.AttentionCount != 1 || notice.WaitingCount != 1 || notice.ActiveCount != 1 ||
		!notice.Attention || notice.IssueID != attention.ID || notice.IssueName != attention.Goal {
		t.Fatalf("notice aggregation = %+v", notice)
	}
}

func TestIssueExplicitNeedsAttentionSurvivesSeenRun(t *testing.T) {
	ct := &contact{ID: "contact-explicit-attention", Name: "Explicit", Loaded: true}
	execution := &issueExecution{ID: "run-explicit", Status: issueExecutionFailed, Detail: "seen failure", AttentionSeenStatus: issueExecutionFailed}
	issue := &productIssue{ID: "issue-explicit", ContactID: ct.ID, Goal: "explicit attention", Status: issueStatusNeedsAttention,
		Work: issueWorkSession{Loaded: true}, Executions: []*issueExecution{execution}}
	ct.Issues = []*productIssue{issue}
	display := issueDisplayStatusFor(issue)
	if !display.Attention || display.Label != "Needs attention" {
		t.Fatalf("explicit needs_attention was cleared by a seen run: %+v", display)
	}
	notice := (&appModel{}).issueWorkNotice(ct)
	if !notice.Attention || notice.AttentionCount != 1 || notice.IssueID != issue.ID {
		t.Fatalf("explicit needs_attention was cleared from aggregation: %+v", notice)
	}
	if issue.Status != issueStatusNeedsAttention || execution.AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("seen field or persistent status was rewritten: issue=%+v run=%+v", issue, execution)
	}
}

func TestIssueWorkNoticeBaselineTerminalAndWaiting(t *testing.T) {
	ct := &contact{ID: "contact-baseline", Name: "Baseline", Loaded: true}
	completed := &productIssue{ID: "issue-baseline-completed", ContactID: ct.ID, Goal: "completed old",
		Status: issueStatusCompleted, Executions: []*issueExecution{{ID: "run-baseline-old", Status: issueExecutionFailed, Detail: "old failure"}}}
	archived := &productIssue{ID: "issue-baseline-archived", ContactID: ct.ID, Goal: "archived active",
		Status: issueStatusInProgress, Archived: true, Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-baseline-active", SendState: sendAccepted}}
	waiting := &productIssue{ID: "issue-baseline-waiting", ContactID: ct.ID, Goal: "waiting without run",
		Status: issueStatusInProgress, Questions: []*issueQuestion{{Ref: "question-baseline", Text: "please answer", State: issueQuestionOpen}}}
	ct.Issues = []*productIssue{completed, archived, waiting}
	notice := (&appModel{}).issueWorkNotice(ct)
	if notice.Attention || notice.IssueID == "" || notice.IssueID != waiting.ID {
		t.Fatalf("baseline notice = %+v, want waiting candidate", notice)
	}
	if got := issueWorkActiveCount(ct); got != 0 {
		t.Fatalf("terminal/archived active count = %d, want 0", got)
	}
}

func TestIssueWorkNoticeWinnerScopeAndActive(t *testing.T) {
	ct := &contact{ID: "contact-winner", Name: "Winner", Loaded: true}
	attention := &productIssue{ID: "issue-winner-attention", ContactID: ct.ID, Goal: "attention active",
		Status: issueStatusInProgress, Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-winner-attention", SendState: sendAccepted},
		Executions: []*issueExecution{{ID: "run-winner-attention", Status: issueExecutionFailed, Detail: "attention reason"}}}
	waiting := &productIssue{ID: "issue-winner-waiting", ContactID: ct.ID, Goal: "waiting no run",
		Status: issueStatusInProgress, Questions: []*issueQuestion{{Ref: "question-winner", Text: "answer", State: issueQuestionOpen}}}
	active := &productIssue{ID: "issue-winner-active", ContactID: ct.ID, Goal: "active only",
		Status: issueStatusInProgress, Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-winner-active", SendState: sendAccepted}}
	otherContact := &productIssue{ID: "issue-other-contact", ContactID: "other-contact", Goal: "other failure",
		Status: issueStatusInProgress, Executions: []*issueExecution{{ID: "run-other", Status: issueExecutionFailed, Detail: "other reason"}}}
	ct.Issues = []*productIssue{attention, waiting, active, otherContact}
	notice := (&appModel{}).issueWorkNotice(ct)
	if !notice.Attention || notice.Waiting || !notice.Active ||
		notice.IssueID != attention.ID || notice.IssueName != attention.Goal ||
		!strings.Contains(notice.AttentionReason, "attention reason") {
		t.Fatalf("attention winner = %+v", notice)
	}
	if notice.AttentionCount != 1 || notice.WaitingCount != 1 || notice.ActiveCount != 2 {
		t.Fatalf("winner counts = %+v", notice)
	}
	if got := issueWorkActiveCount(ct); got != 2 {
		t.Fatalf("active count scope = %d, want 2", got)
	}

	ct.Issues = []*productIssue{waiting, active}
	notice = (&appModel{}).issueWorkNotice(ct)
	if notice.Attention || !notice.Waiting || notice.Active ||
		notice.IssueID != waiting.ID || notice.IssueName != waiting.Goal {
		t.Fatalf("waiting winner = %+v", notice)
	}
	if notice.WaitingCount != 1 || notice.ActiveCount != 1 {
		t.Fatalf("waiting winner counts = %+v", notice)
	}

	inProgress := &productIssue{ID: "issue-in-progress", ContactID: ct.ID, Goal: "in progress no run", Status: issueStatusInProgress}
	display := issueDisplayStatusFor(inProgress)
	if display.Status != issueStatusInProgress || display.Label != "In progress" || display.Active {
		t.Fatalf("in-progress without run = %+v", display)
	}
	ct.Issues = []*productIssue{inProgress}
	if got := issueWorkActiveCount(ct); got != 0 {
		t.Fatalf("in-progress without active count = %d", got)
	}
	notStartedActive := &productIssue{ID: "issue-not-started-active", ContactID: ct.ID, Goal: "not started active",
		Status: issueStatusNotStarted, Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-not-started-active", SendState: sendAccepted}}
	display = issueDisplayStatusFor(notStartedActive)
	if display.Status != issueStatusInProgress || display.Label != "In progress" || !display.Active {
		t.Fatalf("not-started with active work = %+v", display)
	}
	ct.Issues = []*productIssue{notStartedActive}
	if got := issueWorkActiveCount(ct); got != 1 {
		t.Fatalf("not-started active count = %d", got)
	}
	if notice := (&appModel{}).issueWorkNotice(ct); !notice.Active || notice.Attention || notice.Waiting ||
		notice.IssueID != notStartedActive.ID || notice.IssueName != notStartedActive.Goal {
		t.Fatalf("not-started active notice = %+v", notice)
	}

	unknown := &productIssue{ID: "issue-unknown", ContactID: ct.ID, Goal: "unknown dispatch", Status: issueStatusInProgress,
		Work: issueWorkSession{Loaded: true, SendState: sendUnknown, SendError: "delivery not confirmed"}}
	display = issueDisplayStatusFor(unknown)
	if !display.Attention || display.Status != issueStatusNeedsAttention || display.Reason != "delivery not confirmed" {
		t.Fatalf("unknown dispatch projection = %+v", display)
	}
}

func TestPiIssueAttentionReopensUnseen(t *testing.T) {
	a, ct, _ := piRunStartedTestApp(t)
	ct.ActiveTaskID, ct.SendState = "", sendIdle
	issue := &productIssue{ID: "failed-issue", ContactID: ct.ID, Goal: "Retain failure", RequirementsRevision: 1, Status: issueStatusNeedsAttention}
	attachIssueToolTestExecution(issue)
	issue.Executions[0].Status, issue.Executions[0].Detail = issueExecutionFailed, "Pi failed"
	ct.Issues = []*productIssue{issue}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(a.store.path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reopened := &appModel{}
	applyStoreSelection(reopened, store)
	contact := reopened.selectedContact()
	restored := contact.issueByID(issue.ID)
	if restored == nil || len(restored.Executions) != 1 || restored.Executions[0].AttentionSeenStatus != "" || *restored.Executions[0].PiRun != *issue.Executions[0].PiRun {
		t.Fatalf("reopened Pi result did not preserve its unseen run: %+v", restored)
	}
	if notice := reopened.issueWorkNotice(contact); !notice.Attention || notice.IssueID != issue.ID {
		t.Fatalf("reopened Pi failure did not raise attention: %+v", notice)
	}
}

func TestIssueWorkNoticeSurfacesFailureWithoutChatUnread(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", Loaded: true, Unread: 3}
	issue := &productIssue{ID: "i1", ContactID: "x", Goal: "build login"}
	issue.Work.ActiveTaskID = ""
	issue.Executions = []*issueExecution{{ID: "e1", Status: issueExecutionFailed, Detail: "provider failed"}}
	ct.Issues = []*productIssue{issue}
	a := &appModel{contacts: []*contact{ct}, selected: 0}
	notice := a.issueWorkNotice(ct)
	if !notice.Attention || notice.IssueID != issue.ID || !strings.Contains(notice.AttentionReason, "provider failed") {
		t.Fatalf("issue work notice = %+v", notice)
	}
	if ct.Unread != 3 || ct.ActiveTaskID != "" {
		t.Fatalf("notice changed unread/active conversation: unread=%d active=%q", ct.Unread, ct.ActiveTaskID)
	}
	issue.Executions[0].Status = issueExecutionRunning
	notice = a.issueWorkNotice(ct)
	if notice.IssueID != "" {
		t.Fatalf("running execution without active task produced a false notice: %+v", notice)
	}
}
