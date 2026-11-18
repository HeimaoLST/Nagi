package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func newIssueToolTestApp(t *testing.T) (*appModel, *contactStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{}
	a.store = store
	a.contacts = contactsFromStore(store)
	a.selected = 0
	a.issueToolLedger = store.issueToolLedger
	a.issueToolScopes = store.issueToolScopes
	a.issueToolWaiters = map[string]chan IssueToolReceipt{}
	return a, store
}

func activateIssueToolTestScope(t *testing.T, a *appModel, scope *IssueToolScope) {
	t.Helper()
	if scope == nil || scope.Mode != "" && scope.Mode != issueToolModeConversation {
		t.Fatal("synthetic conversation scope required")
	}
	ct := a.contactByID(scope.ContactID)
	if ct == nil {
		t.Fatal("scope test contact missing")
	}
	contextID := "scope-fixture-" + ct.ID
	if ct.PiBinding == nil {
		if ct.SessionID != "" {
			t.Fatal("scope fixture cannot retag an opaque conversation session")
		}
		root := filepath.Dir(a.store.path)
		ct.PiBinding = &piConversationBinding{
			Engine: "pi",
			Context: pi.ContextBinding{
				ContextID: contextID, BotID: ct.ID, Purpose: pi.Conversation,
				SelectionRevision: 1, CWD: root,
			},
			Selection: piSelection{CWD: root, CodingTools: []string{}},
			Configuration: piConfiguration{
				WireAPI: pi.OpenAICompletions, BaseURL: "http://127.0.0.1:1/v1", Model: "scope-fixture",
				ContextTokens: 65536, OutputTokens: 4096, CredentialSource: "scope-fixture",
			},
		}
	} else if ct.PiBinding.Engine != "pi" || ct.PiBinding.Context.ContextID != contextID ||
		ct.PiBinding.Context.BotID != ct.ID || ct.PiBinding.Context.Purpose != pi.Conversation ||
		ct.PiBinding.Context.SessionRef != ct.SessionID || ct.PiBinding.Context.Used {
		t.Fatal("scope fixture cannot replace a foreign conversation binding")
	}
	ct.SessionID = scope.RuntimeSessionID
	ct.PiBinding.Context.SessionRef = ct.SessionID
	ct.SendState = sendAccepted
	ct.ActiveTaskID = "task-test"
	scope.RuntimeTaskID = "task-test"
	a.issueToolScopes[scope.Ref] = *scope
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
}
func issueToolActionArgs(t *testing.T, scope IssueToolScope, action string, extra map[string]any) json.RawMessage {
	t.Helper()
	args := map[string]any{"scope": scope.Ref, "action": action}
	for key, value := range extra {
		args[key] = value
	}
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func issueToolResponseJSON(t *testing.T, response issueToolForwardResponse) []byte {
	t.Helper()
	if len(response.Content) != 1 {
		t.Fatalf("response content = %+v", response.Content)
	}
	return []byte(response.Content[0].Text)
}

func TestIssueToolOperationRepeatsAndPersistsAcrossReload(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-1", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	args := issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{
		"goal": "first goal", "requirements": []string{"requirement one"},
	})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || first.IsError {
		t.Fatalf("first action = %+v err=%v", first, err)
	}
	if len(ct.Issues) != 1 || len(a.issueToolLedger.Operations) != 1 {
		t.Fatalf("first action state issues=%d ops=%d", len(ct.Issues), len(a.issueToolLedger.Operations))
	}
	issueID := ct.Issues[0].ID
	reloaded, err := openContactStore(a.store.path, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	b := &appModel{}
	b.store = reloaded
	b.contacts = contactsFromStore(reloaded)
	b.selected = 0
	b.issueToolLedger = reloaded.issueToolLedger
	b.issueToolScopes = reloaded.issueToolScopes
	b.issueToolWaiters = map[string]chan IssueToolReceipt{}
	second, err := b.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || second.IsError {
		t.Fatalf("duplicate after reload = %+v err=%v", second, err)
	}
	if len(b.contacts[0].Issues) != 1 || b.contacts[0].Issues[0].ID != issueID || len(b.issueToolLedger.Operations) != 1 {
		t.Fatalf("duplicate changed state issues=%d ops=%d", len(b.contacts[0].Issues), len(b.issueToolLedger.Operations))
	}
	conflictArgs := issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "different goal"})
	conflict, err := b.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: conflictArgs})
	if err != nil || !conflict.IsError {
		t.Fatalf("different payload = %+v err=%v", conflict, err)
	}
	if len(b.contacts[0].Issues) != 1 {
		t.Fatal("different payload created another issue")
	}
	if !strings.Contains(string(issueToolResponseJSON(t, conflict)), "different payload") {
		t.Fatalf("conflict response = %s", issueToolResponseJSON(t, conflict))
	}
}

func TestIssueToolSaveFailureDoesNotCreateIssue(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-2", "turn-2")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	a.store.path = t.TempDir() // a directory cannot be replaced by the atomic save
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{
		Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "must not persist"}),
	})
	if err != nil || !response.IsError {
		t.Fatalf("save failure response = %+v err=%v", response, err)
	}
	if len(ct.Issues) != 0 || len(a.issueToolLedger.Operations) != 0 {
		t.Fatalf("save failure changed state issues=%d ops=%d", len(ct.Issues), len(a.issueToolLedger.Operations))
	}
	if !strings.Contains(string(issueToolResponseJSON(t, response)), "save issue tool operation") {
		t.Fatalf("save failure response = %s", issueToolResponseJSON(t, response))
	}
}

func TestIssueToolListUsesCursor(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	if _, err := a.createIssueFor(ct, "A", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.createIssueFor(ct, "B", "b"); err != nil {
		t.Fatal(err)
	}
	scope, err := a.issueToolNewScope(ct.ID, "conversation-3", "turn-3")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	args, _ := json.Marshal(map[string]any{"scope": scope.Ref, "limit": 1})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_list", Arguments: args})
	if err != nil || first.IsError {
		t.Fatalf("first list = %+v err=%v", first, err)
	}
	var page IssueToolIssueListResult
	if err := json.Unmarshal(issueToolResponseJSON(t, first), &page); err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || page.NextCursor == "" || len(page.Issues) != 1 {
		t.Fatalf("first page = %+v", page)
	}
	args, _ = json.Marshal(map[string]any{"scope": scope.Ref, "limit": 1, "cursor": page.NextCursor})
	second, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_list", Arguments: args})
	if err != nil || second.IsError {
		t.Fatalf("second list = %+v err=%v", second, err)
	}
	var next IssueToolIssueListResult
	if err := json.Unmarshal(issueToolResponseJSON(t, second), &next); err != nil {
		t.Fatal(err)
	}
	if len(next.Issues) != 1 || next.Issues[0].ID == page.Issues[0].ID {
		t.Fatalf("cursor repeated first page: first=%+v second=%+v", page, next)
	}
}

func TestIssueToolQueryDoesNotSwitchSelectedContactOrPage(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "query goal", "query requirement")
	if err != nil {
		t.Fatal(err)
	}
	ct.SelectedIssueID = issue.ID
	scope, err := a.issueToolNewScope(ct.ID, "conversation-4", "turn-4")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	beforeSelected := ct.SelectedIssueID
	args, _ := json.Marshal(map[string]any{"scope": scope.Ref, "issue_id": issue.ID})
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_get", Arguments: args})
	if err != nil || response.IsError {
		t.Fatalf("get = %+v err=%v", response, err)
	}
	if a.selectedContact().ID != ct.ID || ct.SelectedIssueID != beforeSelected || a.issueResult.Open || a.aux == "issue-result" {
		t.Fatalf("query changed selection/page: contact=%q issue=%q result=%v aux=%q", a.selectedContact().ID, ct.SelectedIssueID, a.issueResult.Open, a.aux)
	}
}

func TestIssueToolRunWithoutRuntimeRecordsUnknownAndDoesNotDuplicate(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "run goal", "run requirement")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := a.issueToolNewScope(ct.ID, "conversation-5", "turn-5")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	args := issueToolActionArgs(t, *scope, issueToolActionRun, map[string]any{"issue_id": issue.ID})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || !first.IsError {
		t.Fatalf("run without runtime = %+v err=%v", first, err)
	}
	if len(issue.Executions) != 0 || len(a.issueToolLedger.Operations) != 1 {
		t.Fatalf("run failure state executions=%d ops=%d", len(issue.Executions), len(a.issueToolLedger.Operations))
	}
	operation := a.issueToolLedger.Operations[0]
	if operation.Receipt == nil || operation.Receipt.State != issueToolReceiptUnknown || operation.Receipt.Error == nil {
		t.Fatalf("run failure receipt = %+v", operation.Receipt)
	}
	second, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || !second.IsError || len(issue.Executions) != 0 {
		t.Fatalf("duplicate run failure = %+v err=%v executions=%d", second, err, len(issue.Executions))
	}
}

