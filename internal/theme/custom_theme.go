package theme

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

type AppearanceThemeIDs struct {
	Light string `json:"light,omitempty"`
	Dark  string `json:"dark,omitempty"`
}

func (ids AppearanceThemeIDs) ForKind(kind string) string {
	if kind == AppearanceDark {
		if ids.Dark != "" {
			return ids.Dark
		}
		return "night"
	}
	if ids.Light != "" {
		return ids.Light
	}
	return "paper"
}

type ThemeFileError struct {
	File, Field, Message string
}

type ThemeContrastWarning struct {
	Foreground, Background string
	Ratio, Minimum         float64
}

type CustomTheme struct {
	ID, Name, Kind, File string
	Palette              WorkbenchTheme
	Warnings             []ThemeContrastWarning
}

// CustomThemeCatalog collects validated custom themes and file diagnostics;
// filesByID tracks validated claims including excluded duplicate ID groups.
type CustomThemeCatalog struct {
	Directory string
	Themes    []CustomTheme
	Errors    []ThemeFileError
	filesByID map[string][]string
}

type customThemeFile struct {
	Version int                        `json:"version"`
	ID      string                     `json:"id"`
	Name    string                     `json:"name"`
	Kind    string                     `json:"kind"`
	Colors  map[string]json.RawMessage `json:"colors"`
}

type ColorField struct {
	Name, Fallback string
	Target         func(*WorkbenchTheme) *ui.Color
}

var themeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
var themeColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

var ColorFields = []ColorField{
	{"background", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.Background }},
	{"surface", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.Surface }},
	{"surface_hover", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.SurfaceHover }},
	{"surface_pressed", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.SurfacePressed }},
	{"border", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.Border }},
	{"text", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.Text }},
	{"text_muted", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.TextMuted }},
	{"accent", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.Accent }},
	{"accent_hover", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.AccentHover }},
	{"accent_pressed", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.AccentPressed }},
	{"accent_text", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.AccentText }},
	{"selection", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.Selection }},
	{"focus", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.Focus }},
	{"danger", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.Danger }},
	{"warning", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.Warning }},
	{"success", "", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.Success }},
	{"rail", "", func(t *WorkbenchTheme) *ui.Color { return &t.Rail }},
	{"panel", "", func(t *WorkbenchTheme) *ui.Color { return &t.Panel }},
	{"bubble_user", "", func(t *WorkbenchTheme) *ui.Color { return &t.BubbleUser }},
	{"bubble_contact", "", func(t *WorkbenchTheme) *ui.Color { return &t.BubbleContact }},
	{"scrollbar", "text_muted", func(t *WorkbenchTheme) *ui.Color { return &t.Theme.Scrollbar }},
	{"reading", "background", func(t *WorkbenchTheme) *ui.Color { return &t.Reading }},
	{"well", "surface", func(t *WorkbenchTheme) *ui.Color { return &t.Well }},
	{"work_surface", "panel", func(t *WorkbenchTheme) *ui.Color { return &t.WorkSurface }},
	{"work_text", "text", func(t *WorkbenchTheme) *ui.Color { return &t.WorkText }},
	{"work_muted", "text_muted", func(t *WorkbenchTheme) *ui.Color { return &t.WorkMuted }},
	{"work_attention", "warning", func(t *WorkbenchTheme) *ui.Color { return &t.WorkAttention }},
	{"work_warning_bg", "well", func(t *WorkbenchTheme) *ui.Color { return &t.WorkWarningBg }},
}

