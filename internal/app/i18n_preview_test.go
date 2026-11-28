package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/materials"
	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

// TestPreviewI18nSelectedSlice is the I1 headless preview for the selected
// Settings and copy slice. It is not native-window evidence and does not
// expand into the final AC3 preview matrix.
func TestPreviewI18nSelectedSlice(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write the I1 i18n previews")
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
			copyLabel := locales.In(locale).CopyMessageLabel()
			if chatTr.HasText(copyLabel) {
				t.Fatalf("outer copy button unexpectedly visible in %s preview: %v", locale, chatTr.Texts())
			}
			if err := chatTr.RightClick("Profile edits affect later replies and leave saved messages intact."); err != nil {
				t.Fatal(err)
			}
			chatTr.Frame()
			if !chatTr.HasText(copyLabel) {
				t.Fatalf("copy popover menu item %q missing: %v", copyLabel, chatTr.Texts())
			}
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

	cfg := connectionConfig{API: connectionAPIChatCompletions, Endpoint: "https://provider.example.invalid/v1", APIKey: "fixture", Model: "fixture", ContextTokens: 131072, OutputTokens: 8192, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
	conn, _ := workspaceTestApp(t)
	conn.connectionSaved = &cfg
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

func TestPreviewTaskContextLocalized(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR for the localized task context preview")
	}
	tc := &taskContext{}
	if _, err := tc.AddSnippet("Release notes", "Line one"); err != nil {
		t.Fatal(err)
	}
	tc.OpenSnippetForm()
	tc.FormName = ""
	tc.FormError = errTaskContextName
	contextTr := taskContextTester(tc, false)
	contextTr.SetLocale("zh-Hans")
	contextTr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "task-context-error-zh-Hans-light-1040x720.png"), contextTr.Image())
}

