package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func openConnectionHarness(t *testing.T) *guiHarness {
	t.Helper()
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	h := &guiHarness{app: a, pump: newUIPump(t, a)}
	t.Cleanup(func() { h.shutdown(t) })
	h.pump.do(func() { a.startRuntime() })
	return h
}

func clickSettingsAction(tr *ui.Tester, action string) error {
	for i := 0; i < 12; i++ {
		if r, ok := tr.Find(action); ok && r.W > 0 && r.H > 0 && r.Y >= 0 && r.Y+r.H <= 720 {
			return tr.Click(action)
		}
		tr.Scroll(520, 360, 0, 180)
		tr.Frame()
	}
	return fmt.Errorf("connection action %q was not reachable by scrolling", action)
}

func saveAppConnection(t *testing.T, h *guiHarness, config connectionConfig) {
	t.Helper()
	h.pump.do(func() {
		h.app.openConfiguration()
		h.app.connectionForm.Draft = newConnectionSettings(&config, "").Draft
		h.app.saveConnection(config)
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.connectionForm.Error != "" {
			t.Error("configuration save failed: " + h.app.connectionForm.Error)
		}
	})
}

func applyAppConnection(t *testing.T, h *guiHarness) {
	t.Helper()
	h.pump.do(func() { h.app.applyConnection() })
	h.pump.wait(func() bool {
		return !h.app.connectionForm.Busy && h.app.runtimeStatus == runtimeStateReady && h.app.runtimeCapabilitiesLoaded
	})
	h.pump.do(func() {
		if h.app.connectionForm.Error != "" {
			t.Error("configuration apply failed: " + h.app.connectionForm.Error)
		}
	})
}

func TestIssueToolStartupFailureVisibleAndApplyRecovery(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	root := t.TempDir()
	isolateConnectionApp(t, root, binary)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", filepath.Join(root, "missing-helper"))
	config := testConnectionConfig(t)
	if err := saveConnectionConfig(connectionConfigPath(guiStorePath()), config); err != nil {
		t.Error(err)
		return
	}
	h := openConnectionHarness(t)
	h.pump.wait(func() bool { return h.app.runtimeStatus == runtimeStateReady && h.app.runtimeCapabilitiesLoaded })
	var initialRT *runtimeClient
	var initialError string
	h.pump.do(func() {
		a := h.app
		initialRT, initialError = a.rt, a.issueToolErr
		if initialError == "" || a.issueToolEnabled() || a.issueToolLaunchSource() != nil {
			t.Error("missing helper did not disable issue collaboration")
		}
		a.selectedContact().Draft = "preserve ordinary chat draft"
		for _, locale := range []string{"en", "zh-Hans"} {
			for _, width := range []int{720, 1040} {
				for _, dark := range []bool{false, true} {
					a.windowWidth = width
					tr := ui.NewTester(a.view, width, 640)
					tr.SetLocale(locale)
					tr.SetDark(dark)
					tr.Frame()
					msgs := locales.In(locale)
					if !tr.HasText(msgs.IssueCollaborationRepairHint()) || tr.HasText(initialError) {
						t.Errorf("%s: real initialization failure is not visible", locale)
					}
					if r, ok := tr.Find(msgs.ConfigurationDetails()); !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(width) || r.Y+r.H > 640 {
						t.Errorf("%s width=%d diagnostic action is not visible: %+v", locale, width, r)
					}
					if dir := os.Getenv("SAKI_PREVIEW_DIR"); dir != "" {
						writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("issue-collaboration-unavailable-%s-%d-dark-%v.png", locale, width, dark)), tr.Image())
					}
					if err := tr.Click(msgs.DetailsAction()); err != nil {
						t.Error(err)
						return
					}
					tr.Frame()
					if !tr.HasText(initialError) || a.issueToolErr != initialError || a.issueToolEnabled() || a.issueToolLaunchSource() != nil || a.rt != initialRT {
						t.Error("coordination disclosure lost the original failure or retried initialization")
						return
					}
					if err := tr.Click(msgs.HideDetailsAction()); err != nil {
						t.Error(err)
						return
					}
					tr.Frame()
				}
			}
		}
		a.windowWidth = 1040
		tr := ui.NewTester(a.view, 1040, 720)
		tr.Frame()
		msgs := locales.In("en")
		if !tr.HasText(msgs.IssueCollaborationRepairHint()) || tr.HasText(initialError) {
			t.Error("real initialization failure is not visible")
		}
		if err := tr.Click(msgs.ConfigurationDetails()); err != nil {
			t.Error(err)
		}
		tr.Frame()
		if a.settingsTab != settingsTabAdvanced || !tr.HasText(initialError) || !tr.HasText(msgs.RuntimeStatusLine(msgs.RuntimeStatusReady())) {
			t.Errorf("diagnostics did not distinguish base Ready from issue failure: %v", tr.Texts())
		}
		if err := clickSettingsAction(tr, msgs.SettingsOpenConnection()); err != nil {
			t.Error(err)
		}
		tr.Frame()
		if a.settingsTab != settingsTabConnection || a.rt != initialRT || a.issueToolErr != initialError || a.issueToolServer != nil {
			t.Error("diagnostic navigation retried initialization or changed Runtime")
		}
	})
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	t.Setenv("SAKI_RUNTIME_BIN", filepath.Join(root, "missing-runtime"))
	h.pump.do(func() { h.app.applyConnection() })
	h.pump.wait(func() bool { return !h.app.connectionApplying })
	var recoveredSocket string
	h.pump.do(func() {
		a := h.app
		recoveredSocket = a.issueToolEndpoint
		if a.issueToolServer == nil || recoveredSocket == "" || a.issueToolErr != initialError || a.issueToolEnabled() || a.rt != initialRT || a.runtimeStatus != runtimeStateReady {
			t.Error("failed Apply falsely recovered collaboration or replaced the working Runtime")
		}
	})
	t.Setenv("SAKI_RUNTIME_BIN", binary)
	applyAppConnection(t, h)
	h.pump.do(func() {
		a := h.app
		if !a.issueToolEnabled() || a.issueToolErr != "" || a.issueToolEndpoint != recoveredSocket || a.rt == initialRT {
			t.Error("successful Apply did not install the existing issue service")
		}
		a.closeSettings()
		tr := ui.NewTester(a.view, 1040, 720)
		tr.Frame()
		if tr.HasText(locales.In("en").IssueCollaborationRepairHint()) || tr.HasText(initialError) || a.selectedContact().Draft != "preserve ordinary chat draft" {
			t.Error("successful Apply left a stale warning or changed the draft")
		}
	})
	data, err := os.ReadFile(h.app.connectionLaunch.HostConfigPath)
	if err != nil {
		t.Error(err)
		return
	}
	var host struct {
		ToolSources []runtimeToolSource `json:"tool_sources"`
	}
	if err := json.Unmarshal(data, &host); err != nil || len(host.ToolSources) != 1 || host.ToolSources[0].ID != issueToolSourceID {
		t.Fatalf("recovered host source: %+v / %v", host, err)
	}
	h.shutdown(t)
	if _, err := os.Lstat(recoveredSocket); !os.IsNotExist(err) {
		t.Fatalf("recovered app socket remained after shutdown: %v", err)
	}
}

func sendConnectionConversation(t *testing.T, h *guiHarness, text, result string) string {
	t.Helper()
	accepted := false
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = text
		accepted = h.app.send(ct, text)
	})
	if !accepted {
		t.Fatal("conversation was not admitted")
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && !ct.HistoryDirty && containsText(contactMessageTexts(ct), result)
	})
	var id string
	h.pump.do(func() { id = h.app.selectedContact().SessionID })
	return id
}

func TestConnectionGUIFirstSaveApplyTestSendAndRestart(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	server := httptest.NewServer(provider.handler(t, "first-use"))
	t.Cleanup(server.Close)
	root := t.TempDir()
	isolateConnectionApp(t, root, binary)
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	config.Endpoint, config.Model = server.URL, "first-model"
	var clickErr error
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&config, "").Draft
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.Frame()
		logSave := func(phase string) {
			r, ok := tr.Find("Save configuration")
			t.Logf("Save bounds %s: rect=%+v found=%t scrollY=%g saved=%t busy=%t dirty=%t editOpen=%v provider=%d", phase, r, ok, h.app.settingsScroll.Y, h.app.connectionForm.HasSaved, h.app.connectionForm.Busy, h.app.connectionForm.Dirty(), h.app.connectionForm.editOpen, provider.count())
		}
		logSave("initial")
		for step := 0; step < 12; step++ {
			if r, ok := tr.Find("Save configuration"); ok && r.W > 0 && r.H >= 32 && r.X >= 0 && r.X+r.W <= 1040 && r.Y >= 0 && r.Y+r.H <= 720 {
				break
			}
			tr.Scroll(520, 360, 0, 180)
			tr.Frame()
		}
		logSave("reached before activation")
		if r, ok := tr.Find("Save configuration"); !ok || r.W <= 0 || r.H < 32 || r.X < 0 || r.X+r.W > 1040 || r.Y < 0 || r.Y+r.H > 720 {
			t.Error("Save configuration does not provide a fully reachable minimum click target before activation")
			return
		}
		clickErr = clickSettingsAction(tr, "Save configuration")
		logSave("after activation before explicit frame")
		if r, ok := tr.Find("Save configuration"); !ok || r.H < 32 {
			t.Error("Save configuration does not provide the minimum click target")
		}
		tr.Frame()
		logSave("after explicit frame")
	})
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	h.pump.wait(func() bool { return h.app.connectionForm.HasSaved && !h.app.connectionForm.Busy })
	if provider.count() != 0 {
		t.Fatal("the Save button called the model")
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if ct.Workspace != "" {
			t.Errorf("fresh contact saved a directory override: %q; want inherited", ct.Workspace)
		}
		if got := h.app.effectiveContactWorkspace(ct); got != config.WorkingDirectory {
			t.Errorf("fresh contact effective directory = %q; want %q", got, config.WorkingDirectory)
		}
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.Frame()
		clickErr = clickSettingsAction(tr, "Apply saved")
		tr.Frame()
	})
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	h.pump.wait(func() bool {
		return h.app.runtimeStatus == runtimeStateReady && h.app.runtimeCapabilitiesLoaded && !h.app.connectionForm.Busy
	})
	h.pump.do(func() {
		running, generation := h.app.rt, h.app.connectionGeneration
		ct := h.app.selectedContact()
		ct.Draft = "conversation draft survives settings"
		tr := ui.NewTester(h.app.view, 1040, 720)
		for _, action := range []string{"← Back to workbench", "Settings", "Connection & model"} {
			if err := tr.Click(action); err != nil {
				t.Error(err)
				return
			}
			tr.Frame()
		}
		if !tr.HasText("Current: "+config.Model) || tr.HasText("awaiting Apply") || tr.HasText("Saved. Apply saved") || h.app.connectionForm.Dirty() {
			t.Error("reopening the applied connection showed pending or unsaved settings")
		}
		if h.app.rt != running || h.app.connectionGeneration != generation || h.app.selectedContact() != ct || ct.Draft != "conversation draft survives settings" {
			t.Error("settings roundtrip changed the runtime, contact or conversation draft")
		}
	})
	if provider.count() != 0 {
		t.Fatal("Apply or reopening settings performed a model test without an explicit request")
	}
	beforeTest, err := os.ReadFile(guiStorePath())
	if err != nil {
		t.Fatal(err)
	}
	h.pump.do(func() {
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.Frame()
		clickErr = clickSettingsAction(tr, "Test connection and model")
		tr.Frame()
	})
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	if provider.count() != 1 {
		t.Fatal("the actual Test button did not run exactly one fixture request")
	}
	afterTest, err := os.ReadFile(guiStorePath())
	if err != nil || string(beforeTest) != string(afterTest) {
		t.Fatal("the connection test changed contacts or their work history")
	}
	oldID := sendConnectionConversation(t, h, "first real conversation", "first-use answer")
	var actual runtimeConnectionConfig
	var client *runtimeClient
	h.pump.do(func() { client = h.app.rt })
	actual, err = client.connectionConfig(oldID)
	wantDir, pathErr := filepath.EvalSymlinks(config.WorkingDirectory)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if err != nil || actual.EffectiveConfig.ConnectionBinding.State != "bound" || actual.EffectiveConfig.WorkingDirectory != wantDir || actual.EffectiveConfig.Model != config.Model {
		t.Fatal("first GUI request did not use the saved connection/model/cwd with a bound identity")
	}
	h.pump.do(func() {
		h.app.selectedContact().Draft = "a draft across restart"
		h.app.persist()
	})
	h.shutdown(t)
	t.Setenv("API_KEY", "irrelevant-fixture-key")
	t.Setenv("SAKI_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("SAKI_MODEL", "wrong-model")
	t.Setenv("SAKI_GUI_WORKDIR", root)
	restarted := openConnectionHarness(t)
	restarted.pump.wait(func() bool {
		ct := restarted.app.selectedContact()
		return restarted.app.runtimeStatus == runtimeStateReady && ct.Loaded && len(ct.Messages) > 0
	})
	restarted.pump.do(func() {
		ct := restarted.app.selectedContact()
		if ct.SessionID != oldID || ct.Draft != "a draft across restart" || ct.SessionModel != config.Model {
			t.Error("restart did not retain the conversation, draft and effective model")
		}
	})
	if provider.count() != 2 {
		t.Fatal("restart silently submitted a model request")
	}
}

