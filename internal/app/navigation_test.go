package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestResizeDoesNotStealSettingsFocus(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	a.detail = ""
	a.openContactProfile(a.selectedID())
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen || !tr.Focused("← Back to workbench") {
		t.Fatal("Settings did not open with its own Back focused")
	}
	a.windowWidth = 720
	tr.SetSize(720, 640)
	tr.Frame()
	if !a.settingsOpen || !tr.Focused("← Back to workbench") {
		t.Fatalf("resize stole Settings focus: settings=%v texts=%v", a.settingsOpen, tr.Texts())
	}
	if tr.HasText("Composer for Nagi") || tr.HasText(locales.In("en").AvatarAddContact()) {
		t.Fatal("Settings resize mounted the workbench background")
	}
}

func TestAvatarSettingsResizeBackFocus(t *testing.T) {
	t.Run("roundtrip-back-to-settings", func(t *testing.T) {
		a, _ := workspaceTestApp(t)
		a.windowWidth = 960
		a.openContactProfile(a.selectedID())
		tr := ui.NewTester(a.view, 960, 640)
		tr.Frame()
		if err := tr.Click("Settings"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if !a.settingsOpen || !tr.Focused("← Back to workbench") {
			t.Fatal("avatar Settings did not open with Settings focus")
		}
		a.windowWidth = 899
		tr.SetSize(899, 640)
		tr.Frame()
		a.windowWidth = 960
		tr.SetSize(960, 640)
		tr.Frame()
		if a.workspaceOpen == false || a.detailPageActive() || !a.settingsOpen {
			t.Fatalf("roundtrip changed settings/work mode: open=%v page=%v settings=%v", a.workspaceOpen, a.detailPageActive(), a.settingsOpen)
		}
		if err := tr.Click("← Back to workbench"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if !tr.Focused("Settings") {
			t.Fatalf("Back from resized Settings focused %v, want Settings", tr.Texts())
		}
	})

	t.Run("settings-to-899-back-visible", func(t *testing.T) {
		a, _ := workspaceTestApp(t)
		a.windowWidth = 960
		a.openContactProfile(a.selectedID())
		tr := ui.NewTester(a.view, 960, 640)
		tr.Frame()
		if err := tr.Click("Settings"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		a.windowWidth = 899
		tr.SetSize(899, 640)
		tr.Frame()
		if !a.settingsOpen || !a.workspaceOpen {
			t.Fatalf("899 settings state = settings=%v work=%v", a.settingsOpen, a.workspaceOpen)
		}
		if err := tr.Click("← Back to workbench"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if !a.detailPageActive() || !tr.Focused("Close profile") || tr.Focused("Settings") {
			t.Fatalf("899 Back from Settings lost the visible profile close control: %v", tr.Texts())
		}
	})
}

func TestStopConversationKeepsPrimarySlotWithoutModelScope(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.SessionID = "s1"
	ct.SessionModel = "gpt-fixture"
	ct.ActiveTaskID = "task-live"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	msgs := locales.In("en")
	rect, ok := tr.Find(msgs.StopConversation())
	if !ok || rect.W != 32 || rect.H != 32 {
		t.Fatalf("stop control geometry = %+v/%v", rect, ok)
	}
	attach, attachOK := tr.Find(msgs.ComposerAttach())
	if !attachOK || attach.W != 32 || attach.H != 32 || rect.X < attach.X+attach.W {
		t.Fatalf("attach/stop geometry = attach=%+v(%v) stop=%+v", attach, attachOK, rect)
	}
	if _, sendOK := tr.Find(msgs.SendAction()); sendOK {
		t.Fatal("running conversation kept Send alongside Stop chat")
	}
	if tr.HasText("Chat model") || tr.HasText("gpt-fixture") {
		t.Fatalf("retired model chrome appeared in composer: %v", tr.Texts())
	}
	for _, text := range tr.Texts() {
		if strings.Contains(text, "More") {
			t.Fatalf("primary action overflowed into toolbar menu: %q", text)
		}
	}
}

func TestProfileSettingsReturnToVisibleOrigin(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	a.detail = ""
	a.openContactProfile(a.selectedID())
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Cmd+, did not open Settings from profile")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.detailIs(detailProfile) || !tr.Focused(locales.In("en").ProfileClose()) {
		t.Fatalf("Settings Back did not restore focus to the visible Bot profile Back control: %v", tr.Texts())
	}
}
