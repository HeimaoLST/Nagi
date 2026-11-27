package app

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

func activityStatusBadge(c *ui.Context, t *ui.Theme, msgs locales.Messages, activity pi.ProductActivitySummary) {
	label, ink := productActivityStateLabel(msgs, activity.State), t.TextMuted
	switch {
	case activity.ClosedAt != "":
		label = msgs.ActivityClosed()
	case activity.State == "waitingForAnswer" || activity.State == "needsAttention":
		ink = t.Warning
	case activity.State == "completed" || activity.State == "running":
		ink = t.Success
	}
	capabilityBadge(c, t, label, ink)
}

func activityProgressView(c *ui.Context, t *ui.Theme, msgs locales.Messages, progress *pi.ProductActivityProgress) {
	if progress == nil || progress.Stage == "" {
		return
	}
	ui.Row(c).MinWidth(0).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
		stage := capabilityText(c, t, progress.Stage).MaxLines(1).Ellipsis("…")
		appTooltip(c, stage, progress.Stage)
		if progress.TotalSteps > 0 {
			capabilityBadge(c, t, msgs.ActivityProgressSteps(int(progress.Step), int(progress.TotalSteps)), t.TextMuted)
		}
	})
}

func activityExecutionLabel(msgs locales.Messages, state string) string {
	switch state {
	case "starting":
		return msgs.WorkStatusStarting()
	case "running":
		return msgs.IssueExecutionRunning()
	case "completed":
		return msgs.IssueExecutionCompleted()
	case "failed":
		return msgs.IssueExecutionFailed()
	case "interrupted":
		return msgs.IssueExecutionInterrupted()
	case "unknown":
		return msgs.IssueExecutionUnknown()
	default:
		return workNoticeText(msgs, state, "")
	}
}

func latestProductExecution(activity *pi.ProductActivity) (pi.ProductExecution, bool) {
	if len(activity.Executions) == 0 {
		return pi.ProductExecution{}, false
	}
	return activity.Executions[len(activity.Executions)-1], true
}

func canAcknowledgeActivity(activity *pi.ProductActivity, execution pi.ProductExecution) bool {
	return activity.State == "completed" && activity.AcknowledgedAt == "" && activity.ClosedAt == "" &&
		execution.ExecutionID != "" && execution.State == "completed" && len(activity.OpenQuestionIDs)+len(activity.PendingApprovalRequestIDs) == 0
}

func canCloseActivity(activity *pi.ProductActivity) bool {
	if activity.ClosedAt != "" || len(activity.OpenQuestionIDs)+len(activity.PendingApprovalRequestIDs) != 0 {
		return false
	}
	execution, ok := latestProductExecution(activity)
	if !ok || execution.State == "starting" || execution.State == "running" {
		return false
	}
	switch activity.State {
	case "completed", "cancelled", "needsAttention":
		return true
	default:
		return activity.State == "unknown" && execution.State == "unknown"
	}
}

func canSteerActivity(p *productActivityPanel, activity *pi.ProductActivity, execution pi.ProductExecution) bool {
	latest, ok := latestProductExecution(activity)
	return ok && latest.ExecutionID == execution.ExecutionID && execution.State == "running" && activity.State == "running" &&
		activity.ClosedAt == "" && p.stopping != execution.ExecutionID && len(activity.OpenQuestionIDs)+len(activity.PendingApprovalRequestIDs) == 0
}

func (a *appModel) activityLifecycleActions(c *ui.Context, ct *contact, activity *pi.ProductActivity, blocked bool) {
	execution, _ := latestProductExecution(activity)
	acknowledge, closeActivity := canAcknowledgeActivity(activity, execution), canCloseActivity(activity)
	if !acknowledge && !closeActivity {
		return
	}
	msgs := locales.In(c.Locale())
	ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).Children(func() {
		if acknowledge {
			target := productActivityIntent{BotID: ct.ID, ActivityID: activity.ActivityID, Action: "acknowledge", ExecutionID: execution.ExecutionID, Revision: activity.Revision}
			theme.TextAction(c, msgs.ActivityAcknowledge(), false).Disabled(blocked).OnClick(func() { a.submitProductActivity(ct, target, nil) })
		}
		if closeActivity {
			target := productActivityIntent{BotID: ct.ID, ActivityID: activity.ActivityID, Action: "close", Revision: activity.Revision}
			theme.TextAction(c, msgs.ActivityClose(), false).Disabled(blocked).OnClick(func() { a.submitProductActivity(ct, target, nil) })
		}
	})
}

