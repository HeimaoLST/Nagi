package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/connection"
	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

const sendErrorCredentialUnavailable = "credential-unavailable"

const (
	providerTabAccounts = iota
	providerTabRouting
	providerTabWorkspace
)

type providerSettings struct {
	tab          int
	selectedID   string
	focusID      string
	editor       *providerEditor
	drafts       map[string]*providerEditor
	adding       bool
	listScroll   ui.ScrollState
	detailScroll ui.ScrollState
	dialogScroll ui.ScrollState
	busy         bool
	cancelTest   context.CancelFunc
	failure      providerFeedback
}

type providerEditor struct {
	pi.ProductProviderAccount
	APIKey     string
	preset     int
	context    string
	output     string
	additional string
	advanced   bool
	custom     bool
	showModels bool
	fields     map[string]string
	probe      providerFeedback
	tested     bool
	latency    uint64
}

type providerFeedback struct {
	kind    string
	detail  string
	open    bool
	models  int
	latency uint64
}

var providerPresets = [...]struct {
	name, endpoint, model string
	wire                  pi.WireAPI
	output                int
}{
	{"OpenAI", "https://api.openai.com/v1", "gpt-5", pi.OpenAIResponses, 16384},
	{"DeepSeek", "https://api.deepseek.com/v1", "deepseek-chat", pi.OpenAICompletions, 8192},
	{"OpenRouter", "https://openrouter.ai/api/v1", "openai/gpt-4o", pi.OpenAICompletions, 8192},
	{"Kimi", "https://api.moonshot.cn/v1", "kimi-k2-0905-preview", pi.OpenAICompletions, 8192},
	{"Zhipu GLM", "https://open.bigmodel.cn/api/paas/v4", "glm-4.7", pi.OpenAICompletions, 8192},
	{"Qwen", "https://dashscope.aliyuncs.com/compatible-mode/v1", "qwen3-coder-plus", pi.OpenAICompletions, 8192},
	{"Ollama", "http://127.0.0.1:11434/v1", "qwen3:8b", pi.OpenAICompletions, 8192},
	{"", "", "", pi.OpenAICompletions, 8192},
}

var providerTokenPresets = [...]struct {
	context, output int
}{{131072, 8192}, {262144, 16384}, {524288, 32768}}

func (a *appModel) hasEffectiveProviderPool() bool {
	host := a.productHost()
	return host != nil && host.productProviders.EffectiveRevision != nil
}

func (a *appModel) hasUsableProviderRoute() bool {
	return a.hasEffectiveProviderPool() && a.productHost().productProviders.ResolvedRoles.Dialogue != nil
}

func (a *appModel) productProvidersPending() bool {
	host := a.productHost()
	if host == nil || host.productProviders.SavedRevision == nil {
		return false
	}
	status := host.productProviders
	return status.EffectiveRevision == nil || *status.SavedRevision != *status.EffectiveRevision
}

func (a *appModel) hasAnySavedConnection() bool {
	host := a.productHost()
	return a.connectionSaved != nil || host != nil && len(host.productProviders.Accounts) > 0
}

func (a *appModel) providerBusy() bool { return a.providers.busy }

func (a *appModel) applyProductProviders(value pi.ProductProviderList) {
	a.productHost().productProviders = value
	a.updateProductReadiness()
}

func (a *appModel) refreshProductProviders(host *piAppHost) {
	if a.productHost() != host || !a.productProfileReady() {
		return
	}
	if a.providerBusy() {
		host.providerRefresh = true
		return
	}
	host.providerRead++
	read := host.providerRead
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		value, err := host.product.ListProviders(ctx)
		a.updateUI(func() {
			if a.productHost() == host && host.providerRead == read && err == nil {
				a.applyProductProviders(value)
			}
		})
	}()
}

func (a *appModel) providerChangesBlocked() bool {
	return a.providerBusy() || !a.productProfileReady() || a.dataRecoveryBlocksChanges() || a.connectionApplying ||
		a.connectionSaveInFlight || a.connectionTesting
}

