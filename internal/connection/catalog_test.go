package connection

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProviderCatalogEmbeddedOrderAndMetadata(t *testing.T) {
	cat := LoadProviderCatalog("")
	if len(cat.Warnings) != 0 {
		t.Fatalf("embedded catalog produced warnings: %v", cat.Warnings)
	}
	if len(cat.FallbackIconSVG) == 0 || !strings.Contains(string(cat.FallbackIconSVG), "<svg") {
		t.Fatalf("embedded fallback SVG invalid: len=%d", len(cat.FallbackIconSVG))
	}
	if len(cat.Sections) != 2 || cat.Sections[0].ID != "local" || cat.Sections[1].ID != "cloud" {
		t.Fatalf("embedded section order = %+v, want [local, cloud]", cat.Sections)
	}
	if cat.Sections[0].Collapsible || !cat.Sections[1].Collapsible {
		t.Fatalf("section collapsible flags = local:%v cloud:%v, want false/true", cat.Sections[0].Collapsible, cat.Sections[1].Collapsible)
	}

	gotLocal := make([]string, 0)
	for _, p := range cat.SectionProviders("local") {
		gotLocal = append(gotLocal, p.ID)
	}
	wantLocal := []string{"ollama", "lmstudio", "custom"}
	if !slices.Equal(gotLocal, wantLocal) {
		t.Fatalf("local preset order = %v, want %v", gotLocal, wantLocal)
	}

	gotCloud := make([]string, 0)
	for _, p := range cat.SectionProviders("cloud") {
		gotCloud = append(gotCloud, p.ID)
	}
	wantCloud := []string{"openai", "google", "deepseek", "xai", "moonshot", "zhipu", "minimax", "volcengine", "qwen", "openrouter"}
	if !slices.Equal(gotCloud, wantCloud) {
		t.Fatalf("cloud preset order = %v, want %v", gotCloud, wantCloud)
	}
	if len(cat.Presets) != len(wantLocal)+len(wantCloud) {
		t.Fatalf("total presets = %d, want %d", len(cat.Presets), len(wantLocal)+len(wantCloud))
	}

	for _, preset := range cat.Presets {
		if preset.DefaultModel != "" {
			t.Fatalf("built-in preset %q unexpectedly hardcoded DefaultModel %q", preset.ID, preset.DefaultModel)
		}
		if len(preset.IconSVG) == 0 || !strings.Contains(string(preset.IconSVG), "<svg") {
			t.Fatalf("preset %q missing valid SVG bytes", preset.ID)
		}
		if preset.WireAPI != "openai-completions" && preset.WireAPI != "openai-responses" {
			t.Fatalf("preset %q unexpected wireApi %q", preset.ID, preset.WireAPI)
		}
		if preset.Name.Resolve("en") == "" {
			t.Fatalf("preset %q missing English name", preset.ID)
		}
		if got, ok := cat.PresetByID(preset.ID); !ok || got.ID != preset.ID {
			t.Fatalf("PresetByID(%q) failed: %+v ok=%v", preset.ID, got, ok)
		}
	}
	if ollama, ok := cat.PresetByID("ollama"); !ok || !ollama.NoKey || ollama.Custom {
		t.Fatalf("ollama metadata mismatch: %+v ok=%v", ollama, ok)
	}
	if custom, ok := cat.PresetByID("custom"); !ok || !custom.Custom || custom.Endpoint != "" || custom.DefaultModel != "" {
		t.Fatalf("custom metadata mismatch: %+v ok=%v", custom, ok)
	}
	if _, ok := cat.PresetByID("does-not-exist"); ok {
		t.Fatal("PresetByID matched nonexistent ID")
	}
}

