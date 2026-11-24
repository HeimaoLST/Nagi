package app

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"

	"github.com/HeimaoLST/Nagi/locales"
)

const replyBubbleSeparator = "<<<NEXT>>>"
const replyBubbleBeat = 360 * time.Millisecond

func splitReplyBubbles(text string, streaming bool) []string {
	bubbles, _ := splitReplyBubblesState(text, streaming)
	return bubbles
}

func splitReplyBubblesState(text string, streaming bool) ([]string, int) {
	end := len(text)
	partial := 0
	if streaming {
		for n := min(end, len(replyBubbleSeparator)-1); n > 0; n-- {
			if strings.HasSuffix(text, replyBubbleSeparator[:n]) {
				partial = n
				break
			}
		}
	}
	if partial == 0 && !strings.Contains(text, replyBubbleSeparator) {
		if strings.TrimSpace(text) == "" {
			return nil, 0
		}
		return []string{text}, 0
	}

	var doc *parser.Document
	if strings.ContainsAny(text, "`~") {
		doc = parser.Parse(text)
		doc.SetStreaming(streaming)
	}
	blockIndex, inlineIndex := 1, 0
	inCode := func(offset int) bool {
		for doc != nil && blockIndex < doc.Len() {
			b := doc.Block(blockIndex)
			if b.Start > offset {
				break
			}
			if offset < b.End {
				if b.Kind == parser.CodeBlock && b.Fenced {
					return true
				}
				inlines := doc.Inlines(blockIndex)
				for inlineIndex < len(inlines) && inlines[inlineIndex].Start <= offset {
					in := inlines[inlineIndex]
					if in.Kind == parser.Code && offset < in.End {
						return true
					}
					inlineIndex++
				}
				if b.Next == blockIndex+1 {
					return false
				}
			}
			blockIndex++
			inlineIndex = 0
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

	start := 0
	for search := 0; search < len(text); {
		i := strings.Index(text[search:], replyBubbleSeparator)
		if i < 0 {
			break
		}
		offset := search + i
		search = offset + len(replyBubbleSeparator)
		if !inCode(offset) {
			appendBubble(strings.TrimRight(text[start:offset], " \t"))
			tail := text[search:]
			start = len(text) - len(strings.TrimLeft(tail, " \t"))
			search = start
		}
	}
	if partial > 0 && end-partial >= start && !inCode(end-partial) {
		end -= partial
		lineStart := max(start, strings.LastIndexByte(text[:end], '\n')+1)
		if strings.TrimSpace(text[lineStart:end]) == "" {
			end = lineStart
		}
	}
	if start == 0 {
		if strings.TrimSpace(text[:end]) == "" {
			return nil, 0
		}
		return []string{text[:end]}, 0
	}
	completed := min(len(bubbles), maxBubbles-1)
	appendBubble(text[start:end])
	if overflow.Len() > 0 {
		bubbles[maxBubbles-1] = overflow.String()
	}
	return bubbles, completed
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
	bubbles, completed := splitReplyBubblesState(m.Text, m.Streaming)
	visible, typing := len(bubbles), false
	live := m.liveReveal
	reduceMotion := c.Preferences().ReduceMotion
	if live {
		eligible := len(bubbles)
		if m.Streaming {
			eligible = completed
		}
		now := c.Now()
		if reduceMotion {
			m.revealedBubbles = eligible
		} else if m.revealedBubbles < eligible && !now.Before(m.nextRevealAt) {
			m.revealedBubbles++
			m.nextRevealAt = now.Add(replyBubbleBeat)
		}
		if m.revealedBubbles < eligible {
			c.After(max(16*time.Millisecond, m.nextRevealAt.Sub(now)))
		}
		visible = min(m.revealedBubbles, eligible)
		typing = m.Streaming || visible < len(bubbles)
		m.liveReveal = typing
	}
	if visible == 0 && !typing {
		if m.AlertTitle == "" {
			return
		}
		bubbles, visible = []string{""}, 1
	}
	palette := a.workTheme
	mdTheme := *messageMarkdownTheme
	mdTheme.Text, mdTheme.Muted, mdTheme.Link = palette.ContactText, palette.ContactMuted, palette.ContactLink
	options := messageReaderOptions(c, false)
	options.Theme = &mdTheme
	options = a.readerOptions(options, scope, base)
	ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Start).Children(func() {
		ui.Column(c).MaxWidth(userMessageMaxWidth).MinWidth(0).Shrink(1).AlignItems(ui.Start).Gap(4).Children(func() {
			for i, text := range bubbles[:visible] {
				c.Key(i)
				opts := options
				if i > 0 {
					opts.LinkScope = scope + "/bubble:" + strconv.Itoa(i)
				}
				last := i == len(bubbles)-1
				bubble := markdownMessageBubble{Background: palette.BubbleContact, PaddingX: 12, PaddingY: 8, Radius: 12, MenuOnlyCopy: true}
				if last && m.AlertTitle != "" {
					bubble.Footer = func(fc *ui.Context) { a.alert(fc, t, m) }
				}
				opts.MessageBubble = &bubble
				reader := markdownReader(c, text, opts).MaxWidthPercent(100).MinWidth(0).FontSize(typeBody).FixedLineHeight(lineBody)
				if m.revealedBubbles > 0 && !reduceMotion {
					reader.Transition(ui.ElementTransition{Duration: 180 * time.Millisecond, Enter: &ui.Motion{Opacity: 0, Y: 4}})
				}
			}
			if typing {
				a.replyTypingBubble(c)
			}
		})
	})
}

func (a *appModel) replyTypingBubble(c *ui.Context) {
	palette := a.workTheme
	reduceMotion := c.Preferences().ReduceMotion
	bubble := ui.Column(c).Key("reply-typing").AlignSelf(ui.Start).Radius(12).Padding(10, 14).
		Background(palette.BubbleContact).Role(ui.RoleStatus).Label(locales.In(c.Locale()).BotTyping())
	bubble.Children(func() {
		ui.Box(c).Size(21, 5).Draw(func(p *ui.Painter, r ui.Rect) {
			phase := float64(p.Now().UnixMilli()%1400) * (2 * math.Pi / 1400)
			for i := range 3 {
				alpha := float32(.65)
				if !reduceMotion {
					alpha = .60 + .23*float32(math.Sin(phase-float64(i)*.80))
				}
				p.Fill(ui.Rect{X: r.X + 8*float32(i), Y: r.Y, W: 5, H: 5}, palette.ContactMuted.Alpha(alpha), 2.5)
			}
			if !reduceMotion {
				p.After(33 * time.Millisecond)
			}
		})
	})
}
