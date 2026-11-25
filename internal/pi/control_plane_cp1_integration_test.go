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
	"unicode/utf8"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

type cp1ToolCall struct {
	ID   string
	Name string
	Args string
}

type cp1Reply struct {
	Text  string
	Calls []cp1ToolCall
}

type cp1Request struct {
	System      string
	Tools       []string
	LastRole    string
	LastToolID  string
	LastContent string
	UserText    string
	ToolResults map[string]string
}

type cp1Provider struct {
	t        *testing.T
	server   *httptest.Server
	secret   string
	calls    atomic.Int64
	mu       sync.Mutex
	handler  func(req cp1Request) cp1Reply
	requests []cp1Request
	problems []string
	forbid   []string
}

func newCP1Provider(t *testing.T, secret string, handler func(req cp1Request) cp1Reply) *cp1Provider {
	t.Helper()
	p := &cp1Provider{t: t, secret: secret, handler: handler}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

func (p *cp1Provider) setHandler(fn func(req cp1Request) cp1Reply) {
	p.mu.Lock()
	p.handler = fn
	p.mu.Unlock()
}

func (p *cp1Provider) addForbiddenSecret(value string) {
	p.mu.Lock()
	p.forbid = append(p.forbid, value)
	p.mu.Unlock()
}

func (p *cp1Provider) recordProblem(msg string) {
	p.mu.Lock()
	p.problems = append(p.problems, msg)
	p.mu.Unlock()
}

func (p *cp1Provider) assertNoProblems() {
	p.t.Helper()
	p.mu.Lock()
	probs := append([]string(nil), p.problems...)
	p.mu.Unlock()
	if len(probs) > 0 {
		p.t.Fatalf("cp1 provider problems: %v", probs)
	}
}

func (p *cp1Provider) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		p.recordProblem("unexpected path " + r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	p.calls.Add(1)
	if p.secret != "" && r.Header.Get("Authorization") != "Bearer "+p.secret {
		p.recordProblem("unexpected auth header")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
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
		p.recordProblem("decode provider request: " + err.Error())
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	raw, _ := json.Marshal(body)
	p.mu.Lock()
	forbid := append([]string(nil), p.forbid...)
	handler := p.handler
	p.mu.Unlock()
	for _, secret := range forbid {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			p.recordProblem("forbidden secret leaked into outbound provider body")
		}
	}
	var sysParts []string
	var userText string
	toolResults := map[string]string{}
	for _, m := range body.Messages {
		text := unmarshalToolText(m.Content)
		switch m.Role {
		case "system", "developer":
			sysParts = append(sysParts, text)
		case "user":
			if idx := strings.Index(text, "\n\n<active_activities>"); idx >= 0 {
				text = text[:idx]
			}
			userText = text
		case "tool":
			if m.ToolCallID != "" {
				toolResults[m.ToolCallID] = text
			}
		}
	}
	tools := make([]string, 0, len(body.Tools))
	for _, item := range body.Tools {
		tools = append(tools, item.Function.Name)
	}
	if !slices.IsSorted(tools) {
		p.recordProblem(fmt.Sprintf("tools not sorted: %v", tools))
	}
	last := body.Messages[len(body.Messages)-1]
	req := cp1Request{
		System:      strings.Join(sysParts, "\n"),
		Tools:       tools,
		LastRole:    last.Role,
		LastToolID:  last.ToolCallID,
		LastContent: unmarshalToolText(last.Content),
		UserText:    userText,
		ToolResults: toolResults,
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	if handler == nil {
		piChatResponse(w, "OK", "", "", "")
		return
	}
	reply := handler(req)
	writeCP1SSE(w, reply)
}

func writeCP1SSE(w http.ResponseWriter, reply cp1Reply) {
	w.Header().Set("Content-Type", "text/event-stream")
	var chunks []map[string]any
	if reply.Text != "" {
		chunks = append(chunks, map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": reply.Text}, "finish_reason": nil})
	}
	finish := "stop"
	if len(reply.Calls) > 0 {
		finish = "tool_calls"
		toolCalls := make([]any, 0, len(reply.Calls))
		for i, call := range reply.Calls {
			args := call.Args
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, map[string]any{
				"index": i,
				"id":    call.ID,
				"type":  "function",
				"function": map[string]string{
					"name":      call.Name,
					"arguments": args,
				},
			})
		}
		delta := map[string]any{"tool_calls": toolCalls}
		if reply.Text == "" {
			delta["role"] = "assistant"
		}
		chunks = append(chunks, map[string]any{"index": 0, "delta": delta, "finish_reason": nil})
	} else if reply.Text == "" {
		chunks = append(chunks, map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "OK"}, "finish_reason": nil})
	}
	chunks = append(chunks, map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish})
	for _, choice := range chunks {
		line, _ := json.Marshal(map[string]any{
			"id":      "cp1-local-response",
			"object":  "chat.completion.chunk",
			"created": 1,
			"model":   productModel,
			"choices": []any{choice},
		})
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

type cp1Process struct {
	client   *pi.ProductClient
	exits    chan pi.ProcessOutcome
	mu       sync.Mutex
	events   []pi.ProductEvent
	problems []error
	notify   chan struct{}
	streamID string
	seq      uint64
	closed   bool
}

func startCP1Process(t *testing.T, options pi.ProductOptions) *cp1Process {
	t.Helper()
	p := &cp1Process{
		exits:  make(chan pi.ProcessOutcome, 1),
		notify: make(chan struct{}),
	}
	signal := func() {
		close(p.notify)
		p.notify = make(chan struct{})
	}
	options.Handlers = pi.ProductHandlers{
		OnEvent: func(event pi.ProductEvent) {
			p.mu.Lock()
			if p.streamID != "" && (event.StreamID != p.streamID || event.Seq != p.seq+1) {
				p.problems = append(p.problems, fmt.Errorf("event stream=%q seq=%d want=%q/%d", event.StreamID, event.Seq, p.streamID, p.seq+1))
			} else if p.streamID != "" {
				p.seq = event.Seq
			}
			p.events = append(p.events, event)
			signal()
			p.mu.Unlock()
		},
		OnProtocolErr: func(err error) {
			p.mu.Lock()
			p.problems = append(p.problems, err)
			signal()
			p.mu.Unlock()
		},
		OnStreamGap: func() {
			p.mu.Lock()
			p.problems = append(p.problems, pi.ErrProductStreamGap)
			signal()
			p.mu.Unlock()
		},
		OnExit: func(outcome pi.ProcessOutcome) {
			p.exits <- outcome
		},
	}
	client, err := pi.StartProduct(options)
	if err != nil {
		t.Fatal(err)
	}
	p.client = client
	t.Cleanup(func() {
		if !p.closed {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			outcome, err := client.Kill(ctx)
			if !outcome.Reaped {
				t.Errorf("cleanup could not reap cp1 worker: %+v: %v", outcome, err)
			}
		}
	})
	return p
}

func (p *cp1Process) open(t *testing.T, ctx context.Context) pi.ProductOpenResult {
	t.Helper()
	ready, err := p.client.Ready(ctx)
	if err != nil || ready.Engine.Version != "1.5.2" || ready.Runtime.Version != "v22.23.5" || ready.WorkerEpoch == "" {
		t.Fatalf("cp1 worker ready=%+v err=%v", ready, err)
	}
	opened, err := p.client.Open(ctx, []int{pi.ProductVersion})
	if err != nil || opened.Version != 1 || opened.StreamID == "" {
		t.Fatalf("cp1 worker open=%+v err=%v", opened, err)
	}
	p.mu.Lock()
	p.streamID = opened.StreamID
	p.seq = opened.EventSeq
	p.mu.Unlock()
	return opened
}

func (p *cp1Process) exit(t *testing.T, ctx context.Context, outcome pi.ProcessOutcome, success bool, err error) {
	t.Helper()
	if !outcome.Reaped || outcome.PID <= 0 || success != (outcome.ExitCode == 0 && err == nil) {
		t.Fatalf("cp1 worker exit=%+v success=%v err=%v", outcome, success, err)
	}
	select {
	case <-p.client.CallbacksDone():
	case <-ctx.Done():
		t.Fatal("cp1 worker callbacks did not finish")
	}
	select {
	case observed := <-p.exits:
		if !observed.Reaped || observed.PID != outcome.PID || observed.ExitCode != outcome.ExitCode {
			t.Fatalf("cp1 worker exit callback=%+v wait=%+v", observed, outcome)
		}
	default:
		t.Fatal("cp1 worker missing exit callback")
	}
	p.closed = true
}

func (p *cp1Process) shutdown(t *testing.T, ctx context.Context) {
	t.Helper()
	outcome, err := p.client.Shutdown(ctx)
	p.exit(t, ctx, outcome, true, err)
	p.mu.Lock()
	probs := append([]error(nil), p.problems...)
	p.mu.Unlock()
	if len(probs) > 0 {
		t.Fatalf("cp1 product stream problems: %v", probs)
	}
}

func (p *cp1Process) waitEvent(t *testing.T, ctx context.Context, pred func(pi.ProductEvent) bool) pi.ProductEvent {
	t.Helper()
	scanned := 0
	for {
		p.mu.Lock()
		if len(p.problems) > 0 {
			err := p.problems[0]
			p.mu.Unlock()
			t.Fatalf("cp1 product stream error: %v", err)
		}
		for scanned < len(p.events) {
			ev := p.events[scanned]
			scanned++
			if pred(ev) {
				p.mu.Unlock()
				return ev
			}
		}
		ch := p.notify
		p.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for cp1 product event after %d events: %v", scanned, ctx.Err())
		}
	}
}

func (p *cp1Process) waitSettled(t *testing.T, ctx context.Context, execID string) pi.ProductExecutionEvent {
	t.Helper()
	var result pi.ProductExecutionEvent
	p.waitEvent(t, ctx, func(ev pi.ProductEvent) bool {
		if ev.Name != "execution.settled" {
			return false
		}
		var settled pi.ProductExecutionEvent
		if json.Unmarshal(ev.Data, &settled) != nil || settled.ExecutionID != execID {
			return false
		}
		result = settled
		return true
	})
	return result
}

func (p *cp1Process) waitActivityChange(t *testing.T, ctx context.Context, pred func(pi.ProductActivityChange) bool) pi.ProductActivityChange {
	t.Helper()
	ev := p.waitEvent(t, ctx, func(ev pi.ProductEvent) bool {
		return ev.Name == "activity.changed" && ev.ActivityChanged != nil && pred(*ev.ActivityChanged)
	})
	return *ev.ActivityChanged
}

func (p *cp1Process) activityChangesFor(activityID string) []pi.ProductActivityChange {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []pi.ProductActivityChange
	for _, ev := range p.events {
		if ev.Name == "activity.changed" && ev.ActivityChanged != nil && ev.ActivityChanged.ActivityID == activityID {
			out = append(out, *ev.ActivityChanged)
		}
	}
	return out
}

func cp1WorkerPaths(t *testing.T) (string, string) {
	t.Helper()
	nodePath := os.Getenv("NAGI_TEST_NODE")
	workerPath := os.Getenv("NAGI_TEST_WORKER")
	for _, p := range []string{nodePath, workerPath} {
		if info, err := os.Stat(p); err != nil || !filepath.IsAbs(p) || !info.Mode().IsRegular() {
			t.Fatalf("NAGI_TEST_NODE and NAGI_TEST_WORKER must name pinned regular absolute files: %q: %v", p, err)
		}
	}
	return nodePath, workerPath
}

var cp1ConnOpSeq atomic.Int64

func cp1ConfigureConnection(t *testing.T, ctx context.Context, client *pi.ProductClient, baseURL, apiKey, cwd string) {
	t.Helper()
	seq := cp1ConnOpSeq.Add(1)
	saved, err := client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: fmt.Sprintf("cp1-conn-save-%d", seq),
		Configuration: pi.ProductConnectionInput{
			APIKey: apiKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI:          pi.OpenAICompletions,
				BaseURL:          baseURL,
				Model:            productModel,
				ContextTokens:    262144,
				OutputTokens:     4096,
				WorkingDirectory: cwd,
				MaxActiveTasks:   4,
			},
		},
	})
	if err != nil || saved.SavedRevision == nil {
		t.Fatalf("SaveConnection = %+v, %v", saved, err)
	}
	if _, err := client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{
		OperationID:   fmt.Sprintf("cp1-conn-apply-%d", seq),
		SavedRevision: *saved.SavedRevision,
	}); err != nil {
		t.Fatalf("ApplyConnection = %v", err)
	}
}

func cp1SendTurn(t *testing.T, ctx context.Context, proc *cp1Process, convID, opPrefix, text string, materials []pi.ProductMaterial) pi.ProductExecutionEvent {
	t.Helper()
	conv, err := proc.client.GetConversation(ctx, convID)
	if err != nil || conv.Draft == nil {
		t.Fatalf("GetConversation(%s) = %+v, %v", convID, conv, err)
	}
	if materials == nil {
		materials = []pi.ProductMaterial{}
	}
	saved, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
		ProductText:               pi.ProductText{Text: text, Materials: materials},
		ExpectedRevision:          conv.Draft.Revision,
		OperationID:               opPrefix + "-draft",
	})
	if err != nil {
		t.Fatalf("SaveDraft(%s) = %v", opPrefix, err)
	}
	receipt, err := proc.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
		DraftRevision:             saved.Revision,
		OperationID:               opPrefix + "-send",
	})
	if err != nil || receipt.State != "accepted" || receipt.Execution == nil {
		t.Fatalf("Send(%s) = %+v, %v", opPrefix, receipt, err)
	}
	return proc.waitSettled(t, ctx, receipt.Execution.ExecutionID)
}

func cp1AssertRemoteCode(t *testing.T, err error, want string) {
	t.Helper()
	var remote *pi.RemoteError
	if !errors.As(err, &remote) || remote.Code != want {
		t.Fatalf("error = %v, want remote code %q", err, want)
	}
}

func cp1FileSHA256(t *testing.T, path string) string {
	t.Helper()
	sum := sha256.Sum256(productRead(t, path))
	return hex.EncodeToString(sum[:])
}

