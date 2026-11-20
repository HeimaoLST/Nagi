package main

import (
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
