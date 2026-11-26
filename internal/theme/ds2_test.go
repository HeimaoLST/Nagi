package theme

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestDS2TokensContrastAndPrimitives(t *testing.T) {
	if ControlRadius != 9 || CardRadius != 12 || ModalRadius != 16 || PillRadius != 99 {
		t.Fatalf("DS2 radius scale mismatch: control=%d card=%d modal=%d pill=%d", ControlRadius, CardRadius, ModalRadius, PillRadius)
	}
	if ControlTransition.Duration != 180*time.Millisecond || !ControlTransition.Colors || !ControlTransition.Shadows {
		t.Fatalf("ControlTransition mismatch: %+v", ControlTransition)
	}

	for _, tc := range []struct {
		name string
		dark bool
	}{{"paper-light", false}, {"moss-dark", true}} {
		t.Run(tc.name, func(t *testing.T) {
			wb := ResolveWorkbenchTheme(tc.dark)
			st := SettingsTheme(wb.Theme)
			for _, bg := range []struct {
				name  string
				color ui.Color
			}{
				{"Background", st.Background},
				{"Surface", st.Surface},
				{"SurfaceHover", st.SurfaceHover},
				{"SurfacePressed", st.SurfacePressed},
			} {
				for _, ink := range []struct {
					name  string
					color ui.Color
				}{
					{"Text", st.Text},
					{"TextMuted", st.TextMuted},
					{"Success", st.Success},
					{"Warning", st.Warning},
					{"Danger", st.Danger},
				} {
					if ratio := PaletteContrast(ink.color, bg.color); ratio < 4.5 {
						t.Fatalf("%s on %s contrast = %.2f, want >= 4.5", ink.name, bg.name, ratio)
					}
				}
			}
			for _, dangerBg := range []ui.Color{st.Surface.Mix(st.Danger, .10), st.Surface.Mix(st.Danger, .16)} {
				if ratio := PaletteContrast(st.Danger, dangerBg); ratio < 4.5 {
					t.Fatalf("TextAction danger contrast on hover/pressed tint = %.2f, want >= 4.5", ratio)
				}
			}
			pillSelectedBg := st.Surface.Mix(st.Success, .09)
			if st.Dark {
				pillSelectedBg = st.Surface.Mix(st.Accent, .12)
			}
			if ratio := PaletteContrast(st.Text, pillSelectedBg); ratio < 4.5 {
				t.Fatalf("TagPill selected contrast = %.2f, want >= 4.5", ratio)
			}
		})
	}
}

