package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

type scheduledWorkDeps struct {
	client             *pi.ProductClient
	botID              string
	wb                 theme.WorkbenchTheme
	blocked            bool
	locale             string
	updateUI           func(func())
	persistOperations  func() error
	onBack             func()
	onOpenConversation func(conversationID string)
}

type scheduledWorkCopy struct {
	locale string
}

func (m scheduledWorkCopy) text(en, zh string) string {
	if strings.HasPrefix(m.locale, "zh") {
		return zh
	}
	return en
}

func (m scheduledWorkCopy) instant(value, zone string) string {
	stamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	if zone != "" {
		if location, err := time.LoadLocation(zone); err == nil {
			stamp = stamp.In(location)
		}
	}
	return stamp.Format(m.text("Jan 2, 2006 15:04 MST (-07:00)", "2006年1月2日 15:04 MST (-07:00)"))
}

type scheduledWorkState struct {
	bots map[string]*botScheduleState
}

type botScheduleState struct {
	client          *pi.ProductClient
	kind            pi.ProductWatchKind
	showGrants      bool
	watchID         string
	grantID         string
	returnToGrants  bool
	watches         scheduledRead[pi.ProductPage[pi.ProductWatchSummary]]
	grants          scheduledRead[pi.ProductPage[pi.ProductWatchGrant]]
	detail          scheduledRead[pi.ProductWatchDetail]
	editor          *scheduledWatchEditor
	review          *scheduledWatchReview
	confirm         string
	confirmRevision uint64
	operation       *scheduledWatchOperation
	working         bool
	problem         error
	problemDetails  bool
	result          string
	invalidated     []string
	focusWatchID    string
	focusGrantID    string
	focusNew        bool
	focusHeading    bool
	scroll          ui.ScrollState
	listScroll      float32
	editorScroll    float32
	grantScroll     float32
}

type scheduledRead[T any] struct {
	value   T
	ticket  uint64
	ready   bool
	loading bool
	problem error
}

func (r *scheduledRead[T]) invalidate() {
	*r = scheduledRead[T]{ticket: r.ticket + 1}
}

func (s *scheduledWorkState) bot(botID string) *botScheduleState {
	if s.bots == nil {
		s.bots = make(map[string]*botScheduleState)
	}
	if s.bots[botID] == nil {
		s.bots[botID] = &botScheduleState{}
	}
	return s.bots[botID]
}

func (s *scheduledWorkState) applyWatchChanged(change pi.ProductWatchChange) {
	if bot := s.bots[change.BotID]; bot != nil {
		bot.watches.invalidate()
		if change.GrantID != "" || change.Change == "grant_saved" || change.Change == "grant_revoked" {
			bot.grants.invalidate()
		}
		if bot.watchID != "" && (change.WatchID == bot.watchID || change.GrantID != "") {
			bot.detail.invalidate()
		}
	}
}

func (b *botScheduleState) refresh() {
	b.watches.invalidate()
	b.grants.invalidate()
	b.detail.invalidate()
}

func (s *scheduledWorkState) pendingOperations() []store.PendingWatchOperation {
	var pending []store.PendingWatchOperation
	for _, b := range s.bots {
		if b.operation != nil {
			pending = append(pending, store.PendingWatchOperation{
				ID:       b.operation.id,
				BotID:    b.operation.botID,
				WatchID:  b.operation.watchID,
				GrantID:  b.operation.grantID,
				Revision: b.operation.revision,
				Action:   b.operation.action,
			})
		}
	}
	return pending
}

func (s *scheduledWorkState) restoreOperations(pending []store.PendingWatchOperation) {
	for _, op := range pending {
		if op.ID == "" || op.BotID == "" {
			continue
		}
		b := s.bot(op.BotID)
		if b.operation == nil {
			b.operation = &scheduledWatchOperation{
				id:       op.ID,
				botID:    op.BotID,
				watchID:  op.WatchID,
				grantID:  op.GrantID,
				revision: op.Revision,
				action:   op.Action,
			}
		}
	}
}

func loadScheduled[T any](deps scheduledWorkDeps, read *scheduledRead[T], query func(context.Context) (T, error), apply func(T)) {
	if deps.client == nil || deps.updateUI == nil || read.loading {
		return
	}
	read.ticket++
	ticket := read.ticket
	read.loading, read.problem = true, nil
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		value, err := query(ctx)
		deps.updateUI(func() {
			if read.ticket != ticket {
				return
			}
			read.loading, read.problem = false, err
			if err == nil {
				apply(value)
				read.ready = true
			}
		})
	}()
}

func (b *botScheduleState) readWatches(deps scheduledWorkDeps, cursor string) {
	params := pi.ProductWatchListParams{BotID: deps.botID, Kind: b.kind, Cursor: cursor}
	loadScheduled(deps, &b.watches, func(ctx context.Context) (pi.ProductPage[pi.ProductWatchSummary], error) {
		return deps.client.ListWatches(ctx, params)
	}, func(page pi.ProductPage[pi.ProductWatchSummary]) {
		if cursor != "" {
			page.Items = append(b.watches.value.Items, page.Items...)
		}
		b.watches.value = page
	})
}

func (b *botScheduleState) readGrants(deps scheduledWorkDeps, cursor string) {
	params := pi.ProductWatchGrantListParams{BotID: deps.botID, IncludeRevoked: true, Cursor: cursor}
	loadScheduled(deps, &b.grants, func(ctx context.Context) (pi.ProductPage[pi.ProductWatchGrant], error) {
		return deps.client.ListWatchGrants(ctx, params)
	}, func(page pi.ProductPage[pi.ProductWatchGrant]) {
		if cursor != "" {
			page.Items = append(b.grants.value.Items, page.Items...)
		}
		b.grants.value = page
	})
}

func (b *botScheduleState) readDetail(deps scheduledWorkDeps) {
	watchID := b.watchID
	loadScheduled(deps, &b.detail, func(ctx context.Context) (pi.ProductWatchDetail, error) {
		value, err := deps.client.GetWatch(ctx, deps.botID, watchID)
		if err == nil && (value.WatchID != watchID || value.OwnerBotID != deps.botID) {
			err = errors.New("watch detail did not match the selected watch")
		}
		return value, err
	}, func(value pi.ProductWatchDetail) {
		b.detail.value = value
	})
}

type scheduledWatchOperation struct {
	id, botID, watchID, grantID string
	revision                    uint64
	action                      string
}

type scheduledWatchReview struct {
	watch  pi.ProductWatchSummary
	grant  *pi.ProductWatchGrant
	save   *pi.ProductWatchSaveParams
	needed bool
}

func (b *botScheduleState) mutationBlocked(deps scheduledWorkDeps) bool {
	return deps.blocked || deps.client == nil || deps.updateUI == nil || b.operation != nil
}

func (b *botScheduleState) startOperation(deps scheduledWorkDeps, op scheduledWatchOperation, call func(context.Context, string) (pi.ProductReceipt, error)) {
	if b.mutationBlocked(deps) {
		return
	}
	op.id, op.botID = "watch-"+rand.Text(), deps.botID
	b.operation = &op
	if deps.persistOperations != nil && deps.persistOperations() != nil {
		b.operation = nil
		b.problem = errors.New("could not save watch operation")
		return
	}
	b.runOperation(deps, func(ctx context.Context) (pi.ProductReceipt, error) { return call(ctx, op.id) }, false)
}

func (b *botScheduleState) ReconcileWatchOperation(deps scheduledWorkDeps) {
	if b.operation == nil || b.operation.botID != deps.botID || b.working || deps.client == nil || deps.updateUI == nil {
		return
	}
	b.client = deps.client
	id := b.operation.id
	b.runOperation(deps, func(ctx context.Context) (pi.ProductReceipt, error) { return deps.client.Operation(ctx, id) }, true)
}

