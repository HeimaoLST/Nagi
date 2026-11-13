package main

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// workbenchTheme is the one per-frame resolution of the Paper & Moss
// appearance. Theme keeps the framework's control roles semantic; the small
// app-only set names reading surfaces that must not be reused as button faces.
// CSS roles use the adopted rem16/scale1 profile, independent of Theme.Rem.
// Existing workbench title aliases remain an S2 binding seam.
const (
	lineBody             float32 = 24
	cssRem               float32 = 16
	controlHeight        float32 = 28
	controlHit           float32 = 32
	typeLabel            float32 = 13
	lineLabel            float32 = 20
	lineMeta             float32 = 18
	lineAction           float32 = 16
	typeNavigation       float32 = 12
	lineNavigation       float32 = 16
	settingsSidebarWidth float32 = 248
	settingsContentWidth float32 = 820
	preferenceBreakpoint float32 = 40 * cssRem
	typeContactTitle     float32 = 16
	typePageTitle        float32 = 16
	typeGroupTitle       float32 = 14
	typeBody             float32 = 14
	typeAction           float32 = 13
	typeMeta             float32 = 12
	typeRailName         float32 = 13
	typeRailSummary      float32 = 12
)

type workbenchTheme struct {
	Theme         *ui.Theme
	Rail          ui.Color
	Reading       ui.Color
	Panel         ui.Color
	Well          ui.Color
	BubbleUser    ui.Color
	BubbleContact ui.Color
	WorkSurface   ui.Color
	WorkText      ui.Color
	WorkMuted     ui.Color
	WorkAttention ui.Color
	WorkWarningBg ui.Color
}

func resolveWorkbenchTheme(dark bool) workbenchTheme {
	t := ui.LightTheme()
	t.FontSize = typeLabel
	t.Font = "system-ui"
	t.Spacing = 4
	t.Radius = 8
	if dark {
		t = ui.DarkTheme()
		t.FontSize = typeLabel
		t.Font = "system-ui"
		t.Spacing = 4
		t.Radius = 8
		// #218 palette: the same theme fields, populated from the researched
		// semantic roles. Border is the control/input boundary; the decorative
		// line role has no separate Theme field in v0.7.3.
		t.Background = ui.Hex("#1D2520")
		t.Surface = ui.Hex("#29362D")
		t.SurfaceHover = ui.Hex("#314D3B")
		t.SurfacePressed = ui.Hex("#303B33")
		t.Border = ui.Hex("#879B8D")
		t.Text = ui.Hex("#EDF3EE")
		t.TextMuted = ui.Hex("#B0BFB3")
		t.Accent = ui.Hex("#B5D5BE")
		t.AccentHover = ui.Hex("#C7E3CF")
		t.AccentPressed = ui.Hex("#D8EDDE")
		t.AccentText = ui.Hex("#183624")
		t.Selection = ui.Hex("#314D3B")
		t.Focus = ui.Hex("#A7D6B8")
		t.Danger = ui.Hex("#F2A8AF")
		t.Warning = ui.Hex("#F0C38E")
		t.Success = ui.Hex("#A2D5AF")
		t.Scrollbar = ui.Hex("#B0BFB3")
		return workbenchTheme{
			Theme: t,
			Rail:  ui.Hex("#171E19"), Reading: ui.Hex("#1D2520"), Panel: ui.Hex("#202A23"),
			Well: ui.Hex("#29362D"), BubbleUser: ui.Hex("#324A3B"), BubbleContact: ui.Hex("#28332B"),
			WorkSurface: ui.Hex("#202A23"), WorkText: ui.Hex("#EDF3EE"),
			WorkMuted: ui.Hex("#B0BFB3"), WorkAttention: ui.Hex("#F0C38E"),
			WorkWarningBg: ui.Hex("#403222"),
		}
	}
	t.Background = ui.Hex("#FFFFFF")
	t.Surface = ui.Hex("#EEF2EF")
	t.SurfaceHover = ui.Hex("#E5EFE8")
	t.SurfacePressed = ui.Hex("#E5EAE6")
	t.Border = ui.Hex("#7C8A7F")
	t.Text = ui.Hex("#252E28")
	t.TextMuted = ui.Hex("#5D6B61")
	t.Accent = ui.Hex("#365E4D")
	t.AccentHover = ui.Hex("#2D5242")
	t.AccentPressed = ui.Hex("#244536")
	t.AccentText = ui.Hex("#FFFFFF")
	t.Selection = ui.Hex("#E5EFE8")
	t.Focus = ui.Hex("#3C7155")
	t.Danger = ui.Hex("#9B3C43")
	t.Warning = ui.Hex("#80501F")
	t.Success = ui.Hex("#31684A")
	t.Scrollbar = ui.Hex("#5D6B61")
	return workbenchTheme{
		Theme: t,
		Rail:  ui.Hex("#F5F7F5"), Reading: ui.Hex("#FFFFFF"), Panel: ui.Hex("#F8FAF8"),
		Well: ui.Hex("#EEF2EF"), BubbleUser: ui.Hex("#E5EFE8"), BubbleContact: ui.Hex("#F2F5F2"),
		WorkSurface: ui.Hex("#F8FAF8"), WorkText: ui.Hex("#252E28"),
		WorkMuted: ui.Hex("#5D6B61"), WorkAttention: ui.Hex("#80501F"),
		WorkWarningBg: ui.Hex("#FFF2DF"),
	}
}

