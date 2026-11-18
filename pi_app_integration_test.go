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

	"github.com/egoist/mygo"
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
	config   pi.ConfigureParams
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
	f.config = pi.ConfigureParams{WireAPI: pi.OpenAICompletions, BaseURL: f.server.URL + "/v1", APIKey: "nagi-product-loopback", CredentialSource: "product-loopback-connection", Model: "nagi-product-fixture", ContextTokens: 65536, OutputTokens: 4096}
	if _, err := client.Configure(f.ctx, f.config); err != nil {
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
		ct.PiBinding = &piConversationBinding{
			Engine: "pi", Context: binding,
			Selection: piSelection{CWD: binding.CWD, CodingTools: []string{}},
			Configuration: piConfiguration{WireAPI: f.config.WireAPI, BaseURL: f.config.BaseURL,
				Model: f.config.Model, ContextTokens: f.config.ContextTokens, OutputTokens: f.config.OutputTokens,
				CredentialSource: f.config.CredentialSource},
		}
		ct.PiBinding.Context.Used = true
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
	storePath := f.app.store.path
	backupPath := storePath + ".scoped-failure-backup"
	backupHeld, obstruction := false, false
	observations := map[string]any{"store_path": storePath, "backup_path": backupPath}
	restoreStore := func() error {
		if obstruction {
			if err := os.Remove(storePath); err != nil {
				return err
			}
			obstruction = false
		}
		if backupHeld {
			if err := os.Rename(backupPath, storePath); err != nil {
				return err
			}
			backupHeld = false
		}
		return nil
	}
	t.Cleanup(func() {
		err := restoreStore()
		observations["cleanup_error"] = fmt.Sprint(err)
		piAppRecord(t, "scoped-store-failure", observations)
		if err != nil {
			t.Errorf("restore scoped store: %v", err)
		}
	})
	diskBefore, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var committedBefore []byte
	var savedIssues []storedIssue
	var savedOperations *IssueToolOperationLedger
	var originalName string
	f.onUI(func() error {
		var err error
		committedBefore, err = json.Marshal(f.app.store.state)
		if err != nil {
			return err
		}
		savedIssues = f.app.store.state.Issues
		savedOperations = f.app.store.state.IssueToolOperations
		originalName = f.app.contactByID(aID).Name
		observations["before"] = map[string]any{"disk_sha256": fmt.Sprintf("%x", sha256.Sum256(diskBefore)), "committed": json.RawMessage(committedBefore), "requests": f.provider.count(), "receipt": receipt}
		return nil
	})
	if err := os.Rename(storePath, backupPath); err != nil {
		t.Fatal(err)
	}
	backupHeld = true
	if err := os.Mkdir(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	obstruction = true
	const rejectedName = "Rejected profile draft"
	const savedName = "Saved profile after failure"
	f.onUI(func() error {
		ct := f.app.contactByID(aID)
		editor := f.app.ensureSettingsProfileEditor(aID)
		editor.Draft.Name = rejectedName
		if f.app.saveContactProfileDraftFor(aID, editor.Draft, false) {
			return errors.New("obstructed profile save unexpectedly succeeded")
		}
		committedAfter, err := json.Marshal(f.app.store.state)
		if err != nil {
			return err
		}
		diskAfter, err := os.ReadFile(backupPath)
		observations["failure"] = map[string]any{"error": editor.Error, "draft": editor.Draft.Name, "contact_name": ct.Name, "committed": json.RawMessage(committedAfter), "disk_sha256": fmt.Sprintf("%x", sha256.Sum256(diskAfter)), "requests": f.provider.count()}
		if err != nil || editor.Error != profileNoticeErrorSave || editor.Draft.Name != rejectedName || ct.Name != originalName || !bytes.Equal(committedBefore, committedAfter) || !bytes.Equal(diskBefore, diskAfter) || f.provider.count() != beforeRequests {
			return fmt.Errorf("failed profile save changed committed state or lost draft: %v", err)
		}
		return nil
	})
	if err := restoreStore(); err != nil {
		t.Fatal(err)
	}
	f.onUI(func() error {
		editor := f.app.ensureSettingsProfileEditor(aID)
		editor.Draft.Name = savedName
		if !f.app.saveContactProfileDraftFor(aID, editor.Draft, false) {
			return fmt.Errorf("profile save after restoration failed: %s", editor.Error)
		}
		observations["after"] = map[string]any{"name": f.app.contactByID(aID).Name, "requests": f.provider.count(), "receipt": f.app.issueToolLedger.Operations[0].Receipt}
		return nil
	})
	checkedStore, err := openContactStore(storePath, f.project)
	if err != nil {
		t.Fatal(err)
	}
	checkedContacts := contactsFromStore(checkedStore)
	var checkedContact *contact
	for _, ct := range checkedContacts {
		if ct.ID == aID {
			checkedContact = ct
		}
	}
	observations["reopen"] = map[string]any{"contact": checkedContact, "operations": checkedStore.issueToolLedger, "requests": f.provider.count()}
	if checkedContact == nil || checkedContact.Name != savedName || !reflect.DeepEqual(savedIssues, checkedStore.state.Issues) || !reflect.DeepEqual(savedOperations, checkedStore.issueToolLedger) || f.provider.count() != beforeRequests {
		t.Fatal("reopened store lost the legitimate profile or changed the original issue/receipt")
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
	storePath := f.app.store.path
	backupPath := storePath + ".answer-failure-backup"
	backupHeld, obstruction := false, false
	observations := map[string]any{"store_path": storePath, "backup_path": backupPath, "question_ref": question.Ref}
	restoreStore := func() error {
		if obstruction {
			if err := os.Remove(storePath); err != nil {
				return err
			}
			obstruction = false
		}
		if backupHeld {
			if err := os.Rename(backupPath, storePath); err != nil {
				return err
			}
			backupHeld = false
		}
		return nil
	}
	t.Cleanup(func() {
		err := restoreStore()
		observations["cleanup_error"] = fmt.Sprint(err)
		piAppRecord(t, "answer-store-failure", observations)
		if err != nil {
			t.Errorf("restore answer store: %v", err)
		}
	})
	diskBefore, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var committedBefore, operationsBefore []byte
	waitersBefore := map[string]int{}
	f.onUI(func() error {
		var err error
		committedBefore, err = json.Marshal(f.app.store.state)
		if err != nil {
			return err
		}
		operationsBefore, err = json.Marshal(f.app.issueToolLedger)
		for id, waiter := range f.app.issueToolWaiters {
			waitersBefore[id] = len(waiter)
		}
		observations["before"] = map[string]any{"disk_sha256": fmt.Sprintf("%x", sha256.Sum256(diskBefore)), "committed": json.RawMessage(committedBefore), "operations": json.RawMessage(operationsBefore), "question": *question, "waiters": waitersBefore, "requests": beforeSave}
		return err
	})
	f.mu.Lock()
	eventsBefore := len(f.events)
	f.mu.Unlock()
	if err := os.Rename(storePath, backupPath); err != nil {
		t.Fatal(err)
	}
	backupHeld = true
	if err := os.Mkdir(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	obstruction = true
	f.saveForm(tr, locales.WorkAnswer())
	f.onUI(func() error {
		committedAfter, err := json.Marshal(f.app.store.state)
		if err != nil {
			return err
		}
		operationsAfter, err := json.Marshal(f.app.issueToolLedger)
		if err != nil {
			return err
		}
		diskAfter, err := os.ReadFile(backupPath)
		supplementCount := 0
		if success.Supplements != nil {
			supplementCount = len(success.Supplements.Items)
		}
		waitersAfter := map[string]int{}
		for id, waiter := range f.app.issueToolWaiters {
			waitersAfter[id] = len(waiter)
		}
		observations["failure"] = map[string]any{"error": f.app.issueActionError, "draft": *f.app.issueSupplementDraft(success.ID), "question": *question, "supplement_count": supplementCount, "committed": json.RawMessage(committedAfter), "operations": json.RawMessage(operationsAfter), "disk_sha256": fmt.Sprintf("%x", sha256.Sum256(diskAfter)), "waiters": waitersAfter, "requests": f.provider.count()}
		if err != nil || f.app.issueActionError == "" || !tr.HasText(f.app.issueActionError) || *f.app.issueSupplementDraft(success.ID) != answer || f.app.issueSupplementFormID != success.ID || question.State != issueQuestionOpen || question.AnswerSupplementID != "" || success.Supplements != nil && len(success.Supplements.Items) != 0 || !bytes.Equal(committedBefore, committedAfter) || !bytes.Equal(operationsBefore, operationsAfter) || !bytes.Equal(diskBefore, diskAfter) || !reflect.DeepEqual(waitersBefore, waitersAfter) || f.provider.count() != beforeSave {
			return fmt.Errorf("failed answer save changed durable state or lost its draft: %v", err)
		}
		return nil
	})
	f.mu.Lock()
	eventsAfter := len(f.events)
	f.mu.Unlock()
	if eventsAfter != eventsBefore {
		t.Fatal("failed answer save emitted a Pi event")
	}
	if err := restoreStore(); err != nil {
		t.Fatal(err)
	}
	failedStore, err := openContactStore(storePath, f.project)
	if err != nil {
		t.Fatal(err)
	}
	var failedIssue *productIssue
	for _, ct := range contactsFromStore(failedStore) {
		if ct.ID == aID {
			failedIssue = ct.issueByID(success.ID)
		}
	}
	failedOperations, err := json.Marshal(failedStore.issueToolLedger)
	observations["reopen_failure"] = map[string]any{"issue": failedIssue, "operations": json.RawMessage(failedOperations), "events_before": eventsBefore, "events_after": eventsAfter}
	if err != nil || failedIssue == nil || failedIssue.openQuestion() == nil || failedIssue.openQuestion().Ref != question.Ref || failedIssue.openQuestion().AnswerSupplementID != "" || failedIssue.Supplements != nil && len(failedIssue.Supplements.Items) != 0 || !bytes.Equal(operationsBefore, failedOperations) {
		t.Fatalf("reopened failed answer changed question or receipt: %v", err)
	}
	f.saveForm(tr, locales.WorkAnswer())
	var supplementID string
	f.onUI(func() error {
		if question.State != issueQuestionAnswered || question.AnswerSupplementID == "" || success.Supplements == nil || len(success.Supplements.Items) != 1 || success.Supplements.Items[0].Text != answer || success.Supplements.Items[0].ID != question.AnswerSupplementID || f.app.issueSupplementFormID != "" || *f.app.issueSupplementDraft(success.ID) != "" {
			return errors.New("actual GUI save did not make one atomic answer/supplement association")
		}
		supplementID = question.AnswerSupplementID
		observations["after"] = map[string]any{"question": *question, "supplement_id": supplementID, "draft": *f.app.issueSupplementDraft(success.ID), "requests": f.provider.count()}
		return nil
	})
	if f.provider.count() != beforeSave {
		t.Fatal("GUI answer Save issued a model request")
	}
	answeredStore, err := openContactStore(storePath, f.project)
	if err != nil {
		t.Fatal(err)
	}
	var answeredIssue *productIssue
	for _, ct := range contactsFromStore(answeredStore) {
		if ct.ID == aID {
			answeredIssue = ct.issueByID(success.ID)
		}
	}
	observations["reopen_after"] = map[string]any{"issue": answeredIssue, "operations": answeredStore.issueToolLedger, "requests": f.provider.count()}
	if answeredIssue == nil || answeredIssue.questionByRef(question.Ref) == nil || answeredIssue.questionByRef(question.Ref).AnswerSupplementID != supplementID || answeredIssue.Supplements == nil || len(answeredIssue.Supplements.Items) != 1 || answeredIssue.Supplements.Items[0].Text != answer {
		t.Fatal("reopened store lost the successful answer after the failed save")
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

type piFirstUseRequest struct {
	Endpoint string          `json:"endpoint"`
	Path     string          `json:"path"`
	AuthOK   bool            `json:"auth_ok"`
	Body     json.RawMessage `json:"body"`
}

type piFirstUseHarness struct {
	t             *testing.T
	app           *appModel
	pump          *uiPump
	tester        *ui.Tester
	provider      *piAppProvider
	servers       []*httptest.Server
	mu            sync.Mutex
	requests      []piFirstUseRequest
	streamStarted chan struct{}
	streamRelease <-chan struct{}
	hosts         []*piAppHost
	probes        []*piConnectionProbe
	capturedHost  *piAppHost
	closed        bool
}

func (h *piFirstUseHarness) onUI(fn func() error) {
	h.t.Helper()
	var err error
	h.pump.do(func() { err = fn() })
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *piFirstUseHarness) click(label string) {
	h.t.Helper()
	h.onUI(func() error {
		h.tester.Frame()
		if bounds, ok := h.tester.Find(label); !ok || bounds.Y < 0 || bounds.Y+bounds.H > 720 {
			h.tester.Scroll(520, 360, 0, -3000)
			h.tester.Frame()
		}
		if err := clickSettingsAction(h.tester, label); err != nil {
			return err
		}
		h.tester.Frame()
		return nil
	})
}

func (h *piFirstUseHarness) focus(label string, input bool) error {
	h.tester.Frame()
	for range 128 {
		active, _ := h.tester.TextInputActive()
		if h.tester.Focused(label) && active == input {
			return nil
		}
		h.tester.Key(0, ui.KeyTab)
		h.tester.Frame()
	}
	return fmt.Errorf("controller %q was not keyboard reachable as text input=%v", label, input)
}

func (h *piFirstUseHarness) activate(label string) {
	h.t.Helper()
	h.onUI(func() error {
		if err := h.focus(label, false); err != nil {
			return err
		}
		h.tester.Key(0, ui.KeySpace)
		h.tester.Frame()
		return nil
	})
}

func (h *piFirstUseHarness) selectOption(current, option string, options ...string) {
	h.t.Helper()
	h.onUI(func() error {
		if err := h.focus(current, false); err != nil {
			return err
		}
		h.tester.Key(0, ui.KeySpace)
		h.tester.Frame()
		for _, expected := range options {
			if !h.tester.HasText(expected) {
				return fmt.Errorf("select %q did not reveal option %q", current, expected)
			}
		}
		if err := h.tester.Click(option); err != nil {
			return err
		}
		h.tester.Frame()
		return nil
	})
}

func (h *piFirstUseHarness) input(label, text string) {
	h.t.Helper()
	h.onUI(func() error {
		if err := h.focus(label, true); err != nil {
			return err
		}
		h.tester.Key(ui.Cmd, ui.KeyA)
		h.tester.Type(text)
		h.tester.Frame()
		form := h.app.connectionForm
		if form == nil {
			return errors.New("connection form disappeared while typing")
		}
		var actual string
		switch label {
		case "API endpoint":
			actual = form.Draft.Endpoint
		case "API key":
			actual = form.Draft.APIKey
		case "Default model":
			actual = form.Draft.Model
		case "Context tokens":
			actual = form.Draft.ContextTokens
		case "Output tokens":
			actual = form.Draft.OutputTokens
		case "Maximum active tasks":
			actual = form.Draft.Slots
		default:
			return fmt.Errorf("unknown connection input %q", label)
		}
		if actual != text {
			return fmt.Errorf("input %q did not retain the typed value", label)
		}
		return nil
	})
}

type piFirstUseWriter struct {
	http.ResponseWriter
	started chan struct{}
	release <-chan struct{}
	ctx     context.Context
	flushed bool
}

func (w *piFirstUseWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if !w.flushed && w.started != nil {
		w.flushed = true
		if flush, ok := w.ResponseWriter.(http.Flusher); ok {
			flush.Flush()
		}
		close(w.started)
		select {
		case <-w.release:
		case <-w.ctx.Done():
		}
	}
	return n, err
}

func (h *piFirstUseHarness) endpoint(name string) string {
	h.t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h.mu.Lock()
		h.requests = append(h.requests, piFirstUseRequest{Endpoint: name, Path: r.URL.Path,
			AuthOK: r.Header.Get("Authorization") == "Bearer nagi-product-loopback", Body: append(json.RawMessage(nil), body...)})
		started, release := h.streamStarted, h.streamRelease
		h.streamStarted, h.streamRelease = nil, nil
		h.mu.Unlock()
		h.provider.ServeHTTP(&piFirstUseWriter{ResponseWriter: w, started: started, release: release, ctx: r.Context()}, r)
	}))
	h.servers = append(h.servers, server)
	return server.URL + "/v1"
}

func (h *piFirstUseHarness) script(arrived chan struct{}, release <-chan struct{}) {
	h.t.Helper()
	if err := h.provider.install([]piAppCall{}, arrived, release); err != nil {
		h.t.Fatal(err)
	}
}

func (h *piFirstUseHarness) profileSelection(expected *[]string) {
	h.t.Helper()
	h.onUI(func() error {
		ct := h.app.selectedContact()
		if ct == nil || h.app.store == nil || !reflect.DeepEqual(ct.BuiltinTools, expected) {
			return errors.New("profile selection did not match the saved draft")
		}
		data, err := os.ReadFile(h.app.store.path)
		if err != nil {
			return err
		}
		var state storedState
		if err := json.Unmarshal(data, &state); err != nil {
			return err
		}
		for _, stored := range state.Contacts {
			if stored.ID == ct.ID {
				if !reflect.DeepEqual(stored.BuiltinTools, expected) {
					return errors.New("durable profile selection differs from the GUI selection")
				}
				return nil
			}
		}
		return errors.New("saved contact is absent from the product store")
	})
}

func piFirstUseCodingTools(active, supported []string) map[string]bool {
	names := make(map[string]bool)
	for _, name := range active {
		for _, codingName := range supported {
			if name == codingName {
				names[name] = true
			}
		}
	}
	return names
}

func (h *piFirstUseHarness) send(text string) {
	h.t.Helper()
	h.onUI(func() error {
		ct := h.app.selectedContact()
		if ct == nil {
			return errors.New("no selected contact for Send")
		}
		label := locales.In("en").ComposerLabel(ct.Name)
		if err := h.focus(label, true); err != nil {
			return err
		}
		h.tester.Key(ui.Cmd, ui.KeyA)
		h.tester.Type(text)
		h.tester.Frame()
		if ct.Draft != text {
			return errors.New("composer did not retain the exact draft")
		}
		if err := h.tester.Click(locales.In("en").SendAction()); err != nil {
			return err
		}
		h.tester.Frame()
		if ct.SendState != sendSubmitting || ct.pendingSend == nil {
			return fmt.Errorf("Send did not submit the captured draft: state=%v error=%q", ct.SendState, ct.SendError)
		}
		return nil
	})
}

func (h *piFirstUseHarness) actor(ctx context.Context, fn func()) error {
	done := make(chan struct{})
	select {
	case h.pump.q <- func() { fn(); h.pump.captureProcessIdentity(); close(done) }:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *piFirstUseHarness) waitUI(ctx context.Context, condition func() bool) error {
	waiter := &uiWaiter{cond: condition, ch: make(chan struct{})}
	h.pump.mu.Lock()
	h.pump.waiters = append(h.pump.waiters, waiter)
	h.pump.mu.Unlock()
	defer func() {
		h.pump.mu.Lock()
		for i, current := range h.pump.waiters {
			if current == waiter {
				h.pump.waiters = append(h.pump.waiters[:i], h.pump.waiters[i+1:]...)
				break
			}
		}
		h.pump.mu.Unlock()
	}()
	select {
	case h.pump.q <- func() {}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-waiter.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *piFirstUseHarness) shutdown() error {
	if h.closed {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	owners := make(chan *piAppHost, 1)
	if err := h.actor(ctx, func() {
		owners <- h.app.pi
		h.app.beginShutdown()
	}); err != nil {
		return fmt.Errorf("request app shutdown: %w", err)
	}
	host := <-owners
	if host != nil {
		h.capturedHost = host
	}
	if err := h.waitUI(ctx, func() bool { return h.app.quitReady }); err != nil {
		return fmt.Errorf("wait for app shutdown: %w", err)
	}
	drainResult := make(chan string, 1)
	if err := h.actor(ctx, func() {
		if h.app.runtimeErrKind == runtimeNoticeRuntimeShutdown {
			drainResult <- h.app.runtimeErr
		} else {
			drainResult <- ""
		}
	}); err != nil {
		return fmt.Errorf("read app shutdown result: %w", err)
	}
	drainError := <-drainResult
	if host != nil {
		if host.client == nil {
			return errors.New("app shutdown lost its owned Pi client")
		}
		outcome, err := host.client.Wait(ctx)
		select {
		case <-host.client.CallbacksDone():
		case <-ctx.Done():
			err = errors.Join(err, ctx.Err())
		}
		if host.exitApplied == nil {
			err = errors.Join(err, errors.New("app exit callback receipt is unavailable"))
		} else {
			select {
			case <-host.exitApplied:
			case <-ctx.Done():
				err = errors.Join(err, ctx.Err())
			}
		}
		host.mu.Lock()
		observed := host.outcome
		host.mu.Unlock()
		piAppRecord(h.t, "app-worker-exit", map[string]any{"wait": outcome, "callback": observed, "callbacks_done": shutdownChannelClosed(host.client.CallbacksDone()), "error": fmt.Sprint(err), "drain_error": drainError})
		if err != nil || drainError != "" || !outcome.Reaped || outcome.ExitCode != 0 || outcome.PID <= 0 || observed.PID != outcome.PID || !observed.Reaped || observed.ExitCode != outcome.ExitCode {
			return fmt.Errorf("actual app worker did not drain and reap: wait=%+v callback=%+v error=%v drain=%q", outcome, observed, err, drainError)
		}
	} else if drainError != "" {
		return fmt.Errorf("app shutdown failed: %s", drainError)
	}
	h.closed = true
	close(h.pump.q)
	select {
	case <-h.pump.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("close UI pump: %w", ctx.Err())
	}
}

func (h *piFirstUseHarness) cleanupClient(client *pi.Client, host *piAppHost, source string) bool {
	if client == nil {
		return true
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	outcome, waitErr := client.Wait(waitCtx)
	waitCancel()
	forced := false
	var killErr error
	if !outcome.Reaped {
		forced = true
		killCtx, killCancel := context.WithTimeout(context.Background(), 10*time.Second)
		outcome, killErr = client.Kill(killCtx)
		killCancel()
		h.t.Errorf("owned %s worker required forced cleanup: %v", source, killErr)
	}
	finalCtx, finalCancel := context.WithTimeout(context.Background(), 2*time.Second)
	finalOutcome, finalErr := client.Wait(finalCtx)
	finalCancel()
	callbackCtx, callbackCancel := context.WithTimeout(context.Background(), 10*time.Second)
	callbacksDone := false
	select {
	case <-client.CallbacksDone():
		callbacksDone = true
	case <-callbackCtx.Done():
	}
	exitApplied := host == nil
	var observed pi.ProcessOutcome
	if host != nil {
		if host.exitApplied != nil {
			select {
			case <-host.exitApplied:
				exitApplied = true
			case <-callbackCtx.Done():
			}
		}
		host.mu.Lock()
		observed = host.outcome
		host.mu.Unlock()
	}
	callbackErr := callbackCtx.Err()
	callbackCancel()
	piAppRecord(h.t, "owned-client-cleanup", map[string]any{"source": source, "wait": outcome, "wait_error": fmt.Sprint(waitErr), "final_wait": finalOutcome, "final_wait_error": fmt.Sprint(finalErr), "forced": forced, "kill_error": fmt.Sprint(killErr), "callbacks_done": callbacksDone, "callback_error": fmt.Sprint(callbackErr), "on_exit": observed, "exit_applied": exitApplied, "has_exit_handler": host != nil})
	if !forced && (waitErr != nil || outcome.Err != nil || outcome.ExitCode != 0) {
		h.t.Errorf("owned %s worker exited unsuccessfully: outcome=%+v error=%v", source, outcome, waitErr)
	}
	if !outcome.Reaped || !finalOutcome.Reaped || finalOutcome.PID <= 0 || finalOutcome.PID != outcome.PID || finalOutcome.ExitCode != outcome.ExitCode || finalErr != nil && (finalOutcome.Err == nil || !errors.Is(finalErr, finalOutcome.Err)) || !callbacksDone || !exitApplied || host != nil && (observed.PID != finalOutcome.PID || !observed.Reaped || observed.ExitCode != finalOutcome.ExitCode || fmt.Sprint(observed.Err) != fmt.Sprint(finalOutcome.Err)) {
		h.t.Errorf("owned %s cleanup is incomplete: wait=%+v on_exit=%+v callbacks=%v exit_applied=%v", source, outcome, observed, callbacksDone, exitApplied)
		return false
	}
	return true
}

func (h *piFirstUseHarness) cleanup() {
	defer func() {
		for _, server := range h.servers {
			server.CloseClientConnections()
			done := make(chan struct{})
			go func() { server.Close(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				h.t.Error("controlled loopback server did not close")
			}
		}
	}()
	type owners struct {
		host     *piAppHost
		probe    *piConnectionProbe
		observed *pi.Client
		facts    map[string]any
	}
	capture := func() (owners, error) {
		result := make(chan owners, 1)
		read := func() {
			value := owners{host: h.app.pi, probe: h.app.connectionProbe, observed: h.pump.observedPi}
			value.facts = map[string]any{"store_path": h.app.storePath, "local_save_error": h.app.localSaveErr, "runtime_status": h.app.runtimeStatus, "connection_applying": h.app.connectionApplying}
			if h.app.connectionSaved != nil {
				config := h.app.connectionSaved
				value.facts["saved_connection"] = map[string]any{"endpoint": config.Endpoint, "wire_api": config.API, "model": config.Model, "context_tokens": config.ContextTokens, "output_tokens": config.OutputTokens, "working_directory": config.WorkingDirectory}
			}
			if h.app.store != nil {
				digest, err := piAppFileHash(h.app.store.path)
				value.facts["store_sha256"], value.facts["store_hash_error"] = digest, fmt.Sprint(err)
				value.facts["contact_count"], value.facts["selected_id"] = len(h.app.contacts), h.app.selectedID()
			}
			result <- value
		}
		if h.closed {
			select {
			case <-h.pump.done:
				read()
			default:
				return owners{}, errors.New("closed UI pump has not stopped")
			}
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := h.actor(ctx, read)
			cancel()
			if err != nil {
				return owners{}, err
			}
		}
		return <-result, nil
	}
	last, captureErr := capture()
	if captureErr != nil {
		h.t.Errorf("capture cleanup owner: %v", captureErr)
	} else {
		piAppRecord(h.t, "first-use-state", last.facts)
	}
	h.mu.Lock()
	piAppRecord(h.t, "first-use-requests", h.requests)
	h.mu.Unlock()
	h.provider.mu.Lock()
	piAppRecord(h.t, "first-use-provider", map[string]any{"requests": h.provider.requests, "limit": h.provider.limit, "problems": h.provider.problems})
	if len(h.provider.problems) != 0 {
		h.t.Errorf("controlled provider problems: %v", h.provider.problems)
	}
	h.provider.mu.Unlock()
	if h.pump != nil && !h.closed {
		observedHost := make(chan *piAppHost, 1)
		watcher := &uiWaiter{ch: make(chan struct{}), cond: func() bool {
			if h.app.pi == nil || h.app.pi.client == nil {
				return false
			}
			observedHost <- h.app.pi
			return true
		}}
		h.pump.mu.Lock()
		h.pump.waiters = append(h.pump.waiters, watcher)
		h.pump.mu.Unlock()
		if err := h.shutdown(); err != nil {
			h.t.Errorf("normal app shutdown failed: %v", err)
		}
		h.pump.mu.Lock()
		for i, current := range h.pump.waiters {
			if current == watcher {
				h.pump.waiters = append(h.pump.waiters[:i], h.pump.waiters[i+1:]...)
				break
			}
		}
		h.pump.mu.Unlock()
		select {
		case host := <-observedHost:
			h.hosts = append(h.hosts, host)
		default:
		}
		if updated, captureErr := capture(); captureErr != nil {
			h.t.Errorf("capture owner after shutdown: %v", captureErr)
		} else {
			if updated.host == nil {
				updated.host = last.host
			}
			if updated.probe == nil {
				updated.probe = last.probe
			}
			last = updated
		}
	}
	seen := map[*pi.Client]bool{}
	complete := true
	for _, host := range append(append([]*piAppHost{}, h.hosts...), h.capturedHost, last.host) {
		if host == nil {
			continue
		}
		if host.client == nil {
			h.t.Error("captured app host has no owned client")
			complete = false
			continue
		}
		if seen[host.client] {
			continue
		}
		seen[host.client] = true
		complete = h.cleanupClient(host.client, host, "app") && complete
	}
	seenProbes := map[*piConnectionProbe]bool{}
	for _, probe := range append(append([]*piConnectionProbe{}, h.probes...), last.probe) {
		if probe == nil || seenProbes[probe] {
			continue
		}
		seenProbes[probe] = true
		probe.mu.Lock()
		client, outcome, callbacksDone, forced := probe.client, probe.outcome, probe.callbacksDone, probe.forced
		testErr, cleanupErr := probe.testErr, probe.cleanupErr
		probe.mu.Unlock()
		piAppRecord(h.t, "probe-cleanup-owner", map[string]any{"outcome": outcome, "callbacks_done": callbacksDone, "forced": forced, "test_error": fmt.Sprint(testErr), "cleanup_error": fmt.Sprint(cleanupErr), "has_exit_handler": false})
		if forced || cleanupErr != nil || testErr != nil {
			h.t.Errorf("connection probe did not complete cleanly: forced=%v test=%v cleanup=%v", forced, testErr, cleanupErr)
		}
		if client == nil || seen[client] {
			continue
		}
		seen[client] = true
		complete = h.cleanupClient(client, nil, "probe") && complete
	}
	if last.observed != nil && !seen[last.observed] {
		seen[last.observed] = true
		complete = h.cleanupClient(last.observed, nil, "observed-app") && complete
		h.t.Error("app worker exit owner was unavailable during cleanup")
	}
	if !h.closed {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := h.waitUI(ctx, func() bool { return h.app.quitReady })
		quit := err == nil
		cancel()
		if complete && quit {
			h.closed = true
			close(h.pump.q)
			select {
			case <-h.pump.done:
			case <-time.After(5 * time.Second):
				h.t.Error("owned UI pump did not stop")
			}
		} else {
			h.t.Error("UI pump retained because app shutdown or callback ownership is unconfirmed")
		}
	}
}

func TestPiAppConnectionAndConversation(t *testing.T) {
	inputs := map[string]string{}
	paths := make([]string, 0, 2)
	for _, name := range []string{"NAGI_TEST_NODE", "NAGI_TEST_WORKER"} {
		path := os.Getenv(name)
		if !filepath.IsAbs(path) {
			t.Fatalf("%s must identify an explicit pinned absolute input", name)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s must identify a regular file: %v", name, err)
		}
		hash, err := piAppFileHash(resolved)
		if err != nil {
			t.Fatal(err)
		}
		inputs[name] = hash
		paths = append(paths, resolved)
	}
	payload := filepath.Dir(filepath.Dir(paths[0]))
	if paths[0] != filepath.Join(payload, "bin", "node") || paths[1] != filepath.Join(payload, "dist", "worker.js") {
		t.Fatal("pinned Node and worker do not belong to one actual product payload tree")
	}
	for _, path := range []string{"pi_app_integration_test.go", "pi_app.go", "connection_app.go", "connection_settings.go", "store.go", "internal/pi/worker/worker.ts"} {
		hash, err := piAppFileHash(path)
		if err != nil {
			t.Fatal(err)
		}
		inputs[path] = hash
	}
	piAppRecord(t, "first-use-source-and-inputs", inputs)
	root := t.TempDir()
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("TMPDIR", root)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", "")
	isolateConnectionApp(t, root, payload)
	storePath := guiStorePath()
	broken := filepath.Join(root, "broken", "contacts.json")
	if err := os.MkdirAll(filepath.Dir(broken), 0o700); err != nil {
		t.Fatal(err)
	}
	brokenBytes := []byte("{unreadable synthetic store\n")
	if err := os.WriteFile(broken, brokenBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAKI_GUI_STORE", broken)
	if _, err := newRuntimeApp(); err == nil {
		t.Fatal("unreadable existing store was accepted")
	}
	afterBroken, err := os.ReadFile(broken)
	if err != nil || !bytes.Equal(afterBroken, brokenBytes) {
		t.Fatalf("unreadable original store changed: %v", err)
	}
	t.Setenv("SAKI_GUI_STORE", storePath)
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	h := &piFirstUseHarness{t: t, app: app, provider: &piAppProvider{limit: 16}}
	h.pump = newUIPump(t, app)
	h.tester = ui.NewTester(app.view, 1040, 720)
	h.tester.SetLocale("en")
	h.tester.SetFocused(true)
	t.Cleanup(h.cleanup)
	endpointA, endpointB := h.endpoint("initial"), h.endpoint("replacement")
	var firstID string
	h.onUI(func() error {
		if !app.settingsOpen || app.settingsTab != settingsTabConnection || app.connectionForm == nil || app.pi != nil || app.selectedContact() == nil {
			return errors.New("fresh app did not open disconnected Connection settings")
		}
		firstID = app.selectedContact().ID
		h.tester.Frame()
		return nil
	})
	h.click("← Back to workbench")
	h.onUI(func() error {
		if err := h.focus("Composer for Saki", true); err != nil {
			return err
		}
		h.tester.Type("Keep the offline draft")
		h.tester.Key(0, ui.KeyEnter)
		h.tester.Frame()
		if app.selectedContact().Draft != "Keep the offline draft" || len(app.selectedContact().Messages) != 0 || app.pi != nil {
			return errors.New("disconnected Send changed the draft or fabricated a reply")
		}
		if !h.tester.HasText("Open connection settings") {
			return errors.New("disconnected Send did not offer connection settings")
		}
		return nil
	})
	h.click("Open connection settings")
	h.onUI(func() error {
		if !app.settingsOpen || app.settingsTab != settingsTabConnection {
			return errors.New("disconnected recovery did not open Connection settings")
		}
		return nil
	})
	h.click("← Back to workbench")
	h.click("Add contact")
	var secondID string
	h.onUI(func() error {
		ct := app.selectedContact()
		if ct == nil || ct.ID == firstID || app.contactByID(firstID) == nil {
			return errors.New("offline Add contact did not create a separate product contact")
		}
		secondID = ct.ID
		return nil
	})
	h.click("Settings")
	h.click("General")
	h.selectOption("System", "Dark", "System", "Light", "Dark")
	h.onUI(func() error {
		if app.appearanceMode != appearanceDark {
			return errors.New("appearance controller did not apply Dark")
		}
		return nil
	})
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
	app, err = newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	h.app, h.pump, h.closed = app, newUIPump(t, app), false
	h.tester = ui.NewTester(app.view, 1040, 720)
	h.tester.SetLocale("en")
	h.tester.SetFocused(true)
	h.onUI(func() error {
		first := app.contactByID(firstID)
		if first == nil || first.Draft != "Keep the offline draft" || app.contactByID(secondID) == nil || app.appearanceMode != appearanceDark || app.pi != nil {
			return errors.New("offline contact, draft or general preference did not survive restart")
		}
		if app.selectedContact() == nil || app.selectedContact().ID != secondID || !app.settingsOpen || app.settingsTab != settingsTabConnection || app.connectionForm == nil {
			return errors.New("restart lost its selected contact or disconnected Connection page")
		}
		h.tester.Frame()
		return nil
	})
	if h.provider.count() != 0 {
		t.Fatal("offline preparation made a model request")
	}
	h.onUI(func() error {
		form := app.connectionForm
		if form == nil || form.Draft.ContextTokens != "131072" || form.Draft.OutputTokens != "8192" || form.Draft.Slots != "2" || form.LimitsOpen {
			return errors.New("fresh connection defaults or advanced disclosure differ")
		}
		h.tester.Frame()
		for _, text := range []string{"Capacity & execution limits", "Context tokens: 131072", "Output tokens: 8192", "Maximum active tasks: 2"} {
			if !h.tester.HasText(text) {
				return fmt.Errorf("fresh Connection did not disclose %q", text)
			}
		}
		return nil
	})
	h.selectOption("Chat completions", "Responses", "Chat completions", "Responses")
	h.onUI(func() error {
		if app.connectionForm.Draft.API != connectionAPIResponses {
			return errors.New("protocol controller did not select Responses")
		}
		return nil
	})
	h.selectOption("Responses", "Chat completions", "Chat completions", "Responses")
	h.onUI(func() error {
		if app.connectionForm.Draft.API != connectionAPIChatCompletions {
			return errors.New("protocol controller did not select Chat completions")
		}
		return nil
	})
	h.input("API endpoint", endpointA)
	h.input("API key", "nagi-product-loopback")
	h.input("Default model", "nagi-product-fixture")
	h.onUI(func() error {
		if err := h.focus("Capacity & execution limits", false); err != nil {
			return err
		}
		h.tester.Key(0, ui.KeySpace)
		h.tester.Frame()
		if !app.connectionForm.LimitsOpen {
			return errors.New("capacity disclosure did not open")
		}
		for _, label := range []string{"Context tokens", "Output tokens", "Maximum active tasks"} {
			if !h.tester.HasText(label) {
				return fmt.Errorf("expanded capacity controls omit %q", label)
			}
		}
		return nil
	})
	h.input("Context tokens", "65536")
	h.input("Output tokens", "4096")
	h.input("Maximum active tasks", "2")
	h.onUI(func() error {
		app.chooseFolder = func(mygo.OpenDialogOptions) ([]string, error) { return []string{project}, nil }
		return nil
	})
	h.click("Choose folder…")
	h.pump.wait(func() bool { return !app.connectionForm.Busy })
	h.onUI(func() error {
		if app.connectionForm.Draft.WorkingDirectory != project || app.connectionForm.Draft.API != connectionAPIChatCompletions {
			return errors.New("actual folder or protocol control did not update the connection draft")
		}
		return nil
	})
	h.click("Save configuration")
	h.pump.wait(func() bool { return !app.connectionForm.Busy && !app.connectionSaveInFlight })
	h.onUI(func() error {
		if app.connectionForm.Error != "" || app.connectionSaved == nil || app.pi != nil {
			return fmt.Errorf("Save did not leave a saved, unapplied connection: %s", app.connectionForm.Error)
		}
		return nil
	})
	if h.provider.count() != 0 {
		t.Fatal("Save made a model request")
	}
	h.script(nil, nil)
	h.click("Test connection and model")
	var probe *piConnectionProbe
	h.onUI(func() error { probe = app.connectionProbe; return nil })
	if probe == nil {
		t.Fatal("actual Test did not capture its probe owner")
	}
	h.probes = append(h.probes, probe)
	h.pump.waitTimeout(func() bool { return !app.connectionTesting && !app.connectionForm.Busy }, 45*time.Second)
	probe.mu.Lock()
	probeOutcome, probeCallbacks, forced, probeErr := probe.outcome, probe.callbacksDone, probe.forced, probe.testErr
	probe.mu.Unlock()
	piAppRecord(t, "connection-test-process", map[string]any{"outcome": probeOutcome, "callbacks_done": probeCallbacks, "forced": forced, "error": fmt.Sprint(probeErr)})
	if probeErr != nil || forced || !probeOutcome.Reaped || probeOutcome.ExitCode != 0 || !probeCallbacks || h.provider.count() != 1 {
		t.Fatalf("real connection Test or probe cleanup failed: outcome=%+v callbacks=%v forced=%v error=%v", probeOutcome, probeCallbacks, forced, probeErr)
	}
	h.onUI(func() error {
		if app.connectionForm.Status != connStatusTestCompleted || app.connectionForm.Error != "" {
			return fmt.Errorf("actual Test did not report success: status=%q error=%q", app.connectionForm.Status, app.connectionForm.Error)
		}
		return nil
	})
	h.click("Apply saved")
	h.pump.waitTimeout(func() bool { return !app.connectionApplying }, 45*time.Second)
	var host *piAppHost
	h.onUI(func() error {
		host = app.pi
		if host == nil || host.client == nil || app.runtimeStatus != runtimeStateReady || !app.runtimeBuiltinToolsLoaded || app.connectionForm.Error != "" {
			return errors.New("actual Apply did not publish a ready Pi host and catalog")
		}
		return nil
	})
	h.hosts = append(h.hosts, host)
	ready, err := host.client.Ready(context.Background())
	if err != nil || ready.BuiltinCatalog == nil {
		t.Fatalf("actual worker catalog unavailable: %v", err)
	}
	var supported []string
	for _, tool := range ready.BuiltinCatalog.Supported {
		supported = append(supported, tool.Name)
	}
	if !reflect.DeepEqual(supported, []string{"read", "bash", "edit", "write", "grep", "find", "ls"}) || !reflect.DeepEqual(ready.BuiltinCatalog.Defaults, []string{"read", "bash", "edit", "write"}) || h.provider.count() != 1 {
		t.Fatalf("actual catalog or Apply request count differs: %+v requests=%d", ready.BuiltinCatalog, h.provider.count())
	}
	piAppRecord(t, "actual-ready-catalog", ready)
	h.click("Contact profile")
	h.click("Save changes")
	h.profileSelection(nil)
	defaultLabel := "Use host defaults (" + strings.Join(ready.BuiltinCatalog.Defaults, ", ") + ")"
	h.activate(defaultLabel)
	for _, name := range ready.BuiltinCatalog.Defaults {
		h.activate(name)
	}
	h.click("Save changes")
	empty := []string{}
	h.profileSelection(&empty)
	h.activate("read")
	h.click("Save changes")
	readOnly := []string{"read"}
	h.profileSelection(&readOnly)
	h.activate(defaultLabel)
	h.click("Save changes")
	h.profileSelection(nil)
	if h.provider.count() != 1 {
		t.Fatal("profile catalog or Save made a model request")
	}
	h.click("← Back to workbench")
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseStream := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseStream()
	h.mu.Lock()
	h.streamStarted, h.streamRelease = started, release
	h.mu.Unlock()
	h.script(nil, nil)
	h.send("First original message 你好")
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("controlled provider did not start the response stream")
	}
	h.pump.waitTimeout(func() bool {
		ct := app.selectedContact()
		if ct == nil {
			return false
		}
		for _, message := range ct.Messages {
			if message != nil && message.Streaming && message.Text == "PRODUCT_TURN_COMPLETE" {
				return true
			}
		}
		return false
	}, 15*time.Second)
	releaseStream()
	h.pump.waitTimeout(func() bool {
		ct := app.selectedContact()
		return ct != nil && ct.PiBinding != nil && ct.SendState == sendIdle && ct.SessionState == string(pi.Completed) && ct.ActiveTaskID == "" && app.submissionInFlight == 0
	}, 30*time.Second)
	var oldBinding *piConversationBinding
	h.onUI(func() error {
		ct := app.selectedContact()
		if ct == nil || ct.PiBinding == nil || ct.SessionID == "" || ct.PiBinding.Context.SessionRef != ct.SessionID {
			return errors.New("actual conversation did not retain its Pi binding")
		}
		oldBinding = clonePiConversationBinding(ct.PiBinding)
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	oldConfig, err := host.client.ContextConfiguration(ctx, oldBinding.Context.ContextID)
	if err != nil || oldConfig.BaseURL != endpointA || oldConfig.CredentialSource != oldBinding.Configuration.CredentialSource || oldConfig.Model != "nagi-product-fixture" || oldConfig.OutputTokens != 4096 || !reflect.DeepEqual(piFirstUseCodingTools(oldConfig.ActiveTools, supported), map[string]bool{"read": true, "bash": true, "edit": true, "write": true}) {
		t.Fatalf("first context configuration=%+v error=%v", oldConfig, err)
	}
	piAppRecord(t, "first-context-configuration", oldConfig)
	h.onUI(func() error { app.reloadContactByID(app.selectedID(), true); return nil })
	h.pump.waitTimeout(func() bool { return app.contactHistoryInFlight == 0 }, 30*time.Second)
	h.onUI(func() error {
		ct := app.selectedContact()
		if ct == nil || !ct.Loaded || ct.LoadErr != "" {
			return errors.New("actual conversation history reload failed")
		}
		for _, entry := range ct.History {
			if entry.From == "me" && entry.Text == "First original message 你好" && entry.ID != "" && entry.Date != "" {
				piAppRecord(t, "original-history-entry", entry)
				return nil
			}
		}
		return errors.New("public history lost original text, entry identity or timestamp")
	})
	h.onUI(func() error {
		copies := map[string]bool{"First original message 你好": false, "PRODUCT_TURN_COMPLETE": false}
		for range 192 {
			h.tester.Key(0, ui.KeyTab)
			h.tester.Frame()
			active, _ := h.tester.TextInputActive()
			if !h.tester.Focused("Copy message") || active {
				continue
			}
			h.tester.SetClipboard("first-use-copy-sentinel")
			h.tester.Key(0, ui.KeySpace)
			h.tester.Frame()
			copied := h.tester.Clipboard()
			if _, expected := copies[copied]; !expected {
				return fmt.Errorf("focused message copy returned unexpected source %q", copied)
			}
			copies[copied] = true
			if copies["First original message 你好"] && copies["PRODUCT_TURN_COMPLETE"] {
				piAppRecord(t, "first-use-copy-sources", copies)
				return nil
			}
		}
		return fmt.Errorf("keyboard copy controls did not preserve both original sources: %v", copies)
	})
	h.click("Settings")
	h.click("Connection & model")
	msgs := locales.In("en")
	endpointLabel := msgs.ConnectionFieldEndpoint()
	editEndpoint := msgs.ConnectionEditField(endpointLabel)
	closeEndpoint := msgs.ConnectionCloseField(endpointLabel)
	h.onUI(func() error {
		form := app.connectionForm
		if form == nil || !form.HasSaved || app.connectionSaved == nil || form.Draft.Endpoint != endpointA || app.connectionSaved.Endpoint != endpointA {
			return errors.New("saved endpoint editor did not retain the original connection")
		}
		h.tester.Frame()
		initialOpen := form.editOpen["Endpoint"]
		_, hasEdit := h.tester.Find(editEndpoint)
		_, hasClose := h.tester.Find(closeEndpoint)
		piAppRecord(t, "saved-endpoint-editor-initial", map[string]any{"open": initialOpen, "edit_control": hasEdit, "close_control": hasClose, "limits_open": form.LimitsOpen})
		if initialOpen {
			if !hasClose || hasEdit {
				return errors.New("open endpoint editor did not expose its Close controller")
			}
			if err := h.focus(closeEndpoint, false); err != nil {
				return err
			}
			h.tester.Key(0, ui.KeySpace)
			h.tester.Frame()
		}
		_, hasEdit = h.tester.Find(editEndpoint)
		_, hasClose = h.tester.Find(closeEndpoint)
		if form.editOpen["Endpoint"] || !hasEdit || hasClose || form.Draft.Endpoint != endpointA {
			return errors.New("saved endpoint editor did not close to its unchanged read-only value")
		}
		if err := h.focus(editEndpoint, false); err != nil {
			return err
		}
		active, _ := h.tester.TextInputActive()
		piAppRecord(t, "saved-endpoint-editor-closed", map[string]any{"open": form.editOpen["Endpoint"], "edit_focused": h.tester.Focused(editEndpoint), "text_input_active": active})
		h.tester.Key(0, ui.KeySpace)
		h.tester.Frame()
		_, hasClose = h.tester.Find(closeEndpoint)
		if !form.editOpen["Endpoint"] || !hasClose || form.Draft.Endpoint != endpointA {
			return errors.New("saved endpoint Edit controller did not reopen the unchanged editor")
		}
		if err := h.focus(endpointLabel, true); err != nil {
			return err
		}
		active, _ = h.tester.TextInputActive()
		piAppRecord(t, "saved-endpoint-editor-opened", map[string]any{"open": form.editOpen["Endpoint"], "input_focused": h.tester.Focused(endpointLabel), "text_input_active": active})
		return nil
	})
	h.input(endpointLabel, endpointB)
	h.input("Output tokens", "2048")
	h.click("Save configuration")
	h.pump.wait(func() bool { return !app.connectionForm.Busy && !app.connectionSaveInFlight })
	h.click("Apply saved")
	h.pump.waitTimeout(func() bool { return !app.connectionApplying }, 30*time.Second)
	if h.provider.count() != 2 {
		t.Fatalf("saving and applying new defaults made a model request: %d", h.provider.count())
	}
	h.onUI(func() error {
		ct := app.selectedContact()
		if app.pi != host || ct == nil || !reflect.DeepEqual(ct.PiBinding, oldBinding) || app.connectionForm.Error != "" {
			return errors.New("Apply changed an existing context binding or host")
		}
		return nil
	})
	unchanged, err := host.client.ContextConfiguration(ctx, oldBinding.Context.ContextID)
	if err != nil || !reflect.DeepEqual(unchanged, oldConfig) {
		t.Fatalf("Apply changed captured context configuration: before=%+v after=%+v error=%v", oldConfig, unchanged, err)
	}
	h.click("Contact profile")
	h.activate(defaultLabel)
	for _, name := range []string{"bash", "edit", "write"} {
		h.activate(name)
	}
	h.click("Save changes")
	h.profileSelection(&readOnly)
	unchanged, err = host.client.ContextConfiguration(ctx, oldBinding.Context.ContextID)
	if err != nil || !reflect.DeepEqual(unchanged, oldConfig) || h.provider.count() != 2 {
		t.Fatalf("profile Save changed the existing context or made a request: configuration=%+v requests=%d error=%v", unchanged, h.provider.count(), err)
	}
	h.click("← Back to workbench")
	h.script(nil, nil)
	h.send("Existing binding remains original")
	h.pump.waitTimeout(func() bool {
		ct := app.selectedContact()
		return ct != nil && ct.SendState == sendIdle && ct.SessionState == string(pi.Completed) && ct.ActiveTaskID == "" && app.submissionInFlight == 0
	}, 30*time.Second)
	h.click("Settings")
	h.click("Contact profile")
	h.click("Save and start new chat")
	h.onUI(func() error {
		ct := app.selectedContact()
		if ct == nil || ct.PiBinding != nil || len(ct.PreviousPiBindings) == 0 || !reflect.DeepEqual(&ct.PreviousPiBindings[len(ct.PreviousPiBindings)-1], oldBinding) {
			return errors.New("new chat did not preserve the prior typed binding")
		}
		return nil
	})
	h.click("← Back to workbench")
	h.script(nil, nil)
	h.send("New binding uses new defaults")
	h.pump.waitTimeout(func() bool {
		ct := app.selectedContact()
		return ct != nil && ct.PiBinding != nil && ct.SendState == sendIdle && ct.SessionState == string(pi.Completed) && ct.ActiveTaskID == "" && app.submissionInFlight == 0
	}, 30*time.Second)
	var newBinding *piConversationBinding
	h.onUI(func() error {
		ct := app.selectedContact()
		if ct == nil || ct.PiBinding == nil || !reflect.DeepEqual(ct.PiBinding.Selection.CodingTools, []string{"read"}) {
			return errors.New("new conversation did not capture the explicit profile selection")
		}
		newBinding = clonePiConversationBinding(ct.PiBinding)
		return nil
	})
	newConfig, err := host.client.ContextConfiguration(ctx, newBinding.Context.ContextID)
	if err != nil {
		t.Fatalf("read replacement context configuration: %v", err)
	}
	selectedCodingTools := piFirstUseCodingTools(newConfig.ActiveTools, supported)
	if newConfig.BaseURL != endpointB || newConfig.OutputTokens != 2048 || newConfig.CredentialSource == oldConfig.CredentialSource || !reflect.DeepEqual(selectedCodingTools, map[string]bool{"read": true}) {
		t.Fatalf("new captured context configuration=%+v error=%v", newConfig, err)
	}
	piAppRecord(t, "replacement-context-configuration", newConfig)
	arrived, held := make(chan struct{}), make(chan struct{})
	var heldOnce sync.Once
	releaseHeld := func() { heldOnce.Do(func() { close(held) }) }
	defer releaseHeld()
	h.script(arrived, held)
	h.send("Cancel this owned run")
	select {
	case <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("cancel scenario did not reach the controlled provider")
	}
	h.pump.waitTimeout(func() bool {
		ct := app.selectedContact()
		return ct != nil && ct.ActiveTaskID != "" && ct.SendState == sendAccepted
	}, 15*time.Second)
	var canceledRunID string
	h.onUI(func() error {
		ct := app.selectedContact()
		if ct == nil || ct.ActiveTaskID == "" || ct.PiBinding == nil || ct.PiBinding.Context.ContextID != newBinding.Context.ContextID {
			return errors.New("cancel target is no longer the captured conversation")
		}
		canceledRunID = ct.ActiveTaskID
		h.tester.Frame()
		if err := h.tester.Click("Stop chat"); err != nil {
			return err
		}
		h.tester.Frame()
		if ct.ActiveTaskID != canceledRunID || ct.SendErrorKind != "send-cancelled" {
			return errors.New("Stop did not target the captured active run")
		}
		return nil
	})
	h.pump.waitTimeout(func() bool {
		ct := app.selectedContact()
		return ct != nil && ct.ActiveTaskID == "" && ct.LastTaskID == canceledRunID && ct.SendState == sendIdle && ct.SessionState == string(pi.Interrupted) && app.submissionInFlight == 0
	}, 30*time.Second)
	releaseHeld()
	if h.provider.count() != 5 {
		t.Fatalf("unexpected model request count before restart: %d", h.provider.count())
	}
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
	app, err = newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	h.app, h.pump, h.closed = app, newUIPump(t, app), false
	h.tester = ui.NewTester(app.view, 1040, 720)
	h.tester.SetLocale("en")
	h.tester.SetFocused(true)
	h.onUI(func() error {
		ct := app.contactByID(secondID)
		if ct == nil || ct.PiBinding == nil || !reflect.DeepEqual(ct.PiBinding, newBinding) || len(ct.PreviousPiBindings) == 0 || !reflect.DeepEqual(&ct.PreviousPiBindings[len(ct.PreviousPiBindings)-1], oldBinding) {
			return errors.New("restart lost current or prior typed binding")
		}
		app.startPiHost()
		return nil
	})
	h.pump.waitTimeout(func() bool {
		return !app.connectionApplying && app.runtimeStatus == runtimeStateReady && app.contactHistoryInFlight == 0
	}, 45*time.Second)
	h.onUI(func() error {
		if app.pi == nil || app.pi.client == nil {
			return errors.New("restart did not own a Pi host")
		}
		h.hosts = append(h.hosts, app.pi)
		ct := app.contactByID(secondID)
		if ct == nil || !ct.Loaded || ct.LoadErr != "" {
			return errors.New("restart did not reload the captured conversation")
		}
		return nil
	})
	if h.provider.count() != 5 || len(h.hosts)+len(h.probes) != 3 {
		t.Fatalf("restart replayed a request or exceeded worker budget: requests=%d hosts=%d probes=%d", h.provider.count(), len(h.hosts), len(h.probes))
	}
	h.mu.Lock()
	requests := append([]piFirstUseRequest(nil), h.requests...)
	h.mu.Unlock()
	if len(requests) != 5 {
		t.Fatalf("request receipt count=%d", len(requests))
	}
	for i, request := range requests {
		var body struct {
			Model               string            `json:"model"`
			MaxTokens           int               `json:"max_tokens"`
			MaxCompletionTokens int               `json:"max_completion_tokens"`
			Tools               []json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatal(err)
		}
		wantEndpoint, wantLimit := "initial", 4096
		if i >= 3 {
			wantEndpoint, wantLimit = "replacement", 2048
		}
		if request.Endpoint != wantEndpoint || request.Path != "/v1/chat/completions" || !request.AuthOK || body.Model != "nagi-product-fixture" || body.MaxTokens != wantLimit && body.MaxCompletionTokens != wantLimit || i == 0 && len(body.Tools) != 0 {
			t.Fatalf("actual request %d did not match captured configuration: endpoint=%s path=%s auth=%v body=%s", i, request.Endpoint, request.Path, request.AuthOK, request.Body)
		}
	}
	piAppRecord(t, "first-use-budget", map[string]any{"workers": len(h.hosts) + len(h.probes), "worker_limit": 3, "model_requests": len(requests), "request_limit": 16})
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
}

type piActivityProvider struct {
	harness   *piFirstUseHarness
	mu        sync.Mutex
	requests  int
	bodies    []json.RawMessage
	problems  []string
	results   map[string]string
	issueA    string
	issueB    string
	stopB     bool
	arrivedA  chan struct{}
	releaseA  chan struct{}
	arrivedB  chan struct{}
	releaseB  chan struct{}
	quitStart chan struct{}
	quitHold  chan struct{}
}

func (p *piActivityProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

func (p *piActivityProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	if err == nil && (p.requests > 24 || r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer nagi-product-loopback" || request.Model != "nagi-product-fixture" || !request.Stream || len(request.Messages) == 0) {
		err = errors.New("unexpected activity loopback request or budget exceeded")
	}
	stopB, issueA, issueB := p.stopB, p.issueA, p.issueB
	p.mu.Unlock()
	if err != nil {
		p.mu.Lock()
		p.problems = append(p.problems, err.Error())
		p.mu.Unlock()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	marker := ""
	for _, message := range request.Messages {
		if message.Role == "user" {
			for _, candidate := range []string{"ACTIVITY_A", "ACTIVITY_B", "ACTIVITY_CHAT_QUERY", "ACTIVITY_CHAT_QUIT"} {
				if strings.Contains(string(message.Content), candidate) {
					marker = candidate
				}
			}
		}
	}
	last := request.Messages[len(request.Messages)-1]
	if marker == "" && err == nil {
		err = errors.New("activity loopback request has no owned scenario marker")
	}
	callName, callID, scope := "", "", ""
	if err == nil && last.Role == "tool" {
		var text string
		if json.Unmarshal(last.Content, &text) != nil || last.ToolCallID == "" {
			err = errors.New("activity tool result is not correlated text")
		} else {
			p.mu.Lock()
			p.results[last.ToolCallID] = text
			p.mu.Unlock()
		}
	} else if err == nil {
		switch {
		case marker == "ACTIVITY_A":
			callName, callID = "issue_wait", "activity-wait-a"
		case marker == "ACTIVITY_B" && stopB:
			callName, callID = "issue_wait", "activity-wait-b"
		case marker == "ACTIVITY_CHAT_QUERY":
			callName, callID = "issue_get", "activity-progress-query"
		}
		if callName != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			err = p.harness.actor(ctx, func() {
				ct := p.harness.app.selectedContact()
				if ct == nil {
					return
				}
				if callName == "issue_get" {
					if ct.pendingSend != nil {
						scope = ct.pendingSend.ToolScopeRef
					}
					return
				}
				issueID := issueA
				if marker == "ACTIVITY_B" {
					issueID = issueB
				}
				if issue := ct.issueByID(issueID); issue != nil {
					if execution := latestIssueExecution(issue); execution != nil {
						scope = execution.ToolScopeRef
					}
				}
			})
			cancel()
			found := false
			for _, tool := range request.Tools {
				found = found || tool.Function.Name == callName
			}
			if err == nil && (!found || scope == "") {
				err = fmt.Errorf("actual %s registration or captured scope is unavailable", callName)
			}
		}
	}
	if err != nil {
		p.mu.Lock()
		p.problems = append(p.problems, err.Error())
		p.mu.Unlock()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if last.Role != "tool" && marker == "ACTIVITY_A" {
		close(p.arrivedA)
		select {
		case <-p.releaseA:
		case <-r.Context().Done():
			return
		}
	}
	if last.Role != "tool" && marker == "ACTIVITY_B" && !stopB {
		close(p.arrivedB)
		select {
		case <-p.releaseB:
		case <-r.Context().Done():
			return
		}
	}
	delta := map[string]any{"role": "assistant", "content": "ACTIVITY_TURN_COMPLETE"}
	finish := "stop"
	if callName != "" {
		arguments := map[string]any{"scope": scope}
		if callName == "issue_wait" {
			arguments["text"] = "Which delivery should this activity use?"
		} else {
			arguments["issue_id"] = issueA
		}
		encoded, _ := json.Marshal(arguments)
		delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": callID, "type": "function", "function": map[string]string{"name": callName, "arguments": string(encoded)}}}}
		finish = "tool_calls"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	chunk, _ := json.Marshal(map[string]any{"id": "activity-loopback", "object": "chat.completion.chunk", "created": 1, "model": "nagi-product-fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
	fmt.Fprintf(w, "data: %s\n\n", chunk)
	if marker == "ACTIVITY_CHAT_QUIT" {
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
		close(p.quitStart)
		select {
		case <-p.quitHold:
		case <-r.Context().Done():
			return
		}
	}
	chunk, _ = json.Marshal(map[string]any{"id": "activity-loopback", "object": "chat.completion.chunk", "created": 1, "model": "nagi-product-fixture", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
}

type piActivityHarness struct {
	*piFirstUseHarness
	provider  *piActivityProvider
	evidence  string
	contactID string
}

func (h *piActivityHarness) screenshot(name string) {
	h.t.Helper()
	var save func()
	h.onUI(func() error {
		h.tester.Frame()
		image := h.tester.Image()
		save = func() { writePreviewPNG(h.t, filepath.Join(h.evidence, name+".png"), image) }
		return nil
	})
	save()
}

func (h *piActivityHarness) activateActivity(label string, check func() error) {
	h.t.Helper()
	var actionErr error
	var save func()
	h.pump.do(func() {
		actionErr = h.focus(label, false)
		if actionErr == nil {
			h.tester.Key(0, ui.KeySpace)
			h.tester.Frame()
			actionErr = check()
		}
		if actionErr == nil {
			return
		}
		h.tester.Frame()
		image := h.tester.Image()
		save = func() { writePreviewPNG(h.t, filepath.Join(h.evidence, "activity-control-failure.png"), image) }
		labels := h.tester.Texts()
		count := len(labels)
		if len(labels) > 100 {
			labels = labels[:100]
		}
		bounds, found := h.tester.Find(label)
		active, noInputMethod := h.tester.TextInputActive()
		selectedContact, selectedIssue, detailIssue := "", "", ""
		chatWork := false
		if ct := h.app.selectedContact(); ct != nil {
			selectedContact, selectedIssue, detailIssue = ct.ID, ct.SelectedIssueID, ct.issueView.DetailID
			chatWork = ct.issueView.ShowConversationWork
		}
		piAppRecord(h.t, "activity-control-failure", map[string]any{
			"control": label, "error": actionErr.Error(), "expected_contact": h.contactID,
			"selected_contact": selectedContact, "selected_issue": selectedIssue, "detail_issue": detailIssue,
			"conversation_work": chatWork, "form_id": h.app.issueSupplementFormID,
			"answer_target": h.app.issueSupplementAnswerTarget, "question_ref": h.app.issueSupplementQuestionRef,
			"frame_labels_first_100": labels, "frame_label_count": count, "first_match_found": found,
			"first_match_bounds": bounds, "controller_focused": h.tester.Focused(label),
			"text_input_active": active, "no_input_method": noInputMethod, "viewport": image.Bounds(),
		})
	})
	if actionErr != nil {
		save()
		h.t.Fatal(actionErr)
	}
}

func (h *piActivityHarness) backToIssues() {
	h.t.Helper()
	var detailID, selectedID string
	var conversationWork bool
	h.onUI(func() error {
		ct := h.app.selectedContact()
		if ct == nil || ct.ID != h.contactID {
			return errors.New("back navigation lost its captured contact")
		}
		detailID, selectedID, conversationWork = ct.issueView.DetailID, ct.SelectedIssueID, ct.issueView.ShowConversationWork
		if detailID == "" && !conversationWork {
			return errors.New("back navigation has no captured detail or conversation work")
		}
		return nil
	})
	h.activateActivity(locales.In("en").WorkBackToIssues(), func() error {
		ct := h.app.selectedContact()
		if ct == nil || ct.ID != h.contactID || ct.SelectedIssueID != selectedID || ct.issueView.ShowConversationWork {
			return errors.New("back navigation changed its captured owner or did not leave conversation work")
		}
		if conversationWork && ct.issueView.DetailID != detailID || !conversationWork && (ct.issueView.DetailID != "" || ct.SelectedWorkTaskID != "") {
			return errors.New("back navigation did not reach the expected activity overview")
		}
		return nil
	})
}

func (h *piActivityHarness) cancelFollowup(issueID, draft string) {
	h.t.Helper()
	h.activateActivity(locales.In("en").SupplementCancel(), func() error {
		ct := h.app.selectedContact()
		saved := h.app.issueSupplementDrafts[issueID]
		if ct == nil || ct.ID != h.contactID || ct.SelectedIssueID != issueID || ct.issueView.DetailID != issueID ||
			h.app.issueSupplementFormID != "" || h.app.issueSupplementAnswerTarget != (issueSupplementTarget{}) ||
			h.app.issueSupplementQuestionRef != "" || saved == nil || *saved != draft {
			return errors.New("cancel did not close the captured ordinary form and preserve its draft")
		}
		return nil
	})
}

func (h *piActivityHarness) openFollowup(issueID, name string) {
	h.t.Helper()
	h.screenshot("activity-followup-before-action-" + name)
	h.onUI(func() error {
		msgs := locales.In("en")
		record := func(stage, controller string) {
			h.tester.Frame()
			labels := h.tester.Texts()
			labelCount := len(labels)
			if len(labels) > 100 {
				labels = labels[:100]
			}
			bounds := map[string]any{}
			for _, label := range []string{msgs.IssueMoreActions(), msgs.IssueHideActions(), msgs.SupplementAdd(), msgs.SupplementFormLabel()} {
				rect, found := h.tester.Find(label)
				bounds[label] = map[string]any{"found": found, "bounds": rect}
			}
			active, noInputMethod := h.tester.TextInputActive()
			selectedContact, selectedIssue, detailIssue := "", "", ""
			if ct := h.app.selectedContact(); ct != nil {
				selectedContact, selectedIssue, detailIssue = ct.ID, ct.SelectedIssueID, ct.issueView.DetailID
			}
			piAppRecord(h.t, "activity-followup-action", map[string]any{
				"stage": stage, "expected_contact": h.contactID, "expected_issue": issueID,
				"selected_contact": selectedContact, "selected_issue": selectedIssue, "detail_issue": detailIssue,
				"expanded": h.app.issueDetailOpen[issueID], "form_id": h.app.issueSupplementFormID,
				"answer_target": h.app.issueSupplementAnswerTarget, "question_ref": h.app.issueSupplementQuestionRef,
				"frame_labels_first_100": labels, "frame_label_count": labelCount, "first_match_bounds": bounds,
				"expected_controller": controller, "controller_focused": h.tester.Focused(controller),
				"text_input_active": active, "no_input_method": noInputMethod, "viewport": h.tester.Image().Bounds(),
			})
		}
		owned := func() bool {
			ct := h.app.selectedContact()
			return ct != nil && ct.ID == h.contactID && ct.SelectedIssueID == issueID && ct.issueView.DetailID == issueID && ct.issueByID(issueID) != nil
		}
		record("before-more-actions", msgs.IssueMoreActions())
		if !owned() {
			return errors.New("follow-up action lost its captured contact or issue")
		}
		if !h.app.issueDetailOpen[issueID] {
			if err := h.focus(msgs.IssueMoreActions(), false); err != nil {
				record("more-actions-focus-failed", msgs.IssueMoreActions())
				return err
			}
			h.tester.Key(0, ui.KeySpace)
		}
		record("after-more-actions", msgs.SupplementAdd())
		if !owned() || !h.app.issueDetailOpen[issueID] || !h.tester.HasText(msgs.IssueHideActions()) {
			return errors.New("activity actions did not expand for the captured issue")
		}
		if err := h.focus(msgs.SupplementAdd(), false); err != nil {
			record("add-follow-up-focus-failed", msgs.SupplementAdd())
			return err
		}
		h.tester.Key(0, ui.KeySpace)
		record("after-add-follow-up", msgs.SupplementFormLabel())
		if !owned() || h.app.issueSupplementFormID != issueID || h.app.issueSupplementAnswerTarget != (issueSupplementTarget{}) || h.app.issueSupplementQuestionRef != "" {
			return errors.New("ordinary follow-up form did not open for the captured issue")
		}
		return nil
	})
}

func (h *piActivityHarness) createIssue(goal string) string {
	h.t.Helper()
	msgs := locales.In("en")
	h.activateActivity(msgs.IssueNewIssue(), func() error {
		if ct := h.app.selectedContact(); ct == nil || ct.ID != h.contactID || !h.app.issueFormOpen || h.app.issueFormEditingID != "" {
			return errors.New("new issue form did not open for the captured contact")
		}
		return nil
	})
	h.onUI(func() error {
		if err := h.focus(msgs.IssueFormGoalLabel(), true); err != nil {
			return err
		}
		h.tester.Type(goal)
		if err := h.focus(msgs.IssueFormRequirementsLabel(), true); err != nil {
			return err
		}
		h.tester.Type("Keep the captured activity ownership.")
		return nil
	})
	var id string
	h.activateActivity(msgs.WorkSaveIssue(), func() error {
		ct := h.app.contactByID(h.contactID)
		if ct == nil || h.app.selectedContact() != ct || h.app.issueFormOpen || ct.SelectedIssueID == "" || ct.issueByID(ct.SelectedIssueID) == nil || ct.issueByID(ct.SelectedIssueID).Goal != goal || ct.issueView.DetailID != ct.SelectedIssueID {
			return fmt.Errorf("GUI did not create captured issue %q", goal)
		}
		id = ct.SelectedIssueID
		return nil
	})
	h.backToIssues()
	return id
}

func (h *piActivityHarness) selectIssue(goal, id string) {
	h.t.Helper()
	h.activateActivity(locales.In("en").IssueRowLabel(goal), func() error {
		ct := h.app.contactByID(h.contactID)
		if ct == nil || h.app.selectedContact() != ct || ct.SelectedIssueID != id || ct.issueNavigation().DetailID != id || ct.issueView.ShowConversationWork {
			return errors.New("GUI selected a different activity")
		}
		return nil
	})
}

func (h *piActivityHarness) startIssue(label, id string) {
	h.t.Helper()
	var before int
	h.onUI(func() error {
		ct := h.app.selectedContact()
		if ct == nil || ct.ID != h.contactID || ct.SelectedIssueID != id || ct.issueView.DetailID != id || ct.issueByID(id) == nil {
			return errors.New("run action lost its captured activity")
		}
		before = len(ct.issueByID(id).Executions)
		return nil
	})
	h.activateActivity(label, func() error {
		ct := h.app.selectedContact()
		if ct == nil || ct.ID != h.contactID || ct.SelectedIssueID != id || ct.issueView.DetailID != id {
			return errors.New("run action changed its captured activity")
		}
		issue := ct.issueByID(id)
		if issue == nil || len(issue.Executions) != before+1 || latestIssueExecution(issue) == nil || !latestIssueExecution(issue).PiIntent {
			return errors.New("GUI did not persist an activity intent")
		}
		return nil
	})
}

func (h *piActivityHarness) wait(condition func() bool) {
	h.t.Helper()
	h.pump.waitTimeout(condition, 45*time.Second)
}

func (h *piActivityHarness) waitSignal(signal <-chan struct{}, name string) {
	h.t.Helper()
	select {
	case <-signal:
	case <-time.After(30 * time.Second):
		h.t.Fatalf("timed out waiting for %s", name)
	}
}

func (h *piActivityHarness) saveForm(inputLabel, saveLabel string) {
	h.t.Helper()
	h.onUI(func() error {
		if err := h.focus(locales.In("en").SupplementCancel(), false); err != nil {
			return err
		}
		h.tester.Key(0, ui.KeyTab)
		h.tester.Frame()
		active, _ := h.tester.TextInputActive()
		if !active || !h.tester.Focused(inputLabel) {
			return errors.New("captured form input was not focused after Cancel")
		}
		h.tester.Key(0, ui.KeyTab)
		h.tester.Frame()
		active, _ = h.tester.TextInputActive()
		if active || !h.tester.Focused(saveLabel) {
			return errors.New("captured form Save was not focused after its input")
		}
		h.tester.Key(0, ui.KeySpace)
		h.tester.Frame()
		return nil
	})
}

func TestPiAppActivitiesAndQuestions(t *testing.T) {
	node, worker := os.Getenv("NAGI_TEST_NODE"), os.Getenv("NAGI_TEST_WORKER")
	if !filepath.IsAbs(node) || !filepath.IsAbs(worker) {
		t.Fatal("NAGI_TEST_NODE and NAGI_TEST_WORKER must be pinned absolute files")
	}
	var err error
	node, err = filepath.EvalSymlinks(node)
	if err != nil {
		t.Fatal(err)
	}
	worker, err = filepath.EvalSymlinks(worker)
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Dir(filepath.Dir(node))
	if node != filepath.Join(payload, "bin", "node") || worker != filepath.Join(payload, "dist", "worker.js") {
		t.Fatal("pinned Node and worker must belong to the same product payload")
	}
	for _, path := range []string{node, worker} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("pinned input %q is not a regular file: %v", path, err)
		}
	}
	evidence := os.Getenv("NAGI_ACTIVITY_EVIDENCE_DIR")
	if !filepath.IsAbs(evidence) {
		t.Fatal("NAGI_ACTIVITY_EVIDENCE_DIR must be an absolute directory outside source")
	}
	worktree, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(evidence); err != nil || !info.IsDir() {
		t.Fatalf("activity evidence directory must already exist: %v", err)
	}
	worktree, err = filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err = filepath.EvalSymlinks(evidence)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(worktree, evidence)
	if err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatal("activity evidence must be written outside the source tree")
	}
	inputs := map[string]string{}
	for _, path := range []string{node, worker, "pi_app_integration_test.go", "pi_activity.go", "pi_app.go", "issue_supplement_view.go", "workspace.go"} {
		digest, err := piAppFileHash(path)
		if err != nil {
			t.Fatal(err)
		}
		inputs[path] = digest
	}
	piAppRecord(t, "activity-source-inputs", inputs)
	root := t.TempDir()
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("TMPDIR", root)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", "")
	isolateConnectionApp(t, root, payload)
	t.Setenv("SAKI_GUI_STORE", guiStorePath())
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	first := &piFirstUseHarness{t: t, app: app, provider: &piAppProvider{limit: 24}}
	first.pump = newUIPump(t, app)
	first.tester = ui.NewTester(app.view, 1040, 720)
	first.tester.SetLocale("en")
	first.tester.SetFocused(true)
	h := &piActivityHarness{piFirstUseHarness: first, evidence: evidence}
	p := &piActivityProvider{harness: first, results: make(map[string]string), arrivedA: make(chan struct{}), releaseA: make(chan struct{}), arrivedB: make(chan struct{}), releaseB: make(chan struct{}), quitStart: make(chan struct{}), quitHold: make(chan struct{})}
	h.provider = p
	server := httptest.NewServer(p)
	first.servers = append(first.servers, server)
	t.Cleanup(func() {
		p.mu.Lock()
		bodies := append([]json.RawMessage(nil), p.bodies...)
		problems := append([]string(nil), p.problems...)
		requests := p.requests
		p.mu.Unlock()
		piAppRecord(t, "activity-loopback", map[string]any{"requests": requests, "bodies": bodies, "problems": problems})
		if len(problems) != 0 {
			t.Errorf("activity loopback rejected requests through cleanup: %v", problems)
		}
		if !t.Failed() && requests != 7 {
			t.Errorf("activity loopback final request count = %d, want 7", requests)
		}
	})
	t.Cleanup(first.cleanup)
	var releaseA, releaseB, releaseQuit sync.Once
	t.Cleanup(func() {
		releaseA.Do(func() { close(p.releaseA) })
		releaseB.Do(func() { close(p.releaseB) })
		releaseQuit.Do(func() { close(p.quitHold) })
	})
	h.onUI(func() error {
		if app.selectedContact() == nil || app.connectionForm == nil || app.connectionForm.Draft.Slots != "2" {
			return errors.New("fresh app or default capacity is unavailable")
		}
		h.contactID = app.selectedContact().ID
		return nil
	})
	h.input("API endpoint", server.URL+"/v1")
	h.input("API key", "nagi-product-loopback")
	h.input("Default model", "nagi-product-fixture")
	h.activate("Capacity & execution limits")
	h.input("Context tokens", "65536")
	h.input("Output tokens", "4096")
	h.input("Maximum active tasks", "3")
	h.onUI(func() error {
		app.chooseFolder = func(mygo.OpenDialogOptions) ([]string, error) { return []string{project}, nil }
		return nil
	})
	h.click("Choose folder…")
	h.wait(func() bool { return !app.connectionForm.Busy })
	h.click("Save configuration")
	h.wait(func() bool { return !app.connectionForm.Busy && !app.connectionSaveInFlight })
	h.click("Apply saved")
	h.wait(func() bool { return !app.connectionApplying })
	var host *piAppHost
	h.onUI(func() error {
		host = app.pi
		if host == nil || host.client == nil || app.runtimeStatus != runtimeStateReady || app.connectionForm.Error != "" || app.maxActiveTasks() != 3 {
			return errors.New("GUI Apply did not publish a ready three-slot worker")
		}
		return nil
	})
	first.hosts = append(first.hosts, host)
	ready, err := host.client.Ready(context.Background())
	if err != nil || ready.Engine.Version != "1.5.2" || ready.Runtime.Version != "v22.23.5" || ready.WorkerEpoch == "" {
		t.Fatalf("actual pinned worker readiness: %+v %v", ready, err)
	}
	piAppRecord(t, "activity-ready", ready)
	h.click("← Back to workbench")
	h.click("Work")
	issueA := h.createIssue("ACTIVITY_A")
	issueB := h.createIssue("ACTIVITY_B")
	issueC := h.createIssue("ACTIVITY_C_PREATTEMPT")
	p.mu.Lock()
	p.issueA, p.issueB = issueA, issueB
	p.mu.Unlock()
	msgs := locales.In("en")
	h.selectIssue("ACTIVITY_A", issueA)
	h.startIssue(msgs.WorkStart(), issueA)
	h.waitSignal(p.arrivedA, "activity A provider request")
	h.openFollowup(issueA, "a")
	const initialDraft = "Keep this follow-up draft"
	const finalDraft = initialDraft + " while the question arrives"
	h.onUI(func() error {
		if err := h.focus(msgs.SupplementFormLabel(), true); err != nil {
			return err
		}
		h.tester.Type(initialDraft)
		h.tester.Frame()
		active, _ := h.tester.TextInputActive()
		if !active || !h.tester.Focused(msgs.SupplementFormLabel()) || *app.issueSupplementDraft(issueA) != initialDraft || app.issueSupplementFormID != issueA {
			return errors.New("ordinary follow-up did not retain its focused draft")
		}
		piAppRecord(t, "activity-followup-before", map[string]any{"issue_id": issueA, "focused": true, "text_input_active": active, "draft": *app.issueSupplementDraft(issueA)})
		return nil
	})
	h.screenshot("activity-followup-before-question")
	releaseA.Do(func() { close(p.releaseA) })
	h.wait(func() bool {
		issue := app.contactByID(h.contactID).issueByID(issueA)
		return issue != nil && issue.openQuestion() != nil
	})
	var runA piActivityRun
	var questionA piQuestionCall
	var questionRef, scopeA string
	var questionRevision int
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		issue := ct.issueByID(issueA)
		execution := latestIssueExecution(issue)
		question := issue.openQuestion()
		if execution == nil || execution.PiRun == nil || !execution.PiRun.Admitted || question == nil || question.PiCall == nil || question.DeliveryState != "" || question.State != issueQuestionOpen || len(issue.Questions) != 1 {
			return errors.New("actual held call did not save one unanswered question")
		}
		runA, questionA = *execution.PiRun, *question.PiCall
		questionRef, questionRevision, scopeA = question.Ref, question.RequirementsRevision, execution.ToolScopeRef
		if questionA.WorkerEpoch != ready.WorkerEpoch || questionA.ContextID != runA.ContextID || questionA.RunID != runA.RunID || questionA.CallID != "activity-wait-a" || questionA.ContactID != h.contactID || questionA.IssueID != issueA || questionA.ExecutionID != execution.ID || questionA.SessionRef != runA.SessionRef || questionA.OperationID == "" || questionRevision != issue.RequirementsRevision || app.piQuestionWaiters[questionA.OperationID] == nil {
			return errors.New("held question did not preserve exact worker, call and product ownership")
		}
		operation := app.issueToolLedger.operationByID(questionA.OperationID)
		if operation == nil || operation.PiCall == nil || *operation.PiCall != questionA || operation.Receipt == nil || operation.Receipt.QuestionRef != questionRef {
			return errors.New("held call operation receipt is not correlated to its question")
		}
		h.tester.Frame()
		active, _ := h.tester.TextInputActive()
		if !active || !h.tester.Focused(msgs.SupplementFormLabel()) || *app.issueSupplementDraft(issueA) != initialDraft {
			return errors.New("question arrival changed the focused follow-up controller or draft")
		}
		h.tester.Type(" while the question arrives")
		h.tester.Key(ui.Cmd, ui.KeyA)
		h.tester.Key(ui.Cmd, ui.KeyC)
		h.tester.Frame()
		active, _ = h.tester.TextInputActive()
		if !active || *app.issueSupplementDraft(issueA) != finalDraft || h.tester.Clipboard() != finalDraft || !h.tester.Focused(msgs.SupplementFormLabel()) {
			return errors.New("typing or copying after the question lost the focused follow-up")
		}
		piAppRecord(t, "activity-followup-after", map[string]any{"question": questionRef, "call": questionA, "run": runA, "focused": true, "text_input_active": active, "draft": *app.issueSupplementDraft(issueA), "copied": h.tester.Clipboard()})
		return nil
	})
	h.screenshot("activity-followup-after-question")
	h.cancelFollowup(issueA, finalDraft)
	h.backToIssues()
	h.selectIssue("ACTIVITY_B", issueB)
	h.startIssue(msgs.WorkStart(), issueB)
	h.waitSignal(p.arrivedB, "activity B provider request")
	var runB piActivityRun
	var bindingB *piActivityBinding
	var executionBID, issueStatusB string
	var sendStateB sendState
	var workStateB workState
	h.wait(func() bool {
		issue := app.contactByID(h.contactID).issueByID(issueB)
		execution := latestIssueExecution(issue)
		return execution != nil && execution.PiRun != nil && execution.PiRun.Admitted && execution.Status == issueExecutionRunning && issue.Work.ActiveTaskID == execution.PiRun.RunID
	})
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		a, b := ct.issueByID(issueA), ct.issueByID(issueB)
		execution := latestIssueExecution(b)
		runB = *execution.PiRun
		work := workFor(ct, runB.RunID)
		if work == nil || work.State != workRunning {
			return errors.New("activity B did not publish its running work state")
		}
		bindingB, executionBID, issueStatusB = clonePiActivityBinding(b.PiBinding), execution.ID, b.Status
		sendStateB, workStateB = b.Work.SendState, work.State
		if a.PiBinding == nil || b.PiBinding == nil || a.PiBinding.Context.Purpose != pi.Activity || b.PiBinding.Context.Purpose != pi.Activity || a.PiBinding.Context.BotID != h.contactID || b.PiBinding.Context.BotID != h.contactID || a.PiBinding.Context.ActivityID != issueA || b.PiBinding.Context.ActivityID != issueB || runA.ContextID == runB.ContextID || runA.RunID == runB.RunID || !reflect.DeepEqual(a.PiBinding.Configuration, b.PiBinding.Configuration) || !reflect.DeepEqual(a.PiBinding.Selection, b.PiBinding.Selection) {
			return errors.New("concurrent activities did not retain distinct captured Pi owners and configurations")
		}
		return nil
	})
	checkB := func() error {
		ct := app.contactByID(h.contactID)
		if ct == nil {
			return errors.New("activity B owner disappeared")
		}
		issue := ct.issueByID(issueB)
		if issue == nil {
			return errors.New("activity B disappeared")
		}
		execution := latestIssueExecution(issue)
		work := workFor(ct, runB.RunID)
		if execution == nil || execution.ID != executionBID || !reflect.DeepEqual(execution.PiRun, &runB) || !reflect.DeepEqual(issue.PiBinding, bindingB) || issue.Work.ActiveTaskID != runB.RunID || execution.Status != issueExecutionRunning || issue.Work.SendState != sendStateB || issue.Status != issueStatusB || work == nil || work.State != workStateB {
			return errors.New("activity B changed while activity A resolved")
		}
		return nil
	}
	beforeCapacity := p.count()
	h.backToIssues()
	h.selectIssue("ACTIVITY_C_PREATTEMPT", issueC)
	h.openFollowup(issueC, "c")
	h.onUI(func() error {
		if err := h.focus(msgs.SupplementFormLabel(), true); err != nil {
			return err
		}
		h.tester.Type("Material preserved before cancellation")
		return nil
	})
	h.saveForm(msgs.SupplementFormLabel(), msgs.IssueSupplementSave())
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		issue := ct.issueByID(issueC)
		reason := app.canStartIssue(ct, issue)
		h.tester.Frame()
		if reason != sendBlockRaw(blockWorkCapacity) || !h.tester.HasText(msgs.WorkNextActionUnavailable(reason)) || latestIssueExecution(issue) != nil || issue.Supplements == nil || issue.Supplements.pendingCount() != 1 || issue.Supplements.Items[0].Text != "Material preserved before cancellation" || p.count() != beforeCapacity {
			return errors.New("full work capacity did not preserve the unadmitted supplement and visible refusal")
		}
		if _, present := h.tester.Find(msgs.WorkStartWithSupplement()); present {
			return errors.New("full work capacity exposed an available Start action")
		}
		piAppRecord(t, "activity-capacity-refusal", map[string]any{"issue_id": issueC, "reason": reason, "requests": p.count(), "supplement": issue.Supplements.Items[0]})
		return nil
	})
	h.screenshot("activity-capacity-refusal")
	h.send("ACTIVITY_CHAT_QUERY")
	h.wait(func() bool {
		ct := app.contactByID(h.contactID)
		return ct != nil && ct.PiBinding != nil && ct.SendState == sendIdle && ct.ActiveTaskID == ""
	})
	var chatBinding *piConversationBinding
	var activityConfiguration piConfiguration
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		a := ct.issueByID(issueA)
		if ct.PiBinding == nil || a.PiBinding == nil || ct.PiBinding.Context.Purpose != pi.Conversation || ct.PiBinding.Context.BotID != h.contactID || ct.PiBinding.Context.ActivityID != "" || ct.PiBinding.Context.ContextID == runA.ContextID || ct.PiBinding.Context.ContextID == runB.ContextID || ct.PiBinding.Selection.CWD != project || a.PiBinding.Selection.CWD != project || !reflect.DeepEqual(ct.PiBinding.Configuration, a.PiBinding.Configuration) || !reflect.DeepEqual(ct.PiBinding.Selection.CodingTools, a.PiBinding.Selection.CodingTools) {
			return errors.New("chat did not retain the same captured configuration and its own Pi owner")
		}
		chatBinding = clonePiConversationBinding(ct.PiBinding)
		activityConfiguration = a.PiBinding.Configuration
		return nil
	})
	configCtx, configCancel := context.WithTimeout(context.Background(), 10*time.Second)
	for _, owner := range []struct {
		contextID string
		tool      string
	}{{runA.ContextID, "issue_wait"}, {runB.ContextID, "issue_wait"}, {chatBinding.Context.ContextID, "issue_get"}} {
		configuration, configErr := host.client.ContextConfiguration(configCtx, owner.contextID)
		found := false
		for _, tool := range configuration.ActiveTools {
			found = found || tool == owner.tool
		}
		if configErr != nil || configuration.ContextID != owner.contextID || configuration.BaseURL != activityConfiguration.BaseURL || configuration.Model != activityConfiguration.Model || configuration.CredentialSource != activityConfiguration.CredentialSource || configuration.WireAPI != activityConfiguration.WireAPI || !found {
			configCancel()
			t.Fatalf("actual context configuration or registered tool differs: %+v %v", configuration, configErr)
		}
		piAppRecord(t, "activity-context-configuration", configuration)
	}
	configCancel()
	p.mu.Lock()
	queryResult := p.results["activity-progress-query"]
	p.mu.Unlock()
	if !strings.Contains(queryResult, runA.ContextID) || !strings.Contains(queryResult, runA.RunID) || !strings.Contains(queryResult, "pi_progress") {
		t.Fatalf("actual registered issue_get did not return captured progress: %s", queryResult)
	}
	beforeInspect := p.count()
	rawGet, _ := json.Marshal(map[string]string{"scope": scopeA, "issue_id": issueA})
	readCtx, readCancel := context.WithTimeout(context.Background(), 10*time.Second)
	getResponse, err := app.issueToolHandleGet(readCtx, rawGet)
	inspection, inspectErr := host.client.Inspect(readCtx, runA.ContextID)
	readCancel()
	var detail IssueToolIssueDetail
	if err != nil || getResponse.IsError || len(getResponse.Content) != 1 || json.Unmarshal([]byte(getResponse.Content[0].Text), &detail) != nil || detail.PiProgress == nil || !detail.PiProgress.Active || detail.PiProgress.RunID != runA.RunID || detail.ResultUnavailable == "" || inspectErr != nil || inspection.ActiveRun == nil || inspection.ActiveRun.RunID != runA.RunID || p.count() != beforeInspect {
		t.Fatalf("direct issue_get/Inspect changed requests or lost captured activity: response=%+v inspect=%+v errors=%v/%v", getResponse, inspection, err, inspectErr)
	}
	piAppRecord(t, "activity-direct-progress", map[string]any{"detail": detail, "inspect": inspection, "requests": beforeInspect})
	h.backToIssues()
	h.selectIssue("ACTIVITY_A", issueA)
	beforeRefresh := p.count()
	var refreshGeneration int
	h.onUI(func() error {
		refreshGeneration = app.contactByID(h.contactID).issueByID(issueA).Work.historyGen
		return nil
	})
	h.activateActivity(msgs.IssueReloadTask(), func() error {
		ct := app.selectedContact()
		if ct == nil || ct.ID != h.contactID || ct.issueView.DetailID != issueA || ct.SelectedIssueID != issueA || ct.issueByID(issueA).Work.historyGen != refreshGeneration+1 {
			return errors.New("refresh did not start for the captured activity")
		}
		return nil
	})
	h.wait(func() bool { return app.contactHistoryInFlight == 0 })
	if p.count() != beforeRefresh {
		t.Fatal("captured GUI refresh made a model request")
	}
	h.screenshot("activity-question-held")
	h.activateActivity(msgs.WorkAnswer(), func() error {
		ct := app.selectedContact()
		if ct == nil || ct.ID != h.contactID || ct.SelectedIssueID != issueA || ct.issueView.DetailID != issueA || app.issueSupplementFormID != issueA || app.issueSupplementAnswerTarget.ContactID != h.contactID || app.issueSupplementAnswerTarget.IssueID != issueA || app.issueSupplementAnswerTarget.RequirementsRevision != questionRevision || app.issueSupplementQuestionRef != questionRef || *app.issueSupplementDraft(issueA) != finalDraft {
			return errors.New("GUI answer did not preserve the captured question and ordinary draft")
		}
		return nil
	})
	h.screenshot("activity-answer-before-save")
	storePath := app.store.path
	backupPath := storePath + ".activity-answer-backup"
	backupHeld, obstructed := false, false
	restoreStore := func() error {
		if obstructed {
			if err := os.Remove(storePath); err != nil {
				return err
			}
			obstructed = false
		}
		if backupHeld {
			if err := os.Rename(backupPath, storePath); err != nil {
				return err
			}
			backupHeld = false
		}
		return nil
	}
	t.Cleanup(func() {
		if err := restoreStore(); err != nil {
			t.Errorf("restore owned activity store: %v", err)
		}
	})
	diskBefore, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var committedBefore, ledgerBefore []byte
	var waiter chan struct{}
	h.onUI(func() error {
		var err error
		committedBefore, err = json.Marshal(app.store.state)
		if err != nil {
			return err
		}
		ledgerBefore, err = json.Marshal(app.issueToolLedger)
		waiter = app.piQuestionWaiters[questionA.OperationID]
		return err
	})
	if waiter == nil {
		t.Fatal("held question has no installed waiter")
	}
	beforeSave := p.count()
	if err := os.Rename(storePath, backupPath); err != nil {
		t.Fatal(err)
	}
	backupHeld = true
	if err := os.Mkdir(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	obstructed = true
	h.saveForm(msgs.WorkAnswer(), msgs.WorkAnswer())
	h.onUI(func() error {
		issue := app.contactByID(h.contactID).issueByID(issueA)
		question := issue.questionByRef(questionRef)
		committed, err := json.Marshal(app.store.state)
		if err != nil {
			return err
		}
		ledger, err := json.Marshal(app.issueToolLedger)
		if err != nil {
			return err
		}
		disk, err := os.ReadFile(backupPath)
		if err != nil || app.issueActionError == "" || app.issueSupplementFormID != issueA || *app.issueSupplementDraft(issueA) != finalDraft || question == nil || question.State != issueQuestionOpen || question.DeliveryState != "" || question.AnswerSupplementID != "" || app.piQuestionWaiters[questionA.OperationID] != waiter || !bytes.Equal(committed, committedBefore) || !bytes.Equal(ledger, ledgerBefore) || !bytes.Equal(disk, diskBefore) || p.count() != beforeSave {
			return fmt.Errorf("failed captured answer Save changed durable state, waiter or draft: %v", err)
		}
		piAppRecord(t, "activity-answer-save-failure", map[string]any{"question_ref": questionRef, "operation_id": questionA.OperationID, "draft": finalDraft, "disk_sha256": fmt.Sprintf("%x", sha256.Sum256(disk)), "requests": beforeSave, "waiter_retained": true, "error": app.issueActionError})
		return nil
	})
	h.screenshot("activity-answer-save-failed")
	if err := restoreStore(); err != nil {
		t.Fatal(err)
	}
	h.saveForm(msgs.WorkAnswer(), msgs.WorkAnswer())
	var supplementID string
	h.onUI(func() error {
		issue := app.contactByID(h.contactID).issueByID(issueA)
		question := issue.questionByRef(questionRef)
		if question == nil || question.State != issueQuestionAnswered || question.AnswerSupplementID == "" || question.AnswerText != finalDraft || question.DeliveryState == "" || issue.Supplements == nil || len(issue.Supplements.Items) != 1 || issue.Supplements.pendingCount() != 0 || app.issueSupplementFormID != "" {
			return errors.New("GUI did not save exactly one captured held-call answer")
		}
		supplementID = question.AnswerSupplementID
		item := issue.Supplements.itemByID(supplementID)
		if item == nil || item.PiQuestionOperationID != questionA.OperationID || item.Text != finalDraft {
			return errors.New("saved answer is not associated with the exact held call")
		}
		return nil
	})
	h.wait(func() bool {
		issue := app.contactByID(h.contactID).issueByID(issueA)
		question := issue.questionByRef(questionRef)
		execution := latestIssueExecution(issue)
		return question != nil && question.DeliveryState == issueQuestionDeliveryResolved && execution != nil && execution.Status == issueExecutionCompleted && issue.Work.ActiveTaskID == ""
	})
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		issue := ct.issueByID(issueA)
		question := issue.questionByRef(questionRef)
		if issue.Status == issueStatusCompleted || len(issue.Questions) != 1 || len(issue.Supplements.Items) != 1 || issue.Supplements.pendingCount() != 0 || ct.ActiveTaskID != "" || question.DeliveryState != issueQuestionDeliveryResolved {
			return errors.New("answer delivery changed unrelated work or falsely completed the issue")
		}
		if err := checkB(); err != nil {
			return err
		}
		before := p.count()
		receipt, err := app.answerIssueQuestionFor(ct, issueA, questionRef, &questionRevision, finalDraft, nil, nil)
		if err != nil || receipt == nil || receipt.SupplementID != supplementID || len(issue.Supplements.Items) != 1 || p.count() != before {
			return errors.New("identical answer retry created another answer or model run")
		}
		piAppRecord(t, "activity-answer-resolved", map[string]any{"question": question, "run": runA, "receipt": receipt, "requests": before, "raw_tool_finished_exported": false, "raw_tool_result_reply_exported": false})
		return nil
	})
	h.screenshot("activity-answer-delivered")
	p.mu.Lock()
	answerResult := p.results["activity-wait-a"]
	p.mu.Unlock()
	if answerResult != finalDraft || p.count() != beforeSave+1 {
		t.Fatalf("next actual model request did not carry exactly the saved answer: text=%q requests=%d", answerResult, p.count())
	}
	historyCtx, historyCancel := context.WithTimeout(context.Background(), 15*time.Second)
	cursor := ""
	matches := 0
	var matchedEntry json.RawMessage
	var matchedEntryID string
	for {
		page, historyErr := host.client.History(historyCtx, pi.HistoryParams{ContextID: runA.ContextID, Cursor: cursor, Limit: 100})
		if historyErr != nil || page.ContextID != runA.ContextID || page.SessionRef != runA.SessionRef {
			historyCancel()
			t.Fatalf("public activity history owner mismatch: %+v %v", page, historyErr)
		}
		for _, raw := range page.Entries {
			var entry struct {
				ID      string          `json:"id"`
				Type    string          `json:"type"`
				Message json.RawMessage `json:"message"`
			}
			var message struct {
				Role       string `json:"role"`
				ToolCallID string `json:"toolCallId"`
				ToolName   string `json:"toolName"`
			}
			if json.Unmarshal(raw, &entry) == nil && entry.Type == "message" && json.Unmarshal(entry.Message, &message) == nil && message.Role == "toolResult" && message.ToolCallID == questionA.CallID {
				text, contentErr := piAppContent(entry.Message)
				if contentErr != nil || message.ToolName != "issue_wait" || text != finalDraft || entry.ID == "" || len(raw) > 64<<10 {
					historyCancel()
					t.Fatalf("public held answer differs from the delivered call: %s", entry.Message)
				}
				matchedEntry = append(json.RawMessage(nil), raw...)
				matchedEntryID = entry.ID
				matches++
			}
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			historyCancel()
			t.Fatal("activity history cursor did not advance")
		}
		cursor = page.NextCursor
	}
	historyCancel()
	if matches != 1 {
		t.Fatalf("public history held answer count = %d", matches)
	}
	piAppRecord(t, "activity-answer-history", map[string]any{"worker_epoch": runA.WorkerEpoch, "context_id": runA.ContextID, "session_ref": runA.SessionRef, "run_id": runA.RunID, "call_id": questionA.CallID, "entry_id": matchedEntryID, "entry": matchedEntry})
	h.onUI(checkB)
	bCtx, bCancel := context.WithTimeout(context.Background(), 10*time.Second)
	bInspection, bErr := host.client.Inspect(bCtx, runB.ContextID)
	bCancel()
	if bErr != nil || bInspection.ContextID != runB.ContextID || bInspection.SessionRef != runB.SessionRef || bInspection.ActiveRun == nil || bInspection.ActiveRun.RunID != runB.RunID || bInspection.ActiveRun.CancelRequested {
		t.Fatalf("activity B no longer has its captured active run: %+v %v", bInspection, bErr)
	}
	piAppRecord(t, "activity-b-held-after-answer", map[string]any{"run": runB, "inspection": bInspection})
	releaseB.Do(func() { close(p.releaseB) })
	h.wait(func() bool {
		issue := app.contactByID(h.contactID).issueByID(issueB)
		execution := latestIssueExecution(issue)
		return execution != nil && execution.Status == issueExecutionCompleted && issue.Work.ActiveTaskID == ""
	})
	h.backToIssues()
	h.selectIssue("ACTIVITY_B", issueB)
	p.mu.Lock()
	p.stopB = true
	p.mu.Unlock()
	h.startIssue(msgs.WorkContinue(), issueB)
	h.wait(func() bool {
		issue := app.contactByID(h.contactID).issueByID(issueB)
		return issue != nil && issue.openQuestion() != nil
	})
	var stopCall piQuestionCall
	var stopRun piActivityRun
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		issue := ct.issueByID(issueB)
		question := issue.openQuestion()
		execution := latestIssueExecution(issue)
		if question == nil || question.PiCall == nil || question.DeliveryState != "" || execution == nil || execution.PiRun == nil || !execution.PiRun.Admitted {
			return errors.New("second held call was not actually admitted")
		}
		stopCall, stopRun = *question.PiCall, *execution.PiRun
		if stopCall.CallID != "activity-wait-b" || stopCall.RunID != stopRun.RunID || stopRun.ContextID != runB.ContextID || stopRun.RunID == runB.RunID || stopCall.OperationID == questionA.OperationID || stopCall.CallID == questionA.CallID {
			return errors.New("second held call did not retain a distinct exact owner")
		}
		return nil
	})
	h.screenshot("activity-second-question-before-stop")
	h.activateActivity(msgs.WorkStopIssueTask(), func() error {
		ct := app.selectedContact()
		if ct == nil || ct.ID != h.contactID || ct.issueView.DetailID != issueB || ct.SelectedIssueID != issueB {
			return errors.New("Stop changed its captured activity")
		}
		execution := latestIssueExecution(ct.issueByID(issueB))
		if execution == nil || execution.PiRun == nil || execution.PiRun.RunID != stopRun.RunID || !execution.PiRun.CancelRequested {
			return errors.New("Stop did not request cancellation for the captured run")
		}
		return nil
	})
	h.wait(func() bool {
		issue := app.contactByID(h.contactID).issueByID(issueB)
		execution := latestIssueExecution(issue)
		return execution != nil && execution.Status == issueExecutionInterrupted && issue.Work.ActiveTaskID == ""
	})
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		a, b := ct.issueByID(issueA), ct.issueByID(issueB)
		question := b.openQuestion()
		if question == nil || question.PiCall == nil || *question.PiCall != stopCall || question.DeliveryState != "" || question.AnswerSupplementID != "" || a.questionByRef(questionRef).DeliveryState != issueQuestionDeliveryResolved || a.questionByRef(questionRef).AnswerSupplementID != supplementID || ct.ActiveTaskID != "" || b.Status == issueStatusCompleted {
			return errors.New("Stop answered a held call, changed another owner or completed its issue")
		}
		piAppRecord(t, "activity-stopped-held-call", map[string]any{"call": stopCall, "run": stopRun, "execution": latestIssueExecution(b), "question": question})
		return nil
	})
	p.mu.Lock()
	_, stopDelivered := p.results[stopCall.CallID]
	p.mu.Unlock()
	if stopDelivered {
		t.Fatal("explicit Stop delivered an answer to the canceled held call")
	}
	h.backToIssues()
	h.selectIssue("ACTIVITY_C_PREATTEMPT", issueC)
	beforeCanceledPreparation := p.count()
	host.openMu.Lock()
	openLocked := true
	defer func() {
		if openLocked {
			host.openMu.Unlock()
		}
	}()
	h.startIssue(msgs.WorkStartWithSupplement(), issueC)
	h.onUI(func() error {
		issue := app.contactByID(h.contactID).issueByID(issueC)
		execution := latestIssueExecution(issue)
		batch := issueSupplementBatchForExecution(issue, execution.ID)
		if issue.Work.SendState != sendSubmitting || execution.PiRun != nil || batch == nil || batch.State != supplementBatchDispatching {
			return errors.New("open-context gate did not hold the genuine pre-attempt execution and batch")
		}
		h.tester.Frame()
		if _, ok := h.tester.Find(msgs.SupplementCheck()); !ok || !h.tester.HasText(piResultsUnavailable) {
			return errors.New("the unavailable batch Check was not visible while dispatch was held")
		}
		checkErr := h.tester.Click(msgs.SupplementCheck())
		h.tester.Frame()
		if batch.State != supplementBatchDispatching || execution.PiRun != nil || p.count() != beforeCanceledPreparation {
			return errors.New("unavailable batch Check changed the held execution")
		}
		piAppRecord(t, "activity-unavailable-check", map[string]any{"batch_id": batch.ID, "state": batch.State, "click_error": fmt.Sprint(checkErr), "requests": p.count()})
		return nil
	})
	h.screenshot("activity-batch-check-unavailable")
	h.activateActivity(msgs.WorkCancel(), func() error {
		ct := app.selectedContact()
		if ct == nil || ct.ID != h.contactID || ct.SelectedIssueID != issueC || ct.issueView.DetailID != issueC {
			return errors.New("cancel changed its captured activity")
		}
		issue := ct.issueByID(issueC)
		if issue.Status != issueStatusCanceled || !issue.Work.cancelAfterAccept || latestIssueExecution(issue).PiRun != nil {
			return errors.New("GUI did not cancel the owned pre-attempt issue")
		}
		return nil
	})
	host.openMu.Unlock()
	openLocked = false
	h.wait(func() bool {
		issue := app.contactByID(h.contactID).issueByID(issueC)
		execution := latestIssueExecution(issue)
		return execution != nil && execution.Status == issueExecutionNotDispatched && issue.Work.SendState == sendIdle
	})
	h.onUI(func() error {
		issue := app.contactByID(h.contactID).issueByID(issueC)
		execution := latestIssueExecution(issue)
		batch := issueSupplementBatchForExecution(issue, execution.ID)
		if issue.Status != issueStatusCanceled || execution.PiRun != nil || batch == nil || batch.State != supplementBatchNotDispatched || len(batch.Snapshot) != 1 || batch.Snapshot[0].Text != "Material preserved before cancellation" || p.count() != beforeCanceledPreparation {
			return errors.New("pre-attempt cancel dispatched a run or lost its durable batch")
		}
		piAppRecord(t, "activity-pre-attempt-canceled", map[string]any{"issue": issue.ID, "execution": execution, "batch": batch, "requests": p.count()})
		return nil
	})
	checkedStore, err := openContactStore(storePath, project)
	if err != nil {
		t.Fatal(err)
	}
	var reopenedC *productIssue
	for _, contact := range contactsFromStore(checkedStore) {
		if contact.ID == h.contactID {
			reopenedC = contact.issueByID(issueC)
		}
	}
	if reopenedC == nil || reopenedC.Status != issueStatusCanceled || latestIssueExecution(reopenedC) == nil {
		t.Fatal("reopened store lost the canceled issue")
	}
	reopenedExecution := latestIssueExecution(reopenedC)
	reopenedBatch := issueSupplementBatchForExecution(reopenedC, reopenedExecution.ID)
	if reopenedExecution.Status != issueExecutionNotDispatched || reopenedExecution.PiRun != nil || reopenedBatch == nil || reopenedBatch.State != supplementBatchNotDispatched {
		t.Fatal("reopened store lost the proven pre-attempt non-dispatch")
	}
	h.send("ACTIVITY_CHAT_QUIT")
	h.waitSignal(p.quitStart, "active chat stream before Quit")
	var quitRun, chatContext string
	h.wait(func() bool {
		ct := app.contactByID(h.contactID)
		return ct != nil && ct.PiBinding != nil && ct.ActiveTaskID != "" && ct.SendState == sendAccepted
	})
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		quitRun, chatContext = ct.ActiveTaskID, ct.PiBinding.Context.ContextID
		if quitRun == "" || chatContext == "" || chatContext == runA.ContextID || chatContext == runB.ContextID {
			return errors.New("Quit chat did not retain its own captured context and run")
		}
		return nil
	})
	h.screenshot("activity-quit-active-chat")
	requestsBeforeRestart := p.count()
	if err := h.shutdown(); err != nil {
		t.Fatalf("actual app Quit did not drain and reap: %v", err)
	}
	releaseQuit.Do(func() { close(p.quitHold) })
	stoppedContact := app.contactByID(h.contactID)
	if stoppedContact == nil || stoppedContact.ActiveTaskID != "" || stoppedContact.LastTaskID != quitRun || stoppedContact.SessionState != string(pi.Interrupted) || !app.quitReady {
		t.Fatal("Quit did not retain the captured chat terminal state")
	}
	piAppRecord(t, "activity-quit-settlement", map[string]any{"context_id": chatContext, "run_id": quitRun, "status": stoppedContact.SessionState, "requests": requestsBeforeRestart})
	app, err = newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	first.app, first.pump, first.closed = app, newUIPump(t, app), false
	first.tester = ui.NewTester(app.view, 1040, 720)
	first.tester.SetLocale("en")
	first.tester.SetFocused(true)
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		if ct == nil || ct.PiBinding == nil || ct.PiBinding.Context.ContextID != chatContext {
			return errors.New("restart lost the captured conversation binding")
		}
		app.startPiHost()
		return nil
	})
	h.wait(func() bool {
		return !app.connectionApplying && app.runtimeStatus == runtimeStateReady && app.contactHistoryInFlight == 0
	})
	h.onUI(func() error {
		if app.pi == nil || app.pi.client == nil {
			return errors.New("restart did not own its real Pi worker")
		}
		first.hosts = append(first.hosts, app.pi)
		ct := app.contactByID(h.contactID)
		if ct == nil {
			return errors.New("restart lost the activity owner")
		}
		a, b, c := ct.issueByID(issueA), ct.issueByID(issueB), ct.issueByID(issueC)
		if a == nil || b == nil || c == nil {
			return errors.New("restart lost a captured issue")
		}
		question := a.questionByRef(questionRef)
		operation := app.issueToolLedger.operationByID(questionA.OperationID)
		if question == nil || question.PiCall == nil || *question.PiCall != questionA || question.AnswerSupplementID != supplementID || question.AnswerText != finalDraft || question.DeliveryState != issueQuestionDeliveryResolved || operation == nil || operation.PiCall == nil || *operation.PiCall != questionA || a.Supplements == nil || len(a.Supplements.Items) != 1 || a.Supplements.pendingCount() != 0 || b.openQuestion() == nil || b.openQuestion().DeliveryState != "" || c.Status != issueStatusCanceled || latestIssueExecution(c) == nil || latestIssueExecution(c).Status != issueExecutionNotDispatched || len(app.piQuestionWaiters) != 0 || p.count() != requestsBeforeRestart {
			return errors.New("restart replayed or lost the saved answer, unanswered call or canceled intent")
		}
		app.pi.mu.Lock()
		activeBridges := len(app.pi.bridges)
		app.pi.mu.Unlock()
		if activeBridges != 0 {
			return errors.New("restart recreated a run or tool delivery bridge")
		}
		piAppRecord(t, "activity-restart", map[string]any{"question": question, "operation": operation, "unanswered": b.openQuestion(), "canceled_execution": latestIssueExecution(c), "requests": p.count(), "bridges": activeBridges})
		return nil
	})
	var beforeA []byte
	var stoppedQuestionRef string
	var stoppedExecutionCount int
	var settingsOpen, workOpen, detailOpen, chatWorkOpen bool
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		if app.selectedContact() != ct {
			return errors.New("restart selected a different activity owner")
		}
		a, b := ct.issueByID(issueA), ct.issueByID(issueB)
		question := b.openQuestion()
		if question == nil || question.PiCall == nil || *question.PiCall != stopCall || question.DeliveryState != "" || app.piQuestionWaiters[stopCall.OperationID] != nil || b.Work.ActiveTaskID != "" {
			return errors.New("restarted stopped call is not an unanswered, waiter-free question")
		}
		var err error
		beforeA, err = json.Marshal(a.questionByRef(questionRef))
		if err != nil {
			return err
		}
		stoppedQuestionRef, stoppedExecutionCount = question.Ref, len(b.Executions)
		settingsOpen, workOpen = app.settingsOpen, app.workspaceOpen
		detailOpen, chatWorkOpen = ct.issueNavigation().DetailID != "", ct.issueNavigation().ShowConversationWork
		return nil
	})
	if settingsOpen {
		h.click("← Back to workbench")
	}
	if !workOpen {
		h.click("Work")
	}
	if chatWorkOpen {
		h.backToIssues()
	}
	if detailOpen {
		h.backToIssues()
	}
	h.selectIssue("ACTIVITY_B", issueB)
	h.activateActivity(msgs.WorkAnswer(), func() error {
		ct := app.selectedContact()
		if ct == nil || ct.ID != h.contactID || ct.SelectedIssueID != issueB || ct.issueView.DetailID != issueB || app.issueSupplementFormID != issueB || app.issueSupplementAnswerTarget.ContactID != h.contactID || app.issueSupplementAnswerTarget.IssueID != issueB || app.issueSupplementQuestionRef != stoppedQuestionRef || ct.issueByID(issueB).openQuestion() == nil || app.issueSupplementAnswerTarget.RequirementsRevision != ct.issueByID(issueB).openQuestion().RequirementsRevision {
			return errors.New("GUI did not capture the restarted stopped question")
		}
		return nil
	})
	const stoppedAnswer = "Save this answer for the stopped activity."
	h.onUI(func() error {
		if app.issueSupplementAnswerTarget.ContactID != h.contactID || app.issueSupplementAnswerTarget.IssueID != issueB || app.issueSupplementQuestionRef != stoppedQuestionRef {
			return errors.New("GUI did not capture the restarted stopped question")
		}
		if err := h.focus(msgs.WorkAnswer(), true); err != nil {
			return err
		}
		h.tester.Key(ui.Cmd, ui.KeyA)
		h.tester.Type(stoppedAnswer)
		h.tester.Frame()
		if *app.issueSupplementDraft(issueB) != stoppedAnswer {
			return errors.New("restarted answer form did not retain its draft")
		}
		return nil
	})
	h.saveForm(msgs.WorkAnswer(), msgs.WorkAnswer())
	var stoppedSupplementID string
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		a, b := ct.issueByID(issueA), ct.issueByID(issueB)
		question := b.questionByRef(stoppedQuestionRef)
		if question == nil || question.PiCall == nil || *question.PiCall != stopCall || question.State != issueQuestionAnswered || question.AnswerText != stoppedAnswer || question.DeliveryState != issueQuestionDeliverySaved || question.AnswerSupplementID == "" || b.Supplements == nil || len(b.Supplements.Items) != 1 || b.Supplements.pendingCount() != 0 || b.Work.ActiveTaskID != "" || len(b.Executions) != stoppedExecutionCount || latestIssueExecution(b).ID != stopCall.ExecutionID || app.piQuestionWaiters[stopCall.OperationID] != nil || len(app.piQuestionWaiters) != 0 || p.count() != requestsBeforeRestart {
			return errors.New("restarted GUI answer was delivered, replayed or lost its saved association")
		}
		stoppedSupplementID = question.AnswerSupplementID
		item := b.Supplements.itemByID(stoppedSupplementID)
		if item == nil || item.PiQuestionOperationID != stopCall.OperationID || item.Text != stoppedAnswer {
			return errors.New("saved answer does not belong to the stopped call")
		}
		currentA, err := json.Marshal(a.questionByRef(questionRef))
		if err != nil || !bytes.Equal(currentA, beforeA) || a.questionByRef(questionRef).DeliveryState != issueQuestionDeliveryResolved || a.questionByRef(questionRef).AnswerSupplementID != supplementID {
			return errors.New("saving the stopped answer changed the resolved activity A")
		}
		app.pi.mu.Lock()
		bridges := len(app.pi.bridges)
		app.pi.mu.Unlock()
		h.tester.Frame()
		if bridges != 0 || !h.tester.HasText(msgs.IssueAnswerSaved()) || h.tester.HasText(msgs.SupplementSavedPending()) {
			return errors.New("saved-unconfirmed answer appeared as pending work or created a bridge")
		}
		piAppRecord(t, "activity-saved-without-waiter", map[string]any{"question": question, "supplement": item, "requests": p.count(), "waiters": len(app.piQuestionWaiters), "bridges": bridges, "executions": len(b.Executions)})
		return nil
	})
	h.screenshot("activity-saved-unconfirmed-after-restart")
	reopenedAnswerStore, err := openContactStore(storePath, project)
	if err != nil {
		t.Fatal(err)
	}
	var reopenedB *productIssue
	for _, contact := range contactsFromStore(reopenedAnswerStore) {
		if contact.ID == h.contactID {
			reopenedB = contact.issueByID(issueB)
		}
	}
	if reopenedB == nil || reopenedB.Supplements == nil {
		t.Fatal("reopened store lost the stopped activity answer")
	}
	if reopenedAnswerStore.issueToolLedger == nil {
		t.Fatal("reopened store lost the stopped call ledger")
	}
	reopenedQuestion := reopenedB.questionByRef(stoppedQuestionRef)
	reopenedItem := reopenedB.Supplements.itemByID(stoppedSupplementID)
	reopenedOperation := reopenedAnswerStore.issueToolLedger.operationByID(stopCall.OperationID)
	if reopenedQuestion == nil || reopenedQuestion.PiCall == nil || *reopenedQuestion.PiCall != stopCall || reopenedQuestion.State != issueQuestionAnswered || reopenedQuestion.DeliveryState != issueQuestionDeliverySaved || reopenedQuestion.AnswerSupplementID != stoppedSupplementID || reopenedQuestion.AnswerText != stoppedAnswer || reopenedItem == nil || reopenedItem.PiQuestionOperationID != stopCall.OperationID || reopenedItem.Text != stoppedAnswer || reopenedB.Supplements.pendingCount() != 0 || reopenedOperation == nil || reopenedOperation.PiCall == nil || *reopenedOperation.PiCall != stopCall || p.count() != requestsBeforeRestart {
		t.Fatal("reopened store did not retain the saved-unconfirmed answer")
	}
	piAppRecord(t, "activity-reopened-saved-without-waiter", map[string]any{"question": reopenedQuestion, "supplement": reopenedItem, "operation": reopenedOperation, "requests": p.count()})
	if len(first.hosts)+len(first.probes) != 2 || p.count() != 7 || p.count() != requestsBeforeRestart {
		t.Fatalf("activity worker/request budget or restart count differs: workers=%d requests=%d", len(first.hosts)+len(first.probes), p.count())
	}
	p.mu.Lock()
	problems := append([]string(nil), p.problems...)
	p.mu.Unlock()
	if len(problems) != 0 {
		t.Fatalf("activity loopback rejected actual requests: %v", problems)
	}
	piAppRecord(t, "activity-budget", map[string]any{"workers": len(first.hosts) + len(first.probes), "worker_limit": 3, "model_requests": p.count(), "request_limit": 24, "raw_tool_finished_exported": false, "raw_tool_result_reply_exported": false})
	if err := h.shutdown(); err != nil {
		t.Fatalf("restarted app did not reap normally: %v", err)
	}
}