// TestPreviewI18nFinalMatrix is the bounded final E preview. It is headless
// layout evidence only, not a native window, IME, or human visual review.
func TestPreviewI18nFinalMatrix(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write the final bounded i18n previews")
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

	scenes := []scene{
		{
			name: "runtime-empty", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a, ct := workspaceTestApp(t)
				a.workspaceOpen = false
				a.pi = &piAppHost{}
				a.runtimeStatus = runtimeStateStarting
				ct.Messages = nil
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.ConnectionStatusConnecting(), msgs.ConnectionRecoveryConnectingHint(), msgs.EmptyNoMessages(), msgs.ComposerLabel("Xuhu")}
			},
			fit: func(msgs locales.Messages) []string { return []string{msgs.ComposerLabel("Xuhu")} },
		},
		{
			name: "config-failure", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a, ct := workspaceTestApp(t)
				a.workspaceOpen = false
				a.runtimeStatus = runtimeStateFailed
				a.runtimeStage = "Connection configuration"
				a.runtimeErrKind = runtimeNoticeConnectionRequired
				a.runtimeHintKind = startupHintEnterConnection
				ct.Draft = "Keep this draft while I fix configuration."
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.ConnectionStatusSetupNeeded(), msgs.ConnectionRecoverySetupHint()}
			},
			fit: func(msgs locales.Messages) []string {
				return []string{msgs.ComposerLabel("Xuhu"), msgs.ConfigurationDetails()}
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
				a.providers.snapshot = pi.ProductProviderList{
					Policy: pi.ProductRoutingBalanced,
					Accounts: []pi.ProductProviderAccountView{{
						ProductProviderAccount: pi.ProductProviderAccount{
							ProviderID:    "preview-provider",
							Name:          "Studio gateway",
							WireAPI:       pi.OpenAICompletions,
							BaseURL:       "https://provider.example.invalid/v1/very/long/endpoint/for/final/preview",
							DefaultModel:  "provider/very-long-model-name-for-final-preview",
							ContextTokens: 131072,
							OutputTokens:  8192,
							Enabled:       true,
						},
						CredentialStatus: "ok",
					}},
				}
				a.openSettingsTab(settingsTabConnection, settingsSourceRail)
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.SettingsConnection(), msgs.ProviderConfiguredTitle(), msgs.ProviderAddShort(), "Studio gateway", msgs.ProviderFoldSummary(10)}
			},
			fit: settingsFit,
		},
		{
			name: "bot-profile", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a, ct := workspaceTestApp(t)
				ct.Name = "Alexandria-Longname-Contact-For-Final-Review"
				ct.Purpose = "Keep a long contact purpose visible without horizontal clipping in the final preview."
				ct.Instructions = "Keep the exact contact instructions and workspace values readable in both languages."
				ct.Workspace = "/Users/you/Workspace/a/very/long/contact/workspace/path/for/final/preview"
				a.workspaceOpen = false
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.ContactProfileAction(), msgs.FieldNameAccessible(), msgs.ProfilePurpose()}
			},
			fit: func(msgs locales.Messages) []string {
				return []string{msgs.ProfileClose(), msgs.FieldNameAccessible()}
			},
		},
		{
			name: "settings-runtime", width: 1040, height: 720,
			setup: func(locale string) *appModel {
				a, _ := workspaceTestApp(t)
				a.runtimeStatus = runtimeStateFailed
				a.runtimeStage = "Runtime startup"
				a.runtimeErr = "Pi worker could not start."
				a.storePath = "/Users/you/Library/Application Support/Xuhu/contacts.json"
				a.workDir = "/Users/you/Workspace/xuhu-run"
				a.openAdvancedSettings()
				return a
			},
			want: func(msgs locales.Messages) []string {
				return []string{msgs.SettingsAdvanced(), msgs.SettingsStartupSection(), msgs.SettingsHideRuntimeDetails(), msgs.RuntimeStoreLine("/Users/you/Library/Application Support/Xuhu/contacts.json")}
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
		if base.name == "bot-profile" {
			if err := tr.RightClick(msgs.AvatarContactLabel(a.selectedContact().Name)); err != nil {
				t.Fatal(err)
			}
			if err := choosePopoverMenuItem(tr, msgs.ContactProfileAction()); err != nil {
				t.Fatal(err)
			}
		}
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
		img := tr.Image()
		if img.Bounds().Dx() != base.width || img.Bounds().Dy() != base.height {
			t.Fatalf("scene %s image = %dx%d, want %dx%d", base.name, img.Bounds().Dx(), img.Bounds().Dy(), base.width, base.height)
		}
		path := filepath.Join(dir, fmt.Sprintf("final-%s-%s-%s-%dx%d.png", locale, theme, base.name, base.width, base.height))
		writePreviewPNG(t, path, img)
		t.Logf("final preview path=%s width=%d height=%d locale=%s theme=%s scene=%s settingsScroll=%v", path, base.width, base.height, locale, theme, base.name, a.settingsScroll.Y)
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
		{"bot-profile", "dark", true},
		{"settings-connection", "light", false},
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
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write the whole-app Markdown link previews")
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
			a.storePath, a.workDir = a.store.Path, filepath.Dir(a.store.Path)
			a.windowWidth, a.workspaceOpen = item.width, false
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
			if tr.HasText(msgs.CopyMessageLabel()) {
				t.Fatalf("outer copy button unexpectedly visible on markdown preview: %v", tr.Texts())
			}
			if err := tr.RightClick("the design note"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if err := choosePopoverMenuItem(tr, msgs.CopyMessageLabel()); err != nil {
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
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write whole-app input state previews")
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
			a.storePath, a.workDir = a.store.Path, filepath.Dir(a.store.Path)
			a.windowWidth, a.workspaceOpen = 1040, false
			a.appearanceMode = item.theme
			a.languageMode, a.languageModeLoaded = "en", true
			cfg := connectionConfig{
				API:      connectionAPIChatCompletions,
				Endpoint: "https://input-fixture.example.invalid/v1", APIKey: "fixture-not-a-real-key", Model: "input-fixture",
				ContextTokens: 131072, OutputTokens: 8192, WorkingDirectory: a.workDir, MaxActiveTasks: 2,
			}
			a.connectionSaved = &cfg
			a.chooseFirstProvider(-1)
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
			a.openContactProfile(ct.ID)
			editor := a.profileEditorFor(ct.ID)
			if item.field == "connection-endpoint" {
				a.pi = stubProductHost(t, a, pi.ProductProviderList{Policy: pi.ProductRoutingBalanced})
				a.demo = false
				a.runtimeStatus = runtimeStateReady
				a.openSettingsTab(settingsTabConnection, settingsSourceRail)
				a.openProviderDialog()
			}
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
				label = msgs.ProviderBaseURL()
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
			wantConnection, wantSaved := a.providers.first.BaseURL, cfg
			wantConversationDraft := ct.Draft
			storeBefore, err := os.ReadFile(a.store.Path)
			if err != nil {
				t.Fatal(err)
			}
			focusInput := func() {
				for range 64 {
					active, _ := tr.TextInputActive()
					if tr.Focused(label) && active {
						return
					}
					tr.Key(0, ui.KeyTab)
					tr.Frame()
				}
				t.Fatalf("actual text controller was not keyboard reachable: label=%q", label)
			}
			if item.field == "connection-endpoint" {
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
			if !profileDraftEqual(editor.Draft, wantProfile) || a.providers.first.BaseURL != wantConnection {
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
				"connection_editor_bounds_observable": item.field != "connection-endpoint",
				"connection_bounds_limit":             "Connection caption and editor share a label; its caption is not an editor-bound observation",
				"input_border_role":                   resolved.Palette.InputBorder, "accent_role": resolved.Palette.Theme.Accent,
				"noninput_border_role": resolved.Palette.Theme.Border,
			}
			pointer := [2]float32{-1, -1}
			if item.state == "hover" || item.state == "disabled" {
				pointer = [2]float32{inputBounds.X + inputBounds.W/2, inputBounds.Y + inputBounds.H/2}
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
					original = a.providers.first.BaseURL
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
					got = a.providers.first.BaseURL
				}
				if got != typed || tr.Clipboard() != typed || !tr.Focused(label) {
					t.Fatalf("actual input typing/selection changed source: draft=%q clipboard=%q want=%q", got, tr.Clipboard(), typed)
				}
				receipt["typed_source"], receipt["copied_source"] = typed, tr.Clipboard()
				tr.Key(ui.Cmd, ui.KeyA)
				tr.Type(original)
				tr.Frame()
			}
			if !profileDraftEqual(editor.Draft, wantProfile) || a.providers.first.BaseURL != wantConnection ||
				!profileDraftEqual(contactProfileDraft(ct), wantContact) || ct.Draft != wantConversationDraft ||
				a.connectionSaved == nil || *a.connectionSaved != wantSaved {
				t.Fatal("input interactions did not preserve the exact drafts and saved source after restoration")
			}
			if a.providerBusy() || (item.field != "connection-endpoint" && a.pi != nil) || len(tr.OpenedURLs()) != 0 {
				t.Fatal("input-only interaction triggered a connection or external action")
			}
			storeAfter, err := os.ReadFile(a.store.Path)
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

func TestPreviewProvidersDS2(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write DS2 provider and toggle previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeShot := func(name string, a *appModel, width, height int, locale string, dark bool) {
		t.Helper()
		a.windowWidth = width
		tr := ui.NewTesterWith(a.view, width, height, ui.TesterOptions{Dark: dark})
		tr.SetLocale(locale)
		tr.SetFocused(true)
		tr.Frame()
		path := filepath.Join(dir, name)
		writePreviewPNG(t, path, tr.Image())
		t.Logf("ds2 preview path=%s width=%d height=%d locale=%s dark=%v", path, width, height, locale, dark)
	}

	v2Cfg := connectionConfig{
		API:              connectionAPIResponses,
		Endpoint:         "https://api.openai.com/v1",
		APIKey:           "sk-preview-v2-synthetic",
		Model:            "gpt-5",
		ContextTokens:    131072,
		OutputTokens:     16384,
		WorkingDirectory: t.TempDir(),
		MaxActiveTasks:   2,
	}
	v2App := providerUITestApp(t, pi.ProductProviderList{Policy: pi.ProductRoutingBalanced})
	v2App.connectionSaved = &v2Cfg
	writeShot("ds2-providers-0acc-v2import-en-light-1040x720.png", v2App, 1040, 720, "en", false)

	foldedApp := providerUITestApp(t, visualGuardSeededPool())
	writeShot("ds2-providers-2acc-folded-zh-light-1040x720.png", foldedApp, 1040, 720, "zh-Hans", false)

	expandedLight := providerUITestApp(t, visualGuardSeededPool())
	expandedLight.providers.catalogFolds = map[string]bool{"cloud": false}
	writeShot("ds2-providers-2acc-expanded-zh-light-1040x720.png", expandedLight, 1040, 720, "zh-Hans", false)

	expandedDark := providerUITestApp(t, visualGuardSeededPool())
	expandedDark.providers.catalogFolds = map[string]bool{"cloud": false}
	writeShot("ds2-providers-2acc-expanded-zh-dark-1040x720.png", expandedDark, 1040, 720, "zh-Hans", true)

	yes := true
	modalSavedPool := visualGuardSeededPool()
	modalLight := providerUITestApp(t, modalSavedPool)
	modalLight.selectProvider(modalSavedPool.Accounts[0])
	modalLight.providers.drafts["prov-internal-openai"].discovered = []pi.ProductDiscoveredModel{
		{ID: "gpt-5", CanonicalID: "openai/gpt-5", IsChat: &yes, ToolCall: &yes, Recommended: true, ContextTokens: 131072, OutputTokens: 16384},
		{ID: "gpt-4o", CanonicalID: "openai/gpt-4o", IsChat: &yes, ToolCall: &yes, Recommended: true, ContextTokens: 131072, OutputTokens: 16384},
		{ID: "gpt-5.4-mini", CanonicalID: "openai/gpt-5.4-mini", IsChat: &yes, ToolCall: &yes, Reasoning: true, Recommended: true, ContextTokens: 262144, OutputTokens: 16384},
	}
	modalLight.providers.drafts["prov-internal-openai"].tested = true
	modalLight.providers.drafts["prov-internal-openai"].latency = 142
	writeShot("ds2-providers-modal-saved-zh-light-1040x720.png", modalLight, 1040, 720, "zh-Hans", false)

	modalDark := providerUITestApp(t, modalSavedPool)
	modalDark.selectProvider(modalSavedPool.Accounts[0])
	modalDark.providers.drafts["prov-internal-openai"].discovered = modalLight.providers.drafts["prov-internal-openai"].discovered
	modalDark.providers.drafts["prov-internal-openai"].tested = true
	modalDark.providers.drafts["prov-internal-openai"].latency = 142
	writeShot("ds2-providers-modal-saved-zh-dark-1040x720.png", modalDark, 1040, 720, "zh-Hans", true)

	routingApp := providerUITestApp(t, visualGuardSeededPool())
	routingApp.providers.tab = providerTabRouting
	writeShot("ds2-providers-routing-zh-light-1040x720.png", routingApp, 1040, 720, "zh-Hans", false)

	narrowDark := providerUITestApp(t, visualGuardSeededPool())
	narrowDark.providers.catalogFolds = map[string]bool{"cloud": false}
	writeShot("ds2-providers-narrow-zh-dark-720x640.png", narrowDark, 720, 640, "zh-Hans", true)

	generalApp := providerUITestApp(t, visualGuardSeededPool())
	generalApp.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	writeShot("ds2-general-toggle-zh-light-1040x720.png", generalApp, 1040, 720, "zh-Hans", false)

	presetDark := providerUITestApp(t, pi.ProductProviderList{Policy: pi.ProductRoutingBalanced})
	presetDark.uiLocale = "zh-Hans"
	presetDark.openProviderPreset(pi.ProductProviderList{Policy: pi.ProductRoutingBalanced}, "deepseek")
	writeShot("ds2-providers-preset-deepseek-zh-dark-1040x720.png", presetDark, 1040, 720, "zh-Hans", true)

	presetLight := providerUITestApp(t, pi.ProductProviderList{Policy: pi.ProductRoutingBalanced})
	presetLight.uiLocale = "en"
	presetLight.openProviderPreset(pi.ProductProviderList{Policy: pi.ProductRoutingBalanced}, "deepseek")
	writeShot("ds2-providers-preset-deepseek-en-light-720x640.png", presetLight, 720, 640, "en", false)
}

func TestPreviewCapabilitiesProfileDS2(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write DS2 Phase 2 capabilities and profile previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeShot := func(name string, a *appModel, width, height int, locale string, dark bool, prepare func(*ui.Tester)) {
		t.Helper()
		a.windowWidth = width
		tr := ui.NewTesterWith(a.view, width, height, ui.TesterOptions{Dark: dark})
		tr.SetLocale(locale)
		tr.SetFocused(true)
		tr.Frame()
		if prepare != nil {
			prepare(tr)
			tr.Advance(theme.ControlTransition.Duration)
			tr.Frame()
		}
		path := filepath.Join(dir, name)
		writePreviewPNG(t, path, tr.Image())
		t.Logf("ds2 phase2 preview path=%s width=%d height=%d locale=%s dark=%v", path, width, height, locale, dark)
	}

	skillsLight, _, _, _ := phase2SeededCapabilitiesProfileApp(t)
	skillsLight.settingsOpen = true
	skillsLight.settingsTab = settingsTabCapabilities
	skillsLight.capabilities.tab = capabilityTabSkills
	writeShot("ds2-p2-skills-en-light-1040x720.png", skillsLight, 1040, 720, "en", false, nil)

	skillsDark, _, _, _ := phase2SeededCapabilitiesProfileApp(t)
	skillsDark.settingsOpen = true
	skillsDark.settingsTab = settingsTabCapabilities
	skillsDark.capabilities.tab = capabilityTabSkills
	writeShot("ds2-p2-skills-zh-dark-1040x720.png", skillsDark, 1040, 720, "zh-Hans", true, nil)

	for _, tc := range []struct {
		name string
		dark bool
	}{
		{"ds2-p2-new-skill-modal-zh-light-1040x720.png", false},
		{"ds2-p2-new-skill-modal-zh-dark-1040x720.png", true},
	} {
		app, _, _, _ := phase2SeededCapabilitiesProfileApp(t)
		app.settingsOpen = true
		app.settingsTab = settingsTabCapabilities
		app.capabilities.tab = capabilityTabSkills
		app.capabilities.newSkill = &skillEditor{
			key:     pi.ProductSkillKey{Source: "global", SkillID: "release-audit"},
			name:    "Release audit",
			summary: "Validate frozen candidate manifests and visual guards.",
			body:    "## Steps\n\n1. Check manifest SHA-256\n2. Inspect rendered PNGs",
		}
		writeShot(tc.name, app, 1040, 720, "zh-Hans", tc.dark, nil)
	}

	mcpLight, _, _, _ := phase2SeededCapabilitiesProfileApp(t)
	mcpLight.settingsOpen = true
	mcpLight.settingsTab = settingsTabCapabilities
	mcpLight.capabilities.tab = capabilityTabMCP
	writeShot("ds2-p2-mcp-detail-zh-light-1040x720.png", mcpLight, 1040, 720, "zh-Hans", false, nil)

	mcpNarrowDark, _, _, _ := phase2SeededCapabilitiesProfileApp(t)
	mcpNarrowDark.settingsOpen = true
	mcpNarrowDark.settingsTab = settingsTabCapabilities
	mcpNarrowDark.capabilities.tab = capabilityTabMCP
	writeShot("ds2-p2-mcp-detail-zh-dark-720x640.png", mcpNarrowDark, 720, 640, "zh-Hans", true, nil)

	for _, tc := range []struct {
		name string
		dark bool
	}{
		{"ds2-p2-add-mcp-modal-zh-light-1040x720.png", false},
		{"ds2-p2-add-mcp-modal-zh-dark-1040x720.png", true},
	} {
		app, _, _, _ := phase2SeededCapabilitiesProfileApp(t)
		app.settingsOpen = true
		app.settingsTab = settingsTabCapabilities
		app.capabilities.tab = capabilityTabMCP
		ed := newMcpEditor(pi.ProductMcpServerView{
			ProductMcpServerConfig: pi.ProductMcpServerConfig{
				Name:      "docs-gateway",
				Transport: "http",
				Isolation: "shared",
				Enabled:   true,
				URL:       "https://mcp.example.invalid/v1",
			},
		})
		ed.secretHTTP = []mcpSecretDraft{{key: "Authorization", value: "Bearer synthetic-preview-token"}}
		app.capabilities.newMcp = ed
		writeShot(tc.name, app, 1040, 720, "zh-Hans", tc.dark, nil)
	}

	profileCaps, ctCaps, _, _ := phase2SeededCapabilitiesProfileApp(t)
	profileCaps.contactProfileID = ctCaps.ID
	profileCaps.openDetail(detailProfile)
	writeShot("ds2-p2-bot-profile-caps-zh-light-1040x720.png", profileCaps, 1040, 720, "zh-Hans", false, func(tr *ui.Tester) {
		profileCaps.profileScrollFor(ctCaps.ID).Y = 560
		tr.Frame()
	})

	profileMcp, ctMcp, _, _ := phase2SeededCapabilitiesProfileApp(t)
	profileMcp.contactProfileID = ctMcp.ID
	profileMcp.openDetail(detailProfile)
	writeShot("ds2-p2-bot-profile-mcp-zh-dark-1040x720.png", profileMcp, 1040, 720, "zh-Hans", true, func(tr *ui.Tester) {
		profileMcp.profileScrollFor(ctMcp.ID).Y = 1280
		tr.Frame()
	})

	profileMem, ctMem, _, _ := phase2SeededCapabilitiesProfileApp(t)
	profileMem.contactProfileID = ctMem.ID
	profileMem.openDetail(detailProfile)
	writeShot("ds2-p2-bot-profile-memory-zh-dark-1040x720.png", profileMem, 1040, 720, "zh-Hans", true, func(tr *ui.Tester) {
		profileMem.profileScrollFor(ctMem.ID).Y = 1750
		tr.Frame()
	})

	reviewApp, ctRev, privateSkill, req := phase2SeededCapabilitiesProfileApp(t)
	reviewApp.contactProfileID = ctRev.ID
	reviewApp.openDetail(detailProfile)
	reviewApp.capabilities.review = &skillReviewState{
		key:     pi.ProductSkillKey{Source: "private", BotID: ctRev.ID, SkillID: "case-script"},
		name:    privateSkill.Name,
		request: &req,
		file:    "scripts/run.mjs",
		read: capabilityRead[pi.ProductSkillDetail]{
			ready: true,
			value: pi.ProductSkillDetail{
				ProductSkillSummary: privateSkill,
				Files: map[string]string{
					"SKILL.md":        "---\nname: Case files script\n---\n",
					"scripts/run.mjs": "export default async function(input, ctx) {\n  return { workspace: ctx.workspace };\n}\n",
				},
			},
		},
	}
	writeShot("ds2-p2-skill-review-modal-zh-dark-1040x720.png", reviewApp, 1040, 720, "zh-Hans", true, nil)

	createApp, _, _, _ := phase2SeededCapabilitiesProfileApp(t)
	createApp.openCreateBotDialog()
	createApp.createBot.name = "Release Reviewer"
	createApp.createBot.purpose = "Audit visual invariants and integration evidence."
	writeShot("ds2-p2-create-bot-modal-zh-light-1040x720.png", createApp, 1040, 720, "zh-Hans", false, nil)

	matApp, ctMat, _, _ := phase2SeededCapabilitiesProfileApp(t)
	matApp.openComposerMaterial(ctMat, materials.Item{})
	matApp.composerMaterial.name = "Design System 2.0 Notes"
	matApp.composerMaterial.text = "Verify 16 DIP frosted modal radius, 1 DIP hairline InputBorder, and first-frame theme contrast."
	writeShot("ds2-p2-composer-material-modal-zh-dark-1040x720.png", matApp, 1040, 720, "zh-Hans", true, nil)
}

func TestPreviewControlPlaneDS2(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write DS2 Phase 3 control plane previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeShot := func(name string, fx phase3SeededControlPlane, width, height int, locale string, dark bool, prepare func(*ui.Tester)) {
		t.Helper()
		fx.app.windowWidth = width
		tr := ui.NewTesterWith(fx.app.view, width, height, ui.TesterOptions{Dark: dark})
		tr.SetLocale(locale)
		tr.SetFocused(true)
		tr.Frame()
		if prepare != nil {
			prepare(tr)
			tr.Advance(theme.ControlTransition.Duration)
			tr.Frame()
		}
		path := filepath.Join(dir, name)
		writePreviewPNG(t, path, tr.Image())
		t.Logf("ds2 phase3 preview path=%s width=%d height=%d locale=%s dark=%v", path, width, height, locale, dark)
	}

	fxQ1 := phase3SeededControlPlaneApp(t)
	fxQ1.contact.activityNotices.selected = activityNoticeKey{fxQ1.questionAct.ActivityID, "question_open", "q-cp-1"}
	writeShot("ds2-p3-notice-strip-question-en-light-1040x720.png", fxQ1, 1040, 720, "en", false, func(tr *ui.Tester) {
		_ = tr.Click("Batch fourteen")
		tr.Move(0, 0)
		tr.Frame()
	})

	fxQ2 := phase3SeededControlPlaneApp(t)
	fxQ2.contact.activityNotices.selected = activityNoticeKey{fxQ2.questionAct.ActivityID, "question_open", "q-cp-1"}
	writeShot("ds2-p3-notice-strip-question-zh-dark-720x640.png", fxQ2, 720, 640, "zh-Hans", true, func(tr *ui.Tester) {
		_ = tr.Click("Batch fourteen")
		tr.Move(0, 0)
		tr.Frame()
	})

	fxApp := phase3SeededControlPlaneApp(t)
	fxApp.contact.activityNotices.selected = activityNoticeKey{fxApp.approvalAct.ActivityID, "approval_required", "req-cp-1"}
	writeShot("ds2-p3-notice-strip-approval-zh-dark-1040x720.png", fxApp, 1040, 720, "zh-Hans", true, nil)

	fxAuto := phase3SeededControlPlaneApp(t)
	fxAuto.contact.activityNotices.selected = activityNoticeKey{fxAuto.autoAnswerAct.ActivityID, "question_auto_saved", "q-cp-auto"}
	writeShot("ds2-p3-notice-strip-auto-answer-en-light-1040x720.png", fxAuto, 1040, 720, "en", false, nil)

	fxDone := phase3SeededControlPlaneApp(t)
	fxDone.contact.activityNotices.selected = activityNoticeKey{fxDone.completedAct.ActivityID, "completed", "exec-done-1"}
	writeShot("ds2-p3-notice-strip-completed-zh-dark-720x640.png", fxDone, 720, 640, "zh-Hans", true, nil)

	fxTraceEn := phase3SeededControlPlaneApp(t)
	fxTraceEn.contact.activity.open = true
	fxTraceEn.contact.activity.activityID = fxTraceEn.steerAct.ActivityID
	fxTraceEn.contact.activity.activity = fxTraceEn.steerAct
	writeShot("ds2-p3-activity-detail-trace-steer-en-light-1040x720.png", fxTraceEn, 1040, 720, "en", false, func(tr *ui.Tester) {
		fxTraceEn.contact.activity.scroll.Y = 180
		tr.Frame()
	})

	fxTraceZh := phase3SeededControlPlaneApp(t)
	fxTraceZh.contact.activity.open = true
	fxTraceZh.contact.activity.activityID = fxTraceZh.steerAct.ActivityID
	fxTraceZh.contact.activity.activity = fxTraceZh.steerAct
	writeShot("ds2-p3-activity-detail-trace-steer-zh-dark-720x640.png", fxTraceZh, 720, 640, "zh-Hans", true, func(tr *ui.Tester) {
		fxTraceZh.contact.activity.scroll.Y = 180
		tr.Frame()
	})

	fxOpt := phase3SeededControlPlaneApp(t)
	fxOpt.contact.activity.open = true
	fxOpt.contact.activity.activityID = fxOpt.questionAct.ActivityID
	fxOpt.contact.activity.activity = fxOpt.questionAct
	fxOpt.app.editProductActivity(fxOpt.contact, activityAnswerIntent(fxOpt.contact, fxOpt.questionAct, fxOpt.questionAct.Questions[0]))
	writeShot("ds2-p3-activity-options-editor-en-light-1040x720.png", fxOpt, 1040, 720, "en", false, func(tr *ui.Tester) {
		_ = tr.Click("Batch fourteen")
		tr.Move(0, 0)
		tr.Frame()
	})

	fxUnk := phase3SeededControlPlaneApp(t)
	fxUnk.contact.activity.open = true
	fxUnk.contact.activity.activityID = fxUnk.steerAct.ActivityID
	fxUnk.contact.activity.activity = fxUnk.steerAct
	fxUnk.app.activityIntents = map[string]productActivityIntent{
		fxUnk.steerAct.ActivityID: {
			OperationID: "op-preview-unknown",
			BotID:       fxUnk.contact.ID,
			ActivityID:  fxUnk.steerAct.ActivityID,
			Action:      "steer",
			ExecutionID: "exec-steer-1",
			Text:        "Verify missing steering receipt",
		},
	}
	writeShot("ds2-p3-activity-steer-unknown-zh-dark-1040x720.png", fxUnk, 1040, 720, "zh-Hans", true, nil)

	fxProfEn := phase3SeededControlPlaneApp(t)
	fxProfEn.app.rightPaneWidth = 440
	fxProfEn.app.contactProfileID = fxProfEn.contact.ID
	fxProfEn.app.openDetail(detailProfile)
	fxProfEn.contact.activity.open = true
	fxProfEn.contact.activity.activityID = fxProfEn.steerAct.ActivityID
	fxProfEn.contact.activity.activity = fxProfEn.steerAct
	writeShot("ds2-p3-workbench-profile-activity-en-light-960x640.png", fxProfEn, 960, 640, "en", false, nil)

	fxProfZh := phase3SeededControlPlaneApp(t)
	fxProfZh.app.rightPaneWidth = 440
	fxProfZh.app.contactProfileID = fxProfZh.contact.ID
	fxProfZh.app.openDetail(detailProfile)
	fxProfZh.contact.activity.open = true
	fxProfZh.contact.activity.activityID = fxProfZh.steerAct.ActivityID
	fxProfZh.contact.activity.activity = fxProfZh.steerAct
	writeShot("ds2-p3-workbench-profile-activity-zh-dark-960x640.png", fxProfZh, 960, 640, "zh-Hans", true, nil)

	fxNarrow := phase3SeededControlPlaneApp(t)
	fxNarrow.contact.activity.open = true
	fxNarrow.contact.activityNotices.selected = activityNoticeKey{fxNarrow.questionAct.ActivityID, "question_open", "q-cp-1"}
	writeShot("ds2-p3-workbench-narrow-composer-zh-dark-720x640.png", fxNarrow, 720, 640, "zh-Hans", true, func(tr *ui.Tester) {
		_ = tr.Click(locales.In("zh-Hans").ComposerLabel(fxNarrow.contact.Name))
		tr.Frame()
	})
}

func TestPreviewSessionInspector(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write BG-1 session inspection previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeSessionShot := func(name string, fx phase3SeededControlPlane, width, height int, locale string, dark bool, prepare func(*ui.Tester)) {
		t.Helper()
		fx.app.windowWidth = width
		tr := ui.NewTesterWith(fx.app.view, width, height, ui.TesterOptions{Dark: dark})
		tr.SetLocale(locale)
		tr.SetFocused(true)
		tr.Frame()
		if prepare != nil {
			prepare(tr)
		}
		path := filepath.Join(dir, name)
		writePreviewPNG(t, path, tr.Image())
		t.Logf("bg1 preview path=%s width=%d height=%d locale=%s dark=%v", path, width, height, locale, dark)
	}

	fxHeader, _, _ := seedVisualSessionInspectorFixture(t)
	writeSessionShot("bg1-session-header-clean-en-light-1040x720.png", fxHeader, 1040, 720, "en", false, nil)

	fxMenuEn, _, _ := seedVisualSessionInspectorFixture(t)
	writeSessionShot("bg1-session-overview-menu-en-light-1040x720.png", fxMenuEn, 1040, 720, "en", false, func(tr *ui.Tester) {
		_ = tr.Click(locales.In("en").BotOverviewLabel())
		tr.Frame()
	})

	fxMenuZh, _, _ := seedVisualSessionInspectorFixture(t)
	writeSessionShot("bg1-session-overview-menu-zh-dark-720x640.png", fxMenuZh, 720, 640, "zh-Hans", true, func(tr *ui.Tester) {
		_ = tr.Click(locales.In("zh-Hans").BotOverviewLabel())
		tr.Frame()
	})

	fxList, ptySummary, pipeSummary := seedVisualSessionInspectorFixture(t)
	fxList.app.openBotSessions(fxList.contact.ID, "header-overview:"+fxList.contact.ID)
	botList := fxList.app.sessions.botSession(fxList.contact.ID)
	botList.items = []pi.ProductSessionSummary{ptySummary, pipeSummary}
	botList.nextCursor = "cursor-page-2"
	botList.listLoading = false
	botList.listFailed = false
	writeSessionShot("bg1-sessions-drawer-list-en-light-1040x720.png", fxList, 1040, 720, "en", false, nil)

	fxPTYEn, ptyDetailEn, pipeListEn := seedVisualSessionInspectorFixture(t)
	fxPTYEn.app.openBotSessions(fxPTYEn.contact.ID, "header-overview:"+fxPTYEn.contact.ID)
	botPTYEn := fxPTYEn.app.sessions.botSession(fxPTYEn.contact.ID)
	botPTYEn.items = []pi.ProductSessionSummary{ptyDetailEn, pipeListEn}
	botPTYEn.listLoading = false
	botPTYEn.listFailed = false
	botPTYEn.detail = &sessionDetailState{
		summary: ptyDetailEn,
		ready:   true,
		output: sessionOutputState{
			loaded:         true,
			from:           0,
			next:           512,
			highWater:      512,
			firstAvailable: 0,
			revision:       ptyDetailEn.Revision,
			authPause:      ptyDetailEn.AuthPause,
			chunks:         []pi.ProductSessionChunk{{Stream: "pty", Offset: 0, NextOffset: 512, Text: "PTY_READY\nPassword:\n"}},
		},
	}
	writeSessionShot("bg1-session-pty-auth-en-light-1040x720.png", fxPTYEn, 1040, 720, "en", false, nil)

	fxPTY, ptyDetailSummary, pipeListSummary := seedVisualSessionInspectorFixture(t)
	fxPTY.app.openBotSessions(fxPTY.contact.ID, "header-overview:"+fxPTY.contact.ID)
	botPTY := fxPTY.app.sessions.botSession(fxPTY.contact.ID)
	botPTY.items = []pi.ProductSessionSummary{ptyDetailSummary, pipeListSummary}
	botPTY.listLoading = false
	botPTY.listFailed = false
	botPTY.detail = &sessionDetailState{
		summary: ptyDetailSummary,
		ready:   true,
		output: sessionOutputState{
			loaded:         true,
			from:           0,
			next:           512,
			highWater:      512,
			firstAvailable: 0,
			revision:       ptyDetailSummary.Revision,
			authPause:      ptyDetailSummary.AuthPause,
			chunks:         []pi.ProductSessionChunk{{Stream: "pty", Offset: 0, NextOffset: 512, Text: "PTY_READY\nPassword:\n"}},
		},
	}
	writeSessionShot("bg1-session-pty-auth-zh-dark-720x640.png", fxPTY, 720, 640, "zh-Hans", true, nil)

	fxPipe, ptyPipeList, pipeDetailSummary := seedVisualSessionInspectorFixture(t)
	fxPipe.app.openBotSessions(fxPipe.contact.ID, "header-overview:"+fxPipe.contact.ID)
	botPipe := fxPipe.app.sessions.botSession(fxPipe.contact.ID)
	botPipe.items = []pi.ProductSessionSummary{ptyPipeList, pipeDetailSummary}
	botPipe.listLoading = false
	botPipe.listFailed = false
	botPipe.detail = &sessionDetailState{
		summary: pipeDetailSummary,
		ready:   true,
		output: sessionOutputState{
			loaded:         true,
			from:           2048,
			next:           6144,
			highWater:      12288,
			firstAvailable: 2048,
			gapBytes:       2048,
			browsing:       true,
			eof:            true,
			revision:       pipeDetailSummary.Revision,
			chunks: []pi.ProductSessionChunk{
				{Stream: "stdout", Offset: 2048, NextOffset: 2070, Text: "Running integration\n"},
				{Stream: "stderr", Offset: 2070, NextOffset: 2090, Text: "warning: slow step\n"},
				{Stream: "stdout", Offset: 2090, NextOffset: 2110, Text: "All checks PASS\n"},
			},
		},
	}
	writeSessionShot("bg1-session-pipe-browsing-en-light-960x640.png", fxPipe, 960, 640, "en", false, nil)

	fxWatches, ptyWatchList, closedSummary := seedVisualSessionInspectorFixture(t)
	closedSummary.State = pi.ProductSessionClosed
	closedSummary.TransportStatus = pi.ProductTransportClosedByUser
	fxWatches.app.openBotSessions(fxWatches.contact.ID, "header-overview:"+fxWatches.contact.ID)
	botWatches := fxWatches.app.sessions.botSession(fxWatches.contact.ID)
	botWatches.items = []pi.ProductSessionSummary{ptyWatchList, closedSummary}
	botWatches.listLoading = false
	botWatches.listFailed = false
	botWatches.detail = &sessionDetailState{
		summary: closedSummary,
		ready:   true,
		output: sessionOutputState{
			loaded:   true,
			eof:      true,
			revision: closedSummary.Revision,
			chunks:   []pi.ProductSessionChunk{{Stream: "stdout", Offset: 0, NextOffset: 16, Text: "All checks PASS\n"}},
		},
		scroll: ui.ScrollState{Y: 260},
	}
	writeSessionShot("bg1-session-closed-watches-zh-dark-1040x720.png", fxWatches, 1040, 720, "zh-Hans", true, nil)
}

func TestPreviewScheduledWork(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write BG-2 scheduled work previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeSchedShot := func(name string, fx phase3SeededControlPlane, width, height int, locale string, dark bool) {
		t.Helper()
		fx.app.windowWidth = width
		tr := ui.NewTesterWith(fx.app.view, width, height, ui.TesterOptions{Dark: dark})
		tr.SetLocale(locale)
		tr.SetFocused(true)
		tr.Frame()
		path := filepath.Join(dir, name)
		writePreviewPNG(t, path, tr.Image())
		t.Logf("bg2 preview path=%s width=%d height=%d locale=%s dark=%v", path, width, height, locale, dark)
	}

	fxList, _, _, _ := seedVisualScheduledWorkFixture(t)
	fxList.app.scheduledWorkBotID = fxList.contact.ID
	writeSchedShot("bg2-schedules-list-en-light-1040x720.png", fxList, 1040, 720, "en", false)

	fxEditor, _, _, _ := seedVisualScheduledWorkFixture(t)
	fxEditor.app.scheduledWorkBotID = fxEditor.contact.ID
	fxEditor.app.schedules.bot(fxEditor.contact.ID).editor = &scheduledWatchEditor{
		kind:      string(pi.ProductWatchSchedule),
		cadence:   string(pi.ProductScheduleCalendar),
		frequency: "weekly",
		precheck:  "executable",
		zone:      "Asia/Shanghai",
		hour:      "9",
		minute:    "30",
	}
	writeSchedShot("bg2-schedules-editor-zh-dark-960x640.png", fxEditor, 960, 640, "zh-Hans", true)

	fxReview, _, dormantWatch, _ := seedVisualScheduledWorkFixture(t)
	fxReview.app.scheduledWorkBotID = fxReview.contact.ID
	fxReview.app.schedules.bot(fxReview.contact.ID).review = &scheduledWatchReview{watch: dormantWatch, needed: true}
	writeSchedShot("bg2-schedules-grant-review-zh-dark-720x640.png", fxReview, 720, 640, "zh-Hans", true)

	fxDetail, detail, _, _ := seedVisualScheduledWorkFixture(t)
	fxDetail.app.scheduledWorkBotID = fxDetail.contact.ID
	fxDetail.app.schedules.bot(fxDetail.contact.ID).watchID = detail.WatchID
	fxDetail.app.schedules.bot(fxDetail.contact.ID).scroll.Y = 340
	writeSchedShot("bg2-schedules-occurrences-en-light-1040x720.png", fxDetail, 1040, 720, "en", false)

	for _, shot := range []struct {
		name, locale string
		w, h         int
		dark         bool
	}{
		{"bg2-settings-recovery-dormant-en-light-1040x720.png", "en", 1040, 720, false},
		{"bg2-settings-recovery-dormant-zh-dark-720x640.png", "zh-Hans", 720, 640, true},
	} {
		fxRec, _, _, _ := seedVisualScheduledWorkFixture(t)
		fxRec.app.scheduledWorkBotID = ""
		fxRec.app.openSettingsTab(settingsTabAppearance, settingsSourceCommand)
		s := fxRec.app.ensureDataRecovery()
		s.target = &storeRecoveryTarget{}
		s.canonical = fxRec.app.storePath
		s.viewGeneration = fxRec.app.folderChoiceGeneration
		s.owner = fxRec.app.selectedContact()
		s.copy = &storeRecoveryCopy{
			Path:           "/tmp/xuhu-backup.profile",
			Contacts:       2,
			Issues:         1,
			Timers:         1,
			Schedules:      2,
			DormantWatches: 3,
			ModifiedAt:     time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC),
		}
		s.scopeOpen = true
		fxRec.app.settingsScroll.Y = 900
		writeSchedShot(shot.name, fxRec, shot.w, shot.h, shot.locale, shot.dark)
	}
}

func TestPreviewNativeLifecycle(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write BG-3A native lifecycle previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, shot := range []struct {
		name, locale string
		w, h         int
		dark, retry  bool
	}{
		{"bg3a-draining-en-light-1040x720.png", "en", 1040, 720, false, false},
		{"bg3a-draining-zh-dark-720x640.png", "zh-Hans", 720, 640, true, false},
		{"bg3a-drain-retry-zh-dark-720x640.png", "zh-Hans", 720, 640, true, true},
	} {
		fx, _, _, _ := seedVisualScheduledWorkFixture(t)
		fx.app.windowWidth = shot.w
		fx.app.shutdownStarted = true
		fx.app.shutdownDrainStarted = !shot.retry
		if shot.retry {
			fx.app.runtimeErrKind = runtimeNoticeRuntimeShutdown
			fx.app.runtimeErr = "worker did not drain before timeout"
		}
		tr := ui.NewTesterWith(fx.app.nativeWindowView, shot.w, shot.h, ui.TesterOptions{Dark: shot.dark})
		tr.SetLocale(shot.locale)
		tr.SetFocused(true)
		tr.Frame()
		path := filepath.Join(dir, shot.name)
		writePreviewPNG(t, path, tr.Image())
		t.Logf("bg3a preview path=%s width=%d height=%d locale=%s dark=%v", path, shot.w, shot.h, shot.locale, shot.dark)
	}
}

func TestPreviewMessageReadStatus(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write message read status previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, shot := range []struct {
		name, locale string
		w, h         int
		dark         bool
		setup        func(*appModel, *contact)
	}{
		{
			name: "read-status-consecutive-zh-dark-720x640.png", locale: "zh-Hans", w: 720, h: 640, dark: true,
			setup: func(a *appModel, ct *contact) {
				ct.Messages = []*message{
					{ID: 1, Kind: msgUser, Text: "请先看下部署状态。", ReadByBot: true},
					{ID: 2, Kind: msgUser, Text: "确认无误后告诉我结果。", ReadByBot: true},
				}
			},
		},
		{
			name: "read-status-typing-en-light-1040x720.png", locale: "en", w: 1040, h: 720, dark: false,
			setup: func(a *appModel, ct *contact) {
				ct.Status = statusWorking
				ct.Messages = []*message{
					{ID: 1, Kind: msgUser, Text: "Can you check the rollout status and verify the checksum?", ReadByBot: true},
					{Key: "live:read-preview:text:pi", Kind: msgContact, Text: "Working on it", Streaming: true, liveReveal: true},
				}
			},
		},
		{
			name: "read-status-cleared-after-reply-zh-light-1040x720.png", locale: "zh-Hans", w: 1040, h: 720, dark: false,
			setup: func(a *appModel, ct *contact) {
				ct.Messages = []*message{
					{ID: 1, Kind: msgUser, Text: "请先看下部署状态。", ReadByBot: true},
					{ID: 2, Kind: msgUser, Text: "确认无误后告诉我结果。", ReadByBot: true},
					{ID: 3, Kind: msgContact, Text: "已核对完成，所有服务状态正常。", History: true},
				}
			},
		},
	} {
		a, ct := workspaceTestApp(t)
		a.windowWidth = shot.w
		shot.setup(a, ct)
		tr := ui.NewTesterWith(a.view, shot.w, shot.h, ui.TesterOptions{Dark: shot.dark})
		tr.SetLocale(shot.locale)
		tr.Frame()
		path := filepath.Join(dir, shot.name)
		writePreviewPNG(t, path, tr.Image())
		t.Logf("read-status preview path=%s width=%d height=%d locale=%s dark=%v", path, shot.w, shot.h, shot.locale, shot.dark)
	}
}

func TestPreviewOrchestrationAndTimeline(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write BG-3B orchestration previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	seedPreviewOrchestrationItem := func(ct *contact) pi.ProductOrchestrationSummary {
		return pi.ProductOrchestrationSummary{
			MandateID:       "watch:w-1",
			OwnerBotID:      ct.ID,
			SourceKind:      pi.ProductMandateSourceWatch,
			Purpose:         "Monitor release readiness",
			State:           pi.ProductMandateWaitingDecision,
			LastDisposition: pi.ProductDispositionNeedsDecision,
			Generation:      2,
			Revision:        7,
			PendingDecision: &pi.ProductOrchestrationDecision{
				DecisionID: "dec-1",
				MandateID:  "watch:w-1",
				OwnerBotID: ct.ID,
				Revision:   1,
				State:      "open",
				Kind:       "approval",
				Purpose:    "Approve rollout",
				Summary:    "Confirm whether to publish release notes.",
				Options: []pi.ProductOrchestrationDecisionOption{
					{ID: "publish", Label: "Publish notes"},
					{ID: "hold", Label: "Hold draft"},
				},
			},
			Metrics: pi.ProductOrchestrationMetrics{TotalWakes: 4, Calls: 3, TotalTokens: 980, CostUSD: 0.0042},
		}
	}
	for _, shot := range []struct {
		name, locale string
		w, h         int
		dark         bool
		setup        func(*appModel, *contact, *ui.Tester)
	}{
		{
			name: "bg3b-overview-menu-en-light-1040x720.png", locale: "en", w: 1040, h: 720, dark: false,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				ct.Messages = []*message{
					{
						Key:       "notice:p-en",
						Kind:      msgNotice,
						Text:      "Checklist smoke checks passed; release notes need sign-off.",
						Timestamp: time.Now(),
						Notice:    &pi.ProductConversationNotice{BotID: ct.ID, MandateID: "watch:w-1", Urgency: "high"},
					},
					{ID: 1, Kind: msgUser, Text: "Confirm once checks settle.", ReadByBot: true},
				}
				ct.orchestration.items = []pi.ProductOrchestrationSummary{seedPreviewOrchestrationItem(ct)}
				tr.Frame()
				_ = tr.Click(locales.In("en").BotOverviewLabel())
				tr.Frame()
			},
		},
		{
			name: "bg3b-drawer-list-zh-light-960x640.png", locale: "zh-Hans", w: 960, h: 640, dark: false,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				ct.Messages = []*message{
					{
						Key:       "notice:p-1",
						Kind:      msgNotice,
						Text:      "发布前冒烟检查已全部通过，发布说明待确认。",
						Timestamp: time.Now(),
						Notice:    &pi.ProductConversationNotice{BotID: ct.ID, MandateID: "watch:w-1", Urgency: "high"},
					},
					{ID: 1, Kind: msgUser, Text: "状态确认无误后告诉我。", ReadByBot: true},
				}
				item := seedPreviewOrchestrationItem(ct)
				item.Purpose = "监视发布检查清单"
				item.PendingDecision.Purpose = "确认是否发布更新说明"
				item.PendingDecision.Summary = "除发布说明外其他检查均已就绪。"
				item.PendingDecision.Options = []pi.ProductOrchestrationDecisionOption{
					{ID: "publish", Label: "发布说明"},
					{ID: "hold", Label: "暂缓发布"},
				}
				ct.orchestration.items = []pi.ProductOrchestrationSummary{item}
				a.openContactProfile(ct.ID)
				a.backgroundWork.panel = &backgroundWorkPanel{
					bot:    ct,
					opener: "header-overview:" + ct.ID,
					items:  ct.orchestration.items,
				}
				tr.Frame()
			},
		},
		{
			name: "bg3b-background-drawer-detail-en-dark-1040x720.png", locale: "en", w: 1040, h: 720, dark: true,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				summary := seedPreviewOrchestrationItem(ct)
				a.openContactProfile(ct.ID)
				a.backgroundWork.panel = &backgroundWorkPanel{
					bot:      ct,
					opener:   "header-overview:" + ct.ID,
					detailID: summary.MandateID,
					items:    []pi.ProductOrchestrationSummary{summary},
					detail: &pi.ProductOrchestrationDetail{
						ProductOrchestrationSummary: summary,
						RecentRuns: []pi.ProductOrchestrationRun{
							{RunID: "run-1", MandateID: summary.MandateID, Generation: 2, State: "completed", RouteRole: "utility", Disposition: pi.ProductDispositionNeedsDecision, Summary: "Requested sign-off on release notes.", Calls: 2, TotalTokens: 480},
						},
						RecentReceipts: []pi.ProductOrchestrationEffectReceipt{
							{ReceiptID: "rcpt-1", MandateID: summary.MandateID, RunID: "run-1", CallID: "c1", Tool: "conversation_notify", Target: "conv-1", State: "settled_ok"},
						},
					},
				}
				tr.Frame()
			},
		},
		{
			name: "bg3b-background-drawer-overlay-zh-dark-720x640.png", locale: "zh-Hans", w: 720, h: 640, dark: true,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				summary := seedPreviewOrchestrationItem(ct)
				summary.Purpose = "监视发布检查清单"
				a.openContactProfile(ct.ID)
				a.backgroundWork.panel = &backgroundWorkPanel{
					bot:      ct,
					opener:   "header-overview:" + ct.ID,
					detailID: summary.MandateID,
					items:    []pi.ProductOrchestrationSummary{summary},
					detail: &pi.ProductOrchestrationDetail{
						ProductOrchestrationSummary: summary,
						RecentRuns: []pi.ProductOrchestrationRun{
							{RunID: "run-1", MandateID: summary.MandateID, Generation: 2, State: "completed", RouteRole: "utility", Disposition: pi.ProductDispositionNeedsDecision, Summary: "已请求确认发布说明。", Calls: 2, TotalTokens: 480},
						},
						RecentReceipts: []pi.ProductOrchestrationEffectReceipt{
							{ReceiptID: "rcpt-1", MandateID: summary.MandateID, RunID: "run-1", CallID: "c1", Tool: "conversation_notify", Target: "conv-1", State: "settled_ok"},
						},
					},
				}
				tr.Frame()
			},
		},
		{
			name: "bg3b-queued-send-zh-dark-720x640.png", locale: "zh-Hans", w: 720, h: 640, dark: true,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				ct.SendState = sendQueued
				ct.SendQueuedPosition = 2
				ct.ActiveTaskID = "exec-queued-preview"
				ct.Draft = "补充核对发布标签校验和。"
				tr.Frame()
			},
		},
		{
			name: "bg3b-recovery-v14-zh-dark-720x640.png", locale: "zh-Hans", w: 720, h: 640, dark: true,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				a.openSettingsTab(settingsTabAppearance, settingsSourceCommand)
				s := a.ensureDataRecovery()
				s.target = &storeRecoveryTarget{}
				s.canonical = a.storePath
				s.viewGeneration = a.folderChoiceGeneration
				s.owner = ct
				s.scopeOpen = true
				s.copy = &storeRecoveryCopy{
					Path: "/Documents/Xuhu.backup.json", Contacts: 2, Issues: 1,
					Timers: 1, Schedules: 1, DormantWatches: 2,
					DormantMandates: 3, Decisions: 2, Notices: 4, ModifiedAt: time.Now(),
				}
				want := locales.In("zh-Hans").RecoveryPreviewOrchestrationCounts(3, 2, 4)
				for y := float32(0); y <= 2400; y += 80 {
					a.settingsScroll.Y = y
					tr.Frame()
					if r, ok := tr.Find(want); ok && r.H > 0 && r.Y >= 80 && r.Y+r.H <= 500 {
						break
					}
				}
			},
		},
	} {
		a, ct := workspaceTestApp(t)
		a.windowWidth = shot.w
		a.demo = false
		a.runtimeStatus = runtimeStateReady
		a.connectionGeneration = 1
		a.pi = phase3StubProductHost(t, a, visualGuardSeededPool())
		a.profileEditors = map[string]*contactProfileEditor{ct.ID: newContactProfileEditor(contactProfileDraft(ct))}
		tr := ui.NewTesterWith(a.view, shot.w, shot.h, ui.TesterOptions{Dark: shot.dark})
		tr.SetLocale(shot.locale)
		tr.SetFocused(true)
		shot.setup(a, ct, tr)
		path := filepath.Join(dir, shot.name)
		writePreviewPNG(t, path, tr.Image())
		t.Logf("bg3b preview path=%s width=%d height=%d locale=%s dark=%v", path, shot.w, shot.h, shot.locale, shot.dark)
	}
}

