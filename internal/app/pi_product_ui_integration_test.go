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
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

type productWorkbench struct {
	t      *testing.T
	app    *appModel
	pump   *uiPump
	view   *ui.Tester
	client *pi.ProductClient
}

func prepareProductWorkbench(t *testing.T, provider http.Handler) string {
	t.Helper()
	node, worker := os.Getenv("NAGI_TEST_NODE"), os.Getenv("NAGI_TEST_WORKER")
	payload := filepath.Dir(filepath.Dir(node))
	if !filepath.IsAbs(node) || node != filepath.Join(payload, "bin", "node") || worker != filepath.Join(payload, "dist", "worker.js") {
		t.Fatal("NAGI_TEST_NODE and NAGI_TEST_WORKER must identify one pinned payload")
	}
	root := t.TempDir()
	isolateConnectionApp(t, root, payload)
	server := httptest.NewServer(provider)
	t.Cleanup(server.Close)
	config := connectionConfig{
		API: connectionAPIChatCompletions, Endpoint: server.URL + "/v1", APIKey: "nagi-product-loopback",
		Model: "nagi-product-fixture", ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: root, MaxActiveTasks: 3,
	}
	if err := saveConnectionConfig(connectionConfigPath(guiStorePath()), config); err != nil {
		t.Fatal(err)
	}
	return root
}

func openProductWorkbench(t *testing.T) *productWorkbench {
	t.Helper()
	app, err := newProductRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	w := newProductWorkbench(t, app)
	w.client = app.productHost().product
	t.Cleanup(func() {
		select {
		case <-w.client.Done():
		default:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := w.client.Kill(ctx); err != nil {
				t.Errorf("terminate test worker: %v", err)
				return
			}
		}
		<-w.client.CallbacksDone()
		w.pump.close()
	})
	return w
}

func newProductWorkbench(t *testing.T, app *appModel) *productWorkbench {
	w := &productWorkbench{t: t, app: app, pump: newUIPump(t, app)}
	w.pump.do(func() {
		app.windowWidth = 1040
		w.view = ui.NewTester(app.view, 1040, 720)
		w.view.SetLocale("en")
		w.view.SetFocused(true)
		w.view.Frame()
	})
	return w
}

func (w *productWorkbench) frame(action func()) {
	w.pump.do(func() {
		action()
		w.view.Frame()
	})
}

func (w *productWorkbench) click(label string) {
	w.t.Helper()
	var err error
	var visible []string
	w.frame(func() {
		err = w.view.Click(label)
		if err != nil {
			visible = w.view.Texts()
		}
	})
	if err != nil {
		w.t.Fatalf("click %q: %v, visible: %v", label, err, visible)
	}
}

func (w *productWorkbench) wait(ready func() bool) {
	w.t.Helper()
	w.pump.waitTimeout(func() bool {
		w.view.Frame()
		return ready()
	}, 12*time.Second)
}

func (w *productWorkbench) shutdown(ctx context.Context) {
	w.t.Helper()
	w.pump.do(w.app.beginShutdown)
	w.pump.wait(func() bool { return w.app.quitReady })
	outcome, err := w.client.Wait(ctx)
	if err != nil || !outcome.Reaped || outcome.ExitCode != 0 {
		w.t.Fatalf("worker shutdown: %+v, %v", outcome, err)
	}
}

