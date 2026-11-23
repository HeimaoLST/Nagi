package connection

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/markdown"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

type Draft struct {
	API, Endpoint, APIKey, Model                         string
	ContextTokens, OutputTokens, WorkingDirectory, Slots string
}

type Settings struct {
	Draft               Draft
	Initial             Draft
	Errors              map[string]string
	Status              string
	StatusArgs          []string
	Error               string
	ErrorDetail         string
	ReloadBlocked       string
	Busy                bool
	HasSaved            bool
	CredentialPresent   bool
	SavedPending        bool
	CurrentSummary      string
	LimitsOpen          bool
	RecoveryDetailsOpen bool
	FocusField          string
	FocusFolder         bool
	FolderError         string
	EditOpen            map[string]bool
	FocusEdit           string
	customTokens        bool
	customSlots         bool
}

type SettingsActions struct {
	Save         func(Config)
	SaveAndApply func(Config)
	Apply        func()
	Test         func(Config)
	Discard      func()
	ChooseFolder func()
}

func NewSettings(config *Config, workDir string) *Settings {
	initial := DefaultConfig(workDir)
	if config != nil {
		initial = *config
	}
	s := &Settings{Draft: Draft{
		API: initial.API, Endpoint: initial.Endpoint, APIKey: initial.APIKey, Model: initial.Model,
		ContextTokens: strconv.Itoa(initial.ContextTokens), OutputTokens: strconv.Itoa(initial.OutputTokens),
		WorkingDirectory: initial.WorkingDirectory, Slots: strconv.Itoa(initial.MaxActiveTasks),
	}, HasSaved: config != nil, CredentialPresent: strings.TrimSpace(initial.APIKey) != "", EditOpen: map[string]bool{}}
	if config == nil {
		s.FocusField = "Endpoint"
	}
	s.Initial = s.Draft
	return s
}

func (s *Settings) Dirty() bool {
	return s != nil && s.Draft != s.Initial
}

func (s *Settings) ResetDraft() {
	if s == nil {
		return
	}
	s.Draft = s.Initial
	s.Errors = nil
	s.Error = ""
	s.ErrorDetail = ""
	s.Status = ""
	s.StatusArgs = nil
	s.ReloadBlocked = ""
	s.FocusField = "Endpoint"
	s.FolderError = ""
	s.RecoveryDetailsOpen = false
	s.customTokens = false
	s.customSlots = false
}

func (s *Settings) Validated() (Config, bool) {
	d := s.Draft
	config := Config{API: d.API, Endpoint: strings.TrimSpace(d.Endpoint),
		APIKey: d.APIKey, Model: strings.TrimSpace(d.Model), WorkingDirectory: strings.TrimSpace(d.WorkingDirectory)}
	numericErrors := map[string]string{}
	for _, f := range []struct {
		key, value string
		target     *int
	}{
		{"ContextTokens", d.ContextTokens, &config.ContextTokens},
		{"OutputTokens", d.OutputTokens, &config.OutputTokens},
		{"MaxActiveTasks", d.Slots, &config.MaxActiveTasks},
	} {
		value, err := strconv.Atoi(strings.TrimSpace(f.value))
		if err != nil || value < 1 {
			numericErrors[f.key] = ValidationNumeric
		}
		*f.target = value
	}
	s.Errors = ValidateConfig(config)
	if s.Errors == nil {
		s.Errors = map[string]string{}
	}
	if s.CredentialPresent && s.Initial.APIKey == "" && strings.TrimSpace(config.APIKey) == "" {
		config.APIKey = ""
		delete(s.Errors, "APIKey")
	}
	for key, message := range numericErrors {
		s.Errors[key] = message
	}
	for _, key := range []string{"Endpoint", "APIKey", "Model", "WorkingDirectory"} {
		if s.Errors[key] != "" {
			s.EditOpen[key] = true
		}
	}
	if s.Errors["ContextTokens"] != "" || s.Errors["OutputTokens"] != "" || s.Errors["MaxActiveTasks"] != "" {
		s.LimitsOpen = true
	}
	s.Error = ""
	s.ErrorDetail = ""
	s.RecoveryDetailsOpen = false
	if len(s.Errors) > 0 {
		s.Error = CheckFields
		for _, key := range []string{"API", "Endpoint", "APIKey", "Model", "ContextTokens", "OutputTokens", "WorkingDirectory", "MaxActiveTasks"} {
			if s.Errors[key] != "" {
				s.FocusField = key
				break
			}
		}
		return config, false
	}
	return config, true
}

