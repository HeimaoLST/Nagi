package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
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
	openerID     string
	editor       *providerEditor
	drafts       map[string]*providerEditor
	firstDrafts  map[string]*providerEditor
	catalog      connection.ProviderCatalog
	catalogIcons map[string]*ui.SVG
	fallbackIcon *ui.SVG
	catalogFolds map[string]bool
	first        *providerEditor
	snapshot     pi.ProductProviderList
	adding       bool
	detailScroll ui.ScrollState
	busy         bool
	healthTimer  *time.Timer
	cancelTest   context.CancelFunc
	failure      providerFeedback
}

type providerEditor struct {
	pi.ProductProviderAccount
	APIKey          string
	preset          int
	presetID        string
	modelInput      string
	replacingKey    bool
	focusKey        bool
	noKey           bool
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

func (a *appModel) refreshProviderCatalog() {
	var dir string
	if a.storePath != "" {
		dir = filepath.Dir(a.storePath)
	}
	s := &a.providers
	s.catalog = connection.LoadProviderCatalog(dir)
	s.fallbackIcon, _ = ui.ParseSVG(s.catalog.FallbackIconSVG)
	s.catalogIcons = make(map[string]*ui.SVG, len(s.catalog.Presets))
	for _, preset := range s.catalog.Presets {
		if icon, err := ui.ParseSVG(preset.IconSVG); err == nil && len(icon.Unsupported()) == 0 {
			s.catalogIcons[preset.ID] = icon
		}
	}
}

func (a *appModel) providerCatalog() *connection.ProviderCatalog {
	if a.providers.catalogIcons == nil {
		a.refreshProviderCatalog()
	}
	return &a.providers.catalog
}

func (a *appModel) providerIcon(preset connection.ProviderPreset) *ui.SVG {
	a.providerCatalog()
	if icon := a.providers.catalogIcons[preset.ID]; icon != nil {
		return icon
	}
	return a.providers.fallbackIcon
}

func (a *appModel) accountPreset(account pi.ProductProviderAccount) connection.ProviderPreset {
	catalog := a.providerCatalog()
	if draft := a.providers.drafts[account.ProviderID]; draft != nil && draft.BaseURL == account.BaseURL {
		if preset, ok := catalog.PresetByID(draft.presetID); ok && !preset.Custom {
			return preset
		}
	}
	preset, _ := catalog.MatchAccount(account.BaseURL, account.Name)
	return preset
}

func (a *appModel) chooseFirstProvider(index int) {
	s := &a.providers
	var preset connection.ProviderPreset
	if index >= 0 {
		preset = a.providerCatalog().Presets[index]
	}
	key := ""
	if index >= 0 {
		key = "preset:" + preset.ID
	}
	if s.firstDrafts == nil {
		s.firstDrafts = make(map[string]*providerEditor)
	}
	e := s.firstDrafts[key]
	if e == nil {
		e = newProviderDraft()
		if index >= 0 {
			e.setPreset(preset, a.uiLocale)
		} else if previous := a.connectionSaved; previous != nil {
			e.Name = locales.In(a.uiLocale).ProviderPrevious()
			e.BaseURL, e.DefaultModel = previous.Endpoint, previous.Model
			if previous.API == connectionAPIResponses {
				e.WireAPI = pi.OpenAIResponses
			}
			e.context, e.output = strconv.Itoa(previous.ContextTokens), strconv.Itoa(previous.OutputTokens)
		}
		s.firstDrafts[key] = e
	}
	e.preset = index
	s.first, s.editor = e, e
}

func (a *appModel) addProvider() {
	if a.providerChangesBlocked() || a.providers.adding {
		return
	}
	for i, preset := range a.providerCatalog().Presets {
		if preset.Custom {
			a.chooseFirstProvider(i)
			a.openProviderDialog()
			return
		}
	}
	s := &a.providers
	if s.firstDrafts == nil {
		s.firstDrafts = make(map[string]*providerEditor)
	}
	const key = "add"
	if s.firstDrafts[key] == nil {
		s.firstDrafts[key] = newProviderDraft()
	}
	s.first, s.editor = s.firstDrafts[key], s.firstDrafts[key]
	a.openProviderDialog()
}

func (a *appModel) dismissProviderDialog() {
	a.providers.adding = false
	a.providers.focusID = a.providers.openerID
	a.providers.openerID = ""
}

func (a *appModel) restoreProviderFocus(control ui.Element, id string) {
	if !a.providers.adding && a.providers.focusID == id && !control.IsDisabled() {
		control.Focus()
		a.providers.focusID = ""
	}
}

func (a *appModel) selectProvider(account pi.ProductProviderAccountView) {
	s := &a.providers
	if s.drafts == nil {
		s.drafts = make(map[string]*providerEditor)
	}
	e := s.drafts[account.ProviderID]
	if e == nil {
		e = newProviderEditor(account.ProductProviderAccount)
		preset := a.accountPreset(account.ProductProviderAccount)
		e.presetID, e.noKey = preset.ID, preset.NoKey
		s.drafts[account.ProviderID] = e
	}
	s.selectedID, s.editor = account.ProviderID, e
	a.openProviderDialog()
}

func (a *appModel) openProviderDialog() {
	s := &a.providers
	s.adding = true
	s.detailScroll.Y = 0
	s.failure = providerFeedback{}
}

func (a *appModel) providerPool() pi.ProductProviderList {
	if host := a.productHost(); host != nil {
		return host.productProviders
	}
	return a.providers.snapshot
}

func (a *appModel) openProviderPreset(pool pi.ProductProviderList, id string) {
	for index, preset := range a.providerCatalog().Presets {
		if preset.ID != id {
			continue
		}
		if !preset.Custom {
			for _, account := range pool.Accounts {
				if a.accountPreset(account.ProductProviderAccount).ID == id {
					a.selectProvider(account)
					return
				}
			}
		}
		a.chooseFirstProvider(index)
		a.openProviderDialog()
		return
	}
}

func (a *appModel) addAnotherProvider(account pi.ProductProviderAccount, preset connection.ProviderPreset, locale string) {
	s := &a.providers
	if s.firstDrafts == nil {
		s.firstDrafts = make(map[string]*providerEditor)
	}
	key := "account:" + account.ProviderID
	if s.firstDrafts[key] == nil {
		e := newProviderDraft()
		if preset.ID != "" && !preset.Custom {
			e.setPreset(preset, locale)
		}
		if e.Name == "" {
			e.Name = account.Name
		}
		e.BaseURL, e.DefaultModel, e.WireAPI = account.BaseURL, account.DefaultModel, account.WireAPI
		e.context, e.output = strconv.Itoa(account.ContextTokens), strconv.Itoa(account.OutputTokens)
		s.firstDrafts[key] = e
	}
	s.first, s.editor = s.firstDrafts[key], s.firstDrafts[key]
	a.openProviderDialog()
}

func (e *providerEditor) resetProbe() {
	e.probe, e.tested, e.latency = providerFeedback{}, false, 0
}

func (e *providerEditor) resetModelCatalog() {
	e.Models, e.EnabledModels, e.ModelOverrides, e.discovered = nil, nil, nil, nil
	e.modelsChanged = false
	e.resetProbe()
}

func (e *providerEditor) setPreset(preset connection.ProviderPreset, locale string) {
	e.presetID, e.noKey = preset.ID, preset.NoKey
	e.Name, e.BaseURL, e.DefaultModel = preset.Name.Resolve(locale), preset.Endpoint, preset.DefaultModel
	if preset.WireAPI != "" {
		e.WireAPI = pi.WireAPI(preset.WireAPI)
	}
	if preset.ContextTokens > 0 {
		e.context = strconv.Itoa(preset.ContextTokens)
	}
	if preset.OutputTokens > 0 {
		e.output = strconv.Itoa(preset.OutputTokens)
	}
}

func (e *providerEditor) localWithoutKey() bool {
	if !e.noKey {
		return false
	}
	endpoint, err := url.Parse(e.BaseURL)
	if err != nil || endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return false
	}
	host := endpoint.Hostname()
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
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
		if fields["Endpoint"] == "" && e.localWithoutKey() {
			key = "nagi-loopback"
		} else {
			fields["APIKey"] = connection.ValidationRequired
		}
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
		savedEditor.presetID, savedEditor.noKey = editor.presetID, editor.noKey
		savedEditor.discovered = editor.discovered
		savedEditor.tested, savedEditor.latency = editor.tested, editor.latency
		s.drafts[editor.ProviderID] = savedEditor
		a.dismissProviderDialog()
		if s.first == editor {
			s.focusID = "account-" + editor.ProviderID
			for id, draft := range s.firstDrafts {
				if draft == editor {
					delete(s.firstDrafts, id)
					break
				}
			}
			s.first = nil
		}
		s.selectedID, s.editor = editor.ProviderID, savedEditor
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
					a.dismissProviderDialog()
					a.providers.focusID = "add-provider"
					if len(a.providerPool().Accounts) == 0 {
						a.providers.focusID = "preset-custom"
					}
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
		Radius(11).Border(1, t.Border).Background(t.Surface).Children(
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
			} else if online && pool.SavedRevision != nil {
				ui.Text(c, msgs.ProviderAllApplied()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Success)
			}
		})
	if !a.providers.adding {
		a.providers.failure.render(c, t)
	}
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
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Children(func() {
		theme.PreferenceSectionHeaderWithSubtitle(c, t, msgs.ProviderRouting(), msgs.ProviderRoutingSubtitle())
		theme.PreferenceGroup(c, t, func() {
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
					ui.Column(c).FillWidth().MinWidth(0).Gap(6).AlignItems(ui.End).Children(func() {
						var thinking string
						if row.route != nil {
							thinking = a.providerRoute(c, t, msgs, row.route, pool.Accounts)
						}
						providerRoleStatus(c, t, msgs, pool.RoleStatus[row.id], row.route, thinking)
						a.providerPinPicker(c, t, msgs, pool, row, options)
					})
				}, i == len(roles)-1)
			}
		})
	})
}

