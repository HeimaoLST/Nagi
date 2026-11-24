package store

// The contact store owns product records and Pi session bindings. The Pi
// worker reads transcripts through Pi's session API.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
)

const ContactStoreVersion = 10

type StoredContact struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Role         string            `json:"role,omitempty"`
	Purpose      string            `json:"purpose,omitempty"`
	Instructions string            `json:"instructions,omitempty"`
	Workspace    string            `json:"workspace,omitempty"`
	BuiltinTools *[]string         `json:"builtin_tools,omitempty"`
	SessionID    string            `json:"session_id,omitempty"`
	Draft        string            `json:"draft,omitempty"`
	TaskContext  []TaskContextItem `json:"task_context,omitempty"`
	Archived     bool              `json:"archived,omitempty"`
	ArchivedAt   int64             `json:"archived_at,omitempty"`

	PiBinding          *PiConversationBinding  `json:"pi_binding,omitempty"`
	PreviousPiBindings []PiConversationBinding `json:"previous_pi_bindings,omitempty"`
	PendingSend        *PendingSend            `json:"pending_send,omitempty"`
	SelectedIssueID    string                  `json:"selected_issue_id,omitempty"`
	UpdatedAt          int64                   `json:"updated_at,omitempty"`
}

type StoredIssueExecution struct {
	ID                   string            `json:"id"`
	SessionID            string            `json:"session_id,omitempty"`
	TaskID               string            `json:"task_id,omitempty"`
	MessageID            string            `json:"message_id,omitempty"`
	Prompt               string            `json:"prompt,omitempty"`
	Status               string            `json:"status,omitempty"`
	Detail               string            `json:"detail,omitempty"`
	AttentionSeenStatus  string            `json:"attention_seen_status,omitempty"`
	RequirementsRevision int               `json:"requirements_revision,omitempty"`
	Context              []TaskContextItem `json:"context,omitempty"`
	SupplementBatchID    string            `json:"supplement_batch_id,omitempty"`
	ToolOperationID      string            `json:"tool_operation_id,omitempty"`
	ToolScopeRef         string            `json:"tool_scope_ref,omitempty"`
	PiIntent             bool              `json:"pi_intent,omitempty"`
	PiRun                *PiActivityRun    `json:"pi_run,omitempty"`
	ReturnedAt           int64             `json:"returned_at,omitempty"`
	WaitRequested        bool              `json:"wait_requested,omitempty"`
}

