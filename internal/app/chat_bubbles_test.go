package app

import (
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestSplitReplyBubblesAndRailPreview(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		streaming bool
		want      []string
	}{
		{
			name: "plain single bubble",
			text: "Short single reply.",
			want: []string{"Short single reply."},
		},
		{
			name: "mid-clause marker inside prose stays literal",
			text: "Keep <<<NEXT>>> inside prose.",
			want: []string{"Keep <<<NEXT>>> inside prose."},
		},
		{
			name: "mid-clause Chinese parenthetical mention stays literal",
			text: "圆周率可以这样计算（就是那个 <<<NEXT>>> 分句的规矩）。\n<<<NEXT>>>这是用 Machin 公式算的。",
			want: []string{"圆周率可以这样计算（就是那个 <<<NEXT>>> 分句的规矩）。", "这是用 Machin 公式算的。"},
		},
		{
			name: "post-sentence punctuation marker splits cleanly",
			text: "第一句。<<<NEXT>>>第二句。",
			want: []string{"第一句。", "第二句。"},
		},
		{
			name: "missing trailing newline after marker splits cleanly",
			text: "圆周率如下。\n<<<NEXT>>>这是用 Machin 公式算的。",
			want: []string{"圆周率如下。", "这是用 Machin 公式算的。"},
		},
		{
			name: "inline single backtick code span keeps marker literal",
			text: "Use `<<<NEXT>>>` literally.\n<<<NEXT>>>\nSecond.",
			want: []string{"Use `<<<NEXT>>>` literally.", "Second."},
		},
		{
			name: "inline double backtick code span keeps marker literal",
			text: "Use ``<<<NEXT>>>`` in docs.<<<NEXT>>>Next bubble.",
			want: []string{"Use ``<<<NEXT>>>`` in docs.", "Next bubble."},
		},
		{
			name:      "inline streaming partial prefix suppressed",
			text:      "Hello <<<NE",
			streaming: true,
			want:      []string{"Hello "},
		},
		{
			name: "three bubbles with crlf whitespace and blank lines",
			text: "\n  First bubble.  \r\n\r\n  <<<NEXT>>> \t \r\n\nSecond bubble.\r\n<<<NEXT>>>\r\n\nThird bubble.\n\n",
			want: []string{"  First bubble.  ", "Second bubble.", "Third bubble."},
		},
		{
			name: "backtick fenced code immunity",
			text: "Before\n```text\n<<<NEXT>>>\n```\n<<<NEXT>>>\nAfter",
			want: []string{"Before\n```text\n<<<NEXT>>>\n```", "After"},
		},
		{
			name: "tilde fenced code immunity",
			text: "Before\n~~~markdown\n<<<NEXT>>>\n~~~\n<<<NEXT>>>\nAfter",
			want: []string{"Before\n~~~markdown\n<<<NEXT>>>\n~~~", "After"},
		},
		{
			name:      "unclosed streaming fence keeps marker inside code",
			text:      "Before\n```\n<<<NEXT>>>\n<<<NE",
			streaming: true,
			want:      []string{"Before\n```\n<<<NEXT>>>\n<<<NE"},
		},
		{
			name:      "streaming hides partial single angle bracket",
			text:      "First\n<",
			streaming: true,
			want:      []string{"First\n"},
		},
		{
			name:      "streaming hides partial triple angle brackets",
			text:      "First\n<<<",
			streaming: true,
			want:      []string{"First\n"},
		},
		{
			name:      "streaming hides partial <<<NE",
			text:      "First\n<<<NEXT>>>\nSecond\n  <<<NE",
			streaming: true,
			want:      []string{"First", "Second"},
		},
		{
			name:      "streaming hides partial <<<NEXT",
			text:      "First\n<<<NEXT",
			streaming: true,
			want:      []string{"First\n"},
		},
		{
			name:      "streaming only partial prefix yields empty",
			text:      "<<<NE",
			streaming: true,
			want:      nil,
		},
		{
			name:      "completed message keeps partial literal",
			text:      "First\n<<<NE",
			streaming: false,
			want:      []string{"First\n<<<NE"},
		},
		{
			name: "four bubble overflow cap merges extras into fourth",
			text: "B1\n<<<NEXT>>>\nB2\n<<<NEXT>>>\nB3\n<<<NEXT>>>\nB4\n<<<NEXT>>>\nB5\n<<<NEXT>>>\nB6",
			want: []string{"B1", "B2", "B3", "B4\n\nB5\n\nB6"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.text
			got := splitReplyBubbles(raw, tc.streaming)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("splitReplyBubbles(%q, %t) = %#v, want %#v", raw, tc.streaming, got, tc.want)
			}
			if raw != tc.text {
				t.Fatalf("raw text mutated: %q", raw)
			}
		})
	}

	a := testApp(t)
	msgs := locales.In("en")
	ct := a.contacts[0]
	ct.Messages = []*message{
		{ID: 1, Kind: msgUser, Text: "Older user message"},
		{ID: 2, Kind: msgContact, Text: "<<<NE", Streaming: true},
	}
	if got := a.snippet(ct, msgs); got != msgs.SnippetUser("Older user message") {
		t.Fatalf("streaming partial-only snippet = %q, want %q", got, msgs.SnippetUser("Older user message"))
	}
	ct.Messages[1].Text = "Draft ready.\n<<<NE"
	if got := a.snippet(ct, msgs); got != "Draft ready." || strings.Contains(got, "<<<") {
		t.Fatalf("streaming partial-tail snippet = %q, want %q", got, "Draft ready.")
	}
	ct.Messages[1].Text = "First reply.\n<<<NEXT>>>\nSecond reply."
	ct.Messages[1].Streaming = false
	if got := a.snippet(ct, msgs); got != "First reply." || strings.Contains(got, "<<<NEXT>>>") {
		t.Fatalf("multi-bubble snippet = %q, want %q", got, "First reply.")
	}
	ct.Messages[1].Text = "Inline `<<<NEXT>>>` marker in first bubble."
	if got := a.snippet(ct, msgs); strings.Contains(got, "<<<NEXT>>>") {
		t.Fatalf("rail snippet leaked separator literal: %q", got)
	}
}

