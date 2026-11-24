package theme

import (
	"encoding/json"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsThemeContrastAndSurfaceSeparation(t *testing.T) {
	customLight := ui.LightTheme()
	customLight.Background = ui.Hex("#DCE2D5")
	customLight.Surface = ui.Hex("#E3E8DC")
	customLight.Text = ui.Hex("#6B7065")
	customLight.TextMuted = ui.Hex("#9EA398")
	customLight.Accent = ui.Hex("#E5D96B")
	customLight.AccentText = ui.Hex("#FFFFFF")
	customLight.AccentHover = ui.Hex("#F3EC9E")
	customLight.AccentPressed = ui.Hex("#FAF6C3")
	customLight.Success = ui.Hex("#64C886")
	customLight.Warning = ui.Hex("#E7B42F")
	customLight.Danger = ui.Hex("#EB6F75")

	customDark := ui.DarkTheme()
	customDark.Background = ui.Hex("#20242B")
	customDark.Surface = ui.Hex("#323842")
	customDark.Text = ui.Hex("#6F7785")
	customDark.TextMuted = ui.Hex("#59606D")
	customDark.Accent = ui.Hex("#2B4C8C")
	customDark.AccentText = ui.Hex("#0D0D0D")
	customDark.AccentHover = ui.Hex("#395EA8")
	customDark.AccentPressed = ui.Hex("#213B6E")
	customDark.Success = ui.Hex("#235D3A")
	customDark.Warning = ui.Hex("#6F4B08")
	customDark.Danger = ui.Hex("#7A1F24")

	cases := []struct {
		name string
		base *ui.Theme
	}{
		{"builtin light", ResolveWorkbenchTheme(false).Theme},
		{"builtin dark", ResolveWorkbenchTheme(true).Theme},
		{"custom light", customLight},
		{"custom dark", customDark},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SettingsTheme(tc.base)
			if got.Background == got.Surface {
				t.Fatalf("settings canvas and card surfaces did not separate: %+v", got.Surface)
			}
			bgs := []struct {
				name  string
				color ui.Color
			}{
				{"Background", got.Background},
				{"Surface", got.Surface},
				{"SurfaceHover", got.SurfaceHover},
				{"SurfacePressed", got.SurfacePressed},
			}
			inks := []struct {
				name  string
				color ui.Color
			}{
				{"Text", got.Text},
				{"TextMuted", got.TextMuted},
				{"Success", got.Success},
				{"Warning", got.Warning},
				{"Danger", got.Danger},
			}
			for _, ink := range inks {
				for _, bg := range bgs {
					if ratio := PaletteContrast(ink.color, bg.color); ratio < 4.5 {
						t.Fatalf("%s on %s contrast = %.3f, want >= 4.5", ink.name, bg.name, ratio)
					}
				}
			}
			for _, accent := range []struct {
				name  string
				color ui.Color
			}{
				{"Accent", got.Accent},
				{"AccentHover", got.AccentHover},
				{"AccentPressed", got.AccentPressed},
			} {
				if ratio := PaletteContrast(got.AccentText, accent.color); ratio < 4.5 {
					t.Fatalf("AccentText on %s contrast = %.3f, want >= 4.5", accent.name, ratio)
				}
			}
		})
	}
}

