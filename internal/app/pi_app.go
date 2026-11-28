package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

type piAppHost struct {
	product          *pi.ProductClient
	productBots      map[string]pi.ProductBot
	productDrafts    map[string]pi.ProductDraft
	productProviders pi.ProductProviderList
	providerRead     uint64
	providerRefresh  bool
	productEvents    chan struct{}
	productPending   map[string]productPendingSend
	generation       int
	activityRevision uint64
	recoveryID       string
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
	if fresh {
		if err := os.MkdirAll(filepath.Dir(a.storePath), 0700); err != nil {
			return err
		}
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
	providers, err := host.product.ListProviders(ctx)
	if err != nil {
		return err
	}
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
	bots, err := listProductBots(ctx, host.product, opened.Snapshot)
	if err != nil {
		return err
	}
	architect, err := host.product.EnsureBuiltinBot(ctx, pi.ProductBotEnsureBuiltinParams{
		Kind: pi.SystemKindBotArchitect, OperationID: "builtin-pandorabox",
	})
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(bots, func(bot pi.ProductBot) bool { return bot.BotID == architect.BotID }); i >= 0 {
		architect = bots[i]
		bots = slices.Delete(bots, i, i+1)
	}
	bots = slices.Insert(bots, 0, architect)
	contacts := make([]*contact, 0, len(bots))
	activeSessions := make(map[string]map[string]pi.ProductSessionSummary, len(bots))
	for _, bot := range bots {
		conversation, err := host.product.GetConversation(ctx, bot.CurrentConversationID)
		if err != nil {
			return err
		}
		ct := productContact(bot)
		host.productBots[ct.ID] = bot
		if draft := conversation.Draft; draft != nil {
			host.productDrafts[ct.ID] = *draft
			applyProductDraft(ct, draft)
		}
		applyProductExecution(ct, conversation.LatestExecution)
		if !ct.Archived {
			ct.activeActivities, _ = listBotActiveActivities(ctx, host.product, ct.ID)
			ct.orchestration.items, _ = listBotActiveOrchestrations(ctx, host.product, ct.ID)
			activeSessions[ct.ID], _ = listBotActiveSessions(ctx, host.product, ct.ID)
		}
		if pending, ok := host.productPending[ct.ID]; ok && pending.ConversationID == ct.SessionID {
			receipt, lookupErr := host.product.Operation(ctx, pending.OperationID)
			if lookupErr == nil && conversationSendReceiptMatches(pending, receipt) {
				if setQueuedConversationSend(ct, receipt) {
					if draft := conversation.Draft; draft != nil && draft.Revision == pending.DraftRevision {
						captureSubmittedDraft(ct, *draft)
					}
				} else if receipt.State == "accepted" || receipt.State == "rejected" {
					delete(host.productPending, ct.ID)
					if receipt.State == "rejected" && !applyNonDispatchedSend(ct, receipt) {
						ct.applyProviderSendRejection(providerAdmission(receipt, nil))
					}
				} else if ct.SendState == sendIdle {
					markUncertainConversationSend(ct)
				}
			} else if ct.SendState == sendIdle {
				markUncertainConversationSend(ct)
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
	if projection, err := productTimeline(ctx, host.product, selected.SessionID); err == nil {
		selected.Messages, selected.History, selected.Loaded = projection.messages, projection.entries, true
		selected.transcriptError = projection.transcriptError
	} else {
		selected.LoadErr = safeDiagnostic(err.Error())
		selected.Unavailable = selected.LoadErr
	}
	a.runOnUI(func() {
		a.pi, a.connectionGeneration, a.contacts = host, host.generation, contacts
		a.runtimeStage, a.runtimeHint, a.runtimeHintKind = "", "", ""
		a.runtimeHintArgs = nil
		a.localSaveErr, a.localSavePrevErr, a.localSavePrevKind = "", "", ""
		a.hideBuiltinArchitect = false
		a.applyProductPreferences(opened.Snapshot.Preferences)
		a.selected = slices.Index(contacts, selected)
		a.activityIntents = nil
		a.sessions = sessionWorkspaceState{
			active:              activeSessions,
			updateUI:            a.updateUI,
			persistCloseIntents: a.persistSessionCloseIntents,
		}
		a.schedules = scheduledWorkState{}
		a.scheduledWorkBotID = ""
		a.focusScheduledWorkOpener = ""
		a.backgroundWork = newBackgroundWorkState(nil)
		if local != nil {
			a.activityIntents = local.PendingActivities
			a.sessions.restoreSessionCloses(local.PendingSessionCloses)
			a.schedules.restoreOperations(local.PendingWatchOperations)
			a.backgroundWork = newBackgroundWorkState(local.PendingOrchestrationOperations)
			a.leftPaneWidth = clampPaneWidth(local.Layout.LeftWidth, defaultLeftPaneWidth, minLeftPaneWidth, maxLeftPaneWidth)
			a.rightPaneWidth = clampPaneWidth(local.Layout.RightWidth, defaultRightPaneWidth, minRightPaneWidth, maxRightPaneWidth)
			a.appearanceThemeIDs = local.ThemeIDs
			a.appearanceMode = normalizeAppearanceMode(local.Appearance)
			a.hideBuiltinArchitect = local.HideBuiltinArchitect
		}
		a.providers = providerSettings{}
		a.capabilities = capabilitySettings{}
		if prefs := opened.Snapshot.Preferences; prefs != nil {
			a.capabilities.discoverLocal = prefs.DiscoverLocalSkills
		}
		a.applyProductProviders(providers)
		a.applyProductConnection(opened.Snapshot.Connection)
		a.applyBuiltinCatalog(ready)
		for _, ct := range contacts {
			ct.Context.OnChange = func() error { return a.saveProductDraft(ct) }
		}
		installed = true
		close(host.productEvents)
		a.reconcileControlPlaneIntents(host)
		a.sessions.blocked = a.dataRecoveryBlocksChanges()
		a.sessions.setSessionInspectorClient(host.product)
		a.reconcilePendingWatchOperations(host)
		a.reconcileOrchestrationOperations(host)
		a.refreshDockMenu()
	})
	return nil
}

func listProductBots(ctx context.Context, product *pi.ProductClient, snapshot pi.ProductSnapshot) ([]pi.ProductBot, error) {
	bots := snapshot.Bots
	for cursor := snapshot.NextBotCursor; cursor != nil; {
		page, err := product.ListBots(ctx, pi.ProductPageParams{Cursor: *cursor})
		if err != nil {
			return nil, err
		}
		bots = append(bots, page.Items...)
		cursor = page.NextCursor
	}
	return bots, nil
}

func (a *appModel) persistProductUI(host *piAppHost) error {
	return writeProfileJSON(productUIPath(a.storePath), productUIState{
		Selected: a.selectedID(), Layout: storedLayout{LeftWidth: a.railWidth(), RightWidth: a.workWidth(), WorkspaceOpen: a.workspaceOpen},
		ThemeIDs: a.appearanceThemeIDs, Appearance: normalizeAppearanceMode(a.appearanceMode), Pending: host.productPending, RecoveryID: host.recoveryID,
		PendingActivities:              a.activityIntents,
		PendingSessionCloses:           a.sessions.pendingSessionCloses(),
		PendingWatchOperations:         a.schedules.pendingOperations(),
		PendingOrchestrationOperations: a.backgroundWork.operations,
		HideBuiltinArchitect:           a.hideBuiltinArchitect,
	})
}

func (a *appModel) persistWatchOperations() error {
	host := a.productHost()
	if host == nil {
		return nil
	}
	return a.persistProductUI(host)
}

func (a *appModel) scheduledWorkDeps(c *ui.Context, ct *contact) scheduledWorkDeps {
	deps := a.scheduledWorkFallbackDeps(ct)
	deps.locale = c.Locale()
	winW := a.windowContentWidth(c)
	rowStartX, rowEndX := a.detailRowBounds(winW, 0)
	deps.headerLeftPad, deps.headerRightPad = windowControlInsets(c, rowStartX, rowEndX, winW)
	if a.detailPageActive() && a.railCollapsed {
		deps.showContacts = a.showContactsButton
	}
	return deps
}

func (a *appModel) scheduledWorkFallbackDeps(ct *contact) scheduledWorkDeps {
	return scheduledWorkDeps{
		client:            a.productActivityClient(),
		botID:             ct.ID,
		wb:                a.workTheme,
		blocked:           a.dataRecoveryBlocksChanges(),
		locale:            "en",
		updateUI:          a.updateUI,
		persistOperations: a.persistWatchOperations,
		onBack: func() {
			a.scheduledWorkBotID = ""
			a.focusScheduledWorkOpener = ct.ID
		},
		onOpenConversation: func(conversationID string) {
			if idx := slices.Index(a.contacts, ct); idx >= 0 {
				a.selectContact(idx)
			}
			a.scheduledWorkBotID = ""
			if a.detailPageActive() {
				a.closeDetail()
			}
			a.sessions.activeBotID = ""
			a.browseProductConversations(ct, conversationID)
		},
	}
}

func (a *appModel) reconcilePendingWatchOperations(host *piAppHost) {
	if a.dataRecoveryBlocksChanges() || a.productActivityClient() != host.product {
		return
	}
	for _, ct := range a.contacts {
		if b := a.schedules.bots[ct.ID]; b != nil {
			b.reconcileWatchOperation(a.scheduledWorkFallbackDeps(ct))
		}
	}
}

func (a *appModel) persistSessionCloseIntents(pending []pi.ProductSessionCloseParams) error {
	host := a.productHost()
	if host == nil {
		return nil
	}
	return a.persistProductUI(host)
}

func (a *appModel) sessionInspectorDeps(c *ui.Context, ct *contact) sessionInspectorDeps {
	msgs := locales.In(c.Locale())
	deps := a.sessionInspectorFallbackDeps(ct)
	deps.copy = sessionInspectionLocaleCopy(msgs)
	deps.formatDimensions = msgs.SessionInspectDimensions
	deps.formatGap = msgs.SessionInspectOutputGap
	deps.formatMS = msgs.SessionInspectMilliseconds
	winW := a.windowContentWidth(c)
	rowStartX, rowEndX := a.detailRowBounds(winW, 0)
	deps.headerLeftPad, deps.headerRightPad = windowControlInsets(c, rowStartX, rowEndX, winW)
	if a.detailPageActive() && a.railCollapsed {
		deps.showContacts = a.showContactsButton
	}
	return deps
}

func (a *appModel) sessionInspectorFallbackDeps(ct *contact) sessionInspectorDeps {
	return sessionInspectorDeps{
		client:              a.productActivityClient(),
		botID:               ct.ID,
		wb:                  a.workTheme,
		blocked:             a.dataRecoveryBlocksChanges(),
		copy:                defaultSessionInspectionCopy,
		updateUI:            a.updateUI,
		persistCloseIntents: a.persistSessionCloseIntents,
		onBack: func() {
			opener := a.sessions.botSession(ct.ID).opener
			if opener == "profile-sessions:"+ct.ID {
				a.focusSessionsOpener = ct.ID
				return
			}
			a.closeDetail()
			a.focusBotDrawerOpener(ct.ID, opener)
		},
		onOpenConversation: func(conversationID string) {
			a.selectContactWithDrawer(slices.Index(a.contacts, ct), true)
			a.browseProductConversations(ct, conversationID)
		},
		onOpenActivity: func(activityID string) {
			a.selectContactWithDrawer(slices.Index(a.contacts, ct), true)
			a.priorChats = nil
			a.closeDetail()
			ct.activity.open = true
			a.openProductActivity(ct, activityID)
		},
	}
}

func applyProductDraft(ct *contact, draft *pi.ProductDraft) {
	ct.Draft = draft.Text
	ct.Context.Items = productTaskContext(draft.Materials)
	ct.DraftQuote = cloneQuote(draft.Quote)
}

func cloneQuote(quote *pi.ProductQuote) *pi.ProductQuote {
	if quote == nil {
		return nil
	}
	copy := *quote
	if quote.BubbleIndex != nil {
		index := *quote.BubbleIndex
		copy.BubbleIndex = &index
	}
	return &copy
}

func sameQuote(a, b *pi.ProductQuote) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.MessageID != b.MessageID || a.Sender != b.Sender || a.AuthorName != b.AuthorName || a.Text != b.Text {
		return false
	}
	if a.BubbleIndex == nil || b.BubbleIndex == nil {
		return a.BubbleIndex == b.BubbleIndex
	}
	return *a.BubbleIndex == *b.BubbleIndex
}

func (a *appModel) updateDraftQuoteReference(ct *contact) {
	quote := ct.DraftQuote
	if quote == nil || !a.quoteDraftEditable(ct) || ct.SendState == sendSubmitting {
		return
	}
	for _, m := range ct.Messages {
		if m.Key == quote.MessageID && m.MessageID != "" && m.MessageID != m.Key {
			resolved := *quote
			resolved.MessageID = m.MessageID
			ct.DraftQuote = &resolved
			a.draftChanged(ct)
			return
		}
	}
}

func (a *appModel) saveProductDraft(ct *contact) error {
	host := a.productHost()
	if !a.productProfileReady() || ct == nil || ct.Archived {
		return errors.New("the product conversation is unavailable")
	}
	materials := make([]pi.ProductMaterial, len(ct.Context.Items))
	for i, item := range ct.Context.Items {
		materials[i] = pi.ProductMaterial{ID: item.ID, Name: item.Name, Source: item.Source, Text: item.Text, Revision: item.Revision}
	}
	previous := host.productDrafts[ct.ID]
	if previous.Text == ct.Draft && slices.Equal(previous.Materials, materials) && sameQuote(previous.Quote, ct.DraftQuote) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	draft, err := host.product.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: ct.SessionID},
		ProductText:               pi.ProductText{Text: ct.Draft, Materials: materials}, ExpectedRevision: previous.Revision, OperationID: newContactID(),
		Quote: ct.DraftQuote,
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
	draft := host.productDrafts[ct.ID]
	pending := productPendingSend{ConversationID: ct.SessionID, OperationID: newContactID(), DraftRevision: draft.Revision}
	host.productPending[ct.ID] = pending
	if err := a.persist(); err != nil {
		delete(host.productPending, ct.ID)
		ct.setSendError(a.connectionSafeMessage(err), "", "")
		return false
	}
	captureSubmittedDraft(ct, draft)
	ct.sendNotice = ""
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
			if a.pi != host || a.contactByID(ct.ID) != ct || ct.SessionID != pending.ConversationID || host.productPending[ct.ID] != pending {
				return
			}
			if conversationSendReceiptMatches(pending, receipt) && a.applyConversationSendReceipt(host, ct, receipt, true) {
				if ct.SendState == sendQueued {
					a.reconcilePendingConversationSend(host, ct, true)
				}
				return
			}
			if rejected {
				delete(host.productPending, ct.ID)
				_ = a.persist()
				ct.SendState = sendIdle
				if ct.applyProviderSendRejection(providerAdmission(receipt, remote)) {
					a.refreshProductProviders(host)
				} else {
					ct.setSendError(a.connectionSafeMessage(err), "", "")
				}
				return
			}
			markUncertainConversationSend(ct)
		})
	}()
	return true
}

