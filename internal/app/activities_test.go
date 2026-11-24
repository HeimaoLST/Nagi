package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func attachIssueToolTestExecution(issue *productActivity) {
	root := "/tmp"
	run := &piActivityRun{WorkerEpoch: "fixture", ContextID: "activity-" + issue.ID, SessionRef: "work-session", RunID: "work-task", Admitted: true}
	issue.PiBinding = &piActivityBinding{Engine: "pi", Context: pi.ContextBinding{ContextID: run.ContextID, BotID: issue.ContactID, Purpose: pi.Activity, ActivityID: issue.ID, SessionRef: run.SessionRef, SelectionRevision: 1, CWD: root}, Selection: piSelection{CWD: root}, Configuration: piConfiguration{CredentialSource: "fixture"}}
	issue.WorkSessionID = run.SessionRef
	issue.Executions = append(issue.Executions, &activityExecution{
		ID: "execution-test", SessionID: run.SessionRef, TaskID: run.RunID, PiIntent: true, PiRun: run,
		Status: activityExecutionCompleted, RequirementsRevision: issue.RequirementsRevision,
	})
}

func TestIssueStoreRoundTripKeepsPiConversationAndDraft(t *testing.T) {
	a, ct, _ := piRunStartedTestApp(t)
	ct.ActiveTaskID, ct.SendState, ct.Draft = "", sendIdle, "preserved draft"
	path, sessionID := a.store.Path, ct.SessionID
	issue, err := a.createActivityFor(ct, "Implement login", "Use SMS verification.")
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Version != contactStoreVersion {
		t.Fatalf("saved version = %d", reloaded.State.Version)
	}
	if len(reloaded.State.Issues) != 1 {
		t.Fatalf("stored issues = %+v", reloaded.State.Issues)
	}
	stored := reloaded.State.Issues[0]
	if stored.ID == "" || stored.ContactID != ct.ID || stored.Goal != "Implement login" ||
		len(stored.RequirementItems) != 1 ||
		stored.RequirementItems[0].Text != "Use SMS verification." ||
		stored.Status != activityStatusNotStarted {
		t.Fatalf("stored issue = %+v", stored)
	}
	if reloaded.State.Contacts[0].Draft != "preserved draft" || reloaded.State.Contacts[0].SessionID != sessionID || reloaded.State.Contacts[0].PiBinding == nil {
		t.Fatalf("saved Pi conversation or draft changed: %+v", reloaded.State.Contacts[0])
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
	if _, err := a.createActivityFor(ct, "", "requirement"); err == nil || len(ct.Activities) != 0 {
		t.Fatalf("empty goal left state: activities=%v error=%v", ct.Activities, err)
	}
	issue, err := a.createActivityFor(ct, "Implement login", "Must support SMS.")
	if err != nil {
		t.Fatal(err)
	}
	if issue.ContactID != "c1" || issue.Goal != "Implement login" || activityRequirementsText(issue) != "Must support SMS." {
		t.Fatalf("issue fields = %+v", issue)
	}
	if len(issue.Executions) != 0 {
		t.Fatalf("creating an item must not create an execution: %+v", issue.Executions)
	}
	reopened, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.State.Issues) != 1 || reopened.State.Issues[0].ID != issue.ID || len(reopened.State.Issues[0].Executions) != 0 {
		t.Fatalf("saved issues = %+v", reopened.State.Issues)
	}
}

func TestCreateIssueFailureKeepsExistingIssues(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Nagi", Draft: "unsent chat"}
	a := &appModel{store: &contactStore{Path: t.TempDir()}, contacts: []*contact{ct}}
	if _, err := a.createActivityFor(ct, "Implement login", "SMS required"); err == nil {
		t.Fatal("createActivityFor unexpectedly succeeded with a failing store")
	}
	if len(ct.Activities) != 0 || ct.Draft != "unsent chat" {
		t.Fatalf("failed save changed activities or chat draft: %+v", ct)
	}
}

