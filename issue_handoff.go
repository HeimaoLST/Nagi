package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

type issueHandoff struct {
	SourceBotID      string   `json:"source_bot_id"`
	SourceSessionRef string   `json:"source_session_ref"`
	SourceTurnID     string   `json:"source_turn_id"`
	Files            []string `json:"files,omitempty"`
}

func cloneIssueHandoff(h *issueHandoff) *issueHandoff {
	if h == nil {
		return nil
	}
	cloned := *h
	cloned.Files = append([]string(nil), h.Files...)
	return &cloned
}

func validHandoffFiles(files []string) bool {
	if len(files) > 16 {
		return false
	}
	for _, path := range files {
		if !filepath.IsAbs(path) || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n") {
			return false
		}
	}
	return true
}

func storedIssueHandoffValid(issue storedIssue, state storedState) bool {
	h := issue.Handoff
	if h.SourceBotID == "" || h.SourceBotID == issue.ContactID || h.SourceSessionRef == "" || h.SourceTurnID == "" || !validHandoffFiles(h.Files) {
		return false
	}
	for _, scope := range state.IssueToolScopes {
		if scope.Mode != issueToolModeWork && scope.ContactID == h.SourceBotID && scope.RuntimeSessionID == h.SourceSessionRef && scope.TurnID == h.SourceTurnID && scope.AllowsIssueID(issue.ID) {
			return true
		}
	}
	return false
}

func (a *appModel) issuesVisibleTo(ct *contact) []*productIssue {
	issues := ct.Issues
	for _, recipient := range a.contacts {
		if recipient == ct {
			continue
		}
		for _, issue := range recipient.Issues {
			if issue != nil && issue.Handoff != nil && issue.Handoff.SourceBotID == ct.ID {
				if len(issues) == len(ct.Issues) {
					issues = append([]*productIssue(nil), issues...)
				}
				issues = append(issues, issue)
			}
		}
	}
	return issues
}

func (a *appModel) issueToolTarget(scope IssueToolScope, issueID string) (*contact, *productIssue) {
	source := a.contactByID(scope.ContactID)
	if source == nil || !scope.AllowsIssueID(issueID) {
		return nil, nil
	}
	if issue := source.issueByID(issueID); issue != nil {
		return source, issue
	}
	if scope.Mode != issueToolModeWork {
		for _, recipient := range a.contacts {
			if issue := recipient.issueByID(issueID); issue != nil && issue.Handoff != nil && issue.Handoff.SourceBotID == source.ID {
				return recipient, issue
			}
		}
	}
	return nil, nil
}

type botToolRecipient struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

func (a *appModel) issueToolListBots(_ context.Context, raw json.RawMessage) (issueToolForwardResponse, error) {
	var args struct {
		Scope  string `json:"scope"`
		Cursor string `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return issueToolErrorResponse("invalid_arguments", "invalid bot_list arguments")
	}
	result := struct {
		Bots       []botToolRecipient `json:"bots"`
		NextCursor string             `json:"next_cursor,omitempty"`
	}{Bots: []botToolRecipient{}}
	var callErr error
	a.runOnUI(func() {
		scope, err := a.issueToolScopeByRef(args.Scope)
		if err != nil || scope.Mode == issueToolModeWork {
			callErr = &IssueToolError{Code: "scope_denied", Message: "bot_list requires a conversation scope"}
			return
		}
		start := args.Cursor == ""
		for _, ct := range a.contacts {
			if !start {
				start = ct.ID == args.Cursor
				continue
			}
			if ct.Archived || ct.ID == scope.ContactID {
				continue
			}
			if len(result.Bots) == 16 {
				result.NextCursor = result.Bots[len(result.Bots)-1].ID
				break
			}
			result.Bots = append(result.Bots, botToolRecipient{
				ID: ct.ID, Name: truncateIssueToolText(ct.Name, 128), Role: truncateIssueToolText(ct.Role, 128),
			})
		}
		if !start {
			callErr = &IssueToolError{Code: "invalid_cursor", Message: "bot_list cursor is no longer available"}
		}
	})
	if callErr != nil {
		return issueToolConsumeError(callErr)
	}
	return issueToolJSONResponse(result, false)
}

func (a *appModel) delegateActivity(raw json.RawMessage) (issueToolForwardResponse, error) {
	var args struct {
		Scope        string   `json:"scope"`
		Key          string   `json:"key"`
		Goal         string   `json:"goal"`
		Requirements []string `json:"requirements"`
		AssigneeID   string   `json:"assignee_id"`
		Files        []string `json:"files"`
	}
	if err := json.Unmarshal(raw, &args); err != nil || args.Key == "" {
		return issueToolErrorResponse("invalid_arguments", "delegate_activity requires a stable key and an activity goal")
	}
	created, err := a.applyIssueToolAction(IssueToolActionRequest{
		ScopeRef: args.Scope, Action: issueToolActionCreate, CreateKey: args.Key,
		Goal: args.Goal, Requirements: args.Requirements, AssigneeID: args.AssigneeID, Files: args.Files,
	})
	if err != nil || created.IsError {
		return created, err
	}
	var saved IssueToolReceipt
	if err := json.Unmarshal([]byte(created.Content[0].Text), &saved); err != nil {
		return issueToolErrorResponse("operation_unknown", "the activity creation receipt could not be read")
	}
	started, err := a.applyIssueToolAction(IssueToolActionRequest{
		ScopeRef: args.Scope, Action: issueToolActionRun, IssueID: saved.IssueID,
	})
	if err != nil {
		return started, err
	}
	if started.IsError {
		return issueToolJSONResponse(struct {
			Activity   IssueToolReceipt `json:"activity"`
			StartError *IssueToolError  `json:"start_error"`
		}{Activity: saved, StartError: issueToolResponseError(started)}, false)
	}
	return started, nil
}