func activityAnswerIntent(ct *contact, activity *pi.ProductActivity, question pi.ProductQuestion) productActivityIntent {
	return productActivityIntent{BotID: ct.ID, ActivityID: activity.ActivityID, ExecutionID: question.ExecutionID,
		QuestionID: question.QuestionID, Revision: question.Revision, Action: "answer"}
}

func activityFormQuestion(activity *pi.ProductActivity, target productActivityIntent) *pi.ProductQuestion {
	if target.Action != "answer" {
		return nil
	}
	for i := range activity.Questions {
		question := &activity.Questions[i]
		if question.QuestionID == target.QuestionID && question.ExecutionID == target.ExecutionID {
			return question
		}
	}
	return nil
}

func questionAllowsText(question pi.ProductQuestion) bool {
	return question.AllowFreeText == nil || *question.AllowFreeText
}

func activityQuestionOption(question pi.ProductQuestion, id string) *pi.ProductQuestionOption {
	for i := range question.Options {
		if question.Options[i].ID == id {
			return &question.Options[i]
		}
	}
	return nil
}

func activityOptions(c *ui.Context, msgs locales.Messages, question pi.ProductQuestion, form *productActivityForm, blocked bool, onSelect func()) {
	c.Key("activity-options")
	ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(4).Children(func() {
		for i, option := range question.Options {
			c.Key(option.ID)
			badge := ""
			if option.Recommended {
				badge = msgs.ActivityRecommended()
			}
			pill := theme.TagPill(c, option.Label, badge, form.target.SelectedOptionID == option.ID, true, blocked).MinWidth(theme.ControlHit)
			tip := option.Label
			if option.Description != "" {
				tip += "\n" + option.Description
			}
			appTooltip(c, pill, tip).OnClick(func() {
				form.target.SelectedOptionID, form.text = option.ID, option.Label
				if onSelect != nil {
					onSelect()
				}
			})
			if i == 0 && onSelect != nil && form.focus && !blocked && !questionAllowsText(question) {
				pill.Focus().ScrollIntoView()
				form.focus = false
			}
		}
	})
}

func (a *appModel) validActivityControlTarget(ct *contact, activity *pi.ProductActivity, target productActivityIntent, form *productActivityForm) bool {
	if activity.ActivityID != target.ActivityID || activity.OwnerBotID != ct.ID {
		return false
	}
	execution, ok := latestProductExecution(activity)
	switch target.Action {
	case "answer":
		question := activityFormQuestion(activity, target)
		if form == nil || question == nil || question.State != "open" || question.Revision != target.Revision {
			return false
		}
		if form.target.SelectedOptionID == "" {
			return questionAllowsText(*question) && strings.TrimSpace(form.text) != ""
		}
		option := activityQuestionOption(*question, form.target.SelectedOptionID)
		return option != nil && form.text == option.Label
	case "steer":
		return !ct.Archived && ok && form != nil && strings.TrimSpace(form.text) != "" && execution.ExecutionID == target.ExecutionID &&
			activity.Revision == target.Revision && canSteerActivity(&ct.activity, activity, execution)
	case "acknowledge":
		return execution.ExecutionID == target.ExecutionID && activity.Revision == target.Revision && canAcknowledgeActivity(activity, execution)
	case "close":
		return activity.Revision == target.Revision && canCloseActivity(activity)
	case "continue":
		return form != nil && !ct.Archived && (strings.TrimSpace(form.text) != "" || form.materials.Count() > 0)
	case "stop":
		return ok && (execution.State == "running" || execution.State == "starting") && execution.ExecutionID == target.ExecutionID && ct.activity.stopping != target.ExecutionID
	default:
		return false
	}
}

func activityActionHasForm(action string) bool {
	return action == "answer" || action == "steer" || action == "continue"
}

