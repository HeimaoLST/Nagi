package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/HeimaoLST/Nagi/locales"
)

// Message typography is supplied at the view call site so the reader's
// parser, clipboard and file behavior stay untouched (#187 owns those files).
var messageMarkdownTheme = &markdown.Theme{FontSize: typeBody, LineHeight: 1.5}

const (
	composerPanelContext = "task-context"
)

// Small, deliberately understated composer glyphs. MyGo's Icon inherits the
// button's text color, so these stay in the same semantic color path as UI text.

func messageReaderOptions(c *ui.Context, plain bool) markdownReaderOptions {
	return markdownReaderOptions{PlainText: plain, CopyLabel: locales.In(c.Locale()).CopyMessageLabel(), Theme: messageMarkdownTheme}
}

func (a *appModel) view(c *ui.Context) {
	a.uiLocale = c.Locale()
	if form := a.connectionForm; form != nil && form.Error == connStatusSavedStoreFailed && a.store != nil && a.localSaveErr == "" {
		form.Error, form.ErrorDetail = "", ""
		form.RecoveryDetailsOpen = false
	}
	a.syncDetailPageMode()
	if !a.settingsOpen {
		a.linkReturnSeen = false
	}
	// Context.begin resets this to the system theme on every pass, so this is
	// the live system appearance even while an explicit mode is shown.
	a.systemDark = c.Theme().Dark
	a.workTheme = a.resolveAppearanceTheme(resolveAppearanceDark(a.appearanceMode, a.systemDark)).Palette
	t := a.workTheme.Theme
	c.SetTheme(t)
	c.OnShortcut(ui.Cmd, ui.KeyComma, func() {
		if !a.settingsOpen {
			source := settingsSourceCommand
			if c.Focused(a.composerHandle) {
				source = settingsSourceComposer
			} else if !a.captureLinkSettingsSource(c, &source) {
				source = a.settingsSourceForVisiblePage()
			}
			a.openSettingsTab(settingsTabAppearance, source)
		}
	})
	if !a.settingsOpen {
		c.OnShortcut(0, ui.KeyEscape, func() { a.escapeTopLayer() })
	}
	ui.Column(c).Fill().LinearGradient(ui.LinearGradient{From: a.workTheme.ChromeFrom, To: a.workTheme.ChromeTo, Angle: 180}).Children(func() {
		// The reference chrome shares the controls' band on macOS. Full pages,
		// narrow rails and platforms with right-side controls reserve it.
		titleBarHeight := c.TitleBar().Height
		if a.settingsOpen || c.TitleBar().Right == 0 && !a.mainPageActive() && !a.detailPageActive() && !a.railCollapsed && !a.avatarRailActive() {
			titleBarHeight = 0
		}
		ui.Row(c).FillWidth().Height(titleBarHeight).Shrink(0).DragWindow()
		ui.Box(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			if a.settingsOpen {
				a.settingsPage(c, t)
				return
			}
			if a.mainPageActive() {
				a.mainPage(c, t)
			} else {
				ui.Row(c).Fill().Children(func() {
					a.layoutBody(c, t)
				})
			}
			a.finishMarkdownLinkReturn()
		})
	})
}

func (a *appModel) escapeTopLayer() {
	if a.aux != "" {
		a.closeAux()
		return
	}
	if a.activeDetail() != "" {
		a.closeDetail()
	}
}

func (a *appModel) captureLinkSettingsSource(c *ui.Context, source *string) bool {
	link := ui.FocusedValue(c, &a.markdownLinkFocus)
	if link == (markdownLinkTarget{}) {
		return false
	}
	a.settingsReturnLink = link
	*source = settingsSourceLink
	return true
}

func (a *appModel) settingsSourceForVisiblePage() string {
	switch {
	case a.aux != "":
		return settingsSourceAux
	case a.detailIs(detailProfile):
		return settingsSourceProfile
	default:
		return settingsSourceCommand
	}
}

func (a *appModel) openSettingsFromContext(c *ui.Context, tab, source string) {
	a.captureLinkSettingsSource(c, &source)
	a.openSettingsTab(tab, source)
}

func (a *appModel) armActiveSurfaceFallback() {
	switch {
	case a.aux == "sources":
		a.focusSourcesBack = true
	case a.detailIs(detailProfile):
		a.focusProfileEdit = true
	default:
		a.focusComposer = true
	}
}

func (a *appModel) readerOptions(options markdownReaderOptions, scope, base string) markdownReaderOptions {
	options.OpenLocalFile = func(dest string) error { return a.openMarkdownFile(base, dest) }
	if !options.PlainText && (options.Theme == nil || options.Theme.Link == (markdown.Color{})) {
		var theme markdown.Theme
		if options.Theme != nil {
			theme = *options.Theme
		}
		theme.Link = a.workTheme.Link
		options.Theme = &theme
	}
	if !options.PlainText && scope != "" {
		options.LinkScope = scope
		options.LinkFocus = &a.markdownLinkFocus
		options.LinkReturn = &a.linkReturnTarget
		options.LinkReturnSeen = &a.linkReturnSeen
	}
	return options
}

func messageLinkScope(contactID string, m *message) string {
	if m == nil {
		return ""
	}
	var identity string
	switch {
	case m.Key != "":
		identity = m.Key
	case m.TaskID != "" || m.MessageID != "" || m.ItemID != "":
		identity = m.TaskID + "/" + m.MessageID + "/" + m.ItemID
	default:
		identity = fmt.Sprintf("%d", m.ID)
	}
	return "contact:" + contactID + "/message:" + identity
}

func historyLinkScope(contactID, document, entryID string) string {
	if entryID == "" {
		return ""
	}
	return "contact:" + contactID + "/history:" + document + "/entry:" + entryID
}

func (a *appModel) finishMarkdownLinkReturn() {
	if a.linkReturnTarget == (markdownLinkTarget{}) {
		return
	}
	if a.linkReturnSeen {
		a.linkReturnTarget = markdownLinkTarget{}
		return
	}
	a.markdownLinkFocus = markdownLinkTarget{}
	a.linkReturnTarget = markdownLinkTarget{}
}

// ---------- rail ----------

