package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

const (
	settingsTabAppearance = "appearance"
	settingsTabConnection = "connection"
	settingsTabAdvanced   = "advanced"

	settingsSourceRail          = "rail"
	settingsSourceProfile       = "profile"
	settingsSourceConnection    = "connection"
	settingsSourceConfiguration = "configuration"
	settingsSourceCommand       = "command"
	settingsSourceComposer      = "composer"
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

func (a *appModel) openSettingsTab(tab, source string) {
	if source != settingsSourceLink {
		a.settingsReturnLink = markdownLinkTarget{}
		a.linkReturnTarget = markdownLinkTarget{}
		a.linkReturnSeen = false
		a.markdownLinkFocus = markdownLinkTarget{}
	}
	switch tab {
	case settingsTabAppearance, settingsTabConnection, settingsTabAdvanced:
	default:
		tab = settingsTabAppearance
	}
	wasOpen := a.settingsOpen
	if !wasOpen || tab != a.settingsTab {
		a.folderChoiceGeneration++
	}
	if !wasOpen {
		a.settingsReturnFocus = source
		a.settingsFocusBack = true
	}
	a.resetComposerOverlays()
	a.settingsOpen = true
	a.settingsTab = tab
	if tab == settingsTabAppearance {
		a.languageModeLoaded = false
	}
	if tab == settingsTabAdvanced {
		a.startupDetailsOpen = a.runtimeStatus == runtimeStateFailed || (!wasOpen && source == settingsSourceConfiguration)
	}
	a.settingsScroll.Y = 0
	if !wasOpen {
		a.settingsFocusTab = ""
	} else {
		a.settingsFocusTab = tab
	}
	a.ensureConnectionForm()
	if a.connectionForm != nil {
		a.connectionForm.FocusField = ""
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
	if !a.settingsOpen {
		return
	}
	a.folderChoiceGeneration++
	a.settingsOpen = false
	a.settingsFocusBack = false
	a.settingsFocusTab = ""
	switch a.settingsReturnFocus {
	case settingsSourceProfile:
		a.focusDetailClose = true
	case settingsSourceConnection:
		a.focusConnectionEntry = true
	case settingsSourceConfiguration:
		a.focusConfiguration = true
	case settingsSourceComposer:
		a.focusComposer = true
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
		case a.aux == "task-context":
			a.focusAuxBack = true
		default:
			a.focusComposer = true
		}
	default:
		a.focusSettings = true
	}
	a.settingsReturnFocus = ""
}

func (a *appModel) connectionApplied() bool {
	return a.pi != nil && a.runtimeStatus == runtimeStateReady && a.connectionEffective != nil
}

func (a *appModel) settingsLandingTab() string {
	if a.connectionConfigurationBlocked() {
		return settingsTabConnection
	}
	return settingsTabAppearance
}

func (a *appModel) settingsPage(c *ui.Context, base *ui.Theme) {
	if a.settingsTab == "" {
		a.settingsTab = a.settingsLandingTab()
	}
	a.ensureConnectionForm()
	t := theme.SettingsTheme(base)
	ui.Themed(c, &t).Fill().Background(t.Background).Children(func() {
		if a.layoutWidth() < int(preferenceBreakpoint) {
			ui.Column(c).Fill().Children(func() {
				a.settingsSidebar(c, &t, true)
				a.settingsMain(c, &t, true)
			})
			return
		}
		ui.Row(c).Fill().Children(func() {
			a.settingsSidebar(c, &t, false)
			a.settingsMain(c, &t, false)
		})
	})
}

var settingsConnectionIcon = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M12 22C6.47715 22 2 17.5228 2 12C2 6.47715 6.47715 2 12 2C17.5228 2 22 6.47715 22 12C22 17.5228 17.5228 22 12 22ZM9.71002 19.6674C8.74743 17.6259 8.15732 15.3742 8.02731 13H4.06189C4.458 16.1765 6.71639 18.7747 9.71002 19.6674ZM10.0307 13C10.1811 15.4388 10.8778 17.7297 12 19.752C13.1222 17.7297 13.8189 15.4388 13.9693 13H10.0307ZM19.9381 13H15.9727C15.8427 15.3742 15.2526 17.6259 14.29 19.6674C17.2836 18.7747 19.542 16.1765 19.9381 13ZM4.06189 11H8.02731C8.15732 8.62577 8.74743 6.37407 9.71002 4.33256C6.71639 5.22533 4.458 7.8235 4.06189 11ZM10.0307 11H13.9693C13.8189 8.56122 13.1222 6.27025 12 4.24799C10.8778 6.27025 10.1811 8.56122 10.0307 11ZM14.29 4.33256C15.2526 6.37407 15.8427 8.62577 15.9727 11H19.9381C19.542 7.8235 17.2836 5.22533 14.29 4.33256Z"/></svg>`))

func (a *appModel) settingsSidebar(c *ui.Context, t *ui.Theme, narrow bool) {
	msgs := locales.In(c.Locale())
	column := ui.Column(c).MinWidth(0).Shrink(0).Background(t.Surface)
	if narrow {
		column.FillWidth()
	} else {
		column.FillHeight().Width(settingsSidebarWidth)
	}
	navPalette := a.workTheme
	navPalette.NavHover = t.Surface.Mix(t.Text, .06)
	navPalette.NavSelected = t.Surface.Mix(t.Text, .10)
	column.Children(func() {
		if !narrow {
			ui.Row(c).FillWidth().Height(max(float32(40), c.TitleBar().Height)).Shrink(0).DragWindow()
		}
		ui.Toolbar(c, func() {
			back := secondaryButton(c, "").Label(msgs.SettingsBackAccessible()).Padding(8, 12).Gap(8).TextColor(t.Text)
			back.Children(func() {
				ui.Icon(c, navBackIcon).Size(16, 16).TextColor(t.TextMuted)
				ui.Text(c, msgs.SettingsBack()).FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction).TextColor(t.Text)
			})
			back.OnClick(func() { a.closeSettings() })
			if a.settingsFocusBack {
				back.Focus()
				a.settingsFocusBack = false
			}
		}).Label(msgs.SettingsNavigationLabel()).FillWidth().Padding(12)
		ui.Text(c, msgs.SettingsTitle()).FontSize(typeLabel).FontWeight(600).FixedLineHeight(lineLabel).TextColor(t.Text).Padding(4, 16)
		nav := func(label, tab string) {
			selected := a.settingsTab == tab
			weight, ink := 400, t.TextMuted
			if selected {
				weight, ink = 500, t.Text
			}
			button := navigationControl(c, ui.ButtonBase(c).Label(label).Selected(selected), navPalette, selected).
				FontSize(typeNavigation).FontWeight(weight).FixedLineHeight(lineNavigation).
				Padding(8, 12).Gap(8).Justify(ui.Start).TextColor(ink)
			if !narrow {
				button.FillWidth()
			}
			button.Children(func() {
				glyph := glyphSettings4Line
				switch tab {
				case settingsTabConnection:
					glyph = settingsConnectionIcon
				case settingsTabAdvanced:
					glyph = glyphMoreLine
				}
				ui.Box(c).Size(16, 16).AlignItems(ui.Center).Justify(ui.Center).Children(func() { ui.Icon(c, glyph).Size(14, 14) })
				ui.Text(c, label).FontSize(typeNavigation).FontWeight(weight).FixedLineHeight(lineNavigation).TextColor(ink)
			})
			button.OnClick(func() { a.selectSettingsTab(tab) })
			if a.settingsFocusTab == tab {
				button.Focus()
				a.settingsFocusTab = ""
			}
		}
		if narrow {
			ui.Row(c).FillWidth().Padding(0, 8).Gap(2).Wrap().Children(func() {
				nav(msgs.SettingsGeneral(), settingsTabAppearance)
				nav(msgs.SettingsConnection(), settingsTabConnection)
				nav(msgs.SettingsAdvanced(), settingsTabAdvanced)
			})
		} else {
			ui.Column(c).FillWidth().Padding(0, 8).Gap(2).Children(func() {
				nav(msgs.SettingsGeneral(), settingsTabAppearance)
				nav(msgs.SettingsConnection(), settingsTabConnection)
				nav(msgs.SettingsAdvanced(), settingsTabAdvanced)
			})
		}
	})
}

