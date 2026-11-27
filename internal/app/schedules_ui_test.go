package app

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
)

func TestScheduledWatchEditorParametersValidation(t *testing.T) {
	m := scheduledWorkCopy{locale: "en"}
	timerEd := editScheduledWatch(pi.ProductWatchSummary{
		WatchID:  "w-timer",
		Revision: 4,
		Kind:     pi.ProductWatchTimer,
		Purpose:  "Remind me",
		Timer:    &pi.ProductTimerSpec{FireAt: "2026-12-01T09:00:00+08:00", MissedPolicy: pi.ProductMissedFireOnceCoalesced},
	})
	params, problem := timerEd.parameters("bot-1", m)
	if problem != "" || params.Timer == nil || params.Timer.FireAt != "2026-12-01T01:00:00Z" ||
		params.ExpectedRevision == nil || *params.ExpectedRevision != 4 {
		t.Fatalf("timer params = %+v, problem=%q", params, problem)
	}

	timerEd.purpose = "   "
	if _, problem := timerEd.parameters("bot-1", m); problem == "" {
		t.Fatal("blank purpose was accepted")
	}
	timerEd.purpose = "Remind me"
	timerEd.fireAt = "not-a-timestamp"
	if _, problem := timerEd.parameters("bot-1", m); problem == "" {
		t.Fatal("invalid timer timestamp was accepted")
	}

	schedEd := editScheduledWatch(pi.ProductWatchSummary{
		Kind:    pi.ProductWatchSchedule,
		Purpose: "Check repo",
		Schedule: &pi.ProductScheduleSpec{
			CadenceKind: pi.ProductScheduleCalendar,
			Frequency:   "weekly",
			Hour:        new(9),
			Minute:      new(15),
			DaysOfWeek:  []int{1, 3},
			TimeZone:    "Asia/Shanghai",
		},
	})
	params, problem = schedEd.parameters("bot-1", m)
	if problem != "" || params.Schedule == nil || len(params.Schedule.DaysOfWeek) != 2 {
		t.Fatalf("weekly calendar params = %+v, problem=%q", params, problem)
	}

	for _, zone := range []string{"", "Local", "Mars/Olympus"} {
		schedEd.zone = zone
		if _, problem := schedEd.parameters("bot-1", m); problem == "" {
			t.Fatalf("invalid timezone %q was accepted", zone)
		}
	}
	schedEd.zone = "UTC"
	schedEd.days = [7]bool{}
	if _, problem := schedEd.parameters("bot-1", m); problem == "" {
		t.Fatal("weekly schedule without days was accepted")
	}

	schedEd.cadence = string(pi.ProductScheduleInterval)
	schedEd.interval = "9"
	if _, problem := schedEd.parameters("bot-1", m); problem == "" {
		t.Fatal("interval < 10s was accepted")
	}
	schedEd.interval = "30"

	schedEd.precheck = "executable"
	schedEd.executable = "relative/bin"
	if _, problem := schedEd.parameters("bot-1", m); problem == "" {
		t.Fatal("relative executable path was accepted")
	}
	schedEd.executable = filepath.Join(t.TempDir(), "probe")
	schedEd.args = `["ok", null]`
	if _, problem := schedEd.parameters("bot-1", m); problem == "" {
		t.Fatal("null element in executable args was accepted")
	}
	schedEd.args = `["--status"]`
	schedEd.timeout = "999"
	if _, problem := schedEd.parameters("bot-1", m); problem == "" {
		t.Fatal("timeout below 1000ms was accepted")
	}
	schedEd.timeout = "3000"
	params, problem = schedEd.parameters("bot-1", m)
	if problem != "" || params.Precheck == nil || len(params.Precheck.AllowedArgs) != 1 {
		t.Fatalf("executable precheck params = %+v, problem=%q", params, problem)
	}

	schedEd.precheck = "skill"
	schedEd.skill = "sk-1"
	schedEd.source = "private"
	schedEd.script = "check"
	schedEd.input = "{bad-json"
	if _, problem := schedEd.parameters("bot-1", m); problem == "" {
		t.Fatal("invalid JSON skill input was accepted")
	}
	schedEd.input = `{"strict":true}`
	params, problem = schedEd.parameters("bot-1", m)
	if problem != "" || params.Precheck == nil || params.Precheck.SkillID != "sk-1" {
		t.Fatalf("skill precheck params = %+v, problem=%q", params, problem)
	}
}