func activitySteerOutcome(target productActivityIntent, steer pi.ProductActivitySteer) (accepted, rejected bool) {
	if steer.OperationID != target.OperationID || steer.ActivityID != target.ActivityID || steer.ExecutionID != target.ExecutionID {
		return false, false
	}
	switch steer.State {
	case "pending_enqueue", "queued", "delivered", "not_applied", "unknown":
		return steer.SteerID != "", false
	case "rejected":
		return false, true
	}
	return false, false
}

func (a *appModel) reconcileControlPlaneIntents(host *piAppHost) {
	if a.productActivityClient() != host.product || a.dataRecoveryBlocksChanges() {
		return
	}
	for _, ct := range a.contacts {
		p := &ct.activity
		p.recovery = p.recovery[:0]
		for _, target := range a.activityIntents {
			if target.BotID != ct.ID {
				continue
			}
			switch target.Action {
			case "steer", "acknowledge", "close":
				p.recovery = append(p.recovery, target)
			}
		}
		a.resumeControlPlaneReadback(ct)
	}
}

func (a *appModel) resumeControlPlaneReadback(ct *contact) {
	p, client := &ct.activity, a.productActivityClient()
	if p.changing || client == nil || !a.currentActivityClient(ct, client) || a.dataRecoveryBlocksChanges() {
		return
	}
	for len(p.recovery) > 0 {
		target := p.recovery[0]
		p.recovery[0] = productActivityIntent{}
		p.recovery = p.recovery[1:]
		if a.activityIntents[target.ActivityID].OperationID == target.OperationID {
			a.runProductActivityAction(ct, client, target, false)
			return
		}
	}
}

func activitySteerReceipts(c *ui.Context, t *ui.Theme, msgs locales.Messages, activity *pi.ProductActivity, executionID string) {
	for _, steer := range activity.Steers {
		if steer.ActivityID != activity.ActivityID || steer.ExecutionID != executionID {
			continue
		}
		label, ink := msgs.ActivitySteerUnknown(), t.Warning
		switch steer.State {
		case "pending_enqueue", "queued":
			label, ink = msgs.ActivitySteerQueued(), t.TextMuted
		case "delivered":
			label, ink = msgs.ActivitySteerDelivered(), t.Success
		case "not_applied":
			label = msgs.ActivitySteerNotApplied()
		case "rejected":
			label, ink = msgs.ActivitySteerRejected(), t.Danger
		}
		ui.Row(c).Key(steer.SteerID).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
			capabilityBadge(c, t, label, ink)
			if reason := activitySteerReason(msgs, steer.Reason); reason != "" {
				capabilityText(c, t, reason)
			}
		})
	}
}

func activitySteerReason(msgs locales.Messages, reason string) string {
	switch reason {
	case "target_unavailable":
		return msgs.ActivitySteerTargetUnavailable()
	case "input_handled":
		return msgs.ActivitySteerInputHandled()
	case "queue_removed":
		return msgs.ActivitySteerQueueRemoved()
	case "delivery_unobserved":
		return msgs.ActivitySteerUnobserved()
	case "worker_lost":
		return msgs.ActivitySteerWorkerLost()
	default:
		return reason
	}
}

func openActivityApprovals(ct *contact, activity *pi.ProductActivity) []pi.ProductSkillApprovalRequest {
	var requests []pi.ProductSkillApprovalRequest
	for _, request := range activity.ApprovalRequests {
		if request.State != "open" || request.BotID != ct.ID || request.ActivityID != activity.ActivityID ||
			!slices.Contains(activity.PendingApprovalRequestIDs, request.RequestID) {
			continue
		}
		if current, ok := ct.capabilities.requests[request.RequestID]; ok && current.Revision >= request.Revision && current.State != "open" {
			continue
		}
		requests = append(requests, request)
	}
	return requests
}

func (a *appModel) activityApprovalCard(c *ui.Context, t *ui.Theme, ct *contact, request pi.ProductSkillApprovalRequest, blocked, compact bool) {
	msgs := locales.In(c.Locale())
	blocked = blocked || a.capabilitiesBlocked() || ct.Archived
	label := msgs.CapPendingApproval(request.SkillID, request.Script)
	text := capabilityText(c, t, label).TextColor(t.Warning)
	if compact {
		appTooltip(c, text.MaxLines(1).Ellipsis("…"), label)
	}
	ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(4).Children(func() {
		key := pi.ProductSkillKey{Source: request.Source, BotID: request.BotID, SkillID: request.SkillID}
		a.skillReviewButton(c, msgs.CapReviewApprove(), key, request.SkillID, &request, blocked)
		theme.TextAction(c, msgs.CapDeny(), true).Disabled(blocked).OnClick(func() { a.denySkillApproval(ct, request) })
	})
	ct.capabilities.failure.render(c, t, msgs.CapApprovalChangeFailed())
}