func (a *appModel) rail(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	ui.Column(c).FillWidth().FillHeight().Padding(8).Gap(8).Background(ui.Transparent).Children(func() {
		ui.Row(c).FillWidth().Height(40).Shrink(0).DragWindow().AlignItems(ui.Center).Children(func() {
			ui.Spacer(c)
			ui.Toolbar(c, func() {
				hide := iconControlFace(c, appTooltip(c, ui.Button(c, "").Label(msgs.RailHide()), msgs.RailHideContactsTooltip()), iconChrome, false)
				hide.Children(func() {
					controlIcon(c, railHideIcon, iconChrome).TextColor(t.TextMuted)
				})
				hide.OnClick(func() { a.toggleRail() })
				if a.focusRail {
					hide.Focus()
					a.focusRail = false
				}
			}).Label(msgs.RailWindowActions()).Padding(0)
		})
		ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
			ui.Text(c, msgs.RailContactsTitle()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
			ui.Spacer(c)
			ui.Toolbar(c, func() {
				add := iconControlFace(c, appTooltip(c, ui.Button(c, "").Label(msgs.AvatarAddContact()), msgs.AvatarAddContact()), iconChrome, false)
				add.Children(func() {
					controlIcon(c, railAddIcon, iconChrome).TextColor(t.TextMuted)
				})
				add.OnClick(func() { a.addDefaultContact() })
			}).Label(msgs.RailContactListActions()).Padding(0)
		})
		ui.SearchField(c, &a.contactSearch).Label(msgs.SearchContacts()).FontSize(typeAction).Placeholder(msgs.RailContactSearchPlaceholder())
		visible := a.visibleContactIndices()
		if len(visible) == 0 {
			ui.Text(c, msgs.RailNoContactsMatch()).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.TextMuted)
			ui.Spacer(c).Grow(1)
		} else {
			ui.Scroll(c).Grow(1).Children(func() {
				for _, i := range visible {
					a.railRow(c, t, i)
				}
			})
		}
		ui.Toolbar(c, func() {
			settingsLabel := locales.In(c.Locale()).SettingsTitle()
			settings := appTooltip(c, ui.Button(c, "").Label(settingsLabel), locales.In(c.Locale()).SettingsOpenTooltip()).
				FillWidth().MinHeight(32).Height(32).Padding(4, 8).Gap(8).Justify(ui.Start)
			settings.Children(func() {
				controlIcon(c, railSettingsIcon, iconChrome).TextColor(t.TextMuted)
				ui.Text(c, settingsLabel).FontSize(typeAction).TextColor(t.Text)
			})
			settings.OnClick(func() { a.openSettingsTab(a.settingsLandingTab(), settingsSourceRail) })
			if a.focusSettings {
				settings.Focus()
				a.focusSettings = false
			}
		}).Label(locales.In(c.Locale()).SettingsRailActions()).Padding(0).FillWidth()
	})
}

func (a *appModel) avatarRail(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Width(avatarRailWidth).Shrink(0).FillHeight().Padding(4).Gap(4).Background(ui.Transparent).Children(func() {
		addLabel := locales.In(c.Locale()).AvatarAddContact()
		add := appTooltip(c, ui.Button(c, "").Label(addLabel).
			MinWidth(32).MinHeight(32).Width(32).Height(32).Padding(0).Justify(ui.Center), addLabel)
		add.Children(func() { controlIcon(c, railAddIcon, iconChrome).TextColor(t.TextMuted) })
		add.OnClick(func() { a.addDefaultContact() })
		visible := a.visibleContactIndices()
		if len(visible) == 0 {
			ui.Spacer(c).Grow(1)
		} else {
			ui.Scroll(c).Grow(1).Children(func() {
				for _, i := range visible {
					a.avatarRailRow(c, t, i)
				}
			})
		}
		ui.Toolbar(c, func() {
			settingsLabel := locales.In(c.Locale()).SettingsTitle()
			settings := iconControlFace(c, appTooltip(c, ui.Button(c, "").Label(settingsLabel),
				locales.In(c.Locale()).SettingsOpenTooltip()), iconChrome, false)
			settings.Children(func() { controlIcon(c, railSettingsIcon, iconChrome).TextColor(t.TextMuted) })
			settings.OnClick(func() { a.openSettingsTab(a.settingsLandingTab(), settingsSourceRail) })
			if a.focusSettings {
				settings.Focus()
				a.focusSettings = false
			}
		}).Label(locales.In(c.Locale()).SettingsRailActions()).Padding(0)
	})
}

func (a *appModel) avatarRailRow(c *ui.Context, t *ui.Theme, i int) {
	ct := a.contacts[i]
	msgs := locales.In(c.Locale())
	status := a.headerStatusLabel(ct, msgs)
	label := msgs.AvatarContactLabel(ct.Name)
	tip := label
	if status != "" {
		tip += "\n" + status
	}
	row := ui.ButtonBase(c).Key(ct.ID).Label(label).Description(status).
		Selected(i == a.selected).Width(48).Height(44).Padding(4).Radius(8).
		AlignItems(ui.Center).Justify(ui.Start).Background(ui.Transparent).BorderWidth(0).TextColor(t.Text)
	if i == a.selected {
		row.Background(t.Selection).TextColor(t.Text)
	}
	row = appTooltip(c, row, tip)
	row.OnClick(func() { a.selectContact(i) })
	row.Children(func() {
		contactAvatar(c, t, ct.Name, 24)
		if marker, color := a.avatarRailMarker(ct); marker != "" {
			ui.Text(c, marker).
				FontSize(typeMeta).FontWeight(500).TextColor(color).
				Background(t.Surface).Padding(1, 3).Radius(4).SingleLine().
				Absolute().Right(0).Bottom(0).IgnoreInput(true)
		}
	})
}

func (a *appModel) avatarRailMarker(ct *contact) (string, ui.Color) {
	switch ct.Status {
	case statusWorking:
		return "▶", a.workTheme.Theme.Success
	case statusAttention:
		return "!", a.workTheme.Theme.Warning
	default:
		return "", ui.Color{}
	}
}

func (a *appModel) railRow(c *ui.Context, t *ui.Theme, i int) {
	ct := a.contacts[i]
	currentMsgs := locales.In(c.Locale())
	selected := i == a.selected
	row := ui.ButtonBase(c).Key(ct.ID).Label(currentMsgs.AvatarContactLabel(ct.Name)).Selected(selected).
		Gap(8).Padding(4, 8).MinHeight(controlHit).Radius(8).AlignItems(ui.Center).Background(ui.Transparent).BorderWidth(0)
	focusFill := a.workTheme.NavHover
	if selected {
		focusFill = t.Selection
	}
	row = drawControlFace(c, row, 8, 4, false, false, selected, 0, 2, focusFill)
	row.OnClick(func() { a.selectContact(i) })
	row.Children(func() {
		contactAvatar(c, t, ct.Name, 28)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
				nameWeight := 400
				if selected {
					nameWeight = 500
				}
				ui.Text(c, ct.Name).FontSize(typeRailName).FontWeight(nameWeight).FixedLineHeight(lineNavigation).
					MaxLines(1).Grow(1).MinWidth(0).TextColor(t.Text)
				if ct.Unread > 0 {
					ui.Text(c, fmt.Sprintf("%d", ct.Unread)).FontSize(typeMeta).FixedLineHeight(lineMeta).Shrink(0).TextColor(t.Warning)
				}
			})
			msgs := locales.In(c.Locale())
			line := a.snippet(ct, msgs)
			color := t.TextMuted
			if ct.Status != statusReady {
				status := a.statusLabel(ct, msgs)
				detail := strings.TrimSpace(line)
				if detail == status {
					detail = ""
				}
				if detail == "" {
					line = status
				} else {
					line = msgs.StatusDetail(status, detail)
				}
				color = a.statusColor(t, ct.Status)
			}
			ui.Text(c, line).FontSize(typeRailSummary).FixedLineHeight(lineNavigation).MaxLines(1).TextColor(color)
		})
	})
}

func (a *appModel) contactIcon(c *ui.Context, t *ui.Theme, size float32) {
	contactAvatar(c, t, a.selectedContact().Name, size)
}

