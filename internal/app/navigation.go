package app

import (
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

type detailKind string

const detailProfile detailKind = "profile"

func (a *appModel) activeDetail() detailKind {
	if !a.workspaceOpen || a.detail != detailProfile {
		return ""
	}
	return a.detail
}

func (a *appModel) detailIs(kind detailKind) bool {
	return a.workspaceOpen && a.activeDetail() == kind
}

func (a *appModel) openDetail(kind detailKind) {
	a.clearBotDrawers()
	a.priorChats = nil
	a.headerMenuOpen = false
	a.workspaceOpen = true
	a.detail = kind
	a.focusDetailClose = true
}

func (a *appModel) openBotScheduledWork(contactID string) {
	if a.contactByID(contactID) == nil {
		return
	}
	a.openContactProfile(contactID)
	a.scheduledWorkBotID = contactID
	a.schedules.bot(contactID).focusHeading = true
}

func (a *appModel) openBotSessions(botID, opener string) {
	ct := a.contactByID(botID)
	if ct == nil || ct.Archived || !a.productProfileReady() || a.dataRecoveryBlocksChanges() {
		return
	}
	a.openContactProfile(botID)
	bot := a.sessions.botSession(botID)
	bot.opener, bot.detail = opener, nil
	a.sessions.openSessionInspection(a.sessionInspectorFallbackDeps(ct))
}

func (a *appModel) clearBotDrawers() {
	a.backgroundWork.panel, a.backgroundWork.returnFocus = nil, ""
	a.sessions.activeBotID = ""
	a.scheduledWorkBotID = ""
	a.focusScheduledWorkOpener, a.focusSessionsOpener = "", ""
}

func (a *appModel) focusBotDrawerOpener(botID, opener string) {
	switch opener {
	case "header-overview:" + botID:
		a.focusHeaderOverview = botID
		a.focusProfileRow, a.focusRail = "", false
	case "row:" + botID:
		if !a.railCollapsed {
			a.focusProfileRow = botID
		}
	}
}

func (a *appModel) closeDetail() {
	if a.railCollapsed {
		a.focusRail = true
	} else {
		a.focusProfileRow = a.contactProfileID
	}
	a.contactProfileID = ""
	a.clearBotDrawers()
	a.folderChoiceGeneration++
	a.workspaceOpen = false
	a.detail = ""
	a.focusDetailClose = false
}

func (a *appModel) detailPageActive() bool {
	return a.activeDetail() != "" && a.workOverlay() && a.priorChats == nil
}

func (a *appModel) backProductConversations() {
	browser := a.priorChats
	a.priorChats = nil
	if browser.contact != nil && a.sessions.activeBotID == browser.contact.ID {
		a.sessions.botSession(browser.contact.ID).focusSourceChat = true
		return
	}
	browser.contact.activity.focusSource = true
}

func (a *appModel) productConversationsView(c *ui.Context, t *ui.Theme, browser *productConversationBrowser) {
	msgs := locales.In(c.Locale())
	title := msgs.SourceChat()
	owner := browser.contact
	if browser.ownerID != "" {
		owner = a.contactByID(browser.ownerID)
	}
	name := browser.ownerID
	if owner != nil {
		name = owner.Name
	}
	winW := a.windowContentWidth(c)
	rowStartX, rowEndX := a.mainHeaderBounds(winW)
	leftSafe, rightSafe := windowControlInsets(c, rowStartX, rowEndX, winW)
	ui.Column(c).Key("conversation-browser").Fill().MinHeight(0).Children(func() {
		ui.Row(c).FillWidth().MinHeight(max(float32(48), c.TitleBar().Height)).Padding(6, max(12, rightSafe), 6, max(12, leftSafe)).AlignItems(ui.Center).Gap(8).DragWindow().Children(func() {
			if a.railCollapsed {
				a.showContactsButton(c, t)
			}
			back := secondaryButton(c, msgs.BackAction()).Label(msgs.BackLabel()).OnClick(a.backProductConversations)
			if browser.focusBack {
				back.Focus()
				browser.focusBack = false
			}
			ui.Text(c, title).FontSize(typeContactTitle).FontWeight(500).FixedLineHeight(lineHeader).TextColor(t.Text).Grow(1).MinWidth(0)
		})
		ui.Row(c).FillWidth().Justify(ui.Center).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(conversationMaxWidth).Padding(8, 16).Gap(4).Children(func() {
				a.activityText(c, t, name)
				a.activityText(c, t, msgs.ReadOnlyChat())
				if browser.reading {
					a.activityText(c, t, msgs.LoadingChatHistory())
				} else if browser.problem != "" || !a.productProfileReady() {
					summary := msgs.HistoryReadFailed()
					if !a.productProfileReady() {
						summary = msgs.HistoryRuntimeNotReady()
					}
					a.recoveryNotice(c, t, "prior-chat:"+browser.selectedID, summary, browser.problem, false)
					secondaryButton(c, msgs.ReloadAction()).AlignSelf(ui.Start).Disabled(!a.productProfileReady()).OnClick(func() {
						a.refreshProductTranscript(browser)
					})
				}
			})
		})
		a.productReadOnlyTranscript(c, t, browser)
	})
}