func captureSubmittedDraft(ct *contact, draft pi.ProductDraft) {
	ct.SubmittedDraft, ct.SubmittedRev = draft.Text, ct.draftRev
	ct.SubmittedQuote = cloneQuote(draft.Quote)
	ct.submittedMaterials = productTaskContext(draft.Materials)
}

func conversationSendReceiptMatches(pending productPendingSend, receipt pi.ProductReceipt) bool {
	return receipt.OperationID == pending.OperationID && receipt.Target.ConversationID == pending.ConversationID
}

func setQueuedConversationSend(ct *contact, receipt pi.ProductReceipt) bool {
	if receipt.State != "pending" || receipt.Execution == nil || receipt.Execution.ExecutionID == "" {
		return false
	}
	result, _ := pi.DecodeConversationSendResult(receipt)
	ct.SendState, ct.Status = sendQueued, statusReady
	ct.SendQueuedPosition, ct.ActiveTaskID = result.QueuePosition, receipt.Execution.ExecutionID
	ct.clearSendError()
	return true
}

func applyNonDispatchedSend(ct *contact, receipt pi.ProductReceipt) bool {
	result, err := pi.DecodeConversationSendResult(receipt)
	if err != nil || result.Delivery != "non_dispatched" {
		return false
	}
	if result.Reason != "cancelled_before_dispatch" && result.Reason != "interrupted_before_dispatch" {
		return false
	}
	applyProductExecution(ct, nil)
	ct.clearSendError()
	ct.sendNotice = result.Reason
	return true
}

