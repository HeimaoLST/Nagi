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
)

type providerSettings struct {
	tab          int
	selectedID   string
	focusID      string
	editor       *providerEditor
	drafts       map[string]*providerEditor
	first        *providerEditor
	snapshot     pi.ProductProviderList
	adding       bool
	listScroll   ui.ScrollState
	detailScroll ui.ScrollState
	dialogScroll ui.ScrollState
	busy         bool
	healthTimer  *time.Timer
	cancelTest   context.CancelFunc
	failure      providerFeedback
}

type providerEditor struct {
	pi.ProductProviderAccount
	APIKey          string
	preset          int
	context         string
	output          string
	additional      string
	advanced        bool
	custom          bool
	showModels      bool
	fields          map[string]string
	probe           providerFeedback
	tested          bool
	latency         uint64
	discovered      []pi.ProductDiscoveredModel
	modelsChanged   bool
	fallbackChanged bool
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
	a.providers.snapshot = value
	a.scheduleProviderHealthRefresh(value)
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
	return a.providerBusy() || !a.productProfileReady() || a.dataRecoveryBlocksChanges()
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

func newProviderDraft() *providerEditor {
	return newProviderEditor(pi.ProductProviderAccount{
		ProviderID: "provider-" + newContactID(), WireAPI: pi.OpenAICompletions, Enabled: true,
		ContextTokens: 131072, OutputTokens: 8192,
	})
}

func providerPresetName(msgs locales.Messages, index int) string {
	if index < 0 {
		return msgs.ProviderPrevious()
	}
	if name := providerPresets[index].name; name != "" {
		return name
	}
	return msgs.ProviderCustom()
}

func (a *appModel) chooseFirstProvider(index int) {
	s := &a.providers
	if s.first == nil {
		s.first = newProviderDraft()
	}
	e := s.first
	if index < 0 {
		previous := a.connectionSaved
		e.choosePreset(len(providerPresets) - 1)
		e.preset = -1
		e.BaseURL, e.DefaultModel = previous.Endpoint, previous.Model
		if previous.API == connectionAPIResponses {
			e.WireAPI = pi.OpenAIResponses
		}
		e.context, e.output = strconv.Itoa(previous.ContextTokens), strconv.Itoa(previous.OutputTokens)
	} else {
		e.choosePreset(index)
	}
	e.Name = providerPresetName(locales.In(a.uiLocale), index)
	s.detailScroll.Y = 0
	if !s.adding {
		s.editor = e
	}
}

func (a *appModel) addProvider() {
	if a.providerChangesBlocked() || a.providers.adding {
		return
	}
	a.providers.editor = newProviderDraft()
	a.providers.adding = true
	a.providers.dialogScroll.Y = 0
	a.providers.failure = providerFeedback{}
}

func (a *appModel) dismissProviderDialog() {
	a.providers.adding = false
	a.providers.editor = a.providers.drafts[a.providers.selectedID]
	if a.providers.editor == nil {
		a.providers.editor = a.providers.first
	}
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

func (e *providerEditor) resetModelCatalog() {
	e.Models, e.EnabledModels, e.ModelOverrides, e.discovered = nil, nil, nil, nil
	e.modelsChanged = false
	e.resetProbe()
}

func (e *providerEditor) choosePreset(index int) {
	preset := providerPresets[index]
	e.preset = index
	e.Name, e.BaseURL, e.DefaultModel, e.WireAPI = preset.name, preset.endpoint, preset.model, preset.wire
	e.context, e.output, e.custom = "131072", strconv.Itoa(preset.output), false
	e.additional, e.fields = "", nil
	e.FallbackOnly, e.fallbackChanged = nil, false
	e.resetModelCatalog()
}

func (e *providerEditor) input(keepKey bool) (pi.ProductProviderTestParams, bool) {
	account := e.ProductProviderAccount
	if !e.modelsChanged {
		account.EnabledModels = nil
	}
	if !e.fallbackChanged {
		account.FallbackOnly = nil
	}
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
			editor.probe = providerFeedback{kind: "tested", models: len(result.Models), latency: result.LatencyMs}
			editor.tested, editor.latency = true, result.LatencyMs
			editor.discovered = result.DiscoveredModels
			for _, id := range result.Models {
				if len(editor.Models) == 128 {
					break
				}
				if !slices.Contains(editor.Models, id) {
					editor.Models = append(editor.Models, id)
				}
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
		for _, account := range s.snapshot.Accounts {
			if account.ProviderID == editor.ProviderID {
				savedEditor = newProviderEditor(account.ProductProviderAccount)
				break
			}
		}
		savedEditor.discovered = editor.discovered
		savedEditor.tested, savedEditor.latency = editor.tested, editor.latency
		s.drafts[editor.ProviderID] = savedEditor
		if s.adding || s.first == editor {
			s.focusID = editor.ProviderID
			s.detailScroll.Y = 0
		}
		if s.first == editor {
			s.first = nil
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
	case "reorder":
		summary = msgs.ProviderReorderFailed()
	case "override":
		summary = msgs.ProviderPinFailed()
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
	id         pi.ProductModelRole
	name, hint string
	route      *pi.ProductModelRoute
}

func providerRoleRows(msgs locales.Messages, roles pi.ProductResolvedRoles) [4]providerRoleRow {
	return [4]providerRoleRow{
		{"dialogue", msgs.ProviderRoleDialogue(), msgs.ProviderRoleDialogueHint(), roles.Dialogue},
		{"coding", msgs.ProviderRoleCoding(), msgs.ProviderRoleCodingHint(), roles.Coding},
		{"reasoning", msgs.ProviderRoleReasoning(), msgs.ProviderRoleReasoningHint(), roles.Reasoning},
		{"utility", msgs.ProviderRoleUtility(), msgs.ProviderRoleUtilityHint(), roles.Utility},
	}
}

func (a *appModel) providerTabs(c *ui.Context, msgs locales.Messages, count int) {
	c.Key("connection-subtabs")
	selected := a.providers.tab
	theme.PreferenceSegmentedSelection(c, &selected, msgs.SettingsConnection(), false, false,
		msgs.ProviderTabAccounts(count), msgs.ProviderTabRouting()).OnChange(func() {
		a.providers.tab = selected
		a.settingsScroll.Y = 0
	})
}

func (a *appModel) providerStatus(c *ui.Context, t *ui.Theme, pool pi.ProductProviderList, roles [4]providerRoleRow) {
	msgs := locales.In(c.Locale())
	enabled, active := 0, 0
	var degraded bool
	for _, account := range pool.Accounts {
		if account.Enabled {
			enabled++
		}
		degraded = degraded || account.Enabled && (account.CredentialStatus != "ok" || account.HealthSummary != "healthy" && account.HealthSummary != "")
	}
	for _, role := range roles {
		if role.route != nil {
			active++
		}
	}
	online := a.productProfileReady()
	pending := online && a.productProvidersPending()
	summary := msgs.ProviderEnabledCount(enabled) + " · " + msgs.ProviderActiveCount(active)
	ink := t.TextMuted
	switch {
	case !online:
		summary = msgs.ProviderRuntimeUnavailable()
	case len(pool.Accounts) == 0 && !pending:
		summary = msgs.ProviderQuickStartHint()
		if a.connectionSaved != nil {
			summary = msgs.ProviderImportHint()
		}
	case pending:
		summary += " · " + msgs.ProviderPending()
		ink = t.Warning
	case degraded || active < len(roles):
		if status := pool.RoleStatus["dialogue"].Status; status == "credential_missing" || status == "auth_failed" {
			summary += " · " + msgs.ProviderCredentialsUnavailable()
		} else {
			summary += " · " + msgs.ProviderRoutesNeedAttention()
		}
		ink = t.Warning
	default:
		ink = t.Success
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
			} else if ink == t.Success {
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
	options := a.providerPinOptions(msgs, pool, roles)
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
			c.Key(string(row.id))
			settingsPreferenceRow(c, t, row.name, row.hint, func() {
				ui.Column(c).FillWidth().MinWidth(0).Gap(4).AlignItems(ui.Start).Children(func() {
					if row.route != nil {
						providerRoute(c, t, msgs, row.route, pool.Accounts)
					}
					providerRoleStatus(c, t, msgs, pool.RoleStatus[row.id], row.route)
					a.providerPinPicker(c, t, msgs, pool, row, options)
				})
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

func (a *appModel) providerWorkspace(c *ui.Context, t *ui.Theme, pool pi.ProductProviderList, wide bool) {
	msgs := locales.In(c.Locale())
	roles := providerRoleRows(msgs, pool.ResolvedRoles)
	a.providerStatus(c, t, pool, roles)
	var account pi.ProductProviderAccountView
	var editor *providerEditor
	index := -1
	if len(pool.Accounts) == 0 {
		if a.providers.first == nil || a.providers.first.preset < 0 && a.connectionSaved == nil {
			initial := 0
			if a.connectionSaved != nil {
				initial = -1
			}
			a.chooseFirstProvider(initial)
		}
		editor = a.providers.first
		if !a.providers.adding {
			a.providers.editor = editor
		}
	} else {
		index = max(0, slices.IndexFunc(pool.Accounts, func(account pi.ProductProviderAccountView) bool { return account.ProviderID == a.providers.selectedID }))
		account = pool.Accounts[index]
		a.selectProvider(account)
		editor = a.providers.drafts[account.ProviderID]
	}
	blocked := a.providerChangesBlocked()
	background := t.Surface.Mix(t.Background, .45)
	split := ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch)
	if !wide {
		split.Column()
	}
	split.FillWidth().MinWidth(0).Background(t.Surface).Border(1, t.Border).Radius(12).Clip().Children(func() {
		list := ui.Column(c).MinWidth(0).MinHeight(0).Shrink(0).Background(background).BorderColor(t.Border)
		if wide {
			list.Width(244).BorderWidth(0, 1, 0, 0)
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
				scroll.MaxHeight(122)
			}
			scroll.Children(func() {
				ui.Column(c).FillWidth().MinWidth(0).Padding(6, 14, 6, 6).Gap(4).Children(func() {
					if len(pool.Accounts) == 0 {
						a.providerQuickStart(c, t, msgs, editor, blocked)
						return
					}
					for i, row := range pool.Accounts {
						c.Key("provider-item-" + row.ProviderID)
						status, ink := providerAccountStatus(t, msgs, row)
						meta := row.DefaultModel + " · " + msgs.ProviderModelCount(len(row.Models))
						if row.FallbackOnly != nil && *row.FallbackOnly {
							meta += " · " + msgs.ProviderFallbackTag()
						}
						if assigned := providerRoleNames(msgs, row.ProviderID, roles); assigned != "" {
							meta += " · " + assigned
						}
						a.providerListItem(c, t, row.Name, meta, status, ink, i+1, a.providers.selectedID == row.ProviderID, blocked, func() { a.selectProvider(row) })
					}
				})
			})
		})
		c.Key("provider-detail-" + editor.ProviderID)
		a.providerDetail(c, t, msgs, pool, index, account, editor, providerRoleNames(msgs, account.ProviderID, roles), background, blocked)
	})
}

func (a *appModel) providerQuickStart(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, blocked bool) {
	if previous := a.connectionSaved; previous != nil {
		c.Key("previous-provider")
		a.providerListItem(c, t, msgs.ProviderPrevious(), previous.Model+" · "+previous.Endpoint, "", t.TextMuted, 0, editor.preset < 0, blocked, func() {
			if editor.preset >= 0 {
				a.chooseFirstProvider(-1)
			}
		})
	}
	ui.Text(c, msgs.ProviderQuickStart()).Padding(4, 10).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
	for i, preset := range providerPresets {
		c.Key("quick-provider-" + strconv.Itoa(i))
		meta := preset.model
		if meta == "" {
			meta = msgs.ProviderPresetCompatible()
		}
		a.providerListItem(c, t, providerPresetName(msgs, i), meta, "", t.TextMuted, 0, editor.preset == i, blocked, func() {
			if editor.preset != i {
				a.chooseFirstProvider(i)
			}
		})
	}
}

func providerDot(c *ui.Context, color ui.Color) {
	ui.Box(c).Size(6, 6).Radius(3).Shrink(0).Background(color)
}

func providerRoute(c *ui.Context, t *ui.Theme, msgs locales.Messages, route *pi.ProductModelRoute, accounts []pi.ProductProviderAccountView) {
	name := route.ProviderName
	if name == "" {
		name = msgs.ProviderPendingRemoval()
		for _, account := range accounts {
			if account.ProviderID == route.ProviderID {
				name = account.Name
				break
			}
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
	ui.Column(c).FillWidth().MinWidth(0).AlignItems(ui.Start).Gap(2).Children(func() {
		ui.Row(c).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
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
	if account.HealthSummary != "" && account.HealthSummary != "healthy" {
		return providerHealthState(msgs, account.HealthSummary), t.Warning
	}
	return msgs.ProviderReady(), t.Success
}

func (a *appModel) providerListItem(c *ui.Context, t *ui.Theme, name, meta, status string, ink ui.Color, priority int, selected, blocked bool, selectItem func()) {
	palette := a.workTheme
	palette.NavHover, palette.NavSelected = t.SurfaceHover, t.Selection
	item := navigationControl(c, ui.ButtonBase(c).Label(name).Selected(selected), palette, selected).
		FillWidth().MinWidth(0).Height(52).MinHeight(52).Shrink(0).Margin(0).Column().AlignItems(ui.Stretch).Gap(2).Padding(8, 10).Radius(8).Disabled(blocked).Transition(capabilityTransition)
	item.Children(func() {
		ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(8).Children(func() {
			providerDot(c, ink)
			if priority > 0 {
				ui.Text(c, "#"+strconv.Itoa(priority)).Shrink(0).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
			ui.Text(c, name).Grow(1).MinWidth(0).FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction).MaxLines(1).Ellipsis("…").TextColor(t.Text)
			if status != "" {
				ui.Text(c, status).Shrink(0).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(ink)
			}
		})
		ui.Text(c, meta).MinWidth(0).Padding(0, 0, 0, 14).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
	})
	appTooltip(c, item, name+"\n"+meta)
	item.OnClick(selectItem)
	if selected && a.providers.focusID != "" && a.providers.focusID == a.providers.selectedID && priority > 0 {
		item.Focus().ScrollIntoView()
		a.providers.focusID = ""
	}
}

func (a *appModel) providerDetail(c *ui.Context, t *ui.Theme, msgs locales.Messages, pool pi.ProductProviderList, index int, account pi.ProductProviderAccountView, editor *providerEditor, roles string, background ui.Color, blocked bool) {
	saved := index >= 0
	name := account.Name
	if !saved {
		name = providerPresetName(msgs, editor.preset)
	}
	keepKey := account.CredentialStatus == "ok" && account.BaseURL == editor.BaseURL && account.WireAPI == editor.WireAPI
	detail := ui.Column(c).Grow(1).MinWidth(0).MinHeight(0)
	detail.Children(func() {
		ui.Row(c).FillWidth().MinWidth(0).Shrink(0).Wrap().AlignItems(ui.Center).Gap(8).Padding(14, 18).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(
			func() {
				ui.Column(c).Basis(180).Grow(1).MinWidth(0).Gap(2).Children(func() {
					status, ink := providerAccountStatus(t, msgs, account)
					if !saved {
						status, ink = msgs.ProviderNotSaved(), t.TextMuted
					} else if editor.tested && status == msgs.ProviderReady() {
						status = msgs.ProviderLatencyReady(strconv.FormatUint(editor.latency, 10))
					}
					ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
						title := ui.Text(c, name).MinWidth(0).FontSize(15).FontWeight(600).FixedLineHeight(20).MaxLines(1).Ellipsis("…")
						appTooltip(c, title, name)
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
					if saved {
						a.providerPriority(c, msgs, pool, index, blocked)
						c.Key("provider-enabled")
						selected := 0
						if !editor.Enabled {
							selected = 1
						}
						theme.PreferenceSegmentedSelection(c, &selected, msgs.ProviderAccountStatus(), blocked, false,
							msgs.ProviderEnabled(), msgs.ProviderToggleDisabled()).Wrap().OnChange(func() { editor.Enabled = selected == 0 })
					}
					a.providerTestButton(c, msgs.ProviderTestSync(), msgs.ProviderTestAccount(name), editor, keepKey, blocked)
				})
			})
		body := func() {
			ui.Column(c).FillWidth().MinWidth(0).Gap(10).Padding(14, 32, 14, 18).Children(func() {
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
				a.providerFallback(c, t, msgs, editor, blocked)
				a.providerAdvanced(c, t, msgs, editor, blocked)
				a.providerHealth(c, t, msgs, account, blocked)
			})
		}
		ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).TrackScroll(&a.providers.detailScroll).Children(body)
		ui.Column(c).FillWidth().Shrink(0).Gap(6).Padding(12, 18).Background(background).
			BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(
			func() {
				providerFormStatus(c, t, msgs, editor, a.providerBusy())
				ui.Row(c).FillWidth().MinWidth(0).Wrap().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
					if saved {
						dangerButton(c, msgs.ProviderDelete()).Label(msgs.ProviderDeleteAccount(name)).MinHeight(32).Disabled(blocked).
							OnClick(func() { a.changeProviderAccount(account.ProviderID, false) })
					} else {
						ui.Spacer(c)
					}
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
			editor.resetModelCatalog()
		} else if secret {
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
		theme.PreferenceSegmentedSelection(c, &selected, msgs.ProtocolLabel(), blocked, false, msgs.ProtocolChatCompletionsLabel(), msgs.ProtocolResponsesLabel()).Wrap().OnChange(func() {
			editor.WireAPI = wires[selected]
			editor.resetModelCatalog()
		})
	})
}

func (a *appModel) providerModels(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, discover, blocked bool) {
	providerFormRow(c, t, msgs.ProviderDefaultModel(), func() {
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			a.providerInput(c, editor, "Model", msgs.ProviderDefaultModel(), "", &editor.DefaultModel, false, blocked).Basis(128).Grow(1)
			if discover {
				a.providerTestButton(c, msgs.ProviderTestSync(), msgs.ProviderTestDiscover(), editor, false, blocked)
			}
		})
		providerFieldError(c, t, msgs, editor.fields["Model"])
		editor.probe.render(c, t)
	})
	if len(editor.Models) == 0 && editor.EnabledModels == nil {
		return
	}
	models := providerModelIDs(editor.ProductProviderAccount)
	options := make([]theme.SelectOption, 0, len(models))
	for _, id := range models {
		options = append(options, theme.SelectOption{Value: id, Label: id})
	}
	providerFormRow(c, t, msgs.ProviderModelPool(), func() {
		c.Key("provider-default-picker")
		selected := editor.DefaultModel
		if theme.PreferenceSelect(c, &selected, options, msgs.ProviderPickDefault(), blocked).FillWidth().Changed() {
			editor.DefaultModel = selected
			delete(editor.fields, "Model")
		}
		active := editor.EnabledModels
		if active == nil {
			automatic := make([]string, 0, len(models))
			for _, id := range models {
				model := editor.discoveredModel(id)
				if model.Recommended || id == editor.DefaultModel && model.ID != "" && !model.PriceProtected && (model.IsChat == nil || *model.IsChat) {
					automatic = append(automatic, id)
				}
			}
			active = &automatic
			if len(editor.discovered) == 0 {
				ui.Text(c, msgs.ProviderAutomaticPoolHint()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
		}
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).Children(func() {
			for _, id := range models {
				c.Key("provider-model-" + id)
				model := editor.discoveredModel(id)
				chip := capabilityChip(c, t, id, providerModelHints(msgs, model), slices.Contains(*active, id), blocked).
					Label(msgs.ProviderToggleModel(id)).MaxWidthPercent(100)
				chip.OnClick(func() {
					next := make([]string, 0, len(*active)+1)
					next = append(next, (*active)...)
					if index := slices.Index(next, id); index >= 0 {
						next = slices.Delete(next, index, index+1)
					} else {
						next = append(next, id)
					}
					editor.EnabledModels, editor.modelsChanged = &next, true
				})
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
	case connection.ValidationRequired:
		text = msgs.ValidationRequired()
	case connection.ValidationEndpointWhitespace:
		text = msgs.ValidationEndpointWhitespace()
	case connection.ValidationEndpointURL:
		text = msgs.ValidationEndpointURL()
	case connection.ValidationEndpointUserinfo:
		text = msgs.ValidationEndpointUserinfo()
	case connection.ValidationEndpointQuery:
		text = msgs.ValidationEndpointQuery()
	case connection.ValidationEndpointFragment:
		text = msgs.ValidationEndpointFragment()
	case connection.ValidationPositive:
		text = msgs.ValidationPositive()
	case connection.ValidationContextGap:
		text = msgs.ValidationContextGap()
	default:
		text = reason
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
			ui.Column(c).FillWidth().MinWidth(0).Gap(10).Padding(2, 14, 2, 2).Children(func() {
				providerPresetGrid(c, &t, msgs, editor, blocked)
				a.providerField(c, &t, msgs, editor, "Name", msgs.FieldName(), "", &editor.Name, false, blocked)
				a.providerProtocol(c, &t, msgs, editor, blocked)
				a.providerEndpoint(c, &t, msgs, editor, blocked)
				a.providerField(c, &t, msgs, editor, "APIKey", msgs.ConnectionFieldAPIKey(), "", &editor.APIKey, true, blocked)
				a.providerModels(c, &t, msgs, editor, true, blocked)
				a.providerFallback(c, &t, msgs, editor, blocked)
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
					label := providerPresetName(msgs, i)
					c.Key("provider-preset-" + strconv.Itoa(i))
					selected := editor.preset == i
					button := ui.Button(c, "").Label(label).AccessSelected(selected).Disabled(blocked).Column().AlignItems(ui.Stretch).
						Basis(0).Grow(1).MinWidth(0).MinHeight(52).Gap(2).Padding(8, 10).Radius(8).Border(1, t.Border).Transition(capabilityTransition)
					if selected {
						button.Background(t.Selection).Border(1, t.Focus)
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
		linkButton(c, msgs.ProviderAdvanced()).Padding(4, 0).Expanded(editor.advanced).Disabled(blocked).OnClick(func() {
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
		a.providerAdditionalModels(c, t, msgs, editor, blocked)
		a.providerTokenFields(c, t, msgs, editor, blocked)
	}
}
