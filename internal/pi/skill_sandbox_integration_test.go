//go:build integration

package pi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
)

const sandboxProbeScript = `import { readFileSync, writeFileSync, statSync } from 'node:fs';
import { createConnection } from 'node:net';
import { createSocket } from 'node:dgram';
import { fileURLToPath } from 'node:url';
import { marker } from './helper.mjs';
const inspect = fn => { try { return fn(); } catch (error) { return error.code; } };
export default async function(input, ctx) {
  const socket = await new Promise(resolve => {
    const client = createConnection({ host: '127.0.0.1', port: input.port });
    client.once('error', error => resolve(error.code));
    client.once('connect', () => { client.destroy(); resolve('ALLOWED'); });
  });
  const udp = await new Promise(resolve => {
    const client = createSocket('udp4');
    const end = code => { try { client.close(); } catch {} resolve(code); };
    client.once('error', error => end(error.code));
    client.once('listening', () => end('ALLOWED'));
    try { client.bind(0, '127.0.0.1'); } catch (error) { end(error.code); }
  });
  let nativeHTTP;
  try { await fetch(input.allowed); nativeHTTP = 'ALLOWED'; } catch (error) { nativeHTTP = error.cause?.code ?? error.code; }
  const http = async path => { try { return await (await ctx.fetch(path, { headers: { 'X-From-Skill': 'public' } })).json(); } catch (error) { return error.code; } };
  return {
    marker,
    protected: input.protected.map(path => inspect(() => readFileSync(path, 'utf8'))),
    writeProtected: inspect(() => writeFileSync(input.protectedWrite, 'bad')),
    workspace: inspect(() => readFileSync(input.public, 'utf8')),
    stagedWrite: inspect(() => writeFileSync(fileURLToPath(import.meta.url), 'bad')),
    mode: statSync(fileURLToPath(import.meta.url)).mode & 0o777,
    socket,
    udp,
    nativeHTTP,
    env: Object.keys(process.env),
    mediated: await http(input.allowed),
    otherPort: await http(input.other),
    redirect: await http(input.redirect),
  };
}
`

type sandboxHarness struct {
	t             *testing.T
	server        *httptest.Server
	otherServer   *httptest.Server
	providerCalls atomic.Int64
	mu            sync.Mutex
	scripts       map[string][]mcpProviderStep
	outcomes      map[string]map[string]any
	outbound      []string
	problems      []string
	authHeader    string
	vaultSecret   string
}

func newSandboxHarness(t *testing.T, vaultSecret string) *sandboxHarness {
	t.Helper()
	h := &sandboxHarness{
		t:           t,
		scripts:     make(map[string][]mcpProviderStep),
		outcomes:    make(map[string]map[string]any),
		authHeader:  "Bearer " + vaultSecret,
		vaultSecret: vaultSecret,
	}
	h.otherServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.outbound = append(h.outbound, "other:"+r.URL.Path)
		h.mu.Unlock()
		_, _ = w.Write([]byte("unexpected"))
	}))
	h.server = httptest.NewServer(http.HandlerFunc(h.serveHTTP))
	t.Cleanup(func() {
		h.server.Close()
		h.otherServer.Close()
	})
	return h
}

func (h *sandboxHarness) recordProblem(msg string) {
	h.mu.Lock()
	h.problems = append(h.problems, msg)
	h.mu.Unlock()
}

func (h *sandboxHarness) assertNoProblems() {
	h.t.Helper()
	h.mu.Lock()
	problems := append([]string(nil), h.problems...)
	h.mu.Unlock()
	if len(problems) > 0 {
		h.t.Fatalf("sandbox harness problems: %v", problems)
	}
}

func (h *sandboxHarness) setScript(user string, steps ...mcpProviderStep) {
	h.mu.Lock()
	h.scripts[user] = steps
	h.mu.Unlock()
}

func (h *sandboxHarness) outcome(callID string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.outcomes[callID]
}

func (h *sandboxHarness) outboundPaths() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.outbound...)
}

