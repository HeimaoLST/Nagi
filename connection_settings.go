package main

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

type connectionDraft struct {
	API, Endpoint, APIKey, Model                         string
	ContextTokens, OutputTokens, WorkingDirectory, Slots string
}

type connectionSettings struct {
	Draft               connectionDraft
	initial             connectionDraft
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
	focusField          string
	focusFolder         bool
	folderError         string
	editOpen            map[string]bool
	focusEdit           string
}

type connectionSettingsActions struct {
	Save         func(connectionConfig)
	SaveAndApply func(connectionConfig)
	Apply        func()
	Test         func(connectionConfig)
	Discard      func()
	ChooseFolder func()
}

func newConnectionSettings(config *connectionConfig, workDir string) *connectionSettings {
	initial := defaultConnectionConfig(workDir)
	if config != nil {
		initial = *config
	}
	s := &connectionSettings{Draft: connectionDraft{
		API: initial.API, Endpoint: initial.Endpoint, APIKey: initial.APIKey, Model: initial.Model,
		ContextTokens: strconv.Itoa(initial.ContextTokens), OutputTokens: strconv.Itoa(initial.OutputTokens),
		WorkingDirectory: initial.WorkingDirectory, Slots: strconv.Itoa(initial.MaxActiveTasks),
	}, HasSaved: config != nil, editOpen: map[string]bool{}}
	if config == nil {
		s.focusField = "Endpoint"
	}
	s.initial = s.Draft
	return s
}

func (s *connectionSettings) Dirty() bool {
	return s != nil && s.Draft != s.initial
}

func (s *connectionSettings) ResetDraft() {
	if s == nil {
		return
	}
	s.Draft = s.initial
	s.Errors = nil
	s.Error = ""
	s.ErrorDetail = ""
	s.Status = ""
	s.StatusArgs = nil
	s.ReloadBlocked = ""
	s.focusField = "Endpoint"
	s.folderError = ""
	s.RecoveryDetailsOpen = false
}

func (s *connectionSettings) validated() (connectionConfig, bool) {
	d := s.Draft
	config := connectionConfig{Version: 1, API: d.API, Endpoint: strings.TrimSpace(d.Endpoint),
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
			numericErrors[f.key] = validationNumeric
		}
		*f.target = value
	}
	s.Errors = validateConnectionConfig(config)
	if s.Errors == nil {
		s.Errors = map[string]string{}
	}
	for key, message := range numericErrors {
		s.Errors[key] = message
	}
	for _, key := range []string{"Endpoint", "APIKey", "Model", "WorkingDirectory"} {
		if s.Errors[key] != "" {
			s.editOpen[key] = true
		}
	}
	if s.Errors["ContextTokens"] != "" || s.Errors["OutputTokens"] != "" || s.Errors["MaxActiveTasks"] != "" {
		s.LimitsOpen = true
	}
	s.Error = ""
	s.ErrorDetail = ""
	s.RecoveryDetailsOpen = false
	if len(s.Errors) > 0 {
		s.Error = connectionCheckFields
		for _, key := range []string{"API", "Endpoint", "APIKey", "Model", "ContextTokens", "OutputTokens", "WorkingDirectory", "MaxActiveTasks"} {
			if s.Errors[key] != "" {
				s.focusField = key
				break
			}
		}
		return config, false
	}
	return config, true
}