func TestChatTimestampDividersAndFormatting(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 11, 23, 18, 0, 0, 0, loc)
	at := func(day, hour, min int) time.Time {
		return time.Date(2026, 11, day, hour, min, 0, 0, loc)
	}
	messages := []*message{
		{ID: 1, Kind: msgActivity, Text: "Ignored activity", Timestamp: at(23, 14, 20)},
		{ID: 2, Kind: msgUser, Text: "No timestamp"},
		{ID: 3, Kind: msgUser, Text: "Anchor 14:30", Timestamp: at(23, 14, 30)},
		{ID: 4, Kind: msgContact, Text: "Plus 3m omitted", Timestamp: at(23, 14, 33)},
		{ID: 5, Kind: msgActivity, Text: "Activity does not reset anchor", Timestamp: at(23, 14, 36)},
		{ID: 6, Kind: msgContact, Text: "Zero stamp ignored"},
		{ID: 7, Kind: msgUser, Text: "Plus 5m triggers 14:35", Timestamp: at(23, 14, 35)},
		{ID: 8, Kind: msgContact, Text: "Plus 3m from 14:35 omitted", Timestamp: at(23, 14, 38)},
		{ID: 9, Kind: msgUser, Text: "Plus 5m from 14:35 triggers 14:40", Timestamp: at(23, 14, 40)},
		{ID: 10, Kind: msgUser, Text: "Before midnight", Timestamp: at(23, 23, 59)},
		{ID: 11, Kind: msgContact, Text: "After midnight 2m later", Timestamp: at(24, 0, 1)},
	}
	dividers := chatTimestampDividers(messages, now, "en")
	wantShown := map[int]bool{2: true, 6: true, 8: true, 9: true, 10: true}
	for i := range messages {
		shown := i < len(dividers) && dividers[i] != ""
		if shown != wantShown[i] {
			t.Fatalf("message[%d] (%s) divider=%q shown=%t want=%t", i, messages[i].Text, dividers[i], shown, wantShown[i])
		}
	}
	if dividers[2] != "14:30" || dividers[6] != "14:35" || dividers[8] != "14:40" || dividers[9] != "23:59" || !strings.Contains(dividers[10], "00:01") {
		t.Fatalf("unexpected divider labels: %#v", dividers)
	}

	if got := chatTimestampDividers([]*message{{ID: 1, Kind: msgActivity, Timestamp: at(23, 14, 30)}, {ID: 2, Kind: msgUser}}, now, "en"); got != nil {
		t.Fatalf("expected nil dividers when only activity/zero timestamps exist: %#v", got)
	}

	for _, tc := range []struct {
		locale        string
		yesterdayWord string
	}{
		{"en", "Yesterday 14:30"},
		{"zh-Hans", "昨天 14:30"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			today := formatChatTimestamp(at(23, 14, 30), now, tc.locale)
			if today != "14:30" {
				t.Fatalf("today = %q, want 14:30", today)
			}
			yesterday := formatChatTimestamp(at(22, 14, 30), now, tc.locale)
			if yesterday != tc.yesterdayWord {
				t.Fatalf("yesterday = %q, want %q", yesterday, tc.yesterdayWord)
			}
			weekday := formatChatTimestamp(at(20, 9, 15), now, tc.locale)
			if !strings.Contains(weekday, "09:15") || weekday == "09:15" || strings.Contains(weekday, "2026") || strings.Contains(weekday, tc.yesterdayWord[:2]) {
				t.Fatalf("weekday = %q", weekday)
			}
			sameYear := formatChatTimestamp(time.Date(2026, 8, 5, 8, 5, 0, 0, loc), now, tc.locale)
			if !strings.Contains(sameYear, "08:05") || sameYear == "08:05" || strings.Contains(sameYear, "2026") {
				t.Fatalf("sameYear = %q", sameYear)
			}
			earlierYear := formatChatTimestamp(time.Date(2025, 12, 31, 23, 45, 0, 0, loc), now, tc.locale)
			if !strings.Contains(earlierYear, "2025") || !strings.Contains(earlierYear, "23:45") {
				t.Fatalf("earlierYear = %q", earlierYear)
			}
		})
	}
}

