package theme

import (
	"math"

	"github.com/egoist/mygo/ui"
)

const (
	AppearanceSystem = "system"
	AppearanceLight  = "light"
	AppearanceDark   = "dark"
)

const (
	DefaultLeftPaneWidth  float32 = 272
	MinLeftPaneWidth      float32 = 180
	MaxLeftPaneWidth      float32 = 420
	DefaultRightPaneWidth float32 = 340
	MinRightPaneWidth     float32 = 280
	MaxRightPaneWidth     float32 = 440
	MinCenterPaneWidth    float32 = 480
	PaneSeparatorWidth    float32 = 1

	AvatarRailWidth      float32 = 56
	FullRailBreakpoint   int     = 1180
	AvatarRailBreakpoint int     = 900
	MinAvatarDetailWidth float32 = 300
	EffectiveCenterWidth float32 = 516
)

// WorkbenchTheme resolves the Qoder reference palette and product surfaces.
// CSS roles use the adopted rem16/scale1 profile, independent of Theme.Rem.
const (
	PreferenceSelectMinWidth float32 = 120
	PreferenceSelectMaxWidth float32 = 160
	LineBody                 float32 = 24
	CSSRem                   float32 = 16
	ControlHeight            float32 = 28
	ControlHit               float32 = 32
	TypeSettingsTitle        float32 = 22
	LineSettingsTitle        float32 = 28
	LineCompact              float32 = 22
	LineForm                 float32 = 20
	TypeHeaderMeta           float32 = 12
	LineHeader               float32 = 16
	MenuCompactRadius        float32 = 6
	MenuCompactPadding       float32 = 6
	MenuCompactGap           float32 = 6
	GlyphMenu                float32 = 14
	ConversationMaxWidth     float32 = 768
	UserMessageMaxWidth      float32 = 752
	TypeLabel                float32 = 14
	LineLabel                float32 = 20
	LineMeta                 float32 = 17
	LineAction               float32 = 16
	TypeNavigation           float32 = 12
	LineNavigation           float32 = 16
	SettingsSidebarWidth     float32 = 248
	SettingsContentWidth     float32 = 820
	PreferenceBreakpoint     float32 = 40 * CSSRem
	TypeContactTitle         float32 = 13
	TypePageTitle            float32 = TypeSettingsTitle
	TypeGroupTitle           float32 = 13
	LineGroupTitle           float32 = 18
	TypeBody                 float32 = 14
	TypeAction               float32 = 13
	TypeMeta                 float32 = 12
	TypeRailName             float32 = 12
	TypeRailSummary          float32 = 12
)

type WorkbenchTheme struct {
	Theme           *ui.Theme
	Link            ui.Color
	InputBorder     ui.Color
	ChromeFrom      ui.Color
	ChromeTo        ui.Color
	Tertiary        ui.Color
	NavHover        ui.Color
	NavSelected     ui.Color
	Rail            ui.Color
	Reading         ui.Color
	Panel           ui.Color
	Well            ui.Color
	BubbleUser      ui.Color
	BubbleContact   ui.Color
	ContactText     ui.Color
	ContactMuted    ui.Color
	ContactLink     ui.Color
	ComposerSurface ui.Color
	ComposerBorder  ui.Color
	ComposerChipBg  ui.Color
	ComposerText    ui.Color
	ComposerMuted   ui.Color
	WorkSurface     ui.Color
	WorkText        ui.Color
	WorkMuted       ui.Color
	WorkAttention   ui.Color
	WorkWarningBg   ui.Color
}

