package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func connectionSettingsTester(s *connectionSettings, actions connectionSettingsActions, width, height int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		theme := resolveWorkbenchTheme(c.Theme().Dark).Theme
		c.SetTheme(theme)
		ui.Scroll(c).Fill().Padding(16).Background(theme.Surface).Children(func() { s.View(c, theme, actions) })
	}, width, height)
}

func TestConnectionSettingsRequireExplicitSaveAndTest(t *testing.T) {
	config := testConnectionConfig(t)
	s := newConnectionSettings(&config, "")
	s.Draft.Model = "changed-model"
	saved, tested, applied := 0, 0, 0
	tr := connectionSettingsTester(s, connectionSettingsActions{
		Save: func(candidate connectionConfig) {
			saved++
			if candidate.Model != "changed-model" {
				t.Fatal("save lost the edited model")
			}
			s.Error = "Cannot save configuration. Your changes are still here."
		},
		Test: func(candidate connectionConfig) {
			tested++
			if candidate.Model != "changed-model" {
				t.Fatal("test did not use the current edited draft")
			}
		}, Apply: func() { applied++ },
	}, 580, 1600)
	tr.Frame()
	if saved != 0 || tested != 0 || applied != 0 {
		t.Fatal("rendering performed an action")
	}
	if err := tr.Click("Save"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if saved != 1 || tested != 0 || s.Draft.Model != "changed-model" || s.Error == "" {
		t.Fatal("save failure must retain editing and must not test or apply")
	}
	s.ReloadBlocked = "Wait for all active work to finish."
	tr.Frame()
	if err := tr.Click("Apply saved"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if applied != 0 {
		t.Fatal("blocked apply restarted the connection")
	}
	if err := tr.Click("Test"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if tested != 1 || saved != 1 || applied != 0 {
		t.Fatal("explicit connection test must not save or apply configuration")
	}
}

func TestConnectionSettingsValidationKeepsDraftAndFocus(t *testing.T) {
	config := testConnectionConfig(t)
	s := newConnectionSettings(&config, "")
	s.Draft.ContextTokens = "not-a-number"
	s.Draft.APIKey = ""
	calls := 0
	tr := connectionSettingsTester(s, connectionSettingsActions{Save: func(connectionConfig) { calls++ }}, 580, 1600)
	tr.SetFocused(true)
	tr.Frame()
	if err := tr.Click("Save"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Frame()
	if calls != 0 || s.Draft.ContextTokens != "not-a-number" || s.Errors["APIKey"] == "" || s.Errors["ContextTokens"] == "" || !s.LimitsOpen {
		t.Fatal("invalid form must retain every edit, open capacity for its error, and report field errors without saving")
	}
	tr.Type("replacement-key")
	tr.Frame()
	if s.Draft.APIKey != "replacement-key" {
		t.Fatal("validation did not focus the first invalid field")
	}
}

func TestConnectionProtocolSegmentedKeepsDraftAndBusyGate(t *testing.T) {
	msgs := locales.In("en")
	chatCompletions, responses := msgs.ProtocolChatCompletionsLabel(), msgs.ProtocolResponsesLabel()
	config := testConnectionConfig(t)
	config.API = connectionAPIChatCompletions
	s := newConnectionSettings(&config, "")
	tr := connectionSettingsTester(s, connectionSettingsActions{}, 580, 1600)
	tr.Frame()
	if !tr.HasText(msgs.ProtocolLabel()) || !tr.HasText(chatCompletions) || !tr.HasText(responses) {
		t.Fatalf("protocol segments were not independently visible: %v", tr.Texts())
	}
	if err := tr.Click(responses); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if s.Draft.API != connectionAPIResponses {
		t.Fatalf("protocol draft = %q, want Responses", s.Draft.API)
	}
	if err := tr.Click(chatCompletions); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if s.Draft.API != connectionAPIChatCompletions || s.Initial.API != config.API {
		t.Fatal("switching back did not restore the draft protocol or changed the saved selection")
	}
	s.Busy = true
	tr.Frame()
	if err := tr.Click(responses); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if s.Draft.API != connectionAPIChatCompletions {
		t.Fatalf("busy protocol changed draft to %q", s.Draft.API)
	}
	s.Busy = false
	s.Draft.API = "unsupported-protocol"
	saves := 0
	tr = connectionSettingsTester(s, connectionSettingsActions{Save: func(connectionConfig) { saves++ }}, 580, 1600)
	tr.SetFocused(true)
	tr.Frame()
	if err := tr.Click(msgs.ConnectionSaveConfiguration()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if saves != 0 || s.Errors["API"] == "" || !tr.Focused(chatCompletions) || !tr.HasText(msgs.ValidationAPI()) {
		t.Fatalf("invalid protocol did not block Save or focus first segment: errors=%v focused=%v", s.Errors, tr.Focused(chatCompletions))
	}
	if err := tr.Click(chatCompletions); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if s.Draft.API != connectionAPIChatCompletions || s.Errors["API"] != "" {
		t.Fatalf("clicking Chat completions did not repair invalid protocol: draft=%q errors=%v", s.Draft.API, s.Errors)
	}
	beforeBox, ok := tr.Find(responses)
	if !ok {
		t.Fatal("Responses segment missing")
	}
	tr.Key(0, ui.KeyRight)
	tr.Frame()
	afterBox, ok := tr.Find(responses)
	if !ok || s.Draft.API != connectionAPIResponses || !tr.Focused(responses) || beforeBox != afterBox {
		t.Fatalf("keyboard Right did not focus Responses without geometry shift: api=%q before=%+v after=%+v", s.Draft.API, beforeBox, afterBox)
	}
}

func TestPreviewConnectionSettings(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR for connection form previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		for _, width := range []int{340, 600} {
			s := newConnectionSettings(nil, "/Users/you/Workspace")
			tr := connectionSettingsTester(s, connectionSettingsActions{}, width, 640)
			tr.SetDark(dark)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("connection-form-%d-dark-%t.png", width, dark)), tr.Image())
			tr.Scroll(100, 500, 0, 1000)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("connection-actions-%d-dark-%t.png", width, dark)), tr.Image())
		}
	}
	isolateConnectionApp(t, t.TempDir(), "")
	for _, dark := range []bool{false, true} {
		for _, size := range [][2]int{{1040, 720}, {720, 640}} {
			a, err := newRuntimeApp()
			if err != nil {
				t.Fatal(err)
			}
			a.windowWidth = size[0]
			tr := ui.NewTester(a.view, size[0], size[1])
			tr.SetDark(dark)
			tr.Frame()
			name := fmt.Sprintf("connection-app-%dx%d-dark-%t", size[0], size[1], dark)
			writePreviewPNG(t, filepath.Join(dir, name+"-form.png"), tr.Image())
			tr.Scroll(float32(size[0]/2), float32(size[1]/2), 0, 1200)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, name+"-actions.png"), tr.Image())
		}
	}
}