func (a *appModel) settingsMain(c *ui.Context, t *ui.Theme, narrow bool) {
	msgs := locales.In(c.Locale())
	title := msgs.SettingsAdvanced()
	switch a.settingsTab {
	case settingsTabAppearance:
		title = msgs.SettingsGeneral()
	case settingsTabConnection:
		title = msgs.SettingsConnection()
	}
	main := ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).AlignSelf(ui.Stretch).BorderColor(t.Border).Clip()
	if narrow {
		main.BorderWidth(1, 0, 0, 0)
	} else {
		main.BorderWidth(0, 0, 0, 1)
	}
	main.Children(func() {
		ui.Row(c).FillWidth().MaxWidth(settingsContentWidth).AlignSelf(ui.Center).Height(max(float32(48), c.TitleBar().Height)).Shrink(0).Padding(0, 32).AlignItems(ui.Center).DragWindow().Children(func() {
			pageHeading(c, title)
		})
		ui.Scroll(c).FillWidth().MinWidth(0).Grow(1).MinHeight(0).TrackScroll(&a.settingsScroll).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(settingsContentWidth).MinWidth(0).AlignSelf(ui.Center).Padding(12, 32, 32).Gap(20).Children(func() {
				switch a.settingsTab {
				case settingsTabAppearance:
					a.settingsAppearanceContent(c, t)
				case settingsTabConnection:
					a.settingsConnectionContent(c, t)
				default:
					a.settingsAdvancedContent(c, t)
				}
			})
		})
	})
}

