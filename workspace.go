package main

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

// A workItem is the GUI projection of one runtime task. User/assistant text
// stays in chat; tool activity is attached here by task/call identity.
type workState string

const (
	workAccepted    workState = "accepted"
	workRunning     workState = "running"
	workStopping    workState = "stopping"
	workCompleted   workState = "completed"
	workFailed      workState = "failed"
	workInterrupted workState = "interrupted"
	workUnknown     workState = "unknown"
)

type workTool struct {
	CallID   string
	Name     string
	Args     string
	Output   string
	Started  bool
	Finished bool
	Expanded bool
}

type workItem struct {
	TaskID        string
	MessageID     string
	Prompt        string
	State         workState
	Detail        string
	Tools         []*workTool
	StopRequested bool
}

const (
	defaultLeftPaneWidth  float32 = 200
	minLeftPaneWidth      float32 = 180
	maxLeftPaneWidth      float32 = 280
	defaultRightPaneWidth float32 = 340
	minRightPaneWidth     float32 = 280
	maxRightPaneWidth     float32 = 440
	minCenterPaneWidth    float32 = 480
	paneSeparatorWidth    float32 = 1

	avatarRailWidth      float32 = 56
	fullRailBreakpoint   int     = 1180
	avatarRailBreakpoint int     = 900
	minAvatarDetailWidth float32 = 300
	effectiveCenterWidth float32 = 494
)

func clampPaneWidth(value, fallback, minValue, maxValue float32) float32 {
	if value <= 0 {
		value = fallback
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func (a *appModel) layoutWidth() int {
	if a.windowWidth > 0 {
		return a.windowWidth
	}
	return 1040
}

func (a *appModel) railWidth() float32 {
	return clampPaneWidth(a.leftPaneWidth, defaultLeftPaneWidth, minLeftPaneWidth, maxLeftPaneWidth)
}

func (a *appModel) workWidth() float32 {
	return clampPaneWidth(a.rightPaneWidth, defaultRightPaneWidth, minRightPaneWidth, maxRightPaneWidth)
}

func (a *appModel) workOverlay() bool {
	return a.layoutWidth() < avatarRailBreakpoint
}

func (a *appModel) avatarRailActive() bool {
	return a.workspaceOpen && !a.railCollapsed && !a.workOverlay() &&
		a.layoutWidth() < fullRailBreakpoint
}

func (a *appModel) effectiveRailWidth() float32 {
	switch {
	case a.railCollapsed:
		return 0
	case a.avatarRailActive():
		return avatarRailWidth
	default:
		return a.railWidth()
	}
}

func (a *appModel) detailMinWidthForLayout() float32 {
	width := a.layoutWidth()
	if width >= avatarRailBreakpoint && width < fullRailBreakpoint {
		return minAvatarDetailWidth
	}
	return minRightPaneWidth
}

func (a *appModel) effectiveDetailWidth() float32 {
	separators := 1 // main/work split
	if a.effectiveRailWidth() > 0 {
		separators++ // rail/main split or fixed avatar divider
	}
	available := float32(a.layoutWidth()) - a.effectiveRailWidth() -
		float32(separators)*paneSeparatorWidth - effectiveCenterWidth
	minWidth := a.detailMinWidthForLayout()
	if available < minWidth {
		available = minWidth
	}
	return clampPaneWidth(a.workWidth(), defaultRightPaneWidth, minWidth, available)
}

func (a *appModel) clampLeftForDrag(value float32, threePaneAtStart bool) float32 {
	limit := maxLeftPaneWidth
	if a.workspaceOpen && threePaneAtStart {
		// Left drag changes the rail preference. The detail may derive narrower
		// effective width, but the saved right preference is not rewritten.
		byCenter := float32(a.layoutWidth()) - 2*paneSeparatorWidth -
			a.detailMinWidthForLayout() - effectiveCenterWidth
		if byCenter < limit {
			limit = byCenter
		}
	}
	return clampPaneWidth(value, defaultLeftPaneWidth, minLeftPaneWidth, limit)
}

func (a *appModel) clampRightForDrag(value, area float32, threePaneAtStart bool) float32 {
	limit := maxRightPaneWidth
	if a.workspaceOpen && threePaneAtStart {
		byCenter := area - paneSeparatorWidth - effectiveCenterWidth
		if byCenter < limit {
			limit = byCenter
		}
	}
	return clampPaneWidth(value, defaultRightPaneWidth, a.detailMinWidthForLayout(), limit)
}

func workFor(ct *contact, taskID string) *workItem {
	if ct == nil || taskID == "" {
		return nil
	}
	for _, w := range ct.Works {
		if w.TaskID == taskID {
			return w
		}
	}
	return nil
}

func (a *appModel) ensureWork(ct *contact, taskID, messageID string) *workItem {
	if w := workFor(ct, taskID); w != nil {
		if w.MessageID == "" {
			w.MessageID = messageID
		}
		return w
	}
	w := &workItem{TaskID: taskID, MessageID: messageID, State: workAccepted}
	ct.Works = append(ct.Works, w)
	return w
}

func (ct *contact) activeWork() *workItem { return workFor(ct, ct.ActiveTaskID) }

func (ct *contact) recentWork() *workItem {
	if w := workFor(ct, ct.LastTaskID); w != nil {
		return w
	}
	for i := len(ct.Works) - 1; i >= 0; i-- {
		if ct.Works[i].TaskID != "" {
			return ct.Works[i]
		}
	}
	return nil
}

func workStateFromTask(status string) workState {
	switch status {
	case "completed":
		return workCompleted
	case "failed":
		return workFailed
	case "interrupted":
		return workInterrupted
	case "running":
		return workRunning
	default:
		return workUnknown
	}
}

func workStateText(msgs locales.Messages, w *workItem) string {
	if w.State == workInterrupted && w.StopRequested {
		return msgs.WorkStateStoppedInterrupted()
	}
	switch w.State {
	case workAccepted:
		return msgs.WorkStateAccepted()
	case workRunning:
		return msgs.WorkStateWorking()
	case workStopping:
		return msgs.WorkStateStopping()
	case workCompleted:
		return msgs.WorkStateCompleted()
	case workFailed:
		return msgs.WorkStateFailed()
	case workInterrupted:
		return msgs.WorkStateInterrupted()
	default:
		return msgs.WorkStateUnknown()
	}
}

func (a *appModel) workStateColor(state workState) ui.Color {
	switch state {
	case workStopping, workFailed, workInterrupted, workUnknown:
		return a.workTheme.WorkAttention
	default:
		return a.workTheme.WorkText
	}
}

func (a *appModel) toggleRail() {
	a.railCollapsed = !a.railCollapsed
	a.focusRail = true
}

// layoutBody draws the contact rail and the main conversation. A visible
// work panel nests a second Split so either edge can be resized independently.
func (a *appModel) layoutBody(c *ui.Context, t *ui.Theme) {
	if a.detailPageActive() {
		a.detailPage(c, t)
		return
	}
	if a.railCollapsed {
		a.layoutMainArea(c, t, 0, false)
		return
	}
	if a.avatarRailActive() {
		ui.Row(c).Fill().Children(func() {
			a.avatarRail(c, t)
			ui.Column(c).Width(paneSeparatorWidth).Shrink(0).FillHeight().Background(t.Border)
			a.layoutMainArea(c, t, avatarRailWidth+paneSeparatorWidth, false)
		})
		return
	}
	threePaneAtStart := a.workspaceOpen
	a.leftPaneWidth = a.railWidth()
	ui.Split(c, &a.leftPaneWidth, func() { a.rail(c, t) }, func() {
		a.layoutMainArea(c, t, a.leftPaneWidth, true)
	}).OnChange(func() {
		// Split updates its bound width inside the handle's afterInput, before
		// OnChange and the rebuild. Clamp here, not after a stale build.
		a.leftPaneWidth = a.clampLeftForDrag(a.leftPaneWidth, threePaneAtStart)
	}).Fill()
}

func (a *appModel) layoutMainArea(c *ui.Context, t *ui.Theme, left float32, outerSeparator bool) {
	if !a.workspaceOpen {
		a.main(c, t)
		return
	}
	area := float32(a.layoutWidth()) - left
	if outerSeparator {
		area -= paneSeparatorWidth
	}
	threePaneAtStart := true
	a.centerPaneWidth = area - paneSeparatorWidth - a.effectiveDetailWidth()
	ui.Split(c, &a.centerPaneWidth, func() {
		a.main(c, t)
	}, func() {
		a.workspaceFrame(c, t)
	}).OnChange(func() {
		candidate := area - paneSeparatorWidth - a.centerPaneWidth
		a.rightPaneWidth = a.clampRightForDrag(candidate, area, threePaneAtStart)
	}).Fill()
}

// workspaceFrame draws the current right detail in its own column.
func (a *appModel) workspaceFrame(c *ui.Context, t *ui.Theme) {
	ui.Column(c).FillWidth().FillHeight().Background(a.workTheme.Panel).BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		a.detailContent(c, t, false)
	})
}

