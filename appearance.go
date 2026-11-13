package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"

	"github.com/HeimaoLST/Nagi/locales"
)

const (
	appearanceSystem = "system"
	appearanceLight  = "light"
	appearanceDark   = "dark"
)

type appearanceThemeResolution struct {
	Palette                  workbenchTheme
	RequestedID, EffectiveID string
	Unavailable              bool
	Warnings                 []themeContrastWarning
}

func appearanceThemeDirectory(directory string) (string, error) {
	if directory != "" {
		return directory, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "Saki", "themes"), nil
}

func (a *appModel) reloadAppearanceThemes() {
	directory, err := appearanceThemeDirectory(a.appearanceThemes.Directory)
	if err != nil {
		a.appearanceThemes = customThemeCatalog{Errors: []themeFileError{{Field: "$", Message: err.Error()}}}
		return
	}
	a.appearanceThemes = loadCustomThemes(directory)
}

type appearanceThemeImport struct {
	Busy            bool
	Problem         *themeFileError
	ImportedName    string
	focus           bool
	installInFlight bool
}

func (a *appModel) themeImportCurrent(state *appearanceThemeImport, generation uint64, owner *contact) bool {
	return !a.shutdownStarted && a.themeImport == state && a.folderChoiceGeneration == generation && a.settingsOpen && a.settingsTab == settingsTabAppearance && a.selectedContact() == owner
}

func (a *appModel) importAppearanceTheme() {
	if a.dataRecoveryBlocksChanges() || a.shutdownStarted || !a.settingsOpen || a.settingsTab != settingsTabAppearance || (a.themeImport != nil && a.themeImport.Busy) {
		return
	}
	if a.themeImport == nil {
		a.themeImport = &appearanceThemeImport{}
	}
	state := a.themeImport
	state.Busy = true
	generation, owner := a.folderChoiceGeneration, a.selectedContact()
	directory := a.appearanceThemes.Directory
	pick := a.chooseThemeFile
	if pick == nil {
		pick = mygo.Dialog.Open
	}
	msgs := locales.In(a.uiLocale)
	opts := mygo.OpenDialogOptions{Parent: a.win, Title: msgs.ThemeImportTitle(), ButtonLabel: msgs.ThemeImportAction(), Filters: []mygo.FileFilter{{Name: msgs.ThemeImportFilter(), Extensions: []string{"json"}}}}
	go func() {
		home, _ := os.UserHomeDir()
		opts.DefaultPath = home
		if downloads := filepath.Join(home, "Downloads"); home != "" {
			if info, err := os.Stat(downloads); err == nil && info.IsDir() {
				opts.DefaultPath = downloads
			}
		}
		paths, err := pick(opts)
		a.updateUI(func() {
			if a.shutdownStarted {
				return
			}
			state.Busy = false
			if !a.themeImportCurrent(state, generation, owner) {
				return
			}
			state.focus = true
			if err != nil {
				state.Problem = &themeFileError{Field: "$", Message: err.Error()}
				state.ImportedName = ""
				return
			}
			if len(paths) == 0 {
				return
			}
			state.Busy = true
			state.focus = false
			state.installInFlight = true
			source := paths[0]
			go func() {
				target, err := appearanceThemeDirectory(directory)
				var theme customTheme
				var problem *themeFileError
				if err != nil {
					problem = &themeFileError{Field: "$", Message: err.Error()}
				} else {
					theme, problem = installCustomTheme(source, target)
				}
				var catalog customThemeCatalog
				if problem == nil {
					catalog = loadCustomThemes(target)
				}
				a.updateUI(func() {
					state.installInFlight = false
					if a.shutdownStarted {
						a.finishShutdown()
						return
					}
					state.Busy = false
					if !a.themeImportCurrent(state, generation, owner) {
						return
					}
					state.focus = true
					state.Problem = problem
					if problem != nil {
						state.ImportedName = ""
						return
					}
					a.appearanceThemes = catalog
					state.ImportedName = theme.Name
				})
			}()
		})
	}()
}

func (a *appModel) resolveAppearanceTheme(dark bool) appearanceThemeResolution {
	kind, builtin := appearanceLight, "paper"
	if dark {
		kind, builtin = appearanceDark, "night"
	}
	id := a.appearanceThemeIDs.forKind(kind)
	result := appearanceThemeResolution{Palette: resolveWorkbenchTheme(dark), RequestedID: id, EffectiveID: builtin}
	if id == builtin {
		return result
	}
	for _, theme := range a.appearanceThemes.Themes {
		if theme.ID == id && theme.Kind == kind {
			palette := theme.Palette
			controls := *palette.Theme
			palette.Theme = &controls
			result.Palette, result.EffectiveID, result.Warnings = palette, id, theme.Warnings
			return result
		}
	}
	result.Unavailable = true
	return result
}

