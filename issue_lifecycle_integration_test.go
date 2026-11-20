package main

import "testing"

func TestIssueTerminalLifecycleDoesNotAutoComplete(t *testing.T) {
	issue := &productIssue{ID: "i1", Status: issueStatusInProgress, Work: issueWorkSession{StopRequestedTasks: map[string]bool{}}}
	a := &appModel{}
	a.applyIssueTerminalLifecycle(issue, "t1", "completed")
	if issue.Status != issueStatusInProgress {
		t.Fatalf("completed execution changed issue lifecycle: %q", issue.Status)
	}
	a.applyIssueTerminalLifecycle(issue, "t2", "failed")
	if issue.Status != issueStatusNeedsAttention {
		t.Fatalf("failed execution status = %q", issue.Status)
	}
	issue.Status = issueStatusInProgress
	issue.Work.StopRequestedTasks["t3"] = true
	a.applyIssueTerminalLifecycle(issue, "t3", "interrupted")
	if issue.Status != issueStatusInProgress {
		t.Fatalf("user-stopped execution status = %q", issue.Status)
	}
	issue.Status = issueStatusInProgress
	a.applyIssueTerminalLifecycle(issue, "t4", "interrupted")
	if issue.Status != issueStatusNeedsAttention {
		t.Fatalf("external interrupted status = %q", issue.Status)
	}
	issue.Status = issueStatusCanceled
	a.applyIssueTerminalLifecycle(issue, "t5", "failed")
	if issue.Status != issueStatusCanceled {
		t.Fatalf("terminal event overwrote canceled issue: %q", issue.Status)
	}
}