func TestScheduledWorkOperationsAndCorrelation(t *testing.T) {
	state := &scheduledWorkState{}
	state.restoreOperations([]store.PendingWatchOperation{
		{ID: "op-2", BotID: "bot-b", WatchID: "w-2", Revision: 1, Action: "pause"},
		{ID: "op-1", BotID: "bot-a", GrantID: "g-1", Revision: 2, Action: "revoke"},
		{ID: "op-3", BotID: "bot-a", WatchID: "w-3", Revision: 1, Action: "save"},
		{ID: "", BotID: "bot-ignored"},
	})
	state.bots["bot-a-second"] = &botScheduleState{
		operation: &scheduledWatchOperation{id: "op-0", botID: "bot-a", watchID: "w-0", action: "cancel"},
	}
	pending := state.pendingOperations()
	if len(pending) != 3 || pending[0].BotID != "bot-a" || pending[0].ID != "op-0" || pending[1].ID != "op-1" || pending[2].BotID != "bot-b" {
		t.Fatalf("pendingOperations = %+v", pending)
	}

	bot := state.bot("bot-a")
	bot.review = &scheduledWatchReview{needed: false}
	opGrant := &scheduledWatchOperation{id: "op-1", botID: "bot-a", grantID: "g-1", revision: 2, action: "revoke"}
	bot.operation = opGrant
	bot.finishOperation(opGrant, pi.ProductReceipt{
		OperationID: "op-1",
		State:       "accepted",
		Target:      pi.ProductReceiptTarget{BotID: "bot-a", GrantID: "g-1"},
		Result:      []byte(`{"grant":{"grantId":"g-1","ownerBotId":"bot-a","revision":2,"state":"revoked"},"invalidatedWatchIds":["w-9"]}`),
	}, nil, false)
	if bot.operation != nil || bot.result != "revoke" || len(bot.invalidated) != 1 || bot.invalidated[0] != "w-9" {
		t.Fatalf("grant revoke finish = op:%v result:%q inv:%v", bot.operation, bot.result, bot.invalidated)
	}

	opWatch := &scheduledWatchOperation{id: "op-2", botID: "bot-a", watchID: "w-1", revision: 1, action: "activate"}
	bot.operation = opWatch
	bot.review = &scheduledWatchReview{needed: false}
	bot.finishOperation(opWatch, pi.ProductReceipt{}, &pi.CommandError{
		Delivery: pi.Rejected,
		Err:      &pi.RemoteError{Code: "grant_required", Message: "approve grant"},
	}, false)
	if bot.operation != nil || !bot.review.needed || bot.problem != nil {
		t.Fatalf("grant_required rejection = op:%v review:%+v err:%v", bot.operation, bot.review, bot.problem)
	}

	bot.operation = opWatch
	bot.finishOperation(opWatch, pi.ProductReceipt{}, &pi.CommandError{
		Delivery: pi.NotSent,
		Err:      errors.New("pipe closed"),
	}, false)
	if bot.operation != nil || bot.problem == nil {
		t.Fatalf("NotSent error did not settle operation: op=%v problem=%v", bot.operation, bot.problem)
	}

	bot.watchID = "w-1"
	bot.watches.ready, bot.grants.ready, bot.detail.ready = true, true, true
	state.applyWatchChanged(pi.ProductWatchChange{BotID: "bot-a", WatchID: "w-1", Change: "saved"})
	if bot.watches.ready || bot.detail.ready || !bot.grants.ready {
		t.Fatalf("targeted watch invalidation = watches:%v detail:%v grants:%v", bot.watches.ready, bot.detail.ready, bot.grants.ready)
	}
}