func paletteLuminance(c ui.Color) float64 {
	linear := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.04045 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(c.R) + 0.7152*linear(c.G) + 0.0722*linear(c.B)
}

func paletteContrast(a, b ui.Color) float64 {
	x, y := paletteLuminance(a), paletteLuminance(b)
	if x < y {
		x, y = y, x
	}
	return (x + 0.05) / (y + 0.05)
}

// segmentedSelection keeps MyGo's official SegmentedBase radio group,
// keyboard semantics and Changed contract, with the app's restrained
// Selection/Text face instead of the framework pressedColor and shadow.
func segmentedSelection(c *ui.Context, selected *int, labels ...string) ui.Element {
	t := c.Theme()
	parts := ui.SegmentedBase(c, selected, len(labels))
	track := parts.Track.MinHeight(32).AlignItems(ui.Stretch).Padding(t.Space(0.5)).Gap(t.Space(0.5)).
		Radius(t.Radius).Background(t.Surface).Border(1, t.Border)
	track.Children(func() {
		for i, label := range labels {
			segment := parts.Segment(i).Label(label).MinHeight(32).Padding(t.Space(1), t.Space(2)).
				Radius(t.Radius).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted)
			if i == *selected {
				segment.Background(t.Selection).TextColor(t.Text)
			}
			if segment.Pressed() {
				segment.Background(t.SurfacePressed)
			} else if segment.Hovered() && i != *selected {
				segment.Background(t.SurfaceHover)
			}
			if segment.Focused() {
				segment.BorderWidth(1).BorderColor(t.Focus)
			}
			segment.Children(func() {
				ui.Text(c, label).FontSize(typeAction).SingleLine()
			})
		}
	})
	return track
}

// selectionButtonState gives a secondary button a Selection/Text selected
// face while preserving explicit hover, pressed and focus feedback.
func selectionButtonState(c *ui.Context, button ui.Element, selected bool) {
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

// preferenceSelect retains the official popup, stable values and keyboard controller.
type selectOption struct {
	Value string
	Label string
}

func preferenceSelect(c *ui.Context, selected *string, options []selectOption, label string, disabled ...bool) ui.Element {
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
	trigger := sharedControlFace(c, sel.Trigger.Label(label).Value(display).Disabled(blocked), 16, true, false, false).
		FillWidth().Justify(ui.Start).Padding(8, 10, 8, 12).FontWeight(500)
	trigger.Children(func() {
		ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, display).FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction).
				MaxLines(1).Grow(1).Shrink(1).MinWidth(0).Ellipsis("…").TextColor(t.Text)
			ui.Icon(c, glyphSelectChevron).Width(16).Height(16).Shrink(0).TextColor(t.TextMuted)
		})
	})
	sel.Popup(func(panel ui.Element) {
		panel.Padding(4).Radius(8).Background(t.Background).Border(1, t.Border)
		ui.Scroll(c).MaxHeight(t.Space(80)).Children(func() {
			for _, option := range options {
				item := sel.Item(option.Value).Disabled(blocked).MinHeight(controlHit).Padding(8, 6).Radius(6).FontSize(typeMeta).FixedLineHeight(lineAction)
				switch {
				case item.Highlighted():
					item.Background(t.Selection).TextColor(t.Text)
				case option.Value == *selected:
					item.Background(t.Selection).TextColor(t.Text)
				}
				item.Children(func() { ui.Text(c, option.Label).FontSize(typeMeta).FixedLineHeight(lineAction).SingleLine() })
			}
		})
	})
	trigger.OnChange(func() { trigger.Focus() })
	return appTooltip(c, trigger, full)
}

