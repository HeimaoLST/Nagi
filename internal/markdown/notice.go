package markdown

import (
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

var (
	diagnosticSecret     = regexp.MustCompile(`(?i)\b(api[_-]?key|token)\s*[:=]\s*[^\s,;]+|\bauthorization\s*:\s*(?:bearer\s+)?[^\s,;]+|\bbearer\s+[^\s,;]+`)
	diagnosticJSONSecret = regexp.MustCompile(`(?i)"(?:api[_-]?key|token|secret|password|authorization)"\s*:\s*"[^"]*"`)
	diagnosticSkKey      = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`)
	diagnosticURLCred    = regexp.MustCompile(`://[^/\s@]+@`)
)

func Truncate(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

func SafeDiagnostic(text string) string {
	text = strings.TrimSpace(text)
	text = diagnosticSecret.ReplaceAllString(text, "[redacted]")
	text = diagnosticJSONSecret.ReplaceAllString(text, "[redacted]")
	text = diagnosticSkKey.ReplaceAllString(text, "[redacted]")
	text = diagnosticURLCred.ReplaceAllString(text, "://[redacted]@")
	return Truncate(text, 500)
}

func RecoveryNotice(c *ui.Context, t *ui.Theme, summary, detail string, expanded bool, toggle, settings func(), settingsLabel string) ui.Element {
	msgs := locales.In(c.Locale())
	detail = SafeDiagnostic(detail)
	var entry ui.Element
	ui.Column(c).FillWidth().MinWidth(0).Gap(6).Children(func() {
		ui.Text(c, summary).FontSize(theme.TypeBody).FixedLineHeight(theme.LineBody).TextColor(t.Warning)
		ui.Row(c).Wrap().Gap(8).Children(func() {
			if settings != nil {
				entry = theme.SecondaryButton(c, settingsLabel).Label(settingsLabel).OnClick(settings)
			}
			if detail != "" && toggle != nil {
				label := msgs.DetailsAction()
				if expanded {
					label = msgs.HideDetailsAction()
				}
				theme.SecondaryButton(c, label).Label(label).OnClick(toggle)
			}
		})
		if expanded && detail != "" {
			ui.Text(c, msgs.RecoveryTechnicalDetailsHint()).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
			Reader(c, detail, ReaderOptions{PlainText: true, CopyLabel: msgs.CopyErrorDetailsLabel()}).
				FontSize(theme.TypeBody).FixedLineHeight(theme.LineBody).TextColor(t.TextMuted)
		}
	})
	return entry
}
