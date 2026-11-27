package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

type TaskContextItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Source   string `json:"source,omitempty"`
	Text     string `json:"text"`
	Revision uint64 `json:"revision"`
}

func NewTaskContextID() string {
	return "context-" + rand.Text()
}

// InputName is the single label projection used for both the size budget and
// the caller-context wire value. Named snippets stay just their name; file
// snapshots make the explicit source path visible without changing Text.
func (item TaskContextItem) InputName() string {
	if item.Source == "" {
		return item.Name
	}
	return item.Name + " · file " + item.Source
}

func CloneTaskContextItems(items []TaskContextItem) []TaskContextItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]TaskContextItem, len(items))
	copy(out, items)
	return out
}

type PendingSend struct {
	Draft   string            `json:"draft,omitempty"`
	Context []TaskContextItem `json:"context,omitempty"`
	State   string            `json:"state,omitempty"`
	// ToolScopeRef binds the app-issued issue-tool scope to the one pending
	// conversation turn. It is not a user-visible field and is cleared with the
	// pending send after acceptance.
	ToolScopeRef string `json:"tool_scope_ref,omitempty"`
}

func ClonePendingSend(send *PendingSend) *PendingSend {
	if send == nil {
		return nil
	}
	return &PendingSend{
		Draft:        send.Draft,
		Context:      CloneTaskContextItems(send.Context),
		State:        send.State,
		ToolScopeRef: send.ToolScopeRef,
	}
}

type PiSelection struct {
	CWD          string   `json:"cwd"`
	Instructions string   `json:"instructions"`
	CodingTools  []string `json:"coding_tools"`
}

type PiConfiguration struct {
	WireAPI          pi.WireAPI `json:"wire_api"`
	BaseURL          string     `json:"base_url"`
	Model            string     `json:"model"`
	ContextTokens    int        `json:"context_tokens"`
	OutputTokens     int        `json:"output_tokens"`
	CredentialSource string     `json:"credential_source"`
}

type PiConversationBinding struct {
	Engine        string            `json:"engine"`
	Context       pi.ContextBinding `json:"context"`
	Selection     PiSelection       `json:"selection"`
	Configuration PiConfiguration   `json:"configuration"`
}

type PiActivityBinding struct {
	Engine        string            `json:"engine"`
	Context       pi.ContextBinding `json:"context"`
	Selection     PiSelection       `json:"selection"`
	Configuration PiConfiguration   `json:"configuration"`
}

type PiActivityRun struct {
	WorkerEpoch     string `json:"worker_epoch"`
	ContextID       string `json:"context_id"`
	SessionRef      string `json:"session_ref"`
	RunID           string `json:"run_id"`
	CancelRequested bool   `json:"cancel_requested,omitempty"`
	Admitted        bool   `json:"admitted"`
}

type PiQuestionCall struct {
	WorkerEpoch string `json:"worker_epoch"`
	ContextID   string `json:"context_id"`
	RunID       string `json:"run_id"`
	CallID      string `json:"call_id"`
	OperationID string `json:"operation_id"`
	ExecutionID string `json:"execution_id"`
	ContactID   string `json:"contact_id"`
	IssueID     string `json:"issue_id"`
	SessionRef  string `json:"session_ref"`
}

func ClonePiConversationBinding(binding *PiConversationBinding) *PiConversationBinding {
	if binding == nil {
		return nil
	}
	cloned := *binding
	if binding.Selection.CodingTools != nil {
		cloned.Selection.CodingTools = append([]string{}, binding.Selection.CodingTools...)
	}
	return &cloned
}

func ClonePiActivityBinding(binding *PiActivityBinding) *PiActivityBinding {
	if binding == nil {
		return nil
	}
	copied := *binding
	if binding.Selection.CodingTools != nil {
		copied.Selection.CodingTools = append([]string{}, binding.Selection.CodingTools...)
	}
	return &copied
}

func ClonePiActivityRun(run *PiActivityRun) *PiActivityRun {
	if run == nil {
		return nil
	}
	copied := *run
	return &copied
}

func ClonePiQuestionCall(call *PiQuestionCall) *PiQuestionCall {
	if call == nil {
		return nil
	}
	copied := *call
	return &copied
}

