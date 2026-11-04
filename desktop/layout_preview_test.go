package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestPreviewAdjustableLayout is an opt-in headless matrix for the global
// workbench layout. It is not native drag or native window evidence.
func TestPreviewAdjustableLayout(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write adjustable-layout previews")
	}
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SAKI_PREVIEW_RESPONSIVE_ONLY") != "1" {
		scenes := []string{"closed", "open", "dragged"}
		for _, dark := range []bool{false, true} {
			theme := "light"
			if dark {
				theme = "dark"
			}
			for _, scene := range scenes {
				for _, size := range []struct{ width, height int }{{1040, 720}, {1380, 900}, {960, 640}, {720, 640}} {
					a, ct := workspaceTestApp(t)
					a.windowWidth = size.width
					a.workspaceOpen = scene != "closed"
					if scene == "dragged" && size.width >= 1040 {
						a.leftPaneWidth = defaultLeftPaneWidth
						a.rightPaneWidth = maxRightPaneWidth - 82
					}
					ct.Status = statusWorking
					ct.ActiveTaskID, ct.LastTaskID = "task-layout", "task-layout"
					w := a.ensureWork(ct, "task-layout", "message-layout")
					w.State = workRunning
					w.Prompt = "Layout preview"
					tr := ui.NewTester(a.view, size.width, size.height)
					tr.SetDark(dark)
					tr.Frame()
					writePreviewPNG(t, filepath.Join(dir,
						fmt.Sprintf("headless-%s-layout-%s-%dx%d.png", theme, scene, size.width, size.height)), tr.Image())
				}
			}
		}
	}
	writeResponsiveBoundaryPreviews(t, dir)
}

func writeResponsiveBoundaryPreviews(t *testing.T, dir string) {
	type scene struct {
		name, locale       string
		width, left, right float32
		workspaceOpen      bool
		dark               bool
		longName           string
	}
	scenes := []scene{
		{name: "960-default-en", locale: "en", width: 960, left: 200, right: 340, workspaceOpen: true, longName: "Long English contact name for responsive workbench review"},
		{name: "960-default-en-dark", locale: "en", width: 960, left: 200, right: 340, workspaceOpen: true, dark: true, longName: "Long English contact name for responsive workbench review"},
		{name: "960-max-zh", locale: "zh-Hans", width: 960, left: 280, right: 440, workspaceOpen: true, longName: "这是一个用于响应式工作台检查的较长中文联系人名称"},
		{name: "960-max-zh-dark", locale: "zh-Hans", width: 960, left: 280, right: 440, workspaceOpen: true, dark: true, longName: "这是一个用于响应式工作台检查的较长中文联系人名称"},
		{name: "1180-max-pseudo", locale: "en-XA", width: 1180, left: 280, right: 440, workspaceOpen: true, longName: "Expanded pseudo contact name for workbench review"},
		{name: "1180-max-pseudo-dark", locale: "en-XA", width: 1180, left: 280, right: 440, workspaceOpen: true, dark: true, longName: "Expanded pseudo contact name for workbench review"},
		{name: "899-zh-page", locale: "zh-Hans", width: 899, left: 280, right: 440, workspaceOpen: true, longName: "这是一个用于窄页面返回检查的较长中文联系人名称"},
		{name: "899-zh-page-dark", locale: "zh-Hans", width: 899, left: 280, right: 440, workspaceOpen: true, dark: true, longName: "这是一个用于窄页面返回检查的较长中文联系人名称"},
	}
	for _, sc := range scenes {
		a, ct := workspaceTestApp(t)
		a.windowWidth = int(sc.width)
		a.leftPaneWidth = sc.left
		a.rightPaneWidth = sc.right
		a.workspaceOpen = sc.workspaceOpen
		ct.Name = sc.longName
		tr := ui.NewTester(a.view, int(sc.width), 640)
		tr.SetLocale(sc.locale)
		tr.SetDark(sc.dark)
		tr.Frame()
		theme := "light"
		if sc.dark {
			theme = "dark"
		}
		writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("headless-responsive-%s-%dx640.png", sc.name, int(sc.width))), tr.Image())
		t.Logf("preview scene=%s base=12c3382fcf5e149c10183129f41d805c1eebbb4d source=working-tree theme=%s locale=%s width=%.0f height=640", sc.name, theme, sc.locale, sc.width)
	}
}
