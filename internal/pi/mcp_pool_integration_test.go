//go:build integration

package pi_test

import (
	"bytes"
	"context"
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
	"syscall"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
)

const mockMcpStdioScript = `import { appendFileSync } from 'node:fs';
import { createInterface } from 'node:readline';

const ledger = process.env.M2_LEDGER;
const secret = process.env.M2_PRIVATE ?? '';
const server = process.env.M2_FIXTURE;
const tools = [
  { name: 'read_info', description: ` + "`Read fixture info ${secret}`" + `, annotations: { readOnlyHint: true }, inputSchema: { type: 'object', properties: {} } },
  { name: 'write_info', description: 'Write fixture info', annotations: { destructiveHint: true }, inputSchema: { type: 'object', properties: {} } },
  { name: 'contradictory', description: 'Contradictory annotations', annotations: { readOnlyHint: true, destructiveHint: true }, inputSchema: { type: 'object', properties: {} } },
  { name: 'unannotated', description: 'Unannotated', inputSchema: { type: 'object', properties: {} } },
];
const record = (kind, extra = {}) => appendFileSync(ledger, ` + "`${JSON.stringify({ kind, server, pid: process.pid, cwd: process.cwd(), ...extra })}\\n`" + `);
record('spawn');
process.on('SIGTERM', () => record('term'));
setInterval(() => {}, 1000);
for await (const line of createInterface({ input: process.stdin })) {
  const { id, method, params } = JSON.parse(line);
  if (id === undefined) continue;
  let result;
  if (method === 'initialize') result = { protocolVersion: params.protocolVersion, serverInfo: { name: 'stdio-fixture', version: '1' }, capabilities: { tools: {} } };
  if (method === 'tools/list') result = { tools };
  if (method === 'tools/call') {
    record('call', { tool: params.name });
    result = { content: [{ type: 'text', text: JSON.stringify({ pid: process.pid, cwd: process.cwd(), tool: params.name, value: secret }) }] };
  }
  if (result) process.stdout.write(` + "`${JSON.stringify({ jsonrpc: '2.0', id, result })}\\n`" + `);
  else process.stdout.write(` + "`${JSON.stringify({ jsonrpc: '2.0', id, error: { code: -32601, message: 'Not found' } })}\\n`" + `);
}
`

type mcpLedgerRow struct {
	Kind   string `json:"kind"`
	Server string `json:"server"`
	PID    int    `json:"pid"`
	Cwd    string `json:"cwd"`
	Tool   string `json:"tool"`
}

type mcpProviderStep struct {
	Name string
	ID   string
	Args string
}

type mcpHttpEndpoint struct {
	secret string
	fail   bool
}

type mcpHarness struct {
	t             *testing.T
	server        *httptest.Server
	providerCalls atomic.Int64
	mu            sync.Mutex
	scripts       map[string][]mcpProviderStep
	endpoints     map[string]mcpHttpEndpoint
	activeHttp    map[string]struct{}
	httpRequests  int
	advertised    map[string][]string
	outcomes      map[string]map[string]any
	secrets       []string
	problems      []string
	holdUser      string
	held          chan struct{}
	release       chan struct{}
	heldOnce      sync.Once
	releaseOnce   sync.Once
}

func newMcpHarness(t *testing.T, secrets []string) *mcpHarness {
	t.Helper()
	h := &mcpHarness{
		t:          t,
		scripts:    make(map[string][]mcpProviderStep),
		endpoints:  make(map[string]mcpHttpEndpoint),
		activeHttp: make(map[string]struct{}),
		advertised: make(map[string][]string),
		outcomes:   make(map[string]map[string]any),
		secrets:    secrets,
		held:       make(chan struct{}),
		release:    make(chan struct{}),
	}
	h.server = httptest.NewServer(http.HandlerFunc(h.serveHTTP))
	t.Cleanup(func() {
		h.releaseHold()
		h.server.Close()
	})
	return h
}

func (h *mcpHarness) releaseHold() {
	h.releaseOnce.Do(func() { close(h.release) })
}

func (h *mcpHarness) recordProblem(msg string) {
	h.mu.Lock()
	h.problems = append(h.problems, msg)
	h.mu.Unlock()
}

func (h *mcpHarness) assertNoProblems() {
	h.t.Helper()
	h.mu.Lock()
	problems := append([]string(nil), h.problems...)
	h.mu.Unlock()
	if len(problems) > 0 {
		h.t.Fatalf("harness problems: %v", problems)
	}
}

func (h *mcpHarness) containsSecret(raw string) string {
	for _, s := range h.secrets {
		if s != "" && strings.Contains(raw, s) {
			return s
		}
	}
	return ""
}

func (h *mcpHarness) activeHttpCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.activeHttp)
}

func (h *mcpHarness) httpRequestCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.httpRequests
}

func (h *mcpHarness) mcpToolsFor(user string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, name := range h.advertised[user] {
		if strings.HasPrefix(name, "mcp__") {
			out = append(out, name)
		}
	}
	return out
}

func (h *mcpHarness) outcome(callID string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.outcomes[callID]
}

func (h *mcpHarness) setScript(user string, steps ...mcpProviderStep) {
	h.mu.Lock()
	h.scripts[user] = steps
	h.mu.Unlock()
}

func (h *mcpHarness) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/chat/completions" {
		h.serveProvider(w, r)
		return
	}
	h.serveMcpHTTP(w, r)
}

func decodeChatText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}

