package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func connectionSettingsTester(s *connectionSettings, actions connectionSettingsActions, width, height int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		theme := resolveWorkbenchTheme(c.Theme().Dark).Theme
		c.SetTheme(theme)
		ui.Scroll(c).Fill().Padding(16).Background(theme.Surface).Children(func() { s.view(c, theme, actions) })
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
	if err := tr.Click("Save configuration"); err != nil {
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
	if err := tr.Click("Test connection and model"); err != nil {
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
	if err := tr.Click("Save configuration"); err != nil {
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
	config := testConnectionConfig(t)
	config.API = connectionAPIChatCompletions
	s := newConnectionSettings(&config, "")
	tr := connectionSettingsTester(s, connectionSettingsActions{}, 580, 1600)
	tr.Frame()
	if err := tr.Click("Protocol"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Protocol") || !tr.HasText("Chat completions") || !tr.HasText("Responses") {
		t.Fatalf("protocol choices missing: %v", tr.Texts())
	}
	if err := tr.Click("Responses"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if s.Draft.API != connectionAPIResponses {
		t.Fatalf("protocol draft = %q, want Responses", s.Draft.API)
	}
	if err := tr.Click("Protocol"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	s.Busy = true
	tr.Frame()
	if err := tr.Click("Chat completions"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if s.Draft.API != connectionAPIResponses {
		t.Fatalf("busy protocol changed draft to %q", s.Draft.API)
	}
}

func TestPreviewConnectionSettings(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR for connection form previews")
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
			tr := connectionSettingsTester(form, connectionSettingsActions{Save: func(config connectionConfig) { saved = config }}, 580, 1800)
			tr.SetLocale(locale)
			tr.SetFocused(true)
			tr.Frame()
			msgs := locales.In(locale)
			if !tr.HasText(msgs.WorkFolderRequiredHint()) {
				t.Fatal("fresh work-folder instruction is not visible")
			}
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
			if saved.WorkingDirectory != fixture.WorkingDirectory {
				t.Fatal("explicit text selection could not be saved")
			}
			form.ResetDraft()
			tr.Frame()
			if err := tr.Click(msgs.ConnectionSaveConfiguration()); err != nil {
				t.Fatal(err)
			}
			if form.Errors["WorkingDirectory"] == "" || !tr.HasText(msgs.WorkFolderRequiredHint()) {
				t.Fatal("reset silently reconfirmed a default work folder")
			}
		})
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
				initial := s.initial
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
				if !tr.HasText(msgs.ConnectionTestHint()) {
					t.Fatal("possibly charged explicit Test warning is hidden")
				}
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
				if len(tested) != 1 || saves != 0 || applies != 0 || saveApplies != 0 || recoveryRecordSnapshot(t, ct) != before || *a.connectionSaved != saved || *a.connectionEffective != old || !a.connectionSavedPending() || s.initial != initial || s.ErrorDetail != raw {
					t.Fatal("disclosure/language/navigation changed history/saved state or triggered Test/Save/Apply")
				}
				tr.SetLocale(locale)
				s.focusField = "Model"
				tr.Frame()
				tr.Key(ui.Cmd, ui.KeyA)
				tr.Type("edited-after-disclosure")
				tr.Frame()
				current.Model = "edited-after-disclosure"
				if err := tr.Click(msgs.ConnectionTestAction()); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if len(tested) != 2 || tested[1] != current || s.RecoveryDetailsOpen || s.Draft.Model != current.Model || s.initial != initial || !s.HasSaved || !s.SavedPending || recoveryRecordSnapshot(t, ct) != before || saves != 0 || applies != 0 || saveApplies != 0 || a.rt.nextID != 0 {
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
	if tests+saves+applies != 0 || s.initial.Endpoint != saved.Endpoint {
		t.Fatal("editing performed a confirmation action or advanced the saved baseline")
	}
	if err := tr.Click("Test connection and model"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if tested != current || tests != 1 || saves+applies != 0 {
		t.Fatal("Test did not consume the complete collapsed draft exactly once")
	}
	if err := tr.Click("Save configuration"); err != nil {
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
	if applies != 1 || tests != 1 || saves != 1 || s.initial.Endpoint != saved.Endpoint || s.Draft.Endpoint != current.Endpoint {
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
	if err := tr.Click("Save configuration"); err != nil {
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
	s.Draft.ContextTokens = "131072"
	if err := tr.Click("Save configuration"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Default working directory") || s.Draft.WorkingDirectory != "relative-path" || calls != 0 {
		t.Fatal("validation did not reveal and focus the manual folder input")
	}
}

func TestConnectionSettingsSlotsStepPreservesInvalidAndOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct{ raw, increase, decrease string }{
		{"2", "3", "1"}, {"  +2  ", "3", "1"}, {"0002", "3", "1"},
		{"1", "2", "1"}, {strconv.Itoa(maxInt), strconv.Itoa(maxInt), strconv.Itoa(maxInt - 1)},
		{"", "", ""}, {" ", " ", " "}, {"1.5", "1.5", "1.5"},
		{"2tasks", "2tasks", "2tasks"}, {"0x2", "0x2", "0x2"}, {"0", "0", "0"},
		{"999999999999999999999999", "999999999999999999999999", "999999999999999999999999"},
		{"9007199254740993", "9007199254740994", "9007199254740992"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			config := testConnectionConfig(t)
			s := newConnectionSettings(&config, config.WorkingDirectory)
			s.Draft.Slots, s.LimitsOpen = tc.raw, true
			s.Errors = map[string]string{"MaxActiveTasks": validationNumeric}
			tr := connectionSettingsTester(s, connectionSettingsActions{}, 600, 2000)
			tr.Frame()
			if err := tr.Click("Increase maximum active tasks"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if s.Draft.Slots != tc.increase || s.Errors["MaxActiveTasks"] != validationNumeric {
				t.Fatalf("increase %q = %q, want %q; pending error=%q", tc.raw, s.Draft.Slots, tc.increase, s.Errors["MaxActiveTasks"])
			}
			s.Draft.Slots = tc.raw
			tr.Frame()
			if err := tr.Click("Decrease maximum active tasks"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if s.Draft.Slots != tc.decrease {
				t.Fatalf("decrease %q = %q, want %q", tc.raw, s.Draft.Slots, tc.decrease)
			}
			s.Busy = true
			s.Draft.Slots = tc.raw
			tr.Frame()
			if err := tr.Click("Increase maximum active tasks"); err != nil {
				t.Fatal(err)
			}
			if s.Draft.Slots != tc.raw {
				t.Fatal("busy slot step changed the draft")
			}
		})
	}
}
