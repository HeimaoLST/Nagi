package app

import (
	"slices"
	"testing"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func TestActivityLifecycleGuards(t *testing.T) {
	completed := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{ActivityID: "a1", State: "completed"},
		Executions:             []pi.ProductExecution{{ExecutionID: "e1", State: "completed"}},
	}
	if !canAcknowledgeActivity(completed, completed.Executions[0]) {
		t.Fatal("completed activity with completed execution should be acknowledgeable")
	}
	if !canCloseActivity(completed) {
		t.Fatal("completed activity with settled execution should be closeable")
	}

	completed.OpenQuestionIDs = []string{"q1"}
	if canAcknowledgeActivity(completed, completed.Executions[0]) || canCloseActivity(completed) {
		t.Fatal("open question must block acknowledge and close")
	}

	completed.OpenQuestionIDs = nil
	completed.PendingApprovalRequestIDs = []string{"req-1"}
	if canAcknowledgeActivity(completed, completed.Executions[0]) || canCloseActivity(completed) {
		t.Fatal("pending approval must block acknowledge and close")
	}

	completed.PendingApprovalRequestIDs = nil
	completed.AcknowledgedAt = "2026-11-27T00:00:00Z"
	if canAcknowledgeActivity(completed, completed.Executions[0]) {
		t.Fatal("already acknowledged activity must not be acknowledgeable")
	}

	unknownAct := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{ActivityID: "a2", State: "unknown"},
		Executions:             []pi.ProductExecution{{ExecutionID: "e2", State: "unknown"}},
	}
	if !canCloseActivity(unknownAct) {
		t.Fatal("unknown activity with unknown latest execution should be closeable")
	}

	running := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{ActivityID: "a3", State: "running"},
		Executions: []pi.ProductExecution{
			{ExecutionID: "e1", State: "completed"},
			{ExecutionID: "e2", State: "running"},
		},
	}
	panel := &productActivityPanel{}
	if !canSteerActivity(panel, running, running.Executions[1]) {
		t.Fatal("latest running execution should be steerable")
	}
	if canSteerActivity(panel, running, running.Executions[0]) {
		t.Fatal("non-latest execution must not be steerable")
	}
	panel.stopping = "e2"
	if canSteerActivity(panel, running, running.Executions[1]) {
		t.Fatal("stopping execution must not be steerable")
	}
}

func TestValidActivityControlTargetAndSteerOutcome(t *testing.T) {
	a := &appModel{}
	ct := &contact{ID: "bot-1"}
	noFreeText := false
	activity := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{
			ActivityID: "act-1",
			OwnerBotID: "bot-1",
			Revision:   3,
			State:      "running",
		},
		Questions: []pi.ProductQuestion{
			{
				QuestionID:    "q1",
				ExecutionID:   "exec-1",
				Revision:      2,
				State:         "open",
				AllowFreeText: &noFreeText,
				Options:       []pi.ProductQuestionOption{{ID: "opt-1", Label: "Approve"}},
			},
		},
		Executions: []pi.ProductExecution{{ExecutionID: "exec-1", State: "running"}},
	}
	target := activityAnswerIntent(ct, activity, activity.Questions[0])
	form := &productActivityForm{target: target, text: "free text"}
	if a.validActivityControlTarget(ct, activity, target, form) {
		t.Fatal("option-only question accepted free text without selected option")
	}
	form.target.SelectedOptionID = "opt-1"
	form.text = "mismatch"
	if a.validActivityControlTarget(ct, activity, target, form) {
		t.Fatal("option answer accepted mismatched label text")
	}
	form.text = "Approve"
	if !a.validActivityControlTarget(ct, activity, target, form) {
		t.Fatal("matching option answer was rejected")
	}
	target.Revision = 1
	if a.validActivityControlTarget(ct, activity, target, form) {
		t.Fatal("stale question revision was accepted")
	}

	steerTarget := productActivityIntent{
		BotID:       "bot-1",
		ActivityID:  "act-1",
		ExecutionID: "exec-1",
		Action:      "steer",
		OperationID: "op-steer-1",
		Revision:    3,
	}
	if !a.validActivityControlTarget(ct, activity, steerTarget, &productActivityForm{text: "focus on tests"}) {
		t.Fatal("valid steer target was rejected")
	}
	ct.Archived = true
	if a.validActivityControlTarget(ct, activity, steerTarget, &productActivityForm{text: "focus on tests"}) {
		t.Fatal("archived bot allowed steer")
	}

	if acc, rej := activitySteerOutcome(steerTarget, pi.ProductActivitySteer{
		OperationID: "op-steer-1",
		ActivityID:  "act-1",
		ExecutionID: "exec-1",
		SteerID:     "steer-1",
		State:       "queued",
	}); !acc || rej {
		t.Fatalf("queued steer outcome = (%v, %v), want (true, false)", acc, rej)
	}
	if acc, rej := activitySteerOutcome(steerTarget, pi.ProductActivitySteer{
		OperationID: "op-steer-1",
		ActivityID:  "act-1",
		ExecutionID: "exec-1",
		State:       "rejected",
	}); acc || !rej {
		t.Fatalf("rejected steer outcome = (%v, %v), want (false, true)", acc, rej)
	}
	if acc, rej := activitySteerOutcome(steerTarget, pi.ProductActivitySteer{
		OperationID: "other-op",
		ActivityID:  "act-1",
		ExecutionID: "exec-1",
		SteerID:     "steer-1",
		State:       "queued",
	}); acc || rej {
		t.Fatalf("mismatched operation outcome = (%v, %v), want (false, false)", acc, rej)
	}
}

