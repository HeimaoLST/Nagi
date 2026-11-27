package pi

import (
	"context"
	"encoding/json"
)

type ProductSessionState string

const (
	ProductSessionStarting     ProductSessionState = "starting"
	ProductSessionRunning      ProductSessionState = "running"
	ProductSessionExited       ProductSessionState = "exited"
	ProductSessionClosed       ProductSessionState = "closed"
	ProductSessionDisconnected ProductSessionState = "disconnected"
	ProductSessionFailed       ProductSessionState = "failed"
)

type ProductSessionMode string

const (
	ProductSessionPTY  ProductSessionMode = "pty"
	ProductSessionPipe ProductSessionMode = "pipe"
)

type ProductSessionTransportStatus string

const (
	ProductTransportLive             ProductSessionTransportStatus = "live"
	ProductTransportEOF              ProductSessionTransportStatus = "eof"
	ProductTransportSupervisorLost   ProductSessionTransportStatus = "supervisor_lost"
	ProductTransportWorkerRestarted  ProductSessionTransportStatus = "worker_restarted"
	ProductTransportClosedByUser     ProductSessionTransportStatus = "closed_by_user"
	ProductTransportClosedByBot      ProductSessionTransportStatus = "closed_by_bot"
	ProductTransportClosedOnShutdown ProductSessionTransportStatus = "closed_on_shutdown"
	ProductTransportExecFailed       ProductSessionTransportStatus = "exec_failed"
)