// detailPage is the non-modal narrow rendering of the same right detail. It
// replaces the whole workbench body, so the 480px reading column is not
// squeezed or covered by a backdrop/focus trap.
func (a *appModel) detailPage(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(a.workTheme.Panel).Children(func() {
		a.detailContent(c, t, true)
	})
}

func (a *appModel) detailContent(c *ui.Context, t *ui.Theme, narrow bool) {
	switch a.activeDetail() {
	case detailHistory:
		a.historyDetail(c, t, narrow)
	case detailProfile:
		a.profileDetail(c, t, narrow)
	case detailWork:
		a.workspaceContent(c, t, narrow)
	}
}

func (a *appModel) workspaceContent(c *ui.Context, t *ui.Theme, narrow bool) {
	ct := a.selectedContact()
	nav := ct.issueNavigation()
	issue := ct.issueByID(nav.DetailID)
	if nav.DetailID != "" && issue == nil {
		nav.DetailID = ""
	}
	msgs := locales.In(c.Locale())
	title := msgs.WorkTasksTab()
	if nav.ShowConversationWork {
		title = msgs.WorkChatRunsTab()
	} else if issue != nil {
		title = msgs.WorkTask()
	}
	newIssueAction := func() {
		if issue != nil || nav.ShowConversationWork || a.issueFormOpen {
			return
		}
		newIssue := secondaryButton(c, msgs.IssueNewIssue()).FontWeight(500).OnClick(func() {
			a.issueFormOpen = true
			a.issueGoal = ""
			a.issueRequirements = ""
			a.clearIssueFormNotice()
			a.focusIssueGoal = true
		})
		if a.focusIssueNew {
			newIssue.Focus()
			a.focusIssueNew = false
		}
	}
	ui.Column(c).FillWidth().Padding(12).Gap(8).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			if narrow {
				backAction := func() { a.closeDetail() }
				backLabel, backTip, backVisible := msgs.WorkBackToChat(), msgs.WorkBackToChat(), msgs.WorkBackToChat()
				if nav.ShowConversationWork {
					backAction = func() { a.backFromConversationWork() }
					backLabel, backTip, backVisible = msgs.WorkBackToIssues(), msgs.WorkBackToIssues(), msgs.WorkBackToIssues()
				} else if issue != nil {
					backAction = func() { a.backToIssueOverview() }
					backLabel, backTip, backVisible = msgs.WorkBackToIssues(), msgs.WorkBackToIssues(), msgs.WorkBackToIssues()
				}
				back := detailBackButton(c, t, backLabel, backTip, backVisible).OnClick(backAction)
				if nav.FocusBack {
					back.Focus()
					nav.FocusBack = false
				}
				a.focusDetailControl(c, back)
				ui.Text(c, title).FontSize(typePageTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text).Grow(1).MaxLines(2)
				newIssueAction()
				return
			}
			ui.Text(c, title).FontSize(typePageTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text).Grow(1)
			newIssueAction()
			close := iconControlFace(c, ui.Button(c, "").Label(msgs.WorkClose()), iconPanel, false)
			close = appTooltip(c, close, msgs.WorkCloseTooltip()).OnClick(func() { a.closeDetail() })
			close.Children(func() { controlIcon(c, navCloseIcon, iconPanel).TextColor(t.TextMuted) })
			a.focusDetailControl(c, close)
		})
		tab := 0
		if nav.ShowConversationWork {
			tab = 1
		}
		tabs := segmentedSelection(c, &tab, msgs.WorkTasksTab(), msgs.WorkChatRunsTab()).Label(msgs.WorkTabs()).
			FontSize(typeAction).MaxWidth(360).FillWidth()
		if nav.FocusConversation {
			tabs.Focus()
			nav.FocusConversation = false
		}
		if tabs.Changed() {
			nav.ShowConversationWork = tab == 1
		}
	})
	switch {
	case nav.ShowConversationWork:
		a.conversationWorkView(c, t, ct, narrow)
	case issue != nil:
		a.issueDetailView(c, t, ct, issue, nav, narrow)
	default:
		a.issueOverviewView(c, t, ct, nav)
	}
}

func issueFilterIndex(filter string) int {
	switch filter {
	case issueFilterCompleted:
		return 1
	case issueFilterAll:
		return 2
	default:
		return 0
	}
}

func issueFilterFromIndex(index int) string {
	switch index {
	case 1:
		return issueFilterCompleted
	case 2:
		return issueFilterAll
	default:
		return issueFilterUnfinished
	}
}

func (a *appModel) issueOverviewView(c *ui.Context, t *ui.Theme, ct *contact, nav *issueViewState) {
	if a.issueFormOpen {
		a.issueFormView(c, t)
	}
	ui.Column(c).FillWidth().Grow(1).Padding(12).Gap(8).Children(func() {
		msgs := locales.In(c.Locale())
		filterIndex := issueFilterIndex(nav.Filter)
		filter := segmentedSelection(c, &filterIndex, msgs.IssueFilterUnfinished(), msgs.IssueFilterCompleted(), msgs.IssueFilterAll()).
			Label(msgs.IssueFilterLabel()).FontSize(typeAction).MaxWidth(360).FillWidth()
		if filter.Changed() {
			a.setIssueFilter(ct, issueFilterFromIndex(filterIndex))
			nav = ct.issueNavigation()
		}
		ui.Scroll(c).Grow(1).FillWidth().TrackScroll(&nav.OverviewScroll).Children(func() {
			ui.Column(c).Gap(3).Children(func() {
				a.issueList(c, t, ct)
			})
		})
	})
}

func issueRunLabel(msgs locales.Messages, status string) string {
	switch status {
	case issueExecutionFailed:
		return msgs.TaskRunFailed()
	case issueExecutionInterrupted:
		return msgs.TaskRunInterrupted()
	case issueExecutionUnknown:
		return msgs.TaskRunUnknown()
	case issueExecutionCompleted:
		return msgs.TaskRunCompleted()
	case issueExecutionRunning:
		return msgs.TaskRunRunning()
	case issueExecutionAccepted:
		return msgs.TaskRunAccepted()
	case issueExecutionPending:
		return msgs.TaskRunPending()
	case issueExecutionNotDispatched:
		return msgs.TaskRunNotDispatched()
	case issueExecutionRejected:
		return msgs.TaskRunRejected()
	default:
		return ""
	}
}

