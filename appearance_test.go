package main

import (
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/egoist/mygo"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestAppearanceModeResolutionUsesDarkTheme(t *testing.T) {
	light := resolveWorkbenchTheme(false)
	dark := resolveWorkbenchTheme(true)
	if light.Theme.Dark {
		t.Fatal("light workbench theme is marked dark")
	}
	if !dark.Theme.Dark {
		t.Fatal("dark workbench theme is not marked dark")
	}
	if resolveAppearanceDark(appearanceSystem, true) != true ||
		resolveAppearanceDark(appearanceSystem, false) != false ||
		resolveAppearanceDark(appearanceLight, true) != false ||
		resolveAppearanceDark(appearanceDark, false) != true {
		t.Fatal("appearance mode did not resolve to the requested/system appearance")
	}
}

func TestCustomThemeSystemModeAndHeadlessPreviews(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	a.appearanceMode = appearanceSystem
	directory := t.TempDir()
	writeCustomTheme(t, directory, "light.json", customThemeDocument("garden-light", appearanceLight))
	writeCustomTheme(t, directory, "dark.json", customThemeDocument("garden-dark", appearanceDark))
	a.appearanceThemes = loadCustomThemes(directory)
	a.appearanceThemeIDs = appearanceThemeIDs{Light: "garden-light", Dark: "garden-dark"}
	ct.Draft, ct.ActiveTaskID = "Keep system appearance work", "system-task"
	tr := ui.NewTester(a.view, 1040, 720)
	for _, dark := range []bool{false, true, false} {
		tr.SetDark(dark)
		tr.Frame()
		resolved := a.resolveAppearanceTheme(dark)
		want := "garden-light"
		if dark {
			want = "garden-dark"
		}
		if resolved.EffectiveID != want || a.workTheme.Theme.Dark != dark || a.appearanceMode != appearanceSystem || ct.ActiveTaskID != "system-task" || ct.Draft != "Keep system appearance work" {
			t.Fatalf("system kind switch changed selection/work: dark=%v result=%+v", dark, resolved)
		}
	}
	if dir := os.Getenv("SAKI_PREVIEW_DIR"); dir != "" {
		a.openAppearance()
		for _, dark := range []bool{false, true} {
			name := "custom-theme-paper.png"
			if dark {
				name = "custom-theme-moss.png"
			}
			writeV07AppearancePreview(t, dir, name, a, 1040, 720, dark)
		}
	}
}

func TestSemanticPaletteContrastInvariants(t *testing.T) {
	for _, tc := range []struct {
		name  string
		theme workbenchTheme
	}{
		{name: "light", theme: resolveWorkbenchTheme(false)},
		{name: "dark", theme: resolveWorkbenchTheme(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th := tc.theme
			textBases := []struct {
				name string
				bg   ui.Color
			}{
				{"surface", th.Reading},
				{"panel", th.Panel},
				{"well", th.Well},
				{"bubble-user", th.BubbleUser},
				{"bubble-assistant", th.BubbleContact},
				{"hover", th.Theme.SurfaceHover},
				{"pressed", th.Theme.SurfacePressed},
				{"selection", th.Theme.Selection},
			}
			for _, base := range textBases {
				if got := paletteContrast(th.Theme.Text, base.bg); got < 4.5 {
					t.Errorf("%s text/%s contrast = %.3f, want >= 4.5", tc.name, base.name, got)
				}
			}
			// The reader copy control and composer attach icon render muted
			// glyphs on these same control faces, including pressed and selected.
			for _, base := range textBases {
				if got := paletteContrast(th.Theme.TextMuted, base.bg); got < 4.5 {
					t.Errorf("%s muted/%s contrast = %.3f, want >= 4.5", tc.name, base.name, got)
				}
			}
			for _, accent := range []struct {
				name string
				bg   ui.Color
			}{
				{"accent", th.Theme.Accent},
				{"accent-hover", th.Theme.AccentHover},
				{"accent-pressed", th.Theme.AccentPressed},
			} {
				if got := paletteContrast(th.Theme.AccentText, accent.bg); got < 4.5 {
					t.Errorf("%s on_accent/%s contrast = %.3f, want >= 4.5", tc.name, accent.name, got)
				}
			}
			for _, base := range []struct {
				name string
				bg   ui.Color
			}{
				{"surface", th.Reading},
				{"panel", th.Panel},
				{"well", th.Well},
				{"selected", th.Theme.Selection},
			} {
				if got := paletteContrast(th.Theme.Focus, base.bg); got < 3 {
					t.Errorf("%s focus/%s contrast = %.3f, want >= 3", tc.name, base.name, got)
				}
			}
			for _, base := range []struct {
				name string
				bg   ui.Color
			}{
				{"surface", th.Reading},
				{"panel", th.Panel},
				{"well", th.Well},
				{"selected", th.Theme.Selection},
			} {
				if got := paletteContrast(th.Theme.Border, base.bg); got < 3 {
					t.Errorf("%s control-border/%s contrast = %.3f, want >= 3", tc.name, base.name, got)
				}
			}
		})
	}
}