func TestPiAppPreviousAndSourceChat(t *testing.T) {
	const oldPrompt, oldReply = "Original Cedar prompt", "Original Cedar reply"
	const laterPrompt, laterReply = "Follow-up Cedar\ninventory", "Follow-up Cedar reply"
	const laterLabel = "Follow-up Cedar inventory"
	const newPrompt, newReply = "Current Cedar draft", "Current Cedar reply"
	const otherDraft = "Independent Copper draft"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	provider := &piAppProvider{limit: 3}
	if err := provider.install([]piAppCall{}, oldReply, nil, nil); err != nil {
		t.Fatal(err)
	}
	root := prepareProductWorkbench(t, provider)
	if err := writeProfileJSON(guiStorePath(), storedState{Version: contactStoreVersion, Selected: "cedar", Contacts: []storedContact{
		{ID: "cedar", Name: "Cedar", Workspace: root, Draft: oldPrompt},
		{ID: "copper", Name: "Copper", Workspace: root, Draft: otherDraft},
	}, Issues: []storedIssue{{ID: "source-activity", ContactID: "cedar", Goal: "Original activity", Status: "not_started", RequirementsRevision: 1}}}); err != nil {
		t.Fatal(err)
	}
	msg := locales.In("en")
	first := openProductWorkbench(t)
	cedar := first.app.contactByID("cedar")
	oldID := cedar.SessionID
	first.click(msg.SendAction())
	first.wait(func() bool {
		return cedar.SendState == sendIdle && len(cedar.History) == 2 && cedar.History[1].Text == oldReply
	})
	oldHistory, err := productHistory(ctx, first.client, oldID)
	if err != nil || len(oldHistory.entries) != 2 {
		t.Fatalf("original chat readback: %+v, %v", oldHistory.entries, err)
	}
	bot := first.app.productHost().productBots[cedar.ID]
	var laterID string
	var laterHistory historyProjection
	for i := range 26 {
		bot, err = first.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
			ProductBotRevisionParams: pi.ProductBotRevisionParams{BotID: bot.BotID, ExpectedRevision: bot.ProfileRevision, OperationID: fmt.Sprintf("fixture-chat-%d", i)},
			Profile:                  bot.Profile, StartNewConversation: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if i != 24 {
			continue
		}
		laterID = bot.CurrentConversationID
		if err := provider.install([]piAppCall{}, laterReply, nil, nil); err != nil {
			t.Fatal(err)
		}
		params := pi.ProductConversationParams{ConversationID: laterID}
		conversation, err := first.client.GetConversation(ctx, laterID)
		if err != nil || conversation.Draft == nil {
			t.Fatalf("follow-up draft readback: %+v, %v", conversation, err)
		}
		draft, err := first.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
			ProductConversationParams: params, ExpectedRevision: conversation.Draft.Revision, OperationID: "seed-follow-up-draft",
			ProductText: pi.ProductText{Text: laterPrompt},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := first.client.Send(ctx, pi.ProductSendParams{
			ProductConversationParams: params, DraftRevision: draft.Revision, OperationID: "seed-follow-up-send",
		}); err != nil {
			t.Fatal(err)
		}
		first.wait(func() bool {
			conversation, err := first.client.GetConversation(ctx, laterID)
			return err == nil && conversation.LatestExecution != nil && conversation.LatestExecution.State == "completed"
		})
		laterHistory, err = productHistory(ctx, first.client, laterID)
		if err != nil || len(laterHistory.entries) != 2 || laterHistory.entries[0].Text != laterPrompt || laterHistory.entries[1].Text != laterReply {
			t.Fatalf("follow-up chat readback: %+v, %v", laterHistory.entries, err)
		}
	}
	currentID := bot.CurrentConversationID
	current, err := first.client.GetConversation(ctx, currentID)
	if err != nil || current.Draft == nil || currentID == oldID {
		t.Fatalf("current chat readback: %+v, %v", current, err)
	}
	material := pi.ProductMaterial{ID: "material-current", Revision: 1, Name: "Current material", Text: "Current snapshot"}
	_, err = first.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: currentID},
		ExpectedRevision:          current.Draft.Revision, OperationID: "seed-current-draft",
		ProductText: pi.ProductText{Text: newPrompt, Materials: []pi.ProductMaterial{material}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first.shutdown(ctx)

	second := openProductWorkbench(t)
	cedar = second.app.contactByID("cedar")
	second.wait(func() bool { return cedar.activity.loaded && len(cedar.activity.items) == 1 })
	second.click(msg.PreviousChats())
	second.wait(func() bool {
		p := second.app.priorChats
		return p != nil && !p.reading && p.nextCursor != nil
	})
	second.pump.do(func() {
		_, oldRow := second.view.Find(oldPrompt)
		_, oldIDLabel := second.view.Find(oldID)
		_, laterRow := second.view.Find(laterLabel)
		_, emptyRow := second.view.Find(msg.PreviousChatFallback(2))
		if !oldRow || oldIDLabel || laterRow || !emptyRow {
			t.Errorf("first page does not expose the original prompt and numbered empty chat: %v", second.view.Texts())
		}
	})
	second.frame(func() { second.app.priorChats.listScroll.Y = 10000 })
	second.click(msg.LoadMoreChats())
	second.wait(func() bool {
		p := second.app.priorChats
		return p != nil && !p.reading && p.nextCursor == nil && len(p.items) == 26
	})
	assertPrior := func(w *productWorkbench, bot *contact, draft, id, reply string, expected historyProjection) {
		t.Helper()
		w.wait(func() bool {
			p := w.app.priorChats
			return p != nil && !p.reading && len(p.history.entries) == 2
		})
		var correct bool
		w.pump.do(func() {
			p := w.app.priorChats
			_, composer := w.view.Find(msg.ComposerLabel(bot.Name))
			correct = p.ownerID == bot.ID && p.selectedID == id && reflect.DeepEqual(p.history.entries, expected.entries) &&
				w.view.HasText(reply) && !composer && bot.SessionID == currentID && bot.Draft == draft
		})
		if !correct {
			t.Fatalf("opening chat %s changed the transcript, draft, or current send target", id)
		}
	}
	second.frame(func() { second.app.priorChats.listScroll.Y = 10000 })
	second.click(laterLabel)
	assertPrior(second, cedar, newPrompt, laterID, laterReply, laterHistory)
	second.frame(func() { second.view.Key(0, ui.KeyEscape) })
	second.wait(func() bool { return second.view.Focused(laterLabel) })
	second.frame(func() { second.app.priorChats.listScroll.Y = 0 })
	second.click(oldPrompt)
	assertPrior(second, cedar, newPrompt, oldID, oldReply, oldHistory)
	second.frame(func() { second.view.Key(0, ui.KeyEscape) })
	second.wait(func() bool { return second.view.Focused(oldPrompt) })
	second.frame(func() { second.view.Key(0, ui.KeyEscape) })
	second.click(msg.WorkActivity())
	second.click(msg.IssueRowLabel("Original activity"))
	second.wait(func() bool {
		return cedar.activity.activity != nil && cedar.activity.activity.OriginConversationID == oldID
	})
	second.click(msg.SourceChat())
	assertPrior(second, cedar, newPrompt, oldID, oldReply, oldHistory)
	second.frame(func() { second.view.Key(0, ui.KeyEscape) })
	second.wait(func() bool { return second.view.Focused(msg.SourceChat()) })
	second.frame(func() { second.app.selectContact(1) })
	second.wait(func() bool {
		ct := second.app.selectedContact()
		return ct.ID == "copper" && ct.Loaded && ct.Draft == otherDraft
	})
	if provider.count() != 2 {
		t.Fatal("opening previous or source chat requested the provider")
	}
	second.frame(func() { second.app.selectContact(0) })
	if err := provider.install([]piAppCall{}, newReply, nil, nil); err != nil {
		t.Fatal(err)
	}
	second.click(msg.SendAction())
	second.wait(func() bool {
		return cedar.SendState == sendIdle && len(cedar.History) == 2 && cedar.History[1].Text == newReply
	})
	newHistory, err := productHistory(ctx, second.client, currentID)
	if err != nil || len(newHistory.entries) != 2 {
		t.Fatalf("current chat history: %+v, %v", newHistory.entries, err)
	}
	prompt := newHistory.entries[0]
	if prompt.ID == oldHistory.entries[0].ID || prompt.Text != newPrompt || len(prompt.Context) != 1 || prompt.Context[0].ID != material.ID || prompt.Context[0].Text != material.Text {
		t.Fatalf("send did not reach the current chat with the captured material: %+v", prompt)
	}
	second.shutdown(ctx)

	third := openProductWorkbench(t)
	third.click(msg.PreviousChats())
	third.wait(func() bool {
		return third.app.priorChats != nil && !third.app.priorChats.reading && len(third.app.priorChats.items) > 0
	})
	third.click(oldPrompt)
	assertPrior(third, third.app.contactByID("cedar"), "", oldID, oldReply, oldHistory)
	third.shutdown(ctx)
	if requests := provider.count(); requests != 3 {
		t.Fatalf("expected just the three intentional sends, saw %d requests", requests)
	}
}

