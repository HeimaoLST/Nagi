package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func customThemeDocument(id, kind string) map[string]any {
	palette := resolveWorkbenchTheme(kind == appearanceDark)
	colors := make(map[string]any)
	for _, field := range themeColorFields {
		if field.fallback == "" {
			color := *field.target(&palette)
			colors[field.name] = fmt.Sprintf("#%02X%02X%02X", color.R, color.G, color.B)
		}
	}
	return map[string]any{"version": 1, "id": id, "name": "Garden", "kind": kind, "colors": colors}
}

func writeCustomTheme(t *testing.T, directory, filename string, document map[string]any) string {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, filename)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func snapshotThemeFiles(t *testing.T, directory string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]string, len(entries))
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = string(data)
	}
	return files
}

func TestCustomThemeInstallCreatesDirectoryAndUsesExistingCatalog(t *testing.T) {
	source := writeCustomTheme(t, t.TempDir(), "downloaded-garden.json", customThemeDocument("garden-import", appearanceLight))
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "config", "themes")
	app := &appModel{appearanceMode: appearanceDark, appearanceThemes: loadCustomThemes(directory)}
	theme, problem := installCustomTheme(source, directory)
	if problem != nil || theme.ID != "garden-import" || theme.File != filepath.Join(directory, filepath.Base(source)) {
		t.Fatalf("valid install = %+v, problem=%+v", theme, problem)
	}
	installed, err := os.ReadFile(theme.File)
	if err != nil || string(installed) != string(original) {
		t.Fatal("import did not preserve the selected JSON bytes and basename")
	}
	if data, err := os.ReadFile(source); err != nil || string(data) != string(original) {
		t.Fatal("import changed the selected source file")
	}
	app.reloadAppearanceThemes()
	if len(app.appearanceThemes.Themes) != 1 || app.appearanceThemes.Themes[0].ID != theme.ID || len(app.appearanceThemes.Errors) != 0 {
		t.Fatalf("installed file was not discovered by the existing loader: %+v", app.appearanceThemes)
	}
	if app.appearanceMode != appearanceDark || app.appearanceThemeIDs != (appearanceThemeIDs{}) || app.resolveAppearanceTheme(false).EffectiveID != "paper" {
		t.Fatal("import selected a theme or changed appearance preferences")
	}
	broken := filepath.Join(directory, "broken.json")
	if err := os.WriteFile(broken, []byte("invalid existing theme"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotThemeFiles(t, directory)
	second := writeCustomTheme(t, t.TempDir(), "mode-word.json", customThemeDocument("system", appearanceDark))
	if _, problem := installCustomTheme(second, directory); problem != nil {
		t.Fatalf("unrelated invalid file or non-reserved ID prevented import: %+v", problem)
	}
	after := snapshotThemeFiles(t, directory)
	for file, data := range before {
		if after[file] != data {
			t.Fatalf("import changed existing file %q", file)
		}
	}
	catalog := loadCustomThemes(directory)
	if len(catalog.Themes) != 2 || len(catalog.Errors) != 1 || catalog.Errors[0].File != broken {
		t.Fatalf("import repaired or hid an existing invalid theme: %+v", catalog)
	}
}

func TestCustomThemeInstallRejectsInvalidSourcesBeforeCreatingDirectory(t *testing.T) {
	for _, kind := range []string{"malformed", "color", "paper", "night", "extension"} {
		t.Run(kind, func(t *testing.T) {
			document := customThemeDocument("valid-id", appearanceLight)
			field := "$"
			if kind == "paper" || kind == "night" {
				document["id"], field = kind, "id"
			}
			if kind == "color" {
				document["colors"].(map[string]any)["text"], field = "#FFFFFF80", "colors.text"
			}
			filename := "download.json"
			if kind == "extension" {
				filename = "download.txt"
			}
			source := writeCustomTheme(t, t.TempDir(), filename, document)
			if kind == "malformed" {
				if err := os.WriteFile(source, []byte(`{"version":1`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			original, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(t.TempDir(), "not-created")
			_, problem := installCustomTheme(source, directory)
			if problem == nil || problem.File != source || problem.Field != field || problem.Message == "" {
				t.Fatalf("invalid source diagnostic = %+v", problem)
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("rejected source created the target directory")
			}
			if data, err := os.ReadFile(source); err != nil || string(data) != string(original) {
				t.Fatal("rejected source was changed")
			}
		})
	}
}

func TestCustomThemeInstallRejectsIDAndFilenameCollisions(t *testing.T) {
	for _, kind := range []string{"existing-id", "excluded-duplicate-id", "filename"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			writeCustomTheme(t, directory, "existing.json", customThemeDocument("claimed", appearanceLight))
			id, filename, field := "claimed", "download.json", "id"
			if kind == "excluded-duplicate-id" {
				writeCustomTheme(t, directory, "second.json", customThemeDocument("claimed", appearanceDark))
				if catalog := loadCustomThemes(directory); len(catalog.Themes) != 0 || len(catalog.Errors) != 2 {
					t.Fatal("fixture did not expose an excluded duplicate group")
				}
			}
			if kind == "filename" {
				id, filename, field = "different-id", "existing.json", "$"
			}
			source := writeCustomTheme(t, t.TempDir(), filename, customThemeDocument(id, appearanceLight))
			before := snapshotThemeFiles(t, directory)
			_, problem := installCustomTheme(source, directory)
			if problem == nil || problem.Field != field || problem.Message == "" {
				t.Fatalf("collision did not return a useful diagnostic: %+v", problem)
			}
			if kind != "filename" && (!strings.Contains(problem.Message, "existing.json") || (kind == "excluded-duplicate-id" && !strings.Contains(problem.Message, "second.json"))) {
				t.Fatalf("ID collision omitted the claiming paths: %+v", problem)
			}
			if after := snapshotThemeFiles(t, directory); !reflect.DeepEqual(before, after) {
				t.Fatal("collision replaced or silently renamed a theme file")
			}
		})
	}
}

func TestCustomThemeInstallReadAndWriteFailuresPreserveExistingFiles(t *testing.T) {
	t.Run("missing-source", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "missing.json")
		directory := filepath.Join(t.TempDir(), "not-created")
		_, problem := installCustomTheme(source, directory)
		if problem == nil || problem.File != source || problem.Field != "$" || problem.Message == "" {
			t.Fatalf("source read failure = %+v", problem)
		}
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("failed source read created a target directory")
		}
	})
	t.Run("unreadable-source", func(t *testing.T) {
		source := writeCustomTheme(t, t.TempDir(), "source.json", customThemeDocument("source-id", appearanceLight))
		if err := os.Chmod(source, 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(source, 0o600)
		directory := filepath.Join(t.TempDir(), "not-created")
		_, problem := installCustomTheme(source, directory)
		if problem == nil || problem.File != source || problem.Message == "" {
			t.Fatalf("source permission failure = %+v", problem)
		}
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("failed source read created a target directory")
		}
	})
	t.Run("target-is-file", func(t *testing.T) {
		source := writeCustomTheme(t, t.TempDir(), "source.json", customThemeDocument("source-id", appearanceLight))
		directory := filepath.Join(t.TempDir(), "themes")
		if err := os.WriteFile(directory, []byte("existing user file"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, problem := installCustomTheme(source, directory)
		if problem == nil || problem.File != directory || problem.Message == "" {
			t.Fatalf("target read failure = %+v", problem)
		}
		if data, err := os.ReadFile(directory); err != nil || string(data) != "existing user file" {
			t.Fatal("failed target read replaced the user file")
		}
	})
	t.Run("target-not-writable", func(t *testing.T) {
		source := writeCustomTheme(t, t.TempDir(), "source.json", customThemeDocument("source-id", appearanceLight))
		directory := t.TempDir()
		writeCustomTheme(t, directory, "old.json", customThemeDocument("old-id", appearanceDark))
		before := snapshotThemeFiles(t, directory)
		if err := os.Chmod(directory, 0o500); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(directory, 0o700)
		_, problem := installCustomTheme(source, directory)
		if problem == nil || problem.File != filepath.Join(directory, filepath.Base(source)) || problem.Message == "" {
			t.Fatalf("target write failure = %+v", problem)
		}
		if after := snapshotThemeFiles(t, directory); !reflect.DeepEqual(before, after) {
			t.Fatal("failed write replaced existing files or left an incomplete theme")
		}
	})
}

func TestCustomThemeInstallRestoresMissingSelectionWithContrastWarning(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "themes")
	app := &appModel{appearanceMode: appearanceSystem, appearanceThemeIDs: appearanceThemeIDs{Light: "restored", Dark: "night"}, appearanceThemes: loadCustomThemes(directory)}
	if !app.resolveAppearanceTheme(false).Unavailable {
		t.Fatal("fixture did not have a missing selected ID")
	}
	document := customThemeDocument("restored", appearanceLight)
	document["colors"].(map[string]any)["work_attention"] = "#FFFFFF"
	source := writeCustomTheme(t, t.TempDir(), "restored.json", document)
	ids := app.appearanceThemeIDs
	theme, problem := installCustomTheme(source, directory)
	if problem != nil || len(theme.Warnings) == 0 {
		t.Fatalf("low-contrast theme was rejected or warning omitted: %+v %+v", theme, problem)
	}
	if !app.resolveAppearanceTheme(false).Unavailable {
		t.Fatal("install unexpectedly reloaded or changed application state")
	}
	app.reloadAppearanceThemes()
	resolved := app.resolveAppearanceTheme(false)
	if resolved.EffectiveID != "restored" || resolved.Unavailable || len(resolved.Warnings) == 0 || app.appearanceThemeIDs != ids || app.appearanceMode != appearanceSystem || app.resolveAppearanceTheme(true).EffectiveID != "night" || app.store != nil || app.rt != nil {
		t.Fatal("reload did not restore the existing request independently of preferences/store/Runtime")
	}
}

