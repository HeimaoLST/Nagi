package pi

import "context"

type ProductRoutingPolicy string

const (
	ProductRoutingEconomy  ProductRoutingPolicy = "economy"
	ProductRoutingBalanced ProductRoutingPolicy = "balanced"
	ProductRoutingQuality  ProductRoutingPolicy = "quality"
)

type ProductModelRole string

type ProductThinkingLevel string

type ProductDeliveryBlocker string

const ProductCredentialUnavailable ProductDeliveryBlocker = "credential_unavailable"
const ProductRouteUnavailable ProductDeliveryBlocker = "route_unavailable"

type ProductProviderAccount struct {
	ProviderID     string                  `json:"providerId"`
	Name           string                  `json:"name"`
	WireAPI        WireAPI                 `json:"wireApi"`
	BaseURL        string                  `json:"baseUrl"`
	Models         []string                `json:"models,omitempty"`
	DefaultModel   string                  `json:"defaultModel"`
	ContextTokens  int                     `json:"contextTokens"`
	OutputTokens   int                     `json:"outputTokens"`
	Enabled        bool                    `json:"enabled"`
	EnabledModels  *[]string               `json:"enabledModels,omitempty"`
	FallbackOnly   *bool                   `json:"fallbackOnly,omitempty"`
	ModelOverrides *[]ProductModelOverride `json:"modelOverrides,omitempty"`
}

type ProductModelOverride struct {
	ID            string `json:"id"`
	IsChat        *bool  `json:"isChat,omitempty"`
	ToolCall      *bool  `json:"toolCall,omitempty"`
	ContextTokens *int   `json:"contextTokens,omitempty"`
	OutputTokens  *int   `json:"outputTokens,omitempty"`
}

type ProductProviderAccountView struct {
	ProductProviderAccount
	CredentialSourceID string               `json:"credentialSourceId"`
	CredentialStatus   string               `json:"credentialStatus"`
	Priority           *int                 `json:"priority,omitempty"`
	HealthSummary      string               `json:"healthSummary,omitempty"`
	RouteHealth        []ProductRouteHealth `json:"routeHealth,omitempty"`
}

type ProductModelRoute struct {
	ProviderID         string               `json:"providerId"`
	CredentialSourceID string               `json:"credentialSourceId"`
	ResolvedModel      string               `json:"resolvedModel"`
	Role               ProductModelRole     `json:"role"`
	ThinkingLevel      ProductThinkingLevel `json:"thinkingLevel"`
	CanonicalModelID   string               `json:"canonicalModelId,omitempty"`
	Source             string               `json:"source,omitempty"`
	CostTier           string               `json:"costTier,omitempty"`
	Reason             string               `json:"reason,omitempty"`
	ProviderName       string               `json:"providerName,omitempty"`
	Failover           string               `json:"failover,omitempty"`
}

type ProductResolvedRoles struct {
	Dialogue  *ProductModelRoute `json:"dialogue"`
	Coding    *ProductModelRoute `json:"coding"`
	Reasoning *ProductModelRoute `json:"reasoning"`
	Utility   *ProductModelRoute `json:"utility"`
}

type ProductRoutingStatus struct {
	ProductConnectionRevisions
	Policy             ProductRoutingPolicy                   `json:"policy"`
	EffectivePolicy    ProductRoutingPolicy                   `json:"effectivePolicy"`
	ResolvedRoles      ProductResolvedRoles                   `json:"resolvedRoles"`
	Overrides          ProductRoutingOverrides                `json:"overrides,omitempty"`
	EffectiveOverrides ProductRoutingOverrides                `json:"effectiveOverrides,omitempty"`
	RoleStatus         map[ProductModelRole]ProductRoleStatus `json:"roleStatus,omitempty"`
}

type ProductRoutingOverride struct {
	Mode                string `json:"mode"`
	CanonicalModelID    string `json:"canonicalModelId,omitempty"`
	ProviderID          string `json:"providerId,omitempty"`
	Model               string `json:"model,omitempty"`
	EndpointFingerprint string `json:"endpointFingerprint,omitempty"`
}

type ProductRoutingOverrides map[ProductModelRole]ProductRoutingOverride

type ProductRoutingOverridesPatch map[ProductModelRole]*ProductRoutingOverride