type StoredRequirement struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	State     string `json:"state,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

type StoredIssue struct {
	ID        string `json:"id"`
	ContactID string `json:"contact_id"`
	Goal      string `json:"goal"`

	RequirementsRevision int                    `json:"requirements_revision,omitempty"`
	RequirementItems     []StoredRequirement    `json:"requirement_items,omitempty"`
	Status               string                 `json:"status,omitempty"`
	Archived             bool                   `json:"archived,omitempty"`
	Evidence             string                 `json:"evidence,omitempty"`
	CreatedAt            int64                  `json:"created_at,omitempty"`
	UpdatedAt            int64                  `json:"updated_at,omitempty"`
	PiBinding            *PiActivityBinding     `json:"pi_binding,omitempty"`
	Handoff              *IssueHandoff          `json:"handoff,omitempty"`
	WorkSessionID        string                 `json:"work_session_id,omitempty"`
	TaskContext          []TaskContextItem      `json:"task_context,omitempty"`
	Supplements          *IssueSupplementLedger `json:"supplements,omitempty"`
	Questions            []*IssueQuestion       `json:"questions,omitempty"`
	Executions           []StoredIssueExecution `json:"executions,omitempty"`
}

// StoredLayout is the GUI-wide workbench preference. It is optional so
// stores written before #184 keep loading with the right work panel closed.
type StoredLayout struct {
	LeftWidth     float32 `json:"left_width,omitempty"`
	RightWidth    float32 `json:"right_width,omitempty"`
	WorkspaceOpen bool    `json:"workspace_open"`
}

type StoredState struct {
	Version             int                       `json:"version"`
	Selected            string                    `json:"selected,omitempty"`
	Contacts            []StoredContact           `json:"contacts"`
	Issues              []StoredIssue             `json:"issues,omitempty"`
	IssueToolOperations *IssueToolOperationLedger `json:"issue_tool_operations,omitempty"`
	IssueToolScopes     []IssueToolScope          `json:"issue_tool_scopes,omitempty"`
	Layout              *StoredLayout             `json:"layout,omitempty"`
	ThemeIDs            *theme.AppearanceThemeIDs `json:"theme_ids,omitempty"`
	Appearance          string                    `json:"appearance,omitempty"`
}

type ContactStore struct {
	Path            string
	State           StoredState
	IssueToolLedger *IssueToolOperationLedger
	IssueToolScopes map[string]IssueToolScope
}

func NewContactID() string {
	var bytes [6]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "contact-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("contact-%d", time.Now().UnixNano())
}

// OpenContactStore loads the store, or creates and persists a default Nagi
// contact when the file does not exist. A corrupt or unsupported file is
// reported and left untouched; the app must not silently replace it.
func OpenContactStore(path, defaultWorkspace string) (*ContactStore, error) {
	store := &ContactStore{Path: path}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		store.State, err = DecodeStoredState(path, data, false)
		if err != nil {
			return nil, err
		}
		if !store.HasContact(store.State.Selected) {
			store.State.Selected = store.State.Contacts[0].ID
		}
		store.RestoreIssueToolState()
		return store, nil
	case errors.Is(err, os.ErrNotExist):
		store.State = StoredState{
			Version: ContactStoreVersion,
			Contacts: []StoredContact{{
				ID:        NewContactID(),
				Name:      "Nagi",
				Role:      "Assistant",
				Workspace: defaultWorkspace,
				UpdatedAt: NowMillis(),
			}},
		}
		store.State.Selected = store.State.Contacts[0].ID
		store.RestoreIssueToolState()
		if err := store.SaveState(); err != nil {
			return nil, err
		}
		return store, nil
	default:
		return nil, fmt.Errorf("profile store %s is not accessible: %w", path, err)
	}
}

func (s *ContactStore) RestoreIssueToolState() {
	if s.IssueToolLedger == nil {
		s.IssueToolLedger = CloneIssueToolLedger(s.State.IssueToolOperations)
	}
	if s.IssueToolLedger == nil {
		s.IssueToolLedger = &IssueToolOperationLedger{}
	}
	if s.IssueToolScopes == nil {
		s.IssueToolScopes = map[string]IssueToolScope{}
	}
	for _, scope := range s.State.IssueToolScopes {
		if scope.Validate() == nil {
			s.IssueToolScopes[scope.Ref] = CloneIssueToolScope(scope)
		}
	}
}

func (s *ContactStore) SetIssueToolState(ledger *IssueToolOperationLedger, scopes map[string]IssueToolScope) {
	if ledger == nil {
		ledger = &IssueToolOperationLedger{}
	}
	s.IssueToolLedger = ledger
	s.IssueToolScopes = make(map[string]IssueToolScope, len(scopes))
	for ref, scope := range scopes {
		s.IssueToolScopes[ref] = CloneIssueToolScope(scope)
	}
}

func (state StoredState) validatePiBindings() error {
	for _, ct := range state.Contacts {
		if ct.SessionID != "" && (ct.PiBinding == nil || ct.PiBinding.Engine != "pi" || ct.PiBinding.Context.SessionRef != ct.SessionID) {
			return fmt.Errorf("Bot %s has an invalid Pi session binding", ct.ID)
		}
	}
	for _, issue := range state.Issues {
		if !StoredPiIssueValid(issue, state.IssueToolOperations) {
			return fmt.Errorf("issue %s has an invalid Pi execution record", issue.ID)
		}
		if issue.Handoff != nil && !StoredIssueHandoffValid(issue, state) {
			return fmt.Errorf("issue %s has an invalid handoff", issue.ID)
		}
	}
	return nil
}

func ReadablePiIssueStatus(status string) bool {
	return status == IssueExecutionCompleted || status == IssueExecutionFailed || status == IssueExecutionInterrupted
}

func CloneIssueToolReceipt(receipt *IssueToolReceipt) *IssueToolReceipt {
	if receipt == nil {
		return nil
	}
	copied := *receipt
	copied.PiRun = ClonePiActivityRun(receipt.PiRun)
	if receipt.RequirementsRevision != nil {
		value := *receipt.RequirementsRevision
		copied.RequirementsRevision = &value
	}
	if receipt.Error != nil {
		errorCopy := *receipt.Error
		errorCopy.Candidates = append([]string(nil), receipt.Error.Candidates...)
		if receipt.Error.CurrentRevision != nil {
			value := *receipt.Error.CurrentRevision
			errorCopy.CurrentRevision = &value
		}
		copied.Error = &errorCopy
	}
	return &copied
}

func StoredPiIssueValid(issue StoredIssue, ledger *IssueToolOperationLedger) bool {
	if issue.PiBinding == nil {
		if issue.WorkSessionID != "" {
			return false
		}
	} else {
		binding := issue.PiBinding
		if binding.Engine != "pi" || binding.Context.Purpose != "activity" ||
			binding.Context.BotID != issue.ContactID || binding.Context.ActivityID != issue.ID ||
			binding.Context.ContextID == "" || binding.Context.SessionRef == "" || binding.Context.SelectionRevision == 0 || binding.Context.CWD == "" ||
			binding.Selection.CWD == "" || binding.Configuration.CredentialSource == "" ||
			issue.WorkSessionID != binding.Context.SessionRef {
			return false
		}
	}
	for _, execution := range issue.Executions {
		if execution.ReturnedAt != 0 && (issue.Handoff == nil || execution.ReturnedAt < issue.CreatedAt || execution.PiRun == nil || !execution.PiRun.Admitted || !ReadablePiIssueStatus(execution.Status)) {
			return false
		}
		if !execution.PiIntent || execution.ID == "" || execution.MessageID != "" {
			return false
		}
		if execution.PiRun == nil {
			if execution.SessionID != "" || execution.TaskID != "" || execution.Status == IssueExecutionAccepted || execution.Status == IssueExecutionRunning || execution.Status == IssueExecutionCompleted {
				return false
			}
			continue
		}
		run := execution.PiRun
		if issue.PiBinding == nil || run.WorkerEpoch == "" || run.ContextID != issue.PiBinding.Context.ContextID ||
			run.SessionRef != issue.PiBinding.Context.SessionRef || run.RunID == "" ||
			execution.SessionID != "" && execution.SessionID != run.SessionRef ||
			execution.TaskID != "" && execution.TaskID != run.RunID ||
			run.Admitted && (execution.SessionID != run.SessionRef || execution.TaskID != run.RunID) ||
			!run.Admitted && (execution.Status == IssueExecutionAccepted || execution.Status == IssueExecutionRunning || execution.Status == IssueExecutionCompleted) {
			return false
		}
	}
	for _, question := range issue.Questions {
		if question == nil || question.PiCall == nil {
			continue
		}
		call := question.PiCall
		if issue.PiBinding == nil || call.ContactID != issue.ContactID || call.IssueID != issue.ID || call.WorkerEpoch == "" || call.ContextID != issue.PiBinding.Context.ContextID || call.SessionRef != issue.PiBinding.Context.SessionRef || call.RunID == "" || call.CallID == "" || call.OperationID == "" || call.ExecutionID == "" || ledger == nil {
			return false
		}
		operation := ledger.OperationByID(call.OperationID)
		if operation == nil || operation.PiCall == nil || *operation.PiCall != *call {
			return false
		}
		matched := false
		for _, execution := range issue.Executions {
			if execution.ID == call.ExecutionID && execution.PiRun != nil && execution.PiRun.WorkerEpoch == call.WorkerEpoch && execution.PiRun.ContextID == call.ContextID && execution.PiRun.RunID == call.RunID {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
		if question.State == IssueQuestionAnswered {
			if question.AnswerSupplementID == "" || issue.Supplements == nil {
				return false
			}
			item := issue.Supplements.ItemByID(question.AnswerSupplementID)
			if item == nil || item.PiQuestionOperationID != call.OperationID || item.Text != question.AnswerText {
				return false
			}
		}
	}
	return true
}

func CloneIssueToolLedger(ledger *IssueToolOperationLedger) *IssueToolOperationLedger {
	if ledger == nil {
		return nil
	}
	cloned := *ledger
	if ledger.Operations != nil {
		cloned.Operations = make([]*IssueToolOperation, len(ledger.Operations))
		for i, operation := range ledger.Operations {
			if operation != nil {
				copied := *operation
				copied.PiCall = ClonePiQuestionCall(operation.PiCall)
				copied.Receipt = CloneIssueToolReceipt(operation.Receipt)
				cloned.Operations[i] = &copied
			}
		}
	}
	return &cloned
}

func CloneIssueToolScope(scope IssueToolScope) IssueToolScope {
	if scope.AllowedIssueIDs != nil {
		scope.AllowedIssueIDs = append([]string{}, scope.AllowedIssueIDs...)
	}
	return scope
}

func CloneIssueQuestions(questions []*IssueQuestion) []*IssueQuestion {
	if questions == nil {
		return nil
	}
	cloned := make([]*IssueQuestion, len(questions))
	for i, question := range questions {
		if question != nil {
			copied := *question
			copied.PiCall = ClonePiQuestionCall(question.PiCall)
			copied.AnswerContext = CloneTaskContextItems(question.AnswerContext)
			cloned[i] = &copied
		}
	}
	return cloned
}

func ClonePiConversationBindings(bindings []PiConversationBinding) []PiConversationBinding {
	if bindings == nil {
		return nil
	}
	cloned := make([]PiConversationBinding, len(bindings))
	for i := range bindings {
		cloned[i] = *ClonePiConversationBinding(&bindings[i])
	}
	return cloned
}

func CloneStoredState(state StoredState) StoredState {
	cloned := state
	if state.Contacts != nil {
		cloned.Contacts = append([]StoredContact{}, state.Contacts...)
		for i := range cloned.Contacts {
			ct := &cloned.Contacts[i]
			ct.BuiltinTools = CloneBuiltinTools(ct.BuiltinTools)
			ct.TaskContext = CloneTaskContextItems(ct.TaskContext)
			ct.PiBinding = ClonePiConversationBinding(ct.PiBinding)
			ct.PreviousPiBindings = ClonePiConversationBindings(ct.PreviousPiBindings)
			ct.PendingSend = ClonePendingSend(ct.PendingSend)
		}
	}
	if state.Issues != nil {
		cloned.Issues = append([]StoredIssue{}, state.Issues...)
		for i := range cloned.Issues {
			issue := &cloned.Issues[i]
			if issue.RequirementItems != nil {
				issue.RequirementItems = append([]StoredRequirement{}, issue.RequirementItems...)
			}
			issue.TaskContext = CloneTaskContextItems(issue.TaskContext)
			issue.PiBinding = ClonePiActivityBinding(issue.PiBinding)
			issue.Supplements = CloneIssueSupplementLedger(issue.Supplements)
			issue.Questions = CloneIssueQuestions(issue.Questions)
			if issue.Executions != nil {
				issue.Executions = append([]StoredIssueExecution{}, issue.Executions...)
				for j := range issue.Executions {
					issue.Executions[j].PiRun = ClonePiActivityRun(issue.Executions[j].PiRun)
					issue.Executions[j].Context = CloneTaskContextItems(issue.Executions[j].Context)
				}
			}
		}
	}
	cloned.IssueToolOperations = CloneIssueToolLedger(state.IssueToolOperations)
	if state.IssueToolScopes != nil {
		cloned.IssueToolScopes = make([]IssueToolScope, len(state.IssueToolScopes))
		for i, scope := range state.IssueToolScopes {
			cloned.IssueToolScopes[i] = CloneIssueToolScope(scope)
		}
	}
	if state.Layout != nil {
		layout := *state.Layout
		cloned.Layout = &layout
	}
	if state.ThemeIDs != nil {
		themeIDs := *state.ThemeIDs
		cloned.ThemeIDs = &themeIDs
	}
	return cloned
}

func (s *ContactStore) SaveState() error {
	state := s.State
	state.IssueToolOperations = nil
	state.IssueToolScopes = nil
	candidate := CloneStoredState(state)
	candidate.IssueToolOperations = CloneIssueToolLedger(s.IssueToolLedger)
	candidate.IssueToolScopes = s.IssueToolScopesList()
	return s.CommitState(candidate)
}

func (s *ContactStore) CommitState(candidate StoredState) error {
	if err := candidate.validatePiBindings(); err != nil {
		return err
	}
	if err := WriteProfileJSON(s.Path, candidate); err != nil {
		return err
	}
	s.State = candidate
	return nil
}

type ProductPendingSend struct {
	ConversationID string `json:"conversationId"`
	OperationID    string `json:"operationId"`
}

type ProductUIState struct {
	Selected             string                        `json:"selected,omitempty"`
	Layout               StoredLayout                  `json:"layout"`
	ThemeIDs             theme.AppearanceThemeIDs      `json:"themeIds"`
	Appearance           string                        `json:"appearance,omitempty"`
	HideBuiltinArchitect bool                          `json:"hideBuiltinArchitect,omitempty"`
	Pending              map[string]ProductPendingSend `json:"pending,omitempty"`
	RecoveryID           string                        `json:"recoveryId,omitempty"`

	PendingActivities map[string]ProductActivityIntent `json:"pending_activities,omitempty"`
}

func ProductUIPath(profilePath string) string { return profilePath + ".ui.json" }

func ReadProductUI(profilePath string) (*ProductUIState, error) {
	data, err := os.ReadFile(ProductUIPath(profilePath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state ProductUIState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func WriteProfileJSON(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create profile store directory: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(dir, ".contacts-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if err := writeStoreBytes(temp, data); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func (s *ContactStore) IssueToolScopesList() []IssueToolScope {
	if len(s.IssueToolScopes) == 0 {
		return nil
	}
	out := make([]IssueToolScope, 0, len(s.IssueToolScopes))
	for _, scope := range s.IssueToolScopes {
		out = append(out, CloneIssueToolScope(scope))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

func (s *ContactStore) HasContact(id string) bool {
	for _, c := range s.State.Contacts {
		if c.ID == id {
			return true
		}
	}
	return false
}

func DecodeStoredState(path string, data []byte, strict bool) (StoredState, error) {
	var state StoredState
	var err error
	if strict {
		if !utf8.Valid(data) {
			return state, fmt.Errorf("profile store %s is not readable: expected UTF-8 JSON", path)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&state)
		if err == nil {
			if decoder.Decode(new(any)) != io.EOF {
				err = errors.New("expected exactly one JSON object")
			}
		}
	} else {
		err = json.Unmarshal(data, &state)
	}
	if err != nil {
		return state, fmt.Errorf("profile store %s is not readable: %w", path, err)
	}
	if state.Version != ContactStoreVersion {
		return state, fmt.Errorf("profile store %s has unsupported version %d", path, state.Version)
	}
	if len(state.Contacts) == 0 {
		return state, fmt.Errorf("profile store %s has no Bots", path)
	}
	contacts, issues := map[string]bool{}, map[string]bool{}
	for _, contact := range state.Contacts {
		if contact.ID == "" || contacts[contact.ID] {
			return state, fmt.Errorf("profile store %s has an empty or duplicate Bot ID", path)
		}
		contacts[contact.ID] = true
	}
	for _, issue := range state.Issues {
		if issue.ID == "" || issues[issue.ID] {
			return state, fmt.Errorf("profile store %s has an empty or duplicate issue ID", path)
		}
		if issue.ContactID == "" || !contacts[issue.ContactID] {
			return state, fmt.Errorf("profile store %s issue %s has an unknown Bot ID", path, issue.ID)
		}
		issues[issue.ID] = true
	}
	return state, state.validatePiBindings()
}

func ValidateRecoveryState(state StoredState) error {
	fail := func(field, message string) error { return fmt.Errorf("%s: %s", field, message) }
	if state.Version != ContactStoreVersion {
		return fail("version", "only current version 10 recovery copies are supported")
	}
	selected := false
	for i, contact := range state.Contacts {
		selected = selected || contact.ID == state.Selected
		if contact.SelectedIssueID != "" {
			found := false
			for _, issue := range state.Issues {
				found = found || issue.ID == contact.SelectedIssueID && issue.ContactID == contact.ID
			}
			if !found {
				return fail(fmt.Sprintf("contacts[%d].selected_issue_id", i), "selected issue does not belong to this Bot")
			}
		}
		if pending := contact.PendingSend; pending != nil && (pending.State == "submitting" || pending.State == "unknown" && contact.SessionID == "") {
			return fail(fmt.Sprintf("contacts[%d].pending_send", i), "this intent would change or be discarded on ordinary load")
		}
	}
	if !selected {
		return fail("selected", "selected Bot is missing")
	}
	if state.IssueToolOperations == nil {
		return fail("issue_tool_operations", "missing operation ledger would be synthesized on ordinary load")
	}
	refs := map[string]bool{}
	for i, scope := range state.IssueToolScopes {
		if scope.Validate() != nil || refs[scope.Ref] {
			return fail(fmt.Sprintf("issue_tool_scopes[%d]", i), "invalid or duplicate scope would be dropped or replaced on ordinary load")
		}
		refs[scope.Ref] = true
	}
	for i, issue := range state.Issues {
		field := fmt.Sprintf("issues[%d]", i)
		if issue.RequirementsRevision < 1 {
			return fail(field+".requirements_revision", "revision would be repaired on ordinary load")
		}
		for j, item := range issue.RequirementItems {
			if strings.TrimSpace(item.Text) == "" || item.State != RequirementUnverified && item.State != RequirementSatisfied && item.State != RequirementUnmet {
				return fail(fmt.Sprintf("%s.requirement_items[%d]", field, j), "requirement would be dropped or normalized on ordinary load")
			}
		}
		for j, question := range issue.Questions {
			if question == nil || strings.TrimSpace(question.Ref) == "" {
				return fail(fmt.Sprintf("%s.questions[%d]", field, j), "question would be dropped on ordinary load")
			}
		}
		switch issue.Status {
		case IssueStatusNotStarted, IssueStatusInProgress, IssueStatusWaitingUser, IssueStatusNeedsAttention, IssueStatusCompleted, IssueStatusCanceled:
		default:
			return fail(field+".status", "issue status would be normalized on ordinary load")
		}
		for j, execution := range issue.Executions {
			if execution.ID == "" || execution.Status == "" || execution.Status == IssueExecutionPending || execution.TaskID == "" && (execution.Status == IssueExecutionAccepted || execution.Status == IssueExecutionRunning) {
				return fail(fmt.Sprintf("%s.executions[%d]", field, j), "execution would be dropped or normalized on ordinary load")
			}
		}
		if issue.Supplements != nil {
			for j, batch := range issue.Supplements.Batches {
				if batch != nil && batch.State == SupplementBatchDispatching && batch.ExecutionID == "" {
					return fail(fmt.Sprintf("%s.supplements.batches[%d]", field, j), "batch would be normalized on ordinary load")
				}
			}
		}
	}
	if state.Appearance != "" && state.Appearance != theme.AppearanceSystem && state.Appearance != theme.AppearanceLight && state.Appearance != theme.AppearanceDark {
		return fail("appearance", "invalid appearance preference")
	}
	if layout := state.Layout; layout != nil {
		if layout.LeftWidth != 0 && (layout.LeftWidth < theme.MinLeftPaneWidth || layout.LeftWidth > theme.MaxLeftPaneWidth) || layout.RightWidth != 0 && (layout.RightWidth < theme.MinRightPaneWidth || layout.RightWidth > theme.MaxRightPaneWidth) {
			return fail("layout", "pane dimensions would be corrected on ordinary load")
		}
	}
	return nil
}

type StoreRecoveryCopy struct {
	Path       string
	Contacts   int
	Issues     int
	ModifiedAt time.Time
	data       []byte
	info       os.FileInfo
}

func ProductProfileVersion(data []byte) bool {
	var marker struct {
		Version int `json:"version"`
	}
	return json.Unmarshal(data, &marker) == nil && marker.Version == 1
}

var ErrInvalidProductRecovery = errors.New("the current product profile contains unsupported or inconsistent records")

func InspectProductProfileBytes(data []byte, copy bool) (contacts, activities int, err error) {
	node, worker, err := pi.PayloadPaths()
	if err != nil {
		return 0, 0, err
	}
	module := filepath.Join(filepath.Dir(worker), "product-profile.js")
	const script = `
