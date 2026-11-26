package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

type botMemoryViewState struct {
	data             pi.ProductBotMemory
	matches          []pi.ProductWorkLog
	addDraft         string
	editRuleID       string
	query            string
	problem          string
	loading          bool
	loaded           bool
	busy             bool
	loadFailed       bool
	searching        bool
	searchFailed     bool
	loadGeneration   uint64
	searchGeneration uint64
	cancelSearch     context.CancelFunc
}

func (a *appModel) loadBotMemory(ct *contact) {
	host := a.productHost()
	if host == nil || !a.productProfileReady() {
		return
	}
	m := &ct.memory
	m.loadGeneration++
	generation := m.loadGeneration
	m.loading, m.loadFailed = true, false
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		data, err := host.product.GetBotMemory(ctx, ct.ID)
		a.updateUI(func() {
			if a.productHost() != host || a.contactByID(ct.ID) != ct || generation != m.loadGeneration {
				return
			}
			m.loading, m.loadFailed = false, err != nil
			if err != nil {
				return
			}
			m.data, m.loaded = data, true
			ct.PendingLearnings = len(data.PendingLearnings)
			if strings.TrimSpace(m.query) != "" {
				a.searchBotMemory(ct, 0)
			}
		})
	}()
}

func (a *appModel) searchBotMemory(ct *contact, delay time.Duration) {
	m := &ct.memory
	if m.cancelSearch != nil {
		m.cancelSearch()
	}
	m.searchGeneration++
	generation := m.searchGeneration
	query := strings.TrimSpace(m.query)
	m.matches, m.searching, m.searchFailed = nil, false, false
	host := a.productHost()
	if query == "" || host == nil || !a.productProfileReady() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	m.cancelSearch = cancel
	m.searching = true
	go func() {
		defer cancel()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		items, err := host.product.SearchMemoryLogs(ctx, pi.ProductMemorySearchParams{BotID: ct.ID, Query: query, Limit: 500})
		a.updateUI(func() {
			if a.productHost() != host || a.contactByID(ct.ID) != ct || generation != m.searchGeneration {
				return
			}
			m.matches, m.searching, m.searchFailed = items, false, err != nil
		})
	}()
}

type memoryAction func(context.Context, *pi.ProductClient, pi.ProductMemoryOperation) (pi.ProductMemoryChangedEvent, error)

func (a *appModel) changeBotMemory(ct *contact, action memoryAction, done func()) {
	m := &ct.memory
	if m.busy || !a.productProfileReady() || a.dataRecoveryBlocksChanges() {
		return
	}
	host := a.productHost()
	params := pi.ProductMemoryOperation{BotID: ct.ID, OperationID: newContactID()}
	m.busy, m.problem = true, ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := action(ctx, host.product, params)
		a.updateUI(func() {
			if a.productHost() != host || a.contactByID(ct.ID) != ct {
				return
			}
			m.busy = false
			if err != nil {
				m.problem = "request_failed"
				var remote *pi.ProductRemoteError
				if errors.As(err, &remote) {
					m.problem = remote.Code
				}
				return
			}
			if done != nil {
				done()
			}
			a.loadBotMemory(ct)
		})
	}()
}

func memoryProblemText(msgs locales.Messages, reason string) string {
	switch reason {
	case "memory_secret":
		return msgs.MemoryErrorSecret()
	case "memory_temporary":
		return msgs.MemoryErrorTemporary()
	case "memory_duplicate":
		return msgs.MemoryErrorDuplicate()
	case "memory_invalid", "invalid_params":
		return msgs.MemoryErrorInvalid()
	case "memory_rule_limit":
		return msgs.MemoryErrorRuleLimit()
	case "memory_reviewed", "not_found":
		return msgs.MemoryErrorChanged()
	default:
		return msgs.MemoryErrorSave()
	}
}

func (a *appModel) submitMemoryRule(ct *contact) {
	m := &ct.memory
	text, ruleID := strings.TrimSpace(m.addDraft), m.editRuleID
	if text == "" {
		return
	}
	a.changeBotMemory(ct, func(ctx context.Context, client *pi.ProductClient, params pi.ProductMemoryOperation) (pi.ProductMemoryChangedEvent, error) {
		add := pi.ProductMemoryAddRuleParams{ProductMemoryOperation: params, Text: text}
		if ruleID != "" {
			return client.UpdateMemoryRule(ctx, pi.ProductMemoryUpdateRuleParams{ProductMemoryAddRuleParams: add, RuleID: ruleID})
		}
		return client.AddMemoryRule(ctx, add)
	}, func() { m.addDraft, m.editRuleID = "", "" })
}

