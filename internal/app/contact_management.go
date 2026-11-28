package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

const (
	archiveNoticeAlready = "archive-already"
	archiveNoticeUnknown = "archive-unknown"
	archiveNoticeActive  = "archive-active"
	archiveNoticeStore   = "archive-store"
	archiveNoticeSave    = "archive-save"
	archiveNoticeSaved   = "archive-saved"
	restoreNoticeStore   = "restore-store"
	restoreNoticeSave    = "restore-save"
	restoreNoticeSaved   = "restore-saved"
)

func contactProfileDraft(ct *contact) profileDraft {
	return cloneProfileDraft(profileDraft{Name: ct.Name, Purpose: ct.Purpose, Instructions: ct.Instructions, StarterPrompts: ct.StarterPrompts,
		Workspace: ct.Workspace, BuiltinTools: ct.BuiltinTools, ToolPreset: ct.ToolPreset, MountedSkills: ct.MountedSkills, McpBindings: ct.McpBindings})
}

func (a *appModel) openContactProfile(contactID string) {
	ct := a.contactByID(contactID)
	if ct == nil {
		return
	}
	if a.contactProfileID != contactID {
		a.folderChoiceGeneration++
	}
	if a.profileEditors == nil {
		a.profileEditors = make(map[string]*contactProfileEditor)
	}
	if a.profileEditors[contactID] == nil {
		a.profileEditors[contactID] = newContactProfileEditor(contactProfileDraft(ct))
		a.profileEditors[contactID].profileRevision = ct.ProfileRevision
	}
	a.profileEditors[contactID].focusField = ""
	a.contactProfileID = contactID
	a.openDetail(detailProfile)
	a.loadBotMemory(ct)
	a.loadBotCapabilities(ct)
}

func (a *appModel) profileEditorFor(contactID string) *contactProfileEditor {
	return a.profileEditors[contactID]
}

func (a *appModel) contactActivity(ct *contact) (active, unknown bool) {
	if sessionBusy(ct.SendState, ct.ActiveTaskID) || ct.pendingSend != nil {
		active = true
	}
	if ct.SendState == sendUnknown || (ct.pendingSend != nil && ct.pendingSend.State == "unknown") {
		unknown = true
	}
	for _, activity := range ct.Activities {
		if sessionBusy(activity.Work.SendState, activity.Work.ActiveTaskID) || activity.Work.pendingExecutionID != "" {
			active = true
		}
		if activity.Work.SendState == sendUnknown {
			unknown = true
		}
		if activity.Supplements != nil && activity.Supplements.AutoContinue && activity.Supplements.PendingCount() > 0 {
			active = true
		}
		for _, execution := range activity.Executions {
			if !unresolvedExecution(execution) {
				continue
			}
			active = true
			if execution.Status == activityExecutionUnknown {
				unknown = true
			}
		}
	}
	return active, unknown
}

func (a *appModel) contactArchiveBlocked(ct *contact) string {
	if ct.Archived {
		return archiveNoticeAlready
	}
	active, unknown := a.contactActivity(ct)
	if unknown {
		return archiveNoticeUnknown
	}
	if active {
		return archiveNoticeActive
	}
	return ""
}

func (a *appModel) updateProductBot(host *piAppHost, ct *contact, editor *contactProfileEditor, entered profileDraft, startNew bool) bool {
	draft, problems := validateProfileDraft(entered, ct.Workspace)
	if len(problems) != 0 {
		if editor != nil {
			editor.Draft, editor.Errors = entered, problems
		}
		return false
	}
	if problem := a.validateBuiltinToolSelection(ct.BuiltinTools, draft.BuiltinTools); problem != "" {
		if editor != nil {
			editor.setError(problem)
		}
		return false
	}
	var codingTools []string
	if draft.BuiltinTools != nil && (draft.ToolPreset == "" || draft.ToolPreset == pi.ToolPresetCustom) {
		codingTools = *draft.BuiltinTools
	} else if draft.ToolPreset == pi.ToolPresetCustom {
		codingTools = []string{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if editor != nil && editor.profileRevision != 0 && editor.profileRevision != ct.ProfileRevision {
		editor.setError(profileNoticeChangedElsewhere)
		return false
	}
	bot, err := host.product.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{BotID: ct.ID, ExpectedRevision: ct.ProfileRevision, OperationID: newContactID()},
		Profile: pi.ProductBotProfile{Name: draft.Name, Role: ct.Role, Purpose: draft.Purpose, Instructions: draft.Instructions,
			StarterPrompts: draft.StarterPrompts, Workspace: draft.Workspace, CodingTools: codingTools, ToolPreset: draft.ToolPreset,
			MountedSkills: draft.MountedSkills, McpBindings: draft.McpBindings}, StartNewConversation: startNew,
	})
	if err != nil {
		if editor != nil {
			editor.setError(profileNoticeErrorSave)
		}
		return false
	}
	a.applyProductBot(host, bot, false)
	if editor != nil {
		editor.setProfile(contactProfileDraft(ct))
		editor.profileRevision = ct.ProfileRevision
		editor.Status = profileNoticeSavedDefaults
	}
	a.refreshBotSkills(ct)
	return true
}

