package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

type botOrchestrationState struct {
	items []pi.ProductOrchestrationSummary
	read  uint64
}

type backgroundWorkPanel struct {
	bot          *contact
	opener       string
	returnChat   *productConversationBrowser
	detailID     string
	listOpener   string
	focusBack    bool
	read         uint64
	loading      bool
	failed       bool
	items        []pi.ProductOrchestrationSummary
	next         *string
	detail       *pi.ProductOrchestrationDetail
	listScroll   ui.ScrollState
	detailScroll ui.ScrollState
}

type orchestrationTarget struct {
	botID, mandateID, decisionID string
}

type orchestrationDecisionDraft struct {
	optionID, note string
}

type backgroundWorkState struct {
	panel       *backgroundWorkPanel
	returnFocus string
	operations  map[string]store.PendingOrchestrationOperation
	checking    map[string]bool
	problems    map[orchestrationTarget]string
	drafts      map[orchestrationTarget]*orchestrationDecisionDraft
}

func newBackgroundWorkState(operations map[string]store.PendingOrchestrationOperation) backgroundWorkState {
	state := backgroundWorkState{
		operations: make(map[string]store.PendingOrchestrationOperation), checking: make(map[string]bool),
		problems: make(map[orchestrationTarget]string), drafts: make(map[orchestrationTarget]*orchestrationDecisionDraft),
	}
	maps.Copy(state.operations, operations)
	return state
}

func listBotActiveOrchestrations(ctx context.Context, client *pi.ProductClient, botID string) ([]pi.ProductOrchestrationSummary, error) {
	params := pi.ProductOrchestrationListParams{BotID: botID, States: []pi.ProductMandateState{
		pi.ProductMandateActive, pi.ProductMandateWaitingDecision, pi.ProductMandateRestoredReadOnly,
	}}
	var items []pi.ProductOrchestrationSummary
	for {
		page, err := client.ListOrchestrations(ctx, params)
		if err != nil {
			if params.Cursor == "" && unsupportedOrchestrationMethod(err) {
				return nil, nil
			}
			return nil, err
		}
		for _, item := range page.Items {
			if item.OwnerBotID == botID {
				items = append(items, item)
			}
		}
		if page.NextCursor == nil {
			return items, nil
		}
		params.Cursor = *page.NextCursor
	}
}

func (a *appModel) refreshBotOrchestrations(host *piAppHost, ct *contact) {
	ct.orchestration.read++
	read, botID := ct.orchestration.read, ct.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		items, err := listBotActiveOrchestrations(ctx, host.product, botID)
		if err != nil {
			return
		}
		a.updateUI(func() {
			if a.pi == host && a.contactByID(botID) == ct && ct.orchestration.read == read {
				ct.orchestration.items = items
			}
		})
	}()
}

func (a *appModel) applyOrchestrationChange(host *piAppHost, change pi.ProductOrchestrationChange) {
	if change.Change == "queue_updated" {
		for _, ct := range a.contacts {
			if ct.SendState == sendQueued {
				a.reconcilePendingConversationSend(host, ct, false)
			}
		}
	}
	if ct := a.contactByID(change.BotID); ct != nil {
		a.refreshBotOrchestrations(host, ct)
	}
	if panel := a.backgroundWork.panel; panel != nil && panel.bot.ID == change.BotID &&
		(panel.detailID == "" || change.MandateID == "" || panel.detailID == change.MandateID) {
		a.loadBackgroundWork(panel, "")
	}
}

func (a *appModel) openBackgroundWork(botID, mandateID, opener string) {
	ct := a.contactByID(botID)
	if ct == nil || !a.productProfileReady() || a.dataRecoveryBlocksChanges() {
		return
	}
	var returnChat *productConversationBrowser
	if strings.HasPrefix(opener, "notice:") {
		returnChat = a.priorChats
	}
	a.openContactProfile(botID)
	panel := &backgroundWorkPanel{bot: ct, detailID: mandateID, opener: opener, focusBack: true, returnChat: returnChat}
	a.backgroundWork.panel, a.backgroundWork.returnFocus = panel, ""
	a.loadBackgroundWork(panel, "")
}

