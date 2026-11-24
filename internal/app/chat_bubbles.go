package app

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"

	"github.com/HeimaoLST/Nagi/locales"
)

const replyBubbleSeparator = "<<<NEXT>>>"

func splitReplyBubbles(text string, streaming bool) []string {
	partial := -1
	if streaming {
		start := strings.LastIndexByte(text, '\n') + 1
		tail := strings.TrimLeftFunc(text[start:], unicode.IsSpace)
		if tail != "" && len(tail) < len(replyBubbleSeparator) && strings.HasPrefix(replyBubbleSeparator, tail) {
			partial = start
		}
	}
	if partial < 0 && !strings.Contains(text, replyBubbleSeparator) {
		return []string{text}
	}

	var doc *parser.Document
	if strings.Contains(text, "```") || strings.Contains(text, "~~~") {
		doc = parser.Parse(text)
	}
	nextBlock := 1
	inFence := func(offset int) bool {
		for doc != nil && nextBlock < doc.Len() {
			b := doc.Block(nextBlock)
			if b.Start > offset {
				return false
			}
			if b.Kind == parser.CodeBlock && b.Fenced && offset < b.End {
				return true
			}
			nextBlock++
		}
		return false
	}

	const maxBubbles = 4
	var bubbles []string
	var overflow strings.Builder
	appendBubble := func(text string) {
		text = trimBubbleBlankLines(text)
		if text == "" {
			return
		}
		if len(bubbles) < maxBubbles {
			if bubbles == nil {
				bubbles = make([]string, 0, maxBubbles)
			}
			bubbles = append(bubbles, text)
			return
		}
		if overflow.Len() == 0 {
			overflow.WriteString(bubbles[maxBubbles-1])
		}
		overflow.WriteString("\n\n")
		overflow.WriteString(text)
	}

	start, end, offset := 0, len(text), 0
	for line := range strings.Lines(text) {
		lineStart := offset
		offset += len(line)
		if lineStart == partial {
			if !inFence(lineStart) {
				end = lineStart
			}
			break
		}
		if strings.TrimSpace(line) == replyBubbleSeparator && !inFence(lineStart) {
			appendBubble(text[start:lineStart])
			start = offset
		}
	}
	if start == 0 {
		if end != len(text) && strings.TrimSpace(text[:end]) == "" {
			return nil
		}
		return []string{text[:end]}
	}
	appendBubble(text[start:end])
	if overflow.Len() > 0 {
		bubbles[maxBubbles-1] = overflow.String()
	}
	return bubbles
}

func trimBubbleBlankLines(text string) string {
	start, end, offset := -1, 0, 0
	for line := range strings.Lines(text) {
		if strings.TrimSpace(line) != "" {
			if start < 0 {
				start = offset
			}
			end = offset + len(strings.TrimRight(line, "\r\n"))
		}
		offset += len(line)
	}
	if start < 0 {
		return ""
	}
	return text[start:end]
}

func sameLocalDay(a, b time.Time) bool {
	a, b = a.Local(), b.Local()
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

func chatTimestampDividers(messages []*message, now time.Time, locale string) []string {
	var labels []string
	var anchor time.Time
	for i, m := range messages {
		stamp := m.Timestamp
		if stamp.IsZero() || m.Kind == msgActivity {
			continue
		}
		if anchor.IsZero() || !sameLocalDay(anchor, stamp) || stamp.Sub(anchor) >= 5*time.Minute {
			if labels == nil {
				labels = make([]string, len(messages))
			}
			labels[i] = formatChatTimestamp(stamp, now, locale)
			anchor = stamp
		}
	}
	return labels
}

func formatChatTimestamp(stamp, now time.Time, locale string) string {
	stamp, now = stamp.Local(), now.Local()
	clock := stamp.Format("15:04")
	if sameLocalDay(stamp, now) {
		return clock
	}
	msgs := locales.In(locale)
	year, month, day := now.Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, now.Location())
	if sameLocalDay(stamp, today.AddDate(0, 0, -1)) {
		return msgs.ChatTimestampYesterday(clock)
	}
	if stamp.Before(today) && !stamp.Before(today.AddDate(0, 0, -6)) {
		day := i18n.FormatDate(locale, stamp, i18n.DateOptions{Skeleton: "EEE"})
		return msgs.ChatTimestampWeekday(day, clock)
	}
	skeleton := "MMMd"
	if stamp.Year() != year {
		skeleton = "yMMMd"
	}
	date := i18n.FormatDate(locale, stamp, i18n.DateOptions{Skeleton: skeleton})
	return msgs.ChatTimestampDate(date, clock)
}

func chatMessageRow(c *ui.Context, t *ui.Theme, messages []*message, dividers []string, index int, children func()) {
	row := ui.Column(c).FillWidth().MinWidth(0)
	if len(dividers) != 0 && dividers[index] != "" {
		row.Children(func() {
			ui.Text(c, dividers[index]).AlignSelf(ui.Center).Padding(12, 0, 8, 0).
				FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			children()
		})
		return
	}
	if index > 0 {
		gap := float32(14)
		kind := messages[index].Kind
		if kind != msgActivity && messages[index-1].Kind == kind {
			gap = 4
		}
		row.Padding(gap, 0, 0, 0)
	}
	row.Children(children)
}

func (a *appModel) botReplyBubbles(c *ui.Context, t *ui.Theme, m *message, scope, base string) {
	bubbles := splitReplyBubbles(m.Text, m.Streaming)
	if len(bubbles) == 0 {
		if m.AlertTitle == "" {
			return
		}
		bubbles = []string{""}
	}
	palette := a.workTheme
	mdTheme := *messageMarkdownTheme
	mdTheme.Text, mdTheme.Muted, mdTheme.Link = palette.ContactText, palette.ContactMuted, palette.ContactLink
	options := messageReaderOptions(c, false)
	options.Theme = &mdTheme
	options = a.readerOptions(options, scope, base)
	ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Start).Children(func() {
		ui.Column(c).MaxWidth(userMessageMaxWidth).MinWidth(0).Shrink(1).AlignItems(ui.Start).Gap(4).Children(func() {
			for i, text := range bubbles {
				c.Key(i)
				opts := options
				if i > 0 {
					opts.LinkScope = scope + "/bubble:" + strconv.Itoa(i)
				}
				last := i == len(bubbles)-1
				opts.Streaming = last && m.Streaming
				bubble := markdownMessageBubble{Background: palette.BubbleContact, PaddingX: 12, PaddingY: 8, Radius: 12}
				if last && m.AlertTitle != "" {
					bubble.Footer = func(fc *ui.Context) { a.alert(fc, t, m) }
				}
				opts.MessageBubble = &bubble
				markdownReader(c, text, opts).MaxWidthPercent(100).MinWidth(0).FontSize(typeBody).FixedLineHeight(lineBody)
			}
		})
	})
}