func markUncertainConversationSend(ct *contact) {
	ct.ActiveTaskID, ct.SendQueuedPosition = "", 0
	ct.SendState, ct.Status = sendUnknown, statusAttention
	ct.setSendError(taskContextUncertainSendError, "send-uncertain", "")
}

func (a *appModel) applyConversationSendReceipt(host *piAppHost, ct *contact, receipt pi.ProductReceipt, recoverAccepted bool) bool {
	switch receipt.State {
	case "pending":
		return setQueuedConversationSend(ct, receipt)
	case "accepted":
		if receipt.Execution == nil || !recoverAccepted {
			return true
		}
		ct.clearSendError()
		applyProductExecution(ct, receipt.Execution)
		if receipt.Execution.State == "completed" {
			a.recordBotCompletion(ct)
		}
	case "rejected":
		if !applyNonDispatchedSend(ct, receipt) {
			applyProductExecution(ct, nil)
			if ct.applyProviderSendRejection(providerAdmission(receipt, nil)) {
				a.refreshProductProviders(host)
			} else {
				ct.setSendError("The conversation was not dispatched.", "", "")
			}
		}
	default:
		return false
	}
	delete(host.productPending, ct.ID)
	_ = a.persist()
	return true
}

func (a *appModel) reconcilePendingConversationSend(host *piAppHost, ct *contact, recoverAccepted bool) {
	pending, ok := host.productPending[ct.ID]
	if !ok || pending.ConversationID != ct.SessionID || ct.SendState == sendSubmitting {
		return
	}
	ct.sendReceiptRead++
	read := ct.sendReceiptRead
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		receipt, err := host.product.Operation(ctx, pending.OperationID)
		if err != nil || !conversationSendReceiptMatches(pending, receipt) {
			return
		}
		if recoverAccepted && receipt.State == "accepted" && receipt.Execution != nil {
			conversation, err := host.product.GetConversation(ctx, pending.ConversationID)
			if err == nil && conversation.LatestExecution != nil && conversation.LatestExecution.ExecutionID == receipt.Execution.ExecutionID {
				receipt.Execution = conversation.LatestExecution
			}
		}
		a.updateUI(func() {
			if a.pi != host || a.contactByID(ct.ID) != ct || ct.SessionID != pending.ConversationID || host.productPending[ct.ID] != pending || ct.sendReceiptRead != read {
				return
			}
			a.applyConversationSendReceipt(host, ct, receipt, recoverAccepted)
			if recoverAccepted && receipt.State == "accepted" {
				a.refreshProductConversation(host, ct, ct.SubmittedRev)
				a.reloadProductConversation(host, ct)
			}
		})
	}()
}

