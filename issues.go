package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HeimaoLST/Nagi/locales"
)

// A productIssue is the GUI-owned product object. Runtime tasks are linked as
// execution associations; an execution terminal is not an item completion.
type productIssue struct {
	ID                   string
	ContactID            string
	Goal                 string
	Requirements         []*issueRequirement
	Status               string
	Archived             bool
	Evidence             string
	CreatedAt            int64
	UpdatedAt            int64
	Executions           []*issueExecution
	PiBinding            *piActivityBinding
	Handoff              *issueHandoff
	RequirementsRevision int

	// Context is the pending context for the next Run/Continue. Each execution
	// stores its own immutable Context snapshot before dispatch.
	Context taskContext

	// Supplements owns user supplements and their dispatch receipts for this
	// issue. It is persisted with the issue; it is not a Runtime history copy.
	Supplements *issueSupplementLedger

	// Questions are persistent missing-information prompts. An answer is linked
	// to the same supplement ledger, not to an invented assistant message.
	Questions []*issueQuestion

	// WorkSessionID is the issue-owned execution context. It is intentionally
	// separate from contact.SessionID (the ongoing conversation). Old
	// executions keep their actual SessionID and are never migrated in place.
	WorkSessionID string
	Work          issueWorkSession
}

const (
	issueQuestionOpen                = "open"
	issueQuestionAnswered            = "answered"
	issueQuestionSuperseded          = "superseded"
	issueQuestionDeliverySaved       = "saved"
	issueQuestionDeliveryDispatching = "dispatching"
	issueQuestionDeliveryResolved    = "resolved"
	issueQuestionDeliveryUnknown     = "unknown"
	issueQuestionDeliveryCanceled    = "canceled"
)

type issueQuestion struct {
	Ref                  string            `json:"ref"`
	Text                 string            `json:"text"`
	RequirementsRevision int               `json:"requirements_revision"`
	SourceTaskID         string            `json:"source_task_id,omitempty"`
	SourceScopeRef       string            `json:"source_scope_ref,omitempty"`
	SourceTurnID         string            `json:"source_turn_id,omitempty"`
	State                string            `json:"state"`
	CreatedAt            int64             `json:"created_at,omitempty"`
	AnsweredAt           int64             `json:"answered_at,omitempty"`
	AnswerSupplementID   string            `json:"answer_supplement_id,omitempty"`
	PiCall               *piQuestionCall   `json:"pi_call,omitempty"`
	DeliveryState        string            `json:"delivery_state,omitempty"`
	AnswerContext        []taskContextItem `json:"answer_context,omitempty"`
	AnswerText           string            `json:"answer_text,omitempty"`
}

func newIssueQuestionRef() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "question-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("question-%d", time.Now().UnixNano())
}

func (issue *productIssue) openQuestion() *issueQuestion {
	if issue == nil {
		return nil
	}
	for index := len(issue.Questions) - 1; index >= 0; index-- {
		question := issue.Questions[index]
		if question != nil && question.State == issueQuestionOpen {
			return question
		}
	}
	return nil
}

func (issue *productIssue) questionByRef(ref string) *issueQuestion {
	if issue == nil {
		return nil
	}
	for _, question := range issue.Questions {
		if question != nil && question.Ref == ref {
			return question
		}
	}
	return nil
}

// issueWorkSession is transient state for the issue's Pi activity.
type issueWorkSession struct {
	Loaded             bool
	LoadErr            string
	ActiveTaskID       string
	LastTaskID         string
	SendState          sendState
	SendError          string
	SendErrorKind      string
	SendErrorDetail    string
	Messages           []*message
	StopRequestedTasks map[string]bool
	pendingExecutionID string
	cancelAfterAccept  bool
	piCancelSent       map[string]bool
	historyGen         int
}

type issueRequirement struct {
	ID        string
	Text      string
	State     string
	Evidence  string
	UpdatedAt int64
}