func TestConnectionGUISaveDuringIssueAndReloadFailurePreservesHelper(t *testing.T) {
	binary := requireRuntimeBinary(t)
	hold := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	provider := &providerFixture{Hold: hold, Arrived: make(chan struct{})}
	server := httptest.NewServer(provider.handler(t, "working"))
	t.Cleanup(server.Close)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	t.Cleanup(release)
	config := testConnectionConfig(t)
	config.Endpoint = server.URL
	saveAppConnection(t, h, config)
	applyAppConnection(t, h)
	var original *runtimeClient
	var issueID string
	h.pump.do(func() {
		original = h.app.rt
		h.app.createIssue("Keep the original work", "Preserve these requirements")
		issueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(issueID) {
			t.Error("issue was not admitted")
		}
	})
	select {
	case <-provider.Arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("issue did not reach the provider")
	}
	var workSession string
	h.pump.do(func() { workSession = h.app.selectedContact().issueByID(issueID).WorkSessionID })
	actual, err := original.connectionConfig(workSession)
	if err != nil || actual.EffectiveConfig.ConnectionBinding.State != "bound" || actual.EffectiveConfig.Model != config.Model {
		t.Fatal("issue work did not use the configured connection")
	}
	h.pump.do(func() { h.app.addDefaultContact() })
	edited := config
	edited.Model, edited.MaxActiveTasks = "next-model", 3
	edited.WorkingDirectory = t.TempDir()
	saveAppConnection(t, h, edited)
	h.pump.do(func() {
		if h.app.workDir != config.WorkingDirectory {
			t.Error("Save changed the effective working directory before Apply")
		}
		if h.app.connectionReloadBlocked() == "" {
			t.Error("a non-selected contact's active issue did not block reload")
		}
		h.app.applyConnection()
		if h.app.rt != original {
			t.Error("Apply replaced the helper while issue work was running")
		}
	})
	select {
	case <-original.done:
		t.Fatal("saving/applying new defaults interrupted active work")
	default:
	}
	release()
	h.pump.wait(func() bool {
		issue := h.app.contacts[0].issueByID(issueID)
		return issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle && len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted
	})
	t.Setenv("SAKI_RUNTIME_BIN", filepath.Join(t.TempDir(), "missing-helper"))
	h.pump.do(func() {
		tr := ui.NewTester(h.app.view, 1040, 720)
		if err := clickSettingsAction(tr, "Apply saved"); err != nil {
			t.Error(err)
			return
		}
		if !h.app.connectionApplying || !h.app.connectionForm.Busy {
			t.Error("Apply remained disabled after the real issue reached its terminal state")
		}
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.rt != original || h.app.runtimeStatus != runtimeStateReady || h.app.connectionForm.Error == "" {
			t.Error("failed candidate start did not retain the old helper and show its failure")
		}
	})
	falseReady := filepath.Join(t.TempDir(), "ready-then-exit")
	if err := os.WriteFile(falseReady, []byte("#!/bin/sh\nprintf '%s\\n' '{\"version\":1,\"type\":\"ready\",\"api\":\"openai-responses\"}'\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAKI_RUNTIME_BIN", falseReady)
	h.pump.do(func() { h.app.applyConnection() })
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.rt != original || h.app.runtimeStatus != runtimeStateReady || h.app.connectionForm.Error == "" {
			t.Error("ready without a validated connection replaced the working helper")
		}
	})
	sendConnectionConversation(t, h, "old helper remains usable", "working answer")
	if !strings.Contains(provider.body(1), `"model":"`+config.Model+`"`) {
		t.Fatal("failed reload silently applied the pending default model")
	}
	t.Setenv("SAKI_RUNTIME_BIN", binary)
	applyAppConnection(t, h)
	h.pump.do(func() {
		if h.app.rt == original || h.app.runtimeMaxActiveTasks != 3 {
			t.Error("successful reload did not install the candidate and reread real capacity")
		}
		if issueRequirementsText(h.app.contacts[0].issueByID(issueID)) != "Preserve these requirements" {
			t.Error("reload changed issue requirements")
		}
	})
}

func TestConnectionGUIQuitWaitsForTestCleanup(t *testing.T) {
	binary := requireRuntimeBinary(t)
	hold := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	provider := &providerFixture{Hold: hold, Arrived: make(chan struct{})}
	server := httptest.NewServer(provider.handler(t, "probe-cancel"))
	t.Cleanup(server.Close)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	t.Cleanup(release)
	config := testConnectionConfig(t)
	config.Endpoint = server.URL
	saveAppConnection(t, h, config)
	applyAppConnection(t, h)
	h.pump.do(func() {
		h.app.createIssue("Do not start during quit", "Keep this pending")
		h.app.testConnection(config)
	})
	select {
	case <-provider.Arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("connection test did not reach the provider")
	}
	quit := make(chan struct{}, 1)
	h.pump.do(func() {
		h.app.requestQuit = func() {
			if h.app.connectionTesting {
				t.Error("quit was requested before the test helper finished cleanup")
			}
			quit <- struct{}{}
		}
		h.app.beginShutdown()
		if h.app.send(h.app.selectedContact(), "too late") || h.app.runIssue(h.app.selectedContact().SelectedIssueID) {
			t.Error("new conversation or issue work was admitted after quit began")
		}
		if h.app.quitReady {
			t.Error("quit did not wait for the in-flight test")
		}
	})
	select {
	case <-quit:
	case <-time.After(10 * time.Second):
		t.Fatal("connection test cancellation did not allow quit")
	}
	h.shutdown(t)
	release()
}

func TestConnectionGUIFailedTestCanBeCorrectedWithoutSaving(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	valid := provider.handler(t, "corrected")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer rejected-fixture-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("rejected-fixture-key is invalid"))
			return
		}
		valid.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	config.Endpoint, config.APIKey = server.URL, "rejected-fixture-key"
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&config, "").Draft
		h.app.testConnection(config)
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		form := h.app.connectionForm
		text := connectionErrorText(locales.In("en"), form.Error, form.ErrorDetail)
		if form.Error == "" || strings.Contains(text, config.APIKey) || strings.Contains(form.ErrorDetail, config.APIKey) {
			t.Error("failed Test did not show a key-free, correctable error")
		}
		if h.app.connectionForm.Draft.APIKey != config.APIKey {
			t.Error("failed Test discarded the editable draft")
		}
	})
	config.APIKey = "corrected-fixture-key"
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&config, "").Draft
		h.app.testConnection(config)
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.connectionForm.Error != "" || h.app.connectionForm.Status != connStatusTestCompleted {
			t.Error("corrected Test did not succeed")
		}
		if h.app.connectionSaved != nil || h.app.connectionEffective != nil || h.app.rt != nil {
			t.Error("Test saved or applied the draft without an explicit action")
		}
	})
	if _, err := os.Stat(guiStorePath()); !os.IsNotExist(err) {
		t.Fatal("Test created a contact store")
	}
}

func TestConnectionGUISaveFailureAndUnknownExecutionRecovery(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	server := httptest.NewServer(provider.handler(t, "recovery"))
	t.Cleanup(server.Close)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	config.Endpoint = server.URL
	saveAppConnection(t, h, config)
	applyAppConnection(t, h)
	path := filepath.Join(filepath.Dir(guiStorePath()), "connection.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	edited := config
	edited.Model = "unsaved-new-model"
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&edited, "").Draft
		h.app.saveConnection(edited)
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.connectionForm.Error == "" || h.app.connectionForm.Draft.Model != edited.Model {
			t.Error("save failure did not retain editable values and an error")
		}
	})
	after, err := os.ReadFile(path)
	if err != nil || string(original) != string(after) {
		t.Fatal("failed save replaced the existing source")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var client *runtimeClient
	var issueID string
	h.pump.do(func() {
		client = h.app.rt
		h.app.createIssue("Unconfirmed dispatch", "Preserve this before retrying")
		issue := h.app.selectedContact().selectedIssue()
		issueID = issue.ID
		issue.Executions = []*issueExecution{{ID: "unconfirmed-local-execution", Status: issueExecutionUnknown}}
		h.app.persist()
	})
	saveAppConnection(t, h, edited)
	h.pump.do(func() {
		if h.app.connectionReloadBlocked() == "" {
			t.Error("an unresolved persisted execution was ignored")
		}
		h.app.applyConnection()
		if h.app.rt != client {
			t.Error("unknown execution did not protect the current helper")
		}
		if !h.app.confirmIssueExecutionNotDispatched(issueID, "unconfirmed-local-execution") {
			t.Error("the existing explicit recovery action did not resolve the unknown dispatch")
		}
	})
	applyAppConnection(t, h)
	if provider.count() != 0 {
		t.Fatal("configuration recovery replayed an unconfirmed task")
	}
}