// View renders the connection form. The host owns persistence, process lifetime
// and UI-thread updates; rendering and editing never starts a model request or
// writes configuration.
func (s *Settings) View(c *ui.Context, t *ui.Theme, actions SettingsActions, inputBorder ...ui.Color) {
	border := c.Theme().Border
	if len(inputBorder) > 0 {
		border = inputBorder[0]
	}
	msgs := locales.In(c.Locale())
	if s.FocusField != "" {
		s.EditOpen[s.FocusField] = true
	}
	ui.Column(c).Key("connection-form").FillWidth().AlignItems(ui.Start).Gap(20).Children(func() {
		s.statusCard(c, t, msgs, actions.Test)
		if s.Error != "" || s.ErrorDetail != "" {
			summary := ErrorText(msgs, s.Error, "")
			detail := s.ErrorDetail
			if detail == "" && !ErrorIsKind(s.Error) && s.Error != CheckFields {
				detail = s.Error
			}
			if s.Error == CheckFields {
				summary = msgs.ConnectionCheckFields()
				detail = ""
			}
			if !ErrorIsKind(s.Error) && s.Error != CheckFields {
				summary = msgs.RecoveryConnectionSaveFailed()
			}
			if s.Draft.APIKey != "" {
				detail = strings.ReplaceAll(detail, s.Draft.APIKey, "[redacted]")
			}
			markdown.RecoveryNotice(c, t, summary, detail, s.RecoveryDetailsOpen, func() { s.RecoveryDetailsOpen = !s.RecoveryDetailsOpen }, nil, "")
		} else {
			s.RecoveryDetailsOpen = false
		}
		c.Key("connection-credentials")
		theme.PreferenceSection(c, t, msgs.ConnectionCredentialsSection(), func() {
			protocols := [...]string{APIChatCompletions, APIResponses}
			selected := slices.Index(protocols[:], s.Draft.API)
			theme.SettingsPreferenceRow(c, t, msgs.ProtocolLabel(), "", func() {
				c.Key("connection-protocol")
				focus := s.FocusField == "API" && !s.Busy
				protocol := theme.PreferenceSegmentedSelection(c, &selected, msgs.ProtocolLabel(), s.Busy, focus, msgs.ProtocolChatCompletionsLabel(), msgs.ProtocolResponsesLabel())
				protocol.OnChange(func() {
					s.Draft.API = protocols[selected]
					delete(s.Errors, "API")
				})
				if focus {
					s.FocusField = ""
				}
				if message := s.Errors["API"]; message != "" {
					ui.Text(c, FieldErrorText(msgs, "API", message)).FontSize(theme.TypeMeta).LineHeight(1.5).TextColor(t.Danger)
				}
			})
			s.editableField(c, t, "Endpoint", msgs.ConnectionFieldEndpoint(), msgs.ConnectionEndpointPlaceholder(), &s.Draft.Endpoint, false, msgs.ConnectionEndpointHint(), border, false)
			s.editableField(c, t, "APIKey", msgs.ConnectionFieldAPIKey(), msgs.ConnectionAPIKeyPlaceholder(), &s.Draft.APIKey, true, msgs.ConnectionAPIKeyHint(), border, true)
		})
		c.Key("connection-model-capacity")
		theme.PreferenceSection(c, t, msgs.ConnectionModelCapacitySection(), func() {
			s.editableField(c, t, "Model", msgs.ConnectionFieldModel(), msgs.ConnectionModelPlaceholder(), &s.Draft.Model, false, "", border, false)
			s.capacity(c, t, msgs, border)
		})
		c.Key("connection-directory")
		theme.PreferenceSection(c, t, msgs.ConnectionDirectorySection(), func() {
			s.workingDirectory(c, t, msgs, actions.ChooseFolder, border)
		})
		ui.Column(c).Key("connection-actions").FillWidth().Gap(8).Children(func() {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				applySaved := s.SavedPending && !s.Dirty()
				if applySaved {
					theme.PrimaryButton(c, msgs.ConnectionApplySaved()).Disabled(s.Busy || s.ReloadBlocked != "").OnClick(actions.Apply)
				} else {
					theme.PrimaryButton(c, msgs.ConnectionSaveAndApply()).Disabled(s.Busy).OnClick(func() {
						if config, ok := s.Validated(); ok && actions.SaveAndApply != nil {
							actions.SaveAndApply(config)
						}
					})
				}
				theme.SecondaryButton(c, msgs.ConnectionSaveConfiguration()).Disabled(s.Busy).OnClick(func() {
					if config, ok := s.Validated(); ok && actions.Save != nil {
						actions.Save(config)
					}
				})
				if !applySaved && s.HasSaved && s.SavedPending {
					theme.SecondaryButton(c, msgs.ConnectionApplySaved()).Disabled(s.Busy || s.ReloadBlocked != "").OnClick(actions.Apply)
				}
				if s.Dirty() && actions.Discard != nil {
					theme.GhostButton(c, msgs.ConnectionDiscard()).Disabled(s.Busy).OnClick(actions.Discard)
				}
			})
			if s.ReloadBlocked != "" {
				ui.Text(c, BlockedText(msgs, s.ReloadBlocked)).FontSize(theme.TypeMeta).LineHeight(1.5).TextColor(t.Warning)
			}
			if s.Status != "" {
				ui.Text(c, StatusText(msgs, s.Status, s.StatusArgs)).FontSize(theme.TypeMeta).LineHeight(1.5).TextColor(t.Text)
			}
			if s.SavedPending && s.Dirty() {
				ui.Text(c, msgs.ConnectionSavedAwaitingApply()).FontSize(theme.TypeMeta).LineHeight(1.5).TextColor(t.Warning)
			}
		})
	})
}

