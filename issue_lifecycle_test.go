package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
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
	a := &appModel{contacts: []*contact{ct}, selected: 0, scrollTo: -1, runtimeStatus: runtimeStateReady}
	return a, ct, issue
}

func TestIssueStatusAndRequirementLifecycleFacts(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	if issue.Status != issueStatusNotStarted || len(issue.Requirements) != 2 {
		t.Fatalf("initial issue = %+v", issue)
	}
	path := filepath.Join(t.TempDir(), "contacts.json")
	a.store = &contactStore{path: path}
	oldPrompt := "sent snapshot before edit"
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work", TaskID: "t1", Prompt: oldPrompt, Status: issueExecutionCompleted, RequirementsRevision: 1}}
	oldFirst := issue.Requirements[0]
	if !a.editIssue(issue.ID, "implement SMS login", "support SMS\nshow errors\nhandle expiry") {
		t.Fatal("edit failed")
	}
	if issue.Goal != "implement SMS login" || issue.RequirementsRevision != 2 || len(issue.Requirements) != 3 {
		t.Fatalf("edited issue = %+v", issue)
	}
	if issue.Requirements[0] != oldFirst || issue.Requirements[0].State != requirementUnverified {
		t.Fatal("unchanged requirement lost its identity/state")
	}
	if issue.Executions[0].Prompt != oldPrompt || issue.Executions[0].RequirementsRevision != 1 {
		t.Fatalf("old execution snapshot was rewritten: %+v", issue.Executions[0])
	}
	newRequirement := issue.Requirements[2]
	if newRequirement.State != requirementUnverified || newRequirement.Text != "handle expiry" {
		t.Fatalf("new requirement = %+v", newRequirement)
	}
	if !a.setIssueRequirementState(issue.ID, newRequirement.ID, requirementSatisfied, "verified by test") {
		t.Fatal("requirement state did not save")
	}
	if newRequirement.State != requirementSatisfied || newRequirement.Evidence != "verified by test" {
		t.Fatalf("requirement evidence = %+v", newRequirement)
	}
}

func TestIssueCompletedExecutionDoesNotCompleteIssue(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	issue.Status = issueStatusInProgress
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work", TaskID: "t1", Status: issueExecutionCompleted}}
	recoveryTestStore(t, a)
	if issue.Status != issueStatusInProgress {
		t.Fatal("fixture status")
	}
	if !a.completeIssue(issue.ID, "user checked the delivered result") || issue.Status != issueStatusCompleted {
		t.Fatalf("explicit completion = %+v", issue)
	}
	if !strings.Contains(issueEvidenceText(locales.In("en"), issue), "user checked") {
		t.Fatalf("completion evidence = %q", issueEvidenceText(locales.In("en"), issue))
	}
	reloaded, err := openContactStore(a.storePath, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.state.Issues) != 1 || reloaded.state.Issues[0].ID != issue.ID ||
		reloaded.state.Issues[0].Status != issueStatusCompleted || reloaded.state.Issues[0].Evidence != issue.Evidence {
		t.Fatalf("saved completion = %+v", reloaded.state.Issues)
	}
}

func TestIssueStopAndCancelAreDifferentActions(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	issue.Status = issueStatusInProgress
	issue.Work.ActiveTaskID = "t1"
	issue.Work.SendState = sendAccepted
	recoveryTestStore(t, a)
	if a.cancelIssueProduct(issue.ID) {
		t.Fatal("cancel product succeeded without a Runtime for active work")
	}
	issue.Work.ActiveTaskID = ""
	issue.Work.SendState = sendIdle
	if !a.cancelIssueProduct(issue.ID) || issue.Status != issueStatusCanceled {
		t.Fatalf("cancel issue = %+v", issue)
	}
	if issue.Work.ActiveTaskID != "" {
		t.Fatal("cancel invented an active work state")
	}
	reloaded, err := openContactStore(a.storePath, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.state.Issues) != 1 || reloaded.state.Issues[0].ID != issue.ID || reloaded.state.Issues[0].Status != issueStatusCanceled {
		t.Fatalf("saved cancellation = %+v", reloaded.state.Issues)
	}
	if !a.reopenIssue(issue.ID) || issue.Status != issueStatusNotStarted {
		t.Fatalf("reopen canceled issue = %+v", issue)
	}
	reloaded, err = openContactStore(a.storePath, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.state.Issues) != 1 || reloaded.state.Issues[0].ID != issue.ID || reloaded.state.Issues[0].Status != issueStatusNotStarted {
		t.Fatalf("saved reopened issue = %+v", reloaded.state.Issues)
	}
}