func TestAppearanceFirstLaunchCreatesExistingStoreWithoutLosingCurrentContact(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "contacts.json")
	t.Setenv("SAKI_GUI_STORE", storePath)
	t.Setenv("SAKI_GUI_WORKDIR", dir)
	t.Setenv("SAKI_SESSION_DIR", filepath.Join(dir, "sessions"))
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.store != nil {
		t.Fatal("unconfigured first launch unexpectedly had a contact store")
	}
	ct := a.selectedContact()
	id := ct.ID
	ct.Name = "First launch contact"
	ct.Draft = "keep this draft across appearance save"
	a.setAppearanceMode(appearanceDark)
	if a.store == nil {
		t.Fatal("appearance save did not lazily initialize the existing GUI store")
	}
	if a.appearanceError != "" {
		t.Fatalf("first-launch appearance save error = %q", a.appearanceError)
	}

	restarted, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if restarted.appearanceMode != appearanceDark {
		t.Fatalf("restarted appearance = %q", restarted.appearanceMode)
	}
	got := restarted.selectedContact()
	if got.ID != id || got.Draft != ct.Draft || got.Name != ct.Name {
		t.Fatalf("lazy store changed contact = id=%q draft=%q name=%q", got.ID, got.Draft, got.Name)
	}
	got.Name = "Renamed after appearance"
	if err := restarted.persist(); err != nil {
		t.Fatal(err)
	}
	afterContactSave, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if afterContactSave.appearanceMode != appearanceDark || afterContactSave.selectedContact().Name != got.Name {
		t.Fatalf("contact save lost appearance/name = mode=%q name=%q", afterContactSave.appearanceMode, afterContactSave.selectedContact().Name)
	}
}

func TestAppearanceSwitchKeepsComposerFocusAndActiveTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store, storePath: path, appearanceMode: appearanceSystem, rt: &runtimeClient{}, runtimeStatus: runtimeStateReady}
	applyStoreSelection(a, store)
	ct := a.selectedContact()
	ct.Draft = "focus and task survive"
	ct.ActiveTaskID = "task-keep"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	focusComposer(t, tr, "Composer for "+ct.Name)
	a.setAppearanceMode(appearanceDark)
	tr.Frame()
	if !tr.Focused("Composer for " + ct.Name) {
		t.Fatal("appearance switch moved focus away from the composer")
	}
	if a.selectedContact().Draft != "focus and task survive" || a.selectedContact().ActiveTaskID != "task-keep" {
		t.Fatal("appearance switch changed the draft or active task")
	}
}

func TestAppearanceFailedSaveKeepsModeAndRetryClearsWarning(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &contactStore{path: filepath.Join(blocked, "contacts.json")}
	a := &appModel{
		store:            store,
		selected:         0,
		appearanceMode:   appearanceSystem,
		contacts:         []*contact{{ID: "contact-keep", Name: "Saki", Draft: "draft-keep"}},
		issueToolLedger:  &IssueToolOperationLedger{},
		issueToolScopes:  map[string]IssueToolScope{},
		issueToolWaiters: map[string]chan IssueToolReceipt{},
	}
	a.setAppearanceMode(appearanceDark)
	if a.appearanceMode != appearanceDark {
		t.Fatalf("failed save reverted mode = %q", a.appearanceMode)
	}
	if a.appearanceError != appearanceStatusSaveFailed {
		t.Fatalf("failed save warning = %q", a.appearanceError)
	}
	if a.selectedContact().Draft != "draft-keep" {
		t.Fatal("failed appearance save changed the draft")
	}

	store.path = filepath.Join(t.TempDir(), "contacts.json")
	a.setAppearanceMode(appearanceDark)
	if a.appearanceMode != appearanceDark || a.appearanceError != "" {
		t.Fatalf("retry did not clear warning: mode=%q error=%q", a.appearanceMode, a.appearanceError)
	}
}