func contactAvatar(c *ui.Context, t *ui.Theme, name string, size float32) {
	name = strings.TrimSpace(name)
	initials := [2]rune{'?'}
	n := 0
	for word := range strings.FieldsSeq(name) {
		for _, r := range word {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				initials[n] = unicode.ToUpper(r)
				n++
				break
			}
		}
		if n == len(initials) {
			break
		}
	}
	background, ink := contactAvatarColors(t, name)
	ui.Box(c).Size(size, size).MinWidth(size).Shrink(0).Radius(8).Background(background).
		AlignItems(ui.Center).Justify(ui.Center).IgnoreInput(true).Children(func() {
		ui.Text(c, string(initials[:max(1, n)])).FontSize(typeMeta).FontWeight(600).FixedLineHeight(lineMeta).
			SingleLine().TextColor(ink).IgnoreInput(true)
	})
}

func (a *appModel) statusColor(t *ui.Theme, s contactStatus) ui.Color {
	switch s {
	case statusWorking:
		return t.Success
	case statusAttention:
		return t.Warning
	default:
		return t.TextMuted
	}
}

// ---------- main column ----------

func (a *appModel) headerStatusLabel(ct *contact, msgs locales.Messages) string {
	if ct != nil && (ct.Status == statusWorking || ct.Status == statusAttention) {
		return a.statusLabel(ct, msgs)
	}
	switch a.connectionViewState() {
	case connectionViewApplying:
		return msgs.ConnectionStatusApplying()
	case connectionViewConnecting:
		return msgs.ConnectionStatusConnecting()
	case connectionViewSaved:
		return msgs.ConnectionStatusSavedAwaitingApply()
	case connectionViewSetup:
		return msgs.ConnectionStatusSetupNeeded()
	case connectionViewNotConnected:
		return msgs.ConnectionStatusNotConnected()
	default:
		return a.statusLabel(ct, msgs)
	}
}

func (a *appModel) headerStatusColor(t *ui.Theme, ct *contact) ui.Color {
	if ct != nil && (ct.Status == statusWorking || ct.Status == statusAttention) {
		return a.statusColor(t, ct.Status)
	}
	switch a.connectionViewState() {
	case connectionViewSaved, connectionViewSetup, connectionViewNotConnected:
		return t.Warning
	default:
		return a.statusColor(t, ct.Status)
	}
}

func runFailureSummary(msgs locales.Messages, detail string) string {
	for _, status := range []string{"HTTP 401", "HTTP 403"} {
		if strings.HasPrefix(detail, "Provider authentication failed: "+status+". Check API_KEY and provider configuration;") {
			return msgs.RecoveryProviderDenied(status)
		}
	}
	if strings.HasPrefix(detail, "Model request failed:") {
		return msgs.RecoveryModelRequestFailed()
	}
	return msgs.RecoveryRunFailed()
}

func formattedRunFailure(detail string) bool {
	return detail == "Task failed." || strings.HasPrefix(detail, "Task failed:") ||
		strings.HasPrefix(detail, "Model request failed:") ||
		strings.HasPrefix(detail, "Provider authentication failed: HTTP 401. Check API_KEY and provider configuration;") ||
		strings.HasPrefix(detail, "Provider authentication failed: HTTP 403. Check API_KEY and provider configuration;")
}

func recoveryNotice(c *ui.Context, t *ui.Theme, summary, detail string, expanded bool, toggle, settings func(), settingsLabel string) ui.Element {
	msgs := locales.In(c.Locale())
	detail = safeDiagnostic(detail)
	var entry ui.Element
	ui.Column(c).FillWidth().MinWidth(0).Gap(6).Children(func() {
		ui.Text(c, summary).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
		ui.Row(c).Wrap().Gap(8).Children(func() {
			if settings != nil {
				entry = secondaryButton(c, settingsLabel).Label(settingsLabel).OnClick(settings)
			}
			if detail != "" && toggle != nil {
				label := msgs.DetailsAction()
				if expanded {
					label = msgs.HideDetailsAction()
				}
				secondaryButton(c, label).Label(label).OnClick(toggle)
			}
		})
		if expanded && detail != "" {
			ui.Text(c, msgs.RecoveryTechnicalDetailsHint()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			markdownReader(c, detail, markdownReaderOptions{PlainText: true, CopyLabel: msgs.CopyErrorDetailsLabel()}).
				FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		}
	})
	return entry
}

func (a *appModel) recoveryNotice(c *ui.Context, t *ui.Theme, key, summary, detail string, settings bool, disclosure ...func(bool, func())) {
	localData := key == "startup" && a.runtimeStage == startupStageLocalStore && a.productHost() == nil
	detail = a.redactConnectionText(detail)
	key = "recovery:" + key + ":" + detail
	var openSettings func()
	if settings {
		openSettings = func() { a.openSettingsTab(settingsTabConnection, settingsSourceConnection) }
	}
	entry := recoveryNotice(c, t, summary, detail, a.connectionDetailsOpen[key], func() {
		expanding := !a.connectionDetailsOpen[key]
		toggle := func() {
			if a.connectionDetailsOpen == nil {
				a.connectionDetailsOpen = map[string]bool{}
			}
			a.connectionDetailsOpen[key] = expanding
		}
		if len(disclosure) > 0 {
			disclosure[0](expanding, toggle)
		} else {
			toggle()
		}
	}, openSettings, locales.In(c.Locale()).ConnectionSettingsAction())
	if localData {
		secondaryButton(c, locales.In(c.Locale()).RestorePausedAction()).Label(locales.In(c.Locale()).RestorePausedAction()).OnClick(a.openDataRecovery)
	}
	if settings && a.focusConnectionEntry {
		entry.Focus()
		a.focusConnectionEntry = false
	}
}

func (a *appModel) runtimeRecoverySummary(msgs locales.Messages) string {
	if a.runtimeStage == startupStageLocalStore {
		if a.dataRecovery != nil {
			switch a.dataRecovery.result.Outcome {
			case storeRecoveryVerifiedInstalled:
				return msgs.RecoveryInstalled()
			case storeRecoveryInstallUnconfirmed:
				return msgs.RecoveryUnconfirmed()
			}
		}
		return msgs.RecoveryLocalDataUnavailable()
	}
	switch a.runtimeErrKind {
	case runtimeNoticeStart:
		return msgs.RecoveryRuntimeStartFailed()
	case runtimeNoticeSavedNeedsRepair:
		return msgs.ConnectionErrorSavedNeedsRepair()
	case runtimeNoticeConnectionRequired:
		return msgs.ConnectionRecoverySetupHint()
	case runtimeNoticeStoreSave:
		return msgs.RecoveryLocalSaveFailed()
	default:
		if a.runtimeStatus == runtimeStateExited {
			return msgs.ConnectionRecoveryNotConnectedHint()
		}
		return msgs.ConnectionRecoveryManagedHint()
	}
}

func (a *appModel) localSaveNotice(c *ui.Context, t *ui.Theme) {
	if a.localSaveErr == "" {
		return
	}
	a.recoveryNotice(c, t, "local-save", locales.In(c.Locale()).RecoveryLocalSaveFailed(), a.localSaveErr, false)
	secondaryButton(c, locales.In(c.Locale()).SettingsRetrySave()).Label(locales.In(c.Locale()).SettingsRetrySave()).OnClick(func() { _ = a.persist() })
}

func (a *appModel) connectionRecoveryNotice(c *ui.Context, t *ui.Theme, state connectionViewState, showRuntimeError bool) {
	msgs := locales.In(c.Locale())
	title, hint := msgs.ConnectionStatusSetupNeeded(), msgs.ConnectionRecoverySetupHint()
	switch state {
	case connectionViewSaved:
		title, hint = msgs.ConnectionStatusSavedAwaitingApply(), msgs.ConnectionRecoverySavedHint()
	case connectionViewNotConnected:
		title, hint = msgs.ConnectionStatusNotConnected(), msgs.ConnectionRecoveryNotConnectedHint()
		if a.runtimeStatus != runtimeStateExited && a.runtimeStage == "Connection configuration" && (a.connectionEffective != nil || a.connectionSaved != nil || a.connectionManaged) {
			title, hint = msgs.ConnectionRecoveryManagedTitle(), msgs.ConnectionRecoveryManagedHint()
		}
	}
	runtimeSummary := ""
	if showRuntimeError {
		runtimeSummary = a.runtimeRecoverySummary(msgs)
	}
	ui.Column(c).Padding(8, 16).Gap(3).Children(func() {
		ui.Text(c, title).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
		if hint != "" && hint != runtimeSummary {
			ui.Text(c, hint).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		}
		if showRuntimeError {
			a.recoveryNotice(c, t, "startup", runtimeSummary, a.runtimeErrorDisplay(msgs)+"\n"+a.runtimeHintDisplay(msgs), false)
		}
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			entry := secondaryButton(c, msgs.ConnectionRecoveryOpenSettings()).Label(msgs.ConnectionRecoveryOpenSettings()).MinHeight(32).OnClick(a.openConfiguration)
			if a.focusConnectionEntry && !c.Focused(a.composerHandle) {
				entry.Focus()
				a.focusConnectionEntry = false
			}
			secondaryButton(c, msgs.ConfigurationDetails()).OnClick(func() { a.openAdvancedSettings() })
		})
	})
}