func (h *sandboxHarness) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		h.mu.Lock()
		h.outbound = append(h.outbound, r.URL.Path)
		h.mu.Unlock()
		if r.Header.Get("Authorization") != h.authHeader {
			h.recordProblem("mediated request authorization mismatch: " + r.Header.Get("Authorization"))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", h.otherServer.URL+"/pivot")
			w.WriteHeader(http.StatusFound)
			return
		}
		if r.URL.Path != "/approved" {
			h.recordProblem("unexpected mediated path: " + r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answer":    "mediated",
			"reflected": h.vaultSecret,
			"env":       r.Header.Get("X-From-Skill"),
		})
		return
	}

	h.providerCalls.Add(1)
	if r.Header.Get("Authorization") != "Bearer "+productKey {
		h.recordProblem("provider authorization mismatch")
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
		h.recordProblem("provider decode: " + err.Error())
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if rawBody, err := json.Marshal(body); err == nil && strings.Contains(string(rawBody), h.vaultSecret) {
		h.recordProblem("vault secret leaked into provider payload")
	}
	tools := make([]string, 0, len(body.Tools))
	var skillTools []string
	for _, item := range body.Tools {
		tools = append(tools, item.Function.Name)
		if strings.HasPrefix(item.Function.Name, "skill_") {
			skillTools = append(skillTools, item.Function.Name)
		}
	}
	if !slices.IsSorted(tools) {
		h.recordProblem(fmt.Sprintf("tools not sorted: %v", tools))
	}
	wantSkillTools := []string{"skill_delete", "skill_exec", "skill_list", "skill_read", "skill_save", "skill_test"}
	if !slices.Equal(skillTools, wantSkillTools) {
		h.recordProblem(fmt.Sprintf("skill tools = %v, want %v", skillTools, wantSkillTools))
	}
	var user string
	for _, msg := range body.Messages {
		if msg.Role == "user" {
			user = decodeChatText(msg.Content)
		}
	}
	last := body.Messages[len(body.Messages)-1]
	h.mu.Lock()
	plan, ok := h.scripts[user]
	h.mu.Unlock()
	if !ok {
		h.recordProblem("unknown provider prompt: " + user)
		http.Error(w, "unknown prompt", http.StatusBadRequest)
		return
	}
	var next *mcpProviderStep
	if last.Role == "user" {
		if len(plan) > 0 {
			next = &plan[0]
		}
	} else if last.Role == "tool" {
		text := decodeChatText(last.Content)
		if strings.Contains(text, h.vaultSecret) {
			h.recordProblem("vault secret leaked in tool result")
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			h.recordProblem("tool result not JSON: " + text)
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
	piChatResponse(w, "Sandbox fixture complete.", "", "", "")
}

type sandboxEventLog struct {
	settled     map[string]pi.ProductExecutionEvent
	skillEvents []pi.ProductSkillChange
}

func newSandboxEventLog() *sandboxEventLog {
	return &sandboxEventLog{settled: make(map[string]pi.ProductExecutionEvent)}
}

func (l *sandboxEventLog) record(t *testing.T, ev pi.ProductEvent) {
	t.Helper()
	if ev.SkillChanged != nil {
		l.skillEvents = append(l.skillEvents, *ev.SkillChanged)
	}
	if ev.Name == "execution.settled" {
		var settled pi.ProductExecutionEvent
		if err := json.Unmarshal(ev.Data, &settled); err != nil {
			t.Fatal(err)
		}
		l.settled[settled.ExecutionID] = settled
	}
}

func (l *sandboxEventLog) waitState(t *testing.T, ctx context.Context, proc *productProcess, executionID, wantState string) {
	t.Helper()
	for {
		if settled, ok := l.settled[executionID]; ok {
			if settled.State != wantState {
				t.Fatalf("execution %s state=%q want=%q error=%q", executionID, settled.State, wantState, settled.Error)
			}
			return
		}
		l.record(t, proc.event(t, ctx))
	}
}

func (l *sandboxEventLog) drainSkillChange(t *testing.T, ctx context.Context, proc *productProcess, skillID, change string) {
	t.Helper()
	has := func() bool {
		for _, ev := range l.skillEvents {
			if ev.SkillID == skillID && ev.Change == change {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(5 * time.Second)
	for !has() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for skill.changed(%s, %s); saw %+v", skillID, change, l.skillEvents)
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

func sandboxFramedDigest(botID, source, skillID string, files map[string]string) string {
	h := sha256.New()
	writeField := func(s string) {
		var lenBuf [8]byte
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(s)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(s))
	}
	principal, _ := json.Marshal([]string{botID, source})
	writeField(string(principal))
	writeField(skillID)
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		writeField(p)
		writeField(files[p])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sandboxParseStdout(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("expected ok=true sandbox output, got %+v", res)
	}
	raw, _ := res["stdout"].(string)
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &out); err != nil {
		t.Fatalf("decode sandbox stdout %q: %v", raw, err)
	}
	return out
}

func sandboxRunStep(kind, id, skillID string, input any) mcpProviderStep {
	args, _ := json.Marshal(map[string]any{"skill_id": skillID, "script": "main", "input": input})
	return mcpProviderStep{Name: "skill_" + kind, ID: id, Args: string(args)}
}

func sandboxWorkerChildren(t *testing.T, stateDir string) []int {
	t.Helper()
	out, err := exec.Command("/bin/ps", "-axo", "pid=,ppid=,args=").Output()
	if err != nil {
		t.Fatal(err)
	}
	type procRow struct {
		pid  int
		ppid int
		args string
	}
	var rows []procRow
	workerPID := 0
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		args := strings.Join(fields[2:], " ")
		rows = append(rows, procRow{pid: pid, ppid: ppid, args: args})
		if strings.Contains(args, stateDir) {
			workerPID = pid
		}
	}
	if workerPID == 0 {
		return nil
	}
	var children []int
	queue := []int{workerPID}
	seen := map[int]bool{workerPID: true}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, row := range rows {
			if row.ppid == parent && !seen[row.pid] {
				seen[row.pid] = true
				children = append(children, row.pid)
				queue = append(queue, row.pid)
			}
		}
	}
	return children
}

func sandboxSetupConnectionAndMcp(t *testing.T, ctx context.Context, proc *productProcess, h *sandboxHarness, workspace string) {
	t.Helper()
	savedConn, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-sb-conn-save",
		Configuration: pi.ProductConnectionInput{
			APIKey: productKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI:          pi.OpenAICompletions,
				BaseURL:          h.server.URL + "/v1",
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
		OperationID:   "op-sb-conn-apply",
		SavedRevision: *savedConn.SavedRevision,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server: pi.ProductMcpServerInput{
			ProductMcpServerConfig: pi.ProductMcpServerConfig{
				ServerID:         "fixture",
				Name:             "fixture",
				Transport:        "http",
				Isolation:        "shared",
				Enabled:          true,
				URL:              h.server.URL + "/approved",
				SecretHeaderKeys: []string{"Authorization"},
			},
		},
		Secrets:     &pi.ProductMcpSecrets{Headers: map[string]*string{"Authorization": mcpStrPtr(h.authHeader)}},
		OperationID: "op-sb-mcp-save",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPiProductSkillSandboxIsolationMediatedBrokerAndHotSwap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	nodePath, workerPath := mcpRequirePinnedWorker(t)
	const vaultSecret = "synthetic-m3-go-vault-secret-38715"
	t.Setenv("SYNTHETIC_PARENT_SECRET", vaultSecret)
	t.Setenv("NAGI_FORCE_SANDBOX_UNAVAILABLE", "0")

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, "profile")
	workspace := root
	alternateDir := filepath.Join(root, "alternate-workspace")
	for _, dir := range []string{sourceDir, alternateDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	publicFile := filepath.Join(workspace, "public.txt")
	if err := os.WriteFile(publicFile, []byte("workspace allowed"), 0o600); err != nil {
		t.Fatal(err)
	}
	aliasSecret := filepath.Join(workspace, "alias-secret")
	if err := os.Symlink(filepath.Join(sourceDir, "connection.json"), aliasSecret); err != nil {
		t.Fatal(err)
	}

	h := newSandboxHarness(t, vaultSecret)
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
	sandboxSetupConnectionAndMcp(t, ctx, proc, h, workspace)

	bot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-sb-bot",
		Profile:     pi.ProductBotProfile{Name: "SandboxBot", Workspace: workspace, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	privManifest := &pi.ProductSkillManifest{
		Version: 1,
		Scripts: map[string]pi.ProductSkillScript{
			"main": {
				Entry:       "scripts/main.mjs",
				Description: "Reviewed security probe",
				TimeoutMS:   5000,
				Permissions: pi.ProductSkillPermissions{
					WorkspaceRead:  true,
					WorkspaceWrite: true,
					NetworkOrigins: []string{h.server.URL},
					CredentialRefs: []string{"mcp:fixture:authorization"},
				},
			},
		},
	}
	privScripts := map[string]string{
		"scripts/main.mjs":   sandboxProbeScript,
		"scripts/helper.mjs": `export const marker = "FIRST";`,
	}
	pureManifest := &pi.ProductSkillManifest{
		Version: 1,
		Scripts: map[string]pi.ProductSkillScript{
			"main": {Entry: "scripts/main.mjs", Description: "Pure compute", TimeoutMS: 5000},
		},
	}
	pureScripts := map[string]string{
		"scripts/main.mjs": `export default input => ({ result: input * 2 });`,
	}

	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "pure"},
		Name:            "Pure Skill",
		Description:     "Pure compute skill",
		Content:         "Documented pure skill.",
		Manifest:        pureManifest,
		Scripts:         pureScripts,
		OperationID:     "op-save-pure",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		Name:            "Priv Skill",
		Description:     "Privileged probe skill",
		Content:         "Documented privileged skill.",
		Manifest:        privManifest,
		Scripts:         privScripts,
		OperationID:     "op-save-priv",
	}); err != nil {
		t.Fatal(err)
	}

	_, err = proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "leak"},
		Name:            "Leak Skill",
		Description:     "Contains vault secret",
		Content:         "Documented leak test.",
		Manifest:        pureManifest,
		Scripts:         map[string]string{"scripts/main.mjs": `export default () => "` + vaultSecret + `";`},
		OperationID:     "op-save-leak",
	})
	mcpExpectRemoteCode(t, err, "skill_secret", "SaveSkill executable script with vault secret")

	v1, err := proc.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"})
	if err != nil {
		t.Fatal(err)
	}
	if v1.ApprovalStatus != "approval_required" || v1.CanonicalWorkspace != workspace || v1.PermissionsDigest == "" {
		t.Fatalf("unexpected v1 skill review: %+v", v1.ProductSkillSummary)
	}
	manifestDisk := string(productRead(t, filepath.Join(sourceDir, "bots", bot.BotID, "skills", "priv", "manifest.json")))
	wantFiles := map[string]string{
		"SKILL.md":           v1.Markdown,
		"manifest.json":      manifestDisk,
		"scripts/main.mjs":   privScripts["scripts/main.mjs"],
		"scripts/helper.mjs": privScripts["scripts/helper.mjs"],
	}
	if !reflect.DeepEqual(v1.Files, wantFiles) {
		t.Fatalf("GetSkill files mismatch:\ngot=%+v\nwant=%+v", v1.Files, wantFiles)
	}
	wantDigest := sandboxFramedDigest(bot.BotID, "private", "priv", wantFiles)
	if v1.ApprovalDigest != wantDigest {
		t.Fatalf("approvalDigest = %q, want canonical framed digest %q", v1.ApprovalDigest, wantDigest)
	}

	serverPort, err := strconv.Atoi(strings.Split(h.server.Listener.Addr().String(), ":")[1])
	if err != nil {
		t.Fatal(err)
	}
	probeInput := map[string]any{
		"port": serverPort,
		"protected": []string{
			filepath.Join(sourceDir, "connection.json"),
			filepath.Join(sourceDir, "mcp-secrets.json"),
			filepath.Join(sourceDir, "nagi.db"),
			filepath.Join(sourceDir, "contacts.json"),
			filepath.Join(sourceDir, ".skill-installation"),
			aliasSecret,
		},
		"protectedWrite": filepath.Join(sourceDir, "forbidden"),
		"public":         publicFile,
		"allowed":        h.server.URL + "/approved",
		"other":          h.otherServer.URL + "/unapproved",
		"redirect":       h.server.URL + "/redirect",
	}

	evLog := newSandboxEventLog()
	h.setScript("TEST_AND_PURE",
		sandboxRunStep("test", "dry-run", "priv", probeInput),
		sandboxRunStep("exec", "priv-before", "priv", probeInput),
		sandboxRunStep("exec", "pure-before", "pure", 21),
		sandboxRunStep("test", "pure-test", "pure", 21),
		sandboxRunStep("exec", "pure-exec", "pure", 21),
	)
	exec1 := mcpSendTurn(t, ctx, proc, bot, "TEST_AND_PURE", "op-t1")
	evLog.waitState(t, ctx, proc, exec1, "completed")

	assertEPERM := func(label string, val any) {
		t.Helper()
		if val != "EPERM" {
			t.Fatalf("%s = %v, want EPERM", label, val)
		}
	}
	dry := sandboxParseStdout(t, h.outcome("dry-run"))
	for i, code := range dry["protected"].([]any) {
		assertEPERM(fmt.Sprintf("dry.protected[%d]", i), code)
	}
	assertEPERM("dry.writeProtected", dry["writeProtected"])
	assertEPERM("dry.stagedWrite", dry["stagedWrite"])
	assertEPERM("dry.workspace", dry["workspace"])
	assertEPERM("dry.socket", dry["socket"])
	assertEPERM("dry.udp", dry["udp"])
	assertEPERM("dry.nativeHTTP", dry["nativeHTTP"])
	if int(dry["mode"].(float64)) != 0o400 {
		t.Fatalf("staged script mode = %o, want 0400", int(dry["mode"].(float64)))
	}
	if len(dry["env"].([]any)) != 0 {
		t.Fatalf("child env not empty: %v", dry["env"])
	}
	for _, key := range []string{"mediated", "otherPort", "redirect"} {
		if dry[key] != "scope_denied" {
			t.Fatalf("unprivileged skill_test %s = %v, want scope_denied", key, dry[key])
		}
	}
	if len(h.outboundPaths()) != 0 {
		t.Fatalf("unprivileged skill_test made outbound HTTP calls: %v", h.outboundPaths())
	}
	if h.outcome("priv-before")["code"] != "approval_required" {
		t.Fatalf("priv exec before approval = %+v", h.outcome("priv-before"))
	}
	privBeforeDetails, _ := h.outcome("priv-before")["details"].(map[string]any)
	if privBeforeDetails["approvalDigest"] != v1.ApprovalDigest {
		t.Fatalf("approval_required details digest = %v, want %s", privBeforeDetails["approvalDigest"], v1.ApprovalDigest)
	}
	if h.outcome("pure-before")["code"] != "approval_required" {
		t.Fatalf("pure exec before test = %+v", h.outcome("pure-before"))
	}
	if int(sandboxParseStdout(t, h.outcome("pure-test"))["result"].(float64)) != 42 {
		t.Fatalf("pure-test output = %+v", h.outcome("pure-test"))
	}
	if int(sandboxParseStdout(t, h.outcome("pure-exec"))["result"].(float64)) != 42 {
		t.Fatalf("pure-exec output = %+v", h.outcome("pure-exec"))
	}
	evLog.drainSkillChange(t, ctx, proc, "pure", "tested")
	pureView, err := proc.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "pure"})
	if err != nil || pureView.ApprovalStatus != "pure_compute_tested" || !slices.Equal(pureView.TestedScripts, []string{"main"}) {
		t.Fatalf("pure skill status after test = %+v, %v", pureView.ProductSkillSummary, err)
	}

	approved, err := proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:   pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		ApprovalDigest:    v1.ApprovalDigest,
		PermissionsDigest: v1.PermissionsDigest,
		OperationID:       "op-approve-priv-v1",
	})
	if err != nil || approved.Change != "approved" {
		t.Fatalf("ApproveSkill = %+v, %v", approved, err)
	}
	evLog.drainSkillChange(t, ctx, proc, "priv", "approved")

	h.setScript("MEDIATED", sandboxRunStep("exec", "approved-run", "priv", probeInput))
	execMed := mcpSendTurn(t, ctx, proc, bot, "MEDIATED", "op-med")
	evLog.waitState(t, ctx, proc, execMed, "completed")
	live := sandboxParseStdout(t, h.outcome("approved-run"))
	for i, code := range live["protected"].([]any) {
		assertEPERM(fmt.Sprintf("live.protected[%d]", i), code)
	}
	assertEPERM("live.writeProtected", live["writeProtected"])
	assertEPERM("live.stagedWrite", live["stagedWrite"])
	assertEPERM("live.socket", live["socket"])
	assertEPERM("live.udp", live["udp"])
	assertEPERM("live.nativeHTTP", live["nativeHTTP"])
	if live["workspace"] != "workspace allowed" {
		t.Fatalf("approved workspace read = %v, want 'workspace allowed'", live["workspace"])
	}
	if len(live["env"].([]any)) != 0 {
		t.Fatalf("approved child env not empty: %v", live["env"])
	}
	mediatedMap, ok := live["mediated"].(map[string]any)
	if !ok || mediatedMap["answer"] != "mediated" || mediatedMap["reflected"] != "[credential]" || mediatedMap["env"] != "public" {
		t.Fatalf("mediated HTTP response mismatch: %+v", live["mediated"])
	}
	if live["otherPort"] != "scope_denied" {
		t.Fatalf("unapproved port returned %v, want scope_denied", live["otherPort"])
	}
	if live["redirect"] != "redirect_denied" {
		t.Fatalf("redirect returned %v, want redirect_denied", live["redirect"])
	}
	if !slices.Equal(h.outboundPaths(), []string{"/approved", "/redirect"}) {
		t.Fatalf("outbound HTTP requests = %v, want [/approved /redirect]", h.outboundPaths())
	}
	if _, err := os.Stat(filepath.Join(sourceDir, "forbidden")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forbidden profile write unexpectedly created file: %v", err)
	}

	v2SaveArgs, _ := json.Marshal(map[string]any{
		"skill_id":    "priv",
		"name":        "Priv Skill",
		"description": "Privileged probe skill",
		"content":     "Documented privileged skill.",
		"manifest":    privManifest,
		"scripts": map[string]string{
			"scripts/main.mjs": `export default () => ({ marker: "SECOND_VERSION_EXECUTED" });`,
		},
	})
	h.setScript("HOT_SWAP",
		mcpProviderStep{Name: "skill_save", ID: "write-v2", Args: string(v2SaveArgs)},
		sandboxRunStep("exec", "blocked-v2", "priv", probeInput),
	)
	execSwap := mcpSendTurn(t, ctx, proc, bot, "HOT_SWAP", "op-swap")
	evLog.waitState(t, ctx, proc, execSwap, "completed")
	if h.outcome("write-v2")["change"] != "saved" {
		t.Fatalf("hot swap save failed: %+v", h.outcome("write-v2"))
	}
	if h.outcome("blocked-v2")["code"] != "approval_required" {
		t.Fatalf("post-approval hot swap was not blocked: %+v", h.outcome("blocked-v2"))
	}
	blockedDetails, _ := h.outcome("blocked-v2")["details"].(map[string]any)
	if blockedDetails["approvalDigest"] == v1.ApprovalDigest {
		t.Fatalf("hot swap digest did not change: %v", blockedDetails["approvalDigest"])
	}
	if !slices.Equal(h.outboundPaths(), []string{"/approved", "/redirect"}) {
		t.Fatalf("blocked hot swap made outbound calls: %v", h.outboundPaths())
	}

	v2, err := proc.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:   pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		ApprovalDigest:    v2.ApprovalDigest,
		PermissionsDigest: v2.PermissionsDigest,
		OperationID:       "op-approve-priv-v2",
	}); err != nil {
		t.Fatal(err)
	}

	wsChangedProfile := bot.Profile
	wsChangedProfile.Workspace = alternateDir
	bot, err = proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			OperationID: "op-ws-alt", BotID: bot.BotID, ExpectedRevision: bot.ProfileRevision,
		},
		Profile:              wsChangedProfile,
		StartNewConversation: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.setScript("WS_CHANGE", sandboxRunStep("exec", "blocked-ws", "priv", probeInput))
	execWS := mcpSendTurn(t, ctx, proc, bot, "WS_CHANGE", "op-ws-turn")
	evLog.waitState(t, ctx, proc, execWS, "completed")
	if h.outcome("blocked-ws")["code"] != "approval_required" {
		t.Fatalf("workspace change did not invalidate approval: %+v", h.outcome("blocked-ws"))
	}
	_, err = proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:   pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		ApprovalDigest:    v2.ApprovalDigest,
		PermissionsDigest: v2.PermissionsDigest,
		OperationID:       "op-approve-stale-ws",
	})
	mcpExpectRemoteCode(t, err, "approval_required", "ApproveSkill with stale workspace permissionsDigest")

	wsRestoredProfile := bot.Profile
	wsRestoredProfile.Workspace = workspace
	bot, err = proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			OperationID: "op-ws-orig", BotID: bot.BotID, ExpectedRevision: bot.ProfileRevision,
		},
		Profile:              wsRestoredProfile,
		StartNewConversation: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		Name:            "Priv Skill",
		Description:     "Privileged probe skill",
		Content:         "Documented privileged skill.",
		Manifest:        privManifest,
		Scripts:         privScripts,
		OperationID:     "op-resave-priv",
	}); err != nil {
		t.Fatal(err)
	}
	v3, err := proc.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:   pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		ApprovalDigest:    v3.ApprovalDigest,
		PermissionsDigest: v3.PermissionsDigest,
		OperationID:       "op-approve-priv-v3",
	}); err != nil {
		t.Fatal(err)
	}
	diskScript := filepath.Join(sourceDir, "bots", bot.BotID, "skills", "priv", "scripts", "main.mjs")
	if err := os.WriteFile(diskScript, []byte(`export default () => ({ marker: "DISK_SWAP_EXECUTED" });`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.setScript("DISK_EDIT", sandboxRunStep("exec", "blocked-disk", "priv", probeInput))
	execDisk := mcpSendTurn(t, ctx, proc, bot, "DISK_EDIT", "op-disk-turn")
	evLog.waitState(t, ctx, proc, execDisk, "completed")
	if h.outcome("blocked-disk")["code"] != "approval_required" {
		t.Fatalf("disk swap did not invalidate approval: %+v", h.outcome("blocked-disk"))
	}

	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		Name:            "Priv Skill",
		Description:     "Privileged probe skill",
		Content:         "Documented privileged skill.",
		Manifest:        privManifest,
		Scripts:         privScripts,
		OperationID:     "op-resave-priv-2",
	}); err != nil {
		t.Fatal(err)
	}
	v4, err := proc.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:   pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		ApprovalDigest:    v4.ApprovalDigest,
		PermissionsDigest: v4.PermissionsDigest,
		OperationID:       "op-approve-priv-v4",
	}); err != nil {
		t.Fatal(err)
	}
	revoked, err := proc.client.RevokeSkillApproval(ctx, pi.ProductSkillRevokeParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		OperationID:     "op-revoke-priv",
	})
	if err != nil || revoked.Change != "revoked" {
		t.Fatalf("RevokeSkillApproval = %+v, %v", revoked, err)
	}
	evLog.drainSkillChange(t, ctx, proc, "priv", "revoked")
	afterRevoke, err := proc.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"})
	if err != nil || afterRevoke.ApprovalStatus != "approval_required" {
		t.Fatalf("status after revoke = %+v, %v", afterRevoke.ProductSkillSummary, err)
	}

	if _, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server: pi.ProductMcpServerInput{
			ProductMcpServerConfig: pi.ProductMcpServerConfig{
				ServerID:         "fixture",
				Name:             "fixture",
				Transport:        "http",
				Isolation:        "shared",
				Enabled:          true,
				URL:              h.server.URL + "/approved",
				SecretHeaderKeys: []string{"Authorization"},
			},
		},
		Secrets:     &pi.ProductMcpSecrets{Headers: map[string]*string{"Authorization": mcpStrPtr("Bearer synthetic-m3-rotated-12037")}},
		OperationID: "op-rotate-mcp-cred",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:   pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		ApprovalDigest:    afterRevoke.ApprovalDigest,
		PermissionsDigest: afterRevoke.PermissionsDigest,
		OperationID:       "op-approve-stale-cred",
	})
	mcpExpectRemoteCode(t, err, "approval_required", "ApproveSkill after MCP credential rotation")

	if _, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server: pi.ProductMcpServerInput{
			ProductMcpServerConfig: pi.ProductMcpServerConfig{
				ServerID:         "fixture",
				Name:             "fixture",
				Transport:        "http",
				Isolation:        "shared",
				Enabled:          true,
				URL:              h.server.URL + "/approved",
				SecretHeaderKeys: []string{"Authorization"},
			},
		},
		Secrets:     &pi.ProductMcpSecrets{Headers: map[string]*string{"Authorization": mcpStrPtr(h.authHeader)}},
		OperationID: "op-restore-mcp-cred",
	}); err != nil {
		t.Fatal(err)
	}
	v5, err := proc.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:   pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		ApprovalDigest:    v5.ApprovalDigest,
		PermissionsDigest: v5.PermissionsDigest,
		OperationID:       "op-approve-priv-v5",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.PromoteSkill(ctx, pi.ProductSkillPromoteParams{
		BotID:         bot.BotID,
		SkillID:       "priv",
		TargetSkillID: "priv-global",
		OperationID:   "op-promote-priv",
	}); err != nil {
		t.Fatal(err)
	}
	globalView, err := proc.client.GetSkill(ctx, pi.ProductSkillKey{Source: "global", BotID: bot.BotID, SkillID: "priv-global"})
	if err != nil || globalView.ApprovalStatus != "approval_required" {
		t.Fatalf("promoted global skill should require new approval: %+v, %v", globalView.ProductSkillSummary, err)
	}

	proc.shutdown(t, ctx)
	h.assertNoProblems()
}