func decodeCustomTheme(file string, data []byte) (CustomTheme, *ThemeFileError) {
	fail := func(field, message string) (CustomTheme, *ThemeFileError) {
		return CustomTheme{}, &ThemeFileError{File: file, Field: field, Message: message}
	}
	if !utf8.Valid(data) {
		return fail("$", "expected UTF-8 JSON")
	}
	var input customThemeFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		field := "$"
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field != "" {
			field = typeErr.Field
		} else if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			field, _ = strconv.Unquote(name)
		}
		return fail(field, err.Error())
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fail("$", "expected exactly one JSON object")
	}
	if input.Version != 1 {
		return fail("version", "expected version 1")
	}
	if !themeIDPattern.MatchString(input.ID) {
		return fail("id", "expected a lowercase kebab-case ID starting with a letter")
	}
	if input.ID == "paper" || input.ID == "night" {
		return fail("id", "ID is reserved for a built-in theme")
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return fail("name", "expected a nonempty display name")
	}
	if input.Kind != AppearanceLight && input.Kind != AppearanceDark {
		return fail("kind", "expected light or dark")
	}
	if input.Colors == nil {
		return fail("colors", "expected a semantic color object")
	}
	known := make(map[string]bool, len(ColorFields))
	for _, field := range ColorFields {
		known[field.Name] = true
	}
	keys := make([]string, 0, len(input.Colors))
	for name := range input.Colors {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if !known[name] {
			return fail("colors."+name, "unknown semantic color")
		}
	}
	palette := ResolveWorkbenchTheme(input.Kind == AppearanceDark)
	colors := make(map[string]ui.Color, len(ColorFields))
	for _, field := range ColorFields {
		raw, supplied := input.Colors[field.Name]
		var color ui.Color
		if supplied {
			var value string
			if json.Unmarshal(raw, &value) != nil || !themeColorPattern.MatchString(value) {
				return fail("colors."+field.Name, "expected an opaque #RRGGBB color")
			}
			color = ui.Hex(value)
		} else if field.Fallback != "" {
			color = colors[field.Fallback]
		} else {
			return fail("colors."+field.Name, "required semantic color is missing")
		}
		colors[field.Name] = color
		*field.Target(&palette) = color
	}
	if palette.BubbleContact == palette.Reading {
		palette.BubbleContact = palette.Reading.Mix(highContrastInk(palette.Reading), .06)
	}
	ink := highContrastInk(palette.BubbleContact)
	palette.ContactText = contrastTextColor(palette.Theme.Text, ink, palette.BubbleContact)
	palette.ContactMuted = contrastTextColor(palette.Theme.TextMuted, ink, palette.BubbleContact)
	palette.ContactLink = contrastTextColor(palette.Link, ink, palette.BubbleContact)
	t := palette.Theme
	shade := highContrastInk(t.Text)
	palette.ComposerSurface = contrastTextColor(palette.Reading.Mix(t.Text, .04), shade, t.Text)
	palette.ComposerChipBg = contrastTextColor(palette.ComposerSurface.Mix(t.Text, .06), shade, t.Text)
	palette.ComposerBorder = t.Border
	palette.ComposerText = t.Text
	palette.ComposerMuted = contrastTextColor(t.TextMuted, t.Text, palette.ComposerSurface, palette.ComposerChipBg)
	return CustomTheme{ID: input.ID, Name: input.Name, Kind: input.Kind, File: file, Palette: palette, Warnings: ThemeContrastWarnings(palette)}, nil
}

func highContrastInk(background ui.Color) ui.Color {
	black := ui.RGB(0, 0, 0)
	if PaletteContrast(black, background) >= 4.5 {
		return black
	}
	return ui.RGB(255, 255, 255)
}

