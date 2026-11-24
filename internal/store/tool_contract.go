package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	IssueToolActionCreate        = "create"
	IssueToolActionUpdate        = "update"
	IssueToolActionRun           = "run"
	IssueToolActionStop          = "stop"
	IssueToolActionAddSupplement = "add_supplement"
	IssueToolActionWait          = "wait"
	IssueToolActionResume        = "resume"
)

const (
	IssueToolReceiptSaved         = "saved"
	IssueToolReceiptDispatching   = "dispatching"
	IssueToolReceiptAccepted      = "accepted"
	IssueToolReceiptStopRequested = "stop_requested"
	IssueToolReceiptUnknown       = "unknown"
)

const IssueToolExpectedRevisionCorrection = "expected_revision must be a positive integer copied from the issue's current requirements_revision (from the create receipt or issue_get); do not guess"

var (
	ErrIssueToolScope             = errors.New("issue tool scope is invalid")
	ErrIssueToolAction            = errors.New("issue tool action is invalid")
	ErrIssueToolIntentConflict    = errors.New("issue tool intent already has different payload")
	ErrIssueToolSaveRequired      = errors.New("issue tool operation save is required")
	ErrIssueToolReceiptIDsMissing = errors.New("accepted issue tool receipt lacks an admitted Pi run")
)

const (
	IssueToolModeConversation = "conversation"
	IssueToolModeWork         = "work"
)

// IssueToolScope is issued before a Runtime task exists. TurnID is the stable
// app-issued user-turn/intent identity; RuntimeTaskID may be attached later and
// is never required to create the scope.
type IssueToolScope struct {
	Ref              string   `json:"ref"`
	Mode             string   `json:"mode,omitempty"`
	ContactID        string   `json:"contact_id"`
	RuntimeSessionID string   `json:"runtime_session_id,omitempty"`
	TurnID           string   `json:"turn_id"`
	AllowedIssueIDs  []string `json:"allowed_issue_ids,omitempty"`
	AllowCreate      bool     `json:"allow_create,omitempty"`
	RuntimeTaskID    string   `json:"runtime_task_id,omitempty"`
	WorkIssueID      string   `json:"work_issue_id,omitempty"`
	WorkExecutionID  string   `json:"work_execution_id,omitempty"`
	CreatedAt        int64    `json:"created_at"`
}

func (s IssueToolScope) Validate() error {
	if strings.TrimSpace(s.Ref) == "" || strings.TrimSpace(s.ContactID) == "" ||
		strings.TrimSpace(s.RuntimeSessionID) == "" || strings.TrimSpace(s.TurnID) == "" {
		return ErrIssueToolScope
	}
	for _, id := range s.AllowedIssueIDs {
		if strings.TrimSpace(id) == "" {
			return ErrIssueToolScope
		}
	}
	return nil
}

func (s IssueToolScope) AllowsIssueID(issueID string) bool {
	for _, id := range s.AllowedIssueIDs {
		if id == issueID {
			return true
		}
	}
	return false
}

type IssueToolError struct {
	Code            string   `json:"code"`
	Message         string   `json:"message"`
	Candidates      []string `json:"candidates,omitempty"`
	CurrentRevision *int     `json:"current_revision,omitempty"`
}

