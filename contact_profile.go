package main

// Contact profile draft editor and validation.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

type contactProfileActions struct {
	Save              func(profileDraft)
	SaveAndNew        func(profileDraft)
	Archive           func()
	Restore           func()
	Discard           func()
	RetryBuiltinTools func()
	ChooseFolder      func()
}

type contactProfileEditor struct {
	Draft                 profileDraft
	Archived              bool
	HasConversation       bool
	ActiveWork            bool
	UnknownWork           bool
	Busy                  bool
	Errors                map[string]string
	Error                 string
	Status                string
	BuiltinTools          []runtimeBuiltinTool
	BuiltinToolsLoaded    bool
	BuiltinToolsLoading   bool
	BuiltinToolsError     string
	BuiltinToolsSupported bool
	CanRetryBuiltinTools  bool
	focusField            string
	focusFolder           bool
	folderError           string
	initial               profileDraft
}

// Profile notices are stored by kind so the page can say them again in the
// current language. Unknown text stays raw for callers that show a diagnostic.
const (
	profileNoticeBusy               = "profile-busy"
	profileNoticeArchived           = "profile-archived"
	profileNoticeNoConversation     = "profile-no-conversation"
	profileNoticeReasonArchived     = "profile-reason-archived"
	profileNoticeReasonUnknown      = "profile-reason-unknown"
	profileNoticeReasonActive       = "profile-reason-active"
	profileNoticeErrorStore         = "profile-error-store"
	profileNoticeErrorSave          = "profile-error-save"
	profileNoticeSavedDefaults      = "profile-saved-defaults"
	profileNoticeSavedNextMessage   = "profile-saved-next-message"
	profileNoticeNameRequired       = "profile-name-required"
	profileNoticeWorkspaceAbsolute  = "profile-workspace-absolute"
	profileNoticeWorkspaceDirectory = "profile-workspace-directory"
)

func profileNoticeText(msgs locales.Messages, notice string) string {
	switch notice {
	case profileNoticeBusy:
		return msgs.ProfileErrorBusy()
	case profileNoticeArchived:
		return msgs.ProfileErrorArchived()
	case profileNoticeNoConversation:
		return msgs.ProfileErrorNoConversation()
	case profileNoticeReasonArchived:
		return msgs.ProfileReasonArchived()
	case profileNoticeReasonUnknown:
		return msgs.ProfileReasonUnknown()
	case profileNoticeReasonActive:
		return msgs.ProfileReasonActive()
	case profileNoticeErrorStore:
		return msgs.ProfileErrorStoreUnavailable()
	case profileNoticeErrorSave:
		return msgs.ProfileErrorSaveFailed()
	case profileNoticeSavedDefaults:
		return msgs.ProfileSavedDefaults()
	case profileNoticeSavedNextMessage:
		return msgs.ProfileSavedNextMessage()
	case profileNoticeNameRequired:
		return msgs.ValidationNameRequired()
	case profileNoticeWorkspaceAbsolute:
		return msgs.ValidationWorkspaceAbsolute()
	case profileNoticeWorkspaceDirectory:
		return msgs.ValidationWorkspaceDirectory()
	case archiveNoticeAlready:
		return msgs.ArchiveErrorAlready()
	case archiveNoticeUnknown:
		return msgs.ArchiveErrorUnknown()
	case archiveNoticeActive:
		return msgs.ArchiveErrorActive()
	case archiveNoticeStore:
		return msgs.ArchiveErrorStore()
	case archiveNoticeSave:
		return msgs.ArchiveErrorSave()
	case archiveNoticeSaved:
		return msgs.ArchiveSaved()
	case restoreNoticeStore:
		return msgs.RestoreErrorStore()
	case restoreNoticeSave:
		return msgs.RestoreErrorSave()
	case restoreNoticeSaved:
		return msgs.RestoreSaved()
	case builtinNoticeLegacy:
		return msgs.SettingsBuiltinLegacyError()
	case builtinNoticeManaged:
		return msgs.SettingsBuiltinManagedError()
	case builtinNoticeListUnavailable:
		return msgs.BuiltinListUnavailable()
	default:
		if name, ok := strings.CutPrefix(notice, builtinNoticeToolPrefix); ok {
			return msgs.BuiltinToolUnavailable(name)
		}
		return notice
	}
}