func horizontalEdgeEnergy(img *image.RGBA, rect image.Rectangle) int64 {
	bounds := rect.Intersect(img.Bounds())
	var total int64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x+1 < bounds.Max.X; x++ {
			a := img.RGBAAt(x, y)
			b := img.RGBAAt(x+1, y)
			diff := func(u, v uint8) int64 {
				if u > v {
					return int64(u - v)
				}
				return int64(v - u)
			}
			total += diff(a.R, b.R) + diff(a.G, b.G) + diff(a.B, b.B)
		}
	}
	return total
}

func TestIMChatBubblesSpacingLinksCopyAndFrostedCreateBotModal(t *testing.T) {
	a := testApp(t)
	a.windowWidth = 1280
	ct := a.contacts[0]
	now := time.Now()
	t1 := now.Add(-10 * time.Minute)
	t2 := t1.Add(1 * time.Minute)
	t3 := t1.Add(2 * time.Minute)
	rawReply := "First bubble with [Docs](https://example.invalid/one)\n<<<NEXT>>>\nSecond bubble with [Spec](https://example.invalid/two)"
	ct.Messages = []*message{
		{ID: 1, Kind: msgUser, Text: "User first line", Timestamp: t1},
		{ID: 2, Kind: msgUser, Text: "User second line", Timestamp: t2},
		{ID: 3, Kind: msgContact, Text: rawReply, Timestamp: t3},
	}

	tr := ui.NewTester(a.view, 1280, 720)
	tr.SetLocale("en")
	tr.SetDark(true)
	tr.SetFocused(true)
	tr.Frame()

	msgs := locales.In("en")
	if ct.Messages[2].Text != rawReply {
		t.Fatalf("rendering mutated raw reply text: %q", ct.Messages[2].Text)
	}
	if tr.HasText(replyBubbleSeparator) {
		t.Fatalf("rendered transcript leaked raw separator: %v", tr.Texts())
	}
	for _, want := range []string{"User first line", "User second line", "First bubble with", "Second bubble with", "Docs", "Spec"} {
		if !tr.HasText(want) {
			t.Fatalf("chat transcript missing %q: %v", want, tr.Texts())
		}
	}

	u1, ok1 := tr.Find("User first line")
	u2, ok2 := tr.Find("User second line")
	b1, ok3 := tr.Find("Docs")
	b2, ok4 := tr.Find("Spec")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		t.Fatalf("missing message rects: ok=(%t,%t,%t,%t)", ok1, ok2, ok3, ok4)
	}
	sameSpeakerGap := u2.Y - (u1.Y + u1.H)
	speakerChangeGap := b1.Y - (u2.Y + u2.H)
	if speakerChangeGap <= sameSpeakerGap+5 {
		t.Fatalf("expected speaker-change gap > same-speaker gap: same=%.1f change=%.1f", sameSpeakerGap, speakerChangeGap)
	}
	if b2.Y <= b1.Y {
		t.Fatalf("second reply bubble did not stack below first: b1=%+v b2=%+v", b1, b2)
	}

	if err := tr.Click("Docs"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Click("Spec"); err != nil {
		t.Fatal(err)
	}
	if urls := tr.OpenedURLs(); !slices.Equal(urls, []string{"https://example.invalid/one", "https://example.invalid/two"}) {
		t.Fatalf("bubble links opened %v", urls)
	}

	for _, mode := range []struct {
		name   string
		locale string
		dark   bool
		width  int
	}{
		{"en dark", "en", true, 1280},
		{"zh-Hans light", "zh-Hans", false, 1040},
	} {
		t.Run(mode.name, func(t *testing.T) {
			a.windowWidth = mode.width
			tr.SetSize(mode.width, 720)
			tr.SetLocale(mode.locale)
			tr.SetDark(mode.dark)
			tr.Frame()
			m := locales.In(mode.locale)
			railRegion := image.Rect(8, 80, 220, 620)
			beforeEnergy := horizontalEdgeEnergy(tr.Image(), railRegion)
			if beforeEnergy < 10000 {
				t.Fatalf("unexpectedly low unblurred rail edge energy: %d", beforeEnergy)
			}

			if err := tr.Click(m.AvatarAddContact()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.createBot == nil || !tr.HasText(m.CreateBotTitle()) {
				t.Fatalf("Create Bot modal did not open: %v", tr.Texts())
			}
			afterEnergy := horizontalEdgeEnergy(tr.Image(), railRegion)
			if afterEnergy*5 >= beforeEnergy {
				t.Fatalf("frosted backdrop did not blur underlying rail enough: before=%d after=%d", beforeEnergy, afterEnergy)
			}

			tr.ClickAt(20, 20)
			tr.Frame()
			if a.createBot != nil || !tr.Focused(m.AvatarAddContact()) {
				t.Fatalf("backdrop click did not dismiss frosted modal or return focus: open=%v", a.createBot != nil)
			}

			if err := tr.Click(m.AvatarAddContact()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			tr.Key(0, ui.KeyEscape)
			tr.Frame()
			if a.createBot != nil || !tr.Focused(m.AvatarAddContact()) {
				t.Fatalf("Escape did not dismiss frosted modal or return focus: open=%v", a.createBot != nil)
			}
		})
	}

	ct.Messages = []*message{{ID: 3, Kind: msgContact, Text: rawReply, Timestamp: t3}}
	tr.SetLocale("en")
	tr.Frame()
	if tr.HasText(msgs.CopyMessageLabel()) {
		t.Fatalf("outer Bot bubble copy button should be absent: %v", tr.Texts())
	}
	if err := tr.RightClick("Docs"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := choosePopoverMenuItem(tr, msgs.CopyMessageLabel()); err != nil {
		t.Fatal(err)
	}
	if tr.Clipboard() != "First bubble with [Docs](https://example.invalid/one)" {
		t.Fatalf("Bot bubble context menu copy copied %q", tr.Clipboard())
	}
}

func TestLatestReadUserMessageIndexAndVisibleReplyBubble(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	unread := []*message{{Kind: msgUser, Text: "hi"}}
	if got := latestReadUserMessageIndex(unread, now, false); got != -1 {
		t.Fatalf("unread message index = %d, want -1", got)
	}

	readWithActivity := []*message{
		{Kind: msgUser, Text: "first", History: true},
		{Kind: msgUser, Text: "second", ReadByBot: true},
		{Kind: msgActivity, Text: "tool log"},
	}
	if got := latestReadUserMessageIndex(readWithActivity, now, false); got != 1 {
		t.Fatalf("read message before activity index = %d, want 1", got)
	}

	streamingHidden := []*message{
		{Kind: msgUser, Text: "second", ReadByBot: true},
		{Kind: msgContact, Text: "streaming reply", Streaming: true, liveReveal: true, revealedBubbles: 0},
	}
	if got := latestReadUserMessageIndex(streamingHidden, now, false); got != 0 {
		t.Fatalf("streaming unrevealed reply index = %d, want 0", got)
	}
	if got := latestReadUserMessageIndex(streamingHidden, now, true); got != 0 {
		t.Fatalf("streaming incomplete sentence with reduceMotion index = %d, want 0", got)
	}

	streamingTwo := []*message{
		{Kind: msgUser, Text: "second", ReadByBot: true},
		{Kind: msgContact, Text: "First.\n<<<NEXT>>>\nSecond", Streaming: true, liveReveal: true, revealedBubbles: 0},
	}
	if got := latestReadUserMessageIndex(streamingTwo, now, true); got != -1 {
		t.Fatalf("reduceMotion revealed eligible bubble index = %d, want -1", got)
	}

	alertVisible := []*message{
		{Kind: msgUser, Text: "second", ReadByBot: true},
		{Kind: msgContact, AlertTitle: "Interrupted"},
	}
	if got := latestReadUserMessageIndex(alertVisible, now, false); got != -1 {
		t.Fatalf("visible alert reply index = %d, want -1", got)
	}
}
