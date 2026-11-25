//go:build darwin && nagi_native_integration

package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

const connectionNativeSentinel = "Nagi_SYNTHETIC_AX_20261116_Aa7"

var connectionNativeQuit = make(chan struct{})

func TestMain(m *testing.M) {
	flag.Parse()
	root := os.Getenv("NAGI_NATIVE_CONTROL_DIR")
	if !filepath.IsAbs(root) || flag.Lookup("test.run").Value.String() != "^TestConnectionNativeSecureControlE2E$" || os.Getenv("MYGO_LOCALE") != "en" {
		fmt.Fprintln(os.Stderr, "native control requires its owned absolute directory, exact test selector and MYGO_LOCALE=en")
		os.Exit(2)
	}
	mygo.App.SetName("Nagi Secure Control E2E")
	mygo.App.SetPath(mygo.PathUserData, filepath.Join(root, "mygo-user-data"))
	mygo.App.SetPath(mygo.PathCache, filepath.Join(root, "mygo-cache"))
	mygo.App.SetPath(mygo.PathLogs, filepath.Join(root, "mygo-logs"))
	mygo.App.OnQuit(func() { close(connectionNativeQuit) })
	result := make(chan int, 1)
	mygo.App.WhenReady(func() {
		go func() {
			result <- m.Run()
			mygo.App.Quit()
		}()
	})
	if err := mygo.App.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(<-result)
}

type connectionNativeScene struct {
	app           *appModel
	editor        *providerEditor
	root          string
	testCalls     int
	saveCalls     int
	callbackExact bool
	released      bool
	saved         bool
	failure       string
}

func (s *connectionNativeScene) recordFailure(message string) {
	if s.failure == "" {
		s.failure = message
	}
}

func (s *connectionNativeScene) view(c *ui.Context) {
	theme := resolveWorkbenchTheme(c.Theme().Dark)
	c.SetTheme(theme.Theme)
	msgs := locales.In(c.Locale())
	blocked := s.app.providers.busy
	ui.Scroll(c).Fill().Background(theme.Reading).Children(func() {
		ui.Column(c).FillWidth().MaxWidth(settingsContentWidth).AlignSelf(ui.Center).AlignItems(ui.Start).Padding(8, 32, 48).Gap(10).Children(func() {
			pageHeading(c, msgs.SettingsConnection())
			s.app.providerField(c, theme.Theme, msgs, s.editor, "Name", msgs.FieldName(), "", &s.editor.Name, false, blocked)
			s.app.providerEndpoint(c, theme.Theme, msgs, s.editor, blocked)
			s.app.providerField(c, theme.Theme, msgs, s.editor, "APIKey", msgs.ConnectionFieldAPIKey(), "", &s.editor.APIKey, true, blocked)
			s.app.providerField(c, theme.Theme, msgs, s.editor, "Model", msgs.ProviderDefaultModel(), "", &s.editor.DefaultModel, false, blocked)
			ui.Row(c).Gap(8).Children(func() {
				secondaryButton(c, msgs.ProviderTestSync()).Disabled(blocked).OnClick(func() {
					s.testCalls++
					s.callbackExact = s.editor.APIKey == connectionNativeSentinel
					if s.testCalls != 1 || !s.callbackExact {
						s.recordFailure("Test callback count or synthetic key differs")
					}
					s.app.providers.busy = true
				})
				primaryButton(c, msgs.ProviderSave()).Disabled(blocked).OnClick(func() {
					s.saveCalls++
					if s.editor.APIKey != connectionNativeSentinel {
						s.recordFailure("Save did not receive the exact synthetic key")
						return
					}
					cfg := connectionConfig{
						API:              connectionAPIChatCompletions,
						Endpoint:         s.editor.BaseURL,
						APIKey:           s.editor.APIKey,
						Model:            s.editor.DefaultModel,
						ContextTokens:    131072,
						OutputTokens:     8192,
						WorkingDirectory: s.app.workDir,
						MaxActiveTasks:   2,
					}
					if err := saveConnectionConfig(s.app.connectionPath, cfg); err != nil {
						s.recordFailure(err.Error())
						return
					}
					s.saved = true
				})
			})
		})
	})
	if err := s.writeState(); err != nil {
		s.recordFailure(err.Error())
	}
}

