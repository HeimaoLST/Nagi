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
	"os/exec"
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

type routingReqRecord struct {
	Role     string
	User     string
	Model    string
	Thinking string
	LastRole string
}

type routingPlanFunc func(role, lastRole string) (choice string, tool *mcpProviderStep)

type routingMultiHarness struct {
	t        *testing.T
	keys     map[string]string
	servers  map[string]*httptest.Server
	mu       sync.Mutex
	requests []routingReqRecord
	probes   []string
	plans    map[string]routingPlanFunc
	problems []string
}

func newRoutingMultiHarness(t *testing.T, roles []string) *routingMultiHarness {
	t.Helper()
	h := &routingMultiHarness{
		t:       t,
		keys:    make(map[string]string),
		servers: make(map[string]*httptest.Server),
		plans:   make(map[string]routingPlanFunc),
	}
	for _, role := range roles {
		r := role
		h.keys[r] = "synthetic-m4-go-auth-" + r + "-81395"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			h.serveRole(r, w, req)
		}))
		h.servers[r] = srv
		t.Cleanup(srv.Close)
	}
	return h
}

func (h *routingMultiHarness) allSecrets() []string {
	out := make([]string, 0, len(h.keys))
	for _, k := range h.keys {
		out = append(out, k)
	}
	return out
}

func (h *routingMultiHarness) recordProblem(msg string) {
	h.mu.Lock()
	h.problems = append(h.problems, msg)
	h.mu.Unlock()
}

func (h *routingMultiHarness) assertNoProblems() {
	h.t.Helper()
	h.mu.Lock()
	probs := append([]string(nil), h.problems...)
	h.mu.Unlock()
	if len(probs) > 0 {
		h.t.Fatalf("routing harness problems: %v", probs)
	}
}

func (h *routingMultiHarness) setPlan(user string, fn routingPlanFunc) {
	h.mu.Lock()
	h.plans[user] = fn
	h.mu.Unlock()
}

func (h *routingMultiHarness) requestCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

func (h *routingMultiHarness) allRequests() []routingReqRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]routingReqRecord(nil), h.requests...)
}

func (h *routingMultiHarness) probeList() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.probes...)
}

func (h *routingMultiHarness) account(role string, models ...string) pi.ProductProviderAccount {
	return pi.ProductProviderAccount{
		ProviderID:    role,
		Name:          role,
		WireAPI:       pi.OpenAICompletions,
		BaseURL:       h.servers[role].URL + "/v1",
		Models:        models,
		DefaultModel:  "m4-" + role,
		ContextTokens: 65536,
		OutputTokens:  4096,
		Enabled:       true,
	}
}

func (h *routingMultiHarness) serveRole(role string, w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+h.keys[role] {
		h.recordProblem(fmt.Sprintf("role %s auth mismatch", role))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/v1/models" {
		h.mu.Lock()
		h.probes = append(h.probes, role)
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "m4-" + role},
				{"id": h.keys[role]},
				{"id": "m4-" + role + "-pro"},
			},
		})
		return
	}
	if r.URL.Path != "/v1/chat/completions" {
		h.recordProblem("unexpected path: " + r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var body struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
		Messages        []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.recordProblem("decode provider body: " + err.Error())
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var user string
	for _, msg := range body.Messages {
		if msg.Role == "user" {
			user = decodeChatText(msg.Content)
			if idx := strings.Index(user, "\n\n<active_activities>"); idx >= 0 {
				user = user[:idx]
			}
		}
	}
	last := body.Messages[len(body.Messages)-1]
	thinking := body.ReasoningEffort
	if thinking == "" {
		thinking = "off"
	}
	h.mu.Lock()
	h.requests = append(h.requests, routingReqRecord{
		Role:     role,
		User:     user,
		Model:    body.Model,
		Thinking: thinking,
		LastRole: last.Role,
	})
	plan := h.plans[user]
	h.mu.Unlock()

	var choice string
	var tool *mcpProviderStep
	if plan != nil {
		choice, tool = plan(role, last.Role)
	}
	switch choice {
	case "reject-auth":
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("controlled rejected key " + h.keys[role]))
		return
	case "fail":
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("controlled provider failure"))
		return
	}
	if tool != nil {
		args := tool.Args
		if args == "" {
			args = "{}"
		}
		piChatResponse(w, "", tool.Name, tool.ID, args)
		return
	}
	reply := choice
	if reply == "" {
		reply = "Finished controlled turn."
	}
	piChatResponse(w, reply, "", "", "")
}

type routingEventLog struct {
	settled        map[string]pi.ProductExecutionEvent
	providerEvents []pi.ProductProviderChange
}

func newRoutingEventLog() *routingEventLog {
	return &routingEventLog{settled: make(map[string]pi.ProductExecutionEvent)}
}

func (l *routingEventLog) record(t *testing.T, ev pi.ProductEvent) {
	t.Helper()
	if ev.ProviderChanged != nil {
		l.providerEvents = append(l.providerEvents, *ev.ProviderChanged)
	}
	if ev.Name == "execution.settled" {
		var settled pi.ProductExecutionEvent
		if err := json.Unmarshal(ev.Data, &settled); err != nil {
			t.Fatal(err)
		}
		l.settled[settled.ExecutionID] = settled
	}
}

func (l *routingEventLog) waitSettled(t *testing.T, ctx context.Context, proc *productProcess, executionID string) pi.ProductExecutionEvent {
	t.Helper()
	for {
		if settled, ok := l.settled[executionID]; ok {
			return settled
		}
		l.record(t, proc.event(t, ctx))
	}
}

