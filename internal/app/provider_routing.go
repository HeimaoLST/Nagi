package app

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

const sendErrorRouteUnavailable = "route-unavailable"

func providerAdmission(receipt pi.ProductReceipt, remote *pi.RemoteError) pi.ProductRouteAdmission {
	var admission pi.ProductRouteAdmission
	_ = json.Unmarshal(receipt.Result, &admission)
	if admission.Code == "" && remote != nil {
		admission.Code = pi.ProductDeliveryBlocker(remote.Code)
	}
	return admission
}

func providerDeliveryBlocked(code pi.ProductDeliveryBlocker) bool {
	return code == pi.ProductCredentialUnavailable || code == pi.ProductRouteUnavailable
}

func (ct *contact) providerSendBlocked() bool {
	return ct.SendErrorKind == sendErrorCredentialUnavailable || ct.SendErrorKind == sendErrorRouteUnavailable
}

func (ct *contact) applyProviderSendRejection(admission pi.ProductRouteAdmission) bool {
	switch admission.Code {
	case pi.ProductCredentialUnavailable:
		ct.setSendError("The Provider credential is unavailable.", sendErrorCredentialUnavailable, "")
	case pi.ProductRouteUnavailable:
		ct.setSendError("The Provider route is unavailable.", sendErrorRouteUnavailable, admission.RetryAt)
	default:
		return false
	}
	return true
}

func providerRouteRejectionText(c *ui.Context, msgs locales.Messages, retryAt string) string {
	if retry := providerRetryLabel(c, msgs, retryAt); retry != "" {
		return msgs.ProviderRouteCoolingDown(retry)
	}
	return msgs.ProviderRouteUnavailable()
}

func (a *appModel) scheduleProviderHealthRefresh(pool pi.ProductProviderList) {
	if timer := a.providers.healthTimer; timer != nil {
		timer.Stop()
		a.providers.healthTimer = nil
	}
	now := time.Now()
	var next time.Time
	consider := func(stamp string) {
		if retry, err := time.Parse(time.RFC3339Nano, stamp); err == nil && retry.After(now) && (next.IsZero() || retry.Before(next)) {
			next = retry
		}
	}
	for _, role := range pool.RoleStatus {
		consider(role.RetryAt)
	}
	for _, account := range pool.Accounts {
		for _, health := range account.RouteHealth {
			consider(health.RetryAt)
		}
	}
	if next.IsZero() {
		return
	}
	host := a.productHost()
	var timer *time.Timer
	timer = time.AfterFunc(max(time.Until(next), time.Second), func() {
		a.updateUI(func() {
			if a.providers.healthTimer == timer {
				a.providers.healthTimer = nil
				a.refreshProductProviders(host)
			}
		})
	})
	a.providers.healthTimer = timer
}

