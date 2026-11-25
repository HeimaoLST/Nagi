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

	"github.com/HeimaoLST/Nagi/internal/pi"
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
		}
		if pending, ok := host.productPending[ct.ID]; ok && pending.ConversationID == ct.SessionID {
			receipt, lookupErr := host.product.Operation(ctx, pending.OperationID)
			if lookupErr == nil && receipt.Target.ConversationID == ct.SessionID && receipt.State != "pending" && receipt.State != "unknown" {
				delete(host.productPending, ct.ID)
				if receipt.State == "rejected" {
					ct.applyProviderSendRejection(providerAdmission(receipt, nil))
				}
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
		a.hideBuiltinArchitect = false
		a.applyProductPreferences(opened.Snapshot.Preferences)
		a.selected = slices.Index(contacts, selected)
		a.activityIntents = nil
		if local != nil {
			a.activityIntents = local.PendingActivities
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
		PendingActivities:    a.activityIntents,
		HideBuiltinArchitect: a.hideBuiltinArchitect,
	})
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
	pending := productPendingSend{ConversationID: ct.SessionID, OperationID: newContactID()}
	host.productPending[ct.ID] = pending
	if err := a.persist(); err != nil {
		delete(host.productPending, ct.ID)
		ct.setSendError(a.connectionSafeMessage(err), "", "")
		return false
	}
	draft := host.productDrafts[ct.ID]
	ct.SubmittedDraft, ct.SubmittedRev = draft.Text, ct.draftRev
	ct.SubmittedQuote = cloneQuote(draft.Quote)
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
					if receipt.Execution.State == "completed" {
						a.recordBotCompletion(ct)
					}
				}
				delete(host.productPending, ct.ID)
				_ = a.persist()
				return
			}
			if rejected || receipt.State == "rejected" {
				delete(host.productPending, ct.ID)
				_ = a.persist()
				ct.SendState = sendIdle
				if ct.applyProviderSendRejection(providerAdmission(receipt, remote)) {
					a.refreshProductProviders(host)
				} else if rejected {
					ct.setSendError(a.connectionSafeMessage(err), "", "")
				} else {
					ct.setSendError("The conversation was not dispatched.", "", "")
				}
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
			ct.Messages = mergeLiveConversation(ct.Messages, projection.messages)
			ct.History = projection.entries
			ct.Loaded, ct.LoadErr, ct.Unavailable = true, "", ""
			a.updateDraftQuoteReference(ct)
		})
	}()
}

func mergeLiveConversation(previous, history []*message) []*message {
	position := 0
	for i := 0; i < len(previous); i++ {
		user := previous[i]
		if user.TaskID == "" {
			if position < len(history) && history[position].MessageID == user.MessageID {
				position++
			}
			continue
		}
		if user.Kind != msgUser {
			continue
		}
		if position == len(history) {
			return append(history, previous[i:]...)
		}
		var reply *message
		if i+1 < len(previous) && previous[i+1].TaskID == user.TaskID && previous[i+1].Kind == msgContact {
			i++
			reply = previous[i]
		}
		stored := history[position]
		if stored.Kind != msgUser || stored.Text != user.Text || (user.MessageID != "" && stored.MessageID != user.MessageID) {
			continue
		}
		stored.Key, stored.TaskID = user.Key, user.TaskID
		position++
		if reply == nil {
			continue
		}
		end, remaining := position, reply.Text
		for end < len(history) && history[end].Kind == msgContact {
			var ok bool
			remaining, ok = strings.CutPrefix(remaining, history[end].Text)
			if !ok {
				break
			}
			end++
		}
		if end < len(history) && history[end].Kind == msgContact {
			continue
		}
		if end == position+1 && remaining == "" {
			reply.MessageID = history[position].MessageID
		}
		history = slices.Replace(history, position, end, reply)
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
			kind, from := msgContact, "contact"
			if row.Role == "user" {
				kind, from = msgUser, "me"
			}
			stamp, _ := time.Parse(time.RFC3339Nano, row.Timestamp)
			when := formatMillis(stamp.UnixMilli())
			materials := productTaskContext(row.Materials)
			messages := projection.messages
			if kind == msgContact && len(messages) > 0 && messages[len(messages)-1].Kind == msgContact {
				previous := messages[len(messages)-1]
				if reply.Len() == 0 {
					reply.WriteString(previous.Text)
				}
				reply.WriteString(row.Text)
				previous.Text = reply.String()
			} else {
				reply.Reset()
				projection.messages = append(messages, &message{Key: row.MessageID, MessageID: row.MessageID, Kind: kind, Text: row.Text, Time: when, Timestamp: stamp, Context: materials, Quote: row.Quote, History: true})
			}
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
			if ct.ActiveTaskID != data.ExecutionID && host.productPending[ct.ID].OperationID != data.OperationID {
				return
			}
			applyProductExecution(ct, &pi.ProductExecution{ExecutionID: data.ExecutionID, State: "running"})
			stamp := time.Now()
			ct.Messages = append(ct.Messages, &message{Key: data.ExecutionID + ":user", Kind: msgUser, Text: ct.SubmittedDraft, Time: stamp.Format("15:04"), Timestamp: stamp, TaskID: data.ExecutionID, Context: productTaskContext(host.productDrafts[ct.ID].Materials), Quote: ct.SubmittedQuote})
			ct.SubmittedQuote = nil
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
				editor.SetProfile(current)
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
					if !ct.Archived {
						a.refreshBotActivityStatus(host, ct)
					}
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
				if ct.ActiveTaskID != "" {
					finishLiveReply(ct, ct.ActiveTaskID)
				}
				ct.ActiveTaskID, ct.SendState, ct.Status = "", sendUnknown, statusAttention
				ct.setSendError(taskContextUncertainSendError, "send-uncertain", "")
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
