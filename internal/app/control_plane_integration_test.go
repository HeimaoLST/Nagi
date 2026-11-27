//go:build integration

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

type cpToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

type cpProviderReply struct {
	StreamPrefix string
	Entered      chan struct{}
	Release      <-chan struct{}
	Calls        []cpToolCall
	Text         string
}

type cpProviderMessage struct {
	Role       string
	Content    string
	ToolCallID string
}

type cpProviderRequest struct {
	FirstUser   string
	LastUser    string
	LastRole    string
	LastToolID  string
	LastContent string
	Messages    []cpProviderMessage
	ToolResults map[string]string
	Tools       []string
}

type cpProvider struct {
	t        *testing.T
	calls    atomic.Int64
	mu       sync.Mutex
	handler  func(cpProviderRequest) cpProviderReply
	requests []cpProviderRequest
}

func newCPProvider(t *testing.T, handler func(cpProviderRequest) cpProviderReply) *cpProvider {
	t.Helper()
	return &cpProvider{t: t, handler: handler}
}

func (p *cpProvider) setHandler(fn func(cpProviderRequest) cpProviderReply) {
	p.mu.Lock()
	p.handler = fn
	p.mu.Unlock()
}

func (p *cpProvider) snapshotRequests() []cpProviderRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]cpProviderRequest(nil), p.requests...)
}

func decodeCPMessageContent(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(part.Text)
		}
		return b.String()
	}
	return string(raw)
}