func TestCustomThemeDirectoryValidationAndDuplicateRecovery(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "absent")
	if got := loadCustomThemes(missing); len(got.Errors) != 0 || len(got.Themes) != 0 {
		t.Fatalf("missing directory = %+v", got)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("discovery created the missing directory")
	}
	light := customThemeDocument("garden", appearanceLight)
	light["colors"].(map[string]any)["well"] = "#DDEEDD"
	writeCustomTheme(t, directory, "light.json", light)
	writeCustomTheme(t, directory, "dark.json", customThemeDocument("garden-dark", appearanceDark))
	cases := []struct {
		name, field string
		change      func(map[string]any)
	}{
		{"version", "version", func(d map[string]any) { d["version"] = 2 }},
		{"missing", "colors.text", func(d map[string]any) { delete(d["colors"].(map[string]any), "text") }},
		{"alpha", "colors.text", func(d map[string]any) { d["colors"].(map[string]any)["text"] = "#FFFFFF80" }},
		{"null", "colors.reading", func(d map[string]any) { d["colors"].(map[string]any)["reading"] = nil }},
		{"unknown", "colors.link", func(d map[string]any) { d["colors"].(map[string]any)["link"] = "#FFFFFF" }},
		{"font", "font", func(d map[string]any) { d["font"] = "Other font" }},
		{"reserved", "id", func(d map[string]any) { d["id"] = "paper" }},
		{"bad-id", "id", func(d map[string]any) { d["id"] = "Not-Kebab" }},
		{"kind", "kind", func(d map[string]any) { d["kind"] = "system" }},
	}
	for _, tc := range cases {
		document := customThemeDocument("bad-"+tc.name, appearanceLight)
		tc.change(document)
		writeCustomTheme(t, directory, tc.name+".json", document)
	}
	malformed := filepath.Join(directory, "malformed.json")
	if err := os.WriteFile(malformed, []byte(`{"version":1`), 0o600); err != nil {
		t.Fatal(err)
	}
	wrongRoot := filepath.Join(directory, "wrong-root.json")
	if err := os.WriteFile(wrongRoot, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := loadCustomThemes(directory)
	if len(catalog.Themes) != 2 || len(catalog.Errors) != len(cases)+2 {
		t.Fatalf("invalid files affected valid themes: %+v", catalog)
	}
	for _, theme := range catalog.Themes {
		if theme.ID == "garden" {
			palette := theme.Palette
			if palette.Well != ui.Hex("#DDEEDD") || palette.WorkWarningBg != palette.Well || palette.Reading != palette.Theme.Background || palette.WorkText != palette.Theme.Text || palette.Theme.Scrollbar != palette.Theme.TextMuted {
				t.Fatal("optional colors did not resolve from the same file")
			}
		}
	}
	for _, tc := range cases {
		found := false
		for _, problem := range catalog.Errors {
			if problem.File == filepath.Join(directory, tc.name+".json") && problem.Field == tc.field && problem.Message != "" {
				found = true
			}
		}
		if !found {
			t.Errorf("missing file/field error for %s: %+v", tc.name, catalog.Errors)
		}
	}
	for _, problem := range catalog.Errors {
		if problem.File == wrongRoot && (problem.Field != "$" || problem.Message == "") {
			t.Fatalf("wrong root type lost its root diagnostic: %+v", problem)
		}
	}
	duplicate := writeCustomTheme(t, directory, "duplicate.json", customThemeDocument("garden", appearanceLight))
	catalog = loadCustomThemes(directory)
	if len(catalog.Themes) != 1 || catalog.Themes[0].ID != "garden-dark" {
		t.Fatalf("duplicate silently chose a winner: %+v", catalog.Themes)
	}
	var duplicateFiles []string
	for _, problem := range catalog.Errors {
		if strings.Contains(problem.Message, "duplicate ID") {
			duplicateFiles = append(duplicateFiles, problem.File)
			if !strings.Contains(problem.Message, duplicate) || !strings.Contains(problem.Message, "light.json") {
				t.Fatalf("duplicate error omitted peers: %+v", problem)
			}
		}
	}
	if len(duplicateFiles) != 2 {
		t.Fatalf("duplicate diagnostics = %v", duplicateFiles)
	}
	if err := os.Remove(duplicate); err != nil {
		t.Fatal(err)
	}
	writeCustomTheme(t, directory, "mode-word.json", customThemeDocument("system", appearanceLight))
	if got := loadCustomThemes(directory); len(got.Themes) != 3 {
		t.Fatalf("recovery or non-reserved ID failed: %+v", got)
	}
	if got := loadCustomThemes(malformed); len(got.Errors) != 1 || got.Errors[0].File != malformed {
		t.Fatalf("directory read error = %+v", got)
	}
}