func providerModelIDs(account pi.ProductProviderAccount) []string {
	ids := make([]string, 0, 1+len(account.Models))
	if account.DefaultModel != "" {
		ids = append(ids, account.DefaultModel)
	}
	for _, id := range account.Models {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if account.EnabledModels != nil {
		for _, id := range *account.EnabledModels {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func (e *providerEditor) discoveredModel(id string) pi.ProductDiscoveredModel {
	for _, model := range e.discovered {
		if model.ID == id {
			return model
		}
	}
	return pi.ProductDiscoveredModel{}
}

func providerModelHints(msgs locales.Messages, model pi.ProductDiscoveredModel) string {
	hints := make([]string, 0, 6)
	switch {
	case model.IsChat == nil:
		hints = append(hints, msgs.ProviderChatUnknown())
	case !*model.IsChat:
		hints = append(hints, msgs.ProviderNotChat())
	}
	switch {
	case model.ToolCall == nil:
		hints = append(hints, msgs.ProviderToolsUnknown())
	case *model.ToolCall:
		hints = append(hints, msgs.ProviderTools())
	default:
		hints = append(hints, msgs.ProviderNoTools())
	}
	if model.Reasoning {
		hints = append(hints, msgs.ProviderReasoningCapability())
	}
	if model.Recommended {
		hints = append(hints, msgs.ProviderRecommended())
	}
	if model.PriceProtected {
		hints = append(hints, msgs.ProviderPriceProtected())
	}
	if model.ContextTokens > 0 && model.OutputTokens > 0 {
		hints = append(hints, msgs.ProviderModelLimits(model.ContextTokens, model.OutputTokens))
	}
	return strings.Join(hints, " · ")
}

func (a *appModel) providerPriority(c *ui.Context, msgs locales.Messages, pool pi.ProductProviderList, index int, blocked bool) {
	if len(pool.Accounts) < 2 || pool.SavedRevision == nil {
		return
	}
	c.Key("provider-order")
	ui.Row(c).Gap(4).Children(func() {
		for _, move := range [...]struct {
			delta       int
			arrow, name string
		}{{-1, "↑", msgs.ProviderMoveUp()}, {1, "↓", msgs.ProviderMoveDown()}} {
			c.Key(move.arrow)
			target := index + move.delta
			button := secondaryButton(c, move.arrow).Label(move.name).Size(32, 32).Padding(4, 8).
				Disabled(blocked || target < 0 || target >= len(pool.Accounts))
			appTooltip(c, button, move.name)
			button.OnClick(func() {
				ids := make([]string, len(pool.Accounts))
				for i, account := range pool.Accounts {
					ids[i] = account.ProviderID
				}
				ids[index], ids[target] = ids[target], ids[index]
				a.runProviderRPC(false, func(ctx context.Context, client *pi.ProductClient) error {
					_, err := client.ReorderProviders(ctx, pi.ProductProviderReorderParams{
						ProviderIDs: ids, SavedRevision: *pool.SavedRevision, OperationID: newContactID(),
					})
					return err
				}, func(err error) { a.providers.failure = a.providerFailure("reorder", err) })
			})
		}
	})
}

func (a *appModel) providerFallback(c *ui.Context, msgs locales.Messages, editor *providerEditor, blocked bool) {
	fallback := editor.FallbackOnly != nil && *editor.FallbackOnly
	control := ui.Checkbox(c, &fallback, msgs.ProviderUseAsBackup()).MinHeight(theme.ControlHit).Disabled(blocked).OnChange(func() {
		editor.FallbackOnly, editor.fallbackChanged = &fallback, true
	})
	appTooltip(c, control, msgs.ProviderFallbackHint())
}

func providerRetryLabel(c *ui.Context, msgs locales.Messages, stamp string) string {
	retryAt, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return ""
	}
	return msgs.ProviderRetryAt(i18n.FormatDate(c.Locale(), retryAt.Local(), i18n.DateOptions{Time: i18n.MediumStyle}))
}

func providerHealthState(msgs locales.Messages, state string) string {
	switch state {
	case "degraded":
		return msgs.ProviderDegraded()
	case "cooldown":
		return msgs.ProviderCooldown()
	case "half_open":
		return msgs.ProviderHalfOpen()
	case "auth_failed":
		return msgs.ProviderKeyRejected()
	default:
		return msgs.ProviderUnavailable()
	}
}

func providerUnavailableReason(msgs locales.Messages, reason string) string {
	switch reason {
	case "provider_deleted":
		return msgs.ProviderPinDeleted()
	case "provider_disabled":
		return msgs.ProviderPinDisabled()
	case "endpoint_changed":
		return msgs.ProviderPinRepointed()
	case "model_disabled":
		return msgs.ProviderPinModelDisabled()
	case "credential_missing":
		return msgs.ProviderMissingKey()
	case "auth_failed":
		return msgs.ProviderKeyRejected()
	case "incompatible":
		return msgs.ProviderModelIncompatible()
	case "empty_pool":
		return msgs.ProviderEmptyPool()
	case "rate_limited":
		return msgs.ProviderRateLimited()
	case "provider_failure":
		return msgs.ProviderServiceFailure()
	case "network":
		return msgs.ProviderNetworkFailure()
	case "stream_error":
		return msgs.ProviderStreamFailure()
	default:
		return ""
	}
}

func (a *appModel) providerHealth(c *ui.Context, t *ui.Theme, msgs locales.Messages, account pi.ProductProviderAccountView, blocked bool) {
	if len(account.RouteHealth) == 0 && account.HealthSummary != "degraded" && account.HealthSummary != "cooldown" && account.HealthSummary != "half_open" {
		return
	}
	c.Key("provider-health")
	ui.Column(c).FillWidth().MinWidth(0).Gap(6).Padding(8, 0, 0).Children(func() {
		ui.Row(c).FillWidth().Wrap().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, msgs.ProviderRouteHealth()).FontSize(typeLabel).FontWeight(500).FixedLineHeight(lineLabel)
			secondaryButton(c, msgs.CapRefreshStatus()).Disabled(blocked).OnClick(func() {
				a.refreshProductProviders(a.productHost())
			})
		})
		for i, entry := range account.RouteHealth {
			c.Key(strconv.Itoa(i))
			ui.Column(c).FillWidth().MinWidth(0).Gap(4).Padding(8, 0).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
				name := entry.WireModelID
				if name == "" {
					name = msgs.ConnectionFieldEndpoint()
				}
				ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
					text := ui.Text(c, name).MinWidth(0).MaxLines(1).Ellipsis("…").FontSize(typeAction)
					appTooltip(c, text, name)
					capabilityBadge(c, t, providerHealthState(msgs, entry.State), t.Warning)
				})
				notes := []string{providerUnavailableReason(msgs, entry.Reason)}
				if entry.ConsecutiveFailures > 0 {
					notes = append(notes, msgs.ProviderFailureCount(entry.ConsecutiveFailures))
				}
				if retry := providerRetryLabel(c, msgs, entry.RetryAt); retry != "" {
					notes = append(notes, retry)
				}
				ui.Text(c, strings.Join(notes, " · ")).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			})
		}
	})
}