// appTooltip keeps MyGo's official tooltip controller but gives product tips
// the app's supporting size and flat semantic colors. The anchor's Description
// remains the accessible source; TooltipBase owns hover, keyboard, press and
// Escape dismissal.
func appTooltip(c *ui.Context, anchor ui.Element, text string) ui.Element {
	if text == "" {
		return anchor
	}
	anchor.Description(text)
	t := c.Theme()
	ui.TooltipBase(c, anchor, func(tip ui.Element) {
		tip.MaxWidth(t.Space(80)).Padding(t.Space(1.25), t.Space(2)).Radius(t.Space(1.25)).
			Background(t.Text).TextColor(t.Background).FontSize(typeMeta)
		tip.Children(func() { ui.Text(c, text).FontSize(typeMeta) })
		tip.AttachTo(anchor, ui.AnchorTop, ui.AnchorBottom)
	})
	return anchor
}

// secondaryButton uses the shared md ghost face and the existing Button controller.
func secondaryButton(c *ui.Context, label string) ui.Element {
	return sharedControlFace(c, ui.Button(c, label), 16, false, false, false).FontSize(typeAction).FontWeight(500).FixedLineHeight(lineAction).Padding(8, 12).Gap(6)
}

// The transparent interaction box extends two DIPs past the visible face.
// Negative block margins keep its layout contribution at the declared 28 DIPs.
func sharedControlFace(c *ui.Context, e ui.Element, region float32, outlined, invalid, selected bool) ui.Element {
	t := c.Theme()
	e.Height(controlHit).MinHeight(controlHit).Margin(-2, 0).Radius(0).
		Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false)
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		r.Y += 2
		r.H -= 4
		fill, border := ui.Transparent, ui.Transparent
		if outlined {
			fill, border = t.Surface, t.Border
		}
		if selected {
			fill = t.Selection
		}
		if !e.IsDisabled() {
			if e.Pressed() {
				fill = t.SurfacePressed
			} else if e.Hovered() {
				fill = t.SurfaceHover
			}
		}
		if invalid {
			border = t.Danger
		} else if e.Focused() {
			border = t.Focus
		}
		p.FillPath(controlPath(r, region, 4), fill)
		inside := ui.Rect{X: r.X + .5, Y: r.Y + .5, W: r.W - 1, H: r.H - 1}
		p.StrokePath(controlPath(inside, region-.5, 4), 1, border)
		if !outlined && e.FocusVisible() {
			p.StrokePath(controlPath(r, region, 4), 2, t.Focus)
		}
	})
	return e
}