func (h *mcpHarness) serveProvider(w http.ResponseWriter, r *http.Request) {
	h.providerCalls.Add(1)
	if r.Header.Get("Authorization") != "Bearer "+productKey {
		h.recordProblem("provider authorization header mismatch")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.recordProblem("provider decode: " + err.Error())
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	tools := make([]string, 0, len(body.Tools))
	for _, item := range body.Tools {
		tools = append(tools, item.Function.Name)
	}
	if !slices.IsSorted(tools) {
		h.recordProblem(fmt.Sprintf("tools not sorted alphabetically: %v", tools))
	}
	if rawTools, err := json.Marshal(body.Tools); err == nil {
		if leaked := h.containsSecret(string(rawTools)); leaked != "" {
			h.recordProblem("secret leaked in provider tools: " + leaked)
		}
	}
	var user string
	for _, msg := range body.Messages {
		if msg.Role == "user" {
			user = decodeChatText(msg.Content)
			if idx := strings.Index(user, "\n\n<active_activities>"); idx >= 0 {
				user = user[:idx]
			}
		}
		isFilterCall := false
		for _, tc := range msg.ToolCalls {
			if strings.HasPrefix(tc.ID, "filter-") {
				isFilterCall = true
			}
		}
		if !isFilterCall {
			if rawMsg, err := json.Marshal(msg); err == nil {
				if leaked := h.containsSecret(string(rawMsg)); leaked != "" {
					h.recordProblem("secret leaked in provider message: " + leaked)
				}
			}
		}
	}
	last := body.Messages[len(body.Messages)-1]
	h.mu.Lock()
	if _, seen := h.advertised[user]; !seen {
		h.advertised[user] = tools
	}
	plan, ok := h.scripts[user]
	hold := h.holdUser
	h.mu.Unlock()
	if !ok {
		h.recordProblem("unknown provider prompt: " + user)
		http.Error(w, "unknown prompt", http.StatusBadRequest)
		return
	}
	if hold != "" && user == hold && last.Role == "user" {
		h.heldOnce.Do(func() { close(h.held) })
		select {
		case <-h.release:
		case <-r.Context().Done():
			return
		}
	}
	var next *mcpProviderStep
	if last.Role == "user" {
		if len(plan) > 0 {
			next = &plan[0]
		}
	} else if last.Role == "tool" {
		content := decodeChatText(last.Content)
		if leaked := h.containsSecret(content); leaked != "" {
			h.recordProblem("secret leaked in tool result: " + leaked)
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(content), &parsed); err != nil {
			h.recordProblem("tool result not json: " + content)
		} else {
			h.mu.Lock()
			h.outcomes[last.ToolCallID] = parsed
			h.mu.Unlock()
		}
		idx := -1
		for i, step := range plan {
			if step.ID == last.ToolCallID {
				idx = i
				break
			}
		}
		if idx < 0 {
			h.recordProblem("unexpected tool call id: " + last.ToolCallID)
		} else if idx+1 < len(plan) {
			next = &plan[idx+1]
		}
	}
	if next != nil {
		args := next.Args
		if args == "" {
			args = "{}"
		}
		piChatResponse(w, "", next.Name, next.ID, args)
		return
	}
	piChatResponse(w, "Finished MCP fixture turn.", "", "", "")
}

func (h *mcpHarness) serveMcpHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	endpoint, ok := h.endpoints[r.URL.Path]
	h.httpRequests++
	h.mu.Unlock()
	if !ok {
		h.recordProblem("unknown MCP HTTP path: " + r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Header.Get("Authorization") != endpoint.secret {
		h.recordProblem("MCP HTTP authorization mismatch on " + r.URL.Path)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	session := r.Header.Get("Mcp-Session-Id")
	if r.Method == http.MethodGet || r.Method == http.MethodDelete {
		h.mu.Lock()
		_, active := h.activeHttp[session]
		if active && r.Method == http.MethodDelete {
			delete(h.activeHttp, session)
		}
		h.mu.Unlock()
		if !active {
			h.recordProblem("MCP HTTP " + r.Method + " for unknown session")
			http.Error(w, "unknown session", http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var rpc struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
			Name            string `json:"name"`
		} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
		h.recordProblem("MCP HTTP decode: " + err.Error())
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var resp map[string]any
	switch rpc.Method {
	case "initialize":
		nextSession := fmt.Sprintf("http-session-%d", time.Now().UnixNano())
		h.mu.Lock()
		h.activeHttp[nextSession] = struct{}{}
		h.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", nextSession)
		resp = map[string]any{
			"jsonrpc": "2.0",
			"id":      rpc.ID,
			"result": map[string]any{
				"protocolVersion": rpc.Params.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "m2-http-fixture", "version": "1"},
			},
		}
	case "notifications/initialized":
		h.mu.Lock()
		_, active := h.activeHttp[session]
		h.mu.Unlock()
		if !active {
			h.recordProblem("MCP HTTP initialized without active session")
			http.Error(w, "bad session", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	case "tools/list":
		h.mu.Lock()
		_, active := h.activeHttp[session]
		h.mu.Unlock()
		if !active {
			h.recordProblem("MCP HTTP tools/list without active session")
			http.Error(w, "bad session", http.StatusBadRequest)
			return
		}
		if endpoint.fail {
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      rpc.ID,
				"error":   map[string]any{"code": -32000, "message": "Probe failure " + endpoint.secret},
			}
		} else {
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      rpc.ID,
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "echo",
						"description": "HTTP fixture " + endpoint.secret,
						"annotations": map[string]any{"readOnlyHint": true},
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
					}},
				},
			}
		}
	case "tools/call":
		h.mu.Lock()
		_, active := h.activeHttp[session]
		h.mu.Unlock()
		if !active {
			h.recordProblem("MCP HTTP tools/call without active session")
			http.Error(w, "bad session", http.StatusBadRequest)
			return
		}
		payload, _ := json.Marshal(map[string]any{"session": session, "value": endpoint.secret})
		resp = map[string]any{
			"jsonrpc": "2.0",
			"id":      rpc.ID,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": string(payload)}},
			},
		}
	default:
		resp = map[string]any{
			"jsonrpc": "2.0",
			"id":      rpc.ID,
			"error":   map[string]any{"code": -32601, "message": "Not found"},
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func mcpStrPtr(s string) *string { return &s }

func mcpReadLedger(t *testing.T, path string) []mcpLedgerRow {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var rows []mcpLedgerRow
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var row mcpLedgerRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("decode ledger row %q: %v", line, err)
		}
		rows = append(rows, row)
	}
	return rows
}

func mcpSpawns(t *testing.T, ledgerPath string) []mcpLedgerRow {
	t.Helper()
	var out []mcpLedgerRow
	for _, row := range mcpReadLedger(t, ledgerPath) {
		if row.Kind == "spawn" {
			out = append(out, row)
		}
	}
	return out
}

func mcpPIDExited(pid int) bool {
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return errors.Is(err, syscall.ESRCH)
}

func mcpWaitUntil(t *testing.T, timeout time.Duration, label string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}

func mcpExpectRemoteCode(t *testing.T, err error, want string, label string) {
	t.Helper()
	var remote *pi.RemoteError
	if !errors.As(err, &remote) || remote.Code != want {
		t.Fatalf("%s: err=%v, want RemoteError code %q", label, err, want)
	}
}

func mcpSendTurn(t *testing.T, ctx context.Context, proc *productProcess, bot pi.ProductBot, text string, opPrefix string) string {
	t.Helper()
	conv, err := proc.client.GetConversation(ctx, bot.CurrentConversationID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ProductText:               pi.ProductText{Text: text},
		ExpectedRevision:          conv.Draft.Revision,
		OperationID:               opPrefix + "-draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := proc.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		DraftRevision:             draft.Revision,
		OperationID:               opPrefix + "-send",
	})
	if err != nil || receipt.Execution == nil {
		t.Fatalf("Send(%s) = %+v, %v", text, receipt, err)
	}
	return receipt.Execution.ExecutionID
}

type mcpEventLog struct {
	settled   map[string]pi.ProductExecutionEvent
	mcpEvents []pi.ProductMcpChange
	botEvents []pi.ProductBotChangedEvent
}

func newMcpEventLog() *mcpEventLog {
	return &mcpEventLog{settled: make(map[string]pi.ProductExecutionEvent)}
}

func (l *mcpEventLog) record(t *testing.T, ev pi.ProductEvent) {
	t.Helper()
	if ev.McpChanged != nil {
		l.mcpEvents = append(l.mcpEvents, *ev.McpChanged)
	}
	if ev.Name == "bot.changed" {
		var changed pi.ProductBotChangedEvent
		if err := json.Unmarshal(ev.Data, &changed); err == nil {
			l.botEvents = append(l.botEvents, changed)
		}
	}
	if ev.Name == "execution.settled" {
		var settled pi.ProductExecutionEvent
		if err := json.Unmarshal(ev.Data, &settled); err != nil {
			t.Fatal(err)
		}
		l.settled[settled.ExecutionID] = settled
	}
}

func (l *mcpEventLog) waitSettled(t *testing.T, ctx context.Context, proc *productProcess, executionID string) {
	t.Helper()
	for {
		if settled, ok := l.settled[executionID]; ok {
			if settled.State != "completed" {
				t.Fatalf("execution %s settled state=%q error=%q", executionID, settled.State, settled.Error)
			}
			return
		}
		l.record(t, proc.event(t, ctx))
	}
}

func (l *mcpEventLog) drainUntil(t *testing.T, ctx context.Context, proc *productProcess, until func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !until() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for expected product event")
		}
		select {
		case failure := <-proc.problems:
			t.Fatalf("product stream: %v", failure)
		case ev := <-proc.events:
			if ev.StreamID != proc.streamID || ev.Seq != proc.seq+1 {
				t.Fatalf("event stream=%q seq=%d want=%q/%d", ev.StreamID, ev.Seq, proc.streamID, proc.seq+1)
			}
			proc.seq = ev.Seq
			l.record(t, ev)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func mcpAssertNoSecretBytes(t *testing.T, path string, secrets []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(data, []byte(secret)) {
			t.Fatalf("MCP secret leaked into %s", path)
		}
	}
}