func (a *appModel) providerAddButton(c *ui.Context, label string, blocked bool) {
	button := theme.TextAction(c, "+ "+label, false).Label(label).Disabled(blocked)
	a.restoreProviderFocus(button, "add-provider")
	button.OnClick(func() {
		button.Focus()
		a.providers.openerID = "add-provider"
		a.addProvider()
	})
}

func (a *appModel) providerWorkspace(c *ui.Context, t *ui.Theme, pool pi.ProductProviderList, wide bool) {
	msgs := locales.In(c.Locale())
	roles := providerRoleRows(msgs, pool.ResolvedRoles)
	a.providerStatus(c, t, pool, roles)
	blocked := a.providerChangesBlocked()
	canAdd := !blocked && len(pool.Accounts) < 64
	if previous := a.connectionSaved; len(pool.Accounts) == 0 && previous != nil {
		c.Key("previous-provider")
		preset, _ := a.providerCatalog().MatchAccount(previous.Endpoint, "")
		meta := previous.Model + " · " + previous.Endpoint
		item := providerDirectoryButton(c, t, msgs.ProviderV2ImportTitle(), canAdd).FillWidth().Radius(11).
			Background(t.Surface.Mix(t.Warning, .06)).Border(1, t.Border).Padding(10, 12).Gap(10)
		item.Children(func() {
			theme.BrandIcon(c, a.providerIcon(preset), 22)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, msgs.ProviderV2ImportTitle()).FontWeight(500).FontSize(typeAction)
				ui.Text(c, meta).FontSize(typeMeta).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
			})
			ui.Text(c, msgs.ProviderV2ImportAction()).Shrink(0).FontSize(typeAction).FontWeight(500)
		})
		appTooltip(c, item, meta)
		a.restoreProviderFocus(item, "previous-provider")
		item.OnClick(func() {
			item.Focus()
			a.providers.openerID = "previous-provider"
			a.chooseFirstProvider(-1)
			a.openProviderDialog()
		})
	}
	configured := make(map[string]ui.Color, len(pool.Accounts))
	if len(pool.Accounts) > 0 {
		c.Key("configured-providers")
		ui.Column(c).FillWidth().MinWidth(0).Gap(8).Children(func() {
			theme.PreferenceSectionHeaderWithSubtitle(c, t, msgs.ProviderConfiguredTitle(), msgs.ProviderConfiguredSubtitle(), func() {
				a.providerAddButton(c, msgs.ProviderAddShort(), !canAdd)
			})
			ui.Column(c).FillWidth().MinWidth(0).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
				for i, account := range pool.Accounts {
					c.Key("provider-item-" + account.ProviderID)
					preset := a.accountPreset(account.ProductProviderAccount)
					status, ink := providerAccountStatus(t, msgs, account)
					prior, exists := configured[preset.ID]
					if preset.ID != "" && (!exists || prior == t.TextMuted || ink == t.Success) {
						configured[preset.ID] = ink
					}
					a.configuredProviderRow(c, t, msgs, pool, i, preset, status, ink, roles, wide, blocked)
				}
			})
		})
	}
	catalog := a.providerCatalog()
	columns := 2
	if wide {
		columns = 3
	}
	for _, section := range catalog.Sections {
		presets := catalog.SectionProviders(section.ID)
		if len(presets) == 0 {
			continue
		}
		c.Key("provider-section-" + section.ID)
		a.providerCatalogSection(c, t, msgs, pool, section, presets, configured, columns, blocked, canAdd)
	}
}