func TestProviderCatalogLocalizedTextResolveAndMatchAccount(t *testing.T) {
	text := LocalizedText{"en": "Zhipu GLM", "zh-Hans": "智谱 GLM", "fr": "Zhipu"}
	for _, tc := range []struct {
		locale string
		want   string
	}{
		{"zh-Hans", "智谱 GLM"},
		{"zh-Hans-CN", "智谱 GLM"},
		{"zh-CN", "智谱 GLM"},
		{"fr-CA", "Zhipu"},
		{"de-DE", "Zhipu GLM"},
		{"", "Zhipu GLM"},
	} {
		if got := text.Resolve(tc.locale); got != tc.want {
			t.Errorf("Resolve(%q) = %q, want %q", tc.locale, got, tc.want)
		}
	}
	onlyJa := LocalizedText{"ja": "日本語"}
	if got := onlyJa.Resolve("en"); got != "日本語" {
		t.Fatalf("Resolve fallback without en = %q, want 日本語", got)
	}
	withDashKey := LocalizedText{"-x": "DashPrefix", "en": "English"}
	if got := withDashKey.Resolve(""); got != "English" {
		t.Fatalf("Resolve(\"\") matched dash key = %q, want English", got)
	}
	if got := (LocalizedText{}).Resolve("en"); got != "" {
		t.Fatalf("empty LocalizedText resolved to %q", got)
	}

	cat := LoadProviderCatalog("")
	for _, tc := range []struct {
		baseURL string
		name    string
		wantID  string
		wantOK  bool
	}{
		{"http://127.0.0.1:11434/v1", "", "ollama", true},
		{"http://localhost:11434/v1", "My Local", "ollama", true},
		{"http://127.0.0.1:9999/v1", "Unrelated", "", false},
		{"http://localhost:1234/v1", "", "lmstudio", true},
		{"https://api.openai.com./v1", "", "openai", true},
		{"https://api.kimi.ai/v1", "", "moonshot", true},
		{"https://api.z.ai/v1", "", "zhipu", true},
		{"https://dashscope-intl.aliyuncs.com/compatible-mode/v1", "", "qwen", true},
		{"https://proxy.example.invalid/v1", "智谱 GLM", "zhipu", true},
		{"https://proxy.example.invalid/v1", "DEEPSEEK", "deepseek", true},
		{"https://proxy.example.invalid/v1", "Custom API", "", false},
		{"", "", "", false},
	} {
		got, ok := cat.MatchAccount(tc.baseURL, tc.name)
		if ok != tc.wantOK || got.ID != tc.wantID {
			t.Errorf("MatchAccount(%q, %q) = (%q, %v), want (%q, %v)", tc.baseURL, tc.name, got.ID, ok, tc.wantID, tc.wantOK)
		}
	}
}

