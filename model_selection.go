package main

// Standalone session-model view. Runtime catalog/config DTOs stay in
// runtime.go; this file only holds the editable UI state for one target.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

type modelSelectionTarget struct {
	Kind   string
	Label  string
	Config runtimeConnectionConfig
}

type modelSelectionDraft struct {
	Model         string
	CatalogID     string
	CapacityMode  string
	ContextTokens string
	OutputTokens  string
}

type modelSelectionRequest struct {
	Target modelSelectionTarget
	Patch  runtimeModelPatch
}

type modelSelectionActions struct {
	Refresh func(modelSelectionTarget)
	Update  func(modelSelectionRequest)
	Cancel  func()
}

type modelSelection struct {
	Target            modelSelectionTarget
	Effective         modelSelectionTarget
	Catalog           runtimeModelCatalog
	Draft             modelSelectionDraft
	Filter            string
	Unconfirmed       bool
	Busy              bool
	Error             string
	ErrorDetail       string
	Status            string
	UnconfirmedKind   string
	UnconfirmedDetail string
	focusField        string
}

const (
	modelCapacityCatalog  = "catalog"
	modelCapacityExplicit = "explicit"
)

func newModelSelection(target modelSelectionTarget, catalog runtimeModelCatalog) *modelSelection {
	s := &modelSelection{Target: target, Effective: target, Catalog: catalog, focusField: "Model"}
	s.ResetDraft()
	return s
}

func (s *modelSelection) effectiveCompaction() runtimeCompaction {
	return s.Effective.Config.EffectiveConfig.Compaction
}

func (s *modelSelection) effectiveCatalogID() string {
	effective := s.Effective.Config.EffectiveConfig
	if effective.CapacitySource.Kind != "catalog" {
		return ""
	}
	return effective.CapacitySource.CatalogID
}

func (s *modelSelection) ResetDraft() {
	compaction := s.effectiveCompaction()
	s.Draft = modelSelectionDraft{
		Model:         s.Effective.Config.EffectiveConfig.Model,
		CapacityMode:  modelCapacityExplicit,
		ContextTokens: strconv.Itoa(compaction.ContextTokens),
		OutputTokens:  strconv.Itoa(compaction.OutputTokens),
	}
	if catalogID := s.effectiveCatalogID(); catalogID != "" {
		s.Draft.CatalogID = catalogID
		s.Draft.CapacityMode = modelCapacityCatalog
	}
	s.Error = ""
	s.ErrorDetail = ""
	s.Status = ""
	s.focusField = "Model"
}

func (s *modelSelection) SetTarget(target modelSelectionTarget) {
	wasDirty := s.dirty()
	changedTarget := s.Target.Config.SessionID != target.Config.SessionID || s.Target.Kind != target.Kind
	s.Target = target
	s.Effective = target
	if changedTarget {
		s.Filter = ""
		s.Unconfirmed = false
		s.UnconfirmedKind = ""
		s.UnconfirmedDetail = ""
		s.Error = ""
		s.ErrorDetail = ""
		s.Status = ""
	}
	if changedTarget || !wasDirty {
		s.ResetDraft()
	}
}

func (s *modelSelection) SetCatalog(catalog runtimeModelCatalog) {
	s.Catalog = catalog
}

// SetEffective applies a readback snapshot without claiming a successful
// update. A dirty draft is retained so an uncertain update can be corrected.
func (s *modelSelection) SetEffective(config runtimeConnectionConfig) {
	wasDirty := s.dirty()
	changedTarget := s.Target.Config.SessionID != config.SessionID
	s.Effective.Config = config
	s.Target.Config = config
	if changedTarget || !wasDirty {
		s.ResetDraft()
	}
	s.Unconfirmed = false
	s.UnconfirmedKind = ""
	s.UnconfirmedDetail = ""
	s.Error = ""
	s.ErrorDetail = ""
	s.Status = modelStatusReadback
}

