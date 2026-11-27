package app

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

type dockMenuState struct {
	locale       string
	botID        string
	windowHidden bool
	draining     bool
	closing      bool
	canSchedule  bool
	canInspect   bool
}

func (a *appModel) handleWindowClose(event *mygo.CloseEvent) {
	if a.quitReady || a.sessionEnding {
		return
	}
	event.PreventDefault()
	if a.windowHidden {
		return
	}
	a.windowHidden = true
	a.hideNativeWindow()
	a.refreshDockMenu()
}

func (a *appModel) hideNativeWindow() {
	if a.hideWindow != nil {
		a.hideWindow()
	} else if a.win != nil {
		a.win.Hide()
	}
}

func (a *appModel) handleAppActivate(hasVisibleWindows bool) {
	if !hasVisibleWindows || a.windowHidden {
		a.reopenWindow()
	}
}

func (a *appModel) reopenWindow() {
	if a.quitReady || a.sessionEnding {
		return
	}
	a.windowHidden = false
	if a.showWindow != nil {
		a.showWindow()
	}
	if a.focusWindow != nil {
		a.focusWindow()
	}
	if a.focusApp != nil {
		a.focusApp()
	}
	a.refreshDockMenu()
}

func (a *appModel) invalidateNativeWindow() {
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *appModel) currentDockMenuState() dockMenuState {
	state := dockMenuState{
		locale: "en", windowHidden: a.windowHidden,
		draining: a.shutdownStarted && !a.quitReady, closing: a.quitReady || a.sessionEnding,
	}
	if strings.HasPrefix(a.uiLocale, "zh") {
		state.locale = "zh-Hans"
	}
	if state.draining || state.closing || a.selected < 0 || a.selected >= len(a.contacts) {
		return state
	}
	ct := a.selectedContact()
	if ct != nil && ct.ID != "" && !ct.isBuiltin() {
		state.botID = ct.ID
		state.canSchedule = a.productActivityClient() != nil
		state.canInspect = a.productHost() != nil && !ct.Archived
	}
	return state
}

func (a *appModel) refreshDockMenu() {
	if a.setDockMenu == nil {
		return
	}
	key := a.currentDockMenuState()
	if a.dockMenuKey != key {
		a.dockMenuKey = key
		a.setDockMenu(a.buildDockMenu())
	}
}

func (a *appModel) buildDockMenu() *mygo.Menu {
	state := a.currentDockMenuState()
	msgs := locales.In(state.locale)
	items := []*mygo.MenuItem{{ID: "open-nagi", Label: msgs.DockOpenNagi(), Disabled: state.closing, Click: func(*mygo.MenuItem, *mygo.Window) {
		a.reopenWindow()
	}}}
	if state.draining && !state.closing {
		items = append(items, mygo.Separator(), &mygo.MenuItem{Label: msgs.RuntimeStatusDraining(), Disabled: true})
	}
	if state.botID != "" {
		items = append(items, mygo.Separator(),
			&mygo.MenuItem{ID: "scheduled-work", Label: msgs.SchedWorkTitle(), Disabled: !state.canSchedule, Click: func(*mygo.MenuItem, *mygo.Window) {
				a.openDockBotPanel(state.botID, false)
			}},
			&mygo.MenuItem{ID: "sessions", Label: msgs.SessionInspectTitle(), Disabled: !state.canInspect, Click: func(*mygo.MenuItem, *mygo.Window) {
				a.openDockBotPanel(state.botID, true)
			}},
		)
	}
	return mygo.NewMenu(items)
}

func (a *appModel) openDockBotPanel(botID string, sessions bool) {
	state := a.currentDockMenuState()
	if state.botID != botID || (sessions && !state.canInspect) || (!sessions && !state.canSchedule) {
		return
	}
	ct := a.selectedContact()
	a.closeSettings()
	a.closeAux()
	a.priorChats = nil
	a.resetComposerOverlays()
	if sessions {
		if a.activeDetail() != "" {
			a.closeDetail()
		}
		a.sessions.openSessionInspection(a.sessionInspectorFallbackDeps(ct))
	} else {
		a.sessions.activeBotID = ""
		a.openBotScheduledWork(botID)
	}
	a.reopenWindow()
	a.invalidateNativeWindow()
}

func (a *appModel) handleBeforeQuit(event *mygo.QuitEvent) {
	if event != nil && event.SessionEnding {
		a.drainSynchronouslyOnSessionEnding()
		return
	}
	if a.quitReady {
		return
	}
	if event != nil {
		event.PreventDefault()
	}
	if a.windowHidden {
		a.reopenWindow()
	}
	a.drainPiHost()
	a.invalidateNativeWindow()
}

func (a *appModel) drainSynchronouslyOnSessionEnding() {
	a.sessionEnding, a.shutdownStarted = true, true
	a.runtimeStatus = runtimeStateDraining
	a.stopProviderTimers()
	if !a.quitReady {
		if err := a.persist(); err != nil {
			log.Printf("Nagi session end save: %s", a.connectionSafeMessage(err))
		}
		if host := a.productHost(); host != nil {
			deadline, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			graceful, stop := context.WithTimeout(deadline, 3*time.Second)
			outcome, _ := host.product.Shutdown(graceful)
			if !outcome.Reaped {
				outcome, _ = host.product.Wait(graceful)
			}
			stop()
			if !outcome.Reaped {
				outcome, _ = host.product.Kill(deadline)
			}
			if !outcome.Reaped {
				log.Printf("Nagi session end could not confirm reaping worker %d", outcome.PID)
			}
		}
	}
	a.pi, a.quitReady = nil, true
}

func (a *appModel) nativeWindowView(c *ui.Context) {
	if !a.shutdownStarted {
		a.view(c)
		return
	}
	palette := a.resolveAppearanceTheme(resolveAppearanceDark(a.appearanceMode, c.Theme().Dark)).Palette
	t := palette.Theme
	c.SetTheme(t)
	msgs := locales.In(c.Locale())
	ui.Column(c).Fill().MinWidth(0).Background(t.Background).Children(func() {
		ui.Row(c).FillWidth().Shrink(0).Height(max(32, c.TitleBar().Height)).DragWindow()
		ui.Column(c).FillWidth().MaxWidth(720).AlignSelf(ui.Center).Padding(24).Gap(8).Children(func() {
			if !a.shutdownDrainStarted && a.runtimeErrKind == runtimeNoticeRuntimeShutdown {
				ui.Text(c, a.runtimeErrorDisplay(msgs)).MinWidth(0).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
				primaryButton(c, msgs.RecoveryQuitAction()).AlignSelf(ui.Start).OnClick(func() { a.handleBeforeQuit(nil) })
				return
			}
			pageHeading(c, msgs.RuntimeStatusDraining())
			ui.Text(c, msgs.RuntimeDraining()).MinWidth(0).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		})
	})
}
