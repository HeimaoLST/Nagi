package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

// TestPreviewI18nSelectedSlice is the I1 headless preview for the selected
// Settings and copy slice. It is not native-window evidence and does not
// expand into the final AC3 preview matrix.
func TestPreviewI18nSelectedSlice(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write the I1 i18n previews")
	}
	for _, locale := range []string{"en", "zh-Hans", "en-XA"} {
		for _, dark := range []bool{false, true} {
			theme := "light"
			if dark {
				theme = "dark"
			}
			a, _ := workspaceTestApp(t)
			a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
			a.windowWidth = 1040
			tr := ui.NewTester(a.view, 1040, 720)
			tr.SetLocale(locale)
			tr.SetDark(dark)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("settings-%s-%s-1040x720.png", locale, theme)), tr.Image())

			chat := testApp(t)
			chat.windowWidth = 1040
			chatTr := ui.NewTester(chat.view, 1040, 720)
			chatTr.SetLocale(locale)
			chatTr.SetDark(dark)
			chatTr.Frame()
			showPreviewTooltip(t, chatTr, locales.In(locale).CopyMessageLabel())
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("copy-%s-%s-1040x720.png", locale, theme)), chatTr.Image())
		}
	}

	a, _ := workspaceTestApp(t)
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	a.windowWidth = 960
	tr := ui.NewTester(a.view, 960, 640)
	tr.SetLocale("zh-Hans")
	tr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "settings-zh-Hans-light-960x640.png"), tr.Image())

	history, first := workspaceTestApp(t)
	first.History = []historyEntry{
		{ID: "h1", Date: "10月30日", From: "contact", Text: "这是一段足够长的中文历史内容，用来检查右侧历史列表的换行、密度与按钮是否完整。"},
	}
	history.openHistory()
	historyTr := ui.NewTester(history.view, 1040, 720)
	historyTr.SetLocale("zh-Hans")
	historyTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "history-zh-Hans-light-1040x720.png"), historyTr.Image())

	fullApp, fullContact := workspaceTestApp(t)
	fullEntry := historyEntry{ID: "h-full", Date: "10月30日", From: "contact", Text: "完整历史原文，保持原样，不做任何翻译。"}
	fullContact.History = []historyEntry{fullEntry}
	fullApp.openHistoryEntryFull(fullEntry)
	fullTr := ui.NewTester(fullApp.view, 1040, 720)
	fullTr.SetLocale("zh-Hans")
	fullTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "history-full-zh-Hans-light-1040x720.png"), fullTr.Image())
	fullTr.SetLocale("en")
	fullTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "history-full-en-light-1040x720.png"), fullTr.Image())

	cfg := connectionConfig{Version: 1, API: connectionAPIChatCompletions, Endpoint: "https://provider.example.invalid/v1", APIKey: "fixture", Model: "fixture", ContextTokens: 131072, OutputTokens: 8192, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
	conn, _ := workspaceTestApp(t)
	conn.connectionForm = newConnectionSettings(&cfg, cfg.WorkingDirectory)
	conn.connectionForm.Status = connStatusApplied
	conn.openSettingsTab(settingsTabConnection, settingsSourceRail)
	connTr := ui.NewTester(conn.view, 1040, 720)
	connTr.SetLocale("zh-Hans")
	connTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "connection-zh-Hans-light-1040x720.png"), connTr.Image())

	model, _ := workspaceTestApp(t)
	model.modelForm = newModelSelection(selectionTarget(), selectionCatalog())
	model.aux = "model"
	modelTr := ui.NewTester(model.view, 1040, 720)
	modelTr.SetLocale("zh-Hans")
	modelTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "model-zh-Hans-light-1040x720.png"), modelTr.Image())

	runtimeApp, _ := workspaceTestApp(t)
	runtimeApp.runtimeStatus = runtimeStateFailed
	runtimeApp.runtimeStage = "Connection configuration"
	runtimeApp.runtimeErrKind = runtimeNoticeConnectionRequired
	runtimeApp.runtimeHintKind = startupHintEnterConnection
	runtimeApp.openAdvancedSettings()
	runtimeTr := ui.NewTester(runtimeApp.view, 1040, 720)
	runtimeTr.SetLocale("zh-Hans")
	runtimeTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "runtime-startup-zh-Hans-light-1040x720.png"), runtimeTr.Image())
}

