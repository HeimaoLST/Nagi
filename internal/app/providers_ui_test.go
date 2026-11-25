package app

import (
	"context"
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
	dir := t.TempDir()
	stub := filepath.Join(dir, "stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	client, err := pi.StartProduct(pi.ProductOptions{
		NodePath:        "/bin/sh",
		WorkerPath:      stub,
		StateDir:        dir,
		ProfilePath:     filepath.Join(dir, "profile.sqlite"),
		CredentialsPath: filepath.Join(dir, "connection.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = client.Kill(ctx)
		<-client.CallbacksDone()
	})
	a, _ := workspaceTestApp(t)
	a.demo = false
	a.runtimeStatus = runtimeStateReady
	a.connectionGeneration = 1
	a.pi = &piAppHost{
		generation:       1,
		product:          client,
		productProviders: pool,
	}
	a.applyProductProviders(pool)
	a.settingsOpen = true
	a.settingsTab = settingsTabConnection
	a.providers.tab = providerTabAccounts
	a.ensureConnectionForm()
	return a
}

func TestProviderSettingsSubTabsMasterDetailDraftsValidationAndPrimaryCTA(t *testing.T) {
	msgs := locales.In("en")

	t.Run("offline-hides-provider-subtabs-and-empty-keeps-legacy-form", func(t *testing.T) {
		offline, _ := workspaceTestApp(t)
		offline.windowWidth = 1040
		offline.openSettingsTab(settingsTabConnection, settingsSourceRail)
		trOff := ui.NewTester(offline.view, 1040, 720)
		trOff.SetLocale("en")
		trOff.Frame()
		if trOff.HasText(msgs.ProviderTabAccounts(0)) || trOff.HasText(msgs.ProviderTabRouting()) {
			t.Fatalf("offline connection settings unexpectedly rendered provider sub-tabs: %v", trOff.Texts())
		}

		empty := providerUITestApp(t, pi.ProductProviderList{Policy: pi.ProductRoutingBalanced})
		empty.windowWidth = 1040
		tr := ui.NewTester(empty.view, 1040, 720)
		tr.SetLocale("en")
		tr.Frame()
		for _, want := range []string{
			msgs.ProviderTabAccounts(0),
			msgs.ProviderTabRouting(),
			msgs.ProviderTabWorkspace(),
			msgs.ProviderNoAccounts(),
			msgs.ProviderAdd(),
			msgs.ConnectionFieldEndpoint(),
		} {
			if !tr.HasText(want) {
				t.Fatalf("empty Providers tab missing %q: %v", want, tr.Texts())
			}
		}
	})

	rev1 := uint64(1)
	rev2 := uint64(2)
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
					Models:        []string{"gpt-4o", "codex-flagship"},
					ContextTokens: 131072,
					OutputTokens:  8192,
					Enabled:       true,
				},
				CredentialStatus: "ok",
			},
			{
				ProductProviderAccount: pi.ProductProviderAccount{
					ProviderID:    "prov-internal-beta",
					Name:          "Ollama local",
					WireAPI:       pi.OpenAICompletions,
					BaseURL:       "http://127.0.0.1:11434/v1",
					DefaultModel:  "qwen3:8b",
					ContextTokens: 131072,
					OutputTokens:  8192,
					Enabled:       false,
				},
				CredentialStatus: "missing",
			},
		},
		ResolvedRoles: pi.ProductResolvedRoles{
			Dialogue:  &pi.ProductModelRoute{ProviderID: "prov-internal-alpha", ResolvedModel: "gpt-4o", ThinkingLevel: "off"},
			Coding:    &pi.ProductModelRoute{ProviderID: "prov-internal-alpha", ResolvedModel: "codex-flagship", ThinkingLevel: "medium"},
			Reasoning: &pi.ProductModelRoute{ProviderID: "prov-internal-alpha", ResolvedModel: "codex-flagship", ThinkingLevel: "medium"},
			Utility:   &pi.ProductModelRoute{ProviderID: "prov-internal-alpha", ResolvedModel: "gpt-4o", ThinkingLevel: "off"},
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

	listRect, okList := tr.Find("Ollama local")
	detailRect, okDetail := tr.Find(msgs.FieldName())
	if !okList || !okDetail || detailRect.X <= listRect.X {
		t.Fatalf("wide Master-Detail did not lay out side-by-side: list=%+v(%v) detail=%+v(%v)", listRect, okList, detailRect, okDetail)
	}
	if tr.HasText(msgs.ProviderApplyChanges()) || !tr.HasText(msgs.ProviderAllApplied()) {
		t.Fatalf("applied state should show All changes applied without Apply button: %v", tr.Texts())
	}

	alphaDraft := a.providers.drafts["prov-internal-alpha"]
	if alphaDraft == nil {
		t.Fatal("selected account draft missing")
	}
	if _, ok := alphaDraft.input(true); !ok {
		t.Fatalf("blank keepKey should succeed when BaseURL and WireAPI are unchanged: %+v", alphaDraft.fields)
	}
	alphaDraft.BaseURL = "https://changed.example.invalid/v1"
	if _, ok := alphaDraft.input(false); ok || alphaDraft.fields["APIKey"] != connection.ValidationRequired {
		t.Fatalf("changing endpoint without API key should fail APIKey validation: ok=%v fields=%+v", ok, alphaDraft.fields)
	}
	alphaDraft.Name = "Studio edited draft"
	alphaDraft.BaseURL = "https://studio.example.invalid/v1"

	if err := tr.Click("Ollama local"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.providers.selectedID != "prov-internal-beta" {
		t.Fatalf("clicking second account did not switch selection: got %q", a.providers.selectedID)
	}
	a.providers.drafts["prov-internal-beta"].Name = "Ollama edited draft"
	if err := tr.Click("Studio gateway"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.providers.drafts["prov-internal-alpha"].Name != "Studio edited draft" ||
		a.providers.drafts["prov-internal-beta"].Name != "Ollama edited draft" {
		t.Fatalf("switching accounts did not isolate drafts: alpha=%q beta=%q",
			a.providers.drafts["prov-internal-alpha"].Name, a.providers.drafts["prov-internal-beta"].Name)
	}

	appliedImg := cloneRGBA(tr.Image())
	saveApplyRect, okSaveApply := tr.Find(msgs.ProviderSaveApply())
	if !okSaveApply {
		t.Fatal("Save and apply provider button missing")
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
		t.Fatalf("pending state did not transfer sole primary button style to Apply provider changes: applied=%+v apply=%+v demoted=%+v",
			appliedBg, applyFill, demotedFill)
	}

	if err := tr.Click(msgs.ProviderTabRouting()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	for _, want := range []string{msgs.ProviderRouting(), msgs.ProviderActiveRoutingNote(), msgs.ProviderRoleDialogue(), msgs.ProviderRoleCoding(), msgs.ProviderRoleReasoning(), msgs.ProviderRoleUtility()} {
		if !tr.HasText(want) {
			t.Fatalf("Routing sub-tab missing %q: %v", want, tr.Texts())
		}
	}

	if err := tr.Click(msgs.ProviderTabAccounts(2)); err != nil {
		t.Fatal(err)
	}
	a.windowWidth = 720
	tr.SetSize(720, 640)
	tr.SetLocale("zh-Hans")
	tr.SetDark(true)
	tr.Frame()
	zh := locales.In("zh-Hans")
	narrowList, okNL := tr.Find("Ollama local")
	narrowDetail, okND := tr.Find(zh.FieldName())
	if !okNL || !okND || narrowDetail.Y <= narrowList.Y {
		t.Fatalf("narrow 720x640 layout did not stack detail below list: list=%+v(%v) detail=%+v(%v)", narrowList, okNL, narrowDetail, okND)
	}
	if narrowDetail.X+narrowDetail.W > 720 {
		t.Fatalf("narrow 720x640 detail overflowed horizontally: %+v", narrowDetail)
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

	if err := tr.Click(msgs.ProviderAdd()); err != nil {
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
	if !tr.Focused(msgs.ProviderAdd()) {
		t.Fatal("Escape dismissal did not return focus to opening Add provider button at 1040x720")
	}

	a.windowWidth = 720
	tr.SetSize(720, 640)
	tr.SetLocale("zh-Hans")
	tr.SetDark(true)
	tr.Frame()
	zh := locales.In("zh-Hans")
	if err := tr.Click(zh.ProviderAdd()); err != nil {
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
	if !tr.Focused(zh.ProviderAdd()) {
		t.Fatal("Escape dismissal did not return focus to opening Add provider button at 720x640")
	}
}

func TestProviderCredentialUnavailableNoticesAndReadConnectionStartupV3(t *testing.T) {
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
	if startup.LoadErr != nil || startup.Config != nil || startup.FormConfig == nil {
		t.Fatalf("readConnectionStartup mishandled provider-only v3 config: loadErr=%v config=%+v form=%+v", startup.LoadErr, startup.Config, startup.FormConfig)
	}

	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			msgs := locales.In(locale)
			rev1 := uint64(1)
			pool := pi.ProductProviderList{
				SavedRevision:     &rev1,
				EffectiveRevision: &rev1,
				Policy:            pi.ProductRoutingBalanced,
				Accounts: []pi.ProductProviderAccountView{
					{
						ProductProviderAccount: pi.ProductProviderAccount{
							ProviderID: "p1", Name: "Studio", WireAPI: pi.OpenAICompletions,
							BaseURL: "https://api.example.com/v1", DefaultModel: "gpt-4o", Enabled: true,
						},
						CredentialStatus: "missing",
					},
				},
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
				t.Fatalf("%s rejected send notice or preserved draft/quote missing: draft=%q texts=%v", locale, ct.Draft, tr.Texts())
			}
			if tr.HasText(msgs.ProviderCredentialRecovery()) {
				t.Fatalf("%s duplicate top credential recovery banner rendered alongside composer notice: %v", locale, tr.Texts())
			}
			if _, ok := tr.Find(msgs.ConnectionRecoveryOpenSettings()); !ok {
				t.Fatalf("%s rejected send missing Open connection settings action: %v", locale, tr.Texts())
			}

			ct.clearSendError()
			ct.activity.open = true
			ct.activity.activityID = "act-paused-1"
			ct.activity.activity = &pi.ProductActivity{
				ActivityID: "act-paused-1",
				OwnerBotID: ct.ID,
				Goal:       "Check the paper settings",
				State:      "needsAttention",
				Questions: []pi.ProductQuestion{
					{
						QuestionID:      "q-paused-1",
						Text:            "Confirm paper size",
						State:           "answered",
						Delivery:        "saved",
						DeliveryBlocker: pi.ProductCredentialUnavailable,
						Answer:          &pi.ProductText{Text: "Use A4 for the draft."},
					},
				},
			}
			tr.Frame()
			if !tr.HasText("Use A4 for the draft.") || !tr.HasText(msgs.ProviderAnswerPaused()) {
				t.Fatalf("%s paused Activity answer notice missing: %v", locale, tr.Texts())
			}
			if tr.HasText(msgs.ProviderCredentialRecovery()) {
				t.Fatalf("%s duplicate top credential recovery banner rendered alongside paused Activity answer: %v", locale, tr.Texts())
			}
			if !slices.Contains(tr.Texts(), msgs.ConnectionRecoveryOpenSettings()) {
				t.Fatalf("%s paused Activity answer missing Open connection settings link", locale)
			}
		})
	}
}