func (b *botScheduleState) runOperation(deps scheduledWorkDeps, call func(context.Context) (pi.ProductReceipt, error), lookup bool) {
	op := b.operation
	b.working, b.problem, b.result = true, nil, ""
	b.invalidated = nil
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		receipt, err := call(ctx)
		deps.updateUI(func() {
			if b.operation != op {
				return
			}
			b.working = false
			if b.client != deps.client {
				return
			}
			b.refresh()
			b.finishOperation(op, receipt, err, lookup)
			if b.operation == nil && deps.persistOperations != nil {
				_ = deps.persistOperations()
			}
			if lookup {
				b.readWatches(deps, "")
				b.readGrants(deps, "")
				if b.watchID != "" {
					b.readDetail(deps)
				}
			}
		})
	}()
}

func (b *botScheduleState) finishOperation(op *scheduledWatchOperation, receipt pi.ProductReceipt, err error, lookup bool) {
	target := receipt.Target
	correlated := receipt.OperationID == op.id && target.BotID == op.botID &&
		(op.watchID == "" || op.watchID == target.WatchID) && (op.grantID == "" || op.grantID == target.GrantID)
	if err == nil && correlated && receipt.State == "accepted" {
		if op.grantID != "" {
			result, decodeErr := pi.DecodeWatchRevokeGrantResult(receipt)
			if decodeErr != nil || result.Grant.GrantID != op.grantID || result.Grant.OwnerBotID != op.botID || result.Grant.Revision < op.revision {
				return
			}
			b.invalidated = result.InvalidatedWatchIDs
		} else {
			result, decodeErr := pi.DecodeWatchMutationResult(receipt)
			watch := result.Watch
			if decodeErr != nil || target.WatchID == "" || watch.WatchID != target.WatchID || watch.OwnerBotID != op.botID || watch.Revision < op.revision {
				return
			}
			if op.action == "save" && (!lookup || b.editor != nil) {
				b.editor = nil
				b.openWatch(watch.WatchID)
			}
		}
		b.operation, b.review, b.confirm = nil, nil, ""
		b.result = op.action
		return
	}
	var command *pi.CommandError
	rejected := correlated && receipt.State == "rejected" || !lookup && errors.As(err, &command) && (command.Delivery == pi.NotSent || command.Delivery == pi.Rejected)
	if !rejected {
		return
	}
	b.operation = nil
	b.confirm = ""
	if capabilityErrorCode(err) == "grant_required" && b.review != nil {
		b.review.needed, b.review.grant = true, nil
		return
	}
	b.review = nil
	b.problem = err
	if b.problem == nil {
		b.problem = errors.New("scheduled work operation was rejected")
	}
}

func (b *botScheduleState) openWatch(id string) {
	b.listScroll = b.scroll.Y
	b.watchID, b.grantID = id, ""
	b.editor, b.review, b.confirm = nil, nil, ""
	b.detail.invalidate()
	b.scroll.Y = 0
	b.focusHeading = true
}

func (b *botScheduleState) changeWatch(deps scheduledWorkDeps, watch pi.ProductWatchSummary, action string, approve bool) {
	op := scheduledWatchOperation{watchID: watch.WatchID, revision: watch.Revision, action: action}
	b.startOperation(deps, op, func(ctx context.Context, id string) (pi.ProductReceipt, error) {
		params := pi.ProductWatchStateMutationParams{BotID: deps.botID, WatchID: watch.WatchID, ExpectedRevision: watch.Revision, OperationID: id}
		switch action {
		case "activate":
			return deps.client.ActivateWatch(ctx, pi.ProductWatchActivateParams{BotID: params.BotID, WatchID: params.WatchID, ExpectedRevision: params.ExpectedRevision, ApproveGrant: approve, OperationID: id})
		case "pause":
			return deps.client.PauseWatch(ctx, params)
		default:
			return deps.client.CancelWatch(ctx, params)
		}
	})
}

func (b *botScheduleState) saveWatch(deps scheduledWorkDeps, params pi.ProductWatchSaveParams) {
	op := scheduledWatchOperation{watchID: params.WatchID, action: "save"}
	if params.ExpectedRevision != nil {
		op.revision = *params.ExpectedRevision
	}
	b.startOperation(deps, op, func(ctx context.Context, id string) (pi.ProductReceipt, error) {
		params.OperationID = id
		return deps.client.SaveWatch(ctx, params)
	})
}

func (b *botScheduleState) revokeGrant(deps scheduledWorkDeps, grant pi.ProductWatchGrant) {
	op := scheduledWatchOperation{grantID: grant.GrantID, revision: grant.Revision, action: "revoke"}
	b.startOperation(deps, op, func(ctx context.Context, id string) (pi.ProductReceipt, error) {
		return deps.client.RevokeWatchGrant(ctx, pi.ProductWatchRevokeGrantParams{
			BotID: deps.botID, GrantID: grant.GrantID, ExpectedRevision: grant.Revision, OperationID: id,
		})
	})
}

type scheduledWatchEditor struct {
	base                    pi.ProductWatchSummary
	kind, label, purpose    string
	prompt, fireAt, missed  string
	cadence, interval, zone string
	frequency, minute, hour string
	days                    [7]bool
	precheck, executable    string
	args, timeout           string
	workspaceRead, emit     bool
	skill, source, script   string
	input                   string
	focusField              string
	problem                 string
}

func editScheduledWatch(watch pi.ProductWatchSummary) *scheduledWatchEditor {
	e := &scheduledWatchEditor{
		base: watch, kind: string(watch.Kind), label: watch.Label, purpose: watch.Purpose, prompt: watch.Prompt,
		missed: string(pi.ProductMissedSkip), cadence: string(pi.ProductScheduleInterval), interval: "300", zone: "UTC",
		frequency: "daily", minute: "0", hour: "9", timeout: "5000",
	}
	if timer := watch.Timer; timer != nil {
		e.fireAt, e.missed = timer.FireAt, string(timer.MissedPolicy)
	}
	if schedule := watch.Schedule; schedule != nil {
		e.cadence, e.zone = string(schedule.CadenceKind), schedule.TimeZone
		if schedule.IntervalSeconds != nil {
			e.interval = strconv.Itoa(*schedule.IntervalSeconds)
		}
		if schedule.Frequency != "" {
			e.frequency = schedule.Frequency
		}
		if schedule.Minute != nil {
			e.minute = strconv.Itoa(*schedule.Minute)
		}
		if schedule.Hour != nil {
			e.hour = strconv.Itoa(*schedule.Hour)
		}
		for _, day := range schedule.DaysOfWeek {
			if day >= 0 && day < len(e.days) {
				e.days[day] = true
			}
		}
	}
	if pre := watch.Precheck; pre != nil {
		e.precheck, e.executable = pre.Kind, pre.Executable
		args, _ := json.Marshal(pre.AllowedArgs)
		if len(pre.AllowedArgs) > 0 {
			e.args = string(args)
		}
		if pre.TimeoutMS != nil {
			e.timeout = strconv.Itoa(*pre.TimeoutMS)
		}
		e.workspaceRead, e.emit = pre.WorkspaceRead, pre.EmitInitial
		e.skill, e.source, e.script = pre.SkillID, pre.SkillSource, pre.ScriptName
		if pre.Input != nil {
			input, _ := json.Marshal(pre.Input)
			e.input = string(input)
		}
	}
	return e
}