func TestConnectionGUIEndpointChangePreservesOldContextAndNewDefaults(t *testing.T) {
	binary := requireRuntimeBinary(t)
	providerA, providerB := &providerFixture{}, &providerFixture{}
	serverA := httptest.NewServer(providerA.handler(t, "original-context"))
	serverB := httptest.NewServer(providerB.handler(t, "new-context"))
	t.Cleanup(serverA.Close)
	t.Cleanup(serverB.Close)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	configA := testConnectionConfig(t)
	configA.Endpoint, configA.Model = serverA.URL, "model-A"
	saveAppConnection(t, h, configA)
	applyAppConnection(t, h)
	oldSession := sendConnectionConversation(t, h, "keep this original conversation", "original-context answer")
	var originalOverride string
	h.pump.do(func() {
		originalOverride = h.app.selectedContact().Workspace
		h.app.selectedContact().Draft = "keep this old-context draft"
		h.app.persist()
	})
	configB := configA
	configB.Endpoint, configB.Model, configB.WorkingDirectory = serverB.URL, "model-B", t.TempDir()
	saveAppConnection(t, h, configB)
	applyAppConnection(t, h)
	h.pump.wait(func() bool { return h.app.contacts[0].Unavailable != "" && len(h.app.contacts[0].Messages) > 0 })
	h.pump.do(func() {
		ct := h.app.contacts[0]
		if ct.SessionID != oldSession {
			t.Errorf("endpoint change replaced old conversation: %q; want %q", ct.SessionID, oldSession)
		}
		if ct.Workspace != originalOverride {
			t.Errorf("endpoint change replaced saved directory override: %q; want %q", ct.Workspace, originalOverride)
		}
		if ct.Draft != "keep this old-context draft" {
			t.Errorf("endpoint change replaced old conversation draft: %q", ct.Draft)
		}
		if ct.Loaded {
			t.Error("incompatible old conversation remained loaded")
		}
		if !containsText(contactMessageTexts(ct), "original-context answer") {
			t.Error("incompatible context lost its readable history")
		}
		h.app.addDefaultContact()
		if h.app.selectedContact().Workspace != configB.WorkingDirectory {
			t.Error("new contact did not use the applied default directory")
		}
	})
	newSession := sendConnectionConversation(t, h, "use new connection explicitly", "new-context answer")
	if oldSession == newSession || providerA.count() != 1 || providerB.count() != 1 || !strings.Contains(providerB.body(0), `"model":"model-B"`) {
		t.Fatal("new work did not use a new context on B without replaying A")
	}
}

func TestSettingsHelperDiagnosticsUseActualProbe(t *testing.T) {
	isolateConnectionApp(t, t.TempDir(), "")
	t.Setenv("PATH", "")
	h := openConnectionHarness(t)
	assertHelper := func(want string) {
		t.Helper()
		h.pump.do(func() {
			h.app.openAdvancedSettings()
			tr := ui.NewTester(h.app.view, 1040, 720)
			if !tr.HasText("Helper: " + want) {
				t.Errorf("helper diagnostics did not reflect %q", want)
			}
		})
	}
	assertHelper("not checked yet")
	config := testConnectionConfig(t)
	saveAppConnection(t, h, config)
	assertHelper("not started yet")
	h.pump.do(func() { h.app.applyConnection() })
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	assertHelper("not found")
	h.pump.do(func() {
		if h.app.rt != nil || !h.app.runtimeHelperChecked || h.app.connectionForm.Error == "" {
			t.Error("failed helper discovery did not retain its failure and probe fact")
		}
	})
}

func TestConnectionGUIShutdownWaitsForMetadataCallback(t *testing.T) {
	binary := requireRuntimeBinary(t)
	for _, tc := range []struct {
		name  string
		wait  func(*appModel) bool
		start func(*appModel)
	}{
		{
			name:  "capabilities",
			wait:  func(a *appModel) bool { return a.runtimeCapabilitiesLoaded },
			start: func(a *appModel) { a.retryCapabilities() },
		},
		{
			name:  "builtin tools",
			wait:  func(a *appModel) bool { return a.runtimeBuiltinToolsLoaded && !a.runtimeBuiltinToolsLoading },
			start: func(a *appModel) { a.retryBuiltinTools() },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateConnectionApp(t, t.TempDir(), binary)
			h := openConnectionHarness(t)
			config := testConnectionConfig(t)
			saveAppConnection(t, h, config)
			applyAppConnection(t, h)
			h.pump.wait(func() bool {
				return h.app.metadataInFlight == 0 &&
					(h.app.runtimeCapabilitiesLoaded || h.app.runtimeCapabilitiesErr != "") &&
					(h.app.runtimeBuiltinToolsLoaded || h.app.runtimeBuiltinToolsErr != "")
			})

			callbacks := make(chan func(), 8)
			quitCalls := make(chan struct{}, 4)
			var metadataCallback func()
			h.pump.do(func() {
				h.app.setUIHook(func(fn func()) { callbacks <- fn })
				h.app.requestQuit = func() { quitCalls <- struct{}{} }
				tc.start(h.app)
			})
			select {
			case metadataCallback = <-callbacks:
			case <-time.After(10 * time.Second):
				t.Fatal("metadata callback was not delivered")
			}
			var inFlight int
			h.pump.do(func() { inFlight = h.app.metadataInFlight })
			if inFlight != 1 {
				t.Fatalf("metadata in-flight = %d, want 1", inFlight)
			}
			h.pump.do(func() {
				h.app.beginShutdown()
				if h.app.shutdownDrainStarted || h.app.quitReady {
					t.Error("shutdown started before the admitted metadata callback completed")
				}
				h.app.retryCapabilities()
				h.app.retryBuiltinTools()
				if h.app.metadataInFlight != 1 {
					t.Errorf("metadata admission changed during shutdown: %d", h.app.metadataInFlight)
				}
			})
			h.pump.do(func() { metadataCallback() })
			var remaining int
			h.pump.do(func() { remaining = h.app.metadataInFlight })
			if remaining != 0 {
				t.Fatalf("metadata callback did not finish: inFlight=%d", remaining)
			}
			waitCallbacksForQuit(t, h, callbacks)
			if got := len(quitCalls); got != 1 {
				t.Fatalf("requestQuit calls = %d, want exactly 1", got)
			}
			h.closed = true
			h.pump.close()
		})
	}
}

func TestConnectionGUIStaleMetadataCallbackFinishesPendingShutdown(t *testing.T) {
	binary := requireRuntimeBinary(t)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	saveAppConnection(t, h, config)
	applyAppConnection(t, h)
	h.pump.wait(func() bool {
		return h.app.metadataInFlight == 0 && h.app.runtimeBuiltinToolsLoaded && !h.app.runtimeBuiltinToolsLoading
	})

	callbacks := make(chan func(), 8)
	quitCalls := make(chan struct{}, 4)
	var metadataCallback func()
	h.pump.do(func() {
		h.app.setUIHook(func(fn func()) { callbacks <- fn })
		h.app.requestQuit = func() { quitCalls <- struct{}{} }
		h.app.retryBuiltinTools()
	})
	select {
	case metadataCallback = <-callbacks:
	case <-time.After(10 * time.Second):
		t.Fatal("built-in tools callback was not delivered")
	}
	h.pump.do(func() { h.app.beginShutdown() })
	var drainStarted bool
	h.pump.do(func() { drainStarted = h.app.shutdownDrainStarted })
	if drainStarted {
		t.Fatal("shutdown started before the admitted stale metadata callback completed")
	}
	h.pump.do(func() {
		h.app.connectionGeneration++
		h.app.runtimeBuiltinTools = []runtimeBuiltinTool{{Name: "kept-newer"}}
		h.app.runtimeBuiltinToolsLoaded = true
		h.app.runtimeBuiltinToolsLoading = true
		h.app.runtimeBuiltinToolsErr = "newer generation"
	})
	h.pump.do(func() { metadataCallback() })
	var remaining int
	var kept string
	var loaded, loading bool
	var loadErr string
	h.pump.do(func() {
		remaining = h.app.metadataInFlight
		loaded, loading, loadErr = h.app.runtimeBuiltinToolsLoaded, h.app.runtimeBuiltinToolsLoading, h.app.runtimeBuiltinToolsErr
		if len(h.app.runtimeBuiltinTools) == 1 {
			kept = h.app.runtimeBuiltinTools[0].Name
		}
	})
	if remaining != 0 || kept != "kept-newer" || !loaded || !loading || loadErr != "newer generation" {
		t.Fatalf("stale callback changed newer generation: inFlight=%d tool=%q loaded=%v loading=%v err=%q", remaining, kept, loaded, loading, loadErr)
	}
	waitCallbacksForQuit(t, h, callbacks)
	if got := len(quitCalls); got != 1 {
		t.Fatalf("requestQuit calls = %d, want exactly 1", got)
	}
	h.closed = true
	h.pump.close()
}

func waitCallbacksForQuit(t *testing.T, h *guiHarness, callbacks <-chan func()) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var quitReady bool
		h.pump.do(func() { quitReady = h.app.quitReady })
		if quitReady {
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("shutdown did not become ready: quitReady=%v", quitReady)
		}
		select {
		case callback := <-callbacks:
			h.pump.do(func() { callback() })
		case <-time.After(remaining):
			t.Fatalf("shutdown callback was not delivered: quitReady=%v", quitReady)
		}
	}
}

func TestConnectionGUISaveAndApplyStartsValidatedRuntime(t *testing.T) {
	binary := requireRuntimeBinary(t)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&config, config.WorkingDirectory).Draft
		h.app.saveAndApplyConnection(config)
	})
	h.pump.wait(func() bool {
		return !h.app.connectionForm.Busy && !h.app.connectionApplying &&
			h.app.runtimeStatus == runtimeStateReady && h.app.connectionEffective != nil && h.app.rt != nil
	})
	var savedPreserved, effectivePreserved bool
	h.pump.do(func() {
		saved, effective := h.app.connectionSaved, h.app.connectionEffective
		savedPreserved = saved != nil && *saved == config
		effectivePreserved = effective != nil && *effective == config
	})
	if !savedPreserved || !effectivePreserved {
		t.Fatal("save-and-apply did not preserve the saved/effective connection")
	}
	h.shutdown(t)
}

func TestConnectionGUISaveAndApplyRechecksActivityBeforeFinalSaveCallback(t *testing.T) {
	binary := requireRuntimeBinary(t)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	base := testConnectionConfig(t)
	saveAppConnection(t, h, base)
	applyAppConnection(t, h)
	var oldRT *runtimeClient
	h.pump.do(func() { oldRT = h.app.rt })
	h.pump.wait(func() bool {
		return h.app.metadataInFlight == 0 && h.app.runtimeBuiltinToolsLoaded && !h.app.runtimeBuiltinToolsLoading
	})
	edited := base
	edited.Model = "edited-after-save"
	callbacks := make(chan func(), 16)
	var originalHook func(func())
	h.pump.do(func() {
		originalHook = h.app.getUIHook()
		h.app.setUIHook(func(fn func()) { callbacks <- fn })
		h.app.connectionForm.Draft = newConnectionSettings(&edited, "").Draft
		h.app.saveAndApplyConnection(edited)
	})
	t.Cleanup(func() {
		if originalHook != nil && !h.closed {
			h.pump.do(func() {
				h.app.setUIHook(originalHook)
				ct := h.app.selectedContact()
				ct.ActiveTaskID = ""
				ct.SendState = sendIdle
				h.app.modelOpActive = false
			})
		}
	})
	var busy bool
	h.pump.do(func() { busy = h.app.connectionForm.Busy })
	if !busy {
		t.Fatal("save did not enter in-flight state before activity was introduced")
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.ActiveTaskID = "active-during-save"
		ct.SendState = sendAccepted
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		var saved *connectionConfig
		h.pump.do(func() { saved = h.app.connectionSaved })
		if saved != nil && saved.Model == edited.Model {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("save final UI callback did not complete")
		}
		select {
		case callback := <-callbacks:
			h.pump.do(callback)
		case <-time.After(remaining):
			t.Fatal("save callback was not delivered")
		}
	}
	var applying bool
	var rt *runtimeClient
	var saved *connectionConfig
	var blocked string
	h.pump.do(func() {
		applying, rt, saved, blocked = h.app.connectionApplying, h.app.rt, h.app.connectionSaved, h.app.connectionForm.ReloadBlocked
	})
	if applying || rt != oldRT {
		t.Fatal("save-then-apply replaced the healthy runtime while new activity was present")
	}
	if blocked != connBlockedActiveWork {
		t.Fatalf("active-task guard did not block Apply: %q", blocked)
	}
	if saved == nil || saved.Model != edited.Model {
		t.Fatal("successful save was not retained for later Apply")
	}
	h.pump.do(func() {
		h.app.setUIHook(originalHook)
		ct := h.app.selectedContact()
		ct.ActiveTaskID = ""
		ct.SendState = sendIdle
		h.app.modelOpActive = false
	})
	h.shutdown(t)
}