func (s *Settings) statusCard(c *ui.Context, t *ui.Theme, msgs locales.Messages, test func(Config)) {
	label, summary, dot := msgs.ConnectionStatusUnconfigured(), msgs.ConnectionNoneApplied(), t.TextMuted
	if s.CurrentSummary != "" {
		label, summary, dot = msgs.ConnectionStatusInUse(), msgs.ConnectionCurrent(s.CurrentSummary), t.Success
	}
	switch {
	case s.Dirty():
		label, dot = msgs.ConnectionUnsavedForm(), t.Warning
	case s.SavedPending:
		label, dot = msgs.ConnectionSavedAwaitingApply(), t.Warning
	}
	c.Key("connection-status")
	theme.PreferenceGroup(c, t, func() {
		ui.Row(c).FillWidth().MinWidth(0).Padding(8, 0).Gap(16).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Box(c).Size(8, 8).Shrink(0).Radius(4).Background(dot)
					ui.Text(c, label).FontSize(theme.TypeLabel).FontWeight(500).LineHeight(1.4).TextColor(t.Text).MinWidth(0)
				})
				text := ui.Text(c, summary).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
				theme.AppTooltip(c, text, summary)
				if s.CurrentSummary != "" && s.Error != "" && s.Error != CheckFields {
					ui.Text(c, msgs.ConnectionKeptOnFailure()).FontSize(theme.TypeMeta).LineHeight(1.5).TextColor(t.Warning)
				}
			})
			button := theme.SecondaryButton(c, msgs.ConnectionTestAction()).Shrink(0).Disabled(s.Busy).OnClick(func() {
				if config, ok := s.Validated(); ok && test != nil {
					test(config)
				}
			})
			theme.AppTooltip(c, button, msgs.ConnectionTestHint())
		})
	})
}