func (e *scheduledWatchEditor) parameters(botID string, m scheduledWorkCopy) (pi.ProductWatchSaveParams, string) {
	activate, approve := true, true
	params := pi.ProductWatchSaveParams{
		BotID: botID, WatchID: e.base.WatchID, Kind: pi.ProductWatchKind(e.kind), Label: strings.TrimSpace(e.label),
		Purpose: strings.TrimSpace(e.purpose), Prompt: e.prompt, TargetConversationID: e.base.TargetConversationID,
		Activate: &activate, ApproveGrant: &approve,
	}
	if params.WatchID != "" {
		revision := e.base.Revision
		params.ExpectedRevision = &revision
	}
	if params.Purpose == "" {
		return params, m.text("Enter a purpose for this work.", "请填写这项工作的用途。")
	}
	if params.Kind == pi.ProductWatchTimer {
		stamp, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(e.fireAt))
		if err != nil {
			return params, m.text("Enter a timer date and time with an offset, for example 2026-12-01T09:00:00+08:00.", "请填写带时区偏移的计时时间，例如 2026-12-01T09:00:00+08:00。")
		}
		params.Timer = &pi.ProductTimerSpec{FireAt: stamp.UTC().Format(time.RFC3339Nano), MissedPolicy: pi.ProductTimerMissedPolicy(e.missed)}
		return params, ""
	}
	zone := strings.TrimSpace(e.zone)
	if _, err := time.LoadLocation(zone); err != nil || zone == "" || zone == "Local" {
		return params, m.text("Enter an IANA time zone, for example Asia/Shanghai or UTC.", "请输入 IANA 时区，例如 Asia/Shanghai 或 UTC。")
	}
	schedule := &pi.ProductScheduleSpec{CadenceKind: pi.ProductScheduleCadenceKind(e.cadence), TimeZone: zone}
	if e.base.Schedule != nil {
		schedule.JitterSeconds = e.base.Schedule.JitterSeconds
	}
	if schedule.CadenceKind == pi.ProductScheduleInterval {
		seconds, err := strconv.Atoi(strings.TrimSpace(e.interval))
		if err != nil || seconds < 10 {
			return params, m.text("Enter an interval of at least 10 seconds.", "请输入至少 10 秒的间隔。")
		}
		schedule.IntervalSeconds = &seconds
	} else {
		schedule.Frequency = e.frequency
		minute, err := strconv.Atoi(strings.TrimSpace(e.minute))
		if err != nil || minute < 0 || minute > 59 {
			return params, m.text("Enter a minute from 0 to 59.", "请输入 0 至 59 的分钟数。")
		}
		schedule.Minute = &minute
		if e.frequency != "hourly" {
			hour, err := strconv.Atoi(strings.TrimSpace(e.hour))
			if err != nil || hour < 0 || hour > 23 {
				return params, m.text("Enter an hour from 0 to 23.", "请输入 0 至 23 的小时数。")
			}
			schedule.Hour = &hour
		}
		if e.frequency == "weekly" {
			for day, selected := range e.days {
				if selected {
					schedule.DaysOfWeek = append(schedule.DaysOfWeek, day)
				}
			}
			if len(schedule.DaysOfWeek) == 0 {
				return params, m.text("Choose at least one day of the week.", "请至少选择一个星期几。")
			}
		}
	}
	params.Schedule = schedule
	if e.precheck == "" {
		return params, ""
	}
	precheck := &pi.ProductWatchPrecheck{Kind: e.precheck}
	if e.precheck == "executable" {
		precheck.Executable = strings.TrimSpace(e.executable)
		if !filepath.IsAbs(precheck.Executable) {
			return params, m.text("Enter an absolute executable path.", "请输入可执行文件的绝对路径。")
		}
		if args := strings.TrimSpace(e.args); args != "" {
			var argv []*string
			problem := m.text("Enter arguments as a JSON array of strings, for example [\"-h\", \".\"].", "请将参数写成 JSON 字符串数组，例如 [\"-h\", \".\"]。")
			if err := json.Unmarshal([]byte(args), &argv); err != nil || argv == nil {
				return params, problem
			}
			for _, arg := range argv {
				if arg == nil {
					return params, problem
				}
				precheck.AllowedArgs = append(precheck.AllowedArgs, *arg)
			}
		}
		timeout, err := strconv.Atoi(strings.TrimSpace(e.timeout))
		if err != nil || timeout < 1000 || timeout > 30000 {
			return params, m.text("Enter a timeout from 1,000 to 30,000 milliseconds.", "请输入 1,000 至 30,000 毫秒的超时时间。")
		}
		precheck.TimeoutMS, precheck.WorkspaceRead, precheck.EmitInitial = &timeout, e.workspaceRead, e.emit
	} else {
		precheck.SkillID, precheck.SkillSource, precheck.ScriptName = strings.TrimSpace(e.skill), e.source, strings.TrimSpace(e.script)
		if precheck.SkillID == "" || precheck.SkillSource == "" || precheck.ScriptName == "" {
			return params, m.text("Choose a skill source and enter the skill ID and script name.", "请选择技能来源，并填写技能 ID 和脚本名称。")
		}
		if input := strings.TrimSpace(e.input); input != "" {
			if !json.Valid([]byte(input)) {
				return params, m.text("Enter skill input as valid JSON.", "请输入有效的 JSON 技能输入。")
			}
			precheck.Input = json.RawMessage(input)
		}
	}
	params.Precheck = precheck
	return params, ""
}

func (m scheduledWorkCopy) recoveryScope() string {
	return m.text("Restored timers and executable schedules stay paused and unarmed. Standing grants are installation-local. The copy imports zero grants.", "恢复的计时器和可执行计划会保持暂停，不会启用。常驻授权仅在本机安装中有效，恢复副本不会导入授权。")
}

func (s *scheduledWorkState) render(c *ui.Context, deps scheduledWorkDeps) {
	if deps.botID == "" {
		return
	}
	if deps.locale == "" {
		deps.locale = c.Locale()
	}
	b := s.bot(deps.botID)
	if b.client != deps.client {
		b.client = deps.client
		b.refresh()
	}
	m := scheduledWorkCopy{locale: deps.locale}
	t := deps.wb.Theme
	c.Key("scheduled-work-" + deps.botID)
	ui.Column(c).FillWidth().Grow(1).MinWidth(0).MinHeight(0).Clip().Radius(theme.CardRadius).Background(t.Background).Children(func() {
		ui.Row(c).FillWidth().MaxWidth(720).AlignSelf(ui.Center).Shrink(0).Wrap().Gap(8).Padding(8, 16).AlignItems(ui.Center).Children(func() {
			msgs := locales.In(deps.locale)
			back := secondaryButton(c, msgs.BackAction()).Label(msgs.BackLabel()).Disabled(!b.hasBack() && deps.onBack == nil).OnClick(func() { b.back(deps) })
			if b.focusHeading {
				back.Focus()
				b.focusHeading = false
			}
			title := m.text("Scheduled work", "计划工作")
			switch {
			case b.review != nil:
				title = m.text("Review activation", "审核启用")
			case b.editor != nil && b.editor.base.WatchID == "":
				title = m.text("New scheduled work", "新建计划工作")
			case b.editor != nil:
				title = m.text("Edit scheduled work", "编辑计划工作")
			case b.showGrants:
				title = m.text("Standing grants", "常驻授权")
			}
			pageHeading(c, title).MinWidth(0)
		})
		ui.Scroll(c).FillWidth().Grow(1).MinWidth(0).MinHeight(0).TrackScroll(&b.scroll).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(720).AlignSelf(ui.Center).MinWidth(0).Gap(16).Padding(8, 16, 32).Children(func() {
				b.renderOperation(c, deps, m)
				switch {
				case b.review != nil:
					b.renderReview(c, deps, m)
				case b.editor != nil:
					b.renderEditor(c, deps, m)
				case b.watchID != "":
					b.renderWatch(c, deps, m)
				case b.showGrants:
					b.renderGrants(c, deps, m)
				default:
					b.renderWatches(c, deps, m)
				}
			})
		})
	})
}

func (b *botScheduleState) hasBack() bool {
	return b.review != nil || b.editor != nil || b.watchID != "" || b.showGrants
}

func (b *botScheduleState) back(deps scheduledWorkDeps) {
	b.confirm = ""
	if b.operation != nil && (b.review != nil || b.editor != nil) {
		if deps.onBack != nil {
			deps.onBack()
		}
		return
	}
	switch {
	case b.review != nil:
		b.review = nil
		if b.editor != nil {
			b.scroll.Y = b.editorScroll
		}
		b.focusHeading = true
	case b.editor != nil:
		b.editor = nil
		if b.watchID == "" {
			b.focusNew = true
			b.scroll.Y = b.listScroll
		} else {
			b.focusHeading = true
		}
	case b.watchID != "":
		b.focusWatchID, b.watchID = b.watchID, ""
		b.detail.invalidate()
		if b.returnToGrants {
			b.showGrants, b.returnToGrants = true, false
			b.scroll.Y, b.focusHeading = b.grantScroll, true
		} else {
			b.scroll.Y = b.listScroll
		}
	case b.showGrants && b.grantID != "":
		b.focusGrantID, b.grantID = b.grantID, ""
		b.scroll.Y = b.grantScroll
	case b.showGrants:
		b.showGrants = false
		b.scroll.Y, b.focusHeading = b.listScroll, true
	case deps.onBack != nil:
		deps.onBack()
	}
}

