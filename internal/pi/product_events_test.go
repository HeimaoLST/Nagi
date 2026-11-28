package pi

import (
	"encoding/json"
	"testing"
)

func newTestProductEventClient(t *testing.T, handlers ProductHandlers) *Client {
	t.Helper()
	client := &Client{
		product:       &handlers,
		streamID:      "stream-1",
		callbackWake:  make(chan struct{}, 1),
		callbacksDone: make(chan struct{}),
	}
	go client.dispatchCallbacks()
	t.Cleanup(func() {
		client.callbackMu.Lock()
		client.callbackClosed = true
		client.callbackMu.Unlock()
		client.wakeCallbacks()
		<-client.callbacksDone
	})
	return client
}

func TestHandleProductEventValidatesBGEventsAndSequenceGaps(t *testing.T) {
	var delivered []ProductEvent
	gaps := 0
	client := newTestProductEventClient(t, ProductHandlers{
		OnEvent:     func(ev ProductEvent) { delivered = append(delivered, ev) },
		OnStreamGap: func() { gaps++ },
	})

	validLines := []string{
		`{"streamId":"stream-1","seq":1,"event":"session.changed","data":{"sessionId":"s1","ownerBotId":"b1","creatingOperationId":"op1","generation":1,"revision":1,"commandSummary":"echo ok","cwd":"/tmp","mode":"pipe","state":"running","transportStatus":"live","firstAvailableOffset":0,"nextOffset":0,"eof":false,"evictedBytes":0,"watches":[],"createdAt":"2026-11-27T00:00:00Z","updatedAt":"2026-11-27T00:00:00Z"}}`,
		`{"streamId":"stream-1","seq":2,"event":"session.outputUpdated","data":{"sessionId":"s1","ownerBotId":"b1","generation":1,"firstAvailableOffset":0,"nextOffset":12,"evictedBytes":0,"eof":false,"at":"2026-11-27T00:00:01Z"}}`,
		`{"streamId":"stream-1","seq":3,"event":"watch.changed","data":{"botId":"b1","change":"saved","watchId":"w1","at":"2026-11-27T00:00:02Z"}}`,
		`{"streamId":"stream-1","seq":4,"event":"conversation.timelineChanged","data":{"conversationId":"c1","timelineRevision":2,"noticeId":"n1","at":"2026-11-27T00:00:03Z"}}`,
		`{"streamId":"stream-1","seq":5,"event":"orchestration.changed","data":{"botId":"b1","change":"run_started","mandateId":"m1","runId":"r1","at":"2026-11-27T00:00:04Z"}}`,
	}
	for _, line := range validLines {
		if err := client.handleProductEvent([]byte(line)); err != nil {
			t.Fatalf("handleProductEvent(%s) unexpected error: %v", line, err)
		}
	}

	if err := client.handleProductEvent([]byte(validLines[0])); err != nil {
		t.Fatalf("duplicate seq unexpected error: %v", err)
	}
	if err := client.handleProductEvent([]byte(`{"streamId":"other-stream","seq":99,"event":"watch.changed","data":{"botId":"b1","change":"saved","at":"2026-11-27T00:00:05Z"}}`)); err != nil {
		t.Fatalf("other stream unexpected error: %v", err)
	}

	invalidLines := []string{
		`{"streamId":"","seq":6,"event":"watch.changed","data":{"botId":"b1","change":"saved","at":"t"}}`,
		`{"streamId":"stream-1","seq":6,"event":"session.changed","data":{"sessionId":"s1","ownerBotId":"b1","generation":0,"revision":1}}`,
		`{"streamId":"stream-1","seq":6,"event":"session.outputUpdated","data":{"sessionId":"s1","ownerBotId":"","generation":1}}`,
		`{"streamId":"stream-1","seq":6,"event":"watch.changed","data":{"botId":"b1","change":"","at":"t"}}`,
		`{"streamId":"stream-1","seq":6,"event":"conversation.timelineChanged","data":{"conversationId":"c1","timelineRevision":0,"at":"t"}}`,
		`{"streamId":"stream-1","seq":6,"event":"orchestration.changed","data":{"botId":"b1","change":"stopped","at":""}}`,
	}
	for _, line := range invalidLines {
		if err := client.handleProductEvent([]byte(line)); err == nil {
			t.Fatalf("handleProductEvent(%s) unexpectedly succeeded", line)
		}
	}

	if err := client.handleProductEvent([]byte(`{"streamId":"stream-1","seq":7,"event":"watch.changed","data":{"botId":"b1","change":"paused","at":"2026-11-27T00:00:06Z"}}`)); err != nil {
		t.Fatalf("gap event unexpected error: %v", err)
	}
	if err := client.handleProductEvent([]byte(`{"streamId":"stream-1","seq":6,"event":"watch.changed","data":{"botId":"b1","change":"paused","at":"2026-11-27T00:00:07Z"}}`)); err != nil {
		t.Fatalf("post-gap event unexpected error: %v", err)
	}

	client.callbackMu.Lock()
	client.callbackClosed = true
	client.callbackMu.Unlock()
	client.wakeCallbacks()
	<-client.callbacksDone

	if len(delivered) != 5 {
		t.Fatalf("delivered event count = %d, want 5", len(delivered))
	}
	if delivered[0].SessionChanged == nil || delivered[1].SessionOutputUpdated == nil ||
		delivered[2].WatchChanged == nil || delivered[3].TimelineChanged == nil ||
		delivered[4].OrchestrationChanged == nil {
		t.Fatalf("decoded event payload missing: %+v", delivered)
	}
	if gaps != 1 {
		t.Fatalf("gap count = %d, want 1", gaps)
	}
}