func (a *appModel) settingsAppearanceContent(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	a.ensureLanguageMode()
	preferenceSection(c, t, msgs.RailContactsTitle(), func() {
		c.Key("pandorabox-visibility")
		settingsPreferenceRow(c, t, msgs.SettingsPandoraBoxVisibility(), msgs.SettingsPandoraBoxVisibilityHint(), func() {
			selected := 0
			if a.hideBuiltinArchitect {
				selected = 1
			}
			theme.PreferenceSegmentedSelection(c, &selected, msgs.SettingsPandoraBoxVisibility(), !a.canChangeBuiltinArchitectVisibility(), false,
				msgs.SettingsPandoraBoxShow(), msgs.SettingsPandoraBoxHide()).
				OnChange(func() { a.setBuiltinArchitectHidden(selected == 1) })
		}, true)
	})
	preferenceSection(c, t, msgs.SettingsAppearance(), func() {
		c.Key("appearance-preference")
		settingsPreferenceRow(c, t, msgs.AppearanceMode(), "", func() {
			modes := [...]string{appearanceSystem, appearanceLight, appearanceDark}
			selected := appearanceModeIndex(a.appearanceMode)
			c.Key("appearance-mode")
			selection := segmentedSelection(c, &selected, msgs.AppearanceSystem(), msgs.AppearanceLight(), msgs.AppearanceDark()).Label(msgs.AppearanceMode())
			selection.OnChange(func() { a.setAppearanceMode(modes[selected]) })
		})
		if a.appearanceStatus != appearanceStatusNone {
			ui.Text(c, appearanceStatusLabel(msgs, a.appearanceStatus)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Success)
		}
		if a.appearanceError != "" {
			ui.Text(c, appearanceStatusLabel(msgs, a.appearanceError)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
			secondaryButton(c, msgs.SettingsRetrySave()).AlignSelf(ui.Start).OnClick(func() { a.saveAppearance() })
		}
		if len(a.appearanceThemes.Themes) > 0 || a.appearanceThemeIDs != (appearanceThemeIDs{}) {
			for _, kind := range []string{appearanceLight, appearanceDark} {
				if a.appearanceMode != "" && a.appearanceMode != appearanceSystem && a.appearanceMode != kind {
					continue
				}
				label, builtinName, builtinID := msgs.ThemeLight(), msgs.ThemePaper(), "paper"
				if kind == appearanceDark {
					label, builtinName, builtinID = msgs.ThemeDark(), msgs.ThemeMoss(), "night"
				}
				selected := a.appearanceThemeIDs.ForKind(kind)
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
				ui.Text(c, msgs.ThemeImportFailed()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
				problem := state.Problem
				ui.Text(c, msgs.ThemeFileError(safeDiagnostic(problem.File), safeDiagnostic(problem.Field), safeDiagnostic(problem.Message))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			} else if state.ImportedName != "" {
				ui.Text(c, msgs.ThemeImportSucceeded(safeDiagnostic(state.ImportedName))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Success)
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
	scopeOpen          bool
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
	productBackup := !restore && a.productHost() != nil
	for _, editor := range a.profileEditors {
		if editor.Busy {
			return "busy"
		}
	}
	if a.contextFilePickers > 0 || a.shutdownStarted || a.connectionSaveInFlight || a.connectionApplying || a.connectionTesting || a.contactHistoryInFlight > 0 || a.submissionInFlight > 0 || a.themeImport != nil && a.themeImport.Busy || a.connectionForm != nil && a.connectionForm.Busy {
		return "busy"
	}
	if !productBackup && a.activeTaskCount() > 0 {
		return "busy"
	}
	if a.dataRecovery != nil && (a.dataRecovery.choosing || a.dataRecovery.inFlight || a.dataRecovery.result.Outcome == storeRecoveryVerifiedInstalled || a.dataRecovery.result.Outcome == storeRecoveryInstallUnconfirmed) {
		return "busy"
	}
	if !restore {
		if productBackup {
			if a.productActivityClient() == nil || a.localSaveErr != "" {
				return "saved"
			}
			return ""
		}
		if a.store == nil || a.localSaveErr != "" || a.storePath == "" {
			return "saved"
		}
		return ""
	}
	if a.store != nil || a.storeInitAllowed || a.pi != nil || a.runtimeStage != startupStageLocalStore || a.storePath == "" {
		return "unavailable"
	}
	if a.localSaveErr != "" || len(a.contacts) != 1 || a.connectionForm != nil && a.connectionForm.Dirty() {
		return "unsaved"
	}
	ct := a.selectedContact()
	if ct.Unavailable == "" || ct.SessionID != "" || ct.Draft != "" || len(ct.Messages)+len(ct.History)+len(ct.Activities)+len(ct.PreviousPiBindings) > 0 || ct.pendingSend != nil || len(ct.Context.Items) > 0 || ct.Context.FormOpen || ct.Context.FormName != "" || ct.Context.FormText != "" || ct.Context.FormPath != "" {
		return "unsaved"
	}
	for id, editor := range a.profileEditors {
		if owner := a.contactByID(id); owner == nil || !profileDraftEqual(editor.Draft, contactProfileDraft(owner)) {
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
				host := a.productHost()
				go func() {
					var output string
					var err error
					if host != nil {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						output, err = host.product.BackupProfile(ctx, destination)
					} else {
						var captured *storeRecoveryCopy
						captured, err = inspectStoreRecoveryCopy(path)
						if err == nil {
							output, err = writeStoreRecoveryCopy(captured, destination)
						}
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
		settingsPreferenceRow(c, t, msgs.RecoveryBackupLabel(), msgs.RecoveryBackupHint(), func() {
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
				label := msgs.RecoveryBackupDetails()
				if s.scopeOpen {
					label = msgs.HideDetailsAction()
				}
				linkButton(c, label).OnClick(func() { s.scopeOpen = !s.scopeOpen })
			})
		}, true)
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			if s.scopeOpen {
				ui.Text(c, msgs.RecoveryDataIncluded()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				ui.Text(c, msgs.RecoveryDataExcluded()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
			if reason := a.dataRecoveryBlocked(a.store == nil && a.productHost() == nil); reason != "" && s.result.Outcome != storeRecoveryVerifiedInstalled && s.result.Outcome != storeRecoveryInstallUnconfirmed {
				ui.Text(c, dataRecoveryBlockedText(msgs, reason)).FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
			}
			if s.choosing || s.inFlight {
				ui.Text(c, msgs.RecoveryDataWorking()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
			if a.dataRecoveryPreviewCurrent(s) {
				ui.Text(c, msgs.RecoveryPreviewTitle()).FontSize(typeLabel).FontWeight(500).FixedLineHeight(lineLabel).TextColor(t.Text)
				ui.Text(c, safeDiagnostic(s.copy.Path)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Text)
				ui.Text(c, msgs.RecoveryPreviewCounts(s.copy.Contacts, s.copy.Issues)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Text)
				ui.Text(c, msgs.RecoveryFileModified(s.copy.ModifiedAt.Format("2006-01-02 15:04:05 MST"))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				ui.Text(c, msgs.RecoveryPreviewWarning()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
				ui.Text(c, msgs.RecoveryPauseWarning()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
				ui.Row(c).Wrap().Gap(8).Children(func() {
					confirm := primaryButton(c, msgs.RecoveryConfirmAction()).Label(msgs.RecoveryConfirmAction()).Disabled(s.inFlight || a.dataRecoveryBlocked(true) != "").OnClick(a.confirmDataRecovery)
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
				ui.Text(c, summary).FontSize(typeMeta).LineHeight(1.5).TextColor(color)
			}
			reveal := func(path, label string) {
				if path == "" {
					return
				}
				if path == s.result.ArchivePath {
					ui.Text(c, msgs.RecoveryArchiveLocation(safeDiagnostic(path))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				}
				secondaryButton(c, label).Label(label).AlignSelf(ui.Start).OnClick(func() {
					fn := a.revealRecoveryFile
					if fn == nil {
						fn = requestRevealActivityResultWithSystem
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
				linkButton(c, label).AlignSelf(ui.Start).OnClick(func() { s.details = !s.details })
				if s.details {
					markdownReader(c, safeDiagnostic(s.problem), markdownReaderOptions{PlainText: true, CopyLabel: msgs.CopyErrorDetailsLabel()}).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				}
			}
			if s.result.Outcome == storeRecoveryVerifiedInstalled || s.result.Outcome == storeRecoveryInstallUnconfirmed {
				quit := secondaryButton(c, msgs.RecoveryQuitAction()).Label(msgs.RecoveryQuitAction()).AlignSelf(ui.Start).OnClick(a.beginShutdown)
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
	form.CurrentSummary = ""
	if a.connectionApplied() {
		form.CurrentSummary = a.redactConnectionText(connectionCurrentSummary(msgs, a.connectionEffective))
	}
	form.ReloadBlocked = a.connectionReloadBlocked()
	form.SavedPending = a.connectionSavedPending()
	actions := a.connectionActions()
	actions.Discard = form.ResetDraft
	form.View(c, t, actions, a.workTheme.InputBorder)
}

func connectionCurrentSummary(msgs locales.Messages, config *connectionConfig) string {
	if config == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if strings.TrimSpace(config.Model) != "" {
		parts = append(parts, config.Model)
	}
	switch config.API {
	case connectionAPIChatCompletions:
		parts = append(parts, msgs.ProtocolChatCompletionsLabel())
	case connectionAPIResponses:
		parts = append(parts, msgs.ProtocolResponsesLabel())
	}
	if strings.TrimSpace(config.Endpoint) != "" {
		parts = append(parts, config.Endpoint)
	}
	return strings.Join(parts, " · ")
}

func (a *appModel) connectionSavedPending() bool {
	if a.runtimeStatus == runtimeStateExited {
		return a.connectionSaved != nil
	}
	if a.productHost() != nil {
		return a.productConnectionPending()
	}
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
	preferenceGroup(c, t, func() {
		settingsPreferenceRow(c, t, msgs.SettingsStartupSection(), a.runtimeStatusText(msgs), func() {
			ui.Row(c).Wrap().Gap(8).Children(func() {
				label := msgs.SettingsShowRuntimeDetails()
				if a.startupDetailsOpen {
					label = msgs.SettingsHideRuntimeDetails()
				}
				secondaryButton(c, label).OnClick(func() { a.startupDetailsOpen = !a.startupDetailsOpen })
				if !a.connectionApplied() {
					secondaryButton(c, msgs.SettingsOpenConnection()).OnClick(func() { a.selectSettingsTab(settingsTabConnection) })
				}
			})
		}, true)
		if a.startupDetailsOpen {
			ui.Column(c).FillWidth().MinWidth(0).AlignItems(ui.Start).Gap(8).Children(func() {
				a.runtimePanel(c, t, a.selectedContact())
			})
		}
	})
}
