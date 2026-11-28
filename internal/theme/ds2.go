package theme

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// DS 2.1 geometry and keyboard-focus ring constants shared across controls.
const (
	ControlRadius           = 9
	CardRadius              = 12
	ModalRadius             = 16
	PillRadius              = 99
	FocusRingGap    float32 = 3
	FocusRingStroke float32 = 2
)

// StatusTone selects semantic background, border and foreground inks for StatusPill and InlineBanner.
type StatusTone uint8

const (
	// ToneNeutral renders calm surface-tinted metadata for informational pills or banners.
	ToneNeutral StatusTone = iota
	// ToneSuccess renders Paper/Moss green tokens for completed checks and healthy status.
	ToneSuccess
	// ToneWarning renders amber tokens for recoverable notices and approval/review boundaries.
	ToneWarning
	// ToneDanger renders destructive red tokens for validation errors or blocked states.
	ToneDanger
)

// Locked vector glyphs for compact status corner badges (pure paths for subpixel crispness).
var (
	BadgeCheckSVG   = ui.MustParseSVG([]byte(`<svg viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" xmlns="http://www.w3.org/2000/svg"><path d="M2.2 6.3 L4.8 8.9 L9.8 3.4"/></svg>`))
	BadgeExclaimSVG = ui.MustParseSVG([]byte(`<svg viewBox="0 0 12 12" fill="currentColor" xmlns="http://www.w3.org/2000/svg"><path d="M5.1 2.1H6.9C7.397 2.1 7.8 2.503 7.8 3V6C7.8 6.497 7.397 6.9 6.9 6.9H5.1C4.603 6.9 4.2 6.497 4.2 6V3C4.2 2.503 4.603 2.1 5.1 2.1ZM6 8.2C6.552 8.2 7 8.648 7 9.2C7 9.752 6.552 10.2 6 10.2C5.448 10.2 5 9.752 5 9.2C5 8.648 5.448 8.2 6 8.2Z"/></svg>`))
	BadgeMinusSVG   = ui.MustParseSVG([]byte(`<svg viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" xmlns="http://www.w3.org/2000/svg"><path d="M3 6 H9"/></svg>`))
)

// ControlTransition coordinates 180ms color and elevation transitions within one palette.
var ControlTransition = ui.ElementTransition{Duration: 180 * time.Millisecond, Colors: true, Shadows: true}

func isBuiltinPalette(t *ui.Theme) bool {
	if t.Dark {
		return t.Accent == ui.Hex("#E0C65C")
	}
	return t.Accent == ui.Hex("#0D0D0D")
}

// FocusRingInk returns the Paper deep-green or Moss gold outer keyboard-focus stroke color.
func FocusRingInk(t *ui.Theme) ui.Color {
	if !t.Dark && isBuiltinPalette(t) {
		return ui.Hex("#2D6A4A").Alpha(.72)
	}
	return t.Accent.Alpha(.85)
}

// LinkInk returns the accessible inline-action link color for t.
func LinkInk(t *ui.Theme) ui.Color {
	if !t.Dark && isBuiltinPalette(t) {
		return ui.Hex("#2D6A4A")
	}
	return t.Accent
}

// TertiaryInk returns muted tertiary ink for axis labels, timestamps and secondary metadata.
func TertiaryInk(t *ui.Theme) ui.Color {
	if isBuiltinPalette(t) {
		if t.Dark {
			return ui.Hex("#89877A")
		}
		return ui.Hex("#838280")
	}
	return t.TextMuted.Mix(t.Surface, .25)
}

// InputBorderColor returns the subtle resting hairline border used by inputs and secondary controls.
func InputBorderColor(t *ui.Theme) ui.Color {
	if isBuiltinPalette(t) {
		if t.Dark {
			return ui.Hex("#3A3D36")
		}
		return ui.Hex("#D4D4CE")
	}
	return t.Border.Mix(t.Text, .14)
}

// StrongHoverFill returns the stepped hover fill for tactile buttons and interactive rows.
func StrongHoverFill(t *ui.Theme) ui.Color {
	if isBuiltinPalette(t) {
		if t.Dark {
			return ui.Hex("#2F322A")
		}
		return ui.Hex("#EAEAE4")
	}
	return t.SurfaceHover
}