func TestIssueToolScopeDeniesOtherContactIssue(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	first := a.selectedContact()
	second := &contact{ID: "contact-other", Name: "Other", Workspace: a.workDir}
	otherIssue, err := a.createIssueFor(second, "other goal", "")
	if err != nil {
		t.Fatal(err)
	}
	a.contacts = append(a.contacts, second)
	scope, err := a.issueToolNewScope(first.ID, "conversation-6", "turn-6")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	args, _ := json.Marshal(map[string]any{"scope": scope.Ref, "issue_id": otherIssue.ID})
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_get", Arguments: args})
	if err != nil || !response.IsError {
		t.Fatalf("cross-contact get = %+v err=%v", response, err)
	}
	var toolErr IssueToolError
	if err := json.Unmarshal(issueToolResponseJSON(t, response), &toolErr); err != nil {
		t.Fatal(err)
	}
	if toolErr.Code != "not_found" && toolErr.Code != "scope_denied" {
		t.Fatalf("cross-contact error = %+v", toolErr)
	}
}

func TestIssueToolCapturedCreateLeavesSelectedContactFormAndDraftAlone(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	first := a.selectedContact()
	second := &contact{ID: "contact-second", Name: "Second", Workspace: a.workDir, Draft: "second draft", SelectedIssueID: "second-issue"}
	a.contacts = append(a.contacts, second)
	a.selected = 1
	a.issueFormOpen = true
	a.issueGoal = "form goal"
	a.issueRequirements = "form requirements"
	scope, err := a.issueToolNewScope(first.ID, "conversation-7", "turn-7")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{
		Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "captured goal"}),
	})
	if err != nil || response.IsError {
		t.Fatalf("captured create = %+v err=%v", response, err)
	}
	if a.selected != 1 || a.selectedContact() != second || second.Draft != "second draft" || second.SelectedIssueID != "second-issue" {
		t.Fatalf("captured create changed selected contact state: selected=%d contact=%+v", a.selected, a.selectedContact())
	}
	if !a.issueFormOpen || a.issueGoal != "form goal" || a.issueRequirements != "form requirements" {
		t.Fatalf("captured create changed issue form: open=%v goal=%q requirements=%q", a.issueFormOpen, a.issueGoal, a.issueRequirements)
	}
	if len(first.Issues) != 1 || first.Issues[0].Goal != "captured goal" {
		t.Fatalf("captured create did not attach to captured contact: %+v", first.Issues)
	}
}

func TestIssueToolScopeUsesLatestTaskBinding(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	ct.SessionID = "conversation-late"
	ct.SendState = sendSubmitting
	ct.pendingSend = &pendingSend{ToolScopeRef: ""}
	scope, err := a.issueToolNewScope(ct.ID, "conversation-late", "turn-late")
	if err != nil {
		t.Fatal(err)
	}
	ct.pendingSend.ToolScopeRef = scope.Ref
	if !a.issueToolScopeActive(*scope) {
		t.Fatal("scope was not active before Runtime task binding")
	}
	captured := *scope // RuntimeTaskID intentionally still empty
	a.issueToolBindTask(scope.Ref, "task-late")
	ct.SendState = sendAccepted
	ct.ActiveTaskID = "task-late"
	request := IssueToolActionRequest{ScopeRef: captured.Ref, Action: issueToolActionCreate, Goal: "late binding"}
	operation, created, existing, _, err := a.issueToolReserveForTurn(captured, request)
	if err != nil || !created || existing || operation == nil {
		t.Fatalf("consumer reserve with captured old scope = op=%+v created=%v existing=%v err=%v", operation, created, existing, err)
	}
	response, err := a.issueToolCreate(captured, request, operation)
	if err != nil || response.IsError {
		t.Fatalf("consumer create with captured old scope = %+v err=%v", response, err)
	}
	if len(ct.Issues) != 1 || ct.Issues[0].Goal != "late binding" {
		t.Fatalf("late-bound create = %+v", ct.Issues)
	}
}

func TestIssueToolInactiveScopeKeepsOldReceiptButRejectsNewAction(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-old", "turn-old")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	createArgs := issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "old goal"})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: createArgs})
	if err != nil || first.IsError {
		t.Fatalf("active create = %+v err=%v", first, err)
	}
	issue := ct.Issues[0]
	// End the captured turn without creating a new scope.
	ct.ActiveTaskID = ""
	ct.SendState = sendIdle
	ct.pendingSend = nil
	duplicate, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: createArgs})
	if err != nil || duplicate.IsError || len(ct.Issues) != 1 {
		t.Fatalf("inactive duplicate receipt = %+v err=%v issues=%d", duplicate, err, len(ct.Issues))
	}
	updateArgs := issueToolActionArgs(t, *scope, issueToolActionUpdate, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "goal": "new goal",
	})
	newAction, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: updateArgs})
	if err != nil || !newAction.IsError {
		t.Fatalf("inactive new action = %+v err=%v", newAction, err)
	}
	if issue.Goal != "old goal" || len(ct.Issues) != 1 {
		t.Fatalf("inactive scope mutated issue: %+v", issue)
	}
	listArgs, _ := json.Marshal(map[string]any{"scope": scope.Ref})
	list, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_list", Arguments: listArgs})
	if err != nil || list.IsError {
		t.Fatalf("inactive retained read query = %+v err=%v", list, err)
	}
}

func TestIssueToolTruncatePreservesUTF8(t *testing.T) {
	text := strings.Repeat("中文", 10)
	got := truncateIssueToolText(text, 3)
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "中文中") || !strings.Contains(got, "truncated") {
		t.Fatalf("UTF-8 truncation = %q", got)
	}
}

func TestIssueToolConcurrentSameCreateDoesNotDuplicate(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-concurrent", "turn-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	pump := newUIPump(t, a)
	args := issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "one create"})
	type result struct {
		response issueToolForwardResponse
		err      error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
			results <- result{response: response, err: err}
		}()
	}
	wg.Wait()
	close(results)
	pump.close()
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
	}
	if len(ct.Issues) != 1 || len(a.issueToolLedger.Operations) != 1 {
		t.Fatalf("concurrent create duplicated: issues=%d ops=%d", len(ct.Issues), len(a.issueToolLedger.Operations))
	}
}

func TestIssueToolReservedActionRechecksOwnerBeforeSideEffect(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-atomic", "turn-atomic")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	request := IssueToolActionRequest{
		ScopeRef: scope.Ref, Action: issueToolActionCreate, Goal: "atomic goal",
	}
	operation, created, existing, _, err := a.issueToolReserveForTurn(*scope, request)
	if err != nil || !created || existing || operation == nil {
		t.Fatalf("reserve = %+v created=%v existing=%v err=%v", operation, created, existing, err)
	}
	ct.Archived = true
	response, err := a.issueToolCreate(*scope, request, operation)
	if err != nil || !response.IsError {
		t.Fatalf("archived reserved action = %+v err=%v", response, err)
	}
	if len(ct.Issues) != 0 || operation.Receipt != nil {
		t.Fatalf("reserved action reached side effect: issues=%d receipt=%+v", len(ct.Issues), operation.Receipt)
	}
}

func TestIssueToolAcceptedSaveFailureKeepsKnownIDs(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "known ids", "")
	if err != nil {
		t.Fatal(err)
	}
	operation := &IssueToolOperation{ID: "issue-op-known", Action: issueToolActionRun, TargetIssueID: issue.ID}
	a.issueToolLedger.Operations = append(a.issueToolLedger.Operations, operation)
	execution := &issueExecution{
		ID: "execution-known", SessionID: "session-known", TaskID: "task-known",
		PiIntent: true, PiRun: &piActivityRun{WorkerEpoch: "epoch", ContextID: "activity", SessionRef: "session-known", RunID: "task-known", Admitted: true}, ToolOperationID: operation.ID,
	}
	waiter := make(chan IssueToolReceipt, 1)
	a.issueToolWaiters[operation.ID] = waiter
	a.store.path = t.TempDir() // force receipt persistence to fail
	a.issueToolCompleteExecution(execution, true, "")
	receipt := <-waiter
	if receipt.State != issueToolReceiptUnknown || receipt.IssueID != issue.ID ||
		receipt.ExecutionID != execution.ID || receipt.SessionID != execution.SessionID ||
		receipt.TaskID != execution.TaskID || !reflect.DeepEqual(receipt.PiRun, execution.PiRun) {
		t.Fatalf("accepted save failure receipt lost known identity: %+v", receipt)
	}
}

