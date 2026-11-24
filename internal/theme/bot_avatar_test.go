package theme

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestBotAvatarSVGsSupportedAndRenderable(t *testing.T) {
	svgs := BotAvatarSVGs()
	if len(svgs) != len(botAvatarShapes)*2 {
		t.Fatalf("BotAvatarSVGs() returned %d SVGs, want %d", len(svgs), len(botAvatarShapes)*2)
	}
	for i, svg := range svgs {
		if svg == nil {
			t.Fatalf("BotAvatarSVGs()[%d] is nil", i)
		}
		if unsupported := svg.Unsupported(); len(unsupported) != 0 {
			t.Fatalf("BotAvatarSVGs()[%d] has unsupported SVG features: %v", i, unsupported)
		}
	}

	for _, dark := range []bool{false, true} {
		mode := "light"
		if dark {
			mode = "dark"
		}
		wt := ResolveWorkbenchTheme(dark)
		for _, shape := range botAvatarShapes {
			t.Run(mode+"/"+shape.id, func(t *testing.T) {
				tr := ui.NewTesterWith(func(c *ui.Context) {
					c.SetTheme(wt.Theme)
					ui.Box(c).Fill().Background(wt.Reading).Center().Children(func() {
						ui.Box(c).Size(48, 48).Center().Children(func() {
							ui.Icon(c, shape.body).Size(48, 48).TextColor(botAvatarColors[0].light)
							ui.Icon(c, shape.eyes).Absolute().Left(0).Top(0).Size(48, 48).TextColor(botAvatarColors[0].eyeLight)
						})
					})
				}, 64, 64, ui.TesterOptions{Dark: dark})
				tr.Frame()
				img := tr.Image()
				bg := img.RGBAAt(0, 0)
				nonBg := 0
				eyeHits := 0
				for y := 0; y < 64; y++ {
					for x := 0; x < 64; x++ {
						px := img.RGBAAt(x, y)
						if px != bg {
							nonBg++
						}
						if px.R > 240 && px.G > 235 && px.B > 220 {
							eyeHits++
						}
					}
				}
				if nonBg < 100 {
					t.Fatalf("%s/%s rendered too few body pixels: %d", mode, shape.id, nonBg)
				}
				if eyeHits == 0 {
					t.Fatalf("%s/%s rendered zero visible eye pixels", mode, shape.id)
				}
			})
		}
	}
}

func TestResolveBotAvatarPinningDeterminismCoverageAndColors(t *testing.T) {
	lightTheme := ResolveWorkbenchTheme(false).Theme
	darkTheme := ResolveWorkbenchTheme(true).Theme

	for _, tc := range []struct {
		id      string
		name    string
		builtin bool
	}{
		{"builtin:pandorabox", "PandoraBox", true},
		{"any-custom-id", "PandoraBox", false},
		{"another-id", "  pandorabox  ", false},
		{"sys-1", "Custom Name", true},
	} {
		got := ResolveBotAvatar(lightTheme, tc.id, tc.name, tc.builtin)
		if got.ShapeID != "duo_stack" || got.ColorID != "terracotta" {
			t.Fatalf("PandoraBox pin mismatch for (%q,%q,%v): shape=%q color=%q", tc.id, tc.name, tc.builtin, got.ShapeID, got.ColorID)
		}
	}

	first := ResolveBotAvatar(lightTheme, "bot-stable-42", "Alpha", false)
	renamed := ResolveBotAvatar(lightTheme, "bot-stable-42", "Completely Renamed", false)
	if first.ShapeID != renamed.ShapeID || first.ColorID != renamed.ColorID {
		t.Fatalf("stable ID avatar changed on rename: %+v vs %+v", first, renamed)
	}

	fallbackA := ResolveBotAvatar(lightTheme, "", "Cedar", false)
	fallbackB := ResolveBotAvatar(lightTheme, "   ", "Cedar", false)
	if fallbackA.ShapeID != fallbackB.ShapeID || fallbackA.ColorID != fallbackB.ColorID {
		t.Fatalf("empty ID name fallback not deterministic: %+v vs %+v", fallbackA, fallbackB)
	}

	bg, ink := ContactAvatarColors(lightTheme, "Cedar")
	if bg != fallbackA.BodyColor || ink != fallbackA.EyeColor {
		t.Fatalf("ContactAvatarColors did not delegate to ResolveBotAvatar: (%+v,%+v) vs (%+v,%+v)", bg, ink, fallbackA.BodyColor, fallbackA.EyeColor)
	}
	pbBg, pbInk := ContactAvatarColors(darkTheme, "  pandorabox ")
	pbDark := ResolveBotAvatar(darkTheme, "", "PandoraBox", true)
	if pbBg != pbDark.BodyColor || pbInk != pbDark.EyeColor {
		t.Fatalf("ContactAvatarColors PandoraBox dark mismatch")
	}

	shapes := make(map[string]int)
	colors := make(map[string]int)
	pairs := make(map[string]int)
	var obsidianID string
	for i := range 200 {
		id := fmt.Sprintf("bot-corpus-%03d", i)
		av := ResolveBotAvatar(lightTheme, id, "Bot", false)
		shapes[av.ShapeID]++
		colors[av.ColorID]++
		pairs[av.ShapeID+"/"+av.ColorID]++
		if av.ColorID == "obsidian" && obsidianID == "" {
			obsidianID = id
		}
	}
	if len(shapes) != len(botAvatarShapes) {
		t.Fatalf("shape coverage = %d/%d: %v", len(shapes), len(botAvatarShapes), shapes)
	}
	if len(colors) != len(botAvatarColors) {
		t.Fatalf("color coverage = %d/%d: %v", len(colors), len(botAvatarColors), colors)
	}
	if len(pairs) <= len(botAvatarColors) {
		t.Fatalf("shape and color hashes appear coupled: only %d distinct pairs", len(pairs))
	}
	if obsidianID == "" {
		t.Fatal("corpus never hit obsidian color")
	}

	obsLight := ResolveBotAvatar(lightTheme, obsidianID, "Obs", false)
	obsDark := ResolveBotAvatar(darkTheme, obsidianID, "Obs", false)
	if obsLight.BodyColor != ui.Hex("#222320") || obsLight.EyeColor != ui.Hex("#FFFDF4") {
		t.Fatalf("obsidian light colors mismatch: body=%+v eye=%+v", obsLight.BodyColor, obsLight.EyeColor)
	}
	if obsDark.BodyColor != ui.Hex("#E8E5DA") || obsDark.EyeColor != ui.Hex("#1D1E1B") {
		t.Fatalf("obsidian dark polarity flip mismatch: body=%+v eye=%+v", obsDark.BodyColor, obsDark.EyeColor)
	}

	lightPalette := ResolveWorkbenchTheme(false)
	darkPalette := ResolveWorkbenchTheme(true)
	for _, c := range botAvatarColors {
		lightRatio := PaletteContrast(c.eyeLight, c.light)
		darkRatio := PaletteContrast(c.eyeDark, c.dark)
		lightBgRatio := PaletteContrast(c.light, lightPalette.Rail)
		darkBgRatio := PaletteContrast(c.dark, darkPalette.Rail)
		if lightRatio < 2.2 || darkRatio < 2.2 {
			t.Fatalf("color %s eye-on-body contrast too low: light=%.2f dark=%.2f", c.id, lightRatio, darkRatio)
		}
		if lightBgRatio < 2.5 || darkBgRatio < 2.5 {
			t.Fatalf("color %s body-on-rail contrast too low: light=%.2f dark=%.2f", c.id, lightBgRatio, darkBgRatio)
		}
		if c.id == "obsidian" && (lightRatio < 12.0 || darkRatio < 12.0) {
			t.Fatalf("obsidian contrast regression: light=%.2f dark=%.2f", lightRatio, darkRatio)
		}
		t.Logf("color=%-10s light_eye=%.2f dark_eye=%.2f light_rail=%.2f dark_rail=%.2f", c.id, lightRatio, darkRatio, lightBgRatio, darkBgRatio)
	}
}