type activityTraceState struct {
	open, reading, loaded bool
	problem               string
	page                  pi.ProductActivityTrace
	requested, observed   uint64
	client                *pi.ProductClient
}

func (a *appModel) applyActivityTraceUpdated(host *piAppHost, change pi.ProductActivityTraceUpdated) {
	target := productActivityExecution(change.ActivityID, change.ExecutionID)
	for _, ct := range a.contacts {
		p := &ct.activity
		if p.activityID == change.ActivityID {
			if state := p.traces[target]; state != nil && change.LastSeq > state.requested {
				state.requested = change.LastSeq
				if p.open && state.open && p.result == nil && !a.settingsOpen && a.selectedContact() == ct {
					a.readActivityTrace(ct, host.product, target, state, false)
				}
			}
		}
	}
}

func (a *appModel) readActivityTrace(ct *contact, client *pi.ProductClient, target pi.ProductActivityExecutionParams, state *activityTraceState, more bool) {
	if state.reading || a.shutdownStarted {
		return
	}
	params := pi.ProductActivityTraceParams{ProductActivityParams: target.ProductActivityParams, ExecutionID: target.ExecutionID,
		ProductPageParams: pi.ProductPageParams{Limit: 20}}
	keep := len(state.page.Items)
	if more && state.page.NextCursor != nil {
		params.Cursor = *state.page.NextCursor
		keep = 0
	}
	observed := state.requested
	state.reading, state.problem = true, ""
	a.contactHistoryInFlight++
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var page pi.ProductActivityTrace
		cursor := params.Cursor
		var err error
		for {
			params.Cursor = cursor
			var next pi.ProductActivityTrace
			next, err = client.ActivityTrace(ctx, params)
			if err != nil {
				break
			}
			page.Items = append(page.Items, next.Items...)
			page.NextCursor, page.HistoryTruncated, page.Coverage = next.NextCursor, next.HistoryTruncated, next.Coverage
			if page.NextCursor == nil || len(page.Items) >= keep {
				break
			}
			cursor = *page.NextCursor
		}
		a.updateUI(func() {
			defer a.finishActivityRead()
			state.reading = false
			if !a.currentActivityClient(ct, client) || ct.activity.traces[target] != state {
				return
			}
			state.client, state.observed = client, observed
			state.problem = a.connectionSafeMessage(err)
			if err == nil {
				if more {
					page.Items = append(state.page.Items, page.Items...)
				}
				state.page, state.loaded = page, true
			}
		})
	}()
}

func (a *appModel) activityExecutionTrace(c *ui.Context, t *ui.Theme, ct *contact, activityID, executionID string) {
	p, client, msgs := &ct.activity, a.productActivityClient(), locales.In(c.Locale())
	target := productActivityExecution(activityID, executionID)
	if p.traces == nil {
		p.traces = make(map[pi.ProductActivityExecutionParams]*activityTraceState)
	}
	state := p.traces[target]
	if state == nil {
		state = &activityTraceState{}
		p.traces[target] = state
	}
	c.Key("execution-trace")
	theme.TextAction(c, msgs.ActivityTrace(), false).AlignSelf(ui.Start).Expanded(state.open).OnClick(func() { state.open = !state.open })
	if !state.open {
		return
	}
	if client != nil && !state.reading && (state.client != client || state.requested > state.observed || !state.loaded && state.problem == "") {
		a.readActivityTrace(ct, client, target, state, false)
	}
	if state.reading {
		capabilityText(c, t, msgs.ResultLoadingHistory()).Role(ui.RoleStatus)
	}
	if state.problem != "" {
		capabilityText(c, t, state.problem).TextColor(t.Warning)
		secondaryButton(c, msgs.MemoryRetry()).AlignSelf(ui.Start).Disabled(client == nil || state.reading).OnClick(func() {
			a.readActivityTrace(ct, client, target, state, false)
		})
	}
	if state.loaded && len(state.page.Items) == 0 {
		capabilityText(c, t, msgs.ActivityTraceEmpty())
	}
	if state.page.HistoryTruncated {
		capabilityText(c, t, msgs.ActivityTraceTruncated())
	}
	for _, item := range state.page.Items {
		c.Key(item.Seq)
		activityTraceRow(c, t, msgs, item)
	}
	if state.page.NextCursor != nil {
		secondaryButton(c, msgs.ActivityLoadMore()).AlignSelf(ui.Start).Disabled(client == nil || state.reading).OnClick(func() {
			a.readActivityTrace(ct, client, target, state, true)
		})
	}
}