func TestConnectionGUILanguageChangePreservesRequestsAndSession(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	server := httptest.NewServer(provider.handler(t, "locale-fixture"))
	t.Cleanup(server.Close)
	root := t.TempDir()
	isolateConnectionApp(t, root, binary)
	oldRead, oldApply := loadLanguageMode, applyAppLocale
	t.Cleanup(func() { loadLanguageMode, applyAppLocale = oldRead, oldApply })
	savedMode := "en"
	loadLanguageMode = func() string { return savedMode }
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	config.API, config.Endpoint, config.Model = connectionAPIResponses, server.URL, "locale-fixture-model"
	saveAppConnection(t, h, config)
	applyAppConnection(t, h)
	const instructions = "Keep these instructions byte-for-byte: 不翻译用户指令."
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Name, ct.Purpose, ct.Instructions = "Saki", "Original purpose 原始用途", instructions
	})
	if provider.count() != 0 {
		t.Fatal("configuration created an unsolicited model request")
	}
	sessionID := sendConnectionConversation(t, h, "First explicit input 第一条", "locale-fixture answer")
	var client *runtimeClient
	var generation int
	var ct *contact
	h.pump.do(func() {
		client, generation, ct = h.app.rt, h.app.connectionGeneration, h.app.selectedContact()
		ct.Draft = "Unsent draft 保留草稿"
	})
	var before json.RawMessage
	if err := client.call("session.config.get", map[string]any{"session_id": sessionID}, &before, runtimeControlTimeout); err != nil {
		t.Fatal(err)
	}
	storeBefore, err := os.ReadFile(guiStorePath())
	if err != nil {
		t.Fatal(err)
	}
	var tr *ui.Tester
	var applied []string
	openLanguage := func(locale string) bool {
		label := locales.In(locale).SettingsLanguage()
		for step := 0; step < 16 && !tr.Focused(label); step++ {
			tr.Key(0, ui.KeyTab)
			tr.Frame()
		}
		if !tr.Focused(label) {
			t.Error("Language selector was not keyboard reachable")
			return false
		}
		tr.Key(0, ui.KeySpace)
		tr.Frame()
		t.Logf("Language selector opened through focused %q control", label)
		return true
	}
	applyAppLocale = func(tag string) { savedMode = tag; applied = append(applied, tag); tr.SetLocale(tag) }
	h.pump.do(func() {
		h.app.openSettingsTab(settingsTabAppearance, settingsSourceRail)
		tr = ui.NewTester(h.app.view, 1040, 720)
		tr.SetLocale("en")
		tr.Frame()
		selector, selectorFound := tr.Find("Language")
		option, optionFound := tr.Find("简体中文")
		t.Logf("Language before disclosure: selector=%+v/%t option=%+v/%t", selector, selectorFound, option, optionFound)
		if !openLanguage("en") {
			return
		}
		if err := tr.Click("简体中文"); err != nil {
			t.Error(err)
			return
		}
		tr.Frame()
		if !tr.HasText("通用") || h.app.languageMode != "zh-Hans" {
			t.Error("the actual General language action did not update its live view")
		}
		if h.app.rt != client || h.app.connectionGeneration != generation || h.app.selectedContact() != ct || ct.SessionID != sessionID || ct.Draft != "Unsent draft 保留草稿" || ct.Instructions != instructions {
			t.Error("language change replaced Runtime, session, contact, draft or instructions")
		}
	})
	if t.Failed() {
		return
	}
	var after json.RawMessage
	if err := client.call("session.config.get", map[string]any{"session_id": sessionID}, &after, runtimeControlTimeout); err != nil {
		t.Fatal(err)
	}
	storeAfter, err := os.ReadFile(guiStorePath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !bytes.Equal(storeBefore, storeAfter) || provider.count() != 1 {
		t.Fatal("language change mutated configuration/storage or requested the model")
	}
	h.pump.do(func() { h.app.closeSettings() })
	if got := sendConnectionConversation(t, h, "Second explicit input 第二条", "locale-fixture answer"); got != sessionID {
		t.Fatalf("second explicit send used a new session: %s / %s", got, sessionID)
	}
	h.pump.do(func() {
		h.app.openSettingsTab(settingsTabAppearance, settingsSourceRail)
		tr.SetLocale("zh-Hans")
		tr.Frame()
		if !openLanguage("zh-Hans") {
			return
		}
		if err := tr.Click("English"); err != nil {
			t.Error(err)
			return
		}
		tr.Frame()
		if !tr.HasText("General") || h.app.languageMode != "en" {
			t.Error("English did not return in the same UI instance")
		}
	})
	if provider.count() != 2 || !slices.Equal(applied, []string{"zh-Hans", "en"}) {
		t.Fatalf("explicit sends/switches = requests %d, applied %v", provider.count(), applied)
	}
	invariant := func(body string) []byte {
		var request map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		var input []map[string]json.RawMessage
		if err := json.Unmarshal(request["input"], &input); err != nil {
			t.Fatal(err)
		}
		var system []json.RawMessage
		for _, item := range input {
			var role string
			_ = json.Unmarshal(item["role"], &role)
			if role == "system" || role == "developer" {
				data, err := json.Marshal(item)
				if err != nil {
					t.Fatal(err)
				}
				system = append(system, data)
			}
		}
		if len(system) == 0 || !strings.Contains(body, instructions) {
			t.Fatal("the real provider did not receive the original system/instructions")
		}
		data, err := json.Marshal(struct {
			Model, Tools json.RawMessage
			System       []json.RawMessage
		}{request["model"], request["tools"], system})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if !bytes.Equal(invariant(provider.body(0)), invariant(provider.body(1))) {
		t.Fatal("UI language changed provider model, tools, system/developer roles or prompt bytes")
	}
}

type c8HistoryWrite struct {
	Method     string `json:"method"`
	Bytes      int    `json:"bytes"`
	Written    int    `json:"written"`
	Completed  bool   `json:"completed"`
	ParseError string `json:"parse_error,omitempty"`
	WriteError string `json:"write_error,omitempty"`
}

type c8HistoryStdin struct {
	pipe   io.WriteCloser
	mu     sync.Mutex
	writes []c8HistoryWrite
	closes []string
}

func (s *c8HistoryStdin) Write(p []byte) (int, error) {
	var frame struct {
		Method string `json:"method"`
	}
	entry := c8HistoryWrite{Bytes: len(p)}
	if err := json.Unmarshal(p, &frame); err != nil {
		entry.ParseError = err.Error()
	} else if frame.Method == "" || len(p) == 0 || p[len(p)-1] != '\n' || bytes.Count(p, []byte{'\n'}) != 1 {
		entry.ParseError = "expected one complete JSONL request with a method"
	}
	entry.Method = frame.Method
	s.mu.Lock()
	index := len(s.writes)
	s.writes = append(s.writes, entry)
	s.mu.Unlock()
	n, err := s.pipe.Write(p)
	s.mu.Lock()
	s.writes[index].Written, s.writes[index].Completed = n, true
	if err != nil {
		s.writes[index].WriteError = err.Error()
	}
	s.mu.Unlock()
	return n, err
}

func (s *c8HistoryStdin) Close() error {
	err := s.pipe.Close()
	message := ""
	if err != nil {
		message = err.Error()
	}
	s.mu.Lock()
	s.closes = append(s.closes, message)
	s.mu.Unlock()
	return err
}

func (s *c8HistoryStdin) snapshot() ([]c8HistoryWrite, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]c8HistoryWrite(nil), s.writes...), append([]string(nil), s.closes...)
}

