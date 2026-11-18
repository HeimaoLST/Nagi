package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

const (
	piIssueToolQueueLimit = 16
	piIssueToolCallLimit  = 256
)

var piIssueToolDefinitions = sync.OnceValues(func() ([]pi.DomainTool, error) {
	definitions := issueToolConversationDefinitions()
	tools := make([]pi.DomainTool, 0, len(definitions))
	for _, definition := range definitions {
		parameters, err := json.Marshal(definition.InputSchema)
		if err != nil {
			return nil, err
		}
		tools = append(tools, pi.DomainTool{
			Name: definition.Name, Description: definition.Description, Parameters: parameters,
		})
	}
	return tools, nil
})

var piIssueWorkDefinitions = sync.OnceValues(func() ([]pi.DomainTool, error) {
	definitions := issueToolWorkDefinitions()
	tools := make([]pi.DomainTool, 0, len(definitions))
	for _, definition := range definitions {
		parameters, err := json.Marshal(definition.InputSchema)
		if err != nil {
			return nil, err
		}
		tools = append(tools, pi.DomainTool{Name: definition.Name, Description: "Ask the user a concise question and wait for the answer.", Parameters: parameters})
	}
	return tools, nil
})

type piIssueToolCall struct {
	callID  string
	request issueToolForwardRequest
}

// piIssueTools owns callbacks for one run. Install HandleEvent before StartRun;
// an admission error requires the host to stop or cancel that run. Drain must
// finish off the UI actor before the host releases its actor or Pi client.
// Draining does not cancel admitted business work or settle an issue.
type piIssueTools struct {
	app     *appModel
	client  *pi.Client
	epoch   string
	binding pi.ContextBinding
	runID   string
	scope   IssueToolScope
	tools   []pi.DomainTool

	mu       sync.Mutex
	started  bool
	settled  bool
	closed   bool
	seq      uint64
	calls    map[string]struct{}
	queue    chan piIssueToolCall
	done     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	err      error
}

func newPiIssueTools(ctx context.Context, a *appModel, client *pi.Client, binding pi.ContextBinding, run pi.StartRunParams) (*piIssueTools, error) {
	if a == nil || client == nil || binding.ContextID == "" || binding.BotID == "" || binding.SessionRef == "" ||
		run.ContextID != binding.ContextID || run.RunID == "" || run.Correlation == nil ||
		run.Correlation.TurnID == "" || run.Correlation.ScopeRef == "" ||
		(binding.Purpose == pi.Conversation && (binding.ActivityID != "" || run.Correlation.ExecutionID != "")) ||
		(binding.Purpose == pi.Activity && (binding.ActivityID == "" || run.Correlation.ExecutionID == "")) ||
		(binding.Purpose != pi.Conversation && binding.Purpose != pi.Activity) {
		return nil, errors.New("Pi issue tools require an exact bound run and scope")
	}
	ready, err := client.Ready(ctx)
	if err != nil {
		return nil, err
	}
	tools, err := piIssueToolDefinitions()
	if binding.Purpose == pi.Activity {
		tools, err = piIssueWorkDefinitions()
	}
	if err != nil {
		return nil, err
	}
	var scope IssueToolScope
	a.runOnUI(func() {
		scope, err = a.issueToolScopeByRef(run.Correlation.ScopeRef)
		if err != nil {
			return
		}
		if scope.ContactID != binding.BotID || scope.RuntimeSessionID != binding.SessionRef ||
			scope.TurnID != run.Correlation.TurnID || scope.RuntimeTaskID != "" && scope.RuntimeTaskID != run.RunID ||
			binding.Purpose == pi.Conversation && scope.Mode == issueToolModeWork ||
			binding.Purpose == pi.Activity && (scope.Mode != issueToolModeWork || scope.WorkIssueID != binding.ActivityID || scope.WorkExecutionID != run.Correlation.ExecutionID) ||
			!a.issueToolScopeActive(scope) {
			err = errors.New("Pi binding does not own the active issue tool scope")
		}
	})
	if err != nil {
		return nil, err
	}
	bridge := &piIssueTools{
		app: a, client: client, epoch: ready.WorkerEpoch, binding: binding,
		runID: run.RunID, scope: scope, tools: tools,
		calls: make(map[string]struct{}), queue: make(chan piIssueToolCall, piIssueToolQueueLimit),
		done: make(chan struct{}), stop: make(chan struct{}),
	}
	go bridge.serve()
	return bridge, nil
}

