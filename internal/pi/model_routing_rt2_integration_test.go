//go:build integration

package pi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
)

type rt2Req struct {
	Path       string
	Method     string
	Key        string
	Model      string
	LastRole   string
	LatestUser string
	FirstUser  string
	RawBody    string
}

type rt2Action struct {
	Status       int
	RetryAfter   string
	Body         string
	Text         string
	FinishReason string
	ToolCall     *mcpProviderStep
	DropStream   bool
	HoldEntered  chan struct{}
	HoldRelease  <-chan struct{}
	Models404    bool
	Models       []string
}

type rt2MockServer struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	handler  func(req rt2Req, r *http.Request) rt2Action
	requests []rt2Req
	inFlight atomic.Int64
	maxFly   atomic.Int64
	seq      atomic.Int64
}

func newRT2MockServer(t *testing.T, handler func(req rt2Req, r *http.Request) rt2Action) *rt2MockServer {
	t.Helper()
	m := &rt2MockServer{t: t, handler: handler}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}

func (m *rt2MockServer) setHandler(fn func(req rt2Req, r *http.Request) rt2Action) {
	m.mu.Lock()
	m.handler = fn
	m.mu.Unlock()
}

func (m *rt2MockServer) countAll() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *rt2MockServer) countPosts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.requests {
		if r.Method == http.MethodPost {
			n++
		}
	}
	return n
}

func (m *rt2MockServer) countKeyPosts(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.requests {
		if r.Method == http.MethodPost && r.Key == key {
			n++
		}
	}
	return n
}

func (m *rt2MockServer) postsSnapshot() []rt2Req {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []rt2Req
	for _, r := range m.requests {
		if r.Method == http.MethodPost {
			out = append(out, r)
		}
	}
	return out
}

func (m *rt2MockServer) serve(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	rawBytes, _ := io.ReadAll(r.Body)
	rawBody := string(rawBytes)
	rec := rt2Req{Path: r.URL.Path, Method: r.Method, Key: key, RawBody: rawBody}
	if r.Method == http.MethodPost && len(rawBytes) > 0 {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Input []struct {
				Role    string          `json:"role"`
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
		}
		if json.Unmarshal(rawBytes, &body) == nil {
			rec.Model = body.Model
			for _, msg := range body.Messages {
				if msg.Role == "user" {
					txt := rt2StripBoard(decodeChatText(msg.Content))
					if rec.FirstUser == "" {
						rec.FirstUser = txt
					}
					rec.LatestUser = txt
				}
			}
			if len(body.Messages) > 0 {
				rec.LastRole = body.Messages[len(body.Messages)-1].Role
			}
			for _, item := range body.Input {
				if item.Role == "user" {
					txt := rt2StripBoard(decodeChatText(item.Content))
					if rec.FirstUser == "" {
						rec.FirstUser = txt
					}
					rec.LatestUser = txt
				}
			}
			if rec.LastRole == "" && len(body.Input) > 0 {
				rec.LastRole = body.Input[len(body.Input)-1].Role
			}
		}
	}
	m.mu.Lock()
	m.requests = append(m.requests, rec)
	h := m.handler
	m.mu.Unlock()

	var act rt2Action
	if h != nil {
		act = h(rec, r)
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
		if act.Models404 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		models := act.Models
		if len(models) == 0 {
			models = []string{"gpt-4o", "gpt-4o-mini"}
		}
		data := make([]map[string]string, 0, len(models))
		for _, id := range models {
			data = append(data, map[string]string{"id": id})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		return
	}
	cur := m.inFlight.Add(1)
	for {
		prev := m.maxFly.Load()
		if cur <= prev || m.maxFly.CompareAndSwap(prev, cur) {
			break
		}
	}
	defer m.inFlight.Add(-1)

	if act.HoldEntered != nil {
		select {
		case act.HoldEntered <- struct{}{}:
		default:
		}
	}
	if act.HoldRelease != nil {
		select {
		case <-act.HoldRelease:
		case <-r.Context().Done():
			return
		}
	}
	if act.Status >= 400 {
		if act.RetryAfter != "" {
			w.Header().Set("Retry-After", act.RetryAfter)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(act.Status)
		body := act.Body
		if body == "" {
			body = fmt.Sprintf(`{"error":{"message":"controlled HTTP %d"}}`, act.Status)
		}
		_, _ = w.Write([]byte(body))
		return
	}
	isResponses := strings.HasSuffix(r.URL.Path, "/responses")
	if act.DropStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if isResponses {
			_, _ = fmt.Fprintf(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r_drop\",\"status\":\"in_progress\"}}\n\n")
			_, _ = fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"i1\",\"output_index\":0,\"content_index\":0,\"delta\":\"partial\"}\n\n")
		} else {
			chunk, _ := json.Marshal(map[string]any{
				"id": "c_drop", "object": "chat.completion.chunk", "created": 1, "model": rec.Model,
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "partial"}, "finish_reason": nil}},
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				time.Sleep(25 * time.Millisecond)
				_ = conn.Close()
			}
		}
		return
	}
	if isResponses {
		text := act.Text
		if text == "" {
			text = "OK"
		}
		n := m.seq.Add(1)
		respID := fmt.Sprintf("resp_rt2_%d", n)
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(ev string, payload map[string]any) {
			payload["type"] = ev
			raw, _ := json.Marshal(payload)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, raw)
		}
		send("response.created", map[string]any{"response": map[string]any{"id": respID, "status": "in_progress"}})
		send("response.output_text.delta", map[string]any{"item_id": "msg_1", "output_index": 0, "content_index": 0, "delta": text})
		send("response.completed", map[string]any{
			"response": map[string]any{
				"id":     respID,
				"status": "completed",
				"output": []any{map[string]any{
					"type":    "message",
					"id":      "msg_1",
					"role":    "assistant",
					"status":  "completed",
					"content": []any{map[string]any{"type": "output_text", "text": text}},
				}},
			},
		})
		return
	}
	if act.ToolCall != nil {
		args := act.ToolCall.Args
		if args == "" {
			args = "{}"
		}
		piChatResponse(w, "", act.ToolCall.Name, act.ToolCall.ID, args)
		return
	}
	if act.FinishReason != "" {
		text := act.Text
		if text == "" {
			text = "partial"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{
			"id": "c_fin", "object": "chat.completion.chunk", "created": 1, "model": rec.Model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": act.FinishReason}},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
		return
	}
	text := act.Text
	if text == "" {
		text = "OK from " + rec.Model
	}
	piChatResponse(w, text, "", "", "")
}

func rt2StripBoard(text string) string {
	if idx := strings.Index(text, "\n\n<active_activities>"); idx >= 0 {
		return text[:idx]
	}
	return text
}

func rt2SetupOfflinePreload(t *testing.T, serverURL string) {
	t.Helper()
	preloadPath := filepath.Join(t.TempDir(), "rt2-offline-preload.mjs")
	script := fmt.Sprintf(`
const target = new URL(%q);
const allowedHosts = new Set(['api.openai.com', 'openrouter.ai']);
const origFetch = globalThis.fetch;
globalThis.fetch = function(input, init) {
  const raw = input instanceof Request ? input.url : String(input);
  const u = new URL(raw);
  if (u.hostname === '127.0.0.1' || u.hostname === 'localhost') {
    return origFetch.call(this, input, init);
  }
  if (!allowedHosts.has(u.hostname)) {
    return Promise.reject(new Error('RT2_OFFLINE_BLOCKED: ' + raw));
  }
  const rewritten = new URL(target.origin + '/' + u.hostname + u.pathname + u.search);
  if (input instanceof Request) {
    return origFetch.call(this, new Request(rewritten, input), init);
  }
  return origFetch.call(this, rewritten, init);
};
`, serverURL)
	if err := os.WriteFile(preloadPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODE_OPTIONS", "--import "+preloadPath)
}

func rt2SendAndDecode(t *testing.T, ctx context.Context, proc *productProcess, convID, text, opBase string, materials ...pi.ProductMaterial) (pi.ProductReceipt, pi.ProductRouteAdmission, pi.ProductDraft) {
	t.Helper()
	conv, err := proc.client.GetConversation(ctx, convID)
	if err != nil || conv.Draft == nil {
		t.Fatalf("GetConversation(%s) = %+v, %v", convID, conv, err)
	}
	mats := materials
	if mats == nil {
		mats = []pi.ProductMaterial{}
	}
	draft, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
		ProductText:               pi.ProductText{Text: text, Materials: mats},
		ExpectedRevision:          conv.Draft.Revision,
		OperationID:               opBase + "-draft",
	})
	if err != nil {
		t.Fatalf("SaveDraft(%s): %v", opBase, err)
	}
	receipt, err := proc.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
		DraftRevision:             draft.Revision,
		OperationID:               opBase + "-send",
	})
	if err != nil {
		t.Fatalf("Send(%s): %v", opBase, err)
	}
	var adm pi.ProductRouteAdmission
	if len(receipt.Result) > 0 {
		if err := json.Unmarshal(receipt.Result, &adm); err != nil {
			t.Fatalf("unmarshal admission(%s) %s: %v", opBase, string(receipt.Result), err)
		}
	}
	return receipt, adm, draft
}

type rt2EventWatcher struct {
	mu             sync.Mutex
	all            []pi.ProductEvent
	providerEvents []pi.ProductProviderChange
	settled        map[string]pi.ProductExecutionEvent
	lastSeq        uint64
}

func newRT2EventWatcher() *rt2EventWatcher {
	return &rt2EventWatcher{settled: make(map[string]pi.ProductExecutionEvent)}
}

