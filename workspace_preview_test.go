package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPreviewChatToolActivityWrapping(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write chat tool activity previews")
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, size := range []struct{ width, height int }{{1040, 720}, {960, 640}, {720, 640}, {320, 900}, {360, 800}, {1380, 900}} {
			a, ct := workspaceTestApp(t)
			longPath := "/Users/someone/" + strings.Repeat("非常长的目录/", 20) + "file.json"
			ct.Messages = []*message{{
				Key: "tool:read", Kind: msgActivity, CallID: "c1", ToolName: "read",
				ToolArgs:   `{"path":"` + longPath + `"}`,
				ToolOutput: `{"content":"` + strings.Repeat("非常长的未换行工具输出/very/long/unbroken/path/", 12) + `","path":"` + longPath + `"}`,
			}}
			a.windowWidth = size.width
			tr := ui.NewTester(a.view, size.width, size.height)
			tr.SetDark(dark)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir,
				fmt.Sprintf("headless-%s-chat-tool-activity-%dx%d.png", theme, size.width, size.height)), tr.Image())
		}
	}
}