func (b *piIssueTools) HandleEvent(event pi.Event) error {
	if event.WorkerEpoch != b.epoch || event.ContextID != b.binding.ContextID || event.RunID != b.runID ||
		event.Version != 1 || event.Type != "event" || event.Seq == 0 {
		return errors.New("Pi issue tool callback has a foreign transport owner")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if event.Seq <= b.seq || b.closed && event.Name != pi.RunStarted && event.Name != pi.RunSettled && event.Name != pi.ToolFinished {
		return errors.New("Pi issue tool callback is stale or admission is closed")
	}
	switch event.Name {
	case pi.RunStarted:
		var data struct {
			Correlation pi.RunCorrelation `json:"correlation"`
		}
		if b.started || b.settled || json.Unmarshal(event.Data, &data) != nil ||
			data.Correlation.TurnID != b.scope.TurnID || data.Correlation.ScopeRef != b.scope.Ref ||
			data.Correlation.ExecutionID != b.scope.WorkExecutionID {
			return errors.New("Pi run started with a different issue tool correlation")
		}
		b.started = true
	case pi.ToolRequest:
		var data pi.ToolRequestData
		if !b.started || json.Unmarshal(event.Data, &data) != nil || data.CallID == "" {
			return errors.New("Pi issue tool request has no admitted run or call identity")
		}
		known := false
		for _, tool := range b.tools {
			if tool.Name == data.Name {
				known = true
				break
			}
		}
		if !known {
			return errors.New("Pi callback is not a registered conversation issue tool")
		}
		if _, exists := b.calls[data.CallID]; exists {
			if b.binding.Purpose == pi.Activity {
				b.seq = event.Seq
				return nil
			}
			return errors.New("Pi issue tool call was already admitted; not replaying it")
		}
		if len(b.calls) >= piIssueToolCallLimit {
			return errors.New("Pi issue tool run reached its callback admission limit")
		}
		call := piIssueToolCall{callID: data.CallID, request: issueToolForwardRequest{Tool: data.Name, Arguments: data.Input}}
		select {
		case b.queue <- call:
			b.calls[data.CallID] = struct{}{}
		default:
			return errors.New("Pi issue tool callback queue is full")
		}
	case pi.RunSettled:
		if b.settled {
			return errors.New("Pi run has already settled")
		}
		b.settled = true
		b.cancelWait()
		if !b.closed {
			b.closed = true
			close(b.queue)
		}
	}
	b.seq = event.Seq
	return nil
}

func (b *piIssueTools) closeAdmission() {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		close(b.queue)
	}
	b.mu.Unlock()
}

func (b *piIssueTools) cancelWait() {
	b.stopOnce.Do(func() { close(b.stop) })
}