func TestCustomThemeSelectionRetainsMissingIDsAcrossSavesAndRecovery(t *testing.T) {
	directory := t.TempDir()
	file := writeCustomTheme(t, directory, "choice.json", customThemeDocument("garden", appearanceLight))
	store, err := openContactStore(filepath.Join(t.TempDir(), "contacts.json"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{appearanceThemes: loadCustomThemes(directory)}
	applyStoreSelection(a, store)
	a.setAppearanceTheme(appearanceLight, "garden")
	if a.appearanceError != "" || a.resolveAppearanceTheme(false).EffectiveID != "garden" {
		t.Fatalf("selection was not saved/applied: %+v", a)
	}
	restart := func() *appModel {
		t.Helper()
		reloaded, err := openContactStore(store.path, "/ignored")
		if err != nil {
			t.Fatal(err)
		}
		app := &appModel{appearanceThemes: loadCustomThemes(directory)}
		applyStoreSelection(app, reloaded)
		return app
	}
	b := restart()
	if b.appearanceThemeIDs.Light != "garden" {
		t.Fatalf("restart lost the requested ID: %+v", b.appearanceThemeIDs)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	b.reloadAppearanceThemes()
	missing := b.resolveAppearanceTheme(false)
	if !missing.Unavailable || missing.RequestedID != "garden" || missing.EffectiveID != "paper" || missing.Palette.Theme.Dark {
		t.Fatalf("missing light theme did not fall back in kind: %+v", missing)
	}
	b.selectedContact().Draft = "Keep a later contact edit"
	if err := b.persist(); err != nil {
		t.Fatal(err)
	}
	b = restart()
	if b.appearanceThemeIDs.Light != "garden" || b.selectedContact().Draft != "Keep a later contact edit" {
		t.Fatal("unrelated save or restart discarded missing selection/draft")
	}
	writeCustomTheme(t, directory, "renamed.json", customThemeDocument("garden", appearanceDark))
	b.reloadAppearanceThemes()
	if !b.resolveAppearanceTheme(false).Unavailable || b.resolveAppearanceTheme(true).EffectiveID != "night" {
		t.Fatal("kind change moved or cross-applied the selected ID")
	}
	b.setAppearanceTheme(appearanceDark, "garden")
	if got := b.resolveAppearanceTheme(true); got.EffectiveID != "garden" || !got.Palette.Theme.Dark {
		t.Fatalf("explicit dark selection failed: %+v", got)
	}
	writeCustomTheme(t, directory, "renamed.json", customThemeDocument("garden", appearanceLight))
	b.reloadAppearanceThemes()
	if b.resolveAppearanceTheme(false).EffectiveID != "garden" || !b.resolveAppearanceTheme(true).Unavailable {
		t.Fatal("restored ID did not recover its original kind slot")
	}
}

func TestCustomThemeLowContrastStillAppliesAndRetriesFailedSave(t *testing.T) {
	directory := t.TempDir()
	document := customThemeDocument("low-contrast", appearanceLight)
	document["colors"].(map[string]any)["text"] = "#FFFFFF"
	writeCustomTheme(t, directory, "low.json", document)
	catalog := loadCustomThemes(directory)
	if len(catalog.Errors) != 0 || len(catalog.Themes) != 1 || len(catalog.Themes[0].Warnings) == 0 {
		t.Fatalf("low contrast was rejected or not warned: %+v", catalog)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ct := &contact{ID: "contact-keep", Draft: "draft-keep", ActiveTaskID: "task-keep", SessionID: "session-keep"}
	a := &appModel{store: &contactStore{path: filepath.Join(blocked, "contacts.json")}, contacts: []*contact{ct}, appearanceThemes: catalog, runtimeErr: "unrelated Runtime error"}
	a.setAppearanceTheme(appearanceLight, "low-contrast")
	if a.appearanceError != appearanceStatusSaveFailed || a.resolveAppearanceTheme(false).EffectiveID != "low-contrast" {
		t.Fatal("failed save reverted the applied low-contrast palette")
	}
	if a.resolveAppearanceTheme(false).Palette.Theme.Text != ui.Hex("#FFFFFF") {
		t.Fatal("low contrast caused automatic recoloring")
	}
	a.store.path = filepath.Join(t.TempDir(), "contacts.json")
	a.setAppearanceTheme(appearanceLight, "low-contrast")
	if a.appearanceError != "" || a.runtimeErr != "unrelated Runtime error" || ct.Draft != "draft-keep" || ct.ActiveTaskID != "task-keep" || ct.SessionID != "session-keep" {
		t.Fatalf("retry changed unrelated state: %+v %+v", a, ct)
	}
	reloaded, err := openContactStore(a.store.path, "/ignored")
	if err != nil || reloaded.state.ThemeIDs == nil || reloaded.state.ThemeIDs.Light != "low-contrast" {
		t.Fatalf("retry did not persist theme preference: %+v %v", reloaded, err)
	}
	palette := a.resolveAppearanceTheme(false).Palette
	base := resolveWorkbenchTheme(false).Theme
	if palette.Theme.FontSize != base.FontSize || palette.Theme.Radius != base.Radius || palette.Theme.Spacing != base.Spacing || palette.Theme.Font != base.Font {
		t.Fatal("custom colors changed typography or spacing")
	}
	palette.Theme.Text = ui.Hex("#010203")
	if reflect.DeepEqual(palette.Theme, a.resolveAppearanceTheme(false).Palette.Theme) {
		t.Fatal("a rendered frame mutated the catalog palette")
	}
}
