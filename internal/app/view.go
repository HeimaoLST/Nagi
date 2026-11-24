package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

// Message typography is supplied at the view call site so the reader's
// parser, clipboard and file behavior stay untouched (#187 owns those files).
var messageMarkdownTheme = &markdown.Theme{FontSize: typeBody, LineHeight: 1.5}

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
	c.SetMenuPresentation(ui.PopoverMenus)
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
	a.createBotDialogView(c, t)
	a.composerMaterialDialog(c, t)
}

func (a *appModel) escapeTopLayer() {
	if a.composerMaterial != nil || a.composerAttachMenuOpen {
		return
	}
	if a.detailPageActive() {
		a.closeDetail()
		return
	}
	if a.priorChats != nil {
		a.backProductConversations()
		return
	}
	if a.aux != "" {
		a.closeAux()
		return
	}
	if ct := a.selectedContact(); ct != nil && ct.activity.open {
		a.backProductActivity(ct)
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
	case a.priorChats != nil:
		a.priorChats.focusBack = true
	case a.detailIs(detailProfile):
		a.focusDetailClose = true
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
				add.OnClick(func() {
					add.Focus()
					a.openCreateBotDialog()
				})
			}).Label(msgs.RailContactListActions()).Padding(0)
		})
		ui.SearchField(c, &a.contactSearch).Label(msgs.SearchContacts()).FontSize(typeAction).Placeholder(msgs.RailContactSearchPlaceholder())
		visible := a.visibleContactIndices()
		if len(visible) == 0 {
			if a.hideBuiltinArchitect {
				secondaryButton(c, msgs.RailShowPandoraBox()).AlignSelf(ui.Start).
					Disabled(!a.canChangeBuiltinArchitectVisibility()).OnClick(a.showBuiltinArchitectInRail)
			} else {
				ui.Text(c, msgs.RailNoContactsMatch()).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.TextMuted)
			}
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
		add.OnClick(func() {
			add.Focus()
			a.openCreateBotDialog()
		})
		visible := a.visibleContactIndices()
		if len(visible) == 0 {
			if a.hideBuiltinArchitect {
				label := locales.In(c.Locale()).RailShowPandoraBox()
				button := appTooltip(c, ui.ButtonBase(c).Label(label).Size(32, 32).Center().Radius(8), label)
				button.Disabled(!a.canChangeBuiltinArchitectVisibility()).OnClick(a.showBuiltinArchitectInRail)
				button.Children(func() { contactAvatar(c, t, "PandoraBox", 24) })
			}
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
	state := a.botVisualState(ct, c.Now())
	status := state.label(msgs, true)
	if state.kind == botVisualIdle && ct.recentCompletion {
		status = msgs.BotStatusRailDone()
	}
	label := msgs.AvatarContactLabel(ct.Name)
	tip := label
	if status != "" {
		tip += "\n" + status
	}
	row := ui.ButtonBase(c).Key(ct.ID).Label(label).Description(status).
		Selected(i == a.selected).FillWidth().Height(44).Padding(4).Radius(8).
		AlignItems(ui.Center).Justify(ui.Center).Background(ui.Transparent).BorderWidth(0).TextColor(t.Text)
	if i == a.selected {
		row.Background(t.Selection).TextColor(t.Text)
	}
	row = appTooltip(c, row, tip)
	row.OnClick(func() { a.selectContact(i) })
	a.bindBotRowMenu(row, ct.ID, msgs)
	row.Children(func() {
		botStatusAvatar(c, t, ct, 24, state)
	})
}