func (a *appModel) stopProductConversation(host *piAppHost, ct *contact, execution string) {
	if ct == nil || execution == "" || ct.ActiveTaskID != execution || ct.stoppingQueued == execution {
		return
	}
	id := ct.SessionID
	queued := ct.SendState == sendQueued
	if queued {
		ct.stoppingQueued = execution
		a.submissionAsyncStarted()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := host.product.StopConversation(ctx, pi.ProductConversationStopParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: id}, ExecutionID: execution, OperationID: newContactID(),
		})
		a.updateUI(func() {
			if queued {
				defer a.submissionAsyncFinished()
				if ct.stoppingQueued == execution {
					ct.stoppingQueued = ""
				}
			}
			if a.pi != host || ct.SessionID != id || ct.ActiveTaskID != execution {
				return
			}
			if queued {
				a.reconcilePendingConversationSend(host, ct, false)
			}
			if err != nil {
				ct.setSendError(a.connectionSafeMessage(err), "", "")
			}
		})
	}()
}

func (a *appModel) reloadProductConversation(host *piAppHost, ct *contact) {
	a.refreshProductTimeline(host, ct, true)
}

func (a *appModel) refreshProductTimeline(host *piAppHost, ct *contact, resolveDraftQuote bool) {
	if !a.canReloadPiConversation(ct) {
		return
	}
	ct.historyGen++
	generation, id := ct.historyGen, ct.SessionID
	a.historyAsyncStarted()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		projection, err := productTimeline(ctx, host.product, id)
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
			ct.Messages = mergeLiveConversation(ct.Messages, projection.messages)
			ct.History = projection.entries
			ct.transcriptError = projection.transcriptError
			ct.Loaded, ct.LoadErr, ct.Unavailable = true, "", ""
			if resolveDraftQuote {
				a.updateDraftQuoteReference(ct)
			}
		})
	}()
}

