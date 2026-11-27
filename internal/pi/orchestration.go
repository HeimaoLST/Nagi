package pi

import (
	"context"
	"encoding/json"
)

type ProductConversationTimelineAnchorKind string

const (
	ProductAnchorPreSend        ProductConversationTimelineAnchorKind = "pre_send"
	ProductAnchorPostSettlement ProductConversationTimelineAnchorKind = "post_settlement"
	ProductAnchorStandalone     ProductConversationTimelineAnchorKind = "standalone"
)

type ProductConversationTimelineSourceRef struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

type ProductConversationNotice struct {
	NoticeID          string                                 `json:"noticeId"`
	BotID             string                                 `json:"botId"`
	MandateID         string                                 `json:"mandateId"`
	RunID             string                                 `json:"runId"`
	FactDigest        string                                 `json:"factDigest"`
	AnchorKind        ProductConversationTimelineAnchorKind  `json:"anchorKind"`
	AnchorExecutionID string                                 `json:"anchorExecutionId,omitempty"`
	AnchorOutcome     string                                 `json:"anchorOutcome,omitempty"`
	SourceRefs        []ProductConversationTimelineSourceRef `json:"sourceRefs"`
	Urgency           string                                 `json:"urgency"`
	Dormant           bool                                   `json:"dormant"`
	CreatedAt         string                                 `json:"createdAt"`
}

type ProductConversationTimelineItem struct {
	Kind      string                     `json:"kind"`
	MessageID string                     `json:"messageId"`
	Timestamp string                     `json:"timestamp"`
	AnchorSeq uint64                     `json:"anchorSeq"`
	Role      string                     `json:"role,omitempty"`
	Text      string                     `json:"text"`
	Materials []ProductMaterial          `json:"materials,omitempty"`
	Quote     *ProductQuote              `json:"quote,omitempty"`
	Notice    *ProductConversationNotice `json:"notice,omitempty"`
}