func (a *appModel) main(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	msgs := locales.In(c.Locale())
	ui.Column(c).Grow(1).MinWidth(0).FillHeight().Margin(4).Background(a.workTheme.Reading).Border(1, t.Border).Radius(16).Clip().Children(func() {
		a.header(c, t)
		state := a.connectionViewState()
		showConnectionSettings := true
		if !a.demo {
			if a.localSaveErr != "" {
				ui.Column(c).Padding(8, 16).Gap(6).Children(func() {
					a.localSaveNotice(c, t)
				})
			}
			duplicateLocalSave := a.localSaveErr != "" && a.runtimeErrKind == runtimeNoticeStoreSave && a.runtimeErr == a.localSaveErr
			showRuntimeError := a.hasRuntimeError() && (!duplicateLocalSave || a.runtimeStage == startupStageLocalStore)
			switch {
			case a.runtimeStatus == runtimeStateDraining:
				ui.Text(c, msgs.RuntimeDraining()).FontSize(typeBody).FixedLineHeight(lineBody).Padding(8, 16).TextColor(t.TextMuted)
			case state == connectionViewApplying:
				ui.Column(c).Padding(8, 16).Gap(2).Children(func() {
					ui.Text(c, msgs.ConnectionStatusApplying()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
					ui.Text(c, msgs.ConnectionRecoveryApplyingHint()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
				})
			case a.connectionConfigurationBlocked():
				a.connectionRecoveryNotice(c, t, state, showRuntimeError && a.runtimeErrKind != runtimeNoticeConnectionRequired)
				showConnectionSettings = false
			case a.hasRuntimeError():
				if showRuntimeError {
					ui.Column(c).Padding(8, 16).Gap(6).Children(func() {
						a.recoveryNotice(c, t, "startup", a.runtimeRecoverySummary(msgs), a.runtimeErrorDisplay(msgs)+"\n"+a.runtimeHintDisplay(msgs), true)
						details := secondaryButton(c, msgs.ConfigurationDetails()).OnClick(func() { a.openAdvancedSettings() })
						if a.focusConfiguration {
							details.Focus()
							a.focusConfiguration = false
						}
					})
					showConnectionSettings = false
				}
			case a.runtimeStatus == runtimeStateStarting:
				ui.Column(c).Padding(8, 16).Gap(2).Children(func() {
					ui.Text(c, msgs.RuntimeConnecting()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
					ui.Text(c, msgs.ConnectionRecoveryConnectingHint()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
				})
			}
		}
		showConnectionSettings = a.connectionErrorBanner(c, t, ct, showConnectionSettings)
		if ct.LoadErr != "" {
			if !knownConnectionFailure(ct.LoadErr) {
				ui.Column(c).Padding(8, 16).Children(func() {
					a.recoveryNotice(c, t, "history:"+ct.ID, msgs.HistoryReadFailed(), ct.LoadErr, showConnectionSettings)
				})
				showConnectionSettings = false
			}
		}
		if ct.Archived {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Padding(6, 16).Background(t.SurfaceHover).Children(func() {
				ui.Text(c, msgs.ArchiveNotice()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
				ui.Spacer(c)
				secondaryButton(c, msgs.RestoreContactAction()).TextColor(t.Text).OnClick(func() { a.restoreContact(ct.ID) })
				secondaryButton(c, msgs.ContactProfileAction()).OnClick(func() { a.openContactProfile(ct.ID) })
			})
		}
		a.chat(c, t, state, showConnectionSettings)
	})
}

func (a *appModel) header(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	msgs := locales.In(c.Locale())
	ui.Row(c).Key("header").Justify(ui.Center).Children(func() {
		ui.Column(c).FillWidth().Padding(4, 6).Gap(4).Children(func() {
			ui.Row(c).FillWidth().MinHeight(32).AlignItems(ui.Center).Gap(12).Children(func() {
				ui.Column(c).MinWidth(0).Shrink(1).Gap(0).Children(func() {
					ui.Text(c, ct.Name).FontSize(typeContactTitle).FontWeight(500).FixedLineHeight(lineHeader).TextColor(t.Text).MaxLines(2)
					ui.Row(c).AlignItems(ui.Center).Gap(12).Wrap().Children(func() {
						ui.Text(c, a.headerStatusLabel(ct, msgs)).FontSize(typeHeaderMeta).FixedLineHeight(lineHeader).FontWeight(400).TextColor(a.headerStatusColor(t, ct))
						ui.Text(c, ct.Role).FontSize(typeHeaderMeta).FontWeight(400).FixedLineHeight(lineHeader).MinWidth(0).Grow(1).TextColor(t.TextMuted)
					})
				})
				if a.railCollapsed {
					ui.Spacer(c)
					a.showContactsButton(c, t)
				}
			})
		})
	})
}

// ---------- chat ----------

type chatItem struct {
	id      string
	entry   *historyEntry
	msg     *message
	working bool
}

func (a *appModel) chat(c *ui.Context, t *ui.Theme, state connectionViewState, showConnectionSettings bool) {
	ct := a.selectedContact()
	items := make([]chatItem, 0, len(ct.Messages)+len(ct.Revealed)+1)
	for _, e := range a.revealedEntries() {
		e := e
		items = append(items, chatItem{id: "h-" + e.ID, entry: &e})
	}
	for _, m := range ct.Messages {
		m := m
		key := fmt.Sprintf("m-%d", m.ID)
		if m.Key != "" {
			key = m.Key
		}
		items = append(items, chatItem{id: key, msg: m})
	}
	ct.list.Key = func(i int) any { return items[i].id }
	ct.list.FollowEnd = true
	if a.scrollTo >= 0 {
		ct.list.ScrollTo(a.scrollTo, ui.Start)
		a.scrollTo = -1
	}
	ui.Column(c).Key("chat").Grow(1).Children(func() {
		if len(items) == 0 {
			ui.Row(c).Grow(1).FillHeight().Justify(ui.Center).Children(func() {
				ui.Scroll(c).FillHeight().FillWidth().MaxWidth(conversationMaxWidth).Padding(16).Children(func() {
					a.emptyState(c, t)
				})
			})
		} else {
			ui.Row(c).Grow(1).FillHeight().Justify(ui.Center).Children(func() {
				ui.List(c, &ct.list, len(items), func(i int) {
					it := items[i]
					switch {
					case it.entry != nil:
						a.historyMessage(c, t, *it.entry, it.entry.ID == a.highlight)
					default:
						a.message(c, t, ct, it.msg)
					}
				}).Label(locales.In(c.Locale()).ConversationMessagesLabel()).FillHeight().FillWidth().MaxWidth(conversationMaxWidth).Padding(16).Gap(12)
			})
		}
		if len(items) > 0 && !ct.list.AtEnd() {
			ui.Row(c).Key("back-to-latest").Justify(ui.Center).Children(func() {
				back := secondaryButton(c, locales.In(c.Locale()).BackToLatest())
				back.OnClick(func() {
					ct.list.ScrollToEnd()
					a.focusComposer = true
				})
			})
		}
		ui.Row(c).Key("composer").Justify(ui.Center).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(conversationMaxWidth).Children(func() {
				a.composer(c, t, state, showConnectionSettings)
			})
		})
	})
}

func (a *appModel) historyMessage(c *ui.Context, t *ui.Theme, e historyEntry, highlight bool) {
	bg := ui.Transparent
	if e.From == "me" {
		bg = a.workTheme.BubbleUser
	} else if highlight {
		bg = t.Selection
	}
	if e.From == "me" {
		ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
			ui.Column(c).MaxWidth(userMessageMaxWidth).MinWidth(0).Gap(4).Children(func() {
				if e.Text != "" {
					opts := messageReaderOptions(c, true)
					opts.MessageBubble = &markdownMessageBubble{Background: bg, PaddingX: 12, PaddingY: 8, Radius: 12, ContextualCopy: true}
					markdownReader(c, e.Text, opts).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
				}
				taskContextReadOnlyView(c, a.workTheme, e.Context)
			})
		})
		return
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
		ui.Column(c).MaxWidth(conversationMaxWidth).MinWidth(0).Shrink(1).Children(func() {
			opts := a.readerOptions(messageReaderOptions(c, false), historyLinkScope(a.selectedContact().ID, "conversation", e.ID), a.selectedContact().SessionWorkingDir)
			opts.MessageBubble = &markdownMessageBubble{
				Background: bg, PaddingX: 0, PaddingY: 0, Radius: 0,
				Header: func(hc *ui.Context) {
					ui.Text(hc, a.selectedContact().Name+"  "+e.Date).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				},
			}
			markdownReader(c, e.Text, opts).FontSize(typeBody).FixedLineHeight(lineBody)
		})
	})
}

