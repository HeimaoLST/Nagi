package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

const (
	settingsTabAppearance = "appearance"
	settingsTabConnection = "connection"
	settingsTabContact    = "contact"
	settingsTabAdvanced   = "advanced"

	settingsSourceRail          = "rail"
	settingsSourceProfile       = "profile"
	settingsSourceWork          = "work"
	settingsSourceConnection    = "connection"
	settingsSourceConfiguration = "configuration"
	settingsSourceCommand       = "command"
	settingsSourceComposer      = "composer"
	settingsSourceModel         = "composer-model"
	settingsSourceHistory       = "history"
	settingsSourceAux           = "aux"
	settingsSourceLink          = "link"
)

var applyAppLocale = mygo.App.SetLocale
var loadLanguageMode = readSavedLanguageTag

func normalizeLanguageMode(mode string) string {
	return strings.TrimSpace(mode)
}

func languageModeIndex(mode string) int {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return 0
	}
	if i18n.Match([]string{mode}, []string{"zh-Hans", "en"}) == "zh-Hans" {
		return 1
	}
	return 2
}

func languageSelfName(msgs locales.Messages, locale string) string {
	if locale == "zh-Hans" {
		return msgs.LanguageChinese()
	}
	return msgs.LanguageEnglish()
}

func (a *appModel) ensureLanguageMode() {
	if a.languageModeLoaded {
		return
	}
	a.languageMode = normalizeLanguageMode(loadLanguageMode())
	a.languageModeLoaded = true
}

func (a *appModel) setLanguageMode(mode string) {
	mode = normalizeLanguageMode(mode)
	applyAppLocale(mode)
	a.languageMode = mode
	a.languageModeLoaded = true
}

func (a *appModel) ensureSettingsProfileEditor(contactID string) *contactProfileEditor {
	ct := a.contactByID(contactID)
	if ct == nil {
		return nil
	}
	if a.settingsProfiles == nil {
		a.settingsProfiles = map[string]*contactProfileEditor{}
	}
	editor := a.settingsProfiles[contactID]
	if editor == nil {
		editor = newContactProfileEditor(contactProfileDraft(ct))
		a.settingsProfiles[contactID] = editor
	}
	a.contactProfile = editor
	a.contactProfileID = contactID
	return editor
}

func (a *appModel) profileEditorFor(contactID string) *contactProfileEditor {
	if a.settingsProfiles != nil {
		if editor := a.settingsProfiles[contactID]; editor != nil {
			return editor
		}
	}
	if a.contactProfile != nil && a.contactProfileID == contactID {
		return a.contactProfile
	}
	return nil
}

func (a *appModel) settingsContactEditor() *contactProfileEditor {
	if a.settingsContactID == "" {
		a.settingsContactID = a.selectedContact().ID
	}
	editor := a.ensureSettingsProfileEditor(a.settingsContactID)
	if editor == nil {
		a.settingsContactID = a.selectedContact().ID
		editor = a.ensureSettingsProfileEditor(a.settingsContactID)
	}
	if editor == nil {
		return nil
	}
	ct := a.contactByID(a.settingsContactID)
	editor.Archived = ct.Archived
	editor.HasConversation = ct.SessionID != ""
	editor.ActiveWork, editor.UnknownWork = a.contactActivity(ct)
	return editor
}

func (a *appModel) openSettingsTab(tab, source string) {
	if source != settingsSourceLink {
		a.settingsReturnLink = markdownLinkTarget{}
		a.linkReturnTarget = markdownLinkTarget{}
		a.linkReturnSeen = false
		a.markdownLinkFocus = markdownLinkTarget{}
	}
	switch tab {
	case settingsTabAppearance, settingsTabConnection, settingsTabContact, settingsTabAdvanced:
	default:
		tab = settingsTabAppearance
	}
	wasOpen := a.settingsOpen
	if !wasOpen || tab != a.settingsTab {
		a.folderChoiceGeneration++
	}
	if !wasOpen {
		if a.aux == "model" && a.modelPanelKind == "work" {
			target := modelSelectionTarget{Kind: "work", Label: a.modelPanelLabel, Config: runtimeConnectionConfig{SessionID: a.modelPanelSessionID}}
			if a.modelForm != nil {
				target = a.modelForm.Target
			}
			a.settingsReturnModelTarget = &target
			a.settingsReturnModel = a.modelForm
		}
		a.settingsReturnFocus = source
		a.settingsFocusBack = true
	}
	a.closeComposerModel()
	if a.modelForm != nil {
		a.modelForm.focusField = ""
	}
	a.settingsOpen = true
	a.settingsTab = tab
	if tab == settingsTabAppearance {
		a.languageModeLoaded = false
	}
	if tab == settingsTabAdvanced {
		a.startupDetailsOpen = true
	}
	a.settingsScroll.Y = 0
	if !wasOpen {
		a.settingsFocusTab = ""
	} else {
		a.settingsFocusTab = tab
	}
	if a.settingsContactID == "" {
		a.settingsContactID = a.selectedContact().ID
	}
	if tab == settingsTabContact {
		if editor := a.settingsContactEditor(); editor != nil {
			editor.focusField = ""
		}
	}
	a.ensureConnectionForm()
	if a.connectionForm != nil {
		a.connectionForm.focusField = ""
	}
}

func (a *appModel) selectSettingsTab(tab string) {
	if tab == a.settingsTab {
		a.settingsScroll.Y = 0
		a.settingsFocusTab = tab
		return
	}
	a.openSettingsTab(tab, a.settingsReturnFocus)
}