func TestBubbleContactContrastAndTonalSeparation(t *testing.T) {
	makeCustom := func(id, kind, reading, bubbleContact, text, muted string) WorkbenchTheme {
		t.Helper()
		payload := map[string]any{
			"version": 1,
			"id":      id,
			"name":    id,
			"kind":    kind,
			"colors": map[string]string{
				"background":      reading,
				"surface":         reading,
				"surface_hover":   "#2C2E28",
				"surface_pressed": "#363830",
				"border":          "#555555",
				"text":            text,
				"text_muted":      muted,
				"accent":          "#3B6EA5",
				"accent_hover":    "#487BB2",
				"accent_pressed":  "#2D5A8B",
				"accent_text":     "#FFFFFF",
				"selection":       "#333333",
				"focus":           "#777777",
				"danger":          "#C43D44",
				"warning":         "#A66D00",
				"success":         "#2E6B45",
				"rail":            reading,
				"panel":           reading,
				"bubble_user":     "#DDE8DD",
				"bubble_contact":  bubbleContact,
			},
		}
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		custom, fileErr := decodeCustomTheme(id+".json", data)
		if fileErr != nil {
			t.Fatalf("decodeCustomTheme(%s): %+v", id, fileErr)
		}
		return custom.Palette
	}

	cases := []struct {
		name    string
		palette WorkbenchTheme
	}{
		{"builtin light", ResolveWorkbenchTheme(false)},
		{"builtin dark", ResolveWorkbenchTheme(true)},
		{"custom light matching reading derives tint", makeCustom("custom-light-same", AppearanceLight, "#F8F8F6", "#F8F8F6", "#7A7A75", "#9B9B96")},
		{"custom dark explicit low-contrast inks", makeCustom("custom-dark-explicit", AppearanceDark, "#14161A", "#232730", "#687080", "#555C6A")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.palette
			if p.BubbleContact == p.Reading {
				t.Fatalf("BubbleContact did not separate from Reading: %+v", p.BubbleContact)
			}
			for _, ink := range []struct {
				name  string
				color ui.Color
			}{
				{"ContactText", p.ContactText},
				{"ContactMuted", p.ContactMuted},
				{"ContactLink", p.ContactLink},
			} {
				if ratio := PaletteContrast(ink.color, p.BubbleContact); ratio < 4.5 {
					t.Fatalf("%s on BubbleContact contrast = %.3f, want >= 4.5", ink.name, ratio)
				}
			}
		})
	}
}

func TestComposerThemeContrastAndDefaultRailWidth(t *testing.T) {
	if DefaultLeftPaneWidth != 272 || EffectiveCenterWidth != 516 {
		t.Fatalf("layout widths = left:%g center:%g, want 272/516", DefaultLeftPaneWidth, EffectiveCenterWidth)
	}
	makeCustom := func(id, kind, bg, surface, text, muted string) WorkbenchTheme {
		t.Helper()
		payload := map[string]any{
			"version": 1,
			"id":      id,
			"name":    id,
			"kind":    kind,
			"colors": map[string]string{
				"background":      bg,
				"surface":         surface,
				"surface_hover":   "#2C2E28",
				"surface_pressed": "#363830",
				"border":          "#555555",
				"text":            text,
				"text_muted":      muted,
				"accent":          "#3B6EA5",
				"accent_hover":    "#487BB2",
				"accent_pressed":  "#2D5A8B",
				"accent_text":     "#FFFFFF",
				"selection":       "#333333",
				"focus":           "#777777",
				"danger":          "#C43D44",
				"warning":         "#A66D00",
				"success":         "#2E6B45",
				"rail":            bg,
				"panel":           surface,
				"bubble_user":     "#DDE8DD",
				"bubble_contact":  surface,
			},
		}
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		custom, fileErr := decodeCustomTheme(id+".json", data)
		if fileErr != nil {
			t.Fatalf("decodeCustomTheme(%s): %+v", id, fileErr)
		}
		return custom.Palette
	}

	cases := []struct {
		name    string
		palette WorkbenchTheme
	}{
		{"builtin light", ResolveWorkbenchTheme(false)},
		{"builtin dark", ResolveWorkbenchTheme(true)},
		{"custom light low contrast muted", makeCustom("custom-composer-light", AppearanceLight, "#F6F6F2", "#FFFFFF", "#2A2B28", "#A3A59E")},
		{"custom dark low contrast muted", makeCustom("custom-composer-dark", AppearanceDark, "#14161A", "#1E2026", "#E5E7EB", "#59606D")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.palette
			if p.ComposerSurface.A != 255 || p.ComposerBorder.A != 255 || p.ComposerChipBg.A != 255 {
				t.Fatalf("composer tokens must be opaque: surface=%+v border=%+v chip=%+v", p.ComposerSurface, p.ComposerBorder, p.ComposerChipBg)
			}
			for _, bg := range []struct {
				name  string
				color ui.Color
			}{
				{"ComposerSurface", p.ComposerSurface},
				{"ComposerChipBg", p.ComposerChipBg},
			} {
				for _, ink := range []struct {
					name  string
					color ui.Color
				}{
					{"ComposerText", p.ComposerText},
					{"ComposerMuted", p.ComposerMuted},
				} {
					if ratio := PaletteContrast(ink.color, bg.color); ratio < 4.5 {
						t.Fatalf("%s on %s contrast = %.3f, want >= 4.5", ink.name, bg.name, ratio)
					}
				}
			}
		})
	}
}