func (a *appModel) loadBackgroundWork(panel *backgroundWorkPanel, cursor string) {
	if !a.productProfileReady() || a.dataRecoveryBlocksChanges() {
		return
	}
	client := a.productHost().product
	botID, mandateID := panel.bot.ID, panel.detailID
	panel.read++
	read := panel.read
	panel.loading, panel.failed = true, false
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		var page pi.ProductPage[pi.ProductOrchestrationSummary]
		var detail pi.ProductOrchestrationDetail
		var err error
		if mandateID == "" {
			page, err = client.ListOrchestrations(ctx, pi.ProductOrchestrationListParams{BotID: botID, Cursor: cursor})
			if cursor == "" && unsupportedOrchestrationMethod(err) {
				err = nil
			}
		} else {
			detail, err = client.GetOrchestration(ctx, botID, mandateID)
			if err == nil && (detail.OwnerBotID != botID || detail.MandateID != mandateID) {
				err = errors.New("the background work belongs to another target")
			}
		}
		a.updateUI(func() {
			if a.backgroundWork.panel != panel || panel.read != read || !a.currentActivityClient(panel.bot, client) {
				return
			}
			panel.loading, panel.failed = false, err != nil
			if err != nil {
				return
			}
			if mandateID != "" {
				panel.detail = &detail
				return
			}
			if cursor == "" {
				panel.items = panel.items[:0]
			}
			for _, item := range page.Items {
				if item.OwnerBotID != botID {
					continue
				}
				index := slices.IndexFunc(panel.items, func(previous pi.ProductOrchestrationSummary) bool { return previous.MandateID == item.MandateID })
				if index < 0 {
					panel.items = append(panel.items, item)
				} else {
					panel.items[index] = item
				}
			}
			panel.next = page.NextCursor
		})
	}()
}

func (a *appModel) backBackgroundWork() {
	panel := a.backgroundWork.panel
	if panel.listOpener != "" && panel.detailID != "" {
		panel.detailID, panel.detail = "", nil
		panel.read++
		panel.loading, panel.failed = false, false
		a.backgroundWork.returnFocus = panel.listOpener
		return
	}
	if panel.opener == "profile-background:"+panel.bot.ID {
		a.backgroundWork.panel = nil
	} else {
		a.closeDetail()
		a.priorChats = panel.returnChat
		a.focusBotDrawerOpener(panel.bot.ID, panel.opener)
	}
	a.backgroundWork.returnFocus = panel.opener
}

func (a *appModel) focusBackgroundOpener(button ui.Element, opener string) {
	if a.backgroundWork.returnFocus == opener {
		button.Focus().ScrollIntoView()
		a.backgroundWork.returnFocus = ""
	}
}

func (a *appModel) orchestrationReadOnly(ct *contact) bool {
	return ct.Archived || a.shutdownStarted || !a.productProfileReady() || a.dataRecoveryBlocksChanges()
}

func targetOfOperation(op store.PendingOrchestrationOperation) orchestrationTarget {
	return orchestrationTarget{op.BotID, op.MandateID, op.DecisionID}
}

func (s *backgroundWorkState) pending(target orchestrationTarget) store.PendingOrchestrationOperation {
	for _, op := range s.operations {
		if targetOfOperation(op) == target {
			return op
		}
	}
	return store.PendingOrchestrationOperation{}
}

func (a *appModel) startOrchestrationOperation(ct *contact, target orchestrationTarget, call func(context.Context, *pi.ProductClient, string) (pi.ProductReceipt, error)) {
	s := &a.backgroundWork
	if a.orchestrationReadOnly(ct) || ct.ID != target.botID || s.pending(target).OperationID != "" {
		return
	}
	op := store.PendingOrchestrationOperation{BotID: target.botID, MandateID: target.mandateID, DecisionID: target.decisionID, OperationID: newContactID()}
	s.operations[op.OperationID] = op
	delete(s.problems, target)
	if err := a.persist(); err != nil {
		delete(s.operations, op.OperationID)
		s.problems[target] = a.connectionSafeMessage(err)
		return
	}
	a.readOrchestrationOperation(a.productHost(), op, call)
}