type ProductConversationTimelineParams struct {
	ConversationID string `json:"conversationId"`
	Cursor         string `json:"cursor,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}

type ProductConversationTimelineResult struct {
	ConversationID      string                            `json:"conversationId"`
	TimelineRevision    uint64                            `json:"timelineRevision"`
	TranscriptAvailable bool                              `json:"transcriptAvailable"`
	TranscriptError     string                            `json:"transcriptError,omitempty"`
	Items               []ProductConversationTimelineItem `json:"items"`
	NextCursor          *string                           `json:"nextCursor"`
}

type ProductConversationTimelineChanged struct {
	ConversationID   string `json:"conversationId"`
	TimelineRevision uint64 `json:"timelineRevision"`
	NoticeID         string `json:"noticeId,omitempty"`
	At               string `json:"at"`
}

type ProductConversationSendResult struct {
	Queued        bool            `json:"queued,omitempty"`
	QueuePosition int             `json:"queuePosition,omitempty"`
	Reason        string          `json:"reason,omitempty"`
	DraftRevision uint64          `json:"draftRevision,omitempty"`
	Delivery      string          `json:"delivery,omitempty"`
	Route         json.RawMessage `json:"route,omitempty"`
}

type ProductMandateState string

const (
	ProductMandateActive           ProductMandateState = "active"
	ProductMandateWaitingDecision  ProductMandateState = "waiting_decision"
	ProductMandatePaused           ProductMandateState = "paused"
	ProductMandateRestored         ProductMandateState = "restored"
	ProductMandateRestoredReadOnly ProductMandateState = "restored_readonly"
	ProductMandateCompleted        ProductMandateState = "completed"
	ProductMandateStopped          ProductMandateState = "stopped"
	ProductMandateFailed           ProductMandateState = "failed"
	ProductMandateUnknown          ProductMandateState = "unknown"
)

type ProductMandateSourceKind string

const (
	ProductMandateSourceWatch    ProductMandateSourceKind = "watch"
	ProductMandateSourceTimer    ProductMandateSourceKind = "timer"
	ProductMandateSourceSchedule ProductMandateSourceKind = "schedule"
	ProductMandateSourceSession  ProductMandateSourceKind = "session"
	ProductMandateSourceUser     ProductMandateSourceKind = "user"
)

type ProductOrchestrationDisposition string

const (
	ProductDispositionSilent        ProductOrchestrationDisposition = "silent"
	ProductDispositionNotified      ProductOrchestrationDisposition = "notified"
	ProductDispositionNeedsDecision ProductOrchestrationDisposition = "needs_decision"
	ProductDispositionStopped       ProductOrchestrationDisposition = "stopped"
	ProductDispositionFailed        ProductOrchestrationDisposition = "failed"
	ProductDispositionUnknown       ProductOrchestrationDisposition = "unknown"
)

type ProductOrchestrationDecisionOutcome string

const (
	ProductDecisionApprove ProductOrchestrationDecisionOutcome = "approve"
	ProductDecisionReject  ProductOrchestrationDecisionOutcome = "reject"
)

type ProductOrchestrationMetrics struct {
	TotalWakes            uint64  `json:"totalWakes"`
	Calls                 uint64  `json:"calls"`
	InputTokens           uint64  `json:"inputTokens"`
	OutputTokens          uint64  `json:"outputTokens"`
	TotalTokens           uint64  `json:"totalTokens"`
	CostUSD               float64 `json:"costUsd"`
	LastQueueDelayMS      uint64  `json:"lastQueueDelayMs"`
	LastCoalescedCount    uint64  `json:"lastCoalescedCount"`
	BackoffDelayMS        uint64  `json:"backoffDelayMs"`
	BackoffReason         string  `json:"backoffReason,omitempty"`
	ConsecutiveNoProgress uint64  `json:"consecutiveNoProgress"`
	NextEligibleAt        string  `json:"nextEligibleAt,omitempty"`
}

type ProductOrchestrationDecisionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

type ProductOrchestrationDecision struct {
	DecisionID           string                               `json:"decisionId"`
	MandateID            string                               `json:"mandateId"`
	OwnerBotID           string                               `json:"ownerBotId"`
	RunID                string                               `json:"runId"`
	Revision             uint64                               `json:"revision"`
	State                string                               `json:"state"`
	Kind                 string                               `json:"kind"`
	Purpose              string                               `json:"purpose"`
	Summary              string                               `json:"summary"`
	TargetConversationID string                               `json:"targetConversationId"`
	Options              []ProductOrchestrationDecisionOption `json:"options"`
	Outcome              string                               `json:"outcome,omitempty"`
	SelectedOptionID     string                               `json:"selectedOptionId,omitempty"`
	ResponseText         string                               `json:"responseText,omitempty"`
	Dormant              bool                                 `json:"dormant"`
	StatusReason         string                               `json:"statusReason,omitempty"`
	CreatedAt            string                               `json:"createdAt"`
	UpdatedAt            string                               `json:"updatedAt"`
	ResolvedAt           string                               `json:"resolvedAt,omitempty"`
}

type ProductOrchestrationRun struct {
	RunID          string                          `json:"runId"`
	MandateID      string                          `json:"mandateId"`
	Generation     uint64                          `json:"generation"`
	BatchID        string                          `json:"batchId,omitempty"`
	State          string                          `json:"state"`
	RouteRole      string                          `json:"routeRole,omitempty"`
	Disposition    ProductOrchestrationDisposition `json:"disposition,omitempty"`
	Summary        string                          `json:"summary,omitempty"`
	NoticeID       string                          `json:"noticeId,omitempty"`
	DecisionID     string                          `json:"decisionId,omitempty"`
	Error          string                          `json:"error,omitempty"`
	QueueDelayMS   uint64                          `json:"queueDelayMs"`
	CoalescedCount uint64                          `json:"coalescedCount"`
	Calls          uint64                          `json:"calls"`
	InputTokens    uint64                          `json:"inputTokens"`
	OutputTokens   uint64                          `json:"outputTokens"`
	TotalTokens    uint64                          `json:"totalTokens"`
	CostUSD        float64                         `json:"costUsd"`
	StartedAt      string                          `json:"startedAt"`
	SettledAt      string                          `json:"settledAt,omitempty"`
}

type ProductOrchestrationEffectReceipt struct {
	ReceiptID string `json:"receiptId"`
	MandateID string `json:"mandateId"`
	RunID     string `json:"runId"`
	CallID    string `json:"callId"`
	Tool      string `json:"tool"`
	Target    string `json:"target,omitempty"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt string `json:"createdAt"`
}

type ProductOrchestrationSummary struct {
	MandateID             string                          `json:"mandateId"`
	OwnerBotID            string                          `json:"ownerBotId"`
	TargetConversationID  string                          `json:"targetConversationId"`
	SourceKind            ProductMandateSourceKind        `json:"sourceKind"`
	SourceID              string                          `json:"sourceId,omitempty"`
	Purpose               string                          `json:"purpose"`
	State                 ProductMandateState             `json:"state"`
	Revision              uint64                          `json:"revision"`
	Generation            uint64                          `json:"generation"`
	CurrentRunID          string                          `json:"currentRunId,omitempty"`
	CurrentExecutionState string                          `json:"currentExecutionState,omitempty"`
	PendingDecision       *ProductOrchestrationDecision   `json:"pendingDecision,omitempty"`
	Metrics               ProductOrchestrationMetrics     `json:"metrics"`
	LastDisposition       ProductOrchestrationDisposition `json:"lastDisposition,omitempty"`
	LastNoticeID          string                          `json:"lastNoticeId,omitempty"`
	LastReceiptState      string                          `json:"lastReceiptState,omitempty"`
	LastError             string                          `json:"lastError,omitempty"`
	StatusReason          string                          `json:"statusReason,omitempty"`
	Dormant               bool                            `json:"dormant"`
	CreatedAt             string                          `json:"createdAt"`
	UpdatedAt             string                          `json:"updatedAt"`
	SettledAt             string                          `json:"settledAt,omitempty"`
}