func (p *cpProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	p.calls.Add(1)
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
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
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Messages) == 0 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	msgs := make([]cpProviderMessage, 0, len(body.Messages))
	toolResults := make(map[string]string)
	var firstUser, lastUser string
	for _, m := range body.Messages {
		content := decodeCPMessageContent(m.Content)
		msgs = append(msgs, cpProviderMessage{Role: m.Role, Content: content, ToolCallID: m.ToolCallID})
		switch m.Role {
		case "user":
			if firstUser == "" {
				firstUser = content
			}
			lastUser = content
		case "tool":
			if m.ToolCallID != "" {
				toolResults[m.ToolCallID] = content
			}
		}
	}
	tools := make([]string, 0, len(body.Tools))
	for _, item := range body.Tools {
		tools = append(tools, item.Function.Name)
	}
	last := msgs[len(msgs)-1]
	req := cpProviderRequest{
		FirstUser:   firstUser,
		LastUser:    lastUser,
		LastRole:    last.Role,
		LastToolID:  last.ToolCallID,
		LastContent: last.Content,
		Messages:    msgs,
		ToolResults: toolResults,
		Tools:       tools,
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	handler := p.handler
	p.mu.Unlock()
	reply := cpProviderReply{Text: "OK"}
	if handler != nil {
		reply = handler(req)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	writeChunk := func(choice map[string]any) {
		chunk, _ := json.Marshal(map[string]any{
			"id":      "cp-loopback",
			"object":  "chat.completion.chunk",
			"created": 1,
			"model":   "nagi-product-fixture",
			"choices": []any{choice},
		})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		if flusher != nil {
			flusher.Flush()
		}
	}
	if reply.StreamPrefix != "" {
		writeChunk(map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": reply.StreamPrefix}, "finish_reason": nil})
	}
	if reply.Entered != nil {
		select {
		case <-reply.Entered:
		default:
			close(reply.Entered)
		}
	}
	if reply.Release != nil {
		select {
		case <-reply.Release:
		case <-r.Context().Done():
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	finish := "stop"
	if len(reply.Calls) > 0 {
		finish = "tool_calls"
		toolCalls := make([]any, 0, len(reply.Calls))
		for i, call := range reply.Calls {
			args, _ := json.Marshal(call.Args)
			toolCalls = append(toolCalls, map[string]any{
				"index": i,
				"id":    call.ID,
				"type":  "function",
				"function": map[string]string{
					"name":      call.Name,
					"arguments": string(args),
				},
			})
		}
		delta := map[string]any{"tool_calls": toolCalls}
		if reply.StreamPrefix == "" {
			delta["role"] = "assistant"
		}
		writeChunk(map[string]any{"index": 0, "delta": delta, "finish_reason": nil})
	} else if reply.Text != "" {
		delta := map[string]any{"content": reply.Text}
		if reply.StreamPrefix == "" {
			delta["role"] = "assistant"
		}
		writeChunk(map[string]any{"index": 0, "delta": delta, "finish_reason": nil})
	} else if reply.StreamPrefix == "" {
		writeChunk(map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "OK"}, "finish_reason": nil})
	}
	writeChunk(map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish})
	io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func clickInActivityScroll(w *productWorkbench, ct *contact, label string) {
	w.t.Helper()
	var err error
	var visible []string
	w.pump.do(func() {
		if w.view.FramePending() {
			w.view.Frame()
		}
		for _, y := range []float32{ct.activity.scroll.Y, 0, 120, 260, 420, 620, 900, 1300, 1800, 2400} {
			ct.activity.scroll.Y = y
			w.view.Frame()
			if r, ok := w.view.Find(label); ok && r.W > 0 && r.H > 0 {
				if clickErr := w.view.Click(label); clickErr == nil {
					w.view.Frame()
					err = nil
					return
				} else {
					err = clickErr
				}
			}
		}
		if err == nil {
			err = fmt.Errorf("control %q not found in activity scroll", label)
		}
		visible = w.view.Texts()
	})
	if err != nil {
		w.t.Fatalf("clickInActivityScroll %q: %v, visible=%v", label, err, visible)
	}
}

func hasTextInActivityScrollOnPump(w *productWorkbench, ct *contact, needle string) bool {
	saved := ct.activity.scroll.Y
	found := false
	for _, y := range []float32{saved, 0, 180, 420, 760, 1200} {
		ct.activity.scroll.Y = y
		w.view.Frame()
		if w.view.HasText(needle) {
			found = true
			break
		}
	}
	ct.activity.scroll.Y = saved
	w.view.Frame()
	return found
}

func hasTextInActivityScroll(w *productWorkbench, ct *contact, needle string) bool {
	found := false
	w.pump.do(func() {
		found = hasTextInActivityScrollOnPump(w, ct, needle)
	})
	return found
}

func safeCloseChan(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func TestPiAppControlPlaneCP1LifecycleProgressTraceAndApproval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	const (
		lifecycleActID = "act-cp1-lifecycle"
		approvalActID  = "act-cp1-approval"
		lifecycleGoal  = "CP1_LIFECYCLE_GOAL"
		approvalGoal   = "CP1_APPROVAL_GOAL"
		scriptSource   = "export default async function(input, ctx) {\n  return { workspace: ctx.workspace };\n}\n"
	)

	step1Done := make(chan struct{})
	releaseStep2 := make(chan struct{})
	step2Done := make(chan struct{})
	releaseBatch := make(chan struct{})

	var lifecycleTurn atomic.Int32
	var approvalTurn atomic.Int32

	provider := newCPProvider(t, func(req cpProviderRequest) cpProviderReply {
		if strings.Contains(req.FirstUser, lifecycleGoal) {
			turn := lifecycleTurn.Add(1)
			switch turn {
			case 1:
				return cpProviderReply{
					Calls: []cpToolCall{{
						ID:   "prog-1",
						Name: "activity_progress",
						Args: map[string]any{"stage": "Scanning lot manifest", "step": 1, "total_steps": 12},
					}},
				}
			case 2:
				return cpProviderReply{
					Entered: step1Done,
					Release: releaseStep2,
					Calls: []cpToolCall{{
						ID:   "prog-2",
						Name: "activity_progress",
						Args: map[string]any{"stage": "Verifying checksums", "step": 2, "total_steps": 12},
					}},
				}
			case 3:
				calls := make([]cpToolCall, 0, 10)
				for step := 3; step <= 12; step++ {
					calls = append(calls, cpToolCall{
						ID:   fmt.Sprintf("prog-%d", step),
						Name: "activity_progress",
						Args: map[string]any{"stage": fmt.Sprintf("Checking batch %d", step), "step": step, "total_steps": 12},
					})
				}
				return cpProviderReply{
					Entered: step2Done,
					Release: releaseBatch,
					Calls:   calls,
				}
			default:
				return cpProviderReply{Text: "All 12 intake batches verified."}
			}
		}
		if strings.Contains(req.FirstUser, approvalGoal) {
			if approvalTurn.Add(1) == 1 {
				return cpProviderReply{
					Calls: []cpToolCall{{
						ID:   "call-cp1-skill",
						Name: "skill_exec",
						Args: map[string]any{"skill_id": "case-script", "script": "run", "input": map[string]any{}},
					}},
				}
			}
			return cpProviderReply{Text: "Approval request handled cleanly."}
		}
		return cpProviderReply{Text: "Foreground acknowledged."}
	})

	root := prepareProductWorkbench(t, provider)
	t.Cleanup(func() {
		safeCloseChan(releaseStep2)
		safeCloseChan(releaseBatch)
	})
	if err := writeProfileJSON(guiStorePath(), storedState{
		Version:  contactStoreVersion,
		Selected: "cedar",
		Contacts: []storedContact{{ID: "cedar", Name: "Cedar", Workspace: root, Draft: "Source conversation turn"}},
		Issues: []storedActivity{
			{ID: lifecycleActID, ContactID: "cedar", Goal: lifecycleGoal, Status: activityStatusNotStarted, RequirementsRevision: 1},
			{ID: approvalActID, ContactID: "cedar", Goal: approvalGoal, Status: activityStatusNotStarted, RequirementsRevision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	msgs := locales.In("en")
	w := openProductWorkbench(t)
	var cedar *contact
	w.pump.do(func() { cedar = w.app.contactByID("cedar") })
	w.wait(func() bool { return cedar != nil && cedar.Loaded })

	w.click(msgs.SendAction())
	w.wait(func() bool { return cedar.SendState == sendIdle && len(cedar.History) == 2 })

	cont, err := w.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: lifecycleActID},
		ProductText:           pi.ProductText{Text: lifecycleGoal},
		ExpectedRevision:      1,
		OperationID:           "op-cp1-start-lifecycle",
	})
	if err != nil || cont.Execution == nil {
		t.Fatalf("ContinueActivity lifecycle: %+v, %v", cont, err)
	}
	execID := cont.Execution.ExecutionID

	select {
	case <-step1Done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for step 1")
	}

	w.click(msgs.WorkActivity())
	w.wait(func() bool { return cedar.activity.open && len(cedar.activity.items) >= 1 })
	clickInActivityScroll(w, cedar, msgs.IssueRowLabel(lifecycleGoal))
	w.wait(func() bool {
		return cedar.activity.activity != nil &&
			cedar.activity.activity.ActivityID == lifecycleActID &&
			cedar.activity.activity.LatestProgress != nil &&
			cedar.activity.activity.LatestProgress.Stage == "Scanning lot manifest" &&
			w.view.HasText(msgs.ActivityProgressSteps(1, 12))
	})

	var rev1, seq1 uint64
	w.pump.do(func() {
		rev1 = cedar.activity.activity.Revision
		seq1 = cedar.activity.activity.LatestProgress.ProgressSeq
	})

	clickInActivityScroll(w, cedar, msgs.ActivityTrace())
	traceTarget := productActivityExecution(lifecycleActID, execID)
	w.wait(func() bool {
		st := cedar.activity.traces[traceTarget]
		return st != nil && st.open && st.loaded && len(st.page.Items) > 0
	})
	var traceCount1 int
	w.pump.do(func() { traceCount1 = len(cedar.activity.traces[traceTarget].page.Items) })

	close(releaseStep2)
	select {
	case <-step2Done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for step 2")
	}

	w.wait(func() bool {
		status, ok := cedar.activeActivities[lifecycleActID]
		st := cedar.activity.traces[traceTarget]
		return ok &&
			status.Revision == rev1 &&
			status.LatestProgress != nil &&
			status.LatestProgress.ProgressSeq > seq1 &&
			status.LatestProgress.Stage == "Verifying checksums" &&
			cedar.activity.activity != nil &&
			cedar.activity.activity.LatestProgress != nil &&
			cedar.activity.activity.LatestProgress.Stage == "Verifying checksums" &&
			w.view.HasText(msgs.ActivityProgressSteps(2, 12)) &&
			st != nil && !st.reading && len(st.page.Items) > traceCount1
	})

	close(releaseBatch)
	w.wait(func() bool {
		st := cedar.activity.traces[traceTarget]
		return cedar.activity.activity != nil &&
			cedar.activity.activity.State == "completed" &&
			st != nil && !st.reading && len(st.page.Items) == 20 && st.page.NextCursor != nil
	})

	clickInActivityScroll(w, cedar, msgs.ActivityLoadMore())
	w.wait(func() bool {
		st := cedar.activity.traces[traceTarget]
		return st != nil && !st.reading && len(st.page.Items) > 20 && st.page.NextCursor == nil
	})

	clickInActivityScroll(w, cedar, msgs.ActivityAcknowledge())
	w.wait(func() bool {
		_, stillActive := cedar.activeActivities[lifecycleActID]
		return !cedar.activity.changing &&
			cedar.activity.activity != nil &&
			cedar.activity.activity.AcknowledgedAt != "" &&
			!stillActive &&
			!w.view.HasText(msgs.ActivityAcknowledge())
	})

	clickInActivityScroll(w, cedar, msgs.ActivityClose())
	w.wait(func() bool {
		return !cedar.activity.changing &&
			cedar.activity.activity != nil &&
			cedar.activity.activity.ClosedAt != "" &&
			w.view.HasText(msgs.ActivityClosed())
	})

	clickInActivityScroll(w, cedar, msgs.IssueOpenResultRun("1"))
	w.wait(func() bool {
		return cedar.activity.result != nil && strings.Contains(cedar.activity.result.Text, "All 12 intake batches verified.")
	})
	w.click(msgs.BackLabel())
	w.wait(func() bool { return cedar.activity.result == nil && cedar.activity.activity != nil })

	clickInActivityScroll(w, cedar, msgs.SourceChat())
	w.wait(func() bool {
		return w.app.priorChats != nil && !w.app.priorChats.reading && len(w.app.priorChats.history.entries) == 2
	})
	w.click(msgs.BackLabel())
	w.wait(func() bool { return w.app.priorChats == nil && cedar.activity.activity != nil })

	if !hasTextInActivityScroll(w, cedar, msgs.ActivityTrace()) {
		t.Fatal("closed activity lost readable Execution trace")
	}

	var backErr string
	w.pump.do(func() {
		w.app.refreshProductActivities(cedar, false)
		if !cedar.activity.reading {
			backErr = "expected in-flight activity refresh before clicking Back"
			return
		}
		if err := w.view.Click(msgs.BackLabel()); err != nil {
			backErr = fmt.Sprintf("click Back during in-flight refresh: %v", err)
			return
		}
		w.view.Frame()
	})
	if backErr != "" {
		t.Fatal(backErr)
	}
	w.wait(func() bool { return !cedar.activity.reading && cedar.activity.loaded })
	var lateID string
	var lateAct *pi.ProductActivity
	w.pump.do(func() {
		lateID = cedar.activity.activityID
		lateAct = cedar.activity.activity
		cedar.activity.open = false
		w.view.Frame()
	})
	if lateID != "" || lateAct != nil {
		t.Fatalf("late detail read repopulated activity after Back: id=%q activity=%+v", lateID, lateAct)
	}

	if _, err := w.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: "cedar", SkillID: "case-script"},
		Name:            "Case script",
		Description:     "Run verification check.",
		Content:         "Execute run script.",
		Manifest: &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"run": {
					Entry:       "scripts/run.mjs",
					Description: "Verify workspace.",
					TimeoutMS:   10000,
					Permissions: pi.ProductSkillPermissions{WorkspaceRead: true},
				},
			},
		},
		Scripts:     map[string]string{"scripts/run.mjs": scriptSource},
		OperationID: "op-cp1-save-skill",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := w.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: approvalActID},
		ProductText:           pi.ProductText{Text: approvalGoal},
		ExpectedRevision:      1,
		OperationID:           "op-cp1-start-approval",
	}); err != nil {
		t.Fatal(err)
	}

	w.wait(func() bool {
		return w.view.HasText(msgs.CapPendingApproval("case-script", "run")) &&
			w.view.HasText(msgs.CapReviewApprove()) &&
			w.view.HasText(msgs.CapDeny())
	})

	w.click(msgs.CapReviewApprove())
	w.wait(func() bool {
		return w.app.capabilities.review != nil &&
			w.app.capabilities.review.read.ready &&
			!w.app.capabilities.review.read.loading &&
			w.view.HasText(msgs.CapApproveBot())
	})
	w.click(msgs.CreateBotCancel())
	w.wait(func() bool { return w.app.capabilities.review == nil })

	w.pump.do(func() {
		cedar.activity.open = true
		w.app.openProductActivity(cedar, approvalActID)
		w.view.Frame()
	})
	w.wait(func() bool {
		return cedar.activity.activity != nil &&
			cedar.activity.activity.ActivityID == approvalActID &&
			len(cedar.activity.activity.PendingApprovalRequestIDs) == 1 &&
			hasTextInActivityScrollOnPump(w, cedar, msgs.CapDeny())
	})

	clickInActivityScroll(w, cedar, msgs.CapDeny())
	w.wait(func() bool {
		if w.app.capabilitiesBusy() || cedar.activity.activity == nil {
			return false
		}
		return cedar.activity.activity.State == "needsAttention" &&
			len(cedar.activity.activity.PendingApprovalRequestIDs) == 0 &&
			len(cedar.activity.activity.ApprovalRequests) == 1 &&
			cedar.activity.activity.ApprovalRequests[0].State == "denied"
	})

	w.shutdown(ctx)
}

