package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestInvalidContentSizeStopsBeforeStartup(t *testing.T) {
	if os.Getenv("GO_WANT_INVALID_CONTENT_SIZE") == "1" {
		main()
		t.Fatal("invalid content size did not stop startup")
	}
	for _, size := range []string{"960", "960x", "x640", "960x640x1", "0x640", "960x-1", "NaNx640", "960.5x640", "999999999999999999999999x640"} {
		t.Run(size, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("NAGI_GUI_STORE", filepath.Join(root, "contacts.json"))
			t.Setenv("NAGI_GUI_WORKDIR", root)
			t.Setenv("NAGI_GUI_CONTENT_SIZE", size)
			t.Setenv("GO_WANT_INVALID_CONTENT_SIZE", "1")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInvalidContentSizeStopsBeforeStartup$")
			output, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("startup error = %v, context = %v, output = %s", err, ctx.Err(), output)
			}
			if !strings.Contains(string(output), "NAGI_GUI_CONTENT_SIZE must be positive integer WIDTHxHEIGHT in DIPs") {
				t.Fatalf("missing startup input diagnostic: %s", output)
			}
			files, err := os.ReadDir(root)
			if err != nil || len(files) != 0 {
				t.Fatalf("invalid input wrote startup data: files=%v err=%v", files, err)
			}
		})
	}
}

func runtimeFeedback(a *appModel) (string, string) {
	msgs := locales.In("en")
	return a.runtimeErrorDisplay(msgs), a.runtimeHintDisplay(msgs)
}

func TestSafeDiagnosticRedactsSecrets(t *testing.T) {
	cases := []string{
		"API_KEY=sk-sentinel-112",
		"Authorization: Bearer sentinel-112",
		"token=token-sentinel-112",
		`{"api_key":"json-sentinel-112"}`,
		`{"token": "json-token-sentinel-112"}`,
		"Incorrect API key provided: sk-sentinel-112",
		"https://user:pass-sentinel-112@example.test/v1",
	}
	for _, input := range cases {
		got := safeDiagnostic(input)
		if strings.Contains(got, "sentinel-112") {
			t.Errorf("safeDiagnostic(%q) leaked a secret: %q", input, got)
		}
	}
	if got := safeDiagnostic("API_KEY is not set"); !strings.Contains(got, "API_KEY is not set") {
		t.Fatalf("safe non-value diagnostic was lost: %q", got)
	}
}

func TestUnavailableStoreFallbackKeepsPathsAndFile(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "contacts.json")
	workDir := filepath.Join(dir, "work")
	original := []byte("{not json")
	if err := os.WriteFile(storePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NAGI_GUI_STORE", storePath)
	t.Setenv("NAGI_GUI_WORKDIR", workDir)

	_, err := newRuntimeApp()
	if err == nil || !strings.Contains(err.Error(), storePath) {
		t.Fatalf("newRuntimeApp error = %v", err)
	}
	a := newUnavailableApp(err)
	if a.store != nil {
		t.Fatal("unavailable fallback must keep store=nil")
	}
	if a.runtimeStage != startupStageLocalStore {
		t.Fatalf("stage = %q", a.runtimeStage)
	}
	if a.storePath != storePath || a.workDir != workDir {
		t.Fatalf("fallback paths = store %q, work %q", a.storePath, a.workDir)
	}
	if !strings.Contains(a.runtimeErr, storePath) || a.contacts[0].Unavailable != err.Error() {
		t.Fatalf("original store error was not preserved: runtimeErr=%q unavailable=%q", a.runtimeErr, a.contacts[0].Unavailable)
	}
	_, fallbackHint := runtimeFeedback(a)
	for _, want := range []string{storePath, "untouched", "did not create a replacement store", "Drafts cannot be persisted"} {
		if !strings.Contains(fallbackHint, want) {
			t.Errorf("hint = %q, missing %q", fallbackHint, want)
		}
	}

	a.openAdvancedSettings()
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	tr.Frame()
	for _, want := range []string{
		"Startup & configuration",
		locales.In("en").RuntimeStageLocalStore() + ":",
		locales.In("en").RuntimeNotStartedLocalStore(),
		"Store: " + storePath,
		"GUI work directory: " + workDir,
	} {
		if !tr.HasText(want) {
			t.Errorf("fallback panel missing %q", want)
		}
	}

	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("fallback must not overwrite the corrupt store")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "contacts.json" {
		t.Fatalf("fallback created extra store/session/work files: %v", entries)
	}
}

