package main

import (
	"path/filepath"
	"testing"

	"github.com/HeimaoLST/Nagi/locales"
)

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