// SetResult applies a confirmed config update and discards the draft.
func (s *modelSelection) SetResult(config runtimeConnectionConfig) {
	s.Target.Config = config
	s.Effective.Config = config
	s.Busy = false
	s.Unconfirmed = false
	s.UnconfirmedKind = ""
	s.UnconfirmedDetail = ""
	s.ResetDraft()
	s.Status = modelStatusApplied
}

func (s *modelSelection) SetUnconfirmed(kind, detail string) {
	s.Unconfirmed = true
	s.Busy = false
	s.Error = ""
	s.ErrorDetail = ""
	s.Status = ""
	s.UnconfirmedKind = kind
	s.UnconfirmedDetail = detail
}

func (s *modelSelection) SetBusy(busy bool, status string) {
	s.Busy = busy
	s.Status = status
	if busy {
		s.Error = ""
		s.ErrorDetail = ""
	}
	if !busy {
		s.focusField = "Model"
	}
}

func (s *modelSelection) SetError(message string) {
	s.Busy = false
	s.Error = message
	s.ErrorDetail = ""
}

func (s *modelSelection) SetErrorKind(kind, detail string) {
	s.Busy = false
	s.Error = kind
	s.ErrorDetail = detail
}

func (s *modelSelection) SetStatus(message string) {
	s.Status = message
	s.Error = ""
	s.ErrorDetail = ""
}

func (s *modelSelection) dirty() bool {
	compaction := s.effectiveCompaction()
	if s.Draft.Model != s.Effective.Config.EffectiveConfig.Model {
		return true
	}
	if s.Draft.CapacityMode != modelCapacityExplicit {
		return s.Draft.CatalogID != s.effectiveCatalogID()
	}
	return s.Draft.ContextTokens != strconv.Itoa(compaction.ContextTokens) ||
		s.Draft.OutputTokens != strconv.Itoa(compaction.OutputTokens)
}

func (s *modelSelection) SelectCatalog(index int) {
	if index < 0 || index >= len(s.Catalog.Models) {
		return
	}
	entry := s.Catalog.Models[index]
	s.Draft.CatalogID = entry.CatalogID
	s.Draft.CapacityMode = modelCapacityCatalog
	s.Draft.ContextTokens = strconv.Itoa(entry.Limits.Context)
	if entry.Limits.Output != nil {
		s.Draft.OutputTokens = strconv.Itoa(*entry.Limits.Output)
	}
	s.Error = ""
}

func (s *modelSelection) SelectConfigured(index int) {
	if index < 0 || index >= len(s.Catalog.ConfiguredModels) {
		return
	}
	entry := s.Catalog.ConfiguredModels[index]
	s.Draft.Model = entry.Model
	s.Draft.CatalogID = ""
	s.Draft.CapacityMode = modelCapacityExplicit
	s.Draft.ContextTokens = strconv.Itoa(entry.Compaction.ContextTokens)
	s.Draft.OutputTokens = strconv.Itoa(entry.Compaction.OutputTokens)
	s.Error = ""
	s.focusField = ""
}

func (s *modelSelection) SelectCapacityMode(mode string) {
	if mode != modelCapacityCatalog && mode != modelCapacityExplicit {
		return
	}
	s.Draft.CapacityMode = mode
	if mode == modelCapacityExplicit {
		s.Draft.CatalogID = ""
	}
	s.Error = ""
}

func (s *modelSelection) Request() (modelSelectionRequest, bool) {
	if s.Unconfirmed {
		s.Error = modelValidationUnconfirmedKind
		return modelSelectionRequest{}, false
	}
	modelID := strings.TrimSpace(s.Draft.Model)
	if modelID == "" {
		s.Error = modelValidationModelKind
		s.focusField = "Model"
		return modelSelectionRequest{}, false
	}
	if s.Target.Config.SessionID == "" {
		s.Error = modelValidationSessionKind
		return modelSelectionRequest{}, false
	}
	request := modelSelectionRequest{
		Target: s.Target,
		Patch:  runtimeModelPatch{Model: modelID},
	}
	switch s.Draft.CapacityMode {
	case modelCapacityCatalog:
		catalogID := strings.TrimSpace(s.Draft.CatalogID)
		if catalogID == "" {
			s.Error = modelValidationCatalogKind
			return modelSelectionRequest{}, false
		}
		request.Patch.CatalogID = catalogID
	default:
		contextTokens, err := positiveInt(s.Draft.ContextTokens)
		if err != nil {
			s.Error = modelValidationContextKind
			return modelSelectionRequest{}, false
		}
		outputTokens, err := positiveInt(s.Draft.OutputTokens)
		if err != nil {
			s.Error = modelValidationOutputKind
			return modelSelectionRequest{}, false
		}
		if contextTokens <= outputTokens {
			s.Error = modelValidationGreaterKind
			return modelSelectionRequest{}, false
		}
		compaction := s.effectiveCompaction()
		compaction.ContextTokens = contextTokens
		compaction.OutputTokens = outputTokens
		request.Patch.Compaction = &compaction
	}
	s.Error = ""
	return request, true
}