func issueDisplayLabel(msgs locales.Messages, display issueDisplayStatus) string {
	if display.Archived {
		return msgs.TaskStatusArchived()
	}
	switch display.Status {
	case issueStatusCompleted:
		return msgs.TaskStatusCompleted()
	case issueStatusCanceled:
		return msgs.TaskStatusCanceled()
	case issueStatusNeedsAttention:
		return msgs.TaskStatusNeedsAttention()
	case issueStatusWaitingUser:
		return msgs.TaskStatusWaiting()
	case issueStatusInProgress:
		return msgs.TaskStatusInProgress()
	case issueStatusNotStarted:
		return msgs.TaskStatusNotStarted()
	default:
		return msgs.TaskStatusNotStarted()
	}
}

func issueDisplayColor(t *ui.Theme, display issueDisplayStatus) ui.Color {
	if display.Archived || display.Status == issueStatusCanceled || display.Status == issueStatusNotStarted {
		return t.TextMuted
	}
	switch display.Status {
	case issueStatusNeedsAttention, issueStatusWaitingUser:
		return t.Warning
	default:
		return t.Success
	}
}

func (a *appModel) issueOverviewRow(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	if issue == nil {
		return
	}
	nav := ct.issueNavigation()
	row := ui.ButtonBase(c).Label(locales.In(c.Locale()).IssueRowLabel(issue.Goal)).Gap(8).Padding(8).Radius(8).
		Background(ui.Transparent).BorderWidth(0).AlignItems(ui.Start)
	if row.Pressed() {
		row.Background(t.SurfacePressed)
	} else if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.OnClick(func() { a.selectIssue(issue.ID) })
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				goalWeight := 400
				if nav.DetailID == issue.ID {
					goalWeight = 500
				}
				ui.Text(c, issue.Goal).FontSize(typeAction).FontWeight(goalWeight).LineHeight(1.35).MaxLines(2).TextColor(t.Text).Grow(1)
				ui.Icon(c, glyphArrowRightSLine).Size(14, 14).TextColor(t.TextMuted)
			})
			msgs := locales.In(c.Locale())
			display := issueDisplayStatusFor(issue)
			statusColor := issueDisplayColor(t, display)
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, "●").FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(statusColor)
				ui.Text(c, issueDisplayLabel(msgs, display)).FontSize(typeMeta).FixedLineHeight(lineMeta).MaxLines(1).TextColor(statusColor)
			})
			if latest := latestIssueExecution(issue); latest != nil {
				runLabel := issueRunLabel(msgs, latest.Status)
				sameCompleted := display.Status == issueStatusCompleted && latest.Status == issueExecutionCompleted
				sameActive := display.Status == issueStatusInProgress && display.Active &&
					(latest.Status == issueExecutionAccepted || latest.Status == issueExecutionRunning)
				if runLabel != "" && !sameCompleted && !sameActive {
					runColor := t.TextMuted
					if issueExecutionNeedsAttention(latest.Status) {
						runColor = t.Warning
					}
					ui.Text(c, runLabel).FontSize(typeMeta).FixedLineHeight(lineMeta).MaxLines(1).TextColor(runColor)
				}
			}
		})
	})
	if nav.FocusRowID == issue.ID {
		row.Focus()
		row.ScrollIntoView()
		nav.FocusRowID = ""
	}
}

func (a *appModel) issueDetailView(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, nav *issueViewState, narrow bool) {
	work := ct.issueWork(issue)
	active := work != nil && issue.Work.ActiveTaskID == work.TaskID
	explicit := work != nil && ct.SelectedWorkTaskID != "" && ct.SelectedWorkTaskID == work.TaskID
	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(&nav.DetailScroll).Padding(0, 12).Children(func() {
		ui.Column(c).Gap(8).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				if !narrow {
					back := detailBackButton(c, t, locales.In(c.Locale()).WorkBackToIssues(), locales.In(c.Locale()).WorkBackToIssues(), locales.In(c.Locale()).WorkBackToIssues()).OnClick(func() { a.backToIssueOverview() })
					if nav.FocusBack {
						back.Focus()
						nav.FocusBack = false
					}
				}
				ui.Spacer(c)
				if active {
					label := locales.In(c.Locale()).WorkStopIssueTask()
					if work.State == workStopping {
						label = locales.In(c.Locale()).WorkStoppingIssue()
					}
					stop := secondaryButton(c, label).TextColor(t.Warning).OnClick(func() {
						a.cancelWorkTask(ct, work.TaskID)
					})
					if work.State == workStopping {
						stop.Disabled(true)
					}
				}
			})
			if explicit {
				ui.Row(c).Children(func() {
					secondaryButton(c, locales.In(c.Locale()).IssueBackToIssue()).OnClick(func() {
						ct.SelectedWorkTaskID = ""
						nav.FocusBack = true
					})
				})
			}
			if active {
				ui.Text(c, locales.In(c.Locale()).IssueCurrentRun(workStateText(locales.In(c.Locale()), work))).FontSize(typeMeta).FixedLineHeight(lineMeta).MaxLines(2).TextColor(a.workStateColor(work.State))
			}
			display := issueDisplayStatusFor(issue)
			if issue.openQuestion() == nil {
				ui.Text(c, issueDisplayLabel(locales.In(c.Locale()), display)).FontSize(typeMeta).FixedLineHeight(lineMeta).FontWeight(500).TextColor(issueDisplayColor(t, display))
			}
			if issue.Status == issueStatusCompleted {
				ui.Text(c, locales.In(c.Locale()).IssueMarkedByUser(issueEvidenceText(locales.In(c.Locale()), issue))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted).MaxLines(3)
			}
			if a.issueFormOpen && a.issueFormEditingID != "" {
				a.issueFormView(c, t)
			}
			if explicit {
				a.issueResultContext(c, t, issue, work)
				snapshot, current := a.issueWorkDisclosureSnapshot(ct, issue, work)
				state, detail := work.State, work.Detail
				a.workCard(c, t, work, active, true, func(expanding bool, toggle func()) {
					if !a.issueWorkViewCurrent(ct, issue, work, state, detail) {
						return
					}
					toggle()
					if expanding && current() {
						a.confirmIssueExecutionSeen(snapshot.contact, snapshot.issue, snapshot.execution)
					}
				})
				a.workActivity(c, t, work)
				ui.Divider(c)
			}
			a.issueCard(c, t, ct, issue)
		})
	})
}

func (a *appModel) conversationWorkView(c *ui.Context, t *ui.Theme, ct *contact, narrow bool) {
	nav := ct.issueNavigation()
	ui.Scroll(c).Grow(1).FillWidth().Padding(0, 12).Children(func() {
		ui.Column(c).Gap(8).Children(func() {
			ui.Row(c).Children(func() {
				if !narrow {
					back := detailBackButton(c, t, locales.In(c.Locale()).WorkBackToIssues(), locales.In(c.Locale()).WorkBackToIssues(), locales.In(c.Locale()).WorkBackToIssues()).OnClick(func() { a.backFromConversationWork() })
					if nav.FocusBack {
						back.Focus()
						nav.FocusBack = false
					}
				}
			})
			work := conversationWork(ct)
			if work == nil {
				a.workEmptyState(c, t, ct)
			} else {
				title := "Recent work"
				if ct.ActiveTaskID == work.TaskID {
					title = "Current work"
				}
				ui.Text(c, title).FontSize(typePageTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text)
				a.workCard(c, t, work, ct.ActiveTaskID == work.TaskID, false)
				a.workActivity(c, t, work)
			}
			a.unlinkedActivity(c, t, ct.UnlinkedTools)
		})
	})
}