func (b *botScheduleState) renderOperation(c *ui.Context, deps scheduledWorkDeps, m scheduledWorkCopy) {
	t := deps.wb.Theme
	switch {
	case b.working:
		capabilityText(c, t, m.text("Waiting for confirmation…", "正在等待确认…"))
	case b.operation != nil:
		scheduledWorkCard(c, t, func() {
			capabilityText(c, t, m.text("The result is unconfirmed. Check the saved receipt before making another change.", "结果尚未确认。再次更改前，请检查已保存的回执。")).TextColor(t.Warning)
			scheduledButton(c, m.text("Check saved result", "检查已保存的结果"), func() { b.ReconcileWatchOperation(deps) }).Disabled(deps.client == nil || deps.updateUI == nil)
		})
	case b.problem != nil:
		message := m.text("The change was not applied. Review the current item and try again.", "更改未应用。请检查当前项目后重试。")
		if capabilityErrorCode(b.problem) == "stale_revision" {
			message = m.text("This item changed. Return to its details and reopen the action.", "此项目已更改。请返回详情并重新打开操作。")
		}
		recoveryNoticeView(c, t, message, safeDiagnostic(b.problem.Error()), b.problemDetails, func() { b.problemDetails = !b.problemDetails }, nil, "")
	case b.result == "save":
		capabilityText(c, t, m.text("Scheduled work saved and activated.", "计划工作已保存并启用。")).TextColor(t.Success)
	case b.result == "activate":
		capabilityText(c, t, m.text("Scheduled work activated.", "计划工作已启用。")).TextColor(t.Success)
	case b.result == "pause":
		capabilityText(c, t, m.text("Scheduled work paused.", "计划工作已暂停。")).TextColor(t.Text)
	case b.result == "cancel":
		capabilityText(c, t, m.text("Scheduled work cancelled.", "计划工作已取消。")).TextColor(t.Text)
	case b.result == "revoke":
		capabilityText(c, t, fmt.Sprintf(m.text("Grant revoked. Dependent watches invalidated: %d.", "授权已撤销，失效的关联监测项目：%d。"), len(b.invalidated))).TextColor(t.Text)
	case deps.client == nil || deps.updateUI == nil:
		capabilityText(c, t, m.text("Scheduled work is unavailable while the worker is disconnected.", "工作进程断开时，计划工作不可用。"))
	case deps.blocked:
		capabilityText(c, t, m.text("Scheduled work is read-only right now.", "计划工作目前为只读。"))
	}
}

func scheduledWorkCard(c *ui.Context, t *ui.Theme, body func()) {
	ui.Column(c).FillWidth().MinWidth(0).Padding(12).Gap(8).Radius(theme.CardRadius).Border(1, t.Border).Background(t.Surface).Children(body)
}

func scheduledHeading(c *ui.Context, t *ui.Theme, title string) {
	ui.Text(c, title).MinWidth(0).FontSize(16).FixedLineHeight(24).FontWeight(600).TextColor(t.Text)
}

func scheduledButton(c *ui.Context, label string, action func()) ui.Element {
	return secondaryButton(c, label).AlignSelf(ui.Start).MaxWidthPercent(100).OnClick(action)
}

func scheduledValue(c *ui.Context, t *ui.Theme, label, value string) {
	if value == "" {
		return
	}
	ui.Column(c).MinWidth(0).FillWidth().Gap(2).Children(func() {
		capabilityText(c, t, label)
		value := safeDiagnostic(value)
		text := ui.Text(c, value).MinWidth(0).FillWidth().MaxLines(3).Ellipsis("…").FontSize(typeAction).FixedLineHeight(lineAction).TextColor(t.Text)
		appTooltip(c, text, value)
	})
}

func scheduledReadStatus(c *ui.Context, t *ui.Theme, m scheduledWorkCopy, loading, ready bool, err error, retry func()) {
	if loading {
		label := m.text("Loading…", "正在加载…")
		if ready {
			label = m.text("Loading more…", "正在加载更多…")
		}
		capabilityText(c, t, label)
	}
	if err != nil {
		capabilityText(c, t, m.text("Could not load this view. Try refreshing.", "无法加载此视图。请尝试刷新。")).TextColor(t.Warning)
		scheduledButton(c, m.text("Refresh", "刷新"), retry)
	}
}

func (m scheduledWorkCopy) watchState(state pi.ProductWatchState) string {
	switch state {
	case pi.ProductWatchActive:
		return m.text("Active", "进行中")
	case pi.ProductWatchProposed:
		return m.text("Proposed", "待审核")
	case pi.ProductWatchPaused:
		return m.text("Paused", "已暂停")
	case pi.ProductWatchRestored:
		return m.text("Restored, paused and unarmed", "已恢复，暂停且未启用")
	case pi.ProductWatchInvalidated:
		return m.text("Invalidated", "已失效")
	case pi.ProductWatchSettled:
		return m.text("Settled", "已结束")
	case pi.ProductWatchCancelled:
		return m.text("Cancelled", "已取消")
	default:
		return m.text("Unknown state", "状态未知")
	}
}

func (m scheduledWorkCopy) watchKind(kind pi.ProductWatchKind) string {
	if kind == pi.ProductWatchTimer {
		return m.text("Timer", "计时器")
	}
	return m.text("Recurring schedule", "重复计划")
}

func scheduledWatchZone(w pi.ProductWatchSummary) string {
	if w.Schedule != nil {
		return w.Schedule.TimeZone
	}
	return ""
}

func (b *botScheduleState) renderWatches(c *ui.Context, deps scheduledWorkDeps, m scheduledWorkCopy) {
	t := deps.wb.Theme
	r := &b.watches
	if !r.ready && r.problem == nil {
		b.readWatches(deps, "")
	}
	ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
		add := primaryButton(c, m.text("New timer or schedule", "新建计时器或计划")).Disabled(b.mutationBlocked(deps)).OnClick(func() {
			kind := b.kind
			if kind == "" {
				kind = pi.ProductWatchTimer
			}
			b.listScroll = b.scroll.Y
			b.editor = editScheduledWatch(pi.ProductWatchSummary{Kind: kind})
			b.result, b.problem = "", nil
			b.scroll.Y, b.focusHeading = 0, true
		})
		if b.focusNew && !b.mutationBlocked(deps) {
			add.Focus().ScrollIntoView()
			b.focusNew = false
		}
		scheduledButton(c, m.text("Standing grants", "常驻授权"), func() {
			b.listScroll = b.scroll.Y
			b.showGrants, b.grantID = true, ""
			b.scroll.Y, b.focusHeading = 0, true
		})
	})
	ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(4).Children(func() {
		for _, option := range []struct {
			kind  pi.ProductWatchKind
			label string
		}{
			{"", m.text("All work", "全部")},
			{pi.ProductWatchTimer, m.text("Timers", "计时器")},
			{pi.ProductWatchSchedule, m.text("Schedules", "计划")},
		} {
			c.Key(string(option.kind) + "-filter")
			button := secondaryButton(c, option.label).OnClick(func() {
				if b.kind != option.kind {
					b.kind = option.kind
					r.invalidate()
				}
			})
			theme.SelectionButtonState(c, button, b.kind == option.kind)
		}
	})
	scheduledReadStatus(c, t, m, r.loading, r.ready, r.problem, func() { b.readWatches(deps, "") })
	if r.ready && len(r.value.Items) == 0 {
		capabilityEmpty(c, t, m.text("No scheduled work in this view.", "此视图暂无计划工作。"))
	}
	for _, group := range []struct {
		label  string
		states []pi.ProductWatchState
	}{
		{m.text("Current work", "当前工作"), []pi.ProductWatchState{pi.ProductWatchActive, pi.ProductWatchPaused}},
		{m.text("Proposals", "待审核提案"), []pi.ProductWatchState{pi.ProductWatchProposed}},
		{m.text("Dormant imports", "已导入的休眠项目"), []pi.ProductWatchState{pi.ProductWatchRestored}},
		{m.text("Invalidated work", "失效工作"), []pi.ProductWatchState{pi.ProductWatchInvalidated}},
		{m.text("History", "历史"), []pi.ProductWatchState{pi.ProductWatchSettled, pi.ProductWatchCancelled}},
	} {
		shown := false
		for _, watch := range r.value.Items {
			if watch.OwnerBotID != deps.botID || !slices.Contains(group.states, watch.State) {
				continue
			}
			if !shown {
				scheduledHeading(c, t, group.label)
				shown = true
			}
			b.renderWatchRow(c, deps, m, watch)
		}
	}
	if next := r.value.NextCursor; next != nil {
		scheduledButton(c, m.text("Load more", "加载更多"), func() { b.readWatches(deps, *next) }).Disabled(r.loading || deps.client == nil)
	}
}