func positiveInt(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("not a positive integer")
	}
	return parsed, nil
}

const maxVisibleModelChoices = 6

func (s *modelSelection) matchesFilter(text string) bool {
	query := strings.ToLower(strings.TrimSpace(s.Filter))
	return query == "" || strings.Contains(strings.ToLower(text), query)
}

func (s *modelSelection) visibleConfiguredModels() []runtimeConfiguredModel {
	visible := make([]runtimeConfiguredModel, 0, maxVisibleModelChoices)
	for _, entry := range s.Catalog.ConfiguredModels {
		if !s.matchesFilter(entry.Model) {
			continue
		}
		visible = append(visible, entry)
		if len(visible) == maxVisibleModelChoices {
			break
		}
	}
	return visible
}

func (s *modelSelection) visibleCatalogModels() []runtimeCatalogModel {
	visible := make([]runtimeCatalogModel, 0, maxVisibleModelChoices)
	for _, entry := range s.Catalog.Models {
		if !s.matchesFilter(entry.CatalogID + " " + entry.Name) {
			continue
		}
		visible = append(visible, entry)
		if len(visible) == maxVisibleModelChoices {
			break
		}
	}
	return visible
}

func (s *modelSelection) SelectCatalogID(catalogID string) {
	for i, entry := range s.Catalog.Models {
		if entry.CatalogID == catalogID {
			s.SelectCatalog(i)
			return
		}
	}
}

func (s *modelSelection) SelectConfiguredModel(model string) {
	for i, entry := range s.Catalog.ConfiguredModels {
		if entry.Model == model {
			s.SelectConfigured(i)
			return
		}
	}
}

