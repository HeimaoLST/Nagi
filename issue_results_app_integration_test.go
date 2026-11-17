package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func issueResultTextsHave(texts []string, want string) bool {
	for _, text := range texts {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}

func TestIssueResultPageCheckSequenceAppliesOnlyLatest(t *testing.T) {
	app := &appModel{scrollTo: -1}
	pump := newUIPump(t, app)
	t.Cleanup(func() { pump.close() })
	file := issueResultFile{
		ExecutionID: "exec-seq", SessionID: "session-seq", TaskID: "task-seq", MessageID: "msg-seq",
		CallID: "call-seq", ToolName: "write", Path: "/tmp/issue-result-seq.txt", WorkingDirectory: "/tmp",
		RawResult: `{"path":"/tmp/issue-result-seq.txt","bytes_written":1}`,
	}
	key := issueResultFileKey(file)
	app.aux = "issue-result"
	app.issueResult = issueResultPage{
		Open: true, OwnerContactID: "contact-seq", IssueID: "issue-seq", ExecutionID: "exec-seq",
		SessionID: "session-seq", TaskID: "task-seq", MessageID: "msg-seq", Generation: 1,
		Panel: issueResultPanelState{Files: []issueResultFile{file}, Selected: key, InspectFor: key},
	}

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	var calls atomic.Int32
	app.issueResultInspect = func(issueResultFile) issueResultFileInspection {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			<-releaseFirst
			return issueResultFileInspection{Path: file.Path, Exists: true, Regular: true, Preview: "older"}
		default:
			close(secondStarted)
			<-releaseSecond
			return issueResultFileInspection{Path: file.Path, Exists: true, Regular: true, Preview: "newer"}
		}
	}
	waitSignal := func(ch chan struct{}, label string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", label)
		}
	}

	pump.do(func() { app.checkIssueResultFile(1, file) })
	waitSignal(firstStarted, "first inspection")
	pump.do(func() { app.checkIssueResultFile(1, file) })
	waitSignal(secondStarted, "second inspection")
	close(releaseSecond)
	pump.wait(func() bool {
		inspection := app.issueResult.Panel.Inspection
		return inspection != nil && inspection.Preview == "newer"
	})
	close(releaseFirst)
	pump.wait(func() bool { return app.resultInFlight == 0 })
	var preview string
	pump.do(func() {
		if inspection := app.issueResult.Panel.Inspection; inspection != nil {
			preview = inspection.Preview
		}
	})
	if preview != "newer" {
		t.Fatalf("older same-key check overwrote latest inspection: %q", preview)
	}
}

func TestIssueResultPageReloadErrorKeepsReadableBody(t *testing.T) {
	a, ct := workspaceTestApp(t)
	file := issueResultPanelFixtureFile("reload", "/tmp/reload-body.txt", `{"path":"/tmp/reload-body.txt","bytes_written":1}`)
	a.aux = "issue-result"
	a.windowWidth = 720
	a.issueResult = issueResultPage{
		Open: true, OwnerContactID: ct.ID, IssueID: "issue-reload", ExecutionID: "exec-reload",
		SessionID: "session-reload", TaskID: "task-reload", MessageID: "msg-reload", Generation: 3,
		LoadError: "Execution history is unavailable: injected",
		Notice:    "Runtime session working directory is unavailable; file paths are shown as reported.",
		Panel: issueResultPanelState{
			RunNumber: 2, RunGoal: "Reload error", RunStatus: issueExecutionCompleted,
			Response: "previous readable body\n", Files: []issueResultFile{file},
		},
	}
	tr := ui.NewTester(a.view, 720, 900)
	tr.Frame()
	texts := tr.Texts()
	for _, want := range []string{"Execution history is unavailable: injected", "previous readable body", "reload-body.txt", "Reload result"} {
		if !issueResultTextsHave(texts, want) {
			t.Fatalf("reload error hid readable body or retry control: missing %q in %v", want, texts)
		}
	}
}