func TestProviderCatalogLayeredPluginsOverridesHiddenAndSecurity(t *testing.T) {
	profileDir := t.TempDir()
	outsideDir := t.TempDir()
	const validSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10" fill="currentColor"/></svg>`
	const secretSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect width="24" height="24" fill="#ff0000"/></svg>`

	outsideSVG := filepath.Join(outsideDir, "secret.svg")
	if err := os.WriteFile(outsideSVG, []byte(secretSVG), 0o600); err != nil {
		t.Fatal(err)
	}

	writeFile := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	pluginA := filepath.Join(profileDir, "provider-plugins", "01-enterprise")
	writeFile(filepath.Join(pluginA, "icons", "corp.svg"), validSVG)
	writeFile(filepath.Join(pluginA, "icons", "huge.svg"), strings.Repeat(" ", providerIconLimit+16))
	if err := os.Symlink(outsideSVG, filepath.Join(pluginA, "icons", "escape.svg")); err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(pluginA, "providers.json"), `{
  "version": 1,
  "sections": [
    {"id": "enterprise", "order": 30, "collapsible": false, "title": {"en": "Enterprise", "zh-Hans": "企业内部"}}
  ],
  "providers": [
    {"id": "corp-gw", "section": "enterprise", "order": 10, "name": {"en": "Corp Gateway"}, "icon": "corp.svg", "endpoint": "https://gw.corp.invalid/v1", "wireApi": "openai-completions", "defaultModel": "corp-1"},
    {"id": "escape-gw", "section": "enterprise", "order": 20, "name": {"en": "Escape Test"}, "icon": "escape.svg", "endpoint": "https://escape.invalid/v1", "wireApi": "openai-completions", "defaultModel": "m1"},
    {"id": "huge-gw", "section": "enterprise", "order": 30, "name": {"en": "Huge Test"}, "icon": "huge.svg", "endpoint": "https://huge.invalid/v1", "wireApi": "openai-completions", "defaultModel": "m1"},
    {"id": "traversal-gw", "section": "enterprise", "order": 40, "name": {"en": "Traversal"}, "icon": "../secret.svg", "endpoint": "https://trav.invalid/v1", "wireApi": "openai-completions", "defaultModel": "m1"},
    {"id": "nulicon-gw", "section": "enterprise", "order": 45, "name": {"en": "NulIcon"}, "icon": "bad\u0000.svg", "endpoint": "https://nul.invalid/v1", "wireApi": "openai-completions", "defaultModel": "m1"},
    {"id": "badwire-gw", "section": "enterprise", "order": 50, "name": {"en": "Bad Wire"}, "endpoint": "https://bad.invalid/v1", "wireApi": "anthropic-messages", "defaultModel": "m1"},
    {"id": "openai", "defaultModel": "gpt-5-plugin"}
  ]
}`)

	pluginBroken := filepath.Join(profileDir, "provider-plugins", "02-malformed")
	writeFile(filepath.Join(pluginBroken, "providers.json"), `{not valid json`)

	pluginBadVer := filepath.Join(profileDir, "provider-plugins", "03-bad-version")
	writeFile(filepath.Join(pluginBadVer, "providers.json"), `{"version": 99, "providers": []}`)

	pluginSymlinkIcons := filepath.Join(profileDir, "provider-plugins", "04-symlink-icons-dir")
	if err := os.MkdirAll(pluginSymlinkIcons, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(pluginSymlinkIcons, "icons")); err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(pluginSymlinkIcons, "providers.json"), `{
  "version": 1,
  "providers": [
    {"id": "symdir-gw", "section": "enterprise", "order": 60, "name": {"en": "Symdir Test"}, "icon": "secret.svg", "endpoint": "https://symdir.invalid/v1", "wireApi": "openai-completions", "defaultModel": "m1"}
  ]
}`)

	writeFile(filepath.Join(profileDir, "providers.json"), `{
  "version": 1,
  "providers": [
    {"id": "openai", "defaultModel": "gpt-5-profile"},
    {"id": "xai", "hidden": true}
  ]
}`)

	cat := LoadProviderCatalog(profileDir)
	if len(cat.Sections) != 3 || cat.Sections[2].ID != "enterprise" {
		t.Fatalf("expected custom enterprise section appended last: %+v", cat.Sections)
	}
	if corp, ok := cat.PresetByID("corp-gw"); !ok || string(corp.IconSVG) != validSVG {
		t.Fatalf("custom plugin preset did not load valid local SVG: ok=%v len=%d", ok, len(corp.IconSVG))
	}
	if openai, ok := cat.PresetByID("openai"); !ok || openai.DefaultModel != "gpt-5-profile" || openai.Endpoint != "https://api.openai.com/v1" || len(openai.IconSVG) == 0 {
		t.Fatalf("profile override failed or lost inherited built-in fields: %+v ok=%v", openai, ok)
	}
	if _, ok := cat.PresetByID("xai"); ok {
		t.Fatal("hidden built-in preset xai was not excluded")
	}
	if _, ok := cat.PresetByID("traversal-gw"); ok {
		t.Fatal("path-traversal icon preset was admitted")
	}
	if _, ok := cat.PresetByID("nulicon-gw"); ok {
		t.Fatal("NUL-byte icon preset was admitted")
	}
	if _, ok := cat.PresetByID("badwire-gw"); ok {
		t.Fatal("unsupported wireApi preset was admitted")
	}
	for _, fallbackID := range []string{"escape-gw", "huge-gw", "symdir-gw"} {
		preset, ok := cat.PresetByID(fallbackID)
		if !ok {
			t.Fatalf("preset %q unexpectedly dropped instead of falling back icon", fallbackID)
		}
		if string(preset.IconSVG) != string(cat.FallbackIconSVG) || strings.Contains(string(preset.IconSVG), "#ff0000") {
			t.Fatalf("preset %q did not safely fall back to built-in custom SVG", fallbackID)
		}
	}
	joined := strings.Join(cat.Warnings, "\n")
	for _, wantWarn := range []string{"escape-gw", "huge-gw", "traversal-gw", "badwire-gw", "02-malformed", "unsupported provider catalog version 99", "symdir-gw"} {
		if !strings.Contains(joined, wantWarn) {
			t.Errorf("expected warning mentioning %q, got:\n%s", wantWarn, joined)
		}
	}
}