// The host owns persistence, process lifetime and UI-thread updates. Rendering
// and editing this form never starts a model request or writes configuration.
func (s *connectionSettings) view(c *ui.Context, t *ui.Theme, actions connectionSettingsActions) {
	msgs := locales.In(c.Locale())
	if s.focusField != "" {
		s.editOpen[s.focusField] = true
	}
	ui.Column(c).Key("connection-form").FillWidth().AlignItems(ui.Start).Gap(24).Children(func() {
		if s.Error != "" || s.ErrorDetail != "" {
			summary := connectionErrorText(msgs, s.Error, "")
			detail := s.ErrorDetail
			if detail == "" && !connectionErrorIsKind(s.Error) && s.Error != connectionCheckFields {
				detail = s.Error
			}
			if s.Error == connectionCheckFields {
				summary = msgs.ConnectionCheckFields()
				detail = ""
			}
			if !connectionErrorIsKind(s.Error) && s.Error != connectionCheckFields {
				summary = msgs.RecoveryConnectionSaveFailed()
			}
			if s.Draft.APIKey != "" {
				detail = strings.ReplaceAll(detail, s.Draft.APIKey, "[redacted]")
			}
			recoveryNotice(c, t, summary, detail, s.RecoveryDetailsOpen, func() { s.RecoveryDetailsOpen = !s.RecoveryDetailsOpen }, nil, "")
		} else {
			s.RecoveryDetailsOpen = false
		}
		c.Key("connection-fields")
		preferenceGroup(c, t, func() {
			settingsPreferenceRow(c, t, msgs.ProtocolLabel(), "", func() {
				c.Key("connection-protocol")
				preferenceSelect(c, &s.Draft.API, []selectOption{{connectionAPIChatCompletions, msgs.ProtocolChatCompletionsLabel()}, {connectionAPIResponses, msgs.ProtocolResponsesLabel()}}, msgs.ProtocolLabel(), s.Busy)
			})
			if s.Errors["API"] != "" {
				ui.Text(c, connectionFieldErrorText(msgs, "API", s.Errors["API"])).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Danger)
			}
			s.editableField(c, t, "Endpoint", msgs.ConnectionFieldEndpoint(), msgs.ConnectionEndpointPlaceholder(), &s.Draft.Endpoint, false, msgs.ConnectionEndpointHint())
			s.editableField(c, t, "APIKey", msgs.ConnectionFieldAPIKey(), msgs.ConnectionAPIKeyPlaceholder(), &s.Draft.APIKey, true, msgs.ConnectionAPIKeyHint())
			s.editableField(c, t, "Model", msgs.ConnectionFieldModel(), msgs.ConnectionModelPlaceholder(), &s.Draft.Model, false, "")
			ui.Column(c).Key("connection-limits").FillWidth().Gap(8).Children(func() {
				limitsLabel := msgs.ConnectionCapacityShow()
				limitsTooltip := msgs.ConnectionCapacityShowTooltip()
				if s.LimitsOpen {
					limitsLabel = msgs.ConnectionCapacityHide()
					limitsTooltip = msgs.ConnectionCapacityHideTooltip()
				}
				ui.Toolbar(c, func() {
					c.Key("limits-disclosure")
					limits := appTooltip(c, secondaryButton(c, "").Label(limitsLabel), limitsTooltip).
						MinHeight(32).Padding(4, 8).Gap(6)
					limits.Children(func() {
						arrow := float32(180)
						if s.LimitsOpen {
							arrow = -90
						}
						ui.Icon(c, glyphArrowRightSLine).Width(16).Height(16).Rotate(arrow - 180).TextColor(t.TextMuted)
						ui.Text(c, limitsLabel).FontSize(typeAction).TextColor(t.Text)
					})
					limits.OnClick(func() { s.LimitsOpen = !s.LimitsOpen })
				}).Label(msgs.ConnectionCapacityDisclosure()).Padding(0)
				if s.LimitsOpen {
					ui.Text(c, msgs.ConnectionLimitsHint()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
					s.field(c, t, "ContextTokens", msgs.ConnectionFieldContextTokens(), "131072", &s.Draft.ContextTokens, false, "")
					s.field(c, t, "OutputTokens", msgs.ConnectionFieldOutputTokens(), "8192", &s.Draft.OutputTokens, false, "")
					s.field(c, t, "MaxActiveTasks", msgs.ConnectionFieldMaxActiveTasks(), "2", &s.Draft.Slots, false, "")
				} else {
					ui.Text(c, s.valueSource(msgs)).FontSize(typeMeta).TextColor(t.TextMuted)
					for _, field := range []struct{ key, label, value string }{
						{"ContextTokens", msgs.ConnectionFieldContextTokens(), s.Draft.ContextTokens},
						{"OutputTokens", msgs.ConnectionFieldOutputTokens(), s.Draft.OutputTokens},
						{"MaxActiveTasks", msgs.ConnectionFieldMaxActiveTasks(), s.Draft.Slots},
					} {
						ui.Text(c, field.label+": "+field.value).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
						if message := s.Errors[field.key]; message != "" {
							ui.Text(c, connectionFieldErrorText(msgs, field.key, message)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Danger)
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
				settingsPreferenceRow(c, t, msgs.ConnectionFieldWorkingDirectory(), s.valueSource(msgs), func() {
					ui.Text(c, path).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.Text)
					c.Key("choose-connection-folder")
					folder := secondaryButton(c, msgs.WorkFolderChoose()).Label(msgs.WorkFolderChoose()).Disabled(s.Busy).OnClick(actions.ChooseFolder)
					if s.focusFolder {
						folder.Focus()
						s.focusFolder = false
					}
				}, true)
				ui.Text(c, folderHint).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				label := msgs.ConnectionFolderEnterPath()
				if s.editOpen["WorkingDirectory"] {
					label = msgs.ConnectionFolderHidePath()
				}
				c.Key("folder-path-disclosure")
				secondaryButton(c, label).Label(label).AlignSelf(ui.Start).OnClick(func() {
					s.editOpen["WorkingDirectory"] = !s.editOpen["WorkingDirectory"]
					if s.editOpen["WorkingDirectory"] {
						s.focusField = "WorkingDirectory"
					}
				})
				if s.editOpen["WorkingDirectory"] {
					s.field(c, t, "WorkingDirectory", msgs.ConnectionFieldWorkingDirectory(), msgs.ConnectionWorkingDirectoryPlaceholder(), &s.Draft.WorkingDirectory, false, "")
				} else if message := s.Errors["WorkingDirectory"]; message != "" {
					ui.Text(c, connectionFieldErrorText(msgs, "WorkingDirectory", message)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Danger)
				}
				if s.folderError != "" {
					ui.Text(c, msgs.WorkFolderChooseFailed(s.folderError)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Danger)
				}
			})
		})
		ui.Column(c).Key("connection-actions").FillWidth().Gap(8).Children(func() {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if s.SavedPending && !s.Dirty() {
					secondaryButton(c, msgs.ConnectionApplySaved()).Label(msgs.ConnectionApplySaved()).Disabled(s.Busy || s.ReloadBlocked != "").OnClick(actions.Apply)
					secondaryButton(c, msgs.ConnectionSaveConfiguration()).Label(msgs.ConnectionSaveConfiguration()).FontSize(typeAction).MinHeight(32).Disabled(s.Busy).OnClick(func() {
						if config, ok := s.validated(); ok && actions.Save != nil {
							actions.Save(config)
						}
					})
				} else {
					secondaryButton(c, msgs.ConnectionSaveAndApply()).Label(msgs.ConnectionSaveAndApply()).Disabled(s.Busy).OnClick(func() {
						if config, ok := s.validated(); ok && actions.SaveAndApply != nil {
							actions.SaveAndApply(config)
						}
					})
					secondaryButton(c, msgs.ConnectionSaveConfiguration()).Label(msgs.ConnectionSaveConfiguration()).FontSize(typeAction).MinHeight(32).Disabled(s.Busy).OnClick(func() {
						if config, ok := s.validated(); ok && actions.Save != nil {
							actions.Save(config)
						}
					})
					if s.HasSaved {
						secondaryButton(c, msgs.ConnectionApplySaved()).Label(msgs.ConnectionApplySaved()).Disabled(s.Busy || s.ReloadBlocked != "").OnClick(actions.Apply)
					}
				}
				if s.Dirty() && actions.Discard != nil {
					secondaryButton(c, msgs.ConnectionDiscard()).Label(msgs.ConnectionDiscard()).Disabled(s.Busy).OnClick(actions.Discard)
				}
			})
			if s.ReloadBlocked != "" {
				ui.Text(c, connectionBlockedText(msgs, s.ReloadBlocked)).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.Warning)
			}
			ui.Text(c, msgs.ConnectionSaveDoesNotInterrupt()).
				FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				secondaryButton(c, msgs.ConnectionTestAction()).Label(msgs.ConnectionTestAction()).Disabled(s.Busy).OnClick(func() {
					if config, ok := s.validated(); ok && actions.Test != nil {
						actions.Test(config)
					}
				})
			})
			ui.Text(c, msgs.ConnectionTestHint()).
				FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
			if s.Status != "" {
				ui.Text(c, connectionStatusText(msgs, s.Status, s.StatusArgs)).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.Text)
			}
		})
	})
}

