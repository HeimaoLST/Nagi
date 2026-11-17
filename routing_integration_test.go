package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func containsText(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func contactMessageTexts(ct *contact) []string {
	out := make([]string, 0, len(ct.Messages))
	for _, m := range ct.Messages {
		out = append(out, m.Text)
	}
	return out
}

func TestIssueWorkMessageDoesNotBecomeConversationUnread(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", SessionID: "conversation-x", Loaded: true}
	issue := &productIssue{ID: "i1", ContactID: "x", Goal: "work", WorkSessionID: "work-i1"}
	issue.WorkSessionID = "work-i1"
	issue.Work.ActiveTaskID = "t1"
	ct.Issues = []*productIssue{issue}
	other := &contact{ID: "y", Name: "Y"}
	a := &appModel{contacts: []*contact{ct, other}, selected: 1, scrollTo: -1}
	a.applyIssueEvent(ct, issue, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "work-i1", TaskID: "t1",
		Event: &runtimeEvent{Type: "response_text", ItemID: "text-1", Text: "issue result"},
	})
	if len(ct.Messages) != 0 || ct.Unread != 0 {
		t.Fatalf("issue message entered conversation/unread: messages=%d unread=%d", len(ct.Messages), ct.Unread)
	}
	if len(issue.Work.Messages) != 1 || issue.Work.Messages[0].Text != "issue result" {
		t.Fatalf("issue work message missing: %+v", issue.Work.Messages)
	}
}

func TestLateIssueTaskTerminalDoesNotReplaceCurrentWork(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", Loaded: true}
	issue := &productIssue{ID: "i1", ContactID: "x", Goal: "work"}
	issue.WorkSessionID = "work-i1"
	issue.Work.ActiveTaskID = "t2"
	issue.Work.LastTaskID = "t2"
	issue.Work.SendState = sendAccepted
	issue.Executions = []*issueExecution{
		{ID: "e1", SessionID: "work-i1", TaskID: "t1", Status: issueExecutionCompleted},
		{ID: "e2", SessionID: "work-i1", TaskID: "t2", Status: issueExecutionRunning},
	}
	ct.Issues = []*productIssue{issue}
	ct.Works = []*workItem{{TaskID: "t1", State: workCompleted}, {TaskID: "t2", State: workRunning}}
	a := &appModel{contacts: []*contact{ct}, selected: 0, scrollTo: -1}
	a.applyIssueEvent(ct, issue, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "work-i1", TaskID: "t1",
		Event: &runtimeEvent{Type: "task_finished", Status: "failed"},
	})
	if issue.Work.ActiveTaskID != "t2" || issue.Work.SendState == sendIdle {
		t.Fatalf("late terminal replaced current issue work: %+v", issue.Work)
	}
	if issue.Executions[0].Status != issueExecutionCompleted || issue.Executions[1].Status != issueExecutionRunning {
		t.Fatalf("late terminal rewrote executions: %+v", issue.Executions)
	}
}

func TestRuntimeErrorReasonIsDecoded(t *testing.T) {
	var err runtimeError
	if err := json.Unmarshal([]byte(`{"code":"busy","message":"runtime task capacity is full","reason":"capacity_exceeded"}`), &err); err != nil {
		t.Fatal(err)
	}
	if err.Code != "busy" || err.Reason != "capacity_exceeded" || !strings.Contains(runtimeMessage(&err), "capacity_exceeded") {
		t.Fatalf("runtime reason DTO = %+v / %q", err, runtimeMessage(&err))
	}
}
