package pi

import (
	"context"
	"encoding/json"
)

type ProductWatchKind string

const (
	ProductWatchTimer    ProductWatchKind = "timer"
	ProductWatchSchedule ProductWatchKind = "schedule"
)

type ProductWatchState string

const (
	ProductWatchActive      ProductWatchState = "active"
	ProductWatchProposed    ProductWatchState = "proposed"
	ProductWatchPaused      ProductWatchState = "paused"
	ProductWatchRestored    ProductWatchState = "restored"
	ProductWatchInvalidated ProductWatchState = "invalidated"
	ProductWatchSettled     ProductWatchState = "settled"
	ProductWatchCancelled   ProductWatchState = "cancelled"
)

type ProductTimerMissedPolicy string

const (
	ProductMissedSkip              ProductTimerMissedPolicy = "skip"
	ProductMissedFireOnceCoalesced ProductTimerMissedPolicy = "fire_once_coalesced"
)

type ProductScheduleCadenceKind string

const (
	ProductScheduleInterval ProductScheduleCadenceKind = "interval"
	ProductScheduleCalendar ProductScheduleCadenceKind = "calendar"
)

type ProductWatchSource string

const (
	ProductWatchSourceUser  ProductWatchSource = "user"
	ProductWatchSourceModel ProductWatchSource = "model"
)

type ProductTimerSpec struct {
	FireAt       string                   `json:"fireAt"`
	MissedPolicy ProductTimerMissedPolicy `json:"missedPolicy"`
}

type ProductScheduleSpec struct {
	CadenceKind     ProductScheduleCadenceKind `json:"cadenceKind"`
	IntervalSeconds *int                       `json:"intervalSeconds,omitempty"`
	Frequency       string                     `json:"frequency,omitempty"`
	Minute          *int                       `json:"minute,omitempty"`
	Hour            *int                       `json:"hour,omitempty"`
	DaysOfWeek      []int                      `json:"daysOfWeek,omitempty"`
	TimeZone        string                     `json:"timeZone"`
	JitterSeconds   *int                       `json:"jitterSeconds,omitempty"`
}

type ProductWatchPrecheck struct {
	Kind             string   `json:"kind"`
	SkillID          string   `json:"skillId,omitempty"`
	SkillSource      string   `json:"skillSource,omitempty"`
	ScriptName       string   `json:"scriptName,omitempty"`
	Input            any      `json:"input,omitempty"`
	Executable       string   `json:"executable,omitempty"`
	AllowedArgs      []string `json:"allowedArgs,omitempty"`
	WorkspaceRead    bool     `json:"workspaceRead,omitempty"`
	TimeoutMS        *int     `json:"timeoutMs,omitempty"`
	EmitInitial      bool     `json:"emitInitial,omitempty"`
	ExecutableDigest string   `json:"executableDigest,omitempty"`
	ApprovalDigest   string   `json:"approvalDigest,omitempty"`
}

type ProductWatchOccurrence struct {
	OccurrenceID  string `json:"occurrenceId"`
	WatchID       string `json:"watchId"`
	OwnerBotID    string `json:"ownerBotId"`
	WatchRevision uint64 `json:"watchRevision"`
	ScheduledAt   string `json:"scheduledAt"`
	Outcome       string `json:"outcome"`
	State         string `json:"state"`
	ExitCode      *int   `json:"exitCode,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
	Digest        string `json:"digest,omitempty"`
	Excerpt       string `json:"excerpt,omitempty"`
	Error         string `json:"error,omitempty"`
	EventID       string `json:"eventId,omitempty"`
	CoalescedInto string `json:"coalescedInto,omitempty"`
	CreatedAt     string `json:"createdAt"`
	SettledAt     string `json:"settledAt,omitempty"`
}

type ProductWatchGrant struct {
	GrantID           string   `json:"grantId"`
	InstallationID    string   `json:"installationId"`
	OwnerBotID        string   `json:"ownerBotId"`
	Revision          uint64   `json:"revision"`
	State             string   `json:"state"`
	Purpose           string   `json:"purpose"`
	OperationClass    string   `json:"operationClass"`
	ArtifactKind      string   `json:"artifactKind"`
	Executable        string   `json:"executable,omitempty"`
	ExecutableDigest  string   `json:"executableDigest,omitempty"`
	AllowedArgs       []string `json:"allowedArgs,omitempty"`
	SkillID           string   `json:"skillId,omitempty"`
	SkillSource       string   `json:"skillSource,omitempty"`
	ScriptName        string   `json:"scriptName,omitempty"`
	ApprovalDigest    string   `json:"approvalDigest,omitempty"`
	PermissionsDigest string   `json:"permissionsDigest,omitempty"`
	CanonicalCWD      string   `json:"canonicalCwd"`
	WorkspaceRead     bool     `json:"workspaceRead"`
	NetworkOrigins    []string `json:"networkOrigins"`
	MinCadenceSeconds int      `json:"minCadenceSeconds"`
	MaxTimeoutMS      int      `json:"maxTimeoutMs"`
	RevokedReason     string   `json:"revokedReason,omitempty"`
	CreatedAt         string   `json:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"`
	RevokedAt         string   `json:"revokedAt,omitempty"`
}

