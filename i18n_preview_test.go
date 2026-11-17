package main

import (
	"bytes"
	"encoding/json"
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

func TestPreviewHelperBrand(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR for the helper brand previews")
	}

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

func TestPreviewMarkdownLinks(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write the whole-app Markdown link previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const paragraph = "Read [the design note](https://example.invalid/design) with `inline code` and surrounding text. 中文正文保留原文与链接。\n"
	type scene struct {
		locale, theme string
		width, height int
		streaming     bool
		custom        bool
	}
	scenes := make([]scene, 0, 11)
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, theme := range []string{appearanceLight, appearanceDark} {
			for _, size := range [][2]int{{720, 640}, {1040, 720}} {
				scenes = append(scenes, scene{locale: locale, theme: theme, width: size[0], height: size[1]})
			}
		}
	}
	for _, theme := range []string{appearanceLight, appearanceDark} {
		scenes = append(scenes, scene{locale: "en", theme: theme, width: 1040, height: 720, streaming: true})
	}
	scenes = append(scenes, scene{locale: "zh-Hans", theme: appearanceLight, width: 1040, height: 720, custom: true})
	for _, item := range scenes {
		state, caption := "ordinary", "Ordinary / 普通正文"
		if item.streaming {
			state, caption = "streaming", "Streaming / 流式正文"
		}
		theme := item.theme
		if item.custom {
			theme = "custom-light-accent"
		}
		name := fmt.Sprintf("markdown-links-paragraph-%s-%s-%s-%dx%d", item.locale, theme, state, item.width, item.height)
		t.Run(name, func(t *testing.T) {
			a, ct, _ := piRunStartedTestApp(t)
			ct.ActiveTaskID, ct.SendState = "", sendIdle
			a.storePath, a.workDir = a.store.path, filepath.Dir(a.store.path)
			a.windowWidth, a.scrollTo, a.workspaceOpen = item.width, -1, false
			a.appearanceMode = item.theme
			a.languageMode, a.languageModeLoaded = item.locale, true
			wantThemeID := "paper"
			if item.theme == appearanceDark {
				wantThemeID = "night"
			}
			if item.custom {
				wantThemeID = "preview-light-accent"
				syntheticLightPalette := resolveWorkbenchTheme(false)
				a.appearanceThemes = customThemeCatalog{Themes: []customTheme{{
					ID: wantThemeID, Name: "Custom theme with built-in light accent", Kind: appearanceLight,
					Palette: syntheticLightPalette,
				}}}
				a.appearanceThemeIDs = appearanceThemeIDs{Light: wantThemeID}
			}
			source := caption + "\n\n" + paragraph
			ct.Draft = "Draft preserved / 保留草稿"
			ct.Messages = []*message{{Key: "preview-markdown-link", Kind: msgContact, Text: source, Streaming: item.streaming}}
			tr := ui.NewTester(a.view, item.width, item.height)
			tr.SetLocale(item.locale)
			tr.SetDark(item.theme == appearanceDark)
			tr.Move(-1, -1)
			tr.Frame()
			msgs := locales.In(item.locale)
			viewport, viewportOK := tr.Find(msgs.ConversationMessagesLabel())
			link, linkOK := tr.Find("the design note")
			if !viewportOK || !linkOK || !tr.HasText("the design note") || link.W <= 0 || link.H <= 0 ||
				link.X < 0 || link.Y < 0 || link.X+link.W > float32(item.width) || link.Y+link.H > float32(item.height) ||
				link.X < viewport.X || link.Y < viewport.Y || link.X+link.W > viewport.X+viewport.W || link.Y+link.H > viewport.Y+viewport.H {
				t.Fatalf("parsed paragraph link is not visible: link=%+v found=%v viewport=%+v found=%v texts=%q", link, linkOK, viewport, viewportOK, tr.Texts())
			}
			resolved := a.resolveAppearanceTheme(item.theme == appearanceDark)
			if resolved.Unavailable || resolved.RequestedID != wantThemeID || resolved.EffectiveID != wantThemeID {
				t.Fatalf("preview theme did not resolve its selected provenance: %+v", resolved)
			}
			if opened := tr.OpenedURLs(); len(opened) != 0 {
				t.Fatalf("baseline activated a link: %q", opened)
			}
			path := filepath.Join(dir, name+".png")
			t.Logf("Markdown link preview path=%s source=%q locale=%s theme=%s requested=%s effective=%s width=%d height=%d streaming=%v synthetic_custom_catalog=%v accent=%+v pointer=(-1,-1) link=%+v viewport=%+v", path, source, item.locale, theme, resolved.RequestedID, resolved.EffectiveID, item.width, item.height, item.streaming, item.custom, resolved.Palette.Theme.Accent, link, viewport)
			writePreviewPNG(t, path, tr.Image())
			if err := tr.Click(msgs.CopyMessageLabel()); err != nil {
				t.Fatal(err)
			}
			if got := tr.Clipboard(); got != source {
				t.Fatalf("message copy changed the original Markdown: got=%q want=%q", got, source)
			}
			if ct.Draft != "Draft preserved / 保留草稿" || ct.Messages[0].Text != source || ct.Messages[0].Streaming != item.streaming {
				t.Fatal("copy changed the draft or preview message")
			}
			if opened := tr.OpenedURLs(); len(opened) != 0 {
				t.Fatalf("copy activated a link: %q", opened)
			}
		})
	}
}

