package app

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/markdown"
	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

type botCapabilitiesState struct {
	skills       capabilityRead[pi.ProductSkillList]
	requests     map[string]pi.ProductSkillApprovalRequest
	requestRead  capabilityRead[[]pi.ProductSkillApprovalRequest]
	requestEpoch uint64
	mountChoice  string
	serverChoice string
	promotion    *skillPromotion
	failure      capabilityNotice
}

type skillPromotion struct {
	key     pi.ProductSkillKey
	target  string
	problem string
}

type skillReviewState struct {
	key     pi.ProductSkillKey
	name    string
	request *pi.ProductSkillApprovalRequest
	read    capabilityRead[pi.ProductSkillDetail]
	file    string
	scroll  ui.ScrollState
	failure capabilityNotice
}

func (a *appModel) loadBotCapabilities(ct *contact) {
	a.refreshBotSkills(ct)
	a.refreshCapabilityServers()
	a.refreshSkillApprovalRequests(ct)
}

func (a *appModel) refreshBotSkills(ct *contact) {
	id := ct.ID
	readCapability(a, &ct.capabilities.skills, func(ctx context.Context, client *pi.ProductClient) (pi.ProductSkillList, error) {
		return client.ListSkills(ctx, id)
	}, nil)
}

func (a *appModel) applySkillApprovalRequests(rows []pi.ProductSkillApprovalRequest) {
	for _, row := range rows {
		ct := a.contactByID(row.BotID)
		if ct == nil {
			continue
		}
		state := &ct.capabilities
		if state.requests == nil {
			state.requests = make(map[string]pi.ProductSkillApprovalRequest)
		}
		if previous, ok := state.requests[row.RequestID]; !ok || row.Revision >= previous.Revision {
			state.requests[row.RequestID] = row
			state.requestEpoch++
		}
	}
}

func (a *appModel) applyActivitySkillApprovals(change *pi.ProductActivityChange) {
	a.applySkillApprovalRequests(change.ApprovalRequests)
	if ct := a.contactByID(change.OwnerBotID); ct != nil {
		state := &ct.capabilities
		for id, row := range state.requests {
			if row.ActivityID == change.ActivityID && row.State == "open" && !slices.Contains(change.PendingApprovalRequestIDs, id) {
				row.State = "closed"
				state.requests[id] = row
				state.requestEpoch++
			}
		}
	}
}

func (a *appModel) refreshSkillApprovalRequests(ct *contact) {
	id := ct.ID
	state := &ct.capabilities
	epoch := state.requestEpoch
	readCapability(a, &ct.capabilities.requestRead, func(ctx context.Context, client *pi.ProductClient) ([]pi.ProductSkillApprovalRequest, error) {
		items, err := listBotActiveActivities(ctx, client, id)
		if err != nil {
			return nil, err
		}
		var rows []pi.ProductSkillApprovalRequest
		for _, item := range items {
			if len(item.PendingApprovalRequestIDs) == 0 {
				continue
			}
			activity, err := client.GetActivity(ctx, item.ActivityID)
			if err != nil {
				return nil, err
			}
			rows = append(rows, activity.ApprovalRequests...)
		}
		return rows, nil
	}, func(rows []pi.ProductSkillApprovalRequest) {
		if state.requestEpoch == epoch {
			clear(state.requests)
		}
		a.applySkillApprovalRequests(rows)
	})
}

func (a *appModel) openBotCapabilitySettings(tab int) {
	a.capabilities.tab = tab
	a.openSettingsTab(settingsTabCapabilities, settingsSourceProfile)
}

func (a *appModel) botCapabilitiesView(c *ui.Context, t *ui.Theme, ct *contact, editor *contactProfileEditor) {
	msgs := locales.In(c.Locale())
	blocked := a.capabilitiesBlocked() || editor.Archived || editor.Busy
	c.Key("bot-capabilities")
	ui.Column(c).FillWidth().MinWidth(0).Gap(12).Padding(8, 0).Children(func() {
		preferenceSection(c, t, msgs.CapBuiltInTools(), func() {
			a.botBuiltinTools(c, t, msgs, &editor.Draft, blocked)
		})
		preferenceSection(c, t, msgs.CapSkills(), func() {
			a.botSkillControls(c, t, msgs, ct, &editor.Draft, blocked)
		})
		preferenceSection(c, t, msgs.CapMcp(), func() {
			a.botMcpControls(c, t, msgs, ct, &editor.Draft, blocked)
		})
	})
}

