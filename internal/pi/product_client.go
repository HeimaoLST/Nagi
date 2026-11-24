package pi

import (
	"context"
	"encoding/json"
	"errors"
)

const ProductVersion = 1

const SystemKindBotArchitect = "bot_architect"

var ErrProductStreamGap = errors.New("product event stream has a sequence gap")

type ProductOptions struct {
	NodePath        string
	WorkerPath      string
	StateDir        string
	ProfilePath     string
	CredentialsPath string
	FreshProfile    bool
	Handlers        ProductHandlers
}

type ProductHandlers struct {
	OnEvent       func(ProductEvent)
	OnStreamGap   func()
	OnProtocolErr func(error)
	OnExit        func(ProcessOutcome)
}

type ProductClient struct {
	client *Client
}

type ProductEvent struct {
	StreamID string          `json:"streamId"`
	Seq      uint64          `json:"seq"`
	Name     string          `json:"event"`
	Data     json.RawMessage `json:"data"`
}

type ProductExecutionEvent struct {
	ConversationID string `json:"conversationId,omitempty"`
	ActivityID     string `json:"activityId,omitempty"`
	ExecutionID    string `json:"executionId"`
	OperationID    string `json:"operationId"`
	Revision       uint64 `json:"revision,omitempty"`
	State          string `json:"state,omitempty"`
	Offset         uint64 `json:"offset,omitempty"`
	Text           string `json:"text,omitempty"`
	Error          string `json:"error,omitempty"`
}

type ProductObjectChanged struct {
	ConversationID string `json:"conversationId,omitempty"`
	ActivityID     string `json:"activityId,omitempty"`
	Revision       uint64 `json:"revision"`
}

type ProductRemoteError struct {
	RemoteError
	Details json.RawMessage `json:"details,omitempty"`
}

func (e *ProductRemoteError) Unwrap() error { return &e.RemoteError }