func TestStoreRejectsIssueWithoutValidContact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	state := storedState{
		Version: contactStoreVersion, Selected: "c1",
		Contacts: []storedContact{{ID: "c1", Name: "Cedar"}},
		Issues:   []storedActivity{{ID: "i1", Goal: "orphan"}},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openContactStore(path, "/tmp"); err == nil || !strings.Contains(err.Error(), "unknown Bot ID") {
		t.Fatalf("orphan issue error = %v", err)
	}
}

func TestStoredPendingExecutionIsUnknownAfterRestart(t *testing.T) {
	issues := activitiesForStoredContact([]storedActivity{{
		ID: "i1", ContactID: "c1", Goal: "work", Status: activityStatusNotStarted,
		Executions: []storedActivityExecution{{ID: "e1", TaskID: "task-possible", Prompt: "work", Status: activityExecutionPending}},
	}}, "c1")
	if len(issues) != 1 || len(issues[0].Executions) != 1 {
		t.Fatalf("issues = %+v", issues)
	}
	if got := issues[0].Executions[0].Status; got != activityExecutionUnknown {
		t.Fatalf("restart pending status = %q, want unknown", got)
	}
	if reason := activityRunBlocked(issues[0]); reason == "" {
		t.Fatal("unknown execution must block blind Run")
	}
}

func TestRestoredNonterminalExecutionWithoutTaskIsUnknown(t *testing.T) {
	for _, status := range []string{activityExecutionPending, activityExecutionAccepted, activityExecutionRunning} {
		issues := activitiesForStoredContact([]storedActivity{{
			ID: "i1", ContactID: "c1", Goal: "work", Status: activityStatusNotStarted,
			Executions: []storedActivityExecution{{ID: "e1", Prompt: "work", Status: status}},
		}}, "c1")
		execution := issues[0].Executions[0]
		if execution.Status != activityExecutionUnknown || activityRunBlocked(issues[0]) == "" {
			t.Fatalf("%s without task restored as %q", status, execution.Status)
		}
	}
	issues := activitiesForStoredContact([]storedActivity{{
		ID: "i1", ContactID: "c1", Goal: "work", Status: activityStatusNotStarted,
		Executions: []storedActivityExecution{{ID: "e1", TaskID: "t1", Prompt: "work", Status: activityExecutionRunning}},
	}}, "c1")
	if e := issues[0].Executions[0]; e.TaskID != "t1" || e.Status != activityExecutionUnknown || activityRunBlocked(issues[0]) == "" {
		t.Fatalf("unbound execution did not preserve its task ID and block replay: %+v", e)
	}
}

func TestContactActivityObservesIssueWorkTarget(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", Loaded: true}
	a := &appModel{contacts: []*contact{ct}, runtimeStatus: runtimeStateReady}
	if active, unknown := a.contactActivity(ct); active || unknown {
		t.Fatal("contact with no issue work was active")
	}
	issue := &productActivity{ID: "i1", ContactID: ct.ID, Goal: "work", WorkSessionID: "work-session"}
	issue.Work.SendState = sendAccepted
	issue.Work.ActiveTaskID = "task-1"
	ct.Activities = []*productActivity{issue}
	if active, unknown := a.contactActivity(ct); !active || unknown {
		t.Fatalf("active issue work was not observed: active=%v unknown=%v", active, unknown)
	}
}

