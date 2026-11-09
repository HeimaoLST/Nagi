package main

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
)

func TestKnownConnectionFailureIsConciseAndKeepsOriginalDetails(t *testing.T) {
	a, ct := workspaceTestApp(t)
	raw := "connection_mismatch: session was created for another connection; credential details omitted"
	ct.LoadErr = raw
	ct.SendError = raw
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Connection unavailable or changed") ||
		!tr.HasText("Connection settings") || !tr.HasText("Details") {
		t.Fatalf("known connection failure summary missing: %v", tr.Texts())
	}
	if tr.HasText(raw) {
		t.Fatal("raw connection diagnostic was shown without Details")
	}
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText(raw) || !tr.HasText("Copy error details") {
		t.Fatalf("connection detail was not accessible: %v", tr.Texts())
	}
	if err := tr.Click("Connection settings"); err != nil {
		t.Fatal(err)
	}
	if !a.settingsOpen || a.settingsTab != settingsTabConnection {
		t.Fatalf("connection settings entry = open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
}

func TestUnknownContactLoadErrorIsNotSwallowed(t *testing.T) {
	a, ct := workspaceTestApp(t)
	raw := "opaque load failure without a known connection code"
	ct.LoadErr = raw
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText(raw) {
		t.Fatalf("unknown contact load error was swallowed: %v", tr.Texts())
	}
}

func TestMixedConnectionDiagnosticsStayIndependent(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.LoadErr = "connection_mismatch: session is bound elsewhere"
	ct.SendError = "send pipeline unavailable; inspect the task record"
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Connection unavailable or changed") || !tr.HasText(ct.SendError) {
		t.Fatalf("mixed known/unknown diagnostics were not both visible: %v", tr.Texts())
	}
	if tr.HasText(ct.LoadErr) {
		t.Fatal("known load diagnostic leaked before Details")
	}
	if knownConnectionFailure("wrapped: connection_mismatch: still unknown") {
		t.Fatal("unknown diagnostic containing the code was treated as known")
	}
}

func TestConnectionRecoveryStatusAndOneClickSettings(t *testing.T) {
	config := testConnectionConfig(t)
	for _, tc := range []struct {
		name       string
		setup      func(*appModel, *contact)
		wantHeader string
		wantHint   string
		wantButton bool
	}{
		{
			name: "setup",
			setup: func(a *appModel, _ *contact) {
				a.runtimeStatus = runtimeStateFailed
				a.runtimeStage = "Connection configuration"
				a.ensureConnectionForm()
				a.connectionForm.HasSaved = false
			},
			wantHeader: "Setup needed",
			wantHint:   "Complete the connection settings to start the runtime.",
			wantButton: true,
		},
		{
			name: "saved awaiting apply",
			setup: func(a *appModel, _ *contact) {
				a.runtimeStatus = runtimeStateFailed
				a.connectionSaved = &config
				a.ensureConnectionForm()
			},
			wantHeader: "Saved — apply to connect",
			wantHint:   "The saved connection is waiting to be applied.",
			wantButton: true,
		},
		{
			name: "exited host connection",
			setup: func(a *appModel, _ *contact) {
				a.runtimeStatus = runtimeStateExited
				a.runtimeStage = "Runtime connection"
				a.connectionLegacy = true
				a.ensureConnectionForm()
			},
			wantHeader: "Not connected",
			wantHint:   "The runtime is not connected. Review the connection settings.",
			wantButton: true,
		},
		{
			name: "failed after applied connection",
			setup: func(a *appModel, _ *contact) {
				a.runtimeStatus = runtimeStateExited
				a.runtimeStage = "Runtime connection"
				a.connectionSaved = &config
				a.connectionEffective = &config
			},
			wantHeader: "Not connected",
			wantHint:   "The runtime is not connected. Review the connection settings.",
			wantButton: true,
		},
		{
			name: "managed saved connection did not start",
			setup: func(a *appModel, _ *contact) {
				a.runtimeStatus = runtimeStateExited
				a.runtimeStage = "Connection configuration"
				a.connectionSaved = &config
				a.connectionEffective = &config
			},
			wantHeader: "Not connected",
			wantHint:   "Fix Connection settings, then save and apply.",
			wantButton: true,
		},
		{
			name: "starting with saved connection",
			setup: func(a *appModel, _ *contact) {
				a.rt = &runtimeClient{}
				a.runtimeStatus = runtimeStateStarting
				a.connectionSaved = &config
			},
			wantHeader: "Connecting…",
			wantHint:   "Wait for the runtime to connect.",
			wantButton: false,
		},
		{
			name: "applying",
			setup: func(a *appModel, _ *contact) {
				a.connectionApplying = true
				a.runtimeStatus = runtimeStateFailed
			},
			wantHeader: "Applying connection…",
			wantHint:   "Wait for the connection to become ready.",
			wantButton: false,
		},
		{
			name: "healthy old runtime with pending saved candidate",
			setup: func(a *appModel, _ *contact) {
				old, next := config, config
				old.Model = "old-model"
				next.Model = "pending-model"
				a.rt = &runtimeClient{}
				a.runtimeStatus = runtimeStateReady
				a.connectionEffective = &old
				a.connectionSaved = &next
				a.connectionForm = newConnectionSettings(&next, "")
			},
			wantHeader: "Ready",
			wantButton: false,
		},
		{
			name: "healthy legacy runtime without effective config",
			setup: func(a *appModel, _ *contact) {
				a.rt = &runtimeClient{}
				a.runtimeStatus = runtimeStateReady
				a.connectionLegacy = true
				a.connectionEffective = nil
			},
			wantHeader: "Ready",
			wantButton: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			tc.setup(a, ct)
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			if !tr.HasText(tc.wantHeader) {
				t.Fatalf("header = %v, want %q", tr.Texts(), tc.wantHeader)
			}
			if tc.wantHint != "" && !tr.HasText(tc.wantHint) {
				t.Fatalf("empty-draft connection reason %q missing: %v", tc.wantHint, tr.Texts())
			}
			if tc.wantButton {
				if !tr.HasText("Open connection settings") || !tr.HasText("Configuration details") {
					t.Fatalf("recovery entry missing: %v", tr.Texts())
				}
				if err := tr.Click("Open connection settings"); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if !a.settingsOpen || a.settingsTab != settingsTabConnection {
					t.Fatalf("recovery entry = open=%v tab=%q", a.settingsOpen, a.settingsTab)
				}
			} else if tr.HasText("Open connection settings") {
				t.Fatalf("non-actionable state exposed a recovery entry: %v", tr.Texts())
			}
		})
	}
}