const (
	IssueQuestionOpen                = "open"
	IssueQuestionAnswered            = "answered"
	IssueQuestionSuperseded          = "superseded"
	IssueQuestionDeliverySaved       = "saved"
	IssueQuestionDeliveryDispatching = "dispatching"
	IssueQuestionDeliveryResolved    = "resolved"
	IssueQuestionDeliveryUnknown     = "unknown"
	IssueQuestionDeliveryCanceled    = "canceled"
)

type IssueQuestion struct {
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
	PiCall               *PiQuestionCall   `json:"pi_call,omitempty"`
	DeliveryState        string            `json:"delivery_state,omitempty"`
	AnswerContext        []TaskContextItem `json:"answer_context,omitempty"`
	AnswerText           string            `json:"answer_text,omitempty"`
}

func NewIssueQuestionRef() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "question-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("question-%d", time.Now().UnixNano())
}

type IssueRequirement struct {
	ID        string
	Text      string
	State     string
	Evidence  string
	UpdatedAt int64
}

// IssueExecution is the persisted association for one attempt on an issue.
// Prompt is the exact pre-dispatch snapshot; Runtime history remains the
// authority for task/message/tool results. AttentionSeenStatus tracks the
// last displayed exception status and is empty when unseen.
type IssueExecution struct {
	ID                   string
	SessionID            string
	TaskID               string
	MessageID            string
	Prompt               string
	Status               string
	Detail               string
	AttentionSeenStatus  string
	RequirementsRevision int
	Context              []TaskContextItem
	ContextExpanded      bool
	SupplementBatchID    string
	ToolOperationID      string
	ToolScopeRef         string
	PiIntent             bool
	PiRun                *PiActivityRun
	ReturnedAt           int64
	WaitRequested        bool
}

const (
	IssueStatusNotStarted     = "not_started"
	IssueStatusInProgress     = "in_progress"
	IssueStatusWaitingUser    = "waiting_user"
	IssueStatusNeedsAttention = "needs_attention"
	IssueStatusCompleted      = "completed"
	IssueStatusCanceled       = "canceled"

	RequirementUnverified = "unverified"
	RequirementSatisfied  = "satisfied"
	RequirementUnmet      = "unmet"

	IssueExecutionPending       = "pending"
	IssueExecutionNotDispatched = "not_dispatched"
	IssueExecutionRejected      = "rejected"
	IssueExecutionAccepted      = "accepted"
	IssueExecutionRunning       = "running"
	IssueExecutionCompleted     = "completed"
	IssueExecutionFailed        = "failed"
	IssueExecutionInterrupted   = "interrupted"
	IssueExecutionUnknown       = "unknown"
)

func NewIssueID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "issue-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("issue-%d", time.Now().UnixNano())
}

func NewRequirementID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "requirement-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("requirement-%d", time.Now().UnixNano())
}

func ParseIssueRequirements(text string) []*IssueRequirement {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []*IssueRequirement
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, &IssueRequirement{ID: NewRequirementID(), Text: line, State: RequirementUnverified})
	}
	return out
}

type ProductActivityIntent struct {
	OperationID string               `json:"operation_id"`
	BotID       string               `json:"bot_id"`
	ActivityID  string               `json:"activity_id"`
	Action      string               `json:"action"`
	Revision    uint64               `json:"revision"`
	ExecutionID string               `json:"execution_id,omitempty"`
	QuestionID  string               `json:"question_id,omitempty"`
	Text        string               `json:"text,omitempty"`
	Materials   []pi.ProductMaterial `json:"materials,omitempty"`

	SelectedOptionID string `json:"selected_option_id,omitempty"`
}

func CloneBuiltinTools(selection *[]string) *[]string {
	if selection == nil {
		return nil
	}
	names := append([]string(nil), (*selection)...)
	if names == nil {
		names = []string{}
	}
	return &names
}

func NowMillis() int64 { return time.Now().UnixMilli() }

type productRecoveryCounts struct {
	Contacts       int `json:"contacts"`
	Activities     int `json:"activities"`
	Timers         int `json:"timers"`
	Schedules      int `json:"schedules"`
	DormantWatches int `json:"dormantWatches"`
}

type PendingWatchOperation struct {
	ID       string `json:"id"`
	BotID    string `json:"bot_id"`
	WatchID  string `json:"watch_id,omitempty"`
	GrantID  string `json:"grant_id,omitempty"`
	Revision uint64 `json:"revision,omitempty"`
	Action   string `json:"action"`
}