func (a *appModel) issueFormView(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	title := msgs.IssueNewIssue()
	if a.issueFormEditingID != "" {
		title = msgs.IssueEdit()
	}
	ui.Column(c).Padding(8, 0).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(typeBody).FontWeight(500).TextColor(t.Text)
			ui.Spacer(c)
			secondaryButton(c, locales.In(c.Locale()).ModelCancel()).OnClick(func() { a.cancelIssueForm() })
		})
		ui.Text(c, locales.In(c.Locale()).IssueFormGoal()).FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		goal := formTextArea(c, &a.issueGoal, false).Label(locales.In(c.Locale()).IssueFormGoalLabel()).Placeholder(locales.In(c.Locale()).IssueFormGoalPlaceholder()).Height(56)
		if a.focusIssueGoal {
			goal.Focus()
			a.focusIssueGoal = false
		}
		ui.Text(c, locales.In(c.Locale()).IssueFormRequirements()).FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		formTextArea(c, &a.issueRequirements, false).Label(locales.In(c.Locale()).IssueFormRequirementsLabel()).Placeholder(locales.In(c.Locale()).IssueFormRequirementsPlaceholder()).Height(72)
		if a.issueFormError != "" {
			text := a.issueFormError
			if a.issueFormNotice != "" {
				text = issueNoticeText(locales.In(c.Locale()), a.issueFormNotice, a.issueFormNoticeDetail)
			}
			ui.Text(c, text).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Children(func() {
			secondaryButton(c, locales.In(c.Locale()).WorkSaveIssue()).OnClick(func() { a.saveIssueForm() })
		})
	})
}

func (ct *contact) issueWork(issue *productIssue) *workItem {
	if issue == nil {
		return nil
	}
	for _, taskID := range []string{issue.Work.ActiveTaskID, ct.SelectedWorkTaskID, ct.ActiveTaskID, ct.LastTaskID} {
		if !issueHasTask(issue, taskID) {
			continue
		}
		if work := workFor(ct, taskID); work != nil {
			return work
		}
	}
	for index := len(issue.Executions) - 1; index >= 0; index-- {
		execution := issue.Executions[index]
		if work := workFor(ct, execution.TaskID); work != nil {
			return work
		}
	}
	return nil
}

func (ct *contact) resultTextForExecution(execution *issueExecution) string {
	if execution == nil {
		return ""
	}
	// New issue work messages live in the issue-owned session projection, not
	// in the conversation transcript.
	for _, issue := range ct.Issues {
		for index := len(issue.Work.Messages) - 1; index >= 0; index-- {
			m := issue.Work.Messages[index]
			if m.Kind != msgContact || strings.TrimSpace(m.Text) == "" {
				continue
			}
			if execution.TaskID != "" && m.TaskID == execution.TaskID {
				return strings.TrimSpace(m.Text)
			}
		}
	}
	// Legacy executions used the conversation session; keep their actual
	// history readable without migrating them.
	for index := len(ct.Messages) - 1; index >= 0; index-- {
		m := ct.Messages[index]
		if m.Kind != msgContact || strings.TrimSpace(m.Text) == "" {
			continue
		}
		if execution.TaskID != "" && m.TaskID == execution.TaskID {
			return strings.TrimSpace(m.Text)
		}
		if execution.TaskID == "" && execution.MessageID != "" && m.MessageID == execution.MessageID {
			return strings.TrimSpace(m.Text)
		}
	}
	return ""
}

func (a *appModel) issueResultContext(c *ui.Context, t *ui.Theme, issue *productIssue, work *workItem) {
	index, _ := issueExecutionForTask(issue, work.TaskID)
	msgs := locales.In(c.Locale())
	text := msgs.IssueRunResultSummary(strconv.Itoa(index+1), issue.Goal)
	if work.State == workAccepted || work.State == workRunning || work.State == workStopping {
		text = msgs.IssueRunInProgressSummary(strconv.Itoa(index+1), issue.Goal)
	}
	ui.Text(c, text).FontSize(typeAction).FontWeight(500).TextColor(t.TextMuted).MaxLines(2)
}

func latestIssueExecution(issue *productIssue) *issueExecution {
	if issue == nil {
		return nil
	}
	for index := len(issue.Executions) - 1; index >= 0; index-- {
		if issue.Executions[index] != nil {
			return issue.Executions[index]
		}
	}
	return nil
}

func issueHasReadableResult(ct *contact, execution *issueExecution) bool {
	if execution == nil || execution.Status != issueExecutionCompleted {
		return false
	}
	if strings.TrimSpace(ct.resultTextForExecution(execution)) != "" {
		return true
	}
	work := workFor(ct, execution.TaskID)
	if work == nil {
		return false
	}
	for _, tool := range work.Tools {
		if tool != nil && tool.Finished && strings.TrimSpace(tool.Output) != "" {
			return true
		}
	}
	return false
}

func (a *appModel) issuePrimaryAction(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) string {
	msgs := locales.In(c.Locale())
	editable := !issue.Archived && issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled
	if question := issue.openQuestion(); question != nil && editable {
		if strings.TrimSpace(question.Text) == "" {
			secondaryButton(c, msgs.IssueReloadTask()).OnClick(func() { a.reloadIssueWorkByID(ct.ID, issue.ID) })
			return "question-missing"
		}
		ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			secondaryButton(c, msgs.WorkAnswer()).FontWeight(500).OnClick(func() { a.openIssueSupplementForm(issue.ID) })
		})
		return "answer"
	}
	startReason := ""
	if !issueManagementTerminal(issue) && !issue.Archived {
		startReason = issueLifecycleStartBlocked(issue)
		if startReason == "" {
			startReason = a.canStartIssue(ct, issue)
		}
		if startReason == "" {
			startReason = issueRunBlocked(issue)
		}
		if startReason == "" {
			label := msgs.WorkStart()
			if len(issue.Executions) > 0 {
				label = msgs.WorkContinue()
			}
			if issue.Supplements != nil && issue.Supplements.pendingCount() > 0 {
				if len(issue.Executions) == 0 {
					label = msgs.WorkStartWithSupplement()
				} else {
					label = msgs.WorkContinueWithSupplement()
				}
			}
			ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
				secondaryButton(c, label).FontWeight(500).OnClick(func() {
					if !a.runIssue(issue.ID) {
						a.setIssueActionNotice(a.issueFormError, a.issueFormNotice, a.issueFormNoticeDetail)
					}
				})
			})
			return "continue"
		}
	}
	if latest := latestIssueExecution(issue); issueHasReadableResult(ct, latest) {
		ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			read := secondaryButton(c, msgs.WorkReadResult()).FontWeight(500).OnClick(func() {
				a.openIssueResultFromLatest(issue.ID, latest.ID)
			})
			if a.issueResultShouldFocus(issue.ID, latest.ID, true) {
				read.Focus()
				a.consumeIssueResultReturnFocus()
			}
		})
		return "read"
	}
	if startReason != "" {
		ui.Text(c, msgs.WorkNextActionUnavailable(sendBlockText(msgs, startReason))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted).MaxLines(2)
	}
	return ""
}