func showPreviewTooltip(t *testing.T, tr *ui.Tester, label string) {
	t.Helper()
	r, ok := tr.Find(label)
	if !ok {
		t.Fatalf("preview control %q not found: %v", label, tr.Texts())
	}
	tr.Move(1, 1)
	tr.Move(r.X+r.W/2, r.Y+r.H/2)
	tr.Advance(700 * time.Millisecond)
	tr.Frame()
}

func TestPreviewModelValidationAndHelperBrand(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR for the model validation and helper brand previews")
	}
	model, _ := workspaceTestApp(t)
	model.modelForm = newModelSelection(selectionTarget(), selectionCatalog())
	model.modelForm.Draft.Model = ""
	model.modelForm.Request()
	model.aux = "model"
	modelTr := ui.NewTester(model.view, 1040, 720)
	modelTr.SetLocale("zh-Hans")
	modelTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "model-validation-zh-Hans-light-1040x720.png"), modelTr.Image())

	helper, _ := workspaceTestApp(t)
	helper.runtimeStatus = runtimeStateFailed
	helper.runtimeStage = "Runtime helper"
	helper.runtimeErrKind = runtimeNoticeHelperMissing
	helper.runtimeHintKind = startupHintHelperInstall
	helper.runtimeHintArgs = []string{"/Applications/Saki.app/Contents/MacOS/Saki-runtime, /usr/local/bin/Saki-runtime"}
	helper.openAdvancedSettings()
	helperTr := ui.NewTester(helper.view, 1040, 720)
	helperTr.SetLocale("zh-Hans")
	helperTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "runtime-helper-zh-Hans-light-1040x720.png"), helperTr.Image())
}

func TestPreviewIssueWorkLocalized(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR for the localized issue/work previews")
	}
	list := issuePreviewApp("expanded")
	list.windowWidth = 1040
	list.selectedContact().issueNavigation().Filter = issueFilterAll
	list.setWorkspaceOpen(true)
	listTr := ui.NewTester(list.view, 1040, 720)
	listTr.SetLocale("zh-Hans")
	listTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "issue-overview-zh-Hans-light-1040x720.png"), listTr.Image())

	detail := issuePreviewApp("expanded")
	detail.windowWidth = 1040
	issue := detail.selectedContact().Issues[0]
	detail.selectedContact().issueNavigation().DetailID = issue.ID
	detail.issueDetailOpen = map[string]bool{issue.ID: true}
	detail.setWorkspaceOpen(true)
	detailTr := ui.NewTester(detail.view, 1040, 720)
	detailTr.SetLocale("zh-Hans")
	detailTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "issue-detail-zh-Hans-light-1040x720.png"), detailTr.Image())
}

func TestPreviewPhase5bLocalized(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR for the localized context/result previews")
	}
	tc := &taskContext{}
	if _, err := tc.AddSnippet("Release notes", "Line one"); err != nil {
		t.Fatal(err)
	}
	tc.openSnippetForm()
	tc.formName = ""
	tc.formError = errTaskContextName
	contextTr := taskContextTester(tc, false)
	contextTr.SetLocale("zh-Hans")
	contextTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "task-context-error-zh-Hans-light-1040x720.png"), contextTr.Image())

	state := &issueResultPanelState{
		RunNumber: 1, RunGoal: "Ship results", RunStatus: issueExecutionCompleted,
		Response: "Recorded result body.",
		Files:    []issueResultFile{{Path: "/tmp/result.md", WorkingDirectory: "/tmp", ToolName: "write", CallID: "call-1"}},
	}
	resultTr := issueResultPanelTester(state, issueResultPanelActions{}, 1040, 720, false)
	resultTr.SetLocale("zh-Hans")
	resultTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "result-panel-zh-Hans-light-1040x720.png"), resultTr.Image())
}