func (s *connectionSettings) valueSource(msgs locales.Messages) string {
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

func (s *connectionSettings) editableField(c *ui.Context, t *ui.Theme, key, label, placeholder string, value *string, secret bool, helper string) {
	msgs := locales.In(c.Locale())
	ui.Column(c).Key("connection-editor-" + key).FillWidth().Gap(8).Children(func() {
		if !s.HasSaved {
			s.editOpen[key] = true
			s.field(c, t, key, label, placeholder, value, secret, helper)
			return
		}
		open := s.editOpen[key]
		settingsPreferenceRow(c, t, label, s.valueSource(msgs), func() {
			text := *value
			if text == "" {
				text = msgs.ConnectionValueUnset()
			} else if secret {
				text = "••••••••"
			}
			ui.Text(c, text).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.Text)
			buttonText, accessible := msgs.ConnectionEditAction(), msgs.ConnectionEditField(label)
			if open {
				buttonText, accessible = msgs.ConnectionCloseEditorAction(), msgs.ConnectionCloseField(label)
			}
			c.Key("editor-disclosure")
			button := secondaryButton(c, buttonText).Label(accessible).OnClick(func() {
				s.editOpen[key] = !open
				if !open {
					s.focusField = key
				} else {
					s.focusEdit = key
				}
			})
			if s.focusEdit == key {
				button.Focus()
				s.focusEdit = ""
			}
		})
		if open {
			s.field(c, t, key, label, placeholder, value, secret, helper)
		} else if message := s.Errors[key]; message != "" {
			ui.Text(c, connectionFieldErrorText(msgs, key, message)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Danger)
		}
	})
}