func issueResultAppPreview(scene string, width int) *appModel {
	a := issuePreviewApp("issue-results")
	a.windowWidth = width
	a.aux = "issue-result"
	ct := a.selectedContact()
	file := issueResultPanelFixtureFile("preview", "/Users/you/Workspace/Saki/report.txt", `{"path":"/Users/you/Workspace/Saki/report.txt","bytes_written":12}`)
	page := issueResultPage{
		Open: true, OwnerContactID: ct.ID, IssueID: ct.SelectedIssueID, ExecutionID: "exec-preview",
		SessionID: "session-preview", TaskID: "task-preview", MessageID: "msg-preview", Generation: 1,
		Panel: issueResultPanelState{
			RunNumber: 2, RunGoal: "Ship visible results", RunStatus: issueExecutionCompleted,
			Response: "# Recorded output\n\nResult body with `code`.\n\n```go\nfunc main() {}\n```\n",
			Files:    []issueResultFile{file},
		},
	}
	setReader := func(file issueResultFile, inspection issueResultFileInspection) {
		key := issueResultFileKey(file)
		page.Panel.Files = []issueResultFile{file}
		page.Panel.Selected = key
		page.Panel.InspectFor = key
		page.Panel.Inspection = &inspection
		page.Panel.ReaderKey = key
	}
	switch scene {
	case "navigation":
		ct.issueNavigation().DetailID = ct.SelectedIssueID
		ct.issueNavigation().Filter = issueFilterUnfinished
		a.issueDetailOpen = map[string]bool{ct.SelectedIssueID: true}
	case "loading":
		page.Loading = true
		page.Panel.Response = ""
		page.Panel.Files = nil
	case "empty":
		page.Panel.Response = ""
		page.Panel.Files = nil
	case "markdown":
		mdFile := issueResultPanelFixtureFile("markdown", "/Users/you/Workspace/Saki/中文结果.md", `{"path":"/Users/you/Workspace/Saki/中文结果.md","bytes_written":12}`)
		setReader(mdFile, issueResultFileInspection{
			Path: mdFile.Path, WorkingDirectory: mdFile.WorkingDirectory, Exists: true, Regular: true,
			Preview: "# 中文结果\n\n这是中文段落，包含 **重点** 与 `inline`。\n\n- 第一项\n- 第二项\n\n| 名称 | 类型 | 标记 | 状态 |\n| --- | --- | --- | --- |\n| 中文内容 | Markdown | **重点** | 完成 |\n| 长内容 | text | `code` | 可滚动 |\n\n```go\nfunc main() {\n\tprintln(\"保留缩进\")\n}\n```\n",
		})
	case "plain":
		plainFile := issueResultPanelFixtureFile("plain", "/Users/you/Workspace/Saki/notes.txt", `{"path":"/Users/you/Workspace/Saki/notes.txt","bytes_written":12}`)
		setReader(plainFile, issueResultFileInspection{
			Path: plainFile.Path, WorkingDirectory: plainFile.WorkingDirectory, Exists: true, Regular: true,
			Preview: "# 这不是标题\n\n`这不是 inline code`\n\n| 不是 | 表格 |\n",
		})
	case "missing":
		missingFile := issueResultPanelFixtureFile("missing", "/Users/you/Workspace/Saki/missing.md", `{"path":"/Users/you/Workspace/Saki/missing.md","bytes_written":12}`)
		setReader(missingFile, issueResultFileInspection{
			Path: missingFile.Path, WorkingDirectory: missingFile.WorkingDirectory,
			NoticeKind: resultNoticeMissing,
		})
		page.Panel.ActionFor = issueResultFileKey(missingFile)
		page.Panel.Action = issueResultFileAction{Path: missingFile.Path, ErrKind: resultActionOpen, ErrDetail: "no handler"}
	case "readerror":
		readErrorFile := issueResultPanelFixtureFile("readerror", "/Users/you/Workspace/Saki/readerror.md", `{"path":"/Users/you/Workspace/Saki/readerror.md","bytes_written":12}`)
		setReader(readErrorFile, issueResultFileInspection{
			Path: readErrorFile.Path, WorkingDirectory: readErrorFile.WorkingDirectory, Exists: true, Regular: true,
			ReadErrorKind: resultNoticeRead, ReadErrorDetail: "permission denied",
		})
	case "source":
		inspection := issueResultFileInspection{
			Path: file.Path, WorkingDirectory: file.WorkingDirectory, Exists: true, Regular: true,
			Preview: "current file content\n",
		}
		setReader(file, inspection)
		page.Panel.SourceFor = issueResultFileKey(file)
	case "long":
		longName := strings.Repeat("很长的中文结果文件名", 4) + ".md"
		longPath := "/Users/you/Workspace/Saki/" + longName
		longFile := issueResultPanelFixtureFile("long", longPath, fmt.Sprintf(`{"path":%q,"bytes_written":1}`, longPath))
		setReader(longFile, issueResultFileInspection{
			Path: longPath, WorkingDirectory: longFile.WorkingDirectory, Exists: true, Regular: true,
			Preview: "# 长文件名\n\n" + strings.Repeat("窄窗口中的中文长文本需要换行并保持可读。", 8) + "\n",
		})
	case "multiple":
		second := issueResultPanelFixtureFile("preview-two", file.Path, `{"path":"/Users/you/Workspace/Saki/report.txt","applied":1}`)
		page.Panel.RunNumber = 3
		page.Panel.RunGoal = "Ship visible results"
		page.Panel.Files = []issueResultFile{file, second}
	}
	a.issueResult = page
	return a
}