func LoadCustomThemeCatalog(directory string) CustomThemeCatalog {
	catalog := CustomThemeCatalog{Directory: directory}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return catalog
	}
	if err != nil {
		catalog.Errors = append(catalog.Errors, ThemeFileError{File: directory, Field: "$", Message: err.Error()})
		return catalog
	}
	var candidates []CustomTheme
	catalog.filesByID = make(map[string][]string)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		file := filepath.Join(directory, entry.Name())
		data, err := os.ReadFile(file)
		if err != nil {
			catalog.Errors = append(catalog.Errors, ThemeFileError{File: file, Field: "$", Message: err.Error()})
			continue
		}
		theme, problem := decodeCustomTheme(file, data)
		if problem != nil {
			catalog.Errors = append(catalog.Errors, *problem)
			continue
		}
		candidates = append(candidates, theme)
		catalog.filesByID[theme.ID] = append(catalog.filesByID[theme.ID], file)
	}
	for _, theme := range candidates {
		if peers := catalog.filesByID[theme.ID]; len(peers) > 1 {
			catalog.Errors = append(catalog.Errors, ThemeFileError{File: theme.File, Field: "id", Message: fmt.Sprintf("duplicate ID %q in %s", theme.ID, strings.Join(peers, ", "))})
			continue
		}
		catalog.Themes = append(catalog.Themes, theme)
	}
	sort.Slice(catalog.Themes, func(i, j int) bool {
		a, b := catalog.Themes[i], catalog.Themes[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	sort.SliceStable(catalog.Errors, func(i, j int) bool { return catalog.Errors[i].File < catalog.Errors[j].File })
	return catalog
}

// ImportCustomThemeFile copies a validated .json file without replacing an existing
// filename or ID. The caller reloads its catalog and owns appearance selection.
func ImportCustomThemeFile(sourcePath, directory string) (CustomTheme, *ThemeFileError) {
	fail := func(file, field, message string) (CustomTheme, *ThemeFileError) {
		return CustomTheme{}, &ThemeFileError{File: file, Field: field, Message: message}
	}
	if filepath.Ext(sourcePath) != ".json" {
		return fail(sourcePath, "$", "expected a .json theme file")
	}
	if directory == "" {
		return fail(directory, "$", "theme directory is unavailable")
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return fail(sourcePath, "$", err.Error())
	}
	theme, problem := decodeCustomTheme(sourcePath, data)
	if problem != nil {
		return CustomTheme{}, problem
	}
	catalog := LoadCustomThemeCatalog(directory)
	for _, problem := range catalog.Errors {
		if problem.File == directory {
			return CustomTheme{}, &problem
		}
	}
	if peers := catalog.filesByID[theme.ID]; len(peers) != 0 {
		return fail(sourcePath, "id", fmt.Sprintf("ID %q is already defined in %s", theme.ID, strings.Join(peers, ", ")))
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fail(directory, "$", err.Error())
	}
	destination := filepath.Join(directory, filepath.Base(sourcePath))
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail(destination, "$", err.Error())
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		if removeErr := os.Remove(destination); removeErr != nil {
			err = fmt.Errorf("%v; remove incomplete theme: %w", err, removeErr)
		}
		return fail(destination, "$", err.Error())
	}
	theme.File = destination
	return theme, nil
}

func ThemeContrastWarnings(theme WorkbenchTheme) []ThemeContrastWarning {
	colors := make(map[string]ui.Color, len(ColorFields))
	for _, field := range ColorFields {
		colors[field.Name] = *field.Target(&theme)
	}
	var warnings []ThemeContrastWarning
	check := func(foreground, background string, minimum float64) {
		if ratio := PaletteContrast(colors[foreground], colors[background]); ratio < minimum {
			warnings = append(warnings, ThemeContrastWarning{Foreground: foreground, Background: background, Ratio: ratio, Minimum: minimum})
		}
	}
	for _, background := range []string{"reading", "panel", "well", "bubble_user", "bubble_contact", "surface_hover", "surface_pressed", "selection"} {
		check("text", background, 4.5)
		check("text_muted", background, 4.5)
	}
	for _, background := range []string{"accent", "accent_hover", "accent_pressed"} {
		check("accent_text", background, 4.5)
	}
	for _, background := range []string{"reading", "panel", "well", "selection"} {
		check("focus", background, 3)
		check("border", background, 3)
	}
	for _, foreground := range []string{"work_text", "work_muted", "work_attention"} {
		check(foreground, "work_surface", 4.5)
	}
	return warnings
}
