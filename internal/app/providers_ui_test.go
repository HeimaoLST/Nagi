package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/connection"
	"github.com/HeimaoLST/Nagi/internal/pi"
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
			msgs.ProviderQuickStart(),
			msgs.ProviderSave(),
			msgs.ProviderSaveApply(),
		} {
			if !tr.HasText(want) {
				t.Fatalf("offline unified Connection page missing %q: %v", want, tr.Texts())
			}
		}
		for _, retired := range []string{msgs.ConnectionDirectorySection(), msgs.WorkFolderChoose(), msgs.ProviderNoAccounts()} {
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
			msgs.ProviderQuickStart(),
			"OpenAI", "DeepSeek", "OpenRouter", "Kimi", "Zhipu GLM", "Qwen", "Ollama", msgs.ProviderCustom(),
			msgs.ProviderSave(),
			msgs.ProviderSaveApply(),
		} {
			if !tr.HasText(want) {
				t.Fatalf("0-account Plan A page missing %q: %v", want, tr.Texts())
			}
		}
		if tr.HasText(msgs.ConnectionDirectorySection()) || tr.HasText(msgs.WorkFolderChoose()) {
			t.Fatalf("0-account page rendered retired legacy card form: %v", tr.Texts())
		}

		const syntheticKey = "sk-quickstart-keep-999"
		empty.providers.first.APIKey = syntheticKey
		for _, tc := range []struct {
			click    string
			wantURL  string
			wantWire pi.WireAPI
		}{
			{"DeepSeek", "https://api.deepseek.com/v1", pi.OpenAICompletions},
			{"Ollama", "http://127.0.0.1:11434/v1", pi.OpenAICompletions},
			{"OpenAI", "https://api.openai.com/v1", pi.OpenAIResponses},
			{msgs.ProviderCustom(), "", pi.OpenAICompletions},
		} {
			if err := tr.Click(tc.click); err != nil {
				t.Fatalf("click quick-start preset %q: %v", tc.click, err)
			}
			tr.Frame()
			if empty.providers.first.APIKey != syntheticKey {
				t.Fatalf("quick-start preset %q wiped typed API key: got %q", tc.click, empty.providers.first.APIKey)
			}
			if empty.providers.first.BaseURL != tc.wantURL || empty.providers.first.WireAPI != tc.wantWire {
				t.Fatalf("quick-start preset %q set url=%q wire=%q, want %q/%q", tc.click, empty.providers.first.BaseURL, empty.providers.first.WireAPI, tc.wantURL, tc.wantWire)
			}
		}

		empty.windowWidth = 720
		tr.SetSize(720, 640)
		tr.SetLocale("zh-Hans")
		tr.SetDark(true)
		tr.Frame()
		zh := locales.In("zh-Hans")
		for _, label := range []string{zh.ProviderSave(), zh.ProviderSaveApply()} {
			r, ok := tr.Find(label)
			if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 720 || r.Y+r.H > 640 {
				t.Fatalf("narrow 720x640 0-account footer control %q clipped/missing: rect=%+v ok=%v", label, r, ok)
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
		if !tr.HasText(msgs.ProviderImportHint()) || !tr.HasText(msgs.ProviderPrevious()) {
			t.Fatalf("legacy v2 prefill missing import hint or Previous connection row: %v", tr.Texts())
		}
		e := a.providers.first
		if e == nil || e.preset != -1 || e.BaseURL != cfg.Endpoint || e.WireAPI != pi.OpenAIResponses ||
			e.DefaultModel != cfg.Model || e.context != "65536" || e.output != "4096" || e.APIKey != "" {
			t.Fatalf("legacy v2 prefill mismatch or leaked secret: %+v", e)
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
	for _, want := range []string{"#1", "#2", msgs.ProviderFallbackTag(), msgs.ProviderAllApplied()} {
		if !tr.HasText(want) {
			t.Fatalf("Providers rail missing %q: %v", want, tr.Texts())
		}
	}
	if _, okUp := tr.Find(msgs.ProviderMoveUp()); !okUp {
		t.Fatal("Priority Move up control missing")
	}
	if _, okDown := tr.Find(msgs.ProviderMoveDown()); !okDown {
		t.Fatal("Priority Move down control missing")
	}

	listRect, okList := tr.Find("Backup gateway")
	detailRect, okDetail := tr.Find(msgs.FieldName())
	if !okList || !okDetail || detailRect.X <= listRect.X {
		t.Fatalf("wide Master-Detail did not lay out side-by-side: list=%+v(%v) detail=%+v(%v)", listRect, okList, detailRect, okDetail)
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

	advSummary, okAdv := tr.Find(msgs.ProviderProtocolChatShort() + " · 128K / 8K")
	if !okAdv {
		t.Fatalf("Advanced summary row missing: %v", tr.Texts())
	}
	tr.ClickAt(detailRect.X+16, advSummary.Y+advSummary.H/2)
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

	if err := tr.Click("Backup gateway"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.providers.selectedID != "prov-internal-beta" {
		t.Fatalf("clicking second account did not switch selection: got %q", a.providers.selectedID)
	}
	a.providers.drafts["prov-internal-beta"].Name = "Backup edited draft"
	if err := tr.Click("Studio gateway"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.providers.drafts["prov-internal-alpha"].Name != "Studio edited draft" ||
		a.providers.drafts["prov-internal-beta"].Name != "Backup edited draft" {
		t.Fatalf("switching accounts did not isolate drafts: alpha=%q beta=%q",
			a.providers.drafts["prov-internal-alpha"].Name, a.providers.drafts["prov-internal-beta"].Name)
	}

	appliedImg := cloneRGBA(tr.Image())
	saveApplyRect, okSaveApply := tr.Find(msgs.ProviderSaveApply())
	if !okSaveApply {
		t.Fatal("Save & apply button missing")
	}
	appliedBg := appliedImg.RGBAAt(int(saveApplyRect.X+4), int(saveApplyRect.Y+4))

	a.pi.productProviders.SavedRevision = &rev2
	tr.Frame()
	if !tr.HasText(msgs.ProviderApplyChanges()) {
		t.Fatalf("pending state missing Apply provider changes CTA: %v", tr.Texts())
	}
	pendingImg := cloneRGBA(tr.Image())
	applyRect, okApply := tr.Find(msgs.ProviderApplyChanges())
	saveApplyPendingRect, _ := tr.Find(msgs.ProviderSaveApply())
	if !okApply {
		t.Fatal("Apply provider changes rect missing")
	}
	applyFill := pendingImg.RGBAAt(int(applyRect.X+4), int(applyRect.Y+4))
	demotedFill := pendingImg.RGBAAt(int(saveApplyPendingRect.X+4), int(saveApplyPendingRect.Y+4))
	if applyFill == demotedFill || demotedFill == appliedBg {
		t.Fatalf("pending state did not transfer sole primary fill to Apply provider changes: applied=%+v apply=%+v demoted=%+v",
			appliedBg, applyFill, demotedFill)
	}

	a.addProvider()
	tr.Frame()
	modalImg := cloneRGBA(tr.Image())
	demotedApplyRect, _ := tr.Find(msgs.ProviderApplyChanges())
	if modalImg.RGBAAt(int(demotedApplyRect.X+4), int(demotedApplyRect.Y+4)) == applyFill {
		t.Fatal("opening Add provider modal while pending did not demote background Apply CTA")
	}
	a.dismissProviderDialog()
	tr.Frame()

	a.windowWidth = 720
	tr.SetSize(720, 640)
	tr.SetLocale("zh-Hans")
	tr.SetDark(true)
	tr.Frame()
	zh := locales.In("zh-Hans")
	narrowList, okNL := tr.Find("Backup gateway")
	narrowDetail, okND := tr.Find(zh.FieldName())
	if !okNL || !okND || narrowDetail.Y <= narrowList.Y || narrowDetail.X+narrowDetail.W > 720 {
		t.Fatalf("narrow 720x640 layout failed stack/width check: list=%+v(%v) detail=%+v(%v)", narrowList, okNL, narrowDetail, okND)
	}
	for _, label := range []string{zh.ProviderDeleteAccount("Studio gateway"), zh.ProviderSave(), zh.ProviderSaveApply()} {
		r, ok := tr.Find(label)
		if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 720 || r.Y+r.H > 640 {
			t.Fatalf("narrow 720x640 account footer control %q clipped/missing: rect=%+v ok=%v", label, r, ok)
		}
	}
}

func TestProviderAddModalPresetsPreserveKeyNarrowFooterAndEscapeFocus(t *testing.T) {
	a := providerUITestApp(t, pi.ProductProviderList{Policy: pi.ProductRoutingBalanced})
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
	for _, presetName := range []string{"OpenAI", "DeepSeek", "OpenRouter", "Kimi", "Zhipu GLM", "Qwen", "Ollama", msgs.ProviderCustom()} {
		if !tr.HasText(presetName) {
			t.Fatalf("Add provider modal missing preset %q: %v", presetName, tr.Texts())
		}
	}

	const syntheticKey = "sk-synthetic-preset-keep-123456"
	a.providers.editor.APIKey = syntheticKey
	if redacted := a.redactConnectionText("probe failed for " + syntheticKey); strings.Contains(redacted, syntheticKey) || !strings.Contains(redacted, "[redacted]") {
		t.Fatalf("redactConnectionText did not redact provider modal key: %q", redacted)
	}

	for _, tc := range []struct {
		click    string
		wantURL  string
		wantWire pi.WireAPI
	}{
		{"OpenAI", "https://api.openai.com/v1", pi.OpenAIResponses},
		{"DeepSeek", "https://api.deepseek.com/v1", pi.OpenAICompletions},
		{"Ollama", "http://127.0.0.1:11434/v1", pi.OpenAICompletions},
		{msgs.ProviderCustom(), "", pi.OpenAICompletions},
	} {
		if err := tr.Click(tc.click); err != nil {
			t.Fatalf("click preset %q: %v", tc.click, err)
		}
		tr.Frame()
		if a.providers.editor.APIKey != syntheticKey {
			t.Fatalf("preset %q wiped typed API key: got %q", tc.click, a.providers.editor.APIKey)
		}
		if a.providers.editor.BaseURL != tc.wantURL || a.providers.editor.WireAPI != tc.wantWire {
			t.Fatalf("preset %q set endpoint=%q wire=%q, want %q/%q", tc.click, a.providers.editor.BaseURL, a.providers.editor.WireAPI, tc.wantURL, tc.wantWire)
		}
	}

	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.providers.adding {
		t.Fatal("Escape did not dismiss Add provider modal at 1040x720")
	}
	if !tr.Focused(msgs.ProviderAddShort()) {
		t.Fatal("Escape dismissal did not return focus to opening Add button at 1040x720")
	}

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