func (w *rt2EventWatcher) consume(t *testing.T, ev pi.ProductEvent) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if ev.Seq != w.lastSeq+1 {
		t.Fatalf("non-contiguous event seq: got %d after %d (%s)", ev.Seq, w.lastSeq, ev.Name)
	}
	w.lastSeq = ev.Seq
	w.all = append(w.all, ev)
	if ev.ProviderChanged != nil {
		switch ev.ProviderChanged.Change {
		case "saved", "deleted", "applied", "revoked", "policy":
		default:
			t.Fatalf("illegal provider.changed value outside M4 closed switch: %q", ev.ProviderChanged.Change)
		}
		w.providerEvents = append(w.providerEvents, *ev.ProviderChanged)
	}
	if ev.Name == "execution.settled" {
		var s pi.ProductExecutionEvent
		if err := json.Unmarshal(ev.Data, &s); err != nil {
			t.Fatal(err)
		}
		w.settled[s.ExecutionID] = s
	}
}

func (w *rt2EventWatcher) waitSettled(t *testing.T, ctx context.Context, proc *productProcess, execID string) pi.ProductExecutionEvent {
	t.Helper()
	for {
		w.mu.Lock()
		if s, ok := w.settled[execID]; ok {
			w.mu.Unlock()
			return s
		}
		w.mu.Unlock()
		w.consume(t, proc.event(t, ctx))
	}
}

func (w *rt2EventWatcher) waitProviderChange(t *testing.T, ctx context.Context, proc *productProcess, change string) pi.ProductProviderChange {
	t.Helper()
	for {
		w.mu.Lock()
		for i, ev := range w.providerEvents {
			if ev.Change == change {
				w.providerEvents = append(w.providerEvents[:i], w.providerEvents[i+1:]...)
				w.mu.Unlock()
				return ev
			}
		}
		w.mu.Unlock()
		w.consume(t, proc.event(t, ctx))
	}
}

func rt2BoolPtr(v bool) *bool           { return &v }
func rt2SlicePtr(v ...string) *[]string { s := append([]string(nil), v...); return &s }

