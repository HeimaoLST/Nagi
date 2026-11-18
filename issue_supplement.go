package main

// Issue supplement domain model. This is deliberately independent of the GUI
// store and Runtime client. The app will attach the ledger to productIssue and
// include it in the same atomic store save; this file has no dispatcher, queue
// worker, or second execution loop.

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
	maxPendingSupplements     = 32
	maxSupplementTextBytes    = 16 * 1024
	maxPendingSupplementBytes = 64 * 1024
)

const (
	supplementStatePending       = "pending"
	supplementStateDispatching   = "dispatching"
	supplementStateSubmitted     = "submitted"
	supplementStateRejected      = "rejected"
	supplementStateNotDispatched = "not_dispatched"
	supplementStateUnknown       = "unknown"

	supplementBatchDispatching   = "dispatching"
	supplementBatchAccepted      = "accepted"
	supplementBatchRejected      = "rejected"
	supplementBatchNotDispatched = "not_dispatched"
	supplementBatchUnknown       = "unknown"
	supplementBatchCanceled      = "canceled"
)

var (
	errSupplementSaveRequired = errors.New("supplement save is required before it can be received")
	errSupplementNoPending    = errors.New("no pending supplements for this issue")
)

type issueSupplementTarget struct {
	ContactID            string `json:"contact_id"`
	IssueID              string `json:"issue_id"`
	RequirementsRevision int    `json:"requirements_revision"`
}

type issueSupplement struct {
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
	PiRun                 *piActivityRun `json:"pi_run,omitempty"`
	PiQuestionOperationID string         `json:"pi_question_operation_id,omitempty"`
}

type issueSupplementSnapshot struct {
	ID                   string `json:"id"`
	Text                 string `json:"text"`
	RequirementsRevision int    `json:"requirements_revision,omitempty"`
}

type issueSupplementBatch struct {
	ID          string                    `json:"id"`
	Target      issueSupplementTarget     `json:"target"`
	Snapshot    []issueSupplementSnapshot `json:"snapshot"`
	Text        string                    `json:"text"`
	CreatedAt   int64                     `json:"created_at"`
	State       string                    `json:"state"`
	SessionID   string                    `json:"session_id,omitempty"`
	TaskID      string                    `json:"task_id,omitempty"`
	ExecutionID string                    `json:"execution_id,omitempty"`
	PiRun       *piActivityRun            `json:"pi_run,omitempty"`
	SaveError   string                    `json:"save_error,omitempty"`
}

type issueSupplementAssociation struct {
	SessionID   string
	TaskID      string
	ExecutionID string
	PiRun       *piActivityRun
}

type issueSupplementLedger struct {
	Items        []*issueSupplement      `json:"items,omitempty"`
	Batches      []*issueSupplementBatch `json:"batches,omitempty"`
	AutoContinue bool                    `json:"auto_continue"`
}

func newIssueSupplementID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "supplement-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("supplement-%d", time.Now().UnixNano())
}

func newIssueSupplementLedger() *issueSupplementLedger {
	return &issueSupplementLedger{AutoContinue: true}
}

func (l *issueSupplementLedger) pendingCount() int {
	count := 0
	for _, item := range l.Items {
		if item != nil && item.State == supplementStatePending && item.PiQuestionOperationID == "" {
			count++
		}
	}
	return count
}

func (l *issueSupplementLedger) pendingBytes() int {
	total := 0
	for _, item := range l.Items {
		if item != nil && item.State == supplementStatePending && item.PiQuestionOperationID == "" {
			total += len([]byte(item.Text))
		}
	}
	return total
}

// Add receives a supplement only after save succeeds. The callback is the app's
// existing one-shot store save; nil is rejected instead of calling in-memory
// data received.
func (l *issueSupplementLedger) Add(target issueSupplementTarget, text string, save func() error) (*issueSupplement, error) {
	return l.add(target, text, nil, save)
}

