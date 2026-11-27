//go:build integration

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
	"github.com/HeimaoLST/Nagi/locales"
)

func clickInScheduleScroll(w *productWorkbench, botID, label string) {
	w.t.Helper()
	var err error
	var visible []string
	w.pump.do(func() {
		if w.view.FramePending() {
			w.view.Frame()
		}
		b := w.app.schedules.bot(botID)
		offsets := []float32{b.scroll.Y}
		for y := float32(0); y <= 1600; y += 110 {
			offsets = append(offsets, y)
		}
		for _, y := range offsets {
			b.scroll.Y = y
			w.view.Frame()
			if clickErr := w.view.Click(label); clickErr == nil {
				w.view.Frame()
				err = nil
				return
			} else {
				err = clickErr
			}
		}
		visible = w.view.Texts()
	})
	if err != nil {
		w.t.Fatalf("clickInScheduleScroll %q: %v, visible=%v", label, err, visible)
	}
}

func clickInProfileScroll(w *productWorkbench, botID, label string) {
	w.t.Helper()
	var err error
	var visible []string
	w.pump.do(func() {
		if w.view.FramePending() {
			w.view.Frame()
		}
		sc := w.app.profileScrollFor(botID)
		offsets := []float32{sc.Y}
		for y := float32(0); y <= 1600; y += 200 {
			offsets = append(offsets, y)
		}
		for _, y := range offsets {
			sc.Y = y
			w.view.Frame()
			if clickErr := w.view.Click(label); clickErr == nil {
				w.view.Frame()
				err = nil
				return
			} else {
				err = clickErr
			}
		}
		visible = w.view.Texts()
	})
	if err != nil {
		w.t.Fatalf("clickInProfileScroll %q: %v, visible=%v", label, err, visible)
	}
}