func (a *appModel) botBuiltinTools(c *ui.Context, t *ui.Theme, msgs locales.Messages, draft *profileDraft, blocked bool) {
	presets := [...]string{pi.ToolPresetNone, pi.ToolPresetReadonly, pi.ToolPresetWorkspace, pi.ToolPresetCustom}
	hints := [...]string{msgs.CapToolsNone(), msgs.CapToolsReadonly(), msgs.CapToolsWorkspace(), msgs.CapToolsCustom()}
	var explicit []string
	if draft.BuiltinTools != nil {
		explicit = *draft.BuiltinTools
	}
	effective := pi.ResolveCodingTools(explicit, draft.ToolPreset)
	selected := slices.Index(presets[:], draft.ToolPreset)
	if selected < 0 {
		selected = 3
		if effective == nil {
			effective = a.runtimeBuiltinDefaults
		}
	}
	capabilityProfileRow(c, t, func() {
		capabilityLabel(c, t, msgs.CapToolPreset())
		capabilityText(c, t, hints[selected])
		c.Key("bot-tool-preset")
		compact := *c.Theme()
		compact.Spacing = 2
		ui.Themed(c, &compact).MinWidth(0).Children(func() {
			theme.PreferenceSegmentedSelection(c, &selected, msgs.CapToolPreset(), blocked, false,
				msgs.CapNone(), msgs.CapReadOnly(), msgs.CapSkillWorkspace(), msgs.CapCustom()).Wrap().OnChange(func() {
				draft.ToolPreset = presets[selected]
				if selected == 3 {
					tools := append([]string{}, effective...)
					draft.BuiltinTools = &tools
				} else {
					draft.BuiltinTools = nil
				}
			})
		})
	}, selected != 3)
	if selected != 3 {
		return
	}
	capabilityProfileRow(c, t, func() {
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).Children(func() {
			for _, name := range pi.ResolveCodingTools(nil, pi.ToolPresetWorkspace) {
				c.Key(name)
				capabilityChip(c, t, name, builtinToolDescription(msgs, name), slices.Contains(effective, name), blocked).OnClick(func() {
					tools := toggleCapabilityTool(effective, name)
					draft.ToolPreset, draft.BuiltinTools = pi.ToolPresetCustom, &tools
				})
			}
		})
	}, true)
}

func builtinToolDescription(msgs locales.Messages, name string) string {
	switch name {
	case "bash":
		return msgs.CapToolBash()
	case "edit":
		return msgs.CapToolEdit()
	case "find":
		return msgs.CapToolFind()
	case "grep":
		return msgs.CapToolGrep()
	case "ls":
		return msgs.CapToolLs()
	case "read":
		return msgs.CapToolRead()
	case "write":
		return msgs.CapToolWrite()
	default:
		return ""
	}
}

func toggleCapabilityTool(tools []string, name string) []string {
	result := append([]string{}, tools...)
	if i := slices.Index(result, name); i >= 0 {
		return slices.Delete(result, i, i+1)
	}
	return append(result, name)
}

func capabilityChip(c *ui.Context, t *ui.Theme, name, hint string, selected, disabled bool) ui.Element {
	chip := ui.ButtonBase(c).Label(name).AccessSelected(selected).MinHeight(32).MinWidth(32).MaxWidth(180).Padding(4, 8).
		Radius(8).AlignItems(ui.Center).Justify(ui.Center).Disabled(disabled).Transition(capabilityTransition)
	fill, ink, border := t.Surface, t.TextMuted, t.Border
	if selected {
		fill, ink, border = t.Selection, t.Text, t.Focus
	}
	if chip.Pressed() {
		fill = t.SurfacePressed
	} else if chip.Hovered() {
		fill = t.SurfaceHover
	}
	if chip.FocusVisible() {
		border = t.Accent
	}
	chip.Background(fill).Border(1, border).Children(func() {
		ui.Text(c, name).FontSize(typeMeta).FixedLineHeight(lineMeta).MinWidth(0).MaxLines(1).Ellipsis("…").TextColor(ink)
	})
	return appTooltip(c, chip, name+"\n"+hint)
}

func botSkill(list pi.ProductSkillList, source, id string) (pi.ProductSkillSummary, bool) {
	for _, rows := range [][]pi.ProductSkillSummary{list.Private, list.Global, list.Workspace, list.Local} {
		for _, row := range rows {
			if row.Source == source && row.SkillID == id {
				return row, true
			}
		}
	}
	return pi.ProductSkillSummary{}, false
}

func skillApprovalBadge(c *ui.Context, t *ui.Theme, msgs locales.Messages, skill pi.ProductSkillSummary) {
	if len(skill.Scripts) == 0 && skill.ApprovalStatus == "" {
		return
	}
	switch skill.ApprovalStatus {
	case "pure_compute_tested":
		capabilityBadge(c, t, msgs.CapPureVerified(), t.Success)
	case "user_approved":
		capabilityBadge(c, t, msgs.CapApproved(), t.Success)
	default:
		capabilityBadge(c, t, msgs.CapApprovalRequired(), t.Warning)
	}
}