func (a *appModel) reconcileOrchestrationOperations(host *piAppHost) {
	for _, op := range a.backgroundWork.operations {
		a.readOrchestrationOperation(host, op, nil)
	}
}

func (a *appModel) readOrchestrationOperation(host *piAppHost, op store.PendingOrchestrationOperation, call func(context.Context, *pi.ProductClient, string) (pi.ProductReceipt, error)) {
	s := &a.backgroundWork
	if s.checking[op.OperationID] {
		return
	}
	s.checking[op.OperationID] = true
	if call != nil {
		a.submissionAsyncStarted()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		var receipt pi.ProductReceipt
		var err error
		var remote *pi.RemoteError
		if call != nil {
			receipt, err = call(ctx, host.product, op.OperationID)
		}
		rejected := call != nil && errors.As(err, &remote) && remote.Code != "operation_conflict"
		if !rejected && (call == nil || !settledOrchestrationReceipt(op, receipt)) {
			lookup, stop := context.WithTimeout(context.Background(), 4*time.Second)
			receipt, _ = host.product.Operation(lookup, op.OperationID)
			stop()
		}
		a.updateUI(func() {
			if call != nil {
				defer a.submissionAsyncFinished()
			}
			if a.pi != host || s.operations[op.OperationID] != op {
				return
			}
			delete(s.checking, op.OperationID)
			if !rejected && !settledOrchestrationReceipt(op, receipt) {
				return
			}
			target := targetOfOperation(op)
			delete(s.operations, op.OperationID)
			_ = a.persist()
			if rejected || receipt.State == "rejected" {
				s.problems[target] = locales.In(a.uiLocale).OrchestrationDecisionRejected()
				if rejected {
					s.problems[target] = a.connectionSafeMessage(err)
				}
			} else {
				delete(s.drafts, target)
				delete(s.problems, target)
			}
			a.applyOrchestrationChange(host, pi.ProductOrchestrationChange{BotID: op.BotID, MandateID: op.MandateID})
		})
	}()
}

func settledOrchestrationReceipt(op store.PendingOrchestrationOperation, receipt pi.ProductReceipt) bool {
	if receipt.OperationID != op.OperationID || receipt.Target.BotID != op.BotID ||
		receipt.Target.MandateID != op.MandateID || receipt.Target.DecisionID != op.DecisionID {
		return false
	}
	if receipt.State != "accepted" {
		return receipt.State == "rejected"
	}
	if op.DecisionID != "" {
		result, err := pi.DecodeOrchestrationDecisionResult(receipt)
		return err == nil && result.Mandate.MandateID == op.MandateID && result.Mandate.OwnerBotID == op.BotID &&
			result.Decision.DecisionID == op.DecisionID && result.Decision.MandateID == op.MandateID && result.Decision.OwnerBotID == op.BotID
	}
	result, err := pi.DecodeOrchestrationStopResult(receipt)
	return err == nil && result.Mandate.MandateID == op.MandateID && result.Mandate.OwnerBotID == op.BotID
}

func (a *appModel) submitOrchestrationDecision(ct *contact, item pi.ProductOrchestrationDecision, draft *orchestrationDecisionDraft, outcome pi.ProductOrchestrationDecisionOutcome) {
	if item.Dormant || item.State != "open" || item.OwnerBotID != ct.ID {
		return
	}
	args := pi.ProductOrchestrationDecideParams{
		BotID: ct.ID, MandateID: item.MandateID, DecisionID: item.DecisionID, ExpectedRevision: item.Revision,
		Outcome: outcome, SelectedOptionID: draft.optionID, Text: strings.TrimSpace(draft.note),
	}
	a.startOrchestrationOperation(ct, orchestrationTarget{ct.ID, item.MandateID, item.DecisionID}, func(ctx context.Context, client *pi.ProductClient, opID string) (pi.ProductReceipt, error) {
		args.OperationID = opID
		return client.DecideOrchestration(ctx, args)
	})
}

