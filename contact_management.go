package main

import (
	"context"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

// Contact management and saved-context notices are stored by kind and said
// again at render time in the current language.
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

	historyNoticeMissingQuery = "history-missing-query"
	historyNoticeRuntime      = "history-runtime"
	historyNoticePartial      = "history-partial"
	historyNoticeNotSaved     = "history-not-saved"
	historyNoticeReadFailed   = "history-read-failed"
	historyNoticeMoved        = "history-moved"
)

func contactHistoryNoticeText(msgs locales.Messages, notice, detail string) string {
	text := ""
	switch notice {
	case historyNoticeMissingQuery:
		text = msgs.HistorySearchMissingQuery()
	case historyNoticeRuntime:
		text = msgs.HistorySearchUnavailable()
	case historyNoticePartial:
		text = msgs.HistorySearchPartial()
	case historyNoticeNotSaved:
		text = msgs.HistoryMessageNotSaved()
	case historyNoticeReadFailed:
		text = msgs.HistoryReadFailed()
	case historyNoticeMoved:
		text = msgs.HistoryMessageMoved()
	default:
		text = notice
	}
	if detail != "" {
		text += " " + detail
	}
	return text
}

type contactHistoryFull struct {
	WorkingDirectory string
	Origin           string
	SessionID        string
	EntryID          string
	Date             string
	From             string
	Text             string
	Context          []taskContextItem
}

const (
	historyOriginSaved  = "saved"
	historyOriginLoaded = "loaded"
)

func historyOriginKey(origin, sessionID, entryID string) string {
	return origin + "\x00" + sessionID + "\x00" + entryID
}

func contactProfileDraft(ct *contact) profileDraft {
	return profileDraft{Name: ct.Name, Purpose: ct.Purpose, Instructions: ct.Instructions, Workspace: ct.Workspace, BuiltinTools: cloneBuiltinTools(ct.BuiltinTools)}
}

func (a *appModel) openContactProfile(contactID string) {
	ct := a.contactByID(contactID)
	if ct == nil {
		return
	}
	a.settingsContactID = ct.ID
	a.openDetail(detailProfile)
}

