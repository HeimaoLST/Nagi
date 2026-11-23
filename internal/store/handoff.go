package store

import (
	"path/filepath"
	"strings"
)

type IssueHandoff struct {
	SourceBotID      string   `json:"source_bot_id"`
	SourceSessionRef string   `json:"source_session_ref"`
	SourceTurnID     string   `json:"source_turn_id"`
	Files            []string `json:"files,omitempty"`
}

func CloneIssueHandoff(h *IssueHandoff) *IssueHandoff {
	if h == nil {
		return nil
	}
	cloned := *h
	cloned.Files = append([]string(nil), h.Files...)
	return &cloned
}

func ValidHandoffFiles(files []string) bool {
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

func StoredIssueHandoffValid(issue StoredIssue, state StoredState) bool {
	h := issue.Handoff
	if h.SourceBotID == "" || h.SourceBotID == issue.ContactID || h.SourceSessionRef == "" || h.SourceTurnID == "" || !ValidHandoffFiles(h.Files) {
		return false
	}
	for _, scope := range state.IssueToolScopes {
		if scope.Mode != IssueToolModeWork && scope.ContactID == h.SourceBotID && scope.RuntimeSessionID == h.SourceSessionRef && scope.TurnID == h.SourceTurnID && scope.AllowsIssueID(issue.ID) {
			return true
		}
	}
	return false
}