func compactTokens(raw string) string {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err == nil && value >= 1024 && value%1024 == 0 {
		return strconv.Itoa(value/1024) + "K"
	}
	return raw
}

func (s *Settings) capacity(c *ui.Context, t *ui.Theme, msgs locales.Messages, border ui.Color) {
	ui.Column(c).Key("connection-limits").FillWidth().Gap(4).Children(func() {
		theme.SettingsPreferenceRow(c, t, msgs.ConnectionCapacityShow(), "", func() {
			ui.Row(c).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, msgs.ConnectionCapacitySummary(compactTokens(s.Draft.ContextTokens), compactTokens(s.Draft.OutputTokens), s.Draft.Slots)).
					FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
				label, hint := msgs.SettingsShowRuntimeDetails(), msgs.ConnectionCapacityShowTooltip()
				if s.LimitsOpen {
					label, hint = msgs.SettingsHideRuntimeDetails(), msgs.ConnectionCapacityHideTooltip()
				}
				c.Key("limits-disclosure")
				theme.AppTooltip(c, theme.LinkButton(c, label), hint).OnClick(func() {
					s.LimitsOpen = !s.LimitsOpen
					if !s.LimitsOpen {
						s.customSlots = false
					}
				})
			})
		}, !s.LimitsOpen)
		if !s.LimitsOpen {
			for _, key := range [...]string{"ContextTokens", "OutputTokens", "MaxActiveTasks"} {
				if message := s.Errors[key]; message != "" {
					ui.Text(c, FieldErrorText(msgs, key, message)).FontSize(theme.TypeMeta).LineHeight(1.5).TextColor(t.Danger)
				}
			}
			return
		}
		s.tokenLimits(c, t, msgs, border)
		slots, _ := strconv.Atoi(strings.TrimSpace(s.Draft.Slots))
		if s.customSlots || slots < 1 || slots > 4 {
			s.customSlots = true
			s.fieldRow(c, t, "MaxActiveTasks", msgs.ConnectionFieldMaxActiveTasks(), "2", &s.Draft.Slots, false, "", border, true)
			return
		}
		selected := slots - 1
		theme.SettingsPreferenceRow(c, t, msgs.ConnectionFieldMaxActiveTasks(), "", func() {
			c.Key("active-task-presets")
			theme.PreferenceSegmentedSelection(c, &selected, msgs.ConnectionFieldMaxActiveTasks(), s.Busy, false, "1", "2", "3", "4").OnChange(func() {
				s.Draft.Slots = strconv.Itoa(selected + 1)
				delete(s.Errors, "MaxActiveTasks")
			})
		}, true)
	})
}

