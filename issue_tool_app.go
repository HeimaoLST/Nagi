package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

const (
	issueToolScopeContextName     = "nagi.issue_tools.scope"
	issueToolIssueListContextName = "nagi.issue_tools.issues"
	issueToolWaitTimeout          = 25 * time.Second
	issueToolResultExcerptSize    = 4096
	issueToolQueryMaxBytes        = 16 * 1024
	issueToolEnvelopeReserve      = 512
	issueToolQueryTextLimit       = 512
)

func newIssueToolTurnID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "turn-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("turn-%d", time.Now().UnixNano())
}

func newIssueToolScopeRef() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "scope-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("scope-%d", time.Now().UnixNano())
}

func issueToolConversationDefinitions() []issueToolDefinition {
	all := issueToolDefinitions()
	out := make([]issueToolDefinition, 0, len(all))
	for _, definition := range all {
		if definition.Name != issueToolWorkToolName {
			out = append(out, definition)
		}
	}
	return out
}

func issueToolWorkDefinitions() []issueToolDefinition {
	for _, definition := range issueToolDefinitions() {
		if definition.Name == issueToolWorkToolName {
			return []issueToolDefinition{definition}
		}
	}
	return nil
}

const issueToolWorkToolName = "issue_wait"

func (a *appModel) issueToolNewWorkScope(contactID, issueID, executionID, sessionID string) (*IssueToolScope, error) {
	ct := a.contactByID(contactID)
	if ct == nil || issueForContact(ct, issueID) == nil {
		return nil, errors.New("issue not found for work scope")
	}
	scope := IssueToolScope{
		Ref: newIssueToolScopeRef(), Mode: issueToolModeWork,
		ContactID: contactID, RuntimeSessionID: sessionID,
		TurnID: executionID, AllowedIssueIDs: []string{issueID},
		WorkIssueID: issueID, WorkExecutionID: executionID,
		CreatedAt: nowMillis(),
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if a.issueToolScopes == nil {
		a.issueToolScopes = map[string]IssueToolScope{}
	}
	a.issueToolScopes[scope.Ref] = scope
	if err := a.persist(); err != nil {
		delete(a.issueToolScopes, scope.Ref)
		return nil, fmt.Errorf("save work issue scope: %w", err)
	}
	return &scope, nil
}

// issueToolNewScope issues one app-owned scope before a conversation task is
// sent. The caller appends the bounded issue list followed by the opaque scope.
func (a *appModel) issueToolNewScope(contactID, sessionID, turnID string) (*IssueToolScope, error) {
	ct := a.contactByID(contactID)
	if ct == nil {
		return nil, errors.New("contact not found for issue tool scope")
	}
	issues := a.issuesVisibleTo(ct)
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue != nil && issue.ID != "" {
			ids = append(ids, issue.ID)
		}
	}
	scope := IssueToolScope{
		Ref: newIssueToolScopeRef(), ContactID: contactID, RuntimeSessionID: sessionID,
		TurnID: turnID, AllowedIssueIDs: ids, AllowCreate: true,
		CreatedAt: nowMillis(),
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if a.issueToolScopes == nil {
		a.issueToolScopes = map[string]IssueToolScope{}
	}
	a.issueToolScopes[scope.Ref] = scope
	if err := a.persist(); err != nil {
		delete(a.issueToolScopes, scope.Ref)
		return nil, fmt.Errorf("save issue tool scope: %w", err)
	}
	return &scope, nil
}

// issueToolScopeToken is the only model-facing projection of an app-issued
// scope. The full object stays in the app map/store for authorization and
// source checking; the model receives only this opaque ref.
func issueToolScopeToken(scope *IssueToolScope) string {
	if scope == nil {
		return ""
	}
	return scope.Ref
}

func issueToolScopeInput(scope *IssueToolScope) (pi.ContextInput, bool) {
	token := issueToolScopeToken(scope)
	if token == "" {
		return pi.ContextInput{}, false
	}
	return pi.ContextInput{Name: issueToolScopeContextName, Text: token}, true
}

// The list helps discover existing issues. The issued scope controls authorization.
func (a *appModel) issueToolConversationIssueList(scope *IssueToolScope) (pi.ContextInput, error) {
	ct := a.contactByID(scope.ContactID)
	if ct == nil {
		return pi.ContextInput{}, errors.New("contact not found for issue list context")
	}
	list, err := issueToolList(*scope, a.issuesVisibleTo(ct), false, 50, "")
	if err != nil {
		return pi.ContextInput{}, err
	}
	data, err := json.Marshal(boundIssueToolList(list))
	if err != nil {
		return pi.ContextInput{}, err
	}
	return pi.ContextInput{Name: issueToolIssueListContextName, Text: string(data)}, nil
}

func (a *appModel) issueToolScopeByRef(ref string) (IssueToolScope, error) {
	if a.issueToolScopes == nil {
		return IssueToolScope{}, errors.New("issue tool scope is unknown")
	}
	scope, ok := a.issueToolScopes[ref]
	if !ok || scope.Validate() != nil {
		return IssueToolScope{}, errors.New("issue tool scope is unknown")
	}
	if a.contactByID(scope.ContactID) == nil {
		return IssueToolScope{}, errors.New("issue tool scope contact no longer exists")
	}
	return scope, nil
}

// issueToolScopeActive binds a scope to the one captured conversation turn
// that is currently submitting or executing. Scopes are not permanent
// authorization: a new turn, new session, archive, restart after terminal, or
// failed send cannot use an old scope for a new side effect.
func (a *appModel) issueToolScopeActive(captured IssueToolScope) bool {
	if a.shutdownStarted && a.pi == nil || captured.Validate() != nil || a.issueToolScopes == nil {
		return false
	}
	// The accepted task can bind after the handler captured its scope copy.
	// Read the latest binding from the app map, while requiring that the
	// immutable scope identity still matches the captured one.
	scope, ok := a.issueToolScopes[captured.Ref]
	if !ok || scope.Ref != captured.Ref || scope.ContactID != captured.ContactID ||
		scope.RuntimeSessionID != captured.RuntimeSessionID || scope.TurnID != captured.TurnID {
		return false
	}
	ct := a.contactByID(scope.ContactID)
	if ct == nil || ct.Archived || scope.Validate() != nil {
		return false
	}
	if scope.Mode == issueToolModeWork {
		issue := ct.issueByID(scope.WorkIssueID)
		if issue == nil || issue.Archived || issue.WorkSessionID != scope.RuntimeSessionID {
			return false
		}
		_, execution := ct.executionByID(scope.WorkExecutionID)
		if issue.Work.ActiveTaskID != "" {
			return scope.RuntimeTaskID == issue.Work.ActiveTaskID ||
				execution != nil && execution.ToolScopeRef == scope.Ref && execution.TaskID == issue.Work.ActiveTaskID
		}
		return issue.Work.SendState == sendSubmitting && execution != nil &&
			execution.ToolScopeRef == scope.Ref && execution.SessionID == scope.RuntimeSessionID
	}
	if ct.SessionID != scope.RuntimeSessionID {
		return false
	}
	if ct.ActiveTaskID != "" {
		return scope.RuntimeTaskID != "" && scope.RuntimeTaskID == ct.ActiveTaskID
	}
	return ct.SendState == sendSubmitting && ct.pendingSend != nil && ct.pendingSend.ToolScopeRef == scope.Ref
}

func (a *appModel) issueToolActionAllowed(scope IssueToolScope) error {
	if !a.issueToolScopeActive(scope) {
		return &IssueToolError{Code: "scope_inactive", Message: "the conversation turn or issue tool owner is no longer active"}
	}
	return nil
}

func (a *appModel) issueToolAttachPendingScope(contactID, ref string) error {
	ct := a.contactByID(contactID)
	if ct == nil || ct.pendingSend == nil {
		return errors.New("conversation send intent is no longer present")
	}
	previous := ct.pendingSend.ToolScopeRef
	ct.pendingSend.ToolScopeRef = ref
	if err := a.persist(); err != nil {
		ct.pendingSend.ToolScopeRef = previous
		return err
	}
	return nil
}

func (a *appModel) issueToolBindTask(ref, taskID string) {
	if ref == "" || taskID == "" {
		return
	}
	scope, ok := a.issueToolScopes[ref]
	if !ok {
		return
	}
	scope.RuntimeTaskID = taskID
	a.issueToolScopes[ref] = scope
	for _, ct := range a.contacts {
		for _, issue := range ct.Issues {
			for _, question := range issue.Questions {
				if question != nil && question.SourceScopeRef == ref && question.SourceTaskID == "" {
					question.SourceTaskID = taskID
				}
			}
		}
	}
}

// issueToolReserveForTurn performs existing-operation lookup, active-turn
// validation and Reserve in one UI queue callback. A repeated old operation can
// still return its receipt after the scope expires, but it can never reserve a
// new side effect outside the captured turn.
func (a *appModel) issueToolReserveForTurn(scope IssueToolScope, request IssueToolActionRequest) (*IssueToolOperation, bool, bool, *IssueToolReceipt, error) {
	var operation *IssueToolOperation
	var created, existing bool
	var receipt *IssueToolReceipt
	var resultErr error
	a.runOnUI(func() {
		if a.issueToolLedger == nil {
			a.issueToolLedger = &IssueToolOperationLedger{}
		}
		payload, err := issueToolCanonicalPayload(request)
		if err != nil {
			resultErr = err
			return
		}
		slot := issueToolIntentSlot(scope, request)
		if found, foundOK, err := a.issueToolLedger.operationFor(slot, payload); foundOK {
			existing = true
			operation = found
			if err != nil {
				resultErr = err
				return
			}
			receipt = cloneIssueToolReceipt(found.Receipt)
			return
		}
		if !a.issueToolScopeActive(scope) {
			resultErr = &IssueToolError{Code: "scope_inactive", Message: "the conversation turn that issued this scope is no longer active"}
			return
		}
		operation, created, resultErr = a.issueToolReserve(scope, request)
		if !created && resultErr == nil {
			existing = true
			receipt = cloneIssueToolReceipt(operation.Receipt)
		}
	})
	return operation, created, existing, receipt, resultErr
}

func cloneIssueToolReceipt(receipt *IssueToolReceipt) *IssueToolReceipt {
	if receipt == nil {
		return nil
	}
	copy := *receipt
	copy.PiRun = clonePiActivityRun(receipt.PiRun)
	if receipt.RequirementsRevision != nil {
		value := *receipt.RequirementsRevision
		copy.RequirementsRevision = &value
	}
	if receipt.Error != nil {
		errorCopy := *receipt.Error
		errorCopy.Candidates = append([]string(nil), receipt.Error.Candidates...)
		if receipt.Error.CurrentRevision != nil {
			value := *receipt.Error.CurrentRevision
			errorCopy.CurrentRevision = &value
		}
		copy.Error = &errorCopy
	}
	return &copy
}

func issueToolErrorResponse(code, message string) (issueToolForwardResponse, error) {
	return issueToolDomainErrorResponse(&IssueToolError{Code: code, Message: message})
}

func issueToolDomainErrorResponse(toolErr *IssueToolError) (issueToolForwardResponse, error) {
	data, err := json.Marshal(toolErr)
	if err != nil {
		return issueToolForwardResponse{}, err
	}
	return issueToolForwardResponse{
		Content: []issueToolContent{{Type: "text", Text: string(data)}},
		IsError: true,
	}, nil
}

func issueToolConsumeError(err error) (issueToolForwardResponse, error) {
	if toolErr, ok := err.(*IssueToolError); ok {
		return issueToolDomainErrorResponse(toolErr)
	}
	return issueToolErrorResponse("operation_unknown", err.Error())
}

func issueToolJSONResponse(value any, isError bool) (issueToolForwardResponse, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return issueToolErrorResponse("encode_failed", "could not encode issue tool response")
	}
	return issueToolForwardResponse{
		Content: []issueToolContent{{Type: "text", Text: string(data)}},
		IsError: isError,
	}, nil
}