type ProductSessionAuthPause struct {
	Paused  bool   `json:"paused"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type ProductSessionWatchSummary struct {
	WatchID        string   `json:"watchId"`
	SessionID      string   `json:"sessionId"`
	Generation     uint64   `json:"generation"`
	Revision       uint64   `json:"revision"`
	State          string   `json:"state"`
	Conditions     []string `json:"conditions"`
	StartOffset    uint64   `json:"startOffset"`
	MatchLiteral   string   `json:"matchLiteral,omitempty"`
	IdleMS         *uint64  `json:"idleMs,omitempty"`
	TimeoutMS      *uint64  `json:"timeoutMs,omitempty"`
	FiredKind      string   `json:"firedKind,omitempty"`
	OccurrenceID   string   `json:"occurrenceId,omitempty"`
	FiredAt        string   `json:"firedAt,omitempty"`
	FiredOffset    *uint64  `json:"firedOffset,omitempty"`
	MatchedExcerpt string   `json:"matchedExcerpt,omitempty"`
}

type ProductSessionSummary struct {
	SessionID            string                        `json:"sessionId"`
	OwnerBotID           string                        `json:"ownerBotId"`
	OriginConversationID string                        `json:"originConversationId,omitempty"`
	OriginActivityID     string                        `json:"originActivityId,omitempty"`
	CreatingOperationID  string                        `json:"creatingOperationId"`
	Generation           uint64                        `json:"generation"`
	Revision             uint64                        `json:"revision"`
	Label                string                        `json:"label,omitempty"`
	CommandSummary       string                        `json:"commandSummary"`
	Cwd                  string                        `json:"cwd"`
	Mode                 ProductSessionMode            `json:"mode"`
	Cols                 *int                          `json:"cols,omitempty"`
	Rows                 *int                          `json:"rows,omitempty"`
	State                ProductSessionState           `json:"state"`
	ExitCode             *int                          `json:"exitCode,omitempty"`
	ExitSignal           string                        `json:"exitSignal,omitempty"`
	TransportStatus      ProductSessionTransportStatus `json:"transportStatus"`
	FirstAvailableOffset uint64                        `json:"firstAvailableOffset"`
	NextOffset           uint64                        `json:"nextOffset"`
	EOF                  bool                          `json:"eof"`
	EvictedBytes         uint64                        `json:"evictedBytes"`
	AuthPause            *ProductSessionAuthPause      `json:"authPause,omitempty"`
	Watches              []ProductSessionWatchSummary  `json:"watches"`
	Error                string                        `json:"error,omitempty"`
	CreatedAt            string                        `json:"createdAt"`
	UpdatedAt            string                        `json:"updatedAt"`
	SettledAt            string                        `json:"settledAt,omitempty"`
}

type ProductSessionChunk struct {
	Stream     string `json:"stream"`
	Offset     uint64 `json:"offset"`
	NextOffset uint64 `json:"nextOffset"`
	Text       string `json:"text"`
}

type ProductSessionListParams struct {
	BotID  string                `json:"botId"`
	States []ProductSessionState `json:"states,omitempty"`
	Cursor string                `json:"cursor,omitempty"`
	Limit  int                   `json:"limit,omitempty"`
}

type ProductSessionReadParams struct {
	BotID      string  `json:"botId"`
	SessionID  string  `json:"sessionId"`
	Generation uint64  `json:"generation"`
	Offset     *uint64 `json:"offset,omitempty"`
	Limit      int     `json:"limit,omitempty"`
}

type ProductSessionReadResult struct {
	SessionID            string                   `json:"sessionId"`
	Generation           uint64                   `json:"generation"`
	Revision             uint64                   `json:"revision"`
	Mode                 ProductSessionMode       `json:"mode"`
	State                ProductSessionState      `json:"state"`
	FirstAvailableOffset uint64                   `json:"firstAvailableOffset"`
	FromOffset           uint64                   `json:"fromOffset"`
	NextOffset           uint64                   `json:"nextOffset"`
	HighWaterOffset      uint64                   `json:"highWaterOffset"`
	EOF                  bool                     `json:"eof"`
	Gap                  bool                     `json:"gap"`
	GapBytes             uint64                   `json:"gapBytes"`
	AuthPause            *ProductSessionAuthPause `json:"authPause,omitempty"`
	Chunks               []ProductSessionChunk    `json:"chunks"`
}

type ProductSessionCloseParams struct {
	BotID            string  `json:"botId"`
	SessionID        string  `json:"sessionId"`
	Generation       uint64  `json:"generation"`
	ExpectedRevision *uint64 `json:"expectedRevision,omitempty"`
	OperationID      string  `json:"operationId"`
}

type ProductSessionCloseResult struct {
	SessionID        string                        `json:"sessionId"`
	Generation       uint64                        `json:"generation"`
	Revision         uint64                        `json:"revision"`
	State            ProductSessionState           `json:"state"`
	CloseDisposition string                        `json:"closeDisposition"`
	ChildReaped      bool                          `json:"childReaped"`
	ExitCode         *int                          `json:"exitCode,omitempty"`
	ExitSignal       string                        `json:"exitSignal,omitempty"`
	TransportStatus  ProductSessionTransportStatus `json:"transportStatus"`
}

type ProductSessionOutputUpdated struct {
	SessionID            string `json:"sessionId"`
	OwnerBotID           string `json:"ownerBotId"`
	Generation           uint64 `json:"generation"`
	FirstAvailableOffset uint64 `json:"firstAvailableOffset"`
	NextOffset           uint64 `json:"nextOffset"`
	EvictedBytes         uint64 `json:"evictedBytes"`
	EOF                  bool   `json:"eof"`
	At                   string `json:"at"`
}

func (p *ProductClient) ListSessions(ctx context.Context, params ProductSessionListParams) (ProductPage[ProductSessionSummary], error) {
	return productCall[ProductPage[ProductSessionSummary]](ctx, p, "session.list", params)
}

func (p *ProductClient) GetSession(ctx context.Context, botID, sessionID string) (ProductSessionSummary, error) {
	return productCall[ProductSessionSummary](ctx, p, "session.get", struct {
		BotID     string `json:"botId"`
		SessionID string `json:"sessionId"`
	}{BotID: botID, SessionID: sessionID})
}

func (p *ProductClient) ReadSession(ctx context.Context, params ProductSessionReadParams) (ProductSessionReadResult, error) {
	return productCall[ProductSessionReadResult](ctx, p, "session.read", params)
}

func (p *ProductClient) CloseSession(ctx context.Context, params ProductSessionCloseParams) (ProductReceipt, error) {
	return productCall[ProductReceipt](ctx, p, "session.close", params)
}

func DecodeSessionCloseResult(receipt ProductReceipt) (ProductSessionCloseResult, error) {
	var result ProductSessionCloseResult
	err := json.Unmarshal(receipt.Result, &result)
	return result, err
}
