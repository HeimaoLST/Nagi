package app

import (
	"testing"

	"github.com/egoist/mygo"
)

func TestNativeWindowCloseHideAndReopen(t *testing.T) {
	hides, shows, focuses, appFocuses, dockBuilds := 0, 0, 0, 0, 0
	var latestMenu *mygo.Menu
	a := &appModel{
		contacts:    []*contact{{ID: "bot-1", Name: "Bot One"}},
		selected:    0,
		uiLocale:    "en",
		hideWindow:  func() { hides++ },
		showWindow:  func() { shows++ },
		focusWindow: func() { focuses++ },
		focusApp:    func() { appFocuses++ },
		setDockMenu: func(menu *mygo.Menu) {
			dockBuilds++
			latestMenu = menu
		},
	}

	ev := &mygo.CloseEvent{}
	a.handleWindowClose(ev)
	if !ev.DefaultPrevented() || !a.windowHidden || hides != 1 || dockBuilds != 1 || latestMenu == nil {
		t.Fatalf("first handleWindowClose = prevented:%v hidden:%v hides:%d builds:%d", ev.DefaultPrevented(), a.windowHidden, hides, dockBuilds)
	}

	ev2 := &mygo.CloseEvent{}
	a.handleWindowClose(ev2)
	if !ev2.DefaultPrevented() || hides != 1 || dockBuilds != 1 {
		t.Fatalf("duplicate handleWindowClose = prevented:%v hides:%d builds:%d", ev2.DefaultPrevented(), hides, dockBuilds)
	}

	a.handleAppActivate(true)
	if a.windowHidden || shows != 1 || focuses != 1 || appFocuses != 1 || dockBuilds != 2 {
		t.Fatalf("handleAppActivate = hidden:%v shows:%d focuses:%d appFocuses:%d builds:%d",
			a.windowHidden, shows, focuses, appFocuses, dockBuilds)
	}

	a.quitReady = true
	ev3 := &mygo.CloseEvent{}
	a.handleWindowClose(ev3)
	if ev3.DefaultPrevented() {
		t.Fatal("handleWindowClose prevented close after quitReady")
	}
}

func TestDockMenuStateDeduplicationAndGuards(t *testing.T) {
	dockBuilds := 0
	var lastMenu *mygo.Menu
	a := &appModel{
		contacts: []*contact{
			{ID: "pandorabox", SystemKind: "bot_architect"},
			{ID: "bot-1", Name: "Bot One"},
			{ID: "bot-2", Name: "Bot Two", Archived: true},
		},
		selected:    0,
		uiLocale:    "zh-Hans",
		setDockMenu: func(menu *mygo.Menu) { dockBuilds++; lastMenu = menu },
	}

	a.refreshDockMenu()
	if dockBuilds != 1 || a.dockMenuKey.locale != "zh-Hans" || a.dockMenuKey.botID != "" {
		t.Fatalf("builtin selection dock state = builds:%d key:%+v", dockBuilds, a.dockMenuKey)
	}
	a.refreshDockMenu()
	if dockBuilds != 1 {
		t.Fatalf("unchanged state rebuilt dock menu %d times", dockBuilds)
	}

	a.selected = 1
	a.refreshDockMenu()
	if dockBuilds != 2 || a.dockMenuKey.botID != "bot-1" || lastMenu == nil || len(lastMenu.Items()) < 3 {
		t.Fatalf("user bot dock menu = builds:%d key:%+v items:%d", dockBuilds, a.dockMenuKey, len(lastMenu.Items()))
	}

	a.shutdownStarted = true
	a.refreshDockMenu()
	if !a.dockMenuKey.draining || a.dockMenuKey.botID != "" {
		t.Fatalf("draining state did not clear bot actions: %+v", a.dockMenuKey)
	}
}
