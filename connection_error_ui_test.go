package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
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
	if !tr.HasText(locales.In("en").HistoryReadFailed()) || tr.HasText(raw) {
		t.Fatalf("unknown load summary/disclosure missing: %v", tr.Texts())
	}
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText(raw) || ct.LoadErr != raw {
		t.Fatal("unknown load error was lost or rewritten")
	}
}

func TestMixedConnectionDiagnosticsStayIndependent(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.LoadErr = "connection_mismatch: session is bound elsewhere"
	ct.SendError = "send pipeline unavailable; inspect the task record"
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Connection unavailable or changed") || !tr.HasText(locales.In("en").RecoveryRequestFailed()) || tr.HasText(ct.SendError) {
		t.Fatalf("mixed known/unknown diagnostics were not both visible: %v", tr.Texts())
	}
	if tr.HasText(ct.LoadErr) {
		t.Fatal("known load diagnostic leaked before Details")
	}
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText(ct.LoadErr) || !tr.HasText(ct.SendError) {
		t.Fatal("independent details were lost")
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
			wantHint:   locales.In("en").ConnectionRecoverySetupHint(),
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
			wantHint:   locales.In("en").ConnectionRecoveryManagedHint(),
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

func TestRecoveryPresentationTaskFailureDisclosureAndSettings(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			a.workspaceOpen = false
			message := "upstream unavailable; API_KEY=never-show-this"
			raw := taskFailureDetail("model_error", &message)
			ct.StatusDetail = raw
			ct.Draft = "Keep this unsent text"
			tr := ui.NewTester(a.view, 1040, 900)
			tr.SetLocale(locale)
			tr.Frame()
			summary := "The model request failed."
			if locale == "zh-Hans" {
				summary = "模型请求失败。"
			}
			found := false
			for _, text := range tr.Texts() {
				found = found || strings.HasPrefix(text, summary)
			}
			if !found || tr.HasText(raw) {
				t.Fatalf("expected plain summary before disclosure: %v", tr.Texts())
			}
			msgs := locales.In(locale)
			if err := tr.Click(msgs.DetailsAction()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tr.HasText(raw) || ct.StatusDetail != raw || ct.Draft != "Keep this unsent text" || ct.SendState != sendIdle {
				t.Fatalf("disclosure changed the record/draft or hid its redacted detail: %v", tr.Texts())
			}
			if err := tr.Click(msgs.ConnectionSettingsAction()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !a.settingsOpen || a.settingsTab != settingsTabConnection || ct.StatusDetail != raw || ct.Draft != "Keep this unsent text" || ct.SendState != sendIdle {
				t.Fatal("recovery did not route to Connection settings without sending")
			}
		})
	}
}

