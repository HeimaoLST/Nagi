package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

type piSelection struct {
	CWD          string   `json:"cwd"`
	Instructions string   `json:"instructions"`
	CodingTools  []string `json:"coding_tools"`
}

type piConfiguration struct {
	WireAPI          pi.WireAPI `json:"wire_api"`
	BaseURL          string     `json:"base_url"`
	Model            string     `json:"model"`
	ContextTokens    int        `json:"context_tokens"`
	OutputTokens     int        `json:"output_tokens"`
	CredentialSource string     `json:"credential_source"`
}

type piConversationBinding struct {
	Engine        string            `json:"engine"`
	Context       pi.ContextBinding `json:"context"`
	Selection     piSelection       `json:"selection"`
	Configuration piConfiguration   `json:"configuration"`
}

type piActivityBinding struct {
	Engine        string            `json:"engine"`
	Context       pi.ContextBinding `json:"context"`
	Selection     piSelection       `json:"selection"`
	Configuration piConfiguration   `json:"configuration"`
}

type piActivityRun struct {
	WorkerEpoch     string `json:"worker_epoch"`
	ContextID       string `json:"context_id"`
	SessionRef      string `json:"session_ref"`
	RunID           string `json:"run_id"`
	CancelRequested bool   `json:"cancel_requested,omitempty"`
	Admitted        bool   `json:"admitted"`
}

type piQuestionCall struct {
	WorkerEpoch string `json:"worker_epoch"`
	ContextID   string `json:"context_id"`
	RunID       string `json:"run_id"`
	CallID      string `json:"call_id"`
	OperationID string `json:"operation_id"`
	ExecutionID string `json:"execution_id"`
	ContactID   string `json:"contact_id"`
	IssueID     string `json:"issue_id"`
	SessionRef  string `json:"session_ref"`
}

func clonePiActivityBinding(binding *piActivityBinding) *piActivityBinding {
	if binding == nil {
		return nil
	}
	copy := *binding
	if binding.Selection.CodingTools != nil {
		copy.Selection.CodingTools = append([]string{}, binding.Selection.CodingTools...)
	}
	return &copy
}

func clonePiActivityRun(run *piActivityRun) *piActivityRun {
	if run == nil {
		return nil
	}
	copy := *run
	return &copy
}

func samePiActivityRun(left, right *piActivityRun) bool {
	return left != nil && right != nil && left.WorkerEpoch == right.WorkerEpoch && left.ContextID == right.ContextID && left.SessionRef == right.SessionRef && left.RunID == right.RunID
}

func clonePiQuestionCall(call *piQuestionCall) *piQuestionCall {
	if call == nil {
		return nil
	}
	copy := *call
	return &copy
}

func isPiActivityIssue(issue *productIssue) bool {
	return issue != nil && issue.PiBinding != nil && issue.PiBinding.Engine == "pi" &&
		issue.PiBinding.Context.Purpose == pi.Activity && issue.PiBinding.Context.BotID == issue.ContactID &&
		issue.PiBinding.Context.ActivityID == issue.ID && issue.PiBinding.Context.ContextID != "" &&
		issue.PiBinding.Context.SessionRef != "" && issue.WorkSessionID == issue.PiBinding.Context.SessionRef
}

type piAppHost struct {
	client         *pi.Client
	product        *pi.ProductClient
	productBots    map[string]pi.ProductBot
	productDrafts  map[string]pi.ProductDraft
	productConn    pi.ProductConnectionStatus
	productEvents  chan struct{}
	productPending map[string]productPendingSend
	generation     int
	mu             sync.Mutex
	bridges        map[string]*piIssueTools
	connectionPath string
	contexts       map[string]pi.ContextBinding
	openMu         sync.Mutex
	outcome        pi.ProcessOutcome
	exitApplied    chan struct{}
	defaults       piConfiguration

	activityRevision uint64
	recoveryID       string
}

func clonePiConversationBinding(binding *piConversationBinding) *piConversationBinding {
	if binding == nil {
		return nil
	}
	cloned := *binding
	if binding.Selection.CodingTools != nil {
		cloned.Selection.CodingTools = append([]string{}, binding.Selection.CodingTools...)
	}
	return &cloned
}

func (ct *contact) piPreviousBindings() []piConversationBinding {
	if ct.PreviousPiBindings == nil {
		return nil
	}
	bindings := make([]piConversationBinding, 0, len(ct.PreviousPiBindings))
	for i := range ct.PreviousPiBindings {
		bindings = append(bindings, *clonePiConversationBinding(&ct.PreviousPiBindings[i]))
	}
	return bindings
}

func piPayloadPaths() (string, string, error) {
	root := os.Getenv("NAGI_PI_PAYLOAD")
	if root == "" {
		executable, err := os.Executable()
		if err != nil {
			return "", "", err
		}
		root = filepath.Join(filepath.Dir(executable), "..", "Resources", "pi")
	}
	if !filepath.IsAbs(root) {
		return "", "", errors.New("Pi payload directory must be absolute")
	}
	return filepath.Join(root, "bin", "node"), filepath.Join(root, "dist", "worker.js"), nil
}

func (a *appModel) productHost() *piAppHost {
	if a.pi != nil && a.pi.product != nil {
		return a.pi
	}
	return nil
}

func (a *appModel) productProfileReady() bool {
	host := a.productHost()
	if host == nil || a.shutdownStarted {
		return false
	}
	select {
	case <-host.product.Done():
		return false
	default:
		return true
	}
}

func (a *appModel) productActivityClient() *pi.ProductClient {
	if !a.productProfileReady() {
		return nil
	}
	return a.productHost().product
}

func (a *appModel) productActivityRevision() uint64 {
	if host := a.productHost(); host != nil {
		return uint64(host.generation)<<32 | host.activityRevision
	}
	return 0
}