func activityTraceRow(c *ui.Context, t *ui.Theme, msgs locales.Messages, item pi.ProductActivityTraceItem) {
	label, ink := msgs.ActivityTraceStarted(), t.TextMuted
	switch item.Kind {
	case "tool_finished":
		if item.OK != nil && *item.OK {
			label, ink = msgs.WorkStateCompleted(), t.Success
		} else {
			label, ink = msgs.WorkStateFailed(), t.Danger
		}
	case "file_confirmed":
		label, ink = msgs.ActivityTraceFile(), t.Success
	case "state_changed":
		label = productActivityStateLabel(msgs, item.State)
		if item.Scope == "execution" {
			label = activityExecutionLabel(msgs, item.State)
		}
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(2).Padding(4, 8).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
			if item.Tool != "" {
				capabilityText(c, t, item.Tool).TextColor(t.Text)
			}
			capabilityBadge(c, t, label, ink)
		})
		for _, text := range [...]string{item.Target, item.Path, item.Excerpt} {
			if text != "" {
				capabilityText(c, t, text).Selectable()
			}
		}
	})
}

type activityNoticeState struct {
	client          *pi.ProductClient
	version, loaded uint64
	reading, failed bool
	facts           []pi.ProductActivityDigest
	details         map[string]*pi.ProductActivity
	dismissed       map[activityNoticeKey]bool
	selected        activityNoticeKey
	scroll          ui.ScrollState
}

type activityNoticeKey struct {
	activityID, kind, itemID string
}

type activityNotice struct {
	key      activityNoticeKey
	activity *pi.ProductActivity
	question pi.ProductQuestion
	approval pi.ProductSkillApprovalRequest
}

func (a *appModel) applyActivityDigest(host *piAppHost, digest pi.ProductActivityDigest) {
	ct := a.contactByID(digest.OwnerBotID)
	if ct == nil || digest.FactID == "" || digest.ActivityID == "" || digest.OriginConversationID == "" {
		return
	}
	switch digest.Kind {
	case "completed", "needs_attention", "question_open", "question_auto_saved", "approval_required":
	default:
		return
	}
	n := &ct.activityNotices
	if slices.ContainsFunc(n.facts, func(saved pi.ProductActivityDigest) bool { return saved.FactID == digest.FactID }) {
		return
	}
	if len(n.facts) == 64 {
		n.facts = slices.Delete(n.facts, 0, 1)
	}
	n.facts = append(n.facts, digest)
	n.version++
	if ct.activeActivities == nil {
		a.refreshBotActivityStatus(host, ct)
	}
}

