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
	ui.Row(c).FillWidth().Padding(12, 16, 0).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).Role(ui.RoleHeading).Level(2).FontSize(TypeGroupTitle).FontWeight(600).FixedLineHeight(LineGroupTitle).TextColor(t.Text)
	})
}

func SettingsPreferenceRow(c *ui.Context, t *ui.Theme, label, description string, control func(), last ...bool) {
	row := ui.Row(c).FillWidth().MinWidth(0).Wrap().AlignItems(ui.Center).Gap(12).Padding(12, 0)
	if len(last) == 0 || !last[0] {
		row.BorderWidth(0, 0, 1, 0).BorderColor(t.Border).BorderStyle(ui.BorderDashed)
	}
	row.Children(func() {
		ui.Column(c).Basis(280).Grow(1).Shrink(1).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, label).FontSize(TypeLabel).FontWeight(500).FixedLineHeight(LineLabel).TextColor(t.Text)
			if description != "" {
				text := ui.Text(c, description).FontSize(TypeMeta).FixedLineHeight(LineMeta).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
				AppTooltip(c, text, description)
			}
		})
		ui.Column(c).Basis(320).Grow(1).Shrink(1).MinWidth(0).AlignItems(ui.End).Gap(8).Children(control)
	})
}