func TestConnectionSettingsSaveAndApplyPrimaryAndRetryApply(t *testing.T) {
	config := testConnectionConfig(t)
	s := newConnectionSettings(&config, config.WorkingDirectory)
	saveOnly, saveApply, applied := 0, 0, 0
	tr := connectionSettingsTester(s, connectionSettingsActions{
		Save:         func(connectionConfig) { saveOnly++ },
		SaveAndApply: func(connectionConfig) { saveApply++ },
		Apply:        func() { applied++ },
	}, 580, 1600)
	tr.Frame()
	if r, ok := tr.Find("Save and apply"); !ok || r.H < 32 {
		t.Fatalf("save-and-apply target = %+v/%v", r, ok)
	}
	if err := tr.Click("Save and apply"); err != nil {
		t.Fatal(err)
	}
	if saveApply != 1 || saveOnly != 0 || applied != 0 {
		t.Fatalf("save and apply action = saveOnly:%d saveApply:%d apply:%d", saveOnly, saveApply, applied)
	}
	s.HasSaved = true
	s.SavedPending = true
	tr.Frame()
	if r, ok := tr.Find("Apply saved"); !ok || r.H < 32 {
		t.Fatalf("retry-apply target = %+v/%v", r, ok)
	}
	if err := tr.Click("Apply saved"); err != nil {
		t.Fatal(err)
	}
	if applied != 1 || saveOnly != 0 || saveApply != 1 {
		t.Fatalf("retry apply action = saveOnly:%d saveApply:%d apply:%d", saveOnly, saveApply, applied)
	}
}