func TestIssueResultReaderActionsFollowLocale(t *testing.T) {
	app := issueResultAppPreview("markdown", 1040)
	tr := ui.NewTester(app.view, 1040, 900)
	tr.SetLocale("zh-Hans")
	tr.Frame()
	for _, want := range []string{"再次检查", "打开文件", "在 Finder 中显示", "源码与原始记录"} {
		if !tr.HasText(want) {
			t.Fatalf("localized reader action %q missing: %v", want, tr.Texts())
		}
	}
}

func TestIssueResultAppPreviewsAlwaysWriteHeadlessPNGs(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	type sceneCase struct {
		name  string
		width int
		dark  bool
	}
	var cases []sceneCase
	for _, scene := range []string{"markdown", "plain", "missing", "readerror", "source", "long"} {
		for _, width := range []int{720, 1040, 1280} {
			for _, dark := range []bool{false, true} {
				cases = append(cases, sceneCase{scene, width, dark})
			}
		}
	}
	for _, scene := range []sceneCase{
		{"loading", 720, false},
		{"loading", 1040, true},
		{"empty", 1040, false},
		{"empty", 720, true},
		{"multiple", 1280, true},
	} {
		cases = append(cases, scene)
	}
	for _, width := range []int{720, 1040, 1280} {
		for _, dark := range []bool{false, true} {
			cases = append(cases, sceneCase{"navigation", width, dark})
		}
	}
	for _, scene := range cases {
		app := issueResultAppPreview(scene.name, scene.width)
		tr := ui.NewTester(app.view, scene.width, 900)
		tr.SetDark(scene.dark)
		tr.Frame()
		if scene.name == "navigation" {
			// The navigation preview must exercise the real result-page Back
			// path, not only clear issueResult.Open in the fixture.
			if err := tr.Click("Close results"); err != nil {
				t.Fatalf("preview scene %s could not close Run results: %v", scene.name, err)
			}
			tr.Frame()
			if app.issueResult.Open || app.aux != "" {
				t.Fatalf("preview scene %s did not return to the issue: open=%v aux=%q", scene.name, app.issueResult.Open, app.aux)
			}
		}
		readerWant := map[string][]string{
			"markdown":   {"中文结果", "名称", "完成", "Check again", "Open file", "Show in Finder", "Source & raw record", "Copy current file"},
			"plain":      {"# 这不是标题", "| 不是 | 表格 |"},
			"missing":    {"No file exists at the reported path"},
			"readerror":  {"permission denied"},
			"source":     {"Source & raw record"},
			"long":       {"很长的中文结果文件名"},
			"navigation": {"Task", "Read latest result", "Open result: run 1"},
		}
		for _, want := range readerWant[scene.name] {
			if !tr.HasText(want) {
				t.Fatalf("preview scene %s missing %q: %v", scene.name, want, tr.Texts())
			}
		}
		theme := "light"
		if scene.dark {
			theme = "dark"
		}
		writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("issue-result-app-%s-%s-%d.png", scene.name, theme, scene.width)), tr.Image())
	}
}

