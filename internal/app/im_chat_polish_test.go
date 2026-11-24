package app

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestWholeBubbleCadenceReduceMotionHistoryAndStopReveal(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	msgs := locales.In("en")
	liveMsg := &message{
		Key: "live:exec-1:text:pi", Kind: msgContact, TaskID: "exec-1", ItemID: "pi",
		Text: "First whole bubble.", Streaming: true, liveReveal: true,
	}
	ct.Status = statusWorking
	ct.Messages = []*message{
		{Key: "live:exec-1:user", Kind: msgUser, TaskID: "exec-1", Text: "Explain Machin formula"},
		liveMsg,
	}

	tr := ui.NewTesterWith(a.view, 1040, 720, ui.TesterOptions{Dark: false})
	tr.SetLocale("en")
	tr.Frame()
	if tr.HasText("First whole bubble.") || !tr.HasText(msgs.BotTyping()) {
		t.Fatalf("unfinished first bubble was not held behind typing dots: %v", tr.Texts())
	}

	typingRect, ok := tr.Find(msgs.BotTyping())
	if !ok {
		t.Fatal("typing bubble rect missing")
	}
	before := cloneRGBA(tr.Image())
	tr.Advance(350 * time.Millisecond)
	tr.Frame()
	after := cloneRGBA(tr.Image())
	if diffPixelsInRect(before, after, int(typingRect.X), int(typingRect.Y), int(typingRect.X+typingRect.W), int(typingRect.Y+typingRect.H)) == 0 {
		t.Fatal("typing dots did not animate under normal motion")
	}

	liveMsg.Text = "First whole bubble.\n<<<NEXT>>>Second whole bubble."
	tr.Frame()
	if !tr.HasText("First whole bubble.") || tr.HasText("Second whole bubble.") || !tr.HasText(msgs.BotTyping()) {
		t.Fatalf("after separator, first bubble should reveal while second stays behind typing dots: %v", tr.Texts())
	}

	liveMsg.Streaming = false
	ct.Status = statusReady
	tr.Advance(359 * time.Millisecond)
	if tr.HasText("Second whole bubble.") || !tr.HasText(msgs.BotTyping()) {
		t.Fatalf("second bubble revealed before 360ms beat (at 359ms): %v", tr.Texts())
	}

	tr.Advance(1 * time.Millisecond)
	if !tr.HasText("Second whole bubble.") || tr.HasText(msgs.BotTyping()) {
		t.Fatalf("second bubble did not reveal at 360ms or typing dots remained: %v", tr.Texts())
	}

	t.Run("reduce-motion-immediate-and-static-dots", func(t *testing.T) {
		ra, rct := workspaceTestApp(t)
		ra.windowWidth = 1040
		rm := &message{
			Key: "live:exec-rm:text:pi", Kind: msgContact, TaskID: "exec-rm", ItemID: "pi",
			Text: "RM bubble one.\n<<<NEXT>>>RM bubble two.", Streaming: true, liveReveal: true,
		}
		rct.Status = statusWorking
		rct.Messages = []*message{rm}
		rtr := ui.NewTesterWith(ra.view, 1040, 720, ui.TesterOptions{
			Dark:        false,
			Preferences: ui.Preferences{ReduceMotion: true},
		})
		rtr.SetLocale("en")
		rtr.Frame()
		if !rtr.HasText("RM bubble one.") || rtr.HasText("RM bubble two.") || !rtr.HasText(msgs.BotTyping()) {
			t.Fatalf("reduce-motion streaming state mismatch: %v", rtr.Texts())
		}
		dotRect, ok := rtr.Find(msgs.BotTyping())
		if !ok {
			t.Fatal("reduce-motion typing bubble missing")
		}
		b0 := cloneRGBA(rtr.Image())
		rtr.Advance(350 * time.Millisecond)
		rtr.Frame()
		b1 := cloneRGBA(rtr.Image())
		if changed := diffPixelsInRect(b0, b1, int(dotRect.X), int(dotRect.Y), int(dotRect.X+dotRect.W), int(dotRect.Y+dotRect.H)); changed != 0 {
			t.Fatalf("typing dots changed %d pixels under ReduceMotion", changed)
		}

		rm.Streaming = false
		rct.Status = statusReady
		rtr.Frame()
		if !rtr.HasText("RM bubble two.") || rtr.HasText(msgs.BotTyping()) {
			t.Fatalf("reduce-motion did not reveal remaining bubble immediately on settlement: %v", rtr.Texts())
		}
	})

	t.Run("restored-history-immediate-and-stop-partial-reveal", func(t *testing.T) {
		ha, hct := workspaceTestApp(t)
		ha.windowWidth = 1040
		hct.Messages = []*message{{
			Key: "hist-1", Kind: msgContact, History: true,
			Text: "History part one.\n<<<NEXT>>>History part two.",
		}}
		htr := ui.NewTester(ha.view, 1040, 720)
		htr.SetLocale("en")
		htr.Frame()
		if !htr.HasText("History part one.") || !htr.HasText("History part two.") || htr.HasText(msgs.BotTyping()) {
			t.Fatalf("restored history did not display immediately without typing dots: %v", htr.Texts())
		}

		partial := &message{
			Key: "live:stop-1:text:pi", Kind: msgContact, TaskID: "stop-1", ItemID: "pi",
			Text: "Partial text before stop", Streaming: true, liveReveal: true,
		}
		hct.Status = statusWorking
		hct.Messages = append(hct.Messages, partial)
		htr.Frame()
		if htr.HasText("Partial text before stop") || !htr.HasText(msgs.BotTyping()) {
			t.Fatalf("streaming partial text did not stay behind typing dots before stop: %v", htr.Texts())
		}
		finishLiveReply(hct, "stop-1")
		hct.Status = statusReady
		htr.Frame()
		if !htr.HasText("Partial text before stop") || htr.HasText(msgs.BotTyping()) {
			t.Fatalf("finishLiveReply did not reveal partial text after stop/exit: %v", htr.Texts())
		}
	})
}