func TestIssueToolShutdownRejectsNewActionButKeepsReceipt(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	a.issueToolShutdown = make(chan struct{})
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-shutdown", "turn-shutdown")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	createArgs := issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "before shutdown"})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: createArgs})
	if err != nil || first.IsError {
		t.Fatalf("pre-shutdown create = %+v err=%v", first, err)
	}
	issue := ct.Issues[0]
	a.beginShutdown()
	updateArgs := issueToolActionArgs(t, *scope, issueToolActionUpdate, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "goal": "after shutdown",
	})
	blocked, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: updateArgs})
	if err != nil || !blocked.IsError || len(ct.Issues) != 1 || len(a.issueToolLedger.Operations) != 1 {
		t.Fatalf("shutdown action = %+v err=%v issues=%d ops=%d", blocked, err, len(ct.Issues), len(a.issueToolLedger.Operations))
	}
	duplicate, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: createArgs})
	if err != nil || duplicate.IsError || len(a.issueToolLedger.Operations) != 1 {
		t.Fatalf("shutdown old receipt = %+v err=%v ops=%d", duplicate, err, len(a.issueToolLedger.Operations))
	}
}

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

func TestIssueToolWaitAnswerResumeUsesQuestionAnd142Ledger(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "needs input", "current requirement")
	if err != nil {
		t.Fatal(err)
	}
	attachIssueToolTestExecution(issue)
	scope, err := a.issueToolNewScope(ct.ID, "conversation-q", "turn-q")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	waitArgs := issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "text": "Which method should I use?",
	})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: waitArgs})
	if err != nil || first.IsError {
		t.Fatalf("wait = %+v err=%v", first, err)
	}
	var waitReceipt IssueToolReceipt
	if err := json.Unmarshal(issueToolResponseJSON(t, first), &waitReceipt); err != nil {
		t.Fatal(err)
	}
	if waitReceipt.QuestionRef == "" || waitReceipt.IssueStatus != issueStatusWaitingUser || waitReceipt.QuestionState != issueQuestionOpen ||
		issue.Status != issueStatusWaitingUser || issue.openQuestion() == nil || issue.Supplements.AutoContinue || len(issue.Questions) != 1 {
		t.Fatalf("wait state = receipt=%+v question=%+v status=%q auto=%v", waitReceipt, issue.openQuestion(), issue.Status, issue.Supplements.AutoContinue)
	}
	questionRef := waitReceipt.QuestionRef
	if a.runIssue(issue.ID) {
		t.Fatal("run bypassed the open structured question")
	}
	repeated, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: waitArgs})
	if err != nil || repeated.IsError || len(issue.Questions) != 1 {
		t.Fatalf("repeated wait = %+v err=%v questions=%d", repeated, err, len(issue.Questions))
	}
	resumeArgs := issueToolActionArgs(t, *scope, issueToolActionResume, map[string]any{"issue_id": issue.ID})
	if a.resumeIssueFor(ct, issue.ID) {
		t.Fatal("UI resume bypassed the open structured question")
	}
	resumed, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: resumeArgs})
	if err != nil || !resumed.IsError || issue.Status != issueStatusWaitingUser || issue.openQuestion() == nil {
		t.Fatalf("resume with open question = %+v err=%v status=%q question=%+v", resumed, err, issue.Status, issue.openQuestion())
	}
	answerArgs := issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "Use SMS.", "question_ref": questionRef,
	})
	answered, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: answerArgs})
	if err != nil || answered.IsError {
		t.Fatalf("answer = %+v err=%v", answered, err)
	}
	var answerReceipt IssueToolReceipt
	if err := json.Unmarshal(issueToolResponseJSON(t, answered), &answerReceipt); err != nil {
		t.Fatal(err)
	}
	question := issue.questionByRef(questionRef)
	if question == nil || question.State != issueQuestionAnswered || question.AnswerSupplementID == "" ||
		answerReceipt.SupplementID != question.AnswerSupplementID || answerReceipt.IssueStatus != issueStatusInProgress ||
		answerReceipt.QuestionState != issueQuestionAnswered || issue.Supplements.pendingCount() != 1 ||
		issue.Status != issueStatusInProgress || !issue.Supplements.AutoContinue {
		t.Fatalf("answer state = question=%+v receipt=%+v pending=%d status=%q auto=%v", question, answerReceipt, issue.Supplements.pendingCount(), issue.Status, issue.Supplements.AutoContinue)
	}
	duplicate, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: answerArgs})
	if err != nil || duplicate.IsError || issue.Supplements.pendingCount() != 1 {
		t.Fatalf("duplicate answer = %+v err=%v pending=%d", duplicate, err, issue.Supplements.pendingCount())
	}
	// A new turn cannot answer the same open question twice.
	secondScope, err := a.issueToolNewScope(ct.ID, "conversation-q2", "turn-q2")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, secondScope)
	crossTurn, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *secondScope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "Use SMS.", "question_ref": questionRef,
	})})
	if err != nil || crossTurn.IsError || issue.Supplements.pendingCount() != 1 {
		t.Fatalf("cross-turn duplicate answer = %+v err=%v pending=%d", crossTurn, err, issue.Supplements.pendingCount())
	}
	changedScope, err := a.issueToolNewScope(ct.ID, "conversation-q3", "turn-q3")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, changedScope)
	changed, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *changedScope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "Use email instead.", "question_ref": questionRef,
	})})
	if err != nil || !changed.IsError || !strings.Contains(string(issueToolResponseJSON(t, changed)), "question_answer_conflict") ||
		issue.Supplements.pendingCount() != 1 {
		t.Fatalf("changed answer = %+v err=%v pending=%d", changed, err, issue.Supplements.pendingCount())
	}
	reloaded, err := openContactStore(a.store.path, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	reloadedContacts := contactsFromStore(reloaded)
	if len(reloadedContacts) != 1 || len(reloadedContacts[0].Issues) != 1 {
		t.Fatalf("reloaded question state = %+v", reloadedContacts)
	}
	reloadedIssue := reloadedContacts[0].Issues[0]
	reloadedQuestion := reloadedIssue.questionByRef(questionRef)
	if reloadedQuestion == nil || reloadedQuestion.State != issueQuestionAnswered ||
		reloadedQuestion.AnswerSupplementID != question.AnswerSupplementID || reloadedIssue.Supplements.pendingCount() != 1 {
		t.Fatalf("reloaded question/answer = %+v", reloadedIssue)
	}
}

func TestIssueToolWrongOrStaleQuestionRefIsRejected(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stale    bool
		wantCode string
	}{
		{name: "wrong", wantCode: "question_not_found"},
		{name: "stale", stale: true, wantCode: "revision_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ct, event := piRunStartedTestApp(t)
			a.storePath, a.workDir = a.store.path, filepath.Dir(a.store.path)
			a.issueToolLedger, a.issueToolScopes = a.store.issueToolLedger, a.store.issueToolScopes
			a.issueToolWaiters = map[string]chan IssueToolReceipt{}
			issue, err := a.createIssueFor(ct, "question errors", "")
			if err != nil {
				t.Fatal(err)
			}
			scope, err := a.issueToolNewScope(ct.ID, ct.SessionID, event.RunID)
			if err != nil {
				t.Fatal(err)
			}
			ct.pendingSend = &pendingSend{Draft: "Question reference validation", State: "submitting"}
			if err := a.issueToolAttachPendingScope(ct.ID, scope.Ref); err != nil {
				t.Fatal(err)
			}
			a.issueToolBindTask(scope.Ref, event.RunID)
			a.applyPiEvent(a.connectionGeneration, event)
			if ct.SendState != sendAccepted || ct.ActiveTaskID != event.RunID {
				t.Fatalf("current Pi conversation was not admitted: %+v", ct)
			}
			waitArgs := issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
				"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "text": "Need input",
			})
			wait, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: waitArgs})
			if err != nil || wait.IsError {
				t.Fatalf("wait setup = %+v err=%v", wait, err)
			}
			question := issue.openQuestion()
			if question == nil || question.SourceTaskID != event.RunID || question.SourceScopeRef != scope.Ref || question.SourceTurnID != scope.TurnID {
				t.Fatalf("question lost the admitted conversation owner: %+v", question)
			}
			questionRef := "question-wrong"
			if tc.stale {
				question.RequirementsRevision = 0
				questionRef = question.Ref
			}
			response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{
				"issue_id": issue.ID, "text": "answer", "question_ref": questionRef,
			})})
			if err != nil || !response.IsError || !strings.Contains(string(issueToolResponseJSON(t, response)), tc.wantCode) {
				t.Fatalf("%s question = %+v err=%v", tc.name, response, err)
			}
			if question.State != issueQuestionOpen || question.AnswerSupplementID != "" {
				t.Fatalf("rejected answer changed the open question: %+v", question)
			}
		})
	}
}