type providerPinOption struct {
	theme.SelectOption
	pin pi.ProductRoutingOverride
}

func (a *appModel) providerPinOptions(msgs locales.Messages, pool pi.ProductProviderList, roles [4]providerRoleRow) []providerPinOption {
	options := []providerPinOption{{SelectOption: theme.SelectOption{Value: "auto", Label: msgs.ProviderFollowStrategy()}}}
	canonicalIDs := make(map[string]bool)
	for _, account := range pool.Accounts {
		if !account.Enabled {
			continue
		}
		for _, id := range providerModelIDs(account.ProductProviderAccount) {
			var model pi.ProductDiscoveredModel
			if draft := a.providers.drafts[account.ProviderID]; draft != nil && draft.BaseURL == account.BaseURL && draft.WireAPI == account.WireAPI {
				model = draft.discoveredModel(id)
			}
			if account.EnabledModels != nil && !slices.Contains(*account.EnabledModels, id) || model.IsChat != nil && !*model.IsChat ||
				account.EnabledModels == nil && (model.PriceProtected || model.ID != "" && id != account.DefaultModel && !model.Recommended) {
				continue
			}
			canonical := model.CanonicalID
			for _, role := range roles {
				if route := role.route; route != nil && route.ProviderID == account.ProviderID && route.ResolvedModel == id {
					canonical = route.CanonicalModelID
					break
				}
			}
			if endpoint, err := url.Parse(account.BaseURL); canonical == "" && err == nil && endpoint.Scheme == "http" {
				canonical = "custom:" + strconv.Itoa(len(account.ProviderID)) + ":" + account.ProviderID + ":" + id
			}
			if canonical != "" && !canonicalIDs[canonical] {
				canonicalIDs[canonical] = true
				label := canonical
				if strings.HasPrefix(canonical, "custom:") {
					label = id + " · " + account.Name
				}
				options = append(options, providerPinOption{theme.SelectOption{Value: "model:" + canonical, Label: msgs.ProviderModelPinOption(label)},
					pi.ProductRoutingOverride{Mode: "model", CanonicalModelID: canonical}})
			}
			options = append(options, providerPinOption{theme.SelectOption{Value: "route:" + account.ProviderID + "\x00" + id, Label: account.Name + " · " + id},
				pi.ProductRoutingOverride{Mode: "route", ProviderID: account.ProviderID, Model: id}})
		}
	}
	return options
}