// Sample the declared superellipse, normalizing corner regions to the box.
// This paints the face; MyGo's public Clip remains a separate circular primitive.
func controlPath(r ui.Rect, region float32, exponent float64) *ui.Path {
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

func preferenceInput(c *ui.Context, value *string, invalid bool) ui.Element {
	e := ui.TextInputBase(c, value).Font("system-ui").FontSize(typeAction).FontWeight(400).
		FixedLineHeight(lineAction).Padding(8, 12).TextColor(c.Theme().Text)
	return sharedControlFace(c, e, 16, true, invalid, false)
}

// RemixIcon v4.6.0 (Apache-2.0), with the distinct Heroicons v1.0.6 (MIT) selector indicator.
var (
	glyphAddLine         = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M11 11V5H13V11H19V13H13V19H11V13H5V11H11Z"/></svg>`))
	glyphArrowDownSLine  = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M11.9999 13.1714L16.9497 8.22168L18.3639 9.63589L11.9999 15.9999L5.63599 9.63589L7.0502 8.22168L11.9999 13.1714Z"/></svg>`))
	glyphArrowUpLine     = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M13.0001 7.82843V20H11.0001V7.82843L5.63614 13.1924L4.22192 11.7782L12.0001 4L19.7783 11.7782L18.3641 13.1924L13.0001 7.82843Z"/></svg>`))
	glyphArrowLeftLine   = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M7.82843 10.9999H20V12.9999H7.82843L13.1924 18.3638L11.7782 19.778L4 11.9999L11.7782 4.22168L13.1924 5.63589L7.82843 10.9999Z"/></svg>`))
	glyphArrowLeftSLine  = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M10.8284 12.0007L15.7782 16.9504L14.364 18.3646L8 12.0007L14.364 5.63672L15.7782 7.05093L10.8284 12.0007Z"/></svg>`))
	glyphArrowRightSLine = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M13.1717 12.0007L8.22192 7.05093L9.63614 5.63672L16.0001 12.0007L9.63614 18.3646L8.22192 16.9504L13.1717 12.0007Z"/></svg>`))
	glyphCheckLine       = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M9.9997 15.1709L19.1921 5.97852L20.6063 7.39273L9.9997 17.9993L3.63574 11.6354L5.04996 10.2212L9.9997 15.1709Z"/></svg>`))
	glyphCloseLine       = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M11.9997 10.5865L16.9495 5.63672L18.3637 7.05093L13.4139 12.0007L18.3637 16.9504L16.9495 18.3646L11.9997 13.4149L7.04996 18.3646L5.63574 16.9504L10.5855 12.0007L5.63574 7.05093L7.04996 5.63672L11.9997 10.5865Z"/></svg>`))
	glyphSettings4Line   = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M2 11.9998C2 11.1353 2.1097 10.2964 2.31595 9.49631C3.40622 9.55283 4.48848 9.01015 5.0718 7.99982C5.65467 6.99025 5.58406 5.78271 4.99121 4.86701C6.18354 3.69529 7.66832 2.82022 9.32603 2.36133C9.8222 3.33385 10.8333 3.99982 12 3.99982C13.1667 3.99982 14.1778 3.33385 14.674 2.36133C16.3317 2.82022 17.8165 3.69529 19.0088 4.86701C18.4159 5.78271 18.3453 6.99025 18.9282 7.99982C19.5115 9.01015 20.5938 9.55283 21.6841 9.49631C21.8903 10.2964 22 11.1353 22 11.9998C22 12.8643 21.8903 13.7032 21.6841 14.5033C20.5938 14.4468 19.5115 14.9895 18.9282 15.9998C18.3453 17.0094 18.4159 18.2169 19.0088 19.1326C17.8165 20.3043 16.3317 21.1794 14.674 21.6383C14.1778 20.6658 13.1667 19.9998 12 19.9998C10.8333 19.9998 9.8222 20.6658 9.32603 21.6383C7.66832 21.1794 6.18354 20.3043 4.99121 19.1326C5.58406 18.2169 5.65467 17.0094 5.0718 15.9998C4.48848 14.9895 3.40622 14.4468 2.31595 14.5033C2.1097 13.7032 2 12.8643 2 11.9998ZM6.80385 14.9998C7.43395 16.0912 7.61458 17.3459 7.36818 18.5236C7.77597 18.8138 8.21005 19.0652 8.66489 19.2741C9.56176 18.4712 10.7392 17.9998 12 17.9998C13.2608 17.9998 14.4382 18.4712 15.3351 19.2741C15.7899 19.0652 16.224 18.8138 16.6318 18.5236C16.3854 17.3459 16.566 16.0912 17.1962 14.9998C17.8262 13.9085 18.8225 13.1248 19.9655 12.7493C19.9884 12.5015 20 12.2516 20 11.9998C20 11.7481 19.9884 11.4981 19.9655 11.2504C18.8225 10.8749 17.8262 10.0912 17.1962 8.99982C16.566 7.90845 16.3854 6.65378 16.6318 5.47605C16.224 5.18588 15.7899 4.93447 15.3351 4.72552C14.4382 5.52844 13.2608 5.99982 12 5.99982C10.7392 5.99982 9.56176 5.52844 8.66489 4.72552C8.21005 4.93447 7.77597 5.18588 7.36818 5.47605C7.61458 6.65378 7.43395 7.90845 6.80385 8.99982C6.17376 10.0912 5.17754 10.8749 4.03451 11.2504C4.01157 11.4981 4 11.7481 4 11.9998C4 12.2516 4.01157 12.5015 4.03451 12.7493C5.17754 13.1248 6.17376 13.9085 6.80385 14.9998ZM12 14.9998C10.3431 14.9998 9 13.6567 9 11.9998C9 10.343 10.3431 8.99982 12 8.99982C13.6569 8.99982 15 10.343 15 11.9998C15 13.6567 13.6569 14.9998 12 14.9998ZM12 12.9998C12.5523 12.9998 13 12.5521 13 11.9998C13 11.4475 12.5523 10.9998 12 10.9998C11.4477 10.9998 11 11.4475 11 11.9998C11 12.5521 11.4477 12.9998 12 12.9998Z"/></svg>`))
	glyphFileCopyLine    = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M6.9998 6V3C6.9998 2.44772 7.44752 2 7.9998 2H19.9998C20.5521 2 20.9998 2.44772 20.9998 3V17C20.9998 17.5523 20.5521 18 19.9998 18H16.9998V20.9991C16.9998 21.5519 16.5499 22 15.993 22H4.00666C3.45059 22 3 21.5554 3 20.9991L3.0026 7.00087C3.0027 6.44811 3.45264 6 4.00942 6H6.9998ZM5.00242 8L5.00019 20H14.9998V8H5.00242ZM8.9998 6H16.9998V16H18.9998V4H8.9998V6Z"/></svg>`))
	glyphSubtractLine    = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M5 11V13H19V11H5Z"/></svg>`))
	glyphMoreLine        = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M4.5 10.5C3.675 10.5 3 11.175 3 12C3 12.825 3.675 13.5 4.5 13.5C5.325 13.5 6 12.825 6 12C6 11.175 5.325 10.5 4.5 10.5ZM19.5 10.5C18.675 10.5 18 11.175 18 12C18 12.825 18.675 13.5 19.5 13.5C20.325 13.5 21 12.825 21 12C21 11.175 20.325 10.5 19.5 10.5ZM12 10.5C11.175 10.5 10.5 11.175 10.5 12C10.5 12.825 11.175 13.5 12 13.5C12.825 13.5 13.5 12.825 13.5 12C13.5 11.175 12.825 10.5 12 10.5Z"/></svg>`))
	glyphSearchLine      = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M18.031 16.6168L22.3137 20.8995L20.8995 22.3137L16.6168 18.031C15.0769 19.263 13.124 20 11 20C6.032 20 2 15.968 2 11C2 6.032 6.032 2 11 2C15.968 2 20 6.032 20 11C20 13.124 19.263 15.0769 18.031 16.6168ZM16.0247 15.8748C17.2475 14.6146 18 12.8956 18 11C18 7.1325 14.8675 4 11 4C7.1325 4 4 7.1325 4 11C4 14.8675 7.1325 18 11 18C12.8956 18 14.6146 17.2475 15.8748 16.0247L16.0247 15.8748Z"/></svg>`))
	glyphStopFill        = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M6 5H18C18.5523 5 19 5.44772 19 6V18C19 18.5523 18.5523 19 18 19H6C5.44772 19 5 18.5523 5 18V6C5 5.44772 5.44772 5 6 5Z"/></svg>`))
	glyphFolderLine      = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M4 5V19H20V7H11.5858L9.58579 5H4ZM12.4142 5H21C21.5523 5 22 5.44772 22 6V20C22 20.5523 21.5523 21 21 21H3C2.44772 21 2 20.5523 2 20V4C2 3.44772 2.44772 3 3 3H10.4142L12.4142 5Z"/></svg>`))
	glyphTimeLine        = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M12 22C6.47715 22 2 17.5228 2 12C2 6.47715 6.47715 2 12 2C17.5228 2 22 6.47715 22 12C22 17.5228 17.5228 22 12 22ZM12 20C16.4183 20 20 16.4183 20 12C20 7.58172 16.4183 4 12 4C7.58172 4 4 7.58172 4 12C4 16.4183 7.58172 20 12 20ZM13 12H17V14H11V7H13V12Z"/></svg>`))
	glyphSelectChevron   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" aria-hidden="true">
  <path stroke-linecap="round" stroke-linejoin="round" d="M19 9l-7 7-7-7"/>
</svg>`))
	navWorkIcon            = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M7 5V2C7 1.44772 7.44772 1 8 1H16C16.5523 1 17 1.44772 17 2V5H21C21.5523 5 22 5.44772 22 6V20C22 20.5523 21.5523 21 21 21H3C2.44772 21 2 20.5523 2 20V6C2 5.44772 2.44772 5 3 5H7ZM4 16V19H20V16H4ZM4 14H20V7H4V14ZM9 3V5H15V3H9ZM11 11H13V13H11V11Z"/></svg>`))
	navHistoryIcon         = glyphTimeLine
	navProfileIcon         = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M4 22C4 17.5817 7.58172 14 12 14C16.4183 14 20 17.5817 20 22H18C18 18.6863 15.3137 16 12 16C8.68629 16 6 18.6863 6 22H4ZM12 13C8.685 13 6 10.315 6 7C6 3.685 8.685 1 12 1C15.315 1 18 3.685 18 7C18 10.315 15.315 13 12 13ZM12 11C14.21 11 16 9.21 16 7C16 4.79 14.21 3 12 3C9.79 3 8 4.79 8 7C8 9.21 9.79 11 12 11Z"/></svg>`))
	navBackIcon            = glyphArrowLeftLine
	navContactsIcon        = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M2 22C2 17.5817 5.58172 14 10 14C14.4183 14 18 17.5817 18 22H16C16 18.6863 13.3137 16 10 16C6.68629 16 4 18.6863 4 22H2ZM10 13C6.685 13 4 10.315 4 7C4 3.685 6.685 1 10 1C13.315 1 16 3.685 16 7C16 10.315 13.315 13 10 13ZM10 11C12.21 11 14 9.21 14 7C14 4.79 12.21 3 10 3C7.79 3 6 4.79 6 7C6 9.21 7.79 11 10 11ZM18.2837 14.7028C21.0644 15.9561 23 18.752 23 22H21C21 19.564 19.5483 17.4671 17.4628 16.5271L18.2837 14.7028ZM17.5962 3.41321C19.5944 4.23703 21 6.20361 21 8.5C21 11.3702 18.8042 13.7252 16 13.9776V11.9646C17.6967 11.7222 19 10.264 19 8.5C19 7.11935 18.2016 5.92603 17.041 5.35635L17.5962 3.41321Z"/></svg>`))
	navCloseIcon           = glyphCloseLine
	composerAttachIcon     = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M14.8287 7.75737L9.1718 13.4142C8.78127 13.8047 8.78127 14.4379 9.1718 14.8284C9.56232 15.219 10.1955 15.219 10.586 14.8284L16.2429 9.17158C17.4144 8.00001 17.4144 6.10052 16.2429 4.92894C15.0713 3.75737 13.1718 3.75737 12.0002 4.92894L6.34337 10.5858C4.39075 12.5384 4.39075 15.7042 6.34337 17.6569C8.29599 19.6095 11.4618 19.6095 13.4144 17.6569L19.0713 12L20.4855 13.4142L14.8287 19.0711C12.095 21.8047 7.66283 21.8047 4.92916 19.0711C2.19549 16.3374 2.19549 11.9053 4.92916 9.17158L10.586 3.51473C12.5386 1.56211 15.7045 1.56211 17.6571 3.51473C19.6097 5.46735 19.6097 8.63317 17.6571 10.5858L12.0002 16.2427C10.8287 17.4142 8.92916 17.4142 7.75759 16.2427C6.58601 15.0711 6.58601 13.1716 7.75759 12L13.4144 6.34316L14.8287 7.75737Z"/></svg>`))
	composerSendIcon       = glyphArrowUpLine
	composerStopIcon       = glyphStopFill
	railHideIcon           = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M5 5H13V19H5V5ZM19 19H15V5H19V19ZM4 3C3.44772 3 3 3.44772 3 4V20C3 20.5523 3.44772 21 4 21H20C20.5523 21 21 20.5523 21 20V4C21 3.44772 20.5523 3 20 3H4ZM7 12L11 8.5V15.5L7 12Z"/></svg>`))
	railAddIcon            = glyphAddLine
	railSettingsIcon       = glyphSettings4Line
	markdownCopyIcon       = glyphFileCopyLine
	markdownCopiedIcon     = glyphCheckLine
	markdownCopyFailedIcon = glyphCloseLine
	modelSelectedIcon      = glyphCheckLine
)