func (a *appModel) mainPageActive() bool {
	return a.aux == "task-context" || a.priorChats != nil && a.activeDetail() != "" && a.workOverlay()
}

func (a *appModel) windowContentWidth(c *ui.Context) float32 {
	width, _ := c.Size()
	if width <= 0 {
		return float32(a.layoutWidth())
	}
	return width
}

func windowControlInsets(c *ui.Context, rowStartX, rowEndX, winW float32) (left, right float32) {
	bar := c.TitleBar()
	if bar.Height <= 0 {
		return 0, 0
	}
	if bar.Left > rowStartX {
		left = bar.Left - rowStartX + 4
	}
	if edge := winW - bar.Right; bar.Right > 0 && edge < rowEndX {
		right = rowEndX - edge + 4
	}
	return left, right
}

func (a *appModel) mainHeaderBounds(winW float32) (rowStartX, rowEndX float32) {
	rowStartX, rowEndX = 5, winW-5
	if a.mainPageActive() {
		return rowStartX, rowEndX
	}
	if railWidth := a.effectiveRailWidth(); railWidth > 0 {
		rowStartX += railWidth + paneSeparatorWidth
	}
	if a.activeDetail() != "" {
		rowEndX -= a.effectiveDetailWidth() + paneSeparatorWidth
	}
	return rowStartX, rowEndX
}

func (a *appModel) detailRowBounds(winW, extraPad float32) (rowStartX, rowEndX float32) {
	if a.detailPageActive() {
		return 5 + extraPad, winW - 5 - extraPad
	}
	return winW - a.effectiveDetailWidth() + 1 + extraPad, winW - extraPad
}

func (a *appModel) focusDetailControl(c *ui.Context, button ui.Element) {
	if a.focusDetailClose {
		button.Focus()
		a.focusDetailClose = false
	}
}

func (a *appModel) showContactsButton(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	label := msgs.RailContactsTitle()
	contacts := iconControlFace(c, appTooltip(c, ui.Button(c, "").Key("show-contacts-btn").Label(label), msgs.NavShowContactsTooltip()), iconChrome, false)
	contacts.Children(func() {
		controlIcon(c, railShowIcon, iconChrome).TextColor(t.TextMuted)
	})
	if a.focusRail {
		contacts.Focus()
		a.focusRail = false
	}
	contacts.OnClick(a.toggleRail)
}

func (a *appModel) profileScrollFor(contactID string) *ui.ScrollState {
	if a.profileScrolls == nil {
		a.profileScrolls = map[string]*ui.ScrollState{}
	}
	if a.profileScrolls[contactID] == nil {
		a.profileScrolls[contactID] = &ui.ScrollState{}
	}
	return a.profileScrolls[contactID]
}