func (a *appModel) issueLifecycleActions(c *ui.Context, t *ui.Theme, issue *productIssue) {
	msgs := locales.In(c.Locale())
	if issue.Archived {
		ui.Row(c).Wrap().Gap(6).Children(func() {
			secondaryButton(c, msgs.WorkRestore()).OnClick(func() {
				if !a.restoreIssue(issue.ID) {
					a.setIssueActionNotice("Could not restore this issue.", "restore-failed", "")
				}
			})
		})
		return
	}
	editable := issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled
	if !editable {
		if reason := issueRunBlocked(issue); reason == "" {
			ui.Row(c).Wrap().Gap(6).Children(func() {
				secondaryButton(c, msgs.WorkReopen()).OnClick(func() {
					if !a.reopenIssue(issue.ID) {
						a.setIssueActionNotice("Could not reopen this issue.", "reopen-failed", "")
					}
				})
			})
		} else {
			ui.Text(c, msgs.WorkNextActionUnavailable(reason)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted).MaxLines(2)
		}
		return
	}
	ui.Row(c).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
		secondaryButton(c, msgs.WorkCancel()).TextColor(t.Warning).OnClick(func() {
			if !a.cancelIssueProduct(issue.ID) {
				a.setIssueActionNotice("Could not cancel this issue; its state was not changed.", "cancel-failed", "")
			}
		})
	})
	if issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle && issueRunBlocked(issue) == "" {
		ui.Text(c, msgs.WorkCompletionEvidence()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		draft := a.completionDraft(issue)
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			preferenceInput(c, draft, false).Label(msgs.WorkCompletionEvidence()).Placeholder(msgs.WorkCompletionEvidencePlaceholder()).Grow(1)
			secondaryButton(c, msgs.WorkComplete()).OnClick(func() {
				if !a.completeIssue(issue.ID, *draft) {
					a.setIssueActionNotice("Could not complete this issue.", "complete-failed", "")
				}
			})
		})
	}
}

func (a *appModel) issueExecutionCheckHint(msgs locales.Messages, ct *contact, issue *productIssue, execution *issueExecution) string {
	if a.runtimeStatus != runtimeStateReady || a.rt == nil || a.shutdownStarted {
		return msgs.SendDisconnectedState()
	}
	if ct != a.selectedContact() || issue == nil || ct.issueByID(issue.ID) != issue || execution == nil || execution.SessionID == "" || execution.TaskID == "" {
		return msgs.RecoveryTaskAssociationMissing()
	}
	owner, current := ct.executionByID(execution.ID)
	if owner != issue || current != execution {
		return msgs.RecoveryTaskAssociationMissing()
	}
	return ""
}

func (a *appModel) issueCard(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	msgs := locales.In(c.Locale())
	editable := !issue.Archived && issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled
	expanded := a.issueDetailOpen[issue.ID]
	ui.Column(c).Padding(8, 0).Gap(8).Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, issue.Goal).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
		})
		if a.issueActionError != "" {
			text := a.issueActionError
			if a.issueActionNotice != "" {
				text = issueNoticeText(locales.In(c.Locale()), a.issueActionNotice, a.issueActionNoticeDetail)
			}
			ui.Text(c, text).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
		}
		if question := issue.openQuestion(); question != nil {
			display := issueDisplayStatusFor(issue)
			ui.Column(c).Padding(8, 0).Gap(4).Children(func() {
				ui.Text(c, issueDisplayLabel(locales.In(c.Locale()), display)).FontSize(typeAction).FontWeight(500).TextColor(issueDisplayColor(t, display))
				if strings.TrimSpace(question.Text) == "" {
					ui.Text(c, msgs.QuestionUnavailableTitle()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
					ui.Text(c, msgs.QuestionUnavailableHint()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
					secondaryButton(c, msgs.IssueReloadTask()).OnClick(func() { a.reloadIssueWorkByID(ct.ID, issue.ID) })
				} else {
					ui.Text(c, question.Text).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
				}
			})
		}
		primaryKind := a.issuePrimaryAction(c, t, ct, issue)
		if issue.Work.SendError != "" || issue.Work.SendState == sendUnknown {
			text := issue.Work.SendError
			if issue.Work.SendErrorKind != "" {
				text = contactNoticeText(locales.In(c.Locale()), issue.Work.SendErrorKind, "")
			}
			if issue.Work.SendState == sendUnknown {
				text = a.issueExecutionCheckHint(msgs, ct, issue, latestIssueExecution(issue))
				if text == "" {
					text = msgs.SupplementUnknown()
				}
			}
			if issue.Work.SendErrorKind == "" && issue.Work.SendState != sendUnknown {
				text = msgs.RecoveryRequestFailed()
			}
			a.recoveryNotice(c, t, "issue-send:"+ct.ID+":"+issue.ID, text, issue.Work.SendError, true)
		}
		a.issueLatestResult(c, t, ct, issue, primaryKind != "read")
		if latest := latestIssueExecution(issue); latest != nil && latest.Status == issueExecutionCompleted &&
			issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled && issueHasRemainingRequirements(issue) {
			ui.Text(c, msgs.IssueEndedOpen()).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.TextMuted)
		}
		a.issueRequirementSummary(c, t, issue)
		a.issueLifecycleActions(c, t, issue)
		label := msgs.IssueMoreActions()
		if expanded {
			label = msgs.IssueHideActions()
		}
		ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			secondaryButton(c, label).OnClick(func() {
				if a.issueDetailOpen == nil {
					a.issueDetailOpen = map[string]bool{}
				}
				a.issueDetailOpen[issue.ID] = !a.issueDetailOpen[issue.ID]
			})
		})
		if expanded {
			ui.Row(c).Wrap().Gap(6).Children(func() {
				if editable {
					secondaryButton(c, msgs.IssueEdit()).OnClick(func() { a.openEditIssueForm(issue) })
				}
				if issue.WorkSessionID != "" {
					modelButton := secondaryButton(c, msgs.IssueModel()).OnClick(func() {
						a.openSessionModel(issue.WorkSessionID, "work", issue.Goal)
					})
					if a.focusIssueModel {
						modelButton.Focus()
						a.focusIssueModel = false
					}
				}
			})
			a.issueActions(c, t, ct, issue, editable)
			evidence := issueEvidenceText(locales.In(c.Locale()), issue)
			if issue.Evidence != "" && issue.Status != issueStatusCompleted {
				evidence = msgs.IssuePreviousEvidence(issue.Evidence)
			}
			ui.Text(c, msgs.WorkCompletionEvidence()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
			ui.Text(c, evidence).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text).MaxLines(4)
			a.issueRequirementEvidence(c, t, ct, issue, editable)
			a.issueSupplementSection(c, t, ct, issue, editable)
			a.issueExecutions(c, t, ct, issue)
		} else if a.issueSupplementFormID == issue.ID {
			a.issueSupplementSection(c, t, ct, issue, editable)
		}
		if executionHasUnknownRecovery(issue) {
			ui.Text(c, msgs.IssueRecoveryAvailable()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		}
	})
}

