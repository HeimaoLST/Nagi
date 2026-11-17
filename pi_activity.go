package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func (h *piAppHost) openActivity(ctx context.Context, binding piActivityBinding) (pi.ContextBinding, error) {
	if binding.Engine != "pi" || binding.Context.ContextID == "" || binding.Context.BotID == "" || binding.Context.ActivityID == "" || binding.Context.Purpose != pi.Activity {
		return pi.ContextBinding{}, errors.New("invalid Pi activity binding")
	}
	h.openMu.Lock()
	defer h.openMu.Unlock()
	if existing, ok := h.contexts[binding.Context.ContextID]; ok {
		if existing.BotID != binding.Context.BotID || existing.ActivityID != binding.Context.ActivityID || existing.Purpose != pi.Activity || existing.SessionRef != binding.Context.SessionRef {
			return pi.ContextBinding{}, errors.New("Pi activity context belongs to a different binding")
		}
		if binding.Context.Used {
			existing.Used = true
			h.contexts[existing.ContextID] = existing
		}
		return existing, nil
	}
	private, err := readPrivateConnectionConfig(h.connectionPath)
	if err != nil {
		return pi.ContextBinding{}, err
	}
	source, err := private.resolve(binding.Configuration.CredentialSource)
	if err != nil {
		return pi.ContextBinding{}, err
	}
	if piWireAPI(source.API) != binding.Configuration.WireAPI || source.Endpoint != binding.Configuration.BaseURL {
		return pi.ContextBinding{}, errors.New("captured Pi connection source does not match the activity")
	}
	config := pi.ConfigureParams{WireAPI: binding.Configuration.WireAPI, BaseURL: binding.Configuration.BaseURL,
		APIKey: source.APIKey, CredentialSource: binding.Configuration.CredentialSource,
		Model: binding.Configuration.Model, ContextTokens: binding.Configuration.ContextTokens, OutputTokens: binding.Configuration.OutputTokens}
	tools, err := piIssueWorkDefinitions()
	if err != nil {
		return pi.ContextBinding{}, err
	}
	actual, err := h.client.OpenContext(ctx, pi.OpenContextParams{
		ContextID: binding.Context.ContextID, BotID: binding.Context.BotID, Purpose: pi.Activity,
		ActivityID: binding.Context.ActivityID, SelectionRevision: binding.Context.SelectionRevision,
		CWD: binding.Selection.CWD, Instructions: binding.Selection.Instructions,
		ResumeRef: binding.Context.SessionRef, Used: binding.Context.Used,
		CodingTools: binding.Selection.CodingTools, DomainTools: tools, Configuration: &config,
	})
	if err != nil {
		return pi.ContextBinding{}, err
	}
	if actual.ContextID != binding.Context.ContextID || actual.BotID != binding.Context.BotID || actual.Purpose != pi.Activity || actual.ActivityID != binding.Context.ActivityID || actual.SelectionRevision != binding.Context.SelectionRevision || actual.CWD == "" || actual.SessionRef == "" || binding.Context.Used && actual.SessionRef != binding.Context.SessionRef {
		return pi.ContextBinding{}, errors.New("Pi returned a different activity owner")
	}
	h.contexts[actual.ContextID] = actual
	return actual, nil
}

func (a *appModel) startPiIssueTask(ct *contact, issue *productIssue, execution *issueExecution, prompt string) bool {
	if a.pi == nil || execution == nil || !execution.PiIntent {
		issue.Work.setSendError("The Pi activity is unavailable.", "", "")
		return false
	}
	host := a.pi
	binding := clonePiActivityBinding(issue.PiBinding)
	workspace := a.effectiveContactWorkspace(ct)
	instructions := a.issueToolWorkSystemPrompt(ct)
	codingTools := cloneBuiltinTools(ct.BuiltinTools)
	items := cloneTaskContextItems(execution.Context)
	host.mu.Lock()
	configuration := host.defaults
	host.mu.Unlock()
	a.submissionAsyncStarted()
	go a.submitPiActivity(host, ct.ID, issue.ID, execution.ID, binding, workspace, instructions, codingTools, configuration, prompt, items)
	return true
}