func (a *appModel) runProviderRPC(test bool, run func(context.Context, *pi.ProductClient) error, done func(error)) {
	if a.providerChangesBlocked() {
		return
	}
	host := a.productHost()
	a.providers.busy = true
	a.providers.failure = providerFeedback{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	if test {
		a.providers.cancelTest = cancel
	} else {
		host.providerRead++
	}
	go func() {
		defer cancel()
		err := run(ctx, host.product)
		var snapshot pi.ProductProviderList
		var refreshErr error
		if !test {
			refresh, stop := context.WithTimeout(context.Background(), 5*time.Second)
			snapshot, refreshErr = host.product.ListProviders(refresh)
			stop()
		}
		a.updateUI(func() {
			if a.productHost() != host {
				return
			}
			a.providers.busy, a.providers.cancelTest = false, nil
			if !test && refreshErr == nil {
				a.applyProductProviders(snapshot)
			}
			done(err)
			if !test && refreshErr != nil && err == nil {
				a.providers.failure = a.providerFailure("refresh", refreshErr)
			}
			if host.providerRefresh || refreshErr != nil {
				host.providerRefresh = false
				a.refreshProductProviders(host)
			}
			if a.shutdownStarted {
				a.finishShutdown()
			}
		})
	}()
}

func (a *appModel) providerFailure(kind string, err error) providerFeedback {
	if err == nil {
		return providerFeedback{}
	}
	var remote *pi.RemoteError
	if errors.As(err, &remote) && remote.Code == "stale_revision" {
		kind = "stale"
	}
	return providerFeedback{kind: kind, detail: a.connectionSafeMessage(err)}
}

func newProviderEditor(account pi.ProductProviderAccount) *providerEditor {
	return &providerEditor{
		ProductProviderAccount: account,
		context:                strconv.Itoa(account.ContextTokens),
		output:                 strconv.Itoa(account.OutputTokens),
		preset:                 -1,
	}
}

func (a *appModel) addProvider() {
	if a.providerChangesBlocked() || a.providers.adding {
		return
	}
	a.providers.editor = newProviderEditor(pi.ProductProviderAccount{
		ProviderID: "provider-" + newContactID(), WireAPI: pi.OpenAICompletions, Enabled: true,
		ContextTokens: 131072, OutputTokens: 8192,
	})
	a.providers.adding = true
	a.providers.dialogScroll.Y = 0
	a.providers.failure = providerFeedback{}
}

func (a *appModel) dismissProviderDialog() {
	a.providers.adding = false
	a.providers.editor = a.providers.drafts[a.providers.selectedID]
}

func (a *appModel) selectProvider(account pi.ProductProviderAccountView) {
	s := &a.providers
	if s.selectedID != account.ProviderID {
		s.detailScroll.Y = 0
	}
	s.selectedID = account.ProviderID
	if s.drafts == nil {
		s.drafts = make(map[string]*providerEditor)
	}
	if s.drafts[s.selectedID] == nil {
		s.drafts[s.selectedID] = newProviderEditor(account.ProductProviderAccount)
	}
	if !s.adding {
		s.editor = s.drafts[s.selectedID]
	}
}

func (e *providerEditor) resetProbe() {
	e.probe, e.tested, e.latency = providerFeedback{}, false, 0
}

func (e *providerEditor) choosePreset(index int) {
	preset := providerPresets[index]
	e.preset = index
	e.Name, e.BaseURL, e.DefaultModel, e.WireAPI = preset.name, preset.endpoint, preset.model, preset.wire
	e.context, e.output, e.custom = "131072", strconv.Itoa(preset.output), false
	e.Models, e.additional, e.fields = nil, "", nil
	e.resetProbe()
}

func (e *providerEditor) input(keepKey bool) (pi.ProductProviderTestParams, bool) {
	account := e.ProductProviderAccount
	account.Name, account.DefaultModel = strings.TrimSpace(account.Name), strings.TrimSpace(account.DefaultModel)
	key := strings.TrimSpace(e.APIKey)
	fields := make(map[string]string)
	for field, value := range map[string]string{"Name": account.Name, "Model": account.DefaultModel} {
		if value == "" {
			fields[field] = connection.ValidationRequired
		} else if utf16Length(value) > 256 {
			fields[field] = "name-length"
		}
	}
	if reason := connection.ValidateEndpoint(account.BaseURL); reason != "" {
		fields["Endpoint"] = reason
	} else if utf16Length(account.BaseURL) > 2048 {
		fields["Endpoint"] = "endpoint-length"
	}
	if key == "" && !keepKey {
		fields["APIKey"] = connection.ValidationRequired
	}
	account.ContextTokens, _ = strconv.Atoi(strings.TrimSpace(e.context))
	account.OutputTokens, _ = strconv.Atoi(strings.TrimSpace(e.output))
	const maxTokens = 9007199254740991
	if account.ContextTokens <= 0 || account.ContextTokens > maxTokens {
		fields["ContextTokens"] = connection.ValidationPositive
	}
	if account.OutputTokens <= 0 || account.OutputTokens > maxTokens {
		fields["OutputTokens"] = connection.ValidationPositive
	} else if account.ContextTokens > 0 && account.ContextTokens-account.OutputTokens < 4 {
		fields["OutputTokens"] = connection.ValidationContextGap
	}
	if fields["ContextTokens"] != "" || fields["OutputTokens"] != "" {
		e.custom, e.advanced = true, true
	}
	models := slices.Clone(e.Models)
	for _, model := range strings.FieldsFunc(e.additional, func(r rune) bool { return r == ',' || r == '\n' }) {
		model = strings.TrimSpace(model)
		if model != "" && !slices.Contains(models, model) {
			models = append(models, model)
		}
	}
	for _, model := range models {
		if len(models) > 128 || utf16Length(model) > 256 {
			fields["Models"], e.showModels, e.advanced = "model-limit", true, true
			break
		}
	}
	account.Models = models
	e.fields = fields
	return pi.ProductProviderTestParams{Account: account, APIKey: key}, len(fields) == 0
}

func utf16Length(text string) int {
	length := 0
	for _, r := range text {
		length += utf16.RuneLen(r)
	}
	return length
}

func (a *appModel) testProvider(params pi.ProductProviderTestParams, editor *providerEditor) {
	var result pi.ProductProviderTestResult
	a.runProviderRPC(true, func(ctx context.Context, client *pi.ProductClient) error {
		var err error
		result, err = client.TestProvider(ctx, params)
		if err == nil && !result.OK {
			err = errors.New(result.Error)
		}
		return err
	}, func(err error) {
		editor.probe = a.providerFailure("test", err)
		if err == nil {
			editor.probe = providerFeedback{kind: "tested", models: len(result.Models), latency: result.LatencyMS}
			editor.tested, editor.latency = true, result.LatencyMS
			if len(result.Models) > 0 {
				editor.Models = result.Models
			}
		} else {
			editor.tested, editor.latency = false, 0
		}
	})
}

func (a *appModel) saveProvider(editor *providerEditor, params pi.ProductProviderTestParams, apply bool) {
	var saved bool
	a.runProviderRPC(false, func(ctx context.Context, client *pi.ProductClient) error {
		revisions, err := client.SaveProvider(ctx, pi.ProductProviderSaveParams{ProductProviderTestParams: params, OperationID: newContactID()})
		saved = err == nil
		if saved && apply {
			_, err = client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: *revisions.SavedRevision, OperationID: newContactID()})
		}
		return err
	}, func(err error) {
		if !saved {
			editor.probe = a.providerFailure("save", err)
			return
		}
		a.providers.failure = a.providerFailure("apply", err)
		s := &a.providers
		if s.drafts == nil {
			s.drafts = make(map[string]*providerEditor)
		}
		savedEditor := newProviderEditor(params.Account)
		savedEditor.tested, savedEditor.latency = editor.tested, editor.latency
		s.drafts[editor.ProviderID] = savedEditor
		if s.adding {
			s.focusID = editor.ProviderID
			s.detailScroll.Y = 0
		}
		s.adding, s.selectedID, s.editor = false, editor.ProviderID, savedEditor
		editor.APIKey = ""
	})
}