func (a *appModel) issueLatestResult(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, showRead bool) {
	msgs := locales.In(c.Locale())
	execution := latestIssueExecution(issue)
	if execution == nil {
		ui.Text(c, msgs.IssueNoExecution()).FontSize(typeAction).FontWeight(500).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, msgs.IssueLatestExecution()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
	ui.Text(c, issueExecutionText(msgs, execution.Status)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	text := strings.TrimSpace(ct.resultTextForExecution(execution))
	work := workFor(ct, execution.TaskID)
	switch {
	case execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning:
		if text == "" {
			ui.Text(c, msgs.IssueNoResultRunning()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text).MaxLines(3)
		} else {
			ui.Text(c, msgs.IssueCurrentResponse(truncate(text, 300))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text).MaxLines(3)
		}
	case execution.Status == issueExecutionCompleted:
		if text == "" {
			if work != nil && len(work.Tools) > 0 {
				ui.Text(c, msgs.IssueNoAssistantTools()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text).MaxLines(3)
			} else {
				ui.Text(c, msgs.IssueNoAssistant()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text).MaxLines(3)
			}
		} else {
			ui.Text(c, msgs.IssueResultPreview(truncate(text, 300))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text).MaxLines(3)
		}
	default:
		if text != "" {
			ui.Text(c, msgs.IssueResultPreview(truncate(text, 300))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text).MaxLines(3)
		}
		reason := strings.TrimSpace(execution.Detail)
		if reason == "" {
			switch execution.Status {
			case issueExecutionPending:
				reason = msgs.IssuePreparedNotDispatched()
			case issueExecutionNotDispatched:
				reason = msgs.IssueNoTaskDispatched()
			case issueExecutionRejected:
				reason = msgs.IssueExecutionRejectedDetail()
			case issueExecutionFailed:
				reason = msgs.IssueExecutionFailedDetail()
			case issueExecutionInterrupted:
				reason = msgs.IssueExecutionInterruptedDetail()
			default:
				reason = msgs.IssueExecutionUnknownDetail()
			}
		}
		summary := reason
		if execution.Status == issueExecutionFailed {
			summary = runFailureSummary(msgs, execution.Detail)
		}
		if execution.Status == issueExecutionUnknown {
			summary = msgs.IssueExecutionUnknownDetail()
		} else if execution.Status == issueExecutionPending {
			summary = msgs.IssuePreparedNotDispatched()
		}
		a.recoveryNotice(c, t, "latest:"+ct.ID+":"+execution.ID, summary, execution.Detail, true)
	}
	if showRead {
		ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			read := secondaryButton(c, locales.In(c.Locale()).WorkReadResult()).OnClick(func() { a.openIssueResultFromLatest(issue.ID, execution.ID) })
			if a.issueResultShouldFocus(issue.ID, execution.ID, true) {
				read.Focus()
				a.consumeIssueResultReturnFocus()
			}
		})
	}
	if work != nil {
		ui.Row(c).Wrap().Gap(6).Children(func() {
			secondaryButton(c, locales.In(c.Locale()).IssueViewExecution()).OnClick(func() { a.showIssueWork(issue.ID, execution.TaskID) })
		})
	}
}

func (a *appModel) issueRequirementSummary(c *ui.Context, t *ui.Theme, issue *productIssue) {
	if len(issue.Requirements) == 0 {
		ui.Text(c, locales.In(c.Locale()).IssueRequirements()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
		ui.Text(c, locales.In(c.Locale()).IssueNoRequirements()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, locales.In(c.Locale()).IssueRequirements()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
	for _, requirement := range issue.Requirements {
		if requirement == nil {
			continue
		}
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, requirement.Text).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text).Grow(1)
			ui.Text(c, requirementStateText(locales.In(c.Locale()), requirement.State)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(requirementStateColor(t, requirement.State))
		})
	}
}

func (a *appModel) issueRequirementEvidence(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, editable bool) {
	if len(issue.Requirements) == 0 {
		return
	}
	ui.Text(c, locales.In(c.Locale()).IssueRequirementEvidence()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
	for _, requirement := range issue.Requirements {
		if requirement == nil {
			continue
		}
		ui.Column(c).Padding(6).Radius(6).Background(t.Surface).Gap(3).Children(func() {
			ui.Text(c, requirement.Text).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text).MaxLines(2)
			if editable {
				draft := a.requirementDraft(requirement)
				preferenceInput(c, draft, false).Label(locales.In(c.Locale()).IssueEvidenceFor(requirement.Text)).Placeholder(locales.In(c.Locale()).IssueEvidencePlaceholder())
				ui.Row(c).Gap(4).Children(func() {
					for _, action := range []struct {
						label, state string
						color        ui.Color
					}{
						{locales.In(c.Locale()).IssueRequirementSatisfied(), requirementSatisfied, t.Success},
						{locales.In(c.Locale()).IssueRequirementUnmet(), requirementUnmet, t.Warning},
						{locales.In(c.Locale()).IssueRequirementUnverified(), requirementUnverified, t.TextMuted},
					} {
						action := action
						secondaryButton(c, action.label).TextColor(action.color).OnClick(func() {
							if !a.setIssueRequirementState(issue.ID, requirement.ID, action.state, *draft) {
								a.setIssueActionNotice("Could not save the requirement state.", "requirement-save-failed", "")
							}
						})
					}
				})
			} else if requirement.Evidence != "" {
				ui.Text(c, locales.In(c.Locale()).IssueEvidencePrefix(requirement.Evidence)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted).MaxLines(2)
			}
		})
	}
}

func (a *appModel) issueExecutions(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	if len(issue.Executions) == 0 {
		return
	}
	msgs := locales.In(c.Locale())
	ui.Text(c, locales.In(c.Locale()).IssueExecutionHistory()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
	for index, execution := range issue.Executions {
		if execution == nil {
			continue
		}
		w := workFor(ct, execution.TaskID)
		ui.Column(c).Padding(8).Radius(8).Background(t.Surface).Gap(4).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, locales.In(c.Locale()).IssueRunHeader(strconv.Itoa(index+1), issueExecutionText(locales.In(c.Locale()), execution.Status))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
				if execution.RequirementsRevision > 0 {
					ui.Text(c, locales.In(c.Locale()).IssueRunRevision(strconv.Itoa(execution.RequirementsRevision))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
				}
			})
			contextCount := len(executionTaskContextItems(ct, issue, execution))
			if execution.Prompt != "" || contextCount > 0 {
				label := msgs.IssueShowSentSnapshot()
				if contextCount > 0 {
					label = msgs.IssueSentContextCount(contextCount)
					if execution.ContextExpanded {
						label = msgs.IssueHideSentContext()
					}
				} else if execution.ContextExpanded {
					label = msgs.IssueHideSentSnapshot()
				}
				secondaryButton(c, label).OnClick(func() {
					execution.ContextExpanded = !execution.ContextExpanded
				})
			}
			if execution.ContextExpanded {
				if execution.Prompt != "" {
					ui.Text(c, locales.In(c.Locale()).IssueSentSnapshot(truncate(execution.Prompt, 240))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted).MaxLines(3)
				}
				a.executionTaskContextView(c, t, ct, issue, execution)
			}
			if execution.Detail != "" {
				if execution.Status == issueExecutionFailed || execution.Status == issueExecutionUnknown || execution.Status == issueExecutionPending {
					summary := a.issueExecutionCheckHint(msgs, ct, issue, execution)
					if summary == "" {
						summary = msgs.SupplementUnknown()
					}
					if execution.Status == issueExecutionFailed {
						summary = runFailureSummary(msgs, execution.Detail)
					} else if execution.Status == issueExecutionPending {
						summary = msgs.IssuePreparedNotDispatched()
					}
					a.recoveryNotice(c, t, "execution:"+ct.ID+":"+execution.ID, summary, execution.Detail, true)
				} else {
					ui.Text(c, execution.Detail).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
				}
			}
			if execution.Status == issueExecutionUnknown || execution.Status == issueExecutionPending {
				ui.Row(c).Gap(6).Children(func() {
					checkHint := a.issueExecutionCheckHint(msgs, ct, issue, execution)
					secondaryButton(c, msgs.IssueCheckExecution()).Label(msgs.IssueCheckExecution()).Disabled(checkHint != "").OnClick(func() {
						if a.issueExecutionCheckHint(locales.In(c.Locale()), ct, issue, execution) != "" {
							return
						}
						if !a.reconcileIssueExecution(issue.ID, execution.ID) {
							a.setIssueActionNotice(execution.Detail, "", "")
						}
					})
					if execution.TaskID == "" && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle {
						secondaryButton(c, locales.In(c.Locale()).IssueConfirmNotDispatched()).OnClick(func() {
							if !a.confirmIssueExecutionNotDispatched(issue.ID, execution.ID) {
								a.setIssueActionNotice("Could not confirm this execution.", "confirm-failed", "")
							}
						})
					}
				})
				if hint := a.issueExecutionCheckHint(msgs, ct, issue, execution); hint != "" {
					ui.Text(c, hint).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				}
			}
			ui.Column(c).AlignItems(ui.Start).Gap(6).Children(func() {
				resultButton := secondaryButton(c, msgs.IssueOpenResultRun(strconv.Itoa(index+1))).OnClick(func() { a.openIssueResult(issue.ID, execution.ID) })
				if a.issueResultShouldFocus(issue.ID, execution.ID, false) {
					resultButton.Focus()
					a.consumeIssueResultReturnFocus()
				}
				if w != nil {
					secondaryButton(c, msgs.IssueViewExecutionRun(strconv.Itoa(index+1))).OnClick(func() { a.showIssueWork(issue.ID, execution.TaskID) })
				}
			})
		})
	}
}