func mcpRequirePinnedWorker(t *testing.T) (string, string) {
	t.Helper()
	nodePath := os.Getenv("NAGI_TEST_NODE")
	workerPath := os.Getenv("NAGI_TEST_WORKER")
	for _, p := range []string{nodePath, workerPath} {
		if info, err := os.Stat(p); err != nil || !filepath.IsAbs(p) || !info.Mode().IsRegular() {
			t.Fatalf("NAGI_TEST_NODE and NAGI_TEST_WORKER must name pinned regular absolute inputs: %q: %v", p, err)
		}
	}
	if os.Getenv("NAGI_PI_PAYLOAD") == "" {
		t.Setenv("NAGI_PI_PAYLOAD", filepath.Dir(filepath.Dir(nodePath)))
	}
	return nodePath, workerPath
}

func TestPiProductMcpPoolingLifecycleAndPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Setenv("NAGI_MCP_IDLE_TTL_MS", "3500")
	nodePath, workerPath := mcpRequirePinnedWorker(t)

	const localSecret = "synthetic-m2-go-stdio-secret-52527"
	const bareHttpSecret = "synthetic-m2-go-http-secret-42701"
	const httpSecret = "Bearer " + bareHttpSecret
	const bareDraftSecret = "synthetic-m2-go-draft-probe-76023"
	const draftSecret = "Bearer " + bareDraftSecret
	allSecrets := []string{localSecret, httpSecret, bareHttpSecret, draftSecret, bareDraftSecret}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace1 := filepath.Join(root, "workspace-one")
	workspace1Alias := filepath.Join(root, "workspace-one-alias")
	workspace2 := filepath.Join(root, "workspace-two")
	for _, dir := range []string{workspace1, workspace2} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(workspace1, workspace1Alias); err != nil {
		t.Fatal(err)
	}

	stdioScript := filepath.Join(root, "mcp-stdio.mjs")
	if err := os.WriteFile(stdioScript, []byte(mockMcpStdioScript), 0o600); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(root, "stdio-ledger.jsonl")

	harness := newMcpHarness(t, allSecrets)
	harness.endpoints["/mcp"] = mcpHttpEndpoint{secret: httpSecret}
	harness.endpoints["/fail"] = mcpHttpEndpoint{secret: draftSecret, fail: true}
	harness.holdUser = "REVOKE"

	sourceDir := filepath.Join(root, "source")
	opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(sourceDir, "pi"),
		ProfilePath:     filepath.Join(sourceDir, "contacts.json"),
		CredentialsPath: filepath.Join(sourceDir, "connection.json"),
		FreshProfile:    true,
	}

	proc := startProductProcess(t, opts)
	_ = proc.open(t, ctx)

	savedConn, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-conn-save",
		Configuration: pi.ProductConnectionInput{
			APIKey: productKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI:          pi.OpenAICompletions,
				BaseURL:          harness.server.URL + "/v1",
				Model:            productModel,
				ContextTokens:    65536,
				OutputTokens:     4096,
				WorkingDirectory: workspace1,
				MaxActiveTasks:   8,
			},
		},
	})
	if err != nil || savedConn.SavedRevision == nil {
		t.Fatalf("SaveConnection = %+v, %v", savedConn, err)
	}
	if _, err := proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{
		OperationID:   "op-conn-apply",
		SavedRevision: *savedConn.SavedRevision,
	}); err != nil {
		t.Fatal(err)
	}

	stdioConfig := func(kind, isolation string, withSecret bool) pi.ProductMcpServerInput {
		cfg := pi.ProductMcpServerInput{
			ProductMcpServerConfig: pi.ProductMcpServerConfig{
				ServerID:  "stdio-" + kind,
				Name:      kind,
				Transport: "stdio",
				Isolation: isolation,
				Enabled:   true,
				Command:   nodePath,
				Args:      []string{stdioScript},
				Env:       map[string]string{"M2_LEDGER": ledgerPath, "M2_FIXTURE": kind},
			},
		}
		if withSecret {
			cfg.SecretEnvKeys = []string{"M2_PRIVATE"}
		}
		return cfg
	}
	sharedCfg := stdioConfig("shared", "shared", true)
	workspaceCfg := stdioConfig("workspace", "per_workspace", false)
	botCfg := stdioConfig("bot", "per_bot", false)
	httpCfg := pi.ProductMcpServerInput{
		ProductMcpServerConfig: pi.ProductMcpServerConfig{
			ServerID:         "http-main",
			Name:             "http",
			Transport:        "http",
			Isolation:        "shared",
			Enabled:          true,
			URL:              harness.server.URL + "/mcp",
			SecretHeaderKeys: []string{"Authorization"},
		},
	}

	badRelative := sharedCfg
	badRelative.Command = "node"
	_, err = proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{Server: badRelative, OperationID: "op-bad-rel"})
	mcpExpectRemoteCode(t, err, "invalid_params", "relative stdio command")

	badURL := httpCfg
	badURL.URL = strings.Replace(httpCfg.URL, "://", "://user:pass@", 1)
	_, err = proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{Server: badURL, OperationID: "op-bad-url"})
	mcpExpectRemoteCode(t, err, "invalid_params", "HTTP URL with credentials")

	badOverlap := sharedCfg
	badOverlap.Env = map[string]string{"M2_LEDGER": ledgerPath, "M2_FIXTURE": "shared", "M2_PRIVATE": localSecret}
	_, err = proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{Server: badOverlap, OperationID: "op-bad-overlap"})
	mcpExpectRemoteCode(t, err, "invalid_params", "secret env overlapping plain env")

	savedShared, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server:      sharedCfg,
		Secrets:     &pi.ProductMcpSecrets{Env: map[string]*string{"M2_PRIVATE": mcpStrPtr(localSecret)}},
		OperationID: "op-save-shared",
	})
	if err != nil || savedShared.CredentialStatus != "ok" || savedShared.Revision != 1 {
		t.Fatalf("SaveMcpServer(shared) = %+v, %v", savedShared, err)
	}

	replayedShared, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server:      sharedCfg,
		Secrets:     &pi.ProductMcpSecrets{Env: map[string]*string{"M2_PRIVATE": mcpStrPtr(localSecret)}},
		OperationID: "op-save-shared",
	})
	if err != nil || !reflect.DeepEqual(replayedShared, savedShared) {
		t.Fatalf("idempotent SaveMcpServer replay = %+v, %v", replayedShared, err)
	}

	_, err = proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server:      sharedCfg,
		Secrets:     &pi.ProductMcpSecrets{Env: map[string]*string{"M2_PRIVATE": mcpStrPtr("different-synthetic-secret")}},
		OperationID: "op-save-shared",
	})
	mcpExpectRemoteCode(t, err, "operation_conflict", "conflicting secret replay")

	rev1 := savedShared.Revision
	keptSecretCfg := sharedCfg
	keptSecretCfg.Revision = &rev1
	keptSecret, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server:      keptSecretCfg,
		OperationID: "op-save-shared-keep-secret",
	})
	if err != nil || keptSecret.CredentialStatus != "ok" || keptSecret.Revision != 2 {
		t.Fatalf("SaveMcpServer non-secret edit lost vault secret: %+v, %v", keptSecret, err)
	}

	leakInPlain := workspaceCfg
	leakInPlain.ServerID = "stdio-leak"
	leakInPlain.Name = "leak"
	leakInPlain.Env = map[string]string{"M2_LEDGER": ledgerPath, "M2_FIXTURE": localSecret}
	_, err = proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{Server: leakInPlain, OperationID: "op-bad-plain-secret"})
	mcpExpectRemoteCode(t, err, "mcp_secret", "vault secret in plain env")

	dupNS := workspaceCfg
	dupNS.ServerID = "stdio-dup-ns"
	dupNS.Name = "shared"
	_, err = proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{Server: dupNS, OperationID: "op-dup-ns"})
	mcpExpectRemoteCode(t, err, "invalid_params", "duplicate namespace")

	if _, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{Server: workspaceCfg, OperationID: "op-save-ws"}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{Server: botCfg, OperationID: "op-save-bot"}); err != nil {
		t.Fatal(err)
	}
	savedHTTP, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server:      httpCfg,
		Secrets:     &pi.ProductMcpSecrets{Headers: map[string]*string{"authorization": mcpStrPtr(httpSecret)}},
		OperationID: "op-save-http",
	})
	if err != nil || savedHTTP.CredentialStatus != "ok" {
		t.Fatalf("SaveMcpServer(http) = %+v, %v", savedHTTP, err)
	}

	vaultPath := filepath.Join(sourceDir, "mcp-secrets.json")
	vaultInfo, err := os.Stat(vaultPath)
	if err != nil {
		t.Fatalf("mcp-secrets.json missing: %v", err)
	}
	if vaultInfo.Mode().Perm() != 0o600 {
		t.Fatalf("mcp-secrets.json perm = %o, want 0600", vaultInfo.Mode().Perm())
	}
	if got := len(mcpSpawns(t, ledgerPath)); got != 0 {
		t.Fatalf("lazy start violated on save: stdio spawns = %d, want 0", got)
	}
	if got := harness.httpRequestCount(); got != 0 {
		t.Fatalf("lazy start violated on save: http requests = %d, want 0", got)
	}

	draftProbeCfg := httpCfg
	draftProbeCfg.ServerID = "scratch-http"
	draftProbeCfg.Name = "scratch"
	draftProbeCfg.URL = harness.server.URL + "/fail"
	draftProbe, err := proc.client.TestMcpServer(ctx, pi.ProductMcpTestParams{
		Server:  &draftProbeCfg,
		Secrets: &pi.ProductMcpSecrets{Headers: map[string]*string{"Authorization": mcpStrPtr(draftSecret)}},
	})
	if err != nil || draftProbe.OK || !strings.Contains(draftProbe.Error, "[credential]") || strings.Contains(draftProbe.Error, bareDraftSecret) {
		t.Fatalf("draft probe redaction mismatch: %+v, %v", draftProbe, err)
	}
	if harness.activeHttpCount() != 0 {
		t.Fatalf("draft probe leaked HTTP session: %d", harness.activeHttpCount())
	}

	probeShared, err := proc.client.TestMcpServer(ctx, pi.ProductMcpTestParams{ServerID: sharedCfg.ServerID})
	if err != nil || !probeShared.OK || len(probeShared.Tools) != 4 {
		t.Fatalf("TestMcpServer(shared) = %+v, %v", probeShared, err)
	}
	foundReadInfo := false
	for _, tool := range probeShared.Tools {
		if tool.Name == "read_info" {
			foundReadInfo = true
			if !strings.Contains(tool.Description, "[credential]") || strings.Contains(tool.Description, localSecret) {
				t.Fatalf("tool catalog description leaked or missed redaction: %q", tool.Description)
			}
		}
	}
	if !foundReadInfo {
		t.Fatalf("read_info missing from probe catalog: %+v", probeShared.Tools)
	}
	probeSpawns := mcpSpawns(t, ledgerPath)
	if len(probeSpawns) != 1 {
		t.Fatalf("probe spawns = %d, want 1", len(probeSpawns))
	}
	mcpWaitUntil(t, 5*time.Second, "probe stdio pid exit", func() bool { return mcpPIDExited(probeSpawns[0].PID) })

	probeHTTP, err := proc.client.TestMcpServer(ctx, pi.ProductMcpTestParams{ServerID: httpCfg.ServerID})
	if err != nil || !probeHTTP.OK || len(probeHTTP.Tools) != 1 {
		t.Fatalf("TestMcpServer(http) = %+v, %v", probeHTTP, err)
	}
	if harness.activeHttpCount() != 0 {
		t.Fatalf("saved HTTP probe leaked session: %d", harness.activeHttpCount())
	}
	if pools, err := proc.client.McpStatus(ctx); err != nil || len(pools) != 0 {
		t.Fatalf("McpStatus after probes = %+v, %v", pools, err)
	}
	listed, err := proc.client.ListMcpServers(ctx)
	if err != nil || len(listed) != 4 {
		t.Fatalf("ListMcpServers = %+v, %v", listed, err)
	}
	var sharedView *pi.ProductMcpServerView
	for i := range listed {
		if listed[i].ServerID == sharedCfg.ServerID {
			sharedView = &listed[i]
		}
	}
	if sharedView == nil || sharedView.Catalog == nil || len(sharedView.Catalog.Tools) != 4 {
		t.Fatalf("cached catalog missing on shared server: %+v", sharedView)
	}

	evLog1 := newMcpEventLog()
	evLog1.drainUntil(t, ctx, proc, func() bool {
		savedSeen, testedSeen := false, false
		for _, ev := range evLog1.mcpEvents {
			if ev.ServerID == sharedCfg.ServerID && ev.Change == "saved" {
				savedSeen = true
			}
			if ev.ServerID == httpCfg.ServerID && ev.Change == "tested" {
				testedSeen = true
			}
		}
		return savedSeen && testedSeen
	})

	botA, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-a",
		Profile: pi.ProductBotProfile{
			Name: "Bot A", Workspace: workspace1, CodingTools: []string{},
			McpBindings: []pi.ProductMcpBinding{
				{ServerID: sharedCfg.ServerID, Policy: "readonly"},
				{ServerID: workspaceCfg.ServerID, Policy: "readonly"},
				{ServerID: botCfg.ServerID, Policy: "readonly"},
				{ServerID: httpCfg.ServerID, Policy: "readonly"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	botB, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-b",
		Profile: pi.ProductBotProfile{
			Name: "Bot B", Workspace: workspace1Alias, CodingTools: []string{},
			McpBindings: []pi.ProductMcpBinding{
				{ServerID: sharedCfg.ServerID, Policy: "allowlist", AllowedTools: []string{"write_info"}},
				{ServerID: workspaceCfg.ServerID, Policy: "readonly"},
				{ServerID: botCfg.ServerID, Policy: "readonly"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	botC, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-c",
		Profile: pi.ProductBotProfile{
			Name: "Bot C", Workspace: workspace2, CodingTools: []string{},
			McpBindings: []pi.ProductMcpBinding{
				{ServerID: workspaceCfg.ServerID, Policy: "readonly"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	proc.shutdown(t, ctx)

	spawnsBeforeReopen := len(mcpSpawns(t, ledgerPath))
	httpBeforeReopen := harness.httpRequestCount()
	providerBeforeReopen := harness.providerCalls.Load()

	opts.FreshProfile = false
	proc = startProductProcess(t, opts)
	_ = proc.open(t, ctx)
	if len(mcpSpawns(t, ledgerPath)) != spawnsBeforeReopen || harness.httpRequestCount() != httpBeforeReopen || harness.providerCalls.Load() != providerBeforeReopen {
		t.Fatalf("app.open violated lazy start: spawns=%d/%d http=%d/%d provider=%d/%d",
			len(mcpSpawns(t, ledgerPath)), spawnsBeforeReopen, harness.httpRequestCount(), httpBeforeReopen, harness.providerCalls.Load(), providerBeforeReopen)
	}
	if pools, err := proc.client.McpStatus(ctx); err != nil || len(pools) != 0 {
		t.Fatalf("McpStatus on reopen = %+v, %v", pools, err)
	}

	const (
		toolSharedRead  = "mcp__shared__read_info"
		toolSharedWrite = "mcp__shared__write_info"
		toolWsRead      = "mcp__workspace__read_info"
		toolBotRead     = "mcp__bot__read_info"
		toolHttpEcho    = "mcp__http__echo"
	)

	harness.setScript("A1",
		mcpProviderStep{Name: toolSharedRead, ID: "a-share"},
		mcpProviderStep{Name: toolWsRead, ID: "a-work"},
		mcpProviderStep{Name: toolBotRead, ID: "a-bot"},
		mcpProviderStep{Name: toolHttpEcho, ID: "a-http"},
	)
	harness.setScript("B1",
		mcpProviderStep{Name: toolSharedWrite, ID: "b-share"},
		mcpProviderStep{Name: toolWsRead, ID: "b-work"},
		mcpProviderStep{Name: toolBotRead, ID: "b-bot"},
	)
	harness.setScript("C1",
		mcpProviderStep{Name: toolWsRead, ID: "c-work"},
	)

	evLog := newMcpEventLog()
	execA1 := mcpSendTurn(t, ctx, proc, botA, "A1", "op-a1")
	evLog.waitSettled(t, ctx, proc, execA1)
	execB1 := mcpSendTurn(t, ctx, proc, botB, "B1", "op-b1")
	evLog.waitSettled(t, ctx, proc, execB1)
	execC1 := mcpSendTurn(t, ctx, proc, botC, "C1", "op-c1")
	evLog.waitSettled(t, ctx, proc, execC1)

	wantA1 := []string{toolBotRead, toolHttpEcho, toolSharedRead, toolWsRead}
	wantB1 := []string{toolBotRead, toolSharedWrite, toolWsRead}
	wantC1 := []string{toolWsRead}
	if !reflect.DeepEqual(harness.mcpToolsFor("A1"), wantA1) {
		t.Fatalf("Bot A advertised MCP tools = %v, want %v", harness.mcpToolsFor("A1"), wantA1)
	}
	if !reflect.DeepEqual(harness.mcpToolsFor("B1"), wantB1) {
		t.Fatalf("Bot B advertised MCP tools = %v, want %v", harness.mcpToolsFor("B1"), wantB1)
	}
	if !reflect.DeepEqual(harness.mcpToolsFor("C1"), wantC1) {
		t.Fatalf("Bot C advertised MCP tools = %v, want %v", harness.mcpToolsFor("C1"), wantC1)
	}

	pidOf := func(callID string) int {
		t.Helper()
		res := harness.outcome(callID)
		v, ok := res["pid"].(float64)
		if !ok || v <= 0 {
			t.Fatalf("outcome %s missing pid: %+v", callID, res)
		}
		return int(v)
	}
	if pidOf("a-share") != pidOf("b-share") {
		t.Fatalf("shared MCP server spawned separate PIDs: A=%d B=%d", pidOf("a-share"), pidOf("b-share"))
	}
	if pidOf("a-work") != pidOf("b-work") {
		t.Fatalf("per_workspace MCP did not reuse canonical symlink workspace: A=%d B=%d", pidOf("a-work"), pidOf("b-work"))
	}
	if pidOf("a-work") == pidOf("c-work") {
		t.Fatalf("per_workspace MCP shared PID across distinct workspaces: %d", pidOf("a-work"))
	}
	if harness.outcome("a-work")["cwd"] != workspace1 || harness.outcome("c-work")["cwd"] != workspace2 {
		t.Fatalf("per_workspace cwd mismatch: A=%v C=%v", harness.outcome("a-work")["cwd"], harness.outcome("c-work")["cwd"])
	}
	if pidOf("a-bot") == pidOf("b-bot") {
		t.Fatalf("per_bot MCP shared PID across bots: %d", pidOf("a-bot"))
	}
	if harness.outcome("a-share")["value"] != "[credential]" {
		t.Fatalf("stdio MCP output not redacted: %+v", harness.outcome("a-share"))
	}
	if harness.outcome("a-http")["value"] != "[credential]" {
		t.Fatalf("HTTP MCP output not redacted: %+v", harness.outcome("a-http"))
	}

	livePools, err := proc.client.McpStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	poolCounts := map[string]int{}
	for _, pool := range livePools {
		poolCounts[pool.ServerID]++
	}
	if poolCounts[sharedCfg.ServerID] != 1 || poolCounts[workspaceCfg.ServerID] != 2 || poolCounts[botCfg.ServerID] != 2 || poolCounts[httpCfg.ServerID] != 1 {
		t.Fatalf("unexpected live pool counts: %+v pools=%+v", poolCounts, livePools)
	}
	if got := len(mcpSpawns(t, ledgerPath)) - spawnsBeforeReopen; got != 5 {
		t.Fatalf("expected 5 pooled stdio spawns, got %d", got)
	}
	if harness.activeHttpCount() != 1 {
		t.Fatalf("expected 1 pooled HTTP session, got %d", harness.activeHttpCount())
	}

	harness.setScript("REVOKE", mcpProviderStep{Name: toolSharedWrite, ID: "revoked-write"})
	execRevoke := mcpSendTurn(t, ctx, proc, botB, "REVOKE", "op-revoke")
	select {
	case <-harness.held:
	case <-ctx.Done():
		t.Fatal("timed out waiting for REVOKE turn at provider")
	}
	clearedProfile := botB.Profile
	clearedProfile.McpBindings = nil
	botB, err = proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			OperationID:      "op-clear-b-mcp",
			BotID:            botB.BotID,
			ExpectedRevision: botB.ProfileRevision,
		},
		Profile:              clearedProfile,
		StartNewConversation: false,
	})
	harness.releaseHold()
	if err != nil {
		t.Fatal(err)
	}
	evLog.waitSettled(t, ctx, proc, execRevoke)
	if harness.outcome("revoked-write")["code"] != "scope_denied" {
		t.Fatalf("dynamic policy revocation did not return scope_denied: %+v", harness.outcome("revoked-write"))
	}
	writeCalls := 0
	for _, row := range mcpReadLedger(t, ledgerPath) {
		if row.Kind == "call" && row.Tool == "write_info" {
			writeCalls++
		}
	}
	if writeCalls != 1 {
		t.Fatalf("revoked write_info reached MCP server: call count = %d, want 1", writeCalls)
	}

	delegateBArgs, _ := json.Marshal(map[string]any{"goal": "B_ACTIVITY", "key": "handoff-b", "assignee_id": botB.BotID})
	harness.setScript("DELEGATE_B", mcpProviderStep{Name: "activity_delegate", ID: "call-handoff-b", Args: string(delegateBArgs)})
	harness.setScript("B_ACTIVITY")
	execDelB := mcpSendTurn(t, ctx, proc, botA, "DELEGATE_B", "op-del-b")
	evLog.waitSettled(t, ctx, proc, execDelB)
	handoffBExec, _ := harness.outcome("call-handoff-b")["execution"].(map[string]any)
	handoffBExecID, _ := handoffBExec["executionId"].(string)
	if handoffBExecID == "" {
		t.Fatalf("activity_delegate to Bot B missing executionId: %+v", harness.outcome("call-handoff-b"))
	}
	evLog.waitSettled(t, ctx, proc, handoffBExecID)
	if got := harness.mcpToolsFor("B_ACTIVITY"); len(got) != 0 {
		t.Fatalf("Bot B delegated Activity leaked Bot A MCP tools: %v", got)
	}

	delegateAArgs, _ := json.Marshal(map[string]any{"goal": "A_ACTIVITY", "key": "handoff-a"})
	harness.setScript("DELEGATE_A", mcpProviderStep{Name: "activity_delegate", ID: "call-handoff-a", Args: string(delegateAArgs)})
	harness.setScript("A_ACTIVITY", mcpProviderStep{Name: toolSharedRead, ID: "activity-read"})
	execDelA := mcpSendTurn(t, ctx, proc, botA, "DELEGATE_A", "op-del-a")
	evLog.waitSettled(t, ctx, proc, execDelA)
	handoffATarget, _ := harness.outcome("call-handoff-a")["target"].(map[string]any)
	handoffAActivityID, _ := handoffATarget["activityId"].(string)
	handoffAExec, _ := harness.outcome("call-handoff-a")["execution"].(map[string]any)
	handoffAExecID, _ := handoffAExec["executionId"].(string)
	if handoffAActivityID == "" || handoffAExecID == "" {
		t.Fatalf("activity_delegate to Bot A missing target/execution: %+v", harness.outcome("call-handoff-a"))
	}
	evLog.waitSettled(t, ctx, proc, handoffAExecID)
	if !slices.Contains(harness.mcpToolsFor("A_ACTIVITY"), toolSharedRead) {
		t.Fatalf("Bot A Activity missing %s: %v", toolSharedRead, harness.mcpToolsFor("A_ACTIVITY"))
	}
	tracePage, err := proc.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: handoffAActivityID},
	})
	if err != nil {
		t.Fatal(err)
	}
	traceJSON, _ := json.Marshal(tracePage.Items)
	if strings.Contains(string(traceJSON), localSecret) {
		t.Fatalf("Activity trace leaked vault secret: %s", string(traceJSON))
	}
	sawRedactedTrace := false
	for _, item := range tracePage.Items {
		if item.Kind == "tool_finished" && item.Tool == toolSharedRead && strings.Contains(item.Excerpt, "[credential]") {
			sawRedactedTrace = true
		}
	}
	if !sawRedactedTrace {
		t.Fatalf("Activity trace missing redacted tool_finished for %s: %s", toolSharedRead, string(traceJSON))
	}

	idlePIDs := []int{}
	for _, row := range mcpSpawns(t, ledgerPath) {
		idlePIDs = append(idlePIDs, row.PID)
	}
	mcpWaitUntil(t, 12*time.Second, "idle TTL reap of all pooled MCP servers", func() bool {
		pools, err := proc.client.McpStatus(ctx)
		if err != nil || len(pools) != 0 || harness.activeHttpCount() != 0 {
			return false
		}
		for _, pid := range idlePIDs {
			if !mcpPIDExited(pid) {
				return false
			}
		}
		return true
	})
	sawTerm := false
	for _, row := range mcpReadLedger(t, ledgerPath) {
		if row.Kind == "term" && slices.Contains(idlePIDs, row.PID) {
			sawTerm = true
		}
	}
	if !sawTerm {
		t.Fatal("expected SIGTERM->SIGKILL escalation record in stdio ledger during idle reap")
	}

	harness.setScript("RESTART_POOL", mcpProviderStep{Name: toolSharedRead, ID: "restart-share"})
	execRestart := mcpSendTurn(t, ctx, proc, botA, "RESTART_POOL", "op-restart")
	evLog.waitSettled(t, ctx, proc, execRestart)

	restartedPools, err := proc.client.McpStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var oldSharedPID, oldBotPID int
	for _, pool := range restartedPools {
		if pool.ServerID == sharedCfg.ServerID && pool.PID != nil {
			oldSharedPID = *pool.PID
		}
		if pool.ServerID == botCfg.ServerID && pool.PID != nil {
			oldBotPID = *pool.PID
		}
	}
	if oldSharedPID == 0 || oldBotPID == 0 {
		t.Fatalf("expected live shared and bot PIDs after restart: %+v", restartedPools)
	}

	editedShared := sharedCfg
	editedShared.Env = map[string]string{"M2_LEDGER": ledgerPath, "M2_FIXTURE": "shared", "M2_UPDATED": "1"}
	updatedShared, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server:      editedShared,
		OperationID: "op-edit-shared-recycle",
	})
	if err != nil || updatedShared.CredentialStatus != "ok" {
		t.Fatalf("SaveMcpServer recycle = %+v, %v", updatedShared, err)
	}
	mcpWaitUntil(t, 5*time.Second, "old shared PID reaped after save", func() bool { return mcpPIDExited(oldSharedPID) })

	delParams := pi.ProductMcpDeleteParams{ServerID: botCfg.ServerID, OperationID: "op-delete-bot-mcp"}
	deleted, err := proc.client.DeleteMcpServer(ctx, delParams)
	if err != nil || !deleted {
		t.Fatalf("DeleteMcpServer = %v, %v", deleted, err)
	}
	deletedReplay, err := proc.client.DeleteMcpServer(ctx, delParams)
	if err != nil || !deletedReplay {
		t.Fatalf("DeleteMcpServer replay = %v, %v", deletedReplay, err)
	}
	mcpWaitUntil(t, 5*time.Second, "deleted bot server PID reaped", func() bool { return mcpPIDExited(oldBotPID) })

	evLog.drainUntil(t, ctx, proc, func() bool {
		sawDel, sawBotUpdate := false, false
		for _, ev := range evLog.mcpEvents {
			if ev.ServerID == botCfg.ServerID && ev.Change == "deleted" {
				sawDel = true
			}
		}
		for _, ev := range evLog.botEvents {
			if ev.Bot.BotID == botA.BotID && ev.Change == "updated" && len(ev.Bot.Profile.McpBindings) == 3 {
				sawBotUpdate = true
			}
		}
		return sawDel && sawBotUpdate
	})

	liveAtShutdown, err := proc.client.McpStatus(ctx)
	if err != nil || len(liveAtShutdown) == 0 {
		t.Fatalf("expected live pools before shutdown: %+v, %v", liveAtShutdown, err)
	}
	proc.shutdown(t, ctx)

	for _, row := range mcpSpawns(t, ledgerPath) {
		if !mcpPIDExited(row.PID) {
			t.Fatalf("orphan stdio MCP child survived shutdown: pid=%d server=%s", row.PID, row.Server)
		}
	}
	if harness.activeHttpCount() != 0 {
		t.Fatalf("active HTTP MCP session survived shutdown: %d", harness.activeHttpCount())
	}
	harness.assertNoProblems()
}