func (a *appModel) refreshActivityNotices(ct *contact, client *pi.ProductClient) {
	n := &ct.activityNotices
	if n.reading || a.shutdownStarted || n.client == client && n.version == n.loaded {
		return
	}
	ids := make(map[string]string)
	for _, item := range ct.activeActivities {
		ids[item.ActivityID] = item.OriginConversationID
	}
	for _, fact := range n.facts {
		if _, present := ids[fact.ActivityID]; !present {
			ids[fact.ActivityID] = fact.OriginConversationID
		}
	}
	if len(ids) == 0 {
		n.client, n.loaded, n.details, n.failed = client, n.version, nil, false
		return
	}
	version, owner := n.version, ct.ID
	n.reading = true
	a.contactHistoryInFlight++
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		details := make(map[string]*pi.ProductActivity, len(ids))
		failed := false
		for _, id := range slices.Sorted(maps.Keys(ids)) {
			item, err := client.GetActivity(ctx, id)
			var remote *pi.RemoteError
			if errors.As(err, &remote) && remote.Code == "not_found" {
				continue
			}
			if err != nil {
				failed = true
				break
			}
			if item.ActivityID == id && item.OwnerBotID == owner && item.OriginConversationID == ids[id] {
				details[id] = &item
			}
		}
		a.updateUI(func() {
			defer a.finishActivityRead()
			n.reading = false
			if !a.currentActivityClient(ct, client) || version != n.version {
				return
			}
			n.client, n.loaded, n.details, n.failed = client, version, details, failed
			for _, item := range details {
				current, exists := ct.activeActivities[item.ActivityID]
				if exists && (current.Revision > item.Revision || current.Revision == item.Revision && activityProgressSeq(current.LatestProgress) > activityProgressSeq(item.LatestProgress)) {
					continue
				}
				if activityNeedsStatus(item.ProductActivitySummary) {
					if ct.activeActivities == nil {
						ct.activeActivities = make(map[string]pi.ProductActivitySummary)
					}
					ct.activeActivities[item.ActivityID] = item.ProductActivitySummary
				} else {
					delete(ct.activeActivities, item.ActivityID)
				}
				a.applySkillApprovalRequests(item.ApprovalRequests)
			}
		})
	}()
}

func activityNotices(ct *contact) []activityNotice {
	n := &ct.activityNotices
	var notices []activityNotice
	for id, activity := range n.details {
		if ct.activity.open && ct.activity.activityID == id && ct.activity.activity != nil && ct.activity.result == nil {
			continue
		}
		current, active := ct.activeActivities[id]
		if active && !sameActivityNotice(current, activity.ProductActivitySummary) {
			continue
		}
		if !active && n.version != n.loaded || activity.ClosedAt != "" {
			continue
		}
		execution, _ := latestProductExecution(activity)
		for _, question := range activity.Questions {
			key := activityNoticeKey{id, "question_open", question.QuestionID}
			switch {
			case active && question.State == "open" && slices.Contains(activity.OpenQuestionIDs, question.QuestionID):
				notices = append(notices, activityNotice{key: key, activity: activity, question: question})
			case question.Answer != nil && question.Provenance == "bot_auto" && activity.AcknowledgedAt == "" && question.ExecutionID == execution.ExecutionID:
				key.kind = "question_auto_saved"
				if !n.dismissed[key] {
					notices = append(notices, activityNotice{key: key, activity: activity, question: question})
				}
			}
		}
		if !active {
			continue
		}
		for _, approval := range openActivityApprovals(ct, activity) {
			notices = append(notices, activityNotice{key: activityNoticeKey{id, "approval_required", approval.RequestID}, activity: activity, approval: approval})
		}
		if canAcknowledgeActivity(activity, execution) {
			notices = append(notices, activityNotice{key: activityNoticeKey{id, "completed", execution.ExecutionID}, activity: activity})
		} else if activity.State == "needsAttention" && len(activity.OpenQuestionIDs)+len(activity.PendingApprovalRequestIDs) == 0 {
			notices = append(notices, activityNotice{key: activityNoticeKey{id, "needs_attention", ""}, activity: activity})
		}
	}
	priority := map[string]int{"approval_required": 0, "question_open": 1, "needs_attention": 2, "completed": 3, "question_auto_saved": 4}
	slices.SortFunc(notices, func(x, y activityNotice) int {
		if order := cmp.Compare(priority[x.key.kind], priority[y.key.kind]); order != 0 {
			return order
		}
		if order := cmp.Compare(x.key.activityID, y.key.activityID); order != 0 {
			return order
		}
		return cmp.Compare(x.key.itemID, y.key.itemID)
	})
	return notices
}