func (a *appModel) issueActions(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, editable bool) {
	if issue.Archived {
		return
	}
	if editable {
		msgs := locales.In(c.Locale())
		taskContextButton := func() {
			label := msgs.TaskContextCount(issue.Context.count())
			button := secondaryButton(c, label).OnClick(func() { a.openIssueTaskContext(issue.ID) })
			if a.focusIssueContext {
				button.Focus()
				a.focusIssueContext = false
			}
		}
		addSupplementButton := func() {
			label := msgs.SupplementAdd()
			if issue.Supplements != nil {
				if count := issue.Supplements.pendingCount() + len(issue.Supplements.Batches); count > 0 {
					label = msgs.SupplementActionCount(count)
				}
			}
			secondaryButton(c, label).OnClick(func() { a.openIssueSupplementForm(issue.ID) })
		}
		waitButton := func() {
			if issue.openQuestion() != nil {
				return
			}
			if issue.Status == issueStatusWaitingUser || issue.Status == issueStatusNeedsAttention {
				secondaryButton(c, locales.In(c.Locale()).IssueResume()).OnClick(func() {
					if !a.resumeIssue(issue.ID) {
						a.setIssueActionNotice("Could not resume this issue.", "resume-failed", "")
					}
				})
			} else if issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle {
				secondaryButton(c, locales.In(c.Locale()).IssueMarkWaiting()).OnClick(func() {
					if !a.markIssueWaiting(issue.ID) {
						a.setIssueActionNotice("Could not mark this issue waiting.", "wait-failed", "")
					}
				})
			}
		}

		ui.Row(c).Wrap().Gap(6).Children(func() {
			taskContextButton()
			addSupplementButton()
		})
		ui.Row(c).Wrap().Gap(6).Children(waitButton)
	}

	ui.Row(c).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
		secondaryButton(c, locales.In(c.Locale()).IssueArchive()).OnClick(func() {
			if !a.archiveIssue(issue.ID) {
				a.setIssueActionNotice("Could not archive this issue.", "archive-failed", "")
			}
		})
	})
}

func (a *appModel) issueList(c *ui.Context, t *ui.Theme, ct *contact) {
	msgs := locales.In(c.Locale())
	nav := ct.issueNavigation()
	issues := ct.issuesForFilter(nav.Filter)
	if len(issues) == 0 {
		ui.Text(c, msgs.IssueNoIssuesIn(issueFilterText(msgs, nav.Filter))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		return
	}
	for _, issue := range issues {
		a.issueOverviewRow(c, t, ct, issue)
	}
}

func (a *appModel) issueStatusColor(t *ui.Theme, issue *productIssue) ui.Color {
	return issueDisplayColor(t, issueDisplayStatusFor(issue))
}

func requirementStateText(msgs locales.Messages, state string) string {
	switch state {
	case requirementSatisfied:
		return msgs.IssueRequirementSatisfied()
	case requirementUnmet:
		return msgs.IssueRequirementUnmet()
	default:
		return msgs.IssueRequirementUnverified()
	}
}

func requirementStateColor(t *ui.Theme, state string) ui.Color {
	switch state {
	case requirementSatisfied:
		return t.Success
	case requirementUnmet:
		return t.Warning
	default:
		return t.TextMuted
	}
}

func (a *appModel) workEmptyState(c *ui.Context, t *ui.Theme, ct *contact) {
	if !a.demo && ct.SessionID != "" && !ct.Loaded {
		ui.Text(c, locales.In(c.Locale()).WorkLoadingHistoryTitle()).FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		ui.Text(c, locales.In(c.Locale()).WorkLoadingHistoryHint()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		if a.runtimeStatus != runtimeStateReady {
			ui.Text(c, locales.In(c.Locale()).WorkHistoryNotReady()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
		}
		return
	}
	if !a.demo && a.runtimeStatus != runtimeStateReady {
		ui.Text(c, locales.In(c.Locale()).WorkUnavailableTitle()).FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		ui.Text(c, locales.In(c.Locale()).WorkUnavailableHint()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, locales.In(c.Locale()).WorkNoCurrent()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
	ui.Text(c, locales.In(c.Locale()).WorkNoCurrentHint(ct.Name)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
}

func (a *appModel) issueWorkDisclosureSnapshot(ct *contact, issue *productIssue, work *workItem) (issueExecutionDisclosure, func() bool) {
	snapshot := captureIssueExecutionDisclosure(ct, issue, executionForTask(issue, work.TaskID))
	state, detail := work.State, work.Detail
	return snapshot, func() bool {
		return a.issueExecutionDisclosureCurrent(snapshot) && a.issueWorkViewCurrent(ct, issue, work, state, detail) &&
			issueExecutionStatusFromWork(state) == snapshot.status &&
			strings.TrimSpace(detail) != "" && strings.TrimSpace(detail) == strings.TrimSpace(snapshot.detail)
	}
}

func (a *appModel) issueWorkViewCurrent(ct *contact, issue *productIssue, work *workItem, state workState, detail string) bool {
	if a.shutdownStarted || a.selectedContact() != ct || a.contactByID(ct.ID) != ct || ct.issueByID(issue.ID) != issue ||
		issue.ContactID != ct.ID || !a.detailIs(detailWork) || a.settingsOpen || a.mainPageActive() || a.aux != "" {
		return false
	}
	nav := ct.issueNavigation()
	return nav.DetailID == issue.ID && !nav.ShowConversationWork && ct.SelectedIssueID == issue.ID &&
		ct.SelectedWorkTaskID == work.TaskID && ct.issueWork(issue) == work && workFor(ct, work.TaskID) == work &&
		work.State == state && work.Detail == detail
}

func (a *appModel) workCard(c *ui.Context, t *ui.Theme, w *workItem, isCurrent, issueWork bool, disclosure ...func(bool, func())) {
	msgs := locales.In(c.Locale())
	ui.Column(c).Padding(8, 0).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Gap(4).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, workStateText(msgs, w)).FontSize(typeAction).FontWeight(500).TextColor(a.workStateColor(w.State))
			ui.Spacer(c)
			if isCurrent && (w.State == workAccepted || w.State == workRunning || w.State == workStopping) {
				label := msgs.WorkStopTask()
				if issueWork {
					label = msgs.WorkStopIssueTask()
				}
				if w.State == workStopping {
					label = msgs.WorkStoppingTask()
					if issueWork {
						label = msgs.WorkStoppingIssue()
					}
				}
				stop := secondaryButton(c, label).FontWeight(500).OnClick(func() {
					if issueWork {
						a.cancelWorkTask(a.selectedContact(), w.TaskID)
					} else {
						a.cancelSelected()
					}
				})
				if w.State == workStopping {
					stop.Disabled(true)
				}
			}
		})
		if w.Prompt != "" {
			ui.Text(c, w.Prompt).FontSize(typeBody).FixedLineHeight(lineBody).MaxLines(4).TextColor(a.workTheme.WorkText)
		}
		if len(w.Tools) > 0 {
			last := w.Tools[len(w.Tools)-1]
			action := last.Name
			if last.Finished {
				action = msgs.WorkLastAction(action)
			} else {
				action = msgs.WorkRunningAction(action)
			}
			ui.Text(c, action).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(a.workTheme.WorkMuted)
		}
		if w.Detail != "" {
			if w.State == workFailed || formattedRunFailure(w.Detail) {
				a.recoveryNotice(c, t, "work:"+a.selectedContact().ID+":"+w.TaskID, runFailureSummary(msgs, w.Detail), w.Detail, true, disclosure...)
			} else {
				ui.Text(c, w.Detail).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(a.workTheme.WorkAttention)
			}
		}
	})
}

func (a *appModel) workActivity(c *ui.Context, t *ui.Theme, w *workItem) {
	ui.Text(c, locales.In(c.Locale()).WorkActivity()).FontSize(typeMeta).FixedLineHeight(lineMeta).FontWeight(500).TextColor(t.TextMuted)
	if len(w.Tools) == 0 {
		ui.Text(c, locales.In(c.Locale()).WorkNoToolActivity()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		return
	}
	a.toolCalls(c, t, w.Tools)
}

func (a *appModel) unlinkedActivity(c *ui.Context, t *ui.Theme, tools []*workTool) {
	if len(tools) == 0 {
		return
	}
	ui.Text(c, locales.In(c.Locale()).WorkEarlierActivity()).FontSize(typeAction).FontWeight(500).TextColor(t.TextMuted)
	ui.Text(c, locales.In(c.Locale()).WorkNoTaskIDHint()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	a.toolCalls(c, t, tools)
}

func (a *appModel) toolCalls(c *ui.Context, t *ui.Theme, tools []*workTool) {
	for _, call := range tools {
		call := call
		ui.Column(c).Padding(8).Radius(8).Background(a.workTheme.Well).Gap(3).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, toolTitleFor(call.Name, call.Args)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
				ui.Spacer(c)
				state := locales.In(c.Locale()).WorkToolRunningState()
				if call.Finished {
					state = locales.In(c.Locale()).WorkToolFinishedState()
				}
				ui.Text(c, state).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
			})
			if call.Output != "" {
				summary := toolOutputSummary(locales.In(c.Locale()), call.Output)
				if summary != "" {
					// Keep long output (including unbroken JSON/paths) in its
					// own full-width line so it cannot push the actions out of
					// the card.
					ui.Text(c, locales.In(c.Locale()).WorkPreview(summary)).FontSize(typeBody).FixedLineHeight(lineBody).MaxLines(4).TextColor(t.TextMuted)
				}
				label := locales.In(c.Locale()).WorkDetails()
				if call.Expanded {
					label = locales.In(c.Locale()).WorkHide()
				}
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					if !call.Expanded {
						secondaryButton(c, locales.In(c.Locale()).CopyReceivedOutputLabel()).OnClick(func() {
							if err := writeClipboardText(call.Output, c.Services().WriteClipboard, c.Services().ReadClipboard); err != nil {
								c.Toast(locales.In(c.Locale()).CopyFailed(err.Error()))
							} else {
								c.Toast(locales.In(c.Locale()).CopiedReceivedOutput())
							}
						})
					}
					secondaryButton(c, label).OnClick(func() { call.Expanded = !call.Expanded })
				})
				if call.Expanded {
					markdownReader(c, call.Output, markdownReaderOptions{PlainText: true, CopyLabel: locales.In(c.Locale()).CopyReceivedOutputLabel()}).
						FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
				}
			} else if call.Started && !call.Finished {
				ui.Text(c, locales.In(c.Locale()).WorkToolRunning()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
			}
		})
	}
}