func (a *appModel) botMemoryView(c *ui.Context, t *ui.Theme, ct *contact) {
	if a.productHost() == nil {
		return
	}
	m, msgs := &ct.memory, locales.In(c.Locale())
	mutable := a.productProfileReady() && !a.dataRecoveryBlocksChanges() && !m.busy
	ui.Column(c).Key("bot-memory").FillWidth().MinWidth(0).Gap(12).Padding(12, 0, 4).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).BorderStyle(ui.BorderSolid).Children(func() {
		ui.Text(c, msgs.MemoryTitle()).Role(ui.RoleHeading).Level(2).FontSize(typeLabel).FontWeight(600).FixedLineHeight(lineBody).TextColor(t.Text)
		if m.loadFailed || !a.productProfileReady() {
			ui.Text(c, msgs.MemoryErrorLoad()).FontSize(typeMeta).TextColor(t.Danger)
			if a.productProfileReady() {
				secondaryButton(c, msgs.MemoryRetry()).OnClick(func() { a.loadBotMemory(ct) })
			}
		}
		if !m.loaded {
			if m.loading {
				ui.Text(c, msgs.MemoryLoading()).FontSize(typeMeta).TextColor(t.TextMuted)
			}
			return
		}
		if len(m.data.PendingLearnings) != 0 {
			a.memoryLearningList(c, t, ct, msgs, mutable)
		}
		a.memoryRuleList(c, t, ct, msgs, mutable)
		a.memoryWorkLogs(c, t, ct, msgs, mutable)
	})
}

func memoryHeading(c *ui.Context, t *ui.Theme, text string) {
	ui.Text(c, text).Role(ui.RoleHeading).Level(3).FontSize(typeLabel).FixedLineHeight(lineLabel).FontWeight(500).TextColor(t.Text)
}

func (a *appModel) memoryLearningList(c *ui.Context, t *ui.Theme, ct *contact, msgs locales.Messages, mutable bool) {
	ui.Column(c).Key("pending").FillWidth().MinWidth(0).Gap(6).Children(func() {
		memoryHeading(c, t, msgs.MemoryReviewCount(len(ct.memory.data.PendingLearnings)))
		for _, learning := range ct.memory.data.PendingLearnings {
			card := ui.Column(c).Key(learning.LearningID).FillWidth().MinWidth(0).Gap(4).Padding(8).Radius(theme.CardRadius).
				Border(1, t.Border).Background(t.SurfaceHover)
			card.Children(func() {
				ui.Text(c, learning.Text).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.Text)
				ui.Text(c, learning.Reason).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
					memoryApproveButton(c, t, msgs.MemoryApprove(), !mutable).OnClick(func() {
						a.reviewMemoryLearning(ct, learning.LearningID, "approve")
					})
					secondaryButton(c, msgs.MemoryReject()).Disabled(!mutable).OnClick(func() {
						a.reviewMemoryLearning(ct, learning.LearningID, "reject")
					})
				})
			})
		}
	})
}

func memoryApproveButton(c *ui.Context, t *ui.Theme, label string, disabled bool) ui.Element {
	button := ui.ButtonBase(c).Label(label).MinHeight(theme.ControlHit).Padding(6, 12).Radius(theme.ControlRadius).
		Disabled(disabled).FocusRingColor(t.Accent).AlignItems(ui.Center).Justify(ui.Center)
	fill, border, ink := t.Surface.Mix(t.Accent, .12), t.Border.Mix(t.Accent, .45), t.Text
	switch {
	case disabled:
		fill, border, ink = t.Surface, t.Border, t.TextMuted
	case button.Pressed():
		fill = t.Surface.Mix(t.Accent, .24)
	case button.Hovered():
		fill = t.Surface.Mix(t.Accent, .18)
	}
	button.Background(fill).Border(1, border).TextColor(ink).Children(func() {
		ui.Text(c, label).FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction)
	})
	return theme.TransitionControl(button, t)
}

func (a *appModel) reviewMemoryLearning(ct *contact, learningID, decision string) {
	a.changeBotMemory(ct, func(ctx context.Context, client *pi.ProductClient, params pi.ProductMemoryOperation) (pi.ProductMemoryChangedEvent, error) {
		return client.ReviewLearning(ctx, pi.ProductMemoryReviewLearningParams{ProductMemoryOperation: params, LearningID: learningID, Decision: decision})
	}, nil)
}