func newContactProfileEditor(draft profileDraft) *contactProfileEditor {
	draft = cloneProfileDraft(draft)
	return &contactProfileEditor{Draft: draft, initial: cloneProfileDraft(draft), focusField: "Name"}
}

func cloneProfileDraft(draft profileDraft) profileDraft {
	draft.BuiltinTools = cloneBuiltinTools(draft.BuiltinTools)
	return draft
}

// SetProfile replaces the host's saved profile and resets the draft. It is
// used after a successful host save, never to optimistically claim success.
func (e *contactProfileEditor) SetProfile(draft profileDraft) {
	e.Busy = false
	e.initial = cloneProfileDraft(draft)
	e.Draft = cloneProfileDraft(draft)
	e.Errors = nil
	e.Error = ""
	e.Status = ""
	e.folderError = ""
}

func (e *contactProfileEditor) SetBusy(busy bool, status string) {
	e.Busy = busy
	e.Status = status
}

func (e *contactProfileEditor) SetError(message string) {
	e.Busy = false
	e.Error = message
	e.Status = ""
}

func (e *contactProfileEditor) ResetDraft() {
	e.Draft = cloneProfileDraft(e.initial)
	e.Errors = nil
	e.Error = ""
	e.Status = ""
	e.focusField = "Name"
	e.folderError = ""
}

func validateProfileDraft(draft profileDraft) (profileDraft, map[string]string) {
	problems := map[string]string{}
	draft.Name = strings.TrimSpace(draft.Name)
	draft.Workspace = strings.TrimSpace(draft.Workspace)
	if draft.Name == "" {
		problems["Name"] = profileNoticeNameRequired
	}
	if draft.Workspace != "" {
		if !filepath.IsAbs(draft.Workspace) {
			problems["Workspace"] = profileNoticeWorkspaceAbsolute
		} else if info, err := os.Stat(draft.Workspace); err != nil || !info.IsDir() {
			problems["Workspace"] = profileNoticeWorkspaceDirectory
		}
	}
	return draft, problems
}

func (e *contactProfileEditor) Request(saveAndNew bool) (profileDraft, bool) {
	if e.Busy {
		e.Error = profileNoticeBusy
		return profileDraft{}, false
	}
	if e.Archived {
		e.Error = profileNoticeArchived
		return profileDraft{}, false
	}
	if saveAndNew {
		if !e.HasConversation {
			e.Error = profileNoticeNoConversation
			return profileDraft{}, false
		}
		if e.ActiveWork || e.UnknownWork {
			e.Error = e.activityReason()
			return profileDraft{}, false
		}
	}
	draft, errors := validateProfileDraft(e.Draft)
	e.Errors = errors
	e.Error = ""
	if len(e.Errors) > 0 {
		e.Status = ""
		if e.Errors["Name"] != "" {
			e.focusField = "Name"
		} else {
			e.focusField = "Workspace"
		}
		return profileDraft{}, false
	}
	return draft, true
}

func (e *contactProfileEditor) activityReason() string {
	switch {
	case e.Archived:
		return profileNoticeReasonArchived
	case e.UnknownWork:
		return profileNoticeReasonUnknown
	case e.ActiveWork:
		return profileNoticeReasonActive
	default:
		return ""
	}
}

func (e *contactProfileEditor) builtinDefaultNames() []string {
	return builtinToolNames(e.BuiltinTools)
}

func (e *contactProfileEditor) setBuiltinDefault(enabled bool) {
	if enabled {
		e.Draft.BuiltinTools = nil
		return
	}
	names := e.builtinDefaultNames()
	e.Draft.BuiltinTools = &names
}

