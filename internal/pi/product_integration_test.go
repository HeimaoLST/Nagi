//go:build integration

package pi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

const (
	productCedar  = "contact-cedar"
	productCopper = "contact-copper"
	productSource = "qa-process-connection"
	productKey    = "nagi-loopback-only"
	productModel  = "nagi-local-integration"
)

type productProvider struct {
	mu       sync.Mutex
	requests []string
	problems []string
	arrivals chan string
}

func (p *productProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
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
	var kind, problem string
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+productKey {
		problem = "unexpected local endpoint or authorization"
	} else if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Stream || body.Model != productModel || len(body.Messages) == 0 {
		problem = "expected a streamed Chat Completions request for the fixture model"
	} else {
		last := body.Messages[len(body.Messages)-1]
		if last.Role == "tool" {
			kind = "answer-cedar"
			if last.ToolCallID != "qa-question-cedar" || !strings.Contains(string(last.Content), "CEDAR_ANSWER") || !strings.Contains(string(last.Content), "captured answer material") {
				problem = "provider did not receive the exact Cedar tool answer"
			}
		} else if last.Role == "user" {
			for _, marker := range []string{"SEED_HISTORY", "ASK_CEDAR", "ASK_COPPER", "CHAT_WHILE_WAITING", "HOLD_SEND"} {
				if strings.Contains(string(last.Content), marker) {
					kind = marker
					break
				}
			}
		}
		if kind == "ASK_CEDAR" || kind == "ASK_COPPER" {
			waitOffered := false
			for _, tool := range body.Tools {
				waitOffered = waitOffered || tool.Function.Name == "issue_wait"
			}
			if !waitOffered {
				problem = "activity execution did not offer the product question tool"
			}
		}
		if kind == "" {
			problem = "unexpected prompt or tool delivery"
		}
	}
	p.mu.Lock()
	p.requests = append(p.requests, kind)
	if problem != "" {
		p.problems = append(p.problems, problem)
	}
	p.mu.Unlock()
	if problem != "" {
		http.Error(w, problem, http.StatusBadRequest)
		return
	}
	switch kind {
	case "SEED_HISTORY":
		piChatResponse(w, "SEEDED_REPLY", "", "", "")
	case "ASK_CEDAR":
		piChatResponse(w, "", "issue_wait", "qa-question-cedar", `{"text":"Which cedar should I use?"}`)
	case "ASK_COPPER":
		piChatResponse(w, "", "issue_wait", "qa-question-copper", `{"text":"Which copper should I use?"}`)
	case "HOLD_SEND":
		select {
		case p.arrivals <- kind:
		case <-r.Context().Done():
		}
		<-r.Context().Done()
	case "CHAT_WHILE_WAITING":
		piChatResponse(w, "CHAT_OK", "", "", "")
	case "answer-cedar":
		piChatResponse(w, "CEDAR_RESULT", "", "", "")
	}
}

func (p *productProvider) await(t *testing.T, ctx context.Context, want string) {
	t.Helper()
	select {
	case got := <-p.arrivals:
		if got != want {
			t.Fatalf("provider held %q, want %q", got, want)
		}
	case <-ctx.Done():
		t.Fatalf("provider did not reach %s: %v", want, ctx.Err())
	}
}

func (p *productProvider) check(t *testing.T, want ...string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !reflect.DeepEqual(p.requests, want) || len(p.problems) != 0 {
		t.Fatalf("provider requests=%v want=%v problems=%v", p.requests, want, p.problems)
	}
	t.Logf("provider request sequence %v", p.requests)
}

type productFixture struct {
	options             pi.ProductOptions
	provider            *productProvider
	endpoint, workspace string
}

func newProductFixture(t *testing.T) productFixture {
	t.Helper()
	provider := &productProvider{arrivals: make(chan string, 8)}
	server := httptest.NewServer(provider)
	t.Cleanup(server.Close)
	root := t.TempDir()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture := productFixture{
		provider: provider, endpoint: server.URL, workspace: workspace,
		options: pi.ProductOptions{
			NodePath: os.Getenv("NAGI_TEST_NODE"), WorkerPath: os.Getenv("NAGI_TEST_WORKER"),
			StateDir: filepath.Join(root, "pi"), ProfilePath: filepath.Join(root, "contacts.json"), CredentialsPath: filepath.Join(root, "connection.json"),
		},
	}
	for _, path := range []string{fixture.options.NodePath, fixture.options.WorkerPath} {
		if info, err := os.Stat(path); err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() {
			t.Fatalf("NAGI_TEST_NODE and NAGI_TEST_WORKER must name pinned regular absolute inputs: %q: %v", path, err)
		}
	}
	credential := map[string]string{"api": "openai-chat-completions", "endpoint": fixture.endpoint + "/v1", "api_key": productKey}
	writeProductFixture(t, fixture.options.CredentialsPath, map[string]any{
		"version": 2, "credential_source": productSource, "model": productModel, "context_tokens": 65536, "output_tokens": 4096,
		"working_directory": workspace, "max_active_tasks": 3, "sources": map[string]any{productSource: credential},
	})
	return fixture
}

