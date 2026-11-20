package main

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
)

const contactStoreVersion = 10

type storedContact struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Role         string            `json:"role,omitempty"`
	Purpose      string            `json:"purpose,omitempty"`
	Instructions string            `json:"instructions,omitempty"`
	Workspace    string            `json:"workspace,omitempty"`
	BuiltinTools *[]string         `json:"builtin_tools,omitempty"`
	SessionID    string            `json:"session_id,omitempty"`
	Draft        string            `json:"draft,omitempty"`
	TaskContext  []taskContextItem `json:"task_context,omitempty"`
	Archived     bool              `json:"archived,omitempty"`
	ArchivedAt   int64             `json:"archived_at,omitempty"`

	PiBinding          *piConversationBinding  `json:"pi_binding,omitempty"`
	PreviousPiBindings []piConversationBinding `json:"previous_pi_bindings,omitempty"`
	PendingSend        *pendingSend            `json:"pending_send,omitempty"`
	SelectedIssueID    string                  `json:"selected_issue_id,omitempty"`
	UpdatedAt          int64                   `json:"updated_at,omitempty"`
}

type storedIssueExecution struct {
	ID                   string            `json:"id"`
	SessionID            string            `json:"session_id,omitempty"`
	TaskID               string            `json:"task_id,omitempty"`
	MessageID            string            `json:"message_id,omitempty"`
	Prompt               string            `json:"prompt,omitempty"`
	Status               string            `json:"status,omitempty"`
	Detail               string            `json:"detail,omitempty"`
	AttentionSeenStatus  string            `json:"attention_seen_status,omitempty"`
	RequirementsRevision int               `json:"requirements_revision,omitempty"`
	Context              []taskContextItem `json:"context,omitempty"`
	SupplementBatchID    string            `json:"supplement_batch_id,omitempty"`
	ToolOperationID      string            `json:"tool_operation_id,omitempty"`
	ToolScopeRef         string            `json:"tool_scope_ref,omitempty"`
	PiIntent             bool              `json:"pi_intent,omitempty"`
	PiRun                *piActivityRun    `json:"pi_run,omitempty"`
	ReturnedAt           int64             `json:"returned_at,omitempty"`
	WaitRequested        bool              `json:"wait_requested,omitempty"`
}

