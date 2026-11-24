package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HeimaoLST/Nagi/locales"
)

// A productActivity is the GUI-owned product object. Runtime tasks are linked as
// execution associations; an execution terminal is not an item completion.
type productActivity struct {
	ID                   string
	ContactID            string
	Goal                 string
	Requirements         []*activityRequirement
	Status               string
	Archived             bool
	Evidence             string
	CreatedAt            int64
	UpdatedAt            int64
	Executions           []*activityExecution
	PiBinding            *piActivityBinding
	Handoff              *activityHandoff
	RequirementsRevision int

	// Context is the pending context for the next Run/Continue. Each execution
	// stores its own immutable Context snapshot before dispatch.
	Context taskContext

	// Supplements owns user supplements and their dispatch receipts for this
	// activity. It is persisted with the activity; it is not a Runtime history copy.
	Supplements *activitySupplementLedger

	// Questions are persistent missing-information prompts. An answer is linked
	// to the same supplement ledger, not to an invented assistant message.
	Questions []*activityQuestion

	// WorkSessionID is the activity-owned execution context. It is intentionally
	// separate from contact.SessionID (the ongoing conversation). Old
	// executions keep their actual SessionID and are never migrated in place.
	WorkSessionID string
	Work          activityWorkSession
}

func (activity *productActivity) openQuestion() *activityQuestion {
	if activity == nil {
		return nil
	}
	for index := len(activity.Questions) - 1; index >= 0; index-- {
		question := activity.Questions[index]
		if question != nil && question.State == activityQuestionOpen {
			return question
		}
	}
	return nil
}

func (activity *productActivity) questionByRef(ref string) *activityQuestion {
	if activity == nil {
		return nil
	}
	for _, question := range activity.Questions {
		if question != nil && question.Ref == ref {
			return question
		}
	}
	return nil
}