func (s *Settings) tokenLimits(c *ui.Context, t *ui.Theme, msgs locales.Messages, border ui.Color) {
	defaults := DefaultConfig("")
	presets := [...]struct {
		id              string
		context, output int
	}{
		{"default", defaults.ContextTokens, defaults.OutputTokens},
		{"256k", 256 * 1024, 16 * 1024},
		{"512k", 512 * 1024, 32 * 1024},
	}
	contextTokens, _ := strconv.Atoi(strings.TrimSpace(s.Draft.ContextTokens))
	outputTokens, _ := strconv.Atoi(strings.TrimSpace(s.Draft.OutputTokens))
	selected := "custom"
	options := make([]theme.SelectOption, 0, len(presets)+1)
	for _, preset := range presets {
		label := fmt.Sprintf("%dK / %dK", preset.context/1024, preset.output/1024)
		if preset.id == "default" {
			label = msgs.ConnectionTokenPresetDefault(label)
		}
		options = append(options, theme.SelectOption{Value: preset.id, Label: label})
		if !s.customTokens && contextTokens == preset.context && outputTokens == preset.output {
			selected = preset.id
		}
	}
	options = append(options, theme.SelectOption{Value: "custom", Label: msgs.ConnectionTokenPresetCustom()})
	theme.SettingsPreferenceRow(c, t, msgs.ConnectionTokenLimits(), "", func() {
		c.Key("token-limit-presets")
		preset := theme.PreferenceSelect(c, &selected, options, msgs.ConnectionTokenLimits(), s.Busy)
		if preset.Changed() {
			s.customTokens = selected == "custom"
			for _, choice := range presets {
				if choice.id == selected {
					s.Draft.ContextTokens = strconv.Itoa(choice.context)
					s.Draft.OutputTokens = strconv.Itoa(choice.output)
					delete(s.Errors, "ContextTokens")
					delete(s.Errors, "OutputTokens")
				}
			}
		}
	})
	if selected == "custom" {
		s.customTokens = true
		s.fieldRow(c, t, "ContextTokens", msgs.ConnectionFieldContextTokens(), "131072", &s.Draft.ContextTokens, false, "", border, false)
		s.fieldRow(c, t, "OutputTokens", msgs.ConnectionFieldOutputTokens(), "8192", &s.Draft.OutputTokens, false, "", border, false)
	}
}

func (s *Settings) workingDirectory(c *ui.Context, t *ui.Theme, msgs locales.Messages, choose func(), border ui.Color) {
	ui.Column(c).Key("connection-folder").FillWidth().Gap(4).Children(func() {
		path := strings.TrimSpace(s.Draft.WorkingDirectory)
		if path == "" {
			path = msgs.ConnectionValueUnset()
		}
		theme.SettingsPreferenceRow(c, t, msgs.ConnectionFieldWorkingDirectory(), path, func() {
			ui.Row(c).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
				c.Key("choose-connection-folder")
				folder := theme.SecondaryButton(c, msgs.WorkFolderChoose()).Disabled(s.Busy).OnClick(choose)
				if strings.TrimSpace(s.Draft.WorkingDirectory) == "" {
					theme.AppTooltip(c, folder, msgs.WorkFolderRequiredHint())
				}
				if s.FocusFolder {
					folder.Focus()
					s.FocusFolder = false
				}
				label := msgs.ConnectionFolderEnterPath()
				if s.EditOpen["WorkingDirectory"] {
					label = msgs.ConnectionFolderHidePath()
				}
				c.Key("folder-path-disclosure")
				theme.LinkButton(c, label).Disabled(s.Busy).OnClick(func() {
					s.EditOpen["WorkingDirectory"] = !s.EditOpen["WorkingDirectory"]
					if s.EditOpen["WorkingDirectory"] {
						s.FocusField = "WorkingDirectory"
					}
				})
			})
			if s.EditOpen["WorkingDirectory"] {
				s.fieldInput(c, "WorkingDirectory", msgs.ConnectionFieldWorkingDirectory(), msgs.ConnectionWorkingDirectoryPlaceholder(), &s.Draft.WorkingDirectory, false, border).FillWidth()
			}
			s.fieldError(c, t, "WorkingDirectory")
		}, true)
		if s.FolderError != "" {
			ui.Text(c, msgs.WorkFolderChooseFailed(s.FolderError)).FontSize(theme.TypeMeta).LineHeight(1.5).TextColor(t.Danger)
		}
	})
}