func TestIssueResultPageReloadDuringCheckDrainsStaleCallback(t *testing.T) {
	execution := &issueExecution{
		ID: "exec-reload", SessionID: "new-session", TaskID: "new-task", MessageID: "new-message",
		Status: issueExecutionCompleted,
	}
	issue := &productIssue{
		ID: "issue-reload", ContactID: "contact-reload", Goal: "reload while checking",
		Status: issueStatusInProgress, Executions: []*issueExecution{execution},
	}
	ct := &contact{ID: "contact-reload", Issues: []*productIssue{issue}}
	app := &appModel{contacts: []*contact{ct}, selected: 0, scrollTo: -1}
	pump := newUIPump(t, app)
	t.Cleanup(func() { pump.close() })
	file := issueResultFile{
		ExecutionID: "exec-reload", SessionID: "old-session", TaskID: "old-task", MessageID: "old-message",
		CallID: "call-reload", ToolName: "write", Path: "/tmp/issue-result-reload.txt", WorkingDirectory: "/tmp",
		RawResult: `{"path":"/tmp/issue-result-reload.txt","bytes_written":1}`,
	}
	key := issueResultFileKey(file)
	app.aux = "issue-result"
	app.issueResult = issueResultPage{
		Open: true, OwnerContactID: ct.ID, IssueID: issue.ID, ExecutionID: execution.ID,
		SessionID: "old-session", TaskID: "old-task", MessageID: "old-message", Generation: 1,
		Panel: issueResultPanelState{
			RunNumber: 1, RunGoal: "old label", RunStatus: issueExecutionRunning,
			Files: []issueResultFile{file}, Selected: key, InspectFor: key,
		},
	}
	checkStarted := make(chan struct{})
	releaseCheck := make(chan struct{})
	app.issueResultInspect = func(issueResultFile) issueResultFileInspection {
		close(checkStarted)
		<-releaseCheck
		return issueResultFileInspection{Path: file.Path, Exists: true, Regular: true, Preview: "old inspection"}
	}
	pump.do(func() { app.checkIssueResultFile(1, file) })
	select {
	case <-checkStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("check did not start")
	}
	pump.do(func() { app.reloadIssueResult(1) })
	var checking bool
	var sessionID, taskID, messageID, status, runLabel, loadError string
	pump.do(func() {
		page := app.issueResult
		checking = page.Panel.Checking
		sessionID, taskID, messageID = page.SessionID, page.TaskID, page.MessageID
		status, runLabel, loadError = page.Panel.RunStatus, page.Panel.RunGoal, page.LoadError
	})
	if checking {
		t.Fatal("reload left an in-flight Check marked as checking")
	}
	if sessionID != "new-session" || taskID != "new-task" || messageID != "new-message" {
		t.Fatalf("reload kept stale execution identity: %s/%s/%s", sessionID, taskID, messageID)
	}
	if status != execution.Status || runLabel != "reload while checking" {
		t.Fatalf("reload kept stale run label/status: %q / %q", runLabel, status)
	}
	if loadError == "" {
		t.Fatal("reload without a ready runtime should leave an explainable error")
	}
	close(releaseCheck)
	pump.wait(func() bool { return app.resultInFlight == 0 })
	var applied bool
	pump.do(func() {
		applied = app.issueResult.Panel.Inspection != nil && app.issueResult.Panel.Inspection.Preview == "old inspection"
	})
	if applied {
		t.Fatal("reload allowed a stale in-flight check callback to apply")
	}
}