func TestDecodeBGOperationReceipts(t *testing.T) {
	emptySend, err := DecodeConversationSendResult(ProductReceipt{OperationID: "op-empty"})
	if err != nil || emptySend.Queued || emptySend.Delivery != "" {
		t.Fatalf("empty send decode = %+v, err=%v", emptySend, err)
	}

	queuedSend, err := DecodeConversationSendResult(ProductReceipt{
		Result: json.RawMessage(`{"queued":true,"queuePosition":2,"reason":"provider_capacity","draftRevision":3}`),
	})
	if err != nil || !queuedSend.Queued || queuedSend.QueuePosition != 2 || queuedSend.DraftRevision != 3 {
		t.Fatalf("queued send decode = %+v, err=%v", queuedSend, err)
	}

	sessionClose, err := DecodeSessionCloseResult(ProductReceipt{
		Result: json.RawMessage(`{"sessionId":"s1","generation":1,"revision":2,"state":"closed","closeDisposition":"terminated","childReaped":true,"transportStatus":"closed_by_user"}`),
	})
	if err != nil || sessionClose.SessionID != "s1" || !sessionClose.ChildReaped {
		t.Fatalf("session close decode = %+v, err=%v", sessionClose, err)
	}

	watchMut, err := DecodeWatchMutationResult(ProductReceipt{
		Result: json.RawMessage(`{"watch":{"watchId":"w1","ownerBotId":"b1","targetConversationId":"c1","revision":2,"kind":"timer","source":"user","state":"active","armed":true,"label":"","purpose":"Check","consecutiveFailures":0,"createdAt":"t","updatedAt":"t"},"disposition":"saved"}`),
	})
	if err != nil || watchMut.Watch.WatchID != "w1" || watchMut.Disposition != "saved" {
		t.Fatalf("watch mutation decode = %+v, err=%v", watchMut, err)
	}

	revokeGrant, err := DecodeWatchRevokeGrantResult(ProductReceipt{
		Result: json.RawMessage(`{"grant":{"grantId":"g1","installationId":"i1","ownerBotId":"b1","revision":2,"state":"revoked","purpose":"P","operationClass":"precheck_executable","artifactKind":"executable","canonicalCwd":"/tmp","workspaceRead":false,"networkOrigins":[],"minCadenceSeconds":10,"maxTimeoutMs":5000,"createdAt":"t","updatedAt":"t"},"invalidatedWatchIds":["w1","w2"]}`),
	})
	if err != nil || revokeGrant.Grant.GrantID != "g1" || len(revokeGrant.InvalidatedWatchIDs) != 2 {
		t.Fatalf("watch revoke grant decode = %+v, err=%v", revokeGrant, err)
	}

	decision, err := DecodeOrchestrationDecisionResult(ProductReceipt{
		Result: json.RawMessage(`{"mandate":{"mandateId":"m1","ownerBotId":"b1","targetConversationId":"c1","sourceKind":"watch","purpose":"P","state":"active","revision":2,"generation":1,"metrics":{"totalWakes":1,"calls":1,"inputTokens":1,"outputTokens":1,"totalTokens":2,"costUsd":0,"lastQueueDelayMs":0,"lastCoalescedCount":1,"backoffDelayMs":0,"consecutiveNoProgress":0},"dormant":false,"createdAt":"t","updatedAt":"t"},"decision":{"decisionId":"d1","mandateId":"m1","ownerBotId":"b1","runId":"r1","revision":2,"state":"approved","kind":"approval","purpose":"P","summary":"S","targetConversationId":"c1","options":[],"outcome":"approve","dormant":false,"createdAt":"t","updatedAt":"t"}}`),
	})
	if err != nil || decision.Mandate.MandateID != "m1" || decision.Decision.Outcome != "approve" {
		t.Fatalf("orchestration decision decode = %+v, err=%v", decision, err)
	}

	stop, err := DecodeOrchestrationStopResult(ProductReceipt{
		Result: json.RawMessage(`{"mandate":{"mandateId":"m1","ownerBotId":"b1","targetConversationId":"c1","sourceKind":"watch","purpose":"P","state":"stopped","revision":3,"generation":1,"metrics":{"totalWakes":1,"calls":1,"inputTokens":1,"outputTokens":1,"totalTokens":2,"costUsd":0,"lastQueueDelayMs":0,"lastCoalescedCount":1,"backoffDelayMs":0,"consecutiveNoProgress":0},"dormant":false,"createdAt":"t","updatedAt":"t"},"cancelRequested":true,"stoppedRunId":"r1"}`),
	})
	if err != nil || !stop.CancelRequested || stop.StoppedRunID != "r1" {
		t.Fatalf("orchestration stop decode = %+v, err=%v", stop, err)
	}
}