func orchestrationStoppable(item pi.ProductOrchestrationSummary) bool {
	return !item.Dormant && (item.State == pi.ProductMandateActive || item.State == pi.ProductMandateWaitingDecision || item.State == pi.ProductMandatePaused)
}

func (a *appModel) stopOrchestration(ct *contact, item pi.ProductOrchestrationSummary) {
	if item.OwnerBotID != ct.ID || !orchestrationStoppable(item) {
		return
	}
	args := pi.ProductOrchestrationStopParams{
		BotID: ct.ID, MandateID: item.MandateID, ExpectedRevision: &item.Revision, Generation: &item.Generation, RunID: item.CurrentRunID,
	}
	a.startOrchestrationOperation(ct, orchestrationTarget{ct.ID, item.MandateID, ""}, func(ctx context.Context, client *pi.ProductClient, opID string) (pi.ProductReceipt, error) {
		args.OperationID = opID
		return client.StopOrchestration(ctx, args)
	})
}

func (a *appModel) orchestrationOperationFeedback(c *ui.Context, t *ui.Theme, ct *contact, target orchestrationTarget, pending store.PendingOrchestrationOperation) {
	s, msgs := &a.backgroundWork, locales.In(c.Locale())
	if pending.OperationID != "" {
		working := s.checking[pending.OperationID]
		text := msgs.OrchestrationDecisionUnknown()
		if target.decisionID == "" {
			text = msgs.OrchestrationStopUnknown()
			if working {
				text = msgs.OrchestrationStopping()
			}
		} else if working {
			text = msgs.OrchestrationDecisionPending()
		}
		a.activityText(c, t, text)
		if !working {
			secondaryButton(c, msgs.OrchestrationCheckStatus()).AlignSelf(ui.Start).Disabled(a.orchestrationReadOnly(ct)).OnClick(func() {
				a.readOrchestrationOperation(a.productHost(), pending, nil)
			})
		}
	} else if problem := s.problems[target]; problem != "" {
		text := msgs.OrchestrationDecisionRejected()
		if target.decisionID == "" {
			text = msgs.OrchestrationStopFailed(problem)
		}
		a.activityText(c, t, text)
		secondaryButton(c, msgs.OrchestrationCheckStatus()).AlignSelf(ui.Start).Disabled(!a.productProfileReady()).OnClick(func() {
			delete(s.problems, target)
			a.applyOrchestrationChange(a.productHost(), pi.ProductOrchestrationChange{BotID: ct.ID, MandateID: target.mandateID})
		})
	}
}

func (a *appModel) orchestrationDecisionStrip(c *ui.Context, t *ui.Theme, ct *contact) {
	visible := func(item pi.ProductOrchestrationSummary) bool {
		return item.PendingDecision != nil && item.PendingDecision.OwnerBotID == ct.ID && item.PendingDecision.MandateID == item.MandateID &&
			item.PendingDecision.State == "open"
	}
	if !slices.ContainsFunc(ct.orchestration.items, visible) {
		return
	}
	ui.Scroll(c).Key("orchestration-decisions").FillWidth().MaxHeight(228).Shrink(0).Children(func() {
		ui.Row(c).FillWidth().Justify(ui.Center).Children(func() {
			ui.Column(c).FillWidth().MinWidth(0).MaxWidth(conversationMaxWidth).Padding(4, 16).Gap(8).Children(func() {
				for _, item := range ct.orchestration.items {
					if visible(item) {
						a.orchestrationDecisionCard(c, t, ct, *item.PendingDecision, true)
					}
				}
			})
		})
	})
}