func (a *appModel) submitPiActivity(host *piAppHost, contactID, issueID, executionID string, binding *piActivityBinding, workspace, instructions string, codingTools *[]string, configuration piConfiguration, prompt string, items []taskContextItem) {
	defer a.updateUI(func() { a.submissionAsyncFinished() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if binding == nil {
		if configuration.CredentialSource == "" {
			a.failPiActivity(host, contactID, issueID, executionID, errors.New("Pi connection defaults are unavailable"), false)
			return
		}
		var selection []string
		if codingTools != nil {
			selection = append([]string{}, (*codingTools)...)
		}
		binding = &piActivityBinding{Engine: "pi", Context: pi.ContextBinding{ContextID: newIssueToolTurnID(), BotID: contactID, Purpose: pi.Activity, ActivityID: issueID, SelectionRevision: 1},
			Selection: piSelection{CWD: workspace, Instructions: instructions, CodingTools: selection}, Configuration: configuration}
	}
	actual, err := host.openActivity(ctx, *binding)
	if err != nil {
		a.failPiActivity(host, contactID, issueID, executionID, err, false)
		return
	}
	binding.Context = actual
	var scope *IssueToolScope
	a.runOnUI(func() {
		ct := a.contactByID(contactID)
		if a.pi != host || a.shutdownStarted || ct == nil {
			err = errors.New("activity owner changed before binding")
			return
		}
		issue, execution := ct.executionByID(executionID)
		if issue == nil || issue.ID != issueID || execution == nil || issue.Work.pendingExecutionID != executionID || !execution.PiIntent {
			err = errors.New("activity intent is no longer current")
			return
		}
		previousBinding, previousSession, previousLoaded := issue.PiBinding, issue.WorkSessionID, issue.Work.Loaded
		issue.PiBinding, issue.WorkSessionID = clonePiActivityBinding(binding), actual.SessionRef
		issue.Work.Loaded = true
		if err = a.persist(); err != nil {
			issue.PiBinding, issue.WorkSessionID = previousBinding, previousSession
			issue.Work.Loaded = previousLoaded
			return
		}
		scope, err = a.issueToolNewWorkScope(contactID, issueID, executionID, actual.SessionRef, false)
	})
	if err != nil {
		a.failPiActivity(host, contactID, issueID, executionID, err, false)
		return
	}
	ready, err := host.client.Ready(ctx)
	if err != nil {
		a.failPiActivity(host, contactID, issueID, executionID, err, false)
		return
	}
	runID := newIssueToolTurnID()
	run := pi.StartRunParams{ContextID: actual.ContextID, RunID: runID, Text: prompt,
		Correlation: &pi.RunCorrelation{TurnID: scope.TurnID, ScopeRef: scope.Ref, ExecutionID: executionID}}
	wireInputs := taskContextInputs(items)
	if scopeInput, ok := issueToolScopeInput(scope); ok {
		wireInputs = append(wireInputs, scopeInput)
	}
	if err := validateRuntimeContextInputs(wireInputs); err != nil {
		a.failPiActivity(host, contactID, issueID, executionID, err, false)
		return
	}
	for _, item := range wireInputs {
		run.Context = append(run.Context, pi.ContextInput{Name: item.Name, Text: item.Text})
	}
	alreadyAttempted := false
	a.runOnUI(func() {
		ct := a.contactByID(contactID)
		if a.pi != host || a.shutdownStarted || ct == nil {
			err = errors.New("activity owner changed before run attempt")
			return
		}
		issue, execution := ct.executionByID(executionID)
		if issue == nil || issue.ID != issueID || execution == nil || issue.Work.pendingExecutionID != executionID || !isPiActivityIssue(issue) {
			err = errors.New("activity intent changed before run attempt")
			return
		}
		if issue.Status == issueStatusCanceled || issue.Work.cancelAfterAccept {
			if execution.PiRun != nil || execution.Status != issueExecutionPending {
				alreadyAttempted = true
				return
			}
			err = errors.New("activity canceled before dispatch")
			return
		}
		previousUsed := issue.PiBinding.Context.Used
		previousScope := a.issueToolScopes[scope.Ref]
		issue.PiBinding.Context.Used = true
		execution.PiRun = &piActivityRun{WorkerEpoch: ready.WorkerEpoch, ContextID: actual.ContextID, SessionRef: actual.SessionRef, RunID: runID, CancelRequested: issue.Work.cancelAfterAccept || issue.Status == issueStatusCanceled}
		execution.ToolScopeRef, execution.ToolScopeInput = scope.Ref, scope.Ref
		issue.Work.ActiveTaskID = runID
		a.issueToolBindTask(scope.Ref, runID)
		err = a.persist()
		if err != nil {
			issue.PiBinding.Context.Used = previousUsed
			a.issueToolScopes[scope.Ref] = previousScope
			execution.PiRun = nil
			execution.ToolScopeRef, execution.ToolScopeInput = "", ""
			issue.Work.ActiveTaskID = ""
		}
	})
	if alreadyAttempted {
		return
	}
	if err != nil {
		a.failPiActivity(host, contactID, issueID, executionID, err, false)
		return
	}
	bridge, err := newPiIssueTools(ctx, a, host.client, actual, run)
	if err != nil {
		a.failPiActivity(host, contactID, issueID, executionID, err, false)
		return
	}
	host.mu.Lock()
	host.bridges[runID] = bridge
	host.mu.Unlock()
	admission, err := host.client.StartRun(ctx, run)
	if err != nil || !admission.Admitted || admission.ContextID != actual.ContextID || admission.RunID != runID {
		uncertain := true
		if err != nil {
			var command *pi.CommandError
			uncertain = !errors.As(err, &command) || command.Delivery == pi.Unanswered
		} else {
			err = errors.New("Pi returned an invalid activity admission")
		}
		bridge.mu.Lock()
		started := bridge.started
		bridge.mu.Unlock()
		if started {
			a.runOnUI(func() { a.acceptPiActivity(host, contactID, issueID, executionID, runID) })
			return
		}
		admitted := false
		a.runOnUI(func() {
			if ct := a.contactByID(contactID); a.pi == host && ct != nil {
				owner, execution := ct.executionByID(executionID)
				admitted = owner != nil && owner.ID == issueID && execution != nil && execution.PiRun != nil && execution.PiRun.RunID == runID && execution.PiRun.Admitted
			}
		})
		if admitted {
			return
		}
		if !uncertain {
			bridge.cancelWait()
			_ = bridge.Drain(ctx)
			host.mu.Lock()
			delete(host.bridges, runID)
			host.mu.Unlock()
		}
		a.failPiActivity(host, contactID, issueID, executionID, err, uncertain)
		return
	}
	a.runOnUI(func() { a.acceptPiActivity(host, contactID, issueID, executionID, runID) })
}

func (a *appModel) acceptPiActivity(host *piAppHost, contactID, issueID, executionID, runID string) {
	ct := a.contactByID(contactID)
	if a.pi != host || ct == nil {
		return
	}
	issue, execution := ct.executionByID(executionID)
	if issue == nil || issue.ID != issueID || execution == nil || execution.PiRun == nil || execution.PiRun.RunID != runID || execution.PiRun.Admitted {
		return
	}
	execution.PiRun.Admitted = true
	execution.SessionID, execution.TaskID = execution.PiRun.SessionRef, runID
	execution.Status, execution.Detail = issueExecutionAccepted, ""
	issue.PiBinding.Context.Used = true
	issue.Work.ActiveTaskID, issue.Work.LastTaskID = runID, runID
	issue.Work.SendState, issue.Work.pendingExecutionID = sendAccepted, ""
	issue.Work.clearSendError()
	work := a.ensureWork(ct, runID, "")
	work.State, work.Prompt = workAccepted, execution.Prompt
	if execution.PiRun.CancelRequested {
		work.State, work.StopRequested = workStopping, true
	}
	previousContext := issue.Context.snapshot()
	previousPreview := issue.Context.previewID
	issue.Context.clearSubmitted(execution.Context)
	batch := issueSupplementBatchForExecution(issue, execution.ID)
	var err error
	if batch != nil {
		var accepted bool
		accepted, err = issue.Supplements.Accept(batch, issueSupplementAssociation{ExecutionID: execution.ID, PiRun: execution.PiRun}, a.persist)
		if !accepted && err == nil {
			err = errors.New("the Pi supplement association could not be recorded")
		}
	} else {
		err = a.persist()
	}
	if err != nil {
		issue.Context.items, issue.Context.previewID = previousContext, previousPreview
		execution.Status = issueExecutionUnknown
		execution.Detail = "Pi admitted the run, but its local receipt could not be saved."
		issue.Work.SendState = sendUnknown
		issue.Work.setSendError(execution.Detail, "send-uncertain", "")
	}
	a.issueToolCompleteExecution(execution, err == nil, execution.Detail)
	if execution.PiRun.CancelRequested {
		a.dispatchPiCancellation(host, issue, runID)
	}
}

func (a *appModel) failPiActivity(host *piAppHost, contactID, issueID, executionID string, failure error, uncertain bool) {
	a.runOnUI(func() {
		ct := a.contactByID(contactID)
		if a.pi != host || ct == nil {
			return
		}
		issue, execution := ct.executionByID(executionID)
		if issue == nil || issue.ID != issueID || execution == nil || execution.PiRun != nil && execution.PiRun.Admitted {
			return
		}
		if !uncertain && execution.PiRun == nil && (issue.Status == issueStatusCanceled || issue.Work.cancelAfterAccept) {
			if execution.Status != issueExecutionPending {
				return
			}
			batch := issueSupplementBatchForExecution(issue, execution.ID)
			if batch != nil && batch.State != supplementBatchDispatching && batch.State != supplementBatchCanceled && batch.State != supplementBatchNotDispatched {
				issue.Work.SendState = sendUnknown
				issue.Work.setSendError("The existing supplement outcome must be resolved before changing this activity.", "send-uncertain", "")
				return
			}
			previousStatus, previousDetail := execution.Status, execution.Detail
			previousPending, previousCancel := issue.Work.pendingExecutionID, issue.Work.cancelAfterAccept
			detail := "Activity canceled before dispatch."
			execution.Status, execution.Detail = issueExecutionNotDispatched, detail
			issue.Work.pendingExecutionID, issue.Work.cancelAfterAccept = "", false
			issue.Work.SendState = sendIdle
			saved := false
			if batch != nil && batch.State == supplementBatchDispatching {
				saved = issue.Supplements.NotDispatched(batch, detail, a.persist)
			} else {
				saved = a.persist() == nil
			}
			if !saved {
				execution.Status, execution.Detail = previousStatus, previousDetail
				issue.Work.pendingExecutionID, issue.Work.cancelAfterAccept = previousPending, previousCancel
				issue.Work.SendState = sendUnknown
				detail = "The canceled activity could not be saved; no run was dispatched."
				issue.Work.setSendError(detail, "send-uncertain", "")
			}
			a.issueToolCompleteExecution(execution, false, detail)
			return
		}
		detail := a.connectionSafeMessage(failure)
		issue.Work.pendingExecutionID = ""
		issue.Work.SendState = sendIdle
		execution.Status = issueExecutionNotDispatched
		var command *pi.CommandError
		if errors.As(failure, &command) && command.Delivery == pi.Rejected {
			execution.Status = issueExecutionRejected
		}
		if uncertain {
			execution.Status = issueExecutionUnknown
			issue.Work.SendState = sendUnknown
		} else {
			issue.Work.ActiveTaskID = ""
		}
		execution.Detail = detail
		issue.Work.setSendError(detail, "", "")
		if issue.Status != issueStatusCanceled && issue.Status != issueStatusCompleted {
			issue.Status = issueStatusNeedsAttention
		}
		if issue.Supplements != nil {
			issue.Supplements.AutoContinue = false
		}
		batch := issueSupplementBatchForExecution(issue, execution.ID)
		if batch != nil {
			if uncertain {
				issue.Supplements.Unknown(batch, detail, a.persist)
			} else if execution.Status == issueExecutionRejected {
				issue.Supplements.Reject(batch, detail, a.persist)
			} else {
				issue.Supplements.NotDispatched(batch, detail, a.persist)
			}
		} else {
			_ = a.persist()
		}
		a.issueToolCompleteExecution(execution, false, detail)
		if uncertain && execution.PiRun != nil && execution.PiRun.CancelRequested {
			a.dispatchPiCancellation(host, issue, execution.PiRun.RunID)
		}
	})
}

func (a *appModel) canCancelIssue(ct *contact, issue *productIssue) string {
	if ct == nil || issue == nil || a.contactByID(ct.ID) != ct || ct.issueByID(issue.ID) != issue {
		return "The issue owner is no longer available."
	}
	if a.demo {
		if issue.Work.ActiveTaskID == "" {
			return "This issue has no active work."
		}
		return ""
	}
	if a.pi == nil || !isPiActivityIssue(issue) || issue.Work.ActiveTaskID == "" {
		return "This issue has no active Pi work."
	}
	if issue.Work.StopRequestedTasks[issue.Work.ActiveTaskID] {
		return "Cancellation is already requested."
	}
	for _, execution := range issue.Executions {
		if execution != nil && execution.PiIntent && execution.PiRun != nil && execution.PiRun.RunID == issue.Work.ActiveTaskID && execution.PiRun.ContextID == issue.PiBinding.Context.ContextID && execution.PiRun.SessionRef == issue.WorkSessionID {
			return ""
		}
	}
	return "The active activity owner is unavailable."
}

func (a *appModel) cancelPiIssue(ct *contact, issue *productIssue) bool {
	if a.canCancelIssue(ct, issue) != "" {
		return false
	}
	host, runID := a.pi, issue.Work.ActiveTaskID
	var run *piActivityRun
	for _, execution := range issue.Executions {
		if execution != nil && execution.PiRun != nil && execution.PiRun.RunID == runID {
			run = execution.PiRun
			break
		}
	}
	if run == nil {
		return false
	}
	if issue.Work.StopRequestedTasks == nil {
		issue.Work.StopRequestedTasks = map[string]bool{}
	}
	issue.Work.StopRequestedTasks[runID] = true
	run.CancelRequested = true
	if issue.Supplements != nil {
		issue.Supplements.AutoContinue = false
	}
	if work := workFor(ct, runID); work != nil {
		work.State, work.StopRequested = workStopping, true
	}
	for _, question := range issue.Questions {
		if question != nil && question.PiCall != nil && question.PiCall.RunID == runID {
			if question.DeliveryState == issueQuestionDeliverySaved {
				question.DeliveryState = issueQuestionDeliveryCanceled
			} else if question.DeliveryState == issueQuestionDeliveryDispatching {
				question.DeliveryState = issueQuestionDeliveryUnknown
			}
		}
	}
	_ = a.persist()
	if run.Admitted || issue.Work.SendState == sendUnknown {
		a.dispatchPiCancellation(host, issue, runID)
	}
	return true
}

func (a *appModel) dispatchPiCancellation(host *piAppHost, issue *productIssue, runID string) {
	if issue.Work.piCancelSent[runID] {
		return
	}
	if issue.Work.piCancelSent == nil {
		issue.Work.piCancelSent = map[string]bool{}
	}
	issue.Work.piCancelSent[runID] = true
	contextID := issue.PiBinding.Context.ContextID
	contactID, issueID := issue.ContactID, issue.ID
	host.mu.Lock()
	bridge := host.bridges[runID]
	host.mu.Unlock()
	if bridge != nil {
		bridge.closeAdmission()
		bridge.cancelWait()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := host.client.CancelRun(ctx, pi.CancelRunParams{ContextID: contextID, RunID: runID})
		if err != nil {
			a.updateUI(func() {
				if ct := a.contactByID(contactID); a.pi == host && ct != nil && ct.issueByID(issueID) == issue && issue.Work.ActiveTaskID == runID {
					issue.Work.setSendError("Cancellation could not be confirmed: "+a.connectionSafeMessage(err), "", "")
				}
			})
		}
	}()
}

func (a *appModel) applyPiActivityEvent(host *piAppHost, bridge *piIssueTools, event pi.Event) {
	if a.pi != host || a.connectionGeneration != host.generation || event.WorkerEpoch != bridge.epoch {
		return
	}
	ct := a.contactByID(bridge.binding.BotID)
	if ct == nil {
		return
	}
	issue, execution := ct.executionByID(bridge.scope.WorkExecutionID)
	if issue == nil || issue.ID != bridge.binding.ActivityID || execution == nil || execution.PiRun == nil || execution.PiRun.WorkerEpoch != event.WorkerEpoch || execution.PiRun.RunID != event.RunID || execution.PiRun.ContextID != event.ContextID {
		return
	}
	if event.Name != pi.ToolFinished && issue.Work.ActiveTaskID != event.RunID {
		return
	}
	switch event.Name {
	case pi.RunStarted:
		a.acceptPiActivity(host, ct.ID, issue.ID, execution.ID, event.RunID)
		if execution.Status == issueExecutionAccepted {
			execution.Status = issueExecutionRunning
			if work := workFor(ct, event.RunID); work != nil && !execution.PiRun.CancelRequested {
				work.State = workRunning
			}
			_ = a.persist()
		}
	case pi.TextDelta:
		var data pi.TextDeltaData
		if json.Unmarshal(event.Data, &data) == nil {
			if work := workFor(ct, event.RunID); work != nil {
				work.Detail += data.Text
			}
		}
	case pi.ToolFinished:
		var data pi.ToolFinishedData
		if json.Unmarshal(event.Data, &data) == nil && data.Name == issueToolWorkToolName && !data.IsError {
			for _, question := range issue.Questions {
				if question != nil && question.PiCall != nil && question.PiCall.WorkerEpoch == event.WorkerEpoch && question.PiCall.ContextID == event.ContextID && question.PiCall.RunID == event.RunID && question.PiCall.CallID == data.CallID && (question.DeliveryState == issueQuestionDeliveryDispatching || question.DeliveryState == issueQuestionDeliveryUnknown) {
					question.DeliveryState = issueQuestionDeliveryResolved
					if err := a.persist(); err != nil {
						question.DeliveryState = issueQuestionDeliveryUnknown
					}
				}
			}
		}
	case pi.RunSettled:
		var data pi.RunSettledData
		decodeErr := json.Unmarshal(event.Data, &data)
		bridge.mu.Lock()
		started := bridge.started
		bridge.mu.Unlock()
		if started && !execution.PiRun.Admitted {
			a.acceptPiActivity(host, ct.ID, issue.ID, execution.ID, event.RunID)
		}
		issue.Work.ActiveTaskID, issue.Work.pendingExecutionID = "", ""
		if decodeErr != nil || data.Status != pi.Completed && data.Status != pi.Interrupted && data.Status != pi.Failed {
			execution.Status = issueExecutionUnknown
			execution.Detail = "Pi returned an invalid activity settlement."
			issue.Work.SendState = sendUnknown
		} else if !execution.PiRun.Admitted {
			execution.Status = issueExecutionUnknown
			execution.Detail = "Pi settled an activity without recorded admission."
			issue.Work.SendState = sendUnknown
		} else {
			execution.Status = string(data.Status)
			if data.Error != nil {
				execution.Detail = data.Error.Message
			}
			if work := workFor(ct, event.RunID); work != nil {
				work.State, work.Detail = workStateFromTask(string(data.Status)), execution.Detail
			}
			issue.Work.SendState = sendIdle
			a.applyIssueTerminalLifecycle(issue, event.RunID, string(data.Status))
		}
		if err := a.persist(); err != nil {
			execution.Status = issueExecutionUnknown
			execution.Detail = "Pi settled the run, but its local state could not be saved."
			issue.Work.SendState = sendUnknown
		}
		a.submissionAsyncStarted()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := bridge.Drain(ctx)
			cancel()
			if err == nil {
				host.mu.Lock()
				delete(host.bridges, event.RunID)
				host.mu.Unlock()
			}
			a.updateUI(func() {
				defer a.submissionAsyncFinished()
				if err != nil && a.pi == host {
					issue.Work.setSendError("Activity callbacks did not drain: "+a.connectionSafeMessage(err), "", "")
				}
			})
		}()
	}
}

func (b *piIssueTools) dispatchActivity(call piIssueToolCall) {
	select {
	case <-b.stop:
		return
	case <-b.client.Done():
		return
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), issueToolWaitTimeout)
	ownerErr := b.authorize(ctx)
	cancel()
	if ownerErr != nil {
		b.sendActivityError(call.callID, ownerErr)
		return
	}
	scopeRef, err := issueToolArgumentString(call.request.Arguments, "scope")
	if err != nil || scopeRef != b.scope.Ref {
		b.sendActivityError(call.callID, errors.New("the question scope does not match the activity"))
		return
	}
	text, err := issueToolArgumentString(call.request.Arguments, "text")
	if err != nil || strings.TrimSpace(text) == "" {
		b.sendActivityError(call.callID, errors.New("the question text is required"))
		return
	}
	wake := make(chan struct{}, 1)
	var questionRef, operationID string
	b.app.runOnUI(func() {
		ct := b.app.contactByID(b.binding.BotID)
		if b.app.shutdownStarted || ct == nil || !b.app.issueToolScopeActive(b.scope) {
			err = errors.New("the captured activity is no longer active")
			return
		}
		issue, execution := ct.executionByID(b.scope.WorkExecutionID)
		if issue == nil || issue.ID != b.binding.ActivityID || execution == nil || execution.PiRun == nil || execution.PiRun.RunID != b.runID || execution.RequirementsRevision != issue.RequirementsRevision {
			err = errors.New("the activity question owner or revision changed")
			return
		}
		if b.app.issueToolLedger == nil {
			b.app.issueToolLedger = &IssueToolOperationLedger{}
		}
		for _, existing := range b.app.issueToolLedger.Operations {
			if existing != nil && existing.PiCall != nil && existing.PiCall.WorkerEpoch == b.epoch && existing.PiCall.ContextID == b.binding.ContextID && existing.PiCall.RunID == b.runID && existing.PiCall.CallID == call.callID {
				err = errors.New("the activity call is already recorded; no replay")
				return
			}
		}
		request := IssueToolActionRequest{ScopeRef: b.scope.Ref, Action: issueToolActionWait, IssueID: issue.ID, ExpectedRevision: issueToolRevisionPointer(issue.RequirementsRevision), Text: text}
		if err = request.Validate(b.scope); err != nil {
			return
		}
		payload, payloadErr := issueToolCanonicalPayload(request)
		if payloadErr != nil {
			err = payloadErr
			return
		}
		operationID = newIssueToolOperationID()
		owner := &piQuestionCall{WorkerEpoch: b.epoch, ContextID: b.binding.ContextID, RunID: b.runID, CallID: call.callID,
			OperationID: operationID, ExecutionID: execution.ID, ContactID: ct.ID, IssueID: issue.ID, SessionRef: b.binding.SessionRef}
		question := &issueQuestion{Ref: newIssueQuestionRef(), Text: strings.TrimSpace(text), RequirementsRevision: issue.RequirementsRevision,
			SourceTaskID: b.runID, SourceScopeRef: b.scope.Ref, SourceTurnID: b.scope.TurnID,
			State: issueQuestionOpen, CreatedAt: nowMillis(), PiCall: clonePiQuestionCall(owner)}
		operation := &IssueToolOperation{ID: operationID, ScopeRef: b.scope.Ref, TurnID: b.scope.TurnID,
			Action: issueToolActionWait, TargetIssueID: issue.ID, Payload: payload, PiCall: owner, CreatedAt: nowMillis(), UpdatedAt: nowMillis()}
		receipt := &IssueToolReceipt{State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: question.Ref,
			QuestionState: issueQuestionOpen, IssueStatus: issueStatusWaitingUser, RequirementsRevision: issueToolRevisionPointer(issue.RequirementsRevision)}
		if b.app.piQuestionWaiters == nil {
			b.app.piQuestionWaiters = map[string]chan struct{}{}
		}
		b.app.piQuestionWaiters[operationID] = wake
		b.app.issueToolLedger.Operations = append(b.app.issueToolLedger.Operations, operation)
		supersede := ""
		if open := issue.openQuestion(); open != nil {
			supersede = open.Ref
		}
		if !b.app.markIssueWaitingForQuestionActive(ct, issue.ID, supersede, question, operation, receipt, true) {
			b.app.issueToolLedger.Operations = b.app.issueToolLedger.Operations[:len(b.app.issueToolLedger.Operations)-1]
			delete(b.app.piQuestionWaiters, operationID)
			err = errors.New("the activity question could not be saved")
			return
		}
		questionRef = question.Ref
	})
	if err != nil {
		b.sendActivityError(call.callID, err)
		return
	}
	defer b.app.runOnUI(func() { delete(b.app.piQuestionWaiters, operationID) })
	select {
	case <-wake:
	case <-b.stop:
		return
	case <-b.client.Done():
		return
	}
	var answer string
	b.app.runOnUI(func() {
		ct := b.app.contactByID(b.binding.BotID)
		if ct == nil || b.app.pi == nil || b.app.pi.client != b.client {
			err = errors.New("the activity owner is unavailable")
			return
		}
		issue := ct.issueByID(b.binding.ActivityID)
		if issue == nil {
			err = errors.New("the activity issue is unavailable")
			return
		}
		question := issue.questionByRef(questionRef)
		if question == nil || question.PiCall == nil || question.PiCall.OperationID != operationID || question.State != issueQuestionAnswered || question.DeliveryState != issueQuestionDeliverySaved {
			err = errors.New("the saved answer no longer owns this call")
			return
		}
		answer = question.AnswerText
		for _, item := range question.AnswerContext {
			answer += "\n\n" + item.inputName() + ":\n" + item.Text
		}
		question.DeliveryState = issueQuestionDeliveryDispatching
		if err = b.app.persist(); err != nil {
			question.DeliveryState = issueQuestionDeliverySaved
			issue.Work.setSendError("The saved answer could not be dispatched because its receipt could not be saved.", "", "")
		}
	})
	if err != nil {
		b.recordError(err)
		return
	}
	select {
	case <-b.stop:
		b.app.runOnUI(func() {
			ct := b.app.contactByID(b.binding.BotID)
			if ct == nil {
				return
			}
			issue := ct.issueByID(b.binding.ActivityID)
			if issue == nil {
				return
			}
			question := issue.questionByRef(questionRef)
			if question != nil && question.PiCall != nil && question.PiCall.OperationID == operationID && question.DeliveryState != "" && question.DeliveryState != issueQuestionDeliveryResolved {
				question.DeliveryState = issueQuestionDeliveryCanceled
				if err := b.app.persist(); err != nil {
					question.DeliveryState = issueQuestionDeliveryUnknown
				}
			}
		})
		return
	case <-b.client.Done():
		b.app.runOnUI(func() {
			ct := b.app.contactByID(b.binding.BotID)
			if ct == nil {
				return
			}
			issue := ct.issueByID(b.binding.ActivityID)
			if issue == nil {
				return
			}
			question := issue.questionByRef(questionRef)
			if question != nil && question.PiCall != nil && question.PiCall.OperationID == operationID && question.DeliveryState == issueQuestionDeliveryDispatching {
				question.DeliveryState = issueQuestionDeliveryUnknown
				_ = b.app.persist()
			}
		})
		return
	default:
	}
	deliveryCtx, deliveryCancel := context.WithTimeout(context.Background(), issueToolWaitTimeout)
	receipt, deliveryErr := b.client.ToolResult(deliveryCtx, pi.ToolResultParams{WorkerEpoch: b.epoch, ContextID: b.binding.ContextID, RunID: b.runID, CallID: call.callID,
		Content: []pi.ContentBlock{{Type: "text", Text: answer}}})
	deliveryCancel()
	b.app.runOnUI(func() {
		ct := b.app.contactByID(b.binding.BotID)
		if ct == nil {
			return
		}
		issue := ct.issueByID(b.binding.ActivityID)
		if issue == nil {
			return
		}
		question := issue.questionByRef(questionRef)
		if question == nil || question.PiCall == nil || question.PiCall.OperationID != operationID || question.DeliveryState == issueQuestionDeliveryResolved {
			return
		}
		if deliveryErr == nil && receipt.Resolved {
			question.DeliveryState = issueQuestionDeliveryResolved
		} else {
			question.DeliveryState = issueQuestionDeliveryUnknown
		}
		if err := b.app.persist(); err != nil {
			question.DeliveryState = issueQuestionDeliveryUnknown
		}
		if question.DeliveryState == issueQuestionDeliveryUnknown {
			issue.Work.setSendError("Answer delivery could not be confirmed.", "", "")
		}
	})
}

