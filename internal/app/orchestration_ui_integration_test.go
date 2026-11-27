//go:build integration

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func clickInBackgroundWorkScroll(w *productWorkbench, label string) {
	w.t.Helper()
	var err error
	var visible []string
	w.pump.do(func() {
		if w.view.FramePending() {
			w.view.Frame()
		}
		panel := w.app.backgroundWork.panel
		if panel == nil {
			err = fmt.Errorf("background work panel closed before clicking %q", label)
			return
		}
		scroll := &panel.listScroll
		if panel.detailID != "" {
			scroll = &panel.detailScroll
		}
		offsets := []float32{scroll.Y}
		for y := float32(0); y <= 1800; y += 140 {
			offsets = append(offsets, y)
		}
		for _, y := range offsets {
			scroll.Y = y
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
		w.t.Fatalf("clickInBackgroundWorkScroll %q: %v, visible=%v", label, err, visible)
	}
}

func waitInBackgroundDetailScroll(w *productWorkbench, ctx context.Context, wanted ...string) {
	w.t.Helper()
	found := make(map[string]bool, len(wanted))
	for ctx.Err() == nil {
		w.pump.do(func() {
			if w.view.FramePending() {
				w.view.Frame()
			}
			panel := w.app.backgroundWork.panel
			if panel == nil {
				return
			}
			for y := float32(0); y <= 2000; y += 160 {
				panel.detailScroll.Y = y
				w.view.Frame()
				for _, target := range wanted {
					if w.view.HasText(target) {
						found[target] = true
					}
				}
			}
		})
		if len(found) == len(wanted) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	w.t.Fatalf("timed out waiting for background detail texts %v, found=%v", wanted, found)
}

func TestPiAppOrchestrationAndTimeline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	copperEntered := make(chan struct{})
	releaseCopper := make(chan struct{})
	var mu sync.Mutex
	var onceCopper bool
	var resolvedPayloads []string

	provider := newCPProvider(t, nil)
	provider.setHandler(func(req cpProviderRequest) cpProviderReply {
		if req.LastRole == "tool" {
			return cpProviderReply{Text: "Done"}
		}
		if strings.Contains(req.LastUser, "HOLD_COPPER") {
			mu.Lock()
			first := !onceCopper
			onceCopper = true
			mu.Unlock()
			if first {
				close(copperEntered)
			}
			return cpProviderReply{Release: releaseCopper, Text: "Copper settled."}
		}
		if strings.Contains(req.LastUser, "<orchestration_event>") {
			switch {
			case strings.Contains(req.LastUser, "decision_resolved"):
				mu.Lock()
				resolvedPayloads = append(resolvedPayloads, req.LastUser)
				mu.Unlock()
				return cpProviderReply{
					Calls: []cpToolCall{
						{
							ID:   "call-notify-resolved",
							Name: "conversation_notify",
							Args: map[string]any{
								"text":    "Approved option publish received; monitoring post-release.",
								"urgency": "normal",
							},
						},
						{
							ID:   "call-finish-resolved",
							Name: "orchestration_finish",
							Args: map[string]any{
								"summary":           "Recorded human approval.",
								"observedStateHash": "wake-resolved",
								"progressMade":      true,
							},
						},
					},
				}
			case strings.Contains(req.LastUser, "Approval gate timer"):
				return cpProviderReply{
					Calls: []cpToolCall{
						{
							ID:   "call-open-decision",
							Name: "orchestration_request_decision",
							Args: map[string]any{
								"kind":    "approval",
								"purpose": "Approve release checklist",
								"summary": "Confirm whether to publish release notes.",
								"options": []map[string]any{
									{"id": "publish", "label": "Publish notes"},
									{"id": "hold", "label": "Hold draft"},
								},
							},
						},
						{
							ID:   "call-finish-decision",
							Name: "orchestration_finish",
							Args: map[string]any{
								"summary":           "Requested human decision.",
								"observedStateHash": "wake-decision",
								"progressMade":      true,
							},
						},
					},
				}
			case strings.Contains(req.LastUser, "Publish draft reminder"):
				return cpProviderReply{
					Calls: []cpToolCall{
						{
							ID:   "call-notify-urgent",
							Name: "conversation_notify",
							Args: map[string]any{
								"text":    "Checklist smoke checks passed; release notes need sign-off.",
								"urgency": "high",
							},
						},
						{
							ID:   "call-finish-urgent",
							Name: "orchestration_finish",
							Args: map[string]any{
								"summary":           "Published release notice.",
								"observedStateHash": "wake-notice",
								"progressMade":      true,
							},
						},
					},
				}
			}
			return cpProviderReply{
				Calls: []cpToolCall{
					{
						ID:   "call-finish-default",
						Name: "orchestration_finish",
						Args: map[string]any{
							"summary":           "No action required.",
							"observedStateHash": "wake-default",
							"progressMade":      false,
						},
					},
				},
			}
		}
		return cpProviderReply{Text: "OK"}
	})

	root := prepareProductWorkbench(t, provider)
	cfgPath := connectionConfigPath(guiStorePath())
	cfg, err := loadConnectionConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxActiveTasks = 1
	if err := saveConnectionConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	if err := writeProfileJSON(guiStorePath(), storedState{
		Version:  contactStoreVersion,
		Selected: "cedar",
		Contacts: []storedContact{
			{ID: "cedar", Name: "Cedar", Workspace: root},
			{ID: "copper", Name: "Copper", Workspace: root, Draft: "HOLD_COPPER"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	msgs := locales.In("en")
	w := openProductWorkbench(t)
	var cedar, copper *contact
	w.pump.do(func() {
		cedar = w.app.contactByID("cedar")
		copper = w.app.contactByID("copper")
	})
	if cedar == nil || copper == nil {
		t.Fatalf("missing contacts cedar=%v copper=%v", cedar, copper)
	}
	w.wait(func() bool { return cedar.Loaded && w.app.selectedContact() == cedar })

	w.click("Copper")
	w.wait(func() bool {
		return w.app.selectedContact() == copper && copper.Loaded && copper.Draft == "HOLD_COPPER"
	})
	w.click(msgs.SendAction())
	select {
	case <-copperEntered:
	case <-ctx.Done():
		t.Fatal("timed out waiting for copper foreground turn to occupy single runtime slot")
	}

	w.click("Cedar")
	w.wait(func() bool { return w.app.selectedContact() == cedar })
	w.click(msgs.ComposerLabel("Cedar"))
	w.frame(func() { w.view.Type("Check rollout checklist") })
	w.click(msgs.SendAction())
	w.wait(func() bool {
		return cedar.SendState == sendQueued && w.view.HasText(msgs.SendQueuedPosition(1)) &&
			cedar.ActiveTaskID != "" && w.app.submissionInFlight == 0 && w.view.HasText(msgs.StopConversation())
	})
	var queuedHasRead bool
	var queuedDraft string
	w.pump.do(func() {
		queuedHasRead = w.view.HasText(msgs.MessageReadStatus())
		queuedDraft = cedar.Draft
	})
	if queuedHasRead || queuedDraft != "Check rollout checklist" {
		t.Fatalf("queued send invariant failed: hasRead=%v draft=%q", queuedHasRead, queuedDraft)
	}

	w.click(msgs.StopConversation())
	w.wait(func() bool {
		return cedar.SendState == sendIdle && cedar.ActiveTaskID == "" &&
			w.view.HasText(msgs.SendCancelledBeforeDispatch()) && cedar.Draft == "Check rollout checklist"
	})
	var cancelledHasRead bool
	w.pump.do(func() {
		cancelledHasRead = w.view.HasText(msgs.MessageReadStatus())
	})
	if cancelledHasRead {
		t.Fatal("pre-dispatch cancelled send must not mark Read")
	}

	close(releaseCopper)
	w.wait(func() bool { return copper.SendState == sendIdle && copper.Status != statusWorking })

	yes := true
	if _, err := w.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		OperationID: newContactID(),
		BotID:       "cedar",
		Kind:        pi.ProductWatchTimer,
		Purpose:     "Publish draft reminder",
		Prompt:      "Inspect smoke tests",
		Activate:    &yes,
		Timer: &pi.ProductTimerSpec{
			FireAt:       time.Now().UTC().Add(500 * time.Millisecond).Format("2006-01-02T15:04:05.000Z"),
			MissedPolicy: pi.ProductMissedFireOnceCoalesced,
		},
	}); err != nil {
		t.Fatalf("save urgent notice timer: %v", err)
	}

	w.wait(func() bool {
		return w.view.HasText("Checklist smoke checks passed; release notes need sign-off.") &&
			w.view.HasText(msgs.TimelineNoticeUrgent())
	})

	const decisionTimerPurpose = "Approval gate timer\nRequest release approval"
	if _, err := w.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		OperationID: newContactID(),
		BotID:       "cedar",
		Kind:        pi.ProductWatchTimer,
		Purpose:     "Approval gate timer",
		Prompt:      "Request release approval",
		Activate:    &yes,
		Timer: &pi.ProductTimerSpec{
			FireAt:       time.Now().UTC().Add(500 * time.Millisecond).Format("2006-01-02T15:04:05.000Z"),
			MissedPolicy: pi.ProductMissedFireOnceCoalesced,
		},
	}); err != nil {
		t.Fatalf("save decision timer: %v", err)
	}

	w.wait(func() bool {
		return w.view.HasText("Approve release checklist") &&
			w.view.HasText("Publish notes") &&
			w.view.HasText(msgs.OrchestrationHeaderCount(2))
	})

	w.click("Publish notes")
	w.click(msgs.OrchestrationNotePlaceholder())
	w.frame(func() { w.view.Type("Ship after tag check") })
	w.click(msgs.OrchestrationApprove())

	w.wait(func() bool {
		return w.view.HasText("Approved option publish received; monitoring post-release.") &&
			!w.view.HasText("Approve release checklist")
	})

	mu.Lock()
	resolvedSnapshot := append([]string(nil), resolvedPayloads...)
	mu.Unlock()
	if len(resolvedSnapshot) == 0 || !strings.Contains(resolvedSnapshot[0], "publish") || !strings.Contains(resolvedSnapshot[0], "Ship after tag check") {
		t.Fatalf("decision resolution payload missing option/note: %v", resolvedSnapshot)
	}

	const scheduleMandatePurpose = "Continuous rollout watch\nWatch rollout"
	interval := 300
	if _, err := w.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		OperationID: newContactID(),
		BotID:       "cedar",
		Kind:        pi.ProductWatchSchedule,
		Purpose:     "Continuous rollout watch",
		Prompt:      "Watch rollout",
		Activate:    &yes,
		Schedule: &pi.ProductScheduleSpec{
			CadenceKind:     pi.ProductScheduleInterval,
			IntervalSeconds: &interval,
			TimeZone:        "UTC",
		},
	}); err != nil {
		t.Fatalf("save schedule watch: %v", err)
	}

	headerCount3 := msgs.OrchestrationHeaderCount(3)
	w.wait(func() bool {
		return w.app.selectedContact() == cedar && w.view.HasText(headerCount3)
	})
	w.click(headerCount3)
	w.wait(func() bool {
		return w.app.backgroundWork.panel != nil &&
			w.view.HasText(decisionTimerPurpose) &&
			w.view.HasText(scheduleMandatePurpose)
	})

	clickInBackgroundWorkScroll(w, msgs.DetailsAction()+" "+decisionTimerPurpose)
	w.wait(func() bool {
		return w.app.backgroundWork.panel != nil && w.app.backgroundWork.panel.detail != nil
	})
	waitInBackgroundDetailScroll(w, ctx, msgs.OrchestrationApproved(), msgs.OrchestrationRunsTitle(), msgs.OrchestrationReceiptsTitle(), "orchestration_request_decision", "conversation_notify")

	w.click(msgs.OrchestrationBack())
	w.wait(func() bool {
		return w.app.backgroundWork.panel != nil && w.app.backgroundWork.panel.detailID == "" && w.view.HasText(scheduleMandatePurpose)
	})

	clickInBackgroundWorkScroll(w, msgs.OrchestrationStop()+" "+scheduleMandatePurpose)
	w.wait(func() bool {
		return w.view.HasText(msgs.OrchestrationStateStopped())
	})

	w.frame(func() { w.view.Key(0, ui.KeyEscape) })
	w.wait(func() bool { return w.app.backgroundWork.panel == nil })

	w.wait(func() bool {
		if w.view.FramePending() {
			w.view.Frame()
		}
		list, err := w.client.ListOrchestrations(ctx, pi.ProductOrchestrationListParams{BotID: "cedar"})
		if err != nil {
			return false
		}
		for _, item := range list.Items {
			if item.CurrentRunID != "" {
				return false
			}
		}
		return cedar.SendState == sendIdle && copper.SendState == sendIdle
	})

	backupPath := filepath.Join(t.TempDir(), "bg3b-v14-backup.json")
	exported, err := w.client.BackupProfile(ctx, backupPath)
	if err != nil {
		t.Fatalf("BackupProfile: %v", err)
	}
	copied, err := inspectStoreRecoveryCopy(exported)
	if err != nil {
		t.Fatalf("inspectStoreRecoveryCopy: %v", err)
	}
	if copied.Notices < 2 || copied.Decisions < 1 {
		b, _ := json.Marshal(copied)
		t.Fatalf("expected v14 backup notices >= 2 and decisions >= 1, got %+v (%s)", copied, b)
	}

	w.shutdown(ctx)
}