func (a *appModel) closeSettings() {
	if !a.settingsOpen || (a.modelOpActive && a.settingsReturnModelTarget != nil && a.settingsReturnModel == nil) {
		return
	}
	a.folderChoiceGeneration++
	a.settingsOpen = false
	if target := a.settingsReturnModelTarget; target != nil && a.aux == "model" {
		if a.settingsReturnModel != nil {
			a.modelOpenToken++
			a.modelForm = a.settingsReturnModel
			a.modelPanelKind = target.Kind
			a.modelPanelSessionID = target.Config.SessionID
			a.modelPanelLabel = target.Label
		} else {
			a.openSessionModel(target.Config.SessionID, target.Kind, target.Label)
		}
	}
	a.settingsReturnModel = nil
	a.settingsReturnModelTarget = nil
	a.settingsFocusBack = false
	a.settingsFocusTab = ""
	switch a.settingsReturnFocus {
	case settingsSourceProfile:
		if a.detailIs(detailProfile) {
			a.focusProfileEdit = true
		} else {
			a.focusContactProfile = true
		}
	case settingsSourceWork:
		if a.detailPageActive() {
			a.focusDetailClose = true
		} else {
			a.focusWorkspace = true
		}
	case settingsSourceConnection:
		a.focusConnectionEntry = true
	case settingsSourceConfiguration:
		a.focusConfiguration = true
	case settingsSourceModel:
		a.composerFocusModel = true
	case settingsSourceComposer:
		a.focusComposer = true
	case settingsSourceHistory:
		switch {
		case a.contactHistoryFull != nil:
			a.focusHistoryFullBack = true
		case a.detailIs(detailHistory):
			a.focusDetailClose = true
		default:
			a.focusHistory = true
		}
	case settingsSourceLink:
		if a.settingsReturnLink != (markdownLinkTarget{}) {
			a.markdownLinkFocus = a.settingsReturnLink
			a.linkReturnTarget = a.settingsReturnLink
			a.linkReturnSeen = false
			a.armActiveSurfaceFallback()
		}
		a.settingsReturnLink = markdownLinkTarget{}
	case settingsSourceAux:
		switch {
		case a.issueResult.Open:
			if a.issueResult.Panel.FocusReturn == nil {
				if a.issueResult.Panel.ReaderKey != "" {
					a.issueResult.Panel.FocusReader = true
				} else {
					a.issueResult.Panel.FocusClose = true
				}
			}
		case a.aux == "sources":
			a.focusSourcesBack = true
		case a.aux == "model":
			if a.composerInlineAux() {
				a.composerFocusModel = true
			} else {
				a.focusAuxBack = true
			}
		case a.aux == "task-context":
			if a.composerInlineAux() {
				a.composerFocusContext = true
			} else {
				a.focusAuxBack = true
			}
		default:
			if a.detailPageActive() {
				a.focusDetailClose = true
			} else {
				a.focusWorkspace = true
			}
		}
	default:
		a.focusSettings = true
	}
	a.settingsReturnFocus = ""
}

func (a *appModel) connectionApplied() bool {
	return a.rt != nil && a.runtimeStatus == runtimeStateReady && a.connectionEffective != nil
}

func (a *appModel) settingsLandingTab() string {
	if a.connectionConfigurationBlocked() {
		return settingsTabConnection
	}
	return settingsTabAppearance
}

func (a *appModel) settingsPage(c *ui.Context, t *ui.Theme) {
	if a.settingsTab == "" {
		a.settingsTab = a.settingsLandingTab()
	}
	if a.settingsContactID == "" {
		a.settingsContactID = a.selectedContact().ID
	}
	a.ensureConnectionForm()
	if a.layoutWidth() < int(preferenceBreakpoint) {
		ui.Column(c).Fill().Background(a.workTheme.Reading).Children(func() {
			a.settingsSidebar(c, t, true)
			a.settingsMain(c, t, true)
		})
		return
	}
	ui.Row(c).Fill().Background(a.workTheme.Reading).Children(func() {
		a.settingsSidebar(c, t, false)
		a.settingsMain(c, t, false)
	})
}

