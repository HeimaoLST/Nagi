package app

import (
	"os"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestCreateBotDialogLifecycleValidationDismissalAndRetry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		width   int
		compact bool
	}{
		{"full rail", 1280, false},
		{"compact rail", 1040, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			isolateConnectionApp(t, root, "")
			a, err := newRuntimeApp()
			if err != nil {
				t.Fatal(err)
			}
			a.windowWidth = tc.width
			a.closeSettings()
			origin := a.selectedContact()
			origin.Draft = "Keep original draft"
			if tc.compact {
				a.openContactProfile(origin.ID)
			}
			tr := ui.NewTester(a.view, tc.width, 720)
			tr.SetLocale("en")
			tr.SetFocused(true)
			tr.Frame()
			msgs := locales.In("en")
			if a.avatarRailActive() != tc.compact {
				t.Fatalf("compact = %t, want %t", a.avatarRailActive(), tc.compact)
			}

			for _, dismiss := range []string{"cancel", "escape", "backdrop"} {
				if err := tr.Click(msgs.AvatarAddContact()); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if a.createBot == nil || !tr.HasText(msgs.CreateBotTitle()) || !tr.HasText(msgs.CreateBotAction()) {
					t.Fatalf("%s: Create Bot dialog did not open: %v", dismiss, tr.Texts())
				}
				if a.createBot.name != msgs.NewContactName(len(a.contacts)+1) {
					t.Fatalf("%s: default name = %q", dismiss, a.createBot.name)
				}
				switch dismiss {
				case "cancel":
					if err := tr.Click(msgs.CreateBotCancel()); err != nil {
						t.Fatal(err)
					}
				case "escape":
					tr.Key(0, ui.KeyEscape)
				case "backdrop":
					tr.ClickAt(8, 8)
				}
				tr.Frame()
				if a.createBot != nil || len(a.contacts) != 1 || origin.Draft != "Keep original draft" {
					t.Fatalf("%s: dismiss left modal open or mutated contacts/draft", dismiss)
				}
				if !tr.Focused(msgs.AvatarAddContact()) {
					t.Fatalf("%s: dismiss did not return focus to Add Bot", dismiss)
				}
			}

			if err := tr.Click(msgs.AvatarAddContact()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			tr.Key(ui.Cmd, ui.KeyA)
			tr.Type("   ")
			tr.Frame()
			if err := tr.Click(msgs.CreateBotAction()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.createBot == nil || !a.createBot.invalidName || !tr.HasText(msgs.ValidationNameRequired()) || !tr.Focused(msgs.FieldNameAccessible()) || len(a.contacts) != 1 {
				t.Fatalf("blank name was not blocked and refocused: texts=%v", tr.Texts())
			}

			tr.Key(ui.Cmd, ui.KeyA)
			tr.Type("  Scout Bot  ")
			tr.Frame()
			tr.Key(0, ui.KeyTab)
			tr.Frame()
			tr.Type("  Audit changes  ")
			tr.Frame()

			if err := os.MkdirAll(a.storePath, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := tr.Click(msgs.CreateBotAction()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.createBot == nil || !a.createBot.failed || !tr.HasText(msgs.CreateBotFailed()) || len(a.contacts) != 1 {
				t.Fatalf("store failure did not retain dialog and show error: %v", tr.Texts())
			}
			if a.createBot.name != "  Scout Bot  " || a.createBot.purpose != "  Audit changes  " {
				t.Fatalf("store failure lost user draft: %+v", a.createBot)
			}
			firstOpID := a.createBot.operationID
			if firstOpID == "" {
				t.Fatal("failed create did not capture an operation ID")
			}

			if err := os.Remove(a.storePath); err != nil {
				t.Fatal(err)
			}
			if err := tr.Click(msgs.CreateBotAction()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.createBot != nil || len(a.contacts) != 2 {
				t.Fatalf("retry after unblocking did not create Bot: createBot=%+v contacts=%d", a.createBot, len(a.contacts))
			}
			created := a.selectedContact()
			if created.Name != "Scout Bot" || created.Purpose != "Audit changes" || created.Role != "" || created.Workspace != "" || created.BuiltinTools != nil {
				t.Fatalf("created Bot fields = %+v", created)
			}
			if a.activeDetail() != "" {
				t.Fatalf("confirmed create left detail open: %q", a.activeDetail())
			}
			if !tr.Focused(msgs.ComposerLabel(created.Name)) {
				t.Fatalf("confirmed create did not focus new Bot composer: %v", tr.Texts())
			}
			if origin.Draft != "Keep original draft" {
				t.Fatalf("creating new Bot altered existing Bot draft: %q", origin.Draft)
			}
		})
	}
}

func TestBotRowPopoverMenuActionsOnFullAndCompactRails(t *testing.T) {
	for _, tc := range []struct {
		name    string
		width   int
		compact bool
	}{
		{"full rail", 1280, false},
		{"compact rail", 1040, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			a.windowWidth = tc.width
			first := a.contacts[0]
			second := a.contacts[1]
			first.Draft = "Keep first draft"
			second.SessionID = "sess-second"
			second.Messages = []*message{{ID: 1, Kind: msgUser, Text: "prior turn"}}
			second.Issues = []*productIssue{{ID: "act-1", Goal: "Track release", Status: issueStatusNotStarted}}
			second.activity.loaded = true
			a.selectContact(0)
			if tc.compact {
				a.openContactProfile(first.ID)
			}
			tr := ui.NewTester(a.view, tc.width, 720)
			tr.SetLocale("en")
			tr.SetFocused(true)
			tr.Frame()
			msgs := locales.In("en")
			rowLabel := msgs.AvatarContactLabel(second.Name)
			if a.avatarRailActive() != tc.compact {
				t.Fatalf("compact = %t, want %t", a.avatarRailActive(), tc.compact)
			}

			if err := tr.RightClick(rowLabel); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			for _, want := range []string{msgs.BotMenuActivities(1), msgs.ContactProfileAction(), msgs.BotMenuArchive()} {
				if !tr.HasText(want) {
					t.Fatalf("popover menu missing %q: %v", want, tr.Texts())
				}
			}
			for _, absent := range []string{"New chat", "Previous chats", msgs.BotMenuStopReply(), "Copy workspace", "Open workspace", "Show in Finder"} {
				if tr.HasText(absent) {
					t.Fatalf("idle popover menu unexpectedly showed %q: %v", absent, tr.Texts())
				}
			}

			if err := choosePopoverMenuItem(tr, msgs.ContactProfileAction()); err != nil {
				t.Fatal(err)
			}
			if a.selectedContact() != first || a.contactProfileID != second.ID {
				t.Fatalf("Bot profile menu item switched chat or opened wrong Bot: selected=%q profile=%q", a.selectedID(), a.contactProfileID)
			}
			editor := a.profileEditorFor(second.ID)
			editor.Draft.Purpose = "Unsaved profile edit"

			if err := tr.RightClick(rowLabel); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if err := choosePopoverMenuItem(tr, msgs.BotMenuArchive()); err != nil {
				t.Fatal(err)
			}
			if !second.Archived || second.SessionID != "sess-second" || len(second.Messages) != 1 || editor.Draft.Purpose != "Unsaved profile edit" {
				t.Fatalf("Archive Bot failed or altered conversation/draft: archived=%t session=%q msgs=%d purpose=%q", second.Archived, second.SessionID, len(second.Messages), editor.Draft.Purpose)
			}

			if err := tr.RightClick(rowLabel); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tr.HasText(msgs.RestoreContactAction()) || tr.HasText(msgs.BotMenuArchive()) {
				t.Fatalf("archived Bot menu did not switch to Restore Bot: %v", tr.Texts())
			}
			if err := choosePopoverMenuItem(tr, msgs.RestoreContactAction()); err != nil {
				t.Fatal(err)
			}
			if second.Archived {
				t.Fatal("Restore Bot menu item did not restore target Bot")
			}

			second.ActiveTaskID = "active-run-1"
			second.SendState = sendAccepted
			tr.Frame()
			if err := tr.RightClick(rowLabel); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tr.HasText(msgs.BotMenuStopReply()) {
				t.Fatalf("busy Bot menu missing Stop reply: %v", tr.Texts())
			}
			tr.Key(0, ui.KeyEscape)
			tr.Frame()
			second.ActiveTaskID = ""
			second.SendState = sendIdle
			tr.Frame()

			if err := tr.RightClick(rowLabel); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if err := choosePopoverMenuItem(tr, msgs.BotMenuActivities(1)); err != nil {
				t.Fatal(err)
			}
			if a.selectedContact() != second || !second.activity.open || first.Draft != "Keep first draft" {
				t.Fatalf("Activities menu item did not select Bot and open activities: selected=%q open=%t", a.selectedID(), second.activity.open)
			}
		})
	}
}