type piCrashProvider struct {
	activity *piActivityProvider
	mu       sync.Mutex
	bodies   []json.RawMessage
	seen     map[string]bool
	problems []string
}

func (p *piCrashProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err == nil {
		err = json.Unmarshal(body, &request)
	}
	marker := ""
	for _, message := range request.Messages {
		if message.Role == "user" {
			for _, candidate := range []string{"ACTIVITY_A", "ACTIVITY_B", "ACTIVITY_CHAT_QUIT"} {
				if strings.Contains(string(message.Content), candidate) {
					marker = candidate
				}
			}
		}
	}
	p.mu.Lock()
	p.bodies = append(p.bodies, append(json.RawMessage(nil), body...))
	if err == nil && (len(p.bodies) > 12 || marker == "" || p.seen[marker]) {
		err = fmt.Errorf("unexpected or repeated crash scenario request: marker=%q count=%d", marker, len(p.bodies))
	}
	if err != nil {
		p.problems = append(p.problems, err.Error())
	} else {
		p.seen[marker] = true
	}
	p.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	p.activity.ServeHTTP(w, r)
}

func (p *piCrashProvider) snapshot() ([]json.RawMessage, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]json.RawMessage(nil), p.bodies...), append([]string(nil), p.problems...)
}

type piCrashHarness struct {
	*piActivityHarness
	provider    *piCrashProvider
	server      *httptest.Server
	killed      *piAppHost
	releaseA    sync.Once
	releaseB    sync.Once
	releaseChat sync.Once
}