func (a *appModel) applyProviderChanges(revision uint64) {
	a.runProviderRPC(false, func(ctx context.Context, client *pi.ProductClient) error {
		_, err := client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{SavedRevision: revision, OperationID: newContactID()})
		return err
	}, func(err error) { a.providers.failure = a.providerFailure("apply", err) })
}

func (a *appModel) setProviderPolicy(policy pi.ProductRoutingPolicy) {
	a.runProviderRPC(false, func(ctx context.Context, client *pi.ProductClient) error {
		_, err := client.SetRoutingPolicy(ctx, pi.ProductRoutingPolicyParams{Policy: policy, OperationID: newContactID()})
		return err
	}, func(err error) { a.providers.failure = a.providerFailure("policy", err) })
}

func (a *appModel) changeProviderAccount(id string, revoke bool) {
	kind := "delete"
	if revoke {
		kind = "revoke"
	}
	a.runProviderRPC(false, func(ctx context.Context, client *pi.ProductClient) error {
		params := pi.ProductProviderTargetParams{ProviderID: id, OperationID: newContactID()}
		if revoke {
			_, err := client.RevokeProviderCredential(ctx, params)
			return err
		}
		_, err := client.DeleteProvider(ctx, params)
		return err
	}, func(err error) {
		a.providers.failure = a.providerFailure(kind, err)
		if err == nil {
			if revoke {
				if draft := a.providers.drafts[id]; draft != nil {
					draft.resetProbe()
				}
			} else {
				delete(a.providers.drafts, id)
				if a.providers.selectedID == id {
					a.providers.selectedID, a.providers.editor = "", nil
				}
			}
		}
	})
}

func (f *providerFeedback) render(c *ui.Context, t *ui.Theme) {
	if f.kind == "" {
		return
	}
	msgs := locales.In(c.Locale())
	if f.kind == "tested" {
		ui.Text(c, msgs.ProviderProbeResult(f.models, strconv.FormatUint(f.latency, 10))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Success)
		return
	}
	var summary string
	switch f.kind {
	case "test":
		summary = msgs.ProviderTestFailed()
	case "save":
		summary = msgs.ProviderSaveFailed()
	case "apply":
		summary = msgs.ProviderApplyFailed()
	case "policy":
		summary = msgs.ProviderPolicyFailed()
	case "revoke":
		summary = msgs.ProviderRevokeFailed()
	case "delete":
		summary = msgs.ProviderDeleteFailed()
	case "stale":
		summary = msgs.ProviderStaleRevision()
	default:
		summary = msgs.ProviderRefreshFailed()
	}
	recoveryNoticeView(c, t, summary, f.detail, f.open, func() { f.open = !f.open }, nil, "")
}

type providerRoleRow struct {
	name, hint string
	route      *pi.ProductModelRoute
}

func providerRoleRows(msgs locales.Messages, roles pi.ProductResolvedRoles) [4]providerRoleRow {
	return [4]providerRoleRow{
		{msgs.ProviderRoleDialogue(), msgs.ProviderRoleDialogueHint(), roles.Dialogue},
		{msgs.ProviderRoleCoding(), msgs.ProviderRoleCodingHint(), roles.Coding},
		{msgs.ProviderRoleReasoning(), msgs.ProviderRoleReasoningHint(), roles.Reasoning},
		{msgs.ProviderRoleUtility(), msgs.ProviderRoleUtilityHint(), roles.Utility},
	}
}

func (a *appModel) providerTabs(c *ui.Context, msgs locales.Messages, count int) {
	c.Key("connection-subtabs")
	selected := a.providers.tab
	theme.PreferenceSegmentedSelection(c, &selected, msgs.SettingsConnection(), false, false,
		msgs.ProviderTabAccounts(count), msgs.ProviderTabRouting(), msgs.ProviderTabWorkspace()).OnChange(func() {
		a.providers.tab = selected
		a.settingsScroll.Y = 0
		a.folderChoiceGeneration++
		if selected == providerTabWorkspace && count == 0 {
			a.connectionForm.FocusFolder = true
		}
	})
}

func (a *appModel) providerStatus(c *ui.Context, t *ui.Theme, pool pi.ProductProviderList, roles [4]providerRoleRow) {
	msgs := locales.In(c.Locale())
	enabled, active := 0, 0
	for _, account := range pool.Accounts {
		if account.Enabled {
			enabled++
		}
	}
	for _, role := range roles {
		if role.route != nil {
			active++
		}
	}
	pending := a.productProvidersPending()
	summary := msgs.ProviderEnabledCount(enabled) + " · " + msgs.ProviderActiveCount(active)
	ink := t.TextMuted
	switch {
	case pending:
		summary += " · " + msgs.ProviderPending()
		ink = t.Warning
	case active > 0:
		ink = t.Success
	case pool.EffectiveRevision != nil && enabled > 0:
		summary += " · " + msgs.ProviderCredentialsUnavailable()
		ink = t.Warning
	}
	ui.Row(c).FillWidth().MinWidth(0).Shrink(0).Wrap().AlignItems(ui.Center).Gap(8).Padding(10, 14).
		Radius(10).Border(1, t.Border).Background(t.Surface).Children(
		func() {
			ui.Row(c).Basis(280).Grow(1).MinWidth(0).AlignItems(ui.Center).Gap(8).Children(func() {
				providerDot(c, ink)
				ui.Text(c, summary).MinWidth(0).FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction)
			})
			if pending {
				button := primaryButton
				if a.providers.adding {
					button = secondaryButton
				}
				button(c, msgs.ProviderApplyChanges()).MinHeight(32).Disabled(a.providerChangesBlocked()).OnClick(func() { a.applyProviderChanges(*pool.SavedRevision) })
			} else if active > 0 {
				ui.Text(c, msgs.ProviderAllApplied()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Success)
			}
		})
	a.providers.failure.render(c, t)
}

