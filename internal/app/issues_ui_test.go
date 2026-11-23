package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestUnconfiguredLaunchExposesConfigurationDetails(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.pi != nil {
		t.Fatal("unconfigured first launch unexpectedly started a runtime")
	}
	if a.runtimeStatus == runtimeStateReady {
		t.Fatal("unconfigured first launch unexpectedly reported a ready runtime")
	}
	a.windowWidth = 1040
	render := func(dark bool) *ui.Tester {
		tr := ui.NewTester(a.view, 1040, 720)
		tr.SetDark(dark)
		tr.Frame()
		return tr
	}
	previewDir := os.Getenv("NAGI_PREVIEW_DIR")
	if previewDir != "" {
		if err := os.MkdirAll(previewDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, dark := range []bool{false, true} {
		tr := render(dark)
		if !a.settingsOpen || a.settingsTab != settingsTabConnection || !tr.HasText(locales.In("en").SettingsConnection()) {
			t.Fatalf("unconfigured first launch did not open connection settings: %v", tr.Texts())
		}
		if previewDir != "" {
			theme := "light"
			if dark {
				theme = "dark"
			}
			writePreviewPNG(t, filepath.Join(previewDir, "capacity-unconfigured-"+theme+"-1040x720.png"), tr.Image())
		}
	}

	a.closeSettings()
	startupError := a.runtimeErrorDisplay(locales.In("en"))
	if startupError == "" {
		t.Fatal("unconfigured first launch lost its real startup error")
	}
	if !strings.Contains(startupError, "Connection settings are required") {
		t.Fatalf("unconfigured first launch error = %q", startupError)
	}
	for _, dark := range []bool{false, true} {
		tr := render(dark)
		if !tr.HasText("Setup needed") || !tr.HasText("Open connection settings") || !tr.HasText("Configuration details") {
			t.Fatalf("unconfigured composer lost recovery controls: %v", tr.Texts())
		}
		if previewDir != "" {
			theme := "light"
			if dark {
				theme = "dark"
			}
			writePreviewPNG(t, filepath.Join(previewDir, "capacity-unconfigured-composer-"+theme+"-1040x720.png"), tr.Image())
		}
	}
	tr := render(false)
	if err := tr.Click("Configuration details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAdvanced || !tr.HasText("Runtime & diagnostics") || !tr.HasText(startupError) {
		t.Fatalf("Configuration details did not open raw Advanced diagnostics: open=%v tab=%q texts=%v", a.settingsOpen, a.settingsTab, tr.Texts())
	}
}
