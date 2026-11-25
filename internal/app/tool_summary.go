package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/HeimaoLST/Nagi/locales"
)

func toolTitleFor(name, args string) string {
	if name == "" {
		name = "Tool"
	}
	if args == "" {
		return name
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err == nil {
		for _, key := range []string{"path", "command"} {
			if value, ok := parsed[key].(string); ok && strings.TrimSpace(value) != "" {
				return name + ": " + truncate(value, 70)
			}
		}
	}
	return name + ": " + truncate(args, 70)
}

// toolOutputSummary keeps structured receipts collapsed behind explicit Details/Copy.
func toolOutputSummary(msgs locales.Messages, output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	var value map[string]any
	if json.Unmarshal([]byte(output), &value) != nil {
		return truncate(output, 300)
	}
	body := ""
	for _, key := range []string{"content", "stdout", "stderr", "output"} {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			body = truncate(text, 240)
			break
		}
	}
	prefix := ""
	if text, ok := value["error"].(string); ok && strings.TrimSpace(text) != "" {
		prefix = msgs.ToolOutputFailed(truncate(text, 120))
	}
	code, hasCode := value["exit_code"].(float64)
	timedOut, _ := value["timed_out"].(bool)
	switch {
	case timedOut && hasCode && code != 0:
		prefix = msgs.ToolOutputTimedOutExit(fmt.Sprintf("%g", code))
	case timedOut:
		prefix = msgs.ToolOutputTimedOut()
	case hasCode && code != 0:
		if prefix == "" {
			prefix = msgs.ToolOutputFailedExit(fmt.Sprintf("%g", code))
		} else {
			prefix += " (" + fmt.Sprintf("%g", code) + ")"
		}
	}
	if prefix != "" {
		if body != "" {
			return prefix + ": " + body
		}
		return prefix
	}
	if body != "" {
		return body
	}
	return msgs.ToolOutputStructured()
}