func TestPiProductRT2PinsOverridesHotPrecedenceAndRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	nodePath, workerPath := mcpRequirePinnedWorker(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "ws")
	srcDir := filepath.Join(root, "src")
	for _, dir := range []string{ws, srcDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	const (
		keyA1 = "sk-rt2-key-a1-secret"
		keyA2 = "sk-rt2-key-a2-rotated"
		keyB  = "sk-rt2-key-b-fallback"
		keyC  = "sk-rt2-key-c-ordinary"
		keyD  = "sk-rt2-key-d-incompat"
	)
	allSecrets := []string{keyA1, keyA2, keyB, keyC, keyD}

	var mu sync.Mutex
	keyStatus := map[string]int{}
	retryAfter := map[string]string{}
	mock := newRT2MockServer(t, func(req rt2Req, _ *http.Request) rt2Action {
		if strings.HasPrefix(req.LatestUser, "DELEGATE_") && req.LastRole == "user" {
			goal := strings.TrimPrefix(req.LatestUser, "DELEGATE_")
			args, _ := json.Marshal(map[string]any{"key": "k_" + goal, "goal": goal, "kind": "coding", "complexity": "standard"})
			return rt2Action{ToolCall: &mcpProviderStep{Name: "activity_delegate", ID: "call_del_" + goal, Args: string(args)}}
		}
		mu.Lock()
		st := keyStatus[req.Key+":"+req.Model]
		ra := retryAfter[req.Key+":"+req.Model]
		mu.Unlock()
		if st >= 400 {
			return rt2Action{Status: st, RetryAfter: ra}
		}
		return rt2Action{Text: "OK:" + req.Key + ":" + req.Model}
	})
	rt2SetupOfflinePreload(t, mock.server.URL)
	t.Setenv("NAGI_HEALTH_MIN_COOLDOWN_MS", "5000")
	t.Setenv("NAGI_HEALTH_MAX_COOLDOWN_MS", "15000")
	t.Setenv("NAGI_HEALTH_BACKOFF_SCALE", "1")

	opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(srcDir, "pi"),
		ProfilePath:     filepath.Join(srcDir, "contacts.json"),
		CredentialsPath: filepath.Join(srcDir, "connection.json"),
		FreshProfile:    true,
	}
	proc := startProductProcess(t, opts)
	_ = proc.open(t, ctx)
	ev := newRT2EventWatcher()

	provA := pi.ProductProviderAccount{
		ProviderID:    "provA",
		Name:          "Prov A",
		WireAPI:       pi.OpenAICompletions,
		BaseURL:       "https://api.openai.com/v1",
		Models:        []string{"gpt-4o", "gpt-4o-mini", "gpt-5.4-pro", "text-embedding-3-small"},
		DefaultModel:  "gpt-4o",
		ContextTokens: 128000,
		OutputTokens:  8192,
		Enabled:       true,
	}
	provB := pi.ProductProviderAccount{
		ProviderID:    "provB",
		Name:          "Prov B Fallback",
		WireAPI:       pi.OpenAICompletions,
		BaseURL:       "https://openrouter.ai/api/v1",
		Models:        []string{"openai/gpt-4o"},
		DefaultModel:  "openai/gpt-4o",
		EnabledModels: rt2SlicePtr("openai/gpt-4o"),
		FallbackOnly:  rt2BoolPtr(true),
		ContextTokens: 128000,
		OutputTokens:  8192,
		Enabled:       true,
	}
	provC := pi.ProductProviderAccount{
		ProviderID:    "provC",
		Name:          "Prov C Ordinary",
		WireAPI:       pi.OpenAICompletions,
		BaseURL:       "https://api.openai.com/v1",
		Models:        []string{"gpt-4o-mini"},
		DefaultModel:  "gpt-4o-mini",
		EnabledModels: rt2SlicePtr("gpt-4o-mini"),
		ContextTokens: 128000,
		OutputTokens:  8192,
		Enabled:       true,
	}
	incompatOverrides := []pi.ProductModelOverride{{ID: "no-tools-m", IsChat: rt2BoolPtr(true), ToolCall: rt2BoolPtr(false)}}
	provD := pi.ProductProviderAccount{
		ProviderID:     "provD",
		Name:           "Prov D NoTools",
		WireAPI:        pi.OpenAICompletions,
		BaseURL:        mock.server.URL + "/provd/v1",
		Models:         []string{"no-tools-m"},
		DefaultModel:   "no-tools-m",
		EnabledModels:  rt2SlicePtr("no-tools-m"),
		ModelOverrides: &incompatOverrides,
		ContextTokens:  32768,
		OutputTokens:   4096,
		Enabled:        true,
	}

	var rev pi.ProductConnectionRevisions
	for _, item := range []struct {
		acc pi.ProductProviderAccount
		key string
		op  string
	}{
		{provA, keyA1, "op-save-a"},
		{provB, keyB, "op-save-b"},
		{provC, keyC, "op-save-c"},
		{provD, keyD, "op-save-d"},
	} {
		rev, err = proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
			ProductProviderTestParams: pi.ProductProviderTestParams{Account: item.acc, APIKey: item.key},
			OperationID:               item.op,
		})
		if err != nil {
			t.Fatal(err)
		}
		ev.waitProviderChange(t, ctx, proc, "saved")
	}
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-init"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")

	for _, bad := range []struct {
		name  string
		patch pi.ProductRoutingOverridesPatch
	}{
		{"unauthorized Pro SKU", pi.ProductRoutingOverridesPatch{"dialogue": {Mode: "route", ProviderID: "provA", Model: "gpt-5.4-pro"}}},
		{"non-chat embedding", pi.ProductRoutingOverridesPatch{"dialogue": {Mode: "route", ProviderID: "provA", Model: "text-embedding-3-small"}}},
		{"missing provider", pi.ProductRoutingOverridesPatch{"dialogue": {Mode: "route", ProviderID: "missing", Model: "gpt-4o"}}},
		{"client endpointFingerprint", pi.ProductRoutingOverridesPatch{"dialogue": {Mode: "route", ProviderID: "provA", Model: "gpt-4o", EndpointFingerprint: "forged"}}},
		{"unknown canonical model", pi.ProductRoutingOverridesPatch{"coding": {Mode: "model", CanonicalModelID: "openai/does-not-exist"}}},
	} {
		_, err := proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
			SavedRevision: *rev.SavedRevision,
			Overrides:     bad.patch,
			OperationID:   "op-bad-" + bad.name,
		})
		mcpExpectRemoteCode(t, err, "invalid_params", "SetRoutingOverrides "+bad.name)
	}

	effBeforeOv := *rev.EffectiveRevision
	ov1Params := pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides: pi.ProductRoutingOverridesPatch{
			"dialogue": {Mode: "route", ProviderID: "provA", Model: "gpt-4o"},
			"coding":   {Mode: "model", CanonicalModelID: "openai/gpt-4o"},
		},
		OperationID: "op-ov-1",
	}
	revOv1, err := proc.client.SetRoutingOverrides(ctx, ov1Params)
	if err != nil || *revOv1.SavedRevision != *rev.SavedRevision+1 || *revOv1.EffectiveRevision != effBeforeOv {
		t.Fatalf("SetRoutingOverrides 1 = %+v, %v", revOv1, err)
	}
	savedChg := ev.waitProviderChange(t, ctx, proc, "saved")
	if *savedChg.SavedRevision != *revOv1.SavedRevision || *savedChg.EffectiveRevision != effBeforeOv {
		t.Fatalf("provider.changed after setOverrides = %+v", savedChg)
	}
	rtPending, err := proc.client.Routing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rtPending.Overrides["dialogue"].EndpointFingerprint != "openai-completions|https://api.openai.com/v1" ||
		rtPending.Overrides["coding"].CanonicalModelID != "openai/gpt-4o" ||
		len(rtPending.EffectiveOverrides) != 0 {
		t.Fatalf("pending overrides mismatch: saved=%+v eff=%+v", rtPending.Overrides, rtPending.EffectiveOverrides)
	}

	revOv2, err := proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *revOv1.SavedRevision,
		Overrides: pi.ProductRoutingOverridesPatch{
			"utility":   {Mode: "route", ProviderID: "provC", Model: "gpt-4o-mini"},
			"reasoning": nil,
		},
		OperationID: "op-ov-2",
	})
	if err != nil || *revOv2.SavedRevision != *revOv1.SavedRevision+1 {
		t.Fatalf("SetRoutingOverrides 2 = %+v, %v", revOv2, err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	replayOv1, err := proc.client.SetRoutingOverrides(ctx, ov1Params)
	if err != nil || !reflect.DeepEqual(replayOv1, revOv1) {
		t.Fatalf("duplicate operationId replay before CAS = %+v, want %+v, err=%v", replayOv1, revOv1, err)
	}
	_, err = proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *revOv1.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"utility": nil},
		OperationID:   "op-ov-stale",
	})
	mcpExpectRemoteCode(t, err, "stale_revision", "stale savedRevision setOverrides")

	rtPatch, err := proc.client.Routing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rtPatch.Overrides["dialogue"].ProviderID != "provA" || rtPatch.Overrides["coding"].CanonicalModelID != "openai/gpt-4o" || rtPatch.Overrides["utility"].ProviderID != "provC" {
		t.Fatalf("presence-preserving patch lost slots: %+v", rtPatch.Overrides)
	}

	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *revOv2.SavedRevision, OperationID: "op-apply-ov"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")

	botHot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-hot",
		Profile:     pi.ProductBotProfile{Name: "HotBot", Workspace: ws, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	botFresh, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-fresh",
		Profile:     pi.ProductBotProfile{Name: "FreshBot", Workspace: ws, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec1, adm1, _ := rt2SendAndDecode(t, ctx, proc, botFresh.CurrentConversationID, "PIN_READY", "op-pin-ready")
	if rec1.State != "accepted" || adm1.Route == nil || adm1.Route.Source != "pinned_route" || adm1.Route.Reason != "exact_pin" || adm1.Route.ProviderID != "provA" || adm1.Route.ProviderName != "Prov A" {
		t.Fatalf("pinned_route accepted receipt mismatch: %+v adm=%+v", rec1, adm1)
	}
	if s := ev.waitSettled(t, ctx, proc, rec1.Execution.ExecutionID); s.State != "completed" {
		t.Fatalf("PIN_READY settled = %+v", s)
	}

	delPending, err := proc.client.DeleteProvider(ctx, pi.ProductProviderTargetParams{ProviderID: "provA", OperationID: "op-del-a-pending"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "deleted")
	listPendingDel, err := proc.client.ListProviders(ctx)
	if err != nil || listPendingDel.ResolvedRoles.Dialogue == nil || listPendingDel.ResolvedRoles.Dialogue.ProviderName != "Prov A" {
		t.Fatalf("pending delete lost effective providerName: %+v, %v", listPendingDel.ResolvedRoles.Dialogue, err)
	}
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *delPending.SavedRevision, OperationID: "op-apply-del-a"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")

	rtDel, err := proc.client.Routing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rtDel.ResolvedRoles.Dialogue != nil || rtDel.RoleStatus["dialogue"].Status != "pin_unavailable" || rtDel.RoleStatus["dialogue"].UnavailableReason != "provider_deleted" || rtDel.EffectiveOverrides["dialogue"].ProviderID != "provA" {
		t.Fatalf("deleted pinned_route status mismatch: roles=%+v status=%+v effOv=%+v", rtDel.ResolvedRoles.Dialogue, rtDel.RoleStatus["dialogue"], rtDel.EffectiveOverrides)
	}
	if rtDel.ResolvedRoles.Coding == nil || rtDel.ResolvedRoles.Coding.ProviderID != "provB" || rtDel.ResolvedRoles.Coding.Source != "pinned_model" {
		t.Fatalf("pinned_model coding did not fall back to provB after provA delete: %+v", rtDel.ResolvedRoles.Coding)
	}

	postsBeforeBlocked := mock.countPosts()
	recDel, admDel, draftDel := rt2SendAndDecode(t, ctx, proc, botHot.CurrentConversationID, "BLOCKED_DELETED", "op-blk-del")
	if recDel.State != "rejected" || admDel.Code != pi.ProductRouteUnavailable || admDel.Delivery != "non_dispatched" || admDel.Reason != "provider_deleted" {
		t.Fatalf("deleted pin send mismatch: %+v %+v", recDel, admDel)
	}
	convCheck, _ := proc.client.GetConversation(ctx, botHot.CurrentConversationID)
	if convCheck.Draft == nil || !reflect.DeepEqual(*convCheck.Draft, draftDel) {
		t.Fatalf("rejected send lost draft: got=%+v want=%+v", convCheck.Draft, draftDel)
	}

	recycledA := provA
	recycledA.BaseURL = mock.server.URL + "/hijacked-endpoint/v1"
	rev, err = proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: recycledA, APIKey: keyA1},
		OperationID:               "op-recycle-a-diff-url",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-recycle-a"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")
	rtEndpoint, _ := proc.client.Routing(ctx)
	if rtEndpoint.ResolvedRoles.Dialogue != nil || rtEndpoint.RoleStatus["dialogue"].UnavailableReason != "endpoint_changed" {
		t.Fatalf("recycled providerId different URL did not fail closed with endpoint_changed: %+v", rtEndpoint.RoleStatus["dialogue"])
	}
	recEp, admEp, _ := rt2SendAndDecode(t, ctx, proc, botHot.CurrentConversationID, "BLOCKED_ENDPOINT", "op-blk-ep")
	if recEp.State != "rejected" || admEp.Code != pi.ProductRouteUnavailable || admEp.Reason != "endpoint_changed" {
		t.Fatalf("endpoint_changed send mismatch: %+v %+v", recEp, admEp)
	}

	disabledA := provA
	disabledA.Enabled = false
	rev, err = proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: disabledA, APIKey: keyA1},
		OperationID:               "op-restore-a-disabled",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-a-disabled"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")
	recDis, admDis, _ := rt2SendAndDecode(t, ctx, proc, botHot.CurrentConversationID, "BLOCKED_DISABLED", "op-blk-dis")
	if recDis.State != "rejected" || admDis.Code != pi.ProductRouteUnavailable || admDis.Reason != "provider_disabled" {
		t.Fatalf("provider_disabled send mismatch: %+v %+v", recDis, admDis)
	}

	excludedA := provA
	excludedA.EnabledModels = rt2SlicePtr("gpt-4o-mini")
	rev, err = proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: excludedA, APIKey: keyA1},
		OperationID:               "op-a-excluded",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-a-excluded"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")
	recExcl, admExcl, _ := rt2SendAndDecode(t, ctx, proc, botHot.CurrentConversationID, "BLOCKED_EXCLUDED", "op-blk-excl")
	if recExcl.State != "rejected" || admExcl.Code != pi.ProductRouteUnavailable || admExcl.Reason != "model_disabled" {
		t.Fatalf("model_disabled send mismatch: %+v %+v", recExcl, admExcl)
	}

	rev, err = proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"dialogue": {Mode: "route", ProviderID: "provD", Model: "no-tools-m"}},
		OperationID:   "op-pin-incompat",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-pin-incompat"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")
	recIncompat, admIncompat, _ := rt2SendAndDecode(t, ctx, proc, botHot.CurrentConversationID, "BLOCKED_INCOMPAT", "op-blk-incompat")
	if recIncompat.State != "rejected" || admIncompat.Code != pi.ProductRouteUnavailable || admIncompat.Reason != "incompatible" {
		t.Fatalf("incompatible pinned_route send mismatch: %+v %+v", recIncompat, admIncompat)
	}
	if mock.countPosts() != postsBeforeBlocked {
		t.Fatalf("blocked pin checks sent %d unexpected provider POSTs", mock.countPosts()-postsBeforeBlocked)
	}

	provAEnabled := provA
	provAEnabled.EnabledModels = rt2SlicePtr("gpt-4o", "gpt-4o-mini")
	rev, err = proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: provAEnabled, APIKey: keyA1},
		OperationID:               "op-a-back",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"dialogue": {Mode: "route", ProviderID: "provA", Model: "gpt-4o"}},
		OperationID:   "op-pin-a-back",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-a-back"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")

	mu.Lock()
	keyStatus[keyA1+":gpt-4o"] = http.StatusUnauthorized
	mu.Unlock()
	botAuth, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-auth",
		Profile:     pi.ProductBotProfile{Name: "AuthBot", Workspace: ws, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec401, _, _ := rt2SendAndDecode(t, ctx, proc, botAuth.CurrentConversationID, "TRIGGER_401", "op-401")
	if s := ev.waitSettled(t, ctx, proc, rec401.Execution.ExecutionID); s.State != "failed" {
		t.Fatalf("401 run settled = %+v, want failed", s)
	}
	list401, _ := proc.client.ListProviders(ctx)
	if list401.RoleStatus["dialogue"].Status != "auth_failed" || list401.RoleStatus["dialogue"].UnavailableReason != "auth_failed" {
		t.Fatalf("401 roleStatus mismatch: %+v", list401.RoleStatus["dialogue"])
	}
	var aHealth string
	for _, acc := range list401.Accounts {
		if acc.ProviderID == "provA" {
			aHealth = acc.HealthSummary
		}
	}
	if aHealth != "auth_failed" {
		t.Fatalf("provA healthSummary after 401 = %q, want auth_failed", aHealth)
	}
	rev, err = proc.client.SetRoutingPolicy(ctx, pi.ProductRoutingPolicyParams{Policy: pi.ProductRoutingQuality, OperationID: "op-unrelated-pol"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "policy")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-unrelated-apply"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")
	rtStill401, _ := proc.client.Routing(ctx)
	if rtStill401.RoleStatus["dialogue"].Status != "auth_failed" {
		t.Fatalf("unrelated Apply cleared 401 rejection: %+v", rtStill401.RoleStatus["dialogue"])
	}
	recBlocked401, admBlocked401, _ := rt2SendAndDecode(t, ctx, proc, botHot.CurrentConversationID, "BLOCKED_401", "op-blk-401")
	if recBlocked401.State != "rejected" || admBlocked401.Code != pi.ProductCredentialUnavailable || admBlocked401.Reason != "auth_failed" {
		t.Fatalf("401 blocked send = %+v %+v", recBlocked401, admBlocked401)
	}

	rev, err = proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: provAEnabled, APIKey: keyA2},
		OperationID:               "op-rotate-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-rotate-a"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")
	recRot, admRot, _ := rt2SendAndDecode(t, ctx, proc, botHot.CurrentConversationID, "AFTER_KEY_ROTATION", "op-after-rot")
	if recRot.State != "accepted" || admRot.Route == nil || admRot.Route.ProviderID != "provA" {
		t.Fatalf("same-endpoint key rotation did not restore pinned route: %+v %+v", recRot, admRot)
	}
	if s := ev.waitSettled(t, ctx, proc, recRot.Execution.ExecutionID); s.State != "completed" {
		t.Fatalf("AFTER_KEY_ROTATION settled = %+v", s)
	}

	rev, err = proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"dialogue": {Mode: "model", CanonicalModelID: "openai/gpt-4o"}},
		OperationID:   "op-pin-model-dlg",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-pin-model-dlg"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")
	mu.Lock()
	keyStatus[keyA2+":gpt-4o"] = http.StatusTooManyRequests
	retryAfter[keyA2+":gpt-4o"] = "10"
	mu.Unlock()
	botMod1, _ := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-mod1",
		Profile:     pi.ProductBotProfile{Name: "Mod1", Workspace: ws, CodingTools: []string{}},
	})
	recCoolA, _, _ := rt2SendAndDecode(t, ctx, proc, botMod1.CurrentConversationID, "COOL_A_MODEL_PIN", "op-cool-a-mp")
	if s := ev.waitSettled(t, ctx, proc, recCoolA.Execution.ExecutionID); s.State != "failed" {
		t.Fatalf("COOL_A_MODEL_PIN settled = %+v", s)
	}
	botMod2, _ := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-mod2",
		Profile:     pi.ProductBotProfile{Name: "Mod2", Workspace: ws, CodingTools: []string{}},
	})
	recFailB, admFailB, _ := rt2SendAndDecode(t, ctx, proc, botMod2.CurrentConversationID, "PIN_MODEL_TO_B", "op-mp-b")
	if recFailB.State != "accepted" || admFailB.Route == nil || admFailB.Route.ProviderID != "provB" || admFailB.Route.Source != "pinned_model" || admFailB.Route.Failover != "same_model_cooldown" || admFailB.Route.CanonicalModelID != "openai/gpt-4o" {
		t.Fatalf("pinned_model failover to provB mismatch: %+v %+v", recFailB, admFailB)
	}
	if s := ev.waitSettled(t, ctx, proc, recFailB.Execution.ExecutionID); s.State != "completed" {
		t.Fatalf("PIN_MODEL_TO_B settled = %+v", s)
	}
	mu.Lock()
	keyStatus[keyB+":openai/gpt-4o"] = http.StatusTooManyRequests
	retryAfter[keyB+":openai/gpt-4o"] = "10"
	mu.Unlock()
	botMod3, _ := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-mod3",
		Profile:     pi.ProductBotProfile{Name: "Mod3", Workspace: ws, CodingTools: []string{}},
	})
	recCoolB, _, _ := rt2SendAndDecode(t, ctx, proc, botMod3.CurrentConversationID, "COOL_B_MODEL_PIN", "op-cool-b-mp")
	if s := ev.waitSettled(t, ctx, proc, recCoolB.Execution.ExecutionID); s.State != "failed" {
		t.Fatalf("COOL_B_MODEL_PIN settled = %+v", s)
	}
	cPostsBefore := mock.countKeyPosts(keyC)
	botMod4, _ := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-mod4",
		Profile:     pi.ProductBotProfile{Name: "Mod4", Workspace: ws, CodingTools: []string{}},
	})
	recExh, admExh, _ := rt2SendAndDecode(t, ctx, proc, botMod4.CurrentConversationID, "PIN_MODEL_EXHAUSTED", "op-mp-exh")
	if recExh.State != "rejected" || admExh.Code != pi.ProductRouteUnavailable || admExh.Reason != "cooldown" || admExh.RetryAt == "" {
		t.Fatalf("exhausted pinned_model mismatch: %+v %+v", recExh, admExh)
	}
	if mock.countKeyPosts(keyC) != cPostsBefore {
		t.Fatalf("exhausted pinned_model fell back to unrelated model on provC!")
	}
	mu.Lock()
	delete(keyStatus, keyA2+":gpt-4o")
	delete(keyStatus, keyB+":openai/gpt-4o")
	mu.Unlock()

	rev, err = proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"dialogue": nil, "coding": nil, "utility": nil, "reasoning": nil},
		OperationID:   "op-clear-all-ov",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-clear-ov"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")

	botSticky, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-sticky",
		Profile:     pi.ProductBotProfile{Name: "Sticky", Workspace: ws, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	recSt1, admSt1, _ := rt2SendAndDecode(t, ctx, proc, botSticky.CurrentConversationID, "HOT_AUTO_1", "op-st1")
	if recSt1.State != "accepted" || admSt1.Route == nil || admSt1.Route.ProviderID != "provC" {
		t.Fatalf("HOT_AUTO_1 expected provC (provA/provB cooling): %+v %+v", recSt1, admSt1)
	}
	if s := ev.waitSettled(t, ctx, proc, recSt1.Execution.ExecutionID); s.State != "completed" {
		t.Fatalf("HOT_AUTO_1 settled = %+v", s)
	}
	dbPath := filepath.Join(srcDir, "xuhu.db")
	sessionBeforePin := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const row = db.prepare('SELECT binding_json FROM conversations WHERE conversation_id = ?').get(JSON.stringify(process.argv[2]));
db.close();
console.log(JSON.parse(row.binding_json).sessionRef);
`, botSticky.CurrentConversationID)

	rev, err = proc.client.ReorderProviders(ctx, pi.ProductProviderReorderParams{
		ProviderIDs:   []string{"provB", "provA", "provD", "provC"},
		SavedRevision: *rev.SavedRevision,
		OperationID:   "op-reorder-hot",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.SetRoutingPolicy(ctx, pi.ProductRoutingPolicyParams{Policy: pi.ProductRoutingEconomy, OperationID: "op-pol-econ"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "policy")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-hot-reorder"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")

	recSt2, admSt2, _ := rt2SendAndDecode(t, ctx, proc, botSticky.CurrentConversationID, "HOT_AUTO_2", "op-st2")
	if recSt2.State != "accepted" || admSt2.Route == nil || admSt2.Route.ProviderID != "provC" || admSt2.Route.ResolvedModel != "gpt-4o-mini" {
		t.Fatalf("hot Auto conversation broke stickiness across reorder/policy: %+v %+v", recSt2, admSt2)
	}
	if s := ev.waitSettled(t, ctx, proc, recSt2.Execution.ExecutionID); s.State != "completed" {
		t.Fatalf("HOT_AUTO_2 settled = %+v", s)
	}

	rev, err = proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"dialogue": {Mode: "route", ProviderID: "provA", Model: "gpt-4o-mini"}},
		OperationID:   "op-incompat-pin-hot",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-incompat-pin-hot"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")

	recSt3, admSt3, _ := rt2SendAndDecode(t, ctx, proc, botSticky.CurrentConversationID, "HOT_EXPLICIT_PIN", "op-st3")
	if recSt3.State != "accepted" || admSt3.Route == nil || admSt3.Route.ProviderID != "provA" || admSt3.Route.ResolvedModel != "gpt-4o-mini" || admSt3.Route.Source != "pinned_route" {
		t.Fatalf("explicitly applied incompatible pin did not take effect on next hot turn: %+v %+v", recSt3, admSt3)
	}
	if s := ev.waitSettled(t, ctx, proc, recSt3.Execution.ExecutionID); s.State != "completed" {
		t.Fatalf("HOT_EXPLICIT_PIN settled = %+v", s)
	}
	sessionAfterPin := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const row = db.prepare('SELECT binding_json FROM conversations WHERE conversation_id = ?').get(JSON.stringify(process.argv[2]));
db.close();
console.log(JSON.parse(row.binding_json).sessionRef);
`, botSticky.CurrentConversationID)
	if sessionAfterPin != sessionBeforePin {
		t.Fatalf("hot pin rebind changed sessionRef: before=%q after=%q", sessionBeforePin, sessionAfterPin)
	}
	hist, err := proc.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botSticky.CurrentConversationID},
		ProductPageParams:         pi.ProductPageParams{Limit: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	seenTurns := map[string]bool{}
	for _, item := range hist.Items {
		if item.Role == "user" {
			seenTurns[item.Text] = true
		}
	}
	if !seenTurns["HOT_AUTO_1"] || !seenTurns["HOT_AUTO_2"] || !seenTurns["HOT_EXPLICIT_PIN"] {
		t.Fatalf("hot pin rebind lost transcript history: %+v", seenTurns)
	}

	rev, err = proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"coding": {Mode: "route", ProviderID: "provA", Model: "gpt-4o-mini"}},
		OperationID:   "op-pin-coding-cont",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-coding-cont"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")

	recDelAct, _, _ := rt2SendAndDecode(t, ctx, proc, botSticky.CurrentConversationID, "DELEGATE_CONT_PIN", "op-del-cont")
	if s := ev.waitSettled(t, ctx, proc, recDelAct.Execution.ExecutionID); s.State != "completed" {
		t.Fatalf("DELEGATE_CONT_PIN settled = %+v", s)
	}
	acts, err := proc.client.ListActivities(ctx, pi.ProductActivityListParams{BotID: botSticky.BotID})
	if err != nil || len(acts.Items) == 0 {
		t.Fatalf("ListActivities = %+v, %v", acts, err)
	}
	contActID := acts.Items[0].ActivityID
	contAct, err := proc.client.GetActivity(ctx, contActID)
	if err != nil || len(contAct.Executions) == 0 {
		t.Fatalf("GetActivity = %+v, %v", contAct, err)
	}
	if s := ev.waitSettled(t, ctx, proc, contAct.Executions[0].ExecutionID); s.State != "completed" {
		t.Fatalf("CONT_PIN exec 1 settled = %+v", s)
	}
	contAct, _ = proc.client.GetActivity(ctx, contActID)

	mu.Lock()
	keyStatus[keyA2+":gpt-4o-mini"] = http.StatusTooManyRequests
	retryAfter[keyA2+":gpt-4o-mini"] = "10"
	mu.Unlock()
	recCoolMini, _, _ := rt2SendAndDecode(t, ctx, proc, botSticky.CurrentConversationID, "COOL_A_MINI", "op-cool-a-mini")
	if s := ev.waitSettled(t, ctx, proc, recCoolMini.Execution.ExecutionID); s.State != "failed" {
		t.Fatalf("COOL_A_MINI settled = %+v", s)
	}
	postsBeforeCont := mock.countPosts()
	contRes, err := proc.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: contActID},
		ExpectedRevision:      contAct.Revision,
		ProductText:           pi.ProductText{Text: "continue on cooled captured route"},
		OperationID:           "op-cont-cooled",
	})
	if err != nil || contRes.State != "rejected" {
		t.Fatalf("manual activity.continue on cooled captured route = %+v, %v", contRes, err)
	}
	var contAdm pi.ProductRouteAdmission
	if err := json.Unmarshal(contRes.Result, &contAdm); err != nil || contAdm.Code != pi.ProductRouteUnavailable || contAdm.Reason != "cooldown" || contAdm.Delivery != "non_dispatched" {
		t.Fatalf("manual activity.continue rejected admission mismatch: %s (%+v)", string(contRes.Result), contAdm)
	}
	if mock.countPosts() != postsBeforeCont {
		t.Fatalf("manual activity.continue leaked %d requests onto another provider", mock.countPosts()-postsBeforeCont)
	}

	rev, err = proc.client.DeleteProvider(ctx, pi.ProductProviderTargetParams{ProviderID: "provA", OperationID: "op-del-a-dangling"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "deleted")
	rev, err = proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"utility": {Mode: "route", ProviderID: "provC", Model: "gpt-4o-mini"}},
		OperationID:   "op-saved-only-util-ov",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")

	backupPath := filepath.Join(root, "rt2-backup.json")
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	for _, checkPath := range []string{backupPath, opts.ProfilePath, dbPath, filepath.Join(srcDir, "xuhu.db-wal")} {
		mcpAssertNoSecretBytes(t, checkPath, allSecrets)
	}
	var v3Conn struct {
		Version  int              `json:"version"`
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(productRead(t, opts.CredentialsPath), &v3Conn); err != nil || v3Conn.Version != 3 {
		t.Fatalf("connection.json v3 decode failed: %+v, %v", v3Conn, err)
	}
	for _, acc := range v3Conn.Accounts {
		for _, forbidden := range []string{"overrides", "effectiveOverrides", "routeHealth", "healthSummary", "enabledModels", "fallbackOnly", "modelOverrides", "priority"} {
			if _, ok := acc[forbidden]; ok {
				t.Fatalf("connection.json v3 leaked forbidden field %q: %+v", forbidden, acc)
			}
		}
	}
	if bytes.Contains(productRead(t, opts.CredentialsPath), []byte("routingOverrides")) || bytes.Contains(productRead(t, opts.CredentialsPath), []byte("routeHealth")) {
		t.Fatal("connection.json v3 leaked top-level routing/health metadata")
	}
	proc.shutdown(t, ctx)

	restoreDir := filepath.Join(root, "rt2-restored")
	if err := os.MkdirAll(restoreDir, 0o700); err != nil {
		t.Fatal(err)
	}
	restoreProfile := filepath.Join(restoreDir, "contacts.json")
	if err := os.WriteFile(restoreProfile, []byte("{damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	copyInfo, err := store.InspectStoreRecoveryCopy(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	targetInfo, err := store.InspectStoreRecoveryTarget(restoreProfile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RestoreStoreRecoveryCopyWithRename(copyInfo, targetInfo, os.Rename); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(restoreDir, "connection.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("independent restore unexpectedly created connection.json: %v", err)
	}

	postsBeforeRestore := mock.countAll()
	restoreOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(restoreDir, "pi"),
		ProfilePath:     restoreProfile,
		CredentialsPath: filepath.Join(restoreDir, "connection.json"),
	}
	for reopen := range 2 {
		rProc := startProductProcess(t, restoreOpts)
		_ = rProc.open(t, ctx)
		rtRest, err := rProc.client.Routing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if rtRest.Overrides["dialogue"].ProviderID != "provA" ||
			rtRest.Overrides["utility"].ProviderID != "provC" ||
			rtRest.EffectiveOverrides["dialogue"].ProviderID != "provA" ||
			rtRest.EffectiveOverrides["utility"].ProviderID != "" {
			t.Fatalf("reopen %d lost saved/effective override separation or dangling pin: saved=%+v eff=%+v", reopen, rtRest.Overrides, rtRest.EffectiveOverrides)
		}
		if rtRest.ResolvedRoles.Dialogue != nil || rtRest.RoleStatus["dialogue"].Status != "credential_missing" {
			t.Fatalf("reopen %d restored credential-free status mismatch: roles=%+v status=%+v", reopen, rtRest.ResolvedRoles.Dialogue, rtRest.RoleStatus["dialogue"])
		}
		recNoKey, admNoKey, _ := rt2SendAndDecode(t, ctx, rProc, botFresh.CurrentConversationID, "RESTORED_NO_KEY", fmt.Sprintf("op-rest-send-%d", reopen))
		if recNoKey.State != "rejected" || admNoKey.Code != pi.ProductCredentialUnavailable || admNoKey.Delivery != "non_dispatched" {
			t.Fatalf("reopen %d restored send did not return non-dispatched credential_unavailable: %+v %+v", reopen, recNoKey, admNoKey)
		}
		rProc.shutdown(t, ctx)
	}
	if mock.countAll() != postsBeforeRestore {
		t.Fatalf("credential-free restore/reopen emitted %d provider calls, want 0", mock.countAll()-postsBeforeRestore)
	}
	schemaCheck := runNodeSQLite(t, nodePath, filepath.Join(restoreDir, "xuhu.db"), `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.close();
console.log(JSON.stringify({ v, fk }));
`)
	if schemaCheck != `{"v":14,"fk":0}` {
		t.Fatalf("restored RT-2 SQLite check = %s, want v14 fk=0", schemaCheck)
	}
}