func TestBubbleRightClickCopyUserMultiBubblePreviousChatAndCodeCopy(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	msgs := locales.In("en")
	const userRaw = "Show the calculation for π."
	const codeBody = "pi = 16 * atan(1 / 5) - 4 * atan(1 / 239)\n"
	const bubble0 = "圆周率可以用下面的表达式计算。\n\n```python\n" + codeBody + "```"
	const bubble1 = "这是用 Machin 公式算的。"
	ct.Messages = []*message{
		{ID: 1, Kind: msgUser, Text: userRaw},
		{ID: 2, Kind: msgContact, Text: bubble0 + "\n<<<NEXT>>>" + bubble1},
	}

	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if tr.HasText(msgs.CopyMessageLabel()) {
		t.Fatalf("outer Copy message button unexpectedly visible: %v", tr.Texts())
	}
	if !tr.HasText(msgs.CopyCodeLabel()) {
		t.Fatalf("fenced code Copy code button missing: %v", tr.Texts())
	}

	if err := tr.RightClick(userRaw); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := choosePopoverMenuItem(tr, msgs.CopyMessageLabel()); err != nil {
		t.Fatal(err)
	}
	if tr.Clipboard() != userRaw {
		t.Fatalf("user bubble popover copy = %q, want %q", tr.Clipboard(), userRaw)
	}

	if err := tr.RightClick("圆周率可以用下面的表达式计算。"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := choosePopoverMenuItem(tr, msgs.CopyMessageLabel()); err != nil {
		t.Fatal(err)
	}
	if tr.Clipboard() != bubble0 {
		t.Fatalf("Bot bubble 0 popover copy = %q, want %q", tr.Clipboard(), bubble0)
	}

	if err := tr.RightClick(bubble1); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := choosePopoverMenuItem(tr, msgs.CopyMessageLabel()); err != nil {
		t.Fatal(err)
	}
	if tr.Clipboard() != bubble1 {
		t.Fatalf("Bot bubble 1 popover copy = %q, want %q", tr.Clipboard(), bubble1)
	}

	if err := tr.Click(msgs.CopyCodeLabel()); err != nil {
		t.Fatal(err)
	}
	if tr.Clipboard() != codeBody {
		t.Fatalf("fenced code copy = %q, want %q", tr.Clipboard(), codeBody)
	}

	a.priorChats = &productConversationBrowser{
		contact:    ct,
		selectedID: "prior-conv-1",
		ownerID:    ct.ID,
		history: historyProjection{
			messages: []*message{
				{Key: "prev-user-1", ID: 10, Kind: msgUser, Text: "Previous user prompt"},
				{Key: "prev-bot-2", ID: 11, Kind: msgContact, Text: "Previous Bot reply one.\n<<<NEXT>>>Previous Bot reply two."},
			},
		},
	}
	tr.Frame()
	if tr.HasText(msgs.CopyMessageLabel()) {
		t.Fatalf("previous chat unexpectedly rendered outer Copy message: %v", tr.Texts())
	}
	if err := tr.RightClick("Previous Bot reply two."); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := choosePopoverMenuItem(tr, msgs.CopyMessageLabel()); err != nil {
		t.Fatal(err)
	}
	if tr.Clipboard() != "Previous Bot reply two." {
		t.Fatalf("previous chat bubble popover copy = %q", tr.Clipboard())
	}
}

func TestComposerSubmittingInlineStatusZeroHeightJump(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			a, ct, _ := piRunStartedTestApp(t)
			ct.ActiveTaskID, ct.SendState, ct.Status = "", sendIdle, statusReady
			ct.Draft = "Show the calculation for π."
			a.windowWidth = 1040
			msgs := locales.In(locale)

			tr := ui.NewTester(a.view, 1040, 720)
			tr.SetLocale(locale)
			tr.Frame()

			idleInput, ok1 := tr.Find(msgs.ComposerLabel(ct.Name))
			idleToolbar, ok2 := tr.Find(msgs.ComposerTools())
			if !ok1 || !ok2 {
				t.Fatalf("idle composer geometry missing: input=%v toolbar=%v", ok1, ok2)
			}

			ct.SendState = sendSubmitting
			tr.Frame()
			if !tr.HasText(msgs.SendingState()) {
				t.Fatalf("%s inline sending status missing: %v", locale, tr.Texts())
			}
			if tr.HasText(sendBlockText(msgs, blockBusy)) {
				t.Fatalf("%s blockBusy line rendered during sendSubmitting: %v", locale, tr.Texts())
			}
			subInput, _ := tr.Find(msgs.ComposerLabel(ct.Name))
			subToolbar, _ := tr.Find(msgs.ComposerTools())
			statusRect, statusOK := tr.Find(msgs.SendingState())
			if !statusOK || statusRect.Y < subToolbar.Y-1 || statusRect.Y+statusRect.H > subToolbar.Y+subToolbar.H+1 {
				t.Fatalf("%s sending status not inline inside toolbar: status=%+v toolbar=%+v", locale, statusRect, subToolbar)
			}
			if math.Abs(float64(subInput.Y-idleInput.Y)) > 0.01 || math.Abs(float64(subInput.H-idleInput.H)) > 0.01 ||
				math.Abs(float64(subToolbar.Y-idleToolbar.Y)) > 0.01 || math.Abs(float64(subToolbar.H-idleToolbar.H)) > 0.01 {
				t.Fatalf("%s composer jumped on sendSubmitting: idle=(%+v,%+v) sub=(%+v,%+v)", locale, idleInput, idleToolbar, subInput, subToolbar)
			}

			ct.SendState = sendAccepted
			ct.Status = statusReady
			tr.Frame()
			if !tr.HasText(msgs.SendAcceptedState()) {
				t.Fatalf("%s inline accepted status missing: %v", locale, tr.Texts())
			}
			accInput, _ := tr.Find(msgs.ComposerLabel(ct.Name))
			accToolbar, _ := tr.Find(msgs.ComposerTools())
			if math.Abs(float64(accInput.Y-idleInput.Y)) > 0.01 || math.Abs(float64(accToolbar.Y-idleToolbar.Y)) > 0.01 ||
				math.Abs(float64(accToolbar.H-idleToolbar.H)) > 0.01 {
				t.Fatalf("%s composer jumped on sendAccepted: idle=(%+v,%+v) acc=(%+v,%+v)", locale, idleInput, idleToolbar, accInput, accToolbar)
			}

			ct.SendState = sendIdle
			tr.Frame()
			backInput, _ := tr.Find(msgs.ComposerLabel(ct.Name))
			backToolbar, _ := tr.Find(msgs.ComposerTools())
			if math.Abs(float64(backInput.Y-idleInput.Y)) > 0.01 || math.Abs(float64(backToolbar.Y-idleToolbar.Y)) > 0.01 {
				t.Fatalf("%s composer did not return to exact idle position: idle=(%+v,%+v) back=(%+v,%+v)", locale, idleInput, idleToolbar, backInput, backToolbar)
			}
		})
	}
}