func TestDS2ToggleSwitchTagPillTextActionAndBrandIcon(t *testing.T) {
	monoSVG, err := ui.ParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect x="4" y="4" width="16" height="16" fill="currentColor"/></svg>`))
	if err != nil || len(monoSVG.Unsupported()) != 0 {
		t.Fatalf("monochrome SVG parse failed: err=%v unsupported=%v", err, monoSVG.Unsupported())
	}
	colorSVG, err := ui.ParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="12" cy="12" r="9" fill="#4D6BFE"/></svg>`))
	if err != nil || len(colorSVG.Unsupported()) != 0 {
		t.Fatalf("color SVG parse failed: err=%v unsupported=%v", err, colorSVG.Unsupported())
	}

	diffPixels := func(a, b *image.RGBA, r ui.Rect) int {
		changed := 0
		x0, y0 := max(0, int(r.X)), max(0, int(r.Y))
		x1, y1 := min(a.Bounds().Dx(), int(r.X+r.W)), min(a.Bounds().Dy(), int(r.Y+r.H))
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
					changed++
				}
			}
		}
		return changed
	}

	var lightMonoCenter, darkMonoCenter [4]uint8
	for _, dark := range []bool{false, true} {
		checked := false
		disabledChecked := true
		pillSelected := false
		pillClicks, textClicks, dangerClicks := 0, 0, 0
		view := func(c *ui.Context) {
			wb := ResolveWorkbenchTheme(dark)
			st := SettingsTheme(wb.Theme)
			c.SetTheme(&st)
			ui.Column(c).Fill().Padding(16).Gap(12).Background(st.Surface).Children(func() {
				ToggleSwitch(c, &checked, "Active toggle", false)
				ToggleSwitch(c, &disabledChecked, "Disabled toggle", true)
				TagPill(c, "gpt-5", "128K", pillSelected, true, false).OnClick(func() {
					pillSelected = !pillSelected
					pillClicks++
				})
				TextAction(c, "Edit action", false).OnClick(func() { textClicks++ })
				TextAction(c, "Delete action", true).OnClick(func() { dangerClicks++ })
				ui.Row(c).Gap(12).Children(func() {
					BrandIcon(c, monoSVG, 24).Label("Mono mark")
					BrandIcon(c, colorSVG, 24).Label("Color mark")
				})
			})
		}

		tr := ui.NewTesterWith(view, 480, 320, ui.TesterOptions{Dark: dark})
		tr.SetFocused(true)
		tr.Frame()

		swRect, ok := tr.Find("Active toggle")
		if !ok || swRect.W < 40 || swRect.H < ControlHit {
			t.Fatalf("ToggleSwitch geometry = %+v ok=%v, want >= 40x%.0f", swRect, ok, ControlHit)
		}
		beforeSwitch := cloneTestRGBA(tr.Image())
		if err := tr.Click("Active toggle"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if !checked {
			t.Fatal("ToggleSwitch click did not toggle checked state")
		}
		tr.Advance(250 * time.Millisecond)
		if diffPixels(beforeSwitch, tr.Image(), swRect) == 0 {
			t.Fatal("ToggleSwitch pixels did not visibly update after toggle")
		}
		if err := tr.Click("Disabled toggle"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if !disabledChecked {
			t.Fatal("disabled ToggleSwitch mutated state on click")
		}

		pillRect, ok := tr.Find("gpt-5")
		if !ok || pillRect.H < ControlHit {
			t.Fatalf("TagPill geometry = %+v ok=%v, want H >= %.0f", pillRect, ok, ControlHit)
		}
		beforePill := cloneTestRGBA(tr.Image())
		if err := tr.Click("gpt-5"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		tr.Advance(250 * time.Millisecond)
		if !pillSelected || pillClicks != 1 || diffPixels(beforePill, tr.Image(), pillRect) == 0 {
			t.Fatalf("TagPill click did not toggle/restyle: selected=%v clicks=%d", pillSelected, pillClicks)
		}

		for _, item := range []struct {
			label  string
			clicks *int
		}{{"Edit action", &textClicks}, {"Delete action", &dangerClicks}} {
			r, ok := tr.Find(item.label)
			if !ok || r.W < ControlHit || r.H < ControlHit {
				t.Fatalf("TextAction %q geometry = %+v ok=%v, want >= %.0fx%.0f", item.label, r, ok, ControlHit, ControlHit)
			}
			if err := tr.Click(item.label); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if *item.clicks != 1 {
				t.Fatalf("TextAction %q click count = %d, want 1", item.label, *item.clicks)
			}
		}

		monoRect, okM := tr.Find("Mono mark")
		colorRect, okC := tr.Find("Color mark")
		if !okM || !okC || monoRect.W != 24 || monoRect.H != 24 || colorRect.W != 24 || colorRect.H != 24 {
			t.Fatalf("BrandIcon rects = mono:%+v(%v) color:%+v(%v)", monoRect, okM, colorRect, okC)
		}
		img := tr.Image()
		mc := img.RGBAAt(int(monoRect.X+12), int(monoRect.Y+12))
		cc := img.RGBAAt(int(colorRect.X+12), int(colorRect.Y+12))
		if cc.B <= cc.R {
			t.Fatalf("colored BrandIcon did not preserve brand blue fill: %+v", cc)
		}
		if dark {
			darkMonoCenter = [4]uint8{mc.R, mc.G, mc.B, mc.A}
		} else {
			lightMonoCenter = [4]uint8{mc.R, mc.G, mc.B, mc.A}
		}
	}
	if lightMonoCenter == darkMonoCenter {
		t.Fatalf("monochrome currentColor BrandIcon did not adapt between Paper Light and Moss Dark: %+v", lightMonoCenter)
	}
}

func cloneTestRGBA(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}

func TestDS2TransitionControlThemeSwitchSnapsFirstFrame(t *testing.T) {
	newView := func(checked, pillSelected *bool, hoverAction *bool) func(*ui.Context) {
		return func(c *ui.Context) {
			wb := ResolveWorkbenchTheme(c.Theme().Dark)
			st := SettingsTheme(wb.Theme)
			c.SetTheme(&st)
			ui.Column(c).Fill().Padding(16).Gap(12).Background(st.Surface).Children(func() {
				ToggleSwitch(c, checked, "Theme switch", false)
				TagPill(c, "read", "128K", *pillSelected, true, false).OnClick(func() {
					*pillSelected = !*pillSelected
				})
				TextAction(c, "Danger action", true)
				if *hoverAction {
					TextAction(c, "Quiet action", false)
				}
			})
		}
	}

	sampleRect := func(img *image.RGBA, r ui.Rect, dx, dy float32) [4]uint8 {
		c := img.RGBAAt(int(r.X+dx), int(r.Y+dy))
		return [4]uint8{c.R, c.G, c.B, c.A}
	}
	closeColor := func(a, b [4]uint8) bool {
		for i := range 4 {
			d := int(a[i]) - int(b[i])
			if d < 0 {
				d = -d
			}
			if d > 3 {
				return false
			}
		}
		return true
	}

	lightChecked, lightSelected, showQuiet := true, true, true
	refLight := ui.NewTesterWith(newView(&lightChecked, &lightSelected, &showQuiet), 480, 320, ui.TesterOptions{Dark: false})
	refLight.SetFocused(true)
	refLight.Frame()
	if ta, ok := refLight.Find("Danger action"); ok {
		refLight.Move(ta.X+ta.W/2, ta.Y+ta.H/2)
		refLight.Advance(ControlTransition.Duration)
		refLight.Frame()
	}
	lightImg := cloneTestRGBA(refLight.Image())

	darkChecked, darkSelected, darkQuiet := true, true, true
	refDark := ui.NewTesterWith(newView(&darkChecked, &darkSelected, &darkQuiet), 480, 320, ui.TesterOptions{Dark: true})
	refDark.SetFocused(true)
	refDark.Frame()
	if ta, ok := refDark.Find("Danger action"); ok {
		refDark.Move(ta.X+ta.W/2, ta.Y+ta.H/2)
		refDark.Advance(ControlTransition.Duration)
		refDark.Frame()
	}
	darkImg := cloneTestRGBA(refDark.Image())

	checked, selected, quiet := true, true, true
	tr := ui.NewTesterWith(newView(&checked, &selected, &quiet), 480, 320, ui.TesterOptions{Dark: false})
	tr.SetFocused(true)
	tr.Frame()

	swRect, okS := tr.Find("Theme switch")
	pillRect, okP := tr.Find("read")
	taRect, okT := tr.Find("Danger action")
	if !okS || !okP || !okT {
		t.Fatalf("missing test controls: switch=%v pill=%v textAction=%v", okS, okP, okT)
	}
	tr.Move(taRect.X+taRect.W/2, taRect.Y+taRect.H/2)
	tr.Advance(ControlTransition.Duration)
	tr.Frame()

	tr.SetDark(true)
	firstDark := cloneTestRGBA(tr.Image())
	if got, want := sampleRect(firstDark, swRect, 10, swRect.H/2), sampleRect(darkImg, swRect, 10, swRect.H/2); !closeColor(got, want) {
		t.Fatalf("ToggleSwitch track did not snap on first Light->Dark frame: got=%v want=%v", got, want)
	}
	if got, want := sampleRect(firstDark, pillRect, 6, pillRect.H/2), sampleRect(darkImg, pillRect, 6, pillRect.H/2); !closeColor(got, want) {
		t.Fatalf("TagPill fill did not snap on first Light->Dark frame: got=%v want=%v", got, want)
	}
	if got, want := sampleRect(firstDark, taRect, 3, 3), sampleRect(darkImg, taRect, 3, 3); !closeColor(got, want) {
		t.Fatalf("TextAction hover fill did not snap on first Light->Dark frame: got=%v want=%v", got, want)
	}

	tr.SetDark(false)
	firstLight := cloneTestRGBA(tr.Image())
	if got, want := sampleRect(firstLight, swRect, 10, swRect.H/2), sampleRect(lightImg, swRect, 10, swRect.H/2); !closeColor(got, want) {
		t.Fatalf("ToggleSwitch track did not snap on first Dark->Light frame: got=%v want=%v", got, want)
	}
	if got, want := sampleRect(firstLight, pillRect, 6, pillRect.H/2), sampleRect(lightImg, pillRect, 6, pillRect.H/2); !closeColor(got, want) {
		t.Fatalf("TagPill fill did not snap on first Dark->Light frame: got=%v want=%v", got, want)
	}
	if got, want := sampleRect(firstLight, taRect, 3, 3), sampleRect(lightImg, taRect, 3, 3); !closeColor(got, want) {
		t.Fatalf("TextAction hover fill did not snap on first Dark->Light frame: got=%v want=%v", got, want)
	}

	offChecked, offSelected, offQuiet := false, false, true
	refLightOff := ui.NewTesterWith(newView(&offChecked, &offSelected, &offQuiet), 480, 320, ui.TesterOptions{Dark: false})
	refLightOff.SetFocused(true)
	refLightOff.Frame()
	lightOffImg := cloneTestRGBA(refLightOff.Image())

	if err := tr.Click("Theme switch"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Click("read"); err != nil {
		t.Fatal(err)
	}
	tr.Move(0, 0)
	if checked || selected {
		t.Fatalf("intra-palette clicks did not toggle state: checked=%v selected=%v", checked, selected)
	}
	if got, want := sampleRect(tr.Image(), swRect, 30, swRect.H/2), sampleRect(lightOffImg, swRect, 30, swRect.H/2); closeColor(got, want) {
		t.Fatalf("ToggleSwitch intra-palette toggle unexpectedly snapped at t=0 without transition: got=%v want!=%v", got, want)
	}
	tr.Advance(ControlTransition.Duration)
	tr.Frame()
	settledOff := cloneTestRGBA(tr.Image())
	if got, want := sampleRect(settledOff, swRect, 30, swRect.H/2), sampleRect(lightOffImg, swRect, 30, swRect.H/2); !closeColor(got, want) {
		t.Fatalf("ToggleSwitch intra-palette toggle did not settle after %v: got=%v want=%v", ControlTransition.Duration, got, want)
	}
	if got, want := sampleRect(settledOff, pillRect, 6, pillRect.H/2), sampleRect(lightOffImg, pillRect, 6, pillRect.H/2); !closeColor(got, want) {
		t.Fatalf("TagPill intra-palette toggle did not settle after %v: got=%v want=%v", ControlTransition.Duration, got, want)
	}
}