func ResolveWorkbenchTheme(dark bool) WorkbenchTheme {
	t := ui.LightTheme()
	if dark {
		t = ui.DarkTheme()
	}
	t.FontSize, t.Font, t.Spacing, t.Radius = TypeAction, "system-ui", 4, 8
	t.Background = ui.Hex("#FFFFFF")
	t.Surface = ui.Hex("#FFFFFF")
	t.SurfaceHover = ui.Hex("#EFEFEF")
	t.SurfacePressed = ui.Hex("#DFDFDF")
	t.Border = ui.Hex("#E6E6E6")
	t.Text = ui.Hex("#141414")
	t.TextMuted = ui.Hex("#636261")
	t.Accent = ui.Hex("#0D0D0D")
	t.AccentHover = ui.Hex("#2B2B2A")
	t.AccentPressed = ui.Hex("#3A3A38")
	t.AccentText = ui.Hex("#FDFDFD")
	t.Selection = ui.Hex("#EFEFEF")
	t.Focus = ui.Hex("#BCBBBA")
	t.Danger = ui.Hex("#B4232C")
	t.Warning = ui.Hex("#8A5300")
	t.Success = ui.Hex("#2E6B45")
	t.Scrollbar, t.ScrollbarWidth = ui.Hex("#DFDFDF"), 6
	palette := WorkbenchTheme{
		Theme: t,
		Link:  ui.Hex("#2D6A4A"), InputBorder: ui.Hex("#8F8E8B"),
		ChromeFrom: ui.Hex("#F2F2E2"), ChromeTo: ui.Hex("#E6EBEF"), Tertiary: ui.Hex("#838280"),
		NavHover: ui.Hex("#0000000A"), NavSelected: ui.Hex("#00000012"),
		Rail: ui.Hex("#F2F2E2"), Reading: t.Background, Panel: t.Background,
		Well: t.Surface, BubbleUser: ui.Hex("#F2F4F2"), BubbleContact: ui.Hex("#F3F3EF"),
		ComposerSurface: ui.Hex("#FFFFFF"), ComposerBorder: ui.Hex("#E2E2DE"), ComposerChipBg: ui.Hex("#F4F4F0"),
		WorkSurface: t.Background, WorkText: t.Text, WorkMuted: t.TextMuted,
		WorkAttention: t.Warning, WorkWarningBg: ui.Hex("#FEFBE8"),
	}
	if dark {
		t.Background, t.Surface = ui.Hex("#171814"), ui.Hex("#171814")
		t.SurfaceHover, t.SurfacePressed = ui.Hex("#2C2E28"), ui.Hex("#363830")
		t.Border, t.Text, t.TextMuted = ui.Hex("#30322D"), ui.Hex("#F2F1E8"), ui.Hex("#C1BFB1")
		t.Accent, t.AccentHover, t.AccentPressed = ui.Hex("#E0C65C"), ui.Hex("#F0DA78"), ui.Hex("#E8D06E")
		t.AccentText, t.Selection, t.Focus = ui.Hex("#171407"), t.SurfaceHover, ui.Hex("#47483F")
		t.Danger, t.Warning, t.Success, t.Scrollbar = ui.Hex("#FF4D4F"), ui.Hex("#FAAD14"), ui.Hex("#73CD94"), ui.Hex("#363830")
		palette.ChromeFrom, palette.ChromeTo, palette.Rail = ui.Hex("#10110E"), ui.Hex("#10110E"), ui.Hex("#10110E")
		palette.Tertiary = ui.Hex("#89877A")
		palette.Link, palette.InputBorder = ui.Hex("#E0C65C"), ui.Hex("#76776C")
		palette.NavHover, palette.NavSelected = ui.Hex("#3D3F39"), ui.Hex("#454741")
		palette.Reading, palette.Panel, palette.Well = t.Background, t.Background, t.Surface
		palette.BubbleUser, palette.BubbleContact = ui.Hex("#2E2916"), ui.Hex("#22241F")
		palette.ComposerSurface, palette.ComposerBorder, palette.ComposerChipBg = ui.Hex("#1E201B"), ui.Hex("#30322D"), ui.Hex("#282A24")
		palette.WorkSurface, palette.WorkText, palette.WorkMuted = t.Background, t.Text, t.TextMuted
		palette.WorkAttention, palette.WorkWarningBg = t.Warning, ui.Hex("#4A4019")
	}
	palette.ContactText, palette.ContactMuted, palette.ContactLink = t.Text, t.TextMuted, palette.Link
	palette.ComposerText, palette.ComposerMuted = t.Text, t.TextMuted
	return palette
}

func SettingsTheme(base *ui.Theme) ui.Theme {
	t := *base
	ink, shade := ui.RGB(0, 0, 0), ui.RGB(255, 255, 255)
	if t.Dark {
		ink, shade = shade, ink
	}
	switch {
	case t.Dark && t.Background == ui.Hex("#171814") && t.Surface == t.Background:
		t.Background, t.Surface, t.Border = ui.Hex("#141512"), ui.Hex("#1C1E19"), ui.Hex("#2B2D28")
	case !t.Dark && t.Background == ui.Hex("#FFFFFF") && t.Surface == t.Background:
		t.Background, t.Surface, t.Border = ui.Hex("#F6F6F3"), ui.Hex("#FFFFFF"), ui.Hex("#E4E4E0")
	default:
		if t.Dark {
			t.Surface = t.Surface.Mix(ink, .04)
		}
		for PaletteContrast(ink, t.Surface.Mix(ink, .10)) < 4.5 {
			t.Surface = t.Surface.Mix(shade, .18)
		}
		t.Background = t.Surface.Mix(ui.RGB(0, 0, 0), .06)
		t.SurfaceHover, t.SurfacePressed = t.Surface.Mix(ink, .06), t.Surface.Mix(ink, .10)
		t.Selection = t.SurfaceHover
		t.Border = t.Surface.Mix(ink, .11)
	}
	for _, color := range []*ui.Color{&t.Text, &t.TextMuted, &t.Success, &t.Warning, &t.Danger} {
		*color = contrastTextColor(*color, ink, t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed)
	}
	if PaletteContrast(t.AccentText, t.Accent) < 4.5 {
		if black := ui.RGB(0, 0, 0); PaletteContrast(black, t.Accent) >= 4.5 {
			t.AccentText = black
		} else {
			t.AccentText = ui.RGB(255, 255, 255)
		}
	}
	for _, color := range []*ui.Color{&t.AccentHover, &t.AccentPressed} {
		if PaletteContrast(t.AccentText, *color) < 4.5 {
			*color = t.Accent
		}
	}
	return t
}

func contrastTextColor(color, ink ui.Color, backgrounds ...ui.Color) ui.Color {
	for _, background := range backgrounds {
		if PaletteContrast(color, background) < 4.5 {
			mixed := color.Mix(ink, .25)
			if mixed == color {
				return ink
			}
			return contrastTextColor(mixed, ink, backgrounds...)
		}
	}
	return color
}

func PaletteLuminance(c ui.Color) float64 {
	linear := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.04045 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(c.R) + 0.7152*linear(c.G) + 0.0722*linear(c.B)
}

func PaletteContrast(a, b ui.Color) float64 {
	x, y := PaletteLuminance(a), PaletteLuminance(b)
	if x < y {
		x, y = y, x
	}
	return (x + 0.05) / (y + 0.05)
}

