package app

// App wiring for the task-context component: binding the single persistence
// callback, restoring owned snapshot lists, the bounded editor panel, and
// read-only material rendering for Runtime history.

import (
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/materials"
	"github.com/HeimaoLST/Nagi/locales"
)

const taskContextUncertainSendError = "A previous message may have been sent; delivery was not confirmed before restart. Reload history to verify. No automatic retry."

func (a *appModel) loadStoredTaskContexts(store *contactStore) {
	if store == nil {
		return
	}
	byID := make(map[string]*contact, len(a.contacts))
	for _, ct := range a.contacts {
		byID[ct.ID] = ct
	}
	for _, stored := range store.State.Contacts {
		ct := byID[stored.ID]
		if ct == nil {
			continue
		}
		ct.Context.Items = cloneTaskContextItems(stored.TaskContext)
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
		ct.Context.OnChange = a.persist
		for _, issue := range ct.Issues {
			issue.Context.OnChange = a.persist
		}
	}
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
	target.ChooseFile = func() { a.chooseTaskContextFile(ct, target) }
	target.FileChooserBlocked = a.shutdownStarted || a.dataRecoveryBlocksChanges()
	target.View(c, a.workTheme)
}

func (a *appModel) chooseTaskContextFile(ct *contact, tc *taskContext) {
	currentOwner := func() bool {
		if a.shutdownStarted || a.settingsOpen || a.dataRecoveryBlocksChanges() || a.aux != "task-context" || a.selectedContact() != ct || a.contactByID(ct.ID) != ct {
			return false
		}
		return tc == &ct.Context
	}
	if !currentOwner() || !tc.FormOpen || tc.FormMode != "file" || tc.FileChoosing {
		return
	}
	tc.FileRequest++
	request, form, revision, path := tc.FileRequest, tc.FileForm, tc.FileForm.PathRevision, tc.FormPath
	generation, settingsGeneration := a.contextFileGeneration, a.folderChoiceGeneration
	tc.FileChoosing = true
	tc.FocusFileChooser = false
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
			if tc.FileForm != form || tc.FileRequest != request {
				return
			}
			tc.FileChoosing = false
			if !currentOwner() || a.contextFileGeneration != generation || a.folderChoiceGeneration != settingsGeneration || !tc.FormOpen || tc.FormMode != "file" || form.PathRevision != revision || tc.FormPath != path {
				return
			}
			tc.FocusFileChooser = true
			if err != nil {
				tc.FileChoiceFailed = true
				return
			}
			if len(paths) == 0 {
				return
			}
			tc.FormPath = paths[0]
			form.PathRevision++
			tc.FileChoiceFailed = false
		})
	}()
}

func (a *appModel) composerMaterialsAllowed(ct *contact) bool {
	return !a.shutdownStarted && !a.settingsOpen && !a.dataRecoveryBlocksChanges() &&
		ct != nil && a.selectedContact() == ct && a.contactByID(ct.ID) == ct
}

func (a *appModel) chooseComposerFiles(ct *contact) {
	if !a.composerMaterialsAllowed(ct) || ct.Context.FileChoosing {
		return
	}
	tc := &ct.Context
	tc.FileRequest++
	request := tc.FileRequest
	generation, folderGeneration := a.contextFileGeneration, a.folderChoiceGeneration
	tc.FileChoosing = true
	a.contextFilePickers++
	pick := a.chooseContextFile
	if pick == nil {
		pick = mygo.Dialog.Open
	}
	msgs := locales.In(a.uiLocale)
	defaultPath, _ := os.UserHomeDir()
	opts := mygo.OpenDialogOptions{Parent: a.win, Title: msgs.ComposerAttachFile(), ButtonLabel: msgs.ComposerAttachFilesAction(), DefaultPath: defaultPath, Multiple: true}
	go func() {
		paths, err := pick(opts)
		a.updateUI(func() {
			a.contextFilePickers--
			if tc.FileRequest != request {
				return
			}
			tc.FileChoosing = false
			if !a.composerMaterialsAllowed(ct) || a.contextFileGeneration != generation || a.folderChoiceGeneration != folderGeneration {
				return
			}
			a.focusComposerAttach = true
			if err != nil {
				tc.ActionError = materials.ErrFileChooser
				return
			}
			if len(paths) != 0 {
				a.attachComposerFiles(ct, paths)
			}
		})
	}()
}

func (a *appModel) attachComposerFiles(ct *contact, paths []string) {
	if !a.composerMaterialsAllowed(ct) {
		return
	}
	var firstErr error
	for _, path := range paths {
		if _, err := ct.Context.AttachFile(path, ""); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	ct.Context.ActionError = firstErr
}