func (a *appModel) bindBotRowMenu(row ui.Element, contactID string, msgs locales.Messages) {
	row.PopoverMenu(func(menu *ui.Menu) {
		ct := a.contactByID(contactID)
		if ct == nil {
			return
		}
		ready := a.productProfileReady() && !a.dataRecoveryBlocksChanges()
		mutable := ready || a.demo
		count := len(ct.Issues)
		if ct.activity.loaded && ct.activity.nextCursor == nil {
			count = max(count, len(ct.activity.items))
		}
		if menu.Item(msgs.BotMenuActivities(count)).Disabled(!ready && !ct.activity.loaded).Chosen() {
			a.selectContact(slices.Index(a.contacts, ct))
			a.priorChats = nil
			a.aux = ""
			ct.activity.open, ct.activity.focusToggle = true, true
			ct.activity.scroll.Y = 0
		}
		menu.Separator()
		profile := msgs.ContactProfileAction()
		if ct.PendingLearnings > 0 {
			profile = msgs.MemoryProfileAction(ct.PendingLearnings)
		}
		if menu.Item(profile).Chosen() {
			a.openContactProfile(contactID)
		}
		menu.Separator()
		if sessionBusy(ct.SendState, ct.ActiveTaskID) {
			if menu.Item(msgs.BotMenuStopReply()).Disabled(!ready || ct.ActiveTaskID == "").Chosen() {
				a.cancelRuntimeTask(ct, ct.ActiveTaskID)
			}
		}
		if ct.isBuiltin() {
			if menu.Item(a.builtinArchitectVisibilityLabel(msgs)).Disabled(!a.canChangeBuiltinArchitectVisibility()).Chosen() {
				a.setBuiltinArchitectHidden(!a.hideBuiltinArchitect)
			}
			return
		}
		if ct.Archived {
			if menu.Item(msgs.RestoreContactAction()).Disabled(!mutable).Chosen() {
				a.restoreContact(contactID)
			}
		} else if menu.Item(msgs.BotMenuArchive()).Disabled(!mutable || a.contactArchiveBlocked(ct) != "").Chosen() {
			a.archiveContact(contactID)
		}
	})
	if a.focusProfileRow == contactID {
		row.Focus().ScrollIntoView()
		a.focusProfileRow = ""
	}
}

func (a *appModel) railRow(c *ui.Context, t *ui.Theme, i int) {
	ct := a.contacts[i]
	msgs := locales.In(c.Locale())
	state := a.botVisualState(ct, c.Now())
	line, color := a.botRailSubtitle(ct, msgs, t, state)
	selected := i == a.selected
	row := ui.ButtonBase(c).Key(ct.ID).Label(msgs.AvatarContactLabel(ct.Name)).Description(line).Selected(selected).
		FillWidth().Gap(8).Padding(4, 8).MinHeight(controlHit).Radius(8).AlignItems(ui.Center).Background(ui.Transparent).BorderWidth(0)
	focusFill := a.workTheme.NavHover
	if selected {
		focusFill = t.Selection
	}
	row = drawControlFace(c, row, 8, 4, false, false, selected, 0, 2, focusFill)
	row.OnClick(func() { a.selectContact(i) })
	row = appTooltip(c, row, ct.Name+"\n"+line)
	a.bindBotRowMenu(row, ct.ID, msgs)
	row.Children(func() {
		botStatusAvatar(c, t, ct, 28, state)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
				nameWeight := 400
				if selected {
					nameWeight = 500
				}
				ui.Text(c, ct.Name).FontSize(typeRailName).FontWeight(nameWeight).FixedLineHeight(lineNavigation).
					MaxLines(1).MinWidth(0).Grow(1).TextColor(t.Text)
				if ct.Unread > 0 {
					ui.Text(c, fmt.Sprintf("%d", ct.Unread)).FontSize(typeMeta).FixedLineHeight(lineMeta).Shrink(0).TextColor(t.Warning)
				}
			})
			ui.Text(c, line).FontSize(typeRailSummary).FixedLineHeight(lineNavigation).MaxLines(1).TextColor(color)
		})
	})
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
	radius, fontSize, lineHeight := float32(8), typeMeta, lineMeta
	switch size {
	case 24:
		radius = 7
	case 48:
		radius, fontSize, lineHeight = 14, 20, 24
	}
	ui.Box(c).Size(size, size).MinWidth(size).Shrink(0).Radius(radius).Background(background).
		AlignItems(ui.Center).Justify(ui.Center).IgnoreInput(true).Children(func() {
		ui.Text(c, string(initials[:max(1, n)])).FontSize(fontSize).FontWeight(600).FixedLineHeight(lineHeight).
			SingleLine().TextColor(ink).IgnoreInput(true)
	})
}

func (a *appModel) headerStatusLabel(ct *contact, msgs locales.Messages, visual ...botStatus) string {
	var state botStatus
	if len(visual) == 0 {
		state = a.botVisualState(ct, time.Now())
	} else {
		state = visual[0]
	}
	if state.kind == botVisualIdle {
		return localizedBotPurpose(ct, msgs)
	}
	return state.label(msgs, false)
}