func (a *appModel) providerCatalogSection(c *ui.Context, t *ui.Theme, msgs locales.Messages, pool pi.ProductProviderList, section connection.ProviderSection, presets []connection.ProviderPreset, configured map[string]ui.Color, columns int, blocked, canAdd bool) {
	id := section.ID
	collapsed, set := a.providers.catalogFolds[id]
	collapsed = section.Collapsible && (collapsed || !set && len(pool.Accounts) > 0)
	toggle := func() {
		if a.providers.catalogFolds == nil {
			a.providers.catalogFolds = make(map[string]bool)
		}
		a.providers.catalogFolds[id] = !collapsed
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Children(func() {
		title := section.Title.Resolve(c.Locale())
		subtitle := section.Subtitle.Resolve(c.Locale())
		var trailing []func()
		if section.Collapsible {
			subtitle = strconv.Itoa(len(presets)) + "  " + subtitle
			trailing = append(trailing, func() {
				label := msgs.ProviderFoldCollapse()
				if collapsed {
					label = msgs.ProviderFoldExpand()
				}
				theme.TextAction(c, label, false).Label(label + " " + title).Expanded(!collapsed).OnClick(toggle)
			})
		}
		theme.PreferenceSectionHeaderWithSubtitle(c, t, title, subtitle, trailing...)
		if collapsed {
			c.Key("provider-section-preview")
			label := msgs.ProviderFoldSummary(len(presets))
			button := providerDirectoryButton(c, t, label, true).Expanded(false).FillWidth().Wrap().Radius(11).
				Background(t.Surface).Border(1, t.Border).Padding(10, 12).Gap(8).Justify(ui.SpaceBetween)
			button.Children(func() {
				ui.Row(c).MinWidth(0).Wrap().AlignItems(ui.Center).Gap(10).Children(func() {
					for _, preset := range presets {
						theme.BrandIcon(c, a.providerIcon(preset), 18)
					}
				})
				ui.Text(c, label).MinWidth(0).FontSize(typeAction).FontWeight(500)
			})
			button.OnClick(toggle)
			return
		}
		ui.Grid(c).FillWidth().MinWidth(0).Columns(columns).GapX(12).GapY(4).Children(func() {
			for _, preset := range presets {
				c.Key("provider-preset-" + preset.ID)
				name := preset.Name.Resolve(c.Locale())
				ink, exists := configured[preset.ID]
				button := providerDirectoryButton(c, t, name, !blocked && (canAdd || exists)).FillWidth().Padding(8, 12).Gap(10)
				button.Children(func() {
					theme.BrandIcon(c, a.providerIcon(preset), 22)
					text := ui.Text(c, name).MinWidth(0).Shrink(1).MaxLines(1).Ellipsis("…").FontSize(typeAction)
					if exists {
						providerDot(c, ink)
					} else if tag := preset.Tag.Resolve(c.Locale()); tag != "" {
						text.Grow(1)
						ui.Text(c, tag).Shrink(0).FontSize(typeMeta).TextColor(t.TextMuted)
					}
				})
				appTooltip(c, button, name)
				a.restoreProviderFocus(button, "preset-"+preset.ID)
				button.OnClick(func() {
					button.Focus()
					a.providers.openerID = "preset-" + preset.ID
					a.openProviderPreset(pool, preset.ID)
				})
			}
		})
	})
}

func providerDirectoryButton(c *ui.Context, t *ui.Theme, label string, available bool) ui.Element {
	button := ui.ButtonBase(c).Label(label).Disabled(!available).MinWidth(0).MinHeight(theme.ControlHit).
		Radius(10).Border(1, ui.Transparent).AlignItems(ui.Center).Justify(ui.Start).
		FocusRingColor(t.Accent).Transition(theme.ControlTransition)
	if button.Pressed() {
		button.Background(t.SurfacePressed).BorderColor(t.Border)
	} else if button.Hovered() {
		button.Background(t.Surface).BorderColor(t.Border)
	}
	return button
}

func providerDot(c *ui.Context, color ui.Color) {
	ui.Box(c).Size(6, 6).Radius(3).Shrink(0).Background(color)
}

func (a *appModel) providerRoute(c *ui.Context, t *ui.Theme, msgs locales.Messages, route *pi.ProductModelRoute, accounts []pi.ProductProviderAccountView) string {
	name := route.ProviderName
	var preset connection.ProviderPreset
	for _, account := range accounts {
		if account.ProviderID == route.ProviderID {
			preset = a.accountPreset(account.ProductProviderAccount)
			if name == "" {
				name = account.Name
			}
			break
		}
	}
	if name == "" {
		name = msgs.ProviderPendingRemoval()
	} else if preset.ID == "" {
		preset, _ = a.providerCatalog().MatchAccount("", name)
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
	ui.Row(c).FillWidth().MinWidth(0).Gap(8).AlignItems(ui.Center).Justify(ui.End).Children(func() {
		theme.BrandIcon(c, a.providerIcon(preset), 18)
		text := ui.Text(c, label).MinWidth(0).MaxLines(1).Ellipsis("…").FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction)
		appTooltip(c, text, label+"\n"+thinking)
	})
	return thinking
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

func (a *appModel) configuredProviderRow(c *ui.Context, t *ui.Theme, msgs locales.Messages, pool pi.ProductProviderList, index int, preset connection.ProviderPreset, status string, ink ui.Color, roles [4]providerRoleRow, wide, blocked bool) {
	account := pool.Accounts[index]
	count := len(providerModelIDs(account.ProductProviderAccount))
	if account.EnabledModels != nil {
		count = len(*account.EnabledModels)
	}
	meta := msgs.ProviderEnabledModelsCount(count)
	if assigned := providerRoleNames(msgs, account.ProviderID, roles); assigned != "" {
		meta += " · " + assigned
	}
	ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(4).Padding(0, 8, 0, 0).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		c.Key("edit-provider")
		item := providerDirectoryButton(c, t, account.Name, !blocked).Grow(1).MinHeight(48).
			Radius(theme.ControlRadius).Padding(8, 12).Gap(10)
		if item.Hovered() && !item.Pressed() {
			item.Background(t.SurfaceHover).BorderColor(ui.Transparent)
		}
		item.Children(func() {
			ui.Text(c, "#"+strconv.Itoa(index+1)).MinWidth(24).Shrink(0).FontSize(typeMeta).TextColor(t.TextMuted)
			theme.BrandIcon(c, a.providerIcon(preset), 22)
			body := ui.Row(c).MinWidth(0).Grow(1).AlignItems(ui.Center).Gap(8)
			if !wide {
				body.Column().AlignItems(ui.Start).Gap(2)
			}
			body.Children(func() {
				ui.Row(c).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, account.Name).MinWidth(0).MaxLines(1).Ellipsis("…").FontSize(typeAction).FontWeight(500)
					providerDot(c, ink)
				})
				ui.Text(c, meta).MinWidth(0).MaxLines(1).Ellipsis("…").FontSize(typeMeta).TextColor(t.TextMuted)
			})
			if account.FallbackOnly != nil && *account.FallbackOnly {
				capabilityBadge(c, t, msgs.ProviderFallbackTag(), t.TextMuted)
			}
		})
		appTooltip(c, item, account.Name+"\n"+status+"\n"+meta)
		a.restoreProviderFocus(item, "account-"+account.ProviderID)
		item.OnClick(func() {
			item.Focus()
			a.providers.openerID = "account-" + account.ProviderID
			a.selectProvider(account)
		})
		a.providerPriority(c, msgs, pool, index, blocked)
		ui.Icon(c, theme.GlyphArrowRightSLine).Size(16, 16).Shrink(0).TextColor(t.TextMuted)
	})
}

