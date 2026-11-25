package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func (a *appModel) backProductActivity(ct *contact) {
	p := &ct.activity
	switch {
	case p.file != nil:
		p.file = nil
	case p.result != nil:
		p.result = nil
	case p.legacyIssue:
		p.legacyIssue = false
	case p.activityID != "":
		p.activityID, p.activity = "", nil
		p.focusQuestion = ""
		p.problem, p.actionProblem = "", ""
	default:
		p.open, p.focusToggle = false, true
		return
	}
	p.scroll.Y, p.focusBack = 0, true
}

func (a *appModel) conversationActivities(c *ui.Context, t *ui.Theme, ct *contact) {
	p, client := &ct.activity, a.productActivityClient()
	if a.productHost() == nil && !p.loaded && !p.open {
		return
	}
	if client != nil && (!p.loaded || p.client != client || p.revision != a.productActivityRevision()) {
		a.refreshProductActivities(ct, false)
	}
	if len(p.items) == 0 && p.problem == "" && !p.open && client != nil {
		return
	}
	msgs := locales.In(c.Locale())
	ui.Row(c).Key("conversation-activities").Justify(ui.Center).Children(func() {
		ui.Column(c).Key(ct.ID).FillWidth().MaxWidth(conversationMaxWidth).Padding(0, 16).Gap(4).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				if p.open && (p.activityID != "" || p.legacyIssue) {
					back := secondaryButton(c, msgs.BackAction()).Label(msgs.BackLabel()).OnClick(func() { a.backProductActivity(ct) })
					if p.focusBack {
						back.Focus()
						p.focusBack = false
					}
				}
				toggle := secondaryButton(c, msgs.WorkActivity()).Label(msgs.WorkActivity()).OnClick(func() {
					p.open = !p.open
					p.scroll.Y = 0
				})
				if p.focusToggle || p.focusBack && p.activityID == "" && !p.legacyIssue {
					toggle.Focus()
					p.focusToggle, p.focusBack = false, false
				}
				if p.open {
					ui.Spacer(c)
					secondaryButton(c, msgs.ReloadAction()).Disabled(client == nil || p.reading || p.changing).OnClick(func() {
						a.refreshProductActivities(ct, false)
					})
				}
			})
			if p.open {
				ui.Scroll(c).FillWidth().Height(230).TrackScroll(&p.scroll).Children(func() {
					ui.Column(c).FillWidth().MinWidth(0).Gap(8).Padding(4, 8).Children(func() {
						if client == nil {
							a.activityText(c, t, msgs.WorkHistoryNotReady())
						}
						if p.problem != "" {
							ui.Text(c, p.problem).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
						}
						if p.actionProblem != "" {
							ui.Text(c, p.actionProblem).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
						}
						switch {
						case p.legacyIssue:
							if activity := ct.selectedActivity(); activity != nil {
								a.activityText(c, t, activity.Goal)
								if question := activity.openQuestion(); question != nil {
									a.activityText(c, t, question.Text)
								}
							}
						case p.result != nil:
							a.productActivityResultView(c, t, p)
						case p.activity != nil:
							a.productActivityDetails(c, t, ct, client)
						case p.activityID == "":
							for _, activity := range ct.Activities {
								if activity != nil && !activity.Archived {
									secondaryButton(c, activity.Goal).Label(msgs.IssueRowLabel(activity.Goal)).OnClick(func() { a.openLegacyBotActivity(ct, activity) })
								}
							}
							for _, item := range p.items {
								ui.Row(c).Key(item.ActivityID).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
									secondaryButton(c, item.Goal).Label(msgs.IssueRowLabel(item.Goal)).Disabled(client == nil || p.reading).OnClick(func() {
										a.openProductActivity(ct, item.ActivityID)
									})
									a.activityText(c, t, productActivityStateLabel(msgs, item.State))
								})
							}
							if p.nextCursor != nil {
								secondaryButton(c, msgs.SupplementShowAll()).Disabled(client == nil || p.reading).OnClick(func() { a.refreshProductActivities(ct, true) })
							}
							if client != nil && p.loaded && !p.reading && len(p.items) == 0 && p.problem == "" {
								a.activityText(c, t, msgs.WorkNoCurrent())
							}
						}
						if p.reading {
							a.activityText(c, t, msgs.ResultLoadingHistory())
						}
					})
				})
			}
		})
	})
}

func productActivityStateLabel(msgs locales.Messages, state string) string {
	switch state {
	case "notStarted":
		return msgs.TaskStatusNotStarted()
	case "running":
		return msgs.TaskStatusInProgress()
	case "waitingForAnswer":
		return msgs.TaskStatusWaiting()
	case "needsAttention":
		return msgs.TaskStatusNeedsAttention()
	case "completed":
		return msgs.TaskStatusCompleted()
	case "cancelled":
		return msgs.TaskStatusCanceled()
	default:
		return state
	}
}

