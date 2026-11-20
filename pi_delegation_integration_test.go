//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

var delegationScopePattern = regexp.MustCompile(`scope-[a-f0-9]+`)
var delegationMarkerPattern = regexp.MustCompile(`DELEGATION_[A-Z_]+`)

type delegationReply struct {
	tool string
	id   string
	args map[string]any
	text string
}

type delegationRequest struct {
	input, scope, callID, result string
}

type delegationProvider struct {
	t        *testing.T
	routes   map[string]func(delegationRequest) delegationReply
	requests atomic.Int32
}

func (p *delegationProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.requests.Add(1) > 40 || r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer nagi-product-loopback" {
		p.t.Error("unexpected delegation provider request")
		http.Error(w, "unexpected delegation provider request", http.StatusBadRequest)
		return
	}
	var request struct {
		Stream   bool `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			CallID  string          `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&request); err != nil || !request.Stream || len(request.Messages) == 0 {
		p.t.Errorf("invalid provider body: %v", err)
		http.Error(w, "invalid provider body", http.StatusBadRequest)
		return
	}
	var input string
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == "user" {
			input = string(request.Messages[i].Content)
			break
		}
	}
	marker := delegationMarkerPattern.FindString(input)
	route := p.routes[marker]
	if route == nil {
		p.t.Errorf("unexpected provider marker %q", marker)
		http.Error(w, "unknown scenario", http.StatusBadRequest)
		return
	}
	last := request.Messages[len(request.Messages)-1]
	var result string
	if last.Role == "tool" {
		_ = json.Unmarshal(last.Content, &result)
	}
	reply := route(delegationRequest{input: input, scope: delegationScopePattern.FindString(input), callID: last.CallID, result: result})
	if reply.text == "" && reply.tool == "" {
		<-r.Context().Done()
		return
	}
	delta := map[string]any{"role": "assistant", "content": reply.text}
	finish := "stop"
	if reply.tool != "" {
		args, _ := json.Marshal(reply.args)
		delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": 0, "id": reply.id, "type": "function", "function": map[string]string{"name": reply.tool, "arguments": string(args)},
		}}}
		finish = "tool_calls"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, choice := range []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}, {"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
		chunk, _ := json.Marshal(map[string]any{"id": "delegation-loopback", "object": "chat.completion.chunk", "created": 1, "model": "nagi-product-fixture", "choices": []any{choice}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	io.WriteString(w, "data: [DONE]\n\n")
}

func delegationCall(request delegationRequest, tool, id string, args map[string]any) delegationReply {
	args["scope"] = request.scope
	return delegationReply{tool: tool, id: id, args: args}
}

func TestPiConversationDelegation(t *testing.T) {
	root := t.TempDir()
	node, worker := os.Getenv("NAGI_TEST_NODE"), os.Getenv("NAGI_TEST_WORKER")
	payload := filepath.Dir(filepath.Dir(node))
	if !filepath.IsAbs(node) || node != filepath.Join(payload, "bin", "node") || worker != filepath.Join(payload, "dist", "worker.js") {
		t.Fatal("NAGI_TEST_NODE and NAGI_TEST_WORKER must identify one pinned payload")
	}
	isolateConnectionApp(t, root, payload)
	artifact := filepath.Join(t.TempDir(), "research.md")
	reviewFile := filepath.Join(t.TempDir(), "review.md")
	const research = "Finding: the measurement needs a source."
	const review = "Review: attach the source before publishing."
	atB := make(chan struct{})
	var closeB sync.Once
	p := &delegationProvider{t: t, routes: make(map[string]func(delegationRequest) delegationReply)}
	server := httptest.NewServer(p)
	t.Cleanup(server.Close)
	config := testConnectionConfig(t)
	config.Endpoint, config.APIKey, config.Model = server.URL+"/v1", "nagi-product-loopback", "nagi-product-fixture"
	config.API = connectionAPIChatCompletions
	config.WorkingDirectory, config.MaxActiveTasks = root, 3
	if err := saveConnectionConfig(connectionConfigPath(guiStorePath()), config); err != nil {
		t.Fatal(err)
	}
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	h := &piFirstUseHarness{t: t, app: app, pump: newUIPump(t, app), provider: &piAppProvider{limit: 40}}
	t.Cleanup(h.cleanup)
	var sourceID, reviewerID string
	h.onUI(func() error {
		sourceID = app.selectedID()
		app.selectedContact().Name = "Researcher"
		app.addRuntimeContact()
		reviewerID = app.selectedID()
		app.selectedContact().Name = "Reviewer"
		return app.persist()
	})
	var activityA, activityB, reviewID, interruptedID atomic.Value
	admitted := func(request delegationRequest, slot *atomic.Value) string {
		var receipt IssueToolReceipt
		if err := json.Unmarshal([]byte(request.result), &receipt); err != nil || receipt.State != issueToolReceiptAccepted || receipt.PiRun == nil || !receipt.PiRun.Admitted || receipt.IssueID == "" {
			t.Errorf("%s got invalid admission: %s (%v)", request.callID, request.result, err)
		}
		slot.Store(receipt.IssueID)
		return receipt.IssueID
	}
	query := func(request delegationRequest, callID string, issueID *atomic.Value) delegationReply {
		return delegationCall(request, "issue_get", callID, map[string]any{"issue_id": issueID.Load()})
	}
	stop := func(request delegationRequest) delegationReply {
		return delegationCall(request, "issue_action", "stop_b", map[string]any{"action": "stop", "issue_id": activityB.Load()})
	}
	p.routes["DELEGATION_START"] = func(r delegationRequest) delegationReply {
		switch r.callID {
		case "":
			return delegationCall(r, "delegate_activity", "delegate_a", map[string]any{"key": "research-a", "goal": "DELEGATION_WORK_A"})
		case "delegate_a":
			admitted(r, &activityA)
			return delegationCall(r, "delegate_activity", "delegate_b", map[string]any{"key": "research-b", "goal": "DELEGATION_WORK_B"})
		case "delegate_b":
			admitted(r, &activityB)
			return delegationReply{text: "Both research activities started."}
		}
		return delegationReply{text: "Unexpected start response."}
	}
	p.routes["DELEGATION_WORK_A"] = func(r delegationRequest) delegationReply {
		if strings.Contains(r.input, "Apply this review") {
			if r.callID == "" {
				if !strings.Contains(r.input, review) {
					t.Errorf("revision did not receive the actual review: %s", r.input)
				}
				return delegationReply{tool: "write", id: "revise_research", args: map[string]any{"path": artifact, "content": research + "\n" + review}}
			}
			return delegationReply{text: "Research revised from the review."}
		}
		if r.callID == "" && !strings.Contains(r.input, "Add the measurement source") {
			select {
			case <-atB:
			case <-time.After(15 * time.Second):
				t.Error("activity B did not overlap activity A")
			}
			return delegationReply{tool: "write", id: "research_file", args: map[string]any{"path": artifact, "content": research}}
		}
		if strings.Contains(r.input, "Add the measurement source") {
			return delegationReply{text: "Measurement source added."}
		}
		return delegationReply{text: research}
	}
	p.routes["DELEGATION_WORK_B"] = func(r delegationRequest) delegationReply {
		closeB.Do(func() { close(atB) })
		return delegationReply{}
	}
	p.routes["DELEGATION_UPDATE"] = func(r delegationRequest) delegationReply {
		switch r.callID {
		case "":
			return query(r, "read_a", &activityA)
		case "read_a":
			var detail IssueToolIssueDetail
			if err := json.Unmarshal([]byte(r.result), &detail); err != nil || detail.ResultExcerpt != research || len(detail.ResultFiles) != 1 || detail.ResultFiles[0].Path != artifact {
				t.Errorf("A result was not available to Bot: %s", r.result)
			}
			return query(r, "read_b", &activityB)
		case "read_b":
			var detail IssueToolIssueDetail
			if err := json.Unmarshal([]byte(r.result), &detail); err != nil || detail.PiProgress == nil || !detail.PiProgress.Active {
				t.Errorf("B progress was not available to Bot: %s", r.result)
			}
			return delegationCall(r, "issue_action", "supplement_a", map[string]any{"action": "add_supplement", "issue_id": activityA.Load(), "text": "Add the measurement source"})
		case "supplement_a":
			return stop(r)
		case "stop_b":
			var receipt IssueToolReceipt
			if err := json.Unmarshal([]byte(r.result), &receipt); err != nil || receipt.State != issueToolReceiptStopRequested || receipt.IssueID != activityB.Load() {
				t.Errorf("B stop receipt was incorrect: %s", r.result)
			}
		}
		return delegationReply{text: "A: " + research + " B: stop requested."}
	}
	p.routes["DELEGATION_HANDOFF"] = func(r delegationRequest) delegationReply {
		switch r.callID {
		case "":
			return delegationCall(r, "bot_list", "list_bots", map[string]any{})
		case "list_bots":
			if !strings.Contains(r.result, reviewerID) || !strings.Contains(r.result, "Reviewer") || strings.Contains(r.result, sourceID) {
				t.Errorf("recipient discovery returned %s", r.result)
			}
			return delegationCall(r, "delegate_activity", "delegate_review", map[string]any{"key": "review", "goal": "DELEGATION_REVIEW_WORK", "assignee_id": reviewerID, "files": []string{artifact}})
		case "delegate_review":
			admitted(r, &reviewID)
		}
		return delegationReply{text: "Reviewer accepted the report."}
	}
	p.routes["DELEGATION_REVIEW_WORK"] = func(r delegationRequest) delegationReply {
		if r.callID == "" {
			if !strings.Contains(r.input, artifact) {
				t.Errorf("reviewer did not receive %s", artifact)
			}
			return delegationReply{tool: "read", id: "read_research", args: map[string]any{"path": artifact}}
		}
		if r.callID == "read_research" {
			if !strings.Contains(r.result, research) {
				t.Errorf("reviewer did not read report: %s", r.result)
			}
			return delegationReply{tool: "write", id: "review_file", args: map[string]any{"path": reviewFile, "content": review}}
		}
		return delegationReply{text: review}
	}
	p.routes["DELEGATION_FOLLOWUP"] = func(r delegationRequest) delegationReply {
		if r.callID == "" {
			if !strings.Contains(r.input, reviewID.Load().(string)) || !strings.Contains(r.input, "last_returned_execution_id") {
				t.Errorf("return receipt absent from conversation context: %s", r.input)
			}
			return query(r, "read_review", &reviewID)
		}
		if r.callID == "read_review" {
			var detail IssueToolIssueDetail
			if err := json.Unmarshal([]byte(r.result), &detail); err != nil || detail.Issue.OwnerBotID != reviewerID || detail.Issue.SourceBotID != sourceID || detail.ResultExcerpt != review || len(detail.ResultFiles) != 1 || detail.ResultFiles[0].Path != reviewFile {
				t.Errorf("review result detached: %s", r.result)
			}
		}
		return delegationReply{text: review}
	}
	p.routes["DELEGATION_REVISE"] = func(r delegationRequest) delegationReply {
		switch r.callID {
		case "":
			return query(r, "review_for_revision", &reviewID)
		case "review_for_revision":
			var detail IssueToolIssueDetail
			if err := json.Unmarshal([]byte(r.result), &detail); err != nil || detail.ResultExcerpt != review {
				t.Errorf("revision query did not read the reviewer result: %s", r.result)
			}
			return delegationCall(r, "issue_action", "revise_a", map[string]any{"action": "add_supplement", "issue_id": activityA.Load(), "text": "Apply this review: " + detail.ResultExcerpt})
		case "revise_a":
			var receipt IssueToolReceipt
			if err := json.Unmarshal([]byte(r.result), &receipt); err != nil || receipt.State != issueToolReceiptSaved || receipt.IssueID != activityA.Load() {
				t.Errorf("revision was not attached to A: %s", r.result)
			}
		}
		return delegationReply{text: "Review sent to the research activity."}
	}
	p.routes["DELEGATION_INTERRUPTED"] = func(r delegationRequest) delegationReply {
		if r.callID == "" {
			return delegationCall(r, "delegate_activity", "interruptible_review", map[string]any{"key": "second-review", "goal": "DELEGATION_INTERRUPTED_WORK", "assignee_id": reviewerID})
		}
		admitted(r, &interruptedID)
		return delegationReply{text: "Second review started."}
	}
	interruptedArrived := make(chan struct{})
	var interruptedOnce sync.Once
	p.routes["DELEGATION_INTERRUPTED_WORK"] = func(delegationRequest) delegationReply {
		interruptedOnce.Do(func() { close(interruptedArrived) })
		return delegationReply{}
	}
	start := func() {
		h.pump.do(h.app.startPiHost)
		h.pump.waitTimeout(func() bool { return h.app.runtimeAvailableForView() }, 30*time.Second)
	}
	send := func(id, input, reply string) {
		t.Helper()
		h.pump.do(func() {
			for index, ct := range h.app.contacts {
				if ct.ID == id {
					h.app.selectContact(index)
					return
				}
			}
		})
		h.pump.waitTimeout(func() bool {
			ct := h.app.contactByID(id)
			return ct != nil && (ct.PiBinding == nil || ct.Loaded)
		}, 30*time.Second)
		h.onUI(func() error {
			ct := h.app.contactByID(id)
			if !h.app.sendPiConversation(ct, input) {
				return fmt.Errorf("chat %q rejected for %s: %s", input, id, ct.SendError)
			}
			return nil
		})
		h.pump.waitTimeout(func() bool {
			ct := h.app.contactByID(id)
			if ct.SendState != sendIdle || ct.ActiveTaskID != "" {
				return false
			}
			for _, message := range ct.Messages {
				if strings.Contains(message.Text, reply) {
					return true
				}
			}
			return false
		}, 30*time.Second)
	}
	waitExecution := func(botID string, issueID *atomic.Value, number int, status string) piActivityRun {
		t.Helper()
		var run piActivityRun
		h.pump.waitTimeout(func() bool {
			id, _ := issueID.Load().(string)
			issue := h.app.contactByID(botID).issueByID(id)
			if issue == nil || len(issue.Executions) != number {
				return false
			}
			execution := issue.Executions[number-1]
			return execution.Status == status && execution.PiRun != nil && execution.PiRun.Admitted
		}, 30*time.Second)
		h.pump.do(func() { run = *h.app.contactByID(botID).issueByID(issueID.Load().(string)).Executions[number-1].PiRun })
		return run
	}
	start()
	send(sourceID, "DELEGATION_START", "Both research activities started.")
	select {
	case <-atB:
	case <-time.After(15 * time.Second):
		t.Fatal("second activity did not reach the provider")
	}
	a := waitExecution(sourceID, &activityA, 1, issueExecutionCompleted)
	b := waitExecution(sourceID, &activityB, 1, issueExecutionRunning)
	if a.ContextID == b.ContextID || a.SessionRef == b.SessionRef {
		t.Fatal("parallel activities shared one Pi context")
	}
	send(sourceID, "DELEGATION_UPDATE", "B: stop requested.")
	waitExecution(sourceID, &activityB, 1, issueExecutionInterrupted)
	h.onUI(func() error {
		ct := h.app.contactByID(sourceID)
		issue := ct.issueByID(activityA.Load().(string))
		if len(issue.Executions) == 1 && !h.app.issueRunCaptured(ct, issue) {
			return fmt.Errorf("A supplement could not continue: %s", h.app.issueFormError)
		}
		return nil
	})
	next := waitExecution(sourceID, &activityA, 2, issueExecutionCompleted)
	if next.ContextID != a.ContextID || next.SessionRef != a.SessionRef || next.RunID == a.RunID {
		t.Fatal("A follow-up did not continue the original session with a new run")
	}
	send(sourceID, "DELEGATION_HANDOFF", "Reviewer accepted the report.")
	reviewRun := waitExecution(reviewerID, &reviewID, 1, issueExecutionCompleted)
	if reviewRun.ContextID == a.ContextID || reviewRun.SessionRef == a.SessionRef {
		t.Fatal("cross-Bot handoff reused source context")
	}
	for path, want := range map[string]string{artifact: research, reviewFile: review} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("artifact %s = %q, %v", path, got, err)
		}
	}
	send(reviewerID, "DELEGATION_FOLLOWUP", review)
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
	before := p.requests.Load()
	app, err = newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	h.app, h.pump, h.closed = app, newUIPump(t, app), false
	start()
	waitExecution(reviewerID, &reviewID, 1, issueExecutionCompleted)
	if p.requests.Load() != before {
		t.Fatal("startup replayed a delegated execution")
	}
	send(sourceID, "DELEGATION_FOLLOWUP", review)
	if p.requests.Load() != before+2 {
		t.Fatalf("reopened result read added unexpected requests: %d -> %d", before, p.requests.Load())
	}
	send(sourceID, "DELEGATION_REVISE", "Review sent to the research activity.")
	revised := waitExecution(sourceID, &activityA, 3, issueExecutionCompleted)
	got, err := os.ReadFile(artifact)
	if err != nil || string(got) != research+"\n"+review || revised.ContextID != a.ContextID || revised.SessionRef != a.SessionRef || revised.RunID == next.RunID {
		t.Fatalf("review-driven revision = %q, run=%+v, err=%v", got, revised, err)
	}
	send(sourceID, "DELEGATION_INTERRUPTED", "Second review started.")
	select {
	case <-interruptedArrived:
	case <-time.After(15 * time.Second):
		t.Fatal("second review did not reach the provider")
	}
	interrupted := waitExecution(reviewerID, &interruptedID, 1, issueExecutionRunning)
	var dead *piAppHost
	h.pump.do(func() { dead = h.app.pi })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	outcome, killErr := dead.client.Kill(ctx)
	cancel()
	crash := &piCrashHarness{piActivityHarness: &piActivityHarness{piFirstUseHarness: h}}
	if !outcome.Reaped || outcome.ExitCode == 0 || killErr == nil || !crash.observeKilled(dead) {
		t.Fatalf("worker loss did not settle: %+v %v", outcome, killErr)
	}
	h.pump.wait(func() bool { return h.app.submissionInFlight == 0 })
	before = p.requests.Load()
	close(h.pump.q)
	<-h.pump.done
	h.closed = true
	app, err = newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	h.app, h.pump, h.closed = app, newUIPump(t, app), false
	start()
	restored := waitExecution(reviewerID, &interruptedID, 1, issueExecutionUnknown)
	h.onUI(func() error {
		issue := h.app.contactByID(reviewerID).issueByID(interruptedID.Load().(string))
		if restored != interrupted || issue.Executions[0].ReturnedAt != 0 || issue.Handoff.SourceBotID != sourceID || p.requests.Load() != before {
			return fmt.Errorf("unknown handoff changed after restart: issue=%s execution=%+v", issue.ID, issue.Executions[0])
		}
		return nil
	})
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
	piAppRecord(t, "delegation-run-owners", map[string]any{"research": a, "continuation": next, "stopped": b, "review": reviewRun, "revision": revised, "unknown": restored, "requests": p.requests.Load()})
}