func TestConnectionSettingsSaveFailureUsesLocalizedDetail(t *testing.T) {
	s := newConnectionSettings(nil, "/tmp")
	s.ErrorDetail = "permission denied"
	tr := connectionSettingsTester(s, connectionSettingsActions{}, 580, 1000)
	for _, locale := range []string{"en", "zh-Hans"} {
		tr.SetLocale(locale)
		tr.Frame()
		msgs := locales.In(locale)
		if !tr.HasText(msgs.RecoveryConnectionSaveFailed()) {
			t.Fatalf("localized save summary missing: %v", tr.Texts())
		}
		if !s.RecoveryDetailsOpen {
			if tr.HasText("permission denied") {
				t.Fatal("detail shown before disclosure")
			}
			if err := tr.Click(msgs.DetailsAction()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
		}
		if !tr.HasText("permission denied") || s.ErrorDetail != "permission denied" {
			t.Fatal("original detail lost or rewritten")
		}
	}
}

func TestConnectionFolderControlRequiresExplicitSaveAndApply(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			msgs := locales.In(locale)
			config := testConnectionConfig(t)
			chosen := t.TempDir()
			form := newConnectionSettings(&config, config.WorkingDirectory)
			saves, applies, tests := 0, 0, 0
			tr := connectionSettingsTester(form, connectionSettingsActions{
				ChooseFolder: func() { form.Draft.WorkingDirectory = chosen },
				Save: func(candidate connectionConfig) {
					saves++
					if candidate.WorkingDirectory != chosen {
						t.Fatal("Save did not consume the chosen draft")
					}
				},
				Apply: func() { applies++ }, Test: func(connectionConfig) { tests++ },
			}, 580, 1800)
			tr.SetLocale(locale)
			tr.Frame()
			if err := tr.Click(msgs.WorkFolderChoose()); err != nil {
				t.Fatal(err)
			}
			if saves != 0 || applies != 0 || tests != 0 || form.Draft.WorkingDirectory != chosen {
				t.Fatal("choosing a folder performed a confirmation action")
			}
			tr.Frame()
			if err := tr.Click(msgs.ConnectionSaveConfiguration()); err != nil {
				t.Fatal(err)
			}
			if saves != 1 || applies != 0 || tests != 0 {
				t.Fatal("Save unexpectedly applied or tested the chosen folder")
			}
		})
	}
}