func TestIssueToolAnswerAfterProductCancelIsRejected(t *testing.T) {
	a, ct, event := piRunStartedTestApp(t)
	a.storePath, a.workDir = a.store.path, filepath.Dir(a.store.path)
	a.issueToolLedger, a.issueToolScopes = a.store.issueToolLedger, a.store.issueToolScopes
	a.issueToolWaiters = map[string]chan IssueToolReceipt{}
	issue, err := a.createIssueFor(ct, "cancel question", "")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := a.issueToolNewScope(ct.ID, ct.PiBinding.Context.SessionRef, event.RunID)
	if err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "Cancel question validation", State: "submitting"}
	if err := a.issueToolAttachPendingScope(ct.ID, scope.Ref); err != nil {
		t.Fatal(err)
	}
	a.issueToolBindTask(scope.Ref, event.RunID)
	a.applyPiEvent(a.connectionGeneration, event)
	if ct.SendState != sendAccepted || ct.ActiveTaskID != event.RunID || !a.issueToolScopeActive(*scope) {
		t.Fatalf("current Pi conversation was not admitted: %+v", ct)
	}
	wait, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "text": "need answer",
	})})
	if err != nil || wait.IsError {
		t.Fatalf("wait = %+v err=%v", wait, err)
	}
	question := issue.openQuestion()
	if question == nil || question.SourceTaskID != event.RunID || question.SourceScopeRef != scope.Ref || question.SourceTurnID != scope.TurnID {
		t.Fatalf("question lost the admitted conversation owner: %+v", question)
	}
	if !a.cancelIssueProductFor(ct, issue.ID) || issue.Status != issueStatusCanceled {
		t.Fatalf("product cancel = status %q", issue.Status)
	}
	if !a.issueToolScopeActive(*scope) {
		t.Fatal("product cancellation invalidated the conversation scope")
	}
	answer, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "answer", "question_ref": question.Ref,
	})})
	if err != nil || !answer.IsError || issue.Supplements.pendingCount() != 0 {
		t.Fatalf("canceled answer = %+v err=%v pending=%d", answer, err, issue.Supplements.pendingCount())
	}
}

func TestIssueToolAnsweredQuestionMissingSupplementIsUnknown(t *testing.T) {
	a, ct, event := piRunStartedTestApp(t)
	a.storePath, a.workDir = a.store.path, filepath.Dir(a.store.path)
	a.issueToolLedger, a.issueToolScopes = a.store.issueToolLedger, a.store.issueToolScopes
	a.issueToolWaiters = map[string]chan IssueToolReceipt{}
	issue, err := a.createIssueFor(ct, "missing answer", "")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := a.issueToolNewScope(ct.ID, ct.PiBinding.Context.SessionRef, event.RunID)
	if err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "Missing answer validation", State: "submitting"}
	if err := a.issueToolAttachPendingScope(ct.ID, scope.Ref); err != nil {
		t.Fatal(err)
	}
	a.issueToolBindTask(scope.Ref, event.RunID)
	a.applyPiEvent(a.connectionGeneration, event)
	if ct.SendState != sendAccepted || ct.ActiveTaskID != event.RunID || !a.issueToolScopeActive(*scope) {
		t.Fatalf("current Pi conversation was not admitted: %+v", ct)
	}
	wait, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "text": "question",
	})})
	if err != nil || wait.IsError {
		t.Fatalf("wait = %+v err=%v", wait, err)
	}
	question := issue.openQuestion()
	if question == nil || question.SourceTaskID != event.RunID || question.SourceScopeRef != scope.Ref || question.SourceTurnID != scope.TurnID {
		t.Fatalf("question lost the admitted conversation owner: %+v", question)
	}
	question.State = issueQuestionAnswered
	question.AnswerSupplementID = "supplement-missing"
	settled := event
	settled.Name, settled.Seq = pi.RunSettled, event.Seq+1
	settled.Data, err = json.Marshal(pi.RunSettledData{Status: pi.Completed})
	if err != nil {
		t.Fatal(err)
	}
	a.applyPiEvent(a.connectionGeneration, settled)
	if ct.SendState != sendIdle || ct.ActiveTaskID != "" || a.localSaveErr != "" {
		t.Fatalf("previous conversation run did not settle: contact=%+v save=%q", ct, a.localSaveErr)
	}
	event.RunID, event.Seq = newIssueToolTurnID(), settled.Seq+1
	newScope, err := a.issueToolNewScope(ct.ID, ct.PiBinding.Context.SessionRef, newIssueToolTurnID())
	if err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "Resolve missing answer", State: "submitting"}
	if err := a.issueToolAttachPendingScope(ct.ID, newScope.Ref); err != nil {
		t.Fatal(err)
	}
	ct.ActiveTaskID, ct.SendState = event.RunID, sendSubmitting
	a.issueToolBindTask(newScope.Ref, event.RunID)
	a.applyPiEvent(a.connectionGeneration, event)
	if ct.SendState != sendAccepted || ct.ActiveTaskID != event.RunID || !a.issueToolScopeActive(*newScope) {
		t.Fatalf("next Pi conversation turn was not admitted: %+v", ct)
	}
	answer, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *newScope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "answer", "question_ref": question.Ref,
	})})
	if err != nil || !answer.IsError || !strings.Contains(string(issueToolResponseJSON(t, answer)), "operation_unknown") ||
		issue.Supplements.pendingCount() != 0 {
		t.Fatalf("missing answer supplement = %+v err=%v pending=%d", answer, err, issue.Supplements.pendingCount())
	}
}

