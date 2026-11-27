//go:build integration

package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func bg3aPTYChildScript() string {
	return `process.stdout.write('PTY_IDS:' + String(process.pid) + ':' + String(process.ppid) + '\n');
setInterval(() => {}, 1000);
`
}

func findDockMenuItem(menu *mygo.Menu, id string) *mygo.MenuItem {
	for _, item := range menu.Items() {
		if item.ID == id {
			return item
		}
	}
	return nil
}

func installNativeLifecycleTestHooks(app *appModel) (hideCount, showCount, focusCount, appFocusCount, quitCount *atomic.Int32, dockMenus **mygo.Menu) {
	hideCount = new(atomic.Int32)
	showCount = new(atomic.Int32)
	focusCount = new(atomic.Int32)
	appFocusCount = new(atomic.Int32)
	quitCount = new(atomic.Int32)
	var latestDock *mygo.Menu
	dockMenus = &latestDock
	app.hideWindow = func() { hideCount.Add(1) }
	app.showWindow = func() { showCount.Add(1) }
	app.focusWindow = func() { focusCount.Add(1) }
	app.focusApp = func() { appFocusCount.Add(1) }
	app.setDockMenu = func(menu *mygo.Menu) { latestDock = menu }
	app.requestQuit = func() { quitCount.Add(1) }
	app.refreshDockMenu()
	return hideCount, showCount, focusCount, appFocusCount, quitCount, dockMenus
}

func workerOSPID(t *testing.T, storePath string) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", storePath).Output()
	if err != nil {
		t.Fatalf("pgrep worker for %s: %v", storePath, err)
	}
	for line := range strings.Lines(strings.TrimSpace(string(out))) {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && pid > 0 {
			return pid
		}
	}
	t.Fatalf("no positive worker PID in pgrep output %q", string(out))
	return 0
}

func assertProcessReaped(t *testing.T, name string, pid int) {
	t.Helper()
	if pid <= 0 {
		t.Fatalf("invalid %s PID %d", name, pid)
	}
	err := syscall.Kill(pid, 0)
	if err != syscall.ESRCH {
		t.Fatalf("%s pid %d still exists after shutdown: err=%v", name, pid, err)
	}
}

func assertProcessAlive(t *testing.T, name string, pid int) {
	t.Helper()
	if pid <= 0 {
		t.Fatalf("invalid %s PID %d", name, pid)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("%s pid %d not alive: %v", name, pid, err)
	}
}

