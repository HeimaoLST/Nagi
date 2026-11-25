package pi

import (
	"encoding/json"
	"fmt"
)

type Options struct {
	NodePath   string
	WorkerPath string
	StateDir   string
	Handlers   Handlers
}

type Handlers struct {
	OnReady         func(Ready)
	OnEvent         func(Event)
	OnProtocolError func(error)
	OnExit          func(ProcessOutcome)
}

const (
	ProtocolVersion = 1
	MaxFrameBytes   = 8 * 1024 * 1024
)

type WireAPI string

const (
	OpenAICompletions WireAPI = "openai-completions"
	OpenAIResponses   WireAPI = "openai-responses"
)

type Purpose string

const (
	Conversation Purpose = "conversation"
	Activity     Purpose = "activity"
)

type ComponentVersion struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type BuiltinTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type BuiltinCatalog struct {
	Supported []BuiltinTool `json:"supported"`
	Defaults  []string      `json:"defaults"`
}

type Ready struct {
	Version        int              `json:"v"`
	Type           string           `json:"type"`
	WorkerEpoch    string           `json:"workerEpoch"`
	Engine         ComponentVersion `json:"engine"`
	Runtime        ComponentVersion `json:"runtime"`
	BuiltinCatalog *BuiltinCatalog  `json:"builtinCatalog"`
}

type ConfigureParams struct {
	WireAPI          WireAPI `json:"wireApi"`
	BaseURL          string  `json:"baseUrl"`
	APIKey           string  `json:"apiKey"`
	Model            string  `json:"model"`
	ContextTokens    int     `json:"contextTokens"`
	OutputTokens     int     `json:"outputTokens"`
	CredentialSource string  `json:"credentialSource"`
}

type ConfigureResult struct {
	Configured       bool   `json:"configured"`
	WorkerEpoch      string `json:"workerEpoch"`
	EngineVersion    string `json:"engineVersion"`
	PricingKnown     bool   `json:"pricingKnown"`
	CredentialSource string `json:"credentialSource"`
}

type ContextSettings struct {
	Model         string `json:"model"`
	ContextTokens int    `json:"contextTokens"`
	OutputTokens  int    `json:"outputTokens"`
}

type ConfigureContextParams struct {
	ContextID string          `json:"contextId"`
	Settings  ContextSettings `json:"settings"`
}

type ContextConfiguration struct {
	ContextID         string   `json:"contextId"`
	WireAPI           WireAPI  `json:"wireApi"`
	BaseURL           string   `json:"baseUrl"`
	Model             string   `json:"model"`
	ContextTokens     int      `json:"contextTokens"`
	OutputTokens      int      `json:"outputTokens"`
	CredentialSource  string   `json:"credentialSource"`
	CredentialPresent bool     `json:"credentialPresent"`
	ActiveTools       []string `json:"activeTools"`
}

type DomainTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type MCPServer struct {
	Name     string          `json:"name"`
	SourceID string          `json:"sourceId"`
	Config   json.RawMessage `json:"config"`
}

type OpenContextParams struct {
	ContextID         string           `json:"contextId"`
	BotID             string           `json:"botId"`
	Purpose           Purpose          `json:"purpose"`
	ActivityID        string           `json:"activityId,omitempty"`
	SelectionRevision uint64           `json:"selectionRevision"`
	Instructions      string           `json:"instructions"`
	CWD               string           `json:"cwd"`
	ResumeRef         string           `json:"resumeRef,omitempty"`
	Used              bool             `json:"used"`
	CodingTools       []string         `json:"codingTools"`
	DomainTools       []DomainTool     `json:"domainTools"`
	Configuration     *ConfigureParams `json:"configuration,omitempty"`
	MCPServers        []MCPServer      `json:"mcpServers,omitempty"`
}

type ContextBinding struct {
	ContextID         string  `json:"contextId"`
	BotID             string  `json:"botId"`
	Purpose           Purpose `json:"purpose"`
	ActivityID        string  `json:"activityId,omitempty"`
	SelectionRevision uint64  `json:"selectionRevision"`
	CWD               string  `json:"cwd"`
	SessionRef        string  `json:"sessionRef"`
	Used              bool    `json:"used"`
}

type ContextParams struct {
	ContextID string `json:"contextId"`
}

type HistoryParams struct {
	ContextID string `json:"contextId"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit"`
}

type HistoryResult struct {
	ContextID  string                     `json:"contextId"`
	SessionRef string                     `json:"sessionRef"`
	Entries    []json.RawMessage          `json:"entries"`
	NextCursor string                     `json:"nextCursor,omitempty"`
	Projection map[string]json.RawMessage `json:"projection"`
}

type ActiveRun struct {
	RunID           string `json:"runId"`
	CancelRequested bool   `json:"cancelRequested"`
}

