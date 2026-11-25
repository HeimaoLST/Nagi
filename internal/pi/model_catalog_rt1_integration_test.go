//go:build integration

package pi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
)

type rt1WireRequest struct {
	Path            string
	Model           string
	UserText        string
	MaxOutputTokens int
	ReasoningEffort string
	ThinkingType    string
	HasReasoning    bool
	HasThinking     bool
	IsDiscovery     bool
}

type rt1Harness struct {
	t          *testing.T
	secret     string
	server     *httptest.Server
	offsiteLog string
	mu         sync.Mutex
	discovery  map[string][]string
	delegates  map[string]map[string]any
	requests   []rt1WireRequest
	problems   []string
	seq        int
}

func newRT1Harness(t *testing.T) *rt1Harness {
	t.Helper()
	h := &rt1Harness{
		t:         t,
		secret:    "rt1-synthetic-loopback-auth-77319",
		discovery: make(map[string][]string),
		delegates: make(map[string]map[string]any),
	}
	h.server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.server.Close)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.offsiteLog = filepath.Join(dir, "offsite.log")
	preloadPath := filepath.Join(dir, "rt1-offline-preload.mjs")
	preload := fmt.Sprintf(`import { appendFileSync } from 'node:fs';
const orig = globalThis.fetch;
const mapped = new Set([
  'api.openai.com', 'openrouter.ai', 'api.deepseek.com',
  'api.minimax.io', 'open.bigmodel.cn', 'dashscope.aliyuncs.com', 'api.x.ai', 'api.moonshot.cn',
]);
const targetBase = %q;
const offsiteLog = %q;
globalThis.fetch = (input, init) => {
  const url = new URL(input instanceof Request ? input.url : String(input));
  if (url.hostname === '127.0.0.1' || url.hostname === 'localhost') return orig(input, init);
  if (mapped.has(url.hostname)) {
    const dest = new URL(targetBase + '/' + url.hostname + url.pathname + url.search);
    return orig(input instanceof Request ? new Request(dest, input) : dest, init);
  }
  appendFileSync(offsiteLog, url.origin + '\n');
  throw new Error('Network disabled outside controlled loopback: ' + url.origin);
};
`, h.server.URL, h.offsiteLog)
	if err := os.WriteFile(preloadPath, []byte(preload), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODE_OPTIONS", "--import "+preloadPath)
	return h
}

func (h *rt1Harness) setDiscovery(path string, ids []string) {
	h.mu.Lock()
	h.discovery[path] = append([]string(nil), ids...)
	h.mu.Unlock()
}

func (h *rt1Harness) setDelegate(trigger string, args map[string]any) {
	h.mu.Lock()
	h.delegates[trigger] = args
	h.mu.Unlock()
}

func (h *rt1Harness) requestCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

func (h *rt1Harness) exactMatching(text string) (rt1WireRequest, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.requests) - 1; i >= 0; i-- {
		if h.requests[i].UserText == text {
			return h.requests[i], true
		}
	}
	return rt1WireRequest{}, false
}

func (h *rt1Harness) lastMatching(needle string) (rt1WireRequest, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.requests) - 1; i >= 0; i-- {
		if strings.Contains(h.requests[i].UserText, needle) {
			return h.requests[i], true
		}
	}
	return rt1WireRequest{}, false
}

func (h *rt1Harness) assertClean() {
	h.t.Helper()
	if data, err := os.ReadFile(h.offsiteLog); err == nil && len(data) > 0 {
		h.t.Fatalf("unexpected offsite network attempts: %s", string(data))
	}
	h.mu.Lock()
	probs := append([]string(nil), h.problems...)
	h.mu.Unlock()
	if len(probs) > 0 {
		h.t.Fatalf("RT-1 harness problems: %v", probs)
	}
}

func (h *rt1Harness) recordProblem(msg string) {
	h.mu.Lock()
	h.problems = append(h.problems, msg)
	h.mu.Unlock()
}

func rt1ExtractUserText(rawMessages []json.RawMessage) (userText, lastRole string) {
	for _, raw := range rawMessages {
		var msg struct {
			Role    string          `json:"role"`
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		role := msg.Role
		if role == "" {
			role = msg.Type
		}
		if role != "" {
			lastRole = role
		}
		if msg.Role == "user" {
			text := decodeChatText(msg.Content)
			if idx := strings.Index(text, "\n\n<active_activities>"); idx >= 0 {
				text = text[:idx]
			}
			userText = text
		}
	}
	return userText, lastRole
}

func (h *rt1Harness) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+h.secret {
		h.recordProblem("auth mismatch on " + r.URL.Path)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
		h.mu.Lock()
		ids := append([]string(nil), h.discovery[r.URL.Path]...)
		h.requests = append(h.requests, rt1WireRequest{Path: r.URL.Path, IsDiscovery: true})
		h.mu.Unlock()
		rows := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, map[string]string{"id": id})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": rows})
		return
	}
	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		h.recordProblem("read body: " + err.Error())
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	isResponses := strings.HasSuffix(r.URL.Path, "/responses")
	if !isResponses && !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		h.recordProblem("unexpected path: " + r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var parsed struct {
		Model           string            `json:"model"`
		MaxOutputTokens int               `json:"max_output_tokens"`
		ReasoningEffort string            `json:"reasoning_effort"`
		Reasoning       json.RawMessage   `json:"reasoning"`
		Thinking        json.RawMessage   `json:"thinking"`
		Messages        []json.RawMessage `json:"messages"`
		Input           []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		h.recordProblem("decode request: " + err.Error())
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	msgs := parsed.Messages
	if isResponses {
		msgs = parsed.Input
	}
	userText, lastRole := rt1ExtractUserText(msgs)
	effort := parsed.ReasoningEffort
	hasReasoning := len(parsed.Reasoning) > 0 && string(parsed.Reasoning) != "null"
	if hasReasoning {
		var rObj struct {
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal(parsed.Reasoning, &rObj); err == nil && rObj.Effort != "" {
			effort = rObj.Effort
		}
	}
	hasThinking := len(parsed.Thinking) > 0 && string(parsed.Thinking) != "null"
	var thinkingType string
	if hasThinking {
		var tObj struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(parsed.Thinking, &tObj); err == nil {
			thinkingType = tObj.Type
		}
	}
	h.mu.Lock()
	h.seq++
	seq := h.seq
	h.requests = append(h.requests, rt1WireRequest{
		Path:            r.URL.Path,
		Model:           parsed.Model,
		UserText:        userText,
		MaxOutputTokens: parsed.MaxOutputTokens,
		ReasoningEffort: effort,
		ThinkingType:    thinkingType,
		HasReasoning:    hasReasoning,
		HasThinking:     hasThinking,
	})
	delegateArgs := h.delegates[userText]
	h.mu.Unlock()

	var toolArgs string
	if delegateArgs != nil && lastRole == "user" {
		raw, _ := json.Marshal(delegateArgs)
		toolArgs = string(raw)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if isResponses {
		var item map[string]any
		if toolArgs != "" {
			item = map[string]any{
				"type":      "function_call",
				"id":        fmt.Sprintf("fc_%d", seq),
				"call_id":   fmt.Sprintf("call_%d", seq),
				"name":      "activity_delegate",
				"arguments": toolArgs,
				"status":    "completed",
			}
		} else {
			item = map[string]any{
				"type":    "message",
				"id":      fmt.Sprintf("msg_%d", seq),
				"role":    "assistant",
				"status":  "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "RT1 fixture complete.", "annotations": []any{}}},
			}
		}
		send := func(kind string, fields map[string]any) {
			fields["type"] = kind
			raw, _ := json.Marshal(fields)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
		}
		send("response.created", map[string]any{"response": map[string]any{"id": fmt.Sprintf("resp_%d", seq), "status": "in_progress"}})
		send("response.output_item.done", map[string]any{"output_index": 0, "item": item})
		send("response.completed", map[string]any{"response": map[string]any{
			"id": fmt.Sprintf("resp_%d", seq), "status": "completed", "output": []any{item},
			"usage": map[string]any{"input_tokens": 20, "output_tokens": 10, "total_tokens": 30},
		}})
		return
	}
	if toolArgs != "" {
		piChatResponse(w, "", "activity_delegate", fmt.Sprintf("call_%d", seq), toolArgs)
		return
	}
	piChatResponse(w, "RT1 fixture complete.", "", "", "")
}

func rt1BoolPtr(v bool) *bool { return &v }

func rt1IntPtr(v int) *int { return &v }

func rt1SlicePtr(ids ...string) *[]string {
	out := make([]string, len(ids))
	copy(out, ids)
	return &out
}

func rt1OverridesPtr(rows ...pi.ProductModelOverride) *[]pi.ProductModelOverride {
	out := make([]pi.ProductModelOverride, len(rows))
	copy(out, rows)
	return &out
}

func rt1Account(providerID, baseURL string, wire pi.WireAPI, defaultModel string) pi.ProductProviderAccount {
	return pi.ProductProviderAccount{
		ProviderID:    providerID,
		Name:          "Fixture " + providerID,
		WireAPI:       wire,
		BaseURL:       baseURL,
		DefaultModel:  defaultModel,
		ContextTokens: 1000000,
		OutputTokens:  150000,
		Enabled:       true,
	}
}

func rt1SaveApply(t *testing.T, ctx context.Context, proc *productProcess, acc pi.ProductProviderAccount, apiKey, opPrefix string) pi.ProductConnectionRevisions {
	t.Helper()
	saved, err := proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: acc, APIKey: apiKey},
		OperationID:               opPrefix + "-save",
	})
	if err != nil || saved.SavedRevision == nil {
		t.Fatalf("SaveProvider(%s) = %+v, %v", acc.ProviderID, saved, err)
	}
	applied, err := proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		SavedRevision: *saved.SavedRevision,
		OperationID:   opPrefix + "-apply",
	})
	if err != nil {
		t.Fatalf("ApplyProviders(%s) = %+v, %v", acc.ProviderID, applied, err)
	}
	return applied
}

func rt1SendAndWait(t *testing.T, ctx context.Context, proc *productProcess, evLog *routingEventLog, bot pi.ProductBot, text, opID string) {
	t.Helper()
	execID := mcpSendTurn(t, ctx, proc, bot, text, opID)
	if s := evLog.waitSettled(t, ctx, proc, execID); s.State != "completed" {
		t.Fatalf("turn %q settled = %+v", text, s)
	}
}

func rt1DelegateAndWait(t *testing.T, ctx context.Context, proc *productProcess, evLog *routingEventLog, h *rt1Harness,
	origin, worker pi.ProductBot, marker, kind, complexity, opID string) rt1WireRequest {
	t.Helper()
	trigger := "RT1_DELEGATE_" + marker
	h.setDelegate(trigger, map[string]any{
		"goal": marker, "key": marker, "assignee_id": worker.BotID,
		"kind": kind, "complexity": complexity,
	})
	rt1SendAndWait(t, ctx, proc, evLog, origin, trigger, opID)
	var actID string
	mcpWaitUntil(t, 10*time.Second, "delegated "+marker, func() bool {
		page, err := proc.client.ListActivities(ctx, pi.ProductActivityListParams{BotID: worker.BotID})
		if err != nil {
			return false
		}
		for _, item := range page.Items {
			if item.Goal == marker {
				actID = item.ActivityID
				return true
			}
		}
		return false
	})
	act, err := proc.client.GetActivity(ctx, actID)
	if err != nil || len(act.Executions) == 0 {
		t.Fatalf("GetActivity(%s) = %+v, %v", marker, act, err)
	}
	if s := evLog.waitSettled(t, ctx, proc, act.Executions[len(act.Executions)-1].ExecutionID); s.State != "completed" {
		t.Fatalf("activity %s settled = %+v", marker, s)
	}
	req, ok := h.exactMatching(marker)
	if !ok {
		t.Fatalf("missing wire request for %s", marker)
	}
	return req
}