func TestFirstUseWorkFolderManualEntryAndReset(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			form := newConnectionSettings(nil, "")
			fixture := testConnectionConfig(t)
			form.Draft.Endpoint, form.Draft.APIKey, form.Draft.Model = fixture.Endpoint, fixture.APIKey, fixture.Model
			var saved connectionConfig
			saves := 0
			tr := connectionSettingsTester(form, connectionSettingsActions{Save: func(config connectionConfig) {
				saved = config
				saves++
			}}, 580, 1800)
			tr.SetLocale(locale)
			tr.SetFocused(true)
			tr.Frame()
			msgs := locales.In(locale)
			assertConnectionTooltip(t, tr, msgs.WorkFolderChoose(), msgs.WorkFolderRequiredHint())
			if err := tr.Click(msgs.ConnectionFolderEnterPath()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			// The caption shares this label; reach the input through the keyboard focus order.
			for i := 0; i < 12 && !tr.Focused(msgs.ConnectionFieldWorkingDirectory()); i++ {
				tr.Key(0, ui.KeyTab)
				tr.Frame()
			}
			if !tr.Focused(msgs.ConnectionFieldWorkingDirectory()) {
				t.Fatal("work-folder input is not keyboard reachable")
			}
			tr.Type(fixture.WorkingDirectory)
			tr.Frame()
			if err := tr.Click(msgs.ConnectionSaveConfiguration()); err != nil {
				t.Fatal(err)
			}
			if saved.WorkingDirectory != fixture.WorkingDirectory || saves != 1 {
				t.Fatal("explicit text selection could not be saved")
			}
			form.ResetDraft()
			tr.Frame()
			if err := tr.Click(msgs.ConnectionSaveConfiguration()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if form.Draft.WorkingDirectory != "" || form.Errors["WorkingDirectory"] == "" || !tr.HasText(msgs.ValidationWorkspaceAbsolutePath()) || saves != 1 {
				t.Fatalf("reset did not require a new folder choice: path=%q saves=%d errors=%v", form.Draft.WorkingDirectory, saves, form.Errors)
			}
		})
	}
}