func writeProductFixture(t *testing.T, path string, input any) []byte {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func productRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type productProcess struct {
	client   *pi.ProductClient
	events   chan pi.ProductEvent
	problems chan error
	exits    chan pi.ProcessOutcome
	streamID string
	seq      uint64
	closed   bool
}

func startProductProcess(t *testing.T, options pi.ProductOptions) *productProcess {
	t.Helper()
	process := &productProcess{events: make(chan pi.ProductEvent, 128), problems: make(chan error, 8), exits: make(chan pi.ProcessOutcome, 1)}
	options.Handlers = pi.ProductHandlers{
		OnEvent:       func(event pi.ProductEvent) { process.events <- event },
		OnProtocolErr: func(err error) { process.problems <- err },
		OnStreamGap:   func() { process.problems <- pi.ErrProductStreamGap },
		OnExit:        func(outcome pi.ProcessOutcome) { process.exits <- outcome },
	}
	client, err := pi.StartProduct(options)
	if err != nil {
		t.Fatal(err)
	}
	process.client = client
	t.Cleanup(func() {
		if !process.closed {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			outcome, err := client.Kill(ctx)
			if !outcome.Reaped {
				t.Errorf("cleanup could not reap product worker: %+v: %v", outcome, err)
			}
		}
	})
	return process
}

func (p *productProcess) open(t *testing.T, ctx context.Context) pi.ProductOpenResult {
	t.Helper()
	ready, err := p.client.Ready(ctx)
	if err != nil || ready.Engine.Version != "1.5.2" || ready.Runtime.Version != "v22.23.5" || ready.WorkerEpoch == "" {
		t.Fatalf("actual product worker ready=%+v error=%v", ready, err)
	}
	open, err := p.client.Open(ctx, []int{pi.ProductVersion})
	if err != nil || open.Version != 1 || open.StreamID == "" || open.StreamID == p.streamID {
		t.Fatalf("product startup snapshot=%+v error=%v", open, err)
	}
	p.streamID, p.seq = open.StreamID, open.EventSeq
	return open
}

func (p *productProcess) exit(t *testing.T, ctx context.Context, outcome pi.ProcessOutcome, success bool, err error) {
	t.Helper()
	if !outcome.Reaped || outcome.PID <= 0 || success != (outcome.ExitCode == 0 && err == nil) {
		t.Fatalf("product process exit=%+v success=%v err=%v", outcome, success, err)
	}
	select {
	case <-p.client.CallbacksDone():
	case <-ctx.Done():
		t.Fatal("product process callbacks did not finish")
	}
	select {
	case observed := <-p.exits:
		if !observed.Reaped || observed.PID != outcome.PID || observed.ExitCode != outcome.ExitCode {
			t.Fatalf("product exit callback=%+v wait=%+v", observed, outcome)
		}
	default:
		t.Fatal("product worker did not publish its exit callback")
	}
	p.closed = true
	t.Logf("product worker reaped pid=%d status=%d", outcome.PID, outcome.ExitCode)
}

func (p *productProcess) shutdown(t *testing.T, ctx context.Context) {
	t.Helper()
	outcome, err := p.client.Shutdown(ctx)
	p.exit(t, ctx, outcome, true, err)
	select {
	case err := <-p.problems:
		t.Fatalf("product stream: %v", err)
	default:
	}
}

func (p *productProcess) event(t *testing.T, ctx context.Context) pi.ProductEvent {
	t.Helper()
	select {
	case failure := <-p.problems:
		t.Fatalf("product stream: %v", failure)
	case event := <-p.events:
		if event.StreamID != p.streamID || event.Seq != p.seq+1 {
			t.Fatalf("event stream=%q seq=%d want=%q/%d", event.StreamID, event.Seq, p.streamID, p.seq+1)
		}
		p.seq = event.Seq
		return event
	case <-ctx.Done():
		t.Fatalf("waiting for product event after sequence %d: %v", p.seq, ctx.Err())
	}
	return pi.ProductEvent{}
}

func productResult[T any](t *testing.T, result T, err error) T {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func productBinding(f productFixture, context pi.ContextBinding) map[string]any {
	return map[string]any{
		"engine": "pi", "context": context,
		"selection": map[string]any{"cwd": context.CWD, "instructions": "Keep the source text", "coding_tools": []string{}},
		"configuration": map[string]any{"wire_api": pi.OpenAICompletions, "base_url": f.endpoint + "/v1", "model": productModel,
			"context_tokens": 65536, "output_tokens": 4096, "credential_source": productSource},
	}
}

func productContacts(f productFixture) []map[string]any {
	return []map[string]any{
		{"id": productCedar, "name": "Cedar", "role": "Reviewer", "purpose": "Inventory", "instructions": "Keep the source text", "workspace": f.workspace, "builtin_tools": []string{}, "draft": "cedar draft 你好"},
		{"id": productCopper, "name": "Copper", "role": "Writer", "workspace": f.workspace, "draft": "copper draft"},
	}
}

func productConversation(t *testing.T, p *productProcess, ctx context.Context, id string) pi.ProductConversation {
	t.Helper()
	conversation, err := p.client.GetConversation(ctx, id)
	return productResult(t, conversation, err)
}

func productSave(t *testing.T, p *productProcess, ctx context.Context, id, operation string, revision uint64, text string, materials []pi.ProductMaterial) pi.ProductDraft {
	t.Helper()
	draft, err := p.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: id}, ExpectedRevision: revision, OperationID: operation,
		ProductText: pi.ProductText{Text: text, Materials: materials},
	})
	return productResult(t, draft, err)
}