func (a *appModel) orchestrationDecisionCard(c *ui.Context, t *ui.Theme, ct *contact, item pi.ProductOrchestrationDecision, showInspect bool) {
	msgs, s := locales.In(c.Locale()), &a.backgroundWork
	target := orchestrationTarget{ct.ID, item.MandateID, item.DecisionID}
	pending := s.pending(target)
	draft := s.drafts[target]
	if draft == nil {
		if s.drafts == nil {
			s.drafts = make(map[orchestrationTarget]*orchestrationDecisionDraft)
		}
		draft = &orchestrationDecisionDraft{}
		s.drafts[target] = draft
	}
	if draft.optionID != "" && !slices.ContainsFunc(item.Options, func(option pi.ProductOrchestrationDecisionOption) bool { return option.ID == draft.optionID }) {
		draft.optionID = ""
	}
	blocked := item.Dormant || a.orchestrationReadOnly(ct) || pending.OperationID != ""
	opener := "decision:" + item.DecisionID
	ui.Column(c).Key(item.DecisionID).FillWidth().MinWidth(0).Padding(10, 12).Gap(6).Radius(theme.CardRadius).Border(1, t.Border).Background(t.Surface).Children(func() {
		ui.Text(c, msgs.OrchestrationDecisionBadge()).FontSize(typeMeta).FontWeight(500).TextColor(t.Warning)
		orchestrationHeading(c, t, item.Purpose)
		if item.Summary != "" && item.Summary != item.Purpose {
			orchestrationText(c, t, item.Summary)
		}
		if item.Dormant {
			a.activityText(c, t, msgs.OrchestrationDormantNotice())
		}
		if len(item.Options) > 0 {
			ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
				for _, option := range item.Options {
					c.Key(option.ID)
					pill := theme.TagPill(c, option.Label, "", draft.optionID == option.ID, false, blocked)
					if option.Description != "" {
						appTooltip(c, pill, option.Description)
					}
					pill.OnClick(func() { draft.optionID = option.ID })
				}
			})
		}
		if !item.Dormant {
			label := msgs.OrchestrationNotePlaceholder()
			caption := ui.Text(c, label).AlignSelf(ui.Start).FontSize(typeMeta).TextColor(t.TextMuted)
			input := preferenceInput(c, &draft.note, false, a.workTheme.InputBorder).FillWidth().MinWidth(0).Label(label).Disabled(blocked)
			caption.OnClick(func() { input.Focus() })
		}
		ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
			if !item.Dormant {
				primaryButton(c, msgs.OrchestrationApprove()).AlignSelf(ui.Start).Disabled(blocked).OnClick(func() {
					a.submitOrchestrationDecision(ct, item, draft, pi.ProductDecisionApprove)
				})
				secondaryButton(c, msgs.OrchestrationReject()).AlignSelf(ui.Start).Disabled(blocked).OnClick(func() {
					a.submitOrchestrationDecision(ct, item, draft, pi.ProductDecisionReject)
				})
			}
			if showInspect {
				button := theme.TextAction(c, msgs.TimelineInspectMandate(), false).AlignSelf(ui.Start).Disabled(!a.productProfileReady())
				a.focusBackgroundOpener(button, opener)
				button.OnClick(func() { a.openBackgroundWork(ct.ID, item.MandateID, opener) })
			}
		})
		a.orchestrationOperationFeedback(c, t, ct, target, pending)
	})
}