func (a *appModel) openProductProfile(ctx context.Context, fresh bool, generation int) error {
	node, worker, err := piPayloadPaths()
	if err != nil {
		return err
	}
	host := &piAppHost{
		generation: generation, productBots: make(map[string]pi.ProductBot),
		productDrafts: make(map[string]pi.ProductDraft), productEvents: make(chan struct{}),
	}
	local, err := readProductUI(a.storePath)
	if err != nil {
		return err
	}
	host.product, err = pi.StartProduct(pi.ProductOptions{
		NodePath: node, WorkerPath: worker, StateDir: filepath.Join(filepath.Dir(a.storePath), "pi"),
		ProfilePath: a.storePath, CredentialsPath: a.connectionPath, FreshProfile: fresh,
		Handlers: pi.ProductHandlers{
			OnEvent: func(event pi.ProductEvent) {
				select {
				case <-host.productEvents:
					a.updateUI(func() { a.applyProductEvent(host, event) })
				default:
				}
			},
			OnStreamGap: func() {
				a.updateUI(func() { a.reconcileProductStream(host) })
			},
		},
	})
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if !installed {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = host.product.Kill(cleanup)
		}
	}()
	ready, err := host.product.Ready(ctx)
	if err != nil {
		return err
	}
	if ready.Runtime.Version != "v22.23.5" {
		return fmt.Errorf("Pi worker requires Node 22.23.5; got %s", ready.Runtime.Version)
	}
	opened, err := host.product.Open(ctx, []int{pi.ProductVersion})
	if err != nil {
		return err
	}
	bots := opened.Snapshot.Bots
	if recovery := opened.Snapshot.Recovery; recovery != nil {
		host.recoveryID = recovery.ID
		if local == nil || local.RecoveryID != recovery.ID {
			local = nil
			if len(recovery.UI) != 0 {
				local = &productUIState{}
				if err := json.Unmarshal(recovery.UI, local); err != nil {
					return err
				}
			}
		}
	}
	host.productPending = make(map[string]productPendingSend)
	if local != nil && local.Pending != nil {
		host.productPending = local.Pending
	}
	for cursor := opened.Snapshot.NextBotCursor; cursor != nil; {
		page, err := host.product.ListBots(ctx, pi.ProductPageParams{Cursor: *cursor})
		if err != nil {
			return err
		}
		bots = append(bots, page.Items...)
		cursor = page.NextCursor
	}
	if len(bots) == 0 {
		bot, err := host.product.CreateBot(ctx, pi.ProductBotCreateParams{
			OperationID: "initial-bot", Profile: pi.ProductBotProfile{Name: "Nagi", Role: "Assistant"},
		})
		if err != nil {
			return err
		}
		bots = append(bots, bot)
	}
	contacts := make([]*contact, 0, len(bots))
	for _, bot := range bots {
		conversation, err := host.product.GetConversation(ctx, bot.CurrentConversationID)
		if err != nil {
			return err
		}
		ct := productContact(bot)
		host.productBots[ct.ID] = bot
		if draft := conversation.Draft; draft != nil {
			host.productDrafts[ct.ID] = *draft
			ct.Draft = draft.Text
			ct.Context.items = productTaskContext(draft.Materials)
		}
		applyProductExecution(ct, conversation.LatestExecution)
		if pending, ok := host.productPending[ct.ID]; ok && pending.ConversationID == ct.SessionID {
			receipt, lookupErr := host.product.Operation(ctx, pending.OperationID)
			if lookupErr == nil && receipt.Target.ConversationID == ct.SessionID && receipt.State != "pending" && receipt.State != "unknown" {
				delete(host.productPending, ct.ID)
			} else if ct.SendState == sendIdle {
				ct.SendState, ct.Status = sendUnknown, statusAttention
				ct.setSendError(taskContextUncertainSendError, "send-uncertain", "")
			}
		}
		contacts = append(contacts, ct)
	}
	selectedID := ""
	if opened.Snapshot.Preferences != nil {
		selectedID = opened.Snapshot.Preferences.SelectedBotID
	}
	if local != nil && local.Selected != "" {
		selectedID = local.Selected
	}
	selected := contacts[0]
	for _, ct := range contacts {
		if ct.ID == selectedID {
			selected = ct
			break
		}
	}
	if projection, err := productHistory(ctx, host.product, selected.SessionID); err == nil {
		selected.Messages, selected.History, selected.Loaded = projection.messages, projection.entries, true
	} else {
		selected.LoadErr = safeDiagnostic(err.Error())
		selected.Unavailable = selected.LoadErr
	}
	a.runOnUI(func() {
		a.pi, a.connectionGeneration, a.contacts = host, host.generation, contacts
		a.runtimeStage, a.runtimeHint, a.runtimeHintKind = "", "", ""
		a.runtimeHintArgs = nil
		a.localSaveErr, a.localSavePrevErr, a.localSavePrevKind = "", "", ""
		a.applyProductPreferences(opened.Snapshot.Preferences)
		a.selected = slices.Index(contacts, selected)
		a.activityIntents = nil
		if local != nil {
			a.activityIntents = local.PendingActivities
			a.leftPaneWidth = clampPaneWidth(local.Layout.LeftWidth, defaultLeftPaneWidth, minLeftPaneWidth, maxLeftPaneWidth)
			a.rightPaneWidth = clampPaneWidth(local.Layout.RightWidth, defaultRightPaneWidth, minRightPaneWidth, maxRightPaneWidth)
			a.appearanceThemeIDs = local.ThemeIDs
			a.appearanceMode = normalizeAppearanceMode(local.Appearance)
		}
		a.applyProductConnection(opened.Snapshot.Connection)
		a.connectionForm = newConnectionSettings(a.connectionSaved, a.workDir)
		a.connectionForm.Busy = a.connectionApplying
		a.applyBuiltinCatalog(ready)
		for _, ct := range contacts {
			ct.Context.onChange = func() error { return a.saveProductDraft(ct) }
		}
		installed = true
		close(host.productEvents)
	})
	return nil
}

func (a *appModel) persistProductUI(host *piAppHost) error {
	return writeProfileJSON(productUIPath(a.storePath), productUIState{
		Selected: a.selectedID(), Layout: storedLayout{LeftWidth: a.railWidth(), RightWidth: a.workWidth(), WorkspaceOpen: a.workspaceOpen},
		ThemeIDs: a.appearanceThemeIDs, Appearance: normalizeAppearanceMode(a.appearanceMode), Pending: host.productPending, RecoveryID: host.recoveryID,
		PendingActivities: a.activityIntents,
	})
}

func (a *appModel) saveProductDraft(ct *contact) error {
	host := a.productHost()
	if !a.productProfileReady() || ct == nil || ct.Archived {
		return errors.New("the product conversation is unavailable")
	}
	materials := make([]pi.ProductMaterial, len(ct.Context.items))
	for i, item := range ct.Context.items {
		materials[i] = pi.ProductMaterial{ID: item.ID, Name: item.Name, Source: item.Source, Text: item.Text, Revision: item.Revision}
	}
	previous := host.productDrafts[ct.ID]
	if previous.Text == ct.Draft && slices.Equal(previous.Materials, materials) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	draft, err := host.product.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: ct.SessionID},
		ProductText:               pi.ProductText{Text: ct.Draft, Materials: materials}, ExpectedRevision: previous.Revision, OperationID: newContactID(),
	})
	if err == nil {
		host.productDrafts[ct.ID] = draft
	}
	return err
}

func (a *appModel) sendProductConversation(host *piAppHost, ct *contact) bool {
	if err := a.saveProductDraft(ct); err != nil {
		ct.setSendError(a.connectionSafeMessage(err), "", "")
		return false
	}
	pending := productPendingSend{ConversationID: ct.SessionID, OperationID: newContactID()}
	host.productPending[ct.ID] = pending
	if err := a.persist(); err != nil {
		delete(host.productPending, ct.ID)
		ct.setSendError(a.connectionSafeMessage(err), "", "")
		return false
	}
	draft := host.productDrafts[ct.ID]
	ct.SubmittedDraft, ct.SubmittedRev = draft.Text, ct.draftRev
	ct.SendState = sendSubmitting
	ct.clearSendError()
	a.submissionAsyncStarted()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		receipt, err := host.product.Send(ctx, pi.ProductSendParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: pending.ConversationID},
			DraftRevision:             draft.Revision, OperationID: pending.OperationID,
		})
		var remote *pi.RemoteError
		rejected := errors.As(err, &remote)
		if err != nil && !rejected {
			lookup, done := context.WithTimeout(context.Background(), 3*time.Second)
			receipt, _ = host.product.Operation(lookup, pending.OperationID)
			done()
		}
		a.updateUI(func() {
			defer a.submissionAsyncFinished()
			if a.pi != host || a.contactByID(ct.ID) != ct || ct.SessionID != pending.ConversationID {
				return
			}
			if receipt.State == "accepted" && receipt.Execution != nil {
				if ct.SendState == sendSubmitting {
					applyProductExecution(ct, receipt.Execution)
				}
				delete(host.productPending, ct.ID)
				_ = a.persist()
				return
			}
			if rejected {
				delete(host.productPending, ct.ID)
				_ = a.persist()
				ct.SendState = sendIdle
				ct.setSendError(a.connectionSafeMessage(err), "", "")
				return
			}
			ct.SendState, ct.Status = sendUnknown, statusAttention
			ct.setSendError(taskContextUncertainSendError, "send-uncertain", "")
		})
	}()
	return true
}