func TestPiProductRT1OfflineCatalogProvenanceAndDualWireDispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	nodePath, workerPath := mcpRequirePinnedWorker(t)
	payloadDir := filepath.Dir(filepath.Dir(workerPath))

	var manifest struct {
		Catalog struct {
			Path   string            `json:"path"`
			SHA256 string            `json:"sha256"`
			Inputs map[string]string `json:"inputs"`
		} `json:"catalog"`
		Files map[string]any `json:"files"`
	}
	if err := json.Unmarshal(productRead(t, filepath.Join(payloadDir, "manifest.json")), &manifest); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(payloadDir, manifest.Catalog.Path)
	catalogRaw := productRead(t, catalogPath)
	catSum := sha256.Sum256(catalogRaw)
	if hex.EncodeToString(catSum[:]) != manifest.Catalog.SHA256 {
		t.Fatalf("packaged catalog SHA-256 mismatch")
	}
	if _, ok := manifest.Files["licenses/models.dev-MIT.txt"]; !ok {
		t.Fatal("packaged manifest missing licenses/models.dev-MIT.txt")
	}
	if !bytes.Contains(productRead(t, filepath.Join(payloadDir, "licenses", "models.dev-MIT.txt")), []byte("MIT License")) {
		t.Fatal("packaged models.dev-MIT.txt missing MIT notice")
	}
	var catalog struct {
		Schema       int    `json:"schema"`
		RankRevision int    `json:"rankRevision"`
		Digest       string `json:"digest"`
		ModelsDev    struct {
			Commit  string `json:"commit"`
			License string `json:"license"`
		} `json:"modelsDev"`
		Models     []any `json:"models"`
		Supplement []any `json:"supplement"`
	}
	if err := json.Unmarshal(catalogRaw, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Schema != 1 || catalog.RankRevision != 1 ||
		catalog.ModelsDev.Commit != "066eb88a4dbbe15d5274ad5528070ce5112bbe18" ||
		catalog.ModelsDev.License != "MIT" || len(catalog.Models) < 1600 || len(catalog.Supplement) < 2000 {
		t.Fatalf("unexpected catalog provenance metadata: schema=%d rank=%d commit=%s models=%d supp=%d",
			catalog.Schema, catalog.RankRevision, catalog.ModelsDev.Commit, len(catalog.Models), len(catalog.Supplement))
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newRT1Harness(t)

	offlineDir := filepath.Join(root, "offline")
	if err := os.MkdirAll(offlineDir, 0o700); err != nil {
		t.Fatal(err)
	}
	offlineOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(offlineDir, "pi"),
		ProfilePath:     filepath.Join(offlineDir, "contacts.json"),
		CredentialsPath: filepath.Join(offlineDir, "connection.json"),
		FreshProfile:    true,
	}
	offProc := startProductProcess(t, offlineOpts)
	_ = offProc.open(t, ctx)
	if _, err := offProc.client.Routing(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := offProc.client.ListProviders(ctx); err != nil {
		t.Fatal(err)
	}
	offProc.shutdown(t, ctx)
	if h.requestCount() != 0 {
		t.Fatalf("offline startup made %d provider requests, want 0", h.requestCount())
	}

	dir := filepath.Join(root, "dispatch")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(dir, "pi"),
		ProfilePath:     filepath.Join(dir, "contacts.json"),
		CredentialsPath: filepath.Join(dir, "connection.json"),
		FreshProfile:    true,
	}
	proc := startProductProcess(t, opts)
	_ = proc.open(t, ctx)
	evLog := newRoutingEventLog()

	advertised := []string{
		"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-5.4-pro", "gpt-5.5-pro",
		"o1-pro", "o3-pro", "gpt-4o", "gpt-4o-2024-08-06", "text-embedding-3-small",
	}
	for i := 0; i < 212; i++ {
		advertised = append(advertised, fmt.Sprintf("zz-unknown-%03d", i))
	}
	slices.Reverse(advertised)
	h.setDiscovery("/api.openai.com/v1/models", advertised)
	h.setDiscovery("/openrouter.ai/api/v1/models", []string{
		"openai/gpt-4o", "openai/gpt-5", "openai/gpt-5.4",
		"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro-0813",
		"google/gemma-4-31b-it", "google/gemma-4-31b-it:free",
	})
	h.setDiscovery("/api.minimax.io/v1/models", []string{"MiniMax-M2.5", "MiniMax-M2.7"})
	h.setDiscovery("/open.bigmodel.cn/api/paas/v4/models", []string{"glm-4.7", "glm-5.2"})
	h.setDiscovery("/dashscope.aliyuncs.com/compatible-mode/v1/models", []string{"qwen3.5-plus"})
	h.setDiscovery("/priv/v1/models", []string{"gpt-5.4", "default", "qwen3:8b", "qwen3:32b"})

	directAcc := rt1Account("direct", "https://api.openai.com/v1", pi.OpenAIResponses, "gpt-5.4")
	directTest, err := proc.client.TestProvider(ctx, pi.ProductProviderTestParams{Account: directAcc, APIKey: h.secret})
	if err != nil || !directTest.OK || len(directTest.Models) != 128 || len(directTest.DiscoveredModels) != 128 {
		t.Fatalf("direct TestProvider = %+v, %v", directTest, err)
	}
	if !directTest.DiscoveredModels[0].Recommended {
		t.Fatalf("expected recommended model ranked first: %+v", directTest.DiscoveredModels[0])
	}
	byID := func(rows []pi.ProductDiscoveredModel) map[string]pi.ProductDiscoveredModel {
		out := make(map[string]pi.ProductDiscoveredModel, len(rows))
		for _, row := range rows {
			out[row.ID] = row
		}
		return out
	}
	dm := byID(directTest.DiscoveredModels)
	if dm["text-embedding-3-small"].IsChat == nil || *dm["text-embedding-3-small"].IsChat {
		t.Fatalf("embedding must have isChat=false: %+v", dm["text-embedding-3-small"])
	}
	if dm["zz-unknown-000"].IsChat != nil || dm["zz-unknown-000"].ToolCall != nil {
		t.Fatalf("unknown ID must have nullable isChat/toolCall: %+v", dm["zz-unknown-000"])
	}
	if dm["gpt-5.4"].ContextTokens != 272000 || dm["gpt-5.4"].OutputTokens != 128000 {
		t.Fatalf("direct gpt-5.4 limits mismatch: %+v", dm["gpt-5.4"])
	}
	if dm["gpt-5.4"].CanonicalID == dm["gpt-5.4-mini"].CanonicalID || dm["gpt-5.4"].CanonicalID == dm["gpt-5.4-pro"].CanonicalID {
		t.Fatalf("GPT prefix variants conflated: %+v / %+v / %+v", dm["gpt-5.4"], dm["gpt-5.4-mini"], dm["gpt-5.4-pro"])
	}
	if dm["gpt-4o"].CanonicalID == dm["gpt-4o-2024-08-06"].CanonicalID {
		t.Fatalf("moving vs dated gpt-4o conflated: %+v / %+v", dm["gpt-4o"], dm["gpt-4o-2024-08-06"])
	}
	for _, proID := range []string{"gpt-5.4-pro", "gpt-5.5-pro", "o3-pro"} {
		row := dm[proID]
		if row.IsChat == nil || !*row.IsChat || row.ToolCall == nil || !*row.ToolCall || !row.PriceProtected || row.Recommended {
			t.Fatalf("OpenAI Responses Pro SKU %s regression: %+v", proID, row)
		}
	}
	directComp := rt1Account("directComp", "https://api.openai.com/v1", pi.OpenAICompletions, "gpt-5.4")
	directCompTest, err := proc.client.TestProvider(ctx, pi.ProductProviderTestParams{Account: directComp, APIKey: h.secret})
	if err != nil || !directCompTest.OK {
		t.Fatalf("directComp TestProvider = %+v, %v", directCompTest, err)
	}
	dcm := byID(directCompTest.DiscoveredModels)
	for _, proID := range []string{"gpt-5.4-pro", "gpt-5.5-pro", "o3-pro"} {
		if dcm[proID].IsChat == nil || *dcm[proID].IsChat {
			t.Fatalf("Responses-only Pro SKU %s should be isChat=false on Completions: %+v", proID, dcm[proID])
		}
	}

	relayAcc := rt1Account("relay", "https://openrouter.ai/api/v1", pi.OpenAICompletions, "openai/gpt-5.4")
	relayTest, err := proc.client.TestProvider(ctx, pi.ProductProviderTestParams{Account: relayAcc, APIKey: h.secret})
	if err != nil || !relayTest.OK {
		t.Fatalf("relay TestProvider = %+v, %v", relayTest, err)
	}
	rm := byID(relayTest.DiscoveredModels)
	if rm["openai/gpt-5.4"].CanonicalID != dm["gpt-5.4"].CanonicalID || rm["openai/gpt-5.4"].ContextTokens != 1000000 {
		t.Fatalf("relay gpt-5.4 identity/context mismatch: %+v", rm["openai/gpt-5.4"])
	}
	for _, aliasID := range []string{"openai/gpt-4o", "openai/gpt-5"} {
		if rm[aliasID].ToolCall == nil || !*rm[aliasID].ToolCall || !rm[aliasID].Recommended || rm[aliasID].CanonicalID != aliasID {
			t.Fatalf("OpenRouter alias %s toolCall/recommended regression: %+v", aliasID, rm[aliasID])
		}
	}
	if rm["deepseek/deepseek-v4-pro"].CanonicalID == rm["deepseek/deepseek-v4-pro-0813"].CanonicalID {
		t.Fatalf("versioned DeepSeek conflated: %+v / %+v", rm["deepseek/deepseek-v4-pro"], rm["deepseek/deepseek-v4-pro-0813"])
	}
	if rm["google/gemma-4-31b-it"].CanonicalID != rm["google/gemma-4-31b-it:free"].CanonicalID || rm["google/gemma-4-31b-it:free"].Tier != "free" {
		t.Fatalf("OpenRouter free/paid sibling mismatch: %+v / %+v", rm["google/gemma-4-31b-it"], rm["google/gemma-4-31b-it:free"])
	}

	for _, preset := range []struct {
		id, url, model string
	}{
		{"minimax", "https://api.minimax.io/v1", "MiniMax-M2.5"},
		{"zhipuai", "https://open.bigmodel.cn/api/paas/v4", "glm-4.7"},
		{"alibaba", "https://dashscope.aliyuncs.com/compatible-mode/v1", "qwen3.5-plus"},
	} {
		tr, err := proc.client.TestProvider(ctx, pi.ProductProviderTestParams{
			Account: rt1Account(preset.id, preset.url, pi.OpenAICompletions, preset.model),
			APIKey:  h.secret,
		})
		if err != nil || !tr.OK {
			t.Fatalf("preset %s TestProvider = %+v, %v", preset.id, tr, err)
		}
		pm := byID(tr.DiscoveredModels)[preset.model]
		if pm.ToolCall == nil || !*pm.ToolCall || !pm.Recommended {
			t.Fatalf("preset %s model %s expected toolCall=true recommended=true: %+v", preset.id, preset.model, pm)
		}
	}

	privAcc := rt1Account("private", h.server.URL+"/priv/v1", pi.OpenAICompletions, "gpt-5.4")
	privTest, err := proc.client.TestProvider(ctx, pi.ProductProviderTestParams{Account: privAcc, APIKey: h.secret})
	if err != nil || !privTest.OK {
		t.Fatalf("private TestProvider = %+v, %v", privTest, err)
	}
	pvm := byID(privTest.DiscoveredModels)
	if pvm["gpt-5.4"].CanonicalID != "custom:7:private:gpt-5.4" || pvm["gpt-5.4"].Reasoning || pvm["gpt-5.4"].ContextTokens != 1000000 {
		t.Fatalf("private proxy leaked vendor identity/capabilities: %+v", pvm["gpt-5.4"])
	}
	if pvm["qwen3:8b"].CanonicalID == pvm["qwen3:32b"].CanonicalID {
		t.Fatalf("Ollama size tags conflated: %+v / %+v", pvm["qwen3:8b"], pvm["qwen3:32b"])
	}

	rt1SaveApply(t, ctx, proc, directAcc, h.secret, "op-direct")
	exactBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-exact-bot",
		Profile:     pi.ProductBotProfile{Name: "Exact", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt1SendAndWait(t, ctx, proc, evLog, exactBot, "RT1_RESPONSES_TURN1", "op-resp-1")
	r1, ok := h.lastMatching("RT1_RESPONSES_TURN1")
	if !ok || r1.Path != "/api.openai.com/v1/responses" || r1.Model != "gpt-5.4" || r1.ReasoningEffort != "low" || r1.MaxOutputTokens != 128000 {
		t.Fatalf("direct Responses turn1 mismatch: %+v ok=%v", r1, ok)
	}
	var mirror struct {
		Conversations []struct {
			ConversationID string `json:"conversationId"`
			Binding        *struct {
				Route *struct {
					ContextTokens int `json:"contextTokens"`
					Capabilities  *struct {
						Reasoning        bool           `json:"reasoning"`
						Input            []string       `json:"input"`
						ThinkingLevelMap map[string]any `json:"thinkingLevelMap"`
						Cost             map[string]any `json:"cost"`
						Compat           map[string]any `json:"compat"`
					} `json:"capabilities"`
				} `json:"route"`
			} `json:"binding"`
		} `json:"conversations"`
	}
	if err := json.Unmarshal(productRead(t, opts.ProfilePath), &mirror); err != nil {
		t.Fatal(err)
	}
	checkedBinding := false
	for _, c := range mirror.Conversations {
		if c.ConversationID == exactBot.CurrentConversationID && c.Binding != nil && c.Binding.Route != nil && c.Binding.Route.Capabilities != nil {
			checkedBinding = true
			cap := c.Binding.Route.Capabilities
			if c.Binding.Route.ContextTokens != 272000 || !cap.Reasoning || !slices.Equal(cap.Input, []string{"text", "image"}) || cap.ThinkingLevelMap["off"] != "none" {
				t.Fatalf("captured route capabilities mismatch: %+v", c.Binding.Route)
			}
		}
	}
	if !checkedBinding {
		t.Fatal("missing captured route capabilities in contacts.json")
	}

	proc.shutdown(t, ctx)
	opts.FreshProfile = false
	proc = startProductProcess(t, opts)
	_ = proc.open(t, ctx)
	evLog = newRoutingEventLog()
	rt1SendAndWait(t, ctx, proc, evLog, exactBot, "RT1_RESPONSES_REOPEN", "op-resp-reopen")
	r2, ok := h.lastMatching("RT1_RESPONSES_REOPEN")
	if !ok || r2.Model != r1.Model || r2.ReasoningEffort != r1.ReasoningEffort || r2.MaxOutputTokens != r1.MaxOutputTokens {
		t.Fatalf("reopened Responses dispatch drifted: before=%+v after=%+v", r1, r2)
	}

	if _, err := proc.client.DeleteProvider(ctx, pi.ProductProviderTargetParams{ProviderID: "direct", OperationID: "op-del-direct"}); err != nil {
		t.Fatal(err)
	}
	rt1SaveApply(t, ctx, proc, relayAcc, h.secret, "op-relay")
	relayBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-relay-bot",
		Profile:     pi.ProductBotProfile{Name: "Relay", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt1SendAndWait(t, ctx, proc, evLog, relayBot, "RT1_RELAY_COMPLETIONS", "op-relay-turn")
	rr, ok := h.lastMatching("RT1_RELAY_COMPLETIONS")
	if !ok || rr.Path != "/openrouter.ai/api/v1/chat/completions" || rr.Model != "openai/gpt-5.4" || rr.ReasoningEffort != "low" {
		t.Fatalf("OpenRouter completions dispatch mismatch: %+v", rr)
	}

	dsPro := rt1Account("relay", "https://api.deepseek.com/v1", pi.OpenAICompletions, "deepseek-v4-pro")
	rt1SaveApply(t, ctx, proc, dsPro, h.secret, "op-ds-pro")
	dsRouting, err := proc.client.Routing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dsRouting.ResolvedRoles.Dialogue.ResolvedModel != "deepseek-v4-pro" ||
		dsRouting.ResolvedRoles.Coding.ResolvedModel != "deepseek-v4-pro" ||
		dsRouting.ResolvedRoles.Reasoning.ResolvedModel != "deepseek-v4-pro" ||
		dsRouting.ResolvedRoles.Utility.ResolvedModel != "deepseek-v4-pro" ||
		dsRouting.ResolvedRoles.Reasoning.ThinkingLevel != "off" ||
		dsRouting.ResolvedRoles.Dialogue.Reason != "single_model" {
		t.Fatalf("DeepSeek single-model collapse / lower-only clamp mismatch: %+v", dsRouting.ResolvedRoles)
	}
	dsOrigin, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-ds-origin",
		Profile:     pi.ProductBotProfile{Name: "DSOrigin", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dsWorker, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-ds-worker",
		Profile:     pi.ProductBotProfile{Name: "DSWorker", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dmReq := rt1DelegateAndWait(t, ctx, proc, evLog, h, dsOrigin, dsWorker, "RT1_DS_MEDIUM", "coding", "standard", "op-ds-med")
	if dmReq.Model != "deepseek-v4-pro" || dmReq.ThinkingType != "disabled" || dmReq.ReasoningEffort != "" {
		t.Fatalf("DeepSeek medium must clamp down to off/disabled: %+v", dmReq)
	}
	dhReq := rt1DelegateAndWait(t, ctx, proc, evLog, h, dsOrigin, dsWorker, "RT1_DS_HIGH", "coding", "deep", "op-ds-high")
	if dhReq.Model != "deepseek-v4-pro" || dhReq.ThinkingType != "enabled" || dhReq.ReasoningEffort != "high" {
		t.Fatalf("DeepSeek deep must dispatch enabled/high: %+v", dhReq)
	}

	dsFlash := rt1Account("relay", "https://api.deepseek.com/v1", pi.OpenAICompletions, "deepseek-v4-flash")
	rt1SaveApply(t, ctx, proc, dsFlash, h.secret, "op-ds-flash")
	dsFlashRouting, err := proc.client.Routing(ctx)
	if err != nil || dsFlashRouting.ResolvedRoles.Utility == nil || dsFlashRouting.ResolvedRoles.Utility.ThinkingLevel != "off" {
		t.Fatalf("deepseek-v4-flash toggle=true regression: %+v, %v", dsFlashRouting, err)
	}
	dsFlashBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-ds-flash-bot",
		Profile:     pi.ProductBotProfile{Name: "DSFlash", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt1SendAndWait(t, ctx, proc, evLog, dsFlashBot, "RT1_DS_FLASH", "op-ds-flash-turn")
	dfReq, ok := h.lastMatching("RT1_DS_FLASH")
	if !ok || dfReq.Model != "deepseek-v4-flash" || dfReq.ThinkingType != "enabled" || dfReq.ReasoningEffort != "low" {
		t.Fatalf("deepseek-v4-flash compat wire mismatch: %+v", dfReq)
	}

	dsResp := rt1Account("relay", "https://api.deepseek.com/v1", pi.OpenAIResponses, "deepseek-v4-pro")
	rt1SaveApply(t, ctx, proc, dsResp, h.secret, "op-ds-resp")
	dsRespRouting, err := proc.client.Routing(ctx)
	if err != nil || dsRespRouting.ResolvedRoles.Utility == nil || dsRespRouting.ResolvedRoles.Utility.ThinkingLevel != "off" {
		t.Fatalf("deepseek-v4-pro Responses off-level regression: %+v, %v", dsRespRouting, err)
	}

	fixedID := "deepseek/deepseek-v4-pro-0813"
	fixedAcc := rt1Account("relay", "https://openrouter.ai/api/v1", pi.OpenAICompletions, fixedID)
	fixedAcc.EnabledModels = rt1SlicePtr(fixedID)
	fixedAcc.ModelOverrides = rt1OverridesPtr(pi.ProductModelOverride{ID: fixedID, IsChat: rt1BoolPtr(true), ToolCall: rt1BoolPtr(true)})
	rt1SaveApply(t, ctx, proc, fixedAcc, h.secret, "op-fixed")
	fixedBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-fixed-bot",
		Profile:     pi.ProductBotProfile{Name: "Fixed", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt1SendAndWait(t, ctx, proc, evLog, fixedBot, "RT1_FIXED_DS", "op-fixed-turn")
	fxReq, ok := h.lastMatching("RT1_FIXED_DS")
	if !ok || fxReq.Model != fixedID || fxReq.ReasoningEffort != "low" {
		t.Fatalf("versioned DeepSeek exact wire mismatch: %+v", fxReq)
	}

	gpt4oAcc := rt1Account("relay", "https://api.openai.com/v1", pi.OpenAIResponses, "gpt-4o")
	rt1SaveApply(t, ctx, proc, gpt4oAcc, h.secret, "op-p3-gpt4o")
	gpt4oBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-gpt4o-bot",
		Profile:     pi.ProductBotProfile{Name: "GPT4o", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt1SendAndWait(t, ctx, proc, evLog, gpt4oBot, "RT1_P3_GPT4O", "op-p3-turn")
	if err := json.Unmarshal(productRead(t, opts.ProfilePath), &mirror); err != nil {
		t.Fatal(err)
	}
	checkedMidHistory := false
	for _, c := range mirror.Conversations {
		if c.ConversationID == gpt4oBot.CurrentConversationID && c.Binding != nil && c.Binding.Route != nil && c.Binding.Route.Capabilities != nil {
			checkedMidHistory = c.Binding.Route.Capabilities.Compat["supportsMidConvoSystemMessages"] == true
		}
	}
	if !checkedMidHistory {
		t.Fatal("gpt-4o captured route missing supportsMidConvoSystemMessages=true (P3-1 regression)")
	}

	incompat := rt1Account("relay", "https://api.openai.com/v1", pi.OpenAICompletions, "gpt-6.2-sol")
	incompat.EnabledModels = rt1SlicePtr("gpt-6.2-sol")
	incompat.ModelOverrides = rt1OverridesPtr(pi.ProductModelOverride{ID: "gpt-6.2-sol", IsChat: rt1BoolPtr(true), ToolCall: rt1BoolPtr(true)})
	rt1SaveApply(t, ctx, proc, incompat, h.secret, "op-incompat")
	if rt, err := proc.client.Routing(ctx); err != nil || rt.ResolvedRoles.Dialogue != nil {
		t.Fatalf("known-incompatible tool route must be ineligible: %+v, %v", rt, err)
	}
	blockedBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-blocked-bot",
		Profile:     pi.ProductBotProfile{Name: "Blocked", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	countBeforeBlock := h.requestCount()
	draftIncompat, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: blockedBot.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "RT1_BLOCK_INCOMPAT"},
		ExpectedRevision:          0,
		OperationID:               "op-draft-incompat",
	})
	if err != nil {
		t.Fatal(err)
	}
	recIncompat, err := proc.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: blockedBot.CurrentConversationID},
		DraftRevision:             draftIncompat.Revision,
		OperationID:               "op-send-incompat",
	})
	if err != nil || recIncompat.State != "rejected" {
		t.Fatalf("Send on known-incompatible tool route = %+v, %v", recIncompat, err)
	}
	var admIncompat pi.ProductRouteAdmission
	if err := json.Unmarshal(recIncompat.Result, &admIncompat); err != nil || admIncompat.Code != pi.ProductRouteUnavailable || admIncompat.Delivery != "non_dispatched" || admIncompat.Reason != "incompatible" {
		t.Fatalf("known-incompatible tool route admission = %s (%+v), err=%v", string(recIncompat.Result), admIncompat, err)
	}
	if h.requestCount() != countBeforeBlock {
		t.Fatalf("known-incompatible tool route dispatched %d requests", h.requestCount()-countBeforeBlock)
	}

	undeclared := rt1Account("relay", h.server.URL+"/priv/v1", pi.OpenAICompletions, "default")
	undeclared.EnabledModels = rt1SlicePtr("default", "text-embedding-3-small")
	undeclared.ModelOverrides = rt1OverridesPtr(pi.ProductModelOverride{ID: "text-embedding-3-small", IsChat: rt1BoolPtr(true), ToolCall: rt1BoolPtr(true)})
	rt1SaveApply(t, ctx, proc, undeclared, h.secret, "op-undeclared")
	recUndeclared, err := proc.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: blockedBot.CurrentConversationID},
		DraftRevision:             draftIncompat.Revision,
		OperationID:               "op-send-undeclared",
	})
	if err != nil || recUndeclared.State != "rejected" {
		t.Fatalf("Send on undeclared private route = %+v, %v", recUndeclared, err)
	}
	var admUndeclared pi.ProductRouteAdmission
	if err := json.Unmarshal(recUndeclared.Result, &admUndeclared); err != nil || admUndeclared.Code != pi.ProductRouteUnavailable || admUndeclared.Delivery != "non_dispatched" || admUndeclared.Reason != "incompatible" {
		t.Fatalf("undeclared private route admission = %s (%+v), err=%v", string(recUndeclared.Result), admUndeclared, err)
	}
	if h.requestCount() != countBeforeBlock {
		t.Fatalf("undeclared private route dispatched %d requests", h.requestCount()-countBeforeBlock)
	}

	proc.shutdown(t, ctx)
	h.assertClean()
}