func (l *routingEventLog) drainProviderChange(t *testing.T, ctx context.Context, proc *productProcess, change string) {
	t.Helper()
	has := func() bool {
		for _, ev := range l.providerEvents {
			if ev.Change == change {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(5 * time.Second)
	for !has() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for provider.changed(%s); saw %+v", change, l.providerEvents)
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

func routingDelegate(t *testing.T, ctx context.Context, proc *productProcess, evLog *routingEventLog, h *routingMultiHarness,
	origin, target pi.ProductBot, goal, kind, complexity string, constraints []string, opPrefix string) pi.ProductActivitySummary {
	t.Helper()
	label := "DELEGATE " + goal
	argsMap := map[string]any{"goal": goal, "key": goal, "assignee_id": target.BotID}
	if kind != "" {
		argsMap["kind"] = kind
	}
	if complexity != "" {
		argsMap["complexity"] = complexity
	}
	if len(constraints) > 0 {
		argsMap["constraints"] = constraints
	}
	argsJSON, _ := json.Marshal(argsMap)
	h.setPlan(label, func(_role, lastRole string) (string, *mcpProviderStep) {
		if lastRole == "user" {
			return "", &mcpProviderStep{Name: "activity_delegate", ID: "call_" + opPrefix, Args: string(argsJSON)}
		}
		return "", nil
	})
	execID := mcpSendTurn(t, ctx, proc, origin, label, opPrefix)
	settled := evLog.waitSettled(t, ctx, proc, execID)
	if settled.State != "completed" {
		t.Fatalf("delegate turn %s settled state=%q error=%q", goal, settled.State, settled.Error)
	}
	var found pi.ProductActivitySummary
	mcpWaitUntil(t, 10*time.Second, "delegated activity "+goal, func() bool {
		page, err := proc.client.ListActivities(ctx, pi.ProductActivityListParams{BotID: target.BotID})
		if err != nil {
			return false
		}
		for _, item := range page.Items {
			if item.Goal == goal {
				found = item
				return true
			}
		}
		return false
	})
	return found
}

func runNodeModule(t *testing.T, nodePath, script string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"--input-type=module", "-e", script}, args...)
	cmd := exec.Command(nodePath, cmdArgs...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("node module script failed: %v\n%s\n%s", err, string(out), string(exitErr.Stderr))
		}
		t.Fatalf("node module script failed: %v\n%s", err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func TestPiProductModelRoutingPoolStickinessAndSafeEscalation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	nodePath, workerPath := mcpRequirePinnedWorker(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}

	roles := []string{"dialogue", "coding", "reasoning", "utility"}
	h := newRoutingMultiHarness(t, append(append([]string(nil), roles...), "legacy"))

	var writeCount atomic.Int64
	var observedAdmissionMu sync.Mutex
	var observedAdmission string
	effectPath := filepath.Join(root, "effect")
	dbPath := filepath.Join(sourceDir, "nagi.db")

	mcpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
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
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		var resp map[string]any
		switch rpc.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", fmt.Sprintf("m4-mcp-%d", time.Now().UnixNano()))
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      rpc.ID,
				"result": map[string]any{
					"protocolVersion": rpc.Params.ProtocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "controlled", "version": "1"},
				},
			}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      rpc.ID,
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "write_once",
						"description": "Controlled mutating write with misleading readOnlyHint",
						"annotations": map[string]any{"readOnlyHint": true},
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
					}},
				},
			}
		case "tools/call":
			out := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const rows = db.prepare("SELECT tool_class, state FROM execution_mutation_admissions WHERE tool_name = 'mcp__fixture__write_once'").all();
db.close();
console.log(JSON.stringify(rows));
`)
			observedAdmissionMu.Lock()
			observedAdmission = out
			observedAdmissionMu.Unlock()
			n := writeCount.Add(1)
			_ = os.WriteFile(effectPath, []byte(fmt.Sprintf("%d", n)), 0o600)
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			http.Error(w, "hijack failed", http.StatusInternalServerError)
			return
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mcpSrv.Close)

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

	initialRouting, err := proc.client.Routing(ctx)
	if err != nil || initialRouting.EffectiveRevision != nil {
		t.Fatalf("initial Routing = %+v, %v", initialRouting, err)
	}

	legacyAcc := h.account("legacy")
	savedLegacy, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-legacy-save",
		Configuration: pi.ProductConnectionInput{
			APIKey: h.keys["legacy"],
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI:          legacyAcc.WireAPI,
				BaseURL:          legacyAcc.BaseURL,
				Model:            legacyAcc.DefaultModel,
				ContextTokens:    legacyAcc.ContextTokens,
				OutputTokens:     legacyAcc.OutputTokens,
				WorkingDirectory: root,
				MaxActiveTasks:   8,
			},
		},
	})
	if err != nil || savedLegacy.SavedRevision == nil {
		t.Fatalf("SaveConnection(legacy) = %+v, %v", savedLegacy, err)
	}
	if _, err := proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{
		OperationID:   "op-legacy-apply",
		SavedRevision: *savedLegacy.SavedRevision,
	}); err != nil {
		t.Fatal(err)
	}
	var legacyConnFile map[string]any
	if err := json.Unmarshal(productRead(t, opts.CredentialsPath), &legacyConnFile); err != nil {
		t.Fatal(err)
	}
	if int(legacyConnFile["version"].(float64)) != 2 {
		t.Fatalf("legacy connection.json version = %v, want 2", legacyConnFile["version"])
	}

	legacyBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-legacy-bot",
		Profile:     pi.ProductBotProfile{Name: "Legacy", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	evLog := newRoutingEventLog()
	execLegacy := mcpSendTurn(t, ctx, proc, legacyBot, "LEGACY", "op-legacy-turn")
	if settled := evLog.waitSettled(t, ctx, proc, execLegacy); settled.State != "completed" {
		t.Fatalf("legacy turn failed: %+v", settled)
	}
	reqs := h.allRequests()
	if len(reqs) == 0 || reqs[len(reqs)-1].Role != "legacy" {
		t.Fatalf("legacy turn did not hit legacy endpoint: %+v", reqs)
	}
	callsBeforeMigration := h.requestCount()
	legacyProfileBytes := productRead(t, opts.ProfilePath)
	proc.shutdown(t, ctx)

	runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec('DROP TABLE activity_steers; DROP TABLE activity_triage_accounting; ALTER TABLE activities DROP COLUMN updated_at; DROP TABLE activity_progress; DROP TABLE skill_approval_requests; ALTER TABLE activities DROP COLUMN acknowledged_at; ALTER TABLE activities DROP COLUMN acknowledged_execution_id; ALTER TABLE activities DROP COLUMN closed_at; DROP TABLE execution_mutation_admissions; DROP TABLE execution_admission_coverage; DROP TABLE activity_escalations; DROP TABLE provider_accounts; ALTER TABLE activities DROP COLUMN routing_json; PRAGMA user_version = 6;');
db.close();
`)

	opts.FreshProfile = false
	proc = startProductProcess(t, opts)
	_ = proc.open(t, ctx)
	evLog = newRoutingEventLog()

	migCheck := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.close();
