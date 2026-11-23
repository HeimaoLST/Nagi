package theme

import "github.com/egoist/mygo/ui"

func PreferenceGroup(c *ui.Context, t *ui.Theme, children func()) {
	ui.Column(c).FillWidth().Padding(16).Draw(func(p *ui.Painter, r ui.Rect) {
		PaintPreferenceFace(p, r, 12, 2, t.Background, t.Border)
	}).Children(children)
}

func PreferenceSection(c *ui.Context, t *ui.Theme, title string, children func()) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		PreferenceSectionHeader(c, t, title)
		PreferenceGroup(c, t, children)
	})
}

func PreferenceSectionHeader(c *ui.Context, t *ui.Theme, title string) {
	ui.Column(c).FillWidth().Height(44).Padding(0, 16).Justify(ui.End).Children(func() {
		ui.Row(c).FillWidth().Height(28).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(TypeMeta).FontWeight(400).FixedLineHeight(LineMeta).TextColor(t.TextMuted)
		})
	})
}

func SettingsPreferenceRow(c *ui.Context, t *ui.Theme, label, description string, control func(), last ...bool) {
	row := ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(12).Padding(12, 0)
	if len(last) == 0 || !last[0] {
		row.BorderWidth(0, 0, 1, 0).BorderColor(t.Border).BorderStyle(ui.BorderDashed)
	}
	row.Children(func() {
		ui.Column(c).Basis(240).Grow(1).Shrink(1).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, label).FontSize(TypeLabel).FontWeight(400).FixedLineHeight(LineLabel).TextColor(t.Text)
			if description != "" {
				ui.Text(c, description).FontSize(TypeMeta).FontWeight(400).FixedLineHeight(LineMeta).TextColor(t.TextMuted)
			}
		})
		ui.Column(c).Basis(388).Grow(1).Shrink(1).MinWidth(0).AlignItems(ui.End).Gap(8).Children(control)
	})
}