func mergeLiveConversation(previous, history []*message) []*message {
	position := 0
	for i := 0; i < len(previous); i++ {
		user := previous[i]
		if user.Kind == msgNotice {
			continue
		}
		if user.TaskID == "" {
			for position < len(history) && history[position].Kind == msgNotice {
				position++
			}
			if position < len(history) && history[position].MessageID == user.MessageID {
				position++
			}
			continue
		}
		if user.Kind != msgUser {
			continue
		}
		var reply *message
		if i+1 < len(previous) && previous[i+1].TaskID == user.TaskID && previous[i+1].Kind == msgContact {
			i++
			reply = previous[i]
		}
		for position < len(history) && history[position].Kind == msgNotice {
			notice := history[position].Notice
			if notice != nil && notice.AnchorKind == pi.ProductAnchorPostSettlement && notice.AnchorExecutionID == user.TaskID {
				break
			}
			position++
		}
		if position == len(history) || history[position].Kind == msgNotice {
			history = slices.Insert(history, position, user)
			position++
		} else {
			stored := history[position]
			if stored.Kind != msgUser || stored.Text != user.Text || (user.MessageID != "" && stored.MessageID != user.MessageID) {
				continue
			}
			stored.Key, stored.TaskID = user.Key, user.TaskID
			stored.ReadByBot = stored.ReadByBot || user.ReadByBot || stored.History
			position++
		}
		if reply == nil {
			continue
		}
		if position < len(history) && history[position].Kind == msgContact {
			stored := history[position]
			if !strings.HasPrefix(reply.Text, stored.Text) {
				position++
				continue
			}
			if stored.Text == reply.Text {
				reply.MessageID = stored.MessageID
			}
			history[position] = reply
		} else {
			history = slices.Insert(history, position, reply)
		}
		position++
	}
	return history
}