func (a *appModel) stopProductConversation(host *piAppHost, ct *contact, execution string) {
	if ct == nil || execution == "" || ct.ActiveTaskID != execution {
		return
	}
	id := ct.SessionID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := host.product.StopConversation(ctx, pi.ProductConversationStopParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: id}, ExecutionID: execution, OperationID: newContactID(),
		})
		if err != nil {
			a.updateUI(func() {
				if a.pi == host && ct.SessionID == id && ct.ActiveTaskID == execution {
					ct.setSendError(a.connectionSafeMessage(err), "", "")
				}
			})
		}
	}()
}

func (a *appModel) reloadProductConversation(host *piAppHost, ct *contact) {
	if !a.canReloadPiConversation(ct) {
		return
	}
	ct.historyGen++
	generation, id := ct.historyGen, ct.SessionID
	a.historyAsyncStarted()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		projection, err := productHistory(ctx, host.product, id)
		a.updateUI(func() {
			defer a.historyAsyncFinished()
			if a.pi != host || ct.historyGen != generation || ct.SessionID != id {
				return
			}
			if err != nil {
				ct.Loaded = false
				ct.LoadErr = a.connectionSafeMessage(err)
				ct.Unavailable = ct.LoadErr
				return
			}
			ct.Messages, ct.History = projection.messages, projection.entries
			ct.Loaded, ct.LoadErr, ct.Unavailable = true, "", ""
		})
	}()
}

func productHistory(ctx context.Context, client *pi.ProductClient, id string) (historyProjection, error) {
	params := pi.ProductHistoryParams{ProductConversationParams: pi.ProductConversationParams{ConversationID: id}}
	var projection historyProjection
	for {
		page, err := client.History(ctx, params)
		if err != nil {
			return historyProjection{}, err
		}
		for _, row := range page.Items {
			kind, from := msgContact, "contact"
			if row.Role == "user" {
				kind, from = msgUser, "me"
			}
			stamp, _ := time.Parse(time.RFC3339Nano, row.Timestamp)
			when := formatMillis(stamp.UnixMilli())
			materials := productTaskContext(row.Materials)
			projection.messages = append(projection.messages, &message{Key: row.MessageID, MessageID: row.MessageID, Kind: kind, Text: row.Text, Time: when, Context: materials, History: true})
			projection.entries = append(projection.entries, historyEntry{ID: row.MessageID, Date: when, From: from, Text: row.Text, Context: materials})
		}
		if page.NextCursor == nil {
			return projection, nil
		}
		params.Cursor = *page.NextCursor
	}
}

func (a *appModel) applyProductEvent(host *piAppHost, event pi.ProductEvent) {
	if a.pi != host {
		return
	}
	var data pi.ProductExecutionEvent
	if json.Unmarshal(event.Data, &data) != nil {
		return
	}
	if data.ActivityID != "" && (event.Name == "activity.changed" || event.Name == "execution.started" || event.Name == "execution.settled") {
		host.activityRevision++
		return
	}
	if data.ConversationID == "" {
		return
	}
	for _, ct := range a.contacts {
		if ct.SessionID != data.ConversationID {
			continue
		}
		switch event.Name {
		case "execution.started":
			if ct.ActiveTaskID != data.ExecutionID && host.productPending[ct.ID].OperationID != data.OperationID {
				return
			}
			applyProductExecution(ct, &pi.ProductExecution{ExecutionID: data.ExecutionID, State: "running"})
			ct.Messages = append(ct.Messages, &message{Key: data.ExecutionID + ":user", Kind: msgUser, Text: ct.SubmittedDraft, Time: nowTime(), TaskID: data.ExecutionID, Context: productTaskContext(host.productDrafts[ct.ID].Materials)})
		case "execution.textDelta":
			if ct.ActiveTaskID == data.ExecutionID {
				item := a.liveText(ct, data.ExecutionID, "pi")
				if uint64(len(item.Text)) == data.Offset {
					item.Text += data.Text
					item.Streaming = true
				}
			}
		case "execution.settled":
			if ct.ActiveTaskID == data.ExecutionID {
				applyProductExecution(ct, &pi.ProductExecution{ExecutionID: data.ExecutionID, State: data.State, Error: data.Error})
				a.reloadProductConversation(host, ct)
			}
		case "conversation.changed":
			a.refreshProductConversation(host, ct, ct.SubmittedRev)
		}
		return
	}
}

func (a *appModel) refreshProductConversation(host *piAppHost, ct *contact, edit int) {
	id := ct.SessionID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		value, err := host.product.GetConversation(ctx, id)
		if err != nil || value.Draft == nil {
			return
		}
		a.updateUI(func() {
			if a.pi != host || ct.SessionID != id || value.Draft.Revision < host.productDrafts[ct.ID].Revision {
				return
			}
			host.productDrafts[ct.ID] = *value.Draft
			if ct.draftRev == edit {
				ct.Draft = value.Draft.Text
				ct.Context.items = productTaskContext(value.Draft.Materials)
			}
		})
	}()
}

func (a *appModel) reconcileProductStream(host *piAppHost) {
	if a.pi != host {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := host.product.Open(ctx, []int{pi.ProductVersion}); err != nil {
			return
		}
		a.updateUI(func() {
			if a.pi == host {
				host.activityRevision++
				for _, ct := range a.contacts {
					a.refreshProductConversation(host, ct, ct.draftRev)
					a.reloadProductConversation(host, ct)
				}
			}
		})
	}()
}

func (a *appModel) finishProductShutdown(host *piAppHost) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	outcome, err := host.product.Shutdown(ctx)
	drained := false
	select {
	case <-host.product.CallbacksDone():
		drained = true
	case <-ctx.Done():
		err = errors.Join(err, ctx.Err())
	}
	a.updateUI(func() {
		if a.pi != host {
			return
		}
		if err != nil || !outcome.Reaped || !drained {
			a.runtimeErrKind = runtimeNoticeRuntimeShutdown
			if err != nil {
				a.runtimeErr = a.connectionSafeMessage(err)
			}
		}
		if !outcome.Reaped || !drained {
			a.shutdownDrainStarted = false
			return
		}
		a.pi, a.quitReady = nil, true
		if a.requestQuit != nil {
			a.requestQuit()
		}
	})
}

func (a *appModel) observeProductExit(host *piAppHost) {
	<-host.product.Done()
	<-host.product.CallbacksDone()
	a.updateUI(func() {
		if a.pi != host || a.shutdownStarted {
			return
		}
		a.runtimeStatus = runtimeStateExited
		host.activityRevision++
		a.runtimeErrKind = runtimeNoticeConnectionLost
		a.clearBuiltinToolCatalog()
		for _, ct := range a.contacts {
			if ct.ActiveTaskID != "" || ct.SendState == sendSubmitting {
				ct.ActiveTaskID, ct.SendState, ct.Status = "", sendUnknown, statusAttention
				ct.setSendError(taskContextUncertainSendError, "send-uncertain", "")
			}
		}
	})
}

