package app

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

type createBotForm struct {
	name, purpose                   string
	submittedName, submittedPurpose string
	operationID                     string
	focusName, invalidName, failed  bool
}

func (a *appModel) openCreateBotDialog() {
	if a.createBot != nil || a.shutdownStarted || a.dataRecoveryBlocksChanges() {
		return
	}
	a.createBot = &createBotForm{name: locales.In(a.uiLocale).NewContactName(len(a.contacts) + 1)}
}

func (a *appModel) submitCreateBot(form *createBotForm) {
	name, purpose := strings.TrimSpace(form.name), strings.TrimSpace(form.purpose)
	form.invalidName, form.failed = name == "", false
	if form.invalidName {
		form.focusName = true
		return
	}
	if form.submittedName != name || form.submittedPurpose != purpose {
		form.submittedName, form.submittedPurpose = name, purpose
		form.operationID = newContactID()
	}
	if err := a.createContact(pi.ProductBotProfile{Name: name, Purpose: purpose}, form.operationID); err != nil {
		form.failed = true
		return
	}
	a.createBot = nil
	if a.activeDetail() != "" {
		a.closeDetail()
		a.focusProfileRow = ""
	}
}

func (a *appModel) createBotWithArchitect(bot *contact) {
	a.createBot = nil
	if a.activeDetail() != "" {
		a.closeDetail()
		a.focusProfileRow, a.focusRail = "", false
	}
	a.selectContact(slices.Index(a.contacts, bot))
	a.aux, a.priorChats, a.focusComposer = "", nil, true
}

func (a *appModel) createBotDialogView(c *ui.Context, t *ui.Theme) {
	form := a.createBot
	if form == nil {
		return
	}
	msgs := locales.In(c.Locale())
	architect := slices.IndexFunc(a.contacts, func(ct *contact) bool { return ct.SystemKind == pi.SystemKindBotArchitect && !ct.Archived })
	width, _ := c.Size()
	open := true
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		theme.StyleFrostedModal(backdrop, panel, t)
		panel.Label(msgs.CreateBotTitle()).Width(440).MaxWidth(width - 32).Padding(24).Gap(16)
		pageHeading(c, msgs.CreateBotTitle())
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, msgs.FieldName()).FontSize(typeLabel).FontWeight(500).FixedLineHeight(lineLabel).TextColor(t.Text)
			name := preferenceInput(c, &form.name, form.invalidName, a.workTheme.InputBorder).
				FillWidth().Label(msgs.FieldNameAccessible())
			if form.focusName {
				name.Focus()
				form.focusName = false
			}
			if form.invalidName {
				ui.Text(c, msgs.ValidationNameRequired()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
			}
		})
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, msgs.ProfilePurpose()).FontSize(typeLabel).FontWeight(500).FixedLineHeight(lineLabel).TextColor(t.Text)
			formTextArea(c, &form.purpose, false, a.workTheme.InputBorder).FillWidth().
				Label(msgs.FieldPurposeAccessible()).Placeholder(msgs.CreateBotPurposePlaceholder())
		})
		if form.failed {
			ui.Text(c, msgs.CreateBotFailed()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
		}
		footer := ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.End).Gap(8).Wrap()
		if architect >= 0 {
			footer.Justify(ui.SpaceBetween)
		}
		footer.Children(func() {
			if architect >= 0 {
				bot := a.contacts[architect]
				linkButton(c, msgs.CreateBotWithPandoraBox()).OnClick(func() { a.createBotWithArchitect(bot) })
			}
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				secondaryButton(c, msgs.CreateBotCancel()).MinHeight(32).OnClick(func() { a.createBot = nil })
				primaryButton(c, msgs.CreateBotAction()).MinHeight(32).OnClick(func() { a.submitCreateBot(form) })
			})
		})
	})
	if !open {
		a.createBot = nil
	}
}