var contactAvatarSwatches = [...]struct{ light, dark ui.Color }{
	{ui.Hex("#CADDEB"), ui.Hex("#2E4961")},
	{ui.Hex("#D9D1EA"), ui.Hex("#4B3B61")},
	{ui.Hex("#E9CDD4"), ui.Hex("#633B4B")},
	{ui.Hex("#EED7BE"), ui.Hex("#634528")},
	{ui.Hex("#C6E1DB"), ui.Hex("#284E4A")},
	{ui.Hex("#DDE5C1"), ui.Hex("#47532F")},
	{ui.Hex("#E4CFBE"), ui.Hex("#5B4338")},
	{ui.Hex("#CBD2EE"), ui.Hex("#3B456B")},
}

func ContactAvatarColors(t *ui.Theme, name string) (background, ink ui.Color) {
	hash := uint32(2166136261)
	for i := 0; i < len(name); i++ {
		hash = (hash ^ uint32(name[i])) * 16777619
	}
	swatch := contactAvatarSwatches[(hash^(hash>>16))%uint32(len(contactAvatarSwatches))]
	if t.Dark {
		return swatch.dark, ui.RGB(242, 241, 232)
	}
	return swatch.light, ui.RGB(20, 20, 20)
}

// SegmentedSelection keeps MyGo's official SegmentedBase radio group,
// keyboard semantics and Changed contract, with the app's restrained
// Selection/Text face instead of the framework pressedColor and shadow.
func SegmentedSelection(c *ui.Context, selected *int, labels ...string) ui.Element {
	return segmentedSelection(c, selected, false, false, labels)
}

func PreferenceSegmentedSelection(c *ui.Context, selected *int, label string, disabled, focus bool, labels ...string) ui.Element {
	return segmentedSelection(c, selected, disabled, focus, labels).Label(label)
}

func segmentedSelection(c *ui.Context, selected *int, disabled, focus bool, labels []string) ui.Element {
	t := c.Theme()
	initial := *selected
	parts := ui.SegmentedBase(c, selected, len(labels))
	if initial == -1 {
		*selected = initial
	}
	track := parts.Track.MinHeight(32).AlignItems(ui.Stretch).Padding(t.Space(0.5)).Gap(t.Space(0.5)).
		Radius(t.Radius).Background(t.Surface).Border(1, t.Border)
	track.Children(func() {
		for i, label := range labels {
			segment := parts.Segment(i).Label(label).Disabled(disabled).MinWidth(ControlHit).MinHeight(ControlHit).Padding(t.Space(1), t.Space(2)).
				Radius(t.Radius).Background(ui.Transparent).Border(2, ui.Transparent).FocusRing(false).TextColor(t.TextMuted)
			if i == *selected {
				segment.Background(t.Selection).TextColor(t.Text)
			}
			if segment.Pressed() {
				segment.Background(t.SurfacePressed)
			} else if segment.Hovered() && i != *selected {
				if t.Dark {
					segment.Background(ui.RGBA(255, 255, 255, 14.0/255))
				} else {
					segment.Background(ui.RGBA(0, 0, 0, 10.0/255))
				}
			}
			if focus && !disabled && i == max(0, *selected) {
				segment.Focus().ScrollIntoView()
			}
			if segment.FocusVisible() {
				segment.BorderColor(t.Accent)
			}
			segment.Children(func() {
				ui.Text(c, label).FontSize(TypeAction).FontWeight(500).FixedLineHeight(LineAction).SingleLine()
			})
		}
	})
	return track
}

// SelectionButtonState gives a secondary button a Selection/Text selected
// face while preserving explicit hover, pressed and focus feedback.
func SelectionButtonState(c *ui.Context, button ui.Element, selected bool) {
	t := c.Theme()
	if selected {
		button.Background(t.Selection).TextColor(t.Text)
	}
	if button.Pressed() {
		button.Background(t.SurfacePressed)
	} else if button.Hovered() {
		button.Background(t.SurfaceHover)
	}
	if button.Focused() {
		button.BorderWidth(1).BorderColor(t.Focus)
	}
}

// SelectOption represents one value-label pair in PreferenceSelect.
type SelectOption struct {
	Value string
	Label string
}

// PreferenceSelect retains the official popup, stable values and keyboard controller.
func PreferenceSelect(c *ui.Context, selected *string, options []SelectOption, label string, disabled ...bool) ui.Element {
	t := c.Theme()
	blocked := len(disabled) > 0 && disabled[0]
	display := ""
	for _, option := range options {
		if option.Value == *selected {
			display = option.Label
			break
		}
	}
	full := label
	if display != "" {
		full += ": " + display
	}
	sel := ui.SelectBase(c, selected)
	trigger := SharedControlFace(c, sel.Trigger.Label(label).Value(display).Disabled(blocked), 16, 4, true, false, false).
		FillWidth().MinWidth(PreferenceSelectMinWidth).MaxWidth(PreferenceSelectMaxWidth).
		Justify(ui.Start).Padding(8, 10, 8, 12).FontWeight(500)
	trigger.Children(func() {
		ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, display).FontSize(TypeAction).FontWeight(500).FixedLineHeight(LineAction).
				MaxLines(1).Grow(1).Shrink(1).MinWidth(0).Ellipsis("…").TextColor(t.Text)
			ui.Icon(c, GlyphSelectChevron).Width(16).Height(16).Shrink(0).TextColor(t.TextMuted)
		})
	})
	sel.Popup(func(panel ui.Element) {
		panel.Padding(4).Radius(0).Background(ui.Transparent).Border(0, ui.Transparent).Draw(func(p *ui.Painter, r ui.Rect) { PaintPreferenceFace(p, r, 8, 2, t.Background, t.Border) })
		ui.Scroll(c).MaxHeight(t.Space(80)).Children(func() {
			for _, option := range options {
				item := sel.Item(option.Value).Disabled(blocked)
				item = SharedControlFace(c, item, 6, 2, false, false, item.Highlighted() || option.Value == *selected).
					Padding(8, 6).FontSize(TypeMeta).FixedLineHeight(LineAction).TextColor(t.Text)
				item.Children(func() { ui.Text(c, option.Label).FontSize(TypeMeta).FixedLineHeight(LineAction).SingleLine() })
			}
		})
	})
	trigger.OnChange(func() { trigger.Focus() })
	return AppTooltip(c, trigger, full)
}

