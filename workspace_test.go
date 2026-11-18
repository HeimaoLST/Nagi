package main

import (
	"fmt"
	"os"
	"strings"
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

func TestThreePaneBreakpointsAndToggles(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 1180
	tr := ui.NewTester(a.view, 1180, 720)
	tr.Frame()
	if !tr.HasText("No tasks in Not done.") || !tr.HasText("Chat runs") {
		t.Fatal("new work panel should show the issue overview and local work entry")
	}
	if r, ok := tr.Find("No tasks in Not done."); !ok || r.X < 800 {
		t.Fatalf("full-rail work overview x = %v, want >= 800", r.X)
	}

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

	if err := tr.Click("Close workspace"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if tr.HasText("No tasks in Not done.") || !tr.Focused("Work") {
		t.Fatal("closing work should hide it and return focus to Work")
	}
	if err := tr.Click("Work"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("No tasks in Not done.") || !tr.Focused("Close workspace") {
		t.Fatal("opening work should show it and focus Close workspace")
	}

	a.windowWidth = 960
	tr960 := ui.NewTester(a.view, 960, 640)
	tr960.Frame()
	if a.workOverlay() || !a.avatarRailActive() || !tr960.HasText("Close workspace") {
		t.Fatal("960 should use the avatar rail with the detail as a column")
	}
	a.windowWidth = 1380
	trWide := ui.NewTester(a.view, 1380, 900)
	trWide.Frame()
	if a.workOverlay() {
		t.Fatal("1380 should keep the work panel as a third column")
	}
	if r, ok := trWide.Find("No tasks in Not done."); !ok || r.X < 1000 {
		t.Fatalf("wide work overview x = %v, want >= 1000", r.X)
	}

	a.windowWidth = 900
	trNarrow := ui.NewTester(a.view, 900, 640)
	trNarrow.Frame()
	if a.workOverlay() || !a.avatarRailActive() || !trNarrow.HasText("Close workspace") {
		t.Fatal("900 should use the avatar rail, not the full page")
	}
	if err := trNarrow.Click("Close workspace"); err != nil {
		t.Fatal(err)
	}
	trNarrow.Frame()
	if a.workspaceOpen || !trNarrow.Focused("Work") {
		t.Fatal("avatar detail close did not return to the Work entry")
	}
}

func TestIssueOverviewContentHasPanelInset(t *testing.T) {
	for _, width := range []int{1040, 720} {
		for _, empty := range []bool{true, false} {
			name := fmt.Sprintf("%d/empty=%v", width, empty)
			t.Run(name, func(t *testing.T) {
				a, ct := workspaceTestApp(t)
				a.windowWidth = width
				if !empty {
					issue := &productIssue{ID: "i-overview-inset", ContactID: ct.ID, Goal: "overview row inset",
						Status: issueStatusInProgress}
					ct.Issues = []*productIssue{issue}
					ct.SelectedIssueID = issue.ID
				}
				tr := ui.NewTester(a.view, width, 720)
				tr.Frame()
				panelX := float32(width) - a.workWidth() - paneSeparatorWidth
				if a.detailPageActive() {
					panelX = 0
				}
				check := func(label string) {
					t.Helper()
					r, ok := tr.Find(label)
					if !ok || r.W <= 0 || r.H <= 0 || r.X < panelX+12 || r.X+r.W > float32(width)-12 {
						t.Errorf("%s rect=%+v ok=%v panelX=%.1f width=%d", label, r, ok, panelX, width)
					}
					t.Logf("%s width=%d panelX=%.1f rect=%+v ok=%v", label, width, panelX, r, ok)
				}
				check("Task filter")
				if empty {
					check("No tasks in Not done.")
				} else {
					check("overview row inset")
				}
			})
		}
	}
	if dir := os.Getenv("NAGI_PREVIEW_DIR"); dir != "" {
		writeIssueOverviewInsetPreviews(t, dir)
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

func TestWorkModalEscapeReturnsFocusToEntry(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 899
	tr := ui.NewTester(a.view, 899, 640)
	tr.Frame()
	if !tr.HasText("Back to chat") {
		t.Fatal("narrow work page missing")
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.workspaceOpen || !tr.Focused("Work") {
		t.Fatal("Escape did not close/focus-return the work page")
	}
}

func TestAvatarWorkEscapeReturnsFocusToEntry(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 960
	a.workspaceOpen = true
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	if a.workOverlay() || !a.avatarRailActive() || !tr.HasText("Close workspace") {
		t.Fatal("960 avatar work page missing")
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.workspaceOpen || !tr.Focused("Work") {
		t.Fatal("960 Escape did not close/focus-return the avatar work page")
	}
}

func TestHistoryModalPreservesWorkAndReturnsFocus(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t1"
	ct.LastTaskID = "t1"
	ct.Draft = "later draft"
	w := a.ensureWork(ct, "t1", "m1")
	w.State = workRunning
	ct.Messages = []*message{{Key: "hist:1", Kind: msgContact, Text: "earlier answer"}}
	ct.History = []historyEntry{{ID: "hist:1", Date: "Yesterday", From: "contact", Text: "earlier answer"}}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("History"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.detailIs(detailHistory) || !tr.HasText("Close history") || !tr.Focused("Close history") {
		t.Fatal("history modal did not open/focus")
	}
	if err := tr.Click("Close history"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.workspaceOpen || !tr.Focused("History") || tr.HasText("History: Nagi") {
		t.Fatal("closing history did not return to chat with the History entry focused")
	}
	if ct.ActiveTaskID != "t1" || w.State != workRunning || ct.Draft != "later draft" {
		t.Fatal("history modal changed work or draft")
	}

	if err := tr.Click("History"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.workspaceOpen || !tr.Focused("History") {
		t.Fatal("Escape did not close history/focus its entry")
	}
	if err := tr.Click("History"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("View in chat"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.workspaceOpen || a.highlight != "hist:1" {
		t.Fatal("view-in-conversation did not close/return to chat")
	}
}

func TestHistoryModalIncludesLiveTextBeforeReload(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Messages = []*message{
		{Key: "live:t1:user", Kind: msgUser, Text: "live question"},
		{Key: "live:t1:text:a1", Kind: msgContact, Text: "live answer"},
	}
	a.openHistory()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("live question") || !tr.HasText("live answer") {
		t.Fatalf("live text missing from History: %v", tr.Texts())
	}
	if tr.HasText("No saved messages") {
		t.Fatal("live text was reported as no saved messages")
	}
}

func TestHistoryEmptyQueryDoesNotClaimNoMatch(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.History = nil
	a.openHistory()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if tr.HasText("Try a shorter phrase") {
		t.Fatal("empty history was reported as a failed search")
	}
	if !tr.HasText("No saved messages") {
		t.Fatal("empty history did not use the saved-message empty state")
	}
}

func TestSettingsPageEscapeDoesNotLeavePageAndBackReturnsToThreePane(t *testing.T) {
	a, _ := workspaceTestApp(t)
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
	if a.settingsOpen || !tr.Focused("Settings") || !tr.HasText("Tasks") {
		t.Fatal("Back did not return to the three-pane work view with entry focus")
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
	openConversationWorkPanel(t, tr)
	if tr.HasText("No messages yet") || tr.HasText("No current work") {
		t.Fatalf("unloaded session was presented as truly empty: %v", tr.Texts())
	}
	for _, want := range []string{"Chat history is not loaded", "Session history is not loaded", "Runtime is not ready"} {
		if !tr.HasText(want) {
			t.Errorf("unloaded state missing %q", want)
		}
	}
}

func TestWorkSurvivesNavigationSwitchAndDetails(t *testing.T) {
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
	a.toggleWorkspace()
	if a.workspaceOpen {
		t.Fatal("workspace did not close")
	}
	a.toggleWorkspace()
	if !a.workspaceOpen {
		t.Fatal("workspace did not reopen")
	}
	a.openHistory()
	a.backToChat()
	a.openSettings()
	a.closeAux()
	if ct.ActiveTaskID != "t1" || w.State != workRunning || ct.Draft != "later draft" {
		t.Fatal("history/settings round trip changed work")
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

func TestWorkPanelRendersToolDetailsWithoutInference(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Works = []*workItem{{
		TaskID: "t1", State: workRunning, Prompt: "Inspect the file.",
		Tools: []*workTool{{
			CallID: "k1", Name: "read", Args: `{"path":"README.md"}`,
			Output: `{"content":"first heading"}`, Started: true, Finished: true,
		}},
	}}
	ct.ActiveTaskID = "t1"
	ct.LastTaskID = "t1"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	openConversationWorkPanel(t, tr)
	for _, want := range []string{"Current work", "read", "first heading", "No tool activity yet."} {
		if want == "No tool activity yet." {
			if tr.HasText(want) {
				t.Fatal("work panel should not add an empty-activity placeholder when tools exist")
			}
			continue
		}
		if !tr.HasText(want) {
			t.Errorf("work panel missing %q", want)
		}
	}
	if tr.HasText("Delivered file") || tr.HasText("artifact") {
		t.Fatal("tool output must not be inferred as a delivered file")
	}
	if err := tr.Click("Copy received output"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.Clipboard(), "first heading") {
		t.Fatalf("collapsed copy did not preserve raw tool output: %q", tr.Clipboard())
	}
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("content") {
		t.Fatalf("raw tool output not visible after Details: %v", tr.Texts())
	}
	if tr.HasText("Copy full output") {
		t.Fatal("expanded tool details duplicated the full-output copy control")
	}
	if err := tr.Click("Copy received output"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.Clipboard(), "first heading") {
		t.Fatalf("copy did not preserve raw tool output: %q", tr.Clipboard())
	}
}

func TestWorkspaceToolCardActionsStayVisibleAtAllWidths(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t1"
	ct.LastTaskID = "t1"
	w := a.ensureWork(ct, "t1", "m1")
	w.State = workRunning
	w.Prompt = "long work"
	longPath := "/Users/someone/" + strings.Repeat("非常长的目录/", 20) + "file.json"
	output := `{"content":"` + strings.Repeat("x", 240) + `","path":"` + longPath + `"}`
	w.Tools = []*workTool{{CallID: "c1", Name: "read", Finished: true, Args: `{"path":"` + longPath + `"}`, Output: output}}

	for _, size := range []struct{ width, height int }{{1040, 720}, {960, 640}, {720, 640}, {1380, 900}} {
		a.windowWidth = size.width
		w.Tools[0].Expanded = false
		tr := ui.NewTester(a.view, size.width, size.height)
		tr.Frame()
		openConversationWorkPanel(t, tr)
		panel, ok := tr.Find("Activity")
		if !ok || panel.W <= 0 {
			t.Fatalf("%dx%d activity panel = %+v ok=%v", size.width, size.height, panel, ok)
		}
		for _, label := range []string{"Finished", "Copy received output", "Details"} {
			r, ok := tr.Find(label)
			if !ok || r.W <= 0 || r.H <= 0 || r.X < panel.X || r.X+r.W > panel.X+panel.W ||
				r.Y < panel.Y || r.X < 0 || r.X+r.W > float32(size.width) || r.Y+r.H > float32(size.height) {
				t.Fatalf("%dx%d %q bounds = %+v panel=%+v ok=%v", size.width, size.height, label, r, panel, ok)
			}
		}
		tr.SetClipboard("sentinel")
		if err := tr.Click("Copy received output"); err != nil {
			t.Fatalf("%dx%d copy click: %v", size.width, size.height, err)
		}
		if got := tr.Clipboard(); got != output {
			t.Fatalf("%dx%d copied %q", size.width, size.height, got)
		}
		if err := tr.Click("Details"); err != nil {
			t.Fatalf("%dx%d details click: %v", size.width, size.height, err)
		}
		tr.Frame()
		if !w.Tools[0].Expanded || !tr.HasText("Hide") {
			t.Fatalf("%dx%d details did not expand", size.width, size.height)
		}
	}
}

func TestWorkspaceStructuredToolOutputDoesNotLeakRawJSON(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t-write"
	ct.LastTaskID = "t-write"
	w := a.ensureWork(ct, "t-write", "m-write")
	w.State = workRunning
	raw := `{"path":"/Users/someone/result.txt","bytes_written":42}`
	w.Tools = []*workTool{{CallID: "write-call", Name: "write", Args: `{"path":"/Users/someone/result.txt"}`, Finished: true, Output: raw}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	openConversationWorkPanel(t, tr)
	if tr.HasText(`"bytes_written"`) || tr.HasText(`{"path"`) {
		t.Fatalf("raw structured result leaked into preview: %v", tr.Texts())
	}
	if !tr.HasText("Structured output received; open Details for the exact record.") {
		t.Fatalf("neutral structured summary missing: %v", tr.Texts())
	}
	tr.SetClipboard("")
	if err := tr.Click("Copy received output"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != raw {
		t.Fatalf("copy lost exact structured output: %q", got)
	}
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText(`"bytes_written"`) {
		t.Fatalf("details did not expose exact structured output: %v", tr.Texts())
	}
}

func TestWorkRecoveryKeepsRunAndAgentRecords(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			message := "HTTP 403: suppressed provider body with entitlement guesses"
			raw := "Provider authentication failed: HTTP 403. Check API_KEY and provider configuration; the request was denied."
			w := &workItem{TaskID: "failed-run", State: workFailed, Detail: raw, Prompt: "Original user request"}
			ct.Works = []*workItem{w}
			tr := ui.NewTester(func(c *ui.Context) {
				a.workTheme = resolveWorkbenchTheme(false)
				c.SetTheme(a.workTheme.Theme)
				ui.Scroll(c).Fill().Children(func() { a.workCard(c, c.Theme(), w, false, false) })
			}, 600, 720)
			tr.SetLocale(locale)
			tr.Frame()
			msgs := locales.In(locale)
			if !tr.HasText(msgs.RecoveryProviderDenied("HTTP 403")) || tr.HasText(raw) {
				t.Fatalf("failed work summary missing: %v", tr.Texts())
			}
			if err := tr.Click(msgs.DetailsAction()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tr.HasText(raw) || w.Detail != raw || tr.HasText(message) || w.Prompt != "Original user request" {
				t.Fatal("work disclosure changed records or exposed a suppressed provider body")
			}
			if err := tr.Click(msgs.ConnectionSettingsAction()); err != nil {
				t.Fatal(err)
			}
			if !a.settingsOpen || a.settingsTab != settingsTabConnection || ct.SendState != sendIdle || w.State != workFailed {
				t.Fatal("recovery routed incorrectly or replayed work")
			}
		})
	}
}

func TestIssueUnknownDeliveryKeepsRecordsAndDiagnostic(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			a, ct, issue := disconnectedRecoveryTestApp(t, "exit")
			before := recoveryRecordSnapshot(t, ct)
			tr := recoveryIssueTester(a, ct, issue, 720, 1200)
			tr.SetLocale(locale)
			tr.Frame()
			msgs := locales.In(locale)
			if tr.HasText(msgs.SendUnknownState()) || !tr.HasText(msgs.SendDisconnectedState()) {
				t.Fatalf("unavailable issue offered conversation recovery: %v", tr.Texts())
			}
			if err := tr.Click(msgs.DetailsAction()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tr.HasText(issue.Work.SendError) || recoveryRecordSnapshot(t, ct) != before || a.submissionInFlight != 0 {
				t.Fatal("disclosure lost the diagnostic or changed the unknown execution")
			}
		})
	}
}

func recoveryIssueTester(a *appModel, ct *contact, issue *productIssue, width, height int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		a.workTheme = resolveWorkbenchTheme(c.Theme().Dark)
		c.SetTheme(a.workTheme.Theme)
		ui.Scroll(c).Fill().Children(func() { a.issueCard(c, c.Theme(), ct, issue); a.issueExecutions(c, c.Theme(), ct, issue) })
	}, width, height)
}

func TestRP1AdjacentStatesKeepAdmission(t *testing.T) {
	for _, status := range []string{issueExecutionPending, issueExecutionNotDispatched, issueExecutionRejected, issueExecutionFailed, issueExecutionAccepted} {
		t.Run(status, func(t *testing.T) {
			a, ct, issue := disconnectedRecoveryTestApp(t, "exit")
			a.runtimeStatus = runtimeStateReady
			execution := issue.Executions[0]
			execution.Status = status
			execution.Detail = ""
			issue.Work.SendState = sendIdle
			issue.Work.clearSendError()
			if status == issueExecutionFailed {
				execution.Detail = "Task failed: controlled execution failure"
			}
			before := recoveryRecordSnapshot(t, ct)
			tr := ui.NewTester(func(c *ui.Context) {
				a.workTheme = resolveWorkbenchTheme(false)
				c.SetTheme(a.workTheme.Theme)
				ui.Column(c).Fill().Children(func() { a.issueLatestResult(c, c.Theme(), ct, issue, false) })
			}, 720, 900)
			tr.Frame()
			msgs := locales.In("en")
			want := issueExecutionText(msgs, status)
			if !tr.HasText(want) || tr.HasText(msgs.SendDisconnectedState()) || tr.HasText(msgs.SendUnknownState()) {
				t.Fatalf("neighboring run state lost its meaning: %v", tr.Texts())
			}
			if status == issueExecutionFailed && !tr.HasText(msgs.RecoveryRunFailed()) {
				t.Fatal("real failed run lost its failure summary")
			}
			if status == issueExecutionNotDispatched && !tr.HasText(msgs.IssueNoTaskDispatched()) {
				t.Fatal("not-dispatched summary missing")
			}
			if status == issueExecutionRejected && !tr.HasText(msgs.IssueExecutionRejectedDetail()) {
				t.Fatal("rejected summary missing")
			}
			if recoveryRecordSnapshot(t, ct) != before || issue.Status == issueStatusCompleted || a.submissionInFlight != 0 {
				t.Fatal("presentation changed admission or task completion")
			}
		})
	}
}