func (a *appModel) reopenProductConnection(host *piAppHost, config *connectionConfig, apply bool) {
	form := a.connectionForm
	form.Busy, a.connectionApplying = true, true
	form.Status = connStatusChecking
	form.Error, form.ErrorDetail, form.ReloadBlocked = "", "", ""
	generation := a.connectionGeneration + 1
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var err error
		if host != nil {
			outcome, _ := host.product.Wait(ctx)
			if !outcome.Reaped {
				err = errors.New("the previous product worker is still running")
			}
		}
		if err == nil {
			err = a.openProductProfile(ctx, false, generation)
		}
		a.updateUI(func() {
			a.connectionApplying, a.connectionForm.Busy = false, false
			a.connectionForm.Status = ""
			if err != nil {
				a.connectionForm.Error = a.connectionSafeMessage(err)
			} else {
				current := a.productHost()
				go a.observeProductExit(current)
				if !a.shutdownStarted {
					switch {
					case config != nil:
						a.connectionForm.Draft = form.Draft
						a.changeProductConnection(current, config, apply)
					case apply && a.productConnectionPending():
						a.changeProductConnection(current, nil, true)
					case apply && a.connectionSaved == nil:
						a.connectionForm.ReloadBlocked = connBlockedNoSaved
					case apply:
						a.connectionForm.Status = connStatusApplied
					}
				}
			}
			if a.shutdownStarted {
				a.finishShutdown()
			}
		})
	}()
}

func productContact(bot pi.ProductBot) *contact {
	profile := bot.Profile
	var tools *[]string
	if profile.CodingTools != nil {
		tools = &profile.CodingTools
	}
	return &contact{
		ID: bot.BotID, Name: profile.Name, Role: profile.Role, Purpose: profile.Purpose,
		Instructions: profile.Instructions, Workspace: profile.Workspace, BuiltinTools: tools,
		Archived: bot.Archived, SessionID: bot.CurrentConversationID, Status: statusReady,
	}
}

func productTaskContext(materials []pi.ProductMaterial) []taskContextItem {
	items := make([]taskContextItem, len(materials))
	for i, item := range materials {
		items[i] = taskContextItem{ID: item.ID, Name: item.Name, Source: item.Source, Text: item.Text, Revision: item.Revision}
	}
	return items
}

func applyProductExecution(ct *contact, execution *pi.ProductExecution) {
	ct.ActiveTaskID, ct.SendState, ct.Status = "", sendIdle, statusReady
	if execution == nil {
		return
	}
	ct.LastTaskID = execution.ExecutionID
	ct.SessionState = execution.State
	switch execution.State {
	case "starting", "running":
		ct.ActiveTaskID, ct.SendState, ct.Status = execution.ExecutionID, sendAccepted, statusWorking
	case "unknown":
		ct.SendState, ct.Status = sendUnknown, statusAttention
		ct.setSendError(taskContextUncertainSendError, "send-uncertain", "")
	case "failed":
		ct.Status = statusAttention
		ct.setSendError(execution.Error, "", "")
	}
}

func (a *appModel) applyBuiltinCatalog(ready pi.Ready) {
	a.runtimeBuiltinTools = make([]runtimeBuiltinTool, len(ready.BuiltinCatalog.Supported))
	for i, tool := range ready.BuiltinCatalog.Supported {
		a.runtimeBuiltinTools[i] = runtimeBuiltinTool{Name: tool.Name, Description: tool.Description}
	}
	a.runtimeBuiltinDefaults = append([]string{}, ready.BuiltinCatalog.Defaults...)
	a.runtimeBuiltinToolsLoaded = true
	a.runtimeBuiltinToolsErr = ""
}

func piWireAPI(api string) pi.WireAPI {
	if api == connectionAPIResponses {
		return pi.OpenAIResponses
	}
	return pi.OpenAICompletions
}

func piConfigureParams(config connectionConfig, sourceID string) pi.ConfigureParams {
	return pi.ConfigureParams{WireAPI: piWireAPI(config.API), BaseURL: config.Endpoint, APIKey: config.APIKey,
		Model: config.Model, ContextTokens: config.ContextTokens, OutputTokens: config.OutputTokens, CredentialSource: sourceID}
}

func (a *appModel) startPiHost() {
	if a.demo || a.shutdownStarted || a.connectionApplying {
		return
	}
	if a.connectionSaved == nil || a.store == nil {
		a.runtimeStatus = runtimeStateFailed
		a.runtimeErrKind = runtimeNoticeConnectionRequired
		return
	}
	a.ensureConnectionForm()
	a.connectionApplying = true
	a.connectionForm.Busy = true
	a.connectionForm.Status = connStatusChecking
	config := *a.connectionSaved
	host := a.pi
	owner := host
	generation := a.connectionGeneration
	ownerGeneration := generation
	connectionPath, stateDir := a.connectionPath, filepath.Join(filepath.Dir(a.storePath), "pi")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		private, err := readPrivateConnectionConfig(connectionPath)
		if err == nil {
			var saved connectionConfig
			saved, err = private.defaults()
			if err == nil && saved != config {
				err = errors.New("saved connection changed while applying")
			}
		}
		if err == nil && host != nil {
			select {
			case <-host.client.Done():
				outcome, _ := host.client.Wait(ctx)
				if !outcome.Reaped {
					err = errors.New("Pi worker exit was not reaped")
				} else {
					select {
					case <-host.client.CallbacksDone():
					case <-ctx.Done():
						err = ctx.Err()
					}
					if err == nil && host.exitApplied != nil {
						select {
						case <-host.exitApplied:
						case <-ctx.Done():
							err = ctx.Err()
						}
					}
				}
				if err == nil {
					a.runOnUI(func() {
						if a.pi != owner || a.connectionGeneration != ownerGeneration || a.shutdownStarted {
							err = errors.New("Pi connection owner changed while applying")
							return
						}
						a.pi = nil
						a.clearBuiltinToolCatalog()
					})
					if err == nil {
						host = nil
						owner = nil
					}
				}
			default:
			}
		}
		created := false
		cleanupFailed := false
		if err == nil && host == nil {
			generation++
			var node, worker string
			node, worker, err = piPayloadPaths()
			if err == nil {
				host = &piAppHost{generation: generation, connectionPath: connectionPath, exitApplied: make(chan struct{}),
					bridges: make(map[string]*piIssueTools), contexts: make(map[string]pi.ContextBinding)}
				host.client, err = pi.Start(pi.Options{NodePath: node, WorkerPath: worker, StateDir: stateDir,
					Handlers: pi.Handlers{OnEvent: func(event pi.Event) { host.onEvent(a, event) },
						OnExit: func(outcome pi.ProcessOutcome) { host.onExit(a, outcome) }}})
				created = err == nil
			}
		}
		var ready pi.Ready
		if err == nil {
			ready, err = host.client.Ready(ctx)
			if err == nil && ready.Runtime.Version != "v22.23.5" {
				err = fmt.Errorf("Pi worker requires Node 22.23.5; got %s", ready.Runtime.Version)
			}
		}
		if err == nil {
			_, err = host.client.Configure(ctx, piConfigureParams(config, private.CredentialSource))
		}
		if err != nil && created {
			cleanup, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			outcome, cleanupErr := host.client.Shutdown(cleanup)
			if outcome.Reaped {
				select {
				case <-host.client.CallbacksDone():
				case <-cleanup.Done():
					cleanupErr = errors.Join(cleanupErr, cleanup.Err())
				}
			}
			if !outcome.Reaped || cleanup.Err() != nil {
				cleanupFailed = true
				err = errors.Join(err, cleanupErr, errors.New("Pi startup worker cleanup is incomplete"))
			}
			cleanupCancel()
		}
		a.updateUI(func() {
			a.connectionApplying = false
			a.connectionForm.Busy = false
			if a.pi != owner || a.connectionGeneration != ownerGeneration {
				err = errors.New("Pi connection owner changed while applying")
			}
			if err == nil {
				select {
				case <-host.client.Done():
					err = errors.New("Pi worker exited while applying")
					if created {
						cleanupFailed = true
					}
				default:
				}
			}
			if err != nil {
				if cleanupFailed && a.pi == owner {
					a.pi = host
					a.connectionGeneration = generation
				}
				if created || owner == nil {
					a.runtimeStatus = runtimeStateFailed
				}
				a.connectionForm.Status = ""
				a.connectionForm.Error = connErrorApplyConfig
				a.connectionForm.ErrorDetail = a.connectionSafeMessage(err)
				if a.shutdownStarted {
					a.finishShutdown()
				}
				return
			}
			a.pi = host
			a.connectionGeneration = generation
			if a.shutdownStarted {
				a.finishShutdown()
				return
			}
			host.mu.Lock()
			host.defaults = piConfiguration{WireAPI: piWireAPI(config.API), BaseURL: config.Endpoint,
				Model: config.Model, ContextTokens: config.ContextTokens, OutputTokens: config.OutputTokens,
				CredentialSource: private.CredentialSource}
			host.mu.Unlock()
			a.applyBuiltinCatalog(ready)
			a.connectionEffective = &config
			a.connectionManaged = true
			a.workDir = config.WorkingDirectory
			a.runtimeStatus = runtimeStateReady
			a.runtimeMaxActiveTasks = config.MaxActiveTasks
			a.runtimeCapabilitiesLoaded = true
			a.runtimeErr, a.runtimeErrKind = "", ""
			a.connectionForm.Status = connStatusApplied
			a.connectionForm.Error = ""
			for _, ct := range a.contacts {
				if ct.PiBinding != nil && !ct.Loaded {
					a.reloadContactByID(ct.ID)
				}
			}
		})
	}()
}

