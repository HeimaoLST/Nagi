package app

import "sort"

func scopeAllowsActivity(s activityToolScope, activity *productActivity) bool {
	if activity == nil || s.Validate() != nil {
		return false
	}
	return (activity.ContactID == s.ContactID || s.Mode != activityToolModeWork && activity.Handoff != nil && activity.Handoff.SourceBotID == s.ContactID) && s.AllowsIssueID(activity.ID)
}

func activityToolSummary(activity *productActivity) activityToolSummaryView {
	summary := activityToolSummaryView{}
	if activity == nil {
		return summary
	}
	summary.ID, summary.Goal, summary.Status = activity.ID, activity.Goal, activity.Status
	summary.Archived, summary.RequirementsRevision = activity.Archived, activity.RequirementsRevision
	if activity.Handoff != nil {
		summary.OwnerBotID, summary.SourceBotID = activity.ContactID, activity.Handoff.SourceBotID
	}
	if activity.Supplements != nil {
		summary.PendingSupplements = activity.Supplements.PendingCount()
	}
	if question := activity.openQuestion(); question != nil {
		summary.QuestionRef, summary.Question, summary.QuestionRevision = question.Ref, question.Text, question.RequirementsRevision
	}
	for index := len(activity.Executions) - 1; index >= 0; index-- {
		execution := activity.Executions[index]
		if execution == nil {
			continue
		}
		if execution.ReturnedAt != 0 && summary.LastReturnedExecutionID == "" {
			summary.LastReturnedExecutionID = execution.ID
		}
		if summary.LastExecutionID == "" {
			summary.LastExecutionID, summary.LastExecutionState = execution.ID, execution.Status
		}
		if summary.ActiveExecutionID == "" && (execution.Status == activityExecutionAccepted || execution.Status == activityExecutionRunning || execution.Status == activityExecutionPending) {
			summary.ActiveExecutionID = execution.ID
		}
	}
	return summary
}

// activityToolList reads existing product facts without mutating activities or
// inferring targets from GUI selection.
func activityToolList(scope activityToolScope, activities []*productActivity, includeArchived bool, limit int, cursor string) (activityToolListResult, error) {
	if err := scope.Validate(); err != nil {
		return activityToolListResult{}, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	all := make([]activityToolSummaryView, 0)
	for _, activity := range activities {
		if !scopeAllowsActivity(scope, activity) || (!includeArchived && activity.Archived) {
			continue
		}
		all = append(all, activityToolSummary(activity))
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
			return activityToolListResult{}, &activityToolError{Code: "invalid_cursor", Message: "issue list cursor is not in this scope"}
		}
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	result := activityToolListResult{Issues: all[start:end]}
	if end < len(all) {
		result.HasMore = true
		result.NextCursor = all[end-1].ID
	}
	return result, nil
}

func activityToolGet(scope activityToolScope, activities []*productActivity, activityID string) (*activityToolDetail, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	var activity *productActivity
	for _, candidate := range activities {
		if candidate != nil && candidate.ID == activityID {
			activity = candidate
			break
		}
	}
	if activity == nil {
		return nil, &activityToolError{Code: "not_found", Message: "issue was not found in this scope"}
	}
	if !scopeAllowsActivity(scope, activity) {
		return nil, &activityToolError{Code: "scope_denied", Message: "issue is outside the issued scope"}
	}
	detail := &activityToolDetail{Issue: activityToolSummary(activity)}
	for _, requirement := range activity.Requirements {
		if requirement == nil {
			continue
		}
		detail.Requirements = append(detail.Requirements, activityToolRequirementView{
			ID: requirement.ID, Text: requirement.Text, State: requirement.State, Evidence: requirement.Evidence,
		})
	}
	executionStart := 0
	if len(activity.Executions) > activityToolExecutionPageLimit {
		executionStart = len(activity.Executions) - activityToolExecutionPageLimit
		detail.ExecutionsHasMore = true
	}
	for _, execution := range activity.Executions[executionStart:] {
		if execution == nil {
			continue
		}
		detail.Executions = append(detail.Executions, activityToolExecutionView{
			ID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID,
			Status: execution.Status, Detail: execution.Detail,
			PiRun:                clonePiActivityRun(execution.PiRun),
			ReturnedAt:           execution.ReturnedAt,
			RequirementsRevision: execution.RequirementsRevision,
		})
	}
	detail.ExecutionCount = len(activity.Executions)
	return detail, nil
}