func (a *appModel) handleIssueToolForward(ctx context.Context, request issueToolForwardRequest) (issueToolForwardResponse, error) {
	switch request.Tool {
	case "bot_list":
		return a.issueToolListBots(ctx, request.Arguments)
	case "delegate_activity":
		return a.delegateActivity(request.Arguments)
	case "issue_list", "issue_get", "issue_action":
	default:
		return issueToolErrorResponse("unknown_tool", "unknown issue tool")
	}
	if request.Tool == "issue_list" {
		return a.issueToolHandleList(ctx, request.Arguments)
	}
	if request.Tool == "issue_get" {
		return a.issueToolHandleGet(ctx, request.Arguments)
	}
	return a.issueToolHandleAction(ctx, request.Arguments)
}

func issueToolArgumentString(raw json.RawMessage, field string) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", errors.New("tool arguments must be an object")
	}
	var value string
	if item, ok := fields[field]; ok {
		if err := json.Unmarshal(item, &value); err != nil {
			return "", fmt.Errorf("%s must be a string", field)
		}
	}
	return strings.TrimSpace(value), nil
}

func issueToolResponseBytes(value any) int {
	response, err := issueToolJSONResponse(value, false)
	if err != nil {
		return issueToolQueryMaxBytes + 1
	}
	data, err := json.Marshal(response)
	if err != nil {
		return issueToolQueryMaxBytes + 1
	}
	return len(data) + issueToolEnvelopeReserve
}