func capabilityProfileRow(c *ui.Context, t *ui.Theme, content func(), last ...bool) {
	row := ui.Column(c).FillWidth().MinWidth(0).AlignItems(ui.Start).Gap(6).Padding(10, 0)
	if len(last) == 0 || !last[0] {
		row.BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
	}
	row.Children(content)
}

func (a *appModel) botSkillControls(c *ui.Context, t *ui.Theme, msgs locales.Messages, ct *contact, draft *profileDraft, blocked bool) {
	s := &ct.capabilities
	list := s.skills.value
	s.skills.failure.render(c, t, msgs.CapSkillsReadFailed())
	s.requestRead.failure.render(c, t, msgs.CapApprovalReadFailed())
	s.failure.render(c, t, msgs.CapSkillChangeFailed())
	if s.skills.failure.detail != "" || s.requestRead.failure.detail != "" {
		secondaryButton(c, msgs.MemoryRetry()).AlignSelf(ui.Start).OnClick(func() { a.loadBotCapabilities(ct) })
	}
	for _, id := range slices.Sorted(maps.Keys(s.requests)) {
		request := s.requests[id]
		if request.State != "open" {
			continue
		}
		c.Key(id)
		row, found := botSkill(list, request.Source, request.SkillID)
		name := request.SkillID
		if found {
			name = row.Name
		}
		a.skillRequestBanner(c, t, msgs, ct, request, name, blocked)
	}
	for i, mounted := range draft.MountedSkills {
		key := pi.ProductSkillKey{Source: mounted.Source, BotID: ct.ID, SkillID: mounted.SkillID}
		c.Key(key)
		row, available := botSkill(list, mounted.Source, mounted.SkillID)
		available = available && (mounted.ExpectedDigest == "" || mounted.ExpectedDigest == row.SHA256)
		name := mounted.SkillID
		if row.Name != "" {
			name = row.Name
		}
		capabilityProfileRow(c, t, func() {
			capabilityHeading(c, t, name, func() {
				capabilityBadge(c, t, skillSourceLabel(msgs, mounted.Source), t.TextMuted)
				if !s.skills.ready {
					capabilityBadge(c, t, msgs.CapLoading(), t.TextMuted)
				} else if available {
					capabilityBadge(c, t, msgs.CapAvailable(), t.Success)
					skillApprovalBadge(c, t, msgs, row)
				} else {
					capabilityBadge(c, t, msgs.CapMissing(), t.Warning)
				}
			})
			ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).Children(func() {
				if available && len(row.Scripts) != 0 {
					a.skillReviewButton(c, msgs.CapReviewApprove(), key, name, nil, blocked)
				}
				theme.GhostButton(c, msgs.CapUnmount()).Padding(4, 0).Disabled(blocked).OnClick(func() {
					draft.MountedSkills = slices.Delete(draft.MountedSkills, i, i+1)
				})
			})
		})
	}
	a.mountSkillControls(c, t, msgs, ct, draft, blocked)
	if len(list.Private) != 0 {
		ui.Row(c).Padding(10, 0, 2).Children(func() { capabilityLabel(c, t, msgs.CapPrivateSkills()) })
	}
	for _, row := range list.Private {
		key := pi.ProductSkillKey{Source: "private", BotID: ct.ID, SkillID: row.SkillID}
		c.Key(key)
		a.privateSkillRow(c, t, msgs, ct, key, row, blocked)
	}
}

func (a *appModel) mountSkillControls(c *ui.Context, t *ui.Theme, msgs locales.Messages, ct *contact, draft *profileDraft, blocked bool) {
	s := &ct.capabilities
	list := s.skills.value
	options := []theme.SelectOption{{Value: "", Label: msgs.CapChooseSkill()}}
	choices := make(map[string]pi.ProductMountedSkill)
	for _, rows := range [][]pi.ProductSkillSummary{list.Global, list.Workspace, list.Local} {
		for _, row := range rows {
			if slices.ContainsFunc(draft.MountedSkills, func(m pi.ProductMountedSkill) bool { return m.Source == row.Source && m.SkillID == row.SkillID }) {
				continue
			}
			id := row.Source + ":" + row.SkillID
			options = append(options, theme.SelectOption{Value: id, Label: row.Name + " · " + skillSourceLabel(msgs, row.Source)})
			choices[id] = pi.ProductMountedSkill{Source: row.Source, SkillID: row.SkillID}
		}
	}
	if _, ok := choices[s.mountChoice]; !ok {
		s.mountChoice = ""
	}
	capabilityProfileRow(c, t, func() {
		capabilityLabel(c, t, msgs.CapMountSkill())
		if !s.skills.ready {
			capabilityText(c, t, msgs.CapLoading())
		}
		if len(choices) != 0 {
			ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
				theme.PreferenceSelect(c, &s.mountChoice, options, msgs.CapChooseSkill(), blocked).MinWidth(0).MaxWidth(208)
				secondaryButton(c, msgs.CapMount()).Disabled(blocked || s.mountChoice == "").OnClick(func() {
					if mount, ok := choices[s.mountChoice]; ok {
						draft.MountedSkills = append(draft.MountedSkills, mount)
						s.mountChoice = ""
					}
				})
			})
		}
		linkButton(c, msgs.CapOpenSettings()).Padding(4, 0).OnClick(func() { a.openBotCapabilitySettings(capabilityTabSkills) })
	}, len(list.Private) == 0)
}

