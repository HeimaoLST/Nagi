package connection

import (
	"fmt"
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
	SavedPending        bool
	LimitsOpen          bool
	RecoveryDetailsOpen bool
	FocusField          string
	FocusFolder         bool
	FolderError         string
	EditOpen            map[string]bool
	FocusEdit           string
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
	}, HasSaved: config != nil, EditOpen: map[string]bool{}}
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
	ui.Column(c).Key("connection-form").FillWidth().AlignItems(ui.Start).Gap(24).Children(func() {
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
		c.Key("connection-fields")
		theme.PreferenceGroup(c, t, func() {
			theme.SettingsPreferenceRow(c, t, msgs.ProtocolLabel(), "", func() {
				c.Key("connection-protocol")
				theme.PreferenceSelect(c, &s.Draft.API, []theme.SelectOption{{Value: APIChatCompletions, Label: msgs.ProtocolChatCompletionsLabel()}, {Value: APIResponses, Label: msgs.ProtocolResponsesLabel()}}, msgs.ProtocolLabel(), s.Busy)
			})
			if s.Errors["API"] != "" {
				ui.Text(c, FieldErrorText(msgs, "API", s.Errors["API"])).FontSize(theme.TypeBody).FixedLineHeight(theme.LineBody).TextColor(t.Danger)
			}
			s.editableField(c, t, "Endpoint", msgs.ConnectionFieldEndpoint(), msgs.ConnectionEndpointPlaceholder(), &s.Draft.Endpoint, false, msgs.ConnectionEndpointHint(), border)
			s.editableField(c, t, "APIKey", msgs.ConnectionFieldAPIKey(), msgs.ConnectionAPIKeyPlaceholder(), &s.Draft.APIKey, true, msgs.ConnectionAPIKeyHint(), border)
			s.editableField(c, t, "Model", msgs.ConnectionFieldModel(), msgs.ConnectionModelPlaceholder(), &s.Draft.Model, false, "", border)
			ui.Column(c).Key("connection-limits").FillWidth().Gap(8).Children(func() {
				limitsLabel := msgs.ConnectionCapacityShow()
				limitsTooltip := msgs.ConnectionCapacityShowTooltip()
				if s.LimitsOpen {
					limitsLabel = msgs.ConnectionCapacityHide()
					limitsTooltip = msgs.ConnectionCapacityHideTooltip()
				}
				ui.Toolbar(c, func() {
					c.Key("limits-disclosure")
					limits := theme.AppTooltip(c, theme.SecondaryButton(c, "").Label(limitsLabel), limitsTooltip).
						MinHeight(32).Padding(4, 8).Gap(6)
					limits.Children(func() {
						arrow := float32(180)
						if s.LimitsOpen {
							arrow = -90
						}
						ui.Icon(c, theme.GlyphArrowRightSLine).Width(16).Height(16).Rotate(arrow - 180).TextColor(t.TextMuted)
						ui.Text(c, limitsLabel).FontSize(theme.TypeAction).TextColor(t.Text)
					})
					limits.OnClick(func() { s.LimitsOpen = !s.LimitsOpen })
				}).Label(msgs.ConnectionCapacityDisclosure()).Padding(0)
				if s.LimitsOpen {
					ui.Text(c, msgs.ConnectionLimitsHint()).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
					s.Field(c, t, "ContextTokens", msgs.ConnectionFieldContextTokens(), "131072", &s.Draft.ContextTokens, false, "", border)
					s.Field(c, t, "OutputTokens", msgs.ConnectionFieldOutputTokens(), "8192", &s.Draft.OutputTokens, false, "", border)
					s.Field(c, t, "MaxActiveTasks", msgs.ConnectionFieldMaxActiveTasks(), "2", &s.Draft.Slots, false, "", border)
				} else {
					ui.Text(c, s.valueSource(msgs)).FontSize(theme.TypeMeta).TextColor(t.TextMuted)
					for _, field := range []struct{ key, label, value string }{
						{"ContextTokens", msgs.ConnectionFieldContextTokens(), s.Draft.ContextTokens},
						{"OutputTokens", msgs.ConnectionFieldOutputTokens(), s.Draft.OutputTokens},
						{"MaxActiveTasks", msgs.ConnectionFieldMaxActiveTasks(), s.Draft.Slots},
					} {
						ui.Text(c, field.label+": "+field.value).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
						if message := s.Errors[field.key]; message != "" {
							ui.Text(c, FieldErrorText(msgs, field.key, message)).FontSize(theme.TypeBody).FixedLineHeight(theme.LineBody).TextColor(t.Danger)
						}
					}
				}
			})
			ui.Column(c).Key("connection-folder").FillWidth().Gap(8).Children(func() {
				folderHint := msgs.ConnectionWorkingDirectoryHint()
				path := s.Draft.WorkingDirectory
				if strings.TrimSpace(path) == "" {
					path = msgs.ConnectionValueUnset()
					folderHint = msgs.WorkFolderRequiredHint()
				}
				theme.SettingsPreferenceRow(c, t, msgs.ConnectionFieldWorkingDirectory(), s.valueSource(msgs), func() {
					ui.Text(c, path).FontSize(theme.TypeLabel).FixedLineHeight(theme.LineLabel).TextColor(t.Text)
					c.Key("choose-connection-folder")
					folder := theme.SecondaryButton(c, msgs.WorkFolderChoose()).Label(msgs.WorkFolderChoose()).Disabled(s.Busy).OnClick(actions.ChooseFolder)
					if s.FocusFolder {
						folder.Focus()
						s.FocusFolder = false
					}
				}, true)
				ui.Text(c, folderHint).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
				label := msgs.ConnectionFolderEnterPath()
				if s.EditOpen["WorkingDirectory"] {
					label = msgs.ConnectionFolderHidePath()
				}
				c.Key("folder-path-disclosure")
				theme.SecondaryButton(c, label).Label(label).AlignSelf(ui.Start).OnClick(func() {
					s.EditOpen["WorkingDirectory"] = !s.EditOpen["WorkingDirectory"]
					if s.EditOpen["WorkingDirectory"] {
						s.FocusField = "WorkingDirectory"
					}
				})
				if s.EditOpen["WorkingDirectory"] {
					s.Field(c, t, "WorkingDirectory", msgs.ConnectionFieldWorkingDirectory(), msgs.ConnectionWorkingDirectoryPlaceholder(), &s.Draft.WorkingDirectory, false, "", border)
				} else if message := s.Errors["WorkingDirectory"]; message != "" {
					ui.Text(c, FieldErrorText(msgs, "WorkingDirectory", message)).FontSize(theme.TypeBody).FixedLineHeight(theme.LineBody).TextColor(t.Danger)
				}
				if s.FolderError != "" {
					ui.Text(c, msgs.WorkFolderChooseFailed(s.FolderError)).FontSize(theme.TypeBody).FixedLineHeight(theme.LineBody).TextColor(t.Danger)
				}
			})
		})
		ui.Column(c).Key("connection-actions").FillWidth().Gap(8).Children(func() {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if s.SavedPending && !s.Dirty() {
					theme.SecondaryButton(c, msgs.ConnectionApplySaved()).Label(msgs.ConnectionApplySaved()).Disabled(s.Busy || s.ReloadBlocked != "").OnClick(actions.Apply)
					theme.SecondaryButton(c, msgs.ConnectionSaveConfiguration()).Label(msgs.ConnectionSaveConfiguration()).FontSize(theme.TypeAction).MinHeight(32).Disabled(s.Busy).OnClick(func() {
						if config, ok := s.Validated(); ok && actions.Save != nil {
							actions.Save(config)
						}
					})
				} else {
					theme.SecondaryButton(c, msgs.ConnectionSaveAndApply()).Label(msgs.ConnectionSaveAndApply()).Disabled(s.Busy).OnClick(func() {
						if config, ok := s.Validated(); ok && actions.SaveAndApply != nil {
							actions.SaveAndApply(config)
						}
					})
					theme.SecondaryButton(c, msgs.ConnectionSaveConfiguration()).Label(msgs.ConnectionSaveConfiguration()).FontSize(theme.TypeAction).MinHeight(32).Disabled(s.Busy).OnClick(func() {
						if config, ok := s.Validated(); ok && actions.Save != nil {
							actions.Save(config)
						}
					})
					if s.HasSaved {
						theme.SecondaryButton(c, msgs.ConnectionApplySaved()).Label(msgs.ConnectionApplySaved()).Disabled(s.Busy || s.ReloadBlocked != "").OnClick(actions.Apply)
					}
				}
				if s.Dirty() && actions.Discard != nil {
					theme.SecondaryButton(c, msgs.ConnectionDiscard()).Label(msgs.ConnectionDiscard()).Disabled(s.Busy).OnClick(actions.Discard)
				}
			})
			if s.ReloadBlocked != "" {
				ui.Text(c, BlockedText(msgs, s.ReloadBlocked)).FontSize(theme.TypeLabel).FixedLineHeight(theme.LineLabel).TextColor(t.Warning)
			}
			ui.Text(c, msgs.ConnectionSaveDoesNotInterrupt()).
				FontSize(theme.TypeMeta).LineHeight(1.5).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				theme.SecondaryButton(c, msgs.ConnectionTestAction()).Label(msgs.ConnectionTestAction()).Disabled(s.Busy).OnClick(func() {
					if config, ok := s.Validated(); ok && actions.Test != nil {
						actions.Test(config)
					}
				})
			})
			ui.Text(c, msgs.ConnectionTestHint()).
				FontSize(theme.TypeMeta).LineHeight(1.5).TextColor(t.TextMuted)
			if s.Status != "" {
				ui.Text(c, StatusText(msgs, s.Status, s.StatusArgs)).FontSize(theme.TypeLabel).FixedLineHeight(theme.LineLabel).TextColor(t.Text)
			}
		})
	})
}

