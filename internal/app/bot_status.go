package app

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

type botVisualKind uint8

const (
	botVisualIdle botVisualKind = iota
	botVisualDone
	botVisualWorking
	botVisualReplying
	botVisualWaiting
	botVisualArchived
)

type botStatus struct {
	kind                                       botVisualKind
	waitingCount, attentionCount, workingCount int
	waitingActivity, attentionActivity         string
	questionID                                 string
	waitingIssue, attentionIssue               *productIssue
}

func (a *appModel) botVisualState(ct *contact, now time.Time) botStatus {
	if ct.Archived {
		return botStatus{kind: botVisualArchived}
	}
	var status botStatus
	if ct.activeActivities == nil {
		for _, item := range ct.activity.items {
			status.addActivity(ct.ID, item)
		}
	} else {
		for _, item := range ct.activeActivities {
			status.addActivity(ct.ID, item)
		}
	}
	for _, issue := range ct.Issues {
		if issue == nil || issueManagementTerminal(issue) {
			continue
		}
		if ct.activeActivities != nil {
			if _, exists := ct.activeActivities[issue.ID]; exists {
				continue
			}
		}
		state := issueDisplayStatusFor(issue)
		questions := 0
		for _, question := range issue.Questions {
			if question != nil && question.State == issueQuestionOpen {
				questions++
			}
		}
		switch {
		case questions > 0:
			status.waitingCount += questions
			if status.waitingIssue == nil {
				status.waitingIssue = issue
			}
		case state.Attention || state.Waiting:
			status.attentionCount++
			if status.attentionIssue == nil {
				status.attentionIssue = issue
			}
		case state.Active || state.Status == issueStatusInProgress:
			status.workingCount++
		}
	}
	switch {
	case status.waitingCount > 0 || status.attentionCount > 0 || ct.Status == statusAttention:
		status.kind = botVisualWaiting
	case ct.Status == statusWorking || sessionBusy(ct.SendState, ct.ActiveTaskID):
		status.kind = botVisualReplying
	case status.workingCount > 0:
		status.kind = botVisualWorking
	case now.Before(ct.completedUntil):
		status.kind = botVisualDone
	}
	return status
}

func (s *botStatus) addActivity(owner string, item pi.ProductActivitySummary) {
	if item.OwnerBotID != owner || item.State == "completed" || item.State == "cancelled" {
		return
	}
	switch {
	case len(item.OpenQuestionIDs) > 0:
		s.waitingCount += len(item.OpenQuestionIDs)
		if s.waitingActivity == "" || item.ActivityID < s.waitingActivity {
			s.waitingActivity, s.questionID = item.ActivityID, item.OpenQuestionIDs[0]
		}
	case item.State == "waitingForAnswer" || item.State == "needsAttention":
		s.attentionCount++
		if s.attentionActivity == "" || item.ActivityID < s.attentionActivity {
			s.attentionActivity = item.ActivityID
		}
	case item.State == "running":
		s.workingCount++
	}
}

func (a *appModel) recordBotCompletion(ct *contact) {
	now := time.Now()
	if a.botVisualState(ct, now).kind > botVisualDone {
		return
	}
	ct.completedUntil = now.Add(1800 * time.Millisecond)
	ct.recentCompletion = len(a.contacts) > 0 && a.selectedContact() != ct
}

func localizedBotPurpose(ct *contact, msgs locales.Messages) string {
	purpose := strings.TrimSpace(ct.Purpose)
	if ct.isBuiltin() && purpose == locales.In("en").PandoraBoxPurpose() {
		return msgs.PandoraBoxPurpose()
	}
	return purpose
}

func (s botStatus) label(msgs locales.Messages, rail bool) string {
	switch s.kind {
	case botVisualArchived:
		return msgs.ProfileArchivedBadge()
	case botVisualWaiting:
		if s.waitingCount == 0 {
			return msgs.BotStatusNeedsAttention()
		}
		if rail {
			return msgs.BotStatusRailWaiting(s.waitingCount)
		}
		return msgs.BotStatusWaiting(s.waitingCount)
	case botVisualReplying:
		if !rail && s.workingCount > 0 {
			return msgs.BotStatusReplyingWithTasks(s.workingCount)
		}
		return msgs.BotStatusReplying()
	case botVisualWorking:
		if rail {
			return msgs.BotStatusRailWorking(s.workingCount)
		}
		return msgs.BotStatusWorking(s.workingCount)
	case botVisualDone:
		if rail {
			return msgs.BotStatusRailDone()
		}
		return msgs.BotStatusDone()
	default:
		return ""
	}
}