func productExecution(t *testing.T, receipt pi.ProductReceipt, operation string, target pi.ProductReceiptTarget) string {
	t.Helper()
	if receipt.OperationID != operation || receipt.State != "accepted" || receipt.Target != target || receipt.Execution == nil || receipt.Execution.ExecutionID == "" {
		t.Fatalf("invalid acceptance for %q and %+v: %+v", operation, target, receipt)
	}
	return receipt.Execution.ExecutionID
}

func (p *productProcess) executionEvent(t *testing.T, ctx context.Context, eventName string, owner pi.ProductReceiptTarget, operation, execution string) pi.ProductExecutionEvent {
	t.Helper()
	for {
		event := p.event(t, ctx)
		if event.Name != eventName {
			continue
		}
		var data pi.ProductExecutionEvent
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.ExecutionID == execution {
			if data.ConversationID != owner.ConversationID || data.ActivityID != owner.ActivityID || data.OperationID != operation || data.Revision == 0 {
				t.Fatalf("execution event correlation mismatch: %+v", data)
			}
			return data
		}
	}
}

func TestPiProductProcessAdmitsCurrentProfileAndRestoresDrafts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := newProductFixture(t)
	seed := startPiProcess(t, ctx, f.options.StateDir, f.endpoint)
	openContext := func(id string) pi.ContextBinding {
		params := pi.OpenContextParams{ContextID: id, BotID: productCedar, Purpose: pi.Conversation, SelectionRevision: 2, CWD: f.workspace, Instructions: "Keep the source text", CodingTools: []string{}}
		binding, err := seed.client.OpenContext(ctx, params)
		seed.contexts = append(seed.contexts, id)
		return productResult(t, binding, err)
	}
	current := openContext("cedar-current")
	previous := openContext("cedar-previous")
	const original = "SEED_HISTORY original source text 你好"
	_, err := seed.client.StartRun(ctx, pi.StartRunParams{ContextID: previous.ContextID, RunID: "seed-history-run", Text: original, Correlation: &pi.RunCorrelation{TurnID: "seed-history-turn"}})
	if err != nil {
		t.Fatal(err)
	}
	piSettled(t, ctx, seed, previous.ContextID, "seed-history-run", pi.Completed, "SEEDED_REPLY")
	seedHistory := piPublicHistory(t, ctx, seed.client, previous.ContextID)
	previous.Used = true
	seed.shutdown(t, ctx)
	f.provider.check(t, "SEED_HISTORY")

	materialPath := filepath.Join(f.workspace, "notes.txt")
	if err := os.WriteFile(materialPath, []byte("captured file text"), 0600); err != nil {
		t.Fatal(err)
	}
	cedarMaterial := pi.ProductMaterial{ID: "material-cedar", Revision: 2, Name: "notes.txt", Text: "captured file text", Source: materialPath}
	copperMaterial := pi.ProductMaterial{ID: "material-copper", Revision: 3, Name: "copper notes", Text: "Copper's own materials"}
	contacts := productContacts(f)
	contacts[0]["session_id"] = current.SessionRef
	contacts[0]["pi_binding"] = productBinding(f, current)
	contacts[0]["previous_pi_bindings"] = []any{productBinding(f, previous)}
	contacts[0]["task_context"] = []pi.ProductMaterial{cedarMaterial}
	contacts[0]["pending_send"] = map[string]any{"draft": "old submitted text", "context": []pi.ProductMaterial{cedarMaterial}, "state": "unknown", "tool_scope_ref": "legacy-scope"}
	contacts[1]["task_context"] = []pi.ProductMaterial{copperMaterial}
	profile := map[string]any{
		"version": 10, "selected": productCopper, "appearance": "dark", "contacts": contacts,
		"issue_tool_scopes": []any{map[string]any{"ref": "legacy-scope", "contact_id": productCedar, "runtime_session_id": current.SessionRef, "turn_id": "legacy-send", "runtime_task_id": "legacy-run", "created_at": 1}},
		"issues": []any{map[string]any{"id": "legacy-activity", "contact_id": productCedar, "goal": "Recover prior findings", "status": "completed", "requirements_revision": 4,
			"requirement_items": []any{map[string]any{"id": "legacy-requirement", "text": "Retain the findings"}},
			"executions":        []any{map[string]any{"id": "legacy-activity-execution", "task_id": "legacy-activity-run", "status": "completed", "requirements_revision": 4}},
			"questions": []any{map[string]any{"ref": "legacy-question", "text": "Use the retained findings?", "state": "answered", "requirements_revision": 4,
				"source_task_id": "legacy-activity-run", "answer_text": "Yes", "answer_context": []pi.ProductMaterial{cedarMaterial}, "delivery_state": "resolved"}}}},
		"issue_tool_operations": map[string]any{"operations": []any{map[string]any{"id": "legacy-effect", "scope_ref": "legacy-scope", "turn_id": "legacy-send", "action": "add_supplement", "target_issue_id": "legacy-activity", "payload": "{}", "created_at": 1, "updated_at": 2,
			"receipt": map[string]any{"state": "saved", "issue_id": "legacy-activity", "execution_id": "legacy-activity-execution", "question_ref": "legacy-question"}}}},
	}
	writeProductFixture(t, f.options.ProfilePath, profile)
	credentials := productRead(t, f.options.CredentialsPath)
	process := startProductProcess(t, f.options)
	_, err = process.client.Open(ctx, []int{99})
	piRejected(t, err, "unsupported_version")
	opened := process.open(t, ctx)
	if opened.Snapshot.Preferences == nil || opened.Snapshot.Preferences.SelectedBotID != productCopper || opened.Snapshot.Preferences.Appearance != "dark" {
		t.Fatalf("imported UI preferences=%+v", opened.Snapshot.Preferences)
	}
	connection := opened.Snapshot.Connection
	if connection.SavedRevision == nil || *connection.SavedRevision != 1 || connection.EffectiveRevision == nil || *connection.EffectiveRevision != 1 || connection.Saved == nil ||
		connection.Saved.WireAPI != pi.OpenAICompletions || connection.Saved.BaseURL != f.endpoint+"/v1" || connection.Saved.Model != productModel ||
		!connection.Saved.CredentialPresent || connection.Saved.WorkingDirectory != f.workspace || connection.Saved.MaxActiveTasks != 3 {
		t.Fatalf("imported connection=%+v", connection)
	}
	encoded, err := json.Marshal(opened)
	if err != nil || bytes.Contains(encoded, []byte(productKey)) {
		t.Fatal("public startup snapshot exposed the private provider key")
	}
	page, err := process.client.ListBots(ctx, pi.ProductPageParams{})
	bots := productResult(t, page, err).Items
	if len(bots) != 2 || !reflect.DeepEqual(bots, opened.Snapshot.Bots) || bots[0].BotID != productCedar || bots[0].CurrentConversationID != current.ContextID ||
		bots[0].ProfileRevision != 2 || bots[0].Profile.CodingTools == nil || len(bots[0].Profile.CodingTools) != 0 || bots[0].Profile.Instructions != "Keep the source text" ||
		bots[1].BotID != productCopper || bots[1].Profile.CodingTools != nil || bots[1].CurrentConversationID == "" {
		t.Fatalf("imported Bot identities or profiles=%+v", bots)
	}
	copperID := bots[1].CurrentConversationID
	list, err := process.client.ListConversations(ctx, pi.ProductConversationListParams{BotID: productCedar})
	if items := productResult(t, list, err).Items; len(items) != 2 || items[0].ConversationID != current.ContextID || items[0].Mode != "current" || items[1].ConversationID != previous.ContextID || items[1].Mode != "readOnly" {
		t.Fatalf("imported conversations=%+v", items)
	}
	cedarDraft := pi.ProductDraft{Text: "cedar draft 你好", Materials: []pi.ProductMaterial{cedarMaterial}}
	copperDraft := pi.ProductDraft{Text: "copper draft", Materials: []pi.ProductMaterial{copperMaterial}}
	for _, check := range []struct {
		id    string
		draft *pi.ProductDraft
	}{{current.ContextID, &cedarDraft}, {previous.ContextID, nil}, {copperID, &copperDraft}} {
		got := productConversation(t, process, ctx, check.id)
		if got.ConversationID != check.id || !reflect.DeepEqual(got.Draft, check.draft) {
			t.Fatalf("imported conversation %q returned %+v", check.id, got)
		}
	}
	activity, err := process.client.GetActivity(ctx, "legacy-activity")
	activity = productResult(t, activity, err)
	if activity.OwnerBotID != productCedar || activity.OriginConversationID != current.ContextID || activity.State != "completed" || activity.Revision != 4 ||
		!reflect.DeepEqual(activity.Requirements, []string{"Retain the findings"}) || len(activity.Questions) != 1 || len(activity.Executions) != 1 ||
		activity.Questions[0].QuestionID != "legacy-question" || activity.Questions[0].ExecutionID != "legacy-activity-execution" || activity.Questions[0].Delivery != "delivered" ||
		!reflect.DeepEqual(activity.Questions[0].Answer, &pi.ProductText{Text: "Yes", Materials: []pi.ProductMaterial{cedarMaterial}}) {
		t.Fatalf("imported activity=%+v", activity)
	}
	for _, wanted := range []struct {
		op, state, run string
		target         pi.ProductReceiptTarget
	}{
		{"legacy-send", "unknown", "legacy-run", pi.ProductReceiptTarget{ConversationID: current.ContextID}},
		{"legacy-effect", "accepted", "legacy-activity-execution", pi.ProductReceiptTarget{ActivityID: "legacy-activity", QuestionID: "legacy-question"}},
	} {
		got, err := process.client.Operation(ctx, wanted.op)
		if err != nil || got.OperationID != wanted.op || got.State != wanted.state || got.Target != wanted.target || got.Execution == nil || got.Execution.ExecutionID != wanted.run {
			t.Fatalf("imported operation %s: %+v error=%v", wanted.op, got, err)
		}
	}
	params := pi.ProductHistoryParams{ProductConversationParams: pi.ProductConversationParams{ConversationID: previous.ContextID}}
	history, err := process.client.History(ctx, params)
	rows := productResult(t, history, err).Items
	if len(rows) != 2 || rows[0].Role != "user" || rows[0].Text != original || rows[1].Role != "assistant" || rows[1].Text != "SEEDED_REPLY" || rows[0].MessageID == rows[1].MessageID {
		t.Fatalf("imported worker history=%+v", rows)
	}
	for _, row := range rows {
		var source struct {
			Timestamp string `json:"timestamp"`
		}
		if err := json.Unmarshal(seedHistory[row.MessageID], &source); err != nil || source.Timestamp == "" || source.Timestamp != row.Timestamp {
			t.Fatalf("history entry lost its original worker ID or timestamp: %+v source=%+v err=%v", row, source, err)
		}
	}
	f.provider.check(t, "SEED_HISTORY")
	saved := productSave(t, process, ctx, current.ContextID, "draft-cedar-1", 0, "cedar edit", []pi.ProductMaterial{cedarMaterial})
	if !reflect.DeepEqual(saved, pi.ProductDraft{Revision: 1, Text: "cedar edit", Materials: []pi.ProductMaterial{cedarMaterial}}) {
		t.Fatalf("saved draft=%+v", saved)
	}
	_, err = process.client.SaveDraft(ctx, pi.ProductSaveDraftParams{ProductConversationParams: pi.ProductConversationParams{ConversationID: current.ContextID}, OperationID: "draft-stale", ExpectedRevision: 0, ProductText: pi.ProductText{Text: "stale edit"}})
	piRejected(t, err, "stale_revision")
	if err := os.WriteFile(materialPath, []byte("different file text"), 0600); err != nil {
		t.Fatal(err)
	}
	process.shutdown(t, ctx)
	reopened := startProductProcess(t, f.options)
	if again := reopened.open(t, ctx); again.StreamID == opened.StreamID || !reflect.DeepEqual(again.Snapshot.Bots, bots) {
		t.Fatalf("reopen changed Bot identities or reused the stream: %+v", again)
	}
	again, err := reopened.client.History(ctx, params)
	if !reflect.DeepEqual(productResult(t, again, err).Items, rows) ||
		!reflect.DeepEqual(productConversation(t, reopened, ctx, current.ContextID).Draft, &saved) ||
		!reflect.DeepEqual(productConversation(t, reopened, ctx, copperID).Draft, &copperDraft) ||
		!bytes.Equal(productRead(t, f.options.CredentialsPath), credentials) {
		t.Fatal("reopen changed history, draft snapshots, or private configuration")
	}
	reopened.shutdown(t, ctx)
	broken := f.options
	broken.ProfilePath = filepath.Join(t.TempDir(), "contacts.json")
	current.BotID = productCopper
	contacts[0]["pi_binding"] = productBinding(f, current)
	originalBroken := writeProductFixture(t, broken.ProfilePath, profile)
	rejected := startProductProcess(t, broken)
	if _, err := rejected.client.Ready(ctx); err == nil {
		t.Fatal("profile with cross-Bot binding passed admission")
	}
	outcome, err := rejected.client.Wait(ctx)
	rejected.exit(t, ctx, outcome, false, err)
	if !bytes.Equal(productRead(t, broken.ProfilePath), originalBroken) {
		t.Fatal("failed admission changed the input profile")
	}
	f.provider.check(t, "SEED_HISTORY")
}

