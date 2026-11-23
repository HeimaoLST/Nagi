package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

// TestPreviewAdjustableLayout is an opt-in headless matrix for the global
// workbench layout. It is not native drag or native window evidence.
func TestPreviewAdjustableLayout(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write adjustable-layout previews")
	}
	if os.Getenv("NAGI_PREVIEW_RESPONSIVE_ONLY") != "1" {
		scenes := []string{"closed", "open", "dragged"}
		for _, dark := range []bool{false, true} {
			theme := "light"
			if dark {
				theme = "dark"
			}
			for _, scene := range scenes {
				for _, size := range []struct{ width, height int }{{1040, 720}, {1380, 900}, {960, 640}, {720, 640}} {
					a, ct := workspaceTestApp(t)
					a.windowWidth = size.width
					if scene != "closed" {
						a.openContactProfile(a.selectedID())
					}
					if scene == "dragged" && size.width >= 1040 {
						a.leftPaneWidth = defaultLeftPaneWidth
						a.rightPaneWidth = maxRightPaneWidth - 82
					}
					ct.Status = statusWorking
					ct.ActiveTaskID, ct.LastTaskID = "task-layout", "task-layout"
					w := a.ensureWork(ct, "task-layout", "message-layout")
					w.State = workRunning
					w.Prompt = "Layout preview"
					tr := ui.NewTester(a.view, size.width, size.height)
					tr.SetDark(dark)
					tr.Frame()
					writePreviewPNG(t, filepath.Join(dir,
						fmt.Sprintf("headless-%s-layout-%s-%dx%d.png", theme, scene, size.width, size.height)), tr.Image())
				}
			}
		}
	}
	writeResponsiveBoundaryPreviews(t, dir)
}

func writeResponsiveBoundaryPreviews(t *testing.T, dir string) {
	type scene struct {
		name, locale       string
		width, left, right float32
		workspaceOpen      bool
		dark               bool
		longName           string
	}
	scenes := []scene{
		{name: "960-default-en", locale: "en", width: 960, left: 200, right: 340, workspaceOpen: true, longName: "Long English contact name for responsive workbench review"},
		{name: "960-default-en-dark", locale: "en", width: 960, left: 200, right: 340, workspaceOpen: true, dark: true, longName: "Long English contact name for responsive workbench review"},
		{name: "960-max-zh", locale: "zh-Hans", width: 960, left: 280, right: 440, workspaceOpen: true, longName: "这是一个用于响应式工作台检查的较长中文联系人名称"},
		{name: "960-max-zh-dark", locale: "zh-Hans", width: 960, left: 280, right: 440, workspaceOpen: true, dark: true, longName: "这是一个用于响应式工作台检查的较长中文联系人名称"},
		{name: "1180-max-pseudo", locale: "en-XA", width: 1180, left: 280, right: 440, workspaceOpen: true, longName: "Expanded pseudo contact name for workbench review"},
		{name: "1180-max-pseudo-dark", locale: "en-XA", width: 1180, left: 280, right: 440, workspaceOpen: true, dark: true, longName: "Expanded pseudo contact name for workbench review"},
		{name: "899-zh-page", locale: "zh-Hans", width: 899, left: 280, right: 440, workspaceOpen: true, longName: "这是一个用于窄页面返回检查的较长中文联系人名称"},
		{name: "899-zh-page-dark", locale: "zh-Hans", width: 899, left: 280, right: 440, workspaceOpen: true, dark: true, longName: "这是一个用于窄页面返回检查的较长中文联系人名称"},
	}
	for _, sc := range scenes {
		a, ct := workspaceTestApp(t)
		a.windowWidth = int(sc.width)
		a.leftPaneWidth = sc.left
		a.rightPaneWidth = sc.right
		if sc.workspaceOpen {
			a.openContactProfile(a.selectedID())
		}
		ct.Name = sc.longName
		tr := ui.NewTester(a.view, int(sc.width), 640)
		tr.SetLocale(sc.locale)
		tr.SetDark(sc.dark)
		tr.Frame()
		theme := "light"
		if sc.dark {
			theme = "dark"
		}
		writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("headless-responsive-%s-%dx640.png", sc.name, int(sc.width))), tr.Image())
		t.Logf("preview scene=%s base=12c3382fcf5e149c10183129f41d805c1eebbb4d source=working-tree theme=%s locale=%s width=%.0f height=640", sc.name, theme, sc.locale, sc.width)
	}
}