func (b *piIssueTools) sendActivityError(callID string, failure error) {
	ctx, cancel := context.WithTimeout(context.Background(), issueToolWaitTimeout)
	defer cancel()
	select {
	case <-b.stop:
		return
	default:
	}
	_, err := b.client.ToolResult(ctx, pi.ToolResultParams{WorkerEpoch: b.epoch, ContextID: b.binding.ContextID, RunID: b.runID, CallID: callID,
		Error: &pi.RemoteError{Code: "operation_failed", Message: failure.Error()}})
	if err != nil {
		b.recordError(err)
	}
}

func (a *appModel) inspectPiIssueWork(contactID, issueID string) {
	ct := a.contactByID(contactID)
	if a.shutdownStarted || a.pi == nil || ct == nil {
		return
	}
	issue := ct.issueByID(issueID)
	if !isPiActivityIssue(issue) || len(issue.Executions) == 0 {
		return
	}
	execution := issue.Executions[len(issue.Executions)-1]
	if execution == nil || execution.PiRun == nil {
		return
	}
	host, run, executionID := a.pi, *execution.PiRun, execution.ID
	issue.Work.historyGen++
	generation := issue.Work.historyGen
	a.historyAsyncStarted()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ready, err := host.client.Ready(ctx)
		var inspection pi.InspectResult
		if err == nil && ready.WorkerEpoch != run.WorkerEpoch {
			err = errors.New("the recorded activity belongs to a previous Pi worker")
		}
		if err == nil {
			inspection, err = host.client.Inspect(ctx, run.ContextID)
		}
		cancel()
		a.updateUI(func() {
			defer a.historyAsyncFinished()
			ct := a.contactByID(contactID)
			if a.pi != host || ct == nil {
				return
			}
			issue, execution := ct.executionByID(executionID)
			if issue == nil || issue.ID != issueID || issue.Work.historyGen != generation || execution == nil || !samePiActivityRun(execution.PiRun, &run) {
				return
			}
			if err != nil || inspection.ContextID != run.ContextID || inspection.SessionRef != run.SessionRef {
				issue.Work.LoadErr = "Pi activity progress is unavailable."
				return
			}
			issue.Work.Loaded, issue.Work.LoadErr = true, ""
			if inspection.ActiveRun != nil && inspection.ActiveRun.RunID == run.RunID && execution.TaskID == run.RunID {
				work := a.ensureWork(ct, run.RunID, "")
				work.State = workRunning
				if inspection.ActiveRun.CancelRequested {
					work.State, work.StopRequested = workStopping, true
				}
			}
		})
	}()
}