func (a *appModel) skillRequestBanner(c *ui.Context, t *ui.Theme, msgs locales.Messages, ct *contact, request pi.ProductSkillApprovalRequest, name string, blocked bool) {
	capabilityProfileRow(c, t, func() {
		capabilityText(c, t, msgs.CapPendingApproval(name, request.Script)).TextColor(t.Warning).Role(ui.RoleStatus)
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).Children(func() {
			key := pi.ProductSkillKey{Source: request.Source, BotID: ct.ID, SkillID: request.SkillID}
			a.skillReviewButton(c, msgs.CapReviewApprove(), key, name, &request, blocked)
			theme.GhostButton(c, msgs.CapDeny()).Disabled(blocked).OnClick(func() { a.denySkillApproval(ct, request) })
		})
	})
}

func (a *appModel) denySkillApproval(ct *contact, request pi.ProductSkillApprovalRequest) {
	var updated pi.ProductSkillApprovalRequest
	a.runCapabilityRPC(func(ctx context.Context, client *pi.ProductClient) error {
		var err error
		updated, err = client.DenySkillApprovalRequest(ctx, pi.ProductSkillDenyApprovalRequestParams{
			RequestID: request.RequestID, RequestRevision: request.Revision, OperationID: newContactID(),
		})
		return err
	}, func(err error) {
		ct.capabilities.failure = a.capabilityFailure(err)
		if err == nil {
			a.applySkillApprovalRequests([]pi.ProductSkillApprovalRequest{updated})
		}
	})
}

func (a *appModel) privateSkillRow(c *ui.Context, t *ui.Theme, msgs locales.Messages, ct *contact, key pi.ProductSkillKey, row pi.ProductSkillSummary, blocked bool) {
	s := &ct.capabilities
	capabilityProfileRow(c, t, func() {
		capabilityHeading(c, t, row.Name, func() {
			kind := msgs.CapSkillText()
			if len(row.Scripts) != 0 {
				kind = msgs.CapExecutable()
			}
			capabilityBadge(c, t, kind, t.TextMuted)
			skillApprovalBadge(c, t, msgs, row)
		})
		capabilityText(c, t, row.Description)
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).Children(func() {
			label := msgs.CapInspect()
			if len(row.Scripts) != 0 {
				label = msgs.CapReviewApprove()
			}
			a.skillReviewButton(c, label, key, row.Name, nil, blocked)
			if s.promotion == nil || s.promotion.key != key {
				secondaryButton(c, msgs.CapPromoteGlobal()).Disabled(blocked).OnClick(func() { a.promotePrivateSkill(ct, key, "") })
			}
			dangerButton(c, msgs.CapDeleteSkill()).Disabled(blocked).OnClick(func() { a.deleteCapabilitySkill(key, &s.failure) })
		})
		if p := s.promotion; p != nil && p.key == key {
			ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
				preferenceInput(c, &p.target, p.problem != "", t.Focus).Basis(136).Grow(1).MinWidth(0).Label(msgs.CapSkillSlug()).Disabled(blocked)
				secondaryButton(c, msgs.CapPromote()).Disabled(blocked).OnClick(func() {
					if !skillSlugPattern.MatchString(p.target) {
						p.problem = msgs.CapSkillSlugInvalid()
						return
					}
					a.promotePrivateSkill(ct, key, p.target)
				})
				theme.GhostButton(c, msgs.CreateBotCancel()).Disabled(blocked).OnClick(func() { s.promotion = nil })
			})
			capabilityFieldError(c, t, p.problem)
		}
	})
}

