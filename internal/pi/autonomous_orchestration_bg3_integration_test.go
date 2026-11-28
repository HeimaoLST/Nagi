//go:build integration

package pi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

type bg3ProviderRequest struct {
	URL        string
	Model      string
	Event      *bg3WireEvent
	LatestRole string
	ToolCallID string
	LatestText string
	Advertised []string
	RawBody    string
}

type bg3WireEvent struct {
	Provenance     string `json:"provenance"`
	MandateID      string `json:"mandateId"`
	Purpose        string `json:"purpose"`
	WorkingSummary string `json:"workingSummary"`
	Facts          []struct {
		Kind           string `json:"kind"`
		SourceID       string `json:"sourceId"`
		OccurrenceID   string `json:"occurrenceId"`
		CoalescedCount int    `json:"coalescedCount"`
		ObservedAt     string `json:"observedAt"`
		Data           string `json:"data"`
	} `json:"facts"`
}

type bg3ToolPlan struct {
	Name string
	Args any
}

type bg3Barrier struct {
	arrived chan bg3ProviderRequest
	release chan struct{}
	once    sync.Once
}

type bg3ForegroundPlan struct {
	Call   *bg3ToolPlan
	Result string
}

type bg3ProviderHarness struct {
	t         *testing.T
	server    *httptest.Server
	secret    string
	calls     atomic.Int64
	mu        sync.Mutex
	requests  []bg3ProviderRequest
	problems  []string
	forbid    []string
	mandates  map[string]func(bg3WireEvent) *bg3ToolPlan
	fgPlans   map[string]*bg3ForegroundPlan
	fgCalls   map[string]*bg3ForegroundPlan
	barriers  map[string]*bg3Barrier
	failMarks map[string]int
}

func newBG3ProviderHarness(t *testing.T, secret string) *bg3ProviderHarness {
	t.Helper()
	h := &bg3ProviderHarness{
		t:         t,
		secret:    secret,
		mandates:  map[string]func(bg3WireEvent) *bg3ToolPlan{},
		fgPlans:   map[string]*bg3ForegroundPlan{},
		fgCalls:   map[string]*bg3ForegroundPlan{},
		barriers:  map[string]*bg3Barrier{},
		failMarks: map[string]int{},
	}
	h.server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(func() {
		h.releaseAllBarriers()
		h.server.Close()
	})
	return h
}

func (h *bg3ProviderHarness) releaseAllBarriers() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, b := range h.barriers {
		b.once.Do(func() { close(b.release) })
	}
	h.barriers = map[string]*bg3Barrier{}
}

func (h *bg3ProviderHarness) setMandatePlan(mandateID string, fn func(bg3WireEvent) *bg3ToolPlan) {
	h.mu.Lock()
	h.mandates[mandateID] = fn
	h.mu.Unlock()
}

func (h *bg3ProviderHarness) setForegroundPlan(prefix string, plan *bg3ForegroundPlan) {
	h.mu.Lock()
	h.fgPlans[prefix] = plan
	h.mu.Unlock()
}

func (h *bg3ProviderHarness) clearForegroundPlan(prefix string) {
	h.mu.Lock()
	delete(h.fgPlans, prefix)
	h.mu.Unlock()
}

func (h *bg3ProviderHarness) setFailure(prefix string, status int) {
	h.mu.Lock()
	h.failMarks[prefix] = status
	h.mu.Unlock()
}

func (h *bg3ProviderHarness) armBarrier(key string) *bg3Barrier {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := &bg3Barrier{arrived: make(chan bg3ProviderRequest, 1), release: make(chan struct{})}
	h.barriers[key] = b
	return b
}

func (b *bg3Barrier) await(t *testing.T, ctx context.Context, label string) bg3ProviderRequest {
	t.Helper()
	select {
	case req := <-b.arrived:
		return req
	case <-ctx.Done():
		t.Fatalf("timed out waiting for provider barrier %s: %v", label, ctx.Err())
		return bg3ProviderRequest{}
	}
}

func (b *bg3Barrier) open() {
	b.once.Do(func() { close(b.release) })
}

func (h *bg3ProviderHarness) recordProblem(msg string) {
	h.mu.Lock()
	h.problems = append(h.problems, msg)
	h.mu.Unlock()
}

func (h *bg3ProviderHarness) assertNoProblems() {
	h.t.Helper()
	h.mu.Lock()
	probs := append([]string(nil), h.problems...)
	h.mu.Unlock()
	if len(probs) > 0 {
		h.t.Fatalf("bg3 provider problems: %v", probs)
	}
}

func (h *bg3ProviderHarness) requestList() []bg3ProviderRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]bg3ProviderRequest(nil), h.requests...)
}

func bg3MessageText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