func (s *Settings) valueSource(msgs locales.Messages) string {
	if len(s.Errors) > 0 {
		return msgs.ConnectionValueInvalidDraft()
	}
	if s.Dirty() {
		return msgs.ConnectionValueUnsavedDraft()
	}
	if s.HasSaved && !s.Busy && s.Error == "" && s.ErrorDetail == "" {
		return msgs.ConnectionValueSaved()
	}
	return msgs.ConnectionValueDraft()
}

func (s *Settings) editableField(c *ui.Context, t *ui.Theme, key, label, placeholder string, value *string, secret bool, helper string, inputBorder ...ui.Color) {
	border := c.Theme().Border
	if len(inputBorder) > 0 {
		border = inputBorder[0]
	}
	msgs := locales.In(c.Locale())
	ui.Column(c).Key("connection-editor-" + key).FillWidth().Gap(8).Children(func() {
		if !s.HasSaved {
			s.EditOpen[key] = true
			s.Field(c, t, key, label, placeholder, value, secret, helper, border)
			return
		}
		open := s.EditOpen[key]
		theme.SettingsPreferenceRow(c, t, label, s.valueSource(msgs), func() {
			text := *value
			if text == "" {
				text = msgs.ConnectionValueUnset()
			} else if secret {
				text = "••••••••"
			}
			ui.Text(c, text).FontSize(theme.TypeLabel).FixedLineHeight(theme.LineLabel).TextColor(t.Text)
			buttonText, accessible := msgs.ConnectionEditAction(), msgs.ConnectionEditField(label)
			if open {
				buttonText, accessible = msgs.ConnectionCloseEditorAction(), msgs.ConnectionCloseField(label)
			}
			c.Key("editor-disclosure")
			button := theme.SecondaryButton(c, buttonText).Label(accessible).OnClick(func() {
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
		if open {
			s.Field(c, t, key, label, placeholder, value, secret, helper, border)
		} else if message := s.Errors[key]; message != "" {
			ui.Text(c, FieldErrorText(msgs, key, message)).FontSize(theme.TypeBody).FixedLineHeight(theme.LineBody).TextColor(t.Danger)
		}
	})
}

func (s *Settings) Field(c *ui.Context, t *ui.Theme, key, label, placeholder string, value *string, secret bool, helper string, inputBorder ...ui.Color) {
	border := c.Theme().Border
	if len(inputBorder) > 0 {
		border = inputBorder[0]
	}
	ui.Column(c).Key("connection-field-" + key).FillWidth().Gap(4).Children(func() {
		input := func() ui.Element {
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
		theme.SettingsPreferenceRow(c, t, label, helper, func() {
			if key == "MaxActiveTasks" {
				ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(4).Children(func() {
					s.slotStep(c, -1)
					input().Width(180).MinWidth(0).Shrink(1)
					s.slotStep(c, 1)
				})
			} else {
				input().FillWidth()
			}
		})
		if message := s.Errors[key]; message != "" {
			ui.Text(c, FieldErrorText(locales.In(c.Locale()), key, message)).FontSize(theme.TypeBody).FixedLineHeight(theme.LineBody).TextColor(t.Danger)
		}
	})
}

func positiveInt(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("not a positive integer")
	}
	return parsed, nil
}

func (s *Settings) slotStep(c *ui.Context, delta int) {
	msgs := locales.In(c.Locale())
	text, label := "−", msgs.ConnectionSlotsDecrease()
	if delta > 0 {
		text, label = "+", msgs.ConnectionSlotsIncrease()
	}
	value, err := positiveInt(s.Draft.Slots)
	blocked := err != nil || delta < 0 && value == 1 || delta > 0 && value == int(^uint(0)>>1)
	c.Key("slot-step-" + text)
	button := theme.SecondaryButton(c, "").Label(label).Width(40).Padding(8, 12).Disabled(s.Busy || blocked)
	icon := theme.GlyphSubtractLine
	if delta > 0 {
		icon = theme.GlyphAddLine
	}
	button.Children(func() { ui.Icon(c, icon).Width(16).Height(16) })
	button.OnClick(func() {
		s.Draft.Slots = strconv.Itoa(value + delta)
	})
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