func (a *appModel) promotePrivateSkill(ct *contact, key pi.ProductSkillKey, target string) {
	a.runCapabilityRPC(func(ctx context.Context, client *pi.ProductClient) error {
		_, err := client.PromoteSkill(ctx, pi.ProductSkillPromoteParams{BotID: key.BotID, SkillID: key.SkillID, TargetSkillID: target, OperationID: newContactID()})
		return err
	}, func(err error) {
		s := &ct.capabilities
		s.failure = capabilityNotice{}
		if capabilityErrorCode(err) == "already_exists" {
			s.promotion = &skillPromotion{key: key, target: target, problem: locales.In(a.uiLocale).CapSkillSlugTaken()}
		} else if err != nil {
			s.failure = a.capabilityFailure(err)
		} else {
			s.promotion = nil
			a.refreshCapabilitiesSkills()
		}
	})
}

func (a *appModel) botMcpControls(c *ui.Context, t *ui.Theme, msgs locales.Messages, ct *contact, draft *profileDraft, blocked bool) {
	servers := &a.capabilities.servers
	servers.failure.render(c, t, msgs.CapMcpReadFailed())
	if servers.failure.detail != "" {
		secondaryButton(c, msgs.MemoryRetry()).AlignSelf(ui.Start).OnClick(a.refreshCapabilityServers)
	}
	for i := range draft.McpBindings {
		binding := &draft.McpBindings[i]
		c.Key(binding.ServerID)
		row, exists := capabilityServer(servers.value, binding.ServerID)
		capabilityProfileRow(c, t, func() {
			name, status, ink := msgs.CapMcpServer(), msgs.CapUnavailable(), t.Warning
			if exists {
				name = row.Name
				status, ink = mcpHealth(msgs, t, row)
			} else if !servers.ready {
				status, ink = msgs.CapLoading(), t.TextMuted
			}
			capabilityHeading(c, t, name, func() { capabilityBadge(c, t, status, ink) })
			ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
				policies := [...]string{"readonly", "allowlist", "all"}
				selected := max(0, slices.Index(policies[:], binding.Policy))
				theme.PreferenceSegmentedSelection(c, &selected, msgs.CapToolScope(), blocked || !exists, false,
					msgs.CapReadOnly(), msgs.CapSelected(), msgs.CapAll()).OnChange(func() {
					binding.Policy = policies[selected]
					if binding.Policy != "allowlist" {
						binding.AllowedTools = nil
					}
				})
				theme.GhostButton(c, msgs.CapRemove()).Padding(4, 0).Disabled(blocked).OnClick(func() { draft.McpBindings = slices.Delete(draft.McpBindings, i, i+1) })
			})
			if binding.Policy == "allowlist" {
				botMcpToolChips(c, t, msgs, binding, row.Catalog, blocked || !exists)
			}
		})
	}
	options := []theme.SelectOption{{Value: "", Label: msgs.CapChooseServer()}}
	for _, row := range servers.value {
		if !slices.ContainsFunc(draft.McpBindings, func(b pi.ProductMcpBinding) bool { return b.ServerID == row.ServerID }) {
			options = append(options, theme.SelectOption{Value: row.ServerID, Label: row.Name})
		}
	}
	s := &ct.capabilities
	if !slices.ContainsFunc(options, func(option theme.SelectOption) bool { return option.Value == s.serverChoice }) {
		s.serverChoice = ""
	}
	capabilityProfileRow(c, t, func() {
		capabilityLabel(c, t, msgs.CapAttachServer())
		switch {
		case len(options) > 1:
			ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
				theme.PreferenceSelect(c, &s.serverChoice, options, msgs.CapChooseServer(), blocked).MinWidth(0).MaxWidth(208)
				secondaryButton(c, msgs.CapAttach()).Disabled(blocked || s.serverChoice == "").OnClick(func() {
					if s.serverChoice != "" {
						draft.McpBindings = append(draft.McpBindings, pi.ProductMcpBinding{ServerID: s.serverChoice, Policy: "readonly"})
						s.serverChoice = ""
					}
				})
			})
		case !servers.ready:
			capabilityText(c, t, msgs.CapLoading())
		case len(servers.value) == 0:
			linkButton(c, msgs.CapConfigureMcp()).Padding(4, 0).OnClick(func() { a.openBotCapabilitySettings(capabilityTabMCP) })
		default:
			capabilityText(c, t, msgs.CapAllServersAttached())
		}
	}, true)
}