func (s *modelSelection) view(c *ui.Context, t *ui.Theme, actions modelSelectionActions) {
	msgs := locales.In(c.Locale())
	effective := s.Effective.Config.EffectiveConfig
	compaction := effective.Compaction
	ui.Column(c).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, msgs.ModelSessionTitle()).FontSize(typeSettingsTitle).FontWeight(500).FixedLineHeight(lineSettingsTitle).TextColor(t.Text)
			ui.Spacer(c)
			secondaryButton(c, msgs.ModelCancel()).Disabled(s.Busy).OnClick(func() {
				s.ResetDraft()
				if actions.Cancel != nil {
					actions.Cancel()
				}
			})
		})
		ui.Text(c, msgs.ModelTargetLabel(s.targetLabel(msgs))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		ui.Text(c, msgs.ModelSessionLine(valueOr(s.Target.Config.SessionID, msgs.ModelNotCreated()))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		if id := connectionIdentity(effective.ConnectionID); id != "" {
			ui.Text(c, msgs.ModelConnectionLine(id)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
		modelLine := msgs.ModelEffectiveModel(valueOr(effective.Model, msgs.ModelUnknown()))
		limitsPrefix := msgs.ModelEffectiveLimits
		if s.Unconfirmed {
			modelLine = msgs.ModelLastReadModel(valueOr(effective.Model, msgs.ModelUnknown()))
			limitsPrefix = msgs.ModelLastReadLimits
		}
		ui.Text(c, modelLine).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
		source := capacitySourceLabel(msgs, s.Effective)
		if catalogID := s.effectiveCatalogID(); catalogID != "" {
			source += " (" + catalogID + ")"
		}
		ui.Text(c, limitsPrefix(strconv.Itoa(compaction.ContextTokens), strconv.Itoa(compaction.OutputTokens), source, strconv.FormatUint(s.Target.Config.ConfigRevision, 10))).
			FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.TextMuted)
		toolsLine := msgs.ModelEffectiveTools(valueOr(builtinToolSelectionLabel(effective.ToolSelection.Builtin), msgs.BuiltinNone()))
		if s.Unconfirmed {
			toolsLine = msgs.ModelLastReadTools(valueOr(builtinToolSelectionLabel(effective.ToolSelection.Builtin), msgs.BuiltinNone()))
		}
		ui.Text(c, toolsLine).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		if s.Unconfirmed {
			ui.Text(c, msgs.ModelUnconfirmedHint()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
		}
		if s.Error != "" {
			ui.Text(c, modelErrorText(msgs, s.Error, s.ErrorDetail)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
		}
		if s.UnconfirmedKind != "" {
			ui.Text(c, modelUnconfirmedText(msgs, s.UnconfirmedKind, s.UnconfirmedDetail)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Warning)
		}
		ui.Text(c, msgs.ModelProviderID()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
		input := preferenceInput(c, &s.Draft.Model, false).Label(msgs.ModelProviderID()).Placeholder(msgs.ModelProviderID()).Disabled(s.Busy)
		if s.focusField == "Model" {
			input.Focus()
			s.focusField = ""
		}
		ui.Text(c, msgs.ModelCatalogHint()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		ui.Row(c).Gap(6).Children(func() {
			catalogButton := secondaryButton(c, msgs.ModelUseCatalog()).Disabled(s.Busy || len(s.Catalog.Models) == 0).OnClick(func() { s.SelectCapacityMode(modelCapacityCatalog) })
			selectionButtonState(c, catalogButton, s.Draft.CapacityMode == modelCapacityCatalog)
			explicitButton := secondaryButton(c, msgs.ModelExplicitLimits()).Disabled(s.Busy).OnClick(func() { s.SelectCapacityMode(modelCapacityExplicit) })
			selectionButtonState(c, explicitButton, s.Draft.CapacityMode != modelCapacityCatalog)
		})
		if s.Draft.CapacityMode == modelCapacityCatalog {
			ui.Text(c, msgs.ModelCatalogCapacityHint()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		} else {
			ui.Text(c, msgs.ModelContextTokens()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			preferenceInput(c, &s.Draft.ContextTokens, false).Label(msgs.ModelContextTokens()).Placeholder("131072").Disabled(s.Busy)
			ui.Text(c, msgs.ModelOutputTokens()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			preferenceInput(c, &s.Draft.OutputTokens, false).Label(msgs.ModelOutputTokens()).Placeholder("8192").Disabled(s.Busy)
			ui.Text(c, msgs.ModelCompactionHint()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(8).Children(func() {
			secondaryButton(c, msgs.ModelRefresh()).Disabled(s.Busy).OnClick(func() {
				if actions.Refresh != nil {
					actions.Refresh(s.Target)
				}
			})
			secondaryButton(c, locales.In(c.Locale()).ModelUpdateModel()).Disabled(s.Busy || s.Unconfirmed).OnClick(func() {
				if request, ok := s.Request(); ok && actions.Update != nil {
					actions.Update(request)
				}
			})
		})
		if s.Busy {
			ui.Text(c, msgs.ModelWaiting()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
		if s.Status != "" {
			ui.Text(c, modelStatusText(msgs, s.Status)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Text)
		}
		ui.Text(c, msgs.ModelFilterCandidates()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
		preferenceInput(c, &s.Filter, false).Label(msgs.ModelFilter()).Placeholder(msgs.ModelFilterPlaceholder()).Disabled(s.Busy)
		configured := s.visibleConfiguredModels()
		if len(configured) > 0 {
			ui.Text(c, msgs.ModelConfiguredModels()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			ui.Text(c, msgs.ModelConfiguredHint()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			for _, entry := range configured {
				entry := entry
				button := secondaryButton(c, msgs.ModelConfiguredChoice(entry.Model, strconv.Itoa(entry.Compaction.ContextTokens))).Disabled(s.Busy).OnClick(func() { s.SelectConfiguredModel(entry.Model) })
				selectionButtonState(c, button, s.Draft.Model == entry.Model && s.Draft.CapacityMode == modelCapacityExplicit)
			}
			if len(configured) < len(s.Catalog.ConfiguredModels) {
				ui.Text(c, msgs.ModelConfiguredMore(len(configured), len(s.Catalog.ConfiguredModels))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
		}
		catalog := s.visibleCatalogModels()
		if len(catalog) > 0 {
			ui.Text(c, msgs.ModelCatalogCapacity()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			for _, entry := range catalog {
				entry := entry
				label := entry.Name
				if label == "" {
					label = entry.CatalogID
				}
				button := secondaryButton(c, msgs.ModelCatalogChoice(label, strconv.Itoa(entry.Limits.Context))).Disabled(s.Busy).OnClick(func() { s.SelectCatalogID(entry.CatalogID) })
				selectionButtonState(c, button, s.Draft.CatalogID != "" && s.Draft.CatalogID == entry.CatalogID)
			}
			if len(catalog) < len(s.Catalog.Models) {
				ui.Text(c, msgs.ModelCatalogMore(len(catalog), len(s.Catalog.Models))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
		} else if len(s.Catalog.Models) == 0 {
			ui.Text(c, msgs.ModelNoCatalog()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
	})
}

const (
	modelStatusCatalogUnavailable     = "catalog-unavailable"
	modelStatusRefreshedNoCatalog     = "refreshed-no-catalog"
	modelStatusRefreshed              = "refreshed"
	modelStatusUpdated                = "updated"
	modelStatusApplied                = "applied"
	modelStatusReadingConfig          = "reading-config"
	modelStatusUpdating               = "updating"
	modelStatusReloading              = "reloading"
	modelStatusReloaded               = "reloaded"
	modelStatusReadback               = "readback"
	modelUnconfirmedQuarantined       = "quarantined"
	modelUnconfirmedNotConfirmed      = "not-confirmed"
	modelUnconfirmedNeedsReload       = "needs-reload"
	modelUnconfirmedCommit            = "commit"
	modelUnconfirmedReadback          = "readback"
	modelUnconfirmedReloadFailed      = "reload-failed"
	modelErrorRuntimeNotReady         = "runtime-not-ready"
	modelErrorNeedReload              = "need-reload"
	modelErrorNeedRefresh             = "need-refresh"
	modelErrorRefreshBefore           = "refresh-before"
	modelErrorBusy                    = "busy"
	modelErrorReleasedLoad            = "released-load"
	modelValidationUnconfirmedKind    = "validation-unconfirmed"
	modelValidationModelKind          = "validation-model-required"
	modelValidationSessionKind        = "validation-session-required"
	modelValidationCatalogKind        = "validation-catalog-required"
	modelValidationContextKind        = "validation-context-positive"
	modelValidationOutputKind         = "validation-output-positive"
	modelValidationGreaterKind        = "validation-context-greater"
	modelUnconfirmedNeedReloadUpdate  = "need-reload-update"
	modelUnconfirmedNeedRefreshUpdate = "need-refresh-update"
	modelPanelMessageNoSession        = "no-session"
	modelPanelMessageReading          = "reading"
	modelPanelErrorRuntimeNotReady    = "runtime-not-ready"
	modelPanelErrorReadFailed         = "read-failed"
)

func modelStatusText(msgs locales.Messages, kind string) string {
	switch kind {
	case modelStatusCatalogUnavailable:
		return msgs.ModelCatalogNotLoaded()
	case modelStatusRefreshedNoCatalog:
		return msgs.ModelRefreshedNoCatalog()
	case modelStatusRefreshed:
		return msgs.ModelRefreshed()
	case modelStatusUpdated:
		return msgs.ModelUpdated()
	case modelStatusApplied:
		return msgs.ModelConfigApplied()
	case modelStatusReadingConfig:
		return msgs.ModelReadingConfig()
	case modelStatusUpdating:
		return msgs.ModelUpdating()
	case modelStatusReloading:
		return msgs.ModelReloading()
	case modelStatusReloaded:
		return msgs.ModelReloaded()
	case modelStatusReadback:
		return msgs.ModelStatusReadback()
	default:
		return kind
	}
}

func modelUnconfirmedText(msgs locales.Messages, kind, detail string) string {
	switch kind {
	case modelUnconfirmedQuarantined:
		return msgs.ModelQuarantined()
	case modelUnconfirmedNotConfirmed:
		return msgs.ModelNotConfirmed()
	case modelUnconfirmedNeedsReload:
		return msgs.ModelNeedsExplicitReload()
	case modelUnconfirmedCommit:
		return msgs.ModelCommitUnconfirmed(detail)
	case modelUnconfirmedReadback:
		return msgs.ModelReadbackUnconfirmed(detail)
	case modelUnconfirmedReloadFailed:
		return msgs.ModelReloadFailed(detail)
	case modelUnconfirmedNeedReloadUpdate:
		return msgs.ModelNeedReloadUpdate()
	case modelUnconfirmedNeedRefreshUpdate:
		return msgs.ModelNeedRefreshUpdate()
	default:
		return kind
	}
}

func modelErrorText(msgs locales.Messages, kind, detail string) string {
	switch kind {
	case modelErrorRuntimeNotReady:
		return msgs.ModelNotReadyUpdate()
	case modelErrorNeedReload:
		return msgs.ModelNeedReloadUpdate()
	case modelErrorNeedRefresh:
		return msgs.ModelNeedRefreshUpdate()
	case modelErrorRefreshBefore:
		return msgs.ModelRefreshBeforeUpdate()
	case modelErrorBusy:
		return msgs.ModelBusy()
	case modelErrorReleasedLoad:
		return msgs.ModelLoadReleasedFailed(detail)
	case modelValidationUnconfirmedKind:
		return msgs.ModelValidationUnconfirmed()
	case modelValidationModelKind:
		return msgs.ModelValidationModelRequired()
	case modelValidationSessionKind:
		return msgs.ModelValidationSessionRequired()
	case modelValidationCatalogKind:
		return msgs.ModelValidationCatalogRequired()
	case modelValidationContextKind:
		return msgs.ModelValidationContextPositive()
	case modelValidationOutputKind:
		return msgs.ModelValidationOutputPositive()
	case modelValidationGreaterKind:
		return msgs.ModelValidationContextGreater()
	default:
		return kind
	}
}

func modelPanelMessageText(msgs locales.Messages, kind string) string {
	switch kind {
	case modelPanelMessageNoSession:
		return msgs.ModelNoSession()
	case modelPanelMessageReading:
		return msgs.ModelReading()
	default:
		return kind
	}
}

func modelPanelErrorText(msgs locales.Messages, kind, detail string) string {
	switch kind {
	case modelPanelErrorRuntimeNotReady:
		return msgs.ModelRuntimeNotReadyRead()
	case modelPanelErrorReadFailed:
		return msgs.ModelReadFailed(detail)
	default:
		return kind
	}
}

func (s *modelSelection) targetLabel(msgs locales.Messages) string {
	kind := s.Target.Kind
	switch kind {
	case "work", "issue":
		kind = msgs.ModelTargetIssueWork()
	case "conversation":
		kind = msgs.ModelTargetConversation()
	default:
		kind = valueOr(kind, msgs.ModelTargetSession())
	}
	return msgs.ModelTargetValue(kind, valueOr(s.Target.Label, s.Target.Config.SessionID))
}

func capacitySourceLabel(msgs locales.Messages, target modelSelectionTarget) string {
	switch target.Config.EffectiveConfig.CapacitySource.Kind {
	case "catalog":
		return msgs.ModelSourceCatalog()
	case "explicit":
		return msgs.ModelSourceExplicit()
	case "stored":
		return msgs.ModelSourceStored()
	default:
		return valueOr(target.Config.EffectiveConfig.CapacitySource.Kind, msgs.ModelSourceUnknown())
	}
}

func connectionIdentity(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}