func (a *appModel) message(c *ui.Context, t *ui.Theme, ct *contact, m *message) {
	msgs := locales.In(c.Locale())
	switch m.Kind {
	case msgActivity:
		if m.ToolName != "" || m.ToolOutput != "" {
			title := toolTitleFor(m.ToolName, m.ToolArgs)
			summary := toolOutputSummary(msgs, m.ToolOutput)
			if m.Expanded {
				summary = truncate(m.ToolOutput, 2000)
			}
			ui.Column(c).Gap(2).Padding(4, 0).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, title).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.TextMuted)
					if m.ToolOutput != "" {
						label := msgs.DetailsAction()
						if m.Expanded {
							label = msgs.HideAction()
						}
						secondaryButton(c, label).Radius(999).OnClick(func() { m.Expanded = !m.Expanded })
					}
				})
				if summary != "" {
					lines := 2
					if m.Expanded {
						lines = 12
					}
					ui.Text(c, summary).FontSize(typeAction).FixedLineHeight(lineCompact).MaxLines(lines).TextColor(t.TextMuted)
				}
			})
			return
		}
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Gap(4).Children(func() {
				label := msgs.LookedUpEarlier()
				if m.Expanded {
					label = "– " + label
				} else {
					label = "+ " + label
				}
				ui.Row(c).Children(func() {
					secondaryButton(c, label).OnClick(func() { m.Expanded = !m.Expanded })
				})
				if m.Expanded {
					for _, step := range m.Steps {
						ui.Text(c, step).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.TextMuted)
					}
					if len(m.Sources) > 0 {
						ui.Row(c).Children(func() {
							button := secondaryButton(c, msgs.OpenSources()).OnClick(func() { a.openSources(m.ID) })
							if a.focusSources && a.auxMessage == m.ID {
								button.Focus()
								a.focusSources = false
							}
						})
					}
				}
			})
		})
	case msgUser:
		ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
			ui.Column(c).MaxWidth(userMessageMaxWidth).MinWidth(0).Shrink(1).Gap(2).Children(func() {
				if m.Key != "" && m.Key == a.highlight {
					ui.Text(c, msgs.LocatedInHistory()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
				}
				if m.Text != "" {
					opts := messageReaderOptions(c, true)
					opts.MessageBubble = &markdownMessageBubble{Background: a.workTheme.BubbleUser, PaddingX: 12, PaddingY: 8, Radius: 12, ContextualCopy: true}
					markdownReader(c, m.Text, opts).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
				}
				taskContextReadOnlyView(c, a.workTheme, m.Context)
			})
		})
	default:
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Column(c).MaxWidth(userMessageMaxWidth).MinWidth(0).Shrink(1).Children(func() {
				opts := a.readerOptions(markdownReaderOptions{Streaming: m.Streaming, CopyLabel: locales.In(c.Locale()).CopyMessageLabel(), Theme: messageMarkdownTheme}, messageLinkScope(ct.ID, m), ct.SessionWorkingDir)
				opts.MessageBubble = &markdownMessageBubble{
					Background: ui.Transparent, PaddingX: 0, PaddingY: 0, Radius: 0,
					Header: func(hc *ui.Context) {
						if m.Key != "" && m.Key == a.highlight {
							ui.Text(hc, msgs.LocatedInHistory()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
						}
					},
					Footer: func(fc *ui.Context) {
						if m.AlertTitle != "" {
							a.alert(fc, t, m)
						}
						if len(m.Sources) > 0 {
							ui.Row(fc).Children(func() {
								button := secondaryButton(fc, msgs.EarlierMessagesCount(len(m.Sources))).OnClick(func() { a.openSources(m.ID) })
								if a.focusSources && a.auxMessage == m.ID {
									button.Focus()
									a.focusSources = false
								}
							})
						}
					},
				}
				markdownReader(c, m.Text, opts).FontSize(typeBody).FixedLineHeight(lineBody)
			})
		})
	}
}