func cp1AuditLineCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func TestPiProductCP1ActivityParityCapturedCwdAndSkillApprovalCard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	nodePath, workerPath := cp1WorkerPaths(t)
	root := t.TempDir()
	wsA, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wsB1, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wsB2, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wsB1Alias := filepath.Join(root, "ws-b1-alias")
	if err := os.Symlink(wsB1, wsB1Alias); err != nil {
		t.Fatal(err)
	}

	const providerSecret = "cp1-parity-provider-key-4411"
	var mu sync.Mutex
	var botASystem, botBActivitySystem string
	var botBActivityTools []string
	var act1ToolsChecked bool
	var botBMemorySearchJSON string
	var act1Phase int
	var actContinueSystem string
	var mixedPhase int
	var mixedDeniedToolJSON string
	var mixedAnswerToolJSON string
	var stopPhase int
	var mutatePhase int
	var crashPhase int
	var botBID string

	provider := newCP1Provider(t, providerSecret, func(req cp1Request) cp1Reply {
		mu.Lock()
		defer mu.Unlock()
		isActivity := slices.Contains(req.Tools, "activity_ask")
		if !isActivity {
			if strings.Contains(req.System, "Bot Identity\nName: BotA") {
				botASystem = req.System
			}
			switch req.UserText {
			case "SEED_LOG_A":
				if req.LastRole == "user" {
					return cp1Reply{Calls: []cp1ToolCall{{ID: "log-a-1", Name: "memory_log_work", Args: `{"summary":"BotA private audit log entry","tags":["alpha"]}`}}}
				}
				return cp1Reply{Text: "Logged A"}
			case "SEED_LOG_B":
				if req.LastRole == "user" {
					return cp1Reply{Calls: []cp1ToolCall{{ID: "log-b-1", Name: "memory_log_work", Args: `{"summary":"BotB inventory baseline entry","tags":["beta"]}`}}}
				}
				return cp1Reply{Text: "Logged B"}
			case "DELEGATE_PARITY":
				if req.LastRole == "user" {
					args, _ := json.Marshal(map[string]any{
						"key":         "parity-handoff-1",
						"goal":        "CP1_PARITY_AND_APPROVAL",
						"assignee_id": botBID,
					})
					return cp1Reply{Calls: []cp1ToolCall{{ID: "del-parity-1", Name: "activity_delegate", Args: string(args)}}}
				}
				return cp1Reply{Text: "Delegated parity task"}
			case "DELEGATE_MIXED":
				if req.LastRole == "user" {
					args, _ := json.Marshal(map[string]any{
						"key":         "mixed-waiters-1",
						"goal":        "CP1_MIXED_WAITERS",
						"assignee_id": botBID,
					})
					return cp1Reply{Calls: []cp1ToolCall{{ID: "del-mixed-1", Name: "activity_delegate", Args: string(args)}}}
				}
				return cp1Reply{Text: "Delegated mixed task"}
			case "DELEGATE_STOP":
				if req.LastRole == "user" {
					args, _ := json.Marshal(map[string]any{
						"key":         "stop-approval-1",
						"goal":        "CP1_STOP_APPROVAL",
						"assignee_id": botBID,
					})
					return cp1Reply{Calls: []cp1ToolCall{{ID: "del-stop-1", Name: "activity_delegate", Args: string(args)}}}
				}
				return cp1Reply{Text: "Delegated stop task"}
			case "DELEGATE_MUTATE":
				if req.LastRole == "user" {
					args, _ := json.Marshal(map[string]any{
						"key":         "mutate-approval-1",
						"goal":        "CP1_MUTATE_APPROVAL",
						"assignee_id": botBID,
					})
					return cp1Reply{Calls: []cp1ToolCall{{ID: "del-mutate-1", Name: "activity_delegate", Args: string(args)}}}
				}
				return cp1Reply{Text: "Delegated mutate task"}
			case "DELEGATE_CRASH":
				if req.LastRole == "user" {
					args, _ := json.Marshal(map[string]any{
						"key":         "crash-approval-1",
						"goal":        "CP1_CRASH_APPROVAL",
						"assignee_id": botBID,
					})
					return cp1Reply{Calls: []cp1ToolCall{{ID: "del-crash-1", Name: "activity_delegate", Args: string(args)}}}
				}
				return cp1Reply{Text: "Delegated crash task"}
			default:
				return cp1Reply{Text: "OK"}
			}
		}

		switch {
		case strings.Contains(req.UserText, "CP1_CONTINUE_CHECK"):
			actContinueSystem = req.System
			return cp1Reply{Text: "Continue preserved captured binding"}
		case strings.Contains(req.UserText, "CP1_PARITY_AND_APPROVAL"):
			if !act1ToolsChecked {
				botBActivitySystem = req.System
				botBActivityTools = append([]string(nil), req.Tools...)
				act1ToolsChecked = true
			}
			act1Phase++
			switch act1Phase {
			case 1:
				return cp1Reply{Calls: []cp1ToolCall{{ID: "act-mem-1", Name: "memory_search_log", Args: `{"query":"entry"}`}}}
			case 2:
				botBMemorySearchJSON = req.ToolResults["act-mem-1"]
				return cp1Reply{Calls: []cp1ToolCall{{ID: "act-pure-test", Name: "skill_test", Args: `{"skill_id":"pure-calc","source":"private","script":"double","input":{"n":21}}`}}}
			case 3:
				return cp1Reply{Calls: []cp1ToolCall{{ID: "act-pure-exec", Name: "skill_exec", Args: `{"skill_id":"pure-calc","source":"private","script":"double","input":{"n":21}}`}}}
			case 4:
				return cp1Reply{Calls: []cp1ToolCall{{ID: "act-priv-exec", Name: "skill_exec", Args: `{"skill_id":"ws-append","source":"private","script":"append","input":{"line":"approved-once"}}`}}}
			default:
				return cp1Reply{Text: "Parity and approved skill succeeded"}
			}
		case strings.Contains(req.UserText, "CP1_MIXED_WAITERS"):
			mixedPhase++
			if mixedPhase == 1 {
				return cp1Reply{Calls: []cp1ToolCall{
					{ID: "mixed-ask-1", Name: "activity_ask", Args: `{"text":"Confirm sibling question?"}`},
					{ID: "mixed-skill-1", Name: "skill_exec", Args: `{"skill_id":"ws-append","source":"private","script":"append","input":{"line":"must-not-write"}}`},
				}}
			}
			mixedAnswerToolJSON = req.ToolResults["mixed-ask-1"]
			mixedDeniedToolJSON = req.ToolResults["mixed-skill-1"]
			return cp1Reply{Text: "Mixed waiters finished"}
		case strings.Contains(req.UserText, "CP1_STOP_APPROVAL"):
			stopPhase++
			if stopPhase == 1 {
				return cp1Reply{Calls: []cp1ToolCall{{ID: "stop-skill-1", Name: "skill_exec", Args: `{"skill_id":"ws-append","source":"private","script":"append","input":{"line":"stopped-run"}}`}}}
			}
			return cp1Reply{Text: "Stopped"}
		case strings.Contains(req.UserText, "CP1_MUTATE_APPROVAL"):
			mutatePhase++
			if mutatePhase == 1 {
				return cp1Reply{Calls: []cp1ToolCall{{ID: "mutate-skill-1", Name: "skill_exec", Args: `{"skill_id":"ws-append","source":"private","script":"append","input":{"line":"mutated-run"}}`}}}
			}
			return cp1Reply{Text: "Mutated"}
		case strings.Contains(req.UserText, "CP1_CRASH_APPROVAL"):
			crashPhase++
			if crashPhase == 1 {
				return cp1Reply{Calls: []cp1ToolCall{{ID: "crash-skill-1", Name: "skill_exec", Args: `{"skill_id":"ws-append","source":"private","script":"append","input":{"line":"crashed-run"}}`}}}
			}
			return cp1Reply{Text: "Crashed"}
		default:
			return cp1Reply{Text: "Done"}
		}
	})

	opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(root, "pi"),
		ProfilePath:     filepath.Join(root, "contacts.json"),
		CredentialsPath: filepath.Join(root, "connection.json"),
		FreshProfile:    true,
	}
	proc := startCP1Process(t, opts)
	_ = proc.open(t, ctx)
	cp1ConfigureConnection(t, ctx, proc.client, provider.server.URL+"/v1", providerSecret, wsA)

	botA, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp1-create-a",
		Profile: pi.ProductBotProfile{
			Name: "BotA", Role: "Coordinator", Purpose: "Delegate tasks",
			Instructions: "Coordinate work cleanly.", Workspace: wsA, ToolPreset: pi.ToolPresetReadonly,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	botB, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp1-create-b",
		Profile: pi.ProductBotProfile{
			Name: "BotB", Role: "Specialist", Purpose: "Run specialist skills",
			Instructions: "Original BotB instructions.", Workspace: wsB1Alias, ToolPreset: pi.ToolPresetReadonly,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = botB.BotID
	mu.Unlock()

	if _, err := proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: botA.BotID, OperationID: "cp1-rule-a"},
		Text:                   "BotA exclusive rule alpha-991",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: botB.BotID, OperationID: "cp1-rule-b"},
		Text:                   "BotB lasting rule beta-772",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: botB.BotID, SkillID: "pure-calc"},
		Name:            "Pure Calc",
		Description:     "Pure compute doubling script",
		Content:         "Use pure-calc for deterministic math.",
		OperationID:     "cp1-save-pure",
		Manifest: &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"double": {Entry: "scripts/double.mjs", Description: "Double number", Permissions: pi.ProductSkillPermissions{}},
			},
		},
		Scripts: map[string]string{
			"scripts/double.mjs": "export default async function(input) { return { doubled: input.n * 2 }; }\n",
		},
	}); err != nil {
		t.Fatal(err)
	}

	appendScriptV1 := "import { appendFileSync } from 'node:fs';\nimport { join } from 'node:path';\nexport default async function(input, ctx) { appendFileSync(join(ctx.workspace, 'audit.log'), input.line + '\\n', 'utf8'); return { appended: input.line, cwd: ctx.workspace }; }\n"
	appendScriptV2 := "import { appendFileSync } from 'node:fs';\nimport { join } from 'node:path';\nexport default async function(input, ctx) { appendFileSync(join(ctx.workspace, 'audit.log'), 'v2:' + input.line + '\\n', 'utf8'); return { appended: 'v2:' + input.line }; }\n"
	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: botB.BotID, SkillID: "ws-append"},
		Name:            "Workspace Append",
		Description:     "Append an audit line in workspace",
		Content:         "Appends a line into workspace audit.log.",
		OperationID:     "cp1-save-append-v1",
		Manifest: &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"append": {
					Entry:       "scripts/append.mjs",
					Description: "Append audit line",
					Permissions: pi.ProductSkillPermissions{WorkspaceRead: true, WorkspaceWrite: true},
				},
			},
		},
		Scripts: map[string]string{"scripts/append.mjs": appendScriptV1},
	}); err != nil {
		t.Fatal(err)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "seed-a", "SEED_LOG_A", nil); settled.State != "completed" {
		t.Fatalf("seed A settled = %+v", settled)
	}
	if settled := cp1SendTurn(t, ctx, proc, botB.CurrentConversationID, "seed-b", "SEED_LOG_B", nil); settled.State != "completed" {
		t.Fatalf("seed B settled = %+v", settled)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-1", "DELEGATE_PARITY", nil); settled.State != "completed" {
		t.Fatalf("delegate parity settled = %+v", settled)
	}

	parkedChange := proc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.Goal == "CP1_PARITY_AND_APPROVAL" && ev.State == "waitingForAnswer" && len(ev.PendingApprovalRequestIDs) == 1
	})
	if len(parkedChange.ApprovalRequests) != 1 {
		t.Fatalf("expected 1 approval request on activity.changed, got %+v", parkedChange.ApprovalRequests)
	}
	req1 := parkedChange.ApprovalRequests[0]
	if req1.State != "open" || req1.Revision != 1 || req1.BotID != botB.BotID || req1.SkillID != "ws-append" ||
		req1.CanonicalWorkspace != wsB1 || req1.ApprovalDigest == "" || req1.PermissionsDigest == "" || req1.InstallationID == "" {
		t.Fatalf("unexpected approval request card: %+v", req1)
	}

	mu.Lock()
	gotBotASys := botASystem
	gotBotBSys := botBActivitySystem
	gotBotBTools := append([]string(nil), botBActivityTools...)
	gotMemSearch := botBMemorySearchJSON
	mu.Unlock()

	if !strings.HasPrefix(gotBotASys, "You are a long-lived Nagi Bot teammate.") || !strings.Contains(gotBotASys, "Use activity_delegate") {
		t.Fatalf("foreground BotA prompt missing line-1 anchor or conversationGuidance: %q", gotBotASys)
	}
	if !strings.HasPrefix(gotBotBSys, "You are a long-lived Nagi Bot teammate.") {
		t.Fatalf("Activity prompt lost line-1 anchor: %q", gotBotBSys)
	}
	if strings.Contains(gotBotBSys, "Use activity_delegate") || strings.Contains(gotBotBSys, "PandoraBox") {
		t.Fatalf("Activity prompt leaked foreground or Architect instructions: %q", gotBotBSys)
	}
	for _, want := range []string{"BotB lasting rule beta-772", "<available_skills>", "ws-append", "pure-calc", "Default working directory: " + wsB1} {
		if !strings.Contains(gotBotBSys, want) {
			t.Fatalf("Activity prompt missing %q: %q", want, gotBotBSys)
		}
	}
	if strings.Contains(gotBotBSys, "BotA exclusive rule alpha-991") {
		t.Fatalf("Activity prompt leaked BotA rules: %q", gotBotBSys)
	}
	for _, wantTool := range []string{
		"activity_ask", "activity_progress", "activity_result_read", "activity_trace_read",
		"conversation_read", "conversation_search", "memory_search_log",
		"skill_exec", "skill_list", "skill_read", "skill_test",
	} {
		if !slices.Contains(gotBotBTools, wantTool) {
			t.Fatalf("Activity missing required tool %q in %v", wantTool, gotBotBTools)
		}
	}
	for _, forbiddenTool := range []string{
		"skill_save", "skill_delete", "memory_save_rule", "memory_forget_rule", "memory_propose_learning",
		"memory_log_work", "memory_list_rules", "activity_delegate", "activity_action", "activity_list",
		"activity_get", "bot_list", "bot_inspect_team", "bot_create", "bot_update",
	} {
		if slices.Contains(gotBotBTools, forbiddenTool) {
			t.Fatalf("Activity exposed forbidden tool %q in %v", forbiddenTool, gotBotBTools)
		}
	}
	if !strings.Contains(gotMemSearch, "BotB inventory baseline entry") || strings.Contains(gotMemSearch, "BotA private audit log entry") {
		t.Fatalf("Activity memory_search_log scoping failed: %s", gotMemSearch)
	}

	dbPath := filepath.Join(root, "nagi.db")
	skillTestClass := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const rows = db.prepare("SELECT tool_name, tool_class FROM execution_mutation_admissions WHERE execution_id = ?").all(JSON.stringify(process.argv[2]));