func (b *botScheduleState) renderWatchRow(c *ui.Context, deps scheduledWorkDeps, m scheduledWorkCopy, watch pi.ProductWatchSummary) {
	t := deps.wb.Theme
	ui.Column(c).Key(watch.WatchID).FillWidth().MinWidth(0).Gap(2).Children(func() {
		label := watch.Label
		if label == "" {
			label = watch.Purpose
		}
		button := scheduledButton(c, label, func() {
			b.openWatch(watch.WatchID)
			b.result, b.problem = "", nil
		})
		appTooltip(c, button, label)
		if b.focusWatchID == watch.WatchID {
			button.Focus().ScrollIntoView()
			b.focusWatchID = ""
		}
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(8).Children(func() {
			capabilityText(c, t, m.watchKind(watch.Kind))
			capabilityText(c, t, m.watchState(watch.State))
			if watch.NextDueAt != "" {
				capabilityText(c, t, m.instant(watch.NextDueAt, scheduledWatchZone(watch)))
			}
		})
		if watch.Purpose != label {
			appTooltip(c, capabilityText(c, t, watch.Purpose).MaxLines(2).Ellipsis("…"), watch.Purpose)
		}
	})
}

func (b *botScheduleState) renderWatch(c *ui.Context, deps scheduledWorkDeps, m scheduledWorkCopy) {
	t := deps.wb.Theme
	r := &b.detail
	if !r.ready && r.problem == nil {
		b.readDetail(deps)
	}
	scheduledReadStatus(c, t, m, r.loading, r.ready, r.problem, func() { b.readDetail(deps) })
	if !r.ready {
		return
	}
	detail := r.value
	watch := detail.ProductWatchSummary
	label := watch.Label
	if label == "" {
		label = watch.Purpose
	}
	scheduledHeading(c, t, label)
	ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(8).Children(func() {
		capabilityText(c, t, m.watchKind(watch.Kind))
		capabilityText(c, t, m.watchState(watch.State))
		if watch.Source == pi.ProductWatchSourceModel {
			capabilityText(c, t, m.text("Created by the Bot", "由 Bot 创建"))
		} else {
			capabilityText(c, t, m.text("Created by you", "由你创建"))
		}
	})
	if watch.State == pi.ProductWatchRestored {
		capabilityText(c, t, m.recoveryScope()).TextColor(t.Warning)
	}
	if watch.StatusReason != "" {
		scheduledValue(c, t, m.text("Status reason", "状态原因"), m.statusReason(watch.StatusReason))
	}
	if watch.Purpose != label {
		scheduledValue(c, t, m.text("Purpose", "用途"), watch.Purpose)
	}
	b.renderWatchActions(c, deps, m, detail)
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Children(func() {
		m.watchTiming(c, t, watch)
		scheduledValue(c, t, m.text("Consecutive failures", "连续失败次数"), strconv.Itoa(watch.ConsecutiveFailures))
		scheduledValue(c, t, m.text("Instructions", "指令"), watch.Prompt)
	})
	if detail.Grant != nil && detail.Grant.OwnerBotID == deps.botID {
		m.renderGrant(c, t, *detail.Grant)
	}
	m.renderOccurrences(c, t, detail)
}

func (m scheduledWorkCopy) statusReason(reason string) string {
	switch reason {
	case "grant_revoked":
		return m.text("Standing grant revoked", "常驻授权已撤销")
	case "executable_changed":
		return m.text("The executable changed", "可执行文件已更改")
	case "workspace_changed":
		return m.text("The workspace changed", "工作区已更改")
	case "grant_required":
		return m.text("Local grant required", "需要本机授权")
	default:
		return reason
	}
}

func (m scheduledWorkCopy) watchTiming(c *ui.Context, t *ui.Theme, watch pi.ProductWatchSummary) {
	if watch.NextDueAt != "" {
		scheduledValue(c, t, m.text("Next due", "下次到期"), m.instant(watch.NextDueAt, scheduledWatchZone(watch)))
	}
	if timer := watch.Timer; timer != nil {
		scheduledValue(c, t, m.text("Fire at", "触发时间"), m.instant(timer.FireAt, ""))
		policy := m.text("Skip missed time", "跳过错过的时间")
		if timer.MissedPolicy == pi.ProductMissedFireOnceCoalesced {
			policy = m.text("Fire once after a missed time", "错过后补触发一次")
		}
		scheduledValue(c, t, m.text("Missed timer", "错过的计时"), policy)
	}
	if schedule := watch.Schedule; schedule != nil {
		scheduledValue(c, t, m.text("Time zone", "时区"), schedule.TimeZone)
		if seconds := schedule.IntervalSeconds; seconds != nil {
			scheduledValue(c, t, m.text("Cadence", "执行频率"), fmt.Sprintf(m.text("Every %d seconds", "每 %d 秒"), *seconds))
		} else if schedule.Minute != nil {
			minute := *schedule.Minute
			cadence := fmt.Sprintf(m.text("Hourly at minute %02d", "每小时第 %02d 分钟"), minute)
			if schedule.Hour != nil {
				cadence = fmt.Sprintf(m.text("Daily at %02d:%02d", "每天 %02d:%02d"), *schedule.Hour, minute)
				if schedule.Frequency == "weekly" {
					days := make([]string, 0, len(schedule.DaysOfWeek))
					for _, day := range schedule.DaysOfWeek {
						if day >= 0 && day < 7 {
							days = append(days, m.weekdays()[day])
						}
					}
					cadence = fmt.Sprintf(m.text("%s at %02d:%02d", "每周 %s %02d:%02d"), strings.Join(days, m.text(", ", "、")), *schedule.Hour, minute)
				}
			}
			scheduledValue(c, t, m.text("Cadence", "执行频率"), cadence)
		}
		capabilityText(c, t, m.text("Missed schedule slots are skipped.", "错过的计划时段会跳过。"))
	}
}

func (m scheduledWorkCopy) renderOccurrences(c *ui.Context, t *ui.Theme, detail pi.ProductWatchDetail) {
	scheduledHeading(c, t, m.text("Recent occurrences", "最近运行记录"))
	if len(detail.RecentOccurrences) == 0 {
		capabilityText(c, t, m.text("No occurrences recorded yet.", "暂无运行记录。"))
	}
	for _, occurrence := range detail.RecentOccurrences[:min(20, len(detail.RecentOccurrences))] {
		if occurrence.OwnerBotID != detail.OwnerBotID || occurrence.WatchID != detail.WatchID {
			continue
		}
		ui.Column(c).Key(occurrence.OccurrenceID).FillWidth().MinWidth(0).Padding(8, 0, 0).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
				capabilityText(c, t, m.instant(occurrence.ScheduledAt, scheduledWatchZone(detail.ProductWatchSummary))).TextColor(t.Text)
				if occurrence.Truncated {
					capabilityBadge(c, t, m.text("Truncated", "已截断"), t.Warning)
				}
			})
			scheduledValue(c, t, m.text("Outcome", "结果"), m.occurrenceStatus(occurrence.Outcome))
			scheduledValue(c, t, m.text("State", "状态"), m.occurrenceStatus(occurrence.State))
			if occurrence.ExitCode != nil {
				scheduledValue(c, t, m.text("Exit code", "退出码"), strconv.Itoa(*occurrence.ExitCode))
			}
			scheduledValue(c, t, m.text("Output excerpt", "输出节选"), occurrence.Excerpt)
			scheduledValue(c, t, m.text("Digest", "摘要哈希"), occurrence.Digest)
			scheduledValue(c, t, m.text("Error", "错误"), occurrence.Error)
			scheduledValue(c, t, m.text("Coalesced into", "合并到"), occurrence.CoalescedInto)
		})
	}
}

