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
	root := t.TempDir()
	isolateConnectionApp(t, root, payload)

	var (
		mu             sync.Mutex
		modelsRequests int
		chatRequests   int
		chatModels     []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			mu.Lock()
			modelsRequests++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"},{"id":"codex-flagship"},{"id":"deepseek-r1-pro"},{"id":"tiny-utility"}]}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		chatRequests++
		chatModels = append(chatModels, req.Model)
		mu.Unlock()

		var delta map[string]any
		finish := "stop"
		if strings.Contains(string(body), activityGoal) || strings.Contains(string(body), "Start paper check") {
			args, _ := json.Marshal(map[string]any{"text": questionText})
			delta = map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"index": 0, "id": "call-ask-paper", "type": "function",
					"function": map[string]string{"name": "activity_ask", "arguments": string(args)},
				}},
			}
			finish = "tool_calls"
		} else {
			delta = map[string]any{"role": "assistant", "content": chatReply}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []map[string]any{
			{"index": 0, "delta": delta, "finish_reason": nil},
			{"index": 0, "delta": map[string]any{}, "finish_reason": finish},
		} {
			chunk, _ := json.Marshal(map[string]any{
				"id": "prov-loopback", "object": "chat.completion.chunk", "created": 1,
				"model": req.Model, "choices": []any{choice},
			})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

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

	w.click("DeepSeek")
	w.frame(func() {
		ed := w.app.providers.editor
		ed.Name = "Studio gateway"
		ed.BaseURL = server.URL + "/v1"
		ed.APIKey = apiKey
	})
	w.click(msgs.ProviderTestDiscover())
	w.wait(func() bool {
		return !w.app.providerBusy() && w.app.providers.editor != nil && w.app.providers.editor.tested && len(w.app.providers.editor.Models) == 4
	})

	mu.Lock()
	gotModelsProbe := modelsRequests
	mu.Unlock()
	if gotModelsProbe != 1 {
		t.Fatalf("expected 1 /v1/models discovery request, got %d", gotModelsProbe)
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
			pool.ResolvedRoles.Coding != nil && pool.ResolvedRoles.Coding.ResolvedModel == "codex-flagship" &&
			pool.ResolvedRoles.Reasoning != nil && pool.ResolvedRoles.Reasoning.ResolvedModel == "deepseek-r1-pro" &&
			pool.ResolvedRoles.Utility != nil && pool.ResolvedRoles.Utility.ResolvedModel == "tiny-utility"
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
	mu.Unlock()
	if chatsBeforeRevoke < 2 {
		t.Fatalf("expected provider-only chat + activity requests >= 2, got %d", chatsBeforeRevoke)
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

	privateCfg, err := connection.ReadPrivateConfig(connectionConfigPath(guiStorePath()))
	if err != nil || privateCfg.Version != 3 || len(privateCfg.Accounts) == 0 || privateCfg.CredentialSource != "" {
		t.Fatalf("v3 provider-only config on disk mismatch: %+v err=%v", privateCfg, err)
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
