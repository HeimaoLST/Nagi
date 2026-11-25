//go:build integration

package pi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
)

func unmarshalToolText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var out strings.Builder
		for _, p := range parts {
			out.WriteString(p.Text)
		}
		return out.String()
	}
	return string(raw)
}

func assertNoRetiredToolsAndSorted(t *testing.T, tools []string) {
	t.Helper()
	if !slices.IsSorted(tools) {
		t.Errorf("tools not sorted alphabetically: %v", tools)
	}
	for _, retired := range []string{"issue_wait", "issue_list", "issue_get", "issue_action", "delegate_activity"} {
		if slices.Contains(tools, retired) {
			t.Errorf("retired live tool %q exposed in %v", retired, tools)
		}
	}
}

func waitSettledExecution(t *testing.T, ctx context.Context, proc *productProcess, execID string) pi.ProductExecutionEvent {
	t.Helper()
	for {
		ev := proc.event(t, ctx)
		if ev.Name != "execution.settled" {
			continue
		}
		var settled pi.ProductExecutionEvent
		if err := json.Unmarshal(ev.Data, &settled); err != nil {
			t.Fatalf("decode execution.settled: %v", err)
		}
		if settled.ExecutionID == execID {
			return settled
		}
	}
}

func TestPiProductActivityInspectionLiveTraceConversationAndACL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const inactiveKey = "sk-inactive-secret-m0a-9876543210"
	const coldKey = "sk-cold-active-m0a-5555555555"
	const needle = "LATENT_NEEDLE_7742"

	root := t.TempDir()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	boundaryNote := strings.Repeat("x", 370) + inactiveKey + " trailing_note_marker\n"
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte(boundaryNote), 0600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var botBID string
	var actCID, execCID, convCID string
	var actA1ID, convAID string
	var actBID string
	var convAStep, actCStep, actA1Step, actBStep int
	var coldPhase atomic.Bool
	var coldConvACalls atomic.Int32
	var botBDispatchCount atomic.Int32
	var toolResults = map[string]string{}
	var providerErrs []string
	actBReadOriginDone := make(chan struct{})
	var actBReadOriginOnce sync.Once
	var savedTailCursor string

	recordErr := func(format string, args ...any) {
		mu.Lock()
		providerErrs = append(providerErrs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role       string          `json:"role"`
				ToolCallID string          `json:"tool_call_id"`
				Content    json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		var sys string
		for _, m := range body.Messages {
			if m.Role == "system" || m.Role == "developer" {
				sys += unmarshalToolText(m.Content) + "\n"
			}
		}
		toolNames := make([]string, 0, len(body.Tools))
		for _, tl := range body.Tools {
			toolNames = append(toolNames, tl.Function.Name)
		}
		assertNoRetiredToolsAndSorted(t, toolNames)
		isActivity := slices.Contains(toolNames, "activity_ask")
		if isActivity {
			for _, reqTool := range []string{"activity_ask", "activity_trace_read", "conversation_read", "conversation_search"} {
				if !slices.Contains(toolNames, reqTool) {
					recordErr("activity missing required tool %q in %v", reqTool, toolNames)
				}
			}
		}
		last := body.Messages[len(body.Messages)-1]
		lastText := unmarshalToolText(last.Content)
		if last.Role == "tool" && last.ToolCallID != "" {
			mu.Lock()
			toolResults[last.ToolCallID] = lastText
			mu.Unlock()
		}

		isAlpha := strings.Contains(sys, "Bot Identity\nName: Alpha")
		isBeta := strings.Contains(sys, "Bot Identity\nName: Beta")
		isGamma := strings.Contains(sys, "Bot Identity\nName: Gamma")

		if isGamma && !isActivity {
			if last.Role == "tool" {
				piChatResponse(w, "Gamma delegated actC.", "", "", "")
				return
			}
			piChatResponse(w, "", "activity_delegate", "gamma-del-1", `{"key":"gamma-work","goal":"Gamma isolated work"}`)
			return
		}
		if isGamma && isActivity {
			mu.Lock()
			actCStep++
			step := actCStep
			mu.Unlock()
			switch step {
			case 1:
				piChatResponse(w, "", "read", "gamma-read-1", `{"path":"notes.txt"}`)
			default:
				piChatResponse(w, "Gamma activity complete.", "", "", "")
			}
			return
		}

		if isAlpha && isActivity {
			mu.Lock()
			actA1Step++
			step := actA1Step
			mu.Unlock()
			switch step {
			case 1:
				piChatResponse(w, "", "read", "a1-read-1", `{"path":"notes.txt"}`)
			case 2:
				piChatResponse(w, "", "write", "a1-write-1", `{"path":"out.txt","content":"verified output\n"}`)
			case 3:
				piChatResponse(w, "", "activity_ask", "a1-ask-1", `{"text":"Confirm live trace check?"}`)
			default:
				piChatResponse(w, "Alpha activity done with "+inactiveKey, "", "", "")
			}
			return
		}

		if isAlpha && !isActivity {
			if coldPhase.Load() {
				coldConvACalls.Add(1)
			}
			mu.Lock()
			convAStep++
			step := convAStep
			a1 := actA1ID
			cID := actCID
			bID := botBID
			mu.Unlock()

			switch step {
			case 1:
				piChatResponse(w, "Recorded turn one with "+inactiveKey, "", "", "")
			case 2:
				piChatResponse(w, "", "activity_delegate", "a-del-own", `{"key":"alpha-own","goal":"Alpha trace run"}`)
			case 3:
				piChatResponse(w, "Delegated alpha-own.", "", "", "")
			case 4:
				oversized := make([]string, 300)
				for i := range oversized {
					oversized[i] = a1
				}
				args, _ := json.Marshal(map[string]any{
					"key": "oversized-links", "goal": "Should reject oversized raw input",
					"assignee_id": bID, "linked_activity_ids": oversized,
				})
				piChatResponse(w, "", "activity_delegate", "a-del-oversized", string(args))
			case 5:
				args, _ := json.Marshal(map[string]any{
					"key": "unauth-link", "goal": "Should deny foreign actC",
					"assignee_id": bID, "linked_activity_ids": []string{cID},
				})
				piChatResponse(w, "", "activity_delegate", "a-del-unauth", string(args))
			case 6:
				args, _ := json.Marshal(map[string]any{
					"key": "missing-link", "goal": "Should deny missing ID",
					"assignee_id": bID, "linked_activity_ids": []string{"missing-act-999"},
				})
				piChatResponse(w, "", "activity_delegate", "a-del-missing", string(args))
			case 7:
				args, _ := json.Marshal(map[string]any{
					"key": "valid-grant", "goal": "Beta inspect granted trace and origin",
					"assignee_id": bID, "linked_activity_ids": []string{a1, a1},
				})
				piChatResponse(w, "", "activity_delegate", "a-del-valid", string(args))
			default:
				select {
				case <-actBReadOriginDone:
				case <-r.Context().Done():
				}
				piChatResponse(w, "Alpha finished after Beta live origin inspection.", "", "", "")
			}
			return
		}

		if isBeta && isActivity {
			botBDispatchCount.Add(1)
			mu.Lock()
			actBStep++
			step := actBStep
			a1 := actA1ID
			cID := actCID
			cExec := execCID
			cA := convAID
			cC := convCID
			mu.Unlock()

			switch step {
			case 1:
				args, _ := json.Marshal(map[string]any{"conversation_id": cA, "tail": true, "limit": 1, "max_bytes": 2048})
				piChatResponse(w, "", "conversation_read", "b-conv-tail-1", string(args))
			case 2:
				var page struct {
					NextCursor string `json:"nextCursor"`
				}
				_ = json.Unmarshal([]byte(lastText), &page)
				mu.Lock()
				savedTailCursor = page.NextCursor
				mu.Unlock()
				args, _ := json.Marshal(map[string]any{"conversation_id": cA, "cursor": page.NextCursor, "tail": false})
				piChatResponse(w, "", "conversation_read", "b-conv-conflict", string(args))
			case 3:
				mu.Lock()
				cur := savedTailCursor
				mu.Unlock()
				args, _ := json.Marshal(map[string]any{"conversation_id": cA, "cursor": cur, "tail": true, "limit": 2, "max_bytes": 4096})
				piChatResponse(w, "", "conversation_read", "b-conv-tail-2", string(args))
			case 4:
				args, _ := json.Marshal(map[string]any{"conversation_id": cA, "query": strings.ToLower(needle)})
				piChatResponse(w, "", "conversation_search", "b-search-needle", string(args))
			case 5:
				args, _ := json.Marshal(map[string]any{"conversation_id": cA, "query": inactiveKey})
				piChatResponse(w, "", "conversation_search", "b-search-secret", string(args))
			case 6:
				args, _ := json.Marshal(map[string]any{"activity_id": a1, "limit": 2})
				piChatResponse(w, "", "activity_trace_read", "b-trace-granted", string(args))
			case 7:
				var page struct {
					NextCursor string `json:"nextCursor"`
				}
				_ = json.Unmarshal([]byte(lastText), &page)
				args, _ := json.Marshal(map[string]any{"activity_id": a1, "cursor": page.NextCursor + "AA"})
				piChatResponse(w, "", "activity_trace_read", "b-trace-tampered", string(args))
			case 8:
				args, _ := json.Marshal(map[string]any{"activity_id": cID})
				piChatResponse(w, "", "activity_trace_read", "b-trace-foreign", string(args))
			case 9:
				args, _ := json.Marshal(map[string]any{"activity_id": "nonexistent-act-xyz"})
				piChatResponse(w, "", "activity_trace_read", "b-trace-missing", string(args))
			case 10:
				args, _ := json.Marshal(map[string]any{"activity_id": a1, "execution_id": cExec})
				piChatResponse(w, "", "activity_trace_read", "b-trace-foreign-exec", string(args))
			case 11:
				args, _ := json.Marshal(map[string]any{"conversation_id": cC})
				piChatResponse(w, "", "conversation_read", "b-conv-foreign", string(args))
			case 12:
				actBReadOriginOnce.Do(func() { close(actBReadOriginDone) })
				piChatResponse(w, "Beta live inspection complete.", "", "", "")
			case 13:
				args, _ := json.Marshal(map[string]any{"conversation_id": cA, "limit": 2, "max_bytes": 4096})
				piChatResponse(w, "", "conversation_read", "b-cold-fwd", string(args))
			case 14:
				args, _ := json.Marshal(map[string]any{"conversation_id": cA, "tail": true, "limit": 2, "max_bytes": 4096})
				piChatResponse(w, "", "conversation_read", "b-cold-tail", string(args))
			case 15:
				args, _ := json.Marshal(map[string]any{"conversation_id": cA, "query": strings.ToLower(needle)})
				piChatResponse(w, "", "conversation_search", "b-cold-search", string(args))
			case 16:
				args, _ := json.Marshal(map[string]any{"activity_id": a1, "limit": 10})
				piChatResponse(w, "", "activity_trace_read", "b-cold-trace", string(args))
			default:
				piChatResponse(w, "Beta cold inspection complete.", "", "", "")
			}
			return
		}

		recordErr("unexpected provider call isAlpha=%v isBeta=%v isGamma=%v isActivity=%v", isAlpha, isBeta, isGamma, isActivity)
		http.Error(w, "unexpected route", http.StatusBadRequest)
	}))
	t.Cleanup(provider.Close)

	opts := pi.ProductOptions{
		NodePath:        os.Getenv("NAGI_TEST_NODE"),
		WorkerPath:      os.Getenv("NAGI_TEST_WORKER"),
		StateDir:        filepath.Join(root, "pi"),
		ProfilePath:     filepath.Join(root, "contacts.json"),
		CredentialsPath: filepath.Join(root, "connection.json"),
		FreshProfile:    true,
	}
	writeProductFixture(t, opts.CredentialsPath, map[string]any{
		"version": 2, "credential_source": productSource, "model": productModel,
		"context_tokens": 65536, "output_tokens": 4096, "working_directory": workspace, "max_active_tasks": 4,
		"sources": map[string]any{
			productSource:      map[string]string{"api": "openai-chat-completions", "endpoint": provider.URL + "/v1", "api_key": productKey},
			"inactive-archive": map[string]string{"api": "openai-chat-completions", "endpoint": "http://127.0.0.1:9/v1", "api_key": inactiveKey},
		},
	})

	proc := startProductProcess(t, opts)
	proc.open(t, ctx)

	if _, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-conn-save-1",
		Configuration: pi.ProductConnectionInput{
			APIKey: productKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI: pi.OpenAICompletions, BaseURL: provider.URL + "/v1", Model: productModel,
				ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: workspace, MaxActiveTasks: 4,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	savedRev := uint64(1)
	if _, err := proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{SavedRevision: savedRev, OperationID: "op-conn-apply-1"}); err != nil {
		t.Fatal(err)
	}

	coding := []string{"read", "write"}
	botA, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-a",
		Profile:     pi.ProductBotProfile{Name: "Alpha", Purpose: "Lead bot", CodingTools: coding},
	})
	if err != nil {
		t.Fatal(err)
	}
	botB, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-b",
		Profile:     pi.ProductBotProfile{Name: "Beta", Purpose: "Inspector bot", CodingTools: coding},
	})
	if err != nil {
		t.Fatal(err)
	}
	botC, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-c",
		Profile:     pi.ProductBotProfile{Name: "Gamma", Purpose: "Foreign bot", CodingTools: coding},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = botB.BotID
	convAID = botA.CurrentConversationID
	convCID = botC.CurrentConversationID
	mu.Unlock()

	if _, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botC.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "Start Gamma work"},
		ExpectedRevision:          0, OperationID: "op-c-draft",
	}); err != nil {
		t.Fatal(err)
	}
	cSend, err := proc.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botC.CurrentConversationID},
		DraftRevision:             1, OperationID: "op-c-send",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitSettledExecution(t, ctx, proc, cSend.Execution.ExecutionID)
	cActs, err := proc.client.ListActivities(ctx, pi.ProductActivityListParams{BotID: botC.BotID})
	if err != nil || len(cActs.Items) != 1 {
		t.Fatalf("gamma activities = %+v, %v", cActs, err)
	}
	actCDetail, err := proc.client.GetActivity(ctx, cActs.Items[0].ActivityID)
	if err != nil || len(actCDetail.Executions) != 1 {
		t.Fatalf("actC detail = %+v, %v", actCDetail, err)
	}
	if actCDetail.Executions[0].State == "running" || actCDetail.Executions[0].State == "starting" {
		waitSettledExecution(t, ctx, proc, actCDetail.Executions[0].ExecutionID)
	}
	mu.Lock()
	actCID = actCDetail.ActivityID
	execCID = actCDetail.Executions[0].ExecutionID
	mu.Unlock()

	longUserText := strings.Repeat("u", 395) + inactiveKey + " end_turn_one"
	materialText := strings.Repeat("m", 1200) + " " + inactiveKey + " " + needle + " tail_material"
	if _, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botA.CurrentConversationID},
		ProductText: pi.ProductText{
			Text: longUserText,
			Materials: []pi.ProductMaterial{{
				ID: "mat-1", Revision: 1, Name: "audit-" + inactiveKey + ".txt", Text: materialText,
			}},
		},
		ExpectedRevision: 0, OperationID: "op-a-draft-1",
	}); err != nil {
		t.Fatal(err)
	}
	aSend1, err := proc.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botA.CurrentConversationID},
		DraftRevision:             1, OperationID: "op-a-send-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitSettledExecution(t, ctx, proc, aSend1.Execution.ExecutionID)

	convAState, err := proc.client.GetConversation(ctx, botA.CurrentConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botA.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "Delegate Alpha own activity"},
		ExpectedRevision:          convAState.Draft.Revision, OperationID: "op-a-draft-2",
	}); err != nil {
		t.Fatal(err)
	}
	aSend2, err := proc.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botA.CurrentConversationID},
		DraftRevision:             convAState.Draft.Revision + 1, OperationID: "op-a-send-2",
	})
	if err != nil {
		t.Fatal(err)
	}

	var gotTraceEvent *pi.ProductActivityTraceUpdated
	var a1ActivityID, a1ExecID, a1QuestionID string
	var a1QuestionRev uint64
	var a1RevBeforeLiveTrace uint64
	aSend2Settled := false
	for !aSend2Settled || a1QuestionID == "" || gotTraceEvent == nil {
		ev := proc.event(t, ctx)
		switch ev.Name {
		case "activity.traceUpdated":
			if ev.TraceUpdated != nil && ev.TraceUpdated.ActivityID != actCID {
				copyEv := *ev.TraceUpdated
				gotTraceEvent = &copyEv
			}
		case "activity.changed":
			var summary pi.ProductActivitySummary
			if err := json.Unmarshal(ev.Data, &summary); err == nil && summary.OwnerBotID == botA.BotID {
				a1ActivityID = summary.ActivityID
				if len(summary.OpenQuestionIDs) > 0 {
					detail, err := proc.client.GetActivity(ctx, a1ActivityID)
					if err != nil {
						t.Fatal(err)
					}
					a1ExecID = detail.Executions[0].ExecutionID
					a1QuestionID = detail.Questions[0].QuestionID
					a1QuestionRev = detail.Questions[0].Revision
					a1RevBeforeLiveTrace = detail.Revision
				}
			}
		case "execution.settled":
			var settled pi.ProductExecutionEvent
			if err := json.Unmarshal(ev.Data, &settled); err == nil && settled.ExecutionID == aSend2.Execution.ExecutionID {
				aSend2Settled = true
			}
		}
	}
	mu.Lock()
	actA1ID = a1ActivityID
	mu.Unlock()

	liveTrace, err := proc.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: a1ActivityID},
		ExecutionID:           a1ExecID,
	})
	if err != nil {
		t.Fatalf("live ActivityTrace failed: %v", err)
	}
	if liveTrace.Coverage != "recorded" || liveTrace.HistoryTruncated || len(liveTrace.Items) < 4 {
		t.Fatalf("unexpected live trace: %+v", liveTrace)
	}
	rawLiveTrace, _ := json.Marshal(liveTrace)
	if bytes.Contains(rawLiveTrace, []byte(inactiveKey)) || bytes.Contains(rawLiveTrace, []byte(productKey)) {
		t.Fatalf("live trace leaked credential: %s", rawLiveTrace)
	}
	kinds := map[string]bool{}
	for _, item := range liveTrace.Items {
		kinds[item.Kind] = true
	}
	for _, wantKind := range []string{"state_changed", "tool_started", "tool_finished", "file_confirmed"} {
		if !kinds[wantKind] {
			t.Fatalf("live trace missing kind %q: %+v", wantKind, liveTrace.Items)
		}
	}
	a1DetailCheck, err := proc.client.GetActivity(ctx, a1ActivityID)
	if err != nil || a1DetailCheck.Revision != a1RevBeforeLiveTrace {
		t.Fatalf("trace-only updates changed activity revision: before=%d after=%d err=%v", a1RevBeforeLiveTrace, a1DetailCheck.Revision, err)
	}

	convAState2, err := proc.client.GetConversation(ctx, botA.CurrentConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botA.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "Unrelated draft save preserves traces"},
		ExpectedRevision:          convAState2.Draft.Revision, OperationID: "op-a-draft-preserve",
	}); err != nil {
		t.Fatal(err)
	}
	afterDraftTrace, err := proc.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: a1ActivityID},
		ExecutionID:           a1ExecID,
	})
	if err != nil || len(afterDraftTrace.Items) < len(liveTrace.Items) {
		t.Fatalf("draft save erased traces: before=%d after=%d err=%v", len(liveTrace.Items), len(afterDraftTrace.Items), err)
	}

	if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: a1ActivityID},
			ExecutionID:           a1ExecID,
		},
		ProductText:      pi.ProductText{Text: "Confirmed live"},
		QuestionID:       a1QuestionID,
		QuestionRevision: a1QuestionRev,
		OperationID:      "op-a1-answer",
	}); err != nil {
		t.Fatal(err)
	}
	waitSettledExecution(t, ctx, proc, a1ExecID)
	a1Res, err := proc.client.ActivityResult(ctx, pi.ProductActivityExecutionParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: a1ActivityID},
		ExecutionID:           a1ExecID,
	})
	if err != nil || len(a1Res.Files) != 1 || filepath.Base(a1Res.Files[0].Path) != "out.txt" || strings.Contains(a1Res.Text, inactiveKey) {
		t.Fatalf("a1 result mismatch or secret leak: %+v, %v", a1Res, err)
	}

	if _, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-conn-save-cold",
		Configuration: pi.ProductConnectionInput{
			APIKey: coldKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI: pi.OpenAICompletions, BaseURL: provider.URL + "/v1", Model: productModel,
				ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: workspace, MaxActiveTasks: 4,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{SavedRevision: 2, OperationID: "op-conn-apply-cold"}); err != nil {
		t.Fatal(err)
	}

	convAState3, err := proc.client.GetConversation(ctx, botA.CurrentConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botA.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "Run ACL and delegation inspection checks"},
		ExpectedRevision:          convAState3.Draft.Revision, OperationID: "op-a-draft-3",
	}); err != nil {
		t.Fatal(err)
	}
	aSend3, err := proc.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botA.CurrentConversationID},
		DraftRevision:             convAState3.Draft.Revision + 1, OperationID: "op-a-send-3",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitSettledExecution(t, ctx, proc, aSend3.Execution.ExecutionID)

	bActs, err := proc.client.ListActivities(ctx, pi.ProductActivityListParams{BotID: botB.BotID})
	if err != nil || len(bActs.Items) != 1 {
		t.Fatalf("expected exactly 1 Beta activity (denied grants must create 0), got %+v, %v", bActs, err)
	}
	actBID = bActs.Items[0].ActivityID
	actBDetail, err := proc.client.GetActivity(ctx, actBID)
	if err != nil || len(actBDetail.Executions) != 1 {
		t.Fatalf("actB detail = %+v, %v", actBDetail, err)
	}
	if actBDetail.Executions[0].State == "running" || actBDetail.Executions[0].State == "starting" {
		waitSettledExecution(t, ctx, proc, actBDetail.Executions[0].ExecutionID)
	}

	mu.Lock()
	resOversized := toolResults["a-del-oversized"]
	resUnauth := toolResults["a-del-unauth"]
	resMissing := toolResults["a-del-missing"]
	resTail1 := toolResults["b-conv-tail-1"]
	resConflict := toolResults["b-conv-conflict"]
	resTail2 := toolResults["b-conv-tail-2"]
	resNeedle := toolResults["b-search-needle"]
	resSecretSearch := toolResults["b-search-secret"]
	resTraceGranted := toolResults["b-trace-granted"]
	resTraceTampered := toolResults["b-trace-tampered"]
	resTraceForeign := toolResults["b-trace-foreign"]
	resTraceMissing := toolResults["b-trace-missing"]
	resTraceForeignExec := toolResults["b-trace-foreign-exec"]
	resConvForeign := toolResults["b-conv-foreign"]
	errsSnapshot := slices.Clone(providerErrs)
	mu.Unlock()

	if len(errsSnapshot) != 0 {
		t.Fatalf("provider assertion errors: %v", errsSnapshot)
	}
	if !strings.Contains(resOversized, `"invalid_params"`) {
		t.Fatalf("oversized raw linked_activity_ids did not return invalid_params: %s", resOversized)
	}
	if !strings.Contains(resUnauth, `"scope_denied"`) || resUnauth != resMissing {
		t.Fatalf("delegate unauth vs missing not uniform scope_denied: unauth=%s missing=%s", resUnauth, resMissing)
	}
	if strings.Contains(resTail1, inactiveKey) || strings.Contains(resTail2, inactiveKey) || !strings.Contains(resTail1, `"nextCursor"`) {
		t.Fatalf("live origin conversation_read leaked secret or missed cursor: tail1=%s tail2=%s", resTail1, resTail2)
	}
	if !strings.Contains(resConflict, `"invalid_cursor"`) {
		t.Fatalf("conflicting direction with cursor did not fail invalid_cursor: %s", resConflict)
	}
	if !strings.Contains(resNeedle, `"field":"material"`) || !strings.Contains(resNeedle, needle) || strings.Contains(resNeedle, inactiveKey) {
		t.Fatalf("conversation_search did not find redacted material hit beyond excerpt: %s", resNeedle)
	}
	if !strings.Contains(resSecretSearch, `"items":[]`) {
		t.Fatalf("conversation_search matched redacted inactive secret: %s", resSecretSearch)
	}
	if !strings.Contains(resTraceGranted, `"coverage":"recorded"`) || strings.Contains(resTraceGranted, inactiveKey) {
		t.Fatalf("granted activity_trace_read invalid or leaked secret: %s", resTraceGranted)
	}
	if !strings.Contains(resTraceTampered, `"invalid_cursor"`) {
		t.Fatalf("tampered trace cursor did not fail invalid_cursor: %s", resTraceTampered)
	}
	if !strings.Contains(resTraceForeign, `"scope_denied"`) || resTraceForeign != resTraceMissing {
		t.Fatalf("foreign vs missing activity_trace_read not uniform scope_denied: foreign=%s missing=%s", resTraceForeign, resTraceMissing)
	}
	if !strings.Contains(resTraceForeignExec, `"scope_denied"`) {
		t.Fatalf("foreign execution_id did not return scope_denied: %s", resTraceForeignExec)
	}
	if !strings.Contains(resConvForeign, `"scope_denied"`) {
		t.Fatalf("foreign origin conversation_read did not return scope_denied: %s", resConvForeign)
	}

	proc.shutdown(t, ctx)

	rev1SourceID := runNodeSQLite(t, opts.NodePath, filepath.Join(root, "nagi.db"), `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const row = db.prepare('SELECT source_id FROM connections WHERE revision = 1').get();
db.close();
console.log(JSON.parse(row.source_id));
`)
	rawCreds := productRead(t, opts.CredentialsPath)
	var credsDoc map[string]any
	if err := json.Unmarshal(rawCreds, &credsDoc); err != nil {
		t.Fatal(err)
	}
	sources := credsDoc["sources"].(map[string]any)
	delete(sources, rev1SourceID)
	writeProductFixture(t, opts.CredentialsPath, credsDoc)

	coldPhase.Store(true)
	opts.FreshProfile = false
	proc2 := startProductProcess(t, opts)
	proc2.open(t, ctx)

	actBReopened, err := proc2.client.GetActivity(ctx, actBID)
	if err != nil {
		t.Fatal(err)
	}
	contReceipt, err := proc2.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actBID},
		ProductText:           pi.ProductText{Text: "Inspect cold origin conversation"},
		OperationID:           "op-b-cold-continue",
		ExpectedRevision:      actBReopened.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitSettledExecution(t, ctx, proc2, contReceipt.Execution.ExecutionID)
	proc2.shutdown(t, ctx)

	if got := coldConvACalls.Load(); got != 0 {
		t.Fatalf("cold origin inspection woke origin conversation with %d provider calls", got)
	}
	mu.Lock()
	coldFwd := toolResults["b-cold-fwd"]
	coldTail := toolResults["b-cold-tail"]
	coldSearch := toolResults["b-cold-search"]
	coldTrace := toolResults["b-cold-trace"]
	mu.Unlock()
	if !strings.Contains(coldFwd, `"items":[`) || strings.Contains(coldFwd, inactiveKey) || !strings.Contains(coldTail, `"items":[`) || strings.Contains(coldTail, inactiveKey) {
		t.Fatalf("cold conversation_read failed or leaked secret: fwd=%s tail=%s", coldFwd, coldTail)
	}
	if !strings.Contains(coldSearch, needle) || strings.Contains(coldSearch, inactiveKey) {
		t.Fatalf("cold conversation_search failed or leaked secret: %s", coldSearch)
	}
	if !strings.Contains(coldTrace, `"coverage":"recorded"`) || strings.Contains(coldTrace, inactiveKey) {
		t.Fatalf("cold granted activity_trace_read failed: %s", coldTrace)
	}
}