func TestRecoveryPresentationLocalRetryClearsOnlyOwnedFeedback(t *testing.T) {
	a, ct := workspaceTestApp(t)
	root := t.TempDir()
	path := filepath.Join(root, "contacts.json")
	store, err := openContactStore(path, root)
	if err != nil {
		t.Fatal(err)
	}
	a.store, a.storePath, a.workDir = store, path, root
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	ct.Draft = "retained local edit"
	if err := a.persist(); err == nil {
		t.Fatal("expected controlled write failure")
	}
	config := testConnectionConfig(t)
	a.connectionSaved = &config
	a.connectionForm = newConnectionSettings(&config, root)
	a.connectionForm.Error, a.connectionForm.ErrorDetail = connStatusSavedStoreFailed, a.localSaveErr
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	tr := ui.NewTester(a.view, 1040, 900)
	tr.Frame()
	if err := tr.Click("Retry save"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	tr.Frame()
	if a.localSaveErr != "" || a.connectionForm.Error != "" || a.connectionForm.ErrorDetail != "" || a.connectionReloadBlocked() != "" {
		t.Fatalf("successful local retry left stale form feedback: local=%q form=%q detail=%q", a.localSaveErr, a.connectionForm.Error, a.connectionForm.ErrorDetail)
	}
	a.store = nil
	a.connectionForm.Error, a.connectionForm.ErrorDetail = connStatusSavedStoreFailed, "unavailable local data"
	tr.Frame()
	if a.connectionForm.Error != connStatusSavedStoreFailed {
		t.Fatal("unavailable store falsely cleared feedback")
	}
	a.connectionForm.Error, a.connectionForm.ErrorDetail = connErrorStart, "unrelated startup failure"
	tr.Frame()
	if a.connectionForm.Error != connErrorStart || a.connectionForm.ErrorDetail != "unrelated startup failure" || ct.Draft != "retained local edit" {
		t.Fatal("local recovery cleared unrelated state")
	}
}

func TestRecoveryPresentationRendersAndControls(t *testing.T) {
	dir := os.Getenv("NAGI_RECOVERY_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	type geometry struct {
		Case, Locale, Surface, State string
		Width, Height                int
		Controls                     map[string]ui.Rect
	}
	records := []geometry{}
	for _, size := range [][2]int{{720, 640}, {1040, 720}} {
		for _, locale := range []string{"en", "zh-Hans"} {
			for _, surface := range []string{"startup", "local-save", "form", "empty-result"} {
				t.Run(fmt.Sprintf("%s-%s-%d", surface, locale, size[0]), func(t *testing.T) {
					a, ct := workspaceTestApp(t)
					a.windowWidth = size[0]
					a.workspaceOpen = false
					dark := locale == "zh-Hans"
					a.appearanceMode = appearanceLight
					if dark {
						a.appearanceMode = appearanceDark
					}
					msgs := locales.In(locale)
					var tr *ui.Tester
					action := msgs.ConnectionSettingsAction()
					switch surface {
					case "startup":
						action = msgs.ConnectionRecoveryOpenSettings()
						a.runtimeStatus = runtimeStateFailed
						a.runtimeErrKind = runtimeNoticeHelperMissing
						a.runtimeErr = "Checked application runtime locations; component absent"
						a.runtimeStage = "Runtime helper"
						tr = ui.NewTester(a.view, size[0], size[1])
					case "local-save":
						a.rt = &runtimeClient{}
						a.runtimeStatus = runtimeStateReady
						a.localSaveErr = "Controlled local write error; original edits remain in this window."
						tr = ui.NewTester(a.view, size[0], size[1])
						action = msgs.SettingsRetrySave()
					case "form":
						config := testConnectionConfig(t)
						a.connectionSaved = &config
						a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
						a.connectionForm.Error, a.connectionForm.ErrorDetail = connErrorTestFailed, "Controlled provider request failure with a long diagnostic that wraps over ordinary text lines."
						a.openSettingsTab(settingsTabConnection, settingsSourceRail)
						tr = ui.NewTester(a.view, size[0], size[1])
						action = msgs.SettingsBackAccessible()
					case "empty-result":
						message := "Controlled request failure with original bounded details"
						execution := &issueExecution{ID: "recovery-geometry-run", SessionID: "recovery-geometry-session", TaskID: "recovery-geometry-task",
							Status: issueExecutionFailed, Detail: taskFailureDetail("model_error", &message)}
						issue := &productIssue{ID: "recovery-geometry-issue", ContactID: ct.ID, Goal: "Read the saved run failure", Status: issueStatusInProgress,
							WorkSessionID: execution.SessionID, Work: issueWorkSession{Loaded: true}, Executions: []*issueExecution{execution}}
						ct.Issues, ct.SelectedIssueID = []*productIssue{issue}, issue.ID
						a.rt = nil
						recoveryTestStore(t, a)
						a.openIssueResult(issue.ID, execution.ID)
						tr = ui.NewTester(a.view, size[0], size[1])
						action = msgs.SettingsTitle()
					}
					tr.SetLocale(locale)
					tr.SetDark(dark)
					tr.SetFocused(true)
					tr.Frame()
					for i := 0; i < 60 && !tr.Focused(msgs.DetailsAction()); i++ {
						tr.Key(0, ui.KeyTab)
						tr.Frame()
					}
					if !tr.Focused(msgs.DetailsAction()) {
						t.Fatal("Details is not keyboard reachable")
					}
					for _, state := range []string{"summary", "details"} {
						if state == "details" {
							if err := tr.Click(msgs.DetailsAction()); err != nil {
								t.Fatal(err)
							}
							tr.Frame()
						}
						label := msgs.DetailsAction()
						if state == "details" {
							label = msgs.HideDetailsAction()
						}
						record := geometry{Case: fmt.Sprintf("%s-%s-%d-%s", surface, locale, size[0], state), Locale: locale, Surface: surface, State: state, Width: size[0], Height: size[1], Controls: map[string]ui.Rect{}}
						for _, control := range []string{action, label} {
							r, ok := tr.Find(control)
							record.Controls[control] = r
							if !ok || r.W < 1 || r.H < 32 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
								t.Errorf("%s control %q is not visible/reachable: %+v/%v", record.Case, control, r, ok)
							}
						}
						records = append(records, record)
						writePreviewPNG(t, filepath.Join(dir, record.Case+".png"), tr.Image())
					}
				})
			}
		}
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "geometry.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestC8UnavailableDataRestoreEntryKeepsOriginalFile(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	path := guiStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{unreadable saved workbench")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	_, loadErr := newRuntimeApp()
	if loadErr == nil {
		t.Fatal("corrupt canonical file unexpectedly loaded")
	}
	a := newUnavailableApp(loadErr)
	if a.store != nil || a.storeInitAllowed || a.rt != nil || a.localSaveErr != "" || a.runtimeStage != startupStageLocalStore {
		t.Fatal("actual unavailable-store startup did not remain quiescent and fail-closed")
	}
	ct := a.selectedContact()
	before, diagnostic := recoveryRecordSnapshot(t, ct), a.runtimeErr
	defer func() {
		after, readErr := os.ReadFile(path)
		if readErr != nil || string(after) != string(original) || a.store != nil || a.storeInitAllowed || a.storePath != path || recoveryRecordSnapshot(t, ct) != before || a.runtimeErr != diagnostic || ct.Unavailable != loadErr.Error() {
			t.Error("restore entry replaced the unavailable original, initialized a store or changed fallback facts")
		}
		entries, readErr := os.ReadDir(filepath.Dir(path))
		if readErr != nil || len(entries) != 1 || entries[0].Name() != "contacts.json" {
			t.Error("restore navigation created replacement/configuration/archive files before confirmation")
		}
		if a.rt != nil || a.runtimeHelperChecked || a.connectionSaved != nil || a.connectionEffective != nil || a.connectionSaveInFlight || a.connectionApplying || a.connectionTesting || a.modelOpActive || a.submissionInFlight != 0 || a.shutdownStarted {
			t.Error("restore entry saved/applied/tested a connection or started execution/shutdown")
		}
	}()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText(locales.In("en").RecoveryLocalDataUnavailable()) {
		t.Fatal("actual unavailable-data notice was not rendered")
	}
	if err := tr.Click("Restore paused…"); err != nil {
		t.Fatalf("C8-R4/R6: unavailable-data notice has no Restore paused route: %v", err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAppearance {
		t.Fatal("Restore paused did not route to the existing General Settings recovery surface")
	}
}

func TestC8RecoveryOutcomePresentationUsesOutcomeWithError(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, size := range [][2]int{{720, 640}, {1040, 720}} {
			for _, outcome := range []string{"not-installed", "unconfirmed"} {
				t.Run(fmt.Sprintf("%s-%d-%s", locale, size[0], outcome), func(t *testing.T) {
					a := c8UnavailableApp(t)
					a.windowWidth = size[0]
					original := recoverySavedBytes(t, a.storePath)
					source := filepath.Join(t.TempDir(), "compatible.json")
					recoveryWriteState(t, source, recoveryProducedState(t))
					copy, err := inspectStoreRecoveryCopy(source)
					if err != nil {
						t.Fatal(err)
					}
					target, err := inspectStoreRecoveryTarget(a.storePath)
					if err != nil {
						t.Fatal(err)
					}
					result, err := restoreStoreRecoveryCopyWithRename(copy, target, func(stage, destination string) error {
						if outcome == "not-installed" {
							return os.ErrPermission
						}
						if err := os.Rename(stage, destination); err != nil {
							return err
						}
						return os.Remove(destination)
					})
					if err == nil || result.ArchivePath == "" || string(recoverySavedBytes(t, result.ArchivePath)) != string(original) {
						t.Fatal("controlled backend outcome did not retain its original archive/error")
					}
					// Presentation consumes a real backend result from its existing per-call fault boundary.
					s := a.ensureDataRecovery()
					s.result, s.problem = result, a.connectionSafeMessage(err)
					tr := ui.NewTester(a.view, size[0], size[1])
					tr.SetLocale(locale)
					tr.SetDark(locale == "zh-Hans")
					tr.Frame()
					msgs := locales.In(locale)
					summary := msgs.RecoveryNotInstalled()
					if outcome == "unconfirmed" {
						summary = msgs.RecoveryUnconfirmed()
					}
					if !tr.HasText(summary) || tr.HasText(s.problem) || tr.HasText(string(result.Outcome)) || a.store != nil || a.storeInitAllowed || a.rt != nil {
						t.Fatal("outcome/error was misrepresented or details leaked/activated app")
					}
					name := fmt.Sprintf("%s-%d-%s", locale, size[0], outcome)
					c8RenderEvidence(t, tr, name+"-summary", size[0], size[1], msgs.DetailsAction())
					if err := tr.Click(msgs.DetailsAction()); err != nil {
						t.Fatal(err)
					}
					tr.Frame()
					if !tr.HasText(safeDiagnostic(s.problem)) {
						t.Fatal("explicit Details lost original bounded diagnostic")
					}
					c8RenderEvidence(t, tr, name+"-details", size[0], size[1], msgs.HideDetailsAction())
					if outcome == "unconfirmed" {
						before := s.result
						a.chooseDataRecovery(true)
						a.confirmDataRecovery()
						if s.result != before || s.inFlight || s.choosing || !a.dataRecoveryBlocksChanges() {
							t.Fatal("unconfirmed result admitted blind retry or lost evidence")
						}
					}
				})
			}
		}
	}
}