// AppTooltip keeps MyGo's official tooltip controller but gives product tips
// the app's supporting size and flat semantic colors. The anchor's Description
// remains the accessible source; TooltipBase owns hover, keyboard, press and
// Escape dismissal.
func AppTooltip(c *ui.Context, anchor ui.Element, text string) ui.Element {
	if text == "" {
		return anchor
	}
	anchor.Description(text)
	t := c.Theme()
	ui.TooltipBase(c, anchor, func(tip ui.Element) {
		tip.MaxWidth(t.Space(80)).Padding(t.Space(1.25), t.Space(2)).Radius(t.Space(1.25)).
			Background(t.Text).TextColor(t.Background).FontSize(TypeMeta)
		tip.Children(func() { ui.Text(c, text).FontSize(TypeMeta) })
		tip.AttachTo(anchor, ui.AnchorTop, ui.AnchorBottom)
	})
	return anchor
}

func PrimaryButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	return actionButton(c, label, t.AccentText, t.Accent, t.AccentHover, t.AccentPressed, ui.Transparent, true)
}

func SecondaryButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	return actionButton(c, label, t.Text, t.Surface, t.SurfaceHover, t.SurfacePressed, t.Focus, false)
}

func DangerButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	return actionButton(c, label, t.Danger, t.Surface, t.Surface.Mix(t.Danger, 0.08), t.Surface.Mix(t.Danger, 0.14), t.Focus.Mix(t.Danger, 0.35), false)
}

func GhostButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	return actionButton(c, label, t.TextMuted, ui.Transparent, t.SurfaceHover, t.SurfacePressed, ui.Transparent, false)
}

func LinkButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	button := actionButton(c, "", t.TextMuted, ui.Transparent, t.SurfaceHover, t.SurfacePressed, ui.Transparent, false).Label(label)
	button.Children(func() { ui.Text(c, label).Underline().SingleLine() })
	return button
}

func actionButton(c *ui.Context, label string, ink, background, hover, pressed, border ui.Color, primary bool) ui.Element {
	t := c.Theme()
	button := ui.Button(c, label).Label(label).Height(ControlHit).MinHeight(ControlHit).MinWidth(ControlHit).
		Radius(0).Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).
		FontSize(TypeAction).FontWeight(500).FixedLineHeight(LineAction).TextColor(ink).Padding(8, 12).Gap(6)
	return button.Draw(func(p *ui.Painter, r ui.Rect) {
		r.Y, r.H = r.Y+2, r.H-4
		fill := background
		switch {
		case button.IsDisabled() && primary:
			fill = background.Mix(t.Surface, 0.35)
		case button.IsDisabled():
		case button.Pressed():
			fill = pressed
		case button.Hovered():
			fill = hover
		}
		PaintPreferenceFace(p, r, 16, 4, fill, border)
		if button.FocusVisible() && !button.IsDisabled() {
			ring := t.Accent
			if primary {
				ring = t.AccentText
			}
			inside := ui.Rect{X: r.X + 2, Y: r.Y + 2, W: r.W - 4, H: r.H - 4}
			p.StrokePath(ControlPath(inside, 14, 4), 2, ring)
		}
	})
}

func NavigationControl(c *ui.Context, e ui.Element, palette WorkbenchTheme, selected bool) ui.Element {
	e = SharedControlFace(c, e, 8, 4, false, false, false)
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		r.Y, r.H = r.Y+2, r.H-4
		fill := ui.Transparent
		if selected {
			fill = palette.NavSelected
		} else if e.Hovered() || e.Focused() {
			fill = palette.NavHover
		}
		PaintPreferenceFace(p, r, 8, 4, fill, ui.Transparent)
	})
	return e
}

// SharedControlFace extends the transparent interaction box two DIPs past the visible face
// while keeping layout contribution at the declared 28 DIPs via negative block margins.
func SharedControlFace(c *ui.Context, e ui.Element, region float32, exponent float64, outlined, invalid, selected bool, inputBorder ...ui.Color) ui.Element {
	e.Height(ControlHit).MinHeight(ControlHit).Margin(-2, 0).Radius(0).
		Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false)
	return DrawControlFace(c, e, region, exponent, outlined, invalid, selected, 0, 2, c.Theme().SurfaceHover, inputBorder...)
}