func TestPiProductRT2HealthFailoverHalfOpenGatesAndM4StreamCompat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	nodePath, workerPath := mcpRequirePinnedWorker(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "ws")
	dir := filepath.Join(root, "run")
	for _, d := range []string{ws, dir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	const (
		keyA      = "sk-rt2-h-a"
		keyB      = "sk-rt2-h-b"
		keyC      = "sk-rt2-h-c"
		keyResp   = "sk-rt2-h-resp"
		keySmall  = "sk-rt2-h-small"
		keyLarge  = "sk-rt2-h-large"
		keyOther  = "sk-rt2-h-other-candidate"
		keyClosed = "sk-rt2-h-closed"
	)

	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := closedLn.Addr().String()
	_ = closedLn.Close()

	var mu sync.Mutex
	var customHandler func(req rt2Req, r *http.Request) rt2Action
	mock := newRT2MockServer(t, func(req rt2Req, r *http.Request) rt2Action {
		mu.Lock()
		fn := customHandler
		mu.Unlock()
		if fn != nil {
			return fn(req, r)
		}
		return rt2Action{Text: "OK:" + req.Key + ":" + req.Model}
	})
	setMock := func(fn func(req rt2Req, r *http.Request) rt2Action) {
		mu.Lock()
		customHandler = fn
		mu.Unlock()
	}
	rt2SetupOfflinePreload(t, mock.server.URL)
	t.Setenv("NAGI_HEALTH_MIN_COOLDOWN_MS", "250")
	t.Setenv("NAGI_HEALTH_MAX_COOLDOWN_MS", "450")
	t.Setenv("NAGI_HEALTH_BACKOFF_SCALE", "0.01")

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
	ev := newRT2EventWatcher()

	provA := pi.ProductProviderAccount{
		ProviderID: "provA", Name: "Prov A", WireAPI: pi.OpenAICompletions, BaseURL: "https://api.openai.com/v1",
		Models: []string{"gpt-4o", "gpt-4o-mini"}, DefaultModel: "gpt-4o", EnabledModels: rt2SlicePtr("gpt-4o", "gpt-4o-mini"),
		ContextTokens: 128000, OutputTokens: 8192, Enabled: true,
	}
	provB := pi.ProductProviderAccount{
		ProviderID: "provB", Name: "Prov B Fallback", WireAPI: pi.OpenAICompletions, BaseURL: "https://openrouter.ai/api/v1",
		Models: []string{"openai/gpt-4o"}, DefaultModel: "openai/gpt-4o", EnabledModels: rt2SlicePtr("openai/gpt-4o"),
		FallbackOnly: rt2BoolPtr(true), ContextTokens: 128000, OutputTokens: 8192, Enabled: true,
	}
	provC := pi.ProductProviderAccount{
		ProviderID: "provC", Name: "Prov C", WireAPI: pi.OpenAICompletions, BaseURL: "https://api.openai.com/v1",
		Models: []string{"gpt-4o-mini"}, DefaultModel: "gpt-4o-mini", EnabledModels: rt2SlicePtr("gpt-4o-mini"),
		ContextTokens: 128000, OutputTokens: 8192, Enabled: true,
	}
	provRespOverrides := []pi.ProductModelOverride{{ID: "resp-m", IsChat: rt2BoolPtr(true), ToolCall: rt2BoolPtr(true)}}
	provResp := pi.ProductProviderAccount{
		ProviderID: "provResp", Name: "Prov Resp", WireAPI: pi.OpenAIResponses, BaseURL: mock.server.URL + "/resp/v1",
		Models: []string{"resp-m"}, DefaultModel: "resp-m", EnabledModels: rt2SlicePtr("resp-m"), ModelOverrides: &provRespOverrides,
		ContextTokens: 64000, OutputTokens: 4096, Enabled: true,
	}
	closedOverrides := []pi.ProductModelOverride{{ID: "m1", IsChat: rt2BoolPtr(true), ToolCall: rt2BoolPtr(true)}, {ID: "m2", IsChat: rt2BoolPtr(true), ToolCall: rt2BoolPtr(true)}}
	provClosed := pi.ProductProviderAccount{
		ProviderID: "provClosed", Name: "Prov Closed", WireAPI: pi.OpenAICompletions, BaseURL: "http://" + closedAddr + "/v1",
		Models: []string{"m1", "m2"}, DefaultModel: "m1", EnabledModels: rt2SlicePtr("m1", "m2"), ModelOverrides: &closedOverrides,
		ContextTokens: 32768, OutputTokens: 4096, Enabled: true,
	}

	var rev pi.ProductConnectionRevisions
	for _, item := range []struct {
		acc pi.ProductProviderAccount
		key string
		op  string
	}{
		{provA, keyA, "op-ha-a"},
		{provB, keyB, "op-ha-b"},
		{provC, keyC, "op-ha-c"},
		{provResp, keyResp, "op-ha-resp"},
		{provClosed, keyClosed, "op-ha-closed"},
	} {
		rev, err = proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
			ProductProviderTestParams: pi.ProductProviderTestParams{Account: item.acc, APIKey: item.key},
			OperationID:               item.op,
		})
		if err != nil {
			t.Fatal(err)
		}
		ev.waitProviderChange(t, ctx, proc, "saved")
	}
	rev, err = proc.client.SetRoutingPolicy(ctx, pi.ProductRoutingPolicyParams{Policy: pi.ProductRoutingQuality, OperationID: "op-ha-pol"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "policy")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-ha-apply"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")

	newBot := func(name string) pi.ProductBot {
		b, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
			OperationID: "op-bot-" + name,
			Profile:     pi.ProductBotProfile{Name: name, Workspace: ws, CodingTools: []string{}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	savedBeforeHealth, effBeforeHealth := *rev.SavedRevision, *rev.EffectiveRevision

	setMock(func(req rt2Req, _ *http.Request) rt2Action {
		if req.Key == keyA && req.Model == "gpt-4o" {
			return rt2Action{Status: http.StatusTooManyRequests, RetryAfter: "1"}
		}
		if req.Key == keyB && req.Model == "openai/gpt-4o" {
			if req.LatestUser == "COOL_B_DATE" {
				return rt2Action{Status: http.StatusTooManyRequests, RetryAfter: time.Now().UTC().Add(5 * time.Second).Format(http.TimeFormat)}
			}
		}
		return rt2Action{Text: "OK:" + req.Key + ":" + req.Model}
	})

	b1 := newBot("H1")
	recA429, admA429, _ := rt2SendAndDecode(t, ctx, proc, b1.CurrentConversationID, "HIT_A_429", "op-h-a429")
	if recA429.State != "accepted" || admA429.Route == nil || admA429.Route.ProviderID != "provA" || admA429.Route.ResolvedModel != "gpt-4o" {
		t.Fatalf("expected initial Auto quality route on provA/gpt-4o: %+v %+v", recA429, admA429)
	}
	if s := ev.waitSettled(t, ctx, proc, recA429.Execution.ExecutionID); s.State != "failed" {
		t.Fatalf("HIT_A_429 settled = %+v", s)
	}

	listAfter429, err := proc.client.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if *listAfter429.SavedRevision != savedBeforeHealth || *listAfter429.EffectiveRevision != effBeforeHealth {
		t.Fatalf("health transition bumped revisions: before=(%d,%d) after=(%d,%d)", savedBeforeHealth, effBeforeHealth, *listAfter429.SavedRevision, *listAfter429.EffectiveRevision)
	}
	if listAfter429.ResolvedRoles.Dialogue == nil || listAfter429.ResolvedRoles.Dialogue.ProviderID != "provB" || listAfter429.ResolvedRoles.Dialogue.ResolvedModel != "openai/gpt-4o" || listAfter429.ResolvedRoles.Dialogue.Failover != "same_model_cooldown" || listAfter429.RoleStatus["dialogue"].Failover != "same_model_cooldown" {
		t.Fatalf("same-model-first failover did not pick fallbackOnly provB/openai/gpt-4o before provC/gpt-4o-mini: route=%+v status=%+v", listAfter429.ResolvedRoles.Dialogue, listAfter429.RoleStatus["dialogue"])
	}
	if listAfter429.ResolvedRoles.Utility == nil || listAfter429.ResolvedRoles.Utility.ProviderID != "provA" || listAfter429.ResolvedRoles.Utility.ResolvedModel != "gpt-4o-mini" {
		t.Fatalf("429 on provA/gpt-4o poisoned sibling provA/gpt-4o-mini: %+v", listAfter429.ResolvedRoles.Utility)
	}
	for _, acc := range listAfter429.Accounts {
		if acc.ProviderID == "provA" {
			if acc.HealthSummary != "degraded" || len(acc.RouteHealth) != 1 || acc.RouteHealth[0].WireModelID != "gpt-4o" || acc.RouteHealth[0].State != "cooldown" || acc.RouteHealth[0].Reason != "rate_limited" || acc.RouteHealth[0].RetryAt == "" {
				t.Fatalf("provA routeHealth after model-specific 429 mismatch: %+v", acc)
			}
		}
	}

	b2 := newBot("H2")
	recBDate, admBDate, _ := rt2SendAndDecode(t, ctx, proc, b2.CurrentConversationID, "COOL_B_DATE", "op-h-bdate")
	if recBDate.State != "accepted" || admBDate.Route == nil || admBDate.Route.ProviderID != "provB" || admBDate.Route.Failover != "same_model_cooldown" {
		t.Fatalf("COOL_B_DATE did not dispatch to provB with same_model_cooldown: %+v %+v", recBDate, admBDate)
	}
	if s := ev.waitSettled(t, ctx, proc, recBDate.Execution.ExecutionID); s.State != "failed" {
		t.Fatalf("COOL_B_DATE settled = %+v", s)
	}
	rtNextModel, _ := proc.client.Routing(ctx)
	if rtNextModel.ResolvedRoles.Dialogue == nil || rtNextModel.ResolvedRoles.Dialogue.CanonicalModelID != "openai/gpt-4o-mini" || rtNextModel.ResolvedRoles.Dialogue.Failover != "model_cooldown" || rtNextModel.RoleStatus["dialogue"].Failover != "model_cooldown" {
		t.Fatalf("all gpt-4o cooled did not advance to gpt-4o-mini with model_cooldown: %+v %+v", rtNextModel.ResolvedRoles.Dialogue, rtNextModel.RoleStatus["dialogue"])
	}

	rev, err = proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"dialogue": {Mode: "route", ProviderID: "provA", Model: "gpt-4o"}},
		OperationID:   "op-h-pin-a-cool",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-h-apply-pin-a-cool"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")

	bPostsBeforePin := mock.countKeyPosts(keyB)
	cPostsBeforePin := mock.countKeyPosts(keyC)
	b3 := newBot("H3")
	recPinCool, admPinCool, _ := rt2SendAndDecode(t, ctx, proc, b3.CurrentConversationID, "EXACT_PIN_COOLED", "op-h-pincool")
	if recPinCool.State != "rejected" || admPinCool.Code != pi.ProductRouteUnavailable || admPinCool.Reason != "cooldown" || admPinCool.RetryAt == "" {
		t.Fatalf("exact pin during cooldown mismatch: %+v %+v", recPinCool, admPinCool)
	}
	if mock.countKeyPosts(keyB) != bPostsBeforePin || mock.countKeyPosts(keyC) != cPostsBeforePin {
		t.Fatal("exact pin during cooldown leaked requests to provB or provC")
	}

	models404 := false
	setMock(func(req rt2Req, _ *http.Request) rt2Action {
		if req.Method == http.MethodGet {
			return rt2Action{Models404: models404, Models: []string{"gpt-4o", "gpt-4o-mini"}}
		}
		return rt2Action{Text: "OK"}
	})
	testModels, err := proc.client.TestProvider(ctx, pi.ProductProviderTestParams{Account: provA})
	if err != nil || !testModels.OK {
		t.Fatalf("/models test failed: %+v, %v", testModels, err)
	}
	rtStillCool, _ := proc.client.Routing(ctx)
	if rtStillCool.RoleStatus["dialogue"].Status != "cooldown" {
		t.Fatalf("/models GET cleared 429 cooldown: %+v", rtStillCool.RoleStatus["dialogue"])
	}
	models404 = true
	aPostsBeforeBlockedProbe := mock.countKeyPosts(keyA)
	testBlocked, err := proc.client.TestProvider(ctx, pi.ProductProviderTestParams{Account: provA})
	if err != nil || testBlocked.OK {
		t.Fatalf("fallback probe during active cooldown must fail without bypassing Retry-After: %+v, %v", testBlocked, err)
	}
	if mock.countKeyPosts(keyA) != aPostsBeforeBlockedProbe {
		t.Fatal("fallback probe bypassed Retry-After!")
	}
	testOtherKey, err := proc.client.TestProvider(ctx, pi.ProductProviderTestParams{Account: provA, APIKey: keyOther})
	if err != nil || !testOtherKey.OK {
		t.Fatalf("different-key candidate probe failed: %+v, %v", testOtherKey, err)
	}
	rtAfterOtherKey, _ := proc.client.Routing(ctx)
	if rtAfterOtherKey.RoleStatus["dialogue"].Status != "cooldown" {
		t.Fatalf("different-key candidate probe cleared effective cooldown: %+v", rtAfterOtherKey.RoleStatus["dialogue"])
	}

	time.Sleep(520 * time.Millisecond)
	listHalfOpen, _ := proc.client.ListProviders(ctx)
	sawHalfOpen := false
	for _, acc := range listHalfOpen.Accounts {
		if acc.ProviderID == "provA" {
			for _, rh := range acc.RouteHealth {
				if rh.WireModelID == "gpt-4o" && rh.State == "half_open" {
					sawHalfOpen = true
				}
			}
		}
	}
	if !sawHalfOpen {
		t.Fatalf("provA/gpt-4o did not transition to half_open after expiry: %+v", listHalfOpen.Accounts)
	}

	hoEntered := make(chan struct{}, 2)
	hoRelease := make(chan struct{})
	mock.maxFly.Store(0)
	setMock(func(req rt2Req, _ *http.Request) rt2Action {
		return rt2Action{Text: "HALF_OPEN_OK", HoldEntered: hoEntered, HoldRelease: hoRelease}
	})
	bHo1 := newBot("HO1")
	bHo2 := newBot("HO2")
	dHo1, _ := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bHo1.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "HO_1", Materials: []pi.ProductMaterial{}},
		ExpectedRevision:          0, OperationID: "op-ho1-d",
	})
	dHo2, _ := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bHo2.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "HO_2", Materials: []pi.ProductMaterial{}},
		ExpectedRevision:          0, OperationID: "op-ho2-d",
	})
	type sendRes struct {
		rec pi.ProductReceipt
		err error
	}
	ch1, ch2 := make(chan sendRes, 1), make(chan sendRes, 1)
	go func() {
		r, e := proc.client.Send(ctx, pi.ProductSendParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: bHo1.CurrentConversationID},
			DraftRevision:             dHo1.Revision, OperationID: "op-ho1-s",
		})
		ch1 <- sendRes{r, e}
	}()
	go func() {
		r, e := proc.client.Send(ctx, pi.ProductSendParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: bHo2.CurrentConversationID},
			DraftRevision:             dHo2.Revision, OperationID: "op-ho2-s",
		})
		ch2 <- sendRes{r, e}
	}()
	rHo1, rHo2 := <-ch1, <-ch2
	if rHo1.err != nil || rHo2.err != nil {
		t.Fatalf("simultaneous half-open sends errored: %v / %v", rHo1.err, rHo2.err)
	}
	states := []string{rHo1.rec.State, rHo2.rec.State}
	slices.Sort(states)
	if !slices.Equal(states, []string{"accepted", "rejected"}) {
		t.Fatalf("simultaneous half-open admissions = %v, want [accepted rejected]", states)
	}
	acceptedHo := rHo1.rec
	if rHo2.rec.State == "accepted" {
		acceptedHo = rHo2.rec
	}
	select {
	case <-hoEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("half-open winner never reached provider")
	}
	if mock.maxFly.Load() != 1 {
		t.Fatalf("half-open maxInFlight = %d, want 1", mock.maxFly.Load())
	}
	close(hoRelease)
	if s := ev.waitSettled(t, ctx, proc, acceptedHo.Execution.ExecutionID); s.State != "completed" {
		t.Fatalf("half-open winner settled = %+v", s)
	}
	listHealed, _ := proc.client.ListProviders(ctx)
	for _, acc := range listHealed.Accounts {
		if acc.ProviderID == "provA" && (acc.HealthSummary != "healthy" || len(acc.RouteHealth) != 0) {
			t.Fatalf("provA did not return to healthy after half-open recovery: %+v", acc)
		}
	}

	lateEntered := make(chan struct{}, 1)
	lateRelease := make(chan struct{})
	setMock(func(req rt2Req, _ *http.Request) rt2Action {
		if req.LatestUser == "LATE_OLD_SUCCESS" {
			return rt2Action{Text: "LATE_OK", HoldEntered: lateEntered, HoldRelease: lateRelease}
		}
		if req.LatestUser == "NEWER_429" {
			return rt2Action{Status: http.StatusTooManyRequests, RetryAfter: "1"}
		}
		return rt2Action{Text: "OK"}
	})
	bLate1 := newBot("Late1")
	bLate2 := newBot("Late2")
	recLate1, _, _ := rt2SendAndDecode(t, ctx, proc, bLate1.CurrentConversationID, "LATE_OLD_SUCCESS", "op-late1")
	select {
	case <-lateEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("LATE_OLD_SUCCESS did not reach provider")
	}
	recLate2, _, _ := rt2SendAndDecode(t, ctx, proc, bLate2.CurrentConversationID, "NEWER_429", "op-late2")
	if s := ev.waitSettled(t, ctx, proc, recLate2.Execution.ExecutionID); s.State != "failed" {
		t.Fatalf("NEWER_429 settled = %+v", s)
	}
	close(lateRelease)
	if s := ev.waitSettled(t, ctx, proc, recLate1.Execution.ExecutionID); s.State != "completed" {
		t.Fatalf("LATE_OLD_SUCCESS settled = %+v", s)
	}
	rtAfterLate, _ := proc.client.Routing(ctx)
	if rtAfterLate.RoleStatus["dialogue"].Status != "cooldown" {
		t.Fatalf("late success from older generation cleared newer 429 cooldown: %+v", rtAfterLate.RoleStatus["dialogue"])
	}

	time.Sleep(520 * time.Millisecond)
	setMock(func(req rt2Req, _ *http.Request) rt2Action {
		if req.Method == http.MethodGet {
			return rt2Action{Models404: true}
		}
		return rt2Action{Text: "OK"}
	})
	testHeal, err := proc.client.TestProvider(ctx, pi.ProductProviderTestParams{Account: provA})
	if err != nil || !testHeal.OK {
		t.Fatalf("matching post-expiry fallback probe failed: %+v, %v", testHeal, err)
	}
	listProbeHealed, _ := proc.client.ListProviders(ctx)
	for _, acc := range listProbeHealed.Accounts {
		if acc.ProviderID == "provA" && acc.HealthSummary != "healthy" {
			t.Fatalf("matching fallback probe did not heal provA/gpt-4o: %+v", acc)
		}
	}

	setMock(func(req rt2Req, _ *http.Request) rt2Action {
		switch req.LatestUser {
		case "ERR_400":
			return rt2Action{Status: http.StatusBadRequest}
		case "ERR_422":
			return rt2Action{Status: http.StatusUnprocessableEntity}
		case "ERR_503_1", "ERR_503_2":
			return rt2Action{Status: http.StatusServiceUnavailable}
		case "DROP_COMP_1", "DROP_COMP_2", "DROP_RESP_1", "DROP_RESP_2":
			return rt2Action{DropStream: true}
		}
		return rt2Action{Text: "OK"}
	})
	bClean := newBot("Clean")
	for _, msg := range []string{"ERR_400", "ERR_422"} {
		rec, _, _ := rt2SendAndDecode(t, ctx, proc, bClean.CurrentConversationID, msg, "op-"+msg)
		if s := ev.waitSettled(t, ctx, proc, rec.Execution.ExecutionID); s.State != "failed" {
			t.Fatalf("%s settled = %+v", msg, s)
		}
	}
	abortEntered := make(chan struct{}, 1)
	abortRelease := make(chan struct{})
	setMock(func(req rt2Req, _ *http.Request) rt2Action {
		if req.LatestUser == "USER_ABORT" {
			return rt2Action{Text: "NEVER", HoldEntered: abortEntered, HoldRelease: abortRelease}
		}
		return rt2Action{Text: "OK"}
	})
	recAbort, _, _ := rt2SendAndDecode(t, ctx, proc, bClean.CurrentConversationID, "USER_ABORT", "op-abort")
	select {
	case <-abortEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("USER_ABORT did not reach provider")
	}
	if _, err := proc.client.StopConversation(ctx, pi.ProductConversationStopParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bClean.CurrentConversationID},
		ExecutionID:               recAbort.Execution.ExecutionID,
		OperationID:               "op-stop-abort",
	}); err != nil {
		t.Fatal(err)
	}
	if s := ev.waitSettled(t, ctx, proc, recAbort.Execution.ExecutionID); s.State != "interrupted" {
		t.Fatalf("USER_ABORT settled = %+v, want interrupted", s)
	}
	close(abortRelease)
	listClean, _ := proc.client.ListProviders(ctx)
	for _, acc := range listClean.Accounts {
		if acc.ProviderID == "provA" && (acc.HealthSummary != "healthy" || len(acc.RouteHealth) != 0) {
			t.Fatalf("400/422/user-abort poisoned health: %+v", acc)
		}
	}

	setMock(func(req rt2Req, _ *http.Request) rt2Action {
		if strings.HasPrefix(req.LatestUser, "ERR_503_") {
			return rt2Action{Status: http.StatusServiceUnavailable}
		}
		if strings.HasPrefix(req.LatestUser, "DROP_") {
			return rt2Action{DropStream: true}
		}
		return rt2Action{Text: "OK"}
	})
	rec503_1, _, _ := rt2SendAndDecode(t, ctx, proc, bClean.CurrentConversationID, "ERR_503_1", "op-503-1")
	if s := ev.waitSettled(t, ctx, proc, rec503_1.Execution.ExecutionID); s.State != "failed" {
		t.Fatalf("ERR_503_1 settled = %+v", s)
	}
	rt503_1, _ := proc.client.Routing(ctx)
	if rt503_1.RoleStatus["dialogue"].Status != "ready" {
		t.Fatalf("1st 503 cooled route prematurely: %+v", rt503_1.RoleStatus["dialogue"])
	}
	rec503_2, _, _ := rt2SendAndDecode(t, ctx, proc, bClean.CurrentConversationID, "ERR_503_2", "op-503-2")
	if s := ev.waitSettled(t, ctx, proc, rec503_2.Execution.ExecutionID); s.State != "failed" {
		t.Fatalf("ERR_503_2 settled = %+v", s)
	}
	rt503_2, _ := proc.client.Routing(ctx)
	if rt503_2.RoleStatus["dialogue"].Status != "cooldown" {
		t.Fatalf("2nd consecutive 503 did not cool route: %+v", rt503_2.RoleStatus["dialogue"])
	}

	rev, err = proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"dialogue": {Mode: "route", ProviderID: "provResp", Model: "resp-m"}},
		OperationID:   "op-pin-resp",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-pin-resp"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")
	bResp := newBot("RespDrop")
	recDr1, _, _ := rt2SendAndDecode(t, ctx, proc, bResp.CurrentConversationID, "DROP_RESP_1", "op-dr1")
	if s := ev.waitSettled(t, ctx, proc, recDr1.Execution.ExecutionID); s.State != "failed" {
		t.Fatalf("DROP_RESP_1 settled = %+v, want failed (not interrupted)", s)
	}
	recDr2, _, _ := rt2SendAndDecode(t, ctx, proc, bResp.CurrentConversationID, "DROP_RESP_2", "op-dr2")
	if s := ev.waitSettled(t, ctx, proc, recDr2.Execution.ExecutionID); s.State != "failed" {
		t.Fatalf("DROP_RESP_2 settled = %+v, want failed", s)
	}
	listRespDrop, _ := proc.client.ListProviders(ctx)
	if listRespDrop.RoleStatus["dialogue"].Status != "cooldown" {
		t.Fatalf("interrupted HTTP 200 responses stream 2-strike did not cool route: %+v", listRespDrop.RoleStatus["dialogue"])
	}
	for _, acc := range listRespDrop.Accounts {
		if acc.ProviderID == "provResp" {
			if len(acc.RouteHealth) != 1 || acc.RouteHealth[0].Reason != "stream_error" || acc.RouteHealth[0].State != "cooldown" {
				t.Fatalf("provResp interrupted stream routeHealth mismatch: %+v", acc.RouteHealth)
			}
		}
	}

	rev, err = proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"dialogue": {Mode: "route", ProviderID: "provClosed", Model: "m1"}, "utility": {Mode: "route", ProviderID: "provClosed", Model: "m2"}},
		OperationID:   "op-pin-closed",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-pin-closed"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")
	bClosed := newBot("ClosedNet")
	recNet, _, _ := rt2SendAndDecode(t, ctx, proc, bClosed.CurrentConversationID, "NET_REFUSED", "op-net")
	if s := ev.waitSettled(t, ctx, proc, recNet.Execution.ExecutionID); s.State != "failed" {
		t.Fatalf("NET_REFUSED settled = %+v", s)
	}
	listNet, _ := proc.client.ListProviders(ctx)
	if listNet.RoleStatus["dialogue"].Status != "cooldown" || listNet.RoleStatus["utility"].Status != "cooldown" {
		t.Fatalf("ECONNREFUSED did not cool endpoint scope across sibling m2: dlg=%+v util=%+v", listNet.RoleStatus["dialogue"], listNet.RoleStatus["utility"])
	}
	for _, acc := range listNet.Accounts {
		if acc.ProviderID == "provClosed" {
			if acc.HealthSummary != "cooldown" || len(acc.RouteHealth) != 1 || acc.RouteHealth[0].Scope != "endpoint" || acc.RouteHealth[0].Reason != "network" {
				t.Fatalf("provClosed endpoint routeHealth mismatch: %+v", acc)
			}
		}
	}

	smallOv := []pi.ProductModelOverride{{ID: "ctx-m", IsChat: rt2BoolPtr(true), ToolCall: rt2BoolPtr(true)}}
	provSmall := pi.ProductProviderAccount{
		ProviderID: "provSmall", Name: "Small Ctx", WireAPI: pi.OpenAICompletions, BaseURL: mock.server.URL + "/small/v1",
		Models: []string{"ctx-m"}, DefaultModel: "ctx-m", EnabledModels: rt2SlicePtr("ctx-m"), ModelOverrides: &smallOv,
		ContextTokens: 8192, OutputTokens: 2048, Enabled: true,
	}
	provLarge := pi.ProductProviderAccount{
		ProviderID: "provLarge", Name: "Large Ctx", WireAPI: pi.OpenAICompletions, BaseURL: mock.server.URL + "/large/v1",
		Models: []string{"ctx-m"}, DefaultModel: "ctx-m", EnabledModels: rt2SlicePtr("ctx-m"), ModelOverrides: &smallOv,
		ContextTokens: 32768, OutputTokens: 4096, Enabled: true,
	}
	for _, id := range []string{"provA", "provB", "provC", "provResp", "provClosed"} {
		rev, err = proc.client.DeleteProvider(ctx, pi.ProductProviderTargetParams{ProviderID: id, OperationID: "op-del-ctx-" + id})
		if err != nil {
			t.Fatal(err)
		}
		ev.waitProviderChange(t, ctx, proc, "deleted")
	}
	rev, err = proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: provSmall, APIKey: keySmall},
		OperationID:               "op-save-small",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: provLarge, APIKey: keyLarge},
		OperationID:               "op-save-large",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"dialogue": nil, "coding": nil, "reasoning": nil, "utility": nil},
		OperationID:   "op-clear-ctx-ov",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "saved")
	rev, err = proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *rev.SavedRevision, OperationID: "op-apply-ctx"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitProviderChange(t, ctx, proc, "applied")

	setMock(func(req rt2Req, _ *http.Request) rt2Action {
		return rt2Action{Text: "CTX_OK:" + req.Key}
	})
	bCtx1 := newBot("CtxSmall")
	recSmall, admSmall, _ := rt2SendAndDecode(t, ctx, proc, bCtx1.CurrentConversationID, "SHORT_PROMPT", "op-ctx-short")
	if recSmall.State != "accepted" || admSmall.Route == nil || admSmall.Route.ProviderID != "provSmall" {
		t.Fatalf("short prompt did not pick priority-1 provSmall: %+v %+v", recSmall, admSmall)
	}
	if s := ev.waitSettled(t, ctx, proc, recSmall.Execution.ExecutionID); s.State != "completed" {
		t.Fatalf("SHORT_PROMPT settled = %+v", s)
	}

	smallPostsBefore := mock.countKeyPosts(keySmall)
	bCtx2 := newBot("CtxLarge")
	bigMat := pi.ProductMaterial{ID: "m-big", Revision: 1, Name: "big.txt", Text: strings.Repeat("a", 32*1024)}
	recLarge, admLarge, _ := rt2SendAndDecode(t, ctx, proc, bCtx2.CurrentConversationID, "BIG_MAT_PROMPT", "op-ctx-big", bigMat)
	if recLarge.State != "accepted" || admLarge.Route == nil || admLarge.Route.ProviderID != "provLarge" {
		t.Fatalf("large Material did not bypass provSmall to provLarge: %+v %+v", recLarge, admLarge)
	}
	if s := ev.waitSettled(t, ctx, proc, recLarge.Execution.ExecutionID); s.State != "completed" {
		t.Fatalf("BIG_MAT_PROMPT settled = %+v", s)
	}
	if mock.countKeyPosts(keySmall) != smallPostsBefore {
		t.Fatal("oversized Material leaked a request to provSmall before minContextTokens gate")
	}

	postsBeforeHuge := mock.countPosts()
	bCtx3 := newBot("CtxHuge")
	hugeMat := pi.ProductMaterial{ID: "m-huge", Revision: 1, Name: "huge.txt", Text: strings.Repeat("b", 160*1024)}
	recHuge, admHuge, _ := rt2SendAndDecode(t, ctx, proc, bCtx3.CurrentConversationID, "HUGE_MAT_PROMPT", "op-ctx-huge", hugeMat)
	if recHuge.State != "rejected" || admHuge.Code != pi.ProductRouteUnavailable || admHuge.Reason != "incompatible" || admHuge.Delivery != "non_dispatched" {
		t.Fatalf("impossible Material budget did not reject incompatible: %+v %+v", recHuge, admHuge)
	}
	if mock.countPosts() != postsBeforeHuge {
		t.Fatal("impossible Material budget dispatched a provider request")
	}

	crashEntered := make(chan struct{}, 1)
	crashHold := make(chan struct{})
	defer close(crashHold)
	setMock(func(req rt2Req, _ *http.Request) rt2Action {
		if req.LatestUser == "CRASH_UNKNOWN" {
			return rt2Action{Text: "NEVER", HoldEntered: crashEntered, HoldRelease: crashHold}
		}
		return rt2Action{Text: "OK"}
	})
	bCrash := newBot("CrashBot")
	recCrash, _, _ := rt2SendAndDecode(t, ctx, proc, bCrash.CurrentConversationID, "CRASH_UNKNOWN", "op-crash")
	if recCrash.State != "accepted" || recCrash.Execution == nil {
		t.Fatalf("CRASH_UNKNOWN admission = %+v", recCrash)
	}
	select {
	case <-crashEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("CRASH_UNKNOWN did not reach provider")
	}
	postsAtCrash := mock.countPosts()
	killOut, _ := proc.client.Kill(ctx)
	proc.closed = true
	if !killOut.Reaped {
		t.Fatalf("could not reap killed worker: %+v", killOut)
	}

	opts.FreshProfile = false
	proc2 := startProductProcess(t, opts)
	_ = proc2.open(t, ctx)
	convCrash, err := proc2.client.GetConversation(ctx, bCrash.CurrentConversationID)
	if err != nil || convCrash.LatestExecution == nil || convCrash.LatestExecution.State != "unknown" {
		t.Fatalf("reopened crashed conversation execution = %+v, %v", convCrash.LatestExecution, err)
	}
	if mock.countPosts() != postsAtCrash {
		t.Fatalf("reopen after crash replayed %d requests", mock.countPosts()-postsAtCrash)
	}
	proc2.shutdown(t, ctx)
}
