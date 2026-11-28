package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestPreviewStartupDiagnostics writes a small, opt-in set of headless images
// for the G1 startup/configuration UI. It is not a pixel assertion and does not
// run unless NAGI_PREVIEW_DIR points at a scratch directory; native Finder/IME
// evidence remains separate.
func TestPreviewStartupDiagnostics(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write G1 startup/configuration previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	preview := func(scene string) *appModel {
		a := &appModel{
			demo:            false,
			runtimeStatus:   runtimeStateFailed,
			runtimeStage:    "Connection configuration",
			runtimeErrKind:  runtimeNoticeConnectionRequired,
			runtimeHintKind: startupHintEnterConnection,
			storePath:       "/Users/you/Library/Application Support/Xuhu/contacts.json",
			workDir:         "/Users/you/Workspace/xuhu-run",
			contacts: []*contact{{
				ID: "xuhu", Name: "Xuhu", Role: "Assistant",
				Draft: "Keep this draft while I fix configuration.",
			}},
		}
		if scene == "configuration-details" {
			a.openConfiguration()
		}
		return a
	}

	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, scene := range []string{"config-failure", "configuration-details"} {
			for _, size := range []struct {
				width, height int
				label         string
			}{{1040, 720, "1040x720"}, {960, 640, "960x640"}} {
				a := preview(scene)
				tr := ui.NewTester(a.view, size.width, size.height)
				tr.SetDark(dark)
				tr.Frame()
				writePreviewPNG(t, filepath.Join(dir,
					fmt.Sprintf("headless-%s-g1-%s-%s.png", theme, scene, size.label)), tr.Image())
			}
		}
	}

	a := preview("configuration-details")
	tr := ui.NewTester(a.view, 1600, 900)
	tr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "headless-light-g1-configuration-details-1600x900.png"), tr.Image())
	writeConnectionRecoveryPreviews(t, dir)
}
