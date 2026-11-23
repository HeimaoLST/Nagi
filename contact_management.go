package main

import (
	"context"
	"strings"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
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
	return profileDraft{Name: ct.Name, Purpose: ct.Purpose, Instructions: ct.Instructions, Workspace: ct.Workspace, BuiltinTools: cloneBuiltinTools(ct.BuiltinTools)}
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
	}
	a.profileEditors[contactID].focusField = ""
	a.contactProfileID = contactID
	a.openDetail(detailProfile)
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
	for _, issue := range ct.Issues {
		if sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) || issue.Work.pendingExecutionID != "" {
			active = true
		}
		if issue.Work.SendState == sendUnknown {
			unknown = true
		}
		if issue.Supplements != nil && issue.Supplements.AutoContinue && issue.Supplements.pendingCount() > 0 {
			active = true
		}
		for _, execution := range issue.Executions {
			if !unresolvedExecution(execution) {
				continue
			}
			active = true
			if execution.Status == issueExecutionUnknown {
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

func (a *appModel) updateProductBot(host *piAppHost, ct *contact, entered profileDraft, startNew bool) bool {
	editor := a.profileEditorFor(ct.ID)
	draft, problems := validateProfileDraft(entered)
	if len(problems) != 0 {
		if editor != nil {
			editor.Draft, editor.Errors = entered, problems
		}
		return false
	}
	if problem := a.validateBuiltinToolSelection(ct.BuiltinTools, draft.BuiltinTools); problem != "" {
		if editor != nil {
			editor.SetError(problem)
		}
		return false
	}
	var codingTools []string
	if draft.BuiltinTools != nil {
		codingTools = *draft.BuiltinTools
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bot, err := host.product.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{BotID: ct.ID, ExpectedRevision: host.productBots[ct.ID].ProfileRevision, OperationID: newContactID()},
		Profile: pi.ProductBotProfile{Name: draft.Name, Role: ct.Role, Purpose: draft.Purpose, Instructions: draft.Instructions,
			Workspace: draft.Workspace, CodingTools: codingTools}, StartNewConversation: startNew,
	})
	if err != nil {
		if editor != nil {
			editor.SetError(profileNoticeErrorSave)
		}
		return false
	}
	projected := productContact(bot)
	ct.Name, ct.Purpose, ct.Instructions, ct.Workspace, ct.BuiltinTools = projected.Name, projected.Purpose, projected.Instructions, projected.Workspace, projected.BuiltinTools
	host.productBots[ct.ID] = bot
	if ct.SessionID != bot.CurrentConversationID {
		ct.SessionID = bot.CurrentConversationID
		ct.Messages, ct.History, ct.Loaded = nil, nil, false
		ct.LoadErr, ct.Unavailable = "", ""
		applyProductExecution(ct, nil)
		a.refreshProductConversation(host, ct, ct.draftRev)
		a.reloadProductConversation(host, ct)
	}
	if editor != nil {
		editor.SetProfile(draft)
		editor.HasConversation = true
		editor.Status = profileNoticeSavedDefaults
	}
	return true
}

func (a *appModel) setProductBotArchived(host *piAppHost, ct *contact, archived bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := pi.ProductBotRevisionParams{BotID: ct.ID, ExpectedRevision: host.productBots[ct.ID].ProfileRevision, OperationID: newContactID()}
	change, failure, success := host.product.RestoreBot, restoreNoticeSave, restoreNoticeSaved
	if archived {
		change, failure, success = host.product.ArchiveBot, archiveNoticeSave, archiveNoticeSaved
	}
	bot, err := change(ctx, params)
	editor := a.profileEditorFor(ct.ID)
	if err != nil {
		if editor != nil {
			editor.SetError(failure)
		}
		return false
	}
	host.productBots[ct.ID], ct.Archived = bot, bot.Archived
	if editor != nil {
		editor.Archived, editor.Status = bot.Archived, success
	}
	return true
}

func (a *appModel) saveContactProfileDraftFor(contactID string, draft profileDraft, startNew bool) bool {
	ct := a.contactByID(contactID)
	editor := a.profileEditorFor(contactID)
	entered := draft
	if ct == nil {
		return false
	}
	if host := a.productHost(); host != nil {
		return a.updateProductBot(host, ct, draft, startNew)
	}
	if !a.demo && a.ensureContactStore() != nil {
		if editor != nil {
			editor.Draft = entered
			editor.SetError(profileNoticeErrorStore)
		}
		return false
	}
	if ct.Archived {
		if editor != nil {
			editor.SetError(profileNoticeArchived)
		}
		return false
	}
	if startNew {
		active, unknown := a.contactActivity(ct)
		if unknown || active {
			if editor != nil {
				editor.SetError(editor.activityReason())
			}
			return false
		}
		if ct.PiBinding == nil && (!a.demo || ct.SessionID == "") {
			if editor != nil {
				editor.SetError(profileNoticeNoConversation)
			}
			return false
		}
	}

	draft, problems := validateProfileDraft(entered)
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
		oldState = a.store.state
	}

	ct.Name = draft.Name
	ct.Purpose = draft.Purpose
	ct.Instructions = draft.Instructions
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
			a.store.state = oldState
		}
		if editor != nil {
			editor.Draft = entered
			editor.SetError(profileNoticeErrorSave)
		}
		return false
	}
	if editor != nil {
		editor.SetProfile(draft)
		editor.HasConversation = ct.PiBinding != nil || a.demo && ct.SessionID != ""
		editor.Status = profileNoticeSavedDefaults
		if startNew {
			editor.HasConversation = false
			editor.Status = profileNoticeSavedNextMessage
		}
	}
	return true
}

func (a *appModel) archiveContact(contactID string) bool {
	ct := a.contactByID(contactID)
	if ct == nil || ct.Archived {
		return false
	}
	if host := a.productHost(); host != nil {
		return a.setProductBotArchived(host, ct, true)
	}
	if !a.demo && a.ensureContactStore() != nil {
		if editor := a.profileEditorFor(contactID); editor != nil {
			editor.SetError(archiveNoticeStore)
		}
		return false
	}
	if reason := a.contactArchiveBlocked(ct); reason != "" {
		if editor := a.profileEditorFor(contactID); editor != nil {
			editor.SetError(reason)
		}
		return false
	}
	old := *ct
	var oldState storedState
	if a.store != nil {
		oldState = a.store.state
	}
	ct.Archived = true
	ct.ArchivedAt = nowMillis()
	if err := a.persist(); err != nil {
		*ct = old
		if a.store != nil {
			a.store.state = oldState
		}
		if editor := a.profileEditorFor(contactID); editor != nil {
			editor.SetError(archiveNoticeSave)
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
			editor.SetError(restoreNoticeStore)
		}
		return false
	}
	old := *ct
	var oldState storedState
	if a.store != nil {
		oldState = a.store.state
	}
	ct.Archived = false
	ct.ArchivedAt = 0
	if err := a.persist(); err != nil {
		*ct = old
		if a.store != nil {
			a.store.state = oldState
		}
		if editor := a.profileEditorFor(contactID); editor != nil {
			editor.SetError(restoreNoticeSave)
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
		if query != "" && !strings.Contains(strings.ToLower(ct.Name+" "+ct.Purpose), query) {
			continue
		}
		out = append(out, i)
	}
	return out
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