func botMcpToolChips(c *ui.Context, t *ui.Theme, msgs locales.Messages, binding *pi.ProductMcpBinding, catalog *pi.ProductMcpCatalog, blocked bool) {
	var tools []pi.ProductMcpTool
	if catalog != nil {
		tools = catalog.Tools
	}
	if len(tools) == 0 {
		capabilityText(c, t, msgs.CapNoMcpTools())
	}
	ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).Children(func() {
		for _, tool := range tools {
			c.Key(tool.Name)
			hint, _ := mcpToolHint(t, msgs, tool)
			capabilityChip(c, t, tool.Name, hint+"\n"+tool.Description, slices.Contains(binding.AllowedTools, tool.Name), blocked).OnClick(func() {
				binding.AllowedTools = toggleCapabilityTool(binding.AllowedTools, tool.Name)
			})
		}
		for _, name := range binding.AllowedTools {
			if slices.ContainsFunc(tools, func(tool pi.ProductMcpTool) bool { return tool.Name == name }) {
				continue
			}
			c.Key(name)
			capabilityChip(c, t, name, msgs.CapMissing(), true, blocked).OnClick(func() {
				binding.AllowedTools = toggleCapabilityTool(binding.AllowedTools, name)
			})
		}
	})
}

func (a *appModel) skillReviewButton(c *ui.Context, label string, key pi.ProductSkillKey, name string, request *pi.ProductSkillApprovalRequest, blocked bool) {
	button := secondaryButton(c, label).Disabled(blocked)
	button.OnClick(func() {
		button.Focus()
		review := &skillReviewState{key: key, name: name, request: request, file: "SKILL.md"}
		a.capabilities.review = review
		a.loadSkillReview(review)
	})
}

func (a *appModel) loadSkillReview(review *skillReviewState) {
	key := review.key
	readCapability(a, &review.read, func(ctx context.Context, client *pi.ProductClient) (pi.ProductSkillDetail, error) {
		return client.GetSkill(ctx, key)
	}, nil)
}

func (a *appModel) canApproveSkill(review *skillReviewState) bool {
	if a.capabilitiesBlocked() || review.read.loading || !review.read.ready || review.read.failure.detail != "" {
		return false
	}
	ct := a.contactByID(review.key.BotID)
	if ct == nil || ct.Archived {
		return false
	}
	value := review.read.value
	if value.ApprovalDigest == "" || value.PermissionsDigest == "" || value.CanonicalWorkspace == "" || len(value.Files) == 0 {
		return false
	}
	if req := review.request; req != nil {
		current := ct.capabilities.requests[req.RequestID]
		return current.State == "open" && current.Revision == req.Revision && value.ApprovalDigest == req.ApprovalDigest &&
			value.PermissionsDigest == req.PermissionsDigest && value.CanonicalWorkspace == req.CanonicalWorkspace
	}
	if review.key.Source == "private" {
		return true
	}
	return slices.ContainsFunc(ct.MountedSkills, func(mount pi.ProductMountedSkill) bool {
		return mount.Source == review.key.Source && mount.SkillID == review.key.SkillID &&
			(mount.ExpectedDigest == "" || mount.ExpectedDigest == value.SHA256)
	})
}

func (a *appModel) grantReviewedSkill(review *skillReviewState) {
	if !a.canApproveSkill(review) {
		return
	}
	value := review.read.value
	params := pi.ProductSkillApproveParams{ProductSkillKey: review.key, OperationID: newContactID(), ApprovalDigest: value.ApprovalDigest,
		PermissionsDigest: value.PermissionsDigest, CanonicalWorkspace: value.CanonicalWorkspace}
	if req := review.request; req != nil {
		params.RequestID, params.RequestRevision, params.InstallationID = req.RequestID, req.Revision, req.InstallationID
	}
	var result pi.ProductSkillChange
	a.runCapabilityRPC(func(ctx context.Context, client *pi.ProductClient) error {
		var err error
		result, err = client.ApproveSkill(ctx, params)
		return err
	}, func(err error) {
		review.failure = a.capabilityFailure(err)
		if err == nil {
			a.capabilities.review = nil
			if result.Request != nil {
				a.applySkillApprovalRequests([]pi.ProductSkillApprovalRequest{*result.Request})
			}
			a.refreshCapabilitiesSkills()
		}
	})
}

func (a *appModel) revokeReviewedSkill(review *skillReviewState) {
	a.runCapabilityRPC(func(ctx context.Context, client *pi.ProductClient) error {
		_, err := client.RevokeSkillApproval(ctx, pi.ProductSkillRevokeParams{ProductSkillKey: review.key, OperationID: newContactID()})
		return err
	}, func(err error) {
		review.failure = a.capabilityFailure(err)
		if err == nil {
			a.capabilities.review = nil
			a.refreshCapabilitiesSkills()
		}
	})
}