func TestPreviewComposerPlusAlign(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write the composer plus alignment previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, shot := range []struct {
		name, locale string
		w, h         int
		dark         bool
		setup        func(*appModel, *contact, *ui.Tester)
	}{
		{
			name: "composer-empty-en-light-1040x720.png", locale: "en", w: 1040, h: 720, dark: false,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				tr.SetFocused(false)
				tr.Move(-1, -1)
				tr.Frame()
			},
		},
		{
			name: "composer-empty-zh-dark-1040x720.png", locale: "zh-Hans", w: 1040, h: 720, dark: true,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				tr.SetFocused(false)
				tr.Move(-1, -1)
				tr.Frame()
			},
		},
		{
			name: "composer-empty-zh-light-720x640.png", locale: "zh-Hans", w: 720, h: 640, dark: false,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				tr.SetFocused(false)
				tr.Move(-1, -1)
				tr.Frame()
			},
		},
		{
			name: "composer-empty-en-dark-720x640.png", locale: "en", w: 720, h: 640, dark: true,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				tr.SetFocused(false)
				tr.Move(-1, -1)
				tr.Frame()
			},
		},
		{
			name: "composer-hover-plus-en-light-1040x720.png", locale: "en", w: 1040, h: 720, dark: false,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				tr.SetFocused(true)
				tr.Frame()
				showPreviewTooltip(t, tr, locales.In("en").ComposerAttach())
			},
		},
		{
			name: "composer-hover-plus-zh-dark-1040x720.png", locale: "zh-Hans", w: 1040, h: 720, dark: true,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				tr.SetFocused(true)
				tr.Frame()
				showPreviewTooltip(t, tr, locales.In("zh-Hans").ComposerAttach())
			},
		},
		{
			name: "composer-quote-materials-en-light-960x640.png", locale: "en", w: 960, h: 640, dark: false,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				ct.DraftQuote = &pi.ProductQuote{
					MessageID:  "q-preview",
					Sender:     "bot",
					AuthorName: ct.Name,
					Text:       "Align the borderless plus icon optically with placeholder text, quote strip, and chips.",
				}
				if _, err := ct.Context.AddSnippet("Design notes", "Composer spacing spec"); err != nil {
					t.Fatal(err)
				}
				if _, err := ct.Context.AddSnippet("UI checklist", "V-CP-01 alignment"); err != nil {
					t.Fatal(err)
				}
				a.openContactProfile(ct.ID)
				tr.SetFocused(false)
				tr.Move(-1, -1)
				tr.Frame()
			},
		},
		{
			name: "composer-quote-materials-zh-dark-960x640.png", locale: "zh-Hans", w: 960, h: 640, dark: true,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				ct.DraftQuote = &pi.ProductQuote{
					MessageID:  "q-preview",
					Sender:     "bot",
					AuthorName: ct.Name,
					Text:       "将无边框加号图标与上方占位文字、引用条和资料芯片光学左对齐。",
				}
				if _, err := ct.Context.AddSnippet("设计规范", "输入框边距与对齐细节"); err != nil {
					t.Fatal(err)
				}
				if _, err := ct.Context.AddSnippet("验收清单", "V-CP-01 无边框与对齐"); err != nil {
					t.Fatal(err)
				}
				a.openContactProfile(ct.ID)
				tr.SetFocused(false)
				tr.Move(-1, -1)
				tr.Frame()
			},
		},
		{
			name: "composer-quote-materials-popover-zh-light-720x640.png", locale: "zh-Hans", w: 720, h: 640, dark: false,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				ct.DraftQuote = &pi.ProductQuote{
					MessageID:  "q-preview",
					Sender:     "bot",
					AuthorName: ct.Name,
					Text:       "点击加号后上方弹出主题菜单，按钮保持 26 × 26 柔和底色。",
				}
				if _, err := ct.Context.AddSnippet("附件清单", "对齐验证材料"); err != nil {
					t.Fatal(err)
				}
				if _, err := ct.Context.AddSnippet("构建日志", "离线检查结果"); err != nil {
					t.Fatal(err)
				}
				tr.SetFocused(true)
				tr.Frame()
				if err := tr.Click(locales.In("zh-Hans").ComposerAttach()); err != nil {
					t.Fatal(err)
				}
				tr.Advance(theme.ControlTransition.Duration)
				tr.Frame()
			},
		},
		{
			name: "composer-focus-ring-en-dark-1040x720.png", locale: "en", w: 1040, h: 720, dark: true,
			setup: func(a *appModel, ct *contact, tr *ui.Tester) {
				tr.SetFocused(true)
				tr.Move(-1, -1)
				tr.Frame()
				_ = tr.Click(locales.In("en").ComposerLabel(ct.Name))
				tr.Frame()
				for i := 0; i < 24 && !tr.Focused(locales.In("en").ComposerAttach()); i++ {
					tr.Key(0, ui.KeyTab)
					tr.Frame()
				}
				tr.Key(0, ui.KeyEnter)
				tr.Frame()
				tr.Key(0, ui.KeyEscape)
				tr.Advance(theme.ControlTransition.Duration)
				tr.Frame()
			},
		},
	} {
		a, ct := workspaceTestApp(t)
		a.windowWidth = shot.w
		a.workspaceOpen = false
		a.focusComposer = false
		ct.SessionID = "composer-plus-preview"
		ct.Draft = ""
		ct.Context.Items = nil
		tr := ui.NewTesterWith(a.view, shot.w, shot.h, ui.TesterOptions{Dark: shot.dark})
		tr.SetLocale(shot.locale)
		shot.setup(a, ct, tr)
		path := filepath.Join(dir, shot.name)
		writePreviewPNG(t, path, tr.Image())
		t.Logf("composer plus preview path=%s width=%d height=%d locale=%s dark=%v", path, shot.w, shot.h, shot.locale, shot.dark)
	}
}