func (s botStatus) color(t *ui.Theme) ui.Color {
	switch s.kind {
	case botVisualWaiting, botVisualArchived:
		return t.Warning
	case botVisualReplying, botVisualWorking, botVisualDone:
		return t.Success
	default:
		return t.TextMuted
	}
}

func (a *appModel) botRailSubtitle(ct *contact, msgs locales.Messages, t *ui.Theme, state botStatus) (string, ui.Color) {
	if state.kind == botVisualIdle && ct.recentCompletion {
		state.kind = botVisualDone
	}
	label := state.label(msgs, true)
	switch state.kind {
	case botVisualIdle:
		label = a.snippet(ct, msgs)
	case botVisualDone:
		if len(ct.Messages) > 0 {
			if snippet := a.snippet(ct, msgs); snippet != msgs.EmptyNoMessages() && snippet != msgs.HistoryNotLoadedCompact() {
				label = msgs.BotStatusRailDoneWithSnippet(snippet)
			}
		}
	}
	return label, state.color(t)
}

func (a *appModel) botStatusAction(c *ui.Context, t *ui.Theme, ct *contact, state botStatus, msgs locales.Messages) {
	if state.kind == botVisualArchived || state.waitingCount+state.attentionCount+state.workingCount == 0 {
		return
	}
	label := msgs.BotStatusViewTasks()
	ink, border := t.TextMuted, t.Border
	if state.kind == botVisualWaiting && state.waitingCount+state.attentionCount > 0 {
		label = msgs.BotStatusAnswer()
		ink, border = t.Warning, t.Warning.Alpha(.35)
	}
	button := ui.Button(c, label).Label(label).Height(32).MinHeight(32).Padding(4, 10).Radius(8).Shrink(0).
		FontSize(typeAction).FontWeight(500).FixedLineHeight(lineCompact).TextColor(ink).Border(1, border).
		Disabled(a.shutdownStarted || a.dataRecoveryBlocksChanges())
	fill := t.Surface
	if button.Pressed() {
		fill = t.SurfacePressed
	} else if button.Hovered() {
		fill = t.SurfaceHover
	}
	button.Background(fill)
	button.OnClick(func() {
		p := &ct.activity
		p.open = true
		switch {
		case state.waitingActivity != "":
			a.openProductActivity(ct, state.waitingActivity)
			p.focusQuestion = state.questionID
			p.focusBack = false
		case state.waitingIssue != nil:
			a.openLegacyBotIssue(ct, state.waitingIssue)
		case state.attentionActivity != "":
			a.openProductActivity(ct, state.attentionActivity)
		case state.attentionIssue != nil:
			a.openLegacyBotIssue(ct, state.attentionIssue)
		default:
			p.activityID, p.activity, p.result, p.file = "", nil, nil, nil
			p.focusQuestion, p.legacyIssue = "", false
			p.problem, p.actionProblem, p.scroll.Y = "", "", 0
			p.focusToggle = true
		}
	})
}

func (a *appModel) openLegacyBotIssue(ct *contact, issue *productIssue) {
	ct.SelectedIssueID = issue.ID
	p := &ct.activity
	p.activityID, p.activity, p.result, p.file = "", nil, nil, nil
	p.problem, p.actionProblem, p.scroll.Y = "", "", 0
	p.legacyIssue, p.focusBack = true, true
}

