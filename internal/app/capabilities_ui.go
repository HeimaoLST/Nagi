package app

import (
	"context"
	"errors"
	"maps"
	"net/url"
	"path/filepath"
	"regexp"
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

const (
	capabilityTabSkills = iota
	capabilityTabMCP
)

var capabilityTransition = ui.ElementTransition{Duration: 180 * time.Millisecond, Colors: true, Shadows: true}
var skillSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var mcpNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,63}$`)
var mcpEnvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var mcpHeaderKeyPattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

type capabilityNotice struct {
	detail string
	open   bool
}

func (n *capabilityNotice) render(c *ui.Context, t *ui.Theme, title string) {
	if n.detail != "" {
		recoveryNoticeView(c, t, title, n.detail, n.open, func() { n.open = !n.open }, nil, "")
	}
}

type capabilityRead[T any] struct {
	value          T
	version        uint64
	loading, ready bool
	failure        capabilityNotice
}

type capabilitySettings struct {
	tab           int
	busy          bool
	discoverLocal bool
	library       capabilityRead[pi.ProductSkillList]
	servers       capabilityRead[[]pi.ProductMcpServerView]
	selectedSkill pi.ProductSkillKey
	selectedMcp   string
	focusSkill    pi.ProductSkillKey
	focusMcp      string
	skillDetails  map[pi.ProductSkillKey]*skillDetailRead
	skillEdit     *skillEditor
	skillFiles    bool
	mcpEditors    map[string]*mcpEditor
	newSkill      *skillEditor
	newMcp        *mcpEditor
	review        *skillReviewState
	listScroll    [2]ui.ScrollState
	detailScroll  [2]ui.ScrollState
	dialogScroll  ui.ScrollState
	skillFailure  capabilityNotice
	mcpFailure    capabilityNotice
}

type skillDetailRead struct {
	capabilityRead[pi.ProductSkillDetail]
	digest string
}

type skillEditor struct {
	key                      pi.ProductSkillKey
	sha, name, summary, body string
	slugEdited               bool
	fields                   map[string]string
	failure                  capabilityNotice
}

func (a *appModel) capabilitiesBusy() bool { return a.capabilities.busy }

func (a *appModel) capabilitiesBlocked() bool {
	return a.capabilitiesBusy() || !a.productProfileReady() || a.dataRecoveryBlocksChanges()
}

func (a *appModel) capabilityFailure(err error) capabilityNotice {
	if err == nil {
		return capabilityNotice{}
	}
	return capabilityNotice{detail: a.connectionSafeMessage(err)}
}

func capabilityErrorCode(err error) string {
	var remote *pi.RemoteError
	if errors.As(err, &remote) {
		return remote.Code
	}
	return ""
}

func readCapability[T any](a *appModel, state *capabilityRead[T], run func(context.Context, *pi.ProductClient) (T, error), applied func(T)) {
	host := a.productHost()
	if !a.productProfileReady() {
		return
	}
	state.version++
	version := state.version
	state.loading = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		value, err := run(ctx, host.product)
		a.updateUI(func() {
			if a.productHost() != host || state.version != version {
				return
			}
			state.loading = false
			state.failure = a.capabilityFailure(err)
			if err == nil {
				state.value, state.ready = value, true
				if applied != nil {
					applied(value)
				}
			}
		})
	}()
}

func (a *appModel) runCapabilityRPC(run func(context.Context, *pi.ProductClient) error, done func(error)) {
	if a.capabilitiesBlocked() {
		return
	}
	host := a.productHost()
	a.capabilities.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := run(ctx, host.product)
		a.updateUI(func() {
			if a.productHost() != host {
				return
			}
			a.capabilities.busy = false
			done(err)
			if a.shutdownStarted {
				a.finishShutdown()
			}
		})
	}()
}

func (a *appModel) refreshCapabilitiesSkills() {
	a.refreshCapabilityLibrary()
	if ct := a.contactByID(a.contactProfileID); ct != nil && a.detail == detailProfile {
		a.refreshBotSkills(ct)
	}
}

func (a *appModel) refreshCapabilityLibrary() {
	s := &a.capabilities
	readCapability(a, &s.library, func(ctx context.Context, client *pi.ProductClient) (pi.ProductSkillList, error) {
		return client.ListSkills(ctx, "")
	}, func(list pi.ProductSkillList) {
		s.discoverLocal = list.DiscoverLocalSkills
		for key := range s.skillDetails {
			if _, ok := librarySkill(list, key); !ok {
				delete(s.skillDetails, key)
			}
		}
		if row, ok := librarySkill(list, s.selectedSkill); ok {
			a.selectLibrarySkill(row)
		} else if len(list.Global) != 0 {
			a.selectLibrarySkill(list.Global[0])
		} else if len(list.Local) != 0 {
			a.selectLibrarySkill(list.Local[0])
		} else {
			s.selectedSkill = pi.ProductSkillKey{}
			s.skillEdit = nil
		}
	})
}

func librarySkill(list pi.ProductSkillList, key pi.ProductSkillKey) (pi.ProductSkillSummary, bool) {
	rows := list.Global
	if key.Source == "local" {
		rows = list.Local
	}
	for _, row := range rows {
		if row.Source == key.Source && row.SkillID == key.SkillID {
			return row, true
		}
	}
	return pi.ProductSkillSummary{}, false
}

func (a *appModel) selectLibrarySkill(row pi.ProductSkillSummary) {
	s := &a.capabilities
	key := pi.ProductSkillKey{Source: row.Source, SkillID: row.SkillID}
	if s.selectedSkill != key {
		s.selectedSkill = key
		s.skillEdit = nil
		s.skillFiles = false
		s.detailScroll[capabilityTabSkills].Y = 0
	}
	if s.skillDetails == nil {
		s.skillDetails = make(map[pi.ProductSkillKey]*skillDetailRead)
	}
	read := s.skillDetails[key]
	if read == nil || read.digest != row.SHA256 {
		read = &skillDetailRead{digest: row.SHA256}
		s.skillDetails[key] = read
	}
	if read.loading || read.ready && read.value.SHA256 == row.SHA256 && read.failure.detail == "" {
		return
	}
	readCapability(a, &read.capabilityRead, func(ctx context.Context, client *pi.ProductClient) (pi.ProductSkillDetail, error) {
		return client.GetSkill(ctx, key)
	}, nil)
}

func (a *appModel) setLocalSkillDiscovery(on bool) {
	a.runCapabilityRPC(func(ctx context.Context, client *pi.ProductClient) error {
		_, err := client.SetSkillPreferences(ctx, pi.ProductSkillPreferences{DiscoverLocalSkills: on, OperationID: newContactID()})
		return err
	}, func(err error) {
		a.capabilities.skillFailure = a.capabilityFailure(err)
		if err == nil {
			a.capabilities.discoverLocal = on
			a.refreshCapabilitiesSkills()
		}
	})
}

func (a *appModel) saveLibrarySkill(e *skillEditor) {
	msgs := locales.In(a.uiLocale)
	e.fields = make(map[string]string)
	name, summary := strings.TrimSpace(e.name), strings.TrimSpace(e.summary)
	if name == "" {
		e.fields["name"] = msgs.CapRequired()
	}
	if !skillSlugPattern.MatchString(e.key.SkillID) {
		e.fields["slug"] = msgs.CapSkillSlugInvalid()
	}
	if summary == "" {
		e.fields["summary"] = msgs.CapRequired()
	}
	if strings.TrimSpace(e.body) == "" {
		e.fields["body"] = msgs.CapRequired()
	}
	if len(e.fields) != 0 {
		return
	}
	params := pi.ProductSkillSaveParams{ProductSkillKey: e.key, Name: name, Description: summary, Content: e.body, OperationID: newContactID()}
	creating := a.capabilities.newSkill == e
	sha := e.sha
	var conflict bool
	a.runCapabilityRPC(func(ctx context.Context, client *pi.ProductClient) error {
		current, err := client.GetSkill(ctx, params.ProductSkillKey)
		if err == nil && (creating || current.SHA256 != sha) {
			conflict = true
			return nil
		}
		if err != nil && (!creating || capabilityErrorCode(err) != "skill_missing") {
			return err
		}
		_, err = client.SaveSkill(ctx, params)
		return err
	}, func(err error) {
		e.failure = a.capabilityFailure(err)
		if conflict {
			if creating {
				e.fields["slug"] = locales.In(a.uiLocale).CapSkillSlugTaken()
			} else {
				e.failure.detail = locales.In(a.uiLocale).CapSkillChanged()
				a.refreshCapabilityLibrary()
			}
			return
		}
		if err != nil {
			return
		}
		s := &a.capabilities
		delete(s.skillDetails, params.ProductSkillKey)
		s.selectedSkill, s.focusSkill = params.ProductSkillKey, params.ProductSkillKey
		s.newSkill, s.skillEdit = nil, nil
		a.refreshCapabilitiesSkills()
	})
}

func (a *appModel) capabilityTabs(c *ui.Context, msgs locales.Messages) {
	s := &a.capabilities
	c.Key("capability-subtabs")
	selected := s.tab
	theme.PreferenceSegmentedSelection(c, &selected, msgs.SettingsCapabilities(), false, false,
		msgs.CapSkillsCount(len(s.library.value.Global)+len(s.library.value.Local)), msgs.CapMcpCount(len(s.servers.value))).OnChange(func() {
		s.tab = selected
		a.settingsScroll.Y = 0
		if selected == capabilityTabSkills {
			a.refreshCapabilityLibrary()
		} else {
			a.refreshCapabilityServers()
		}
	})
}

func (a *appModel) settingsCapabilitiesContent(c *ui.Context, t *ui.Theme, wide bool) {
	s := &a.capabilities
	c.Key(s.tab)
	if s.tab == capabilityTabSkills {
		a.skillLibrary(c, t, wide)
	} else {
		a.mcpSettings(c, t, wide)
	}
}

func capabilityStrip(c *ui.Context, t *ui.Theme, summary string, ink ui.Color, actions func()) {
	bar := ui.Row(c).FillWidth().MinWidth(0).Shrink(0).Wrap().AlignItems(ui.Center).Gap(8).Padding(10, 14).
		Radius(10).Border(1, t.Border).Background(t.Surface)
	bar.Children(func() {
		ui.Row(c).Basis(210).Grow(1).MinWidth(0).AlignItems(ui.Center).Gap(8).Children(func() {
			providerDot(c, ink)
			ui.Text(c, summary).MinWidth(0).FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction).TextColor(t.Text)
		})
		actions()
	})
}

func capabilitySplit(c *ui.Context, t *ui.Theme, wide bool, scroll *ui.ScrollState, header, list, detail func()) {
	background := t.Surface.Mix(t.Background, .45)
	split := ui.Row(c).FillWidth().MinWidth(0).Background(t.Surface).Border(1, t.Border).Radius(12).Clip()
	if wide {
		split.Grow(1).MinHeight(0).AlignItems(ui.Stretch)
	} else {
		split.Column()
	}
	split.Children(func() {
		rail := ui.Column(c).MinWidth(0).MinHeight(0).Shrink(0).Background(background).BorderColor(t.Border)
		if wide {
			rail.Width(236).BorderWidth(0, 1, 0, 0)
		} else {
			rail.FillWidth().BorderWidth(0, 0, 1, 0)
		}
		rail.Children(func() {
			if header != nil {
				header()
			}
			pane := ui.Scroll(c).FillWidth().MinWidth(0).MinHeight(0).TrackScroll(scroll)
			if wide {
				pane.Grow(1)
			} else {
				pane.MaxHeight(144)
			}
			pane.Children(func() { ui.Column(c).FillWidth().MinWidth(0).Padding(6).Gap(4).Children(list) })
		})
		pane := ui.Column(c).MinWidth(0).MinHeight(0)
		if wide {
			pane.Grow(1)
		} else {
			pane.FillWidth()
		}
		pane.Children(detail)
	})
}

func (a *appModel) capabilityListRow(c *ui.Context, t *ui.Theme, name, subtitle, state string, ink ui.Color, selected, disabled bool) ui.Element {
	palette := a.workTheme
	palette.NavHover, palette.NavSelected = ui.Transparent, ui.Transparent
	row := navigationControl(c, ui.ButtonBase(c).Label(name).AccessSelected(selected), palette, selected).
		FillWidth().MinWidth(0).Height(52).MinHeight(52).Shrink(0).Margin(0).Column().AlignItems(ui.Stretch).
		Gap(2).Padding(8, 10).Radius(8).Disabled(disabled).Transition(capabilityTransition)
	fill := ui.Transparent
	switch {
	case row.Pressed():
		fill = t.Surface.Mix(t.Text, .14)
	case selected:
		fill = t.Surface.Mix(t.Text, .10)
	case row.Hovered() || row.Focused():
		fill = t.Surface.Mix(t.Text, .05)
	}
	row.Background(fill)
	row.Children(func() {
		ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(8).Children(func() {
			providerDot(c, ink)
			ui.Text(c, name).Grow(1).MinWidth(0).FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction).MaxLines(1).Ellipsis("…").TextColor(t.Text)
			ui.Text(c, state).Shrink(0).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(ink).Transition(capabilityTransition)
		})
		ui.Text(c, subtitle).MinWidth(0).Padding(0, 0, 0, 14).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
	})
	return appTooltip(c, row, name+"\n"+subtitle)
}

func capabilityBadge(c *ui.Context, t *ui.Theme, text string, ink ui.Color) ui.Element {
	return ui.Text(c, text).MinWidth(0).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(ink).
		Padding(2, 6).Radius(6).Background(t.Surface.Mix(ink, .08)).Transition(capabilityTransition)
}

func capabilityHeading(c *ui.Context, t *ui.Theme, title string, badges func()) {
	ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
		name := ui.Text(c, title).FontSize(15).FontWeight(600).FixedLineHeight(20).MinWidth(0).MaxLines(1).Ellipsis("…").TextColor(t.Text)
		appTooltip(c, name, title)
		if badges != nil {
			badges()
		}
	})
}

func capabilityBody(c *ui.Context, wide bool, scroll *ui.ScrollState, body func()) {
	content := func() { ui.Column(c).FillWidth().MinWidth(0).Gap(10).Padding(14, 18).Children(body) }
	if wide {
		ui.Scroll(c).FillWidth().MinWidth(0).Grow(1).MinHeight(0).TrackScroll(scroll).Children(content)
	} else {
		content()
	}
}

func capabilityFooter(c *ui.Context, t *ui.Theme, left, right func()) {
	footer := ui.Row(c).FillWidth().MinWidth(0).Shrink(0).Wrap().AlignItems(ui.Center).Justify(ui.SpaceBetween).
		Gap(8).Padding(12, 18).Background(t.Surface.Mix(t.Background, .45)).BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
	footer.Children(func() { left(); right() })
}

func capabilityEmpty(c *ui.Context, t *ui.Theme, text string) {
	ui.Text(c, text).FillWidth().MinWidth(0).Padding(16).Border(1, t.Border).Radius(12).Background(t.Surface).
		FontSize(typeLabel).FixedLineHeight(lineLabel).TextColor(t.TextMuted)
}

func capabilityText(c *ui.Context, t *ui.Theme, text string) ui.Element {
	return ui.Text(c, text).MinWidth(0).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
}

func capabilityAddButton(c *ui.Context, label string, primary, disabled bool, open func()) {
	button := secondaryButton
	if primary {
		button = primaryButton
	}
	element := button(c, "+ "+label).Label(label).Disabled(disabled)
	element.OnClick(func() { element.Focus(); open() })
}

func capabilityDialog(c *ui.Context, base *ui.Theme, key, title, hint string, blocked bool, scroll *ui.ScrollState, closeDialog func(), body, footer func(*ui.Theme)) {
	t := theme.SettingsTheme(base)
	width, height := c.Size()
	open := true
	c.Key(key)
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		theme.StyleFrostedModalBackdrop(backdrop, &t)
		panel.Label(title).Width(560).MaxWidth(width-32).MaxHeight(height-48).Padding(20).Gap(14).
			Background(t.Surface).Border(1, t.Border).Radius(14).Shadow(0, 8, 28, 0, ui.RGBA(0, 0, 0, .24))
		ui.Row(c).FillWidth().Shrink(0).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				pageHeading(c, title)
				capabilityText(c, &t, hint)
			})
			label := locales.In(c.Locale()).ComposerMaterialClose()
			closeButton := iconControlFace(c, ui.ButtonBase(c).Label(label), iconChrome, false).
				Size(32, 32).Shrink(0).Disabled(blocked).OnClick(closeDialog)
			appTooltip(c, closeButton, label).Children(func() { controlIcon(c, glyphCloseLine, iconChrome) })
		})
		ui.Scroll(c).FillWidth().MinWidth(0).MinHeight(0).Shrink(1).TrackScroll(scroll).Children(func() {
			ui.Column(c).FillWidth().MinWidth(0).AlignItems(ui.Start).Gap(10).Padding(2, 14, 2, 2).Children(func() { body(&t) })
		})
		bar := ui.Column(c).FillWidth().Shrink(0).Gap(6).Padding(12, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
		bar.Children(func() { footer(&t) })
	})
	if !open && !blocked {
		closeDialog()
	}
}

func capabilityLabel(c *ui.Context, t *ui.Theme, text string) {
	ui.Text(c, text).MinWidth(0).FontSize(typeGroupTitle).FontWeight(600).FixedLineHeight(lineLabel).TextColor(t.Text)
}

func skillSourceLabel(msgs locales.Messages, source string) string {
	switch source {
	case "global":
		return msgs.CapSkillGlobal()
	case "local":
		return msgs.CapSkillLocal()
	case "private":
		return msgs.CapSkillPrivate()
	default:
		return msgs.CapSkillWorkspace()
	}
}

func (a *appModel) skillLibrary(c *ui.Context, t *ui.Theme, wide bool) {
	s, msgs := &a.capabilities, locales.In(c.Locale())
	list := s.library.value
	canAdd := s.library.ready && !a.capabilitiesBlocked()
	summary := msgs.CapGlobalCount(len(list.Global))
	if s.discoverLocal {
		summary += " · " + msgs.CapDiscoveredCount(len(list.Local))
	}
	if !s.library.ready {
		summary = msgs.CapLoading()
	}
	ink := t.TextMuted
	if len(list.Global)+len(list.Local) != 0 {
		ink = t.Success
	}
	capabilityStrip(c, t, summary, ink, func() {
		ui.Row(c).MinWidth(0).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
			capabilityText(c, t, msgs.CapLocalDiscovery())
			selected := 0
			if s.discoverLocal {
				selected = 1
			}
			theme.PreferenceSegmentedSelection(c, &selected, msgs.CapLocalDiscovery(), !canAdd, false, msgs.CapOff(), msgs.CapOn()).OnChange(func() {
				a.setLocalSkillDiscovery(selected == 1)
			})
			capabilityAddButton(c, msgs.CapNewSkill(), len(list.Global) == 0 && s.newSkill == nil, !canAdd, func() {
				s.newSkill = &skillEditor{key: pi.ProductSkillKey{Source: "global"}}
				s.dialogScroll.Y = 0
			})
		})
	})
	s.library.failure.render(c, t, msgs.CapSkillsReadFailed())
	if s.library.failure.detail != "" {
		secondaryButton(c, msgs.MemoryRetry()).AlignSelf(ui.Start).OnClick(a.refreshCapabilityLibrary)
	}
	s.skillFailure.render(c, t, msgs.CapSkillChangeFailed())
	if len(list.Global)+len(list.Local) == 0 {
		if s.library.ready {
			capabilityEmpty(c, t, msgs.CapNoSkills())
		}
		return
	}
	capabilitySplit(c, t, wide, &s.listScroll[capabilityTabSkills], nil, func() {
		for _, rows := range [][]pi.ProductSkillSummary{list.Global, list.Local} {
			if len(rows) != 0 && len(list.Global) != 0 && len(list.Local) != 0 {
				label := msgs.CapSkillGlobal()
				if rows[0].Source == "local" {
					label = msgs.CapLocalGroup()
				}
				capabilityText(c, t, label).FontWeight(600).Padding(8, 10, 4)
			}
			for _, row := range rows {
				key := pi.ProductSkillKey{Source: row.Source, SkillID: row.SkillID}
				c.Key(key)
				state, ink := msgs.CapSkillText(), t.TextMuted
				if row.Source == "local" {
					state = msgs.CapReadOnly()
				} else if len(row.Scripts) != 0 {
					state, ink = msgs.CapExecutable(), t.Success
				}
				item := a.capabilityListRow(c, t, row.Name, row.Description, state, ink, key == s.selectedSkill, a.capabilitiesBusy()).OnClick(func() { a.selectLibrarySkill(row) })
				if key == s.focusSkill {
					item.Focus().ScrollIntoView()
					s.focusSkill = pi.ProductSkillKey{}
				}
			}
		}
	}, func() { a.librarySkillDetail(c, t, wide) })
}

func (a *appModel) librarySkillDetail(c *ui.Context, t *ui.Theme, wide bool) {
	s, msgs := &a.capabilities, locales.In(c.Locale())
	row, ok := librarySkill(s.library.value, s.selectedSkill)
	if !ok {
		return
	}
	c.Key(s.selectedSkill)
	read := s.skillDetails[s.selectedSkill]
	ready := read != nil && read.ready && read.value.SHA256 == row.SHA256
	global := row.Source == "global"
	blocked := a.capabilitiesBlocked()
	header := ui.Row(c).FillWidth().MinWidth(0).Shrink(0).Wrap().AlignItems(ui.Center).Gap(8).Padding(14, 18).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
	header.Children(func() {
		ui.Column(c).Basis(180).Grow(1).MinWidth(0).Children(func() {
			capabilityHeading(c, t, row.Name, func() {
				source := msgs.CapSkillGlobal()
				if !global {
					source = msgs.CapLocalReadOnly()
				}
				capabilityBadge(c, t, source, t.TextMuted)
				if len(row.Scripts) != 0 {
					capabilityBadge(c, t, msgs.CapExecutable(), t.Success)
				}
			})
		})
		if global {
			selected := 0
			if s.skillEdit != nil {
				selected = 1
			}
			theme.PreferenceSegmentedSelection(c, &selected, msgs.CapSkillView(), !ready || blocked, false, msgs.CapPreview(), msgs.CapEdit()).OnChange(func() {
				s.skillEdit = nil
				if selected == 1 {
					detail := read.value
					s.skillEdit = &skillEditor{key: s.selectedSkill, sha: detail.SHA256, name: detail.Name, summary: detail.Description, body: detail.Content}
				}
				s.detailScroll[capabilityTabSkills].Y = 0
			})
		}
	})
	capabilityBody(c, wide, &s.detailScroll[capabilityTabSkills], func() {
		if read != nil && read.failure.detail != "" {
			read.failure.render(c, t, msgs.CapSkillReadFailed())
			secondaryButton(c, msgs.MemoryRetry()).AlignSelf(ui.Start).OnClick(func() { a.selectLibrarySkill(row) })
		} else if !ready {
			capabilityText(c, t, msgs.CapLoading())
		} else if s.skillEdit != nil {
			a.skillForm(c, t, s.skillEdit, false)
		} else {
			capabilityText(c, t, row.Description)
			markdownReader(c, read.value.Content, markdownReaderOptions{CopyLabel: msgs.CopyCodeLabel()}).MaxWidthPercent(100).MinWidth(0)
		}
		if ready && len(read.value.Scripts) != 0 {
			skillScriptList(c, t, msgs, read.value, true)
			linkButton(c, msgs.CapInspectFiles()).AlignSelf(ui.Start).Expanded(s.skillFiles).OnClick(func() { s.skillFiles = !s.skillFiles })
			if s.skillFiles {
				for _, name := range slices.Sorted(maps.Keys(read.value.Files)) {
					c.Key(name)
					capabilityLabel(c, t, name)
					capabilityCode(c, msgs, read.value.Files[name])
				}
			}
		}
	})
	if global {
		capabilityFooter(c, t, func() {
			dangerButton(c, msgs.CapDeleteSkill()).Disabled(blocked).OnClick(func() { a.deleteCapabilitySkill(s.selectedSkill, &s.skillFailure) })
		}, func() {
			if s.skillEdit != nil {
				button := primaryButton
				if s.newSkill != nil {
					button = secondaryButton
				}
				button(c, msgs.CapSaveSkill()).Disabled(blocked || !ready).OnClick(func() { a.saveLibrarySkill(s.skillEdit) })
			}
		})
	}
}

func (a *appModel) deleteCapabilitySkill(key pi.ProductSkillKey, failure *capabilityNotice) {
	a.runCapabilityRPC(func(ctx context.Context, client *pi.ProductClient) error {
		_, err := client.DeleteSkill(ctx, pi.ProductSkillDeleteParams{ProductSkillKey: key, OperationID: newContactID()})
		return err
	}, func(err error) {
		*failure = a.capabilityFailure(err)
		if err == nil {
			delete(a.capabilities.skillDetails, key)
			a.refreshCapabilitiesSkills()
		}
	})
}

func skillSlug(name string) string {
	var slug strings.Builder
	separator := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if separator && slug.Len() != 0 && slug.Len() < 62 {
				slug.WriteByte('-')
			}
			slug.WriteByte(byte(r))
			separator = false
		} else {
			separator = true
		}
		if slug.Len() >= 63 {
			break
		}
	}
	return slug.String()
}

func capabilityInput(c *ui.Context, t *ui.Theme, label, key string, value *string, fields map[string]string, disabled bool) ui.Element {
	c.Key(key)
	return preferenceInput(c, value, fields[key] != "", t.Focus).FillWidth().MinWidth(0).Label(label).Disabled(disabled)
}

func capabilityFieldError(c *ui.Context, t *ui.Theme, problem string) {
	if problem != "" {
		capabilityText(c, t, problem).TextColor(t.Danger)
	}
}

func (a *appModel) skillForm(c *ui.Context, t *ui.Theme, e *skillEditor, creating bool) {
	msgs, blocked := locales.In(c.Locale()), a.capabilitiesBlocked()
	providerFormRow(c, t, msgs.FieldName(), func() {
		capabilityInput(c, t, msgs.FieldName(), "name", &e.name, e.fields, blocked).OnChange(func() {
			delete(e.fields, "name")
			if creating && !e.slugEdited {
				e.key.SkillID = skillSlug(e.name)
				delete(e.fields, "slug")
			}
		})
		capabilityFieldError(c, t, e.fields["name"])
	})
	if creating {
		providerFormRow(c, t, msgs.CapSkillSlug(), func() {
			capabilityInput(c, t, msgs.CapSkillSlug(), "slug", &e.key.SkillID, e.fields, blocked).OnChange(func() { e.slugEdited = true; delete(e.fields, "slug") })
			capabilityFieldError(c, t, e.fields["slug"])
		})
	}
	providerFormRow(c, t, msgs.CapDescription(), func() {
		capabilityInput(c, t, msgs.CapDescription(), "summary", &e.summary, e.fields, blocked).OnChange(func() { delete(e.fields, "summary") })
		capabilityFieldError(c, t, e.fields["summary"])
	})
	providerFormRow(c, t, msgs.CapInstructions(), func() {
		c.Key("skill-body")
		formTextArea(c, &e.body, e.fields["body"] != "", t.Focus).Lines(6, 16).FillWidth().MinWidth(0).Label(msgs.CapInstructions()).Disabled(blocked).
			OnChange(func() { delete(e.fields, "body") })
		capabilityFieldError(c, t, e.fields["body"])
	})
	e.failure.render(c, t, msgs.CapSkillChangeFailed())
}

func (a *appModel) capabilityDialogs(c *ui.Context, t *ui.Theme) {
	s, msgs := &a.capabilities, locales.In(c.Locale())
	if e := s.newSkill; e != nil {
		closeDialog := func() { s.newSkill = nil }
		capabilityDialog(c, t, "new-skill", msgs.CapNewSkill(), msgs.CapNewSkillHint(), s.busy, &s.dialogScroll, closeDialog,
			func(t *ui.Theme) { a.skillForm(c, t, e, true) }, func(t *ui.Theme) {
				a.capabilityDialogActions(c, msgs, msgs.CapSaveSkill(), closeDialog, func() { a.saveLibrarySkill(e) })
			})
	}
	if e := s.newMcp; e != nil {
		closeDialog := func() { s.newMcp = nil }
		capabilityDialog(c, t, "add-mcp-server", msgs.CapAddMcp(), msgs.CapAddMcpHint(), s.busy, &s.dialogScroll, closeDialog,
			func(t *ui.Theme) { a.mcpForm(c, t, e, true) }, func(t *ui.Theme) {
				a.capabilityDialogActions(c, msgs, msgs.CapSaveMcp(), closeDialog, func() { a.saveCapabilityMcp(e) })
			})
	}
}

func (a *appModel) capabilityDialogActions(c *ui.Context, msgs locales.Messages, save string, cancel, apply func()) {
	ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
		theme.GhostButton(c, msgs.CreateBotCancel()).Disabled(a.capabilitiesBusy()).OnClick(cancel)
		primaryButton(c, save).Disabled(a.capabilitiesBlocked()).OnClick(apply)
	})
}

type mcpSecretDraft struct {
	key, value string
	clear      bool
	saved      bool
}

type mcpEditor struct {
	base       pi.ProductMcpServerView
	config     pi.ProductMcpServerConfig
	args       string
	env        string
	headers    string
	secretEnv  []mcpSecretDraft
	secretHTTP []mcpSecretDraft
	dirty      bool
	fields     map[string]string
	failure    capabilityNotice
	stale      bool
	test       *pi.ProductMcpTestResult
	testedAt   string
}

func newMcpEditor(server pi.ProductMcpServerView) *mcpEditor {
	e := &mcpEditor{base: server, config: server.ProductMcpServerConfig, args: strings.Join(server.Args, "\n")}
	for _, key := range slices.Sorted(maps.Keys(server.Env)) {
		e.env += key + "=" + server.Env[key] + "\n"
	}
	for _, key := range slices.Sorted(maps.Keys(server.Headers)) {
		e.headers += key + ": " + server.Headers[key] + "\n"
	}
	for _, key := range server.SecretEnvKeys {
		e.secretEnv = append(e.secretEnv, mcpSecretDraft{key: key, saved: true})
	}
	for _, key := range server.SecretHeaderKeys {
		e.secretHTTP = append(e.secretHTTP, mcpSecretDraft{key: key, saved: true})
	}
	return e
}

func (a *appModel) appendDraftMcpSecrets(keys []string) []string {
	appendEditor := func(e *mcpEditor) {
		if e == nil {
			return
		}
		for _, rows := range [][]mcpSecretDraft{e.secretEnv, e.secretHTTP} {
			for _, entry := range rows {
				if value := strings.TrimSpace(entry.value); value != "" {
					keys = append(keys, value)
					kind, token, _ := strings.Cut(value, " ")
					if token != "" && (strings.EqualFold(kind, "Bearer") || strings.EqualFold(kind, "Basic")) {
						keys = append(keys, strings.TrimSpace(token))
					}
				}
			}
		}
	}
	appendEditor(a.capabilities.newMcp)
	for _, editor := range a.capabilities.mcpEditors {
		appendEditor(editor)
	}
	return keys
}

func (a *appModel) refreshCapabilityServers() {
	s := &a.capabilities
	readCapability(a, &s.servers, func(ctx context.Context, client *pi.ProductClient) ([]pi.ProductMcpServerView, error) {
		return client.ListMcpServers(ctx)
	}, func(rows []pi.ProductMcpServerView) {
		for id, e := range s.mcpEditors {
			row, exists := capabilityServer(rows, id)
			if !exists {
				delete(s.mcpEditors, id)
			} else if e.base.Revision != row.Revision {
				if e.dirty {
					e.stale = true
				} else {
					s.mcpEditors[id] = newMcpEditor(row)
				}
			}
		}
		if row, ok := capabilityServer(rows, s.selectedMcp); ok {
			a.selectCapabilityServer(row)
		} else if len(rows) != 0 {
			a.selectCapabilityServer(rows[0])
		} else {
			s.selectedMcp = ""
		}
	})
}

func capabilityServer(rows []pi.ProductMcpServerView, id string) (pi.ProductMcpServerView, bool) {
	for _, row := range rows {
		if row.ServerID == id {
			return row, true
		}
	}
	return pi.ProductMcpServerView{}, false
}

func (a *appModel) selectCapabilityServer(row pi.ProductMcpServerView) {
	s := &a.capabilities
	if s.selectedMcp != row.ServerID {
		s.selectedMcp = row.ServerID
		s.detailScroll[capabilityTabMCP].Y = 0
	}
	if s.mcpEditors == nil {
		s.mcpEditors = make(map[string]*mcpEditor)
	}
	if s.mcpEditors[row.ServerID] == nil {
		s.mcpEditors[row.ServerID] = newMcpEditor(row)
	}
}

func (a *appModel) openMcpEditor() {
	s := &a.capabilities
	s.newMcp = newMcpEditor(pi.ProductMcpServerView{ProductMcpServerConfig: pi.ProductMcpServerConfig{
		ServerID: "mcp-" + newContactID(), Transport: "http", Isolation: "shared", Enabled: true,
	}})
	s.newMcp.secretHTTP = []mcpSecretDraft{{}}
	s.newMcp.secretEnv = []mcpSecretDraft{{}}
	s.dialogScroll.Y = 0
}

func (e *mcpEditor) changed(key string) {
	e.dirty = true
	e.test = nil
	e.failure = capabilityNotice{}
	delete(e.fields, key)
}

func mcpValues(text, separator string, pattern *regexp.Regexp, fold bool) (map[string]string, bool) {
	var values map[string]string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\r"), separator)
		key = strings.TrimSpace(key)
		if fold {
			key, value = strings.ToLower(key), strings.TrimSpace(value)
		}
		if !ok || !pattern.MatchString(key) || strings.ContainsAny(value, "\x00\r\n") || len(values) == 64 {
			return nil, false
		}
		if _, duplicate := values[key]; duplicate {
			return nil, false
		}
		if values == nil {
			values = make(map[string]string)
		}
		values[key] = value
	}
	return values, true
}

func mcpSecretInput(rows []mcpSecretDraft, plain map[string]string, pattern *regexp.Regexp, fold bool) ([]string, map[string]*string, bool) {
	var keys []string
	var patch map[string]*string
	for _, row := range rows {
		key := strings.TrimSpace(row.key)
		if fold {
			key = strings.ToLower(key)
		}
		if key == "" && strings.TrimSpace(row.value) == "" && !row.clear {
			continue
		}
		if _, collision := plain[key]; collision || !pattern.MatchString(key) || slices.Contains(keys, key) || len(keys) == 64 {
			return nil, nil, false
		}
		keys = append(keys, key)
		if row.clear || row.value != "" {
			if patch == nil {
				patch = make(map[string]*string)
			}
			if row.clear {
				patch[key] = nil
			} else if strings.TrimSpace(row.value) == "" || strings.ContainsAny(row.value, "\x00\r\n") {
				return nil, nil, false
			} else {
				patch[key] = &row.value
			}
		}
	}
	return keys, patch, true
}

func (e *mcpEditor) input(msgs locales.Messages) (pi.ProductMcpServerInput, *pi.ProductMcpSecrets, bool) {
	e.fields = make(map[string]string)
	input := pi.ProductMcpServerInput{ProductMcpServerConfig: pi.ProductMcpServerConfig{
		ServerID: e.config.ServerID, Name: strings.TrimSpace(e.config.Name), Transport: e.config.Transport,
		Isolation: e.config.Isolation, Enabled: e.config.Enabled,
	}, Revision: &e.base.Revision}
	if !mcpNamePattern.MatchString(input.Name) {
		e.fields["name"] = msgs.CapMcpNameInvalid()
	}
	secrets := &pi.ProductMcpSecrets{}
	var valid bool
	if input.Transport == "stdio" {
		input.Command, input.Cwd = strings.TrimSpace(e.config.Command), strings.TrimSpace(e.config.Cwd)
		if !filepath.IsAbs(input.Command) || strings.ContainsRune(input.Command, 0) {
			e.fields["command"] = msgs.CapAbsolutePath()
		}
		if input.Cwd != "" && (!filepath.IsAbs(input.Cwd) || strings.ContainsRune(input.Cwd, 0)) {
			e.fields["cwd"] = msgs.CapAbsolutePath()
		}
		for _, arg := range strings.Split(e.args, "\n") {
			if strings.ContainsRune(arg, 0) {
				e.fields["args"] = msgs.CapMcpArgumentsInvalid()
			}
			if arg != "" {
				input.Args = append(input.Args, strings.TrimSuffix(arg, "\r"))
			}
		}
		input.EnvKeys = e.config.EnvKeys
		input.Env, valid = mcpValues(e.env, "=", mcpEnvKeyPattern, false)
		if !valid {
			e.fields["env"] = msgs.CapMcpEnvInvalid()
		}
		input.SecretEnvKeys, secrets.Env, valid = mcpSecretInput(e.secretEnv, input.Env, mcpEnvKeyPattern, false)
	} else {
		input.URL = strings.TrimSpace(e.config.URL)
		address, err := url.Parse(input.URL)
		if err != nil || address.Hostname() == "" || address.User != nil || address.Scheme != "http" && address.Scheme != "https" {
			e.fields["url"] = msgs.CapMcpURLInvalid()
		}
		input.HeaderKeys = e.config.HeaderKeys
		input.Headers, valid = mcpValues(e.headers, ":", mcpHeaderKeyPattern, true)
		if !valid {
			e.fields["headers"] = msgs.CapMcpHeadersInvalid()
		}
		input.SecretHeaderKeys, secrets.Headers, valid = mcpSecretInput(e.secretHTTP, input.Headers, mcpHeaderKeyPattern, true)
	}
	if !valid {
		e.fields["secrets"] = msgs.CapMcpSecretsInvalid()
	}
	if len(secrets.Env)+len(secrets.Headers) == 0 {
		secrets = nil
	}
	return input, secrets, len(e.fields) == 0
}

func (a *appModel) testCapabilityMcp(e *mcpEditor) {
	params := pi.ProductMcpTestParams{ServerID: e.config.ServerID}
	if e.base.Revision == 0 || e.dirty {
		input, secrets, valid := e.input(locales.In(a.uiLocale))
		if !valid {
			return
		}
		params = pi.ProductMcpTestParams{Server: &input, Secrets: secrets}
	}
	var result pi.ProductMcpTestResult
	a.runCapabilityRPC(func(ctx context.Context, client *pi.ProductClient) error {
		var err error
		result, err = client.TestMcpServer(ctx, params)
		return err
	}, func(err error) {
		e.failure = a.capabilityFailure(err)
		e.test = nil
		if err == nil && result.OK {
			e.test, e.testedAt = &result, time.Now().Format(time.RFC3339)
		} else if err == nil {
			e.failure.detail = a.redactConnectionText(result.Error)
			if e.failure.detail == "" {
				e.failure.detail = locales.In(a.uiLocale).CapUnavailable()
			}
		}
		if params.ServerID != "" {
			a.refreshCapabilityServers()
		}
	})
}

func (a *appModel) saveCapabilityMcp(e *mcpEditor) {
	input, secrets, valid := e.input(locales.In(a.uiLocale))
	if !valid || e.stale {
		return
	}
	params := pi.ProductMcpSaveParams{Server: input, Secrets: secrets, OperationID: newContactID()}
	var saved pi.ProductMcpServerView
	a.runCapabilityRPC(func(ctx context.Context, client *pi.ProductClient) error {
		var err error
		saved, err = client.SaveMcpServer(ctx, params)
		return err
	}, func(err error) {
		e.failure = a.capabilityFailure(err)
		if err != nil {
			if capabilityErrorCode(err) == "stale_revision" {
				e.stale = true
				a.refreshCapabilityServers()
			}
			return
		}
		s := &a.capabilities
		a.selectCapabilityServer(saved)
		editor := newMcpEditor(saved)
		editor.test, editor.testedAt = e.test, e.testedAt
		s.mcpEditors[saved.ServerID] = editor
		for _, rows := range [][]mcpSecretDraft{e.secretEnv, e.secretHTTP} {
			for i := range rows {
				rows[i].value = ""
			}
		}
		s.focusMcp, s.newMcp = saved.ServerID, nil
		a.refreshCapabilityServers()
	})
}

func (a *appModel) deleteCapabilityMcp(id string) {
	a.runCapabilityRPC(func(ctx context.Context, client *pi.ProductClient) error {
		_, err := client.DeleteMcpServer(ctx, pi.ProductMcpDeleteParams{ServerID: id, OperationID: newContactID()})
		return err
	}, func(err error) {
		a.capabilities.mcpFailure = a.capabilityFailure(err)
		if err == nil {
			a.refreshCapabilityServers()
		}
	})
}

func (a *appModel) refreshMcpStatus() {
	var pools []pi.ProductMcpPool
	a.runCapabilityRPC(func(ctx context.Context, client *pi.ProductClient) error {
		var err error
		pools, err = client.McpStatus(ctx)
		return err
	}, func(err error) {
		s := &a.capabilities
		s.mcpFailure = a.capabilityFailure(err)
		if err == nil {
			for i := range s.servers.value {
				row := &s.servers.value[i]
				row.Pools = nil
				for _, pool := range pools {
					if pool.ServerID == row.ServerID {
						row.Pools = append(row.Pools, pool)
					}
				}
			}
		}
	})
}

func mcpHealth(msgs locales.Messages, t *ui.Theme, row pi.ProductMcpServerView) (string, ui.Color) {
	if !row.Enabled {
		return msgs.ProviderDisabled(), t.TextMuted
	}
	if row.CredentialStatus == "credential_missing" {
		return msgs.ProviderMissingKey(), t.Warning
	}
	if row.Catalog != nil && row.Catalog.Error != "" {
		return msgs.CapUnavailable(), t.Warning
	}
	return msgs.ProviderReady(), t.Success
}

func mcpIsolationLabel(msgs locales.Messages, isolation string) string {
	switch isolation {
	case "per_workspace":
		return msgs.CapPerWorkspace()
	case "per_bot":
		return msgs.CapPerBot()
	default:
		return msgs.CapShared()
	}
}

func (a *appModel) mcpSettings(c *ui.Context, t *ui.Theme, wide bool) {
	s, msgs := &a.capabilities, locales.In(c.Locale())
	rows := s.servers.value
	blocked := a.capabilitiesBlocked()
	enabled, pools := 0, 0
	for _, row := range rows {
		if row.Enabled {
			enabled++
		}
		pools += len(row.Pools)
	}
	summary, ink := msgs.CapNoMcp(), t.TextMuted
	if !s.servers.ready {
		summary = msgs.CapLoading()
	} else if len(rows) != 0 {
		summary = msgs.CapMcpEnabledCount(enabled) + " · " + msgs.CapMcpPoolCount(pools)
		if enabled > 0 {
			ink = t.Success
		}
	}
	capabilityStrip(c, t, summary, ink, func() {
		secondaryButton(c, msgs.CapRefreshStatus()).Disabled(blocked || !s.servers.ready).OnClick(a.refreshMcpStatus)
		if len(rows) == 0 {
			capabilityAddButton(c, msgs.CapAddMcp(), s.newMcp == nil, blocked || !s.servers.ready, a.openMcpEditor)
		}
	})
	s.servers.failure.render(c, t, msgs.CapMcpReadFailed())
	if s.servers.failure.detail != "" {
		secondaryButton(c, msgs.MemoryRetry()).AlignSelf(ui.Start).OnClick(a.refreshCapabilityServers)
	}
	s.mcpFailure.render(c, t, msgs.CapMcpChangeFailed())
	if len(rows) == 0 {
		return
	}
	capabilitySplit(c, t, wide, &s.listScroll[capabilityTabMCP], func() {
		header := ui.Row(c).FillWidth().Shrink(0).Gap(8).Padding(10, 12).AlignItems(ui.Center).Justify(ui.SpaceBetween).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
		header.Children(func() {
			capabilityText(c, t, msgs.CapMcp()).FontWeight(600)
			capabilityAddButton(c, msgs.ProviderAddShort(), false, blocked || len(rows) >= 64, a.openMcpEditor)
		})
	}, func() {
		for _, row := range rows {
			c.Key(row.ServerID)
			transport := "stdio"
			if row.Transport == "http" {
				transport = "HTTP"
			}
			count := 0
			if row.Catalog != nil {
				count = len(row.Catalog.Tools)
			}
			status, color := mcpHealth(msgs, t, row)
			subtitle := transport + " · " + mcpIsolationLabel(msgs, row.Isolation) + " · " + msgs.CapToolCount(count)
			item := a.capabilityListRow(c, t, row.Name, subtitle, status, color, s.selectedMcp == row.ServerID, s.busy).OnClick(func() { a.selectCapabilityServer(row) })
			if s.focusMcp == row.ServerID {
				item.Focus().ScrollIntoView()
				s.focusMcp = ""
			}
		}
	}, func() {
		if row, ok := capabilityServer(rows, s.selectedMcp); ok {
			a.mcpDetail(c, t, row, s.mcpEditors[row.ServerID], wide)
		}
	})
}

func (a *appModel) mcpDetail(c *ui.Context, t *ui.Theme, row pi.ProductMcpServerView, e *mcpEditor, wide bool) {
	s, msgs := &a.capabilities, locales.In(c.Locale())
	c.Key(row.ServerID)
	blocked := a.capabilitiesBlocked()
	header := ui.Row(c).FillWidth().MinWidth(0).Shrink(0).Wrap().AlignItems(ui.Center).Gap(8).Padding(14, 18).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
	header.Children(func() {
		ui.Column(c).Basis(144).Grow(1).MinWidth(0).Children(func() {
			capabilityHeading(c, t, row.Name, func() {
				status, ink := mcpHealth(msgs, t, row)
				if row.Enabled && e.test != nil && e.test.OK {
					status, ink = msgs.ProviderLatencyReady(strconv.FormatUint(e.test.LatencyMS, 10)), t.Success
				}
				capabilityBadge(c, t, status, ink)
			})
		})
		ui.Row(c).MinWidth(0).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
			selected := 0
			if !e.config.Enabled {
				selected = 1
			}
			theme.PreferenceSegmentedSelection(c, &selected, msgs.ProviderAccountStatus(), blocked, false,
				msgs.ProviderEnabled(), msgs.ProviderToggleDisabled()).OnChange(func() {
				e.config.Enabled = selected == 0
				e.changed("enabled")
			})
			secondaryButton(c, msgs.CapMcpTestRefresh()).Disabled(blocked || e.stale).OnClick(func() { a.testCapabilityMcp(e) })
		})
	})
	capabilityBody(c, wide, &s.detailScroll[capabilityTabMCP], func() {
		if e.stale {
			capabilityText(c, t, msgs.CapMcpChanged()).TextColor(t.Warning)
			secondaryButton(c, msgs.CapReloadServer()).AlignSelf(ui.Start).Disabled(blocked).OnClick(func() { s.mcpEditors[row.ServerID] = newMcpEditor(row) })
		}
		a.mcpForm(c, t, e, false)
		if e.test == nil && row.Catalog != nil {
			mcpCatalog(c, t, msgs, row.Catalog.Tools, row.Catalog.FetchedAt)
			if row.Catalog.Error != "" && e.failure.detail == "" {
				capabilityText(c, t, msgs.CapMcpTestFailed()).TextColor(t.Warning)
			}
		}
		mcpPools(c, t, msgs, row.Pools)
	})
	capabilityFooter(c, t, func() {
		dangerButton(c, msgs.CapDeleteMcp()).Disabled(blocked).OnClick(func() { a.deleteCapabilityMcp(row.ServerID) })
	}, func() {
		button := primaryButton
		if s.newMcp != nil {
			button = secondaryButton
		}
		button(c, msgs.CapSaveMcp()).Disabled(blocked || e.stale).OnClick(func() { a.saveCapabilityMcp(e) })
	})
}

func (a *appModel) mcpForm(c *ui.Context, t *ui.Theme, e *mcpEditor, adding bool) {
	msgs, blocked := locales.In(c.Locale()), a.capabilitiesBlocked()
	field := func(label, key string, value *string) {
		providerFormRow(c, t, label, func() {
			capabilityInput(c, t, label, key, value, e.fields, blocked).OnChange(func() { e.changed(key) })
			capabilityFieldError(c, t, e.fields[key])
		})
	}
	area := func(label, key, placeholder string, value *string) {
		providerFormRow(c, t, label, func() {
			c.Key(key)
			formTextArea(c, value, e.fields[key] != "", t.Focus).MinHeight(36).Lines(1, 5).FillWidth().MinWidth(0).Label(label).
				Placeholder(placeholder).Disabled(blocked).OnChange(func() { e.changed(key) })
			capabilityFieldError(c, t, e.fields[key])
		})
	}
	field(msgs.FieldName(), "name", &e.config.Name)
	providerFormRow(c, t, msgs.CapTransport(), func() {
		c.Key("mcp-transport")
		transports := [...]string{"stdio", "http"}
		selected := max(0, slices.Index(transports[:], e.config.Transport))
		theme.PreferenceSegmentedSelection(c, &selected, msgs.CapTransport(), blocked, false, "stdio", msgs.CapStreamableHTTP()).OnChange(func() {
			e.config.Transport = transports[selected]
			e.fields = nil
			e.changed("transport")
		})
	})
	providerFormRow(c, t, msgs.CapIsolation(), func() {
		c.Key("mcp-isolation")
		isolations := [...]string{"shared", "per_workspace", "per_bot"}
		selected := max(0, slices.Index(isolations[:], e.config.Isolation))
		theme.PreferenceSegmentedSelection(c, &selected, msgs.CapIsolation(), blocked, false, msgs.CapShared(), msgs.CapPerWorkspace(), msgs.CapPerBot()).OnChange(func() {
			e.config.Isolation = isolations[selected]
			e.changed("isolation")
		})
		capabilityText(c, t, msgs.CapIsolationHint())
	})
	if e.config.Transport == "stdio" {
		field(msgs.CapCommand(), "command", &e.config.Command)
		area(msgs.CapArguments(), "args", msgs.CapArgumentsHint(), &e.args)
		if !adding {
			field(msgs.ConnectionFieldWorkingDirectory(), "cwd", &e.config.Cwd)
			area(msgs.CapEnvironment(), "env", "KEY=VALUE", &e.env)
		}
		mcpSecretFields(c, t, msgs, e, &e.secretEnv, msgs.CapSecretEnv(), blocked)
	} else {
		field(msgs.CapServerURL(), "url", &e.config.URL)
		if !adding {
			area(msgs.CapHeaders(), "headers", "Header: Value", &e.headers)
		}
		mcpSecretFields(c, t, msgs, e, &e.secretHTTP, msgs.CapSecretHeader(), blocked)
	}
	if adding {
		secondaryButton(c, msgs.CapTestConnection()).AlignSelf(ui.Start).Disabled(blocked).OnClick(func() { a.testCapabilityMcp(e) })
	}
	if a.capabilitiesBusy() {
		capabilityText(c, t, msgs.ProviderWorking()).Role(ui.RoleStatus)
	}
	e.failure.render(c, t, msgs.CapMcpChangeFailed())
	if e.test != nil && e.test.OK {
		if adding {
			capabilityText(c, t, msgs.CapMcpProbeResult(len(e.test.Tools), strconv.FormatUint(e.test.LatencyMS, 10))).TextColor(t.Success)
		}
		mcpCatalog(c, t, msgs, e.test.Tools, e.testedAt)
	}
}

func mcpSecretFields(c *ui.Context, t *ui.Theme, msgs locales.Messages, e *mcpEditor, rows *[]mcpSecretDraft, label string, blocked bool) {
	providerFormRow(c, t, label, func() {
		for i := range *rows {
			c.Key(i)
			row := &(*rows)[i]
			ui.Column(c).FillWidth().MinWidth(0).Gap(4).Children(func() {
				if row.saved {
					capabilityText(c, t, row.key).MaxLines(1).Ellipsis("…")
				} else {
					capabilityInput(c, t, label, "secret-name", &row.key, nil, blocked).OnChange(func() { e.changed("secrets") })
				}
				ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
					placeholder := msgs.CapSecretValue()
					if row.saved && e.base.CredentialStatus == "ok" {
						placeholder = msgs.ProviderKeepKey()
					}
					value := preferenceInput(c, &row.value, e.fields["secrets"] != "", t.Focus).Basis(140).Grow(1).MinWidth(0).
						Label(msgs.CapSecretValue()).Placeholder(placeholder).Password().Disabled(blocked || row.clear)
					value.OnChange(func() { e.changed("secrets") })
					if row.saved {
						ui.Checkbox(c, &row.clear, msgs.CapClear()).Disabled(blocked).OnChange(func() {
							if row.clear {
								row.value = ""
							}
							e.changed("secrets")
						})
					} else {
						theme.GhostButton(c, msgs.CapRemove()).Disabled(blocked).OnClick(func() {
							*rows = slices.Delete(*rows, i, i+1)
							e.changed("secrets")
						})
					}
				})
			})
		}
		linkButton(c, msgs.CapAddSecret()).Padding(4, 0).Disabled(blocked || len(*rows) >= 64).OnClick(func() { *rows = append(*rows, mcpSecretDraft{}) })
		capabilityFieldError(c, t, e.fields["secrets"])
	})
}

func mcpToolHint(t *ui.Theme, msgs locales.Messages, tool pi.ProductMcpTool) (string, ui.Color) {
	if tool.DestructiveHint != nil && *tool.DestructiveHint {
		return msgs.CapDestructive(), t.Danger
	}
	if tool.ReadOnlyHint != nil && *tool.ReadOnlyHint {
		return msgs.CapReadOnly(), t.Success
	}
	return msgs.CapMutating(), t.TextMuted
}

func mcpCatalog(c *ui.Context, t *ui.Theme, msgs locales.Messages, tools []pi.ProductMcpTool, fetchedAt string) {
	capabilityLabel(c, t, msgs.CapToolCatalog())
	caption := msgs.CapToolCount(len(tools))
	if stamp, err := time.Parse(time.RFC3339Nano, fetchedAt); err == nil {
		caption += " · " + i18n.FormatDate(c.Locale(), stamp.Local(), i18n.DateOptions{Date: i18n.ShortStyle, Time: i18n.ShortStyle})
	}
	capabilityText(c, t, caption)
	for _, tool := range tools {
		c.Key(tool.Name)
		row := ui.Column(c).FillWidth().MinWidth(0).Gap(4).Padding(8, 0).BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
		row.Children(func() {
			ui.Row(c).FillWidth().MinWidth(0).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
				name := ui.Text(c, tool.Name).Font("monospace").FontSize(typeAction).FontWeight(500).MinWidth(0).MaxLines(1).Ellipsis("…")
				appTooltip(c, name, tool.Name)
				hint, ink := mcpToolHint(t, msgs, tool)
				capabilityBadge(c, t, hint, ink)
			})
			if tool.Description != "" {
				appTooltip(c, capabilityText(c, t, tool.Description).MaxLines(2).Ellipsis("…"), tool.Description)
			}
		})
	}
}

func mcpPools(c *ui.Context, t *ui.Theme, msgs locales.Messages, pools []pi.ProductMcpPool) {
	if len(pools) == 0 {
		return
	}
	capabilityLabel(c, t, msgs.CapLivePools())
	for _, pool := range pools {
		c.Key(pool.ScopeKey)
		row := ui.Column(c).FillWidth().MinWidth(0).Gap(4).Padding(8, 0).BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
		row.Children(func() {
			state, color := msgs.CapPoolConnecting(), t.TextMuted
			switch pool.State {
			case "connected":
				state, color = msgs.CapPoolConnected(), t.Success
			case "closing":
				state = msgs.CapPoolClosing()
			}
			ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
				capabilityText(c, t, mcpIsolationLabel(msgs, pool.Isolation))
				capabilityBadge(c, t, state, color)
				capabilityText(c, t, msgs.CapPoolReferences(int(pool.RefCount)))
				if pool.PID != nil {
					capabilityText(c, t, msgs.CapPoolPID(strconv.Itoa(*pool.PID)))
				}
			})
			appTooltip(c, capabilityText(c, t, pool.ScopeKey).MaxLines(1).Ellipsis("…"), pool.ScopeKey)
		})
	}
}
