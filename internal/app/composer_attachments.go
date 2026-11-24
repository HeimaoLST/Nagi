package app

import (
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/materials"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

func (a *appModel) composerToolbar(c *ui.Context, t *ui.Theme, ct *contact, sendDisabled bool, submissionStatus string) {
	msgs := locales.In(c.Locale())
	blocked := a.shutdownStarted || a.dataRecoveryBlocksChanges()
	toolbar := ui.Row(c).Key("composer-tools").FillWidth().Height(32).Shrink(0).AlignItems(ui.Center).Gap(8).
		Role(ui.RoleGroup).Label(msgs.ComposerTools())
	toolbar.Children(func() {
		trigger := ui.ButtonBase(c).Key("attach").Size(32, 32).Shrink(0).Center().Radius(8).
			Label(msgs.ComposerAttach()).Disabled(blocked)
		bg, border := a.workTheme.ComposerSurface, a.workTheme.ComposerBorder
		switch {
		case trigger.Pressed() || a.composerAttachMenuOpen:
			bg = t.SurfacePressed
		case trigger.Hovered():
			bg = t.SurfaceHover
		}
		if trigger.Focused() {
			border = t.Focus
		}
		trigger.Background(bg).Border(1, border).TextColor(a.workTheme.ComposerText)
		trigger.Children(func() { ui.Icon(c, composerAttachIcon).Size(16, 16) })
		trigger.PopoverMenuButton(func(menu *ui.Menu) {
			if menu.Item(msgs.ComposerAttachFile()).Disabled(blocked || ct.Context.FileChoosing).Chosen() {
				trigger.Focus()
				a.chooseComposerFiles(ct)
			}
			if menu.Item(msgs.ComposerAddTextNote()).Disabled(blocked).Chosen() {
				trigger.Focus()
				a.openComposerMaterial(ct, materials.Item{})
			}
		}, ui.PopoverMenuButtonOptions{
			At: ui.AnchorTopStart, Self: ui.AnchorBottomStart, Open: &a.composerAttachMenuOpen,
		})
		appTooltip(c, trigger, msgs.ComposerAttach())
		if a.focusComposerAttach {
			trigger.Focus()
			a.focusComposerAttach = false
		}
		if submissionStatus != "" {
			ui.Text(c, submissionStatus).FontSize(typeMeta).FixedLineHeight(lineMeta).SingleLine().
				TextColor(a.workTheme.ComposerMuted).MinWidth(0).Shrink(1)
		}
		ui.Spacer(c)
		if !a.demo && ct.ActiveTaskID != "" {
			stop := appTooltip(c, ui.Button(c, "").Label(msgs.StopConversation()), msgs.StopConversationTooltip()).
				Size(32, 32).Shrink(0).Radius(16).Padding(0).Center().Border(1, t.Border).TextColor(t.Warning)
			stop.Children(func() { ui.Icon(c, composerStopIcon).Size(16, 16) })
			stop.OnClick(a.cancelSelected)
			return
		}
		button := ui.PrimaryButton
		outlined := a.detailIs(detailProfile)
		if outlined {
			button = ui.Button
		}
		send := appTooltip(c, button(c, "").Label(msgs.SendAction()), msgs.SendTooltip()).
			Size(32, 32).Shrink(0).Radius(16).Padding(0).Center().Disabled(sendDisabled)
		if outlined {
			send.Border(1, t.Focus)
		}
		send.Children(func() { ui.Icon(c, composerSendIcon).Size(16, 16) })
		if send.Clicked() {
			a.sendCurrent()
		}
	})
}

func (a *appModel) composerAttachments(c *ui.Context, t *ui.Theme, ct *contact) {
	msgs := locales.In(c.Locale())
	wb := a.workTheme
	blocked := a.shutdownStarted || a.dataRecoveryBlocksChanges()
	c.Key("attachments")
	ui.Scroll(c).FillWidth().MaxHeight(84).Label(msgs.TaskContextHeading()).Children(func() {
		ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
			for _, item := range ct.Context.Items {
				icon := theme.GlyphStickyNoteLine
				tip := item.Name + "\n" + materials.FormatSize(int64(len(item.Text)))
				if item.Source != "" {
					icon = theme.GlyphFileTextLine
					if size := materials.PathReferenceSize(item); size != "" {
						icon = theme.GlyphAttachment2
						tip = item.Name + "\n" + msgs.ComposerPathReferenceDetails(size)
					}
					tip += "\n" + item.Source
				}
				chip := ui.Row(c).Key(item.ID).Height(28).Radius(8).Padding(0, 4, 0, 8).Gap(6).AlignItems(ui.Center).
					Background(wb.ComposerChipBg).Border(1, wb.ComposerBorder)
				chip.Children(func() {
					main := ui.ButtonBase(c).Height(24).Radius(6).AlignItems(ui.Center).Gap(6).Label(item.Name).Disabled(blocked)
					if main.Hovered() {
						main.Background(wb.ComposerChipBg.Mix(wb.ComposerText, .06))
					}
					main.Children(func() {
						ui.Icon(c, icon).Size(14, 14).Shrink(0).TextColor(wb.ComposerMuted)
						ui.Text(c, item.Name).MaxWidth(176).MinWidth(0).MaxLines(1).Ellipsis("…").
							FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(wb.ComposerText)
					})
					if a.focusComposerMaterial == item.ID {
						main.Focus().ScrollIntoView()
						a.focusComposerMaterial = ""
					}
					appTooltip(c, main, tip)
					main.OnClick(func() {
						main.Focus()
						a.openComposerMaterial(ct, item)
					})
					label := msgs.ComposerRemoveAttachment(item.Name)
					composerRemoveButton(c, t, label).TextColor(wb.ComposerMuted).Disabled(blocked).OnClick(func() {
						if a.composerMaterialsAllowed(ct) && ct.Context.Remove(item.ID) {
							a.focusComposerAttach = true
						}
					})
				})
			}
		})
	})
}

func (a *appModel) composerAttachmentError(c *ui.Context, t *ui.Theme, ct *contact) {
	msgs := locales.In(c.Locale())
	ui.Row(c).Key("attachment-error").FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
		ui.Text(c, materials.ErrorText(msgs, ct.Context.ActionError)).Grow(1).MinWidth(0).
			FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
		composerRemoveButton(c, t, msgs.ComposerDismissAttachmentError()).OnClick(func() {
			ct.Context.ActionError = nil
			a.focusComposerAttach = true
		})
	})
}

func composerRemoveButton(c *ui.Context, t *ui.Theme, label string) ui.Element {
	button := ui.ButtonBase(c).Label(label).Size(20, 20).Shrink(0).Radius(6).Center().TextColor(t.TextMuted)
	if button.Pressed() {
		button.Background(t.SurfacePressed)
	} else if button.Hovered() {
		button.Background(t.SurfaceHover)
	}
	button.Children(func() { ui.Icon(c, theme.GlyphCloseLine).Size(12, 12) })
	return appTooltip(c, button, label)
}
