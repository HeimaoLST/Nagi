package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
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
		a.profileDetail(c, t)
	})
}

// detailPage is the non-modal narrow rendering of the same right detail. It
// replaces the whole workbench body, so the 480px reading column is not
// squeezed or covered by a backdrop/focus trap.
func (a *appModel) detailPage(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(a.workTheme.Panel).Children(func() {
		a.profileDetail(c, t)
	})
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

type productActivityIntent struct {
	OperationID string               `json:"operation_id"`
	BotID       string               `json:"bot_id"`
	ActivityID  string               `json:"activity_id"`
	Action      string               `json:"action"`
	Revision    uint64               `json:"revision"`
	ExecutionID string               `json:"execution_id,omitempty"`
	QuestionID  string               `json:"question_id,omitempty"`
	Text        string               `json:"text,omitempty"`
	Materials   []pi.ProductMaterial `json:"materials,omitempty"`
}

type productActivityForm struct {
	target    productActivityIntent
	text      string
	materials taskContext
	focus     bool
}

type productActivityPanel struct {
	focusSource   bool
	client        *pi.ProductClient
	revision      uint64
	loaded        bool
	reading       bool
	changing      bool
	open          bool
	focusToggle   bool
	focusBack     bool
	items         []pi.ProductActivitySummary
	nextCursor    *string
	activityID    string
	activity      *pi.ProductActivity
	forms         map[string]*productActivityForm
	result        *pi.ProductActivityResult
	file          *issueResultFileInspection
	problem       string
	actionProblem string
	stopping      string
	scroll        ui.ScrollState
}

func (a *appModel) refreshProductActivities(ct *contact, more bool) {
	client := a.productActivityClient()
	if client == nil || a.dataRecoveryBlocksChanges() || a.selectedContact() != ct {
		return
	}
	p := &ct.activity
	if p.reading || p.changing {
		return
	}
	revision := a.productActivityRevision()
	params := pi.ProductActivityListParams{BotID: ct.ID, ProductPageParams: pi.ProductPageParams{Limit: 30}}
	if more && p.nextCursor != nil && p.client == client && p.revision == revision {
		params.Cursor = *p.nextCursor
	}
	p.reading = true
	id := p.activityID
	a.contactHistoryInFlight++
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		page, err := client.ListActivities(ctx, params)
		var detail pi.ProductActivity
		var detailErr error
		if id != "" {
			detail, detailErr = client.GetActivity(ctx, id)
		}
		a.updateUI(func() {
			defer a.finishActivityRead()
			p.reading = false
			if !a.currentActivityClient(ct, client) {
				return
			}
			p.client, p.revision, p.loaded = client, revision, true
			p.problem = a.connectionSafeMessage(err)
			if err == nil {
				if params.Cursor == "" {
					p.items = page.Items
				} else {
					p.items = append(p.items, page.Items...)
				}
				p.nextCursor = page.NextCursor
			}
			if id != "" && p.activityID == id {
				if detailErr != nil {
					p.activity = nil
					p.problem = a.connectionSafeMessage(detailErr)
				} else {
					p.activity = &detail
				}
			}
		})
	}()
}

func (a *appModel) currentActivityClient(ct *contact, client *pi.ProductClient) bool {
	return a.contactByID(ct.ID) == ct && a.productHost() != nil && a.productHost().product == client
}

func (a *appModel) finishActivityRead() {
	a.contactHistoryInFlight--
	if a.shutdownStarted {
		a.finishShutdown()
	}
}

func (a *appModel) openProductActivity(ct *contact, activityID string) {
	p := &ct.activity
	p.activityID, p.activity, p.result, p.file = activityID, nil, nil, nil
	p.scroll.Y = 0
	p.actionProblem, p.problem = "", ""
	p.focusBack = true
	a.refreshProductActivities(ct, false)
}

func (a *appModel) readProductActivityResult(ct *contact, activityID, executionID string) {
	p, client := &ct.activity, a.productActivityClient()
	if client == nil || p.reading || p.activityID != activityID || a.dataRecoveryBlocksChanges() {
		return
	}
	p.reading, p.problem, p.result, p.file = true, "", nil, nil
	a.contactHistoryInFlight++
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result, err := client.ActivityResult(ctx, productActivityExecution(activityID, executionID))
		a.updateUI(func() {
			defer a.finishActivityRead()
			p.reading = false
			if !a.currentActivityClient(ct, client) || p.activityID != activityID {
				return
			}
			p.problem = a.connectionSafeMessage(err)
			if err == nil && result.ActivityID == activityID && result.ExecutionID == executionID {
				p.result, p.scroll.Y = &result, 0
				p.focusBack = true
			}
		})
	}()
}