func (a *appModel) providerPinPicker(c *ui.Context, t *ui.Theme, msgs locales.Messages, pool pi.ProductProviderList, role providerRoleRow, options []providerPinOption) {
	pin := pool.Overrides[role.id]
	applied := pool.EffectiveOverrides[role.id]
	pending := pin != applied
	pin.EndpointFingerprint = ""
	selected := "auto"
	choices := make([]theme.SelectOption, 0, len(options)+1)
	for _, option := range options {
		choices = append(choices, option.SelectOption)
		if option.pin == pin {
			selected = option.Value
		}
	}
	status := pool.RoleStatus[role.id]
	dangling := !pending && status.Status == "pin_unavailable"
	if pin.Mode != "" && (selected == "auto" || dangling) {
		label := msgs.ProviderPinnedUnavailable()
		if pin.Mode == "route" {
			label += " · " + pin.Model
		}
		selected = "unavailable"
		choices = append(choices, theme.SelectOption{Value: selected, Label: label})
	}
	c.Key("provider-pin")
	if theme.PreferenceSelect(c, &selected, choices, msgs.ProviderPinRole(role.name), a.providerChangesBlocked() || pool.SavedRevision == nil).FillWidth().MaxWidth(264).Changed() {
		if index := slices.IndexFunc(options, func(option providerPinOption) bool { return option.Value == selected }); index >= 0 {
			var change *pi.ProductRoutingOverride
			if value := options[index].pin; value.Mode != "" {
				change = &value
			}
			a.runProviderRPC(false, func(ctx context.Context, client *pi.ProductClient) error {
				_, err := client.SetRoutingOverrides(ctx, pi.ProductRoutingOverridesParams{SavedRevision: *pool.SavedRevision,
					Overrides: pi.ProductRoutingOverridesPatch{role.id: change}, OperationID: newContactID()})
				return err
			}, func(err error) { a.providers.failure = a.providerFailure("override", err) })
		}
	}
	if pending {
		capabilityBadge(c, t, msgs.ProviderPending(), t.Warning)
	}
}

func providerRoleStatus(c *ui.Context, t *ui.Theme, msgs locales.Messages, status pi.ProductRoleStatus, route *pi.ProductModelRoute, thinking string) {
	ink, label := t.Warning, msgs.ProviderUnavailable()
	if route != nil || status.Status == "ready" {
		ink, label = t.Success, msgs.ProviderReady()
	} else if status.Status == "cooldown" {
		label = msgs.ProviderCooldown()
	} else if status.Status == "" {
		ink, label = t.TextMuted, msgs.ProviderUnassigned()
	}
	source, failover := status.Mode, status.Failover
	if route != nil {
		if route.Source != "" {
			source = route.Source
		}
		if route.Failover != "" {
			failover = route.Failover
		}
	}
	attribution := msgs.ProviderAuto()
	switch source {
	case "pinned_model", "model":
		attribution = msgs.ProviderPinnedModel()
	case "pinned_route", "route":
		attribution = msgs.ProviderPinnedRoute()
	}
	ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Justify(ui.End).Gap(6).Children(func() {
		capabilityBadge(c, t, attribution, t.TextMuted)
		if failover == "same_model_cooldown" {
			capabilityBadge(c, t, msgs.ProviderSameModelFailover(), t.Warning)
		} else if failover == "model_cooldown" {
			capabilityBadge(c, t, msgs.ProviderFallbackModel(), t.Warning)
		}
		capabilityBadge(c, t, label, ink)
		if thinking != "" {
			ui.Text(c, thinking).MinWidth(0).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
	})
	if note := providerUnavailableReason(msgs, status.UnavailableReason); note != "" {
		ui.Text(c, note).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(ink)
	}
	if retry := providerRetryLabel(c, msgs, status.RetryAt); retry != "" {
		ui.Text(c, retry).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(ink)
	}
}