func (a *appModel) activityNoticeStrip(c *ui.Context, t *ui.Theme, ct *contact) {
	n, client := &ct.activityNotices, a.productActivityClient()
	if client == nil || ct.Archived {
		return
	}
	a.refreshActivityNotices(ct, client)
	notices := activityNotices(ct)
	if len(notices) == 0 && !n.failed {
		return
	}
	msgs := locales.In(c.Locale())
	index := max(0, slices.IndexFunc(notices, func(item activityNotice) bool { return item.key == n.selected }))
	ui.Row(c).Key("activity-notices").KeepFocus().FillWidth().MinWidth(0).Shrink(0).Justify(ui.Center).Children(func() {
		ui.Column(c).Key(ct.ID).FillWidth().MaxWidth(conversationMaxWidth-32).MinWidth(0).Margin(0, 16, 4).
			Gap(4).Padding(4, 8).Radius(theme.CardRadius).Border(1, t.Border).Background(t.Surface).Children(func() {
			ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(4).AlignItems(ui.Center).Children(func() {
				if len(notices) > 0 {
					item := notices[index]
					caption := capabilityText(c, t, item.activity.Goal).Grow(1).FontWeight(500).TextColor(t.Text).MaxLines(1).Ellipsis("…")
					appTooltip(c, caption, item.activity.Goal)
					if len(notices) > 1 {
						label := msgs.ActivityNextNotice(index+1, len(notices))
						theme.TextAction(c, label, false).Shrink(0).OnClick(func() {
							n.selected = notices[(index+1)%len(notices)].key
							n.scroll.Y = 0
						})
					}
				}
				if n.failed {
					theme.TextAction(c, msgs.ActivityRetryNotices(), false).Disabled(n.reading).OnClick(func() { n.version++ })
				}
			})
			if len(notices) > 0 {
				item := notices[index]
				c.Key(item.key)
				ui.Scroll(c).FillWidth().MinWidth(0).MaxHeight(110).TrackScroll(&n.scroll).Children(func() {
					ui.Column(c).FillWidth().MinWidth(0).Gap(2).Children(func() { a.activityNoticeCard(c, t, ct, item, msgs) })
				})
			}
		})
	})
}

func (a *appModel) activityNoticeCard(c *ui.Context, t *ui.Theme, ct *contact, item activityNotice, msgs locales.Messages) {
	activity := item.activity
	blocked := ct.activity.changing || a.activityIntents[activity.ActivityID].OperationID != "" || a.dataRecoveryBlocksChanges()
	openActivity := func() {
		ct.activity.open = true
		a.openProductActivity(ct, activity.ActivityID)
	}
	if ct.activity.actionTarget == activity.ActivityID && ct.activity.actionProblem != "" {
		capabilityText(c, t, ct.activity.actionProblem).TextColor(t.Warning)
	}
	switch item.key.kind {
	case "approval_required":
		a.activityApprovalCard(c, t, ct, item.approval, blocked, true)
	case "question_open":
		question := item.question
		target := activityAnswerIntent(ct, activity, question)
		form := activityDraft(&ct.activity, target)
		text := capabilityText(c, t, question.Text).MaxLines(2).Ellipsis("…")
		appTooltip(c, text, question.Text)
		theme.TextAction(c, msgs.ActivityAnswer(), false).AlignSelf(ui.Start).Disabled(blocked).OnClick(func() {
			openActivity()
			a.editProductActivity(ct, target)
		})
		if len(question.Options) > 0 {
			activityOptions(c, msgs, question, form, blocked, nil)
		}
	case "question_auto_saved":
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			capabilityBadge(c, t, msgs.ActivityAutoAnswered(), t.TextMuted)
			answer := item.question.Answer.Text
			appTooltip(c, capabilityText(c, t, answer).Grow(1).MaxLines(1).Ellipsis("…"), answer)
			theme.TextAction(c, msgs.ActivityDismissHint(), false).OnClick(func() {
				if ct.activityNotices.dismissed == nil {
					ct.activityNotices.dismissed = make(map[activityNoticeKey]bool)
				}
				ct.activityNotices.dismissed[item.key] = true
			})
		})
	default:
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			activityStatusBadge(c, t, msgs, activity.ProductActivitySummary)
			if item.key.kind == "completed" {
				target := productActivityIntent{BotID: ct.ID, ActivityID: activity.ActivityID, Action: "acknowledge", ExecutionID: item.key.itemID, Revision: activity.Revision}
				theme.TextAction(c, msgs.ActivityAcknowledge(), false).Disabled(blocked).OnClick(func() { a.submitProductActivity(ct, target, nil) })
			}
			theme.TextAction(c, msgs.ActivityOpen(), false).OnClick(openActivity)
		})
	}
}