func boundIssueToolList(result IssueToolIssueListResult) IssueToolIssueListResult {
	for index := range result.Issues {
		result.Issues[index].Goal = truncateIssueToolText(result.Issues[index].Goal, issueToolQueryTextLimit)
		result.Issues[index].Question = truncateIssueToolText(result.Issues[index].Question, issueToolQueryTextLimit)
	}
	for len(result.Issues) > 0 && issueToolResponseBytes(result) > issueToolQueryMaxBytes {
		result.Issues = result.Issues[:len(result.Issues)-1]
		result.HasMore = true
	}
	if result.HasMore && len(result.Issues) > 0 {
		result.NextCursor = result.Issues[len(result.Issues)-1].ID
	} else if len(result.Issues) == 0 {
		result.HasMore = false
		result.NextCursor = ""
	}
	return result
}

func boundIssueToolDetail(detail *IssueToolIssueDetail) *IssueToolIssueDetail {
	if detail == nil {
		return nil
	}
	detail.Issue.Goal = truncateIssueToolText(detail.Issue.Goal, issueToolQueryTextLimit)
	detail.Issue.Question = truncateIssueToolText(detail.Issue.Question, issueToolQueryTextLimit)
	for index := range detail.Requirements {
		detail.Requirements[index].Text = truncateIssueToolText(detail.Requirements[index].Text, issueToolQueryTextLimit)
		detail.Requirements[index].Evidence = truncateIssueToolText(detail.Requirements[index].Evidence, issueToolQueryTextLimit)
	}
	for index := range detail.Executions {
		detail.Executions[index].Detail = truncateIssueToolText(detail.Executions[index].Detail, issueToolQueryTextLimit)
	}
	detail.ResultExcerptHasMore = utf8.RuneCountInString(detail.ResultExcerpt) > issueToolResultExcerptSize
	detail.ResultExcerpt = truncateIssueToolText(detail.ResultExcerpt, issueToolResultExcerptSize)
	detail.ResultUnavailable = truncateIssueToolText(detail.ResultUnavailable, issueToolQueryTextLimit)
	if detail.RequirementCount == 0 {
		detail.RequirementCount = len(detail.Requirements)
	}
	for len(detail.Requirements) > 0 && issueToolResponseBytes(detail) > issueToolQueryMaxBytes {
		detail.Requirements = detail.Requirements[:len(detail.Requirements)-1]
		detail.RequirementsHasMore = true
	}
	for len(detail.Executions) > 1 && issueToolResponseBytes(detail) > issueToolQueryMaxBytes {
		detail.Executions = detail.Executions[1:]
		detail.ExecutionsHasMore = true
	}
	for len(detail.ResultFiles) > 0 && issueToolResponseBytes(detail) > issueToolQueryMaxBytes {
		detail.ResultFiles = detail.ResultFiles[:len(detail.ResultFiles)-1]
		detail.ResultFilesHasMore = true
	}
	if detail.ResultExcerpt != "" && issueToolResponseBytes(detail) > issueToolQueryMaxBytes {
		original := detail.ResultExcerpt
		for _, limit := range []int{2048, 1024, 512, 256, 128} {
			detail.ResultExcerpt = truncateIssueToolText(original, limit)
			detail.ResultExcerptHasMore = true
			if issueToolResponseBytes(detail) <= issueToolQueryMaxBytes {
				break
			}
		}
		if issueToolResponseBytes(detail) > issueToolQueryMaxBytes {
			detail.ResultExcerpt = ""
			detail.ResultExcerptHasMore = true
		}
	}
	return detail
}