func TestIssueResultAppEmptyStateShowsHonestPanelControls(t *testing.T) {
	app := issueResultAppPreview("empty", 1040)
	tr := ui.NewTester(app.view, 1040, 900)
	tr.Frame()
	texts := tr.Texts()
	for _, want := range []string{
		"No assistant response was recorded for this run.",
		"No files were reported for this run.",
		"Reload result",
		"Close results",
	} {
		if !issueResultTextsHave(texts, want) {
			t.Fatalf("loaded empty result page missing %q in %v", want, texts)
		}
	}
}

func TestRecoveryPresentationEmptyResultUsesActualSettingsRoute(t *testing.T) {
	message := "upstream refused the request"
	raw := taskFailureDetail("model_error", &message)
	a, ct, issue, execution := attentionDisclosureApp(t, issueExecutionFailed, raw)
	a.openIssueResult(issue.ID, execution.ID)
	tr := ui.NewTester(func(c *ui.Context) {
		a.workTheme = resolveWorkbenchTheme(false)
		c.SetTheme(a.workTheme.Theme)
		ui.Column(c).Fill().Children(func() { a.issueResultPanelView(c, c.Theme()) })
	}, 720, 640)
	tr.Frame()
	if !tr.HasText(locales.In("en").RecoveryModelRequestFailed()) || tr.HasText(raw) {
		t.Fatalf("empty result omitted recovery summary: %v", tr.Texts())
	}
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText(raw) || a.issueResult.Panel.RunDetail != raw {
		t.Fatal("empty result lost raw run record")
	}
	if err := tr.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if !a.settingsOpen || a.settingsTab != settingsTabAppearance {
		t.Fatal("result Settings did not preserve its General entry route")
	}
	settings := ui.NewTester(a.view, 1040, 720)
	settings.Frame()
	if err := settings.Click("Connection & model"); err != nil {
		t.Fatal(err)
	}
	settings.Frame()
	if a.settingsTab != settingsTabConnection || a.rt != nil || ct.SendState != sendIdle || a.issueResult.Panel.RunDetail != raw {
		t.Fatal("ordinary result-to-connection route changed records or executed work")
	}
}

