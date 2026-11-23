package theme

import (
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