func assertConnectionTooltip(t *testing.T, tr *ui.Tester, action, description string) {
	t.Helper()
	if tr.HasText(description) {
		t.Fatalf("tooltip for %q was expanded before interaction", action)
	}
	box, found := tr.Find(action)
	if !found || box.W <= 0 || box.H <= 0 {
		t.Fatalf("tooltip target %q was not available: %+v", action, box)
	}
	tr.Move(box.X+box.W/2, box.Y+box.H/2)
	tr.Advance(time.Second)
	if !tr.HasText(description) {
		t.Fatalf("tooltip for %q did not show %q", action, description)
	}
	tr.Move(0, 0)
	tr.Frame()
	for i := 0; i < 24 && !tr.Focused(action); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused(action) || !tr.HasText(description) {
		t.Fatalf("keyboard focus did not show the tooltip for %q", action)
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if !tr.Focused(action) || tr.HasText(description) {
		t.Fatalf("Escape did not hide the tooltip and retain focus on %q", action)
	}
}

func TestRecoveryPresentationExplicitTestUsesDraftAndDisclosesFailure(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, failure := range []string{"start runtime component: access denied", "test helper shutdown: cleanup failed after a completed response"} {
			t.Run(locale+"/"+failure, func(t *testing.T) {
				a, ct, _ := disconnectedRecoveryTestApp(t, "exit")
				recoveryTestStore(t, a)
				saved := testConnectionConfig(t)
				saved.Model = "saved-model"
				old := saved
				old.Model = "old-applied-model"
				a.connectionSaved, a.connectionEffective = &saved, &old
				s := newConnectionSettings(&saved, saved.WorkingDirectory)
				a.connectionForm = s
				s.SavedPending = a.connectionSavedPending()
				current := saved
				current.Model = "edited-model"
				current.APIKey = "controlled-component-secret"
				current.Endpoint = "https://example.invalid/current"
				current.ContextTokens = 8192
				current.OutputTokens = 1024
				current.MaxActiveTasks = 2
				s.Draft = newConnectionSettings(&current, current.WorkingDirectory).Draft
				initial := s.Initial
				before := recoveryRecordSnapshot(t, ct)
				saves, applies, saveApplies := 0, 0, 0
				tested := []connectionConfig{}
				raw := ""
				actions := a.connectionActions()
				actions.Save = func(connectionConfig) { saves++ }
				actions.Apply = func() { applies++ }
				actions.SaveAndApply = func(connectionConfig) { saveApplies++ }
				actions.Test = func(candidate connectionConfig) {
					tested = append(tested, candidate)
					raw = failure + "; API_KEY=" + candidate.APIKey + "; " + strings.Repeat("original diagnostic ", 40)
					s.Error, s.ErrorDetail = connErrorTestFailed, raw
				}
				tr := connectionSettingsTester(s, actions, 600, 2100)
				tr.SetLocale(locale)
				tr.SetFocused(true)
				tr.Frame()
				msgs := locales.In(locale)
				assertConnectionTooltip(t, tr, msgs.ConnectionTestAction(), msgs.ConnectionTestHint())
				if err := tr.Click(msgs.ConnectionTestAction()); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if len(tested) != 1 || tested[0] != current || !tr.HasText(msgs.RecoveryConnectionTestFailed()) || tr.HasText(msgs.RecoveryModelRequestFailed()) || tr.HasText(raw) {
					t.Fatalf("Test procedure was misrepresented or did not use the complete draft: %v", tr.Texts())
				}
				if err := tr.Click(msgs.DetailsAction()); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if err := tr.Click(msgs.CopyErrorDetailsLabel()); err != nil {
					t.Fatal(err)
				}
				copied := tr.Clipboard()
				if strings.Contains(copied, current.APIKey) || !strings.Contains(copied, "[redacted]") || len([]rune(copied)) > 501 || s.ErrorDetail != raw {
					t.Fatal("Test diagnostic was lost, rewritten, unbounded or leaked a credential")
				}
				tr.SetLocale("zh-Hans")
				tr.Frame()
				// Navigate the app-owned form's actual Settings Back route without invoking its hard-wired Test callback.
				a.openSettingsTab(settingsTabConnection, settingsSourceRail)
				nav := ui.NewTester(a.view, 1040, 720)
				nav.Frame()
				if err := nav.Click(locales.In("en").SettingsBackAccessible()); err != nil {
					t.Fatal(err)
				}
				nav.Frame()
				if a.settingsOpen || a.connectionForm != s {
					t.Fatal("Back replaced the app-owned form or did not return")
				}
				if len(tested) != 1 || saves != 0 || applies != 0 || saveApplies != 0 || recoveryRecordSnapshot(t, ct) != before || *a.connectionSaved != saved || *a.connectionEffective != old || !a.connectionSavedPending() || s.Initial != initial || s.ErrorDetail != raw {
					t.Fatal("disclosure/language/navigation changed history/saved state or triggered Test/Save/Apply")
				}
				tr.SetLocale(locale)
				s.FocusField = "Model"
				tr.Frame()
				tr.Key(ui.Cmd, ui.KeyA)
				tr.Type("edited-after-disclosure")
				tr.Frame()
				current.Model = "edited-after-disclosure"
				if err := tr.Click(msgs.ConnectionTestAction()); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if len(tested) != 2 || tested[1] != current || s.RecoveryDetailsOpen || s.Draft.Model != current.Model || s.Initial != initial || !s.HasSaved || !s.SavedPending || recoveryRecordSnapshot(t, ct) != before || saves != 0 || applies != 0 || saveApplies != 0 || a.submissionInFlight != 0 {
					t.Fatal("second explicit Test did not consume the edited complete form once or preserve app/history state")
				}
			})
		}
	}
}

