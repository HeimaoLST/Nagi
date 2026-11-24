package app

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

func (a *appModel) emptyState(c *ui.Context, t *ui.Theme, ct *contact) {
	msgs := locales.In(c.Locale())
	unloaded := !a.demo && ct.SessionID != "" && !ct.Loaded
	if !unloaded && !ct.isBuiltin() && len(ct.StarterPrompts) == 0 && !ct.memory.loaded && !ct.memory.loading && !ct.memory.loadFailed {
		a.loadBotMemory(ct)
	}
	ui.Column(c).Key(ct.ID).FillHeight().FillWidth().MinWidth(0).MaxWidth(conversationMaxWidth).Padding(16, 16, 8, 16).Children(func() {
		if unloaded {
			ui.Column(c).Fill().AlignItems(ui.Center).Justify(ui.Center).Gap(8).Children(func() {
				ui.Text(c, msgs.HistoryNotLoadedTitle()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text).TextAlign(ui.Center)
				ui.Text(c, msgs.HistoryNotLoadedHint()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted).TextAlign(ui.Center)
				if !a.canReloadPiConversation(ct) {
					ui.Text(c, msgs.HistoryRuntimeNotReady()).FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Warning).TextAlign(ui.Center)
				}
			})
			return
		}
		ui.Spacer(c)
		ui.Column(c).FillWidth().AlignItems(ui.Center).Gap(10).Children(func() {
			contactAvatar(c, t, ct, 48)
			ui.Text(c, msgs.EmptyBotGreeting(ct.Name)).MaxWidth(480).MaxLines(2).TextAlign(ui.Center).
				FontSize(typePageTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text)
			if purpose := localizedBotPurpose(ct, msgs); purpose != "" {
				ui.Text(c, purpose).MaxWidth(480).MaxLines(2).TextAlign(ui.Center).
					FontSize(typeAction).FixedLineHeight(lineCompact).TextColor(t.TextMuted)
			}
		})
		ui.Spacer(c)
		if prompts := starterPromptsFor(ct, msgs); len(prompts) != 0 {
			ui.Column(c).FillWidth().Gap(2).Padding(0, 0, 4, 0).Children(func() {
				for _, prompt := range prompts {
					a.starterPromptRow(c, t, ct, prompt)
				}
			})
		}
	})
}

func starterPromptsFor(ct *contact, msgs locales.Messages) []string {
	if ct.isBuiltin() {
		defaults := pandoraBoxStarters(msgs)
		if len(ct.StarterPrompts) == 0 {
			return defaults[:]
		}
		english := pandoraBoxStarters(locales.In("en"))
		if defaults == english {
			return ct.StarterPrompts
		}
		prompts := slices.Clone(ct.StarterPrompts)
		for i, prompt := range prompts {
			if index := slices.Index(english[:], prompt); index >= 0 {
				prompts[i] = defaults[index]
			}
		}
		return prompts
	}
	if len(ct.StarterPrompts) > 0 {
		return ct.StarterPrompts
	}
	var prompts []string
	for _, log := range ct.memory.data.WorkLogs {
		if summary := truncate(log.Summary, 100); summary != "" && log.Date != "" {
			prompt := msgs.StarterFollowUp(summary)
			if !slices.Contains(prompts, prompt) {
				prompts = append(prompts, prompt)
			}
			if len(prompts) == 2 {
				break
			}
		}
	}
	if purpose := truncate(ct.Purpose, 100); purpose != "" {
		prompts = append(prompts, msgs.StarterHelpWithPurpose(purpose))
	}
	return prompts
}

func pandoraBoxStarters(msgs locales.Messages) [3]string {
	return [3]string{msgs.PandoraBoxStarterWriting(), msgs.PandoraBoxStarterCodeReview(), msgs.PandoraBoxStarterAudit()}
}

func (a *appModel) starterPromptRow(c *ui.Context, t *ui.Theme, ct *contact, prompt string) {
	button := ui.ButtonBase(c).Key(prompt).Label(prompt).FillWidth().MinWidth(0).MinHeight(34).Padding(6, 10).Radius(8).
		Justify(ui.Start).AlignItems(ui.Center).Gap(10).Background(ui.Transparent).BorderWidth(0).
		Disabled(ct.Archived || a.shutdownStarted || a.dataRecoveryBlocksChanges())
	ink := t.TextMuted
	switch {
	case button.Pressed():
		button.Background(t.SurfacePressed)
		ink = t.Text
	case button.Hovered():
		button.Background(t.SurfaceHover)
		ink = t.Text
	}
	button.Children(func() {
		ui.Icon(c, theme.GlyphChat3Line).Size(15, 15).Shrink(0).TextColor(ink).IgnoreInput(true)
		ui.Text(c, prompt).Grow(1).MinWidth(0).MaxLines(1).FontSize(typeAction).FixedLineHeight(lineCompact).TextColor(ink)
	})
	button.OnClick(func() {
		ct.Draft = prompt
		a.draftChanged(ct)
		a.focusComposer = true
		a.composerCaretToEnd = true
	})
}