type storedRequirement struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	State     string `json:"state,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

type storedIssue struct {
	ID        string `json:"id"`
	ContactID string `json:"contact_id"`
	Goal      string `json:"goal"`

	RequirementsRevision int                    `json:"requirements_revision,omitempty"`
	RequirementItems     []storedRequirement    `json:"requirement_items,omitempty"`
	Status               string                 `json:"status,omitempty"`
	Archived             bool                   `json:"archived,omitempty"`
	Evidence             string                 `json:"evidence,omitempty"`
	CreatedAt            int64                  `json:"created_at,omitempty"`
	UpdatedAt            int64                  `json:"updated_at,omitempty"`
	PiBinding            *piActivityBinding     `json:"pi_binding,omitempty"`
	Handoff              *issueHandoff          `json:"handoff,omitempty"`
	WorkSessionID        string                 `json:"work_session_id,omitempty"`
	TaskContext          []taskContextItem      `json:"task_context,omitempty"`
	Supplements          *issueSupplementLedger `json:"supplements,omitempty"`
	Questions            []*issueQuestion       `json:"questions,omitempty"`
	Executions           []storedIssueExecution `json:"executions,omitempty"`
}

// storedLayout is the GUI-wide workbench preference. It is optional so
// stores written before #184 keep loading with the right work panel closed.
type storedLayout struct {
	LeftWidth     float32 `json:"left_width,omitempty"`
	RightWidth    float32 `json:"right_width,omitempty"`
	WorkspaceOpen bool    `json:"workspace_open"`
}

type storedState struct {
	Version             int                       `json:"version"`
	Selected            string                    `json:"selected,omitempty"`
	Contacts            []storedContact           `json:"contacts"`
	Issues              []storedIssue             `json:"issues,omitempty"`
	IssueToolOperations *IssueToolOperationLedger `json:"issue_tool_operations,omitempty"`
	IssueToolScopes     []IssueToolScope          `json:"issue_tool_scopes,omitempty"`
	Layout              *storedLayout             `json:"layout,omitempty"`
	ThemeIDs            *appearanceThemeIDs       `json:"theme_ids,omitempty"`
	Appearance          string                    `json:"appearance,omitempty"`
}

type contactStore struct {
	path            string
	state           storedState
	issueToolLedger *IssueToolOperationLedger
	issueToolScopes map[string]IssueToolScope
}

func newContactID() string {
	var bytes [6]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "contact-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("contact-%d", time.Now().UnixNano())
}

func nowMillis() int64 { return time.Now().UnixMilli() }

// openContactStore loads the store, or creates and persists a default Nagi
// contact when the file does not exist. A corrupt or unsupported file is
// reported and left untouched; the app must not silently replace it.
func openContactStore(path, defaultWorkspace string) (*contactStore, error) {
	store := &contactStore{path: path}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		store.state, err = decodeStoredState(path, data, false)
		if err != nil {
			return nil, err
		}
		if !store.hasContact(store.state.Selected) {
			store.state.Selected = store.state.Contacts[0].ID
		}
		store.restoreIssueToolState()
		return store, nil
	case errors.Is(err, os.ErrNotExist):
		store.state = storedState{
			Version: contactStoreVersion,
			Contacts: []storedContact{{
				ID:        newContactID(),
				Name:      "Nagi",
				Role:      "Assistant",
				Workspace: defaultWorkspace,
				UpdatedAt: nowMillis(),
			}},
		}
		store.state.Selected = store.state.Contacts[0].ID
		store.restoreIssueToolState()
		if err := store.saveState(); err != nil {
			return nil, err
		}
		return store, nil
	default:
		return nil, fmt.Errorf("contact store %s is not accessible: %w", path, err)
	}
}

func (s *contactStore) restoreIssueToolState() {
	if s.issueToolLedger == nil {
		s.issueToolLedger = cloneIssueToolLedger(s.state.IssueToolOperations)
	}
	if s.issueToolLedger == nil {
		s.issueToolLedger = &IssueToolOperationLedger{}
	}
	if s.issueToolScopes == nil {
		s.issueToolScopes = map[string]IssueToolScope{}
	}
	for _, scope := range s.state.IssueToolScopes {
		if scope.Validate() == nil {
			s.issueToolScopes[scope.Ref] = cloneIssueToolScope(scope)
		}
	}
}

func (s *contactStore) setIssueToolState(ledger *IssueToolOperationLedger, scopes map[string]IssueToolScope) {
	if ledger == nil {
		ledger = &IssueToolOperationLedger{}
	}
	s.issueToolLedger = ledger
	s.issueToolScopes = make(map[string]IssueToolScope, len(scopes))
	for ref, scope := range scopes {
		s.issueToolScopes[ref] = cloneIssueToolScope(scope)
	}
}

func (state storedState) validatePiBindings() error {
	for _, ct := range state.Contacts {
		if ct.SessionID != "" && (ct.PiBinding == nil || ct.PiBinding.Engine != "pi" || ct.PiBinding.Context.SessionRef != ct.SessionID) {
			return fmt.Errorf("contact %s has an invalid Pi session binding", ct.ID)
		}
	}
	for _, issue := range state.Issues {
		if !storedPiIssueValid(issue, state.IssueToolOperations) {
			return fmt.Errorf("issue %s has an invalid Pi execution record", issue.ID)
		}
		if issue.Handoff != nil && !storedIssueHandoffValid(issue, state) {
			return fmt.Errorf("issue %s has an invalid handoff", issue.ID)
		}
	}
	return nil
}

func storedPiIssueValid(issue storedIssue, ledger *IssueToolOperationLedger) bool {
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
		if execution.ReturnedAt != 0 && (issue.Handoff == nil || execution.ReturnedAt < issue.CreatedAt || execution.PiRun == nil || !execution.PiRun.Admitted || !readablePiIssueStatus(execution.Status)) {
			return false
		}
		if !execution.PiIntent || execution.ID == "" || execution.MessageID != "" {
			return false
		}
		if execution.PiRun == nil {
			if execution.SessionID != "" || execution.TaskID != "" || execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning || execution.Status == issueExecutionCompleted {
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
			!run.Admitted && (execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning || execution.Status == issueExecutionCompleted) {
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
		operation := ledger.operationByID(call.OperationID)
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
		if question.State == issueQuestionAnswered {
			if question.AnswerSupplementID == "" || issue.Supplements == nil {
				return false
			}
			item := issue.Supplements.itemByID(question.AnswerSupplementID)
			if item == nil || item.PiQuestionOperationID != call.OperationID || item.Text != question.AnswerText {
				return false
			}
		}
	}
	return true
}

func cloneIssueToolLedger(ledger *IssueToolOperationLedger) *IssueToolOperationLedger {
	if ledger == nil {
		return nil
	}
	cloned := *ledger
	if ledger.Operations != nil {
		cloned.Operations = make([]*IssueToolOperation, len(ledger.Operations))
		for i, operation := range ledger.Operations {
			if operation != nil {
				copy := *operation
				copy.PiCall = clonePiQuestionCall(operation.PiCall)
				copy.Receipt = cloneIssueToolReceipt(operation.Receipt)
				cloned.Operations[i] = &copy
			}
		}
	}
	return &cloned
}

func cloneIssueToolScope(scope IssueToolScope) IssueToolScope {
	if scope.AllowedIssueIDs != nil {
		scope.AllowedIssueIDs = append([]string{}, scope.AllowedIssueIDs...)
	}
	return scope
}

func cloneIssueQuestions(questions []*issueQuestion) []*issueQuestion {
	if questions == nil {
		return nil
	}
	cloned := make([]*issueQuestion, len(questions))
	for i, question := range questions {
		if question != nil {
			copy := *question
			copy.PiCall = clonePiQuestionCall(question.PiCall)
			copy.AnswerContext = cloneTaskContextItems(question.AnswerContext)
			cloned[i] = &copy
		}
	}
	return cloned
}

func clonePiConversationBindings(bindings []piConversationBinding) []piConversationBinding {
	if bindings == nil {
		return nil
	}
	cloned := make([]piConversationBinding, len(bindings))
	for i := range bindings {
		cloned[i] = *clonePiConversationBinding(&bindings[i])
	}
	return cloned
}

func cloneStoredState(state storedState) storedState {
	cloned := state
	if state.Contacts != nil {
		cloned.Contacts = append([]storedContact{}, state.Contacts...)
		for i := range cloned.Contacts {
			ct := &cloned.Contacts[i]
			ct.BuiltinTools = cloneBuiltinTools(ct.BuiltinTools)
			ct.TaskContext = cloneTaskContextItems(ct.TaskContext)
			ct.PiBinding = clonePiConversationBinding(ct.PiBinding)
			ct.PreviousPiBindings = clonePiConversationBindings(ct.PreviousPiBindings)
			ct.PendingSend = clonePendingSend(ct.PendingSend)
		}
	}
	if state.Issues != nil {
		cloned.Issues = append([]storedIssue{}, state.Issues...)
		for i := range cloned.Issues {
			issue := &cloned.Issues[i]
			if issue.RequirementItems != nil {
				issue.RequirementItems = append([]storedRequirement{}, issue.RequirementItems...)
			}
			issue.TaskContext = cloneTaskContextItems(issue.TaskContext)
			issue.PiBinding = clonePiActivityBinding(issue.PiBinding)
			issue.Supplements = cloneIssueSupplementLedger(issue.Supplements)
			issue.Questions = cloneIssueQuestions(issue.Questions)
			if issue.Executions != nil {
				issue.Executions = append([]storedIssueExecution{}, issue.Executions...)
				for j := range issue.Executions {
					issue.Executions[j].PiRun = clonePiActivityRun(issue.Executions[j].PiRun)
					issue.Executions[j].Context = cloneTaskContextItems(issue.Executions[j].Context)
				}
			}
		}
	}
	cloned.IssueToolOperations = cloneIssueToolLedger(state.IssueToolOperations)
	if state.IssueToolScopes != nil {
		cloned.IssueToolScopes = make([]IssueToolScope, len(state.IssueToolScopes))
		for i, scope := range state.IssueToolScopes {
			cloned.IssueToolScopes[i] = cloneIssueToolScope(scope)
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

func (s *contactStore) saveState() error {
	state := s.state
	state.IssueToolOperations = nil
	state.IssueToolScopes = nil
	candidate := cloneStoredState(state)
	candidate.IssueToolOperations = cloneIssueToolLedger(s.issueToolLedger)
	candidate.IssueToolScopes = s.issueToolScopesList()
	return s.commitState(candidate)
}

func (s *contactStore) commitState(candidate storedState) error {
	if err := candidate.validatePiBindings(); err != nil {
		return err
	}
	if err := writeProfileJSON(s.path, candidate); err != nil {
		return err
	}
	s.state = candidate
	return nil
}

type productPendingSend struct {
	ConversationID string `json:"conversationId"`
	OperationID    string `json:"operationId"`
}

type productUIState struct {
	Selected   string                        `json:"selected,omitempty"`
	Layout     storedLayout                  `json:"layout"`
	ThemeIDs   appearanceThemeIDs            `json:"themeIds"`
	Appearance string                        `json:"appearance,omitempty"`
	Pending    map[string]productPendingSend `json:"pending,omitempty"`
	RecoveryID string                        `json:"recoveryId,omitempty"`

	PendingActivities map[string]productActivityIntent `json:"pending_activities,omitempty"`
}

func productUIPath(profilePath string) string { return profilePath + ".ui.json" }

func readProductUI(profilePath string) (*productUIState, error) {
	data, err := os.ReadFile(productUIPath(profilePath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state productUIState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func writeProfileJSON(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create contact store directory: %w", err)
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

// save writes the current in-memory contacts. It is called from the change
// event (drafts) and from explicit product edits; it does not rewrite messages.
func (s *contactStore) save(contacts []*contact, selected string) error {
	return s.saveWithPreferences(contacts, selected, s.state.Appearance, s.state.ThemeIDs, s.state.Layout)
}

func (s *contactStore) saveWithPreferences(contacts []*contact, selected, appearance string, themeIDs *appearanceThemeIDs, layout *storedLayout) error {
	candidate := storedState{
		Version:             contactStoreVersion,
		Selected:            selected,
		Contacts:            make([]storedContact, 0, len(contacts)),
		IssueToolOperations: cloneIssueToolLedger(s.issueToolLedger),
		IssueToolScopes:     s.issueToolScopesList(),
		Appearance:          appearance,
	}
	if layout != nil {
		value := *layout
		candidate.Layout = &value
	}
	if themeIDs != nil {
		value := *themeIDs
		candidate.ThemeIDs = &value
	}
	for _, c := range contacts {
		storedContact := storedContact{
			ID:           c.ID,
			Name:         c.Name,
			Role:         c.Role,
			Purpose:      c.Purpose,
			Instructions: c.Instructions,
			Workspace:    c.Workspace,
			BuiltinTools: cloneBuiltinTools(c.BuiltinTools),
			SessionID:    c.SessionID,
			Draft:        c.Draft,
			TaskContext:  c.Context.snapshot(),
			Archived:     c.Archived,
			ArchivedAt:   c.ArchivedAt,

			PiBinding:          clonePiConversationBinding(c.PiBinding),
			PreviousPiBindings: c.piPreviousBindings(),
			PendingSend:        clonePendingSend(c.pendingSend),
			SelectedIssueID:    c.SelectedIssueID,
			UpdatedAt:          nowMillis(),
		}
		candidate.Contacts = append(candidate.Contacts, storedContact)
		for _, issue := range c.Issues {
			stored := storedIssue{
				ID: issue.ID, ContactID: issue.ContactID, Goal: issue.Goal,
				Status: issue.Status, Archived: issue.Archived,
				Evidence: issue.Evidence, CreatedAt: issue.CreatedAt, UpdatedAt: issue.UpdatedAt,
				PiBinding:     clonePiActivityBinding(issue.PiBinding),
				Handoff:       cloneIssueHandoff(issue.Handoff),
				WorkSessionID: issue.WorkSessionID, RequirementsRevision: issue.RequirementsRevision,
				TaskContext: issue.Context.snapshot(), Supplements: cloneIssueSupplementLedger(issue.Supplements),
				Questions: cloneIssueQuestions(issue.Questions),
			}
			for _, requirement := range issue.Requirements {
				if requirement == nil || strings.TrimSpace(requirement.Text) == "" {
					continue
				}
				stored.RequirementItems = append(stored.RequirementItems, storedRequirement{
					ID: requirement.ID, Text: requirement.Text, State: requirement.State,
					Evidence: requirement.Evidence, UpdatedAt: requirement.UpdatedAt,
				})
			}
			for _, execution := range issue.Executions {
				stored.Executions = append(stored.Executions, storedIssueExecution{
					ID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID,
					MessageID: execution.MessageID, Prompt: execution.Prompt,
					Status: execution.Status, Detail: execution.Detail,
					AttentionSeenStatus:  execution.AttentionSeenStatus,
					RequirementsRevision: execution.RequirementsRevision,
					Context:              cloneTaskContextItems(execution.Context),
					SupplementBatchID:    execution.SupplementBatchID,
					ToolOperationID:      execution.ToolOperationID,
					ToolScopeRef:         execution.ToolScopeRef,
					PiIntent:             execution.PiIntent, PiRun: clonePiActivityRun(execution.PiRun),
					ReturnedAt:    execution.ReturnedAt,
					WaitRequested: execution.WaitRequested,
				})
			}
			candidate.Issues = append(candidate.Issues, stored)
		}
	}
	if len(contacts) > 0 {
		found := false
		for _, ct := range candidate.Contacts {
			if ct.ID == selected {
				found = true
				break
			}
		}
		if !found {
			candidate.Selected = contacts[0].ID
		}
	}
	return s.commitState(candidate)
}

func (s *contactStore) issueToolScopesList() []IssueToolScope {
	if len(s.issueToolScopes) == 0 {
		return nil
	}
	out := make([]IssueToolScope, 0, len(s.issueToolScopes))
	for _, scope := range s.issueToolScopes {
		out = append(out, cloneIssueToolScope(scope))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

func (s *contactStore) hasContact(id string) bool {
	for _, c := range s.state.Contacts {
		if c.ID == id {
			return true
		}
	}
	return false
}

func decodeStoredState(path string, data []byte, strict bool) (storedState, error) {
	var state storedState
	var err error
	if strict {
		if !utf8.Valid(data) {
			return state, fmt.Errorf("contact store %s is not readable: expected UTF-8 JSON", path)
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
		return state, fmt.Errorf("contact store %s is not readable: %w", path, err)
	}
	if state.Version != contactStoreVersion {
		return state, fmt.Errorf("contact store %s has unsupported version %d", path, state.Version)
	}
	if len(state.Contacts) == 0 {
		return state, fmt.Errorf("contact store %s has no contacts", path)
	}
	contacts, issues := map[string]bool{}, map[string]bool{}
	for _, contact := range state.Contacts {
		if contact.ID == "" || contacts[contact.ID] {
			return state, fmt.Errorf("contact store %s has an empty or duplicate contact id", path)
		}
		contacts[contact.ID] = true
	}
	for _, issue := range state.Issues {
		if issue.ID == "" || issues[issue.ID] {
			return state, fmt.Errorf("contact store %s has an empty or duplicate issue id", path)
		}
		if issue.ContactID == "" || !contacts[issue.ContactID] {
			return state, fmt.Errorf("contact store %s issue %s has an unknown contact id", path, issue.ID)
		}
		issues[issue.ID] = true
	}
	return state, state.validatePiBindings()
}

func validateRecoveryState(state storedState) error {
	fail := func(field, message string) error { return fmt.Errorf("%s: %s", field, message) }
	if state.Version != contactStoreVersion {
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
				return fail(fmt.Sprintf("contacts[%d].selected_issue_id", i), "selected issue does not belong to this contact")
			}
		}
		if pending := contact.PendingSend; pending != nil && (pending.State == "submitting" || pending.State == "unknown" && contact.SessionID == "") {
			return fail(fmt.Sprintf("contacts[%d].pending_send", i), "this intent would change or be discarded on ordinary load")
		}
	}
	if !selected {
		return fail("selected", "selected contact is missing")
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
			if strings.TrimSpace(item.Text) == "" || item.State != requirementUnverified && item.State != requirementSatisfied && item.State != requirementUnmet {
				return fail(fmt.Sprintf("%s.requirement_items[%d]", field, j), "requirement would be dropped or normalized on ordinary load")
			}
		}
		for j, question := range issue.Questions {
			if question == nil || strings.TrimSpace(question.Ref) == "" {
				return fail(fmt.Sprintf("%s.questions[%d]", field, j), "question would be dropped on ordinary load")
			}
		}
		switch issue.Status {
		case issueStatusNotStarted, issueStatusInProgress, issueStatusWaitingUser, issueStatusNeedsAttention, issueStatusCompleted, issueStatusCanceled:
		default:
			return fail(field+".status", "issue status would be normalized on ordinary load")
		}
		for j, execution := range issue.Executions {
			if execution.ID == "" || execution.Status == "" || execution.Status == issueExecutionPending || execution.TaskID == "" && (execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning) {
				return fail(fmt.Sprintf("%s.executions[%d]", field, j), "execution would be dropped or normalized on ordinary load")
			}
		}
		if issue.Supplements != nil {
			for j, batch := range issue.Supplements.Batches {
				if batch != nil && batch.State == supplementBatchDispatching && batch.ExecutionID == "" {
					return fail(fmt.Sprintf("%s.supplements.batches[%d]", field, j), "batch would be normalized on ordinary load")
				}
			}
		}
	}
	if state.Appearance != "" && state.Appearance != appearanceSystem && state.Appearance != appearanceLight && state.Appearance != appearanceDark {
		return fail("appearance", "invalid appearance preference")
	}
	if layout := state.Layout; layout != nil {
		if layout.LeftWidth != 0 && (layout.LeftWidth < minLeftPaneWidth || layout.LeftWidth > maxLeftPaneWidth) || layout.RightWidth != 0 && (layout.RightWidth < minRightPaneWidth || layout.RightWidth > maxRightPaneWidth) {
			return fail("layout", "pane dimensions would be corrected on ordinary load")
		}
	}
	return nil
}

type storeRecoveryCopy struct {
	Path       string
	Contacts   int
	Issues     int
	ModifiedAt time.Time
	data       []byte
	info       os.FileInfo
}

func productProfileVersion(data []byte) bool {
	var marker struct {
		Version int `json:"version"`
	}
	return json.Unmarshal(data, &marker) == nil && marker.Version == 1
}

var errInvalidProductRecovery = errors.New("the current product profile contains unsupported or inconsistent records")

func inspectProductProfileBytes(data []byte, copy bool) (contacts, activities int, err error) {
	node, worker, err := piPayloadPaths()
	if err != nil {
		return 0, 0, err
	}
	module := filepath.Join(filepath.Dir(worker), "product-profile.js")
	const script = `
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
const { decodeProductProfile, inspectProductRecovery } = await import(pathToFileURL(process.argv[1]).href);
try {
  const data = JSON.parse(readFileSync(0, 'utf8'));
  if (process.argv[2] === 'copy') {
    process.stdout.write(JSON.stringify(inspectProductRecovery(data)));
  } else {
    decodeProductProfile(data);
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
			return 0, 0, errInvalidProductRecovery
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

type storeRecoveryTarget struct {
	path string
	data []byte
	info os.FileInfo
}

type storeRecoveryOutcome string

const (
	storeRecoveryNotInstalled       storeRecoveryOutcome = "not_installed"
	storeRecoveryVerifiedInstalled  storeRecoveryOutcome = "verified_installed"
	storeRecoveryInstallUnconfirmed storeRecoveryOutcome = "install_unconfirmed"
)

type storeRecoveryResult struct {
	Outcome     storeRecoveryOutcome
	OutputPath  string
	ArchivePath string
}

// A copy captures one saved point in time. Later operations use these validated
// bytes, never a live app projection or a newly read source with different facts.
func inspectStoreRecoveryCopy(path string) (*storeRecoveryCopy, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, info, err := readRecoveryFile(path)
	if err != nil {
		return nil, err
	}
	if productProfileVersion(data) {
		contacts, activities, err := inspectProductProfileBytes(data, true)
		if err != nil {
			return nil, fmt.Errorf("unsupported recovery copy %s: %w", path, err)
		}
		return &storeRecoveryCopy{Path: path, Contacts: contacts, Issues: activities, ModifiedAt: info.ModTime(), data: data, info: info}, nil
	}
	state, err := decodeStoredState(path, data, true)
	if err == nil {
		err = validateRecoveryState(state)
	}
	if err != nil {
		return nil, fmt.Errorf("unsupported recovery copy %s: %w", path, err)
	}
	return &storeRecoveryCopy{Path: path, Contacts: len(state.Contacts), Issues: len(state.Issues), ModifiedAt: info.ModTime(), data: data, info: info}, nil
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

// The caller chooses a complete destination filename and owns GUI admission.
// A collision is an error; this operation never overwrites or silently renames.
func writeStoreRecoveryCopy(copy *storeRecoveryCopy, destination string) (string, error) {
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

func inspectStoreRecoveryTarget(path string) (*storeRecoveryTarget, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, info, err := readRecoveryFile(path)
	if err != nil {
		return nil, err
	}
	if _, err := decodeStoredState(path, data, false); err == nil {
		return nil, fmt.Errorf("%s: the store is readable; recovery replacement is not allowed", path)
	}
	if productProfileVersion(data) {
		_, _, err := inspectProductProfileBytes(data, false)
		if err == nil {
			return nil, fmt.Errorf("%s: the store is readable; recovery replacement is not allowed", path)
		}
		if !errors.Is(err, errInvalidProductRecovery) {
			return nil, err
		}
	}
	return &storeRecoveryTarget{path: path, data: data, info: info}, nil
}

func (target *storeRecoveryTarget) unchanged() error {
	current, err := inspectStoreRecoveryTarget(target.path)
	if err != nil {
		return err
	}
	if !os.SameFile(target.info, current.info) || !bytes.Equal(target.data, current.data) {
		return fmt.Errorf("%s: the unavailable store changed after inspection", target.path)
	}
	return nil
}

// The UI must explicitly confirm pausing and reserve quiescent unavailable
// state before calling. A successful Rename is never rolled back or retried:
// failed readback means install_unconfirmed, not that nothing was installed.
func restoreStoreRecoveryCopy(copy *storeRecoveryCopy, target *storeRecoveryTarget) (storeRecoveryResult, error) {
	return restoreStoreRecoveryCopyWithRename(copy, target, os.Rename)
}

func restoreStoreRecoveryCopyWithRename(copy *storeRecoveryCopy, target *storeRecoveryTarget, rename func(string, string) error) (storeRecoveryResult, error) {
	result := storeRecoveryResult{Outcome: storeRecoveryNotInstalled}
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
	if productProfileVersion(copy.data) {
		if _, _, err := inspectProductProfileBytes(copy.data, true); err != nil {
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
		recovery.ID = newContactID()
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
		state, err := decodeStoredState(copy.Path, copy.data, true)
		if err != nil {
			return result, err
		}
		if err := validateRecoveryState(state); err != nil {
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
	result.Outcome = storeRecoveryInstallUnconfirmed
	installed, info, err := readRecoveryFile(target.path)
	if err == nil && (!os.SameFile(stageInfo, info) || !bytes.Equal(installed, paused)) {
		err = errors.New("installed data readback did not match the paused recovery copy")
	}
	if err != nil {
		return result, fmt.Errorf("installation result must be checked before any retry: %w", err)
	}
	result.Outcome = storeRecoveryVerifiedInstalled
	return result, nil
}