func (m scheduledWorkCopy) occurrenceStatus(value string) string {
	switch value {
	case "baseline", "initial_baseline":
		return m.text("Initial baseline", "初始基准")
	case "changed", "change":
		return m.text("Changed", "已变化")
	case "unchanged":
		return m.text("Unchanged", "无变化")
	case "failed", "failure":
		return m.text("Failed", "失败")
	case "recovered", "recovery":
		return m.text("Recovered", "已恢复")
	case "tick", "schedule_tick":
		return m.text("Scheduled tick", "计划触发")
	case "settled":
		return m.text("Settled", "已结束")
	case "pending":
		return m.text("Pending", "待处理")
	case "unknown":
		return m.text("Unconfirmed", "尚未确认")
	default:
		return value
	}
}

func (b *botScheduleState) renderEditor(c *ui.Context, deps scheduledWorkDeps, m scheduledWorkCopy) {
	t, e, disabled := deps.wb.Theme, b.editor, b.mutationBlocked(deps)
	if e.base.WatchID == "" {
		scheduledSelect(c, t, m.text("Type", "类型"), &e.kind, disabled, []selectOption{
			{Value: string(pi.ProductWatchTimer), Label: m.watchKind(pi.ProductWatchTimer)},
			{Value: string(pi.ProductWatchSchedule), Label: m.watchKind(pi.ProductWatchSchedule)},
		})
	}
	b.renderInput(c, deps, m.text("Name", "名称"), "name", &e.label, "", disabled)
	b.renderInput(c, deps, m.text("Purpose", "用途"), "purpose", &e.purpose, "", disabled)
	b.renderInput(c, deps, m.text("Instructions (optional)", "指令（可选）"), "prompt", &e.prompt, "", disabled)
	if pi.ProductWatchKind(e.kind) == pi.ProductWatchTimer {
		b.renderInput(c, deps, m.text("Fire at (with time zone offset)", "触发时间（含时区偏移）"), "fire-at", &e.fireAt, "2026-12-01T09:00:00+08:00", disabled)
		scheduledSelect(c, t, m.text("Missed timer", "错过的计时"), &e.missed, disabled, []selectOption{
			{Value: string(pi.ProductMissedSkip), Label: m.text("Skip missed time", "跳过错过的时间")},
			{Value: string(pi.ProductMissedFireOnceCoalesced), Label: m.text("Fire once after a missed time", "错过后补触发一次")},
		})
	} else {
		b.renderCadence(c, deps, m, disabled)
		b.renderPrecheck(c, deps, m, disabled)
	}
	if e.problem != "" {
		capabilityText(c, t, e.problem).TextColor(t.Warning)
	}
	label := m.text("Save and activate", "保存并启用")
	if pi.ProductWatchKind(e.kind) == pi.ProductWatchSchedule && e.precheck != "" {
		label = m.text("Review local grant", "审核本机授权")
	}
	primaryButton(c, label).AlignSelf(ui.Start).Disabled(disabled).OnClick(func() {
		params, problem := e.parameters(deps.botID, m)
		e.problem = problem
		if problem != "" {
			return
		}
		if params.Precheck != nil {
			b.review = &scheduledWatchReview{save: &params, needed: true}
			b.editorScroll = b.scroll.Y
			b.scroll.Y, b.focusHeading = 0, true
		} else {
			b.saveWatch(deps, params)
		}
	})
}

func (b *botScheduleState) renderCadence(c *ui.Context, deps scheduledWorkDeps, m scheduledWorkCopy, disabled bool) {
	e, t := b.editor, deps.wb.Theme
	scheduledHeading(c, t, m.text("Timing", "执行时间"))
	scheduledSelect(c, t, m.text("Cadence", "执行频率"), &e.cadence, disabled, []selectOption{
		{Value: string(pi.ProductScheduleInterval), Label: m.text("Fixed interval", "固定间隔")},
		{Value: string(pi.ProductScheduleCalendar), Label: m.text("Calendar", "日历")},
	})
	b.renderInput(c, deps, m.text("Time zone", "时区"), "zone", &e.zone, "Asia/Shanghai", disabled)
	if e.cadence == string(pi.ProductScheduleInterval) {
		b.renderInput(c, deps, m.text("Interval (seconds)", "间隔（秒）"), "interval", &e.interval, "300", disabled)
		return
	}
	scheduledSelect(c, t, m.text("Repeat", "重复"), &e.frequency, disabled, []selectOption{
		{Value: "hourly", Label: m.text("Hourly", "每小时")},
		{Value: "daily", Label: m.text("Daily", "每天")},
		{Value: "weekly", Label: m.text("Weekly", "每周")},
	})
	if e.frequency != "hourly" {
		b.renderInput(c, deps, m.text("Hour (0–23)", "小时（0–23）"), "hour", &e.hour, "9", disabled)
	}
	b.renderInput(c, deps, m.text("Minute (0–59)", "分钟（0–59）"), "minute", &e.minute, "0", disabled)
	if e.frequency == "weekly" {
		capabilityText(c, t, m.text("Days of the week", "星期"))
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(4).Children(func() {
			for i, day := range m.weekdays() {
				c.Key("weekday-" + strconv.Itoa(i))
				button := secondaryButton(c, day).Disabled(disabled).OnClick(func() { e.days[i] = !e.days[i] })
				theme.SelectionButtonState(c, button, e.days[i])
			}
		})
	}
	capabilityText(c, t, m.text("Calendar times follow the selected zone’s daylight saving rules.", "日历时间遵循所选时区的夏令时规则。"))
}

func (m scheduledWorkCopy) weekdays() [7]string {
	return [7]string{
		m.text("Sun", "周日"), m.text("Mon", "周一"), m.text("Tue", "周二"), m.text("Wed", "周三"),
		m.text("Thu", "周四"), m.text("Fri", "周五"), m.text("Sat", "周六"),
	}
}

func (b *botScheduleState) renderPrecheck(c *ui.Context, deps scheduledWorkDeps, m scheduledWorkCopy, disabled bool) {
	e, t := b.editor, deps.wb.Theme
	scheduledSelect(c, t, m.text("Precheck", "预检查"), &e.precheck, disabled, []selectOption{
		{Value: "", Label: m.text("None", "无")},
		{Value: "executable", Label: m.text("Executable", "可执行文件")},
		{Value: "skill", Label: m.text("Skill script", "技能脚本")},
	})
	switch e.precheck {
	case "executable":
		b.renderInput(c, deps, m.text("Executable path", "可执行文件路径"), "executable", &e.executable, "", disabled)
		b.renderInput(c, deps, m.text("Allowed arguments (JSON array)", "允许的参数（JSON 数组）"), "args", &e.args, `["-h", "."]`, disabled)
		b.renderInput(c, deps, m.text("Timeout (ms)", "超时（毫秒）"), "timeout", &e.timeout, "5000", disabled)
		scheduledToggle(c, t, m.text("Allow workspace reads", "允许读取工作区"), &e.workspaceRead, disabled)
		scheduledToggle(c, t, m.text("Report the initial result", "报告初始结果"), &e.emit, disabled)
	case "skill":
		scheduledSelect(c, t, m.text("Skill source", "技能来源"), &e.source, disabled, []selectOption{
			{Value: "private", Label: m.text("Private", "私有")},
			{Value: "workspace", Label: m.text("Workspace", "工作区")},
			{Value: "global", Label: m.text("Global", "全局")},
			{Value: "local", Label: m.text("Local", "本地")},
		})
		b.renderInput(c, deps, m.text("Skill ID", "技能 ID"), "skill-id", &e.skill, "", disabled)
		b.renderInput(c, deps, m.text("Script name", "脚本名称"), "script", &e.script, "", disabled)
		b.renderInput(c, deps, m.text("Skill input (optional JSON)", "技能输入（可选 JSON）"), "skill-input", &e.input, "", disabled)
	}
}