func (a *appModel) providerRouting(c *ui.Context, t *ui.Theme, pool pi.ProductProviderList) {
	msgs := locales.In(c.Locale())
	roles := providerRoleRows(msgs, pool.ResolvedRoles)
	a.providerStatus(c, t, pool, roles)
	policies := [...]pi.ProductRoutingPolicy{pi.ProductRoutingEconomy, pi.ProductRoutingBalanced, pi.ProductRoutingQuality}
	labels := [...]string{msgs.ProviderEconomy(), msgs.ProviderBalanced(), msgs.ProviderQuality()}
	hints := [...]string{msgs.ProviderEconomyHint(), msgs.ProviderBalancedHint(), msgs.ProviderQualityHint()}
	selected := slices.Index(policies[:], pool.Policy)
	if selected < 0 {
		selected = 1
	}
	c.Key("provider-routing")
	preferenceSection(c, t, msgs.ProviderRouting(), func() {
		if a.productProvidersPending() {
			ui.Text(c, msgs.ProviderActiveRoutingNote()).Padding(8, 0, 0).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
		settingsPreferenceRow(c, t, msgs.ProviderStrategy(), hints[selected], func() {
			c.Key("provider-strategy")
			theme.PreferenceSegmentedSelection(c, &selected, msgs.ProviderStrategy(), a.providerChangesBlocked(), false, labels[:]...).OnChange(func() {
				a.setProviderPolicy(policies[selected])
			})
		})
		for i, row := range roles {
			c.Key("role-" + strconv.Itoa(i))
			settingsPreferenceRow(c, t, row.name, row.hint, func() {
				providerRoute(c, t, msgs, row.route, pool.Accounts)
			}, i == len(roles)-1)
		}
	})
}

func (a *appModel) providerAddButton(c *ui.Context, label string, blocked bool) {
	button := secondaryButton(c, "+ "+label).Label(label).MinHeight(32).Disabled(blocked)
	button.OnClick(func() {
		button.Focus()
		a.addProvider()
	})
}

func (a *appModel) providerEmpty(c *ui.Context, t *ui.Theme, pool pi.ProductProviderList) {
	msgs := locales.In(c.Locale())
	if a.productProvidersPending() {
		a.providerStatus(c, t, pool, providerRoleRows(msgs, pool.ResolvedRoles))
	} else {
		a.providers.failure.render(c, t)
	}
	ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Padding(10, 14).
		Radius(10).Border(1, t.Border).Background(t.Surface).Children(
		func() {
			ui.Text(c, msgs.ProviderNoAccounts()).FontSize(typeLabel).FontWeight(500).FixedLineHeight(lineLabel)
			a.providerAddButton(c, msgs.ProviderAdd(), a.providerChangesBlocked())
		})
}

func (a *appModel) providerWorkspace(c *ui.Context, t *ui.Theme, pool pi.ProductProviderList, wide bool) {
	msgs := locales.In(c.Locale())
	roles := providerRoleRows(msgs, pool.ResolvedRoles)
	a.providerStatus(c, t, pool, roles)
	i := slices.IndexFunc(pool.Accounts, func(account pi.ProductProviderAccountView) bool { return account.ProviderID == a.providers.selectedID })
	account := pool.Accounts[max(0, i)]
	a.selectProvider(account)
	editor := a.providers.drafts[account.ProviderID]
	blocked := a.providerChangesBlocked()
	background := t.Surface.Mix(t.Background, .45)
	split := ui.Row(c)
	if wide {
		split.Grow(1).MinHeight(0).AlignItems(ui.Stretch)
	} else {
		split.Column()
	}
	split.FillWidth().MinWidth(0).Background(t.Surface).Border(1, t.Border).Radius(12).Clip().Children(func() {
		list := ui.Column(c).MinWidth(0).MinHeight(0).Shrink(0).Background(background).BorderColor(t.Border)
		if wide {
			list.Width(236).BorderWidth(0, 1, 0, 0)
		} else {
			list.FillWidth().BorderWidth(0, 0, 1, 0)
		}
		list.Children(func() {
			ui.Row(c).FillWidth().Shrink(0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Padding(10, 12).
				BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(
				func() {
					ui.Text(c, msgs.ProviderAccounts()).FontSize(typeMeta).FontWeight(600).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
					a.providerAddButton(c, msgs.ProviderAddShort(), blocked || len(pool.Accounts) >= 64)
				})
			scroll := ui.Scroll(c).FillWidth().MinWidth(0).MinHeight(0).TrackScroll(&a.providers.listScroll)
			if wide {
				scroll.Grow(1)
			} else {
				scroll.MaxHeight(160)
			}
			scroll.Children(func() {
				ui.Column(c).FillWidth().MinWidth(0).Padding(6).Gap(4).Children(func() {
					for _, row := range pool.Accounts {
						c.Key("provider-item-" + row.ProviderID)
						a.providerListItem(c, t, msgs, row, providerRoleNames(msgs, row.ProviderID, roles), blocked)
					}
				})
			})
		})
		c.Key("provider-detail-" + account.ProviderID)
		a.providerDetail(c, t, msgs, account, editor, providerRoleNames(msgs, account.ProviderID, roles), background, wide, blocked)
	})
}

func providerDot(c *ui.Context, color ui.Color) {
	ui.Box(c).Size(6, 6).Radius(3).Shrink(0).Background(color)
}

func providerRoute(c *ui.Context, t *ui.Theme, msgs locales.Messages, route *pi.ProductModelRoute, accounts []pi.ProductProviderAccountView) {
	if route == nil {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			providerDot(c, t.TextMuted)
			ui.Text(c, msgs.ProviderUnassigned()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		})
		return
	}
	name := msgs.ProviderPendingRemoval()
	for _, account := range accounts {
		if account.ProviderID == route.ProviderID {
			name = account.Name
			break
		}
	}
	thinking := msgs.ProviderThinkingOff()
	switch route.ThinkingLevel {
	case "low":
		thinking = msgs.ProviderThinkingLow()
	case "medium":
		thinking = msgs.ProviderThinkingMedium()
	case "high":
		thinking = msgs.ProviderThinkingHigh()
	}
	label := name + " · " + route.ResolvedModel
	ui.Column(c).FillWidth().MinWidth(0).AlignItems(ui.End).Gap(2).Children(func() {
		ui.Row(c).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
			providerDot(c, t.Success)
			text := ui.Text(c, label).MinWidth(0).Shrink(1).MaxLines(1).Ellipsis("…").FontSize(typeAction).FixedLineHeight(lineAction)
			appTooltip(c, text, label+"\n"+thinking)
		})
		ui.Text(c, thinking).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
	})
}