func (s *Settings) editableField(c *ui.Context, t *ui.Theme, key, label, placeholder string, value *string, secret bool, helper string, border ui.Color, last bool) {
	c.Key("connection-editor-" + key)
	if !s.HasSaved {
		s.EditOpen[key] = true
		s.fieldRow(c, t, key, label, placeholder, value, secret, helper, border, last)
		return
	}
	msgs := locales.In(c.Locale())
	open := s.EditOpen[key]
	text := *value
	if secret && s.CredentialPresent && s.Initial.APIKey == "" {
		placeholder = msgs.ConnectionSavedAPIKeyPlaceholder()
	}
	if open {
		text = ""
	} else if secret && (text != "" || s.CredentialPresent) {
		text = "••••••••"
	} else if text == "" {
		text = msgs.ConnectionValueUnset()
	} else if key == "Endpoint" {
		text = markdown.SafeDiagnostic(text)
	}
	theme.SettingsPreferenceRow(c, t, label, text, func() {
		row := ui.Row(c).MinWidth(0).Gap(8).AlignItems(ui.Center)
		if open {
			row.FillWidth()
		}
		row.Children(func() {
			buttonText, accessible := msgs.ConnectionEditAction(), msgs.ConnectionEditField(label)
			if open {
				s.fieldInput(c, key, label, placeholder, value, secret, border).Grow(1).MinWidth(0)
				buttonText, accessible = msgs.ConnectionCloseEditorAction(), msgs.ConnectionCloseField(label)
			}
			c.Key("editor-disclosure")
			button := theme.SecondaryButton(c, buttonText).Shrink(0).Label(accessible).Disabled(s.Busy).OnClick(func() {
				s.EditOpen[key] = !open
				if !open {
					s.FocusField = key
				} else {
					s.FocusEdit = key
				}
			})
			if s.FocusEdit == key {
				button.Focus()
				s.FocusEdit = ""
			}
		})
		if open && helper != "" {
			ui.Text(c, helper).AlignSelf(ui.Stretch).FontSize(theme.TypeMeta).LineHeight(1.5).TextColor(t.TextMuted)
		}
		s.fieldError(c, t, key)
	}, last)
}

func (s *Settings) fieldRow(c *ui.Context, t *ui.Theme, key, label, placeholder string, value *string, secret bool, helper string, border ui.Color, last bool) {
	c.Key("connection-field-" + key)
	theme.SettingsPreferenceRow(c, t, label, helper, func() {
		s.fieldInput(c, key, label, placeholder, value, secret, border).FillWidth()
		s.fieldError(c, t, key)
	}, last)
}

func (s *Settings) fieldInput(c *ui.Context, key, label, placeholder string, value *string, secret bool, border ui.Color) ui.Element {
	c.Key("input-" + key)
	field := theme.PreferenceInput(c, value, s.Errors[key] != "", border).Label(label).Placeholder(placeholder).Disabled(s.Busy)
	if secret {
		field.Password()
	}
	if s.FocusField == key {
		field.Focus().ScrollIntoView()
		s.FocusField = ""
	}
	return field
}

func (s *Settings) fieldError(c *ui.Context, t *ui.Theme, key string) {
	if message := s.Errors[key]; message != "" {
		ui.Text(c, FieldErrorText(locales.In(c.Locale()), key, message)).AlignSelf(ui.Stretch).FontSize(theme.TypeMeta).LineHeight(1.5).TextColor(t.Danger)
	}
}

// Form feedback is stored by kind (and raw detail where one exists) so the
// current language says it again on every frame. Unknown text stays raw.
const (
	CheckFields       = "check-fields"
	ValidationNumeric = "numeric"

	StatusSaving           = "saving"
	StatusSavedApplied     = "saved-applied"
	StatusChecking         = "checking-saved"
	StatusApplied          = "applied"
	StatusTesting          = "testing"
	StatusTestCompleted    = "test-completed"
	StatusSavedStoreFailed = "saved-store-failed"
	ErrorTestFailed        = "test-failed"
	ErrorSavedNeedsRepair  = "saved-needs-repair"
	ErrorApplyConfig       = "apply-config"
	BlockedApplying        = "applying"
	BlockedShutdown        = "shutdown"
	BlockedNoSaved         = "no-saved"
	BlockedStore           = "store"
	BlockedActiveWork      = "active-work"
	BlockedUntrustedIssue  = "untrusted-issue"
)