func TestIssueToolWaitSupersedesChangedQuestionAndRevision(t *testing.T) {
	a, ct, event := piRunStartedTestApp(t)
	a.storePath, a.workDir = a.store.path, filepath.Dir(a.store.path)
	a.issueToolLedger, a.issueToolScopes = a.store.issueToolLedger, a.store.issueToolScopes
	a.issueToolWaiters = map[string]chan IssueToolReceipt{}
	issue, err := a.createIssueFor(ct, "supersede", "first requirement")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := a.issueToolNewScope(ct.ID, ct.PiBinding.Context.SessionRef, event.RunID)
	if err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "Initial question", State: "submitting"}
	if err := a.issueToolAttachPendingScope(ct.ID, scope.Ref); err != nil {
		t.Fatal(err)
	}
	a.issueToolBindTask(scope.Ref, event.RunID)
	a.applyPiEvent(a.connectionGeneration, event)
	if ct.SendState != sendAccepted || ct.ActiveTaskID != event.RunID || !a.issueToolScopeActive(*scope) {
		t.Fatalf("current Pi conversation was not admitted: %+v", ct)
	}
	wait := func(scope *IssueToolScope, text string, revision int) {
		t.Helper()
		response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
			"issue_id": issue.ID, "expected_revision": revision, "text": text,
		})})
		if err != nil || response.IsError {
			t.Fatalf("wait %q = %+v err=%v", text, response, err)
		}
	}
	wait(scope, "first question", issue.RequirementsRevision)
	first := issue.openQuestion()
	if first == nil || first.SourceTaskID != event.RunID || first.SourceScopeRef != scope.Ref || first.SourceTurnID != scope.TurnID {
		t.Fatalf("question lost the admitted conversation owner: %+v", first)
	}
	firstRef := first.Ref
	settled := event
	settled.Name, settled.Seq = pi.RunSettled, event.Seq+1
	settled.Data, err = json.Marshal(pi.RunSettledData{Status: pi.Completed})
	if err != nil {
		t.Fatal(err)
	}
	a.applyPiEvent(a.connectionGeneration, settled)
	if ct.SendState != sendIdle || ct.ActiveTaskID != "" || a.localSaveErr != "" {
		t.Fatalf("first conversation run did not settle: contact=%+v save=%q", ct, a.localSaveErr)
	}
	event.RunID, event.Seq = newIssueToolTurnID(), settled.Seq+1
	secondScope, err := a.issueToolNewScope(ct.ID, ct.PiBinding.Context.SessionRef, newIssueToolTurnID())
	if err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "Correct the question", State: "submitting"}
	if err := a.issueToolAttachPendingScope(ct.ID, secondScope.Ref); err != nil {
		t.Fatal(err)
	}
	ct.ActiveTaskID, ct.SendState = event.RunID, sendSubmitting
	a.issueToolBindTask(secondScope.Ref, event.RunID)
	a.applyPiEvent(a.connectionGeneration, event)
	if ct.SendState != sendAccepted || ct.ActiveTaskID != event.RunID || !a.issueToolScopeActive(*secondScope) {
		t.Fatalf("second Pi conversation turn was not admitted: %+v", ct)
	}
	wait(secondScope, "corrected question", issue.RequirementsRevision)
	if len(issue.Questions) != 2 || issue.Questions[0].Ref != firstRef || issue.Questions[0].State != issueQuestionSuperseded ||
		issue.Questions[1].State != issueQuestionOpen || issue.Questions[1].Text != "corrected question" {
		t.Fatalf("superseded questions = %+v", issue.Questions)
	}
	secondRef := issue.openQuestion().Ref
	if !a.editIssueFor(ct, issue.ID, issue.Goal, "second requirement") {
		t.Fatalf("edit requirements: %s", a.issueFormError)
	}
	settled.RunID, settled.Seq = event.RunID, event.Seq+1
	a.applyPiEvent(a.connectionGeneration, settled)
	if ct.SendState != sendIdle || ct.ActiveTaskID != "" || a.localSaveErr != "" {
		t.Fatalf("second conversation run did not settle: contact=%+v save=%q", ct, a.localSaveErr)
	}
	event.RunID, event.Seq = newIssueToolTurnID(), settled.Seq+1
	thirdScope, err := a.issueToolNewScope(ct.ID, ct.PiBinding.Context.SessionRef, newIssueToolTurnID())
	if err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "Question for revised requirements", State: "submitting"}
	if err := a.issueToolAttachPendingScope(ct.ID, thirdScope.Ref); err != nil {
		t.Fatal(err)
	}
	ct.ActiveTaskID, ct.SendState = event.RunID, sendSubmitting
	a.issueToolBindTask(thirdScope.Ref, event.RunID)
	a.applyPiEvent(a.connectionGeneration, event)
	if ct.SendState != sendAccepted || ct.ActiveTaskID != event.RunID || !a.issueToolScopeActive(*thirdScope) {
		t.Fatalf("third Pi conversation turn was not admitted: %+v", ct)
	}
	wait(thirdScope, "new revision question", issue.RequirementsRevision)
	if len(issue.Questions) != 3 || issue.questionByRef(secondRef).State != issueQuestionSuperseded {
		t.Fatalf("revision supersede = %+v", issue.Questions)
	}
	detail, err := issueToolGet(*thirdScope, []*productIssue{issue}, issue.ID)
	if err != nil || detail.Issue.QuestionRevision != issue.RequirementsRevision || detail.Issue.Question == "" {
		t.Fatalf("query question/revision = %+v err=%v", detail, err)
	}
}

func TestIssueToolResumeWithoutStructuredQuestionStillWorks(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "manual waiting", "")
	if err != nil {
		t.Fatal(err)
	}
	attachIssueToolTestExecution(issue)
	if !a.markIssueWaitingFor(ct, issue.ID) || issue.Status != issueStatusWaitingUser || issue.openQuestion() != nil {
		t.Fatalf("manual wait state = status=%q question=%+v", issue.Status, issue.openQuestion())
	}
	scope, _ := a.issueToolNewScope(ct.ID, "conversation-manual", "turn-manual")
	activateIssueToolTestScope(t, a, scope)
	resume, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionResume, map[string]any{"issue_id": issue.ID})})
	if err != nil || resume.IsError || issue.Status != issueStatusInProgress {
		t.Fatalf("manual resume = %+v err=%v status=%q", resume, err, issue.Status)
	}
}

func TestIssueToolReplacementQuestionSaveFailureKeepsOriginalOpen(t *testing.T) {
	a, ct, event := piRunStartedTestApp(t)
	a.storePath, a.workDir = a.store.path, filepath.Dir(a.store.path)
	a.issueToolLedger, a.issueToolScopes = a.store.issueToolLedger, a.store.issueToolScopes
	a.issueToolWaiters = map[string]chan IssueToolReceipt{}
	issue, err := a.createIssueFor(ct, "atomic replacement", "requirement")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := a.issueToolNewScope(ct.ID, ct.PiBinding.Context.SessionRef, event.RunID)
	if err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "Atomic question replacement", State: "submitting"}
	if err := a.issueToolAttachPendingScope(ct.ID, scope.Ref); err != nil {
		t.Fatal(err)
	}
	a.issueToolBindTask(scope.Ref, event.RunID)
	a.applyPiEvent(a.connectionGeneration, event)
	if ct.SendState != sendAccepted || ct.ActiveTaskID != event.RunID || !a.issueToolScopeActive(*scope) {
		t.Fatalf("current Pi conversation was not admitted: %+v", ct)
	}
	wait, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "text": "original question",
	})})
	if err != nil || wait.IsError {
		t.Fatalf("wait = %+v err=%v", wait, err)
	}
	original := issue.openQuestion()
	if original == nil || original.SourceTaskID != event.RunID || original.SourceScopeRef != scope.Ref || original.SourceTurnID != scope.TurnID {
		t.Fatalf("question lost the admitted conversation owner: %+v", original)
	}
	originalRef, originalText := original.Ref, original.Text
	issue.Supplements.AutoContinue = true
	replacement := &issueQuestion{
		Ref: "question-replacement", Text: "replacement question", RequirementsRevision: issue.RequirementsRevision,
		SourceScopeRef: scope.Ref, SourceTurnID: scope.TurnID, State: issueQuestionOpen, CreatedAt: nowMillis(),
	}
	operation := &IssueToolOperation{ID: "issue-op-replacement", Action: issueToolActionWait, TargetIssueID: issue.ID}
	receipt := &IssueToolReceipt{State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: replacement.Ref}
	a.store.path = t.TempDir()
	if a.markIssueWaitingForQuestion(ct, issue.ID, originalRef, replacement, operation, receipt) {
		t.Fatal("replacement wait unexpectedly saved with a failing store")
	}
	if a.localSaveErr == "" {
		t.Fatal("replacement wait did not reach the filesystem save failure")
	}
	if issue.openQuestion() == nil || issue.openQuestion().Ref != originalRef || issue.openQuestion().Text != originalText ||
		issue.openQuestion().State != issueQuestionOpen || len(issue.Questions) != 1 ||
		issue.Status != issueStatusWaitingUser || !issue.Supplements.AutoContinue || operation.Receipt != nil {
		t.Fatalf("failed replacement lost original question: questions=%+v status=%q auto=%v receipt=%+v", issue.Questions, issue.Status, issue.Supplements.AutoContinue, operation.Receipt)
	}
}

func TestIssueToolWaitBeforeTaskBindingUsesScopeThenRealTask(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "wait before accept", "")
	if err != nil {
		t.Fatal(err)
	}
	ct.SessionID = "conversation-pre-bind"
	ct.SendState = sendSubmitting
	ct.pendingSend = &pendingSend{}
	scope, err := a.issueToolNewScope(ct.ID, "conversation-pre-bind", "turn-pre-bind")
	if err != nil {
		t.Fatal(err)
	}
	ct.pendingSend.ToolScopeRef = scope.Ref
	if !a.issueToolScopeActive(*scope) {
		t.Fatal("submitting scope not active before accept")
	}
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "text": "question before accept",
	})})
	if err != nil || response.IsError {
		t.Fatalf("pre-accept wait = %+v err=%v", response, err)
	}
	question := issue.openQuestion()
	if question == nil || question.SourceTaskID != "" || question.SourceScopeRef != scope.Ref || question.SourceTurnID != scope.TurnID {
		t.Fatalf("pre-accept question source = %+v", question)
	}
	a.issueToolBindTask(scope.Ref, "task-after-accept")
	if question.SourceTaskID != "task-after-accept" {
		t.Fatalf("question task was not bound to the real accepted task: %+v", question)
	}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(a.store.path, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	reloadedIssue := contactsFromStore(reloaded)[0].Issues[0]
	reloadedQuestion := reloadedIssue.questionByRef(question.Ref)
	if reloadedQuestion == nil || reloadedQuestion.SourceTaskID != "task-after-accept" || reloadedQuestion.SourceScopeRef != scope.Ref {
		t.Fatalf("reloaded bound question = %+v", reloadedQuestion)
	}
}