func TestIssueWorkNoticeKeepsUnseenOlderExceptionAfterSuccess(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", Loaded: true}
	issue := &productActivity{ID: "i-old-exception", ContactID: ct.ID, Goal: "older failed work"}
	issue.Executions = []*activityExecution{
		{ID: "e-old", TaskID: "t-old", Status: activityExecutionFailed, Detail: "old unseen failure"},
		{ID: "e-new", TaskID: "t-new", Status: activityExecutionCompleted},
	}
	ct.Activities = []*productActivity{issue}
	a := &appModel{contacts: []*contact{ct}, selected: 0}
	notice := a.activityWorkNotice(ct)
	if !notice.Attention || notice.ActivityID != issue.ID || !strings.Contains(notice.AttentionReason, "old unseen failure") {
		t.Fatalf("later success hid an unseen older exception: %+v", notice)
	}
	issue.Executions[0].AttentionSeenStatus = activityExecutionFailed
	if notice := a.activityWorkNotice(ct); notice.ActivityID != "" {
		t.Fatalf("seen older exception was re-lit after success: %+v", notice)
	}
}

func TestIssueSeenUnknownWithActiveTaskIDDoesNotReAttention(t *testing.T) {
	ct := &contact{ID: "contact-seen-active-unknown", Name: "Seen Unknown", Loaded: true}
	execution := &activityExecution{
		ID: "run-seen-active-unknown", TaskID: "task-seen-active-unknown",
		Status: activityExecutionUnknown, Detail: "unknown already seen",
		AttentionSeenStatus: activityExecutionUnknown,
	}
	issue := &productActivity{
		ID: "issue-seen-active-unknown", ContactID: ct.ID, Goal: "seen unknown with active mapping",
		Status: activityStatusInProgress,
		Work: activityWorkSession{Loaded: true, ActiveTaskID: "task-seen-active-unknown", LastTaskID: "task-seen-active-unknown",
			SendState: sendUnknown, SendError: "delivery not confirmed"},
		Executions: []*activityExecution{execution},
	}
	ct.Activities = []*productActivity{issue}
	display := activityDisplayStatusFor(issue)
	if display.Attention {
		t.Fatalf("seen unknown with retained ActiveTaskID re-raised attention: %+v", display)
	}
	notice := (&appModel{}).activityWorkNotice(ct)
	if notice.Attention || notice.ActivityID != "" {
		t.Fatalf("seen unknown with retained ActiveTaskID re-raised notice: %+v", notice)
	}
	if issue.Work.ActiveTaskID != "task-seen-active-unknown" || issue.Work.SendState != sendUnknown ||
		execution.AttentionSeenStatus != activityExecutionUnknown || execution.Status != activityExecutionUnknown {
		t.Fatalf("seen unknown state was rewritten: work=%+v run=%+v", issue.Work, execution)
	}
}