// StrongPressedFill returns the stepped pressed fill for tactile buttons and interactive rows.
func StrongPressedFill(t *ui.Theme) ui.Color {
	if isBuiltinPalette(t) {
		if t.Dark {
			return ui.Hex("#3C3F36")
		}
		return ui.Hex("#DCDCD3")
	}
	return t.SurfacePressed
}

// PedestalColors returns the soft rounded backdrop fill and hairline border shared by avatar and provider pedestals.
func PedestalColors(t *ui.Theme) (bg, border ui.Color) {
	if isBuiltinPalette(t) {
		if t.Dark {
			return ui.Hex("#242620"), ui.Hex("#F2F1E8").Alpha(.08)
		}
		return ui.Hex("#F2F2EC"), ui.Hex("#141414").Alpha(.08)
	}
	if t.Dark {
		return t.Surface.Mix(t.Text, .06), t.Text.Alpha(.08)
	}
	return t.Surface.Mix(ui.RGB(0, 0, 0), .05), t.Text.Alpha(.08)
}

// InputSlotColors returns the recessed resting surface and top inner shade for DS 2.1 form inputs, checkboxes and chart tracks.
func InputSlotColors(t *ui.Theme) (bg, innerShade ui.Color) {
	if isBuiltinPalette(t) {
		if t.Dark {
			return ui.Hex("#12130F"), ui.RGBA(0, 0, 0, .36)
		}
		return ui.Hex("#F8F8F4"), ui.RGBA(20, 20, 20, .055)
	}
	if t.Dark {
		return t.Surface.Mix(ui.RGB(0, 0, 0), .18), ui.RGBA(0, 0, 0, .30)
	}
	return t.Surface.Mix(ui.RGB(0, 0, 0), .028), ui.RGBA(0, 0, 0, .05)
}

// InputFocusInk returns the focused input border ink (#2E6B45 on Paper, #E0C65C on Moss).
func InputFocusInk(t *ui.Theme) ui.Color {
	if !t.Dark && isBuiltinPalette(t) {
		return ui.Hex("#2E6B45")
	}
	return t.Accent
}

// InputFocusHalo returns the 3-DIP translucent focus ring glow for active inputs and the composer card.
func InputFocusHalo(t *ui.Theme) ui.Color {
	if !t.Dark && isBuiltinPalette(t) {
		return ui.Hex("#2E6B45").Alpha(.16)
	}
	return t.Accent.Alpha(.19)
}

// StatusVividSuccess returns high-visibility green ink for active status rings and corner badges.
func StatusVividSuccess(t *ui.Theme) ui.Color {
	if isBuiltinPalette(t) {
		if t.Dark {
			return ui.Hex("#68D391")
		}
		return ui.Hex("#227A4B")
	}
	return t.Success
}

// StatusVividWarning returns high-visibility amber ink for waiting status rings and corner badges.
func StatusVividWarning(t *ui.Theme) ui.Color {
	if isBuiltinPalette(t) {
		if t.Dark {
			return ui.Hex("#FAAD14")
		}
		return ui.Hex("#C86D00")
	}
	return t.Warning
}

// ChartBaseBar returns the neutral base column fill for regular un-coalesced activity counts.
func ChartBaseBar(t *ui.Theme) ui.Color {
	if isBuiltinPalette(t) {
		if t.Dark {
			return ui.Hex("#44473E")
		}
		return ui.Hex("#CFCFC6")
	}
	return t.Border.Mix(t.Text, .22)
}

// ChartAccentBar returns the emphasized fill for coalesced wakes and highlighted rhythm bars.
func ChartAccentBar(t *ui.Theme) ui.Color {
	if !t.Dark && isBuiltinPalette(t) {
		return ui.Hex("#2E6B45")
	}
	return t.Accent
}

// ButtonElevation returns resting outer shadow and top edge rim highlight colors for elevated buttons.
func ButtonElevation(t *ui.Theme, primary bool) (shadowCol, topRim ui.Color) {
	if primary {
		if t.Dark {
			return ui.RGBA(0, 0, 0, .36), ui.RGBA(255, 255, 255, .24)
		}
		return ui.RGBA(13, 13, 13, .18), ui.RGBA(255, 255, 255, .16)
	}
	if t.Dark {
		return ui.RGBA(0, 0, 0, .28), ui.RGBA(255, 255, 255, .05)
	}
	return ui.RGBA(20, 20, 20, .06), ui.RGBA(255, 255, 255, .80)
}