console.log(JSON.stringify({ v, fk }));
`)
	if migCheck != `{"v":10,"fk":0}` {
		t.Fatalf("v6->v10 migration check = %s", migCheck)
	}
	if !bytes.Equal(productRead(t, opts.ProfilePath), legacyProfileBytes) {
		t.Fatal("v6->v7 migration mutated canonical legacy contacts.json bytes")
	}
	if h.requestCount() != callsBeforeMigration {
		t.Fatalf("v6->v7 reopen triggered provider calls: %d vs %d", h.requestCount(), callsBeforeMigration)
	}

	revDlg, err := proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: h.account("dialogue"), APIKey: h.keys["dialogue"]},
		OperationID:               "op-save-dialogue",
	})
	if err != nil || revDlg.SavedRevision == nil || revDlg.EffectiveRevision != nil {
		t.Fatalf("SaveProvider(dialogue) = %+v, %v", revDlg, err)
	}
	connInfo, err := os.Stat(opts.CredentialsPath)
	if err != nil || connInfo.Mode().Perm() != 0o600 {
		t.Fatalf("connection.json v3 mode = %+v, %v", connInfo, err)
	}
	var v3Conn map[string]any
	if err := json.Unmarshal(productRead(t, opts.CredentialsPath), &v3Conn); err != nil {
		t.Fatal(err)
	}
	if int(v3Conn["version"].(float64)) != 3 {
		t.Fatalf("upgraded connection.json version = %v, want 3", v3Conn["version"])
	}
	for k, wantVal := range legacyConnFile {
		if k == "version" {
			continue
		}
		if !reflect.DeepEqual(v3Conn[k], wantVal) {
			t.Fatalf("connection.json v3 lost legacy field %q: got=%v want=%v", k, v3Conn[k], wantVal)
		}
	}

	if _, err := proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		OperationID:   "op-apply-single",
		SavedRevision: *revDlg.SavedRevision,
	}); err != nil {
		t.Fatal(err)
	}
	singleRouting, err := proc.client.Routing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for name, slot := range map[string]*pi.ProductModelRoute{
		"dialogue":  singleRouting.ResolvedRoles.Dialogue,
		"coding":    singleRouting.ResolvedRoles.Coding,
		"reasoning": singleRouting.ResolvedRoles.Reasoning,
		"utility":   singleRouting.ResolvedRoles.Utility,
	} {
		if slot == nil || slot.ResolvedModel != "m4-dialogue" {
			t.Fatalf("single-model collapse slot %s = %+v, want m4-dialogue", name, slot)
		}
	}

	var lastRev pi.ProductConnectionRevisions
	for _, role := range roles[1:] {
		lastRev, err = proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
			ProductProviderTestParams: pi.ProductProviderTestParams{Account: h.account(role), APIKey: h.keys[role]},
			OperationID:               "op-save-" + role,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	beforeApply, err := proc.client.Routing(ctx)
	if err != nil || beforeApply.ResolvedRoles.Utility == nil || beforeApply.ResolvedRoles.Utility.ResolvedModel != "m4-dialogue" {
		t.Fatalf("SaveProvider mutated effective roles before Apply: %+v, %v", beforeApply, err)
	}
	if _, err := proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		OperationID:   "op-apply-four",
		SavedRevision: *lastRev.SavedRevision,
	}); err != nil {
		t.Fatal(err)
	}
	fourRouting, err := proc.client.Routing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fourRouting.ResolvedRoles.Dialogue.ResolvedModel != "m4-dialogue" ||
		fourRouting.ResolvedRoles.Coding.ResolvedModel != "m4-coding" ||
		fourRouting.ResolvedRoles.Reasoning.ResolvedModel != "m4-reasoning" ||
		fourRouting.ResolvedRoles.Utility.ResolvedModel != "m4-utility" {
		t.Fatalf("four-role resolution mismatch: %+v", fourRouting.ResolvedRoles)
	}

	listBeforeTest, err := proc.client.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	testRes, err := proc.client.TestProvider(ctx, pi.ProductProviderTestParams{Account: h.account("dialogue")})
	if err != nil || !testRes.OK || !slices.Equal(testRes.Models, []string{"m4-dialogue", "m4-dialogue-pro"}) {
		t.Fatalf("TestProvider = %+v, %v", testRes, err)
	}
	listAfterTest, err := proc.client.ListProviders(ctx)
	if err != nil || !reflect.DeepEqual(listAfterTest, listBeforeTest) {
		t.Fatalf("TestProvider mutated ListProviders: before=%+v after=%+v err=%v", listBeforeTest, listAfterTest, err)
	}
	if !slices.Equal(h.probeList(), []string{"dialogue"}) || h.requestCount() != callsBeforeMigration {
		t.Fatalf("TestProvider probe/chat counts = %v / %d", h.probeList(), h.requestCount())
	}

	if _, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server: pi.ProductMcpServerInput{
			ProductMcpServerConfig: pi.ProductMcpServerConfig{
				ServerID: "fixture", Name: "fixture", Transport: "http", Isolation: "shared", Enabled: true, URL: mcpSrv.URL + "/mcp",
			},
		},
		OperationID: "op-mcp-fixture",
	}); err != nil {
		t.Fatal(err)
	}

	originBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-origin-bot",
		Profile:     pi.ProductBotProfile{Name: "Origin", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-worker-bot",
		Profile: pi.ProductBotProfile{
			Name: "Worker", Workspace: root, CodingTools: []string{"ls"},
			McpBindings: []pi.ProductMcpBinding{{ServerID: "fixture", Policy: "all"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	execHot1 := mcpSendTurn(t, ctx, proc, originBot, "HOT ONE", "op-hot-1")
	if s := evLog.waitSettled(t, ctx, proc, execHot1); s.State != "completed" {
		t.Fatalf("HOT ONE failed: %+v", s)
	}
	if _, err := proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: h.account("dialogue", "m4-dialogue-pro"), APIKey: h.keys["dialogue"]},
		OperationID:               "op-save-dlg-pro",
	}); err != nil {
		t.Fatal(err)
	}
	polRev, err := proc.client.SetRoutingPolicy(ctx, pi.ProductRoutingPolicyParams{
		Policy: pi.ProductRoutingQuality, OperationID: "op-policy-quality",
	})
	if err != nil || polRev.SavedRevision == nil {
		t.Fatalf("SetRoutingPolicy = %+v, %v", polRev, err)
	}
	if rt, err := proc.client.Routing(ctx); err != nil || rt.EffectivePolicy != pi.ProductRoutingBalanced || rt.Policy != pi.ProductRoutingQuality {
		t.Fatalf("policy before Apply = %+v, %v", rt, err)
	}
	execHot2 := mcpSendTurn(t, ctx, proc, originBot, "HOT TWO", "op-hot-2")
	if s := evLog.waitSettled(t, ctx, proc, execHot2); s.State != "completed" {
		t.Fatalf("HOT TWO failed: %+v", s)
	}
	if _, err := proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		OperationID: "op-apply-quality", SavedRevision: *polRev.SavedRevision,
	}); err != nil {
		t.Fatal(err)
	}
	evLog.drainProviderChange(t, ctx, proc, "policy")
	evLog.drainProviderChange(t, ctx, proc, "applied")
	if rt, err := proc.client.Routing(ctx); err != nil || rt.ResolvedRoles.Dialogue.ResolvedModel != "m4-dialogue-pro" {
		t.Fatalf("dialogue model after quality Apply = %+v, %v", rt, err)
	}
	execHot3 := mcpSendTurn(t, ctx, proc, originBot, "HOT THREE", "op-hot-3")
	if s := evLog.waitSettled(t, ctx, proc, execHot3); s.State != "completed" {
		t.Fatalf("HOT THREE failed: %+v", s)
	}
	var hotModels []string
	for _, req := range h.allRequests() {
		if strings.HasPrefix(req.User, "HOT ") {
			hotModels = append(hotModels, req.Model)
		}
	}
	if !slices.Equal(hotModels, []string{"m4-dialogue", "m4-dialogue", "m4-dialogue"}) {
		t.Fatalf("hot Conversation failed stickiness guard: %v", hotModels)
	}

	freshBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-fresh-bot",
		Profile:     pi.ProductBotProfile{Name: "Fresh", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	execFresh := mcpSendTurn(t, ctx, proc, freshBot, "NEW DIALOGUE", "op-fresh")
	if s := evLog.waitSettled(t, ctx, proc, execFresh); s.State != "completed" {
		t.Fatalf("NEW DIALOGUE failed: %+v", s)
	}
	reqs = h.allRequests()
	if reqs[len(reqs)-1].Model != "m4-dialogue-pro" {
		t.Fatalf("fresh conversation model = %q, want m4-dialogue-pro", reqs[len(reqs)-1].Model)
	}

	codingSum := routingDelegate(t, ctx, proc, evLog, h, originBot, workerBot, "DEEP CODING", "coding", "deep", []string{"fixture constraint"}, "op-del-code")
	codingAct, err := proc.client.GetActivity(ctx, codingSum.ActivityID)
	if err != nil || len(codingAct.Executions) == 0 {
		t.Fatalf("GetActivity(DEEP CODING) = %+v, %v", codingAct, err)
	}
	if s := evLog.waitSettled(t, ctx, proc, codingAct.Executions[0].ExecutionID); s.State != "completed" {
		t.Fatalf("DEEP CODING settled = %+v", s)
	}
	if codingAct.Kind != "coding" || codingAct.Complexity != "deep" || !slices.Equal(codingAct.Constraints, []string{"fixture constraint"}) {
		t.Fatalf("DEEP CODING routing metadata = %+v", codingAct)
	}
	var codingReqs []routingReqRecord
	for _, req := range h.allRequests() {
		if req.User == "DEEP CODING\nfixture constraint" {
			codingReqs = append(codingReqs, req)
		}
	}
	if len(codingReqs) != 1 || codingReqs[0].Role != "coding" || codingReqs[0].Model != "m4-coding" || codingReqs[0].Thinking != "high" {
		t.Fatalf("DEEP CODING provider routing = %+v", codingReqs)
	}

	proc.shutdown(t, ctx)

	protocolJS := filepath.Join(filepath.Dir(workerPath), "protocol.js")
	oldSession := runNodeModule(t, nodePath, `
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { DatabaseSync } from 'node:sqlite';
import { pathToFileURL } from 'node:url';
const [profilePath, dbPath, protocolPath, convId] = process.argv.slice(1);
const { canonicalJSON } = await import(pathToFileURL(protocolPath).href);
const profile = JSON.parse(readFileSync(profilePath, 'utf8'));
const row = profile.conversations.find(item => item.conversationId === convId);
const oldSession = row.binding.sessionRef;
row.binding.lastTurnAt = new Date(Date.now() - 6 * 60 * 1000).toISOString();
const mirror = canonicalJSON(profile) + '\n';
writeFileSync(profilePath, mirror, { mode: 0o600 });
const db = new DatabaseSync(dbPath);
db.exec('BEGIN IMMEDIATE');
db.prepare('UPDATE conversations SET binding_json = ? WHERE conversation_id = ?').run(JSON.stringify(row.binding), JSON.stringify(convId));
db.prepare("UPDATE meta SET value = ? WHERE key = 'snapshot_sha256'").run(createHash('sha256').update(mirror).digest('hex'));
db.exec('COMMIT');
db.close();
console.log(oldSession);
`, opts.ProfilePath, dbPath, protocolJS, originBot.CurrentConversationID)

	proc = startProductProcess(t, opts)
	_ = proc.open(t, ctx)
	evLog = newRoutingEventLog()
	execCold := mcpSendTurn(t, ctx, proc, originBot, "COLD DIALOGUE", "op-cold")
	if s := evLog.waitSettled(t, ctx, proc, execCold); s.State != "completed" {
		t.Fatalf("COLD DIALOGUE failed: %+v", s)
	}
	reqs = h.allRequests()
	if reqs[len(reqs)-1].Model != "m4-dialogue-pro" {
		t.Fatalf("cold dialogue did not adopt applied model: %+v", reqs[len(reqs)-1])
	}
	reboundSession := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const row = db.prepare('SELECT binding_json FROM conversations WHERE conversation_id = ?').get(JSON.stringify(process.argv[2]));
db.close();
const binding = JSON.parse(row.binding_json);
console.log(JSON.stringify({ sessionRef: binding.sessionRef, model: binding.route.resolvedModel }));
`, originBot.CurrentConversationID)
	if !strings.Contains(reboundSession, oldSession) || !strings.Contains(reboundSession, `"m4-dialogue-pro"`) {
		t.Fatalf("cold adoption lost sessionRef or model: got %s want session %s", reboundSession, oldSession)
	}
	hist, err := proc.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: originBot.CurrentConversationID},
		ProductPageParams:         pi.ProductPageParams{Limit: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	sawHotThree := false
	for _, item := range hist.Items {
		if item.Text == "HOT THREE" {
			sawHotThree = true
		}
	}
	if !sawHotThree {
		t.Fatalf("cold adoption lost prior transcript history: %+v", hist.Items)
	}

	h.setPlan("LOST WRITE", func(_role, lastRole string) (string, *mcpProviderStep) {
		if lastRole == "user" {
			return "", &mcpProviderStep{Name: "mcp__fixture__write_once", ID: "call_write_once"}
		}
		return "fail", nil
	})
	lostSum := routingDelegate(t, ctx, proc, evLog, h, originBot, workerBot, "LOST WRITE", "quick", "light", nil, "op-del-lost")
	lostAct, err := proc.client.GetActivity(ctx, lostSum.ActivityID)
	if err != nil || len(lostAct.Executions) != 1 {
		t.Fatalf("GetActivity(LOST WRITE) = %+v, %v", lostAct, err)
	}
	lostExecID := lostAct.Executions[0].ExecutionID
	if s := evLog.waitSettled(t, ctx, proc, lostExecID); s.State != "failed" {
		t.Fatalf("LOST WRITE execution settled = %+v, want failed", s)
	}
	lostAct, err = proc.client.GetActivity(ctx, lostSum.ActivityID)
	if err != nil {
		t.Fatal(err)
	}
	if lostAct.State != "needsAttention" || len(lostAct.Executions) != 1 || lostAct.Executions[0].State != "failed" {
		t.Fatalf("LOST WRITE escalated or wrong state: %+v", lostAct)
	}
	if writeCount.Load() != 1 || string(productRead(t, effectPath)) != "1" {
		t.Fatalf("duplicate side effect on lost receipt: writeCount=%d", writeCount.Load())
	}
	observedAdmissionMu.Lock()
	admSnap := observedAdmission
	observedAdmissionMu.Unlock()
	if admSnap != `[{"tool_class":"mutating","state":"admitted"}]` {
		t.Fatalf("pre-dispatch mutation admission missing before write: %s", admSnap)
	}
	lostTrace, err := proc.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: lostSum.ActivityID},
		ExecutionID:           lostExecID,
	})
	if err != nil {
		t.Fatal(err)
	}
	sawWriteTrace := false
	for _, item := range lostTrace.Items {
		if item.Kind == "tool_started" && item.Tool == "mcp__fixture__write_once" {
			sawWriteTrace = true
		}
	}
	if !sawWriteTrace {
		t.Fatalf("lost write trace missing mcp__fixture__write_once: %+v", lostTrace.Items)
	}
	lostEscCount := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const n = db.prepare('SELECT count(*) AS n FROM activity_escalations WHERE activity_id = ?').get(JSON.stringify(process.argv[2])).n;
