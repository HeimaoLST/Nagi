//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

var delegationMarkerPattern = regexp.MustCompile(`DELEGATION_[A-Z_]+`)

type delegationReply struct {
	tool string
	id   string
	args map[string]any
	text string
}

type delegationRequest struct {
	input, callID, result string
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
	reply := route(delegationRequest{input: input, callID: last.CallID, result: result})
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
		Issues: []storedActivity{{ID: foreignActivity, ContactID: unrelated, Goal: "Unrelated inventory", Status: activityStatusNotStarted, RequirementsRevision: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	delegate := map[string]any{"key": key, "goal": goal, "assignee_id": recipient, "requirements": []string{"Use the saved inventory"}}
	var admitted atomic.Value
	p.routes["DELEGATION_PRODUCT_CHAT"] = func(request delegationRequest) delegationReply {
		switch request.callID {
		case "":
			return delegationReply{tool: "activity_get", id: "foreign-read", args: map[string]any{"activity_id": foreignActivity}}
		case "foreign-read":
			if !strings.Contains(request.result, `"scope_denied"`) || strings.Contains(request.result, "Unrelated inventory") {
				t.Errorf("foreign activity lookup leaked or returned the wrong refusal: %s", request.result)
			}
			return delegationReply{tool: "activity_delegate", id: "delegate-first", args: delegate}
		case "delegate-first":
			var receipt pi.ProductReceipt
			if err := json.Unmarshal([]byte(request.result), &receipt); err != nil || receipt.Target.ActivityID == "" || receipt.Execution == nil || receipt.Execution.ExecutionID == "" {
				t.Errorf("Product did not admit the delegated activity: %s, %v", request.result, err)
			}
			admitted.Store(receipt)
			return delegationReply{tool: "activity_delegate", id: "delegate-repeat", args: delegate}
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
	openProfile := func(workbench *productWorkbench, name string) {
		t.Helper()
		var openErr error
		workbench.frame(func() { openErr = chooseBotProfileFromRail(workbench.view, msg.AvatarContactLabel(name)) })
		if openErr != nil {
			t.Fatal(openErr)
		}
	}
	openProfile(w, botsBefore[recipientIndex].Profile.Name)
	w.click(msg.FieldNameAccessible())
	w.frame(func() {
		w.view.Key(ui.Cmd, ui.KeyA)
		w.view.Type(savedName)
	})
	saveProfile := func(wantError, wantName string) {
		t.Helper()
		var clickErr error
		w.frame(func() { clickErr = clickBotProfileAction(w.view, msg.ContactSaveChanges()) })
		if clickErr != nil {
			t.Fatal(clickErr)
		}
		w.pump.do(func() {
			editor := w.app.profileEditorFor(recipient)
			if editor == nil || editor.Error != wantError || editor.Draft.Name != savedName || w.app.contactProfileID != recipient ||
				w.app.contactByID(recipient).Name != wantName || w.app.selectedID() != origin || w.app.settingsOpen {
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
	profileAction := func(label string) {
		t.Helper()
		var actionErr error
		reopened.frame(func() { actionErr = clickBotProfileAction(reopened.view, label) })
		if actionErr != nil {
			t.Fatal(actionErr)
		}
	}
	currentRecipient := func() pi.ProductBot {
		t.Helper()
		bots := listBots(reopened.client)
		index := slices.IndexFunc(bots, func(bot pi.ProductBot) bool { return bot.BotID == recipient })
		if index < 0 {
			t.Fatal("the recipient disappeared from the Bot Team")
		}
		return bots[index]
	}
	openProfile(reopened, savedName)
	profileAction(msg.ProfileArchive())
	archived := currentRecipient()
	if !archived.Archived || archived.CurrentConversationID != recipientConversation {
		t.Fatalf("Archive changed the wrong Bot or conversation: %+v", archived)
	}
	reopened.click(msg.ProfileClose())
	reopened.wait(func() bool {
		return reopened.view.Focused(msg.AvatarContactLabel(savedName)) && reopened.app.selectedID() == origin
	})
	openProfile(reopened, savedName)
	profileAction(msg.ProfileRestore())
	restored := currentRecipient()
	if restored.Archived || restored.CurrentConversationID != recipientConversation {
		t.Fatalf("Restore did not keep the recipient's conversation: %+v", restored)
	}
	reopened.pump.do(func() {
		if reopened.view.HasText("Save and start new chat") {
			t.Errorf("retired Save and start new chat rendered in Bot profile: %v", reopened.view.Texts())
		}
	})
	profileAction(msg.ContactSaveChanges())
	updated := currentRecipient()
	if updated.CurrentConversationID != recipientConversation {
		t.Fatalf("Save changes rotated the recipient conversation: got %q want %q", updated.CurrentConversationID, recipientConversation)
	}
	sameChat, err := reopened.client.GetConversation(ctx, updated.CurrentConversationID)
	if err != nil || sameChat.BotID != recipient || !reflect.DeepEqual(sameChat.Draft, draftBefore.Draft) || sameChat.LatestExecution != nil {
		t.Fatalf("profile/lifecycle actions did not preserve the recipient conversation and draft without sending: %+v, %v", sameChat, err)
	}
	reopened.pump.do(func() {
		if reopened.app.selectedID() != origin {
			t.Error("recipient profile actions changed the selected chat")
		}
	})
	reopened.shutdown(ctx)
	if got := p.requests.Load(); got != 5 {
		t.Fatalf("delegation, duplicate or restart contacted the provider %d times, want 5", got)
	}
}