// DrawFocusRing paints the DS 2.1 outer keyboard ring after an explicit 3-DIP air gap around r.
// Set exponent > 0 to trace a superellipse ControlPath (such as buttons), or 0 for circular/pill corners.
//
//	button.FocusRing(false).DrawOver(func(p *ui.Painter, r ui.Rect) {
//		if button.FocusVisible() && !button.IsDisabled() {
//			theme.DrawFocusRing(p, r, theme.ControlRadius, 2, c.Theme())
//		}
//	})
func DrawFocusRing(p *ui.Painter, r ui.Rect, radius float32, exponent float64, t *ui.Theme) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	ink := FocusRingInk(t)
	if exponent > 0 {
		expand := FocusRingGap + FocusRingStroke/2
		ringRect := ui.Rect{X: r.X - expand, Y: r.Y - expand, W: r.W + 2*expand, H: r.H + 2*expand}
		p.StrokePath(ControlPath(ringRect, radius+expand, exponent), FocusRingStroke, ink)
		return
	}
	d := FocusRingGap + FocusRingStroke
	ringRect := ui.Rect{X: r.X - d, Y: r.Y - d, W: r.W + 2*d, H: r.H + 2*d}
	p.Stroke(ringRect, ink, radius+d, FocusRingStroke)
}

// TransitionControl attaches ControlTransition only while t's core palette stays constant so theme switches snap on frame one.
func TransitionControl(e ui.Element, t *ui.Theme) ui.Element {
	palette := [10]ui.Color{t.Surface, t.SurfaceHover, t.SurfacePressed, t.Border, t.Text, t.TextMuted, t.Accent, t.AccentText, t.Success, t.Danger}
	previous := ui.Local(e, "control-palette", func() [10]ui.Color { return palette })
	if *previous == palette {
		e.Transition(ControlTransition)
	}
	*previous = palette
	return e
}

// StyleFrostedModal styles a translucent 12-DIP blurred backdrop and opaque ModalRadius dialog card (e.g. 440/380/616 DIP modals).
//
//	theme.StyleFrostedModal(backdrop, panel, t)
func StyleFrostedModal(backdrop, panel ui.Element, t *ui.Theme) {
	StyleFrostedModalBackdrop(backdrop, t)
	panel.Background(t.Surface).Border(1, t.Border).Radius(ModalRadius).Shadow(0, 8, 28, 0, ui.RGBA(0, 0, 0, .24))
}

// ToggleSwitch renders the 40x23 boolean track centered inside a 32-DIP hit target with thumb shadow and 3-DIP focus ring.
// Callers attach persistence or side effects with OnChange on the returned element:
//
//	theme.ToggleSwitch(c, &enabled, label, blocked).OnChange(func() { savePreference(enabled) })
func ToggleSwitch(c *ui.Context, checked *bool, label string, disabled ...bool) ui.Element {
	t := c.Theme()
	control := ui.SwitchBase(c, checked).Label(label).Size(40, ControlHit).Shrink(0).
		Disabled(len(disabled) > 0 && disabled[0]).Background(ui.Transparent).Border(0, ui.Transparent).
		FocusRing(false).AlignItems(ui.Center).Justify(ui.Center)
	control.Children(func() {
		mix := float32(.14)
		if t.Dark {
			mix = .18
		}
		track, thumb, side := t.Border.Mix(t.Text, mix), t.Surface, ui.Start
		if *checked {
			track, thumb, side = t.Accent, t.AccentText, ui.End
		}
		trackElement := ui.Row(c).Size(40, 23).Padding(0, 3).Radius(PillRadius).Background(track)
		TransitionControl(trackElement, t).AlignItems(ui.Center).Justify(side).Children(func() {
			TransitionControl(ui.Box(c).Size(17, 17).Radius(PillRadius).Background(thumb).Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, .22)), t)
		})
	})
	return control.DrawOver(func(p *ui.Painter, r ui.Rect) {
		if control.FocusVisible() && !control.IsDisabled() {
			trackRect := ui.Rect{X: r.X, Y: r.Y + (r.H-23)/2, W: r.W, H: 23}
			DrawFocusRing(p, trackRect, 11.5, 0, t)
		}
	})
}

