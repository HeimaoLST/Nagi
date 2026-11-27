package app

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"

	"github.com/HeimaoLST/Nagi/internal/pi"
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
		if replySeparatorPosition(text[:offset]) && !inCode(offset) {
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

func replySeparatorPosition(prefix string) bool {
	prefix = strings.TrimRight(prefix, " \t")
	if prefix == "" {
		return true
	}
	last, _ := utf8.DecodeLastRuneInString(prefix)
	return strings.ContainsRune("\r\n.!?。！？…)）」”\"", last)
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
		if stamp.IsZero() || m.Kind == msgActivity || m.Kind == msgNotice {
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
		if kind != msgActivity && kind != msgNotice && messages[index-1].Kind == kind {
			gap = 4
		}
		row.Padding(gap, 0, 0, 0)
	}
	row.Children(children)
}

func botReplyHasVisibleBubble(m *message, now time.Time, reduceMotion bool) bool {
	if m.AlertTitle != "" {
		return true
	}
	bubbles, completed := splitReplyBubblesState(m.Text, m.Streaming)
	visible := len(bubbles)
	if m.liveReveal {
		eligible := len(bubbles)
		if m.Streaming {
			eligible = completed
		}
		revealed := m.revealedBubbles
		if reduceMotion {
			revealed = eligible
		} else if revealed < eligible && !now.Before(m.nextRevealAt) {
			revealed++
		}
		visible = min(revealed, eligible)
	}
	return visible > 0
}

func latestReadUserMessageIndex(messages []*message, now time.Time, reduceMotion bool) int {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m == nil || m.Kind == msgActivity || m.Kind == msgNotice {
			continue
		}
		if m.Kind == msgContact {
			if botReplyHasVisibleBubble(m, now, reduceMotion) {
				return -1
			}
			continue
		}
		if m.Kind != msgUser {
			return -1
		}
		if m.ReadByBot || m.History {
			return i
		}
		return -1
	}
	return -1
}

func (a *appModel) botReplyBubbles(c *ui.Context, t *ui.Theme, m *message, scope, base string, ct *contact, recall *quoteRecall) {
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
				bubble.BeforeCopyMenu = a.quoteMenu(c, ct, m, i, text)
				bubble.Highlight = recall.highlight(c, m, i)
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

const (
	quoteRecallDuration = 1600 * time.Millisecond
	quoteRecallFade     = 180 * time.Millisecond
	userQuoteBubble     = -1
)

type quoteRecall struct {
	messageID string
	bubble    int
	until     time.Time
}

func quoteMessageID(m *message) string {
	switch {
	case m.MessageID != "":
		return m.MessageID
	case m.Key != "":
		return m.Key
	case m.ID != 0:
		return strconv.Itoa(m.ID)
	default:
		return ""
	}
}

func matchesQuotedMessage(m *message, id string) bool {
	return id != "" && (m.Key == id || quoteMessageID(m) == id)
}

func quoteText(text string, limit int) string {
	trim := func(r rune) bool { return unicode.IsSpace(r) || r == '\ufeff' }
	text = strings.TrimFunc(text, trim)
	if len(text) <= limit {
		return text
	}
	for !utf8.RuneStart(text[limit]) {
		limit--
	}
	return strings.TrimRightFunc(text[:limit], trim)
}

func (a *appModel) quoteDraftEditable(ct *contact) bool {
	return ct != nil && !ct.Archived && !a.shutdownStarted && !a.dataRecoveryBlocksChanges()
}

func (a *appModel) quoteMenu(c *ui.Context, ct *contact, m *message, bubble int, text string) func(*ui.Menu) {
	if !a.quoteDraftEditable(ct) || strings.TrimSpace(text) == "" || quoteMessageID(m) == "" {
		return nil
	}
	return func(menu *ui.Menu) {
		msgs := locales.In(c.Locale())
		if !menu.Item(msgs.QuoteAction()).Chosen() || a.selectedContact() != ct || !a.quoteDraftEditable(ct) {
			return
		}
		quote := &pi.ProductQuote{MessageID: quoteMessageID(m), Text: quoteText(text, 1024), Sender: "bot", AuthorName: quoteText(ct.Name, 120), BubbleIndex: &bubble}
		if bubble == userQuoteBubble {
			quote.Sender, quote.AuthorName, quote.BubbleIndex = "user", msgs.QuoteAuthorYou(), nil
		}
		if quote.Text != "" && quote.AuthorName != "" {
			ct.DraftQuote = quote
			a.draftChanged(ct)
			a.focusComposer = true
		}
	}
}

func (a *appModel) removeDraftQuote(ct *contact) bool {
	if ct.DraftQuote == nil || !a.quoteDraftEditable(ct) {
		return false
	}
	ct.DraftQuote = nil
	a.draftChanged(ct)
	a.focusComposer = true
	return true
}

func (a *appModel) composerQuote(c *ui.Context, t *ui.Theme, ct *contact) {
	quote := ct.DraftQuote
	msgs := locales.In(c.Locale())
	ui.Row(c).Key("draft-quote").FillWidth().MinWidth(0).Radius(8).Padding(6, 8, 6, 10).
		Background(a.workTheme.ComposerChipBg).Gap(8).AlignItems(ui.Center).Children(func() {
		quoteStripText(c, t, msgs.QuoteReplyingTo(quote.AuthorName), quote.Text)
		composerRemoveButton(c, t, msgs.QuoteRemove()).Size(24, 24).Disabled(!a.quoteDraftEditable(ct)).OnClick(func() {
			a.removeDraftQuote(ct)
		})
	})
}

func quoteStripText(c *ui.Context, t *ui.Theme, author, text string) {
	ui.Box(c).Width(2.5).AlignSelf(ui.Stretch).Shrink(0).Radius(1.25).Background(t.Accent.Alpha(.65))
	ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
		ui.Text(c, author).FontSize(typeMeta).FontWeight(500).FixedLineHeight(lineMeta).
			SingleLine().Ellipsis("…").TextColor(t.Text)
		ui.Text(c, strings.Join(strings.Fields(text), " ")).FontSize(typeMeta).FixedLineHeight(lineMeta).
			SingleLine().Ellipsis("…").TextColor(t.TextMuted)
	})
}