func (a *appModel) issueToolHandleList(_ context.Context, raw json.RawMessage) (issueToolForwardResponse, error) {
	scopeRef, err := issueToolArgumentString(raw, "scope")
	if err != nil {
		return issueToolErrorResponse("invalid_arguments", err.Error())
	}
	var args struct {
		IncludeArchived bool   `json:"include_archived"`
		Limit           int    `json:"limit"`
		Cursor          string `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return issueToolErrorResponse("invalid_arguments", "invalid issue_list arguments")
	}
	scopeRef = strings.TrimSpace(scopeRef)
	var result IssueToolIssueListResult
	var callErr error
	a.runOnUI(func() {
		scope, err := a.issueToolScopeByRef(scopeRef)
		if err != nil {
			callErr = err
			return
		}
		ct := a.contactByID(scope.ContactID)
		if ct == nil {
			callErr = errors.New("issue tool scope contact no longer exists")
			return
		}
		result, callErr = issueToolList(scope, a.issuesVisibleTo(ct), args.IncludeArchived, args.Limit, strings.TrimSpace(args.Cursor))
	})
	if callErr != nil {
		return issueToolConsumeError(callErr)
	}
	result = boundIssueToolList(result)
	return issueToolJSONResponse(result, false)
}

func (a *appModel) issueToolHandleGet(ctx context.Context, raw json.RawMessage) (issueToolForwardResponse, error) {
	scopeRef, err := issueToolArgumentString(raw, "scope")
	if err != nil {
		return issueToolErrorResponse("invalid_arguments", err.Error())
	}
	issueID, err := issueToolArgumentString(raw, "issue_id")
	if err != nil || issueID == "" {
		return issueToolErrorResponse("invalid_arguments", "issue_id is required")
	}
	executionID, err := issueToolArgumentString(raw, "execution_id")
	if err != nil {
		return issueToolErrorResponse("invalid_arguments", err.Error())
	}
	var detail *IssueToolIssueDetail
	var scope IssueToolScope
	var piHost *piAppHost
	var piRun *piActivityRun
	var binding *piActivityBinding
	var status string
	var callErr error
	a.runOnUI(func() {
		if a.shutdownStarted {
			callErr = errors.New("app is shutting down")
			return
		}
		var err error
		scope, err = a.issueToolScopeByRef(scopeRef)
		if err != nil {
			callErr = err
			return
		}
		ct, issue := a.issueToolTarget(scope, issueID)
		if issue == nil {
			callErr = &IssueToolError{Code: "not_found", Message: "issue was not found"}
			return
		}
		detail, callErr = issueToolGet(scope, []*productIssue{issue}, issueID)
		if callErr != nil {
			return
		}
		execution := latestIssueExecution(issue)
		if executionID != "" {
			owner, selected := ct.executionByID(executionID)
			if owner != issue {
				callErr = &IssueToolError{Code: "not_found", Message: "execution was not found in this issue"}
				return
			}
			execution = selected
		}
		if execution == nil {
			return
		}
		detail.ResultExecutionID = execution.ID
		status = execution.Status
		if execution.PiRun != nil && isPiActivityIssue(issue) {
			piHost, piRun = a.pi, clonePiActivityRun(execution.PiRun)
			if canReadPiIssueResult(ct, issue, execution) {
				binding = clonePiActivityBinding(issue.PiBinding)
			}
		}
	})
	if callErr != nil {
		return issueToolConsumeError(callErr)
	}
	if detail == nil {
		return issueToolErrorResponse("not_found", "issue was not found")
	}
	if piRun == nil {
		if detail.ResultExecutionID != "" {
			detail.ResultUnavailable = "The execution has no recorded Pi run."
		}
		return issueToolJSONResponse(boundIssueToolDetail(detail), false)
	}
	if piHost == nil {
		detail.ResultUnavailable = "The Pi connection is unavailable."
		return issueToolJSONResponse(boundIssueToolDetail(detail), false)
	}
	current := func() bool {
		valid := false
		a.runOnUI(func() {
			ct, issue := a.issueToolTarget(scope, issueID)
			if issue == nil || a.pi != piHost || a.connectionGeneration != piHost.generation {
				return
			}
			owner, execution := ct.executionByID(detail.ResultExecutionID)
			if owner != issue || !isPiActivityIssue(owner) ||
				execution.Status != status || !samePiActivityRun(execution.PiRun, piRun) {
				return
			}
			valid = binding == nil || canReadPiIssueResult(ct, owner, execution) && reflect.DeepEqual(owner.PiBinding, binding)
		})
		return valid
	}
	if binding != nil {
		readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		result, err := piHost.readPiIssueResult(readCtx, *binding, *piRun, detail.ResultExecutionID, status)
		cancel()
		switch {
		case !current():
			detail.ResultUnavailable = "The activity owner or connection changed while reading the result."
		case err != nil:
			detail.ResultUnavailable = "Pi activity result history is unavailable."
		default:
			detail.ResultExcerpt = result.response
			detail.ResultFileCount = len(result.files)
			detail.ResultFiles = make([]IssueToolResultFileView, 0, len(result.files))
			for _, file := range result.files {
				detail.ResultFiles = append(detail.ResultFiles, IssueToolResultFileView{
					ExecutionID: file.ExecutionID, CallID: file.CallID, ToolName: file.ToolName, Path: file.Path,
				})
			}
		}
		return issueToolJSONResponse(boundIssueToolDetail(detail), false)
	}
	detail.ResultUnavailable = "This execution has no confirmed result."
	ready, readyErr := piHost.client.Ready(ctx)
	if readyErr != nil {
		detail.ResultUnavailable = "Pi activity progress is unavailable."
		return issueToolJSONResponse(boundIssueToolDetail(detail), false)
	}
	if ready.WorkerEpoch != piRun.WorkerEpoch {
		detail.ResultUnavailable = "The recorded activity belongs to a previous Pi worker."
		return issueToolJSONResponse(boundIssueToolDetail(detail), false)
	}
	inspection, inspectErr := piHost.client.Inspect(ctx, piRun.ContextID)
	switch {
	case !current():
		detail.ResultUnavailable = "The activity changed while progress was inspected."
	case inspectErr != nil:
		detail.ResultUnavailable = "Pi activity progress is unavailable."
	case inspection.ContextID != piRun.ContextID || inspection.SessionRef != piRun.SessionRef:
		detail.ResultUnavailable = "Pi returned a different activity context."
	default:
		progress := &IssueToolPiProgress{ContextID: piRun.ContextID, RunID: piRun.RunID}
		if inspection.ActiveRun != nil && inspection.ActiveRun.RunID == piRun.RunID {
			progress.Active, progress.CancelRequested = true, inspection.ActiveRun.CancelRequested
		}
		detail.PiProgress = progress
	}
	return issueToolJSONResponse(boundIssueToolDetail(detail), false)
}

func truncateIssueToolText(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "\n…[truncated]"
}

func issueToolRevisionPointer(value int) *int { return &value }

func (a *appModel) issueToolReserve(scope IssueToolScope, request IssueToolActionRequest) (*IssueToolOperation, bool, error) {
	if a.issueToolLedger == nil {
		a.issueToolLedger = &IssueToolOperationLedger{}
	}
	return a.issueToolLedger.Reserve(scope, request, a.persist)
}

func (a *appModel) issueToolSetReceipt(operation *IssueToolOperation, receipt *IssueToolReceipt) error {
	if operation == nil || a.issueToolLedger == nil {
		return errors.New("issue tool operation is unavailable")
	}
	return a.issueToolLedger.SetReceipt(operation.ID, receipt, a.persist)
}

func (a *appModel) issueToolAddScopeIssue(ref, issueID string) {
	scope, ok := a.issueToolScopes[ref]
	if !ok || issueID == "" {
		return
	}
	for _, existing := range scope.AllowedIssueIDs {
		if existing == issueID {
			return
		}
	}
	scope.AllowedIssueIDs = append(scope.AllowedIssueIDs, issueID)
	a.issueToolScopes[ref] = scope
}

func (a *appModel) issueToolNotify(operationID string, receipt IssueToolReceipt) {
	if operationID == "" || a.issueToolWaiters == nil {
		return
	}
	if waiter, ok := a.issueToolWaiters[operationID]; ok {
		select {
		case waiter <- receipt:
		default:
		}
	}
}

func (a *appModel) issueToolCompleteExecution(execution *issueExecution, accepted bool, detail string) {
	if execution == nil || execution.ToolOperationID == "" || a.issueToolLedger == nil {
		return
	}
	operation := a.issueToolLedger.operationByID(execution.ToolOperationID)
	if operation == nil {
		return
	}
	issueID := operation.TargetIssueID
	if issueID == "" {
		for _, ct := range a.contacts {
			if owner, _ := ct.executionByID(execution.ID); owner != nil {
				issueID = owner.ID
				break
			}
		}
	}
	receipt := &IssueToolReceipt{
		State: issueToolReceiptUnknown, IssueID: issueID, ExecutionID: execution.ID,
		SessionID: execution.SessionID, TaskID: execution.TaskID,
		PiRun: clonePiActivityRun(execution.PiRun),
	}
	if accepted {
		receipt.State = issueToolReceiptAccepted
	}
	if detail != "" {
		receipt.Error = &IssueToolError{Code: "operation_unknown", Message: detail}
	}
	if err := a.issueToolSetReceipt(operation, receipt); err != nil {
		receipt = &IssueToolReceipt{
			State: issueToolReceiptUnknown, IssueID: issueID, ExecutionID: execution.ID,
			SessionID: execution.SessionID, TaskID: execution.TaskID,
			PiRun: clonePiActivityRun(execution.PiRun),
			Error: &IssueToolError{Code: "operation_unknown", Message: "task acceptance was not saved with its operation receipt; reload before retrying"},
		}
	}
	a.issueToolNotify(operation.ID, *cloneIssueToolReceipt(receipt))
}

func (a *appModel) issueToolHandleAction(_ context.Context, raw json.RawMessage) (issueToolForwardResponse, error) {
	var request IssueToolActionRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		message := "invalid issue_action arguments"
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field == "expected_revision" {
			message += ": " + issueToolExpectedRevisionCorrection
		}
		return issueToolErrorResponse("invalid_arguments", message)
	}
	if request.AssigneeID != "" || len(request.Files) != 0 {
		return issueToolErrorResponse("invalid_arguments", "use delegate_activity to pass work to another Bot")
	}
	return a.applyIssueToolAction(request)
}

func (a *appModel) applyIssueToolAction(request IssueToolActionRequest) (issueToolForwardResponse, error) {
	request.ScopeRef = strings.TrimSpace(request.ScopeRef)
	var scope IssueToolScope
	var scopeErr error
	a.runOnUI(func() { scope, scopeErr = a.issueToolScopeByRef(request.ScopeRef) })
	if scopeErr != nil {
		return issueToolErrorResponse("scope_denied", scopeErr.Error())
	}
	if scope.Mode == issueToolModeWork {
		return issueToolErrorResponse("scope_denied", "work scope allows only issue_wait")
	}
	if err := request.Validate(scope); err != nil {
		if toolErr, ok := err.(*IssueToolError); ok {
			return issueToolJSONResponse(toolErr, true)
		}
		return issueToolErrorResponse("invalid_action", err.Error())
	}
	operation, created, existing, receipt, reserveErr := a.issueToolReserveForTurn(scope, request)
	if reserveErr != nil {
		return issueToolConsumeError(reserveErr)
	}
	if existing {
		if receipt == nil {
			return issueToolErrorResponse("operation_unknown", "this turn already recorded an operation without a receipt; not replaying it")
		}
		return issueToolJSONResponse(receipt, receipt.State == issueToolReceiptUnknown)
	}
	if !created {
		return issueToolErrorResponse("operation_unknown", "operation was not reserved; no side effect was performed")
	}
	var response issueToolForwardResponse
	switch request.Action {
	case issueToolActionCreate:
		response, _ = a.issueToolCreate(scope, request, operation)
	case issueToolActionUpdate:
		response, _ = a.issueToolUpdate(scope, request, operation)
	case issueToolActionAddSupplement:
		response, _ = a.issueToolSupplement(scope, request, operation)
	case issueToolActionWait:
		response, _ = a.issueToolWait(scope, request, operation)
	case issueToolActionResume:
		response, _ = a.issueToolResume(scope, request, operation)
	case issueToolActionRun:
		response, _ = a.issueToolRun(scope, request, operation)
	case issueToolActionStop:
		response, _ = a.issueToolStop(scope, request, operation)
	default:
		return issueToolErrorResponse("invalid_action", "unsupported issue action")
	}
	if response.IsError {
		toolErr := issueToolResponseError(response)
		if toolErr == nil {
			toolErr = &IssueToolError{Code: "operation_unknown", Message: "issue action failed without a receipt; no automatic retry"}
		}
		a.runOnUI(func() {
			if operation.Receipt != nil {
				return
			}
			receipt := &IssueToolReceipt{
				State: issueToolReceiptUnknown, IssueID: operation.TargetIssueID, Error: toolErr,
			}
			if err := a.issueToolSetReceipt(operation, receipt); err == nil {
				a.issueToolNotify(operation.ID, *cloneIssueToolReceipt(receipt))
			}
		})
	}
	return response, nil
}

func issueToolResponseError(response issueToolForwardResponse) *IssueToolError {
	if len(response.Content) != 1 {
		return nil
	}
	var toolErr IssueToolError
	if err := json.Unmarshal([]byte(response.Content[0].Text), &toolErr); err != nil {
		return nil
	}
	return &toolErr
}

// issueToolCreateContext returns only the user context snapshot belonging to
// the exact conversation scope/turn that issued create. It prefers the
// pre-accept pendingSend snapshot and otherwise uses the live msgUser record
// with the scope's real Runtime task. It never reads the current draft, scans
// chat history, or treats the scope token as user material.
func (a *appModel) issueToolTurnContext(ct *contact, scope IssueToolScope) []taskContextItem {
	if ct == nil || ct.ID != scope.ContactID {
		return nil
	}
	sessionID := scope.RuntimeSessionID
	if sessionID == "" || ct.SessionID != sessionID {
		return nil
	}
	if ct.pendingSend != nil && ct.pendingSend.ToolScopeRef == scope.Ref {
		return cloneTaskContextItems(ct.pendingSend.Context)
	}
	if scope.RuntimeTaskID == "" {
		return nil
	}
	for index := len(ct.Messages) - 1; index >= 0; index-- {
		message := ct.Messages[index]
		if message != nil && message.Kind == msgUser && message.TaskID == scope.RuntimeTaskID {
			return cloneTaskContextItems(message.Context)
		}
	}
	return nil
}

func mergeIssueToolTurnContext(existing, captured []taskContextItem) []taskContextItem {
	merged := cloneTaskContextItems(existing)
	index := make(map[string]taskContextItem, len(merged))
	for _, item := range merged {
		index[item.ID+"\x00"+strconv.FormatUint(item.Revision, 10)] = item
	}
	for _, item := range captured {
		key := item.ID + "\x00" + strconv.FormatUint(item.Revision, 10)
		if _, ok := index[key]; ok {
			continue
		}
		merged = append(merged, item)
		index[key] = item
	}
	return merged
}

func (a *appModel) issueToolCaptureTurnContext(ct *contact, scope IssueToolScope) ([]taskContextItem, error) {
	captured := a.issueToolTurnContext(ct, scope)
	if err := validateTaskContext(captured); err != nil {
		return nil, err
	}
	return captured, nil
}

func (a *appModel) issueToolCreate(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var callErr error
	var receipt *IssueToolReceipt
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct := a.contactByID(scope.ContactID)
		if ct == nil {
			callErr = errors.New("issue tool scope contact no longer exists")
			return
		}
		latestScope, latestErr := a.issueToolScopeByRef(scope.Ref)
		if latestErr != nil {
			callErr = latestErr
			return
		}
		recipient := ct
		var handoff *issueHandoff
		if request.AssigneeID != "" {
			recipient = a.contactByID(request.AssigneeID)
			if recipient == nil || recipient.Archived {
				callErr = &IssueToolError{Code: "recipient_unavailable", Message: "the requested Bot is unavailable"}
				return
			}
			handoff = &issueHandoff{
				SourceBotID: ct.ID, SourceSessionRef: latestScope.RuntimeSessionID, SourceTurnID: latestScope.TurnID,
				Files: request.Files,
			}
		}
		capturedContext := a.issueToolTurnContext(ct, latestScope)
		if err := validateTaskContext(capturedContext); err != nil {
			callErr = err
			return
		}
		_, callErr = a.createIssueForWithSave(recipient, request.Goal, strings.Join(request.Requirements, "\n"), func(created *productIssue) error {
			created.Handoff = handoff
			created.Context.items = capturedContext
			receipt = &IssueToolReceipt{
				State: issueToolReceiptSaved, IssueID: created.ID, IssueStatus: created.Status,
				RequirementsRevision: issueToolRevisionPointer(created.RequirementsRevision),
			}
			operation.Receipt = receipt
			a.issueToolAddScopeIssue(scope.Ref, created.ID)
			if err := a.persist(); err != nil {
				operation.Receipt = nil
				a.issueToolScopes[scope.Ref] = latestScope
				return err
			}
			receipt = cloneIssueToolReceipt(operation.Receipt)
			return nil
		})
	})
	if callErr != nil {
		return issueToolErrorResponse("saved_failed", callErr.Error())
	}
	return issueToolJSONResponse(receipt, false)
}

func (a *appModel) issueToolUpdate(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var callErr error
	var revision int
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct, issue := a.issueToolTarget(scope, request.IssueID)
		if issue == nil {
			callErr = errors.New("issue not found")
			return
		}
		if request.ExpectedRevision == nil || *request.ExpectedRevision != issue.RequirementsRevision {
			callErr = &IssueToolError{Code: "revision_mismatch", Message: "issue requirements changed; reload before updating", CurrentRevision: issueToolRevisionPointer(issue.RequirementsRevision)}
			return
		}
		if !a.editIssueFor(ct, issue.ID, request.Goal, strings.Join(request.Requirements, "\n")) {
			callErr = errors.New(valueOr(a.issueFormError, "issue update was rejected"))
			return
		}
		revision = issue.RequirementsRevision
		callErr = a.issueToolSetReceipt(operation, &IssueToolReceipt{
			State: issueToolReceiptSaved, IssueID: issue.ID, IssueStatus: issue.Status,
			RequirementsRevision: issueToolRevisionPointer(revision),
		})
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	var receipt *IssueToolReceipt
	a.runOnUI(func() { receipt = cloneIssueToolReceipt(operation.Receipt) })
	return issueToolJSONResponse(receipt, false)
}

func (a *appModel) issueToolSupplement(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	if strings.TrimSpace(request.QuestionRef) != "" {
		return a.issueToolAnswerQuestion(scope, request, operation)
	}
	var callErr error
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct, issue := a.issueToolTarget(scope, request.IssueID)
		if issue == nil {
			callErr = errors.New("issue not found")
			return
		}
		latestScope, latestErr := a.issueToolScopeByRef(scope.Ref)
		if latestErr != nil {
			callErr = latestErr
			return
		}
		captured, captureErr := a.issueToolCaptureTurnContext(a.contactByID(scope.ContactID), latestScope)
		if captureErr != nil {
			callErr = captureErr
			return
		}
		previousContext := issue.Context.snapshot()
		mergedContext := mergeIssueToolTurnContext(issue.Context.items, captured)
		contextChanged := len(mergedContext) != len(issue.Context.items)
		if contextChanged {
			if err := validateTaskContext(mergedContext); err != nil {
				callErr = err
				return
			}
		}
		if err := a.addIssueSupplementBefore(ct, request.IssueID, request.Text, func(*issueSupplement) {
			if contextChanged {
				issue.Context.items = mergedContext
			}
		}); err != nil {
			issue.Context.items = previousContext
			callErr = err
			return
		}
		callErr = a.issueToolSetReceipt(operation, &IssueToolReceipt{
			State: issueToolReceiptSaved, IssueID: request.IssueID,
			RequirementsRevision: issueToolRevisionPointer(issue.RequirementsRevision),
		})
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	var receipt *IssueToolReceipt
	a.runOnUI(func() { receipt = cloneIssueToolReceipt(operation.Receipt) })
	return issueToolJSONResponse(receipt, false)
}

func (a *appModel) issueToolWait(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var receipt *IssueToolReceipt
	var callErr error
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct := a.contactByID(scope.ContactID)
		issue := issueForContact(ct, request.IssueID)
		if issue == nil || issue.Archived || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
			callErr = errors.New("issue cannot wait in its current state")
			return
		}
		if request.ExpectedRevision == nil || *request.ExpectedRevision != issue.RequirementsRevision {
			callErr = &IssueToolError{Code: "revision_mismatch", Message: "issue requirements changed; reload before waiting", CurrentRevision: issueToolRevisionPointer(issue.RequirementsRevision)}
			return
		}
		if issue.Work.ActiveTaskID != "" || issue.Work.SendState != sendIdle || issueRunBlocked(issue) != "" {
			callErr = errors.New("issue has active or unresolved work; wait cannot be recorded")
			return
		}
		text := strings.TrimSpace(request.Text)
		supersedeRef := ""
		if open := issue.openQuestion(); open != nil {
			if open.RequirementsRevision == issue.RequirementsRevision && strings.TrimSpace(open.Text) == text {
				receipt = &IssueToolReceipt{
					State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: open.Ref,
					QuestionState: open.State, IssueStatus: issue.Status,
					RequirementsRevision: issueToolRevisionPointer(open.RequirementsRevision),
				}
				callErr = a.issueToolSetReceipt(operation, receipt)
				return
			}
			supersedeRef = open.Ref
		}
		question := &issueQuestion{
			Ref: newIssueQuestionRef(), Text: text,
			RequirementsRevision: issue.RequirementsRevision, SourceTaskID: scope.RuntimeTaskID,
			SourceScopeRef: scope.Ref, SourceTurnID: scope.TurnID,
			State: issueQuestionOpen, CreatedAt: nowMillis(),
		}
		receipt = &IssueToolReceipt{
			State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: question.Ref,
			QuestionState: issueQuestionOpen, IssueStatus: issueStatusWaitingUser,
			RequirementsRevision: issueToolRevisionPointer(question.RequirementsRevision),
		}
		if !a.markIssueWaitingForQuestion(ct, issue.ID, supersedeRef, question, operation, receipt) {
			callErr = errors.New("issue could not be marked waiting and saved")
			return
		}
		receipt = cloneIssueToolReceipt(receipt)
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	return issueToolJSONResponse(receipt, false)
}

func (a *appModel) issueToolResume(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var receipt *IssueToolReceipt
	var callErr error
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct, issue := a.issueToolTarget(scope, request.IssueID)
		if issue == nil {
			callErr = errors.New("issue not found")
			return
		}
		if !a.resumeIssueFor(ct, issue.ID) {
			callErr = errors.New(valueOr(a.issueActionError, "issue cannot resume in its current state"))
			return
		}
		receipt = &IssueToolReceipt{
			State: issueToolReceiptSaved, IssueID: issue.ID, IssueStatus: issue.Status,
			RequirementsRevision: issueToolRevisionPointer(issue.RequirementsRevision),
		}
		if question := issue.openQuestion(); question != nil {
			receipt.QuestionRef, receipt.QuestionState = question.Ref, question.State
		}
		callErr = a.issueToolSetReceipt(operation, receipt)
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	return issueToolJSONResponse(cloneIssueToolReceipt(receipt), false)
}

func (a *appModel) issueToolAnswerQuestion(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var receipt *IssueToolReceipt
	var callErr error
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct, issue := a.issueToolTarget(scope, request.IssueID)
		if issue == nil {
			callErr = errors.New("issue not found")
			return
		}
		latestScope, err := a.issueToolScopeByRef(scope.Ref)
		if err != nil {
			callErr = err
			return
		}
		receipt, callErr = a.answerIssueQuestionFor(ct, request.IssueID, request.QuestionRef, request.ExpectedRevision, request.Text, a.issueToolTurnContext(a.contactByID(scope.ContactID), latestScope), operation)
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	return issueToolJSONResponse(receipt, false)
}

func (a *appModel) answerIssueQuestionFor(ct *contact, issueID, questionRef string, expectedRevision *int, text string, capturedContext []taskContextItem, operation *IssueToolOperation) (*IssueToolReceipt, error) {
	if a.shutdownStarted || ct == nil || a.contactByID(ct.ID) != ct || ct.Archived {
		return nil, &IssueToolError{Code: "scope_inactive", Message: "the question owner is no longer active"}
	}
	issue := issueForContact(ct, issueID)
	if issue == nil {
		return nil, &IssueToolError{Code: "not_found", Message: "issue not found for this question"}
	}
	question := issue.questionByRef(questionRef)
	if question == nil {
		candidates := make([]string, 0)
		if open := issue.openQuestion(); open != nil {
			candidates = append(candidates, open.Ref)
		}
		return nil, &IssueToolError{Code: "question_not_found", Message: "question_ref is not attached to this issue", Candidates: candidates}
	}
	if question.State == issueQuestionAnswered {
		answer := issueSupplementLedgerFor(issue).itemByID(question.AnswerSupplementID)
		if answer == nil {
			return nil, &IssueToolError{Code: "operation_unknown", Message: "the saved answer supplement is missing; reload before revising"}
		}
		if strings.TrimSpace(text) != strings.TrimSpace(answer.Text) {
			return nil, &IssueToolError{Code: "question_answer_conflict", Message: "this question already has a different answer; reload before revising"}
		}
		receipt := &IssueToolReceipt{
			State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: question.Ref,
			QuestionState: question.State, IssueStatus: issue.Status,
			SupplementID:         question.AnswerSupplementID,
			RequirementsRevision: issueToolRevisionPointer(question.RequirementsRevision),
		}
		if operation != nil {
			if err := a.issueToolSetReceipt(operation, receipt); err != nil {
				return nil, err
			}
		}
		return cloneIssueToolReceipt(receipt), nil
	}
	if question.State != issueQuestionOpen || issue.openQuestion() != question {
		return nil, &IssueToolError{Code: "question_inactive", Message: "this question is no longer awaiting an answer; reload before answering"}
	}
	if expectedRevision != nil && *expectedRevision != issue.RequirementsRevision ||
		question.RequirementsRevision != issue.RequirementsRevision {
		return nil, &IssueToolError{Code: "revision_mismatch", Message: "question or issue was changed; reload before answering", CurrentRevision: issueToolRevisionPointer(issue.RequirementsRevision)}
	}
	if issue.Archived || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		return nil, errors.New("issue cannot accept this answer in its current state")
	}
	if err := validateTaskContext(capturedContext); err != nil {
		return nil, err
	}
	ledger := issueSupplementLedgerFor(issue)
	previousQuestion := *question
	previousStatus, previousUpdated, previousAuto := issue.Status, issue.UpdatedAt, ledger.AutoContinue
	previousContext := issue.Context.snapshot()
	var previousReceipt *IssueToolReceipt
	if operation != nil {
		previousReceipt = operation.Receipt
	}
	mergedContext := mergeIssueToolTurnContext(issue.Context.items, capturedContext)
	contextChanged := len(mergedContext) != len(issue.Context.items)
	if contextChanged {
		if err := validateTaskContext(mergedContext); err != nil {
			return nil, err
		}
	}
	var receipt *IssueToolReceipt
	if err := a.addIssueSupplementBefore(ct, issue.ID, text, func(item *issueSupplement) {
		if contextChanged {
			issue.Context.items = mergedContext
		}
		question.State, question.AnsweredAt, question.AnswerSupplementID = issueQuestionAnswered, nowMillis(), item.ID
		if question.PiCall != nil {
			item.PiQuestionOperationID = question.PiCall.OperationID
			question.AnswerText = item.Text
			question.AnswerContext = cloneTaskContextItems(capturedContext)
			question.DeliveryState = issueQuestionDeliverySaved
			issue.Status, ledger.AutoContinue = issueStatusInProgress, false
		} else if len(issue.Executions) > 0 {
			issue.Status, ledger.AutoContinue = issueStatusInProgress, true
		} else {
			issue.Status, ledger.AutoContinue = issueStatusNotStarted, false
		}
		issue.UpdatedAt = nowMillis()
		receipt = &IssueToolReceipt{
			State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: question.Ref,
			QuestionState: issueQuestionAnswered, IssueStatus: issue.Status,
			SupplementID: item.ID, RequirementsRevision: issueToolRevisionPointer(issue.RequirementsRevision),
		}
		if operation != nil {
			operation.Receipt = receipt
		}
	}); err != nil {
		*question = previousQuestion
		issue.Status, issue.UpdatedAt, ledger.AutoContinue = previousStatus, previousUpdated, previousAuto
		issue.Context.items = previousContext
		if operation != nil {
			operation.Receipt = previousReceipt
		}
		return nil, err
	}
	if question.PiCall != nil {
		if waiter := a.piQuestionWaiters[question.PiCall.OperationID]; waiter != nil {
			select {
			case waiter <- struct{}{}:
			default:
			}
		}
	}
	return cloneIssueToolReceipt(receipt), nil
}

func (a *appModel) issueToolRun(scope IssueToolScope, _ IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	waiter := make(chan IssueToolReceipt, 1)
	var callErr error
	a.runOnUI(func() {
		if a.shutdownStarted {
			callErr = errors.New("app is shutting down")
			return
		}
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct, issue := a.issueToolTarget(scope, operation.TargetIssueID)
		if issue == nil {
			callErr = errors.New("issue not found")
			return
		}
		if a.issueToolWaiters == nil {
			a.issueToolWaiters = map[string]chan IssueToolReceipt{}
		}
		a.issueToolWaiters[operation.ID] = waiter
		if !a.issueRunCapturedWithOperation(ct, issue, operation.ID) {
			callErr = errors.New(valueOr(a.issueFormError, "issue run was rejected"))
			return
		}
		execution := issue.Executions[len(issue.Executions)-1]
		if execution == nil || execution.ID == "" {
			callErr = errors.New("issue run did not record an execution")
			return
		}
	})
	if callErr != nil {
		a.runOnUI(func() { delete(a.issueToolWaiters, operation.ID) })
		return issueActionErrorResponse(callErr)
	}
	select {
	case receipt := <-waiter:
		a.runOnUI(func() { delete(a.issueToolWaiters, operation.ID) })
		return issueToolJSONResponse(receipt, receipt.State == issueToolReceiptUnknown)
	case <-time.After(issueToolWaitTimeout):
		a.runOnUI(func() { delete(a.issueToolWaiters, operation.ID) })
		return issueToolErrorResponse("operation_unknown", "issue task acceptance was not confirmed; no automatic retry")
	case <-a.issueToolShutdown:
		a.runOnUI(func() { delete(a.issueToolWaiters, operation.ID) })
		return issueToolErrorResponse("operation_unknown", "the app is shutting down before task acceptance was confirmed; no automatic retry")
	}
}

func (a *appModel) issueToolStop(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var receipt *IssueToolReceipt
	var callErr error
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct, issue := a.issueToolTarget(scope, request.IssueID)
		if issue == nil {
			callErr = errors.New("issue not found")
			return
		}
		taskID := issue.Work.ActiveTaskID
		execution := ct.executionByTask(taskID)
		if taskID == "" || execution == nil || execution.SessionID == "" || !issueHasTask(issue, taskID) {
			callErr = errors.New("issue has no active execution to stop")
			return
		}
		if !a.cancelIssueFor(ct, issue.ID) {
			callErr = errors.New("stop request was rejected")
			return
		}
		receipt = &IssueToolReceipt{
			State: issueToolReceiptStopRequested, IssueID: issue.ID, IssueStatus: issue.Status,
			ExecutionID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID,
			PiRun: clonePiActivityRun(execution.PiRun),
		}
		callErr = a.issueToolSetReceipt(operation, receipt)
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	return issueToolJSONResponse(cloneIssueToolReceipt(receipt), false)
}

func issueActionErrorResponse(err error) (issueToolForwardResponse, error) {
	if toolErr, ok := err.(*IssueToolError); ok {
		return issueToolJSONResponse(toolErr, true)
	}
	return issueToolErrorResponse("operation_unknown", err.Error())
}
