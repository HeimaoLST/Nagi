package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestResizeDoesNotStealSettingsFocus(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	a.detail = ""
	a.openDetail(detailProfile)
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
	if tr.HasText("Composer for Nagi") || tr.HasText("Add contact") {
		t.Fatal("Settings resize mounted the workbench background")
	}
}

func TestAvatarSettingsResizeBackFocus(t *testing.T) {
	t.Run("roundtrip-back-to-settings", func(t *testing.T) {
		a, _ := workspaceTestApp(t)
		a.windowWidth = 960
		a.openDetail(detailProfile)
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
		a.openDetail(detailProfile)
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

func TestStopConversationKeepsVisibleTextAndModelScope(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.SessionID = "s1"
	ct.SessionModel = "gpt-fixture"
	ct.ActiveTaskID = "task-live"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Stop chat") || !tr.HasText("Chat") || !tr.HasText("gpt-fixture") {
		t.Fatalf("stop/model scope missing: %v", tr.Texts())
	}
	rect, ok := tr.Find("Stop chat")
	if !ok || rect.W < 80 || rect.H < 32 {
		t.Fatalf("stop control geometry = %+v/%v", rect, ok)
	}
	for _, text := range tr.Texts() {
		if strings.Contains(text, "More") {
			t.Fatalf("primary action overflowed into toolbar menu: %q", text)
		}
	}
}

func TestSourcesPageBackReturnsToOriginatingControl(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Messages = []*message{{ID: 7, Kind: msgContact, Text: "answer", Expanded: true, Sources: []string{"h1"}}}
	ct.History = []historyEntry{{ID: "h1", Date: "Yesterday", From: "contact", Text: "source text"}}
	a.openSources(7)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !a.mainPageActive() || !tr.HasText("source text") || tr.HasText("Composer for Nagi") {
		t.Fatalf("sources did not replace the main reading surface: %v", tr.Texts())
	}
	if err := tr.Click("← Back"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "" || a.mainPageActive() || !tr.Focused("1 earlier message") {
		t.Fatalf("sources Back = aux=%q focused=%v", a.aux, tr.Focused("1 earlier messages"))
	}
}

func TestProfileAndSourcesSettingsReturnToVisibleOrigin(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	a.detail = ""
	a.openDetail(detailProfile)
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
	if !a.detailIs(detailProfile) || !tr.Focused("Edit full profile") {
		t.Fatalf("profile settings return did not focus its visible Edit: %v", tr.Texts())
	}

	ct.Messages = []*message{{ID: 9, Kind: msgContact, Text: "sources", Sources: []string{"h1"}}}
	a.openSources(9)
	tr.Frame()
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Cmd+, did not open Settings from sources")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "sources" || !tr.Focused("Close sources") {
		t.Fatalf("sources settings return = aux=%q focused=%v", a.aux, tr.Focused("Close sources"))
	}
}