func (a *appModel) backgroundWorkView(c *ui.Context, t *ui.Theme, panel *backgroundWorkPanel) {
	msgs := locales.In(c.Locale())
	ui.Column(c).Key("background-work").Fill().MinWidth(0).MinHeight(0).Background(a.workTheme.Panel).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Padding(8, 12).Shrink(0).Children(func() {
			back := detailBackButton(c, t, msgs.OrchestrationBack(), msgs.OrchestrationBack()).OnClick(a.backBackgroundWork)
			if panel.focusBack {
				back.Focus()
				panel.focusBack = false
			}
			title := msgs.OrchestrationTitle()
			heading := ui.Text(c, title).Role(ui.RoleHeading).Level(1).FontSize(16).FontWeight(600).
				FixedLineHeight(24).TextColor(t.Text).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			appTooltip(c, heading, title)
			secondaryButton(c, msgs.OrchestrationReload()).AlignSelf(ui.Start).Disabled(panel.loading || !a.productProfileReady()).OnClick(func() {
				a.loadBackgroundWork(panel, "")
			})
			if a.detailPageActive() && a.railCollapsed {
				a.showContactsButton(c, t)
			}
		})
		key, scroll := "background-work-list", &panel.listScroll
		if panel.detailID != "" {
			key, scroll = panel.detailID, &panel.detailScroll
		}
		sessionBody(c, key, scroll, func() {
			a.activityText(c, t, panel.bot.Name)
			switch {
			case panel.failed:
				a.activityText(c, t, msgs.OrchestrationLoadFailed())
			case panel.loading && (panel.detailID == "" && len(panel.items) == 0 || panel.detailID != "" && panel.detail == nil):
				a.activityText(c, t, msgs.OrchestrationLoading())
			}
			if panel.detailID != "" {
				if panel.detail != nil {
					a.backgroundWorkDetail(c, t, panel)
				}
				return
			}
			if len(panel.items) == 0 && !panel.loading && !panel.failed {
				a.activityText(c, t, msgs.OrchestrationEmpty())
			}
			for _, item := range panel.items {
				ui.Column(c).Key(item.MandateID).FillWidth().MinWidth(0).Gap(6).Padding(12).Radius(theme.CardRadius).Border(1, t.Border).Children(func() {
					a.backgroundWorkSummary(c, t, item)
					a.activityText(c, t, msgs.OrchestrationMetricsWakes(int(item.Metrics.TotalWakes), int(item.Metrics.Calls), int(item.Metrics.TotalTokens)))
					ui.Row(c).Wrap().Gap(8).Children(func() {
						opener := "mandate:" + item.MandateID
						button := secondaryButton(c, msgs.DetailsAction()).AlignSelf(ui.Start).Label(msgs.DetailsAction() + " " + item.Purpose)
						a.focusBackgroundOpener(button, opener)
						button.OnClick(func() {
							panel.listOpener, panel.detailID, panel.detail, panel.focusBack = opener, item.MandateID, nil, true
							panel.detailScroll = ui.ScrollState{}
							a.loadBackgroundWork(panel, "")
						})
						a.orchestrationStopButton(c, panel.bot, item)
					})
					a.orchestrationStopFeedback(c, t, panel.bot, item)
				})
			}
			if panel.next != nil {
				secondaryButton(c, msgs.OrchestrationLoadMore()).AlignSelf(ui.Start).Disabled(panel.loading).OnClick(func() { a.loadBackgroundWork(panel, *panel.next) })
			}
		})
	})
}

func (a *appModel) backgroundWorkSummary(c *ui.Context, t *ui.Theme, item pi.ProductOrchestrationSummary) {
	msgs := locales.In(c.Locale())
	orchestrationHeading(c, t, item.Purpose)
	ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
		a.activityText(c, t, orchestrationSourceLabel(msgs, item.SourceKind))
		a.activityText(c, t, orchestrationStateLabel(msgs, item.State))
		if item.LastDisposition != "" {
			a.activityText(c, t, orchestrationDispositionLabel(msgs, item.LastDisposition))
		}
	})
	a.activityText(c, t, msgs.OrchestrationVersion(int(item.Generation), int(item.Revision)))
	if item.Dormant {
		a.activityText(c, t, msgs.OrchestrationDormantNotice())
	}
}

func (a *appModel) orchestrationStopButton(c *ui.Context, ct *contact, item pi.ProductOrchestrationSummary) {
	msgs := locales.In(c.Locale())
	pending := a.backgroundWork.pending(orchestrationTarget{ct.ID, item.MandateID, ""})
	theme.DangerButton(c, msgs.OrchestrationStop()).AlignSelf(ui.Start).Label(msgs.OrchestrationStop() + " " + item.Purpose).
		Disabled(a.orchestrationReadOnly(ct) || !orchestrationStoppable(item) || pending.OperationID != "").
		OnClick(func() { a.stopOrchestration(ct, item) })
}