func TestPiProductMcpVaultExclusionBackupRestoreAndMigration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	nodePath, workerPath := mcpRequirePinnedWorker(t)

	const localSecret = "synthetic-m2-vault-excl-stdio-99104"
	const bareHttpSecret = "synthetic-m2-vault-excl-http-77312"
	const httpSecret = "Bearer " + bareHttpSecret
	allSecrets := []string{localSecret, httpSecret, bareHttpSecret}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	stdioScript := filepath.Join(root, "mcp-stdio.mjs")
	if err := os.WriteFile(stdioScript, []byte(mockMcpStdioScript), 0o600); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(root, "ledger.jsonl")

	harness := newMcpHarness(t, allSecrets)
	harness.endpoints["/mcp"] = mcpHttpEndpoint{secret: httpSecret}

	sourceDir := filepath.Join(root, "source")
	opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(sourceDir, "pi"),
		ProfilePath:     filepath.Join(sourceDir, "contacts.json"),
		CredentialsPath: filepath.Join(sourceDir, "connection.json"),
		FreshProfile:    true,
	}

	proc := startProductProcess(t, opts)
	_ = proc.open(t, ctx)

	savedConn, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-conn-save",
		Configuration: pi.ProductConnectionInput{
			APIKey: productKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI:          pi.OpenAICompletions,
				BaseURL:          harness.server.URL + "/v1",
				Model:            productModel,
				ContextTokens:    65536,
				OutputTokens:     4096,
				WorkingDirectory: workspace,
				MaxActiveTasks:   4,
			},
		},
	})
	if err != nil || savedConn.SavedRevision == nil {
		t.Fatalf("SaveConnection = %+v, %v", savedConn, err)
	}
	if _, err := proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{
		OperationID:   "op-conn-apply",
		SavedRevision: *savedConn.SavedRevision,
	}); err != nil {
		t.Fatal(err)
	}

	stdioCfg := pi.ProductMcpServerInput{
		ProductMcpServerConfig: pi.ProductMcpServerConfig{
			ServerID:      "stdio-vault",
			Name:          "vault",
			Transport:     "stdio",
			Isolation:     "shared",
			Enabled:       true,
			Command:       nodePath,
			Args:          []string{stdioScript},
			Env:           map[string]string{"M2_LEDGER": ledgerPath, "M2_FIXTURE": "vault"},
			SecretEnvKeys: []string{"M2_PRIVATE"},
		},
	}
	httpCfg := pi.ProductMcpServerInput{
		ProductMcpServerConfig: pi.ProductMcpServerConfig{
			ServerID:         "http-vault",
			Name:             "httpvault",
			Transport:        "http",
			Isolation:        "shared",
			Enabled:          true,
			URL:              harness.server.URL + "/mcp",
			SecretHeaderKeys: []string{"Authorization"},
		},
	}
	if _, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server:      stdioCfg,
		Secrets:     &pi.ProductMcpSecrets{Env: map[string]*string{"M2_PRIVATE": mcpStrPtr(localSecret)}},
		OperationID: "op-save-stdio-vault",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server:      httpCfg,
		Secrets:     &pi.ProductMcpSecrets{Headers: map[string]*string{"Authorization": mcpStrPtr(httpSecret)}},
		OperationID: "op-save-http-vault",
	}); err != nil {
		t.Fatal(err)
	}

	bot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-vault",
		Profile: pi.ProductBotProfile{
			Name: "Vault Bot", Workspace: workspace, CodingTools: []string{},
			McpBindings: []pi.ProductMcpBinding{
				{ServerID: stdioCfg.ServerID, Policy: "readonly"},
				{ServerID: httpCfg.ServerID, Policy: "readonly"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: bot.BotID, OperationID: "op-mem-stdio"},
		Text:                   "Remember key " + localSecret + " for vault",
	})
	mcpExpectRemoteCode(t, err, "memory_secret", "AddMemoryRule stdio secret")

	_, err = proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: bot.BotID, OperationID: "op-mem-http-bare"},
		Text:                   "Remember bare token " + bareHttpSecret + " for http",
	})
	mcpExpectRemoteCode(t, err, "memory_secret", "AddMemoryRule bare HTTP Bearer token")

	_, err = proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "rpc-secret-stdio"},
		Name:            "Leak Stdio",
		Description:     "Probe vault exclusion",
		Content:         "Use " + localSecret,
		OperationID:     "op-skill-stdio",
	})
	mcpExpectRemoteCode(t, err, "skill_secret", "SaveSkill stdio secret")

	_, err = proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "rpc-secret-http"},
		Name:            "Leak HTTP",
		Description:     "Probe bare Bearer exclusion",
		Content:         "Token is " + bareHttpSecret,
		OperationID:     "op-skill-http",
	})
	mcpExpectRemoteCode(t, err, "skill_secret", "SaveSkill bare HTTP Bearer token")

	logArgs, _ := json.Marshal(map[string]any{"summary": "Completed task using " + localSecret})
	learnArgs, _ := json.Marshal(map[string]any{"text": "Prefer key " + bareHttpSecret, "reason": "Controlled fixture"})
	skillArgs, _ := json.Marshal(map[string]any{
		"skill_id": "model-secret-probe", "name": "Secret Probe", "description": "Synthetic probe", "content": "Bearer " + bareHttpSecret,
	})
	harness.setScript("EXCLUSION",
		mcpProviderStep{Name: "memory_log_work", ID: "filter-log", Args: string(logArgs)},
		mcpProviderStep{Name: "memory_propose_learning", ID: "filter-learning", Args: string(learnArgs)},
		mcpProviderStep{Name: "skill_save", ID: "filter-skill", Args: string(skillArgs)},
	)
	execExcl := mcpSendTurn(t, ctx, proc, bot, "EXCLUSION", "op-excl")
	newMcpEventLog().waitSettled(t, ctx, proc, execExcl)
	if harness.outcome("filter-log")["code"] != "memory_secret" {
		t.Fatalf("memory_log_work did not reject vault secret: %+v", harness.outcome("filter-log"))
	}
	if harness.outcome("filter-learning")["code"] != "memory_secret" {
		t.Fatalf("memory_propose_learning did not reject bare HTTP secret: %+v", harness.outcome("filter-learning"))
	}
	if harness.outcome("filter-skill")["code"] != "skill_secret" {
		t.Fatalf("skill_save did not reject vault secret: %+v", harness.outcome("filter-skill"))
	}
	if _, err := os.Stat(filepath.Join(sourceDir, "bots", bot.BotID, "skills", "model-secret-probe", "SKILL.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected private skill was written to disk: err=%v", err)
	}

	backupPath := filepath.Join(root, "mcp-recovery.json")
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	for _, checkPath := range []string{
		backupPath,
		opts.ProfilePath,
		filepath.Join(sourceDir, "nagi.db"),
		filepath.Join(sourceDir, "nagi.db-wal"),
	} {
		mcpAssertNoSecretBytes(t, checkPath, allSecrets)
	}

	var parsedBackup struct {
		McpServers []pi.ProductMcpServerConfig `json:"mcpServers"`
		Bots       []pi.ProductBot             `json:"bots"`
	}
	if err := json.Unmarshal(productRead(t, backupPath), &parsedBackup); err != nil {
		t.Fatal(err)
	}
	if len(parsedBackup.McpServers) != 2 || len(parsedBackup.Bots) != 1 || len(parsedBackup.Bots[0].Profile.McpBindings) != 2 {
		t.Fatalf("backup missing MCP servers or Bot bindings: %+v", parsedBackup)
	}

	proc.shutdown(t, ctx)

	tamperedDir := filepath.Join(root, "tampered-vault")
	if err := os.MkdirAll(tamperedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"contacts.json", "nagi.db", "connection.json", "mcp-secrets.json"} {
		data := productRead(t, filepath.Join(sourceDir, name))
		mode := os.FileMode(0o600)
		if name == "mcp-secrets.json" {
			mode = 0o644
		}
		if err := os.WriteFile(filepath.Join(tamperedDir, name), data, mode); err != nil {
			t.Fatal(err)
		}
	}
	badVaultOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(tamperedDir, "pi"),
		ProfilePath:     filepath.Join(tamperedDir, "contacts.json"),
		CredentialsPath: filepath.Join(tamperedDir, "connection.json"),
	}
	badVaultClient, startErr := pi.StartProduct(badVaultOpts)
	if startErr == nil {
		if _, readyErr := badVaultClient.Ready(ctx); readyErr == nil {
			if _, openErr := badVaultClient.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
				t.Fatal("worker admitted non-0600 mcp-secrets.json")
			}
		}
		_, _ = badVaultClient.Wait(ctx)
	}

	providerBeforeRestore := harness.providerCalls.Load()
	spawnsBeforeRestore := len(mcpSpawns(t, ledgerPath))
	httpBeforeRestore := harness.httpRequestCount()

	restoreDir := filepath.Join(root, "restored-independent")
	restoreProfile := filepath.Join(restoreDir, "contacts.json")
	if err := os.MkdirAll(restoreDir, 0o700); err != nil {
		t.Fatal(err)
	}
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
	restoredResult, err := store.RestoreStoreRecoveryCopyWithRename(copyInfo, targetInfo, os.Rename)
	if err != nil || restoredResult.OutputPath != restoreProfile {
		t.Fatalf("RestoreStoreRecoveryCopyWithRename = %+v, %v", restoredResult, err)
	}
	if _, err := os.Stat(filepath.Join(restoreDir, "mcp-secrets.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("independent restore directory unexpectedly had mcp-secrets.json: %v", err)
	}

	restoreOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(restoreDir, "pi"),
		ProfilePath:     restoreProfile,
		CredentialsPath: filepath.Join(restoreDir, "connection.json"),
	}
	restored1 := startProductProcess(t, restoreOpts)
	open1 := restored1.open(t, ctx)
	if len(open1.Snapshot.Bots) != 1 || len(open1.Snapshot.Bots[0].Profile.McpBindings) != 2 {
		t.Fatalf("restored snapshot missing Bot MCP bindings: %+v", open1.Snapshot.Bots)
	}
	restoredServers, err := restored1.client.ListMcpServers(ctx)
	if err != nil || len(restoredServers) != 2 {
		t.Fatalf("restored ListMcpServers = %+v, %v", restoredServers, err)
	}
	for _, srv := range restoredServers {
		if srv.CredentialStatus != "credential_missing" {
			t.Fatalf("restored server %s credentialStatus = %q, want credential_missing", srv.ServerID, srv.CredentialStatus)
		}
	}
	if pools, err := restored1.client.McpStatus(ctx); err != nil || len(pools) != 0 {
		t.Fatalf("restored McpStatus = %+v, %v", pools, err)
	}
	missingProbe, err := restored1.client.TestMcpServer(ctx, pi.ProductMcpTestParams{ServerID: stdioCfg.ServerID})
	if err != nil || missingProbe.OK || missingProbe.Error != "credential_missing" {
		t.Fatalf("restored TestMcpServer = %+v, %v", missingProbe, err)
	}
	restoreLog := newMcpEventLog()
	restoreLog.drainUntil(t, ctx, restored1, func() bool {
		for _, ev := range restoreLog.mcpEvents {
			if ev.ServerID == stdioCfg.ServerID && ev.Change == "tested" {
				return true
			}
		}
		return false
	})
	restored1.shutdown(t, ctx)

	restored2 := startProductProcess(t, restoreOpts)
	_ = restored2.open(t, ctx)
	if pools, err := restored2.client.McpStatus(ctx); err != nil || len(pools) != 0 {
		t.Fatalf("second reopen McpStatus = %+v, %v", pools, err)
	}
	restored2.shutdown(t, ctx)

	if harness.providerCalls.Load() != providerBeforeRestore || len(mcpSpawns(t, ledgerPath)) != spawnsBeforeRestore || harness.httpRequestCount() != httpBeforeRestore {
		t.Fatalf("restore/reopen triggered unexpected I/O: provider=%d/%d stdio=%d/%d http=%d/%d",
			harness.providerCalls.Load(), providerBeforeRestore, len(mcpSpawns(t, ledgerPath)), spawnsBeforeRestore, harness.httpRequestCount(), httpBeforeRestore)
	}

	preDir := filepath.Join(root, "pre-migration")
	preOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(preDir, "pi"),
		ProfilePath:     filepath.Join(preDir, "contacts.json"),
		CredentialsPath: filepath.Join(preDir, "connection.json"),
		FreshProfile:    true,
	}
	preProc := startProductProcess(t, preOpts)
	_ = preProc.open(t, ctx)
	legacyBot, err := preProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-legacy-bot",
		Profile:     pi.ProductBotProfile{Name: "Legacy", Workspace: workspace, CodingTools: []string{}, McpBindings: []pi.ProductMcpBinding{}},
	})
	if err != nil || legacyBot.Profile.McpBindings != nil {
		t.Fatalf("legacy bot should omit empty mcpBindings: %+v, %v", legacyBot, err)
	}
	preProc.shutdown(t, ctx)

	migDir := filepath.Join(root, "migrated-v4-v5")
	if err := os.MkdirAll(migDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyMirror := productRead(t, preOpts.ProfilePath)
	if bytes.Contains(legacyMirror, []byte("mcpBindings")) || bytes.Contains(legacyMirror, []byte("mcpServers")) {
		t.Fatalf("legacy mirror should omit empty MCP fields: %s", string(legacyMirror))
	}
	if err := os.WriteFile(filepath.Join(migDir, "contacts.json"), legacyMirror, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "nagi.db"), productRead(t, filepath.Join(preDir, "nagi.db")), 0o600); err != nil {
		t.Fatal(err)
	}
	migDB := filepath.Join(migDir, "nagi.db")
	runNodeSQLite(t, nodePath, migDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec('DROP TABLE activity_steers; DROP TABLE activity_triage_accounting; ALTER TABLE activities DROP COLUMN updated_at; DROP TABLE activity_progress; DROP TABLE skill_approval_requests; ALTER TABLE activities DROP COLUMN acknowledged_at; ALTER TABLE activities DROP COLUMN acknowledged_execution_id; ALTER TABLE activities DROP COLUMN closed_at; DROP TABLE execution_mutation_admissions; DROP TABLE execution_admission_coverage; DROP TABLE activity_escalations; DROP TABLE provider_accounts; ALTER TABLE activities DROP COLUMN routing_json; DROP TABLE skill_approvals; DROP TABLE mcp_servers; PRAGMA user_version = 4;');
db.close();
`)

	migOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(migDir, "pi"),
		ProfilePath:     filepath.Join(migDir, "contacts.json"),
		CredentialsPath: filepath.Join(migDir, "connection.json"),
	}
	migProc := startProductProcess(t, migOpts)
	migOpen := migProc.open(t, ctx)
	if len(migOpen.Snapshot.Bots) != 1 || migOpen.Snapshot.Bots[0].BotID != legacyBot.BotID {
		t.Fatalf("v4->v8 migration lost bot: %+v", migOpen.Snapshot.Bots)
	}
	if servers, err := migProc.client.ListMcpServers(ctx); err != nil || len(servers) != 0 {
		t.Fatalf("v4->v8 migration servers = %+v, %v", servers, err)
	}
	migProc.shutdown(t, ctx)

	if !bytes.Equal(productRead(t, migOpts.ProfilePath), legacyMirror) {
		t.Fatal("v4->v8 migration altered canonical legacy contacts.json bytes")
	}
	migState := runNodeSQLite(t, nodePath, migDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
const hasTable = !!db.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name='mcp_servers'").get();
db.exec('PRAGMA user_version = 12');
db.close();
console.log(JSON.stringify({ v, fk, hasTable }));
`)
	if migState != `{"v":11,"fk":0,"hasTable":true}` {
		t.Fatalf("v4->v11 migration SQLite state = %s", migState)
	}

	v8Client, startErr := pi.StartProduct(migOpts)
	if startErr == nil {
		if _, readyErr := v8Client.Ready(ctx); readyErr == nil {
			if _, openErr := v8Client.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
				t.Fatal("worker admitted SQLite user_version=12")
			}
		}
		_, _ = v8Client.Wait(ctx)
	}
	botCountAfterV6 := runNodeSQLite(t, nodePath, migDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const n = db.prepare('SELECT COUNT(*) AS n FROM bots').get().n;
db.close();
console.log(n);
`)
	if botCountAfterV6 != "1" {
		t.Fatalf("v6 refusal mutated bots table: count=%s", botCountAfterV6)
	}

	harness.assertNoProblems()
}