func TestContactScopeSelectAndComposerModelEntry(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Contact scope") || !tr.HasText("Active") {
		t.Fatalf("contact scope selector is missing: %q", tr.Texts())
	}
	if err := tr.Click("Active"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Archived"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.contactScope != "archived" {
		t.Fatalf("scope select = %q, want archived", a.contactScope)
	}

	a.selectContact(0)
	a.contacts[0].SessionID = ""
	a.contacts[0].Draft = "draft-kept"
	a.connectionEffective = &connectionConfig{Model: "model-default"}
	tr = ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	head, tail := a.conversationModelDisplayParts(locales.In("en"), a.contacts[0])
	if !tr.HasText(head) || !tr.HasText(tail) {
		t.Fatalf("unbound conversation default is not explicit: %q", tr.Texts())
	}
	if tr.HasText("Task model") {
		t.Fatal("issue model entry leaked into the conversation composer")
	}
	if err := tr.Click("Chat model"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.composerModelOpen || a.mainPageActive() || !tr.HasText("This context has no Runtime session yet") {
		t.Fatalf("conversation model entry = aux=%q texts=%q", a.aux, tr.Texts())
	}
	if a.selectedContact().Draft != "draft-kept" {
		t.Fatal("opening conversation model changed the draft")
	}
}

func TestAppearanceLaterContactSaveClearsUnsavedWarning(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &contactStore{path: filepath.Join(blocked, "contacts.json")}
	a := &appModel{
		store: store, selected: 0, appearanceMode: appearanceSystem,
		contacts:        []*contact{{ID: "contact-keep", Name: "Saki"}},
		issueToolLedger: &IssueToolOperationLedger{}, issueToolScopes: map[string]IssueToolScope{},
	}
	a.setAppearanceMode(appearanceDark)
	if a.appearanceError == "" {
		t.Fatal("expected the failed appearance save warning")
	}
	store.path = filepath.Join(t.TempDir(), "contacts.json")
	a.contacts[0].Draft = "later contact save"
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	if a.appearanceError != "" || a.appearanceMode != appearanceDark {
		t.Fatalf("later save left stale warning: mode=%q error=%q", a.appearanceMode, a.appearanceError)
	}
}

func TestAppearancePanelSwitchesModeAndKeepsCurrentDraft(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store, storePath: path, appearanceMode: appearanceSystem, rt: &runtimeClient{}, runtimeStatus: runtimeStateReady}
	applyStoreSelection(a, store)
	ct := a.selectedContact()
	const draftBeforeAppearance = "draft stays while appearance changes"
	ct.Draft = draftBeforeAppearance
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAppearance || !tr.HasText("Appearance") || !tr.HasText("System") ||
		a.appearanceMode != appearanceSystem || a.workTheme.Theme == nil || a.workTheme.Theme.Dark != a.systemDark {
		t.Fatalf("appearance panel/state missing: %q", tr.Texts())
	}
	// The official segmented control is one tab stop; arrows move and choose
	// among its radio segments, without a custom keyboard handler.
	for i := 0; i < 8 && !tr.Focused("System"); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused("System") {
		t.Fatalf("appearance segmented control is not keyboard reachable: %v", tr.Texts())
	}
	tr.Key(0, ui.KeyRight)
	tr.Frame()
	tr.Key(0, ui.KeyRight)
	tr.Frame()
	if !tr.Focused("Dark") {
		t.Fatalf("Dark appearance segment is not keyboard reachable: %v", tr.Texts())
	}
	if a.appearanceMode != appearanceDark || a.appearanceError != "" {
		t.Fatalf("appearance switch = mode=%q error=%q", a.appearanceMode, a.appearanceError)
	}
	if a.workTheme.Theme == nil || !a.workTheme.Theme.Dark {
		t.Fatal("dark appearance did not take effect in the next frame")
	}
	if a.selectedContact().Draft != draftBeforeAppearance {
		t.Fatal("appearance switch changed the current draft")
	}
}