func (a *appModel) activityText(c *ui.Context, t *ui.Theme, text string) {
	ui.Text(c, text).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
}

func (a *appModel) productActivityDetails(c *ui.Context, t *ui.Theme, ct *contact, client *pi.ProductClient) {
	p, msgs := &ct.activity, locales.In(c.Locale())
	activity := p.activity
	target := productActivityIntent{BotID: ct.ID, ActivityID: activity.ActivityID}
	pending := a.activityIntents[activity.ActivityID]
	blocked := client == nil || p.changing || pending.OperationID != "" || a.dataRecoveryBlocksChanges()
	ui.Text(c, activity.Goal).FontSize(typeSettingsTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text)
	a.activityText(c, t, productActivityStateLabel(msgs, activity.State))
	if origin := activity.OriginConversationID; origin != "" {
		source := secondaryButton(c, msgs.SourceChat()).Disabled(client == nil).OnClick(func() { a.browseProductConversations(ct, origin) })
		if p.focusSource {
			source.Focus().ScrollIntoView()
			p.focusSource = false
		}
	}
	for _, requirement := range activity.Requirements {
		a.activityText(c, t, requirement)
	}
	taskContextReadOnlyView(c, a.workTheme, productTaskContext(activity.Materials))
	if pending.OperationID != "" {
		a.activityText(c, t, msgs.SupplementUnknown())
		if p.forms[activity.ActivityID] == nil {
			a.activityText(c, t, pending.Text)
			taskContextReadOnlyView(c, a.workTheme, productTaskContext(pending.Materials))
		}
		secondaryButton(c, msgs.ReloadAction()).Disabled(client == nil || p.changing || a.dataRecoveryBlocksChanges()).OnClick(func() {
			a.runProductActivityAction(ct, client, pending, false)
		})
	}
	for _, question := range activity.Questions {
		ui.Column(c).Key(question.QuestionID).FillWidth().Gap(4).Children(func() {
			a.activityText(c, t, question.Text)
			if question.State == "open" {
				answer := target
				answer.Action, answer.Revision = "answer", question.Revision
				answer.ExecutionID, answer.QuestionID = question.ExecutionID, question.QuestionID
				button := secondaryButton(c, msgs.IssueNoticeWaiting(activity.Goal)).Disabled(blocked).OnClick(func() { a.editProductActivity(ct, answer) })
				if p.focusQuestion == question.QuestionID && !blocked {
					button.Focus().ScrollIntoView()
					p.focusQuestion = ""
				}
			}
			if question.Answer != nil {
				a.activityText(c, t, question.Answer.Text)
				taskContextReadOnlyView(c, a.workTheme, productTaskContext(question.Answer.Materials))
				switch question.Delivery {
				case "saved":
					if question.DeliveryBlocker == pi.ProductCredentialUnavailable {
						ui.Text(c, msgs.ProviderAnswerPaused()).FontSize(typeMeta).LineHeight(1.5).TextColor(t.Warning)
						linkButton(c, msgs.ConnectionRecoveryOpenSettings()).AlignSelf(ui.Start).OnClick(a.openConfiguration)
					} else {
						a.activityText(c, t, msgs.IssueAnswerSaved())
					}
				case "dispatching":
					a.activityText(c, t, msgs.IssueAnswerDispatching())
				case "unknown":
					a.activityText(c, t, msgs.SupplementUnknown())
				default:
					a.activityText(c, t, question.Delivery)
				}
			}
		})
	}
	canContinue := len(activity.OpenQuestionIDs) == 0
	for i := len(activity.Executions) - 1; i >= 0; i-- {
		execution := activity.Executions[i]
		label := msgs.IssueCurrentRun(workNoticeText(msgs, execution.State, ""))
		ui.Column(c).Key(execution.ExecutionID).Gap(4).Children(func() {
			if i != len(activity.Executions)-1 {
				label = workNoticeText(msgs, execution.State, "")
			}
			a.activityText(c, t, label)
			if execution.Partial != nil && *execution.Partial != "" {
				a.activityText(c, t, msgs.IssueCurrentResponse(*execution.Partial))
			}
			if execution.Error != "" {
				a.activityText(c, t, a.redactConnectionText(execution.Error))
			}
			switch execution.State {
			case "starting", "running":
				canContinue = false
				stop := target
				stop.Action, stop.Revision, stop.ExecutionID = "stop", execution.Revision, execution.ExecutionID
				if p.stopping == execution.ExecutionID {
					a.activityText(c, t, msgs.IssueSendCancelRequested())
				}
				secondaryButton(c, msgs.WorkStopIssueTask()).Disabled(blocked || p.stopping == execution.ExecutionID).OnClick(func() { a.submitProductActivity(ct, stop, nil) })
			case "unknown":
				if i == len(activity.Executions)-1 {
					canContinue = false
				}
			default:
				label := msgs.IssueOpenResultRun(fmt.Sprint(i + 1))
				secondaryButton(c, label).Label(label).Disabled(client == nil || p.reading).OnClick(func() {
					a.readProductActivityResult(ct, activity.ActivityID, execution.ExecutionID)
				})
			}
		})
	}
	if canContinue && p.forms[activity.ActivityID] == nil {
		target.Action, target.Revision = "continue", activity.Revision
		secondaryButton(c, msgs.SupplementAdd()).Disabled(blocked || ct.Archived).OnClick(func() { a.editProductActivity(ct, target) })
	}
	if form := p.forms[activity.ActivityID]; form != nil {
		a.productActivityEditView(c, t, ct, form, blocked)
	}
}

