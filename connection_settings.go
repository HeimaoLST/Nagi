package main

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
)

type connectionDraft struct {
	API, Endpoint, APIKey, Model                         string
	ContextTokens, OutputTokens, WorkingDirectory, Slots string
}

type connectionSettings struct {
	Draft         connectionDraft
	initial       connectionDraft
	Errors        map[string]string
	Status        string
	StatusArgs    []string
	Error         string
	ErrorDetail   string
	ReloadBlocked string
	Busy          bool
	HasSaved      bool
	SavedPending  bool
	LimitsOpen    bool
	focusField    string
}

type connectionSettingsActions struct {
	Save         func(connectionConfig)
	SaveAndApply func(connectionConfig)
	Apply        func()
	Test         func(connectionConfig)
	Discard      func()
}

func newConnectionSettings(config *connectionConfig, workDir string) *connectionSettings {
	s := &connectionSettings{Draft: connectionDraft{
		API: "openai-chat-completions", ContextTokens: "131072", OutputTokens: "8192",
		WorkingDirectory: workDir, Slots: "2",
	}, focusField: "Endpoint"}
	if config != nil {
		s.Draft = connectionDraft{
			API: config.API, Endpoint: config.Endpoint, APIKey: config.APIKey, Model: config.Model,
			ContextTokens: strconv.Itoa(config.ContextTokens), OutputTokens: strconv.Itoa(config.OutputTokens),
			WorkingDirectory: config.WorkingDirectory, Slots: strconv.Itoa(config.MaxActiveTasks),
		}
		s.HasSaved = true
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
	if s.Errors["ContextTokens"] != "" || s.Errors["OutputTokens"] != "" || s.Errors["MaxActiveTasks"] != "" {
		s.LimitsOpen = true
	}
	s.Error = ""
	s.ErrorDetail = ""
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
	ui.Column(c).FillWidth().AlignItems(ui.Start).Gap(24).Children(func() {
		if s.Error != "" || s.ErrorDetail != "" {
			text := s.Error
			if s.Error == "" {
				text = msgs.ConnectionSaveFailed(s.ErrorDetail)
			} else if connectionErrorIsKind(s.Error) {
				text = connectionErrorText(msgs, s.Error, s.ErrorDetail)
			} else if s.ErrorDetail != "" {
				text = msgs.ConnectionSaveFailed(s.ErrorDetail)
			}
			ui.Text(c, text).FontSize(typeBody).LineHeight(1.5).TextColor(t.Danger)
		}
		ui.Column(c).FillWidth().Gap(16).Children(func() {
			ui.Column(c).FillWidth().Gap(4).Children(func() {
				ui.Text(c, msgs.ProtocolLabel()).FontSize(typeAction).FontWeight(500).TextColor(t.Text)
				apiIndex := 0
				if s.Draft.API == connectionAPIResponses {
					apiIndex = 1
				}
				protocol := segmentedSelection(c, &apiIndex, msgs.ProtocolChatCompletionsLabel(), msgs.ProtocolResponsesLabel()).
					Label(msgs.ProtocolLabel()).Disabled(s.Busy)
				if protocol.Changed() {
					s.Draft.API = []string{connectionAPIChatCompletions, connectionAPIResponses}[apiIndex]
				}
				if s.Errors["API"] != "" {
					ui.Text(c, connectionFieldErrorText(msgs, "API", s.Errors["API"])).FontSize(typeBody).LineHeight(1.5).TextColor(t.Danger)
				}
			})
			s.field(c, t, "Endpoint", msgs.ConnectionFieldEndpoint(), msgs.ConnectionEndpointPlaceholder(), &s.Draft.Endpoint, false,
				msgs.ConnectionEndpointHint())
			s.field(c, t, "APIKey", msgs.ConnectionFieldAPIKey(), msgs.ConnectionAPIKeyPlaceholder(), &s.Draft.APIKey, true,
				msgs.ConnectionAPIKeyHint())
			s.field(c, t, "Model", msgs.ConnectionFieldModel(), msgs.ConnectionModelPlaceholder(), &s.Draft.Model, false, "")
			ui.Column(c).FillWidth().Gap(8).Children(func() {
				limitsLabel := msgs.ConnectionCapacityShow()
				limitsTooltip := msgs.ConnectionCapacityShowTooltip()
				if s.LimitsOpen {
					limitsLabel = msgs.ConnectionCapacityHide()
					limitsTooltip = msgs.ConnectionCapacityHideTooltip()
				}
				ui.Toolbar(c, func() {
					limits := appTooltip(c, ui.Button(c, "").Label(limitsLabel), limitsTooltip).
						MinHeight(32).Padding(4, 8).Gap(6)
					limits.Children(func() {
						arrow := float32(180)
						if s.LimitsOpen {
							arrow = -90
						}
						ui.Icon(c, navBackIcon).Width(16).Height(16).Rotate(arrow).TextColor(t.TextMuted)
						ui.Text(c, limitsLabel).FontSize(typeAction).TextColor(t.Text)
					})
					limits.OnClick(func() { s.LimitsOpen = !s.LimitsOpen })
				}).Label(msgs.ConnectionCapacityDisclosure()).Padding(0)
				if s.LimitsOpen {
					ui.Text(c, msgs.ConnectionLimitsHint()).
						FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
					ui.Column(c).FillWidth().Gap(12).Children(func() {
						s.field(c, t, "ContextTokens", msgs.ConnectionFieldContextTokens(), "131072", &s.Draft.ContextTokens, false, "")
						s.field(c, t, "OutputTokens", msgs.ConnectionFieldOutputTokens(), "8192", &s.Draft.OutputTokens, false, "")
						s.field(c, t, "MaxActiveTasks", msgs.ConnectionFieldMaxActiveTasks(), "2", &s.Draft.Slots, false, "")
					})
				}
			})
			s.field(c, t, "WorkingDirectory", msgs.ConnectionFieldWorkingDirectory(), msgs.ConnectionWorkingDirectoryPlaceholder(), &s.Draft.WorkingDirectory, false,
				msgs.ConnectionWorkingDirectoryHint())
		})
		ui.Column(c).FillWidth().Gap(8).Children(func() {
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
					secondaryButton(c, msgs.ConnectionDiscard()).Disabled(s.Busy).OnClick(actions.Discard)
				}
			})
			if s.ReloadBlocked != "" {
				ui.Text(c, connectionBlockedText(msgs, s.ReloadBlocked)).FontSize(typeBody).LineHeight(1.5).TextColor(t.Warning)
			}
			ui.Text(c, msgs.ConnectionSaveDoesNotInterrupt()).
				FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				secondaryButton(c, msgs.ConnectionTestAction()).Disabled(s.Busy).OnClick(func() {
					if config, ok := s.validated(); ok && actions.Test != nil {
						actions.Test(config)
					}
				})
			})
			ui.Text(c, msgs.ConnectionTestHint()).
				FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
			if s.Status != "" {
				ui.Text(c, connectionStatusText(msgs, s.Status, s.StatusArgs)).FontSize(typeBody).LineHeight(1.5).TextColor(t.Text)
			}
		})
	})
}

