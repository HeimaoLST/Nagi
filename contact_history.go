package main

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

type contactHistoryMatch struct {
	SessionID string
	EntryID   string
	Date      string
	From      string
	Preview   string
}

type contactHistoryFailure struct {
	SessionID string
	Err       error
}

type contactHistoryResult struct {
	Matches  []contactHistoryMatch
	Failures []contactHistoryFailure
	Limited  bool
}

const contactHistoryMatchLimit = 100

func (a *appModel) searchContactHistory(h *piAppHost, bindings []piConversationBinding, query string) contactHistoryResult {
	query = strings.ToLower(strings.TrimSpace(query))
	result := contactHistoryResult{}
	seen := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		sessionRef := binding.Context.SessionRef
		if seen[sessionRef] {
			continue
		}
		seen[sessionRef] = true
		projection, err := h.loadPiHistory(context.Background(), binding)
		if err != nil {
			result.Failures = append(result.Failures, contactHistoryFailure{SessionID: sessionRef, Err: err})
			continue
		}
		if projection.detail != "" {
			result.Failures = append(result.Failures, contactHistoryFailure{SessionID: sessionRef, Err: errors.New(projection.detail)})
		}
		for i := len(projection.entries) - 1; i >= 0; i-- {
			entry := projection.entries[i]
			if !strings.Contains(strings.ToLower(entry.Text), query) {
				continue
			}
			result.Matches = append(result.Matches, contactHistoryMatch{
				SessionID: sessionRef, EntryID: entry.ID, Date: entry.Date, From: entry.From,
				Preview: contactHistoryPreview(entry.Text, query),
			})
			if len(result.Matches) == contactHistoryMatchLimit {
				result.Limited = true
				return result
			}
		}
	}
	return result
}

func contactHistoryPreview(text, query string) string {
	const length = 240
	runes := []rune(text)
	if len(runes) <= length {
		return text
	}
	start := 0
	lower := strings.ToLower(text)
	if index := strings.Index(lower, query); index >= 0 {
		start = max(0, utf8.RuneCountInString(lower[:index])-60)
	}
	end := min(len(runes), start+length)
	preview := string(runes[start:end])
	if start > 0 {
		preview = "…" + preview
	}
	if end < len(runes) {
		preview += "…"
	}
	return preview
}
