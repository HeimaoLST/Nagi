package theme

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
)

// ActivityDay holds one day's regular and coalesced execution totals for SegmentedActivityChart7D.
type ActivityDay struct {
	Label     string
	Regular   int
	Coalesced int
	Tooltip   string
}

// WakeHour holds one hourly slot's autonomous wake count and optional tooltip text for WakeRhythmStrip24H.
type WakeHour struct {
	Hour    int
	Runs    int
	Tooltip string
}

// SegmentedActivityChart7D renders a responsive 7-day activity column chart separating Regular (neutral base)
// and Coalesced (green/gold accent cap with a 2-DIP separator gap), dashed grid/mean reference lines,
// and accessible per-column AppTooltip overlays.
//
//	theme.SegmentedActivityChart7D(c, days, avgTotal)
func SegmentedActivityChart7D(c *ui.Context, days []ActivityDay, meanValue float64) ui.Element {
	t := c.Theme()
	baseBar := ChartBaseBar(t)
	accentBar := ChartAccentBar(t)
	tertiary := TertiaryInk(t)

	maxY := 30
	for _, d := range days {
		if total := d.Regular + d.Coalesced; total > maxY {
			maxY = ((total + 9) / 10) * 10
		}
	}

	const (
		padL float32 = 24
		padR float32 = 10
		padT float32 = 16
		padB float32 = 22
	)

	chart := ui.Box(c).FillWidth().Height(152).MinWidth(180)
	chart.Draw(func(p *ui.Painter, r ui.Rect) {
		plotW := r.W - padL - padR
		plotH := r.H - padT - padB
		if plotW <= 0 || plotH <= 0 {
			return
		}
		for _, tick := range [3]int{0, maxY / 2, maxY} {
			y := r.Y + padT + plotH*(1-float32(tick)/float32(maxY))
			if tick == 0 {
				p.StrokePolyline([][2]float32{{r.X + padL, y}, {r.X + r.W - padR, y}}, 1, t.Border)
			} else {
				drawDashedHorizontal(p, r.X+padL, r.X+r.W-padR, y, 2.5, 3, 1, t.Border)
			}
			label := fmt.Sprintf("%d", tick)
			p.Text(r.X+padL-float32(len(label))*5.5-5, y-5, label, 9.5, tertiary)
		}
		if meanValue > 0 && meanValue <= float64(maxY) {
			avgY := r.Y + padT + plotH*float32(1-meanValue/float64(maxY))
			drawDashedHorizontal(p, r.X+padL, r.X+r.W-padR, avgY, 4, 3, 1, t.TextMuted.Alpha(.5))
		}
		n := len(days)
		if n == 0 {
			return
		}
		stepX := plotW / float32(n)
		barW := min(float32(16), max(float32(8), stepX*0.52))
		for i, d := range days {
			cx := r.X + padL + stepX*(float32(i)+0.5)
			x := cx - barW/2
			hReg := (float32(max(0, d.Regular)) / float32(maxY)) * plotH
			yReg := r.Y + padT + plotH - hReg
			if hReg > 0 {
				p.Fill(ui.Rect{X: x, Y: yReg, W: barW, H: hReg}, baseBar, 4)
			}
			if d.Coalesced > 0 {
				hCoal := max(4, (float32(d.Coalesced)/float32(maxY))*plotH)
				gap := float32(2)
				if hReg == 0 {
					gap = 0
				}
				yCoal := yReg - gap - hCoal
				p.Fill(ui.Rect{X: x, Y: yCoal, W: barW, H: hCoal}, accentBar, 4)
			}
			if d.Label != "" {
				w := estimateChartLabelWidth(d.Label, 10.5)
				p.Text(cx-w/2, r.Y+r.H-15, d.Label, 10.5, t.TextMuted)
			}
		}
	})
	if len(days) > 0 {
		chart.Children(func() {
			ui.Row(c).Fill().Padding(padT, padR, padB, padL).Children(func() {
				for _, d := range days {
					col := ui.Box(c).Grow(1).FillHeight().MinWidth(0)
					if d.Tooltip != "" {
						AppTooltip(c, col, d.Tooltip)
					}
				}
			})
		})
	}
	return chart
}