func (a *appModel) orchestrationStopFeedback(c *ui.Context, t *ui.Theme, ct *contact, item pi.ProductOrchestrationSummary) {
	target := orchestrationTarget{ct.ID, item.MandateID, ""}
	a.orchestrationOperationFeedback(c, t, ct, target, a.backgroundWork.pending(target))
}

func (a *appModel) backgroundWorkDetail(c *ui.Context, t *ui.Theme, panel *backgroundWorkPanel) {
	msgs, detail := locales.In(c.Locale()), panel.detail
	item := detail.ProductOrchestrationSummary
	ui.Column(c).FillWidth().MinWidth(0).Gap(6).Children(func() {
		a.backgroundWorkSummary(c, t, item)
		a.orchestrationStopButton(c, panel.bot, item)
		a.orchestrationStopFeedback(c, t, panel.bot, item)
	})
	m := item.Metrics
	ui.Column(c).FillWidth().MinWidth(0).Gap(4).Children(func() {
		orchestrationHeading(c, t, msgs.OrchestrationMetricsTitle())
		a.activityText(c, t, msgs.OrchestrationMetricsWakes(int(m.TotalWakes), int(m.Calls), int(m.TotalTokens)))
		a.activityText(c, t, msgs.OrchestrationMetricsCost(fmt.Sprintf("%.6f", m.CostUSD)))
		a.activityText(c, t, msgs.OrchestrationMetricsDelay(int(m.LastQueueDelayMS), int(m.LastCoalescedCount), int(m.BackoffDelayMS)))
		if m.BackoffReason != "" {
			a.activityText(c, t, msgs.OrchestrationBackoffReason(safeDiagnostic(m.BackoffReason)))
		}
		if stamp, err := time.Parse(time.RFC3339Nano, m.NextEligibleAt); err == nil {
			a.activityText(c, t, msgs.OrchestrationNextEligible(formatChatTimestamp(stamp, c.Now(), c.Locale())))
		}
	})
	if len(detail.Decisions) > 0 {
		orchestrationHeading(c, t, msgs.OrchestrationDecisionsTitle())
		for _, decision := range detail.Decisions {
			if decision.OwnerBotID != panel.bot.ID || decision.MandateID != item.MandateID {
				continue
			}
			if decision.State == "open" {
				a.orchestrationDecisionCard(c, t, panel.bot, decision, false)
				continue
			}
			ui.Column(c).Key(decision.DecisionID).FillWidth().MinWidth(0).Gap(4).Children(func() {
				orchestrationHeading(c, t, decision.Purpose)
				if decision.Outcome == "approve" {
					a.activityText(c, t, msgs.OrchestrationApproved())
				} else if decision.Outcome == "reject" {
					a.activityText(c, t, msgs.OrchestrationRejected())
				}
				if decision.Summary != "" {
					orchestrationText(c, t, decision.Summary)
				}
			})
		}
	}
	if len(detail.RecentRuns) > 0 {
		orchestrationHeading(c, t, msgs.OrchestrationRunsTitle())
		for _, run := range detail.RecentRuns {
			ui.Column(c).Key(run.RunID).FillWidth().MinWidth(0).Gap(4).Children(func() {
				a.activityText(c, t, orchestrationDispositionLabel(msgs, run.Disposition))
				if run.RouteRole != "" {
					a.activityText(c, t, msgs.OrchestrationRouteRole(safeDiagnostic(run.RouteRole)))
				}
				a.activityText(c, t, msgs.OrchestrationMetricsWakes(1, int(run.Calls), int(run.TotalTokens)))
				a.activityText(c, t, msgs.OrchestrationMetricsCost(fmt.Sprintf("%.6f", run.CostUSD)))
				if run.Summary != "" {
					orchestrationText(c, t, run.Summary)
				}
				if run.Error != "" {
					a.activityText(c, t, safeDiagnostic(run.Error))
				}
			})
		}
	}
	if len(detail.RecentReceipts) > 0 {
		orchestrationHeading(c, t, msgs.OrchestrationReceiptsTitle())
		for _, receipt := range detail.RecentReceipts {
			ui.Column(c).Key(receipt.ReceiptID).FillWidth().MinWidth(0).Gap(4).Children(func() {
				orchestrationHeading(c, t, safeDiagnostic(receipt.Tool))
				a.activityText(c, t, safeDiagnostic(receipt.State))
				if receipt.Target != "" {
					orchestrationText(c, t, safeDiagnostic(receipt.Target))
				}
				if receipt.Reason != "" {
					a.activityText(c, t, safeDiagnostic(receipt.Reason))
				}
			})
		}
	}
}