func (h *piCrashHarness) screenshot(name string) {
	h.t.Helper()
	var save func()
	h.onUI(func() error {
		h.tester.Frame()
		image := h.tester.Image()
		piAppRecord(h.t, "crash-image", map[string]any{"name": name, "viewport": image.Bounds()})
		save = func() { writePreviewPNG(h.t, filepath.Join(h.evidence, name+".png"), image) }
		return nil
	})
	save()
}

func (h *piCrashHarness) release() {
	p := h.provider.activity
	h.releaseA.Do(func() { close(p.releaseA) })
	h.releaseB.Do(func() { close(p.releaseB) })
	h.releaseChat.Do(func() { close(p.quitHold) })
}

func (h *piCrashHarness) observeKilled(host *piAppHost) bool {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	outcome, err := host.client.Wait(ctx)
	callbacks, applied := false, false
	select {
	case <-host.client.CallbacksDone():
		callbacks = true
	case <-ctx.Done():
	}
	select {
	case <-host.exitApplied:
		applied = true
	case <-ctx.Done():
	}
	host.mu.Lock()
	observed := host.outcome
	bridges := make([]*piIssueTools, 0, len(host.bridges))
	for _, bridge := range host.bridges {
		bridges = append(bridges, bridge)
	}
	host.mu.Unlock()
	drained := true
	for _, bridge := range bridges {
		select {
		case <-bridge.done:
		case <-ctx.Done():
			drained = false
		}
	}
	done := shutdownChannelClosed(host.client.Done())
	piAppRecord(h.t, "crash-owned-exit", map[string]any{"injected": true, "wait": outcome, "wait_error": fmt.Sprint(err), "on_exit": observed, "done": done, "callbacks_done": callbacks, "exit_applied": applied, "bridge_count": len(bridges), "bridges_done": drained})
	return outcome.PID > 0 && outcome.Reaped && outcome.ExitCode != 0 && err != nil && observed.PID == outcome.PID && observed.Reaped && observed.ExitCode == outcome.ExitCode && fmt.Sprint(observed.Err) == fmt.Sprint(outcome.Err) && done && callbacks && applied && drained
}