func (a *appModel) profileDetail(c *ui.Context, t *ui.Theme) {
	ct := a.contactByID(a.contactProfileID)
	if ct == nil {
		return
	}
	product := a.productHost() != nil
	if product && a.backgroundWork.panel != nil && a.backgroundWork.panel.bot.ID == ct.ID {
		a.backgroundWorkView(c, t, a.backgroundWork.panel)
		return
	}
	if product && a.scheduledWorkBotID == ct.ID {
		ui.Column(c).Key(ct.ID).Fill().MinWidth(0).MinHeight(0).Children(func() {
			a.schedules.render(c, a.scheduledWorkDeps(c, ct))
		})
		return
	}
	if product && a.sessions.activeBotID == ct.ID {
		a.sessions.sessionInspectionView(c, a.sessionInspectorDeps(c, ct))
		return
	}
	editor := a.profileEditorFor(ct.ID)
	actions := a.contactProfileActions(ct, editor)
	formActions := actions
	if product {
		formActions.SaveInFooter = true
		formActions.Archive, formActions.Restore = nil, nil
		formActions.ToggleVisibility = nil
	}
	msgs := locales.In(c.Locale())
	winW := a.windowContentWidth(c)
	rowStartX, rowEndX := a.detailRowBounds(winW, 16)
	leftSafe, rightSafe := windowControlInsets(c, rowStartX, rowEndX, winW)
	ui.Column(c).Key(ct.ID).Fill().MinWidth(0).MinHeight(0).Padding(0, 16, 16, 16).Gap(8).Children(func() {
		ui.Row(c).FillWidth().MinHeight(max(float32(48), c.TitleBar().Height)).Padding(12, rightSafe, 4, leftSafe).AlignItems(ui.Center).Gap(8).DragWindow().Children(func() {
			if a.detailPageActive() && a.railCollapsed {
				a.showContactsButton(c, t)
			}
			back := detailBackButton(c, t, msgs.ProfileClose(), msgs.ProfileBackTooltip()).OnClick(a.closeDetail)
			a.focusDetailControl(c, back)
			ui.Text(c, msgs.ContactProfileAction()).FontSize(typePageTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text).Grow(1)
		})
		profileScroll := ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).TrackScroll(a.profileScrollFor(ct.ID))
		if product {
			profileScroll.Padding(0, 8, 0, 0)
		}
		profileScroll.Children(func() {
			ui.Column(c).FillWidth().MaxWidth(settingsContentWidth).AlignSelf(ui.Center).Gap(8).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					contactAvatar(c, t, ct, 32)
					ui.Text(c, ct.Name).FontSize(typePageTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text).MinWidth(0).MaxLines(2)
				})
				editor.view(c, t, formActions, a.workTheme.InputBorder)
				if product {
					a.botCapabilitiesView(c, t, ct, editor)
				}
				a.botMemoryView(c, t, ct)
			})
		})
		if product {
			footer := ui.Column(c).FillWidth().Shrink(0).Gap(4).Padding(8, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
			footer.Children(func() {
				editor.saveView(c, t, actions, editor.Archived || editor.Busy || a.capabilitiesBlocked())
				editor.managementView(c, t, actions)
			})
		}
	})
}

func (a *appModel) mainPage(c *ui.Context, t *ui.Theme) {
	if a.aux == "task-context" {
		ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).AlignSelf(ui.Stretch).Margin(4).Radius(16).Border(1, t.Border).Clip().Background(a.workTheme.Reading).Children(func() {
			a.auxPage(c, t)
		})
		return
	}
	ui.Row(c).Fill().Children(func() { a.main(c, t) })
}

func (a *appModel) auxPage(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	title := msgs.TaskContextTitle()
	closeLabel := msgs.CloseTaskContext()
	winW := a.windowContentWidth(c)
	leftSafe, rightSafe := windowControlInsets(c, 5, winW-5, winW)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().MinHeight(max(float32(40), c.TitleBar().Height)).Padding(4, max(6, rightSafe), 4, max(6, leftSafe)).AlignItems(ui.Center).Gap(12).DragWindow().Children(func() {
			back := appTooltip(c, secondaryButton(c, msgs.BackAction()).Label(closeLabel), msgs.BackFromAux(title)).OnClick(func() { a.closeAux() })
			if a.focusAuxBack {
				back.Focus()
				a.focusAuxBack = false
			}
			ui.Text(c, title).FontSize(typeContactTitle).FontWeight(500).FixedLineHeight(lineHeader).TextColor(t.Text)
		})
		ui.Scroll(c).Grow(1).TrackScroll(&a.taskContextScroll).Padding(0, 16).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(conversationMaxWidth).AlignItems(ui.Start).Gap(8).Children(func() {
				a.taskContextPanel(c, t)
			})
		})
	})
}

func (a *appModel) syncDetailPageMode() {
	if a.settingsOpen || a.mainPageActive() {
		return
	}
	now := a.detailPageActive()
	if now == a.detailPageMode {
		return
	}
	a.detailPageMode = now
	if a.workspaceOpen {
		a.focusDetailClose = true
	}
}

func detailBackButton(c *ui.Context, t *ui.Theme, accessible, tooltip string, visible ...string) ui.Element {
	label := locales.In(c.Locale()).BackLabel()
	if len(visible) > 0 && visible[0] != "" {
		label = visible[0]
	}
	button := appTooltip(c, secondaryButton(c, "").Label(accessible), tooltip).Padding(4, 8)
	button.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
			controlIcon(c, navBackIcon, iconChrome).TextColor(t.TextMuted)
			ui.Text(c, label).FontSize(typeAction).FixedLineHeight(lineAction).TextColor(t.Text)
		})
	})
	return button
}
