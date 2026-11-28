package app

import (
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/connection"
	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

func providerUITestApp(t *testing.T, pool pi.ProductProviderList) *appModel {
	t.Helper()
	a, _ := workspaceTestApp(t)
	a.demo = false
	a.runtimeStatus = runtimeStateReady
	a.connectionGeneration = 1
	a.pi = stubProductHost(t, a, pool)
	a.applyProductProviders(pool)
	a.settingsOpen = true
	a.settingsTab = settingsTabConnection
	a.providers.tab = providerTabAccounts
	return a
}

func TestProviderSettingsUnifiedTwoTabsZeroAccountV2ImportAndOffline(t *testing.T) {
	msgs := locales.In("en")

	t.Run("offline-keeps-unified-plan-a-shell", func(t *testing.T) {
		offline, _ := workspaceTestApp(t)
		offline.windowWidth = 1040
		offline.openSettingsTab(settingsTabConnection, settingsSourceRail)
		tr := ui.NewTester(offline.view, 1040, 720)
		tr.SetLocale("en")
		tr.Frame()
		for _, want := range []string{
			msgs.ProviderTabAccounts(0),
			msgs.ProviderTabRouting(),
			msgs.ProviderRuntimeUnavailable(),
			"Local & custom",
			"Popular providers",
			"Ollama", "LM Studio", "Custom API",
			"OpenAI", "Google Gemini", "DeepSeek", "xAI", "Kimi", "Zhipu GLM", "MiniMax", "Volcengine Ark", "Qwen", "OpenRouter",
		} {
			if !tr.HasText(want) {
				t.Fatalf("offline unified Connection page missing %q: %v", want, tr.Texts())
			}
		}
		for _, retired := range []string{msgs.ConnectionDirectorySection(), msgs.WorkFolderChoose(), msgs.ProviderNoAccounts(), msgs.ProviderQuickStart()} {
			if tr.HasText(retired) {
				t.Fatalf("offline Connection page unexpectedly rendered retired UI %q: %v", retired, tr.Texts())
			}
		}
	})

	t.Run("zero-account-quick-start-preserves-key-and-narrow-footer", func(t *testing.T) {
		empty := providerUITestApp(t, pi.ProductProviderList{Policy: pi.ProductRoutingBalanced})
		empty.windowWidth = 1040
		tr := ui.NewTester(empty.view, 1040, 720)
		tr.SetLocale("en")
		tr.SetFocused(true)
		tr.Frame()
		for _, want := range []string{
			msgs.ProviderTabAccounts(0),
			msgs.ProviderTabRouting(),
			msgs.ProviderQuickStartHint(),
			"Local & custom",
			"Popular providers",
			"Ollama", "LM Studio", "Custom API",
			"OpenAI", "Google Gemini", "DeepSeek", "xAI", "Kimi", "Zhipu GLM", "MiniMax", "Volcengine Ark", "Qwen", "OpenRouter",
		} {
			if !tr.HasText(want) {
				t.Fatalf("0-account DS2 directory missing %q: %v", want, tr.Texts())
			}
		}
		if tr.HasText(msgs.ConnectionDirectorySection()) || tr.HasText(msgs.WorkFolderChoose()) {
			t.Fatalf("0-account page rendered retired legacy card form: %v", tr.Texts())
		}

		const syntheticKey = "sk-quickstart-keep-999"
		for _, tc := range []struct {
			click    string
			wantURL  string
			wantWire pi.WireAPI
		}{
			{"DeepSeek", "https://api.deepseek.com/v1", pi.OpenAICompletions},
			{"Ollama", "http://127.0.0.1:11434/v1", pi.OpenAICompletions},
			{"OpenAI", "https://api.openai.com/v1", pi.OpenAIResponses},
			{"Custom API", "", pi.OpenAICompletions},
		} {
			if err := tr.Click(tc.click); err != nil {
				t.Fatalf("click directory preset %q: %v", tc.click, err)
			}
			tr.Frame()
			if !empty.providers.adding || empty.providers.editor == nil {
				t.Fatalf("preset %q did not open provider config modal", tc.click)
			}
			if empty.providers.editor.BaseURL != tc.wantURL || empty.providers.editor.WireAPI != tc.wantWire ||
				empty.providers.editor.DefaultModel != "" || len(empty.providers.editor.Models) != 0 {
				t.Fatalf("preset %q set url=%q wire=%q defaultModel=%q models=%v, want %q/%q empty default",
					tc.click, empty.providers.editor.BaseURL, empty.providers.editor.WireAPI,
					empty.providers.editor.DefaultModel, empty.providers.editor.Models, tc.wantURL, tc.wantWire)
			}
			for _, hardcoded := range []string{"deepseek-chat", "gpt-5", "qwen3:8b"} {
				if tr.HasText(hardcoded) {
					t.Fatalf("preset %q modal leaked removed hardcoded model %q: %v", tc.click, hardcoded, tr.Texts())
				}
			}
			if !tr.HasText(msgs.ProviderFetchModels()) || !tr.HasText(msgs.ProviderFetchModelsHint()) || tr.HasText(msgs.ProviderTestSync()) {
				t.Fatalf("preset %q before-fetch controls mismatch: %v", tc.click, tr.Texts())
			}
			if tc.click == "OpenAI" {
				empty.providers.editor.APIKey = syntheticKey
			}
			tr.Key(0, ui.KeyEscape)
			tr.Frame()
			if empty.providers.adding {
				t.Fatalf("Escape did not dismiss modal opened from %q", tc.click)
			}
		}
		if err := tr.Click("OpenAI"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if empty.providers.editor == nil || empty.providers.editor.APIKey != syntheticKey {
			t.Fatalf("reopening OpenAI preset modal lost typed API key: got %+v", empty.providers.editor)
		}

		empty.windowWidth = 720
		tr.SetSize(720, 640)
		tr.SetLocale("zh-Hans")
		tr.SetDark(true)
		tr.Frame()
		zh := locales.In("zh-Hans")
		for _, label := range []string{zh.CreateBotCancel(), zh.ProviderSave(), zh.ProviderSaveApply()} {
			r, ok := tr.Find(label)
			if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 720 || r.Y+r.H > 640 {
				t.Fatalf("narrow 720x640 0-account modal footer control %q clipped/missing: rect=%+v ok=%v", label, r, ok)
			}
		}
	})

	t.Run("legacy-v2-previous-connection-prefill", func(t *testing.T) {
		cfg := connectionConfig{
			API:              connectionAPIResponses,
			Endpoint:         "https://legacy.example.invalid/v1",
			APIKey:           "sk-never-prefill-secret",
			Model:            "gpt-4o",
			ContextTokens:    65536,
			OutputTokens:     4096,
			WorkingDirectory: t.TempDir(),
			MaxActiveTasks:   2,
		}
		a := providerUITestApp(t, pi.ProductProviderList{Policy: pi.ProductRoutingBalanced})
		a.connectionSaved = &cfg
		a.windowWidth = 1040
		tr := ui.NewTester(a.view, 1040, 720)
		tr.SetLocale("en")
		tr.Frame()
		if !tr.HasText(msgs.ProviderImportHint()) || !tr.HasText(msgs.ProviderV2ImportTitle()) || !tr.HasText(msgs.ProviderV2ImportAction()) {
			t.Fatalf("legacy v2 directory missing import banner: %v", tr.Texts())
		}
		if err := tr.Click(msgs.ProviderV2ImportTitle()); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		e := a.providers.first
		if !a.providers.adding || e == nil || e.preset != -1 || e.BaseURL != cfg.Endpoint || e.WireAPI != pi.OpenAIResponses ||
			e.DefaultModel != cfg.Model || e.context != "65536" || e.output != "4096" || e.APIKey != "" {
			t.Fatalf("legacy v2 prefill mismatch or leaked secret: adding=%v editor=%+v", a.providers.adding, e)
		}
	})
}

func TestProviderSettingsMasterDetailRT1PoolPriorityFallbackAndPrimaryCTA(t *testing.T) {
	msgs := locales.In("en")
	rev1, rev2 := uint64(1), uint64(2)
	yes, no := true, false
	pool := pi.ProductProviderList{
		SavedRevision:     &rev1,
		EffectiveRevision: &rev1,
		Policy:            pi.ProductRoutingQuality,
		Accounts: []pi.ProductProviderAccountView{
			{
				ProductProviderAccount: pi.ProductProviderAccount{
					ProviderID:    "prov-internal-alpha",
					Name:          "Studio gateway",
					WireAPI:       pi.OpenAICompletions,
					BaseURL:       "https://studio.example.invalid/v1",
					DefaultModel:  "gpt-4o",
					Models:        []string{"gpt-4o", "gpt-5.4-mini", "gpt-5.4-pro", "text-embedding-3-small"},
					ContextTokens: 131072,
					OutputTokens:  8192,
					Enabled:       true,
				},
				CredentialStatus: "ok",
				HealthSummary:    "healthy",
			},
			{
				ProductProviderAccount: pi.ProductProviderAccount{
					ProviderID:    "prov-internal-beta",
					Name:          "Backup gateway",
					WireAPI:       pi.OpenAICompletions,
					BaseURL:       "http://127.0.0.1:11434/v1",
					DefaultModel:  "qwen3:8b",
					ContextTokens: 131072,
					OutputTokens:  8192,
					Enabled:       false,
					FallbackOnly:  &yes,
				},
				CredentialStatus: "missing",
			},
		},
		ResolvedRoles: pi.ProductResolvedRoles{
			Dialogue:  &pi.ProductModelRoute{ProviderID: "prov-internal-alpha", ProviderName: "Studio gateway", ResolvedModel: "gpt-4o", ThinkingLevel: "off"},
			Coding:    &pi.ProductModelRoute{ProviderID: "prov-internal-alpha", ProviderName: "Studio gateway", ResolvedModel: "gpt-5.4-mini", ThinkingLevel: "medium"},
			Reasoning: &pi.ProductModelRoute{ProviderID: "prov-internal-alpha", ProviderName: "Studio gateway", ResolvedModel: "gpt-5.4-mini", ThinkingLevel: "medium"},
			Utility:   &pi.ProductModelRoute{ProviderID: "prov-internal-alpha", ProviderName: "Studio gateway", ResolvedModel: "gpt-4o", ThinkingLevel: "off"},
		},
	}

	a := providerUITestApp(t, pool)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()

	for _, txt := range tr.Texts() {
		if strings.Contains(txt, "prov-internal-alpha") || strings.Contains(txt, "prov-internal-beta") {
			t.Fatalf("raw internal providerId leaked into UI text: %q", txt)
		}
	}
	for _, want := range []string{msgs.ProviderConfiguredTitle(), "#1", "#2", msgs.ProviderFallbackTag(), msgs.ProviderAllApplied(), msgs.ProviderFoldSummary(10)} {
		if !tr.HasText(want) {
			t.Fatalf("Providers directory missing %q: %v", want, tr.Texts())
		}
	}
	if _, okUp := tr.Find(msgs.ProviderMoveUp()); !okUp {
		t.Fatal("Priority Move up control missing")
	}
	if _, okDown := tr.Find(msgs.ProviderMoveDown()); !okDown {
		t.Fatal("Priority Move down control missing")
	}

	if err := tr.Click(msgs.ProviderFoldSummary(10)); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("OpenAI") || !tr.HasText("OpenRouter") {
		t.Fatalf("expanding folded popular providers did not reveal cloud vendors: %v", tr.Texts())
	}

	if err := tr.Click("Studio gateway"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.providers.adding || a.providers.selectedID != "prov-internal-alpha" {
		t.Fatalf("clicking configured account did not open modal: adding=%v selected=%q", a.providers.adding, a.providers.selectedID)
	}

	alphaDraft := a.providers.drafts["prov-internal-alpha"]
	if alphaDraft == nil {
		t.Fatal("selected account draft missing")
	}
	alphaDraft.discovered = []pi.ProductDiscoveredModel{
		{ID: "gpt-4o", CanonicalID: "openai/gpt-4o", IsChat: &yes, ToolCall: &yes, Recommended: true, ContextTokens: 128000, OutputTokens: 16384},
		{ID: "gpt-5.4-mini", CanonicalID: "openai/gpt-5.4-mini", IsChat: &yes, ToolCall: &yes, Reasoning: true, Recommended: true},
		{ID: "gpt-5.4-pro", CanonicalID: "openai/gpt-5.4-pro", IsChat: &yes, ToolCall: &yes, PriceProtected: true},
		{ID: "text-embedding-3-small", IsChat: &no, ToolCall: &no},
	}
	unknownHints := providerModelHints(msgs, pi.ProductDiscoveredModel{ID: "custom-model"})
	if !strings.Contains(unknownHints, msgs.ProviderChatUnknown()) || !strings.Contains(unknownHints, msgs.ProviderToolsUnknown()) {
		t.Fatalf("null capability metadata did not render unknown hints: %q", unknownHints)
	}
	embedHints := providerModelHints(msgs, alphaDraft.discovered[3])
	if !strings.Contains(embedHints, msgs.ProviderNotChat()) || !strings.Contains(embedHints, msgs.ProviderNoTools()) {
		t.Fatalf("non-chat model hints mismatch: %q", embedHints)
	}
	proHints := providerModelHints(msgs, alphaDraft.discovered[2])
	if !strings.Contains(proHints, msgs.ProviderPriceProtected()) {
		t.Fatalf("price-protected model hint missing: %q", proHints)
	}

	untouched, ok := alphaDraft.input(true)
	if !ok || untouched.Account.EnabledModels != nil || untouched.Account.FallbackOnly != nil {
		t.Fatalf("untouched editor must omit EnabledModels and FallbackOnly: ok=%v acc=%+v", ok, untouched.Account)
	}

	tr.Frame()
	if err := tr.Click(msgs.ProviderToggleModel("gpt-4o")); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click(msgs.ProviderToggleModel("gpt-5.4-mini")); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	emptyParams, ok := alphaDraft.input(true)
	if !ok || emptyParams.Account.EnabledModels == nil || len(*emptyParams.Account.EnabledModels) != 0 {
		t.Fatalf("deselecting all models must preserve explicit empty EnabledModels slice: ok=%v models=%v", ok, emptyParams.Account.EnabledModels)
	}

	if err := tr.Click(msgs.ProviderUseAsBackup()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	fallbackParams, ok := alphaDraft.input(true)
	if !ok || fallbackParams.Account.FallbackOnly == nil || !*fallbackParams.Account.FallbackOnly {
		t.Fatalf("checking Fallback only did not set FallbackOnly=true: ok=%v val=%v", ok, fallbackParams.Account.FallbackOnly)
	}

	if err := tr.Click(msgs.ProviderAdvanced()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !alphaDraft.advanced || !tr.HasText(msgs.ProviderAdditionalModels()) || !tr.HasText(msgs.ConnectionTokenLimits()) {
		t.Fatalf("Advanced disclosure did not reveal additional models and token limits: %v", tr.Texts())
	}

	alphaDraft.BaseURL = "https://changed.example.invalid/v1"
	if _, ok := alphaDraft.input(false); ok || alphaDraft.fields["APIKey"] != connection.ValidationRequired {
		t.Fatalf("changing endpoint without API key must require APIKey: ok=%v fields=%+v", ok, alphaDraft.fields)
	}
	alphaDraft.Name = "Studio edited draft"
	alphaDraft.BaseURL = "https://studio.example.invalid/v1"

	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if err := tr.Click("Backup gateway"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.providers.selectedID != "prov-internal-beta" {
		t.Fatalf("clicking second account did not switch selection: got %q", a.providers.selectedID)
	}
	a.providers.drafts["prov-internal-beta"].Name = "Backup edited draft"
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if err := tr.Click("Studio gateway"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.providers.drafts["prov-internal-alpha"].Name != "Studio edited draft" ||
		a.providers.drafts["prov-internal-beta"].Name != "Backup edited draft" {
		t.Fatalf("switching accounts did not isolate drafts: alpha=%q beta=%q",
			a.providers.drafts["prov-internal-alpha"].Name, a.providers.drafts["prov-internal-beta"].Name)
	}

	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	a.pi.productProviders.SavedRevision = &rev2
	tr.Frame()
	if !tr.HasText(msgs.ProviderApplyChanges()) {
		t.Fatalf("pending state missing Apply provider changes CTA: %v", tr.Texts())
	}
	pendingImg := cloneRGBA(tr.Image())
	applyRect, okApply := tr.Find(msgs.ProviderApplyChanges())
	if !okApply {
		t.Fatal("Apply provider changes rect missing")
	}
	applyFill := pendingImg.RGBAAt(int(applyRect.X+4), int(applyRect.Y+4))

	a.addProvider()
	tr.Frame()
	modalImg := cloneRGBA(tr.Image())
	demotedApplyRect, _ := tr.Find(msgs.ProviderApplyChanges())
	if modalImg.RGBAAt(int(demotedApplyRect.X+4), int(demotedApplyRect.Y+4)) == applyFill {
		t.Fatal("opening Add provider modal while pending did not demote background Apply CTA")
	}
	a.dismissProviderDialog()
	a.selectProvider(pool.Accounts[0])
	a.windowWidth = 720
	tr.SetSize(720, 640)
	tr.SetLocale("zh-Hans")
	tr.SetDark(true)
	tr.Frame()
	zh := locales.In("zh-Hans")
	for _, label := range []string{zh.ProviderDeleteAccount("Studio gateway"), zh.CreateBotCancel(), zh.ProviderSave(), zh.ProviderSaveApply()} {
		r, ok := tr.Find(label)
		if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 720 || r.Y+r.H > 640 {
			t.Fatalf("narrow 720x640 account modal footer control %q clipped/missing: rect=%+v ok=%v", label, r, ok)
		}
	}
}

func TestProviderAddModalPresetsPreserveKeyNarrowFooterAndEscapeFocus(t *testing.T) {
	a := providerUITestApp(t, visualGuardSeededPool())
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.SetFocused(true)
	tr.Frame()
	msgs := locales.In("en")

	if err := tr.Click(msgs.ProviderAddShort()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.providers.adding || a.providers.editor == nil {
		t.Fatal("Add provider frosted modal did not open")
	}

	const syntheticKey = "sk-synthetic-preset-keep-123456"
	a.providers.editor.APIKey = syntheticKey
	if redacted := a.redactConnectionText("probe failed for " + syntheticKey); strings.Contains(redacted, syntheticKey) || !strings.Contains(redacted, "[redacted]") {
		t.Fatalf("redactConnectionText did not redact provider modal key: %q", redacted)
	}

	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.providers.adding {
		t.Fatal("Escape did not dismiss Add provider modal at 1040x720")
	}
	if !tr.Focused(msgs.ProviderAddShort()) {
		t.Fatal("Escape dismissal did not return focus to opening Add button at 1040x720")
	}

	if err := tr.Click(msgs.ProviderAddShort()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.providers.editor == nil || a.providers.editor.APIKey != syntheticKey {
		t.Fatalf("reopening Add provider modal lost unsaved synthetic key: %+v", a.providers.editor)
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()

	a.windowWidth = 720
	tr.SetSize(720, 640)
	tr.SetLocale("zh-Hans")
	tr.SetDark(true)
	tr.Frame()
	zh := locales.In("zh-Hans")
	if err := tr.Click(zh.ProviderAddShort()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	for _, footerLabel := range []string{zh.CreateBotCancel(), zh.ProviderSave(), zh.ProviderSaveApply()} {
		r, ok := tr.Find(footerLabel)
		if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 720 || r.Y+r.H > 640 {
			t.Fatalf("narrow 720x640 modal footer control %q clipped or missing: rect=%+v ok=%v", footerLabel, r, ok)
		}
	}

	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.providers.adding {
		t.Fatal("Escape did not dismiss Add provider modal at 720x640")
	}
	if !tr.Focused(zh.ProviderAddShort()) {
		t.Fatal("Escape dismissal did not return focus to opening Add button at 720x640")
	}
}

func TestProviderRT2RoutingPinsAttributionFailoverDanglingAndHealth(t *testing.T) {
	msgs := locales.In("en")
	rev1, rev2 := uint64(1), uint64(2)
	pastRetry := time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339Nano)
	futureRetry := time.Now().Add(45 * time.Second).UTC().Format(time.RFC3339Nano)
	yes, no := true, false
	enabledOnly := []string{"gpt-4o"}

	pool := pi.ProductProviderList{
		SavedRevision:     &rev2,
		EffectiveRevision: &rev1,
		Policy:            pi.ProductRoutingQuality,
		Accounts: []pi.ProductProviderAccountView{
			{
				ProductProviderAccount: pi.ProductProviderAccount{
					ProviderID:    "prov-studio",
					Name:          "Studio gateway",
					WireAPI:       pi.OpenAIResponses,
					BaseURL:       "https://api.openai.com/v1",
					DefaultModel:  "gpt-4o",
					Models:        []string{"gpt-4o", "gpt-5.4-mini", "text-embedding-3-small"},
					ContextTokens: 131072,
					OutputTokens:  8192,
					Enabled:       true,
				},
				CredentialStatus: "ok",
				HealthSummary:    "degraded",
				RouteHealth: []pi.ProductRouteHealth{{
					Scope:               "model",
					WireModelID:         "gpt-4o",
					State:               "cooldown",
					Reason:              "rate_limited",
					ConsecutiveFailures: 2,
					RetryAt:             pastRetry,
				}},
			},
			{
				ProductProviderAccount: pi.ProductProviderAccount{
					ProviderID:    "prov-backup",
					Name:          "Backup gateway",
					WireAPI:       pi.OpenAIResponses,
					BaseURL:       "https://api.openai.com/v1",
					DefaultModel:  "gpt-4o",
					Models:        []string{"gpt-4o", "gpt-5.4-mini"},
					EnabledModels: &enabledOnly,
					ContextTokens: 131072,
					OutputTokens:  8192,
					Enabled:       true,
					FallbackOnly:  &yes,
				},
				CredentialStatus: "ok",
				HealthSummary:    "healthy",
			},
		},
		Overrides: pi.ProductRoutingOverrides{
			"dialogue":  {Mode: "model", CanonicalModelID: "openai/gpt-4o"},
			"coding":    {Mode: "route", ProviderID: "deleted-prov", Model: "gpt-5.4", EndpointFingerprint: "sha256:stale"},
			"reasoning": {Mode: "route", ProviderID: "prov-studio", Model: "gpt-4o", EndpointFingerprint: "sha256:fp1"},
		},
		EffectiveOverrides: pi.ProductRoutingOverrides{
			"dialogue":  {Mode: "model", CanonicalModelID: "openai/gpt-4o"},
			"coding":    {Mode: "route", ProviderID: "deleted-prov", Model: "gpt-5.4", EndpointFingerprint: "sha256:stale"},
			"reasoning": {Mode: "route", ProviderID: "prov-studio", Model: "gpt-4o", EndpointFingerprint: "sha256:fp1"},
		},
		ResolvedRoles: pi.ProductResolvedRoles{
			Dialogue: &pi.ProductModelRoute{
				ProviderID:       "prov-backup",
				ProviderName:     "Backup gateway",
				ResolvedModel:    "gpt-4o",
				CanonicalModelID: "openai/gpt-4o",
				ThinkingLevel:    "off",
				Source:           "pinned_model",
				Failover:         "same_model_cooldown",
			},
			Utility: &pi.ProductModelRoute{
				ProviderID:    "pending-removed",
				ProviderName:  "Former studio",
				ResolvedModel: "gpt-5.4-mini",
				ThinkingLevel: "low",
				Source:        "auto",
				Failover:      "model_cooldown",
			},
		},
		RoleStatus: map[pi.ProductModelRole]pi.ProductRoleStatus{
			"dialogue":  {Status: "ready", Mode: "pinned_model", Failover: "same_model_cooldown"},
			"coding":    {Status: "pin_unavailable", Mode: "pinned_route", UnavailableReason: "provider_deleted"},
			"reasoning": {Status: "cooldown", Mode: "pinned_route", UnavailableReason: "rate_limited", RetryAt: pastRetry},
			"utility":   {Status: "ready", Mode: "auto", Failover: "model_cooldown"},
		},
	}

	a := providerUITestApp(t, pool)
	if a.providers.healthTimer != nil {
		t.Fatal("expired RetryAt <= now must not schedule a polling health timer")
	}

	a.selectProvider(pool.Accounts[0])
	a.providers.drafts["prov-studio"].discovered = []pi.ProductDiscoveredModel{
		{ID: "gpt-4o", CanonicalID: "openai/gpt-4o", IsChat: &yes, ToolCall: &yes, Recommended: true},
		{ID: "gpt-5.4-mini", CanonicalID: "openai/gpt-5.4-mini", IsChat: &yes, ToolCall: &yes, Recommended: true},
		{ID: "text-embedding-3-small", IsChat: &no, ToolCall: &no},
	}
	options := a.providerPinOptions(msgs, pool, providerRoleRows(msgs, pool.ResolvedRoles))
	for _, opt := range options {
		if opt.pin.EndpointFingerprint != "" {
			t.Fatalf("pin option must omit EndpointFingerprint: %+v", opt.pin)
		}
		if strings.Contains(opt.Value, "text-embedding-3-small") {
			t.Fatalf("non-chat model leaked into pin picker options: %+v", options)
		}
		if opt.pin.Mode == "route" && opt.pin.ProviderID == "prov-backup" && opt.pin.Model == "gpt-5.4-mini" {
			t.Fatalf("model excluded by EnabledModels leaked into pin picker options: %+v", options)
		}
	}

	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	a.providers.detailScroll.Y = 300
	tr.Frame()
	for _, want := range []string{msgs.ProviderDegraded(), msgs.ProviderRouteHealth(), msgs.CapRefreshStatus(), msgs.ProviderCooldown(), msgs.ProviderFailureCount(2)} {
		if !tr.HasText(want) {
			t.Fatalf("degraded account health section missing %q: %v", want, tr.Texts())
		}
	}

	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if err := tr.Click(msgs.ProviderTabRouting()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	for _, want := range []string{
		msgs.ProviderRouting(),
		msgs.ProviderActiveRoutingNote(),
		msgs.ProviderPinnedModel(),
		msgs.ProviderSameModelFailover(),
		msgs.ProviderPinnedRoute(),
		msgs.ProviderPinDeleted(),
		msgs.ProviderPinnedUnavailable() + " · gpt-5.4",
		msgs.ProviderRateLimited(),
		msgs.ProviderFallbackModel(),
		"Former studio · gpt-5.4-mini",
	} {
		if !tr.HasText(want) {
			t.Fatalf("Routing tab missing RT-2 badge or label %q: %v", want, tr.Texts())
		}
	}

	pool.Accounts[0].RouteHealth[0].RetryAt = futureRetry
	a.applyProductProviders(pool)
	if a.providers.healthTimer == nil {
		t.Fatal("future RetryAt did not schedule one health refresh timer")
	}
	a.beginShutdown()
	if a.providers.healthTimer != nil {
		t.Fatal("beginShutdown did not stop and clear provider healthTimer")
	}
}

func TestProviderCredentialAndRouteUnavailableNoticesAndV3Startup(t *testing.T) {
	storeDir := t.TempDir()
	storePath := filepath.Join(storeDir, "contacts.json")
	cfgPath := connectionConfigPath(storePath)
	rawAccounts := json.RawMessage(`[{"providerId":"p1","name":"Studio","wireApi":"openai-completions","baseUrl":"https://api.example.com/v1","defaultModel":"gpt-4o","models":["gpt-4o"],"contextTokens":131072,"outputTokens":8192,"enabled":true}]`)
	v3Bytes, err := json.Marshal(connection.PrivateConfig{
		Version:       3,
		Sources:       map[string]connection.CredentialSource{"p1": {API: connection.APIChatCompletions, Endpoint: "https://api.example.com/v1", APIKey: "sk-v3-startup"}},
		Accounts:      rawAccounts,
		PublicationID: "pub-startup-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, v3Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	startup := readConnectionStartup(storePath)
	if startup.LoadErr != nil || startup.Config != nil {
		t.Fatalf("readConnectionStartup mishandled provider-only v3 config: loadErr=%v config=%+v", startup.LoadErr, startup.Config)
	}

	retryStamp := time.Now().Add(40 * time.Second).UTC().Format(time.RFC3339Nano)
	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			msgs := locales.In(locale)
			rev1 := uint64(1)
			pool := pi.ProductProviderList{
				SavedRevision:     &rev1,
				EffectiveRevision: &rev1,
				Policy:            pi.ProductRoutingBalanced,
				Accounts: []pi.ProductProviderAccountView{{
					ProductProviderAccount: pi.ProductProviderAccount{
						ProviderID: "p1", Name: "Studio", WireAPI: pi.OpenAICompletions,
						BaseURL: "https://api.example.com/v1", DefaultModel: "gpt-4o", Enabled: true,
					},
					CredentialStatus: "missing",
				}},
			}
			a := providerUITestApp(t, pool)
			a.closeSettings()
			a.windowWidth = 1040
			ct := a.selectedContact()
			ct.Draft = "Keep this draft preserved"
			ct.DraftQuote = &pi.ProductQuote{MessageID: "m-1", Sender: "bot", AuthorName: ct.Name, Text: "Quoted snippet"}
			ct.setSendError("The Provider credential is unavailable.", sendErrorCredentialUnavailable, "")

			tr := ui.NewTester(a.view, 1040, 720)
			tr.SetLocale(locale)
			tr.Frame()
			if !tr.HasText(msgs.ProviderSendRejected()) || ct.Draft != "Keep this draft preserved" || !tr.HasText("Quoted snippet") {
				t.Fatalf("%s rejected credential send notice or preserved draft/quote missing: draft=%q texts=%v", locale, ct.Draft, tr.Texts())
			}
			if tr.HasText(msgs.ProviderCredentialRecovery()) {
				t.Fatalf("%s duplicate top credential recovery banner rendered alongside composer notice: %v", locale, tr.Texts())
			}
			if _, ok := tr.Find(msgs.ConnectionRecoveryOpenSettings()); !ok {
				t.Fatalf("%s rejected send missing Open connection settings action: %v", locale, tr.Texts())
			}

			pool.Accounts[0].CredentialStatus = "ok"
			pool.RoleStatus = map[pi.ProductModelRole]pi.ProductRoleStatus{
				"dialogue": {Status: "cooldown", UnavailableReason: "rate_limited", RetryAt: retryStamp},
			}
			a.applyProductProviders(pool)
			if a.runtimeErrKind != runtimeNoticeRouteUnavailable {
				t.Fatalf("%s cooldown dialogue role set runtimeErrKind=%q, want %q", locale, a.runtimeErrKind, runtimeNoticeRouteUnavailable)
			}
			if !ct.applyProviderSendRejection(pi.ProductRouteAdmission{Code: pi.ProductRouteUnavailable, RetryAt: retryStamp}) {
				t.Fatal("applyProviderSendRejection did not recognize route_unavailable")
			}
			tr.Frame()
			if ct.SendState != sendIdle || ct.Draft != "Keep this draft preserved" || ct.DraftQuote == nil || !tr.HasText("Quoted snippet") {
				t.Fatalf("%s route_unavailable did not retain idle state, draft and quote", locale)
			}
			if tr.HasText(msgs.ProviderSendRejected()) || tr.HasText(msgs.ProviderRoutesNeedAttention()) {
				t.Fatalf("%s route_unavailable showed wrong/duplicate banner: %v", locale, tr.Texts())
			}
			if _, ok := tr.Find(msgs.ConnectionRecoveryOpenSettings()); !ok {
				t.Fatalf("%s route_unavailable missing Open connection settings button", locale)
			}

			ct.clearSendError()
			ct.activity.open = true
			ct.activity.activityID = "act-paused-1"
			ct.activity.actionBlocker = pi.ProductRouteAdmission{Code: pi.ProductRouteUnavailable, RetryAt: retryStamp}
			ct.activity.activity = &pi.ProductActivity{
				ActivityID: "act-paused-1",
				OwnerBotID: ct.ID,
				Goal:       "Check the paper settings",
				State:      "needsAttention",
				Questions: []pi.ProductQuestion{{
					QuestionID:      "q-paused-1",
					Text:            "Confirm paper size",
					State:           "answered",
					Delivery:        "saved",
					DeliveryBlocker: pi.ProductRouteUnavailable,
					Answer:          &pi.ProductText{Text: "Use A4 for the draft."},
				}},
			}
			tr.Frame()
			if !tr.HasText("Use A4 for the draft.") || !tr.HasText(msgs.ProviderAnswerRoutePaused()) {
				t.Fatalf("%s route-paused Activity answer notice missing: %v", locale, tr.Texts())
			}
			if tr.HasText(msgs.ProviderRoutesNeedAttention()) {
				t.Fatalf("%s duplicate top route recovery banner rendered alongside paused Activity answer: %v", locale, tr.Texts())
			}
			if !slices.Contains(tr.Texts(), msgs.ConnectionRecoveryOpenSettings()) {
				t.Fatalf("%s paused Activity answer missing Open connection settings link", locale)
			}

			ct.activity.activity.Questions[0].DeliveryBlocker = pi.ProductCredentialUnavailable
			ct.activity.actionBlocker = pi.ProductRouteAdmission{}
			tr.Frame()
			if !tr.HasText(msgs.ProviderAnswerPaused()) {
				t.Fatalf("%s credential-paused Activity answer notice missing: %v", locale, tr.Texts())
			}
		})
	}
}

func TestProviderRoutingHelpersHealthNotesAndThemeSwitch(t *testing.T) {
	msgs := locales.In("en")

	if got := providerAdmission(pi.ProductReceipt{Result: json.RawMessage(`{"code":"route_unavailable","retryAt":"2026-11-26T00:00:00Z"}`)}, &pi.RemoteError{Code: "other"}); got.Code != pi.ProductRouteUnavailable || got.RetryAt == "" {
		t.Fatalf("providerAdmission receipt = %+v", got)
	}
	if got := providerAdmission(pi.ProductReceipt{}, &pi.RemoteError{Code: "credential_unavailable"}); got.Code != pi.ProductCredentialUnavailable {
		t.Fatalf("providerAdmission remote fallback = %+v", got)
	}
	if !providerDeliveryBlocked(pi.ProductCredentialUnavailable) || !providerDeliveryBlocked(pi.ProductRouteUnavailable) || providerDeliveryBlocked("other") {
		t.Fatal("providerDeliveryBlocked returned unexpected result")
	}
	if got := providerTokenBadge(131072); got != "128K" {
		t.Fatalf("providerTokenBadge(131072) = %q, want 128K", got)
	}
	if got := providerTokenBadge(2 * 1024 * 1024); got != "2.0M" {
		t.Fatalf("providerTokenBadge(2M) = %q, want 2.0M", got)
	}
	if got := providerTokenBadge(512); got != "512" {
		t.Fatalf("providerTokenBadge(512) = %q, want 512", got)
	}
	if got := providerTokenBadge(0); got != "" {
		t.Fatalf("providerTokenBadge(0) = %q, want empty", got)
	}

	ed := newProviderDraft()
	ed.Name = "Loopback Studio"
	ed.BaseURL = "http://127.0.0.1:11434/v1"
	ed.DefaultModel = "qwen3:8b"
	ed.noKey = true
	params, ok := ed.input(false)
	if !ok || params.APIKey != "xuhu-loopback" {
		t.Fatalf("loopback noKey editor input = ok:%v key:%q fields:%+v", ok, params.APIKey, ed.fields)
	}
	ed.Name = strings.Repeat("a", 257)
	ed.output = "131070"
	if _, ok := ed.input(false); ok || ed.fields["Name"] != "name-length" || ed.fields["OutputTokens"] != connection.ValidationContextGap {
		t.Fatalf("invalid Name/OutputTokens not caught: ok=%v fields=%+v", ok, ed.fields)
	}

	a := providerUITestApp(t, pi.ProductProviderList{})
	a.windowWidth = 1040
	t.Run("provider-health-omits-empty-reason-separator", func(t *testing.T) {
		account := pi.ProductProviderAccountView{
			ProductProviderAccount: pi.ProductProviderAccount{
				ProviderID: "p-health", Name: "Studio", WireAPI: pi.OpenAICompletions,
				BaseURL: "https://api.example.com/v1", DefaultModel: "gpt-4o", Enabled: true,
			},
			CredentialStatus: "ok",
			HealthSummary:    "degraded",
			RouteHealth: []pi.ProductRouteHealth{{
				WireModelID:         "gpt-4o",
				Scope:               "route",
				State:               "degraded",
				Reason:              "",
				ConsecutiveFailures: 2,
			}},
		}
		healthView := func(c *ui.Context) {
			wb := theme.ResolveWorkbenchTheme(c.Theme().Dark)
			st := theme.SettingsTheme(wb.Theme)
			c.SetTheme(&st)
			ui.Column(c).Fill().Padding(16).Background(st.Background).Children(func() {
				a.providerHealth(c, &st, msgs, account, false)
			})
		}
		trHealth := ui.NewTester(healthView, 520, 240)
		trHealth.SetLocale("en")
		trHealth.Frame()
		wantCount := msgs.ProviderFailureCount(2)
		for _, txt := range trHealth.Texts() {
			if strings.HasPrefix(txt, " · ") || strings.Contains(txt, "· "+wantCount) && strings.HasPrefix(strings.TrimSpace(txt), "·") {
				t.Fatalf("providerHealth emitted leading middle dot when Reason is empty: %q (all=%v)", txt, trHealth.Texts())
			}
		}
		if !trHealth.HasText(wantCount) {
			t.Fatalf("providerHealth missing failure count %q: %v", wantCount, trHealth.Texts())
		}
	})

	t.Run("directory-button-snaps-on-theme-switch", func(t *testing.T) {
		sampleRect := func(img *image.RGBA, r ui.Rect, dx, dy float32) [4]uint8 {
			c := img.RGBAAt(int(r.X+dx), int(r.Y+dy))
			return [4]uint8{c.R, c.G, c.B, c.A}
		}
		closeColor := func(x, y [4]uint8) bool {
			for i := range 4 {
				d := int(x[i]) - int(y[i])
				if d < 0 {
					d = -d
				}
				if d > 3 {
					return false
				}
			}
			return true
		}
		dirView := func(c *ui.Context) {
			wb := theme.ResolveWorkbenchTheme(c.Theme().Dark)
			st := theme.SettingsTheme(wb.Theme)
			c.SetTheme(&st)
			ui.Column(c).Fill().Padding(16).Background(st.Background).Children(func() {
				providerDirectoryButton(c, &st, "OpenAI", true).FillWidth().Background(st.Surface).Border(1, st.Border)
			})
		}
		refDark := ui.NewTesterWith(dirView, 360, 120, ui.TesterOptions{Dark: true})
		refDark.Frame()
		darkImg := image.NewRGBA(refDark.Image().Bounds())
		copy(darkImg.Pix, refDark.Image().Pix)

		trDir := ui.NewTesterWith(dirView, 360, 120, ui.TesterOptions{Dark: false})
		trDir.Frame()
		btnRect, ok := trDir.Find("OpenAI")
		if !ok {
			t.Fatal("missing providerDirectoryButton")
		}
		trDir.SetDark(true)
		if got, want := sampleRect(trDir.Image(), btnRect, 8, 8), sampleRect(darkImg, btnRect, 8, 8); !closeColor(got, want) {
			t.Fatalf("providerDirectoryButton did not snap on first Light->Dark frame: got=%v want=%v", got, want)
		}
	})
}

func TestProviderLiveModelsInputAutoAddAndPreferredDiscovery(t *testing.T) {
	msgs := locales.In("en")
	yes, no := true, false

	ed := newProviderDraft()
	ed.Name = "DeepSeek"
	ed.BaseURL = "https://api.deepseek.com/v1"
	ed.APIKey = "sk-live-models-test"
	ed.DefaultModel = ""
	if _, ok := ed.input(false); ok || ed.fields["Model"] != connection.ValidationRequired {
		t.Fatalf("save input with empty DefaultModel should fail Model required: ok=%v fields=%+v", ok, ed.fields)
	}
	probeParams, ok := ed.inputFor(false, false)
	if !ok || probeParams.Account.DefaultModel != "" || ed.fields["Model"] != "" {
		t.Fatalf("probe inputFor(false, false) should allow empty DefaultModel and clear Model error: ok=%v acc=%+v fields=%+v", ok, probeParams.Account, ed.fields)
	}

	a := providerUITestApp(t, pi.ProductProviderList{Policy: pi.ProductRoutingBalanced})
	a.windowWidth = 1040
	a.openProviderPreset(pi.ProductProviderList{Policy: pi.ProductRoutingBalanced}, "deepseek")
	a.providers.editor.fields = map[string]string{"Model": connection.ValidationRequired}
	a.providers.editor.modelInput = "deepseek-v4-chat"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if _, ok := tr.Find(msgs.ProviderAddModel()); !ok {
		a.providers.detailScroll.Y = 300
		tr.Frame()
	}
	if err := tr.Click(msgs.ProviderAddModel()); err != nil {
		t.Fatalf("click Add model: %v", err)
	}
	tr.Frame()
	if a.providers.editor.DefaultModel != "deepseek-v4-chat" ||
		!slices.Equal(a.providers.editor.Models, []string{"deepseek-v4-chat"}) ||
		a.providers.editor.fields["Model"] != "" {
		t.Fatalf("adding first manual model did not auto-populate DefaultModel: model=%q models=%v fields=%+v",
			a.providers.editor.DefaultModel, a.providers.editor.Models, a.providers.editor.fields)
	}
	if !tr.HasText(msgs.ProviderTestSync()) || tr.HasText(msgs.ProviderFetchModels()) {
		t.Fatalf("adding first manual model did not switch button to Test & sync: %v", tr.Texts())
	}
	a.providers.editor.modelInput = "deepseek-v4-reasoner"
	if err := tr.Click(msgs.ProviderAddModel()); err != nil {
		t.Fatalf("click Add second model: %v", err)
	}
	tr.Frame()
	if a.providers.editor.DefaultModel != "deepseek-v4-chat" || len(a.providers.editor.Models) != 2 {
		t.Fatalf("adding second model overwrote explicit DefaultModel: %q %v", a.providers.editor.DefaultModel, a.providers.editor.Models)
	}

	if got := preferredDiscoveredModel(pi.ProductProviderTestResult{
		DiscoveredModels: []pi.ProductDiscoveredModel{
			{ID: "text-embedding-3-small", IsChat: &no},
			{ID: "gpt-5.4-pro", IsChat: &yes, PriceProtected: true},
			{ID: "gpt-4o-mini", IsChat: &yes},
			{ID: "o3-pro-bad", IsChat: &yes, Recommended: true, PriceProtected: true},
			{ID: "gpt-5.4", IsChat: &yes, Recommended: true},
		},
	}); got != "gpt-5.4" {
		t.Fatalf("preferredDiscoveredModel recommended selection = %q, want gpt-5.4", got)
	}
	if got := preferredDiscoveredModel(pi.ProductProviderTestResult{
		DiscoveredModels: []pi.ProductDiscoveredModel{
			{ID: "text-embedding-3-small", IsChat: &no},
			{ID: "gpt-5.4-pro", IsChat: &yes, PriceProtected: true},
			{ID: "custom-unknown"},
			{ID: "standard-chat", IsChat: &yes},
		},
	}); got != "standard-chat" {
		t.Fatalf("preferredDiscoveredModel non-recommended chat vs custom/pro = %q, want standard-chat", got)
	}
	if got := preferredDiscoveredModel(pi.ProductProviderTestResult{
		DiscoveredModels: []pi.ProductDiscoveredModel{
			{ID: "embed-1", IsChat: &no},
			{ID: "custom-a"},
			{ID: "custom-b"},
		},
	}); got != "custom-a" {
		t.Fatalf("preferredDiscoveredModel unknown IsChat fallback = %q, want custom-a", got)
	}
	if got := preferredDiscoveredModel(pi.ProductProviderTestResult{
		DiscoveredModels: []pi.ProductDiscoveredModel{
			{ID: "embed-1", IsChat: &no},
			{ID: "gpt-5.4-pro", IsChat: &yes, PriceProtected: true},
		},
	}); got != "gpt-5.4-pro" {
		t.Fatalf("preferredDiscoveredModel price-protected last-resort = %q, want gpt-5.4-pro", got)
	}
	if got := preferredDiscoveredModel(pi.ProductProviderTestResult{
		Models: []string{"  ", "fallback-id"},
	}); got != "fallback-id" {
		t.Fatalf("preferredDiscoveredModel Models slice fallback = %q, want fallback-id", got)
	}
}
