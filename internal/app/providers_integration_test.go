//go:build integration

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/connection"
	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestPiAppProvidersPlanAFlowRoutingRevokeAndReopen(t *testing.T) {
	const (
		apiKey         = "sk-provider-plan-a-loopback"
		chatPrompt     = "Hello from provider-only mode"
		chatReply      = "Provider-only conversation succeeded."
		activityID     = "act-provider-only"
		activityGoal   = "Check the paper settings"
		questionText   = "Confirm the paper size"
		answerText     = "Use A4 for the draft."
		rejectedPrompt = "Keep this draft after credential revocation."
	)
	node, worker := os.Getenv("NAGI_TEST_NODE"), os.Getenv("NAGI_TEST_WORKER")
	payload := filepath.Dir(filepath.Dir(node))
	if !filepath.IsAbs(node) || node != filepath.Join(payload, "bin", "node") || worker != filepath.Join(payload, "dist", "worker.js") {
		t.Fatal("NAGI_TEST_NODE and NAGI_TEST_WORKER must identify one pinned payload")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	isolateConnectionApp(t, root, payload)

	advertised := make([]map[string]string, 0, 222)
	for i := 207; i >= 0; i-- {
		advertised = append(advertised, map[string]string{"id": fmt.Sprintf("zz-custom-%03d", i)})
	}
	for _, id := range []string{
		"text-embedding-3-small", "tts-1", "whisper-1", "gpt-image-2", "omni-moderation-latest",
		"o1-pro", "o3-pro", "gpt-5.4-pro", "gpt-5.5-pro",
		"o3", "gpt-4o", "gpt-5.4-nano", "gpt-5.4-mini", "gpt-5.4",
	} {
		advertised = append(advertised, map[string]string{"id": id})
	}

	var (
		mu             sync.Mutex
		modelsRequests int
		chatRequests   int
		chatModels     []string
		reqSeq         int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api.openai.com/v1/models" {
			mu.Lock()
			modelsRequests++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": advertised})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api.openai.com/v1/responses" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		chatRequests++
		reqSeq++
		seq := reqSeq
		chatModels = append(chatModels, req.Model)
		mu.Unlock()

		var item map[string]any
		if strings.Contains(string(body), activityGoal) || strings.Contains(string(body), "Start paper check") {
			args, _ := json.Marshal(map[string]any{"text": questionText})
			item = map[string]any{
				"type":      "function_call",
				"id":        fmt.Sprintf("fc_%d", seq),
				"call_id":   fmt.Sprintf("call_ask_paper_%d", seq),
				"name":      "activity_ask",
				"arguments": string(args),
				"status":    "completed",
			}
		} else {
			item = map[string]any{
				"type":    "message",
				"id":      fmt.Sprintf("msg_%d", seq),
				"role":    "assistant",
				"status":  "completed",
				"content": []any{map[string]any{"type": "output_text", "text": chatReply, "annotations": []any{}}},
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		sendEvent := func(kind string, payload map[string]any) {
			payload["type"] = kind
			raw, _ := json.Marshal(payload)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
		}
		sendEvent("response.created", map[string]any{"response": map[string]any{"id": fmt.Sprintf("resp_%d", seq), "status": "in_progress"}})
		sendEvent("response.output_item.done", map[string]any{"output_index": 0, "item": item})
		sendEvent("response.completed", map[string]any{"response": map[string]any{
			"id": fmt.Sprintf("resp_%d", seq), "status": "completed", "output": []any{item},
			"usage": map[string]any{"input_tokens": 20, "output_tokens": 10, "total_tokens": 30},
		}})
	}))
	t.Cleanup(server.Close)

	offsiteLog := filepath.Join(root, "offsite.log")
	preloadPath := filepath.Join(root, "editor-preload.mjs")
	preload := fmt.Sprintf(`import { appendFileSync } from 'node:fs';
const orig = globalThis.fetch;
const targetBase = %q;
const offsiteLog = %q;
globalThis.fetch = (input, init) => {
  const url = new URL(input instanceof Request ? input.url : String(input));
  if (url.hostname === '127.0.0.1' || url.hostname === 'localhost') return orig(input, init);
  if (url.hostname === 'api.openai.com') {
    const dest = new URL(targetBase + '/' + url.hostname + url.pathname + url.search);
    return orig(input instanceof Request ? new Request(dest, input) : dest, init);
  }
  appendFileSync(offsiteLog, url.origin + '\n');
  throw new Error('Blocked external fetch in offline editor test: ' + url.origin);
};
`, server.URL, offsiteLog)
	if err := os.WriteFile(preloadPath, []byte(preload), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODE_OPTIONS", "--import "+preloadPath)

	if err := writeProfileJSON(guiStorePath(), storedState{
		Version:  contactStoreVersion,
		Selected: "cedar",
		Contacts: []storedContact{
			{ID: "cedar", Name: "Cedar", Workspace: root},
			{ID: "copper", Name: "Copper", Workspace: root},
		},
		Issues: []storedActivity{
			{ID: activityID, ContactID: "copper", Goal: activityGoal, Status: activityStatusNotStarted, RequirementsRevision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	msgs := locales.In("en")

	w := openProductWorkbench(t)
	w.wait(func() bool { return w.app.settingsOpen && w.app.settingsTab == settingsTabConnection })

	w.click("OpenAI")
	w.wait(func() bool { return w.app.providers.adding && w.app.providers.editor != nil })
	var openAIErr string
	w.pump.do(func() {
		ed := w.app.providers.editor
		if ed.DefaultModel != "" || len(ed.Models) != 0 {
			openAIErr = fmt.Sprintf("OpenAI preset unexpectedly prefilled models: default=%q models=%v", ed.DefaultModel, ed.Models)
			return
		}
		if w.view.HasText("gpt-5") || !w.view.HasText(msgs.ProviderFetchModels()) || !w.view.HasText(msgs.ProviderFetchModelsHint()) || w.view.HasText(msgs.ProviderTestSync()) {
			openAIErr = fmt.Sprintf("OpenAI before-fetch UI state mismatch: %v", w.view.Texts())
			return
		}
		ed.Name = "Studio gateway"
		ed.APIKey = apiKey
		w.view.Frame()
	})
	if openAIErr != "" {
		t.Fatal(openAIErr)
	}
	w.click(msgs.ProviderTestAccount("Studio gateway"))
	w.wait(func() bool {
		return !w.app.providerBusy() && w.app.providers.editor != nil && w.app.providers.editor.tested &&
			len(w.app.providers.editor.Models) == 128 && w.app.providers.editor.DefaultModel != "" &&
			w.view.HasText(msgs.ProviderTestSync()) && !w.view.HasText(msgs.ProviderFetchModels())
	})

	mu.Lock()
	gotModelsProbe := modelsRequests
	mu.Unlock()
	if gotModelsProbe != 1 {
		t.Fatalf("expected 1 /v1/models discovery request, got %d", gotModelsProbe)
	}
	var editorModels []string
	var autoPickedModel string
	w.pump.do(func() {
		editorModels = slices.Clone(w.app.providers.editor.Models)
		autoPickedModel = w.app.providers.editor.DefaultModel
	})
	if !slices.Contains([]string{"gpt-4o", "gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano", "o3"}, autoPickedModel) {
		t.Fatalf("empty-default discovery auto-selected unexpected model %q (pro/non-chat forbidden)", autoPickedModel)
	}
	if len(editorModels) != 128 {
		t.Fatalf("unchanged editor discovery cap = %d, want 128", len(editorModels))
	}
	for _, wantFirst := range []string{"gpt-4o", "gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano", "o3"} {
		if !slices.Contains(editorModels[:5], wantFirst) {
			t.Fatalf("recommended chat model %q not ranked in top 5: %v", wantFirst, editorModels[:12])
		}
	}
	for _, mustExistInCapped := range []string{"gpt-5.4-pro", "o1-pro", "text-embedding-3-small"} {
		if !slices.Contains(editorModels, mustExistInCapped) {
			t.Fatalf("expected %q in capped 128 list to verify safe active pool exclusion: %v", mustExistInCapped, editorModels[:16])
		}
	}

	w.frame(func() { w.app.providers.editor.DefaultModel = "gpt-4o" })
	w.click(msgs.ProviderSaveApply())
	w.wait(func() bool {
		return !w.app.providerBusy() && !w.app.providers.adding && len(w.app.productHost().productProviders.Accounts) == 1 &&
			w.app.hasUsableProviderRoute() && !w.app.productProvidersPending()
	})
	if w.app.connectionSaved != nil || w.app.connectionEffective != nil {
		t.Fatalf("provider-only flow unexpectedly created legacy connection: saved=%+v eff=%+v", w.app.connectionSaved, w.app.connectionEffective)
	}
	if w.app.providers.editor.APIKey != "" {
		t.Fatal("accepted provider Save did not clear plaintext APIKey")
	}
	var savedPool pi.ProductProviderList
	w.pump.do(func() {
		savedPool = w.app.productHost().productProviders
	})
	if len(savedPool.Accounts) != 1 || len(savedPool.Accounts[0].Models) != 128 || savedPool.Accounts[0].EnabledModels != nil {
		t.Fatalf("unchanged editor saved unexpected pool metadata: %+v", savedPool.Accounts)
	}
	if savedPool.ResolvedRoles.Dialogue == nil || savedPool.ResolvedRoles.Dialogue.ResolvedModel != "gpt-4o" ||
		savedPool.ResolvedRoles.Coding == nil || savedPool.ResolvedRoles.Coding.ResolvedModel != "gpt-5.4-mini" ||
		savedPool.ResolvedRoles.Reasoning == nil || savedPool.ResolvedRoles.Reasoning.ResolvedModel != "o3" ||
		savedPool.ResolvedRoles.Utility == nil || savedPool.ResolvedRoles.Utility.ResolvedModel != "gpt-5.4-nano" {
		t.Fatalf("safe default balanced pool mismatch: %+v", savedPool.ResolvedRoles)
	}

	w.click("Ollama")
	w.wait(func() bool { return w.app.providers.adding && w.app.providers.editor != nil })
	var ollamaErr string
	w.pump.do(func() {
		ed := w.app.providers.editor
		if ed.DefaultModel != "" || len(ed.Models) != 0 || w.view.HasText("qwen3:8b") {
			ollamaErr = fmt.Sprintf("Ollama preset unexpectedly prefilled models: default=%q models=%v", ed.DefaultModel, ed.Models)
			return
		}
		ed.DefaultModel = "qwen3:8b"
		ed.Enabled = false
		ed.APIKey = "local-ollama-token"
		w.view.Frame()
	})
	if ollamaErr != "" {
		t.Fatal(ollamaErr)
	}
	w.click(msgs.ProviderSave())
	w.wait(func() bool {
		return !w.app.providerBusy() && !w.app.providers.adding && len(w.app.productHost().productProviders.Accounts) == 2 && w.app.productProvidersPending()
	})
	w.click(msgs.ProviderApplyChanges())
	w.wait(func() bool { return !w.app.providerBusy() && !w.app.productProvidersPending() })

	w.click(msgs.ProviderTabRouting())
	w.wait(func() bool {
		return w.app.providers.tab == providerTabRouting && w.view.HasText(msgs.ProviderQuality())
	})
	w.click(msgs.ProviderQuality())
	w.wait(func() bool {
		return !w.app.providerBusy() && w.app.productHost().productProviders.Policy == pi.ProductRoutingQuality && w.app.productProvidersPending()
	})
	w.click(msgs.ProviderApplyChanges())
	w.wait(func() bool {
		pool := w.app.productHost().productProviders
		return !w.app.providerBusy() && !w.app.productProvidersPending() &&
			pool.ResolvedRoles.Dialogue != nil && pool.ResolvedRoles.Dialogue.ResolvedModel == "gpt-4o" &&
			pool.ResolvedRoles.Coding != nil && pool.ResolvedRoles.Coding.ResolvedModel == "gpt-5.4" &&
			pool.ResolvedRoles.Reasoning != nil && pool.ResolvedRoles.Reasoning.ResolvedModel == "o3" &&
			pool.ResolvedRoles.Utility != nil && pool.ResolvedRoles.Utility.ResolvedModel == "gpt-5.4-mini"
	})

	w.click(msgs.SettingsBack())
	var cedar, copper *contact
	w.pump.do(func() {
		cedar = w.app.contactByID("cedar")
		copper = w.app.contactByID("copper")
	})
	w.wait(func() bool { return !w.app.settingsOpen && cedar != nil && cedar.Loaded })

	w.click(msgs.ComposerLabel("Cedar"))
	w.frame(func() { w.view.Type(chatPrompt) })
	w.click(msgs.SendAction())
	w.wait(func() bool {
		return cedar.SendState == sendIdle && cedar.Status != statusWorking && w.view.HasText(chatReply)
	})

	w.click(msgs.AvatarContactLabel("Copper"))
	w.wait(func() bool {
		return w.app.selectedContact() == copper && len(copper.activity.items) == 1 && !copper.activity.reading
	})
	w.click(msgs.WorkActivity())
	w.click(msgs.IssueRowLabel(activityGoal))
	w.wait(func() bool { return copper.activity.activity != nil && !copper.activity.reading })
	w.click(msgs.SupplementAdd())
	w.frame(func() { w.view.Type("Start paper check") })
	w.frame(func() { copper.activity.scroll.Y = 1000 })
	w.click(msgs.IssueResume())
	w.wait(func() bool {
		item := copper.activity.activity
		return item != nil && len(item.OpenQuestionIDs) == 1 && !copper.activity.reading
	})

	openAct, err := w.client.GetActivity(ctx, activityID)
	if err != nil || len(openAct.Executions) == 0 || len(openAct.Questions) == 0 {
		t.Fatalf("activity did not record open question: %+v err=%v", openAct, err)
	}
	run, question := openAct.Executions[0], openAct.Questions[0]

	mu.Lock()
	chatsBeforeRevoke := chatRequests
	dispatchedModels := slices.Clone(chatModels)
	mu.Unlock()
	if chatsBeforeRevoke < 2 {
		t.Fatalf("expected provider-only chat + activity requests >= 2, got %d", chatsBeforeRevoke)
	}
	if !slices.Equal(dispatchedModels, []string{"gpt-4o", "gpt-5.4-mini"}) {
		t.Fatalf("unexpected outbound wire models from unchanged 222-ID editor flow: %v", dispatchedModels)
	}

	w.frame(func() { w.app.openSettingsTab(settingsTabConnection, settingsSourceRail) })
	w.wait(func() bool { return w.app.settingsOpen })
	w.click(msgs.ProviderTabAccounts(2))
	w.wait(func() bool { return w.app.providers.tab == providerTabAccounts && w.view.HasText("Studio gateway") })
	w.click("Studio gateway")
	w.wait(func() bool {
		return w.app.providers.adding && w.view.HasText(msgs.ProviderRevokeAccount("Studio gateway"))
	})
	var savedBeforeRevoke, effBeforeRevoke uint64
	w.pump.do(func() {
		pool := w.app.productHost().productProviders
		savedBeforeRevoke, effBeforeRevoke = *pool.SavedRevision, *pool.EffectiveRevision
	})
	w.click(msgs.ProviderRevokeAccount("Studio gateway"))
	w.wait(func() bool {
		pool := w.app.productHost().productProviders
		return !w.app.providerBusy() && !w.app.hasUsableProviderRoute() && w.app.runtimeErrKind == runtimeNoticeCredentialUnavailable &&
			*pool.SavedRevision == savedBeforeRevoke && *pool.EffectiveRevision == effBeforeRevoke
	})
	w.frame(func() { w.app.dismissProviderDialog() })

	w.click(msgs.SettingsBack())
	w.click(msgs.AvatarContactLabel("Cedar"))
	w.wait(func() bool { return !w.app.settingsOpen && w.app.selectedContact() == cedar })
	w.click(msgs.ComposerLabel("Cedar"))
	w.frame(func() { w.view.Type(rejectedPrompt) })
	w.frame(func() { w.view.Key(0, ui.KeyEnter) })
	w.wait(func() bool {
		return cedar.SendState == sendIdle && cedar.SendErrorKind == sendErrorCredentialUnavailable && w.view.HasText(msgs.ProviderSendRejected())
	})
	if cedar.Draft != rejectedPrompt {
		t.Fatalf("rejected credential_unavailable send lost draft: got %q want %q", cedar.Draft, rejectedPrompt)
	}

	savedAns, err := w.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: productActivityExecution(activityID, run.ExecutionID),
		QuestionID:                     question.QuestionID,
		QuestionRevision:               question.Revision,
		OperationID:                    "answer-revoked-provider",
		ProductText:                    pi.ProductText{Text: answerText, Materials: []pi.ProductMaterial{}},
	})
	if err != nil || !savedAns.AnswerSaved || savedAns.Delivery != "saved" || savedAns.DeliveryBlocker != pi.ProductCredentialUnavailable {
		t.Fatalf("paused Activity answer mismatch: %+v err=%v", savedAns, err)
	}
	w.click(msgs.AvatarContactLabel("Copper"))
	w.frame(func() { w.app.openProductActivity(copper, activityID) })
	w.wait(func() bool { return w.view.HasText(answerText) && w.view.HasText(msgs.ProviderAnswerPaused()) })

	mu.Lock()
	chatsAfterRevoke := chatRequests
	mu.Unlock()
	if chatsAfterRevoke != chatsBeforeRevoke {
		t.Fatalf("revoked send or paused answer triggered extra provider requests: before=%d after=%d", chatsBeforeRevoke, chatsAfterRevoke)
	}

	w.shutdown(ctx)

	privateRaw, err := os.ReadFile(connectionConfigPath(guiStorePath()))
	if err != nil {
		t.Fatal(err)
	}
	var privateDoc struct {
		Version  int              `json:"version"`
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(privateRaw, &privateDoc); err != nil || privateDoc.Version != 3 || len(privateDoc.Accounts) == 0 {
		t.Fatalf("v3 raw private config mismatch: %s err=%v", string(privateRaw), err)
	}
	for _, acc := range privateDoc.Accounts {
		for _, forbidden := range []string{"priority", "enabledModels", "fallbackOnly", "modelOverrides"} {
			if _, ok := acc[forbidden]; ok {
				t.Fatalf("v3 connection.json leaked routing field %q: %+v", forbidden, acc)
			}
		}
	}
	privateCfg, err := connection.ReadPrivateConfig(connectionConfigPath(guiStorePath()))
	if err != nil || privateCfg.Version != 3 || len(privateCfg.Accounts) == 0 || privateCfg.CredentialSource != "" {
		t.Fatalf("v3 provider-only config on disk mismatch: %+v err=%v", privateCfg, err)
	}
	if data, err := os.ReadFile(offsiteLog); err == nil && len(data) > 0 {
		t.Fatalf("unchanged editor flow attempted external network calls: %s", string(data))
	}
	if startup := readConnectionStartup(guiStorePath()); startup.LoadErr != nil {
		t.Fatalf("readConnectionStartup failed on reopened v3 provider-only profile: %v", startup.LoadErr)
	}

	reopened := openProductWorkbench(t)
	reopened.wait(func() bool {
		return len(reopened.app.productHost().productProviders.Accounts) == 2 &&
			reopened.app.runtimeErrKind == runtimeNoticeCredentialUnavailable &&
			!reopened.app.settingsOpen
	})
	reopened.shutdown(ctx)
}

func TestPiAppProvidersV2UpgradeRT1PoolOrderRT2PinsAndCooldownFailover(t *testing.T) {
	const rtKey = "synthetic-provider-rt-key"
	node, worker := os.Getenv("NAGI_TEST_NODE"), os.Getenv("NAGI_TEST_WORKER")
	payload := filepath.Dir(filepath.Dir(node))
	if !filepath.IsAbs(node) || node != filepath.Join(payload, "bin", "node") || worker != filepath.Join(payload, "dist", "worker.js") {
		t.Fatal("NAGI_TEST_NODE and NAGI_TEST_WORKER must identify one pinned payload")
	}

	var modelsCount, completionsCount atomic.Int32
	var rateLimited atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+rtKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			modelsCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"},{"id":"gpt-5.4-mini"},{"id":"gpt-5.4-pro"},{"id":"text-embedding-3-small"}]}`)
			return
		}
		if r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/responses") || strings.HasSuffix(r.URL.Path, "/chat/completions")) {
			completionsCount.Add(1)
			if rateLimited.Load() {
				w.Header().Set("Retry-After", "60")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"error":{"message":"synthetic provider cooldown","type":"rate_limit_error"}}`)
				return
			}
			t.Error("unexpected model completion call before cooldown phase")
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	setupIsolatedProfile := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		isolateConnectionApp(t, root, payload)
		preload := filepath.Join(root, "route-fixture.mjs")
		source := fmt.Sprintf(`import { appendFileSync } from 'node:fs';
const orig = globalThis.fetch;
globalThis.fetch = (input, init) => {
  const url = new URL(input instanceof Request ? input.url : String(input));
  if (url.hostname === 'api.openai.com') {
    const local = new URL(%q + url.pathname + url.search);
    return orig(input instanceof Request ? new Request(local, input) : local, init);
  }
  if (url.hostname === '127.0.0.1' || url.hostname === 'localhost') return orig(input, init);
  appendFileSync(%q, url.origin + '\n');
  throw new Error('Blocked external fetch in offline test: ' + url.origin);
};`, server.URL, filepath.Join(root, "offsite.log"))
		if err := os.WriteFile(preload, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("NODE_OPTIONS", "--import="+preload)
		if err := writeProfileJSON(guiStorePath(), storedState{
			Version:  contactStoreVersion,
			Selected: "cedar",
			Contacts: []storedContact{{ID: "cedar", Name: "Cedar", Workspace: root, BuiltinTools: &[]string{}}},
		}); err != nil {
			t.Fatal(err)
		}
		return root
	}

	clickScrolled := func(t *testing.T, w *productWorkbench, label string) {
		t.Helper()
		var err error
		var visible []string
		w.pump.do(func() {
			tr := w.view
			if tr.FramePending() {
				tr.Frame()
			}
			if err = tr.Click(label); err == nil {
				return
			}
			for range 12 {
				r, ok := tr.Find(label)
				if !ok || (r.Y >= 105 && r.Y+r.H <= 520) {
					break
				}
				x := float32(920)
				if w.app.providers.adding {
					x = 700
				}
				tr.Scroll(x, 360, 0, r.Y-320)
			}
			err = tr.Click(label)
			if err != nil {
				tr.Frame()
				err = tr.Click(label)
				if err != nil {
					visible = tr.Texts()
				}
			}
		})
		if err != nil {
			t.Fatalf("click %q: %v, visible: %v", label, err, visible)
		}
	}
	typeInput := func(t *testing.T, w *productWorkbench, label, value string) {
		t.Helper()
		var err error
		w.pump.do(func() {
			err = focusPiControl(w.view, label, true)
			if err == nil {
				w.view.Command("selectAll")
				w.view.Type(value)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	applyChanges := func(t *testing.T, w *productWorkbench) {
		t.Helper()
		clickScrolled(t, w, locales.In("en").ProviderApplyChanges())
		w.wait(func() bool { return !w.app.providerBusy() && !w.app.productProvidersPending() })
	}
	pinRole := func(t *testing.T, w *productWorkbench, roleLabel, option string, role pi.ProductModelRole, wantMode string) {
		t.Helper()
		clickScrolled(t, w, locales.In("en").ProviderPinRole(roleLabel))
		clickScrolled(t, w, option)
		w.wait(func() bool {
			return !w.app.providerBusy() && w.app.productHost().productProviders.Overrides[role].Mode == wantMode
		})
	}

	t.Run("legacy-v2-import-upgrade", func(t *testing.T) {
		root := setupIsolatedProfile(t)
		msgs := locales.In("en")
		cfg := connectionConfig{
			API: connectionAPIResponses, Endpoint: "https://api.openai.com/v1", APIKey: rtKey,
			Model: "gpt-4o", ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: root, MaxActiveTasks: 2,
		}
		path := connectionConfigPath(guiStorePath())
		if err := saveConnectionConfig(path, cfg); err != nil {
			t.Fatal(err)
		}
		assertVersion := func(want int) {
			t.Helper()
			data, err := os.ReadFile(path)
			var doc struct {
				Version int `json:"version"`
			}
			if err != nil || json.Unmarshal(data, &doc) != nil || doc.Version != want {
				t.Fatalf("connection.json version = %d (%v), want %d", doc.Version, err, want)
			}
		}
		assertVersion(2)
		w := openProductWorkbench(t)
		w.frame(func() { w.app.openConfiguration() })
		w.wait(func() bool { return w.view.HasText(msgs.ProviderV2ImportTitle()) })
		w.click(msgs.ProviderV2ImportTitle())
		w.wait(func() bool {
			return w.app.providers.adding && w.app.providers.first != nil && w.app.providers.first.preset == -1
		})
		w.frame(func() {
			e := w.app.providers.first
			if e.BaseURL != cfg.Endpoint || e.WireAPI != pi.OpenAIResponses || e.DefaultModel != cfg.Model || e.context != "65536" || e.output != "4096" || e.APIKey != "" {
				t.Errorf("legacy v2 prefill mismatch: %+v", e)
			}
		})
		typeInput(t, w, msgs.ConnectionFieldAPIKey(), rtKey)
		clickScrolled(t, w, msgs.ProviderSaveApply())
		w.wait(func() bool {
			return !w.app.providerBusy() && !w.app.productProvidersPending() && len(w.app.productHost().productProviders.Accounts) == 1
		})
		assertVersion(3)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		w.shutdown(ctx)
	})

	t.Run("rt1-rt2-order-pins-cooldown-failover-and-reopen", func(t *testing.T) {
		root := setupIsolatedProfile(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		msgs := locales.In("en")
		w := openProductWorkbench(t)
		w.wait(func() bool { return w.app.settingsOpen && len(w.app.productHost().productProviders.Accounts) == 0 })

		clickScrolled(t, w, "DeepSeek")
		w.wait(func() bool { return w.app.providers.adding && w.app.providers.first != nil })
		var dsCheckErr string
		w.pump.do(func() {
			e := w.app.providers.first
			if e.DefaultModel != "" || len(e.Models) != 0 || w.view.HasText("deepseek-chat") || !w.view.HasText(msgs.ProviderFetchModels()) {
				dsCheckErr = fmt.Sprintf("DeepSeek preset unexpectedly prefilled models: default=%q models=%v texts=%v", e.DefaultModel, e.Models, w.view.Texts())
			}
			w.app.dismissProviderDialog()
			w.view.Frame()
		})
		if dsCheckErr != "" {
			t.Fatal(dsCheckErr)
		}
		clickScrolled(t, w, "OpenAI")
		w.wait(func() bool { return w.app.providers.adding && w.app.providers.first != nil })
		var oaiCheckErr string
		w.pump.do(func() {
			e := w.app.providers.first
			if e.DefaultModel != "" || len(e.Models) != 0 || w.view.HasText("gpt-5") {
				oaiCheckErr = fmt.Sprintf("OpenAI preset unexpectedly prefilled models: default=%q models=%v", e.DefaultModel, e.Models)
			}
		})
		if oaiCheckErr != "" {
			t.Fatal(oaiCheckErr)
		}
		typeInput(t, w, msgs.ConnectionFieldAPIKey(), rtKey)
		w.frame(func() { w.app.dismissProviderDialog() })
		clickScrolled(t, w, "OpenAI")
		w.frame(func() {
			if e := w.app.providers.first; e == nil || e.APIKey != rtKey || e.presetID != "openai" {
				t.Errorf("reopening OpenAI modal lost key or preset: %+v", e)
			}
		})
		typeInput(t, w, msgs.FieldName(), "Studio gateway")
		clickScrolled(t, w, msgs.ProviderTestAccount("Studio gateway"))
		w.wait(func() bool {
			return !w.app.providerBusy() && w.app.providers.first != nil && w.app.providers.first.tested &&
				w.app.providers.first.DefaultModel != "" && w.view.HasText(msgs.ProviderTestSync())
		})
		var discoveredErr string
		w.pump.do(func() {
			e := w.app.providers.first
			if e.DefaultModel == "gpt-5.4-pro" || e.DefaultModel == "text-embedding-3-small" {
				discoveredErr = fmt.Sprintf("discovery auto-picked protected/non-chat model %q", e.DefaultModel)
				return
			}
			ready := e.discoveredModel("gpt-4o")
			protected := e.discoveredModel("gpt-5.4-pro")
			embedding := e.discoveredModel("text-embedding-3-small")
			if ready.ToolCall == nil || !*ready.ToolCall || !ready.Recommended || !protected.PriceProtected || embedding.IsChat == nil || *embedding.IsChat || len(e.discovered) != 4 || e.EnabledModels != nil {
				discoveredErr = fmt.Sprintf("discovered capability metadata mismatch: %+v", e.discovered)
				return
			}
			e.DefaultModel = "gpt-4o"
			w.view.Frame()
		})
		if discoveredErr != "" {
			t.Fatal(discoveredErr)
		}
		clickScrolled(t, w, msgs.ProviderSaveApply())
		w.wait(func() bool {
			return !w.app.providerBusy() && !w.app.providers.adding && !w.app.productProvidersPending() && len(w.app.productHost().productProviders.Accounts) == 1
		})
		one, err := w.client.ListProviders(ctx)
		if err != nil || one.Accounts[0].EnabledModels != nil {
			t.Fatalf("untouched EnabledModels was unexpectedly populated: %+v err=%v", one, err)
		}
		primaryID := one.Accounts[0].ProviderID

		clickScrolled(t, w, "Studio gateway")
		w.wait(func() bool { return w.app.providers.adding && w.view.HasText(msgs.ProviderAddAnother("OpenAI")) })
		clickScrolled(t, w, msgs.ProviderAddAnother("OpenAI"))
		w.wait(func() bool {
			return w.app.providers.adding && w.app.providers.editor != nil && w.app.providers.editor.ProviderID != primaryID
		})
		typeInput(t, w, msgs.FieldName(), "Backup gateway")
		typeInput(t, w, msgs.ConnectionFieldAPIKey(), rtKey)
		typeInput(t, w, msgs.ProviderDefaultModel(), "gpt-4o")
		clickScrolled(t, w, msgs.ProviderTestAccount("Backup gateway"))
		w.wait(func() bool {
			return !w.app.providerBusy() && w.app.providers.editor != nil && w.app.providers.editor.tested
		})
		clickScrolled(t, w, msgs.ProviderUseAsBackup())
		clickScrolled(t, w, msgs.ProviderToggleModel("gpt-5.4-mini"))
		clickScrolled(t, w, msgs.ProviderToggleModel("gpt-4o"))
		clickScrolled(t, w, msgs.ProviderSave())
		w.wait(func() bool {
			return !w.app.providerBusy() && !w.app.providers.adding && len(w.app.productHost().productProviders.Accounts) == 2
		})
		two, err := w.client.ListProviders(ctx)
		if err != nil {
			t.Fatal(err)
		}
		backupID := two.Accounts[1].ProviderID
		if row := two.Accounts[1]; row.EnabledModels == nil || len(*row.EnabledModels) != 0 || row.FallbackOnly == nil || !*row.FallbackOnly {
			t.Fatalf("explicit empty EnabledModels and FallbackOnly did not persist: %+v", row)
		}

		clickScrolled(t, w, "Backup gateway")
		w.wait(func() bool { return w.app.providers.adding && w.app.providers.selectedID == backupID })
		clickScrolled(t, w, msgs.ProviderToggleModel("gpt-4o"))
		clickScrolled(t, w, msgs.ProviderSave())
		w.wait(func() bool {
			row := w.app.productHost().productProviders.Accounts[1]
			return !w.app.providerBusy() && !w.app.providers.adding && row.EnabledModels != nil && slices.Equal(*row.EnabledModels, []string{"gpt-4o"})
		})
		clickRowMove := func(rowLabel, moveLabel string) {
			t.Helper()
			var okR, okM bool
			var rr, mr ui.Rect
			w.frame(func() {
				rr, okR = w.view.Find(rowLabel)
				mr, okM = w.view.Find(moveLabel)
				if okR && okM {
					w.view.ClickAt(mr.X+mr.W/2, rr.Y+rr.H/2)
				}
			})
			if !okR || !okM {
				t.Fatalf("row move control missing: row=%q(%v) move=%q(%v)", rowLabel, okR, moveLabel, okM)
			}
		}
		clickRowMove("Backup gateway", msgs.ProviderMoveUp())
		w.wait(func() bool {
			return !w.app.providerBusy() && w.app.productHost().productProviders.Accounts[0].ProviderID == backupID
		})
		clickRowMove("Backup gateway", msgs.ProviderMoveDown())
		w.wait(func() bool {
			return !w.app.providerBusy() && w.app.productHost().productProviders.Accounts[0].ProviderID == primaryID
		})
		clickRowMove("Backup gateway", msgs.ProviderMoveUp())
		w.wait(func() bool {
			return !w.app.providerBusy() && w.app.productHost().productProviders.Accounts[0].ProviderID == backupID
		})
		applyChanges(t, w)

		clickScrolled(t, w, msgs.ProviderTabRouting())
		clickScrolled(t, w, msgs.ProviderQuality())
		w.wait(func() bool {
			return !w.app.providerBusy() && w.app.productHost().productProviders.Policy == pi.ProductRoutingQuality
		})
		pinRole(t, w, msgs.ProviderRoleUtility(), msgs.ProviderModelPinOption("openai/gpt-4o"), "utility", "model")
		pinRole(t, w, msgs.ProviderRoleDialogue(), "Studio gateway · gpt-4o", "dialogue", "route")
		applyChanges(t, w)
		pinRole(t, w, msgs.ProviderRoleUtility(), msgs.ProviderFollowStrategy(), "utility", "")
		applyChanges(t, w)
		pinRole(t, w, msgs.ProviderRoleUtility(), msgs.ProviderModelPinOption("openai/gpt-4o"), "utility", "model")
		applyChanges(t, w)

		w.shutdown(ctx)

		w = openProductWorkbench(t)
		reopenedState, err := w.client.ListProviders(ctx)
		if err != nil || len(reopenedState.Accounts) != 2 || reopenedState.Accounts[0].ProviderID != backupID || reopenedState.Accounts[1].ProviderID != primaryID {
			t.Fatalf("reopen lost saved priority order: %+v err=%v", reopenedState, err)
		}
		if row := reopenedState.Accounts[0]; row.EnabledModels == nil || !slices.Equal(*row.EnabledModels, []string{"gpt-4o"}) || row.FallbackOnly == nil || !*row.FallbackOnly {
			t.Fatalf("reopen lost EnabledModels or FallbackOnly: %+v", row)
		}
		if reopenedState.EffectiveOverrides["dialogue"].ProviderID != primaryID || reopenedState.EffectiveOverrides["utility"].CanonicalModelID != "openai/gpt-4o" {
			t.Fatalf("reopen lost effective routing overrides: %+v", reopenedState.EffectiveOverrides)
		}

		w.frame(func() { w.app.closeSettings() })
		var cedar *contact
		w.pump.do(func() { cedar = w.app.contactByID("cedar") })
		rateLimited.Store(true)
		typeInput(t, w, msgs.ComposerLabel("Cedar"), "Trigger the synthetic cooldown")
		clickScrolled(t, w, msgs.SendAction())
		w.wait(func() bool {
			return w.app.productHost().productProviders.RoleStatus["dialogue"].Status == "cooldown" && cedar.SendState == sendIdle
		})
		beforeCooldown := completionsCount.Load()
		if beforeCooldown == 0 {
			t.Fatal("expected cooldown-inducing request count > 0")
		}
		w.wait(func() bool { return cedar.Loaded && w.view.HasText("Trigger the synthetic cooldown") })
		var quoteErr error
		w.frame(func() {
			quoteErr = w.view.RightClick("Trigger the synthetic cooldown")
			if quoteErr == nil {
				quoteErr = choosePopoverMenuItem(w.view, msgs.QuoteAction())
			}
		})
		if quoteErr != nil {
			t.Fatal(quoteErr)
		}
		w.wait(func() bool { return cedar.DraftQuote != nil })
		typeInput(t, w, msgs.ComposerLabel("Cedar"), "Keep this draft while the route cools down")
		clickScrolled(t, w, msgs.SendAction())
		w.wait(func() bool { return cedar.SendErrorKind == sendErrorRouteUnavailable })
		w.frame(func() {
			if cedar.SendState != sendIdle || cedar.SendErrorDetail == "" || cedar.Draft != "Keep this draft while the route cools down" ||
				cedar.DraftQuote == nil || cedar.DraftQuote.Text != "Trigger the synthetic cooldown" || completionsCount.Load() != beforeCooldown {
				t.Errorf("non-dispatched route_unavailable lost draft/quote/retry or contacted provider: state=%v detail=%q draft=%q calls=%d/%d",
					cedar.SendState, cedar.SendErrorDetail, cedar.Draft, completionsCount.Load(), beforeCooldown)
			}
		})

		clickScrolled(t, w, msgs.ConnectionRecoveryOpenSettings())
		clickScrolled(t, w, msgs.ProviderTabRouting())
		pinRole(t, w, msgs.ProviderRoleDialogue(), msgs.ProviderModelPinOption("openai/gpt-4o"), "dialogue", "model")
		applyChanges(t, w)
		w.wait(func() bool {
			route := w.app.productHost().productProviders.ResolvedRoles.Dialogue
			return route != nil && route.ProviderID == backupID && route.Source == "pinned_model" &&
				route.Failover == "same_model_cooldown" && w.view.HasText(msgs.ProviderSameModelFailover())
		})

		pinRole(t, w, msgs.ProviderRoleDialogue(), "Studio gateway · gpt-4o", "dialogue", "route")
		applyChanges(t, w)
		clickScrolled(t, w, msgs.ProviderTabAccounts(2))
		clickScrolled(t, w, "Studio gateway")
		clickScrolled(t, w, msgs.ProviderDeleteAccount("Studio gateway"))
		w.wait(func() bool { return !w.app.providerBusy() && len(w.app.productHost().productProviders.Accounts) == 1 })
		applyChanges(t, w)
		clickScrolled(t, w, msgs.ProviderTabRouting())
		w.wait(func() bool {
			return w.app.productHost().productProviders.RoleStatus["dialogue"].UnavailableReason == "provider_deleted" &&
				w.view.HasText(msgs.ProviderPinnedUnavailable())
		})
		pinRole(t, w, msgs.ProviderRoleDialogue(), msgs.ProviderFollowStrategy(), "dialogue", "")
		applyChanges(t, w)

		w.shutdown(ctx)
		if _, err := os.Stat(filepath.Join(root, "offsite.log")); !os.IsNotExist(err) {
			t.Fatalf("offline RT-1/RT-2 test attempted external fetch: %v", err)
		}
		if modelsCount.Load() < 2 {
			t.Fatalf("expected >= 2 local /models discovery probes, got %d", modelsCount.Load())
		}
	})
}
