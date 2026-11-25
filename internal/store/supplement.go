package store

import (
	"slices"
)

const (
	MaxPendingSupplements     = 32
	MaxSupplementTextBytes    = 16 * 1024
	MaxPendingSupplementBytes = 64 * 1024
)

const (
	SupplementStatePending       = "pending"
	SupplementStateDispatching   = "dispatching"
	SupplementStateSubmitted     = "submitted"
	SupplementStateRejected      = "rejected"
	SupplementStateNotDispatched = "not_dispatched"
	SupplementStateUnknown       = "unknown"

	SupplementBatchDispatching   = "dispatching"
	SupplementBatchAccepted      = "accepted"
	SupplementBatchRejected      = "rejected"
	SupplementBatchNotDispatched = "not_dispatched"
	SupplementBatchUnknown       = "unknown"
	SupplementBatchCanceled      = "canceled"
)

type IssueSupplementTarget struct {
	ContactID            string `json:"contact_id"`
	IssueID              string `json:"issue_id"`
	RequirementsRevision int    `json:"requirements_revision"`
}

type IssueSupplement struct {
	ID                    string         `json:"id"`
	ContactID             string         `json:"contact_id"`
	IssueID               string         `json:"issue_id"`
	Text                  string         `json:"text"`
	RequirementsRevision  int            `json:"requirements_revision"`
	CreatedAt             int64          `json:"created_at"`
	State                 string         `json:"state"`
	BatchID               string         `json:"batch_id,omitempty"`
	Detail                string         `json:"detail,omitempty"`
	SessionID             string         `json:"session_id,omitempty"`
	TaskID                string         `json:"task_id,omitempty"`
	ExecutionID           string         `json:"execution_id,omitempty"`
	PiRun                 *PiActivityRun `json:"pi_run,omitempty"`
	PiQuestionOperationID string         `json:"pi_question_operation_id,omitempty"`
}

type IssueSupplementSnapshot struct {
	ID                   string `json:"id"`
	Text                 string `json:"text"`
	RequirementsRevision int    `json:"requirements_revision,omitempty"`
}

type IssueSupplementBatch struct {
	ID          string                    `json:"id"`
	Target      IssueSupplementTarget     `json:"target"`
	Snapshot    []IssueSupplementSnapshot `json:"snapshot"`
	Text        string                    `json:"text"`
	CreatedAt   int64                     `json:"created_at"`
	State       string                    `json:"state"`
	SessionID   string                    `json:"session_id,omitempty"`
	TaskID      string                    `json:"task_id,omitempty"`
	ExecutionID string                    `json:"execution_id,omitempty"`
	PiRun       *PiActivityRun            `json:"pi_run,omitempty"`
	SaveError   string                    `json:"save_error,omitempty"`
}

type IssueSupplementLedger struct {
	Items        []*IssueSupplement      `json:"items,omitempty"`
	Batches      []*IssueSupplementBatch `json:"batches,omitempty"`
	AutoContinue bool                    `json:"auto_continue"`
}

func NewIssueSupplementLedger() *IssueSupplementLedger {
	return &IssueSupplementLedger{AutoContinue: true}
}

func (l *IssueSupplementLedger) PendingCount() int {
	count := 0
	for _, item := range l.Items {
		if item != nil && item.State == SupplementStatePending && item.PiQuestionOperationID == "" {
			count++
		}
	}
	return count
}

func (l *IssueSupplementLedger) ItemByID(id string) *IssueSupplement {
	for _, item := range l.Items {
		if item != nil && item.ID == id {
			return item
		}
	}
	return nil
}

func CloneIssueSupplementLedger(ledger *IssueSupplementLedger) *IssueSupplementLedger {
	if ledger == nil {
		return nil
	}
	out := &IssueSupplementLedger{AutoContinue: ledger.AutoContinue}
	if len(ledger.Items) > 0 {
		out.Items = make([]*IssueSupplement, 0, len(ledger.Items))
		for _, item := range ledger.Items {
			if item == nil {
				out.Items = append(out.Items, nil)
				continue
			}
			copyItem := *item
			copyItem.PiRun = ClonePiActivityRun(item.PiRun)
			out.Items = append(out.Items, &copyItem)
		}
	}
	if len(ledger.Batches) > 0 {
		out.Batches = make([]*IssueSupplementBatch, 0, len(ledger.Batches))
		for _, batch := range ledger.Batches {
			if batch == nil {
				out.Batches = append(out.Batches, nil)
				continue
			}
			copyBatch := *batch
			copyBatch.PiRun = ClonePiActivityRun(batch.PiRun)
			copyBatch.Snapshot = slices.Clone(batch.Snapshot)
			out.Batches = append(out.Batches, &copyBatch)
		}
	}
	return out
}
