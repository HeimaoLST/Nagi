package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	connectionAPIResponses       = "openai-responses"
	connectionAPIChatCompletions = "openai-chat-completions"
	connectionID                 = "default"
)

// connectionConfig is the plaintext local GUI source; connection.json contains
// a private local value and must stay private to this user.
type connectionConfig struct {
	Version          int    `json:"version"`
	API              string `json:"api"`
	Endpoint         string `json:"endpoint"`
	APIKey           string `json:"api_key"`
	Model            string `json:"model"`
	ContextTokens    int    `json:"context_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	WorkingDirectory string `json:"working_directory"`
	MaxActiveTasks   int    `json:"max_active_tasks"`
}

type connectionCredentialSource struct {
	API      string `json:"api"`
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"api_key"`
}

type privateConnectionConfig struct {
	Version          int                                   `json:"version"`
	CredentialSource string                                `json:"credential_source"`
	Model            string                                `json:"model"`
	ContextTokens    int                                   `json:"context_tokens"`
	OutputTokens     int                                   `json:"output_tokens"`
	WorkingDirectory string                                `json:"working_directory"`
	MaxActiveTasks   int                                   `json:"max_active_tasks"`
	Sources          map[string]connectionCredentialSource `json:"sources"`
}

func readPrivateConnectionConfig(path string) (privateConnectionConfig, error) {
	var config privateConnectionConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return config, fmt.Errorf("read connection config %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("parse private connection config %s: %w", path, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return config, fmt.Errorf("parse private connection config %s: trailing data", path)
	}
	if config.Version != 2 {
		return config, fmt.Errorf("connection config version %d is unsupported", config.Version)
	}
	return config, nil
}

func (config privateConnectionConfig) resolve(sourceID string) (connectionCredentialSource, error) {
	source, ok := config.Sources[sourceID]
	if !ok || sourceID == "" {
		return connectionCredentialSource{}, fmt.Errorf("captured connection credential source is unavailable")
	}
	return source, nil
}

func (config privateConnectionConfig) defaults() (connectionConfig, error) {
	source, err := config.resolve(config.CredentialSource)
	if err != nil {
		return connectionConfig{}, err
	}
	return connectionConfig{Version: 1, API: source.API, Endpoint: source.Endpoint, APIKey: source.APIKey,
		Model: config.Model, ContextTokens: config.ContextTokens, OutputTokens: config.OutputTokens,
		WorkingDirectory: config.WorkingDirectory, MaxActiveTasks: config.MaxActiveTasks}, nil
}

func defaultConnectionConfig(workDir string) connectionConfig {
	return connectionConfig{
		Version: 1, API: connectionAPIChatCompletions,
		ContextTokens: 131072, OutputTokens: 8192,
		WorkingDirectory: workDir, MaxActiveTasks: 2,
	}
}

func loadConnectionConfig(path string) (connectionConfig, error) {
	private, err := readPrivateConnectionConfig(path)
	if err != nil {
		return connectionConfig{}, err
	}
	config, err := private.defaults()
	if err != nil {
		return connectionConfig{}, err
	}
	if fields := validateConnectionConfig(config); len(fields) > 0 {
		return config, fmt.Errorf("invalid connection config %s: %s", path, connectionValidationSummary(fields))
	}
	return config, nil
}

