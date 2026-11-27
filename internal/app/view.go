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

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

var messageMarkdownTheme = &markdown.Theme{FontSize: typeBody, LineHeight: 1.5}

func messageReaderOptions(c *ui.Context, plain bool) markdownReaderOptions {
	return markdownReaderOptions{PlainText: plain, CopyLabel: locales.In(c.Locale()).CopyMessageLabel(), Theme: messageMarkdownTheme}
}

func (a *appModel) view(c *ui.Context) {
	a.uiLocale = c.Locale()
	a.refreshDockMenu()
	a.syncDetailPageMode()
	if !a.settingsOpen {
		a.linkReturnSeen = false
	}
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
	ct := a.selectedContact()
	composerHandlesEscape := a.quoteDraftEditable(ct) && ct.DraftQuote != nil && strings.TrimSpace(ct.Draft) == "" && c.Focused(a.composerHandle)
	if !a.settingsOpen && !composerHandlesEscape {
		c.OnShortcut(0, ui.KeyEscape, func() { a.escapeTopLayer() })
	}
	ui.Column(c).Fill().LinearGradient(ui.LinearGradient{From: a.workTheme.ChromeFrom, To: a.workTheme.ChromeTo, Angle: 180}).Children(func() {
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
	a.providerDialog(c, t)
	a.capabilityDialogs(c, t)
	a.skillReviewDialog(c, t)
	a.composerMaterialDialog(c, t)
}

func (a *appModel) escapeTopLayer() {
	if a.composerMaterial != nil || a.composerAttachMenuOpen || a.capabilities.review != nil {
		return
	}
	if a.backgroundWork.panel != nil {
		a.backBackgroundWork()
		return
	}
	if a.detailIs(detailProfile) && a.scheduledWorkBotID != "" {
		if ct := a.contactByID(a.scheduledWorkBotID); ct != nil {
			a.schedules.bot(ct.ID).back(a.scheduledWorkFallbackDeps(ct))
			return
		}
	}
	if a.detailPageActive() {
		a.closeDetail()
		return
	}
	if a.priorChats != nil {
		a.backProductConversations()
		return
	}
	if ct := a.selectedContact(); !a.demo && ct != nil && a.sessions.activeBotID == ct.ID {
		a.sessions.backSessionInspection(a.sessionInspectorFallbackDeps(ct))
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

func (a *appModel) armActiveSurfaceFallback() {
	switch {
	case a.backgroundWork.panel != nil:
		a.backgroundWork.panel.focusBack = true
	case a.priorChats != nil:
		a.priorChats.focusBack = true
	case !a.demo && a.selectedContact() != nil && a.sessions.activeBotID == a.selectedContact().ID:
		a.sessions.botSession(a.selectedContact().ID).focusBack = true
	case a.detailIs(detailProfile) && a.scheduledWorkBotID != "":
		a.schedules.bot(a.scheduledWorkBotID).focusHeading = true
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
				button.Children(func() { botStoneAvatar(c, t, "", "PandoraBox", true, 24) })
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
		count := len(ct.Activities)
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
		if !a.demo && a.productHost() != nil {
			if menu.Item(msgs.SchedWorkTitle()).Chosen() {
				a.openBotScheduledWork(contactID)
			}
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

func contactAvatar(c *ui.Context, t *ui.Theme, ct *contact, size float32) {
	var id, name string
	var builtin bool
	if ct != nil {
		id, name, builtin = ct.ID, ct.Name, ct.isBuiltin()
	}
	botStoneAvatar(c, t, id, name, builtin, size)
}

func botStoneAvatar(c *ui.Context, t *ui.Theme, id, name string, builtin bool, size float32) {
	spec := resolveBotAvatar(t, id, name, builtin)
	ui.Box(c).Size(size, size).MinWidth(size).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).IgnoreInput(true).Children(func() {
		ui.Icon(c, spec.Body).Size(size, size).TextColor(spec.BodyColor).IgnoreInput(true)
		ui.Icon(c, spec.Eyes).Absolute().Left(0).Top(0).Size(size, size).TextColor(spec.EyeColor).IgnoreInput(true)
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
	case runtimeNoticeCredentialUnavailable:
		return msgs.ProviderCredentialRecovery()
	case runtimeNoticeRouteUnavailable:
		return msgs.ProviderRoutesNeedAttention()
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
		if panel := a.backgroundWork.panel; panel != nil {
			a.backgroundWorkView(c, t, panel)
			return
		}
		if browser := a.priorChats; browser != nil && browser.contact == ct {
			a.productConversationsView(c, t, browser)
			return
		}
		if !a.demo && a.sessions.activeBotID == ct.ID {
			a.sessions.sessionInspectionView(c, a.sessionInspectorDeps(c, ct))
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
			activity := &ct.activity
			pausedAnswerVisible := activity.open && !activity.legacyIssue && activity.result == nil && activity.activity != nil &&
				slices.ContainsFunc(activity.activity.Questions, func(question pi.ProductQuestion) bool {
					return question.Answer != nil && question.Delivery == "saved" && providerDeliveryBlocked(question.DeliveryBlocker)
				})
			switch {
			case a.runtimeStatus == runtimeStateDraining:
				conversationNotice(c, func() {
					ui.Text(c, msgs.RuntimeDraining()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				})
			case (a.runtimeErrKind == runtimeNoticeCredentialUnavailable || a.runtimeErrKind == runtimeNoticeRouteUnavailable) && (ct.providerSendBlocked() || pausedAnswerVisible):
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
			if !a.demo && a.productHost() != nil && !ct.Archived {
				a.sessions.sessionInspectorButton(c, a.sessionInspectorDeps(c, ct))
			}
			if !a.demo && a.productHost() != nil {
				a.backgroundWorkButton(c, ct, visual.backgroundActive+visual.backgroundDecisions)
			}
			if label := connectionStatusLabel(connection, msgs); !a.demo && label != "" && visual.kind != botVisualWaiting {
				if connection == connectionViewConnecting {
					ui.Text(c, label).FontSize(typeHeaderMeta).FixedLineHeight(lineHeader).TextColor(t.TextMuted).SingleLine().Shrink(0)
				} else if a.runtimeErrKind == runtimeNoticeCredentialUnavailable {
					ui.Text(c, label).FontSize(typeHeaderMeta).FixedLineHeight(lineHeader).TextColor(t.Warning).SingleLine().Shrink(0)
				} else {
					button := secondaryButton(c, label).Label(label).MinHeight(theme.ControlHit).Padding(4, 10).
						Radius(theme.ControlRadius).Shrink(0).TextColor(t.Warning)
					theme.TransitionControl(button, t).OnClick(a.openConfiguration)
				}
			}
			if a.railCollapsed {
				a.showContactsButton(c, t)
			}
		})
	})
}

func (a *appModel) chat(c *ui.Context, t *ui.Theme, state connectionViewState, showConnectionSettings bool) {
	ct := a.selectedContact()
	messages := ct.Messages
	waitingForReply := ct.Status == statusWorking && (a.demo || ct.ActiveTaskID != "")
	if waitingForReply && !a.demo {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Kind == msgNotice {
				continue
			}
			if messages[i].TaskID != ct.ActiveTaskID {
				break
			}
			if messages[i].Kind == msgContact {
				waitingForReply = false
				break
			}
		}
	}
	rows := len(messages)
	if waitingForReply {
		rows++
	}
	ct.list.Key = func(i int) any {
		if i == len(messages) {
			return "live:" + ct.ActiveTaskID + ":text:pi"
		}
		m := messages[i]
		if m.Key != "" {
			return m.Key
		}
		return m.ID
	}
	ct.list.FollowEnd = true
	ui.Column(c).Key("chat").Grow(1).MinHeight(0).Children(func() {
		if ct.transcriptError {
			timelineTranscriptWarning(c, t)
		}
		if rows == 0 {
			ui.Row(c).Grow(1).MinHeight(0).Justify(ui.Center).Children(func() {
				a.emptyState(c, t, ct)
			})
		} else {
			dividers := chatTimestampDividers(messages, time.Now(), c.Locale())
			readIndex := latestReadUserMessageIndex(messages, c.Now(), c.Preferences().ReduceMotion)
			ui.Row(c).Grow(1).MinHeight(0).FillHeight().Justify(ui.Center).Children(func() {
				ui.List(c, &ct.list, rows, func(i int) {
					if i == len(messages) {
						ui.Column(c).FillWidth().Padding(14, 0, 0, 0).Children(func() { a.replyTypingBubble(c) })
						return
					}
					chatMessageRow(c, t, messages, dividers, i, func() { a.message(c, t, ct, messages[i], i == readIndex) })
				}).Label(locales.In(c.Locale()).ConversationMessagesLabel()).FillHeight().FillWidth().MaxWidth(conversationMaxWidth).Padding(16, 16, 32)
			})
		}
		if rows > 0 && !ct.list.AtEnd() {
			ui.Row(c).Key("back-to-latest").Justify(ui.Center).Children(func() {
				back := secondaryButton(c, locales.In(c.Locale()).BackToLatest())
				back.OnClick(func() {
					ct.list.ScrollToEnd()
					a.focusComposer = true
				})
			})
		}
		a.conversationActivities(c, t, ct)
		a.orchestrationDecisionStrip(c, t, ct)
		a.activityNoticeStrip(c, t, ct)
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
		dock := ui.Column(c).Key(ct.ID).FillWidth().MaxWidth(conversationMaxWidth-32).MinWidth(0).Margin(0, 16, 4)
		if p.open {
			dock.Gap(4).Padding(4, 8).Radius(theme.CardRadius).Border(1, t.Border).Background(t.Surface)
		}
		dock.Children(func() {
			ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(4).Children(func() {
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
					ui.Column(c).FillWidth().MinWidth(0).Gap(8).Padding(4, 0).Children(func() {
						if client == nil {
							a.activityText(c, t, msgs.WorkHistoryNotReady())
						}
						if p.problem != "" {
							ui.Text(c, p.problem).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
						}
						if p.actionProblem != "" && p.actionTarget == p.activityID {
							ui.Text(c, p.actionProblem).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
						}
						if p.actionTarget == p.activityID && providerDeliveryBlocked(p.actionBlocker.Code) {
							summary := msgs.ProviderSendRejected()
							if p.actionBlocker.Code == pi.ProductRouteUnavailable {
								summary = providerRouteRejectionText(c, msgs, p.actionBlocker.RetryAt)
							}
							ui.Text(c, summary).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
							linkButton(c, msgs.ConnectionRecoveryOpenSettings()).AlignSelf(ui.Start).OnClick(a.openConfiguration)
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
									secondaryButton(c, activity.Goal).AlignSelf(ui.Start).MaxWidthPercent(100).Label(msgs.IssueRowLabel(activity.Goal)).OnClick(func() { a.openLegacyBotActivity(ct, activity) })
								}
							}
							for _, item := range p.items {
								ui.Row(c).Key(item.ActivityID).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
									secondaryButton(c, item.Goal).Label(msgs.IssueRowLabel(item.Goal)).Disabled(client == nil || p.reading).OnClick(func() {
										a.openProductActivity(ct, item.ActivityID)
									})
									activityStatusBadge(c, t, msgs, item)
									activityProgressView(c, t, msgs, item.LatestProgress)
								})
							}
							if p.nextCursor != nil {
								secondaryButton(c, msgs.SupplementShowAll()).AlignSelf(ui.Start).Disabled(client == nil || p.reading).OnClick(func() { a.refreshProductActivities(ct, true) })
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
	ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
		activityStatusBadge(c, t, msgs, activity.ProductActivitySummary)
		activityProgressView(c, t, msgs, activity.LatestProgress)
	})
	if origin := activity.OriginConversationID; origin != "" {
		source := secondaryButton(c, msgs.SourceChat()).AlignSelf(ui.Start).Disabled(client == nil).OnClick(func() { a.browseProductConversations(ct, origin) })
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
		if pending.Action == "steer" {
			capabilityBadge(c, t, msgs.ActivitySteerUnknown(), t.Warning)
		}
		if p.forms[activity.ActivityID] == nil {
			a.activityText(c, t, pending.Text)
			taskContextReadOnlyView(c, a.workTheme, productTaskContext(pending.Materials))
		}
		secondaryButton(c, msgs.ReloadAction()).AlignSelf(ui.Start).Disabled(client == nil || p.changing || a.dataRecoveryBlocksChanges()).OnClick(func() {
			a.runProductActivityAction(ct, client, pending, false)
		})
	}
	for _, request := range openActivityApprovals(ct, activity) {
		c.Key(request.RequestID)
		ui.Column(c).FillWidth().MinWidth(0).Padding(8, 0, 0).Gap(4).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			a.activityApprovalCard(c, t, ct, request, blocked, false)
		})
	}
	for _, question := range activity.Questions {
		ui.Column(c).Key(question.QuestionID).FillWidth().Gap(4).Children(func() {
			a.activityText(c, t, question.Text)
			if question.State == "open" {
				answer := activityAnswerIntent(ct, activity, question)
				button := secondaryButton(c, msgs.IssueNoticeWaiting(activity.Goal)).AlignSelf(ui.Start).MaxWidthPercent(100).Disabled(blocked).OnClick(func() { a.editProductActivity(ct, answer) })
				if p.focusQuestion == question.QuestionID && !blocked {
					button.Focus().ScrollIntoView()
					p.focusQuestion = ""
				}
			}
			if question.Answer != nil {
				a.activityText(c, t, question.Answer.Text)
				if question.Provenance == "bot_auto" {
					capabilityBadge(c, t, msgs.ActivityAutoAnswered(), t.TextMuted)
				}
				taskContextReadOnlyView(c, a.workTheme, productTaskContext(question.Answer.Materials))
				switch question.Delivery {
				case "saved":
					if providerDeliveryBlocked(question.DeliveryBlocker) {
						notice := msgs.ProviderAnswerPaused()
						if question.DeliveryBlocker == pi.ProductRouteUnavailable {
							notice = msgs.ProviderAnswerRoutePaused()
						}
						ui.Text(c, notice).FontSize(typeMeta).LineHeight(1.5).TextColor(t.Warning)
						linkButton(c, msgs.ConnectionRecoveryOpenSettings()).AlignSelf(ui.Start).OnClick(a.openConfiguration)
					} else {
						a.activityText(c, t, msgs.IssueAnswerSaved())
					}
				case "dispatching":
					a.activityText(c, t, msgs.IssueAnswerDispatching())
				case "delivered":
					a.activityText(c, t, msgs.ActivityDelivered())
				case "unknown":
					a.activityText(c, t, msgs.SupplementUnknown())
				default:
					a.activityText(c, t, question.Delivery)
				}
			}
		})
	}
	canContinue := activity.ClosedAt == "" && len(activity.OpenQuestionIDs)+len(activity.PendingApprovalRequestIDs) == 0
	for i := len(activity.Executions) - 1; i >= 0; i-- {
		execution := activity.Executions[i]
		label := msgs.IssueCurrentRun(activityExecutionLabel(msgs, execution.State))
		ui.Column(c).Key(execution.ExecutionID).FillWidth().MinWidth(0).Gap(4).Children(func() {
			if i != len(activity.Executions)-1 {
				label = activityExecutionLabel(msgs, execution.State)
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
				ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).Children(func() {
					secondaryButton(c, msgs.WorkStopIssueTask()).Disabled(blocked || p.stopping == execution.ExecutionID).OnClick(func() { a.submitProductActivity(ct, stop, nil) })
					if canSteerActivity(p, activity, execution) {
						steer := target
						steer.Action, steer.Revision, steer.ExecutionID = "steer", activity.Revision, execution.ExecutionID
						secondaryButton(c, msgs.ActivitySteer()).Disabled(blocked || ct.Archived).OnClick(func() { a.editProductActivity(ct, steer) })
					}
				})
			case "unknown":
				if i == len(activity.Executions)-1 {
					canContinue = false
				}
			default:
				label := msgs.IssueOpenResultRun(fmt.Sprint(i + 1))
				secondaryButton(c, label).AlignSelf(ui.Start).Label(label).Disabled(client == nil || p.reading).OnClick(func() {
					a.readProductActivityResult(ct, activity.ActivityID, execution.ExecutionID)
				})
			}
			activitySteerReceipts(c, t, msgs, activity, execution.ExecutionID)
			a.activityExecutionTrace(c, t, ct, activity.ActivityID, execution.ExecutionID)
		})
	}
	if canContinue && p.forms[activity.ActivityID] == nil {
		target.Action, target.Revision = "continue", activity.Revision
		secondaryButton(c, msgs.SupplementAdd()).AlignSelf(ui.Start).Disabled(blocked || ct.Archived).OnClick(func() { a.editProductActivity(ct, target) })
	}
	if form := p.forms[activity.ActivityID]; form != nil {
		a.productActivityEditView(c, t, ct, form, blocked)
	}
	a.activityLifecycleActions(c, ct, activity, blocked)
}

func (a *appModel) productActivityEditView(c *ui.Context, t *ui.Theme, ct *contact, form *productActivityForm, blocked bool) {
	p, msgs := &ct.activity, locales.In(c.Locale())
	label, submit := msgs.SupplementFormLabel(), msgs.IssueResume()
	question := activityFormQuestion(p.activity, form.target)
	switch form.target.Action {
	case "answer":
		label, submit = msgs.IssueNoticeWaiting(p.activity.Goal), msgs.ContactSaveChanges()
	case "steer":
		label, submit = msgs.ActivitySteerInstruction(), msgs.ActivitySendSteer()
	}
	valid := a.validActivityControlTarget(ct, p.activity, form.target, form)
	ui.Column(c).Key(activityFormKey(form.target)).FillWidth().MinWidth(0).Gap(8).Children(func() {
		var input ui.Element
		if question == nil || questionAllowsText(*question) {
			c.Key("activity-edit-text")
			input = formTextArea(c, &form.text, false, a.workTheme.InputBorder).Label(label).Disabled(blocked).Height(80)
			if form.caretToEnd {
				input.SetTextSelection(math.MaxInt, math.MaxInt)
				form.caretToEnd = false
			}
			if form.focus && !blocked {
				input.Focus().ScrollIntoView()
				form.focus = false
			}
			if question != nil {
				input.OnChange(func() {
					if option := activityQuestionOption(*question, form.target.SelectedOptionID); option != nil && form.text != option.Label {
						form.target.SelectedOptionID = ""
					}
				})
			}
		}
		if question != nil && len(question.Options) > 0 {
			activityOptions(c, msgs, *question, form, blocked || question.State != "open", func() {
				if questionAllowsText(*question) {
					form.focus, form.caretToEnd = true, true
				}
			})
		}
		if !blocked {
			form.materials.ChooseFile = func() { a.chooseActivityMaterial(ct, form) }
			form.materials.View(c, a.workTheme)
		} else {
			taskContextReadOnlyView(c, a.workTheme, form.materials.Items)
		}
		ui.Row(c).Wrap().Gap(8).Children(func() {
			secondaryButton(c, submit).Disabled(blocked || !valid || form.materials.FormOpen).OnClick(func() { a.submitProductActivity(ct, form.target, form) })
			secondaryButton(c, msgs.SupplementCancel()).Disabled(blocked).OnClick(func() { discardActivityDraft(p, form.target) })
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

func (a *appModel) productReadOnlyTranscript(c *ui.Context, t *ui.Theme, browser *productConversationBrowser) {
	messages := browser.history.messages
	msgs := locales.In(c.Locale())
	if browser.history.transcriptError {
		timelineTranscriptWarning(c, t)
	}
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
	readIndex := latestReadUserMessageIndex(messages, c.Now(), c.Preferences().ReduceMotion)
	ui.Row(c).Grow(1).FillWidth().MinHeight(0).Justify(ui.Center).Children(func() {
		ui.List(c, &browser.transcript, len(messages), func(i int) {
			m := messages[i]
			chatMessageRow(c, t, messages, dividers, i, func() {
				if m.Kind == msgNotice {
					a.timelineNotice(c, t, browser.ownerID, m)
					return
				}
				if m.Kind == msgUser {
					a.userChatBubble(c, t, m, nil, &browser.recall, i == readIndex, func() {
						recallQuotedMessage(c, messages, &browser.transcript, &browser.recall, m.Quote)
					})
					return
				}
				scope := historyLinkScope(browser.ownerID, browser.selectedID, m.Key)
				a.botReplyBubbles(c, t, m, scope, "", nil, &browser.recall)
			})
		}).Label(msgs.ConversationMessagesLabel()).Fill().MaxWidth(conversationMaxWidth).Padding(16, 16, 32)
	})
}

func (a *appModel) message(c *ui.Context, t *ui.Theme, ct *contact, m *message, read bool) {
	msgs := locales.In(c.Locale())
	switch m.Kind {
	case msgNotice:
		a.timelineNotice(c, t, ct.ID, m)
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
		a.userChatBubble(c, t, m, ct, &ct.recall, read, func() {
			recallQuotedMessage(c, ct.Messages, &ct.list, &ct.recall, m.Quote)
		})
	default:
		a.botReplyBubbles(c, t, m, messageLinkScope(ct.ID, m), ct.SessionWorkingDir, ct, &ct.recall)
	}
}

func (a *appModel) userChatBubble(c *ui.Context, t *ui.Theme, m *message, ct *contact, recall *quoteRecall, read bool, jump func()) {
	ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
		ui.Column(c).MaxWidth(userMessageMaxWidth).MinWidth(0).Shrink(1).Gap(2).Children(func() {
			if m.Quote != nil {
				a.sentQuote(c, t, m.Quote, jump)
			}
			if m.Text != "" {
				opts := messageReaderOptions(c, true)
				opts.MessageBubble = &markdownMessageBubble{Background: a.workTheme.BubbleUser, PaddingX: 12, PaddingY: 8, Radius: 12, MenuOnlyCopy: true}
				opts.MessageBubble.BeforeCopyMenu = a.quoteMenu(c, ct, m, userQuoteBubble, m.Text)
				opts.MessageBubble.Highlight = recall.highlight(c, m, userQuoteBubble)
				markdownReader(c, m.Text, opts).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
			}
			taskContextReadOnlyView(c, a.workTheme, m.Context)
			if read {
				ui.Text(c, locales.In(c.Locale()).MessageReadStatus()).Key("read-status").AlignSelf(ui.End).
					Padding(0, 4).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
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
			if ct.DraftQuote != nil {
				a.composerQuote(c, t, ct)
			}
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
				if k.Key == ui.KeyEscape && strings.TrimSpace(k.Text()) == "" && a.removeDraftQuote(ct) {
					return true
				}
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
			submissionStatus := ""
			if !a.demo {
				switch {
				case ct.SendState == sendSubmitting:
					submissionStatus = msgs.SendingState()
				case ct.SendState == sendQueued:
					submissionStatus = msgs.SendQueuedState()
					if ct.SendQueuedPosition > 0 {
						submissionStatus = msgs.SendQueuedPosition(ct.SendQueuedPosition)
					}
				case ct.sendNotice == "cancelled_before_dispatch":
					submissionStatus = msgs.SendCancelledBeforeDispatch()
				case ct.sendNotice == "interrupted_before_dispatch":
					submissionStatus = msgs.SendInterruptedBeforeDispatch()
				case ct.SendState == sendAccepted && ct.Status != statusWorking:
					submissionStatus = msgs.SendAcceptedState()
				}
			}
			a.composerToolbar(c, t, ct, sendDisabled, submissionStatus)
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
				case "":
				default:
					if strings.TrimSpace(ct.Draft) != "" && (sendReason != blockBusy || submissionStatus == "") {
						ui.Text(c, sendBlockText(msgs, sendReason)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
					}
				}
				if ct.SendState == sendUnknown && !unknownNotice && !knownMismatch {
					ui.Text(c, unknownSummary).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
				}
				if ct.providerSendBlocked() {
					summary := msgs.ProviderSendRejected()
					if ct.SendErrorKind == sendErrorRouteUnavailable {
						summary = providerRouteRejectionText(c, msgs, ct.SendErrorDetail)
					}
					a.recoveryNotice(c, t, "provider-send:"+ct.ID, summary, "", true)
					showConnectionSettings = false
				} else if sendErrorVisible || connectionLostDetail {
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
				if canReload && !ct.providerSendBlocked() && (ct.SendState == sendUnknown || ct.SendError != "") {
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

func (a *appModel) openConfiguration() {
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
	if a.connectionManaged || a.connectionSaved != nil {
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
	action := requestOpenActivityResultFile(activityResultFile{Path: filepath.Clean(path)}, open)
	if action.NoticeKind != "" {
		return errors.New(activityResultNoticeText(msgs, action.NoticeKind, action.NoticeDetail))
	}
	if action.ErrKind != "" {
		return errors.New(activityResultNoticeText(msgs, action.ErrKind, action.ErrDetail))
	}
	return nil
}