func TestIssueAttentionDisclosureRejectsStaleResultActions(t *testing.T) {
	cases := []struct {
		name   string
		change func(*appModel, *contact, *productIssue, *issueExecution)
	}{
		{"reason", func(_ *appModel, _ *contact, _ *productIssue, e *issueExecution) { e.Detail += " changed" }},
		{"status", func(_ *appModel, _ *contact, _ *productIssue, e *issueExecution) { e.Status = issueExecutionUnknown }},
		{"session", func(_ *appModel, _ *contact, _ *productIssue, e *issueExecution) { e.SessionID = "new-session" }},
		{"task", func(_ *appModel, _ *contact, _ *productIssue, e *issueExecution) { e.TaskID = "new-task" }},
		{"message", func(_ *appModel, _ *contact, _ *productIssue, e *issueExecution) { e.MessageID = "new-message" }},
		{"replacement", func(_ *appModel, _ *contact, i *productIssue, e *issueExecution) {
			replacement := *e
			i.Executions[0] = &replacement
		}},
		{"detached", func(_ *appModel, ct *contact, _ *productIssue, _ *issueExecution) { ct.Issues = nil }},
		{"contact", func(a *appModel, _ *contact, _ *productIssue, _ *issueExecution) {
			a.contacts = append(a.contacts, &contact{ID: "other-owner"})
			a.selected = 1
		}},
		{"generation", func(a *appModel, _ *contact, _ *productIssue, _ *issueExecution) { a.issueResult.Generation++ }},
		{"page-issue", func(a *appModel, _ *contact, _ *productIssue, _ *issueExecution) {
			a.issueResult.IssueID = "other-issue"
		}},
		{"page-detail", func(a *appModel, _ *contact, _ *productIssue, _ *issueExecution) {
			a.issueResult.Panel.RunDetail = "different displayed reason"
		}},
		{"settings", func(a *appModel, _ *contact, _ *productIssue, _ *issueExecution) { a.settingsOpen = true }},
		{"reader", func(a *appModel, _ *contact, _ *productIssue, _ *issueExecution) {
			a.issueResult.Panel.ReaderKey = "file-reader"
		}},
		{"aux", func(a *appModel, _ *contact, _ *productIssue, _ *issueExecution) { a.aux = "sources" }},
		{"closed", func(a *appModel, _ *contact, _ *productIssue, _ *issueExecution) { a.closeIssueResult() }},
		{"shutdown", func(a *appModel, _ *contact, _ *productIssue, _ *issueExecution) { a.shutdownStarted = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, ct, issue, execution := attentionDisclosureApp(t, issueExecutionFailed, "captured original reason")
			a.openIssueResult(issue.ID, execution.ID)
			actions := a.issueResultPanelActions(a.issueResult.Generation)
			before, err := os.ReadFile(a.store.path)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(a, ct, issue, execution)
			actions.ToggleRunDetails()
			after, err := os.ReadFile(a.store.path)
			if err != nil {
				t.Fatal(err)
			}
			if execution.AttentionSeenStatus != "" || a.issueResult.Panel.RunDetailsOpen || string(before) != string(after) || a.resultInFlight != 0 {
				t.Fatal("stale disclosure changed seen, disclosure state, saved bytes or started a read")
			}
		})
	}
}