func TestRuntimePanelShowsPathsAndMetadata(t *testing.T) {
	ct := &contact{
		ID: "nagi", Name: "Nagi", Role: "Assistant",
		Workspace: "/tmp/contact-ws", SessionID: "session-1", Loaded: true,
		SessionWorkingDir: "/tmp/session-ws", SessionModel: "model-x",
		SessionAPI: "openai-chat-completions", SessionState: "running", Draft: "keep",
	}
	a := &appModel{
		demo: false, runtimeStatus: runtimeStateReady,
		storePath: "/tmp/contacts.json", workDir: "/tmp/work",
		contacts: []*contact{ct},
	}
	a.openAdvancedSettings()
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	tr.Frame()
	for _, want := range []string{
		"Startup & configuration",
		"Store: /tmp/contacts.json",
		"GUI work directory: /tmp/work",
		locales.In("en").RuntimeContactWorkspaceLine("/tmp/session-ws"),
		"Session: session-1",
		"state: running",
		"Session API: openai-chat-completions",
		"Session model: model-x",
	} {
		if !tr.HasText(want) {
			t.Errorf("runtime panel missing %q; texts=%v", want, tr.Texts())
		}
	}
	if ct.Draft != "keep" {
		t.Fatalf("runtime status changed the draft: %q", ct.Draft)
	}
}

func TestConfigurationEntryKeepsGlobalSettingsUsable(t *testing.T) {
	ct := &contact{ID: "nagi", Name: "Nagi", Role: "Assistant", Draft: "keep"}
	a := &appModel{
		runtimeStatus: runtimeStateReady, storePath: "/tmp/contacts.json",
		workDir:  "/tmp/work",
		contacts: []*contact{ct},
	}
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	msgs := locales.In("en")
	for _, want := range []string{msgs.SettingsGeneral(), msgs.SettingsConnection(), msgs.SettingsAdvanced(), msgs.SettingsBackAccessible()} {
		if !tr.HasText(want) {
			t.Fatalf("global Settings missing %q", want)
		}
	}
	if err := tr.Click("Advanced"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAdvanced {
		t.Fatalf("advanced route = open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
	for _, want := range []string{"Startup & configuration", locales.In("en").RuntimeStoreLine("/tmp/contacts.json")} {
		if !tr.HasText(want) {
			t.Fatalf("advanced page missing %q", want)
		}
	}
	if ct.Draft != "keep" {
		t.Fatalf("configuration view changed the draft: %q", ct.Draft)
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	if a.settingsOpen {
		t.Fatal("back did not close the settings page")
	}
}

func TestStartupBannerKeepsComposerFocusAndDraft(t *testing.T) {
	ct := &contact{ID: "nagi", Name: "Nagi", Role: "Assistant", Draft: "keep"}
	a := &appModel{
		demo: false, runtimeStatus: runtimeStateFailed,
		runtimeStage: "Runtime startup", runtimeErr: "Pi worker could not start.",
		runtimeHint: "Check the installed Nagi app.",
		contacts:    []*contact{ct},
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	if !tr.HasText("Configuration details") || !tr.HasText(locales.In("en").ConnectionRecoveryManagedHint()) || tr.HasText(a.runtimeErr) || tr.HasText(a.runtimeHint) {
		t.Fatal("startup failure should show an actionable banner")
	}
	if err := tr.Click("Composer for Nagi"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if _, active := tr.TextCaret(); !active {
		t.Fatal("startup banner must not steal composer focus")
	}
	if ct.Draft != "keep" {
		t.Fatalf("startup banner changed the draft: %q", ct.Draft)
	}
	if err := tr.Click("Configuration details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAdvanced || !tr.HasText(a.runtimeErr) || !tr.HasText(a.runtimeHint) {
		t.Fatalf("banner opened settings=%v tab=%q, want Advanced", a.settingsOpen, a.settingsTab)
	}
	a.closeSettings()
	if ct.Draft != "keep" || a.runtimeStatus != runtimeStateFailed || a.runtimeErr != "Pi worker could not start." ||
		a.runtimeHint != "Check the installed Nagi app." || a.submissionInFlight != 0 {
		t.Fatal("startup disclosure changed the draft or original failure facts")
	}
}

func withoutEnv(env []string, name string) []string {
	out := env[:0]
	for _, entry := range env {
		if strings.HasPrefix(entry, name+"=") {
			continue
		}
		out = append(out, entry)
	}
	return out
}
