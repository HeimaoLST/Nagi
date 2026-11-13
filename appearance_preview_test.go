package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPreferenceControlFacesKeepHitAndKeyboard(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, width := range []int{160, 320} {
			t.Run(fmt.Sprintf("width%d-dark%t", width, dark), func(t *testing.T) {
				value, selected := "", "one"
				first, second := 0, 0
				blocked, invalid := false, false
				tr := ui.NewTester(func(c *ui.Context) {
					theme := resolveWorkbenchTheme(dark).Theme
					c.SetTheme(theme)
					ui.Column(c).Fill().Padding(20).Gap(12).Background(theme.Background).Children(func() {
						preferenceInput(c, &value, invalid).Label("Exact value").Width(float32(width)).Disabled(blocked)
						secondaryButton(c, "First action").Label("First action").OnClick(func() { first++ })
						secondaryButton(c, "Second action").Label("Second action").OnClick(func() { second++ })
						preferenceSelect(c, &selected, []selectOption{{"one", "One"}, {"two", "Two"}}, "Choice", blocked).Width(float32(width))
					})
				}, width+40, 280)
				tr.SetFocused(true)
				tr.Frame()
				input, ok := tr.Find("Exact value")
				if !ok || input.H < 32 {
					t.Fatalf("input has no full interaction box: %+v", input)
				}
				image := tr.Image()
				background := resolveWorkbenchTheme(dark).Theme.Background
				pixel := image.RGBAAt(int(input.X+input.W/2), int(input.Y))
				if pixel.R != background.R || pixel.G != background.G || pixel.B != background.B {
					t.Fatalf("transparent hit extension became a visible input face: %+v", pixel)
				}
				tr.ClickAt(input.X+input.W/2, input.Y+1)
				tr.Type("  +0002  ")
				tr.Frame()
				if value != "  +0002  " || !tr.Focused("Exact value") {
					t.Fatal("transparent input extension failed to focus/edit the exact string")
				}
				tr.Key(ui.Cmd, ui.KeyA)
				tr.Type("long raw invalid value with suffix 2x")
				invalid = true
				tr.Frame()
				if value != "long raw invalid value with suffix 2x" {
					t.Fatal("styled input normalized or lost invalid text")
				}
				bounds := map[string]ui.Rect{"input": input}
				for _, label := range []string{"First action", "Second action"} {
					box, ok := tr.Find(label)
					if !ok || box.H < 32 {
						t.Fatalf("action lost its real hit target: %s %+v", label, box)
					}
					bounds[label] = box
				}
				one, two := bounds["First action"], bounds["Second action"]
				tr.ClickAt(one.X+one.W/2, one.Y+1)
				tr.Frame()
				if first != 1 || second != 0 {
					t.Fatal("transparent button extension did not dispatch exactly its own action")
				}
				tr.ClickAt(one.X+one.W/2, (one.Y+one.H+two.Y)/2)
				tr.Frame()
				if first != 1 || second != 0 {
					t.Fatal("interaction extension intercepted the gap or its neighbor")
				}
				tr.ClickAt(two.X+two.W/2, two.Y+two.H-1)
				tr.Frame()
				tr.Key(0, ui.KeyEnter)
				tr.Frame()
				if first != 1 || second != 2 {
					t.Fatal("neighbor pointer/keyboard action changed ownership or dispatch count")
				}
				if err := tr.Click("Choice"); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				blocked = true
				tr.Frame()
				if err := tr.Click("Two"); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if selected != "one" {
					t.Fatal("busy popup changed a stable enum value")
				}
				tr.Key(0, ui.KeyEscape)
				tr.Frame()
				if directory := os.Getenv("SAKI_PREVIEW_DIR"); directory != "" {
					name := fmt.Sprintf("shared-controls-%d-dark-%t", width, dark)
					writePreviewPNG(t, filepath.Join(directory, name+".png"), tr.Image())
					data, err := json.MarshalIndent(bounds, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(directory, name+".bounds.json"), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestPreferenceControlShapeReferenceFrames(t *testing.T) {
	for _, region := range []float32{8, 16} {
		for _, width := range []int{80, 240} {
			for _, state := range []string{"normal", "focus", "invalid", "disabled"} {
				name := fmt.Sprintf("shape-region%.0f-width%d-%s", region, width, state)
				t.Run(name, func(t *testing.T) {
					value := ""
					if state == "focus" {
						value = "Long input text deliberately overflows its inner text viewport"
					}
					tr := ui.NewTester(func(c *ui.Context) {
						theme := resolveWorkbenchTheme(false).Theme
						c.SetTheme(theme)
						ui.Column(c).Fill().Padding(20).Background(theme.Background).Children(func() {
							var face ui.Element
							if region == 16 {
								face = preferenceInput(c, &value, state == "invalid")
							} else {
								// The same region8 face used by navigation, isolated for geometric comparison.
								face = sharedControlFace(c, ui.ButtonBase(c), region, 4, true, state == "invalid", false)
							}
							face.Label(name).Width(float32(width)).Disabled(state == "disabled")
						})
					}, width+40, 80)
					tr.SetFocused(true)
					tr.Frame()
					if state == "focus" {
						if err := tr.Click(name); err != nil {
							t.Fatal(err)
						}
						tr.Frame()
						tr.Move(0, 0)
						if region == 16 {
							tr.Key(ui.Cmd, ui.KeyA)
						}
						tr.Frame()
						if !tr.Focused(name) {
							t.Fatal("reference frame did not acquire actual control focus")
						}
					}
					box, ok := tr.Find(name)
					if !ok || box.H < 32 || box.X < 0 || box.X+box.W > float32(width+40) {
						t.Fatalf("reference control lost its actual bounded interaction box: %+v", box)
					}
					if directory := os.Getenv("SAKI_PREVIEW_DIR"); directory != "" {
						writePreviewPNG(t, filepath.Join(directory, name+".png"), tr.Image())
						data, err := json.MarshalIndent(map[string]any{"hit": box, "face": ui.Rect{X: box.X, Y: box.Y + 2, W: box.W, H: box.H - 4}, "region": region, "state": state, "actual_focus": tr.Focused(name), "actual_input": region == 16, "diagnostic": "Region8 isolates the shared face; region16 is the production TextInputBase path. Native/browser text raster is not compared."}, "", "  ")
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(directory, name+".json"), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func TestPreferenceDisabledFaceComposesOnce(t *testing.T) {
	blocked, value := false, ""
	tr := ui.NewTester(func(c *ui.Context) {
		theme := resolveWorkbenchTheme(false).Theme
		c.SetTheme(theme)
		ui.Column(c).Fill().Padding(20).Background(theme.Background).Children(func() {
			preferenceInput(c, &value, false).Label("Opacity input").Width(160).Disabled(blocked)
		})
	}, 200, 80)
	tr.Frame()
	box, ok := tr.Find("Opacity input")
	if !ok {
		t.Fatal("input missing")
	}
	x, y := int(box.X+box.W/2), int(box.Y+2)
	normal := tr.Image().RGBAAt(x, y)
	blocked = true
	tr.Frame()
	disabled := tr.Image().RGBAAt(x, y)
	background := resolveWorkbenchTheme(false).Theme.Background
	for channel, pair := range [][3]uint8{{normal.R, disabled.R, background.R}, {normal.G, disabled.G, background.G}, {normal.B, disabled.B, background.B}} {
		expected := (int(pair[0]) + int(pair[2])) / 2
		difference := int(pair[1]) - expected
		// One level permits only integer alpha rounding, not overlapping half-opacity paints.
		if difference < -1 || difference > 1 {
			t.Errorf("disabled border channel%d=%d, single half-opacity composite=%d (normal=%v disabled=%v)", channel, pair[1], expected, normal, disabled)
		}
	}
}

// appearancePreviewApp uses the existing workspace fixture state; it does not
// seed production work or call Runtime.
func appearancePreviewApp(t *testing.T, scene string) (*appModel, *contact) {
	t.Helper()
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	a.runtimeCapabilitiesLoaded = true
	a.runtimeMaxActiveTasks = 2
	ct.Role = "Assistant"
	ct.Draft = "把这周的发布说明收一下，保留待确认的问题。"
	switch scene {
	case "normal":
		ct.Messages = []*message{
			{ID: 1, Kind: msgUser, Text: "帮我把这周的发布说明收一下。", Time: "09:42"},
			{ID: 2, Kind: msgContact, Text: "可以。我会先核对已完成的事项，再把结果放到工作区。", Time: "09:43"},
		}
	case "working":
		ct.Status = statusWorking
		ct.ActiveTaskID, ct.LastTaskID = "preview-task", "preview-task"
		ct.Messages = []*message{
			{ID: 1, Kind: msgUser, Text: "先汇总已完成的事项。", Time: "10:02"},
			{ID: 2, Kind: msgContact, Text: "我正在核对结果与未满足的要求。", Time: "10:03"},
		}
		w := a.ensureWork(ct, "preview-task", "preview-message")
		w.State = workRunning
		w.Prompt = "Checking release notes"
		w.Tools = []*workTool{{CallID: "preview-tool", Name: "read", Args: `{"path":"notes.md"}`, Started: true, Finished: true, Output: "release notes"}}
		a.workspaceOpen = true
	case "attention":
		ct.Status = statusAttention
		ct.Messages = []*message{{
			ID: 1, Kind: msgContact, Text: "有一项要求还缺少依据，需要你确认后再继续。", Time: "10:12",
			AlertTitle: "Needs attention", AlertDetail: "One requirement is still unverified.",
		}}
	case "error":
		ct.SendError = "connection_mismatch: preview connection unavailable"
		ct.LoadErr = "connection_mismatch: preview connection unavailable"
		ct.Messages = []*message{{ID: 1, Kind: msgContact, Text: "连接不可用；这段草稿仍然保留。", Time: "10:20"}}
	case "empty":
		ct.Messages = nil
	}
	return a, ct
}

func writeV07AppearancePreview(t *testing.T, dir, name string, a *appModel, width, height int, dark bool) {
	t.Helper()
	tr := ui.NewTester(a.view, width, height)
	tr.SetDark(dark)
	tr.Frame()
	writePreviewPNG(t, filepath.Join(dir, name), tr.Image())
}

// writeV07AppearancePreviews is invoked by the existing TestPreviewImages
// entry; it adds no new opt-in skip and no second preview test.
func writeV07AppearancePreviews(t *testing.T, dir string) {
	t.Helper()
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}
	// First review gate: main, Appearance panel, and the minimum composer.
	for _, mode := range []struct {
		name    string
		mode    string
		sysDark bool
	}{{"paper", appearanceLight, true}, {"night", appearanceDark, false}} {
		for _, tc := range []struct {
			name           string
			openAppearance bool
			width, height  int
		}{
			{"main", false, 1040, 720},
			{"appearance", true, 1040, 720},
			{"composer", false, 720, 640},
		} {
			a, _ := appearancePreviewApp(t, "normal")
			a.appearanceMode = mode.mode
			a.windowWidth = tc.width
			if tc.openAppearance {
				a.openAppearance()
			}
			writeV07AppearancePreview(t, dir,
				fmt.Sprintf("v07-review-%s-%s-%dx%d.png", mode.name, tc.name, tc.width, tc.height),
				a, tc.width, tc.height, mode.sysDark)
		}
	}

	// Wider state matrix: widths are normal shell, richer states at 1040.
	scenes := []string{"normal", "working", "attention", "error", "empty"}
	sizes := []struct{ width, height int }{{1040, 720}, {1380, 900}, {960, 640}, {720, 640}}
	for _, mode := range []struct {
		name    string
		mode    string
		sysDark bool
	}{{"paper", appearanceLight, true}, {"night", appearanceDark, false}} {
		for _, scene := range scenes {
			for _, size := range sizes {
				if scene != "normal" && (size.width != 1040 || size.height != 720) {
					continue
				}
				a, _ := appearancePreviewApp(t, scene)
				a.appearanceMode = mode.mode
				a.windowWidth = size.width
				writeV07AppearancePreview(t, dir,
					fmt.Sprintf("v07-%s-%s-%dx%d.png", mode.name, scene, size.width, size.height),
					a, size.width, size.height, mode.sysDark)
			}
		}
	}
}