func (e *contactProfileEditor) toggleBuiltinTool(name string, enabled bool) {
	if e.Draft.BuiltinTools == nil {
		names := e.builtinDefaultNames()
		e.Draft.BuiltinTools = &names
	}
	names := append([]string(nil), (*e.Draft.BuiltinTools)...)
	if enabled {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	} else {
		for index, current := range names {
			if current == name {
				names = append(names[:index], names[index+1:]...)
				break
			}
		}
	}
	if names == nil {
		names = []string{}
	}
	e.Draft.BuiltinTools = &names
}

func (e *contactProfileEditor) builtinToolsView(c *ui.Context, t *ui.Theme, editable bool, actions contactProfileActions) {
	msgs := locales.In(c.Locale())
	if !e.BuiltinToolsSupported {
		if e.BuiltinToolsError != "" {
			ui.Text(c, msgs.ProfileBuiltinTools()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			ui.Text(c, e.BuiltinToolsError).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
		}
		return
	}
	ui.Text(c, msgs.ProfileBuiltinTools()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
	if e.BuiltinToolsLoading {
		ui.Text(c, msgs.ProfileBuiltinLoading()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		return
	}
	if !e.BuiltinToolsLoaded {
		message := e.BuiltinToolsError
		if message == "" {
			message = msgs.ProfileBuiltinNotLoaded()
		}
		ui.Text(c, message).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
		if e.CanRetryBuiltinTools {
			secondaryButton(c, msgs.ProfileBuiltinRetry()).Disabled(e.Busy).OnClick(func() {
				if actions.RetryBuiltinTools != nil {
					actions.RetryBuiltinTools()
				}
			})
		}
		return
	}

	defaultNames := e.builtinDefaultNames()
	defaultSelected := e.Draft.BuiltinTools == nil
	defaultLabel := msgs.ProfileBuiltinUseDefaults()
	if len(defaultNames) > 0 {
		defaultLabel += " (" + strings.Join(defaultNames, ", ") + ")"
	}
	defaultValue := defaultSelected
	defaultBox := appTooltip(c, ui.Checkbox(c, &defaultValue, defaultLabel), msgs.ProfileBuiltinUseDefaultsTooltip()).FillWidth().MinHeight(32).Disabled(!editable)
	defaultBox.OnChange(func() { e.setBuiltinDefault(defaultValue) })

	for _, tool := range e.BuiltinTools {
		name := tool.Name
		checked := slices.Contains(defaultNames, name)
		if !defaultSelected {
			checked = builtinToolSelectionContains(e.Draft.BuiltinTools, name)
		}
		value := checked
		box := ui.Checkbox(c, &value, name).FillWidth().MinHeight(32).Disabled(!editable)
		if tool.Description != "" {
			box = appTooltip(c, box, tool.Description)
		}
		box.OnChange(func() { e.toggleBuiltinTool(name, value) })
	}
	if e.Draft.BuiltinTools != nil && len(*e.Draft.BuiltinTools) == 0 {
		ui.Text(c, msgs.ProfileBuiltinNone()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
	}
	if e.BuiltinToolsError != "" {
		ui.Text(c, e.BuiltinToolsError).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
	}
}

func (e *contactProfileEditor) view(c *ui.Context, t *ui.Theme, actions contactProfileActions) {
	msgs := locales.In(c.Locale())
	editable := !e.Busy && !e.Archived
	ui.Column(c).Gap(10).Children(func() {
		ui.Text(c, msgs.ProfileSaveIntro()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		if reason := e.activityReason(); reason != "" {
			ui.Text(c, profileNoticeText(msgs, reason)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
		}
		if e.Archived {
			ui.Text(c, msgs.ProfileArchivedBadge()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Warning)
		}

		ui.Text(c, msgs.FieldName()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
		name := preferenceInput(c, &e.Draft.Name, e.Errors["Name"] != "").Label(msgs.FieldNameAccessible()).Placeholder(msgs.FieldNamePlaceholder()).Disabled(!editable)
		if e.focusField == "Name" {
			name.Focus()
			e.focusField = ""
		}
		if message := e.Errors["Name"]; message != "" {
			ui.Text(c, profileNoticeText(msgs, message)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
		}
		ui.Text(c, msgs.ProfilePurpose()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
		formTextArea(c, &e.Draft.Purpose, false).Label(msgs.FieldPurposeAccessible()).Placeholder(msgs.ProfilePurpose()).Disabled(!editable)
		ui.Text(c, msgs.ProfileInstructions()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
		formTextArea(c, &e.Draft.Instructions, false).Label(msgs.FieldInstructionsAccessible()).Placeholder(msgs.ProfileInstructions()).Disabled(!editable)
		ui.Text(c, msgs.ProfileWorkspace()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
		workspace := preferenceInput(c, &e.Draft.Workspace, e.Errors["Workspace"] != "").Label(msgs.FieldWorkspaceAccessible()).Placeholder(msgs.FieldWorkspacePlaceholder()).Disabled(!editable)
		if e.focusField == "Workspace" {
			workspace.Focus()
			e.focusField = ""
		}
		if message := e.Errors["Workspace"]; message != "" {
			ui.Text(c, profileNoticeText(msgs, message)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
		}
		ui.Text(c, msgs.ProfileWorkspaceHint()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			folder := secondaryButton(c, msgs.WorkFolderChoose()).Disabled(!editable).OnClick(actions.ChooseFolder)
			if e.focusFolder {
				folder.Focus()
				e.focusFolder = false
			}
			secondaryButton(c, msgs.WorkFolderUseDefault()).Disabled(!editable || e.Draft.Workspace == "").OnClick(func() {
				e.Draft.Workspace = ""
				delete(e.Errors, "Workspace")
				e.folderError = ""
			})
		})
		if e.folderError != "" {
			ui.Text(c, msgs.WorkFolderChooseFailed(e.folderError)).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Danger)
		}

		e.builtinToolsView(c, t, editable, actions)

		ui.Column(c).Gap(6).Children(func() {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				secondaryButton(c, msgs.ContactSaveChanges()).Label(msgs.ContactSaveChanges()).Disabled(!editable).OnClick(func() {
					if draft, ok := e.Request(false); ok && actions.Save != nil {
						actions.Save(draft)
					}
				})
				if e.HasConversation && !e.Archived {
					secondaryButton(c, msgs.ProfileSaveAndNew()).Disabled(!editable || e.ActiveWork || e.UnknownWork).OnClick(func() {
						if draft, ok := e.Request(true); ok && actions.SaveAndNew != nil {
							actions.SaveAndNew(draft)
						}
					})
				}
				if !profileDraftEqual(e.Draft, e.initial) && !e.Busy && actions.Discard != nil {
					secondaryButton(c, msgs.ProfileDiscard()).OnClick(actions.Discard)
				}
			})
			ui.Text(c, msgs.ProfileContactManagement()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if e.Archived {
					secondaryButton(c, msgs.ProfileRestore()).Disabled(e.Busy).OnClick(func() {
						if actions.Restore != nil {
							actions.Restore()
						}
					})
				} else {
					secondaryButton(c, msgs.ProfileArchive()).Disabled(e.Busy || e.ActiveWork || e.UnknownWork).OnClick(func() {
						if actions.Archive != nil {
							actions.Archive()
						}
					})
				}
			})
		})
		if e.Busy {
			ui.Text(c, msgs.ProfileSaving()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
		if e.Error != "" {
			ui.Text(c, profileNoticeText(msgs, e.Error)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
		}
		if e.Status != "" {
			ui.Text(c, profileNoticeText(msgs, e.Status)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Text)
		}
	})
}