func (s *connectionSettings) field(c *ui.Context, t *ui.Theme, key, label, placeholder string, value *string, secret bool, helper string) {
	ui.Column(c).Key("connection-field-" + key).FillWidth().Gap(4).Children(func() {
		input := func() ui.Element {
			c.Key("input-" + key)
			field := preferenceInput(c, value, s.Errors[key] != "").Label(label).Placeholder(placeholder).Disabled(s.Busy)
			if secret {
				field.Password()
			}
			if s.focusField == key {
				field.Focus().ScrollIntoView()
				s.focusField = ""
			}
			return field
		}
		settingsPreferenceRow(c, t, label, helper, func() {
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
			ui.Text(c, connectionFieldErrorText(locales.In(c.Locale()), key, message)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Danger)
		}
	})
}

func (s *connectionSettings) slotStep(c *ui.Context, delta int) {
	msgs := locales.In(c.Locale())
	text, label := "−", msgs.ConnectionSlotsDecrease()
	if delta > 0 {
		text, label = "+", msgs.ConnectionSlotsIncrease()
	}
	value, err := positiveInt(s.Draft.Slots)
	blocked := err != nil || delta < 0 && value == 1 || delta > 0 && value == int(^uint(0)>>1)
	c.Key("slot-step-" + text)
	button := secondaryButton(c, "").Label(label).Width(40).Padding(8, 12).Disabled(s.Busy || blocked)
	icon := glyphSubtractLine
	if delta > 0 {
		icon = glyphAddLine
	}
	button.Children(func() { ui.Icon(c, icon).Width(16).Height(16) })
	button.OnClick(func() {
		s.Draft.Slots = strconv.Itoa(value + delta)
	})
}

// Form feedback is stored by kind (and raw detail where one exists) so the
// current language says it again on every frame. Unknown text stays raw.
const (
	connectionCheckFields = "check-fields"
	validationNumeric     = "numeric"

	connStatusSaving             = "saving"
	connStatusSavedApplied       = "saved-applied"
	connStatusChecking           = "checking-saved"
	connStatusApplied            = "applied"
	connStatusTesting            = "testing"
	connStatusTestCompleted      = "test-completed"
	connStatusSavedStoreFailed   = "saved-store-failed"
	connErrorTestFailed          = "test-failed"
	connErrorSavedNeedsRepair    = "saved-needs-repair"
	connErrorApplyConfig         = "apply-config"
	connErrorHelperMissing       = "helper-missing"
	connErrorPrepare             = "prepare"
	connErrorStart               = "start"
	connErrorExitedBeforeReady   = "exited-before-ready"
	connErrorNotReady            = "not-ready"
	connErrorNotExposed          = "not-exposed"
	connErrorExitedWhileChecking = "exited-while-validating"
	connErrorExitedDuringInstall = "exited-during-validation"
	connBlockedModelOp           = "model-op"
	connBlockedApplying          = "applying"
	connBlockedShutdown          = "shutdown"
	connBlockedNoSaved           = "no-saved"
	connBlockedStore             = "store"
	connBlockedActiveWork        = "active-work"
	connBlockedUntrustedIssue    = "untrusted-issue"
)

func noticeArg(args []string, index int) string {
	if index < 0 || index >= len(args) {
		return ""
	}
	return args[index]
}

func connectionFieldErrorText(msgs locales.Messages, key, kind string) string {
	switch kind {
	case connectionCheckFields:
		return msgs.ConnectionCheckFields()
	case connectionValidationRequired:
		return msgs.ValidationRequired()
	case connectionValidationAPI:
		return msgs.ValidationAPI()
	case connectionValidationEndpointWhitespace:
		return msgs.ValidationEndpointWhitespace()
	case connectionValidationEndpointURL:
		return msgs.ValidationEndpointURL()
	case connectionValidationEndpointUserinfo:
		return msgs.ValidationEndpointUserinfo()
	case connectionValidationEndpointQuery:
		return msgs.ValidationEndpointQuery()
	case connectionValidationEndpointFragment:
		return msgs.ValidationEndpointFragment()
	case connectionValidationWorkspaceAbsolute:
		return msgs.ValidationWorkspaceAbsolutePath()
	case connectionValidationWorkspaceDirectory:
		return msgs.ValidationWorkspaceDirectoryExists()
	case connectionValidationPositive, connectionValidationContextGap, connectionValidationLessThanContext:
		if connectionValidationContextGap == kind {
			return msgs.ValidationContextGap()
		}
		if connectionValidationLessThanContext == kind {
			return msgs.ValidationLessThanContext()
		}
		return msgs.ValidationPositive()
	case connectionValidationVersion:
		return msgs.ValidationVersion()
	case validationNumeric:
		return msgs.ValidationNumeric()
	default:
		return kind
	}
}

func connectionStatusText(msgs locales.Messages, kind string, args []string) string {
	switch kind {
	case connStatusSaving:
		return msgs.ConnectionStatusSaving()
	case connStatusSavedApplied:
		return msgs.ConnectionStatusSavedApplied()
	case connStatusChecking:
		return msgs.ConnectionStatusCheckingSaved()
	case connStatusApplied:
		return msgs.ConnectionStatusApplied()
	case connStatusTesting:
		return msgs.ConnectionStatusTesting()
	case connStatusTestCompleted:
		return msgs.ConnectionStatusTestCompleted(noticeArg(args, 0), noticeArg(args, 1), noticeArg(args, 2), noticeArg(args, 3))
	default:
		return kind
	}
}

func connectionErrorIsKind(kind string) bool {
	switch kind {
	case connErrorTestFailed, connErrorApplyConfig, connErrorHelperMissing, connErrorPrepare, connErrorStart,
		connErrorExitedBeforeReady, connErrorNotReady, connErrorNotExposed, connErrorExitedWhileChecking,
		connErrorExitedDuringInstall, connStatusSavedStoreFailed, connErrorSavedNeedsRepair:
		return true
	default:
		return false
	}
}

func connectionErrorText(msgs locales.Messages, kind, detail string) string {
	switch kind {
	case connErrorTestFailed:
		return msgs.RecoveryConnectionTestFailed()
	case connErrorSavedNeedsRepair:
		return msgs.ConnectionErrorSavedNeedsRepair()
	case connErrorApplyConfig:
		return msgs.RecoveryConnectionApplyFailed()
	case connErrorHelperMissing:
		return msgs.RecoveryRuntimeMissing()
	case connErrorPrepare:
		return msgs.RecoveryRuntimeStartFailed()
	case connErrorStart:
		return msgs.RecoveryRuntimeStartFailed()
	case connStatusSavedStoreFailed:
		return msgs.RecoveryLocalSaveFailed()
	case connErrorExitedBeforeReady:
		return msgs.ConnectionErrorExitedBeforeReady()
	case connErrorNotReady:
		return msgs.ConnectionErrorNotReady()
	case connErrorNotExposed:
		return msgs.ConnectionErrorNotExposed()
	case connErrorExitedWhileChecking:
		return msgs.ConnectionErrorExitedWhileValidating()
	case connErrorExitedDuringInstall:
		return msgs.ConnectionErrorExitedDuringValidation()
	default:
		if detail != "" {
			return kind + " " + detail
		}
		return kind
	}
}

func connectionBlockedText(msgs locales.Messages, kind string) string {
	switch kind {
	case connBlockedModelOp:
		return msgs.ConnectionBlockedModelOp()
	case connBlockedApplying:
		return msgs.ConnectionBlockedApplying()
	case connBlockedShutdown:
		return msgs.ConnectionBlockedShutdown()
	case connBlockedNoSaved:
		return msgs.ConnectionBlockedNoSaved()
	case connBlockedStore:
		return msgs.ConnectionBlockedStore()
	case connBlockedActiveWork:
		return msgs.ConnectionBlockedActiveWork()
	case connBlockedUntrustedIssue:
		return msgs.ConnectionBlockedUntrustedIssue()
	default:
		return kind
	}
}