func TestIssueArchiveKeepsActiveWorkAndRestore(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	issue.Status = issueStatusInProgress
	issue.WorkSessionID = "work-i1"
	issue.Work.ActiveTaskID = "t1"
	issue.Work.SendState = sendAccepted
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-i1", TaskID: "t1", Status: issueExecutionRunning}}
	recoveryTestStore(t, a)
	if !a.archiveIssue(issue.ID) {
		t.Fatal("archive failed")
	}
	if !issue.Archived || issue.Work.ActiveTaskID != "t1" || issue.Status != issueStatusInProgress ||
		issue.WorkSessionID != "work-i1" {
		t.Fatalf("archive lost running identity: %+v", issue)
	}
	reloaded, err := openContactStore(a.storePath, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.state.Issues) != 1 || reloaded.state.Issues[0].ID != issue.ID ||
		!reloaded.state.Issues[0].Archived || reloaded.state.Issues[0].Status != issueStatusInProgress ||
		reloaded.state.Issues[0].WorkSessionID != "work-i1" || len(reloaded.state.Issues[0].Executions) != 1 ||
		reloaded.state.Issues[0].Executions[0].TaskID != "t1" {
		t.Fatalf("saved archive lost running identity: %+v", reloaded.state.Issues)
	}
	if !a.restoreIssue(issue.ID) || issue.Archived {
		t.Fatal("restore failed")
	}
	if issue.Work.ActiveTaskID != "t1" || issue.WorkSessionID != "work-i1" {
		t.Fatal("restore lost running identity")
	}
	reloaded, err = openContactStore(a.storePath, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.state.Issues) != 1 || reloaded.state.Issues[0].ID != issue.ID ||
		reloaded.state.Issues[0].Archived || reloaded.state.Issues[0].WorkSessionID != "work-i1" ||
		len(reloaded.state.Issues[0].Executions) != 1 || reloaded.state.Issues[0].Executions[0].TaskID != "t1" {
		t.Fatalf("saved restore lost running identity: %+v", reloaded.state.Issues)
	}
}

func TestIssueRequirementEditOnlyInvalidatesChangedText(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	issue.Status = issueStatusInProgress
	first := issue.Requirements[0]
	second := issue.Requirements[1]
	first.State, first.Evidence = requirementSatisfied, "old proof"
	second.State, second.Evidence = requirementUnverified, ""
	recoveryTestStore(t, a)
	if !a.editIssue(issue.ID, issue.Goal, "support SMS\nshow better errors") {
		t.Fatal("edit failed")
	}
	if issue.Requirements[0] != first || first.State != requirementSatisfied || first.Evidence != "old proof" {
		t.Fatalf("unchanged requirement evidence changed: %+v", first)
	}
	if issue.Requirements[1] == second || issue.Requirements[1].State != requirementUnverified || issue.Requirements[1].Evidence != "" {
		t.Fatalf("changed requirement kept old evidence: %+v", issue.Requirements[1])
	}
	reloaded, err := openContactStore(a.storePath, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.state.Issues) != 1 || reloaded.state.Issues[0].ID != issue.ID || len(reloaded.state.Issues[0].RequirementItems) != 2 {
		t.Fatalf("saved edited issue = %+v", reloaded.state.Issues)
	}
	stored := reloaded.state.Issues[0].RequirementItems
	if stored[0].ID != first.ID || stored[0].State != requirementSatisfied || stored[0].Evidence != "old proof" ||
		stored[1].Text != "show better errors" || stored[1].State != requirementUnverified || stored[1].Evidence != "" {
		t.Fatalf("saved requirement evidence = %+v", stored)
	}
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

func writeStoredState(path string, state storedState) error {
	store := &contactStore{path: path, state: state}
	return store.saveState()
}

func TestGoalChangeInvalidatesSameTextRequirementEvidence(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	requirement := issue.Requirements[0]
	requirement.State, requirement.Evidence = requirementSatisfied, "proved against old goal"
	recoveryTestStore(t, a)
	if !a.editIssue(issue.ID, "new goal", issueRequirementsText(issue)) {
		t.Fatal("goal edit failed")
	}
	if issue.Requirements[0] != requirement || requirement.State != requirementUnverified ||
		!strings.HasPrefix(requirement.Evidence, "Previous evidence (goal changed): ") {
		t.Fatalf("goal edit kept satisfied evidence as current: %+v", requirement)
	}
	reloaded, err := openContactStore(a.storePath, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.state.Issues) != 1 || reloaded.state.Issues[0].ID != issue.ID ||
		reloaded.state.Issues[0].Goal != "new goal" || len(reloaded.state.Issues[0].RequirementItems) != 2 {
		t.Fatalf("saved goal edit = %+v", reloaded.state.Issues)
	}
	stored := reloaded.state.Issues[0].RequirementItems[0]
	if stored.ID != requirement.ID || stored.State != requirementUnverified || stored.Evidence != requirement.Evidence {
		t.Fatalf("saved goal-change evidence = %+v", stored)
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