// TestPreviewI18nFinalMatrix is the bounded final E preview. It is headless
// layout evidence only, not a native window, IME, or human visual review.
func TestPreviewI18nFinalMatrix(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write the final bounded i18n previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}

	type scene struct {
		name          string
		width, height int
		setup         func(locale string) *appModel
		want          func(locales.Messages) []string
		fit           func(locales.Messages) []string
		check         func(t *testing.T, a *appModel, tr *ui.Tester, msgs locales.Messages)
	}
	settingsFit := func(msgs locales.Messages) []string {
		return []string{msgs.SettingsBackAccessible(), msgs.SettingsTitle()}
	}
	languageMode := func(locale string) string {
		if locale == "zh-Hans" {
			return "zh-Hans"
		}
		return "en"
	}

	overviewRows := func(a *appModel) {
		ct := a.selectedContact()
		ct.SelectedIssueID = ""
		nav := ct.issueNavigation()
		nav.DetailID = ""
		nav.Filter = issueFilterAll
		for index := 0; index < 10; index++ {
			ct.Issues = append(ct.Issues, &productIssue{
				ID: fmt.Sprintf("final-overview-%02d", index), ContactID: ct.ID, Status: issueStatusInProgress,
				Goal:          fmt.Sprintf("Longer overview goal %02d for the final language and theme matrix", index+1),
				Requirements:  parseIssueRequirements("Keep this representative row visible."),
				WorkSessionID: "final-overview", RequirementsRevision: 1,
			})
		}
		a.setWorkspaceOpen(true)
	}

	scenes := []scene{
		{
			name: "runtime-empty", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a, ct := workspaceTestApp(t)
				a.workspaceOpen = false
				a.rt = &runtimeClient{}
				a.runtimeStatus = runtimeStateStarting
				ct.Messages = nil
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.RuntimeConnecting(), msgs.EmptyNoMessages(), msgs.ComposerLabel("Saki")}
			},
			fit: func(msgs locales.Messages) []string { return []string{msgs.ComposerLabel("Saki")} },
		},
		{
			name: "config-failure", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a, ct := workspaceTestApp(t)
				a.workspaceOpen = false
				a.runtimeStatus = runtimeStateFailed
				a.runtimeStage = "Runtime configuration"
				a.runtimeErrKind = runtimeNoticeAPIKey
				a.runtimeHintKind = startupHintSetAPIKey
				a.runtimeHintArgs = []string{"/Users/you/Workspace/saki-run"}
				ct.Draft = "Keep this draft while I fix configuration."
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.ConnectionStatusNotConnected(), msgs.ConnectionRecoveryNotConnectedHint()}
			},
			fit: func(msgs locales.Messages) []string {
				return []string{msgs.ComposerLabel("Saki"), msgs.ConfigurationDetails()}
			},
		},
		{
			name: "question", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a := issuePreviewApp("issue-question")
				a.windowWidth = 1040
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.WorkAnswer(), "Which verification method should I use before continuing this implementation?"}
			},
			fit: func(msgs locales.Messages) []string { return []string{msgs.WorkClose(), msgs.WorkAnswer()} },
		},
		{
			name: "trueoverview", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a := issuePreviewApp("issue-overview")
				a.windowWidth = 1040
				overviewRows(a)
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.IssueNewIssue(), msgs.IssueFilterAll(), "Longer overview goal 01 for the final language and theme matrix"}
			},
			fit: func(msgs locales.Messages) []string { return []string{msgs.IssueNewIssue(), msgs.IssueFilterAll()} },
		},
		{
			name: "960detail", width: 960, height: 640,
			setup: func(locale string) *appModel {
				a := issuePreviewApp("issue-detail")
				a.windowWidth = 960
				ct := a.selectedContact()
				issue := ct.selectedIssue()
				a.issueDetailOpen = map[string]bool{issue.ID: true}
				a.setWorkspaceOpen(true)
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.WorkTask(), msgs.IssueHideActions(), msgs.IssueExecutionHistory()}
			},
			fit: func(msgs locales.Messages) []string {
				return []string{msgs.WorkClose(), msgs.ComposerLabel("Saki")}
			},
			check: func(t *testing.T, a *appModel, tr *ui.Tester, msgs locales.Messages) {
				t.Helper()
				if a.detailPageActive() || !a.avatarRailActive() {
					t.Fatalf("960 detail geometry = page=%v avatar=%v", a.detailPageActive(), a.avatarRailActive())
				}
				composer, ok := tr.Find(msgs.ComposerLabel("Saki"))
				if !ok || composer.W-4 < 440 {
					t.Fatalf("960 composer content = %+v ok=%v, want a real center of at least 440", composer, ok)
				}
			},
		},
		{
			name: "navigation-reader", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a := issuePreviewApp("issue-detail")
				a.windowWidth = 1040
				ct := a.selectedContact()
				issue := ct.selectedIssue()
				execution := issue.Executions[0]
				file := issueResultFile{ExecutionID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID, MessageID: execution.MessageID, CallID: "final-reader", ToolName: "write", Path: "/Users/you/Workspace/saki-run/final-reader-result.md", WorkingDirectory: "/Users/you/Workspace/saki-run", RawResult: `{"path":"/Users/you/Workspace/saki-run/final-reader-result.md","bytes_written":1}`}
				key := issueResultFileKey(file)
				longPreview := "# Final reader preview\n\nA long saved file line that must remain readable in both languages.\n"
				for index := 0; index < 40; index++ {
					longPreview += fmt.Sprintf("- reader line %02d keeps the raw saved content visible\n", index+1)
				}
				a.aux = "issue-result"
				a.issueResult = issueResultPage{
					Open: true, OwnerContactID: ct.ID, IssueID: issue.ID, ExecutionID: execution.ID,
					Panel: issueResultPanelState{
						RunNumber: 1, RunStatus: issueExecutionCompleted, Files: []issueResultFile{file},
						Inspection: &issueResultFileInspection{Exists: true, Regular: true, Preview: longPreview},
						InspectFor: key, ReaderKey: key,
					},
				}
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.ReaderCheckAgainLabel(), msgs.ResultBackResults(), msgs.ResultCurrentFileDiff()}
			},
			fit: func(msgs locales.Messages) []string {
				return []string{msgs.ResultBackResults(), msgs.ReaderCheckAgainLabel()}
			},
		},
		{
			name: "settings-general", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a, _ := workspaceTestApp(t)
				a.languageMode = languageMode(locale)
				a.languageModeLoaded = true
				a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.SettingsGeneral(), msgs.SettingsAppearance(), msgs.SettingsLanguage(), msgs.LanguageHint()}
			},
			fit: settingsFit,
		},
		{
			name: "settings-connection", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a, _ := workspaceTestApp(t)
				cfg := connectionConfig{Version: 1, API: connectionAPIChatCompletions, Endpoint: "https://provider.example.invalid/v1/very/long/endpoint/for/final/preview", APIKey: "fixture", Model: "provider/very-long-model-name-for-final-preview", ContextTokens: 131072, OutputTokens: 8192, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
				a.connectionForm = newConnectionSettings(&cfg, cfg.WorkingDirectory)
				a.connectionSaved = &cfg
				a.connectionEffective = &cfg
				a.connectionForm.Status = connStatusApplied
				a.openSettingsTab(settingsTabConnection, settingsSourceRail)
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.SettingsConnection(), msgs.ConnectionSaveConfiguration(), msgs.ConnectionApplySaved()}
			},
			fit: settingsFit,
		},
		{
			name: "settings-contact", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a, ct := workspaceTestApp(t)
				ct.Name = "Alexandria-Longname-Contact-For-Final-Review"
				ct.Purpose = "Keep a long contact purpose visible without horizontal clipping in the final preview."
				ct.Instructions = "Keep the exact contact instructions and workspace values readable in both languages."
				ct.Workspace = "/Users/you/Workspace/a/very/long/contact/workspace/path/for/final/preview"
				a.openSettingsTab(settingsTabContact, settingsSourceRail)
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.SettingsContact(), msgs.FieldNameAccessible(), msgs.ProfilePurpose()}
			},
			fit: settingsFit,
		},
		{
			name: "settings-runtime", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a, _ := workspaceTestApp(t)
				a.runtimeStatus = runtimeStateFailed
				a.runtimeStage = "Runtime helper"
				a.runtimeErrKind = runtimeNoticeHelperMissing
				a.runtimeHintKind = startupHintHelperInstall
				a.runtimeHintArgs = []string{"/Applications/Nagi.app/Contents/MacOS/Saki-runtime, /usr/local/bin/Saki-runtime"}
				a.runtimeBinary = "/Applications/Nagi.app/Contents/MacOS/Saki-runtime"
				a.storePath = "/Users/you/Library/Application Support/Saki/contacts.json"
				a.sessionDir = "/Users/you/Library/Application Support/Saki/sessions"
				a.workDir = "/Users/you/Workspace/saki-run"
				a.openAdvancedSettings()
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.SettingsAdvanced(), msgs.SettingsAdvancedIntro(), msgs.SettingsHideRuntimeDetails(), msgs.RuntimeStageRuntimeHelper(), msgs.StartupHintHelperInstall("/Applications/Nagi.app/Contents/MacOS/Saki-runtime, /usr/local/bin/Saki-runtime")}
			},
			fit: settingsFit,
		},
	}

	write := func(t *testing.T, base scene, locale, theme string, dark bool) {
		t.Helper()
		a := base.setup(locale)
		tr := ui.NewTester(a.view, base.width, base.height)
		tr.SetLocale(locale)
		tr.SetDark(dark)
		tr.Frame()
		msgs := locales.In(locale)
		for _, want := range base.want(msgs) {
			if !tr.HasText(want) {
				t.Fatalf("scene %s locale=%s missing %q: %v", base.name, locale, want, tr.Texts())
			}
		}
		for _, label := range base.fit(msgs) {
			r, ok := tr.Find(label)
			if !ok {
				t.Fatalf("scene %s locale=%s fit label %q not found", base.name, locale, label)
			}
			if r.X < 0 || r.W <= 0 || r.X+r.W > float32(base.width) {
				t.Fatalf("scene %s locale=%s label %q outside %d: %+v", base.name, locale, label, base.width, r)
			}
		}
		if base.check != nil {
			base.check(t, a, tr, msgs)
		}
		img := tr.Image()
		if img.Bounds().Dx() != base.width || img.Bounds().Dy() != base.height {
			t.Fatalf("scene %s image = %dx%d, want %dx%d", base.name, img.Bounds().Dx(), img.Bounds().Dy(), base.width, base.height)
		}
		path := filepath.Join(dir, fmt.Sprintf("final-%s-%s-%s-%dx%d.png", locale, theme, base.name, base.width, base.height))
		writePreviewPNG(t, path, img)
		settingsScroll, overviewScroll, detailScroll, readerScroll := float32(0), float32(0), float32(0), float32(0)
		if ct := a.selectedContact(); ct != nil {
			nav := ct.issueNavigation()
			overviewScroll, detailScroll = nav.OverviewScroll.Y, nav.DetailScroll.Y
		}
		settingsScroll = a.settingsScroll.Y
		readerScroll = a.issueResult.Panel.ReaderScroll.Y
		t.Logf("final preview path=%s width=%d height=%d locale=%s theme=%s scene=%s settingsScroll=%v overviewScroll=%v detailScroll=%v readerScroll=%v", path, base.width, base.height, locale, theme, base.name, settingsScroll, overviewScroll, detailScroll, readerScroll)
	}

	for _, base := range scenes {
		for _, mode := range []struct {
			locale string
			theme  string
			dark   bool
		}{{"en", "dark", true}, {"zh-Hans", "light", false}} {
			write(t, base, mode.locale, mode.theme, mode.dark)
		}
	}

	extra := []struct {
		scene string
		theme string
		dark  bool
	}{
		{"trueoverview", "dark", true},
		{"960detail", "light", false},
		{"settings-contact", "dark", true},
		{"settings-connection", "light", false},
		{"navigation-reader", "dark", true},
		{"settings-general", "light", false},
	}
	byName := map[string]scene{}
	for _, base := range scenes {
		byName[base.name] = base
	}
	for _, item := range extra {
		base, ok := byName[item.scene]
		if !ok {
			t.Fatalf("final pseudo scene %q is not in the base matrix", item.scene)
		}
		write(t, base, "en-XA", item.theme, item.dark)
	}
}
