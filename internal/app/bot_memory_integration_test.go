//go:build integration

package app

import (
	"context"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestPiAppBotMemoryProfileActionsAndSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var providerCalls atomic.Int32
	provider := &piAppProvider{limit: 4}
	if err := provider.install([]piAppCall{
		{id: "call-save", name: "memory_save_rule", args: func(string) map[string]any { return map[string]any{"text": "Use UTC in release notes."} }},
		{id: "call-prop", name: "memory_propose_learning", args: func(string) map[string]any {
			return map[string]any{"text": "Put decisions before caveats.", "reason": "Based on your release note feedback."}
		}},
		{id: "call-log", name: "memory_log_work", args: func(string) map[string]any {
			return map[string]any{"summary": "发布核验完成，证据已记录。", "date": "2026-11-18", "tags": []string{"release"}}
		}},
	}, "Saved release details.", nil, nil); err != nil {
		t.Fatal(err)
	}
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		provider.ServeHTTP(w, r)
	})
	root := prepareProductWorkbench(t, counted)
	profile := guiStorePath()
	if err := writeProfileJSON(profile, storedState{
		Version:  contactStoreVersion,
		Selected: "auditor",
		Contacts: []storedContact{
			{ID: "auditor", Name: "Release Auditor", Workspace: root, Draft: "Save release details."},
		},
	}); err != nil {
		t.Fatal(err)
	}

	w := openProductWorkbench(t)
	msgs := locales.In("en")
	auditor := w.app.contactByID("auditor")
	w.wait(func() bool { return w.app.selectedContact() == auditor && auditor.Loaded })
	w.click(msgs.SendAction())
	w.wait(func() bool { return auditor.SendState == sendIdle && auditor.PendingLearnings == 1 })
	callsAfterTurn := providerCalls.Load()

	w.frame(func() {
		if err := w.view.RightClick(msgs.AvatarContactLabel("Release Auditor")); err != nil {
			t.Errorf("right click auditor: %v", err)
		}
	})
	w.wait(func() bool { return w.view.HasText(msgs.MemoryProfileAction(1)) })
	w.click(msgs.MemoryProfileAction(1))
	w.wait(func() bool { return w.app.detailIs(detailProfile) && auditor.memory.loaded })
	w.frame(func() { w.app.profileScrollFor(auditor.ID).Y = 1000 })

	var archiveRect ui.Rect
	w.pump.do(func() {
		r, ok := w.view.Find(msgs.ProfileArchive())
		if !ok || r.H < 32 {
			t.Errorf("Archive footer missing: %+v ok=%v", r, ok)
		}
		archiveRect = r
	})

	w.click(msgs.MemoryApprove())
	w.wait(func() bool {
		return auditor.PendingLearnings == 0 && len(auditor.memory.data.PendingLearnings) == 0 && len(auditor.memory.data.Rules) == 2
	})

	w.click(msgs.MemoryAddPlaceholder())
	w.frame(func() { w.view.Type("Never store sk-live1234567890 here") })
	w.click(msgs.MemoryAdd())
	w.wait(func() bool {
		return !auditor.memory.busy && auditor.memory.problem == "memory_secret" && w.view.HasText(msgs.MemoryErrorSecret())
	})

	w.click(msgs.MemoryAddPlaceholder())
	w.frame(func() {
		w.view.Command("selectAll")
		w.view.Type("Cite commit SHAs.")
	})
	w.click(msgs.MemoryAdd())
	w.wait(func() bool { return !auditor.memory.busy && len(auditor.memory.data.Rules) == 3 })

	w.click(msgs.MemoryEdit())
	w.wait(func() bool { return auditor.memory.editRuleID != "" })
	w.click(msgs.MemoryEditRule())
	w.frame(func() {
		w.view.Command("selectAll")
		w.view.Type("Use ISO UTC timestamps in release notes.")
	})
	w.click(msgs.MemorySave())
	w.wait(func() bool {
		return !auditor.memory.busy && auditor.memory.editRuleID == "" &&
			slices.ContainsFunc(auditor.memory.data.Rules, func(r pi.ProductMemoryRule) bool { return r.Text == "Use ISO UTC timestamps in release notes." })
	})

	w.click(msgs.MemoryRemoveRule())
	w.wait(func() bool { return !auditor.memory.busy && len(auditor.memory.data.Rules) == 2 })

	w.click(msgs.MemorySearchPlaceholder())
	w.frame(func() { w.view.Type("核验完成") })
	w.wait(func() bool {
		return !auditor.memory.searching && len(auditor.memory.matches) == 1 && auditor.memory.matches[0].Date == "2026-11-18"
	})

	w.click(msgs.MemoryRemoveLog())
	w.wait(func() bool { return !auditor.memory.busy && len(auditor.memory.data.WorkLogs) == 0 })

	w.pump.do(func() {
		afterRect, ok := w.view.Find(msgs.ProfileArchive())
		if !ok || afterRect != archiveRect {
			t.Errorf("Archive footer moved across memory actions: before=%+v after=%+v", archiveRect, afterRect)
		}
	})
	if providerCalls.Load() != callsAfterTurn {
		t.Fatalf("UI memory actions triggered unexpected provider calls: before=%d after=%d", callsAfterTurn, providerCalls.Load())
	}
	w.shutdown(ctx)
}