// issueExecution is the persisted association for one attempt on an issue.
// Prompt is the exact pre-dispatch snapshot; Runtime history remains the
// authority for task/message/tool results.
type issueExecution struct {
	ID                   string
	SessionID            string
	TaskID               string
	MessageID            string
	Prompt               string
	Status               string
	Detail               string
	AttentionSeenStatus  string // last exception status displayed; empty means unseen
	RequirementsRevision int
	Context              []taskContextItem
	ContextExpanded      bool
	SupplementBatchID    string
	ToolOperationID      string
	ToolScopeRef         string
	PiIntent             bool
	PiRun                *piActivityRun
	ReturnedAt           int64
	WaitRequested        bool
}

const (
	issueStatusNotStarted     = "not_started"
	issueStatusInProgress     = "in_progress"
	issueStatusWaitingUser    = "waiting_user"
	issueStatusNeedsAttention = "needs_attention"
	issueStatusCompleted      = "completed"
	issueStatusCanceled       = "canceled"

	requirementUnverified = "unverified"
	requirementSatisfied  = "satisfied"
	requirementUnmet      = "unmet"

	issueExecutionPending       = "pending"
	issueExecutionNotDispatched = "not_dispatched"
	issueExecutionRejected      = "rejected"
	issueExecutionAccepted      = "accepted"
	issueExecutionRunning       = "running"
	issueExecutionCompleted     = "completed"
	issueExecutionFailed        = "failed"
	issueExecutionInterrupted   = "interrupted"
	issueExecutionUnknown       = "unknown"
)

func newIssueID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "issue-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("issue-%d", time.Now().UnixNano())
}

func newRequirementID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "requirement-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("requirement-%d", time.Now().UnixNano())
}

func parseIssueRequirements(text string) []*issueRequirement {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []*issueRequirement
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, &issueRequirement{ID: newRequirementID(), Text: line, State: requirementUnverified})
	}
	return out
}