func (s *connectionNativeScene) writeState() error {
	digest := sha256.Sum256([]byte(s.editor.APIKey))
	phase := "fresh"
	if s.app.providers.busy && s.testCalls > 0 {
		phase = "busy"
	} else if s.saved {
		phase = "saved"
	} else if s.released {
		phase = "released"
	}
	state := map[string]any{
		"pid": os.Getpid(), "phase": phase, "busy": s.app.providers.busy,
		"test_calls": s.testCalls, "save_calls": s.saveCalls,
		"draft_exact":    s.editor.APIKey == connectionNativeSentinel,
		"draft_sha256":   hex.EncodeToString(digest[:]),
		"callback_exact": s.callbackExact,
		"has_saved":      s.saved,
		"config_path":    s.app.connectionPath, "failure": s.failure,
		"scope": "native production Provider secure input; controlled Test callback, real Save, no Apply or provider",
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temp := filepath.Join(s.root, "scene.json.tmp")
	if err := os.WriteFile(temp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, filepath.Join(s.root, "scene.json"))
}

func connectionNativeGate(root, name string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-connectionNativeQuit:
			return errors.New("native app quit before controlled observations completed")
		case <-ticker.C:
			data, err := os.ReadFile(filepath.Join(root, name))
			if err == nil && string(data) == "observed\n" {
				return nil
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
}

func TestConnectionNativeSecureControlE2E(t *testing.T) {
	root := os.Getenv("NAGI_NATIVE_CONTROL_DIR")
	profile := filepath.Join(root, "profile")
	if err := os.Mkdir(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(profile, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(profile, "contacts.json")
	store, err := openContactStore(storePath, work)
	if err != nil {
		t.Fatal(err)
	}
	app := &appModel{store: store, storePath: storePath, workDir: work, connectionPath: filepath.Join(profile, "connection.json")}
	applyStoreSelection(app, store)
	editor := newProviderDraft()
	editor.Name = "Synthetic Native"
	editor.WireAPI = pi.OpenAICompletions
	editor.BaseURL = "http://127.0.0.1:1/v1"
	editor.DefaultModel = "synthetic-no-provider"
	scene := &connectionNativeScene{app: app, editor: editor, root: root}
	mygo.RunOnMain(func() {
		app.win = mygo.NewWindow(mygo.WindowOptions{
			Title: "Nagi Secure Control E2E — synthetic only", Width: 1040, Height: 720,
			MinWidth: 720, MinHeight: 640, Content: ui.View(scene.view),
		})
	})
	if err := connectionNativeGate(root, "release-test"); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	app.updateUI(func() {
		if scene.testCalls != 1 || !app.providers.busy || !scene.callbackExact {
			scene.recordFailure("release did not follow one exact held Test callback")
		}
		app.providers.busy = false
		scene.released = true
		close(released)
	})
	<-released
	if err := connectionNativeGate(root, "finish"); err != nil {
		t.Fatal(err)
	}
	checked := make(chan string, 1)
	app.updateUI(func() {
		if scene.testCalls != 1 || scene.saveCalls != 1 || !scene.released || app.providers.busy || !scene.saved || editor.APIKey != connectionNativeSentinel {
			scene.recordFailure("fresh/Test/Save sequence or exact draft is incomplete")
		}
		if app.pi != nil || app.connectionEffective != nil || app.providerBusy() {
			scene.recordFailure("a provider or Apply path was activated")
		}
		if err := scene.writeState(); err != nil {
			scene.recordFailure(err.Error())
		}
		checked <- scene.failure
	})
	if failure := <-checked; failure != "" {
		t.Fatal(failure)
	}
	saved, err := loadConnectionConfig(app.connectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != connectionNativeSentinel {
		t.Fatal("saved configuration lost the synthetic key")
	}
	for _, phase := range []string{"fresh", "busy", "saved"} {
		data, err := os.ReadFile(filepath.Join(root, "ax-"+phase+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var observation struct {
			PID          int    `json:"pid"`
			Phase        string `json:"phase"`
			AllQueried   bool   `json:"all_queried"`
			NoExposure   bool   `json:"no_exposure"`
			Secure       bool   `json:"secure_subrole"`
			Enabled      bool   `json:"enabled"`
			EnabledKnown bool   `json:"enabled_known"`
		}
		if err := json.Unmarshal(data, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.PID != os.Getpid() || observation.Phase != phase || !observation.AllQueried || !observation.NoExposure || !observation.Secure || !observation.EnabledKnown || observation.Enabled != (phase != "busy") {
			t.Fatalf("native AX observation incomplete or failed for %s", phase)
		}
	}
	data, err := json.MarshalIndent(map[string]any{"control_e2e": "PASS", "native_full_product": "UNVERIFIED", "test_calls": 1, "save_calls": 1, "provider_calls": 0}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "result.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
