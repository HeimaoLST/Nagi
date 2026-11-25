//go:build integration

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestPiAppProductConnectionStopAndWorkerLoss(t *testing.T) {
	const (
		key        = "nagi-product-loopback"
		model      = "nagi-product-fixture"
		firstInput = "Inventory the original workspace"
		firstReply = "Original workspace inventoried"
		stopInput  = "Check the original workspace again"
		stopDelta  = "Original workspace check in progress"
		lossInput  = "Inventory the second workspace"
		nextDraft  = "Keep this unsent draft after recovery"
	)
	node, worker := os.Getenv("NAGI_TEST_NODE"), os.Getenv("NAGI_TEST_WORKER")
	payload := filepath.Dir(filepath.Dir(node))
	if !filepath.IsAbs(node) || node != filepath.Join(payload, "bin", "node") || worker != filepath.Join(payload, "dist", "worker.js") {
		t.Fatal("NAGI_TEST_NODE and NAGI_TEST_WORKER must identify one pinned payload")
	}
	root, launchFolder, explicitFolder := t.TempDir(), t.TempDir(), t.TempDir()
	t.Chdir(launchFolder)
	isolateConnectionApp(t, root, payload)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	primary := &piAppProvider{limit: 2}
	secondary := &piAppProvider{limit: 1}
	var modelsProbes atomic.Int32
	streamStarted, lossArrived := make(chan struct{}), make(chan struct{})
	streamCancelled := make(chan bool, 1)
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			if r.Header.Get("Authorization") != "Bearer "+key {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			modelsProbes.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"object":"list","data":[{"id":%q}]}`, model)
			return
		}
		if primary.count() != 1 {
			primary.ServeHTTP(w, r)
			return
		}
		stream := &piFirstUseWriter{ResponseWriter: w, started: streamStarted, release: ctx.Done(), ctx: r.Context()}
		primary.ServeHTTP(stream, r)
		streamCancelled <- r.Context().Err() != nil
	}))
	t.Cleanup(original.Close)
	replacement := httptest.NewServer(secondary)
	t.Cleanup(replacement.Close)
	firstEndpoint, secondEndpoint := original.URL+"/v1", replacement.URL+"/v1"
	install := func(p *piAppProvider, reply string, arrived chan struct{}) {
		t.Helper()
		if err := p.install([]piAppCall{}, reply, arrived, ctx.Done()); err != nil {
			t.Fatal(err)
		}
	}
	await := func(signal <-chan struct{}, action string) {
		t.Helper()
		select {
		case <-signal:
		case <-ctx.Done():
			t.Fatalf("provider did not reach %s: %v", action, ctx.Err())
		}
	}

	msg := locales.In("en")
	w := openProductWorkbench(t)
	initialClient := w.client
	go w.app.observeProductExit(w.app.productHost())
	enter := func(label, text string) {
		t.Helper()
		var err error
		w.frame(func() {
			err = focusPiControl(w.view, label, true)
			if err == nil {
				w.view.Key(ui.Cmd, ui.KeyA)
				w.view.Type(text)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	providers := func() pi.ProductProviderList {
		t.Helper()
		list, err := w.client.ListProviders(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	conversation := func(id string) pi.ProductConversation {
		t.Helper()
		state, err := w.client.GetConversation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	assertExecution := func(id string, execution *pi.ProductExecution, state string) {
		t.Helper()
		receipt, err := w.client.Operation(ctx, execution.OperationID)
		if err != nil || receipt.Target.ConversationID != id || receipt.OperationID != execution.OperationID ||
			receipt.Execution == nil || receipt.Execution.ExecutionID != execution.ExecutionID || receipt.Execution.State != state {
			t.Fatalf("receipt for conversation %q and execution %q: %+v, %v", id, execution.ExecutionID, receipt, err)
		}
	}

	w.wait(func() bool { return w.app.settingsOpen && w.app.settingsTab == settingsTabConnection })
	if initial := providers(); initial.SavedRevision != nil || initial.EffectiveRevision != nil || len(initial.Accounts) != 0 {
		t.Fatalf("fresh profile already has providers: %+v", initial)
	}
	w.click(msg.SettingsBackAccessible())
	var first, second *contact
	w.pump.do(func() { first = w.app.selectedContact() })
	firstID, firstChat := first.ID, first.SessionID
	enter(msg.ComposerLabel(first.Name), firstInput)
	w.click(msg.AvatarAddContact())
	w.click(msg.CreateBotAction())
	w.wait(func() bool { return len(w.app.contacts) == 2 && w.app.selectedID() != firstID })
	var profileSaved bool
	w.frame(func() {
		second = w.app.selectedContact()
		w.app.openContactProfile(second.ID)
		profile := contactProfileDraft(second)
		profile.Workspace = explicitFolder
		profileSaved = w.app.saveContactProfileDraftFor(second.ID, profile, false)
		w.app.closeDetail()
	})
	if !profileSaved || second.SessionID == firstChat {
		t.Fatal("could not establish an independent Bot with an explicit workspace before provider setup")
	}
	secondID, secondChat := second.ID, second.SessionID
	const setupDraft = "Draft prepared before the first provider Save"
	enter(msg.ComposerLabel(second.Name), setupDraft)
	w.frame(func() { w.app.openSettingsTab(settingsTabConnection, settingsSourceConnection) })

	w.click(msg.ProviderCustom())
	enter(msg.FieldName(), "Primary gateway")
	enter(msg.ProviderBaseURL(), firstEndpoint)
	enter(msg.ConnectionFieldAPIKey(), key)
	enter(msg.ProviderDefaultModel(), model)

	w.click(msg.ProviderTestAccount(msg.ProviderCustom()))
	w.wait(func() bool {
		return !w.app.providerBusy() && w.app.providers.first != nil && w.app.providers.first.tested
	})
	if checked := providers(); checked.SavedRevision != nil || checked.EffectiveRevision != nil || modelsProbes.Load() != 1 || primary.count() != 0 {
		t.Fatalf("Test & sync should probe /models without saving or contacting chat completions: %+v probes=%d chats=%d", checked, modelsProbes.Load(), primary.count())
	}

	credentials := connectionConfigPath(guiStorePath())
	if _, err := os.Lstat(credentials); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first Save must begin without credentials: %v", err)
	}
	enter(msg.FieldName(), "Primary "+key)
	w.click(msg.ProviderSave())
	w.wait(func() bool { return !w.app.providerBusy() && w.view.HasText(msg.ProviderSaveFailed()) })
	w.pump.do(func() {
		ed := w.app.providers.first
		if ed == nil || ed.Name != "Primary "+key || ed.BaseURL != firstEndpoint || ed.APIKey != key || ed.DefaultModel != model {
			t.Errorf("first Save refusal lost editable provider values: %+v", ed)
		}
	})
	if _, err := os.Lstat(credentials); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first Save refusal unexpectedly wrote credentials: %v", err)
	}
	if refused := providers(); refused.SavedRevision != nil || refused.EffectiveRevision != nil || len(refused.Accounts) != 0 || primary.count() != 0 {
		t.Fatalf("first Save refusal changed revisions or contacted the provider: %+v", refused)
	}
	enter(msg.FieldName(), "Primary gateway")

	completion := make(chan func(), 8)
	var previousHook func(func())
	w.pump.do(func() {
		previousHook = w.app.getUIHook()
		w.app.setUIHook(func(fn func()) { completion <- fn })
	})
	w.click(msg.ProviderSave())
	held1 := awaitFolderChoice(t, completion)
	held2 := awaitFolderChoice(t, completion)
	w.pump.do(func() { w.app.setUIHook(previousHook) })
	if status := providers(); status.SavedRevision == nil || status.EffectiveRevision != nil || len(status.Accounts) != 1 {
		t.Fatalf("the explicit retry did not save before UI completion: %+v", status)
	}
	var editor *contactProfileEditor
	var completionPending bool
	w.pump.do(func() {
		completionPending = w.app.providerBusy() && len(w.app.productHost().productProviders.Accounts) == 0
		editor = w.app.profileEditorFor(secondID)
		editor.Draft.Name = "Uncommitted profile name"
	})
	if !completionPending {
		t.Fatal("the successful Save completion was not held at the UI boundary")
	}
	w.click(msg.SettingsBackAccessible())
	enter(msg.ComposerLabel(second.Name), lossInput)
	uiPath := productUIPath(guiStorePath())
	if err := os.Remove(uiPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(uiPath, 0700); err != nil {
		t.Fatal(err)
	}
	w.click(msg.AvatarContactLabel(first.Name))
	var beforeError string
	w.pump.do(func() {
		if w.view.HasText(msg.RecoveryLocalSaveFailed()) {
			beforeError = w.app.localSaveErr
		}
	})
	if beforeError == "" {
		t.Fatal("the intervening Bot selection did not report the local Save failure")
	}
	w.frame(func() {
		held1()
		held2()
	})
	var retained bool
	w.pump.do(func() {
		retained = w.app.selectedContact() == first && w.app.contactByID(secondID) == second &&
			first.Draft == firstInput && second.Draft == lossInput && w.app.localSaveErr == beforeError &&
			editor.Draft.Name == "Uncommitted profile name" && second.Name != editor.Draft.Name &&
			w.app.productHost() != nil && w.app.productHost().product == initialClient && w.view.HasText(msg.RecoveryLocalSaveFailed())
	})
	if !retained {
		t.Fatal("provider Save changed the intervening Bot selection, drafts, diagnostic, or unsaved profile")
	}
	if err := os.Remove(uiPath); err != nil {
		t.Fatal(err)
	}
	w.click(msg.SettingsRetrySave())
	w.wait(func() bool { return w.app.localSaveErr == "" && !w.view.HasText(msg.RecoveryLocalSaveFailed()) })
	w.frame(func() { w.app.openSettingsTab(settingsTabConnection, settingsSourceConnection) })
	w.wait(func() bool { return !w.app.providerBusy() && w.app.productProvidersPending() })
	saved := providers()
	if saved.SavedRevision == nil || saved.EffectiveRevision != nil || len(saved.Accounts) != 1 ||
		saved.Accounts[0].BaseURL != firstEndpoint || saved.Accounts[0].DefaultModel != model ||
		saved.Accounts[0].CredentialStatus != "ok" || primary.count() != 0 {
		t.Fatalf("Save should persist the entered provider without applying: %+v", saved)
	}
	primaryID := saved.Accounts[0].ProviderID
	w.click(msg.ProviderApplyChanges())
	w.wait(func() bool { return !w.app.providerBusy() && !w.app.productProvidersPending() })
	if applied := providers(); !reflect.DeepEqual(saved.SavedRevision, applied.EffectiveRevision) || primary.count() != 0 {
		t.Fatalf("Apply should activate the saved revision without a request: %+v", applied)
	}
	assertWorkspaces := func() {
		t.Helper()
		w.pump.do(func() {
			inherited, explicit := w.app.contactByID(firstID), w.app.contactByID(secondID)
			if inherited == nil || explicit == nil || inherited.SessionID != firstChat || explicit.SessionID != secondChat ||
				inherited.Workspace != "" || explicit.Workspace != explicitFolder || w.app.effectiveContactWorkspace(inherited) != root ||
				w.app.effectiveContactWorkspace(explicit) != explicitFolder {
				t.Error("the applied provider replaced a Bot workspace override or changed inherited workspace and conversation identity")
			}
		})
	}
	assertWorkspaces()
	for id, want := range map[string]string{firstChat: firstInput, secondChat: lossInput} {
		if state := conversation(id); state.Draft == nil || state.Draft.Text != want || state.LatestExecution != nil {
			t.Fatalf("first provider Apply changed a preconfiguration draft or admitted work: %+v", state)
		}
	}
	w.click(msg.SettingsBackAccessible())
	install(primary, firstReply, nil)
	w.click(msg.SendAction())
	w.wait(func() bool { return first.SendState == sendIdle && w.view.HasText(firstReply) })

	w.frame(func() { w.app.openSettingsTab(settingsTabConnection, settingsSourceConnection) })
	w.click(msg.ProviderAddShort())
	w.wait(func() bool { return w.app.providers.adding && w.app.providers.editor != nil })
	w.frame(func() {
		ed := w.app.providers.editor
		ed.Name = "Replacement gateway"
		ed.WireAPI = pi.OpenAICompletions
		ed.BaseURL = secondEndpoint
		ed.APIKey = key
		ed.DefaultModel = model
	})
	w.click(msg.ProviderSave())
	w.wait(func() bool {
		return !w.app.providerBusy() && !w.app.providers.adding && w.app.productProvidersPending() && len(w.app.productHost().productProviders.Accounts) == 2
	})
	replacementID := providers().Accounts[1].ProviderID
	w.click(msg.ProviderMoveUp())
	w.wait(func() bool {
		return !w.app.providerBusy() && w.app.productHost().productProviders.Accounts[0].ProviderID == replacementID
	})
	pending := providers()
	if pending.Accounts[0].ProviderID != replacementID || pending.Accounts[1].ProviderID != primaryID || !reflect.DeepEqual(pending.EffectiveRevision, saved.SavedRevision) {
		t.Fatalf("saving/reordering the replacement provider changed the active revision before Apply: %+v", pending)
	}
	w.click(msg.ProviderApplyChanges())
	w.wait(func() bool { return !w.app.providerBusy() && !w.app.productProvidersPending() })
	applied := providers()
	if !reflect.DeepEqual(applied.EffectiveRevision, pending.SavedRevision) || applied.ResolvedRoles.Dialogue == nil || applied.ResolvedRoles.Dialogue.ProviderID != replacementID {
		t.Fatalf("the replacement provider did not become effective: %+v", applied)
	}
	w.click(msg.SettingsBackAccessible())

	install(primary, stopDelta, nil)
	enter(msg.ComposerLabel(first.Name), stopInput)
	w.click(msg.SendAction())
	await(streamStarted, "the sticky primary conversation stream")
	w.wait(func() bool {
		if first.ActiveTaskID == "" || !w.view.HasText(msg.BotTyping()) || !w.view.HasText(msg.StopConversation()) {
			return false
		}
		for _, m := range first.Messages {
			if m.TaskID == first.ActiveTaskID && m.Kind == msgContact && strings.Contains(m.Text, stopDelta) {
				return true
			}
		}
		return false
	})
	stopped := conversation(firstChat).LatestExecution
	if stopped == nil || stopped.State != "running" || stopped.OperationID == "" {
		t.Fatalf("could not identify the captured conversation stream: %+v", stopped)
	}
	w.click(msg.StopConversation())
	select {
	case cancelled := <-streamCancelled:
		if !cancelled {
			t.Fatal("composer Stop did not cancel the provider stream")
		}
	case <-ctx.Done():
		t.Fatal("composer Stop did not close the provider request")
	}
	w.wait(func() bool {
		return first.ActiveTaskID == "" && first.SessionState == "interrupted" && w.app.contactHistoryInFlight == 0 && w.view.HasText(stopDelta)
	})
	assertExecution(firstChat, stopped, "interrupted")

	w.click(msg.AvatarContactLabel(second.Name))
	w.wait(func() bool {
		return w.app.selectedID() == secondID && second.Draft == lossInput && w.app.contactHistoryInFlight == 0
	})
	install(secondary, "Unexpected completed response", lossArrived)
	w.click(msg.SendAction())
	await(lossArrived, "the second Bot send on replacement provider")
	w.wait(func() bool { return second.ActiveTaskID != "" && w.app.submissionInFlight == 0 && second.Draft == "" })
	lost := conversation(secondChat).LatestExecution
	if lost == nil || lost.State != "running" || lost.OperationID == "" {
		t.Fatalf("could not identify the second Bot stream: %+v", lost)
	}
	enter(msg.ComposerLabel(second.Name), nextDraft)
	beforeLoss := conversation(secondChat)
	if beforeLoss.Draft == nil || beforeLoss.Draft.Text != nextDraft {
		t.Fatalf("the new draft was not saved during the held send: %+v", beforeLoss.Draft)
	}
	outcome, err := initialClient.Kill(ctx)
	if !outcome.Reaped || outcome.PID <= 0 || outcome.ExitCode == 0 || outcome.Err == nil || !errors.Is(err, outcome.Err) {
		t.Fatalf("terminate the owned product worker: %+v, %v", outcome, err)
	}
	w.wait(func() bool {
		return w.app.runtimeStatus == runtimeStateExited && second.SendState == sendUnknown && second.Draft == nextDraft &&
			w.view.HasText(msg.SendDisconnectedState())
	})

	w = openProductWorkbench(t)
	assertRecovered := func() {
		t.Helper()
		w.wait(func() bool {
			current := w.app.contactByID(secondID)
			prior := w.app.contactByID(firstID)
			return current != nil && current.SessionID == secondChat && current.SendState == sendUnknown && current.Draft == nextDraft &&
				prior != nil && prior.SessionID == firstChat && prior.SessionState == "interrupted" && w.app.selectedID() == secondID
		})
		assertExecution(firstChat, stopped, "interrupted")
		assertExecution(secondChat, lost, "unknown")
		if resumed := conversation(secondChat); !reflect.DeepEqual(resumed.Draft, beforeLoss.Draft) ||
			resumed.LatestExecution == nil || resumed.LatestExecution.ExecutionID != lost.ExecutionID || resumed.LatestExecution.State != "unknown" {
			t.Fatalf("reopen did not retain the exact unknown execution and newer draft: %+v", resumed)
		}
		history, err := w.client.History(ctx, pi.ProductHistoryParams{ProductConversationParams: pi.ProductConversationParams{ConversationID: secondChat}})
		if err != nil || len(history.Items) != 1 || history.Items[0].Role != "user" || history.Items[0].Text != lossInput {
			t.Fatalf("reopen changed the interrupted Bot's recorded prompt: %+v, %v", history, err)
		}
	}
	assertRecovered()
	w.wait(func() bool { return w.view.HasText(msg.SendUnknownState()) && w.app.contactHistoryInFlight == 0 })
	var reloadErr error
	var reloadGeneration int
	w.frame(func() {
		current := w.app.selectedContact()
		reloadGeneration = current.historyGen
		reloadErr = focusPiControl(w.view, msg.ReloadAction(), false)
		if reloadErr == nil {
			w.view.Key(0, ui.KeySpace)
		}
	})
	if reloadErr != nil {
		t.Fatal(reloadErr)
	}
	w.wait(func() bool {
		current := w.app.selectedContact()
		return current.historyGen > reloadGeneration && w.app.contactHistoryInFlight == 0 && current.LoadErr == "" && current.Loaded
	})
	assertRecovered()
	if primary.count() != 2 || secondary.count() != 1 {
		t.Fatal("keyboard Reload contacted the provider")
	}
	t.Log("First Plan A provider Save refusal preserved edits and empty revisions. Sticky primary Stop, replacement routing, worker loss recovery, and keyboard Reload completed cleanly without replay.")
	w.shutdown(ctx)
	w = openProductWorkbench(t)
	assertRecovered()
	assertWorkspaces()
	w.shutdown(ctx)

	assertProvider := func(p *piAppProvider, prompts ...string) {
		t.Helper()
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.requests != len(prompts) || len(p.problems) != 0 {
			t.Fatalf("provider budget or routing mismatch: requests=%d want=%d problems=%v", p.requests, len(prompts), p.problems)
		}
		for i, prompt := range prompts {
			if !bytes.Contains(p.bodies[i], []byte(prompt)) {
				t.Fatalf("provider request %d did not carry %q", i+1, prompt)
			}
		}
	}
	assertProvider(primary, firstInput, stopInput)
	assertProvider(secondary, lossInput)
}