type ProductRoleStatus struct {
	Mode              string `json:"mode"`
	Status            string `json:"status"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
	RetryAt           string `json:"retryAt,omitempty"`
	Failover          string `json:"failover,omitempty"`
}

type ProductRouteHealth struct {
	WireModelID         string `json:"wireModelId,omitempty"`
	Scope               string `json:"scope"`
	State               string `json:"state"`
	Reason              string `json:"reason"`
	RetryAt             string `json:"retryAt,omitempty"`
	ConsecutiveFailures int    `json:"consecutiveFailures,omitempty"`
}

type ProductRouteAdmission struct {
	Route    *ProductModelRoute     `json:"route,omitempty"`
	Code     ProductDeliveryBlocker `json:"code,omitempty"`
	Delivery string                 `json:"delivery,omitempty"`
	Reason   string                 `json:"reason,omitempty"`
	RetryAt  string                 `json:"retryAt,omitempty"`
}

type ProductProviderList struct {
	ProductRoutingStatus
	Accounts []ProductProviderAccountView `json:"accounts"`
}

type ProductProviderTestParams struct {
	Account ProductProviderAccount `json:"account"`
	APIKey  string                 `json:"apiKey,omitempty"`
}

type ProductProviderTestResult struct {
	OK               bool                     `json:"ok"`
	Models           []string                 `json:"models"`
	LatencyMs        uint64                   `json:"latencyMs"`
	Error            string                   `json:"error,omitempty"`
	DiscoveredModels []ProductDiscoveredModel `json:"discoveredModels,omitempty"`
}

type ProductDiscoveredModel struct {
	ID             string `json:"id"`
	CanonicalID    string `json:"canonicalId"`
	IsChat         *bool  `json:"isChat"`
	ToolCall       *bool  `json:"toolCall"`
	Reasoning      bool   `json:"reasoning"`
	Tier           string `json:"tier"`
	PriceProtected bool   `json:"priceProtected"`
	Recommended    bool   `json:"recommended"`
	ContextTokens  int    `json:"contextTokens"`
	OutputTokens   int    `json:"outputTokens"`
}

type ProductProviderSaveParams struct {
	ProductProviderTestParams
	OperationID string `json:"operationId"`
}

type ProductProviderTargetParams struct {
	ProviderID  string `json:"providerId"`
	OperationID string `json:"operationId"`
}

type ProductProviderReorderParams struct {
	ProviderIDs   []string `json:"providerIds"`
	SavedRevision uint64   `json:"savedRevision"`
	OperationID   string   `json:"operationId"`
}

type ProductRoutingPolicyParams struct {
	Policy      ProductRoutingPolicy `json:"policy"`
	OperationID string               `json:"operationId"`
}

type ProductRoutingOverridesParams struct {
	SavedRevision uint64                       `json:"savedRevision"`
	Overrides     ProductRoutingOverridesPatch `json:"overrides"`
	OperationID   string                       `json:"operationId"`
}

type ProductProviderChange struct {
	ProductConnectionRevisions
	Change string `json:"change"`
}

func (event ProductProviderChange) valid() bool {
	switch event.Change {
	case "saved", "deleted", "applied", "revoked", "policy":
		return event.SavedRevision != nil && (event.EffectiveRevision == nil || *event.EffectiveRevision <= *event.SavedRevision)
	default:
		return false
	}
}

func (p *ProductClient) ListProviders(ctx context.Context) (ProductProviderList, error) {
	return productCall[ProductProviderList](ctx, p, "provider.list", struct{}{})
}

func (p *ProductClient) TestProvider(ctx context.Context, params ProductProviderTestParams) (ProductProviderTestResult, error) {
	return productCall[ProductProviderTestResult](ctx, p, "provider.test", params)
}

func (p *ProductClient) SaveProvider(ctx context.Context, params ProductProviderSaveParams) (ProductConnectionRevisions, error) {
	return productCall[ProductConnectionRevisions](ctx, p, "provider.save", params)
}

func (p *ProductClient) DeleteProvider(ctx context.Context, params ProductProviderTargetParams) (ProductConnectionRevisions, error) {
	return productCall[ProductConnectionRevisions](ctx, p, "provider.delete", params)
}

func (p *ProductClient) ReorderProviders(ctx context.Context, params ProductProviderReorderParams) (ProductConnectionRevisions, error) {
	if params.ProviderIDs == nil {
		params.ProviderIDs = []string{}
	}
	return productCall[ProductConnectionRevisions](ctx, p, "provider.reorder", params)
}

func (p *ProductClient) ApplyProviders(ctx context.Context, params ProductConnectionApplyParams) (ProductConnectionRevisions, error) {
	return productCall[ProductConnectionRevisions](ctx, p, "provider.apply", params)
}

func (p *ProductClient) RevokeProviderCredential(ctx context.Context, params ProductProviderTargetParams) (ProductConnectionRevisions, error) {
	return productCall[ProductConnectionRevisions](ctx, p, "provider.revokeCredential", params)
}

func (p *ProductClient) Routing(ctx context.Context) (ProductRoutingStatus, error) {
	return productCall[ProductRoutingStatus](ctx, p, "routing.get", struct{}{})
}

func (p *ProductClient) SetRoutingPolicy(ctx context.Context, params ProductRoutingPolicyParams) (ProductConnectionRevisions, error) {
	return productCall[ProductConnectionRevisions](ctx, p, "routing.setPolicy", params)
}

func (p *ProductClient) SetRoutingOverrides(ctx context.Context, params ProductRoutingOverridesParams) (ProductConnectionRevisions, error) {
	if params.Overrides == nil {
		params.Overrides = ProductRoutingOverridesPatch{}
	}
	return productCall[ProductConnectionRevisions](ctx, p, "routing.setOverrides", params)
}