func (e *IssueToolError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

type IssueToolActionRequest struct {
	ScopeRef         string   `json:"scope"`
	Action           string   `json:"action"`
	IssueID          string   `json:"issue_id,omitempty"`
	ExpectedRevision *int     `json:"expected_revision,omitempty"`
	Goal             string   `json:"goal,omitempty"`
	CreateKey        string   `json:"create_key,omitempty"`
	AssigneeID       string   `json:"assignee_id,omitempty"`
	Files            []string `json:"files,omitempty"`
	Requirements     []string `json:"requirements,omitempty"`
	Text             string   `json:"text,omitempty"`
	QuestionRef      string   `json:"question_ref,omitempty"`
}

func (r IssueToolActionRequest) Validate(scope IssueToolScope) error {
	if err := scope.Validate(); err != nil || r.ScopeRef != scope.Ref {
		return ErrIssueToolScope
	}
	if r.CreateKey != "" && (r.Action != IssueToolActionCreate || strings.TrimSpace(r.CreateKey) != r.CreateKey || len(r.CreateKey) > 128 || strings.ContainsRune(r.CreateKey, 0)) {
		return fmt.Errorf("%w: create_key must be a nonempty trimmed key of at most 128 bytes, usable only with create", ErrIssueToolAction)
	}
	if r.AssigneeID != "" && (r.Action != IssueToolActionCreate || r.CreateKey == "" || r.AssigneeID == scope.ContactID) ||
		len(r.Files) != 0 && (r.AssigneeID == "" || !ValidHandoffFiles(r.Files)) {
		return fmt.Errorf("%w: a handoff requires another Bot, a create_key and valid absolute file references", ErrIssueToolAction)
	}
	switch r.Action {
	case IssueToolActionCreate:
		if strings.TrimSpace(r.IssueID) != "" || strings.TrimSpace(r.Goal) == "" || !scope.AllowCreate {
			return ErrIssueToolAction
		}
	case IssueToolActionUpdate:
		if strings.TrimSpace(r.IssueID) == "" || strings.TrimSpace(r.Goal) == "" {
			return ErrIssueToolAction
		}
		if r.ExpectedRevision == nil || *r.ExpectedRevision < 1 {
			return fmt.Errorf("%w: update: %s", ErrIssueToolAction, IssueToolExpectedRevisionCorrection)
		}
	case IssueToolActionRun, IssueToolActionStop:
		if strings.TrimSpace(r.IssueID) == "" {
			return ErrIssueToolAction
		}
	case IssueToolActionWait:
		if strings.TrimSpace(r.IssueID) == "" || strings.TrimSpace(r.Text) == "" {
			return ErrIssueToolAction
		}
		if r.ExpectedRevision == nil || *r.ExpectedRevision < 1 {
			return fmt.Errorf("%w: wait: %s", ErrIssueToolAction, IssueToolExpectedRevisionCorrection)
		}
	case IssueToolActionResume:
		if strings.TrimSpace(r.IssueID) == "" {
			return ErrIssueToolAction
		}
	case IssueToolActionAddSupplement:
		if strings.TrimSpace(r.IssueID) == "" || strings.TrimSpace(r.Text) == "" {
			return ErrIssueToolAction
		}
	default:
		return ErrIssueToolAction
	}
	if r.Action != IssueToolActionCreate && !scope.AllowsIssueID(r.IssueID) {
		return &IssueToolError{Code: "scope_denied", Message: "issue is outside the issued scope"}
	}
	return nil
}

func (r IssueToolActionRequest) TargetIssueID() string {
	if r.Action == IssueToolActionCreate {
		return ""
	}
	return r.IssueID
}

// issueToolCanonicalPayload is the persisted payload tied to one intent slot.
// It is intentionally independent from the operation ID.
func issueToolCanonicalPayload(r IssueToolActionRequest) (string, error) {
	payload := struct {
		Action           string   `json:"action"`
		IssueID          string   `json:"issue_id,omitempty"`
		ExpectedRevision *int     `json:"expected_revision,omitempty"`
		Goal             string   `json:"goal,omitempty"`
		CreateKey        string   `json:"create_key,omitempty"`
		AssigneeID       string   `json:"assignee_id,omitempty"`
		Files            []string `json:"files,omitempty"`
		Requirements     []string `json:"requirements,omitempty"`
		Text             string   `json:"text,omitempty"`
		QuestionRef      string   `json:"question_ref,omitempty"`
	}{
		Action: r.Action, IssueID: r.IssueID, ExpectedRevision: r.ExpectedRevision, CreateKey: r.CreateKey,
		AssigneeID: r.AssigneeID, Files: r.Files,
		Goal: strings.TrimSpace(r.Goal), Requirements: append([]string(nil), r.Requirements...),
		Text: strings.TrimSpace(r.Text), QuestionRef: strings.TrimSpace(r.QuestionRef),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func issueToolIntentSlot(scope IssueToolScope, r IssueToolActionRequest) string {
	return issueToolSlot(scope.Ref, scope.TurnID, r.Action, r.TargetIssueID(), r.CreateKey)
}

func issueToolSlot(ref, turn, action, issueID, createKey string) string {
	slot := ref + "\x00" + turn + "\x00" + action + "\x00" + issueID
	if createKey != "" {
		return slot + "\x00" + createKey
	}
	return slot
}

// IssueToolReceipt is a projection of the existing issue/execution/ledger
// facts. It is not a second business state machine.
type IssueToolReceipt struct {
	State                string          `json:"state"`
	IssueID              string          `json:"issue_id,omitempty"`
	ExecutionID          string          `json:"execution_id,omitempty"`
	SessionID            string          `json:"session_id,omitempty"`
	TaskID               string          `json:"task_id,omitempty"`
	PiRun                *PiActivityRun  `json:"pi_run,omitempty"`
	RequirementsRevision *int            `json:"requirements_revision,omitempty"`
	QuestionRef          string          `json:"question_ref,omitempty"`
	QuestionState        string          `json:"question_state,omitempty"`
	IssueStatus          string          `json:"issue_status,omitempty"`
	SupplementID         string          `json:"supplement_id,omitempty"`
	Error                *IssueToolError `json:"error,omitempty"`
}

func ValidateIssueToolReceipt(receipt *IssueToolReceipt) error {
	if receipt == nil {
		return nil
	}
	switch receipt.State {
	case IssueToolReceiptSaved:
		if strings.TrimSpace(receipt.IssueID) == "" {
			return errors.New("saved receipt requires issue_id")
		}
	case IssueToolReceiptDispatching:
		if strings.TrimSpace(receipt.IssueID) == "" || strings.TrimSpace(receipt.ExecutionID) == "" {
			return errors.New("dispatching receipt requires issue_id and execution_id")
		}
	case IssueToolReceiptAccepted:
		piAccepted := receipt.PiRun != nil && receipt.PiRun.Admitted && receipt.PiRun.WorkerEpoch != "" && receipt.PiRun.ContextID != "" && receipt.PiRun.SessionRef != "" && receipt.PiRun.RunID != "" && (receipt.SessionID == "" || receipt.SessionID == receipt.PiRun.SessionRef) && (receipt.TaskID == "" || receipt.TaskID == receipt.PiRun.RunID)
		if strings.TrimSpace(receipt.IssueID) == "" || strings.TrimSpace(receipt.ExecutionID) == "" || !piAccepted {
			return ErrIssueToolReceiptIDsMissing
		}
	case IssueToolReceiptStopRequested:
		if strings.TrimSpace(receipt.IssueID) == "" || strings.TrimSpace(receipt.ExecutionID) == "" || strings.TrimSpace(receipt.SessionID) == "" ||
			strings.TrimSpace(receipt.TaskID) == "" {
			return errors.New("stop_requested receipt requires real execution/session/task ids")
		}
	case IssueToolReceiptUnknown:
	default:
		return errors.New("unknown issue tool receipt state")
	}
	return nil
}

// IssueToolOperation is an application-issued operation record. It stores only
// the canonical payload and a receipt snapshot; issue/execution facts remain
// authoritative.
type IssueToolOperation struct {
	ID            string            `json:"id"`
	ScopeRef      string            `json:"scope_ref"`
	TurnID        string            `json:"turn_id"`
	Action        string            `json:"action"`
	TargetIssueID string            `json:"target_issue_id,omitempty"`
	CreateKey     string            `json:"create_key,omitempty"`
	Payload       string            `json:"payload"`
	PiCall        *PiQuestionCall   `json:"pi_call,omitempty"`
	Receipt       *IssueToolReceipt `json:"receipt,omitempty"`
	CreatedAt     int64             `json:"created_at"`
	UpdatedAt     int64             `json:"updated_at"`
}

type IssueToolOperationLedger struct {
	Operations []*IssueToolOperation `json:"operations,omitempty"`
}

func newIssueToolOperationID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "issue-op-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("issue-op-%d", time.Now().UnixNano())
}

func (l *IssueToolOperationLedger) operationFor(slot, payload string) (*IssueToolOperation, bool, error) {
	for _, operation := range l.Operations {
		if operation == nil || issueToolOperationSlot(operation) != slot {
			continue
		}
		if operation.Payload != payload {
			return nil, true, ErrIssueToolIntentConflict
		}
		return operation, true, nil
	}
	return nil, false, nil
}

func issueToolOperationSlot(operation *IssueToolOperation) string {
	if operation == nil {
		return ""
	}
	return issueToolSlot(operation.ScopeRef, operation.TurnID, operation.Action, operation.TargetIssueID, operation.CreateKey)
}

// Reserve atomically records the app-issued operation before the caller runs a
// business side effect. A duplicate same slot/payload returns the existing
// operation; a different payload is a conflict. Save failure removes the new
// reservation, so the caller must not execute the side effect.
func (l *IssueToolOperationLedger) Reserve(scope IssueToolScope, request IssueToolActionRequest, save func() error) (*IssueToolOperation, bool, error) {
	if err := request.Validate(scope); err != nil {
		return nil, false, err
	}
	if save == nil {
		return nil, false, ErrIssueToolSaveRequired
	}
	payload, err := issueToolCanonicalPayload(request)
	if err != nil {
		return nil, false, err
	}
	slot := issueToolIntentSlot(scope, request)
	if existing, found, err := l.operationFor(slot, payload); found {
		return existing, false, err
	}
	now := time.Now().UnixMilli()
	operation := &IssueToolOperation{
		ID: newIssueToolOperationID(), ScopeRef: scope.Ref, TurnID: scope.TurnID,
		Action: request.Action, TargetIssueID: request.TargetIssueID(), CreateKey: request.CreateKey,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	}
	l.Operations = append(l.Operations, operation)
	if err := save(); err != nil {
		l.Operations = l.Operations[:len(l.Operations)-1]
		return nil, false, fmt.Errorf("save issue tool operation: %w", err)
	}
	return operation, true, nil
}

func (l *IssueToolOperationLedger) OperationByID(id string) *IssueToolOperation {
	for _, operation := range l.Operations {
		if operation != nil && operation.ID == id {
			return operation
		}
	}
	return nil
}

func (l *IssueToolOperationLedger) SetReceipt(operationID string, receipt *IssueToolReceipt, save func() error) error {
	operation := l.OperationByID(operationID)
	if operation == nil || save == nil {
		return errors.New("issue tool operation is not in this ledger or save is missing")
	}
	if err := ValidateIssueToolReceipt(receipt); err != nil {
		return err
	}
	if operation.Action != IssueToolActionCreate && receipt != nil &&
		receipt.IssueID != operation.TargetIssueID {
		return &IssueToolError{Code: "target_mismatch", Message: "receipt issue does not match the operation target"}
	}
	old := operation.Receipt
	oldUpdated := operation.UpdatedAt
	operation.Receipt = receipt
	operation.UpdatedAt = time.Now().UnixMilli()
	if err := save(); err != nil {
		operation.Receipt = old
		operation.UpdatedAt = oldUpdated
		return fmt.Errorf("save issue tool receipt: %w", err)
	}
	return nil
}

type IssueToolIssueListResult struct {
	Issues     []IssueToolIssueSummary `json:"issues"`
	HasMore    bool                    `json:"has_more"`
	NextCursor string                  `json:"next_cursor,omitempty"`
}

type IssueToolIssueSummary struct {
	ID                      string `json:"id"`
	Goal                    string `json:"goal"`
	Status                  string `json:"status"`
	Archived                bool   `json:"archived"`
	RequirementsRevision    int    `json:"requirements_revision"`
	ActiveExecutionID       string `json:"active_execution_id,omitempty"`
	LastExecutionID         string `json:"last_execution_id,omitempty"`
	LastExecutionState      string `json:"last_execution_state,omitempty"`
	PendingSupplements      int    `json:"pending_supplements"`
	QuestionRef             string `json:"question_ref,omitempty"`
	Question                string `json:"question,omitempty"`
	QuestionRevision        int    `json:"question_revision,omitempty"`
	OwnerBotID              string `json:"owner_bot_id,omitempty"`
	SourceBotID             string `json:"source_bot_id,omitempty"`
	LastReturnedExecutionID string `json:"last_returned_execution_id,omitempty"`
}

type IssueToolRequirementView struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	State    string `json:"state"`
	Evidence string `json:"evidence,omitempty"`
}

type IssueToolExecutionView struct {
	ID                   string         `json:"id"`
	SessionID            string         `json:"session_id,omitempty"`
	TaskID               string         `json:"task_id,omitempty"`
	PiRun                *PiActivityRun `json:"pi_run,omitempty"`
	Status               string         `json:"status"`
	Detail               string         `json:"detail,omitempty"`
	RequirementsRevision int            `json:"requirements_revision"`
	ReturnedAt           int64          `json:"returned_at,omitempty"`
}

const IssueToolExecutionPageLimit = 20

type IssueToolResultFileView struct {
	ExecutionID string `json:"execution_id"`
	CallID      string `json:"call_id"`
	ToolName    string `json:"tool_name"`
	Path        string `json:"path"`
}

type IssueToolPiProgress struct {
	ContextID       string `json:"context_id"`
	RunID           string `json:"run_id"`
	Active          bool   `json:"active"`
	CancelRequested bool   `json:"cancel_requested,omitempty"`
}

type IssueToolIssueDetail struct {
	Issue               IssueToolIssueSummary      `json:"issue"`
	Requirements        []IssueToolRequirementView `json:"requirements,omitempty"`
	RequirementCount    int                        `json:"requirement_count"`
	RequirementsHasMore bool                       `json:"requirements_has_more,omitempty"`
	Executions          []IssueToolExecutionView   `json:"executions,omitempty"`
	ExecutionCount      int                        `json:"execution_count"`
	ExecutionsHasMore   bool                       `json:"executions_has_more"`
	PiProgress          *IssueToolPiProgress       `json:"pi_progress,omitempty"`

	// Result facts come from the selected execution's typed Pi history.
	ResultExecutionID    string                    `json:"result_execution_id,omitempty"`
	ResultFileCount      int                       `json:"result_file_count"`
	ResultFiles          []IssueToolResultFileView `json:"result_files,omitempty"`
	ResultFilesHasMore   bool                      `json:"result_files_has_more,omitempty"`
	ResultExcerpt        string                    `json:"result_excerpt,omitempty"`
	ResultExcerptHasMore bool                      `json:"result_excerpt_has_more,omitempty"`
	ResultUnavailable    string                    `json:"result_unavailable,omitempty"`
}