func providerRoleNames(msgs locales.Messages, id string, roles [4]providerRoleRow) string {
	var names [4]string
	count := 0
	for _, role := range roles {
		if role.route != nil && role.route.ProviderID == id {
			names[count] = role.name
			count++
		}
	}
	return strings.Join(names[:count], msgs.ProviderRoleSeparator())
}

func providerAccountStatus(t *ui.Theme, msgs locales.Messages, account pi.ProductProviderAccountView) (string, ui.Color) {
	if !account.Enabled {
		return msgs.ProviderDisabled(), t.TextMuted
	}
	if account.CredentialStatus != "ok" {
		return msgs.ProviderMissingKey(), t.Warning
	}
	return msgs.ProviderEnabled(), t.Success
}

func (a *appModel) providerListItem(c *ui.Context, t *ui.Theme, msgs locales.Messages, account pi.ProductProviderAccountView, roles string, blocked bool) {
	selected := a.providers.selectedID == account.ProviderID
	palette := a.workTheme
	palette.NavHover, palette.NavSelected = t.SurfaceHover, t.Selection
	status, ink := providerAccountStatus(t, msgs, account)
	meta := account.DefaultModel + " · " + msgs.ProviderModelCount(len(account.Models))
	if roles != "" {
		meta += " · " + roles
	}
	item := navigationControl(c, ui.ButtonBase(c).Label(account.Name).Selected(selected), palette, selected).
		FillWidth().MinWidth(0).Height(52).MinHeight(52).Shrink(0).Margin(0).Column().AlignItems(ui.Stretch).Gap(2).Padding(8, 10).Radius(8).Disabled(blocked)
	item.Children(func() {
		ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(8).Children(func() {
			providerDot(c, ink)
			ui.Text(c, account.Name).Grow(1).MinWidth(0).FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction).MaxLines(1).Ellipsis("…").TextColor(t.Text)
			ui.Text(c, status).Shrink(0).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(ink)
		})
		ui.Text(c, meta).MinWidth(0).Padding(0, 0, 0, 14).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
	})
	appTooltip(c, item, account.Name+"\n"+meta)
	item.OnClick(func() { a.selectProvider(account) })
	if a.providers.focusID == account.ProviderID {
		item.Focus().ScrollIntoView()
		a.providers.focusID = ""
	}
}

