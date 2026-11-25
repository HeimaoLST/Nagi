package app

import (
	"slices"
	"testing"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestMergeLiveConversation(t *testing.T) {
	t.Run("preserves-in-flight-tail-when-history-lags", func(t *testing.T) {
		previous := []*message{
			{Key: "hist-1", MessageID: "msg-1", Kind: msgUser, Text: "first"},
			{Key: "live-u", Kind: msgUser, TaskID: "exec-2", Text: "second"},
			{Key: "live-r", Kind: msgContact, TaskID: "exec-2", Text: "partial", Streaming: true},
		}
		history := []*message{
			{Key: "msg-1", MessageID: "msg-1", Kind: msgUser, Text: "first", History: true},
		}
		got := mergeLiveConversation(previous, history)
		if len(got) != 3 {
			t.Fatalf("len(got) = %d, want 3", len(got))
		}
		if got[1].Key != "live-u" || got[1].TaskID != "exec-2" || got[2].Key != "live-r" || !got[2].Streaming {
			t.Fatalf("lost live optimistic tail: %+v", got)
		}
	})

	t.Run("deduplicates-multi-chunk-history-reply-against-live-bubble", func(t *testing.T) {
		previous := []*message{
			{Key: "live-u", Kind: msgUser, TaskID: "exec-1", Text: "hello"},
			{Key: "live-r", Kind: msgContact, TaskID: "exec-1", Text: "part1part2", Streaming: true},
		}
		history := []*message{
			{Key: "h-u", MessageID: "h-u", Kind: msgUser, Text: "hello", History: true},
			{Key: "h-a1", MessageID: "h-a1", Kind: msgContact, Text: "part1", History: true},
			{Key: "h-a2", MessageID: "h-a2", Kind: msgContact, Text: "part2", History: true},
		}
		got := mergeLiveConversation(previous, history)
		if len(got) != 2 {
			t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
		}
		if got[0].Key != "live-u" || got[0].TaskID != "exec-1" || got[1].Key != "live-r" || got[1].Text != "part1part2" {
			t.Fatalf("unexpected merged messages: %+v", got)
		}
	})

	t.Run("assigns-message-id-on-single-chunk-exact-match", func(t *testing.T) {
		liveReply := &message{Key: "live-r", Kind: msgContact, TaskID: "exec-1", Text: "done"}
		previous := []*message{
			{Key: "live-u", Kind: msgUser, TaskID: "exec-1", Text: "go"},
			liveReply,
		}
		history := []*message{
			{Key: "h-u", MessageID: "h-u", Kind: msgUser, Text: "go", History: true},
			{Key: "h-a", MessageID: "h-a", Kind: msgContact, Text: "done", History: true},
		}
		got := mergeLiveConversation(previous, history)
		if len(got) != 2 || got[1] != liveReply || liveReply.MessageID != "h-a" {
			t.Fatalf("expected liveReply with MessageID h-a, got %+v (MessageID=%q)", got, liveReply.MessageID)
		}
	})
}

func TestQuoteHelpersAndDraftQuoteResolution(t *testing.T) {
	if cloneQuote(nil) != nil {
		t.Fatal("cloneQuote(nil) must return nil")
	}
	idx := 2
	orig := &pi.ProductQuote{MessageID: "m1", Sender: "bot", AuthorName: "Writer", BubbleIndex: &idx, Text: "quoted"}
	cloned := cloneQuote(orig)
	*cloned.BubbleIndex = 9
	cloned.Text = "changed"
	if *orig.BubbleIndex != 2 || orig.Text != "quoted" {
		t.Fatalf("cloneQuote aliased original: %+v (idx=%d)", orig, *orig.BubbleIndex)
	}

	idx2 := 2
	same := &pi.ProductQuote{MessageID: "m1", Sender: "bot", AuthorName: "Writer", BubbleIndex: &idx2, Text: "quoted"}
	if !sameQuote(nil, nil) || sameQuote(orig, nil) || sameQuote(nil, orig) || !sameQuote(orig, same) {
		t.Fatal("sameQuote basic equality failed")
	}
	if sameQuote(orig, &pi.ProductQuote{MessageID: "m1", Sender: "bot", AuthorName: "Writer", Text: "quoted"}) {
		t.Fatal("sameQuote failed to distinguish nil vs non-nil BubbleIndex")
	}

	a, ct := workspaceTestApp(t)
	ct.Messages = []*message{{Key: "temp-key", MessageID: "settled-id", Kind: msgContact, Text: "answer"}}
	ct.DraftQuote = &pi.ProductQuote{MessageID: "temp-key", Sender: "bot", AuthorName: ct.Name, Text: "answer"}
	rev := ct.draftRev
	a.updateDraftQuoteReference(ct)
	if ct.DraftQuote.MessageID != "settled-id" || ct.draftRev == rev {
		t.Fatalf("expected DraftQuote MessageID resolved and draftRev incremented: quote=%+v rev=%d->%d", ct.DraftQuote, rev, ct.draftRev)
	}

	ct.DraftQuote = &pi.ProductQuote{MessageID: "temp-key-2", Sender: "bot", AuthorName: ct.Name, Text: "answer"}
	ct.Messages = []*message{{Key: "temp-key-2", MessageID: "settled-id-2", Kind: msgContact, Text: "answer"}}
	ct.SendState = sendSubmitting
	a.updateDraftQuoteReference(ct)
	if ct.DraftQuote.MessageID != "temp-key-2" {
		t.Fatalf("expected quote reference to stay frozen while submitting, got %q", ct.DraftQuote.MessageID)
	}
}

func TestToolSummaryFormatting(t *testing.T) {
	if got := toolTitleFor("", ""); got != "Tool" {
		t.Fatalf("toolTitleFor empty = %q, want Tool", got)
	}
	if got := toolTitleFor("read", `{"path":"/tmp/alpha.txt"}`); got != "read: /tmp/alpha.txt" {
		t.Fatalf("toolTitleFor path = %q", got)
	}
	if got := toolTitleFor("bash", `{"command":"go test ./..."}`); got != "bash: go test ./..." {
		t.Fatalf("toolTitleFor command = %q", got)
	}
	if got := toolTitleFor("grep", `{"pattern":"foo"}`); got != `grep: {"pattern":"foo"}` {
		t.Fatalf("toolTitleFor fallback = %q", got)
	}

	msgs := locales.In("en")
	if got := toolOutputSummary(msgs, "   \n"); got != "" {
		t.Fatalf("blank summary = %q", got)
	}
	if got := toolOutputSummary(msgs, "plain text output"); got != "plain text output" {
		t.Fatalf("plain text summary = %q", got)
	}
	if got := toolOutputSummary(msgs, `{"content":"ok"}`); got != "ok" {
		t.Fatalf("content summary = %q", got)
	}
	if got := toolOutputSummary(msgs, `{"timed_out":true,"exit_code":124,"stderr":"hung"}`); got != msgs.ToolOutputTimedOutExit("124")+": hung" {
		t.Fatalf("timed_out summary = %q", got)
	}
	if got := toolOutputSummary(msgs, `{"error":"boom","exit_code":2}`); got != msgs.ToolOutputFailed("boom")+" (2)" {
		t.Fatalf("error+exit_code summary = %q", got)
	}
	if got := toolOutputSummary(msgs, `{"other":true}`); !slices.Contains([]string{msgs.ToolOutputStructured()}, got) {
		t.Fatalf("structured fallback = %q", got)
	}
}
