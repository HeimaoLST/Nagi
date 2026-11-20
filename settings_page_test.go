package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestSettingsPageReplacesWorkbenchAndRoutesEntries(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.pi = presentationPiHost(a)
	ct.Draft = "keep the conversation draft"
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAppearance {
		t.Fatalf("settings page = open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
	if !tr.Focused("← Back to workbench") {
		t.Fatalf("settings page did not focus Back to workbench: %v", tr.Texts())
	}
	if a.settingsScroll.Y != 0 {
		t.Fatalf("settings page opened scrolled: %v", a.settingsScroll.Y)
	}
	msgs := locales.In("en")
	for _, forbidden := range []string{msgs.RailContactsTitle(), msgs.AvatarAddContact(), msgs.ComposerLabel(ct.Name), "Send", "History", msgs.ContactProfileAction()} {
		if tr.HasText(forbidden) {
			t.Fatalf("settings page still rendered workbench text %q: %v", forbidden, tr.Texts())
		}
	}
	for _, want := range []string{msgs.SettingsTitle(), msgs.SettingsGeneral(), msgs.SettingsConnection(), msgs.SettingsAdvanced()} {
		if !tr.HasText(want) {
			t.Fatalf("settings page missing %q: %v", want, tr.Texts())
		}
	}
	if err := tr.Click("Connection & model"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.settingsScroll.Y != 0 {
		t.Fatalf("connection category opened scrolled: %v", a.settingsScroll.Y)
	}
	if a.settingsTab != settingsTabConnection || !tr.HasText("No connection is currently applied.") {
		t.Fatalf("connection category = %q texts=%v", a.settingsTab, tr.Texts())
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.settingsOpen || !tr.Focused("Settings") {
		t.Fatalf("return from settings = open=%v settingsFocus=%v", a.settingsOpen, tr.Focused("Settings"))
	}
	if ct.Draft != "keep the conversation draft" {
		t.Fatalf("settings changed the conversation draft: %q", ct.Draft)
	}
}

func TestCustomThemeSettingsSelectReloadAndReturnKeepsWorkState(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	a.appearanceMode = appearanceLight
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	ct.Draft, ct.ActiveTaskID = "Keep my draft while changing themes", "active-task"
	directory := t.TempDir()
	document := customThemeDocument("garden", appearanceLight)
	document["colors"].(map[string]any)["background"] = "#E8F0EA"
	file := writeCustomTheme(t, directory, "garden.json", document)
	a.appearanceThemes = loadCustomThemes(directory)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	focusComposer(t, tr, "Composer for Nagi")
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if err := tr.Click(locales.In("en").ThemePaper()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Garden (garden)") {
		t.Fatal("theme selector did not open the custom option")
	}
	tr.Key(0, ui.KeyEnd)
	tr.Frame()
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if a.appearanceThemeIDs.Light != "garden" || a.workTheme.Theme.Background != ui.Hex("#E8F0EA") || a.appearanceError != "" {
		t.Fatalf("theme selector did not apply/save: ids=%+v error=%q", a.appearanceThemeIDs, a.appearanceError)
	}
	if !tr.Focused("Light theme") {
		t.Fatal("theme selection lost the picker keyboard focus")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := tr.Click("Reload themes"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.appearanceThemeIDs.Light != "garden" || !a.resolveAppearanceTheme(false).Unavailable || !tr.HasText("garden unavailable — using Bee Light") {
		t.Fatalf("reload forgot or hid missing selection: %v", tr.Texts())
	}
	writeCustomTheme(t, directory, "restored-name.json", document)
	if err := tr.Click("Reload themes"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.resolveAppearanceTheme(false).EffectiveID != "garden" || a.workTheme.Theme.Background != ui.Hex("#E8F0EA") {
		t.Fatal("restoring a selected file did not recover on explicit reload")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Composer for Nagi") || ct.Draft != "Keep my draft while changing themes" || ct.ActiveTaskID != "active-task" || ct.SessionID != "s1" {
		t.Fatalf("theme settings changed focus/work state: focus=%v contact=%+v", tr.Focused("Composer for Nagi"), ct)
	}
}

func TestCustomThemeSettingsLowContrastFailedSaveRetry(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.appearanceMode = appearanceLight
	ct.Draft, ct.ActiveTaskID = "Keep failed-save draft", "active-task"
	a.runtimeErr = "unrelated Runtime error"
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.store = &contactStore{path: filepath.Join(blocked, "contacts.json")}
	directory := t.TempDir()
	document := customThemeDocument("low-contrast", appearanceLight)
	document["colors"].(map[string]any)["work_attention"] = "#FFFFFF"
	writeCustomTheme(t, directory, "low.json", document)
	a.appearanceThemes = loadCustomThemes(directory)
	a.openAppearance()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	if err := tr.Click(locales.In("en").ThemePaper()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Garden (low-contrast)") {
		t.Fatal("theme selector did not open the low-contrast option")
	}
	tr.Key(0, ui.KeyEnd)
	tr.Frame()
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	resolved := a.resolveAppearanceTheme(false)
	if a.appearanceError != appearanceStatusSaveFailed || resolved.EffectiveID != "low-contrast" || a.workTheme.WorkAttention != ui.Hex("#FFFFFF") || !tr.Focused("Light theme") {
		t.Fatalf("failed save did not retain theme and picker focus: error=%q resolution=%+v", a.appearanceError, resolved)
	}
	if len(resolved.Warnings) == 0 {
		t.Fatal("applied low-contrast theme omitted its advisory warning")
	}
	warning := resolved.Warnings[0]
	message := locales.In("en").ThemeContrastWarning("low-contrast", warning.Foreground, warning.Background, fmt.Sprintf("%.2f", warning.Ratio), fmt.Sprintf("%.1f", warning.Minimum))
	if !tr.HasText(message) || !tr.HasText("Retry save") {
		t.Fatalf("warning/save recovery was not visible: %v", tr.Texts())
	}
	a.store.path = filepath.Join(t.TempDir(), "contacts.json")
	if err := tr.Click("Retry save"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	reloaded, err := openContactStore(a.store.path, "/ignored")
	if err != nil || reloaded.state.ThemeIDs == nil || reloaded.state.ThemeIDs.Light != "low-contrast" || a.appearanceError != "" {
		t.Fatalf("UI retry did not persist theme: error=%v appearance=%q", err, a.appearanceError)
	}
	if ct.Draft != "Keep failed-save draft" || ct.ActiveTaskID != "active-task" || a.runtimeErr != "unrelated Runtime error" {
		t.Fatal("theme recovery changed work or Runtime state")
	}
	if dir := os.Getenv("NAGI_PREVIEW_DIR"); dir != "" {
		writeV07AppearancePreview(t, dir, "custom-theme-warning.png", a, 1040, 720, false)
	}
}

func TestSettingsPageConnectionErrorRoutesToConnectionCategory(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040

	ct.LoadErr = "connection_mismatch: session is bound elsewhere"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click(locales.In("en").ConnectionRecoveryOpenSettings()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabConnection {
		t.Fatalf("connection entry route = open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
}

func TestSettingsCategoryChangeResetsContentScrollAndKeepsDraft(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	a.connectionForm.Draft.Model = "unsaved model"
	a.settingsScroll.Y = 200
	a.selectSettingsTab(settingsTabAdvanced)
	if a.settingsScroll.Y != 0 || a.settingsTab != settingsTabAdvanced {
		t.Fatalf("category switch = scroll=%v tab=%q", a.settingsScroll.Y, a.settingsTab)
	}
	a.selectSettingsTab(settingsTabConnection)
	if a.connectionForm.Draft.Model != "unsaved model" {
		t.Fatal("category switch discarded the connection draft")
	}
}

func TestGeneralPreferenceSelectorsKeepWidthAndKeyboardChoice(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, width := range []int{720, 1040} {
			t.Run(fmt.Sprintf("%s-%d", locale, width), func(t *testing.T) {
				a, ct := workspaceTestApp(t)
				recoveryTestStore(t, a)
				a.windowWidth = width
				a.languageModeLoaded = true
				a.languageMode = ""
				ct.Draft = "preserve current text 原文"
				a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
				oldApply := applyAppLocale
				defer func() { applyAppLocale = oldApply }()
				var applied []string
				applyAppLocale = func(tag string) { applied = append(applied, tag) }
				tr := ui.NewTester(a.view, width, 720)
				tr.SetLocale(locale)
				tr.SetFocused(true)
				tr.Frame()
				msgs := locales.In(locale)
				selectedMode := msgs.AppearanceSystem()
				for step := 0; step < 12; step++ {
					bounds, found := tr.Find(selectedMode)
					if found && bounds.W > 0 && bounds.H > 0 && bounds.Y >= 0 && bounds.Y+bounds.H <= 720 {
						break
					}
					tr.Scroll(float32(width-100), 600, 0, 100)
					tr.Frame()
				}
				if err := tr.Click(selectedMode); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if !tr.HasText(msgs.AppearanceLight()) || !tr.HasText(msgs.AppearanceDark()) {
					t.Fatal("the displayed appearance value did not open its choices")
				}
				tr.Key(0, ui.KeyEscape)
				tr.Frame()
				if len(applied) != 0 {
					t.Fatal("rendering a General selector changed locale")
				}
				for step := 0; step < 16 && !tr.Focused(msgs.SettingsLanguage()); step++ {
					tr.Key(0, ui.KeyTab)
					tr.Frame()
				}
				if !tr.Focused(msgs.SettingsLanguage()) {
					t.Fatal("real Language selector was not keyboard reachable")
				}
				tr.Key(0, ui.KeySpace)
				tr.Frame()
				tr.Key(0, ui.KeyEnd)
				tr.Frame()
				tr.Key(0, ui.KeyEnter)
				tr.Frame()
				if len(applied) != 1 || applied[0] != "en" || a.languageMode != "en" || ct.Draft != "preserve current text 原文" || a.settingsTab != settingsTabAppearance {
					t.Fatalf("real popup keyboard choice lost its stable value/state: applied=%v mode=%q draft=%q", applied, a.languageMode, ct.Draft)
				}
			})
		}
	}
}

func TestSettingsConnectionDirtyDiscardAndAppliedReopen(t *testing.T) {
	config := connectionConfig{API: connectionAPIChatCompletions, Endpoint: "http://example.test/v1", APIKey: "fixture", Model: "fixture", ContextTokens: 1000, OutputTokens: 100, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
	a, _ := workspaceTestApp(t)
	a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
	a.connectionSaved = &config
	a.connectionEffective = &config
	a.pi = presentationPiHost(a)
	a.runtimeStatus = runtimeStateReady
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	if !a.connectionApplied() || a.connectionForm.Dirty() {
		t.Fatalf("applied reopen = applied=%v dirty=%v", a.connectionApplied(), a.connectionForm.Dirty())
	}
	a.connectionForm.Draft.Model = "changed"
	if !a.connectionForm.Dirty() {
		t.Fatal("connection edit was not marked dirty")
	}
	tr := ui.NewTester(a.view, 1040, 720)
	if err := clickSettingsAction(tr, "Discard changes"); err != nil {
		t.Fatal(err)
	}
	if a.connectionForm.Dirty() || a.connectionForm.Draft.Model != "fixture" {
		t.Fatalf("connection discard = %+v", a.connectionForm.Draft)
	}
}

func TestSettingsConnectionEditorsSurviveNavigationAndLocale(t *testing.T) {
	a, ct := workspaceTestApp(t)
	recoveryTestStore(t, a)
	saved := testConnectionConfig(t)
	effective := saved
	effective.Model = "effective-model"
	a.connectionSaved, a.connectionEffective = &saved, &effective
	form := newConnectionSettings(&saved, saved.WorkingDirectory)
	a.connectionForm = form
	ct.Draft = "keep conversation text"
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	if err := clickSettingsAction(tr, "Edit Default model"); err != nil {
		t.Fatal(err)
	}
	if !tr.Focused("Default model") {
		t.Fatal("model Edit did not focus the existing input")
	}
	tr.Key(ui.Cmd, ui.KeyA)
	tr.Type("unsaved-model")
	tr.Frame()
	if err := clickSettingsAction(tr, "Close Default model editor"); err != nil {
		t.Fatal(err)
	}
	form.Draft.Slots = "invalid slots"
	form.Errors = map[string]string{"MaxActiveTasks": validationNumeric}
	form.Error, form.ErrorDetail = connErrorTestFailed, "original failure detail"
	form.Status = connStatusTestCompleted
	form.StatusArgs = []string{"tested-model", "https://tested.example.invalid", "1000", "100"}
	draft := form.Draft
	a.selectSettingsTab(settingsTabAppearance)
	tr.Frame()
	a.selectSettingsTab(settingsTabConnection)
	tr.Frame()
	a.closeSettings()
	tr.Frame()
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	tr.Frame()
	tr.SetLocale("zh-Hans")
	tr.Frame()
	tr.SetLocale("en")
	tr.Frame()
	if a.connectionForm != form || form.Draft != draft || form.ErrorDetail != "original failure detail" || form.Status != connStatusTestCompleted || form.Errors["MaxActiveTasks"] != validationNumeric || !form.Dirty() || !a.connectionSavedPending() {
		t.Fatal("navigation/locale replaced the form, raw edits or independent feedback")
	}
	if *a.connectionSaved != saved || *a.connectionEffective != effective || a.pi != nil || ct.SessionID != "s1" || ct.Draft != "keep conversation text" {
		t.Fatal("disclosure/navigation changed saved, effective or conversation state, or started Runtime")
	}
	if err := clickSettingsAction(tr, "Edit Default model"); err != nil {
		t.Fatal(err)
	}
	tr.Type("-again")
	tr.Frame()
	if form.Draft.Model != "unsaved-model-again" || form.ErrorDetail != "original failure detail" {
		t.Fatal("reopened editor lost current text, focus or feedback")
	}
}

func TestPreferencesListControlsAndPreviews(t *testing.T) {
	directory := os.Getenv("NAGI_PREVIEW_DIR")
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, dark := range []bool{false, true} {
			for _, size := range [][2]int{{720, 640}, {1040, 720}} {
				for _, scene := range []string{"general", "fresh", "saved", "dirty", "invalid-long"} {
					name := fmt.Sprintf("preferences-%s-%s-%dx%d-dark-%t", scene, locale, size[0], size[1], dark)
					t.Run(name, func(t *testing.T) {
						a, ct := workspaceTestApp(t)
						recoveryTestStore(t, a)
						a.windowWidth = size[0]
						config := testConnectionConfig(t)
						a.connectionPath = filepath.Join(t.TempDir(), "connection.json")
						if scene != "fresh" {
							if err := saveConnectionConfig(a.connectionPath, config); err != nil {
								t.Fatal(err)
							}
							saved, err := loadConnectionConfig(a.connectionPath)
							if err != nil {
								t.Fatal(err)
							}
							a.connectionSaved = &saved
						}
						a.connectionForm = newConnectionSettings(a.connectionSaved, "")
						form := a.connectionForm
						if scene == "dirty" || scene == "invalid-long" {
							form.Draft.Endpoint = "https://current.example.invalid/" + strings.Repeat("long-project/", 10)
							form.Draft.Model = "current/" + strings.Repeat("long-model-name-", 8)
						}
						if scene == "invalid-long" {
							form.Draft.APIKey = ""
							form.Draft.ContextTokens, form.Draft.OutputTokens, form.Draft.Slots = "9007199254740993suffix", "1.5", "invalid slots"
							form.validated()
							form.Errors["Endpoint"] = strings.Repeat("A complete original field error must remain readable while scrolling. ", 8)
						}
						tab := settingsTabConnection
						if scene == "general" {
							tab = settingsTabAppearance
							a.appearanceThemeIDs = appearanceThemeIDs{Light: "paper", Dark: "night"}
						}
						a.openSettingsTab(tab, settingsSourceRail)
						tr := ui.NewTester(a.view, size[0], size[1])
						tr.SetLocale(locale)
						tr.SetDark(dark)
						tr.SetFocused(true)
						tr.Frame()
						msgs := locales.In(locale)
						controls := []string{msgs.SettingsBackAccessible()}
						if scene == "general" {
							controls = append(controls, msgs.ThemeImportAction(), msgs.ThemeReload())
						} else {
							controls = append(controls, msgs.WorkFolderChoose(), msgs.ConnectionFolderEnterPath(), msgs.ConnectionSaveConfiguration(), msgs.ConnectionTestAction())
							if scene == "saved" || scene == "dirty" {
								controls = append(controls, msgs.ConnectionEditField(msgs.ConnectionFieldEndpoint()), msgs.ConnectionEditField(msgs.ConnectionFieldAPIKey()), msgs.ConnectionEditField(msgs.ConnectionFieldModel()))
							}
						}
						if scene == "saved" {
							for i := 0; i < 18; i++ {
								tr.Key(0, ui.KeyTab)
								tr.Frame()
								if active, _ := tr.TextInputActive(); active {
									t.Fatal("saved quiescent settings kept a permanent free-text input")
								}
							}
							tr.Key(0, ui.KeyEscape)
						}
						if scene == "fresh" {
							fields := []struct {
								label string
								value *string
							}{
								{msgs.ConnectionFieldEndpoint(), &form.Draft.Endpoint},
								{msgs.ConnectionFieldAPIKey(), &form.Draft.APIKey},
								{msgs.ConnectionFieldModel(), &form.Draft.Model},
							}
							inputs := make(map[string]bool, len(fields))
							for i := 0; i < 18; i++ {
								tr.Key(0, ui.KeyTab)
								tr.Frame()
								for _, field := range fields {
									if inputs[field.label] || !tr.Focused(field.label) {
										continue
									}
									if active, _ := tr.TextInputActive(); !active {
										t.Fatalf("focused preference %q is not an editor", field.label)
									}
									previous := *field.value
									tr.Key(ui.Cmd, ui.KeyA)
									tr.Type("qa-field-value")
									tr.Frame()
									if *field.value != "qa-field-value" {
										t.Fatalf("typing did not update the %q draft", field.label)
									}
									*field.value = previous
									tr.Frame()
									inputs[field.label] = true
								}
								if tr.Focused(msgs.ConnectionFieldWorkingDirectory()) {
									t.Fatal("fresh setup exposed the manual path before its disclosure")
								}
							}
							for _, field := range fields {
								if !inputs[field.label] {
									t.Fatalf("fresh setup required an Edit action for %q", field.label)
								}
							}
						}
						a.settingsScroll.Y = 0
						tr.Frame()
						seen := map[string]bool{}
						bounds := map[string]ui.Rect{}
						fullErrorSeen := false
						for step := 0; step < 24; step++ {
							for _, label := range controls {
								r, ok := tr.Find(label)
								if ok && r.X >= 0 && r.X+r.W <= float32(size[0]) && r.Y >= 0 && r.Y+r.H <= float32(size[1]) && r.H >= 32 {
									seen[label] = true
									bounds[label] = r
								}
							}
							for _, text := range tr.Texts() {
								r, ok := tr.Find(text)
								if ok && (r.X < 0 || r.X+r.W > float32(size[0])+1) {
									t.Fatalf("text crosses horizontal window edge: %q %+v", text, r)
								}
							}
							if scene == "invalid-long" {
								text := form.Errors["Endpoint"]
								r, ok := tr.Find(text)
								_, height := ui.ShapeText(text, ui.Font{Size: typeBody, LineHeight: 1.5, Lang: locale}, r.W).Size()
								visible := ok && r.H+0.5 >= height && r.Y >= 0 && r.Y+r.H <= float32(size[1])
								if visible && !fullErrorSeen && directory != "" {
									writePreviewPNG(t, filepath.Join(directory, name+"-full-error.png"), tr.Image())
								}
								fullErrorSeen = fullErrorSeen || visible
								if visible {
									bounds["complete-field-error"] = r
								}
							}
							before := a.settingsScroll.Y
							if directory != "" && (step == 0 || step == 2) {
								writePreviewPNG(t, filepath.Join(directory, fmt.Sprintf("%s-scroll-%02d.png", name, step)), tr.Image())
							}
							tr.Scroll(float32(size[0]/2), float32(size[1]-100), 0, 240)
							tr.Frame()
							if before == a.settingsScroll.Y {
								if directory != "" {
									writePreviewPNG(t, filepath.Join(directory, name+"-bottom.png"), tr.Image())
								}
								break
							}
						}
						for _, label := range controls {
							if !seen[label] {
								t.Errorf("required control not fully reachable with a 32px target: %q", label)
							}
						}
						if scene == "invalid-long" && !fullErrorSeen {
							t.Fatal("the complete original field error was not reachable by scrolling")
						}
						if a.pi != nil || a.connectionTesting || a.connectionApplying || ct.SessionID != "s1" {
							t.Fatal("rendering/keyboard traversal started execution or changed the session")
						}
						if directory != "" {
							data, err := json.MarshalIndent(bounds, "", "  ")
							if err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(filepath.Join(directory, name+".bounds.json"), data, 0600); err != nil {
								t.Fatal(err)
							}
						}
					})
				}
			}
		}
	}
}

func TestSettingsPageCmdCommaReturnsToFocusedComposer(t *testing.T) {
	a, ct := workspaceTestApp(t)
	recoveryTestStore(t, a)
	ct.Draft = "before "
	a.focusComposer = true
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.Focused("Composer for Nagi") {
		t.Fatal("composer did not receive initial focus")
	}
	tr.Type("settings")
	if ct.Draft != "before settings" {
		t.Fatalf("draft before Cmd+, = %q", ct.Draft)
	}
	if !tr.Focused("Composer for Nagi") {
		t.Fatal("typing lost composer focus before Cmd+,")
	}
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Cmd+, from composer did not open settings")
	}
	if a.settingsReturnFocus != settingsSourceComposer {
		t.Fatalf("Cmd+, captured return source %q; want composer", a.settingsReturnFocus)
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Composer for Nagi") {
		t.Fatalf("Back did not return focus to the composer: %v", tr.Texts())
	}
	tr.Type(" again")
	if ct.Draft != "before settings again" {
		t.Fatalf("typing after Back = %q", ct.Draft)
	}
}

func TestSettingsPageReturnKeepsChatScroll(t *testing.T) {
	a, ct := workspaceTestApp(t)
	directory := t.TempDir()
	writeCustomTheme(t, directory, "light.json", customThemeDocument("garden", appearanceLight))
	a.appearanceThemes = loadCustomThemes(directory)
	for i := range 30 {
		ct.Messages = append(ct.Messages, &message{ID: i + 1, Kind: msgContact, Text: fmt.Sprintf("message %d with enough text to scroll", i)})
	}
	ct.Draft = "keep the conversation draft"
	recoveryTestStore(t, a)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	tr.Scroll(500, 300, 0, -600)
	tr.Frame()
	first, last := ct.list.Visible()
	if last >= len(ct.Messages)-1 {
		t.Fatalf("chat did not scroll away from the latest message: first=%d last=%d", first, last)
	}
	label := locales.In("en").ConversationMessagesLabel()
	viewport, viewportFound := tr.Find(label)
	if !viewportFound || viewport.W <= 0 || viewport.H <= 0 {
		t.Fatalf("missing conversation viewport: %+v", viewport)
	}
	var anchor string
	var row ui.Rect
	for _, message := range ct.Messages[first : last+1] {
		candidate, found := tr.Find(message.Text)
		if found && candidate.W > 0 && candidate.H > 0 && candidate.Y >= viewport.Y && candidate.Y+candidate.H <= viewport.Y+viewport.H {
			anchor, row = message.Text, candidate
			break
		}
	}
	if anchor == "" {
		t.Fatalf("no visible scroll anchor between rows %d and %d in %+v", first, last, viewport)
	}
	offset := row.Y - viewport.Y
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("settings did not open")
	}
	a.setAppearanceTheme(appearanceLight, "garden")
	tr.Frame()
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	afterViewport, viewportFound := tr.Find(label)
	afterRow, rowFound := tr.Find(anchor)
	afterOffset := afterRow.Y - afterViewport.Y
	if !viewportFound || !rowFound || afterRow.H <= 0 || afterOffset < offset-1 || afterOffset > offset+1 ||
		a.selectedContact() != ct || ct.Draft != "keep the conversation draft" || a.localSaveErr != "" {
		t.Fatalf("settings return changed chat anchor %q: offset=%v→%v selected=%q draft=%q saveError=%q", anchor, offset, afterOffset, a.selectedID(), ct.Draft, a.localSaveErr)
	}
}

func TestPreviewSettingsPage(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	export := dir != ""
	if !export {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	longPath := "/Users/you/Workspace/a-very-long-project-directory-name/with/deep/nested/segments"
	longURL := "https://provider.example.invalid/v1/organizations/team/projects/workspace/models/default"
	longError := "Connection could not be applied because the saved endpoint is unavailable. Review the endpoint and retry after the current work finishes."
	config := connectionConfig{API: connectionAPIChatCompletions, Endpoint: longURL, Model: "provider/very-long-model-identifier", ContextTokens: 131072, OutputTokens: 8192, WorkingDirectory: longPath, MaxActiveTasks: 2}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, size := range []struct{ width, height int }{{720, 640}, {1040, 720}, {1380, 900}} {
			for _, tab := range []string{settingsTabAppearance, settingsTabConnection, settingsTabAdvanced} {
				a, ct, _ := piRunStartedTestApp(t)
				ct.ActiveTaskID, ct.SendState = "", sendIdle
				a.storePath = a.store.path
				a.connectionPath = connectionConfigPath(a.storePath)
				a.workDir = filepath.Dir(a.storePath)
				a.windowWidth = size.width
				switch tab {
				case settingsTabConnection:
					a.connectionForm = newConnectionSettings(&config, longPath)
					a.connectionSaved = &config
					a.connectionEffective = &config
					a.connectionForm.HasSaved = true
					a.connectionForm.Error = longError
					a.runtimeStatus = runtimeStateReady
				case settingsTabAdvanced:
					a.runtimeStatus = runtimeStateFailed
					a.runtimeStage = "Runtime helper"
					a.runtimeErr = longError
					a.openAdvancedSettings()
				}
				if tab != settingsTabAdvanced {
					a.openSettingsTab(tab, settingsSourceRail)
				}
				tr := ui.NewTester(a.view, size.width, size.height)
				tr.SetDark(dark)
				tr.Frame()
				if _, back := tr.Find("← Back to workbench"); !tr.HasText("Settings") || !back {
					t.Fatalf("settings preview missing page shell for %s %dx%d", tab, size.width, size.height)
				}
				if tab == settingsTabConnection {
					if a.settingsScroll.Y != 0 {
						t.Fatalf("connection preview scrolled at %dx%d: %v", size.width, size.height, a.settingsScroll.Y)
					}
					if !tr.HasText("Current:") {
						t.Fatalf("connection current state missing at %dx%d: %v", size.width, size.height, tr.Texts())
					}
					if r, ok := tr.Find("Protocol"); !ok || r.X < 0 || r.X+r.W > float32(size.width) || r.Y < 0 || r.Y+r.H > float32(size.height) {
						t.Fatalf("connection first field not visible at %dx%d: %+v ok=%v", size.width, size.height, r, ok)
					}
				}
				if export {
					writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("settings-%s-%s-%dx%d.png", tab, theme, size.width, size.height)), tr.Image())
				}
			}
		}
	}
}

func TestFirstLaunchOpensConnectionSettingsPageWithoutEmptyOverlay(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if !a.settingsOpen || a.settingsTab != settingsTabConnection || a.aux != "" {
		t.Fatalf("first launch route = open=%v tab=%q aux=%q", a.settingsOpen, a.settingsTab, a.aux)
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Connection & model") || !tr.HasText("Save configuration") || tr.HasText("No messages yet") {
		t.Fatalf("first launch did not show the connection page alone: %v", tr.Texts())
	}
	msgs := locales.In("en")
	if err := tr.Click(msgs.SettingsAdvanced()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText(msgs.StartupHintEnterConnection()) {
		t.Fatalf("unconfigured Advanced did not explain setup: %v", tr.Texts())
	}
	if err := tr.Click(msgs.SettingsOpenConnection()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.settingsTab != settingsTabConnection || !tr.HasText("Save configuration") || tr.HasText("No messages yet") {
		t.Fatalf("Advanced did not return to the connection page alone: %v", tr.Texts())
	}
}

func TestSettingsLongConnectionScrollsThenPrimaryActionIsVisible(t *testing.T) {
	for _, width := range []int{720, 800} {
		a, _ := workspaceTestApp(t)
		config := connectionConfig{API: connectionAPIChatCompletions, Endpoint: "https://provider.example.invalid/v1/very/long/endpoint", APIKey: "fixture", Model: "fixture", ContextTokens: 1000, OutputTokens: 100, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
		a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
		a.connectionSaved = &config
		a.connectionEffective = &config
		a.connectionForm.Error = strings.Repeat("A long form error that wraps without pushing controls out of the page. ", 8)
		a.windowWidth = width
		a.openSettingsTab(settingsTabConnection, settingsSourceRail)
		tr := ui.NewTester(a.view, width, 640)
		tr.Frame()
		tr.Scroll(float32(width/2), 400, 0, 2000)
		tr.Frame()
		if a.settingsScroll.Y <= 0 {
			t.Fatalf("%d long settings content did not scroll: %v", width, a.settingsScroll.Y)
		}
		for _, label := range []string{"Save configuration", "Apply saved", "Test connection and model"} {
			r, ok := tr.Find(label)
			if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > float32(width) || r.Y < 0 || r.Y+r.H > 640 {
				t.Fatalf("%d action %q not reachable after scroll: %+v ok=%v", width, label, r, ok)
			}
		}
	}
}

func TestSettingsCmdCommaFromMarkdownLinkReturnsToSameLink(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Messages = []*message{{
		ID: 1, Kind: msgContact, Text: "请阅读[文档](https://example.com/docs)。",
	}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	for i := 0; i < 80 && !tr.Focused("文档"); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused("文档") {
		t.Fatalf("Tab never reached the inline link: %v", tr.Texts())
	}
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAppearance {
		t.Fatalf("Cmd+, from link = open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("文档") {
		settingsButton := tr.Focused("Settings")
		reopened := false
		if settingsButton {
			tr.Key(0, ui.KeyEnter)
			tr.Frame()
			reopened = a.settingsOpen
		}
		t.Fatalf("Back did not return focus to the inline link: settingsButton=%v reopened=%v texts=%v",
			settingsButton, reopened, tr.Texts())
	}
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if got := tr.OpenedURLs(); len(got) != 1 || got[0] != "https://example.com/docs" {
		t.Fatalf("Enter on restored link opened %v, want the original URL", got)
	}
}

func tabToFocused(t *testing.T, tr *ui.Tester, label string) {
	t.Helper()
	for i := 0; i < 120 && !tr.Focused(label); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused(label) {
		t.Fatalf("Tab never reached %q: %v", label, tr.Texts())
	}
}

func TestSettingsCmdCommaFromMarkdownLinkReturnsToExactRepeatedURLPosition(t *testing.T) {
	for _, label := range []string{"第一链接", "第二链接"} {
		t.Run(label, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			ct.Messages = []*message{{
				ID: 1, Kind: msgContact,
				Text: "第一[第一链接](https://example.com/same) 第二[第二链接](https://example.com/same)",
			}}
			a.windowWidth = 1040
			tr := ui.NewTester(a.view, 1040, 720)
			tr.SetFocused(true)
			tr.Frame()
			tabToFocused(t, tr, label)
			tr.Key(ui.Cmd, ui.KeyComma)
			tr.Frame()
			if !a.settingsOpen {
				t.Fatal("Cmd+, from repeated URL link did not open settings")
			}
			if err := tr.Click("← Back to workbench"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tr.Focused(label) {
				t.Fatalf("Back focused wrong link, want %q: %v", label, tr.Texts())
			}
			tr.Key(0, ui.KeyEnter)
			tr.Frame()
			if got := tr.OpenedURLs(); len(got) != 1 || got[0] != "https://example.com/same" {
				t.Fatalf("%s opened %v, want only its own URL", label, got)
			}
		})
	}
}

func TestSettingsCmdCommaFromRemovedMarkdownLinkFallsBackOnce(t *testing.T) {
	a, ct := workspaceTestApp(t)
	const original = "请阅读[文档](https://example.com/docs)。"
	ct.Messages = []*message{{ID: 1, Kind: msgContact, Text: original}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	tabToFocused(t, tr, "文档")
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Cmd+, from link did not open settings")
	}
	ct.Messages[0].Text = "链接已移除"
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Composer for Nagi") {
		t.Fatalf("removed link did not fall back to the chat composer: %v", tr.Texts())
	}
	if a.markdownLinkFocus != (markdownLinkTarget{}) || a.linkReturnTarget != (markdownLinkTarget{}) {
		t.Fatalf("removed link left pending focus: focus=%+v target=%+v", a.markdownLinkFocus, a.linkReturnTarget)
	}
	ct.Messages[0].Text = original
	tr.Frame()
	tr.Frame()
	if !tr.Focused("Composer for Nagi") {
		t.Fatalf("restored old link stole focus from the composer: %v", tr.Texts())
	}
	if got := tr.OpenedURLs(); len(got) != 0 {
		t.Fatalf("removed or restored link activated a URL: %v", got)
	}
}

func TestSettingsCmdCommaFromReplacedMarkdownLinkDoesNotStealFocusWhenOldTargetReturns(t *testing.T) {
	a, ct := workspaceTestApp(t)
	const original = "请阅读[文档](https://example.com/original)。"
	ct.Messages = []*message{{ID: 1, Kind: msgContact, Text: original}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	tabToFocused(t, tr, "文档")
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Cmd+, from link did not open settings")
	}
	ct.Messages[0].Text = "请阅读[文档](https://example.com/replaced)。"
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Composer for Nagi") {
		t.Fatalf("replaced link did not fall back to the chat composer: %v", tr.Texts())
	}
	ct.Messages[0].Text = original
	tr.Frame()
	tr.Frame()
	if !tr.Focused("Composer for Nagi") {
		t.Fatalf("old link later reappeared and stole focus: %v", tr.Texts())
	}
	if a.linkReturnTarget != (markdownLinkTarget{}) || a.markdownLinkFocus != (markdownLinkTarget{}) {
		t.Fatalf("replaced link kept a delayed request: target=%+v focus=%+v", a.linkReturnTarget, a.markdownLinkFocus)
	}
	if got := tr.OpenedURLs(); len(got) != 0 {
		t.Fatalf("replaced or restored link activated a URL: %v", got)
	}
}

func TestExplicitRailSettingsDoesNotReuseOldMarkdownLinkReturn(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Messages = []*message{{
		ID: 1, Kind: msgContact, Text: "请阅读[文档](https://example.com/docs)。",
	}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	tabToFocused(t, tr, "文档")
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("文档") {
		t.Fatalf("first link return did not focus its link: %v", tr.Texts())
	}
	if err := tr.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("rail Settings did not open settings")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Settings") {
		t.Fatalf("rail Settings return did not focus the rail button: %v", tr.Texts())
	}
	if a.markdownLinkFocus != (markdownLinkTarget{}) || a.linkReturnTarget != (markdownLinkTarget{}) {
		t.Fatalf("rail Settings left the old link request pending: focus=%+v target=%+v", a.markdownLinkFocus, a.linkReturnTarget)
	}
}

func TestSettingsPendingBannerSurvivesDistinctTestStatus(t *testing.T) {
	a, _ := workspaceTestApp(t)
	config := testConnectionConfig(t)
	a.connectionSaved = &config
	a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
	a.connectionForm.Status = "Connection test completed: fixture"
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText(locales.In("en").ConnectionSavedAwaitingApply()) || !tr.HasText("Connection test completed: fixture") {
		t.Fatalf("pending fact was lost behind distinct test status: %v", tr.Texts())
	}
}

func TestBotProfileFolderChooserKeepsBotAndSessionUntilSave(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.uiLocale = "en"
	a.workDir = t.TempDir()
	ct.Workspace = t.TempDir()
	ct.SendState = sendUnknown
	a.openContactProfile(ct.ID)
	editor := a.profileEditorFor(ct.ID)
	editor.Draft.Name = "Unsaved name"
	before := editor.Draft
	requests, replies, updates := controlledFolderChoice(t, a)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := clickBotProfileAction(tr, locales.In("en").WorkFolderChoose()); err != nil {
		t.Fatal(err)
	}
	if got := awaitFolderChoice(t, requests).DefaultPath; got != ct.Workspace {
		t.Fatalf("contact chooser started in %q, want %q", got, ct.Workspace)
	}
	chosen := t.TempDir()
	replies <- folderChoiceReply{paths: []string{chosen}}
	awaitFolderChoice(t, updates)()
	tr.Frame()
	if editor.Draft.Workspace != chosen || editor.Draft.Name != before.Name || ct.Workspace != before.Workspace || ct.Name == before.Name || ct.SessionID != "s1" || ct.SendState != sendUnknown || !tr.Focused("Choose folder…") {
		t.Fatal("contact choice changed persisted identity/session or lost focus/draft")
	}
	if err := tr.Click("Use default folder"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if editor.Draft.Workspace != "" || ct.Workspace != before.Workspace || a.effectiveContactWorkspace(ct) != before.Workspace {
		t.Fatal("default-folder reset did not stay in the draft")
	}
	// The folder controls do not loosen unknown-work new-session protection.
	if _, ok := editor.Request(true); ok {
		t.Fatal("unknown work was allowed to start a replacement conversation")
	}
}

func TestBotProfileFolderChooserCancelErrorAndStaleIdentity(t *testing.T) {
	for _, change := range []string{"cancel", "error", "bot", "editor", "profile-roundtrip", "closed", "shutdown"} {
		t.Run(change, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			a.workDir = t.TempDir()
			ct.Workspace = t.TempDir()
			a.openContactProfile(ct.ID)
			editor := a.profileEditorFor(ct.ID)
			editor.Error = "existing profile error"
			editor.folderError = "existing chooser error"
			before := cloneProfileDraft(editor.Draft)
			requests, replies, updates := controlledFolderChoice(t, a)
			a.chooseContactWorkFolder(ct.ID, editor)
			awaitFolderChoice(t, requests)
			reply := folderChoiceReply{paths: []string{t.TempDir()}}
			switch change {
			case "cancel":
				reply = folderChoiceReply{}
			case "error":
				reply = folderChoiceReply{paths: []string{filepath.Join(t.TempDir(), "removed")}}
			case "bot":
				other := &contact{ID: "other", Name: "Other"}
				a.contacts = append(a.contacts, other)
				a.openContactProfile(other.ID)
			case "editor":
				a.profileEditors[ct.ID] = newContactProfileEditor(before)
			case "profile-roundtrip":
				a.closeDetail()
				a.openContactProfile(ct.ID)
			case "closed":
				a.closeDetail()
			case "shutdown":
				a.shutdownStarted = true
			}
			replies <- reply
			awaitFolderChoice(t, updates)()
			if !profileDraftEqual(editor.Draft, before) || editor.Error != "existing profile error" || editor.Busy || ct.Workspace != before.Workspace {
				t.Fatal("cancel/error/stale choice replaced the contact draft or saved profile")
			}
			if change == "error" && editor.folderError == "existing chooser error" {
				t.Fatal("removed folder did not produce chooser feedback")
			}
			if change != "error" && editor.folderError != "existing chooser error" {
				t.Fatal("cancel/stale result changed the existing chooser error")
			}
		})
	}
}

func TestSettingsThemeImportChooseSelectAndRestart(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, width := range []int{720, 1040} {
			t.Run(fmt.Sprintf("%s/%d", locale, width), func(t *testing.T) {
				a, ct, callbacks := themeImportTestApp(t)
				a.windowWidth = width
				a.win = &mygo.Window{}
				sourceDir := t.TempDir()
				source := writeCustomTheme(t, sourceDir, "下载 theme.json", customThemeDocument("downloaded-garden", appearanceLight))
				sourceBytes, err := os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}
				picked := make(chan mygo.OpenDialogOptions, 1)
				calls := 0
				a.chooseThemeFile = func(options mygo.OpenDialogOptions) ([]string, error) {
					picked <- options
					return []string{source}, nil
				}
				tr := ui.NewTester(a.view, width, 720)
				tr.SetLocale(locale)
				tr.SetFocused(true)
				tr.Frame()
				msgs := locales.In(locale)
				before := recoveryRecordSnapshot(t, ct)
				ids, mode := a.appearanceThemeIDs, a.appearanceMode
				stored, err := os.ReadFile(a.store.path)
				if err != nil {
					t.Fatal(err)
				}
				if !tr.HasText(msgs.ThemeImportHint()) {
					t.Fatal("ordinary file import guidance missing")
				}
				if err := clickSettingsAction(tr, msgs.ThemeImportAction()); err != nil {
					t.Fatal(err)
				}
				var options mygo.OpenDialogOptions
				select {
				case options = <-picked:
					calls++
				case <-time.After(5 * time.Second):
					t.Fatal("Import did not open the controlled chooser")
				}
				if options.Parent != a.win || options.Directory || options.Multiple || options.DefaultPath == "" {
					t.Fatal("chooser did not target this app and one local file")
				}
				themeImportCompletion(t, callbacks)()
				themeImportCompletion(t, callbacks)()
				tr.Frame()
				imported := a.themeImport
				if imported.Problem != nil || imported.ImportedName != "Garden" || imported.Busy || !tr.HasText(msgs.ThemeImportSucceeded("Garden")) || !tr.Focused(msgs.ThemeImportAction()) {
					t.Fatalf("import result/focus unavailable: state=%+v texts=%v", imported, tr.Texts())
				}
				data, err := os.ReadFile(filepath.Join(a.appearanceThemes.Directory, filepath.Base(source)))
				if err != nil || string(data) != string(sourceBytes) {
					t.Fatal("downloaded bytes/basename were not installed")
				}
				after, err := os.ReadFile(a.store.path)
				if err != nil {
					t.Fatal(err)
				}
				if calls != 1 || a.appearanceThemeIDs != ids || a.appearanceMode != mode || string(after) != string(stored) || recoveryRecordSnapshot(t, ct) != before || a.submissionInFlight != 0 {
					t.Fatal("import selected/saved a theme or changed work/preferences")
				}
				tr.Scroll(float32(width/2), 360, 0, -2000)
				tr.Frame()
				if err := tr.Click(msgs.ThemePaper()); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if !tr.HasText("Garden (downloaded-garden)") {
					t.Fatal("theme selector did not open the imported option")
				}
				tr.Key(0, ui.KeyEnd)
				tr.Key(0, ui.KeyEnter)
				tr.Frame()
				if a.appearanceThemeIDs.Light != "downloaded-garden" || a.appearanceMode != mode || a.appearanceError != "" || recoveryRecordSnapshot(t, ct) != before {
					t.Fatal("normal explicit selection did not save independently of active work")
				}
				reloaded, err := openContactStore(a.store.path, "/ignored")
				if err != nil {
					t.Fatal(err)
				}
				restarted := &appModel{appearanceThemes: loadCustomThemes(a.appearanceThemes.Directory)}
				applyStoreSelection(restarted, reloaded)
				if restarted.appearanceThemeIDs.Light != "downloaded-garden" || restarted.appearanceMode != mode || restarted.resolveAppearanceTheme(false).EffectiveID != "downloaded-garden" || restarted.selectedContact().ID != ct.ID || restarted.selectedContact().Draft != ct.Draft {
					t.Fatal("restart did not discover the installed theme/retained preference and draft")
				}
				if dir := os.Getenv("NAGI_THEME_PREVIEW_DIR"); dir != "" {
					writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("import-success-%s-%d.png", locale, width)), tr.Image())
				}
			})
		}
	}
}

func TestSettingsThemeImportCancelErrorAndRetry(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			a, ct, callbacks := themeImportTestApp(t)
			a.themeImport = &appearanceThemeImport{Problem: &themeFileError{File: "earlier.json", Field: "id", Message: "earlier error"}}
			previous := a.themeImport.Problem
			calls := 0
			a.chooseThemeFile = func(mygo.OpenDialogOptions) ([]string, error) { calls++; return nil, nil }
			tr := ui.NewTester(a.view, 1040, 720)
			tr.SetLocale(locale)
			tr.SetFocused(true)
			tr.Frame()
			msgs := locales.In(locale)
			before := recoveryRecordSnapshot(t, ct)
			if err := clickSettingsAction(tr, msgs.ThemeImportAction()); err != nil {
				t.Fatal(err)
			}
			themeImportCompletion(t, callbacks)()
			tr.Frame()
			if a.themeImport.Problem != previous || !tr.Focused(msgs.ThemeImportAction()) || calls != 1 {
				t.Fatal("cancel lost prior error/focus or retried")
			}
			missing := filepath.Join(t.TempDir(), "removed.json")
			a.chooseThemeFile = func(mygo.OpenDialogOptions) ([]string, error) { calls++; return []string{missing}, nil }
			if err := clickSettingsAction(tr, msgs.ThemeImportAction()); err != nil {
				t.Fatal(err)
			}
			themeImportCompletion(t, callbacks)()
			themeImportCompletion(t, callbacks)()
			tr.Frame()
			if !tr.HasText(msgs.ThemeImportFailed()) || !tr.Focused(msgs.ThemeImportAction()) || a.themeImport.Problem == nil {
				t.Fatal("failed file import has no visible retry/focus")
			}
			source := writeCustomTheme(t, t.TempDir(), "valid.json", customThemeDocument("retry-valid", appearanceLight))
			a.chooseThemeFile = func(mygo.OpenDialogOptions) ([]string, error) { calls++; return []string{source}, nil }
			if err := clickSettingsAction(tr, msgs.ThemeImportAction()); err != nil {
				t.Fatal(err)
			}
			themeImportCompletion(t, callbacks)()
			themeImportCompletion(t, callbacks)()
			tr.Frame()
			if a.themeImport.Problem != nil || !tr.HasText(msgs.ThemeImportSucceeded("Garden")) || calls != 3 || a.appearanceThemeIDs.Light != "paper" || recoveryRecordSnapshot(t, ct) != before || a.submissionInFlight != 0 {
				t.Fatal("explicit import retry failed or changed current work/selection")
			}
		})
	}
}

func TestC8SaveRecoveryCopyEntryKeepsSavedAndUnsavedState(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	focusComposer(t, tr, "Composer for Nagi")
	tr.Type("Saved local work for the recovery copy")
	tr.Frame()
	ct := a.selectedContact()
	if a.store == nil || a.localSaveErr != "" || a.store.state.Contacts[0].Draft != ct.Draft || ct.Draft == "" {
		t.Fatal("ordinary pre-connection draft editing did not produce a healthy saved workbench")
	}
	saved, err := os.ReadFile(a.storePath)
	if err != nil {
		t.Fatal(err)
	}
	openBotProfileFromRail(t, tr, locales.In("en").AvatarContactLabel(ct.Name))
	editor := a.profileEditorFor(ct.ID)
	editor.Draft.Name = "Unsaved profile name excluded from the copy"
	a.connectionForm.Draft.Model = "unsaved-connection-model"
	profileName, modelDraft := editor.Draft.Name, a.connectionForm.Draft
	requests, replies, updates := c8ControlledPicker(t, a)
	folder := t.TempDir()
	before := recoveryRecordSnapshot(t, ct)
	ids, mode, store := a.appearanceThemeIDs, a.appearanceMode, a.store
	defer func() {
		after, readErr := os.ReadFile(a.storePath)
		if readErr != nil || string(after) != string(saved) || recoveryRecordSnapshot(t, ct) != before || editor.Draft.Name != profileName || ct.Name == profileName || a.connectionForm.Draft != modelDraft || a.appearanceThemeIDs != ids || a.appearanceMode != mode || a.store != store {
			t.Error("recovery-copy entry changed saved bytes, IDs, work, unsaved editors or preferences")
		}
		if a.pi != nil || a.connectionSaved != nil || a.connectionEffective != nil || a.connectionSaveInFlight || a.connectionApplying || a.connectionTesting || a.submissionInFlight != 0 || a.shutdownStarted {
			t.Error("recovery-copy entry saved/applied/tested a connection or started execution/shutdown")
		}
	}()
	if err := tr.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("General"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAppearance {
		t.Fatal("existing Settings did not route to General")
	}
	ct.Context.formName, ct.Context.formText = "unsaved context name", "unsaved context text"
	defer func() {
		if ct.Context.formName != "unsaved context name" || ct.Context.formText != "unsaved context text" {
			t.Error("copy changed the unsaved context editor")
		}
	}()
	if err := clickSettingsAction(tr, "Save recovery copy…"); err != nil {
		t.Fatalf("C8-R1: healthy saved workbench has no reachable Save recovery copy action: %v", err)
	}
	options := awaitFolderChoice(t, requests)
	if options.Parent != a.win || !options.Directory {
		t.Fatal("copy did not request its parented folder chooser")
	}
	replies <- folderChoiceReply{paths: []string{folder}}
	awaitFolderChoice(t, updates)()
	awaitFolderChoice(t, updates)()
	tr.Frame()
	if a.dataRecovery.problem != "" || a.dataRecovery.output == "" || string(recoverySavedBytes(t, a.dataRecovery.output)) != string(saved) {
		t.Fatal("GUI recovery copy did not export the captured saved bytes")
	}
	if filepath.Dir(a.dataRecovery.output) != folder || !strings.HasPrefix(filepath.Base(a.dataRecovery.output), "Nagi-workbench-recovery-") || !tr.HasText(locales.In("en").RecoveryCopySaved(a.dataRecovery.output)) {
		t.Fatal("successful copy did not expose the actual chosen location")
	}
}

func c8ControlledPicker(t *testing.T, a *appModel) (<-chan mygo.OpenDialogOptions, chan<- folderChoiceReply, <-chan func()) {
	t.Helper()
	requests := make(chan mygo.OpenDialogOptions, 8)
	replies := make(chan folderChoiceReply, 8)
	updates := make(chan func(), 16)
	pick := func(opts mygo.OpenDialogOptions) ([]string, error) {
		requests <- opts
		reply := <-replies
		return reply.paths, reply.err
	}
	a.chooseFolder, a.chooseRecoveryFile = pick, pick
	a.setUIHook(func(fn func()) { updates <- fn })
	t.Cleanup(func() { close(replies) })
	return requests, replies, updates
}

func c8SavedApp(t *testing.T) *appModel {
	t.Helper()
	isolateConnectionApp(t, t.TempDir(), "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	a.selectedContact().Draft = "Saved workbench 原文"
	a.draftChanged(a.selectedContact())
	if a.store == nil || a.localSaveErr != "" {
		t.Fatal("saved workbench fixture failed")
	}
	a.openAppearance()
	return a
}

func c8UnavailableApp(t *testing.T) *appModel {
	t.Helper()
	isolateConnectionApp(t, t.TempDir(), "")
	if err := os.MkdirAll(filepath.Dir(guiStorePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guiStorePath(), []byte("{damaged original 原文"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := newRuntimeApp()
	if err == nil {
		t.Fatal("corrupt data unexpectedly loaded")
	}
	a := newUnavailableApp(err)
	a.openDataRecovery()
	return a
}

func c8ReachAction(t *testing.T, tr *ui.Tester, action string, width, height int) ui.Rect {
	t.Helper()
	for i := 0; i < 25; i++ {
		if r, ok := tr.Find(action); ok && r.W > 0 && r.H >= 32 && r.Y >= 0 && r.Y+r.H <= float32(height) && r.X >= 0 && r.X+r.W <= float32(width)+1 {
			return r
		}
		tr.Scroll(float32(width/2), float32(height/2), 0, 150)
		tr.Frame()
	}
	r, found := tr.Find(action)
	t.Fatalf("recovery action %q not reachable in %dx%d: bounds=%+v found=%t", action, width, height, r, found)
	return ui.Rect{}
}

func c8RenderEvidence(t *testing.T, tr *ui.Tester, name string, width, height int, controls ...string) {
	t.Helper()
	dir := os.Getenv("NAGI_DATA_RECOVERY_PREVIEW_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	bounds := map[string]ui.Rect{}
	for _, label := range controls {
		bounds[label] = c8ReachAction(t, tr, label, width, height)
	}
	tr.Frame()
	writePreviewPNG(t, filepath.Join(dir, name+".png"), tr.Image())
	data, err := json.MarshalIndent(bounds, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".bounds.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestC8RecoveryCancellationErrorsAndStaleSelection(t *testing.T) {
	for _, restore := range []bool{false, true} {
		for _, change := range []string{"cancel", "error", "back", "tab-return", "contact", "path", "shutdown"} {
			t.Run(fmt.Sprintf("restore-%t/%s", restore, change), func(t *testing.T) {
				a := c8SavedApp(t)
				if restore {
					a = c8UnavailableApp(t)
				}
				path := a.storePath
				before := recoverySavedBytes(t, path)
				s := a.ensureDataRecovery()
				s.notice, s.problem = "earlier", "earlier diagnostic"
				req, reply, updates := c8ControlledPicker(t, a)
				a.chooseDataRecovery(restore)
				awaitFolderChoice(t, req)
				result := folderChoiceReply{paths: []string{t.TempDir()}}
				switch change {
				case "cancel":
					result = folderChoiceReply{}
				case "error":
					result = folderChoiceReply{err: fmt.Errorf("controlled file selection failure")}
				case "back":
					a.closeSettings()
				case "tab-return":
					a.selectSettingsTab(settingsTabConnection)
					a.selectSettingsTab(settingsTabAppearance)
				case "contact":
					a.contacts = append(a.contacts, &contact{ID: "other", Name: "Other", Draft: "new live draft"})
					a.selected = 1
				case "path":
					a.storePath = filepath.Join(t.TempDir(), "different.json")
				case "shutdown":
					a.beginShutdown()
					before = recoverySavedBytes(t, path)
				}
				reply <- result
				awaitFolderChoice(t, updates)()
				if s.choosing || s.inFlight || s.copy != nil || s.target != nil || s.output != "" || string(recoverySavedBytes(t, path)) != string(before) || a.pi != nil {
					t.Fatal("canceled/stale selection wrote or admitted an operation")
				}
				if change == "error" {
					if s.problem != "controlled file selection failure" {
						t.Fatal("picker error was lost")
					}
				} else if s.notice != "earlier" || s.problem != "earlier diagnostic" {
					t.Fatal("cancel/stale selection changed existing feedback")
				}
			})
		}
	}
}

func TestC8RecoveryCopyCollisionAndRetry(t *testing.T) {
	a := c8SavedApp(t)
	req, reply, updates := c8ControlledPicker(t, a)
	folder := t.TempDir()
	a.chooseDataRecovery(false)
	awaitFolderChoice(t, req)
	collision := filepath.Join(folder, a.dataRecovery.filename)
	if err := os.WriteFile(collision, []byte("keep existing output"), 0600); err != nil {
		t.Fatal(err)
	}
	reply <- folderChoiceReply{paths: []string{folder}}
	awaitFolderChoice(t, updates)()
	awaitFolderChoice(t, updates)()
	if a.dataRecovery.notice != "copy-failed" || a.dataRecovery.problem == "" || a.dataRecovery.output != "" || string(recoverySavedBytes(t, collision)) != "keep existing output" {
		t.Fatal("GUI copy overwrote or hid its filename collision")
	}
	otherFolder := t.TempDir()
	a.chooseDataRecovery(false)
	awaitFolderChoice(t, req)
	reply <- folderChoiceReply{paths: []string{otherFolder}}
	awaitFolderChoice(t, updates)()
	awaitFolderChoice(t, updates)()
	if a.dataRecovery.problem != "" || filepath.Dir(a.dataRecovery.output) != otherFolder || string(recoverySavedBytes(t, collision)) != "keep existing output" {
		t.Fatal("explicit retry did not preserve the existing file and use the chosen folder")
	}
}

func TestC8RecoveryRechecksTargetAndDirtyPreview(t *testing.T) {
	for _, change := range []string{"target", "profile", "form", "context", "view", "cancel"} {
		t.Run(change, func(t *testing.T) {
			a := c8UnavailableApp(t)
			req, reply, updates := c8ControlledPicker(t, a)
			source := filepath.Join(t.TempDir(), "compatible.json")
			recoveryWriteState(t, source, recoveryProducedState(t))
			a.chooseDataRecovery(true)
			awaitFolderChoice(t, req)
			reply <- folderChoiceReply{paths: []string{source}}
			awaitFolderChoice(t, updates)()
			awaitFolderChoice(t, updates)()
			if !a.dataRecoveryPreviewCurrent(a.dataRecovery) {
				t.Fatal("real preview did not become confirmable")
			}
			switch change {
			case "target":
				if err := os.WriteFile(a.storePath, []byte("{different original"), 0600); err != nil {
					t.Fatal(err)
				}
			case "profile":
				a.openContactProfile(a.selectedID())
				a.profileEditorFor(a.selectedID()).Draft.Name = "keep unsaved profile"
			case "form":
				a.connectionForm.Draft.Model = "keep unsaved model"
			case "context":
				a.selectedContact().Context.formText = "keep unsaved context"
			case "view":
				a.closeSettings()
				a.openAppearance()
			case "cancel":
				tr := ui.NewTester(a.view, 1040, 720)
				tr.Frame()
				c8ReachAction(t, tr, locales.In("en").RecoveryCancelAction(), 1040, 720)
				if err := tr.Click(locales.In("en").RecoveryCancelAction()); err != nil {
					t.Fatal(err)
				}
			}
			original := recoverySavedBytes(t, a.storePath)
			a.confirmDataRecovery()
			if change == "target" {
				awaitFolderChoice(t, updates)()
				if a.dataRecovery.result.Outcome != storeRecoveryNotInstalled || a.dataRecovery.problem == "" {
					t.Fatal("changed original did not remain not-installed")
				}
			} else if a.dataRecovery.inFlight {
				t.Fatal("dirty/stale preview admitted replacement")
			}
			if string(recoverySavedBytes(t, a.storePath)) != string(original) || a.store != nil || a.storeInitAllowed || a.pi != nil {
				t.Fatal("rejected confirmation changed unavailable original or activated app")
			}
		})
	}
}

func TestC8UnsavedRecoveryGuidanceBeforeQuit(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, size := range [][2]int{{720, 640}, {1040, 720}} {
			for _, state := range []string{"blocked", "installed", "unconfirmed"} {
				t.Run(fmt.Sprintf("%s-%d-%s", locale, size[0], state), func(t *testing.T) {
					a := c8UnavailableApp(t)
					a.windowWidth = size[0]
					req, reply, updates := c8ControlledPicker(t, a)
					msgs := locales.In(locale)
					tr := ui.NewTester(a.view, size[0], size[1])
					tr.SetLocale(locale)
					tr.SetFocused(true)
					tr.SetDark(locale == "zh-Hans")
					tr.Frame()
					tr.Frame()
					var completion func()
					if state != "blocked" {
						if reason := a.dataRecoveryBlocked(true); reason != "" {
							t.Fatalf("initial restore was not quiescent: %s", reason)
						}
						source := filepath.Join(t.TempDir(), "compatible-copy.json")
						recoveryWriteState(t, source, recoveryProducedState(t))
						c8ReachAction(t, tr, msgs.RestorePausedAction(), size[0], size[1])
						if err := tr.Click(msgs.RestorePausedAction()); err != nil {
							t.Fatal(err)
						}
						awaitFolderChoice(t, req)
						reply <- folderChoiceReply{paths: []string{source}}
						awaitFolderChoice(t, updates)()
						awaitFolderChoice(t, updates)()
						tr.Frame()
						tr.Frame()
						if !a.dataRecoveryPreviewCurrent(a.dataRecovery) {
							t.Fatal("compatible preview was not prepared")
						}
						if state == "installed" {
							c8ReachAction(t, tr, msgs.RecoveryConfirmAction(), size[0], size[1])
							if err := tr.Click(msgs.RecoveryConfirmAction()); err != nil {
								t.Fatal(err)
							}
							completion = awaitFolderChoice(t, updates)
						} else {
							// A real typed backend outcome from its existing local, per-call fault boundary.
							s := a.dataRecovery
							result, err := restoreStoreRecoveryCopyWithRename(s.copy, s.target, func(stage, destination string) error {
								if err := os.Rename(stage, destination); err != nil {
									return err
								}
								return os.Remove(destination)
							})
							if err == nil || result.Outcome != storeRecoveryInstallUnconfirmed || result.ArchivePath == "" {
								t.Fatal("controlled installation did not produce a preserved unconfirmed outcome")
							}
							s.result, s.problem, s.copy, s.target = result, a.connectionSafeMessage(err), nil, nil
						}
					}
					c8ReachAction(t, tr, msgs.SettingsBackAccessible(), size[0], size[1])
					if err := tr.Click(msgs.SettingsBackAccessible()); err != nil {
						t.Fatal(err)
					}
					tr.Frame()
					text := "Unsaved text to copy before quitting 未保存文字"
					focusComposer(t, tr, msgs.ComposerLabel(a.selectedContact().Name))
					tr.Type(text)
					tr.Frame()
					ct := a.selectedContact()
					if ct.Draft != text || a.localSaveErr == "" || a.store != nil || a.storeInitAllowed || a.pi != nil {
						t.Fatal("actual unavailable composer did not retain unsaved text with a failed local save")
					}
					if completion != nil {
						completion()
						tr.Frame()
					}
					if ct.Draft != text {
						t.Fatal("restore completion discarded the intervening unsaved draft")
					}
					var original []byte
					if state != "unconfirmed" {
						original = recoverySavedBytes(t, a.storePath)
					}
					if err := tr.Click(msgs.RestorePausedAction()); err != nil {
						t.Fatal(err)
					}
					tr.Frame()
					tr.Frame()
					a.chooseDataRecovery(true)
					select {
					case <-req:
						t.Fatal("unsaved/terminal recovery opened another chooser")
					default:
					}
					if a.dataRecovery.choosing || a.dataRecovery.inFlight || ct.Draft != text || a.store != nil || a.storeInitAllowed || a.pi != nil {
						t.Fatal("unsaved/terminal recovery admitted another operation or lost open-window text")
					}
					if state != "unconfirmed" && string(recoverySavedBytes(t, a.storePath)) != string(original) {
						t.Fatal("blocked recovery changed canonical bytes")
					}
					if state == "unconfirmed" {
						if _, err := os.Stat(a.storePath); !os.IsNotExist(err) {
							t.Fatal("uncertain installation was blindly retried")
						}
					}
					summary, loss := msgs.RecoveryRestoreUnsaved(), "Quitting does not save these edits."
					if state == "installed" {
						summary = msgs.RecoveryInstalled()
					}
					if state == "unconfirmed" {
						summary = msgs.RecoveryUnconfirmed()
					}
					if locale == "zh-Hans" {
						loss = "退出不会保存它们。"
					}
					if state != "blocked" {
						loss = "Before quitting, copy any unsaved text you need somewhere safe; this window cannot save it."
						if locale == "zh-Hans" {
							loss = "退出前请先将需要的未保存文字复制到安全位置，此窗口无法保存它们。"
						}
					}
					controls := []string{msgs.SettingsBackAccessible()}
					if state != "blocked" {
						controls = append(controls, msgs.RecoveryQuitAction())
					}
					// Reach the actual warning paragraph without scrolling the fixed Settings navigation away.
					top := float32(0)
					if size[0] < int(preferenceBreakpoint) {
						nav, ok := tr.Find(msgs.SettingsAdvanced())
						if !ok {
							t.Fatal("narrow Settings navigation missing")
						}
						top = nav.Y + nav.H + 16
					}
					visible := false
					bounds := map[string]ui.Rect{}
					fontSize := typeBody
					if state == "blocked" {
						fontSize = typeMeta
					}
					fullWarningHeight := float32(0)
					for i := 0; i < 25; i++ {
						r, ok := tr.Find(summary)
						if ok && r.W > 0 {
							_, fullWarningHeight = ui.ShapeText(summary, ui.Font{Size: fontSize, LineHeight: 1.5, Lang: locale}, r.W).Size()
						}
						visible = ok && r.H+0.5 >= fullWarningHeight && r.Y >= top && r.Y+r.H <= float32(size[1])
						bounds["warning"] = r
						for _, control := range controls {
							box, found := tr.Find(control)
							bounds[control] = box
							visible = visible && found && box.H >= 32 && box.X >= 0 && box.X+box.W <= float32(size[0])+1 && box.Y >= 0 && box.Y+box.H <= float32(size[1])
						}
						if visible {
							break
						}
						tr.Scroll(float32(size[0]/2), float32(size[1]/2), 0, 100)
						tr.Frame()
					}
					if !visible {
						t.Fatal("warning and return/quit controls are not reachable together in their real viewport")
					}
					if dir := os.Getenv("NAGI_DATA_RECOVERY_PREVIEW_DIR"); dir != "" {
						if err := os.MkdirAll(dir, 0755); err != nil {
							t.Fatal(err)
						}
						name := fmt.Sprintf("unsaved-%s-%d-%s", locale, size[0], state)
						writePreviewPNG(t, filepath.Join(dir, name+".png"), tr.Image())
						data, err := json.MarshalIndent(struct {
							Bounds            map[string]ui.Rect
							FullWarningHeight float32
						}{bounds, fullWarningHeight}, "", "  ")
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, name+".bounds.json"), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
					if !tr.HasText(summary) || !strings.Contains(summary, loss) {
						t.Fatalf("C8-UNSAVED-COPY-1: %s does not warn that quitting loses this retained unsaved text", state)
					}
					if err := tr.Click(msgs.SettingsBackAccessible()); err != nil {
						t.Fatal(err)
					}
					tr.Frame()
					if a.settingsOpen || ct.Draft != text {
						t.Fatal("return action lost open-window text before the user could copy it")
					}
					quit := 0
					a.requestQuit = func() { quit++ }
					a.beginShutdown()
					if quit != 1 || !a.quitReady || ct.Draft != text || a.localSaveErr == "" {
						t.Fatal("ordinary unsavable fallback shutdown behavior unexpectedly changed")
					}
					if state != "unconfirmed" && string(recoverySavedBytes(t, a.storePath)) != string(original) {
						t.Fatal("fallback shutdown overwrote stored data with the unsaved draft")
					}
					// Reconstruct the ordinary startup without launching a Runtime or GUI process.
					reopened, err := newRuntimeApp()
					if err != nil {
						reopened = newUnavailableApp(err)
					}
					if reopened.selectedContact().Draft == text || reopened.pi != nil {
						t.Fatal("fixture unexpectedly durably retained unsaved text or launched execution")
					}
				})
			}
		}
	}
}

func TestTaskContextPickerAndRecoveryMutualAdmission(t *testing.T) {
	a, ct, tc, tr, callbacks := contextFileChooserTestApp(t)
	a.storePath = a.store.path
	before := recoverySavedBytes(t, a.store.path)
	release := make(chan struct{})
	a.chooseContextFile = func(mygo.OpenDialogOptions) ([]string, error) { <-release; return []string{"/stale-selected"}, nil }
	if err := tr.Click("Choose UTF-8 file…"); err != nil {
		t.Fatal(err)
	}
	a.openAppearance()
	a.chooseFolder = func(mygo.OpenDialogOptions) ([]string, error) {
		t.Error("recovery opened a competing native picker")
		return nil, nil
	}
	if a.dataRecoveryBlocked(false) != "busy" {
		t.Fatal("context picker did not reserve against recovery")
	}
	a.chooseDataRecovery(false)
	if a.ensureDataRecovery().choosing || a.ensureDataRecovery().inFlight {
		t.Fatal("recovery admitted while context picker pending")
	}
	close(release)
	themeImportCompletion(t, callbacks)()
	if a.contextFilePickers != 0 || tc.formPath != "" {
		t.Fatal("stale context choice changed hidden form or leaked reservation")
	}
	a.closeSettings()
	a.openConversationTaskContext()
	a.dataRecovery = &dataRecoveryState{choosing: true}
	a.chooseContextFile = func(mygo.OpenDialogOptions) ([]string, error) {
		t.Error("context picker bypassed recovery reservation")
		return nil, nil
	}
	a.chooseTaskContextFile(ct, tc)
	if a.contextFilePickers != 0 || tc.fileChoosing || string(recoverySavedBytes(t, a.store.path)) != string(before) {
		t.Fatal("context choice bypassed actual recovery admission")
	}
}

func TestSaveFailureTypingKeepsComposerReturnFocus(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			a.windowWidth, a.uiLocale = 1040, locale
			a.appearanceThemes = loadCustomThemes(t.TempDir())
			ct.Draft = "before "
			recoveryTestStore(t, a)
			goodPath := a.store.path
			goodBytes, err := os.ReadFile(goodPath)
			if err != nil {
				t.Fatal(err)
			}
			if a.localSaveErr != "" || a.pi != nil {
				t.Fatal("valid isolated-store setup failed")
			}
			ownerID, sessionID := ct.ID, ct.SessionID
			a.focusComposer = true
			tr := ui.NewTester(a.view, 1040, 720)
			tr.SetLocale(locale)
			tr.Frame()
			msgs := locales.In(locale)
			if !tr.Focused(msgs.ComposerLabel(ct.Name)) {
				t.Fatal("initial composer did not receive focus")
			}
			capture := func(phase string) {
				t.Logf("phase=%s composerFocused=%t settingsOpen=%t returnSource=%q owner=%q draft=%q localSaveErr=%q", phase, tr.Focused(msgs.ComposerLabel(ct.Name)), a.settingsOpen, a.settingsReturnFocus, a.selectedContact().ID, ct.Draft, a.localSaveErr)
				if dir := os.Getenv("NAGI_SAVE_ERROR_PREVIEW_DIR"); dir != "" {
					if err := os.MkdirAll(dir, 0755); err != nil {
						t.Fatal(err)
					}
					writePreviewPNG(t, filepath.Join(dir, "composer-"+locale+"-"+phase+".png"), tr.Image())
				}
			}
			capture("focused-before-failure")
			blockedParent := filepath.Join(t.TempDir(), "regular-file-parent")
			if err := os.WriteFile(blockedParent, []byte("This is a file, not a directory."), 0600); err != nil {
				t.Fatal(err)
			}
			a.store.path = filepath.Join(blockedParent, "contacts.json")
			tr.Type("settings")
			capture("immediately-after-type")
			if ct.Draft != "before settings" {
				t.Fatalf("actual typing changed draft unexpectedly: %q", ct.Draft)
			}
			if a.localSaveErr == "" || !tr.HasText(msgs.RecoveryLocalSaveFailed()) {
				t.Fatal("typing did not expose actual failed filesystem autosave")
			}
			if got, err := os.ReadFile(goodPath); err != nil || string(got) != string(goodBytes) {
				t.Fatal("failed typing save changed original good disk bytes")
			}
			if a.selectedContact() != ct || ct.ID != ownerID || ct.SessionID != sessionID {
				t.Fatal("failed typing save changed selected owner or session")
			}
			if !tr.Focused(msgs.ComposerLabel(ct.Name)) {
				t.Fatal("AC-SAVE2: actual failed autosave/error reflow lost composer focus immediately after Type, before Cmd+,")
			}
			tr.Key(ui.Cmd, ui.KeyComma)
			tr.Frame()
			capture("settings-opened")
			if !a.settingsOpen || a.settingsReturnFocus != settingsSourceComposer {
				t.Fatalf("AC-SAVE2: Cmd+, did not capture composer source: open=%t source=%q", a.settingsOpen, a.settingsReturnFocus)
			}
			if err := tr.Click(msgs.SettingsBackAccessible()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			capture("returned-from-settings")
			if !tr.Focused(msgs.ComposerLabel(ct.Name)) {
				t.Fatal("AC-SAVE2: Back did not restore composer focus after the genuine save error")
			}
			tr.Type(" again")
			capture("typed-after-back")
			if ct.Draft != "before settings again" || a.selectedContact() != ct || ct.ID != ownerID || ct.SessionID != sessionID || a.pi != nil || a.localSaveErr == "" {
				t.Fatal("AC-SAVE2: typing after Back lost exact draft, owner or failed-save state")
			}
			if got, err := os.ReadFile(goodPath); err != nil || string(got) != string(goodBytes) {
				t.Fatal("failed follow-up autosave changed original good disk bytes")
			}
		})
	}
}

func TestSettingsQoderIntegratedLayout(t *testing.T) {
	for _, size := range [][2]int{{720, 640}, {1040, 720}, {1380, 900}} {
		width, height := size[0], size[1]
		for _, locale := range []string{"en", "zh-Hans"} {
			for _, dark := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s-%dx%d-dark-%t", locale, width, height, dark), func(t *testing.T) {
					a, ct := workspaceTestApp(t)
					recoveryTestStore(t, a)
					a.windowWidth = width
					config := testConnectionConfig(t)
					a.connectionSaved, a.connectionEffective = &config, &config
					a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
					ct.Draft = "Keep the actual draft while inspecting settings layout"
					a.openSettingsTab(settingsTabConnection, settingsSourceRail)
					var viewport ui.Rect
					tr := ui.NewTester(func(c *ui.Context) {
						ui.Box(c).Fill().DrawOver(func(_ *ui.Painter, _ ui.Rect) { viewport = a.settingsScroll.Bounds(c) }).Children(func() { a.view(c) })
					}, width, height)
					tr.SetLocale(locale)
					tr.SetDark(dark)
					tr.Frame()
					if dir := os.Getenv("NAGI_PREVIEW_DIR"); dir != "" {
						writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("qoder-layout-%s-%dx%d-dark-%t-top.png", locale, width, height, dark)), tr.Image())
					}
					msgs := locales.In(locale)
					// The scrolling viewport belongs to the main pane, not its readable column.
					if viewport.W <= 0 || float32(width)-(viewport.X+viewport.W) > 9 {
						t.Errorf("scroll viewport stops before main-pane edge: viewport=%+v window=%d", viewport, width)
					}
					general, ok := tr.Find(msgs.SettingsGeneral())
					if !ok {
						t.Fatal("General navigation row missing")
					}
					connection, ok := tr.Find(msgs.SettingsConnection())
					if !ok {
						t.Fatal("Connection navigation row missing")
					}
					if general.X != connection.X || general.W != connection.W || viewport.X-(connection.X+connection.W) > 24 {
						t.Errorf("navigation rows do not fill the sidebar group: general=%+v connection=%+v viewport=%+v", general, connection, viewport)
					}
					if ct.Draft != "Keep the actual draft while inspecting settings layout" || ct.pendingSend != nil {
						t.Fatal("rendering settings changed the conversation draft")
					}
					a.selectSettingsTab(settingsTabAppearance)
					tr.Frame()
					if dir := os.Getenv("NAGI_PREVIEW_DIR"); dir != "" {
						writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("qoder-layout-%s-%dx%d-dark-%t-general.png", locale, width, height, dark)), tr.Image())
					}
				})
			}
		}
	}
}