db.close();
console.log(n);
`, lostSum.ActivityID)
	if lostEscCount != "0" {
		t.Fatalf("activity_escalations count for lost write = %s, want 0", lostEscCount)
	}

	lsArgs, _ := json.Marshal(map[string]any{"path": root})
	h.setPlan("READ ONLY", func(role, lastRole string) (string, *mcpProviderStep) {
		if role == "utility" && lastRole == "user" {
			return "", &mcpProviderStep{Name: "ls", ID: "call_read_only", Args: string(lsArgs)}
		}
		return "fail", nil
	})
	readSum := routingDelegate(t, ctx, proc, evLog, h, originBot, workerBot, "READ ONLY", "quick", "light", nil, "op-del-read")
	var escalatedAct pi.ProductActivity
	mcpWaitUntil(t, 10*time.Second, "readonly safe escalation", func() bool {
		act, err := proc.client.GetActivity(ctx, readSum.ActivityID)
		if err == nil && len(act.Executions) == 2 {
			escalatedAct = act
			return true
		}
		return false
	})
	_ = evLog.waitSettled(t, ctx, proc, escalatedAct.Executions[1].ExecutionID)
	escalatedAct, err = proc.client.GetActivity(ctx, readSum.ActivityID)
	if err != nil || len(escalatedAct.Executions) != 2 {
		t.Fatalf("escalated activity = %+v, %v", escalatedAct, err)
	}
	priorID, nextID := escalatedAct.Executions[0].ExecutionID, escalatedAct.Executions[1].ExecutionID
	if escalatedAct.Executions[0].State != "failed" || escalatedAct.Executions[1].State != "failed" {
		t.Fatalf("escalated executions states = %s, %s", escalatedAct.Executions[0].State, escalatedAct.Executions[1].State)
	}
	escCheck := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const adm = db.prepare('SELECT tool_class, state FROM execution_mutation_admissions WHERE execution_id = ?').all(JSON.stringify(process.argv[2]));
const esc = db.prepare('SELECT prior_execution_id, escalated_execution_id, from_role, to_role FROM activity_escalations').all();
db.close();
console.log(JSON.stringify({ adm, esc }));
`, priorID)
	if !strings.Contains(escCheck, `"tool_class":"readonly"`) || !strings.Contains(escCheck, `"state":"settled_ok"`) ||
		!strings.Contains(escCheck, priorID) || !strings.Contains(escCheck, nextID) || !strings.Contains(escCheck, `"from_role":"utility"`) || !strings.Contains(escCheck, `"to_role":"reasoning"`) {
		t.Fatalf("readonly escalation SQLite records mismatch: %s", escCheck)
	}
	priorTrace, err := proc.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: readSum.ActivityID},
		ExecutionID:           priorID,
	})
	if err != nil || priorTrace.Coverage != "recorded" {
		t.Fatalf("prior execution trace = %+v, %v", priorTrace, err)
	}
	var readRouteSeq []string
	for _, req := range h.allRequests() {
		if req.User == "READ ONLY" {
			readRouteSeq = append(readRouteSeq, req.Role+":"+req.Thinking)
		}
	}
	if !slices.Equal(readRouteSeq, []string{"utility:off", "utility:off", "reasoning:high"}) {
		t.Fatalf("readonly escalation provider sequence = %v", readRouteSeq)
	}

	proc.shutdown(t, ctx)
	h.assertNoProblems()
}