func (a *appModel) providerDetail(c *ui.Context, t *ui.Theme, msgs locales.Messages, account pi.ProductProviderAccountView, editor *providerEditor, roles string, background ui.Color, wide, blocked bool) {
	keepKey := account.CredentialStatus == "ok" && account.BaseURL == editor.BaseURL && account.WireAPI == editor.WireAPI
	detail := ui.Column(c).MinWidth(0).MinHeight(0)
	if wide {
		detail.Grow(1)
	} else {
		detail.FillWidth()
	}
	detail.Children(func() {
		ui.Row(c).FillWidth().MinWidth(0).Shrink(0).Wrap().AlignItems(ui.Center).Gap(8).Padding(14, 18).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(
			func() {
				ui.Column(c).Basis(180).Grow(1).MinWidth(0).Gap(2).Children(func() {
					status, ink := providerAccountStatus(t, msgs, account)
					if account.Enabled && account.CredentialStatus == "ok" {
						status = msgs.ProviderReady()
						if editor.tested {
							status = msgs.ProviderLatencyReady(strconv.FormatUint(editor.latency, 10))
						}
					}
					ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
						name := ui.Text(c, account.Name).MinWidth(0).FontSize(15).FontWeight(600).FixedLineHeight(20).MaxLines(1).Ellipsis("…")
						appTooltip(c, name, account.Name)
						ui.Text(c, status).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(ink)
					})
					subtitle := msgs.ProviderNoRole()
					if roles != "" {
						subtitle = msgs.ProviderActiveFor(roles)
					}
					text := ui.Text(c, subtitle).MinWidth(0).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
					appTooltip(c, text, subtitle)
				})
				ui.Row(c).MinWidth(0).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
					c.Key("provider-enabled")
					selected := 0
					if !editor.Enabled {
						selected = 1
					}
					theme.PreferenceSegmentedSelection(c, &selected, msgs.ProviderAccountStatus(), blocked, false,
						msgs.ProviderEnabled(), msgs.ProviderToggleDisabled()).OnChange(func() { editor.Enabled = selected == 0 })
					a.providerTestButton(c, msgs.ProviderTestSync(), msgs.ProviderTestAccount(account.Name), editor, keepKey, blocked)
				})
			})
		body := func() {
			ui.Column(c).FillWidth().MinWidth(0).Gap(10).Padding(14, 18).Children(func() {
				a.providerField(c, t, msgs, editor, "Name", msgs.FieldName(), "", &editor.Name, false, blocked)
				a.providerProtocol(c, t, msgs, editor, blocked)
				a.providerEndpoint(c, t, msgs, editor, blocked)
				providerFormRow(c, t, msgs.ConnectionFieldAPIKey(), func() {
					placeholder := ""
					if keepKey {
						placeholder = msgs.ProviderKeepKey()
					}
					ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
						a.providerInput(c, editor, "APIKey", msgs.ConnectionFieldAPIKey(), placeholder, &editor.APIKey, true, blocked).Basis(110).Grow(1)
						if account.CredentialStatus == "ok" {
							secondaryButton(c, msgs.ProviderRevoke()).Label(msgs.ProviderRevokeAccount(account.Name)).MinHeight(32).Disabled(blocked).
								OnClick(func() { a.changeProviderAccount(account.ProviderID, true) })
						}
					})
					providerFieldError(c, t, msgs, editor.fields["APIKey"])
				})
				a.providerModels(c, t, msgs, editor, false, blocked)
				a.providerAdditionalModels(c, t, msgs, editor, blocked)
				a.providerTokenFields(c, t, msgs, editor, blocked)
			})
		}
		if wide {
			ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).TrackScroll(&a.providers.detailScroll).Children(body)
		} else {
			body()
		}
		ui.Column(c).FillWidth().Shrink(0).Gap(6).Padding(12, 18).Background(background).
			BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(
			func() {
				providerFormStatus(c, t, msgs, editor, a.providerBusy())
				ui.Row(c).FillWidth().MinWidth(0).Wrap().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
					dangerButton(c, msgs.ProviderDelete()).Label(msgs.ProviderDeleteAccount(account.Name)).MinHeight(32).Disabled(blocked).
						OnClick(func() { a.changeProviderAccount(account.ProviderID, false) })
					a.providerSaveActions(c, msgs, editor, keepKey, blocked, !a.productProvidersPending() && !a.providers.adding)
				})
			})
	})
}

func (a *appModel) providerTestButton(c *ui.Context, label, accessible string, editor *providerEditor, keepKey, blocked bool) {
	secondaryButton(c, label).Label(accessible).MinHeight(32).Disabled(blocked).OnClick(func() {
		if params, ok := editor.input(keepKey); ok {
			a.testProvider(params, editor)
		}
	})
}

func (a *appModel) providerSaveActions(c *ui.Context, msgs locales.Messages, editor *providerEditor, keepKey, blocked, primary bool) {
	ui.Row(c).MinWidth(0).Wrap().Justify(ui.End).AlignItems(ui.Center).Gap(8).Children(func() {
		secondaryButton(c, msgs.ProviderSave()).MinHeight(32).Disabled(blocked).OnClick(func() {
			if params, ok := editor.input(keepKey); ok {
				a.saveProvider(editor, params, false)
			}
		})
		button := secondaryButton
		if primary {
			button = primaryButton
		}
		button(c, msgs.ProviderSaveApply()).MinHeight(32).Disabled(blocked).OnClick(func() {
			if params, ok := editor.input(keepKey); ok {
				a.saveProvider(editor, params, true)
			}
		})
	})
}

func providerFormStatus(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, busy bool) {
	if busy {
		ui.Text(c, msgs.ProviderWorking()).Role(ui.RoleStatus).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
	}
	if len(editor.fields) > 0 {
		ui.Text(c, msgs.ConnectionCheckFields()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
	}
}

func providerFormRow(c *ui.Context, t *ui.Theme, label string, controls func()) {
	ui.Row(c).FillWidth().MinWidth(0).Gap(12).AlignItems(ui.Start).Children(func() {
		ui.Text(c, label).Width(112).Shrink(0).Padding(7, 0).FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction).TextColor(t.Text)
		ui.Column(c).Grow(1).MinWidth(0).AlignItems(ui.Start).Gap(6).Children(controls)
	})
}

func (a *appModel) providerInput(c *ui.Context, editor *providerEditor, key, label, placeholder string, value *string, secret, blocked bool) ui.Element {
	c.Key("provider-field-" + key)
	field := preferenceInput(c, value, editor.fields[key] != "", a.workTheme.InputBorder).FillWidth().MinWidth(0).Label(label).Placeholder(placeholder).Disabled(blocked)
	if secret {
		field.Password()
	}
	return field.OnChange(func() {
		delete(editor.fields, key)
		if key == "Endpoint" {
			editor.Models = nil
		}
		if key == "Endpoint" || secret {
			editor.resetProbe()
		}
	})
}

func (a *appModel) providerField(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, key, label, placeholder string, value *string, secret, blocked bool) {
	providerFormRow(c, t, label, func() {
		a.providerInput(c, editor, key, label, placeholder, value, secret, blocked)
		providerFieldError(c, t, msgs, editor.fields[key])
	})
}

func (a *appModel) providerEndpoint(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, blocked bool) {
	a.providerField(c, t, msgs, editor, "Endpoint", msgs.ProviderBaseURL(), "https://api.example.com/v1", &editor.BaseURL, false, blocked)
}

