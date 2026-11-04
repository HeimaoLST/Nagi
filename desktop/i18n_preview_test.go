package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
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