func (h *bg3ProviderHarness) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+h.secret {
		h.recordProblem("unexpected auth header on " + r.URL.Path)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/models") {
		model := "gpt-4.1"
		if strings.Contains(r.URL.Path, "/utility-pin/") {
			model = "gpt-4o-mini"
		} else if strings.Contains(r.URL.Path, "/utility/") {
			model = "gpt-4.1-mini"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": model}},
		})
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		h.recordProblem("unexpected path " + r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var body struct {
		Model    string `json:"model"`
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.recordProblem("decode provider request: " + err.Error())
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	rawBytes, _ := json.Marshal(body)
	h.mu.Lock()
	forbid := append([]string(nil), h.forbid...)
	h.mu.Unlock()
	for _, secret := range forbid {
		if secret != "" && bytes.Contains(rawBytes, []byte(secret)) {
			h.recordProblem("forbidden secret leaked into provider body on " + r.URL.Path)
		}
	}
	var event *bg3WireEvent
	for i := len(body.Messages) - 1; i >= 0; i-- {
		if body.Messages[i].Role != "user" {
			continue
		}
		msgText := bg3MessageText(body.Messages[i].Content)
		if strings.HasPrefix(msgText, "<orchestration_event>\n") {
			lines := strings.Split(msgText, "\n")
			if len(lines) >= 2 {
				var parsed bg3WireEvent
				if err := json.Unmarshal([]byte(lines[1]), &parsed); err == nil {
					event = &parsed
				}
			}
			break
		}
	}
	latest := body.Messages[len(body.Messages)-1]
	latestText := bg3MessageText(latest.Content)
	advertised := make([]string, 0, len(body.Tools))
	for _, tool := range body.Tools {
		advertised = append(advertised, tool.Function.Name)
	}
	slices.Sort(advertised)
	if event != nil {
		wantTools := []string{"conversation_notify", "orchestration_finish", "orchestration_request_decision"}
		if !slices.Equal(advertised, wantTools) {
			h.recordProblem(fmt.Sprintf("private tools=%v want=%v", advertised, wantTools))
		}
		if event.Provenance != "internal" {
			h.recordProblem(fmt.Sprintf("private provenance=%q want=internal", event.Provenance))
		}
		if !strings.HasPrefix(r.URL.Path, "/utility") {
			h.recordProblem(fmt.Sprintf("private path=%q want=/utility*", r.URL.Path))
		}
	} else {
		for _, name := range advertised {
			if name == "conversation_notify" || strings.HasPrefix(name, "orchestration_") {
				h.recordProblem("foreground request advertised private tool " + name)
			}
		}
	}
	rec := bg3ProviderRequest{
		URL:        r.URL.Path,
		Model:      body.Model,
		Event:      event,
		LatestRole: latest.Role,
		ToolCallID: latest.ToolCallID,
		LatestText: latestText,
		Advertised: advertised,
		RawBody:    string(rawBytes),
	}
	callNum := h.calls.Add(1)
	var gate *bg3Barrier
	h.mu.Lock()
	h.requests = append(h.requests, rec)
	if event != nil {
		if b := h.barriers[event.MandateID]; b != nil {
			gate = b
			delete(h.barriers, event.MandateID)
		}
	} else {
		for mark, b := range h.barriers {
			if strings.HasPrefix(latestText, mark) {
				gate = b
				delete(h.barriers, mark)
				break
			}
		}
	}
	h.mu.Unlock()
	if gate != nil {
		gate.arrived <- rec
		select {
		case <-gate.release:
		case <-r.Context().Done():
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	var failStatus int
	var call *bg3ToolPlan
	var fgPlan *bg3ForegroundPlan
	h.mu.Lock()
	if event == nil && latest.Role == "user" {
		for prefix, code := range h.failMarks {
			if strings.HasPrefix(latestText, prefix) {
				failStatus = code
				delete(h.failMarks, prefix)
				break
			}
		}
	}
	if latest.ToolCallID != "" {
		if p := h.fgCalls[latest.ToolCallID]; p != nil {
			p.Result = latestText
			delete(h.fgCalls, latest.ToolCallID)
		}
	}
	if event != nil && latest.Role != "tool" {
		if fn := h.mandates[event.MandateID]; fn != nil {
			call = fn(*event)
		} else if len(event.Facts) > 0 {
			call = &bg3ToolPlan{
				Name: "orchestration_finish",
				Args: map[string]any{
					"summary":           "Observed a new state",
					"observedStateHash": event.Facts[len(event.Facts)-1].OccurrenceID,
					"progressMade":      true,
				},
			}
		}
	}
	if event == nil && latest.Role == "user" {
		for prefix, plan := range h.fgPlans {
			if strings.HasPrefix(latestText, prefix) {
				fgPlan = plan
				call = plan.Call
				break
			}
		}
	}
	h.mu.Unlock()
	if failStatus != 0 {
		w.WriteHeader(failStatus)
		_, _ = w.Write([]byte("injected provider error"))
		return
	}
	callID := fmt.Sprintf("bg3call_%06d", callNum)
	if fgPlan != nil && call != nil {
		h.mu.Lock()
		h.fgCalls[callID] = fgPlan
		h.mu.Unlock()
	}
	finishReason := "stop"
	delta := map[string]any{"role": "assistant", "content": "Public assistant reply"}
	if event != nil {
		delta["content"] = "Private assistant result"
	}
	if call != nil {
		finishReason = "tool_calls"
		argsJSON, _ := json.Marshal(call.Args)
		delta = map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"index": 0,
					"id":    callID,
					"type":  "function",
					"function": map[string]string{
						"name":      call.Name,
						"arguments": string(argsJSON),
					},
				},
			},
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, choice := range []map[string]any{
		{"index": 0, "delta": delta, "finish_reason": nil},
		{"index": 0, "delta": map[string]any{}, "finish_reason": finishReason},
	} {
		line, _ := json.Marshal(map[string]any{
			"id":      fmt.Sprintf("bg3-fixture-%d", callNum),
			"object":  "chat.completion.chunk",
			"created": 1,
			"model":   body.Model,
			"choices": []any{choice},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
	}
	usageLine, _ := json.Marshal(map[string]any{
		"id":      fmt.Sprintf("bg3-fixture-%d", callNum),
		"object":  "chat.completion.chunk",
		"created": 1,
		"model":   body.Model,
		"choices": []any{},
		"usage": map[string]int{
			"prompt_tokens":     19,
			"completion_tokens": 7,
			"total_tokens":      26,
		},
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", usageLine)
}

func bg3ConfigureProviders(t *testing.T, ctx context.Context, client *pi.ProductClient, baseURL, key, workspace string, maxActive int) uint64 {
	t.Helper()
	saved, err := client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "conn-save-" + key[:8],
		Configuration: pi.ProductConnectionInput{
			APIKey: key,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI:          pi.OpenAICompletions,
				BaseURL:          baseURL + "/dialogue/v1",
				Model:            "gpt-4.1",
				ContextTokens:    65536,
				OutputTokens:     4096,
				WorkingDirectory: workspace,
				MaxActiveTasks:   maxActive,
			},
		},
	})
	if err != nil || saved.SavedRevision == nil {
		t.Fatalf("SaveConnection = %+v err=%v", saved, err)
	}
	if _, err := client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{
		SavedRevision: *saved.SavedRevision, OperationID: "conn-apply-" + key[:8],
	}); err != nil {
		t.Fatal(err)
	}
	fallbackOnly := true
	roles := []struct {
		id       string
		path     string
		model    string
		fallback *bool
	}{
		{id: "dialogue-fixture", path: "/dialogue/v1", model: "gpt-4.1"},
		{id: "utility-fixture", path: "/utility/v1", model: "gpt-4.1-mini"},
		{id: "utility-pin-fixture", path: "/utility-pin/v1", model: "gpt-4o-mini", fallback: &fallbackOnly},
	}
	var rev pi.ProductConnectionRevisions
	for _, r := range roles {
		rev, err = client.SaveProvider(ctx, pi.ProductProviderSaveParams{
			OperationID: "prov-save-" + r.id,
			ProductProviderTestParams: pi.ProductProviderTestParams{
				APIKey: key,
				Account: pi.ProductProviderAccount{
					ProviderID:    r.id,
					Name:          r.id,
					WireAPI:       pi.OpenAICompletions,
					BaseURL:       baseURL + r.path,
					DefaultModel:  r.model,
					Models:        []string{r.model},
					ContextTokens: 65536,
					OutputTokens:  4096,
					Enabled:       true,
					FallbackOnly:  r.fallback,
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	rev, err = client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *rev.SavedRevision,
		Overrides: pi.ProductRoutingOverridesPatch{
			"dialogue": {Mode: "route", ProviderID: "dialogue-fixture", Model: "gpt-4.1"},
			"utility":  {Mode: "route", ProviderID: "utility-fixture", Model: "gpt-4.1-mini"},
		},
		OperationID: "prov-ov-init",
	})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		SavedRevision: *rev.SavedRevision, OperationID: "prov-apply-init",
	})
	if err != nil || applied.SavedRevision == nil {
		t.Fatalf("ApplyProviders = %+v err=%v", applied, err)
	}
	return *applied.SavedRevision
}

func bg3SaveWatch(t *testing.T, ctx context.Context, client *pi.ProductClient, params pi.ProductWatchSaveParams) pi.ProductWatchSummary {
	t.Helper()
	receipt, err := client.SaveWatch(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	res, err := pi.DecodeWatchMutationResult(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return res.Watch
}

func bg3AwaitMandateSettled(t *testing.T, ctx context.Context, client *pi.ProductClient, botID, mandateID string, wakes uint64) pi.ProductOrchestrationDetail {
	t.Helper()
	var detail pi.ProductOrchestrationDetail
	bg1WaitUntil(t, ctx, fmt.Sprintf("mandate %s wake>=%d", mandateID, wakes), func() bool {
		got, err := client.GetOrchestration(ctx, botID, mandateID)
		if err != nil {
			t.Fatalf("GetOrchestration(%s,%s): %v", botID, mandateID, err)
		}
		if got.Metrics.TotalWakes >= wakes && got.CurrentRunID == "" {
			detail = got
			return true
		}
		return false
	})
	return detail
}

func bg3HasDiskSession(sessionsDir string) bool {
	found := false
	_ = filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
			found = true
		}
		return nil
	})
	return found
}

func bg3SaveSend(t *testing.T, ctx context.Context, client *pi.ProductClient, convID, text string, quote *pi.ProductQuote, opPrefix string) (pi.ProductDraft, pi.ProductReceipt) {
	t.Helper()
	conv, err := client.GetConversation(ctx, convID)
	if err != nil || conv.Draft == nil {
		t.Fatalf("GetConversation(%s) = %+v err=%v", convID, conv, err)
	}
	draft, err := client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
		ProductText: pi.ProductText{
			Text: text,
			Materials: []pi.ProductMaterial{
				{ID: opPrefix + "-mat", Revision: 1, Name: "Notes", Text: "User material"},
			},
		},
		ExpectedRevision: conv.Draft.Revision,
		OperationID:      opPrefix + "-draft",
		Quote:            quote,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
		DraftRevision:             draft.Revision,
		OperationID:               opPrefix + "-send",
	})
	if err != nil {
		t.Fatal(err)
	}
	return draft, receipt
}

func bg3ForegroundTool(t *testing.T, ctx context.Context, proc *cp1Process, provider *bg3ProviderHarness, bot pi.ProductBot, name string, args any, opPrefix string) string {
	t.Helper()
	marker := "bg3-fg-" + opPrefix
	plan := &bg3ForegroundPlan{Call: &bg3ToolPlan{Name: name, Args: args}}
	provider.setForegroundPlan(marker, plan)
	defer provider.clearForegroundPlan(marker)
	_, receipt := bg3SaveSend(t, ctx, proc.client, bot.CurrentConversationID, marker, nil, opPrefix)
	settled := proc.waitSettled(t, ctx, receipt.Execution.ExecutionID)
	if settled.State != "completed" {
		t.Fatalf("foreground %s settled=%+v", name, settled)
	}
	provider.mu.Lock()
	res := plan.Result
	provider.mu.Unlock()
	if res == "" {
		t.Fatalf("missing tool result for %s", name)
	}
	return res
}

