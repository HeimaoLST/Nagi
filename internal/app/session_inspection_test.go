package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func TestSessionOutputStateReadProjectionAndSafety(t *testing.T) {
	output := &sessionOutputState{from: 20, next: 10, textDirty: true}
	if got := output.projectedText(pi.ProductSessionPTY, defaultSessionInspectionCopy); got != "" {
		t.Fatalf("underflow projectedText = %q", got)
	}

	output = &sessionOutputState{scroll: output.scroll}
	output.applyRead(pi.ProductSessionReadResult{
		FromOffset: 10,
		NextOffset: 24,
		Gap:        true,
		GapBytes:   10,
		Chunks: []pi.ProductSessionChunk{
			{Stream: "stdout", Offset: 10, NextOffset: 15, Text: "hello"},
			{Stream: "stderr", Offset: 15, NextOffset: 24, Text: "warn\n"},
		},
	}, sessionOutputLatest)
	if output.gapBytes != 10 || output.browsing || output.from != 10 || output.next != 24 {
		t.Fatalf("applyRead state = %+v", output)
	}
	en := output.projectedText(pi.ProductSessionPipe, defaultSessionInspectionCopy)
	if !strings.Contains(en, "[stdout]\nhello\n[stderr]\nwarn\n") {
		t.Fatalf("pipe projectedText = %q", en)
	}
	zhWords := sessionInspectionCopy{"stdout": "标准输出", "stderr": "标准错误"}
	zh := output.projectedText(pi.ProductSessionPipe, zhWords)
	if !strings.Contains(zh, "[标准输出]\nhello\n[标准错误]\nwarn\n") {
		t.Fatalf("locale cache invalidation failed: %q", zh)
	}

	output.highWater = 100
	output.applyRead(pi.ProductSessionReadResult{
		FromOffset: 24,
		NextOffset: 30,
		Chunks:     []pi.ProductSessionChunk{{Stream: "stdout", Offset: 24, NextOffset: 30, Text: "later\n"}},
	}, sessionOutputLater)
	if !output.browsing {
		t.Fatal("sessionOutputLater below highWater should keep browsing=true")
	}

	huge := strings.Repeat("x", sessionOutputLimit/2+10)
	output.applyRead(pi.ProductSessionReadResult{
		FromOffset: 30,
		NextOffset: 30 + uint64(len(huge)),
		Chunks:     []pi.ProductSessionChunk{{Stream: "stdout", Offset: 30, NextOffset: 30 + uint64(len(huge)), Text: huge}},
	}, sessionOutputFollow)
	if len(output.chunks) == 0 || output.next-output.from > sessionOutputLimit {
		t.Fatalf("rolling limit exceeded: from=%d next=%d", output.from, output.next)
	}
}

func TestSessionCloseReceiptsAndDeterministicPersistence(t *testing.T) {
	ws := &sessionWorkspaceState{}
	ws.restoreSessionCloses([]pi.ProductSessionCloseParams{
		{BotID: "bot-b", SessionID: "s-2", Generation: 1, OperationID: "op-2"},
		{BotID: "bot-a", SessionID: "s-2", Generation: 1, OperationID: "op-1"},
		{BotID: "bot-a", SessionID: "s-1", Generation: 2, OperationID: "op-3"},
	})
	pending := ws.pendingSessionCloses()
	if len(pending) != 3 || pending[0].BotID != "bot-a" || pending[0].SessionID != "s-1" ||
		pending[1].SessionID != "s-2" || pending[2].BotID != "bot-b" {
		t.Fatalf("pendingSessionCloses = %+v", pending)
	}

	rev := uint64(2)
	baseIntent := func() *sessionCloseIntent {
		return &sessionCloseIntent{
			params: pi.ProductSessionCloseParams{
				BotID: "bot-a", SessionID: "s-1", Generation: 2, ExpectedRevision: &rev, OperationID: "op-3",
			},
			status: "killUnknown",
		}
	}

	intent := baseIntent()
	ws.applySessionCloseReceipt(intent, pi.ProductReceipt{}, &pi.RemoteError{Code: "stale_generation"})
	if intent.status != "killStaleGeneration" {
		t.Fatalf("stale_generation status = %q", intent.status)
	}

	intent = baseIntent()
	ws.applySessionCloseReceipt(intent, pi.ProductReceipt{}, &pi.CommandError{Delivery: pi.NotSent, Err: errors.New("eof")})
	if intent.status != "killNotSent" {
		t.Fatalf("NotSent status = %q", intent.status)
	}

	intent = baseIntent()
	ws.applySessionCloseReceipt(intent, pi.ProductReceipt{
		OperationID: "op-3",
		State:       "accepted",
		Target:      pi.ProductReceiptTarget{BotID: "bot-a", SessionID: "s-1"},
		Result:      []byte(`{"sessionId":"s-1","generation":2,"revision":3,"state":"closed","closeDisposition":"terminated","childReaped":true,"transportStatus":"closed_by_user"}`),
	}, nil)
	if intent.status != "killTerminated" || intent.result == nil {
		t.Fatalf("terminated receipt = status:%q result:%+v", intent.status, intent.result)
	}

	intent = baseIntent()
	ws.applySessionCloseReceipt(intent, pi.ProductReceipt{
		OperationID: "op-3",
		State:       "unknown",
		Target:      pi.ProductReceiptTarget{BotID: "bot-a", SessionID: "s-1"},
		Result:      []byte(`{"sessionId":"s-1","generation":2,"revision":2,"state":"disconnected","closeDisposition":"terminated","childReaped":false,"transportStatus":"supervisor_lost"}`),
	}, nil)
	if intent.status != "killUnknown" || !intent.unresolved() {
		t.Fatalf("unknown receipt = status:%q unresolved:%v", intent.status, intent.unresolved())
	}

	intent = baseIntent()
	ws.applySessionCloseReceipt(intent, pi.ProductReceipt{
		OperationID: "wrong-op",
		State:       "accepted",
		Target:      pi.ProductReceiptTarget{BotID: "bot-a", SessionID: "s-1"},
	}, nil)
	if intent.status != "killUnknown" {
		t.Fatalf("mismatched receipt mutated status to %q", intent.status)
	}

	if got := sessionNumber(1234567); got != "1,234,567" {
		t.Fatalf("sessionNumber(1234567) = %q", got)
	}
}