func TestPreviewHeaderGrouping(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR for the bounded header grouping previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const longRole = "Assistant for release engineering, cross-functional verification and runtime diagnostics"
	write := func(name, locale, role string, dark bool, setup func(*appModel, *contact)) {
		t.Helper()
		a, ct := workspaceTestApp(t)
		a.windowWidth = 960
		ct.Role = role
		if setup != nil {
			setup(a, ct)
		}
		tr := ui.NewTester(a.view, 960, 640)
		tr.SetLocale(locale)
		tr.SetDark(dark)
		tr.Frame()
		if !tr.HasText(role) {
			t.Fatalf("header role %q missing: %v", role, tr.Texts())
		}
		img := tr.Image()
		if img.Bounds().Dx() != 960 || img.Bounds().Dy() != 640 {
			t.Fatalf("header preview image = %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
		}
		writePreviewPNG(t, filepath.Join(dir, name), img)
	}
	write("header-960-light-ready.png", "en", "Assistant", false, func(a *appModel, _ *contact) {
		a.pi = presentationPiHost(a)
		a.runtimeStatus = runtimeStateReady
	})
	write("header-960-dark-not-connected.png", "en", "Assistant", true, func(a *appModel, _ *contact) {
		cfg := connectionConfig{API: connectionAPIChatCompletions, Endpoint: "http://example.test/v1", APIKey: "fixture", Model: "fixture", ContextTokens: 1000, OutputTokens: 100, WorkingDirectory: "/Users/you/Workspace/saki-run", MaxActiveTasks: 2}
		a.runtimeStatus = runtimeStateExited
		a.runtimeStage = "Connection configuration"
		a.connectionSaved = &cfg
		a.connectionEffective = &cfg
		a.ensureConnectionForm()
	})
	write("header-960-dark-en-XA-long-role.png", "en-XA", longRole, true, func(a *appModel, _ *contact) {
		a.pi = presentationPiHost(a)
		a.runtimeStatus = runtimeStateReady
	})
}

func TestPreviewUnifiedTitleBar(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR for headless titlebar previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, size := range []struct{ width, height int }{{720, 640}, {960, 640}, {1040, 720}, {1180, 720}} {
			for _, scene := range []string{"chat", "settings", "profile"} {
				a := testApp(t)
				a.windowWidth = size.width
				a.workspaceOpen = false
				a.selectedContact().Draft = "Keep the draft while navigating."
				switch scene {
				case "settings":
					a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
				case "profile":
					a.selectedContact().Archived = true
					a.openContactProfile(a.selectedID())
				}
				tr := ui.NewTester(a.view, size.width, size.height)
				tr.SetDark(dark)
				tr.SetTitleBar(ui.TitleBar{Height: 38, Left: 80})
				writePreviewPNG(t, filepath.Join(dir,
					fmt.Sprintf("headless-titlebar-%s-%s-%dx%d.png", theme, scene, size.width, size.height)), tr.Image())
			}
		}
	}
}

func TestWorkbenchRoleScenesKeepActionsReachable(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, dark := range []bool{false, true} {
			for _, size := range [][2]int{{720, 640}, {1040, 720}} {
				for _, scene := range []string{"conversation", "contact", "task-context"} {
					t.Run(fmt.Sprintf("%s/%s/%dx%d/dark%t", scene, locale, size[0], size[1], dark), func(t *testing.T) {
						a, ct := taskContextApp(t)
						a.windowWidth, a.workspaceOpen = size[0], false
						a.appearanceThemes = loadCustomThemes(t.TempDir())
						a.languageModeLoaded = true
						ct.Draft = "  keep this unsaved text 中文  "
						ct.Instructions = "  exact profile instruction\n第二行  "
						recoveryTestStore(t, a)
						msgs := locales.In(locale)
						label := msgs.TaskContextCount(0)
						switch scene {
						case "conversation":
							ct.Messages = []*message{{Key: "user-visual", Kind: msgUser, Text: "A retained user message.\n中文换行。"}, {Key: "reply-visual", Kind: msgContact, Text: "A retained reply with **original emphasis**, a [link](https://example.invalid) and `code`."}}
						case "contact":
							a.openContactProfile(ct.ID)
							label = msgs.ContactSaveChanges()
						case "task-context":
							a.openConversationTaskContext()
							ct.Context.OpenFileForm()
							ct.Context.FormName = "A draft name 中文"
							ct.Context.FormPath = "/tmp/Unicode folder/未读取的 draft source.svg"
							label = msgs.TaskContextChooseFile()
						}
						tr := ui.NewTester(a.view, size[0], size[1])
						tr.SetLocale(locale)
						tr.SetDark(dark)
						tr.Frame()
						name := fmt.Sprintf("workbench-%s-%s-%dx%d-dark%t", scene, locale, size[0], size[1], dark)
						if directory := os.Getenv("NAGI_PREVIEW_DIR"); directory != "" {
							writePreviewPNG(t, filepath.Join(directory, name+"-top.png"), tr.Image())
						}
						var target ui.Rect
						visible := false
						for scroll := 0; scroll < 12; scroll++ {
							r, ok := tr.Find(label)
							if ok && r.W > 0 && r.H >= 32 && r.X >= 0 && r.X+r.W <= float32(size[0])+1 && r.Y >= 0 && r.Y+r.H <= float32(size[1])+1 {
								target, visible = r, true
								break
							}
							tr.Scroll(float32(size[0]-80), float32(size[1]-100), 0, 160)
							tr.Frame()
						}
						if !visible {
							t.Fatalf("%s is not reachable with a full target in %s: %v", label, name, tr.Texts())
						}
						if ct.Draft != "  keep this unsaved text 中文  " || ct.Instructions != "  exact profile instruction\n第二行  " || a.selectedContact() != ct || a.pi != nil || ct.pendingSend != nil {
							t.Fatal("presentation changed owner/drafts or admitted execution")
						}
						if scene == "task-context" && (ct.Context.FormName != "A draft name 中文" || ct.Context.FormPath != "/tmp/Unicode folder/未读取的 draft source.svg" || ct.Context.Count() != 0) {
							t.Fatal("rendering read, saved or normalized a file draft")
						}
						if directory := os.Getenv("NAGI_PREVIEW_DIR"); directory != "" {
							writePreviewPNG(t, filepath.Join(directory, name+"-reachable.png"), tr.Image())
							data, _ := json.MarshalIndent(map[string]any{"label": label, "target": target, "width": size[0], "height": size[1], "owner_and_drafts_retained": true}, "", "  ")
							if err := os.WriteFile(filepath.Join(directory, name+".json"), data, 0600); err != nil {
								t.Fatal(err)
							}
						}
					})
				}
			}
		}
	}
}