func TestPiAppControlPlaneCP2NoticeStripStructuredOptionsAndAutoAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	const (
		lockedActID = "act-cp2-locked"
		freeActID   = "act-cp2-free"
		autoActID   = "act-cp2-auto"
		lockedGoal  = "CP2_OPT_LOCKED"
		freeGoal    = "CP2_OPT_FREE"
		autoGoal    = "CP2_BOT_AUTO"
		keepDraft   = "Keep my unsent foreground draft"
	)

	autoTurn2Entered := make(chan struct{})
	releaseAutoTurn2 := make(chan struct{})

	var (
		mu             sync.Mutex
		lockedAnswer   string
		freeAnswer     string
		autoToolResult string
		lockedAsked    bool
		freeAsked      bool
		autoAsked      bool
		proposeCalled  bool
	)

	provider := newCPProvider(t, func(req cpProviderRequest) cpProviderReply {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(req.FirstUser, "CP2_PROPOSE_RULE"):
			if !proposeCalled && req.LastRole == "user" {
				proposeCalled = true
				return cpProviderReply{
					Calls: []cpToolCall{{
						ID:   "prop-fmt-1",
						Name: "memory_propose_learning",
						Args: map[string]any{"text": "formatting: Markdown", "reason": "User prefers Markdown formatting."},
					}},
				}
			}
			return cpProviderReply{Text: "Proposed formatting preference."}
		case strings.Contains(req.FirstUser, lockedGoal):
			if !lockedAsked && req.LastRole == "user" {
				lockedAsked = true
				return cpProviderReply{
					Calls: []cpToolCall{{
						ID:   "ask-locked-1",
						Name: "activity_ask",
						Args: map[string]any{
							"text":            "Which intake batch should I verify?",
							"allow_free_text": false,
							"options": []map[string]any{
								{"id": "batch_14", "label": "Batch fourteen", "description": "Primary verified batch", "recommended": true},
								{"id": "batch_16", "label": "Batch sixteen", "description": "Fallback lot"},
							},
						},
					}},
				}
			}
			lockedAnswer = req.ToolResults["ask-locked-1"]
			return cpProviderReply{Text: "Verified Batch fourteen."}
		case strings.Contains(req.FirstUser, freeGoal):
			if !freeAsked && req.LastRole == "user" {
				freeAsked = true
				return cpProviderReply{
					Calls: []cpToolCall{{
						ID:   "ask-free-1",
						Name: "activity_ask",
						Args: map[string]any{
							"text":            "What report style do you prefer?",
							"allow_free_text": true,
							"options": []map[string]any{
								{"id": "summary", "label": "Summary", "recommended": true},
								{"id": "detailed", "label": "Detailed"},
							},
						},
					}},
				}
			}
			freeAnswer = req.ToolResults["ask-free-1"]
			return cpProviderReply{Text: "Generated custom style report."}
		case strings.Contains(req.FirstUser, autoGoal):
			if !autoAsked && req.LastRole == "user" {
				autoAsked = true
				return cpProviderReply{
					Calls: []cpToolCall{{
						ID:   "ask-auto-1",
						Name: "activity_ask",
						Args: map[string]any{
							"text":            "Which output format should I use?",
							"allow_free_text": false,
							"preference_key":  "formatting",
							"options": []map[string]any{
								{"id": "markdown", "label": "Markdown"},
								{"id": "plain_text", "label": "Plain text"},
							},
						},
					}},
				}
			}
			autoToolResult = req.ToolResults["ask-auto-1"]
			return cpProviderReply{
				StreamPrefix: "Formatting in Markdown...",
				Entered:      autoTurn2Entered,
				Release:      releaseAutoTurn2,
				Text:         " Done.",
			}
		default:
			return cpProviderReply{Text: "OK"}
		}
	})

	root := prepareProductWorkbench(t, provider)
	t.Cleanup(func() { safeCloseChan(releaseAutoTurn2) })
	if err := writeProfileJSON(guiStorePath(), storedState{
		Version:  contactStoreVersion,
		Selected: "cedar",
		Contacts: []storedContact{{ID: "cedar", Name: "Cedar", Workspace: root, Draft: keepDraft}},
		Issues: []storedActivity{
			{ID: lockedActID, ContactID: "cedar", Goal: lockedGoal, Status: activityStatusNotStarted, RequirementsRevision: 1},
			{ID: freeActID, ContactID: "cedar", Goal: freeGoal, Status: activityStatusNotStarted, RequirementsRevision: 1},
			{ID: autoActID, ContactID: "cedar", Goal: autoGoal, Status: activityStatusNotStarted, RequirementsRevision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	msgs := locales.In("en")
	w := openProductWorkbench(t)
	var cedar *contact
	w.pump.do(func() { cedar = w.app.contactByID("cedar") })
	w.wait(func() bool { return cedar != nil && cedar.Loaded })

	for _, start := range []struct {
		id, goal, op string
	}{
		{lockedActID, lockedGoal, "op-cp2-start-locked"},
		{freeActID, freeGoal, "op-cp2-start-free"},
	} {
		if _, err := w.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: start.id},
			ProductText:           pi.ProductText{Text: start.goal},
			ExpectedRevision:      1,
			OperationID:           start.op,
		}); err != nil {
			t.Fatal(err)
		}
	}

	w.wait(func() bool {
		return w.view.HasText(msgs.ActivityNextNotice(1, 2)) &&
			w.view.HasText(msgs.ActivityAnswer()) &&
			len(cedar.activityNotices.facts) >= 2
	})

	var errMsg string
	w.pump.do(func() {
		dup := cedar.activityNotices.facts[0]
		before := len(cedar.activityNotices.facts)
		w.app.applyActivityDigest(w.app.productHost(), dup)
		if len(cedar.activityNotices.facts) != before {
			errMsg = fmt.Sprintf("duplicate FactID was not deduplicated: before=%d after=%d", before, len(cedar.activityNotices.facts))
		}
	})
	if errMsg != "" {
		t.Fatal(errMsg)
	}

	composerLabel := msgs.ComposerLabel(cedar.Name)
	w.click(composerLabel)
	var beforeSelected activityNoticeKey
	var caretBefore, focusedBefore bool
	w.pump.do(func() {
		beforeSelected = cedar.activityNotices.selected
		focusedBefore = w.view.Focused(composerLabel)
		_, caretBefore = w.view.TextCaret()
		if cedar.Draft != keepDraft {
			errMsg = fmt.Sprintf("unexpected draft before cycling notice: %q", cedar.Draft)
		}
	})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	if !focusedBefore || !caretBefore {
		t.Fatalf("composer was not focused before clicking Next notice: focused=%v caret=%v", focusedBefore, caretBefore)
	}
	w.click(msgs.ActivityNextNotice(1, 2))
	w.wait(func() bool { return w.view.HasText(msgs.ActivityNextNotice(2, 2)) })
	var focusErr string
	w.pump.do(func() {
		if cedar.Draft != keepDraft {
			errMsg = fmt.Sprintf("cycling notice strip mutated composer draft: got=%q want=%q", cedar.Draft, keepDraft)
			return
		}
		if cedar.activityNotices.selected == beforeSelected {
			errMsg = "cycling notice strip did not advance selected notice"
			return
		}
		focusedAfter := w.view.Focused(composerLabel)
		_, caretAfter := w.view.TextCaret()
		w.view.Type("!")
		w.view.Frame()
		if !focusedAfter || !caretAfter || cedar.Draft != keepDraft+"!" {
			focusErr = fmt.Sprintf("cycling notice strip stole composer focus: focused=%v caret=%v draft=%q want=%q", focusedAfter, caretAfter, cedar.Draft, keepDraft+"!")
		}
		cedar.Draft = keepDraft
		w.view.Frame()
	})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	if focusErr != "" {
		t.Error(focusErr)
	}

	for {
		var hasBatch14 bool
		w.pump.do(func() { hasBatch14 = w.view.HasText("Batch fourteen") })
		if hasBatch14 {
			break
		}
		w.click(msgs.ActivityNextNotice(2, 2))
		w.frame(func() {})
	}
	w.click(msgs.ActivityAnswer())
	w.wait(func() bool {
		return cedar.activity.open && cedar.activity.activity != nil && cedar.activity.activity.ActivityID == lockedActID
	})
	w.pump.do(func() {
		form := cedar.activity.forms[lockedActID]
		if form == nil {
			errMsg = "expected answer form for locked option question"
			return
		}
		if w.app.validActivityControlTarget(cedar, cedar.activity.activity, form.target, form) {
			errMsg = "allow_free_text=false must not allow submitting without an option selection"
			return
		}
		if _, hasCaret := w.view.TextCaret(); hasCaret {
			errMsg = "allow_free_text=false must hide the free-text answer textarea (unexpected active text caret)"
		}
	})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	clickInActivityScroll(w, cedar, "Batch fourteen")
	clickInActivityScroll(w, cedar, msgs.ContactSaveChanges())
	w.wait(func() bool {
		return !cedar.activity.changing && cedar.activity.activity != nil && cedar.activity.activity.State == "completed"
	})

	lockedAct, err := w.client.GetActivity(ctx, lockedActID)
	if err != nil || len(lockedAct.Questions) != 1 {
		t.Fatalf("locked activity readback: %+v, %v", lockedAct, err)
	}
	if lockedAct.Questions[0].SelectedOptionID != "batch_14" || lockedAct.Questions[0].Answer == nil || lockedAct.Questions[0].Answer.Text != "Batch fourteen" || lockedAct.Questions[0].Delivery != "delivered" {
		t.Fatalf("locked option answer mismatch: %+v", lockedAct.Questions[0])
	}
	mu.Lock()
	gotLockedRaw := lockedAnswer
	mu.Unlock()
	if !strings.Contains(gotLockedRaw, `"selectedOptionId":"batch_14"`) || !strings.Contains(gotLockedRaw, `"Batch fourteen"`) {
		t.Fatalf("worker tool result missing locked option: %s", gotLockedRaw)
	}

	w.pump.do(func() {
		cedar.activity.open = false
		cedar.activity.activityID = ""
		cedar.activity.activity = nil
		w.view.Frame()
	})
	w.wait(func() bool { return w.view.HasText("Summary") && w.view.HasText(msgs.ActivityAnswer()) })
	w.click(msgs.ActivityAnswer())
	w.wait(func() bool {
		return cedar.activity.open && cedar.activity.activity != nil && cedar.activity.activity.ActivityID == freeActID
	})
	clickInActivityScroll(w, cedar, "Summary")
	w.pump.do(func() {
		form := cedar.activity.forms[freeActID]
		if form == nil || form.target.SelectedOptionID != "summary" || form.text != "Summary" {
			errMsg = fmt.Sprintf("selecting free-text option pill did not populate draft and option ID: %+v", form)
			return
		}
		w.view.Type(" with dates")
		w.view.Frame()
		if form.target.SelectedOptionID != "" || form.text != "Summary with dates" {
			errMsg = fmt.Sprintf("editing free-text answer did not clear SelectedOptionID: option=%q text=%q", form.target.SelectedOptionID, form.text)
		}
	})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	clickInActivityScroll(w, cedar, msgs.ContactSaveChanges())
	w.wait(func() bool {
		return !cedar.activity.changing && cedar.activity.activity != nil && cedar.activity.activity.State == "completed"
	})

	freeAct, err := w.client.GetActivity(ctx, freeActID)
	if err != nil || len(freeAct.Questions) != 1 {
		t.Fatalf("free-text activity readback: %+v, %v", freeAct, err)
	}
	if freeAct.Questions[0].SelectedOptionID != "" || freeAct.Questions[0].Answer == nil || freeAct.Questions[0].Answer.Text != "Summary with dates" {
		t.Fatalf("free-text answer mismatch: %+v", freeAct.Questions[0])
	}
	mu.Lock()
	gotFreeRaw := freeAnswer
	mu.Unlock()
	if strings.Contains(gotFreeRaw, `"selectedOptionId":"summary"`) || !strings.Contains(gotFreeRaw, `"Summary with dates"`) {
		t.Fatalf("worker tool result retained stale option ID: %s", gotFreeRaw)
	}

	w.shutdown(ctx)

	w2 := openProductWorkbench(t)
	var cedar2 *contact
	w2.pump.do(func() { cedar2 = w2.app.contactByID("cedar") })
	w2.wait(func() bool {
		return cedar2 != nil && cedar2.Loaded &&
			w2.view.HasText(msgs.ActivityAcknowledge()) &&
			w2.view.HasText(msgs.ActivityOpen())
	})

	w2.pump.do(func() {
		w2.app.applyActivityDigest(w2.app.productHost(), pi.ProductActivityDigest{
			FactID:               "stale-fact-answered",
			ActivityID:           lockedActID,
			OwnerBotID:           "cedar",
			OriginConversationID: lockedAct.OriginConversationID,
			Revision:             lockedAct.Revision,
			Kind:                 "question_open",
			QuestionID:           lockedAct.Questions[0].QuestionID,
			Summary:              "Stale question event",
		})
		w2.view.Frame()
	})
	w2.wait(func() bool {
		return !cedar2.activityNotices.reading && cedar2.activityNotices.loaded == cedar2.activityNotices.version
	})
	var reopenedAnswered bool
	w2.pump.do(func() { reopenedAnswered = w2.view.HasText(msgs.ActivityAnswer()) })
	if reopenedAnswered {
		t.Fatal("stale question_open digest reopened an already answered question")
	}

	if _, err := w2.client.AcknowledgeActivity(ctx, pi.ProductActivityAcknowledgeParams{
		ProductActivityExecutionParams: productActivityExecution(lockedActID, lockedAct.Executions[len(lockedAct.Executions)-1].ExecutionID),
		ExpectedRevision:               lockedAct.Revision,
		OperationID:                    "op-cp2-ack-locked",
	}); err != nil {
		t.Fatal(err)
	}
	lockedAfterAck, err := w2.client.GetActivity(ctx, lockedActID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w2.client.CloseActivity(ctx, pi.ProductActivityCloseParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: lockedActID},
		ExpectedRevision:      lockedAfterAck.Revision,
		OperationID:           "op-cp2-close-locked",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w2.client.AcknowledgeActivity(ctx, pi.ProductActivityAcknowledgeParams{
		ProductActivityExecutionParams: productActivityExecution(freeActID, freeAct.Executions[len(freeAct.Executions)-1].ExecutionID),
		ExpectedRevision:               freeAct.Revision,
		OperationID:                    "op-cp2-ack-free",
	}); err != nil {
		t.Fatal(err)
	}
	w2.wait(func() bool { return len(activityNotices(cedar2)) == 0 })

	w2.pump.do(func() {
		w2.app.applyActivityDigest(w2.app.productHost(), pi.ProductActivityDigest{
			FactID:               "stale-fact-closed",
			ActivityID:           lockedActID,
			OwnerBotID:           "cedar",
			OriginConversationID: lockedAct.OriginConversationID,
			Revision:             lockedAfterAck.Revision + 1,
			Kind:                 "question_open",
			QuestionID:           lockedAct.Questions[0].QuestionID,
			Summary:              "Stale closed question event",
		})
		w2.view.Frame()
	})
	w2.wait(func() bool {
		return !cedar2.activityNotices.reading && cedar2.activityNotices.loaded == cedar2.activityNotices.version
	})
	var closedNotices []activityNotice
	w2.pump.do(func() { closedNotices = activityNotices(cedar2) })
	if len(closedNotices) != 0 {
		t.Fatalf("stale question_open digest reopened a closed activity: %+v", closedNotices)
	}

	botCedar, err := w2.client.GetConversation(ctx, cedar2.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	draftRev := uint64(0)
	if botCedar.Draft != nil {
		draftRev = botCedar.Draft.Revision
	}
	savedDraft, err := w2.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botCedar.ConversationID},
		ExpectedRevision:          draftRev,
		OperationID:               "op-cp2-save-propose-draft",
		ProductText:               pi.ProductText{Text: "CP2_PROPOSE_RULE"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w2.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botCedar.ConversationID},
		DraftRevision:             savedDraft.Revision,
		OperationID:               "op-cp2-propose-rule",
	}); err != nil {
		t.Fatal(err)
	}
	var learningID string
	w2.wait(func() bool {
		mem, memErr := w2.client.GetBotMemory(ctx, "cedar")
		if memErr != nil || len(mem.PendingLearnings) == 0 {
			return false
		}
		learningID = mem.PendingLearnings[0].LearningID
		return true
	})
	if _, err := w2.client.ReviewLearning(ctx, pi.ProductMemoryReviewLearningParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: "cedar", OperationID: "op-cp2-approve-rule"},
		LearningID:             learningID,
		Decision:               "approve",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := w2.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: autoActID},
		ProductText:           pi.ProductText{Text: autoGoal},
		ExpectedRevision:      1,
		OperationID:           "op-cp2-start-auto",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-autoTurn2Entered:
	case <-ctx.Done():
		t.Fatal("timed out waiting for bot_auto turn 2")
	}

	w2.wait(func() bool {
		return w2.view.HasText(msgs.ActivityAutoAnswered()) &&
			w2.view.HasText("Markdown") &&
			w2.view.HasText(msgs.ActivityDismissHint())
	})
	w2.click(msgs.ActivityDismissHint())
	w2.wait(func() bool { return !w2.view.HasText(msgs.ActivityDismissHint()) })

	close(releaseAutoTurn2)
	w2.pump.do(func() {
		cedar2.activity.open = true
		w2.app.openProductActivity(cedar2, autoActID)
		w2.view.Frame()
	})
	w2.wait(func() bool {
		return cedar2.activity.activity != nil &&
			cedar2.activity.activity.ActivityID == autoActID &&
			cedar2.activity.activity.State == "completed" &&
			len(cedar2.activity.activity.Questions) == 1 &&
			cedar2.activity.activity.Questions[0].Provenance == "bot_auto" &&
			hasTextInActivityScrollOnPump(w2, cedar2, msgs.ActivityAutoAnswered()) &&
			hasTextInActivityScrollOnPump(w2, cedar2, "Markdown") &&
			hasTextInActivityScrollOnPump(w2, cedar2, msgs.ActivityDelivered())
	})

	mu.Lock()
	gotAutoRaw := autoToolResult
	mu.Unlock()
	if !strings.Contains(gotAutoRaw, `"provenance":"bot_auto"`) || !strings.Contains(gotAutoRaw, `"Markdown"`) {
		t.Fatalf("bot_auto tool result mismatch: %s", gotAutoRaw)
	}

	w2.shutdown(ctx)
}