func toolTitleFor(name, args string) string {
	if name == "" {
		name = "Tool"
	}
	if args == "" {
		return name
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err == nil {
		for _, key := range []string{"path", "command"} {
			if value, ok := parsed[key].(string); ok && strings.TrimSpace(value) != "" {
				return name + ": " + truncate(value, 70)
			}
		}
	}
	return name + ": " + truncate(args, 70)
}

const (
	workNoticeResuming                = "resuming"
	workNoticeRunning                 = "running"
	workNoticeStarting                = "starting"
	workNoticeCompacting              = "compacting"
	workNoticeWaitingModel            = "waiting-model"
	workNoticeThinking                = "thinking"
	workNoticeWriting                 = "writing"
	workNoticeRunningTool             = "running-tool"
	workNoticeLastAction              = "last-action"
	workNoticeStateAccepted           = "state-accepted"
	workNoticeStateRunning            = "state-running"
	workNoticeStateStopping           = "state-stopping"
	workNoticeStateCompleted          = "state-completed"
	workNoticeStateFailed             = "state-failed"
	workNoticeStateInterrupted        = "state-interrupted"
	workNoticeStateStoppedInterrupted = "state-stopped-interrupted"
	workNoticeStateUnknown            = "state-unknown"
)

func workNoticeForWork(w *workItem) string {
	if w.State == workInterrupted && w.StopRequested {
		return workNoticeStateStoppedInterrupted
	}
	return workNoticeForState(w.State)
}

func workNoticeForState(state workState) string {
	switch state {
	case workAccepted:
		return workNoticeStateAccepted
	case workRunning:
		return workNoticeStateRunning
	case workStopping:
		return workNoticeStateStopping
	case workCompleted:
		return workNoticeStateCompleted
	case workFailed:
		return workNoticeStateFailed
	case workInterrupted:
		return workNoticeStateInterrupted
	default:
		return workNoticeStateUnknown
	}
}

func workNoticeText(msgs locales.Messages, kind, arg string) string {
	switch kind {
	case workNoticeResuming:
		return msgs.WorkStatusResuming()
	case workNoticeRunning:
		return msgs.WorkStatusRunning()
	case workNoticeStarting:
		return msgs.WorkStatusStarting()
	case workNoticeCompacting:
		return msgs.WorkStatusCompacting()
	case workNoticeWaitingModel:
		return msgs.WorkStatusWaitingModel()
	case workNoticeThinking:
		return msgs.WorkStatusThinking()
	case workNoticeWriting:
		return msgs.WorkStatusWriting()
	case workNoticeRunningTool:
		return msgs.WorkRunningAction(arg)
	case workNoticeLastAction:
		return msgs.WorkLastAction(arg)
	case workNoticeStateAccepted:
		return msgs.WorkStateAccepted()
	case workNoticeStateRunning:
		return msgs.WorkStateWorking()
	case workNoticeStateStopping:
		return msgs.WorkStateStopping()
	case workNoticeStateCompleted:
		return msgs.WorkStateCompleted()
	case workNoticeStateFailed:
		return msgs.WorkStateFailed()
	case workNoticeStateInterrupted:
		return msgs.WorkStateInterrupted()
	case workNoticeStateStoppedInterrupted:
		return msgs.WorkStateStoppedInterrupted()
	case workNoticeStateUnknown:
		return msgs.WorkStateUnknown()
	default:
		return kind
	}
}

func (a *appModel) workActivitySummary(ct *contact) (string, string) {
	if w := ct.activeWork(); w != nil {
		if len(w.Tools) > 0 {
			last := w.Tools[len(w.Tools)-1]
			if !last.Finished {
				return workNoticeRunningTool, last.Name
			}
			return workNoticeLastAction, last.Name
		}
		return workNoticeForWork(w), ""
	}
	if w := ct.recentWork(); w != nil {
		return workNoticeForWork(w), ""
	}
	return "", ""
}

func executionHasUnknownRecovery(issue *productIssue) bool {
	for _, execution := range issue.Executions {
		if execution.Status == issueExecutionUnknown || execution.Status == issueExecutionPending {
			return true
		}
	}
	return false
}

func (a *appModel) cancelWorkTask(ct *contact, taskID string) {
	if issue, _ := ct.issueForTask(taskID); issue != nil && issue.Work.ActiveTaskID == taskID {
		a.cancelIssue(issue.ID)
		return
	}
	a.cancelSelected()
}
