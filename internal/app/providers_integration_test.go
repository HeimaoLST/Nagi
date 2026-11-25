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

	w.click(msgs.ProviderAdd())
	w.wait(func() bool { return w.app.providers.adding && w.app.providers.editor != nil })

	w.click("OpenAI")
	w.frame(func() {
		ed := w.app.providers.editor
		ed.Name = "Studio gateway"
		ed.APIKey = apiKey
	})
	w.click(msgs.ProviderTestDiscover())
	w.wait(func() bool {
		return !w.app.providerBusy() && w.app.providers.editor != nil && w.app.providers.editor.tested && len(w.app.providers.editor.Models) == 128
	})

	mu.Lock()
	gotModelsProbe := modelsRequests
	mu.Unlock()
	if gotModelsProbe != 1 {
		t.Fatalf("expected 1 /v1/models discovery request, got %d", gotModelsProbe)
	}
	var editorModels []string
	w.pump.do(func() {
		editorModels = slices.Clone(w.app.providers.editor.Models)
	})
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

	w.click("gpt-4o")
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
	w.pump.do(func() {
		pool := w.app.productHost().productProviders
		if len(pool.Accounts) != 1 || len(pool.Accounts[0].Models) != 128 || pool.Accounts[0].EnabledModels != nil {
			t.Fatalf("unchanged editor saved unexpected pool metadata: %+v", pool.Accounts)
		}
		if pool.ResolvedRoles.Dialogue == nil || pool.ResolvedRoles.Dialogue.ResolvedModel != "gpt-4o" ||
			pool.ResolvedRoles.Coding == nil || pool.ResolvedRoles.Coding.ResolvedModel != "gpt-5.4-mini" ||
			pool.ResolvedRoles.Reasoning == nil || pool.ResolvedRoles.Reasoning.ResolvedModel != "o3" ||
			pool.ResolvedRoles.Utility == nil || pool.ResolvedRoles.Utility.ResolvedModel != "gpt-5.4-nano" {
			t.Fatalf("safe default balanced pool mismatch: %+v", pool.ResolvedRoles)
		}
	})

	w.click(msgs.ProviderAddShort())
	w.wait(func() bool { return w.app.providers.adding })
	w.click("Ollama")
	w.frame(func() {
		ed := w.app.providers.editor
		ed.Enabled = false
		ed.APIKey = "local-ollama-token"
	})
	w.click(msgs.ProviderSave())
	w.wait(func() bool {
		return !w.app.providerBusy() && !w.app.providers.adding && len(w.app.productHost().productProviders.Accounts) == 2 && w.app.productProvidersPending()
	})
	w.click(msgs.ProviderApplyChanges())
	w.wait(func() bool { return !w.app.providerBusy() && !w.app.productProvidersPending() })

	w.click("Studio gateway")
	w.wait(func() bool { return w.app.providers.editor != nil && w.app.providers.editor.Name == "Studio gateway" })

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
	w.wait(func() bool { return w.app.settingsOpen && w.view.HasText(msgs.ProviderRevokeAccount("Studio gateway")) })
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
