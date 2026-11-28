package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
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
	const longPurpose = "Assistant for release engineering, cross-functional verification and runtime diagnostics"
	write := func(name, locale, purpose string, dark bool, setup func(*appModel, *contact)) {
		t.Helper()
		a, ct := workspaceTestApp(t)
		a.windowWidth = 960
		ct.Purpose = purpose
		if setup != nil {
			setup(a, ct)
		}
		tr := ui.NewTester(a.view, 960, 640)
		tr.SetLocale(locale)
		tr.SetDark(dark)
		tr.Frame()
		if !tr.HasText(purpose) {
			t.Fatalf("header purpose %q missing: %v", purpose, tr.Texts())
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
	})
	write("header-960-dark-en-XA-long-role.png", "en-XA", longPurpose, true, func(a *appModel, _ *contact) {
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
						label := msgs.ComposerAttach()
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

func TestPreviewFloatingBackAndCollapsedRail(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write floating Back-to-latest and collapsed rail previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	longLine := strings.Repeat("Wide continuous assistant prose occupying the full reading bubble width behind the overlay control. ", 2)
	longBody := strings.Join([]string{longLine, longLine, longLine, longLine, longLine, longLine}, "\n")
	seedLongTranscript := func(ct *contact) {
		ct.Messages = make([]*message, 0, 36)
		for i := 0; i < 36; i++ {
			ct.Messages = append(ct.Messages, &message{
				Key:  fmt.Sprintf("prev-scroll-%02d", i),
				Kind: msgContact,
				Text: fmt.Sprintf("Message %02d\n%s", i, longBody),
			})
		}
	}
	scrollUp := func(ct *contact, tr *ui.Tester, msgs locales.Messages) {
		vp, _ := tr.Find(msgs.ConversationMessagesLabel())
		for i := 0; i < 3 && ct.list.AtEnd(); i++ {
			tr.Scroll(vp.X+vp.W/2, vp.Y+vp.H/2, 0, -640)
			tr.Frame()
		}
	}

	for _, shot := range []struct {
		name, locale string
		w, h         int
		dark         bool
		setup        func(*testing.T, *ui.Tester, *appModel, *contact, locales.Messages)
	}{
		{
			name: "collapsed-scroll-en-light-1040x720.png", locale: "en", w: 1040, h: 720, dark: false,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				seedLongTranscript(ct)
				a.railCollapsed = true
				tr.Frame()
				scrollUp(ct, tr, msgs)
			},
		},
		{
			name: "compare-expanded-en-light-1040x720.png", locale: "en", w: 1040, h: 720, dark: false,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				seedLongTranscript(ct)
				a.railCollapsed = false
				tr.Frame()
				scrollUp(ct, tr, msgs)
			},
		},
		{
			name: "collapsed-scroll-zh-dark-1040x720.png", locale: "zh-Hans", w: 1040, h: 720, dark: true,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				seedLongTranscript(ct)
				a.railCollapsed = true
				tr.Frame()
				scrollUp(ct, tr, msgs)
			},
		},
		{
			name: "collapsed-scroll-en-dark-960x640.png", locale: "en", w: 960, h: 640, dark: true,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				seedLongTranscript(ct)
				a.railCollapsed = true
				tr.Frame()
				scrollUp(ct, tr, msgs)
			},
		},
		{
			name: "collapsed-scroll-zh-light-960x640.png", locale: "zh-Hans", w: 960, h: 640, dark: false,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				seedLongTranscript(ct)
				a.railCollapsed = true
				tr.Frame()
				scrollUp(ct, tr, msgs)
			},
		},
		{
			name: "collapsed-scroll-en-light-720x640.png", locale: "en", w: 720, h: 640, dark: false,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				seedLongTranscript(ct)
				a.railCollapsed = true
				tr.Frame()
				scrollUp(ct, tr, msgs)
			},
		},
		{
			name: "collapsed-scroll-zh-dark-720x640.png", locale: "zh-Hans", w: 720, h: 640, dark: true,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				seedLongTranscript(ct)
				a.railCollapsed = true
				tr.Frame()
				scrollUp(ct, tr, msgs)
			},
		},
		{
			name: "hover-back-en-light-1040x720.png", locale: "en", w: 1040, h: 720, dark: false,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				seedLongTranscript(ct)
				a.railCollapsed = true
				tr.Frame()
				scrollUp(ct, tr, msgs)
				r, _ := tr.Find(msgs.BackToLatest())
				tr.Move(1, 1)
				tr.Move(r.X+16, r.Y+16)
				tr.Advance(700 * time.Millisecond)
				tr.Frame()
			},
		},
		{
			name: "hover-back-zh-dark-1040x720.png", locale: "zh-Hans", w: 1040, h: 720, dark: true,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				seedLongTranscript(ct)
				a.railCollapsed = true
				tr.Frame()
				scrollUp(ct, tr, msgs)
				r, _ := tr.Find(msgs.BackToLatest())
				tr.Move(1, 1)
				tr.Move(r.X+16, r.Y+16)
				tr.Advance(700 * time.Millisecond)
				tr.Frame()
			},
		},
		{
			name: "hover-show-rail-en-light-1040x720.png", locale: "en", w: 1040, h: 720, dark: false,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				a.railCollapsed = true
				tr.Frame()
				r, _ := tr.Find(msgs.RailContactsTitle())
				tr.Move(1, 1)
				tr.Move(r.X+16, r.Y+16)
				tr.Advance(700 * time.Millisecond)
				tr.Frame()
			},
		},
		{
			name: "focus-back-en-dark-1040x720.png", locale: "en", w: 1040, h: 720, dark: true,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				seedLongTranscript(ct)
				a.railCollapsed = true
				tr.Frame()
				scrollUp(ct, tr, msgs)
				_ = tr.Click(msgs.ComposerLabel(ct.Name))
				tr.Frame()
				tr.Key(ui.Shift, ui.KeyTab)
				tr.Frame()
			},
		},
		{
			name: "focus-show-rail-zh-light-1040x720.png", locale: "zh-Hans", w: 1040, h: 720, dark: false,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				a.railCollapsed = true
				tr.Frame()
				_ = tr.Click(msgs.ComposerLabel(ct.Name))
				tr.Frame()
				for i := 0; i < 16 && !tr.Focused(msgs.RailContactsTitle()); i++ {
					tr.Key(0, ui.KeyTab)
					tr.Frame()
				}
			},
		},
		{
			name: "avatar-rail-profile-en-light-960x640.png", locale: "en", w: 960, h: 640, dark: false,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				seedLongTranscript(ct)
				a.openContactProfile(ct.ID)
				tr.Frame()
			},
		},
		{
			name: "avatar-rail-profile-zh-dark-960x640.png", locale: "zh-Hans", w: 960, h: 640, dark: true,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				seedLongTranscript(ct)
				a.openContactProfile(ct.ID)
				tr.Frame()
			},
		},
		{
			name: "narrow-collapsed-profile-en-light-720x640.png", locale: "en", w: 720, h: 640, dark: false,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				a.railCollapsed = true
				a.openContactProfile(ct.ID)
				tr.Frame()
			},
		},
		{
			name: "narrow-collapsed-source-chat-zh-dark-720x640.png", locale: "zh-Hans", w: 720, h: 640, dark: true,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				a.railCollapsed = true
				a.priorChats = &productConversationBrowser{
					contact:    ct,
					selectedID: "prior-preview",
					ownerID:    ct.ID,
					history: historyProjection{
						messages: []*message{
							{Key: "p-u", ID: 1, Kind: msgUser, Text: "请确认收起侧栏后的只读对话顶栏对齐。"},
							{Key: "p-b", ID: 2, Kind: msgContact, Text: "顶部预留红绿灯空间，展开按钮位于返回之前。"},
						},
					},
				}
				tr.Frame()
			},
		},
		{
			name: "narrow-collapsed-sessions-en-dark-720x640.png", locale: "en", w: 720, h: 640, dark: true,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				fx, ptySummary, pipeSummary := seedVisualSessionInspectorFixture(t)
				fx.app.windowWidth = 720
				fx.app.railCollapsed = true
				fx.app.openBotSessions(fx.contact.ID, "header-overview:"+fx.contact.ID)
				bot := fx.app.sessions.botSession(fx.contact.ID)
				bot.items = []pi.ProductSessionSummary{ptySummary, pipeSummary}
				bot.listLoading = false
				t2 := ui.NewTesterWith(fx.app.view, 720, 640, ui.TesterOptions{Dark: true})
				t2.SetLocale("en")
				t2.SetFocused(true)
				t2.SetTitleBar(ui.TitleBar{Height: 38, Left: 80})
				t2.Frame()
				writePreviewPNG(t, filepath.Join(dir, "narrow-collapsed-sessions-en-dark-720x640.png"), t2.Image())
			},
		},
		{
			name: "narrow-collapsed-background-work-zh-light-720x640.png", locale: "zh-Hans", w: 720, h: 640, dark: false,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				fx := phase3SeededControlPlaneApp(t)
				fx.app.windowWidth = 720
				fx.app.railCollapsed = true
				fx.app.openContactProfile(fx.contact.ID)
				fx.app.backgroundWork.panel = &backgroundWorkPanel{
					bot:    fx.contact,
					opener: "header-overview:" + fx.contact.ID,
				}
				t2 := ui.NewTesterWith(fx.app.view, 720, 640, ui.TesterOptions{Dark: false})
				t2.SetLocale("zh-Hans")
				t2.SetFocused(true)
				t2.SetTitleBar(ui.TitleBar{Height: 38, Left: 80})
				t2.Frame()
				writePreviewPNG(t, filepath.Join(dir, "narrow-collapsed-background-work-zh-light-720x640.png"), t2.Image())
			},
		},
		{
			name: "narrow-collapsed-scheduled-work-en-light-720x640.png", locale: "en", w: 720, h: 640, dark: false,
			setup: func(t *testing.T, tr *ui.Tester, a *appModel, ct *contact, msgs locales.Messages) {
				fx, _, _, _ := seedVisualScheduledWorkFixture(t)
				fx.app.windowWidth = 720
				fx.app.railCollapsed = true
				fx.app.scheduledWorkBotID = fx.contact.ID
				t2 := ui.NewTesterWith(fx.app.view, 720, 640, ui.TesterOptions{Dark: false})
				t2.SetLocale("en")
				t2.SetFocused(true)
				t2.SetTitleBar(ui.TitleBar{Height: 38, Left: 80})
				t2.Frame()
				writePreviewPNG(t, filepath.Join(dir, "narrow-collapsed-scheduled-work-en-light-720x640.png"), t2.Image())
			},
		},
	} {
		if strings.HasPrefix(shot.name, "narrow-collapsed-sessions") ||
			strings.HasPrefix(shot.name, "narrow-collapsed-background-work") ||
			strings.HasPrefix(shot.name, "narrow-collapsed-scheduled-work") {
			shot.setup(t, nil, nil, nil, locales.In(shot.locale))
			continue
		}
		a, ct := appReadingTestApp(t)
		a.windowWidth = shot.w
		a.workspaceOpen = false
		a.focusComposer = false
		ct.Draft = "Keep draft across collapsed rail previews"
		tr := ui.NewTesterWith(a.view, shot.w, shot.h, ui.TesterOptions{Dark: shot.dark})
		tr.SetLocale(shot.locale)
		tr.SetFocused(true)
		tr.SetTitleBar(ui.TitleBar{Height: 38, Left: 80})
		msgs := locales.In(shot.locale)
		shot.setup(t, tr, a, ct, msgs)
		writePreviewPNG(t, filepath.Join(dir, shot.name), tr.Image())
		t.Logf("floating back preview path=%s", filepath.Join(dir, shot.name))
	}
}