func (b *piIssueTools) Drain(ctx context.Context) error {
	b.closeAdmission()
	select {
	case <-b.done:
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *piIssueTools) serve() {
	defer close(b.done)
	for {
		select {
		case call, ok := <-b.queue:
			if !ok {
				return
			}
			if b.binding.Purpose == pi.Activity {
				b.dispatchActivity(call)
			} else {
				b.dispatch(call)
			}
		case <-b.client.Done():
			b.cancelWait()
			b.closeAdmission()
			for call := range b.queue {
				if b.binding.Purpose == pi.Activity {
					b.dispatchActivity(call)
				} else {
					b.dispatch(call)
				}
			}
			return
		}
	}
}

func (b *piIssueTools) authorize(ctx context.Context) error {
	inspect, err := b.client.Inspect(ctx, b.binding.ContextID)
	if err != nil {
		return err
	}
	if inspect.ContextID != b.binding.ContextID || inspect.SessionRef != b.binding.SessionRef ||
		inspect.ActiveRun == nil || inspect.ActiveRun.RunID != b.runID || inspect.ActiveRun.CancelRequested {
		return &IssueToolError{Code: "scope_inactive", Message: "the captured Pi run is no longer active"}
	}
	var ownerErr error
	b.app.runOnUI(func() {
		b.mu.Lock()
		settled := b.settled
		b.mu.Unlock()
		select {
		case <-b.client.Done():
			ownerErr = pi.ErrDisconnected
			return
		default:
		}
		scope, err := b.app.issueToolScopeByRef(b.scope.Ref)
		if err != nil {
			ownerErr = err
			return
		}
		if settled || scope.ContactID != b.binding.BotID || scope.RuntimeSessionID != b.binding.SessionRef ||
			scope.TurnID != b.scope.TurnID || scope.RuntimeTaskID != "" && scope.RuntimeTaskID != b.runID ||
			b.binding.Purpose == pi.Conversation && scope.Mode == issueToolModeWork ||
			b.binding.Purpose == pi.Activity && (scope.Mode != issueToolModeWork || scope.WorkIssueID != b.binding.ActivityID) ||
			!b.app.issueToolScopeActive(b.scope) {
			ownerErr = &IssueToolError{Code: "scope_inactive", Message: "the captured Pi run no longer owns this issue tool scope"}
		}
	})
	return ownerErr
}

func (b *piIssueTools) dispatch(call piIssueToolCall) {
	ctx, cancel := context.WithTimeout(context.Background(), issueToolWaitTimeout)
	defer cancel()
	var response issueToolForwardResponse
	var err error
	scope, scopeErr := issueToolArgumentString(call.request.Arguments, "scope")
	if scopeErr != nil {
		response, err = issueToolErrorResponse("invalid_arguments", scopeErr.Error())
	} else if scope != b.scope.Ref {
		response, err = issueToolErrorResponse("scope_denied", "the model scope does not match the captured Pi call owner")
	} else if ownerErr := b.authorize(ctx); ownerErr != nil {
		response, err = issueToolConsumeError(ownerErr)
	} else {
		response, err = b.app.handleIssueToolForward(ctx, call.request)
	}
	result := pi.ToolResultParams{
		WorkerEpoch: b.epoch, ContextID: b.binding.ContextID, RunID: b.runID, CallID: call.callID,
		Details: response.StructuredContent,
	}
	if err != nil {
		result.Error = &pi.RemoteError{Code: "operation_unknown", Message: err.Error()}
	} else if response.IsError {
		toolErr := issueToolResponseError(response)
		if toolErr == nil {
			b.recordError(fmt.Errorf("Pi issue tool %s returned an unreadable domain error", call.callID))
			return
		}
		result.Error = &pi.RemoteError{Code: toolErr.Code, Message: response.Content[0].Text}
	} else {
		result.Content = make([]pi.ContentBlock, 0, len(response.Content))
		for _, content := range response.Content {
			result.Content = append(result.Content, pi.ContentBlock{Type: content.Type, Text: content.Text})
		}
	}
	deliveryCtx, deliveryCancel := context.WithTimeout(context.Background(), issueToolWaitTimeout)
	defer deliveryCancel()
	receipt, err := b.client.ToolResult(deliveryCtx, result)
	if err != nil {
		b.recordError(fmt.Errorf("Pi issue tool result %s: %w", call.callID, err))
	} else if !receipt.Resolved {
		b.recordError(fmt.Errorf("Pi issue tool result %s was not resolved", call.callID))
	}
}

func (b *piIssueTools) recordError(err error) {
	b.mu.Lock()
	b.err = errors.Join(b.err, err)
	b.mu.Unlock()
}