type InspectResult struct {
	ContextID         string            `json:"contextId"`
	SessionRef        string            `json:"sessionRef"`
	ModelContext      json.RawMessage   `json:"modelContext"`
	FinalizedMessages []json.RawMessage `json:"finalizedMessages"`
	Partial           json.RawMessage   `json:"partial,omitempty"`
	ActiveRun         *ActiveRun        `json:"activeRun,omitempty"`
	ObservationSeq    uint64            `json:"observationSeq"`
	CapturedAt        string            `json:"capturedAt"`
	Complete          bool              `json:"complete"`
}

type ContextInput struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

type RunCorrelation struct {
	TurnID      string `json:"turnId,omitempty"`
	ExecutionID string `json:"executionId,omitempty"`
	ScopeRef    string `json:"scopeRef,omitempty"`
}

type StartRunParams struct {
	ContextID   string          `json:"contextId"`
	RunID       string          `json:"runId"`
	Text        string          `json:"text"`
	Context     []ContextInput  `json:"context,omitempty"`
	Correlation *RunCorrelation `json:"correlation,omitempty"`
}

type RunAdmission struct {
	ContextID string `json:"contextId"`
	RunID     string `json:"runId"`
	Admitted  bool   `json:"admitted"`
}

type CancelRunParams struct {
	ContextID string `json:"contextId"`
	RunID     string `json:"runId"`
}

type CancelResult struct {
	ContextID       string `json:"contextId"`
	RunID           string `json:"runId"`
	CancelRequested bool   `json:"cancelRequested"`
}

type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

type ToolResultParams struct {
	WorkerEpoch string          `json:"workerEpoch"`
	ContextID   string          `json:"contextId"`
	RunID       string          `json:"runId"`
	CallID      string          `json:"callId"`
	Content     []ContentBlock  `json:"content,omitempty"`
	Details     json.RawMessage `json:"details,omitempty"`
	Error       *RemoteError    `json:"error,omitempty"`
}

type ToolResultReceipt struct {
	Resolved bool `json:"resolved"`
}

type CloseContextResult struct {
	ContextID string `json:"contextId"`
	Closed    bool   `json:"closed"`
}

type ShutdownResult struct {
	ShuttingDown bool `json:"shuttingDown"`
}

type EventName string

const (
	RunStarted   EventName = "run.started"
	TextDelta    EventName = "text.delta"
	ToolRequest  EventName = "tool.request"
	ToolFinished EventName = "tool.finished"
	RunSettled   EventName = "run.settled"
	MCPProcess   EventName = "mcp.process"
)

type Event struct {
	Version     int             `json:"v"`
	Type        string          `json:"type"`
	WorkerEpoch string          `json:"workerEpoch"`
	ContextID   string          `json:"contextId"`
	RunID       string          `json:"runId"`
	Seq         uint64          `json:"seq"`
	Name        EventName       `json:"event"`
	Data        json.RawMessage `json:"data"`
}

type ToolRequestData struct {
	CallID string          `json:"callId"`
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input"`
}

type ToolFinishedData struct {
	CallID  string          `json:"callId"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	IsError bool            `json:"isError"`
	Result  json.RawMessage `json:"result"`
}

type MCPProcessError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type MCPProcessEvent struct {
	Server        string           `json:"server"`
	SourceID      string           `json:"sourceId"`
	AttemptID     string           `json:"attemptId"`
	Kind          string           `json:"kind"`
	PID           *int             `json:"pid,omitempty"`
	Code          *int             `json:"code"`
	Signal        *string          `json:"signal"`
	SpawnObserved *bool            `json:"spawnObserved,omitempty"`
	ExitObserved  *bool            `json:"exitObserved,omitempty"`
	Error         *MCPProcessError `json:"error,omitempty"`
}

type RunStatus string

const (
	Completed   RunStatus = "completed"
	Interrupted RunStatus = "interrupted"
	Failed      RunStatus = "failed"
)

type RunSettledData struct {
	Status RunStatus       `json:"status"`
	Text   string          `json:"text"`
	Error  *RemoteError    `json:"error,omitempty"`
	Usage  json.RawMessage `json:"usage,omitempty"`
}

type RemoteError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RemoteError) Error() string {
	return e.Code + ": " + e.Message
}

type Delivery string

const (
	NotSent    Delivery = "not_sent"
	Unanswered Delivery = "unanswered"
	Rejected   Delivery = "rejected"
)

type CommandError struct {
	Method    string
	RequestID string
	Delivery  Delivery
	Err       error
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("pi %s (%s): %v", e.Method, e.Delivery, e.Err)
}

func (e *CommandError) Unwrap() error {
	return e.Err
}

type ProcessOutcome struct {
	PID      int
	Reaped   bool
	ExitCode int
	Err      error
}