func TestConnectionSettingsEditCollapsePreservesCurrentDraftAndActionScopes(t *testing.T) {
	saved := testConnectionConfig(t)
	s := newConnectionSettings(&saved, saved.WorkingDirectory)
	s.SavedPending = true
	var tested, written connectionConfig
	tests, saves, applies := 0, 0, 0
	tr := connectionSettingsTester(s, connectionSettingsActions{
		Test: func(config connectionConfig) { tested = config; tests++ },
		Save: func(config connectionConfig) {
			written = config
			saves++
			s.Error, s.ErrorDetail = "permission denied", "original save detail"
		},
		Apply: func() { applies++ },
	}, 600, 2000)
	tr.SetFocused(true)
	tr.Frame()
	if err := tr.Click("Edit API endpoint"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("API endpoint") {
		t.Fatal("Edit did not focus the original endpoint input")
	}
	tr.Key(ui.Cmd, ui.KeyA)
	tr.Type("https://current.example.invalid/v1")
	tr.Frame()
	current := saved
	current.Endpoint = "https://current.example.invalid/v1"
	if err := tr.Click("Close API endpoint editor"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if s.Draft.Endpoint != current.Endpoint || !s.Dirty() || !strings.Contains(strings.Join(tr.Texts(), "\n"), current.Endpoint) || !tr.Focused("Edit API endpoint") {
		t.Fatal("collapse discarded or hid the current draft, or lost the Edit return focus")
	}
	if tests+saves+applies != 0 || s.Initial.Endpoint != saved.Endpoint {
		t.Fatal("editing performed a confirmation action or advanced the saved baseline")
	}
	if err := tr.Click("Test"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if tested != current || tests != 1 || saves+applies != 0 {
		t.Fatal("Test did not consume the complete collapsed draft exactly once")
	}
	if err := tr.Click("Save"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if written != current || saves != 1 || s.ErrorDetail != "original save detail" || !s.Dirty() {
		t.Fatal("failed Save lost the draft or original error")
	}
	if err := tr.Click("Apply saved"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if applies != 1 || tests != 1 || saves != 1 || s.Initial.Endpoint != saved.Endpoint || s.Draft.Endpoint != current.Endpoint {
		t.Fatal("Apply changed the current form or performed another action")
	}
	if err := tr.Click("Edit API endpoint"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Type("/again")
	tr.Frame()
	if s.Draft.Endpoint != current.Endpoint+"/again" || s.ErrorDetail != "original save detail" {
		t.Fatal("reopening did not restore the original draft input and feedback")
	}
}

func TestConnectionSettingsValidationRevealsCollapsedDraft(t *testing.T) {
	config := testConnectionConfig(t)
	s := newConnectionSettings(&config, config.WorkingDirectory)
	calls := 0
	tr := connectionSettingsTester(s, connectionSettingsActions{Save: func(connectionConfig) { calls++ }}, 600, 2000)
	tr.SetFocused(true)
	tr.Frame()
	if err := tr.Click("Edit API key"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Key(ui.Cmd, ui.KeyA)
	tr.Key(0, ui.KeyBackspace)
	tr.Frame()
	if err := tr.Click("Close API key editor"); err != nil {
		t.Fatal(err)
	}
	s.Draft.ContextTokens = "9007199254740993suffix"
	s.Draft.WorkingDirectory = "relative-path"
	tr.Frame()
	if err := tr.Click("Save"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if calls != 0 || s.Draft.APIKey != "" || s.Draft.ContextTokens != "9007199254740993suffix" || s.Errors["APIKey"] == "" || s.Errors["ContextTokens"] != validationNumeric || !s.LimitsOpen || !tr.Focused("API key") {
		t.Fatal("validation did not preserve raw values, reveal capacity and focus the hidden first invalid field")
	}
	tr.Type("replacement-key")
	tr.Frame()
	if err := tr.Click("Close API key editor"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if s.Errors["APIKey"] == "" || !tr.HasText(locales.In("en").ValidationRequired()) {
		t.Fatal("explicit collapse erased the pending field error")
	}
	if err := tr.Click(locales.In("en").SettingsHideRuntimeDetails()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if s.LimitsOpen || s.Errors["ContextTokens"] != validationNumeric || !tr.HasText(locales.In("en").ValidationNumeric()) {
		t.Fatal("collapsing capacity hid the pending token error")
	}
	s.Draft.ContextTokens = "131072"
	if err := tr.Click("Save"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused(locales.In("en").ConnectionFieldWorkingDirectory()) || s.Draft.WorkingDirectory != "relative-path" || calls != 0 {
		t.Fatal("validation did not reveal and focus the manual folder input")
	}
	if err := tr.Click(locales.In("en").ConnectionFolderHidePath()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if s.EditOpen["WorkingDirectory"] || s.Errors["WorkingDirectory"] == "" || !tr.HasText(locales.In("en").ValidationWorkspaceAbsolutePath()) {
		t.Fatal("collapsing working directory hid the pending folder error")
	}
}

func TestConnectionSettingsCapacityPreservesExactValues(t *testing.T) {
	msgs := locales.In("en")
	t.Run("segments", func(t *testing.T) {
		config := testConnectionConfig(t)
		s := newConnectionSettings(&config, config.WorkingDirectory)
		var saved connectionConfig
		saves := 0
		tr := connectionSettingsTester(s, connectionSettingsActions{Save: func(candidate connectionConfig) {
			saved = candidate
			saves++
		}}, 600, 2000)
		tr.Frame()
		if _, found := tr.Find(msgs.ConnectionFieldMaxActiveTasks()); found {
			t.Fatal("task count controls were visible before expanding capacity")
		}
		if err := tr.Click(msgs.SettingsShowRuntimeDetails()); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		s.Errors = map[string]string{"MaxActiveTasks": validationNumeric}
		for _, count := range []string{"4", "3", "2", "1"} {
			if err := tr.Click(count); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if s.Draft.Slots != count || saves != 0 || s.Errors["MaxActiveTasks"] != "" {
				t.Fatalf("selecting task count %s changed saved state or kept a validation error: %q %v", count, s.Draft.Slots, s.Errors)
			}
		}
		s.Busy = true
		tr.Frame()
		if err := tr.Click("4"); err != nil {
			t.Fatal(err)
		}
		if s.Draft.Slots != "1" {
			t.Fatal("busy task segments changed the draft")
		}
		s.Busy = false
		tr.Frame()
		if err := tr.Click(msgs.SettingsHideRuntimeDetails()); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if s.LimitsOpen || !tr.HasText(msgs.ConnectionCapacitySummary("64K", "8K", "1")) {
			t.Fatalf("collapsed capacity did not retain the draft count: %v", tr.Texts())
		}
		if err := tr.Click(msgs.ConnectionSaveConfiguration()); err != nil {
			t.Fatal(err)
		}
		config.MaxActiveTasks = 1
		if saves != 1 || saved != config {
			t.Fatalf("explicit Save did not consume the selected draft once: %+v", saved)
		}
	})
	t.Run("token preset and custom", func(t *testing.T) {
		config := testConnectionConfig(t)
		config.ContextTokens = 131072
		s := newConnectionSettings(&config, config.WorkingDirectory)
		path := filepath.Join(t.TempDir(), "connection.json")
		tr := connectionSettingsTester(s, connectionSettingsActions{Save: func(candidate connectionConfig) {
			if err := saveConnectionConfig(path, candidate); err != nil {
				t.Fatal(err)
			}
		}}, 600, 1600)
		tr.SetFocused(true)
		tr.Frame()
		for _, label := range []string{msgs.SettingsShowRuntimeDetails(), msgs.ConnectionTokenPresetDefault("128K / 8K"), "256K / 16K"} {
			if err := tr.Click(label); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
		}
		if s.Draft.ContextTokens != "262144" || s.Draft.OutputTokens != "16384" || !tr.HasText(msgs.ConnectionCapacitySummary("256K", "16K", "2")) {
			t.Fatalf("token preset did not update both draft limits: %q / %q", s.Draft.ContextTokens, s.Draft.OutputTokens)
		}
		for _, label := range []string{"256K / 16K", msgs.ConnectionTokenPresetCustom()} {
			if err := tr.Click(label); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
		}
		for _, field := range []struct{ label, value string }{
			{msgs.ConnectionFieldContextTokens(), "9007199254740991"},
			{msgs.ConnectionFieldOutputTokens(), "16385"},
		} {
			for i := 0; i < 24 && !tr.Focused(field.label); i++ {
				tr.Key(0, ui.KeyTab)
				tr.Frame()
			}
			if active, _ := tr.TextInputActive(); !tr.Focused(field.label) || !active {
				t.Fatalf("custom token field %q is not keyboard editable", field.label)
			}
			tr.Key(ui.Cmd, ui.KeyA)
			tr.Type(field.value)
			tr.Frame()
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("editing capacity performed a write before Save: %v", err)
		}
		if err := tr.Click(msgs.ConnectionSaveConfiguration()); err != nil {
			t.Fatal(err)
		}
		loaded, err := loadConnectionConfig(path)
		config.ContextTokens, config.OutputTokens = 9007199254740991, 16385
		if err != nil || loaded != config {
			t.Fatalf("custom token limits did not round-trip exactly: context=%d output=%d error=%v", loaded.ContextTokens, loaded.OutputTokens, err)
		}
	})
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"2", 2}, {"  +2  ", 2}, {"0002", 2}, {"1", 1}, {strconv.Itoa(maxInt), maxInt},
		{"", 0}, {" ", 0}, {"1.5", 0}, {"2tasks", 0}, {"0x2", 0}, {"0", 0},
		{"999999999999999999999999", 0}, {"9007199254740993", 9007199254740993},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			config := testConnectionConfig(t)
			config.MaxActiveTasks = 5
			s := newConnectionSettings(&config, config.WorkingDirectory)
			path := filepath.Join(t.TempDir(), "connection.json")
			saves := 0
			tr := connectionSettingsTester(s, connectionSettingsActions{Save: func(candidate connectionConfig) {
				saves++
				if err := saveConnectionConfig(path, candidate); err != nil {
					t.Fatal(err)
				}
			}}, 600, 2000)
			tr.SetFocused(true)
			tr.Frame()
			if err := tr.Click(msgs.SettingsShowRuntimeDetails()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			field := msgs.ConnectionFieldMaxActiveTasks()
			for i := 0; i < 24 && !tr.Focused(field); i++ {
				tr.Key(0, ui.KeyTab)
				tr.Frame()
			}
			if active, _ := tr.TextInputActive(); !active || !tr.Focused(field) {
				t.Fatal("a saved count outside the segments was not editable")
			}
			tr.Key(ui.Cmd, ui.KeyA)
			tr.Key(0, ui.KeyBackspace)
			tr.Type(tc.raw)
			tr.Frame()
			s.Busy = true
			tr.Frame()
			tr.Type("7")
			if err := tr.Click(msgs.ConnectionSaveConfiguration()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if s.Draft.Slots != tc.raw || saves != 0 {
				t.Fatalf("editing or busy actions changed the raw task count or saved: %q, saves=%d", s.Draft.Slots, saves)
			}
			s.Busy = false
			tr.Frame()
			if err := tr.Click(msgs.ConnectionSaveConfiguration()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if tc.want == 0 {
				if saves != 0 || s.Draft.Slots != tc.raw || s.Errors["MaxActiveTasks"] != validationNumeric || !tr.Focused(field) || !tr.HasText(msgs.ValidationNumeric()) {
					t.Fatalf("invalid count did not preserve its draft, block Save and focus the error: %q %v", s.Draft.Slots, s.Errors)
				}
				return
			}
			loaded, err := loadConnectionConfig(path)
			config.MaxActiveTasks = tc.want
			if err != nil || saves != 1 || loaded != config || s.Draft.Slots != tc.raw || len(s.Errors) != 0 {
				t.Fatalf("saved count did not round-trip exactly: loaded=%+v draft=%q saves=%d error=%v", loaded, s.Draft.Slots, saves, err)
			}
		})
	}
}
