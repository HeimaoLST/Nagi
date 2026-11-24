//go:build integration

package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestPiProductPandoraBoxVisibilityPersistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	provider := &piAppProvider{limit: 4}
	if err := provider.install([]piAppCall{
		{id: "call-log-1", name: "memory_log_work", args: func(string) map[string]any {
			return map[string]any{"summary": "Oldest log entry", "date": "2026-11-16", "tags": []string{"ops"}}
		}},
		{id: "call-log-2", name: "memory_log_work", args: func(string) map[string]any {
			return map[string]any{"summary": "Middle release check", "date": "2026-11-18", "tags": []string{"ops"}}
		}},
		{id: "call-log-3", name: "memory_log_work", args: func(string) map[string]any {
			return map[string]any{"summary": "Newest verification pass", "date": "2026-11-20", "tags": []string{"ops"}}
		}},
	}, "Logged work entries.", nil, nil); err != nil {
		t.Fatal(err)
	}
	root := prepareProductWorkbench(t, provider)
	profile := guiStorePath()
	if err := writeProfileJSON(profile, storedState{
		Version:  contactStoreVersion,
		Selected: "auditor",
		Contacts: []storedContact{
			{ID: "auditor", Name: "Release Auditor", Purpose: "Track launch readiness", Workspace: root, Draft: "Log recent checks"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	w := openProductWorkbench(t)
	msgs := locales.In("en")
	auditor := w.app.contactByID("auditor")
	w.wait(func() bool { return w.app.selectedContact() == auditor && auditor.Loaded })
	w.click(msgs.SendAction())
	w.wait(func() bool { return auditor.SendState == sendIdle && len(auditor.History) == 2 })

	w.frame(func() { w.app.setBuiltinArchitectHidden(true) })
	w.wait(func() bool { return w.app.hideBuiltinArchitect })
	if _, err := w.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			BotID: auditor.ID, ExpectedRevision: auditor.ProfileRevision, OperationID: "op-new-conv-empty",
		},
		Profile: pi.ProductBotProfile{
			Name: auditor.Name, Role: auditor.Role, Purpose: auditor.Purpose, Instructions: auditor.Instructions, Workspace: auditor.Workspace,
		},
		StartNewConversation: true,
	}); err != nil {
		t.Fatal(err)
	}
	w.shutdown(ctx)

	reopened := openProductWorkbench(t)
	reopenedAuditor := reopened.app.contactByID("auditor")
	reopened.wait(func() bool {
		return reopened.app.selectedContact() == reopenedAuditor && reopenedAuditor.Loaded && reopenedAuditor.memory.loaded
	})
	reopened.pump.do(func() {
		if !reopened.app.hideBuiltinArchitect {
			t.Fatal("HideBuiltinArchitect did not persist across reopen")
		}
		if slices.ContainsFunc(reopened.app.visibleContactIndices(), func(i int) bool { return reopened.app.contacts[i].isBuiltin() }) {
			t.Fatal("hidden PandoraBox reappeared in rail after reopen")
		}
		want := []string{
			msgs.StarterFollowUp("Newest verification pass"),
			msgs.StarterFollowUp("Middle release check"),
			msgs.StarterHelpWithPurpose("Track launch readiness"),
		}
		got := starterPromptsFor(reopenedAuditor, msgs)
		if !slices.Equal(got, want) {
			t.Fatalf("workLog+purpose fallback after reopen = %q, want %q", got, want)
		}
	})
	reopened.shutdown(ctx)
}