func TestPiProductSkillSandboxFailClosedReapingBackupRestoreAndMigration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	nodePath, workerPath := mcpRequirePinnedWorker(t)
	const vaultSecret = "synthetic-m3-reap-backup-secret-60219"
	t.Setenv("NAGI_FORCE_SANDBOX_UNAVAILABLE", "0")

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, "source")
	workspace := filepath.Join(root, "workspace")
	for _, dir := range []string{sourceDir, workspace} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	h := newSandboxHarness(t, vaultSecret)
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
	sandboxSetupConnectionAndMcp(t, ctx, proc, h, workspace)

	bot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-reap-bot",
		Profile:     pi.ProductBotProfile{Name: "ReapBot", Workspace: workspace, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	pureManifest := &pi.ProductSkillManifest{
		Version: 1,
		Scripts: map[string]pi.ProductSkillScript{
			"main": {Entry: "scripts/main.mjs", Description: "Pure compute", TimeoutMS: 5000},
		},
	}
	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "pure"},
		Name:            "Pure",
		Description:     "Pure compute",
		Content:         "Pure skill.",
		Manifest:        pureManifest,
		Scripts:         map[string]string{"scripts/main.mjs": `export default input => ({ value: input + 1 });`},
		OperationID:     "op-reap-save-pure",
	}); err != nil {
		t.Fatal(err)
	}
	privManifest := &pi.ProductSkillManifest{
		Version: 1,
		Scripts: map[string]pi.ProductSkillScript{
			"main": {
				Entry:       "scripts/main.mjs",
				Description: "Privileged fetch",
				TimeoutMS:   5000,
				Permissions: pi.ProductSkillPermissions{
					WorkspaceRead:  true,
					NetworkOrigins: []string{h.server.URL},
					CredentialRefs: []string{"mcp:fixture:authorization"},
				},
			},
		},
	}
	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		Name:            "Priv",
		Description:     "Privileged skill",
		Content:         "Privileged skill.",
		Manifest:        privManifest,
		Scripts:         map[string]string{"scripts/main.mjs": `export default async (_in, ctx) => (await ctx.fetch("` + h.server.URL + `/approved")).json();`},
		OperationID:     "op-reap-save-priv",
	}); err != nil {
		t.Fatal(err)
	}
	privView, err := proc.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.ApproveSkill(ctx, pi.ProductSkillApproveParams{
		ProductSkillKey:   pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"},
		ApprovalDigest:    privView.ApprovalDigest,
		PermissionsDigest: privView.PermissionsDigest,
		OperationID:       "op-reap-approve-priv",
	}); err != nil {
		t.Fatal(err)
	}

	evLog := newSandboxEventLog()
	h.setScript("PURE_ACTIVATE", sandboxRunStep("test", "pure-act", "pure", 41))
	execPure := mcpSendTurn(t, ctx, proc, bot, "PURE_ACTIVATE", "op-pure-act")
	evLog.waitState(t, ctx, proc, execPure, "completed")
	if int(sandboxParseStdout(t, h.outcome("pure-act"))["value"].(float64)) != 42 {
		t.Fatalf("pure activation output = %+v", h.outcome("pure-act"))
	}

	loopManifest := func(timeoutMS int) *pi.ProductSkillManifest {
		return &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"main": {Entry: "scripts/main.mjs", Description: "Infinite loop", TimeoutMS: timeoutMS},
			},
		}
	}
	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "loop"},
		Name:            "Loop",
		Description:     "Timeout probe",
		Content:         "Infinite loop.",
		Manifest:        loopManifest(800),
		Scripts:         map[string]string{"scripts/main.mjs": `export default () => { while (true) {} };`},
		OperationID:     "op-loop-short",
	}); err != nil {
		t.Fatal(err)
	}
	h.setScript("TIMEOUT", sandboxRunStep("test", "loop-timeout", "loop", nil))
	execTimeout := mcpSendTurn(t, ctx, proc, bot, "TIMEOUT", "op-timeout")
	var timeoutPIDs []int
	mcpWaitUntil(t, 5*time.Second, "timeout script child spawn", func() bool {
		timeoutPIDs = sandboxWorkerChildren(t, opts.StateDir)
		return len(timeoutPIDs) > 0
	})
	evLog.waitState(t, ctx, proc, execTimeout, "completed")
	if h.outcome("loop-timeout")["code"] != "skill_timeout" {
		t.Fatalf("timeout script code = %+v, want skill_timeout", h.outcome("loop-timeout"))
	}
	mcpWaitUntil(t, 5*time.Second, "timeout script children reaped", func() bool {
		for _, pid := range timeoutPIDs {
			if !mcpPIDExited(pid) {
				return false
			}
		}
		return true
	})

	backupPath := filepath.Join(root, "m3-recovery.json")
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	backupBytes := productRead(t, backupPath)
	if bytes.Contains(backupBytes, []byte(vaultSecret)) {
		t.Fatal("backup leaked MCP vault secret")
	}
	mirrorBytes := productRead(t, opts.ProfilePath)
	if bytes.Contains(mirrorBytes, []byte("skillApprovals")) || bytes.Contains(mirrorBytes, []byte(`"backup"`)) {
		t.Fatal("normal contacts.json mirror must exclude backup and skillApprovals")
	}
	var parsedBackup struct {
		Backup struct {
			PrivateSkillBundles []struct {
				OwnerBotID   string `json:"ownerBotId"`
				SkillID      string `json:"skillId"`
				RelativePath string `json:"relativePath"`
				SHA256       string `json:"sha256"`
				Content      string `json:"content"`
				Files        []struct {
					RelativePath string `json:"relativePath"`
					SHA256       string `json:"sha256"`
					Content      string `json:"content"`
				} `json:"files"`
			} `json:"privateSkillBundles"`
			SkillApprovals []json.RawMessage `json:"skillApprovals"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(backupBytes, &parsedBackup); err != nil {
		t.Fatal(err)
	}
	if len(parsedBackup.Backup.SkillApprovals) != 2 {
		t.Fatalf("backup skillApprovals count = %d, want 2", len(parsedBackup.Backup.SkillApprovals))
	}
	for _, b := range parsedBackup.Backup.PrivateSkillBundles {
		if sha256HexBytes([]byte(b.Content)) != b.SHA256 {
			t.Fatalf("backup SKILL.md digest mismatch for %s", b.SkillID)
		}
		for _, f := range b.Files {
			if sha256HexBytes([]byte(f.Content)) != f.SHA256 {
				t.Fatalf("backup %s digest mismatch for %s", f.RelativePath, b.SkillID)
			}
		}
	}
	instPath := filepath.Join(sourceDir, ".skill-installation")
	instInfo, err := os.Stat(instPath)
	if err != nil || instInfo.Mode().Perm() != 0o600 {
		t.Fatalf(".skill-installation stat = %+v, %v", instInfo, err)
	}
	sourceInstID := string(productRead(t, instPath))

	proc.shutdown(t, ctx)

	t.Setenv("NAGI_FORCE_SANDBOX_UNAVAILABLE", "1")
	opts.FreshProfile = false
	forcedProc := startProductProcess(t, opts)
	_ = forcedProc.open(t, ctx)
	forcedLog := newSandboxEventLog()
	h.setScript("FORCED_UNAVAIL",
		sandboxRunStep("test", "force-test", "pure", 2),
		sandboxRunStep("exec", "force-exec", "pure", 2),
	)
	execForced := mcpSendTurn(t, ctx, forcedProc, bot, "FORCED_UNAVAIL", "op-forced")
	forcedLog.waitState(t, ctx, forcedProc, execForced, "completed")
	if h.outcome("force-test")["code"] != "sandbox_unavailable" || h.outcome("force-exec")["code"] != "sandbox_unavailable" {
		t.Fatalf("forced sandbox_unavailable failed: test=%+v exec=%+v", h.outcome("force-test"), h.outcome("force-exec"))
	}
	if children := sandboxWorkerChildren(t, opts.StateDir); len(children) != 0 {
		t.Fatalf("forced unavailable spawned child processes: %v", children)
	}
	forcedProc.shutdown(t, ctx)
	t.Setenv("NAGI_FORCE_SANDBOX_UNAVAILABLE", "0")

	reapProc := startProductProcess(t, opts)
	_ = reapProc.open(t, ctx)
	reapLog := newSandboxEventLog()
	if _, err := reapProc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "loop"},
		Name:            "Loop",
		Description:     "Long loop",
		Content:         "Long loop.",
		Manifest:        loopManifest(30000),
		Scripts:         map[string]string{"scripts/main.mjs": `export default () => { while (true) {} };`},
		OperationID:     "op-loop-long",
	}); err != nil {
		t.Fatal(err)
	}
	h.setScript("CANCEL", sandboxRunStep("test", "loop-cancel", "loop", nil))
	execCancel := mcpSendTurn(t, ctx, reapProc, bot, "CANCEL", "op-cancel")
	var cancelPIDs []int
	mcpWaitUntil(t, 5*time.Second, "cancellable script child spawn", func() bool {
		cancelPIDs = sandboxWorkerChildren(t, opts.StateDir)
		return len(cancelPIDs) > 0
	})
	if _, err := reapProc.client.StopConversation(ctx, pi.ProductConversationStopParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ExecutionID:               execCancel,
		OperationID:               "op-stop-cancel",
	}); err != nil {
		t.Fatal(err)
	}
	reapLog.waitState(t, ctx, reapProc, execCancel, "interrupted")
	mcpWaitUntil(t, 5*time.Second, "cancelled script children reaped", func() bool {
		for _, pid := range cancelPIDs {
			if !mcpPIDExited(pid) {
				return false
			}
		}
		return true
	})

	h.setScript("SHUTDOWN", sandboxRunStep("test", "loop-shutdown", "loop", nil))
	_ = mcpSendTurn(t, ctx, reapProc, bot, "SHUTDOWN", "op-shutdown-turn")
	var shutdownPIDs []int
	mcpWaitUntil(t, 5*time.Second, "shutdown script child spawn", func() bool {
		shutdownPIDs = sandboxWorkerChildren(t, opts.StateDir)
		return len(shutdownPIDs) > 0
	})
	reapProc.shutdown(t, ctx)
	for _, pid := range shutdownPIDs {
		if !mcpPIDExited(pid) {
			t.Fatalf("orphan sandbox child survived shutdown: pid=%d", pid)
		}
	}

	providerBeforeRestore := h.providerCalls.Load()
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
	restoreOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(restoreDir, "pi"),
		ProfilePath:     restoreProfile,
		CredentialsPath: filepath.Join(restoreDir, "connection.json"),
	}
	restored1 := startProductProcess(t, restoreOpts)
	_ = restored1.open(t, ctx)
	restoredList, err := restored1.client.ListSkills(ctx, bot.BotID)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, s := range restoredList.Private {
		statuses[s.SkillID] = s.ApprovalStatus
	}
	if statuses["pure"] != "pure_compute_tested" || statuses["priv"] != "approval_required" {
		t.Fatalf("cross-installation restore approvals = %v, want pure=pure_compute_tested priv=approval_required", statuses)
	}
	restoredPriv, err := restored1.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"})
	if err != nil || restoredPriv.ApprovalDigest == "" || restoredPriv.PermissionsDigest == "" || len(restoredPriv.Scripts["main"].Permissions.CredentialRefs) != 1 {
		t.Fatalf("restored priv review fields missing: %+v, %v", restoredPriv.ProductSkillSummary, err)
	}
	restoredInstID := string(productRead(t, filepath.Join(restoreDir, ".skill-installation")))
	if restoredInstID == sourceInstID {
		t.Fatalf("restored installation reused source installation ID %s", sourceInstID)
	}
	if _, err := os.Stat(filepath.Join(restoreDir, "mcp-secrets.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restored directory unexpectedly had mcp-secrets.json: %v", err)
	}
	for _, b := range parsedBackup.Backup.PrivateSkillBundles {
		skillDir := filepath.Join(restoreDir, "bots", b.OwnerBotID, "skills", b.SkillID)
		if string(productRead(t, filepath.Join(skillDir, b.RelativePath))) != b.Content {
			t.Fatalf("restored SKILL.md mismatch for %s", b.SkillID)
		}
		for _, f := range b.Files {
			if string(productRead(t, filepath.Join(skillDir, f.RelativePath))) != f.Content {
				t.Fatalf("restored %s mismatch for %s", f.RelativePath, b.SkillID)
			}
		}
	}
	restored1.shutdown(t, ctx)

	restored2 := startProductProcess(t, restoreOpts)
	_ = restored2.open(t, ctx)
	reopenedPure, err := restored2.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "pure"})
	if err != nil || reopenedPure.ApprovalStatus != "pure_compute_tested" {
		t.Fatalf("second reopen pure status = %+v, %v", reopenedPure.ProductSkillSummary, err)
	}
	reopenedPriv, err := restored2.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "priv"})
	if err != nil || reopenedPriv.ApprovalStatus != "approval_required" {
		t.Fatalf("second reopen priv status = %+v, %v", reopenedPriv.ProductSkillSummary, err)
	}
	restored2.shutdown(t, ctx)

	if h.providerCalls.Load() != providerBeforeRestore {
		t.Fatalf("restore/reopen triggered provider calls: before=%d after=%d", providerBeforeRestore, h.providerCalls.Load())
	}

	migDir := filepath.Join(root, "migrated-v5-v6")
	if err := os.MkdirAll(migDir, 0o700); err != nil {
		t.Fatal(err)
	}
	restoredMirror := productRead(t, restoreProfile)
	if err := os.WriteFile(filepath.Join(migDir, "contacts.json"), restoredMirror, 0o600); err != nil {
		t.Fatal(err)
	}
	migDB := filepath.Join(migDir, "nagi.db")
	if err := os.WriteFile(migDB, productRead(t, filepath.Join(restoreDir, "nagi.db")), 0o600); err != nil {
		t.Fatal(err)
	}
	runNodeSQLite(t, nodePath, migDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec('DROP TABLE activity_triage_accounting; ALTER TABLE activities DROP COLUMN updated_at; DROP TABLE activity_progress; DROP TABLE skill_approval_requests; ALTER TABLE activities DROP COLUMN acknowledged_at; ALTER TABLE activities DROP COLUMN acknowledged_execution_id; ALTER TABLE activities DROP COLUMN closed_at; DROP TABLE execution_mutation_admissions; DROP TABLE execution_admission_coverage; DROP TABLE activity_escalations; DROP TABLE provider_accounts; ALTER TABLE activities DROP COLUMN routing_json; DROP TABLE skill_approvals; PRAGMA user_version = 5;');
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
	_ = migProc.open(t, ctx)
	migProc.shutdown(t, ctx)

	if !bytes.Equal(productRead(t, migOpts.ProfilePath), restoredMirror) {
		t.Fatal("v5->v8 migration altered canonical contacts.json mirror")
	}
	migState := runNodeSQLite(t, nodePath, migDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
const hasTable = !!db.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name='skill_approvals'").get();
db.exec('PRAGMA user_version = 10');
db.close();
console.log(JSON.stringify({ v, fk, hasTable }));
`)
	if migState != `{"v":9,"fk":0,"hasTable":true}` {
		t.Fatalf("v5->v9 SQLite migration state = %s", migState)
	}
	v8Client, startErr := pi.StartProduct(migOpts)
	if startErr == nil {
		if _, readyErr := v8Client.Ready(ctx); readyErr == nil {
			if _, openErr := v8Client.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
				t.Fatal("worker admitted SQLite user_version=10")
			}
		}
		_, _ = v8Client.Wait(ctx)
	}
	botCountAfterV7 := runNodeSQLite(t, nodePath, migDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const n = db.prepare('SELECT COUNT(*) AS n FROM bots').get().n;
db.close();
console.log(n);
`)
	if botCountAfterV7 != "1" {
		t.Fatalf("v7 refusal mutated bots table: count=%s", botCountAfterV7)
	}

	h.assertNoProblems()
}