func (h *piAppHost) onEvent(a *appModel, event pi.Event) {
	h.mu.Lock()
	bridge := h.bridges[event.RunID]
	h.mu.Unlock()
	if bridge == nil {
		return
	}
	if err := bridge.HandleEvent(event); err != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = h.client.CancelRun(ctx, pi.CancelRunParams{ContextID: bridge.binding.ContextID, RunID: bridge.runID})
		}()
		return
	}
	if bridge.binding.Purpose == pi.Activity {
		a.updateUI(func() { a.applyPiActivityEvent(h, bridge, event) })
		return
	}
	a.updateUI(func() { a.applyPiEvent(h.generation, event) })
}

func (h *piAppHost) onExit(a *appModel, outcome pi.ProcessOutcome) {
	h.mu.Lock()
	for _, bridge := range h.bridges {
		bridge.cancelWait()
	}
	h.outcome = outcome
	h.mu.Unlock()
	a.updateUI(func() {
		if h.exitApplied != nil {
			defer close(h.exitApplied)
		}
		if a.pi != h {
			return
		}
		a.clearBuiltinToolCatalog()
		for _, ct := range a.contacts {
			if ct.ActiveTaskID != "" || ct.SendState == sendSubmitting {
				ct.ActiveTaskID = ""
				ct.SendState = sendUnknown
				ct.Status = statusAttention
				if ct.pendingSend == nil {
					ct.pendingSend = &pendingSend{Draft: ct.SubmittedDraft}
				}
				ct.pendingSend.State = "unknown"
				ct.setSendError("Connection lost; message delivery is unconfirmed. This message will not be sent again automatically.", "send-uncertain", "")
			}
			for _, issue := range ct.Issues {
				if issue.Work.ActiveTaskID == "" && issue.Work.SendState != sendSubmitting {
					continue
				}
				for _, execution := range issue.Executions {
					if execution != nil && execution.PiIntent && (execution.Status == issueExecutionPending || execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning) {
						execution.Status = issueExecutionUnknown
						execution.Detail = "Pi disconnected; the activity outcome is unknown."
					}
				}
				for _, question := range issue.Questions {
					if question != nil && question.PiCall != nil && question.DeliveryState == issueQuestionDeliveryDispatching {
						question.DeliveryState = issueQuestionDeliveryUnknown
					}
				}
				issue.Work.ActiveTaskID = ""
				issue.Work.SendState = sendUnknown
				issue.Status = issueStatusNeedsAttention
			}
		}
		_ = a.persist()
		a.runtimeStatus = runtimeStateExited
		if a.shutdownStarted {
			a.finishShutdown()
		}
	})
}

func (h *piAppHost) openContext(ctx context.Context, binding piConversationBinding) (pi.ContextBinding, error) {
	if binding.Engine != "pi" || binding.Context.ContextID == "" || binding.Context.BotID == "" || binding.Context.Purpose != pi.Conversation {
		return pi.ContextBinding{}, errors.New("invalid Pi conversation binding")
	}
	h.openMu.Lock()
	defer h.openMu.Unlock()
	if existing, ok := h.contexts[binding.Context.ContextID]; ok {
		if existing.BotID != binding.Context.BotID || existing.SessionRef != binding.Context.SessionRef {
			return pi.ContextBinding{}, errors.New("Pi context is owned by another binding")
		}
		return existing, nil
	}
	private, err := readPrivateConnectionConfig(h.connectionPath)
	if err != nil {
		return pi.ContextBinding{}, err
	}
	source, err := private.resolve(binding.Configuration.CredentialSource)
	if err != nil {
		return pi.ContextBinding{}, err
	}
	if piWireAPI(source.API) != binding.Configuration.WireAPI || source.Endpoint != binding.Configuration.BaseURL {
		return pi.ContextBinding{}, errors.New("captured Pi connection source does not match the binding")
	}
	config := pi.ConfigureParams{WireAPI: binding.Configuration.WireAPI, BaseURL: binding.Configuration.BaseURL,
		APIKey: source.APIKey, CredentialSource: binding.Configuration.CredentialSource,
		Model: binding.Configuration.Model, ContextTokens: binding.Configuration.ContextTokens, OutputTokens: binding.Configuration.OutputTokens}
	tools, err := piIssueToolDefinitions()
	if err != nil {
		return pi.ContextBinding{}, err
	}
	actual, err := h.client.OpenContext(ctx, pi.OpenContextParams{ContextID: binding.Context.ContextID,
		BotID: binding.Context.BotID, Purpose: pi.Conversation, SelectionRevision: binding.Context.SelectionRevision,
		CWD: binding.Selection.CWD, Instructions: binding.Selection.Instructions,
		ResumeRef: binding.Context.SessionRef, Used: binding.Context.Used,
		CodingTools: binding.Selection.CodingTools, DomainTools: tools, Configuration: &config})
	if err != nil {
		return pi.ContextBinding{}, err
	}
	if binding.Context.Used && actual.SessionRef != binding.Context.SessionRef {
		return pi.ContextBinding{}, errors.New("used Pi conversation returned a different session reference")
	}
	h.contexts[actual.ContextID] = actual
	return actual, nil
}