func TestPiProductModelRoutingDeletedProviderRevocationBackupAndMigration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	nodePath, workerPath := mcpRequirePinnedWorker(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}

	roles := []string{"dialogue", "coding", "reasoning", "utility"}
	h := newRoutingMultiHarness(t, append(append([]string(nil), roles...), "spare"))

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
	evLog := newRoutingEventLog()

	var rev pi.ProductConnectionRevisions
	for _, role := range roles {
		rev, err = proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
			ProductProviderTestParams: pi.ProductProviderTestParams{Account: h.account(role), APIKey: h.keys[role]},
			OperationID:               "op-save-" + role,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		OperationID: "op-apply-base", SavedRevision: *rev.SavedRevision,
	}); err != nil {
		t.Fatal(err)
	}

	originBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-origin",
		Profile:     pi.ProductBotProfile{Name: "Origin", Workspace: root, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-worker",
		Profile:     pi.ProductBotProfile{Name: "Worker", Workspace: root, CodingTools: []string{"ls"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	h.setPlan("AUTH FAIL", func(_role, _lastRole string) (string, *mcpProviderStep) {
		return "reject-auth", nil
	})
	authSum := routingDelegate(t, ctx, proc, evLog, h, originBot, workerBot, "AUTH FAIL", "coding", "standard", nil, "op-del-auth")
	authAct, err := proc.client.GetActivity(ctx, authSum.ActivityID)
	if err != nil || len(authAct.Executions) != 1 {
		t.Fatalf("GetActivity(AUTH FAIL) = %+v, %v", authAct, err)
	}
	if s := evLog.waitSettled(t, ctx, proc, authAct.Executions[0].ExecutionID); s.State != "failed" {
		t.Fatalf("AUTH FAIL settled = %+v, want failed", s)
	}
	authAct, err = proc.client.GetActivity(ctx, authSum.ActivityID)
	if err != nil {
		t.Fatal(err)
	}
	var authReqs []routingReqRecord
	for _, req := range h.allRequests() {
		if req.User == "AUTH FAIL" {
			authReqs = append(authReqs, req)
		}
	}
	if len(authReqs) != 1 || authReqs[0].Role != "coding" || authReqs[0].Thinking != "medium" {
		t.Fatalf("AUTH FAIL requests = %+v", authReqs)
	}
	_, err = proc.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: authSum.ActivityID},
		ExpectedRevision:      authAct.Revision,
		OperationID:           "op-cont-auth",
	})
	mcpExpectRemoteCode(t, err, "credential_unavailable", "ContinueActivity after remote 401")
	authCountAfter := 0
	for _, req := range h.allRequests() {
		if req.User == "AUTH FAIL" {
			authCountAfter++
		}
	}
	if authCountAfter != 1 {
		t.Fatalf("remote 401 did not block subsequent dispatch: count=%d", authCountAfter)
	}

	askArgs, _ := json.Marshal(map[string]any{"text": "Confirm input"})
	h.setPlan("PAUSE", func(_role, lastRole string) (string, *mcpProviderStep) {
		if lastRole == "user" {
			return "", &mcpProviderStep{Name: "activity_ask", ID: "call_pause", Args: string(askArgs)}
		}
		return "", nil
	})
	pausedSum := routingDelegate(t, ctx, proc, evLog, h, originBot, workerBot, "PAUSE", "quick", "light", nil, "op-del-pause")
	var waitingAct pi.ProductActivity
	mcpWaitUntil(t, 10*time.Second, "activity waitingForAnswer", func() bool {
		act, err := proc.client.GetActivity(ctx, pausedSum.ActivityID)
		if err == nil && act.State == "waitingForAnswer" && len(act.Questions) == 1 {
			waitingAct = act
			return true
		}
		return false
	})
	question := waitingAct.Questions[0]

	delRev, err := proc.client.DeleteProvider(ctx, pi.ProductProviderTargetParams{
		ProviderID: "utility", OperationID: "op-del-utility",
	})
	if err != nil || delRev.SavedRevision == nil {
		t.Fatalf("DeleteProvider(utility) = %+v, %v", delRev, err)
	}
	if rt, err := proc.client.Routing(ctx); err != nil || rt.ResolvedRoles.Utility == nil || rt.ResolvedRoles.Utility.ProviderID != "utility" {
		t.Fatalf("DeleteProvider changed effective route before Apply: %+v, %v", rt, err)
	}
	if _, err := proc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		OperationID: "op-apply-del-utility", SavedRevision: *delRev.SavedRevision,
	}); err != nil {
		t.Fatal(err)
	}
	if rt, err := proc.client.Routing(ctx); err != nil || (rt.ResolvedRoles.Utility != nil && rt.ResolvedRoles.Utility.ProviderID == "utility") {
		t.Fatalf("deleted utility still active after Apply: %+v, %v", rt, err)
	}

	if _, err := proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{Account: h.account("spare"), APIKey: h.keys["spare"]},
		OperationID:               "op-save-spare",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: originBot.BotID, OperationID: "op-mem-spare"},
		Text:                   "Retain " + h.keys["spare"],
	})
	mcpExpectRemoteCode(t, err, "memory_secret", "AddMemoryRule inactive provider key")
	_, err = proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: originBot.BotID, SkillID: "m4-spare-secret"},
		Name:            "Spare Secret",
		Description:     "Inactive key test",
		Content:         "Contains " + h.keys["spare"],
		OperationID:     "op-skill-spare",
	})
	mcpExpectRemoteCode(t, err, "skill_secret", "SaveSkill inactive provider key")

	h.setPlan("INACTIVE KEY", func(_role, _lastRole string) (string, *mcpProviderStep) {
		return "Projection removes " + h.keys["spare"], nil
	})
	execInact := mcpSendTurn(t, ctx, proc, originBot, "INACTIVE KEY", "op-inact")
	if s := evLog.waitSettled(t, ctx, proc, execInact); s.State != "completed" {
		t.Fatalf("INACTIVE KEY failed: %+v", s)
	}
	hist, err := proc.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: originBot.CurrentConversationID},
		ProductPageParams:         pi.ProductPageParams{Limit: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	sawRedacted := false
	for _, item := range hist.Items {
		if item.Role == "assistant" && strings.Contains(item.Text, "Projection removes [credential]") {
			sawRedacted = true
		}
		if strings.Contains(item.Text, h.keys["spare"]) {
			t.Fatalf("inactive provider key leaked in history: %+v", item)
		}
	}
	if !sawRedacted {
		t.Fatalf("history missing redacted assistant text: %+v", hist.Items)
	}

	callsBeforeCrash := h.requestCount()
	killOutcome, _ := proc.client.Kill(ctx)
	proc.closed = true
	if !killOutcome.Reaped {
		t.Fatalf("could not reap killed worker: %+v", killOutcome)
	}

	opts.FreshProfile = false
	proc = startProductProcess(t, opts)
	_ = proc.open(t, ctx)
	evLog = newRoutingEventLog()

	recovered, err := proc.client.GetActivity(ctx, pausedSum.ActivityID)
	if err != nil || recovered.State != "needsAttention" || len(recovered.Executions) != 1 || recovered.Executions[0].State != "unknown" {
		t.Fatalf("recovered paused activity = %+v, %v", recovered, err)
	}
	dbPath := filepath.Join(sourceDir, "nagi.db")
	admState := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const row = db.prepare('SELECT state FROM execution_mutation_admissions WHERE execution_id = ?').get(JSON.stringify(process.argv[2]));
db.close();
console.log(row?.state ?? '');
`, question.ExecutionID)
	if admState != "unknown" {
		t.Fatalf("pending admission after crash = %q, want unknown", admState)
	}

	answerParams := pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: pausedSum.ActivityID},
			ExecutionID:           question.ExecutionID,
		},
		ProductText:      pi.ProductText{Text: "answer retained"},
		QuestionID:       question.QuestionID,
		QuestionRevision: question.Revision,
		OperationID:      "op-answer-deleted-provider",
	}
	ansRes, err := proc.client.AnswerQuestion(ctx, answerParams)
	if err != nil || !ansRes.AnswerSaved || ansRes.Delivery != "saved" || ansRes.DeliveryBlocker != pi.ProductCredentialUnavailable {
		t.Fatalf("AnswerQuestion after deleted provider = %+v, %v", ansRes, err)
	}
	ansReplay, err := proc.client.AnswerQuestion(ctx, answerParams)
	if err != nil || !reflect.DeepEqual(ansReplay, ansRes) {
		t.Fatalf("AnswerQuestion duplicate operationId = %+v, %v", ansReplay, err)
	}
	recoveredAfterAns, err := proc.client.GetActivity(ctx, pausedSum.ActivityID)
	if err != nil || recoveredAfterAns.Executions[0].State != "unknown" || recoveredAfterAns.Questions[0].DeliveryBlocker != pi.ProductCredentialUnavailable {
		t.Fatalf("activity after saved answer = %+v, %v", recoveredAfterAns, err)
	}
	_, err = proc.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: pausedSum.ActivityID},
		ExpectedRevision:      recoveredAfterAns.Revision,
		OperationID:           "op-cont-unknown",
	})
	mcpExpectRemoteCode(t, err, "busy", "ContinueActivity on unknown execution must not resurrect")

	if offlineHist, err := proc.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: originBot.CurrentConversationID},
		ProductPageParams:         pi.ProductPageParams{Limit: 10},
	}); err != nil || len(offlineHist.Items) == 0 {
		t.Fatalf("offline History failed: %+v, %v", offlineHist, err)
	}
	if offlineTrace, err := proc.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: pausedSum.ActivityID},
		ExecutionID:           question.ExecutionID,
	}); err != nil || len(offlineTrace.Items) == 0 {
		t.Fatalf("offline ActivityTrace failed: %+v, %v", offlineTrace, err)
	}

	revBeforeRevoke, err := proc.client.Routing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revokedRevs, err := proc.client.RevokeProviderCredential(ctx, pi.ProductProviderTargetParams{
		ProviderID: "dialogue", OperationID: "op-revoke-dialogue",
	})
	if err != nil || !reflect.DeepEqual(revokedRevs, revBeforeRevoke.ProductConnectionRevisions) {
		t.Fatalf("RevokeProviderCredential = %+v, want %+v, err=%v", revokedRevs, revBeforeRevoke.ProductConnectionRevisions, err)
	}
	evLog.drainProviderChange(t, ctx, proc, "revoked")

	conv, err := proc.client.GetConversation(ctx, originBot.CurrentConversationID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: originBot.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "Unsent M4 draft"},
		ExpectedRevision:          conv.Draft.Revision,
		OperationID:               "op-draft-unsent",
	})
	if err != nil {
		t.Fatal(err)
	}
	sendParams := pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: originBot.CurrentConversationID},
		DraftRevision:             draft.Revision,
		OperationID:               "op-send-revoked",
	}
	rejected, err := proc.client.Send(ctx, sendParams)
	if err != nil || rejected.State != "rejected" {
		t.Fatalf("Send with revoked key = %+v, %v", rejected, err)
	}
	var rejPayload struct {
		Code     string `json:"code"`
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal(rejected.Result, &rejPayload); err != nil || rejPayload.Code != "credential_unavailable" || rejPayload.Delivery != "non_dispatched" {
		t.Fatalf("rejected send result = %s, err=%v", string(rejected.Result), err)
	}
	rejectedReplay, err := proc.client.Send(ctx, sendParams)
	if err != nil || !reflect.DeepEqual(rejectedReplay, rejected) {
		t.Fatalf("rejected Send replay = %+v, %v", rejectedReplay, err)
	}
	convAfterReject, err := proc.client.GetConversation(ctx, originBot.CurrentConversationID)
	if err != nil || convAfterReject.Draft == nil || !reflect.DeepEqual(*convAfterReject.Draft, draft) {
		t.Fatalf("revoked Send did not preserve unsent draft: got=%+v want=%+v err=%v", convAfterReject.Draft, draft, err)
	}
	if h.requestCount() != callsBeforeCrash {
		t.Fatalf("deleted/revoked recovery triggered provider calls: before=%d after=%d", callsBeforeCrash, h.requestCount())
	}

	backupPath := filepath.Join(root, "m4-backup.json")
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	backupInfo, err := os.Stat(backupPath)
	if err != nil || backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %+v, %v", backupInfo, err)
	}
	for _, checkPath := range []string{
		backupPath,
		opts.ProfilePath,
		dbPath,
		filepath.Join(sourceDir, "nagi.db-wal"),
	} {
		mcpAssertNoSecretBytes(t, checkPath, h.allSecrets())
	}
	if bytes.Contains(productRead(t, opts.ProfilePath), []byte(`"backup"`)) {
		t.Fatal("normal contacts.json mirror must omit backup envelope")
	}
	proc.shutdown(t, ctx)

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
	installed, err := store.RestoreStoreRecoveryCopyWithRename(copyInfo, targetInfo, os.Rename)
	if err != nil || installed.OutputPath != restoreProfile {
		t.Fatalf("RestoreStoreRecoveryCopyWithRename = %+v, %v", installed, err)
	}
	if _, err := os.Stat(filepath.Join(restoreDir, "connection.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("independent restore directory unexpectedly has connection.json: %v", err)
	}

	restoreOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(restoreDir, "pi"),
		ProfilePath:     restoreProfile,
		CredentialsPath: filepath.Join(restoreDir, "connection.json"),
	}
	for i := range 2 {
		rProc := startProductProcess(t, restoreOpts)
		_ = rProc.open(t, ctx)
		pList, err := rProc.client.ListProviders(ctx)
		if err != nil || len(pList.Accounts) != 4 || pList.ResolvedRoles.Dialogue != nil {
			t.Fatalf("reopen %d ListProviders = %+v, %v", i, pList, err)
		}
		for _, acc := range pList.Accounts {
			if acc.CredentialStatus != "credential_missing" {
				t.Fatalf("reopen %d account %s credentialStatus = %q, want credential_missing", i, acc.ProviderID, acc.CredentialStatus)
			}
		}
		rConv, err := rProc.client.GetConversation(ctx, originBot.CurrentConversationID)
		if err != nil || rConv.Draft == nil || !reflect.DeepEqual(*rConv.Draft, draft) {
			t.Fatalf("reopen %d lost unsent draft: %+v, %v", i, rConv.Draft, err)
		}
		if rAns, err := rProc.client.AnswerQuestion(ctx, answerParams); err != nil || !reflect.DeepEqual(rAns, ansRes) {
			t.Fatalf("reopen %d AnswerQuestion idempotency = %+v, %v", i, rAns, err)
		}
		if rSend, err := rProc.client.Send(ctx, sendParams); err != nil || !reflect.DeepEqual(rSend, rejected) {
			t.Fatalf("reopen %d Send idempotency = %+v, %v", i, rSend, err)
		}
		rProc.shutdown(t, ctx)
	}

	if h.requestCount() != callsBeforeCrash {
		t.Fatalf("independent restore/reopen triggered provider calls: before=%d after=%d", callsBeforeCrash, h.requestCount())
	}

	restoreDB := filepath.Join(restoreDir, "nagi.db")
	restoreCheck := runNodeSQLite(t, nodePath, restoreDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
const cov = db.prepare('SELECT count(*) AS n FROM execution_admission_coverage').get().n;
db.exec('PRAGMA user_version = 11');
db.close();
console.log(JSON.stringify({ v, fk, cov }));
`)
	if restoreCheck != `{"v":10,"fk":0,"cov":0}` {
		t.Fatalf("restored SQLite check = %s", restoreCheck)
	}
	v8Client, startErr := pi.StartProduct(restoreOpts)
	if startErr == nil {
		if _, readyErr := v8Client.Ready(ctx); readyErr == nil {
			if _, openErr := v8Client.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
				t.Fatal("worker admitted SQLite user_version=11")
			}
		}
		_, _ = v8Client.Wait(ctx)
	}
	botCountAfterV8 := runNodeSQLite(t, nodePath, restoreDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const n = db.prepare('SELECT count(*) AS n FROM bots').get().n;
db.close();
console.log(n);
`)
	if botCountAfterV8 != "2" {
		t.Fatalf("v8 refusal mutated restored bots table: count=%s", botCountAfterV8)
	}

	h.assertNoProblems()
}
