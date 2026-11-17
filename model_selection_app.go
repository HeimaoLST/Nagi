package main

import (
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

const piSessionModelUnavailable = "Session-model actions are unavailable during Pi integration. Connection settings remain available for new conversations."

func (a *appModel) openComposerModel() {
	if a.composerModelOpen {
		a.closeComposerModel()
		return
	}
	if a.prepareConversationModel() {
		a.composerModelOpen = true
		a.composerModelFocus = true
	}
}

func (a *appModel) prepareConversationModel() bool {
	ct := a.selectedContact()
	if ct == nil || a.shutdownStarted || a.connectionApplying {
		return false
	}
	return a.openSessionModel(ct.SessionID, "conversation", ct.Name)
}

func (a *appModel) closeComposerModel() {
	a.composerModelOpen = false
}

func (a *appModel) chooseComposerModel(_ int) {
	if a.prepareConversationModel() {
		a.composerModelOpen = true
		a.composerModelFocus = true
	}
}

func (a *appModel) openSessionModel(sessionID, kind, label string) bool {
	if a.shutdownStarted || a.connectionApplying {
		return false
	}
	a.composerModelOpen = false
	if kind == "work" {
		a.cancelHistoryFullDisplay()
		a.composerPanel = ""
		a.composerFocusModel = false
		a.composerFocusContext = false
		a.aux = "model"
		a.focusAuxBack = true
	}
	a.modelPanelKind = kind
	a.modelPanelSessionID = sessionID
	a.modelPanelLabel = label
	a.modelPanelMessage = ""
	a.modelPanelError = piSessionModelUnavailable
	a.modelPanelErrorDetail = ""
	a.modelForm = nil
	a.modelOpenToken++
	return true
}

func (a *appModel) modelPanel(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	pageHeading(c, msgs.ModelSessionTitle())
	ui.Text(c, piSessionModelUnavailable).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
	secondaryButton(c, msgs.ModelClose()).OnClick(func() { a.closeAux() })
}

func (a *appModel) modelSessionStatus(_ string) string {
	if a.demo {
		return ""
	}
	return piSessionModelUnavailable
}