// TextAction renders a compact borderless inline or table action with optional danger ink and 3-DIP keyboard focus ring.
//
//	theme.TextAction(c, msgs.RemoveAction(), true).Disabled(blocked).OnClick(onRemove)
func TextAction(c *ui.Context, label string, danger bool) ui.Element {
	t := c.Theme()
	ink, hover, pressed := t.TextMuted, StrongHoverFill(t), StrongPressedFill(t)
	if danger {
		ink, hover, pressed = t.Danger, t.Surface.Mix(t.Danger, .10), t.Surface.Mix(t.Danger, .16)
	}
	button := ui.ButtonBase(c).Label(label).MinWidth(ControlHit).MinHeight(ControlHit).Radius(ControlRadius).Padding(6, 10).
		Justify(ui.Center).AlignItems(ui.Center).TextColor(ink).FocusRing(false)
	if button.Pressed() {
		button.Background(pressed)
		if !danger {
			button.TextColor(t.Text)
		}
	} else if button.Hovered() {
		button.Background(hover)
		if !danger {
			button.TextColor(t.Text)
		}
	}
	button.Children(func() {
		ui.Text(c, label).FontSize(TypeAction).FontWeight(500).FixedLineHeight(LineAction).SingleLine()
	})
	return TransitionControl(button, t).DrawOver(func(p *ui.Painter, r ui.Rect) {
		if button.FocusVisible() && !button.IsDisabled() {
			DrawFocusRing(p, r, ControlRadius, 2, t)
		}
	})
}

// BrandIcon renders a bare SVG brand mark (retaining brand fills on colored icons and current ink on mono icons).
func BrandIcon(c *ui.Context, icon *ui.SVG, size float32) ui.Element {
	return ui.Box(c).Size(size, size).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		ui.Image(c, icon).Size(size, size).Shrink(0).TextColor(c.Theme().Text).AccessHidden(true)
	})
}

// BrandPedestalIcon mounts a brand or provider glyph inside a rounded PedestalColors backdrop (18:26 glyph-to-box ratio).
//
//	theme.BrandPedestalIcon(c, iconSVG, 26)
func BrandPedestalIcon(c *ui.Context, icon *ui.SVG, size float32) ui.Element {
	t := c.Theme()
	bg, border := PedestalColors(t)
	radius := float32(math.Round(float64(size) * 7.0 / 26.0))
	glyphSize := float32(math.Round(float64(size) * 18.0 / 26.0))
	return ui.Box(c).Size(size, size).Shrink(0).Radius(radius).Background(bg).Border(1, border).
		AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		ui.Image(c, icon).Size(glyphSize, glyphSize).Shrink(0).TextColor(t.Text).AccessHidden(true)
	})
}

// TagPill renders a selectable capability or model pill; set dashed=true for inherited/unpinned presets when unselected.
//
//	theme.TagPill(c, name, ctxBadge, selected, dashed, blocked).OnClick(toggle)
func TagPill(c *ui.Context, label, badge string, selected, dashed, disabled bool) ui.Element {
	t := c.Theme()
	ink, background, border := t.TextMuted, ui.Transparent, t.Border.Mix(t.Text, .12)
	if selected {
		ink, background, border = t.Text, t.Surface.Mix(t.Success, .09), t.Border.Mix(t.Success, .45)
		if t.Dark {
			background, border = t.Surface.Mix(t.Accent, .12), t.Border.Mix(t.Accent, .52)
		}
	}
	pill := ui.ButtonBase(c).Label(label).AccessSelected(selected).Disabled(disabled).FocusRing(false).
		MinHeight(ControlHit).MinWidth(0).MaxWidthPercent(100).Padding(4, 12).Gap(6).AlignItems(ui.Center).
		Radius(PillRadius).Border(1, border)
	TransitionControl(pill, t)
	if pill.Pressed() {
		background, ink = StrongPressedFill(t), t.Text
	} else if pill.Hovered() {
		background, ink = StrongHoverFill(t), t.Text
	}
	pill.Background(background).TextColor(ink)
	if !selected && dashed {
		pill.BorderStyle(ui.BorderDashed)
	}
	pill.Children(func() {
		ui.Text(c, label).MinWidth(0).Shrink(1).MaxLines(1).Ellipsis("…").FontSize(TypeAction).FixedLineHeight(LineAction)
		if badge != "" {
			ui.Text(c, badge).Shrink(0).Radius(PillRadius).Padding(0, 6).Background(t.Surface.Mix(t.Text, .08)).
				FontSize(TypeMeta).FixedLineHeight(LineMeta)
		}
	})
	return pill.DrawOver(func(p *ui.Painter, r ui.Rect) {
		if pill.FocusVisible() && !pill.IsDisabled() {
			DrawFocusRing(p, r, r.H/2, 0, t)
		}
	})
}

