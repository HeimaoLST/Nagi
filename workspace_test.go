package main

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func workspaceTestApp(t *testing.T) (*appModel, *contact) {
	t.Helper()
	ct := &contact{ID: "nagi", Name: "Nagi", Role: "Assistant", SessionID: "s1", PiBinding: recoveryConversationBinding("nagi", "/tmp", "s1"), Loaded: true}
	a := &appModel{
		demo: false, runtimeStatus: runtimeStateReady, workspaceOpen: true,
		windowWidth: 1040, scrollTo: -1, contacts: []*contact{ct},
	}
	return a, ct
}

func TestContactRailHideAndReopenRestoresFocus(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 1180
	tr := ui.NewTester(a.view, 1180, 720)
	tr.Frame()

	if err := tr.Click("Hide"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if _, visible := tr.Find("Hide"); visible || !tr.HasText("Contacts") {
		t.Fatal("hiding the contact rail should expose a Contacts entry")
	}
	if !tr.Focused("Contacts") {
		t.Fatal("collapsing the rail should return focus to Contacts")
	}
	if err := tr.Click("Contacts"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if _, visible := tr.Find("Hide"); !visible {
		t.Fatal("Contacts did not reopen the rail")
	}
}

func TestPiLateSettlementDoesNotReplaceNewConversationRun(t *testing.T) {
	a, ct, first := piRunStartedTestApp(t)
	ct.pendingSend = &pendingSend{Draft: "first", State: "submitting"}
	a.applyPiEvent(a.connectionGeneration, first)
	first.Name, first.Data = pi.RunSettled, []byte(`{"status":"completed","text":"first result"}`)
	a.applyPiEvent(a.connectionGeneration, first)
	second := first
	second.Name, second.RunID, second.Data = pi.RunStarted, "second-run", nil
	ct.ActiveTaskID, ct.SendState = second.RunID, sendSubmitting
	ct.pendingSend = &pendingSend{Draft: "second", State: "submitting"}
	a.applyPiEvent(a.connectionGeneration, second)
	a.applyPiEvent(a.connectionGeneration, first)
	if ct.ActiveTaskID != second.RunID || ct.SendState != sendAccepted || len(ct.Messages) != 3 || ct.Messages[1].Text != "first result" || ct.Messages[2].Text != "second" {
		t.Fatalf("late settlement changed the newer run or transcript: %+v", ct.Messages)
	}
}

func TestSettingsPageEscapeDoesNotLeavePageAndBackReturnsToChat(t *testing.T) {
	a, ct := workspaceTestApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || !tr.HasText("Settings") {
		t.Fatal("settings page did not open")
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Escape incorrectly closed the settings page")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.settingsOpen || !tr.Focused("Settings") || !tr.HasText("Composer for "+ct.Name) {
		t.Fatal("Back did not return to chat with Settings focused")
	}
}

func TestSingleExecutionSlotBlocksOtherContact(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t1"
	ct.Loaded = true
	other := &contact{ID: "mio", Name: "Mio", Role: "Assistant", SessionID: "s2", Loaded: true}
	a.contacts = append(a.contacts, other)
	a.selected = 1
	a.pi = &piAppHost{client: &pi.Client{}}
	a.runtimeStatus = runtimeStateReady
	reason := a.canSend(other)
	if reason != blockAnotherContact {
		t.Fatalf("second contact reason = %q", reason)
	}
	if other.Draft != "" || ct.ActiveTaskID != "t1" {
		t.Fatal("busy guard changed drafts or active task")
	}
}

func TestUnloadedSessionDoesNotClaimEmptyHistory(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.SessionID = "s1"
	ct.Loaded = false
	a.runtimeStatus = runtimeStateFailed
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if tr.HasText("No messages yet") {
		t.Fatalf("unloaded session was presented as truly empty: %v", tr.Texts())
	}
	msgs := locales.In("en")
	for _, want := range []string{msgs.HistoryNotLoadedTitle(), msgs.HistoryNotLoadedHint(), msgs.HistoryRuntimeNotReady()} {
		if !tr.HasText(want) {
			t.Errorf("unloaded state missing %q", want)
		}
	}
}

func TestRunningChatSurvivesContactAndSettingsSwitch(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t1"
	ct.LastTaskID = "t1"
	ct.Draft = "later draft"
	w := a.ensureWork(ct, "t1", "m1")
	w.State = workRunning
	other := &contact{ID: "mio", Name: "Mio", Role: "Assistant", Draft: "mio draft"}
	a.contacts = append(a.contacts, other)

	a.selectContact(1)
	if ct.ActiveTaskID != "t1" || w.State != workRunning || ct.Draft != "later draft" {
		t.Fatal("switching contacts stopped or cleared work")
	}
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	a.closeSettings()
	if ct.ActiveTaskID != "t1" || w.State != workRunning || ct.Draft != "later draft" || other.Draft != "mio draft" {
		t.Fatal("Settings round trip changed active work or either draft")
	}
	a.selectContact(0)
	if ct.ActiveTaskID != "t1" || w.State != workRunning || ct.Draft != "later draft" {
		t.Fatal("returning to the contact lost work")
	}
}

func TestFailedWorkAddsAttentionButNotUnread(t *testing.T) {
	a, ct, event := piRunStartedTestApp(t)
	a.contacts = append(a.contacts, &contact{ID: "mio", Name: "Mio", Role: "Assistant"})
	a.selected = 1
	ct.pendingSend = &pendingSend{Draft: "first", State: "submitting"}
	a.applyPiEvent(a.connectionGeneration, event)
	event.Name, event.Data = pi.RunSettled, []byte(`{"status":"failed","error":{"message":"tool failed"}}`)
	a.applyPiEvent(a.connectionGeneration, event)
	if ct.Status != statusAttention || ct.Unread != 0 || ct.ActiveTaskID != "" || ct.SendState != sendIdle {
		t.Fatalf("failed Pi run state=%v unread=%d active=%q send=%v", ct.Status, ct.Unread, ct.ActiveTaskID, ct.SendState)
	}
}

func TestChatToolDetailsStayReachableAndDoNotInferArtifacts(t *testing.T) {
	for _, size := range [][2]int{{1040, 720}, {960, 640}, {720, 640}, {1380, 900}} {
		a, ct := workspaceTestApp(t)
		a.windowWidth = size[0]
		const raw = `{"path":"/tmp/result.txt","bytes_written":42}`
		activity := &message{Key: "tool:write", Kind: msgActivity, CallID: "write-call", ToolName: "write", ToolOutput: raw}
		ct.Messages = []*message{activity}
		tr := ui.NewTester(a.view, size[0], size[1])
		tr.Frame()
		msgs := locales.In("en")
		if !tr.HasText(msgs.ToolOutputStructured()) || tr.HasText(`"bytes_written"`) || tr.HasText("Delivered file") {
			t.Fatalf("%v collapsed activity misrepresented tool output: %v", size, tr.Texts())
		}
		label := msgs.DetailsAction()
		r, ok := tr.Find(label)
		if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
			t.Fatalf("%v activity Details is outside the viewport: %+v found=%v", size, r, ok)
		}
		if err := tr.Click(label); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if !tr.HasText(raw) || !tr.HasText(msgs.HideAction()) || activity.ToolOutput != raw || len(ct.Messages) != 1 {
			t.Fatalf("%v Details lost or changed the recorded output: %v", size, tr.Texts())
		}
	}
}
