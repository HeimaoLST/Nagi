//go:build integration

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func bg1SessionFixtureScript() string {
	return `import fs from 'node:fs';
const mode = process.argv[2];
if (mode === 'pty') {
  console.log('PTY_LIVE_READY:' + String(process.stdout.isTTY));
  setInterval(() => {}, 1000);
} else {
  const triggerPath = process.argv[3];
  for (let i = 1; i <= 420; i++) {
    console.log('OUT_LINE_' + String(i).padStart(3, '0') + '_' + 'X'.repeat(48));
  }
  console.error('STDERR_BANNER_1');
  const poller = setInterval(() => {
    if (fs.existsSync(triggerPath)) {
      console.log('LIVE_TAIL_FINAL');
      clearInterval(poller);
    }
  }, 25);
  setInterval(() => {}, 1000);
}
`
}

func TestPiAppSessionInspectionAndClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var turn atomic.Int32
	var scriptPath, triggerPath string
	nodePath := os.Getenv("NAGI_TEST_NODE")

	provider := newCPProvider(t, func(req cpProviderRequest) cpProviderReply {
		switch turn.Add(1) {
		case 1:
			return cpProviderReply{
				Calls: []cpToolCall{{
					ID:   "call-open-pipe",
					Name: "session_open",
					Args: map[string]any{
						"command": nodePath,
						"args":    []string{scriptPath, "pipe", triggerPath},
						"mode":    "pipe",
						"label":   "Pipe worker",
					},
				}},
			}
		case 2:
			return cpProviderReply{
				Calls: []cpToolCall{{
					ID:   "call-open-pty",
					Name: "session_open",
					Args: map[string]any{
						"command": nodePath,
						"args":    []string{scriptPath, "pty"},
						"mode":    "pty",
						"label":   "PTY console",
					},
				}},
			}
		default:
			return cpProviderReply{Text: "Sessions ready."}
		}
	})

	root := prepareProductWorkbench(t, provider)
	scriptPath = filepath.Join(root, "session-fixture.mjs")
	triggerPath = filepath.Join(root, "emit-tail")
	if err := os.WriteFile(scriptPath, []byte(bg1SessionFixtureScript()), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := []string{"bash", "read", "edit", "write"}
	if err := writeProfileJSON(guiStorePath(), storedState{
		Version:  contactStoreVersion,
		Selected: "cedar",
		Contacts: []storedContact{{ID: "cedar", Name: "Cedar", Workspace: root, BuiltinTools: &tools, Draft: "Open live pipe and pty sessions"}},
	}); err != nil {
		t.Fatal(err)
	}

	msgs := locales.In("en")
	w := openProductWorkbench(t)
	var cedar *contact
	w.pump.do(func() { cedar = w.app.contactByID("cedar") })
	w.wait(func() bool { return cedar != nil && cedar.Loaded })

	if len(cedar.activeActivities) != 0 {
		t.Fatalf("expected zero initial activities, got %d", len(cedar.activeActivities))
	}

	w.click(msgs.SessionInspectTitle())
	w.wait(func() bool {
		return w.app.sessions.activeBotID == "cedar" && w.view.HasText(msgs.SessionInspectEmptyList())
	})
	w.frame(func() { w.view.Key(0, ui.KeyEscape) })
	w.wait(func() bool { return w.app.sessions.activeBotID == "" && w.view.HasText(msgs.SendAction()) })

	w.click(msgs.SendAction())
	w.wait(func() bool { return cedar.SendState == sendIdle && len(cedar.History) >= 2 })

	w.click(msgs.SessionInspectTitle())
	w.wait(func() bool {
		return w.app.sessions.activeBotID == "cedar" &&
			w.view.HasText("Pipe worker") &&
			w.view.HasText("PTY console")
	})

	w.click("Pipe worker, " + msgs.SessionInspectRunning())
	w.wait(func() bool {
		bot := w.app.sessions.bots["cedar"]
		return bot != nil && bot.detail != nil && bot.detail.output.loaded &&
			strings.Contains(bot.detail.output.projectedText(pi.ProductSessionPipe, defaultSessionInspectionCopy), "OUT_LINE_420_") &&
			strings.Contains(bot.detail.output.projectedText(pi.ProductSessionPipe, defaultSessionInspectionCopy), "STDERR_BANNER_1") &&
			w.view.HasText(msgs.SessionInspectEarlierOutput())
	})

	var pipeSessID string
	var pipeGen uint64
	var projectedInitial string
	w.pump.do(func() {
		detail := w.app.sessions.bots["cedar"].detail
		pipeSessID = detail.summary.SessionID
		pipeGen = detail.summary.Generation
		projectedInitial = detail.output.projectedText(pi.ProductSessionPipe, defaultSessionInspectionCopy)
	})
	if !strings.Contains(projectedInitial, "[stdout]\n") || !strings.Contains(projectedInitial, "[stderr]\n") {
		t.Fatalf("missing pipe stream labels in projected output: %q", projectedInitial)
	}

	w.click(msgs.SessionInspectEarlierOutput())
	w.wait(func() bool {
		detail := w.app.sessions.bots["cedar"].detail
		return detail != nil && detail.output.browsing && !detail.output.loading &&
			strings.Contains(detail.output.projectedText(pi.ProductSessionPipe, defaultSessionInspectionCopy), "OUT_LINE_001_")
	})

	if err := os.WriteFile(triggerPath, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.wait(func() bool {
		return w.view.HasText(msgs.SessionInspectNewOutput()) &&
			w.view.HasText(msgs.SessionInspectLatestOutput())
	})
	w.pump.do(func() {
		detail := w.app.sessions.bots["cedar"].detail
		if !detail.output.browsing {
			t.Fatal("browsing mode was snapped back to tail on new output")
		}
		if strings.Contains(detail.output.projectedText(pi.ProductSessionPipe, defaultSessionInspectionCopy), "LIVE_TAIL_FINAL") {
			t.Fatal("live tail output overwrote browsed earlier slice")
		}
	})

	w.click(msgs.SessionInspectLatestOutput())
	w.wait(func() bool {
		detail := w.app.sessions.bots["cedar"].detail
		return detail != nil && !detail.output.browsing &&
			strings.Contains(detail.output.projectedText(pi.ProductSessionPipe, defaultSessionInspectionCopy), "LIVE_TAIL_FINAL")
	})

	w.click(msgs.SessionInspectSourceChat())
	w.wait(func() bool {
		return w.app.priorChats != nil && w.view.HasText(msgs.ReadOnlyChat()) && w.view.HasText("Sessions ready.")
	})

	w.frame(func() { w.view.Key(0, ui.KeyEscape) })
	w.wait(func() bool {
		return w.app.priorChats == nil && w.app.sessions.activeBotID == "cedar" &&
			w.app.sessions.bots["cedar"].detail != nil &&
			w.app.sessions.bots["cedar"].detail.summary.SessionID == pipeSessID
	})

	w.frame(func() { w.view.Key(0, ui.KeyEscape) })
	w.wait(func() bool {
		return w.app.sessions.activeBotID == "cedar" &&
			w.app.sessions.bots["cedar"].detail == nil &&
			w.view.HasText("PTY console")
	})

	w.click("PTY console, " + msgs.SessionInspectRunning())
	w.wait(func() bool {
		detail := w.app.sessions.bots["cedar"].detail
		return detail != nil && detail.ready && detail.output.loaded &&
			strings.Contains(detail.output.projectedText(pi.ProductSessionPTY, defaultSessionInspectionCopy), "PTY_LIVE_READY:true") &&
			w.view.HasText(msgs.SessionInspectKill())
	})

	w.click(msgs.SessionInspectKill())
	w.wait(func() bool {
		detail := w.app.sessions.bots["cedar"].detail
		return detail != nil && detail.summary.State == pi.ProductSessionClosed &&
			w.view.HasText(msgs.SessionInspectKillTerminated())
	})
	var ptySessID string
	var ptyGen uint64
	var ptyCloseParams pi.ProductSessionCloseParams
	w.pump.do(func() {
		detail := w.app.sessions.bots["cedar"].detail
		ptySessID = detail.summary.SessionID
		ptyGen = detail.summary.Generation
		ptyCloseParams = w.app.sessions.bots["cedar"].closes[sessionRun{ptySessID, ptyGen}].params
	})

	w.click(msgs.SessionInspectBack())
	w.wait(func() bool {
		return w.app.sessions.bots["cedar"].detail == nil && w.view.HasText("PTY console") && w.view.HasText("Pipe worker")
	})

	pipeCurrent, err := w.client.GetSession(ctx, "cedar", pipeSessID)
	if err != nil || pipeCurrent.State != pi.ProductSessionRunning {
		t.Fatalf("killing PTY session affected sibling pipe session: %+v, err=%v", pipeCurrent, err)
	}

	w.pump.do(func() {
		w.app.sessions.restoreSessionCloses([]pi.ProductSessionCloseParams{{
			BotID:       "cedar",
			SessionID:   pipeSessID,
			Generation:  pipeGen,
			OperationID: "op-bg1-live-reconcile-unknown",
		}})
		w.app.sessions.refreshSessionInspection()
	})
	w.wait(func() bool {
		intent := w.app.sessions.botSession("cedar").closes[sessionRun{pipeSessID, pipeGen}]
		return intent != nil && intent.status == "killUnknown" && !intent.checking
	})
	pipeAfterReconcile, err := w.client.GetSession(ctx, "cedar", pipeSessID)
	if err != nil || pipeAfterReconcile.State != pi.ProductSessionRunning {
		t.Fatalf("live reconcile replayed session.close against running pipe session: %+v, err=%v", pipeAfterReconcile, err)
	}
	w.click("Pipe worker, " + msgs.SessionInspectRunning())
	w.wait(func() bool {
		return w.app.sessions.bots["cedar"].detail != nil &&
			w.view.HasText(msgs.SessionInspectKillUnknown()) &&
			w.view.HasText(msgs.SessionInspectCheckClose())
	})

	w.shutdown(ctx)

	uiState, err := readProductUI(guiStorePath())
	if err != nil {
		t.Fatal(err)
	}
	if uiState == nil {
		uiState = &productUIState{}
	}
	uiState.PendingSessionCloses = []pi.ProductSessionCloseParams{
		ptyCloseParams,
		{
			BotID:       "cedar",
			SessionID:   pipeSessID,
			Generation:  pipeGen,
			OperationID: "op-bg1-restart-reconcile-unknown",
		},
	}
	if err := writeProfileJSON(productUIPath(guiStorePath()), uiState); err != nil {
		t.Fatal(err)
	}

	w2 := openProductWorkbench(t)
	w2.wait(func() bool {
		bot := w2.app.sessions.botSession("cedar")
		ptyIntent := bot.closes[sessionRun{ptySessID, ptyGen}]
		pipeIntent := bot.closes[sessionRun{pipeSessID, pipeGen}]
		return ptyIntent != nil && !ptyIntent.checking && ptyIntent.status == "killTerminated" &&
			pipeIntent != nil && !pipeIntent.checking && pipeIntent.status == "killUnknown" &&
			len(w2.app.sessions.pendingSessionCloses()) == 1
	})
	afterRestart, err := w2.client.GetSession(ctx, "cedar", pipeSessID)
	if err != nil || afterRestart.TransportStatus == pi.ProductTransportClosedByUser {
		t.Fatalf("restart reconcile altered session end reason via auto-replay: %+v, err=%v", afterRestart, err)
	}
	w2.click(msgs.SessionInspectTitle())
	w2.wait(func() bool {
		return w2.app.sessions.activeBotID == "cedar" && w2.view.HasText("Pipe worker")
	})
	w2.click("Pipe worker, " + msgs.SessionInspectClosed())
	w2.wait(func() bool {
		return w2.app.sessions.bots["cedar"].detail != nil &&
			w2.view.HasText(msgs.SessionInspectKillUnknown()) &&
			w2.view.HasText(msgs.SessionInspectCheckClose())
	})
	w2.shutdown(ctx)
}