func productActivityExecution(activityID, executionID string) pi.ProductActivityExecutionParams {
	return pi.ProductActivityExecutionParams{ProductActivityParams: pi.ProductActivityParams{ActivityID: activityID}, ExecutionID: executionID}
}

func (a *appModel) editProductActivity(ct *contact, target productActivityIntent) {
	p := &ct.activity
	if p.forms == nil {
		p.forms = make(map[string]*productActivityForm)
	}
	form := p.forms[target.ActivityID]
	if form == nil {
		form = &productActivityForm{target: target}
		p.forms[target.ActivityID] = form
	}
	form.focus = true
}

func (a *appModel) chooseActivityMaterial(ct *contact, form *productActivityForm) {
	p, tc := &ct.activity, &form.materials
	if tc.fileChoosing || a.dataRecoveryBlocksChanges() || a.shutdownStarted {
		return
	}
	tc.fileRequest++
	request, editor, revision := tc.fileRequest, tc.fileForm, tc.fileForm.pathRevision
	viewGeneration := a.folderChoiceGeneration
	tc.fileChoosing = true
	a.contextFilePickers++
	pick := a.chooseContextFile
	if pick == nil {
		pick = mygo.Dialog.Open
	}
	label := locales.In(a.uiLocale).TaskContextChooseFile()
	opts := mygo.OpenDialogOptions{Parent: a.win, Title: label, ButtonLabel: label, DefaultPath: tc.formPath}
	go func() {
		if !filepath.IsAbs(opts.DefaultPath) {
			opts.DefaultPath, _ = os.UserHomeDir()
		}
		paths, err := pick(opts)
		a.updateUI(func() {
			a.contextFilePickers--
			if tc.fileRequest != request || tc.fileForm != editor {
				return
			}
			tc.fileChoosing = false
			if a.shutdownStarted || a.settingsOpen || a.folderChoiceGeneration != viewGeneration || a.selectedContact() != ct || !p.open || p.activityID != form.target.ActivityID || p.forms[p.activityID] != form || editor.pathRevision != revision {
				return
			}
			tc.focusFileChooser, tc.fileChoiceFailed = true, err != nil
			if err == nil && len(paths) > 0 {
				tc.formPath = paths[0]
				editor.pathRevision++
			}
		})
	}()
}

func (a *appModel) submitProductActivity(ct *contact, target productActivityIntent, form *productActivityForm) {
	p, client := &ct.activity, a.productActivityClient()
	if client == nil || a.selectedContact() != ct || p.activityID != target.ActivityID || p.changing || a.dataRecoveryBlocksChanges() || a.activityIntents[target.ActivityID].OperationID != "" {
		return
	}
	if form != nil {
		target.Text = form.text
		target.Materials = make([]pi.ProductMaterial, len(form.materials.items))
		for i, item := range form.materials.items {
			target.Materials[i] = pi.ProductMaterial{ID: item.ID, Name: item.Name, Text: item.Text, Source: item.Source, Revision: item.Revision}
		}
	}
	target.OperationID = newContactID()
	if a.activityIntents == nil {
		a.activityIntents = make(map[string]productActivityIntent)
	}
	a.activityIntents[target.ActivityID] = target
	if err := a.persist(); err != nil {
		delete(a.activityIntents, target.ActivityID)
		p.actionProblem = a.connectionSafeMessage(err)
		return
	}
	a.runProductActivityAction(ct, client, target, true)
}

