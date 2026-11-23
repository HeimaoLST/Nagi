package connection

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
	APIResponses       = "openai-responses"
	APIChatCompletions = "openai-chat-completions"
	DefaultID          = "default"
)

// Config is the plaintext local GUI source; connection.json contains
// a private local value and must stay private to this user.
type Config struct {
	API              string `json:"api"`
	Endpoint         string `json:"endpoint"`
	APIKey           string `json:"api_key"`
	Model            string `json:"model"`
	ContextTokens    int    `json:"context_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	WorkingDirectory string `json:"working_directory"`
	MaxActiveTasks   int    `json:"max_active_tasks"`
}

type CredentialSource struct {
	API      string `json:"api"`
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"api_key"`
}

type PrivateConfig struct {
	Version          int                         `json:"version"`
	CredentialSource string                      `json:"credential_source"`
	Model            string                      `json:"model"`
	ContextTokens    int                         `json:"context_tokens"`
	OutputTokens     int                         `json:"output_tokens"`
	WorkingDirectory string                      `json:"working_directory"`
	MaxActiveTasks   int                         `json:"max_active_tasks"`
	Sources          map[string]CredentialSource `json:"sources"`
}

func ReadPrivateConfig(path string) (PrivateConfig, error) {
	var config PrivateConfig
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

func (config PrivateConfig) Resolve(sourceID string) (CredentialSource, error) {
	source, ok := config.Sources[sourceID]
	if !ok || sourceID == "" {
		return CredentialSource{}, fmt.Errorf("captured connection credential source is unavailable")
	}
	return source, nil
}

func (config PrivateConfig) Defaults() (Config, error) {
	source, err := config.Resolve(config.CredentialSource)
	if err != nil {
		return Config{}, err
	}
	return Config{API: source.API, Endpoint: source.Endpoint, APIKey: source.APIKey,
		Model: config.Model, ContextTokens: config.ContextTokens, OutputTokens: config.OutputTokens,
		WorkingDirectory: config.WorkingDirectory, MaxActiveTasks: config.MaxActiveTasks}, nil
}

func DefaultConfig(workDir string) Config {
	return Config{
		API:           APIChatCompletions,
		ContextTokens: 131072, OutputTokens: 8192,
		WorkingDirectory: workDir, MaxActiveTasks: 2,
	}
}

func LoadConfig(path string) (Config, error) {
	private, err := ReadPrivateConfig(path)
	if err != nil {
		return Config{}, err
	}
	config, err := private.Defaults()
	if err != nil {
		return Config{}, err
	}
	if fields := ValidateConfig(config); len(fields) > 0 {
		return config, fmt.Errorf("invalid connection config %s: %s", path, ValidationSummary(fields))
	}
	return config, nil
}

func SaveConfig(path string, config Config) error {
	if fields := ValidateConfig(config); len(fields) > 0 {
		return fmt.Errorf("invalid connection config: %s", ValidationSummary(fields))
	}
	private, err := ReadPrivateConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		private = PrivateConfig{Version: 2, Sources: make(map[string]CredentialSource)}
	} else if err != nil {
		return err
	}
	source := CredentialSource{API: config.API, Endpoint: config.Endpoint, APIKey: config.APIKey}
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
			private.Sources = make(map[string]CredentialSource)
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

// Stable validation kinds; internal config errors retain these kinds for diagnostics.
const (
	ValidationAPI                = "api"
	ValidationRequired           = "required"
	ValidationPositive           = "positive"
	ValidationLessThanContext    = "less-than-context"
	ValidationContextGap         = "context-gap"
	ValidationEndpointWhitespace = "endpoint-whitespace"
	ValidationEndpointURL        = "endpoint-url"
	ValidationEndpointUserinfo   = "endpoint-userinfo"
	ValidationEndpointQuery      = "endpoint-query"
	ValidationEndpointFragment   = "endpoint-fragment"
	ValidationWorkspaceAbsolute  = "workspace-absolute"
	ValidationWorkspaceDirectory = "workspace-directory"
)

func ValidateConfig(config Config) map[string]string {
	fields := map[string]string{}
	if config.API != APIResponses && config.API != APIChatCompletions {
		fields["API"] = ValidationAPI
	}
	validateEndpoint(config.Endpoint, fields)
	if strings.TrimSpace(config.APIKey) == "" {
		fields["APIKey"] = ValidationRequired
	}
	if strings.TrimSpace(config.Model) == "" {
		fields["Model"] = ValidationRequired
	}
	validateWorkingDirectory(config.WorkingDirectory, fields)
	if config.ContextTokens <= 0 || int64(config.ContextTokens) > 9007199254740991 {
		fields["ContextTokens"] = ValidationPositive
	}
	if config.OutputTokens <= 0 || int64(config.OutputTokens) > 9007199254740991 {
		fields["OutputTokens"] = ValidationPositive
	} else if config.ContextTokens > 0 {
		if config.ContextTokens <= config.OutputTokens {
			fields["OutputTokens"] = ValidationLessThanContext
		} else if config.ContextTokens-config.OutputTokens < 4 {
			fields["ContextTokens"] = ValidationContextGap
		}
	}
	if config.MaxActiveTasks <= 0 {
		fields["MaxActiveTasks"] = ValidationPositive
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func validateEndpoint(raw string, fields map[string]string) {
	if raw == "" {
		fields["Endpoint"] = ValidationRequired
		return
	}
	if raw != strings.TrimSpace(raw) {
		fields["Endpoint"] = ValidationEndpointWhitespace
		return
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		fields["Endpoint"] = ValidationEndpointURL
		return
	}
	if parsed.User != nil {
		fields["Endpoint"] = ValidationEndpointUserinfo
		return
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(raw, "?") {
		fields["Endpoint"] = ValidationEndpointQuery
		return
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		fields["Endpoint"] = ValidationEndpointFragment
	}
}

func validateWorkingDirectory(path string, fields map[string]string) {
	if path == "" || !filepath.IsAbs(path) {
		fields["WorkingDirectory"] = ValidationWorkspaceAbsolute
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		fields["WorkingDirectory"] = ValidationWorkspaceDirectory
	}
}

func ValidationSummary(fields map[string]string) string {
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
