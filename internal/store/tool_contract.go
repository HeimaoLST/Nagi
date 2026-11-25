package store

import (
	"errors"
	"strings"
)

const IssueToolActionCreate = "create"

const (
	IssueToolReceiptSaved         = "saved"
	IssueToolReceiptDispatching   = "dispatching"
	IssueToolReceiptAccepted      = "accepted"
	IssueToolReceiptStopRequested = "stop_requested"
	IssueToolReceiptUnknown       = "unknown"
)

var (
	ErrIssueToolScope             = errors.New("issue tool scope is invalid")
	ErrIssueToolReceiptIDsMissing = errors.New("accepted issue tool receipt lacks an admitted Pi run")
)

const IssueToolModeWork = "work"

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

func (l *IssueToolOperationLedger) OperationByID(id string) *IssueToolOperation {
	for _, operation := range l.Operations {
		if operation != nil && operation.ID == id {
			return operation
		}
	}
	return nil
}