func (a *appModel) setProductBotArchived(host *piAppHost, ct *contact, archived bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := pi.ProductBotRevisionParams{BotID: ct.ID, ExpectedRevision: ct.ProfileRevision, OperationID: newContactID()}
	change, failure, success := host.product.RestoreBot, restoreNoticeSave, restoreNoticeSaved
	if archived {
		change, failure, success = host.product.ArchiveBot, archiveNoticeSave, archiveNoticeSaved
	}
	bot, err := change(ctx, params)
	editor := a.profileEditorFor(ct.ID)
	if err != nil {
		if editor != nil {
			editor.setError(failure)
		}
		return false
	}
	a.applyProductBot(host, bot, false)
	if editor != nil {
		editor.Archived, editor.Status = bot.Archived, success
	}
	return true
}

func (a *appModel) saveContactProfileDraftFor(contactID string, draft profileDraft, startNew bool) bool {
	return a.saveContactProfile(a.contactByID(contactID), a.profileEditorFor(contactID), draft, startNew)
}

func (a *appModel) saveContactProfile(ct *contact, editor *contactProfileEditor, draft profileDraft, startNew bool) bool {
	entered := draft
	if ct == nil {
		return false
	}
	if host := a.productHost(); host != nil {
		return a.updateProductBot(host, ct, editor, draft, startNew)
	}
	if !a.demo && a.ensureContactStore() != nil {
		if editor != nil {
			editor.Draft = entered
			editor.setError(profileNoticeErrorStore)
		}
		return false
	}
	if ct.Archived {
		if editor != nil {
			editor.setError(profileNoticeArchived)
		}
		return false
	}
	if startNew {
		active, unknown := a.contactActivity(ct)
		if unknown || active {
			if editor != nil {
				editor.setError(editor.activityReason())
			}
			return false
		}
		if ct.PiBinding == nil && (!a.demo || ct.SessionID == "") {
			if editor != nil {
				editor.setError(profileNoticeNoConversation)
			}
			return false
		}
	}

	draft, problems := validateProfileDraft(entered, ct.Workspace)
	if len(problems) > 0 {
		if editor != nil {
			editor.Draft = entered
			editor.Errors = problems
			editor.Error = ""
			editor.Status = ""
		}
		return false
	}
	if problem := a.validateBuiltinToolSelection(ct.BuiltinTools, draft.BuiltinTools); problem != "" {
		if editor != nil {
			editor.Draft = entered
			editor.Error = problem
			editor.Status = ""
		}
		return false
	}
	previous := *ct
	var oldState storedState
	if a.store != nil {
		oldState = a.store.State
	}

	ct.Name = draft.Name
	ct.Purpose = draft.Purpose
	ct.Instructions = draft.Instructions
	ct.StarterPrompts = slices.Clone(draft.StarterPrompts)
	ct.Workspace = draft.Workspace
	ct.BuiltinTools = cloneBuiltinTools(draft.BuiltinTools)
	if startNew {
		if binding := clonePiConversationBinding(ct.PiBinding); binding != nil {
			ct.PreviousPiBindings = append(ct.PreviousPiBindings, *binding)
			ct.PiBinding = nil
		}
		ct.SessionID = ""
		ct.Loaded = false
		ct.LoadErr = ""
		ct.Unavailable = ""
		ct.ActiveTaskID = ""
		ct.LastTaskID = ""
		ct.SendState = sendIdle
		ct.clearSendError()
		ct.SubmittedDraft = ""
		ct.SubmittedRev = 0
		ct.pendingSend = nil
		ct.Messages = nil
		ct.History = nil
		ct.Works = nil
		ct.Status = statusReady
		ct.Work = ""
		ct.WorkLabel = "Preparing a reply"
		ct.SessionWorkingDir = ""
		ct.SessionModel = ""
		ct.SessionAPI = ""
		ct.SessionState = ""
	}
	if err := a.persist(); err != nil {
		*ct = previous
		if a.store != nil {
			a.store.State = oldState
		}
		if editor != nil {
			editor.Draft = entered
			editor.setError(profileNoticeErrorSave)
		}
		return false
	}
	if editor != nil {
		editor.setProfile(draft)
		editor.Status = profileNoticeSavedDefaults
		if startNew {
			editor.Status = profileNoticeSavedNextMessage
		}
	}
	return true
}