func (a *appModel) alert(c *ui.Context, t *ui.Theme, m *message) {
	msgs := locales.In(c.Locale())
	ui.Column(c).Padding(8).Radius(10).Background(t.SurfaceHover).Gap(4).Children(func() {
		ui.Text(c, m.AlertTitle).FontSize(typeAction).FontWeight(500).TextColor(t.Warning)
		if m.AlertDetail != "" {
			ui.Text(c, m.AlertDetail).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		}
		if m.Retry && !m.RetryDone {
			ui.Row(c).Children(func() {
				secondaryButton(c, msgs.TryAgain()).OnClick(func() { a.retry(m) })
			})
		} else if m.RetryDone {
			ui.Text(c, msgs.Retried()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		}
	})
}

func (a *appModel) emptyState(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	msgs := locales.In(c.Locale())
	ui.Column(c).Padding(8, 0).Gap(8).Children(func() {
		if !a.demo && ct.SessionID != "" && !ct.Loaded {
			ui.Text(c, msgs.HistoryNotLoadedTitle()).FontSize(typeBody).FontWeight(400).FixedLineHeight(lineBody).TextColor(t.Text)
			ui.Text(c, msgs.HistoryNotLoadedHint()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
			if !a.canReloadPiConversation(ct) {
				ui.Text(c, msgs.HistoryRuntimeNotReady()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
			}
			return
		}
		ui.Text(c, msgs.EmptyNoMessages()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
		hint := msgs.EmptyConversationStart(ct.Name)
		if a.demo {
			hint = msgs.EmptyConversationDraft(ct.Name)
		}
		ui.Text(c, hint).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	})
}

func knownConnectionFailure(text string) bool {
	// runtimeMessage prefixes typed errors as "code: message". Match the code
	// exactly rather than treating an arbitrary diagnostic containing the word
	// as a known connection failure.
	return strings.HasPrefix(strings.TrimSpace(text), "connection_mismatch:")
}

func (a *appModel) connectionSendFailureKnown(ct *contact) bool {
	if ct == nil {
		return false
	}
	return knownConnectionFailure(ct.SendError)
}

func (a *appModel) connectionErrorBanner(c *ui.Context, t *ui.Theme, ct *contact, showConnectionSettings bool) bool {
	if ct == nil {
		return showConnectionSettings
	}
	msgs := locales.In(c.Locale())
	known := knownConnectionFailure(ct.LoadErr) || knownConnectionFailure(ct.SendError)
	if !known {
		return showConnectionSettings
	}
	details := strings.TrimSpace(ct.LoadErr)
	if details == "" || details == ct.SendError {
		details = strings.TrimSpace(ct.SendError)
	} else if ct.SendError != "" {
		details = details + "\n\n" + strings.TrimSpace(ct.SendError)
	}
	ui.Column(c).Padding(8, 16).Gap(3).Background(t.SurfaceHover).Children(func() {
		a.recoveryNotice(c, t, "connection:"+ct.ID, msgs.ConnectionMismatchSummary(), details, showConnectionSettings)
	})
	return false
}

// ---------- composer ----------

func (a *appModel) composerInlineAux() bool {
	return a.aux == composerPanelContext && a.composerPanel == composerPanelContext
}

func (a *appModel) composerModelSummary(c *ui.Context, t *ui.Theme, ct *contact) {
	msgs := locales.In(c.Locale())
	head, tail := a.conversationModelDisplayParts(msgs, ct)
	appTooltip(c, ui.Row(c).Label(msgs.ConversationModelLabel()), msgs.ConversationModelTooltip(a.conversationModelSummary(msgs, ct))).MinWidth(0).MaxWidth(230).Shrink(1).Padding(8).AlignItems(ui.Center).Gap(3).Children(func() {
		ui.Text(c, msgs.ConversationBadge()).FontSize(typeMeta).FontWeight(400).FixedLineHeight(lineNavigation).
			MaxLines(1).Shrink(0).TextColor(t.TextMuted)
		ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Center).Gap(0).Children(func() {
			if head != "" {
				ui.Text(c, head).FontSize(typeAction).FontWeight(400).
					Grow(1).Shrink(1).MinWidth(0).MaxLines(1).Ellipsis("…").TextColor(t.Text)
			}
			if tail != "" {
				ui.Text(c, tail).FontSize(typeAction).FontWeight(400).
					MaxLines(1).Shrink(0).TextColor(t.Text)
			}
		})
	})
}

func (a *appModel) composerInlinePanel(c *ui.Context, t *ui.Theme) {
	if !a.composerInlineAux() {
		return
	}
	msgs := locales.In(c.Locale())
	c.OnShortcut(0, ui.KeyEscape, func() { a.closeAux() })
	ui.Column(c).FillWidth().Children(func() {
		ui.Scroll(c).Label(msgs.TaskContextDetailsScroll()).MaxHeight(300).TrackScroll(a.auxScrollFor(a.aux)).Children(func() {
			ui.Column(c).Padding(8, 0).Gap(8).Children(func() {
				a.taskContextPanel(c, t)
			})
		})
	})
}

func contactNoticeText(msgs locales.Messages, kind, detail string) string {
	switch kind {
	case "send-protocol":
		return msgs.SendNoticeProtocol()
	case "send-delivery":
		return msgs.SendNoticeDelivery()
	case "send-cancelled":
		return msgs.SendNoticeCancelled()
	case "send-previous-incomplete", "status-previous-incomplete":
		return msgs.SendNoticePreviousIncomplete()
	case "status-history-reloaded":
		return msgs.SendNoticeHistoryReloaded()
	case "send-uncertain":
		return msgs.SendNoticeUncertain()
	case "send-issue-association":
		return msgs.SendNoticeIssueAssociation()
	case "send-draft-not-cleared":
		return msgs.SendNoticeDraftNotCleared()
	case "send-delivery-unknown":
		return msgs.SendNoticeDeliveryUnknown(detail)
	case "send-contact-save":
		return msgs.SendNoticeContactSave(detail)
	case "send-cancel-failed":
		return msgs.IssueSendCancelFailed(detail)
	case "send-cancelling":
		return msgs.IssueSendCancelling()
	case "send-cancel-already":
		return msgs.IssueSendCancelAlready()
	case "send-already-finished":
		return msgs.IssueSendAlreadyFinished()
	case "send-reload-disconnected":
		return msgs.IssueSendReloadDisconnected()
	case "supplement-receipt-failed":
		return msgs.IssueSendReceiptFailed()
	case "supplement-receipt-save-failed":
		return msgs.IssueSendReceiptSaveFailed(detail)
	case "supplement-association":
		return msgs.IssueSendAssociation()
	case "waiting-runtime":
		return msgs.IssueSendWaitingRuntime(detail)
	case "supplement-delivery":
		return msgs.IssueSendSupplementDelivery(detail)
	case "auto-stop-memory":
		return msgs.IssueSendAutoStopMemory()
	case "release-tool-failed":
		return msgs.IssueSendReleaseToolFailed(detail)
	case "supplement-history-receipt-failed":
		return msgs.IssueSendHistoryReceiptFailed(detail)
	case "answer-stopping":
		return msgs.IssueSendAnswerStopping()
	case "send-accepted-cancel":
		return msgs.IssueSendAcceptedCancel()
	case "status-connection-lost":
		return msgs.SendNoticeConnectionLost()
	default:
		if text := sendBlockText(msgs, kind); text != kind {
			return text
		}
		return kind
	}
}

func (a *appModel) composer(c *ui.Context, t *ui.Theme, state connectionViewState, showConnectionSettings bool) {
	ct := a.selectedContact()
	msgs := locales.In(c.Locale())
	connected := a.runtimeAvailableForView()
	knownMismatch := a.connectionSendFailureKnown(ct)
	canReload := a.canReloadPiConversation(ct) && !knownMismatch
	unknownSummary := msgs.SendDisconnectedState()
	if knownMismatch {
		unknownSummary = msgs.ConnectionMismatchSummary()
	} else if connected {
		unknownSummary = msgs.IssueExecutionUnknownDetail()
		if canReload {
			unknownSummary = msgs.SendUnknownState()
		}
	}
	ui.Column(c).FillWidth().Padding(8, 16).Gap(4).Children(func() {
		ui.Column(c).FillWidth().MinHeight(composerMinHeight).Padding(8, 12, 0).Radius(16).Border(1, t.Border).Background(a.workTheme.Reading).Gap(4).Children(func() {
			c.Key(ct.ID)
			input := ui.TextArea(c, &ct.Draft).Bind(&a.composerHandle).Label(msgs.ComposerLabel(ct.Name)).
				FontSize(typeBody).FixedLineHeight(lineBody).Lines(2, 6).MinHeight(48).
				Background(ui.Transparent).BorderWidth(0).Padding(2).
				Placeholder(msgs.ComposerPlaceholder(ct.Name)).FillWidth().OnChange(func() {
				a.draftChanged(ct)
			}).OnTextKey(func(k ui.TextKey) bool {
				if k.Key != ui.KeyEnter || k.Mods&ui.Shift != 0 {
					return false
				}
				text := strings.TrimSpace(k.Text())
				if text == "" {
					return true
				}
				if a.send(ct, text) && a.demo {
					k.SetText("")
				}
				return true
			})
			if a.focusComposer {
				input.Focus()
				a.focusComposer = false
			}
			if a.focusConnectionEntry && c.Focused(a.composerHandle) {
				a.focusConnectionEntry = false
			}
			contextCount := ct.Context.count()
			contextLabel := msgs.TaskContextCount(contextCount)
			sendDisabled := strings.TrimSpace(ct.Draft) == "" || ct.Status == statusWorking
			sendReason := ""
			if !a.demo {
				sendReason = a.canSend(ct)
				sendDisabled = strings.TrimSpace(ct.Draft) == "" || sendReason != ""
			}
			ui.Row(c).FillWidth().Height(composerToolbarHeight).Shrink(0).Padding(8, 8).AlignItems(ui.Center).Gap(8).Role(ui.RoleGroup).Label(msgs.ComposerTools()).Draw(func(p *ui.Painter, r ui.Rect) {
				for x := r.X; x < r.X+r.W; x += 6 {
					p.Line(x, r.Y+0.5, min(x+3, r.X+r.W), r.Y+0.5, 1, t.Border.Alpha(0.6))
				}
			}).Children(func() {
				ui.Toolbar(c, func() {
					contextButton := iconControlFace(c, appTooltip(c, ui.Button(c, "").Label(contextLabel), contextLabel), iconContext, false)
					contextButton.Children(func() {
						controlIcon(c, composerAttachIcon, iconContext).TextColor(t.TextMuted)
					})
					if contextCount > 0 {
						ui.Text(c, fmt.Sprint(contextCount)).FontSize(typeMeta).FixedLineHeight(lineNavigation).TextColor(t.TextMuted)
					}
					contextButton.OnClick(func() { a.openComposerTaskContext() })
					if a.composerFocusContext {
						contextButton.Focus()
						a.composerFocusContext = false
					}
				}).Label(msgs.ComposerTools()).Shrink(0).Padding(0)
				ui.Spacer(c)
				a.composerModelSummary(c, t, ct)
				// Send/Stop are outside the Toolbar: the main action must never
				// disappear into a toolbar overflow menu at narrow widths.
				if !a.demo && ct.ActiveTaskID != "" {
					stop := appTooltip(c, secondaryButton(c, "").Label(msgs.StopConversation()).FontWeight(500), msgs.StopConversationTooltip()).
						Shrink(0).Height(32).Padding(4, 8).TextColor(t.Warning)
					stop.Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
							ui.Icon(c, composerStopIcon).Width(16).Height(16)
							ui.Text(c, msgs.StopConversation()).FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction).TextColor(t.Warning)
						})
					})
					stop.OnClick(func() { a.cancelSelected() })
				} else {
					send := appTooltip(c, ui.PrimaryButton(c, "").Label(msgs.SendAction()), msgs.SendTooltip()).
						Width(controlHit).Height(controlHit).Shrink(0).Padding(0).Radius(controlHit / 2).Disabled(sendDisabled)
					send.Children(func() {
						ui.Icon(c, composerSendIcon).Width(16).Height(16)
					})
					if send.Clicked() {
						a.sendCurrent()
					}
				}
			})
			if !a.demo {
				sendErrorVisible := ct.SendError != "" && !knownMismatch
				connectionLostDetail := ct.SendState == sendUnknown && ct.StatusDetail != "" && ct.StatusDetailKind == "status-connection-lost"
				unknownNotice := ct.SendState == sendUnknown && (sendErrorVisible || connectionLostDetail)
				switch sendReason {
				case blockShutdown, blockArchivedSend:
					ui.Text(c, sendBlockText(msgs, sendReason)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
				case blockRuntimeNotReady:
					if state == connectionViewHealthy && !unknownNotice {
						ui.Text(c, sendBlockText(msgs, sendReason)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
					}
				case "", blockApplying:
				default:
					if strings.TrimSpace(ct.Draft) != "" {
						ui.Text(c, sendBlockText(msgs, sendReason)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
					}
				}
				switch {
				case ct.SendState == sendSubmitting:
					ui.Text(c, msgs.SendingState()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				case ct.SendState == sendAccepted && ct.Status != statusWorking:
					ui.Text(c, msgs.SendAcceptedState()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				case ct.SendState == sendUnknown && !unknownNotice:
					ui.Text(c, unknownSummary).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
				}
				if sendErrorVisible || connectionLostDetail {
					var key, detail string
					if sendErrorVisible {
						key = "send:" + ct.ID + ":" + ct.SessionID + ":" + ct.LastTaskID + ":" + ct.ActiveTaskID
						detail = ct.SendError
						if connectionLostDetail && ct.StatusDetail != ct.SendError {
							detail += "\n\n" + ct.StatusDetail
						}
					} else {
						key = "status:" + ct.ID + ":" + ct.SessionID + ":" + ct.LastTaskID
						detail = ct.StatusDetail
					}
					summary := unknownSummary
					if !unknownNotice {
						summary = msgs.RecoveryRequestFailed()
						if ct.SendErrorKind != "" {
							summary = contactNoticeText(msgs, ct.SendErrorKind, "")
						}
					}
					a.recoveryNotice(c, t, key, summary, detail, showConnectionSettings)
					showConnectionSettings = false
				}
				if canReload && (ct.SendState == sendUnknown || ct.SendError != "") {
					secondaryButton(c, msgs.ReloadAction()).Label(msgs.ReloadAction()).OnClick(func() { a.reloadSelected() })
				}
				if ct.StatusDetail != "" && !connectionLostDetail {
					statusText := ct.StatusDetail
					if ct.StatusDetailKind != "" {
						statusText = contactNoticeText(locales.In(c.Locale()), ct.StatusDetailKind, "")
					}
					if ct.StatusDetailKind == "" && formattedRunFailure(ct.StatusDetail) {
						a.recoveryNotice(c, t, "task:"+ct.ID+":"+ct.SessionID+":"+ct.LastTaskID+":"+ct.ActiveTaskID, runFailureSummary(msgs, ct.StatusDetail), ct.StatusDetail, showConnectionSettings)
					} else {
						ui.Text(c, statusText).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
					}
				}
			}
		})
		if a.composerInlineAux() {
			a.composerInlinePanel(c, t)
		}
	})
}

func speakerLabel(msgs locales.Messages, c *contact, e historyEntry) string {
	if e.From == "me" {
		return msgs.SpeakerYou()
	}
	return c.Name
}

// ---------- main pages ----------

func (a *appModel) sourcesPanel(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	msgs := locales.In(c.Locale())
	entries := a.sourceEntries(a.auxMessage)
	ui.Text(c, msgs.SourcesSummary(len(entries), ct.Name)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
	for _, e := range entries {
		e := e
		ui.Column(c).FillWidth().MinWidth(0).Padding(8).Radius(8).Background(t.SurfaceHover).Gap(4).Children(func() {
			ui.Text(c, e.Date+". "+speakerLabel(msgs, ct, e)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			if e.From == "me" {
				markdownReader(c, e.Text, markdownReaderOptions{PlainText: true, CopyLabel: msgs.CopySourceMessageLabel(), Theme: messageMarkdownTheme}).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
			} else {
				markdownReader(c, e.Text, a.readerOptions(markdownReaderOptions{CopyLabel: msgs.CopySourceMessageLabel(), Theme: messageMarkdownTheme}, historyLinkScope(ct.ID, "sources", e.ID), ct.SessionWorkingDir)).FontSize(typeBody).FixedLineHeight(lineBody)
			}
			ui.Row(c).Children(func() {
				secondaryButton(c, msgs.ViewInConversation()).OnClick(func() { a.viewInConversation(e.ID) })
			})
		})
	}
}

func (a *appModel) openConfiguration() {
	a.ensureConnectionForm()
	a.openSettingsTab(settingsTabConnection, settingsSourceConnection)
}

func (a *appModel) runtimePanel(c *ui.Context, t *ui.Theme, ct *contact) {
	msgs := locales.In(c.Locale())
	localStore := a.runtimeStage == startupStageLocalStore
	if localStore {
		ui.Text(c, msgs.RuntimeLocalStoreUnavailable()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Danger)
		ui.Text(c, msgs.RuntimeNotStartedLocalStore()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
	} else {
		status := msgs.RuntimeStatusLine(a.runtimeStatusText(msgs))
		ui.Text(c, status).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
	}
	if a.hasRuntimeError() {
		ui.Text(c, msgs.RuntimeStageLabel(a.runtimeStageDisplay(msgs), a.runtimeErrorDisplay(msgs))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Danger)
	}
	if a.runtimeHintDisplay(msgs) != "" {
		ui.Text(c, a.runtimeHintDisplay(msgs)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	}
	if a.runtimeBinary != "" {
		ui.Text(c, msgs.RuntimeHelperLine(a.runtimeBinary)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	}
	ui.Text(c, msgs.RuntimeStoreLine(valueOr(a.storePath, msgs.RuntimeValueNotConfigured()))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	ui.Text(c, msgs.RuntimeWorkDirLine(valueOr(a.workDir, msgs.RuntimeValueNotConfigured()))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	workspace := ct.SessionWorkingDir
	if workspace == "" {
		workspace = a.effectiveContactWorkspace(ct)
	}
	ui.Text(c, msgs.RuntimeContactWorkspaceLine(valueOr(workspace, msgs.RuntimeValueNotConfigured()))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	session := ct.SessionID
	if session == "" {
		session = msgs.RuntimeSessionNotCreated()
	}
	state := ct.SessionState
	if state == "" {
		state = msgs.RuntimeValueUnknown()
	}
	ui.Text(c, msgs.RuntimeSessionLine(session, state)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	switch {
	case ct.SessionAPI != "":
		ui.Text(c, msgs.RuntimeSessionAPILine(ct.SessionAPI)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	default:
		ui.Text(c, msgs.RuntimeSessionAPIUnknown()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	}
	ui.Text(c, msgs.RuntimeSessionModelLine(valueOr(ct.SessionModel, msgs.RuntimeSessionModelUnknown()))).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	if a.connectionManaged || (a.connectionForm != nil && a.connectionForm.HasSaved) {
		ui.Text(c, msgs.RuntimeConnectionManagedNote()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
	}
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func toolOutputSummary(msgs locales.Messages, output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	var value map[string]any
	if json.Unmarshal([]byte(output), &value) != nil {
		return truncate(output, 300)
	}
	body := ""
	for _, key := range []string{"content", "stdout", "stderr", "output"} {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			body = truncate(text, 240)
			break
		}
	}
	prefix := ""
	if text, ok := value["error"].(string); ok && strings.TrimSpace(text) != "" {
		prefix = msgs.ToolOutputFailed(truncate(text, 120))
	}
	code, hasCode := value["exit_code"].(float64)
	timedOut, _ := value["timed_out"].(bool)
	switch {
	case timedOut && hasCode && code != 0:
		prefix = msgs.ToolOutputTimedOutExit(fmt.Sprintf("%g", code))
	case timedOut:
		prefix = msgs.ToolOutputTimedOut()
	case hasCode && code != 0:
		if prefix == "" {
			prefix = msgs.ToolOutputFailedExit(fmt.Sprintf("%g", code))
		} else {
			prefix += " (" + fmt.Sprintf("%g", code) + ")"
		}
	}
	if prefix != "" {
		if body != "" {
			return prefix + ": " + body
		}
		return prefix
	}
	if body != "" {
		return body
	}
	// Structured tool results such as write/edit receipts have no readable
	// assistant body. Keep the exact record in Details/Copy, but do not present
	// raw JSON as a result preview.
	return msgs.ToolOutputStructured()
}

func truncate(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

func (a *appModel) openMarkdownFile(base, destination string) error {
	msgs := locales.In(a.uiLocale)
	target, err := url.Parse(destination)
	if err != nil {
		return errors.New(msgs.ResultOpenFailed(err.Error()))
	}
	path := target.Path
	if !filepath.IsAbs(path) {
		if !filepath.IsAbs(base) {
			return errors.New(msgs.ResultOpenFailed(msgs.MarkdownFileSourceUnknown()))
		}
		path = filepath.Join(base, path)
	}
	open := a.openPath
	if open == nil {
		open = openFileWithSystem
	}
	action := requestOpenIssueResultFile(issueResultFile{Path: filepath.Clean(path)}, open)
	if action.NoticeKind != "" {
		return errors.New(issueResultNoticeText(msgs, action.NoticeKind, action.NoticeDetail))
	}
	if action.ErrKind != "" {
		return errors.New(issueResultNoticeText(msgs, action.ErrKind, action.ErrDetail))
	}
	return nil
}