type ProductOrchestrationDetail struct {
	ProductOrchestrationSummary
	RecentRuns     []ProductOrchestrationRun           `json:"recentRuns"`
	Decisions      []ProductOrchestrationDecision      `json:"decisions"`
	RecentReceipts []ProductOrchestrationEffectReceipt `json:"recentReceipts"`
}

type ProductOrchestrationListParams struct {
	BotID  string                `json:"botId"`
	States []ProductMandateState `json:"states,omitempty"`
	Cursor string                `json:"cursor,omitempty"`
	Limit  int                   `json:"limit,omitempty"`
}

type ProductOrchestrationDecideParams struct {
	BotID            string                              `json:"botId"`
	MandateID        string                              `json:"mandateId"`
	DecisionID       string                              `json:"decisionId"`
	ExpectedRevision uint64                              `json:"expectedRevision"`
	Outcome          ProductOrchestrationDecisionOutcome `json:"outcome"`
	SelectedOptionID string                              `json:"selectedOptionId,omitempty"`
	Text             string                              `json:"text,omitempty"`
	OperationID      string                              `json:"operationId"`
}

type ProductOrchestrationStopParams struct {
	BotID            string  `json:"botId"`
	MandateID        string  `json:"mandateId"`
	ExpectedRevision *uint64 `json:"expectedRevision,omitempty"`
	Generation       *uint64 `json:"generation,omitempty"`
	RunID            string  `json:"runId,omitempty"`
	OperationID      string  `json:"operationId"`
}

type ProductOrchestrationDecisionResult struct {
	Mandate  ProductOrchestrationSummary  `json:"mandate"`
	Decision ProductOrchestrationDecision `json:"decision"`
}

type ProductOrchestrationStopResult struct {
	Mandate         ProductOrchestrationSummary `json:"mandate"`
	CancelRequested bool                        `json:"cancelRequested"`
	StoppedRunID    string                      `json:"stoppedRunId,omitempty"`
}

type ProductOrchestrationChange struct {
	BotID           string              `json:"botId"`
	Change          string              `json:"change"`
	MandateID       string              `json:"mandateId,omitempty"`
	MandateRevision uint64              `json:"mandateRevision,omitempty"`
	State           ProductMandateState `json:"state,omitempty"`
	RunID           string              `json:"runId,omitempty"`
	DecisionID      string              `json:"decisionId,omitempty"`
	ConversationID  string              `json:"conversationId,omitempty"`
	ExecutionID     string              `json:"executionId,omitempty"`
	OperationID     string              `json:"operationId,omitempty"`
	At              string              `json:"at"`
}

func (p *ProductClient) ConversationTimeline(ctx context.Context, params ProductConversationTimelineParams) (ProductConversationTimelineResult, error) {
	return productCall[ProductConversationTimelineResult](ctx, p, "conversation.timeline", params)
}

func (p *ProductClient) ListOrchestrations(ctx context.Context, params ProductOrchestrationListParams) (ProductPage[ProductOrchestrationSummary], error) {
	return productCall[ProductPage[ProductOrchestrationSummary]](ctx, p, "orchestration.list", params)
}

func (p *ProductClient) GetOrchestration(ctx context.Context, botID, mandateID string) (ProductOrchestrationDetail, error) {
	return productCall[ProductOrchestrationDetail](ctx, p, "orchestration.get", struct {
		BotID     string `json:"botId"`
		MandateID string `json:"mandateId"`
	}{BotID: botID, MandateID: mandateID})
}

func (p *ProductClient) DecideOrchestration(ctx context.Context, params ProductOrchestrationDecideParams) (ProductReceipt, error) {
	return productCall[ProductReceipt](ctx, p, "orchestration.decide", params)
}

func (p *ProductClient) StopOrchestration(ctx context.Context, params ProductOrchestrationStopParams) (ProductReceipt, error) {
	return productCall[ProductReceipt](ctx, p, "orchestration.stop", params)
}

func DecodeConversationSendResult(receipt ProductReceipt) (ProductConversationSendResult, error) {
	if len(receipt.Result) == 0 {
		return ProductConversationSendResult{}, nil
	}
	var result ProductConversationSendResult
	err := json.Unmarshal(receipt.Result, &result)
	return result, err
}

func DecodeOrchestrationDecisionResult(receipt ProductReceipt) (ProductOrchestrationDecisionResult, error) {
	var result ProductOrchestrationDecisionResult
	err := json.Unmarshal(receipt.Result, &result)
	return result, err
}

func DecodeOrchestrationStopResult(receipt ProductReceipt) (ProductOrchestrationStopResult, error) {
	var result ProductOrchestrationStopResult
	err := json.Unmarshal(receipt.Result, &result)
	return result, err
}
