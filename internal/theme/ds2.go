package theme

import (
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	ControlRadius = 9
	CardRadius    = 12
	ModalRadius   = 16
	PillRadius    = 99
)

var ControlTransition = ui.ElementTransition{Duration: 180 * time.Millisecond, Colors: true, Shadows: true}

func TransitionControl(e ui.Element, t *ui.Theme) ui.Element {
	palette := [10]ui.Color{t.Surface, t.SurfaceHover, t.SurfacePressed, t.Border, t.Text, t.TextMuted, t.Accent, t.AccentText, t.Success, t.Danger}
	previous := ui.Local(e, "control-palette", func() [10]ui.Color { return palette })
	if *previous == palette {
		e.Transition(ControlTransition)
	}
	*previous = palette
	return e
}

func StyleFrostedModal(backdrop, panel ui.Element, t *ui.Theme) {
	StyleFrostedModalBackdrop(backdrop, t)
	panel.Background(t.Surface).Border(1, t.Border).Radius(ModalRadius).Shadow(0, 8, 28, 0, ui.RGBA(0, 0, 0, .24))
}

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
			TransitionControl(ui.Box(c).Size(17, 17).Radius(PillRadius).Background(thumb), t)
		})
	})
	return control.Draw(func(p *ui.Painter, r ui.Rect) {
		if control.FocusVisible() && !control.IsDisabled() {
			p.Stroke(ui.Rect{X: r.X + 1, Y: r.Y + 2, W: r.W - 2, H: r.H - 4}, t.Accent, 14, 2)
		}
	})
}

func TextAction(c *ui.Context, label string, danger bool) ui.Element {
	t := c.Theme()
	ink, hover, pressed := t.TextMuted, t.SurfaceHover, t.SurfacePressed
	if danger {
		ink, hover, pressed = t.Danger, t.Surface.Mix(t.Danger, .10), t.Surface.Mix(t.Danger, .16)
	}
	button := ui.ButtonBase(c).Label(label).MinWidth(ControlHit).MinHeight(ControlHit).Radius(ControlRadius).Padding(6, 10).
		Justify(ui.Center).AlignItems(ui.Center).TextColor(ink).FocusRingColor(t.Accent)
	if button.Pressed() {
		button.Background(pressed)
	} else if button.Hovered() {
		button.Background(hover)
	}
	button.Children(func() {
		ui.Text(c, label).FontSize(TypeAction).FontWeight(500).FixedLineHeight(LineAction).SingleLine()
	})
	return TransitionControl(button, t)
}

func BrandIcon(c *ui.Context, icon *ui.SVG, size float32) ui.Element {
	return ui.Box(c).Size(size, size).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		ui.Image(c, icon).Size(size, size).Shrink(0).TextColor(c.Theme().Text).AccessHidden(true)
	})
}

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
		background, ink = t.SurfacePressed, t.Text
	} else if pill.Hovered() {
		background, ink = t.SurfaceHover, t.Text
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
	return pill.Draw(func(p *ui.Painter, r ui.Rect) {
		if pill.FocusVisible() && !pill.IsDisabled() {
			p.Stroke(ui.Rect{X: r.X + 2, Y: r.Y + 2, W: r.W - 4, H: r.H - 4}, t.Accent, (r.H-4)/2, 2)
		}
	})
}