func (a *appModel) sentQuote(c *ui.Context, t *ui.Theme, quote *pi.ProductQuote, click func()) {
	button := ui.ButtonBase(c).Key("sent-quote").AlignSelf(ui.Start).MaxWidthPercent(100).MinWidth(0).
		Radius(8).Padding(5, 10).Gap(8).AlignItems(ui.Center).
		Label(locales.In(c.Locale()).QuoteJump(quote.AuthorName))
	background := a.workTheme.ComposerChipBg
	switch {
	case button.Pressed():
		background = t.SurfacePressed
	case button.Hovered():
		background = t.SurfaceHover
	}
	button.Background(background).Children(func() { quoteStripText(c, t, quote.AuthorName, quote.Text) })
	button.OnClick(click)
}

func recallQuotedMessage(c *ui.Context, messages []*message, list *ui.ListState, recall *quoteRecall, quote *pi.ProductQuote) {
	bubble := userQuoteBubble
	if quote.BubbleIndex != nil {
		bubble = *quote.BubbleIndex
	}
	for i, m := range messages {
		if matchesQuotedMessage(m, quote.MessageID) {
			list.ScrollTo(i, ui.Center)
			*recall = quoteRecall{messageID: quote.MessageID, bubble: bubble, until: c.Now().Add(quoteRecallDuration)}
			c.After(quoteRecallDuration)
			return
		}
	}
}

func (r *quoteRecall) highlight(c *ui.Context, m *message, bubble int) float32 {
	if r.bubble != bubble || !matchesQuotedMessage(m, r.messageID) {
		return 0
	}
	remaining := r.until.Sub(c.Now())
	if remaining <= 0 {
		*r = quoteRecall{}
		return 0
	}
	const opacity = .8
	if c.Preferences().ReduceMotion {
		c.After(remaining)
		return opacity
	}
	if remaining > quoteRecallFade {
		c.After(remaining - quoteRecallFade)
		return opacity
	}
	c.AnimationFrame()
	return opacity * float32(remaining) / float32(quoteRecallFade)
}