func (a *appModel) sendPiConversation(ct *contact, text string) bool {
	if ct == nil || a.contactByID(ct.ID) != ct {
		return false
	}
	if reason := a.canSend(ct); reason != "" {
		ct.setSendError(sendBlockRaw(reason), reason, "")
		return false
	}
	if err := validateTaskContext(ct.Context.items); err != nil {
		ct.setSendError(err.Error(), "", "")
		return false
	}
	if host := a.productHost(); host != nil {
		return a.sendProductConversation(host, ct)
	}
	ct.SubmittedDraft, ct.SubmittedRev = text, ct.draftRev
	ct.pendingSend = &pendingSend{Draft: text, Context: ct.Context.snapshot(), State: "submitting"}
	ct.SendState = sendSubmitting
	ct.clearSendError()
	if err := a.persist(); err != nil {
		ct.pendingSend = nil
		ct.SendState = sendIdle
		ct.setSendError(err.Error(), "", "")
		return false
	}
	host := a.pi
	contactID := ct.ID
	binding := clonePiConversationBinding(ct.PiBinding)
	workspace := a.effectiveContactWorkspace(ct)
	instructions := a.issueToolConversationSystemPrompt(ct)
	codingTools := cloneBuiltinTools(ct.BuiltinTools)
	contextItems := ct.Context.snapshot()
	host.mu.Lock()
	captured := host.defaults
	host.mu.Unlock()
	a.submissionAsyncStarted()
	go a.submitPiConversation(host, contactID, binding, workspace, instructions, codingTools, captured, text, contextItems)
	return true
}

func (a *appModel) submitPiConversation(host *piAppHost, contactID string, binding *piConversationBinding, workspace, instructions string, codingTools *[]string, captured piConfiguration, text string, items []taskContextItem) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if binding == nil {
		if captured.CredentialSource == "" {
			a.failPiSubmission(host, contactID, "", errors.New("Pi connection defaults are unavailable"), false)
			return
		}
		selection := []string(nil)
		if codingTools != nil {
			selection = append([]string{}, (*codingTools)...)
		}
		binding = &piConversationBinding{Engine: "pi", Context: pi.ContextBinding{ContextID: newIssueToolTurnID(), BotID: contactID,
			Purpose: pi.Conversation, SelectionRevision: 1},
			Selection:     piSelection{CWD: workspace, Instructions: instructions, CodingTools: selection},
			Configuration: captured}
	}
	actual, err := host.openContext(ctx, *binding)
	if err != nil {
		a.failPiSubmission(host, contactID, "", err, false)
		return
	}
	binding.Context = actual
	var scope *IssueToolScope
	var issueList pi.ContextInput
	a.runOnUI(func() {
		ct := a.contactByID(contactID)
		if a.pi != host || a.shutdownStarted || ct == nil || ct.pendingSend == nil || ct.SendState != sendSubmitting {
			err = errors.New("conversation owner changed before submission")
			return
		}
		ct.PiBinding = clonePiConversationBinding(binding)
		ct.PiBinding.Context.Used = true
		ct.SessionID = actual.SessionRef
		ct.Loaded = true
		ct.SessionWorkingDir = binding.Selection.CWD
		ct.SessionModel = binding.Configuration.Model
		ct.SessionAPI = string(binding.Configuration.WireAPI)
		if err = a.persist(); err != nil {
			return
		}
		turnID := newIssueToolTurnID()
		scope, err = a.issueToolNewScope(contactID, actual.SessionRef, turnID)
		if err == nil {
			issueList, err = a.issueToolConversationIssueList(scope)
		}
		if err == nil {
			err = a.issueToolAttachPendingScope(contactID, scope.Ref)
		}
	})
	if err != nil {
		a.failPiSubmission(host, contactID, "", err, false)
		return
	}
	runID := newIssueToolTurnID()
	inputs := taskContextInputs(items, issueList, pi.ContextInput{Name: issueToolScopeContextName, Text: scope.Ref})
	if err := validateRuntimeContextInputs(inputs); err != nil {
		a.failPiSubmission(host, contactID, "", err, false)
		return
	}
	run := pi.StartRunParams{ContextID: actual.ContextID, RunID: runID, Text: text, Context: inputs,
		Correlation: &pi.RunCorrelation{TurnID: scope.TurnID, ScopeRef: scope.Ref}}
	bridge, err := newPiIssueTools(ctx, a, host.client, actual, run)
	if err != nil {
		a.failPiSubmission(host, contactID, "", err, false)
		return
	}
	host.mu.Lock()
	host.bridges[runID] = bridge
	host.mu.Unlock()
	a.runOnUI(func() {
		ct := a.contactByID(contactID)
		if a.pi != host || a.shutdownStarted || ct == nil || ct.pendingSend == nil || ct.pendingSend.ToolScopeRef != scope.Ref {
			err = errors.New("conversation owner changed before admission")
			return
		}
		ct.ActiveTaskID = runID
		a.issueToolBindTask(scope.Ref, runID)
		err = a.persist()
	})
	if err != nil {
		_ = bridge.Drain(ctx)
		host.mu.Lock()
		select {
		case <-bridge.done:
			delete(host.bridges, runID)
		default:
		}
		host.mu.Unlock()
		a.failPiSubmission(host, contactID, "", err, false)
		return
	}
	_, err = host.client.StartRun(ctx, run)
	if err != nil {
		bridge.mu.Lock()
		started := bridge.started
		bridge.mu.Unlock()
		if started {
			a.updateUI(func() {
				defer a.submissionAsyncFinished()
				if a.shutdownStarted {
					a.finishShutdown()
				}
			})
			return
		}
		var command *pi.CommandError
		uncertain := !errors.As(err, &command) || command.Delivery == pi.Unanswered
		_ = bridge.Drain(ctx)
		host.mu.Lock()
		select {
		case <-bridge.done:
			delete(host.bridges, runID)
		default:
		}
		host.mu.Unlock()
		a.failPiSubmission(host, contactID, runID, err, uncertain)
		return
	}
	a.updateUI(func() {
		defer a.submissionAsyncFinished()
		if a.shutdownStarted {
			a.finishShutdown()
		}
	})
}

func (a *appModel) failPiSubmission(host *piAppHost, contactID, runID string, failure error, uncertain bool) {
	a.updateUI(func() {
		defer a.submissionAsyncFinished()
		ct := a.contactByID(contactID)
		if a.pi != host || ct == nil || runID != "" && ct.ActiveTaskID != runID {
			return
		}
		ct.ActiveTaskID = ""
		if uncertain {
			ct.SendState = sendUnknown
			if ct.pendingSend == nil {
				ct.pendingSend = &pendingSend{Draft: ct.SubmittedDraft}
			}
			ct.pendingSend.State = "unknown"
			ct.setSendError("Delivery is unknown; reload before retrying. "+a.connectionSafeMessage(failure), "send-uncertain", "")
		} else {
			ct.SendState = sendIdle
			ct.pendingSend = nil
			ct.setSendError(a.connectionSafeMessage(failure), "", "")
		}
		_ = a.persist()
		if a.shutdownStarted {
			a.finishShutdown()
		}
	})
}

