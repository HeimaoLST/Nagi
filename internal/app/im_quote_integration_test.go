//go:build integration

package app

import (
	"context"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestPiAppIMQuotePersistSendRecallAndReopen(t *testing.T) {
	const (
		turn1Prompt = "Show the formula for π."
		turn1B0     = "圆周率可以这样计算（就是那个 <<<NEXT>>> 分句的规矩）。"
		turn1B1     = "这是用 Machin 公式算的，条件是 1 < 2。"
		turn2Prompt = "Explain why the formula converges."
		turn2Reply  = "The arctangent series converges because its inputs have magnitude below one."
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	provider := &piAppProvider{limit: 2}
	if err := provider.install([]piAppCall{}, turn1B0+"\n<<<NEXT>>>"+turn1B1, nil, nil); err != nil {
		t.Fatal(err)
	}
	root := prepareProductWorkbench(t, provider)
	if err := writeProfileJSON(guiStorePath(), storedState{
		Version:  contactStoreVersion,
		Selected: "xuhu",
		Contacts: []storedContact{
			{ID: "xuhu", Name: "Xuhu", Workspace: root, Draft: turn1Prompt},
		},
	}); err != nil {
		t.Fatal(err)
	}

	msgs := locales.In("en")
	w := openProductWorkbench(t)
	xuhu := w.app.contactByID("xuhu")
	w.wait(func() bool { return w.app.selectedContact() == xuhu && xuhu.Loaded })
	w.click(msgs.SendAction())
	w.wait(func() bool {
		return provider.count() == 1 && xuhu.SendState == sendIdle && xuhu.Status != statusWorking
	})
	w.frame(func() { w.view.Advance(450 * time.Millisecond) })
	w.wait(func() bool { return w.view.HasText(turn1B0) && w.view.HasText(turn1B1) })

	var rightErr, menuErr error
	w.frame(func() {
		rightErr = w.view.RightClick(turn1B1)
		if rightErr == nil {
			w.view.Frame()
			menuErr = choosePopoverMenuItem(w.view, msgs.QuoteAction())
		}
	})
	if rightErr != nil {
		t.Fatalf("right click Bubble 1: %v", rightErr)
	}
	if menuErr != nil {
		t.Fatalf("choose Quote on Bubble 1: %v", menuErr)
	}

	conv, err := w.client.GetConversation(ctx, xuhu.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.Draft == nil || conv.Draft.Quote == nil || conv.Draft.Quote.BubbleIndex == nil ||
		*conv.Draft.Quote.BubbleIndex != 1 || conv.Draft.Quote.Sender != "bot" ||
		conv.Draft.Quote.AuthorName != "Xuhu" || conv.Draft.Quote.Text != turn1B1 {
		t.Fatalf("persisted draft quote mismatch: %+v", conv.Draft)
	}
	quotedMsgID := conv.Draft.Quote.MessageID
	if quotedMsgID == "" {
		t.Fatal("persisted draft quote missing message_id")
	}

	w.shutdown(ctx)

	if err := provider.install([]piAppCall{}, turn2Reply, nil, nil); err != nil {
		t.Fatal(err)
	}
	w2 := openProductWorkbench(t)
	xuhu2 := w2.app.contactByID("xuhu")
	w2.wait(func() bool {
		return w2.app.selectedContact() == xuhu2 && xuhu2.Loaded &&
			xuhu2.DraftQuote != nil && xuhu2.DraftQuote.BubbleIndex != nil && *xuhu2.DraftQuote.BubbleIndex == 1 &&
			w2.view.HasText(msgs.QuoteReplyingTo("Xuhu"))
	})

	w2.click(msgs.ComposerLabel("Xuhu"))
	w2.frame(func() { w2.view.Type(turn2Prompt) })
	w2.click(msgs.SendAction())
	w2.wait(func() bool {
		return provider.count() == 2 && xuhu2.SendState == sendIdle && xuhu2.Status != statusWorking &&
			xuhu2.DraftQuote == nil
	})
	w2.frame(func() { w2.view.Advance(450 * time.Millisecond) })
	w2.wait(func() bool { return w2.view.HasText(turn2Reply) })

	provider.mu.Lock()
	var secondBody string
	if len(provider.bodies) >= 2 {
		secondBody = string(provider.bodies[1])
	}
	providerProblems := append([]string(nil), provider.problems...)
	provider.mu.Unlock()
	if len(providerProblems) != 0 {
		t.Fatalf("provider errors: %v", providerProblems)
	}
	if !strings.Contains(secondBody, "<quoted_message") || !strings.Contains(secondBody, "1 &lt; 2") || !strings.Contains(secondBody, turn2Prompt) {
		t.Fatalf("provider request did not carry escaped <quoted_message> + prompt: %s", secondBody)
	}

	w2.pump.do(func() {
		var found bool
		for _, m := range xuhu2.Messages {
			if m.Kind == msgUser && m.Text == turn2Prompt {
				found = true
				if m.Quote == nil || m.Quote.BubbleIndex == nil || *m.Quote.BubbleIndex != 1 {
					t.Errorf("sent user row lost quote metadata: %+v", m.Quote)
				}
			}
			if strings.Contains(m.Text, "<quoted_message") {
				t.Errorf("rendered chat row leaked <quoted_message> XML: %q", m.Text)
			}
		}
		if !found {
			t.Errorf("clean raw follow-up user message missing from live transcript")
		}
	})

	w2.shutdown(ctx)

	w3 := openProductWorkbench(t)
	xuhu3 := w3.app.contactByID("xuhu")
	w3.wait(func() bool {
		return w3.app.selectedContact() == xuhu3 && xuhu3.Loaded &&
			w3.view.HasText(turn1B0) && w3.view.HasText(turn1B1) &&
			w3.view.HasText(turn2Prompt) && w3.view.HasText(msgs.QuoteJump("Xuhu"))
	})

	w3.frame(func() { w3.view.SetDark(true) })
	var b0, b1 struct{ x0, y0, x1, y1 int }
	var ok0, ok1 bool
	w3.pump.do(func() {
		rect0, found0 := w3.view.Find(turn1B0)
		rect1, found1 := w3.view.Find(turn1B1)
		ok0, ok1 = found0, found1
		if found0 && found1 {
			b0 = struct{ x0, y0, x1, y1 int }{int(rect0.X) - 15, int(rect0.Y) - 9, int(rect0.X+rect0.W) + 15, int(rect0.Y+rect0.H) + 9}
			b1 = struct{ x0, y0, x1, y1 int }{int(rect1.X) - 15, int(rect1.Y) - 9, int(rect1.X+rect1.W) + 15, int(rect1.Y+rect1.H) + 9}
		}
	})
	if !ok0 || !ok1 {
		t.Fatalf("reopened source bubbles missing: ok0=%v ok1=%v", ok0, ok1)
	}

	w3.click(msgs.QuoteJump("Xuhu"))
	var during, after *image.RGBA
	var gotRecall, clearedRecall quoteRecall
	w3.pump.do(func() {
		gotRecall = xuhu3.recall
		during = cloneRGBA(w3.view.Image())
	})
	if gotRecall.messageID != quotedMsgID || gotRecall.bubble != 1 {
		t.Fatalf("reopened recall state mismatch: got=%+v wantID=%q bubble=1", gotRecall, quotedMsgID)
	}
	w3.frame(func() { w3.view.Advance(1700 * time.Millisecond) })
	w3.pump.do(func() {
		clearedRecall = xuhu3.recall
		after = cloneRGBA(w3.view.Image())
	})
	if clearedRecall != (quoteRecall{}) {
		t.Fatalf("reopened recall highlight did not expire: %+v", clearedRecall)
	}
	if diffPixelsInRect(during, after, b1.x0, b1.y0, b1.x1, b1.y1) == 0 {
		t.Fatal("reopened click-to-recall did not highlight Bubble 1")
	}
	if changed := diffPixelsInRect(during, after, b0.x0, b0.y0, b0.x1, b0.y1); changed != 0 {
		t.Fatalf("reopened click-to-recall altered Bubble 0 (%d pixels)", changed)
	}

	w3.shutdown(ctx)
}