import { text } from 'node:stream/consumers';
import { pathToFileURL } from 'node:url';
const { decodeProductBackup, inspectProductRecovery } = await import(pathToFileURL(process.argv[1]).href);
try {
  const data = JSON.parse(await text(process.stdin));
  if (process.argv[2] === 'copy') {
    process.stdout.write(JSON.stringify(inspectProductRecovery(data)));
  } else {
    decodeProductBackup(data);
  }
} catch {
  process.exitCode = 2;
}`
	mode := "profile"
	if copy {
		mode = "copy"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--input-type=module", "-e", script, module, mode)
	command.Stdin = bytes.NewReader(data)
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 2 {
			return 0, 0, ErrInvalidProductRecovery
		}
		return 0, 0, fmt.Errorf("could not check the recovery profile: %w", err)
	}
	if !copy {
		return 0, 0, nil
	}
	var counts struct {
		Contacts   int `json:"contacts"`
		Activities int `json:"activities"`
	}
	if err := json.Unmarshal(output, &counts); err != nil {
		return 0, 0, fmt.Errorf("could not read the recovery profile summary: %w", err)
	}
	return counts.Contacts, counts.Activities, nil
}

type StoreRecoveryTarget struct {
	path string
	data []byte
	info os.FileInfo
}

type StoreRecoveryOutcome string

const (
	StoreRecoveryNotInstalled       StoreRecoveryOutcome = "not_installed"
	StoreRecoveryVerifiedInstalled  StoreRecoveryOutcome = "verified_installed"
	StoreRecoveryInstallUnconfirmed StoreRecoveryOutcome = "install_unconfirmed"
)

type StoreRecoveryResult struct {
	Outcome     StoreRecoveryOutcome
	OutputPath  string
	ArchivePath string
}

// InspectStoreRecoveryCopy captures one saved point in time so later operations
// use those validated bytes rather than a live projection or reread source.
func InspectStoreRecoveryCopy(path string) (*StoreRecoveryCopy, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, info, err := readRecoveryFile(path)
	if err != nil {
		return nil, err
	}
	if ProductProfileVersion(data) {
		contacts, activities, err := InspectProductProfileBytes(data, true)
		if err != nil {
			return nil, fmt.Errorf("unsupported recovery copy %s: %w", path, err)
		}
		return &StoreRecoveryCopy{Path: path, Contacts: contacts, Issues: activities, ModifiedAt: info.ModTime(), data: data, info: info}, nil
	}
	state, err := DecodeStoredState(path, data, true)
	if err == nil {
		err = ValidateRecoveryState(state)
	}
	if err != nil {
		return nil, fmt.Errorf("unsupported recovery copy %s: %w", path, err)
	}
	return &StoreRecoveryCopy{Path: path, Contacts: len(state.Contacts), Issues: len(state.Issues), ModifiedAt: info.ModTime(), data: data, info: info}, nil
}

func writeStoreBytes(file *os.File, data []byte) (err error) {
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err = file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func readRecoveryFile(path string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s: expected an existing regular file, not a directory or symbolic link", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	opened, statErr := file.Stat()
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if err := errors.Join(statErr, readErr, closeErr); err != nil {
		return nil, nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(info, opened) || !os.SameFile(info, after) {
		return nil, nil, fmt.Errorf("%s: file changed while being read", path)
	}
	return data, info, nil
}

func removeIncompleteRecoveryFile(path string, info os.FileInfo, cause error) error {
	if info == nil {
		return fmt.Errorf("%w; could not establish ownership to remove incomplete file %s", cause, path)
	}
	current, err := os.Lstat(path)
	if err == nil && os.SameFile(info, current) {
		err = os.Remove(path)
	} else if err == nil {
		err = errors.New("file identity changed; replacement was not removed")
	}
	if errors.Is(err, os.ErrNotExist) {
		return cause
	}
	if err != nil {
		return fmt.Errorf("%w; could not remove incomplete file %s: %v", cause, path, err)
	}
	return cause
}

// WriteStoreRecoveryCopy writes the captured recovery bytes to a new destination
// chosen by the caller. A collision is an error; existing files are never overwritten.
func WriteStoreRecoveryCopy(copy *StoreRecoveryCopy, destination string) (string, error) {
	if copy == nil {
		return "", errors.New("inspect a compatible recovery copy first")
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	if destination == copy.Path {
		return "", errors.New("the recovery output must be separate from its saved source")
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return "", removeIncompleteRecoveryFile(destination, nil, err)
	}
	if err := writeStoreBytes(file, copy.data); err != nil {
		return "", removeIncompleteRecoveryFile(destination, info, err)
	}
	data, current, err := readRecoveryFile(destination)
	if err == nil && (!os.SameFile(info, current) || !bytes.Equal(data, copy.data)) {
		err = errors.New("recovery copy readback did not match the captured saved bytes")
	}
	if err != nil {
		return "", removeIncompleteRecoveryFile(destination, info, err)
	}
	return destination, nil
}

func InspectStoreRecoveryTarget(path string) (*StoreRecoveryTarget, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, info, err := readRecoveryFile(path)
	if err != nil {
		return nil, err
	}
	if _, err := DecodeStoredState(path, data, false); err == nil {
		return nil, fmt.Errorf("%s: the store is readable; recovery replacement is not allowed", path)
	}
	if ProductProfileVersion(data) {
		_, _, err := InspectProductProfileBytes(data, false)
		if err == nil {
			return nil, fmt.Errorf("%s: the store is readable; recovery replacement is not allowed", path)
		}
		if !errors.Is(err, ErrInvalidProductRecovery) {
			return nil, err
		}
	}
	return &StoreRecoveryTarget{path: path, data: data, info: info}, nil
}

func (target *StoreRecoveryTarget) unchanged() error {
	current, err := InspectStoreRecoveryTarget(target.path)
	if err != nil {
		return err
	}
	if !os.SameFile(target.info, current.info) || !bytes.Equal(target.data, current.data) {
		return fmt.Errorf("%s: the unavailable store changed after inspection", target.path)
	}
	return nil
}

// RestoreStoreRecoveryCopy archives the unavailable target and installs the
// paused copy. The UI must explicitly confirm pausing and reserve quiescent
// unavailable state before calling; a successful Rename is never rolled back.
func RestoreStoreRecoveryCopy(copy *StoreRecoveryCopy, target *StoreRecoveryTarget) (StoreRecoveryResult, error) {
	return RestoreStoreRecoveryCopyWithRename(copy, target, os.Rename)
}

func RestoreStoreRecoveryCopyWithRename(copy *StoreRecoveryCopy, target *StoreRecoveryTarget, rename func(string, string) error) (StoreRecoveryResult, error) {
	result := StoreRecoveryResult{Outcome: StoreRecoveryNotInstalled}
	if copy == nil || target == nil {
		return result, errors.New("inspect a compatible copy and the unavailable original first")
	}
	result.OutputPath = target.path
	currentSource, sourceErr := os.Stat(copy.Path)
	if os.SameFile(copy.info, target.info) || filepath.Clean(copy.Path) == filepath.Clean(target.path) || sourceErr == nil && os.SameFile(currentSource, target.info) {
		return result, errors.New("the selected recovery copy must be separate from the unavailable original")
	}
	if err := target.unchanged(); err != nil {
		return result, err
	}
	paused := copy.data
	if ProductProfileVersion(copy.data) {
		if _, _, err := InspectProductProfileBytes(copy.data, true); err != nil {
			return result, err
		}
		var saved map[string]json.RawMessage
		if err := json.Unmarshal(copy.data, &saved); err != nil {
			return result, err
		}
		var recovery struct {
			ID string          `json:"id"`
			UI json.RawMessage `json:"ui,omitempty"`
		}
		if data := saved["recovery"]; len(data) != 0 {
			if err := json.Unmarshal(data, &recovery); err != nil {
				return result, err
			}
		}
		recovery.ID = NewContactID()
		metadata, err := json.Marshal(recovery)
		if err != nil {
			return result, err
		}
		saved["recovery"] = metadata
		paused, err = json.Marshal(saved)
		if err != nil {
			return result, err
		}
		paused = append(paused, '\n')
	} else {
		state, err := DecodeStoredState(copy.Path, copy.data, true)
		if err != nil {
			return result, err
		}
		if err := ValidateRecoveryState(state); err != nil {
			return result, err
		}
		for i := range state.Issues {
			if ledger := state.Issues[i].Supplements; ledger != nil {
				ledger.AutoContinue = false
			}
		}
		paused, err = json.MarshalIndent(state, "", "  ")
		if err != nil {
			return result, err
		}
		paused = append(paused, '\n')
	}
	dir := filepath.Dir(target.path)
	archive, err := os.CreateTemp(dir, "nagi-unreadable-*.json")
	if err != nil {
		return result, err
	}
	archivePath := archive.Name()
	archiveInfo, err := archive.Stat()
	if err != nil {
		archive.Close()
		return result, removeIncompleteRecoveryFile(archivePath, nil, err)
	}
	if err := writeStoreBytes(archive, target.data); err != nil {
		return result, removeIncompleteRecoveryFile(archivePath, archiveInfo, err)
	}
	archived, info, err := readRecoveryFile(archivePath)
	if err == nil && (!os.SameFile(archiveInfo, info) || !bytes.Equal(archived, target.data)) {
		err = errors.New("original archive readback did not match the captured damaged bytes")
	}
	if err != nil {
		return result, removeIncompleteRecoveryFile(archivePath, archiveInfo, err)
	}
	result.ArchivePath = archivePath
	stage, err := os.CreateTemp(dir, ".contacts-restore-*.tmp")
	if err != nil {
		return result, err
	}
	stagePath := stage.Name()
	stageInfo, err := stage.Stat()
	if err != nil {
		stage.Close()
		return result, removeIncompleteRecoveryFile(stagePath, nil, err)
	}
	if err := writeStoreBytes(stage, paused); err != nil {
		return result, removeIncompleteRecoveryFile(stagePath, stageInfo, err)
	}
	if err := target.unchanged(); err != nil {
		return result, removeIncompleteRecoveryFile(stagePath, stageInfo, err)
	}
	if err := rename(stagePath, target.path); err != nil {
		return result, removeIncompleteRecoveryFile(stagePath, stageInfo, err)
	}
	result.Outcome = StoreRecoveryInstallUnconfirmed
	installed, info, err := readRecoveryFile(target.path)
	if err == nil && (!os.SameFile(stageInfo, info) || !bytes.Equal(installed, paused)) {
		err = errors.New("installed data readback did not match the paused recovery copy")
	}
	if err != nil {
		return result, fmt.Errorf("installation result must be checked before any retry: %w", err)
	}
	result.Outcome = StoreRecoveryVerifiedInstalled
	return result, nil
}