func (h *piCrashHarness) cleanup() {
	h.release()
	first := h.piFirstUseHarness
	var current *piAppHost
	if first.pump != nil && !first.closed {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := first.actor(ctx, func() { current = first.app.pi })
		cancel()
		if err != nil {
			h.t.Errorf("capture crash cleanup owner: %v", err)
		}
		if current != h.killed || h.killed == nil {
			observedHost := make(chan *piAppHost, 1)
			watcher := &uiWaiter{ch: make(chan struct{}), cond: func() bool {
				if first.app.pi == nil || first.app.pi.client == nil {
					return false
				}
				observedHost <- first.app.pi
				return true
			}}
			first.pump.mu.Lock()
			first.pump.waiters = append(first.pump.waiters, watcher)
			first.pump.mu.Unlock()
			if err := first.shutdown(); err != nil {
				h.t.Errorf("normal crash-scenario shutdown failed: %v", err)
			}
			first.pump.mu.Lock()
			for i, waiter := range first.pump.waiters {
				if waiter == watcher {
					first.pump.waiters = append(first.pump.waiters[:i], first.pump.waiters[i+1:]...)
					break
				}
			}
			first.pump.mu.Unlock()
			select {
			case host := <-observedHost:
				first.hosts = append(first.hosts, host)
			default:
			}
		}
	}
	seen := map[*pi.Client]bool{}
	for _, host := range append(append([]*piAppHost{}, first.hosts...), current, first.capturedHost) {
		if host == nil || host.client == nil || seen[host.client] {
			continue
		}
		seen[host.client] = true
		if host == h.killed {
			if !h.observeKilled(host) {
				h.t.Error("injected crash did not retain matching owned exit and callbacks")
				first.cleanupClient(host.client, host, "injected crash")
			}
		} else {
			first.cleanupClient(host.client, host, "crash-scenario app")
		}
	}
	if len(seen) > 3 {
		h.t.Errorf("crash scenario exceeded its worker ceiling: %d", len(seen))
	}
	var observed *pi.Client
	if first.pump != nil {
		if first.closed {
			observed = first.pump.observedPi
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := first.actor(ctx, func() { observed = first.pump.observedPi })
			cancel()
			if err != nil {
				h.t.Errorf("capture observed crash-scenario client: %v", err)
			}
		}
	}
	if observed != nil && !seen[observed] {
		first.cleanupClient(observed, nil, "unmatched crash-scenario app")
		h.t.Error("observed crash-scenario client has no captured host or OnExit owner")
	}
	if first.pump != nil && !first.closed {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := first.waitUI(ctx, func() bool {
			return first.app.submissionInFlight == 0 && first.app.contactHistoryInFlight == 0 && !first.app.connectionApplying && len(first.app.piQuestionWaiters) == 0
		})
		cancel()
		if err != nil {
			h.t.Errorf("crash cleanup callbacks remain active: %v", err)
		} else {
			first.closed = true
			close(first.pump.q)
			<-first.pump.done
		}
	}
	if h.server != nil {
		h.server.CloseClientConnections()
		done := make(chan struct{})
		go func() { h.server.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			h.t.Error("crash loopback server did not close")
		}
	}
	bodies, problems := h.provider.snapshot()
	h.provider.activity.mu.Lock()
	activityProblems := append([]string(nil), h.provider.activity.problems...)
	h.provider.activity.mu.Unlock()
	piAppRecord(h.t, "crash-final-provider", map[string]any{"requests": len(bodies), "bodies": bodies, "problems": problems, "activity_problems": activityProblems, "worker_limit": 3, "request_limit": 12})
	if len(problems) != 0 || len(activityProblems) != 0 || len(bodies) > 12 {
		h.t.Errorf("crash provider failed: requests=%d problems=%v activity=%v", len(bodies), problems, activityProblems)
	}
}