// Checkbox renders a 16x16 DS 2.1 recessed-slot checkbox inside a 32-DIP accessible hit row.
// Chain .Disabled(...) or .OnChange(...) directly on the returned element:
//
//	theme.Checkbox(c, &fallback, msgs.ProviderUseAsBackup()).Disabled(blocked).OnChange(onToggle)
func Checkbox(c *ui.Context, checked *bool, label string) ui.Element {
	row := ui.CheckboxBase(c, checked).MinHeight(ControlHit).Gap(8).AlignItems(ui.Center).FocusRing(false)
	if label != "" {
		row.Label(label)
	}
	state := ui.CheckOff
	if *checked {
		state = ui.CheckOn
	}
	return styleCheckboxRow(c, row, state, label)
}

// IndeterminateCheckbox renders a tri-state checkbox (ui.CheckOff, ui.CheckOn, or ui.CheckMixed) for group "select all" rows.
//
//	theme.IndeterminateCheckbox(c, &groupState, label).OnChange(applyGroup)
func IndeterminateCheckbox(c *ui.Context, state *ui.CheckState, label string) ui.Element {
	row := ui.CheckboxStateBase(c, state).MinHeight(ControlHit).Gap(8).AlignItems(ui.Center).FocusRing(false)
	if label != "" {
		row.Label(label)
	}
	return styleCheckboxRow(c, row, *state, label)
}

func styleCheckboxRow(c *ui.Context, row ui.Element, state ui.CheckState, label string) ui.Element {
	t := c.Theme()
	slotBg, innerShade := InputSlotColors(t)
	border := InputBorderColor(t)
	row.Children(func() {
		box := ui.Box(c).Size(16, 16).Radius(5).Shrink(0).IgnoreInput(true)
		TransitionControl(box, t)
		if state != ui.CheckOff {
			box.Background(t.Accent).Border(1.5, t.Accent)
		} else {
			if row.Hovered() && !row.IsDisabled() {
				border = border.Mix(t.Text, .26)
			}
			box.Background(slotBg).Border(1.5, border)
		}
		box.DrawOver(func(p *ui.Painter, r ui.Rect) {
			if state == ui.CheckOff && !row.IsDisabled() {
				interior := ui.Rect{X: r.X + 1.5, Y: r.Y + 1.5, W: max(0, r.W-3), H: max(0, r.H-3)}
				p.Clip(interior, 3.5, func() {
					p.Fill(ui.Rect{X: interior.X, Y: interior.Y, W: interior.W, H: 1.5}, innerShade, 0)
				})
			}
			if state == ui.CheckOn {
				var check ui.Path
				check.MoveTo(r.X+r.W*0.24, r.Y+r.H*0.52).LineTo(r.X+r.W*0.43, r.Y+r.H*0.70).LineTo(r.X+r.W*0.77, r.Y+r.H*0.32)
				p.StrokePath(&check, 2, t.AccentText)
			} else if state == ui.CheckMixed {
				p.Fill(ui.Rect{X: r.X + r.W*0.25, Y: r.Y + r.H/2 - 1, W: r.W * 0.5, H: 2}, t.AccentText, 1)
			}
			if row.FocusVisible() && !row.IsDisabled() {
				DrawFocusRing(p, r, 5, 0, t)
			}
		})
		if label != "" {
			ui.Text(c, label).FontSize(TypeAction).FixedLineHeight(LineAction).TextColor(t.Text)
		}
	})
	return row
}