func (a *appModel) setAppearanceTheme(kind, id string) {
	ids := a.appearanceThemeIDs
	if kind == appearanceDark {
		ids.Dark = id
	} else {
		ids.Light = id
	}
	previous := a.appearanceThemeIDs
	a.appearanceThemeIDs = ids
	if a.resolveAppearanceTheme(kind == appearanceDark).Unavailable {
		a.appearanceThemeIDs = previous
		return
	}
	if a.demo {
		a.appearanceStatus = appearanceStatusPreview
		return
	}
	a.saveAppearance()
}

// Appearance feedback is stored by kind, never as a rendered sentence, so a
// language switch re-localizes feedback that is already on screen.
const (
	appearanceStatusNone       = ""
	appearanceStatusApplied    = "applied"
	appearanceStatusPreview    = "preview"
	appearanceStatusSaved      = "saved"
	appearanceStatusSaveFailed = "save-failed"
)

// normalizeAppearanceMode treats every absent or unknown stored value as the
// legacy system-following default. It never writes a new preference file.
func normalizeAppearanceMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case appearanceLight:
		return appearanceLight
	case appearanceDark:
		return appearanceDark
	default:
		return appearanceSystem
	}
}

func resolveAppearanceDark(mode string, systemDark bool) bool {
	switch normalizeAppearanceMode(mode) {
	case appearanceLight:
		return false
	case appearanceDark:
		return true
	default:
		return systemDark
	}
}

func appearanceModeIndex(mode string) int {
	switch normalizeAppearanceMode(mode) {
	case appearanceLight:
		return 1
	case appearanceDark:
		return 2
	default:
		return 0
	}
}

func appearanceModeFromIndex(index int) string {
	switch index {
	case 1:
		return appearanceLight
	case 2:
		return appearanceDark
	default:
		return appearanceSystem
	}
}

func (a *appModel) openAppearance() {
	a.appearanceStatus = appearanceStatusNone
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
}

// setAppearanceMode applies the mode immediately, then persists it in the same
// existing GUI contact store. A failed write keeps the applied mode and reports
// the gap explicitly; it never falls back to the previous page or a new prefs
// file.
func (a *appModel) setAppearanceMode(mode string) {
	mode = normalizeAppearanceMode(mode)
	if a.appearanceMode == mode {
		if a.appearanceError != "" {
			a.saveAppearance()
		}
		return
	}
	a.appearanceMode = mode
	a.appearanceStatus = appearanceStatusApplied
	a.appearanceError = ""
	if a.demo {
		a.appearanceStatus = appearanceStatusPreview
		return
	}
	a.saveAppearance()
}

// saveAppearance uses the one existing GUI contact store. If the first launch
// has no store yet, it lazily writes the current contacts (including draft and
// ID) through persist; it does not start Runtime or create a preference file.
func (a *appModel) saveAppearance() {
	a.appearanceStatus = appearanceStatusNone
	a.appearanceError = ""
	if err := a.persist(); err != nil {
		a.appearanceError = appearanceStatusSaveFailed
		return
	}
	a.appearanceStatus = appearanceStatusSaved
}

func appearanceStatusLabel(msgs locales.Messages, status string) string {
	switch status {
	case appearanceStatusApplied:
		return msgs.AppearanceStatusApplied()
	case appearanceStatusPreview:
		return msgs.AppearanceStatusPreview()
	case appearanceStatusSaved:
		return msgs.AppearanceStatusSaved()
	case appearanceStatusSaveFailed:
		return msgs.AppearanceStatusSaveFailed()
	default:
		return ""
	}
}

func (a *appModel) connectionDefaultModel() string {
	for _, config := range []*connectionConfig{a.connectionEffective, a.connectionSaved} {
		if config != nil && strings.TrimSpace(config.Model) != "" {
			return strings.TrimSpace(config.Model)
		}
	}
	return ""
}

func (a *appModel) conversationModelDisplayParts(msgs locales.Messages, ct *contact) (head, tail string) {
	full := ""
	if ct != nil {
		if model := strings.TrimSpace(ct.SessionModel); model != "" {
			full = model
		} else if ct.SessionID != "" {
			return msgs.ModelNotLoaded(), ""
		}
	}
	if full == "" {
		full = a.connectionDefaultModel()
	}
	if full == "" {
		return msgs.ModelNotSet(), ""
	}
	return splitModelTail(full, 8)
}

func splitModelTail(full string, tailRunes int) (head, tail string) {
	runes := []rune(full)
	if tailRunes < 1 || len(runes) <= tailRunes*2 {
		return full, ""
	}
	return string(runes[:len(runes)-tailRunes]), string(runes[len(runes)-tailRunes:])
}

func (a *appModel) conversationModelSummary(msgs locales.Messages, ct *contact) string {
	if ct == nil {
		return msgs.ConnectionDefaultNotConfigured()
	}
	if ct.SessionID != "" {
		if strings.TrimSpace(ct.SessionModel) != "" {
			return msgs.SessionModelSummary(strings.TrimSpace(ct.SessionModel))
		}
		return msgs.SessionModelNotLoaded()
	}
	if model := a.connectionDefaultModel(); model != "" {
		return msgs.ConnectionDefaultModel(model)
	}
	return msgs.ConnectionDefaultNotConfigured()
}
