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

type ProductProviderAccount struct {
	ProviderID    string   `json:"providerId"`
	Name          string   `json:"name"`
	WireAPI       WireAPI  `json:"wireApi"`
	BaseURL       string   `json:"baseUrl"`
	Models        []string `json:"models,omitempty"`
	DefaultModel  string   `json:"defaultModel"`
	ContextTokens int      `json:"contextTokens"`
	OutputTokens  int      `json:"outputTokens"`
	Enabled       bool     `json:"enabled"`
}

type ProductProviderAccountView struct {
	ProductProviderAccount
	CredentialSourceID string `json:"credentialSourceId"`
	CredentialStatus   string `json:"credentialStatus"`
}

type ProductModelRoute struct {
	ProviderID         string               `json:"providerId"`
	CredentialSourceID string               `json:"credentialSourceId"`
	ResolvedModel      string               `json:"resolvedModel"`
	Role               ProductModelRole     `json:"role"`
	ThinkingLevel      ProductThinkingLevel `json:"thinkingLevel"`
}

type ProductResolvedRoles struct {
	Dialogue  *ProductModelRoute `json:"dialogue"`
	Coding    *ProductModelRoute `json:"coding"`
	Reasoning *ProductModelRoute `json:"reasoning"`
	Utility   *ProductModelRoute `json:"utility"`
}

type ProductRoutingStatus struct {
	ProductConnectionRevisions
	Policy          ProductRoutingPolicy `json:"policy"`
	EffectivePolicy ProductRoutingPolicy `json:"effectivePolicy"`
	ResolvedRoles   ProductResolvedRoles `json:"resolvedRoles"`
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
	OK        bool     `json:"ok"`
	Models    []string `json:"models"`
	LatencyMs uint64   `json:"latencyMs"`
	Error     string   `json:"error,omitempty"`
}

type ProductProviderSaveParams struct {
	ProductProviderTestParams
	OperationID string `json:"operationId"`
}

type ProductProviderTargetParams struct {
	ProviderID  string `json:"providerId"`
	OperationID string `json:"operationId"`
}

type ProductRoutingPolicyParams struct {
	Policy      ProductRoutingPolicy `json:"policy"`
	OperationID string               `json:"operationId"`
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
