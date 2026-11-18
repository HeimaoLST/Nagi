package main

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func expandIssueDetails(t *testing.T, tr *ui.Tester) {
	t.Helper()
	if err := tr.Click("More task actions"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
}

func TestIssueLifecycleIllegalBoundariesDoNotRewriteState(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	issue.Status = issueStatusCompleted
	issue.UpdatedAt = 11
	if a.markIssueWaiting(issue.ID) || a.completeIssue(issue.ID, "again") || a.resumeIssue(issue.ID) {
		t.Fatal("completed issue accepted an invalid lifecycle action")
	}
	if issue.Status != issueStatusCompleted || issue.UpdatedAt != 11 {
		t.Fatalf("invalid action changed state: %+v", issue)
	}
	issue.Status = issueStatusInProgress
	issue.Work.ActiveTaskID = "t1"
	issue.Work.SendState = sendAccepted
	if a.completeIssue(issue.ID, "while active") || a.markIssueWaiting(issue.ID) || a.reopenIssue(issue.ID) {
		t.Fatal("active issue accepted a terminal/waiting action")
	}
	if issue.Status != issueStatusInProgress || issue.Work.ActiveTaskID != "t1" {
		t.Fatalf("active issue state changed: %+v", issue)
	}
}

func TestIssueRequirementSaveFailureRollsBackFullState(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	a.store = &contactStore{path: t.TempDir()} // directory path makes save fail
	requirement := issue.Requirements[0]
	oldState, oldEvidence, oldRequirementUpdated := requirement.State, requirement.Evidence, requirement.UpdatedAt
	oldIssueUpdated := issue.UpdatedAt
	if a.setIssueRequirementState(issue.ID, requirement.ID, requirementSatisfied, "new evidence") {
		t.Fatal("requirement save unexpectedly succeeded")
	}
	if requirement.State != oldState || requirement.Evidence != oldEvidence ||
		requirement.UpdatedAt != oldRequirementUpdated || issue.UpdatedAt != oldIssueUpdated {
		t.Fatalf("failed requirement save left partial state: requirement=%+v issue.updated=%d", requirement, issue.UpdatedAt)
	}
}

func TestIssueEditClearsCompletionEvidenceButKeepsExecutionSnapshot(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue.Status = issueStatusInProgress
	issue.Evidence = "old completion proof"
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work", TaskID: "t1", Prompt: "old exact prompt", Status: issueExecutionCompleted, RequirementsRevision: 1}}
	if !a.editIssue(issue.ID, "new goal", "support SMS\nshow errors") {
		t.Fatal("goal edit failed")
	}
	if issue.Evidence != "" {
		t.Fatalf("old completion evidence survived edit: %q", issue.Evidence)
	}
	if issue.Executions[0].Prompt != "old exact prompt" || issue.Executions[0].RequirementsRevision != 1 {
		t.Fatalf("old execution snapshot changed: %+v", issue.Executions[0])
	}
}

func TestIssueTerminalLifecycleDoesNotAutoComplete(t *testing.T) {
	issue := &productIssue{ID: "i1", Status: issueStatusInProgress, Work: issueWorkSession{StopRequestedTasks: map[string]bool{}}}
	a := &appModel{}
	a.applyIssueTerminalLifecycle(issue, "t1", "completed")
	if issue.Status != issueStatusInProgress {
		t.Fatalf("completed execution changed issue lifecycle: %q", issue.Status)
	}
	a.applyIssueTerminalLifecycle(issue, "t2", "failed")
	if issue.Status != issueStatusNeedsAttention {
		t.Fatalf("failed execution status = %q", issue.Status)
	}
	issue.Status = issueStatusInProgress
	issue.Work.StopRequestedTasks["t3"] = true
	a.applyIssueTerminalLifecycle(issue, "t3", "interrupted")
	if issue.Status != issueStatusInProgress {
		t.Fatalf("user-stopped execution status = %q", issue.Status)
	}
	issue.Status = issueStatusInProgress
	a.applyIssueTerminalLifecycle(issue, "t4", "interrupted")
	if issue.Status != issueStatusNeedsAttention {
		t.Fatalf("external interrupted status = %q", issue.Status)
	}
	issue.Status = issueStatusCanceled
	a.applyIssueTerminalLifecycle(issue, "t5", "failed")
	if issue.Status != issueStatusCanceled {
		t.Fatalf("terminal event overwrote canceled issue: %q", issue.Status)
	}
}

func TestIssueUITargetsLifecycleActions(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "lifecycle", Status: issueStatusInProgress, RequirementsRevision: 1,
		Requirements: parseIssueRequirements("one requirement"),
		Executions:   []*issueExecution{{ID: "e1", SessionID: "chat", TaskID: "t1", Status: issueExecutionCompleted}}}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	a.pi = presentationPiHost(a)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	for _, want := range []string{"In progress", "Requirements", "Unverified", "Continue task"} {
		if !tr.HasText(want) {
			t.Errorf("lifecycle default UI missing %q: %v", want, tr.Texts())
		}
	}
	expandIssueDetails(t, tr)
	for _, want := range []string{"Met", "Not met", "Unverified", "Mark waiting", "Cancel task", "Archive", "Mark done"} {
		if !tr.HasText(want) {
			t.Errorf("lifecycle details UI missing %q: %v", want, tr.Texts())
		}
	}
	if err := tr.Click("Mark waiting"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Status != issueStatusWaitingUser {
		t.Fatalf("Mark waiting status = %q", issue.Status)
	}
	if !tr.HasText("Waiting for you") || !tr.HasText("Resume") {
		t.Fatal("waiting state controls not visible")
	}
}

func TestIssueUIFilterArchiveAndRestore(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "archive me", Status: issueStatusInProgress, RequirementsRevision: 1}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	if err := tr.Click("Archive"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !issue.Archived || ct.issueNavigation().Filter != issueFilterUnfinished || !tr.HasText("Restore task") || !tr.HasText("Archived") {
		t.Fatalf("archive UI = archived=%v filter=%q texts=%v", issue.Archived, ct.issueNavigation().Filter, tr.Texts())
	}
	if err := tr.Click("Restore task"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Archived || ct.issueNavigation().Filter != issueFilterUnfinished {
		t.Fatalf("restore UI changed the current filter: archived=%v filter=%q", issue.Archived, ct.issueNavigation().Filter)
	}
}

func TestIssueUIEditPrefillsRequirementsAndSavesRevision(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "old goal", Status: issueStatusInProgress, RequirementsRevision: 1,
		Requirements: parseIssueRequirements("old requirement")}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	if err := tr.Click("Edit task"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Edit task") || a.issueFormEditingID != issue.ID || a.issueGoal != "old goal" ||
		a.issueRequirements != "old requirement" {
		t.Fatalf("edit form prefill = %q / %q", a.issueGoal, a.issueRequirements)
	}
	a.issueGoal = "new goal"
	a.issueRequirements = "old requirement\nnew requirement"
	if err := tr.Click("Save task"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Goal != "new goal" || issue.RequirementsRevision != 2 || len(issue.Requirements) != 2 ||
		issue.Evidence != "" || issue.Requirements[1].State != requirementUnverified {
		t.Fatalf("edited UI issue = %+v", issue)
	}
}

func TestIssueEditSaveFailureRestoresCompleteVisibleState(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	a.store = &contactStore{path: t.TempDir()}
	issue.Evidence = "old completion proof"
	oldGoal, oldRequirements, oldRevision := issue.Goal, issue.Requirements, issue.RequirementsRevision
	oldEvidence, oldUpdated := issue.Evidence, issue.UpdatedAt
	if a.editIssue(issue.ID, "new goal", "new requirement") {
		t.Fatal("edit unexpectedly succeeded with a failing store")
	}
	if issue.Goal != oldGoal || issue.RequirementsRevision != oldRevision || issue.Evidence != oldEvidence ||
		issue.UpdatedAt != oldUpdated || len(issue.Requirements) != len(oldRequirements) {
		t.Fatalf("failed edit left partial state: %+v evidence=%q updated=%d", issue, issue.Evidence, issue.UpdatedAt)
	}
}

func TestArchivedActiveIssueStillBlocksApply(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "hidden active", Status: issueStatusInProgress, Archived: true, WorkSessionID: "work"}
	issue.Work.ActiveTaskID = "t1"
	issue.Work.SendState = sendAccepted
	ct.Issues = []*productIssue{issue}
	a.connectionSaved = &connectionConfig{}
	a.pi = presentationPiHost(a)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	reason := a.connectionReloadBlocked()
	if reason != connBlockedActiveWork {
		t.Fatalf("archived active issue was missed by Apply protection: %q", reason)
	}
	if got := connectionBlockedText(locales.In("en"), reason); got != "Wait for all chat and task work to finish before applying a new connection." {
		t.Fatalf("active-work recovery copy = %q", got)
	}
	if got := connectionBlockedText(locales.In("zh-Hans"), reason); got != "请等待所有对话和事项工作结束后再应用新连接。" {
		t.Fatalf("active-work zh recovery copy = %q", got)
	}
}

func TestIssuePersistentActionsRollbackOnSaveFailure(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	a.store = &contactStore{path: t.TempDir()}
	issue.Status = issueStatusInProgress
	oldUpdated := issue.UpdatedAt
	if a.completeIssue(issue.ID, "evidence") || a.archiveIssue(issue.ID) {
		t.Fatal("save-failing actions unexpectedly succeeded")
	}
	if issue.Status != issueStatusInProgress || issue.Archived || issue.UpdatedAt != oldUpdated {
		t.Fatalf("failed actions left partial state: %+v", issue)
	}
}

func TestIssueUICompleteAndReopenActions(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "finish me", Status: issueStatusInProgress, RequirementsRevision: 1}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	completion := a.completionDraft(issue)
	*completion = "user verified the saved result"
	if err := tr.Click("Mark done"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Status != issueStatusCompleted || issue.Evidence != "user verified the saved result" {
		t.Fatalf("UI completion = %q evidence=%q", issue.Status, issue.Evidence)
	}
	if !tr.HasText("Completion evidence") || !tr.HasText("user verified the saved result") {
		t.Fatalf("completion evidence is not readable after completion: %v", tr.Texts())
	}
	if err := tr.Click("Reopen task"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Status != issueStatusNotStarted {
		t.Fatalf("UI reopen = %q", issue.Status)
	}
	if !tr.HasText("Previous completion evidence: user verified the saved result") {
		t.Fatalf("previous completion evidence is not readable after reopen: %v", tr.Texts())
	}
}

func TestIssueUIRequirementEvidenceActionPersists(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "goal", Status: issueStatusInProgress, RequirementsRevision: 1,
		Requirements: parseIssueRequirements("one checked requirement")}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	requirement := issue.Requirements[0]
	draft := a.requirementDraft(requirement)
	*draft = "checked by the user"
	clickAfterBoundedScroll(t, tr, "Met", 1040, 720)
	tr.Frame()
	if requirement.State != requirementSatisfied || requirement.Evidence != "checked by the user" {
		t.Fatalf("requirement UI evidence = %+v", requirement)
	}
}

func TestIssueUIStatusFilterSelection(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "filtered issue", Status: issueStatusCompleted, RequirementsRevision: 1}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Back to tasks"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Not done"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Done"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.issueNavigation().Filter != issueFilterCompleted {
		t.Fatalf("status filter = %q", ct.issueNavigation().Filter)
	}
	if !tr.HasText("filtered issue") || !tr.HasText("Done") {
		t.Fatalf("completed filter did not list issue: %v", tr.Texts())
	}
}
