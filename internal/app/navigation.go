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
	a.workspaceOpen = true
	a.detail = kind
	a.focusDetailClose = true
}

func (a *appModel) closeDetail() {
	if a.railCollapsed {
		a.focusRail = true
	} else {
		a.focusProfileRow = a.contactProfileID
	}
	a.contactProfileID = ""
	a.folderChoiceGeneration++
	a.workspaceOpen = false
	a.detail = ""
	a.focusDetailClose = false
}

// detailPageActive means the right surface is the whole application page
// because the three-pane layout cannot keep its 480px reading column.
func (a *appModel) detailPageActive() bool {
	return a.activeDetail() != "" && a.workOverlay()
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
	ui.Column(c).Key("conversation-browser").Fill().MinHeight(0).Children(func() {
		ui.Row(c).FillWidth().MinHeight(40).Padding(4, 6).AlignItems(ui.Center).Gap(8).Children(func() {
			back := secondaryButton(c, msgs.BackAction()).Label(msgs.BackLabel()).OnClick(a.backProductConversations)
			if browser.focusBack {
				back.Focus()
				browser.focusBack = false
			}
			ui.Text(c, title).FontSize(typeContactTitle).FontWeight(500).FixedLineHeight(lineHeader).TextColor(t.Text).Grow(1).MinWidth(0)
			if a.railCollapsed {
				a.showContactsButton(c, t)
			}
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
					secondaryButton(c, msgs.ReloadAction()).Disabled(!a.productProfileReady()).OnClick(func() {
						a.loadProductTranscript(browser, browser.sourceID)
					})
				}
			})
		})
		a.productReadOnlyTranscript(c, t, browser)
	})
}

func (a *appModel) mainPageActive() bool {
	return a.aux == "task-context"
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
	contacts := appTooltip(c, secondaryButton(c, "").Label(label), msgs.NavShowContactsTooltip()).
		Margin(0).Padding(8).FontSize(typeNavigation).FontWeight(400).FixedLineHeight(lineNavigation)
	contacts.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			controlIcon(c, navContactsIcon, iconChrome).TextColor(t.TextMuted)
			ui.Text(c, label).FontSize(typeNavigation).FixedLineHeight(lineNavigation).TextColor(t.Text)
		})
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
	editor := a.profileEditorFor(ct.ID)
	actions := a.contactProfileActions(ct, editor)
	formActions := actions
	product := a.productHost() != nil
	if product {
		formActions.SaveInFooter = true
		formActions.Archive, formActions.Restore = nil, nil
		formActions.ToggleVisibility = nil
	}
	msgs := locales.In(c.Locale())
	ui.Column(c).Key(ct.ID).Fill().MinWidth(0).MinHeight(0).Padding(16).Gap(8).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
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
	ui.Column(c).Fill().Background(a.workTheme.Reading).Children(func() {
		switch {
		case a.aux == "task-context":
			a.auxPage(c, t)
		default:
			a.main(c, t)
		}
	})
}

func (a *appModel) auxPage(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	title := msgs.TaskContextTitle()
	closeLabel := msgs.CloseTaskContext()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).MinHeight(40).Padding(4, 6).AlignItems(ui.Center).Gap(12).Children(func() {
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

// syncDetailPageMode skips updating detailPageMode while Settings or a main
// reader owns focus, leaving evaluation until the workbench is visible again.
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