// AddWith applies an answer/question association to the new item before the
// same one-shot store save. The caller's mutation must be rollback-safe; the
// item itself is removed if save fails.
func (l *issueSupplementLedger) AddWith(target issueSupplementTarget, text string, before func(*issueSupplement), save func() error) (*issueSupplement, error) {
	return l.add(target, text, before, save)
}

func (l *issueSupplementLedger) add(target issueSupplementTarget, text string, before func(*issueSupplement), save func() error) (*issueSupplement, error) {
	if err := validateSupplementTarget(target); err != nil {
		return nil, err
	}
	if save == nil {
		return nil, errSupplementSaveRequired
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("supplement text is required")
	}
	if len([]byte(text)) > maxSupplementTextBytes {
		return nil, fmt.Errorf("supplement is too large: %d bytes (max %d)", len([]byte(text)), maxSupplementTextBytes)
	}
	if l.pendingCount() >= maxPendingSupplements {
		return nil, fmt.Errorf("too many pending supplements for this issue (max %d)", maxPendingSupplements)
	}
	if l.pendingBytes()+len([]byte(text)) > maxPendingSupplementBytes {
		return nil, fmt.Errorf("pending supplement text is too large (max %d)", maxPendingSupplementBytes)
	}
	item := &issueSupplement{
		ID: newIssueSupplementID(), ContactID: target.ContactID, IssueID: target.IssueID,
		Text: text, RequirementsRevision: target.RequirementsRevision,
		CreatedAt: nowMillis(), State: supplementStatePending,
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

func validateSupplementTarget(target issueSupplementTarget) error {
	if strings.TrimSpace(target.ContactID) == "" || strings.TrimSpace(target.IssueID) == "" {
		return errors.New("supplement target must include contact and issue")
	}
	if target.RequirementsRevision < 0 {
		return errors.New("supplement target revision must not be negative")
	}
	return nil
}

func supplementTargetsSameIssue(a, b issueSupplementTarget) bool {
	return a.ContactID == b.ContactID && a.IssueID == b.IssueID
}

func (l *issueSupplementLedger) pendingFor(target issueSupplementTarget) []*issueSupplement {
	var out []*issueSupplement
	for _, item := range l.Items {
		if item != nil && item.State == supplementStatePending && item.PiQuestionOperationID == "" &&
			supplementTargetsSameIssue(issueSupplementTarget{
				ContactID: item.ContactID, IssueID: item.IssueID,
			}, target) &&
			item.RequirementsRevision <= target.RequirementsRevision {
			out = append(out, item)
		}
	}
	return out
}

func (l *issueSupplementLedger) pendingText(target issueSupplementTarget) string {
	items := l.pendingFor(target)
	var texts []string
	for _, item := range items {
		if item != nil {
			texts = append(texts, item.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func (l *issueSupplementLedger) activeBatch(target issueSupplementTarget) *issueSupplementBatch {
	for _, batch := range l.Batches {
		if batch == nil || !supplementTargetsSameIssue(batch.Target, target) {
			continue
		}
		switch batch.State {
		case supplementBatchDispatching, supplementBatchUnknown:
			return batch
		}
	}
	return nil
}

func (l *issueSupplementLedger) snapshotBatchItems(batch *issueSupplementBatch) map[string]issueSupplement {
	items := map[string]issueSupplement{}
	if batch == nil {
		return items
	}
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			items[item.ID] = *item
		}
	}
	return items
}

func (l *issueSupplementLedger) restoreBatchItems(batch *issueSupplementBatch, old map[string]issueSupplement) {
	if batch == nil {
		return
	}
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			if previous, ok := old[item.ID]; ok {
				*item = previous
			}
		}
	}
}

// PrepareDispatch creates and durably stores a batch already at the dispatch
// boundary. The caller has appended the execution and set Work submitting
// before save; the same save therefore records the execution, batch, items and
// Work intent. It does not invoke Runtime. It deliberately does not create an
// intermediate prepared batch.
func (l *issueSupplementLedger) PrepareDispatch(target issueSupplementTarget, execution *issueExecution, save func() error) (*issueSupplementBatch, error) {
	if err := validateSupplementTarget(target); err != nil {
		return nil, err
	}
	if save == nil {
		return nil, errSupplementSaveRequired
	}
	if execution == nil || strings.TrimSpace(execution.ID) == "" {
		return nil, errors.New("supplement dispatch requires an execution")
	}
	if active := l.activeBatch(target); active != nil {
		return nil, fmt.Errorf("a supplement batch is already %s for this issue", active.State)
	}
	items := l.pendingFor(target)
	if len(items) == 0 {
		return nil, errSupplementNoPending
	}
	batch := &issueSupplementBatch{
		ID: newIssueSupplementID(), Target: target, CreatedAt: nowMillis(), State: supplementBatchDispatching,
		ExecutionID: execution.ID,
	}
	var texts []string
	for _, item := range items {
		batch.Snapshot = append(batch.Snapshot, issueSupplementSnapshot{
			ID: item.ID, Text: item.Text, RequirementsRevision: item.RequirementsRevision,
		})
		texts = append(texts, item.Text)
		item.State = supplementStateDispatching
		item.BatchID = batch.ID
	}
	batch.Text = strings.Join(texts, "\n")
	execution.SupplementBatchID = batch.ID
	l.Batches = append(l.Batches, batch)
	if err := save(); err != nil {
		execution.SupplementBatchID = ""
		l.Batches = l.Batches[:len(l.Batches)-1]
		for _, snapshot := range batch.Snapshot {
			if item := l.itemByID(snapshot.ID); item != nil {
				item.State = supplementStatePending
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
func (l *issueSupplementLedger) RetryUnaccepted(batch *issueSupplementBatch, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil ||
		(canonical.State != supplementBatchRejected && canonical.State != supplementBatchNotDispatched) {
		return false
	}
	old := *canonical
	oldItems := l.snapshotBatchItems(canonical)
	for _, snapshot := range canonical.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStatePending
			item.BatchID = ""
		}
	}
	canonical.State = supplementBatchCanceled
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
func (l *issueSupplementLedger) DeferDispatch(batch *issueSupplementBatch, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil || canonical.State != supplementBatchDispatching {
		return false
	}
	old := *canonical
	oldItems := l.snapshotBatchItems(canonical)
	for _, snapshot := range canonical.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStatePending
			item.BatchID = ""
		}
	}
	canonical.State = supplementBatchCanceled
	if err := save(); err != nil {
		*canonical = old
		l.restoreBatchItems(canonical, oldItems)
		return false
	}
	return true
}

// Accept records the observed runtime association and does not imply issue completion.
func (l *issueSupplementLedger) Accept(batch *issueSupplementBatch, association issueSupplementAssociation, save func() error) (bool, error) {
	canonical := l.canonicalBatch(batch)
	if canonical == nil || (canonical.State != supplementBatchDispatching && canonical.State != supplementBatchUnknown) {
		return false, nil
	}
	batch = canonical
	if save == nil {
		return false, errSupplementSaveRequired
	}
	piAccepted := association.PiRun != nil && association.PiRun.Admitted && association.PiRun.WorkerEpoch != "" && association.PiRun.ContextID != "" && association.PiRun.SessionRef != "" && association.PiRun.RunID != "" && (association.SessionID == "" || association.SessionID == association.PiRun.SessionRef) && (association.TaskID == "" || association.TaskID == association.PiRun.RunID)
	if strings.TrimSpace(association.ExecutionID) == "" || !piAccepted {
		return false, errors.New("accepted supplement requires an admitted Pi run and execution ID")
	}
	batch.State = supplementBatchAccepted
	batch.SessionID, batch.TaskID, batch.ExecutionID = association.SessionID, association.TaskID, association.ExecutionID
	batch.PiRun = clonePiActivityRun(association.PiRun)
	batch.SaveError = ""
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateSubmitted
			item.SessionID, item.TaskID, item.ExecutionID = batch.SessionID, batch.TaskID, batch.ExecutionID
			item.PiRun = clonePiActivityRun(batch.PiRun)
		}
	}
	if err := save(); err != nil {
		// Runtime acceptance is an external fact and cannot be rolled back.
		// Keep the accepted snapshot and association, stop automatic
		// continuation, and expose the persistence failure to the app.
		batch.SaveError = err.Error()
		l.AutoContinue = false
		return true, fmt.Errorf("runtime accepted supplement, but saving the association failed: %w", err)
	}
	return true, nil
}

func (l *issueSupplementLedger) Reject(batch *issueSupplementBatch, detail string, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil ||
		canonical.State != supplementBatchDispatching {
		return false
	}
	batch = canonical
	old := *batch
	oldItems := l.snapshotBatchItems(batch)
	batch.State = supplementBatchRejected
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateRejected
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

func (l *issueSupplementLedger) NotDispatched(batch *issueSupplementBatch, detail string, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil ||
		(canonical.State != supplementBatchDispatching && canonical.State != supplementBatchUnknown) {
		return false
	}
	batch = canonical
	old := *batch
	oldItems := l.snapshotBatchItems(batch)
	batch.State = supplementBatchNotDispatched
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateNotDispatched
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

func (l *issueSupplementLedger) Unknown(batch *issueSupplementBatch, detail string, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil ||
		canonical.State != supplementBatchDispatching {
		return false
	}
	batch = canonical
	old := *batch
	oldAuto, oldItems := l.AutoContinue, l.snapshotBatchItems(batch)
	batch.State = supplementBatchUnknown
	l.AutoContinue = false
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateUnknown
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
func (l *issueSupplementLedger) StopAutoContinue(batch *issueSupplementBatch, save func() error) bool {
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
	oldBatch := issueSupplementBatch{}
	var oldItems map[string]issueSupplement
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

func (l *issueSupplementLedger) ResumeAutoContinue(save func() error) bool {
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

func (l *issueSupplementLedger) itemByID(id string) *issueSupplement {
	for _, item := range l.Items {
		if item != nil && item.ID == id {
			return item
		}
	}
	return nil
}

func (l *issueSupplementLedger) batchByID(id string) *issueSupplementBatch {
	for _, batch := range l.Batches {
		if batch != nil && batch.ID == id {
			return batch
		}
	}
	return nil
}

func (l *issueSupplementLedger) canonicalBatch(batch *issueSupplementBatch) *issueSupplementBatch {
	if batch == nil {
		return nil
	}
	canonical := l.batchByID(batch.ID)
	if canonical != batch {
		return nil
	}
	return canonical
}

func cloneIssueSupplementLedger(ledger *issueSupplementLedger) *issueSupplementLedger {
	if ledger == nil {
		return nil
	}
	out := &issueSupplementLedger{AutoContinue: ledger.AutoContinue}
	if len(ledger.Items) > 0 {
		out.Items = make([]*issueSupplement, 0, len(ledger.Items))
		for _, item := range ledger.Items {
			if item == nil {
				out.Items = append(out.Items, nil)
				continue
			}
			copyItem := *item
			copyItem.PiRun = clonePiActivityRun(item.PiRun)
			out.Items = append(out.Items, &copyItem)
		}
	}
	if len(ledger.Batches) > 0 {
		out.Batches = make([]*issueSupplementBatch, 0, len(ledger.Batches))
		for _, batch := range ledger.Batches {
			if batch == nil {
				out.Batches = append(out.Batches, nil)
				continue
			}
			copyBatch := *batch
			copyBatch.PiRun = clonePiActivityRun(batch.PiRun)
			copyBatch.Snapshot = slices.Clone(batch.Snapshot)
			out.Batches = append(out.Batches, &copyBatch)
		}
	}
	return out
}