func TestPiProductBG3AutonomousOrchestrationTimelineAndPriority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	nodePath, workerPath := cp1WorkerPaths(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "ws")
	binDir := filepath.Join(root, "bin")
	mainDir := filepath.Join(root, "main")
	for _, dir := range []string{ws, binDir, mainDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	precheckBin := bg2CompilePrecheckFixture(t, binDir)
	clockPath := filepath.Join(root, "clock.json")
	epoch := bg2ParseISO(t, "2026-03-07T06:00:00.000Z")
	setClock := func(ms int64) {
		epoch = ms
		bg2WriteClock(t, clockPath, ms)
	}
	setClock(epoch)
	t.Setenv("NAGI_TEST_CLOCK_FILE", clockPath)
	t.Setenv("NAGI_WATCH_POLL_MS", "25")
	t.Setenv("NAGI_ORCHESTRATION_BACKOFF_BASE_MS", "60000")
	t.Setenv("NAGI_ORCHESTRATION_BACKOFF_MAX_MS", "180000")

	providerKey := "xuhu-bg3-fixture-segment-24681"
	provider := newBG3ProviderHarness(t, providerKey)
	mainOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(mainDir, "pi"),
		ProfilePath:     filepath.Join(mainDir, "contacts.json"),
		CredentialsPath: filepath.Join(mainDir, "connection.json"),
		FreshProfile:    true,
	}
	proc := startCP1Process(t, mainOpts)
	_ = proc.open(t, ctx)
	savedProvRev := bg3ConfigureProviders(t, ctx, proc.client, provider.server.URL, providerKey, ws, 2)

	bot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "bg3-bot-attention",
		Profile:     pi.ProductBotProfile{Name: "Attention", Purpose: "Watch fixture", Workspace: ws},
	})
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "bg3-bot-sibling",
		Profile:     pi.ProductBotProfile{Name: "Sibling", Purpose: "Sibling fixture", Workspace: ws},
	})
	if err != nil {
		t.Fatal(err)
	}

	beforeDraft, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ProductText: pi.ProductText{
			Text:      "unsent before the notice",
			Materials: []pi.ProductMaterial{{ID: "mat-before", Revision: 1, Name: "Notes", Text: "User material"}},
		},
		ExpectedRevision: 0,
		OperationID:      "bg3-draft-before",
	})
	if err != nil {
		t.Fatal(err)
	}

	firstWatch := bg3SaveWatch(t, ctx, proc.client, pi.ProductWatchSaveParams{
		BotID:       bot.BotID,
		Kind:        pi.ProductWatchTimer,
		Purpose:     "Empty transcript notice",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 1000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-watch-first",
	})
	provider.setMandatePlan("watch:"+firstWatch.WatchID, func(bg3WireEvent) *bg3ToolPlan {
		return &bg3ToolPlan{
			Name: "conversation_notify",
			Args: map[string]any{
				"text":       "A deadline expired " + providerKey,
				"urgency":    "normal",
				"sourceRefs": []map[string]string{{"kind": "watch", "id": firstWatch.WatchID}},
			},
		}
	})
	mirrorBefore := productRead(t, mainOpts.ProfilePath)
	setClock(bg2ParseISO(t, firstWatch.NextDueAt))
	firstResult := bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+firstWatch.WatchID, 1)
	if len(firstResult.RecentRuns) != 1 || firstResult.RecentRuns[0].RouteRole != "utility" ||
		firstResult.Metrics.Calls != 2 || firstResult.Metrics.InputTokens != 38 ||
		firstResult.Metrics.OutputTokens != 14 || firstResult.Metrics.TotalTokens != 52 ||
		firstResult.LastDisposition != pi.ProductDispositionNotified ||
		len(firstResult.RecentReceipts) == 0 || firstResult.RecentReceipts[0].Tool != "conversation_notify" ||
		firstResult.RecentReceipts[0].State != "settled_ok" {
		t.Fatalf("unexpected first orchestration detail: %+v", firstResult)
	}
	if !bytes.Equal(productRead(t, mainOpts.ProfilePath), mirrorBefore) {
		t.Fatal("private orchestration modified contacts.json mirror")
	}
	convAfterFirst, err := proc.client.GetConversation(ctx, bot.CurrentConversationID)
	if err != nil || convAfterFirst.Draft == nil || convAfterFirst.Draft.Revision != beforeDraft.Revision || convAfterFirst.Draft.Text != beforeDraft.Text {
		t.Fatalf("draft altered by notice: %+v err=%v", convAfterFirst, err)
	}
	if bg3HasDiskSession(filepath.Join(mainOpts.StateDir, "sessions")) {
		t.Fatal("private orchestration wrote a disk .jsonl session transcript")
	}
	histEmpty, err := proc.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
	})
	if err != nil || len(histEmpty.Items) != 0 {
		t.Fatalf("private orchestration leaked into conversation.history: %+v err=%v", histEmpty, err)
	}
	emptyTL, err := proc.client.ConversationTimeline(ctx, pi.ProductConversationTimelineParams{
		ConversationID: bot.CurrentConversationID,
	})
	if err != nil || emptyTL.TranscriptAvailable || emptyTL.TimelineRevision != 1 || len(emptyTL.Items) != 1 ||
		emptyTL.Items[0].Notice == nil || emptyTL.Items[0].Notice.AnchorKind != pi.ProductAnchorStandalone ||
		!strings.HasPrefix(emptyTL.Items[0].Text, "A deadline expired") || strings.Contains(emptyTL.Items[0].Text, providerKey) {
		t.Fatalf("unexpected empty timeline: %+v err=%v", emptyTL, err)
	}
	proc.waitEvent(t, ctx, func(ev pi.ProductEvent) bool {
		return ev.Name == "conversation.timelineChanged" && ev.TimelineChanged != nil &&
			ev.TimelineChanged.ConversationID == bot.CurrentConversationID && ev.TimelineChanged.TimelineRevision == 1
	})
	_, err = proc.client.GetOrchestration(ctx, sibling.BotID, firstResult.MandateID)
	cp1AssertRemoteCode(t, err, "not_found")

	pinRev, err := proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: savedProvRev,
		Overrides:     pi.ProductRoutingOverridesPatch{"utility": {Mode: "route", ProviderID: "utility-pin-fixture", Model: "gpt-4o-mini"}},
		OperationID:   "bg3-ov-pin",
	})
	if err != nil {
		t.Fatal(err)
	}
	pinApplied, err := proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		SavedRevision: *pinRev.SavedRevision, OperationID: "bg3-apply-pin",
	})
	if err != nil {
		t.Fatal(err)
	}
	silentWatch := bg3SaveWatch(t, ctx, proc.client, pi.ProductWatchSaveParams{
		BotID:       bot.BotID,
		Kind:        pi.ProductWatchTimer,
		Purpose:     "Silent pinned utility check",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 1000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-watch-silent",
	})
	reqBeforePin := len(provider.requestList())
	setClock(bg2ParseISO(t, silentWatch.NextDueAt))
	silentRes := bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+silentWatch.WatchID, 1)
	if silentRes.LastDisposition != pi.ProductDispositionSilent {
		t.Fatalf("expected silent disposition, got %+v", silentRes)
	}
	pinReqs := provider.requestList()[reqBeforePin:]
	if len(pinReqs) != 2 || pinReqs[0].URL != "/utility-pin/v1/chat/completions" || pinReqs[0].Model != "gpt-4o-mini" {
		t.Fatalf("pinned utility route not honored: %+v", pinReqs)
	}
	unpinRev, err := proc.client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{
		SavedRevision: *pinApplied.SavedRevision,
		Overrides:     pi.ProductRoutingOverridesPatch{"utility": {Mode: "route", ProviderID: "utility-fixture", Model: "gpt-4.1-mini"}},
		OperationID:   "bg3-ov-unpin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		SavedRevision: *unpinRev.SavedRevision, OperationID: "bg3-apply-unpin",
	}); err != nil {
		t.Fatal(err)
	}

	fgMarker := "human foreground barrier"
	publicGate := provider.armBarrier(fgMarker)
	_, fgReceipt := bg3SaveSend(t, ctx, proc.client, bot.CurrentConversationID, fgMarker, nil, "bg3-fg-completed")
	if fgReceipt.State != "accepted" {
		t.Fatalf("fg send state=%q want=accepted", fgReceipt.State)
	}
	publicReq := publicGate.await(t, ctx, "foreground barrier")
	if publicReq.URL != "/dialogue/v1/chat/completions" || publicReq.Model != "gpt-4.1" || strings.Contains(publicReq.RawBody, "A deadline expired") {
		t.Fatalf("unexpected foreground provider request: %+v", publicReq)
	}
	convDuringFG, err := proc.client.GetConversation(ctx, bot.CurrentConversationID)
	if err != nil || convDuringFG.Draft == nil {
		t.Fatalf("GetConversation during foreground = %+v err=%v", convDuringFG, err)
	}
	latestDraft, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ProductText: pi.ProductText{
			Text:      "keep the new draft and material",
			Materials: []pi.ProductMaterial{{ID: "mat-fg", Revision: 1, Name: "FG", Text: "Draft material"}},
		},
		ExpectedRevision: convDuringFG.Draft.Revision,
		OperationID:      "bg3-draft-during-fg",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondWatch := bg3SaveWatch(t, ctx, proc.client, pi.ProductWatchSaveParams{
		BotID:       bot.BotID,
		Kind:        pi.ProductWatchTimer,
		Purpose:     "After settlement notice",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 1000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-watch-second",
	})
	provider.setMandatePlan("watch:"+secondWatch.WatchID, func(bg3WireEvent) *bg3ToolPlan {
		return &bg3ToolPlan{Name: "conversation_notify", Args: map[string]any{"text": "Settled after this turn", "urgency": "normal"}}
	})
	setClock(bg2ParseISO(t, secondWatch.NextDueAt))
	_ = bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+secondWatch.WatchID, 1)
	heldTL, err := proc.client.ConversationTimeline(ctx, pi.ProductConversationTimelineParams{ConversationID: bot.CurrentConversationID})
	if err != nil || heldTL.TimelineRevision != 1 {
		t.Fatalf("post_settlement timeline revision changed before settlement: %+v err=%v", heldTL, err)
	}
	for _, it := range heldTL.Items {
		if it.Text == "Settled after this turn" {
			t.Fatalf("post_settlement notice leaked before settlement: %+v", heldTL)
		}
	}
	publicGate.open()
	if settledFG := proc.waitSettled(t, ctx, fgReceipt.Execution.ExecutionID); settledFG.State != "completed" {
		t.Fatalf("foreground turn settled=%+v want=completed", settledFG)
	}
	mergedTL, err := proc.client.ConversationTimeline(ctx, pi.ProductConversationTimelineParams{ConversationID: bot.CurrentConversationID})
	if err != nil || !mergedTL.TranscriptAvailable || len(mergedTL.Items) != 4 {
		t.Fatalf("merged timeline=%+v err=%v", mergedTL, err)
	}
	gotKinds := []string{mergedTL.Items[0].Kind, mergedTL.Items[1].Kind, mergedTL.Items[2].Kind, mergedTL.Items[3].Kind}
	if !slices.Equal(gotKinds, []string{"notice", "message", "message", "notice"}) {
		t.Fatalf("timeline kinds=%v want=[notice message message notice]", gotKinds)
	}
	if mergedTL.Items[3].Notice == nil || mergedTL.Items[3].Notice.AnchorKind != pi.ProductAnchorPostSettlement ||
		mergedTL.Items[3].Notice.AnchorExecutionID != fgReceipt.Execution.ExecutionID || mergedTL.Items[3].Notice.AnchorOutcome != "completed" {
		t.Fatalf("post_settlement notice anchor mismatch: %+v", mergedTL.Items[3].Notice)
	}
	convAfterFG, err := proc.client.GetConversation(ctx, bot.CurrentConversationID)
	if err != nil || convAfterFG.Draft == nil || convAfterFG.Draft.Revision != latestDraft.Revision || convAfterFG.Draft.Text != latestDraft.Text {
		t.Fatalf("draft altered across post_settlement notice: %+v err=%v", convAfterFG, err)
	}
	histAfterFG, err := proc.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
	})
	if err != nil || len(histAfterFG.Items) != 2 ||
		histAfterFG.Items[0].MessageID != mergedTL.Items[1].MessageID ||
		histAfterFG.Items[1].MessageID != mergedTL.Items[2].MessageID {
		t.Fatalf("history/timeline message ID mismatch: hist=%+v tl=%+v err=%v", histAfterFG, mergedTL, err)
	}
	pageTL, err := proc.client.ConversationTimeline(ctx, pi.ProductConversationTimelineParams{
		ConversationID: bot.CurrentConversationID, Limit: 1,
	})
	if err != nil || pageTL.NextCursor == nil {
		t.Fatalf("expected timeline cursor page, got %+v err=%v", pageTL, err)
	}
	_, err = proc.client.ConversationTimeline(ctx, pi.ProductConversationTimelineParams{
		ConversationID: sibling.CurrentConversationID, Cursor: *pageTL.NextCursor,
	})
	cp1AssertRemoteCode(t, err, "invalid_cursor")

	quoteObj := &pi.ProductQuote{
		MessageID: histAfterFG.Items[1].MessageID, BubbleIndex: intPtr(0), Sender: "bot", AuthorName: "Attention", Text: histAfterFG.Items[1].Text,
	}
	_, qReceipt := bg3SaveSend(t, ctx, proc.client, bot.CurrentConversationID, "quoted check turn", quoteObj, "bg3-fg-quoted")
	if settledQ := proc.waitSettled(t, ctx, qReceipt.Execution.ExecutionID); settledQ.State != "completed" {
		t.Fatalf("quoted turn settled=%+v", settledQ)
	}
	histQ, err := proc.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
	})
	if err != nil {
		t.Fatal(err)
	}
	tlQ, err := proc.client.ConversationTimeline(ctx, pi.ProductConversationTimelineParams{
		ConversationID: bot.CurrentConversationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var histQuoteMsg, tlQuoteMsg *int
	for _, m := range histQ.Items {
		if m.Quote != nil {
			histQuoteMsg = m.Quote.BubbleIndex
		}
	}
	for _, it := range tlQ.Items {
		if it.Kind == "message" && it.Quote != nil {
			tlQuoteMsg = it.Quote.BubbleIndex
		}
	}
	if histQuoteMsg == nil || tlQuoteMsg == nil || *histQuoteMsg != 0 || *tlQuoteMsg != 0 {
		t.Fatalf("quote bubbleIndex mismatch: hist=%v tl=%v", histQuoteMsg, tlQuoteMsg)
	}

	failMarker := "foreground failure barrier"
	provider.setFailure(failMarker, http.StatusInternalServerError)
	failGate := provider.armBarrier(failMarker)
	_, failFG := bg3SaveSend(t, ctx, proc.client, bot.CurrentConversationID, failMarker, nil, "bg3-fg-failed")
	_ = failGate.await(t, ctx, "failed foreground barrier")
	failNoticeWatch := bg3SaveWatch(t, ctx, proc.client, pi.ProductWatchSaveParams{
		BotID:       bot.BotID,
		Kind:        pi.ProductWatchTimer,
		Purpose:     "Failed anchor notice",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 1000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-watch-fail-anchor",
	})
	provider.setMandatePlan("watch:"+failNoticeWatch.WatchID, func(bg3WireEvent) *bg3ToolPlan {
		return &bg3ToolPlan{Name: "conversation_notify", Args: map[string]any{"text": "Notice after failed send", "urgency": "high"}}
	})
	setClock(bg2ParseISO(t, failNoticeWatch.NextDueAt))
	_ = bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+failNoticeWatch.WatchID, 1)
	failGate.open()
	if settledFail := proc.waitSettled(t, ctx, failFG.Execution.ExecutionID); settledFail.State != "failed" {
		t.Fatalf("expected failed foreground turn, got %+v", settledFail)
	}
	tlAfterFail, err := proc.client.ConversationTimeline(ctx, pi.ProductConversationTimelineParams{
		ConversationID: bot.CurrentConversationID,
	})
	if err != nil || len(tlAfterFail.Items) == 0 {
		t.Fatalf("timeline after failed send: %+v err=%v", tlAfterFail, err)
	}
	lastAfterFail := tlAfterFail.Items[len(tlAfterFail.Items)-1]
	if lastAfterFail.Kind != "notice" || lastAfterFail.Text != "Notice after failed send" ||
		lastAfterFail.Notice.AnchorKind != pi.ProductAnchorPostSettlement ||
		lastAfterFail.Notice.AnchorExecutionID != failFG.Execution.ExecutionID ||
		lastAfterFail.Notice.AnchorOutcome != "failed" {
		t.Fatalf("failed post_settlement notice mismatch: %+v", lastAfterFail)
	}
	_, recFG := bg3SaveSend(t, ctx, proc.client, bot.CurrentConversationID, "clear dialogue health failure", nil, "bg3-fg-recover-health")
	if settledRec := proc.waitSettled(t, ctx, recFG.Execution.ExecutionID); settledRec.State != "completed" {
		t.Fatalf("recovery foreground turn failed: %+v", settledRec)
	}

	origConvID := bot.CurrentConversationID
	frozenWatch := bg3SaveWatch(t, ctx, proc.client, pi.ProductWatchSaveParams{
		BotID:       bot.BotID,
		Kind:        pi.ProductWatchTimer,
		Purpose:     "Frozen target across new chat",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 5000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-watch-frozen",
	})
	provider.setMandatePlan("watch:"+frozenWatch.WatchID, func(bg3WireEvent) *bg3ToolPlan {
		return &bg3ToolPlan{Name: "conversation_notify", Args: map[string]any{"text": "Frozen target notice", "urgency": "normal"}}
	})
	rotatedBot, err := proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			OperationID: "bg3-new-chat", BotID: bot.BotID, ExpectedRevision: bot.ProfileRevision,
		},
		Profile:              bot.Profile,
		StartNewConversation: true,
	})
	if err != nil || rotatedBot.CurrentConversationID == origConvID {
		t.Fatalf("StartNewConversation = %+v err=%v", rotatedBot, err)
	}
	setClock(bg2ParseISO(t, frozenWatch.NextDueAt))
	_ = bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+frozenWatch.WatchID, 1)
	oldTL, err := proc.client.ConversationTimeline(ctx, pi.ProductConversationTimelineParams{ConversationID: origConvID})
	if err != nil || oldTL.Items[len(oldTL.Items)-1].Text != "Frozen target notice" {
		t.Fatalf("frozen target notice missing from original conversation: %+v err=%v", oldTL, err)
	}
	newTL, err := proc.client.ConversationTimeline(ctx, pi.ProductConversationTimelineParams{ConversationID: rotatedBot.CurrentConversationID})
	if err != nil || len(newTL.Items) != 0 {
		t.Fatalf("frozen target notice leaked into new conversation: %+v err=%v", newTL, err)
	}

	interval10 := &pi.ProductScheduleSpec{
		CadenceKind: pi.ProductScheduleInterval, IntervalSeconds: intPtr(10), TimeZone: "UTC", JitterSeconds: intPtr(0),
	}
	stuck := bg3SaveWatch(t, ctx, proc.client, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Stuck mandate", Schedule: interval10, OperationID: "bg3-sched-stuck",
	})
	advancing := bg3SaveWatch(t, ctx, proc.client, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Advancing mandate", Schedule: interval10, OperationID: "bg3-sched-advancing",
	})
	provider.setMandatePlan("watch:"+stuck.WatchID, func(bg3WireEvent) *bg3ToolPlan {
		return &bg3ToolPlan{
			Name: "orchestration_finish",
			Args: map[string]any{"summary": "Waiting for change", "observedStateHash": "same", "actionClass": "observe", "progressMade": false},
		}
	})
	advStart := bg2ParseISO(t, advancing.NextDueAt)
	for tick := 1; tick <= 5; tick++ {
		setClock(advStart + int64(tick-1)*10_000)
		_ = bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+advancing.WatchID, uint64(tick))
		if tick <= 2 {
			_ = bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+stuck.WatchID, uint64(tick))
		}
	}
	stalled := bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+stuck.WatchID, 2)
	if stalled.Metrics.TotalWakes != 2 || stalled.Metrics.BackoffDelayMS != 60000 ||
		stalled.Metrics.ConsecutiveNoProgress != 2 || stalled.Metrics.NextEligibleAt == "" ||
		stalled.LastDisposition != pi.ProductDispositionNotified {
		t.Fatalf("causal backoff mismatch: %+v", stalled)
	}
	progress := bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+advancing.WatchID, 5)
	if progress.Metrics.TotalWakes != 5 || progress.Metrics.BackoffDelayMS != 0 || progress.Generation <= 1 {
		t.Fatalf("sibling progress/rotation mismatch: %+v", progress)
	}
	ckptRow := runNodeSQLite(t, nodePath, filepath.Join(mainDir, "xuhu.db"), fmt.Sprintf(`
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const row = db.prepare('SELECT generation, working_summary FROM orchestration_checkpoints WHERE mandate_id = ?').get(JSON.stringify(%q));
db.close();
console.log(JSON.stringify(row));
`, "watch:"+advancing.WatchID))
	if !strings.Contains(ckptRow, fmt.Sprintf(`"generation":%d`, progress.Generation)) || strings.Contains(ckptRow, "Private assistant result") {
		t.Fatalf("checkpoint row mismatch or leaked raw prose: %s", ckptRow)
	}
	provider.setMandatePlan("watch:"+stuck.WatchID, func(bg3WireEvent) *bg3ToolPlan {
		return &bg3ToolPlan{
			Name: "orchestration_finish",
			Args: map[string]any{"summary": "Observed external change", "observedStateHash": "changed", "actionClass": "observe", "progressMade": true},
		}
	})
	setClock(bg2ParseISO(t, stalled.Metrics.NextEligibleAt) + 1000)
	recoveredStuck := bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+stuck.WatchID, 3)
	if recoveredStuck.Metrics.BackoffDelayMS != 0 || recoveredStuck.Metrics.ConsecutiveNoProgress != 0 || recoveredStuck.Metrics.LastCoalescedCount < 2 {
		t.Fatalf("stuck backoff did not clear on progress: %+v", recoveredStuck)
	}
	_ = bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+advancing.WatchID, 6)

	badRev := uint64(999)
	_, err = proc.client.StopOrchestration(ctx, pi.ProductOrchestrationStopParams{
		BotID: bot.BotID, MandateID: recoveredStuck.MandateID, ExpectedRevision: &badRev, OperationID: "bg3-stop-bad-rev",
	})
	cp1AssertRemoteCode(t, err, "stale_revision")
	stopParams := pi.ProductOrchestrationStopParams{
		BotID: bot.BotID, MandateID: recoveredStuck.MandateID, OperationID: "bg3-stop-stuck",
	}
	stopReceipt1, err := proc.client.StopOrchestration(ctx, stopParams)
	if err != nil {
		t.Fatal(err)
	}
	stopReceipt2, err := proc.client.StopOrchestration(ctx, stopParams)
	if err != nil || string(stopReceipt1.Result) != string(stopReceipt2.Result) {
		t.Fatalf("StopOrchestration idempotency mismatch: r1=%+v r2=%+v err=%v", stopReceipt1, stopReceipt2, err)
	}
	stopRes, err := pi.DecodeOrchestrationStopResult(stopReceipt1)
	if err != nil || stopRes.Mandate.State != pi.ProductMandateStopped {
		t.Fatalf("DecodeOrchestrationStopResult = %+v err=%v", stopRes, err)
	}
	stuckWatchAfter, err := proc.client.GetWatch(ctx, bot.BotID, stuck.WatchID)
	if err != nil || stuckWatchAfter.State != pi.ProductWatchPaused {
		t.Fatalf("stopped mandate watch not paused: %+v err=%v", stuckWatchAfter, err)
	}
	advStillActive, err := proc.client.GetOrchestration(ctx, bot.BotID, "watch:"+advancing.WatchID)
	if err != nil || advStillActive.State != pi.ProductMandateActive {
		t.Fatalf("stopping stuck mandate affected sibling mandate: %+v err=%v", advStillActive, err)
	}
	if _, err := proc.client.StopOrchestration(ctx, pi.ProductOrchestrationStopParams{
		BotID: bot.BotID, MandateID: advStillActive.MandateID, OperationID: "bg3-stop-advancing",
	}); err != nil {
		t.Fatal(err)
	}

	cardArgs := map[string]any{
		"kind":    "approval",
		"purpose": "Controlled choice",
		"summary": "Choose an option",
		"options": []map[string]string{{"id": "ok", "label": "Okay"}},
	}
	approvedW := bg3SaveWatch(t, ctx, proc.client, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchTimer, Purpose: "Approved decision",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 1000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-watch-approve",
	})
	provider.setMandatePlan("watch:"+approvedW.WatchID, func(ev bg3WireEvent) *bg3ToolPlan {
		for _, f := range ev.Facts {
			if f.Kind == "decision_resolved" {
				return &bg3ToolPlan{Name: "conversation_notify", Args: map[string]any{"text": "Decision received", "urgency": "normal"}}
			}
		}
		return &bg3ToolPlan{Name: "orchestration_request_decision", Args: cardArgs}
	})
	setClock(bg2ParseISO(t, approvedW.NextDueAt))
	waitingApprove := bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+approvedW.WatchID, 1)
	if waitingApprove.State != pi.ProductMandateWaitingDecision || waitingApprove.LastDisposition != pi.ProductDispositionNeedsDecision || waitingApprove.PendingDecision == nil {
		t.Fatalf("unexpected waiting_decision detail: %+v", waitingApprove)
	}
	listWait, err := proc.client.ListOrchestrations(ctx, pi.ProductOrchestrationListParams{
		BotID: bot.BotID, States: []pi.ProductMandateState{pi.ProductMandateWaitingDecision},
	})
	if err != nil || len(listWait.Items) != 1 || listWait.Items[0].MandateID != waitingApprove.MandateID {
		t.Fatalf("ListOrchestrations waiting_decision mismatch: %+v err=%v", listWait, err)
	}
	listPage, err := proc.client.ListOrchestrations(ctx, pi.ProductOrchestrationListParams{BotID: bot.BotID, Limit: 1})
	if err != nil || listPage.NextCursor == nil {
		t.Fatalf("ListOrchestrations limit=1 mismatch: %+v err=%v", listPage, err)
	}
	_, err = proc.client.ListOrchestrations(ctx, pi.ProductOrchestrationListParams{
		BotID: sibling.BotID, Cursor: *listPage.NextCursor,
	})
	cp1AssertRemoteCode(t, err, "invalid_cursor")

	card := waitingApprove.PendingDecision
	_, err = proc.client.DecideOrchestration(ctx, pi.ProductOrchestrationDecideParams{
		BotID: sibling.BotID, MandateID: waitingApprove.MandateID, DecisionID: card.DecisionID,
		ExpectedRevision: card.Revision, Outcome: pi.ProductDecisionApprove, SelectedOptionID: "ok", OperationID: "bg3-decide-wrong-bot",
	})
	cp1AssertRemoteCode(t, err, "not_found")
	_, err = proc.client.DecideOrchestration(ctx, pi.ProductOrchestrationDecideParams{
		BotID: bot.BotID, MandateID: waitingApprove.MandateID, DecisionID: card.DecisionID,
		ExpectedRevision: 99, Outcome: pi.ProductDecisionApprove, SelectedOptionID: "ok", OperationID: "bg3-decide-stale",
	})
	cp1AssertRemoteCode(t, err, "stale_revision")
	_, err = proc.client.DecideOrchestration(ctx, pi.ProductOrchestrationDecideParams{
		BotID: bot.BotID, MandateID: waitingApprove.MandateID, DecisionID: card.DecisionID,
		ExpectedRevision: card.Revision, Outcome: pi.ProductDecisionApprove, SelectedOptionID: "bad-option", OperationID: "bg3-decide-bad-opt",
	})
	cp1AssertRemoteCode(t, err, "invalid_params")

	approveParams := pi.ProductOrchestrationDecideParams{
		BotID: bot.BotID, MandateID: waitingApprove.MandateID, DecisionID: card.DecisionID,
		ExpectedRevision: card.Revision, Outcome: pi.ProductDecisionApprove, SelectedOptionID: "ok", OperationID: "bg3-decide-approve",
	}
	decReceipt, err := proc.client.DecideOrchestration(ctx, approveParams)
	if err != nil {
		t.Fatal(err)
	}
	decReplay, err := proc.client.DecideOrchestration(ctx, approveParams)
	if err != nil || string(decReplay.Result) != string(decReceipt.Result) {
		t.Fatalf("DecideOrchestration replay mismatch: %+v vs %+v err=%v", decReplay, decReceipt, err)
	}
	decOp, err := proc.client.Operation(ctx, approveParams.OperationID)
	if err != nil || string(decOp.Result) != string(decReceipt.Result) {
		t.Fatalf("DecideOrchestration operation mismatch: %+v vs %+v err=%v", decOp, decReceipt, err)
	}
	decResult, err := pi.DecodeOrchestrationDecisionResult(decReceipt)
	if err != nil || decResult.Decision.State != "approved" {
		t.Fatalf("DecodeOrchestrationDecisionResult = %+v err=%v", decResult, err)
	}
	afterApprove := bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, waitingApprove.MandateID, 2)
	if afterApprove.LastDisposition != pi.ProductDispositionNotified {
		t.Fatalf("approved decision wake disposition=%+v", afterApprove)
	}

	rejectedW := bg3SaveWatch(t, ctx, proc.client, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchTimer, Purpose: "Rejected decision",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 1000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-watch-reject",
	})
	provider.setMandatePlan("watch:"+rejectedW.WatchID, func(bg3WireEvent) *bg3ToolPlan {
		return &bg3ToolPlan{Name: "orchestration_request_decision", Args: cardArgs}
	})
	setClock(bg2ParseISO(t, rejectedW.NextDueAt))
	waitingReject := bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+rejectedW.WatchID, 1)
	rejReceipt, err := proc.client.DecideOrchestration(ctx, pi.ProductOrchestrationDecideParams{
		BotID: bot.BotID, MandateID: waitingReject.MandateID, DecisionID: waitingReject.PendingDecision.DecisionID,
		ExpectedRevision: waitingReject.PendingDecision.Revision, Outcome: pi.ProductDecisionReject, OperationID: "bg3-decide-reject",
	})
	if err != nil {
		t.Fatal(err)
	}
	rejRes, err := pi.DecodeOrchestrationDecisionResult(rejReceipt)
	if err != nil || rejRes.Mandate.State != pi.ProductMandatePaused || rejRes.Decision.State != "rejected" {
		t.Fatalf("reject decision result=%+v err=%v", rejRes, err)
	}
	afterRej, err := proc.client.GetOrchestration(ctx, bot.BotID, waitingReject.MandateID)
	if err != nil || afterRej.Metrics.TotalWakes != 1 {
		t.Fatalf("reject triggered unexpected wake: %+v err=%v", afterRej, err)
	}

	parkedW := bg3SaveWatch(t, ctx, proc.client, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchTimer, Purpose: "Dormant backup decision",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 1000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-watch-parked",
	})
	provider.setMandatePlan("watch:"+parkedW.WatchID, func(bg3WireEvent) *bg3ToolPlan {
		return &bg3ToolPlan{Name: "orchestration_request_decision", Args: cardArgs}
	})
	setClock(bg2ParseISO(t, parkedW.NextDueAt))
	parkedRes := bg3AwaitMandateSettled(t, ctx, proc.client, bot.BotID, "watch:"+parkedW.WatchID, 1)
	if parkedRes.PendingDecision == nil {
		t.Fatalf("expected open pending decision for backup: %+v", parkedRes)
	}

	backupPath := filepath.Join(root, "recovery.json")
	mirrorBeforeBackup := productRead(t, mainOpts.ProfilePath)
	callsBeforeBackup := provider.calls.Load()
	gotBackupPath, err := proc.client.BackupProfile(ctx, backupPath)
	if err != nil || gotBackupPath != backupPath {
		t.Fatalf("BackupProfile = %q err=%v", gotBackupPath, err)
	}
	if !bytes.Equal(productRead(t, mainOpts.ProfilePath), mirrorBeforeBackup) {
		t.Fatal("BackupProfile modified contacts.json mirror")
	}
	backupRaw := productRead(t, backupPath)
	for _, forbidden := range []string{providerKey, "Private assistant result", `"watchGrants"`, `"botEventInbox"`, `"botEventBatches"`} {
		if bytes.Contains(backupRaw, []byte(forbidden)) {
			t.Fatalf("backup leaked forbidden string %q", forbidden)
		}
	}
	inspectJSON := bg2RunNodeJSON(t, nodePath, fmt.Sprintf(`
import { readFileSync } from 'node:fs';
const { inspectProductRecovery } = await import(%q);
console.log(JSON.stringify(inspectProductRecovery(JSON.parse(readFileSync(process.argv[1], 'utf8')))));
`, "file://"+filepath.Join(filepath.Dir(workerPath), "product-profile.js")), backupPath)
	var recoveredCounts struct {
		Contacts        int `json:"contacts"`
		Notices         int `json:"notices"`
		Decisions       int `json:"decisions"`
		DormantMandates int `json:"dormantMandates"`
	}
	if err := json.Unmarshal([]byte(inspectJSON), &recoveredCounts); err != nil {
		t.Fatal(err)
	}
	if recoveredCounts.Contacts != 2 || recoveredCounts.Decisions != 3 || recoveredCounts.Notices < 3 || recoveredCounts.DormantMandates < 6 {
		t.Fatalf("inspectProductRecovery = %+v", recoveredCounts)
	}
	proc.shutdown(t, ctx)

	restoreDir := filepath.Join(root, "restored")
	if err := os.MkdirAll(restoreDir, 0o700); err != nil {
		t.Fatal(err)
	}
	restoreProfile := filepath.Join(restoreDir, "contacts.json")
	if err := os.WriteFile(restoreProfile, backupRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	restoreOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(restoreDir, "pi"),
		ProfilePath:     restoreProfile,
		CredentialsPath: filepath.Join(restoreDir, "connection.json"),
	}
	for round := 0; round < 2; round++ {
		rProc := startCP1Process(t, restoreOpts)
		_ = rProc.open(t, ctx)
		dormant, err := rProc.client.GetOrchestration(ctx, bot.BotID, parkedRes.MandateID)
		if err != nil || dormant.State != pi.ProductMandateRestoredReadOnly || !dormant.Dormant ||
			dormant.PendingDecision == nil || !dormant.PendingDecision.Dormant {
			t.Fatalf("restored mandate not dormant in round %d: %+v err=%v", round, dormant, err)
		}
		_, err = rProc.client.DecideOrchestration(ctx, pi.ProductOrchestrationDecideParams{
			BotID: bot.BotID, MandateID: dormant.MandateID, DecisionID: dormant.PendingDecision.DecisionID,
			ExpectedRevision: dormant.PendingDecision.Revision, Outcome: pi.ProductDecisionApprove, OperationID: fmt.Sprintf("restore-dec-%d", round),
		})
		cp1AssertRemoteCode(t, err, "invalid_state")
		_, err = rProc.client.StopOrchestration(ctx, pi.ProductOrchestrationStopParams{
			BotID: bot.BotID, MandateID: dormant.MandateID, OperationID: fmt.Sprintf("restore-stop-%d", round),
		})
		cp1AssertRemoteCode(t, err, "invalid_state")
		restoredTL, err := rProc.client.ConversationTimeline(ctx, pi.ProductConversationTimelineParams{ConversationID: origConvID})
		if err != nil || restoredTL.TranscriptAvailable || len(restoredTL.Items) == 0 {
			t.Fatalf("restored timeline mismatch round %d: %+v err=%v", round, restoredTL, err)
		}
		for _, item := range restoredTL.Items {
			if item.Kind != "notice" || item.Notice == nil || !item.Notice.Dormant {
				t.Fatalf("restored timeline contains non-dormant item in round %d: %+v", round, item)
			}
		}
		rProc.shutdown(t, ctx)
	}
	if provider.calls.Load() != callsBeforeBackup {
		t.Fatalf("backup/restore triggered %d provider calls", provider.calls.Load()-callsBeforeBackup)
	}

	queueDir := filepath.Join(root, "queue")
	if err := os.MkdirAll(queueDir, 0o700); err != nil {
		t.Fatal(err)
	}
	queueOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(queueDir, "pi"),
		ProfilePath:     filepath.Join(queueDir, "contacts.json"),
		CredentialsPath: filepath.Join(queueDir, "connection.json"),
		FreshProfile:    true,
	}
	qProc := startCP1Process(t, queueOpts)
	_ = qProc.open(t, ctx)
	_ = bg3ConfigureProviders(t, ctx, qProc.client, provider.server.URL, providerKey, ws, 1)
	humanBot, err := qProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "bg3-q-human", Profile: pi.ProductBotProfile{Name: "Human", Purpose: "Queue priority", Workspace: ws},
	})
	if err != nil {
		t.Fatal(err)
	}
	autoBot, err := qProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "bg3-q-auto", Profile: pi.ProductBotProfile{Name: "Autonomous", Purpose: "Queue slot", Workspace: ws},
	})
	if err != nil {
		t.Fatal(err)
	}

	inFlightW := bg3SaveWatch(t, ctx, qProc.client, pi.ProductWatchSaveParams{
		BotID: autoBot.BotID, Kind: pi.ProductWatchTimer, Purpose: "Occupied provider slot",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 1000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-q-inflight",
	})
	nextW := bg3SaveWatch(t, ctx, qProc.client, pi.ProductWatchSaveParams{
		BotID: autoBot.BotID, Kind: pi.ProductWatchTimer, Purpose: "Next autonomous wake",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 2000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-q-next",
	})
	hold := provider.armBarrier("watch:" + inFlightW.WatchID)
	setClock(bg2ParseISO(t, inFlightW.NextDueAt))
	_ = hold.await(t, ctx, "occupied slot barrier")

	humanGate := provider.armBarrier("human is next")
	pendingDraft, pendingSend := bg3SaveSend(t, ctx, qProc.client, humanBot.CurrentConversationID, "human is next", nil, "bg3-q-pending")
	pendingSendRes, err := pi.DecodeConversationSendResult(pendingSend)
	if err != nil || pendingSend.State != "pending" || !pendingSendRes.Queued ||
		pendingSendRes.QueuePosition != 1 || pendingSendRes.Reason != "provider_capacity" ||
		pendingSendRes.DraftRevision != pendingDraft.Revision {
		t.Fatalf("pending send mismatch: receipt=%+v decoded=%+v err=%v", pendingSend, pendingSendRes, err)
	}
	convPending, err := qProc.client.GetConversation(ctx, humanBot.CurrentConversationID)
	if err != nil || convPending.Draft == nil || convPending.Draft.Text != pendingDraft.Text {
		t.Fatalf("draft cleared prematurely while pending: %+v err=%v", convPending, err)
	}
	setClock(bg2ParseISO(t, nextW.NextDueAt))
	bg1WaitUntil(t, ctx, "next timer settled while held", func() bool {
		w, err := qProc.client.GetWatch(ctx, nextW.OwnerBotID, nextW.WatchID)
		return err == nil && w.State == pi.ProductWatchSettled
	})
	nextBeforeHuman, err := qProc.client.GetOrchestration(ctx, autoBot.BotID, "watch:"+nextW.WatchID)
	if err != nil || nextBeforeHuman.Metrics.TotalWakes != 0 {
		t.Fatalf("next autonomous wake bypassed capacity/human queue: %+v err=%v", nextBeforeHuman, err)
	}
	hold.open()
	_ = humanGate.await(t, ctx, "human preempts waiting autonomous timer")
	pendingOpAfterDispatch, err := qProc.client.Operation(ctx, pendingSend.OperationID)
	if err != nil || pendingOpAfterDispatch.State != "accepted" {
		t.Fatalf("queued send operation not transitioned to accepted: %+v err=%v", pendingOpAfterDispatch, err)
	}
	nextWhileHumanRuns, err := qProc.client.GetOrchestration(ctx, autoBot.BotID, "watch:"+nextW.WatchID)
	if err != nil || nextWhileHumanRuns.Metrics.TotalWakes != 0 {
		t.Fatalf("next autonomous ran ahead of queued human send: %+v err=%v", nextWhileHumanRuns, err)
	}
	humanGate.open()
	if settledH := qProc.waitSettled(t, ctx, pendingSend.Execution.ExecutionID); settledH.State != "completed" {
		t.Fatalf("human queued send settled=%+v", settledH)
	}
	_ = bg3AwaitMandateSettled(t, ctx, qProc.client, autoBot.BotID, "watch:"+nextW.WatchID, 1)
	convAfterHuman, err := qProc.client.GetConversation(ctx, humanBot.CurrentConversationID)
	if err != nil || convAfterHuman.Draft == nil || convAfterHuman.Draft.Text != "" {
		t.Fatalf("human draft not cleared after dispatch/settlement: %+v err=%v", convAfterHuman, err)
	}

	cancelOccW := bg3SaveWatch(t, ctx, qProc.client, pi.ProductWatchSaveParams{
		BotID: autoBot.BotID, Kind: pi.ProductWatchTimer, Purpose: "Occupy slot for cancel",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 1000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-q-cancel-occ",
	})
	cancelHold := provider.armBarrier("watch:" + cancelOccW.WatchID)
	setClock(bg2ParseISO(t, cancelOccW.NextDueAt))
	_ = cancelHold.await(t, ctx, "cancel occupied barrier")
	cancelDraft, cancelSend := bg3SaveSend(t, ctx, qProc.client, humanBot.CurrentConversationID, "cancel this queued send", nil, "bg3-q-cancel")
	if cancelSend.State != "pending" {
		t.Fatalf("expected pending send before cancel, got %+v", cancelSend)
	}
	if _, err := qProc.client.StopConversation(ctx, pi.ProductConversationStopParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: humanBot.CurrentConversationID},
		ExecutionID:               cancelSend.Execution.ExecutionID,
		OperationID:               "bg3-q-stop-pending",
	}); err != nil {
		t.Fatal(err)
	}
	cancelReceipt, err := qProc.client.Operation(ctx, cancelSend.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	cancelRes, err := pi.DecodeConversationSendResult(cancelReceipt)
	if err != nil || cancelReceipt.State != "rejected" || cancelReceipt.Execution == nil ||
		cancelReceipt.Execution.State != "interrupted" || cancelRes.Delivery != "non_dispatched" ||
		cancelRes.Reason != "cancelled_before_dispatch" {
		t.Fatalf("cancelled pending send receipt mismatch: receipt=%+v decoded=%+v err=%v", cancelReceipt, cancelRes, err)
	}
	convAfterCancel, err := qProc.client.GetConversation(ctx, humanBot.CurrentConversationID)
	if err != nil || convAfterCancel.Draft == nil || convAfterCancel.Draft.Text != cancelDraft.Text {
		t.Fatalf("cancelled pending send modified draft: %+v err=%v", convAfterCancel, err)
	}
	cancelHold.open()
	_ = bg3AwaitMandateSettled(t, ctx, qProc.client, autoBot.BotID, "watch:"+cancelOccW.WatchID, 1)

	crashOccW := bg3SaveWatch(t, ctx, qProc.client, pi.ProductWatchSaveParams{
		BotID: autoBot.BotID, Kind: pi.ProductWatchTimer, Purpose: "Occupy slot while crashing",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 1000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-q-crash-occ",
	})
	crashHold := provider.armBarrier("watch:" + crashOccW.WatchID)
	setClock(bg2ParseISO(t, crashOccW.NextDueAt))
	_ = crashHold.await(t, ctx, "crash occupied barrier")
	crashDraft, crashSend := bg3SaveSend(t, ctx, qProc.client, humanBot.CurrentConversationID, "pending during worker crash", nil, "bg3-q-crash")
	if crashSend.State != "pending" {
		t.Fatalf("expected pending send before crash, got %+v", crashSend)
	}
	killOut, killErr := qProc.client.Kill(ctx)
	qProc.exit(t, ctx, killOut, false, killErr)
	crashHold.open()

	callsBeforeQReopen := provider.calls.Load()
	queueOpts.FreshProfile = false
	qProc = startCP1Process(t, queueOpts)
	_ = qProc.open(t, ctx)
	crashOp, err := qProc.client.Operation(ctx, crashSend.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	crashRes, err := pi.DecodeConversationSendResult(crashOp)
	if err != nil || crashOp.State != "rejected" || crashOp.Execution == nil ||
		crashOp.Execution.State != "failed" || crashRes.Delivery != "non_dispatched" ||
		crashRes.Reason != "interrupted_before_dispatch" {
		t.Fatalf("crash pending send receipt mismatch: op=%+v decoded=%+v err=%v", crashOp, crashRes, err)
	}
	convAfterCrash, err := qProc.client.GetConversation(ctx, humanBot.CurrentConversationID)
	if err != nil || convAfterCrash.Draft == nil || convAfterCrash.Draft.Text != crashDraft.Text {
		t.Fatalf("crash recovery lost pending draft: %+v err=%v", convAfterCrash, err)
	}
	lostMandate, err := qProc.client.GetOrchestration(ctx, autoBot.BotID, "watch:"+crashOccW.WatchID)
	if err != nil || lostMandate.State != pi.ProductMandateUnknown ||
		len(lostMandate.RecentRuns) == 0 || lostMandate.RecentRuns[0].State != "unknown" {
		t.Fatalf("interrupted private dispatch not reconciled to unknown: %+v err=%v", lostMandate, err)
	}
	qProc.shutdown(t, ctx)
	if provider.calls.Load() != callsBeforeQReopen {
		t.Fatalf("queue crash recovery replayed %d provider calls", provider.calls.Load()-callsBeforeQReopen)
	}

	sourcesDir := filepath.Join(root, "sources")
	if err := os.MkdirAll(sourcesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcesOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(sourcesDir, "pi"),
		ProfilePath:     filepath.Join(sourcesDir, "contacts.json"),
		CredentialsPath: filepath.Join(sourcesDir, "connection.json"),
		FreshProfile:    true,
	}
	sProc := startCP1Process(t, sourcesOpts)
	_ = sProc.open(t, ctx)
	_ = bg3ConfigureProviders(t, ctx, sProc.client, provider.server.URL, providerKey, ws, 2)
	observer, err := sProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "bg3-s-observer", Profile: pi.ProductBotProfile{Name: "Observe sources", Purpose: "Sources fixture", Workspace: ws},
	})
	if err != nil {
		t.Fatal(err)
	}

	liveSessRaw := bg3ForegroundTool(t, ctx, sProc, provider, observer, "session_open", map[string]any{
		"command": "/usr/bin/printf", "args": []string{"fixture-output"}, "mode": "pipe",
	}, "bg3-s-open")
	var liveSess pi.ProductSessionSummary
	if err := json.Unmarshal([]byte(liveSessRaw), &liveSess); err != nil || liveSess.SessionID == "" {
		t.Fatalf("session_open raw=%s err=%v", liveSessRaw, err)
	}
	sessMandateID := "session:" + liveSess.SessionID
	provider.setMandatePlan(sessMandateID, func(bg3WireEvent) *bg3ToolPlan {
		return &bg3ToolPlan{Name: "conversation_notify", Args: map[string]any{"text": "Session watch finished", "urgency": "normal"}}
	})
	_ = bg3ForegroundTool(t, ctx, sProc, provider, observer, "session_watch", map[string]any{
		"action": "arm", "session_id": liveSess.SessionID, "generation": liveSess.Generation, "on_exit": true,
	}, "bg3-s-watch")
	sessDetail := bg3AwaitMandateSettled(t, ctx, sProc.client, observer.BotID, sessMandateID, 1)
	if sessDetail.LastDisposition != pi.ProductDispositionNotified {
		t.Fatalf("session watch mandate disposition=%+v", sessDetail)
	}

	if err := os.WriteFile(filepath.Join(ws, "state.txt"), []byte("A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	precheckW := bg3SaveWatch(t, ctx, sProc.client, pi.ProductWatchSaveParams{
		BotID: observer.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Sandboxed precheck",
		Schedule:    interval10,
		Precheck:    &pi.ProductWatchPrecheck{Kind: "executable", Executable: precheckBin, WorkspaceRead: true, TimeoutMS: intPtr(10000), EmitInitial: true},
		OperationID: "bg3-s-precheck",
	})
	setClock(bg2ParseISO(t, precheckW.NextDueAt))
	_ = bg3AwaitMandateSettled(t, ctx, sProc.client, observer.BotID, "watch:"+precheckW.WatchID, 1)
	var precheckSeen bool
	for _, r := range provider.requestList() {
		if r.Event != nil && r.Event.MandateID == "watch:"+precheckW.WatchID && len(r.Event.Facts) > 0 && r.Event.Facts[0].Kind == "schedule_precheck" {
			precheckSeen = true
		}
	}
	if !precheckSeen {
		t.Fatal("schedule_precheck fact missing from private wake")
	}
	if _, err := sProc.client.StopOrchestration(ctx, pi.ProductOrchestrationStopParams{
		BotID: observer.BotID, MandateID: "watch:" + precheckW.WatchID, OperationID: "bg3-s-stop-precheck",
	}); err != nil {
		t.Fatal(err)
	}

	sleepSessRaw := bg3ForegroundTool(t, ctx, sProc, provider, observer, "session_open", map[string]any{
		"command": "/bin/sh", "args": []string{"-c", "echo PID:$$; exec /bin/sleep 60"}, "mode": "pipe",
	}, "bg3-s-sleep-open")
	var sleepSess pi.ProductSessionSummary
	if err := json.Unmarshal([]byte(sleepSessRaw), &sleepSess); err != nil || sleepSess.SessionID == "" {
		t.Fatalf("sleep session_open raw=%s err=%v", sleepSessRaw, err)
	}
	pidRead := bg1WaitReadContains(t, ctx, sProc.client, observer.BotID, sleepSess.SessionID, sleepSess.Generation, nil, "PID:")
	sleepLine := strings.TrimSpace(bg1JoinedOutput(pidRead))
	sleepPID, err := strconv.Atoi(strings.TrimPrefix(sleepLine, "PID:"))
	if err != nil || sleepPID <= 0 || bg1ProcessGone(sleepPID) {
		t.Fatalf("invalid/missing live sleep PID from %q: err=%v", sleepLine, err)
	}
	_ = bg3ForegroundTool(t, ctx, sProc, provider, observer, "session_watch", map[string]any{
		"action": "arm", "session_id": sleepSess.SessionID, "generation": sleepSess.Generation, "on_exit": true,
	}, "bg3-s-sleep-watch")

	obsHist, err := sProc.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: observer.CurrentConversationID},
	})
	if err != nil || len(obsHist.Items) == 0 {
		t.Fatalf("expected observer history before crash, got %+v err=%v", obsHist, err)
	}
	var quotedTarget pi.ProductMessage
	for _, m := range obsHist.Items {
		if m.Role == "assistant" {
			quotedTarget = m
			break
		}
	}
	if quotedTarget.MessageID == "" {
		t.Fatalf("missing assistant reply in observer history: %+v", obsHist.Items)
	}

	unkMarker := "human execution interrupted by restart"
	unkGate := provider.armBarrier(unkMarker)
	_, unkSend := bg3SaveSend(t, ctx, sProc.client, observer.CurrentConversationID, unkMarker, nil, "bg3-s-unknown")
	_ = unkGate.await(t, ctx, "unknown foreground barrier")
	convObs, err := sProc.client.GetConversation(ctx, observer.CurrentConversationID)
	if err != nil || convObs.Draft == nil {
		t.Fatalf("GetConversation during unknown send = %+v err=%v", convObs, err)
	}
	savedUnsent, err := sProc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: observer.CurrentConversationID},
		ProductText: pi.ProductText{
			Text:      "draft with quote and material at restart",
			Materials: []pi.ProductMaterial{{ID: "mat-crash", Revision: 1, Name: "CrashNotes", Text: "Keep me"}},
		},
		ExpectedRevision: convObs.Draft.Revision,
		OperationID:      "bg3-s-unsent-quoted",
		Quote: &pi.ProductQuote{
			MessageID: quotedTarget.MessageID, BubbleIndex: intPtr(0), Sender: "bot", AuthorName: "Observe sources", Text: quotedTarget.Text,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	crashNoticeW := bg3SaveWatch(t, ctx, sProc.client, pi.ProductWatchSaveParams{
		BotID: observer.BotID, Kind: pi.ProductWatchTimer, Purpose: "Notice on unknown execution",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 1000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-s-crash-notice",
	})
	provider.setMandatePlan("watch:"+crashNoticeW.WatchID, func(bg3WireEvent) *bg3ToolPlan {
		return &bg3ToolPlan{Name: "conversation_notify", Args: map[string]any{"text": "Notice after unknown execution", "urgency": "high"}}
	})
	setClock(bg2ParseISO(t, crashNoticeW.NextDueAt))
	_ = bg3AwaitMandateSettled(t, ctx, sProc.client, observer.BotID, "watch:"+crashNoticeW.WatchID, 1)
	tlBeforeKill, err := sProc.client.ConversationTimeline(ctx, pi.ProductConversationTimelineParams{
		ConversationID: observer.CurrentConversationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range tlBeforeKill.Items {
		if it.Text == "Notice after unknown execution" {
			t.Fatal("anchored post_settlement notice visible before crash settlement")
		}
	}

	sKillOut, sKillErr := sProc.client.Kill(ctx)
	sProc.exit(t, ctx, sKillOut, false, sKillErr)
	unkGate.open()
	bg1WaitUntil(t, ctx, "sleep child reaped after SIGKILL", func() bool {
		return bg1ProcessGone(sleepPID)
	})

	callsBeforeSourcesReopen := provider.calls.Load()
	sourcesOpts.FreshProfile = false
	sProc = startCP1Process(t, sourcesOpts)
	_ = sProc.open(t, ctx)
	lostSess, err := sProc.client.GetSession(ctx, observer.BotID, sleepSess.SessionID)
	if err != nil || len(lostSess.Watches) == 0 || lostSess.Watches[0].State != "target_lost" {
		t.Fatalf("sleep session watch not target_lost after reopen: %+v err=%v", lostSess, err)
	}
	lostSleepMandate, err := sProc.client.GetOrchestration(ctx, observer.BotID, "session:"+sleepSess.SessionID)
	if err != nil || lostSleepMandate.Metrics.TotalWakes != 0 {
		t.Fatalf("target_lost woke orchestration mandate unexpectedly: %+v err=%v", lostSleepMandate, err)
	}
	tlAfterKill, err := sProc.client.ConversationTimeline(ctx, pi.ProductConversationTimelineParams{
		ConversationID: observer.CurrentConversationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var finalizedUnk *pi.ProductConversationTimelineItem
	for i := range tlAfterKill.Items {
		if tlAfterKill.Items[i].Text == "Notice after unknown execution" {
			finalizedUnk = &tlAfterKill.Items[i]
		}
	}
	if finalizedUnk == nil || finalizedUnk.Notice == nil ||
		finalizedUnk.Notice.AnchorExecutionID != unkSend.Execution.ExecutionID ||
		finalizedUnk.Notice.AnchorOutcome != "unknown" {
		t.Fatalf("unknown execution post_settlement notice mismatch: %+v", finalizedUnk)
	}
	convObsAfter, err := sProc.client.GetConversation(ctx, observer.CurrentConversationID)
	if err != nil || convObsAfter.Draft == nil || convObsAfter.Draft.Text != savedUnsent.Text ||
		len(convObsAfter.Draft.Materials) != 1 || convObsAfter.Draft.Quote == nil ||
		convObsAfter.Draft.Quote.MessageID != quotedTarget.MessageID {
		t.Fatalf("quoted+material draft lost across SIGKILL: %+v err=%v", convObsAfter, err)
	}
	unkOpAfter, err := sProc.client.Operation(ctx, unkSend.OperationID)
	if err != nil || unkOpAfter.Execution == nil || unkOpAfter.Execution.State != "unknown" {
		t.Fatalf("interrupted foreground execution state=%+v err=%v", unkOpAfter, err)
	}
	sProc.shutdown(t, ctx)
	if provider.calls.Load() != callsBeforeSourcesReopen {
		t.Fatalf("sources reopen / target_lost triggered %d provider calls", provider.calls.Load()-callsBeforeSourcesReopen)
	}

	migDir := filepath.Join(root, "mig-v13")
	if err := os.MkdirAll(migDir, 0o700); err != nil {
		t.Fatal(err)
	}
	migOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(migDir, "pi"),
		ProfilePath:     filepath.Join(migDir, "contacts.json"),
		CredentialsPath: filepath.Join(migDir, "connection.json"),
		FreshProfile:    true,
	}
	mProc := startCP1Process(t, migOpts)
	_ = mProc.open(t, ctx)
	mBot, err := mProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "bg3-mig-bot", Profile: pi.ProductBotProfile{Name: "Preserve v13 data", Workspace: ws},
	})
	if err != nil {
		t.Fatal(err)
	}
	mWatch := bg3SaveWatch(t, ctx, mProc.client, pi.ProductWatchSaveParams{
		BotID: mBot.BotID, Kind: pi.ProductWatchTimer, Purpose: "Preserve v13 watch",
		Timer:       &pi.ProductTimerSpec{FireAt: time.UnixMilli(epoch + 60000).UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg3-mig-watch",
	})
	mProc.shutdown(t, ctx)

	migDB := filepath.Join(migDir, "xuhu.db")
	downgradeV13 := `
CREATE TABLE bot_event_inbox_v13 (
  event_id TEXT PRIMARY KEY, owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED,
  target_conversation_id TEXT REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
  source_kind TEXT NOT NULL, source_id TEXT NOT NULL, occurrence_id TEXT NOT NULL UNIQUE, coalesce_key TEXT,
  coalesced_count INTEGER NOT NULL DEFAULT 1, state TEXT NOT NULL CHECK (state IN ('pending','claimed','consumed','unknown','cancelled')),
  batch_id TEXT REFERENCES bot_event_batches(batch_id) DEFERRABLE INITIALLY DEFERRED,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL, payload_json TEXT NOT NULL
) STRICT;
INSERT INTO bot_event_inbox_v13 SELECT event_id, owner_bot_id, target_conversation_id, source_kind, source_id, occurrence_id, coalesce_key, coalesced_count, state, batch_id, created_at, updated_at, payload_json FROM bot_event_inbox;
DROP TABLE bot_event_inbox;
ALTER TABLE bot_event_inbox_v13 RENAME TO bot_event_inbox;
CREATE INDEX bot_event_inbox_by_owner ON bot_event_inbox(owner_bot_id, state, created_at, event_id);
CREATE INDEX bot_event_inbox_by_batch ON bot_event_inbox(batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX bot_event_inbox_by_source ON bot_event_inbox(owner_bot_id, source_id, target_conversation_id, source_kind);
DROP TABLE orchestration_admissions;
DROP TABLE conversation_notices;
DROP TABLE conversation_timelines;
DROP TABLE orchestration_decisions;
DROP TABLE orchestration_runs;
DROP TABLE orchestration_checkpoints;
DROP TABLE orchestration_backoff;
DROP TABLE queued_conversation_sends;
DROP TABLE orchestration_mandates;
DELETE FROM meta WHERE key GLOB 'orchestration_operation:*';
ALTER TABLE activities DROP COLUMN origin_mandate_id;
PRAGMA user_version = 13;`
	runNodeSQLite(t, nodePath, migDB, fmt.Sprintf(`
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec(%q);
db.close();
`, downgradeV13))
	canonBeforeV13 := productRead(t, migOpts.ProfilePath)
	callsBeforeMigReopen := provider.calls.Load()
	migOpts.FreshProfile = false
	mProc = startCP1Process(t, migOpts)
	_ = mProc.open(t, ctx)
	gotMWatch, err := mProc.client.GetWatch(ctx, mBot.BotID, mWatch.WatchID)
	if err != nil || gotMWatch.WatchID != mWatch.WatchID {
		t.Fatalf("v13->v14 lost watch: %+v err=%v", gotMWatch, err)
	}
	mProc.shutdown(t, ctx)
	if provider.calls.Load() != callsBeforeMigReopen {
		t.Fatalf("v13->v14 triggered unexpected provider calls")
	}
	if !bytes.Equal(productRead(t, migOpts.ProfilePath), canonBeforeV13) {
		t.Fatal("v13->v14 modified contacts.json bytes")
	}
	v14Check := runNodeSQLite(t, nodePath, migDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.exec('PRAGMA user_version = 15');
db.close();
console.log(JSON.stringify({ v, fk }));
`)
	if v14Check != `{"v":14,"fk":0}` {
		t.Fatalf("v13->v14 schema check = %s", v14Check)
	}
	v15Client, startErr := pi.StartProduct(migOpts)
	if startErr == nil {
		if _, readyErr := v15Client.Ready(ctx); readyErr == nil {
			if _, openErr := v15Client.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
				t.Fatal("worker admitted user_version=15")
			}
		}
		_, _ = v15Client.Wait(ctx)
	}
	afterV15 := runNodeSQLite(t, nodePath, migDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const bots = db.prepare('SELECT count(*) AS n FROM bots').get().n;
db.close();
console.log(JSON.stringify({ v, bots }));
`)
	if afterV15 != `{"v":15,"bots":1}` {
		t.Fatalf("v15 refusal mutated DB: %s", afterV15)
	}

	provider.assertNoProblems()
}