func (a *appModel) providerTestButton(c *ui.Context, label, accessible string, editor *providerEditor, keepKey, blocked bool) {
	secondaryButton(c, label).Label(accessible).MinHeight(32).Disabled(blocked).OnClick(func() {
		if params, ok := editor.input(keepKey); ok {
			a.testProvider(params, editor)
		}
	})
}

func (a *appModel) providerSaveActions(c *ui.Context, msgs locales.Messages, editor *providerEditor, keepKey, blocked bool) {
	ui.Row(c).MinWidth(0).Wrap().Justify(ui.End).AlignItems(ui.Center).Gap(8).Children(func() {
		theme.TextAction(c, msgs.CreateBotCancel(), false).Disabled(a.providerBusy()).OnClick(a.dismissProviderDialog)
		secondaryButton(c, msgs.ProviderSave()).Disabled(blocked).OnClick(func() {
			if params, ok := editor.input(keepKey); ok {
				a.saveProvider(editor, params, false)
			}
		})
		primaryButton(c, msgs.ProviderSaveApply()).Disabled(blocked).OnClick(func() {
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
	width, _ := c.Size()
	wide := width >= 552
	row := ui.Row(c).FillWidth().MinWidth(0).Gap(12).AlignItems(ui.Start)
	if !wide {
		row.Column().Gap(4)
	}
	row.Children(func() {
		text := ui.Text(c, label).Shrink(0).FontSize(typeAction).FixedLineHeight(lineAction).TextColor(t.TextMuted)
		if wide {
			text.Width(88).Padding(7, 0).TextAlign(ui.End)
		}
		column := ui.Column(c).Grow(1).Shrink(1).MinWidth(0).AlignItems(ui.Start).Gap(6)
		if !wide {
			column.FillWidth()
		}
		column.Children(controls)
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
	providerFormRow(c, t, msgs.ProviderBaseURL(), func() {
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			a.providerInput(c, editor, "Endpoint", msgs.ProviderBaseURL(), "https://api.example.com/v1", &editor.BaseURL, false, blocked).Basis(200).Grow(1)
			c.Key("provider-protocol")
			wires := [...]pi.WireAPI{pi.OpenAICompletions, pi.OpenAIResponses}
			selected := max(0, slices.Index(wires[:], editor.WireAPI))
			theme.PreferenceSegmentedSelection(c, &selected, msgs.ProtocolLabel(), blocked, false, msgs.ProviderProtocolChatShort(), msgs.ProtocolResponsesLabel()).OnChange(func() {
				editor.WireAPI = wires[selected]
				editor.resetModelCatalog()
			})
		})
		providerFieldError(c, t, msgs, editor.fields["Endpoint"])
	})
}

func (a *appModel) providerCredential(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, account pi.ProductProviderAccountView, keepKey, blocked bool) {
	providerFormRow(c, t, msgs.ConnectionFieldAPIKey(), func() {
		if keepKey {
			ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(8).Padding(0, 8).
				Radius(theme.ControlRadius).Border(1, t.Border).Background(t.Surface.Mix(t.Background, .5)).Children(func() {
				ui.Box(c).Size(18, 18).Shrink(0).Radius(theme.PillRadius).Background(t.Success.Mix(t.Surface, .87)).
					AlignItems(ui.Center).Justify(ui.Center).Children(func() {
					ui.Icon(c, theme.GlyphCheckLine).Size(14, 14).TextColor(t.Success)
				})
				ui.Text(c, "••••••••").AccessHidden(true).FontSize(typeAction)
				capabilityBadge(c, t, msgs.ProviderKeyInUse(), t.Success)
				ui.Spacer(c)
				theme.TextAction(c, msgs.ProviderReplaceKey(), false).Expanded(editor.replacingKey).Disabled(blocked).OnClick(func() {
					editor.replacingKey = !editor.replacingKey || editor.APIKey != ""
					editor.focusKey = editor.replacingKey
				})
				theme.TextAction(c, msgs.ProviderRevoke(), true).Label(msgs.ProviderRevokeAccount(account.Name)).Disabled(blocked).
					OnClick(func() { a.changeProviderAccount(account.ProviderID, true) })
			})
		}
		if !keepKey || editor.replacingKey || editor.APIKey != "" {
			placeholder := ""
			if keepKey {
				placeholder = msgs.ProviderKeepKey()
			} else if editor.localWithoutKey() {
				placeholder = msgs.ProviderOptionalKey()
			}
			field := a.providerInput(c, editor, "APIKey", msgs.ConnectionFieldAPIKey(), placeholder, &editor.APIKey, true, blocked)
			if editor.focusKey && !blocked {
				field.Focus().ScrollIntoView()
				editor.focusKey = false
			}
			providerFieldError(c, t, msgs, editor.fields["APIKey"])
		}
	})
}

func (a *appModel) providerModels(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, name string, keepKey, blocked bool) {
	models := providerModelIDs(editor.ProductProviderAccount)
	providerFormRow(c, t, msgs.ProviderDefaultModel(), func() {
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			a.providerInput(c, editor, "Model", msgs.ProviderDefaultModel(), "", &editor.DefaultModel, false, blocked).Basis(128).Grow(1)
			if len(models) > 1 {
				options := make([]theme.SelectOption, 0, len(models))
				for _, id := range models {
					options = append(options, theme.SelectOption{Value: id, Label: id})
				}
				c.Key("provider-default-picker")
				selected := editor.DefaultModel
				if theme.PreferenceSelect(c, &selected, options, msgs.ProviderPickDefault(), blocked).Width(144).Changed() {
					editor.DefaultModel = selected
					delete(editor.fields, "Model")
				}
			}
		})
		providerFieldError(c, t, msgs, editor.fields["Model"])
	})
	providerFormRow(c, t, msgs.ProviderModelPool(), func() {
		active := editor.activeModels(models)
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			secondaryButton(c, msgs.ProviderSelectAll()).Disabled(blocked || len(models) == 0).OnClick(func() {
				editor.setEnabledModels(slices.Clone(models[:min(len(models), 128)]))
			})
			secondaryButton(c, msgs.ProviderSelectNone()).Disabled(blocked || len(models) == 0).OnClick(func() {
				editor.setEnabledModels([]string{})
			})
			a.providerTestButton(c, msgs.ProviderTestSync(), msgs.ProviderTestAccount(name), editor, keepKey, blocked)
			if editor.probe.kind == "tested" {
				editor.probe.render(c, t)
			}
		})
		if editor.probe.kind != "tested" {
			editor.probe.render(c, t)
		}
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).Children(func() {
			for _, id := range models {
				c.Key("provider-model-" + id)
				model := editor.discoveredModel(id)
				label := id
				if id == editor.DefaultModel {
					label = "★ " + id
				}
				enabled := slices.Contains(active, id)
				pill := theme.TagPill(c, label, providerTokenBadge(model.ContextTokens), enabled, true, blocked || !enabled && len(active) >= 128).
					Label(msgs.ProviderToggleModel(id))
				appTooltip(c, pill, providerModelHints(msgs, model))
				pill.OnClick(func() {
					next := slices.Clone(active)
					if index := slices.Index(next, id); index >= 0 {
						next = slices.Delete(next, index, index+1)
					} else {
						next = append(next, id)
					}
					editor.setEnabledModels(next)
				})
			}
		})
		ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
			a.providerInput(c, editor, "ModelInput", msgs.ProviderAddModelPlaceholder(), msgs.ProviderAddModelPlaceholder(), &editor.modelInput, false, blocked).
				Basis(160).Grow(1)
			id := strings.TrimSpace(editor.modelInput)
			secondaryButton(c, msgs.ProviderAddModel()).Disabled(blocked || id == "" || slices.Contains(models, id)).OnClick(func() {
				if len(editor.Models) >= 128 || utf16Length(id) > 256 {
					if editor.fields == nil {
						editor.fields = make(map[string]string)
					}
					editor.fields["ModelInput"] = "model-limit"
					return
				}
				editor.Models = append(editor.Models, id)
				if editor.EnabledModels != nil && len(active) < 128 {
					editor.setEnabledModels(append(slices.Clone(active), id))
				}
				editor.modelInput = ""
				delete(editor.fields, "ModelInput")
			})
		})
		providerFieldError(c, t, msgs, editor.fields["ModelInput"])
	})
}