func (a *appModel) providerProtocol(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, blocked bool) {
	providerFormRow(c, t, msgs.ProtocolLabel(), func() {
		c.Key("provider-protocol")
		wires := [...]pi.WireAPI{pi.OpenAICompletions, pi.OpenAIResponses}
		selected := max(0, slices.Index(wires[:], editor.WireAPI))
		theme.PreferenceSegmentedSelection(c, &selected, msgs.ProtocolLabel(), blocked, false, msgs.ProtocolChatCompletionsLabel(), msgs.ProtocolResponsesLabel()).OnChange(func() {
			editor.WireAPI, editor.Models = wires[selected], nil
			editor.resetProbe()
		})
	})
}

func (a *appModel) providerModels(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, discover, blocked bool) {
	providerFormRow(c, t, msgs.ConnectionFieldModel(), func() {
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			a.providerInput(c, editor, "Model", msgs.ConnectionFieldModel(), "", &editor.DefaultModel, false, blocked).Basis(128).Grow(1)
			if discover {
				a.providerTestButton(c, msgs.ProviderTestDiscover(), msgs.ProviderTestDiscover(), editor, false, blocked)
			}
		})
		providerFieldError(c, t, msgs, editor.fields["Model"])
		if discover || editor.probe.kind != "tested" {
			editor.probe.render(c, t)
		}
		const visibleModels = 8
		if len(editor.Models) == 0 {
			return
		}
		choose := func(model string) {
			editor.DefaultModel = model
			delete(editor.fields, "Model")
		}
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).Children(func() {
			for _, model := range editor.Models[:min(visibleModels, len(editor.Models))] {
				c.Key("provider-model-" + model)
				selected := editor.DefaultModel == model
				chip := ui.Button(c, "").Label(model).AccessSelected(selected).MinHeight(32).MaxWidth(168).Padding(4, 8).Disabled(blocked)
				ink := t.TextMuted
				if selected {
					chip.Border(1, t.Success)
					ink = t.Success
				}
				chip.Children(func() {
					ui.Text(c, model).MinWidth(0).FontSize(typeMeta).FixedLineHeight(lineMeta).MaxLines(1).Ellipsis("…").TextColor(ink)
				})
				chip.OnClick(func() { choose(model) })
				appTooltip(c, chip, model)
			}
			if len(editor.Models) > visibleModels {
				options := []theme.SelectOption{{Value: "", Label: msgs.ProviderMoreModels(len(editor.Models) - visibleModels)}}
				value := ""
				for _, model := range editor.Models[visibleModels:] {
					options = append(options, theme.SelectOption{Value: model, Label: model})
					if model == editor.DefaultModel {
						value = model
					}
				}
				c.Key("provider-more-models")
				if theme.PreferenceSelect(c, &value, options, msgs.ProviderDiscovered(), blocked).MaxWidth(168).Changed() && value != "" {
					choose(value)
				}
			}
		})
	})
}

func (a *appModel) providerAdditionalModels(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, blocked bool) {
	ui.Column(c).FillWidth().MinWidth(0).AlignItems(ui.Start).Gap(6).Children(func() {
		linkButton(c, msgs.ProviderAdditionalModels()).Expanded(editor.showModels).Disabled(blocked).OnClick(func() {
			editor.showModels = !editor.showModels || editor.fields["Models"] != ""
		})
		if editor.showModels {
			c.Key("additional-models")
			formTextArea(c, &editor.additional, editor.fields["Models"] != "", a.workTheme.InputBorder).FillWidth().MinWidth(0).Lines(1, 4).
				Label(msgs.ProviderAdditionalModels()).Placeholder(msgs.ProviderAdditionalModelsHint()).Disabled(blocked).OnChange(func() { delete(editor.fields, "Models") })
			providerFieldError(c, t, msgs, editor.fields["Models"])
		}
	})
}

func providerFieldError(c *ui.Context, t *ui.Theme, msgs locales.Messages, reason string) {
	if reason == "" {
		return
	}
	var text string
	switch reason {
	case "name-length":
		text = msgs.ProviderNameLength()
	case "endpoint-length":
		text = msgs.ProviderEndpointLength()
	case "model-limit":
		text = msgs.ProviderModelLimit()
	default:
		text = connection.FieldErrorText(msgs, "", reason)
	}
	ui.Text(c, text).FontSize(typeMeta).LineHeight(1.5).TextColor(t.Danger)
}

func (a *appModel) providerTokenFields(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, blocked bool) {
	options := make([]theme.SelectOption, 0, len(providerTokenPresets)+1)
	selected := "custom"
	for i, preset := range providerTokenPresets {
		value := strconv.Itoa(i)
		options = append(options, theme.SelectOption{Value: value, Label: fmt.Sprintf("%dK / %dK", preset.context/1024, preset.output/1024)})
		if !editor.custom && editor.context == strconv.Itoa(preset.context) && editor.output == strconv.Itoa(preset.output) {
			selected = value
		}
	}
	options = append(options, theme.SelectOption{Value: "custom", Label: msgs.ProviderCustom()})
	providerFormRow(c, t, msgs.ConnectionTokenLimits(), func() {
		c.Key("provider-token-presets")
		if theme.PreferenceSelect(c, &selected, options, msgs.ConnectionTokenLimits(), blocked).FillWidth().Changed() {
			editor.custom = selected == "custom"
			if !editor.custom {
				index, _ := strconv.Atoi(selected)
				preset := providerTokenPresets[index]
				editor.context, editor.output = strconv.Itoa(preset.context), strconv.Itoa(preset.output)
				delete(editor.fields, "ContextTokens")
				delete(editor.fields, "OutputTokens")
			}
		}
	})
	if selected == "custom" {
		a.providerField(c, t, msgs, editor, "ContextTokens", msgs.ConnectionFieldContextTokens(), "131072", &editor.context, false, blocked)
		a.providerField(c, t, msgs, editor, "OutputTokens", msgs.ConnectionFieldOutputTokens(), "8192", &editor.output, false, blocked)
	}
}