func TestConnectionRecoveryPreservesWorkingAndAttentionStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status contactStatus
		send   sendState
		want   string
	}{
		{name: "working", status: statusWorking, want: "Working"},
		{name: "attention unknown", status: statusAttention, send: sendUnknown, want: "Needs attention"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			ct.Status = tc.status
			ct.SendState = tc.send
			a.runtimeStatus = runtimeStateExited
			a.connectionLegacy = true
			a.ensureConnectionForm()
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			if got := a.headerStatusLabel(ct, locales.In("en")); got != tc.want {
				t.Fatalf("header status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConnectionRecoveryRailSettingsDefaultsToConnection(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.runtimeStatus = runtimeStateFailed
	a.runtimeStage = "Connection configuration"
	a.ensureConnectionForm()
	a.connectionForm.HasSaved = false
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabConnection {
		t.Fatalf("rail Settings = open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
}

func TestConnectionRecoveryLabelsFollowLocale(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.runtimeStatus = runtimeStateFailed
	a.runtimeStage = "Connection configuration"
	a.ensureConnectionForm()
	a.connectionForm.HasSaved = false
	tr := ui.NewTester(a.view, 960, 640)
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("需要设置") || !tr.HasText("打开连接设置") {
		t.Fatalf("localized recovery labels missing: %v", tr.Texts())
	}
}

func writeConnectionRecoveryPreviews(t *testing.T, dir string) {
	t.Helper()
	for _, locale := range []string{"en", "zh-Hans"} {
		a, _ := workspaceTestApp(t)
		a.runtimeStatus = runtimeStateFailed
		a.runtimeStage = "Connection configuration"
		a.ensureConnectionForm()
		a.connectionForm.HasSaved = false
		a.workspaceOpen = false
		a.windowWidth = 960
		tr := ui.NewTester(a.view, 960, 640)
		tr.SetLocale(locale)
		tr.Frame()
		writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("connection-recovery-%s-960x640.png", locale)), tr.Image())
	}
}

func TestConnectionRecoveryEmptyDraftKeepsHigherPriorityReason(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.runtimeStatus = runtimeStateFailed
	a.runtimeStage = "Connection configuration"
	a.ensureConnectionForm()
	a.connectionForm.HasSaved = false
	ct.Archived = true
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("This contact is archived. Restore it before sending a message.") {
		t.Fatalf("archived reason missing from composer: %v", tr.Texts())
	}
	ct.Archived = false
	a.shutdownStarted = true
	tr = ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("The app is shutting down.") {
		t.Fatalf("shutdown reason missing from composer: %v", tr.Texts())
	}
}