func TestPiAppCrashRecoveryNoReplay(t *testing.T) {
	node, worker := os.Getenv("NAGI_TEST_NODE"), os.Getenv("NAGI_TEST_WORKER")
	if !filepath.IsAbs(node) || !filepath.IsAbs(worker) {
		t.Fatal("NAGI_TEST_NODE and NAGI_TEST_WORKER must identify the pinned payload")
	}
	var err error
	node, err = filepath.EvalSymlinks(node)
	if err != nil {
		t.Fatal(err)
	}
	worker, err = filepath.EvalSymlinks(worker)
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Dir(filepath.Dir(node))
	if node != filepath.Join(payload, "bin", "node") || worker != filepath.Join(payload, "dist", "worker.js") {
		t.Fatal("pinned inputs do not belong to the same product payload")
	}
	for _, path := range []string{node, worker} {
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("pinned input %q is not a regular file: %v", path, statErr)
		}
	}
	evidence := os.Getenv("NAGI_ACTIVITY_EVIDENCE_DIR")
	if !filepath.IsAbs(evidence) {
		t.Fatal("crash evidence directory must be absolute")
	}
	worktree, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	worktree, err = filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err = filepath.EvalSymlinks(evidence)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(evidence)
	if err != nil || !info.IsDir() {
		t.Fatalf("crash evidence directory is unavailable: %v", err)
	}
	relative, err := filepath.Rel(worktree, evidence)
	if err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatal("crash evidence must be outside the source tree")
	}
	inputs := map[string]string{}
	for _, path := range []string{node, worker, "pi_app_integration_test.go", "connection_app.go", "pi_app.go", "pi_activity.go"} {
		digest, hashErr := piAppFileHash(path)
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		inputs[path] = digest
	}
	piAppRecord(t, "crash-source-inputs", inputs)
	root := t.TempDir()
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("TMPDIR", root)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", "")
	isolateConnectionApp(t, root, payload)
	t.Setenv("SAKI_GUI_STORE", guiStorePath())
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	first := &piFirstUseHarness{t: t, app: app, provider: &piAppProvider{limit: 12}}
	first.pump = newUIPump(t, app)
	first.tester = ui.NewTester(app.view, 1040, 720)
	first.tester.SetLocale("en")
	first.tester.SetFocused(true)
	activity := &piActivityProvider{harness: first, results: make(map[string]string), stopB: true, arrivedA: make(chan struct{}), releaseA: make(chan struct{}), arrivedB: make(chan struct{}), releaseB: make(chan struct{}), quitStart: make(chan struct{}), quitHold: make(chan struct{})}
	provider := &piCrashProvider{activity: activity, seen: make(map[string]bool)}
	h := &piCrashHarness{piActivityHarness: &piActivityHarness{piFirstUseHarness: first, provider: activity, evidence: evidence}, provider: provider}
	h.server = httptest.NewServer(provider)
	t.Cleanup(h.cleanup)
	h.onUI(func() error {
		if app.selectedContact() == nil || app.connectionForm == nil || app.connectionForm.Draft.Slots != "2" {
			return errors.New("fresh crash scenario has no disconnected owner or default capacity")
		}
		h.contactID = app.selectedContact().ID
		return nil
	})
	h.input("API endpoint", h.server.URL+"/v1")
	h.input("API key", "nagi-product-loopback")
	h.input("Default model", "nagi-product-fixture")
	h.activate("Capacity & execution limits")
	h.input("Context tokens", "65536")
	h.input("Output tokens", "4096")
	h.input("Maximum active tasks", "3")
	h.onUI(func() error {
		app.chooseFolder = func(mygo.OpenDialogOptions) ([]string, error) { return []string{project}, nil }
		return nil
	})
	h.click("Choose folder…")
	h.wait(func() bool { return !app.connectionForm.Busy })
	h.click("Save configuration")
	h.wait(func() bool { return !app.connectionForm.Busy && !app.connectionSaveInFlight })
	h.click("Apply saved")
	h.wait(func() bool { return !app.connectionApplying })
	var original *piAppHost
	h.onUI(func() error {
		original = app.pi
		if original == nil || original.client == nil || app.runtimeStatus != runtimeStateReady || app.connectionForm.Error != "" || app.maxActiveTasks() != 3 || app.connectionProbe != nil {
			return errors.New("crash scenario did not apply a three-slot app-owned host")
		}
		return nil
	})
	first.hosts = append(first.hosts, original)
	ready, err := original.client.Ready(context.Background())
	if err != nil || ready.Engine.Version != "1.5.2" || ready.Runtime.Version != "v22.23.5" || ready.WorkerEpoch == "" {
		t.Fatalf("original app worker readiness: %+v %v", ready, err)
	}
	h.click("← Back to workbench")
	h.click("Work")
	issueA := h.createIssue("ACTIVITY_A")
	issueB := h.createIssue("ACTIVITY_B")
	activity.mu.Lock()
	activity.issueA, activity.issueB = issueA, issueB
	activity.mu.Unlock()
	msgs := locales.In("en")
	h.selectIssue("ACTIVITY_A", issueA)
	h.startIssue(msgs.WorkStart(), issueA)
	h.waitSignal(activity.arrivedA, "activity A request before held question")
	h.releaseA.Do(func() { close(activity.releaseA) })
	h.wait(func() bool {
		issue := app.contactByID(h.contactID).issueByID(issueA)
		return issue != nil && issue.openQuestion() != nil
	})
	var runA, runB piActivityRun
	var callA, callB piQuestionCall
	var bindingA, bindingB *piActivityBinding
	var questionRefA, questionRefB string
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		issue := ct.issueByID(issueA)
		execution, question := latestIssueExecution(issue), issue.openQuestion()
		if execution == nil || execution.PiRun == nil || !execution.PiRun.Admitted || question == nil || question.PiCall == nil || question.DeliveryState != "" || app.piQuestionWaiters[question.PiCall.OperationID] == nil {
			return errors.New("activity A did not admit its real unanswered held question")
		}
		runA, callA, bindingA, questionRefA = *execution.PiRun, *question.PiCall, clonePiActivityBinding(issue.PiBinding), question.Ref
		if callA.WorkerEpoch != ready.WorkerEpoch || callA.ContextID != runA.ContextID || callA.RunID != runA.RunID || callA.IssueID != issueA || callA.ContactID != h.contactID || callA.ExecutionID != execution.ID || callA.CallID != "activity-wait-a" {
			return errors.New("activity A held call lost its actual owner")
		}
		operation := app.issueToolLedger.operationByID(callA.OperationID)
		if bindingA == nil || callA.OperationID == "" || callA.SessionRef != runA.SessionRef || question.RequirementsRevision != issue.RequirementsRevision || operation == nil || operation.PiCall == nil || *operation.PiCall != callA || operation.Receipt == nil || operation.Receipt.QuestionRef != questionRefA {
			return errors.New("activity A question lacks its captured durable operation")
		}
		if app.connectionReloadBlocked() == "" {
			return errors.New("live activity did not block Apply")
		}
		return nil
	})
	h.backToIssues()
	h.selectIssue("ACTIVITY_B", issueB)
	h.startIssue(msgs.WorkStart(), issueB)
	h.wait(func() bool {
		issue := app.contactByID(h.contactID).issueByID(issueB)
		return issue != nil && issue.openQuestion() != nil
	})
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		issue := ct.issueByID(issueB)
		execution, question := latestIssueExecution(issue), issue.openQuestion()
		if execution == nil || execution.PiRun == nil || !execution.PiRun.Admitted || question == nil || question.PiCall == nil || question.DeliveryState != "" || app.piQuestionWaiters[question.PiCall.OperationID] == nil {
			return errors.New("activity B did not admit its real unanswered held question")
		}
		runB, callB, bindingB, questionRefB = *execution.PiRun, *question.PiCall, clonePiActivityBinding(issue.PiBinding), question.Ref
		if callB.WorkerEpoch != ready.WorkerEpoch || callB.ContextID != runB.ContextID || callB.RunID != runB.RunID || callB.IssueID != issueB || callB.ContactID != h.contactID || callB.ExecutionID != execution.ID || callB.CallID != "activity-wait-b" || runB.ContextID == runA.ContextID || runB.RunID == runA.RunID || callB.OperationID == callA.OperationID {
			return errors.New("activity B did not retain a distinct actual call owner")
		}
		operation := app.issueToolLedger.operationByID(callB.OperationID)
		if bindingB == nil || callB.OperationID == "" || callB.SessionRef != runB.SessionRef || question.RequirementsRevision != issue.RequirementsRevision || operation == nil || operation.PiCall == nil || *operation.PiCall != callB || operation.Receipt == nil || operation.Receipt.QuestionRef != questionRefB {
			return errors.New("activity B question lacks its captured durable operation")
		}
		return nil
	})
	h.activateActivity(msgs.WorkStopIssueTask(), func() error {
		ct := app.selectedContact()
		issue := ct.issueByID(issueB)
		execution := latestIssueExecution(issue)
		if ct.ID != h.contactID || ct.issueView.DetailID != issueB || execution == nil || execution.PiRun == nil || execution.PiRun.RunID != runB.RunID || !execution.PiRun.CancelRequested {
			return errors.New("GUI Stop did not target activity B")
		}
		return nil
	})
	h.wait(func() bool {
		issue := app.contactByID(h.contactID).issueByID(issueB)
		execution := latestIssueExecution(issue)
		return execution != nil && execution.Status == issueExecutionInterrupted && issue.Work.ActiveTaskID == ""
	})
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		a, b := ct.issueByID(issueA), ct.issueByID(issueB)
		questionA, questionB := a.questionByRef(questionRefA), b.questionByRef(questionRefB)
		if a.Work.ActiveTaskID != runA.RunID || questionA == nil || questionA.PiCall == nil || *questionA.PiCall != callA || questionA.DeliveryState != "" || app.piQuestionWaiters[callA.OperationID] == nil || questionB == nil || questionB.PiCall == nil || *questionB.PiCall != callB || questionB.DeliveryState != "" || questionB.AnswerSupplementID != "" {
			return errors.New("targeted Stop changed the other held activity or answered a question")
		}
		return nil
	})
	h.send("ACTIVITY_CHAT_QUIT")
	h.waitSignal(activity.quitStart, "unfinished conversation before crash")
	h.wait(func() bool {
		ct := app.contactByID(h.contactID)
		return ct != nil && ct.PiBinding != nil && ct.ActiveTaskID != "" && ct.SendState == sendAccepted
	})
	var chatBinding *piConversationBinding
	var chatRun string
	var pending *pendingSend
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		chatBinding, chatRun, pending = clonePiConversationBinding(ct.PiBinding), ct.ActiveTaskID, clonePendingSend(ct.pendingSend)
		if chatBinding == nil || pending == nil || pending.State != "unknown" || pending.Draft != "ACTIVITY_CHAT_QUIT" || chatRun == "" || chatBinding.Context.ContextID == runA.ContextID || chatBinding.Context.ContextID == runB.ContextID || app.connectionReloadBlocked() == "" {
			return errors.New("active conversation did not retain a distinct owner or block Apply")
		}
		return nil
	})
	const unsentBeforeLoss = "Unsent draft kept during the worker loss"
	const unsentAfterLoss = unsentBeforeLoss + " and continued afterward"
	checkPresentation := func(phase string, check func(func(error) error) error) {
		var checkErr error
		var saveFailureImage func()
		h.onUI(func() error {
			fail := func(err error) error {
				image := h.tester.Image()
				saveFailureImage = func() {
					file, err := os.CreateTemp(evidence, "crash-notice-presentation-failure-*.png")
					if err != nil {
						t.Fatalf("create presentation failure image: %v", err)
					}
					if err := file.Close(); err != nil {
						t.Fatalf("close presentation failure image: %v", err)
					}
					writePreviewPNG(t, file.Name(), image)
					t.Logf("crash notice %s failure image: %s", phase, file.Name())
				}
				return err
			}
			checkErr = check(fail)
			return nil
		})
		if checkErr != nil {
			saveFailureImage()
			t.Fatal(checkErr)
		}
	}
	checkPresentation("draft-before-loss", func(fail func(error) error) error {
		ct := app.contactByID(h.contactID)
		label := msgs.ComposerLabel(ct.Name)
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic": false, "phase": "initial-draft", "draft_empty": ct.Draft == "", "observed_draft": ct.Draft})
		if ct.Draft != "" {
			return fail(errors.New("admitted conversation left an unexpected composer draft"))
		}
		err := h.focus(label, true)
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic": false, "phase": "initial-focus", "focused": h.tester.Focused(label), "error": fmt.Sprint(err)})
		if err != nil {
			return fail(err)
		}
		h.tester.Type(unsentBeforeLoss)
		h.tester.Frame()
		active, _ := h.tester.TextInputActive()
		focused := h.tester.Focused(label)
		draftEqual := ct.Draft == unsentBeforeLoss
		pendingEqual := reflect.DeepEqual(ct.pendingSend, pending)
		piAppRecord(t, "crash-notice-loss-observation", map[string]any{"synthetic": false, "phase": "before-loss", "text_input_active": active, "composer_focused": focused, "draft_equal": draftEqual, "expected_draft": unsentBeforeLoss, "observed_draft": ct.Draft, "pending_equal": pendingEqual, "return_focus_pending": app.focusConnectionEntry, "recovery_settings_focused": h.tester.Focused(msgs.ConnectionSettingsAction())})
		if !active || !focused || !draftEqual || !pendingEqual {
			return fail(errors.New("actual composer did not retain the focused unsent draft before worker loss"))
		}
		piAppRecord(t, "crash-notice-draft-before", map[string]any{"synthetic": false, "draft": ct.Draft, "focused": true, "text_input_active": active})
		return nil
	})
	h.screenshot("crash-before-owned-worker-loss")
	bodies, _ := provider.snapshot()
	if len(bodies) != 3 {
		t.Fatalf("expected three admitted controlled requests before crash, got %d", len(bodies))
	}
	h.killed = original
	killCtx, killCancel := context.WithTimeout(context.Background(), 15*time.Second)
	killed, killErr := original.client.Kill(killCtx)
	killCancel()
	piAppRecord(t, "crash-injection", map[string]any{"kind": "owned-client-kill", "outcome": killed, "error": fmt.Sprint(killErr)})
	if killed.PID <= 0 || !killed.Reaped || killed.ExitCode == 0 || killErr == nil || !h.observeKilled(original) {
		t.Fatalf("owned crash injection did not yield a correlated abnormal exit: %+v %v", killed, killErr)
	}
	h.wait(func() bool { return len(app.piQuestionWaiters) == 0 && app.submissionInFlight == 0 })
	var lossErr error
	var saveLossImage func()
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		label := msgs.ComposerLabel(ct.Name)
		defer func() {
			if lossErr == nil {
				return
			}
			image := h.tester.Image()
			saveLossImage = func() {
				file, err := os.CreateTemp(evidence, "crash-notice-loss-failure-*.png")
				if err != nil {
					t.Fatalf("create loss failure image: %v", err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("close loss failure image: %v", err)
				}
				writePreviewPNG(t, file.Name(), image)
				t.Logf("crash notice loss failure image: %s", file.Name())
			}
		}()
		h.tester.Frame()
		active, _ := h.tester.TextInputActive()
		focused := h.tester.Focused(label)
		draftEqual := ct.Draft == unsentBeforeLoss
		piAppRecord(t, "crash-notice-loss-observation", map[string]any{"synthetic": false, "phase": "after-loss-before-typing", "text_input_active": active, "composer_focused": focused, "draft_equal": draftEqual, "expected_draft": unsentBeforeLoss, "observed_draft": ct.Draft, "return_focus_pending": app.focusConnectionEntry, "recovery_settings_focused": h.tester.Focused(msgs.ConnectionSettingsAction())})
		if !active || !focused || !draftEqual {
			lossErr = errors.New("worker loss changed the focused unsent composer draft")
			return nil
		}
		h.tester.Type(" and continued afterward")
		h.tester.Frame()
		active, _ = h.tester.TextInputActive()
		focused = h.tester.Focused(label)
		draftEqual = ct.Draft == unsentAfterLoss
		pendingEqual := reflect.DeepEqual(ct.pendingSend, pending)
		piAppRecord(t, "crash-notice-loss-observation", map[string]any{"synthetic": false, "phase": "after-loss-after-typing", "text_input_active": active, "composer_focused": focused, "draft_equal": draftEqual, "expected_draft": unsentAfterLoss, "observed_draft": ct.Draft, "pending_equal": pendingEqual, "return_focus_pending": app.focusConnectionEntry, "recovery_settings_focused": h.tester.Focused(msgs.ConnectionSettingsAction())})
		if !active || !focused || !draftEqual || !pendingEqual {
			lossErr = errors.New("notice change interrupted continued typing or changed the pending send")
			return nil
		}
		piAppRecord(t, "crash-notice-draft-after", map[string]any{"synthetic": false, "draft": ct.Draft, "focused": true, "text_input_active": active})
		return nil
	})
	if lossErr != nil {
		saveLossImage()
		t.Fatal(lossErr)
	}
	verifyUnknown := func(current *appModel) error {
		ct := current.contactByID(h.contactID)
		if ct == nil || ct.PiBinding == nil || !reflect.DeepEqual(ct.PiBinding, chatBinding) || !reflect.DeepEqual(ct.pendingSend, pending) || ct.Draft != unsentAfterLoss || ct.SendState != sendUnknown || ct.ActiveTaskID != "" {
			return errors.New("crash lost the captured unknown conversation or its source")
		}
		a, b := ct.issueByID(issueA), ct.issueByID(issueB)
		if a == nil || b == nil || !reflect.DeepEqual(a.PiBinding, bindingA) || !reflect.DeepEqual(b.PiBinding, bindingB) {
			return errors.New("crash changed the captured activity bindings")
		}
		executionA, executionB := latestIssueExecution(a), latestIssueExecution(b)
		questionA, questionB := a.questionByRef(questionRefA), b.questionByRef(questionRefB)
		operationA, operationB := current.issueToolLedger.operationByID(callA.OperationID), current.issueToolLedger.operationByID(callB.OperationID)
		if executionA == nil || executionA.Status != issueExecutionUnknown || executionA.PiRun == nil || !reflect.DeepEqual(*executionA.PiRun, runA) || executionB == nil || executionB.Status != issueExecutionInterrupted || executionB.PiRun == nil || executionB.PiRun.RunID != runB.RunID || questionA == nil || questionA.PiCall == nil || *questionA.PiCall != callA || questionA.State != issueQuestionOpen || questionA.DeliveryState != "" || questionA.AnswerSupplementID != "" || questionB == nil || questionB.PiCall == nil || *questionB.PiCall != callB || questionB.State != issueQuestionOpen || questionB.DeliveryState != "" || questionB.AnswerSupplementID != "" || operationA == nil || operationA.PiCall == nil || *operationA.PiCall != callA || operationB == nil || operationB.PiCall == nil || *operationB.PiCall != callB || len(current.piQuestionWaiters) != 0 {
			return errors.New("crash lost an unknown activity, its unanswered call, or the independently stopped activity")
		}
		return nil
	}
	h.onUI(func() error {
		if app.pi != original || app.runtimeStatus != runtimeStateExited || app.runtimeBuiltinToolsLoaded || app.localSaveErr != "" || app.connectionReloadBlocked() != "" {
			return errors.New("observed dead host did not expose explicit Apply recovery")
		}
		return verifyUnknown(app)
	})
	checkPresentation("exited", func(fail func(error) error) error {
		ct := app.contactByID(h.contactID)
		h.tester.Frame()
		count := 0
		for _, text := range h.tester.Texts() {
			if text == msgs.SendDisconnectedState() {
				count++
			}
		}
		bounds, found := h.tester.Find(msgs.SendDisconnectedState())
		viewport := h.tester.Image().Bounds()
		reloadBounds, reloadFound := h.tester.Find(msgs.ReloadAction())
		knownMismatch := app.connectionSendFailureKnown(ct)
		sendErrorPresent := ct.SendError != ""
		summaryInViewport := found && bounds.W > 0 && bounds.H > 0 && bounds.X >= 0 && bounds.Y >= 0 && bounds.X+bounds.W <= float32(viewport.Dx()) && bounds.Y+bounds.H <= float32(viewport.Dy())
		notConnected := h.tester.HasText(msgs.ConnectionStatusNotConnected())
		managedTitle := h.tester.HasText(msgs.ConnectionRecoveryManagedTitle())
		managedHint := h.tester.HasText(msgs.ConnectionRecoveryManagedHint())
		diagnosticVisible := h.tester.HasText(ct.SendError)
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic": false, "phase": "exited", "known_mismatch": knownMismatch, "send_error_present": sendErrorPresent, "send_error": ct.SendError, "summary_count": count, "summary_found": found, "summary_bounds": bounds, "summary_in_viewport": summaryInViewport, "viewport": viewport, "not_connected": notConnected, "managed_title": managedTitle, "managed_hint": managedHint, "diagnostic_visible": diagnosticVisible, "reload_found": reloadFound, "reload_bounds": reloadBounds})
		if knownMismatch || !sendErrorPresent || count != 1 || !summaryInViewport || !notConnected || managedTitle || managedHint || diagnosticVisible || reloadFound {
			return fail(fmt.Errorf("actual exited recovery presentation is not neutral, singular and folded: summary_count=%d first_match=%+v found=%v viewport=%v", count, bounds, found, viewport))
		}
		piAppRecord(t, "crash-notice-exited", map[string]any{"synthetic": false, "summary_count": count, "summary_first_match": bounds, "viewport": viewport, "reload_visible": false, "diagnostic_folded": true})
		return nil
	})
	h.screenshot("crash-after-owned-worker-loss")
	var saveNoticeImage func()
	checkPresentation("details", func(fail func(error) error) error {
		ct := app.contactByID(h.contactID)
		previousDetail, previousKind := ct.StatusDetail, ct.StatusDetailKind
		defer func() {
			ct.StatusDetail, ct.StatusDetailKind = previousDetail, previousKind
			h.tester.Frame()
		}()
		const syntheticStatus = "C3 synthetic second connection diagnostic"
		ct.StatusDetail, ct.StatusDetailKind = syntheticStatus, "status-connection-lost"
		h.tester.Frame()
		count := 0
		for _, text := range h.tester.Texts() {
			if text == msgs.SendDisconnectedState() {
				count++
			}
		}
		sendVisible := h.tester.HasText(ct.SendError)
		statusVisible := h.tester.HasText(syntheticStatus)
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic": true, "phase": "details-folded", "summary_count": count, "actual_send_error": ct.SendError, "synthetic_status": syntheticStatus, "send_visible": sendVisible, "status_visible": statusVisible})
		if count != 1 || sendVisible || statusVisible {
			return fail(fmt.Errorf("synthetic two-diagnostic notice is not singular and folded: count=%d", count))
		}
		err := h.focus(msgs.DetailsAction(), false)
		detailsBounds, detailsFound := h.tester.Find(msgs.DetailsAction())
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic_status": true, "phase": "details-focus", "found": detailsFound, "bounds": detailsBounds, "focused": h.tester.Focused(msgs.DetailsAction()), "error": fmt.Sprint(err)})
		if err != nil {
			return fail(err)
		}
		h.tester.Key(0, ui.KeySpace)
		h.tester.Frame()
		sendVisible = h.tester.HasText(ct.SendError)
		statusVisible = h.tester.HasText(syntheticStatus)
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic": true, "phase": "details-expanded", "send_visible": sendVisible, "status_visible": statusVisible, "actual_send_error": ct.SendError, "synthetic_status": syntheticStatus})
		if !sendVisible || !statusVisible {
			return fail(errors.New("actual Details control did not reveal both eligible diagnostics"))
		}
		err = h.focus(msgs.CopyErrorDetailsLabel(), false)
		copyBounds, copyFound := h.tester.Find(msgs.CopyErrorDetailsLabel())
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic_status": true, "phase": "copy-focus", "found": copyFound, "bounds": copyBounds, "focused": h.tester.Focused(msgs.CopyErrorDetailsLabel()), "error": fmt.Sprint(err)})
		if err != nil {
			return fail(err)
		}
		image := h.tester.Image()
		saveNoticeImage = func() { writePreviewPNG(t, filepath.Join(evidence, "crash-notice-expanded-details.png"), image) }
		h.tester.SetClipboard("crash-notice-copy-sentinel")
		h.tester.Key(0, ui.KeySpace)
		h.tester.Frame()
		copied := h.tester.Clipboard()
		copyHasSend := strings.Contains(copied, ct.SendError)
		copyHasStatus := strings.Contains(copied, syntheticStatus)
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic": true, "phase": "details-copy", "copied": copied, "actual_send_error": ct.SendError, "synthetic_status": syntheticStatus, "copy_has_send": copyHasSend, "copy_has_status": copyHasStatus})
		if !copyHasSend || !copyHasStatus {
			return fail(errors.New("actual diagnostic Copy did not retain both eligible diagnostics"))
		}
		err = h.focus(msgs.HideDetailsAction(), false)
		hideBounds, hideFound := h.tester.Find(msgs.HideDetailsAction())
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic_status": true, "phase": "hide-focus", "found": hideFound, "bounds": hideBounds, "focused": h.tester.Focused(msgs.HideDetailsAction()), "error": fmt.Sprint(err)})
		if err != nil {
			return fail(err)
		}
		h.tester.Key(0, ui.KeySpace)
		h.tester.Frame()
		sendVisible = h.tester.HasText(ct.SendError)
		statusVisible = h.tester.HasText(syntheticStatus)
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic": true, "phase": "details-hidden", "send_visible": sendVisible, "status_visible": statusVisible, "actual_send_error": ct.SendError, "synthetic_status": syntheticStatus})
		if sendVisible || statusVisible {
			return fail(errors.New("actual Hide details control did not fold the diagnostics"))
		}
		piAppRecord(t, "crash-notice-details", map[string]any{"synthetic_status": true, "actual_send_error": ct.SendError, "copied": copied, "image": "crash-notice-expanded-details.png", "viewport": image.Bounds()})
		if err := verifyUnknown(app); err != nil {
			piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic_status": true, "phase": "details-state", "error": err.Error()})
			return fail(err)
		}
		return nil
	})
	saveNoticeImage()
	checkPresentation("synthetic", func(fail func(error) error) error {
		ct := app.contactByID(h.contactID)
		previousSend, previousSendKind, previousSendDetail := ct.SendError, ct.SendErrorKind, ct.SendErrorDetail
		previousStatus, previousStatusKind := ct.StatusDetail, ct.StatusDetailKind
		previousRuntime, previousStage, previousRuntimeError, previousRuntimeKind := app.runtimeStatus, app.runtimeStage, app.runtimeErr, app.runtimeErrKind
		defer func() {
			ct.SendError, ct.SendErrorKind, ct.SendErrorDetail = previousSend, previousSendKind, previousSendDetail
			ct.StatusDetail, ct.StatusDetailKind = previousStatus, previousStatusKind
			app.runtimeStatus, app.runtimeStage, app.runtimeErr, app.runtimeErrKind = previousRuntime, previousStage, previousRuntimeError, previousRuntimeKind
			h.tester.Frame()
		}()
		ct.SendError, ct.SendErrorKind, ct.SendErrorDetail = "", "", ""
		ct.StatusDetail, ct.StatusDetailKind = "", ""
		h.tester.Frame()
		count := 0
		for _, text := range h.tester.Texts() {
			if text == msgs.SendDisconnectedState() {
				count++
			}
		}
		unknown := ct.SendState == sendUnknown
		pendingEqual := reflect.DeepEqual(ct.pendingSend, pending)
		draftEqual := ct.Draft == unsentAfterLoss
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic": true, "phase": "no-details", "summary_count": count, "send_unknown": unknown, "pending_equal": pendingEqual, "draft_equal": draftEqual, "observed_draft": ct.Draft, "expected_draft": unsentAfterLoss})
		if count != 1 || !unknown || !pendingEqual || !draftEqual {
			return fail(fmt.Errorf("synthetic no-details fallback lost its sole uncertainty notice or retained draft: count=%d", count))
		}
		ct.SendError = "connection_mismatch:C3 synthetic mismatch diagnostic"
		h.tester.Frame()
		mismatchVisible := h.tester.HasText(msgs.ConnectionMismatchSummary())
		diagnosticVisible := h.tester.HasText(ct.SendError)
		reloadBounds, reloadFound := h.tester.Find(msgs.ReloadAction())
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic": true, "phase": "known-mismatch", "mismatch_visible": mismatchVisible, "diagnostic_visible": diagnosticVisible, "diagnostic": ct.SendError, "reload_found": reloadFound, "reload_bounds": reloadBounds})
		if !mismatchVisible || diagnosticVisible || reloadFound {
			return fail(errors.New("synthetic known mismatch exposed its folded diagnostic or Reload"))
		}
		ct.SendError, ct.SendErrorKind, ct.SendErrorDetail = previousSend, previousSendKind, previousSendDetail
		ct.StatusDetail, ct.StatusDetailKind = previousStatus, previousStatusKind
		app.runtimeStatus, app.runtimeStage = runtimeStateFailed, "Connection configuration"
		app.runtimeErrKind, app.runtimeErr = runtimeNoticeStart, "C3 synthetic startup diagnostic"
		h.tester.Frame()
		managedTitle := h.tester.HasText(msgs.ConnectionRecoveryManagedTitle())
		startError := h.tester.HasText(msgs.RecoveryRuntimeStartFailed())
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic": true, "phase": "startup", "managed_title": managedTitle, "start_error": startError, "expected_title": msgs.ConnectionRecoveryManagedTitle(), "expected_start_error": msgs.RecoveryRuntimeStartFailed()})
		if !managedTitle || !startError {
			return fail(errors.New("synthetic startup failure lost the managed title or specific start error"))
		}
		piAppRecord(t, "crash-notice-synthetic", map[string]any{"synthetic": true, "fallback_summary_count": count, "known_mismatch_folded": true, "startup_title": msgs.ConnectionRecoveryManagedTitle(), "startup_error": msgs.RecoveryRuntimeStartFailed()})
		return nil
	})
	h.onUI(func() error { return verifyUnknown(app) })
	bodies, _ = provider.snapshot()
	if len(bodies) != 3 {
		t.Fatalf("recovery notice interactions sent an unexpected model request: %d", len(bodies))
	}
	storePath := app.store.path
	stored, err := openContactStore(storePath, project)
	if err != nil {
		t.Fatal(err)
	}
	var storedContact *storedContact
	for i := range stored.state.Contacts {
		if stored.state.Contacts[i].ID == h.contactID {
			storedContact = &stored.state.Contacts[i]
		}
	}
	var storedA, storedB *productIssue
	for _, ct := range contactsFromStore(stored) {
		if ct.ID == h.contactID {
			storedA, storedB = ct.issueByID(issueA), ct.issueByID(issueB)
		}
	}
	if storedContact == nil || !reflect.DeepEqual(storedContact.PiBinding, chatBinding) || storedContact.PendingSend == nil || storedContact.PendingSend.State != "unknown" || storedContact.PendingSend.Draft != pending.Draft || !reflect.DeepEqual(storedContact.PendingSend.Context, pending.Context) || storedA == nil || storedB == nil || !reflect.DeepEqual(storedA.PiBinding, bindingA) || !reflect.DeepEqual(storedB.PiBinding, bindingB) || latestIssueExecution(storedA) == nil || latestIssueExecution(storedA).Status != issueExecutionUnknown || latestIssueExecution(storedB) == nil || latestIssueExecution(storedB).Status != issueExecutionInterrupted || storedA.questionByRef(questionRefA) == nil || storedA.questionByRef(questionRefA).PiCall == nil || *storedA.questionByRef(questionRefA).PiCall != callA || storedA.questionByRef(questionRefA).DeliveryState != "" || stored.issueToolLedger == nil || stored.issueToolLedger.operationByID(callA.OperationID) == nil {
		t.Fatal("reopened actual store lost crash ownership or unknown delivery")
	}
	storedOperationA := stored.issueToolLedger.operationByID(callA.OperationID)
	storedOperationB := stored.issueToolLedger.operationByID(callB.OperationID)
	storedQuestionB := storedB.questionByRef(questionRefB)
	if !reflect.DeepEqual(storedContact.PendingSend, pending) || latestIssueExecution(storedA).PiRun == nil || !reflect.DeepEqual(*latestIssueExecution(storedA).PiRun, runA) || storedQuestionB == nil || storedQuestionB.PiCall == nil || *storedQuestionB.PiCall != callB || storedQuestionB.State != issueQuestionOpen || storedQuestionB.DeliveryState != "" || storedOperationA == nil || storedOperationA.PiCall == nil || *storedOperationA.PiCall != callA || storedOperationB == nil || storedOperationB.PiCall == nil || *storedOperationB.PiCall != callB {
		t.Fatal("reopened store changed a captured pending send or question operation")
	}
	if storedOperationA.Receipt == nil || storedOperationA.Receipt.QuestionRef != questionRefA || storedOperationB.Receipt == nil || storedOperationB.Receipt.QuestionRef != questionRefB {
		t.Fatal("reopened store lost the captured question receipts")
	}
	if storedContact.Draft != unsentAfterLoss {
		t.Fatal("reopened store lost the continued unsent composer draft")
	}
	piAppRecord(t, "crash-durable-unknown", map[string]any{"contact_id": h.contactID, "chat_run": chatRun, "chat_binding": chatBinding, "pending": storedContact.PendingSend, "activity_run": runA, "activity_call": callA, "stopped_run": runB, "stopped_call": callB, "requests": len(bodies)})
	h.onUI(func() error {
		h.tester.Key(ui.Cmd, ui.KeyComma)
		h.tester.Frame()
		if !app.settingsOpen {
			return errors.New("GUI shortcut did not open settings")
		}
		return nil
	})
	h.activate(msgs.SettingsConnection())
	h.onUI(func() error {
		h.tester.Frame()
		if app.settingsTab != settingsTabConnection || app.connectionForm == nil || app.connectionForm.ReloadBlocked != "" {
			return errors.New("GUI did not expose Apply for the observed dead host")
		}
		return nil
	})
	h.activate(msgs.ConnectionApplySaved())
	h.wait(func() bool { return !app.connectionApplying && app.pi != nil && app.pi != original })
	var replacement *piAppHost
	h.onUI(func() error {
		replacement = app.pi
		if replacement == nil || replacement.client == nil || replacement.client == original.client || app.runtimeStatus != runtimeStateReady || app.connectionForm.Error != "" {
			return errors.New("GUI Apply did not replace the observed dead host")
		}
		if err := verifyUnknown(app); err != nil {
			return err
		}
		replacement.mu.Lock()
		bridges := len(replacement.bridges)
		replacement.mu.Unlock()
		if bridges != 0 {
			return errors.New("GUI Apply recreated a previous run or bridge")
		}
		return nil
	})
	first.hosts = append(first.hosts, replacement)
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	replacementReady, readyErr := replacement.client.Ready(readyCtx)
	readyCancel()
	if readyErr != nil || replacementReady.WorkerEpoch == "" || replacementReady.WorkerEpoch == ready.WorkerEpoch {
		t.Fatalf("replacement worker epoch: %+v %v", replacementReady, readyErr)
	}
	bodies, _ = provider.snapshot()
	if len(bodies) != 3 {
		t.Fatalf("Apply replayed a controlled request: %d", len(bodies))
	}
	h.screenshot("crash-replacement-settings")
	h.click("← Back to workbench")
	var detailOpen bool
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		detailOpen = ct.issueView.DetailID != "" || ct.issueView.ShowConversationWork
		return nil
	})
	if detailOpen {
		h.backToIssues()
	}
	h.selectIssue("ACTIVITY_A", issueA)
	var activityGeneration int
	h.onUI(func() error {
		activityGeneration = app.contactByID(h.contactID).issueByID(issueA).Work.historyGen
		return nil
	})
	h.activateActivity(msgs.IssueReloadTask(), func() error {
		ct := app.selectedContact()
		if ct == nil || ct.ID != h.contactID || ct.issueView.DetailID != issueA || ct.issueByID(issueA).Work.historyGen != activityGeneration+1 {
			return errors.New("GUI did not inspect the captured old-epoch activity")
		}
		return nil
	})
	h.wait(func() bool { return app.contactHistoryInFlight == 0 })
	h.onUI(func() error {
		issue := app.contactByID(h.contactID).issueByID(issueA)
		if issue.Work.LoadErr != "Pi activity progress is unavailable." || latestIssueExecution(issue).Status != issueExecutionUnknown {
			return errors.New("old-epoch activity was remapped or its unavailable progress was hidden")
		}
		return verifyUnknown(app)
	})
	h.screenshot("crash-old-activity-unavailable")
	checkPresentation("recovered", func(fail func(error) error) error {
		ct := app.contactByID(h.contactID)
		h.tester.Frame()
		count := 0
		for _, text := range h.tester.Texts() {
			if text == msgs.SendUnknownState() {
				count++
			}
		}
		bounds, found := h.tester.Find(msgs.SendUnknownState())
		viewport := h.tester.Image().Bounds()
		reloadBounds, reloadFound := h.tester.Find(msgs.ReloadAction())
		summaryInViewport := found && bounds.W > 0 && bounds.H > 0 && bounds.X >= 0 && bounds.Y >= 0 && bounds.X+bounds.W <= float32(viewport.Dx()) && bounds.Y+bounds.H <= float32(viewport.Dy())
		reloadInViewport := reloadFound && reloadBounds.W > 0 && reloadBounds.H > 0 && reloadBounds.X >= 0 && reloadBounds.Y >= 0 && reloadBounds.X+reloadBounds.W <= float32(viewport.Dx()) && reloadBounds.Y+reloadBounds.H <= float32(viewport.Dy())
		draftEqual := ct.Draft == unsentAfterLoss
		unknown := ct.SendState == sendUnknown
		pendingEqual := reflect.DeepEqual(ct.pendingSend, pending)
		piAppRecord(t, "crash-notice-presentation-observation", map[string]any{"synthetic": false, "phase": "recovered", "summary_count": count, "summary_found": found, "summary_bounds": bounds, "summary_in_viewport": summaryInViewport, "viewport": viewport, "reload_found": reloadFound, "reload_bounds": reloadBounds, "reload_in_viewport": reloadInViewport, "draft_equal": draftEqual, "observed_draft": ct.Draft, "expected_draft": unsentAfterLoss, "send_unknown": unknown, "pending_equal": pendingEqual})
		if count != 1 || !summaryInViewport || !reloadInViewport || !draftEqual || !unknown || !pendingEqual {
			return fail(fmt.Errorf("recovered conversation does not show one unknown notice and guarded Reload: summary_count=%d first_match=%+v found=%v viewport=%v", count, bounds, found, viewport))
		}
		piAppRecord(t, "crash-notice-recovered", map[string]any{"synthetic": false, "summary_count": count, "summary_first_match": bounds, "viewport": viewport, "reload_visible": true, "draft": ct.Draft, "pending_state": ct.pendingSend.State})
		return nil
	})
	var historyGeneration int
	h.onUI(func() error {
		historyGeneration = app.contactByID(h.contactID).historyGen
		return nil
	})
	h.activate(msgs.ReloadAction())
	h.wait(func() bool {
		ct := app.contactByID(h.contactID)
		return ct != nil && ct.historyGen > historyGeneration && app.contactHistoryInFlight == 0
	})
	h.onUI(func() error {
		ct := app.contactByID(h.contactID)
		if ct.LoadErr != "" || !ct.Loaded {
			return errors.New("captured conversation history did not reload")
		}
		if err := verifyUnknown(app); err != nil {
			return err
		}
		piAppRecord(t, "crash-history-reload", map[string]any{"send_error": ct.SendError, "pending_state": ct.pendingSend.State, "send_unknown": ct.SendState == sendUnknown, "history_loaded": ct.Loaded})
		return nil
	})
	configCtx, configCancel := context.WithTimeout(context.Background(), 10*time.Second)
	configuration, configErr := replacement.client.ContextConfiguration(configCtx, chatBinding.Context.ContextID)
	configCancel()
	if configErr != nil || configuration.ContextID != chatBinding.Context.ContextID || configuration.BaseURL != chatBinding.Configuration.BaseURL || configuration.WireAPI != chatBinding.Configuration.WireAPI || configuration.Model != chatBinding.Configuration.Model || configuration.CredentialSource != chatBinding.Configuration.CredentialSource || configuration.ContextTokens != chatBinding.Configuration.ContextTokens || configuration.OutputTokens != chatBinding.Configuration.OutputTokens {
		t.Fatalf("reopened conversation changed its captured configuration: %+v %v", configuration, configErr)
	}
	bodies, _ = provider.snapshot()
	if len(bodies) != 3 {
		t.Fatalf("history recovery replayed a model request: %d", len(bodies))
	}
	piAppRecord(t, "crash-replacement", map[string]any{"old_epoch": ready.WorkerEpoch, "new_epoch": replacementReady.WorkerEpoch, "conversation": chatBinding.Context, "configuration": configuration, "unknown_send": true, "old_activity_unavailable": true, "requests": len(bodies)})
	if err := h.shutdown(); err != nil {
		t.Fatalf("replacement app did not drain normally: %v", err)
	}
	app, err = newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	first.app, first.pump, first.closed = app, newUIPump(t, app), false
	first.tester = ui.NewTester(app.view, 1040, 720)
	first.tester.SetLocale("en")
	first.tester.SetFocused(true)
	h.onUI(func() error {
		if err := verifyUnknown(app); err != nil {
			return err
		}
		app.startPiHost()
		return nil
	})
	h.wait(func() bool {
		return !app.connectionApplying && app.runtimeStatus == runtimeStateReady && app.contactHistoryInFlight == 0
	})
	var restarted *piAppHost
	h.onUI(func() error {
		restarted = app.pi
		if restarted == nil || restarted.client == nil || restarted.client == original.client || restarted.client == replacement.client {
			return errors.New("normal app startup did not own a fresh worker")
		}
		if err := verifyUnknown(app); err != nil {
			return err
		}
		restarted.mu.Lock()
		bridges := len(restarted.bridges)
		restarted.mu.Unlock()
		if bridges != 0 {
			return errors.New("normal app startup replayed a previous bridge")
		}
		return nil
	})
	first.hosts = append(first.hosts, restarted)
	readyCtx, readyCancel = context.WithTimeout(context.Background(), 10*time.Second)
	restartedReady, readyErr := restarted.client.Ready(readyCtx)
	readyCancel()
	if readyErr != nil || restartedReady.WorkerEpoch == "" || restartedReady.WorkerEpoch == ready.WorkerEpoch || restartedReady.WorkerEpoch == replacementReady.WorkerEpoch {
		t.Fatalf("normal restart worker epoch: %+v %v", restartedReady, readyErr)
	}
	bodies, _ = provider.snapshot()
	if len(first.hosts) != 3 || len(first.probes) != 0 || len(bodies) != 3 {
		t.Fatalf("crash recovery worker/request budget changed: workers=%d probes=%d requests=%d", len(first.hosts), len(first.probes), len(bodies))
	}
	piAppRecord(t, "crash-restart", map[string]any{"workers": len(first.hosts), "worker_limit": 3, "requests": len(bodies), "request_limit": 12, "old_epoch": ready.WorkerEpoch, "replacement_epoch": replacementReady.WorkerEpoch, "restart_epoch": restartedReady.WorkerEpoch, "unknown_send_preserved": true, "activity_call": callA})
	if err := h.shutdown(); err != nil {
		t.Fatalf("restarted app did not drain normally: %v", err)
	}
}