func TestIssueDisplayProjectionUsesExistingFacts(t *testing.T) {
	ct := &contact{ID: "contact-projection", Name: "Projection", Loaded: true}
	completed := &productActivity{ID: "issue-completed", ContactID: ct.ID, Goal: "completed goal", Status: activityStatusCompleted,
		Executions: []*activityExecution{{ID: "run-completed-old", Status: activityExecutionFailed, Detail: "old failure"}}}
	canceled := &productActivity{ID: "issue-canceled", ContactID: ct.ID, Goal: "canceled goal", Status: activityStatusCanceled,
		Executions: []*activityExecution{{ID: "run-canceled-old", Status: activityExecutionUnknown, Detail: "old unknown"}}}
	archived := &productActivity{ID: "issue-archived", ContactID: ct.ID, Goal: "archived goal", Status: activityStatusInProgress, Archived: true,
		Work: activityWorkSession{Loaded: true, ActiveTaskID: "run-archived-active", SendState: sendAccepted}}
	waiting := &productActivity{ID: "issue-waiting", ContactID: ct.ID, Goal: "waiting goal", Status: activityStatusInProgress,
		Questions: []*activityQuestion{{Ref: "q-waiting", Text: "answer this", State: activityQuestionOpen}}}
	attention := &productActivity{ID: "issue-attention", ContactID: ct.ID, Goal: "attention goal", Status: activityStatusInProgress,
		Executions: []*activityExecution{{ID: "run-attention", Status: activityExecutionFailed, Detail: "unseen failure"}}}
	active := &productActivity{ID: "issue-active", ContactID: ct.ID, Goal: "active goal", Status: activityStatusInProgress,
		Work: activityWorkSession{Loaded: true, ActiveTaskID: "run-active", SendState: sendAccepted}}
	ct.Activities = []*productActivity{completed, canceled, archived, waiting, attention, active}
	notice := (&appModel{}).activityWorkNotice(ct)
	if got := activityDisplayStatusFor(completed).Label; got != "Completed" {
		t.Fatalf("completed label = %q", got)
	}
	if got := activityDisplayStatusFor(canceled).Label; got != "Canceled" {
		t.Fatalf("canceled label = %q", got)
	}
	if got := activityDisplayStatusFor(archived); got.Label != "Archived" || got.Attention || got.Waiting || got.Active {
		t.Fatalf("archived projection = %+v", got)
	}
	if got := activityDisplayStatusFor(waiting); got.Label != activityStatusLabel(&productActivity{Status: activityStatusWaitingUser}) || !got.Waiting {
		t.Fatalf("waiting projection = %+v", got)
	}
	if got := activityDisplayStatusFor(attention); got.Label != "Needs attention" || !got.Attention {
		t.Fatalf("attention projection = %+v", got)
	}
	if got := activityDisplayStatusFor(active); got.Label != "In progress" || got.Status != activityStatusInProgress || !got.Active {
		t.Fatalf("active projection = %+v", got)
	}
	if notice.AttentionCount != 1 || notice.WaitingCount != 1 || notice.ActiveCount != 1 ||
		!notice.Attention || notice.ActivityID != attention.ID || notice.ActivityName != attention.Goal {
		t.Fatalf("notice aggregation = %+v", notice)
	}
}

func TestIssueExplicitNeedsAttentionSurvivesSeenRun(t *testing.T) {
	ct := &contact{ID: "contact-explicit-attention", Name: "Explicit", Loaded: true}
	execution := &activityExecution{ID: "run-explicit", Status: activityExecutionFailed, Detail: "seen failure", AttentionSeenStatus: activityExecutionFailed}
	issue := &productActivity{ID: "issue-explicit", ContactID: ct.ID, Goal: "explicit attention", Status: activityStatusNeedsAttention,
		Work: activityWorkSession{Loaded: true}, Executions: []*activityExecution{execution}}
	ct.Activities = []*productActivity{issue}
	display := activityDisplayStatusFor(issue)
	if !display.Attention || display.Label != "Needs attention" {
		t.Fatalf("explicit needs_attention was cleared by a seen run: %+v", display)
	}
	notice := (&appModel{}).activityWorkNotice(ct)
	if !notice.Attention || notice.AttentionCount != 1 || notice.ActivityID != issue.ID {
		t.Fatalf("explicit needs_attention was cleared from aggregation: %+v", notice)
	}
	if issue.Status != activityStatusNeedsAttention || execution.AttentionSeenStatus != activityExecutionFailed {
		t.Fatalf("seen field or persistent status was rewritten: issue=%+v run=%+v", issue, execution)
	}
}

func TestIssueWorkNoticeBaselineTerminalAndWaiting(t *testing.T) {
	ct := &contact{ID: "contact-baseline", Name: "Baseline", Loaded: true}
	completed := &productActivity{ID: "issue-baseline-completed", ContactID: ct.ID, Goal: "completed old",
		Status: activityStatusCompleted, Executions: []*activityExecution{{ID: "run-baseline-old", Status: activityExecutionFailed, Detail: "old failure"}}}
	archived := &productActivity{ID: "issue-baseline-archived", ContactID: ct.ID, Goal: "archived active",
		Status: activityStatusInProgress, Archived: true, Work: activityWorkSession{Loaded: true, ActiveTaskID: "run-baseline-active", SendState: sendAccepted}}
	waiting := &productActivity{ID: "issue-baseline-waiting", ContactID: ct.ID, Goal: "waiting without run",
		Status: activityStatusInProgress, Questions: []*activityQuestion{{Ref: "question-baseline", Text: "please answer", State: activityQuestionOpen}}}
	ct.Activities = []*productActivity{completed, archived, waiting}
	notice := (&appModel{}).activityWorkNotice(ct)
	if notice.Attention || notice.ActivityID == "" || notice.ActivityID != waiting.ID {
		t.Fatalf("baseline notice = %+v, want waiting candidate", notice)
	}
	if got := activityWorkActiveCount(ct); got != 0 {
		t.Fatalf("terminal/archived active count = %d, want 0", got)
	}
}

