package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testConnectionConfig(t *testing.T) connectionConfig {
	return connectionConfig{
		API:              connectionAPIResponses,
		Endpoint:         "https://example.com/v1",
		APIKey:           "test-connection-key",
		Model:            "test-model",
		ContextTokens:    65536,
		OutputTokens:     8192,
		WorkingDirectory: t.TempDir(),
		MaxActiveTasks:   2,
	}
}

func TestConnectionConfigSaveLoadPermissionsAndRestart(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private", "connection.json")
	config := testConnectionConfig(t)
	if err := saveConnectionConfig(path, config); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("connection file mode = %o, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("connection directory mode = %o, want 0700", dirInfo.Mode().Perm())
	}
	loaded, err := loadConnectionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Endpoint != config.Endpoint || loaded.Model != config.Model || loaded.APIKey != config.APIKey ||
		loaded.ContextTokens != config.ContextTokens || loaded.OutputTokens != config.OutputTokens ||
		loaded.WorkingDirectory != config.WorkingDirectory || loaded.MaxActiveTasks != config.MaxActiveTasks {
		t.Fatal("saved connection config did not round-trip")
	}

	config.Model = "edited-model"
	config.Endpoint = "https://edited.example/v1"
	if err := saveConnectionConfig(path, config); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadConnectionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Model != "edited-model" || loaded.Endpoint != "https://edited.example/v1" {
		t.Fatal("edited connection config did not round-trip")
	}
}

func TestConnectionConfigSaveFailureKeepsPreviousFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connection.json")
	original := testConnectionConfig(t)
	if err := saveConnectionConfig(path, original); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	edited := original
	edited.Model = "must-not-be-saved"
	edited.Endpoint = "https://must-not-be-saved.example/v1"
	if err := saveConnectionConfig(path, edited); err == nil {
		t.Fatal("save unexpectedly succeeded into a read-only directory")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed save changed the previous connection file")
	}
	loaded, err := loadConnectionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Model != original.Model || loaded.Endpoint != original.Endpoint {
		t.Fatal("previous connection values were not retained")
	}
}

func TestConnectionConfigLoadRejectsBadFilesWithoutReplacingThem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connection.json")
	original := []byte(`{"version":1,`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConnectionConfig(path); err == nil {
		t.Fatal("malformed connection file was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("bad connection file was replaced")
	}

	unsupported, err := json.Marshal(privateConnectionConfig{Version: 1, CredentialSource: "saved-source", Model: "m", ContextTokens: 32768,
		OutputTokens: 8192, WorkingDirectory: dir, MaxActiveTasks: 1,
		Sources: map[string]connectionCredentialSource{"saved-source": {API: connectionAPIChatCompletions, Endpoint: "https://example.com/v1", APIKey: "fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, unsupported, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConnectionConfig(path); err == nil || !strings.Contains(err.Error(), "connection config version 1 is unsupported") {
		t.Fatalf("load unsupported disk schema: %v", err)
	}
	after, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(after, unsupported) {
		t.Fatalf("load changed the unsupported connection file: %v", err)
	}

	secret := "bad-file-secret-value"
	invalidConfig := privateConnectionConfig{Version: 2, CredentialSource: "saved-source", Model: "m", ContextTokens: 32768,
		OutputTokens: 8192, WorkingDirectory: dir, MaxActiveTasks: 1,
		Sources: map[string]connectionCredentialSource{"saved-source": {API: "chat", Endpoint: "https://example.com/v1", APIKey: secret}}}
	invalidBytes, err := json.Marshal(invalidConfig)
	if err != nil {
		t.Fatal(err)
	}
	invalid := string(invalidBytes)
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	repairable, err := loadConnectionConfig(path)
	if err == nil {
		t.Fatal("invalid connection config was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("load error exposed sensitive data")
	}
	if repairable.Model != "m" || repairable.APIKey != secret {
		t.Fatal("invalid saved values must remain available for correction in the form")
	}
	after, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != invalid {
		t.Fatal("invalid connection file was replaced")
	}

	if _, err := loadConnectionConfig(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing connection file was silently created")
	}
}

func TestConnectionConfigValidationFields(t *testing.T) {
	valid := testConnectionConfig(t)
	if fields := validateConnectionConfig(valid); len(fields) != 0 {
		t.Fatalf("valid config rejected with fields %v", sortedKeys(fields))
	}
	valid.API = connectionAPIChatCompletions
	if fields := validateConnectionConfig(valid); len(fields) != 0 {
		t.Fatal("chat completions API was rejected")
	}

	check := func(name string, mutate func(*connectionConfig), key string) {
		t.Helper()
		config := testConnectionConfig(t)
		mutate(&config)
		fields := validateConnectionConfig(config)
		if fields[key] == "" {
			t.Fatalf("%s: missing error for %s", name, key)
		}
	}
	check("api shorthand", func(c *connectionConfig) { c.API = "chat" }, "API")
	check("blank key", func(c *connectionConfig) { c.APIKey = " \t " }, "APIKey")
	check("blank model", func(c *connectionConfig) { c.Model = " \n" }, "Model")
	check("zero context", func(c *connectionConfig) { c.ContextTokens = 0 }, "ContextTokens")
	check("zero output", func(c *connectionConfig) { c.OutputTokens = 0 }, "OutputTokens")
	check("output exceeds context", func(c *connectionConfig) { c.ContextTokens = 100; c.OutputTokens = 100 }, "OutputTokens")
	check("context leaves no compaction room", func(c *connectionConfig) { c.ContextTokens = 100; c.OutputTokens = 97 }, "ContextTokens")
	check("relative cwd", func(c *connectionConfig) { c.WorkingDirectory = "relative/path" }, "WorkingDirectory")
	check("missing cwd", func(c *connectionConfig) { c.WorkingDirectory = filepath.Join(t.TempDir(), "missing") }, "WorkingDirectory")
	check("zero capacity", func(c *connectionConfig) { c.MaxActiveTasks = 0 }, "MaxActiveTasks")

	for _, endpoint := range []string{
		"",
		"ftp://example.com/v1",
		"https://user@example.com/v1",
		"https://user:pw@example.com/v1",
		"https://example.com/v1?x=1",
		"https://example.com/v1?",
		"https://example.com/v1#fragment",
		"https://example.com/v1#",
		"relative/path",
		" https://example.com/v1",
	} {
		config := testConnectionConfig(t)
		config.Endpoint = endpoint
		fields := validateConnectionConfig(config)
		if fields["Endpoint"] == "" {
			t.Fatalf("endpoint %q: missing Endpoint error", endpoint)
		}
	}
}

func TestConnectionConfigRejectsInvalidSaveWithoutCreatingData(t *testing.T) {
	config := testConnectionConfig(t)
	config.APIKey = " "
	root := t.TempDir()
	if err := saveConnectionConfig(filepath.Join(root, "connection.json"), config); err == nil {
		t.Fatal("invalid config was accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("invalid config created persisted data")
	}
}

func environmentMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