func (a *appModel) productActivityEditView(c *ui.Context, t *ui.Theme, ct *contact, form *productActivityForm, blocked bool) {
	p, msgs := &ct.activity, locales.In(c.Locale())
	label, submit := msgs.SupplementFormLabel(), msgs.IssueResume()
	if form.target.Action == "answer" {
		label, submit = msgs.IssueNoticeWaiting(p.activity.Goal), msgs.ContactSaveChanges()
	}
	ui.Column(c).Key(form.target.ActivityID + ":" + form.target.QuestionID).FillWidth().Gap(8).Children(func() {
		input := formTextArea(c, &form.text, false, a.workTheme.InputBorder).Label(label).Disabled(blocked).Height(80)
		if form.focus {
			input.Focus().ScrollIntoView()
			form.focus = false
		}
		if !blocked {
			form.materials.ChooseFile = func() { a.chooseActivityMaterial(ct, form) }
			form.materials.View(c, a.workTheme)
		} else {
			taskContextReadOnlyView(c, a.workTheme, form.materials.Items)
		}
		ui.Row(c).Wrap().Gap(8).Children(func() {
			empty := strings.TrimSpace(form.text) == "" && (form.target.Action == "answer" || form.materials.Count() == 0)
			secondaryButton(c, submit).Disabled(blocked || empty || form.materials.FormOpen).OnClick(func() { a.submitProductActivity(ct, form.target, form) })
			secondaryButton(c, msgs.SupplementCancel()).Disabled(blocked).OnClick(func() { delete(p.forms, form.target.ActivityID) })
		})
	})
}

func (a *appModel) productActivityResultView(c *ui.Context, t *ui.Theme, p *productActivityPanel) {
	msgs := locales.In(c.Locale())
	result := p.result
	if file := p.file; file != nil {
		a.activityText(c, t, file.Path)
		a.activityText(c, t, msgs.ResultCurrentFileDiff())
		if notice := activityResultInspectionNoticeText(msgs, *file); notice != "" {
			a.activityText(c, t, notice)
		} else if file.ReadErrorKind != "" {
			a.activityText(c, t, activityResultNoticeText(msgs, file.ReadErrorKind, file.ReadErrorDetail))
		} else if file.Size == 0 {
			a.activityText(c, t, msgs.ResultEmptyFile())
		} else {
			markdownReader(c, file.Preview, markdownReaderOptions{PlainText: true, CopyLabel: msgs.CopyResultLabel()}).FontSize(typeBody).FixedLineHeight(lineBody)
		}
		return
	}
	ui.Text(c, msgs.ResultHeading()).FontSize(typeSettingsTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text)
	if result.Text == "" {
		a.activityText(c, t, msgs.ResultNoAssistant())
	} else {
		markdownReader(c, result.Text, markdownReaderOptions{CopyLabel: msgs.CopyResultLabel()}).FontSize(typeBody).FixedLineHeight(lineBody)
	}
	a.activityText(c, t, msgs.ResultReportedFiles())
	if len(result.Files) == 0 {
		a.activityText(c, t, msgs.ResultNoFiles())
	}
	for i, file := range result.Files {
		ui.Column(c).Key(fmt.Sprintf("%s/%d", file.CallID, i)).Gap(4).Children(func() {
			a.activityText(c, t, file.Path)
			ui.Row(c).Wrap().Gap(8).Children(func() {
				secondaryButton(c, msgs.ResultReadFile()).Label(msgs.ResultFileRowLabel(fmt.Sprint(i+1), filepath.Base(file.Path))).OnClick(func() {
					a.inspectProductActivityFile(p, file.Path)
				})
				secondaryButton(c, msgs.ReaderOpenFileLabel()).OnClick(func() { a.openProductActivityFile(p, file.Path, false) })
				secondaryButton(c, msgs.ReaderShowInFinderLabel()).OnClick(func() { a.openProductActivityFile(p, file.Path, true) })
			})
		})
	}
}
