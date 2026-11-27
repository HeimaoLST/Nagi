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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
)

type cp3Gate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newCP3Gate() *cp3Gate {
	return &cp3Gate{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (g *cp3Gate) wait(ctx context.Context) {
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
	case <-ctx.Done():
	}
}

func (g *cp3Gate) awaitEntered(t *testing.T, label string) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for gate %s", label)
	}
}

func (g *cp3Gate) open() {
	select {
	case <-g.release:
	default:
		close(g.release)
	}
}

type cp3MCPServer struct {
	server *httptest.Server
	mu     sync.Mutex
	gates  map[string]*cp3Gate
}

func newCP3MCPServer(t *testing.T) *cp3MCPServer {
	t.Helper()
	s := &cp3MCPServer{gates: map[string]*cp3Gate{}}
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.server.Close)
	return s
}

func (s *cp3MCPServer) registerGate(key string) *cp3Gate {
	g := newCP3Gate()
	s.mu.Lock()
	s.gates[key] = g
	s.mu.Unlock()
	return g
}

func (s *cp3MCPServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	var req struct {
		ID     any    `json:"id"`
		Method string `json:"method"`
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
			Name            string `json:"name"`
			Arguments       struct {
				Key string `json:"key"`
			} `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		pv := req.Params.ProtocolVersion
		if pv == "" {
			pv = "2025-03-26"
		}
		result = map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "cp3-fixture", "version": "1"},
		}
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
		return
	case "tools/list":
		result = map[string]any{
			"tools": []map[string]any{{
				"name":        "hold",
				"description": "Wait on a controlled local gate",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"key": map[string]any{"type": "string"}},
					"required":   []string{"key"},
				},
			}},
		}
	case "tools/call":
		s.mu.Lock()
		gate := s.gates[req.Params.Arguments.Key]
		s.mu.Unlock()
		if gate != nil {
			gate.wait(r.Context())
		}
		if r.Context().Err() != nil {
			return
		}
		result = map[string]any{
			"content": []map[string]string{{"type": "text", "text": "MCP finished " + req.Params.Arguments.Key}},
		}
	default:
		http.Error(w, "unsupported method", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result":  result,
	})
}

type cp3ParsedSteer struct {
	ID      string
	Digest  string
	RawJSON string
	Text    string
	Mats    []pi.ProductMaterial
	Index   int
}

var cp3SteerEnvelopeRe = regexp.MustCompile(`(?s)^<activity_steer id="([0-9a-f-]+)" digest="([a-f0-9]{64})">\n(.+)\n</activity_steer>$`)

func cp3UnescapeXML(s string) string {
	r := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&apos;", "'",
	)
	return r.Replace(s)
}

func cp3ExtractSteers(t *testing.T, msgs []cp2WireMsg) []cp3ParsedSteer {
	t.Helper()
	var out []cp3ParsedSteer
	for i, m := range msgs {
		if m.Role != "user" || !strings.HasPrefix(m.Content, "<activity_steer id=") {
			continue
		}
		match := cp3SteerEnvelopeRe.FindStringSubmatch(m.Content)
		if len(match) != 4 {
			t.Fatalf("malformed steer envelope: %q", m.Content)
		}
		rawJSON := cp3UnescapeXML(match[3])
		sum := sha256.Sum256([]byte(rawJSON))
		if hex.EncodeToString(sum[:]) != match[2] {
			t.Fatalf("steer envelope digest mismatch: got %s want %x payload=%s", match[2], sum, rawJSON)
		}
		var payload struct {
			Text      string               `json:"text"`
			Materials []pi.ProductMaterial `json:"materials"`
		}
		if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
			t.Fatalf("decode steer envelope payload %q: %v", rawJSON, err)
		}
		out = append(out, cp3ParsedSteer{
			ID:      match[1],
			Digest:  match[2],
			RawJSON: rawJSON,
			Text:    payload.Text,
			Mats:    payload.Materials,
			Index:   i,
		})
	}
	return out
}

func cp3FirstUserBase(msgs []cp2WireMsg) string {
	for _, m := range msgs {
		if m.Role == "user" && !strings.HasPrefix(m.Content, "<activity_steer id=") {
			return stripCP2Board(m.Content)
		}
	}
	return ""
}

func cp3HasUserText(msgs []cp2WireMsg, needle string) bool {
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Content, needle) {
			return true
		}
	}
	return false
}

func cp3WaitSteerState(t *testing.T, ctx context.Context, p *cp1Process, steerID, wantState string) pi.ProductActivitySteer {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		got, err := p.client.SteerStatus(ctx, pi.ProductSteerStatusParams{SteerID: steerID})
		if err == nil && got.State == wantState {
			return got
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(15 * time.Millisecond):
		}
	}
	got, err := p.client.SteerStatus(ctx, pi.ProductSteerStatusParams{SteerID: steerID})
	t.Fatalf("timed out waiting for steer %s state %q (last=%+v err=%v)", steerID, wantState, got, err)
	return pi.ProductActivitySteer{}
}

func TestPiControlPlaneCP3LiveSteeringOrderingEnvelopeAndForegroundAction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	nodePath, workerPath := cp1WorkerPaths(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "cp3-steer-secret-K7mV3qR9"

	mcpSrv := newCP3MCPServer(t)
	batch1Gate := mcpSrv.registerGate("batch-1")
	batch2Gate := mcpSrv.registerGate("batch-2")
	contGate := mcpSrv.registerGate("cont-1")
	fgGate := mcpSrv.registerGate("fg-hold")

	var mu sync.Mutex
	var botBID string
	var fgSteerArgs string
	var fgSteerResult string
	var batchActReqs []cp2Request
	var contActReqs []cp2Request
	var fgActReqs []cp2Request
	var seq atomic.Int64
	nextCall := func(prefix string) string {
		return fmt.Sprintf("%s_%d", prefix, seq.Add(1))
	}

	provider := newCP2Provider(t, secret, func(req cp2Request) cp2Reply {
		mu.Lock()
		targetB := botBID
		actArgs := fgSteerArgs
		mu.Unlock()

		if req.IsActivity {
			for _, m := range req.Messages {
				if strings.Contains(m.Content, secret) {
					t.Errorf("activity request leaked secret: %q", m.Content)
				}
			}
			firstBase := cp3FirstUserBase(req.Messages)
			switch {
			case strings.Contains(firstBase, "CP3_BATCH_STEER"):
				if cp3HasUserText(req.Messages, "CP3_CONT_SECOND_EXEC") {
					mu.Lock()
					contActReqs = append(contActReqs, req)
					contCall := len(contActReqs)
					mu.Unlock()
					if contCall == 1 {
						return cp2Reply{Calls: []cp2ToolCall{{ID: "mcp-c1", Name: "mcp__fixture__hold", Args: `{"key":"cont-1"}`}}}
					}
					return cp2Reply{Text: "Second execution finished after steer."}
				}
				mu.Lock()
				batchActReqs = append(batchActReqs, req)
				callNum := len(batchActReqs)
				mu.Unlock()
				if callNum == 1 {
					return cp2Reply{Calls: []cp2ToolCall{
						{ID: "mcp-b1", Name: "mcp__fixture__hold", Args: `{"key":"batch-1"}`},
						{ID: "mcp-b2", Name: "mcp__fixture__hold", Args: `{"key":"batch-2"}`},
					}}
				}
				return cp2Reply{Text: fmt.Sprintf("Batch turn %d settled.", callNum)}
			case strings.Contains(firstBase, "CP3_FG_STEER_TARGET"):
				mu.Lock()
				fgActReqs = append(fgActReqs, req)
				fgCall := len(fgActReqs)
				mu.Unlock()
				if fgCall == 1 {
					return cp2Reply{Calls: []cp2ToolCall{{ID: "mcp-fg1", Name: "mcp__fixture__hold", Args: `{"key":"fg-hold"}`}}}
				}
				return cp2Reply{Text: "Foreground steered execution complete."}
			default:
				return cp2Reply{Text: "Done."}
			}
		}

		switch {
		case strings.HasPrefix(req.BaseUserText, "CP3_DEL_"):
			if req.LastRole == "user" {
				goal := strings.TrimPrefix(req.BaseUserText, "CP3_DEL_")
				args, _ := json.Marshal(map[string]any{
					"key":         "k_" + goal,
					"goal":        goal,
					"assignee_id": targetB,
				})
				return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("del"), Name: "activity_delegate", Args: string(args)}}}
			}
			return cp2Reply{Text: "Delegated."}
		case strings.HasPrefix(req.BaseUserText, "CP3_FG_CALL") || strings.HasPrefix(req.BaseUserText, "Steer "):
			if req.LastRole == "user" {
				return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("actsteer"), Name: "activity_action", Args: actArgs}}}
			}
			mu.Lock()
			fgSteerResult = req.LastContent
			mu.Unlock()
			return cp2Reply{Text: "Foreground action complete."}
		default:
			return cp2Reply{Text: "OK."}
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
	cp2ConfigureConnection(t, ctx, proc.client, pi.OpenAICompletions, provider.server.URL+"/v1", secret, ws, 4)

	if _, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server: pi.ProductMcpServerInput{
			ProductMcpServerConfig: pi.ProductMcpServerConfig{
				ServerID:  "fixture",
				Name:      "fixture",
				Transport: "http",
				URL:       mcpSrv.server.URL + "/mcp",
				Isolation: "shared",
				Enabled:   true,
			},
		},
		OperationID: "cp3-save-mcp",
	}); err != nil {
		t.Fatalf("SaveMcpServer: %v", err)
	}

	botA, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-bot-a",
		Profile: pi.ProductBotProfile{
			Name: "OriginA", Role: "Coordinator", Purpose: "CP3 steer origin",
			Instructions: "Coordinate.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	botB, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-bot-b",
		Profile: pi.ProductBotProfile{
			Name: "WorkerB", Role: "Worker", Purpose: "CP3 steer target",
			Instructions: "Execute MCP tools.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
			McpBindings: []pi.ProductMcpBinding{{ServerID: "fixture", Policy: "all"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	botC, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-bot-c",
		Profile: pi.ProductBotProfile{
			Name: "ForeignC", Role: "Outsider", Purpose: "CP3 ACL test",
			Instructions: "Isolated.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = botB.BotID
	mu.Unlock()

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-batch", "CP3_DEL_CP3_BATCH_STEER", nil); settled.State != "completed" {
		t.Fatalf("delegate batch settled = %+v", settled)
	}
	batchAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP3_BATCH_STEER")
	exec1ID := batchAct.Executions[len(batchAct.Executions)-1].ExecutionID

	batch1Gate.awaitEntered(t, "batch-1")
	batch2Gate.awaitEntered(t, "batch-2")

	liveAct, err := proc.client.GetActivity(ctx, batchAct.ActivityID)
	if err != nil {
		t.Fatal(err)
	}
	staleRev := liveAct.Revision + 7
	_, staleErr := proc.client.SteerActivity(ctx, pi.ProductSteerActivityParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: batchAct.ActivityID},
			ExecutionID:           exec1ID,
		},
		ProductText:      pi.ProductText{Text: "Stale revision probe"},
		OperationID:      "cp3-op-stale",
		ExpectedRevision: &staleRev,
	})
	var remoteErr *pi.RemoteError
	if !errors.As(staleErr, &remoteErr) || remoteErr.Code != "stale_revision" {
		t.Fatalf("expected stale_revision, got %v", staleErr)
	}

	steer1Text := "/help prioritize <xml> & \"漢字\" 'q' with " + secret
	steer1Mat := pi.ProductMaterial{
		ID:       "mat-cp3-1",
		Revision: 2,
		Name:     "Résumé <材料>",
		Text:     "Material & secret " + secret,
		Source:   "note-1",
	}
	wantRev := liveAct.Revision
	steer1Params := pi.ProductSteerActivityParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: batchAct.ActivityID},
			ExecutionID:           exec1ID,
		},
		ProductText:      pi.ProductText{Text: steer1Text, Materials: []pi.ProductMaterial{steer1Mat}},
		OperationID:      "cp3-op-steer-1",
		ExpectedRevision: &wantRev,
	}
	s1, err := proc.client.SteerActivity(ctx, steer1Params)
	if err != nil || s1.State != "pending_enqueue" || s1.Revision != 1 || s1.SteerID == "" || s1.OperationID != "cp3-op-steer-1" {
		t.Fatalf("SteerActivity #1 = %+v, %v", s1, err)
	}
	s1Dup, err := proc.client.SteerActivity(ctx, steer1Params)
	if err != nil || s1Dup != s1 {
		t.Fatalf("duplicate SteerActivity mismatch: got=%+v want=%+v err=%v", s1Dup, s1, err)
	}
	conflictParams := steer1Params
	conflictParams.Text = "Conflicting text for same op"
	_, conflictErr := proc.client.SteerActivity(ctx, conflictParams)
	if !errors.As(conflictErr, &remoteErr) || remoteErr.Code != "operation_conflict" {
		t.Fatalf("expected operation_conflict, got %v", conflictErr)
	}
	_, mismatchErr := proc.client.SteerStatus(ctx, pi.ProductSteerStatusParams{
		SteerID:     s1.SteerID,
		OperationID: "cp3-wrong-op",
	})
	if !errors.As(mismatchErr, &remoteErr) || remoteErr.Code != "not_found" {
		t.Fatalf("expected not_found on mismatched SteerStatus ids, got %v", mismatchErr)
	}

	s2, err := proc.client.SteerActivity(ctx, pi.ProductSteerActivityParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: batchAct.ActivityID},
			ExecutionID:           exec1ID,
		},
		ProductText: pi.ProductText{Text: "Second ordered steer 漢字 & </activity_steer>"},
		OperationID: "cp3-op-steer-2",
	})
	if err != nil || s2.State != "pending_enqueue" || s2.Revision != 1 || s2.SteerID == s1.SteerID {
		t.Fatalf("SteerActivity #2 = %+v, %v", s2, err)
	}

	cp3WaitSteerState(t, ctx, proc, s1.SteerID, "queued")
	cp3WaitSteerState(t, ctx, proc, s2.SteerID, "queued")

	batch1Gate.open()
	batch2Gate.open()

	if settled := proc.waitSettled(t, ctx, exec1ID); settled.State != "completed" {
		t.Fatalf("batch execution settled = %+v", settled)
	}

	s1Final := cp3WaitSteerState(t, ctx, proc, s1.SteerID, "delivered")
	s2Final := cp3WaitSteerState(t, ctx, proc, s2.SteerID, "delivered")
	if s1Final.Revision < 2 || s2Final.Revision < 2 {
		t.Fatalf("delivered steer revisions too low: s1=%+v s2=%+v", s1Final, s2Final)
	}
	s1ByOp, err := proc.client.SteerStatus(ctx, pi.ProductSteerStatusParams{OperationID: "cp3-op-steer-1"})
	if err != nil || s1ByOp != s1Final {
		t.Fatalf("SteerStatus by operationId mismatch: got=%+v want=%+v err=%v", s1ByOp, s1Final, err)
	}
	opReceipt, err := proc.client.Operation(ctx, "cp3-op-steer-1")
	if err != nil || opReceipt.State != "accepted" {
		t.Fatalf("Operation(cp3-op-steer-1) = %+v, %v", opReceipt, err)
	}
	var origSteer pi.ProductActivitySteer
	if err := json.Unmarshal(opReceipt.Result, &origSteer); err != nil {
		t.Fatalf("decode immutable operation receipt: %v", err)
	}
	if origSteer.State != "pending_enqueue" || origSteer.Revision != 1 || origSteer.SteerID != s1.SteerID {
		t.Fatalf("operation.get mutated original steering receipt: %+v", origSteer)
	}

	gotAct, err := proc.client.GetActivity(ctx, batchAct.ActivityID)
	if err != nil || len(gotAct.Steers) != 2 || gotAct.Steers[0].SteerID != s1.SteerID || gotAct.Steers[1].SteerID != s2.SteerID ||
		gotAct.Steers[0].State != "delivered" || gotAct.Steers[1].State != "delivered" {
		t.Fatalf("GetActivity.Steers mismatch: %+v, %v", gotAct.Steers, err)
	}
	var sawChangedDelivered bool
	proc.mu.Lock()
	for _, ev := range proc.events {
		if ev.Name == "activity.changed" && ev.ActivityChanged != nil && ev.ActivityChanged.ActivityID == batchAct.ActivityID {
			for _, st := range ev.ActivityChanged.Steers {
				if st.SteerID == s1.SteerID && st.State == "delivered" {
					sawChangedDelivered = true
				}
			}
		}
	}
	proc.mu.Unlock()
	if !sawChangedDelivered {
		t.Fatal("activity.changed did not emit delivered steer summary")
	}

	mu.Lock()
	reqs := append([]cp2Request(nil), batchActReqs...)
	mu.Unlock()
	if len(reqs) < 3 {
		t.Fatalf("expected at least 3 provider requests for batch + 2 steers, got %d", len(reqs))
	}
	req2Steers := cp3ExtractSteers(t, reqs[1].Messages)
	if len(req2Steers) != 1 || req2Steers[0].ID != s1.SteerID {
		t.Fatalf("expected steer #1 in request 2, got %+v", req2Steers)
	}
	tool1Idx, tool2Idx := -1, -1
	for i, m := range reqs[1].Messages {
		if m.ToolCallID == "mcp-b1" {
			tool1Idx = i
		}
		if m.ToolCallID == "mcp-b2" {
			tool2Idx = i
		}
	}
	if tool1Idx < 0 || tool2Idx < 0 || req2Steers[0].Index <= tool1Idx || req2Steers[0].Index <= tool2Idx {
		t.Fatalf("steer #1 did not arrive strictly after both blocked tool results: tool1=%d tool2=%d steer=%d", tool1Idx, tool2Idx, req2Steers[0].Index)
	}
	wantRedactedText := "/help prioritize <xml> & \"漢字\" 'q' with [credential]"
	if req2Steers[0].Text != wantRedactedText || len(req2Steers[0].Mats) != 1 || req2Steers[0].Mats[0].ID != "mat-cp3-1" ||
		req2Steers[0].Mats[0].Text != "Material & secret [credential]" || req2Steers[0].Mats[0].Name != "Résumé <材料>" {
		t.Fatalf("steer #1 payload/redaction mismatch: %+v", req2Steers[0])
	}
	lastReqSteers := cp3ExtractSteers(t, reqs[len(reqs)-1].Messages)
	if len(lastReqSteers) != 2 || lastReqSteers[0].ID != s1.SteerID || lastReqSteers[1].ID != s2.SteerID ||
		lastReqSteers[1].Text != "Second ordered steer 漢字 & </activity_steer>" {
		t.Fatalf("concurrent steer admission order mismatch: %+v", lastReqSteers)
	}

	contReceipt, err := proc.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: batchAct.ActivityID},
		ProductText:           pi.ProductText{Text: "CP3_CONT_SECOND_EXEC"},
		ExpectedRevision:      gotAct.Revision,
		OperationID:           "cp3-cont-exec2",
	})
	if err != nil || contReceipt.Execution == nil {
		t.Fatalf("ContinueActivity: %+v, %v", contReceipt, err)
	}
	exec2ID := contReceipt.Execution.ExecutionID
	contGate.awaitEntered(t, "cont-1")
	s3, err := proc.client.SteerActivity(ctx, pi.ProductSteerActivityParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: batchAct.ActivityID},
			ExecutionID:           exec2ID,
		},
		ProductText: pi.ProductText{Text: steer1Text},
		OperationID: "cp3-op-steer-3",
	})
	if err != nil || s3.SteerID == s1.SteerID {
		t.Fatalf("identical text on second execution must get distinct steerId: s1=%s s3=%+v err=%v", s1.SteerID, s3, err)
	}
	cp3WaitSteerState(t, ctx, proc, s3.SteerID, "queued")
	contGate.open()
	if settled := proc.waitSettled(t, ctx, exec2ID); settled.State != "completed" {
		t.Fatalf("second execution settled = %+v", settled)
	}
	cp3WaitSteerState(t, ctx, proc, s3.SteerID, "delivered")

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-fg", "CP3_DEL_CP3_FG_STEER_TARGET", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	fgAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP3_FG_STEER_TARGET")
	fgExecID := fgAct.Executions[len(fgAct.Executions)-1].ExecutionID
	fgGate.awaitEntered(t, "fg-hold")

	fgArgsRaw, _ := json.Marshal(map[string]any{
		"activity_id":  fgAct.ActivityID,
		"execution_id": fgExecID,
		"action":       "steer",
		"text":         "Adjust scope <fg> & 漢字",
	})
	mu.Lock()
	fgSteerArgs = string(fgArgsRaw)
	mu.Unlock()

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "fg-unrel", "CP3_FG_CALL_UNRELATED", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	gotUnrel := fgSteerResult
	mu.Unlock()
	if !strings.Contains(gotUnrel, `"code":"confirmation_required"`) {
		t.Fatalf("unrelated foreground steer must fail with confirmation_required, got %s", gotUnrel)
	}

	exactFGPrompt := fmt.Sprintf("Steer %s %s: Adjust scope <fg> & 漢字", fgAct.ActivityID, fgExecID)
	if settled := cp1SendTurn(t, ctx, proc, botC.CurrentConversationID, "fg-foreign", exactFGPrompt, nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	gotForeign := fgSteerResult
	mu.Unlock()
	if !strings.Contains(gotForeign, `"code":"scope_denied"`) {
		t.Fatalf("foreign bot foreground steer must fail with scope_denied, got %s", gotForeign)
	}

	fgMat := pi.ProductMaterial{
		ID:       "fg-mat-1",
		Revision: 1,
		Name:     "FG Note",
		Text:     "Forwarded draft material with " + secret,
	}
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "fg-exact", exactFGPrompt, []pi.ProductMaterial{fgMat}); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	gotFGAccepted := fgSteerResult
	mu.Unlock()
	var fgReceipt pi.ProductActivitySteer
	if err := json.Unmarshal([]byte(gotFGAccepted), &fgReceipt); err != nil || fgReceipt.State != "pending_enqueue" || fgReceipt.SteerID == "" {
		t.Fatalf("foreground exact steer failed: raw=%s err=%v", gotFGAccepted, err)
	}
	cp3WaitSteerState(t, ctx, proc, fgReceipt.SteerID, "queued")
	fgGate.open()
	if settled := proc.waitSettled(t, ctx, fgExecID); settled.State != "completed" {
		t.Fatalf("fg steered activity settled = %+v", settled)
	}
	cp3WaitSteerState(t, ctx, proc, fgReceipt.SteerID, "delivered")

	mu.Lock()
	fgCaptured := append([]cp2Request(nil), fgActReqs...)
	mu.Unlock()
	if len(fgCaptured) < 2 {
		t.Fatalf("expected >=2 requests on fg steered activity, got %d", len(fgCaptured))
	}
	fgSteers := cp3ExtractSteers(t, fgCaptured[len(fgCaptured)-1].Messages)
	if len(fgSteers) != 1 || fgSteers[0].ID != fgReceipt.SteerID || fgSteers[0].Text != "Adjust scope <fg> & 漢字" ||
		len(fgSteers[0].Mats) != 1 || fgSteers[0].Mats[0].ID != "fg-mat-1" || fgSteers[0].Mats[0].Text != "Forwarded draft material with [credential]" {
		t.Fatalf("foreground steered envelope/materials mismatch: %+v", fgSteers)
	}

	proc.shutdown(t, ctx)
	provider.assertNoProblems()
}

func TestPiControlPlaneCP3RejectionsStopDisposalAndWaiterGuards(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	nodePath, workerPath := cp1WorkerPaths(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "cp3-guards-secret-G5nW8cP2"

	mcpSrv := newCP3MCPServer(t)
	capGate := mcpSrv.registerGate("cap-hold")
	stopGate := newCP3Gate()
	revGate := mcpSrv.registerGate("rev-hold")

	triageEntered := make(chan struct{}, 1)
	triageRelease := make(chan struct{})

	var mu sync.Mutex
	var botBID string
	var hotContReqs []cp2Request
	var coldContReqs []cp2Request
	var seq atomic.Int64
	nextCall := func(prefix string) string {
		return fmt.Sprintf("%s_%d", prefix, seq.Add(1))
	}

	provider := newCP2Provider(t, secret, func(req cp2Request) cp2Reply {
		if req.IsTriage {
			select {
			case triageEntered <- struct{}{}:
			default:
			}
			select {
			case <-triageRelease:
			case <-req.Ctx.Done():
				return cp2Reply{}
			}
			return cp2Reply{Text: `{"decision":"escalate_user"}`}
		}
		mu.Lock()
		targetB := botBID
		mu.Unlock()
		if req.IsActivity {
			for _, m := range req.Messages {
				if strings.Contains(m.Content, secret) {
					t.Errorf("activity request leaked secret: %q", m.Content)
				}
			}
			firstBase := cp3FirstUserBase(req.Messages)
			switch {
			case strings.Contains(firstBase, "CP3_IDLE_DONE"):
				return cp2Reply{Text: "Settled quickly."}
			case strings.Contains(firstBase, "CP3_HUMAN_WAIT"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{
						ID:   nextCall("ask"),
						Name: "activity_ask",
						Args: cp2FormattingAskArgs(map[string]any{"requires_user_decision": true}),
					}}}
				}
				return cp2Reply{Text: "Answered."}
			case strings.Contains(firstBase, "CP3_TRIAGE_WAIT"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{
						ID:   nextCall("ask"),
						Name: "activity_ask",
						Args: cp2FormattingAskArgs(nil),
					}}}
				}
				return cp2Reply{Text: "Triage done."}
			case strings.Contains(firstBase, "CP3_APPROVAL_WAIT"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{
						ID:   nextCall("sk"),
						Name: "skill_exec",
						Args: `{"skill_id":"cp3-ws-skill","source":"private","script":"main"}`,
					}}}
				}
				return cp2Reply{Text: "Approval denied."}
			case strings.Contains(firstBase, "CP3_CAP_TEST"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("cap"), Name: "mcp__fixture__hold", Args: `{"key":"cap-hold"}`}}}
				}
				return cp2Reply{Text: "Cap test done."}
			case strings.Contains(firstBase, "CP3_STOP_DISPOSAL"):
				if cp3HasUserText(req.Messages, "CP3_COLD_CONT") {
					mu.Lock()
					coldContReqs = append(coldContReqs, req)
					mu.Unlock()
					return cp2Reply{Text: "Cold continuation clean."}
				}
				if cp3HasUserText(req.Messages, "CP3_HOT_CONT") {
					mu.Lock()
					hotContReqs = append(hotContReqs, req)
					mu.Unlock()
					return cp2Reply{Text: "Hot continuation clean."}
				}
				if req.LastRole == "user" {
					stopGate.wait(req.Ctx)
					return cp2Reply{Text: "Should be interrupted."}
				}
				return cp2Reply{Text: "Should be interrupted."}
			case strings.Contains(firstBase, "CP3_REVOKE_STEER"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("rev"), Name: "mcp__fixture__hold", Args: `{"key":"rev-hold"}`}}}
				}
				return cp2Reply{Text: "Revoked."}
			default:
				return cp2Reply{Text: "Done."}
			}
		}
		if strings.HasPrefix(req.BaseUserText, "CP3_DEL_") {
			if req.LastRole == "user" {
				goal := strings.TrimPrefix(req.BaseUserText, "CP3_DEL_")
				args, _ := json.Marshal(map[string]any{
					"key":         "k_" + goal,
					"goal":        goal,
					"assignee_id": targetB,
				})
				return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("del"), Name: "activity_delegate", Args: string(args)}}}
			}
			return cp2Reply{Text: "Delegated."}
		}
		return cp2Reply{Text: "OK."}
	})
	provider.addForbiddenSecret(secret)

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
	cp2ConfigureConnection(t, ctx, proc.client, pi.OpenAICompletions, provider.server.URL+"/v1", secret, ws, 4)

	if _, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server: pi.ProductMcpServerInput{
			ProductMcpServerConfig: pi.ProductMcpServerConfig{
				ServerID: "fixture", Name: "fixture", Transport: "http",
				URL: mcpSrv.server.URL + "/mcp", Isolation: "shared", Enabled: true,
			},
		},
		OperationID: "cp3-g-mcp",
	}); err != nil {
		t.Fatal(err)
	}

	botA, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-g-a",
		Profile: pi.ProductBotProfile{
			Name: "GuardA", Role: "Coord", Purpose: "Guard test",
			Instructions: "Coordinate.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	botB, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-g-b",
		Profile: pi.ProductBotProfile{
			Name: "GuardB", Role: "Worker", Purpose: "Guard worker",
			Instructions: "Work.", Workspace: ws, ToolPreset: pi.ToolPresetWorkspace,
			McpBindings: []pi.ProductMcpBinding{{ServerID: "fixture", Policy: "all"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = botB.BotID
	mu.Unlock()

	if _, err := proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: botB.BotID, OperationID: "cp3-g-r1"},
		Text:                   "formatting: Markdown",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: botB.BotID, OperationID: "cp3-g-r2"},
		Text:                   "Prefer formatting: Markdown",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: botB.BotID, SkillID: "cp3-ws-skill"},
		Name:            "Approval Skill", Description: "Needs approval", Content: "Workspace write skill.",
		Manifest: &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"main": {
					Entry: "scripts/main.mjs", Description: "Write",
					Permissions: pi.ProductSkillPermissions{WorkspaceRead: true, WorkspaceWrite: true},
				},
			},
		},
		Scripts:     map[string]string{"scripts/main.mjs": "export default () => ({ ok: true });"},
		OperationID: "cp3-g-skill",
	}); err != nil {
		t.Fatal(err)
	}

	expectSteerErr := func(actID, execID, opID, wantCode string) {
		t.Helper()
		_, err := proc.client.SteerActivity(ctx, pi.ProductSteerActivityParams{
			ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
				ProductActivityParams: pi.ProductActivityParams{ActivityID: actID},
				ExecutionID:           execID,
			},
			ProductText: pi.ProductText{Text: "Rejected probe"},
			OperationID: opID,
		})
		var remoteErr *pi.RemoteError
		if !errors.As(err, &remoteErr) || remoteErr.Code != wantCode {
			t.Fatalf("SteerActivity(%s) = %v, want %s", opID, err, wantCode)
		}
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-idle", "CP3_DEL_CP3_IDLE_DONE", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	idleAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP3_IDLE_DONE")
	idleExecID := idleAct.Executions[len(idleAct.Executions)-1].ExecutionID
	if settled := proc.waitSettled(t, ctx, idleExecID); settled.State != "completed" {
		t.Fatal(settled)
	}
	expectSteerErr(idleAct.ActivityID, idleExecID, "cp3-rej-idle", "not_running")

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-hwait", "CP3_DEL_CP3_HUMAN_WAIT", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	hwAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP3_HUMAN_WAIT")
	hwQ := cp2WaitQuestion(t, ctx, proc, hwAct.ActivityID, "open")
	expectSteerErr(hwAct.ActivityID, hwQ.ExecutionID, "cp3-rej-human", "not_running")
	if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: hwAct.ActivityID},
			ExecutionID:           hwQ.ExecutionID,
		},
		QuestionID: hwQ.QuestionID, QuestionRevision: hwQ.Revision, SelectedOptionID: "markdown", OperationID: "cp3-ans-hw",
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, hwQ.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-twait", "CP3_DEL_CP3_TRIAGE_WAIT", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	twAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP3_TRIAGE_WAIT")
	select {
	case <-triageEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for triage_pending")
	}
	twPending := cp2WaitQuestion(t, ctx, proc, twAct.ActivityID, "triage_pending")
	expectSteerErr(twAct.ActivityID, twPending.ExecutionID, "cp3-rej-triage", "not_running")
	close(triageRelease)
	twOpen := cp2WaitQuestion(t, ctx, proc, twAct.ActivityID, "open")
	if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: twAct.ActivityID},
			ExecutionID:           twOpen.ExecutionID,
		},
		QuestionID: twOpen.QuestionID, QuestionRevision: twOpen.Revision, SelectedOptionID: "markdown", OperationID: "cp3-ans-tw",
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, twOpen.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-await", "CP3_DEL_CP3_APPROVAL_WAIT", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	awAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP3_APPROVAL_WAIT")
	awDigest := cp2WaitDigest(t, ctx, proc, func(d pi.ProductActivityDigest) bool {
		return d.ActivityID == awAct.ActivityID && d.Kind == "approval_required"
	})
	awDet, err := proc.client.GetActivity(ctx, awAct.ActivityID)
	if err != nil || len(awDet.ApprovalRequests) != 1 {
		t.Fatalf("approval request missing: %+v, %v", awDet, err)
	}
	expectSteerErr(awAct.ActivityID, awDigest.ExecutionID, "cp3-rej-approval", "not_running")
	if _, err := proc.client.DenySkillApprovalRequest(ctx, pi.ProductSkillDenyApprovalRequestParams{
		RequestID: awDet.ApprovalRequests[0].RequestID, RequestRevision: awDet.ApprovalRequests[0].Revision,
		Reason: "Denied for guard test.", OperationID: "cp3-deny-aw",
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, awDigest.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-cap", "CP3_DEL_CP3_CAP_TEST", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	capAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP3_CAP_TEST")
	capExecID := capAct.Executions[len(capAct.Executions)-1].ExecutionID
	capGate.awaitEntered(t, "cap-hold")
	for i := 1; i <= 8; i++ {
		if _, err := proc.client.SteerActivity(ctx, pi.ProductSteerActivityParams{
			ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
				ProductActivityParams: pi.ProductActivityParams{ActivityID: capAct.ActivityID},
				ExecutionID:           capExecID,
			},
			ProductText: pi.ProductText{Text: fmt.Sprintf("Cap steer %d", i)},
			OperationID: fmt.Sprintf("cp3-cap-op-%d", i),
		}); err != nil {
			t.Fatalf("steer %d within cap failed: %v", i, err)
		}
	}
	expectSteerErr(capAct.ActivityID, capExecID, "cp3-cap-op-9", "steer_capacity")
	if _, err := proc.client.StopActivity(ctx, pi.ProductActivityStopParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: capAct.ActivityID},
			ExecutionID:           capExecID,
		},
		OperationID: "cp3-stop-cap",
	}); err != nil {
		t.Fatal(err)
	}
	capGate.open()
	if settled := proc.waitSettled(t, ctx, capExecID); settled.State != "interrupted" {
		t.Fatal(settled)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-stop", "CP3_DEL_CP3_STOP_DISPOSAL", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	stopAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP3_STOP_DISPOSAL")
	stopExecID := stopAct.Executions[len(stopAct.Executions)-1].ExecutionID
	stopGate.awaitEntered(t, "stop-hold")

	queuedSteer, err := proc.client.SteerActivity(ctx, pi.ProductSteerActivityParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: stopAct.ActivityID},
			ExecutionID:           stopExecID,
		},
		ProductText: pi.ProductText{Text: "Must never leak into continuation"},
		OperationID: "cp3-stop-queued-steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	cp3WaitSteerState(t, ctx, proc, queuedSteer.SteerID, "queued")

	if _, err := proc.client.StopActivity(ctx, pi.ProductActivityStopParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: stopAct.ActivityID},
			ExecutionID:           stopExecID,
		},
		OperationID: "cp3-stop-disposal-op",
	}); err != nil {
		t.Fatal(err)
	}
	stopGate.open()
	if settled := proc.waitSettled(t, ctx, stopExecID); settled.State != "interrupted" {
		t.Fatalf("stopped disposal activity settled = %+v", settled)
	}
	disposed := cp3WaitSteerState(t, ctx, proc, queuedSteer.SteerID, "not_applied")
	if disposed.Reason != "queue_removed" {
		t.Fatalf("expected queue_removed disposal reason, got %+v", disposed)
	}

	stopAfter, err := proc.client.GetActivity(ctx, stopAct.ActivityID)
	if err != nil {
		t.Fatal(err)
	}
	hotCont, err := proc.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: stopAct.ActivityID},
		ProductText:           pi.ProductText{Text: "CP3_HOT_CONT"},
		ExpectedRevision:      stopAfter.Revision,
		OperationID:           "cp3-hot-cont-op",
	})
	if err != nil || hotCont.Execution == nil {
		t.Fatalf("hot ContinueActivity: %+v, %v", hotCont, err)
	}
	if settled := proc.waitSettled(t, ctx, hotCont.Execution.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	hotReqs := append([]cp2Request(nil), hotContReqs...)
	mu.Unlock()
	if len(hotReqs) != 1 || len(cp3ExtractSteers(t, hotReqs[0].Messages)) != 0 {
		t.Fatalf("stale steer leaked into hot continuation: reqs=%d", len(hotReqs))
	}

	proc.shutdown(t, ctx)

	opts.FreshProfile = false
	proc2 := startCP1Process(t, opts)
	_ = proc2.open(t, ctx)
	coldBefore, err := proc2.client.GetActivity(ctx, stopAct.ActivityID)
	if err != nil {
		t.Fatal(err)
	}
	coldCont, err := proc2.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: stopAct.ActivityID},
		ProductText:           pi.ProductText{Text: "CP3_COLD_CONT"},
		ExpectedRevision:      coldBefore.Revision,
		OperationID:           "cp3-cold-cont-op",
	})
	if err != nil || coldCont.Execution == nil {
		t.Fatalf("cold ContinueActivity: %+v, %v", coldCont, err)
	}
	if settled := proc2.waitSettled(t, ctx, coldCont.Execution.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	coldReqs := append([]cp2Request(nil), coldContReqs...)
	mu.Unlock()
	if len(coldReqs) != 1 || len(cp3ExtractSteers(t, coldReqs[0].Messages)) != 0 {
		t.Fatalf("stale steer leaked into cold continuation: reqs=%d", len(coldReqs))
	}
	proc2.shutdown(t, ctx)

	revDir := filepath.Join(root, "rev-steer")
	if err := os.MkdirAll(revDir, 0o700); err != nil {
		t.Fatal(err)
	}
	revOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(revDir, "pi"),
		ProfilePath:     filepath.Join(revDir, "contacts.json"),
		CredentialsPath: filepath.Join(revDir, "connection.json"),
		FreshProfile:    true,
	}
	revProc := startCP1Process(t, revOpts)
	_ = revProc.open(t, ctx)
	savedP, err := revProc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{
			Account: pi.ProductProviderAccount{
				ProviderID: "rev-local", Name: "Local", WireAPI: pi.OpenAICompletions,
				BaseURL: provider.server.URL + "/v1", DefaultModel: productModel,
				ContextTokens: 262144, OutputTokens: 4096, Enabled: true,
			},
			APIKey: secret,
		},
		OperationID: "cp3-rev-save",
	})
	if err != nil || savedP.SavedRevision == nil {
		t.Fatal(err)
	}
	if _, err := revProc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		SavedRevision: *savedP.SavedRevision, OperationID: "cp3-rev-apply",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := revProc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server: pi.ProductMcpServerInput{
			ProductMcpServerConfig: pi.ProductMcpServerConfig{
				ServerID: "fixture", Name: "fixture", Transport: "http",
				URL: mcpSrv.server.URL + "/mcp", Isolation: "shared", Enabled: true,
			},
		},
		OperationID: "cp3-rev-mcp",
	}); err != nil {
		t.Fatal(err)
	}
	rA, err := revProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-rev-a",
		Profile: pi.ProductBotProfile{
			Name: "RevA", Role: "Coord", Purpose: "Rev test",
			Instructions: "Coordinate.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rB, err := revProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-rev-b",
		Profile: pi.ProductBotProfile{
			Name: "RevB", Role: "Worker", Purpose: "Rev worker",
			Instructions: "Work.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
			McpBindings: []pi.ProductMcpBinding{{ServerID: "fixture", Policy: "all"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = rB.BotID
	mu.Unlock()
	if settled := cp1SendTurn(t, ctx, revProc, rA.CurrentConversationID, "del-rev", "CP3_DEL_CP3_REVOKE_STEER", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	revAct := cp2FindActivityByGoal(t, ctx, revProc, rA.BotID, "CP3_REVOKE_STEER")
	revExecID := revAct.Executions[len(revAct.Executions)-1].ExecutionID
	revGate.awaitEntered(t, "rev-hold")
	if _, err := revProc.client.RevokeProviderCredential(ctx, pi.ProductProviderTargetParams{
		ProviderID: "rev-local", OperationID: "cp3-rev-revoke",
	}); err != nil {
		t.Fatal(err)
	}
	_, revSteerErr := revProc.client.SteerActivity(ctx, pi.ProductSteerActivityParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: revAct.ActivityID},
			ExecutionID:           revExecID,
		},
		ProductText: pi.ProductText{Text: "Steer after revoke"},
		OperationID: "cp3-steer-after-revoke",
	})
	var remoteErr *pi.RemoteError
	if !errors.As(revSteerErr, &remoteErr) || remoteErr.Code != "credential_unavailable" {
		t.Fatalf("steer after credential revocation = %v, want credential_unavailable", revSteerErr)
	}
	if _, err := revProc.client.StopActivity(ctx, pi.ProductActivityStopParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: revAct.ActivityID},
			ExecutionID:           revExecID,
		},
		OperationID: "cp3-rev-stop",
	}); err != nil {
		t.Fatal(err)
	}
	revGate.open()
	if settled := revProc.waitSettled(t, ctx, revExecID); settled.State != "interrupted" {
		t.Fatal(settled)
	}
	revProc.shutdown(t, ctx)
	provider.assertNoProblems()
}

func TestPiControlPlaneCP3CrashRecoveryEscalationExclusionAndV10Backup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	nodePath, workerPath := cp1WorkerPaths(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "cp3-rec-secret-R6pX4vM9"
	const lateSecret = "cp3-late-backup-key-8842"

	mcpSrv := newCP3MCPServer(t)
	crashGate := mcpSrv.registerGate("crash-hold")
	escSteerGate := newCP3Gate()
	backupSteerGate := newCP3Gate()

	var mu sync.Mutex
	var botBID string
	var unsteeredUtilFails int
	var steeredCalls int
	var backupSteerCalls int
	var reasoningModelsSeen []string
	var wbTwoRowBoard string
	var seq atomic.Int64
	nextCall := func(prefix string) string {
		return fmt.Sprintf("%s_%d", prefix, seq.Add(1))
	}

	provider := newCP2Provider(t, secret, func(req cp2Request) cp2Reply {
		mu.Lock()
		targetB := botBID
		mu.Unlock()
		if req.IsActivity {
			for _, m := range req.Messages {
				if strings.Contains(m.Content, secret) {
					t.Errorf("activity request leaked secret: %q", m.Content)
				}
			}
			firstBase := cp3FirstUserBase(req.Messages)
			switch {
			case strings.Contains(firstBase, "CP3_CRASH_STEER"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("cr"), Name: "mcp__fixture__hold", Args: `{"key":"crash-hold"}`}}}
				}
				return cp2Reply{Text: "Should not finish after SIGKILL."}
			case strings.Contains(firstBase, "CP3_TRANSPORT_FAIL"):
				return cp2Reply{HTTPStatus: 500, RawError: "controlled transport utility failure"}
			case strings.Contains(firstBase, "CP3_UNSTEERED_FAIL"):
				mu.Lock()
				unsteeredUtilFails++
				n := unsteeredUtilFails
				mu.Unlock()
				if n == 1 {
					return cp2Reply{Text: "truncated utility output", FinishReason: "length"}
				}
				mu.Lock()
				reasoningModelsSeen = append(reasoningModelsSeen, "escalated")
				mu.Unlock()
				return cp2Reply{Text: "Escalated reasoning recovery succeeded."}
			case strings.Contains(firstBase, "CP3_STEERED_FAIL"):
				mu.Lock()
				steeredCalls++
				n := steeredCalls
				mu.Unlock()
				if n == 1 {
					escSteerGate.wait(req.Ctx)
					return cp2Reply{Text: "Utility step 1 complete before steer consumption."}
				}
				if len(cp3ExtractSteers(t, req.Messages)) == 0 {
					t.Errorf("steered utility request 2 missing consumed steer envelope")
				}
				return cp2Reply{Text: "truncated after steer consumption", FinishReason: "length"}
			case strings.Contains(firstBase, "CP3_BACKUP_STEER"):
				mu.Lock()
				backupSteerCalls++
				n := backupSteerCalls
				mu.Unlock()
				if n == 1 {
					backupSteerGate.wait(req.Ctx)
					return cp2Reply{Text: "Backup steer step 1 done."}
				}
				return cp2Reply{Text: "Backup steer completed cleanly."}
			case strings.Contains(firstBase, "CP3_WB_ROW_"):
				if req.LastRole == "user" {
					args, _ := json.Marshal(map[string]any{
						"text": strings.Repeat("Whiteboard exact two-row budget summary text ", 4),
					})
					calls := make([]cp2ToolCall, 0, 5)
					for j := 0; j < 5; j++ {
						calls = append(calls, cp2ToolCall{
							ID:   fmt.Sprintf("wbq_%s_%d_%d", strings.Repeat("x", 32), seq.Add(1), j),
							Name: "activity_ask",
							Args: string(args),
						})
					}
					return cp2Reply{Calls: calls}
				}
				return cp2Reply{Text: "WB answered."}
			default:
				return cp2Reply{Text: "Done."}
			}
		}
		if strings.HasPrefix(req.BaseUserText, "CP3_DEL_ROUTED_") {
			if req.LastRole == "user" {
				goal := strings.TrimPrefix(req.BaseUserText, "CP3_DEL_ROUTED_")
				args, _ := json.Marshal(map[string]any{
					"key":         "kr_" + goal,
					"goal":        goal,
					"assignee_id": targetB,
					"kind":        "quick",
					"complexity":  "light",
				})
				return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("delr"), Name: "activity_delegate", Args: string(args)}}}
			}
			return cp2Reply{Text: "Delegated routed."}
		}
		if strings.HasPrefix(req.BaseUserText, "CP3_DEL_") {
			if req.LastRole == "user" {
				goal := strings.TrimPrefix(req.BaseUserText, "CP3_DEL_")
				args, _ := json.Marshal(map[string]any{
					"key":         "k_" + goal,
					"goal":        goal,
					"assignee_id": targetB,
				})
				return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("del"), Name: "activity_delegate", Args: string(args)}}}
			}
			return cp2Reply{Text: "Delegated."}
		}
		if strings.HasPrefix(req.BaseUserText, "CP3_WB_CHECK") {
			mu.Lock()
			wbTwoRowBoard = req.BoardText
			mu.Unlock()
			return cp2Reply{Text: "Whiteboard checked."}
		}
		return cp2Reply{Text: "OK."}
	})
	provider.addForbiddenSecret(secret)

	crashDir := filepath.Join(root, "crash-steer")
	if err := os.MkdirAll(crashDir, 0o700); err != nil {
		t.Fatal(err)
	}
	crashOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(crashDir, "pi"),
		ProfilePath:     filepath.Join(crashDir, "contacts.json"),
		CredentialsPath: filepath.Join(crashDir, "connection.json"),
		FreshProfile:    true,
	}
	cProc := startCP1Process(t, crashOpts)
	_ = cProc.open(t, ctx)
	cp2ConfigureConnection(t, ctx, cProc.client, pi.OpenAICompletions, provider.server.URL+"/v1", secret, ws, 4)
	if _, err := cProc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server: pi.ProductMcpServerInput{
			ProductMcpServerConfig: pi.ProductMcpServerConfig{
				ServerID: "fixture", Name: "fixture", Transport: "http",
				URL: mcpSrv.server.URL + "/mcp", Isolation: "shared", Enabled: true,
			},
		},
		OperationID: "cp3-cr-mcp",
	}); err != nil {
		t.Fatal(err)
	}
	cA, err := cProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-cr-a",
		Profile: pi.ProductBotProfile{
			Name: "CrashA", Role: "Coord", Purpose: "Crash",
			Instructions: "Coordinate.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cB, err := cProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-cr-b",
		Profile: pi.ProductBotProfile{
			Name: "CrashB", Role: "Worker", Purpose: "Crash worker",
			Instructions: "Work.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
			McpBindings: []pi.ProductMcpBinding{{ServerID: "fixture", Policy: "all"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = cB.BotID
	mu.Unlock()

	if settled := cp1SendTurn(t, ctx, cProc, cA.CurrentConversationID, "del-cr", "CP3_DEL_CP3_CRASH_STEER", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	crAct := cp2FindActivityByGoal(t, ctx, cProc, cA.BotID, "CP3_CRASH_STEER")
	crExecID := crAct.Executions[len(crAct.Executions)-1].ExecutionID
	crashGate.awaitEntered(t, "crash-hold")
	crSteer, err := cProc.client.SteerActivity(ctx, pi.ProductSteerActivityParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: crAct.ActivityID},
			ExecutionID:           crExecID,
		},
		ProductText: pi.ProductText{Text: "Crash while queued"},
		OperationID: "cp3-crash-steer-op",
	})
	if err != nil {
		t.Fatal(err)
	}
	cp3WaitSteerState(t, ctx, cProc, crSteer.SteerID, "queued")

	callsBeforeKill := provider.totalCalls.Load()
	_, _ = cProc.client.Kill(ctx)
	cProc.closed = true
	crashGate.open()

	crashOpts.FreshProfile = false
	reCrash := startCP1Process(t, crashOpts)
	_ = reCrash.open(t, ctx)
	if provider.totalCalls.Load() != callsBeforeKill {
		t.Fatalf("SIGKILL reopen replayed provider requests: before=%d after=%d", callsBeforeKill, provider.totalCalls.Load())
	}
	recSteer, err := reCrash.client.SteerStatus(ctx, pi.ProductSteerStatusParams{SteerID: crSteer.SteerID})
	if err != nil || recSteer.State != "unknown" || recSteer.Reason != "worker_lost" || recSteer.Revision != 3 {
		t.Fatalf("reopened crashed steer = %+v, %v", recSteer, err)
	}
	recAct, err := reCrash.client.GetActivity(ctx, crAct.ActivityID)
	if err != nil || recAct.State != "needsAttention" || len(recAct.Executions) != 1 || recAct.Executions[0].State != "unknown" {
		t.Fatalf("reopened crashed activity = %+v, %v", recAct, err)
	}
	reCrash.shutdown(t, ctx)

	escDir := filepath.Join(root, "escalation-m4")
	if err := os.MkdirAll(escDir, 0o700); err != nil {
		t.Fatal(err)
	}
	escOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(escDir, "pi"),
		ProfilePath:     filepath.Join(escDir, "contacts.json"),
		CredentialsPath: filepath.Join(escDir, "connection.json"),
		FreshProfile:    true,
	}
	escProc := startCP1Process(t, escOpts)
	_ = escProc.open(t, ctx)
	var lastSavedRev uint64
	for _, role := range []string{"dialogue", "utility", "reasoning"} {
		res, err := escProc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
			ProductProviderTestParams: pi.ProductProviderTestParams{
				Account: pi.ProductProviderAccount{
					ProviderID: role, Name: role, WireAPI: pi.OpenAICompletions,
					BaseURL: provider.server.URL + "/v1", DefaultModel: "cp3-" + role,
					ContextTokens: 262144, OutputTokens: 4096, Enabled: true,
				},
				APIKey: secret,
			},
			OperationID: "cp3-esc-save-" + role,
		})
		if err != nil || res.SavedRevision == nil {
			t.Fatalf("SaveProvider(%s): %+v, %v", role, res, err)
		}
		lastSavedRev = *res.SavedRevision
	}
	if _, err := escProc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		SavedRevision: lastSavedRev, OperationID: "cp3-esc-apply",
	}); err != nil {
		t.Fatal(err)
	}
	eA, err := escProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-esc-a",
		Profile: pi.ProductBotProfile{
			Name: "EscA", Role: "Coord", Purpose: "Escalation",
			Instructions: "Coordinate.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	eB, err := escProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-esc-b",
		Profile: pi.ProductBotProfile{
			Name: "EscB", Role: "Worker", Purpose: "Escalation worker",
			Instructions: "Work.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = eB.BotID
	mu.Unlock()

	if settled := cp1SendTurn(t, ctx, escProc, eA.CurrentConversationID, "del-tresc", "CP3_DEL_ROUTED_CP3_TRANSPORT_FAIL", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	trAct := cp2FindActivityByGoal(t, ctx, escProc, eA.BotID, "CP3_TRANSPORT_FAIL")
	if settled := escProc.waitSettled(t, ctx, trAct.Executions[0].ExecutionID); settled.State != "failed" {
		t.Fatalf("transport utility execution settled = %+v, want failed", settled)
	}
	time.Sleep(300 * time.Millisecond)
	trDone, err := escProc.client.GetActivity(ctx, trAct.ActivityID)
	if err != nil || trDone.State != "needsAttention" || len(trDone.Executions) != 1 {
		t.Fatalf("transport 5xx utility failure must NOT escalate: %+v, %v", trDone, err)
	}

	if settled := cp1SendTurn(t, ctx, escProc, eA.CurrentConversationID, "del-unesc", "CP3_DEL_ROUTED_CP3_UNSTEERED_FAIL", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	unAct := cp2FindActivityByGoal(t, ctx, escProc, eA.BotID, "CP3_UNSTEERED_FAIL")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		got, err := escProc.client.GetActivity(ctx, unAct.ActivityID)
		if err == nil && got.State == "completed" && len(got.Executions) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	unDone, err := escProc.client.GetActivity(ctx, unAct.ActivityID)
	if err != nil || unDone.State != "completed" || len(unDone.Executions) != 2 {
		t.Fatalf("unsteered utility failure did not escalate once: %+v, %v", unDone, err)
	}

	if settled := cp1SendTurn(t, ctx, escProc, eA.CurrentConversationID, "del-stesc", "CP3_DEL_ROUTED_CP3_STEERED_FAIL", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	stAct := cp2FindActivityByGoal(t, ctx, escProc, eA.BotID, "CP3_STEERED_FAIL")
	stExecID := stAct.Executions[len(stAct.Executions)-1].ExecutionID
	escSteerGate.awaitEntered(t, "esc-steer")
	stReceipt, err := escProc.client.SteerActivity(ctx, pi.ProductSteerActivityParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: stAct.ActivityID},
			ExecutionID:           stExecID,
		},
		ProductText: pi.ProductText{Text: "Steer blocking M4 utility escalation"},
		OperationID: "cp3-st-esc-op",
	})
	if err != nil {
		t.Fatal(err)
	}
	cp3WaitSteerState(t, ctx, escProc, stReceipt.SteerID, "queued")
	escSteerGate.open()
	if settled := escProc.waitSettled(t, ctx, stExecID); settled.State != "failed" {
		t.Fatalf("steered utility execution settled = %+v, want failed", settled)
	}
	time.Sleep(300 * time.Millisecond)
	stDone, err := escProc.client.GetActivity(ctx, stAct.ActivityID)
	if err != nil || stDone.State != "needsAttention" || len(stDone.Executions) != 1 {
		t.Fatalf("steered utility failure must NOT escalate: %+v, %v", stDone, err)
	}
	escDB := filepath.Join(escDir, "nagi.db")
	escSQL := runNodeSQLite(t, nodePath, escDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const trEsc = db.prepare('SELECT count(*) AS n FROM activity_escalations WHERE activity_id = ?').get(JSON.stringify(process.argv[2])).n;
const unEsc = db.prepare('SELECT count(*) AS n FROM activity_escalations WHERE activity_id = ?').get(JSON.stringify(process.argv[3])).n;
const stEsc = db.prepare('SELECT count(*) AS n FROM activity_escalations WHERE activity_id = ?').get(JSON.stringify(process.argv[4])).n;
const stMut = db.prepare("SELECT count(*) AS n FROM execution_mutation_admissions WHERE execution_id = ? AND tool_class = 'mutating'").get(JSON.stringify(process.argv[5])).n;
db.close();
console.log(JSON.stringify({ trEsc, unEsc, stEsc, stMut }));
`, trAct.ActivityID, unAct.ActivityID, stAct.ActivityID, stExecID)
	if escSQL != `{"trEsc":0,"unEsc":1,"stEsc":0,"stMut":0}` {
		t.Fatalf("M4 escalation exclusion SQLite state = %s, want trEsc=0 unEsc=1 stEsc=0 stMut=0", escSQL)
	}
	escProc.shutdown(t, ctx)

	v9Dir := filepath.Join(root, "v9-migration")
	if err := os.MkdirAll(v9Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	v9Opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(v9Dir, "pi"),
		ProfilePath:     filepath.Join(v9Dir, "contacts.json"),
		CredentialsPath: filepath.Join(v9Dir, "connection.json"),
	}
	writeProductFixture(t, v9Opts.CredentialsPath, map[string]any{
		"version": 2, "credential_source": "src-v9", "model": productModel,
		"context_tokens": 262144, "output_tokens": 4096, "working_directory": ws, "max_active_tasks": 2,
		"sources": map[string]any{"src-v9": map[string]string{"api": "openai-chat-completions", "endpoint": provider.server.URL + "/v1", "api_key": secret}},
	})
	v9DB := filepath.Join(v9Dir, "nagi.db")
	runNodeSQLite(t, nodePath, v9DB, `
const { createHash } = require('node:crypto');
const { writeFileSync } = require('node:fs');
const { DatabaseSync } = require('node:sqlite');
const [dbPath, mirrorPath, ws, baseURL] = process.argv.slice(1);
const canonical = value => value === null || typeof value !== 'object' ? JSON.stringify(value) :
  Array.isArray(value) ? '[' + value.map(canonical).join(',') + ']' :
  '{' + Object.keys(value).filter(k => value[k] !== undefined).sort().map(k => JSON.stringify(k) + ':' + canonical(value[k])).join(',') + '}';
const profile = {
  version: 1,
  bots: [{ botId: 'bot-v9', profile: { name: 'LegacyV9', role: '', purpose: 'v9 migration', instructions: 'Legacy.', workspace: ws, codingTools: [] }, profileRevision: 1, archived: false, currentConversationId: 'conv-v9' }],
  conversations: [{ conversationId: 'conv-v9', botId: 'bot-v9', revision: 1, draft: { revision: 0, text: '', materials: [] } }],
  activities: [{ activityId: 'act-v9', ownerBotId: 'bot-v9', originConversationId: 'conv-v9', revision: 1, state: 'completed', goal: 'Legacy v9 task', requirements: [], questions: [], materials: [], inputFiles: [], executionIds: ['exec-v9'], results: [], updatedAt: '2026-01-01T00:00:00.000Z' }],
  executions: [{ executionId: 'exec-v9', revision: 1, state: 'completed', operationId: 'op-v9', input: { revision: 0, text: 'Legacy v9 task', materials: [] }, retained: { id: 'exec-v9' } }],
  operations: [],
  connections: [{ revision: 1, sourceId: 'src-v9', configuration: { wireApi: 'openai-completions', baseUrl: baseURL, model: 'nagi-local-integration', contextTokens: 262144, outputTokens: 4096, workingDirectory: ws, maxActiveTasks: 2 } }],
  savedRevision: 1, appliedRevision: 1
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
  CREATE TABLE activities (activity_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id), origin_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id), revision INTEGER NOT NULL, state TEXT NOT NULL, goal_json TEXT NOT NULL, requirements_json TEXT NOT NULL, questions_json TEXT NOT NULL, materials_json TEXT NOT NULL, input_files_json TEXT NOT NULL, execution_ids_json TEXT NOT NULL, results_json TEXT NOT NULL, binding_json TEXT, retained_json TEXT, granted_linked_activity_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(granted_linked_activity_ids_json) AND json_type(granted_linked_activity_ids_json) = 'array'), routing_json TEXT, acknowledged_at TEXT, acknowledged_execution_id TEXT, closed_at TEXT, updated_at TEXT) STRICT;
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
  CREATE TABLE activity_progress (activity_id TEXT PRIMARY KEY REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, call_id TEXT NOT NULL, progress_seq INTEGER NOT NULL CHECK (progress_seq > 0), record_json TEXT NOT NULL) STRICT;
  CREATE TABLE skill_approval_requests (request_id TEXT PRIMARY KEY, activity_id TEXT NOT NULL REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, call_id TEXT NOT NULL, bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, revision INTEGER NOT NULL CHECK (revision > 0), state TEXT NOT NULL CHECK (state IN ('open','approved','denied','expired','cancelled')), record_json TEXT NOT NULL, UNIQUE (execution_id, call_id)) STRICT;
  CREATE INDEX skill_approval_requests_by_activity ON skill_approval_requests(activity_id, state);
  CREATE TABLE activity_triage_accounting (activity_id TEXT PRIMARY KEY REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, attempts_used INTEGER NOT NULL DEFAULT 0, auto_answers_used INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL) STRICT;
`+"`"+`);
db.exec('BEGIN IMMEDIATE');
db.prepare('INSERT INTO bots VALUES (?, ?, ?, ?, ?, ?, ?, ?)').run(JSON.stringify('bot-v9'), 0, JSON.stringify(profile.bots[0].profile), 1, 0, JSON.stringify('conv-v9'), null, null);
db.prepare('INSERT INTO conversations VALUES (?, ?, ?, ?, ?, ?, ?)').run(JSON.stringify('conv-v9'), 0, JSON.stringify('bot-v9'), 1, JSON.stringify(profile.conversations[0].draft), null, null);
db.prepare('INSERT INTO executions VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)').run(JSON.stringify('exec-v9'), 0, 1, 'completed', JSON.stringify('op-v9'), JSON.stringify(profile.executions[0].input), null, null, null, JSON.stringify({ id: 'exec-v9' }));
db.prepare('INSERT INTO activities VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)').run(JSON.stringify('act-v9'), 0, JSON.stringify('bot-v9'), JSON.stringify('conv-v9'), 1, 'completed', JSON.stringify('Legacy v9 task'), '[]', '[]', '[]', '[]', '["exec-v9"]', '[]', null, null, '[]', null, null, null, null, '2026-01-01T00:00:00.000Z');
db.prepare('INSERT INTO connections VALUES (?, ?, ?, ?)').run(1, 0, JSON.stringify('src-v9'), JSON.stringify(profile.connections[0].configuration));
for (const [k, v] of [['saved_revision', '1'], ['applied_revision', '1'], ['routing_policy', '"balanced"'], ['trace_generation', 'v9-gen'], ['snapshot_sha256', digest]]) {
  db.prepare('INSERT INTO meta VALUES (?, ?)').run(k, v);
}
db.exec('COMMIT');
db.exec('PRAGMA user_version = 9');
db.close();
`, v9Opts.ProfilePath, ws, provider.server.URL+"/v1")

	v9MirrorBefore := productRead(t, v9Opts.ProfilePath)
	callsBeforeV9 := provider.totalCalls.Load()
	v9Proc := startCP1Process(t, v9Opts)
	_ = v9Proc.open(t, ctx)
	if provider.totalCalls.Load() != callsBeforeV9 {
		t.Fatal("v9->v10 migration triggered provider calls")
	}
	if !bytes.Equal(productRead(t, v9Opts.ProfilePath), v9MirrorBefore) {
		t.Fatal("v9->v10 open altered original canonical contacts.json bytes")
	}
	v9Check := runNodeSQLite(t, nodePath, v9DB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
const steers = db.prepare('SELECT count(*) AS n FROM activity_steers').get().n;
db.close();
console.log(JSON.stringify({ v, fk, steers }));
`)
	if v9Check != `{"v":13,"fk":0,"steers":0}` {
		t.Fatalf("v9->v13 migration check = %s", v9Check)
	}
	v9Proc.shutdown(t, ctx)

	runNodeSQLite(t, nodePath, v9DB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec('PRAGMA user_version = 14');
db.close();
`)
	v11Client, startErr := pi.StartProduct(v9Opts)
	if startErr == nil {
		if _, readyErr := v11Client.Ready(ctx); readyErr == nil {
			if _, openErr := v11Client.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
				t.Fatal("worker admitted SQLite user_version=14")
			}
		}
		_, _ = v11Client.Wait(ctx)
	}
	v11After := runNodeSQLite(t, nodePath, v9DB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const bots = db.prepare('SELECT count(*) AS n FROM bots').get().n;
db.close();
console.log(JSON.stringify({ v, bots }));
`)
	if v11After != `{"v":14,"bots":1}` {
		t.Fatalf("v14 refusal mutated DB: %s", v11After)
	}

	bkDir := filepath.Join(root, "backup-source")
	if err := os.MkdirAll(bkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bkOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(bkDir, "pi"),
		ProfilePath:     filepath.Join(bkDir, "contacts.json"),
		CredentialsPath: filepath.Join(bkDir, "connection.json"),
		FreshProfile:    true,
	}
	bkProc := startCP1Process(t, bkOpts)
	_ = bkProc.open(t, ctx)
	cp2ConfigureConnection(t, ctx, bkProc.client, pi.OpenAICompletions, provider.server.URL+"/v1", secret, ws, 4)
	bkA, err := bkProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-bk-a",
		Profile: pi.ProductBotProfile{
			Name: "BackupA", Role: "Coord", Purpose: "Backup",
			Instructions: "Coordinate.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	bkB, err := bkProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-bk-b",
		Profile: pi.ProductBotProfile{
			Name: "BackupB", Role: "Worker", Purpose: "Backup worker",
			Instructions: "Work.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = bkB.BotID
	mu.Unlock()

	if settled := cp1SendTurn(t, ctx, bkProc, bkA.CurrentConversationID, "del-bk", "CP3_DEL_CP3_BACKUP_STEER", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	bkAct := cp2FindActivityByGoal(t, ctx, bkProc, bkA.BotID, "CP3_BACKUP_STEER")
	bkExecID := bkAct.Executions[len(bkAct.Executions)-1].ExecutionID
	backupSteerGate.awaitEntered(t, "backup-steer")
	bkSteer, err := bkProc.client.SteerActivity(ctx, pi.ProductSteerActivityParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: bkAct.ActivityID},
			ExecutionID:           bkExecID,
		},
		ProductText: pi.ProductText{
			Text: "Steer mentioning late key " + lateSecret,
			Materials: []pi.ProductMaterial{{
				ID: "bk-mat-1", Revision: 1, Name: "Late Secret Mat",
				Text: "Material containing " + lateSecret,
			}},
		},
		OperationID: "cp3-bk-steer-op",
	})
	if err != nil {
		t.Fatal(err)
	}
	cp3WaitSteerState(t, ctx, bkProc, bkSteer.SteerID, "queued")
	backupSteerGate.open()
	if settled := bkProc.waitSettled(t, ctx, bkExecID); settled.State != "completed" {
		t.Fatal(settled)
	}
	cp3WaitSteerState(t, ctx, bkProc, bkSteer.SteerID, "delivered")

	if _, err := bkProc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{
			Account: pi.ProductProviderAccount{
				ProviderID: "late-key-prov", Name: "Late Key", WireAPI: pi.OpenAICompletions,
				BaseURL: provider.server.URL + "/v1", DefaultModel: productModel,
				ContextTokens: 262144, OutputTokens: 4096, Enabled: true,
			},
			APIKey: lateSecret,
		},
		OperationID: "cp3-bk-save-late-key",
	}); err != nil {
		t.Fatal(err)
	}

	liveMirrorHash := cp2SHA256File(t, bkOpts.ProfilePath)
	liveDBHash := cp2SHA256File(t, filepath.Join(bkDir, "nagi.db"))
	backupPath := filepath.Join(root, "cp3-portable.json")
	if _, err := bkProc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatalf("BackupProfile: %v", err)
	}
	if cp2SHA256File(t, bkOpts.ProfilePath) != liveMirrorHash || cp2SHA256File(t, filepath.Join(bkDir, "nagi.db")) != liveDBHash {
		t.Fatal("BackupProfile altered live contacts.json or nagi.db bytes")
	}
	backupRaw := productRead(t, backupPath)
	if bytes.Contains(backupRaw, []byte(secret)) || bytes.Contains(backupRaw, []byte(lateSecret)) {
		t.Fatal("CP-3 backup leaked active or late-saved secret")
	}
	var bkParsed struct {
		Backup struct {
			ActivitySteers []struct {
				SteerID string `json:"steerId"`
				Payload struct {
					Text      string               `json:"text"`
					Materials []pi.ProductMaterial `json:"materials"`
				} `json:"payload"`
			} `json:"activitySteers"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(backupRaw, &bkParsed); err != nil || len(bkParsed.Backup.ActivitySteers) != 1 {
		t.Fatalf("backup activitySteers mismatch: %+v, %v", bkParsed.Backup.ActivitySteers, err)
	}
	if !strings.Contains(bkParsed.Backup.ActivitySteers[0].Payload.Text, "[credential]") ||
		len(bkParsed.Backup.ActivitySteers[0].Payload.Materials) != 1 ||
		!strings.Contains(bkParsed.Backup.ActivitySteers[0].Payload.Materials[0].Text, "[credential]") {
		t.Fatalf("backup did not redact steer text & material: %+v", bkParsed.Backup.ActivitySteers[0].Payload)
	}
	bkProc.shutdown(t, ctx)

	assertImportRefused := func(label string, mutated []byte, directWorkerMustRefuse bool) {
		t.Helper()
		tDir := filepath.Join(root, "tampered-"+label)
		if err := os.MkdirAll(tDir, 0o700); err != nil {
			t.Fatal(err)
		}
		tProf := filepath.Join(tDir, "contacts.json")
		if err := os.WriteFile(tProf, mutated, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.InspectStoreRecoveryCopy(tProf); err == nil {
			t.Fatalf("InspectStoreRecoveryCopy admitted tampered copy %s", label)
		}
		if !directWorkerMustRefuse {
			return
		}
		tOpts := pi.ProductOptions{
			NodePath:        nodePath,
			WorkerPath:      workerPath,
			StateDir:        filepath.Join(tDir, "pi"),
			ProfilePath:     tProf,
			CredentialsPath: filepath.Join(tDir, "connection.json"),
		}
		c, err := pi.StartProduct(tOpts)
		if err == nil {
			if _, rErr := c.Ready(ctx); rErr == nil {
				if _, oErr := c.Open(ctx, []int{pi.ProductVersion}); oErr == nil {
					t.Fatalf("worker admitted tampered import %s", label)
				}
			}
			_, _ = c.Wait(ctx)
		}
	}

	var tamperOwner map[string]any
	if err := json.Unmarshal(backupRaw, &tamperOwner); err != nil {
		t.Fatal(err)
	}
	bkObj := tamperOwner["backup"].(map[string]any)
	stList := bkObj["activitySteers"].([]any)
	stList[0].(map[string]any)["activityId"] = "tampered-wrong-activity-id"
	tamperOwnerBytes, _ := json.Marshal(tamperOwner)
	assertImportRefused("steer-owner", tamperOwnerBytes, true)

	var tamperMissing map[string]any
	_ = json.Unmarshal(backupRaw, &tamperMissing)
	delete(tamperMissing["backup"].(map[string]any), "activitySteers")
	tamperMissingBytes, _ := json.Marshal(tamperMissing)
	assertImportRefused("steer-missing", tamperMissingBytes, true)

	var tamperUnknownTriage map[string]any
	_ = json.Unmarshal(backupRaw, &tamperUnknownTriage)
	tExecs := tamperUnknownTriage["executions"].([]any)
	for _, rawExec := range tExecs {
		em := rawExec.(map[string]any)
		if em["executionId"] == bkExecID {
			em["state"] = "unknown"
		}
	}
	tActs := tamperUnknownTriage["activities"].([]any)
	for _, rawAct := range tActs {
		am := rawAct.(map[string]any)
		if am["activityId"] == bkAct.ActivityID {
			am["questions"] = []map[string]any{{
				"questionId":          bkExecID + ":q-bad",
				"executionId":         bkExecID,
				"text":                "Which output format should I use?",
				"revision":            1,
				"state":               "triage_pending",
				"triageRulesRevision": 1,
				"options": []map[string]any{
					{"id": "markdown", "label": "Markdown"},
					{"id": "plain_text", "label": "Plain text"},
				},
				"allowFreeText": false,
				"preferenceKey": "formatting",
			}}
		}
	}
	tamperUnknownTriage["backup"].(map[string]any)["triageAccounting"] = []map[string]any{{
		"activityId":      bkAct.ActivityID,
		"attemptsUsed":    1,
		"autoAnswersUsed": 0,
		"updatedAt":       "2026-01-01T00:00:00.000Z",
	}}
	tamperTriageBytes, _ := json.Marshal(tamperUnknownTriage)
	assertImportRefused("unknown-triage-pending", tamperTriageBytes, false)

	restoreDir := filepath.Join(root, "portable-restored")
	if err := os.MkdirAll(restoreDir, 0o700); err != nil {
		t.Fatal(err)
	}
	restoreProfile := filepath.Join(restoreDir, "contacts.json")
	if err := os.WriteFile(restoreProfile, []byte("unavailable profile"), 0o600); err != nil {
		t.Fatal(err)
	}
	recoveryCopy, err := store.InspectStoreRecoveryCopy(backupPath)
	if err != nil {
		t.Fatalf("InspectStoreRecoveryCopy(backup): %v", err)
	}
	recoveryTarget, err := store.InspectStoreRecoveryTarget(restoreProfile)
	if err != nil {
		t.Fatalf("InspectStoreRecoveryTarget: %v", err)
	}
	if _, err := store.RestoreStoreRecoveryCopy(recoveryCopy, recoveryTarget); err != nil {
		t.Fatalf("RestoreStoreRecoveryCopy: %v", err)
	}
	restoreOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(restoreDir, "pi"),
		ProfilePath:     restoreProfile,
		CredentialsPath: filepath.Join(restoreDir, "connection.json"),
	}
	callsBeforeRestore := provider.totalCalls.Load()
	for round := 1; round <= 2; round++ {
		rProc := startCP1Process(t, restoreOpts)
		_ = rProc.open(t, ctx)
		rAct, err := rProc.client.GetActivity(ctx, bkAct.ActivityID)
		if err != nil || len(rAct.Steers) != 1 || rAct.Steers[0].SteerID != bkSteer.SteerID || rAct.Steers[0].State != "delivered" {
			t.Fatalf("restore round %d GetActivity.Steers = %+v, %v", round, rAct.Steers, err)
		}
		rStat, err := rProc.client.SteerStatus(ctx, pi.ProductSteerStatusParams{SteerID: bkSteer.SteerID})
		if err != nil || rStat.State != "delivered" || rStat.OperationID != "cp3-bk-steer-op" {
			t.Fatalf("restore round %d SteerStatus = %+v, %v", round, rStat, err)
		}
		rOp, err := rProc.client.Operation(ctx, "cp3-bk-steer-op")
		if err != nil || rOp.State != "accepted" {
			t.Fatalf("restore round %d Operation = %+v, %v", round, rOp, err)
		}
		var origSteer pi.ProductActivitySteer
		if err := json.Unmarshal(rOp.Result, &origSteer); err != nil || origSteer.State != "pending_enqueue" || origSteer.Revision != 1 {
			t.Fatalf("restore round %d immutable operation receipt mismatch: %+v, %v", round, origSteer, err)
		}
		rProc.shutdown(t, ctx)
	}
	if provider.totalCalls.Load() != callsBeforeRestore {
		t.Fatalf("independent restore across 2 reopens made provider calls: before=%d after=%d", callsBeforeRestore, provider.totalCalls.Load())
	}
	restoreSchema := runNodeSQLite(t, nodePath, filepath.Join(restoreDir, "nagi.db"), `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.close();
console.log(JSON.stringify({ v, fk }));
`)
	if restoreSchema != `{"v":13,"fk":0}` {
		t.Fatalf("restored v13 schema check = %s", restoreSchema)
	}

	wbDir := filepath.Join(root, "wb-exact-fit")
	if err := os.MkdirAll(wbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	wbOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(wbDir, "pi"),
		ProfilePath:     filepath.Join(wbDir, "contacts.json"),
		CredentialsPath: filepath.Join(wbDir, "connection.json"),
		FreshProfile:    true,
	}
	wbProc := startCP1Process(t, wbOpts)
	_ = wbProc.open(t, ctx)
	cp2ConfigureConnection(t, ctx, wbProc.client, pi.OpenAICompletions, provider.server.URL+"/v1", secret, ws, 4)
	wA, err := wbProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-wb-a",
		Profile: pi.ProductBotProfile{
			Name: "WBA", Role: "Coord", Purpose: "WB cleanup check",
			Instructions: "Coordinate.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wB, err := wbProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp3-wb-b",
		Profile: pi.ProductBotProfile{
			Name: "WBB", Role: "Worker", Purpose: "WB worker",
			Instructions: "Work.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = wB.BotID
	mu.Unlock()
	var wbActs []pi.ProductActivity
	for i := 1; i <= 2; i++ {
		goal := fmt.Sprintf("CP3_WB_ROW_%d", i)
		if settled := cp1SendTurn(t, ctx, wbProc, wA.CurrentConversationID, fmt.Sprintf("del-wb-%d", i), "CP3_DEL_"+goal, nil); settled.State != "completed" {
			t.Fatal(settled)
		}
		act := cp2FindActivityByGoal(t, ctx, wbProc, wA.BotID, goal)
		_ = cp2WaitQuestion(t, ctx, wbProc, act.ActivityID, "open")
		wbActs = append(wbActs, act)
	}
	if settled := cp1SendTurn(t, ctx, wbProc, wA.CurrentConversationID, "wb-check", "CP3_WB_CHECK", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	gotBoard := wbTwoRowBoard
	mu.Unlock()
	if gotBoard == "" || len(gotBoard) <= 1400 || len(gotBoard) > 1536 || !utf8.ValidString(gotBoard) ||
		!strings.Contains(gotBoard, wbActs[0].ActivityID) || !strings.Contains(gotBoard, wbActs[1].ActivityID) ||
		strings.Contains(gotBoard, "more active/unread activities") {
		t.Fatalf("two-row whiteboard fit check failed (len=%d): %s", len(gotBoard), gotBoard)
	}
	wbProc.shutdown(t, ctx)

	provider.assertNoProblems()
}
