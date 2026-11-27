//go:build integration

package pi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

type piProcessEvents struct {
	mu     sync.Mutex
	values []pi.Event
	wake   chan struct{}
}

func (q *piProcessEvents) add(event pi.Event) {
	q.mu.Lock()
	q.values = append(q.values, event)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *piProcessEvents) wait(ctx context.Context, contextID, runID string, name pi.EventName) (pi.Event, error) {
	for {
		q.mu.Lock()
		for _, event := range q.values {
			if event.ContextID == contextID && event.RunID == runID && event.Name == name {
				q.mu.Unlock()
				return event, nil
			}
		}
		q.mu.Unlock()
		select {
		case <-q.wake:
		case <-ctx.Done():
			return pi.Event{}, fmt.Errorf("waiting for %s/%s/%s: %w", contextID, runID, name, ctx.Err())
		}
	}
}

func (q *piProcessEvents) await(t *testing.T, ctx context.Context, contextID, runID string, name pi.EventName) pi.Event {
	t.Helper()
	event, err := q.wait(ctx, contextID, runID, name)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

type piProcessHarness struct {
	client   *pi.Client
	ready    pi.Ready
	events   *piProcessEvents
	exits    chan pi.ProcessOutcome
	closed   bool
	contexts []string
}

func startPiProcess(t *testing.T, ctx context.Context, stateDir, endpoint string) *piProcessHarness {
	t.Helper()
	node, worker := os.Getenv("NAGI_TEST_NODE"), os.Getenv("NAGI_TEST_WORKER")
	for name, path := range map[string]string{"NAGI_TEST_NODE": node, "NAGI_TEST_WORKER": worker} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s must explicitly identify the pinned absolute input", name)
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s is not a regular input file: %v", name, err)
		}
	}
	h := &piProcessHarness{events: &piProcessEvents{wake: make(chan struct{}, 1)}, exits: make(chan pi.ProcessOutcome, 1)}
	client, err := pi.Start(pi.Options{
		NodePath: node, WorkerPath: worker, StateDir: stateDir,
		Handlers: pi.Handlers{OnEvent: h.events.add, OnExit: func(outcome pi.ProcessOutcome) { h.exits <- outcome }},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.client = client
	t.Cleanup(func() {
		if !h.closed {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for _, contextID := range h.contexts {
				inspect, err := h.client.Inspect(cleanup, contextID)
				if err == nil && inspect.ActiveRun != nil {
					runID := inspect.ActiveRun.RunID
					if _, err := h.client.CancelRun(cleanup, pi.CancelRunParams{ContextID: contextID, RunID: runID}); err == nil {
						if _, err := h.events.wait(cleanup, contextID, runID, pi.RunSettled); err != nil {
							t.Error(err)
						}
					}
				}
			}
			outcome, err := h.client.Shutdown(cleanup)
			if err != nil || !outcome.Reaped {
				t.Errorf("owned Pi cleanup outcome=%+v error=%v", outcome, err)
			}
		}
	})
	h.ready, err = client.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.ready.Engine.Version != "1.5.2" || h.ready.Runtime.Version != "v22.23.5" || h.ready.WorkerEpoch == "" {
		t.Fatalf("actual process identity=%+v", h.ready)
	}
	if _, err := client.Configure(ctx, pi.ConfigureParams{
		WireAPI: pi.OpenAICompletions, BaseURL: endpoint + "/v1", APIKey: "nagi-loopback-only",
		Model: "nagi-local-integration", ContextTokens: 65536, OutputTokens: 4096,
		CredentialSource: "qa-process-connection",
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *piProcessHarness) shutdown(t *testing.T, ctx context.Context) {
	t.Helper()
	outcome, err := h.client.Shutdown(ctx)
	if err != nil || !outcome.Reaped || outcome.PID <= 0 || outcome.ExitCode != 0 {
		t.Fatalf("actual Pi shutdown outcome=%+v error=%v", outcome, err)
	}
	h.closed = true
	select {
	case <-h.client.CallbacksDone():
	case <-ctx.Done():
		t.Fatalf("Pi callbacks did not drain after reap: %v", ctx.Err())
	}
	h.closed = true
	select {
	case <-h.client.CallbacksDone():
	case <-ctx.Done():
		t.Fatal("Pi callbacks did not finish after process reap")
	}
	select {
	case observed := <-h.exits:
		if observed.PID != outcome.PID || observed.ExitCode != outcome.ExitCode || !observed.Reaped {
			t.Fatalf("exit callback=%+v wait=%+v", observed, outcome)
		}
	default:
		t.Fatal("process reap did not produce its exit callback")
	}
	t.Logf("actual Pi process reaped pid=%d exit=%d epoch=%s", outcome.PID, outcome.ExitCode, h.ready.WorkerEpoch)
}

func piSettled(t *testing.T, ctx context.Context, h *piProcessHarness, contextID, runID string, want pi.RunStatus, text string) {
	t.Helper()
	event := h.events.await(t, ctx, contextID, runID, pi.RunSettled)
	var settled pi.RunSettledData
	if err := json.Unmarshal(event.Data, &settled); err != nil {
		t.Fatal(err)
	}
	if event.WorkerEpoch != h.ready.WorkerEpoch || settled.Status != want || settled.Text != text || settled.Error != nil {
		t.Fatalf("settlement envelope=%+v data=%+v", event, settled)
	}
}

func piRejected(t *testing.T, err error, code string) {
	t.Helper()
	var command *pi.CommandError
	var remote *pi.RemoteError
	if !errors.As(err, &command) || command.Delivery != pi.Rejected || !errors.As(err, &remote) || remote.Code != code {
		t.Fatalf("want explicit %s rejection, got %v", code, err)
	}
}

type piLocalProvider struct {
	mu       sync.Mutex
	requests int
	problems []string
	arrived  chan struct{}
	release  chan struct{}
	once     sync.Once
	unlock   sync.Once
}

func (p *piLocalProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

func (p *piLocalProvider) check(t *testing.T, want int) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.requests != want || len(p.problems) != 0 {
		t.Fatalf("provider requests=%d want=%d problems=%v", p.requests, want, p.problems)
	}
}

func (p *piLocalProvider) fail(w http.ResponseWriter, problem string) {
	p.mu.Lock()
	p.problems = append(p.problems, problem)
	p.mu.Unlock()
	http.Error(w, problem, http.StatusBadRequest)
}

func (p *piLocalProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.requests++
	p.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer nagi-loopback-only" {
		p.fail(w, "unexpected loopback route or authorization")
		return
	}
	var body struct {
		Stream   bool `json:"stream"`
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Stream || len(body.Messages) == 0 {
		p.fail(w, "expected streamed Chat Completions request with messages")
		return
	}
	var user string
	for _, message := range body.Messages {
		if message.Role == "user" {
			user = string(message.Content)
		}
	}
	last := body.Messages[len(body.Messages)-1]
	tool := func(name, id, args string) {
		found := false
		for _, offered := range body.Tools {
			found = found || offered.Function.Name == name
		}
		if !found {
			p.fail(w, "requested tool was not actually offered: "+name)
			return
		}
		piChatResponse(w, "", name, id, args)
	}
	switch {
	case strings.Contains(user, "CODING_READ_WRITE"):
		switch {
		case last.Role != "tool":
			tool("read", "call_read", `{"path":"input.txt"}`)
		case last.ToolCallID == "call_read" && strings.Contains(string(last.Content), "PI_READ_MARKER"):
			tool("write", "call_write", `{"path":"output.txt","content":"PI_WRITE_MARKER\n"}`)
		case last.ToolCallID == "call_write" && len(last.Content) != 0:
			piChatResponse(w, "CODING_OK", "", "", "")
		default:
			p.fail(w, "read/write public tool result did not match its call")
		}
	case strings.Contains(user, "ASK_A"):
		if last.Role == "tool" && last.ToolCallID == "call_answer" && strings.Contains(string(last.Content), "ANSWER_A") {
			piChatResponse(w, "ANSWER_A_OK", "", "", "")
		} else {
			tool("ask_user", "call_answer", `{"question":"Choose A"}`)
		}
	case strings.Contains(user, "CANCEL_A"):
		tool("ask_user", "call_cancel", `{"question":"Cancel only A"}`)
	case strings.Contains(user, "CHAT_B"):
		piChatResponse(w, "CHAT_B_OK", "", "", "")
	case strings.Contains(user, "HOLD_B"):
		p.once.Do(func() { close(p.arrived) })
		select {
		case <-p.release:
			piChatResponse(w, "B_SURVIVED", "", "", "")
		case <-r.Context().Done():
		}
	default:
		p.fail(w, "unexpected loopback prompt")
	}
}

func piChatResponse(w http.ResponseWriter, text, tool, id, args string) {
	w.Header().Set("Content-Type", "text/event-stream")
	delta := map[string]any{"role": "assistant"}
	finish := "stop"
	if tool != "" {
		delta["tool_calls"] = []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]string{"name": tool, "arguments": args}}}
		finish = "tool_calls"
	} else {
		delta["content"] = text
	}
	for _, choice := range []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}, {"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
		chunk, _ := json.Marshal(map[string]any{"id": "local-response", "object": "chat.completion.chunk", "created": 1, "model": "nagi-local-integration", "choices": []any{choice}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func piPublicHistory(t *testing.T, ctx context.Context, client *pi.Client, contextID string) map[string]json.RawMessage {
	t.Helper()
	entries := map[string]json.RawMessage{}
	cursors := map[string]bool{}
	cursor := ""
	for {
		page, err := client.History(ctx, pi.HistoryParams{ContextID: contextID, Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range page.Entries {
			var header struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &header); err != nil || header.ID == "" || entries[header.ID] != nil {
				t.Fatalf("invalid or repeated public history entry: %s", raw)
			}
			entries[header.ID] = raw
		}
		if page.NextCursor == "" {
			return entries
		}
		if cursors[page.NextCursor] {
			t.Fatal("public history cursor did not advance")
		}
		cursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
}

func TestPiClientProcessCodingHistoryReopen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	provider := &piLocalProvider{}
	server := httptest.NewServer(provider)
	defer server.Close()
	state := t.TempDir()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "input.txt"), []byte("PI_READ_MARKER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "project-alias")
	if err := os.Symlink(canonical, alias); err != nil {
		t.Fatal(err)
	}
	h := startPiProcess(t, ctx, state, server.URL)
	provider.check(t, 0)
	params := pi.OpenContextParams{ContextID: "coding", BotID: "bot-coding", Purpose: pi.Conversation, SelectionRevision: 1, CWD: alias, CodingTools: []string{"read", "write"}}
	binding, err := h.client.OpenContext(ctx, params)
	if err != nil || binding.CWD != canonical {
		t.Fatalf("open canonical project binding=%+v error=%v", binding, err)
	}
	h.contexts = append(h.contexts, params.ContextID)
	if _, err := h.client.StartRun(ctx, pi.StartRunParams{ContextID: "coding", RunID: "coding-1", Text: "CODING_READ_WRITE", Correlation: &pi.RunCorrelation{TurnID: "coding-turn"}}); err != nil {
		t.Fatal(err)
	}
	piSettled(t, ctx, h, "coding", "coding-1", pi.Completed, "CODING_OK")
	written, err := os.ReadFile(filepath.Join(project, "output.txt"))
	if err != nil || string(written) != "PI_WRITE_MARKER\n" {
		t.Fatalf("actual coding write=%q error=%v", written, err)
	}
	if _, err := os.Stat(filepath.Join(state, "output.txt")); !os.IsNotExist(err) {
		t.Fatalf("coding output escaped project into worker state: %v", err)
	}
	for _, name := range []string{"read", "write"} {
		h.events.mu.Lock()
		found := false
		for _, event := range h.events.values {
			var finished pi.ToolFinishedData
			if event.ContextID == "coding" && event.RunID == "coding-1" && event.Name == pi.ToolFinished && json.Unmarshal(event.Data, &finished) == nil && finished.Name == name && !finished.IsError {
				found = true
			}
		}
		h.events.mu.Unlock()
		if !found {
			t.Fatalf("actual %s tool completion absent", name)
		}
	}
	used, err := h.client.OpenContext(ctx, params)
	if err != nil || !used.Used || used.SessionRef != binding.SessionRef {
		t.Fatalf("SDK-used binding after same open=%+v error=%v", used, err)
	}
	before := piPublicHistory(t, ctx, h.client, "coding")
	publicEntries, err := json.Marshal(before)
	if err != nil || len(before) < 3 || !strings.Contains(string(publicEntries), "PI_READ_MARKER") || !strings.Contains(string(publicEntries), "PI_WRITE_MARKER") || !strings.Contains(string(publicEntries), "CODING_OK") {
		t.Fatalf("public history lacks actual coding records: entries=%d error=%v", len(before), err)
	}
	inspect, err := h.client.Inspect(ctx, "coding")
	if err != nil || !inspect.Complete || inspect.ActiveRun != nil || !strings.Contains(string(inspect.ModelContext), "PI_READ_MARKER") || !strings.Contains(string(inspect.ModelContext), "CODING_OK") {
		t.Fatalf("public context inspect=%+v error=%v", inspect, err)
	}
	provider.check(t, 3)
	if _, err := h.client.CloseContext(ctx, "coding"); err != nil {
		t.Fatal(err)
	}
	h.shutdown(t, ctx)
	h2 := startPiProcess(t, ctx, state, server.URL)
	params.ResumeRef, params.Used = binding.SessionRef, true
	reopened, err := h2.client.OpenContext(ctx, params)
	if err != nil || !reopened.Used || reopened.SessionRef != binding.SessionRef || h2.ready.WorkerEpoch == h.ready.WorkerEpoch {
		t.Fatalf("new process reopen=%+v error=%v", reopened, err)
	}
	h2.contexts = append(h2.contexts, params.ContextID)
	after := piPublicHistory(t, ctx, h2.client, "coding")
	for id, raw := range before {
		if string(after[id]) != string(raw) {
			t.Fatalf("reopen changed or lost public history entry %s", id)
		}
	}
	inspect, err = h2.client.Inspect(ctx, "coding")
	if err != nil || !inspect.Complete || !strings.Contains(string(inspect.ModelContext), "CODING_OK") || inspect.ActiveRun != nil {
		t.Fatalf("reopened public inspect=%+v error=%v", inspect, err)
	}
	provider.check(t, 3)
	h2.shutdown(t, ctx)
	canonicalState, err := filepath.EvalSymlinks(state)
	if err != nil || filepath.Dir(binding.SessionRef) != filepath.Join(canonicalState, "sessions") {
		t.Fatalf("fault target is not an owned session reference: error=%v", err)
	}
	if err := os.Truncate(binding.SessionRef, 0); err != nil {
		t.Fatal(err)
	}
	emptyBefore, err := os.Stat(binding.SessionRef)
	if err != nil || emptyBefore.Size() != 0 {
		t.Fatalf("owned empty session preparation: %v", err)
	}
	h3 := startPiProcess(t, ctx, state, server.URL)
	_, err = h3.client.OpenContext(ctx, params)
	piRejected(t, err, "history_unavailable")
	emptyAfter, err := os.Stat(binding.SessionRef)
	if err != nil || emptyAfter.Size() != 0 || !os.SameFile(emptyBefore, emptyAfter) || !emptyBefore.ModTime().Equal(emptyAfter.ModTime()) {
		t.Fatalf("used empty session was mutated during rejection: %v", err)
	}
	provider.check(t, 3)
	h3.shutdown(t, ctx)
	t.Logf("actual coding/history/reopen: requests=%d entries_before=%d entries_after=%d", provider.count(), len(before), len(after))
}

func TestPiClientProcessIndependentToolAnswerAndCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	provider := &piLocalProvider{arrived: make(chan struct{}), release: make(chan struct{})}
	server := httptest.NewServer(provider)
	defer func() {
		provider.unlock.Do(func() { close(provider.release) })
		server.Close()
	}()
	h := startPiProcess(t, ctx, t.TempDir(), server.URL)
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := pi.DomainTool{Name: "ask_user", Description: "Hold this integration call until its exact answer or cancellation.", Parameters: json.RawMessage(`{"type":"object","properties":{"question":{"type":"string"}},"required":["question"],"additionalProperties":false}`)}
	for _, params := range []pi.OpenContextParams{
		{ContextID: "activity", BotID: "same-bot", Purpose: pi.Activity, ActivityID: "activity-a", SelectionRevision: 1, CWD: project, CodingTools: []string{}, DomainTools: []pi.DomainTool{tool}},
		{ContextID: "chat", BotID: "same-bot", Purpose: pi.Conversation, SelectionRevision: 1, CWD: project, CodingTools: []string{}},
	} {
		if _, err := h.client.OpenContext(ctx, params); err != nil {
			t.Fatal(err)
		}
		h.contexts = append(h.contexts, params.ContextID)
	}
	provider.check(t, 0)
	if _, err := h.client.StartRun(ctx, pi.StartRunParams{ContextID: "activity", RunID: "a-answer", Text: "ASK_A", Correlation: &pi.RunCorrelation{ExecutionID: "execution-a", ScopeRef: "scope-a"}}); err != nil {
		t.Fatal(err)
	}
	request := h.events.await(t, ctx, "activity", "a-answer", pi.ToolRequest)
	started := h.events.await(t, ctx, "activity", "a-answer", pi.RunStarted)
	var admitted struct {
		Correlation pi.RunCorrelation `json:"correlation"`
	}
	if err := json.Unmarshal(started.Data, &admitted); err != nil || admitted.Correlation.ExecutionID != "execution-a" || admitted.Correlation.ScopeRef != "scope-a" {
		t.Fatalf("actual admitted correlation=%+v error=%v", admitted, err)
	}
	var asked pi.ToolRequestData
	if err := json.Unmarshal(request.Data, &asked); err != nil || asked.Name != "ask_user" || asked.CallID != "call_answer" {
		t.Fatalf("held callback=%+v error=%v", asked, err)
	}
	if _, err := h.client.StartRun(ctx, pi.StartRunParams{ContextID: "activity", RunID: "must-not-queue", Text: "ASK_A"}); err == nil {
		t.Fatal("busy activity accepted another run")
	} else {
		piRejected(t, err, "busy")
	}
	if _, err := h.client.StartRun(ctx, pi.StartRunParams{ContextID: "chat", RunID: "b-chat", Text: "CHAT_B"}); err != nil {
		t.Fatal(err)
	}
	piSettled(t, ctx, h, "chat", "b-chat", pi.Completed, "CHAT_B_OK")
	provider.check(t, 2)
	busyCtx, busyCancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = h.client.Shutdown(busyCtx)
	busyCancel()
	piRejected(t, err, "busy")
	inspect, err := h.client.Inspect(ctx, "activity")
	if err != nil || inspect.ActiveRun == nil || inspect.ActiveRun.RunID != "a-answer" {
		t.Fatalf("busy shutdown aborted held activity: inspect=%+v error=%v", inspect, err)
	}
	answer := pi.ToolResultParams{WorkerEpoch: request.WorkerEpoch, ContextID: "chat", RunID: "b-chat", CallID: asked.CallID, Content: []pi.ContentBlock{{Type: "text", Text: "ANSWER_A"}}}
	_, err = h.client.ToolResult(ctx, answer)
	piRejected(t, err, "not_found")
	answer.ContextID, answer.RunID = "activity", "a-answer"
	result, err := h.client.ToolResult(ctx, answer)
	if err != nil || !result.Resolved {
		t.Fatalf("exact held answer result=%+v error=%v", result, err)
	}
	piSettled(t, ctx, h, "activity", "a-answer", pi.Completed, "ANSWER_A_OK")
	_, err = h.client.ToolResult(ctx, answer)
	piRejected(t, err, "not_found")
	provider.check(t, 3)
	if _, err := h.client.StartRun(ctx, pi.StartRunParams{ContextID: "activity", RunID: "a-cancel", Text: "CANCEL_A"}); err != nil {
		t.Fatal(err)
	}
	request = h.events.await(t, ctx, "activity", "a-cancel", pi.ToolRequest)
	if err := json.Unmarshal(request.Data, &asked); err != nil || asked.CallID != "call_cancel" {
		t.Fatalf("cancellation callback=%+v error=%v", asked, err)
	}
	if _, err := h.client.StartRun(ctx, pi.StartRunParams{ContextID: "chat", RunID: "b-survives", Text: "HOLD_B"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.arrived:
	case <-ctx.Done():
		t.Fatal("second context request did not reach loopback")
	}
	cancelled, err := h.client.CancelRun(ctx, pi.CancelRunParams{ContextID: "activity", RunID: "a-cancel"})
	if err != nil || !cancelled.CancelRequested {
		t.Fatalf("targeted cancellation=%+v error=%v", cancelled, err)
	}
	piSettled(t, ctx, h, "activity", "a-cancel", pi.Interrupted, "")
	inspect, err = h.client.Inspect(ctx, "chat")
	if err != nil || inspect.ActiveRun == nil || inspect.ActiveRun.RunID != "b-survives" || inspect.ActiveRun.CancelRequested {
		t.Fatalf("cancel A affected B: inspect=%+v error=%v", inspect, err)
	}
	answer.RunID, answer.CallID = "a-cancel", asked.CallID
	_, err = h.client.ToolResult(ctx, answer)
	piRejected(t, err, "not_found")
	provider.unlock.Do(func() { close(provider.release) })
	piSettled(t, ctx, h, "chat", "b-survives", pi.Completed, "B_SURVIVED")
	provider.check(t, 5)
	h.shutdown(t, ctx)
	t.Logf("actual held-tool/independent-chat/answer/cancel: requests=%d", provider.count())
}

type piConfigurationRequest struct {
	URL          string
	Model        string
	TokenField   string
	OutputTokens int
	AuthMatches  bool
}

type piConfigurationProvider struct {
	mu       sync.Mutex
	key      string
	requests []piConfigurationRequest
}

func (p *piConfigurationProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model               string `json:"model"`
		Stream              bool   `json:"stream"`
		MaxTokens           *int   `json:"max_tokens"`
		MaxCompletionTokens *int   `json:"max_completion_tokens"`
		Messages            []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Method != http.MethodPost || !body.Stream || len(body.Messages) == 0 {
		http.Error(w, "invalid loopback Chat Completions request", http.StatusBadRequest)
		return
	}
	request := piConfigurationRequest{URL: "http://" + r.Host + r.URL.Path, Model: body.Model, AuthMatches: r.Header.Get("Authorization") == "Bearer "+p.key}
	switch {
	case body.MaxTokens != nil && body.MaxCompletionTokens == nil:
		request.TokenField, request.OutputTokens = "max_tokens", *body.MaxTokens
	case body.MaxCompletionTokens != nil && body.MaxTokens == nil:
		request.TokenField, request.OutputTokens = "max_completion_tokens", *body.MaxCompletionTokens
	default:
		http.Error(w, "missing or ambiguous output limit", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	if !request.AuthMatches {
		http.Error(w, "wrong synthetic credential snapshot", http.StatusUnauthorized)
		return
	}
	delta := map[string]any{"role": "assistant", "content": body.Model + "_DONE"}
	finish := "stop"
	last := body.Messages[len(body.Messages)-1]
	var user string
	for _, message := range body.Messages {
		if message.Role == "user" {
			user = string(message.Content)
		}
	}
	if strings.Contains(user, "CAPTURED_A") && last.Role != "tool" {
		delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": 0, "id": "captured-a-call", "type": "function",
			"function": map[string]string{"name": "ask_user", "arguments": `{"question":"Preserve connection A"}`},
		}}}
		finish = "tool_calls"
	} else if strings.Contains(user, "CAPTURED_A") && (last.ToolCallID != "captured-a-call" || !strings.Contains(string(last.Content), "CONFIG_ANSWER_A")) {
		http.Error(w, "answer did not match captured A call", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, choice := range []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}, {"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
		chunk, _ := json.Marshal(map[string]any{"id": "local-config-response", "object": "chat.completion.chunk", "created": 1, "model": body.Model, "choices": []any{choice}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (p *piConfigurationProvider) check(t *testing.T, endpoint string, models []string, limits []int) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != len(models) || len(models) != len(limits) {
		t.Fatalf("observed configuration requests=%+v expected_models=%v", p.requests, models)
	}
	for i, request := range p.requests {
		if request.URL != endpoint+"/chat/completions" || request.Model != models[i] || request.OutputTokens != limits[i] || !request.AuthMatches {
			t.Fatalf("actual configured request[%d]=%+v", i, request)
		}
	}
	t.Logf("observed loopback configuration requests=%+v", p.requests)
}

func piConfigurationReadback(t *testing.T, got pi.ContextConfiguration, id string, want pi.ConfigureParams, tools []string, secrets ...string) {
	t.Helper()
	if got.ContextID != id || got.WireAPI != want.WireAPI || got.BaseURL != want.BaseURL || got.Model != want.Model || got.ContextTokens != want.ContextTokens || got.OutputTokens != want.OutputTokens || got.CredentialSource != want.CredentialSource || !got.CredentialPresent || len(got.ActiveTools) != len(tools) {
		t.Fatalf("declared nonsecret configuration=%+v", got)
	}
	for i := range tools {
		if got.ActiveTools[i] != tools[i] {
			t.Fatalf("context tools changed: %v", got.ActiveTools)
		}
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(raw), secret) {
			t.Fatal("synthetic credential leaked through a declared readback value")
		}
	}
	t.Logf("declared nonsecret configuration=%s", raw)
}

func TestPiClientProcessCapturedContextConfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	aProvider := &piConfigurationProvider{key: "synthetic-context-key-a"}
	bProvider := &piConfigurationProvider{key: "synthetic-context-key-b"}
	aServer, bServer := httptest.NewServer(aProvider), httptest.NewServer(bProvider)
	defer aServer.Close()
	defer bServer.Close()
	state := t.TempDir()
	h := startPiProcess(t, ctx, state, aServer.URL)
	configA := pi.ConfigureParams{WireAPI: pi.OpenAICompletions, BaseURL: aServer.URL + "/a/v1", APIKey: aProvider.key, CredentialSource: "qa-source-a", Model: "captured-model-a", ContextTokens: 65536, OutputTokens: 1024}
	configB := pi.ConfigureParams{WireAPI: pi.OpenAICompletions, BaseURL: bServer.URL + "/b/v1", APIKey: bProvider.key, CredentialSource: "qa-source-b", Model: "captured-model-b", ContextTokens: 49152, OutputTokens: 768}
	if _, err := h.client.Configure(ctx, configA); err != nil {
		t.Fatal(err)
	}
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := pi.DomainTool{Name: "ask_user", Description: "Hold the exact configuration test call.", Parameters: json.RawMessage(`{"type":"object","properties":{"question":{"type":"string"}},"required":["question"],"additionalProperties":false}`)}
	paramsA := pi.OpenContextParams{ContextID: "config-a", BotID: "config-bot", Purpose: pi.Activity, ActivityID: "config-activity-a", SelectionRevision: 1, CWD: project, CodingTools: []string{}, DomainTools: []pi.DomainTool{tool}}
	bindingA, err := h.client.OpenContext(ctx, paramsA)
	if err != nil {
		t.Fatal(err)
	}
	h.contexts = append(h.contexts, paramsA.ContextID)
	if _, err := h.client.StartRun(ctx, pi.StartRunParams{ContextID: "config-a", RunID: "captured-a", Text: "CAPTURED_A"}); err != nil {
		t.Fatal(err)
	}
	request := h.events.await(t, ctx, "config-a", "captured-a", pi.ToolRequest)
	var asked pi.ToolRequestData
	if err := json.Unmarshal(request.Data, &asked); err != nil || asked.CallID != "captured-a-call" {
		t.Fatalf("captured held call=%+v error=%v", asked, err)
	}
	if _, err := h.client.Configure(ctx, configB); err != nil {
		t.Fatal(err)
	}
	readA, err := h.client.ContextConfiguration(ctx, "config-a")
	if err != nil {
		t.Fatal(err)
	}
	piConfigurationReadback(t, readA, "config-a", configA, []string{"ask_user"}, aProvider.key, bProvider.key)
	paramsB := pi.OpenContextParams{ContextID: "config-b", BotID: "config-bot", Purpose: pi.Conversation, SelectionRevision: 1, CWD: project, CodingTools: []string{}}
	if _, err := h.client.OpenContext(ctx, paramsB); err != nil {
		t.Fatal(err)
	}
	h.contexts = append(h.contexts, paramsB.ContextID)
	readB, err := h.client.ContextConfiguration(ctx, "config-b")
	if err != nil {
		t.Fatal(err)
	}
	piConfigurationReadback(t, readB, "config-b", configB, nil, aProvider.key, bProvider.key)
	if _, err := h.client.StartRun(ctx, pi.StartRunParams{ContextID: "config-b", RunID: "captured-b1", Text: "CONFIG_B1"}); err != nil {
		t.Fatal(err)
	}
	piSettled(t, ctx, h, "config-b", "captured-b1", pi.Completed, configB.Model+"_DONE")
	updatedB := configB
	updatedB.Model, updatedB.ContextTokens, updatedB.OutputTokens = "captured-model-b-updated", 57344, 1536
	readB, err = h.client.ConfigureContext(ctx, pi.ConfigureContextParams{ContextID: "config-b", Settings: pi.ContextSettings{Model: updatedB.Model, ContextTokens: updatedB.ContextTokens, OutputTokens: updatedB.OutputTokens}})
	if err != nil {
		t.Fatal(err)
	}
	piConfigurationReadback(t, readB, "config-b", updatedB, nil, aProvider.key, bProvider.key)
	readA, err = h.client.ContextConfiguration(ctx, "config-a")
	if err != nil {
		t.Fatal(err)
	}
	piConfigurationReadback(t, readA, "config-a", configA, []string{"ask_user"}, aProvider.key, bProvider.key)
	held, err := h.client.Inspect(ctx, "config-a")
	if err != nil || held.ActiveRun == nil || held.ActiveRun.RunID != "captured-a" {
		t.Fatalf("idle B update changed held A: active=%+v error=%v", held.ActiveRun, err)
	}
	if _, err := h.client.StartRun(ctx, pi.StartRunParams{ContextID: "config-b", RunID: "captured-b2", Text: "CONFIG_B2"}); err != nil {
		t.Fatal(err)
	}
	piSettled(t, ctx, h, "config-b", "captured-b2", pi.Completed, updatedB.Model+"_DONE")
	if result, err := h.client.ToolResult(ctx, pi.ToolResultParams{WorkerEpoch: request.WorkerEpoch, ContextID: request.ContextID, RunID: request.RunID, CallID: asked.CallID, Content: []pi.ContentBlock{{Type: "text", Text: "CONFIG_ANSWER_A"}}}); err != nil || !result.Resolved {
		t.Fatalf("captured A answer result=%+v error=%v", result, err)
	}
	piSettled(t, ctx, h, "config-a", "captured-a", pi.Completed, configA.Model+"_DONE")
	aProvider.check(t, configA.BaseURL, []string{configA.Model, configA.Model}, []int{configA.OutputTokens, configA.OutputTokens})
	bProvider.check(t, configB.BaseURL, []string{configB.Model, updatedB.Model}, []int{configB.OutputTokens, updatedB.OutputTokens})
	h.shutdown(t, ctx)
	h2 := startPiProcess(t, ctx, state, aServer.URL)
	if _, err := h2.client.Configure(ctx, configB); err != nil {
		t.Fatal(err)
	}
	paramsA.ResumeRef, paramsA.Used = bindingA.SessionRef, true
	_, err = h2.client.OpenContext(ctx, paramsA)
	piRejected(t, err, "binding_conflict")
	rotatedA := configA
	rotatedA.CredentialSource, rotatedA.APIKey = "qa-source-a-rotated", "synthetic-context-key-a-rotated"
	paramsA.Configuration = &rotatedA
	_, err = h2.client.OpenContext(ctx, paramsA)
	piRejected(t, err, "binding_conflict")
	paramsA.Configuration = &configA
	reopened, err := h2.client.OpenContext(ctx, paramsA)
	if err != nil || reopened.SessionRef != bindingA.SessionRef || !reopened.Used {
		t.Fatalf("captured A reopen=%+v error=%v", reopened, err)
	}
	h2.contexts = append(h2.contexts, paramsA.ContextID)
	readA, err = h2.client.ContextConfiguration(ctx, "config-a")
	if err != nil {
		t.Fatal(err)
	}
	piConfigurationReadback(t, readA, "config-a", configA, []string{"ask_user"}, aProvider.key, bProvider.key, rotatedA.APIKey)
	restored, err := h2.client.Inspect(ctx, "config-a")
	if err != nil || restored.ActiveRun != nil || !strings.Contains(string(restored.ModelContext), configA.Model+"_DONE") {
		t.Fatalf("captured A public reopen inspect error=%v active=%+v", err, restored.ActiveRun)
	}
	aProvider.check(t, configA.BaseURL, []string{configA.Model, configA.Model}, []int{configA.OutputTokens, configA.OutputTokens})
	bProvider.check(t, configB.BaseURL, []string{configB.Model, updatedB.Model}, []int{configB.OutputTokens, updatedB.OutputTokens})
	h2.shutdown(t, ctx)
}

type piMCPReceipt struct {
	Transport string `json:"transport"`
	Value     string `json:"value"`
	ReceiptID string `json:"receiptId"`
	PID       int    `json:"pid,omitempty"`
}

func piMCPContentReceipt(t *testing.T, content []pi.ContentBlock) piMCPReceipt {
	t.Helper()
	var receipt piMCPReceipt
	if len(content) != 1 || content[0].Type != "text" || json.Unmarshal([]byte(content[0].Text), &receipt) != nil || receipt.ReceiptID == "" {
		t.Fatalf("MCP result lacks a fixture receipt: %+v", content)
	}
	return receipt
}

func piAwaitMCPProcess(t *testing.T, ctx context.Context, h *piProcessHarness, contextID, sourceID, kind string, after uint64) (pi.Event, pi.MCPProcessEvent) {
	t.Helper()
	for {
		h.events.mu.Lock()
		values := append([]pi.Event(nil), h.events.values...)
		h.events.mu.Unlock()
		for _, event := range values {
			if event.Name != pi.MCPProcess || event.ContextID != contextID || event.Seq <= after {
				continue
			}
			var data pi.MCPProcessEvent
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.SourceID == sourceID && data.Kind == kind {
				if event.WorkerEpoch != h.ready.WorkerEpoch || event.RunID != "" || data.AttemptID == "" {
					t.Fatalf("MCP envelope=%+v data=%+v", event, data)
				}
				return event, data
			}
		}
		select {
		case <-h.events.wake:
		case <-ctx.Done():
			t.Fatalf("waiting for MCP %s/%s/%s after %d: %v", contextID, sourceID, kind, after, ctx.Err())
		}
	}
}

func TestPiClientProcessMCPToolsLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixturePath, stdioLog := filepath.Join(project, "mcp-server.mjs"), filepath.Join(project, "stdio-rpc.jsonl")
	fixture := `import { createInterface } from 'node:readline';
import { appendFileSync } from 'node:fs';
const record = value => appendFileSync(process.argv[2], JSON.stringify({ pid: process.pid, snapshot: process.env.NAGI_MCP_SNAPSHOT, ...value }) + '\n');
const reply = (id, result) => {
  record({ kind: 'response', id, result });
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id, result }) + '\n');
};
record({ kind: 'started', cwd: process.cwd() });
const input = createInterface({ input: process.stdin, crlfDelay: Infinity });
input.on('line', line => {
  const request = JSON.parse(line);
  record({ kind: 'request', ...request });
  if (request.method === 'initialize') {
    reply(request.id, { protocolVersion: request.params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: 'nagi-stdio-fixture', version: '1' } });
  } else if (request.method === 'tools/list') {
    reply(request.id, { tools: [{ name: 'echo', description: 'Return a correlated fixture receipt.', inputSchema: { type: 'object', properties: { value: { type: 'string' } }, required: ['value'], additionalProperties: false } }] });
  } else if (request.method === 'tools/call') {
    const receipt = { transport: 'stdio', value: request.params.arguments.value, receiptId: 'stdio:' + request.params.arguments.value, pid: process.pid };
    reply(request.id, { content: [{ type: 'text', text: JSON.stringify(receipt) }], structuredContent: receipt, isError: false });
  } else if (request.id !== undefined) {
    process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Unexpected fixture method' } }) + '\n');
  }
});
input.on('close', () => record({ kind: 'stdin_closed' }));
process.on('exit', code => record({ kind: 'exit', code }));
`
	if err := os.WriteFile(fixturePath, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if data, err := os.ReadFile(stdioLog); err == nil {
			t.Logf("actual stdio MCP RPC JSONL:\n%s", data)
		}
	})
	type scene struct{ tool, call, value, transport string }
	scenes := map[string]scene{
		"STDIO_MCP":        {"mcp__stdio_fixture__echo", "stdio-call", "STDIO_REQUEST", "stdio"},
		"STDIO_REOPEN_MCP": {"mcp__stdio_fixture__echo", "stdio-reopen-call", "STDIO_REOPEN_REQUEST", "stdio"},
		"HTTP_MCP":         {"mcp__http_fixture__echo", "http-call", "HTTP_REQUEST", "http"},
		"HTTP_REOPEN_MCP":  {"mcp__http_fixture__echo", "http-reopen-call", "HTTP_REOPEN_REQUEST", "http"},
		"PLAIN_MCP":        {"mcp__plain_fixture__echo", "plain-call", "PLAIN_REQUEST", "http"},
	}
	var providerMu sync.Mutex
	counts := map[string]int{}
	wireReceipts := map[string]piMCPReceipt{}
	var providerProblems []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerMu.Lock()
		defer providerMu.Unlock()
		fail := func(problem string) {
			providerProblems = append(providerProblems, problem)
			http.Error(w, problem, 400)
		}
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer nagi-loopback-only" {
			fail("unexpected model route/auth")
			return
		}
		var body struct {
			Stream   bool `json:"stream"`
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
		if json.NewDecoder(r.Body).Decode(&body) != nil || !body.Stream || len(body.Messages) == 0 {
			fail("invalid model fixture request")
			return
		}
		var user, label string
		for _, message := range body.Messages {
			if message.Role == "user" {
				user = string(message.Content)
			}
		}
		for candidate := range scenes {
			if strings.Contains(user, candidate) {
				label = candidate
			}
		}
		current, exists := scenes[label]
		if !exists || len(body.Tools) != 1 || body.Tools[0].Function.Name != current.tool {
			fail("MCP missing or coding/domain tool unexpectedly exposed: " + user)
			return
		}
		counts[label]++
		last := body.Messages[len(body.Messages)-1]
		if last.Role != "tool" {
			args, _ := json.Marshal(map[string]string{"value": current.value})
			piChatResponse(w, "", current.tool, current.call, string(args))
			return
		}
		var text string
		var receipt piMCPReceipt
		if last.ToolCallID != current.call || json.Unmarshal(last.Content, &text) != nil || json.Unmarshal([]byte(text), &receipt) != nil || receipt.Transport != current.transport || receipt.Value != current.value || receipt.ReceiptID != current.transport+":"+current.value {
			fail("model wire lacks correlated MCP receipt")
			return
		}
		wireReceipts[label] = receipt
		piChatResponse(w, label+"_DONE", "", "", "")
	}))
	defer provider.Close()
	checkRequests := func(want int) {
		t.Helper()
		providerMu.Lock()
		defer providerMu.Unlock()
		total := 0
		for _, count := range counts {
			total += count
		}
		if total != want || len(providerProblems) != 0 {
			t.Fatalf("model requests=%v want_total=%d problems=%v", counts, want, providerProblems)
		}
	}
	var httpMu sync.Mutex
	httpCounts := map[string]map[string]int{"/static": {}, "/plain": {}}
	sessions := map[string]string{}
	var httpProblems []string
	replacementRequests := 0
	mcpHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpMu.Lock()
		defer httpMu.Unlock()
		fail := func(problem string) { httpProblems = append(httpProblems, problem); http.Error(w, problem, 400) }
		ledger, exists := httpCounts[r.URL.Path]
		if !exists {
			replacementRequests++
			fail("unexpected replacement MCP URL")
			return
		}
		wantAuth := ""
		if r.URL.Path == "/static" {
			wantAuth = "Bearer mcp-static-only"
		}
		if r.Header.Get("Authorization") != wantAuth {
			fail("implicit OAuth or lost explicit static header")
			return
		}
		if r.Method == "GET" || r.Method == "DELETE" {
			if r.Header.Get("Mcp-Session-Id") != sessions[r.URL.Path] || r.Header.Get("MCP-Protocol-Version") == "" {
				fail("HTTP session/protocol correlation missing")
				return
			}
			ledger[r.Method]++
			if r.Method == "GET" {
				w.WriteHeader(http.StatusMethodNotAllowed)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
			return
		}
		if r.Method != "POST" {
			fail("unexpected HTTP verb")
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
				Name            string `json:"name"`
				Arguments       struct {
					Value string `json:"value"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			fail("invalid MCP HTTP request")
			return
		}
		if request.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != sessions[r.URL.Path] {
			fail("MCP HTTP request lost session")
			return
		}
		ledger[request.Method]++
		var result any
		switch request.Method {
		case "initialize":
			if request.Params.ProtocolVersion == "" || len(request.ID) == 0 {
				fail("initialize missing version/id")
				return
			}
			sessions[r.URL.Path] = fmt.Sprintf("nagi-%s-%d", strings.TrimPrefix(r.URL.Path, "/"), ledger[request.Method])
			w.Header().Set("Mcp-Session-Id", sessions[r.URL.Path])
			result = map[string]any{"protocolVersion": request.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "nagi-http-fixture", "version": "1"}}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "Return a correlated receipt.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]string{"type": "string"}}, "required": []string{"value"}, "additionalProperties": false}}}}
		case "tools/call":
			if request.Params.Name != "echo" || len(request.ID) == 0 {
				fail("tools/call lost name/id")
				return
			}
			receipt := piMCPReceipt{Transport: "http", Value: request.Params.Arguments.Value, ReceiptID: "http:" + request.Params.Arguments.Value}
			text, _ := json.Marshal(receipt)
			result = map[string]any{"content": []pi.ContentBlock{{Type: "text", Text: string(text)}}, "structuredContent": receipt, "isError": false}
		default:
			fail("unexpected MCP method: " + request.Method)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			httpProblems = append(httpProblems, err.Error())
		}
	}))
	defer mcpHTTP.Close()
	state, ambient := t.TempDir(), t.TempDir()
	seed := func(directory, token string) ([]byte, string) {
		t.Helper()
		values := map[string]any{}
		for name, path := range map[string]string{"http_fixture": "/static", "plain_fixture": "/plain"} {
			values["mcp__"+name+"|"+mcpHTTP.URL+path] = map[string]any{"tokens": map[string]string{"access_token": token, "token_type": "Bearer"}}
		}
		data, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "mcp-auth.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return data, path
	}
	ownedBytes, ownedAuth := seed(filepath.Join(state, "agent"), "OWNED_SYNTHETIC_OAUTH")
	ambientBytes, ambientAuth := seed(ambient, "AMBIENT_SYNTHETIC_OAUTH")
	t.Setenv("PI_CODING_AGENT_DIR", ambient)
	h := startPiProcess(t, ctx, state, provider.URL)
	runMCP := func(contextID, runID, label string) piMCPReceipt {
		t.Helper()
		current := scenes[label]
		if _, err := h.client.StartRun(ctx, pi.StartRunParams{ContextID: contextID, RunID: runID, Text: label}); err != nil {
			t.Fatal(err)
		}
		event := h.events.await(t, ctx, contextID, runID, pi.ToolFinished)
		var finished pi.ToolFinishedData
		var input struct {
			Value string `json:"value"`
		}
		var result struct {
			Content []pi.ContentBlock `json:"content"`
		}
		if json.Unmarshal(event.Data, &finished) != nil || finished.IsError || finished.CallID != current.call || finished.Name != current.tool || json.Unmarshal(finished.Input, &input) != nil || input.Value != current.value || json.Unmarshal(finished.Result, &result) != nil {
			t.Fatalf("MCP completion envelope=%+v data=%+v", event, finished)
		}
		t.Logf("actual MCP tool.finished data=%s", event.Data)
		receipt := piMCPContentReceipt(t, result.Content)
		if receipt.Transport != current.transport || receipt.Value != current.value || receipt.ReceiptID != current.transport+":"+current.value {
			t.Fatalf("MCP receipt=%+v", receipt)
		}
		piSettled(t, ctx, h, contextID, runID, pi.Completed, label+"_DONE")
		providerMu.Lock()
		wire := wireReceipts[label]
		requests := counts[label]
		providerMu.Unlock()
		if wire != receipt || requests != 2 {
			t.Fatalf("MCP completion=%+v wire=%+v requests=%d", receipt, wire, requests)
		}
		found := 0
		for _, raw := range piPublicHistory(t, ctx, h.client, contextID) {
			var entry struct {
				Message struct {
					Role       string          `json:"role"`
					ToolCallID string          `json:"toolCallId"`
					ToolName   string          `json:"toolName"`
					IsError    bool            `json:"isError"`
					Content    json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(raw, &entry) != nil {
				t.Fatalf("invalid public history: %s", raw)
			}
			if entry.Message.Role == "toolResult" && entry.Message.ToolCallID == current.call {
				var content []pi.ContentBlock
				if err := json.Unmarshal(entry.Message.Content, &content); err != nil {
					t.Fatalf("invalid matched MCP history content: %s", raw)
				}
				if entry.Message.ToolName != current.tool || entry.Message.IsError || piMCPContentReceipt(t, content) != receipt {
					t.Fatalf("public MCP history mismatch: %s", raw)
				}
				t.Logf("actual MCP public toolResult=%s", raw)
				found++
			}
		}
		if found != 1 {
			t.Fatalf("matching MCP public tool results=%d", found)
		}
		return receipt
	}
	config := func(value any) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	stdioConfig := config(map[string]any{"command": os.Getenv("NAGI_TEST_NODE"), "args": []string{fixturePath, stdioLog}, "env": map[string]string{"NAGI_MCP_SNAPSHOT": "A"}})
	stdio := pi.OpenContextParams{ContextID: "mcp-stdio", BotID: "mcp-bot", Purpose: pi.Conversation, SelectionRevision: 1, CWD: project, CodingTools: []string{}, MCPServers: []pi.MCPServer{{Name: "stdio-fixture", SourceID: "qa-stdio-source", Config: stdioConfig}}}
	binding, err := h.client.OpenContext(ctx, stdio)
	if err != nil {
		t.Fatal(err)
	}
	h.contexts = append(h.contexts, stdio.ContextID)
	first := runMCP(stdio.ContextID, "stdio-run", "STDIO_MCP")
	history := piPublicHistory(t, ctx, h.client, stdio.ContextID)
	closeStdio := func(after uint64, receipt piMCPReceipt) (uint64, string) {
		t.Helper()
		if closed, err := h.client.CloseContext(ctx, stdio.ContextID); err != nil || !closed.Closed {
			t.Fatalf("stdio close=%+v error=%v", closed, err)
		}
		exitEvent, exit := piAwaitMCPProcess(t, ctx, h, stdio.ContextID, stdio.MCPServers[0].SourceID, "exit", after)
		closeEvent, closed := piAwaitMCPProcess(t, ctx, h, stdio.ContextID, stdio.MCPServers[0].SourceID, "child_close", exitEvent.Seq)
		if exit.Server != "stdio-fixture" || closed.Server != exit.Server || exit.AttemptID != closed.AttemptID || exit.PID == nil || receipt.PID <= 0 || *exit.PID != receipt.PID || closed.PID == nil || *closed.PID != receipt.PID || exit.Code == nil || *exit.Code != 0 || closed.Code == nil || *closed.Code != 0 || exit.Signal != nil || closed.Signal != nil || closed.SpawnObserved == nil || !*closed.SpawnObserved || closed.ExitObserved == nil || !*closed.ExitObserved {
			t.Fatalf("stdio direct-child exit=%+v close=%+v receipt=%+v", exit, closed, receipt)
		}
		for _, event := range []pi.Event{exitEvent, closeEvent} {
			var fields map[string]json.RawMessage
			if json.Unmarshal(event.Data, &fields) != nil || string(fields["code"]) != "0" || string(fields["signal"]) != "null" {
				t.Fatalf("MCP outcome fields lost: %s", event.Data)
			}
		}
		t.Logf("actual MCP direct child pid=%d attempt=%s exit_seq=%d close_seq=%d code=0 signal=null", receipt.PID, exit.AttemptID, exitEvent.Seq, closeEvent.Seq)
		return closeEvent.Seq, closed.AttemptID
	}
	firstSeq, firstAttempt := closeStdio(0, first)
	stdio.ResumeRef, stdio.Used = binding.SessionRef, true
	beforeReject, err := os.ReadFile(stdioLog)
	if err != nil {
		t.Fatal(err)
	}
	changed := stdio
	changed.MCPServers = []pi.MCPServer{{Name: "stdio-fixture", SourceID: "qa-stdio-changed", Config: stdioConfig}}
	_, err = h.client.OpenContext(ctx, changed)
	piRejected(t, err, "binding_conflict")
	changed.MCPServers = []pi.MCPServer{{Name: "stdio-fixture", SourceID: "qa-stdio-source", Config: config(map[string]any{"command": os.Getenv("NAGI_TEST_NODE"), "args": []string{fixturePath, stdioLog}, "env": map[string]string{"NAGI_MCP_SNAPSHOT": "B"}})}}
	_, err = h.client.OpenContext(ctx, changed)
	piRejected(t, err, "binding_conflict")
	afterReject, err := os.ReadFile(stdioLog)
	if err != nil || string(beforeReject) != string(afterReject) {
		t.Fatalf("rejected MCP snapshot spawned/mutated fixture: %v", err)
	}
	checkRequests(2)
	reopened, err := h.client.OpenContext(ctx, stdio)
	if err != nil || reopened.SessionRef != binding.SessionRef || !reopened.Used {
		t.Fatalf("captured MCP reopen=%+v error=%v", reopened, err)
	}
	restored := piPublicHistory(t, ctx, h.client, stdio.ContextID)
	for id, raw := range history {
		if string(restored[id]) != string(raw) {
			t.Fatalf("MCP reopen changed public entry %s", id)
		}
	}
	checkRequests(2)
	second := runMCP(stdio.ContextID, "stdio-reopen-run", "STDIO_REOPEN_MCP")
	_, secondAttempt := closeStdio(firstSeq, second)
	if secondAttempt == firstAttempt {
		t.Fatal("reopen reused MCP process attempt")
	}
	logBytes, err := os.ReadFile(stdioLog)
	if err != nil {
		t.Fatal(err)
	}
	started, initialized, listed, calls, stdinClosed, exited := 0, 0, 0, 0, 0, 0
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(logBytes)), "\n") {
		var record struct {
			Kind     string          `json:"kind"`
			PID      int             `json:"pid"`
			Snapshot string          `json:"snapshot"`
			CWD      string          `json:"cwd"`
			Code     *int            `json:"code"`
			Method   string          `json:"method"`
			ID       json.RawMessage `json:"id"`
			Params   struct {
				Name      string `json:"name"`
				Arguments struct {
					Value string `json:"value"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(line), &record) != nil || record.PID != first.PID && record.PID != second.PID || record.Snapshot != "A" {
			t.Fatalf("unmatched stdio receipt: %s", line)
		}
		switch record.Kind {
		case "started":
			started++
			if record.CWD != project {
				t.Fatalf("MCP cwd=%q want=%q", record.CWD, project)
			}
		case "stdin_closed":
			stdinClosed++
		case "exit":
			exited++
			if record.Code == nil || *record.Code != 0 {
				t.Fatalf("fixture exit: %s", line)
			}
		case "request":
			switch record.Method {
			case "initialize":
				initialized++
			case "tools/list":
				listed++
			case "tools/call":
				calls++
				value := record.Params.Arguments.Value
				if record.Params.Name != "echo" || len(record.ID) == 0 || seen[value] || value != "STDIO_REQUEST" && value != "STDIO_REOPEN_REQUEST" {
					t.Fatalf("unmatched/repeated MCP RPC: %s", line)
				}
				seen[value] = true
			}
		}
	}
	if started != 2 || initialized != 2 || listed < 2 || calls != 2 || stdinClosed != 2 || exited != 2 {
		t.Fatalf("stdio started=%d initialize=%d list=%d call=%d EOF=%d exit=%d", started, initialized, listed, calls, stdinClosed, exited)
	}
	checkRequests(4)
	staticConfig := config(map[string]any{"url": mcpHTTP.URL + "/static", "headers": map[string]string{"Authorization": "Bearer mcp-static-only"}})
	static := pi.OpenContextParams{ContextID: "mcp-http", BotID: "mcp-bot", Purpose: pi.Conversation, SelectionRevision: 1, CWD: project, CodingTools: []string{}, MCPServers: []pi.MCPServer{{Name: "http-fixture", SourceID: "qa-http-source", Config: staticConfig}}}
	httpBinding, err := h.client.OpenContext(ctx, static)
	if err != nil {
		t.Fatal(err)
	}
	h.contexts = append(h.contexts, static.ContextID)
	runMCP(static.ContextID, "http-run", "HTTP_MCP")
	if closed, err := h.client.CloseContext(ctx, static.ContextID); err != nil || !closed.Closed {
		t.Fatalf("HTTP close=%+v error=%v", closed, err)
	}
	static.ResumeRef, static.Used = httpBinding.SessionRef, true
	httpChanged := static
	httpChanged.MCPServers = []pi.MCPServer{{Name: "http-fixture", SourceID: "qa-http-source", Config: config(map[string]any{"url": mcpHTTP.URL + "/replacement", "headers": map[string]string{"Authorization": "Bearer mcp-static-only"}})}}
	_, err = h.client.OpenContext(ctx, httpChanged)
	piRejected(t, err, "binding_conflict")
	checkRequests(6)
	if reopened, err := h.client.OpenContext(ctx, static); err != nil || reopened.SessionRef != httpBinding.SessionRef {
		t.Fatalf("HTTP captured reopen=%+v error=%v", reopened, err)
	}
	checkRequests(6)
	runMCP(static.ContextID, "http-reopen-run", "HTTP_REOPEN_MCP")
	if closed, err := h.client.CloseContext(ctx, static.ContextID); err != nil || !closed.Closed {
		t.Fatalf("reopened HTTP close=%+v error=%v", closed, err)
	}
	plain := pi.OpenContextParams{ContextID: "mcp-plain", BotID: "mcp-bot", Purpose: pi.Conversation, SelectionRevision: 1, CWD: project, CodingTools: []string{}, MCPServers: []pi.MCPServer{{Name: "plain-fixture", SourceID: "qa-plain-source", Config: config(map[string]string{"url": mcpHTTP.URL + "/plain"})}}}
	if _, err := h.client.OpenContext(ctx, plain); err != nil {
		t.Fatal(err)
	}
	h.contexts = append(h.contexts, plain.ContextID)
	runMCP(plain.ContextID, "plain-run", "PLAIN_MCP")
	if closed, err := h.client.CloseContext(ctx, plain.ContextID); err != nil || !closed.Closed {
		t.Fatalf("headerless HTTP close=%+v error=%v", closed, err)
	}
	httpMu.Lock()
	for path, want := range map[string]int{"/static": 2, "/plain": 1} {
		ledger := httpCounts[path]
		if ledger["initialize"] != want || ledger["notifications/initialized"] != want || ledger["tools/list"] < want || ledger["tools/call"] != want || ledger["DELETE"] != want {
			t.Errorf("MCP HTTP %s counts=%v want_calls=%d", path, ledger, want)
		}
	}
	if len(httpProblems) != 0 || replacementRequests != 0 {
		t.Errorf("MCP HTTP problems=%v replacement_requests=%d", httpProblems, replacementRequests)
	}
	t.Logf("actual static/headerless MCP HTTP receipts=%v", httpCounts)
	httpMu.Unlock()
	checkRequests(10)
	missing := filepath.Join(t.TempDir(), "nonexistent-mcp-executable")
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing command exists: %v", err)
	}
	failed := pi.OpenContextParams{ContextID: "mcp-failed", BotID: "mcp-bot", Purpose: pi.Conversation, SelectionRevision: 1, CWD: project, CodingTools: []string{}, MCPServers: []pi.MCPServer{{Name: "failed-fixture", SourceID: "qa-failed-source", Config: config(map[string]string{"command": missing})}}}
	if _, err := h.client.OpenContext(ctx, failed); err != nil {
		t.Fatal(err)
	}
	h.contexts = append(h.contexts, failed.ContextID)
	spawnEvent, spawn := piAwaitMCPProcess(t, ctx, h, failed.ContextID, "qa-failed-source", "spawn_error", 0)
	_, failedClose := piAwaitMCPProcess(t, ctx, h, failed.ContextID, "qa-failed-source", "child_close", spawnEvent.Seq)
	if spawn.Server != "failed-fixture" || failedClose.Server != spawn.Server || spawn.AttemptID != failedClose.AttemptID || spawn.PID != nil || failedClose.PID != nil || spawn.Error == nil || spawn.Error.Code != "ENOENT" || failedClose.Code == nil || *failedClose.Code == 0 || failedClose.Signal != nil || failedClose.SpawnObserved == nil || *failedClose.SpawnObserved || failedClose.ExitObserved == nil || *failedClose.ExitObserved {
		t.Fatalf("actual failed spawn=%+v close=%+v", spawn, failedClose)
	}
	if closed, err := h.client.CloseContext(ctx, failed.ContextID); err != nil || !closed.Closed {
		t.Fatalf("failed context close=%+v error=%v", closed, err)
	}
	checkRequests(10)
	h.shutdown(t, ctx)
	for path, want := range map[string][]byte{ownedAuth: ownedBytes, ambientAuth: ambientBytes} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("synthetic credential state changed: %s error=%v", path, err)
		}
	}
	h.events.mu.Lock()
	values := append([]pi.Event(nil), h.events.values...)
	h.events.mu.Unlock()
	sequences := map[string]uint64{}
	processEvents := 0
	for _, event := range values {
		if event.Name != pi.MCPProcess {
			continue
		}
		var data pi.MCPProcessEvent
		if json.Unmarshal(event.Data, &data) != nil || event.WorkerEpoch != h.ready.WorkerEpoch || event.RunID != "" || event.Seq <= sequences[event.ContextID] {
			t.Fatalf("invalid drained MCP event=%+v", event)
		}
		sequences[event.ContextID] = event.Seq
		if data.AttemptID == spawn.AttemptID && data.Kind == "exit" {
			t.Fatal("failed spawn invented a direct-child exit")
		}
		processEvents++
	}
	if processEvents != 6 {
		t.Fatalf("normal exit/close plus failed error/close events=%d want=6", processEvents)
	}
	t.Logf("actual MCP process root: model requests=10; stdio calls=2; static HTTP calls=2; headerless HTTP calls=1; rejected snapshots/failed spawn HTTP delta=0; worker reap/callback drain exported separately")
}