func (b *botScheduleState) renderInput(c *ui.Context, deps scheduledWorkDeps, label, key string, value *string, placeholder string, disabled bool) {
	ui.Column(c).Key(key).FillWidth().MinWidth(0).Gap(4).Children(func() {
		caption := capabilityText(c, deps.wb.Theme, label)
		if !disabled {
			caption.OnClick(func() {
				if b.editor != nil {
					b.editor.focusField = key
				}
			})
		}
		input := preferenceInput(c, value, false, deps.wb.InputBorder).FillWidth().MinWidth(0).Label(label).Placeholder(placeholder).Disabled(disabled)
		if b.editor != nil && b.editor.focusField == key {
			input.Focus()
			b.editor.focusField = ""
		}
	})
}

func scheduledSelect(c *ui.Context, t *ui.Theme, label string, value *string, disabled bool, options []selectOption) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(4).Children(func() {
		capabilityText(c, t, label)
		preferenceSelect(c, value, options, label, disabled).AlignSelf(ui.Start).MaxWidthPercent(100)
	})
}

func scheduledToggle(c *ui.Context, t *ui.Theme, label string, checked *bool, disabled bool) {
	ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(8).Children(func() {
		theme.ToggleSwitch(c, checked, label, disabled)
		capabilityText(c, t, label)
	})
}

func (b *botScheduleState) renderWatchActions(c *ui.Context, deps scheduledWorkDeps, m scheduledWorkCopy, detail pi.ProductWatchDetail) {
	t, watch := deps.wb.Theme, detail.ProductWatchSummary
	blocked := b.mutationBlocked(deps) || b.detail.loading || b.detail.problem != nil
	terminal := watch.State == pi.ProductWatchSettled || watch.State == pi.ProductWatchCancelled
	ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(8).Children(func() {
		if !terminal {
			if watch.State == pi.ProductWatchActive {
				scheduledButton(c, m.text("Pause", "暂停"), func() { b.changeWatch(deps, watch, "pause", false) }).Disabled(blocked)
			} else {
				primaryButton(c, m.text("Review activation", "审核启用")).Disabled(blocked).OnClick(func() {
					grant := detail.Grant
					if grant != nil && (grant.OwnerBotID != deps.botID || grant.State != "active") {
						grant = nil
					}
					b.review = &scheduledWatchReview{watch: watch, grant: grant, needed: watch.Precheck != nil && grant == nil}
					b.scroll.Y, b.focusHeading = 0, true
				})
			}
			scheduledButton(c, m.text("Edit", "编辑"), func() {
				b.editor = editScheduledWatch(watch)
				b.result, b.problem = "", nil
				b.scroll.Y, b.focusHeading = 0, true
			}).Disabled(blocked)
			dangerButton(c, m.text("Cancel scheduled work", "取消计划工作")).Disabled(blocked).OnClick(func() {
				b.confirm, b.confirmRevision = "cancel", watch.Revision
			})
		}
		if watch.TargetConversationID != "" && deps.onOpenConversation != nil {
			scheduledButton(c, m.text("Open target chat", "打开目标对话"), func() { deps.onOpenConversation(watch.TargetConversationID) })
		}
	})
	if b.confirm == "cancel" {
		b.renderConfirmation(c, t, m, m.text("Cancel this timer or schedule and prevent future runs?", "取消此计时器或计划，停止后续运行？"), m.text("Confirm cancellation", "确认取消"), watch.Revision, blocked, func() {
			b.changeWatch(deps, watch, "cancel", false)
		})
	}
}

func (b *botScheduleState) renderConfirmation(c *ui.Context, t *ui.Theme, m scheduledWorkCopy, question, label string, revision uint64, blocked bool, confirm func()) {
	scheduledWorkCard(c, t, func() {
		changed := b.confirmRevision != revision
		if changed {
			question = m.text("This item changed. Go back and review the current revision.", "此项目已更改。请返回并查看当前版本。")
		}
		capabilityText(c, t, question).TextColor(t.Text)
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(8).Children(func() {
			dangerButton(c, label).Disabled(blocked || changed).OnClick(confirm)
			scheduledButton(c, m.text("Keep it", "保留"), func() { b.confirm = "" })
		})
	})
}

func (b *botScheduleState) renderReview(c *ui.Context, deps scheduledWorkDeps, m scheduledWorkCopy) {
	t, review := deps.wb.Theme, b.review
	watch := review.watch
	blocked := b.mutationBlocked(deps)
	if save := review.save; save != nil {
		watch = pi.ProductWatchSummary{Label: save.Label, Purpose: save.Purpose, Timer: save.Timer, Schedule: save.Schedule, Precheck: save.Precheck}
	} else {
		if !b.detail.ready && b.detail.problem == nil {
			b.readDetail(deps)
		}
		scheduledReadStatus(c, t, m, b.detail.loading, b.detail.ready, b.detail.problem, func() { b.readDetail(deps) })
		currentGrant := b.detail.value.Grant
		grantChanged := review.grant != nil && (currentGrant == nil || currentGrant.GrantID != review.grant.GrantID || currentGrant.Revision != review.grant.Revision)
		if b.detail.ready && (b.detail.value.Revision != watch.Revision || grantChanged) {
			capabilityText(c, t, m.text("This item changed. Go back and review the current revision.", "此项目已更改。请返回并查看当前版本。")).TextColor(t.Warning)
			blocked = true
		}
		blocked = blocked || !b.detail.ready || b.detail.loading || b.detail.problem != nil
	}
	if watch.Label != "" {
		scheduledHeading(c, t, watch.Label)
	}
	scheduledValue(c, t, m.text("Purpose", "用途"), watch.Purpose)
	if watch.State == pi.ProductWatchRestored {
		capabilityText(c, t, m.recoveryScope()).TextColor(t.Warning)
	}
	label := m.text("Activate", "启用")
	switch {
	case watch.Precheck == nil:
		m.watchTiming(c, t, watch)
	case review.needed:
		m.renderRequestedGrant(c, t, watch)
		label = m.text("Approve local grant and activate", "批准本机授权并启用")
	case review.grant != nil:
		m.renderGrant(c, t, *review.grant)
	}
	primaryButton(c, label).AlignSelf(ui.Start).MaxWidthPercent(100).Disabled(blocked).OnClick(func() {
		if review.save != nil {
			b.saveWatch(deps, *review.save)
		} else {
			b.changeWatch(deps, watch, "activate", review.needed)
		}
	})
}

func (m scheduledWorkCopy) renderRequestedGrant(c *ui.Context, t *ui.Theme, watch pi.ProductWatchSummary) {
	pre := watch.Precheck
	scheduledWorkCard(c, t, func() {
		scheduledHeading(c, t, m.text("Installation-local standing grant", "本机常驻授权"))
		capabilityText(c, t, m.text("Approval allows this Bot to run the precheck on this installation at the selected cadence.", "批准后，此 Bot 可以按所选频率在本机执行此预检查。"))
		scheduledValue(c, t, m.text("Precheck", "预检查"), m.artifactKind(pre.Kind))
		scheduledValue(c, t, m.text("Executable path", "可执行文件路径"), pre.Executable)
		scheduledValue(c, t, m.text("Executable digest", "可执行文件摘要"), pre.ExecutableDigest)
		if pre.Kind == "executable" {
			scheduledValue(c, t, m.text("Allowed arguments", "允许的参数"), m.arguments(pre.AllowedArgs))
			scheduledValue(c, t, m.text("Workspace reads", "读取工作区"), m.allowed(pre.WorkspaceRead))
			if pre.TimeoutMS != nil {
				scheduledValue(c, t, m.text("Timeout (ms)", "超时（毫秒）"), strconv.Itoa(*pre.TimeoutMS))
			}
		}
		scheduledValue(c, t, m.text("Skill ID", "技能 ID"), pre.SkillID)
		scheduledValue(c, t, m.text("Skill source", "技能来源"), m.skillSource(pre.SkillSource))
		scheduledValue(c, t, m.text("Script name", "脚本名称"), pre.ScriptName)
		scheduledValue(c, t, m.text("Approval digest", "批准摘要"), pre.ApprovalDigest)
		if pre.Input != nil {
			input, _ := json.Marshal(pre.Input)
			scheduledValue(c, t, m.text("Skill input", "技能输入"), string(input))
		}
		m.watchTiming(c, t, watch)
		capabilityText(c, t, m.text("The worker validates the artifact, its immutable digest and the canonical workspace when approving the grant.", "工作进程在批准授权时会校验运行对象、不可变摘要和规范化工作区。"))
	})
}