db.close();
console.log(JSON.stringify(rows));
`, req1.ExecutionID)
	if !strings.Contains(skillTestClass, `"tool_name":"skill_test","tool_class":"mutating"`) {
		t.Fatalf("skill_test was not classified mutating in M4 ledger: %s", skillTestClass)
	}

	updatedBotB, err := proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			OperationID:      "cp1-edit-b-midrun",
			BotID:            botB.BotID,
			ExpectedRevision: botB.ProfileRevision,
		},
		Profile: pi.ProductBotProfile{
			Name: "BotB-Renamed", Role: "Specialist", Purpose: "Edited mid-run",
			Instructions: "Mutated BotB instructions after binding.", Workspace: wsB2, ToolPreset: pi.ToolPresetReadonly,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:    pi.ProductSkillKey{Source: req1.Source, BotID: req1.BotID, SkillID: req1.SkillID},
		ApprovalDigest:     req1.ApprovalDigest,
		RequestID:          req1.RequestID,
		RequestRevision:    req1.Revision,
		CanonicalWorkspace: req1.CanonicalWorkspace,
		InstallationID:     req1.InstallationID,
		OperationID:        "cp1-approve-missing-perm-digest",
	})
	cp1AssertRemoteCode(t, err, "invalid_params")

	_, err = proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:    pi.ProductSkillKey{Source: req1.Source, BotID: req1.BotID, SkillID: req1.SkillID},
		ApprovalDigest:     req1.ApprovalDigest,
		PermissionsDigest:  req1.PermissionsDigest,
		RequestID:          req1.RequestID,
		RequestRevision:    99,
		CanonicalWorkspace: req1.CanonicalWorkspace,
		InstallationID:     req1.InstallationID,
		OperationID:        "cp1-approve-bad-rev",
	})
	cp1AssertRemoteCode(t, err, "stale_revision")

	_, err = proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:    pi.ProductSkillKey{Source: req1.Source, BotID: req1.BotID, SkillID: req1.SkillID},
		ApprovalDigest:     strings.Repeat("0", 64),
		PermissionsDigest:  req1.PermissionsDigest,
		RequestID:          req1.RequestID,
		RequestRevision:    req1.Revision,
		CanonicalWorkspace: req1.CanonicalWorkspace,
		InstallationID:     req1.InstallationID,
		OperationID:        "cp1-approve-bad-digest",
	})
	cp1AssertRemoteCode(t, err, "approval_required")

	_, err = proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:    pi.ProductSkillKey{Source: req1.Source, BotID: req1.BotID, SkillID: req1.SkillID},
		ApprovalDigest:     req1.ApprovalDigest,
		PermissionsDigest:  strings.Repeat("f", 64),
		RequestID:          req1.RequestID,
		RequestRevision:    req1.Revision,
		CanonicalWorkspace: req1.CanonicalWorkspace,
		InstallationID:     req1.InstallationID,
		OperationID:        "cp1-approve-bad-perm-digest",
	})
	cp1AssertRemoteCode(t, err, "approval_required")

	_, err = proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:    pi.ProductSkillKey{Source: req1.Source, BotID: req1.BotID, SkillID: req1.SkillID},
		ApprovalDigest:     req1.ApprovalDigest,
		PermissionsDigest:  req1.PermissionsDigest,
		RequestID:          req1.RequestID,
		RequestRevision:    req1.Revision,
		CanonicalWorkspace: wsB2,
		InstallationID:     req1.InstallationID,
		OperationID:        "cp1-approve-wrong-ws",
	})
	cp1AssertRemoteCode(t, err, "approval_required")

	if cp1AuditLineCount(t, filepath.Join(wsB1, "audit.log")) != 0 || cp1AuditLineCount(t, filepath.Join(wsB2, "audit.log")) != 0 {
		t.Fatal("rejected approvals spawned skill code")
	}

	approved, err := proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:    pi.ProductSkillKey{Source: req1.Source, BotID: req1.BotID, SkillID: req1.SkillID},
		ApprovalDigest:     req1.ApprovalDigest,
		PermissionsDigest:  req1.PermissionsDigest,
		RequestID:          req1.RequestID,
		RequestRevision:    req1.Revision,
		CanonicalWorkspace: req1.CanonicalWorkspace,
		InstallationID:     req1.InstallationID,
		OperationID:        "cp1-approve-exact",
	})
	if err != nil || approved.Change != "approved" || approved.Request == nil || approved.Request.State != "approved" || approved.Request.Revision != 2 {
		t.Fatalf("exact ApproveSkill = %+v, %v", approved, err)
	}

	if settled := proc.waitSettled(t, ctx, req1.ExecutionID); settled.State != "completed" {
		t.Fatalf("approved skill execution settled = %+v", settled)
	}
	act1Detail, err := proc.client.GetActivity(ctx, req1.ActivityID)
	if err != nil || act1Detail.State != "completed" || len(act1Detail.PendingApprovalRequestIDs) != 0 {
		t.Fatalf("activity after approved skill = %+v, %v", act1Detail, err)
	}
	if cp1AuditLineCount(t, filepath.Join(wsB1, "audit.log")) != 1 || cp1AuditLineCount(t, filepath.Join(wsB2, "audit.log")) != 0 {
		t.Fatalf("captured cwd write count mismatch: wsB1=%d wsB2=%d", cp1AuditLineCount(t, filepath.Join(wsB1, "audit.log")), cp1AuditLineCount(t, filepath.Join(wsB2, "audit.log")))
	}

	contReceipt, err := proc.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: req1.ActivityID},
		ProductText:           pi.ProductText{Text: "CP1_CONTINUE_CHECK", Materials: []pi.ProductMaterial{}},
		OperationID:           "cp1-continue-parity",
		ExpectedRevision:      act1Detail.Revision,
	})
	if err != nil || contReceipt.Execution == nil {
		t.Fatalf("ContinueActivity = %+v, %v", contReceipt, err)
	}
	if settled := proc.waitSettled(t, ctx, contReceipt.Execution.ExecutionID); settled.State != "completed" {
		t.Fatalf("continue execution settled = %+v", settled)
	}
	mu.Lock()
	gotContSys := actContinueSystem
	mu.Unlock()
	if gotContSys != gotBotBSys {
		t.Fatalf("Activity continue did not preserve captured prompt verbatim:\ngot=%q\nwant=%q", gotContSys, gotBotBSys)
	}

	if _, err := proc.client.RevokeSkillApproval(ctx, pi.ProductSkillRevokeParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: botB.BotID, SkillID: "ws-append"},
		OperationID:     "cp1-revoke-before-mixed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			OperationID:      "cp1-restore-b-ws1",
			BotID:            botB.BotID,
			ExpectedRevision: updatedBotB.ProfileRevision,
		},
		Profile: pi.ProductBotProfile{
			Name: "BotB", Role: "Specialist", Purpose: "Run specialist skills",
			Instructions: "Original BotB instructions.", Workspace: wsB1, ToolPreset: pi.ToolPresetReadonly,
		},
	}); err != nil {
		t.Fatal(err)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-mixed", "DELEGATE_MIXED", nil); settled.State != "completed" {
		t.Fatalf("delegate mixed settled = %+v", settled)
	}
	mixedParked := proc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.Goal == "CP1_MIXED_WAITERS" && ev.State == "waitingForAnswer" &&
			len(ev.OpenQuestionIDs) == 1 && len(ev.PendingApprovalRequestIDs) == 1
	})
	mixedDetail, err := proc.client.GetActivity(ctx, mixedParked.ActivityID)
	if err != nil || len(mixedDetail.Questions) != 1 || len(mixedDetail.ApprovalRequests) != 1 {
		t.Fatalf("mixed waiters detail = %+v, %v", mixedDetail, err)
	}
	mixedReq := mixedDetail.ApprovalRequests[0]
	mixedQ := mixedDetail.Questions[0]
	deniedCard, err := proc.client.DenySkillApprovalRequest(ctx, pi.ProductSkillDenyApprovalRequestParams{
		RequestID:       mixedReq.RequestID,
		RequestRevision: mixedReq.Revision,
		OperationID:     "cp1-deny-mixed",
		Reason:          "Denied in QA review",
	})
	if err != nil || deniedCard.State != "denied" || deniedCard.Revision != 2 || deniedCard.Reason != "Denied in QA review" {
		t.Fatalf("DenySkillApprovalRequest = %+v, %v", deniedCard, err)
	}
	afterDeny, err := proc.client.GetActivity(ctx, mixedParked.ActivityID)
	if err != nil || afterDeny.State != "waitingForAnswer" || len(afterDeny.OpenQuestionIDs) != 1 || len(afterDeny.PendingApprovalRequestIDs) != 0 {
		t.Fatalf("denying approval card dropped sibling text waiter: %+v, %v", afterDeny, err)
	}
	ansRes, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: mixedParked.ActivityID},
			ExecutionID:           mixedQ.ExecutionID,
		},
		QuestionID:       mixedQ.QuestionID,
		QuestionRevision: mixedQ.Revision,
		OperationID:      "cp1-answer-mixed",
		ProductText:      pi.ProductText{Text: "Sibling answer preserved", Materials: []pi.ProductMaterial{}},
	})
	if err != nil || !ansRes.AnswerSaved {
		t.Fatalf("AnswerQuestion(mixed) = %+v, %v", ansRes, err)
	}
	if settled := proc.waitSettled(t, ctx, mixedQ.ExecutionID); settled.State != "completed" {
		t.Fatalf("mixed execution settled = %+v", settled)
	}
	mixedFinal, err := proc.client.GetActivity(ctx, mixedParked.ActivityID)
	if err != nil || mixedFinal.State != "needsAttention" {
		t.Fatalf("denied approval card must keep Activity needsAttention: %+v, %v", mixedFinal, err)
	}
	mu.Lock()
	gotDeniedTool := mixedDeniedToolJSON
	gotAnswerTool := mixedAnswerToolJSON
	mu.Unlock()
	if !strings.Contains(gotDeniedTool, `"approval_denied"`) || !strings.Contains(gotAnswerTool, "Sibling answer preserved") {
		t.Fatalf("mixed tool results mismatch: denied=%s answer=%s", gotDeniedTool, gotAnswerTool)
	}
	if cp1AuditLineCount(t, filepath.Join(wsB1, "audit.log")) != 1 {
		t.Fatalf("denied skill spawned code: audit lines=%d", cp1AuditLineCount(t, filepath.Join(wsB1, "audit.log")))
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-stop", "DELEGATE_STOP", nil); settled.State != "completed" {
		t.Fatalf("delegate stop settled = %+v", settled)
	}
	stopParked := proc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.Goal == "CP1_STOP_APPROVAL" && ev.State == "waitingForAnswer" && len(ev.PendingApprovalRequestIDs) == 1
	})
	stopReq := stopParked.ApprovalRequests[0]
	if _, err := proc.client.StopActivity(ctx, pi.ProductActivityStopParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: stopReq.ActivityID},
			ExecutionID:           stopReq.ExecutionID,
		},
		OperationID: "cp1-stop-card",
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, stopReq.ExecutionID); settled.State != "interrupted" {
		t.Fatalf("stopped approval execution = %+v", settled)
	}
	stopDetail, err := proc.client.GetActivity(ctx, stopReq.ActivityID)
	if err != nil || stopDetail.State != "needsAttention" || len(stopDetail.ApprovalRequests) != 1 || stopDetail.ApprovalRequests[0].State != "cancelled" {
		t.Fatalf("stopped approval card state = %+v, %v", stopDetail, err)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-mutate", "DELEGATE_MUTATE", nil); settled.State != "completed" {
		t.Fatalf("delegate mutate settled = %+v", settled)
	}
	mutateParked := proc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.Goal == "CP1_MUTATE_APPROVAL" && ev.State == "waitingForAnswer" && len(ev.PendingApprovalRequestIDs) == 1
	})
	mutateReq := mutateParked.ApprovalRequests[0]
	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: botB.BotID, SkillID: "ws-append"},
		Name:            "Workspace Append",
		Description:     "Mutated while card open",
		Content:         "Mutated content.",
		OperationID:     "cp1-save-append-v2",
		Manifest: &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"append": {
					Entry:       "scripts/append.mjs",
					Description: "Append audit line v2",
					Permissions: pi.ProductSkillPermissions{WorkspaceRead: true, WorkspaceWrite: true},
				},
			},
		},
		Scripts: map[string]string{"scripts/append.mjs": appendScriptV2},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:    pi.ProductSkillKey{Source: mutateReq.Source, BotID: mutateReq.BotID, SkillID: mutateReq.SkillID},
		ApprovalDigest:     mutateReq.ApprovalDigest,
		PermissionsDigest:  mutateReq.PermissionsDigest,
		RequestID:          mutateReq.RequestID,
		RequestRevision:    mutateReq.Revision,
		CanonicalWorkspace: mutateReq.CanonicalWorkspace,
		InstallationID:     mutateReq.InstallationID,
		OperationID:        "cp1-approve-after-mutate",
	})
	cp1AssertRemoteCode(t, err, "approval_expired")
	if settled := proc.waitSettled(t, ctx, mutateReq.ExecutionID); settled.State != "completed" {
		t.Fatalf("mutated execution settled = %+v", settled)
	}
	mutateDetail, err := proc.client.GetActivity(ctx, mutateReq.ActivityID)
	if err != nil || mutateDetail.State != "needsAttention" || len(mutateDetail.ApprovalRequests) != 1 || mutateDetail.ApprovalRequests[0].State != "expired" {
		t.Fatalf("mutated bundle approval card = %+v, %v", mutateDetail, err)
	}
	if cp1AuditLineCount(t, filepath.Join(wsB1, "audit.log")) != 1 {
		t.Fatalf("mutated bundle approval spawned code: audit lines=%d", cp1AuditLineCount(t, filepath.Join(wsB1, "audit.log")))
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-crash", "DELEGATE_CRASH", nil); settled.State != "completed" {
		t.Fatalf("delegate crash settled = %+v", settled)
	}
	crashParked := proc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.Goal == "CP1_CRASH_APPROVAL" && ev.State == "waitingForAnswer" && len(ev.PendingApprovalRequestIDs) == 1
	})
	crashReq := crashParked.ApprovalRequests[0]
	killOutcome, killErr := proc.client.Kill(ctx)
	proc.exit(t, ctx, killOutcome, false, killErr)

	opts.FreshProfile = false
	proc2 := startCP1Process(t, opts)
	_ = proc2.open(t, ctx)
	recovered, err := proc2.client.GetActivity(ctx, crashReq.ActivityID)
	if err != nil || recovered.State != "needsAttention" || len(recovered.Executions) != 1 || recovered.Executions[0].State != "unknown" ||
		len(recovered.ApprovalRequests) != 1 || recovered.ApprovalRequests[0].State != "open" || recovered.ApprovalRequests[0].Revision != 1 {
		t.Fatalf("recovered crashed approval activity = %+v, %v", recovered, err)
	}
	_, err = proc2.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:    pi.ProductSkillKey{Source: crashReq.Source, BotID: crashReq.BotID, SkillID: crashReq.SkillID},
		ApprovalDigest:     crashReq.ApprovalDigest,
		PermissionsDigest:  crashReq.PermissionsDigest,
		RequestID:          crashReq.RequestID,
		RequestRevision:    crashReq.Revision,
		CanonicalWorkspace: crashReq.CanonicalWorkspace,
		InstallationID:     crashReq.InstallationID,
		OperationID:        "cp1-approve-after-crash",
	})
	cp1AssertRemoteCode(t, err, "approval_expired")
	expiredDetail, err := proc2.client.GetActivity(ctx, crashReq.ActivityID)
	if err != nil || len(expiredDetail.ApprovalRequests) != 1 || expiredDetail.ApprovalRequests[0].State != "expired" || expiredDetail.ApprovalRequests[0].Revision != 2 {
		t.Fatalf("stale approval after crash did not expire cleanly: %+v, %v", expiredDetail, err)
	}
	if cp1AuditLineCount(t, filepath.Join(wsB1, "audit.log")) != 1 {
		t.Fatalf("stale approval after worker loss spawned code: audit lines=%d", cp1AuditLineCount(t, filepath.Join(wsB1, "audit.log")))
	}
	proc2.shutdown(t, ctx)

	t.Setenv("NAGI_FORCE_SANDBOX_UNAVAILABLE", "1")
	unavailRoot := t.TempDir()
	var unavailToolJSON string
	var unavailPhase int
	var unavailBotID string
	unavailProvider := newCP1Provider(t, providerSecret, func(req cp1Request) cp1Reply {
		mu.Lock()
		defer mu.Unlock()
		if !slices.Contains(req.Tools, "activity_ask") {
			if req.LastRole == "user" {
				args, _ := json.Marshal(map[string]any{"key": "unavail-1", "goal": "CP1_SANDBOX_UNAVAILABLE", "assignee_id": unavailBotID})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "del-unavail", Name: "activity_delegate", Args: string(args)}}}
			}
			return cp1Reply{Text: "Delegated"}
		}
		unavailPhase++
		if unavailPhase == 1 {
			return cp1Reply{Calls: []cp1ToolCall{{ID: "unavail-exec", Name: "skill_exec", Args: `{"skill_id":"ws-append","source":"private","script":"append","input":{"line":"nope"}}`}}}
		}
		unavailToolJSON = req.ToolResults["unavail-exec"]
		return cp1Reply{Text: "Sandbox unavailable handled"}
	})
	unavailOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(unavailRoot, "pi"),
		ProfilePath:     filepath.Join(unavailRoot, "contacts.json"),
		CredentialsPath: filepath.Join(unavailRoot, "connection.json"),
		FreshProfile:    true,
	}
	unavailProc := startCP1Process(t, unavailOpts)
	_ = unavailProc.open(t, ctx)
	cp1ConfigureConnection(t, ctx, unavailProc.client, unavailProvider.server.URL+"/v1", providerSecret, wsB1)
	uBot, err := unavailProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp1-unavail-bot",
		Profile: pi.ProductBotProfile{
			Name: "BotUnavail", Role: "Worker", Purpose: "Test sandbox unavailable",
			Instructions: "Execute skill.", Workspace: wsB1, ToolPreset: pi.ToolPresetReadonly,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	unavailBotID = uBot.BotID
	mu.Unlock()
	if _, err := unavailProc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: uBot.BotID, SkillID: "ws-append"},
		Name:            "Workspace Append",
		Description:     "Append line",
		Content:         "Append.",
		OperationID:     "cp1-unavail-skill",
		Manifest: &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"append": {Entry: "scripts/append.mjs", Description: "Append", Permissions: pi.ProductSkillPermissions{WorkspaceWrite: true}},
			},
		},
		Scripts: map[string]string{"scripts/append.mjs": appendScriptV1},
	}); err != nil {
		t.Fatal(err)
	}
	if settled := cp1SendTurn(t, ctx, unavailProc, uBot.CurrentConversationID, "unavail-del", "DELEGATE_UNAVAIL", nil); settled.State != "completed" {
		t.Fatalf("delegate unavail settled = %+v", settled)
	}
	unavailSettled := unavailProc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.Goal == "CP1_SANDBOX_UNAVAILABLE" && ev.State == "needsAttention"
	})
	unavailDetail, err := unavailProc.client.GetActivity(ctx, unavailSettled.ActivityID)
	if err != nil || len(unavailDetail.ApprovalRequests) != 0 {
		t.Fatalf("sandbox_unavailable must not create an approval card: %+v, %v", unavailDetail, err)
	}
	mu.Lock()
	gotUnavail := unavailToolJSON
	mu.Unlock()
	if !strings.Contains(gotUnavail, `"sandbox_unavailable"`) {
		t.Fatalf("expected sandbox_unavailable tool error, got %s", gotUnavail)
	}
	unavailProc.shutdown(t, ctx)

	provider.assertNoProblems()
	unavailProvider.assertNoProblems()
}

func TestPiProductCP1ProgressBoundedInspectionOverflowAndResultRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Setenv("NAGI_PROGRESS_MIN_INTERVAL_MS", "60000")
	nodePath, workerPath := cp1WorkerPaths(t)
	root := t.TempDir()
	ws, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	const activeSecret = "cp1-inspect-active-secret-8821"
	const retiredSecret = "cp1-inspect-retired-secret-9932"

	giantMaterial := strings.Repeat("MaterialBlock-0123456789-", 1000)
	livePartialText := "LivePartialHeader " + retiredSecret + " " + strings.Repeat("中文Live部分🚀-", 600)
	var resultBuilder strings.Builder
	resultBuilder.WriteString("Line1: summary " + retiredSecret + "\n")
	resultBuilder.WriteString("Line2: 中文字段 alpha 🚀\n")
	resultBuilder.WriteString("Line3: second line\n")
	resultBuilder.WriteString("Line4: fourth line\n")
	for i := 5; i <= 320; i++ {
		fmt.Fprintf(&resultBuilder, "Line%d: multibyte-数据-🚀-payload-%04d %s\n", i, i, retiredSecret)
	}
	giantRawResult := resultBuilder.String()

	profilePath := filepath.Join(root, "contacts.json")
	var mu sync.Mutex
	var bigActID, bigExecID, otherActID string
	var actBigPhase int
	var progress1Result, progressNoiseResult, progressBadResult, progress2Result string
	var mirrorHashAfterP1, mirrorHashAfterP2 string
	var mirrorMtimeAfterP1, mirrorMtimeAfterP2 time.Time
	var liveGetJSON, liveOverflowReadText string
	var settledGetROJSON, settledOverflowReadText string
	var settledGetNoneJSON string
	var listPage1JSON, listPage2JSON string
	var lineSliceJSON, badMutexJSON, badBoundaryJSON string
	var pagedText strings.Builder
	var pagedFiles []map[string]string
	var linkedAllowedJSON, linkedDeniedJSON string
	var spillPaths []string
	var importedCursorAfterReplaceJSON, importedFreshJSON string
	var importedActID, importedExecID string

	provider := newCP1Provider(t, activeSecret, func(req cp1Request) cp1Reply {
		mu.Lock()
		defer mu.Unlock()
		isActivity := slices.Contains(req.Tools, "activity_ask")
		if isActivity {
			switch {
			case strings.Contains(req.UserText, "CP1_GIANT_ACTIVITY"):
				actBigPhase++
				switch actBigPhase {
				case 1:
					return cp1Reply{Calls: []cp1ToolCall{{ID: "prog-1", Name: "activity_progress", Args: `{"stage":"Phase one","step":1,"total_steps":3}`}}}
				case 2:
					progress1Result = req.ToolResults["prog-1"]
					mirrorHashAfterP1 = cp1FileSHA256(t, profilePath)
					if info, statErr := os.Stat(profilePath); statErr == nil {
						mirrorMtimeAfterP1 = info.ModTime()
					}
					return cp1Reply{Calls: []cp1ToolCall{{ID: "prog-noise", Name: "activity_progress", Args: `{"stage":"Noisy repeat","step":1,"total_steps":3}`}}}
				case 3:
					progressNoiseResult = req.ToolResults["prog-noise"]
					return cp1Reply{Calls: []cp1ToolCall{{ID: "prog-bad", Name: "activity_progress", Args: `{"stage":"   ","step":5,"total_steps":2}`}}}
				case 4:
					progressBadResult = req.ToolResults["prog-bad"]
					return cp1Reply{Calls: []cp1ToolCall{{ID: "prog-2", Name: "activity_progress", Args: `{"stage":"Phase two","step":2,"total_steps":3}`}}}
				case 5:
					progress2Result = req.ToolResults["prog-2"]
					mirrorHashAfterP2 = cp1FileSHA256(t, profilePath)
					if info, statErr := os.Stat(profilePath); statErr == nil {
						mirrorMtimeAfterP2 = info.ModTime()
					}
					return cp1Reply{Calls: []cp1ToolCall{{ID: "write-confirmed", Name: "write", Args: `{"path":"report.txt","content":"confirmed file body\n"}`}}}
				case 6:
					return cp1Reply{
						Text:  livePartialText,
						Calls: []cp1ToolCall{{ID: "hold-live-partial", Name: "activity_ask", Args: `{"text":"Inspect live partial now?"}`}},
					}
				default:
					return cp1Reply{Text: giantRawResult}
				}
			case strings.Contains(req.UserText, "CP1_OTHER_ACTIVITY"):
				return cp1Reply{Text: "Other activity private body"}
			case strings.Contains(req.UserText, "CP1_LINKED_ACTIVITY"):
				if req.LastRole == "user" {
					args1, _ := json.Marshal(map[string]any{"activity_id": bigActID, "execution_id": bigExecID, "start_line": 1, "end_line": 2, "max_bytes": 2048})
					args2, _ := json.Marshal(map[string]any{"activity_id": otherActID, "execution_id": bigExecID, "start_line": 1, "end_line": 1, "max_bytes": 2048})
					return cp1Reply{Calls: []cp1ToolCall{
						{ID: "link-allow", Name: "activity_result_read", Args: string(args1)},
						{ID: "link-deny", Name: "activity_result_read", Args: string(args2)},
					}}
				}
				linkedAllowedJSON = req.ToolResults["link-allow"]
				linkedDeniedJSON = req.ToolResults["link-deny"]
				return cp1Reply{Text: "Linked check done"}
			default:
				return cp1Reply{Text: "OK"}
			}
		}

		switch {
		case strings.HasPrefix(req.UserText, "DELEGATE_GIANT"):
			if req.LastRole == "user" {
				return cp1Reply{Calls: []cp1ToolCall{{ID: "del-giant", Name: "activity_delegate", Args: `{"key":"giant-1","goal":"CP1_GIANT_ACTIVITY","requirements":["Keep bounded"]}`}}}
			}
			return cp1Reply{Text: "Delegated giant"}
		case req.UserText == "DELEGATE_OTHER":
			if req.LastRole == "user" {
				return cp1Reply{Calls: []cp1ToolCall{{ID: "del-other", Name: "activity_delegate", Args: `{"key":"other-1","goal":"CP1_OTHER_ACTIVITY"}`}}}
			}
			return cp1Reply{Text: "Delegated other"}
		case req.UserText == "DELEGATE_LINKED":
			if req.LastRole == "user" {
				args, _ := json.Marshal(map[string]any{
					"key":                 "linked-1",
					"goal":                "CP1_LINKED_ACTIVITY",
					"linked_activity_ids": []string{bigActID},
				})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "del-linked", Name: "activity_delegate", Args: string(args)}}}
			}
			return cp1Reply{Text: "Delegated linked"}
		case req.UserText == "INSPECT_LIVE_PARTIAL":
			if req.LastRole == "user" {
				args, _ := json.Marshal(map[string]any{"activity_id": bigActID, "max_bytes": 2048})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "get-live", Name: "activity_get", Args: string(args)}}}
			}
			if req.LastToolID == "get-live" {
				liveGetJSON = req.LastContent
				var parsed struct {
					Executions []struct {
						Partial *struct {
							OverflowPath string `json:"overflowPath"`
						} `json:"partial"`
					} `json:"executions"`
				}
				if err := json.Unmarshal([]byte(liveGetJSON), &parsed); err == nil && len(parsed.Executions) > 0 && parsed.Executions[0].Partial != nil && parsed.Executions[0].Partial.OverflowPath != "" {
					path := parsed.Executions[0].Partial.OverflowPath
					spillPaths = append(spillPaths, path)
					args, _ := json.Marshal(map[string]any{"path": path, "offset": 1, "limit": 5})
					return cp1Reply{Calls: []cp1ToolCall{{ID: "read-live-spill", Name: "read", Args: string(args)}}}
				}
				return cp1Reply{Text: "Missing live overflowPath"}
			}
			liveOverflowReadText = req.LastContent
			return cp1Reply{Text: "Live partial inspected"}
		case req.UserText == "INSPECT_SETTLED_RO":
			switch req.LastToolID {
			case "":
				return cp1Reply{Calls: []cp1ToolCall{{ID: "list-p1", Name: "activity_list", Args: `{"limit":1,"max_bytes":2048}`}}}
			case "list-p1":
				listPage1JSON = req.LastContent
				var page struct {
					NextCursor *string `json:"nextCursor"`
				}
				_ = json.Unmarshal([]byte(listPage1JSON), &page)
				if page.NextCursor != nil {
					args, _ := json.Marshal(map[string]any{"cursor": *page.NextCursor, "limit": 5, "max_bytes": 2048})
					return cp1Reply{Calls: []cp1ToolCall{{ID: "list-p2", Name: "activity_list", Args: string(args)}}}
				}
				args, _ := json.Marshal(map[string]any{"activity_id": bigActID, "max_bytes": 2048})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "get-settled-ro", Name: "activity_get", Args: string(args)}}}
			case "list-p2":
				listPage2JSON = req.LastContent
				args, _ := json.Marshal(map[string]any{"activity_id": bigActID, "max_bytes": 2048})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "get-settled-ro", Name: "activity_get", Args: string(args)}}}
			case "get-settled-ro":
				settledGetROJSON = req.LastContent
				var parsed struct {
					Results []struct {
						OverflowPath string `json:"overflowPath"`
					} `json:"results"`
				}
				if err := json.Unmarshal([]byte(settledGetROJSON), &parsed); err == nil && len(parsed.Results) > 0 && parsed.Results[0].OverflowPath != "" {
					path := parsed.Results[0].OverflowPath
					spillPaths = append(spillPaths, path)
					args, _ := json.Marshal(map[string]any{"path": path, "offset": 1, "limit": 10})
					return cp1Reply{Calls: []cp1ToolCall{{ID: "read-settled-spill", Name: "read", Args: string(args)}}}
				}
				return cp1Reply{Text: "Missing settled overflowPath"}
			default:
				settledOverflowReadText = req.LastContent
				return cp1Reply{Text: "RO settled inspection done"}
			}
		case req.UserText == "INSPECT_SETTLED_NONE":
			switch {
			case req.LastRole == "user":
				args, _ := json.Marshal(map[string]any{"activity_id": bigActID, "max_bytes": 2048})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "get-settled-none", Name: "activity_get", Args: string(args)}}}
			case req.LastToolID == "get-settled-none":
				settledGetNoneJSON = req.LastContent
				args, _ := json.Marshal(map[string]any{"activity_id": bigActID, "execution_id": bigExecID, "start_line": 2, "end_line": 4, "max_bytes": 2048})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "rr-lines", Name: "activity_result_read", Args: string(args)}}}
			case req.LastToolID == "rr-lines":
				lineSliceJSON = req.LastContent
				args, _ := json.Marshal(map[string]any{"activity_id": bigActID, "execution_id": bigExecID, "start_line": 1, "byte_offset": 0})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "rr-bad-mutex", Name: "activity_result_read", Args: string(args)}}}
			case req.LastToolID == "rr-bad-mutex":
				badMutexJSON = req.LastContent
				redactedLine1Len := len("Line1: summary [credential]\nLine2: ")
				args, _ := json.Marshal(map[string]any{"activity_id": bigActID, "execution_id": bigExecID, "byte_offset": redactedLine1Len + 1})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "rr-bad-boundary", Name: "activity_result_read", Args: string(args)}}}
			case req.LastToolID == "rr-bad-boundary":
				badBoundaryJSON = req.LastContent
				args, _ := json.Marshal(map[string]any{"activity_id": bigActID, "execution_id": bigExecID, "byte_offset": 0, "max_bytes": 2048})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "rr-page-0", Name: "activity_result_read", Args: string(args)}}}
			case strings.HasPrefix(req.LastToolID, "rr-page-"):
				if len(req.LastContent) > 2048 {
					return cp1Reply{Text: fmt.Sprintf("activity_result_read page exceeded 2048 bytes: %d", len(req.LastContent))}
				}
				var page struct {
					Text       string              `json:"text"`
					Files      []map[string]string `json:"files"`
					NextCursor *string             `json:"nextCursor"`
				}
				if err := json.Unmarshal([]byte(req.LastContent), &page); err != nil {
					return cp1Reply{Text: "decode page error: " + err.Error()}
				}
				if !utf8.ValidString(page.Text) {
					return cp1Reply{Text: "invalid UTF-8 page"}
				}
				pagedText.WriteString(page.Text)
				pagedFiles = append(pagedFiles, page.Files...)
				if page.NextCursor != nil {
					args, _ := json.Marshal(map[string]any{"activity_id": bigActID, "execution_id": bigExecID, "cursor": *page.NextCursor, "max_bytes": 2048})
					return cp1Reply{Calls: []cp1ToolCall{{ID: fmt.Sprintf("rr-page-%d", pagedText.Len()), Name: "activity_result_read", Args: string(args)}}}
				}
				return cp1Reply{Text: "None paging done"}
			default:
				return cp1Reply{Text: "Done"}
			}
		default:
			return cp1Reply{Text: "OK"}
		}
	})
	provider.addForbiddenSecret(activeSecret)

	opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(root, "pi"),
		ProfilePath:     profilePath,
		CredentialsPath: filepath.Join(root, "connection.json"),
		FreshProfile:    true,
	}
	proc := startCP1Process(t, opts)
	_ = proc.open(t, ctx)
	cp1ConfigureConnection(t, ctx, proc.client, provider.server.URL+"/v1", retiredSecret, ws)
	cp1ConfigureConnection(t, ctx, proc.client, provider.server.URL+"/v1", activeSecret, ws)

	botRO, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp1-create-ro",
		Profile: pi.ProductBotProfile{
			Name: "BotRO", Role: "Reviewer", Purpose: "Inspect with workspace tools",
			Instructions: "Use workspace tools.", Workspace: ws, ToolPreset: pi.ToolPresetWorkspace,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	botNone, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp1-create-none",
		Profile: pi.ProductBotProfile{
			Name: "BotNone", Role: "Writer", Purpose: "No coding tools",
			Instructions: "No filesystem tools.", Workspace: ws, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if settled := cp1SendTurn(t, ctx, proc, botRO.CurrentConversationID, "del-giant", "DELEGATE_GIANT", []pi.ProductMaterial{
		{ID: "giant-mat-1", Revision: 1, Name: "Giant Material", Text: giantMaterial},
	}); settled.State != "completed" {
		t.Fatalf("delegate giant settled = %+v", settled)
	}

	parkedLive := proc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.Goal == "CP1_GIANT_ACTIVITY" && ev.State == "waitingForAnswer" && len(ev.OpenQuestionIDs) == 1
	})
	changes := proc.activityChangesFor(parkedLive.ActivityID)
	var runningProgressChanges []pi.ProductActivityChange
	for _, ch := range changes {
		if ch.State == "running" && ch.LatestProgress != nil {
			runningProgressChanges = append(runningProgressChanges, ch)
		}
	}
	if len(runningProgressChanges) != 2 ||
		runningProgressChanges[0].LatestProgress.ProgressSeq != 1 ||
		runningProgressChanges[1].LatestProgress.ProgressSeq != 2 ||
		runningProgressChanges[1].LatestProgress.Stage != "Phase two" ||
		runningProgressChanges[1].LatestProgress.Step != 2 ||
		runningProgressChanges[1].LatestProgress.TotalSteps != 3 ||
		runningProgressChanges[0].Revision != runningProgressChanges[1].Revision {
		t.Fatalf("unexpected progress events or revision drift: %+v", runningProgressChanges)
	}

	mu.Lock()
	bigActID = parkedLive.ActivityID
	gotP1 := progress1Result
	gotPNoise := progressNoiseResult
	gotPBad := progressBadResult
	gotP2 := progress2Result
	h1, h2 := mirrorHashAfterP1, mirrorHashAfterP2
	mt1, mt2 := mirrorMtimeAfterP1, mirrorMtimeAfterP2
	mu.Unlock()
	if !strings.Contains(gotP1, `"recorded":true`) || !strings.Contains(gotPNoise, `"recorded":false`) ||
		!strings.Contains(gotPBad, `"invalid_params"`) || !strings.Contains(gotP2, `"recorded":true`) {
		t.Fatalf("progress tool results mismatch: p1=%s noise=%s bad=%s p2=%s", gotP1, gotPNoise, gotPBad, gotP2)
	}
	if h1 == "" || h1 != h2 || !mt1.Equal(mt2) {
		t.Fatalf("activity_progress rewrote contacts.json mirror: hash1=%s hash2=%s mtime1=%v mtime2=%v", h1, h2, mt1, mt2)
	}

	liveDetail, err := proc.client.GetActivity(ctx, bigActID)
	if err != nil || len(liveDetail.Executions) != 1 || len(liveDetail.Questions) != 1 {
		t.Fatalf("live giant detail = %+v, %v", liveDetail, err)
	}
	mu.Lock()
	bigExecID = liveDetail.Executions[0].ExecutionID
	mu.Unlock()

	dbPath := filepath.Join(root, "nagi.db")
	progAdmission := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const classes = db.prepare("SELECT DISTINCT tool_class FROM execution_mutation_admissions WHERE execution_id = ? AND tool_name = 'activity_progress'").all(JSON.stringify(process.argv[2]));
const kinds = db.prepare("SELECT DISTINCT kind FROM activity_traces WHERE execution_id = ?").all(JSON.stringify(process.argv[2])).map(r => r.kind).sort();
db.close();
console.log(JSON.stringify({ classes, kinds }));
`, bigExecID)
	if !strings.Contains(progAdmission, `"tool_class":"readonly"`) {
		t.Fatalf("activity_progress was not classified readonly: %s", progAdmission)
	}

	if settled := cp1SendTurn(t, ctx, proc, botRO.CurrentConversationID, "inspect-live", "INSPECT_LIVE_PARTIAL", nil); settled.State != "completed" {
		t.Fatalf("inspect live partial settled = %+v", settled)
	}
	mu.Lock()
	gotLiveGet := liveGetJSON
	gotLiveSpill := liveOverflowReadText
	mu.Unlock()
	if len(gotLiveGet) == 0 || len(gotLiveGet) > 2048 || strings.Contains(gotLiveGet, retiredSecret) {
		t.Fatalf("live activity_get violated budget/redaction: len=%d", len(gotLiveGet))
	}
	if !strings.Contains(gotLiveSpill, "LivePartialHeader [credential]") || strings.Contains(gotLiveSpill, retiredSecret) {
		t.Fatalf("live partial overflow read failed redaction/content check: %q", gotLiveSpill)
	}

	qLive := liveDetail.Questions[0]
	if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: bigActID},
			ExecutionID:           bigExecID,
		},
		QuestionID:       qLive.QuestionID,
		QuestionRevision: qLive.Revision,
		OperationID:      "cp1-release-giant",
		ProductText:      pi.ProductText{Text: "Proceed to final result", Materials: []pi.ProductMaterial{}},
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, bigExecID); settled.State != "completed" {
		t.Fatalf("giant execution settled = %+v", settled)
	}

	if settled := cp1SendTurn(t, ctx, proc, botNone.CurrentConversationID, "del-other", "DELEGATE_OTHER", nil); settled.State != "completed" {
		t.Fatalf("delegate other settled = %+v", settled)
	}
	otherDone := proc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.Goal == "CP1_OTHER_ACTIVITY" && ev.State == "completed"
	})
	mu.Lock()
	otherActID = otherDone.ActivityID
	mu.Unlock()

	if settled := cp1SendTurn(t, ctx, proc, botRO.CurrentConversationID, "del-linked", "DELEGATE_LINKED", nil); settled.State != "completed" {
		t.Fatalf("delegate linked settled = %+v", settled)
	}
	linkedDone := proc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.Goal == "CP1_LINKED_ACTIVITY" && ev.State == "completed"
	})
	if linkedDone.ActivityID == "" {
		t.Fatal("missing linked activity ID")
	}
	mu.Lock()
	gotLinkAllow := linkedAllowedJSON
	gotLinkDeny := linkedDeniedJSON
	mu.Unlock()
	if !strings.Contains(gotLinkAllow, "Line1: summary [credential]") || !strings.Contains(gotLinkDeny, `"scope_denied"`) {
		t.Fatalf("Activity linked activity_result_read ACL failed: allow=%s deny=%s", gotLinkAllow, gotLinkDeny)
	}

	if settled := cp1SendTurn(t, ctx, proc, botRO.CurrentConversationID, "inspect-ro", "INSPECT_SETTLED_RO", nil); settled.State != "completed" {
		t.Fatalf("inspect settled RO = %+v", settled)
	}
	mu.Lock()
	gotLP1 := listPage1JSON
	gotLP2 := listPage2JSON
	gotSettledRO := settledGetROJSON
	gotSettledSpill := settledOverflowReadText
	gotSpills := append([]string(nil), spillPaths...)
	mu.Unlock()

	for label, payload := range map[string]string{"list-p1": gotLP1, "list-p2": gotLP2, "get-ro": gotSettledRO} {
		if len(payload) == 0 || len(payload) > 2048 || strings.Contains(payload, retiredSecret) {
			t.Fatalf("%s violated 2048 byte budget or leaked secret: len=%d", label, len(payload))
		}
	}
	if !strings.Contains(gotSettledSpill, "Line1: summary [credential]") || !strings.Contains(gotSettledSpill, "Line2: 中文字段 alpha 🚀") || strings.Contains(gotSettledSpill, retiredSecret) {
		t.Fatalf("settled spill file read failed: %q", gotSettledSpill)
	}
	if len(gotSpills) < 2 {
		t.Fatalf("expected live and settled spill paths, got %v", gotSpills)
	}
	canonicalStateDir, err := filepath.EvalSymlinks(opts.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	overflowDir := filepath.Join(canonicalStateDir, "overflow")
	dirInfo, err := os.Stat(overflowDir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("overflow dir mode = %v, %v, want 0700", dirInfo, err)
	}
	for _, sp := range gotSpills {
		if !strings.HasPrefix(sp, overflowDir+string(os.PathSeparator)) {
			t.Fatalf("spill path %q outside %q", sp, overflowDir)
		}
		info, statErr := os.Stat(sp)
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("spill file %q mode = %v, %v, want 0600", sp, info, statErr)
		}
		if bytes.Contains(productRead(t, sp), []byte(retiredSecret)) {
			t.Fatalf("spill file %q leaked secret", sp)
		}
	}

	if _, err := proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			OperationID:      "cp1-rebind-ro-to-none",
			BotID:            botRO.BotID,
			ExpectedRevision: botRO.ProfileRevision,
		},
		Profile: pi.ProductBotProfile{
			Name: "BotRO", Role: "Reviewer", Purpose: "Now none preset",
			Instructions: "No coding tools.", Workspace: ws, ToolPreset: pi.ToolPresetNone,
		},
		StartNewConversation: true,
	}); err != nil {
		t.Fatal(err)
	}
	botsPage, err := proc.client.ListBots(ctx, pi.ProductPageParams{})
	if err != nil {
		t.Fatal(err)
	}
	var roNewConvID string
	for _, b := range botsPage.Items {
		if b.BotID == botRO.BotID {
			roNewConvID = b.CurrentConversationID
		}
	}

	if settled := cp1SendTurn(t, ctx, proc, roNewConvID, "inspect-none", "INSPECT_SETTLED_NONE", nil); settled.State != "completed" {
		t.Fatalf("inspect settled none = %+v", settled)
	}
	mu.Lock()
	gotSettledNone := settledGetNoneJSON
	gotLineSlice := lineSliceJSON
	gotBadMutex := badMutexJSON
	gotBadBoundary := badBoundaryJSON
	gotPagedText := pagedText.String()
	gotPagedFiles := append([]map[string]string(nil), pagedFiles...)
	mu.Unlock()

	if len(gotSettledNone) == 0 || len(gotSettledNone) > 2048 || strings.Contains(gotSettledNone, "overflowPath") {
		t.Fatalf("toolPreset=none activity_get exposed overflowPath or exceeded 2048 bytes: len=%d payload=%s", len(gotSettledNone), gotSettledNone)
	}
	var linePage struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(gotLineSlice), &linePage); err != nil {
		t.Fatalf("decode line slice: %v", err)
	}
	wantLines := "Line2: 中文字段 alpha 🚀\nLine3: second line\nLine4: fourth line\n"
	if linePage.Text != wantLines {
		t.Fatalf("activity_result_read line slice = %q, want %q", linePage.Text, wantLines)
	}
	if !strings.Contains(gotBadMutex, `"invalid_params"`) || !strings.Contains(gotBadBoundary, `"invalid_params"`) {
		t.Fatalf("activity_result_read validation errors missing: mutex=%s boundary=%s", gotBadMutex, gotBadBoundary)
	}
	wantFullRedacted := strings.ReplaceAll(giantRawResult, retiredSecret, "[credential]")
	if gotPagedText != wantFullRedacted {
		t.Fatalf("cursor-paged result text mismatch: got len=%d want len=%d", len(gotPagedText), len(wantFullRedacted))
	}
	if len(gotPagedFiles) != 1 || gotPagedFiles[0]["path"] != filepath.Join(ws, "report.txt") || gotPagedFiles[0]["callId"] != "write-confirmed" {
		t.Fatalf("cursor-paged confirmed files mismatch: %+v", gotPagedFiles)
	}

	uiResult, err := proc.client.ActivityResult(ctx, pi.ProductActivityExecutionParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: bigActID},
		ExecutionID:           bigExecID,
	})
	if err != nil || uiResult.Text != wantFullRedacted {
		t.Fatalf("UI ActivityResult mismatch: err=%v len=%d", err, len(uiResult.Text))
	}
	afterReads, err := proc.client.GetActivity(ctx, bigActID)
	if err != nil || afterReads.AcknowledgedAt != "" || afterReads.AcknowledgedExecutionID != "" {
		t.Fatalf("read tools must never acknowledge Activity: %+v, %v", afterReads, err)
	}

	proc.shutdown(t, ctx)

	for _, sp := range gotSpills {
		if _, err := os.Stat(sp); !os.IsNotExist(err) {
			t.Fatalf("spill file %q was not reaped on clean shutdown: err=%v", sp, err)
		}
	}
	if _, err := os.Stat(overflowDir); !os.IsNotExist(err) {
		t.Fatalf("overflow dir %q was not removed on clean shutdown: err=%v", overflowDir, err)
	}

	impRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	impState := filepath.Join(impRoot, "pi")
	impSessions := filepath.Join(impState, "sessions")
	if err := os.MkdirAll(impSessions, 0o700); err != nil {
		t.Fatal(err)
	}
	impSessionFile := filepath.Join(impSessions, "imported-activity.jsonl")
	type sessionBindingData struct {
		ContextID         string `json:"contextId"`
		BotID             string `json:"botId"`
		Purpose           string `json:"purpose"`
		ActivityID        string `json:"activityId,omitempty"`
		SelectionRevision int    `json:"selectionRevision"`
		CWD               string `json:"cwd"`
	}
	writeImportedSession := func(body string) {
		t.Helper()
		lines := []any{
			map[string]any{"type": "session", "version": 3, "id": "imp-sess", "timestamp": "2026-01-01T00:00:00.000Z", "cwd": ws},
			map[string]any{"type": "custom", "id": "e1", "parentId": nil, "timestamp": "2026-01-01T00:00:01.000Z", "customType": "nagi-context-v1", "data": sessionBindingData{
				ContextID: "ctx-imp-act", BotID: "bot-imp", Purpose: "activity", ActivityID: "act-imp", SelectionRevision: 1, CWD: ws,
			}},
			map[string]any{"type": "custom", "id": "e2", "parentId": "e1", "timestamp": "2026-01-01T00:00:02.000Z", "customType": "nagi-turn-v1", "data": map[string]any{
				"runId": "run-imp-1", "text": "Imported prompt", "correlation": map[string]any{"executionId": "exec-imp-1"},
			}},
			map[string]any{"type": "message", "id": "e3", "parentId": "e2", "timestamp": "2026-01-01T00:00:03.000Z", "message": map[string]any{
				"role": "assistant", "stopReason": "stop", "content": []any{map[string]any{"type": "text", "text": body}},
			}},
		}
		var buf bytes.Buffer
		for _, line := range lines {
			b, _ := json.Marshal(line)
			buf.Write(b)
			buf.WriteByte('\n')
		}
		if err := os.WriteFile(impSessionFile, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeImportedSession(strings.Repeat("IMPORTED_ORIGINAL_CONTENT_", 250))

	impProfile := map[string]any{
		"version": 1,
		"bots": []any{map[string]any{
			"botId": "bot-imp", "profileRevision": 1, "archived": false, "currentConversationId": "conv-imp",
			"profile": map[string]any{"name": "ImpBot", "role": "Reader", "purpose": "Test imported paging", "instructions": "Read.", "workspace": ws, "codingTools": []string{}},
		}},
		"conversations": []any{map[string]any{
			"conversationId": "conv-imp", "botId": "bot-imp", "revision": 1,
			"draft": map[string]any{"revision": 0, "text": "", "materials": []any{}},
		}},
		"activities": []any{map[string]any{
			"activityId": "act-imp", "ownerBotId": "bot-imp", "originConversationId": "conv-imp",
			"revision": 1, "state": "needsAttention", "goal": "Imported activity",
			"requirements": []any{}, "questions": []any{}, "materials": []any{}, "inputFiles": []any{},
			"executionIds": []string{"exec-imp-1"}, "results": []any{},
			"binding": map[string]any{
				"contextId": "ctx-imp-act", "sessionRef": impSessionFile, "connectionRevision": 1,
				"profileRevision": 1, "cwd": ws, "instructions": "Imported", "codingTools": []string{}, "used": true,
			},
		}},
		"executions": []any{map[string]any{
			"executionId": "exec-imp-1", "revision": 1, "state": "completed", "operationId": "op-imp-1",
			"input":       map[string]any{"revision": 0, "text": "Imported prompt", "materials": []any{}},
			"engineRunId": "run-imp-1", "importedActivityId": "act-imp",
			"retained": map[string]any{"id": "exec-imp-1"},
		}},
		"operations": []any{},
		"connections": []any{map[string]any{
			"revision": 1, "sourceId": "src-imp",
			"configuration": map[string]any{
				"wireApi": "openai-completions", "baseUrl": provider.server.URL + "/v1", "model": productModel,
				"contextTokens": 262144, "outputTokens": 4096, "workingDirectory": ws, "maxActiveTasks": 4,
			},
		}},
		"savedRevision": 1, "appliedRevision": 1,
	}
	impOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        impState,
		ProfilePath:     filepath.Join(impRoot, "contacts.json"),
		CredentialsPath: filepath.Join(impRoot, "connection.json"),
	}
	writeProductFixture(t, impOpts.CredentialsPath, map[string]any{
		"version": 2, "credential_source": "src-imp", "model": productModel,
		"context_tokens": 262144, "output_tokens": 4096, "working_directory": ws, "max_active_tasks": 4,
		"sources": map[string]any{"src-imp": map[string]string{"api": "openai-chat-completions", "endpoint": provider.server.URL + "/v1", "api_key": activeSecret}},
	})
	writeProductFixture(t, impOpts.ProfilePath, impProfile)

	mu.Lock()
	importedActID = "act-imp"
	importedExecID = "exec-imp-1"
	mu.Unlock()
	impProc := startCP1Process(t, impOpts)
	_ = impProc.open(t, ctx)

	var savedImportedCursor string
	provider.setHandler(func(req cp1Request) cp1Reply {
		mu.Lock()
		defer mu.Unlock()
		switch req.UserText {
		case "INSPECT_IMPORTED_PAGE1":
			if req.LastRole == "user" {
				args, _ := json.Marshal(map[string]any{"activity_id": importedActID, "execution_id": importedExecID, "byte_offset": 0, "max_bytes": 2048})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "imp-p1", Name: "activity_result_read", Args: string(args)}}}
			}
			var page struct {
				Text       string  `json:"text"`
				NextCursor *string `json:"nextCursor"`
			}
			_ = json.Unmarshal([]byte(req.LastContent), &page)
			if page.NextCursor != nil {
				savedImportedCursor = *page.NextCursor
			}
			return cp1Reply{Text: "Imported page 1 ok"}
		case "INSPECT_IMPORTED_REPLACE":
			if req.LastRole == "user" {
				args, _ := json.Marshal(map[string]any{"activity_id": importedActID, "execution_id": importedExecID, "cursor": savedImportedCursor, "max_bytes": 2048})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "imp-p2-stale", Name: "activity_result_read", Args: string(args)}}}
			}
			if req.LastToolID == "imp-p2-stale" {
				importedCursorAfterReplaceJSON = req.LastContent
				args, _ := json.Marshal(map[string]any{"activity_id": importedActID, "execution_id": importedExecID, "byte_offset": 0, "max_bytes": 2048})
				return cp1Reply{Calls: []cp1ToolCall{{ID: "imp-fresh", Name: "activity_result_read", Args: string(args)}}}
			}
			importedFreshJSON = req.LastContent
			return cp1Reply{Text: "Imported replacement ok"}
		default:
			return cp1Reply{Text: "OK"}
		}
	})

	if settled := cp1SendTurn(t, ctx, impProc, "conv-imp", "imp-turn-1", "INSPECT_IMPORTED_PAGE1", nil); settled.State != "completed" {
		t.Fatalf("imported page 1 turn settled = %+v", settled)
	}
	mu.Lock()
	hasImpCursor := savedImportedCursor != ""
	mu.Unlock()
	if !hasImpCursor {
		t.Fatal("imported activity_result_read did not yield nextCursor")
	}

	writeImportedSession("REPLACED_TRANSCRIPT_BODY_9911")
	if settled := cp1SendTurn(t, ctx, impProc, "conv-imp", "imp-turn-2", "INSPECT_IMPORTED_REPLACE", nil); settled.State != "completed" {
		t.Fatalf("imported replace turn settled = %+v", settled)
	}
	mu.Lock()
	gotStaleImp := importedCursorAfterReplaceJSON
	gotFreshImp := importedFreshJSON
	mu.Unlock()
	if !strings.Contains(gotStaleImp, `"invalid_cursor"`) || !strings.Contains(gotFreshImp, "REPLACED_TRANSCRIPT_BODY_9911") {
		t.Fatalf("imported transcript replacement paging mismatch: stale=%s fresh=%s", gotStaleImp, gotFreshImp)
	}
	impProc.shutdown(t, ctx)
	provider.assertNoProblems()
}