func issueRequirementsText(issue *productIssue) string {
	if issue == nil {
		return ""
	}
	parts := make([]string, 0, len(issue.Requirements))
	for _, requirement := range issue.Requirements {
		if requirement != nil && strings.TrimSpace(requirement.Text) != "" {
			parts = append(parts, requirement.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func normalizeIssueStatus(status string, executions []*issueExecution) string {
	switch status {
	case issueStatusNotStarted, issueStatusInProgress, issueStatusWaitingUser,
		issueStatusNeedsAttention, issueStatusCompleted, issueStatusCanceled:
		return status
	case "", "open":
		if len(executions) > 0 {
			return issueStatusInProgress
		}
		return issueStatusNotStarted
	default:
		return issueStatusNeedsAttention
	}
}

func issueStatusLabel(issue *productIssue) string {
	if issue == nil {
		return "Unknown"
	}
	if issue.Archived {
		return "Archived"
	}
	switch issue.Status {
	case issueStatusNotStarted:
		return "Not started"
	case issueStatusInProgress:
		return "In progress"
	case issueStatusWaitingUser:
		return "Waiting for you"
	case issueStatusNeedsAttention:
		return "Needs attention"
	case issueStatusCompleted:
		return "Completed"
	case issueStatusCanceled:
		return "Canceled"
	default:
		return "Needs attention"
	}
}

func newIssueExecutionID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "execution-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("execution-%d", time.Now().UnixNano())
}

func (ct *contact) issueByID(id string) *productIssue {
	for _, issue := range ct.Issues {
		if issue.ID == id {
			return issue
		}
	}
	return nil
}

func (ct *contact) selectedIssue() *productIssue {
	if ct.SelectedIssueID != "" {
		if issue := ct.issueByID(ct.SelectedIssueID); issue != nil {
			return issue
		}
	}
	for _, issue := range ct.Issues {
		if !issue.Archived && issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled {
			return issue
		}
	}
	if len(ct.Issues) > 0 {
		return ct.Issues[0]
	}
	return nil
}

func executionForTask(issue *productIssue, taskID string) *issueExecution {
	if issue == nil || taskID == "" {
		return nil
	}
	for _, execution := range issue.Executions {
		if execution != nil && execution.TaskID == taskID {
			return execution
		}
	}
	return nil
}

func (ct *contact) executionByTask(taskID string) *issueExecution {
	if taskID == "" {
		return nil
	}
	for _, issue := range ct.Issues {
		for _, execution := range issue.Executions {
			if execution.TaskID == taskID {
				return execution
			}
		}
	}
	return nil
}

func issueHasTask(issue *productIssue, taskID string) bool {
	if issue == nil || taskID == "" {
		return false
	}
	for _, execution := range issue.Executions {
		if execution.TaskID == taskID {
			return true
		}
	}
	return false
}

func issueExecutionForTask(issue *productIssue, taskID string) (int, *issueExecution) {
	if issue == nil {
		return 0, nil
	}
	for index, execution := range issue.Executions {
		if execution.TaskID == taskID {
			return index, execution
		}
	}
	return 0, nil
}

func (ct *contact) executionByID(id string) (*productIssue, *issueExecution) {
	for _, issue := range ct.Issues {
		for _, execution := range issue.Executions {
			if execution.ID == id {
				return issue, execution
			}
		}
	}
	return nil, nil
}

func issuePrompt(issue *productIssue) string {
	goal := strings.TrimSpace(issue.Goal)
	requirements := strings.TrimSpace(issueRequirementsText(issue))
	if requirements != "" {
		goal += "\n\nRequirements:\n" + requirements
	}
	if issue.Handoff != nil && len(issue.Handoff.Files) != 0 {
		goal += "\n\nFiles supplied for this activity:\n" + strings.Join(issue.Handoff.Files, "\n")
	}
	return goal
}

// createIssue persists an explicit product item and its original requirements.
// It does not dispatch any Runtime work; dispatch is a separate Run action.
func (a *appModel) setIssueFormNotice(raw, notice, detail string) {
	a.issueFormError = raw
	a.issueFormNotice = notice
	a.issueFormNoticeDetail = detail
}

func (a *appModel) clearIssueFormNotice() {
	a.issueFormError = ""
	a.issueFormNotice = ""
	a.issueFormNoticeDetail = ""
}

func (a *appModel) setIssueActionNotice(raw, notice, detail string) {
	a.issueActionError = raw
	a.issueActionNotice = notice
	a.issueActionNoticeDetail = detail
}

func (a *appModel) clearIssueActionNotice() {
	a.issueActionError = ""
	a.issueActionNotice = ""
	a.issueActionNoticeDetail = ""
}

func (a *appModel) createIssueFor(ct *contact, goal, requirements string) (*productIssue, error) {
	return a.createIssueForWithSave(ct, goal, requirements, nil)
}

func (a *appModel) createIssueForWithSave(ct *contact, goal, requirements string, save func(*productIssue) error) (*productIssue, error) {
	goal = strings.TrimSpace(goal)
	requirements = strings.TrimSpace(requirements)
	if goal == "" {
		return nil, errors.New("Enter an issue goal.")
	}
	if ct == nil {
		return nil, errors.New("Contact not found.")
	}
	now := nowMillis()
	item := &productIssue{
		ID: newIssueID(), ContactID: ct.ID, Goal: goal,
		Requirements: parseIssueRequirements(requirements), RequirementsRevision: 1,
		Status: issueStatusNotStarted, CreatedAt: now, UpdatedAt: now,
	}
	item.Context.onChange = a.persist
	if item.Supplements == nil {
		item.Supplements = newIssueSupplementLedger()
	}
	previous := ct.Issues
	ct.Issues = append(previous, item)
	if save == nil {
		save = func(*productIssue) error { return a.persist() }
	}
	if err := save(item); err != nil {
		ct.Issues = previous
		return nil, fmt.Errorf("Could not save this issue: %w", err)
	}
	return item, nil
}

func issueHasRemainingRequirements(issue *productIssue) bool {
	if issue == nil {
		return false
	}
	for _, requirement := range issue.Requirements {
		if requirement != nil && requirement.State != requirementSatisfied {
			return true
		}
	}
	return false
}

const (
	issueBlockNoIssue   = "no-issue"
	issueBlockArchived  = "archived"
	issueBlockQuestion  = "question"
	issueBlockCompleted = "completed"
	issueBlockCanceled  = "canceled"
	issueBlockExecution = "execution-untrusted"
)

func issueBlockRaw(code string) string {
	switch code {
	case issueBlockNoIssue:
		return "Issue not found."
	case issueBlockArchived:
		return "Restore this archived issue before starting work."
	case issueBlockQuestion:
		return "Answer the waiting question before continuing this issue."
	case issueBlockCompleted:
		return "Reopen this completed issue before starting another execution."
	case issueBlockCanceled:
		return "Reopen this canceled issue before starting another execution."
	case issueBlockExecution:
		return "An execution is still active or has no trusted terminal state; reload before running this issue again."
	default:
		return sendBlockRaw(code)
	}
}

func issueNoticeText(msgs locales.Messages, notice, detail string) string {
	switch notice {
	case issueBlockNoIssue:
		return msgs.IssueBlockNoIssue()
	case issueBlockArchived:
		return msgs.IssueBlockArchived()
	case issueBlockQuestion:
		return msgs.IssueBlockQuestion()
	case issueBlockCompleted:
		return msgs.IssueBlockCompleted()
	case issueBlockCanceled:
		return msgs.IssueBlockCanceled()
	case issueBlockExecution:
		return msgs.IssueBlockExecution()
	case "contact-missing":
		return msgs.IssueNoticeContactMissing()
	case "issue-missing":
		return msgs.IssueNoticeIssueMissing()
	case "no-goal":
		return msgs.IssueNoticeNoGoal()
	case "enter-goal":
		return msgs.IssueNoticeEnterGoal()
	case "archived-edit":
		return msgs.IssueNoticeArchivedEdit()
	case "terminal-edit":
		return msgs.IssueNoticeTerminalEdit()
	case "restore-failed":
		return msgs.IssueNoticeRestoreFailed()
	case "reopen-failed":
		return msgs.IssueNoticeReopenFailed()
	case "cancel-failed":
		return msgs.IssueNoticeCancelFailed()
	case "complete-failed":
		return msgs.IssueNoticeCompleteFailed()
	case "requirement-save-failed":
		return msgs.IssueNoticeRequirementSaveFailed()
	case "confirm-failed":
		return msgs.IssueNoticeConfirmFailed()
	case "resume-failed":
		return msgs.IssueNoticeResumeFailed()
	case "wait-failed":
		return msgs.IssueNoticeWaitFailed()
	case "archive-failed":
		return msgs.IssueNoticeArchiveFailed()
	case "view-save-failed":
		return msgs.IssueNoticeViewSaveFailed(detail)
	case "save-issue-failed":
		return msgs.IssueNoticeSaveIssueFailed(detail)
	case "execution-save-failed":
		return msgs.IssueNoticeExecutionSaveFailed(detail)
	case "supplement-retry-save-failed":
		return msgs.IssueNoticeSupplementRetrySaveFailed()
	case "supplement-restore-failed":
		return msgs.IssueNoticeSupplementRestoreFailed(detail)
	case "supplement-batch-active":
		return msgs.IssueNoticeSupplementBatchActive(supplementBatchStateLabel(msgs, detail))
	case "form-switch":
		return msgs.IssueNoticeFormSwitch()
	case "supplement-auto-stop-failed":
		return msgs.IssueNoticeSupplementAutoStop()
	case "supplement-auto-resume-failed":
		return msgs.IssueNoticeSupplementAutoResume()
	case "supplement-retry-failed":
		return msgs.IssueNoticeSupplementRetry()
	default:
		return sendBlockText(msgs, notice)
	}
}

func issueExecutionText(msgs locales.Messages, status string) string {
	switch status {
	case issueExecutionPending:
		return msgs.IssueExecutionPending()
	case issueExecutionNotDispatched:
		return msgs.IssueExecutionNotDispatched()
	case issueExecutionRejected:
		return msgs.IssueExecutionRejected()
	case issueExecutionAccepted:
		return msgs.IssueExecutionAccepted()
	case issueExecutionRunning:
		return msgs.IssueExecutionRunning()
	case issueExecutionCompleted:
		return msgs.IssueExecutionCompleted()
	case issueExecutionFailed:
		return msgs.IssueExecutionFailed()
	case issueExecutionInterrupted:
		return msgs.IssueExecutionInterrupted()
	case issueExecutionUnknown:
		return msgs.IssueExecutionUnknown()
	default:
		return status
	}
}

func issuesForStoredContact(all []storedIssue, contactID string) []*productIssue {
	out := make([]*productIssue, 0)
	for _, stored := range all {
		if stored.ContactID != contactID || stored.ID == "" {
			continue
		}
		issue := &productIssue{
			ID: stored.ID, ContactID: contactID, Goal: stored.Goal,
			Archived: stored.Archived, RequirementsRevision: stored.RequirementsRevision,
			Evidence: stored.Evidence, CreatedAt: stored.CreatedAt, UpdatedAt: stored.UpdatedAt,
			PiBinding:     clonePiActivityBinding(stored.PiBinding),
			Handoff:       cloneIssueHandoff(stored.Handoff),
			WorkSessionID: stored.WorkSessionID,
			Context:       taskContext{items: cloneTaskContextItems(stored.TaskContext)},
			Supplements:   cloneIssueSupplementLedger(stored.Supplements),
		}
		for _, question := range stored.Questions {
			if question == nil || strings.TrimSpace(question.Ref) == "" {
				continue
			}
			copy := *question
			copy.PiCall = clonePiQuestionCall(question.PiCall)
			copy.AnswerContext = cloneTaskContextItems(question.AnswerContext)
			if copy.DeliveryState == issueQuestionDeliveryDispatching {
				copy.DeliveryState = issueQuestionDeliveryUnknown
			}
			issue.Questions = append(issue.Questions, &copy)
		}
		for _, item := range stored.RequirementItems {
			if strings.TrimSpace(item.Text) == "" {
				continue
			}
			state := item.State
			if state != requirementSatisfied && state != requirementUnmet {
				state = requirementUnverified
			}
			issue.Requirements = append(issue.Requirements, &issueRequirement{
				ID: item.ID, Text: item.Text, State: state, Evidence: item.Evidence, UpdatedAt: item.UpdatedAt,
			})
		}
		if issue.RequirementsRevision < 1 {
			issue.RequirementsRevision = 1
		}
		for _, execution := range stored.Executions {
			if execution.ID == "" {
				continue
			}
			status := execution.Status
			if status == "" {
				status = issueExecutionUnknown
			}
			detail := execution.Detail
			if status == issueExecutionPending || status == issueExecutionAccepted || status == issueExecutionRunning {
				status = issueExecutionUnknown
				detail = "The previous Pi run is unavailable; its outcome is unknown."
			}
			issue.Executions = append(issue.Executions, &issueExecution{
				ID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID,
				MessageID: execution.MessageID, Prompt: execution.Prompt,
				Status: status, Detail: detail, AttentionSeenStatus: execution.AttentionSeenStatus,
				RequirementsRevision: execution.RequirementsRevision,
				Context:              cloneTaskContextItems(execution.Context), SupplementBatchID: execution.SupplementBatchID,
				ToolOperationID: execution.ToolOperationID, ToolScopeRef: execution.ToolScopeRef,
				WaitRequested: execution.WaitRequested,
				PiIntent:      execution.PiIntent, PiRun: clonePiActivityRun(execution.PiRun),
				ReturnedAt: execution.ReturnedAt,
			})
		}
		issue.Status = normalizeIssueStatus(stored.Status, issue.Executions)
		out = append(out, issue)
	}
	return out
}

// issueRunCaptured validates and dispatches using captured objects only. The
// automatic continuation path uses this directly so it never depends on, or
// steals, the UI's selected issue.
func (a *appModel) issueRunCaptured(ct *contact, issue *productIssue) bool {
	return a.issueRunCapturedWithOperation(ct, issue, "")
}

func (a *appModel) issueRunCapturedWithOperation(ct *contact, issue *productIssue, operationID string) bool {
	if ct == nil || issue == nil {
		a.setIssueFormNotice("Issue not found for this contact.", "issue-missing", "")
		return false
	}
	if reason := issueLifecycleStartBlocked(issue); reason != "" {
		a.setIssueFormNotice(issueBlockRaw(reason), reason, "")
		return false
	}
	if reason := issueRunBlocked(issue); reason != "" {
		a.setIssueFormNotice(issueBlockRaw(reason), reason, "")
		return false
	}
	if reason := a.canStartIssue(ct, issue); reason != "" {
		a.setIssueFormNotice(issueBlockRaw(reason), reason, "")
		issue.Work.setSendError(sendBlockRaw(reason), reason, "")
		return false
	}
	if strings.TrimSpace(issuePrompt(issue)) == "" {
		a.setIssueFormNotice("This issue has no goal to run.", "no-goal", "")
		return false
	}
	return a.startIssueRunWithOperation(ct, issue, operationID)
}

func issueExecutionStatusFromWork(state workState) string {
	switch state {
	case workAccepted:
		return issueExecutionAccepted
	case workRunning:
		return issueExecutionRunning
	case workCompleted:
		return issueExecutionCompleted
	case workFailed:
		return issueExecutionFailed
	case workInterrupted:
		return issueExecutionInterrupted
	default:
		return issueExecutionUnknown
	}
}

func unresolvedExecution(execution *issueExecution) bool {
	switch execution.Status {
	case issueExecutionPending, issueExecutionAccepted, issueExecutionRunning, issueExecutionUnknown:
		return true
	default:
		return false
	}
}

func issueRunBlocked(issue *productIssue) string {
	for _, execution := range issue.Executions {
		if unresolvedExecution(execution) {
			return issueBlockExecution
		}
	}
	return ""
}

// issueDisplayStatus is the single read-only display projection for an issue.
type issueDisplayStatus struct {
	Status    string
	Label     string
	Reason    string
	Attention bool
	Waiting   bool
	Active    bool
	Archived  bool
}

func issueWorkIsActive(issue *productIssue) bool {
	return issue != nil && issue.Work.SendState != sendUnknown &&
		(issue.Work.ActiveTaskID != "" || issue.Work.SendState == sendSubmitting || issue.Work.SendState == sendAccepted)
}

func issueManagementTerminal(issue *productIssue) bool {
	return issue != nil && (issue.Archived ||
		issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled)
}

func issueAttention(issue *productIssue) (bool, string) {
	if issue == nil {
		return false, ""
	}
	// An accepted task can retain its active mapping when clearing the accepted
	// context or receipt fails; that current send error remains actionable.
	if issueWorkIsActive(issue) && issue.Work.SendError != "" {
		return true, issue.Work.SendError
	}
	explicit := issue.Status == issueStatusNeedsAttention
	unseen := false
	unseenDetail := ""
	for index := len(issue.Executions) - 1; index >= 0; index-- {
		execution := issue.Executions[index]
		if execution == nil || !issueExecutionNeedsAttention(execution.Status) ||
			execution.AttentionSeenStatus == execution.Status {
			continue
		}
		unseen = true
		if unseenDetail == "" && execution.Detail != "" {
			unseenDetail = execution.Detail
		}
	}
	unknownDispatch := false
	if issue.Work.SendState == sendUnknown {
		execution := (*issueExecution)(nil)
		if id := issue.Work.pendingExecutionID; id != "" {
			for _, candidate := range issue.Executions {
				if candidate != nil && candidate.ID == id {
					execution = candidate
					break
				}
			}
		}
		if execution == nil {
			taskID := issue.Work.ActiveTaskID
			if taskID == "" {
				taskID = issue.Work.LastTaskID
			}
			execution = executionForTask(issue, taskID)
		}
		unknownDispatch = execution == nil || execution.AttentionSeenStatus == "" ||
			(execution.AttentionSeenStatus != execution.Status && issueExecutionNeedsAttention(execution.Status))
	}
	if !explicit && !unseen && !unknownDispatch {
		return false, ""
	}
	if unseenDetail != "" {
		return true, unseenDetail
	}
	if issue.Work.SendError != "" && (unknownDispatch || explicit) {
		return true, issue.Work.SendError
	}
	return true, ""
}

func issueDisplayStatusFor(issue *productIssue) issueDisplayStatus {
	if issue == nil {
		return issueDisplayStatus{Label: "Unknown"}
	}
	status := normalizeIssueStatus(issue.Status, issue.Executions)
	if issue.Archived {
		return issueDisplayStatus{Status: status, Label: "Archived", Archived: true}
	}
	switch status {
	case issueStatusCompleted, issueStatusCanceled:
		return issueDisplayStatus{Status: status, Label: issueStatusLabel(issue)}
	}
	attention, attentionReason := issueAttention(issue)
	if attention {
		return issueDisplayStatus{
			Status: issueStatusNeedsAttention, Label: "Needs attention",
			Reason: attentionReason, Attention: true, Active: issueWorkIsActive(issue),
		}
	}
	if issue.openQuestion() != nil || status == issueStatusWaitingUser {
		return issueDisplayStatus{
			Status: issueStatusWaitingUser, Label: "Waiting for you", Waiting: true,
			Active: issueWorkIsActive(issue),
		}
	}
	if issueWorkIsActive(issue) {
		return issueDisplayStatus{Status: issueStatusInProgress, Label: "In progress", Active: true}
	}
	if status == issueStatusInProgress {
		return issueDisplayStatus{Status: issueStatusInProgress, Label: "In progress"}
	}
	return issueDisplayStatus{Status: issueStatusNotStarted, Label: "Not started"}
}

type issueWorkNotice struct {
	AttentionReason string
	Attention       bool
	Waiting         bool
	Active          bool
	IssueID         string
	IssueName       string
	AttentionCount  int
	WaitingCount    int
	ActiveCount     int
}

func (a *appModel) issueWorkNotice(ct *contact) issueWorkNotice {
	type candidate struct {
		issueID   string
		issueName string
		attention bool
		waiting   bool
		active    bool
	}
	var attentionCandidate, waitingCandidate, activeCandidate *candidate
	notice := issueWorkNotice{}
	for _, issue := range ct.Issues {
		if issue == nil || issue.ContactID != ct.ID || issueManagementTerminal(issue) {
			continue
		}
		display := issueDisplayStatusFor(issue)
		if display.Attention {
			notice.AttentionCount++
			if attentionCandidate == nil {
				attentionCandidate = &candidate{issueID: issue.ID, issueName: issue.Goal, attention: true, active: display.Active}
				notice.AttentionReason = display.Reason
			}
		}
		if display.Waiting {
			notice.WaitingCount++
			if waitingCandidate == nil {
				waitingCandidate = &candidate{issueID: issue.ID, issueName: issue.Goal, waiting: true, active: display.Active}
			}
		}
		if display.Active {
			notice.ActiveCount++
			if activeCandidate == nil {
				activeCandidate = &candidate{issueID: issue.ID, issueName: issue.Goal, active: true}
			}
		}
	}
	winner := attentionCandidate
	if winner == nil {
		winner = waitingCandidate
	}
	if winner == nil {
		winner = activeCandidate
	}
	if winner != nil {
		notice.IssueID = winner.issueID
		notice.IssueName = winner.issueName
		notice.Attention = winner.attention
		notice.Waiting = winner.waiting
		notice.Active = winner.active
	}
	return notice
}

func issueExecutionNeedsAttention(status string) bool {
	switch status {
	case issueExecutionFailed, issueExecutionUnknown, issueExecutionInterrupted:
		return true
	default:
		return false
	}
}

func (ct *contact) issueForTask(taskID string) (*productIssue, *issueExecution) {
	for _, issue := range ct.Issues {
		for _, execution := range issue.Executions {
			if execution.TaskID == taskID {
				return issue, execution
			}
		}
	}
	return nil, nil
}

func issueWorkActiveCount(ct *contact) int {
	count := 0
	for _, issue := range ct.Issues {
		if issue != nil && issue.ContactID == ct.ID && !issueManagementTerminal(issue) && issueWorkIsActive(issue) {
			count++
		}
	}
	return count
}

func issueOpenCount(ct *contact) int {
	count := 0
	for _, issue := range ct.Issues {
		if issue != nil && issue.ContactID == ct.ID && !issueManagementTerminal(issue) {
			count++
		}
	}
	return count
}
