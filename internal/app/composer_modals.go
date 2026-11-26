package app

import (
	"errors"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/materials"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

type composerMaterialForm struct {
	owner      *contact
	itemID     string
	name, text string
	err        error
	focusName  bool
}

func (a *appModel) resetComposerOverlays() {
	a.composerAttachMenuOpen = false
	a.focusComposerAttach = false
	a.focusComposerMaterial = ""
	a.composerMaterial = nil
}

func (a *appModel) openComposerMaterial(ct *contact, item materials.Item) {
	if !a.composerMaterialsAllowed(ct) || a.composerMaterial != nil {
		return
	}
	a.composerMaterial = &composerMaterialForm{owner: ct, itemID: item.ID, name: item.Name, text: item.Text, focusName: true}
}

func (a *appModel) composerMaterialDialog(c *ui.Context, t *ui.Theme) {
	form := a.composerMaterial
	if form == nil {
		return
	}
	if !a.composerMaterialsAllowed(form.owner) || a.mainPageActive() || a.priorChats != nil || a.detailPageActive() {
		a.composerMaterial = nil
		return
	}
	tc := &form.owner.Context
	item, found := tc.ItemByID(form.itemID)
	adding := form.itemID == ""
	closeDialog := func() {
		a.composerMaterial = nil
		if adding {
			a.focusComposerAttach = true
		} else {
			a.focusComposerMaterial = item.ID
		}
	}
	if !adding && !found {
		a.composerMaterial = nil
		a.focusComposerAttach = true
		return
	}
	msgs := locales.In(c.Locale())
	title := msgs.ComposerMaterialTitle()
	if adding {
		title = msgs.ComposerTextNoteTitle()
	}
	open := true
	width, _ := c.Size()
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		theme.StyleFrostedModal(backdrop, panel, t)
		panel.Label(title).Width(460).MaxWidth(width - 32).Padding(24).Gap(16)
		pageHeading(c, title)
		if item.Source != "" {
			ui.Text(c, msgs.TaskContextSource(item.Source)).Selectable().FillWidth().MinWidth(0).
				FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
		ui.Column(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, msgs.TaskContextName()).FontSize(typeLabel).FontWeight(500).FixedLineHeight(lineLabel).TextColor(t.Text)
			name := preferenceInput(c, &form.name, errors.Is(form.err, materials.ErrName), a.workTheme.InputBorder).
				FillWidth().Label(msgs.TaskContextNameLabel())
			name.OnChange(func() {
				if errors.Is(form.err, materials.ErrName) {
					form.err = nil
				}
			})
			if form.focusName {
				name.Focus()
				form.focusName = false
			}
		})
		if size := materials.PathReferenceSize(item); size != "" {
			ui.Text(c, msgs.ComposerPathReferenceDetails(size)).FillWidth().MinWidth(0).
				FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		} else {
			ui.Column(c).FillWidth().Gap(6).Children(func() {
				ui.Text(c, msgs.TaskContextText()).FontSize(typeLabel).FontWeight(500).FixedLineHeight(lineLabel).TextColor(t.Text)
				formTextArea(c, &form.text, false, a.workTheme.InputBorder).FillWidth().Height(104).
					Label(msgs.TaskContextTextLabel()).Placeholder(msgs.TaskContextTextPlaceholder())
				if item.Source != "" {
					ui.Text(c, msgs.TaskContextEditHint()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				}
			})
		}
		if form.err != nil {
			ui.Text(c, materials.ErrorText(msgs, form.err)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
		}
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if !adding {
				dangerButton(c, msgs.TaskContextRemove()).MinHeight(32).OnClick(func() {
					if tc.Remove(item.ID) {
						a.composerMaterial = nil
						a.focusComposerAttach = true
					} else {
						form.err = tc.ActionError
						tc.ActionError = nil
					}
				})
				if item.Source != "" {
					secondaryButton(c, msgs.ComposerMaterialRefresh()).MinHeight(32).OnClick(func() {
						refreshed, err := tc.Refresh(item.ID)
						form.err = err
						if err == nil {
							form.text = refreshed.Text
						}
					})
				}
			}
			ui.Spacer(c)
			cancel, save := msgs.ComposerMaterialClose(), msgs.ComposerMaterialSave()
			if adding {
				cancel, save = msgs.TaskContextFormCancel(), msgs.ComposerAddNoteAction()
			}
			secondaryButton(c, cancel).MinHeight(32).OnClick(closeDialog)
			primaryButton(c, save).MinHeight(32).OnClick(func() {
				if adding {
					_, form.err = tc.AddSnippet(form.name, form.text)
				} else {
					_, form.err = tc.Edit(item.ID, form.name, form.text)
				}
				if form.err == nil {
					closeDialog()
				} else if errors.Is(form.err, materials.ErrName) {
					form.focusName = true
				}
			})
		})
	})
	if !open {
		closeDialog()
	}
}