func botStatusAvatar(c *ui.Context, t *ui.Theme, ct *contact, size float32, state botStatus) {
	const gutter = 4
	radius := float32(8)
	if size == 24 {
		radius = 7
	}
	if state.kind == botVisualDone {
		c.After(max(16*time.Millisecond, ct.completedUntil.Sub(c.Now())))
	}
	outer := ui.Box(c).Size(size+2*gutter, size+2*gutter).Shrink(0).Center().IgnoreInput(true)
	breathing := state.kind == botVisualReplying && !c.Preferences().ReduceMotion
	if state.kind == botVisualArchived {
		outer.Opacity(.6)
	}
	if breathing {
		outer.Draw(func(p *ui.Painter, r ui.Rect) {
			phase := float64(p.Now().UnixMilli()%1800) * (2 * math.Pi / 1800)
			pulse := float32((1 + math.Sin(phase)) / 2)
			p.Stroke(r, t.Success.Alpha(.12+.12*pulse), radius+gutter, 2)
			ring := ui.Rect{X: r.X + 2, Y: r.Y + 2, W: r.W - 4, H: r.H - 4}
			p.Stroke(ring, t.Success.Alpha(.45+.5*pulse), radius+2, 2)
			p.After(33 * time.Millisecond)
		})
	}
	outer.Children(func() {
		ink, glow, width := t.Border.Alpha(.45), ui.Transparent, float32(1)
		switch state.kind {
		case botVisualReplying:
			ink, glow, width = t.Success.Alpha(.75), t.Success.Alpha(.18), 2
			if breathing {
				ink, glow = ui.Transparent, ui.Transparent
			}
		case botVisualWorking:
			ink, glow, width = t.Success, t.Success.Alpha(.19), 2
		case botVisualWaiting:
			ink, glow, width = t.Warning, t.Warning.Alpha(.34), 2
		case botVisualDone:
			ink, glow, width = t.Success.Alpha(.55), t.Success.Alpha(.12), 2
		}
		inset := gutter - width
		ui.Box(c).Absolute().Left(inset).Right(inset).Top(inset).Bottom(inset).Radius(radius+width).
			Border(width, ink).Shadow(0, 0, 4, 1, glow).IgnoreInput(true).
			Transition(ui.ElementTransition{Duration: 180 * time.Millisecond, Colors: true, Shadows: true})
		contactAvatar(c, t, ct.Name, size)
	})
}

func listBotActiveActivities(ctx context.Context, client *pi.ProductClient, botID string) (map[string]pi.ProductActivitySummary, error) {
	items := make(map[string]pi.ProductActivitySummary)
	params := pi.ProductActivityListParams{BotID: botID, States: []string{"running", "waitingForAnswer", "needsAttention"}, ProductPageParams: pi.ProductPageParams{Limit: 200}}
	for {
		page, err := client.ListActivities(ctx, params)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if item.OwnerBotID == botID {
				items[item.ActivityID] = item
			}
		}
		if page.NextCursor == nil {
			return items, nil
		}
		params.Cursor = *page.NextCursor
	}
}

func activityNeedsStatus(item pi.ProductActivitySummary) bool {
	switch item.State {
	case "completed", "cancelled":
		return false
	case "running", "waitingForAnswer", "needsAttention":
		return true
	default:
		return len(item.OpenQuestionIDs) > 0
	}
}

func (a *appModel) applyBotActivityStatus(item pi.ProductActivitySummary) {
	ct := a.contactByID(item.OwnerBotID)
	if ct == nil {
		return
	}
	if ct.activeActivities == nil {
		ct.activeActivities = make(map[string]pi.ProductActivitySummary)
		for _, saved := range ct.activity.items {
			if saved.OwnerBotID == ct.ID && activityNeedsStatus(saved) {
				ct.activeActivities[saved.ActivityID] = saved
			}
		}
	}
	previous, existed := ct.activeActivities[item.ActivityID]
	if existed && previous.Revision >= item.Revision {
		return
	}
	ct.activityStatusRevision++
	if activityNeedsStatus(item) {
		ct.activeActivities[item.ActivityID] = item
	} else {
		delete(ct.activeActivities, item.ActivityID)
		if existed && item.State == "completed" {
			a.recordBotCompletion(ct)
		}
	}
}

func (a *appModel) refreshBotActivityStatus(host *piAppHost, ct *contact) {
	revision := ct.activityStatusRevision
	a.contactHistoryInFlight++
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		items, err := listBotActiveActivities(ctx, host.product, ct.ID)
		a.updateUI(func() {
			defer a.finishActivityRead()
			if err != nil || a.shutdownStarted || !a.currentActivityClient(ct, host.product) {
				return
			}
			if ct.activityStatusRevision != revision {
				a.refreshBotActivityStatus(host, ct)
				return
			}
			ct.activeActivities = items
		})
	}()
}
