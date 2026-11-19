package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func canReadPiIssueResult(ct *contact, issue *productIssue, execution *issueExecution) bool {
	if ct == nil || !isPiActivityIssue(issue) || issue.ContactID != ct.ID || ct.issueByID(issue.ID) != issue ||
		execution == nil || execution.ID == "" || !execution.PiIntent || execution.PiRun == nil {
		return false
	}
	binding, run := issue.PiBinding.Context, execution.PiRun
	if !binding.Used || !filepath.IsAbs(binding.CWD) || !run.Admitted || run.WorkerEpoch == "" || run.RunID == "" ||
		run.ContextID != binding.ContextID || run.SessionRef != binding.SessionRef || execution.SessionID != run.SessionRef || execution.TaskID != run.RunID {
		return false
	}
	if readablePiIssueStatus(execution.Status) {
		for _, candidate := range issue.Executions {
			if candidate == execution {
				return true
			}
		}
	}
	return false
}

func readablePiIssueStatus(status string) bool {
	return status == issueExecutionCompleted || status == issueExecutionFailed || status == issueExecutionInterrupted
}

type piIssueResult struct {
	cwd      string
	response string
	files    []issueResultFile
}

type piResultMessagePart struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type piResultToolCall struct {
	name string
	path string
}

func (h *piAppHost) readPiIssueResult(ctx context.Context, binding piActivityBinding, run piActivityRun, executionID, status string) (piIssueResult, error) {
	actual, err := h.openActivity(ctx, binding)
	if err != nil {
		return piIssueResult{}, err
	}
	if actual != binding.Context || actual.ContextID != run.ContextID || actual.SessionRef != run.SessionRef {
		return piIssueResult{}, errors.New("Pi returned a different activity binding")
	}

	result := piIssueResult{cwd: actual.CWD}
	var lastAssistant struct {
		StopReason string                `json:"stopReason"`
		Content    []piResultMessagePart `json:"content"`
	}
	calls := make(map[string]piResultToolCall)
	seenCursors := make(map[string]struct{})
	cursor := ""
	found := false
history:
	for {
		page, err := h.client.History(ctx, pi.HistoryParams{ContextID: run.ContextID, Cursor: cursor, Limit: 100})
		if err != nil {
			return piIssueResult{}, err
		}
		if page.ContextID != run.ContextID || page.SessionRef != run.SessionRef {
			return piIssueResult{}, errors.New("Pi history belongs to a different activity")
		}
		lastID := ""
		for _, raw := range page.Entries {
			var entry struct {
				ID         string          `json:"id"`
				Type       string          `json:"type"`
				CustomType string          `json:"customType"`
				Data       json.RawMessage `json:"data"`
				Message    json.RawMessage `json:"message"`
			}
			if err := json.Unmarshal(raw, &entry); err != nil || entry.ID == "" {
				return piIssueResult{}, errors.New("Invalid Pi public history entry")
			}
			lastID = entry.ID
			if entry.Type == "custom" && entry.CustomType == "nagi-turn-v1" {
				if found {
					break history
				}
				var turn struct {
					RunID       string            `json:"runId"`
					Correlation pi.RunCorrelation `json:"correlation"`
				}
				if err := json.Unmarshal(entry.Data, &turn); err != nil || turn.RunID == "" {
					return piIssueResult{}, errors.New("Invalid Pi turn marker")
				}
				sameRun, sameExecution := turn.RunID == run.RunID, turn.Correlation.ExecutionID == executionID
				if sameRun != sameExecution {
					return piIssueResult{}, errors.New("Pi turn marker does not match the recorded run and execution")
				}
				found = sameRun
				continue
			}
			if !found || entry.Type != "message" {
				continue
			}
			var header struct {
				Role string `json:"role"`
			}
			if err := json.Unmarshal(entry.Message, &header); err != nil || header.Role == "" {
				return piIssueResult{}, errors.New("Invalid Pi history message role")
			}
			switch header.Role {
			case "assistant":
				clear(lastAssistant.Content)
				lastAssistant.StopReason, lastAssistant.Content = "", lastAssistant.Content[:0]
				if err := json.Unmarshal(entry.Message, &lastAssistant); err != nil || lastAssistant.StopReason == "" {
					return piIssueResult{}, errors.New("Invalid finalized Pi assistant message")
				}
				for _, part := range lastAssistant.Content {
					if part.Type != "toolCall" || part.ID == "" {
						continue
					}
					if _, exists := calls[part.ID]; exists {
						calls[part.ID] = piResultToolCall{}
						continue
					}
					var args struct {
						Path string `json:"path"`
					}
					if (part.Name == "write" || part.Name == "edit") && json.Unmarshal(part.Arguments, &args) == nil {
						calls[part.ID] = piResultToolCall{name: part.Name, path: args.Path}
					} else {
						calls[part.ID] = piResultToolCall{}
					}
				}
			case "toolResult":
				var receipt struct {
					CallID  string `json:"toolCallId"`
					Name    string `json:"toolName"`
					IsError *bool  `json:"isError"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				}
				if err := json.Unmarshal(entry.Message, &receipt); err != nil {
					continue
				}
				call, exists := calls[receipt.CallID]
				if !exists || call.name == "" {
					continue
				}
				calls[receipt.CallID] = piResultToolCall{}
				if receipt.Name != call.name || receipt.IsError == nil || *receipt.IsError || len(receipt.Content) != 1 || receipt.Content[0].Type != "text" {
					continue
				}
				if path := piResultReceiptPath(result.cwd, call, receipt.Content[0].Text); path != "" {
					result.files = append(result.files, issueResultFile{
						ExecutionID: executionID, SessionID: run.SessionRef, TaskID: run.RunID,
						MessageID: entry.ID, CallID: receipt.CallID, ToolName: call.name,
						Path: path, WorkingDirectory: result.cwd, RawResult: string(entry.Message),
					})
				}
			}
		}
		if page.NextCursor == "" {
			break
		}
		if _, seen := seenCursors[page.NextCursor]; seen || page.NextCursor != lastID {
			return piIssueResult{}, errors.New("Pi history cursor did not advance")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	if !found {
		return piIssueResult{}, errors.New("Pi history does not contain the recorded turn")
	}
	if status == issueExecutionCompleted && lastAssistant.StopReason != "stop" {
		return piIssueResult{}, errors.New("Pi history lacks the final assistant response for the completed run")
	}
	var text strings.Builder
	for _, part := range lastAssistant.Content {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
	}
	result.response = text.String()
	return result, nil
}

func piResultReceiptPath(cwd string, call piResultToolCall, acknowledgement string) string {
	if call.path == "" || strings.ContainsRune(call.path, 0) {
		return ""
	}
	switch call.name {
	case "write":
		if acknowledgement != "Successfully wrote to "+call.path {
			return ""
		}
	case "edit":
		suffix := " block(s) in " + call.path + "."
		count, prefixed := strings.CutPrefix(acknowledgement, "Successfully replaced ")
		count, suffixed := strings.CutSuffix(count, suffix)
		n, err := strconv.Atoi(count)
		if !prefixed || !suffixed || err != nil || n < 1 || strconv.Itoa(n) != count {
			return ""
		}
	default:
		return ""
	}
	path := strings.Map(func(r rune) rune {
		switch {
		case r == '\u00a0', r >= '\u2000' && r <= '\u200a', r == '\u202f', r == '\u205f', r == '\u3000':
			return ' '
		default:
			return r
		}
	}, strings.TrimPrefix(call.path, "@"))
	if path == "" || path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "file://") {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd, path)
}