// activityWorkSession is transient state for the activity's Pi activity.
type activityWorkSession struct {
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

func activityRequirementsText(activity *productActivity) string {
	if activity == nil {
		return ""
	}
	parts := make([]string, 0, len(activity.Requirements))
	for _, requirement := range activity.Requirements {
		if requirement != nil && strings.TrimSpace(requirement.Text) != "" {
			parts = append(parts, requirement.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func normalizeActivityStatus(status string, executions []*activityExecution) string {
	switch status {
	case activityStatusNotStarted, activityStatusInProgress, activityStatusWaitingUser,
		activityStatusNeedsAttention, activityStatusCompleted, activityStatusCanceled:
		return status
	case "", "open":
		if len(executions) > 0 {
			return activityStatusInProgress
		}
		return activityStatusNotStarted
	default:
		return activityStatusNeedsAttention
	}
}

func activityStatusLabel(activity *productActivity) string {
	if activity == nil {
		return "Unknown"
	}
	if activity.Archived {
		return "Archived"
	}
	switch activity.Status {
	case activityStatusNotStarted:
		return "Not started"
	case activityStatusInProgress:
		return "In progress"
	case activityStatusWaitingUser:
		return "Waiting for you"
	case activityStatusNeedsAttention:
		return "Needs attention"
	case activityStatusCompleted:
		return "Completed"
	case activityStatusCanceled:
		return "Canceled"
	default:
		return "Needs attention"
	}
}

func newActivityExecutionID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "execution-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("execution-%d", time.Now().UnixNano())
}

func (ct *contact) activityByID(id string) *productActivity {
	for _, activity := range ct.Activities {
		if activity.ID == id {
			return activity
		}
	}
	return nil
}

func (ct *contact) selectedActivity() *productActivity {
	if ct.SelectedActivityID != "" {
		if activity := ct.activityByID(ct.SelectedActivityID); activity != nil {
			return activity
		}
	}
	for _, activity := range ct.Activities {
		if !activity.Archived && activity.Status != activityStatusCompleted && activity.Status != activityStatusCanceled {
			return activity
		}
	}
	if len(ct.Activities) > 0 {
		return ct.Activities[0]
	}
	return nil
}

func executionForTask(activity *productActivity, taskID string) *activityExecution {
	if activity == nil || taskID == "" {
		return nil
	}
	for _, execution := range activity.Executions {
		if execution != nil && execution.TaskID == taskID {
			return execution
		}
	}
	return nil
}

func (ct *contact) executionByTask(taskID string) *activityExecution {
	if taskID == "" {
		return nil
	}
	for _, activity := range ct.Activities {
		for _, execution := range activity.Executions {
			if execution.TaskID == taskID {
				return execution
			}
		}
	}
	return nil
}

func activityHasTask(activity *productActivity, taskID string) bool {
	if activity == nil || taskID == "" {
		return false
	}
	for _, execution := range activity.Executions {
		if execution.TaskID == taskID {
			return true
		}
	}
	return false
}

func activityExecutionForTask(activity *productActivity, taskID string) (int, *activityExecution) {
	if activity == nil {
		return 0, nil
	}
	for index, execution := range activity.Executions {
		if execution.TaskID == taskID {
			return index, execution
		}
	}
	return 0, nil
}

func (ct *contact) executionByID(id string) (*productActivity, *activityExecution) {
	for _, activity := range ct.Activities {
		for _, execution := range activity.Executions {
			if execution.ID == id {
				return activity, execution
			}
		}
	}
	return nil, nil
}

func activityPrompt(activity *productActivity) string {
	goal := strings.TrimSpace(activity.Goal)
	requirements := strings.TrimSpace(activityRequirementsText(activity))
	if requirements != "" {
		goal += "\n\nRequirements:\n" + requirements
	}
	if activity.Handoff != nil && len(activity.Handoff.Files) != 0 {
		goal += "\n\nFiles supplied for this activity:\n" + strings.Join(activity.Handoff.Files, "\n")
	}
	return goal
}

// createActivity persists an explicit product item and its original requirements.
// It does not dispatch any Runtime work; dispatch is a separate Run action.
func (a *appModel) setActivityFormNotice(raw, notice, detail string) {
	a.activityFormError = raw
	a.activityFormNotice = notice
	a.activityFormNoticeDetail = detail
}

func (a *appModel) clearActivityFormNotice() {
	a.activityFormError = ""
	a.activityFormNotice = ""
	a.activityFormNoticeDetail = ""
}

func (a *appModel) setActivityActionNotice(raw, notice, detail string) {
	a.activityActionError = raw
	a.activityActionNotice = notice
	a.activityActionNoticeDetail = detail
}

func (a *appModel) clearActivityActionNotice() {
	a.activityActionError = ""
	a.activityActionNotice = ""
	a.activityActionNoticeDetail = ""
}

func (a *appModel) createActivityFor(ct *contact, goal, requirements string) (*productActivity, error) {
	return a.createActivityForWithSave(ct, goal, requirements, nil)
}

func (a *appModel) createActivityForWithSave(ct *contact, goal, requirements string, save func(*productActivity) error) (*productActivity, error) {
	goal = strings.TrimSpace(goal)
	requirements = strings.TrimSpace(requirements)
	if goal == "" {
		return nil, errors.New("Enter an issue goal.")
	}
	if ct == nil {
		return nil, errors.New("Bot not found.")
	}
	now := nowMillis()
	item := &productActivity{
		ID: newActivityID(), ContactID: ct.ID, Goal: goal,
		Requirements: parseActivityRequirements(requirements), RequirementsRevision: 1,
		Status: activityStatusNotStarted, CreatedAt: now, UpdatedAt: now,
	}
	item.Context.OnChange = a.persist
	if item.Supplements == nil {
		item.Supplements = newActivitySupplementLedger()
	}
	previous := ct.Activities
	ct.Activities = append(previous, item)
	if save == nil {
		save = func(*productActivity) error { return a.persist() }
	}
	if err := save(item); err != nil {
		ct.Activities = previous
		return nil, fmt.Errorf("Could not save this issue: %w", err)
	}
	return item, nil
}

func activityHasRemainingRequirements(activity *productActivity) bool {
	if activity == nil {
		return false
	}
	for _, requirement := range activity.Requirements {
		if requirement != nil && requirement.State != requirementSatisfied {
			return true
		}
	}
	return false
}

const (
	activityBlockNoActivity = "no-issue"
	activityBlockArchived   = "archived"
	activityBlockQuestion   = "question"
	activityBlockCompleted  = "completed"
	activityBlockCanceled   = "canceled"
	activityBlockExecution  = "execution-untrusted"
)

func activityBlockRaw(code string) string {
	switch code {
	case activityBlockNoActivity:
		return "Issue not found."
	case activityBlockArchived:
		return "Restore this archived issue before starting work."
	case activityBlockQuestion:
		return "Answer the waiting question before continuing this issue."
	case activityBlockCompleted:
		return "Reopen this completed issue before starting another execution."
	case activityBlockCanceled:
		return "Reopen this canceled issue before starting another execution."
	case activityBlockExecution:
		return "An execution is still active or has no trusted terminal state; reload before running this issue again."
	default:
		return sendBlockRaw(code)
	}
}

func activityNoticeText(msgs locales.Messages, notice, detail string) string {
	switch notice {
	case activityBlockNoActivity:
		return msgs.IssueBlockNoIssue()
	case activityBlockArchived:
		return msgs.IssueBlockArchived()
	case activityBlockQuestion:
		return msgs.IssueBlockQuestion()
	case activityBlockCompleted:
		return msgs.IssueBlockCompleted()
	case activityBlockCanceled:
		return msgs.IssueBlockCanceled()
	case activityBlockExecution:
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

func activityExecutionText(msgs locales.Messages, status string) string {
	switch status {
	case activityExecutionPending:
		return msgs.IssueExecutionPending()
	case activityExecutionNotDispatched:
		return msgs.IssueExecutionNotDispatched()
	case activityExecutionRejected:
		return msgs.IssueExecutionRejected()
	case activityExecutionAccepted:
		return msgs.IssueExecutionAccepted()
	case activityExecutionRunning:
		return msgs.IssueExecutionRunning()
	case activityExecutionCompleted:
		return msgs.IssueExecutionCompleted()
	case activityExecutionFailed:
		return msgs.IssueExecutionFailed()
	case activityExecutionInterrupted:
		return msgs.IssueExecutionInterrupted()
	case activityExecutionUnknown:
		return msgs.IssueExecutionUnknown()
	default:
		return status
	}
}

func activitiesForStoredContact(all []storedActivity, contactID string) []*productActivity {
	out := make([]*productActivity, 0)
	for _, stored := range all {
		if stored.ContactID != contactID || stored.ID == "" {
			continue
		}
		activity := &productActivity{
			ID: stored.ID, ContactID: contactID, Goal: stored.Goal,
			Archived: stored.Archived, RequirementsRevision: stored.RequirementsRevision,
			Evidence: stored.Evidence, CreatedAt: stored.CreatedAt, UpdatedAt: stored.UpdatedAt,
			PiBinding:     clonePiActivityBinding(stored.PiBinding),
			Handoff:       cloneActivityHandoff(stored.Handoff),
			WorkSessionID: stored.WorkSessionID,
			Context:       taskContext{Items: cloneTaskContextItems(stored.TaskContext)},
			Supplements:   cloneActivitySupplementLedger(stored.Supplements),
		}
		for _, question := range stored.Questions {
			if question == nil || strings.TrimSpace(question.Ref) == "" {
				continue
			}
			copy := *question
			copy.PiCall = clonePiQuestionCall(question.PiCall)
			copy.AnswerContext = cloneTaskContextItems(question.AnswerContext)
			if copy.DeliveryState == activityQuestionDeliveryDispatching {
				copy.DeliveryState = activityQuestionDeliveryUnknown
			}
			activity.Questions = append(activity.Questions, &copy)
		}
		for _, item := range stored.RequirementItems {
			if strings.TrimSpace(item.Text) == "" {
				continue
			}
			state := item.State
			if state != requirementSatisfied && state != requirementUnmet {
				state = requirementUnverified
			}
			activity.Requirements = append(activity.Requirements, &activityRequirement{
				ID: item.ID, Text: item.Text, State: state, Evidence: item.Evidence, UpdatedAt: item.UpdatedAt,
			})
		}
		if activity.RequirementsRevision < 1 {
			activity.RequirementsRevision = 1
		}
		for _, execution := range stored.Executions {
			if execution.ID == "" {
				continue
			}
			status := execution.Status
			if status == "" {
				status = activityExecutionUnknown
			}
			detail := execution.Detail
			if status == activityExecutionPending || status == activityExecutionAccepted || status == activityExecutionRunning {
				status = activityExecutionUnknown
				detail = "The previous Pi run is unavailable; its outcome is unknown."
			}
			activity.Executions = append(activity.Executions, &activityExecution{
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
		activity.Status = normalizeActivityStatus(stored.Status, activity.Executions)
		out = append(out, activity)
	}
	return out
}

func unresolvedExecution(execution *activityExecution) bool {
	switch execution.Status {
	case activityExecutionPending, activityExecutionAccepted, activityExecutionRunning, activityExecutionUnknown:
		return true
	default:
		return false
	}
}

func activityRunBlocked(activity *productActivity) string {
	for _, execution := range activity.Executions {
		if unresolvedExecution(execution) {
			return activityBlockExecution
		}
	}
	return ""
}

// activityDisplayStatus is the single read-only display projection for an activity.
type activityDisplayStatus struct {
	Status    string
	Label     string
	Reason    string
	Attention bool
	Waiting   bool
	Active    bool
	Archived  bool
}

func activityWorkIsActive(activity *productActivity) bool {
	return activity != nil && activity.Work.SendState != sendUnknown &&
		(activity.Work.ActiveTaskID != "" || activity.Work.SendState == sendSubmitting || activity.Work.SendState == sendAccepted)
}

func activityManagementTerminal(activity *productActivity) bool {
	return activity != nil && (activity.Archived ||
		activity.Status == activityStatusCompleted || activity.Status == activityStatusCanceled)
}

func activityAttention(activity *productActivity) (bool, string) {
	if activity == nil {
		return false, ""
	}
	// An accepted task can retain its active mapping when clearing the accepted
	// context or receipt fails; that current send error remains actionable.
	if activityWorkIsActive(activity) && activity.Work.SendError != "" {
		return true, activity.Work.SendError
	}
	explicit := activity.Status == activityStatusNeedsAttention
	unseen := false
	unseenDetail := ""
	for index := len(activity.Executions) - 1; index >= 0; index-- {
		execution := activity.Executions[index]
		if execution == nil || !activityExecutionNeedsAttention(execution.Status) ||
			execution.AttentionSeenStatus == execution.Status {
			continue
		}
		unseen = true
		if unseenDetail == "" && execution.Detail != "" {
			unseenDetail = execution.Detail
		}
	}
	unknownDispatch := false
	if activity.Work.SendState == sendUnknown {
		execution := (*activityExecution)(nil)
		if id := activity.Work.pendingExecutionID; id != "" {
			for _, candidate := range activity.Executions {
				if candidate != nil && candidate.ID == id {
					execution = candidate
					break
				}
			}
		}
		if execution == nil {
			taskID := activity.Work.ActiveTaskID
			if taskID == "" {
				taskID = activity.Work.LastTaskID
			}
			execution = executionForTask(activity, taskID)
		}
		unknownDispatch = execution == nil || execution.AttentionSeenStatus == "" ||
			(execution.AttentionSeenStatus != execution.Status && activityExecutionNeedsAttention(execution.Status))
	}
	if !explicit && !unseen && !unknownDispatch {
		return false, ""
	}
	if unseenDetail != "" {
		return true, unseenDetail
	}
	if activity.Work.SendError != "" && (unknownDispatch || explicit) {
		return true, activity.Work.SendError
	}
	return true, ""
}

func activityDisplayStatusFor(activity *productActivity) activityDisplayStatus {
	if activity == nil {
		return activityDisplayStatus{Label: "Unknown"}
	}
	status := normalizeActivityStatus(activity.Status, activity.Executions)
	if activity.Archived {
		return activityDisplayStatus{Status: status, Label: "Archived", Archived: true}
	}
	switch status {
	case activityStatusCompleted, activityStatusCanceled:
		return activityDisplayStatus{Status: status, Label: activityStatusLabel(activity)}
	}
	attention, attentionReason := activityAttention(activity)
	if attention {
		return activityDisplayStatus{
			Status: activityStatusNeedsAttention, Label: "Needs attention",
			Reason: attentionReason, Attention: true, Active: activityWorkIsActive(activity),
		}
	}
	if activity.openQuestion() != nil || status == activityStatusWaitingUser {
		return activityDisplayStatus{
			Status: activityStatusWaitingUser, Label: "Waiting for you", Waiting: true,
			Active: activityWorkIsActive(activity),
		}
	}
	if activityWorkIsActive(activity) {
		return activityDisplayStatus{Status: activityStatusInProgress, Label: "In progress", Active: true}
	}
	if status == activityStatusInProgress {
		return activityDisplayStatus{Status: activityStatusInProgress, Label: "In progress"}
	}
	return activityDisplayStatus{Status: activityStatusNotStarted, Label: "Not started"}
}

type activityWorkNotice struct {
	AttentionReason string
	Attention       bool
	Waiting         bool
	Active          bool
	ActivityID      string
	ActivityName    string
	AttentionCount  int
	WaitingCount    int
	ActiveCount     int
}

func (a *appModel) activityWorkNotice(ct *contact) activityWorkNotice {
	type candidate struct {
		activityID   string
		activityName string
		attention    bool
		waiting      bool
		active       bool
	}
	var attentionCandidate, waitingCandidate, activeCandidate *candidate
	notice := activityWorkNotice{}
	for _, activity := range ct.Activities {
		if activity == nil || activity.ContactID != ct.ID || activityManagementTerminal(activity) {
			continue
		}
		display := activityDisplayStatusFor(activity)
		if display.Attention {
			notice.AttentionCount++
			if attentionCandidate == nil {
				attentionCandidate = &candidate{activityID: activity.ID, activityName: activity.Goal, attention: true, active: display.Active}
				notice.AttentionReason = display.Reason
			}
		}
		if display.Waiting {
			notice.WaitingCount++
			if waitingCandidate == nil {
				waitingCandidate = &candidate{activityID: activity.ID, activityName: activity.Goal, waiting: true, active: display.Active}
			}
		}
		if display.Active {
			notice.ActiveCount++
			if activeCandidate == nil {
				activeCandidate = &candidate{activityID: activity.ID, activityName: activity.Goal, active: true}
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
		notice.ActivityID = winner.activityID
		notice.ActivityName = winner.activityName
		notice.Attention = winner.attention
		notice.Waiting = winner.waiting
		notice.Active = winner.active
	}
	return notice
}

func activityExecutionNeedsAttention(status string) bool {
	switch status {
	case activityExecutionFailed, activityExecutionUnknown, activityExecutionInterrupted:
		return true
	default:
		return false
	}
}

func (ct *contact) activityForTask(taskID string) (*productActivity, *activityExecution) {
	for _, activity := range ct.Activities {
		for _, execution := range activity.Executions {
			if execution.TaskID == taskID {
				return activity, execution
			}
		}
	}
	return nil, nil
}

func activityWorkActiveCount(ct *contact) int {
	count := 0
	for _, activity := range ct.Activities {
		if activity != nil && activity.ContactID == ct.ID && !activityManagementTerminal(activity) && activityWorkIsActive(activity) {
			count++
		}
	}
	return count
}

func activityOpenCount(ct *contact) int {
	count := 0
	for _, activity := range ct.Activities {
		if activity != nil && activity.ContactID == ct.ID && !activityManagementTerminal(activity) {
			count++
		}
	}
	return count
}