// Radio renders a 16x16 circular option indicator binding value into *selected within a 32-DIP accessible hit row.
//
//	theme.Radio(c, &mode, modeCompact, msgs.ModeCompact()).OnChange(onSelect)
func Radio[T comparable](c *ui.Context, selected *T, value T, label string) ui.Element {
	t := c.Theme()
	on := *selected == value
	row := ui.RadioBase(c, selected, value).MinHeight(ControlHit).Gap(8).AlignItems(ui.Center).FocusRing(false)
	if label != "" {
		row.Label(label)
	}
	slotBg, innerShade := InputSlotColors(t)
	border := InputBorderColor(t)
	row.Children(func() {
		dot := ui.Box(c).Size(16, 16).Radius(8).Shrink(0).IgnoreInput(true)
		TransitionControl(dot, t)
		if on {
			dot.Background(t.AccentText)
		} else {
			if row.Hovered() && !row.IsDisabled() {
				border = border.Mix(t.Text, .26)
			}
			dot.Background(slotBg).Border(1.5, border)
		}
		dot.DrawOver(func(p *ui.Painter, r ui.Rect) {
			if on {
				p.Stroke(r, t.Accent, 8, 5)
			} else if !row.IsDisabled() {
				interior := ui.Rect{X: r.X + 1.5, Y: r.Y + 1.5, W: max(0, r.W-3), H: max(0, r.H-3)}
				p.Clip(interior, 6.5, func() {
					p.Fill(ui.Rect{X: interior.X, Y: interior.Y, W: interior.W, H: 1.5}, innerShade, 0)
				})
			}
			if row.FocusVisible() && !row.IsDisabled() {
				DrawFocusRing(p, r, 8, 0, t)
			}
		})
		if label != "" {
			ui.Text(c, label).FontSize(TypeAction).FixedLineHeight(LineAction).TextColor(t.Text)
		}
	})
	return row
}

// Slider renders a continuous or stepped horizontal track between lo and hi; pass optional positive step for snapped ticks.
//
//	theme.Slider(c, &temperature, 0, 2, 0.1).Label("Temperature").OnChange(apply)
func Slider(c *ui.Context, value *float64, lo, hi float64, step ...float64) ui.Element {
	t := c.Theme()
	s := ui.SliderBase(c, value, lo, hi).Height(ControlHit).MinWidth(120).PaddingX(9).FocusRing(false)
	if len(step) > 0 && step[0] > 0 {
		s.Step(step[0])
	}
	mix := float32(.14)
	if t.Dark {
		mix = .18
	}
	trackOff := t.Border.Mix(t.Text, mix)
	s.Draw(func(p *ui.Painter, r ui.Rect) {
		if r.W <= 18 {
			return
		}
		frac := float32(0)
		if hi > lo {
			frac = float32(max(0, min(1, (*value-lo)/(hi-lo))))
		}
		trackH := float32(5)
		cy := r.Y + r.H/2
		track := ui.Rect{X: r.X, Y: cy - trackH/2, W: r.W, H: trackH}
		p.Fill(track, trackOff, trackH/2)
		x := r.X + 9 + (r.W-18)*frac
		fillW := max(0, x-track.X)
		if fillW > 0 {
			p.Fill(ui.Rect{X: track.X, Y: track.Y, W: fillW, H: trackH}, t.Accent, trackH/2)
		}
		knobSize := float32(17)
		if !s.IsDisabled() && (s.Hovered() || s.Pressed()) {
			knobSize = 18.5
		}
		knob := ui.Rect{X: x - knobSize/2, Y: cy - knobSize/2, W: knobSize, H: knobSize}
		p.Shadow(knob, knobSize/2, 0, 1, 3, 0, ui.RGBA(0, 0, 0, .22))
		p.Fill(knob, t.Surface, knobSize/2)
		p.Stroke(knob, t.Accent, knobSize/2, 1.5)
		if s.FocusVisible() && !s.IsDisabled() {
			DrawFocusRing(p, knob, knobSize/2, 0, t)
		}
	})
	return s
}