type ProductWatchSummary struct {
	WatchID              string                `json:"watchId"`
	OwnerBotID           string                `json:"ownerBotId"`
	TargetConversationID string                `json:"targetConversationId"`
	OriginActivityID     string                `json:"originActivityId,omitempty"`
	Revision             uint64                `json:"revision"`
	Kind                 ProductWatchKind      `json:"kind"`
	Source               ProductWatchSource    `json:"source"`
	State                ProductWatchState     `json:"state"`
	Armed                bool                  `json:"armed"`
	Label                string                `json:"label"`
	Purpose              string                `json:"purpose"`
	Prompt               string                `json:"prompt,omitempty"`
	GrantID              string                `json:"grantId,omitempty"`
	StatusReason         string                `json:"statusReason,omitempty"`
	Timer                *ProductTimerSpec     `json:"timer,omitempty"`
	Schedule             *ProductScheduleSpec  `json:"schedule,omitempty"`
	Precheck             *ProductWatchPrecheck `json:"precheck,omitempty"`
	NextDueAt            string                `json:"nextDueAt,omitempty"`
	LastFiredAt          string                `json:"lastFiredAt,omitempty"`
	LastGoodDigest       string                `json:"lastGoodDigest,omitempty"`
	LastOutcome          string                `json:"lastOutcome,omitempty"`
	ConsecutiveFailures  int                   `json:"consecutiveFailures"`
	LastOccurrenceID     string                `json:"lastOccurrenceId,omitempty"`
	LastEventID          string                `json:"lastEventId,omitempty"`
	CreatedAt            string                `json:"createdAt"`
	UpdatedAt            string                `json:"updatedAt"`
	SettledAt            string                `json:"settledAt,omitempty"`
}

type ProductWatchDetail struct {
	ProductWatchSummary
	Grant             *ProductWatchGrant       `json:"grant,omitempty"`
	RecentOccurrences []ProductWatchOccurrence `json:"recentOccurrences"`
}

type ProductWatchListParams struct {
	BotID  string              `json:"botId"`
	Kind   ProductWatchKind    `json:"kind,omitempty"`
	States []ProductWatchState `json:"states,omitempty"`
	Cursor string              `json:"cursor,omitempty"`
	Limit  int                 `json:"limit,omitempty"`
}

type ProductWatchSaveParams struct {
	BotID                string                `json:"botId"`
	WatchID              string                `json:"watchId,omitempty"`
	ExpectedRevision     *uint64               `json:"expectedRevision,omitempty"`
	Kind                 ProductWatchKind      `json:"kind"`
	Label                string                `json:"label,omitempty"`
	Purpose              string                `json:"purpose"`
	TargetConversationID string                `json:"targetConversationId,omitempty"`
	Prompt               string                `json:"prompt,omitempty"`
	Timer                *ProductTimerSpec     `json:"timer,omitempty"`
	Schedule             *ProductScheduleSpec  `json:"schedule,omitempty"`
	Precheck             *ProductWatchPrecheck `json:"precheck,omitempty"`
	Activate             *bool                 `json:"activate,omitempty"`
	ApproveGrant         *bool                 `json:"approveGrant,omitempty"`
	OperationID          string                `json:"operationId"`
}

