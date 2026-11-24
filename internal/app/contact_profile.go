package app

// Contact profile draft editor and validation.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

type contactProfileActions struct {
	Save    func(profileDraft)
	Archive func()
	Restore func()
	Discard func()
}

type contactProfileEditor struct {
	Draft           profileDraft
	Archived        bool
	ActiveWork      bool
	UnknownWork     bool
	Busy            bool
	Errors          map[string]string
	Error           string
	Status          string
	focusField      string
	initial         profileDraft
	profileRevision uint64
	builtin         bool
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

const profileNoticeChangedElsewhere = "profile-changed-elsewhere"

func profileNoticeText(msgs locales.Messages, notice string) string {
	switch notice {
	case profileNoticeChangedElsewhere:
		return msgs.ProfileChangedElsewhere()
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
}

func validateProfileDraft(draft profileDraft, savedWorkspace string) (profileDraft, map[string]string) {
	problems := map[string]string{}
	draft.Name = strings.TrimSpace(draft.Name)
	if draft.Name == "" {
		problems["Name"] = profileNoticeNameRequired
	}
	if draft.Workspace != savedWorkspace {
		draft.Workspace = strings.TrimSpace(draft.Workspace)
	}
	if draft.Workspace != savedWorkspace && draft.Workspace != "" {
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
	draft, errors := validateProfileDraft(e.Draft, e.initial.Workspace)
	e.Errors = errors
	e.Error = ""
	if len(e.Errors) > 0 {
		e.Status = ""
		if e.Errors["Name"] != "" {
			e.focusField = "Name"
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

func (e *contactProfileEditor) view(c *ui.Context, t *ui.Theme, actions contactProfileActions, inputBorder ...ui.Color) {
	border := c.Theme().Border
	if len(inputBorder) > 0 {
		border = inputBorder[0]
	}
	msgs := locales.In(c.Locale())
	editable := !e.Busy && !e.Archived
	ui.Column(c).Key("contact-profile-editor").FillWidth().MinWidth(0).Gap(8).Children(func() {
		if reason := e.activityReason(); reason != "" {
			ui.Text(c, profileNoticeText(msgs, reason)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
		}
		if e.builtin {
			ui.Text(c, msgs.ProfileBuiltinArchitectNote()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
		if e.Archived {
			ui.Text(c, msgs.ProfileArchivedBadge()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Warning)
		}

		settingsPreferenceRow(c, t, msgs.FieldName(), "", func() {
			name := preferenceInput(c, &e.Draft.Name, e.Errors["Name"] != "", border).FillWidth().MinWidth(0).Label(msgs.FieldNameAccessible()).Placeholder(msgs.FieldNamePlaceholder()).Disabled(!editable)
			if e.focusField == "Name" {
				name.Focus()
				e.focusField = ""
			}
			if message := e.Errors["Name"]; message != "" {
				ui.Text(c, profileNoticeText(msgs, message)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
			}
		})
		settingsPreferenceRow(c, t, msgs.ProfilePurpose(), "", func() {
			formTextArea(c, &e.Draft.Purpose, false, border).FillWidth().MinWidth(0).Label(msgs.FieldPurposeAccessible()).Placeholder(msgs.ProfilePurpose()).Disabled(!editable)
		})
		settingsPreferenceRow(c, t, msgs.ProfileInstructions(), "", func() {
			formTextArea(c, &e.Draft.Instructions, false, border).FillWidth().MinWidth(0).Label(msgs.FieldInstructionsAccessible()).Placeholder(msgs.ProfileInstructions()).Disabled(!editable)
		}, true)

		ui.Column(c).Gap(6).Children(func() {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				primaryButton(c, msgs.ContactSaveChanges()).Label(msgs.ContactSaveChanges()).Disabled(!editable).OnClick(func() {
					if draft, ok := e.Request(false); ok && actions.Save != nil {
						actions.Save(draft)
					}
				})
				if !profileDraftEqual(e.Draft, e.initial) && !e.Busy && actions.Discard != nil {
					secondaryButton(c, msgs.ProfileDiscard()).OnClick(actions.Discard)
				}
			})
			if !profileDraftEqual(e.Draft, e.initial) {
				ui.Text(c, msgs.SettingsUnsavedChanges()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
			}
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
		if e.builtin {
			return
		}
		ui.Column(c).Gap(8).Padding(8, 0, 0).Children(func() {
			ui.Text(c, msgs.ProfileContactManagement()).FontSize(typeLabel).FontWeight(500).FixedLineHeight(lineLabel).TextColor(t.Text)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if e.Archived {
					secondaryButton(c, msgs.ProfileRestore()).Disabled(e.Busy).OnClick(func() {
						if actions.Restore != nil {
							actions.Restore()
						}
					})
				} else {
					dangerButton(c, msgs.ProfileArchive()).Disabled(e.Busy || e.ActiveWork || e.UnknownWork).OnClick(func() {
						if actions.Archive != nil {
							actions.Archive()
						}
					})
				}
			})
		})
	})
}

func (a *appModel) contactProfileForm(c *ui.Context, t *ui.Theme, ct *contact, editor *contactProfileEditor) {
	editor.Archived, editor.builtin = ct.Archived, ct.isBuiltin()
	editor.ActiveWork, editor.UnknownWork = a.contactActivity(ct)
	id := ct.ID
	editor.view(c, t, contactProfileActions{
		Save:    func(draft profileDraft) { a.saveContactProfileDraftFor(id, draft, false) },
		Archive: func() { a.archiveContact(id) },
		Restore: func() { a.restoreContact(id) },
		Discard: func() {
			editor.SetProfile(contactProfileDraft(ct))
			editor.profileRevision = ct.ProfileRevision
			editor.ResetDraft()
		},
	}, a.workTheme.InputBorder)
}