func TestIssueWorkNoticeWinnerScopeAndActive(t *testing.T) {
	ct := &contact{ID: "contact-winner", Name: "Winner", Loaded: true}
	attention := &productActivity{ID: "issue-winner-attention", ContactID: ct.ID, Goal: "attention active",
		Status: activityStatusInProgress, Work: activityWorkSession{Loaded: true, ActiveTaskID: "run-winner-attention", SendState: sendAccepted},
		Executions: []*activityExecution{{ID: "run-winner-attention", Status: activityExecutionFailed, Detail: "attention reason"}}}
	waiting := &productActivity{ID: "issue-winner-waiting", ContactID: ct.ID, Goal: "waiting no run",
		Status: activityStatusInProgress, Questions: []*activityQuestion{{Ref: "question-winner", Text: "answer", State: activityQuestionOpen}}}
	active := &productActivity{ID: "issue-winner-active", ContactID: ct.ID, Goal: "active only",
		Status: activityStatusInProgress, Work: activityWorkSession{Loaded: true, ActiveTaskID: "run-winner-active", SendState: sendAccepted}}
	otherContact := &productActivity{ID: "issue-other-contact", ContactID: "other-contact", Goal: "other failure",
		Status: activityStatusInProgress, Executions: []*activityExecution{{ID: "run-other", Status: activityExecutionFailed, Detail: "other reason"}}}
	ct.Activities = []*productActivity{attention, waiting, active, otherContact}
	notice := (&appModel{}).activityWorkNotice(ct)
	if !notice.Attention || notice.Waiting || !notice.Active ||
		notice.ActivityID != attention.ID || notice.ActivityName != attention.Goal ||
		!strings.Contains(notice.AttentionReason, "attention reason") {
		t.Fatalf("attention winner = %+v", notice)
	}
	if notice.AttentionCount != 1 || notice.WaitingCount != 1 || notice.ActiveCount != 2 {
		t.Fatalf("winner counts = %+v", notice)
	}
	if got := activityWorkActiveCount(ct); got != 2 {
		t.Fatalf("active count scope = %d, want 2", got)
	}

	ct.Activities = []*productActivity{waiting, active}
	notice = (&appModel{}).activityWorkNotice(ct)
	if notice.Attention || !notice.Waiting || notice.Active ||
		notice.ActivityID != waiting.ID || notice.ActivityName != waiting.Goal {
		t.Fatalf("waiting winner = %+v", notice)
	}
	if notice.WaitingCount != 1 || notice.ActiveCount != 1 {
		t.Fatalf("waiting winner counts = %+v", notice)
	}

	inProgress := &productActivity{ID: "issue-in-progress", ContactID: ct.ID, Goal: "in progress no run", Status: activityStatusInProgress}
	display := activityDisplayStatusFor(inProgress)
	if display.Status != activityStatusInProgress || display.Label != "In progress" || display.Active {
		t.Fatalf("in-progress without run = %+v", display)
	}
	ct.Activities = []*productActivity{inProgress}
	if got := activityWorkActiveCount(ct); got != 0 {
		t.Fatalf("in-progress without active count = %d", got)
	}
	notStartedActive := &productActivity{ID: "issue-not-started-active", ContactID: ct.ID, Goal: "not started active",
		Status: activityStatusNotStarted, Work: activityWorkSession{Loaded: true, ActiveTaskID: "run-not-started-active", SendState: sendAccepted}}
	display = activityDisplayStatusFor(notStartedActive)
	if display.Status != activityStatusInProgress || display.Label != "In progress" || !display.Active {
		t.Fatalf("not-started with active work = %+v", display)
	}
	ct.Activities = []*productActivity{notStartedActive}
	if got := activityWorkActiveCount(ct); got != 1 {
		t.Fatalf("not-started active count = %d", got)
	}
	if notice := (&appModel{}).activityWorkNotice(ct); !notice.Active || notice.Attention || notice.Waiting ||
		notice.ActivityID != notStartedActive.ID || notice.ActivityName != notStartedActive.Goal {
		t.Fatalf("not-started active notice = %+v", notice)
	}

	unknown := &productActivity{ID: "issue-unknown", ContactID: ct.ID, Goal: "unknown dispatch", Status: activityStatusInProgress,
		Work: activityWorkSession{Loaded: true, SendState: sendUnknown, SendError: "delivery not confirmed"}}
	display = activityDisplayStatusFor(unknown)
	if !display.Attention || display.Status != activityStatusNeedsAttention || display.Reason != "delivery not confirmed" {
		t.Fatalf("unknown dispatch projection = %+v", display)
	}
}