func (a *appModel) applyPiEvent(generation int, event pi.Event) {
	if a.pi == nil || a.connectionGeneration != generation || a.pi.generation != generation {
		return
	}
	var ct *contact
	for _, candidate := range a.contacts {
		if candidate.PiBinding != nil && candidate.PiBinding.Context.ContextID == event.ContextID && candidate.ActiveTaskID == event.RunID {
			ct = candidate
			break
		}
	}
	if ct == nil {
		return
	}
	switch event.Name {
	case pi.RunStarted:
		if ct.SendState != sendSubmitting || ct.pendingSend == nil {
			return
		}
		pending := ct.pendingSend
		ct.SendState = sendAccepted
		ct.LastTaskID = event.RunID
		ct.Status = statusWorking
		ct.SessionState = "running"
		ct.Messages = append(ct.Messages, &message{Key: "pi:" + event.RunID + ":user", Kind: msgUser,
			Text: pending.Draft, Time: nowTime(), TaskID: event.RunID, Context: cloneTaskContextItems(pending.Context)})
		previousDraft := ct.Draft
		previousContext := ct.Context.snapshot()
		previousPreviewID := ct.Context.previewID
		if ct.draftRev == ct.SubmittedRev && strings.TrimSpace(ct.Draft) == strings.TrimSpace(pending.Draft) {
			ct.Draft = ""
		}
		pending.State = "unknown"
		ct.Context.clearSubmitted(pending.Context)
		if err := a.persist(); err != nil {
			ct.Draft = previousDraft
			ct.Context.items = previousContext
			ct.Context.previewID = previousPreviewID
			ct.SendState = sendUnknown
			ct.setSendError("Run admitted, but its local state could not be saved.", "send-uncertain", "")
		}
		if a.shutdownStarted {
			a.finishShutdown()
		}
	case pi.TextDelta:
		var data pi.TextDeltaData
		if json.Unmarshal(event.Data, &data) != nil {
			return
		}
		item := a.liveText(ct, event.RunID, "pi")
		item.Text += data.Text
		item.Streaming = true
	case pi.RunSettled:
		var data pi.RunSettledData
		if json.Unmarshal(event.Data, &data) != nil {
			return
		}
		if data.Text != "" {
			item := a.liveText(ct, event.RunID, "pi")
			item.Text, item.Streaming = data.Text, false
		}
		pending := ct.pendingSend
		ct.ActiveTaskID = ""
		ct.SendState = sendIdle
		ct.pendingSend = nil
		ct.SessionState = string(data.Status)
		ct.Status = statusReady
		if data.Status != pi.Completed {
			ct.Status = statusAttention
			if data.Error != nil {
				ct.setSendError(data.Error.Message, "", "")
			}
		}
		if err := a.persist(); err != nil {
			ct.pendingSend = pending
			ct.SendState = sendUnknown
			ct.setSendError("Run settled, but its local state could not be saved.", "send-uncertain", "")
		}
		host := a.pi
		host.mu.Lock()
		bridge := host.bridges[event.RunID]
		host.mu.Unlock()
		if bridge != nil {
			a.submissionAsyncStarted()
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := bridge.Drain(ctx)
				cancel()
				host.mu.Lock()
				delete(host.bridges, event.RunID)
				host.mu.Unlock()
				a.updateUI(func() {
					defer a.submissionAsyncFinished()
					if err != nil && a.pi == host {
						ct.setSendError(a.connectionSafeMessage(err), "send-uncertain", "")
					}
					if a.shutdownStarted {
						a.finishShutdown()
					}
				})
			}()
		}
		if a.shutdownStarted {
			a.finishShutdown()
		}
	}
}

func (h *piAppHost) loadPiHistory(ctx context.Context, binding piConversationBinding) (historyProjection, error) {
	var result historyProjection
	addDetail := func(detail string) {
		if !strings.Contains(result.detail, detail) {
			if result.detail != "" {
				result.detail += " "
			}
			result.detail += detail
		}
	}
	displayTime := func(timestamp string) string {
		parsed, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			addDetail("Some history dates are missing or invalid.")
			return ""
		}
		formatted := formatMillis(parsed.UnixMilli())
		if formatted == "" {
			addDetail("Some history dates are missing or invalid.")
		}
		return formatted
	}
	actual, err := h.openContext(ctx, binding)
	if err != nil {
		return result, err
	}
	cursor := ""
	for {
		page, err := h.client.History(ctx, pi.HistoryParams{ContextID: actual.ContextID, Cursor: cursor, Limit: 100})
		if err != nil {
			return historyProjection{}, err
		}
		if page.ContextID != actual.ContextID || page.SessionRef != actual.SessionRef {
			return historyProjection{}, errors.New("Pi history owner changed")
		}
		for _, raw := range page.Entries {
			var entry struct {
				ID         string          `json:"id"`
				Type       string          `json:"type"`
				Timestamp  string          `json:"timestamp"`
				CustomType string          `json:"customType"`
				Data       json.RawMessage `json:"data"`
				Message    json.RawMessage `json:"message"`
			}
			if err := json.Unmarshal(raw, &entry); err != nil || entry.ID == "" {
				return historyProjection{}, errors.New("invalid Pi public history entry")
			}
			key := "pi:" + actual.ContextID + ":" + entry.ID
			if entry.Type == "custom" && entry.CustomType == "nagi-turn-v1" {
				var turn struct {
					RunID   string            `json:"runId"`
					Text    string            `json:"text"`
					Context []pi.ContextInput `json:"context"`
				}
				if err := json.Unmarshal(entry.Data, &turn); err != nil {
					return historyProjection{}, err
				}
				var contexts []taskContextItem
				for _, item := range turn.Context {
					if item.Name != issueToolScopeContextName && item.Name != issueToolIssueListContextName {
						contexts = append(contexts, taskContextItem{Name: item.Name, Text: item.Text})
					}
				}
				date := displayTime(entry.Timestamp)
				result.messages = append(result.messages, &message{Key: key, Kind: msgUser, Text: turn.Text, Time: date, TaskID: turn.RunID, History: true, Context: contexts})
				result.entries = append(result.entries, historyEntry{ID: key, Date: date, From: "me", Text: turn.Text, Context: contexts})
				continue
			}
			if entry.Type != "message" {
				continue
			}
			var header struct {
				Role string `json:"role"`
			}
			if err := json.Unmarshal(entry.Message, &header); err != nil {
				return historyProjection{}, err
			}
			if header.Role == "toolResult" {
				addDetail("Tool output is kept in saved history and isn't shown here.")
				continue
			}
			if header.Role != "assistant" {
				continue
			}
			var msg struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if err := json.Unmarshal(entry.Message, &msg); err != nil {
				return historyProjection{}, err
			}
			var text strings.Builder
			for _, part := range msg.Content {
				if part.Type == "text" {
					text.WriteString(part.Text)
				}
			}
			if text.Len() != 0 {
				date := displayTime(entry.Timestamp)
				result.messages = append(result.messages, &message{Key: key, Kind: msgContact, Text: text.String(), Time: date, History: true})
				result.entries = append(result.entries, historyEntry{ID: key, Date: date, From: "contact", Text: text.String()})
			}
		}
		if page.NextCursor == "" {
			return result, nil
		}
		if page.NextCursor == cursor {
			return historyProjection{}, errors.New("Pi history cursor did not advance")
		}
		cursor = page.NextCursor
	}
}

