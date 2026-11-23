//go:build integration

package app

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
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
	t.Setenv("NAGI_GUI_WORKDIR", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	primary := &piAppProvider{limit: 3}
	secondary := &piAppProvider{limit: 1}
	streamStarted, lossArrived := make(chan struct{}), make(chan struct{})
	streamCancelled := make(chan bool, 1)
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if primary.count() != 2 {
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
	settingsAction := func(label string) {
		t.Helper()
		var err error
		w.frame(func() {
			w.view.Scroll(520, 360, 0, -3000)
			w.view.Frame()
			err = clickSettingsAction(w.view, label)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
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
	connection := func() pi.ProductConnectionStatus {
		t.Helper()
		status, err := w.client.Connection(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return status
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
	if initial := connection(); initial.SavedRevision != nil || initial.EffectiveRevision != nil {
		t.Fatalf("fresh profile already has connection settings: %+v", initial)
	}
	w.click(msg.SettingsBackAccessible())
	var first, second *contact
	w.pump.do(func() { first = w.app.selectedContact() })
	firstID, firstChat := first.ID, first.SessionID
	enter(msg.ComposerLabel(first.Name), firstInput)
	w.click(msg.AvatarAddContact())
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
		t.Fatal("could not establish an independent Bot with an explicit workspace before connection setup")
	}
	secondID, secondChat := second.ID, second.SessionID
	const setupDraft = "Draft prepared before the first connection Save"
	enter(msg.ComposerLabel(second.Name), setupDraft)
	w.frame(func() { w.app.openSettingsTab(settingsTabConnection, settingsSourceConnection) })
	profileBeforeSetup := recoverySavedBytes(t, guiStorePath())
	w.frame(func() {
		w.app.chooseFolder = func(mygo.OpenDialogOptions) ([]string, error) { return []string{root}, nil }
	})
	enter(msg.ConnectionFieldEndpoint(), firstEndpoint)
	enter(msg.ConnectionFieldAPIKey(), key)
	enter(msg.ConnectionFieldModel(), model)
	settingsAction(msg.SettingsShowRuntimeDetails())
	settingsAction("1")
	credentials := connectionConfigPath(guiStorePath())
	for _, label := range []string{msg.ConnectionTestAction(), msg.ConnectionSaveConfiguration(), msg.ConnectionSaveAndApply()} {
		settingsAction(label)
		var blocked bool
		w.pump.do(func() {
			form := w.app.connectionForm
			blocked = !form.Busy && form.Draft.Slots == "1" && form.Errors["WorkingDirectory"] != "" && w.view.HasText(msg.ValidationWorkspaceAbsolutePath())
		})
		if !blocked {
			t.Fatalf("%q did not require an explicit working folder", label)
		}
	}
	if status := connection(); status.SavedRevision != nil || status.EffectiveRevision != nil || primary.count() != 0 || !bytes.Equal(profileBeforeSetup, recoverySavedBytes(t, guiStorePath())) {
		t.Fatalf("unchosen folder changed the Product profile or contacted the provider: %+v, requests=%d", status, primary.count())
	}
	if _, err := os.Lstat(credentials); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unchosen folder created a credential file: %v", err)
	}
	settingsAction(msg.WorkFolderChoose())
	w.wait(func() bool { return !w.app.connectionForm.Busy && w.app.connectionForm.Draft.WorkingDirectory == root })
	install(primary, "OK", nil)
	settingsAction(msg.ConnectionTestAction())
	w.wait(func() bool { return w.app.connectionForm.Status == connStatusTestCompleted })
	if checked := connection(); checked.SavedRevision != nil || checked.EffectiveRevision != nil || primary.count() != 1 {
		t.Fatalf("Test should make one request without saving or applying: %+v, requests=%d", checked, primary.count())
	}
	var entered connectionDraft
	w.pump.do(func() { entered = w.app.connectionForm.Draft })
	if _, err := os.Lstat(credentials); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first Save must begin without credentials: %v", err)
	}
	if err := os.Mkdir(credentials, 0700); err != nil {
		t.Fatal(err)
	}
	settingsAction(msg.ConnectionSaveConfiguration())
	w.wait(func() bool { return !w.app.connectionForm.Busy && w.view.HasText(msg.RecoveryConnectionSaveFailed()) })
	w.pump.do(func() {
		if form := w.app.connectionForm; form.Draft != entered || !form.Dirty() || form.HasSaved {
			t.Error("first Save refusal changed the editable values or reported a saved connection")
		}
	})
	if refused := connection(); refused.SavedRevision != nil || refused.EffectiveRevision != nil || primary.count() != 1 {
		t.Fatalf("first Save refusal changed a connection revision or contacted the provider: %+v", refused)
	}
	if err := os.Remove(credentials); err != nil {
		t.Fatal(err)
	}
	completion := make(chan func(), 1)
	var previousHook func(func())
	w.pump.do(func() {
		previousHook = w.app.getUIHook()
		w.app.setUIHook(func(fn func()) { completion <- fn })
	})
	settingsAction(msg.ConnectionSaveConfiguration())
	finishSave := awaitFolderChoice(t, completion)
	w.pump.do(func() { w.app.setUIHook(previousHook) })
	if status := connection(); status.SavedRevision == nil || status.EffectiveRevision != nil {
		t.Fatalf("the explicit retry did not save before UI completion: %+v", status)
	}
	var editor *contactProfileEditor
	var completionPending bool
	w.pump.do(func() {
		completionPending = w.app.connectionSaveInFlight && w.app.connectionSaved == nil
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
	w.frame(finishSave)
	var retained bool
	w.pump.do(func() {
		retained = w.app.selectedContact() == first && w.app.contactByID(secondID) == second &&
			first.Draft == firstInput && second.Draft == lossInput && w.app.localSaveErr == beforeError &&
			editor.Draft.Name == "Uncommitted profile name" && second.Name != editor.Draft.Name &&
			w.app.productHost() != nil && w.app.productHost().product == initialClient && w.view.HasText(msg.RecoveryLocalSaveFailed())
	})
	if !retained {
		t.Fatal("connection Save changed the intervening Bot selection, drafts, diagnostic, or unsaved profile")
	}
	if err := os.Remove(uiPath); err != nil {
		t.Fatal(err)
	}
	w.click(msg.SettingsRetrySave())
	w.wait(func() bool { return w.app.localSaveErr == "" && !w.view.HasText(msg.RecoveryLocalSaveFailed()) })
	w.frame(func() { w.app.openSettingsTab(settingsTabConnection, settingsSourceConnection) })
	w.wait(func() bool { return !w.app.connectionForm.Busy && w.app.connectionForm.SavedPending })
	saved := connection()
	if saved.SavedRevision == nil || saved.EffectiveRevision != nil || saved.Saved == nil || saved.Saved.BaseURL != firstEndpoint ||
		saved.Saved.Model != model || saved.Saved.WorkingDirectory != root || saved.Saved.MaxActiveTasks != 1 || !saved.Saved.CredentialPresent || primary.count() != 1 {
		t.Fatalf("Save should persist the entered settings without applying: %+v, requests=%d", saved, primary.count())
	}
	settingsAction(msg.ConnectionApplySaved())
	w.wait(func() bool { return w.app.connectionForm.Status == connStatusApplied })
	if applied := connection(); !reflect.DeepEqual(saved.SavedRevision, applied.EffectiveRevision) || primary.count() != 1 {
		t.Fatalf("Apply should activate the saved revision without a request: %+v", applied)
	}
	assertWorkspaces := func() {
		t.Helper()
		w.pump.do(func() {
			inherited, explicit := w.app.contactByID(firstID), w.app.contactByID(secondID)
			if inherited == nil || explicit == nil || inherited.SessionID != firstChat || explicit.SessionID != secondChat ||
				inherited.Workspace != "" || explicit.Workspace != explicitFolder || w.app.effectiveContactWorkspace(inherited) != root ||
				w.app.effectiveContactWorkspace(explicit) != explicitFolder {
				t.Error("the applied default replaced a Bot workspace override or changed inherited workspace and conversation identity")
			}
		})
	}
	assertWorkspaces()
	for id, want := range map[string]string{firstChat: firstInput, secondChat: lossInput} {
		if state := conversation(id); state.Draft == nil || state.Draft.Text != want || state.LatestExecution != nil {
			t.Fatalf("first connection Apply changed a preconfiguration draft or admitted work: %+v", state)
		}
	}
	w.click(msg.SettingsBackAccessible())
	install(primary, firstReply, nil)
	w.click(msg.SendAction())
	w.wait(func() bool { return first.SendState == sendIdle && w.view.HasText(firstReply) })

	w.frame(func() { w.app.openSettingsTab(settingsTabConnection, settingsSourceConnection) })
	var endpointOpen bool
	w.pump.do(func() { endpointOpen = w.app.connectionForm.EditOpen["Endpoint"] })
	if !endpointOpen {
		settingsAction(msg.ConnectionEditField(msg.ConnectionFieldEndpoint()))
	}
	enter(msg.ConnectionFieldEndpoint(), secondEndpoint)
	settingsAction(msg.ConnectionSaveConfiguration())
	w.wait(func() bool { return !w.app.connectionForm.Busy && w.app.connectionForm.SavedPending })
	pending := connection()
	if pending.Saved == nil || pending.Saved.BaseURL != secondEndpoint || !reflect.DeepEqual(pending.EffectiveRevision, saved.SavedRevision) {
		t.Fatalf("saving the second endpoint changed the active connection: %+v", pending)
	}
	settingsAction(msg.ConnectionApplySaved())
	w.wait(func() bool { return w.app.connectionForm.Status == connStatusApplied })
	applied := connection()
	if applied.Effective == nil || applied.Effective.BaseURL != secondEndpoint || !reflect.DeepEqual(applied.EffectiveRevision, pending.SavedRevision) {
		t.Fatalf("the second endpoint did not become effective: %+v", applied)
	}
	w.click(msg.SettingsBackAccessible())
	install(primary, stopDelta, nil)
	enter(msg.ComposerLabel(first.Name), stopInput)
	w.click(msg.SendAction())
	await(streamStarted, "the captured connection stream")
	w.wait(func() bool { return first.ActiveTaskID != "" && w.view.HasText(stopDelta) })
	stopped := conversation(firstChat).LatestExecution
	if stopped == nil || stopped.State != "running" || stopped.OperationID == "" {
		t.Fatalf("could not identify the captured conversation stream: %+v", stopped)
	}
	w.click(msg.AvatarContactLabel(second.Name))
	w.wait(func() bool { return w.app.selectedID() == secondID && second.Loaded })
	blocked := conversation(secondChat)
	if blocked.Draft == nil || blocked.Draft.Text != lossInput || blocked.LatestExecution != nil {
		t.Fatalf("the second Bot did not retain its unsubmitted draft: %+v", blocked)
	}
	_, err := w.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: secondChat},
		OperationID:               "second-bot-capacity-refusal", DraftRevision: blocked.Draft.Revision,
	})
	var busy *pi.RemoteError
	if !errors.As(err, &busy) || busy.Code != "busy" || !reflect.DeepEqual(conversation(secondChat), blocked) || primary.count() != 3 || secondary.count() != 0 {
		t.Fatalf("the full slot did not refuse the second Bot without submitting: %v", err)
	}
	w.click(msg.AvatarContactLabel(first.Name))
	w.wait(func() bool { return w.app.selectedID() == firstID && w.view.HasText(msg.StopConversation()) })
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
		return first.ActiveTaskID == "" && first.SessionState == "interrupted" && w.app.contactHistoryInFlight == 0
	})
	assertExecution(firstChat, stopped, "interrupted")

	w.click(msg.AvatarContactLabel(second.Name))
	w.wait(func() bool {
		return w.app.selectedID() == secondID && second.Draft == lossInput && w.app.contactHistoryInFlight == 0
	})
	install(secondary, "Unexpected completed response", lossArrived)
	w.click(msg.SendAction())
	await(lossArrived, "the second Bot send")
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
	w.frame(func() { w.app.openSettingsTab(settingsTabConnection, settingsSourceConnection) })
	settingsAction(msg.ConnectionApplySaved())
	w.wait(func() bool {
		host := w.app.productHost()
		if host != nil && host.product != initialClient {
			w.client = host.product
		}
		return w.client != initialClient && w.app.connectionForm.Status == connStatusApplied
	})
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
	w.click(msg.SettingsBackAccessible())
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
	if primary.count() != 3 || secondary.count() != 1 {
		t.Fatal("keyboard Reload contacted the provider")
	}
	t.Log("First Save refusal preserved the fresh draft and empty revisions. The second Bot sent after capacity cleared, and keyboard Reload read history without a provider request.")
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
	assertProvider(primary, "Reply with OK.", firstInput, stopInput)
	assertProvider(secondary, lossInput)
}