func noticeArg(args []string, index int) string {
	if index < 0 || index >= len(args) {
		return ""
	}
	return args[index]
}

func FieldErrorText(msgs locales.Messages, key, kind string) string {
	switch kind {
	case CheckFields:
		return msgs.ConnectionCheckFields()
	case ValidationRequired:
		return msgs.ValidationRequired()
	case ValidationAPI:
		return msgs.ValidationAPI()
	case ValidationEndpointWhitespace:
		return msgs.ValidationEndpointWhitespace()
	case ValidationEndpointURL:
		return msgs.ValidationEndpointURL()
	case ValidationEndpointUserinfo:
		return msgs.ValidationEndpointUserinfo()
	case ValidationEndpointQuery:
		return msgs.ValidationEndpointQuery()
	case ValidationEndpointFragment:
		return msgs.ValidationEndpointFragment()
	case ValidationWorkspaceAbsolute:
		return msgs.ValidationWorkspaceAbsolutePath()
	case ValidationWorkspaceDirectory:
		return msgs.ValidationWorkspaceDirectoryExists()
	case ValidationPositive, ValidationContextGap, ValidationLessThanContext:
		if ValidationContextGap == kind {
			return msgs.ValidationContextGap()
		}
		if ValidationLessThanContext == kind {
			return msgs.ValidationLessThanContext()
		}
		return msgs.ValidationPositive()
	case ValidationNumeric:
		return msgs.ValidationNumeric()
	default:
		return kind
	}
}

func StatusText(msgs locales.Messages, kind string, args []string) string {
	switch kind {
	case StatusSaving:
		return msgs.ConnectionStatusSaving()
	case StatusSavedApplied:
		return msgs.ConnectionStatusSavedApplied()
	case StatusChecking:
		return msgs.ConnectionStatusCheckingSaved()
	case StatusApplied:
		return msgs.ConnectionStatusApplied()
	case StatusTesting:
		return msgs.ConnectionStatusTesting()
	case StatusTestCompleted:
		return msgs.ConnectionStatusTestCompleted(noticeArg(args, 0), noticeArg(args, 1), noticeArg(args, 2), noticeArg(args, 3))
	default:
		return kind
	}
}

func ErrorIsKind(kind string) bool {
	switch kind {
	case ErrorTestFailed, ErrorApplyConfig, StatusSavedStoreFailed, ErrorSavedNeedsRepair:
		return true
	default:
		return false
	}
}

func ErrorText(msgs locales.Messages, kind, detail string) string {
	switch kind {
	case ErrorTestFailed:
		return msgs.RecoveryConnectionTestFailed()
	case ErrorSavedNeedsRepair:
		return msgs.ConnectionErrorSavedNeedsRepair()
	case ErrorApplyConfig:
		return msgs.RecoveryConnectionApplyFailed()
	case StatusSavedStoreFailed:
		return msgs.RecoveryLocalSaveFailed()
	default:
		if detail != "" {
			return kind + " " + detail
		}
		return kind
	}
}

func BlockedText(msgs locales.Messages, kind string) string {
	switch kind {
	case BlockedApplying:
		return msgs.ConnectionBlockedApplying()
	case BlockedShutdown:
		return msgs.ConnectionBlockedShutdown()
	case BlockedNoSaved:
		return msgs.ConnectionBlockedNoSaved()
	case BlockedStore:
		return msgs.ConnectionBlockedStore()
	case BlockedActiveWork:
		return msgs.ConnectionBlockedActiveWork()
	case BlockedUntrustedIssue:
		return msgs.ConnectionBlockedUntrustedIssue()
	default:
		return kind
	}
}