func TestPiProductCP1LifecycleAckCloseV7MigrationPreUpgradePrefixAndBackupRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Setenv("NAGI_PROGRESS_MIN_INTERVAL_MS", "0")
	nodePath, workerPath := cp1WorkerPaths(t)
	root := t.TempDir()
	ws, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	const primarySecret = "cp1-lifecycle-primary-key-3301"
	const preSecret = "cp1-late-saved-secret-7709"

	var mu sync.Mutex
	var lifePhase int
	var crashActPhase int
	var closeInModelResult string
	var targetCloseActID string

	provider := newCP1Provider(t, "", func(req cp1Request) cp1Reply {
		mu.Lock()
		defer mu.Unlock()
		isActivity := slices.Contains(req.Tools, "activity_ask")
		if !isActivity {
			switch req.UserText {
			case "DELEGATE_LIFE":
				if req.LastRole == "user" {
					return cp1Reply{Calls: []cp1ToolCall{{ID: "del-life", Name: "activity_delegate", Args: `{"key":"life-1","goal":"CP1_LIFE_ACTIVITY"}`}}}
				}
				return cp1Reply{Text: "Delegated life"}
			case "DELEGATE_UNKNOWN":
				if req.LastRole == "user" {
					return cp1Reply{Calls: []cp1ToolCall{{ID: "del-unk", Name: "activity_delegate", Args: `{"key":"unk-1","goal":"CP1_UNKNOWN_ACTIVITY"}`}}}
				}
				return cp1Reply{Text: "Delegated unknown"}
			case "MODEL_CLOSE_WITHOUT_REV":
				if req.LastRole == "user" {
					args, _ := json.Marshal(map[string]any{"activity_id": targetCloseActID, "action": "close"})
					return cp1Reply{Calls: []cp1ToolCall{{ID: "close-no-rev", Name: "activity_action", Args: string(args)}}}
				}
				closeInModelResult = req.LastContent
				return cp1Reply{Text: "Close check done"}
			default:
				return cp1Reply{Text: "OK"}
			}
		}

		switch {
		case strings.Contains(req.UserText, "CP1_LIFE_ACTIVITY") && strings.Contains(req.UserText, "RUN_E2"):
			if req.LastRole == "user" {
				args, _ := json.Marshal(map[string]any{"stage": "Stage carrying " + preSecret, "step": 1, "total_steps": 2})
				return cp1Reply{Calls: []cp1ToolCall{
					{ID: "e2-prog", Name: "activity_progress", Args: string(args)},
					{ID: "e2-write", Name: "write", Args: `{"path":"receipt.txt","content":"verified receipt file\n"}`},
				}}
			}
			if _, asked := req.ToolResults["e2-ask"]; !asked {
				return cp1Reply{Calls: []cp1ToolCall{{ID: "e2-ask", Name: "activity_ask", Args: `{"text":"Confirm E2 answer?"}`}}}
			}
			return cp1Reply{Text: "Execution E2 settled cleanly"}
		case strings.Contains(req.UserText, "CP1_LIFE_ACTIVITY") && strings.Contains(req.UserText, "RUN_E4"):
			if req.LastRole == "user" {
				args, _ := json.Marshal(map[string]any{"stage": "Backup stage carrying " + preSecret, "step": 1, "total_steps": 2})
				return cp1Reply{Calls: []cp1ToolCall{
					{ID: "e4-prog", Name: "activity_progress", Args: string(args)},
					{ID: "e4-write", Name: "write", Args: `{"path":"receipt.txt","content":"verified receipt file\n"}`},
				}}
			}
			if _, asked := req.ToolResults["e4-ask"]; !asked {
				return cp1Reply{Calls: []cp1ToolCall{{ID: "e4-ask", Name: "activity_ask", Args: `{"text":"Confirm backup answer?"}`}}}
			}
			return cp1Reply{Text: "Execution E4 settled cleanly"}
		case strings.Contains(req.UserText, "CP1_LIFE_ACTIVITY") && strings.Contains(req.UserText, "RUN_E3"):
			return cp1Reply{Text: "Execution E3 after reopen"}
		case strings.Contains(req.UserText, "CP1_LIFE_ACTIVITY"):
			lifePhase++
			if lifePhase == 1 {
				return cp1Reply{Calls: []cp1ToolCall{{ID: "e1-prog", Name: "activity_progress", Args: `{"stage":"E1 initial stage","step":1,"total_steps":1}`}}}
			}
			return cp1Reply{Text: "Execution E1 complete"}
		case strings.Contains(req.UserText, "CP1_UNKNOWN_ACTIVITY"):
			crashActPhase++
			if crashActPhase == 1 {
				return cp1Reply{Calls: []cp1ToolCall{{ID: "unk-skill", Name: "skill_exec", Args: `{"skill_id":"backup-priv","source":"private","script":"touch","input":{"tag":"x"}}`}}}
			}
			return cp1Reply{Text: "Should not finish"}
		default:
			return cp1Reply{Text: "Done"}
		}
	})

	opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(root, "pi"),
		ProfilePath:     filepath.Join(root, "contacts.json"),
		CredentialsPath: filepath.Join(root, "connection.json"),
		FreshProfile:    true,
	}
	proc := startCP1Process(t, opts)
	_ = proc.open(t, ctx)
	cp1ConfigureConnection(t, ctx, proc.client, provider.server.URL+"/v1", primarySecret, ws)

	bot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp1-life-bot",
		Profile: pi.ProductBotProfile{
			Name: "LifeBot", Role: "Engineer", Purpose: "Lifecycle checks",
			Instructions: "Handle lifecycle cleanly.", Workspace: ws, ToolPreset: pi.ToolPresetWorkspace,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "backup-priv"},
		Name:            "Backup Priv",
		Description:     "Requires approval",
		Content:         "Privileged skill.",
		OperationID:     "cp1-save-backup-priv",
		Manifest: &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"touch": {Entry: "scripts/touch.mjs", Description: "Touch file", Permissions: pi.ProductSkillPermissions{WorkspaceWrite: true}},
			},
		},
		Scripts: map[string]string{
			"scripts/touch.mjs": "import { writeFileSync } from 'node:fs'; import { join } from 'node:path'; export default async function(i, ctx) { writeFileSync(join(ctx.workspace, 'touched.txt'), 'ok'); return { ok: true }; }\n",
		},
	}); err != nil {
		t.Fatal(err)
	}

	if settled := cp1SendTurn(t, ctx, proc, bot.CurrentConversationID, "life-e1", "DELEGATE_LIFE", nil); settled.State != "completed" {
		t.Fatalf("delegate life settled = %+v", settled)
	}
	e1Change := proc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.Goal == "CP1_LIFE_ACTIVITY" && ev.State == "completed"
	})
	actID := e1Change.ActivityID
	actE1, err := proc.client.GetActivity(ctx, actID)
	if err != nil || actE1.State != "completed" || len(actE1.Executions) != 1 || actE1.LatestProgress == nil || actE1.LatestProgress.Stage != "E1 initial stage" {
		t.Fatalf("E1 activity detail = %+v, %v", actE1, err)
	}
	exec1ID := actE1.Executions[0].ExecutionID

	cont2, err := proc.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
		ProductText:           pi.ProductText{Text: "RUN_E2", Materials: []pi.ProductMaterial{}},
		OperationID:           "cp1-life-e2",
		ExpectedRevision:      actE1.Revision,
	})
	if err != nil || cont2.Execution == nil {
		t.Fatalf("ContinueActivity E2 = %+v, %v", cont2, err)
	}
	exec2ID := cont2.Execution.ExecutionID
	e2Parked := proc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.ActivityID == actID && ev.State == "waitingForAnswer" && len(ev.OpenQuestionIDs) == 1
	})

	_, err = proc.client.CloseActivity(ctx, pi.ProductActivityCloseParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
		ExpectedRevision:      e2Parked.Revision,
		OperationID:           "cp1-close-while-waiting",
	})
	cp1AssertRemoteCode(t, err, "busy")

	e2DetailWait, err := proc.client.GetActivity(ctx, actID)
	if err != nil || len(e2DetailWait.Questions) != 1 {
		t.Fatalf("E2 waiting detail = %+v, %v", e2DetailWait, err)
	}
	qE2 := e2DetailWait.Questions[0]
	const answerOpID = "cp1-answer-e2"
	if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
			ExecutionID:           exec2ID,
		},
		QuestionID:       qE2.QuestionID,
		QuestionRevision: qE2.Revision,
		OperationID:      answerOpID,
		ProductText:      pi.ProductText{Text: "Confirmed for backup restore", Materials: []pi.ProductMaterial{}},
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, exec2ID); settled.State != "completed" {
		t.Fatalf("E2 settled = %+v", settled)
	}

	actE2, err := proc.client.GetActivity(ctx, actID)
	if err != nil || actE2.State != "completed" || actE2.LatestProgress == nil || actE2.LatestProgress.ExecutionID != exec2ID || actE2.LatestProgress.ProgressSeq != 1 {
		t.Fatalf("E2 progress did not reset cleanly: %+v, %v", actE2, err)
	}

	_, err = proc.client.AcknowledgeActivity(ctx, pi.ProductActivityAcknowledgeParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
			ExecutionID:           exec1ID,
		},
		ExpectedRevision: actE2.Revision,
		OperationID:      "cp1-ack-stale-exec",
	})
	cp1AssertRemoteCode(t, err, "execution_conflict")

	_, err = proc.client.AcknowledgeActivity(ctx, pi.ProductActivityAcknowledgeParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
			ExecutionID:           exec2ID,
		},
		ExpectedRevision: actE2.Revision - 1,
		OperationID:      "cp1-ack-stale-rev",
	})
	cp1AssertRemoteCode(t, err, "stale_revision")

	acked, err := proc.client.AcknowledgeActivity(ctx, pi.ProductActivityAcknowledgeParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
			ExecutionID:           exec2ID,
		},
		ExpectedRevision: actE2.Revision,
		OperationID:      "cp1-ack-e2",
	})
	if err != nil || acked.AcknowledgedAt == "" || acked.AcknowledgedExecutionID != exec2ID {
		t.Fatalf("AcknowledgeActivity = %+v, %v", acked, err)
	}
	ackedReplay, err := proc.client.AcknowledgeActivity(ctx, pi.ProductActivityAcknowledgeParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
			ExecutionID:           exec2ID,
		},
		ExpectedRevision: actE2.Revision,
		OperationID:      "cp1-ack-e2",
	})
	if err != nil || !reflect.DeepEqual(ackedReplay, acked) {
		t.Fatalf("AcknowledgeActivity idempotent replay = %+v, %v", ackedReplay, err)
	}

	mu.Lock()
	targetCloseActID = actID
	mu.Unlock()
	if settled := cp1SendTurn(t, ctx, proc, bot.CurrentConversationID, "model-close-no-rev", "MODEL_CLOSE_WITHOUT_REV", nil); settled.State != "completed" {
		t.Fatalf("model close check = %+v", settled)
	}
	mu.Lock()
	gotCloseModelRes := closeInModelResult
	mu.Unlock()
	if !strings.Contains(gotCloseModelRes, `"invalid_params"`) {
		t.Fatalf("activity_action(close) without expected_revision must fail with invalid_params: %s", gotCloseModelRes)
	}

	closed, err := proc.client.CloseActivity(ctx, pi.ProductActivityCloseParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
		ExpectedRevision:      acked.Revision,
		OperationID:           "cp1-close-e2",
	})
	if err != nil || closed.ClosedAt == "" || closed.State != "completed" {
		t.Fatalf("CloseActivity = %+v, %v", closed, err)
	}
	if _, err := proc.client.ActivityResult(ctx, pi.ProductActivityExecutionParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
		ExecutionID:           exec2ID,
	}); err != nil {
		t.Fatalf("closed Activity result must stay readable: %v", err)
	}

	cont3, err := proc.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
		ProductText:           pi.ProductText{Text: "RUN_E3", Materials: []pi.ProductMaterial{}},
		OperationID:           "cp1-life-e3",
		ExpectedRevision:      closed.Revision,
	})
	if err != nil || cont3.Execution == nil {
		t.Fatalf("ContinueActivity E3 = %+v, %v", cont3, err)
	}
	if settled := proc.waitSettled(t, ctx, cont3.Execution.ExecutionID); settled.State != "completed" {
		t.Fatalf("E3 settled = %+v", settled)
	}
	actE3, err := proc.client.GetActivity(ctx, actID)
	if err != nil || actE3.AcknowledgedAt != "" || actE3.AcknowledgedExecutionID != "" || actE3.ClosedAt != "" || actE3.LatestProgress != nil {
		t.Fatalf("ContinueActivity did not atomically clear ack/close/progress: %+v, %v", actE3, err)
	}

	cont2b, err := proc.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
		ProductText:           pi.ProductText{Text: "RUN_E4", Materials: []pi.ProductMaterial{}},
		OperationID:           "cp1-life-e4-for-backup",
		ExpectedRevision:      actE3.Revision,
	})
	if err != nil || cont2b.Execution == nil {
		t.Fatalf("ContinueActivity E4 = %+v, %v", cont2b, err)
	}
	exec4ID := cont2b.Execution.ExecutionID
	_ = proc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.ActivityID == actID && ev.Revision > actE3.Revision && ev.State == "waitingForAnswer" && len(ev.OpenQuestionIDs) == 1
	})
	e4WaitDetail, err := proc.client.GetActivity(ctx, actID)
	if err != nil {
		t.Fatal(err)
	}
	qE4 := e4WaitDetail.Questions[len(e4WaitDetail.Questions)-1]
	const backupAnswerOpID = "cp1-answer-e4-backup"
	if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
			ExecutionID:           exec4ID,
		},
		QuestionID:       qE4.QuestionID,
		QuestionRevision: qE4.Revision,
		OperationID:      backupAnswerOpID,
		ProductText:      pi.ProductText{Text: "Backup answer text", Materials: []pi.ProductMaterial{}},
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, exec4ID); settled.State != "completed" {
		t.Fatalf("E4 settled = %+v", settled)
	}

	if settled := cp1SendTurn(t, ctx, proc, bot.CurrentConversationID, "del-unk", "DELEGATE_UNKNOWN", nil); settled.State != "completed" {
		t.Fatalf("delegate unknown settled = %+v", settled)
	}
	unkParked := proc.waitActivityChange(t, ctx, func(ev pi.ProductActivityChange) bool {
		return ev.Goal == "CP1_UNKNOWN_ACTIVITY" && ev.State == "waitingForAnswer" && len(ev.PendingApprovalRequestIDs) == 1
	})
	unkActID := unkParked.ActivityID
	unkKill, unkKillErr := proc.client.Kill(ctx)
	proc.exit(t, ctx, unkKill, false, unkKillErr)

	opts.FreshProfile = false
	procReopen := startCP1Process(t, opts)
	_ = procReopen.open(t, ctx)
	unkRecovered, err := procReopen.client.GetActivity(ctx, unkActID)
	if err != nil || unkRecovered.State != "needsAttention" || len(unkRecovered.Executions) != 1 || unkRecovered.Executions[0].State != "unknown" {
		t.Fatalf("unknown activity recovery = %+v, %v", unkRecovered, err)
	}
	unkReq := unkRecovered.ApprovalRequests[0]
	if _, err := procReopen.client.DenySkillApprovalRequest(ctx, pi.ProductSkillDenyApprovalRequestParams{
		RequestID:       unkReq.RequestID,
		RequestRevision: unkReq.Revision,
		OperationID:     "cp1-deny-unk-with-presecret",
		Reason:          "Denied with " + preSecret,
	}); err != nil {
		t.Fatal(err)
	}
	unkAfterDeny, err := procReopen.client.GetActivity(ctx, unkActID)
	if err != nil {
		t.Fatal(err)
	}
	unkClosed, err := procReopen.client.CloseActivity(ctx, pi.ProductActivityCloseParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: unkActID},
		ExpectedRevision:      unkAfterDeny.Revision,
		OperationID:           "cp1-close-idle-unknown",
	})
	if err != nil || unkClosed.ClosedAt == "" || unkClosed.State != "needsAttention" {
		t.Fatalf("closing idle unknown Activity = %+v, %v", unkClosed, err)
	}
	_, err = procReopen.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: unkActID},
		ProductText:           pi.ProductText{Text: "Must stay unrunnable", Materials: []pi.ProductMaterial{}},
		OperationID:           "cp1-continue-closed-unknown",
		ExpectedRevision:      unkClosed.Revision,
	})
	cp1AssertRemoteCode(t, err, "busy")

	cp1ConfigureConnection(t, ctx, procReopen.client, provider.server.URL+"/v1", preSecret, ws)

	mirrorBeforeBackup := productRead(t, opts.ProfilePath)
	dbHashBeforeBackup := cp1FileSHA256(t, filepath.Join(root, "nagi.db"))
	backupPath := filepath.Join(root, "cp1-backup.json")
	if _, err := procReopen.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatalf("BackupProfile = %v", err)
	}
	if !bytes.Equal(productRead(t, opts.ProfilePath), mirrorBeforeBackup) {
		t.Fatal("BackupProfile mutated live contacts.json mirror")
	}
	if cp1FileSHA256(t, filepath.Join(root, "nagi.db")) != dbHashBeforeBackup {
		t.Fatal("BackupProfile mutated live nagi.db")
	}
	backupInfo, err := os.Stat(backupPath)
	if err != nil || backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, %v, want 0600", backupInfo, err)
	}
	backupBytes := productRead(t, backupPath)
	for _, secret := range []string{primarySecret, preSecret} {
		if bytes.Contains(backupBytes, []byte(secret)) {
			t.Fatalf("backup JSON leaked credential %q", secret)
		}
	}
	if !bytes.Contains(backupBytes, []byte(`"activityProgress"`)) || !bytes.Contains(backupBytes, []byte(`"skillApprovalRequests"`)) {
		t.Fatal("backup JSON missing activityProgress or skillApprovalRequests auxiliary records")
	}
	procReopen.shutdown(t, ctx)

	callsBeforeRestore := provider.calls.Load()
	restoreRoot := t.TempDir()
	restoreOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(restoreRoot, "pi"),
		ProfilePath:     filepath.Join(restoreRoot, "contacts.json"),
		CredentialsPath: filepath.Join(restoreRoot, "connection.json"),
	}
	if err := os.WriteFile(restoreOpts.ProfilePath, backupBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 2; round++ {
		rProc := startCP1Process(t, restoreOpts)
		_ = rProc.open(t, ctx)
		rAct, err := rProc.client.GetActivity(ctx, actID)
		if err != nil || rAct.State != "completed" || rAct.LatestProgress == nil || !strings.Contains(rAct.LatestProgress.Stage, "[credential]") {
			t.Fatalf("restore round %d activity mismatch = %+v, %v", round, rAct, err)
		}
		if _, err := rProc.client.Operation(ctx, backupAnswerOpID); err != nil {
			t.Fatalf("restore round %d lost answer operation receipt: %v", round, err)
		}
		rRes, err := rProc.client.ActivityResult(ctx, pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
			ExecutionID:           exec4ID,
		})
		if err != nil || len(rRes.Files) != 1 || rRes.Files[0].Path != filepath.Join(ws, "receipt.txt") {
			t.Fatalf("restore round %d activity result = %+v, %v", round, rRes, err)
		}
		rTrace, err := rProc.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
			ExecutionID:           exec4ID,
		})
		if err != nil || len(rTrace.Items) == 0 {
			t.Fatalf("restore round %d activity trace = %+v, %v", round, rTrace, err)
		}
		for _, item := range rTrace.Items {
			if item.ActivityID != actID || item.ExecutionID != exec4ID {
				t.Fatalf("restore round %d trace ownership mismatch: %+v", round, item)
			}
		}
		rUnk, err := rProc.client.GetActivity(ctx, unkActID)
		if err != nil || rUnk.State != "needsAttention" || rUnk.ClosedAt == "" || len(rUnk.ApprovalRequests) != 1 ||
			rUnk.ApprovalRequests[0].State != "denied" || !strings.Contains(rUnk.ApprovalRequests[0].Reason, "[credential]") {
			t.Fatalf("restore round %d unknown/approval card mismatch = %+v, %v", round, rUnk, err)
		}
		rProc.shutdown(t, ctx)
	}
	if provider.calls.Load() != callsBeforeRestore {
		t.Fatalf("independent restore across 2 reopens made provider calls: before=%d after=%d", callsBeforeRestore, provider.calls.Load())
	}

	restoreDB := filepath.Join(restoreRoot, "nagi.db")
	restoreSQLite := runNodeSQLite(t, nodePath, restoreDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.exec('PRAGMA user_version = 11');
db.close();
console.log(JSON.stringify({ v, fk }));
`)
	if restoreSQLite != `{"v":10,"fk":0}` {
		t.Fatalf("restored v10 SQLite check = %s", restoreSQLite)
	}
	v9Client, startErr := pi.StartProduct(restoreOpts)
	if startErr == nil {
		if _, readyErr := v9Client.Ready(ctx); readyErr == nil {
			if _, openErr := v9Client.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
				t.Fatal("worker admitted SQLite user_version=11")
			}
		}
		_, _ = v9Client.Wait(ctx)
	}

	v7Root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v7State := filepath.Join(v7Root, "pi")
	v7Sessions := filepath.Join(v7State, "sessions")
	if err := os.MkdirAll(v7Sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	v7SessionFile := filepath.Join(v7Sessions, "pre-upgrade-conv.jsonl")
	preUpgradeL0L1 := "You are a long-lived Nagi Bot teammate. Help with your assigned job and carry useful context across the conversation.\n" +
		"Speak like a calm, sharp colleague in real-time chat. Skip stock greetings, filler, and unnecessary recaps. Default to a single concise bubble whenever you are expressing one complete thought.\n" +
		"Avoid Markdown headings, horizontal rules, tables, and essay-style summaries unless requested. Keep large code blocks and long documents out of chat bubbles. When asked to write a file, use the available tools and briefly report the result.\n" +
		"Only split into 2 (rarely 3) bubbles with <<<NEXT>>> on its own line when switching communicative intent—such as main answer/code followed by a brief side note or caveat, conclusion followed by a follow-up question, or switching to a separate topic. Never split a single sentence, a continuous explanation, or an intro line from its code block/list. Omit <<<NEXT>>> for one bubble and keep it out of code fences and inline code.\n" +
		"Respect prior chat context and user-attached Materials. Ask only for information you still need. Report work and tool results honestly. Never invent results or claim a file changed without evidence. Get explicit confirmation before destructive or irreversible actions.\n" +
		"Use memory_save_rule when the user explicitly asks to remember a lasting rule, memory_propose_learning for inferred preferences needing review, memory_forget_rule to remove outdated rules, and memory_log_work or memory_search_log for dated facts and past work.\n\n" +
		"Bot Identity\nName: LegacyV7Bot\nRole: Analyst\nPurpose:\nPre-upgrade prompt test\n\n" +
		"Behavior Instructions:\nOriginal pre-upgrade behavior.\n\n" +
		"Default working directory: " + ws + ". Do not modify or write files outside this directory unless the user explicitly asks."
	v7CapturedFull := preUpgradeL0L1 + "\n\nLasting Rules:\n- Old v7 rule"

	type v7ContextBinding struct {
		ContextID         string `json:"contextId"`
		BotID             string `json:"botId"`
		Purpose           string `json:"purpose"`
		SelectionRevision int    `json:"selectionRevision"`
		CWD               string `json:"cwd"`
	}
	type v7ConfigData struct {
		WireAPI          string `json:"wireApi"`
		BaseURL          string `json:"baseUrl"`
		Model            string `json:"model"`
		ContextTokens    int    `json:"contextTokens"`
		OutputTokens     int    `json:"outputTokens"`
		CredentialSource string `json:"credentialSource"`
	}
	v7SessLines := []any{
		map[string]any{"type": "session", "version": 3, "id": "v7-sess", "timestamp": "2025-01-01T00:00:00.000Z", "cwd": ws},
		map[string]any{"type": "custom", "id": "b1", "parentId": nil, "timestamp": "2025-01-01T00:00:01.000Z", "customType": "nagi-context-v1", "data": v7ContextBinding{
			ContextID: "ctx-v7", BotID: "bot-v7", Purpose: "conversation", SelectionRevision: 1, CWD: ws,
		}},
		map[string]any{"type": "custom", "id": "c1", "parentId": "b1", "timestamp": "2025-01-01T00:00:02.000Z", "customType": "nagi-configuration-v1", "data": v7ConfigData{
			WireAPI: "openai-completions", BaseURL: provider.server.URL + "/v1", Model: productModel,
			ContextTokens: 262144, OutputTokens: 4096, CredentialSource: "src-v7",
		}},
		map[string]any{"type": "custom", "id": "t1", "parentId": "c1", "timestamp": "2025-01-01T00:00:03.000Z", "customType": "nagi-turn-v1", "data": map[string]any{
			"runId": "run-v7-0", "text": "Hello v7", "context": []any{}, "materials": []any{},
		}},
	}
	var v7SessBuf bytes.Buffer
	for _, line := range v7SessLines {
		b, _ := json.Marshal(line)
		v7SessBuf.Write(b)
		v7SessBuf.WriteByte('\n')
	}
	if err := os.WriteFile(v7SessionFile, v7SessBuf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	v7Opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        v7State,
		ProfilePath:     filepath.Join(v7Root, "contacts.json"),
		CredentialsPath: filepath.Join(v7Root, "connection.json"),
	}
	writeProductFixture(t, v7Opts.CredentialsPath, map[string]any{
		"version": 2, "credential_source": "src-v7", "model": productModel,
		"context_tokens": 262144, "output_tokens": 4096, "working_directory": ws, "max_active_tasks": 4,
		"sources": map[string]any{"src-v7": map[string]string{"api": "openai-chat-completions", "endpoint": provider.server.URL + "/v1", "api_key": primarySecret}},
	})
	v7DB := filepath.Join(v7Root, "nagi.db")
	runNodeSQLite(t, nodePath, v7DB, `
const { createHash } = require('node:crypto');
const { writeFileSync } = require('node:fs');
const { DatabaseSync } = require('node:sqlite');
const [dbPath, mirrorPath, sessionFile, ws, baseURL, captured] = process.argv.slice(1);
const canonical = value => value === null || typeof value !== 'object' ? JSON.stringify(value) :
  Array.isArray(value) ? '[' + value.map(canonical).join(',') + ']' :
  '{' + Object.keys(value).filter(k => value[k] !== undefined).sort().map(k => JSON.stringify(k) + ':' + canonical(value[k])).join(',') + '}';
const profile = {
  version: 1,
  bots: [{ botId: 'bot-v7', profile: { name: 'LegacyV7Bot', role: 'Analyst', purpose: 'Pre-upgrade prompt test', instructions: 'Original pre-upgrade behavior.', workspace: ws, codingTools: [] }, profileRevision: 1, archived: false, currentConversationId: 'conv-v7' }],
  conversations: [{ conversationId: 'conv-v7', botId: 'bot-v7', revision: 1, draft: { revision: 0, text: '', materials: [] }, binding: { contextId: 'ctx-v7', sessionRef: sessionFile, connectionRevision: 1, profileRevision: 1, cwd: ws, instructions: captured, codingTools: [], used: true, memoryRulesRevision: 1 } }],
  activities: [{ activityId: 'act-v7', ownerBotId: 'bot-v7', originConversationId: 'conv-v7', revision: 1, state: 'needsAttention', goal: 'Historical v7 task', requirements: [], questions: [], materials: [], inputFiles: [], executionIds: ['exec-v7'], results: [] }],
  executions: [{ executionId: 'exec-v7', revision: 1, state: 'completed', operationId: 'op-v7', input: { revision: 0, text: 'Historical v7 task', materials: [] }, retained: { id: 'exec-v7' } }],
  operations: [],
  connections: [{ revision: 1, sourceId: 'src-v7', configuration: { wireApi: 'openai-completions', baseUrl: baseURL, model: 'nagi-local-integration', contextTokens: 262144, outputTokens: 4096, workingDirectory: ws, maxActiveTasks: 4 } }],
  savedRevision: 1, appliedRevision: 1,
  memory: { rules: [{ ruleId: 'rule-v7', botId: 'bot-v7', text: 'Old v7 rule', source: 'manual', createdAt: '2025-01-01T00:00:00.000Z', updatedAt: '2025-01-01T00:00:00.000Z' }], workLogs: [], pendingLearnings: [], meta: [{ botId: 'bot-v7', rulesRevision: 1, forceReload: false, lastTurnAt: '2025-01-01T00:00:00.000Z' }] },
};
const mirrorData = Buffer.from(canonical(profile) + '\n');
const digest = createHash('sha256').update(mirrorData).digest('hex');
writeFileSync(mirrorPath, mirrorData, { mode: 0o600 });
const db = new DatabaseSync(dbPath);
db.exec('PRAGMA foreign_keys = ON;');
db.exec(`+"`"+`
  CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
  CREATE TABLE bots (bot_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, profile_json TEXT NOT NULL, profile_revision INTEGER NOT NULL, archived INTEGER NOT NULL CHECK (archived IN (0, 1)), current_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED, retained_json TEXT, system_kind TEXT) STRICT;
  CREATE TABLE conversations (conversation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, revision INTEGER NOT NULL, draft_json TEXT NOT NULL, binding_json TEXT, latest_execution_id TEXT REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED) STRICT;
  CREATE TABLE activities (activity_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id), origin_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id), revision INTEGER NOT NULL, state TEXT NOT NULL, goal_json TEXT NOT NULL, requirements_json TEXT NOT NULL, questions_json TEXT NOT NULL, materials_json TEXT NOT NULL, input_files_json TEXT NOT NULL, execution_ids_json TEXT NOT NULL, results_json TEXT NOT NULL, binding_json TEXT, retained_json TEXT, granted_linked_activity_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(granted_linked_activity_ids_json) AND json_type(granted_linked_activity_ids_json) = 'array'), routing_json TEXT) STRICT;
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
`+"`"+`);
db.exec('BEGIN IMMEDIATE');
db.prepare('INSERT INTO bots VALUES (?, ?, ?, ?, ?, ?, ?, ?)').run(JSON.stringify('bot-v7'), 0, JSON.stringify(profile.bots[0].profile), 1, 0, JSON.stringify('conv-v7'), null, null);
db.prepare('INSERT INTO conversations VALUES (?, ?, ?, ?, ?, ?, ?)').run(JSON.stringify('conv-v7'), 0, JSON.stringify('bot-v7'), 1, JSON.stringify(profile.conversations[0].draft), JSON.stringify(profile.conversations[0].binding), null);
db.prepare('INSERT INTO executions VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)').run(JSON.stringify('exec-v7'), 0, 1, 'completed', JSON.stringify('op-v7'), JSON.stringify(profile.executions[0].input), null, null, null, JSON.stringify({ id: 'exec-v7' }));
db.prepare('INSERT INTO activities VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)').run(JSON.stringify('act-v7'), 0, JSON.stringify('bot-v7'), JSON.stringify('conv-v7'), 1, 'needsAttention', JSON.stringify('Historical v7 task'), '[]', '[]', '[]', '[]', '["exec-v7"]', '[]', null, null, '[]', null);
db.prepare('INSERT INTO connections VALUES (?, ?, ?, ?)').run(1, 0, JSON.stringify('src-v7'), JSON.stringify(profile.connections[0].configuration));
db.prepare('INSERT INTO bot_memory_rules VALUES (?, ?, ?)').run(JSON.stringify('rule-v7'), JSON.stringify('bot-v7'), JSON.stringify(profile.memory.rules[0]));
db.prepare('INSERT INTO bot_memory_meta VALUES (?, ?)').run(JSON.stringify('bot-v7'), JSON.stringify(profile.memory.meta[0]));
db.prepare('INSERT INTO activity_trace_meta VALUES (?, ?, ?, ?)').run(JSON.stringify('exec-v7'), JSON.stringify('act-v7'), 'recorded', 40);
db.prepare('INSERT INTO activity_traces (seq, activity_id, execution_id, at, kind, detail_json) VALUES (?, ?, ?, ?, ?, ?)').run(41, JSON.stringify('act-v7'), JSON.stringify('exec-v7'), '2025-01-01T00:00:04.000Z', 'state_changed', JSON.stringify({ scope: 'activity', state: 'needsAttention' }));
for (const [k, v] of [['saved_revision', '1'], ['applied_revision', '1'], ['memory_present', '1'], ['routing_policy', '"balanced"'], ['trace_generation', 'v7-trace-gen'], ['snapshot_sha256', digest]]) {
  db.prepare('INSERT INTO meta VALUES (?, ?)').run(k, v);
}
db.exec('COMMIT');
db.exec('PRAGMA user_version = 7');
db.close();
`, v7Opts.ProfilePath, v7SessionFile, ws, provider.server.URL+"/v1", v7CapturedFull)

	v7MirrorBefore := productRead(t, v7Opts.ProfilePath)
	v7Proc := startCP1Process(t, v7Opts)
	_ = v7Proc.open(t, ctx)

	if !bytes.Equal(productRead(t, v7Opts.ProfilePath), v7MirrorBefore) {
		t.Fatal("v7->v8 open altered original canonical contacts.json bytes")
	}
	v7Act, err := v7Proc.client.GetActivity(ctx, "act-v7")
	if err != nil || v7Act.State != "needsAttention" {
		t.Fatalf("v7->v8 migration altered historical needsAttention activity: %+v, %v", v7Act, err)
	}
	v7Schema := runNodeSQLite(t, nodePath, v7DB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
const seq = db.prepare("SELECT seq FROM sqlite_sequence WHERE name = 'activity_traces'").get()?.seq;
const ddl = db.prepare("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'activity_traces'").get()?.sql;
db.close();
console.log(JSON.stringify({ v, fk, seq, hasFourKinds: ddl.includes("'tool_started','tool_finished','file_confirmed','state_changed'") }));
`)
	if v7Schema != `{"v":10,"fk":0,"seq":41,"hasFourKinds":true}` {
		t.Fatalf("v7->v10 schema/trace check = %s", v7Schema)
	}

	if _, err := v7Proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			OperationID: "cp1-v7-edit-bot", BotID: "bot-v7", ExpectedRevision: 1,
		},
		Profile: pi.ProductBotProfile{
			Name: "EditedV7Name", Role: "NewRole", Purpose: "Edited purpose",
			Instructions: "Edited instructions after v8 upgrade.", Workspace: ws, CodingTools: []string{},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := v7Proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: "bot-v7", OperationID: "cp1-v7-add-rule"},
		Text:                   "New v8 rule appended after cold reload",
	}); err != nil {
		t.Fatal(err)
	}

	var v7ColdSystem string
	provider.setHandler(func(req cp1Request) cp1Reply {
		mu.Lock()
		v7ColdSystem = req.System
		mu.Unlock()
		return cp1Reply{Text: "V7 cold reload ok"}
	})
	if settled := cp1SendTurn(t, ctx, v7Proc, "conv-v7", "v7-cold-turn", "CHECK_PRE_UPGRADE_PREFIX", nil); settled.State != "completed" {
		t.Fatalf("v7 cold reload turn settled = %+v", settled)
	}
	mu.Lock()
	gotV7Cold := v7ColdSystem
	mu.Unlock()
	if !strings.HasPrefix(gotV7Cold, preUpgradeL0L1) {
		t.Fatalf("cold reload altered pre-upgrade captured L0+L1 prefix:\ngot=%q\nwant prefix=%q", gotV7Cold, preUpgradeL0L1)
	}
	if !strings.Contains(gotV7Cold, "New v8 rule appended after cold reload") || strings.Contains(gotV7Cold, "EditedV7Name") || strings.Contains(gotV7Cold, "Use activity_delegate") {
		t.Fatalf("cold reload did not preserve L0+L1 while updating L1.5 rules: %q", gotV7Cold)
	}
	v7Proc.shutdown(t, ctx)

	provider.assertNoProblems()
}