type ProductPageParams struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type ProductPage[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

type ProductBotProfile struct {
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Purpose      string   `json:"purpose"`
	Instructions string   `json:"instructions"`
	Workspace    string   `json:"workspace"`
	CodingTools  []string `json:"codingTools"`
}

type ProductBot struct {
	BotID                 string            `json:"botId"`
	Profile               ProductBotProfile `json:"profile"`
	ProfileRevision       uint64            `json:"profileRevision"`
	Archived              bool              `json:"archived"`
	CurrentConversationID string            `json:"currentConversationId"`
	SystemKind            string            `json:"systemKind,omitempty"`
	PendingLearnings      int               `json:"pendingLearnings,omitempty"`
}

type ProductBotChangedEvent struct {
	Bot    ProductBot `json:"bot"`
	Change string     `json:"change"`
}

type ProductMemoryRule struct {
	RuleID    string `json:"ruleId"`
	BotID     string `json:"botId"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type ProductWorkLog struct {
	LogID       string   `json:"logId"`
	BotID       string   `json:"botId"`
	Date        string   `json:"date"`
	Summary     string   `json:"summary"`
	Tags        []string `json:"tags"`
	ExecutionID string   `json:"executionId,omitempty"`
	CreatedAt   string   `json:"createdAt"`
}

type ProductPendingLearning struct {
	LearningID  string `json:"learningId"`
	BotID       string `json:"botId"`
	Kind        string `json:"kind"`
	Text        string `json:"text"`
	Reason      string `json:"reason"`
	ExecutionID string `json:"executionId,omitempty"`
	State       string `json:"state"`
	CreatedAt   string `json:"createdAt"`
	ReviewedAt  string `json:"reviewedAt,omitempty"`
}

type ProductBotMemory struct {
	BotID            string                   `json:"botId"`
	RulesRevision    uint64                   `json:"rulesRevision"`
	Rules            []ProductMemoryRule      `json:"rules"`
	PendingLearnings []ProductPendingLearning `json:"pendingLearnings"`
	WorkLogs         []ProductWorkLog         `json:"workLogs"`
}

type ProductMemoryChangedEvent struct {
	BotID         string `json:"botId"`
	RulesRevision uint64 `json:"rulesRevision"`
	PendingCount  int    `json:"pendingCount"`
}

type ProductOpenResult struct {
	Version  int             `json:"version"`
	StreamID string          `json:"streamId"`
	EventSeq uint64          `json:"eventSeq"`
	Snapshot ProductSnapshot `json:"snapshot"`
}

type ProductSnapshot struct {
	Bots          []ProductBot            `json:"bots"`
	NextBotCursor *string                 `json:"nextBotCursor"`
	Connection    ProductConnectionStatus `json:"connection"`
	Preferences   *ProductPreferences     `json:"preferences,omitempty"`
	Recovery      *ProductRecovery        `json:"recovery,omitempty"`
}

type ProductRecovery struct {
	ID string          `json:"id"`
	UI json.RawMessage `json:"ui,omitempty"`
}

type ProductPreferences struct {
	SelectedBotID string          `json:"selectedBotId,omitempty"`
	Layout        json.RawMessage `json:"layout,omitempty"`
	ThemeIDs      json.RawMessage `json:"themeIds,omitempty"`
	Appearance    string          `json:"appearance,omitempty"`
}

type ProductMaterial struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
	Name     string `json:"name"`
	Text     string `json:"text"`
	Source   string `json:"source,omitempty"`
}

type ProductDraft struct {
	Revision  uint64            `json:"revision"`
	Text      string            `json:"text"`
	Materials []ProductMaterial `json:"materials"`
}

type ProductExecution struct {
	ExecutionID string  `json:"executionId"`
	OperationID string  `json:"operationId,omitempty"`
	Revision    uint64  `json:"revision"`
	State       string  `json:"state"`
	Partial     *string `json:"partial,omitempty"`
	TextOffset  *uint64 `json:"textOffset,omitempty"`
	Error       string  `json:"error,omitempty"`
}

type ProductConversationSummary struct {
	ConversationID string `json:"conversationId"`
	BotID          string `json:"botId"`
	Mode           string `json:"mode"`
	Revision       uint64 `json:"revision"`
	Preview        string `json:"preview,omitempty"`
	Timestamp      string `json:"timestamp,omitempty"`
}

type ProductConversation struct {
	ProductConversationSummary
	Draft           *ProductDraft     `json:"draft"`
	LatestExecution *ProductExecution `json:"latestExecution"`
}

type ProductMessage struct {
	MessageID string            `json:"messageId"`
	Role      string            `json:"role"`
	Timestamp string            `json:"timestamp"`
	Text      string            `json:"text"`
	Materials []ProductMaterial `json:"materials"`
}

type ProductReceiptTarget struct {
	BotID          string `json:"botId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	ActivityID     string `json:"activityId,omitempty"`
	QuestionID     string `json:"questionId,omitempty"`
	Connection     bool   `json:"connection,omitempty"`
}

type ProductReceipt struct {
	OperationID string               `json:"operationId"`
	State       string               `json:"state"`
	Target      ProductReceiptTarget `json:"target"`
	Execution   *ProductExecution    `json:"execution,omitempty"`
	Result      json.RawMessage      `json:"result,omitempty"`
}

type ProductActivitySummary struct {
	ActivityID           string   `json:"activityId"`
	OwnerBotID           string   `json:"ownerBotId"`
	OriginConversationID string   `json:"originConversationId"`
	Revision             uint64   `json:"revision"`
	State                string   `json:"state"`
	Goal                 string   `json:"goal"`
	OpenQuestionIDs      []string `json:"openQuestionIds"`
}

type ProductActivity struct {
	ProductActivitySummary
	Requirements []string           `json:"requirements"`
	Materials    []ProductMaterial  `json:"materials"`
	Questions    []ProductQuestion  `json:"questions"`
	Executions   []ProductExecution `json:"executions"`
}

type ProductText struct {
	Text      string            `json:"text"`
	Materials []ProductMaterial `json:"materials"`
}

type ProductQuestion struct {
	QuestionID  string       `json:"questionId"`
	ExecutionID string       `json:"executionId"`
	Text        string       `json:"text"`
	Revision    uint64       `json:"revision"`
	State       string       `json:"state"`
	Answer      *ProductText `json:"answer,omitempty"`
	Delivery    string       `json:"delivery,omitempty"`
}

type ProductResultFile struct {
	Path   string `json:"path"`
	CallID string `json:"callId"`
}

type ProductActivityResult struct {
	ActivityID  string              `json:"activityId"`
	ExecutionID string              `json:"executionId"`
	Text        string              `json:"text"`
	Delivery    string              `json:"delivery"`
	Files       []ProductResultFile `json:"files"`
}

type ProductConnectionSettings struct {
	WireAPI          WireAPI `json:"wireApi"`
	BaseURL          string  `json:"baseUrl"`
	Model            string  `json:"model"`
	ContextTokens    int     `json:"contextTokens"`
	OutputTokens     int     `json:"outputTokens"`
	WorkingDirectory string  `json:"workingDirectory"`
	MaxActiveTasks   int     `json:"maxActiveTasks"`
}

type ProductConnectionInput struct {
	ProductConnectionSettings
	APIKey string `json:"apiKey"`
}

type ProductConnectionView struct {
	ProductConnectionSettings
	CredentialPresent bool `json:"credentialPresent"`
}

type ProductConnectionRevisions struct {
	SavedRevision     *uint64 `json:"savedRevision"`
	EffectiveRevision *uint64 `json:"effectiveRevision"`
}

type ProductConnectionStatus struct {
	ProductConnectionRevisions
	Saved     *ProductConnectionView `json:"saved"`
	Effective *ProductConnectionView `json:"effective"`
}

type ProductBotEnsureBuiltinParams struct {
	Kind        string `json:"kind"`
	OperationID string `json:"operationId"`
}

type ProductBotCreateParams struct {
	OperationID string            `json:"operationId"`
	Profile     ProductBotProfile `json:"profile"`
}

type ProductBotRevisionParams struct {
	OperationID      string `json:"operationId"`
	BotID            string `json:"botId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
}

type ProductBotUpdateParams struct {
	ProductBotRevisionParams
	Profile              ProductBotProfile `json:"profile"`
	StartNewConversation bool              `json:"startNewConversation"`
}

type ProductMemoryOperation struct {
	BotID       string `json:"botId"`
	OperationID string `json:"operationId"`
}

type ProductMemoryAddRuleParams struct {
	ProductMemoryOperation
	Text string `json:"text"`
}

type ProductMemoryUpdateRuleParams struct {
	ProductMemoryAddRuleParams
	RuleID string `json:"ruleId"`
}

type ProductMemoryDeleteRuleParams struct {
	ProductMemoryOperation
	RuleID string `json:"ruleId"`
}

type ProductMemoryReviewLearningParams struct {
	ProductMemoryOperation
	LearningID string  `json:"learningId"`
	Decision   string  `json:"decision"`
	Text       *string `json:"text,omitempty"`
}

type ProductMemoryDeleteLogParams struct {
	ProductMemoryOperation
	LogID string `json:"logId"`
}

type ProductMemorySearchParams struct {
	BotID    string `json:"botId"`
	Query    string `json:"query"`
	DateFrom string `json:"dateFrom,omitempty"`
	DateTo   string `json:"dateTo,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type ProductConversationListParams struct {
	ProductPageParams
	BotID string `json:"botId"`
}

type ProductConversationParams struct {
	ConversationID string `json:"conversationId"`
}

type ProductHistoryParams struct {
	ProductPageParams
	ProductConversationParams
}

type ProductSaveDraftParams struct {
	ProductConversationParams
	ProductText
	ExpectedRevision uint64 `json:"expectedRevision"`
	OperationID      string `json:"operationId"`
}

type ProductSendParams struct {
	ProductConversationParams
	DraftRevision uint64 `json:"draftRevision"`
	OperationID   string `json:"operationId"`
}

type ProductConversationStopParams struct {
	ProductConversationParams
	ExecutionID string `json:"executionId"`
	OperationID string `json:"operationId"`
}

type ProductStopResult struct {
	ExecutionID     string `json:"executionId"`
	CancelRequested bool   `json:"cancelRequested"`
}

type ProductActivityListParams struct {
	ProductPageParams
	BotID  string   `json:"botId"`
	States []string `json:"states,omitempty"`
}

type ProductActivityParams struct {
	ActivityID string `json:"activityId"`
}

type ProductActivityExecutionParams struct {
	ProductActivityParams
	ExecutionID string `json:"executionId"`
}

type ProductActivityStopParams struct {
	ProductActivityExecutionParams
	OperationID string `json:"operationId"`
}

type ProductActivityContinueParams struct {
	ProductActivityParams
	ProductText
	OperationID      string `json:"operationId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
}

type ProductAnswerParams struct {
	ProductActivityExecutionParams
	ProductText
	QuestionID       string `json:"questionId"`
	QuestionRevision uint64 `json:"questionRevision"`
	OperationID      string `json:"operationId"`
}

type ProductAnswerResult struct {
	ProductActivityExecutionParams
	QuestionID  string `json:"questionId"`
	Revision    uint64 `json:"revision"`
	AnswerSaved bool   `json:"answerSaved"`
	Delivery    string `json:"delivery"`
}

type ProductConnectionSaveParams struct {
	Configuration ProductConnectionInput `json:"configuration"`
	OperationID   string                 `json:"operationId"`
}

type ProductConnectionApplyParams struct {
	SavedRevision uint64 `json:"savedRevision"`
	OperationID   string `json:"operationId"`
}

func StartProduct(options ProductOptions) (*ProductClient, error) {
	client, err := start(Options{
		NodePath: options.NodePath, WorkerPath: options.WorkerPath, StateDir: options.StateDir,
		Handlers: Handlers{OnExit: options.Handlers.OnExit, OnProtocolError: options.Handlers.OnProtocolErr},
	}, &options)
	if err != nil {
		return nil, err
	}
	return &ProductClient{client}, nil
}

func (p *ProductClient) Ready(ctx context.Context) (Ready, error)         { return p.client.Ready(ctx) }
func (p *ProductClient) Done() <-chan struct{}                            { return p.client.Done() }
func (p *ProductClient) CallbacksDone() <-chan struct{}                   { return p.client.CallbacksDone() }
func (p *ProductClient) Wait(ctx context.Context) (ProcessOutcome, error) { return p.client.Wait(ctx) }
func (p *ProductClient) Kill(ctx context.Context) (ProcessOutcome, error) { return p.client.Kill(ctx) }
func (p *ProductClient) Shutdown(ctx context.Context) (ProcessOutcome, error) {
	return p.client.Shutdown(ctx)
}

func productCall[T any](ctx context.Context, client *ProductClient, method string, params any) (T, error) {
	var result T
	err := client.client.call(ctx, method, params, &result)
	return result, err
}

func (p *ProductClient) Open(ctx context.Context, versions []int) (ProductOpenResult, error) {
	return productCall[ProductOpenResult](ctx, p, "app.open", struct {
		Versions []int `json:"supportedVersions"`
	}{versions})
}

func (p *ProductClient) ListBots(ctx context.Context, params ProductPageParams) (ProductPage[ProductBot], error) {
	return productCall[ProductPage[ProductBot]](ctx, p, "bot.list", params)
}

func (p *ProductClient) EnsureBuiltinBot(ctx context.Context, params ProductBotEnsureBuiltinParams) (ProductBot, error) {
	return productCall[ProductBot](ctx, p, "bot.ensureBuiltin", params)
}

func (p *ProductClient) CreateBot(ctx context.Context, params ProductBotCreateParams) (ProductBot, error) {
	return productCall[ProductBot](ctx, p, "bot.create", params)
}

func (p *ProductClient) UpdateBot(ctx context.Context, params ProductBotUpdateParams) (ProductBot, error) {
	return productCall[ProductBot](ctx, p, "bot.update", params)
}

func (p *ProductClient) ArchiveBot(ctx context.Context, params ProductBotRevisionParams) (ProductBot, error) {
	return productCall[ProductBot](ctx, p, "bot.archive", params)
}

func (p *ProductClient) RestoreBot(ctx context.Context, params ProductBotRevisionParams) (ProductBot, error) {
	return productCall[ProductBot](ctx, p, "bot.restore", params)
}

func (p *ProductClient) GetBotMemory(ctx context.Context, botID string) (ProductBotMemory, error) {
	return productCall[ProductBotMemory](ctx, p, "memory.get", struct {
		BotID string `json:"botId"`
	}{botID})
}

func (p *ProductClient) AddMemoryRule(ctx context.Context, params ProductMemoryAddRuleParams) (ProductMemoryChangedEvent, error) {
	return productCall[ProductMemoryChangedEvent](ctx, p, "memory.addRule", params)
}

func (p *ProductClient) UpdateMemoryRule(ctx context.Context, params ProductMemoryUpdateRuleParams) (ProductMemoryChangedEvent, error) {
	return productCall[ProductMemoryChangedEvent](ctx, p, "memory.updateRule", params)
}

func (p *ProductClient) DeleteMemoryRule(ctx context.Context, params ProductMemoryDeleteRuleParams) (ProductMemoryChangedEvent, error) {
	return productCall[ProductMemoryChangedEvent](ctx, p, "memory.deleteRule", params)
}

func (p *ProductClient) ReviewLearning(ctx context.Context, params ProductMemoryReviewLearningParams) (ProductMemoryChangedEvent, error) {
	return productCall[ProductMemoryChangedEvent](ctx, p, "memory.reviewLearning", params)
}

func (p *ProductClient) DeleteMemoryLog(ctx context.Context, params ProductMemoryDeleteLogParams) (ProductMemoryChangedEvent, error) {
	return productCall[ProductMemoryChangedEvent](ctx, p, "memory.deleteLog", params)
}

func (p *ProductClient) SearchMemoryLogs(ctx context.Context, params ProductMemorySearchParams) ([]ProductWorkLog, error) {
	result, err := productCall[struct {
		Items []ProductWorkLog `json:"items"`
	}](ctx, p, "memory.searchLogs", params)
	return result.Items, err
}

func (p *ProductClient) ListConversations(ctx context.Context, params ProductConversationListParams) (ProductPage[ProductConversationSummary], error) {
	return productCall[ProductPage[ProductConversationSummary]](ctx, p, "conversation.list", params)
}

func (p *ProductClient) GetConversation(ctx context.Context, conversationID string) (ProductConversation, error) {
	return productCall[ProductConversation](ctx, p, "conversation.get", ProductConversationParams{conversationID})
}

func (p *ProductClient) History(ctx context.Context, params ProductHistoryParams) (ProductPage[ProductMessage], error) {
	return productCall[ProductPage[ProductMessage]](ctx, p, "conversation.history", params)
}

func (p *ProductClient) SaveDraft(ctx context.Context, params ProductSaveDraftParams) (ProductDraft, error) {
	if params.Materials == nil {
		params.Materials = []ProductMaterial{}
	}
	return productCall[ProductDraft](ctx, p, "conversation.saveDraft", params)
}

func (p *ProductClient) Send(ctx context.Context, params ProductSendParams) (ProductReceipt, error) {
	return productCall[ProductReceipt](ctx, p, "conversation.send", params)
}

func (p *ProductClient) StopConversation(ctx context.Context, params ProductConversationStopParams) (ProductStopResult, error) {
	return productCall[ProductStopResult](ctx, p, "conversation.stop", params)
}

func (p *ProductClient) ListActivities(ctx context.Context, params ProductActivityListParams) (ProductPage[ProductActivitySummary], error) {
	return productCall[ProductPage[ProductActivitySummary]](ctx, p, "activity.list", params)
}

func (p *ProductClient) GetActivity(ctx context.Context, activityID string) (ProductActivity, error) {
	return productCall[ProductActivity](ctx, p, "activity.get", ProductActivityParams{activityID})
}

func (p *ProductClient) ActivityResult(ctx context.Context, params ProductActivityExecutionParams) (ProductActivityResult, error) {
	return productCall[ProductActivityResult](ctx, p, "activity.result", params)
}

func (p *ProductClient) ContinueActivity(ctx context.Context, params ProductActivityContinueParams) (ProductReceipt, error) {
	if params.Materials == nil {
		params.Materials = []ProductMaterial{}
	}
	return productCall[ProductReceipt](ctx, p, "activity.continue", params)
}

func (p *ProductClient) StopActivity(ctx context.Context, params ProductActivityStopParams) (ProductStopResult, error) {
	return productCall[ProductStopResult](ctx, p, "activity.stop", params)
}

func (p *ProductClient) AnswerQuestion(ctx context.Context, params ProductAnswerParams) (ProductAnswerResult, error) {
	if params.Materials == nil {
		params.Materials = []ProductMaterial{}
	}
	return productCall[ProductAnswerResult](ctx, p, "question.answer", params)
}

func (p *ProductClient) Connection(ctx context.Context) (ProductConnectionStatus, error) {
	return productCall[ProductConnectionStatus](ctx, p, "connection.get", struct{}{})
}

func (p *ProductClient) TestConnection(ctx context.Context, configuration ProductConnectionInput) error {
	result, err := productCall[struct {
		Verified bool `json:"verified"`
	}](ctx, p, "connection.test", struct {
		Configuration ProductConnectionInput `json:"configuration"`
	}{configuration})
	if err == nil && !result.Verified {
		err = p.client.invalidResult("connection.test")
	}
	return err
}

func (p *ProductClient) SaveConnection(ctx context.Context, params ProductConnectionSaveParams) (ProductConnectionRevisions, error) {
	return productCall[ProductConnectionRevisions](ctx, p, "connection.save", params)
}

func (p *ProductClient) ApplyConnection(ctx context.Context, params ProductConnectionApplyParams) (ProductConnectionRevisions, error) {
	return productCall[ProductConnectionRevisions](ctx, p, "connection.apply", params)
}

func (p *ProductClient) Operation(ctx context.Context, operationID string) (ProductReceipt, error) {
	return productCall[ProductReceipt](ctx, p, "operation.get", struct {
		ID string `json:"operationId"`
	}{operationID})
}

func (p *ProductClient) BackupProfile(ctx context.Context, destination string) (string, error) {
	result, err := productCall[struct {
		Path string `json:"path"`
	}](ctx, p, "profile.backup", struct {
		Destination string `json:"destination"`
	}{destination})
	if err == nil && result.Path == "" {
		err = p.client.invalidResult("profile.backup")
	}
	return result.Path, err
}

func (c *Client) requestType() string {
	if c.product != nil {
		return "request"
	}
	return ""
}

func (c *Client) acceptProductStream(raw json.RawMessage) error {
	var open ProductOpenResult
	if c.product == nil || json.Unmarshal(raw, &open) != nil || open.Version != ProductVersion || open.StreamID == "" {
		return errors.New("invalid product open result")
	}
	c.streamID, c.streamSeq, c.streamGap = open.StreamID, open.EventSeq, false
	return nil
}

func (c *Client) handleProductEvent(line []byte) error {
	var event ProductEvent
	if json.Unmarshal(line, &event) != nil || event.StreamID == "" || event.Seq == 0 || len(event.Data) == 0 || event.Name == "" {
		return errors.New("invalid product event envelope")
	}
	c.mu.Lock()
	if event.StreamID != c.streamID || c.streamGap || event.Seq <= c.streamSeq {
		c.mu.Unlock()
		return nil
	}
	if event.Seq != c.streamSeq+1 {
		c.streamGap = true
		c.mu.Unlock()
		if c.product.OnStreamGap != nil {
			c.enqueue(c.product.OnStreamGap)
		}
		return nil
	}
	c.streamSeq = event.Seq
	c.mu.Unlock()
	if c.product.OnEvent != nil {
		c.enqueue(func() {
			c.mu.Lock()
			current := c.streamID == event.StreamID
			c.mu.Unlock()
			if current {
				c.product.OnEvent(event)
			}
		})
	}
	return nil
}