func TestIssueToolConversationIssueListIsBoundedAndScopeOwned(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	for i := 0; i < 100; i++ {
		ct.Issues = append(ct.Issues, &productIssue{
			ID: fmt.Sprintf("issue-%03d", i), ContactID: ct.ID,
			Goal: strings.Repeat("有界 garden schedule ", 100), Status: issueStatusNotStarted, RequirementsRevision: 1,
		})
	}
	scope, err := a.issueToolNewScope(ct.ID, "catalog-session", "catalog-turn")
	if err != nil {
		t.Fatal(err)
	}
	ct.Issues = append(ct.Issues,
		&productIssue{ID: "new-after-scope", ContactID: ct.ID, Goal: "Not in this issued scope"},
		&productIssue{ID: "foreign-issue", ContactID: "another-contact", Goal: "Foreign issue"})
	other := &contact{ID: "selected-elsewhere", Name: "Elsewhere"}
	a.contacts = append(a.contacts, other)
	a.selected = len(a.contacts) - 1
	before, _ := json.Marshal(ct.Issues)
	input, err := a.issueToolConversationIssueList(scope)
	if err != nil {
		t.Fatal(err)
	}
	var list IssueToolIssueListResult
	if err := json.Unmarshal([]byte(input.Text), &list); err != nil {
		t.Fatal(err)
	}
	if len(input.Text) > issueToolQueryMaxBytes || !utf8.ValidString(input.Text) ||
		len(list.Issues) == 0 || !list.HasMore || list.NextCursor == "" {
		t.Fatalf("bounded discovery page: bytes=%d issues=%d more=%v cursor=%q", len(input.Text), len(list.Issues), list.HasMore, list.NextCursor)
	}
	for _, issue := range list.Issues {
		if !scope.AllowsIssueID(issue.ID) || issue.ID == "foreign-issue" || issue.ID == "new-after-scope" {
			t.Fatalf("issue outside captured scope: %+v", issue)
		}
	}
	next, err := issueToolList(*scope, ct.Issues, false, 1, list.NextCursor)
	if err != nil || len(next.Issues) != 1 || next.Issues[0].ID == list.NextCursor {
		t.Fatalf("discovery cursor cannot continue: %+v err=%v", next, err)
	}
	after, _ := json.Marshal(ct.Issues)
	if string(before) != string(after) || a.selectedContact() != other || len(a.issueToolLedger.Operations) != 0 {
		t.Fatal("discovery mutated issue facts, selection, or operations")
	}
}

func TestIssueToolScopeWireIsOpaqueToken(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "token-session", "token-turn")
	if err != nil {
		t.Fatal(err)
	}
	input, ok := issueToolScopeInput(scope)
	if !ok || input.Text != scope.Ref || strings.Contains(input.Text, "{") {
		t.Fatalf("model-facing scope = %+v scope=%q", input, scope.Ref)
	}
}

func TestIssueToolLegacyJSONScopeIsRejected(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "legacy-json-session", "legacy-json-turn")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	legacyJSON, _ := json.Marshal(scope)
	args, _ := json.Marshal(map[string]any{
		"scope": string(legacyJSON), "action": issueToolActionCreate, "goal": "legacy scope",
	})
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || !response.IsError {
		t.Fatalf("legacy JSON scope was accepted: %+v err=%v", response, err)
	}
	if len(ct.Issues) != 0 {
		t.Fatalf("legacy JSON scope created issue: %+v", ct.Issues)
	}
}

func TestIssueToolContextWireBudgetIncludesInternalScope(t *testing.T) {
	userText := strings.Repeat("x", taskContextMaxBytes-64)
	inputs := taskContextInputs([]taskContextItem{{ID: "u", Name: "user", Text: userText, Revision: 1}})
	scope := pi.ContextInput{Name: issueToolScopeContextName, Text: "scope-budget"}
	inputs = append(inputs, scope)
	if err := validateRuntimeContextInputs(inputs); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("wire budget error = %v", err)
	}
}

func TestIssueToolAnswerBeforeWorkTerminalWaitsForRealTerminal(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "answer before terminal", "requirement")
	if err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{
		ID: "execution-waiting", SessionID: "work-session", TaskID: "work-task",
		Status: issueExecutionRunning, RequirementsRevision: issue.RequirementsRevision,
		WaitRequested: true,
	}
	issue.Executions = []*issueExecution{execution}
	issue.WorkSessionID = "work-session"
	issue.Work.ActiveTaskID = "work-task"
	issue.Work.SendState = sendAccepted
	question := &issueQuestion{
		Ref: "question-before-terminal", Text: "Need input", State: issueQuestionOpen,
		RequirementsRevision: issue.RequirementsRevision, SourceTaskID: "work-task", SourceScopeRef: "work-scope",
		CreatedAt: nowMillis(),
	}
	issue.Questions = []*issueQuestion{question}
	issue.Status = issueStatusWaitingUser
	issue.Supplements = newIssueSupplementLedger()
	issue.Supplements.AutoContinue = false
	ct.SessionID = "conversation-answer"
	scope, err := a.issueToolNewScope(ct.ID, "conversation-answer", "answer-turn")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "answer before terminal", "question_ref": question.Ref,
	})})
	if err != nil || response.IsError {
		t.Fatalf("answer before terminal = %+v err=%v", response, err)
	}
	if question.State != issueQuestionAnswered || issue.Status != issueStatusInProgress || !issue.Supplements.AutoContinue {
		t.Fatalf("answer-before-terminal state = question=%+v status=%q auto=%v", question, issue.Status, issue.Supplements.AutoContinue)
	}
	if len(issue.Executions) != 1 {
		t.Fatalf("answer before terminal dispatched early: %d", len(issue.Executions))
	}
	// A later real terminal must not turn this answer into an already-completed work.
	a.applyIssueTerminalLifecycle(issue, "work-task", "interrupted")
	if issue.Status != issueStatusInProgress || len(issue.Executions) != 1 {
		t.Fatalf("answer-before-terminal terminal state = status=%q executions=%d", issue.Status, len(issue.Executions))
	}
	// A user Stop after the answer closes automatic continuation intent.
	issue.Work.StopRequestedTasks = map[string]bool{"work-task": true}
	issue.Supplements.AutoContinue = false
	a.applyIssueTerminalLifecycle(issue, "work-task", "interrupted")
	if issue.Status != issueStatusInProgress || issue.Supplements.AutoContinue {
		t.Fatalf("stop-after-answer state = status=%q auto=%v", issue.Status, issue.Supplements.AutoContinue)
	}
}

func TestIssueToolWorkScopeRejectsConversationAndActionTools(t *testing.T) {
	a, store := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "work tool boundary", "")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := a.issueToolNewWorkScope(ct.ID, issue.ID, newIssueExecutionID(), filepath.Join(t.TempDir(), "scope-fixture.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	beforeIssue, err := json.Marshal(issue)
	if err != nil {
		t.Fatal(err)
	}
	beforeStore, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{issueToolActionRun, issueToolActionCreate} {
		response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, action, map[string]any{
			"issue_id": issue.ID, "goal": "unauthorized issue",
		})})
		if err != nil || !response.IsError {
			t.Fatalf("work scope %s bypass = %+v err=%v", action, response, err)
		}
		var toolErr IssueToolError
		if err := json.Unmarshal(issueToolResponseJSON(t, response), &toolErr); err != nil {
			t.Fatal(err)
		}
		if toolErr.Code != "scope_denied" {
			t.Fatalf("work scope %s denial = %+v", action, toolErr)
		}
		afterIssue, err := json.Marshal(issue)
		if err != nil {
			t.Fatal(err)
		}
		if len(a.issueToolLedger.Operations) != 0 || len(ct.Issues) != 1 || ct.Issues[0] != issue ||
			len(issue.Executions) != 0 || !bytes.Equal(afterIssue, beforeIssue) {
			t.Fatalf("work scope %s changed issue or operations: issue=%+v operations=%+v", action, issue, a.issueToolLedger.Operations)
		}
		afterStore, err := os.ReadFile(store.path)
		if err != nil || !bytes.Equal(afterStore, beforeStore) {
			t.Fatalf("work scope %s changed saved bytes: %v", action, err)
		}
	}
}

func TestIssueToolSubmissionCallbackParticipatesInShutdownDrain(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	quit := false
	a.requestQuit = func() { quit = true }
	a.submissionAsyncStarted()
	a.beginShutdown()
	if a.quitReady || quit {
		t.Fatal("shutdown completed while a work release callback was in flight")
	}
	a.submissionAsyncFinished()
	a.finishShutdown()
	if !a.quitReady || !quit {
		t.Fatal("shutdown did not complete after the final release callback")
	}
}

