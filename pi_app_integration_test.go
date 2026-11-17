//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

type piAppCall struct {
	name string
	id   string
	args func(string) map[string]any
}

type piAppProvider struct {
	mu       sync.Mutex
	calls    []piAppCall
	step     int
	requests int
	limit    int
	bodies   []json.RawMessage
	problems []string
	arrived  chan struct{}
	release  <-chan struct{}
}

func piAppArguments(values map[string]any) func(string) map[string]any {
	return func(string) map[string]any { return values }
}

func (p *piAppProvider) install(calls []piAppCall, arrived chan struct{}, release <-chan struct{}) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != nil && p.step != len(p.calls)+1 {
		return fmt.Errorf("previous provider turn stopped at %d/%d", p.step, len(p.calls)+1)
	}
	p.calls, p.step, p.arrived, p.release = calls, 0, arrived, release
	return nil
}

func (p *piAppProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

func (p *piAppProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	var request struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
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
	if err == nil {
		err = json.Unmarshal(body, &request)
	}
	p.mu.Lock()
	p.requests++
	p.bodies = append(p.bodies, append(json.RawMessage(nil), body...))
	step := p.step
	if err == nil && (r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer nagi-product-loopback" || request.Model != "nagi-product-fixture" || !request.Stream || len(request.Messages) == 0) {
		err = errors.New("unexpected loopback route, authorization, model or message shape")
	}
	if err == nil && (p.requests > p.limit || p.calls == nil || step > len(p.calls)) {
		err = fmt.Errorf("unexpected provider request %d at step %d", p.requests, step)
	}
	var previous string
	if err == nil && step > 0 {
		last := request.Messages[len(request.Messages)-1]
		if last.Role != "tool" || last.ToolCallID != p.calls[step-1].id {
			err = fmt.Errorf("tool result did not match %s: role=%s call=%s", p.calls[step-1].id, last.Role, last.ToolCallID)
		} else if json.Unmarshal(last.Content, &previous) != nil {
			err = errors.New("tool result content is not text")
		}
	}
	var call piAppCall
	if err == nil && step < len(p.calls) {
		call = p.calls[step]
		found := false
		for _, tool := range request.Tools {
			found = found || tool.Function.Name == call.name
		}
		if !found {
			err = fmt.Errorf("real SDK did not offer %s", call.name)
		}
	}
	arrived, release := p.arrived, p.release
	if err != nil {
		p.problems = append(p.problems, err.Error())
	} else {
		p.step++
	}
	p.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if step == 0 && arrived != nil {
		close(arrived)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
	}
	delta := map[string]any{"role": "assistant", "content": "PRODUCT_TURN_COMPLETE"}
	finish := "stop"
	if call.name != "" {
		arguments, marshalErr := json.Marshal(call.args(previous))
		if marshalErr != nil {
			http.Error(w, marshalErr.Error(), http.StatusBadRequest)
			return
		}
		delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": 0, "id": call.id, "type": "function", "function": map[string]string{"name": call.name, "arguments": string(arguments)},
		}}}
		finish = "tool_calls"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, choice := range []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}, {"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
		chunk, _ := json.Marshal(map[string]any{"id": "product-loopback", "object": "chat.completion.chunk", "created": 1, "model": "nagi-product-fixture", "choices": []any{choice}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

type piAppRun struct {
	binding pi.ContextBinding
	params  pi.StartRunParams
	bridge  *piIssueTools
}

type piAppFixture struct {
	t        *testing.T
	ctx      context.Context
	cancel   context.CancelFunc
	app      *appModel
	pump     *uiPump
	provider *piAppProvider
	server   *httptest.Server
	project  string
	state    string
	client   *pi.Client
	ready    pi.Ready
	exits    chan pi.ProcessOutcome
	bindings []pi.ContextBinding
	closed   bool
	workers  int
	maxNodes int
	turn     int
	mu       sync.Mutex
	current  *piAppRun
	events   []pi.Event
	problems []string
	wake     chan struct{}
}

func piAppRecord(t *testing.T, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil || len(data) > 2<<20 {
		t.Errorf("receipt %s cannot be exported: bytes=%d error=%v", name, len(data), err)
		return
	}
	t.Logf("NAGI_E2E_RECEIPT %s %s", name, data)
}

func piAppFileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func newPiAppFixture(t *testing.T, requestLimit, maxNodes int) *piAppFixture {
	t.Helper()
	for _, name := range []string{"NAGI_TEST_NODE", "NAGI_TEST_WORKER"} {
		path := os.Getenv(name)
		if !filepath.IsAbs(path) {
			t.Fatalf("%s must identify an explicit pinned absolute input", name)
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s must identify a regular input: %v", name, err)
		}
	}
	app, _ := newIssueToolTestApp(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.workDir, app.workspaceOpen, app.windowWidth, app.scrollTo = project, true, 1040, -1
	first := app.selectedContact()
	first.Name, first.Workspace, first.Loaded = "Owner A", project, true
	app.contacts = append(app.contacts, &contact{ID: newContactID(), Name: "Owner B", Workspace: project, Loaded: true})
	app.bindTaskContexts()
	if err := app.persist(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	f := &piAppFixture{t: t, ctx: ctx, cancel: cancel, app: app, project: project, state: t.TempDir(), maxNodes: maxNodes, wake: make(chan struct{}, 1), closed: true}
	f.pump = newUIPump(t, app)
	f.provider = &piAppProvider{limit: requestLimit}
	f.server = httptest.NewUnstartedServer(f.provider)
	f.server.Config.ReadHeaderTimeout = 5 * time.Second
	f.server.Config.ReadTimeout = 10 * time.Second
	f.server.Config.WriteTimeout = 10 * time.Second
	f.server.Start()
	t.Cleanup(f.cleanup)
	inputs := map[string]string{}
	for _, path := range []string{"pi_app_integration_test.go", "pi_issue_tools.go", "issue_tool_app.go", "issue_supplement_app.go", "issue_supplement_view.go", "workspace.go", "model.go", os.Getenv("NAGI_TEST_NODE"), os.Getenv("NAGI_TEST_WORKER")} {
		digest, err := piAppFileHash(path)
		if err != nil {
			t.Fatal(err)
		}
		inputs[path] = digest
	}
	piAppRecord(t, "source-and-inputs", inputs)
	f.startWorker()
	return f
}

func (f *piAppFixture) onUI(fn func() error) {
	f.t.Helper()
	var err error
	f.pump.do(func() { err = fn() })
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *piAppFixture) startWorker() {
	f.t.Helper()
	f.workers++
	if f.workers > f.maxNodes || !f.closed {
		f.t.Fatal("worker budget or sequential ownership violated")
	}
	exits := make(chan pi.ProcessOutcome, 1)
	client, err := pi.Start(pi.Options{
		NodePath: os.Getenv("NAGI_TEST_NODE"), WorkerPath: os.Getenv("NAGI_TEST_WORKER"), StateDir: f.state,
		Handlers: pi.Handlers{OnEvent: f.handleEvent, OnExit: func(outcome pi.ProcessOutcome) { exits <- outcome }},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.client, f.exits, f.closed, f.bindings = client, exits, false, nil
	f.ready, err = client.Ready(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	if f.ready.Engine.Version != "1.5.2" || f.ready.Runtime.Version != "v22.23.5" || f.ready.WorkerEpoch == "" {
		f.t.Fatalf("unpinned real worker identity: %+v", f.ready)
	}
	if _, err := client.Configure(f.ctx, pi.ConfigureParams{WireAPI: pi.OpenAICompletions, BaseURL: f.server.URL + "/v1", APIKey: "nagi-product-loopback", CredentialSource: "product-loopback-connection", Model: "nagi-product-fixture", ContextTokens: 65536, OutputTokens: 4096}); err != nil {
		f.t.Fatal(err)
	}
	piAppRecord(f.t, "ready", f.ready)
}

func (f *piAppFixture) open(contactID string, resume string) pi.ContextBinding {
	f.t.Helper()
	tools, err := piIssueToolDefinitions()
	if err != nil {
		f.t.Fatal(err)
	}
	binding, err := f.client.OpenContext(f.ctx, pi.OpenContextParams{ContextID: "conversation-" + contactID, BotID: contactID, Purpose: pi.Conversation, SelectionRevision: 1, CWD: f.project, ResumeRef: resume, Used: resume != "", CodingTools: []string{}, DomainTools: tools})
	if err != nil {
		f.t.Fatal(err)
	}
	if binding.BotID != contactID || binding.Purpose != pi.Conversation || binding.CWD != f.project || binding.SessionRef == "" {
		f.t.Fatalf("unexpected actual context binding: %+v", binding)
	}
	f.bindings = append(f.bindings, binding)
	piAppRecord(f.t, "binding", binding)
	return binding
}

func (f *piAppFixture) handleEvent(event pi.Event) {
	f.mu.Lock()
	run := f.current
	f.mu.Unlock()
	var err error
	if run != nil && event.ContextID == run.binding.ContextID && event.RunID == run.params.RunID {
		if event.Name == pi.RunStarted {
			var started struct {
				Correlation pi.RunCorrelation `json:"correlation"`
			}
			if json.Unmarshal(event.Data, &started) != nil || started.Correlation != *run.params.Correlation {
				err = errors.New("actual run.started correlation did not match the registered product turn")
			} else {
				f.app.runOnUI(func() {
					ct := f.app.contactByID(run.binding.BotID)
					ct.ActiveTaskID, ct.SendState, ct.pendingSend = event.RunID, sendAccepted, nil
					f.app.issueToolBindTask(run.params.Correlation.ScopeRef, event.RunID)
					err = f.app.persist()
				})
			}
		}
		if err == nil {
			err = run.bridge.HandleEvent(event)
		}
		if event.Name == pi.RunSettled {
			f.app.runOnUI(func() {
				ct := f.app.contactByID(run.binding.BotID)
				ct.ActiveTaskID, ct.SendState, ct.pendingSend = "", sendIdle, nil
			})
		}
	}
	f.mu.Lock()
	f.events = append(f.events, event)
	if err != nil {
		f.problems = append(f.problems, err.Error())
	}
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *piAppFixture) prepare(binding pi.ContextBinding) *piAppRun {
	f.t.Helper()
	f.turn++
	run := &piAppRun{binding: binding, params: pi.StartRunParams{ContextID: binding.ContextID, RunID: fmt.Sprintf("product-run-%d", f.turn), Text: fmt.Sprintf("PRODUCT_TURN_%d", f.turn)}}
	f.onUI(func() error {
		ct := f.app.contactByID(binding.BotID)
		ct.SessionID, ct.SendState, ct.ActiveTaskID = binding.SessionRef, sendSubmitting, ""
		ct.pendingSend = &pendingSend{Draft: run.params.Text}
		scope, err := f.app.issueToolNewScope(ct.ID, binding.SessionRef, run.params.RunID+"-turn", false)
		if err != nil {
			return err
		}
		if err := f.app.issueToolAttachPendingScope(ct.ID, scope.Ref); err != nil {
			return err
		}
		run.params.Correlation = &pi.RunCorrelation{TurnID: scope.TurnID, ScopeRef: scope.Ref}
		return nil
	})
	bridge, err := newPiIssueTools(f.ctx, f.app, f.client, binding, run.params)
	if err != nil {
		f.t.Fatal(err)
	}
	run.bridge = bridge
	f.mu.Lock()
	f.current = run
	f.mu.Unlock()
	return run
}

func (f *piAppFixture) start(run *piAppRun, calls []piAppCall, arrived chan struct{}, release <-chan struct{}) {
	f.t.Helper()
	if err := f.provider.install(calls, arrived, release); err != nil {
		f.t.Fatal(err)
	}
	admission, err := f.client.StartRun(f.ctx, run.params)
	if err != nil || !admission.Admitted || admission.RunID != run.params.RunID || admission.ContextID != run.binding.ContextID {
		f.t.Fatalf("real run admission=%+v error=%v", admission, err)
	}
	piAppRecord(f.t, "run-admitted", map[string]any{"binding": run.binding, "params": run.params, "admission": admission})
	f.event(run, pi.RunStarted, "")
}

func (f *piAppFixture) event(run *piAppRun, name pi.EventName, callID string) pi.Event {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	event, err := f.await(ctx, run.binding.ContextID, run.params.RunID, name, callID)
	if err != nil {
		f.t.Fatal(err)
	}
	return event
}

func (f *piAppFixture) await(ctx context.Context, contextID, runID string, name pi.EventName, callID string) (pi.Event, error) {
	for {
		f.mu.Lock()
		var found *pi.Event
		for i := range f.events {
			event := &f.events[i]
			if event.WorkerEpoch != f.ready.WorkerEpoch || event.ContextID != contextID || event.RunID != runID || event.Name != name {
				continue
			}
			if callID != "" {
				var data struct {
					CallID string `json:"callId"`
				}
				if json.Unmarshal(event.Data, &data) != nil || data.CallID != callID {
					continue
				}
			}
			copy := *event
			found = &copy
			break
		}
		problems := append([]string(nil), f.problems...)
		f.mu.Unlock()
		if found != nil {
			return *found, nil
		}
		if len(problems) != 0 {
			return pi.Event{}, fmt.Errorf("product event routing failed: %v", problems)
		}
		select {
		case <-f.wake:
		case <-ctx.Done():
			return pi.Event{}, fmt.Errorf("waiting for %s/%s/%s: %w", runID, name, callID, ctx.Err())
		}
	}
}

func (f *piAppFixture) finish(run *piAppRun) {
	f.t.Helper()
	event := f.event(run, pi.RunSettled, "")
	var settled pi.RunSettledData
	if json.Unmarshal(event.Data, &settled) != nil || settled.Status != pi.Completed || settled.Error != nil || settled.Text != "PRODUCT_TURN_COMPLETE" {
		f.t.Fatalf("actual product run did not complete: %s", event.Data)
	}
	if err := run.bridge.Drain(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	if f.current == run {
		f.current = nil
	}
	f.mu.Unlock()
	f.onUI(func() error {
		ct := f.app.contactByID(run.binding.BotID)
		ct.ActiveTaskID, ct.SendState, ct.pendingSend = "", sendIdle, nil
		return f.app.persist()
	})
}

func piAppContent(raw json.RawMessage) (string, error) {
	var result struct {
		Content []pi.ContentBlock `json:"content"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		return "", fmt.Errorf("expected one original product text content: %s", raw)
	}
	return result.Content[0].Text, nil
}

func (f *piAppFixture) tool(run *piAppRun, call piAppCall, errorCode string) string {
	f.t.Helper()
	request := f.event(run, pi.ToolRequest, call.id)
	finished := f.event(run, pi.ToolFinished, call.id)
	var asked pi.ToolRequestData
	var result pi.ToolFinishedData
	var requestedInput, finishedInput any
	if json.Unmarshal(request.Data, &asked) != nil || json.Unmarshal(finished.Data, &result) != nil || json.Unmarshal(asked.Input, &requestedInput) != nil || json.Unmarshal(result.Input, &finishedInput) != nil || asked.Name != call.name || result.Name != call.name || request.Seq >= finished.Seq || result.IsError != (errorCode != "") || !reflect.DeepEqual(requestedInput, finishedInput) {
		f.t.Fatalf("unmatched actual tool request/finished: request=%s finished=%s", request.Data, finished.Data)
	}
	text, err := piAppContent(result.Result)
	if err != nil || errorCode != "" && !strings.Contains(text, errorCode) {
		f.t.Fatalf("tool %s content=%q error=%v expected domain error=%q", call.id, text, err, errorCode)
	}
	piAppRecord(f.t, "tool", map[string]any{"request": request, "finished": finished, "text": text})
	return text
}

func (f *piAppFixture) history(binding pi.ContextBinding) map[string]json.RawMessage {
	f.t.Helper()
	entries := map[string]json.RawMessage{}
	cursors := map[string]bool{}
	cursor := ""
	for {
		page, err := f.client.History(f.ctx, pi.HistoryParams{ContextID: binding.ContextID, Cursor: cursor, Limit: 2})
		if err != nil {
			f.t.Fatal(err)
		}
		for _, raw := range page.Entries {
			var entry struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &entry) != nil || entry.ID == "" || entries[entry.ID] != nil {
				f.t.Fatalf("invalid or repeated public history entry: %s", raw)
			}
			entries[entry.ID] = raw
		}
		if page.NextCursor == "" {
			break
		}
		if cursors[page.NextCursor] {
			f.t.Fatal("public history cursor repeated")
		}
		cursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	piAppRecord(f.t, "public-history", entries)
	return entries
}

func (f *piAppFixture) historyTool(entries map[string]json.RawMessage, call piAppCall, expected string) {
	f.t.Helper()
	matches := 0
	for _, raw := range entries {
		var entry struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(raw, &entry) != nil || entry.Type != "message" {
			continue
		}
		var header struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(entry.Message, &header) != nil || header.Role != "toolResult" {
			continue
		}
		var message struct {
			ToolCallID string `json:"toolCallId"`
			ToolName   string `json:"toolName"`
		}
		if json.Unmarshal(entry.Message, &message) != nil || message.ToolCallID != call.id {
			continue
		}
		text, err := piAppContent(entry.Message)
		if err != nil || message.ToolName != call.name || text != expected {
			f.t.Fatalf("public tool receipt differs from actual tool.finished: %s", entry.Message)
		}
		matches++
	}
	if matches != 1 {
		f.t.Fatalf("public history matches for %s = %d", call.id, matches)
	}
}

func (f *piAppFixture) stop(ctx context.Context) error {
	if f.closed {
		return nil
	}
	for _, binding := range f.bindings {
		inspect, err := f.client.Inspect(ctx, binding.ContextID)
		if err == nil && inspect.ActiveRun != nil {
			if _, err := f.client.CancelRun(ctx, pi.CancelRunParams{ContextID: binding.ContextID, RunID: inspect.ActiveRun.RunID}); err != nil {
				return err
			}
			if _, err := f.await(ctx, binding.ContextID, inspect.ActiveRun.RunID, pi.RunSettled, ""); err != nil {
				return err
			}
		}
	}
	f.mu.Lock()
	run := f.current
	f.mu.Unlock()
	if run != nil {
		if err := run.bridge.Drain(ctx); err != nil {
			return err
		}
	}
	for _, binding := range f.bindings {
		if _, err := f.client.CloseContext(ctx, binding.ContextID); err != nil {
			return err
		}
	}
	outcome, err := f.client.Shutdown(ctx)
	piAppRecord(f.t, "shutdown-wait", map[string]any{"outcome": outcome, "outcome_error": fmt.Sprint(outcome.Err), "error": fmt.Sprint(err)})
	if err != nil {
		return err
	}
	waited, err := f.client.Wait(ctx)
	if err != nil || outcome.PID <= 0 || !outcome.Reaped || outcome.ExitCode != 0 || outcome.Err != nil || waited.PID != outcome.PID || waited.ExitCode != outcome.ExitCode || !waited.Reaped {
		return fmt.Errorf("actual worker wait mismatch: shutdown=%+v wait=%+v error=%v", outcome, waited, err)
	}
	select {
	case <-f.client.CallbacksDone():
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case observed := <-f.exits:
		piAppRecord(f.t, "exit-callback", observed)
		if observed.PID != outcome.PID || observed.ExitCode != outcome.ExitCode || !observed.Reaped || observed.Err != nil {
			return fmt.Errorf("exit callback differs from actual wait: %+v", observed)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	f.closed = true
	return nil
}

func (f *piAppFixture) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer f.cancel()
	if err := f.stop(ctx); err != nil {
		f.t.Errorf("actual process cleanup: %v", err)
	}
	f.pump.do(func() {
		data, err := os.ReadFile(f.app.store.path)
		piAppRecord(f.t, "product-store", map[string]any{"data": string(data), "sha256": fmt.Sprintf("%x", sha256.Sum256(data)), "error": fmt.Sprint(err)})
	})
	f.mu.Lock()
	piAppRecord(f.t, "all-events", f.events)
	piAppRecord(f.t, "bridge-errors", f.problems)
	if len(f.problems) != 0 {
		f.t.Errorf("product event routing problems: %v", f.problems)
	}
	piAppRecord(f.t, "fixture-budget", map[string]any{"workers": f.workers, "worker_limit": f.maxNodes})
	f.mu.Unlock()
	f.provider.mu.Lock()
	piAppRecord(f.t, "loopback", map[string]any{"requests": f.provider.requests, "limit": f.provider.limit, "bodies": f.provider.bodies, "problems": f.provider.problems})
	if len(f.provider.problems) != 0 {
		f.t.Errorf("loopback problems: %v", f.provider.problems)
	}
	f.provider.mu.Unlock()
	f.server.Close()
	if f.closed {
		f.pump.close()
	} else {
		f.t.Error("UI pump retained because process/callback ownership did not finish")
	}
}

func (f *piAppFixture) issue(ownerID, goal string) *productIssue {
	f.t.Helper()
	var issue *productIssue
	f.onUI(func() error {
		var err error
		issue, err = f.app.createIssueFor(f.app.contactByID(ownerID), goal, "Fixture requirement")
		return err
	})
	return issue
}

func TestPiAppScopedDomainTools(t *testing.T) {
	f := newPiAppFixture(t, 12, 2)
	var aID, bID string
	f.onUI(func() error {
		aID, bID = f.app.contacts[0].ID, f.app.contacts[1].ID
		return nil
	})
	foreignIssue := f.issue(bID, "Foreign issue")
	binding := f.open(aID, "")
	other := f.open(bID, "")
	var foreignScope *IssueToolScope
	f.onUI(func() error {
		var err error
		foreignScope, err = f.app.issueToolNewScope(bID, other.SessionRef, "foreign-conversation-turn", false)
		return err
	})
	run := f.prepare(binding)
	scope := run.params.Correlation.ScopeRef
	create := map[string]any{"scope": scope, "action": "create", "goal": "One durable issue", "requirements": []string{"Preserve receipt"}}
	calls := []piAppCall{
		{"issue_list", "domain-list-before", piAppArguments(map[string]any{"scope": scope})},
		{"issue_action", "domain-create", piAppArguments(create)},
		{"issue_action", "domain-create-repeat", piAppArguments(create)},
		{"issue_get", "domain-get", func(previous string) map[string]any {
			var receipt IssueToolReceipt
			_ = json.Unmarshal([]byte(previous), &receipt)
			return map[string]any{"scope": scope, "issue_id": receipt.IssueID}
		}},
		{"issue_action", "domain-foreign-scope", piAppArguments(map[string]any{"scope": foreignScope.Ref, "action": "create", "goal": "Forbidden foreign create"})},
		{"issue_get", "domain-foreign-issue", piAppArguments(map[string]any{"scope": scope, "issue_id": foreignIssue.ID})},
		{"issue_list", "domain-list-after", piAppArguments(map[string]any{"scope": scope})},
	}
	if f.provider.count() != 0 {
		t.Fatal("context/scope registration contacted the provider")
	}
	f.start(run, calls, nil, nil)
	f.finish(run)
	texts := map[string]string{}
	for _, call := range calls {
		code := ""
		if call.id == "domain-foreign-scope" {
			code = "scope_denied"
		}
		if call.id == "domain-foreign-issue" {
			code = "not_found"
		}
		texts[call.id] = f.tool(run, call, code)
	}
	var receipt, repeated IssueToolReceipt
	if json.Unmarshal([]byte(texts["domain-create"]), &receipt) != nil || json.Unmarshal([]byte(texts["domain-create-repeat"]), &repeated) != nil || receipt.State != issueToolReceiptSaved || receipt.IssueID == "" || !reflect.DeepEqual(receipt, repeated) {
		t.Fatalf("repeated logical intent changed original receipt: %+v / %+v", receipt, repeated)
	}
	var beforeList, afterList IssueToolIssueListResult
	var detail IssueToolIssueDetail
	if json.Unmarshal([]byte(texts["domain-list-before"]), &beforeList) != nil || json.Unmarshal([]byte(texts["domain-list-after"]), &afterList) != nil || json.Unmarshal([]byte(texts["domain-get"]), &detail) != nil || len(beforeList.Issues) != 0 || len(afterList.Issues) != 1 || afterList.Issues[0].ID != receipt.IssueID || detail.Issue.ID != receipt.IssueID || detail.Issue.Goal != "One durable issue" {
		t.Fatal("real list/get results did not project the saved captured issue")
	}
	f.onUI(func() error {
		first, second := f.app.contactByID(aID), f.app.contactByID(bID)
		if len(first.Issues) != 1 || first.Issues[0].ID != receipt.IssueID || len(second.Issues) != 1 || second.Issues[0].ID != foreignIssue.ID || len(f.app.issueToolLedger.Operations) != 1 || !reflect.DeepEqual(f.app.issueToolLedger.Operations[0].Receipt, &receipt) {
			return errors.New("real product ledger did not retain exactly one captured create effect")
		}
		return nil
	})
	inactive := f.prepare(binding)
	arrived, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	denied := piAppCall{"issue_action", "domain-inactive", piAppArguments(map[string]any{"scope": inactive.params.Correlation.ScopeRef, "action": "create", "goal": "Forbidden inactive create"})}
	f.start(inactive, []piAppCall{denied}, arrived, release)
	select {
	case <-arrived:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	f.onUI(func() error {
		ct := f.app.contactByID(aID)
		ct.SendState, ct.ActiveTaskID, ct.pendingSend = sendIdle, "", nil
		return f.app.persist()
	})
	releaseOnce.Do(func() { close(release) })
	f.finish(inactive)
	inactiveText := f.tool(inactive, denied, "scope_inactive")
	f.onUI(func() error {
		if len(f.app.contactByID(aID).Issues) != 1 || len(f.app.issueToolLedger.Operations) != 1 {
			return errors.New("inactive scope created another durable effect")
		}
		return nil
	})
	draining := f.prepare(binding)
	drainArrived, drainRelease := make(chan struct{}), make(chan struct{})
	var drainOnce sync.Once
	t.Cleanup(func() { drainOnce.Do(func() { close(drainRelease) }) })
	query := piAppCall{"issue_list", "domain-drained-query", piAppArguments(map[string]any{"scope": draining.params.Correlation.ScopeRef})}
	f.start(draining, []piAppCall{query}, drainArrived, drainRelease)
	select {
	case <-drainArrived:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	callbacks := make(chan func(), 8)
	f.onUI(func() error {
		f.app.setUIHook(func(fn func()) { callbacks <- fn })
		return nil
	})
	t.Cleanup(func() {
		f.app.setUIHook(func(fn func()) { f.pump.q <- fn })
		for {
			select {
			case callback := <-callbacks:
				f.pump.do(callback)
			default:
				return
			}
		}
	})
	drainOnce.Do(func() { close(drainRelease) })
	var held func()
	select {
	case held = <-callbacks:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	var applyHeld sync.Once
	t.Cleanup(func() { applyHeld.Do(func() { f.pump.do(held) }) })
	f.mu.Lock()
	f.current = nil
	f.mu.Unlock()
	draining.bridge.closeAdmission()
	select {
	case <-draining.bridge.done:
		t.Fatal("bridge drained before its admitted actor callback completed")
	default:
	}
	drained := make(chan error, 1)
	go func() { drained <- draining.bridge.Drain(f.ctx) }()
	f.app.setUIHook(func(fn func()) { f.pump.q <- fn })
	applyHeld.Do(func() { f.pump.do(held) })
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	f.finish(draining)
	queryText := f.tool(draining, query, "")
	history := f.history(binding)
	for _, call := range calls {
		f.historyTool(history, call, texts[call.id])
	}
	f.historyTool(history, denied, inactiveText)
	f.historyTool(history, query, queryText)
	beforeRequests := f.provider.count()
	if beforeRequests != 12 {
		t.Fatalf("scoped loopback requests=%d want=12", beforeRequests)
	}
	var ledgerBefore []byte
	f.onUI(func() error {
		var err error
		ledgerBefore, err = json.Marshal(f.app.issueToolLedger)
		return err
	})
	if err := f.stop(f.ctx); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(f.app.store.path, f.project)
	if err != nil {
		t.Fatal(err)
	}
	contacts := contactsFromStore(store)
	ledgerAfter, err := json.Marshal(store.issueToolLedger)
	if err != nil || !bytes.Equal(ledgerBefore, ledgerAfter) || len(contacts[0].Issues) != 1 || contacts[0].Issues[0].ID != receipt.IssueID {
		t.Fatalf("reopened actual product ledger changed: error=%v", err)
	}
	piAppRecord(t, "reopened-ledger", map[string]any{"operations": store.issueToolLedger, "issue": contacts[0].Issues[0]})
	oldEpoch := f.ready.WorkerEpoch
	f.startWorker()
	reopened := f.open(aID, binding.SessionRef)
	if reopened.SessionRef != binding.SessionRef || !reopened.Used || f.ready.WorkerEpoch == oldEpoch {
		t.Fatalf("fresh session reopen binding=%+v", reopened)
	}
	after := f.history(reopened)
	for id, raw := range history {
		if !bytes.Equal(raw, after[id]) {
			t.Fatalf("reopen lost or changed public entry %s", id)
		}
	}
	if f.provider.count() != beforeRequests {
		t.Fatal("ledger/history reopen issued a model request")
	}
	if err := f.stop(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func (f *piAppFixture) question(binding pi.ContextBinding, issue *productIssue, text string) *issueQuestion {
	f.t.Helper()
	run := f.prepare(binding)
	var revision int
	f.onUI(func() error { revision = issue.RequirementsRevision; return nil })
	call := piAppCall{"issue_action", fmt.Sprintf("question-%d", f.turn), piAppArguments(map[string]any{"scope": run.params.Correlation.ScopeRef, "action": "wait", "issue_id": issue.ID, "expected_revision": revision, "text": text})}
	f.start(run, []piAppCall{call}, nil, nil)
	f.finish(run)
	result := f.tool(run, call, "")
	var receipt IssueToolReceipt
	if json.Unmarshal([]byte(result), &receipt) != nil || receipt.QuestionRef == "" || receipt.QuestionState != issueQuestionOpen {
		f.t.Fatalf("real product question receipt=%s", result)
	}
	var question *issueQuestion
	f.onUI(func() error {
		question = issue.questionByRef(receipt.QuestionRef)
		if question == nil || question != issue.openQuestion() || question.RequirementsRevision != revision {
			return errors.New("real question receipt is not the saved current question")
		}
		return nil
	})
	return question
}

func (f *piAppFixture) answerForm(issue *productIssue, question *issueQuestion, draft string) *ui.Tester {
	f.t.Helper()
	var tr *ui.Tester
	f.onUI(func() error {
		f.app.closeIssueSupplementForm()
		f.app.selectContact(0)
		f.app.selectIssue(issue.ID)
		tr = ui.NewTester(f.app.view, 1040, 1600)
		tr.Frame()
		if err := tr.Click(locales.WorkAnswer()); err != nil {
			return err
		}
		tr.Frame()
		captured := f.app.issueSupplementAnswerTarget
		if captured.ContactID != issue.ContactID || captured.IssueID != issue.ID || captured.RequirementsRevision != question.RequirementsRevision || f.app.issueSupplementQuestionRef != question.Ref {
			return errors.New("actual rendered Answer callback did not capture its displayed target")
		}
		return nil
	})
	f.formInput(tr, issue.ID, draft)
	return tr
}

func (f *piAppFixture) formInput(tr *ui.Tester, issueID, text string) {
	f.t.Helper()
	f.onUI(func() error {
		for i := 0; i < 64 && !tr.Focused(locales.SupplementCancel()); i++ {
			tr.Key(0, ui.KeyTab)
		}
		if !tr.Focused(locales.SupplementCancel()) {
			return errors.New("existing form Cancel control did not receive keyboard focus")
		}
		tr.Key(0, ui.KeyTab)
		tr.Key(ui.Cmd, ui.KeyA)
		tr.Type(text)
		tr.Frame()
		if *f.app.issueSupplementDraft(issueID) != text {
			return errors.New("actual form text area did not retain typed text")
		}
		return nil
	})
}

func (f *piAppFixture) saveForm(tr *ui.Tester, saveLabel string) {
	f.t.Helper()
	f.onUI(func() error {
		formID := f.app.issueSupplementFormID
		target := f.app.issueSupplementAnswerTarget
		questionRef := f.app.issueSupplementQuestionRef
		ownerID := f.app.selectedContact().ID
		inputLabel := locales.SupplementFormLabel()
		if saveLabel == locales.WorkAnswer() {
			inputLabel = locales.WorkAnswer()
		}
		if target.IssueID != "" {
			ownerID = target.ContactID
		}
		_, cancelPresent := tr.Find(locales.SupplementCancel())
		_, inputPresent := tr.Find(inputLabel)
		_, savePresent := tr.Find(saveLabel)
		beforeInputActive, _ := tr.TextInputActive()
		facts := map[string]any{
			"form_id": formID, "form_open": formID != "", "selected_contact_id": f.app.selectedContact().ID,
			"target": target, "question_ref": questionRef, "input_label": inputLabel, "save_label": saveLabel,
			"cancel_present": cancelPresent, "input_present": inputPresent, "save_present": savePresent,
			"cancel_focused_before": tr.Focused(locales.SupplementCancel()), "input_label_focused_before": tr.Focused(inputLabel), "save_label_focused_before": tr.Focused(saveLabel),
			"text_input_active_before": beforeInputActive,
		}
		piAppRecord(f.t, "form-focus-before", facts)
		if formID == "" || !cancelPresent || !inputPresent || !savePresent {
			return errors.New("the actual form or its Cancel/input/Save controls are absent; Save was not activated")
		}
		draft := f.app.issueSupplementDraft(formID)
		beforeDraft := *draft
		steps := 0
		for steps < 64 && !tr.Focused(locales.SupplementCancel()) {
			tr.Key(0, ui.KeyTab)
			steps++
		}
		facts["tabs_to_cancel"], facts["cancel_reacquired"] = steps, tr.Focused(locales.SupplementCancel())
		if !tr.Focused(locales.SupplementCancel()) {
			piAppRecord(f.t, "form-save-unreached", facts)
			return errors.New("the rendered form Cancel control did not receive focus; Save was not activated")
		}
		tr.Key(0, ui.KeyTab)
		inputActive, _ := tr.TextInputActive()
		facts["input_label_focused_after_cancel"] = tr.Focused(inputLabel)
		facts["text_input_active_after_cancel"] = inputActive
		if !tr.Focused(inputLabel) || !inputActive {
			piAppRecord(f.t, "form-save-unreached", facts)
			return errors.New("the expected form text input did not receive focus after Cancel; Save was not activated")
		}
		tr.Key(0, ui.KeyTab)
		saveInputActive, _ := tr.TextInputActive()
		facts["save_label_focused_after_input"] = tr.Focused(saveLabel)
		facts["text_input_active_at_save"] = saveInputActive
		facts["draft_before_activation"] = *draft
		piAppRecord(f.t, "form-save-before", facts)
		if !tr.Focused(saveLabel) || saveInputActive || *draft != beforeDraft {
			return errors.New("the expected non-text Save control is not focused or focusing changed the draft; Save was not activated")
		}
		tr.Key(0, ui.KeySpace)
		tr.Frame()
		afterInputActive, _ := tr.TextInputActive()
		association := map[string]any{"owner_id": ownerID, "issue_id": formID, "question_ref": questionRef}
		issue := issueForContact(f.app.contactByID(ownerID), formID)
		association["issue_found"] = issue != nil
		if issue != nil {
			association["issue_status"] = issue.Status
			association["supplement_count"] = 0
			if issue.Supplements != nil {
				association["supplement_count"] = len(issue.Supplements.Items)
			}
			if question := issue.questionByRef(questionRef); question != nil {
				association["question_state"], association["answer_supplement_id"] = question.State, question.AnswerSupplementID
			}
		}
		piAppRecord(f.t, "form-save-after", map[string]any{
			"form_id": f.app.issueSupplementFormID, "form_open": f.app.issueSupplementFormID != "",
			"target": f.app.issueSupplementAnswerTarget, "question_ref": f.app.issueSupplementQuestionRef, "save_label": saveLabel,
			"cancel_focused_after": tr.Focused(locales.SupplementCancel()), "input_label_focused_after": tr.Focused(inputLabel), "save_label_focused_after": tr.Focused(saveLabel),
			"text_input_active_after": afterInputActive,
			"error":                   f.app.issueActionError, "notice": f.app.issueActionNotice, "notice_detail": f.app.issueActionNoticeDetail,
			"draft": *draft, "association": association,
		})
		return nil
	})
}

func (f *piAppFixture) rejectedForm(tr *ui.Tester, issue *productIssue, draft string) {
	f.t.Helper()
	f.saveForm(tr, locales.WorkAnswer())
	f.onUI(func() error {
		if f.app.issueActionError == "" || !tr.HasText(f.app.issueActionError) || *f.app.issueSupplementDraft(issue.ID) != draft || issue.Supplements != nil && len(issue.Supplements.Items) != 0 {
			return fmt.Errorf("rejected form lost draft or created an answer: error=%q draft=%q", f.app.issueActionError, *f.app.issueSupplementDraft(issue.ID))
		}
		piAppRecord(f.t, "rejected-form", map[string]any{"issue": issue.ID, "question": f.app.issueSupplementQuestionRef, "target": f.app.issueSupplementAnswerTarget, "draft": draft, "error": f.app.issueActionError})
		return nil
	})
}

func TestPiAppCapturedQuestionAnswerForm(t *testing.T) {
	f := newPiAppFixture(t, 18, 1)
	var aID, bID string
	f.onUI(func() error { aID, bID = f.app.contacts[0].ID, f.app.contacts[1].ID; return nil })
	binding := f.open(aID, "")
	success := f.issue(aID, "Answer exactly once")
	question := f.question(binding, success, "Which delivery should be used?")
	answer := "Use the captured delivery.\n保留这一行。"
	tr := f.answerForm(success, question, answer)
	beforeSave := f.provider.count()
	f.saveForm(tr, locales.WorkAnswer())
	var supplementID string
	f.onUI(func() error {
		if question.State != issueQuestionAnswered || question.AnswerSupplementID == "" || success.Supplements == nil || len(success.Supplements.Items) != 1 || success.Supplements.Items[0].Text != answer || success.Supplements.Items[0].ID != question.AnswerSupplementID || f.app.issueSupplementFormID != "" || *f.app.issueSupplementDraft(success.ID) != "" {
			return errors.New("actual GUI save did not make one atomic answer/supplement association")
		}
		supplementID = question.AnswerSupplementID
		return nil
	})
	if f.provider.count() != beforeSave {
		t.Fatal("GUI answer Save issued a model request")
	}
	for i, text := range []string{answer, "A conflicting answer"} {
		run := f.prepare(binding)
		call := piAppCall{"issue_action", fmt.Sprintf("answer-repeat-%d", i), piAppArguments(map[string]any{"scope": run.params.Correlation.ScopeRef, "action": "add_supplement", "issue_id": success.ID, "question_ref": question.Ref, "text": text})}
		f.start(run, []piAppCall{call}, nil, nil)
		f.finish(run)
		code := ""
		if i == 1 {
			code = "question_answer_conflict"
		}
		result := f.tool(run, call, code)
		if i == 0 {
			var receipt IssueToolReceipt
			if json.Unmarshal([]byte(result), &receipt) != nil || receipt.SupplementID != supplementID || receipt.QuestionRef != question.Ref || receipt.QuestionState != issueQuestionAnswered {
				t.Fatalf("model-tool duplicate lost the GUI association: %s", result)
			}
		}
		f.historyTool(f.history(binding), call, result)
	}
	f.onUI(func() error {
		if len(success.Supplements.Items) != 1 || question.AnswerSupplementID != supplementID || success.Supplements.Items[0].Text != answer {
			return errors.New("duplicate/conflicting model answer changed the original GUI association")
		}
		return nil
	})
	superseded := f.issue(aID, "Captured superseded question")
	old := f.question(binding, superseded, "First question")
	staleDraft := "Draft for the first question"
	staleForm := f.answerForm(superseded, old, staleDraft)
	newer := f.question(binding, superseded, "Replacement at the same revision")
	f.onUI(func() error {
		if old.State != issueQuestionSuperseded || old.RequirementsRevision != newer.RequirementsRevision || old.Ref == newer.Ref {
			return errors.New("fixture did not produce a real same-revision replacement question")
		}
		staleForm.Frame()
		return nil
	})
	f.rejectedForm(staleForm, superseded, staleDraft)
	f.onUI(func() error {
		if newer.State != issueQuestionOpen || newer.AnswerSupplementID != "" || old.AnswerSupplementID != "" {
			return errors.New("old form answered or retargeted a replacement question")
		}
		return nil
	})
	stale := f.issue(aID, "Captured stale requirements")
	staleQuestion := f.question(binding, stale, "Answer before requirements change")
	staleRevisionForm := f.answerForm(stale, staleQuestion, "Preserve stale revision draft")
	f.onUI(func() error {
		if !f.app.editIssueFor(f.app.contactByID(aID), stale.ID, stale.Goal, "Changed requirement") {
			return errors.New(f.app.issueFormError)
		}
		staleRevisionForm.Frame()
		return nil
	})
	f.rejectedForm(staleRevisionForm, stale, "Preserve stale revision draft")
	completed := f.issue(aID, "Captured completed issue")
	completedQuestion := f.question(binding, completed, "Answer before explicit completion")
	completedForm := f.answerForm(completed, completedQuestion, "Preserve completed issue draft")
	f.onUI(func() error {
		if !f.app.completeIssueFor(f.app.contactByID(aID), completed.ID, "Completed explicitly by the fixture user") {
			return errors.New("real product completion was rejected")
		}
		completedForm.Frame()
		return nil
	})
	f.rejectedForm(completedForm, completed, "Preserve completed issue draft")
	owned := f.issue(aID, "Captured owner")
	ownedQuestion := f.question(binding, owned, "Only the captured owner may answer")
	ownedDraft := "Keep the original owner's draft"
	ownedForm := f.answerForm(owned, ownedQuestion, ownedDraft)
	f.onUI(func() error {
		f.app.selectContact(1)
		ownedForm.Frame()
		if f.app.issueSupplementAnswerTarget.ContactID != aID || *f.app.issueSupplementDraft(owned.ID) != ownedDraft {
			return errors.New("selection change retargeted or erased the captured answer")
		}
		_, err := f.app.answerIssueQuestionFor(f.app.contactByID(bID), owned.ID, ownedQuestion.Ref, &ownedQuestion.RequirementsRevision, ownedDraft, nil, nil)
		var domain *IssueToolError
		if !errors.As(err, &domain) || domain.Code != "not_found" || len(f.app.contactByID(bID).Issues) != 0 || ownedQuestion.AnswerSupplementID != "" {
			return fmt.Errorf("actual shared owner guard did not reject: %v", err)
		}
		f.app.selectContact(0)
		f.app.selectIssue(owned.ID)
		ownedForm.Frame()
		if err := ownedForm.Click(locales.SupplementCancel()); err != nil {
			return err
		}
		ownedForm.Frame()
		if f.app.issueSupplementQuestionRef != "" || f.app.issueSupplementAnswerTarget.IssueID != "" || *f.app.issueSupplementDraft(owned.ID) != ownedDraft {
			return errors.New("Cancel did not clear only transient answer routing")
		}
		return nil
	})
	ownedForm = f.answerForm(owned, ownedQuestion, ownedDraft)
	f.saveForm(ownedForm, locales.WorkAnswer())
	f.onUI(func() error {
		if ownedQuestion.State != issueQuestionAnswered || ownedQuestion.AnswerSupplementID == "" || len(owned.Supplements.Items) != 1 || len(f.app.contactByID(bID).Issues) != 0 {
			return errors.New("reopened captured owner form failed or mutated the other contact")
		}
		return nil
	})
	ordinary := f.issue(aID, "Ordinary follow-up stays ordinary")
	ordinaryQuestion := f.question(binding, ordinary, "This question must remain unanswered")
	var ordinaryForm *ui.Tester
	f.onUI(func() error {
		f.app.closeIssueSupplementForm()
		f.app.selectIssue(ordinary.ID)
		ordinaryForm = ui.NewTester(f.app.view, 1040, 1600)
		if err := ordinaryForm.Click(locales.IssueMoreActions()); err != nil {
			return err
		}
		ordinaryForm.Frame()
		if err := ordinaryForm.Click(locales.SupplementAdd()); err != nil {
			return err
		}
		ordinaryForm.Frame()
		return nil
	})
	f.formInput(ordinaryForm, ordinary.ID, "An ordinary follow-up, not a question answer")
	f.saveForm(ordinaryForm, locales.IssueSupplementSave())
	f.onUI(func() error {
		if ordinaryQuestion.State != issueQuestionOpen || ordinaryQuestion.AnswerSupplementID != "" || ordinary.Supplements == nil || len(ordinary.Supplements.Items) != 1 || ordinary.Supplements.Items[0].Text != "An ordinary follow-up, not a question answer" || f.app.issueSupplementFormID != "" {
			return errors.New("ordinary follow-up was retargeted to a question answer")
		}
		return nil
	})
	store, err := openContactStore(f.app.store.path, f.project)
	if err != nil {
		t.Fatal(err)
	}
	contacts := contactsFromStore(store)
	var saved *productIssue
	for _, ct := range contacts {
		if ct.ID == aID {
			saved = ct.issueByID(success.ID)
		}
	}
	if saved == nil || saved.questionByRef(question.Ref) == nil || saved.questionByRef(question.Ref).AnswerSupplementID != supplementID || saved.Supplements == nil || len(saved.Supplements.Items) != 1 || saved.Supplements.Items[0].Text != answer {
		t.Fatal("reopened real product store lost the original GUI answer association")
	}
	piAppRecord(t, "reopened-answer", saved)
	f.history(binding)
	if f.provider.count() != 18 {
		t.Fatalf("answer-form loopback requests=%d want=18", f.provider.count())
	}
	if err := f.stop(f.ctx); err != nil {
		t.Fatal(err)
	}
}
