package main

import (
	"errors"
	"strings"

	"saki/desktop/locales"
)

const (
	appearanceSystem = "system"
	appearanceLight  = "light"
	appearanceDark   = "dark"
)

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
	if err := a.ensureAppearanceStore(); err != nil {
		a.appearanceError = appearanceStatusSaveFailed
		return
	}
	if err := a.persist(); err != nil {
		a.appearanceError = appearanceStatusSaveFailed
		return
	}
	a.appearanceStatus = appearanceStatusSaved
}

func (a *appModel) ensureAppearanceStore() error {
	if a.store != nil {
		return nil
	}
	if !a.storeInitAllowed || strings.TrimSpace(a.storePath) == "" {
		return errors.New("the local contact store is unavailable")
	}
	if len(a.contacts) == 0 {
		return errors.New("there is no current contact to save")
	}
	store := &contactStore{path: a.storePath}
	store.setIssueToolState(a.issueToolLedger, a.issueToolScopes)
	a.store = store
	return nil
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