func TestPreviewInputStates(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write whole-app input state previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	type scene struct {
		theme, field, state string
		custom              bool
	}
	scenes := make([]scene, 0, 16)
	for _, theme := range []string{appearanceLight, appearanceDark} {
		for _, state := range []string{"rest", "hover", "keyboard-focus", "invalid-focus", "disabled"} {
			scenes = append(scenes, scene{theme: theme, field: "contact-name", state: state})
		}
		scenes = append(scenes,
			scene{theme: theme, field: "contact-instructions", state: "keyboard-focus"},
			scene{theme: theme, field: "connection-endpoint", state: "keyboard-focus"},
		)
	}
	for _, state := range []string{"rest", "keyboard-focus"} {
		scenes = append(scenes, scene{theme: appearanceLight, field: "contact-name", state: state, custom: true})
	}
	for _, item := range scenes {
		themeName := item.theme
		if item.custom {
			themeName = "custom-light-matching-border"
		}
		name := fmt.Sprintf("inputs-%s-%s-%s-1040x960", themeName, item.field, item.state)
		t.Run(name, func(t *testing.T) {
			a, ct, _ := piRunStartedTestApp(t)
			ct.ActiveTaskID, ct.SendState = "", sendIdle
			ct.Name, ct.Purpose = "Input fixture", "Original purpose / 原始用途"
			ct.Instructions = "Original instructions / 原始指令\nKeep the exact source and spacing."
			ct.Draft = "Pending conversation draft / 待发送草稿"
			a.pi = nil
			a.storePath, a.workDir = a.store.path, filepath.Dir(a.store.path)
			a.windowWidth, a.scrollTo, a.workspaceOpen = 1040, -1, false
			a.appearanceMode = item.theme
			a.languageMode, a.languageModeLoaded = "en", true
			cfg := connectionConfig{
				Version: 1, API: connectionAPIChatCompletions,
				Endpoint: "https://input-fixture.example.invalid/v1", APIKey: "fixture-not-a-real-key", Model: "input-fixture",
				ContextTokens: 131072, OutputTokens: 8192, WorkingDirectory: a.workDir, MaxActiveTasks: 2,
			}
			a.connectionSaved = &cfg
			a.connectionForm = newConnectionSettings(&cfg, cfg.WorkingDirectory)
			wantThemeID := "paper"
			if item.theme == appearanceDark {
				wantThemeID = "night"
			}
			if item.custom {
				wantThemeID = "preview-input-custom"
				document := customThemeDocument(wantThemeID, appearanceLight)
				colors := document["colors"].(map[string]any)
				colors["border"], colors["accent"] = "#E6E6E6", "#245B78"
				themeDir := t.TempDir()
				writeCustomTheme(t, themeDir, "input-custom.json", document)
				a.appearanceThemes = loadCustomThemes(themeDir)
				if len(a.appearanceThemes.Errors) != 0 {
					t.Fatalf("custom input fixture did not decode: %+v", a.appearanceThemes.Errors)
				}
				a.appearanceThemeIDs = appearanceThemeIDs{Light: wantThemeID}
			}
			tab := settingsTabContact
			if item.field == "connection-endpoint" {
				tab = settingsTabConnection
			}
			a.openSettingsTab(tab, settingsSourceRail)
			editor := a.ensureSettingsProfileEditor(ct.ID)
			if item.state == "invalid-focus" {
				editor.Errors = map[string]string{"Name": profileNoticeNameRequired}
			}
			if item.state == "disabled" {
				editor.Busy = true
			}
			tr := ui.NewTester(a.view, 1040, 960)
			tr.SetLocale("en")
			tr.SetDark(item.theme == appearanceDark)
			tr.SetFocused(true)
			tr.Move(-1, -1)
			tr.Frame()
			msgs := locales.In("en")
			label := msgs.FieldNameAccessible()
			if item.field == "contact-instructions" {
				label = msgs.FieldInstructionsAccessible()
			} else if item.field == "connection-endpoint" {
				label = msgs.ConnectionFieldEndpoint()
			}
			viewport := ui.Rect{W: float32(tr.Image().Bounds().Dx()), H: float32(tr.Image().Bounds().Dy())}
			contains := func(outer, inner ui.Rect) bool {
				return inner.W > 0 && inner.H > 0 && inner.X >= outer.X && inner.Y >= outer.Y &&
					inner.X+inner.W <= outer.X+outer.W && inner.Y+inner.H <= outer.Y+outer.H
			}
			var inputBounds *ui.Rect
			if item.field != "connection-endpoint" {
				box, found := tr.Find(label)
				if !found || !contains(viewport, box) || box.H < 32 {
					t.Fatalf("actual uniquely named input is not visible: label=%q box=%+v found=%v", label, box, found)
				}
				inputBounds = &box
			}
			baselineActive, baselineNoIME := tr.TextInputActive()
			baselineCaret, baselineCaretActive := tr.TextCaret()
			baseline := map[string]any{
				"pointer": [2]float32{-1, -1}, "target_focused": tr.Focused(label),
				"text_input_active": baselineActive, "no_input_method": baselineNoIME,
				"caret": baselineCaret, "caret_active": baselineCaretActive,
				"control_bounds": inputBounds, "window_viewport": viewport, "scroll_state": a.settingsScroll,
			}
			wantProfile := cloneProfileDraft(editor.Draft)
			wantContact := contactProfileDraft(ct)
			wantConnection, wantSaved := a.connectionForm.Draft, cfg
			wantConversationDraft := ct.Draft
			storeBefore, err := os.ReadFile(a.store.path)
			if err != nil {
				t.Fatal(err)
			}
			focusInput := func() {
				for range 48 {
					active, _ := tr.TextInputActive()
					if tr.Focused(label) && active {
						return
					}
					tr.Key(0, ui.KeyTab)
					tr.Frame()
				}
				t.Fatalf("actual text controller was not keyboard reachable: label=%q", label)
			}
			var disclosureBounds *ui.Rect
			if item.field == "connection-endpoint" {
				if !tr.HasText(cfg.Endpoint) {
					t.Fatal("saved endpoint value is not present before its real disclosure action")
				}
				action := msgs.ConnectionEditField(label)
				box, found := tr.Find(action)
				if !found || !contains(viewport, box) {
					t.Fatalf("endpoint disclosure is not visible: %+v", box)
				}
				disclosureBounds = &box
				if err := tr.Click(action); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				focusInput()
			} else {
				switch item.state {
				case "hover":
					tr.Move(inputBounds.X+inputBounds.W/2, inputBounds.Y+inputBounds.H/2)
				case "keyboard-focus", "invalid-focus":
					focusInput()
				case "disabled":
					tr.ClickAt(inputBounds.X+inputBounds.W/2, inputBounds.Y+inputBounds.H/2)
					if active, _ := tr.TextInputActive(); active || tr.Focused(label) {
						t.Fatal("disabled input acquired text focus")
					}
					tr.Type("must not enter a disabled field")
				}
			}
			tr.Frame()
			if inputBounds != nil {
				box, found := tr.Find(label)
				if !found || box != *inputBounds {
					t.Fatalf("focus/hover changed actual input geometry: before=%+v after=%+v found=%v", *inputBounds, box, found)
				}
			}
			if !profileDraftEqual(editor.Draft, wantProfile) || a.connectionForm.Draft != wantConnection {
				t.Fatal("input focus/hover/disabled targeting changed a draft")
			}
			active, noIME := tr.TextInputActive()
			caret, caretActive := tr.TextCaret()
			if item.state == "keyboard-focus" || item.state == "invalid-focus" {
				if !tr.Focused(label) || !active || !caretActive || !contains(viewport, caret) {
					t.Fatalf("target is not an active visible text controller: label=%q active=%v caret=%+v", label, active, caret)
				}
				if inputBounds != nil && !contains(*inputBounds, caret) {
					t.Fatalf("caret escaped its actual input bounds: caret=%+v input=%+v", caret, *inputBounds)
				}
			}
			resolved := a.resolveAppearanceTheme(item.theme == appearanceDark)
			if resolved.Unavailable || resolved.EffectiveID != wantThemeID || resolved.RequestedID != wantThemeID {
				t.Fatalf("actual appearance resolver did not select the scene theme: %+v", resolved)
			}
			captionBounds, captionFound := tr.Find(label)
			receipt := map[string]any{
				"baseline": baseline, "field": item.field, "state": item.state,
				"theme_requested": resolved.RequestedID, "theme_effective": resolved.EffectiveID,
				"synthetic_custom_document": item.custom, "synthetic_busy": item.state == "disabled",
				"synthetic_error": item.state == "invalid-focus", "provider_readiness_claimed": false,
				"target_label": label, "target_focused": tr.Focused(label), "text_input_active": active,
				"no_input_method": noIME, "caret": caret, "caret_active": caretActive,
				"control_bounds": inputBounds, "window_viewport": viewport, "scroll_state": a.settingsScroll,
				"caption_or_label_bounds": captionBounds, "caption_or_label_found": captionFound,
				"connection_disclosure_bounds":        disclosureBounds,
				"connection_editor_bounds_observable": item.field != "connection-endpoint",
				"connection_bounds_limit":             "Connection caption and editor share a label; its caption is not an editor-bound observation",
				"input_border_role":                   resolved.Palette.InputBorder, "accent_role": resolved.Palette.Theme.Accent,
				"noninput_border_role": resolved.Palette.Theme.Border,
			}
			pointer := [2]float32{-1, -1}
			if item.state == "hover" || item.state == "disabled" {
				pointer = [2]float32{inputBounds.X + inputBounds.W/2, inputBounds.Y + inputBounds.H/2}
			} else if disclosureBounds != nil {
				pointer = [2]float32{disclosureBounds.X + disclosureBounds.W/2, disclosureBounds.Y + disclosureBounds.H/2}
			}
			receipt["pointer"] = pointer
			writePreviewPNG(t, filepath.Join(dir, name+".png"), tr.Image())
			if item.state == "keyboard-focus" || item.state == "invalid-focus" {
				original := editor.Draft.Name
				typed := "  Exact draft 原文 : []() and spaces  "
				if item.field == "contact-instructions" {
					original = editor.Draft.Instructions
					typed = "  Exact instructions 原文\nSecond line : []()\nTrailing spaces  "
				} else if item.field == "connection-endpoint" {
					original = a.connectionForm.Draft.Endpoint
					typed = "https://typed.example.invalid/v1?source=raw&mode=exact"
				}
				tr.Key(ui.Cmd, ui.KeyA)
				tr.Type(typed)
				tr.Key(ui.Cmd, ui.KeyA)
				tr.Key(ui.Cmd, ui.KeyC)
				tr.Frame()
				got := editor.Draft.Name
				if item.field == "contact-instructions" {
					got = editor.Draft.Instructions
				} else if item.field == "connection-endpoint" {
					got = a.connectionForm.Draft.Endpoint
				}
				if got != typed || tr.Clipboard() != typed || !tr.Focused(label) {
					t.Fatalf("actual input typing/selection changed source: draft=%q clipboard=%q want=%q", got, tr.Clipboard(), typed)
				}
				receipt["typed_source"], receipt["copied_source"] = typed, tr.Clipboard()
				tr.Key(ui.Cmd, ui.KeyA)
				tr.Type(original)
				tr.Frame()
			}
			if !profileDraftEqual(editor.Draft, wantProfile) || a.connectionForm.Draft != wantConnection ||
				!profileDraftEqual(contactProfileDraft(ct), wantContact) || ct.Draft != wantConversationDraft ||
				a.connectionSaved == nil || *a.connectionSaved != wantSaved {
				t.Fatal("input interactions did not preserve the exact drafts and saved source after restoration")
			}
			if a.connectionApplying || a.connectionTesting || a.connectionSaveInFlight || a.connectionProbe != nil || a.pi != nil || len(tr.OpenedURLs()) != 0 {
				t.Fatal("input-only interaction triggered a connection or external action")
			}
			storeAfter, err := os.ReadFile(a.store.path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(storeBefore, storeAfter) {
				t.Fatal("input focus/hover/typing changed persisted product data")
			}
			receipt["drafts_restored_exactly"], receipt["saved_source_unchanged"] = true, true
			receipt["provider_action_observed"] = false
			data, err := json.MarshalIndent(receipt, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Logf("whole-app input preview=%s receipt=%s synthetic_fixture=true native_or_IME_acceptance=false", filepath.Join(dir, name+".png"), filepath.Join(dir, name+".json"))
		})
	}
}
