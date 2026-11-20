package main

import (
	"strings"

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
	wasOpen := a.workspaceOpen
	if wasOpen && a.activeDetail() == kind {
		a.focusDetailClose = true
		return
	}
	a.workspaceOpen = true
	a.detail = kind
	a.focusDetailClose = true
	if !wasOpen {
		a.persist()
	}
}

func (a *appModel) closeDetail() {
	if a.activeDetail() == detailProfile {
		a.focusContactProfile = true
	}
	a.workspaceOpen = false
	a.detail = ""
	a.focusDetailClose = false
	a.persist()
}

// detailPageActive means the right surface is the whole application page
// because the three-pane layout cannot keep its 480px reading column.
func (a *appModel) detailPageActive() bool {
	return a.activeDetail() != "" && a.workOverlay()
}

func (a *appModel) mainPageActive() bool {
	switch a.aux {
	case "sources", "task-context":
		return !a.composerInlineAux()
	default:
		return false
	}
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

func (a *appModel) profileDetail(c *ui.Context, t *ui.Theme, narrow bool) {
	ct := a.selectedContact()
	if ct == nil {
		return
	}
	msgs := locales.In(c.Locale())
	ui.Column(c).FillHeight().Padding(16).Gap(8).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			if narrow {
				back := detailBackButton(c, t, msgs.ProfileClose(), msgs.ProfileBackTooltip()).OnClick(func() { a.closeDetail() })
				a.focusDetailControl(c, back)
				ui.Text(c, msgs.NavContactProfile()).FontSize(typePageTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text).Grow(1)
				return
			}
			ui.Text(c, msgs.NavContactProfile()).FontSize(typePageTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text).Grow(1)
			close := appTooltip(c, secondaryButton(c, msgs.ProfileClose()), msgs.ProfileBackTooltip()).OnClick(func() { a.closeDetail() })
			a.focusDetailControl(c, close)
		})
		ui.Scroll(c).Grow(1).TrackScroll(a.profileScrollFor(ct.ID)).Children(func() {
			ui.Column(c).Gap(8).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					a.contactIcon(c, t, 32)
					ui.Column(c).MinWidth(0).Shrink(1).Gap(1).Children(func() {
						ui.Text(c, ct.Name).FontSize(typePageTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text).MaxLines(2)
						ui.Text(c, msgs.StatusDetail(a.statusLabel(ct, msgs), ct.Role)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(a.statusColor(t, ct.Status))
					})
				})
				if ct.Archived {
					ui.Text(c, msgs.ProfileArchivedNotice()).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.Warning)
				}
				a.profileSummaryField(c, t, msgs.ProfilePurpose(), valueOr(ct.Purpose, msgs.ProfileNoPurpose()))
				instructions := strings.TrimSpace(ct.Instructions)
				if instructions == "" {
					instructions = msgs.ProfileNoInstructions()
				}
				a.profileSummaryField(c, t, msgs.ProfileInstructions(), instructions)
				workspace := strings.TrimSpace(ct.Workspace)
				if workspace == "" {
					workspace = msgs.ProfileWorkspaceInherited()
				}
				a.profileSummaryField(c, t, msgs.ProfileWorkspace(), workspace)
				session := msgs.ProfileNoSession()
				if ct.SessionID != "" {
					session = msgs.ProfileSession()
					if !ct.Loaded {
						session = msgs.ProfileSessionNotLoaded()
					}
				}
				a.profileSummaryField(c, t, msgs.ProfileConversation(), session)
				active, unknown := a.contactActivity(ct)
				activity := msgs.ProfileNoActivity()
				switch {
				case unknown:
					activity = msgs.ProfileActivityUnknown()
				case active:
					activity = msgs.ProfileActivityActive()
				}
				a.profileSummaryField(c, t, msgs.ProfileActivity(), activity)
				edit := secondaryButton(c, msgs.ProfileEditFull()).TextColor(t.Text).OnClick(func() { a.editContactProfile() })
				if a.focusProfileEdit {
					edit.Focus()
					a.focusProfileEdit = false
				}
			})
		})
	})
}

func (a *appModel) profileSummaryField(c *ui.Context, t *ui.Theme, label, value string) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(2).Children(func() {
		ui.Text(c, label).FontSize(typeMeta).FontWeight(500).TextColor(t.TextMuted)
		ui.Text(c, value).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
	})
}

func (a *appModel) mainPage(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(a.workTheme.Reading).Children(func() {
		switch {
		case a.aux == "sources":
			a.sourcesPage(c, t)
		case a.aux == "task-context":
			a.auxPage(c, t)
		default:
			a.main(c, t)
		}
	})
}

func (a *appModel) sourcesPage(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(16, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			back := appTooltip(c, secondaryButton(c, msgs.BackAction()).Label(msgs.CloseSources()), msgs.BackFromSources()).OnClick(func() { a.closeAux() })
			if a.focusSourcesBack {
				back.Focus()
				a.focusSourcesBack = false
			}
			ui.Text(c, msgs.SourcesTitle()).FontSize(typePageTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text)
		})
		ui.Scroll(c).Grow(1).TrackScroll(&a.sourcesScroll).Padding(0, 16).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(conversationMaxWidth).AlignItems(ui.Start).Gap(8).Children(func() {
				a.sourcesPanel(c, t)
			})
		})
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
		ui.Scroll(c).Grow(1).TrackScroll(a.auxScrollFor(a.aux)).Padding(0, 16).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(conversationMaxWidth).AlignItems(ui.Start).Gap(8).Children(func() {
				a.taskContextPanel(c, t)
			})
		})
	})
}

func (a *appModel) syncDetailPageMode() {
	// A Settings or main-reader page is not showing the workbench. Do not
	// advance the detail page mode or arm a hidden workbench close while that
	// page owns focus; the next visible workbench frame evaluates it.
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
