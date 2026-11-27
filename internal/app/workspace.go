package app

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
	separators := 1
	if a.effectiveRailWidth() > 0 {
		separators++
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

type productActivityForm struct {
	target     productActivityIntent
	text       string
	materials  taskContext
	focus      bool
	caretToEnd bool
}

type productActivityFormKey struct {
	activity, execution, question, action string
}

func activityFormKey(target productActivityIntent) productActivityFormKey {
	return productActivityFormKey{target.ActivityID, target.ExecutionID, target.QuestionID, target.Action}
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
	focusQuestion string
	legacyIssue   bool
	items         []pi.ProductActivitySummary
	nextCursor    *string
	activityID    string
	activity      *pi.ProductActivity
	forms         map[string]*productActivityForm
	drafts        map[productActivityFormKey]*productActivityForm
	traces        map[pi.ProductActivityExecutionParams]*activityTraceState
	result        *pi.ProductActivityResult
	file          *activityResultFileInspection
	problem       string
	actionProblem string
	actionTarget  string
	recovery      []productActivityIntent
	actionBlocker pi.ProductRouteAdmission
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
			if p.activityID != id || revision != a.productActivityRevision() {
				p.loaded = false
				return
			}
			if id != "" {
				if detailErr != nil {
					p.activity = nil
					p.problem = a.connectionSafeMessage(detailErr)
				} else if detail.ActivityID == id {
					p.activity = &detail
					a.applySkillApprovalRequests(detail.ApprovalRequests)
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
	p.actionBlocker = pi.ProductRouteAdmission{}
	p.focusBack = true
	p.focusQuestion, p.legacyIssue = "", false
	a.refreshProductActivities(ct, false)
	if pending := a.activityIntents[activityID]; pending.Action == "steer" && !p.changing && a.productActivityClient() != nil {
		a.runProductActivityAction(ct, a.productActivityClient(), pending, false)
	}
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
	form := activityDraft(p, target)
	form.target.Revision = target.Revision
	p.forms[target.ActivityID] = form
	form.focus = true
}

func activityDraft(p *productActivityPanel, target productActivityIntent) *productActivityForm {
	if p.drafts == nil {
		p.drafts = make(map[productActivityFormKey]*productActivityForm)
	}
	key := activityFormKey(target)
	form := p.drafts[key]
	if form == nil {
		form = &productActivityForm{target: target}
		p.drafts[key] = form
	}
	return form
}

func discardActivityDraft(p *productActivityPanel, target productActivityIntent) {
	key := activityFormKey(target)
	if p.forms[target.ActivityID] == p.drafts[key] {
		delete(p.forms, target.ActivityID)
	}
	delete(p.drafts, key)
}

func (a *appModel) chooseActivityMaterial(ct *contact, form *productActivityForm) {
	p, tc := &ct.activity, &form.materials
	if tc.FileChoosing || a.dataRecoveryBlocksChanges() || a.shutdownStarted {
		return
	}
	tc.FileRequest++
	request, editor, revision := tc.FileRequest, tc.FileForm, tc.FileForm.PathRevision
	viewGeneration := a.folderChoiceGeneration
	tc.FileChoosing = true
	a.contextFilePickers++
	pick := a.chooseContextFile
	if pick == nil {
		pick = mygo.Dialog.Open
	}
	label := locales.In(a.uiLocale).TaskContextChooseFile()
	opts := mygo.OpenDialogOptions{Parent: a.win, Title: label, ButtonLabel: label, DefaultPath: tc.FormPath}
	go func() {
		if !filepath.IsAbs(opts.DefaultPath) {
			opts.DefaultPath, _ = os.UserHomeDir()
		}
		paths, err := pick(opts)
		a.updateUI(func() {
			a.contextFilePickers--
			if tc.FileRequest != request || tc.FileForm != editor {
				return
			}
			tc.FileChoosing = false
			if a.shutdownStarted || a.settingsOpen || a.folderChoiceGeneration != viewGeneration || a.selectedContact() != ct || !p.open || p.activityID != form.target.ActivityID || p.forms[p.activityID] != form || editor.PathRevision != revision {
				return
			}
			tc.FocusFileChooser, tc.FileChoiceFailed = true, err != nil
			if err == nil && len(paths) > 0 {
				tc.FormPath = paths[0]
				editor.PathRevision++
			}
		})
	}()
}

func (a *appModel) submitProductActivity(ct *contact, target productActivityIntent, form *productActivityForm) {
	p, client := &ct.activity, a.productActivityClient()
	if client == nil || a.selectedContact() != ct || target.BotID != ct.ID || p.changing || a.dataRecoveryBlocksChanges() || a.activityIntents[target.ActivityID].OperationID != "" {
		return
	}
	activity := p.activity
	if p.activityID != target.ActivityID || activity == nil {
		if target.Action != "acknowledge" {
			return
		}
		activity = ct.activityNotices.details[target.ActivityID]
	}
	if activity == nil || !a.validActivityControlTarget(ct, activity, target, form) {
		return
	}
	if form != nil {
		target.Text = form.text
		target.SelectedOptionID = form.target.SelectedOptionID
		if target.Action == "steer" {
			target.Text = strings.TrimSpace(target.Text)
		}
		target.Materials = make([]pi.ProductMaterial, len(form.materials.Items))
		for i, item := range form.materials.Items {
			target.Materials[i] = pi.ProductMaterial{ID: item.ID, Name: item.Name, Text: item.Text, Source: item.Source, Revision: item.Revision}
		}
	}
	target.OperationID = newContactID()
	if a.activityIntents == nil {
		a.activityIntents = make(map[string]productActivityIntent)
	}
	a.activityIntents[target.ActivityID] = target
	p.actionTarget = target.ActivityID
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
	p.actionTarget = target.ActivityID
	p.actionBlocker = pi.ProductRouteAdmission{}
	a.submissionAsyncStarted()
	go func() {
		var accepted, rejected bool
		var err error
		var receipt pi.ProductReceipt
		if dispatch {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			accepted, receipt, err = dispatchProductActivityIntent(target, ctx, client)
			cancel()
			var command *pi.CommandError
			rejected = receipt.State == "rejected" || errors.As(err, &command) && (command.Delivery == pi.NotSent || command.Delivery == pi.Rejected)
		}
		if !accepted && !rejected {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var lookupErr error
			if target.Action == "steer" {
				steer, lookup := client.SteerStatus(ctx, pi.ProductSteerStatusParams{OperationID: target.OperationID})
				accepted, rejected = activitySteerOutcome(target, steer)
				lookupErr = lookup
			} else {
				receipt, lookupErr = client.Operation(ctx, target.OperationID)
				if lookupErr == nil && productActivityIntentMatchesReceipt(target, receipt) {
					accepted, rejected = receipt.State == "accepted", receipt.State == "rejected"
				}
			}
			cancel()
			if err == nil && !accepted {
				err = lookupErr
			}
		}
		var remote *pi.RemoteError
		errors.As(err, &remote)
		admission := providerAdmission(receipt, remote)
		a.updateUI(func() {
			defer a.submissionAsyncFinished()
			defer a.resumeControlPlaneReadback(ct)
			p.changing = false
			if !a.currentActivityClient(ct, client) || a.activityIntents[target.ActivityID].OperationID != target.OperationID {
				return
			}
			if target.Action != "steer" || rejected {
				p.actionProblem = a.connectionSafeMessage(err)
			}
			if rejected && providerDeliveryBlocked(admission.Code) {
				p.actionProblem, p.actionBlocker = "", admission
			}
			if accepted || rejected {
				delete(a.activityIntents, target.ActivityID)
				if err := a.persist(); err != nil {
					a.activityIntents[target.ActivityID] = target
					p.actionProblem = a.connectionSafeMessage(err)
				} else if accepted {
					if activityActionHasForm(target.Action) {
						discardActivityDraft(p, target)
					}
					if target.Action == "stop" {
						p.stopping = target.ExecutionID
					}
					p.actionProblem = ""
				} else if activityActionHasForm(target.Action) && p.drafts[activityFormKey(target)] == nil {
					form := activityDraft(p, target)
					form.text = target.Text
					form.materials.Items = productTaskContext(target.Materials)
					if p.forms[target.ActivityID] == nil && p.activityID == target.ActivityID {
						a.editProductActivity(ct, target)
					}
				}
			}
			if rejected && providerDeliveryBlocked(admission.Code) {
				a.refreshProductProviders(a.productHost())
			}
			p.loaded = false
			ct.activityNotices.version++
		})
	}()
}

type productConversationBrowser struct {
	contact    *contact
	sourceID   string
	reading    bool
	focusBack  bool
	problem    string
	selectedID string
	ownerID    string
	history    historyProjection
	transcript ui.ListState
	recall     quoteRecall
}

func (a *appModel) browseProductConversations(ct *contact, sourceID string) {
	if sourceID == "" || a.selectedContact() != ct || !a.productProfileReady() || a.dataRecoveryBlocksChanges() {
		return
	}
	browser := &productConversationBrowser{contact: ct, sourceID: sourceID, focusBack: true}
	a.priorChats = browser
	a.loadProductTranscript(browser, sourceID)
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
	browser.recall = quoteRecall{}
	a.historyAsyncStarted()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		conversation, err := client.GetConversation(ctx, id)
		var history historyProjection
		if err == nil && conversation.ConversationID != id {
			err = errors.New("the activity's source chat is unavailable")
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

func dispatchProductActivityIntent(target productActivityIntent, ctx context.Context, client *pi.ProductClient) (bool, pi.ProductReceipt, error) {
	execution := productActivityExecution(target.ActivityID, target.ExecutionID)
	text := pi.ProductText{Text: target.Text, Materials: target.Materials}
	switch target.Action {
	case "continue":
		receipt, err := client.ContinueActivity(ctx, pi.ProductActivityContinueParams{ProductActivityParams: execution.ProductActivityParams, ProductText: text, OperationID: target.OperationID, ExpectedRevision: target.Revision})
		if err == nil && productActivityIntentMatchesReceipt(target, receipt) {
			return receipt.State == "accepted", receipt, nil
		}
		return false, pi.ProductReceipt{}, err
	case "answer":
		answer, err := client.AnswerQuestion(ctx, pi.ProductAnswerParams{ProductActivityExecutionParams: execution, ProductText: text, QuestionID: target.QuestionID, QuestionRevision: target.Revision, OperationID: target.OperationID, SelectedOptionID: target.SelectedOptionID})
		return err == nil && answer.AnswerSaved && answer.ActivityID == target.ActivityID && answer.ExecutionID == target.ExecutionID && answer.QuestionID == target.QuestionID && answer.SelectedOptionID == target.SelectedOptionID, pi.ProductReceipt{}, err
	case "stop":
		stop, err := client.StopActivity(ctx, pi.ProductActivityStopParams{ProductActivityExecutionParams: execution, OperationID: target.OperationID})
		return err == nil && stop.CancelRequested && stop.ExecutionID == target.ExecutionID, pi.ProductReceipt{}, err
	case "steer":
		steer, err := client.SteerActivity(ctx, pi.ProductSteerActivityParams{ProductActivityExecutionParams: execution, ProductText: text, OperationID: target.OperationID, ExpectedRevision: &target.Revision})
		accepted, rejected := activitySteerOutcome(target, steer)
		if rejected {
			return false, pi.ProductReceipt{State: "rejected"}, err
		}
		return accepted, pi.ProductReceipt{}, err
	case "acknowledge":
		item, err := client.AcknowledgeActivity(ctx, pi.ProductActivityAcknowledgeParams{ProductActivityExecutionParams: execution, ExpectedRevision: target.Revision, OperationID: target.OperationID})
		return err == nil && item.ActivityID == target.ActivityID && item.AcknowledgedExecutionID == target.ExecutionID && item.AcknowledgedAt != "", pi.ProductReceipt{}, err
	case "close":
		item, err := client.CloseActivity(ctx, pi.ProductActivityCloseParams{ProductActivityParams: execution.ProductActivityParams, ExpectedRevision: target.Revision, OperationID: target.OperationID})
		return err == nil && item.ActivityID == target.ActivityID && item.ClosedAt != "", pi.ProductReceipt{}, err
	default:
		return false, pi.ProductReceipt{}, nil
	}
}

func productActivityIntentMatchesReceipt(target productActivityIntent, receipt pi.ProductReceipt) bool {
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
		return json.Unmarshal(receipt.Result, &result) == nil && result.AnswerSaved && result.ActivityID == target.ActivityID && result.ExecutionID == target.ExecutionID && result.QuestionID == target.QuestionID && result.SelectedOptionID == target.SelectedOptionID
	case "continue":
		return receipt.Execution != nil && receipt.Execution.ExecutionID != ""
	case "acknowledge", "close":
		var result pi.ProductActivitySummary
		if json.Unmarshal(receipt.Result, &result) != nil || result.ActivityID != target.ActivityID {
			return false
		}
		if target.Action == "acknowledge" {
			return result.AcknowledgedAt != "" && result.AcknowledgedExecutionID == target.ExecutionID
		}
		return result.ClosedAt != ""
	default:
		return false
	}
}

func (a *appModel) inspectProductActivityFile(p *productActivityPanel, path string) {
	result := p.result
	go func() {
		inspection := inspectActivityResultFile(activityResultFile{Path: path})
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
		file := activityResultFile{Path: path}
		var outcome activityResultFileAction
		if reveal {
			outcome = requestRevealActivityResultFile(file, requestRevealActivityResultWithSystem)
		} else {
			outcome = requestOpenActivityResultFile(file, open)
		}
		a.updateUI(func() {
			if p.result != result {
				return
			}
			msgs := locales.In(a.uiLocale)
			p.problem = activityResultNoticeText(msgs, outcome.NoticeKind, outcome.NoticeDetail)
			if outcome.ErrKind != "" {
				p.problem = activityResultNoticeText(msgs, outcome.ErrKind, outcome.ErrDetail)
			}
		})
	}()
}