// ContextCapacityBar renders an 8-DIP recessed token/quota meter with semantic fill steps
// (Success < 75%, Warning >= 75%, Danger >= 90%) and a subtle 80% soft-warning threshold tick.
//
//	theme.ContextCapacityBar(c, 38000, 128000, "PandoraBox", "38K / 128K (30%)")
func ContextCapacityBar(c *ui.Context, used, quota float64, label, detail string) ui.Element {
	t := c.Theme()
	slotBg, _ := InputSlotColors(t)
	ratio := float32(0)
	if quota > 0 {
		ratio = float32(max(0, min(1, used/quota)))
	}
	fillColor := t.Success
	switch {
	case ratio >= 0.90:
		fillColor = t.Danger
	case ratio >= 0.75:
		fillColor = t.Warning
	}
	wrap := ui.Column(c).FillWidth().Gap(4)
	wrap.Children(func() {
		if label != "" || detail != "" {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
				if label != "" {
					ui.Text(c, label).FontSize(12).FontWeight(500).FixedLineHeight(16).TextColor(t.Text).MinWidth(0).Shrink(1).MaxLines(1).Ellipsis("…")
				}
				if detail != "" {
					ui.Text(c, detail).FontSize(12).FixedLineHeight(16).TextColor(t.TextMuted).SingleLine().Shrink(0)
				}
			})
		}
		bar := ui.Box(c).FillWidth().Height(8).Radius(PillRadius).Background(slotBg).Border(1, t.Border).Clip()
		bar.Draw(func(p *ui.Painter, r ui.Rect) {
			if ratio > 0 {
				fillW := max(6, (r.W-2)*ratio)
				p.Fill(ui.Rect{X: r.X + 1, Y: r.Y + 1, W: min(r.W-2, fillW), H: max(0, r.H-2)}, fillColor, PillRadius)
			}
			tickX := r.X + r.W*0.80
			p.Fill(ui.Rect{X: tickX, Y: r.Y, W: 1, H: r.H}, t.TextMuted.Alpha(.45), 0)
		})
	})
	return wrap
}

// WakeRhythmStrip24H renders a 24-column hourly wake intensity strip inside a recessed slot tray
// with start, mid and end time labels and per-hour AppTooltip overlays.
//
//	theme.WakeRhythmStrip24H(c, hours24, "00:00", "12:00", msgs.Now())
func WakeRhythmStrip24H(c *ui.Context, hours []WakeHour, startLabel, midLabel, endLabel string) ui.Element {
	t := c.Theme()
	slotBg, _ := InputSlotColors(t)
	accentBar := ChartAccentBar(t)
	tertiary := TertiaryInk(t)

	wrap := ui.Column(c).FillWidth().Gap(6)
	wrap.Children(func() {
		tray := ui.Row(c).FillWidth().Height(52).Padding(10).Radius(10).Background(slotBg).Border(1, t.Border).
			AlignItems(ui.End).Gap(3)
		tray.Children(func() {
			for i := 0; i < 24; i++ {
				runs := 0
				tip := ""
				if i < len(hours) {
					runs, tip = hours[i].Runs, hours[i].Tooltip
				}
				color := t.Border
				height := float32(8)
				switch {
				case runs == 1:
					color, height = t.Success, 16
				case runs == 2:
					color, height = accentBar, 26
				case runs >= 3:
					color, height = t.Warning, 34
				}
				slot := ui.Box(c).Grow(1).FillHeight().AlignItems(ui.End).Justify(ui.End)
				slot.Children(func() {
					ui.Box(c).FillWidth().Height(height).Radius(3).Background(color)
				})
				if tip != "" {
					AppTooltip(c, slot, tip)
				}
			}
		})
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).Children(func() {
			for _, label := range [3]string{startLabel, midLabel, endLabel} {
				ui.Text(c, label).FontSize(10.5).FixedLineHeight(14).TextColor(tertiary).SingleLine()
			}
		})
	})
	return wrap
}

// SparklineStatTile renders a KPI summary tile combining a bold numeric metric with a normalized polyline trend.
//
//	theme.SparklineStatTile(c, msgs.MetricCompleted7D(), "131", pts, t.Success)
func SparklineStatTile(c *ui.Context, label, value string, points []float64, stroke ui.Color) ui.Element {
	t := c.Theme()
	tile := ui.Column(c).MinWidth(0).Grow(1).Padding(8, 10).Gap(4).Radius(ControlRadius).Background(t.Surface).Border(1, t.Border)
	tile.Children(func() {
		ui.Text(c, label).FontSize(11).FixedLineHeight(14).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
		ui.Row(c).FillWidth().AlignItems(ui.End).Justify(ui.SpaceBetween).Gap(6).Children(func() {
			ui.Text(c, value).FontSize(16).FontWeight(600).FixedLineHeight(18).TextColor(t.Text).SingleLine()
			spark := ui.Box(c).Size(62, 20).Shrink(0)
			spark.Draw(func(p *ui.Painter, r ui.Rect) {
				if len(points) < 2 {
					return
				}
				peak := 1.0
				for _, v := range points {
					peak = max(peak, v)
				}
				coords := make([][2]float32, len(points))
				for i, v := range points {
					x := r.X + (r.W-4)*float32(i)/float32(len(points)-1) + 2
					y := r.Y + r.H - 2 - float32(max(0, v)/peak)*max(1, r.H-4)
					coords[i] = [2]float32{x, y}
				}
				p.StrokePolyline(coords, 2, stroke)
			})
		})
	})
	return tile
}

func drawDashedHorizontal(p *ui.Painter, x0, x1, y, dash, gap, width float32, c ui.Color) {
	step := dash + gap
	if step <= 0 || x1 <= x0 {
		return
	}
	for x := x0; x < x1; x += step {
		end := float32(math.Min(float64(x1), float64(x+dash)))
		p.StrokePolyline([][2]float32{{x, y}, {end, y}}, width, c)
	}
}

func estimateChartLabelWidth(s string, size float32) float32 {
	var width float32
	for _, r := range s {
		if r > 0x2E80 {
			width += size
		} else {
			width += size * 0.56
		}
	}
	return width
}