func (a *appModel) skillReviewDialog(c *ui.Context, base *ui.Theme) {
	review := a.capabilities.review
	if review == nil {
		return
	}
	msgs := locales.In(c.Locale())
	value := review.read.value
	name := review.name
	if review.read.ready {
		name = value.Name
	}
	closeDialog := func() { a.capabilities.review = nil }
	capabilityDialog(c, base, "review-skill", name, msgs.CapReviewHint(), a.capabilitiesBusy(), &review.scroll, closeDialog, func(t *ui.Theme) {
		review.read.failure.render(c, t, msgs.CapSkillReadFailed())
		if review.read.loading {
			capabilityText(c, t, msgs.CapLoading()).Role(ui.RoleStatus)
			return
		}
		if review.read.failure.detail != "" {
			secondaryButton(c, msgs.MemoryRetry()).AlignSelf(ui.Start).OnClick(func() { a.loadSkillReview(review) })
			return
		}
		ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
			capabilityBadge(c, t, skillSourceLabel(msgs, review.key.Source), t.TextMuted)
			if value.ApprovalDigest != "" {
				digest := value.ApprovalDigest[:min(12, len(value.ApprovalDigest))]
				appTooltip(c, capabilityBadge(c, t, digest, t.TextMuted).Font("monospace"), value.ApprovalDigest)
			}
			skillApprovalBadge(c, t, msgs, value.ProductSkillSummary)
		})
		if len(value.Scripts) == 0 {
			capabilityText(c, t, value.Description)
			markdownReader(c, value.Content, markdownReaderOptions{CopyLabel: msgs.CopyCodeLabel()}).MaxWidthPercent(100).MinWidth(0)
			return
		}
		skillPermissionChecklist(c, t, msgs, value)
		skillScriptList(c, t, msgs, value, len(value.Scripts) > 1)
		capabilityLabel(c, t, msgs.CapReviewedFiles())
		options := make([]theme.SelectOption, 0, len(value.Files))
		for _, name := range slices.Sorted(maps.Keys(value.Files)) {
			options = append(options, theme.SelectOption{Value: name, Label: name})
		}
		if len(options) != 0 {
			if _, ok := value.Files[review.file]; !ok {
				review.file = options[0].Value
			}
			theme.PreferenceSelect(c, &review.file, options, msgs.CapReviewedFiles(), a.capabilitiesBusy()).MaxWidth(260)
			c.Key(review.file)
			capabilityCode(c, msgs, value.Files[review.file])
		}
	}, func(t *ui.Theme) {
		review.failure.render(c, t, msgs.CapApprovalChangeFailed())
		executable := len(value.Scripts) != 0
		canApprove := a.canApproveSkill(review)
		if executable && !canApprove && !a.capabilitiesBusy() && review.failure.detail == "" {
			message := msgs.CapSaveMountFirst()
			if review.request != nil {
				message = msgs.CapReviewRequestChanged()
			}
			capabilityText(c, t, message).TextColor(t.Warning)
		}
		ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
			theme.GhostButton(c, msgs.CreateBotCancel()).Disabled(a.capabilitiesBusy()).OnClick(closeDialog)
			ui.Row(c).MinWidth(0).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
				if value.ApprovalStatus == "user_approved" || value.ApprovalStatus == "pure_compute_tested" {
					dangerButton(c, msgs.CapRevokeApproval()).Disabled(a.capabilitiesBlocked()).OnClick(func() { a.revokeReviewedSkill(review) })
				}
				if executable {
					primaryButton(c, msgs.CapApproveBot()).Disabled(!canApprove || value.ApprovalStatus == "user_approved" && review.request == nil).
						OnClick(func() { a.grantReviewedSkill(review) })
				}
			})
		})
	})
}

func skillPermissionChecklist(c *ui.Context, t *ui.Theme, msgs locales.Messages, value pi.ProductSkillDetail) {
	var permissions pi.ProductSkillPermissions
	for _, script := range value.Scripts {
		p := script.Permissions
		permissions.WorkspaceRead = permissions.WorkspaceRead || p.WorkspaceRead
		permissions.WorkspaceWrite = permissions.WorkspaceWrite || p.WorkspaceWrite
		permissions.NetworkOrigins = append(permissions.NetworkOrigins, p.NetworkOrigins...)
		permissions.CredentialRefs = append(permissions.CredentialRefs, p.CredentialRefs...)
	}
	slices.Sort(permissions.NetworkOrigins)
	slices.Sort(permissions.CredentialRefs)
	permissions.NetworkOrigins = slices.Compact(permissions.NetworkOrigins)
	permissions.CredentialRefs = slices.Compact(permissions.CredentialRefs)
	capabilityLabel(c, t, msgs.CapPermissions())
	workspace := []string(nil)
	if permissions.WorkspaceRead || permissions.WorkspaceWrite {
		workspace = []string{value.CanonicalWorkspace}
	}
	permissionLine(c, t, msgs.CapWorkspaceAccess(), workspacePermission(msgs, permissions), workspace)
	permissionLine(c, t, msgs.CapNetwork(), emptyPermission(msgs.CapNetworkBlocked(), permissions.NetworkOrigins), permissions.NetworkOrigins)
	permissionLine(c, t, msgs.CapCredentials(), emptyPermission(msgs.CapNone(), permissions.CredentialRefs), permissions.CredentialRefs)
}