func TestPiProductRT1ActivePoolProProtectionScoringAndReorder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	nodePath, workerPath := mcpRequirePinnedWorker(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newRT1Harness(t)

	startFresh := func(name string) (*productProcess, pi.ProductOptions, *routingEventLog) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		opts := pi.ProductOptions{
			NodePath:        nodePath,
			WorkerPath:      workerPath,
			StateDir:        filepath.Join(dir, "pi"),
			ProfilePath:     filepath.Join(dir, "contacts.json"),
			CredentialsPath: filepath.Join(dir, "connection.json"),
			FreshProfile:    true,
		}
		p := startProductProcess(t, opts)
		_ = p.open(t, ctx)
		return p, opts, newRoutingEventLog()
	}

	proProc, _, proEv := startFresh("pro-guard")
	for idx, proID := range []string{"gpt-5.4-pro", "GPT-5.4-PRO", "O1-PRO", "O3-Pro"} {
		privPro := rt1Account("pro", h.server.URL+"/pro/v1", pi.OpenAICompletions, proID)
		privPro.ModelOverrides = rt1OverridesPtr(pi.ProductModelOverride{ID: proID, IsChat: rt1BoolPtr(true), ToolCall: rt1BoolPtr(true)})
		rt1SaveApply(t, ctx, proProc, privPro, h.secret, fmt.Sprintf("op-pro-%d", idx))
		for _, pol := range []pi.ProductRoutingPolicy{pi.ProductRoutingBalanced, pi.ProductRoutingQuality, pi.ProductRoutingEconomy} {
			pr, err := proProc.client.SetRoutingPolicy(ctx, pi.ProductRoutingPolicyParams{Policy: pol, OperationID: fmt.Sprintf("op-pol-%d-%s", idx, pol)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := proProc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *pr.SavedRevision, OperationID: fmt.Sprintf("op-apol-%d-%s", idx, pol)}); err != nil {
				t.Fatal(err)
			}
			rt, err := proProc.client.Routing(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if rt.ResolvedRoles.Dialogue != nil || rt.ResolvedRoles.Coding != nil || rt.ResolvedRoles.Reasoning != nil || rt.ResolvedRoles.Utility != nil {
				t.Fatalf("solitary Pro SKU %q must stay blocked without opt-in under %s: %+v", proID, pol, rt.ResolvedRoles)
			}
		}
	}

	oaPro := rt1Account("pro", "https://api.openai.com/v1", pi.OpenAIResponses, "gpt-5.4-pro")
	rt1SaveApply(t, ctx, proProc, oaPro, h.secret, "op-oa-pro-default")
	if rt, err := proProc.client.Routing(ctx); err != nil || rt.ResolvedRoles.Coding != nil || rt.ResolvedRoles.Reasoning != nil {
		t.Fatalf("OpenAI Direct Pro SKU must stay blocked without opt-in: %+v, %v", rt, err)
	}
	oaPro.EnabledModels = rt1SlicePtr("gpt-5.4-pro")
	rt1SaveApply(t, ctx, proProc, oaPro, h.secret, "op-oa-pro-optin")
	if rt, err := proProc.client.Routing(ctx); err != nil ||
		rt.ResolvedRoles.Dialogue != nil || rt.ResolvedRoles.Utility != nil ||
		rt.ResolvedRoles.Coding == nil || rt.ResolvedRoles.Coding.ResolvedModel != "gpt-5.4-pro" || rt.ResolvedRoles.Coding.ThinkingLevel != "medium" ||
		rt.ResolvedRoles.Reasoning == nil || rt.ResolvedRoles.Reasoning.ResolvedModel != "gpt-5.4-pro" {
		t.Fatalf("OpenAI Direct gpt-5.4-pro explicit opt-in regression: %+v, %v", rt, err)
	}

	oaO3Pro := rt1Account("pro", "https://api.openai.com/v1", pi.OpenAIResponses, "o3-pro")
	oaO3Pro.EnabledModels = rt1SlicePtr("o3-pro")
	rt1SaveApply(t, ctx, proProc, oaO3Pro, h.secret, "op-oa-o3pro-optin")
	prQual, err := proProc.client.SetRoutingPolicy(ctx, pi.ProductRoutingPolicyParams{Policy: pi.ProductRoutingQuality, OperationID: "op-o3pro-qual"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proProc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *prQual.SavedRevision, OperationID: "op-o3pro-aqual"}); err != nil {
		t.Fatal(err)
	}
	if rt, err := proProc.client.Routing(ctx); err != nil ||
		rt.ResolvedRoles.Dialogue == nil || rt.ResolvedRoles.Dialogue.ResolvedModel != "o3-pro" || rt.ResolvedRoles.Dialogue.ThinkingLevel != "low" ||
		rt.ResolvedRoles.Utility != nil {
		t.Fatalf("OpenAI Direct o3-pro explicit opt-in regression: %+v, %v", rt, err)
	}

	fbAcc := rt1Account("backup", h.server.URL+"/fb/v1", pi.OpenAICompletions, "plain-fallback")
	fbAcc.FallbackOnly = rt1BoolPtr(true)
	if _, err := proProc.client.DeleteProvider(ctx, pi.ProductProviderTargetParams{ProviderID: "pro", OperationID: "op-del-pro"}); err != nil {
		t.Fatal(err)
	}
	rt1SaveApply(t, ctx, proProc, fbAcc, h.secret, "op-fb")
	if rt, err := proProc.client.Routing(ctx); err != nil || rt.ResolvedRoles.Dialogue == nil || rt.ResolvedRoles.Dialogue.Reason != "fallback_only" {
		t.Fatalf("fallbackOnly primary activation mismatch: %+v, %v", rt, err)
	}
	ordAcc := rt1Account("ordinary", h.server.URL+"/ord/v1", pi.OpenAICompletions, "plain-ordinary")
	rt1SaveApply(t, ctx, proProc, ordAcc, h.secret, "op-ord")
	if rt, err := proProc.client.Routing(ctx); err != nil || rt.ResolvedRoles.Dialogue == nil || rt.ResolvedRoles.Dialogue.ProviderID != "ordinary" || rt.ResolvedRoles.Dialogue.Reason == "fallback_only" {
		t.Fatalf("ordinary provider did not displace fallbackOnly: %+v, %v", rt, err)
	}

	explicitPro := rt1Account("ordinary", h.server.URL+"/pro/v1", pi.OpenAICompletions, "gpt-5.4-pro")
	explicitPro.EnabledModels = rt1SlicePtr("gpt-5.4-pro")
	explicitPro.ModelOverrides = rt1OverridesPtr(pi.ProductModelOverride{ID: "gpt-5.4-pro", IsChat: rt1BoolPtr(true), ToolCall: rt1BoolPtr(true)})
	rt1SaveApply(t, ctx, proProc, explicitPro, h.secret, "op-explicit-pro")
	proBot, err := proProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-pro-bot",
		Profile:     pi.ProductBotProfile{Name: "ProOptIn", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt1SendAndWait(t, ctx, proProc, proEv, proBot, "RT1_PRO_EXPLICIT", "op-pro-turn")
	proReq, ok := h.lastMatching("RT1_PRO_EXPLICIT")
	if !ok || proReq.Model != "gpt-5.4-pro" || proReq.HasReasoning || proReq.ReasoningEffort != "" || proReq.HasThinking {
		t.Fatalf("private Pro opt-in wire mismatch: %+v", proReq)
	}
	proProc.shutdown(t, ctx)

	poolProc, poolOpts, _ := startFresh("pool-resave-repoint")
	baseDirect := rt1Account("direct", "https://api.openai.com/v1", pi.OpenAIResponses, "gpt-5.4")
	baseDirect.Models = []string{"gpt-5.4", "gpt-5.4-mini"}
	rt1SaveApply(t, ctx, poolProc, baseDirect, h.secret, "op-base")
	connHashBefore := sha256.Sum256(productRead(t, poolOpts.CredentialsPath))

	withRT := baseDirect
	withRT.EnabledModels = rt1SlicePtr()
	withRT.FallbackOnly = rt1BoolPtr(false)
	withRT.ModelOverrides = rt1OverridesPtr(pi.ProductModelOverride{ID: "gpt-5.4", ContextTokens: rt1IntPtr(250000)})
	savedRT, err := poolProc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: withRT},
		OperationID:               "op-set-rt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(productRead(t, poolOpts.CredentialsPath)) != connHashBefore {
		t.Fatal("nonsecret RT edit mutated connection.json")
	}
	legacyResave, err := poolProc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: baseDirect},
		OperationID:               "op-legacy-resave",
	})
	if err != nil || *legacyResave.SavedRevision <= *savedRT.SavedRevision {
		t.Fatalf("same-endpoint legacy resave = %+v, %v", legacyResave, err)
	}
	listedRT, err := poolProc.client.ListProviders(ctx)
	if err != nil || len(listedRT.Accounts) != 1 ||
		listedRT.Accounts[0].EnabledModels == nil || len(*listedRT.Accounts[0].EnabledModels) != 0 ||
		listedRT.Accounts[0].FallbackOnly == nil || *listedRT.Accounts[0].FallbackOnly ||
		listedRT.Accounts[0].ModelOverrides == nil || len(*listedRT.Accounts[0].ModelOverrides) != 1 ||
		(*listedRT.Accounts[0].ModelOverrides)[0].ContextTokens == nil || *(*listedRT.Accounts[0].ModelOverrides)[0].ContextTokens != 250000 {
		t.Fatalf("same-endpoint legacy resave lost RT fields: %+v, %v", listedRT.Accounts, err)
	}
	if _, err := poolProc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *legacyResave.SavedRevision, OperationID: "op-apply-empty"}); err != nil {
		t.Fatal(err)
	}
	if rt, err := poolProc.client.Routing(ctx); err != nil || rt.ResolvedRoles.Dialogue != nil {
		t.Fatalf("explicit empty enabledModels=[] must disable default model: %+v, %v", rt, err)
	}

	repointStale := withRT
	repointStale.BaseURL = h.server.URL + "/repoint-a/v1"
	repointStale.WireAPI = pi.OpenAICompletions
	if _, err := poolProc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: repointStale, APIKey: h.secret},
		OperationID:               "op-repoint-stale",
	}); err != nil {
		t.Fatal(err)
	}
	listedStale, err := poolProc.client.ListProviders(ctx)
	if err != nil || listedStale.Accounts[0].ModelOverrides != nil {
		t.Fatalf("repoint with copied stale modelOverrides did not invalidate them: %+v, %v", listedStale.Accounts, err)
	}

	repointFresh := repointStale
	repointFresh.BaseURL = h.server.URL + "/repoint-b/v1"
	repointFresh.WireAPI = pi.OpenAIResponses
	repointFresh.DefaultModel = "fresh-model"
	repointFresh.EnabledModels = rt1SlicePtr("fresh-model")
	repointFresh.ModelOverrides = rt1OverridesPtr(pi.ProductModelOverride{ID: "fresh-model", IsChat: rt1BoolPtr(true), ToolCall: rt1BoolPtr(true)})
	if _, err := poolProc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: repointFresh, APIKey: h.secret},
		OperationID:               "op-repoint-fresh",
	}); err != nil {
		t.Fatal(err)
	}
	listedFresh, err := poolProc.client.ListProviders(ctx)
	if err != nil || listedFresh.Accounts[0].ModelOverrides == nil || len(*listedFresh.Accounts[0].ModelOverrides) != 1 || (*listedFresh.Accounts[0].ModelOverrides)[0].ID != "fresh-model" {
		t.Fatalf("repoint with freshly supplied modelOverrides lost overrides (P2-3 regression): %+v, %v", listedFresh.Accounts, err)
	}
	poolProc.shutdown(t, ctx)

	scoreProc, _, _ := startFresh("score-shortlist")
	nanoAcc := rt1Account("first", "https://api.openai.com/v1", pi.OpenAIResponses, "gpt-5.4-nano")
	strongAcc := rt1Account("second", "https://api.openai.com/v1", pi.OpenAIResponses, "gpt-5.4")
	if _, err := scoreProc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: nanoAcc, APIKey: h.secret},
		OperationID:               "op-sc-1",
	}); err != nil {
		t.Fatal(err)
	}
	rt1SaveApply(t, ctx, scoreProc, strongAcc, h.secret, "op-sc-2")
	wantPolicy := map[pi.ProductRoutingPolicy]string{
		pi.ProductRoutingBalanced: "gpt-5.4",
		pi.ProductRoutingQuality:  "gpt-5.4",
		pi.ProductRoutingEconomy:  "gpt-5.4-nano",
	}
	for _, pol := range []pi.ProductRoutingPolicy{pi.ProductRoutingBalanced, pi.ProductRoutingQuality, pi.ProductRoutingEconomy} {
		pr, err := scoreProc.client.SetRoutingPolicy(ctx, pi.ProductRoutingPolicyParams{Policy: pol, OperationID: "op-sc-pol-" + string(pol)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := scoreProc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *pr.SavedRevision, OperationID: "op-sc-apol-" + string(pol)}); err != nil {
			t.Fatal(err)
		}
		rt, err := scoreProc.client.Routing(ctx)
		if err != nil || rt.ResolvedRoles.Dialogue == nil || rt.ResolvedRoles.Dialogue.ResolvedModel != wantPolicy[pol] {
			t.Fatalf("anti-inversion policy %s = %+v, want %s", pol, rt.ResolvedRoles.Dialogue, wantPolicy[pol])
		}
	}

	if _, err := scoreProc.client.DeleteProvider(ctx, pi.ProductProviderTargetParams{ProviderID: "first", OperationID: "op-sc-del1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := scoreProc.client.DeleteProvider(ctx, pi.ProductProviderTargetParams{ProviderID: "second", OperationID: "op-sc-del2"}); err != nil {
		t.Fatal(err)
	}
	sameFree := rt1Account("orSame", "https://openrouter.ai/api/v1", pi.OpenAICompletions, "google/gemma-4-31b-it")
	sameFree.EnabledModels = rt1SlicePtr("google/gemma-4-31b-it", "google/gemma-4-31b-it:free")
	rt1SaveApply(t, ctx, scoreProc, sameFree, h.secret, "op-or-same")
	for _, pol := range []pi.ProductRoutingPolicy{pi.ProductRoutingEconomy, pi.ProductRoutingBalanced, pi.ProductRoutingQuality} {
		pr, err := scoreProc.client.SetRoutingPolicy(ctx, pi.ProductRoutingPolicyParams{Policy: pol, OperationID: "op-free-pol-" + string(pol)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := scoreProc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *pr.SavedRevision, OperationID: "op-free-apol-" + string(pol)}); err != nil {
			t.Fatal(err)
		}
		rt, err := scoreProc.client.Routing(ctx)
		if err != nil || rt.ResolvedRoles.Dialogue == nil || rt.ResolvedRoles.Dialogue.ResolvedModel != "google/gemma-4-31b-it:free" || rt.ResolvedRoles.Dialogue.CostTier != "free" {
			t.Fatalf("same-provider :free sibling regression under %s: %+v, %v", pol, rt.ResolvedRoles.Dialogue, err)
		}
	}

	if _, err := scoreProc.client.DeleteProvider(ctx, pi.ProductProviderTargetParams{ProviderID: "orSame", OperationID: "op-del-orsame"}); err != nil {
		t.Fatal(err)
	}
	orPaid := rt1Account("orPaid", "https://openrouter.ai/api/v1", pi.OpenAICompletions, "google/gemma-4-31b-it")
	orPaid.EnabledModels = rt1SlicePtr("google/gemma-4-31b-it")
	orFree := rt1Account("orFree", "https://openrouter.ai/api/v1", pi.OpenAICompletions, "google/gemma-4-31b-it:free")
	orFree.EnabledModels = rt1SlicePtr("google/gemma-4-31b-it:free")
	if _, err := scoreProc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: orPaid, APIKey: h.secret},
		OperationID:               "op-orpaid",
	}); err != nil {
		t.Fatal(err)
	}
	crossRev := rt1SaveApply(t, ctx, scoreProc, orFree, h.secret, "op-orfree")
	ecoRev, err := scoreProc.client.SetRoutingPolicy(ctx, pi.ProductRoutingPolicyParams{Policy: pi.ProductRoutingEconomy, OperationID: "op-cross-eco"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scoreProc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *ecoRev.SavedRevision, OperationID: "op-cross-aeco"}); err != nil {
		t.Fatal(err)
	}
	if rt, err := scoreProc.client.Routing(ctx); err != nil || rt.ResolvedRoles.Dialogue == nil || rt.ResolvedRoles.Dialogue.ProviderID != "orPaid" {
		t.Fatalf("cross-provider canonical dedup must respect applied order even under economy: %+v, %v", rt.ResolvedRoles.Dialogue, err)
	}
	_ = crossRev
	reordCross, err := scoreProc.client.ReorderProviders(ctx, pi.ProductProviderReorderParams{
		ProviderIDs: []string{"orFree", "orPaid"}, SavedRevision: *ecoRev.SavedRevision, OperationID: "op-reord-cross",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scoreProc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *reordCross.SavedRevision, OperationID: "op-areord-cross"}); err != nil {
		t.Fatal(err)
	}
	if rt, err := scoreProc.client.Routing(ctx); err != nil || rt.ResolvedRoles.Dialogue == nil || rt.ResolvedRoles.Dialogue.ProviderID != "orFree" {
		t.Fatalf("reordered cross-provider dedup did not select orFree: %+v, %v", rt.ResolvedRoles.Dialogue, err)
	}

	if _, err := scoreProc.client.DeleteProvider(ctx, pi.ProductProviderTargetParams{ProviderID: "orFree", OperationID: "op-del-orfree"}); err != nil {
		t.Fatal(err)
	}
	if _, err := scoreProc.client.DeleteProvider(ctx, pi.ProductProviderTargetParams{ProviderID: "orPaid", OperationID: "op-del-orpaid"}); err != nil {
		t.Fatal(err)
	}
	cleanOne := rt1Account("cleanOne", "https://api.openai.com/v1", pi.OpenAIResponses, "gpt-5.4")
	cleanOne.Models = []string{"gpt-5.4"}
	noisyTwo := rt1Account("noisyTwo", "https://openrouter.ai/api/v1", pi.OpenAICompletions, "openai/gpt-5.4")
	noisyModels := make([]string, 0, 128)
	noisyModels = append(noisyModels, "openai/gpt-5.4")
	for i := 0; i < 127; i++ {
		noisyModels = append(noisyModels, fmt.Sprintf("zz-noise-%03d", i))
	}
	noisyTwo.Models = noisyModels
	if _, err := scoreProc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: cleanOne, APIKey: h.secret},
		OperationID:               "op-cleanone",
	}); err != nil {
		t.Fatal(err)
	}
	rt1SaveApply(t, ctx, scoreProc, noisyTwo, h.secret, "op-noisytwo")
	if rt, err := scoreProc.client.Routing(ctx); err != nil || rt.ResolvedRoles.Dialogue == nil || rt.ResolvedRoles.Dialogue.ProviderID != "cleanOne" {
		t.Fatalf("128-variant gateway stole priority from cleanOne: %+v, %v", rt.ResolvedRoles.Dialogue, err)
	}
	scoreProc.shutdown(t, ctx)

	prioProc, prioOpts, prioEv := startFresh("priority-hot-v3")
	primary := rt1Account("primary", h.server.URL+"/primary/v1", pi.OpenAICompletions, "default")
	secondary := rt1Account("secondary", h.server.URL+"/secondary/v1", pi.OpenAICompletions, "default")
	if _, err := prioProc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: primary, APIKey: h.secret},
		OperationID:               "op-p1",
	}); err != nil {
		t.Fatal(err)
	}
	state := rt1SaveApply(t, ctx, prioProc, secondary, h.secret, "op-p2")
	hotBot, err := prioProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-hot-bot",
		Profile:     pi.ProductBotProfile{Name: "Hot", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt1SendAndWait(t, ctx, prioProc, prioEv, hotBot, "RT1_HOT_1", "op-hot-1")
	h1, ok := h.lastMatching("RT1_HOT_1")
	if !ok || h1.Path != "/primary/v1/chat/completions" || h1.HasReasoning || h1.ReasoningEffort != "" || h1.HasThinking {
		t.Fatalf("hot turn1 mismatch: %+v", h1)
	}
	origList, err := prioProc.client.ListProviders(ctx)
	if err != nil || origList.ResolvedRoles.Dialogue.CanonicalModelID != "custom:7:primary:default" {
		t.Fatalf("duplicate private default canonical ID mismatch: %+v, %v", origList.ResolvedRoles.Dialogue, err)
	}

	_, err = prioProc.client.ReorderProviders(ctx, pi.ProductProviderReorderParams{
		ProviderIDs: []string{"secondary"}, SavedRevision: *state.SavedRevision, OperationID: "op-bad-perm",
	})
	mcpExpectRemoteCode(t, err, "invalid_params", "ReorderProviders non-permutation")

	reordParams := pi.ProductProviderReorderParams{
		ProviderIDs: []string{"secondary", "primary"}, SavedRevision: *state.SavedRevision, OperationID: "op-reord-ok",
	}
	reordState, err := prioProc.client.ReorderProviders(ctx, reordParams)
	if err != nil || *reordState.SavedRevision != *state.SavedRevision+1 || *reordState.EffectiveRevision != *state.EffectiveRevision {
		t.Fatalf("ReorderProviders result = %+v, prior=%+v, err=%v", reordState, state, err)
	}
	reordReplay, err := prioProc.client.ReorderProviders(ctx, reordParams)
	if err != nil || !reflect.DeepEqual(reordReplay, reordState) {
		t.Fatalf("ReorderProviders duplicate operationId replay = %+v, want %+v, err=%v", reordReplay, reordState, err)
	}
	_, err = prioProc.client.ReorderProviders(ctx, pi.ProductProviderReorderParams{
		ProviderIDs: []string{"secondary", "primary"}, SavedRevision: *state.SavedRevision, OperationID: "op-reord-stale",
	})
	mcpExpectRemoteCode(t, err, "stale_revision", "ReorderProviders stale savedRevision")
	prioEv.drainProviderChange(t, ctx, prioProc, "saved")

	beforeApplyList, err := prioProc.client.ListProviders(ctx)
	if err != nil || len(beforeApplyList.Accounts) != 2 ||
		beforeApplyList.Accounts[0].ProviderID != "secondary" || beforeApplyList.Accounts[0].Priority == nil || *beforeApplyList.Accounts[0].Priority != 1 ||
		beforeApplyList.Accounts[1].ProviderID != "primary" || beforeApplyList.Accounts[1].Priority == nil || *beforeApplyList.Accounts[1].Priority != 2 ||
		beforeApplyList.ResolvedRoles.Dialogue.ProviderID != "primary" {
		t.Fatalf("reorder before Apply state mismatch: %+v, %v", beforeApplyList, err)
	}

	if _, err := prioProc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		SavedRevision: *reordState.SavedRevision, OperationID: "op-apply-reord",
	}); err != nil {
		t.Fatal(err)
	}
	if rt, err := prioProc.client.Routing(ctx); err != nil || rt.ResolvedRoles.Dialogue.CanonicalModelID != "custom:9:secondary:default" {
		t.Fatalf("applied reorder route mismatch: %+v, %v", rt.ResolvedRoles.Dialogue, err)
	}
	rt1SendAndWait(t, ctx, prioProc, prioEv, hotBot, "RT1_HOT_2", "op-hot-2")
	freshBot, err := prioProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-fresh-prio-bot",
		Profile:     pi.ProductBotProfile{Name: "FreshPrio", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt1SendAndWait(t, ctx, prioProc, prioEv, freshBot, "RT1_FRESH_2", "op-fresh-2")
	h2, _ := h.lastMatching("RT1_HOT_2")
	f2, _ := h.lastMatching("RT1_FRESH_2")
	if h2.Path != "/primary/v1/chat/completions" || f2.Path != "/secondary/v1/chat/completions" {
		t.Fatalf("hot capture stickiness / fresh routing mismatch: hot=%+v fresh=%+v", h2, f2)
	}

	if _, err := prioProc.client.DeleteProvider(ctx, pi.ProductProviderTargetParams{ProviderID: "secondary", OperationID: "op-del-sec"}); err != nil {
		t.Fatal(err)
	}
	delList, err := prioProc.client.ListProviders(ctx)
	if err != nil || len(delList.Accounts) != 1 || delList.Accounts[0].ProviderID != "primary" ||
		delList.ResolvedRoles.Dialogue == nil || delList.ResolvedRoles.Dialogue.ProviderName != "Fixture secondary" {
		t.Fatalf("pending deletion must preserve effective providerName: %+v, %v", delList, err)
	}

	var rawConn struct {
		Version  int              `json:"version"`
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(productRead(t, prioOpts.CredentialsPath), &rawConn); err != nil || rawConn.Version != 3 || len(rawConn.Accounts) == 0 {
		t.Fatalf("connection.json v3 decode mismatch: %+v, %v", rawConn, err)
	}
	for _, acc := range rawConn.Accounts {
		for _, forbidden := range []string{"priority", "enabledModels", "fallbackOnly", "modelOverrides"} {
			if _, ok := acc[forbidden]; ok {
				t.Fatalf("connection.json v3 leaked RT field %q: %+v", forbidden, acc)
			}
		}
	}

	prioProc.shutdown(t, ctx)
	h.assertClean()
}

func TestPiProductRT1SQLiteV10MigrationV12RefusalAndIndependentRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	nodePath, workerPath := mcpRequirePinnedWorker(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newRT1Harness(t)

	v10Dir := filepath.Join(root, "genuine-v10")
	if err := os.MkdirAll(v10Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	v10Opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(v10Dir, "pi"),
		ProfilePath:     filepath.Join(v10Dir, "contacts.json"),
		CredentialsPath: filepath.Join(v10Dir, "connection.json"),
	}
	v10DB := filepath.Join(v10Dir, "nagi.db")
	protocolJS := filepath.Join(filepath.Dir(workerPath), "protocol.js")

	canonicalV10Bytes := runNodeModule(t, nodePath, `
import { createHash } from 'node:crypto';
import { writeFileSync } from 'node:fs';
import { DatabaseSync } from 'node:sqlite';
import { pathToFileURL } from 'node:url';
const [dbPath, profilePath, credPath, protocolPath, ws, baseA, baseB, secret] = process.argv.slice(1);
const { canonicalJSON } = await import(pathToFileURL(protocolPath).href);
const accA = { providerId: 'legacyA', name: 'Legacy A', wireApi: 'openai-completions', baseUrl: baseA, defaultModel: 'legacy-chat', contextTokens: 65536, outputTokens: 4096, enabled: true, credentialSourceId: 'cred-legacy-a' };
const accB = { providerId: 'legacyB', name: 'Legacy B', wireApi: 'openai-completions', baseUrl: baseB, defaultModel: 'legacy-small', contextTokens: 65536, outputTokens: 4096, enabled: true, credentialSourceId: 'cred-legacy-b' };
writeFileSync(credPath, JSON.stringify({
  version: 3, sources: {},
  accounts: [{ ...accA, apiKey: secret }, { ...accB, apiKey: secret }],
}) + '\n', { mode: 0o600 });
const steerPayload = { text: 'v10 steer note', materials: [] };
const steerFp = createHash('sha256').update(canonicalJSON({ activityId: 'act-v10', executionId: 'exec-v10', operationId: 'op-steer-v10', ...steerPayload })).digest('hex');
const steerOrig = { steerId: 'steer-v10', operationId: 'op-steer-v10', activityId: 'act-v10', executionId: 'exec-v10', state: 'pending_enqueue', revision: 1 };
const profile = {
  version: 1,
  bots: [{ botId: 'bot-v10', profile: { name: 'V10Bot', role: '', purpose: '', instructions: 'Stay calm.', workspace: ws, codingTools: [] }, profileRevision: 1, archived: false, currentConversationId: 'conv-v10' }],
  conversations: [{ conversationId: 'conv-v10', botId: 'bot-v10', revision: 0, draft: { revision: 0, text: '', materials: [] } }],
  activities: [{
    activityId: 'act-v10', ownerBotId: 'bot-v10', originConversationId: 'conv-v10', revision: 2, state: 'completed',
    goal: 'V10 goal', requirements: [], questions: [], materials: [], inputFiles: [], executionIds: ['exec-v10'],
    results: [{ activityId: 'act-v10', executionId: 'exec-v10', text: 'Done.', delivery: 'available', files: [] }],
    updatedAt: '2026-11-25T10:00:00.000Z',
  }],
  executions: [{ executionId: 'exec-v10', revision: 2, state: 'completed', operationId: 'op-act-v10', input: { revision: 0, text: 'Run', materials: [] } }],
  operations: [
    { method: 'activity.continue', fingerprint: 'a'.repeat(64), receipt: { operationId: 'op-act-v10', state: 'accepted', target: { activityId: 'act-v10' }, execution: { executionId: 'exec-v10', revision: 1, state: 'starting' } }, reply: { operationId: 'op-act-v10', state: 'accepted', target: { activityId: 'act-v10' }, execution: { executionId: 'exec-v10', revision: 1, state: 'starting' } } },
    { method: 'activity.steer', fingerprint: steerFp, receipt: { operationId: 'op-steer-v10', state: 'accepted', target: { activityId: 'act-v10' }, result: steerOrig }, reply: steerOrig },
  ],
  connections: [], savedRevision: null, appliedRevision: null,
  providerAccounts: [accA, accB],
  providerEffectiveAccounts: [accA],
  routingPolicy: 'balanced',
  providerEffectivePolicy: 'balanced',
  providerSavedRevision: 2,
  providerEffectiveRevision: 1,
};
const mirror = canonicalJSON(profile) + '\n';
const digest = createHash('sha256').update(mirror).digest('hex');
const stale = canonicalJSON({ ...profile, preferences: { appearance: 'system' } }) + '\n';
const staleDigest = createHash('sha256').update(stale).digest('hex');
writeFileSync(profilePath, stale, { mode: 0o600 });

const db = new DatabaseSync(dbPath);
db.exec('PRAGMA foreign_keys = ON;');
db.exec(`+"`"+`
  CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
  CREATE TABLE bots (bot_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, profile_json TEXT NOT NULL, profile_revision INTEGER NOT NULL, archived INTEGER NOT NULL CHECK (archived IN (0, 1)), current_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED, retained_json TEXT, system_kind TEXT) STRICT;
  CREATE TABLE conversations (conversation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, revision INTEGER NOT NULL, draft_json TEXT NOT NULL, binding_json TEXT, latest_execution_id TEXT REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED) STRICT;
  CREATE TABLE activities (activity_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id), origin_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id), revision INTEGER NOT NULL, state TEXT NOT NULL, goal_json TEXT NOT NULL, requirements_json TEXT NOT NULL, questions_json TEXT NOT NULL, materials_json TEXT NOT NULL, input_files_json TEXT NOT NULL, execution_ids_json TEXT NOT NULL, results_json TEXT NOT NULL, binding_json TEXT, retained_json TEXT, granted_linked_activity_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(granted_linked_activity_ids_json) AND json_type(granted_linked_activity_ids_json) = 'array'), routing_json TEXT, acknowledged_at TEXT, acknowledged_execution_id TEXT, closed_at TEXT, updated_at TEXT) STRICT;
  CREATE TABLE executions (execution_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, revision INTEGER NOT NULL, state TEXT NOT NULL, operation_id TEXT NOT NULL, input_json TEXT NOT NULL, error_json TEXT, engine_run_id TEXT, imported_activity_id TEXT REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, retained_json TEXT) STRICT;
  CREATE TABLE operations (operation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, method_json TEXT NOT NULL, fingerprint TEXT NOT NULL, receipt_json TEXT NOT NULL, reply_json TEXT NOT NULL, reply_error_json TEXT, connection_revision INTEGER REFERENCES connections(revision), retained_json TEXT) STRICT;
  CREATE TABLE connections (revision INTEGER PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, source_id TEXT NOT NULL, configuration_json TEXT NOT NULL) STRICT;
  CREATE TABLE bot_memory_rules (rule_id TEXT PRIMARY KEY, bot_id TEXT NOT NULL REFERENCES bots(bot_id) ON DELETE CASCADE, record_json TEXT NOT NULL) STRICT;
  CREATE TABLE bot_work_logs (log_id TEXT PRIMARY KEY, bot_id TEXT NOT NULL REFERENCES bots(bot_id) ON DELETE CASCADE, date TEXT NOT NULL, created_at TEXT NOT NULL, summary TEXT NOT NULL, tags_text TEXT NOT NULL, record_json TEXT NOT NULL) STRICT;
  CREATE INDEX bot_work_logs_by_date ON bot_work_logs(bot_id, date DESC, created_at DESC);
  CREATE VIRTUAL TABLE bot_work_logs_fts USING fts5(summary, tags_text, content='bot_work_logs', content_rowid='rowid');
  CREATE TRIGGER bot_work_logs_insert AFTER INSERT ON bot_work_logs BEGIN INSERT INTO bot_work_logs_fts(rowid, summary, tags_text) VALUES (new.rowid, new.summary, new.tags_text); END;
  CREATE TRIGGER bot_work_logs_delete AFTER DELETE ON bot_work_logs BEGIN INSERT INTO bot_work_logs_fts(bot_work_logs_fts, rowid, summary, tags_text) VALUES ('delete', old.rowid, old.summary, old.tags_text); END;
  CREATE TABLE bot_pending_learnings (learning_id TEXT PRIMARY KEY, bot_id TEXT NOT NULL REFERENCES bots(bot_id) ON DELETE CASCADE, record_json TEXT NOT NULL) STRICT;
  CREATE TABLE bot_memory_meta (bot_id TEXT PRIMARY KEY REFERENCES bots(bot_id) ON DELETE CASCADE, record_json TEXT NOT NULL) STRICT;
  CREATE TABLE activity_traces (seq INTEGER PRIMARY KEY AUTOINCREMENT, activity_id TEXT NOT NULL REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, at TEXT NOT NULL, kind TEXT NOT NULL CHECK (kind IN ('tool_started','tool_finished','file_confirmed','state_changed')), detail_json TEXT NOT NULL CHECK (json_valid(detail_json) AND length(CAST(detail_json AS BLOB)) <= 1024)) STRICT;
  CREATE INDEX activity_traces_by_activity ON activity_traces(activity_id, seq);
  CREATE INDEX activity_traces_by_execution ON activity_traces(activity_id, execution_id, seq);
  CREATE TABLE activity_trace_meta (execution_id TEXT PRIMARY KEY REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, activity_id TEXT NOT NULL REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, coverage TEXT NOT NULL CHECK (coverage IN ('recorded','unrecorded')), evicted_through_seq INTEGER NOT NULL DEFAULT 0) STRICT;
  CREATE INDEX activity_trace_meta_by_activity ON activity_trace_meta(activity_id);
  CREATE TABLE mcp_servers (server_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, config_json TEXT NOT NULL, catalog_json TEXT) STRICT;
  CREATE TABLE skill_approvals (grant_key TEXT PRIMARY KEY, owner_bot_id TEXT NOT NULL, source TEXT NOT NULL, skill_id TEXT NOT NULL, approval_digest TEXT NOT NULL, permissions_digest TEXT NOT NULL, mode TEXT NOT NULL CHECK (mode IN ('pure_compute_tested','user_approved')), installation_id TEXT NOT NULL, approved_at TEXT NOT NULL, record_json TEXT NOT NULL) STRICT;
  CREATE TABLE provider_accounts (provider_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, config_json TEXT NOT NULL) STRICT;
  CREATE TABLE execution_mutation_admissions (admission_id INTEGER PRIMARY KEY AUTOINCREMENT, execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, activity_id TEXT REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, call_id TEXT NOT NULL, tool_name TEXT NOT NULL, tool_class TEXT NOT NULL CHECK (tool_class IN ('readonly','mutating')), state TEXT NOT NULL CHECK (state IN ('admitted','non_dispatched','settled_ok','settled_error','unknown')), at TEXT NOT NULL) STRICT;
  CREATE INDEX mutation_admissions_by_execution ON execution_mutation_admissions(execution_id, tool_class, call_id);
  CREATE TABLE execution_admission_coverage (execution_id TEXT PRIMARY KEY REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED) STRICT;
  CREATE TABLE activity_escalations (activity_id TEXT PRIMARY KEY REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, prior_execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, escalated_execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, from_role TEXT NOT NULL, to_role TEXT NOT NULL, at TEXT NOT NULL) STRICT;
  CREATE TABLE activity_progress (activity_id TEXT PRIMARY KEY REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, call_id TEXT NOT NULL, progress_seq INTEGER NOT NULL CHECK (progress_seq > 0), record_json TEXT NOT NULL) STRICT;
  CREATE TABLE skill_approval_requests (request_id TEXT PRIMARY KEY, activity_id TEXT NOT NULL REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, call_id TEXT NOT NULL, bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, revision INTEGER NOT NULL CHECK (revision > 0), state TEXT NOT NULL CHECK (state IN ('open','approved','denied','expired','cancelled')), record_json TEXT NOT NULL, UNIQUE (execution_id, call_id)) STRICT;
  CREATE INDEX skill_approval_requests_by_activity ON skill_approval_requests(activity_id, state);
  CREATE TABLE activity_triage_accounting (activity_id TEXT PRIMARY KEY REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, attempts_used INTEGER NOT NULL DEFAULT 0, auto_answers_used INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL) STRICT;
  CREATE TABLE activity_steers (steer_id TEXT PRIMARY KEY, activity_id TEXT NOT NULL REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, operation_id TEXT NOT NULL UNIQUE, fingerprint TEXT NOT NULL, state TEXT NOT NULL CHECK (state IN ('pending_enqueue','queued','delivered','not_applied','unknown','rejected')), revision INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, settled_at TEXT, payload_json TEXT NOT NULL) STRICT;
  CREATE INDEX activity_steers_by_execution ON activity_steers (activity_id, execution_id, created_at);
`+"`"+`);
db.exec('BEGIN IMMEDIATE');
const b = profile.bots[0];
db.prepare('INSERT INTO bots VALUES (?, 0, ?, ?, 0, ?, NULL, NULL)').run(JSON.stringify(b.botId), JSON.stringify(b.profile), b.profileRevision, JSON.stringify(b.currentConversationId));
const c = profile.conversations[0];
db.prepare('INSERT INTO conversations VALUES (?, 0, ?, 0, ?, NULL, NULL)').run(JSON.stringify(c.conversationId), JSON.stringify(c.botId), JSON.stringify(c.draft));
const e = profile.executions[0];
db.prepare('INSERT INTO executions VALUES (?, 0, 2, ?, ?, ?, NULL, NULL, NULL, NULL)').run(JSON.stringify(e.executionId), e.state, JSON.stringify(e.operationId), JSON.stringify(e.input));
const a = profile.activities[0];
db.prepare('INSERT INTO activities VALUES (?, 0, ?, ?, 2, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, NULL, NULL, NULL, NULL, ?)').run(
  JSON.stringify(a.activityId), JSON.stringify(a.ownerBotId), JSON.stringify(a.originConversationId), a.state,
  JSON.stringify(a.goal), JSON.stringify(a.requirements), JSON.stringify(a.questions), JSON.stringify(a.materials),
  JSON.stringify(a.inputFiles), JSON.stringify(a.executionIds), JSON.stringify(a.results), '[]', a.updatedAt);
profile.operations.forEach((op, idx) => {
  db.prepare('INSERT INTO operations VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, NULL)').run(JSON.stringify(op.receipt.operationId), idx, JSON.stringify(op.method), op.fingerprint, JSON.stringify(op.receipt), JSON.stringify(op.reply));
});
profile.providerAccounts.forEach((acc, idx) => {
  db.prepare('INSERT INTO provider_accounts VALUES (?, ?, ?)').run(acc.providerId, idx, JSON.stringify(acc));
});
db.prepare('INSERT INTO activity_steers VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)').run(
  JSON.stringify('steer-v10'), JSON.stringify('act-v10'), JSON.stringify('exec-v10'), JSON.stringify('op-steer-v10'),
  steerFp, 'delivered', 3, '2026-11-25T10:00:00.000Z', '2026-11-25T10:00:01.000Z', JSON.stringify(steerPayload));
for (const [k, v] of Object.entries({
  trace_generation: '0123456789abcdef0123456789abcdef',
  saved_revision: 'null', applied_revision: 'null',
  routing_policy: '"balanced"', provider_effective_policy: '"balanced"',
  provider_saved_revision: '2', provider_effective_revision: '1',
  provider_effective_accounts: JSON.stringify(profile.providerEffectiveAccounts),
  snapshot_sha256: digest,
  pending_snapshot_previous: JSON.stringify(staleDigest),
  updated_at: '2026-11-25T10:00:02.000Z',
})) {
  db.prepare('INSERT INTO meta VALUES (?, ?)').run(k, v);
}
db.exec('COMMIT');
db.exec('PRAGMA user_version = 10');
db.close();
console.log(Buffer.from(mirror).toString('base64'));
`, v10DB, v10Opts.ProfilePath, v10Opts.CredentialsPath, protocolJS, root, h.server.URL+"/legA/v1", h.server.URL+"/legB/v1", h.secret)

	callsBeforeMig := h.requestCount()
	v10Proc := startProductProcess(t, v10Opts)
	_ = v10Proc.open(t, ctx)
	if h.requestCount() != callsBeforeMig {
		t.Fatal("v10->v11 migration made provider calls")
	}
	repairedMirror := productRead(t, v10Opts.ProfilePath)
	expectedMirror := runNodeModule(t, nodePath, `console.log(Buffer.from(process.argv[1], 'base64').toString('utf8'))`, canonicalV10Bytes) + "\n"
	if string(repairedMirror) != expectedMirror {
		t.Fatalf("v10->v11 pending mirror repair did not restore exact canonical bytes")
	}
	v10State := runNodeSQLite(t, nodePath, v10DB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
const pending = !!db.prepare("SELECT value FROM meta WHERE key = 'pending_snapshot_previous'").get();
db.close();
console.log(JSON.stringify({ v, fk, pending }));
`)
	if v10State != `{"v":11,"fk":0,"pending":false}` {
		t.Fatalf("v10->v11 migration state = %s", v10State)
	}
	v10List, err := v10Proc.client.ListProviders(ctx)
	if err != nil || len(v10List.Accounts) != 2 ||
		v10List.Accounts[0].ProviderID != "legacyA" || v10List.Accounts[1].ProviderID != "legacyB" ||
		*v10List.SavedRevision != 2 || *v10List.EffectiveRevision != 1 ||
		v10List.ResolvedRoles.Dialogue == nil || v10List.ResolvedRoles.Dialogue.ProviderID != "legacyA" {
		t.Fatalf("v10->v11 saved/effective provider state mismatch: %+v, %v", v10List, err)
	}
	v10Steer, err := v10Proc.client.SteerStatus(ctx, pi.ProductSteerStatusParams{SteerID: "steer-v10"})
	if err != nil || v10Steer.State != "delivered" || v10Steer.OperationID != "op-steer-v10" {
		t.Fatalf("v10->v11 lost CP-3 steer record: %+v, %v", v10Steer, err)
	}

	legacyBackupPath := filepath.Join(root, "legacy-v10-backup.json")
	if _, err := v10Proc.client.BackupProfile(ctx, legacyBackupPath); err != nil {
		t.Fatal(err)
	}
	v10Proc.shutdown(t, ctx)

	if err := os.Remove(v10Opts.ProfilePath); err != nil {
		t.Fatal(err)
	}
	missingProc := startProductProcess(t, v10Opts)
	_ = missingProc.open(t, ctx)
	if rt, err := missingProc.client.Routing(ctx); err != nil || rt.ResolvedRoles.Dialogue == nil || rt.ResolvedRoles.Dialogue.ProviderID != "legacyA" {
		t.Fatalf("missing mirror reopen mismatch: %+v, %v", rt, err)
	}
	missingProc.shutdown(t, ctx)

	if err := os.WriteFile(v10Opts.ProfilePath, []byte("{corrupt mirror"), 0o600); err != nil {
		t.Fatal(err)
	}
	badMirrorClient, startErr := pi.StartProduct(v10Opts)
	if startErr == nil {
		if _, readyErr := badMirrorClient.Ready(ctx); readyErr == nil {
			if _, openErr := badMirrorClient.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
				t.Fatal("worker admitted corrupt contacts.json mirror")
			}
		}
		_, _ = badMirrorClient.Wait(ctx)
	}

	if err := os.WriteFile(v10Opts.ProfilePath, repairedMirror, 0o600); err != nil {
		t.Fatal(err)
	}
	runNodeSQLite(t, nodePath, v10DB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec('PRAGMA user_version = 12');
db.close();
`)
	v12Client, startErr := pi.StartProduct(v10Opts)
	if startErr == nil {
		if _, readyErr := v12Client.Ready(ctx); readyErr == nil {
			if _, openErr := v12Client.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
				t.Fatal("worker admitted SQLite user_version=12")
			}
		}
		_, _ = v12Client.Wait(ctx)
	}
	v12After := runNodeSQLite(t, nodePath, v10DB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const bots = db.prepare('SELECT count(*) AS n FROM bots').get().n;
db.close();
console.log(JSON.stringify({ v, bots }));
`)
	if v12After != `{"v":12,"bots":1}` {
		t.Fatalf("v12 refusal mutated database: %s", v12After)
	}

	runNodeSQLite(t, nodePath, v10DB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec('PRAGMA user_version = 11');
db.close();
`)
	rt1Proc := startProductProcess(t, v10Opts)
	_ = rt1Proc.open(t, ctx)
	reord, err := rt1Proc.client.ReorderProviders(ctx, pi.ProductProviderReorderParams{
		ProviderIDs: []string{"legacyB", "legacyA"}, SavedRevision: 2, OperationID: "op-bk-reord",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = reord
	accAUpdated := rt1Account("legacyA", h.server.URL+"/legA/v1", pi.OpenAICompletions, "legacy-chat")
	accAUpdated.EnabledModels = rt1SlicePtr()
	accAUpdated.FallbackOnly = rt1BoolPtr(true)
	accAUpdated.ModelOverrides = rt1OverridesPtr(pi.ProductModelOverride{ID: "legacy-chat", IsChat: rt1BoolPtr(true), ToolCall: rt1BoolPtr(true)})
	if _, err := rt1Proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: accAUpdated},
		OperationID:               "op-bk-rt-fields",
	}); err != nil {
		t.Fatal(err)
	}
	v11BackupPath := filepath.Join(root, "rt1-v11-backup.json")
	if _, err := rt1Proc.client.BackupProfile(ctx, v11BackupPath); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(productRead(t, v11BackupPath), []byte(h.secret)) || bytes.Contains(productRead(t, legacyBackupPath), []byte(h.secret)) {
		t.Fatal("credential leaked into portable backup")
	}
	rt1Proc.shutdown(t, ctx)

	restoreAndVerify := func(name, backupPath string, wantOrder []string, expectRTFields bool) {
		t.Helper()
		destDir := filepath.Join(root, name)
		if err := os.MkdirAll(destDir, 0o700); err != nil {
			t.Fatal(err)
		}
		targetProfile := filepath.Join(destDir, "contacts.json")
		if err := os.WriteFile(targetProfile, []byte("unavailable profile"), 0o600); err != nil {
			t.Fatal(err)
		}
		copyInfo, err := store.InspectStoreRecoveryCopy(backupPath)
		if err != nil {
			t.Fatalf("InspectStoreRecoveryCopy(%s): %v", name, err)
		}
		targetInfo, err := store.InspectStoreRecoveryTarget(targetProfile)
		if err != nil {
			t.Fatalf("InspectStoreRecoveryTarget(%s): %v", name, err)
		}
		if _, err := store.RestoreStoreRecoveryCopy(copyInfo, targetInfo); err != nil {
			t.Fatalf("RestoreStoreRecoveryCopy(%s): %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(destDir, "connection.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("independent restore %s unexpectedly has connection.json: %v", name, err)
		}
		rOpts := pi.ProductOptions{
			NodePath:        nodePath,
			WorkerPath:      workerPath,
			StateDir:        filepath.Join(destDir, "pi"),
			ProfilePath:     targetProfile,
			CredentialsPath: filepath.Join(destDir, "connection.json"),
		}
		for round := 1; round <= 2; round++ {
			rp := startProductProcess(t, rOpts)
			_ = rp.open(t, ctx)
			pl, err := rp.client.ListProviders(ctx)
			if err != nil {
				t.Fatalf("%s round %d ListProviders: %v", name, round, err)
			}
			gotOrder := make([]string, 0, len(pl.Accounts))
			for idx, row := range pl.Accounts {
				gotOrder = append(gotOrder, row.ProviderID)
				if row.Priority == nil || *row.Priority != idx+1 || row.CredentialStatus != "credential_missing" {
					t.Fatalf("%s round %d account %d mismatch: %+v", name, round, idx, row)
				}
				if expectRTFields && row.ProviderID == "legacyA" {
					if row.EnabledModels == nil || len(*row.EnabledModels) != 0 ||
						row.FallbackOnly == nil || !*row.FallbackOnly ||
						row.ModelOverrides == nil || len(*row.ModelOverrides) != 1 || (*row.ModelOverrides)[0].ID != "legacy-chat" {
						t.Fatalf("%s round %d lost RT fields on legacyA: %+v", name, round, row)
					}
				}
			}
			if !slices.Equal(gotOrder, wantOrder) || pl.ResolvedRoles.Dialogue != nil {
				t.Fatalf("%s round %d order/routes mismatch: got=%v want=%v resolved=%+v", name, round, gotOrder, wantOrder, pl.ResolvedRoles)
			}
			st, err := rp.client.SteerStatus(ctx, pi.ProductSteerStatusParams{SteerID: "steer-v10"})
			if err != nil || st.State != "delivered" || st.OperationID != "op-steer-v10" {
				t.Fatalf("%s round %d lost steer receipt: %+v, %v", name, round, st, err)
			}
			op, err := rp.client.Operation(ctx, "op-steer-v10")
			if err != nil || op.State != "accepted" {
				t.Fatalf("%s round %d lost accepted steer operation: %+v, %v", name, round, op, err)
			}
			if round == 1 {
				draft, err := rp.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
					ProductConversationParams: pi.ProductConversationParams{ConversationID: "conv-v10"},
					ProductText:               pi.ProductText{Text: "Blocked without key"},
					ExpectedRevision:          0,
					OperationID:               "op-restored-draft",
				})
				if err != nil {
					t.Fatal(err)
				}
				recRestored, err := rp.client.Send(ctx, pi.ProductSendParams{
					ProductConversationParams: pi.ProductConversationParams{ConversationID: "conv-v10"},
					DraftRevision:             draft.Revision,
					OperationID:               "op-restored-send",
				})
				if err != nil || recRestored.State != "rejected" {
					t.Fatalf("Send on restored credential-free profile = %+v, %v", recRestored, err)
				}
				var admRestored pi.ProductRouteAdmission
				if err := json.Unmarshal(recRestored.Result, &admRestored); err != nil || admRestored.Code != pi.ProductCredentialUnavailable || admRestored.Delivery != "non_dispatched" || admRestored.Reason != "credential_missing" {
					t.Fatalf("restored credential-free admission = %s (%+v), err=%v", string(recRestored.Result), admRestored, err)
				}
			}
			rp.shutdown(t, ctx)
		}
		schema := runNodeSQLite(t, nodePath, filepath.Join(destDir, "nagi.db"), `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.close();
console.log(JSON.stringify({ v, fk }));
`)
		if schema != `{"v":11,"fk":0}` {
			t.Fatalf("%s restored SQLite schema mismatch: %s", name, schema)
		}
	}

	restoreAndVerify("restored-v11", v11BackupPath, []string{"legacyB", "legacyA"}, true)
	restoreAndVerify("restored-legacy-v10", legacyBackupPath, []string{"legacyA", "legacyB"}, false)

	if h.requestCount() != callsBeforeMig {
		t.Fatalf("migration and restores triggered %d provider calls, want 0", h.requestCount()-callsBeforeMig)
	}
	h.assertClean()
}