func (a *appModel) runProductActivityAction(ct *contact, client *pi.ProductClient, target productActivityIntent, dispatch bool) {
	p := &ct.activity
	p.changing, p.actionProblem = true, ""
	a.submissionAsyncStarted()
	go func() {
		var accepted, rejected bool
		var err error
		if dispatch {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			accepted, err = target.dispatch(ctx, client)
			cancel()
			var command *pi.CommandError
			rejected = errors.As(err, &command) && (command.Delivery == pi.NotSent || command.Delivery == pi.Rejected)
		}
		if !accepted && !rejected {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			receipt, lookupErr := client.Operation(ctx, target.OperationID)
			cancel()
			if lookupErr == nil && target.matchesReceipt(receipt) {
				accepted, rejected = receipt.State == "accepted", receipt.State == "rejected"
			} else if err == nil {
				err = lookupErr
			}
		}
		a.updateUI(func() {
			defer a.submissionAsyncFinished()
			p.changing = false
			if !a.currentActivityClient(ct, client) || a.activityIntents[target.ActivityID].OperationID != target.OperationID {
				return
			}
			if p.activityID == target.ActivityID {
				p.actionProblem = a.connectionSafeMessage(err)
			}
			if accepted || rejected {
				delete(a.activityIntents, target.ActivityID)
				if err := a.persist(); err != nil {
					a.activityIntents[target.ActivityID] = target
					if p.activityID == target.ActivityID {
						p.actionProblem = a.connectionSafeMessage(err)
					}
				} else if accepted {
					if owner := a.contactByID(target.BotID); owner != nil && target.Action != "stop" {
						delete(owner.activity.forms, target.ActivityID)
					}
					if target.Action == "stop" {
						p.stopping = target.ExecutionID
					}
					p.actionProblem = ""
				} else if target.Action != "stop" && p.forms[target.ActivityID] == nil {
					a.editProductActivity(ct, target)
					form := p.forms[target.ActivityID]
					form.text = target.Text
					form.materials.items = productTaskContext(target.Materials)
				}
			}
			p.loaded = false
		})
	}()
}

type productConversationRow struct {
	ConversationID string
	Preview        string
	Timestamp      time.Time
}

type productConversationBrowser struct {
	contact    *contact
	sourceID   string
	reading    bool
	focusBack  bool
	focusID    string
	problem    string
	items      []productConversationRow
	nextCursor *string
	selectedID string
	ownerID    string
	history    historyProjection
	listScroll ui.ScrollState
	transcript ui.ListState
}

func (a *appModel) browseProductConversations(ct *contact, sourceID string) {
	if a.selectedContact() != ct || !a.productProfileReady() || a.dataRecoveryBlocksChanges() {
		return
	}
	browser := &productConversationBrowser{contact: ct, sourceID: sourceID, focusBack: true}
	a.priorChats = browser
	if sourceID == "" {
		a.loadProductConversations(browser, "")
	} else {
		a.loadProductTranscript(browser, sourceID)
	}
}

func (a *appModel) loadProductConversations(browser *productConversationBrowser, cursor string) {
	if browser.reading || !a.productProfileReady() || a.dataRecoveryBlocksChanges() {
		return
	}
	client := a.productHost().product
	ct := browser.contact
	currentID := ct.SessionID
	browser.reading, browser.problem = true, ""
	a.historyAsyncStarted()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		page, err := client.ListConversations(ctx, pi.ProductConversationListParams{
			BotID: ct.ID, ProductPageParams: pi.ProductPageParams{Cursor: cursor, Limit: 25},
		})
		a.updateUI(func() {
			defer a.historyAsyncFinished()
			browser.reading = false
			if a.priorChats != browser || !a.currentActivityClient(ct, client) {
				return
			}
			if err != nil {
				browser.problem = a.connectionSafeMessage(err)
				return
			}
			if cursor == "" {
				browser.items = browser.items[:0]
			}
			for _, item := range page.Items {
				if item.BotID == ct.ID && item.Mode == "readOnly" && item.ConversationID != currentID {
					timestamp, _ := time.Parse(time.RFC3339Nano, item.Timestamp)
					browser.items = append(browser.items, productConversationRow{ConversationID: item.ConversationID, Preview: item.Preview, Timestamp: timestamp})
				}
			}
			browser.nextCursor = page.NextCursor
		})
	}()
}