func TestPiAppControlPlaneCP3LiveSteerAndUnknownReconciliation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	const (
		liveActID  = "act-cp3-live"
		crashActID = "act-cp3-crash"
		liveGoal   = "CP3_LIVE_STEER"
		crashGoal  = "CP3_CRASH_STEER"
		steerText  = "Prioritize batch 14 checksums"
		crashText  = "Never replay after crash"
	)

	liveTurn1Entered := make(chan struct{})
	releaseLiveTurn1 := make(chan struct{})
	crashTurn1Entered := make(chan struct{})
	releaseCrashTurn1 := make(chan struct{})

	var (
		mu              sync.Mutex
		liveTurns       int
		observedSteer   bool
		crashTurnsCount int
	)

	provider := newCPProvider(t, func(req cpProviderRequest) cpProviderReply {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(req.FirstUser, liveGoal):
			liveTurns++
			if liveTurns == 1 {
				return cpProviderReply{
					StreamPrefix: "Scanning intake lots...",
					Entered:      liveTurn1Entered,
					Release:      releaseLiveTurn1,
					Calls: []cpToolCall{{
						ID:   "cp3-prog-1",
						Name: "activity_progress",
						Args: map[string]any{"stage": "Applying live instruction", "step": 1, "total_steps": 2},
					}},
				}
			}
			for _, m := range req.Messages {
				if m.Role == "user" && strings.Contains(m.Content, "<activity_steer ") && strings.Contains(m.Content, steerText) {
					observedSteer = true
				}
			}
			return cpProviderReply{Text: "Prioritized batch 14 checksums complete."}
		case strings.Contains(req.FirstUser, crashGoal):
			crashTurnsCount++
			return cpProviderReply{
				StreamPrefix: "Running long batch...",
				Entered:      crashTurn1Entered,
				Release:      releaseCrashTurn1,
				Text:         "Should never complete before crash.",
			}
		default:
			return cpProviderReply{Text: "OK"}
		}
	})

	root := prepareProductWorkbench(t, provider)
	t.Cleanup(func() {
		safeCloseChan(releaseLiveTurn1)
		safeCloseChan(releaseCrashTurn1)
	})
	if err := writeProfileJSON(guiStorePath(), storedState{
		Version:  contactStoreVersion,
		Selected: "cedar",
		Contacts: []storedContact{{ID: "cedar", Name: "Cedar", Workspace: root}},
		Issues: []storedActivity{
			{ID: liveActID, ContactID: "cedar", Goal: liveGoal, Status: activityStatusNotStarted, RequirementsRevision: 1},
			{ID: crashActID, ContactID: "cedar", Goal: crashGoal, Status: activityStatusNotStarted, RequirementsRevision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	msgs := locales.In("en")
	w := openProductWorkbench(t)
	var cedar *contact
	w.pump.do(func() { cedar = w.app.contactByID("cedar") })
	w.wait(func() bool { return cedar != nil && cedar.Loaded })

	if _, err := w.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: liveActID},
		ProductText:           pi.ProductText{Text: liveGoal},
		ExpectedRevision:      1,
		OperationID:           "op-cp3-start-live",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-liveTurn1Entered:
	case <-ctx.Done():
		t.Fatal("timed out waiting for live steer turn 1")
	}

	w.click(msgs.WorkActivity())
	w.wait(func() bool { return cedar.activity.open && len(cedar.activity.items) >= 1 })
	clickInActivityScroll(w, cedar, msgs.IssueRowLabel(liveGoal))
	w.wait(func() bool {
		return cedar.activity.activity != nil &&
			cedar.activity.activity.ActivityID == liveActID &&
			cedar.activity.activity.State == "running" &&
			hasTextInActivityScrollOnPump(w, cedar, msgs.ActivitySteer())
	})

	clickInActivityScroll(w, cedar, msgs.ActivitySteer())
	w.wait(func() bool { return cedar.activity.forms[liveActID] != nil })
	w.pump.do(func() {
		w.view.Type(steerText)
		w.view.Frame()
	})
	clickInActivityScroll(w, cedar, msgs.ActivitySendSteer())
	w.wait(func() bool {
		return !cedar.activity.changing &&
			cedar.activity.activity != nil &&
			len(cedar.activity.activity.Steers) == 1 &&
			cedar.activity.activity.Steers[0].State == "queued" &&
			hasTextInActivityScrollOnPump(w, cedar, msgs.ActivitySteerQueued())
	})

	safeCloseChan(releaseLiveTurn1)
	w.wait(func() bool {
		return cedar.activity.activity != nil &&
			cedar.activity.activity.State == "completed" &&
			len(cedar.activity.activity.Steers) == 1 &&
			cedar.activity.activity.Steers[0].State == "delivered" &&
			hasTextInActivityScrollOnPump(w, cedar, msgs.ActivitySteerDelivered())
	})

	mu.Lock()
	sawSteer := observedSteer
	mu.Unlock()
	if !sawSteer {
		t.Fatalf("controlled local provider did not observe steer instruction %q in next LLM turn", steerText)
	}

	crashCont, err := w.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: crashActID},
		ProductText:           pi.ProductText{Text: crashGoal},
		ExpectedRevision:      1,
		OperationID:           "op-cp3-start-crash",
	})
	if err != nil || crashCont.Execution == nil {
		t.Fatalf("start crash activity: %+v, %v", crashCont, err)
	}
	crashExecID := crashCont.Execution.ExecutionID
	select {
	case <-crashTurn1Entered:
	case <-ctx.Done():
		t.Fatal("timed out waiting for crash steer turn 1")
	}

	w.pump.do(func() {
		cedar.activity.open = true
		w.app.openProductActivity(cedar, crashActID)
		w.view.Frame()
	})
	w.wait(func() bool {
		return cedar.activity.activity != nil &&
			cedar.activity.activity.ActivityID == crashActID &&
			hasTextInActivityScrollOnPump(w, cedar, msgs.ActivitySteer())
	})
	clickInActivityScroll(w, cedar, msgs.ActivitySteer())
	w.wait(func() bool { return cedar.activity.forms[crashActID] != nil })
	w.pump.do(func() {
		w.view.Type(crashText)
		w.view.Frame()
	})
	clickInActivityScroll(w, cedar, msgs.ActivitySendSteer())
	w.wait(func() bool {
		return !cedar.activity.changing &&
			cedar.activity.activity != nil &&
			len(cedar.activity.activity.Steers) == 1 &&
			cedar.activity.activity.Steers[0].State == "queued"
	})

	callsBeforeCrash := provider.calls.Load()
	outcome, err := w.client.Kill(ctx)
	if !outcome.Reaped || outcome.PID <= 0 || outcome.ExitCode == 0 || outcome.Err == nil || !errors.Is(err, outcome.Err) {
		t.Fatalf("kill worker during queued steer: %+v, %v", outcome, err)
	}
	<-w.client.CallbacksDone()

	w2 := openProductWorkbench(t)
	var cedar2 *contact
	w2.pump.do(func() { cedar2 = w2.app.contactByID("cedar") })
	w2.wait(func() bool { return cedar2 != nil && cedar2.Loaded })
	w2.pump.do(func() {
		cedar2.activity.open = true
		w2.app.openProductActivity(cedar2, crashActID)
		w2.view.Frame()
	})
	w2.wait(func() bool {
		return cedar2.activity.activity != nil &&
			cedar2.activity.activity.ActivityID == crashActID &&
			len(cedar2.activity.activity.Steers) == 1 &&
			cedar2.activity.activity.Steers[0].State == "unknown" &&
			hasTextInActivityScrollOnPump(w2, cedar2, msgs.ActivitySteerUnknown()) &&
			hasTextInActivityScrollOnPump(w2, cedar2, msgs.ActivitySteerWorkerLost())
	})
	if got := provider.calls.Load(); got != callsBeforeCrash {
		t.Fatalf("worker crash restart replayed steer or execution: calls=%d want=%d", got, callsBeforeCrash)
	}
	var crashRev uint64
	w2.pump.do(func() { crashRev = cedar2.activity.activity.Revision })
	w2.shutdown(ctx)

	uiState, err := readProductUI(guiStorePath())
	if err != nil {
		t.Fatal(err)
	}
	if uiState == nil {
		uiState = &productUIState{}
	}
	if uiState.PendingActivities == nil {
		uiState.PendingActivities = make(map[string]productActivityIntent)
	}
	uiState.PendingActivities[crashActID] = productActivityIntent{
		OperationID: "op-cp3-unacked-crash-steer",
		BotID:       "cedar",
		ActivityID:  crashActID,
		Action:      "steer",
		Revision:    crashRev,
		ExecutionID: crashExecID,
		Text:        "Pre-dispatch unconfirmed steer",
	}
	if err := writeProfileJSON(productUIPath(guiStorePath()), uiState); err != nil {
		t.Fatal(err)
	}

	callsBeforeIntentReopen := provider.calls.Load()
	w3 := openProductWorkbench(t)
	var cedar3 *contact
	w3.pump.do(func() { cedar3 = w3.app.contactByID("cedar") })
	w3.wait(func() bool { return cedar3 != nil && cedar3.Loaded && !cedar3.activity.changing })
	w3.pump.do(func() {
		cedar3.activity.open = true
		w3.app.openProductActivity(cedar3, crashActID)
		w3.view.Frame()
	})
	w3.wait(func() bool {
		return !cedar3.activity.changing &&
			cedar3.activity.activity != nil &&
			cedar3.activity.activity.ActivityID == crashActID &&
			w3.app.activityIntents[crashActID].OperationID == "op-cp3-unacked-crash-steer" &&
			hasTextInActivityScrollOnPump(w3, cedar3, msgs.ActivitySteerUnknown()) &&
			hasTextInActivityScrollOnPump(w3, cedar3, msgs.SupplementUnknown()) &&
			hasTextInActivityScrollOnPump(w3, cedar3, "Pre-dispatch unconfirmed steer")
	})
	if got := provider.calls.Load(); got != callsBeforeIntentReopen {
		t.Fatalf("persisted unresolved steer intent caused automatic replay: calls=%d want=%d", got, callsBeforeIntentReopen)
	}

	if slices.ContainsFunc(provider.snapshotRequests(), func(r cpProviderRequest) bool {
		return strings.Contains(r.LastUser, "Pre-dispatch unconfirmed steer")
	}) {
		t.Fatal("unresolved pre-dispatch steer text leaked to provider on restart")
	}

	w3.shutdown(ctx)
}