func TestPiAppConcurrentActivities(t *testing.T) {
	const firstID, secondID = "cedar-first", "cedar-second"
	const firstGoal, secondGoal = "Cedar inventory", "Separate inventory"
	const heldPrompt = "Hold separate inventory"
	const answer, resultText = "Use sample fourteen", "Exact Cedar output"
	const material, excerpt = "Cedar answer evidence", "Fourteen is in the reference inventory"
	const resultName, resultCall = "inventory-result.txt", "write-inventory-result"
	const resultContent, revisedContent = "Original inventory file", "Current inventory file"
	const nextPrompt, nextResult = "Recheck the inventory", "Recheck complete, see inventory-result.txt"
	const nextExcerpt, quitPrompt = "Only review the revised inventory", "Copper draft"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	primary := &piAppProvider{limit: 4}
	questionCall := piAppCall{name: "issue_wait", id: "activity-question", args: piAppArguments(map[string]any{"text": "Which sample?"})}
	writeCall := piAppCall{name: "write", id: resultCall, args: piAppArguments(map[string]any{"path": resultName, "content": resultContent})}
	if err := primary.install([]piAppCall{questionCall, writeCall}, resultText, nil, nil); err != nil {
		t.Fatal(err)
	}
	held, stopped := make(chan struct{}), make(chan bool, 1)
	quitHeld, quitCanceled := make(chan struct{}), make(chan struct{})
	var quitRequests atomic.Int32
	var heldRequests atomic.Int32
	root := prepareProductWorkbench(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if bytes.Contains(body, []byte(heldPrompt)) {
			if heldRequests.Add(1) != 1 || bytes.Contains(body, []byte(excerpt)) {
				http.Error(w, "unexpected request for held activity", http.StatusBadRequest)
				return
			}
			close(held)
			select {
			case <-r.Context().Done():
				stopped <- true
			case <-ctx.Done():
				stopped <- false
			}
			return
		}
		if bytes.Contains(body, []byte(quitPrompt)) {
			if quitRequests.Add(1) != 1 {
				http.Error(w, "unexpected repeat of the Quit request", http.StatusBadRequest)
				return
			}
			close(quitHeld)
			<-r.Context().Done()
			close(quitCanceled)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		primary.ServeHTTP(w, r)
	}))
	workspace, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(workspace, resultName)
	if err := writeProfileJSON(guiStorePath(), storedState{Version: contactStoreVersion, Selected: "cedar", Contacts: []storedContact{
		{ID: "cedar", Name: "Cedar", Workspace: root, Draft: "Cedar draft"},
		{ID: "copper", Name: "Copper", Workspace: root, Draft: quitPrompt},
	}, Issues: []storedIssue{
		{ID: firstID, ContactID: "cedar", Goal: firstGoal, Status: "not_started", RequirementsRevision: 1},
		{ID: secondID, ContactID: "cedar", Goal: secondGoal, Status: "not_started", RequirementsRevision: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	msg := locales.In("en")
	open := func() (*productWorkbench, *contact) {
		w := openProductWorkbench(t)
		cedar := w.app.contactByID("cedar")
		w.wait(func() bool { return len(cedar.activity.items) == 2 && !cedar.activity.reading })
		w.click(msg.WorkActivity())
		return w, cedar
	}
	get := func(w *productWorkbench, id string) pi.ProductActivity {
		t.Helper()
		item, err := w.client.GetActivity(ctx, id)
		if err != nil || item.ActivityID != id || item.OwnerBotID != "cedar" {
			t.Fatalf("activity %s readback: %+v, %v", id, item, err)
		}
		return item
	}
	selectActivity := func(w *productWorkbench, cedar *contact, goal, id string) {
		w.click(msg.IssueRowLabel(goal))
		w.wait(func() bool {
			return cedar.activity.activity != nil && cedar.activity.activity.ActivityID == id && !cedar.activity.reading
		})
	}
	readResult := func(w *productWorkbench, cedar *contact, run int, executionID, text string) pi.ProductActivityResult {
		t.Helper()
		label := msg.IssueOpenResultRun(fmt.Sprint(run))
		var clickErr error
		var started bool
		w.wait(func() bool {
			p := &cedar.activity
			if p.reading || p.activityID != firstID || p.activity == nil || len(p.activity.Executions) < run ||
				p.activity.Executions[run-1].ExecutionID != executionID {
				return false
			}
			p.scroll.Y = 1000
			w.view.Frame()
			if p.reading {
				return false
			}
			clickErr = w.view.Click(label)
			w.view.Frame()
			started = p.reading
			return true
		})
		if clickErr != nil || !started {
			t.Fatalf("open result %d for activity %s execution %s: click error=%v read started=%v", run, firstID, executionID, clickErr, started)
		}
		w.wait(func() bool {
			result := cedar.activity.result
			return result != nil && result.ActivityID == firstID && result.ExecutionID == executionID && w.view.HasText(text)
		})
		var result pi.ProductActivityResult
		w.pump.do(func() { result = *cedar.activity.result })
		return result
	}
	w, cedar := open()
	var copperVisible bool
	w.pump.do(func() {
		bounds, found := w.view.Find("Copper")
		copperVisible = found && bounds.W > 0 && bounds.H > 0
	})
	if !copperVisible {
		t.Fatal("Copper disappeared from the workbench while viewing Cedar activities")
	}
	bottom := func() { w.frame(func() { cedar.activity.scroll.Y = 1000 }) }
	back := func() {
		w.click(msg.BackLabel())
		w.wait(func() bool { return cedar.activity.activityID == "" && !cedar.activity.reading })
	}
	resume := func(text string) {
		w.click(msg.SupplementAdd())
		w.frame(func() { w.view.Type(text) })
		bottom()
		w.click(msg.IssueResume())
	}
	selectActivity(w, cedar, firstGoal, firstID)
	resume("Start Cedar inventory")
	w.wait(func() bool {
		item := cedar.activity.activity
		return item != nil && len(item.OpenQuestionIDs) == 1 && !cedar.activity.reading
	})
	question := get(w, firstID).Questions[0]
	w.frame(func() { cedar.activity.scroll.Y = 0 })
	w.click(msg.IssueNoticeWaiting(firstGoal))
	w.frame(func() { w.view.Type(answer) })
	bottom()
	w.click(msg.TaskContextAddSnippet())
	bottom()
	w.click(msg.TaskContextNameLabel())
	w.frame(func() { w.view.Type(material) })
	bottom()
	w.click(msg.TaskContextTextLabel())
	w.frame(func() { w.view.Type(excerpt) })
	bottom()
	w.click(msg.TaskContextSaveSnippet())
	back()
	selectActivity(w, cedar, secondGoal, secondID)
	resume(heldPrompt)
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("the second activity never reached the provider")
	}
	w.wait(func() bool {
		item := cedar.activity.activity
		return item != nil && len(item.Executions) == 1 && item.Executions[0].State == "running" && !cedar.activity.reading
	})
	secondRun := get(w, secondID).Executions[0].ExecutionID
	connection, err := w.client.Connection(ctx)
	if err != nil || connection.EffectiveRevision == nil || connection.Effective == nil {
		t.Fatalf("read effective connection before Apply: %+v, %v", connection, err)
	}
	configuration := connection.Effective.ProductConnectionSettings
	configuration.Model = "unapplied-fixture-model"
	savedConnection, err := w.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID:   "save-while-activity-held",
		Configuration: pi.ProductConnectionInput{ProductConnectionSettings: configuration, APIKey: "nagi-product-loopback"},
	})
	if err != nil || savedConnection.SavedRevision == nil || *savedConnection.SavedRevision == *connection.EffectiveRevision {
		t.Fatalf("save separate connection revision: %+v, %v", savedConnection, err)
	}
	_, err = w.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{SavedRevision: *savedConnection.SavedRevision, OperationID: "apply-while-activity-held"})
	var rejection *pi.RemoteError
	if !errors.As(err, &rejection) || rejection.Code != "busy" {
		t.Fatalf("Apply during active work should return busy: %v", err)
	}
	current, err := w.client.Connection(ctx)
	if err != nil || !reflect.DeepEqual(current.EffectiveRevision, connection.EffectiveRevision) ||
		!reflect.DeepEqual(current.Effective, connection.Effective) || !reflect.DeepEqual(current.SavedRevision, savedConnection.SavedRevision) {
		t.Fatalf("busy Apply changed saved or effective connection: %+v, %v", current, err)
	}
	running := get(w, secondID).Executions
	if len(running) != 1 || running[0].ExecutionID != secondRun || running[0].State != "running" {
		t.Fatalf("busy Apply affected the active execution: %+v", running)
	}
	back()
	selectActivity(w, cedar, firstGoal, firstID)
	bottom()
	profile := guiStorePath()
	activityBefore := get(w, firstID)
	durableBefore := recoverySavedBytes(t, profile)
	requestsBefore := primary.count()
	func() {
		backup := filepath.Join(root, "profile-before-answer.json")
		if err := os.Rename(profile, backup); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Rename(backup, profile); err != nil {
				t.Fatalf("restore the test profile: %v", err)
			}
		}()
		if err := os.Mkdir(profile, 0700); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Remove(profile); err != nil {
				t.Errorf("remove the write obstruction: %v", err)
			}
		}()
		w.click(msg.ContactSaveChanges())
		w.wait(func() bool {
			p := &cedar.activity
			if p.changing || !strings.Contains(p.actionProblem, "rename") {
				return false
			}
			p.scroll.Y = 0
			w.view.Frame()
			return w.view.HasText(p.actionProblem)
		})
		var formRetained bool
		w.pump.do(func() {
			form := cedar.activity.forms[firstID]
			formRetained = form != nil && form.target.QuestionID == question.QuestionID && form.target.ExecutionID == question.ExecutionID &&
				form.text == answer && len(form.materials.Items) == 1 && form.materials.Items[0].Name == material && form.materials.Items[0].Text == excerpt
		})
		if !formRetained || !reflect.DeepEqual(get(w, firstID), activityBefore) || primary.count() != requestsBefore ||
			!bytes.Equal(recoverySavedBytes(t, backup), durableBefore) {
			t.Fatal("failed answer write changed the form, question, durable profile, or provider requests")
		}
	}()
	bottom()
	w.click(msg.ContactSaveChanges())
	w.wait(func() bool {
		item := cedar.activity.activity
		return item != nil && len(item.Executions) == 1 && item.Executions[0].State == "completed" && len(item.Questions) == 1 && item.Questions[0].Delivery == "delivered" && !cedar.activity.reading
	})
	completedRun := get(w, firstID)
	if completedRun.State != "needsAttention" {
		t.Fatalf("completed execution changed activity to %q", completedRun.State)
	}
	answered := completedRun.Questions[0]
	if answered.QuestionID != question.QuestionID || answered.ExecutionID != question.ExecutionID || answered.Answer == nil ||
		answered.Answer.Text != answer || len(answered.Answer.Materials) != 1 || answered.Answer.Materials[0].Name != material || answered.Answer.Materials[0].Text != excerpt {
		t.Fatalf("the answer and captured material changed across the activity switch: %+v", answered)
	}
	back()
	selectActivity(w, cedar, secondGoal, secondID)
	bottom()
	w.click(msg.WorkStopIssueTask())
	select {
	case cancelled := <-stopped:
		if !cancelled {
			t.Fatal("the held provider request reached its deadline")
		}
	case <-ctx.Done():
		t.Fatal("Stop did not cancel the held provider request")
	}
	w.wait(func() bool {
		item := cedar.activity.activity
		return item != nil && len(item.Executions) == 1 && item.Executions[0].State == "interrupted" && !cedar.activity.reading
	})
	back()
	selectActivity(w, cedar, firstGoal, firstID)
	if err := primary.install([]piAppCall{}, nextResult, nil, nil); err != nil {
		t.Fatal(err)
	}
	next, err := w.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: firstID},
		ExpectedRevision:      get(w, firstID).Revision,
		OperationID:           "continue-cedar-again",
		ProductText: pi.ProductText{Text: nextPrompt, Materials: []pi.ProductMaterial{
			{ID: "recheck-input", Revision: 1, Name: "Recheck evidence", Text: nextExcerpt},
		}},
	})
	if err != nil || next.Execution == nil || next.Execution.ExecutionID == question.ExecutionID {
		t.Fatalf("continuing the same activity did not admit a distinct execution: %+v, %v", next, err)
	}
	nextID := next.Execution.ExecutionID
	w.wait(func() bool {
		item := cedar.activity.activity
		return item != nil && len(item.Executions) == 2 && item.Executions[1].ExecutionID == nextID && item.Executions[1].State == "completed" && !cedar.activity.reading
	})
	secondResult := readResult(w, cedar, 2, nextID, nextResult)
	if secondResult.ActivityID != firstID || secondResult.ExecutionID != nextID || secondResult.Text != nextResult || len(secondResult.Files) != 0 {
		t.Fatalf("the second execution inherited a result or file effect: %+v", secondResult)
	}
	w.click(msg.BackLabel())
	result := readResult(w, cedar, 1, question.ExecutionID, resultText)
	if result.ActivityID != firstID || result.ExecutionID != question.ExecutionID || result.Text != resultText {
		t.Fatalf("result opened against the wrong execution: %+v", result)
	}
	if len(result.Files) != 1 || result.Files[0].Path != resultPath || result.Files[0].CallID != resultCall {
		t.Fatalf("result lost the exact confirmed file effect: %+v", result.Files)
	}
	readCurrentFile := func(w *productWorkbench, owner *contact, expected string) {
		t.Helper()
		w.frame(func() { owner.activity.scroll.Y = 1000 })
		w.click(msg.ResultFileRowLabel("1", resultName))
		w.wait(func() bool { return w.view.HasText(expected) && w.view.HasText(msg.ResultCurrentFileDiff()) })
	}
	readCurrentFile(w, cedar, resultContent)
	w.shutdown(ctx)
	if err := os.WriteFile(resultPath, []byte(revisedContent), 0600); err != nil {
		t.Fatal(err)
	}

	reopened, cedar := open()
	selectActivity(reopened, cedar, firstGoal, firstID)
	if got := readResult(reopened, cedar, 2, nextID, nextResult); !reflect.DeepEqual(got, secondResult) {
		t.Fatalf("second result changed after restart: %+v", got)
	}
	reopened.click(msg.BackLabel())
	if got := readResult(reopened, cedar, 1, question.ExecutionID, resultText); !reflect.DeepEqual(got, result) {
		t.Fatalf("result changed after restart: %+v", got)
	}
	readCurrentFile(reopened, cedar, revisedContent)
	firstAfter, secondAfter := get(reopened, firstID), get(reopened, secondID)
	if firstAfter.State != "needsAttention" || len(firstAfter.Questions) != 1 || !reflect.DeepEqual(firstAfter.Questions[0], answered) ||
		len(secondAfter.Executions) != 1 || secondAfter.Executions[0].ExecutionID != secondRun || secondAfter.Executions[0].State != "interrupted" {
		t.Fatal("restart changed the answered question or the stopped execution")
	}
	var drafts [2]string
	reopened.pump.do(func() { drafts = [2]string{cedar.Draft, reopened.app.contactByID("copper").Draft} })
	if drafts != [2]string{"Cedar draft", quitPrompt} {
		t.Fatalf("restart changed the two Bot drafts: %q", drafts)
	}
	copper := reopened.app.contactByID("copper")
	reopened.frame(func() { reopened.app.selectContact(1) })
	reopened.wait(func() bool { return reopened.app.selectedContact() == copper && copper.Loaded })
	reopened.click(msg.SendAction())
	select {
	case <-quitHeld:
	case <-ctx.Done():
		t.Fatal("the conversation did not reach the provider before Quit")
	}
	reopened.wait(func() bool {
		return copper.SendState == sendAccepted && copper.ActiveTaskID != "" && reopened.app.submissionInFlight == 0
	})
	var quitExecution, quitConversation string
	var quitCalls int
	reopened.pump.do(func() {
		quitExecution, quitConversation = copper.ActiveTaskID, copper.SessionID
		reopened.app.requestQuit = func() { quitCalls++ }
		reopened.app.drainPiHost()
	})
	reopened.pump.wait(func() bool { return reopened.app.quitReady })
	outcome, err := reopened.client.Wait(ctx)
	if err != nil || !outcome.Reaped || outcome.ExitCode != 0 {
		t.Fatalf("active desktop Quit did not reap the worker: %+v, %v", outcome, err)
	}
	select {
	case <-reopened.client.CallbacksDone():
	default:
		t.Fatal("desktop Quit completed with worker callbacks still running")
	}
	select {
	case <-quitCanceled:
	case <-ctx.Done():
		t.Fatal("desktop Quit did not cancel the provider request")
	}
	reopened.pump.do(func() {
		if quitCalls != 1 || reopened.app.pi != nil || copper.ActiveTaskID != "" || copper.LastTaskID != quitExecution || copper.SessionState != "interrupted" {
			t.Errorf("desktop Quit did not settle the captured execution before releasing the worker: calls=%d execution=%s state=%s", quitCalls, copper.LastTaskID, copper.SessionState)
		}
	})
	afterQuit := openProductWorkbench(t)
	conversation, err := afterQuit.client.GetConversation(ctx, quitConversation)
	if err != nil || conversation.LatestExecution == nil || conversation.LatestExecution.ExecutionID != quitExecution || conversation.LatestExecution.State != "interrupted" {
		t.Fatalf("reopen after Quit lost the interrupted execution: %+v, %v", conversation, err)
	}
	afterQuit.shutdown(ctx)
	primary.mu.Lock()
	defer primary.mu.Unlock()
	if heldRequests.Load() != 1 || quitRequests.Load() != 1 || primary.requests != 4 || len(primary.problems) != 0 ||
		!bytes.Contains(primary.bodies[1], []byte(answer)) || !bytes.Contains(primary.bodies[1], []byte(excerpt)) {
		t.Fatalf("provider request routing or replay mismatch: answered=%d held=%d errors=%v", primary.requests, heldRequests.Load(), primary.problems)
	}
	var continuation struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(primary.bodies[3], &continuation); err != nil || len(continuation.Messages) == 0 {
		t.Fatalf("invalid second execution request: %v", err)
	}
	input := continuation.Messages[len(continuation.Messages)-1]
	if input.Role != "user" || !bytes.Contains(input.Content, []byte(nextPrompt)) || !bytes.Contains(input.Content, []byte(nextExcerpt)) || bytes.Contains(input.Content, []byte(excerpt)) {
		t.Fatalf("second execution received the wrong captured input: %s", input.Content)
	}
}