func (b *botScheduleState) renderGrants(c *ui.Context, deps scheduledWorkDeps, m scheduledWorkCopy) {
	t, r := deps.wb.Theme, &b.grants
	if !r.ready && r.problem == nil {
		b.readGrants(deps, "")
	}
	scheduledReadStatus(c, t, m, r.loading, r.ready, r.problem, func() { b.readGrants(deps, "") })
	if !r.ready {
		return
	}
	if b.grantID != "" {
		for _, grant := range r.value.Items {
			if grant.GrantID == b.grantID && grant.OwnerBotID == deps.botID {
				b.renderGrantDetail(c, deps, m, grant)
				return
			}
		}
		if r.problem != nil {
			return
		}
		if next := r.value.NextCursor; next != nil {
			b.readGrants(deps, *next)
			return
		}
		capabilityText(c, t, m.text("This grant is unavailable on this installation.", "此授权在本机不可用。"))
		return
	}
	capabilityText(c, t, m.text("Grants apply to this Bot on this installation.", "授权仅适用于本机安装中的此 Bot。"))
	if len(r.value.Items) == 0 {
		capabilityEmpty(c, t, m.text("No local standing grants.", "暂无本机常驻授权。"))
	}
	for _, grant := range r.value.Items {
		if grant.OwnerBotID != deps.botID {
			continue
		}
		ui.Column(c).Key(grant.GrantID).FillWidth().MinWidth(0).Gap(2).Children(func() {
			button := scheduledButton(c, grant.Purpose, func() {
				b.grantID, b.grantScroll = grant.GrantID, b.scroll.Y
				b.scroll.Y, b.focusHeading = 0, true
				b.result, b.problem = "", nil
			})
			appTooltip(c, button, grant.Purpose)
			if b.focusGrantID == grant.GrantID {
				button.Focus().ScrollIntoView()
				b.focusGrantID = ""
			}
			ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(8).Children(func() {
				capabilityText(c, t, m.grantState(grant.State))
				capabilityText(c, t, m.artifactKind(grant.ArtifactKind))
			})
		})
	}
	if next := r.value.NextCursor; next != nil {
		scheduledButton(c, m.text("Load more", "加载更多"), func() { b.readGrants(deps, *next) }).Disabled(r.loading || deps.client == nil)
	}
}

func (b *botScheduleState) renderGrantDetail(c *ui.Context, deps scheduledWorkDeps, m scheduledWorkCopy, grant pi.ProductWatchGrant) {
	t := deps.wb.Theme
	blocked := b.mutationBlocked(deps) || b.grants.loading || b.grants.problem != nil
	if grant.State == "active" {
		if b.confirm == "revoke" {
			b.renderConfirmation(c, t, m, m.text("Revoke this grant and invalidate its dependent watches?", "撤销此授权并停用关联监测项目？"), m.text("Confirm revocation", "确认撤销"), grant.Revision, blocked, func() {
				b.revokeGrant(deps, grant)
			})
		} else {
			dangerButton(c, m.text("Revoke grant", "撤销授权")).AlignSelf(ui.Start).Disabled(blocked).OnClick(func() {
				b.confirm, b.confirmRevision = "revoke", grant.Revision
			})
		}
	}
	if b.result == "revoke" {
		for i, id := range b.invalidated {
			c.Key(id)
			label := fmt.Sprintf(m.text("Open invalidated watch %d", "打开失效监测项目 %d"), i+1)
			scheduledButton(c, label, func() {
				b.showGrants, b.returnToGrants = false, true
				b.watchID = id
				b.detail.invalidate()
				b.scroll.Y, b.focusHeading = 0, true
			})
		}
	}
	m.renderGrant(c, t, grant)
}

func (m scheduledWorkCopy) renderGrant(c *ui.Context, t *ui.Theme, grant pi.ProductWatchGrant) {
	scheduledWorkCard(c, t, func() {
		scheduledHeading(c, t, m.text("Installation-local standing grant", "本机常驻授权"))
		capabilityText(c, t, m.grantState(grant.State))
		scheduledValue(c, t, m.text("Purpose", "用途"), grant.Purpose)
		scheduledValue(c, t, m.text("Precheck", "预检查"), m.artifactKind(grant.ArtifactKind))
		scheduledValue(c, t, m.text("Executable path", "可执行文件路径"), grant.Executable)
		scheduledValue(c, t, m.text("Executable digest", "可执行文件摘要"), grant.ExecutableDigest)
		if grant.Executable != "" || len(grant.AllowedArgs) > 0 {
			scheduledValue(c, t, m.text("Allowed arguments", "允许的参数"), m.arguments(grant.AllowedArgs))
		}
		scheduledValue(c, t, m.text("Skill ID", "技能 ID"), grant.SkillID)
		scheduledValue(c, t, m.text("Skill source", "技能来源"), m.skillSource(grant.SkillSource))
		scheduledValue(c, t, m.text("Script name", "脚本名称"), grant.ScriptName)
		scheduledValue(c, t, m.text("Approval digest", "批准摘要"), grant.ApprovalDigest)
		scheduledValue(c, t, m.text("Permissions digest", "权限摘要"), grant.PermissionsDigest)
		scheduledValue(c, t, m.text("Canonical workspace", "规范化工作区"), grant.CanonicalCWD)
		scheduledValue(c, t, m.text("Workspace reads", "读取工作区"), m.allowed(grant.WorkspaceRead))
		origins := strings.Join(grant.NetworkOrigins, m.text(", ", "、"))
		if origins == "" {
			origins = m.text("None", "无")
		}
		scheduledValue(c, t, m.text("Network origins", "网络来源"), origins)
		scheduledValue(c, t, m.text("Minimum cadence (seconds)", "最短间隔（秒）"), strconv.Itoa(grant.MinCadenceSeconds))
		scheduledValue(c, t, m.text("Maximum timeout (ms)", "最长超时（毫秒）"), strconv.Itoa(grant.MaxTimeoutMS))
		scheduledValue(c, t, m.text("Revocation reason", "撤销原因"), m.statusReason(grant.RevokedReason))
	})
}

func (m scheduledWorkCopy) grantState(state string) string {
	if state == "revoked" {
		return m.text("Revoked", "已撤销")
	}
	return m.watchState(pi.ProductWatchState(state))
}

func (m scheduledWorkCopy) skillSource(source string) string {
	switch source {
	case "private":
		return m.text("Private", "私有")
	case "workspace":
		return m.text("Workspace", "工作区")
	case "global":
		return m.text("Global", "全局")
	case "local":
		return m.text("Local", "本地")
	default:
		return source
	}
}

func (m scheduledWorkCopy) artifactKind(kind string) string {
	switch kind {
	case "executable":
		return m.text("Executable", "可执行文件")
	case "skill", "skill_script":
		return m.text("Skill script", "技能脚本")
	default:
		return kind
	}
}

func (m scheduledWorkCopy) allowed(allowed bool) string {
	if allowed {
		return m.text("Allowed", "允许")
	}
	return m.text("Disallowed", "不允许")
}

func (m scheduledWorkCopy) arguments(args []string) string {
	if len(args) == 0 {
		return m.text("None", "无")
	}
	encoded, _ := json.Marshal(args)
	return string(encoded)
}
