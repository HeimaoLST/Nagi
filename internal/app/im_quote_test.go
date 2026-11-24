package app

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestIMQuotePopoverMenuComposerStripDismissAndUTF8Bounds(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	msgs := locales.In("en")
	const userText = "Show the formula for π."
	const botBubble0 = "First bubble note."
	const botBubble1 = "Second bubble note."
	ct.Messages = []*message{
		{Key: "m-user-1", MessageID: "m-user-1", ID: 1, Kind: msgUser, Text: userText},
		{Key: "m-bot-1", MessageID: "m-bot-1", ID: 2, Kind: msgContact, Text: botBubble0 + "\n<<<NEXT>>>" + botBubble1},
		{Key: "m-bot-2", MessageID: "m-bot-2", ID: 3, Kind: msgContact, Text: "Latest trailing note."},
	}

	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.SetFocused(true)
	tr.Frame()

	if err := tr.RightClick(botBubble0); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	quoteRect, okQ := tr.Find(msgs.QuoteAction())
	copyRect, okC := tr.Find(msgs.CopyMessageLabel())
	if !okQ || !okC || quoteRect.Y >= copyRect.Y {
		t.Fatalf("expected Quote above Copy message: quote=%+v(%v) copy=%+v(%v)", quoteRect, okQ, copyRect, okC)
	}
	if err := choosePopoverMenuItem(tr, msgs.QuoteAction()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.DraftQuote == nil || ct.DraftQuote.Sender != "bot" || ct.DraftQuote.MessageID != "m-bot-1" ||
		ct.DraftQuote.AuthorName != ct.Name || ct.DraftQuote.Text != botBubble0 ||
		ct.DraftQuote.BubbleIndex == nil || *ct.DraftQuote.BubbleIndex != 0 {
		t.Fatalf("unexpected Bot Bubble 0 DraftQuote: %+v", ct.DraftQuote)
	}
	if !tr.HasText(msgs.QuoteReplyingTo(ct.Name)) || !tr.HasText(msgs.QuoteRemove()) {
		t.Fatalf("composer quote strip missing after Quote action: %v", tr.Texts())
	}
	if !tr.Focused(msgs.ComposerLabel(ct.Name)) {
		t.Fatalf("choosing Quote did not focus composer")
	}

	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if ct.DraftQuote != nil || tr.HasText(msgs.QuoteReplyingTo(ct.Name)) {
		t.Fatalf("Escape in empty focused composer did not clear quote: %+v", ct.DraftQuote)
	}

	if err := tr.RightClick(userText); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := choosePopoverMenuItem(tr, msgs.QuoteAction()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.DraftQuote == nil || ct.DraftQuote.Sender != "user" || ct.DraftQuote.MessageID != "m-user-1" ||
		ct.DraftQuote.AuthorName != msgs.QuoteAuthorYou() || ct.DraftQuote.Text != userText || ct.DraftQuote.BubbleIndex != nil {
		t.Fatalf("unexpected user bubble DraftQuote: %+v", ct.DraftQuote)
	}
	if !tr.HasText(msgs.QuoteReplyingTo(msgs.QuoteAuthorYou())) {
		t.Fatalf("composer user quote strip missing: %v", tr.Texts())
	}

	tr.Type("Follow-up draft")
	tr.Frame()
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if ct.DraftQuote == nil || ct.Draft != "Follow-up draft" {
		t.Fatalf("Escape with non-empty draft unexpectedly altered quote/draft: quote=%+v draft=%q", ct.DraftQuote, ct.Draft)
	}

	if err := tr.Click(msgs.QuoteRemove()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.DraftQuote != nil || ct.Draft != "Follow-up draft" {
		t.Fatalf("remove button did not clear quote while keeping draft: quote=%+v draft=%q", ct.DraftQuote, ct.Draft)
	}

	t.Run("utf8-byte-boundary-truncation", func(t *testing.T) {
		longAuthor := "\ufeff  " + strings.Repeat("南枝😀", 30) + "  "
		gotAuthor := quoteText(longAuthor, 120)
		if len(gotAuthor) == 0 || len(gotAuthor) > 120 || !utf8.ValidString(gotAuthor) || strings.HasPrefix(gotAuthor, "\ufeff") || strings.HasPrefix(gotAuthor, " ") {
			t.Fatalf("invalid truncated author: len=%d valid=%v text=%q", len(gotAuthor), utf8.ValidString(gotAuthor), gotAuthor)
		}
		longBody := " \n\ufeff" + strings.Repeat("圆周率π🚀", 120) + "\t\n"
		gotBody := quoteText(longBody, 1024)
		if len(gotBody) == 0 || len(gotBody) > 1024 || !utf8.ValidString(gotBody) || strings.HasPrefix(gotBody, "\ufeff") || strings.HasPrefix(gotBody, " ") {
			t.Fatalf("invalid truncated quote body: len=%d valid=%v", len(gotBody), utf8.ValidString(gotBody))
		}
	})

	t.Run("archived-and-previous-chat-omit-quote-menu", func(t *testing.T) {
		ct.Archived = true
		tr.Frame()
		if err := tr.RightClick(botBubble1); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if tr.HasText(msgs.QuoteAction()) || !tr.HasText(msgs.CopyMessageLabel()) {
			t.Fatalf("archived chat menu should omit Quote and keep Copy message: %v", tr.Texts())
		}
		tr.Key(0, ui.KeyEscape)
		tr.Frame()

		ct.Archived = false
		a.priorChats = &productConversationBrowser{
			contact:    ct,
			selectedID: "prior-quote-1",
			ownerID:    ct.ID,
			history: historyProjection{
				messages: []*message{
					{Key: "prior-u1", MessageID: "prior-u1", ID: 10, Kind: msgUser, Text: "Prior user message"},
					{Key: "prior-b1", MessageID: "prior-b1", ID: 11, Kind: msgContact, Text: "Prior bot reply"},
				},
			},
		}
		tr.Frame()
		if err := tr.RightClick("Prior bot reply"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if tr.HasText(msgs.QuoteAction()) || !tr.HasText(msgs.CopyMessageLabel()) {
			t.Fatalf("read-only previous chat should omit Quote and keep Copy message: %v", tr.Texts())
		}
	})
}

func TestIMSentQuoteRecallCenteringSingleBubbleHighlightAndExpiry(t *testing.T) {
	buildApp := func(t *testing.T) (*appModel, *contact) {
		t.Helper()
		a, ct := workspaceTestApp(t)
		a.windowWidth = 1040
		one := 1
		messages := make([]*message, 0, 40)
		for i := range 39 {
			id := fmt.Sprintf("m-%d", i)
			switch {
			case i == 2:
				messages = append(messages, &message{
					Key: id, MessageID: id, ID: i + 1, Kind: msgContact,
					Text: "Source bubble zero.\n<<<NEXT>>>Source bubble one.",
				})
			case i%2 == 0:
				messages = append(messages, &message{
					Key: id, MessageID: id, ID: i + 1, Kind: msgUser,
					Text: fmt.Sprintf("Filler user prompt %d", i),
				})
			default:
				messages = append(messages, &message{
					Key: id, MessageID: id, ID: i + 1, Kind: msgContact,
					Text: fmt.Sprintf("Filler bot answer %d with padding lines.", i),
				})
			}
		}
		messages = append(messages, &message{
			Key: "m-39", MessageID: "m-39", ID: 40, Kind: msgUser,
			Text: "Explain why the formula converges.",
			Quote: &pi.ProductQuote{
				MessageID:   "m-2",
				Sender:      "bot",
				AuthorName:  ct.Name,
				Text:        "Source bubble one.",
				BubbleIndex: &one,
			},
		})
		ct.Messages = messages
		return a, ct
	}

	msgs := locales.In("en")
	a, ct := buildApp(t)
	tr := ui.NewTesterWith(a.view, 1040, 720, ui.TesterOptions{Dark: true})
	tr.SetLocale("en")
	tr.Frame()
	ct.list.ScrollTo(len(ct.Messages)-1, ui.End)
	tr.Frame()

	if tr.HasText("Source bubble zero.") {
		t.Fatalf("expected source row to start off-screen before recall")
	}
	if err := tr.Click(msgs.QuoteJump(ct.Name)); err != nil {
		t.Fatal(err)
	}
	tr.Frame()

	viewport, okV := tr.Find(msgs.ConversationMessagesLabel())
	b0, ok0 := tr.Find("Source bubble zero.")
	b1, ok1 := tr.Find("Source bubble one.")
	if !okV || !ok0 || !ok1 {
		t.Fatalf("recalled bubbles missing after jump: viewport=%v b0=%v b1=%v", okV, ok0, ok1)
	}
	viewportCenter := viewport.Y + viewport.H/2
	b1Center := b1.Y + b1.H/2
	if math.Abs(float64(b1Center-viewportCenter)) > 120 {
		t.Fatalf("recalled bubble not centered near viewport center: b1Center=%.1f viewportCenter=%.1f", b1Center, viewportCenter)
	}
	if ct.recall.messageID != "m-2" || ct.recall.bubble != 1 {
		t.Fatalf("unexpected recall state: %+v", ct.recall)
	}

	during := cloneRGBA(tr.Image())
	tr.Advance(1650 * time.Millisecond)
	tr.Frame()
	after := cloneRGBA(tr.Image())
	if ct.recall != (quoteRecall{}) {
		t.Fatalf("recall highlight did not clear after 1.6s: %+v", ct.recall)
	}
	b1Diff := diffPixelsInRect(during, after, int(b1.X)-15, int(b1.Y)-9, int(b1.X+b1.W)+15, int(b1.Y+b1.H)+9)
	b0Diff := diffPixelsInRect(during, after, int(b0.X)-15, int(b0.Y)-9, int(b0.X+b0.W)+15, int(b0.Y+b0.H)+9)
	if b1Diff == 0 {
		t.Fatalf("quoted Bubble 1 did not render a visible highlight ring")
	}
	if b0Diff != 0 {
		t.Fatalf("sibling Bubble 0 unexpectedly changed %d pixels during Bubble 1 highlight", b0Diff)
	}

	t.Run("reduce-motion-static-ring-and-missing-target-noop", func(t *testing.T) {
		ra, rct := buildApp(t)
		rtr := ui.NewTesterWith(ra.view, 1040, 720, ui.TesterOptions{
			Dark:        true,
			Preferences: ui.Preferences{ReduceMotion: true},
		})
		rtr.SetLocale("en")
		rtr.Frame()
		rct.list.ScrollTo(len(rct.Messages)-1, ui.End)
		rtr.Frame()

		if err := rtr.Click(msgs.QuoteJump(rct.Name)); err != nil {
			t.Fatal(err)
		}
		rtr.Frame()
		rb1, ok := rtr.Find("Source bubble one.")
		if !ok {
			t.Fatal("reduce-motion recall bubble missing")
		}
		t0 := cloneRGBA(rtr.Image())
		rtr.Advance(1500 * time.Millisecond)
		rtr.Frame()
		t1500 := cloneRGBA(rtr.Image())
		if changed := diffPixelsInRect(t0, t1500, int(rb1.X)-15, int(rb1.Y)-9, int(rb1.X+rb1.W)+15, int(rb1.Y+rb1.H)+9); changed != 0 {
			t.Fatalf("reduce-motion highlight animated (%d pixels changed at 1500ms)", changed)
		}
		rtr.Advance(150 * time.Millisecond)
		rtr.Frame()
		if rct.recall != (quoteRecall{}) {
			t.Fatalf("reduce-motion recall did not clear after 1.6s: %+v", rct.recall)
		}

		rct.Messages = []*message{
			{
				Key: "orphan-1", MessageID: "orphan-1", ID: 1, Kind: msgUser,
				Text: "Still readable without source",
				Quote: &pi.ProductQuote{
					MessageID:  "outside-history-999",
					Sender:     "bot",
					AuthorName: rct.Name,
					Text:       "Archived earlier snippet",
				},
			},
		}
		rtr.Frame()
		if err := rtr.Click(msgs.QuoteJump(rct.Name)); err != nil {
			t.Fatal(err)
		}
		rtr.Frame()
		if rct.recall != (quoteRecall{}) || !rtr.HasText("Archived earlier snippet") {
			t.Fatalf("missing recall target did not degrade gracefully: recall=%+v texts=%v", rct.recall, rtr.Texts())
		}
	})
}