func TestIssueToolQueryResponsesRespectByteBudgetAndUTF8(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	longText := strings.Repeat("中文 requirement ", 2000)
	var firstIssueID string
	for i := 0; i < 60; i++ {
		issue, err := a.createIssueFor(ct, fmt.Sprintf("goal-%02d-%s", i, longText), longText)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstIssueID = issue.ID
		}
	}
	issue := ct.issueByID(firstIssueID)
	for i := 0; i < 32; i++ {
		issue.Requirements = append(issue.Requirements, &issueRequirement{
			ID: fmt.Sprintf("requirement-%02d", i), Text: fmt.Sprintf("%02d %s", i, longText), State: requirementUnverified,
		})
	}
	issue.RequirementsRevision = 9
	scope, err := a.issueToolNewScope(ct.ID, "query-budget-session", "query-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 10; page++ {
		args := map[string]any{"scope": scope.Ref, "limit": 50}
		if cursor != "" {
			args["cursor"] = cursor
		}
		listArgs, _ := json.Marshal(args)
		listResponse, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_list", Arguments: listArgs})
		if err != nil || listResponse.IsError {
			t.Fatalf("budget list page %d = %+v err=%v", page, listResponse, err)
		}
		rawList, _ := json.Marshal(issueToolRPCResponse{JSONRPC: "2.0", ID: json.RawMessage(`"budget-probe"`), Result: listResponse})
		if len(rawList) > issueToolQueryMaxBytes || !utf8.Valid(rawList) {
			t.Fatalf("list MCP response bytes=%d validUTF8=%v", len(rawList), utf8.Valid(rawList))
		}
		var list IssueToolIssueListResult
		if err := json.Unmarshal(issueToolResponseJSON(t, listResponse), &list); err != nil {
			t.Fatal(err)
		}
		for _, summary := range list.Issues {
			if seen[summary.ID] {
				t.Fatalf("list cursor repeated issue %q on page %d", summary.ID, page)
			}
			seen[summary.ID] = true
		}
		if !list.HasMore {
			break
		}
		if list.NextCursor == "" {
			t.Fatalf("list page %d has_more without cursor", page)
		}
		cursor = list.NextCursor
	}
	if len(seen) != 60 {
		t.Fatalf("bounded paging saw %d issues, want 60", len(seen))
	}
	getArgs, _ := json.Marshal(map[string]any{"scope": scope.Ref, "issue_id": firstIssueID})
	getResponse, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_get", Arguments: getArgs})
	if err != nil || getResponse.IsError {
		t.Fatalf("budget get = %+v err=%v", getResponse, err)
	}
	rawGet, _ := json.Marshal(issueToolRPCResponse{JSONRPC: "2.0", ID: json.RawMessage(`"budget-probe"`), Result: getResponse})
	if len(rawGet) > issueToolQueryMaxBytes || !utf8.Valid(rawGet) {
		t.Fatalf("detail MCP response bytes=%d validUTF8=%v", len(rawGet), utf8.Valid(rawGet))
	}
	var detail IssueToolIssueDetail
	if err := json.Unmarshal(issueToolResponseJSON(t, getResponse), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Issue.ID != firstIssueID || detail.Issue.RequirementsRevision != 9 || len(detail.Executions) > 20 ||
		detail.RequirementCount != 33 || len(detail.Requirements) >= 33 || !detail.RequirementsHasMore {
		t.Fatalf("bounded detail identity/range = %+v", detail)
	}
}

func TestIssueToolDetailBudgetShrinksLongExcerptAndPreservesIdentity(t *testing.T) {
	long := strings.Repeat("中文\\\"\\n\\t", 900)
	detail := &IssueToolIssueDetail{
		Issue: IssueToolIssueSummary{
			ID: "issue-budget-identity", Goal: long, Status: issueStatusWaitingUser,
			RequirementsRevision: 7, QuestionRef: "question-budget", Question: long,
			QuestionRevision: 7,
		},
		Requirements:      []IssueToolRequirementView{{ID: "req-1", Text: long, Evidence: long}},
		ExecutionCount:    1,
		ResultExecutionID: "execution-budget",
		ResultExcerpt:     long + long,
	}
	bounded := boundIssueToolDetail(detail)
	responseBytes, _ := json.Marshal(issueToolRPCResponse{JSONRPC: "2.0", ID: json.RawMessage(`"budget"`), Result: mustIssueToolDetailForward(t, bounded)})
	if len(responseBytes) > issueToolQueryMaxBytes || !utf8.Valid(responseBytes) {
		t.Fatalf("long detail MCP bytes=%d validUTF8=%v", len(responseBytes), utf8.Valid(responseBytes))
	}
	if bounded.Issue.ID != "issue-budget-identity" || bounded.Issue.Status != issueStatusWaitingUser ||
		bounded.Issue.QuestionRef != "question-budget" || bounded.Issue.RequirementsRevision != 7 ||
		bounded.ResultExecutionID != "execution-budget" || !bounded.ResultExcerptHasMore {
		t.Fatalf("bounded identity/excerpt = %+v", bounded)
	}
}

func mustIssueToolDetailForward(t *testing.T, detail *IssueToolIssueDetail) issueToolForwardResponse {
	t.Helper()
	response, err := issueToolJSONResponse(detail, false)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestIssueToolRevisionValidationIsActionableAndSideEffectFree(t *testing.T) {
	a, store := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "revision target", "requirement")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := a.issueToolNewScope(ct.ID, "conversation-revision", "turn-revision")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	beforeStore, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	beforeIssue, err := json.Marshal(issue)
	if err != nil {
		t.Fatal(err)
	}
	opsBefore := len(a.issueToolLedger.Operations)
	cases := []struct {
		name   string
		action string
		extra  map[string]any
		code   string
	}{
		{"update-missing", issueToolActionUpdate, map[string]any{"issue_id": issue.ID, "goal": "new goal"}, "invalid_action"},
		{"update-zero", issueToolActionUpdate, map[string]any{"issue_id": issue.ID, "goal": "new goal", "expected_revision": 0}, "invalid_action"},
		{"update-negative", issueToolActionUpdate, map[string]any{"issue_id": issue.ID, "goal": "new goal", "expected_revision": -1}, "invalid_action"},
		{"update-null", issueToolActionUpdate, map[string]any{"issue_id": issue.ID, "goal": "new goal", "expected_revision": nil}, "invalid_action"},
		{"update-string", issueToolActionUpdate, map[string]any{"issue_id": issue.ID, "goal": "new goal", "expected_revision": "1"}, "invalid_arguments"},
		{"update-overflow", issueToolActionUpdate, map[string]any{"issue_id": issue.ID, "goal": "new goal", "expected_revision": 1e100}, "invalid_arguments"},
		{"wait-missing", issueToolActionWait, map[string]any{"issue_id": issue.ID, "text": "question"}, "invalid_action"},
		{"wait-zero", issueToolActionWait, map[string]any{"issue_id": issue.ID, "text": "question", "expected_revision": 0}, "invalid_action"},
		{"wait-negative", issueToolActionWait, map[string]any{"issue_id": issue.ID, "text": "question", "expected_revision": -1}, "invalid_action"},
		{"wait-null", issueToolActionWait, map[string]any{"issue_id": issue.ID, "text": "question", "expected_revision": nil}, "invalid_action"},
		{"wait-float", issueToolActionWait, map[string]any{"issue_id": issue.ID, "text": "question", "expected_revision": 1.5}, "invalid_arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{
				Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, tc.action, tc.extra),
			})
			if err != nil || !response.IsError {
				t.Fatalf("response = %+v err=%v", response, err)
			}
			var toolErr IssueToolError
			if err := json.Unmarshal(issueToolResponseJSON(t, response), &toolErr); err != nil {
				t.Fatal(err)
			}
			if toolErr.Code != tc.code {
				t.Fatalf("code = %q, want %q; error=%s", toolErr.Code, tc.code, issueToolResponseJSON(t, response))
			}
			for _, want := range []string{"expected_revision", "positive integer", "requirements_revision", "create receipt", "issue_get"} {
				if !strings.Contains(toolErr.Message, want) {
					t.Fatalf("message %q missing %q", toolErr.Message, want)
				}
			}
			if after, err := os.ReadFile(store.path); err != nil || string(after) != string(beforeStore) {
				t.Fatalf("rejected revision changed store: err=%v", err)
			}
			afterIssue, err := json.Marshal(issue)
			if err != nil || string(afterIssue) != string(beforeIssue) {
				t.Fatalf("rejected revision changed issue: err=%v", err)
			}
			if len(a.issueToolLedger.Operations) != opsBefore || len(issue.Questions) != 0 || len(issue.Supplements.Items) != 0 || len(issue.Executions) != 0 {
				t.Fatalf("rejected revision side effects: ops=%d questions=%d supplements=%d executions=%d",
					len(a.issueToolLedger.Operations), len(issue.Questions), len(issue.Supplements.Items), len(issue.Executions))
			}
		})
	}
}

