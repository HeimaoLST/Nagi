package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestIssueWorkCardAndComposerStopLabelsAreSeparate(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Messages = nil
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "work A", Status: issueStatusNotStarted, WorkSessionID: "work-session"}
	issue.Work.ActiveTaskID = "task-A"
	issue.Work.SendState = sendAccepted
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", TaskID: "task-A", Status: issueExecutionRunning}}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	ct.ActiveTaskID = "task-B"
	ct.SendState = sendAccepted
	ct.Works = []*workItem{
		{TaskID: "task-A", State: workRunning, Prompt: "work A"},
		{TaskID: "task-B", State: workRunning, Prompt: "conversation B"},
	}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Stop task") || !tr.HasText("Stop chat") {
		t.Fatalf("separate stop labels missing: %v", tr.Texts())
	}
	if !tr.HasText("Running") {
		t.Fatal("issue work state was not visible in detail")
	}
}

func TestIssueResultUsesIssueWorkMessages(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Messages = nil
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "work A", Status: issueStatusNotStarted, WorkSessionID: "work-session"}
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", TaskID: "task-A", Status: issueExecutionCompleted}}
	issue.Work.Messages = []*message{{Key: "work-live", Kind: msgContact, TaskID: "task-A", Text: "issue-owned result"}}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	ct.Works = []*workItem{{TaskID: "task-A", State: workCompleted}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("issue-owned result") {
		t.Fatalf("issue work result missing from issue UI: %v", tr.Texts())
	}
}

func TestRailShowsIssueWorkAttentionWithoutChatUnread(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "failed work", Status: issueStatusNotStarted}
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", TaskID: "task-A", Status: issueExecutionFailed, Detail: "provider failed"}}
	ct.Issues = []*productIssue{issue}
	ct.Unread = 2
	ct.Messages = nil
	a.windowWidth = 1180
	tr := ui.NewTester(a.view, 1180, 720)
	tr.Frame()
	want := locales.In("en").TaskRailAttention(issue.Goal)
	if _, ok := tr.Find(want); !ok {
		t.Fatalf("rail did not surface typed issue-work summary %q: %v", want, tr.Texts())
	}
	if err := tr.Click(want); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.issueNavigation().DetailID != issue.ID || !a.workspaceOpen {
		t.Fatalf("rail summary did not target issue: detail=%q open=%v", ct.issueNavigation().DetailID, a.workspaceOpen)
	}
	if ct.Unread != 2 {
		t.Fatalf("issue-work notice changed chat unread: %d", ct.Unread)
	}
}

func TestUnconfiguredLaunchExposesConfigurationDetails(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.pi != nil {
		t.Fatal("unconfigured first launch unexpectedly started a runtime")
	}
	if a.runtimeStatus == runtimeStateReady {
		t.Fatal("unconfigured first launch unexpectedly reported a ready runtime")
	}
	a.windowWidth = 1040
	render := func(dark bool) *ui.Tester {
		tr := ui.NewTester(a.view, 1040, 720)
		tr.SetDark(dark)
		tr.Frame()
		return tr
	}
	previewDir := os.Getenv("NAGI_PREVIEW_DIR")
	if previewDir != "" {
		if err := os.MkdirAll(previewDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, dark := range []bool{false, true} {
		tr := render(dark)
		if !a.settingsOpen || a.settingsTab != settingsTabConnection || !tr.HasText(locales.In("en").SettingsConnection()) {
			t.Fatalf("unconfigured first launch did not open connection settings: %v", tr.Texts())
		}
		if previewDir != "" {
			theme := "light"
			if dark {
				theme = "dark"
			}
			writePreviewPNG(t, filepath.Join(previewDir, "capacity-unconfigured-"+theme+"-1040x720.png"), tr.Image())
		}
	}

	a.closeSettings()
	startupError := a.runtimeErrorDisplay(locales.In("en"))
	if startupError == "" {
		t.Fatal("unconfigured first launch lost its real startup error")
	}
	if !strings.Contains(startupError, "Connection settings are required") {
		t.Fatalf("unconfigured first launch error = %q", startupError)
	}
	for _, dark := range []bool{false, true} {
		tr := render(dark)
		if !tr.HasText("Setup needed") || !tr.HasText("Open connection settings") || !tr.HasText("Configuration details") {
			t.Fatalf("unconfigured composer lost recovery controls: %v", tr.Texts())
		}
		if previewDir != "" {
			theme := "light"
			if dark {
				theme = "dark"
			}
			writePreviewPNG(t, filepath.Join(previewDir, "capacity-unconfigured-composer-"+theme+"-1040x720.png"), tr.Image())
		}
	}
	tr := render(false)
	if err := tr.Click("Configuration details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAdvanced || !tr.HasText("Startup & configuration") || !tr.HasText(startupError) {
		t.Fatalf("Configuration details did not open raw Advanced diagnostics: open=%v tab=%q texts=%v", a.settingsOpen, a.settingsTab, tr.Texts())
	}
}

func TestRailShowsActiveIssueWorkCount(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "active work", Status: issueStatusNotStarted, WorkSessionID: "work-session"}
	issue.Work.ActiveTaskID = "task-A"
	issue.Work.SendState = sendAccepted
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", TaskID: "task-A", Status: issueExecutionRunning}}
	ct.Issues = []*productIssue{issue}
	ct.Messages = nil
	a.windowWidth = 1180
	tr := ui.NewTester(a.view, 1180, 720)
	tr.Frame()
	wantCount := locales.In("en").TaskActiveCount(1)
	wantSummary := locales.In("en").TaskRailInProgress(issue.Goal)
	if !tr.HasText(wantCount) || !tr.HasText(wantSummary) {
		t.Fatalf("rail did not show typed active count %q / summary %q: %v", wantCount, wantSummary, tr.Texts())
	}
	if ct.Unread != 0 {
		t.Fatalf("active work count changed chat unread: %d", ct.Unread)
	}
}

func TestIssueConfirmNotDispatchedTargetsSelectedExecution(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "recover me", Status: issueStatusNotStarted}
	target := &issueExecution{ID: "e1", SessionID: "work-session", Status: issueExecutionUnknown, Prompt: "target"}
	other := &issueExecution{ID: "e2", SessionID: "other-session", TaskID: "task-other", Status: issueExecutionCompleted, Prompt: "other"}
	issue.Executions = []*issueExecution{target, other}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	recoveryTestStore(t, a)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	clickAfterBoundedScroll(t, tr, "Confirm not dispatched", 1040, 720)
	tr.Frame()
	if target.Status != issueExecutionNotDispatched {
		t.Fatalf("target execution = %+v", target)
	}
	if other.Status != issueExecutionCompleted || other.TaskID != "task-other" {
		t.Fatalf("confirm changed another execution: %+v", other)
	}
	reloaded, err := openContactStore(a.storePath, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.state.Issues) != 1 || reloaded.state.Issues[0].ID != issue.ID || len(reloaded.state.Issues[0].Executions) != 2 {
		t.Fatalf("saved confirmation = %+v", reloaded.state.Issues)
	}
	stored := reloaded.state.Issues[0].Executions
	if stored[0].ID != target.ID || stored[0].Status != issueExecutionNotDispatched || stored[0].TaskID != "" ||
		stored[1].ID != other.ID || stored[1].Status != issueExecutionCompleted || stored[1].TaskID != "task-other" {
		t.Fatalf("saved confirmation changed the wrong execution: %+v", stored)
	}
}
