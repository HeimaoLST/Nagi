package main

import (
	"context"
	"testing"
)

func TestIssueToolCreateContextPreacceptAndRejectBoundary(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "session-context-boundary", "turn-context-boundary", false)
	if err != nil {
		t.Fatal(err)
	}
	ct.SessionID = scope.RuntimeSession()
	ct.Context.items = []taskContextItem{{ID: "draft-only", Name: "draft", Text: "must not be copied", Revision: 1}}
	pendingItem := taskContextItem{ID: "pending-context", Name: "snapshot", Text: "preaccept snapshot", Revision: 1}
	ct.pendingSend = &pendingSend{ToolScopeRef: scope.Ref, Context: []taskContextItem{pendingItem}}
	if got := a.issueToolTurnContext(ct, *scope); len(got) != 1 || got[0].Text != pendingItem.Text {
		t.Fatalf("preaccept context = %+v", got)
	}
	ct.pendingSend.ToolScopeRef = "different-scope"
	if got := a.issueToolTurnContext(ct, *scope); got != nil {
		t.Fatalf("unrelated pending context was copied: %+v", got)
	}
	ct.pendingSend = nil
	scope.RuntimeTaskID = "task-context-boundary"
	ct.Messages = []*message{{
		Kind: msgUser, TaskID: scope.RuntimeTaskID,
		Context: []taskContextItem{{ID: "live-context", Name: "snapshot", Text: "accepted snapshot", Revision: 1}},
	}}
	if got := a.issueToolTurnContext(ct, *scope); len(got) != 1 || got[0].Text != "accepted snapshot" {
		t.Fatalf("accepted msgUser context = %+v", got)
	}
	scope.RuntimeSessionID = "different-session"
	if got := a.issueToolTurnContext(ct, *scope); got != nil {
		t.Fatalf("cross-owner context was copied: %+v", got)
	}
}

func TestIssueToolCreateContextPersistsOnceAndDoesNotDispatch(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "session-duplicate-context", "turn-duplicate-context", false)
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	item := taskContextItem{ID: "duplicate-context", Name: "snapshot", Text: "one captured snapshot", Revision: 1}
	ct.Messages = []*message{{Kind: msgUser, TaskID: scope.RuntimeTaskID, Context: []taskContextItem{item}}}
	args := issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "context duplicate goal"})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || first.IsError {
		t.Fatalf("first create = %+v err=%v", first, err)
	}
	if len(ct.Issues) != 1 || len(ct.Issues[0].Context.items) != 1 || ct.Issues[0].Context.items[0].Text != item.Text {
		t.Fatalf("first issue context = %+v", ct.Issues)
	}
	second, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || second.IsError {
		t.Fatalf("duplicate create = %+v err=%v", second, err)
	}
	if len(ct.Issues) != 1 || len(a.issueToolLedger.Operations) != 1 || len(ct.Issues[0].Executions) != 0 ||
		len(ct.Issues[0].Context.items) != 1 || ct.Issues[0].Context.items[0].Text != item.Text {
		t.Fatalf("duplicate changed context/dispatch: issues=%d ops=%d executions=%d context=%+v",
			len(ct.Issues), len(a.issueToolLedger.Operations), len(ct.Issues[0].Executions), ct.Issues[0].Context.items)
	}
}