func (a *appModel) drainPiHost() {
	if a.shutdownStarted {
		a.finishShutdown()
		return
	}
	a.beginShutdown()
}

func (a *appModel) finishPiShutdown(host *piAppHost) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	host.mu.Lock()
	bridges := make([]*piIssueTools, 0, len(host.bridges))
	for _, bridge := range host.bridges {
		bridges = append(bridges, bridge)
	}
	host.mu.Unlock()
	var drainErr error
	drained := true
	for _, bridge := range bridges {
		drainErr = errors.Join(drainErr, bridge.Drain(ctx))
		select {
		case <-bridge.done:
		default:
			drained = false
		}
	}
	host.openMu.Lock()
	for contextID := range host.contexts {
		_, err := host.client.CloseContext(ctx, contextID)
		drainErr = errors.Join(drainErr, err)
	}
	host.openMu.Unlock()
	outcome, err := host.client.Shutdown(ctx)
	drainErr = errors.Join(drainErr, err)
	callbacksDone := false
	select {
	case <-host.client.CallbacksDone():
		callbacksDone = true
	case <-ctx.Done():
		drainErr = errors.Join(drainErr, ctx.Err())
	}
	host.mu.Lock()
	host.outcome = outcome
	host.mu.Unlock()
	a.updateUI(func() {
		if a.pi != host {
			return
		}
		if !drained || !callbacksDone || !outcome.Reaped {
			a.shutdownDrainStarted = false
			a.runtimeErrKind = runtimeNoticeRuntimeShutdown
			if drainErr != nil {
				a.runtimeErr = a.connectionSafeMessage(drainErr)
			} else {
				a.runtimeErr = "Pi worker exit was not reaped"
			}
			return
		}
		if drainErr != nil {
			a.runtimeErrKind = runtimeNoticeRuntimeShutdown
			a.runtimeErr = a.connectionSafeMessage(drainErr)
		}
		a.pi = nil
		a.quitReady = true
		if a.requestQuit != nil {
			a.requestQuit()
		}
	})
}

type piConnectionProbe struct {
	mu            sync.Mutex
	client        *pi.Client
	stateDir      string
	outcome       pi.ProcessOutcome
	callbacksDone bool
	forced        bool
	cancelErr     error
	shutdownErr   error
	waitErr       error
	killErr       error
	callbackErr   error
	removeErr     error
	cleanupErr    error
	testErr       error
}

func (probe *piConnectionProbe) cleanupComplete() bool {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return probe.client == nil || probe.outcome.Reaped && probe.callbacksDone
}

func probePiConnection(ctx context.Context, config connectionConfig, probe *piConnectionProbe) (err error) {
	defer func() {
		probe.mu.Lock()
		probe.testErr = err
		probe.mu.Unlock()
	}()
	if fields := validateConnectionConfig(config); len(fields) != 0 {
		return errors.New(connectionValidationSummary(fields))
	}
	node, worker, err := piPayloadPaths()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "nagi-pi-test-*")
	if err != nil {
		return err
	}
	probe.mu.Lock()
	probe.stateDir = dir
	probe.mu.Unlock()
	events := make(chan pi.Event, 1)
	client, err := pi.Start(pi.Options{NodePath: node, WorkerPath: worker, StateDir: dir,
		Handlers: pi.Handlers{OnEvent: func(event pi.Event) {
			if event.Name == pi.RunSettled {
				select {
				case events <- event:
				default:
				}
			}
		}}})
	if err != nil {
		removeErr := os.RemoveAll(dir)
		probe.mu.Lock()
		probe.removeErr = removeErr
		probe.mu.Unlock()
		return errors.Join(err, removeErr)
	}
	probe.mu.Lock()
	probe.client = client
	probe.mu.Unlock()
	contextID, runID := newIssueToolTurnID(), newIssueToolTurnID()
	submitted, settled := false, false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var cancelErr, waitErr, killErr, callbackErr, removeErr error
		if submitted && !settled {
			_, cancelErr = client.CancelRun(cleanup, pi.CancelRunParams{ContextID: contextID, RunID: runID})
		}
		outcome, shutdownErr := client.Shutdown(cleanup)
		shutdownErrors := shutdownErr
		if !outcome.Reaped {
			select {
			case <-events:
				outcome, shutdownErr = client.Shutdown(cleanup)
				shutdownErrors = errors.Join(shutdownErrors, shutdownErr)
			case <-client.Done():
				outcome, waitErr = client.Wait(cleanup)
			case <-cleanup.Done():
			}
		}
		forced := false
		if !outcome.Reaped {
			<-cleanup.Done()
			killCtx, killCancel := context.WithTimeout(context.Background(), 10*time.Second)
			forced = true
			outcome, killErr = client.Kill(killCtx)
			killCancel()
		}
		callbackCtx, callbackCancel := context.WithTimeout(context.Background(), 10*time.Second)
		callbacksDone := false
		select {
		case <-client.CallbacksDone():
			callbacksDone = true
		case <-callbackCtx.Done():
			callbackErr = callbackCtx.Err()
		}
		callbackCancel()
		if outcome.Reaped && callbacksDone {
			removeErr = os.RemoveAll(dir)
		}
		cleanupErr := errors.Join(cancelErr, shutdownErr, waitErr, killErr, callbackErr, removeErr)
		if forced {
			cleanupErr = errors.Join(cleanupErr, errors.New("connection test required forced worker termination"))
		}
		if !outcome.Reaped || !callbacksDone {
			cleanupErr = errors.Join(cleanupErr, errors.New("connection test worker cleanup is unconfirmed"))
		}
		probe.mu.Lock()
		probe.outcome = outcome
		probe.callbacksDone = callbacksDone
		probe.forced = forced
		probe.cancelErr = cancelErr
		probe.shutdownErr = shutdownErrors
		probe.waitErr = waitErr
		probe.killErr = killErr
		probe.callbackErr = callbackErr
		probe.removeErr = removeErr
		probe.cleanupErr = cleanupErr
		probe.mu.Unlock()
		err = errors.Join(err, cleanupErr)
	}()
	ready, err := client.Ready(ctx)
	if err != nil {
		return err
	}
	if ready.Runtime.Version != "v22.23.5" {
		return fmt.Errorf("Pi worker requires Node 22.23.5; got %s", ready.Runtime.Version)
	}
	if _, err = client.Configure(ctx, piConfigureParams(config, newIssueToolScopeRef())); err != nil {
		return err
	}
	if _, err = client.OpenContext(ctx, pi.OpenContextParams{ContextID: contextID, BotID: "connection-test", Purpose: pi.Conversation,
		SelectionRevision: 1, CWD: config.WorkingDirectory, CodingTools: []string{}, DomainTools: []pi.DomainTool{}}); err != nil {
		return err
	}
	submitted = true
	if _, err = client.StartRun(ctx, pi.StartRunParams{ContextID: contextID, RunID: runID, Text: "Reply with OK."}); err != nil {
		return err
	}
	for {
		select {
		case event := <-events:
			if event.ContextID != contextID || event.RunID != runID || event.Name != pi.RunSettled {
				continue
			}
			settled = true
			var result pi.RunSettledData
			if err = json.Unmarshal(event.Data, &result); err != nil {
				return err
			}
			if result.Status != pi.Completed {
				return fmt.Errorf("connection test did not complete: %s", result.Status)
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-client.Done():
			return pi.ErrDisconnected
		}
	}
}