func finishLiveReply(ct *contact, executionID string) {
	for i := len(ct.Messages) - 1; i >= 0; i-- {
		item := ct.Messages[i]
		if item.TaskID == executionID && item.Kind == msgContact {
			item.Streaming = false
			return
		}
	}
}

func productHistory(ctx context.Context, client *pi.ProductClient, id string) (historyProjection, error) {
	params := pi.ProductHistoryParams{ProductConversationParams: pi.ProductConversationParams{ConversationID: id}}
	var projection historyProjection
	var reply strings.Builder
	for {
		page, err := client.History(ctx, params)
		if err != nil {
			return historyProjection{}, err
		}
		for _, row := range page.Items {
			projection.appendProductMessage(&reply, row.MessageID, row.Role, row.Timestamp, row.Text, row.Materials, row.Quote)
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
	if changed := event.TimelineChanged; changed != nil {
		for _, ct := range a.contacts {
			if ct.SessionID == changed.ConversationID {
				a.refreshProductTimeline(host, ct, false)
				break
			}
		}
		if browser := a.priorChats; browser != nil && browser.selectedID == changed.ConversationID {
			a.refreshProductTranscript(browser)
		}
		return
	}
	if changed := event.OrchestrationChanged; changed != nil {
		a.applyOrchestrationChange(host, *changed)
		return
	}
	if digest := event.ActivityDigest; digest != nil {
		a.applyActivityDigest(host, *digest)
		return
	}
	if trace := event.TraceUpdated; trace != nil {
		a.applyActivityTraceUpdated(host, *trace)
		return
	}
	if changed := event.SessionChanged; changed != nil {
		a.sessions.applySessionChanged(*changed)
		return
	}
	if updated := event.SessionOutputUpdated; updated != nil {
		a.sessions.applySessionOutputUpdated(*updated)
		return
	}
	if watchChanged := event.WatchChanged; watchChanged != nil {
		a.schedules.applyWatchChanged(*watchChanged)
		return
	}
	if event.Name == "provider.changed" {
		a.refreshProductProviders(host)
		return
	}
	if change := event.SkillChanged; change != nil {
		if change.Request != nil {
			a.applySkillApprovalRequests([]pi.ProductSkillApprovalRequest{*change.Request})
		}
		a.refreshCapabilitiesSkills()
		return
	}
	if event.McpChanged != nil {
		a.refreshCapabilityServers()
		return
	}
	if event.Name == "memory.changed" {
		var changed pi.ProductMemoryChangedEvent
		if json.Unmarshal(event.Data, &changed) == nil {
			if ct := a.contactByID(changed.BotID); ct != nil {
				ct.PendingLearnings = changed.PendingCount
				if a.contactProfileID == ct.ID || ct.memory.loaded || ct.memory.loading || ct.memory.loadFailed {
					a.loadBotMemory(ct)
				}
			}
		}
		return
	}
	if event.Name == "bot.changed" {
		var data pi.ProductBotChangedEvent
		if json.Unmarshal(event.Data, &data) != nil || data.Bot.BotID == "" {
			return
		}
		switch data.Change {
		case "created", "updated", "archived", "restored":
			known := a.contactByID(data.Bot.BotID) != nil
			ct := a.applyProductBot(host, data.Bot, data.Change == "created")
			if !known && data.Change != "created" {
				a.refreshProductConversation(host, ct, ct.draftRev)
				a.reloadProductConversation(host, ct)
			}
		}
		a.refreshDockMenu()
		return
	}
	if event.Name == "activity.changed" {
		if event.ActivityChanged != nil {
			a.applyActivitySkillApprovals(event.ActivityChanged)
		}
		var item pi.ProductActivitySummary
		if json.Unmarshal(event.Data, &item) == nil && item.ActivityID != "" {
			host.activityRevision++
			a.applyBotActivityStatus(item)
		}
		return
	}
	var data pi.ProductExecutionEvent
	if json.Unmarshal(event.Data, &data) != nil {
		return
	}
	if event.Name == "execution.settled" && a.hasEffectiveProviderPool() {
		a.refreshProductProviders(host)
	}
	if data.ActivityID != "" && (event.Name == "execution.started" || event.Name == "execution.settled") {
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
			pending, waiting := host.productPending[ct.ID]
			if ct.ActiveTaskID != data.ExecutionID && (!waiting || pending.OperationID != data.OperationID) {
				return
			}
			ct.clearSendError()
			applyProductExecution(ct, &pi.ProductExecution{ExecutionID: data.ExecutionID, State: "running"})
			if waiting && pending.OperationID == data.OperationID {
				delete(host.productPending, ct.ID)
				_ = a.persist()
			}
			if ct.SubmittedDraft != "" {
				stamp := time.Now()
				ct.Messages = append(ct.Messages, &message{Key: data.ExecutionID + ":user", Kind: msgUser, Text: ct.SubmittedDraft, Time: stamp.Format("15:04"), Timestamp: stamp, TaskID: data.ExecutionID, Context: ct.submittedMaterials, Quote: ct.SubmittedQuote, ReadByBot: true})
				ct.SubmittedDraft, ct.SubmittedQuote, ct.submittedMaterials = "", nil, nil
			} else if waiting {
				a.reloadProductConversation(host, ct)
			}
		case "execution.textDelta":
			if ct.ActiveTaskID == data.ExecutionID {
				item := a.liveText(ct, data.ExecutionID, "pi")
				if uint64(len(item.Text)) == data.Offset {
					item.Text += data.Text
					item.Streaming = true
				}
			}
		case "execution.settled":
			if ct.SendState == sendQueued && ct.ActiveTaskID == data.ExecutionID {
				a.reconcilePendingConversationSend(host, ct, true)
				return
			}
			if ct.ActiveTaskID == data.ExecutionID {
				finishLiveReply(ct, data.ExecutionID)
				applyProductExecution(ct, &pi.ProductExecution{ExecutionID: data.ExecutionID, State: data.State, Error: data.Error})
				if data.State == "completed" {
					a.recordBotCompletion(ct)
				}
				a.reloadProductConversation(host, ct)
			}
		case "conversation.changed":
			a.refreshProductConversation(host, ct, ct.SubmittedRev)
		}
		return
	}
}

func (a *appModel) applyProductBot(host *piAppHost, bot pi.ProductBot, created bool) *contact {
	ct := a.contactByID(bot.BotID)
	if ct == nil {
		ct = productContact(bot)
		ct.Loaded = created
		ct.Context.OnChange = func() error { return a.saveProductDraft(ct) }
		host.productBots[ct.ID] = bot
		host.productDrafts[ct.ID] = pi.ProductDraft{}
		if ct.isBuiltin() {
			selected := a.selectedID()
			a.contacts = slices.Insert(a.contacts, 0, ct)
			if selected != "" {
				a.selected++
			}
		} else {
			a.contacts = append(a.contacts, ct)
		}
		return ct
	}
	if bot.ProfileRevision <= ct.ProfileRevision {
		return ct
	}
	ct.applyProductBotProfile(bot)
	host.productBots[ct.ID] = bot
	if editor := a.profileEditorFor(ct.ID); editor != nil {
		editor.Archived = ct.Archived
		current := contactProfileDraft(ct)
		if profileDraftEqual(editor.Draft, editor.initial) {
			if !profileDraftEqual(editor.initial, current) {
				editor.setProfile(current)
			}
			editor.profileRevision = ct.ProfileRevision
		} else if profileDraftEqual(editor.initial, current) {
			editor.profileRevision = ct.ProfileRevision
		}
	}
	if ct.SessionID != bot.CurrentConversationID {
		ct.SessionID = bot.CurrentConversationID
		ct.Messages, ct.History, ct.Loaded = nil, nil, false
		ct.recall = quoteRecall{}
		ct.SubmittedQuote = nil
		ct.SubmittedDraft, ct.submittedMaterials, ct.sendNotice, ct.stoppingQueued = "", nil, "", ""
		ct.transcriptError = false
		ct.LoadErr, ct.Unavailable = "", ""
		applyProductExecution(ct, nil)
		a.refreshProductConversation(host, ct, ct.draftRev)
		a.reloadProductConversation(host, ct)
	}
	return ct
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
				applyProductDraft(ct, value.Draft)
				a.updateDraftQuoteReference(ct)
			}
		})
	}()
}