func TestIssueToolSupplementAndAnswerCarryTurnContext(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "session-supplement-context", "turn-supplement-context", false)
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	issue, err := a.createIssueFor(ct, "context supplement goal", "requirement")
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.createIssueFor(ct, "unrelated issue", "")
	if err != nil {
		t.Fatal(err)
	}
	a.issueToolAddScopeIssue(scope.Ref, issue.ID)
	itemC := taskContextItem{ID: "material-C", Name: "supplement-material", Text: "supplement attachment", Revision: 1}
	ct.Messages = []*message{{Kind: msgUser, TaskID: scope.RuntimeTaskID, Context: []taskContextItem{itemC}}}
	addArgs := issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "use the supplement material",
	})
	addResponse, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: addArgs})
	if err != nil || addResponse.IsError {
		t.Fatalf("add_supplement = %+v err=%v", addResponse, err)
	}
	if len(issue.Context.items) != 1 || issue.Context.items[0].ID != itemC.ID || issue.Context.items[0].Text != itemC.Text {
		t.Fatalf("supplement issue context = %+v", issue.Context.items)
	}
	if len(other.Context.items) != 0 {
		t.Fatalf("supplement context broadcast to another issue: %+v", other.Context.items)
	}
	if len(issue.Executions) != 0 {
		t.Fatal("supplement on a never-run issue auto-dispatched a run")
	}
	// A later turn carrying the same captured item must reuse the existing
	// context ID/revision rather than append it again.
	scope2, err := a.issueToolNewScope(ct.ID, "session-supplement-context", "turn-supplement-context-2", false)
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope2)
	ct.Messages = []*message{{Kind: msgUser, TaskID: scope2.RuntimeTaskID, Context: []taskContextItem{itemC}}}
	secondArgs := issueToolActionArgs(t, *scope2, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "second text from the same turn",
	})
	if second, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: secondArgs}); err != nil || second.IsError {
		t.Fatalf("second add_supplement = %+v err=%v", second, err)
	}
	if len(issue.Context.items) != 1 || issue.Context.items[0].ID != itemC.ID {
		t.Fatalf("same-turn supplement duplicated context: %+v", issue.Context.items)
	}
	// A distinct answer turn with an attachment links the answer supplement and
	// the captured context in the existing AddWith save, without a first Run.
	question := &issueQuestion{Ref: "question-context-test", Text: "which material?", RequirementsRevision: issue.RequirementsRevision, State: issueQuestionOpen}
	issue.Questions = append(issue.Questions, question)
	itemD := taskContextItem{ID: "material-D", Name: "answer-material", Text: "answer attachment", Revision: 1}
	scope3, err := a.issueToolNewScope(ct.ID, "session-supplement-context", "turn-supplement-context-3", false)
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope3)
	ct.Messages = []*message{{Kind: msgUser, TaskID: scope3.RuntimeTaskID, Context: []taskContextItem{itemD}}}
	answerArgs := issueToolActionArgs(t, *scope3, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "question_ref": question.Ref, "text": "answer with attachment",
	})
	answerResponse, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: answerArgs})
	if err != nil || answerResponse.IsError {
		t.Fatalf("answer = %+v err=%v", answerResponse, err)
	}
	if question.State != issueQuestionAnswered || question.AnswerSupplementID == "" {
		t.Fatalf("answer state = %+v", question)
	}
	if len(issue.Context.items) != 2 || issue.Context.items[1].ID != itemD.ID || issue.Context.items[1].Text != itemD.Text {
		t.Fatalf("answer issue context = %+v", issue.Context.items)
	}
	if len(issue.Executions) != 0 {
		t.Fatal("answer on a never-run issue auto-dispatched a run")
	}
}

func TestIssueToolSupplementAndAnswerSaveFailureRestoreContext(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "session-save-failure", "turn-save-failure", false)
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	issue, err := a.createIssueFor(ct, "save failure context", "")
	if err != nil {
		t.Fatal(err)
	}
	a.issueToolAddScopeIssue(scope.Ref, issue.ID)
	initial := taskContextItem{ID: "initial-context", Name: "initial", Text: "keep me", Revision: 1}
	issue.Context.items = []taskContextItem{initial}
	ct.Messages = []*message{{Kind: msgUser, TaskID: scope.RuntimeTaskID, Context: []taskContextItem{{ID: "new-supplement", Name: "new", Text: "must rollback", Revision: 1}}}}
	a.store.path = t.TempDir()
	addResponse, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{
		Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{"issue_id": issue.ID, "text": "will not save"}),
	})
	if err != nil || !addResponse.IsError {
		t.Fatalf("supplement save failure = %+v err=%v", addResponse, err)
	}
	if len(issue.Context.items) != 1 || issue.Context.items[0].ID != initial.ID {
		t.Fatalf("supplement context was not restored: %+v", issue.Context.items)
	}
	if issue.Supplements.pendingCount() != 0 {
		t.Fatalf("supplement item survived failed save: %d", issue.Supplements.pendingCount())
	}
	question := &issueQuestion{Ref: "question-save-failure", Text: "needs answer", RequirementsRevision: issue.RequirementsRevision, State: issueQuestionOpen}
	issue.Questions = append(issue.Questions, question)
	ct.Messages = []*message{{Kind: msgUser, TaskID: scope.RuntimeTaskID, Context: []taskContextItem{{ID: "answer-context", Name: "answer", Text: "must rollback too", Revision: 1}}}}
	answerResponse, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{
		Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{
			"issue_id": issue.ID, "question_ref": question.Ref, "text": "will not save answer",
		}),
	})
	if err != nil || !answerResponse.IsError {
		t.Fatalf("answer save failure = %+v err=%v", answerResponse, err)
	}
	if question.State != issueQuestionOpen || question.AnswerSupplementID != "" {
		t.Fatalf("answer state was not restored: %+v", question)
	}
	if len(issue.Context.items) != 1 || issue.Context.items[0].ID != initial.ID {
		t.Fatalf("answer context was not restored: %+v", issue.Context.items)
	}
	if issue.Supplements.pendingCount() != 0 {
		t.Fatalf("answer supplement survived failed save: %d", issue.Supplements.pendingCount())
	}
}