func TestIssueToolCreateThenUnstartedWaitAndAnswerStayExplicit(t *testing.T) {
	a, store := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-unstarted", "turn-unstarted")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	created, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{
		Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{
			"goal": "wait before run", "requirements": []string{"ask before executing"},
		}),
	})
	if err != nil || created.IsError {
		t.Fatalf("create = %+v err=%v", created, err)
	}
	var createReceipt IssueToolReceipt
	if err := json.Unmarshal(issueToolResponseJSON(t, created), &createReceipt); err != nil {
		t.Fatal(err)
	}
	if createReceipt.IssueID == "" || createReceipt.RequirementsRevision == nil || *createReceipt.RequirementsRevision < 1 {
		t.Fatalf("create receipt = %+v", createReceipt)
	}
	issue := ct.issueByID(createReceipt.IssueID)
	if issue == nil || issue.Status != issueStatusNotStarted || len(issue.Executions) != 0 {
		t.Fatalf("created issue = %+v", issue)
	}
	waitArgs := issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": *createReceipt.RequirementsRevision, "text": "Which method should I use?",
	})
	wait, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: waitArgs})
	if err != nil || wait.IsError {
		t.Fatalf("not-started wait = %+v err=%v", wait, err)
	}
	var waitReceipt IssueToolReceipt
	if err := json.Unmarshal(issueToolResponseJSON(t, wait), &waitReceipt); err != nil {
		t.Fatal(err)
	}
	if waitReceipt.QuestionRef == "" || issue.Status != issueStatusWaitingUser || issue.openQuestion() == nil || len(issue.Executions) != 0 || issue.Supplements.AutoContinue {
		t.Fatalf("not-started wait state = receipt=%+v issue=%+v", waitReceipt, issue)
	}
	questionRef := waitReceipt.QuestionRef
	waitReloaded, err := openContactStore(store.path, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	waitReloadedContacts := contactsFromStore(waitReloaded)
	if len(waitReloadedContacts) != 1 || len(waitReloadedContacts[0].Issues) != 1 {
		t.Fatalf("reloaded waiting issue = %+v", waitReloadedContacts)
	}
	waitReloadedIssue := waitReloadedContacts[0].Issues[0]
	if waitReloadedIssue.Status != issueStatusWaitingUser || len(waitReloadedIssue.Executions) != 0 ||
		waitReloadedIssue.openQuestion() == nil || waitReloadedIssue.openQuestion().Ref != questionRef {
		t.Fatalf("reloaded waiting state = %+v", waitReloadedIssue)
	}
	duplicateWait, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: waitArgs})
	if err != nil || duplicateWait.IsError || len(issue.Questions) != 1 {
		t.Fatalf("duplicate not-started wait = %+v err=%v questions=%d", duplicateWait, err, len(issue.Questions))
	}
	staleScope, err := a.issueToolNewScope(ct.ID, "conversation-unstarted-stale", "turn-unstarted-stale")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, staleScope)
	answerArgs := issueToolActionArgs(t, *staleScope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "Use SMS.", "question_ref": questionRef,
	})
	answer, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: answerArgs})
	if err != nil || answer.IsError {
		t.Fatalf("not-started answer = %+v err=%v", answer, err)
	}
	var answerReceipt IssueToolReceipt
	if err := json.Unmarshal(issueToolResponseJSON(t, answer), &answerReceipt); err != nil {
		t.Fatal(err)
	}
	question := issue.questionByRef(questionRef)
	if question == nil || question.State != issueQuestionAnswered || answerReceipt.SupplementID == "" ||
		issue.Status != issueStatusNotStarted || issue.Supplements.AutoContinue || len(issue.Executions) != 0 || issue.Supplements.pendingCount() != 1 {
		t.Fatalf("not-started answer state = receipt=%+v question=%+v issue=%+v", answerReceipt, question, issue)
	}
	duplicateAnswer, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: answerArgs})
	if err != nil || duplicateAnswer.IsError || issue.Supplements.pendingCount() != 1 {
		t.Fatalf("duplicate not-started answer = %+v err=%v pending=%d", duplicateAnswer, err, issue.Supplements.pendingCount())
	}
	updateScope, err := a.issueToolNewScope(ct.ID, "conversation-unstarted-update", "turn-unstarted-update")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, updateScope)
	currentRevision := issue.RequirementsRevision
	updated, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *updateScope, issueToolActionUpdate, map[string]any{
		"issue_id": issue.ID, "expected_revision": currentRevision, "goal": "updated after answer", "requirements": []string{"updated requirement"},
	})})
	if err != nil || updated.IsError {
		t.Fatalf("update after answer = %+v err=%v", updated, err)
	}
	var updateReceipt IssueToolReceipt
	if err := json.Unmarshal(issueToolResponseJSON(t, updated), &updateReceipt); err != nil {
		t.Fatal(err)
	}
	if updateReceipt.RequirementsRevision == nil || *updateReceipt.RequirementsRevision <= currentRevision || issue.RequirementsRevision != *updateReceipt.RequirementsRevision || issue.Goal != "updated after answer" {
		t.Fatalf("update receipt/state = receipt=%+v issue=%+v", updateReceipt, issue)
	}
	staleUpdateScope, err := a.issueToolNewScope(ct.ID, "conversation-unstarted-stale-update", "turn-unstarted-stale-update")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, staleUpdateScope)
	staleUpdate, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *staleUpdateScope, issueToolActionUpdate, map[string]any{
		"issue_id": issue.ID, "expected_revision": *createReceipt.RequirementsRevision, "goal": "stale update",
	})})
	if err != nil || !staleUpdate.IsError {
		t.Fatalf("stale update = %+v err=%v", staleUpdate, err)
	}
	var staleUpdateErr IssueToolError
	if err := json.Unmarshal(issueToolResponseJSON(t, staleUpdate), &staleUpdateErr); err != nil {
		t.Fatal(err)
	}
	if staleUpdateErr.Code != "revision_mismatch" || staleUpdateErr.CurrentRevision == nil || *staleUpdateErr.CurrentRevision != issue.RequirementsRevision ||
		issue.Goal != "updated after answer" || len(issue.Questions) != 1 || len(issue.Executions) != 0 || issue.Supplements.pendingCount() != 1 {
		t.Fatalf("stale update error/state = err=%+v issue=%+v", staleUpdateErr, issue)
	}
	staleWaitScope, err := a.issueToolNewScope(ct.ID, "conversation-unstarted-stale-wait", "turn-unstarted-stale-wait")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, staleWaitScope)
	staleWait, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *staleWaitScope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": *createReceipt.RequirementsRevision, "text": "stale wait question",
	})})
	if err != nil || !staleWait.IsError {
		t.Fatalf("stale wait = %+v err=%v", staleWait, err)
	}
	var staleWaitErr IssueToolError
	if err := json.Unmarshal(issueToolResponseJSON(t, staleWait), &staleWaitErr); err != nil {
		t.Fatal(err)
	}
	if staleWaitErr.Code != "revision_mismatch" || staleWaitErr.CurrentRevision == nil || *staleWaitErr.CurrentRevision != issue.RequirementsRevision ||
		issue.openQuestion() != nil || len(issue.Questions) != 1 || len(issue.Executions) != 0 || issue.Supplements.pendingCount() != 1 ||
		issue.questionByRef(questionRef) == nil || issue.questionByRef(questionRef).State != issueQuestionAnswered {
		t.Fatalf("stale wait error/state = err=%+v issue=%+v", staleWaitErr, issue)
	}
	reloaded, err := openContactStore(store.path, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	reloadedContacts := contactsFromStore(reloaded)
	if len(reloadedContacts) != 1 || len(reloadedContacts[0].Issues) != 1 {
		t.Fatalf("reloaded unstarted issue = %+v", reloadedContacts)
	}
	reloadedIssue := reloadedContacts[0].Issues[0]
	if reloadedIssue.Status != issueStatusNotStarted || len(reloadedIssue.Executions) != 0 || reloadedIssue.Supplements.AutoContinue ||
		reloadedIssue.Supplements.pendingCount() != 1 || reloadedIssue.questionByRef(questionRef) == nil ||
		reloadedIssue.questionByRef(questionRef).State != issueQuestionAnswered {
		t.Fatalf("reloaded unstarted state = %+v", reloadedIssue)
	}
}