func (a *appModel) memoryRuleList(c *ui.Context, t *ui.Theme, ct *contact, msgs locales.Messages, mutable bool) {
	m := &ct.memory
	ui.Column(c).Key("rules").FillWidth().MinWidth(0).Gap(6).Children(func() {
		memoryHeading(c, t, msgs.MemoryRules())
		ui.Row(c).Key("rule-editor").FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(6).Children(func() {
			label, action := msgs.MemoryAddPlaceholder(), msgs.MemoryAdd()
			if m.editRuleID != "" {
				label, action = msgs.MemoryEditRule(), msgs.MemorySave()
			}
			c.Key("rule-input")
			preferenceInput(c, &m.addDraft, m.problem != "", a.workTheme.InputBorder).Grow(1).MinWidth(0).Label(label).Placeholder(label).Disabled(!mutable).
				OnTextKey(func(k ui.TextKey) bool {
					if k.Key != ui.KeyEnter {
						return false
					}
					a.submitMemoryRule(ct)
					return true
				})
			secondaryButton(c, action).Shrink(0).Disabled(!mutable || strings.TrimSpace(m.addDraft) == "").OnClick(func() { a.submitMemoryRule(ct) })
			if m.editRuleID != "" {
				memoryRemoveButton(c, t, msgs.MemoryCancelEdit(), false).OnClick(func() { m.editRuleID, m.addDraft, m.problem = "", "", "" })
			}
		})
		if m.problem != "" {
			ui.Text(c, memoryProblemText(msgs, m.problem)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
		}
		if len(m.data.Rules) == 0 {
			ui.Text(c, msgs.MemoryNoRules()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
		for _, rule := range m.data.Rules {
			ui.Row(c).Key(rule.RuleID).FillWidth().MinWidth(0).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, rule.Text).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.Text)
					label := msgs.MemorySourceManual()
					switch rule.Source {
					case "explicit":
						label = msgs.MemorySourceExplicit()
					case "approved":
						label = msgs.MemorySourceApproved()
					}
					ui.Text(c, label).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				})
				linkButton(c, msgs.MemoryEdit()).Disabled(!mutable).OnClick(func() {
					m.editRuleID, m.addDraft, m.problem = rule.RuleID, rule.Text, ""
				})
				memoryRemoveButton(c, t, msgs.MemoryRemoveRule(), !mutable).OnClick(func() {
					a.changeBotMemory(ct, func(ctx context.Context, client *pi.ProductClient, params pi.ProductMemoryOperation) (pi.ProductMemoryChangedEvent, error) {
						return client.DeleteMemoryRule(ctx, pi.ProductMemoryDeleteRuleParams{ProductMemoryOperation: params, RuleID: rule.RuleID})
					}, func() {
						if m.editRuleID == rule.RuleID {
							m.editRuleID, m.addDraft = "", ""
						}
					})
				})
			})
		}
	})
}

func (a *appModel) memoryWorkLogs(c *ui.Context, t *ui.Theme, ct *contact, msgs locales.Messages, mutable bool) {
	m := &ct.memory
	ui.Column(c).Key("work-log").FillWidth().MinWidth(0).Gap(6).Children(func() {
		memoryHeading(c, t, msgs.MemoryWorkLog())
		c.Key("log-search")
		preferenceInput(c, &m.query, m.searchFailed, a.workTheme.InputBorder).FillWidth().MinWidth(0).
			Label(msgs.MemorySearchPlaceholder()).Placeholder(msgs.MemorySearchPlaceholder()).Disabled(!a.productProfileReady()).
			OnChange(func() { a.searchBotMemory(ct, 160*time.Millisecond) })
		items := m.data.WorkLogs
		if query := strings.TrimSpace(m.query); query != "" {
			items = m.matches
			switch {
			case m.searching:
				ui.Text(c, msgs.MemorySearching()).FontSize(typeMeta).TextColor(t.TextMuted)
			case m.searchFailed:
				ui.Text(c, msgs.MemoryErrorSearch()).FontSize(typeMeta).TextColor(t.Danger)
			case len(items) == 0:
				ui.Text(c, msgs.MemoryNoMatches()).FontSize(typeMeta).TextColor(t.TextMuted)
			}
		} else if len(items) == 0 {
			ui.Text(c, msgs.MemoryNoWorkLogs()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
		if len(items) == 0 {
			return
		}
		ui.Scroll(c).FillWidth().MinWidth(0).MaxHeight(200).Children(func() {
			ui.Column(c).FillWidth().MinWidth(0).Gap(6).Children(func() {
				for _, item := range items {
					ui.Row(c).Key(item.LogID).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
							ui.Text(c, item.Date).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
							ui.Text(c, item.Summary).FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.Text)
						})
						memoryRemoveButton(c, t, msgs.MemoryRemoveLog(), !mutable).OnClick(func() {
							a.changeBotMemory(ct, func(ctx context.Context, client *pi.ProductClient, params pi.ProductMemoryOperation) (pi.ProductMemoryChangedEvent, error) {
								return client.DeleteMemoryLog(ctx, pi.ProductMemoryDeleteLogParams{ProductMemoryOperation: params, LogID: item.LogID})
							}, nil)
						})
					})
				}
			})
		})
	})
}

func memoryRemoveButton(c *ui.Context, t *ui.Theme, label string, disabled bool) ui.Element {
	button := ui.ButtonBase(c).Label(label).Size(theme.ControlHit, theme.ControlHit).Shrink(0).Radius(theme.ControlRadius).
		Disabled(disabled).Center().TextColor(t.TextMuted).FocusRingColor(t.Accent)
	if button.Pressed() {
		button.Background(t.SurfacePressed)
	} else if button.Hovered() {
		button.Background(t.SurfaceHover)
	}
	button.Children(func() { controlIcon(c, glyphCloseLine, iconChrome) })
	return appTooltip(c, theme.TransitionControl(button, t), label)
}