func TestPiIssueAttentionReopensUnseen(t *testing.T) {
	a, ct, _ := piRunStartedTestApp(t)
	ct.ActiveTaskID, ct.SendState = "", sendIdle
	issue := &productActivity{ID: "failed-issue", ContactID: ct.ID, Goal: "Retain failure", RequirementsRevision: 1, Status: activityStatusNeedsAttention}
	attachIssueToolTestExecution(issue)
	issue.Executions[0].Status, issue.Executions[0].Detail = activityExecutionFailed, "Pi failed"
	ct.Activities = []*productActivity{issue}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(a.store.Path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reopened := &appModel{}
	applyStoreSelection(reopened, store)
	contact := reopened.selectedContact()
	restored := contact.activityByID(issue.ID)
	if restored == nil || len(restored.Executions) != 1 || restored.Executions[0].AttentionSeenStatus != "" || *restored.Executions[0].PiRun != *issue.Executions[0].PiRun {
		t.Fatalf("reopened Pi result did not preserve its unseen run: %+v", restored)
	}
	if notice := reopened.activityWorkNotice(contact); !notice.Attention || notice.ActivityID != issue.ID {
		t.Fatalf("reopened Pi failure did not raise attention: %+v", notice)
	}
}

func TestIssueWorkNoticeSurfacesFailureWithoutChatUnread(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", Loaded: true, Unread: 3}
	issue := &productActivity{ID: "i1", ContactID: "x", Goal: "build login"}
	issue.Work.ActiveTaskID = ""
	issue.Executions = []*activityExecution{{ID: "e1", Status: activityExecutionFailed, Detail: "provider failed"}}
	ct.Activities = []*productActivity{issue}
	a := &appModel{contacts: []*contact{ct}, selected: 0}
	notice := a.activityWorkNotice(ct)
	if !notice.Attention || notice.ActivityID != issue.ID || !strings.Contains(notice.AttentionReason, "provider failed") {
		t.Fatalf("issue work notice = %+v", notice)
	}
	if ct.Unread != 3 || ct.ActiveTaskID != "" {
		t.Fatalf("notice changed unread/active conversation: unread=%d active=%q", ct.Unread, ct.ActiveTaskID)
	}
	issue.Executions[0].Status = activityExecutionRunning
	notice = a.activityWorkNotice(ct)
	if notice.ActivityID != "" {
		t.Fatalf("running execution without active task produced a false notice: %+v", notice)
	}
}