func (a *appModel) reconcileProductStream(host *piAppHost) {
	if a.pi != host {
		return
	}
	a.refreshProductProviders(host)
	a.refreshCapabilitiesSkills()
	a.refreshCapabilityServers()
	if ct := a.contactByID(a.contactProfileID); ct != nil && a.detail == detailProfile {
		a.refreshSkillApprovalRequests(ct)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		opened, err := host.product.Open(ctx, []int{pi.ProductVersion})
		if err != nil {
			return
		}
		bots, err := listProductBots(ctx, host.product, opened.Snapshot)
		if err != nil {
			return
		}
		a.updateUI(func() {
			if a.pi == host {
				host.activityRevision++
				for _, bot := range bots {
					ct := a.applyProductBot(host, bot, false)
					ct.PendingLearnings = bot.PendingLearnings
					if a.contactProfileID == ct.ID || ct.memory.loaded || ct.memory.loading || ct.memory.loadFailed {
						a.loadBotMemory(ct)
					}
				}
				for _, ct := range a.contacts {
					a.refreshProductConversation(host, ct, ct.draftRev)
					a.reloadProductConversation(host, ct)
					a.reconcilePendingConversationSend(host, ct, true)
					for _, trace := range ct.activity.traces {
						trace.client = nil
					}
					if !ct.Archived {
						a.refreshBotActivityStatus(host, ct)
						a.refreshBotOrchestrations(host, ct)
					}
				}
				a.reconcileControlPlaneIntents(host)
				a.sessions.blocked = a.dataRecoveryBlocksChanges()
				a.sessions.setSessionInspectorClient(host.product)
				a.sessions.refreshSessionInspection()
				for _, b := range a.schedules.bots {
					b.refresh()
				}
				a.reconcilePendingWatchOperations(host)
				a.reconcileOrchestrationOperations(host)
				if panel := a.backgroundWork.panel; panel != nil {
					a.loadBackgroundWork(panel, "")
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
			if a.windowHidden {
				a.reopenWindow()
			}
			return
		}
		a.pi, a.quitReady = nil, true
		a.refreshDockMenu()
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
		a.sessions.setSessionInspectorClient(nil)
		for _, b := range a.schedules.bots {
			b.client = nil
		}
		a.refreshDockMenu()
		for _, ct := range a.contacts {
			if ct.ActiveTaskID != "" || ct.SendState == sendSubmitting {
				if ct.ActiveTaskID != "" {
					finishLiveReply(ct, ct.ActiveTaskID)
				}
				markUncertainConversationSend(ct)
			}
		}
	})
}

func productContact(bot pi.ProductBot) *contact {
	ct := &contact{ID: bot.BotID, SessionID: bot.CurrentConversationID, Status: statusReady}
	ct.applyProductBotProfile(bot)
	ct.PendingLearnings = bot.PendingLearnings
	return ct
}

func (ct *contact) applyProductBotProfile(bot pi.ProductBot) {
	profile := bot.Profile
	ct.Name, ct.Role, ct.Purpose = profile.Name, profile.Role, profile.Purpose
	ct.Instructions, ct.Workspace = profile.Instructions, profile.Workspace
	ct.StarterPrompts = profile.StarterPrompts
	ct.ToolPreset, ct.MountedSkills, ct.McpBindings = profile.ToolPreset, profile.MountedSkills, profile.McpBindings
	ct.BuiltinTools = nil
	if profile.CodingTools != nil {
		ct.BuiltinTools = &profile.CodingTools
	}
	ct.Archived, ct.SystemKind, ct.ProfileRevision = bot.Archived, bot.SystemKind, bot.ProfileRevision
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
	ct.SendQueuedPosition = 0
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

func (a *appModel) drainPiHost() {
	if a.shutdownStarted {
		a.finishShutdown()
		return
	}
	a.beginShutdown()
}