func prepareNativeLifecycleCase(t *testing.T, sessionEnding bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var turn atomic.Int32
	var scriptPath string
	nodePath := os.Getenv("NAGI_TEST_NODE")
	heldSend, releaseSend := make(chan struct{}), make(chan struct{})

	provider := newCPProvider(t, func(req cpProviderRequest) cpProviderReply {
		if !sessionEnding && strings.Contains(req.LastUser, "Hold explicit quit") {
			return cpProviderReply{
				Entered: heldSend,
				Release: releaseSend,
				Text:    "Released after quit drain started.",
			}
		}
		switch turn.Add(1) {
		case 1:
			return cpProviderReply{
				Calls: []cpToolCall{{
					ID:   "call-open-bg3a-pty",
					Name: "session_open",
					Args: map[string]any{
						"command": nodePath,
						"args":    []string{scriptPath},
						"mode":    "pty",
						"label":   "BG3A PTY",
					},
				}},
			}
		default:
			return cpProviderReply{Text: "PTY started."}
		}
	})

	root := prepareProductWorkbench(t, provider)
	scriptPath = filepath.Join(root, "bg3a-pty.mjs")
	if err := os.WriteFile(scriptPath, []byte(bg3aPTYChildScript()), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := []string{"bash", "read", "edit", "write"}
	if err := writeProfileJSON(guiStorePath(), storedState{
		Version:  contactStoreVersion,
		Selected: "cedar",
		Contacts: []storedContact{
			{ID: "cedar", Name: "Cedar", Workspace: root, BuiltinTools: &tools, Draft: "Start BG3A session"},
			{ID: "copper", Name: "Copper", Workspace: root, BuiltinTools: &tools},
		},
	}); err != nil {
		t.Fatal(err)
	}

	msgs := locales.In("en")
	w := openProductWorkbench(t)
	var cedar *contact
	var originalClient *pi.ProductClient
	var hideCount, showCount, focusCount, appFocusCount, quitCount *atomic.Int32
	var dockMenuPtr **mygo.Menu
	w.pump.do(func() {
		cedar = w.app.contactByID("cedar")
		originalClient = w.app.pi.product
		hideCount, showCount, focusCount, appFocusCount, quitCount, dockMenuPtr = installNativeLifecycleTestHooks(w.app)
	})
	w.wait(func() bool { return cedar != nil && cedar.Loaded })

	workerPID := workerOSPID(t, guiStorePath())
	assertProcessAlive(t, "worker", workerPID)

	w.click(msgs.SendAction())
	w.wait(func() bool { return cedar.SendState == sendIdle && len(cedar.History) >= 2 })

	var childPID, supervisorPID int
	w.wait(func() bool {
		page, err := w.client.ListSessions(ctx, pi.ProductSessionListParams{BotID: "cedar"})
		if err != nil || len(page.Items) == 0 {
			return false
		}
		read, err := w.client.ReadSession(ctx, pi.ProductSessionReadParams{
			BotID:      "cedar",
			SessionID:  page.Items[0].SessionID,
			Generation: page.Items[0].Generation,
		})
		if err != nil {
			return false
		}
		for _, chunk := range read.Chunks {
			for line := range strings.Lines(chunk.Text) {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "PTY_IDS:"); ok {
					parts := strings.Split(rest, ":")
					if len(parts) == 2 {
						childPID, _ = strconv.Atoi(parts[0])
						supervisorPID, _ = strconv.Atoi(parts[1])
					}
				}
			}
		}
		return childPID > 0 && supervisorPID > 0
	})
	assertProcessAlive(t, "pty supervisor", supervisorPID)
	assertProcessAlive(t, "pty child", childPID)

	activate := true
	receipt, err := w.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID:       "cedar",
		Kind:        pi.ProductWatchTimer,
		Label:       "Hidden timer",
		Purpose:     "Survive window close",
		OperationID: "op-bg3a-watch",
		Activate:    &activate,
		Timer: &pi.ProductTimerSpec{
			FireAt:       time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			MissedPolicy: pi.ProductMissedSkip,
		},
	})
	if err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	watchID := receipt.Target.WatchID

	quoteIdx := 0
	var firstVisibleBefore, lastVisibleBefore int
	w.frame(func() {
		cedar.Draft = "Preserved draft across hide"
		if _, err := cedar.Context.AddSnippet("Pinned evidence", "Keep across hide"); err != nil {
			t.Fatal(err)
		}
		cedar.DraftQuote = &pi.ProductQuote{MessageID: "q-1", BubbleIndex: &quoteIdx, Sender: "contact", AuthorName: "Cedar", Text: "PTY started."}
		cedar.list.HoldPlace()
		cedar.list.ScrollTo(0, ui.Start)
	})
	var checkErr string
	w.pump.do(func() {
		w.view.Frame()
		firstVisibleBefore, lastVisibleBefore = cedar.list.Visible()
		ev := &mygo.CloseEvent{}
		w.app.handleWindowClose(ev)
		if !ev.DefaultPrevented() {
			checkErr = "handleWindowClose did not prevent default window close before quitReady"
			return
		}
		w.view.Frame()
		if hideCount.Load() != 1 || !w.app.windowHidden || w.app.quitReady || w.app.shutdownStarted {
			checkErr = fmt.Sprintf("window close did not hide cleanly: hides=%d hidden=%v quitReady=%v shutdown=%v",
				hideCount.Load(), w.app.windowHidden, w.app.quitReady, w.app.shutdownStarted)
			return
		}
		firstAfterHide, lastAfterHide := cedar.list.Visible()
		if w.app.pi == nil || w.app.pi.product != originalClient || w.app.selectedContact() != cedar ||
			cedar.Draft != "Preserved draft across hide" || cedar.Context.Count() != 1 ||
			cedar.DraftQuote == nil || firstAfterHide != firstVisibleBefore || lastAfterHide != lastVisibleBefore {
			checkErr = fmt.Sprintf("window hide mutated live composer, scroll, or client state: draft=%q materials=%d quote=%v visible=(%d,%d)!=(%d,%d)",
				cedar.Draft, cedar.Context.Count(), cedar.DraftQuote, firstAfterHide, lastAfterHide, firstVisibleBefore, lastVisibleBefore)
		}
	})
	if checkErr != "" {
		t.Fatal(checkErr)
	}
	assertProcessAlive(t, "worker while hidden", workerPID)
	assertProcessAlive(t, "supervisor while hidden", supervisorPID)
	assertProcessAlive(t, "pty child while hidden", childPID)
	watchWhileHidden, err := w.client.GetWatch(ctx, "cedar", watchID)
	if err != nil || watchWhileHidden.State != pi.ProductWatchActive {
		t.Fatalf("watch stopped while window hidden: %+v, err=%v", watchWhileHidden, err)
	}

	w.pump.do(func() {
		w.app.handleAppActivate(false)
		w.view.Frame()
		if w.app.windowHidden || showCount.Load() != 1 || focusCount.Load() != 1 || appFocusCount.Load() != 1 {
			checkErr = fmt.Sprintf("handleAppActivate did not reopen and focus hidden window: hidden=%v show=%d focus=%d appFocus=%d",
				w.app.windowHidden, showCount.Load(), focusCount.Load(), appFocusCount.Load())
		}
	})
	if checkErr != "" {
		t.Fatal(checkErr)
	}

	w.pump.do(func() {
		w.view.SetLocale("zh-Hans")
		w.view.Frame()
		menu := *dockMenuPtr
		openItem := findDockMenuItem(menu, "open-nagi")
		schedItem := findDockMenuItem(menu, "scheduled-work")
		sessItem := findDockMenuItem(menu, "sessions")
		zhMsgs := locales.In("zh-Hans")
		if openItem == nil || openItem.Label != zhMsgs.DockOpenNagi() ||
			schedItem == nil || schedItem.Label != zhMsgs.SchedWorkTitle() ||
			sessItem == nil || sessItem.Label != zhMsgs.SessionInspectTitle() {
			checkErr = fmt.Sprintf("zh-Hans dock menu mismatch: open=%+v sched=%+v sess=%+v", openItem, schedItem, sessItem)
			return
		}
		ev := &mygo.CloseEvent{}
		w.app.handleWindowClose(ev)
		schedItem.Click(schedItem, nil)
		w.view.Frame()
		if w.app.windowHidden || w.app.scheduledWorkBotID != "cedar" {
			checkErr = fmt.Sprintf("dock Scheduled work item did not reopen and navigate: hidden=%v schedBot=%q", w.app.windowHidden, w.app.scheduledWorkBotID)
		}
	})
	if checkErr != "" {
		t.Fatal(checkErr)
	}

	var staleMenu *mygo.Menu
	w.pump.do(func() {
		w.view.SetLocale("en")
		w.view.Frame()
		staleMenu = *dockMenuPtr
		sessItem := findDockMenuItem(staleMenu, "sessions")
		sessItem.Click(sessItem, nil)
		w.view.Frame()
		if w.app.sessions.activeBotID != "cedar" || w.app.scheduledWorkBotID != "" {
			checkErr = fmt.Sprintf("dock Sessions item did not open Cedar sessions: activeBot=%q schedBot=%q", w.app.sessions.activeBotID, w.app.scheduledWorkBotID)
		}
	})
	if checkErr != "" {
		t.Fatal(checkErr)
	}

	w.pump.do(func() {
		w.app.escapeTopLayer()
		copperIdx := slices.IndexFunc(w.app.contacts, func(ct *contact) bool { return ct.ID == "copper" })
		cedarIdx := slices.IndexFunc(w.app.contacts, func(ct *contact) bool { return ct.ID == "cedar" })
		w.app.selectContact(copperIdx)
		w.view.Frame()
		staleSched := findDockMenuItem(staleMenu, "scheduled-work")
		staleSched.Click(staleSched, nil)
		w.view.Frame()
		if w.app.scheduledWorkBotID != "" {
			checkErr = fmt.Sprintf("stale dock menu item routed to Cedar after switching to Copper: schedBot=%q", w.app.scheduledWorkBotID)
			return
		}
		w.app.selectContact(cedarIdx)
		w.view.Frame()
	})
	if checkErr != "" {
		t.Fatal(checkErr)
	}

	if !sessionEnding {
		w.pump.do(func() {
			cedar.Context.Items = nil
			cedar.DraftQuote = nil
			cedar.Draft = "Hold explicit quit"
			w.app.handleWindowClose(&mygo.CloseEvent{})
			w.view.Frame()
		})
		w.click(msgs.SendAction())
		select {
		case <-heldSend:
		case <-ctx.Done():
			t.Fatal("held explicit-quit turn did not reach provider")
		}
		w.pump.do(func() {
			ev := &mygo.QuitEvent{SessionEnding: false}
			w.app.handleBeforeQuit(ev)
			if !ev.DefaultPrevented() {
				checkErr = "explicit handleBeforeQuit did not prevent immediate exit while worker was active"
				return
			}
			w.view.Frame()
			if w.app.windowHidden || !w.app.shutdownStarted || w.app.quitReady {
				checkErr = fmt.Sprintf("explicit quit did not reopen hidden window and begin draining: hidden=%v shutdown=%v quitReady=%v",
					w.app.windowHidden, w.app.shutdownStarted, w.app.quitReady)
				return
			}
			drainingTester := ui.NewTesterWith(w.app.nativeWindowView, 1040, 720, ui.TesterOptions{})
			drainingTester.SetLocale("en")
			drainingTester.Frame()
			if !drainingTester.HasText(msgs.RuntimeStatusDraining()) || !drainingTester.HasText(msgs.RuntimeDraining()) {
				checkErr = fmt.Sprintf("draining view missing expected status text: %v", drainingTester.Texts())
				return
			}
			w.app.send(cedar, "Should be fenced during shutdown")
			if quitCount.Load() != 0 {
				checkErr = fmt.Sprintf("requestQuit fired before held submission finished: calls=%d", quitCount.Load())
			}
		})
		if checkErr != "" {
			t.Fatal(checkErr)
		}

		close(releaseSend)
		w.pump.wait(func() bool { return w.app.quitReady && quitCount.Load() == 1 })
		assertProcessReaped(t, "worker after explicit quit", workerPID)
		assertProcessReaped(t, "supervisor after explicit quit", supervisorPID)
		assertProcessReaped(t, "pty child after explicit quit", childPID)

		w.pump.do(func() {
			qEv := &mygo.QuitEvent{SessionEnding: false}
			w.app.handleBeforeQuit(qEv)
			if qEv.DefaultPrevented() {
				checkErr = "second handleBeforeQuit prevented exit after quitReady became true"
				return
			}
			cEv := &mygo.CloseEvent{}
			w.app.handleWindowClose(cEv)
			if cEv.DefaultPrevented() {
				checkErr = "handleWindowClose prevented window close after quitReady became true"
			}
		})
		if checkErr != "" {
			t.Fatal(checkErr)
		}
		return
	}

	w.pump.do(func() {
		w.app.providers.healthTimer = time.NewTimer(time.Hour)
		ev := &mygo.QuitEvent{SessionEnding: true}
		w.app.handleBeforeQuit(ev)
		if w.app.providers.healthTimer != nil {
			checkErr = "SessionEnding did not stop provider healthTimer"
			return
		}
		if !w.app.quitReady || !w.app.sessionEnding || w.app.pi != nil || quitCount.Load() != 0 {
			checkErr = fmt.Sprintf("SessionEnding did not synchronously drain worker without deferred quit: ready=%v sessionEnding=%v pi=%v quits=%d",
				w.app.quitReady, w.app.sessionEnding, w.app.pi, quitCount.Load())
			return
		}
		cEv := &mygo.CloseEvent{}
		w.app.handleWindowClose(cEv)
		if cEv.DefaultPrevented() {
			checkErr = "handleWindowClose prevented close during SessionEnding"
		}
	})
	if checkErr != "" {
		t.Fatal(checkErr)
	}
	assertProcessReaped(t, "worker after session ending", workerPID)
	assertProcessReaped(t, "supervisor after session ending", supervisorPID)
	assertProcessReaped(t, "pty child after session ending", childPID)
}

func TestPiAppNativeLifecycleHideAndQuit(t *testing.T) {
	t.Run("explicit_quit", func(t *testing.T) {
		prepareNativeLifecycleCase(t, false)
	})
	t.Run("session_ending", func(t *testing.T) {
		prepareNativeLifecycleCase(t, true)
	})
}