func TestPiAppProductScopedDelegation(t *testing.T) {
	const origin, recipient, unrelated = "origin", "recipient", "unrelated"
	const foreignActivity = "foreign-activity"
	const key, goal = "inventory", "DELEGATION_PRODUCT_WORK"
	const chatReply, resultText = "I delegated the inventory", "Inventory findings"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p := &delegationProvider{t: t, routes: make(map[string]func(delegationRequest) delegationReply)}
	root := prepareProductWorkbench(t, p)
	if err := writeProfileJSON(guiStorePath(), storedState{Version: contactStoreVersion, Selected: origin,
		Contacts: []storedContact{
			{ID: origin, Name: "Origin Bot", Workspace: root, Draft: "DELEGATION_PRODUCT_CHAT"},
			{ID: recipient, Name: "Recipient Bot", Workspace: root, Draft: "Recipient draft"},
			{ID: unrelated, Name: "Unrelated Bot", Workspace: root},
		},
		Issues: []storedIssue{{ID: foreignActivity, ContactID: unrelated, Goal: "Unrelated inventory", Status: issueStatusNotStarted, RequirementsRevision: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	delegate := map[string]any{"key": key, "goal": goal, "assignee_id": recipient, "requirements": []string{"Use the saved inventory"}}
	var admitted atomic.Value
	p.routes["DELEGATION_PRODUCT_CHAT"] = func(request delegationRequest) delegationReply {
		switch request.callID {
		case "":
			return delegationReply{tool: "issue_get", id: "foreign-read", args: map[string]any{"issue_id": foreignActivity}}
		case "foreign-read":
			if !strings.Contains(request.result, "outside this conversation scope") || strings.Contains(request.result, "Unrelated inventory") {
				t.Errorf("foreign activity lookup leaked or returned the wrong refusal: %s", request.result)
			}
			return delegationReply{tool: "delegate_activity", id: "delegate-first", args: delegate}
		case "delegate-first":
			var receipt pi.ProductReceipt
			if err := json.Unmarshal([]byte(request.result), &receipt); err != nil || receipt.Target.ActivityID == "" || receipt.Execution == nil || receipt.Execution.ExecutionID == "" {
				t.Errorf("Product did not admit the delegated activity: %s, %v", request.result, err)
			}
			admitted.Store(receipt)
			return delegationReply{tool: "delegate_activity", id: "delegate-repeat", args: delegate}
		case "delegate-repeat":
			first := admitted.Load().(pi.ProductReceipt)
			var receipt pi.ProductReceipt
			if err := json.Unmarshal([]byte(request.result), &receipt); err != nil || receipt.State != "accepted" || receipt.Target != first.Target {
				t.Errorf("duplicate delegation changed its target: %s, %v", request.result, err)
			}
			return delegationReply{text: chatReply}
		default:
			t.Errorf("unexpected delegation tool result %q", request.callID)
			return delegationReply{text: "Unexpected tool result"}
		}
	}
	p.routes[goal] = func(request delegationRequest) delegationReply {
		if request.callID != "" || !strings.Contains(request.input, "Use the saved inventory") {
			t.Errorf("the delegated execution received the wrong requirements: %+v", request)
		}
		return delegationReply{text: resultText}
	}
	w := openProductWorkbench(t)
	owner := w.app.contactByID(origin)
	source := owner.SessionID
	msg := locales.In("en")
	w.click(msg.SendAction())
	w.wait(func() bool {
		return owner.SendState == sendIdle && len(owner.History) == 2 && owner.History[1].Text == chatReply
	})
	first, ok := admitted.Load().(pi.ProductReceipt)
	if !ok || first.Execution == nil || first.Target.ActivityID == "" {
		t.Fatal("the conversation did not produce an activity receipt")
	}
	activityID, executionID := first.Target.ActivityID, first.Execution.ExecutionID
	w.wait(func() bool {
		for _, activity := range owner.activity.items {
			if activity.ActivityID == activityID && activity.State == "needsAttention" {
				return true
			}
		}
		return false
	})
	conversation, err := w.client.GetConversation(ctx, source)
	if err != nil || conversation.LatestExecution == nil || conversation.LatestExecution.State != "completed" {
		t.Fatalf("source conversation did not settle: %+v, %v", conversation, err)
	}
	operation := conversation.LatestExecution.ExecutionID + ":delegate:" + key
	listBots := func(client *pi.ProductClient) []pi.ProductBot {
		t.Helper()
		page, err := client.ListBots(ctx, pi.ProductPageParams{})
		if err != nil || page.NextCursor != nil {
			t.Fatalf("read fixture Bots: %+v, %v", page, err)
		}
		return page.Items
	}
	botsBefore := listBots(w.client)
	recipientIndex := slices.IndexFunc(botsBefore, func(bot pi.ProductBot) bool { return bot.BotID == recipient })
	if recipientIndex < 0 {
		t.Fatal("delegation recipient is missing from the Bot list")
	}
	recipientConversation := botsBefore[recipientIndex].CurrentConversationID
	draftBefore, err := w.client.GetConversation(ctx, recipientConversation)
	if err != nil || draftBefore.Draft == nil || draftBefore.Draft.Text != "Recipient draft" {
		t.Fatalf("read recipient draft before profile save: %+v, %v", draftBefore, err)
	}
	const savedName = "Inventory Bot"
	w.frame(func() { w.app.openSettingsTab(settingsTabContact, settingsSourceRail) })
	w.click(msg.EditContactLabel())
	w.click(botsBefore[recipientIndex].Profile.Name)
	w.click(msg.FieldNameAccessible())
	w.frame(func() {
		w.view.Key(ui.Cmd, ui.KeyA)
		w.view.Type(savedName)
	})
	saveProfile := func(wantError, wantName string) {
		t.Helper()
		var clickErr error
		w.frame(func() { clickErr = clickSettingsAction(w.view, msg.ContactSaveChanges()) })
		if clickErr != nil {
			t.Fatal(clickErr)
		}
		w.pump.do(func() {
			editor := w.app.settingsContactEditor()
			if editor == nil || editor.Error != wantError || editor.Draft.Name != savedName || w.app.settingsContactID != recipient ||
				w.app.contactByID(recipient).Name != wantName || w.app.selectedID() != origin {
				t.Error("profile save changed the selected conversation, lost the draft, or had the wrong outcome")
			}
		})
	}
	profile := guiStorePath()
	profileBefore := recoverySavedBytes(t, profile)
	func() {
		backup := filepath.Join(root, "profile-before-bot-save.json")
		if err := os.Rename(profile, backup); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Rename(backup, profile); err != nil {
				t.Errorf("restore the fixture profile: %v", err)
			}
		}()
		if err := os.Mkdir(profile, 0700); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Remove(profile); err != nil {
				t.Errorf("remove the fixture write obstruction: %v", err)
			}
		}()
		saveProfile(profileNoticeErrorSave, botsBefore[recipientIndex].Profile.Name)
		if !reflect.DeepEqual(listBots(w.client), botsBefore) || !bytes.Equal(recoverySavedBytes(t, backup), profileBefore) {
			t.Fatal("failed Bot profile save changed the live Bots or durable profile")
		}
	}()
	saveProfile("", savedName)
	botsWant := slices.Clone(botsBefore)
	botsWant[recipientIndex].Profile.Name = savedName
	botsWant[recipientIndex].ProfileRevision++
	if bots := listBots(w.client); !reflect.DeepEqual(bots, botsWant) {
		t.Fatalf("profile retry did not update only the recipient Bot: %+v", bots)
	}
	w.shutdown(ctx)
	reopened := openProductWorkbench(t)
	if bots := listBots(reopened.client); !reflect.DeepEqual(bots, botsWant) {
		t.Fatalf("reopen lost the saved Bot profile or changed another Bot: %+v", bots)
	}
	draft, err := reopened.client.GetConversation(ctx, recipientConversation)
	if err != nil || !reflect.DeepEqual(draft, draftBefore) {
		t.Fatalf("profile save changed the recipient's reopened conversation: %+v, %v", draft, err)
	}
	for _, check := range []struct{ bot, activity string }{{origin, activityID}, {recipient, activityID}, {unrelated, foreignActivity}} {
		items, err := reopened.client.ListActivities(ctx, pi.ProductActivityListParams{BotID: check.bot})
		if err != nil || len(items.Items) != 1 || items.Items[0].ActivityID != check.activity {
			t.Fatalf("Bot %s activity scope after restart: %+v, %v", check.bot, items, err)
		}
	}
	activity, err := reopened.client.GetActivity(ctx, activityID)
	if err != nil || activity.OwnerBotID != recipient || activity.OriginConversationID != source || len(activity.Executions) != 1 ||
		activity.Executions[0].ExecutionID != executionID || activity.Executions[0].State != "completed" {
		t.Fatalf("delegation lost its receiving Bot, source conversation, or execution: %+v, %v", activity, err)
	}
	receipt, err := reopened.client.Operation(ctx, operation)
	if err != nil || receipt.OperationID != operation || receipt.State != "accepted" || receipt.Target != first.Target {
		t.Fatalf("restored delegation receipt: %+v, %v", receipt, err)
	}
	result, err := reopened.client.ActivityResult(ctx, productActivityExecution(activityID, executionID))
	if err != nil || result.ActivityID != activityID || result.ExecutionID != executionID || result.Text != resultText || result.Delivery != "available" {
		t.Fatalf("origin lost the exact delegated result: %+v, %v", result, err)
	}
	reopened.shutdown(ctx)
	if got := p.requests.Load(); got != 5 {
		t.Fatalf("delegation, duplicate or restart contacted the provider %d times, want 5", got)
	}
}