func TestPiProductActivityInspectionBackupMigrationAndRetention(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const inactiveKey = "sk-backup-inactive-m0a-1122334455"
	nodePath := os.Getenv("NAGI_TEST_NODE")
	workerPath := os.Getenv("NAGI_TEST_WORKER")
	if os.Getenv("NAGI_PI_PAYLOAD") == "" {
		t.Setenv("NAGI_PI_PAYLOAD", filepath.Dir(filepath.Dir(nodePath)))
	}

	root := t.TempDir()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "secret-note.txt"), []byte("payload "+inactiveKey+" end\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var botBID string
	var actA1ID, execA1ID, actCID, actBID string
	var providerCalls atomic.Int32
	var aStep, a1Step, bStep, cStep int

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		var sys string
		for _, m := range body.Messages {
			if m.Role == "system" || m.Role == "developer" {
				sys += unmarshalToolText(m.Content) + "\n"
			}
		}
		toolNames := make([]string, 0, len(body.Tools))
		for _, tl := range body.Tools {
			toolNames = append(toolNames, tl.Function.Name)
		}
		isActivity := slices.Contains(toolNames, "activity_ask")
		isAlpha := strings.Contains(sys, "Bot Identity\nName: Alpha")
		isBeta := strings.Contains(sys, "Bot Identity\nName: Beta")
		isGamma := strings.Contains(sys, "Bot Identity\nName: Gamma")

		if isGamma && !isActivity {
			mu.Lock()
			cStep++
			step := cStep
			mu.Unlock()
			if step == 1 {
				piChatResponse(w, "", "activity_delegate", "c-del", `{"key":"c-key","goal":"Foreign gamma task"}`)
				return
			}
			piChatResponse(w, "Gamma done.", "", "", "")
			return
		}
		if isGamma && isActivity {
			piChatResponse(w, "Gamma activity done.", "", "", "")
			return
		}
		if isAlpha && isActivity {
			mu.Lock()
			a1Step++
			step := a1Step
			mu.Unlock()
			if step == 1 {
				piChatResponse(w, "", "read", "a1-r1", `{"path":"secret-note.txt"}`)
				return
			}
			piChatResponse(w, "Alpha activity finished.", "", "", "")
			return
		}
		if isAlpha && !isActivity {
			mu.Lock()
			aStep++
			step := aStep
			a1 := actA1ID
			bID := botBID
			mu.Unlock()
			switch step {
			case 1:
				piChatResponse(w, "", "activity_delegate", "a-del-1", `{"key":"a-key-1","goal":"Alpha recorded trace"}`)
			case 2:
				piChatResponse(w, "Delegated a1.", "", "", "")
			case 3:
				args, _ := json.Marshal(map[string]any{
					"key": "a-del-b", "goal": "Beta granted run",
					"assignee_id": bID, "linked_activity_ids": []string{a1},
				})
				piChatResponse(w, "", "activity_delegate", "a-del-2", string(args))
			default:
				piChatResponse(w, "Delegated b with grant.", "", "", "")
			}
			return
		}
		if isBeta && isActivity {
			mu.Lock()
			bStep++
			mu.Unlock()
			piChatResponse(w, "Beta granted activity done.", "", "", "")
			return
		}
		if strings.Contains(sys, "Bot Identity\nName: Legacy") && isActivity {
			piChatResponse(w, "Legacy continued activity done.", "", "", "")
			return
		}
		http.Error(w, "unexpected route", http.StatusBadRequest)
	}))
	t.Cleanup(provider.Close)

	opts := pi.ProductOptions{
		NodePath: nodePath, WorkerPath: workerPath,
		StateDir: filepath.Join(root, "pi"), ProfilePath: filepath.Join(root, "contacts.json"),
		CredentialsPath: filepath.Join(root, "connection.json"), FreshProfile: true,
	}
	writeProductFixture(t, opts.CredentialsPath, map[string]any{
		"version": 2, "credential_source": productSource, "model": productModel,
		"context_tokens": 65536, "output_tokens": 4096, "working_directory": workspace, "max_active_tasks": 4,
		"sources": map[string]any{
			productSource:      map[string]string{"api": "openai-chat-completions", "endpoint": provider.URL + "/v1", "api_key": productKey},
			"inactive-archive": map[string]string{"api": "openai-chat-completions", "endpoint": "http://127.0.0.1:9/v1", "api_key": inactiveKey},
		},
	})

	proc := startProductProcess(t, opts)
	proc.open(t, ctx)
	if _, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-conn-1",
		Configuration: pi.ProductConnectionInput{
			APIKey: productKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI: pi.OpenAICompletions, BaseURL: provider.URL + "/v1", Model: productModel,
				ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: workspace, MaxActiveTasks: 4,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{SavedRevision: 1, OperationID: "op-apply-1"}); err != nil {
		t.Fatal(err)
	}

	coding := []string{"read"}
	botA, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{OperationID: "b-a", Profile: pi.ProductBotProfile{Name: "Alpha", CodingTools: coding}})
	if err != nil {
		t.Fatal(err)
	}
	botB, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{OperationID: "b-b", Profile: pi.ProductBotProfile{Name: "Beta", CodingTools: coding}})
	if err != nil {
		t.Fatal(err)
	}
	botC, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{OperationID: "b-c", Profile: pi.ProductBotProfile{Name: "Gamma", CodingTools: coding}})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = botB.BotID
	mu.Unlock()

	sendAndSettle := func(convID, opPrefix, text string, draftRev uint64) {
		if _, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
			ProductText:               pi.ProductText{Text: text},
			ExpectedRevision:          draftRev, OperationID: opPrefix + "-draft",
		}); err != nil {
			t.Fatal(err)
		}
		receipt, err := proc.client.Send(ctx, pi.ProductSendParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
			DraftRevision:             draftRev + 1, OperationID: opPrefix + "-send",
		})
		if err != nil {
			t.Fatal(err)
		}
		waitSettledExecution(t, ctx, proc, receipt.Execution.ExecutionID)
	}
	settleBotActivity := func(botID string) pi.ProductActivity {
		list, err := proc.client.ListActivities(ctx, pi.ProductActivityListParams{BotID: botID})
		if err != nil || len(list.Items) == 0 {
			t.Fatalf("list activities for %s = %+v, %v", botID, list, err)
		}
		detail, err := proc.client.GetActivity(ctx, list.Items[0].ActivityID)
		if err != nil || len(detail.Executions) == 0 {
			t.Fatalf("get activity %s = %+v, %v", list.Items[0].ActivityID, detail, err)
		}
		if detail.Executions[0].State == "running" || detail.Executions[0].State == "starting" {
			waitSettledExecution(t, ctx, proc, detail.Executions[0].ExecutionID)
		}
		return detail
	}

	sendAndSettle(botC.CurrentConversationID, "c1", "Delegate gamma", 0)
	actC := settleBotActivity(botC.BotID)
	actCID = actC.ActivityID

	sendAndSettle(botA.CurrentConversationID, "a1", "Delegate alpha", 0)
	actA1 := settleBotActivity(botA.BotID)
	mu.Lock()
	actA1ID = actA1.ActivityID
	execA1ID = actA1.Executions[0].ExecutionID
	mu.Unlock()

	convANow, err := proc.client.GetConversation(ctx, botA.CurrentConversationID)
	if err != nil {
		t.Fatal(err)
	}
	sendAndSettle(botA.CurrentConversationID, "a2", "Delegate beta with grant", convANow.Draft.Revision)
	actB := settleBotActivity(botB.BotID)
	actBID = actB.ActivityID

	origTraces, err := proc.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actA1ID},
		ExecutionID:           execA1ID,
	})
	if err != nil || len(origTraces.Items) < 3 {
		t.Fatalf("orig traces = %+v, %v", origTraces, err)
	}

	backupPath := filepath.Join(root, "m0a-backup.json")
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatalf("BackupProfile failed: %v", err)
	}
	backupInfo, err := os.Stat(backupPath)
	if err != nil || backupInfo.Mode().Perm() != 0600 {
		t.Fatalf("backup permissions = %v err=%v, want 0600", backupInfo.Mode().Perm(), err)
	}
	backupBytes := productRead(t, backupPath)
	if bytes.Contains(backupBytes, []byte(inactiveKey)) || bytes.Contains(backupBytes, []byte(productKey)) {
		t.Fatalf("backup leaked credential bytes")
	}
	var backupEnv map[string]json.RawMessage
	if err := json.Unmarshal(backupBytes, &backupEnv); err != nil {
		t.Fatal(err)
	}
	if len(backupEnv["backup"]) == 0 || len(backupEnv["recovery"]) == 0 {
		t.Fatalf("backup missing top-level backup or recovery envelope")
	}
	if bytes.Contains(backupEnv["recovery"], []byte(`"traces"`)) {
		t.Fatalf("backup payload placed inside recovery instead of top-level")
	}
	mirrorBytes := productRead(t, opts.ProfilePath)
	if bytes.Contains(mirrorBytes, []byte(`"backup"`)) {
		t.Fatalf("compact contacts.json mirror must exclude backup payload")
	}

	recoveryCopy, err := store.InspectStoreRecoveryCopy(backupPath)
	if err != nil || recoveryCopy.Contacts < 3 || recoveryCopy.Issues < 3 {
		t.Fatalf("InspectStoreRecoveryCopy = %+v, %v", recoveryCopy, err)
	}

	var tampered map[string]any
	if err := json.Unmarshal(backupBytes, &tampered); err != nil {
		t.Fatal(err)
	}
	for _, rawAct := range tampered["activities"].([]any) {
		m := rawAct.(map[string]any)
		if m["activityId"] == actBID {
			m["grantedLinkedActivityIds"] = []any{actCID}
		}
	}
	badGrantPath := filepath.Join(root, "bad-grant-backup.json")
	writeProductFixture(t, badGrantPath, tampered)
	if _, err := store.InspectStoreRecoveryCopy(badGrantPath); err == nil {
		t.Fatal("InspectStoreRecoveryCopy accepted invalid direct-authority grant in backup")
	}

	proc.shutdown(t, ctx)
	providerCallsBeforeRestore := providerCalls.Load()

	restoreRoot := t.TempDir()
	restoreProfile := filepath.Join(restoreRoot, "contacts.json")
	if err := os.WriteFile(restoreProfile, []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	target, err := store.InspectStoreRecoveryTarget(restoreProfile)
	if err != nil {
		t.Fatalf("InspectStoreRecoveryTarget on corrupt file failed: %v", err)
	}
	res, err := store.RestoreStoreRecoveryCopyWithRename(recoveryCopy, target, os.Rename)
	if err != nil || res.Outcome != store.StoreRecoveryVerifiedInstalled {
		t.Fatalf("RestoreStoreRecoveryCopyWithRename = %+v, %v", res, err)
	}
	if _, err := store.InspectStoreRecoveryTarget(restoreProfile); err == nil {
		t.Fatal("InspectStoreRecoveryTarget did not recognize installed backup envelope as readable")
	}
	if _, _, err := store.InspectProductProfileBytes(productRead(t, restoreProfile), false); err != nil {
		t.Fatalf("InspectProductProfileBytes(profile mode) rejected installed backup envelope: %v", err)
	}

	restoreOpts := pi.ProductOptions{
		NodePath: nodePath, WorkerPath: workerPath,
		StateDir: filepath.Join(restoreRoot, "pi"), ProfilePath: restoreProfile,
		CredentialsPath: filepath.Join(restoreRoot, "connection.json"),
	}
	rProc1 := startProductProcess(t, restoreOpts)
	rProc1.open(t, ctx)
	rTraces1, err := rProc1.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actA1ID},
		ExecutionID:           execA1ID,
	})
	if err != nil || len(rTraces1.Items) != len(origTraces.Items) || rTraces1.Coverage != "recorded" {
		t.Fatalf("restored first-open traces = %+v, want %d recorded", rTraces1, len(origTraces.Items))
	}
	if bytes.Contains(productRead(t, restoreProfile), []byte(`"backup"`)) {
		t.Fatal("worker first-open did not republish compact contacts.json without backup")
	}
	rProc1.shutdown(t, ctx)

	rProc2 := startProductProcess(t, restoreOpts)
	rProc2.open(t, ctx)
	rTraces2, err := rProc2.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actA1ID},
		ExecutionID:           execA1ID,
	})
	if err != nil || len(rTraces2.Items) != len(origTraces.Items) {
		t.Fatalf("restored second-open traces = %+v", rTraces2)
	}
	page1, err := rProc2.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actA1ID},
		ExecutionID:           execA1ID,
		ProductPageParams:     pi.ProductPageParams{Limit: 1},
	})
	if err != nil || page1.NextCursor == nil || *page1.NextCursor == "" {
		t.Fatalf("page1 trace cursor missing: %+v, %v", page1, err)
	}
	grantsInDB := runNodeSQLite(t, nodePath, filepath.Join(restoreRoot, "nagi.db"), `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const row = db.prepare('SELECT granted_linked_activity_ids_json FROM activities WHERE activity_id = ?').get(JSON.stringify(process.argv[2]));
db.close();
console.log(row.granted_linked_activity_ids_json);
`, actBID)
	if !strings.Contains(grantsInDB, actA1ID) {
		t.Fatalf("restored DB lost grantedLinkedActivityIds: %s", grantsInDB)
	}
	rProc2.shutdown(t, ctx)

	if providerCalls.Load() != providerCallsBeforeRestore {
		t.Fatalf("backup restore and reopens triggered %d unexpected provider calls", providerCalls.Load()-providerCallsBeforeRestore)
	}

	productSQLiteModule := filepath.Join(filepath.Dir(workerPath), "product-sqlite.js")
	retentionOut := runNodeSQLite(t, nodePath, restoreProfile, `
import(process.argv[2]).then(({ ProductSQLite }) => {
  const s = new ProductSQLite(process.argv[1]);
  s.open({ profilePath: process.argv[1], credentialsPath: process.argv[1] + '.cred', fresh: false }, undefined);
  for (let i = 0; i < 515; i++) {
    s.appendTrace({ activityId: process.argv[3], executionId: process.argv[4], kind: 'tool_started', tool: 'read', callId: 'c-' + i, target: 'f.txt' });
  }
  const b = s.traceBounds(process.argv[3], process.argv[4]);
  s.close();
  console.log(JSON.stringify(b));
});
`, "file://"+productSQLiteModule, actA1ID, execA1ID)
	if !strings.Contains(retentionOut, `"evicted":`) || strings.Contains(retentionOut, `"evicted":0`) {
		t.Fatalf("retention cap did not advance evicted_through_seq: %s", retentionOut)
	}

	rProc3 := startProductProcess(t, restoreOpts)
	rProc3.open(t, ctx)
	_, prunedErr := rProc3.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actA1ID},
		ExecutionID:           execA1ID,
		ProductPageParams:     pi.ProductPageParams{Cursor: *page1.NextCursor, Limit: 1},
	})
	piRejected(t, prunedErr, "invalid_cursor")

	var totalRetained int
	var cur string
	for {
		p, err := rProc3.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: actA1ID},
			ExecutionID:           execA1ID,
			ProductPageParams:     pi.ProductPageParams{Cursor: cur, Limit: 50},
		})
		if err != nil {
			t.Fatalf("paginate retained traces: %v", err)
		}
		if !p.HistoryTruncated || p.Coverage != "recorded" {
			t.Fatalf("expected historyTruncated=true recorded coverage: %+v", p)
		}
		totalRetained += len(p.Items)
		if p.NextCursor == nil || *p.NextCursor == "" {
			break
		}
		cur = *p.NextCursor
	}
	if totalRetained != 512 {
		t.Fatalf("retained execution trace rows = %d, want 512", totalRetained)
	}
	rProc3.shutdown(t, ctx)

	var legacyNoBackup map[string]any
	if err := json.Unmarshal(backupBytes, &legacyNoBackup); err != nil {
		t.Fatal(err)
	}
	delete(legacyNoBackup, "backup")
	writeProductFixture(t, restoreProfile, legacyNoBackup)
	rProcLegacy := startProductProcess(t, restoreOpts)
	rProcLegacy.open(t, ctx)
	legacyTrace, err := rProcLegacy.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: actA1ID},
		ExecutionID:           execA1ID,
	})
	if err != nil || len(legacyTrace.Items) != 0 || legacyTrace.Coverage != "unrecorded" || legacyTrace.HistoryTruncated {
		t.Fatalf("legacy v1 import did not clear traces or mark unrecorded: %+v, %v", legacyTrace, err)
	}
	rProcLegacy.shutdown(t, ctx)

	v3Root := t.TempDir()
	v3Profile := filepath.Join(v3Root, "contacts.json")
	v3DB := filepath.Join(v3Root, "nagi.db")
	staleMirror := []byte(`{"version":1,"bots":[]}` + "\n")
	if err := os.WriteFile(v3Profile, staleMirror, 0600); err != nil {
		t.Fatal(err)
	}
	staleSum := sha256.Sum256(staleMirror)
	staleDigest := hex.EncodeToString(staleSum[:])

	expectedV3Digest := runNodeSQLite(t, nodePath, v3DB, `
const { DatabaseSync } = require('node:sqlite');
const { createHash } = require('node:crypto');
const db = new DatabaseSync(process.argv[1]);
const staleDigest = process.argv[2];
db.exec(`+"`"+`
  PRAGMA foreign_keys = ON;
  CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
  CREATE TABLE bots (
    bot_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, profile_json TEXT NOT NULL,
    profile_revision INTEGER NOT NULL, archived INTEGER NOT NULL CHECK (archived IN (0, 1)),
    current_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    retained_json TEXT, system_kind TEXT
  ) STRICT;
  CREATE TABLE conversations (
    conversation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE,
    bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, revision INTEGER NOT NULL,
    draft_json TEXT NOT NULL, binding_json TEXT,
    latest_execution_id TEXT REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED
  ) STRICT;
  CREATE TABLE activities (
    activity_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE,
    owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id), origin_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
    revision INTEGER NOT NULL, state TEXT NOT NULL, goal_json TEXT NOT NULL,
    requirements_json TEXT NOT NULL, questions_json TEXT NOT NULL, materials_json TEXT NOT NULL,
    input_files_json TEXT NOT NULL, execution_ids_json TEXT NOT NULL, results_json TEXT NOT NULL,
    binding_json TEXT, retained_json TEXT
  ) STRICT;
  CREATE TABLE executions (
    execution_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, revision INTEGER NOT NULL, state TEXT NOT NULL,
    operation_id TEXT NOT NULL, input_json TEXT NOT NULL, error_json TEXT, engine_run_id TEXT,
    imported_activity_id TEXT REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, retained_json TEXT
  ) STRICT;
  CREATE TABLE operations (
    operation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, method_json TEXT NOT NULL, fingerprint TEXT NOT NULL,
    receipt_json TEXT NOT NULL, reply_json TEXT NOT NULL, reply_error_json TEXT,
    connection_revision INTEGER REFERENCES connections(revision), retained_json TEXT
  ) STRICT;
  CREATE TABLE connections (
    revision INTEGER PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, source_id TEXT NOT NULL,
    configuration_json TEXT NOT NULL
  ) STRICT;
  CREATE TABLE bot_memory_rules (rule_id TEXT PRIMARY KEY, bot_id TEXT NOT NULL REFERENCES bots(bot_id) ON DELETE CASCADE, record_json TEXT NOT NULL) STRICT;
  CREATE TABLE bot_work_logs (log_id TEXT PRIMARY KEY, bot_id TEXT NOT NULL REFERENCES bots(bot_id) ON DELETE CASCADE, date TEXT NOT NULL, created_at TEXT NOT NULL, summary TEXT NOT NULL, tags_text TEXT NOT NULL, record_json TEXT NOT NULL) STRICT;
  CREATE TABLE bot_pending_learnings (learning_id TEXT PRIMARY KEY, bot_id TEXT NOT NULL REFERENCES bots(bot_id) ON DELETE CASCADE, record_json TEXT NOT NULL) STRICT;
  CREATE TABLE bot_memory_meta (bot_id TEXT PRIMARY KEY REFERENCES bots(bot_id) ON DELETE CASCADE, record_json TEXT NOT NULL) STRICT;
  PRAGMA user_version = 3;
`+"`"+`);
const opReceipt = { operationId: 'op-v3', state: 'accepted', target: { activityId: 'a-v3' }, execution: { executionId: 'e-v3', revision: 1, state: 'completed' } };
const profile = {
  version: 1,
  bots: [{ botId: 'b1', profile: { name: 'Legacy', role: '', purpose: '', instructions: '', workspace: '', codingTools: null }, profileRevision: 1, archived: false, currentConversationId: 'c1' }],
  conversations: [{ conversationId: 'c1', botId: 'b1', revision: 1, draft: { revision: 0, text: '', materials: [] } }],
  activities: [{ activityId: 'a-v3', ownerBotId: 'b1', originConversationId: 'c1', revision: 1, state: 'needsAttention', goal: 'Legacy v3', requirements: [], questions: [], materials: [], inputFiles: [], executionIds: ['e-v3'], results: [] }],
  executions: [{ executionId: 'e-v3', revision: 1, state: 'completed', operationId: 'op-v3', input: { revision: 0, text: 'run', materials: [] } }],
  operations: [{ method: 'activity.continue', fingerprint: 'fp-v3', receipt: opReceipt, reply: {} }],
  connections: [], savedRevision: null, appliedRevision: null
};
const canon = v => Array.isArray(v) ? '[' + v.map(canon).join(',') + ']' : v && typeof v === 'object' ? '{' + Object.keys(v).filter(k => v[k] !== undefined).sort().map(k => JSON.stringify(k) + ':' + canon(v[k])).join(',') + '}' : JSON.stringify(v);
const digest = createHash('sha256').update(Buffer.from(canon(profile) + '\n')).digest('hex');
db.exec('BEGIN IMMEDIATE');
db.prepare('INSERT INTO bots VALUES (?, ?, ?, ?, ?, ?, ?, ?)').run('"b1"', 0, JSON.stringify(profile.bots[0].profile), 1, 0, '"c1"', null, null);
db.prepare('INSERT INTO conversations VALUES (?, ?, ?, ?, ?, ?, ?)').run('"c1"', 0, '"b1"', 1, JSON.stringify(profile.conversations[0].draft), null, null);
db.prepare('INSERT INTO activities VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)').run('"a-v3"', 0, '"b1"', '"c1"', 1, 'needsAttention', '"Legacy v3"', '[]', '[]', '[]', '[]', '["e-v3"]', '[]', null, null);
db.prepare('INSERT INTO executions VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)').run('"e-v3"', 0, 1, 'completed', '"op-v3"', JSON.stringify(profile.executions[0].input), null, null, null, null);
db.prepare('INSERT INTO operations VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)').run('"op-v3"', 0, '"activity.continue"', 'fp-v3', JSON.stringify(opReceipt), '{}', null, null, null);
db.prepare('INSERT INTO meta VALUES (?, ?)').run('saved_revision', 'null');
db.prepare('INSERT INTO meta VALUES (?, ?)').run('applied_revision', 'null');
db.prepare('INSERT INTO meta VALUES (?, ?)').run('snapshot_sha256', digest);
db.prepare('INSERT INTO meta VALUES (?, ?)').run('pending_snapshot_previous', JSON.stringify(staleDigest));
db.exec('COMMIT');
db.close();
console.log(digest);
`, staleDigest)

	v3Opts := pi.ProductOptions{
		NodePath: nodePath, WorkerPath: workerPath,
		StateDir: filepath.Join(v3Root, "pi"), ProfilePath: v3Profile,
		CredentialsPath: filepath.Join(v3Root, "connection.json"),
	}
	v3Proc := startProductProcess(t, v3Opts)
	v3Proc.open(t, ctx)
	v3Trace, err := v3Proc.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: "a-v3"},
	})
	if err != nil || v3Trace.Coverage != "unrecorded" || len(v3Trace.Items) != 0 {
		t.Fatalf("v3 migrated activity trace = %+v, %v", v3Trace, err)
	}
	repairedBytes := productRead(t, v3Profile)
	repairedSum := sha256.Sum256(repairedBytes)
	if hex.EncodeToString(repairedSum[:]) != expectedV3Digest {
		t.Fatalf("v3 pending mirror not repaired: got %s want %s", hex.EncodeToString(repairedSum[:]), expectedV3Digest)
	}
	if _, err := v3Proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-v3-conn",
		Configuration: pi.ProductConnectionInput{
			APIKey: productKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI: pi.OpenAICompletions, BaseURL: provider.URL + "/v1", Model: productModel,
				ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: workspace, MaxActiveTasks: 2,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := v3Proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{SavedRevision: 1, OperationID: "op-v3-apply"}); err != nil {
		t.Fatal(err)
	}
	mixedCont, err := v3Proc.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: "a-v3"},
		ProductText:           pi.ProductText{Text: "Add v4 recorded execution"},
		OperationID:           "op-v3-cont",
		ExpectedRevision:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitSettledExecution(t, ctx, v3Proc, mixedCont.Execution.ExecutionID)
	mixedTrace, err := v3Proc.client.ActivityTrace(ctx, pi.ProductActivityTraceParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: "a-v3"},
	})
	if err != nil || mixedTrace.Coverage != "mixed" || len(mixedTrace.Items) == 0 {
		t.Fatalf("expected mixed coverage across pre-v4 and v4 executions: %+v, %v", mixedTrace, err)
	}
	v3Proc.shutdown(t, ctx)

	v4Check := runNodeSQLite(t, nodePath, v3DB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const version = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
const pending = db.prepare("SELECT value FROM meta WHERE key = 'pending_snapshot_previous'").get();
db.exec('PRAGMA user_version = 10');
db.close();
console.log(JSON.stringify({ version, fk, hasPending: !!pending }));
`)
	if v4Check != `{"version":9,"fk":0,"hasPending":false}` {
		t.Fatalf("v3->v9 pending repair state = %s", v4Check)
	}

	v5Client, startErr := pi.StartProduct(v3Opts)
	if startErr == nil {
		if _, readyErr := v5Client.Ready(ctx); readyErr == nil {
			if _, openErr := v5Client.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
				t.Fatal("worker admitted SQLite user_version=10")
			}
		}
		_, _ = v5Client.Wait(ctx)
	}
}
