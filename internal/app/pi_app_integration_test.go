//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
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
	reply    string
	arrived  chan struct{}
	release  <-chan struct{}
}

func piAppArguments(values map[string]any) func(string) map[string]any {
	return func(string) map[string]any { return values }
}

func (p *piAppProvider) install(calls []piAppCall, reply string, arrived chan struct{}, release <-chan struct{}) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != nil && p.step != len(p.calls)+1 {
		return fmt.Errorf("previous provider turn stopped at %d/%d", p.step, len(p.calls)+1)
	}
	p.calls, p.step, p.reply, p.arrived, p.release = calls, 0, reply, arrived, release
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
	if err == nil && (r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer xuhu-product-loopback" || request.Model != "xuhu-product-fixture" || !request.Stream || len(request.Messages) == 0) {
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
	reply, arrived, release := p.reply, p.arrived, p.release
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
	delta := map[string]any{"role": "assistant", "content": reply}
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
		chunk, _ := json.Marshal(map[string]any{"id": "product-loopback", "object": "chat.completion.chunk", "created": 1, "model": "xuhu-product-fixture", "choices": []any{choice}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func focusPiControl(tr *ui.Tester, label string, input bool) error {
	if tr.FramePending() {
		tr.Frame()
	}
	if active, _ := tr.TextInputActive(); tr.Focused(label) && active == input {
		return nil
	}
	if input {
		r, ok := tr.Find(label)
		if !ok {
			tr.Frame()
			r, ok = tr.Find(label)
		}
		if ok && r.W <= 96 {
			tr.ClickAt(r.X+r.W+24, r.Y+r.H/2)
			if active, _ := tr.TextInputActive(); tr.Focused(label) && active {
				return nil
			}
		}
		if tr.Click(label) == nil {
			if active, _ := tr.TextInputActive(); tr.Focused(label) && active {
				return nil
			}
		}
	}
	for range 128 {
		active, _ := tr.TextInputActive()
		if tr.Focused(label) && active == input {
			return nil
		}
		tr.Key(0, ui.KeyTab)
	}
	return fmt.Errorf("controller %q was not keyboard reachable as text input=%v", label, input)
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

func TestPiAppProductConnectionSaveAndApplyRefusal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	provider := &piAppProvider{}
	root := prepareProductWorkbench(t, provider)
	profile := guiStorePath()
	if err := writeProfileJSON(profile, storedState{Version: contactStoreVersion, Selected: "cedar", Contacts: []storedContact{
		{ID: "cedar", Name: "Cedar", Workspace: root},
	}}); err != nil {
		t.Fatal(err)
	}
	w := openProductWorkbench(t)
	msg := locales.In("en")
	w.frame(func() { w.app.openSettingsTab(settingsTabConnection, settingsSourceRail) })
	w.wait(func() bool {
		return w.app.settingsOpen && w.view.HasText(msg.ProviderV2ImportTitle())
	})
	w.click(msg.ProviderV2ImportTitle())
	w.wait(func() bool {
		return w.app.providers.adding && w.app.providers.first != nil && w.app.providers.first.preset == -1
	})
	enterField := func(label, value string) {
		t.Helper()
		var err error
		w.pump.do(func() {
			err = focusPiControl(w.view, label, true)
			if err == nil {
				w.view.Command("selectAll")
				w.view.Type(value)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	readProviders := func() pi.ProductProviderList {
		t.Helper()
		list, err := w.client.ListProviders(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	before := readProviders()
	if before.SavedRevision != nil || before.EffectiveRevision != nil || len(before.Accounts) != 0 {
		t.Fatalf("expected legacy v2 profile to start with 0 provider accounts: %+v", before)
	}
	const model = "revised-fixture-model"
	const apiKey = "xuhu-product-loopback"
	enterField(msg.FieldName(), "Studio "+apiKey)
	enterField(msg.ProviderDefaultModel(), model)
	enterField(msg.ConnectionFieldAPIKey(), apiKey)
	var firstBaseURL string
	w.pump.do(func() {
		ed := w.app.providers.first
		if ed == nil || ed.DefaultModel != model || ed.APIKey != apiKey {
			t.Errorf("Settings did not capture the edited model and synthetic key: %+v", ed)
			return
		}
		firstBaseURL = ed.BaseURL
	})
	w.click(msg.ProviderSaveApply())
	w.wait(func() bool { return !w.app.providerBusy() && w.view.HasText(msg.ProviderSaveFailed()) })
	w.pump.do(func() {
		ed := w.app.providers.first
		if ed == nil || ed.DefaultModel != model || ed.APIKey != apiKey || ed.BaseURL != firstBaseURL {
			t.Errorf("Save & apply refusal lost editable provider values: %+v", ed)
		}
	})
	if refused := readProviders(); !reflect.DeepEqual(refused, before) {
		t.Fatalf("Save refusal changed saved or effective revisions: %+v", refused)
	}
	enterField(msg.FieldName(), "Studio gateway")
	w.click(msg.ProviderSave())
	w.wait(func() bool {
		return !w.app.providerBusy() && w.app.productProvidersPending() && len(w.app.productHost().productProviders.Accounts) == 1
	})
	saved := readProviders()
	if saved.SavedRevision == nil || saved.EffectiveRevision != nil || len(saved.Accounts) != 1 ||
		saved.Accounts[0].DefaultModel != model || saved.Accounts[0].CredentialStatus != "ok" {
		t.Fatalf("Save did not leave the new revision pending Apply: %+v", saved)
	}
	w.pump.do(func() {
		ed := w.app.providers.editor
		if ed == nil || ed.DefaultModel != model || ed.BaseURL != firstBaseURL || ed.APIKey != "" {
			t.Errorf("accepted Save did not clear plaintext key and retain values: %+v", ed)
		}
	})
	obstruct := func(action string, wantNotice string, check func()) {
		t.Helper()
		backup := profile + ".fixture-backup"
		if err := os.Rename(profile, backup); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Rename(backup, profile); err != nil {
				t.Errorf("restore fixture profile: %v", err)
			}
		}()
		if err := os.Mkdir(profile, 0700); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Remove(profile); err != nil {
				t.Errorf("remove fixture obstruction: %v", err)
			}
		}()
		w.click(action)
		w.wait(func() bool { return !w.app.providerBusy() && w.view.HasText(wantNotice) })
		w.pump.do(check)
	}
	const editedModel = "obstructed-save-model"
	w.click("Studio gateway")
	w.wait(func() bool { return w.app.providers.adding && w.app.providers.editor != nil })
	enterField(msg.ProviderDefaultModel(), editedModel)
	obstruct(msg.ProviderSaveApply(), msg.ProviderSaveFailed(), func() {
		ed := w.app.providers.editor
		if ed == nil || ed.DefaultModel != editedModel || ed.APIKey != "" || !w.app.productProvidersPending() {
			t.Errorf("obstructed Save & apply lost editable values or pending state: %+v", ed)
		}
	})
	if refused := readProviders(); !reflect.DeepEqual(refused, saved) {
		t.Fatalf("obstructed Save & apply changed pending or effective revisions: %+v", refused)
	}
	enterField(msg.ProviderDefaultModel(), model)
	w.frame(func() { w.app.dismissProviderDialog() })
	obstruct(msg.ProviderApplyChanges(), msg.ProviderApplyFailed(), func() {
		ed := w.app.providers.editor
		if ed == nil || ed.DefaultModel != model || ed.APIKey != "" || !w.app.productProvidersPending() {
			t.Errorf("Apply refusal lost pending state or editor values: %+v", ed)
		}
	})
	if refused := readProviders(); !reflect.DeepEqual(refused, saved) {
		t.Fatalf("Apply refusal changed the pending or effective revision: %+v", refused)
	}
	w.click(msg.ProviderApplyChanges())
	w.wait(func() bool {
		return !w.app.providerBusy() && !w.app.productProvidersPending() && w.view.HasText(msg.ProviderAllApplied())
	})
	applied := readProviders()
	if !reflect.DeepEqual(applied.SavedRevision, saved.SavedRevision) || !reflect.DeepEqual(applied.EffectiveRevision, saved.SavedRevision) ||
		len(applied.Accounts) != 1 || applied.Accounts[0].DefaultModel != model || applied.ResolvedRoles.Dialogue == nil {
		t.Fatalf("Apply retry did not activate the existing saved revision: %+v", applied)
	}
	w.pump.do(func() {
		ed := w.app.providers.editor
		if ed == nil || ed.DefaultModel != model || ed.APIKey != "" || w.app.providers.failure != (providerFeedback{}) {
			t.Error("Apply retry changed the editor or retained the prior error")
		}
	})
	w.shutdown(ctx)
	if requests := provider.count(); requests != 0 {
		t.Fatalf("provider Save or Apply contacted the provider %d times", requests)
	}
	t.Logf("Retried Apply of saved revision %d after refusal with zero provider requests", *saved.SavedRevision)
}

func TestPiAppProductActivityQuestionSurvivesWorkerLoss(t *testing.T) {
	const botID, activityID, goal = "cedar", "cedar-inspection", "Inspect Cedar inventory"
	const questionText, answerText = "Which inventory batch?", "Use the original inventory batch."
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	provider := &piAppProvider{limit: 1}
	call := piAppCall{name: "activity_ask", id: "lost-worker-question", args: piAppArguments(map[string]any{"text": questionText})}
	if err := provider.install([]piAppCall{call}, "Unexpected continuation", nil, nil); err != nil {
		t.Fatal(err)
	}
	root := prepareProductWorkbench(t, provider)
	if err := writeProfileJSON(guiStorePath(), storedState{Version: contactStoreVersion, Selected: botID,
		Contacts: []storedContact{{ID: botID, Name: "Cedar", Workspace: root}},
		Issues:   []storedActivity{{ID: activityID, ContactID: botID, Goal: goal, Status: activityStatusNotStarted, RequirementsRevision: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	readActivity := func(w *productWorkbench) pi.ProductActivity {
		t.Helper()
		activity, err := w.client.GetActivity(ctx, activityID)
		if err != nil || activity.ActivityID != activityID || activity.OwnerBotID != botID || len(activity.Executions) != 1 || len(activity.Questions) != 1 {
			t.Fatalf("read the exact activity, execution and question: %+v, %v", activity, err)
		}
		return activity
	}
	w := openProductWorkbench(t)
	go w.app.observeProductExit(w.app.productHost())
	msg := locales.In("en")
	var cedar *contact
	w.pump.do(func() { cedar = w.app.contactByID(botID) })
	w.wait(func() bool { return cedar != nil && len(cedar.activity.items) == 1 && !cedar.activity.reading })
	w.click(msg.WorkActivity())
	w.click(msg.IssueRowLabel(goal))
	w.wait(func() bool { return cedar.activity.activity != nil && !cedar.activity.reading })
	w.click(msg.SupplementAdd())
	w.frame(func() { w.view.Type("Start the inventory inspection") })
	w.frame(func() { cedar.activity.scroll.Y = 1000 })
	w.click(msg.IssueResume())
	w.wait(func() bool {
		item := cedar.activity.activity
		return item != nil && len(item.OpenQuestionIDs) == 1 && !cedar.activity.reading
	})
	open := readActivity(w)
	run, question := open.Executions[0], open.Questions[0]
	if run.ExecutionID == "" || run.State != "running" || question.QuestionID == "" || question.ExecutionID != run.ExecutionID ||
		question.Text != questionText || question.State != "open" || question.Delivery != "" || provider.count() != 1 {
		t.Fatalf("expected an admitted execution holding the exact unanswered question: %+v", open)
	}
	outcome, err := w.client.Kill(ctx)
	if !outcome.Reaped || outcome.PID <= 0 || outcome.ExitCode == 0 || outcome.Err == nil || !errors.Is(err, outcome.Err) {
		t.Fatalf("kill and reap the worker holding the question: %+v, %v", outcome, err)
	}
	w.wait(func() bool { return w.app.runtimeStatus == runtimeStateExited })

	reopened := openProductWorkbench(t)
	unknown := readActivity(reopened)
	if unknown.Executions[0].ExecutionID != run.ExecutionID || unknown.Executions[0].State != "unknown" ||
		!reflect.DeepEqual(unknown.Questions[0], question) || provider.count() != 1 {
		t.Fatalf("reopen lost the unanswered question, changed the execution, or replayed work: %+v", unknown)
	}
	answer := pi.ProductAnswerParams{
		ProductActivityExecutionParams: productActivityExecution(activityID, run.ExecutionID),
		QuestionID:                     question.QuestionID, QuestionRevision: question.Revision, OperationID: "answer-after-worker-loss",
		ProductText: pi.ProductText{Text: answerText, Materials: []pi.ProductMaterial{}},
	}
	saved, err := reopened.client.AnswerQuestion(ctx, answer)
	if err != nil || !saved.AnswerSaved || saved.Delivery != "saved" || saved.ActivityID != activityID ||
		saved.ExecutionID != run.ExecutionID || saved.QuestionID != question.QuestionID || provider.count() != 1 {
		t.Fatalf("the original question's answer was not saved without delivery: %+v, %v", saved, err)
	}
	reopened.shutdown(ctx)

	after := openProductWorkbench(t)
	activity := readActivity(after)
	answered := activity.Questions[0]
	if activity.Executions[0].ExecutionID != run.ExecutionID || activity.Executions[0].State != "unknown" ||
		answered.QuestionID != question.QuestionID || answered.ExecutionID != run.ExecutionID || answered.State != "answered" ||
		answered.Delivery != "saved" || !reflect.DeepEqual(answered.Answer, &answer.ProductText) {
		t.Fatalf("second reopen changed the saved answer or the unknown execution: %+v", activity)
	}
	receipt, err := after.client.Operation(ctx, answer.OperationID)
	var persisted pi.ProductAnswerResult
	if err != nil || receipt.OperationID != answer.OperationID || receipt.State != "accepted" || receipt.Target.ActivityID != activityID ||
		receipt.Target.QuestionID != question.QuestionID || json.Unmarshal(receipt.Result, &persisted) != nil || persisted != saved {
		t.Fatalf("second reopen changed the exact saved delivery receipt: %+v, %v", receipt, err)
	}
	after.shutdown(ctx)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.requests != 1 || len(provider.problems) != 0 || !bytes.Contains(provider.bodies[0], []byte("Start the inventory inspection")) {
		t.Fatalf("activity answer or reopen contacted the provider: requests=%d problems=%v", provider.requests, provider.problems)
	}
	t.Logf("Reaped worker %d and preserved unknown execution %s, question %s and saved answer %s with one provider request", outcome.PID, run.ExecutionID, question.QuestionID, answer.OperationID)
}