func TestActivityNoticesPriorityFilteringAndDismissal(t *testing.T) {
	approvalAct := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{
			ActivityID:                "act-appr",
			OwnerBotID:                "bot-1",
			Revision:                  1,
			State:                     "running",
			PendingApprovalRequestIDs: []string{"req-1"},
		},
		ApprovalRequests: []pi.ProductSkillApprovalRequest{
			{RequestID: "req-1", BotID: "bot-1", ActivityID: "act-appr", State: "open", Revision: 1, SkillID: "sk-1", Script: "run"},
		},
		Executions: []pi.ProductExecution{{ExecutionID: "e-appr", State: "running"}},
	}
	questionAct := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{
			ActivityID:      "act-q",
			OwnerBotID:      "bot-1",
			Revision:        1,
			State:           "waitingForAnswer",
			OpenQuestionIDs: []string{"q-1"},
		},
		Questions:  []pi.ProductQuestion{{QuestionID: "q-1", ExecutionID: "e-q", State: "open", Revision: 1}},
		Executions: []pi.ProductExecution{{ExecutionID: "e-q", State: "running"}},
	}
	completedAct := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{
			ActivityID: "act-done",
			OwnerBotID: "bot-1",
			Revision:   1,
			State:      "completed",
		},
		Questions: []pi.ProductQuestion{
			{QuestionID: "q-auto", ExecutionID: "e-done", State: "answered", Provenance: "bot_auto", Answer: &pi.ProductText{Text: "auto"}},
		},
		Executions: []pi.ProductExecution{{ExecutionID: "e-done", State: "completed"}},
	}
	closedAct := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{
			ActivityID: "act-closed",
			OwnerBotID: "bot-1",
			Revision:   2,
			State:      "completed",
			ClosedAt:   "2026-11-27T00:00:00Z",
		},
		Executions: []pi.ProductExecution{{ExecutionID: "e-closed", State: "completed"}},
	}

	ct := &contact{
		ID: "bot-1",
		activeActivities: map[string]pi.ProductActivitySummary{
			approvalAct.ActivityID:  approvalAct.ProductActivitySummary,
			questionAct.ActivityID:  questionAct.ProductActivitySummary,
			completedAct.ActivityID: completedAct.ProductActivitySummary,
		},
		activityNotices: activityNoticeState{
			details: map[string]*pi.ProductActivity{
				approvalAct.ActivityID:  approvalAct,
				questionAct.ActivityID:  questionAct,
				completedAct.ActivityID: completedAct,
				closedAct.ActivityID:    closedAct,
			},
			dismissed: map[activityNoticeKey]bool{},
		},
	}

	notices := activityNotices(ct)
	var kinds []string
	for _, n := range notices {
		kinds = append(kinds, n.key.kind)
	}
	want := []string{"approval_required", "question_open", "completed", "question_auto_saved"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("notice priority order = %v, want %v", kinds, want)
	}

	ct.activityNotices.dismissed[activityNoticeKey{completedAct.ActivityID, "question_auto_saved", "q-auto"}] = true
	ct.activity = productActivityPanel{open: true, activityID: questionAct.ActivityID, activity: questionAct}
	notices = activityNotices(ct)
	kinds = kinds[:0]
	for _, n := range notices {
		kinds = append(kinds, n.key.kind)
	}
	if !slices.Equal(kinds, []string{"approval_required", "completed"}) {
		t.Fatalf("filtered notices = %v, want [approval_required completed]", kinds)
	}
}