func saveConnectionConfig(path string, config connectionConfig) error {
	if fields := validateConnectionConfig(config); len(fields) > 0 {
		return fmt.Errorf("invalid connection config: %s", connectionValidationSummary(fields))
	}
	private, err := readPrivateConnectionConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		private = privateConnectionConfig{Version: 2, Sources: make(map[string]connectionCredentialSource)}
	} else if err != nil {
		return err
	}
	source := connectionCredentialSource{API: config.API, Endpoint: config.Endpoint, APIKey: config.APIKey}
	sourceID := ""
	for id, existing := range private.Sources {
		if existing == source {
			sourceID = id
			break
		}
	}
	if sourceID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return fmt.Errorf("generate connection source: %w", err)
		}
		sourceID = hex.EncodeToString(id[:])
		if private.Sources == nil {
			private.Sources = make(map[string]connectionCredentialSource)
		}
		if _, exists := private.Sources[sourceID]; exists {
			return fmt.Errorf("generated connection source already exists")
		}
		private.Sources[sourceID] = source
	}
	private.CredentialSource = sourceID
	private.Model, private.ContextTokens, private.OutputTokens = config.Model, config.ContextTokens, config.OutputTokens
	private.WorkingDirectory, private.MaxActiveTasks = config.WorkingDirectory, config.MaxActiveTasks
	data, err := json.MarshalIndent(private, "", "  ")
	if err != nil {
		return fmt.Errorf("encode connection config: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create connection config directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".connection-*.tmp")
	if err != nil {
		return fmt.Errorf("create connection config temp file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("set connection config permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write connection config: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync connection config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close connection config: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace connection config: %w", err)
	}
	return nil
}

// Stable validation kinds. The form says them again in the current language;
// internal config errors keep the kinds for diagnostics.
const (
	connectionValidationVersion            = "version"
	connectionValidationAPI                = "api"
	connectionValidationRequired           = "required"
	connectionValidationPositive           = "positive"
	connectionValidationLessThanContext    = "less-than-context"
	connectionValidationContextGap         = "context-gap"
	connectionValidationEndpointWhitespace = "endpoint-whitespace"
	connectionValidationEndpointURL        = "endpoint-url"
	connectionValidationEndpointUserinfo   = "endpoint-userinfo"
	connectionValidationEndpointQuery      = "endpoint-query"
	connectionValidationEndpointFragment   = "endpoint-fragment"
	connectionValidationWorkspaceAbsolute  = "workspace-absolute"
	connectionValidationWorkspaceDirectory = "workspace-directory"
)

func validateConnectionConfig(config connectionConfig) map[string]string {
	fields := map[string]string{}
	if config.Version != 1 {
		fields["Version"] = connectionValidationVersion
	}
	if config.API != connectionAPIResponses && config.API != connectionAPIChatCompletions {
		fields["API"] = connectionValidationAPI
	}
	validateConnectionEndpoint(config.Endpoint, fields)
	if strings.TrimSpace(config.APIKey) == "" {
		fields["APIKey"] = connectionValidationRequired
	}
	if strings.TrimSpace(config.Model) == "" {
		fields["Model"] = connectionValidationRequired
	}
	validateConnectionWorkingDirectory(config.WorkingDirectory, fields)
	if config.ContextTokens <= 0 || int64(config.ContextTokens) > 9007199254740991 {
		fields["ContextTokens"] = connectionValidationPositive
	}
	if config.OutputTokens <= 0 || int64(config.OutputTokens) > 9007199254740991 {
		fields["OutputTokens"] = connectionValidationPositive
	} else if config.ContextTokens > 0 {
		if config.ContextTokens <= config.OutputTokens {
			fields["OutputTokens"] = connectionValidationLessThanContext
		} else if config.ContextTokens-config.OutputTokens < 4 {
			fields["ContextTokens"] = connectionValidationContextGap
		}
	}
	if config.MaxActiveTasks <= 0 {
		fields["MaxActiveTasks"] = connectionValidationPositive
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func validateConnectionEndpoint(raw string, fields map[string]string) {
	if raw == "" {
		fields["Endpoint"] = connectionValidationRequired
		return
	}
	if raw != strings.TrimSpace(raw) {
		fields["Endpoint"] = connectionValidationEndpointWhitespace
		return
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		fields["Endpoint"] = connectionValidationEndpointURL
		return
	}
	if parsed.User != nil {
		fields["Endpoint"] = connectionValidationEndpointUserinfo
		return
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(raw, "?") {
		fields["Endpoint"] = connectionValidationEndpointQuery
		return
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		fields["Endpoint"] = connectionValidationEndpointFragment
	}
}

func validateConnectionWorkingDirectory(path string, fields map[string]string) {
	if path == "" || !filepath.IsAbs(path) {
		fields["WorkingDirectory"] = connectionValidationWorkspaceAbsolute
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		fields["WorkingDirectory"] = connectionValidationWorkspaceDirectory
	}
}

func connectionValidationSummary(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+": "+fields[key])
	}
	return strings.Join(parts, "; ")
}
