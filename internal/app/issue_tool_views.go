package app

import "sort"

func scopeAllowsIssue(s IssueToolScope, issue *productIssue) bool {
	if issue == nil || s.Validate() != nil {
		return false
	}
	return (issue.ContactID == s.ContactID || s.Mode != issueToolModeWork && issue.Handoff != nil && issue.Handoff.SourceBotID == s.ContactID) && s.AllowsIssueID(issue.ID)
}

func issueToolSummary(issue *productIssue) IssueToolIssueSummary {
	summary := IssueToolIssueSummary{}
	if issue == nil {
		return summary
	}
	summary.ID, summary.Goal, summary.Status = issue.ID, issue.Goal, issue.Status
	summary.Archived, summary.RequirementsRevision = issue.Archived, issue.RequirementsRevision
	if issue.Handoff != nil {
		summary.OwnerBotID, summary.SourceBotID = issue.ContactID, issue.Handoff.SourceBotID
	}
	if issue.Supplements != nil {
		summary.PendingSupplements = issue.Supplements.PendingCount()
	}
	if question := issue.openQuestion(); question != nil {
		summary.QuestionRef, summary.Question, summary.QuestionRevision = question.Ref, question.Text, question.RequirementsRevision
	}
	for index := len(issue.Executions) - 1; index >= 0; index-- {
		execution := issue.Executions[index]
		if execution == nil {
			continue
		}
		if execution.ReturnedAt != 0 && summary.LastReturnedExecutionID == "" {
			summary.LastReturnedExecutionID = execution.ID
		}
		if summary.LastExecutionID == "" {
			summary.LastExecutionID, summary.LastExecutionState = execution.ID, execution.Status
		}
		if summary.ActiveExecutionID == "" && (execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning || execution.Status == issueExecutionPending) {
			summary.ActiveExecutionID = execution.ID
		}
	}
	return summary
}

// issueToolList reads existing product facts without mutating issues or
// inferring targets from GUI selection.
func issueToolList(scope IssueToolScope, issues []*productIssue, includeArchived bool, limit int, cursor string) (IssueToolIssueListResult, error) {
	if err := scope.Validate(); err != nil {
		return IssueToolIssueListResult{}, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	all := make([]IssueToolIssueSummary, 0)
	for _, issue := range issues {
		if !scopeAllowsIssue(scope, issue) || (!includeArchived && issue.Archived) {
			continue
		}
		all = append(all, issueToolSummary(issue))
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Status != all[j].Status {
			return all[i].Status < all[j].Status
		}
		return all[i].ID < all[j].ID
	})
	start := 0
	if cursor != "" {
		for i := range all {
			if all[i].ID == cursor {
				start = i + 1
				break
			}
		}
		if start == 0 {
			return IssueToolIssueListResult{}, &IssueToolError{Code: "invalid_cursor", Message: "issue list cursor is not in this scope"}
		}
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	result := IssueToolIssueListResult{Issues: all[start:end]}
	if end < len(all) {
		result.HasMore = true
		result.NextCursor = all[end-1].ID
	}
	return result, nil
}

func issueToolGet(scope IssueToolScope, issues []*productIssue, issueID string) (*IssueToolIssueDetail, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	var issue *productIssue
	for _, candidate := range issues {
		if candidate != nil && candidate.ID == issueID {
			issue = candidate
			break
		}
	}
	if issue == nil {
		return nil, &IssueToolError{Code: "not_found", Message: "issue was not found in this scope"}
	}
	if !scopeAllowsIssue(scope, issue) {
		return nil, &IssueToolError{Code: "scope_denied", Message: "issue is outside the issued scope"}
	}
	detail := &IssueToolIssueDetail{Issue: issueToolSummary(issue)}
	for _, requirement := range issue.Requirements {
		if requirement == nil {
			continue
		}
		detail.Requirements = append(detail.Requirements, IssueToolRequirementView{
			ID: requirement.ID, Text: requirement.Text, State: requirement.State, Evidence: requirement.Evidence,
		})
	}
	executionStart := 0
	if len(issue.Executions) > issueToolExecutionPageLimit {
		executionStart = len(issue.Executions) - issueToolExecutionPageLimit
		detail.ExecutionsHasMore = true
	}
	for _, execution := range issue.Executions[executionStart:] {
		if execution == nil {
			continue
		}
		detail.Executions = append(detail.Executions, IssueToolExecutionView{
			ID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID,
			Status: execution.Status, Detail: execution.Detail,
			PiRun:                clonePiActivityRun(execution.PiRun),
			ReturnedAt:           execution.ReturnedAt,
			RequirementsRevision: execution.RequirementsRevision,
		})
	}
	detail.ExecutionCount = len(issue.Executions)
	return detail, nil
}
