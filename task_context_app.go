package main

// App wiring for the task-context component: binding the single persistence
// callback, restoring owned snapshot lists, the bounded editor panel, and
// read-only material rendering for Runtime history.

import (
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

const taskContextUncertainSendError = "A previous message may have been sent; delivery was not confirmed before restart. Reload history to verify. No automatic retry."

func clonePendingSend(send *pendingSend) *pendingSend {
	if send == nil {
		return nil
	}
	return &pendingSend{Draft: send.Draft, Context: cloneTaskContextItems(send.Context), State: send.State, ToolScopeRef: send.ToolScopeRef}
}

func (a *appModel) loadStoredTaskContexts(store *contactStore) {
	if store == nil {
		return
	}
	byID := make(map[string]*contact, len(a.contacts))
	for _, ct := range a.contacts {
		byID[ct.ID] = ct
	}
	for _, stored := range store.state.Contacts {
		ct := byID[stored.ID]
		if ct == nil {
			continue
		}
		ct.Context.items = cloneTaskContextItems(stored.TaskContext)
		ct.pendingSend = clonePendingSend(stored.PendingSend)
		if ct.pendingSend != nil && (ct.pendingSend.State == "submitting" || ct.pendingSend.State == "unknown") {
			if ct.SessionID == "" {
				ct.pendingSend = nil
			} else {
				// A restored in-flight intent has the same uncertainty as a
				// persisted unknown one; normalize it so automatic reload does
				// not silently retire it.
				ct.pendingSend.State = "unknown"
				ct.SendState = sendUnknown
				ct.Status = statusAttention
				ct.setSendError(taskContextUncertainSendError, "send-uncertain", "")
				ct.setStatusDetail(ct.SendError, ct.SendErrorKind)
			}
		}
	}
}

func (a *appModel) bindTaskContexts() {
	for _, ct := range a.contacts {
		ct.Context.onChange = a.persist
		for _, issue := range ct.Issues {
			issue.Context.onChange = a.persist
		}
	}
}

func (a *appModel) openComposerTaskContext() {
	a.composerPanel = composerPanelContext
	a.openConversationTaskContext()
}

func (a *appModel) openConversationTaskContext() {
	a.contextFileGeneration++
	a.aux = "task-context"
}

func (a *appModel) taskContextPanel(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	owner := locales.In(c.Locale()).TaskContextOwnerConversation(ct.Name)
	target := &ct.Context
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, owner).FontSize(typeSettingsTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text).MaxLines(2)
		ui.Spacer(c)
		secondaryButton(c, locales.In(c.Locale()).TaskContextClose()).OnClick(func() { a.closeAux() })
	})
	target.chooseFile = func() { a.chooseTaskContextFile(ct, target) }
	target.fileChooserBlocked = a.shutdownStarted || a.dataRecoveryBlocksChanges()
	target.view(c, a.workTheme)
}

func (a *appModel) chooseTaskContextFile(ct *contact, tc *taskContext) {
	currentOwner := func() bool {
		if a.shutdownStarted || a.settingsOpen || a.dataRecoveryBlocksChanges() || a.aux != "task-context" || a.selectedContact() != ct || a.contactByID(ct.ID) != ct {
			return false
		}
		return tc == &ct.Context
	}
	if !currentOwner() || !tc.formOpen || tc.formMode != "file" || tc.fileChoosing {
		return
	}
	tc.fileRequest++
	request, form, revision, path := tc.fileRequest, tc.fileForm, tc.fileForm.pathRevision, tc.formPath
	generation, settingsGeneration := a.contextFileGeneration, a.folderChoiceGeneration
	tc.fileChoosing = true
	tc.focusFileChooser = false
	a.contextFilePickers++
	pick := a.chooseContextFile
	if pick == nil {
		pick = mygo.Dialog.Open
	}
	msgs := locales.In(a.uiLocale)
	opts := mygo.OpenDialogOptions{Parent: a.win, Title: msgs.TaskContextChooseFile(), ButtonLabel: msgs.TaskContextChooseFile()}
	if filepath.IsAbs(path) {
		opts.DefaultPath = path
	} else {
		opts.DefaultPath, _ = os.UserHomeDir()
	}
	go func() {
		paths, err := pick(opts)
		a.updateUI(func() {
			a.contextFilePickers--
			if tc.fileForm != form || tc.fileRequest != request {
				return
			}
			tc.fileChoosing = false
			if !currentOwner() || a.contextFileGeneration != generation || a.folderChoiceGeneration != settingsGeneration || !tc.formOpen || tc.formMode != "file" || form.pathRevision != revision || tc.formPath != path {
				return
			}
			tc.focusFileChooser = true
			if err != nil {
				tc.fileChoiceFailed = true
				return
			}
			if len(paths) == 0 {
				return
			}
			tc.formPath = paths[0]
			form.pathRevision++
			tc.fileChoiceFailed = false
		})
	}()
}
