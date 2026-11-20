package main

import (
	"encoding/json"
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
	defaultLeftPaneWidth  float32 = 238
	minLeftPaneWidth      float32 = 180
	maxLeftPaneWidth      float32 = 420
	defaultRightPaneWidth float32 = 340
	minRightPaneWidth     float32 = 280
	maxRightPaneWidth     float32 = 440
	minCenterPaneWidth    float32 = 480
	paneSeparatorWidth    float32 = 1

	avatarRailWidth      float32 = 56
	fullRailBreakpoint   int     = 1180
	avatarRailBreakpoint int     = 900
	minAvatarDetailWidth float32 = 300
	effectiveCenterWidth float32 = 512
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
	return a.activeDetail() != "" && !a.railCollapsed && !a.workOverlay() &&
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
	threePaneAtStart := a.activeDetail() != ""
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
	if a.activeDetail() == "" {
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
	a.profileDetail(c, t, narrow)
}

func (ct *contact) issueWork(issue *productIssue) *workItem {
	if issue == nil {
		return nil
	}
	for _, taskID := range []string{issue.Work.ActiveTaskID, ct.ActiveTaskID, ct.LastTaskID} {
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
	return ""
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

func issueHasUnconfirmedRun(issue *productIssue) bool {
	for _, execution := range issue.Executions {
		if execution != nil && (execution.Status == issueExecutionUnknown || execution.Status == issueExecutionPending) {
			return true
		}
	}
	return false
}

func (a *appModel) cancelWorkTask(ct *contact, taskID string) {
	if issue, _ := ct.issueForTask(taskID); issue != nil {
		if issue.Work.ActiveTaskID == taskID {
			a.cancelIssueFor(ct, issue.ID)
		}
		return
	}
	if a.demo {
		a.cancelSelected()
		return
	}
	a.cancelRuntimeTask(ct, taskID)
}