func (a *appModel) archiveContact(contactID string) bool {
	ct := a.contactByID(contactID)
	if ct == nil || ct.isBuiltin() || ct.Archived {
		return false
	}
	if host := a.productHost(); host != nil {
		return a.setProductBotArchived(host, ct, true)
	}
	if !a.demo && a.ensureContactStore() != nil {
		if editor := a.profileEditorFor(contactID); editor != nil {
			editor.setError(archiveNoticeStore)
		}
		return false
	}
	if reason := a.contactArchiveBlocked(ct); reason != "" {
		if editor := a.profileEditorFor(contactID); editor != nil {
			editor.setError(reason)
		}
		return false
	}
	old := *ct
	var oldState storedState
	if a.store != nil {
		oldState = a.store.State
	}
	ct.Archived = true
	ct.ArchivedAt = nowMillis()
	if err := a.persist(); err != nil {
		*ct = old
		if a.store != nil {
			a.store.State = oldState
		}
		if editor := a.profileEditorFor(contactID); editor != nil {
			editor.setError(archiveNoticeSave)
		}
		return false
	}
	if editor := a.profileEditorFor(contactID); editor != nil {
		editor.Archived = true
		editor.Status = archiveNoticeSaved
	}
	return true
}

func (a *appModel) restoreContact(contactID string) bool {
	ct := a.contactByID(contactID)
	if ct == nil || !ct.Archived {
		return false
	}
	if host := a.productHost(); host != nil {
		return a.setProductBotArchived(host, ct, false)
	}
	if !a.demo && a.ensureContactStore() != nil {
		if editor := a.profileEditorFor(contactID); editor != nil {
			editor.setError(restoreNoticeStore)
		}
		return false
	}
	old := *ct
	var oldState storedState
	if a.store != nil {
		oldState = a.store.State
	}
	ct.Archived = false
	ct.ArchivedAt = 0
	if err := a.persist(); err != nil {
		*ct = old
		if a.store != nil {
			a.store.State = oldState
		}
		if editor := a.profileEditorFor(contactID); editor != nil {
			editor.setError(restoreNoticeSave)
		}
		return false
	}
	if editor := a.profileEditorFor(contactID); editor != nil {
		editor.Archived = false
		editor.Status = restoreNoticeSaved
	}
	return true
}

func (a *appModel) visibleContactIndices() []int {
	query := strings.ToLower(strings.TrimSpace(a.contactSearch))
	out := make([]int, 0, len(a.contacts))
	for i, ct := range a.contacts {
		if a.hideBuiltinArchitect && ct.isBuiltin() {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(ct.Name+" "+ct.Purpose), query) {
			continue
		}
		out = append(out, i)
	}
	return out
}

func (a *appModel) setBuiltinArchitectHidden(hidden bool) {
	if hidden == a.hideBuiltinArchitect || !a.canChangeBuiltinArchitectVisibility() {
		return
	}
	a.hideBuiltinArchitect = hidden
	if hidden && len(a.contacts) > 0 && a.selectedContact().isBuiltin() {
		for _, index := range a.visibleContactIndices() {
			if !a.contacts[index].Archived {
				a.selectContact(index)
				return
			}
		}
	}
	a.persist()
}

func (a *appModel) canChangeBuiltinArchitectVisibility() bool {
	return !a.shutdownStarted && !a.dataRecoveryBlocksChanges() && (a.demo || a.productHost() != nil)
}

func (a *appModel) builtinArchitectVisibilityLabel(msgs locales.Messages) string {
	if a.hideBuiltinArchitect {
		return msgs.BotMenuShowInTeam()
	}
	return msgs.BotMenuHideFromTeam()
}

func (a *appModel) showBuiltinArchitectInRail() {
	a.contactSearch = ""
	a.setBuiltinArchitectHidden(false)
}

func (a *appModel) historyAsyncStarted() {
	a.contactHistoryInFlight++
}

func (a *appModel) historyAsyncFinished() {
	if a.contactHistoryInFlight > 0 {
		a.contactHistoryInFlight--
	}
	if a.shutdownStarted && !a.quitReady {
		a.finishShutdown()
	}
}