func (a *appModel) settingsSidebar(c *ui.Context, t *ui.Theme, narrow bool) {
	msgs := locales.In(c.Locale())
	width := settingsSidebarWidth
	if narrow {
		width = 0
	}
	column := ui.Column(c).Gap(12).Padding(12).Background(a.workTheme.Rail).AlignItems(ui.Start)
	if narrow {
		column.FillWidth()
	} else {
		column.FillHeight().Width(width)
	}
	column.Children(func() {
		ui.Toolbar(c, func() {
			back := secondaryButton(c, "").Label(msgs.SettingsBackAccessible()).Padding(8, 14, 8, 10)
			back.Children(func() {
				ui.Icon(c, navBackIcon).Width(16).Height(16).TextColor(t.TextMuted)
				ui.Text(c, msgs.SettingsBack()).FontSize(typeAction).TextColor(t.Text)
			})
			back.Disabled(a.modelOpActive && a.settingsReturnModelTarget != nil && a.settingsReturnModel == nil).OnClick(func() { a.closeSettings() })
			if a.settingsFocusBack {
				back.Focus()
				a.settingsFocusBack = false
			}
		}).Label(msgs.SettingsNavigationLabel()).AlignSelf(ui.Start).Padding(0)
		if a.modelOpActive && a.settingsReturnModelTarget != nil && a.settingsReturnModel == nil {
			ui.Text(c, msgs.ModelWaiting()).FontSize(typeAction).LineHeight(1.5).TextColor(t.TextMuted)
		}
		ui.Text(c, msgs.SettingsTitle()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
		nav := func(label, tab string) {
			selected := a.settingsTab == tab
			button := sharedControlFace(c, ui.ButtonBase(c).Label(label), 8, 4, false, false, selected).
				FontSize(typeNavigation).FontWeight(400).FixedLineHeight(lineNavigation).
				Padding(8).Justify(ui.Start).TextColor(t.Text)
			if !narrow {
				button.FillWidth()
			}
			button.Children(func() { ui.Text(c, label).FontSize(typeNavigation).FontWeight(400).FixedLineHeight(lineNavigation) })
			button.OnClick(func() { a.selectSettingsTab(tab) })
			if a.settingsFocusTab == tab {
				button.Focus()
				a.settingsFocusTab = ""
			}
		}
		if narrow {
			ui.Row(c).Gap(4).Wrap().Children(func() {
				nav(msgs.SettingsGeneral(), settingsTabAppearance)
				nav(msgs.SettingsConnection(), settingsTabConnection)
				nav(msgs.SettingsContact(), settingsTabContact)
				nav(msgs.SettingsAdvanced(), settingsTabAdvanced)
			})
		} else {
			ui.Column(c).Gap(4).Children(func() {
				nav(msgs.SettingsGeneral(), settingsTabAppearance)
				nav(msgs.SettingsConnection(), settingsTabConnection)
				nav(msgs.SettingsContact(), settingsTabContact)
				nav(msgs.SettingsAdvanced(), settingsTabAdvanced)
			})
		}
	})
}

func (a *appModel) settingsMain(c *ui.Context, t *ui.Theme, narrow bool) {
	ui.Column(c).Grow(1).FillHeight().AlignItems(ui.Start).Children(func() {
		ui.Scroll(c).Grow(1).FillHeight().TrackScroll(&a.settingsScroll).Padding(8, 32, 48).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(settingsContentWidth).AlignSelf(ui.Center).AlignItems(ui.Start).Gap(8).Children(func() {
				switch a.settingsTab {
				case settingsTabAppearance:
					a.settingsAppearanceContent(c, t)
				case settingsTabConnection:
					a.settingsConnectionContent(c, t)
				case settingsTabContact:
					a.settingsContactContent(c, t)
				default:
					a.settingsAdvancedContent(c, t)
				}
			})
		})
	})
}

func preferenceGroup(c *ui.Context, t *ui.Theme, children func()) {
	ui.Column(c).FillWidth().Padding(0, 16).Draw(func(p *ui.Painter, r ui.Rect) {
		paintPreferenceFace(p, r, 12, 2, t.Background, t.Border)
	}).Children(children)
}

func preferenceSection(c *ui.Context, t *ui.Theme, title string, children func()) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Column(c).FillWidth().Height(44).Padding(0, 16).Justify(ui.End).Children(func() {
			ui.Row(c).FillWidth().Height(28).AlignItems(ui.Center).Children(func() {
				ui.Text(c, title).FontSize(typeMeta).FontWeight(400).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			})
		})
		preferenceGroup(c, t, children)
	})
}

func settingsPreferenceRow(c *ui.Context, t *ui.Theme, label, description string, control func(), last ...bool) {
	width, _ := c.Size()
	row := ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Padding(14, 0)
	if width < preferenceBreakpoint {
		row = ui.Column(c).FillWidth().AlignItems(ui.Stretch).Gap(12).Padding(14, 0)
	}
	if len(last) == 0 || !last[0] {
		row.BorderWidth(0, 0, 1, 0).BorderColor(t.Border).BorderStyle(ui.BorderDashed)
	}
	row.Children(func() {
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, label+":").FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			if description != "" {
				ui.Text(c, description).FontSize(typeMeta).FontWeight(400).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
		})
		controls := ui.Column(c).MinWidth(0).Gap(8)
		if width >= preferenceBreakpoint {
			controls.MaxWidthPercent(66).Shrink(1).AlignItems(ui.End)
		} else {
			controls.FillWidth()
		}
		controls.Children(control)
	})
}