func TestBotAvatarGalleryCaptures(t *testing.T) {
	dir := os.Getenv("NAGI_AVATAR_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set NAGI_AVATAR_CAPTURE_DIR to capture stone avatar gallery PNGs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		mode := "light"
		if dark {
			mode = "dark"
		}
		wt := ResolveWorkbenchTheme(dark)
		tr := ui.NewTesterWith(func(c *ui.Context) {
			c.SetTheme(wt.Theme)
			ui.Column(c).Fill().Background(wt.Reading).Padding(20).Gap(14).Children(func() {
				ui.Text(c, fmt.Sprintf("Stone Avatar 8x9 Gallery (%s) — 48px + 24px", mode)).FontSize(16).FontWeight(600).TextColor(wt.Theme.Text)
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Width(86)
					for _, col := range botAvatarColors {
						ui.Box(c).Width(78).Center().Children(func() {
							ui.Text(c, col.id).FontSize(11).TextColor(wt.Theme.TextMuted)
						})
					}
				})
				for _, sh := range botAvatarShapes {
					ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
						ui.Box(c).Width(86).Children(func() {
							ui.Text(c, sh.id).FontSize(12).FontWeight(500).TextColor(wt.Theme.Text)
						})
						for _, col := range botAvatarColors {
							bodyC, eyeC := col.light, col.eyeLight
							if dark {
								bodyC, eyeC = col.dark, col.eyeDark
							}
							ui.Row(c).Width(78).Height(56).Radius(8).Background(wt.Rail).Center().Gap(4).Children(func() {
								ui.Box(c).Size(44, 44).Center().Children(func() {
									ui.Icon(c, sh.body).Size(44, 44).TextColor(bodyC)
									ui.Icon(c, sh.eyes).Absolute().Left(0).Top(0).Size(44, 44).TextColor(eyeC)
								})
								ui.Box(c).Size(24, 24).Center().Children(func() {
									ui.Icon(c, sh.body).Size(24, 24).TextColor(bodyC)
									ui.Icon(c, sh.eyes).Absolute().Left(0).Top(0).Size(24, 24).TextColor(eyeC)
								})
							})
						}
					})
				}
			})
		}, 940, 620, ui.TesterOptions{Dark: dark})
		tr.Frame()
		writeThemePNG(t, filepath.Join(dir, fmt.Sprintf("gallery-8x9-%s.png", mode)), tr.Image())
	}
}

func writeThemePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
