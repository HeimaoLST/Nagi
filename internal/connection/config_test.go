package connection

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testConnectionConfig(t *testing.T) Config {
	return Config{
		API:              APIResponses,
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
	if err := SaveConfig(path, config); err != nil {
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
	loaded, err := LoadConfig(path)
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
	if err := SaveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadConfig(path)
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
	if err := SaveConfig(path, original); err != nil {
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
	if err := SaveConfig(path, edited); err == nil {
		t.Fatal("save unexpectedly succeeded into a read-only directory")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed save changed the previous connection file")
	}
	loaded, err := LoadConfig(path)
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
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("malformed connection file was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("bad connection file was replaced")
	}

	unsupported, err := json.Marshal(PrivateConfig{Version: 1, CredentialSource: "saved-source", Model: "m", ContextTokens: 32768,
		OutputTokens: 8192, WorkingDirectory: dir, MaxActiveTasks: 1,
		Sources: map[string]CredentialSource{"saved-source": {API: APIChatCompletions, Endpoint: "https://example.com/v1", APIKey: "fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, unsupported, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "connection config version 1 is unsupported") {
		t.Fatalf("load unsupported disk schema: %v", err)
	}
	after, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(after, unsupported) {
		t.Fatalf("load changed the unsupported connection file: %v", err)
	}

	secret := "bad-file-secret-value"
	invalidConfig := PrivateConfig{Version: 2, CredentialSource: "saved-source", Model: "m", ContextTokens: 32768,
		OutputTokens: 8192, WorkingDirectory: dir, MaxActiveTasks: 1,
		Sources: map[string]CredentialSource{"saved-source": {API: "chat", Endpoint: "https://example.com/v1", APIKey: secret}}}
	invalidBytes, err := json.Marshal(invalidConfig)
	if err != nil {
		t.Fatal(err)
	}
	invalid := string(invalidBytes)
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	repairable, err := LoadConfig(path)
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

	if _, err := LoadConfig(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing connection file was silently created")
	}
}

func TestConnectionConfigValidationFields(t *testing.T) {
	valid := testConnectionConfig(t)
	if fields := ValidateConfig(valid); len(fields) != 0 {
		t.Fatalf("valid config rejected with fields %v", sortedKeys(fields))
	}
	valid.API = APIChatCompletions
	if fields := ValidateConfig(valid); len(fields) != 0 {
		t.Fatal("chat completions API was rejected")
	}

	check := func(name string, mutate func(*Config), key string) {
		t.Helper()
		config := testConnectionConfig(t)
		mutate(&config)
		fields := ValidateConfig(config)
		if fields[key] == "" {
			t.Fatalf("%s: missing error for %s", name, key)
		}
	}
	check("api shorthand", func(c *Config) { c.API = "chat" }, "API")
	check("blank key", func(c *Config) { c.APIKey = " \t " }, "APIKey")
	check("blank model", func(c *Config) { c.Model = " \n" }, "Model")
	check("zero context", func(c *Config) { c.ContextTokens = 0 }, "ContextTokens")
	check("zero output", func(c *Config) { c.OutputTokens = 0 }, "OutputTokens")
	check("output exceeds context", func(c *Config) { c.ContextTokens = 100; c.OutputTokens = 100 }, "OutputTokens")
	check("context leaves no compaction room", func(c *Config) { c.ContextTokens = 100; c.OutputTokens = 97 }, "ContextTokens")
	check("relative cwd", func(c *Config) { c.WorkingDirectory = "relative/path" }, "WorkingDirectory")
	check("missing cwd", func(c *Config) { c.WorkingDirectory = filepath.Join(t.TempDir(), "missing") }, "WorkingDirectory")
	check("zero capacity", func(c *Config) { c.MaxActiveTasks = 0 }, "MaxActiveTasks")

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
		fields := ValidateConfig(config)
		if fields["Endpoint"] == "" {
			t.Fatalf("endpoint %q: missing Endpoint error", endpoint)
		}
	}
}

func TestConnectionConfigRejectsInvalidSaveWithoutCreatingData(t *testing.T) {
	config := testConnectionConfig(t)
	config.APIKey = " "
	root := t.TempDir()
	if err := SaveConfig(filepath.Join(root, "connection.json"), config); err == nil {
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

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func TestConnectionConfigV3AccountsPublicationAndVersionGuards(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connection.json")
	rawAccounts := json.RawMessage(`[{"providerId":"prov-1","name":"Studio","wireApi":"openai-completions","baseUrl":"https://api.example.com/v1","defaultModel":"gpt-4o","models":["gpt-4o"],"contextTokens":131072,"outputTokens":8192,"enabled":true}]`)

	v3ProviderOnly := PrivateConfig{
		Version:       3,
		Sources:       map[string]CredentialSource{"prov-1": {API: APIChatCompletions, Endpoint: "https://api.example.com/v1", APIKey: "sk-v3-only"}},
		Accounts:      rawAccounts,
		PublicationID: "pub-v3-001",
	}
	data, err := json.MarshalIndent(v3ProviderOnly, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	compact := func(raw json.RawMessage) string {
		t.Helper()
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	got, err := ReadPrivateConfig(path)
	if err != nil {
		t.Fatalf("ReadPrivateConfig v3 provider-only failed: %v", err)
	}
	if got.Version != 3 || got.PublicationID != "pub-v3-001" || compact(got.Accounts) != string(rawAccounts) {
		t.Fatalf("v3 fields mismatch: version=%d pub=%q accounts=%s", got.Version, got.PublicationID, string(got.Accounts))
	}

	legacy := testConnectionConfig(t)
	if err := SaveConfig(path, legacy); err != nil {
		t.Fatalf("SaveConfig onto v3 file failed: %v", err)
	}
	afterSave, err := ReadPrivateConfig(path)
	if err != nil {
		t.Fatalf("ReadPrivateConfig after SaveConfig on v3 failed: %v", err)
	}
	if afterSave.Version != 3 || afterSave.PublicationID != "pub-v3-001" || compact(afterSave.Accounts) != string(rawAccounts) {
		t.Fatalf("SaveConfig dropped v3 fields: version=%d pub=%q accounts=%s", afterSave.Version, afterSave.PublicationID, string(afterSave.Accounts))
	}
	if _, ok := afterSave.Sources["prov-1"]; !ok {
		t.Fatalf("SaveConfig dropped existing provider credential source")
	}
	defaults, err := afterSave.Defaults()
	if err != nil || defaults.Model != legacy.Model || defaults.Endpoint != legacy.Endpoint {
		t.Fatalf("SaveConfig legacy defaults mismatch: %+v err=%v", defaults, err)
	}

	for _, tc := range []struct {
		name string
		cfg  PrivateConfig
	}{
		{"v2-with-accounts", PrivateConfig{Version: 2, Accounts: rawAccounts, Sources: map[string]CredentialSource{}}},
		{"v2-with-publicationId", PrivateConfig{Version: 2, PublicationID: "pub-bad", Sources: map[string]CredentialSource{}}},
		{"v4-unsupported", PrivateConfig{Version: 4, Sources: map[string]CredentialSource{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			badPath := filepath.Join(dir, tc.name+".json")
			raw, err := json.Marshal(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(badPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadPrivateConfig(badPath); err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("%s expected unsupported version error, got %v", tc.name, err)
			}
			untouched, err := os.ReadFile(badPath)
			if err != nil || !bytes.Equal(untouched, raw) {
				t.Fatalf("%s mutated file on rejection", tc.name)
			}
		})
	}
}