func (a *appModel) loadProductTranscript(browser *productConversationBrowser, id string) {
	if browser.reading || !a.productProfileReady() || a.dataRecoveryBlocksChanges() {
		return
	}
	client := a.productHost().product
	ct := browser.contact
	browser.selectedID, browser.ownerID, browser.problem = id, "", ""
	browser.reading, browser.focusBack = true, true
	browser.history, browser.transcript = historyProjection{}, ui.ListState{}
	a.historyAsyncStarted()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		conversation, err := client.GetConversation(ctx, id)
		var history historyProjection
		if err == nil && (conversation.ConversationID != id || browser.sourceID == "" && (conversation.BotID != ct.ID || conversation.Mode != "readOnly")) {
			err = errors.New("the requested previous chat is unavailable")
		}
		if err == nil {
			history, err = productHistory(ctx, client, id)
		}
		a.updateUI(func() {
			defer a.historyAsyncFinished()
			browser.reading = false
			if a.priorChats != browser || browser.selectedID != id || !a.currentActivityClient(ct, client) {
				return
			}
			if err != nil {
				browser.problem = a.connectionSafeMessage(err)
				return
			}
			browser.ownerID, browser.history = conversation.BotID, history
		})
	}()
}

func (target productActivityIntent) dispatch(ctx context.Context, client *pi.ProductClient) (bool, error) {
	execution := productActivityExecution(target.ActivityID, target.ExecutionID)
	text := pi.ProductText{Text: target.Text, Materials: target.Materials}
	switch target.Action {
	case "continue":
		receipt, err := client.ContinueActivity(ctx, pi.ProductActivityContinueParams{ProductActivityParams: execution.ProductActivityParams, ProductText: text, OperationID: target.OperationID, ExpectedRevision: target.Revision})
		return err == nil && target.matchesReceipt(receipt) && receipt.State == "accepted", err
	case "answer":
		answer, err := client.AnswerQuestion(ctx, pi.ProductAnswerParams{ProductActivityExecutionParams: execution, ProductText: text, QuestionID: target.QuestionID, QuestionRevision: target.Revision, OperationID: target.OperationID})
		return err == nil && answer.AnswerSaved && answer.ActivityID == target.ActivityID && answer.ExecutionID == target.ExecutionID && answer.QuestionID == target.QuestionID, err
	case "stop":
		stop, err := client.StopActivity(ctx, pi.ProductActivityStopParams{ProductActivityExecutionParams: execution, OperationID: target.OperationID})
		return err == nil && stop.CancelRequested && stop.ExecutionID == target.ExecutionID, err
	default:
		return false, nil
	}
}

func (target productActivityIntent) matchesReceipt(receipt pi.ProductReceipt) bool {
	if receipt.OperationID != target.OperationID || receipt.Target.ActivityID != target.ActivityID || receipt.Target.QuestionID != target.QuestionID {
		return false
	}
	if receipt.State != "accepted" {
		return true
	}
	switch target.Action {
	case "stop":
		var result pi.ProductStopResult
		return json.Unmarshal(receipt.Result, &result) == nil && result.CancelRequested && result.ExecutionID == target.ExecutionID
	case "answer":
		var result pi.ProductAnswerResult
		return json.Unmarshal(receipt.Result, &result) == nil && result.AnswerSaved && result.ActivityID == target.ActivityID && result.ExecutionID == target.ExecutionID && result.QuestionID == target.QuestionID
	case "continue":
		return receipt.Execution != nil && receipt.Execution.ExecutionID != ""
	default:
		return false
	}
}

func (a *appModel) inspectProductActivityFile(p *productActivityPanel, path string) {
	result := p.result
	go func() {
		inspection := inspectIssueResultFile(issueResultFile{Path: path})
		a.updateUI(func() {
			if p.result == result {
				p.file, p.scroll.Y, p.focusBack = &inspection, 0, true
			}
		})
	}()
}

func (a *appModel) openProductActivityFile(p *productActivityPanel, path string, reveal bool) {
	result := p.result
	open := a.openPath
	if open == nil {
		open = openFileWithSystem
	}
	go func() {
		file := issueResultFile{Path: path}
		var outcome issueResultFileAction
		if reveal {
			outcome = requestRevealIssueResultFile(file, requestRevealIssueResultWithSystem)
		} else {
			outcome = requestOpenIssueResultFile(file, open)
		}
		a.updateUI(func() {
			if p.result != result {
				return
			}
			msgs := locales.In(a.uiLocale)
			p.problem = issueResultNoticeText(msgs, outcome.NoticeKind, outcome.NoticeDetail)
			if outcome.ErrKind != "" {
				p.problem = issueResultNoticeText(msgs, outcome.ErrKind, outcome.ErrDetail)
			}
		})
	}()
}