func emptyPermission(label string, items []string) string {
	if len(items) == 0 {
		return label
	}
	return ""
}

func permissionLine(c *ui.Context, t *ui.Theme, label, summary string, values []string) {
	row := ui.Row(c).FillWidth().MinWidth(0).Gap(12).Padding(6, 0).BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
	row.Children(func() {
		ui.Text(c, label).Width(112).Shrink(0).FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction).TextColor(t.Text)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			if summary != "" {
				capabilityText(c, t, summary)
			}
			for _, value := range values {
				c.Key(value)
				appTooltip(c, capabilityText(c, t, value).Selectable().MaxLines(2).Ellipsis("…"), value)
			}
		})
	})
}

func workspacePermission(msgs locales.Messages, value pi.ProductSkillPermissions) string {
	if value.WorkspaceWrite {
		return msgs.CapReadWrite()
	}
	if value.WorkspaceRead {
		return msgs.CapReadOnly()
	}
	return msgs.CapIsolated()
}

func skillScriptList(c *ui.Context, t *ui.Theme, msgs locales.Messages, value pi.ProductSkillDetail, includePermissions bool) {
	capabilityLabel(c, t, msgs.CapScripts())
	for _, name := range slices.Sorted(maps.Keys(value.Scripts)) {
		c.Key(name)
		script := value.Scripts[name]
		limit := script.TimeoutMS
		if limit == 0 {
			limit = 10000
		}
		row := ui.Column(c).FillWidth().MinWidth(0).Gap(4).Padding(8, 0).BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
		row.Children(func() {
			capabilityHeading(c, t, name, func() { capabilityBadge(c, t, msgs.CapTimeoutMS(strconv.Itoa(limit)), t.TextMuted) })
			capabilityText(c, t, script.Entry).Font("monospace")
			capabilityText(c, t, script.Description)
			if includePermissions {
				permissionLine(c, t, msgs.CapWorkspaceAccess(), workspacePermission(msgs, script.Permissions), nil)
				permissionLine(c, t, msgs.CapNetwork(), emptyPermission(msgs.CapNetworkBlocked(), script.Permissions.NetworkOrigins), script.Permissions.NetworkOrigins)
				permissionLine(c, t, msgs.CapCredentials(), emptyPermission(msgs.CapNone(), script.Permissions.CredentialRefs), script.Permissions.CredentialRefs)
			}
		})
	}
}

func capabilityCode(c *ui.Context, msgs locales.Messages, source string) {
	t := c.Theme()
	box := ui.Column(c).FillWidth().MinWidth(0).Padding(4, 12, 12).Gap(4).
		Radius(10).Border(1, t.Border).Background(t.Surface.Mix(t.Text, .04))
	type copyFeedback struct {
		until time.Time
		err   error
	}
	feedback := ui.Local(box, "source-copy-feedback", func() copyFeedback { return copyFeedback{} })
	box.Children(func() {
		label := msgs.CopyCodeLabel()
		icon, color, tip := theme.MarkdownCopyIcon, t.TextMuted, label
		if c.Now().Before(feedback.until) {
			if feedback.err == nil {
				icon, color, tip = theme.MarkdownCopiedIcon, t.Success, msgs.Copied()
			} else {
				icon, color, tip = theme.MarkdownCopyFailedIcon, t.Danger, msgs.CopyFailed(feedback.err.Error())
			}
		}
		copyButton := iconControlFace(c, ui.ButtonBase(c).Label(label), iconResponse, false).Size(32, 32).AlignSelf(ui.End)
		appTooltip(c, copyButton, tip).Children(func() { controlIcon(c, icon, iconResponse).TextColor(color) })
		copyButton.OnClick(func() {
			feedback.err = markdown.WriteClipboardText(source, c.Services().WriteClipboard, c.Services().ReadClipboard)
			feedback.until = c.Now().Add(2 * time.Second)
			c.After(2 * time.Second)
		})
		ui.Text(c, source).FillWidth().MinWidth(0).Selectable().Font("monospace").FontSize(typeAction).FixedLineHeight(lineLabel).TextColor(t.Text)
	})
}
