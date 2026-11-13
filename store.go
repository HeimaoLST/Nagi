package main

// GUI-owned contact store. It persists only product fields and the mapping to
// a runtime session_id; messages/history stay in the runtime and are read via
// session.history. The JSON file is private to the GUI.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// v3 added issue.work_session_id. v4 made structured requirements the
// persisted truth. v5 added owned task-context snapshots and pending-send
// intent. v6 added the issue supplement ledger. v7 adds contact archive state
// and explicit references to conversations replaced by Save and start new.
// v8 adds the conversation issue-tool scope/operation ledger. v9 adds
// persistent missing-information questions and their answer associations.
// v1-v8 remain readable here. Older apps reject v9 instead of dropping fields.
const contactStoreVersion = 9

type storedContact struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Role             string            `json:"role,omitempty"`
	Purpose          string            `json:"purpose,omitempty"`
	Instructions     string            `json:"instructions,omitempty"`
	Workspace        string            `json:"workspace,omitempty"`
	BuiltinTools     *[]string         `json:"builtin_tools,omitempty"`
	SessionID        string            `json:"session_id,omitempty"`
	Draft            string            `json:"draft,omitempty"`
	TaskContext      []taskContextItem `json:"task_context,omitempty"`
	Archived         bool              `json:"archived,omitempty"`
	ArchivedAt       int64             `json:"archived_at,omitempty"`
	PreviousSessions []string          `json:"previous_sessions,omitempty"`
	PendingSend      *pendingSend      `json:"pending_send,omitempty"`
	SelectedIssueID  string            `json:"selected_issue_id,omitempty"`
	UpdatedAt        int64             `json:"updated_at,omitempty"`
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
	ToolScopeInput       string            `json:"tool_scope_input,omitempty"`
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
	ID                   string                 `json:"id"`
	ContactID            string                 `json:"contact_id"`
	Goal                 string                 `json:"goal"`
	Requirements         string                 `json:"requirements,omitempty"` // legacy v1-v3 only
	RequirementsRevision int                    `json:"requirements_revision,omitempty"`
	RequirementItems     []storedRequirement    `json:"requirement_items,omitempty"`
	Status               string                 `json:"status,omitempty"`
	Archived             bool                   `json:"archived,omitempty"`
	Evidence             string                 `json:"evidence,omitempty"`
	CreatedAt            int64                  `json:"created_at,omitempty"`
	UpdatedAt            int64                  `json:"updated_at,omitempty"`
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

// openContactStore loads the store, or creates and persists a default Saki
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
				Name:      "Saki",
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
		s.issueToolLedger = s.state.IssueToolOperations
	}
	if s.issueToolLedger == nil {
		s.issueToolLedger = &IssueToolOperationLedger{}
	}
	if s.issueToolScopes == nil {
		s.issueToolScopes = map[string]IssueToolScope{}
	}
	for _, scope := range s.state.IssueToolScopes {
		if scope.Validate() == nil {
			s.issueToolScopes[scope.Ref] = scope
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
		s.issueToolScopes[ref] = scope
	}
}

func (s *contactStore) saveState() error {
	s.state.IssueToolOperations = s.issueToolLedger
	s.state.IssueToolScopes = s.issueToolScopesList()
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create contact store directory: %w", err)
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
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
	return os.Rename(tempName, s.path)
}

// save writes the current in-memory contacts. It is called from the change
// event (drafts) and from explicit product edits; it does not rewrite messages.
func (s *contactStore) save(contacts []*contact, selected string) error {
	s.state = storedState{
		Version:             contactStoreVersion,
		Selected:            selected,
		Contacts:            make([]storedContact, 0, len(contacts)),
		IssueToolOperations: s.issueToolLedger,
		IssueToolScopes:     s.issueToolScopesList(),
		Layout:              s.state.Layout,
		Appearance:          s.state.Appearance,
		ThemeIDs:            s.state.ThemeIDs,
	}
	for _, c := range contacts {
		storedContact := storedContact{
			ID:               c.ID,
			Name:             c.Name,
			Role:             c.Role,
			Purpose:          c.Purpose,
			Instructions:     c.Instructions,
			Workspace:        c.Workspace,
			BuiltinTools:     cloneBuiltinTools(c.BuiltinTools),
			SessionID:        c.SessionID,
			Draft:            c.Draft,
			TaskContext:      c.Context.snapshot(),
			Archived:         c.Archived,
			ArchivedAt:       c.ArchivedAt,
			PreviousSessions: append([]string(nil), c.PreviousSessions...),
			PendingSend:      clonePendingSend(c.pendingSend),
			SelectedIssueID:  c.SelectedIssueID,
			UpdatedAt:        nowMillis(),
		}
		s.state.Contacts = append(s.state.Contacts, storedContact)
		for _, issue := range c.Issues {
			stored := storedIssue{
				ID: issue.ID, ContactID: issue.ContactID, Goal: issue.Goal,
				Status: issue.Status, Archived: issue.Archived,
				Evidence: issue.Evidence, CreatedAt: issue.CreatedAt, UpdatedAt: issue.UpdatedAt,
				WorkSessionID: issue.WorkSessionID, RequirementsRevision: issue.RequirementsRevision,
				TaskContext: issue.Context.snapshot(), Supplements: issue.Supplements,
				Questions: issue.Questions,
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
					ToolScopeInput:       execution.ToolScopeInput,
					WaitRequested:        execution.WaitRequested,
				})
			}
			s.state.Issues = append(s.state.Issues, stored)
		}
	}
	if !s.hasContact(selected) && len(contacts) > 0 {
		s.state.Selected = contacts[0].ID
	}
	return s.saveState()
}

func (s *contactStore) issueToolScopesList() []IssueToolScope {
	if len(s.issueToolScopes) == 0 {
		return nil
	}
	out := make([]IssueToolScope, 0, len(s.issueToolScopes))
	for _, scope := range s.issueToolScopes {
		out = append(out, scope)
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
	if state.Version < 1 || state.Version > contactStoreVersion {
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
	return state, nil
}

func validateRecoveryState(state storedState) error {
	fail := func(field, message string) error { return fmt.Errorf("%s: %s", field, message) }
	if state.Version != contactStoreVersion {
		return fail("version", "only current version 9 recovery copies are supported")
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
		if issue.Requirements != "" {
			return fail(field+".requirements", "legacy requirements are not supported in a version 9 recovery copy")
		}
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
	state, err := decodeStoredState(copy.Path, copy.data, true)
	if err == nil {
		err = validateRecoveryState(state)
	}
	if err != nil {
		return result, err
	}
	for i := range state.Issues {
		if ledger := state.Issues[i].Supplements; ledger != nil {
			ledger.AutoContinue = false
		}
	}
	paused, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return result, err
	}
	paused = append(paused, '\n')
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