// Colors resolves semantic soft background, hairline border and contrast-safe foreground ink for tone.
func (tone StatusTone) Colors(t *ui.Theme) (bg, border, ink ui.Color) {
	switch tone {
	case ToneSuccess:
		return t.Surface.Mix(t.Success, .11), t.Border.Mix(t.Success, .30), t.Success
	case ToneWarning:
		bg = ui.Hex("#FEFBE8")
		if t.Dark {
			bg = t.Surface.Mix(t.Warning, .16)
		}
		return bg, t.Border.Mix(t.Warning, .30), t.Warning
	case ToneDanger:
		return t.Surface.Mix(t.Danger, .10), t.Border.Mix(t.Danger, .42), t.Danger
	default:
		mix := float32(.045)
		if t.Dark {
			mix = .06
		}
		return t.Surface.Mix(t.Text, mix), t.Border, t.TextMuted
	}
}

// StatusPill pairs a 6-DIP indicator dot with compact status text inside a rounded semantic pill.
//
//	theme.StatusPill(c, msgs.ActivityCompleted(), theme.ToneSuccess)
func StatusPill(c *ui.Context, label string, tone StatusTone) ui.Element {
	bg, _, ink := tone.Colors(c.Theme())
	pill := ui.Row(c).Shrink(0).Padding(2, 8).Gap(6).Radius(PillRadius).Background(bg).AlignItems(ui.Center)
	pill.Children(func() {
		ui.Box(c).Size(6, 6).Radius(3).Shrink(0).Background(ink)
		ui.Text(c, label).FontSize(11).FontWeight(600).FixedLineHeight(15).TextColor(ink).SingleLine()
	})
	return pill
}

// InlineBanner renders a full-width wrapping notice strip with optional trailing SecondaryButton action.
//
//	theme.InlineBanner(c, summary, msgs.OpenSettings(), theme.ToneWarning, a.openConfiguration)
func InlineBanner(c *ui.Context, title, actionLabel string, tone StatusTone, onAction func()) ui.Element {
	t := c.Theme()
	bg, border, ink := tone.Colors(t)
	textColor := t.Text
	if tone == ToneDanger {
		textColor = ink
	}
	banner := ui.Row(c).FillWidth().Padding(11, 14).Gap(12).Radius(10).Background(bg).Border(1, border).AlignItems(ui.Center)
	banner.Children(func() {
		ui.Text(c, title).FontSize(12.5).FixedLineHeight(18).TextColor(textColor).MinWidth(0).Grow(1).Shrink(1)
		if actionLabel != "" && onAction != nil {
			SecondaryButton(c, actionLabel).OnClick(onAction)
		}
	})
	return banner
}

// SkeletonLine renders a Paper/Moss shimmer placeholder bar; pass width <= 0 to fill available width.
// Automatically settles to a static neutral block when ReduceMotion is active.
//
//	theme.SkeletonLine(c, 0, 12)
func SkeletonLine(c *ui.Context, width, height float32, radius ...float32) ui.Element {
	t := c.Theme()
	rad := float32(6)
	if len(radius) > 0 {
		rad = radius[0]
	}
	base, sheen := ui.Hex("#ECECE5"), ui.Hex("#F6F6F1")
	if t.Dark {
		base, sheen = ui.Hex("#23251F"), ui.Hex("#2D3028")
	}
	line := ui.Box(c).Size(width, height).Radius(rad).Shrink(0)
	if width <= 0 {
		line.FillWidth()
	}
	return line.Draw(func(p *ui.Painter, r ui.Rect) {
		p.Fill(r, base, rad)
		if c.Preferences().ReduceMotion || r.W <= 0 {
			return
		}
		phase := float32(p.Now().UnixMilli()%1600) / 1600.0
		bandW := max(24, r.W*0.42)
		x := r.X - bandW + (r.W+2*bandW)*phase
		p.Clip(r, rad, func() {
			p.FillGradient(ui.Rect{X: x, Y: r.Y, W: bandW, H: r.H}, ui.LinearGradient{
				Angle: 90,
				Stops: []ui.GradientStop{{Color: base.Alpha(0), At: 0}, {Color: sheen, At: 0.5}, {Color: base.Alpha(0), At: 1}},
			}, 0)
		})
		p.After(33 * time.Millisecond)
	})
}