func DrawControlFace(c *ui.Context, e ui.Element, region float32, exponent float64, outlined, invalid, selected bool, insetX, insetY float32, focusFill ui.Color, inputBorder ...ui.Color) ui.Element {
	t := c.Theme()
	textInput := len(inputBorder) > 0
	var inputBoundary ui.Color
	if textInput {
		inputBoundary = inputBorder[0]
	}
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		r.X, r.Y = r.X+insetX, r.Y+insetY
		r.W, r.H = r.W-2*insetX, r.H-2*insetY
		fill, border := ui.Transparent, ui.Transparent
		if outlined {
			fill, border = t.Surface, t.Border
		}
		if selected {
			fill = t.Selection
		}
		if textInput {
			fill, border = t.Surface, inputBoundary
		}
		if !textInput && !e.IsDisabled() {
			if e.Pressed() {
				fill = t.SurfacePressed
			} else if e.Hovered() {
				fill = t.SurfaceHover
			}
			if !outlined && e.FocusVisible() {
				fill = focusFill
			}
		}
		inputFocused := textInput && !invalid && !e.IsDisabled() && e.Focused()
		if invalid {
			border = t.Danger
		} else if inputFocused {
			border = t.Accent
		} else if !textInput && outlined && e.Focused() {
			border = t.Focus
		}
		if inputFocused {
			interior, interiorRegion := r, region
			if border.A > 0 {
				interior = ui.Rect{X: r.X + 2, Y: r.Y + 2, W: r.W - 4, H: r.H - 4}
				interiorRegion -= 2
			}
			p.FillPath(ControlPath(interior, interiorRegion, exponent), fill)
			inside := ui.Rect{X: r.X + 1, Y: r.Y + 1, W: r.W - 2, H: r.H - 2}
			p.StrokePath(ControlPath(inside, region-1, exponent), 2, border)
		} else {
			PaintPreferenceFace(p, r, region, exponent, fill, border)
		}
	})
	return e
}

type IconControlRole uint8

const (
	IconChrome IconControlRole = iota
	IconHeader
	IconPanel
	IconResponse
	IconContext
	IconPrimary
)

func (role IconControlRole) Metrics() (face, glyph, region float32) {
	switch role {
	case IconChrome:
		return 28, 14, 16
	case IconPanel:
		return 26, 16, 16
	case IconResponse:
		return 24, 16, 16
	case IconContext:
		return 28, 20, 10
	case IconPrimary:
		return 32, 16, 16
	default:
		return 28, 16, 16
	}
}

// IconControlFace centers the face in a real target. Callers reserve the whole target,
// including around an isolated response action; it never borrows a neighbor's box.
func IconControlFace(c *ui.Context, e ui.Element, role IconControlRole, selected bool) ui.Element {
	face, _, region := role.Metrics()
	e.Size(ControlHit, ControlHit).MinWidth(ControlHit).MinHeight(ControlHit).Shrink(0).
		Margin(0).Padding(0).Gap(0).Radius(0).Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false)
	exponent := float64(4)
	if role == IconPrimary {
		exponent = 2
	}
	inset := (ControlHit - face) / 2
	focusFill := c.Theme().SurfaceHover
	if role == IconPrimary {
		focusFill = c.Theme().AccentPressed
	}
	return DrawControlFace(c, e, region, exponent, false, false, selected, inset, inset, focusFill)
}

func ControlIcon(c *ui.Context, glyph *ui.SVG, role IconControlRole) ui.Element {
	_, size, _ := role.Metrics()
	return ui.Icon(c, glyph).Size(size, size).Shrink(0)
}

func PageHeading(c *ui.Context, title string) ui.Element {
	return ui.Text(c, title).Role(ui.RoleHeading).Level(1).Font("system-ui").
		FontSize(TypeSettingsTitle).FontWeight(600).FixedLineHeight(LineSettingsTitle).TextColor(c.Theme().Text)
}

type MarkdownHeadingTone uint8

const (
	HeadingPrimary MarkdownHeadingTone = iota
	HeadingSecondary
	HeadingTertiary
)

// MarkdownHeadingRole defines heading metrics in absolute DIPs; Top applies to nonfirst blocks.
// The reader owns margin composition and display-to-source mapping, including Uppercase.
type MarkdownHeadingRole struct {
	Size, LineHeight           float32
	Weight                     int
	Top, Bottom, LetterSpacing float32
	Uppercase                  bool
	Tone                       MarkdownHeadingTone
}

func MarkdownHeadingRoles() [6]MarkdownHeadingRole {
	return [6]MarkdownHeadingRole{
		{20, 28, 500, 28, 8, -.5, false, HeadingPrimary},
		{18, 24, 600, 24, 8, -.45, false, HeadingPrimary},
		{16, 24, 600, 20, 8, -.4, false, HeadingPrimary},
		{14, 20, 600, 16, 4, -.35, false, HeadingPrimary},
		{14, 24, 600, 16, 4, -.35, false, HeadingSecondary},
		{13, 20, 600, 16, 4, -.325, true, HeadingTertiary},
	}
}

func (tone MarkdownHeadingTone) Color(t *ui.Theme) ui.Color {
	if tone == HeadingPrimary {
		return t.Text
	}
	return t.TextMuted
}

func FormTextArea(c *ui.Context, value *string, invalid bool, inputBorder ...ui.Color) ui.Element {
	border := c.Theme().Border
	if len(inputBorder) > 0 {
		border = inputBorder[0]
	}
	e := ui.TextAreaBase(c, value).Font("system-ui").FontSize(TypeAction).FontWeight(400).
		FixedLineHeight(LineForm).MinHeight(80).BorderWidth(8, 12).BorderColor(ui.Transparent).
		Padding(0).Radius(0).Background(ui.Transparent).TextColor(c.Theme().Text).FocusRing(false)
	return DrawControlFace(c, e, 16, 4, true, invalid, false, 0, 0, c.Theme().SurfaceHover, border)
}