func TestPiAppSettingsBackupRestore(t *testing.T) {
	const seedReply = "Cedar recovery transcript"
	const importedOperation = "copper-operation"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	provider := &piAppProvider{limit: 1}
	held, release := make(chan struct{}), make(chan struct{})
	if err := provider.install([]piAppCall{}, seedReply, held, release); err != nil {
		t.Fatal(err)
	}
	root := prepareProductWorkbench(t, provider)
	profile := guiStorePath()
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if err := writeProfileJSON(profile, storedState{
		Version: contactStoreVersion, Selected: "cedar",
		Contacts: []storedContact{
			{ID: "cedar", Name: "Cedar", Workspace: root, Draft: "Seed Cedar"},
			{ID: "copper", Name: "Copper", Workspace: root, Draft: "Copper retained draft",
				TaskContext: []taskContextItem{{ID: "copper-material", Name: "Copper evidence", Text: "Copper captured text", Revision: 1}},
				PendingSend: &pendingSend{Draft: "Unconfirmed Copper dispatch", State: "unknown", ToolScopeRef: "copper-scope"}},
		},
		Issues:          []storedIssue{{ID: "copper-activity", ContactID: "copper", Goal: "Review Copper materials", Status: "not_started"}},
		IssueToolScopes: []IssueToolScope{{Ref: "copper-scope", ContactID: "copper", TurnID: importedOperation, RuntimeTaskID: "copper-execution"}},
	}); err != nil {
		t.Fatal(err)
	}
	msg := locales.In("en")
	exports := filepath.Join(root, "exports")
	if err := os.Mkdir(exports, 0700); err != nil {
		t.Fatal(err)
	}
	w := openProductWorkbench(t)
	cedar, copper := w.app.contactByID("cedar"), w.app.contactByID("copper")
	cedarID, copperID := cedar.SessionID, copper.SessionID
	w.click(msg.SendAction())
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("the Cedar send never reached the provider")
	}
	w.wait(func() bool { return cedar.ActiveTaskID != "" && w.app.submissionInFlight == 0 })
	w.frame(func() {
		w.app.chooseFolder = func(mygo.OpenDialogOptions) ([]string, error) { return []string{exports}, nil }
		w.app.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	})
	export := func() {
		w.frame(func() { w.app.settingsScroll.Y = 10000 })
		w.click(msg.SaveRecoveryCopyAction())
		w.wait(func() bool { return !w.app.dataRecovery.choosing && !w.app.dataRecovery.inFlight })
	}
	export()
	entries, err := os.ReadDir(exports)
	if err != nil || len(entries) != 0 {
		t.Fatalf("backup created files during the held send: %v, %v", entries, err)
	}
	var refusal string
	w.pump.do(func() { refusal = w.app.dataRecovery.problem })
	if !strings.Contains(refusal, "busy") {
		t.Fatalf("Settings did not report the live execution refusal: %q", refusal)
	}
	close(release)
	w.wait(func() bool {
		return cedar.ActiveTaskID == "" && len(cedar.History) == 2 && cedar.History[1].Text == seedReply && w.app.contactHistoryInFlight == 0
	})
	history, err := productHistory(ctx, w.client, cedarID)
	if err != nil {
		t.Fatal(err)
	}
	var saveErr error
	w.frame(func() {
		cedar.Draft = "Cedar next draft"
		saveErr = w.app.saveProductDraft(cedar)
		w.app.selectContact(1)
	})
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	w.wait(func() bool { return w.app.selectedID() == "copper" && w.app.contactHistoryInFlight == 0 })
	receipt, err := w.client.Operation(ctx, importedOperation)
	if err != nil || receipt.State != "unknown" || receipt.Execution == nil {
		t.Fatalf("imported uncertainty receipt: %+v, %v", receipt, err)
	}
	private := connectionConfigPath(profile)
	original, credentials := read(profile), read(private)
	export()
	var backup string
	w.pump.do(func() { backup = w.app.dataRecovery.output })
	if filepath.Dir(backup) != exports {
		t.Fatalf("Settings did not create a recovery copy in %s: %q", exports, backup)
	}
	w.wait(func() bool { return w.view.HasText(msg.RecoveryCopySaved(safeDiagnostic(backup))) })
	captured := read(backup)
	if !bytes.Equal(read(profile), original) || !bytes.Equal(read(private), credentials) || bytes.Contains(captured, []byte("nagi-product-loopback")) {
		t.Fatal("backup changed saved data or included private credentials")
	}
	w.shutdown(ctx)

	var changed map[string]json.RawMessage
	var conversations []map[string]json.RawMessage
	if err := json.Unmarshal(captured, &changed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(changed["conversations"], &conversations); err != nil {
		t.Fatal(err)
	}
	var origin, foreign map[string]json.RawMessage
	for _, item := range conversations {
		var id string
		if err := json.Unmarshal(item["conversationId"], &id); err != nil {
			t.Fatal(err)
		}
		switch id {
		case cedarID:
			origin = item
		case copperID:
			foreign = item
		}
	}
	if origin["latestExecutionId"] == nil || foreign == nil {
		t.Fatal("backup did not preserve the two conversations and Cedar execution")
	}
	foreign["latestExecutionId"] = origin["latestExecutionId"]
	delete(origin, "latestExecutionId")
	changed["conversations"], err = json.Marshal(conversations)
	if err != nil {
		t.Fatal(err)
	}
	foreignBytes, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	foreignCopy := filepath.Join(root, "foreign.json")
	write(foreignCopy, foreignBytes)
	damaged := []byte(`{"version":1,"bots":[]}`)
	write(profile, damaged)
	if err := writeProfileJSON(productUIPath(profile), productUIState{Selected: "cedar"}); err != nil {
		t.Fatal(err)
	}
	if opened, err := newProductRuntimeApp(); err == nil || opened != nil {
		t.Fatal("the damaged profile was admitted")
	} else {
		unavailable := newUnavailableApp(err)
		restore := newProductWorkbench(t, unavailable)
		t.Cleanup(restore.pump.close)
		chosen := foreignCopy
		restore.frame(func() {
			unavailable.chooseRecoveryFile = func(mygo.OpenDialogOptions) ([]string, error) { return []string{chosen}, nil }
			unavailable.openDataRecovery()
		})
		restore.click(msg.RestorePausedAction())
		restore.wait(func() bool { return restore.view.HasText(msg.RecoveryCopyUnusable()) })
		if !bytes.Equal(read(profile), damaged) {
			t.Fatal("a foreign execution association changed the target profile")
		}
		restore.pump.do(func() { chosen = backup })
		preview := func() {
			restore.click(msg.RestorePausedAction())
			restore.wait(func() bool {
				return restore.view.HasText(msg.RecoveryPreviewCounts(2, 1)) && restore.view.Focused(msg.RecoveryConfirmAction())
			})
			if !bytes.Equal(read(profile), damaged) || !bytes.Equal(read(private), credentials) {
				t.Fatal("preview changed the original profile or credentials")
			}
		}
		preview()
		restore.click(msg.RecoveryCancelAction())
		restore.wait(func() bool { return !restore.view.HasText(msg.RecoveryPreviewTitle()) })
		if !bytes.Equal(read(profile), damaged) || !bytes.Equal(read(backup), captured) || !bytes.Equal(read(private), credentials) {
			t.Fatal("canceling the preview changed the original, backup or credentials")
		}
		preview()
		restore.click(msg.RecoveryConfirmAction())
		restore.wait(func() bool { return restore.view.HasText(msg.RecoveryInstalled()) })
		var archive string
		restore.pump.do(func() { archive = unavailable.dataRecovery.result.ArchivePath })
		if !bytes.Equal(read(archive), damaged) || !bytes.Equal(read(backup), captured) || !bytes.Equal(read(private), credentials) {
			t.Fatal("restore changed the archived bytes, backup or private connection")
		}
		restore.click(msg.RecoveryQuitAction())
		restore.wait(func() bool { return unavailable.quitReady })
	}

	reopened := openProductWorkbench(t)
	var restored bool
	reopened.pump.do(func() {
		c, p := reopened.app.contactByID("cedar"), reopened.app.contactByID("copper")
		restored = reopened.app.selectedID() == "copper" && c.SessionID == cedarID && p.SessionID == copperID &&
			c.Draft == "Cedar next draft" && p.Draft == "Copper retained draft" && p.SendState == sendUnknown &&
			len(p.Context.Items) == 1 && p.Context.Items[0].Text == "Copper captured text"
	})
	if !restored {
		t.Fatal("reopen lost Bot selection, drafts, materials, or send uncertainty")
	}
	after, err := productHistory(ctx, reopened.client, cedarID)
	if err != nil || !reflect.DeepEqual(after.entries, history.entries) {
		t.Fatalf("restore changed the Pi transcript: %+v, %v", after.entries, err)
	}
	got, err := reopened.client.Operation(ctx, importedOperation)
	if err != nil || !reflect.DeepEqual(got, receipt) {
		t.Fatalf("restore changed the unknown receipt: %+v, %v", got, err)
	}
	reopened.shutdown(ctx)
	if requests := provider.count(); requests != 1 {
		t.Fatalf("backup, restore or reopen replayed a send: %d requests", requests)
	}
}