func (e *providerEditor) activeModels(models []string) []string {
	if e.EnabledModels != nil {
		return *e.EnabledModels
	}
	var active []string
	for _, id := range models {
		model := e.discoveredModel(id)
		if model.Recommended || id == e.DefaultModel && !model.PriceProtected && (model.IsChat == nil || *model.IsChat) {
			active = append(active, id)
		}
	}
	return active
}

func (e *providerEditor) setEnabledModels(models []string) {
	e.EnabledModels, e.modelsChanged = &models, true
}

func providerTokenBadge(tokens int) string {
	switch {
	case tokens >= 1024*1024:
		return fmt.Sprintf("%.1fM", float64(tokens)/(1024*1024))
	case tokens >= 1024:
		return strconv.Itoa(tokens/1024) + "K"
	case tokens > 0:
		return strconv.Itoa(tokens)
	default:
		return ""
	}
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
	if !a.providers.adding || !a.settingsOpen || a.settingsTab != settingsTabConnection {
		return
	}
	t := theme.SettingsTheme(base)
	msgs := locales.In(c.Locale())
	editor := a.providers.editor
	pool := a.providerPool()
	var account pi.ProductProviderAccountView
	for _, row := range pool.Accounts {
		if row.ProviderID == editor.ProviderID {
			account = row
			break
		}
	}
	saved := account.ProviderID != ""
	keepKey := saved && account.CredentialStatus == "ok" && account.BaseURL == editor.BaseURL && account.WireAPI == editor.WireAPI
	blocked := a.providerChangesBlocked()
	preset, _ := a.providerCatalog().PresetByID(editor.presetID)
	if preset.ID == "" && (saved || editor.preset < 0) {
		preset = a.accountPreset(editor.ProductProviderAccount)
	}
	name := editor.Name
	if editor == a.providers.firstDrafts[""] {
		name = msgs.ProviderPrevious()
	} else if name == "" {
		name = msgs.ProviderAdd()
	}
	width, height := c.Size()
	open := true
	c.Key("provider-config-dialog")
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		theme.StyleFrostedModalBackdrop(backdrop, &t)
		panel.Label(name).Width(616).MaxWidth(width-32).MaxHeight(height-48).Column().Clip().
			Background(t.Surface).Border(1, t.Border).Radius(theme.ModalRadius).Shadow(0, 8, 28, 0, ui.RGBA(0, 0, 0, .24))
		ui.Themed(c, &t).FillWidth().MinHeight(0).Shrink(1).Column().Children(func() {
			a.providerDialogHeader(c, &t, msgs, editor, account, preset, name, blocked)
			c.Key("provider-dialog-scroll-" + editor.ProviderID)
			ui.Scroll(c).FillWidth().MinWidth(0).MinHeight(0).Shrink(1).TrackScroll(&a.providers.detailScroll).Children(func() {
				ui.Column(c).FillWidth().MinWidth(0).Gap(12).Padding(16, 22).Children(func() {
					a.providerField(c, &t, msgs, editor, "Name", msgs.FieldName(), "", &editor.Name, false, blocked)
					a.providerEndpoint(c, &t, msgs, editor, blocked)
					a.providerCredential(c, &t, msgs, editor, account, keepKey, blocked)
					a.providerModels(c, &t, msgs, editor, name, keepKey, blocked)
					a.providerAdvanced(c, &t, msgs, editor, blocked)
					a.providerHealth(c, &t, msgs, account, blocked)
				})
			})
			ui.Column(c).FillWidth().Shrink(0).Gap(6).Padding(13, 22).Background(t.Surface.Mix(t.Background, .35)).
				BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
				providerFormStatus(c, &t, msgs, editor, a.providerBusy())
				a.providers.failure.render(c, &t)
				ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Justify(ui.End).Gap(8).Children(func() {
					if saved {
						ui.Row(c).Grow(1).MinWidth(0).Wrap().Gap(4).AlignItems(ui.Center).Children(func() {
							c.Key("delete-provider")
							theme.TextAction(c, msgs.ProviderDelete(), true).Label(msgs.ProviderDeleteAccount(account.Name)).Disabled(blocked).
								OnClick(func() { a.changeProviderAccount(account.ProviderID, false) })
							c.Key("add-another-provider")
							label := preset.Name.Resolve(c.Locale())
							if label == "" {
								label = account.Name
							}
							action := msgs.ProviderAddAnother(label)
							theme.TextAction(c, action, false).Disabled(blocked || len(pool.Accounts) >= 64).
								OnClick(func() { a.addAnotherProvider(account.ProductProviderAccount, preset, c.Locale()) })
						})
					}
					a.providerSaveActions(c, msgs, editor, keepKey, blocked)
				})
			})
		})
	})
	if !open && !a.providerBusy() {
		a.dismissProviderDialog()
	}
}