// PaintPreferenceFace renders a superellipse preference face while keeping fill out of the border when disabled opacity applies.
func PaintPreferenceFace(p *ui.Painter, r ui.Rect, region float32, exponent float64, fill, border ui.Color) {
	interior := r
	if border.A > 0 {
		interior = ui.Rect{X: r.X + 1, Y: r.Y + 1, W: r.W - 2, H: r.H - 2}
		p.FillPath(ControlPath(interior, region-1, exponent), fill)
	} else {
		p.FillPath(ControlPath(interior, region, exponent), fill)
	}
	inside := ui.Rect{X: r.X + .5, Y: r.Y + .5, W: r.W - 1, H: r.H - 1}
	p.StrokePath(ControlPath(inside, region-.5, exponent), 1, border)
}

// ControlPath samples the declared superellipse, normalizing corner regions to the box.
func ControlPath(r ui.Rect, region float32, exponent float64) *ui.Path {
	radius := min(region, r.W/2, r.H/2)
	path := new(ui.Path)
	centers := [4][2]float32{{r.X + r.W - radius, r.Y + radius}, {r.X + r.W - radius, r.Y + r.H - radius}, {r.X + radius, r.Y + r.H - radius}, {r.X + radius, r.Y + radius}}
	path.MoveTo(r.X+radius, r.Y)
	for corner, center := range centers {
		for step := 0; step <= 64; step++ {
			angle := (-.5 + float64(corner)*.5 + float64(step)/128) * math.Pi
			x, y := math.Cos(angle), math.Sin(angle)
			x = math.Copysign(math.Pow(math.Abs(x), 2/exponent), x)
			y = math.Copysign(math.Pow(math.Abs(y), 2/exponent), y)
			path.LineTo(center[0]+radius*float32(x), center[1]+radius*float32(y))
		}
	}
	return path.Close()
}

// PreferenceInput styles a single-line input whose editor scrolls inside its real border clip rather than its padding.
func PreferenceInput(c *ui.Context, value *string, invalid bool, inputBorder ...ui.Color) ui.Element {
	border := c.Theme().Border
	if len(inputBorder) > 0 {
		border = inputBorder[0]
	}
	e := ui.TextInputBase(c, value).Font("system-ui").FontSize(TypeAction).FontWeight(400).
		FixedLineHeight(LineAction).TextColor(c.Theme().Text)
	e = SharedControlFace(c, e, 16, 4, true, invalid, false, border)
	return e.BorderWidth(0, 12, 0, 12).Padding(8, 0)
}