func TestPiAppRestoresExistingV10Copy(t *testing.T) {
	const botID, activityID = "restored-bot", "restored-activity"
	const sendOperation, sendExecution = "restore-send", "restore-send-execution"
	const activityExecution = "restored-activity-execution"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	provider := &piAppProvider{}
	root := prepareProductWorkbench(t, provider)
	profile := guiStorePath()
	private := connectionConfigPath(profile)
	credentials := recoverySavedBytes(t, private)
	config, err := readPrivateConnectionConfig(private)
	if err != nil {
		t.Fatal(err)
	}
	const session = "saved-v10-session.jsonl"
	binding := recoveryConversationBinding(botID, root, session)
	binding.Context.Used = false
	binding.Configuration = piConfiguration{WireAPI: pi.OpenAICompletions, BaseURL: config.Sources[config.CredentialSource].Endpoint,
		Model: config.Model, ContextTokens: config.ContextTokens, OutputTokens: config.OutputTokens, CredentialSource: config.CredentialSource}
	const text = "Preserve the saved draft 原文"
	material := taskContextItem{ID: "saved-material", Name: "Retained note", Text: "Captured before backup", Revision: 2}
	copyPath := filepath.Join(root, "v10-backup.json")
	if err := writeProfileJSON(copyPath, storedState{Version: contactStoreVersion, Selected: botID,
		Contacts: []storedContact{{ID: botID, Name: "Restored Bot", Workspace: root, SessionID: session, PiBinding: binding, Draft: text,
			TaskContext: []taskContextItem{material}, PendingSend: &pendingSend{Draft: "Earlier uncertain send", State: "unknown", ToolScopeRef: "backup-scope"}}},
		Issues: []storedIssue{{ID: activityID, ContactID: botID, Goal: "Restore retained findings", Status: issueStatusNeedsAttention, RequirementsRevision: 2,
			RequirementItems: []storedRequirement{{ID: "retained-requirement", Text: "Keep the findings", State: requirementUnverified}},
			Executions:       []storedIssueExecution{{ID: activityExecution, PiIntent: true, Status: issueExecutionUnknown, Prompt: "Original activity input"}}}},
		IssueToolScopes:     []IssueToolScope{{Ref: "backup-scope", ContactID: botID, RuntimeSessionID: session, TurnID: sendOperation, RuntimeTaskID: sendExecution}},
		IssueToolOperations: &IssueToolOperationLedger{},
	}); err != nil {
		t.Fatal(err)
	}
	captured := recoverySavedBytes(t, copyPath)
	damaged := []byte("{damaged original")
	if err := os.WriteFile(profile, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	opened, err := newProductRuntimeApp()
	if err == nil || opened != nil {
		t.Fatal("Product startup admitted the damaged profile")
	}
	unavailable := newUnavailableApp(err)
	restore := newProductWorkbench(t, unavailable)
	t.Cleanup(restore.pump.close)
	msg := locales.In("en")
	restore.frame(func() {
		unavailable.chooseRecoveryFile = func(mygo.OpenDialogOptions) ([]string, error) { return []string{copyPath}, nil }
		unavailable.openDataRecovery()
	})
	restore.click(msg.RestorePausedAction())
	restore.wait(func() bool {
		return restore.view.HasText(msg.RecoveryPreviewCounts(1, 1)) && restore.view.Focused(msg.RecoveryConfirmAction())
	})
	if !bytes.Equal(recoverySavedBytes(t, profile), damaged) {
		t.Fatal("preview changed the damaged profile")
	}
	restore.click(msg.RecoveryConfirmAction())
	restore.wait(func() bool { return restore.view.HasText(msg.RecoveryInstalled()) })
	var archive string
	restore.pump.do(func() { archive = unavailable.dataRecovery.result.ArchivePath })
	if !bytes.Equal(recoverySavedBytes(t, archive), damaged) || !bytes.Equal(recoverySavedBytes(t, copyPath), captured) ||
		!bytes.Equal(recoverySavedBytes(t, private), credentials) {
		t.Fatal("restore changed the backup, archived bytes, or credentials")
	}
	restore.click(msg.RecoveryQuitAction())
	restore.wait(func() bool { return unavailable.quitReady })
	reopened := openProductWorkbench(t)
	reopened.pump.do(func() {
		owner := reopened.app.selectedContact()
		if owner == nil || owner.ID != botID || owner.SessionID != binding.Context.ContextID || owner.Draft != text || owner.SendState != sendUnknown ||
			!reflect.DeepEqual(owner.Context.Items, []taskContextItem{material}) {
			t.Error("Product reopen lost the selected Bot, conversation, draft, material, or uncertain send")
		}
	})
	receipt, err := reopened.client.Operation(ctx, sendOperation)
	if err != nil || receipt.State != "unknown" || receipt.Target.ConversationID != binding.Context.ContextID || receipt.Execution == nil ||
		receipt.Execution.ExecutionID != sendExecution || receipt.Execution.State != "unknown" {
		t.Fatalf("Product reopen lost the original uncertain send receipt: %+v, %v", receipt, err)
	}
	activity, err := reopened.client.GetActivity(ctx, activityID)
	if err != nil || activity.OwnerBotID != botID || activity.OriginConversationID != binding.Context.ContextID || activity.State != "needsAttention" ||
		!reflect.DeepEqual(activity.Requirements, []string{"Keep the findings"}) || len(activity.Executions) != 1 ||
		activity.Executions[0].ExecutionID != activityExecution || activity.Executions[0].State != "unknown" {
		t.Fatalf("Product reopen lost the original activity or its execution: %+v, %v", activity, err)
	}
	reopened.shutdown(ctx)
	if requests := provider.count(); requests != 0 {
		t.Fatalf("restoring or reopening the v10 backup dispatched %d model requests", requests)
	}
}