func TestPiProductProcessTargetsConcurrentActivityQuestions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := newProductFixture(t)
	const cedar, copper = "activity-cedar", "activity-copper"
	writeProductFixture(t, f.options.ProfilePath, map[string]any{"version": 10, "contacts": productContacts(f), "issues": []any{
		map[string]any{"id": cedar, "contact_id": productCedar, "goal": "Cedar inventory", "status": "not_started", "requirements_revision": 1},
		map[string]any{"id": copper, "contact_id": productCedar, "goal": "Copper inventory", "status": "not_started", "requirements_revision": 1},
	}})
	process := startProductProcess(t, f.options)
	opened := process.open(t, ctx)
	conversationID := opened.Snapshot.Bots[0].CurrentConversationID
	get := func(activityID string) pi.ProductActivity {
		result, err := process.client.GetActivity(ctx, activityID)
		activity := productResult(t, result, err)
		if activity.ActivityID != activityID || activity.OwnerBotID != productCedar || activity.OriginConversationID != conversationID {
			t.Fatalf("activity ownership mismatch: %+v", activity)
		}
		return activity
	}
	start := func(activityID, operation, text string) string {
		got, err := process.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{ProductActivityParams: pi.ProductActivityParams{ActivityID: activityID},
			ExpectedRevision: get(activityID).Revision, OperationID: operation, ProductText: pi.ProductText{Text: text}})
		return productExecution(t, productResult(t, got, err), operation, pi.ProductReceiptTarget{ActivityID: activityID})
	}
	awaitQuestion := func(activityID, executionID, text string) pi.ProductQuestion {
		t.Helper()
		for {
			event := process.event(t, ctx)
			if event.Name != "activity.changed" {
				continue
			}
			var change pi.ProductObjectChanged
			if err := json.Unmarshal(event.Data, &change); err != nil {
				t.Fatal(err)
			}
			if change.ActivityID != activityID {
				continue
			}
			activity := get(activityID)
			if activity.State != "waitingForAnswer" {
				continue
			}
			if len(activity.Questions) != 1 {
				t.Fatalf("activity %s did not retain exactly one question: %+v", activityID, activity.Questions)
			}
			question := activity.Questions[0]
			if question.State != "open" || question.Delivery != "" || question.ExecutionID != executionID || question.Text != text || question.Revision == 0 {
				t.Fatalf("the question did not identify its owning execution: %+v", question)
			}
			return question
		}
	}
	f.provider.check(t)
	cedarRun := start(cedar, "continue-cedar", "ASK_CEDAR")
	question := awaitQuestion(cedar, cedarRun, "Which cedar should I use?")
	copperRun := start(copper, "continue-copper", "ASK_COPPER")
	copperQuestion := awaitQuestion(copper, copperRun, "Which copper should I use?")
	draft := productSave(t, process, ctx, conversationID, "save-waiting-chat", 0, "CHAT_WHILE_WAITING", nil)
	chatTarget := pi.ProductReceiptTarget{ConversationID: conversationID}
	chat, err := process.client.Send(ctx, pi.ProductSendParams{ProductConversationParams: pi.ProductConversationParams{ConversationID: conversationID}, OperationID: "send-waiting-chat", DraftRevision: draft.Revision})
	chatRun := productExecution(t, productResult(t, chat, err), "send-waiting-chat", chatTarget)
	if settled := process.executionEvent(t, ctx, "execution.settled", chatTarget, "send-waiting-chat", chatRun); settled.State != "completed" {
		t.Fatalf("chat did not complete while activities were held: %+v", settled)
	}
	chatPage, err := process.client.History(ctx, pi.ProductHistoryParams{ProductConversationParams: pi.ProductConversationParams{ConversationID: conversationID}})
	if items := productResult(t, chatPage, err).Items; len(items) != 2 || items[0].Text != "CHAT_WHILE_WAITING" || items[1].Text != "CHAT_OK" {
		t.Fatalf("independent chat history=%+v", items)
	}
	answer := pi.ProductAnswerParams{ProductActivityExecutionParams: pi.ProductActivityExecutionParams{ProductActivityParams: pi.ProductActivityParams{ActivityID: cedar}, ExecutionID: cedarRun},
		QuestionID: question.QuestionID, QuestionRevision: question.Revision - 1, OperationID: "answer-stale", ProductText: pi.ProductText{Text: "CEDAR_ANSWER",
			Materials: []pi.ProductMaterial{{ID: "cedar-answer-capture", Revision: 1, Name: "Answer evidence", Text: "captured answer material"}}}}
	_, err = process.client.AnswerQuestion(ctx, answer)
	piRejected(t, err, "stale_revision")
	stop := pi.ProductActivityStopParams{ProductActivityExecutionParams: pi.ProductActivityExecutionParams{ProductActivityParams: pi.ProductActivityParams{ActivityID: cedar}, ExecutionID: copperRun}, OperationID: "wrong-stop"}
	_, err = process.client.StopActivity(ctx, stop)
	piRejected(t, err, "not_found")
	if after := get(copper); len(after.Executions) != 1 || after.Executions[0].ExecutionID != copperRun || after.Executions[0].State != "running" {
		t.Fatalf("wrong-owner Stop changed the held execution: %+v", after.Executions)
	}
	if after := get(cedar); after.State != "waitingForAnswer" || len(after.Questions) != 1 || !reflect.DeepEqual(after.Questions[0], question) {
		t.Fatalf("refused commands changed the waiting activity: %+v", after)
	}
	answer.OperationID, answer.QuestionRevision = "answer-cedar", question.Revision
	answered, err := process.client.AnswerQuestion(ctx, answer)
	if err != nil || !answered.AnswerSaved || answered.ActivityID != cedar || answered.ExecutionID != cedarRun || answered.QuestionID != question.QuestionID || answered.Revision <= question.Revision {
		t.Fatalf("targeted answer receipt=%+v error=%v", answered, err)
	}
	stop.ActivityID, stop.OperationID = copper, "stop-copper"
	stopped, err := process.client.StopActivity(ctx, stop)
	if err != nil || !stopped.CancelRequested || stopped.ExecutionID != copperRun {
		t.Fatalf("targeted cancellation=%+v error=%v", stopped, err)
	}
	settlements := map[string]string{}
	wantRuns := map[string]struct{ activity, operation, state string }{
		cedarRun: {cedar, "continue-cedar", "completed"}, copperRun: {copper, "continue-copper", "interrupted"},
	}
	for len(settlements) < len(wantRuns) {
		event := process.event(t, ctx)
		if event.Name != "execution.settled" {
			continue
		}
		var data pi.ProductExecutionEvent
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatal(err)
		}
		want, ok := wantRuns[data.ExecutionID]
		if !ok || data.ActivityID != want.activity || data.OperationID != want.operation || data.State != want.state || data.Revision == 0 || settlements[data.ExecutionID] != "" {
			t.Fatalf("unexpected targeted settlement: %+v", data)
		}
		settlements[data.ExecutionID] = data.State
	}
	cedarAfter := get(cedar)
	if len(cedarAfter.Questions) != 1 || cedarAfter.Questions[0].State != "answered" || cedarAfter.Questions[0].Delivery != "delivered" ||
		!reflect.DeepEqual(cedarAfter.Questions[0].Answer, &answer.ProductText) {
		t.Fatalf("Cedar answer did not survive delivery: %+v", cedarAfter.Questions)
	}
	resultParams := pi.ProductActivityExecutionParams{ProductActivityParams: pi.ProductActivityParams{ActivityID: cedar}, ExecutionID: cedarRun}
	result, err := process.client.ActivityResult(ctx, resultParams)
	if err != nil || result.ActivityID != cedar || result.ExecutionID != cedarRun || result.Text != "CEDAR_RESULT" || result.Delivery != "available" {
		t.Fatalf("exact activity result=%+v error=%v", result, err)
	}
	repeatedAnswer, err := process.client.AnswerQuestion(ctx, answer)
	if err != nil || repeatedAnswer != answered {
		t.Fatalf("duplicate answer returned %+v, %v, want %+v", repeatedAnswer, err, answered)
	}
	wrong := resultParams
	wrong.ExecutionID = copperRun
	_, err = process.client.ActivityResult(ctx, wrong)
	piRejected(t, err, "not_found")
	process.shutdown(t, ctx)
	budget := []string{"ASK_CEDAR", "ASK_COPPER", "CHAT_WHILE_WAITING", "answer-cedar"}
	f.provider.check(t, budget...)
	reopened := startProductProcess(t, f.options)
	if again := reopened.open(t, ctx); again.StreamID == opened.StreamID {
		t.Fatal("reopened activity process reused the old event stream")
	}
	persisted, err := reopened.client.ActivityResult(ctx, resultParams)
	if !reflect.DeepEqual(productResult(t, persisted, err), result) {
		t.Fatalf("reopen changed the exact activity result: %+v", persisted)
	}
	answerReceipt, err := reopened.client.Operation(ctx, "answer-cedar")
	if err != nil || answerReceipt.State != "accepted" || answerReceipt.Target.ActivityID != cedar || answerReceipt.Target.QuestionID != question.QuestionID {
		t.Fatalf("reopened answer receipt=%+v error=%v", answerReceipt, err)
	}
	stopReceipt, err := reopened.client.Operation(ctx, stop.OperationID)
	var recordedStop pi.ProductStopResult
	if err != nil || stopReceipt.OperationID != stop.OperationID || stopReceipt.State != "accepted" ||
		stopReceipt.Target != (pi.ProductReceiptTarget{ActivityID: copper}) || json.Unmarshal(stopReceipt.Result, &recordedStop) != nil || recordedStop != stopped {
		t.Fatalf("reopened Stop receipt=%+v error=%v", stopReceipt, err)
	}
	duplicateStop, err := reopened.client.StopActivity(ctx, stop)
	if err != nil || duplicateStop != stopped {
		t.Fatalf("duplicate Stop after restart returned %+v, %v, want %+v", duplicateStop, err, stopped)
	}
	stop.ExecutionID = cedarRun
	_, err = reopened.client.StopActivity(ctx, stop)
	piRejected(t, err, "operation_conflict")
	stoppedAnswer := pi.ProductAnswerParams{ProductActivityExecutionParams: pi.ProductActivityExecutionParams{ProductActivityParams: pi.ProductActivityParams{ActivityID: copper}, ExecutionID: copperRun},
		QuestionID: copperQuestion.QuestionID, QuestionRevision: copperQuestion.Revision, OperationID: "answer-stopped-copper", ProductText: pi.ProductText{Text: "Keep the answer on the interrupted execution"}}
	saved, err := reopened.client.AnswerQuestion(ctx, stoppedAnswer)
	if err != nil || !saved.AnswerSaved || saved.Delivery != "saved" || saved.ExecutionID != copperRun || saved.QuestionID != copperQuestion.QuestionID {
		t.Fatalf("answer to interrupted execution=%+v error=%v", saved, err)
	}
	reopened.shutdown(t, ctx)
	afterSaved := startProductProcess(t, f.options)
	afterSaved.open(t, ctx)
	stoppedActivity, err := afterSaved.client.GetActivity(ctx, copper)
	if err != nil || len(stoppedActivity.Executions) != 1 || stoppedActivity.Executions[0].ExecutionID != copperRun || stoppedActivity.Executions[0].State != "interrupted" ||
		len(stoppedActivity.Questions) != 1 || stoppedActivity.Questions[0].QuestionID != copperQuestion.QuestionID || stoppedActivity.Questions[0].Delivery != "saved" ||
		!reflect.DeepEqual(stoppedActivity.Questions[0].Answer, &stoppedAnswer.ProductText) {
		t.Fatalf("reopening changed the saved answer or its interrupted execution: %+v, %v", stoppedActivity, err)
	}
	duplicateAnswer, err := afterSaved.client.AnswerQuestion(ctx, stoppedAnswer)
	if err != nil || duplicateAnswer != saved {
		t.Fatalf("duplicate answer after restart returned %+v, %v, want %+v", duplicateAnswer, err, saved)
	}
	afterSaved.shutdown(t, ctx)
	f.provider.check(t, budget...)
}

