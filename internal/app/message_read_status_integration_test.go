//go:build integration

package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestPiAppMessageReadStatusLifecycle(t *testing.T) {
	const (
		turn1Prompt = "READ_STATUS_TURN_1"
		turn2Prompt = "READ_STATUS_TURN_2"
		turn2Reply  = "Agent reply for turn two."
		turn3Prompt = "READ_STATUS_TURN_3"
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	turn1Entered := make(chan struct{})
	turn2Entered := make(chan struct{})
	turn3Entered := make(chan struct{})
	releaseTurn2 := make(chan struct{})
	releaseTurn3 := make(chan struct{})
	var mu sync.Mutex
	var once1, once2, once3 bool

	replyProvider := &piAppProvider{limit: 4}
	if err := replyProvider.install([]piAppCall{}, turn2Reply, nil, nil); err != nil {
		t.Fatal(err)
	}

	root := prepareProductWorkbench(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch {
		case bytes.Contains(body, []byte(turn3Prompt)):
			mu.Lock()
			if !once3 {
				once3 = true
				close(turn3Entered)
			}
			mu.Unlock()
			select {
			case <-releaseTurn3:
			case <-r.Context().Done():
				return
			case <-ctx.Done():
				return
			}
		case bytes.Contains(body, []byte(turn2Prompt)):
			mu.Lock()
			if !once2 {
				once2 = true
				close(turn2Entered)
			}
			mu.Unlock()
			select {
			case <-releaseTurn2:
			case <-r.Context().Done():
				return
			case <-ctx.Done():
				return
			}
		case bytes.Contains(body, []byte(turn1Prompt)):
			mu.Lock()
			if !once1 {
				once1 = true
				close(turn1Entered)
			}
			mu.Unlock()
			<-r.Context().Done()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		replyProvider.ServeHTTP(w, r)
	}))

	if err := writeProfileJSON(guiStorePath(), storedState{
		Version:  contactStoreVersion,
		Selected: "cedar",
		Contacts: []storedContact{
			{ID: "cedar", Name: "Cedar", Workspace: root, Draft: turn1Prompt},
		},
	}); err != nil {
		t.Fatal(err)
	}

	w := openProductWorkbench(t)
	w.frame(func() { w.view.SetLocale("zh-Hans") })
	msgs := locales.In("zh-Hans")
	cedar := w.app.contactByID("cedar")
	if cedar == nil {
		t.Fatal("missing seeded contact cedar")
	}
	w.wait(func() bool { return w.app.selectedContact() == cedar && cedar.Loaded })

	countTexts := func(target string) int {
		count := 0
		for _, s := range w.view.Texts() {
			if s == target {
				count++
			}
		}
		return count
	}

	w.click(msgs.SendAction())
	select {
	case <-turn1Entered:
	case <-ctx.Done():
		t.Fatal("timed out waiting for turn 1 provider entry")
	}

	var count1 int
	var u1X, u1Y, u1H, r1Y float32
	w.wait(func() bool {
		return w.view.HasText(turn1Prompt) && w.view.HasText(msgs.MessageReadStatus()) &&
			cedar.ActiveTaskID != "" && w.app.submissionInFlight == 0 && cedar.Draft == "" && w.view.HasText(msgs.StopConversation())
	})
	w.pump.do(func() {
		count1 = countTexts(msgs.MessageReadStatus())
		u1Rect, okU1 := w.view.Find(turn1Prompt)
		rRect, okR := w.view.Find(msgs.MessageReadStatus())
		if okU1 && okR {
			u1X, u1Y, u1H, r1Y = u1Rect.X, u1Rect.Y, u1Rect.H, rRect.Y
		}
	})
	if count1 != 1 || r1Y < u1Y+u1H-2 || u1X < 0 {
		t.Fatalf("turn 1 read badge invalid: count=%d r1Y=%.1f u1=[x=%.1f y=%.1f h=%.1f]", count1, r1Y, u1X, u1Y, u1H)
	}

	w.click(msgs.StopConversation())
	var turn1Settled bool
	for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); {
		w.frame(func() {})
		w.pump.do(func() {
			turn1Settled = cedar.SendState == sendIdle && cedar.Status != statusWorking && cedar.ActiveTaskID == "" && cedar.Draft == "" && w.app.contactHistoryInFlight == 0
		})
		if turn1Settled {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !turn1Settled {
		var diag string
		w.pump.do(func() {
			diag = fmt.Sprintf("turn1 stop state: send=%v status=%v session=%q active=%q draft=%q err=%q hist=%d",
				cedar.SendState, cedar.Status, cedar.SessionState, cedar.ActiveTaskID, cedar.Draft, cedar.SendError, w.app.contactHistoryInFlight)
		})
		t.Fatal(diag)
	}

	w.click(msgs.ComposerLabel("Cedar"))
	w.frame(func() { w.view.Type(turn2Prompt) })
	w.click(msgs.SendAction())
	select {
	case <-turn2Entered:
	case <-ctx.Done():
		t.Fatal("timed out waiting for turn 2 provider entry")
	}

	var count2 int
	var u2Y, u2H, r2Y float32
	var ok2 bool
	w.wait(func() bool {
		return w.view.HasText(turn1Prompt) && w.view.HasText(turn2Prompt) && w.view.HasText(msgs.MessageReadStatus())
	})
	w.pump.do(func() {
		count2 = countTexts(msgs.MessageReadStatus())
		u2Rect, okU2 := w.view.Find(turn2Prompt)
		rRect, okR := w.view.Find(msgs.MessageReadStatus())
		if okU2 && okR {
			ok2 = true
			u2Y, u2H, r2Y = u2Rect.Y, u2Rect.H, rRect.Y
		}
	})
	if count2 != 1 || !ok2 || r2Y < u2Y+u2H-2 {
		t.Fatalf("consecutive sends must show exactly 1 Read below turn 2 only: count=%d ok=%v r2Y=%.1f u2=[y=%.1f h=%.1f]", count2, ok2, r2Y, u2Y, u2H)
	}

	close(releaseTurn2)
	w.wait(func() bool {
		if w.view.FramePending() {
			w.view.Advance(450 * time.Millisecond)
		}
		return cedar.SendState == sendIdle && cedar.Status != statusWorking && w.view.HasText(turn2Reply)
	})
	var afterReplyCount int
	var afterReplyTexts []string
	w.pump.do(func() {
		afterReplyCount = countTexts(msgs.MessageReadStatus())
		if afterReplyCount != 0 {
			afterReplyTexts = w.view.Texts()
		}
	})
	if afterReplyCount != 0 {
		t.Fatalf("expected all Read badges cleared after agent reply arrived, got %d: %v", afterReplyCount, afterReplyTexts)
	}

	w.click(msgs.ComposerLabel("Cedar"))
	w.frame(func() { w.view.Type(turn3Prompt) })
	w.click(msgs.SendAction())
	select {
	case <-turn3Entered:
	case <-ctx.Done():
		t.Fatal("timed out waiting for turn 3 provider entry")
	}
	var count3 int
	var u3Y, u3H, r3Y float32
	var ok3 bool
	w.wait(func() bool {
		return w.view.HasText(turn3Prompt) && w.view.HasText(msgs.MessageReadStatus())
	})
	w.pump.do(func() {
		count3 = countTexts(msgs.MessageReadStatus())
		u3Rect, okU3 := w.view.Find(turn3Prompt)
		rRect, okR := w.view.Find(msgs.MessageReadStatus())
		if okU3 && okR {
			ok3 = true
			u3Y, u3H, r3Y = u3Rect.Y, u3Rect.H, rRect.Y
		}
	})
	if count3 != 1 || !ok3 || r3Y < u3Y+u3H-2 {
		t.Fatalf("turn 3 read badge missing or misaligned: count=%d ok=%v r3Y=%.1f u3=[y=%.1f h=%.1f]", count3, ok3, r3Y, u3Y, u3H)
	}
	close(releaseTurn3)
	w.wait(func() bool {
		if w.view.FramePending() {
			w.view.Advance(450 * time.Millisecond)
		}
		return cedar.SendState == sendIdle && cedar.Status != statusWorking
	})
	w.shutdown(ctx)
}