const (
	piResultsGoal          = "ACTIVITY_RESULT_RECEIPTS"
	piResultsContextName   = "Activity handoff"
	piResultsFirstContext  = "Use the first saved context snapshot."
	piResultsSecondContext = "Use the second saved context snapshot."
	piResultsFile          = "activity-handoff.md"
	piResultsUnwrittenFile = "unwritten-reference.md"
	piResultsFileContent   = "# Handoff\n\nVerified activity file.\n"
	piResultsCallID        = "activity-results-write"
	piResultsWriteAck      = "Successfully wrote to " + piResultsFile
	piResultsFirstResponse = "\n## First handoff\n\nSee `" + piResultsUnwrittenFile + "` for reference.\n\nFirst handoff complete.\n\n"
	piResultsNextResponse  = "\n## Second handoff\n\nContinuation complete.\n\n"
	piResultsRequestCount  = 3
)

type piResultsProvider struct {
	*piAppProvider
}

func (p piResultsProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	defer p.mu.Unlock()
	p.requests++
	p.bodies = append(p.bodies, body)
	if err == nil && (p.requests > p.limit || r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer nagi-product-loopback" || request.Model != "nagi-product-fixture" || !request.Stream || len(request.Messages) == 0) {
		err = fmt.Errorf("unexpected activity result request %d", p.requests)
	}
	delta := map[string]any{"role": "assistant"}
	finish := "stop"
	if err == nil {
		last := request.Messages[len(request.Messages)-1]
		contextText := piResultsFirstContext
		if p.requests == piResultsRequestCount {
			contextText = piResultsSecondContext
		}
		var userContent json.RawMessage
		for _, message := range request.Messages {
			if message.Role == "user" {
				userContent = message.Content
			}
		}
		snapshot := fmt.Sprintf("<context name=%q>\n%s\n</context>", piResultsContextName, contextText)
		var prompt string
		if json.Unmarshal(userContent, &prompt) != nil {
			var parts []pi.ContentBlock
			if json.Unmarshal(userContent, &parts) == nil {
				for _, part := range parts {
					if part.Type == "text" {
						prompt += part.Text
					}
				}
			}
		}
		if !strings.Contains(prompt, piResultsGoal) || !strings.Contains(prompt, snapshot) || p.requests == piResultsRequestCount && strings.Contains(prompt, piResultsFirstContext) {
			err = fmt.Errorf("request %d has the wrong issue or saved context snapshot: %s", p.requests, userContent)
		} else {
			switch p.requests {
			case 1:
				writeOffered := false
				for _, tool := range request.Tools {
					writeOffered = writeOffered || tool.Function.Name == "write"
				}
				if last.Role != "user" || !writeOffered {
					err = errors.New("first activity prompt did not offer the real write tool")
					break
				}
				arguments, _ := json.Marshal(map[string]string{"path": piResultsFile, "content": piResultsFileContent})
				delta["tool_calls"] = []any{map[string]any{
					"index": 0, "id": piResultsCallID, "type": "function",
					"function": map[string]string{"name": "write", "arguments": string(arguments)},
				}}
				finish = "tool_calls"
			case 2:
				var acknowledgement string
				if last.Role != "tool" || last.ToolCallID != piResultsCallID || json.Unmarshal(last.Content, &acknowledgement) != nil || acknowledgement != piResultsWriteAck {
					err = fmt.Errorf("model did not receive the correlated write acknowledgement: role=%s call=%q text=%q", last.Role, last.ToolCallID, acknowledgement)
					break
				}
				delta["content"] = piResultsFirstResponse
			case 3:
				if last.Role != "user" {
					err = errors.New("continuation did not end in the new activity prompt")
					break
				}
				delta["content"] = piResultsNextResponse
			}
		}
	}
	if err != nil {
		p.problems = append(p.problems, err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, choice := range []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}, {"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
		chunk, _ := json.Marshal(map[string]any{"id": "activity-results-loopback", "object": "chat.completion.chunk", "created": 1, "model": "nagi-product-fixture", "choices": []any{choice}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestPiAppContextAndResults(t *testing.T) {
	inputs := make(map[string]string)
	paths := make([]string, 0, 2)
	for _, name := range []string{"NAGI_TEST_NODE", "NAGI_TEST_WORKER"} {
		path := os.Getenv(name)
		if !filepath.IsAbs(path) {
			t.Fatalf("%s must identify a pinned absolute file", name)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s must identify a regular file: %v", name, err)
		}
		paths = append(paths, resolved)
	}
	payload := filepath.Dir(filepath.Dir(paths[0]))
	if paths[0] != filepath.Join(payload, "bin", "node") || paths[1] != filepath.Join(payload, "dist", "worker.js") {
		t.Fatal("pinned Node and worker are not from the same payload")
	}
	for _, path := range append(paths, "pi_app_integration_test.go", "pi_activity.go", "pi_activity_results.go", "issue_results_app.go", "issue_results.go", "workspace.go") {
		digest, err := piAppFileHash(path)
		if err != nil {
			t.Fatal(err)
		}
		inputs[path] = digest
	}
	piAppRecord(t, "context-results-source-inputs", inputs)
	evidence := piResultsEvidenceDir(t)
	root := t.TempDir()
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", "")
	isolateConnectionApp(t, root, payload)
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	first := &piFirstUseHarness{t: t, app: app, provider: &piAppProvider{limit: piResultsRequestCount}}
	first.pump = newUIPump(t, app)
	first.tester = ui.NewTester(app.view, 1040, 720)
	first.tester.SetLocale("en")
	first.tester.SetFocused(true)
	server := httptest.NewServer(piResultsProvider{first.provider})
	first.servers = append(first.servers, server)
	t.Cleanup(func() {
		first.provider.mu.Lock()
		defer first.provider.mu.Unlock()
		piAppRecord(t, "context-results-loopback", map[string]any{"requests": first.provider.requests, "bodies": first.provider.bodies, "problems": first.provider.problems, "request_limit": piResultsRequestCount})
		if !t.Failed() && first.provider.requests != piResultsRequestCount {
			t.Errorf("final model requests = %d, want %d", first.provider.requests, piResultsRequestCount)
		}
	})
	t.Cleanup(first.cleanup)
	h := &piActivityHarness{piFirstUseHarness: first, evidence: evidence}
	h.onUI(func() error {
		h.contactID = app.selectedContact().ID
		app.chooseFolder = func(mygo.OpenDialogOptions) ([]string, error) { return []string{project}, nil }
		return nil
	})
	h.input("API endpoint", server.URL+"/v1")
	h.input("API key", "nagi-product-loopback")
	h.input("Default model", "nagi-product-fixture")
	h.activate("Capacity & execution limits")
	h.input("Context tokens", "65536")
	h.input("Output tokens", "4096")
	h.click("Choose folder…")
	h.wait(func() bool { return !app.connectionForm.Busy })
	h.click("Save configuration")
	h.wait(func() bool { return !app.connectionForm.Busy && !app.connectionSaveInFlight })
	h.click("Apply saved")
	h.wait(func() bool { return !app.connectionApplying })
	ready := piResultsReady(h, "initial")
	h.click("← Back to workbench")
	h.click("Work")
	issueID := h.createIssue(piResultsGoal)
	h.selectIssue(piResultsGoal, issueID)
	msgs := locales.In("en")
	firstRun := piResultsRun(h, issueID, msgs.WorkStart(), piResultsFirstContext, 2)
	nextRun := piResultsRun(h, issueID, msgs.WorkContinue(), piResultsSecondContext, piResultsRequestCount)
	if firstRun.ID == nextRun.ID || firstRun.PiRun.RunID == nextRun.PiRun.RunID || firstRun.PiRun.ContextID != nextRun.PiRun.ContextID || firstRun.SessionID != nextRun.SessionID || firstRun.PiRun.WorkerEpoch != ready.WorkerEpoch || nextRun.PiRun.WorkerEpoch != ready.WorkerEpoch {
		t.Fatalf("runs do not identify distinct attempts in the same activity: first=%+v next=%+v", firstRun.PiRun, nextRun.PiRun)
	}
	filePath := filepath.Join(project, piResultsFile)
	if data, err := os.ReadFile(filePath); err != nil || string(data) != piResultsFileContent {
		t.Fatalf("real SDK write at %s = %q, %v", filePath, data, err)
	}
	if _, err := os.Stat(filepath.Join(project, piResultsUnwrittenFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("assistant-only file reference unexpectedly exists: %v", err)
	}
	h.screenshot("activity-result-issue-en-light")
	piResultsOpen(h, msgs.WorkReadResult(), issueID, nextRun, 2, piResultsNextResponse, "")
	h.screenshot("activity-result-latest-en-light")
	h.activate(msgs.ResultCloseResults())
	piResultsHistory(h, issueID)
	piResultsOpen(h, msgs.IssueOpenResultRun("1"), issueID, firstRun, 1, piResultsFirstResponse, filePath)
	h.screenshot("activity-result-first-en-light")
	piResultsReload(h, issueID, firstRun, filePath)
	piResultsReadFile(h, msgs, filePath)
	h.screenshot("activity-result-file-en-light")
	h.activate(msgs.ResultCloseReading())
	const narrowWidth, narrowHeight = 720, 640
	h.onUI(func() error {
		app.windowWidth = narrowWidth
		h.tester.SetSize(narrowWidth, narrowHeight)
		h.tester.SetDark(true)
		h.tester.SetLocale("zh-Hans")
		return nil
	})
	zh := locales.In("zh-Hans")
	piResultsCopy(h, zh.CopyResultLabel(), piResultsFirstResponse)
	h.screenshot("activity-result-first-zh-dark-narrow")
	piResultsReadFile(h, zh, filePath)
	h.screenshot("activity-result-file-zh-dark-narrow")
	h.activate(zh.ResultCloseReading())
	h.activate(zh.ResultCloseResults())
	latestLabel := zh.WorkReadResult()
	h.onUI(func() error {
		tr := h.tester
		tr.Frame()
		ct := app.selectedContact()
		if app.mainPageActive() || !app.detailPageActive() || !app.detailIs(detailWork) || ct.ID != h.contactID || ct.issueView.DetailID != issueID || ct.issueView.ShowConversationWork {
			return errors.New("closing the result did not return to the narrow issue detail")
		}
		tabs, found := tr.Find(zh.WorkTabs())
		if !found || tabs.H <= 0 {
			return errors.New("narrow issue detail tabs are not visible")
		}
		fullyVisible := func() bool {
			r, found := tr.Find(latestLabel)
			return found && r.W > 0 && r.H > 0 && r.X > 12 && r.X+r.W < narrowWidth-12 &&
				r.Y > tabs.Y+tabs.H+12 && r.Y+r.H < narrowHeight
		}
		if fullyVisible() {
			return nil
		}
		tr.Scroll(narrowWidth-60, 500, 0, -ct.issueView.DetailScroll.Y)
		for range 24 {
			tr.Frame()
			if fullyVisible() {
				return nil
			}
			tr.Scroll(narrowWidth-60, 500, 0, 120)
		}
		return fmt.Errorf("%q is not fully visible in the narrow issue detail", latestLabel)
	})
	h.screenshot("activity-result-issue-zh-dark-narrow")
	h.onUI(func() error { return h.tester.Click(latestLabel) })
	h.wait(func() bool { return !app.issueResult.Loading && app.resultInFlight == 0 })
	h.onUI(func() error {
		page := &app.issueResult
		if !page.Open || !page.ReturnLatest || page.OwnerContactID != h.contactID || page.IssueID != issueID || page.ExecutionID != nextRun.ID || page.SessionID != nextRun.SessionID || page.TaskID != nextRun.TaskID || page.Panel.RunNumber != 2 || page.Panel.Response != piResultsNextResponse || page.LoadError != "" {
			return fmt.Errorf("%q did not load the latest execution %s: got issue=%s execution=%s task=%s error=%s %s", latestLabel, nextRun.ID, page.IssueID, page.ExecutionID, page.TaskID, page.LoadError, page.LoadErrorDetail)
		}
		return nil
	})
	piResultsRequestBudget(h, piResultsRequestCount)
	if err := h.shutdown(); err != nil {
		t.Fatalf("initial app did not drain and reap its worker: %v", err)
	}
	piResultsReopen(t, first.provider, evidence, h.contactID, issueID, ready.WorkerEpoch, firstRun, nextRun, filePath)
}

func piResultsEvidenceDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("NAGI_ACTIVITY_EVIDENCE_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("NAGI_ACTIVITY_EVIDENCE_DIR must be an existing absolute directory outside source")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() || resolved == root || strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		t.Fatalf("invalid activity result evidence directory %q: %v", resolved, err)
	}
	return resolved
}

func piResultsReady(h *piActivityHarness, phase string) pi.Ready {
	h.t.Helper()
	var host *piAppHost
	h.onUI(func() error {
		host = h.app.pi
		if host != nil && host.client != nil {
			h.hosts = append(h.hosts, host)
		}
		if host == nil || host.client == nil || h.app.runtimeStatus != runtimeStateReady {
			return fmt.Errorf("%s worker did not become ready: %s", phase, h.app.runtimeErr)
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ready, err := host.client.Ready(ctx)
	if err != nil || ready.Engine.Version != "1.5.2" || ready.Runtime.Version != "v22.23.5" || ready.WorkerEpoch == "" {
		h.t.Fatalf("%s pinned worker readiness: %+v, %v", phase, ready, err)
	}
	piAppRecord(h.t, "context-results-ready-"+phase, ready)
	return ready
}

func piResultsRun(h *piActivityHarness, issueID, label, text string, requests int) *issueExecution {
	h.t.Helper()
	var snapshot taskContextItem
	var before int
	var requirements []issueRequirement
	h.onUI(func() error {
		issue := h.app.contactByID(h.contactID).issueByID(issueID)
		before = len(issue.Executions)
		for _, requirement := range issue.Requirements {
			requirements = append(requirements, *requirement)
		}
		if len(requirements) == 0 {
			return errors.New("activity has no requirement to preserve")
		}
		var err error
		snapshot, err = issue.Context.AddSnippet(piResultsContextName, text)
		if err != nil {
			return err
		}
		return h.app.persist()
	})
	h.startIssue(label, issueID)
	h.wait(func() bool {
		issue := h.app.contactByID(h.contactID).issueByID(issueID)
		return len(issue.Executions) == before+1 && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle
	})
	var execution *issueExecution
	h.onUI(func() error {
		issue := h.app.contactByID(h.contactID).issueByID(issueID)
		execution = latestIssueExecution(issue)
		if execution.Status != issueExecutionCompleted || execution.PiRun == nil || !execution.PiRun.Admitted || execution.PiRun.RunID == "" || execution.PiRun.ContextID == "" || execution.SessionID == "" || execution.TaskID != execution.PiRun.RunID {
			return fmt.Errorf("activity did not settle with an admitted, correlated Pi run: %+v", execution)
		}
		if !reflect.DeepEqual(execution.Context, []taskContextItem{snapshot}) || issue.Context.count() != 0 {
			return fmt.Errorf("saved context did not move to the admitted attempt: %+v", execution.Context)
		}
		var current []issueRequirement
		for _, requirement := range issue.Requirements {
			current = append(current, *requirement)
		}
		if issue.Status != issueStatusInProgress || !issueHasRemainingRequirements(issue) || !reflect.DeepEqual(current, requirements) {
			return fmt.Errorf("worker settlement changed business completion or requirements: %s, %+v", issue.Status, current)
		}
		piAppRecord(h.t, "context-results-run", map[string]any{"issue": issueID, "execution": execution.ID, "run": execution.PiRun, "saved_context": execution.Context, "issue_status": issue.Status, "requirements": requirements})
		return nil
	})
	piResultsRequestBudget(h, requests)
	return execution
}

func piResultsRequestBudget(h *piActivityHarness, expected int) {
	h.t.Helper()
	provider := h.piFirstUseHarness.provider
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.requests != expected || len(provider.problems) != 0 {
		h.t.Fatalf("loopback calls = %d, want %d, errors = %v", provider.requests, expected, provider.problems)
	}
}

func piResultsHistory(h *piActivityHarness, issueID string) {
	h.t.Helper()
	var expanded bool
	h.onUI(func() error {
		expanded = h.app.issueDetailOpen[issueID]
		return nil
	})
	if !expanded {
		h.activateActivity(locales.In("en").IssueMoreActions(), func() error {
			if !h.app.issueDetailOpen[issueID] {
				return errors.New("issue history was not expanded")
			}
			return nil
		})
	}
}

func piResultsOpen(h *piActivityHarness, label, issueID string, execution *issueExecution, number int, response, filePath string) {
	h.t.Helper()
	h.activateActivity(label, func() error {
		page := &h.app.issueResult
		if !page.Open || page.ExecutionID != execution.ID {
			return fmt.Errorf("%q opened the wrong execution: %s", label, page.ExecutionID)
		}
		return nil
	})
	piResultsCheck(h, issueID, execution, number, response, filePath)
	piResultsCopy(h, locales.In("en").CopyResultLabel(), response)
}

func piResultsCheck(h *piActivityHarness, issueID string, execution *issueExecution, number int, response, filePath string) {
	h.t.Helper()
	h.wait(func() bool { return !h.app.issueResult.Loading && h.app.resultInFlight == 0 })
	h.onUI(func() error {
		page := &h.app.issueResult
		panel := &page.Panel
		if !page.Open || page.LoadError != "" || page.OwnerContactID != h.contactID || page.IssueID != issueID || page.ExecutionID != execution.ID || page.SessionID != execution.SessionID || page.TaskID != execution.TaskID || panel.RunNumber != number {
			return fmt.Errorf("incorrect loaded result owner for run %d: issue=%s execution=%s session=%s task=%s error=%s %s", number, page.IssueID, page.ExecutionID, page.SessionID, page.TaskID, page.LoadError, page.LoadErrorDetail)
		}
		h.tester.Frame()
		label := locales.In("en").ResultRunLabel(fmt.Sprint(number), piResultsGoal)
		if _, found := h.tester.Find(label); !found || panel.Response != response {
			return fmt.Errorf("run %d label visible=%v response=%q, want %q", number, found, panel.Response, response)
		}
		if filePath == "" {
			if len(panel.Files) != 0 {
				return fmt.Errorf("a different run's file leaked into run %d: %+v", number, panel.Files)
			}
		} else {
			if len(panel.Files) != 1 {
				return fmt.Errorf("want exactly one write receipt, got %+v", panel.Files)
			}
			file := panel.Files[0]
			if file.Path != filePath || file.WorkingDirectory != filepath.Dir(filePath) || file.ToolName != "write" || file.CallID != piResultsCallID || file.MessageID == "" || file.ExecutionID != execution.ID || file.SessionID != execution.SessionID || file.TaskID != execution.TaskID {
				return fmt.Errorf("uncorrelated file receipt: %+v", file)
			}
			piAppRecord(h.t, "context-results-file-receipt", file)
		}
		return nil
	})
}

func piResultsCopy(h *piActivityHarness, label, expected string) {
	h.t.Helper()
	h.onUI(func() error {
		h.tester.SetClipboard("clipboard sentinel")
		return nil
	})
	h.activateActivity(label, func() error {
		if got := h.tester.Clipboard(); got != expected {
			return fmt.Errorf("%q copied %q, want exact bytes %q", label, got, expected)
		}
		piAppRecord(h.t, "context-results-clipboard", map[string]string{"control": label, "text": expected})
		return nil
	})
}

func piResultsReload(h *piActivityHarness, issueID string, execution *issueExecution, filePath string) {
	h.t.Helper()
	var generation int
	h.onUI(func() error {
		generation = h.app.issueResult.Generation
		return nil
	})
	h.activateActivity(locales.In("en").ResultReloadResult(), func() error {
		if h.app.issueResult.Generation <= generation {
			return errors.New("Reload did not start a new history read")
		}
		return nil
	})
	piResultsCheck(h, issueID, execution, 1, piResultsFirstResponse, filePath)
	piResultsCopy(h, locales.In("en").CopyResultLabel(), piResultsFirstResponse)
}

func piResultsReadFile(h *piActivityHarness, msgs locales.Messages, filePath string) {
	h.t.Helper()
	var key string
	h.onUI(func() error {
		panel := &h.app.issueResult.Panel
		if len(panel.Files) != 1 || panel.Files[0].Path != filePath {
			return fmt.Errorf("verified file is absent from the run result: %+v", panel.Files)
		}
		key = issueResultFileKey(panel.Files[0])
		return nil
	})
	h.activateActivity(msgs.ResultFileRowLabel("1", filepath.Base(filePath)), func() error {
		if h.app.issueResult.Panel.ReaderKey != key {
			return errors.New("file row did not open its own reader")
		}
		return nil
	})
	h.wait(func() bool { return !h.app.issueResult.Panel.Checking && h.app.resultInFlight == 0 })
	h.onUI(func() error {
		panel := &h.app.issueResult.Panel
		inspection := panel.Inspection
		if panel.InspectFor != key || inspection == nil || inspection.Path != filePath || !inspection.Exists || !inspection.Regular || inspection.Preview != piResultsFileContent || inspection.NoticeKind != "" || inspection.ReadErrorKind != "" {
			return fmt.Errorf("file reader did not inspect exact current content: %+v", inspection)
		}
		piAppRecord(h.t, "context-results-current-file", inspection)
		return nil
	})
	piResultsCopy(h, msgs.CopyCurrentFileLabel(), piResultsFileContent)
}

func piResultsReopen(t *testing.T, provider *piAppProvider, evidence, contactID, issueID, oldEpoch string, firstRun, nextRun *issueExecution, filePath string) {
	t.Helper()
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	first := &piFirstUseHarness{t: t, app: app, provider: provider}
	first.pump = newUIPump(t, app)
	first.tester = ui.NewTester(app.view, 1040, 720)
	first.tester.SetLocale("en")
	first.tester.SetFocused(true)
	t.Cleanup(first.cleanup)
	h := &piActivityHarness{piFirstUseHarness: first, evidence: evidence, contactID: contactID}
	h.onUI(func() error {
		ct := app.contactByID(contactID)
		if ct == nil || ct != app.selectedContact() || app.connectionSaved == nil {
			return errors.New("reopen did not recover the captured contact and saved connection from the same profile")
		}
		issue := ct.issueByID(issueID)
		if issue == nil || issue.Status != issueStatusInProgress || !issueHasRemainingRequirements(issue) || len(issue.Executions) != 2 {
			return errors.New("reopen did not recover the open issue with two settled runs")
		}
		for index, old := range []*issueExecution{firstRun, nextRun} {
			restored := issue.Executions[index]
			if restored.ID != old.ID || restored.Status != issueExecutionCompleted || restored.SessionID != old.SessionID || restored.TaskID != old.TaskID || !reflect.DeepEqual(restored.PiRun, old.PiRun) || !reflect.DeepEqual(restored.Context, old.Context) {
				return fmt.Errorf("reopen changed execution %s or its saved context: %+v", old.ID, restored)
			}
		}
		app.startPiHost()
		return nil
	})
	h.wait(func() bool { return !app.connectionApplying && app.contactHistoryInFlight == 0 })
	ready := piResultsReady(h, "reopened")
	if ready.WorkerEpoch == oldEpoch {
		t.Fatal("reopened app reused the first worker epoch")
	}
	msgs := locales.In("en")
	var workspaceOpen, detailOpen bool
	h.onUI(func() error {
		workspaceOpen = app.detailIs(detailWork)
		detailOpen = app.selectedContact().issueView.DetailID == issueID
		return nil
	})
	if !workspaceOpen {
		h.click("Work")
	}
	if !detailOpen {
		h.selectIssue(piResultsGoal, issueID)
	}
	piResultsHistory(h, issueID)
	piResultsOpen(h, msgs.IssueOpenResultRun("1"), issueID, firstRun, 1, piResultsFirstResponse, filePath)
	piResultsReadFile(h, msgs, filePath)
	h.screenshot("activity-result-file-reopened-en-light")
	piResultsRequestBudget(h, piResultsRequestCount)
	if err := h.shutdown(); err != nil {
		t.Fatalf("reopened app did not drain and reap its worker: %v", err)
	}
	piResultsRequestBudget(h, piResultsRequestCount)
}
