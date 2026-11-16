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