func (a *appModel) providerDialog(c *ui.Context, base *ui.Theme) {
	if !a.providers.adding {
		return
	}
	t := theme.SettingsTheme(base)
	msgs := locales.In(c.Locale())
	editor := a.providers.editor
	blocked := a.providerChangesBlocked()
	width, height := c.Size()
	open := true
	c.Key("add-provider-dialog")
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		theme.StyleFrostedModalBackdrop(backdrop, &t)
		panel.Label(msgs.ProviderAdd()).Width(560).MaxWidth(width-32).MaxHeight(height-48).Padding(20).Gap(14).
			Background(t.Surface).Border(1, t.Border).Radius(14).Shadow(0, 8, 28, 0, ui.RGBA(0, 0, 0, .24))
		ui.Row(c).FillWidth().Shrink(0).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				pageHeading(c, msgs.ProviderAdd())
				ui.Text(c, msgs.ProviderAddHint()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			})
			closeButton := iconControlFace(c, ui.ButtonBase(c).Label(msgs.ComposerMaterialClose()), iconChrome, false).
				Size(32, 32).Shrink(0).Disabled(blocked).OnClick(a.dismissProviderDialog)
			closeButton.Children(func() { controlIcon(c, glyphCloseLine, iconChrome) })
		})
		ui.Scroll(c).FillWidth().MinWidth(0).MinHeight(0).Shrink(1).TrackScroll(&a.providers.dialogScroll).Children(func() {
			ui.Column(c).FillWidth().MinWidth(0).Gap(10).Padding(2).Children(func() {
				providerPresetGrid(c, &t, msgs, editor, blocked)
				a.providerField(c, &t, msgs, editor, "Name", msgs.FieldName(), "", &editor.Name, false, blocked)
				a.providerEndpoint(c, &t, msgs, editor, blocked)
				a.providerField(c, &t, msgs, editor, "APIKey", msgs.ConnectionFieldAPIKey(), "", &editor.APIKey, true, blocked)
				a.providerModels(c, &t, msgs, editor, true, blocked)
				a.providerAdvanced(c, &t, msgs, editor, blocked)
			})
		})
		ui.Column(c).FillWidth().Shrink(0).Gap(6).Padding(12, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			providerFormStatus(c, &t, msgs, editor, a.providerBusy())
			ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
				theme.GhostButton(c, msgs.CreateBotCancel()).MinHeight(32).Disabled(blocked).OnClick(a.dismissProviderDialog)
				a.providerSaveActions(c, msgs, editor, false, blocked, true)
			})
		})
	})
	if !open && !a.providerBusy() {
		a.dismissProviderDialog()
	}
}

func providerPresetGrid(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, blocked bool) {
	descriptions := [...]string{msgs.ProviderPresetResponses(), msgs.ProviderPresetChat(), msgs.ProviderPresetMultiModel(), "Moonshot",
		"BigModel", "DashScope", msgs.ProviderPresetLocal(), msgs.ProviderPresetCompatible()}
	ui.Text(c, msgs.ProviderPreset()).FontSize(typeMeta).FontWeight(600).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Children(func() {
		for start := 0; start < len(providerPresets); start += 4 {
			c.Key("preset-row-" + strconv.Itoa(start))
			ui.Row(c).FillWidth().MinWidth(0).Gap(8).Children(func() {
				for i := start; i < start+4; i++ {
					label := providerPresets[i].name
					if label == "" {
						label = msgs.ProviderCustom()
					}
					c.Key("provider-preset-" + strconv.Itoa(i))
					selected := editor.preset == i
					button := ui.Button(c, "").Label(label).AccessSelected(selected).Disabled(blocked).Column().AlignItems(ui.Stretch).
						Basis(0).Grow(1).MinWidth(0).MinHeight(52).Gap(2).Padding(8, 10).Radius(8).Border(1, t.Focus)
					if selected {
						button.Border(1.5, t.Accent)
					}
					button.Children(func() {
						ui.Text(c, label).MinWidth(0).MaxLines(1).Ellipsis("…").FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction)
						ui.Text(c, descriptions[i]).MinWidth(0).MaxLines(1).Ellipsis("…").FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
					})
					button.OnClick(func() {
						if editor.preset != i {
							editor.choosePreset(i)
						}
					})
				}
			})
		}
	})
}

func (a *appModel) providerAdvanced(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, blocked bool) {
	advancedError := editor.fields["ContextTokens"] != "" || editor.fields["OutputTokens"] != "" || editor.fields["Models"] != ""
	ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
		linkButton(c, msgs.ProviderAdvanced()).Expanded(editor.advanced).Disabled(blocked).OnClick(func() {
			editor.advanced = !editor.advanced || advancedError
		})
		protocol := msgs.ProviderProtocolChatShort()
		if editor.WireAPI == pi.OpenAIResponses {
			protocol = msgs.ProtocolResponsesLabel()
		}
		limits := msgs.ProviderCustom()
		context, _ := strconv.Atoi(editor.context)
		output, _ := strconv.Atoi(editor.output)
		if !editor.custom && context > 0 && output > 0 && context%1024 == 0 && output%1024 == 0 {
			limits = fmt.Sprintf("%dK / %dK", context/1024, output/1024)
		}
		ui.Text(c, protocol+" · "+limits).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
	})
	if editor.advanced {
		a.providerProtocol(c, t, msgs, editor, blocked)
		a.providerAdditionalModels(c, t, msgs, editor, blocked)
		a.providerTokenFields(c, t, msgs, editor, blocked)
	}
}