func connectionStatusLabel(state connectionViewState, msgs locales.Messages) string {
	switch state {
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
		return ""
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

func (a *appModel) recoveryNotice(c *ui.Context, t *ui.Theme, key, summary, detail string, settings bool, disclosure ...func(bool, func())) {
	localData := key == "startup" && a.runtimeStage == startupStageLocalStore && a.productHost() == nil
	focusDetails := key == "startup" && a.focusConfiguration
	if focusDetails {
		a.focusConfiguration = false
	}
	detail = a.redactConnectionText(detail)
	msgs := locales.In(c.Locale())
	var actions []func()
	switch {
	case localData:
		actions = append(actions, func() {
			secondaryButton(c, msgs.RestorePausedAction()).OnClick(a.openDataRecovery)
		})
	case key == "local-save":
		actions = append(actions, func() {
			secondaryButton(c, msgs.SettingsRetrySave()).OnClick(func() { _ = a.persist() })
		})
	case key == "startup" && detail == "":
		actions = append(actions, func() {
			details := linkButton(c, msgs.ConfigurationDetails()).OnClick(a.openAdvancedSettings)
			if focusDetails && !c.Focused(a.composerHandle) {
				details.Focus()
				focusDetails = false
			}
		})
	}
	key = "recovery:" + key + ":" + detail
	var openSettings func()
	if settings {
		openSettings = func() { a.openSettingsTab(settingsTabConnection, settingsSourceConnection) }
	}
	entry := recoveryNoticeView(c, t, summary, detail, a.connectionDetailsOpen[key], func() {
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
	}, openSettings, msgs.ConnectionRecoveryOpenSettings(), actions...)
	if settings && (a.focusConnectionEntry || focusDetails) && !c.Focused(a.composerHandle) {
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
}

func conversationNotice(c *ui.Context, render func()) {
	ui.Column(c).FillWidth().MaxWidth(conversationMaxWidth).AlignSelf(ui.Center).MinWidth(0).Padding(8, 16, 0).Children(render)
}

func (a *appModel) connectionRecoveryNotice(c *ui.Context, t *ui.Theme, ct *contact, state connectionViewState, showRuntimeError bool) {
	msgs := locales.In(c.Locale())
	summary := msgs.ConnectionRecoverySetupHint()
	managedStartupFailed := state == connectionViewNotConnected && a.runtimeStatus != runtimeStateExited &&
		a.runtimeStage == "Connection configuration" && (a.connectionEffective != nil || a.connectionSaved != nil || a.connectionManaged)
	switch state {
	case connectionViewSaved:
		summary = msgs.ConnectionRecoverySavedHint()
	case connectionViewNotConnected:
		summary = msgs.ConnectionRecoveryNotConnectedHint()
		if managedStartupFailed {
			summary = msgs.ConnectionRecoveryManagedTitle()
		}
	}
	var detail string
	if showRuntimeError {
		if !managedStartupFailed || a.runtimeErrKind != "" {
			summary = a.runtimeRecoverySummary(msgs)
		}
		detail = strings.TrimSpace(a.runtimeErrorDisplay(msgs) + "\n" + a.runtimeHintDisplay(msgs))
	}
	if mismatch := connectionMismatchDetail(ct); mismatch != "" {
		if ct.SendState == sendUnknown && knownConnectionFailure(ct.SendError) {
			summary = msgs.SendDisconnectedState()
		}
		if mismatch != detail {
			detail = strings.TrimSpace(detail + "\n\n" + mismatch)
		}
	}
	conversationNotice(c, func() {
		a.recoveryNotice(c, t, "startup", summary, detail, true)
	})
}

func (a *appModel) main(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	msgs := locales.In(c.Locale())
	ui.Column(c).Grow(1).MinWidth(0).FillHeight().Margin(4).Background(a.workTheme.Reading).Border(1, t.Border).Radius(16).Clip().Children(func() {
		if browser := a.priorChats; browser != nil && browser.contact == ct {
			a.productConversationsView(c, t, browser)
			return
		}
		visual := a.botVisualState(ct, c.Now())
		state := a.connectionViewState()
		a.header(c, t, ct, visual, state)
		showConnectionSettings := true
		if !a.demo {
			if a.localSaveErr != "" {
				conversationNotice(c, func() {
					a.localSaveNotice(c, t)
				})
			}
			duplicateLocalSave := a.localSaveErr != "" && a.runtimeErrKind == runtimeNoticeStoreSave && a.runtimeErr == a.localSaveErr
			showRuntimeError := a.hasRuntimeError() && (!duplicateLocalSave || a.runtimeStage == startupStageLocalStore)
			switch {
			case a.runtimeStatus == runtimeStateDraining:
				conversationNotice(c, func() {
					ui.Text(c, msgs.RuntimeDraining()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				})
			case state == connectionViewApplying:
				conversationNotice(c, func() {
					if visual.kind == botVisualWaiting {
						ui.Text(c, msgs.ConnectionStatusApplying()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning)
					}
					ui.Text(c, msgs.ConnectionRecoveryApplyingHint()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
				})
			case a.connectionConfigurationBlocked():
				a.connectionRecoveryNotice(c, t, ct, state, showRuntimeError && a.runtimeErrKind != runtimeNoticeConnectionRequired)
				showConnectionSettings = false
			case a.hasRuntimeError():
				if showRuntimeError {
					a.connectionRecoveryNotice(c, t, ct, state, true)
					showConnectionSettings = false
				}
			case a.runtimeStatus == runtimeStateStarting:
				conversationNotice(c, func() {
					if visual.kind == botVisualWaiting {
						ui.Text(c, msgs.RuntimeConnecting()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
					}
					ui.Text(c, msgs.ConnectionRecoveryConnectingHint()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
				})
			}
		}
		if showConnectionSettings {
			showConnectionSettings = a.connectionErrorBanner(c, t, ct, showConnectionSettings)
		}
		if ct.LoadErr != "" {
			if !knownConnectionFailure(ct.LoadErr) {
				conversationNotice(c, func() {
					a.recoveryNotice(c, t, "history:"+ct.ID, msgs.HistoryReadFailed(), ct.LoadErr, showConnectionSettings)
				})
				showConnectionSettings = false
			}
		}
		if ct.Archived {
			conversationNotice(c, func() {
				ui.Text(c, msgs.ArchiveNotice()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
			})
		}
		a.chat(c, t, state, showConnectionSettings)
	})
}

func (a *appModel) header(c *ui.Context, t *ui.Theme, ct *contact, visual botStatus, connection connectionViewState) {
	msgs := locales.In(c.Locale())
	header := ui.Row(c).Key("header").FillWidth().BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
	if ct.isBuiltin() && a.hideBuiltinArchitect {
		a.bindBotRowMenu(header, ct.ID, msgs)
	}
	header.Children(func() {
		ui.Row(c).FillWidth().MinWidth(0).Padding(8, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			c.Key(ct.ID)
			botStatusAvatar(c, t, ct, 28, visual)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				title := ui.Text(c, ct.Name).FontSize(typeContactTitle).FontWeight(600).FixedLineHeight(lineHeader).
					TextColor(t.Text).FillWidth().MaxLines(1).Ellipsis("…")
				appTooltip(c, title, ct.Name)
				if label := a.headerStatusLabel(ct, msgs, visual); label != "" {
					subtitle := ui.Text(c, label).FontSize(typeHeaderMeta).FixedLineHeight(lineHeader).FontWeight(400).
						TextColor(visual.color(t)).FillWidth().MaxLines(1).Ellipsis("…")
					appTooltip(c, subtitle, label)
				}
			})
			a.botStatusAction(c, t, ct, visual, msgs)
			if label := connectionStatusLabel(connection, msgs); !a.demo && label != "" && visual.kind != botVisualWaiting {
				if connection == connectionViewConnecting || connection == connectionViewApplying {
					ui.Text(c, label).FontSize(typeHeaderMeta).FixedLineHeight(lineHeader).TextColor(t.TextMuted).SingleLine().Shrink(0)
				} else {
					secondaryButton(c, label).Label(label).MinHeight(32).Padding(4, 10).Radius(8).Shrink(0).TextColor(t.Warning).OnClick(a.openConfiguration)
				}
			}
			if a.railCollapsed {
				a.showContactsButton(c, t)
			}
		})
	})
}

// ---------- chat ----------

func (a *appModel) chat(c *ui.Context, t *ui.Theme, state connectionViewState, showConnectionSettings bool) {
	ct := a.selectedContact()
	messages := ct.Messages
	ct.list.Key = func(i int) any {
		m := messages[i]
		if m.Key != "" {
			return m.Key
		}
		return m.ID
	}
	ct.list.FollowEnd = true
	ui.Column(c).Key("chat").Grow(1).Children(func() {
		if len(messages) == 0 {
			ui.Row(c).Grow(1).MinHeight(0).Justify(ui.Center).Children(func() {
				a.emptyState(c, t, ct)
			})
		} else {
			dividers := chatTimestampDividers(messages, time.Now(), c.Locale())
			ui.Row(c).Grow(1).FillHeight().Justify(ui.Center).Children(func() {
				ui.List(c, &ct.list, len(messages), func(i int) {
					chatMessageRow(c, t, messages, dividers, i, func() { a.message(c, t, ct, messages[i]) })
				}).Label(locales.In(c.Locale()).ConversationMessagesLabel()).FillHeight().FillWidth().MaxWidth(conversationMaxWidth).Padding(16, 16, 32)
			})
		}
		if len(messages) > 0 && !ct.list.AtEnd() {
			ui.Row(c).Key("back-to-latest").Justify(ui.Center).Children(func() {
				back := secondaryButton(c, locales.In(c.Locale()).BackToLatest())
				back.OnClick(func() {
					ct.list.ScrollToEnd()
					a.focusComposer = true
				})
			})
		}
		a.conversationActivities(c, t, ct)
		ui.Row(c).Key("composer").Justify(ui.Center).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(conversationMaxWidth).Children(func() {
				a.composer(c, t, state, showConnectionSettings)
			})
		})
	})
}

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
							if issue := ct.selectedIssue(); issue != nil {
								a.activityText(c, t, issue.Goal)
								if question := issue.openQuestion(); question != nil {
									a.activityText(c, t, question.Text)
								}
							}
						case p.result != nil:
							a.productActivityResultView(c, t, p)
						case p.activity != nil:
							a.productActivityDetails(c, t, ct, client)
						case p.activityID == "":
							for _, issue := range ct.Issues {
								if issue != nil && !issue.Archived {
									secondaryButton(c, issue.Goal).Label(msgs.IssueRowLabel(issue.Goal)).OnClick(func() { a.openLegacyBotIssue(ct, issue) })
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
					a.activityText(c, t, msgs.IssueAnswerSaved())
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
		if notice := issueResultInspectionNoticeText(msgs, *file); notice != "" {
			a.activityText(c, t, notice)
		} else if file.ReadErrorKind != "" {
			a.activityText(c, t, issueResultNoticeText(msgs, file.ReadErrorKind, file.ReadErrorDetail))
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

func (a *appModel) productReadOnlyTranscript(c *ui.Context, t *ui.Theme, browser *productConversationBrowser) {
	messages := browser.history.messages
	msgs := locales.In(c.Locale())
	if len(messages) == 0 {
		if browser.ownerID != "" {
			ui.Row(c).FillWidth().Justify(ui.Center).Children(func() {
				a.activityText(c, t, msgs.EmptyNoMessages())
			})
		}
		return
	}
	browser.transcript.Key = func(i int) any { return messages[i].Key }
	dividers := chatTimestampDividers(messages, time.Now(), c.Locale())
	ui.Row(c).Grow(1).FillWidth().MinHeight(0).Justify(ui.Center).Children(func() {
		ui.List(c, &browser.transcript, len(messages), func(i int) {
			m := messages[i]
			chatMessageRow(c, t, messages, dividers, i, func() {
				if m.Kind == msgUser {
					a.userChatBubble(c, t, m)
					return
				}
				scope := historyLinkScope(browser.ownerID, browser.selectedID, m.Key)
				a.botReplyBubbles(c, t, m, scope, "")
			})
		}).Label(msgs.ConversationMessagesLabel()).Fill().MaxWidth(conversationMaxWidth).Padding(16, 16, 32)
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
	case msgUser:
		a.userChatBubble(c, t, m)
	default:
		a.botReplyBubbles(c, t, m, messageLinkScope(ct.ID, m), ct.SessionWorkingDir)
	}
}

func (a *appModel) userChatBubble(c *ui.Context, t *ui.Theme, m *message) {
	ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
		ui.Column(c).MaxWidth(userMessageMaxWidth).MinWidth(0).Shrink(1).Gap(2).Children(func() {
			if m.Text != "" {
				opts := messageReaderOptions(c, true)
				opts.MessageBubble = &markdownMessageBubble{Background: a.workTheme.BubbleUser, PaddingX: 12, PaddingY: 8, Radius: 12, ContextualCopy: true}
				markdownReader(c, m.Text, opts).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
			}
			taskContextReadOnlyView(c, a.workTheme, m.Context)
		})
	})
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

func connectionMismatchDetail(ct *contact) string {
	if ct == nil {
		return ""
	}
	load, send := strings.TrimSpace(ct.LoadErr), strings.TrimSpace(ct.SendError)
	if !knownConnectionFailure(load) {
		load = ""
	}
	if !knownConnectionFailure(send) || send == load {
		send = ""
	}
	return strings.TrimSpace(load + "\n\n" + send)
}

func (a *appModel) connectionErrorBanner(c *ui.Context, t *ui.Theme, ct *contact, showConnectionSettings bool) bool {
	detail := connectionMismatchDetail(ct)
	if detail == "" {
		return showConnectionSettings
	}
	conversationNotice(c, func() {
		summary := locales.In(c.Locale()).ConnectionMismatchSummary()
		if ct.SendState == sendUnknown && knownConnectionFailure(ct.SendError) {
			summary = locales.In(c.Locale()).SendDisconnectedState()
		}
		a.recoveryNotice(c, t, "connection:"+ct.ID, summary, detail, showConnectionSettings)
	})
	return false
}

// ---------- composer ----------

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
	if connected {
		unknownSummary = msgs.IssueExecutionUnknownDetail()
		if canReload {
			unknownSummary = msgs.SendUnknownState()
		}
	}
	ui.Column(c).FillWidth().Padding(8, 16).Gap(4).Children(func() {
		wb := a.workTheme
		card := ui.Column(c).Key(ct.ID).FillWidth().Padding(10, 14, 8, 14).Gap(4).Radius(16)
		surface, border := wb.ComposerSurface, wb.ComposerBorder
		if c.Focused(a.composerHandle) {
			border = t.Focus
		}
		dragOver := false
		if !a.shutdownStarted && !a.dataRecoveryBlocksChanges() {
			dragOver = card.FileDragOver()
			if paths := card.DroppedFiles(); len(paths) > 0 {
				a.attachComposerFiles(ct, paths)
			}
		}
		if dragOver {
			border, surface = t.Accent, surface.Mix(t.Accent, .06)
		}
		card.Border(1, border).Background(surface)
		if t.Dark {
			card.Shadow(0, 2, 12, 0, ui.RGBA(0, 0, 0, .24))
		} else {
			card.Shadow(0, 2, 10, 0, ui.RGBA(0, 0, 0, .05))
		}
		card.Children(func() {
			if ct.Context.Count() > 0 {
				a.composerAttachments(c, t, ct)
			}
			if dragOver {
				ui.Text(c, msgs.ComposerDropFiles()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(wb.ComposerMuted)
			}
			c.Key("draft")
			input := ui.TextAreaBase(c, &ct.Draft).Bind(&a.composerHandle).Label(msgs.ComposerLabel(ct.Name)).
				FontSize(typeBody).FixedLineHeight(lineBody).Lines(1, 6).TextColor(wb.ComposerText).
				Background(ui.Transparent).BorderWidth(0).Padding(0, 2).FocusRing(false).
				Placeholder(msgs.ComposerPlaceholder(ct.Name)).PlaceholderColor(wb.ComposerMuted).FillWidth().OnChange(func() {
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
			if a.composerCaretToEnd {
				input.SetTextSelection(math.MaxInt, math.MaxInt)
				a.composerCaretToEnd = false
			}
			if a.focusComposer {
				input.Focus()
				a.focusComposer = false
			}
			if a.focusConnectionEntry && c.Focused(a.composerHandle) {
				a.focusConnectionEntry = false
			}
			sendDisabled := strings.TrimSpace(ct.Draft) == "" || ct.Status == statusWorking
			sendReason := ""
			if !a.demo {
				sendReason = a.canSend(ct)
				sendDisabled = strings.TrimSpace(ct.Draft) == "" || sendReason != ""
			}
			a.composerToolbar(c, t, ct, sendDisabled)
			if ct.Context.ActionError != nil {
				a.composerAttachmentError(c, t, ct)
			}
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
				case ct.SendState == sendUnknown && !unknownNotice && !knownMismatch:
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
	})
}

// ---------- main pages ----------

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