func (a *appModel) settingsAppearanceContent(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	a.ensureLanguageMode()
	pageHeading(c, msgs.SettingsGeneral())
	preferenceSection(c, t, msgs.SettingsAppearance(), func() {
		c.Key("appearance-preference")
		settingsPreferenceRow(c, t, msgs.AppearanceMode(), "", func() {
			selected := []string{appearanceSystem, appearanceLight, appearanceDark}[appearanceModeIndex(a.appearanceMode)]
			c.Key("appearance-mode")
			appearance := preferenceSelect(c, &selected, []selectOption{{appearanceSystem, msgs.AppearanceSystem()}, {appearanceLight, msgs.AppearanceLight()}, {appearanceDark, msgs.AppearanceDark()}}, msgs.AppearanceMode())
			if appearance.Changed() {
				a.setAppearanceMode(selected)
			}
		})
		if a.appearanceStatus != appearanceStatusNone {
			ui.Text(c, appearanceStatusLabel(msgs, a.appearanceStatus)).FontSize(typeAction).TextColor(t.Success)
		}
		if a.appearanceError != "" {
			ui.Text(c, appearanceStatusLabel(msgs, a.appearanceError)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Danger)
			secondaryButton(c, msgs.SettingsRetrySave()).OnClick(func() { a.saveAppearance() })
		}
		if len(a.appearanceThemes.Themes) > 0 || a.appearanceThemeIDs != (appearanceThemeIDs{}) {
			for _, kind := range []string{appearanceLight, appearanceDark} {
				label, builtinName, builtinID := msgs.ThemeLight(), msgs.ThemePaper(), "paper"
				if kind == appearanceDark {
					label, builtinName, builtinID = msgs.ThemeDark(), msgs.ThemeMoss(), "night"
				}
				selected := a.appearanceThemeIDs.forKind(kind)
				options := []selectOption{{Value: builtinID, Label: builtinName}}
				for _, theme := range a.appearanceThemes.Themes {
					if theme.Kind == kind {
						options = append(options, selectOption{Value: theme.ID, Label: theme.Name + " (" + theme.ID + ")"})
					}
				}
				resolved := a.resolveAppearanceTheme(kind == appearanceDark)
				if resolved.Unavailable {
					options = append(options, selectOption{Value: selected, Label: msgs.ThemeUnavailable(selected, builtinName)})
				}
				ui.Column(c).Key("theme-row-" + kind).FillWidth().Children(func() {
					settingsPreferenceRow(c, t, label, "", func() {
						c.Key("theme-" + kind)
						if preferenceSelect(c, &selected, options, label).Changed() {
							a.setAppearanceTheme(kind, selected)
						}
					})
				})
				for _, warning := range resolved.Warnings {
					ui.Text(c, msgs.ThemeContrastWarning(selected, warning.Foreground, warning.Background, fmt.Sprintf("%.2f", warning.Ratio), fmt.Sprintf("%.1f", warning.Minimum))).
						FontSize(typeMeta).LineHeight(1.5).TextColor(t.Warning)
				}
			}
		}
		c.Key("local-themes-preference")
		settingsPreferenceRow(c, t, msgs.SettingsLocalThemes(), msgs.ThemeImportHint(), func() {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				busy := a.themeImport != nil && a.themeImport.Busy
				importButton := secondaryButton(c, msgs.ThemeImportAction()).Label(msgs.ThemeImportAction()).Disabled(busy || a.shutdownStarted || a.dataRecoveryBlocksChanges()).OnClick(a.importAppearanceTheme)
				if a.themeImport != nil && a.themeImport.focus {
					importButton.Focus()
					a.themeImport.focus = false
				}
				secondaryButton(c, msgs.ThemeReload()).Label(msgs.ThemeReload()).OnClick(a.reloadAppearanceThemes)
			})
		}, true)
		if state := a.themeImport; state != nil {
			if state.Busy {
				ui.Text(c, msgs.ThemeImportBusy()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
			if state.Problem != nil {
				ui.Text(c, msgs.ThemeImportFailed()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
				problem := state.Problem
				ui.Text(c, msgs.ThemeFileError(safeDiagnostic(problem.File), safeDiagnostic(problem.Field), safeDiagnostic(problem.Message))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			} else if state.ImportedName != "" {
				ui.Text(c, msgs.ThemeImportSucceeded(safeDiagnostic(state.ImportedName))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Success)
			}
		}

		for _, problem := range a.appearanceThemes.Errors {
			ui.Text(c, msgs.ThemeFileError(problem.File, problem.Field, problem.Message)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
		}
	})
	preferenceSection(c, t, msgs.SettingsLanguage(), func() {
		c.Key("language-preference")
		settingsPreferenceRow(c, t, msgs.SettingsLanguage(), msgs.LanguageHint(), func() {
			options := []selectOption{
				{Value: "", Label: msgs.LanguageSystem(languageSelfName(msgs, c.Locale()))},
				{Value: "zh-Hans", Label: msgs.LanguageChinese()},
				{Value: "en", Label: msgs.LanguageEnglish()},
			}
			selected := []string{"", "zh-Hans", "en"}[languageModeIndex(a.languageMode)]
			c.Key("settings-language")
			if preferenceSelect(c, &selected, options, msgs.SettingsLanguage()).Changed() {
				a.setLanguageMode(selected)
			}
		}, true)
	})
	a.settingsDataRecoveryContent(c, t)
}

type dataRecoveryState struct {
	choosing, inFlight bool
	token              uint64
	action             string
	filename           string
	copy               *storeRecoveryCopy
	target             *storeRecoveryTarget
	canonical          string
	viewGeneration     uint64
	owner              *contact
	output             string
	result             storeRecoveryResult
	problem            string
	notice             string
	details            bool
	focus              string
}

func (a *appModel) ensureDataRecovery() *dataRecoveryState {
	if a.dataRecovery == nil {
		a.dataRecovery = &dataRecoveryState{}
	}
	return a.dataRecovery
}

func (a *appModel) dataRecoveryCurrent(s *dataRecoveryState, token uint64, generation uint64, owner *contact, path string) bool {
	return !a.shutdownStarted && a.dataRecovery == s && s.token == token && a.folderChoiceGeneration == generation && a.settingsOpen && a.settingsTab == settingsTabAppearance && a.selectedContact() == owner && a.storePath == path
}

func (a *appModel) dataRecoveryPreviewCurrent(s *dataRecoveryState) bool {
	return s != nil && s.copy != nil && s.target != nil && a.dataRecoveryCurrent(s, s.token, s.viewGeneration, s.owner, s.canonical)
}

func (a *appModel) dataRecoveryBlocksChanges() bool {
	s := a.dataRecovery
	return s != nil && (s.choosing || s.inFlight || a.dataRecoveryPreviewCurrent(s) || s.result.Outcome == storeRecoveryVerifiedInstalled || s.result.Outcome == storeRecoveryInstallUnconfirmed)
}

func (a *appModel) dataRecoveryBlocked(restore bool) string {
	for _, editor := range a.settingsProfiles {
		if editor.Busy {
			return "busy"
		}
	}
	if a.contactProfile != nil && a.contactProfile.Busy {
		return "busy"
	}
	if a.contextFilePickers > 0 || a.shutdownStarted || a.connectionSaveInFlight || a.connectionApplying || a.connectionTesting || a.modelOpActive || a.contactHistoryInFlight > 0 || a.resultInFlight > 0 || a.submissionInFlight > 0 || a.metadataInFlight > 0 || a.issueToolReleaseInFlight > 0 || a.activeTaskCount() > 0 || a.themeImport != nil && a.themeImport.Busy || a.connectionForm != nil && a.connectionForm.Busy {
		return "busy"
	}
	if a.dataRecovery != nil && (a.dataRecovery.choosing || a.dataRecovery.inFlight || a.dataRecovery.result.Outcome == storeRecoveryVerifiedInstalled || a.dataRecovery.result.Outcome == storeRecoveryInstallUnconfirmed) {
		return "busy"
	}
	if !restore {
		if a.store == nil || a.localSaveErr != "" || a.storePath == "" {
			return "saved"
		}
		return ""
	}
	if a.store != nil || a.storeInitAllowed || a.rt != nil || a.runtimeStage != startupStageLocalStore || a.storePath == "" {
		return "unavailable"
	}
	if a.localSaveErr != "" || len(a.contacts) != 1 || a.issueFormOpen || a.issueGoal != "" || a.issueRequirements != "" || len(a.issueRequirementDrafts)+len(a.issueCompletionDrafts)+len(a.issueSupplementDrafts) > 0 || a.modelForm != nil && a.modelForm.dirty() || a.connectionForm != nil && a.connectionForm.Dirty() {
		return "unsaved"
	}
	ct := a.selectedContact()
	if ct.Unavailable == "" || ct.SessionID != "" || ct.Draft != "" || len(ct.Messages)+len(ct.History)+len(ct.Issues)+len(ct.PreviousSessions) > 0 || ct.pendingSend != nil || len(ct.Context.items) > 0 || ct.Context.formOpen || ct.Context.formName != "" || ct.Context.formText != "" || ct.Context.formPath != "" {
		return "unsaved"
	}
	for id, editor := range a.settingsProfiles {
		if owner := a.contactByID(id); owner == nil || !profileDraftEqual(editor.Draft, contactProfileDraft(owner)) {
			return "unsaved"
		}
	}
	if a.contactProfile != nil {
		if owner := a.contactByID(a.contactProfileID); owner == nil || !profileDraftEqual(a.contactProfile.Draft, contactProfileDraft(owner)) {
			return "unsaved"
		}
	}
	return ""
}

func dataRecoveryBlockedText(msgs locales.Messages, reason string) string {
	switch reason {
	case "busy":
		return msgs.RecoveryDataBusy()
	case "saved":
		return msgs.RecoveryCopyNeedsSavedData()
	case "unsaved":
		return msgs.RecoveryRestoreUnsaved()
	default:
		return msgs.RecoveryRestoreNeedsUnavailable()
	}
}

func (a *appModel) openDataRecovery() {
	a.openSettingsTab(settingsTabAppearance, settingsSourceConnection)
	a.ensureDataRecovery().focus = "restore"
}

// Completion accounting belongs to the admitted operation, even after its view
// has gone away. A chooser alone has not admitted any filesystem operation.
func (a *appModel) finishDataRecoveryIO(s *dataRecoveryState) {
	s.inFlight = false
	if a.shutdownStarted {
		a.finishShutdown()
	}
}

func (a *appModel) chooseDataRecovery(restore bool) {
	if !a.settingsOpen || a.settingsTab != settingsTabAppearance || a.dataRecoveryBlocksChanges() {
		return
	}
	s := a.ensureDataRecovery()
	if reason := a.dataRecoveryBlocked(restore); reason != "" {
		s.notice, s.problem = "blocked:"+reason, ""
		return
	}
	s.token++
	s.choosing = true
	s.action = "copy"
	s.filename = "Nagi-workbench-recovery-" + time.Now().Format("20060102-150405") + ".json"
	msgs := locales.In(a.uiLocale)
	pick := a.chooseFolder
	opts := mygo.OpenDialogOptions{Parent: a.win, Directory: true, Title: msgs.RecoveryCopyFolderTitle(), ButtonLabel: msgs.SaveRecoveryCopyAction()}
	if restore {
		s.action = "restore"
		pick = a.chooseRecoveryFile
		opts = mygo.OpenDialogOptions{Parent: a.win, Title: msgs.RecoveryChooseFileTitle(), ButtonLabel: msgs.RestorePausedAction(), Filters: []mygo.FileFilter{{Name: msgs.RecoveryCopyFilter(), Extensions: []string{"json"}}}}
	}
	if pick == nil {
		pick = mygo.Dialog.Open
	}
	token, generation, owner, path := s.token, a.folderChoiceGeneration, a.selectedContact(), a.storePath
	go func() {
		opts.DefaultPath, _ = os.UserHomeDir()
		paths, err := pick(opts)
		a.updateUI(func() {
			s.choosing = false
			if !a.dataRecoveryCurrent(s, token, generation, owner, path) {
				return
			}
			s.focus = s.action
			if err != nil {
				s.notice, s.problem = "choose-failed", a.connectionSafeMessage(err)
				return
			}
			if len(paths) == 0 {
				return
			}
			if reason := a.dataRecoveryBlocked(restore); reason != "" {
				s.notice, s.problem = "blocked:"+reason, ""
				return
			}
			s.inFlight = true
			s.copy, s.target = nil, nil
			s.result = storeRecoveryResult{}
			selected := paths[0]
			if !restore {
				destination := filepath.Join(selected, s.filename)
				go func() {
					captured, err := inspectStoreRecoveryCopy(path)
					output := ""
					if err == nil {
						output, err = writeStoreRecoveryCopy(captured, destination)
					}
					a.updateUI(func() {
						defer a.finishDataRecoveryIO(s)
						s.output, s.problem = output, a.connectionSafeMessage(err)
						s.notice = "copy-saved"
						if err != nil {
							s.notice = "copy-failed"
						}
						if a.dataRecoveryCurrent(s, token, generation, owner, path) {
							s.focus = "copy"
						}
					})
				}()
				return
			}
			go func() {
				captured, copyErr := inspectStoreRecoveryCopy(selected)
				err := copyErr
				var target *storeRecoveryTarget
				if err == nil {
					target, err = inspectStoreRecoveryTarget(path)
				}
				a.updateUI(func() {
					defer a.finishDataRecoveryIO(s)
					if !a.dataRecoveryCurrent(s, token, generation, owner, path) {
						return
					}
					s.notice, s.problem = "inspect-failed", a.connectionSafeMessage(err)
					if copyErr != nil {
						s.notice = "copy-unusable"
					}
					s.focus = "restore"
					if err == nil {
						s.copy, s.target, s.canonical = captured, target, path
						s.viewGeneration, s.owner = generation, owner
						s.notice, s.focus = "", "confirm"
						s.result = storeRecoveryResult{}
					}
				})
			}()
		})
	}()
}

func (a *appModel) confirmDataRecovery() {
	s := a.dataRecovery
	if !a.dataRecoveryPreviewCurrent(s) || s.inFlight || s.choosing {
		return
	}
	if reason := a.dataRecoveryBlocked(true); reason != "" {
		s.notice, s.problem = "blocked:"+reason, ""
		return
	}
	captured, target := s.copy, s.target
	token, generation, owner, path := s.token, s.viewGeneration, s.owner, s.canonical
	s.inFlight = true
	s.problem, s.notice = "", ""
	go func() {
		result, err := restoreStoreRecoveryCopy(captured, target)
		a.updateUI(func() {
			defer a.finishDataRecoveryIO(s)
			s.result, s.problem = result, a.connectionSafeMessage(err)
			s.copy, s.target = nil, nil
			if a.dataRecoveryCurrent(s, token, generation, owner, path) {
				s.focus = "restore"
				if result.Outcome == storeRecoveryVerifiedInstalled || result.Outcome == storeRecoveryInstallUnconfirmed {
					s.focus = "quit"
				}
			}
		})
	}()
}

func (a *appModel) settingsDataRecoveryContent(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	s := a.ensureDataRecovery()
	preferenceSection(c, t, msgs.RecoveryDataTitle(), func() {
		ui.Column(c).FillWidth().Padding(14, 0).Gap(8).Children(func() {
			ui.Text(c, msgs.RecoveryDataIncluded()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
			ui.Text(c, msgs.RecoveryDataExcluded()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			ui.Row(c).Wrap().Gap(8).Children(func() {
				for _, restore := range []bool{false, true} {
					label, key := msgs.SaveRecoveryCopyAction(), "copy"
					if restore {
						label, key = msgs.RestorePausedAction(), "restore"
					}
					button := secondaryButton(c, label).Label(label).Disabled(a.dataRecoveryBlocksChanges() || a.dataRecoveryBlocked(restore) != "").OnClick(func() { a.chooseDataRecovery(restore) })
					if s.focus == key {
						button.Focus().ScrollIntoView()
						s.focus = ""
					}
				}
			})
			if reason := a.dataRecoveryBlocked(a.store == nil); reason != "" && s.result.Outcome != storeRecoveryVerifiedInstalled && s.result.Outcome != storeRecoveryInstallUnconfirmed {
				ui.Text(c, dataRecoveryBlockedText(msgs, reason)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
			if s.choosing || s.inFlight {
				ui.Text(c, msgs.RecoveryDataWorking()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
			}
			if a.dataRecoveryPreviewCurrent(s) {
				ui.Text(c, msgs.RecoveryPreviewTitle()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
				ui.Text(c, safeDiagnostic(s.copy.Path)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
				ui.Text(c, msgs.RecoveryPreviewCounts(s.copy.Contacts, s.copy.Issues)).FontSize(typeBody).TextColor(t.Text)
				ui.Text(c, msgs.RecoveryFileModified(s.copy.ModifiedAt.Format("2006-01-02 15:04:05 MST"))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				ui.Text(c, msgs.RecoveryPreviewWarning()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
				ui.Text(c, msgs.RecoveryPauseWarning()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
				ui.Row(c).Wrap().Gap(8).Children(func() {
					confirm := secondaryButton(c, msgs.RecoveryConfirmAction()).Label(msgs.RecoveryConfirmAction()).Disabled(s.inFlight || a.dataRecoveryBlocked(true) != "").OnClick(a.confirmDataRecovery)
					if s.focus == "confirm" {
						confirm.Focus().ScrollIntoView()
						s.focus = ""
					}
					secondaryButton(c, msgs.RecoveryCancelAction()).Label(msgs.RecoveryCancelAction()).Disabled(s.inFlight).OnClick(func() { s.copy, s.target = nil, nil; s.focus = "restore" })
				})
			}
			var summary string
			switch s.result.Outcome {
			case storeRecoveryNotInstalled:
				summary = msgs.RecoveryNotInstalled()
			case storeRecoveryVerifiedInstalled:
				summary = msgs.RecoveryInstalled()
			case storeRecoveryInstallUnconfirmed:
				summary = msgs.RecoveryUnconfirmed()
			default:
				switch s.notice {
				case "copy-saved":
					summary = msgs.RecoveryCopySaved(safeDiagnostic(s.output))
				case "copy-failed":
					summary = msgs.RecoveryCopyFailed()
				case "copy-unusable":
					summary = msgs.RecoveryCopyUnusable()
				case "choose-failed", "inspect-failed":
					summary = msgs.RecoveryInspectFailed()
				default:
					if strings.HasPrefix(s.notice, "blocked:") {
						summary = dataRecoveryBlockedText(msgs, strings.TrimPrefix(s.notice, "blocked:"))
					}
				}
			}
			if summary != "" {
				color := t.Warning
				if s.notice == "copy-saved" && s.result.Outcome == "" {
					color = t.Success
				}
				ui.Text(c, summary).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(color)
			}
			reveal := func(path, label string) {
				if path == "" {
					return
				}
				if path == s.result.ArchivePath {
					ui.Text(c, msgs.RecoveryArchiveLocation(safeDiagnostic(path))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				}
				secondaryButton(c, label).Label(label).OnClick(func() {
					fn := a.revealRecoveryFile
					if fn == nil {
						fn = requestRevealIssueResultWithSystem
					}
					token, generation, owner, canonical := s.token, a.folderChoiceGeneration, a.selectedContact(), a.storePath
					go func() {
						err := fn(path)
						a.updateUI(func() {
							if err != nil && a.dataRecoveryCurrent(s, token, generation, owner, canonical) {
								s.problem = strings.TrimSpace(s.problem + "\n" + a.connectionSafeMessage(err))
							}
						})
					}()
				})
			}
			reveal(s.output, msgs.RecoveryShowCopy())
			if s.result.OutputPath != "" {
				ui.Text(c, msgs.RecoveryDataLocation(safeDiagnostic(s.result.OutputPath))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
			reveal(s.result.ArchivePath, msgs.RecoveryShowArchive())
			if s.problem != "" {
				label := msgs.DetailsAction()
				if s.details {
					label = msgs.HideDetailsAction()
				}
				secondaryButton(c, label).Label(label).OnClick(func() { s.details = !s.details })
				if s.details {
					markdownReader(c, safeDiagnostic(s.problem), markdownReaderOptions{PlainText: true, CopyLabel: msgs.CopyErrorDetailsLabel()}).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				}
			}
			if s.result.Outcome == storeRecoveryVerifiedInstalled || s.result.Outcome == storeRecoveryInstallUnconfirmed {
				quit := secondaryButton(c, msgs.RecoveryQuitAction()).Label(msgs.RecoveryQuitAction()).OnClick(a.beginShutdown)
				if s.focus == "quit" {
					quit.Focus().ScrollIntoView()
					s.focus = ""
				}
			}
		})
	})
}

func (a *appModel) settingsConnectionContent(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	form := a.connectionForm
	ui.Column(c).FillWidth().AlignItems(ui.Start).Gap(8).Children(func() {
		ui.Text(c, msgs.SettingsConnection()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
		ui.Text(c, msgs.ModelDefaultScope()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
		if a.connectionApplied() && a.connectionEffective != nil {
			ui.Column(c).FillWidth().Gap(4).Children(func() {
				ui.Text(c, msgs.ConnectionCurrent(connectionCurrentSummary(a.connectionEffective))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
				if form.Error != "" {
					ui.Text(c, msgs.ConnectionKeptOnFailure()).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.Warning)
				}
			})
		} else {
			ui.Text(c, msgs.ConnectionNoneApplied()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		}
		if form.Dirty() {
			ui.Text(c, msgs.ConnectionUnsavedForm()).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.Warning)
		}
		if a.connectionSavedPending() {
			ui.Text(c, msgs.ConnectionSavedAwaitingApply()).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.Warning)
		}
		form.ReloadBlocked = a.connectionReloadBlocked()
		form.SavedPending = a.connectionSavedPending()
		actions := a.connectionActions()
		actions.Discard = func() { form.ResetDraft() }
		form.view(c, t, actions)
		ui.Divider(c)
		ui.Text(c, msgs.ModelConversationScope()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
		if a.modelPanelKind == "conversation" && a.modelPanelSessionID == a.selectedContact().SessionID {
			a.modelPanel(c, t)
		} else {
			secondaryButton(c, msgs.ModelSessionTitle()).Disabled(a.modelOpActive).
				OnClick(func() {
					a.prepareConversationModel()
				})
		}
	})
}

func (a *appModel) settingsContactContent(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	editor := a.settingsContactEditor()
	if editor == nil {
		ui.Text(c, msgs.SettingsContact()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
		ui.Text(c, msgs.SettingsNoContactToEdit()).FontSize(14).TextColor(t.Warning)
		return
	}
	options := make([]selectOption, 0, len(a.contacts))
	for _, contact := range a.contacts {
		if contact != nil {
			options = append(options, selectOption{Value: contact.ID, Label: a.settingsContactChoice(msgs, contact)})
		}
	}
	picker := preferenceSelect(c, &a.settingsContactID, options, msgs.EditContactLabel())
	if picker.Changed() {
		a.folderChoiceGeneration++
		a.ensureSettingsProfileEditor(a.settingsContactID)
		a.settingsScroll.Y = 0
	}
	var ct *contact
	ct = a.contactByID(a.settingsContactID)
	editor = a.settingsContactEditor()
	ui.Text(c, msgs.SettingsContactTitle(a.settingsContactChoice(msgs, ct))).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
	if !profileDraftEqual(editor.Draft, editor.initial) {
		ui.Text(c, msgs.SettingsUnsavedChanges()).FontSize(typeMeta).LineHeight(1.5).TextColor(t.Warning)
	}
	editor.BuiltinTools = append([]runtimeBuiltinTool(nil), a.runtimeBuiltinTools...)
	editor.BuiltinToolsLoaded = a.runtimeBuiltinToolsLoaded
	editor.BuiltinToolsLoading = a.runtimeBuiltinToolsLoading
	editor.BuiltinToolsError = a.runtimeBuiltinToolsErr
	editor.BuiltinToolsSupported = a.connectionManaged
	editor.CanRetryBuiltinTools = editor.BuiltinToolsSupported && a.rt != nil && a.runtimeStatus == runtimeStateReady
	if !editor.BuiltinToolsSupported {
		if a.connectionLegacy {
			editor.BuiltinToolsError = msgs.SettingsBuiltinLegacyError()
		} else {
			editor.BuiltinToolsError = msgs.SettingsBuiltinManagedError()
		}
	} else if !editor.BuiltinToolsLoaded && !editor.BuiltinToolsLoading && editor.BuiltinToolsError == "" && !editor.CanRetryBuiltinTools {
		editor.BuiltinToolsError = msgs.SettingsBuiltinNotReady()
	}
	editor.view(c, t, contactProfileActions{
		Save:              func(draft profileDraft) { a.saveContactProfileDraftFor(ct.ID, draft, false) },
		SaveAndNew:        func(draft profileDraft) { a.saveContactProfileDraftFor(ct.ID, draft, true) },
		Archive:           func() { a.archiveContact(ct.ID) },
		Restore:           func() { a.restoreContact(ct.ID) },
		Discard:           func() { editor.ResetDraft() },
		RetryBuiltinTools: func() { a.retryBuiltinTools() },
		ChooseFolder:      func() { a.chooseContactWorkFolder(ct.ID, editor) },
	})
}

func (a *appModel) chooseContactWorkFolder(contactID string, editor *contactProfileEditor) {
	ct := a.contactByID(contactID)
	if ct == nil || editor == nil || editor.Busy || ct.Archived || a.shutdownStarted || !a.settingsOpen || a.settingsTab != settingsTabContact || a.settingsContactID != contactID || a.profileEditorFor(contactID) != editor {
		return
	}
	a.folderChoiceGeneration++
	generation := a.folderChoiceGeneration
	msgs := locales.In(a.uiLocale)
	opts := mygo.OpenDialogOptions{Parent: a.win, Directory: true,
		Title: msgs.WorkFolderChooseTitle(), ButtonLabel: msgs.WorkFolderChoose()}
	draft, effective := editor.Draft.Workspace, a.effectiveContactWorkspace(ct)
	pick := a.chooseFolder
	editor.Busy = true
	go func() {
		opts.DefaultPath = workFolderStart(draft, effective)
		path, err := chooseWorkFolder(pick, opts)
		a.updateUI(func() {
			editor.Busy = false
			if a.shutdownStarted || generation != a.folderChoiceGeneration || a.contactByID(contactID) != ct || ct.Archived || a.profileEditorFor(contactID) != editor || !a.settingsOpen || a.settingsTab != settingsTabContact || a.settingsContactID != contactID {
				return
			}
			editor.focusFolder = true
			if err != nil {
				editor.folderError = err.Error()
			} else if path != "" {
				editor.Draft.Workspace = path
				delete(editor.Errors, "Workspace")
				editor.folderError = ""
			}
		})
	}()
}

func (a *appModel) settingsContactChoice(msgs locales.Messages, contact *contact) string {
	if contact == nil {
		return ""
	}
	sameName := 0
	for _, other := range a.contacts {
		if other != nil && other.Name == contact.Name {
			sameName++
		}
	}
	if sameName <= 1 {
		return contact.Name
	}
	for index, other := range a.contacts {
		if other == contact {
			return msgs.ContactChoiceDuplicate(contact.Name, index+1)
		}
	}
	return contact.Name
}

func connectionCurrentSummary(config *connectionConfig) string {
	if config == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if strings.TrimSpace(config.Model) != "" {
		parts = append(parts, config.Model)
	}
	if strings.TrimSpace(config.Endpoint) != "" {
		parts = append(parts, config.Endpoint)
	}
	if strings.TrimSpace(config.API) != "" {
		parts = append(parts, config.API)
	}
	return strings.Join(parts, ", ")
}

func (a *appModel) connectionSavedPending() bool {
	if a.connectionSaved == nil {
		return false
	}
	if !a.connectionApplied() || a.connectionEffective == nil {
		return true
	}
	return *a.connectionSaved != *a.connectionEffective
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (a *appModel) settingsAdvancedContent(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	ui.Text(c, msgs.SettingsAdvanced()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
	ui.Text(c, msgs.SettingsAdvancedIntro()).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	ui.Text(c, msgs.SettingsStartupSection()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.Text)
	label := msgs.SettingsShowRuntimeDetails()
	if a.startupDetailsOpen {
		label = msgs.SettingsHideRuntimeDetails()
	}
	secondaryButton(c, label).OnClick(func() { a.startupDetailsOpen = !a.startupDetailsOpen })
	if a.startupDetailsOpen {
		a.runtimePanel(c, t, a.selectedContact())
	}
	secondaryButton(c, msgs.SettingsOpenConnection()).OnClick(func() { a.selectSettingsTab(settingsTabConnection) })
}

func (a *appModel) auxScrollFor(kind string) *ui.ScrollState {
	switch kind {
	case "sources":
		return &a.sourcesScroll
	case "model":
		return &a.modelScroll
	case "task-context":
		return &a.taskContextScroll
	case "issue-result":
		return &a.issueResultScroll
	default:
		return &a.sourcesScroll
	}
}