func (a *appModel) providerDialogHeader(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, account pi.ProductProviderAccountView, preset connection.ProviderPreset, name string, blocked bool) {
	status, ink := providerAccountStatus(t, msgs, account)
	if account.ProviderID == "" {
		status, ink = msgs.ProviderNotSaved(), t.TextMuted
	} else if !editor.Enabled {
		status, ink = msgs.ProviderDisabled(), t.TextMuted
	} else if editor.tested && status == msgs.ProviderReady() {
		status = msgs.ProviderLatencyReady(strconv.FormatUint(editor.latency, 10))
	}
	ui.Row(c).FillWidth().MinWidth(0).Shrink(0).Wrap().AlignItems(ui.Center).Gap(8).Padding(16, 22).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).Basis(176).Grow(1).MinWidth(0).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
			theme.BrandIcon(c, a.providerIcon(preset), 24)
			text := ui.Text(c, name).MinWidth(0).Role(ui.RoleHeading).Level(2).FontSize(16).FontWeight(600).MaxLines(1).Ellipsis("…")
			appTooltip(c, text, name)
			capabilityBadge(c, t, status, ink)
		})
		if site, err := url.Parse(preset.Website); err == nil && site.Host != "" && site.User == nil && (site.Scheme == "http" || site.Scheme == "https") {
			link := linkButton(c, site.Host+" ↗").Role(ui.RoleLink).MaxWidth(186)
			appTooltip(c, link, preset.Website)
			link.OnClick(func() { c.OpenURL(preset.Website) })
		}
		c.Key("provider-enabled")
		theme.ToggleSwitch(c, &editor.Enabled, msgs.ProviderAccountStatus(), blocked)
		c.Key("provider-dialog-close")
		closeButton := iconControlFace(c, ui.ButtonBase(c).Label(msgs.ComposerMaterialClose()), iconChrome, false).
			Size(32, 32).Shrink(0).Disabled(a.providerBusy()).OnClick(a.dismissProviderDialog)
		closeButton.Children(func() { controlIcon(c, glyphCloseLine, iconChrome) })
		appTooltip(c, closeButton, msgs.ComposerMaterialClose())
	})
}

func (a *appModel) providerAdvanced(c *ui.Context, t *ui.Theme, msgs locales.Messages, editor *providerEditor, blocked bool) {
	advancedError := editor.fields["ContextTokens"] != "" || editor.fields["OutputTokens"] != "" || editor.fields["Models"] != ""
	ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
		a.providerFallback(c, msgs, editor, blocked)
		context, _ := strconv.Atoi(editor.context)
		output, _ := strconv.Atoi(editor.output)
		label := msgs.ProviderAdvanced()
		if context > 0 && output > 0 {
			label += " · " + providerTokenBadge(context) + " / " + providerTokenBadge(output)
		}
		arrow := " ▾"
		if !editor.advanced {
			arrow = " ▸"
		}
		theme.TextAction(c, label+arrow, false).Label(msgs.ProviderAdvanced()).Expanded(editor.advanced).Disabled(blocked).OnClick(func() {
			editor.advanced = !editor.advanced || advancedError
		})
	})
	if editor.advanced {
		a.providerAdditionalModels(c, t, msgs, editor, blocked)
		a.providerTokenFields(c, t, msgs, editor, blocked)
	}
}