func orchestrationHeading(c *ui.Context, t *ui.Theme, text string) {
	ui.Text(c, text).MinWidth(0).FontSize(typeLabel).FontWeight(500).LineHeight(1.5).TextColor(t.Text)
}

func orchestrationText(c *ui.Context, t *ui.Theme, text string) {
	ui.Text(c, text).MinWidth(0).Selectable().FontSize(typeBody).LineHeight(1.5).TextColor(t.Text)
}

func orchestrationSourceLabel(msgs locales.Messages, source pi.ProductMandateSourceKind) string {
	switch source {
	case pi.ProductMandateSourceWatch:
		return msgs.OrchestrationSourceWatch()
	case pi.ProductMandateSourceTimer:
		return msgs.OrchestrationSourceTimer()
	case pi.ProductMandateSourceSchedule:
		return msgs.OrchestrationSourceSchedule()
	case pi.ProductMandateSourceSession:
		return msgs.SessionInspectSession()
	case pi.ProductMandateSourceUser:
		return msgs.OrchestrationSourceUser()
	default:
		return msgs.OrchestrationStateUnknown()
	}
}

func orchestrationStateLabel(msgs locales.Messages, state pi.ProductMandateState) string {
	switch state {
	case pi.ProductMandateActive:
		return msgs.OrchestrationStateActive()
	case pi.ProductMandateWaitingDecision:
		return msgs.OrchestrationStateWaitingDecision()
	case pi.ProductMandatePaused:
		return msgs.OrchestrationStatePaused()
	case pi.ProductMandateRestored:
		return msgs.OrchestrationStateRestored()
	case pi.ProductMandateRestoredReadOnly:
		return msgs.OrchestrationStateRestoredReadOnly()
	case pi.ProductMandateCompleted:
		return msgs.OrchestrationStateCompleted()
	case pi.ProductMandateStopped:
		return msgs.OrchestrationStateStopped()
	case pi.ProductMandateFailed:
		return msgs.OrchestrationStateFailed()
	default:
		return msgs.OrchestrationStateUnknown()
	}
}

func orchestrationDispositionLabel(msgs locales.Messages, disposition pi.ProductOrchestrationDisposition) string {
	switch disposition {
	case pi.ProductDispositionSilent:
		return msgs.OrchestrationDispositionSilent()
	case pi.ProductDispositionNotified:
		return msgs.OrchestrationDispositionNotified()
	case pi.ProductDispositionNeedsDecision:
		return msgs.OrchestrationDispositionNeedsDecision()
	case pi.ProductDispositionStopped:
		return msgs.OrchestrationDispositionStopped()
	case pi.ProductDispositionFailed:
		return msgs.OrchestrationDispositionFailed()
	default:
		return msgs.OrchestrationDispositionUnknown()
	}
}

func (a *appModel) openDockBackgroundWork(botID string) {
	state := a.currentDockMenuState()
	if state.botID != botID || !state.canSchedule || a.dataRecoveryBlocksChanges() {
		return
	}
	a.closeSettings()
	a.closeAux()
	a.resetComposerOverlays()
	a.openBackgroundWork(botID, "", "header-overview:"+botID)
	a.reopenWindow()
	a.invalidateNativeWindow()
}