func TestIssueAttentionDisclosureVisibleAtMinimumAndDefaultSize(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, size := range [][2]int{{720, 640}, {1040, 720}} {
			for _, surface := range []string{"work", "result"} {
				t.Run(fmt.Sprintf("%s/%dx%d/%s", locale, size[0], size[1], surface), func(t *testing.T) {
					a, _, issue, execution := attentionDisclosureApp(t, issueExecutionFailed, "minimum-size explicit reason")
					a.windowWidth = size[0]
					tr := ui.NewTester(a.view, size[0], size[1])
					tr.SetLocale(locale)
					tr.Frame()
					msgs := locales.In(locale)
					entry := msgs.WorkReadResult()
					if surface == "work" {
						entry = msgs.IssueViewExecution()
					}
					clickAfterBoundedScroll(t, tr, entry, size[0], size[1])
					tr.Frame()
					if execution.AttentionSeenStatus != "" {
						t.Fatal("navigation acknowledged a hidden reason")
					}
					clickAfterBoundedScroll(t, tr, msgs.DetailsAction(), size[0], size[1])
					tr.Frame()
					if !tr.HasText(execution.Detail) || !tr.HasText(issueExecutionText(msgs, execution.Status)) || execution.AttentionSeenStatus != issueExecutionFailed {
						t.Fatalf("visible current disclosure missing: %v", tr.Texts())
					}
					geometry := map[string]ui.Rect{}
					for _, label := range []string{execution.Detail, issueExecutionText(msgs, execution.Status), msgs.HideDetailsAction()} {
						r, ok := tr.Find(label)
						if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
							t.Fatalf("disclosed status/reason/control is outside the viewport: %q %+v found=%v", label, r, ok)
						}
						geometry[label] = r
					}
					t.Logf("reachable entry=%q disclosure=%q size=%dx%d issue=%s", entry, msgs.DetailsAction(), size[0], size[1], issue.ID)
					if dir := os.Getenv("SAKI_PREVIEW_DIR"); dir != "" {
						name := fmt.Sprintf("ad-visible-%s-%dx%d-%s", locale, size[0], size[1], surface)
						writePreviewPNG(t, filepath.Join(dir, name+".png"), tr.Image())
						data, err := json.MarshalIndent(geometry, "", "  ")
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, name+".geometry.json"), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func TestIssueAttentionDisclosureSaveRetryDoesNotAcknowledgeRollback(t *testing.T) {
	a, _, issue, execution := attentionDisclosureApp(t, issueExecutionUnknown, "retained unknown reason")
	goodPath := a.store.path
	blocked := filepath.Join(t.TempDir(), "file-parent")
	if err := os.WriteFile(blocked, []byte("preserve me"), 0600); err != nil {
		t.Fatal(err)
	}
	a.store.path = filepath.Join(blocked, "contacts.json")
	a.openIssueResult(issue.ID, execution.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if execution.AttentionSeenStatus != "" || a.localSaveErr == "" || !a.issueWorkNotice(a.selectedContact()).Attention || !tr.HasText(execution.Detail) {
		t.Fatal("failed seen write did not roll back with the disclosed reason and local error")
	}
	page := a.issueResult
	a.applyIssueResultLoaded(nil, 0, page.Generation, page.OwnerContactID, issue.ID, execution.ID,
		page.SessionID, page.TaskID, issueResultDerivation{}, "retained result body", "")
	tr.Frame()
	if execution.AttentionSeenStatus != "" || a.localSaveErr == "" {
		t.Fatal("background result acknowledged or cleared local error")
	}
	a.store.path = goodPath
	if err := tr.Click(locales.In("en").SettingsRetrySave()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if execution.AttentionSeenStatus != "" || a.localSaveErr != "" {
		t.Fatal("Retry save acknowledged a rolled-back disclosure")
	}
	saved, err := openContactStore(goodPath, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.state.Issues[0].Executions[0].AttentionSeenStatus != "" || execution.Status != issueExecutionUnknown || execution.Detail != "retained unknown reason" {
		t.Fatal("retry changed attention or unknown facts")
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if execution.AttentionSeenStatus != "" {
		t.Fatal("locale/render acknowledged the rolled-back exception")
	}
	if err := tr.Click(locales.In("zh-Hans").HideDetailsAction()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if execution.AttentionSeenStatus != "" {
		t.Fatal("collapsing Details acknowledged the exception")
	}
	if err := tr.Click(locales.In("zh-Hans").DetailsAction()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if execution.AttentionSeenStatus != issueExecutionUnknown || !tr.HasText(execution.Detail) {
		t.Fatal("a new explicit matching disclosure did not acknowledge after local recovery")
	}
}

func TestIssueAttentionRetainedResultScrollShowsInterruptedTarget(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, size := range [][2]int{{720, 640}, {1040, 720}} {
			for _, entry := range []string{"open-long-goal", "reload-response", "reload-long-goal"} {
				t.Run(fmt.Sprintf("%s/%dx%d/%s", locale, size[0], size[1], entry), func(t *testing.T) {
					a, ct, issue, execution := attentionDisclosureApp(t, issueExecutionCompleted, "")
					a.windowWidth = size[0]
					if err := a.persist(); err != nil {
						t.Fatal(err)
					}
					tr := ui.NewTester(a.view, size[0], size[1])
					tr.SetLocale(locale)
					tr.Frame()
					msgs := locales.In(locale)
					clickAfterBoundedScroll(t, tr, msgs.WorkReadResult(), size[0], size[1])
					tr.Frame()
					page := a.issueResult
					a.applyIssueResultLoaded(nil, a.connectionGeneration, page.Generation, ct.ID, issue.ID, execution.ID, execution.SessionID, execution.TaskID,
						issueResultDerivation{}, strings.Repeat("Recorded response paragraph.\n\n", 100), "")
					tr.Frame()
					tr.Scroll(float32(size[0]/2), float32(size[1]/2), 0, 300)
					tr.Frame()
					before := a.issueResultScroll.Y
					if before < 200 {
						t.Fatalf("response did not establish an actual retained scroll: %.1f", before)
					}
					if entry != "reload-response" {
						// Goals have no producer length restriction; this valid saved goal
						// puts the exception below the old offset on a newly opened page.
						issue.Goal = strings.Repeat("Saved context for this task.\n", 60)
					}
					execution.Status, execution.Detail = issueExecutionInterrupted, "Matching interrupted reason"
					if err := a.persist(); err != nil {
						t.Fatal(err)
					}
					if entry == "open-long-goal" {
						if err := tr.Click(msgs.ResultCloseResults()); err != nil {
							t.Fatal(err)
						}
						tr.Frame()
						clickAfterBoundedScroll(t, tr, msgs.WorkReadResult(), size[0], size[1])
					} else if err := tr.Click(msgs.ResultReloadResult()); err != nil {
						t.Fatal(err)
					}
					tr.Frame()
					saved, err := openContactStore(a.store.path, a.workDir)
					if err != nil {
						t.Fatal(err)
					}
					seen := saved.state.Issues[0].Executions[0].AttentionSeenStatus
					attentionTargetEvidence(t, tr, fmt.Sprintf("result-%s-%d-%s", locale, size[0], entry), issueExecutionText(msgs, execution.Status), execution.Detail, seen, before, a.issueResultScroll.Y, size[0], size[1])
					if seen != issueExecutionInterrupted || execution.AttentionSeenStatus != seen || a.issueResult.ExecutionID != execution.ID || a.rt != nil || a.resultInFlight != 0 {
						t.Fatal("result entry lost current identity/persisted seen or started Runtime work")
					}
				})
			}
		}
	}
}

func TestIssueAttentionVisibleTargetKeepsOrdinaryReloadPosition(t *testing.T) {
	for _, kind := range []string{"seen-interrupted", "newly-seen-interrupted", "completed"} {
		t.Run(kind, func(t *testing.T) {
			status, reason := issueExecutionInterrupted, "Already displayed interruption"
			if kind == "completed" {
				status, reason = issueExecutionCompleted, ""
			}
			a, ct, issue, execution := attentionDisclosureApp(t, status, reason)
			if kind == "seen-interrupted" {
				execution.AttentionSeenStatus = status
			}
			if err := a.persist(); err != nil {
				t.Fatal(err)
			}
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			clickAfterBoundedScroll(t, tr, "Read latest result", 1040, 720)
			tr.Frame()
			page := a.issueResult
			response := strings.Repeat("Recorded readable response.\n\n", 100)
			a.applyIssueResultLoaded(nil, a.connectionGeneration, page.Generation, ct.ID, issue.ID, execution.ID, execution.SessionID, execution.TaskID, issueResultDerivation{}, response, "")
			tr.Frame()
			tr.Scroll(520, 360, 0, 300)
			tr.Frame()
			before := a.issueResultScroll.Y
			if before < 200 {
				t.Fatal("long body did not establish a real scroll")
			}
			saved, err := os.ReadFile(a.store.path)
			if err != nil {
				t.Fatal(err)
			}
			page = a.issueResult
			a.applyIssueResultLoaded(nil, a.connectionGeneration, page.Generation, ct.ID, issue.ID, execution.ID, execution.SessionID, execution.TaskID, issueResultDerivation{}, response, "")
			tr.Frame()
			if a.issueResultScroll.Y != before {
				t.Fatal("passive result completion repositioned the view")
			}
			if err := tr.Click("Reload result"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			after, err := os.ReadFile(a.store.path)
			if err != nil {
				t.Fatal(err)
			}
			if a.issueResultScroll.Y != before || string(saved) != string(after) || a.rt != nil || a.resultInFlight != 0 || a.issueResult.Panel.Response != response {
				t.Fatal("ordinary reload scrolled, persisted seen, altered the readable body or started Runtime work")
			}
		})
	}
}