func TestAppearanceRetrySaveUIAndRuntimeErrorOwnership(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &contactStore{path: filepath.Join(blocked, "contacts.json")}
	a := &appModel{
		store: store, selected: 0, appearanceMode: appearanceSystem, systemDark: true,
		contacts:        []*contact{{ID: "contact-keep", Name: "Saki", Draft: "keep"}},
		issueToolLedger: &IssueToolOperationLedger{}, issueToolScopes: map[string]IssueToolScope{},
		runtimeErr: "Runtime protocol error: unrelated",
	}
	a.openAppearance()
	tr := ui.NewTester(a.view, 1040, 720)
	// The actual system is dark while the stored choice stays System; the
	// segmented control exposes the three persistence modes independently.
	tr.SetDark(true)
	tr.Frame()
	// Settings focus order is Back, four category buttons, then the
	// segmented control on its selected System segment. One Right arrow
	// chooses Light through the official radio-group behavior.
	for i := 0; i < 6 && !tr.Focused("System"); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused("System") {
		t.Fatalf("System appearance segment is not keyboard focused: %v", tr.Texts())
	}
	tr.Key(0, ui.KeyRight)
	tr.Frame()
	if !tr.Focused("Light") {
		t.Fatalf("Light appearance segment is not keyboard focused after Right: %v", tr.Texts())
	}
	if a.appearanceMode != appearanceLight || a.appearanceError == "" {
		t.Fatalf("failed UI save = mode=%q error=%q", a.appearanceMode, a.appearanceError)
	}
	if !tr.HasText("Retry save") {
		t.Fatalf("retry action is not discoverable: %q", tr.Texts())
	}
	store.path = filepath.Join(t.TempDir(), "contacts.json")
	if err := tr.Click("Retry save"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.appearanceError != "" || a.appearanceMode != appearanceLight {
		t.Fatalf("retry left appearance state = mode=%q error=%q", a.appearanceMode, a.appearanceError)
	}
	if a.runtimeErr != "Runtime protocol error: unrelated" {
		t.Fatalf("recovery cleared an unrelated runtime error: %q", a.runtimeErr)
	}
	if a.selectedContact().Draft != "keep" {
		t.Fatal("appearance save retry changed the draft")
	}
}

func TestSegmentedSelectionKeepsTargetAndKeyboard(t *testing.T) {
	selected := 0
	tr := ui.NewTester(func(c *ui.Context) {
		segmentedSelection(c, &selected, "One", "Two", "Three").Label("Choice")
	}, 400, 200)
	tr.SetFocused(true)
	tr.Frame()
	for _, label := range []string{"One", "Two", "Three"} {
		bounds, ok := tr.Find(label)
		if !ok || bounds.W <= 0 || bounds.H < 32 {
			t.Fatalf("segment %q geometry = %+v ok=%v", label, bounds, ok)
		}
	}
	for i := 0; i < 8 && !tr.Focused("One"); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused("One") {
		t.Fatalf("segmented control is not keyboard reachable: %v", tr.Texts())
	}
	tr.Key(0, ui.KeyRight)
	tr.Frame()
	if selected != 1 {
		t.Fatalf("Right selected %d, want 1", selected)
	}
}

func themeImportTestApp(t *testing.T) (*appModel, *contact, chan func()) {
	t.Helper()
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	a.rt = &runtimeClient{}
	ct.Draft = "retained theme-import draft"
	ct.ActiveTaskID = "active-task"
	ct.SendState = sendAccepted
	ct.SessionState = "running"
	ct.Messages = []*message{{Kind: msgUser, Text: "original user text"}, {Kind: msgContact, Text: "Original Agent JSON is not translated."}}
	ct.History = []historyEntry{{ID: "original-history", Text: "original saved history"}}
	a.appearanceMode = appearanceSystem
	a.appearanceThemeIDs = appearanceThemeIDs{Light: "paper", Dark: "night"}
	a.appearanceThemes = loadCustomThemes(filepath.Join(t.TempDir(), "installed"))
	recoveryTestStore(t, a)
	a.openAppearance()
	a.uiLocale = "en"
	callbacks := make(chan func(), 8)
	a.setUIHook(func(fn func()) { callbacks <- fn })
	return a, ct, callbacks
}

func themeImportCompletion(t *testing.T, callbacks <-chan func()) func() {
	t.Helper()
	select {
	case fn := <-callbacks:
		return fn
	case <-time.After(5 * time.Second):
		t.Fatal("theme import completion was not queued")
		return nil
	}
}

func TestThemeImportCancelAndDialogErrorPreserveState(t *testing.T) {
	for _, dialogError := range []bool{false, true} {
		t.Run(fmt.Sprintf("dialog-error-%t", dialogError), func(t *testing.T) {
			a, ct, callbacks := themeImportTestApp(t)
			previous := &themeFileError{File: "earlier.json", Field: "colors.text", Message: "earlier diagnostic"}
			a.themeImport = &appearanceThemeImport{Problem: previous, ImportedName: "earlier theme"}
			a.appearanceError = appearanceStatusSaveFailed
			a.appearanceStatus = appearanceStatusApplied
			before := recoveryRecordSnapshot(t, ct)
			ids, mode := a.appearanceThemeIDs, a.appearanceMode
			catalog := a.appearanceThemes
			stored, err := os.ReadFile(a.store.path)
			if err != nil {
				t.Fatal(err)
			}
			a.chooseThemeFile = func(mygo.OpenDialogOptions) ([]string, error) {
				if dialogError {
					return nil, fmt.Errorf("controlled dialog error")
				}
				return nil, nil
			}
			a.importAppearanceTheme()
			themeImportCompletion(t, callbacks)()
			if a.themeImport.Busy || !a.themeImport.focus {
				t.Fatal("dialog did not release its initiating control")
			}
			if !dialogError && (a.themeImport.Problem != previous || a.themeImport.ImportedName != "earlier theme") {
				t.Fatal("cancel changed prior import feedback")
			}
			if dialogError && (a.themeImport.Problem == nil || a.themeImport.Problem.Message != "controlled dialog error") {
				t.Fatal("dialog error is not available for retry")
			}
			after, err := os.ReadFile(a.store.path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(stored) || a.appearanceThemeIDs != ids || a.appearanceMode != mode || !reflect.DeepEqual(a.appearanceThemes, catalog) || recoveryRecordSnapshot(t, ct) != before || a.appearanceError != appearanceStatusSaveFailed || a.appearanceStatus != appearanceStatusApplied || a.rt.nextID != 0 {
				t.Fatal("cancel/dialog error changed preferences, independent save feedback, work or store")
			}
			if _, err := os.Stat(catalog.Directory); !os.IsNotExist(err) {
				t.Fatal("cancel/dialog error installed a directory")
			}
		})
	}
}

func TestThemeImportFailuresPreserveFilesAndPreferences(t *testing.T) {
	for _, kind := range []string{"invalid-json", "reserved-id", "duplicate-id", "excluded-duplicate-id", "filename-collision", "missing-source", "unreadable-source", "target-file", "target-readonly"} {
		t.Run(kind, func(t *testing.T) {
			a, ct, callbacks := themeImportTestApp(t)
			directory := t.TempDir()
			a.appearanceThemes = loadCustomThemes(directory)
			sourceDir := t.TempDir()
			source := writeCustomTheme(t, sourceDir, "downloaded.json", customThemeDocument("imported", appearanceLight))
			switch kind {
			case "invalid-json":
				if err := os.WriteFile(source, []byte("{invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "reserved-id":
				source = writeCustomTheme(t, sourceDir, "downloaded.json", customThemeDocument("paper", appearanceLight))
			case "duplicate-id":
				writeCustomTheme(t, directory, "existing.json", customThemeDocument("imported", appearanceLight))
			case "excluded-duplicate-id":
				writeCustomTheme(t, directory, "first.json", customThemeDocument("imported", appearanceLight))
				writeCustomTheme(t, directory, "second.json", customThemeDocument("imported", appearanceDark))
			case "filename-collision":
				writeCustomTheme(t, directory, "downloaded.json", customThemeDocument("existing", appearanceLight))
			case "missing-source":
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
			case "unreadable-source":
				if err := os.Chmod(source, 0); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(source, 0600)
			case "target-file":
				directory = filepath.Join(t.TempDir(), "existing-file")
				if err := os.WriteFile(directory, []byte("keep original destination"), 0600); err != nil {
					t.Fatal(err)
				}
			case "target-readonly":
				if err := os.Chmod(directory, 0500); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(directory, 0700)
			}
			a.appearanceThemes = loadCustomThemes(directory)
			catalog := a.appearanceThemes
			beforeFiles := map[string]string{}
			if kind != "target-file" {
				beforeFiles = snapshotThemeFiles(t, directory)
			}
			before := recoveryRecordSnapshot(t, ct)
			ids, mode := a.appearanceThemeIDs, a.appearanceMode
			stored, err := os.ReadFile(a.store.path)
			if err != nil {
				t.Fatal(err)
			}
			a.chooseThemeFile = func(mygo.OpenDialogOptions) ([]string, error) { return []string{source}, nil }
			a.importAppearanceTheme()
			themeImportCompletion(t, callbacks)()
			themeImportCompletion(t, callbacks)()
			if a.themeImport.Busy || a.themeImport.Problem == nil || a.themeImport.Problem.Message == "" || !a.themeImport.focus {
				t.Fatalf("import failure is not actionable: %+v", a.themeImport)
			}
			if kind == "target-file" {
				data, err := os.ReadFile(directory)
				if err != nil || string(data) != "keep original destination" {
					t.Fatal("import replaced the target file")
				}
			} else if !reflect.DeepEqual(beforeFiles, snapshotThemeFiles(t, directory)) {
				t.Fatal("failed import changed installed files")
			}
			after, err := os.ReadFile(a.store.path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(stored) || a.appearanceThemeIDs != ids || a.appearanceMode != mode || !reflect.DeepEqual(a.appearanceThemes, catalog) || recoveryRecordSnapshot(t, ct) != before || a.rt.nextID != 0 {
				t.Fatal("failed import rewrote preferences, catalog, work or store")
			}
		})
	}
}

func TestThemeImportStaleDialogAndInstallResults(t *testing.T) {
	for _, phase := range []string{"dialog", "installed"} {
		for _, leave := range []string{"back", "tab-return", "contact", "shutdown"} {
			t.Run(phase+"/"+leave, func(t *testing.T) {
				a, ct, callbacks := themeImportTestApp(t)
				directory := a.appearanceThemes.Directory
				source := writeCustomTheme(t, t.TempDir(), "late.json", customThemeDocument("late-id", appearanceLight))
				a.themeImport = &appearanceThemeImport{ImportedName: "prior result"}
				a.chooseThemeFile = func(mygo.OpenDialogOptions) ([]string, error) { return []string{source}, nil }
				before := recoveryRecordSnapshot(t, ct)
				catalog := a.appearanceThemes
				ids, mode := a.appearanceThemeIDs, a.appearanceMode
				a.importAppearanceTheme()
				completion := themeImportCompletion(t, callbacks)
				if phase == "installed" {
					completion()
					completion = themeImportCompletion(t, callbacks)
				}
				switch leave {
				case "back":
					a.closeSettings()
				case "tab-return":
					a.selectSettingsTab(settingsTabConnection)
					a.selectSettingsTab(settingsTabAppearance)
				case "contact":
					a.contacts = append(a.contacts, &contact{ID: "different-contact", Name: "Other", Loaded: true})
					a.selected = 1
				case "shutdown":
					a.shutdownStarted = true
					a.shutdownDrainStarted = true
				}
				completion()
				if a.appearanceThemeIDs != ids || a.appearanceMode != mode || !reflect.DeepEqual(a.appearanceThemes, catalog) || a.themeImport.ImportedName != "prior result" || a.themeImport.Problem != nil || a.themeImport.focus || recoveryRecordSnapshot(t, ct) != before || a.rt.nextID != 0 {
					t.Fatal("late result changed another view/contact or shutdown state")
				}
				if leave == "shutdown" && !a.themeImport.Busy {
					t.Fatal("post-shutdown callback mutated import state")
				}
				_, err := os.Stat(filepath.Join(directory, "late.json"))
				if phase == "dialog" && !os.IsNotExist(err) {
					t.Fatal("stale dialog started installation")
				}
				if phase == "installed" && err != nil {
					t.Fatal("completed explicit install was removed or lost")
				}
			})
		}
	}
}

func TestThemeImportShutdownWaitsForOwnedCompletion(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		for _, leave := range []bool{false, true} {
			t.Run(fmt.Sprintf("invalid-%t/leave-%t", invalid, leave), func(t *testing.T) {
				a, ct, callbacks := themeImportTestApp(t)
				a.rt = nil
				source := writeCustomTheme(t, t.TempDir(), "shutdown.json", customThemeDocument("shutdown-theme", appearanceLight))
				if invalid {
					if err := os.WriteFile(source, []byte("{invalid"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				original, err := os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}
				previous := &themeFileError{File: "earlier.json", Field: "$", Message: "earlier feedback"}
				a.themeImport = &appearanceThemeImport{Problem: previous, ImportedName: "earlier theme"}
				a.chooseThemeFile = func(mygo.OpenDialogOptions) ([]string, error) { return []string{source}, nil }
				before := recoveryRecordSnapshot(t, ct)
				catalog, ids, mode := a.appearanceThemes, a.appearanceThemeIDs, a.appearanceMode
				quitCount := 0
				a.requestQuit = func() { quitCount++ }
				a.importAppearanceTheme()
				themeImportCompletion(t, callbacks)()
				// I/O has finished; its final UI-owned completion is deliberately still queued.
				completion := themeImportCompletion(t, callbacks)
				if leave {
					a.closeSettings()
				}
				a.beginShutdown()
				a.beginShutdown()
				a.finishShutdown()
				if a.quitReady || quitCount != 0 {
					t.Errorf("quit escaped the admitted import before its final owned callback: ready=%t calls=%d", a.quitReady, quitCount)
				}
				completion()
				if !a.quitReady || quitCount != 1 {
					t.Fatalf("import completion did not finish shutdown exactly once: ready=%t calls=%d", a.quitReady, quitCount)
				}
				a.beginShutdown()
				a.finishShutdown()
				if quitCount != 1 || a.rt != nil || !reflect.DeepEqual(a.appearanceThemes, catalog) || a.appearanceThemeIDs != ids || a.appearanceMode != mode || a.themeImport.Problem != previous || a.themeImport.ImportedName != "earlier theme" || a.themeImport.focus || recoveryRecordSnapshot(t, ct) != before {
					t.Fatal("shutdown completion changed catalog/preferences/feedback/work or requested quit again")
				}
				installed, err := os.ReadFile(filepath.Join(catalog.Directory, filepath.Base(source)))
				if invalid {
					if !os.IsNotExist(err) {
						t.Fatal("invalid import left an installed file")
					}
				} else if err != nil || string(installed) != string(original) {
					t.Fatal("shutdown lost or changed the completed explicit installation")
				}
			})
		}
	}
}

func TestThemeImportShutdownDoesNotWaitForDialog(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(fmt.Sprintf("selected-%t", selected), func(t *testing.T) {
			a, ct, callbacks := themeImportTestApp(t)
			a.rt = nil
			source := writeCustomTheme(t, t.TempDir(), "late-dialog.json", customThemeDocument("late-dialog-theme", appearanceLight))
			entered, release := make(chan struct{}), make(chan struct{})
			a.chooseThemeFile = func(mygo.OpenDialogOptions) ([]string, error) {
				close(entered)
				<-release
				if selected {
					return []string{source}, nil
				}
				return nil, nil
			}
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			before := recoveryRecordSnapshot(t, ct)
			catalog, ids, mode := a.appearanceThemes, a.appearanceThemeIDs, a.appearanceMode
			quitCount := 0
			a.requestQuit = func() { quitCount++ }
			a.importAppearanceTheme()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("controlled picker did not open")
			}
			a.beginShutdown()
			if !a.quitReady || quitCount != 1 {
				t.Fatal("quit waited for a dialog that had admitted no installation")
			}
			close(release)
			themeImportCompletion(t, callbacks)()
			if quitCount != 1 || !reflect.DeepEqual(a.appearanceThemes, catalog) || a.appearanceThemeIDs != ids || a.appearanceMode != mode || a.themeImport.Problem != nil || a.themeImport.ImportedName != "" || a.themeImport.focus || recoveryRecordSnapshot(t, ct) != before {
				t.Fatal("late picker result changed the shutting-down app")
			}
			if _, err := os.Stat(catalog.Directory); !os.IsNotExist(err) {
				t.Fatal("late picker result started installation after quit")
			}
		})
	}
}

func TestThemeImportRestoresRequestedMissingIDWithoutSaving(t *testing.T) {
	a, ct, callbacks := themeImportTestApp(t)
	a.appearanceThemeIDs.Light = "restored-theme"
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	if !a.resolveAppearanceTheme(false).Unavailable {
		t.Fatal("fixture theme is not missing")
	}
	document := customThemeDocument("restored-theme", appearanceLight)
	document["colors"].(map[string]any)["work_attention"] = "#FFFFFF"
	source := writeCustomTheme(t, t.TempDir(), "restored.json", document)
	a.chooseThemeFile = func(mygo.OpenDialogOptions) ([]string, error) { return []string{source}, nil }
	before := recoveryRecordSnapshot(t, ct)
	stored, err := os.ReadFile(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	ids, mode := a.appearanceThemeIDs, a.appearanceMode
	a.importAppearanceTheme()
	themeImportCompletion(t, callbacks)()
	themeImportCompletion(t, callbacks)()
	resolved := a.resolveAppearanceTheme(false)
	after, err := os.ReadFile(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.EffectiveID != "restored-theme" || resolved.Unavailable || len(resolved.Warnings) == 0 || a.appearanceThemeIDs != ids || a.appearanceMode != mode || string(after) != string(stored) || recoveryRecordSnapshot(t, ct) != before {
		t.Fatal("missing-ID restoration lost warning, changed selection/mode or saved work")
	}
}

func TestC8ThemeInstallExcludesRecoveryAndKeepsQuitDrain(t *testing.T) {
	a := c8SavedApp(t)
	needsServiceClose := a.issueToolServer != nil
	if os.Getenv("SAKI_TEST_ISSUE_TOOL_HELPER") != "" && !needsServiceClose {
		t.Fatal("configured saved-app fixture has no owned host service")
	}
	a.appearanceThemes = loadCustomThemes(filepath.Join(t.TempDir(), "themes"))
	req, reply, updates := c8ControlledPicker(t, a)
	a.chooseThemeFile = a.chooseRecoveryFile
	source := writeCustomTheme(t, t.TempDir(), "owned-theme.json", customThemeDocument("owned-theme", appearanceLight))
	a.importAppearanceTheme()
	awaitFolderChoice(t, req)
	reply <- folderChoiceReply{paths: []string{source}}
	awaitFolderChoice(t, updates)()
	completion := awaitFolderChoice(t, updates)
	a.chooseDataRecovery(false)
	if a.dataRecovery != nil && (a.dataRecovery.choosing || a.dataRecovery.inFlight) {
		t.Fatal("admitted theme installation admitted recovery")
	}
	quit := 0
	a.requestQuit = func() { quit++ }
	a.beginShutdown()
	if a.quitReady || quit != 0 {
		t.Fatal("recovery integration bypassed C7 owned-install drain")
	}
	completion()
	if needsServiceClose {
		if a.quitReady || quit != 0 {
			t.Fatal("Quit escaped the owned host-service close completion")
		}
		awaitFolderChoice(t, updates)()
	}
	if !a.quitReady || quit != 1 || a.themeImport.installInFlight {
		t.Fatal("C7 final completion did not release quit after recovery rejection")
	}
	t.Logf("host-service close completion required=%t; final Quit callbacks=%d", needsServiceClose, quit)
}