// RemixIcon v4.6.0 (Apache-2.0), with the distinct Heroicons v1.0.6 (MIT) selector indicator and Heroicons v2.2.0 plus-small.
var (
	GlyphAddLine           = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M11 11V5H13V11H19V13H13V19H11V13H5V11H11Z"/></svg>`))
	GlyphArrowDownSLine    = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M11.9999 13.1714L16.9497 8.22168L18.3639 9.63589L11.9999 15.9999L5.63599 9.63589L7.0502 8.22168L11.9999 13.1714Z"/></svg>`))
	GlyphArrowUpLine       = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M13.0001 7.82843V20H11.0001V7.82843L5.63614 13.1924L4.22192 11.7782L12.0001 4L19.7783 11.7782L18.3641 13.1924L13.0001 7.82843Z"/></svg>`))
	GlyphArrowLeftLine     = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M7.82843 10.9999H20V12.9999H7.82843L13.1924 18.3638L11.7782 19.778L4 11.9999L11.7782 4.22168L13.1924 5.63589L7.82843 10.9999Z"/></svg>`))
	GlyphChat3Line         = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M7.29117 20.8242L2 22L3.17581 16.7088C2.42544 15.3056 2 13.7025 2 12C2 6.47715 6.47715 2 12 2C17.5228 2 22 6.47715 22 12C22 17.5228 17.5228 22 12 22C10.2975 22 8.6944 21.5746 7.29117 20.8242ZM7.58075 18.711L8.23428 19.0605C9.38248 19.6745 10.6655 20 12 20C16.4183 20 20 16.4183 20 12C20 7.58172 16.4183 4 12 4C7.58172 4 4 7.58172 4 12C4 13.3345 4.32549 14.6175 4.93949 15.7657L5.28896 16.4192L4.63416 19.3658L7.58075 18.711Z"/></svg>`))
	GlyphFileTextLine      = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M21 8V20.9932C21 21.5501 20.5552 22 20.0066 22H3.9934C3.44495 22 3 21.556 3 21.0082V2.9918C3 2.45531 3.4487 2 4.00221 2H14.9968L21 8ZM19 9H14V4H5V20H19V9ZM8 7H11V9H8V7ZM8 11H16V13H8V11ZM8 15H16V17H8V15Z"/></svg>`))
	GlyphAttachment2       = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M14.8287 7.75737L9.1718 13.4142C8.78127 13.8047 8.78127 14.4379 9.1718 14.8284C9.56232 15.219 10.1955 15.219 10.586 14.8284L16.2429 9.17158C17.4144 8.00001 17.4144 6.10052 16.2429 4.92894C15.0713 3.75737 13.1718 3.75737 12.0002 4.92894L6.34337 10.5858C4.39075 12.5384 4.39075 15.7042 6.34337 17.6569C8.29599 19.6095 11.4618 19.6095 13.4144 17.6569L19.0713 12L20.4855 13.4142L14.8287 19.0711C12.095 21.8047 7.66283 21.8047 4.92916 19.0711C2.19549 16.3374 2.19549 11.9053 4.92916 9.17158L10.586 3.51473C12.5386 1.56211 15.7045 1.56211 17.6571 3.51473C19.6097 5.46735 19.6097 8.63317 17.6571 10.5858L12.0002 16.2427C10.8287 17.4142 8.92916 17.4142 7.75759 16.2427C6.58601 15.0711 6.58601 13.1716 7.75759 12L13.4144 6.34316L14.8287 7.75737Z"/></svg>`))
	GlyphStickyNoteLine    = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M21 15L15 20.996L4.00221 21C3.4487 21 3 20.5551 3 20.0066V3.9934C3 3.44476 3.44495 3 3.9934 3H20.0066C20.5552 3 21 3.45576 21 4.00247V15ZM19 5H5V19H13V14C13 13.4872 13.386 13.0645 13.8834 13.0067L14 13L19 12.999V5ZM18.171 14.999L15 15V18.169L18.171 14.999Z"/></svg>`))
	GlyphArrowLeftSLine    = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M10.8284 12.0007L15.7782 16.9504L14.364 18.3646L8 12.0007L14.364 5.63672L15.7782 7.05093L10.8284 12.0007Z"/></svg>`))
	GlyphArrowRightSLine   = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M13.1717 12.0007L8.22192 7.05093L9.63614 5.63672L16.0001 12.0007L9.63614 18.3646L8.22192 16.9504L13.1717 12.0007Z"/></svg>`))
	GlyphCheckLine         = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M9.9997 15.1709L19.1921 5.97852L20.6063 7.39273L9.9997 17.9993L3.63574 11.6354L5.04996 10.2212L9.9997 15.1709Z"/></svg>`))
	GlyphCloseLine         = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M11.9997 10.5865L16.9495 5.63672L18.3637 7.05093L13.4139 12.0007L18.3637 16.9504L16.9495 18.3646L11.9997 13.4149L7.04996 18.3646L5.63574 16.9504L10.5855 12.0007L5.63574 7.05093L7.04996 5.63672L11.9997 10.5865Z"/></svg>`))
	GlyphSettings4Line     = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M2 11.9998C2 11.1353 2.1097 10.2964 2.31595 9.49631C3.40622 9.55283 4.48848 9.01015 5.0718 7.99982C5.65467 6.99025 5.58406 5.78271 4.99121 4.86701C6.18354 3.69529 7.66832 2.82022 9.32603 2.36133C9.8222 3.33385 10.8333 3.99982 12 3.99982C13.1667 3.99982 14.1778 3.33385 14.674 2.36133C16.3317 2.82022 17.8165 3.69529 19.0088 4.86701C18.4159 5.78271 18.3453 6.99025 18.9282 7.99982C19.5115 9.01015 20.5938 9.55283 21.6841 9.49631C21.8903 10.2964 22 11.1353 22 11.9998C22 12.8643 21.8903 13.7032 21.6841 14.5033C20.5938 14.4468 19.5115 14.9895 18.9282 15.9998C18.3453 17.0094 18.4159 18.2169 19.0088 19.1326C17.8165 20.3043 16.3317 21.1794 14.674 21.6383C14.1778 20.6658 13.1667 19.9998 12 19.9998C10.8333 19.9998 9.8222 20.6658 9.32603 21.6383C7.66832 21.1794 6.18354 20.3043 4.99121 19.1326C5.58406 18.2169 5.65467 17.0094 5.0718 15.9998C4.48848 14.9895 3.40622 14.4468 2.31595 14.5033C2.1097 13.7032 2 12.8643 2 11.9998ZM6.80385 14.9998C7.43395 16.0912 7.61458 17.3459 7.36818 18.5236C7.77597 18.8138 8.21005 19.0652 8.66489 19.2741C9.56176 18.4712 10.7392 17.9998 12 17.9998C13.2608 17.9998 14.4382 18.4712 15.3351 19.2741C15.7899 19.0652 16.224 18.8138 16.6318 18.5236C16.3854 17.3459 16.566 16.0912 17.1962 14.9998C17.8262 13.9085 18.8225 13.1248 19.9655 12.7493C19.9884 12.5015 20 12.2516 20 11.9998C20 11.7481 19.9884 11.4981 19.9655 11.2504C18.8225 10.8749 17.8262 10.0912 17.1962 8.99982C16.566 7.90845 16.3854 6.65378 16.6318 5.47605C16.224 5.18588 15.7899 4.93447 15.3351 4.72552C14.4382 5.52844 13.2608 5.99982 12 5.99982C10.7392 5.99982 9.56176 5.52844 8.66489 4.72552C8.21005 4.93447 7.77597 5.18588 7.36818 5.47605C7.61458 6.65378 7.43395 7.90845 6.80385 8.99982C6.17376 10.0912 5.17754 10.8749 4.03451 11.2504C4.01157 11.4981 4 11.7481 4 11.9998C4 12.2516 4.01157 12.5015 4.03451 12.7493C5.17754 13.1248 6.17376 13.9085 6.80385 14.9998ZM12 14.9998C10.3431 14.9998 9 13.6567 9 11.9998C9 10.343 10.3431 8.99982 12 8.99982C13.6569 8.99982 15 10.343 15 11.9998C15 13.6567 13.6569 14.9998 12 14.9998ZM12 12.9998C12.5523 12.9998 13 12.5521 13 11.9998C13 11.4475 12.5523 10.9998 12 10.9998C11.4477 10.9998 11 11.4475 11 11.9998C11 12.5521 11.4477 12.9998 12 12.9998Z"/></svg>`))
	GlyphFileCopyLine      = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M6.9998 6V3C6.9998 2.44772 7.44752 2 7.9998 2H19.9998C20.5521 2 20.9998 2.44772 20.9998 3V17C20.9998 17.5523 20.5521 18 19.9998 18H16.9998V20.9991C16.9998 21.5519 16.5499 22 15.993 22H4.00666C3.45059 22 3 21.5554 3 20.9991L3.0026 7.00087C3.0027 6.44811 3.45264 6 4.00942 6H6.9998ZM5.00242 8L5.00019 20H14.9998V8H5.00242ZM8.9998 6H16.9998V16H18.9998V4H8.9998V6Z"/></svg>`))
	GlyphSubtractLine      = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M5 11V13H19V11H5Z"/></svg>`))
	GlyphMoreLine          = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M4.5 10.5C3.675 10.5 3 11.175 3 12C3 12.825 3.675 13.5 4.5 13.5C5.325 13.5 6 12.825 6 12C6 11.175 5.325 10.5 4.5 10.5ZM19.5 10.5C18.675 10.5 18 11.175 18 12C18 12.825 18.675 13.5 19.5 13.5C20.325 13.5 21 12.825 21 12C21 11.175 20.325 10.5 19.5 10.5ZM12 10.5C11.175 10.5 10.5 11.175 10.5 12C10.5 12.825 11.175 13.5 12 13.5C12.825 13.5 13.5 12.825 13.5 12C13.5 11.175 12.825 10.5 12 10.5Z"/></svg>`))
	GlyphSearchLine        = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M18.031 16.6168L22.3137 20.8995L20.8995 22.3137L16.6168 18.031C15.0769 19.263 13.124 20 11 20C6.032 20 2 15.968 2 11C2 6.032 6.032 2 11 2C15.968 2 20 6.032 20 11C20 13.124 19.263 15.0769 18.031 16.6168ZM16.0247 15.8748C17.2475 14.6146 18 12.8956 18 11C18 7.1325 14.8675 4 11 4C7.1325 4 4 7.1325 4 11C4 14.8675 7.1325 18 11 18C12.8956 18 14.6146 17.2475 15.8748 16.0247L16.0247 15.8748Z"/></svg>`))
	GlyphStopFill          = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M6 5H18C18.5523 5 19 5.44772 19 6V18C19 18.5523 18.5523 19 18 19H6C5.44772 19 5 18.5523 5 18V6C5 5.44772 5.44772 5 6 5Z"/></svg>`))
	GlyphFolderLine        = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M4 5V19H20V7H11.5858L9.58579 5H4ZM12.4142 5H21C21.5523 5 22 5.44772 22 6V20C22 20.5523 21.5523 21 21 21H3C2.44772 21 2 20.5523 2 20V4C2 3.44772 2.44772 3 3 3H10.4142L12.4142 5Z"/></svg>`))
	GlyphSelectChevron     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" d="M19 9l-7 7-7-7"/></svg>`))
	NavBackIcon            = GlyphArrowLeftLine
	NavContactsIcon        = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M2 22C2 17.5817 5.58172 14 10 14C14.4183 14 18 17.5817 18 22H16C16 18.6863 13.3137 16 10 16C6.68629 16 4 18.6863 4 22H2ZM10 13C6.685 13 4 10.315 4 7C4 3.685 6.685 1 10 1C13.315 1 16 3.685 16 7C16 10.315 13.315 13 10 13ZM10 11C12.21 11 14 9.21 14 7C14 4.79 12.21 3 10 3C7.79 3 6 4.79 6 7C6 9.21 7.79 11 10 11ZM18.2837 14.7028C21.0644 15.9561 23 18.752 23 22H21C21 19.564 19.5483 17.4671 17.4628 16.5271L18.2837 14.7028ZM17.5962 3.41321C19.5944 4.23703 21 6.20361 21 8.5C21 11.3702 18.8042 13.7252 16 13.9776V11.9646C17.6967 11.7222 19 10.264 19 8.5C19 7.11935 18.2016 5.92603 17.041 5.35635L17.5962 3.41321Z"/></svg>`))
	NavCloseIcon           = GlyphCloseLine
	ComposerAttachIcon     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" aria-hidden="true" data-slot="icon"><path stroke-linecap="round" stroke-linejoin="round" d="M12 6v12m6-6H6"/></svg>`))
	ComposerSendIcon       = GlyphArrowUpLine
	ComposerStopIcon       = GlyphStopFill
	RailHideIcon           = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M5 5H13V19H5V5ZM19 19H15V5H19V19ZM4 3C3.44772 3 3 3.44772 3 4V20C3 20.5523 3.44772 21 4 21H20C20.5523 21 21 20.5523 21 20V4C21 3.44772 20.5523 3 20 3H4ZM7 12L11 8.5V15.5L7 12Z"/></svg>`))
	RailAddIcon            = GlyphAddLine
	RailSettingsIcon       = GlyphSettings4Line
	MarkdownCopyIcon       = GlyphFileCopyLine
	MarkdownCopiedIcon     = GlyphCheckLine
	MarkdownCopyFailedIcon = GlyphCloseLine
)
