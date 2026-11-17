package main

type runtimeConnection struct {
	ID           string `json:"id"`
	API          string `json:"api"`
	Endpoint     string `json:"endpoint"`
	DefaultModel string `json:"default_model"`
}

type runtimeExternalToolBinding struct {
	SourceID    string `json:"source_id"`
	ToolName    string `json:"tool_name"`
	ExposedName string `json:"exposed_name"`
}

type runtimeToolSelection struct {
	Builtin  []string                     `json:"builtin"`
	External []runtimeExternalToolBinding `json:"external"`
}

type runtimeBuiltinTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type runtimeToolList struct {
	Builtin []runtimeBuiltinTool `json:"builtin"`
}

func (c *runtimeClient) tools() (runtimeToolList, error) {
	var result runtimeToolList
	err := c.call("tool.list", map[string]any{}, &result, runtimeControlTimeout)
	return result, err
}

type runtimeConnectionConfig struct {
	SessionID       string   `json:"session_id"`
	ConfigRevision  uint64   `json:"config_revision"`
	StorageSchema   int      `json:"storage_schema"`
	ImmutableFields []string `json:"immutable_fields"`
	EffectiveConfig struct {
		ConnectionID          *string              `json:"connection_id"`
		ExecutionConnectionID *string              `json:"execution_connection_id"`
		Model                 string               `json:"model"`
		API                   string               `json:"api"`
		WorkingDirectory      string               `json:"working_directory"`
		SystemPrompt          string               `json:"system_prompt"`
		Compaction            runtimeCompaction    `json:"compaction"`
		ToolSelection         runtimeToolSelection `json:"tool_selection"`
		CapacitySource        struct {
			Kind      string `json:"kind"`
			CatalogID string `json:"catalog_id"`
		} `json:"capacity_source"`
		ConnectionBinding struct {
			State string `json:"state"`
		} `json:"connection_binding"`
	} `json:"effective_config"`
}

func (c *runtimeClient) connections() ([]runtimeConnection, error) {
	var result struct {
		Connections []runtimeConnection `json:"connections"`
	}
	err := c.call("connection.list", map[string]any{}, &result, runtimeControlTimeout)
	return result.Connections, err
}

func (c *runtimeClient) connectionConfig(sessionID string) (runtimeConnectionConfig, error) {
	var result runtimeConnectionConfig
	err := c.call("session.config.get", map[string]any{"session_id": sessionID}, &result, runtimeControlTimeout)
	return result, err
}

// updateSessionConfiguration changes only the explicitly supplied tool
// selection and/or system prompt. It does not rewrite model, connection, cwd or
// compaction.
func (c *runtimeClient) updateSessionConfiguration(sessionID string, revision uint64, selection map[string]any, systemPrompt string) (runtimeConfigUpdated, error) {
	patch := map[string]any{}
	if selection != nil {
		patch["tool_selection"] = selection
	}
	if systemPrompt != "" {
		patch["system_prompt"] = systemPrompt
	}
	var result runtimeConfigUpdated
	err := c.call("session.config.update", map[string]any{
		"session_id": sessionID, "expected_revision": revision,
		"patch": patch,
	}, &result, runtimeControlTimeout)
	if err != nil {
		// A lost reply is not proof of failure; read back the committed value
		// without submitting the patch a second time.
		result.runtimeConnectionConfig, result.ReadbackError = c.connectionConfig(sessionID)
	}
	return result, err
}