func TestPiAppScheduledWorkAndGrants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	provider := newCPProvider(t, func(req cpProviderRequest) cpProviderReply {
		return cpProviderReply{Text: "OK"}
	})
	root := prepareProductWorkbench(t, provider)
	precheckScript := filepath.Join(root, "check-precheck.sh")
	if err := os.WriteFile(precheckScript, []byte("#!/bin/sh\necho \"PRECHECK_EXEC_OK\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(precheckScript); err == nil {
		precheckScript = resolved
	}
	tools := []string{"bash", "read", "edit", "write"}
	if err := writeProfileJSON(guiStorePath(), storedState{
		Version:  contactStoreVersion,
		Selected: "cedar",
		Contacts: []storedContact{
			{ID: "cedar", Name: "Cedar", Workspace: root, BuiltinTools: &tools, Draft: "Seeded chat"},
			{ID: "copper", Name: "Copper", Workspace: root, BuiltinTools: &tools},
		},
	}); err != nil {
		t.Fatal(err)
	}

	msgs := locales.In("en")
	w := openProductWorkbench(t)
	var cedar *contact
	w.pump.do(func() { cedar = w.app.contactByID("cedar") })
	w.wait(func() bool { return cedar != nil && cedar.Loaded })

	w.frame(func() { w.app.openContactProfile("cedar") })
	clickInProfileScroll(w, "cedar", msgs.SchedWorkOpenAction())
	w.wait(func() bool {
		return w.app.scheduledWorkBotID == "cedar" && w.view.HasText("No scheduled work in this view.")
	})

	w.click("New timer or schedule")
	w.wait(func() bool { return w.view.HasText("Save and activate") })

	clickInScheduleScroll(w, "cedar", "Name")
	w.frame(func() {
		w.view.Command("selectAll")
		w.view.Type("Followup timer")
	})
	clickInScheduleScroll(w, "cedar", "Purpose")
	w.frame(func() {
		w.view.Command("selectAll")
		w.view.Type("Follow up audit")
	})
	clickInScheduleScroll(w, "cedar", "Fire at (with time zone offset)")
	w.frame(func() {
		w.view.Command("selectAll")
		w.view.Type("2030-12-01T09:00:00+08:00")
	})
	clickInScheduleScroll(w, "cedar", "Save and activate")

	w.wait(func() bool {
		return w.view.HasText("Scheduled work saved and activated.") &&
			w.view.HasText("Followup timer") &&
			w.view.HasText("Pause")
	})

	var timerID string
	var timerRev uint64
	w.pump.do(func() {
		d := w.app.schedules.bot("cedar").detail.value
		timerID, timerRev = d.WatchID, d.Revision
	})

	w.click("Pause")
	w.wait(func() bool {
		return w.view.HasText("Scheduled work paused.") && w.view.HasText("Paused")
	})
	w.pump.do(func() {
		timerRev = w.app.schedules.bot("cedar").detail.value.Revision
	})

	w.click(msgs.BackLabel())
	w.wait(func() bool {
		return w.app.schedules.bot("cedar").watchID == "" && w.view.HasText("Followup timer")
	})

	w.click("New timer or schedule")
	w.wait(func() bool { return w.view.HasText("Save and activate") })
	clickInScheduleScroll(w, "cedar", "Timer")
	w.wait(func() bool { return w.view.HasText("Recurring schedule") })
	w.click("Recurring schedule")
	w.wait(func() bool { return w.view.HasText("Precheck") })

	clickInScheduleScroll(w, "cedar", "None")
	w.wait(func() bool { return w.view.HasText("Executable") })
	w.click("Executable")
	w.wait(func() bool { return w.view.HasText("Executable path") })

	clickInScheduleScroll(w, "cedar", "Name")
	w.frame(func() {
		w.view.Command("selectAll")
		w.view.Type("Precheck monitor")
	})
	clickInScheduleScroll(w, "cedar", "Purpose")
	w.frame(func() {
		w.view.Command("selectAll")
		w.view.Type("Verify workspace precheck")
	})
	clickInScheduleScroll(w, "cedar", "Executable path")
	w.frame(func() {
		w.view.Command("selectAll")
		w.view.Type(precheckScript)
	})
	clickInScheduleScroll(w, "cedar", "Allowed arguments (JSON array)")
	w.frame(func() {
		w.view.Command("selectAll")
		w.view.Type("[]")
	})
	clickInScheduleScroll(w, "cedar", "Review local grant")

	w.wait(func() bool {
		return w.view.HasText("Installation-local standing grant") && w.view.HasText("Approve local grant and activate")
	})
	clickInScheduleScroll(w, "cedar", "Approve local grant and activate")
	w.wait(func() bool {
		return w.view.HasText("Scheduled work saved and activated.") && w.view.HasText("Precheck monitor")
	})

	w.frame(func() { w.app.openBotScheduledWork("copper") })
	w.wait(func() bool {
		return w.app.scheduledWorkBotID == "copper" && w.view.HasText("No scheduled work in this view.")
	})
	w.click("Standing grants")
	w.wait(func() bool { return w.view.HasText("No local standing grants.") })

	w.frame(func() { w.app.openBotScheduledWork("cedar") })
	w.wait(func() bool { return w.app.scheduledWorkBotID == "cedar" && w.view.HasText("Precheck monitor") })
	w.click(msgs.BackLabel())
	w.wait(func() bool {
		return w.app.schedules.bot("cedar").watchID == "" && w.view.HasText("Standing grants")
	})
	w.click("Standing grants")
	w.wait(func() bool { return w.view.HasText("Verify workspace precheck") })
	w.click("Verify workspace precheck")
	w.wait(func() bool { return w.view.HasText("Revoke grant") })
	clickInScheduleScroll(w, "cedar", "Revoke grant")
	w.wait(func() bool { return w.view.HasText("Confirm revocation") })
	clickInScheduleScroll(w, "cedar", "Confirm revocation")
	w.wait(func() bool {
		return w.view.HasText("Grant revoked. Dependent watches invalidated: 1.") &&
			w.view.HasText("Open invalidated watch 1")
	})
	clickInScheduleScroll(w, "cedar", "Open invalidated watch 1")
	w.wait(func() bool {
		return w.view.HasText("Invalidated") && w.view.HasText("Standing grant revoked")
	})

	backupPath := filepath.Join(t.TempDir(), "bg2-backup.json")
	exported, err := w.client.BackupProfile(ctx, backupPath)
	if err != nil {
		t.Fatalf("BackupProfile: %v", err)
	}
	copied, err := inspectStoreRecoveryCopy(exported)
	if err != nil {
		t.Fatalf("inspectStoreRecoveryCopy: %v", err)
	}
	if copied.Timers < 1 || copied.Schedules < 1 || copied.DormantWatches < 1 {
		t.Fatalf("unexpected recovery backup watch counts: %+v", copied)
	}

	w.frame(func() {
		w.app.openSettingsTab(settingsTabAppearance, settingsSourceCommand)
		s := w.app.ensureDataRecovery()
		s.target = &storeRecoveryTarget{}
		s.canonical = w.app.storePath
		s.viewGeneration = w.app.folderChoiceGeneration
		s.owner = w.app.selectedContact()
		s.copy = copied
		s.scopeOpen = true
		for y := float32(0); y <= 2200; y += 220 {
			w.app.settingsScroll.Y = y
			w.view.Frame()
			if w.view.HasText(msgs.RecoveryPreviewWatchCounts(copied.Timers, copied.Schedules, copied.DormantWatches)) {
				break
			}
		}
	})
	if !w.view.HasText(msgs.RecoveryPreviewWatchCounts(copied.Timers, copied.Schedules, copied.DormantWatches)) || !w.view.HasText(msgs.RecoveryScopeWatches()) {
		t.Fatalf("Settings recovery preview did not render localized watch counts or dormant warning: %v", w.view.Texts())
	}
	w.frame(func() {
		s := w.app.ensureDataRecovery()
		s.copy, s.target = nil, nil
		w.app.closeSettings()
	})

	w.shutdown(ctx)

	uiState, err := readProductUI(guiStorePath())
	if err != nil {
		t.Fatal(err)
	}
	if uiState == nil {
		uiState = &productUIState{}
	}
	uiState.PendingWatchOperations = []store.PendingWatchOperation{{
		ID:       "op-bg2-never-sent",
		BotID:    "cedar",
		WatchID:  timerID,
		Revision: timerRev,
		Action:   "cancel",
	}}
	if err := writeProfileJSON(productUIPath(guiStorePath()), uiState); err != nil {
		t.Fatal(err)
	}

	w2 := openProductWorkbench(t)
	w2.wait(func() bool {
		b := w2.app.schedules.bot("cedar")
		return b.operation != nil && !b.working && strings.HasPrefix(b.operation.id, "op-bg2-never-sent") && len(w2.app.schedules.pendingOperations()) == 1
	})
	watchAfterRestart, err := w2.client.GetWatch(ctx, "cedar", timerID)
	if err != nil || watchAfterRestart.State != pi.ProductWatchPaused {
		t.Fatalf("restart reconciliation replayed cancel operation against timer: %+v, err=%v", watchAfterRestart, err)
	}
	w2.frame(func() { w2.app.openBotScheduledWork("cedar") })
	w2.wait(func() bool {
		return w2.app.scheduledWorkBotID == "cedar" && w2.view.HasText("Check saved result")
	})
	w2.frame(func() { w2.view.Key(0, ui.KeyEscape) })
	w2.wait(func() bool { return w2.app.scheduledWorkBotID == "" && w2.app.detail == detailProfile })
	w2.shutdown(ctx)
}