func (s *connectionSettings) field(c *ui.Context, t *ui.Theme, key, label, placeholder string, value *string, secret bool, helper string) {
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		ui.Text(c, label).FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		input := ui.TextInput(c, value).Label(label).FontSize(typeBody).Placeholder(placeholder).Disabled(s.Busy)
		if secret {
			input.Password()
		}
		if s.focusField == key {
			input.Focus()
			s.focusField = ""
		}
		if message := s.Errors[key]; message != "" {
			ui.Text(c, connectionFieldErrorText(locales.In(c.Locale()), key, message)).FontSize(typeBody).LineHeight(1.5).TextColor(t.Danger)
		}
		if helper != "" {
			ui.Text(c, helper).FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
		}
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
		return msgs.ConnectionErrorTestFailed(detail)
	case connErrorSavedNeedsRepair:
		return msgs.ConnectionErrorSavedNeedsRepair()
	case connErrorApplyConfig:
		return msgs.ConnectionErrorApplyConfig(detail)
	case connErrorHelperMissing:
		return msgs.ConnectionErrorHelperMissing(detail)
	case connErrorPrepare:
		return msgs.ConnectionErrorPrepare(detail)
	case connErrorStart:
		return msgs.ConnectionErrorStart(detail)
	case connStatusSavedStoreFailed:
		return msgs.ConnectionStatusSavedStoreFailed(detail)
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