func TestC8GUIRecoveryCopyPausedRestoreKeepsRealHistoryWithoutReplay(t *testing.T) {
	binary := requireRuntimeBinary(t)
	ensureIssueToolTestHelper(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	isolateConnectionApp(t, root, binary)
	provider := &providerFixture{ToolFirst: false}
	server := httptest.NewServer(provider.handler(t, "c8-real-history"))
	t.Cleanup(server.Close)
	config := testConnectionConfig(t)
	config.Endpoint = server.URL
	if err := saveConnectionConfig(connectionConfigPath(guiStorePath()), config); err != nil {
		t.Fatal(err)
	}
	configBytes := recoverySavedBytes(t, connectionConfigPath(guiStorePath()))
	evidenceDir := os.Getenv("NAGI_DATA_RECOVERY_PREVIEW_DIR")
	saveEvidence := func(name string, data []byte) {
		t.Helper()
		if evidenceDir != "" {
			if err := os.MkdirAll(evidenceDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(evidenceDir, "c8-real-history-"+name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	startObserved := func() (*guiHarness, *runtimeClient, *c8HistoryStdin) {
		t.Helper()
		a, err := newRuntimeApp()
		if err != nil {
			t.Fatal(err)
		}
		h := &guiHarness{app: a, pump: newUIPump(t, a)}
		t.Cleanup(func() { h.shutdown(t) })
		var rt *runtimeClient
		var observer *c8HistoryStdin
		var attachErr error
		h.pump.do(func() {
			a.startRuntime()
			rt = a.rt
			if rt == nil {
				attachErr = fmt.Errorf("Runtime startup failed: %s", a.runtimeErr)
				return
			}
			rt.writeMu.Lock()
			rt.stateMu.Lock()
			// The occupied pump turn holds Ready; stateMu also excludes a
			// protocol-failure Close racing the stdin interface replacement.
			if rt.nextID != 0 || rt.failed != nil || rt.closed || rt.stdin == nil || a.shutdownStarted {
				attachErr = fmt.Errorf("client not pristine before attachment: nextID=%d failed=%v closed=%v", rt.nextID, rt.failed, rt.closed)
			} else {
				observer = &c8HistoryStdin{pipe: rt.stdin}
				rt.stdin = observer
			}
			rt.stateMu.Unlock()
			rt.writeMu.Unlock()
			if a.rt != rt || rt.cmd == nil || rt.cmd.Path != binary {
				attachErr = fmt.Errorf("startup replaced the direct pinned Runtime")
			}
		})
		if attachErr != nil {
			t.Fatal(attachErr)
		}
		h.pump.wait(func() bool {
			return a.rt == rt && a.runtimeStatus == runtimeStateReady && a.runtimeCapabilitiesLoaded && a.metadataInFlight == 0 && a.contactHistoryInFlight == 0
		})
		return h, rt, observer
	}
	assertWrites := func(observer *c8HistoryStdin, sends, closes int) []c8HistoryWrite {
		t.Helper()
		writes, closed := observer.snapshot()
		count := 0
		for _, entry := range writes {
			if entry.ParseError != "" || !entry.Completed || entry.Written != entry.Bytes || entry.WriteError != "" {
				t.Fatalf("invalid request observation: %+v", entry)
			}
			if entry.Method == "task.send" {
				count++
			}
		}
		if count != sends || len(closed) != closes {
			t.Fatalf("wire attempts/Close = %d/%d, want %d/%d: %+v", count, len(closed), sends, closes, writes)
		}
		return writes
	}
	h, firstRT, firstObserver := startObserved()
	var contactID, issueID, workSession, taskID, messageID, unknownID string
	var setupErr error
	h.pump.do(func() {
		ct := h.app.selectedContact()
		contactID = ct.ID
		if !h.app.createIssue("C8 real history goal", "Keep exact requirement and receipts") {
			setupErr = fmt.Errorf("create issue: %s", h.app.issueFormError)
			return
		}
		issueID = ct.SelectedIssueID
		if err := h.app.addIssueSupplement(ct, issueID, "C8 real submitted supplement 原文"); err != nil {
			setupErr = err
			return
		}
		if !h.app.runIssue(issueID) {
			setupErr = fmt.Errorf("run issue: %s", h.app.issueFormError)
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	h.pump.wait(func() bool {
		issue := h.app.contactByID(contactID).issueByID(issueID)
		return len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle && !issue.Work.HistoryDirty && h.app.submissionInFlight == 0 && h.app.contactHistoryInFlight == 0 && h.app.metadataInFlight == 0
	})
	h.pump.do(func() {
		issue := h.app.contactByID(contactID).issueByID(issueID)
		execution := issue.Executions[0]
		workSession, taskID, messageID = issue.WorkSessionID, execution.TaskID, execution.MessageID
		if workSession == "" || taskID == "" || messageID == "" || !containsText(contactMessageTexts(&contact{Messages: issue.Work.Messages}), "c8-real-history answer") {
			setupErr = fmt.Errorf("real task/history identity or result missing")
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	historyBefore, err := h.app.historyAllForSession(firstRT, workSession)
	if err != nil {
		t.Fatal(err)
	}
	h.pump.do(func() {
		issue := h.app.contactByID(contactID).issueByID(issueID)
		if !supplementHistoryMatches(issue.Executions[0], historyBefore.Records) {
			setupErr = fmt.Errorf("real history did not retain the exact submitted prompt/context")
			return
		}
		// These are saved uncertainty fixtures, not a claimed real lost receipt.
		ledger := issue.Supplements
		target := issueSupplementTargetFor(h.app.selectedContact(), issue)
		if _, err := ledger.Add(target, "C8 unknown supplement 原文", h.app.persist); err != nil {
			setupErr = err
			return
		}
		execution := &issueExecution{ID: newIssueExecutionID(), SessionID: workSession, Status: issueExecutionUnknown,
			Prompt: "C8 constructed uncertain snapshot", Detail: "C8 unknown association remains unconfirmed", RequirementsRevision: issue.RequirementsRevision}
		unknownID = execution.ID
		issue.Executions = append(issue.Executions, execution)
		batch, err := ledger.PrepareDispatch(target, execution, h.app.persist)
		if err != nil {
			setupErr = err
			return
		}
		if !ledger.Unknown(batch, "C8 unknown receipt raw detail", h.app.persist) {
			setupErr = fmt.Errorf("could not save unknown receipt fixture")
			return
		}
		if err := h.app.addIssueSupplement(h.app.selectedContact(), issueID, "C8 pending supplement must not replay"); err != nil {
			setupErr = err
			return
		}
		if !h.app.setIssueSupplementAuto(h.app.selectedContact(), issueID, true) || ledger.pendingCount() != 1 || !ledger.AutoContinue {
			setupErr = fmt.Errorf("compatible pending/unknown enabled ledger fixture missing")
			return
		}
		h.app.selectedContact().Draft = "C8 retained saved chat draft"
		setupErr = h.app.persist()
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	if provider.count() != 1 {
		t.Fatalf("setup made %d provider requests, want one explicit task", provider.count())
	}
	assertWrites(firstObserver, 1, 0)
	canonical := guiStorePath()
	saved := recoverySavedBytes(t, canonical)
	var expected storedState
	if err := json.Unmarshal(saved, &expected); err != nil {
		t.Fatal(err)
	}
	exportDir := filepath.Join(root, "copies")
	if err := os.Mkdir(exportDir, 0700); err != nil {
		t.Fatal(err)
	}
	msgs := locales.In("en")
	var tr *ui.Tester
	h.pump.do(func() {
		h.app.chooseFolder = func(opts mygo.OpenDialogOptions) ([]string, error) {
			if !opts.Directory || opts.Parent != h.app.win {
				return nil, fmt.Errorf("copy chooser was not the parented folder boundary")
			}
			return []string{exportDir}, nil
		}
		tr = ui.NewTester(h.app.view, 1040, 720)
		tr.SetLocale("en")
		tr.SetFocused(true)
		tr.Frame()
	})
	click := func(harness *guiHarness, tester *ui.Tester, action string) {
		t.Helper()
		var clickErr error
		harness.pump.do(func() {
			clickErr = clickSettingsAction(tester, action)
			tester.Frame()
		})
		if clickErr != nil {
			t.Fatal(clickErr)
		}
	}
	click(h, tr, "Settings")
	click(h, tr, "General")
	click(h, tr, msgs.SaveRecoveryCopyAction())
	h.pump.wait(func() bool {
		s := h.app.dataRecovery
		return s != nil && !s.choosing && !s.inFlight && s.notice != ""
	})
	var output string
	h.pump.do(func() {
		s := h.app.dataRecovery
		output = s.output
		if s.problem != "" || s.notice != "copy-saved" || output == "" || h.app.rt != firstRT {
			setupErr = fmt.Errorf("GUI export failed or replaced Runtime: %+v", s)
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	if !bytes.Equal(recoverySavedBytes(t, output), saved) || !bytes.Equal(recoverySavedBytes(t, canonical), saved) {
		t.Fatal("GUI export did not preserve exact saved bytes without force-save")
	}
	h.shutdown(t)
	firstWrites := assertWrites(firstObserver, 1, 1)
	_, firstCloses := firstObserver.snapshot()
	damaged := []byte("{C8 isolated corrupt canonical 原文\n")
	if err := os.WriteFile(canonical, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	_, loadErr := newRuntimeApp()
	if loadErr == nil {
		t.Fatal("corrupt canonical store did not reach the actual unavailable fallback")
	}
	fallback := newUnavailableApp(loadErr)
	f := &guiHarness{app: fallback, pump: newUIPump(t, fallback)}
	t.Cleanup(func() { f.shutdown(t) })
	quitCalls := 0
	f.pump.do(func() {
		if fallback.store != nil || fallback.storeInitAllowed || fallback.rt != nil || fallback.storePath != canonical || fallback.sessionDir != filepath.Join(root, "sessions") {
			setupErr = fmt.Errorf("unavailable fallback changed canonical paths or activated")
		}
		fallback.requestQuit = func() { quitCalls++ }
		fallback.chooseRecoveryFile = func(opts mygo.OpenDialogOptions) ([]string, error) {
			if opts.Directory || opts.Parent != fallback.win {
				return nil, fmt.Errorf("restore chooser was not the parented file boundary")
			}
			return []string{output}, nil
		}
		tr = ui.NewTester(fallback.view, 1040, 720)
		tr.SetLocale("en")
		tr.SetFocused(true)
		tr.Frame()
		if !tr.HasText(msgs.RecoveryLocalDataUnavailable()) {
			setupErr = fmt.Errorf("actual unavailable-data notice missing")
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	click(f, tr, msgs.RestorePausedAction())
	click(f, tr, msgs.RestorePausedAction())
	f.pump.wait(func() bool {
		s := fallback.dataRecovery
		return s != nil && !s.choosing && !s.inFlight && (s.copy != nil || s.problem != "")
	})
	f.pump.do(func() {
		tr.Frame()
		tr.Frame()
		if !fallback.dataRecoveryPreviewCurrent(fallback.dataRecovery) || !tr.HasText(msgs.RecoveryPreviewCounts(1, 1)) || !tr.HasText(msgs.RecoveryPauseWarning()) {
			setupErr = fmt.Errorf("GUI selected-copy preview missing: %v", tr.Texts())
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	if !bytes.Equal(recoverySavedBytes(t, canonical), damaged) {
		t.Fatal("preview installed before explicit confirmation")
	}
	click(f, tr, msgs.RecoveryConfirmAction())
	f.pump.wait(func() bool { return !fallback.dataRecovery.inFlight && fallback.dataRecovery.result.Outcome != "" })
	var result storeRecoveryResult
	f.pump.do(func() {
		s := fallback.dataRecovery
		result = s.result
		if result.Outcome != storeRecoveryVerifiedInstalled || s.problem != "" || fallback.store != nil || fallback.storeInitAllowed || fallback.rt != nil {
			setupErr = fmt.Errorf("GUI paused installation failed or activated fallback: result=%+v problem=%q", result, s.problem)
		}
		tr.Frame()
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	installedBytes := recoverySavedBytes(t, canonical)
	var installed storedState
	if err := json.Unmarshal(installedBytes, &installed); err != nil {
		t.Fatal(err)
	}
	for i := range expected.Issues {
		if expected.Issues[i].Supplements != nil {
			expected.Issues[i].Supplements.AutoContinue = false
		}
	}
	if result.OutputPath != canonical || !reflect.DeepEqual(installed, expected) || !bytes.Equal(recoverySavedBytes(t, output), saved) || !bytes.Equal(recoverySavedBytes(t, result.ArchivePath), damaged) {
		t.Fatal("GUI restore changed source/archive or known facts beyond the confirmed pause")
	}
	click(f, tr, msgs.RecoveryQuitAction())
	f.pump.wait(func() bool { return fallback.quitReady })
	f.pump.do(func() {
		if quitCalls != 1 || fallback.rt != nil || fallback.store != nil {
			setupErr = fmt.Errorf("GUI Quit did not keep fallback blocked through its ordinary drain")
		}
	})
	f.shutdown(t)
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	if !bytes.Equal(recoverySavedBytes(t, canonical), installedBytes) {
		t.Fatal("unavailable GUI shutdown overwrote installed data")
	}
	reopened, secondRT, secondObserver := startObserved()
	reopened.pump.wait(func() bool {
		ct := reopened.app.contactByID(contactID)
		if ct == nil {
			return false
		}
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.Loaded && !issue.Work.HistoryDirty && reopened.app.contactHistoryInFlight == 0 && reopened.app.metadataInFlight == 0 && reopened.app.submissionInFlight == 0
	})
	reopened.pump.do(func() {
		ct := reopened.app.contactByID(contactID)
		issue := ct.issueByID(issueID)
		if reopened.app.rt != secondRT || secondRT == firstRT || reopened.app.storePath != canonical || reopened.app.sessionDir != filepath.Join(root, "sessions") || reopened.app.connectionPath != connectionConfigPath(canonical) ||
			ct.ID != expected.Contacts[0].ID || ct.Draft != expected.Contacts[0].Draft || ct.SelectedIssueID != issueID || issue.WorkSessionID != workSession || len(issue.Executions) != 2 ||
			issue.Executions[0].TaskID != taskID || issue.Executions[0].MessageID != messageID || issue.Executions[0].Status != issueExecutionCompleted ||
			issue.Executions[1].ID != unknownID || issue.Executions[1].TaskID != "" || issue.Executions[1].MessageID != "" || issue.Executions[1].Status != issueExecutionUnknown || issue.Executions[1].Detail != "C8 unknown association remains unconfirmed" ||
			!reflect.DeepEqual(issue.Supplements, expected.Issues[0].Supplements) || issue.Supplements.AutoContinue || issue.Supplements.pendingCount() != 1 || issue.Work.ActiveTaskID != "" ||
			!containsText(contactMessageTexts(&contact{Messages: issue.Work.Messages}), "c8-real-history answer") {
			setupErr = fmt.Errorf("normal real-process reopen changed associations, raw unknown facts, paused receipts or history: %+v", issue)
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	historyAfter, err := reopened.app.historyAllForSession(secondRT, workSession)
	if err != nil {
		t.Fatal(err)
	}
	status, err := secondRT.sessionStatus(workSession)
	if err != nil || status.SessionID != workSession || status.ActiveTask != nil || historyAfter.SessionID != workSession || !reflect.DeepEqual(historyBefore.Records, historyAfter.Records) {
		t.Fatalf("actual Runtime history/session changed: status=%+v err=%v before=%+v after=%+v", status, err, historyBefore, historyAfter)
	}
	reopened.shutdown(t)
	secondWrites := assertWrites(secondObserver, 0, 1)
	_, secondCloses := secondObserver.snapshot()
	if provider.count() != 1 || !bytes.Equal(recoverySavedBytes(t, connectionConfigPath(canonical)), configBytes) {
		t.Fatal("export/restore/reopen/shutdown replayed provider work or changed the original connection")
	}
	beforeJSON, err := json.Marshal(historyBefore)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, err := json.Marshal(historyAfter)
	if err != nil {
		t.Fatal(err)
	}
	observations, err := json.MarshalIndent(map[string]any{
		"contact_id": contactID, "issue_id": issueID, "real_session_id": workSession, "real_task_id": taskID, "real_message_id": messageID,
		"constructed_unknown_execution_id": unknownID, "unknown_origin": "saved fixture, not a real lost receipt", "provider_requests": provider.count(),
		"initial_task_send_attempts": 1, "reopened_task_send_attempts": 0, "initial_writes": firstWrites, "reopened_writes": secondWrites,
		"initial_close_results": firstCloses, "reopened_close_results": secondCloses,
		"attachment": "each main client nextID=0/healthy checked under writeMu+stateMu before queued Ready", "store_path": canonical, "session_dir": filepath.Join(root, "sessions"),
		"copy_path": output, "archive_path": result.ArchivePath, "restore_outcome": result.Outcome, "quit_calls": quitCalls,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	saveEvidence("saved.json", saved)
	saveEvidence("export.json", recoverySavedBytes(t, output))
	saveEvidence("damaged.bin", damaged)
	saveEvidence("archive.bin", recoverySavedBytes(t, result.ArchivePath))
	saveEvidence("installed-before-startup.json", installedBytes)
	saveEvidence("history-before.json", beforeJSON)
	saveEvidence("history-after.json", afterJSON)
	saveEvidence("observations.json", observations)
	t.Logf("C8 real-process recovery observations: %s", observations)
}

func TestC8GUIRecoveryPausedRestoreReportsMissingOrMismatchedRuntimeHistoryWithoutReplay(t *testing.T) {
	for _, unrelated := range []bool{false, true} {
		name := "missing-runtime-data"
		if unrelated {
			name = "unrelated-runtime-dataset"
		}
		if !t.Run(name, func(t *testing.T) {
			binary := requireRuntimeBinary(t)
			ensureIssueToolTestHelper(t)
			ownedRoot := t.TempDir()
			if evidence := os.Getenv("NAGI_DATA_RECOVERY_PREVIEW_DIR"); evidence != "" {
				if err := os.MkdirAll(evidence, 0700); err != nil {
					t.Fatal(err)
				}
				var err error
				ownedRoot, err = os.MkdirTemp(evidence, "owned-"+name+"-")
				if err != nil {
					t.Fatal(err)
				}
			}
			root, err := filepath.EvalSymlinks(ownedRoot)
			if err != nil {
				t.Fatal(err)
			}
			isolateConnectionApp(t, root, binary)
			provider := &providerFixture{ToolFirst: false}
			server := httptest.NewServer(provider.handler(t, "c8-negative-source"))
			t.Cleanup(server.Close)
			config := testConnectionConfig(t)
			config.Endpoint = server.URL
			config.WorkingDirectory = root
			if err := saveConnectionConfig(connectionConfigPath(guiStorePath()), config); err != nil {
				t.Fatal(err)
			}
			configBytes := recoverySavedBytes(t, connectionConfigPath(guiStorePath()))
			evidenceDir := os.Getenv("NAGI_DATA_RECOVERY_PREVIEW_DIR")
			saveEvidence := func(name string, data []byte) {
				t.Helper()
				if evidenceDir != "" {
					if err := os.MkdirAll(evidenceDir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(evidenceDir, "c8-negative-"+t.Name()[strings.LastIndex(t.Name(), "/")+1:]+"-"+name), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			var observed []*c8HistoryStdin
			phase := "initial startup"
			facts := map[string]any{"owned_root": root}
			t.Cleanup(func() {
				var writes [][]c8HistoryWrite
				var closes [][]string
				for _, observer := range observed {
					w, c := observer.snapshot()
					writes = append(writes, w)
					closes = append(closes, c)
				}
				data, err := json.MarshalIndent(map[string]any{"phase": phase, "failed": t.Failed(), "main_client_writes": writes, "main_client_closes": closes, "source_provider_requests": provider.count(), "facts": facts}, "", "  ")
				if err != nil {
					t.Error(err)
					return
				}
				saveEvidence("lifecycle.json", data)
			})
			startObserved := func() (*guiHarness, *runtimeClient, *c8HistoryStdin) {
				t.Helper()
				a, err := newRuntimeApp()
				if err != nil {
					t.Fatal(err)
				}
				h := &guiHarness{app: a, pump: newUIPump(t, a)}
				t.Cleanup(func() { h.shutdown(t) })
				var rt *runtimeClient
				var observer *c8HistoryStdin
				var attachErr error
				h.pump.do(func() {
					a.startRuntime()
					rt = a.rt
					if rt == nil {
						attachErr = fmt.Errorf("Runtime startup failed: %s", a.runtimeErr)
						return
					}
					rt.writeMu.Lock()
					rt.stateMu.Lock()
					// The occupied pump turn holds Ready; stateMu also excludes a
					// protocol-failure Close racing the stdin interface replacement.
					if rt.nextID != 0 || rt.failed != nil || rt.closed || rt.stdin == nil || a.shutdownStarted {
						attachErr = fmt.Errorf("client not pristine before attachment: nextID=%d failed=%v closed=%v", rt.nextID, rt.failed, rt.closed)
					} else {
						observer = &c8HistoryStdin{pipe: rt.stdin}
						rt.stdin = observer
					}
					rt.stateMu.Unlock()
					rt.writeMu.Unlock()
					if a.rt != rt || rt.cmd == nil || rt.cmd.Path != binary {
						attachErr = fmt.Errorf("startup replaced the direct pinned Runtime")
					}
				})
				if observer != nil {
					observed = append(observed, observer)
				}
				if attachErr != nil {
					t.Fatal(attachErr)
				}
				h.pump.wait(func() bool {
					return a.rt == rt && a.runtimeStatus == runtimeStateReady && a.runtimeCapabilitiesLoaded && a.metadataInFlight == 0 && a.contactHistoryInFlight == 0
				})
				return h, rt, observer
			}
			assertWrites := func(observer *c8HistoryStdin, sends, creates, closes int) []c8HistoryWrite {
				t.Helper()
				writes, closed := observer.snapshot()
				count, createCount := 0, 0
				for _, entry := range writes {
					if entry.ParseError != "" || !entry.Completed || entry.Written != entry.Bytes || entry.WriteError != "" {
						t.Fatalf("invalid request observation: %+v", entry)
					}
					if entry.Method == "task.send" {
						count++
					}
					if entry.Method == "session.create" {
						createCount++
					}
				}
				if count != sends || (creates >= 0 && createCount != creates) || len(closed) != closes {
					t.Fatalf("wire send/create/Close = %d/%d/%d, want %d/%d/%d: %+v", count, createCount, len(closed), sends, creates, closes, writes)
				}
				for _, closeErr := range closed {
					if closeErr != "" {
						t.Fatalf("stdin Close: %s", closeErr)
					}
				}
				return writes
			}
			methodCount := func(writes []c8HistoryWrite, method string) int {
				count := 0
				for _, write := range writes {
					if write.Method == method {
						count++
					}
				}
				return count
			}
			assertStopped := func(rt *runtimeClient) {
				t.Helper()
				select {
				case <-rt.done:
				default:
					t.Fatal("owned Runtime still alive before directory move")
				}
				if rt.exitErr != nil {
					t.Fatalf("owned Runtime exit: %v", rt.exitErr)
				}
			}
			phase = "initial real task"
			h, firstRT, firstObserver := startObserved()
			var contactID, issueID, workSession, taskID, messageID, unknownID string
			var setupErr error
			h.pump.do(func() {
				ct := h.app.selectedContact()
				contactID = ct.ID
				if !h.app.createIssue("C8 negative history goal", "Keep exact requirement and receipts") {
					setupErr = fmt.Errorf("create issue: %s", h.app.issueFormError)
					return
				}
				issueID = ct.SelectedIssueID
				if err := h.app.addIssueSupplement(ct, issueID, "C8 real submitted supplement 原文"); err != nil {
					setupErr = err
					return
				}
				if !h.app.runIssue(issueID) {
					setupErr = fmt.Errorf("run issue: %s", h.app.issueFormError)
				}
			})
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			h.pump.wait(func() bool {
				issue := h.app.contactByID(contactID).issueByID(issueID)
				return len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle && !issue.Work.HistoryDirty && h.app.submissionInFlight == 0 && h.app.contactHistoryInFlight == 0 && h.app.metadataInFlight == 0
			})
			h.pump.do(func() {
				issue := h.app.contactByID(contactID).issueByID(issueID)
				execution := issue.Executions[0]
				workSession, taskID, messageID = issue.WorkSessionID, execution.TaskID, execution.MessageID
				if workSession == "" || taskID == "" || messageID == "" || !containsText(contactMessageTexts(&contact{Messages: issue.Work.Messages}), "c8-negative-source answer") {
					setupErr = fmt.Errorf("real task/history identity or result missing")
				}
			})
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			historyBefore, err := h.app.historyAllForSession(firstRT, workSession)
			if err != nil {
				t.Fatal(err)
			}
			h.pump.do(func() {
				issue := h.app.contactByID(contactID).issueByID(issueID)
				if !supplementHistoryMatches(issue.Executions[0], historyBefore.Records) {
					setupErr = fmt.Errorf("real history did not retain the exact submitted prompt/context")
					return
				}
				// These are saved uncertainty fixtures, not a claimed real lost receipt.
				ledger := issue.Supplements
				target := issueSupplementTargetFor(h.app.selectedContact(), issue)
				if _, err := ledger.Add(target, "C8 unknown supplement 原文", h.app.persist); err != nil {
					setupErr = err
					return
				}
				execution := &issueExecution{ID: newIssueExecutionID(), SessionID: workSession, Status: issueExecutionUnknown,
					Prompt: "C8 constructed uncertain snapshot", Detail: "C8 unknown association remains unconfirmed", RequirementsRevision: issue.RequirementsRevision}
				unknownID = execution.ID
				issue.Executions = append(issue.Executions, execution)
				batch, err := ledger.PrepareDispatch(target, execution, h.app.persist)
				if err != nil {
					setupErr = err
					return
				}
				if !ledger.Unknown(batch, "C8 unknown receipt raw detail", h.app.persist) {
					setupErr = fmt.Errorf("could not save unknown receipt fixture")
					return
				}
				if err := h.app.addIssueSupplement(h.app.selectedContact(), issueID, "C8 pending supplement must not replay"); err != nil {
					setupErr = err
					return
				}
				if !h.app.setIssueSupplementAuto(h.app.selectedContact(), issueID, true) || ledger.pendingCount() != 1 || !ledger.AutoContinue {
					setupErr = fmt.Errorf("compatible pending/unknown enabled ledger fixture missing")
					return
				}
				h.app.selectedContact().Draft = "C8 retained saved chat draft"
				setupErr = h.app.persist()
			})
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			facts["contact_id"], facts["issue_id"], facts["session_id"], facts["task_id"], facts["message_id"], facts["unknown_id"] = contactID, issueID, workSession, taskID, messageID, unknownID
			historyJSON, err := json.Marshal(historyBefore)
			if err != nil {
				t.Fatal(err)
			}
			saveEvidence("history-before.json", historyJSON)
			if provider.count() != 1 {
				t.Fatalf("setup made %d provider requests, want one explicit task", provider.count())
			}
			initialWrites := assertWrites(firstObserver, 1, -1, 0)
			initialCreates := methodCount(initialWrites, "session.create")
			if initialCreates < 1 {
				t.Fatal("initial issue session was not observed")
			}
			facts["initial_session_create_attempts"] = initialCreates
			canonical := guiStorePath()
			saved := recoverySavedBytes(t, canonical)
			saveEvidence("saved.json", saved)
			var expected storedState
			if err := json.Unmarshal(saved, &expected); err != nil {
				t.Fatal(err)
			}
			exportDir := filepath.Join(root, "copies")
			if err := os.Mkdir(exportDir, 0700); err != nil {
				t.Fatal(err)
			}
			msgs := locales.In("en")
			var tr *ui.Tester
			h.pump.do(func() {
				h.app.chooseFolder = func(opts mygo.OpenDialogOptions) ([]string, error) {
					if !opts.Directory || opts.Parent != h.app.win {
						return nil, fmt.Errorf("copy chooser was not the parented folder boundary")
					}
					return []string{exportDir}, nil
				}
				tr = ui.NewTester(h.app.view, 1040, 720)
				tr.SetLocale("en")
				tr.SetFocused(true)
				tr.Frame()
			})
			click := func(harness *guiHarness, tester *ui.Tester, action string) {
				t.Helper()
				var clickErr error
				harness.pump.do(func() {
					clickErr = clickSettingsAction(tester, action)
					tester.Frame()
				})
				if clickErr != nil {
					t.Fatal(clickErr)
				}
			}
			phase = "GUI export"
			click(h, tr, "Settings")
			click(h, tr, "General")
			click(h, tr, msgs.SaveRecoveryCopyAction())
			h.pump.wait(func() bool {
				s := h.app.dataRecovery
				return s != nil && !s.choosing && !s.inFlight && s.notice != ""
			})
			var output string
			h.pump.do(func() {
				s := h.app.dataRecovery
				output = s.output
				if s.problem != "" || s.notice != "copy-saved" || output == "" || h.app.rt != firstRT {
					setupErr = fmt.Errorf("GUI export failed or replaced Runtime: %+v", s)
				}
			})
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			if !bytes.Equal(recoverySavedBytes(t, output), saved) || !bytes.Equal(recoverySavedBytes(t, canonical), saved) {
				t.Fatal("GUI export did not preserve exact saved bytes without force-save")
			}
			saveEvidence("export.json", recoverySavedBytes(t, output))
			facts["export_path"] = output
			phase = "owned shutdown and opaque dataset move"
			h.shutdown(t)
			firstWrites := assertWrites(firstObserver, 1, initialCreates, 1)
			assertStopped(firstRT)
			_, firstCloses := firstObserver.snapshot()
			sessionDir := filepath.Join(root, "sessions")
			preservedDir := filepath.Join(root, "original-runtime-data-preserved")
			if err := os.Rename(sessionDir, preservedDir); err != nil {
				t.Fatal(err)
			}
			facts["preserved_runtime_data"] = preservedDir
			var unrelatedSession string
			var unrelatedHistory runtimeHistory
			var unrelatedProvider *providerFixture
			if unrelated {
				unrelatedProvider = &providerFixture{ToolFirst: false}
				bServer := httptest.NewServer(unrelatedProvider.handler(t, "c8-negative-unrelated"))
				t.Cleanup(bServer.Close)
				bConfig := config
				bConfig.Endpoint = bServer.URL
				bDir := filepath.Join(root, "unrelated-runtime-data")
				launch, err := prepareConnectionLaunch(bConfig, os.Environ(), bDir, root)
				if err != nil {
					t.Fatal(err)
				}
				live := startModelTestRuntime(t, binary, launch.Dir, launch.Env)
				bStopped := false
				t.Cleanup(func() {
					if !bStopped {
						_ = live.client.shutdown()
					}
					_ = launch.Cleanup()
				})
				session, err := live.client.createConfiguredSession(config.WorkingDirectory, "Unrelated Runtime dataset; never substitute for recovered work.")
				if err != nil {
					t.Fatal(err)
				}
				unrelatedSession = session.SessionID
				if unrelatedSession == workSession {
					t.Fatal("unrelated Runtime reused the original session ID")
				}
				task, err := live.client.sendTask(unrelatedSession, "Explicit unrelated dataset setup task")
				if err != nil {
					t.Fatal(err)
				}
				live.finishTask(t, task.TaskID)
				unrelatedHistory, err = h.app.historyAllForSession(live.client, unrelatedSession)
				if err != nil || unrelatedHistory.SessionID != unrelatedSession || len(unrelatedHistory.Records) == 0 || !strings.Contains(strings.Join(issueResultResponseSegments(unrelatedHistory, task.TaskID), "\n"), "c8-negative-unrelated answer") {
					t.Fatalf("unrelated public history: %+v err=%v", unrelatedHistory, err)
				}
				facts["unrelated_session_id"] = unrelatedSession
				data, err := json.Marshal(unrelatedHistory)
				if err != nil {
					t.Fatal(err)
				}
				saveEvidence("unrelated-history.json", data)
				if unrelatedProvider.count() != 1 || provider.count() != 1 {
					t.Fatal("setup exceeded two explicit single-request tasks")
				}
				if err := live.client.shutdown(); err != nil {
					t.Fatal(err)
				}
				bStopped = true
				assertStopped(live.client)
				if err := launch.Cleanup(); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(bDir, sessionDir); err != nil {
					t.Fatal(err)
				}
			}
			providerBaseline := 1
			if unrelated {
				providerBaseline++
			}
			providerTotal := func() int {
				count := provider.count()
				if unrelatedProvider != nil {
					count += unrelatedProvider.count()
				}
				return count
			}
			if providerTotal() != providerBaseline {
				t.Fatal("unexpected provider request before recovery")
			}
			facts["provider_baseline"] = providerBaseline
			phase = "GUI corruption and fallback restore"
			damaged := []byte("{C8 isolated corrupt canonical 原文\n")
			if err := os.WriteFile(canonical, damaged, 0600); err != nil {
				t.Fatal(err)
			}
			_, loadErr := newRuntimeApp()
			if loadErr == nil {
				t.Fatal("corrupt canonical store did not reach the actual unavailable fallback")
			}
			fallback := newUnavailableApp(loadErr)
			f := &guiHarness{app: fallback, pump: newUIPump(t, fallback)}
			t.Cleanup(func() { f.shutdown(t) })
			quitCalls := 0
			f.pump.do(func() {
				if fallback.store != nil || fallback.storeInitAllowed || fallback.rt != nil || fallback.storePath != canonical || fallback.sessionDir != filepath.Join(root, "sessions") {
					setupErr = fmt.Errorf("unavailable fallback changed canonical paths or activated")
				}
				fallback.requestQuit = func() { quitCalls++ }
				fallback.chooseRecoveryFile = func(opts mygo.OpenDialogOptions) ([]string, error) {
					if opts.Directory || opts.Parent != fallback.win {
						return nil, fmt.Errorf("restore chooser was not the parented file boundary")
					}
					return []string{output}, nil
				}
				tr = ui.NewTester(fallback.view, 1040, 720)
				tr.SetLocale("en")
				tr.SetFocused(true)
				tr.Frame()
				if !tr.HasText(msgs.RecoveryLocalDataUnavailable()) {
					setupErr = fmt.Errorf("actual unavailable-data notice missing")
				}
			})
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			click(f, tr, msgs.RestorePausedAction())
			click(f, tr, msgs.RestorePausedAction())
			f.pump.wait(func() bool {
				s := fallback.dataRecovery
				return s != nil && !s.choosing && !s.inFlight && (s.copy != nil || s.problem != "")
			})
			f.pump.do(func() {
				tr.Frame()
				tr.Frame()
				if !fallback.dataRecoveryPreviewCurrent(fallback.dataRecovery) || !tr.HasText(msgs.RecoveryPreviewCounts(1, 1)) || !tr.HasText(msgs.RecoveryPauseWarning()) {
					setupErr = fmt.Errorf("GUI selected-copy preview missing: %v", tr.Texts())
				}
			})
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			if !bytes.Equal(recoverySavedBytes(t, canonical), damaged) {
				t.Fatal("preview installed before explicit confirmation")
			}
			click(f, tr, msgs.RecoveryConfirmAction())
			f.pump.wait(func() bool { return !fallback.dataRecovery.inFlight && fallback.dataRecovery.result.Outcome != "" })
			var result storeRecoveryResult
			f.pump.do(func() {
				s := fallback.dataRecovery
				result = s.result
				if result.Outcome != storeRecoveryVerifiedInstalled || s.problem != "" || fallback.store != nil || fallback.storeInitAllowed || fallback.rt != nil {
					setupErr = fmt.Errorf("GUI paused installation failed or activated fallback: result=%+v problem=%q", result, s.problem)
				}
				tr.Frame()
			})
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			saveEvidence("damaged.bin", damaged)
			saveEvidence("archive.bin", recoverySavedBytes(t, result.ArchivePath))
			facts["archive_path"] = result.ArchivePath
			installedBytes := recoverySavedBytes(t, canonical)
			saveEvidence("installed-before-startup.json", installedBytes)
			var installed storedState
			if err := json.Unmarshal(installedBytes, &installed); err != nil {
				t.Fatal(err)
			}
			for i := range expected.Issues {
				if expected.Issues[i].Supplements != nil {
					expected.Issues[i].Supplements.AutoContinue = false
				}
			}
			if result.OutputPath != canonical || !reflect.DeepEqual(installed, expected) || !bytes.Equal(recoverySavedBytes(t, output), saved) || !bytes.Equal(recoverySavedBytes(t, result.ArchivePath), damaged) {
				t.Fatal("GUI restore changed source/archive or known facts beyond the confirmed pause")
			}
			click(f, tr, msgs.RecoveryQuitAction())
			f.pump.wait(func() bool { return fallback.quitReady })
			f.pump.do(func() {
				if quitCalls != 1 || fallback.rt != nil || fallback.store != nil {
					setupErr = fmt.Errorf("GUI Quit did not keep fallback blocked through its ordinary drain")
				}
			})
			f.shutdown(t)
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			if !bytes.Equal(recoverySavedBytes(t, canonical), installedBytes) {
				t.Fatal("unavailable GUI shutdown overwrote installed data")
			}
			phase = "normal observed reopen"
			reopened, secondRT, secondObserver := startObserved()
			reopened.pump.wait(func() bool {
				ct := reopened.app.contactByID(contactID)
				if ct == nil {
					return false
				}
				issue := ct.issueByID(issueID)
				return issue != nil && !issue.Work.Loaded && issue.Work.LoadErr != "" && reopened.app.contactHistoryInFlight == 0 && reopened.app.metadataInFlight == 0 && reopened.app.submissionInFlight == 0
			})
			reopened.pump.do(func() {
				ct := reopened.app.contactByID(contactID)
				issue := ct.issueByID(issueID)
				if reopened.app.rt != secondRT || secondRT == firstRT || reopened.app.storePath != canonical || reopened.app.sessionDir != filepath.Join(root, "sessions") || reopened.app.connectionPath != connectionConfigPath(canonical) ||
					ct.ID != expected.Contacts[0].ID || ct.Draft != expected.Contacts[0].Draft || ct.SelectedIssueID != issueID || issue.WorkSessionID != workSession || len(issue.Executions) != 2 ||
					issue.Executions[0].TaskID != taskID || issue.Executions[0].MessageID != messageID || issue.Executions[0].Status != issueExecutionCompleted ||
					issue.Executions[1].ID != unknownID || issue.Executions[1].TaskID != "" || issue.Executions[1].MessageID != "" || issue.Executions[1].Status != issueExecutionUnknown || issue.Executions[1].Detail != "C8 unknown association remains unconfirmed" ||
					!reflect.DeepEqual(issue.Supplements, expected.Issues[0].Supplements) || issue.Supplements.AutoContinue || issue.Supplements.pendingCount() != 1 || issue.Work.ActiveTaskID != "" ||
					containsText(contactMessageTexts(&contact{Messages: issue.Work.Messages}), "c8-negative-unrelated answer") {
					setupErr = fmt.Errorf("normal real-process reopen changed associations, raw unknown facts, paused receipts or history: %+v", issue)
				}
			})
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			if !bytes.Equal(recoverySavedBytes(t, canonical), installedBytes) {
				t.Fatal("ordinary startup changed installed saved facts before user navigation")
			}
			assertWrites(secondObserver, 0, 0, 0)
			if providerTotal() != providerBaseline {
				t.Fatal("startup replayed a provider task")
			}
			var workLoadError string
			reopened.pump.do(func() {
				workLoadError = reopened.app.contactByID(contactID).issueByID(issueID).Work.LoadErr
				tr = ui.NewTester(reopened.app.view, 1040, 720)
				tr.SetLocale("en")
				tr.SetFocused(true)
				tr.Frame()
			})
			resultClick := func(label string) {
				t.Helper()
				var err error
				reopened.pump.do(func() {
					err = scrollIssueResultActionIntoView(tr, label, 1040, 720)
					if err == nil {
						err = tr.Click(label)
						tr.Frame()
					}
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			facts["work_load_error"] = workLoadError
			phase = "current-owner result GUI navigation"
			resultClick("Work")
			resultClick("Back to tasks")
			resultClick(msgs.IssueRowLabel("C8 negative history goal"))
			var actionsOpen bool
			reopened.pump.do(func() { actionsOpen = reopened.app.issueDetailOpen[issueID] })
			if !actionsOpen {
				resultClick("More task actions")
			}
			resultClick("Open result: run 1")
			var resultGeneration int
			reopened.pump.do(func() { resultGeneration = reopened.app.issueResult.Generation })
			reopened.pump.wait(func() bool {
				page := reopened.app.issueResult
				return page.Open && !page.Loading && reopened.app.resultInFlight == 0
			})
			var resultDetail string
			reopened.pump.do(func() {
				page := reopened.app.issueResult
				resultDetail = page.LoadErrorDetail
				tr.Frame()
				if reopened.app.rt != secondRT || reopened.app.selectedContact().ID != contactID || page.Generation != resultGeneration || page.OwnerContactID != contactID || page.IssueID != issueID || page.ExecutionID != expected.Issues[0].Executions[0].ID || page.SessionID != workSession || page.TaskID != taskID || page.MessageID != messageID || page.LoadError != "load-failed" || resultDetail == "" || page.Panel.Response != "" || len(page.Panel.Files) != 0 || !tr.HasText(msgs.ResultLoadFailed("")) || tr.HasText(resultDetail) {
					setupErr = fmt.Errorf("current-owner real history failure not visible: page=%+v texts=%v", page, tr.Texts())
				}
			})
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			facts["result_detail"] = resultDetail
			resultClick(msgs.DetailsAction())
			reopened.pump.do(func() {
				page := reopened.app.issueResult
				key := fmt.Sprintf("recovery:result-load:%d:%s", resultGeneration, reopened.app.redactConnectionText(resultDetail))
				if !reopened.app.connectionDetailsOpen[key] || page.LoadErrorDetail != resultDetail || !tr.HasText(resultDetail) {
					setupErr = fmt.Errorf("the current result page's Details did not disclose its real diagnostic: %v", tr.Texts())
				}
			})
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			phase = "public history readback"
			historyAfter, historyErr := reopened.app.historyAllForSession(secondRT, workSession)
			if historyErr == nil {
				t.Fatalf("missing bound history returned success: %+v", historyAfter)
			}
			if unrelated {
				readback, err := reopened.app.historyAllForSession(secondRT, unrelatedSession)
				if err != nil || readback.SessionID != unrelatedSession || !reflect.DeepEqual(readback.Records, unrelatedHistory.Records) {
					t.Fatalf("unrelated actual history was unavailable or changed: %+v err=%v", readback, err)
				}
			}
			reopened.pump.do(func() {
				issue := reopened.app.contactByID(contactID).issueByID(issueID)
				if issue.WorkSessionID != workSession || issue.Work.LoadErr != workLoadError || issue.Work.Loaded || issue.Supplements.AutoContinue || !reflect.DeepEqual(issue.Supplements, expected.Issues[0].Supplements) || containsText(contactMessageTexts(&contact{Messages: issue.Work.Messages}), "c8-negative-unrelated answer") {
					setupErr = fmt.Errorf("history readback substituted data or changed retained state")
				}
			})
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			assertWrites(secondObserver, 0, 0, 0)
			if providerTotal() != providerBaseline {
				t.Fatal("result/history readback replayed provider work")
			}
			phase = "final ordinary shutdown"
			reopened.shutdown(t)
			assertStopped(secondRT)
			secondWrites := assertWrites(secondObserver, 0, 0, 1)
			_, secondCloses := secondObserver.snapshot()
			if providerTotal() != providerBaseline || !bytes.Equal(recoverySavedBytes(t, connectionConfigPath(canonical)), configBytes) {
				t.Fatal("recovery/readback/shutdown replayed provider work or changed connection")
			}
			var finalState storedState
			if err := json.Unmarshal(recoverySavedBytes(t, canonical), &finalState); err != nil {
				t.Fatal(err)
			}
			if len(finalState.Contacts) != len(expected.Contacts) {
				t.Fatal("contact count changed")
			}
			for i, want := range expected.Contacts {
				got := finalState.Contacts[i]
				if got.UpdatedAt < want.UpdatedAt {
					t.Fatal("explicit navigation save moved contact timestamp backwards")
				}
				// Opening Work persists layout; store.save timestamps every contact.
				got.UpdatedAt, want.UpdatedAt = 0, 0
				if !reflect.DeepEqual(got, want) {
					t.Fatal("reopen/result/shutdown changed saved contact facts")
				}
			}
			if !reflect.DeepEqual(finalState.Issues, expected.Issues) || !reflect.DeepEqual(finalState.IssueToolOperations, expected.IssueToolOperations) || !reflect.DeepEqual(finalState.IssueToolScopes, expected.IssueToolScopes) {
				t.Fatal("reopen/result/shutdown rewrote saved product facts")
			}
			beforeJSON, err := json.Marshal(historyBefore)
			if err != nil {
				t.Fatal(err)
			}
			afterJSON, err := json.Marshal(historyAfter)
			if err != nil {
				t.Fatal(err)
			}
			observations, err := json.MarshalIndent(map[string]any{
				"contact_id": contactID, "issue_id": issueID, "real_session_id": workSession, "real_task_id": taskID, "real_message_id": messageID,
				"constructed_unknown_execution_id": unknownID, "unknown_origin": "saved fixture, not a real lost receipt", "provider_requests": providerTotal(), "provider_baseline": providerBaseline, "initial_session_create_attempts": initialCreates, "reopened_session_create_attempts": 0, "work_load_error": workLoadError, "result_detail": resultDetail, "public_history_error": historyErr.Error(), "preserved_runtime_data": preservedDir, "unrelated_session_id": unrelatedSession,
				"initial_task_send_attempts": 1, "reopened_task_send_attempts": 0, "initial_writes": firstWrites, "reopened_writes": secondWrites,
				"initial_close_results": firstCloses, "reopened_close_results": secondCloses,
				"attachment": "each main client nextID=0/healthy checked under writeMu+stateMu before queued Ready", "store_path": canonical, "session_dir": filepath.Join(root, "sessions"),
				"copy_path": output, "archive_path": result.ArchivePath, "restore_outcome": result.Outcome, "quit_calls": quitCalls,
			}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			saveEvidence("saved.json", saved)
			saveEvidence("export.json", recoverySavedBytes(t, output))
			saveEvidence("damaged.bin", damaged)
			saveEvidence("archive.bin", recoverySavedBytes(t, result.ArchivePath))
			saveEvidence("installed-before-startup.json", installedBytes)
			saveEvidence("history-before.json", beforeJSON)
			saveEvidence("history-after.json", afterJSON)
			if unrelated {
				data, err := json.Marshal(unrelatedHistory)
				if err != nil {
					t.Fatal(err)
				}
				saveEvidence("unrelated-history.json", data)
			}
			saveEvidence("observations.json", observations)
			t.Logf("C8 missing/unrelated Runtime-data recovery observations: %s", observations)
		}) {
			return
		}
	}
}