func (a *appModel) editContactProfile() {
	ct := a.selectedContact()
	if ct == nil || a.activeDetail() != detailProfile {
		return
	}
	a.settingsContactID = ct.ID
	a.ensureSettingsProfileEditor(ct.ID)
	a.openSettingsTab(settingsTabContact, settingsSourceProfile)
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

func (a *appModel) saveContactProfileDraft(draft profileDraft, startNew bool) bool {
	ct := a.selectedContact()
	if ct == nil {
		return false
	}
	return a.saveContactProfileDraftFor(ct.ID, draft, startNew)
}

func (a *appModel) saveContactProfileDraftFor(contactID string, draft profileDraft, startNew bool) bool {
	ct := a.contactByID(contactID)
	editor := a.profileEditorFor(contactID)
	entered := draft
	if ct == nil {
		return false
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
		ct.Revealed = nil
		ct.Works = nil
		ct.SelectedWorkTaskID = ""
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

func (a *appModel) searchContactHistoryAsync(query string) {
	if a.shutdownStarted {
		return
	}
	ct := a.selectedContact()
	query = strings.TrimSpace(query)
	if query == "" {
		a.contactHistoryError = historyNoticeMissingQuery
		a.contactHistoryErrorDetail = ""
		return
	}
	if ct == nil || a.pi == nil {
		a.contactHistoryError = historyNoticeRuntime
		a.contactHistoryErrorDetail = ""
		return
	}
	a.contactHistoryGen++
	a.contactHistoryDetailGen++
	generation := a.contactHistoryGen
	contactID := ct.ID
	h, hostGeneration := a.pi, a.connectionGeneration
	bindings := ct.piHistoryBindings()
	a.contactHistoryBusy = true
	a.contactHistoryDetailBusy = false
	a.contactHistoryFull = nil
	a.contactHistoryError = ""
	a.contactHistoryErrorDetail = ""
	a.historyAsyncStarted()
	go func() {
		result := a.searchContactHistory(h, bindings, query)
		a.updateUI(func() {
			defer a.historyAsyncFinished()
			if a.pi != h || a.connectionGeneration != hostGeneration || generation != a.contactHistoryGen ||
				a.contactByID(contactID) != ct || a.selectedContact() != ct {
				return
			}
			a.contactHistory = &result
			a.contactHistoryBusy = false
			if len(result.Failures) > 0 {
				a.contactHistoryError = historyNoticePartial
				a.contactHistoryErrorDetail = a.connectionSafeMessage(result.Failures[0].Err)
			}
		})
	}()
}

func (a *appModel) openContactHistoryMatch(match contactHistoryMatch) bool {
	if a.shutdownStarted {
		return false
	}
	ct := a.selectedContact()
	if ct == nil || a.pi == nil {
		a.contactHistoryError = historyNoticeRuntime
		a.contactHistoryErrorDetail = ""
		return false
	}
	bindings := ct.piHistoryBindings()
	var binding *piConversationBinding
	for i := range bindings {
		if bindings[i].Context.SessionRef == match.SessionID {
			binding = &bindings[i]
			break
		}
	}
	if binding == nil {
		a.contactHistoryError = historyNoticeNotSaved
		a.contactHistoryErrorDetail = ""
		return false
	}
	h, hostGeneration := a.pi, a.connectionGeneration
	searchGen := a.contactHistoryGen
	a.contactHistoryDetailGen++
	detailGen := a.contactHistoryDetailGen
	a.contactHistoryDetailBusy = true
	a.contactHistoryFull = nil
	a.contactHistoryError = ""
	a.contactHistoryErrorDetail = ""
	a.historyAsyncStarted()
	go func() {
		projection, err := h.loadPiHistory(context.Background(), *binding)
		var full *contactHistoryFull
		errorText := ""
		if err != nil {
			errorText = historyNoticeReadFailed
		} else {
			for _, entry := range projection.entries {
				if entry.ID == match.EntryID {
					full = &contactHistoryFull{
						WorkingDirectory: binding.Selection.CWD,
						Origin:           historyOriginSaved, SessionID: match.SessionID, EntryID: entry.ID, Date: entry.Date,
						From: entry.From, Text: entry.Text, Context: entry.Context,
					}
					break
				}
			}
			if full == nil {
				errorText = historyNoticeMoved
			}
		}
		a.updateUI(func() {
			defer a.historyAsyncFinished()
			a.applyContactHistoryFull(h, hostGeneration, ct, searchGen, detailGen, full, errorText, err, projection.detail)
		})
	}()
	return true
}

func (a *appModel) applyContactHistoryFull(h *piAppHost, hostGeneration int, ct *contact,
	searchGen, detailGen int, full *contactHistoryFull, errorText string, readErr error, projectionDetail string) {
	if a.pi != h || a.connectionGeneration != hostGeneration ||
		a.contactHistoryGen != searchGen || a.contactHistoryDetailGen != detailGen ||
		a.contactByID(ct.ID) != ct || a.selectedContact() != ct {
		return
	}
	carrierOK := a.aux == "" || a.composerInlineAux()
	if !a.detailIs(detailHistory) || !carrierOK || a.settingsOpen {
		a.contactHistoryDetailBusy = false
		return
	}
	a.contactHistoryDetailBusy = false
	if errorText != "" {
		a.contactHistoryError = errorText
		if readErr != nil {
			a.contactHistoryErrorDetail = a.connectionSafeMessage(readErr)
		} else {
			a.contactHistoryErrorDetail = ""
		}
		return
	}
	if projectionDetail != "" {
		a.contactHistoryError = historyNoticePartial
		a.contactHistoryErrorDetail = projectionDetail
	}
	a.contactHistoryFull = full
	a.historyFullScroll = ui.ScrollState{}
	a.focusHistoryFullBack = full != nil
}

func (a *appModel) openHistoryEntryFull(e historyEntry) {
	ct := a.selectedContact()
	if ct == nil {
		return
	}
	if !a.demo && ct.PiBinding == nil {
		a.contactHistoryError = historyNoticeNotSaved
		a.contactHistoryErrorDetail = ""
		return
	}
	workingDirectory, sessionID := ct.SessionWorkingDir, ct.SessionID
	if binding := ct.PiBinding; binding != nil {
		workingDirectory, sessionID = binding.Selection.CWD, binding.Context.SessionRef
	}
	a.contactHistoryFull = &contactHistoryFull{
		WorkingDirectory: workingDirectory, Origin: historyOriginLoaded, SessionID: sessionID, EntryID: e.ID, Date: e.Date, From: e.From,
		Text: e.Text, Context: cloneTaskContextItems(e.Context),
	}
	a.historyFullScroll = ui.ScrollState{}
	a.focusHistoryFullBack = true
}