func TestPiProductProcessRetainsUnknownSendWithoutReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := newProductFixture(t)
	writeProductFixture(t, f.options.ProfilePath, map[string]any{"version": 10, "contacts": productContacts(f)})
	process := startProductProcess(t, f.options)
	opened := process.open(t, ctx)
	conversationID := opened.Snapshot.Bots[0].CurrentConversationID
	target := pi.ProductReceiptTarget{ConversationID: conversationID}
	draft := productSave(t, process, ctx, conversationID, "save-before-loss", 0, "HOLD_SEND", nil)
	params := pi.ProductSendParams{ProductConversationParams: pi.ProductConversationParams{ConversationID: conversationID}, DraftRevision: draft.Revision, OperationID: "send-before-loss"}
	f.provider.check(t)
	accepted, err := process.client.Send(ctx, params)
	executionID := productExecution(t, productResult(t, accepted, err), params.OperationID, target)
	f.provider.await(t, ctx, "HOLD_SEND")
	process.executionEvent(t, ctx, "execution.started", target, params.OperationID, executionID)
	outcome, err := process.client.Kill(ctx)
	process.exit(t, ctx, outcome, false, err)
	f.provider.check(t, "HOLD_SEND")
	reopened := startProductProcess(t, f.options)
	if again := reopened.open(t, ctx); again.StreamID == opened.StreamID || again.Snapshot.Bots[0].CurrentConversationID != conversationID {
		t.Fatalf("reopen lost the admitted conversation: %+v", again)
	}
	receipt, err := reopened.client.Operation(ctx, params.OperationID)
	if got := productExecution(t, productResult(t, receipt, err), params.OperationID, target); got != executionID || receipt.Execution.State != "unknown" || receipt.Execution.Revision <= accepted.Execution.Revision {
		t.Fatalf("admitted send did not retain its exact unknown execution: %+v", receipt)
	}
	conversation := productConversation(t, reopened, ctx, conversationID)
	latest := conversation.LatestExecution
	if latest == nil || latest.OperationID != params.OperationID || latest.ExecutionID != executionID || latest.State != "unknown" ||
		latest.Revision != receipt.Execution.Revision || conversation.Draft == nil || conversation.Draft.Text != "" || conversation.Draft.Revision <= draft.Revision {
		t.Fatalf("reopened conversation does not reflect actual admission: %+v", conversation)
	}
	duplicate, err := reopened.client.Send(ctx, params)
	if got := productExecution(t, productResult(t, duplicate, err), params.OperationID, target); got != executionID ||
		duplicate.Execution.State != "unknown" || duplicate.Execution.Revision != receipt.Execution.Revision {
		t.Fatalf("duplicate did not return the retained receipt: %+v", duplicate)
	}
	conflict := params
	conflict.DraftRevision++
	_, err = reopened.client.Send(ctx, conflict)
	piRejected(t, err, "operation_conflict")
	conflict.OperationID = "new-send"
	_, err = reopened.client.Send(ctx, conflict)
	piRejected(t, err, "busy")
	reopened.shutdown(t, ctx)
	f.provider.check(t, "HOLD_SEND")
	t.Logf("recovered admitted operation %s and execution %s without replay", params.OperationID, executionID)
}
