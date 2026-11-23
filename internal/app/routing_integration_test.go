package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func containsText(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func openBotProfileFromRail(t *testing.T, tr *ui.Tester, label string) {
	t.Helper()
	if err := chooseBotProfileFromRail(tr, label); err != nil {
		t.Fatal(err)
	}
}

func chooseBotProfileFromRail(tr *ui.Tester, label string) error {
	if err := tr.RightClick(label); err != nil {
		return err
	}
	if err := tr.ChooseMenuItem(locales.In("en").ContactProfileAction()); err != nil {
		return fmt.Errorf("open %q profile: %w", label, err)
	}
	tr.Frame()
	return nil
}

func clickBotProfileAction(tr *ui.Tester, label string) error {
	width, height := tr.Image().Bounds().Dx(), tr.Image().Bounds().Dy()
	for range 12 {
		if row, found := tr.Find(label); found && row.W > 0 && row.H > 0 && row.Y >= 0 && row.Y+row.H <= float32(height) {
			return tr.Click(label)
		}
		tr.Scroll(float32(width-40), float32(height/2), 0, 210)
		tr.Frame()
	}
	return fmt.Errorf("Bot profile action %q is outside the viewport", label)
}

func TestBotRailSecondaryClickTargetsNonselectedBot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		width   int
		compact bool
	}{{"full row padding", 1280, false}, {"compact row slot", 1040, true}} {
		t.Run(tc.name, func(t *testing.T) {
			isolateConnectionApp(t, t.TempDir(), "")
			app, err := newRuntimeApp()
			if err != nil {
				t.Fatal(err)
			}
			app.windowWidth = tc.width
			app.closeSettings()
			first := app.selectedContact()
			app.addDefaultContact()
			second := app.selectedContact()
			first.Draft, second.Draft = "First unsent draft", "Second unsent draft"
			app.selectContact(0)
			if tc.compact {
				app.openContactProfile(first.ID)
			}
			tr := ui.NewTester(app.view, tc.width, 720)
			tr.SetLocale("en")
			tr.SetFocused(true)
			tr.Frame()
			msgs := locales.In("en")
			label := msgs.AvatarContactLabel(second.Name)
			row, ok := tr.Find(label)
			if !ok || app.avatarRailActive() != tc.compact || row.W < 32 || row.H < 32 {
				t.Fatalf("missing Bot row in intended rail: %+v, compact=%t", row, app.avatarRailActive())
			}
			x, y := row.X+row.W/2, row.Y+row.H/2
			if !tc.compact {
				x = row.X + row.W - 12
			}
			tr.RightClickAt(x, y)
			if app.selectedContact() != first {
				t.Fatal("secondary click changed the selected chat")
			}
			if err := tr.ChooseMenuItem(msgs.ContactProfileAction()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if app.settingsOpen || app.selectedContact() != first || !tr.HasText(msgs.FieldNameAccessible()) {
				t.Fatalf("the profile replaced Settings or switched the chat: %v", tr.Texts())
			}
			if err := tr.Click(msgs.FieldNameAccessible()); err != nil {
				t.Fatal(err)
			}
			tr.Key(ui.Cmd, ui.KeyA)
			tr.Type("Saved second Bot")
			tr.Key(ui.Cmd, ui.KeyZ)
			tr.Frame()
			if editor := app.profileEditorFor(second.ID); editor == nil || editor.Draft.Name != second.Name {
				t.Fatal("undo did not restore the second Bot's profile draft")
			}
			tr.Key(ui.Cmd|ui.Shift, ui.KeyZ)
			if err := clickBotProfileAction(tr, msgs.ContactSaveChanges()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if second.Name != "Saved second Bot" || first.Name != "Nagi" || app.selectedContact() != first {
				t.Fatalf("profile save targeted the wrong Bot: first=%q second=%q selected=%q", first.Name, second.Name, app.selectedID())
			}
			if err := tr.Click(msgs.ProfileClose()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			label = msgs.AvatarContactLabel(second.Name)
			if !tr.Focused(label) || !tr.HasText(msgs.ComposerLabel(first.Name)) {
				t.Fatalf("Back did not restore the originating Bot row and original chat: %v", tr.Texts())
			}
			if err := tr.Click(label); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if app.selectedContact() != second || !tr.HasText(msgs.ComposerLabel(second.Name)) || first.Draft != "First unsent draft" || second.Draft != "Second unsent draft" {
				t.Fatalf("left click or chat drafts changed: %v", tr.Texts())
			}
		})
	}
}