type ProductWatchActivateParams struct {
	BotID            string `json:"botId"`
	WatchID          string `json:"watchId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
	ApproveGrant     bool   `json:"approveGrant,omitempty"`
	OperationID      string `json:"operationId"`
}

type ProductWatchStateMutationParams struct {
	BotID            string `json:"botId"`
	WatchID          string `json:"watchId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
	OperationID      string `json:"operationId"`
}

type ProductWatchMutationResult struct {
	Watch       ProductWatchSummary `json:"watch"`
	Grant       *ProductWatchGrant  `json:"grant,omitempty"`
	Disposition string              `json:"disposition,omitempty"`
}

type ProductWatchGrantListParams struct {
	BotID          string `json:"botId"`
	IncludeRevoked bool   `json:"includeRevoked,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}

type ProductWatchRevokeGrantParams struct {
	BotID            string `json:"botId"`
	GrantID          string `json:"grantId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
	OperationID      string `json:"operationId"`
}

type ProductWatchRevokeGrantResult struct {
	Grant               ProductWatchGrant `json:"grant"`
	InvalidatedWatchIDs []string          `json:"invalidatedWatchIds"`
}

type ProductWatchChange struct {
	BotID         string            `json:"botId"`
	Change        string            `json:"change"`
	WatchID       string            `json:"watchId,omitempty"`
	WatchRevision uint64            `json:"watchRevision,omitempty"`
	State         ProductWatchState `json:"state,omitempty"`
	GrantID       string            `json:"grantId,omitempty"`
	GrantRevision uint64            `json:"grantRevision,omitempty"`
	OccurrenceID  string            `json:"occurrenceId,omitempty"`
	NextDueAt     string            `json:"nextDueAt,omitempty"`
	At            string            `json:"at"`
}

func (p *ProductClient) ListWatches(ctx context.Context, params ProductWatchListParams) (ProductPage[ProductWatchSummary], error) {
	return productCall[ProductPage[ProductWatchSummary]](ctx, p, "watch.list", params)
}

func (p *ProductClient) GetWatch(ctx context.Context, botID, watchID string) (ProductWatchDetail, error) {
	return productCall[ProductWatchDetail](ctx, p, "watch.get", struct {
		BotID   string `json:"botId"`
		WatchID string `json:"watchId"`
	}{BotID: botID, WatchID: watchID})
}

func (p *ProductClient) SaveWatch(ctx context.Context, params ProductWatchSaveParams) (ProductReceipt, error) {
	return productCall[ProductReceipt](ctx, p, "watch.save", params)
}

func (p *ProductClient) ActivateWatch(ctx context.Context, params ProductWatchActivateParams) (ProductReceipt, error) {
	return productCall[ProductReceipt](ctx, p, "watch.activate", params)
}

func (p *ProductClient) PauseWatch(ctx context.Context, params ProductWatchStateMutationParams) (ProductReceipt, error) {
	return productCall[ProductReceipt](ctx, p, "watch.pause", params)
}

func (p *ProductClient) CancelWatch(ctx context.Context, params ProductWatchStateMutationParams) (ProductReceipt, error) {
	return productCall[ProductReceipt](ctx, p, "watch.cancel", params)
}

func (p *ProductClient) ListWatchGrants(ctx context.Context, params ProductWatchGrantListParams) (ProductPage[ProductWatchGrant], error) {
	return productCall[ProductPage[ProductWatchGrant]](ctx, p, "watch.listGrants", params)
}

func (p *ProductClient) RevokeWatchGrant(ctx context.Context, params ProductWatchRevokeGrantParams) (ProductReceipt, error) {
	return productCall[ProductReceipt](ctx, p, "watch.revokeGrant", params)
}

func DecodeWatchMutationResult(receipt ProductReceipt) (ProductWatchMutationResult, error) {
	var result ProductWatchMutationResult
	err := json.Unmarshal(receipt.Result, &result)
	return result, err
}

func DecodeWatchRevokeGrantResult(receipt ProductReceipt) (ProductWatchRevokeGrantResult, error) {
	var result ProductWatchRevokeGrantResult
	err := json.Unmarshal(receipt.Result, &result)
	return result, err
}
