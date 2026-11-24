package pi

import (
	"context"
	"encoding/json"
)

type ProductMcpBinding struct {
	ServerID     string   `json:"serverId"`
	Policy       string   `json:"policy"`
	AllowedTools []string `json:"allowedTools,omitempty"`
}

type ProductMcpServerConfig struct {
	ServerID         string            `json:"serverId"`
	Name             string            `json:"name"`
	Transport        string            `json:"transport"`
	Isolation        string            `json:"isolation"`
	Enabled          bool              `json:"enabled"`
	Command          string            `json:"command,omitempty"`
	Args             []string          `json:"args,omitempty"`
	Cwd              string            `json:"cwd,omitempty"`
	EnvKeys          []string          `json:"envKeys,omitempty"`
	SecretEnvKeys    []string          `json:"secretEnvKeys,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	URL              string            `json:"url,omitempty"`
	HeaderKeys       []string          `json:"headerKeys,omitempty"`
	SecretHeaderKeys []string          `json:"secretHeaderKeys,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
}

type ProductMcpServerInput struct {
	ProductMcpServerConfig
	Revision *uint64 `json:"revision,omitempty"`
}

type ProductMcpTool struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	InputSchema     json.RawMessage `json:"inputSchema,omitempty"`
	ReadOnlyHint    *bool           `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool           `json:"destructiveHint,omitempty"`
}

type ProductMcpCatalog struct {
	Tools     []ProductMcpTool `json:"tools"`
	FetchedAt string           `json:"fetchedAt"`
	Error     string           `json:"error,omitempty"`
}

type ProductMcpPool struct {
	ServerID  string `json:"serverId"`
	ScopeKey  string `json:"scopeKey"`
	Isolation string `json:"isolation"`
	State     string `json:"state"`
	RefCount  uint64 `json:"refCount"`
	PID       *int   `json:"pid,omitempty"`
}

type ProductMcpServerView struct {
	ProductMcpServerConfig
	Revision         uint64             `json:"revision"`
	Catalog          *ProductMcpCatalog `json:"catalog,omitempty"`
	CredentialStatus string             `json:"credentialStatus"`
	Pools            []ProductMcpPool   `json:"pools"`
}

type ProductMcpSecrets struct {
	Env     map[string]*string `json:"env,omitempty"`
	Headers map[string]*string `json:"headers,omitempty"`
}

type ProductMcpSaveParams struct {
	Server      ProductMcpServerInput `json:"server"`
	Secrets     *ProductMcpSecrets    `json:"secrets,omitempty"`
	OperationID string                `json:"operationId"`
}

type ProductMcpDeleteParams struct {
	ServerID    string `json:"serverId"`
	OperationID string `json:"operationId"`
}

type ProductMcpTestParams struct {
	ServerID string                 `json:"serverId,omitempty"`
	Server   *ProductMcpServerInput `json:"server,omitempty"`
	Secrets  *ProductMcpSecrets     `json:"secrets,omitempty"`
}

type ProductMcpTestResult struct {
	OK        bool             `json:"ok"`
	Tools     []ProductMcpTool `json:"tools"`
	Error     string           `json:"error,omitempty"`
	LatencyMS uint64           `json:"latencyMs"`
}

type ProductMcpChange struct {
	ServerID string `json:"serverId"`
	Change   string `json:"change"`
}

func (event ProductMcpChange) valid() bool {
	return event.ServerID != "" && (event.Change == "saved" || event.Change == "deleted" || event.Change == "tested")
}

func (p *ProductClient) ListMcpServers(ctx context.Context) ([]ProductMcpServerView, error) {
	result, err := productCall[struct {
		Servers []ProductMcpServerView `json:"servers"`
	}](ctx, p, "mcp.list", struct{}{})
	return result.Servers, err
}

func (p *ProductClient) SaveMcpServer(ctx context.Context, params ProductMcpSaveParams) (ProductMcpServerView, error) {
	result, err := productCall[struct {
		Server ProductMcpServerView `json:"server"`
	}](ctx, p, "mcp.save", params)
	return result.Server, err
}

func (p *ProductClient) DeleteMcpServer(ctx context.Context, params ProductMcpDeleteParams) (bool, error) {
	result, err := productCall[struct {
		Deleted bool `json:"deleted"`
	}](ctx, p, "mcp.delete", params)
	return result.Deleted, err
}

func (p *ProductClient) TestMcpServer(ctx context.Context, params ProductMcpTestParams) (ProductMcpTestResult, error) {
	return productCall[ProductMcpTestResult](ctx, p, "mcp.test", params)
}

func (p *ProductClient) McpStatus(ctx context.Context) ([]ProductMcpPool, error) {
	result, err := productCall[struct {
		Pools []ProductMcpPool `json:"pools"`
	}](ctx, p, "mcp.status", struct{}{})
	return result.Pools, err
}
