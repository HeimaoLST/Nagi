package store

// Issue supplement domain model, attached to issues and included in atomic
// store saves without any second execution loop or dispatcher.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
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

var (
	ErrSupplementSaveRequired = errors.New("supplement save is required before it can be received")
	ErrSupplementNoPending    = errors.New("no pending supplements for this issue")
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

type IssueSupplementAssociation struct {
	SessionID   string
	TaskID      string
	ExecutionID string
	PiRun       *PiActivityRun
}

type IssueSupplementLedger struct {
	Items        []*IssueSupplement      `json:"items,omitempty"`
	Batches      []*IssueSupplementBatch `json:"batches,omitempty"`
	AutoContinue bool                    `json:"auto_continue"`
}

func NewIssueSupplementID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "supplement-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("supplement-%d", time.Now().UnixNano())
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

func (l *IssueSupplementLedger) PendingBytes() int {
	total := 0
	for _, item := range l.Items {
		if item != nil && item.State == SupplementStatePending && item.PiQuestionOperationID == "" {
			total += len([]byte(item.Text))
		}
	}
	return total
}

// Add receives a supplement only after save succeeds. The callback is the app's
// existing one-shot store save; nil is rejected instead of calling in-memory
// data received.
func (l *IssueSupplementLedger) Add(target IssueSupplementTarget, text string, save func() error) (*IssueSupplement, error) {
	return l.add(target, text, nil, save)
}

// AddWith applies an answer/question association to the new item before the
// same one-shot store save. The caller's mutation must be rollback-safe; the
// item itself is removed if save fails.
func (l *IssueSupplementLedger) AddWith(target IssueSupplementTarget, text string, before func(*IssueSupplement), save func() error) (*IssueSupplement, error) {
	return l.add(target, text, before, save)
}

func (l *IssueSupplementLedger) add(target IssueSupplementTarget, text string, before func(*IssueSupplement), save func() error) (*IssueSupplement, error) {
	if err := validateSupplementTarget(target); err != nil {
		return nil, err
	}
	if save == nil {
		return nil, ErrSupplementSaveRequired
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("supplement text is required")
	}
	if len([]byte(text)) > MaxSupplementTextBytes {
		return nil, fmt.Errorf("supplement is too large: %d bytes (max %d)", len([]byte(text)), MaxSupplementTextBytes)
	}
	if l.PendingCount() >= MaxPendingSupplements {
		return nil, fmt.Errorf("too many pending supplements for this issue (max %d)", MaxPendingSupplements)
	}
	if l.PendingBytes()+len([]byte(text)) > MaxPendingSupplementBytes {
		return nil, fmt.Errorf("pending supplement text is too large (max %d)", MaxPendingSupplementBytes)
	}
	item := &IssueSupplement{
		ID: NewIssueSupplementID(), ContactID: target.ContactID, IssueID: target.IssueID,
		Text: text, RequirementsRevision: target.RequirementsRevision,
		CreatedAt: NowMillis(), State: SupplementStatePending,
	}
	l.Items = append(l.Items, item)
	if before != nil {
		before(item)
	}
	if err := save(); err != nil {
		l.Items = l.Items[:len(l.Items)-1]
		return nil, fmt.Errorf("could not save supplement: %w", err)
	}
	return item, nil
}

func validateSupplementTarget(target IssueSupplementTarget) error {
	if strings.TrimSpace(target.ContactID) == "" || strings.TrimSpace(target.IssueID) == "" {
		return errors.New("supplement target must include a Bot and an issue")
	}
	if target.RequirementsRevision < 0 {
		return errors.New("supplement target revision must not be negative")
	}
	return nil
}

func supplementTargetsSameIssue(a, b IssueSupplementTarget) bool {
	return a.ContactID == b.ContactID && a.IssueID == b.IssueID
}

func (l *IssueSupplementLedger) PendingFor(target IssueSupplementTarget) []*IssueSupplement {
	var out []*IssueSupplement
	for _, item := range l.Items {
		if item != nil && item.State == SupplementStatePending && item.PiQuestionOperationID == "" &&
			supplementTargetsSameIssue(IssueSupplementTarget{
				ContactID: item.ContactID, IssueID: item.IssueID,
			}, target) &&
			item.RequirementsRevision <= target.RequirementsRevision {
			out = append(out, item)
		}
	}
	return out
}

func (l *IssueSupplementLedger) PendingText(target IssueSupplementTarget) string {
	items := l.PendingFor(target)
	var texts []string
	for _, item := range items {
		if item != nil {
			texts = append(texts, item.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func (l *IssueSupplementLedger) ActiveBatch(target IssueSupplementTarget) *IssueSupplementBatch {
	for _, batch := range l.Batches {
		if batch == nil || !supplementTargetsSameIssue(batch.Target, target) {
			continue
		}
		switch batch.State {
		case SupplementBatchDispatching, SupplementBatchUnknown:
			return batch
		}
	}
	return nil
}

func (l *IssueSupplementLedger) snapshotBatchItems(batch *IssueSupplementBatch) map[string]IssueSupplement {
	items := map[string]IssueSupplement{}
	if batch == nil {
		return items
	}
	for _, snapshot := range batch.Snapshot {
		if item := l.ItemByID(snapshot.ID); item != nil {
			items[item.ID] = *item
		}
	}
	return items
}

func (l *IssueSupplementLedger) restoreBatchItems(batch *IssueSupplementBatch, old map[string]IssueSupplement) {
	if batch == nil {
		return
	}
	for _, snapshot := range batch.Snapshot {
		if item := l.ItemByID(snapshot.ID); item != nil {
			if previous, ok := old[item.ID]; ok {
				*item = previous
			}
		}
	}
}

// PrepareDispatch creates and durably stores a batch already at the dispatch
// boundary. The caller has appended the execution and set Work submitting
// before save; the same save therefore records the execution, batch, items and
// Work intent. It does not invoke Runtime or create an intermediate prepared batch.
func (l *IssueSupplementLedger) PrepareDispatch(target IssueSupplementTarget, execution *IssueExecution, save func() error) (*IssueSupplementBatch, error) {
	if err := validateSupplementTarget(target); err != nil {
		return nil, err
	}
	if save == nil {
		return nil, ErrSupplementSaveRequired
	}
	if execution == nil || strings.TrimSpace(execution.ID) == "" {
		return nil, errors.New("supplement dispatch requires an execution")
	}
	if active := l.ActiveBatch(target); active != nil {
		return nil, fmt.Errorf("a supplement batch is already %s for this issue", active.State)
	}
	items := l.PendingFor(target)
	if len(items) == 0 {
		return nil, ErrSupplementNoPending
	}
	batch := &IssueSupplementBatch{
		ID: NewIssueSupplementID(), Target: target, CreatedAt: NowMillis(), State: SupplementBatchDispatching,
		ExecutionID: execution.ID,
	}
	var texts []string
	for _, item := range items {
		batch.Snapshot = append(batch.Snapshot, IssueSupplementSnapshot{
			ID: item.ID, Text: item.Text, RequirementsRevision: item.RequirementsRevision,
		})
		texts = append(texts, item.Text)
		item.State = SupplementStateDispatching
		item.BatchID = batch.ID
	}
	batch.Text = strings.Join(texts, "\n")
	execution.SupplementBatchID = batch.ID
	l.Batches = append(l.Batches, batch)
	if err := save(); err != nil {
		execution.SupplementBatchID = ""
		l.Batches = l.Batches[:len(l.Batches)-1]
		for _, snapshot := range batch.Snapshot {
			if item := l.ItemByID(snapshot.ID); item != nil {
				item.State = SupplementStatePending
				item.BatchID = ""
			}
		}
		return nil, fmt.Errorf("could not save supplement dispatch: %w", err)
	}
	return batch, nil
}

// RetryUnaccepted is the explicit user retry for a batch already recorded as
// rejected or not dispatched. It returns only those exact snapshot items to
// pending and leaves the old batch as audit history. It must never be used for
// unknown, dispatching, accepted, or in-flight data.
func (l *IssueSupplementLedger) RetryUnaccepted(batch *IssueSupplementBatch, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil ||
		(canonical.State != SupplementBatchRejected && canonical.State != SupplementBatchNotDispatched) {
		return false
	}
	old := *canonical
	oldItems := l.snapshotBatchItems(canonical)
	for _, snapshot := range canonical.Snapshot {
		if item := l.ItemByID(snapshot.ID); item != nil {
			item.State = SupplementStatePending
			item.BatchID = ""
		}
	}
	canonical.State = SupplementBatchCanceled
	if err := save(); err != nil {
		*canonical = old
		l.restoreBatchItems(canonical, oldItems)
		return false
	}
	return true
}

// DeferDispatch is the local rollback for a dispatch that Runtime explicitly
// rejected as busy/capacity before accepting a task. The immutable batch stays
// as audit history, its items return to pending, and AutoContinue is unchanged
// so the next real capacity/idle signal can try once. It must not be used for
// accepted, rejected, or unknown dispatch results.
func (l *IssueSupplementLedger) DeferDispatch(batch *IssueSupplementBatch, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil || canonical.State != SupplementBatchDispatching {
		return false
	}
	old := *canonical
	oldItems := l.snapshotBatchItems(canonical)
	for _, snapshot := range canonical.Snapshot {
		if item := l.ItemByID(snapshot.ID); item != nil {
			item.State = SupplementStatePending
			item.BatchID = ""
		}
	}
	canonical.State = SupplementBatchCanceled
	if err := save(); err != nil {
		*canonical = old
		l.restoreBatchItems(canonical, oldItems)
		return false
	}
	return true
}

// Accept records the observed runtime association and does not imply issue completion.
// When save fails after runtime acceptance, the accepted snapshot and association
// are retained, automatic continuation is stopped, and the failure is returned.
func (l *IssueSupplementLedger) Accept(batch *IssueSupplementBatch, association IssueSupplementAssociation, save func() error) (bool, error) {
	canonical := l.canonicalBatch(batch)
	if canonical == nil || (canonical.State != SupplementBatchDispatching && canonical.State != SupplementBatchUnknown) {
		return false, nil
	}
	batch = canonical
	if save == nil {
		return false, ErrSupplementSaveRequired
	}
	piAccepted := association.PiRun != nil && association.PiRun.Admitted && association.PiRun.WorkerEpoch != "" && association.PiRun.ContextID != "" && association.PiRun.SessionRef != "" && association.PiRun.RunID != "" && (association.SessionID == "" || association.SessionID == association.PiRun.SessionRef) && (association.TaskID == "" || association.TaskID == association.PiRun.RunID)
	if strings.TrimSpace(association.ExecutionID) == "" || !piAccepted {
		return false, errors.New("accepted supplement requires an admitted Pi run and execution ID")
	}
	batch.State = SupplementBatchAccepted
	batch.SessionID, batch.TaskID, batch.ExecutionID = association.SessionID, association.TaskID, association.ExecutionID
	batch.PiRun = ClonePiActivityRun(association.PiRun)
	batch.SaveError = ""
	for _, snapshot := range batch.Snapshot {
		if item := l.ItemByID(snapshot.ID); item != nil {
			item.State = SupplementStateSubmitted
			item.SessionID, item.TaskID, item.ExecutionID = batch.SessionID, batch.TaskID, batch.ExecutionID
			item.PiRun = ClonePiActivityRun(batch.PiRun)
		}
	}
	if err := save(); err != nil {
		batch.SaveError = err.Error()
		l.AutoContinue = false
		return true, fmt.Errorf("runtime accepted supplement, but saving the association failed: %w", err)
	}
	return true, nil
}

func (l *IssueSupplementLedger) Reject(batch *IssueSupplementBatch, detail string, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil ||
		canonical.State != SupplementBatchDispatching {
		return false
	}
	batch = canonical
	old := *batch
	oldItems := l.snapshotBatchItems(batch)
	batch.State = SupplementBatchRejected
	for _, snapshot := range batch.Snapshot {
		if item := l.ItemByID(snapshot.ID); item != nil {
			item.State = SupplementStateRejected
			item.Detail = detail
		}
	}
	if err := save(); err != nil {
		*batch = old
		l.restoreBatchItems(batch, oldItems)
		return false
	}
	return true
}

func (l *IssueSupplementLedger) NotDispatched(batch *IssueSupplementBatch, detail string, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil ||
		(canonical.State != SupplementBatchDispatching && canonical.State != SupplementBatchUnknown) {
		return false
	}
	batch = canonical
	old := *batch
	oldItems := l.snapshotBatchItems(batch)
	batch.State = SupplementBatchNotDispatched
	for _, snapshot := range batch.Snapshot {
		if item := l.ItemByID(snapshot.ID); item != nil {
			item.State = SupplementStateNotDispatched
			item.Detail = detail
		}
	}
	if err := save(); err != nil {
		*batch = old
		l.restoreBatchItems(batch, oldItems)
		return false
	}
	return true
}

func (l *IssueSupplementLedger) Unknown(batch *IssueSupplementBatch, detail string, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil ||
		canonical.State != SupplementBatchDispatching {
		return false
	}
	batch = canonical
	old := *batch
	oldAuto, oldItems := l.AutoContinue, l.snapshotBatchItems(batch)
	batch.State = SupplementBatchUnknown
	l.AutoContinue = false
	for _, snapshot := range batch.Snapshot {
		if item := l.ItemByID(snapshot.ID); item != nil {
			item.State = SupplementStateUnknown
			item.Detail = detail
		}
	}
	if err := save(); err != nil {
		*batch = old
		l.AutoContinue = oldAuto
		l.restoreBatchItems(batch, oldItems)
		return false
	}
	return true
}

// StopAutoContinue revokes future scheduling only. Dispatching/unknown
// batches retain their identity so a late accepted response can still be
// recorded; it never turns them back into pending.
func (l *IssueSupplementLedger) StopAutoContinue(batch *IssueSupplementBatch, save func() error) bool {
	if save == nil {
		return false
	}
	if batch != nil {
		batch = l.canonicalBatch(batch)
		if batch == nil {
			return false
		}
	}
	oldAuto := l.AutoContinue
	oldBatch := IssueSupplementBatch{}
	var oldItems map[string]IssueSupplement
	l.AutoContinue = false
	if batch != nil {
		oldBatch = *batch
		oldItems = l.snapshotBatchItems(batch)
	}
	if err := save(); err != nil {
		l.AutoContinue = oldAuto
		if batch != nil {
			*batch = oldBatch
			l.restoreBatchItems(batch, oldItems)
		}
		return false
	}
	return true
}

func (l *IssueSupplementLedger) ResumeAutoContinue(save func() error) bool {
	if save == nil {
		return false
	}
	old := l.AutoContinue
	l.AutoContinue = true
	if err := save(); err != nil {
		l.AutoContinue = old
		return false
	}
	return true
}

func (l *IssueSupplementLedger) ItemByID(id string) *IssueSupplement {
	for _, item := range l.Items {
		if item != nil && item.ID == id {
			return item
		}
	}
	return nil
}

func (l *IssueSupplementLedger) BatchByID(id string) *IssueSupplementBatch {
	for _, batch := range l.Batches {
		if batch != nil && batch.ID == id {
			return batch
		}
	}
	return nil
}

func (l *IssueSupplementLedger) canonicalBatch(batch *IssueSupplementBatch) *IssueSupplementBatch {
	if batch == nil {
		return nil
	}
	canonical := l.BatchByID(batch.ID)
	if canonical != batch {
		return nil
	}
	return canonical
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
