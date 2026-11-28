package app

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/connection"
	"github.com/HeimaoLST/Nagi/internal/materials"
	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

func visualGuardSeededPool() pi.ProductProviderList {
	rev1 := uint64(1)
	yes := true
	return pi.ProductProviderList{
		SavedRevision:     &rev1,
		EffectiveRevision: &rev1,
		Policy:            pi.ProductRoutingBalanced,
		Accounts: []pi.ProductProviderAccountView{
			{
				ProductProviderAccount: pi.ProductProviderAccount{
					ProviderID:    "prov-internal-openai",
					Name:          "OpenAI Studio",
					WireAPI:       pi.OpenAIResponses,
					BaseURL:       "https://api.openai.com/v1",
					DefaultModel:  "gpt-5",
					Models:        []string{"gpt-5", "gpt-4o", "gpt-5.4-mini"},
					ContextTokens: 131072,
					OutputTokens:  16384,
					Enabled:       true,
				},
				CredentialStatus: "ok",
				HealthSummary:    "healthy",
			},
			{
				ProductProviderAccount: pi.ProductProviderAccount{
					ProviderID:    "prov-internal-deepseek",
					Name:          "DeepSeek Backup",
					WireAPI:       pi.OpenAICompletions,
					BaseURL:       "https://api.deepseek.com/v1",
					DefaultModel:  "deepseek-chat",
					Models:        []string{"deepseek-chat", "deepseek-reasoner"},
					ContextTokens: 131072,
					OutputTokens:  8192,
					Enabled:       true,
					FallbackOnly:  &yes,
				},
				CredentialStatus: "ok",
				HealthSummary:    "healthy",
			},
		},
		ResolvedRoles: pi.ProductResolvedRoles{
			Dialogue:  &pi.ProductModelRoute{ProviderID: "prov-internal-openai", ProviderName: "OpenAI Studio", ResolvedModel: "gpt-5", ThinkingLevel: "off"},
			Coding:    &pi.ProductModelRoute{ProviderID: "prov-internal-openai", ProviderName: "OpenAI Studio", ResolvedModel: "gpt-5.4-mini", ThinkingLevel: "medium"},
			Reasoning: &pi.ProductModelRoute{ProviderID: "prov-internal-deepseek", ProviderName: "DeepSeek Backup", ResolvedModel: "deepseek-reasoner", ThinkingLevel: "high"},
			Utility:   &pi.ProductModelRoute{ProviderID: "prov-internal-openai", ProviderName: "OpenAI Studio", ResolvedModel: "gpt-4o", ThinkingLevel: "off"},
		},
	}
}

func visualGuardExpectedSettingsContentLeft(width int) float32 {
	narrow := width < int(preferenceBreakpoint)
	mainLeft := float32(0)
	mainWidth := float32(width)
	if !narrow {
		mainLeft = settingsSidebarWidth + 1
		mainWidth -= settingsSidebarWidth + 1
	}
	contentW := min(mainWidth, float32(settingsContentWidth))
	return mainLeft + (mainWidth-contentW)/2 + 32
}

func TestVisualGuardSettingsLeftEdgeAlignment(t *testing.T) {
	sizes := [][2]int{{1040, 720}, {720, 640}, {600, 640}}
	for _, locale := range []string{"en", "zh-Hans"} {
		msgs := locales.In(locale)
		for _, dark := range []bool{false, true} {
			for _, size := range sizes {
				name := fmt.Sprintf("%s-dark%t-%dx%d", locale, dark, size[0], size[1])
				t.Run(name, func(t *testing.T) {
					a := providerUITestApp(t, visualGuardSeededPool())
					a.windowWidth = size[0]
					a.capabilities.library.ready = true
					tr := ui.NewTesterWith(a.view, size[0], size[1], ui.TesterOptions{Dark: dark})
					tr.SetLocale(locale)
					tr.SetFocused(true)

					wantX := visualGuardExpectedSettingsContentLeft(size[0])
					type tabSpec struct {
						name       string
						open       func()
						sections   []string
						cardLabels []string
					}
					tabs := []tabSpec{
						{
							name: "general",
							open: func() { a.openSettingsTab(settingsTabAppearance, settingsSourceRail) },
							sections: []string{
								msgs.RailContactsTitle(),
								msgs.SettingsAppearance(),
								msgs.SettingsLanguage(),
								msgs.RecoveryDataTitle(),
							},
							cardLabels: []string{
								msgs.SettingsPandoraBoxVisibility(),
							},
						},
						{
							name: "providers",
							open: func() {
								a.openSettingsTab(settingsTabConnection, settingsSourceRail)
								a.providers.tab = providerTabAccounts
							},
							sections: []string{
								msgs.ProviderConfiguredTitle(),
								a.providerCatalog().Sections[0].Title.Resolve(locale),
								a.providerCatalog().Sections[1].Title.Resolve(locale),
							},
						},
						{
							name: "routing",
							open: func() {
								a.openSettingsTab(settingsTabConnection, settingsSourceRail)
								a.providers.tab = providerTabRouting
							},
							sections: []string{
								msgs.ProviderRouting(),
							},
							cardLabels: []string{
								msgs.ProviderStrategy(),
							},
						},
						{
							name: "advanced",
							open: func() { a.openSettingsTab(settingsTabAdvanced, settingsSourceRail) },
							cardLabels: []string{
								msgs.SettingsStartupSection(),
							},
						},
						{
							name: "capabilities",
							open: func() {
								a.openSettingsTab(settingsTabCapabilities, settingsSourceRail)
								a.capabilities.library.ready = true
							},
							sections: []string{
								msgs.CapNoSkills(),
							},
						},
					}

					var referenceSectionX *float32
					var referenceCardLabelX *float32
					for _, tab := range tabs {
						tab.open()
						a.settingsScroll.Y = 0
						tr.Frame()
						for _, sec := range tab.sections {
							r, ok := tr.Find(sec)
							if !ok {
								t.Fatalf("tab=%s missing section header %q: %v", tab.name, sec, tr.Texts())
							}
							if delta := math.Abs(float64(r.X - wantX)); delta > 2.5 {
								t.Fatalf("tab=%s section %q X=%.2f drifted from PageHeading X=%.2f (delta=%.2f > 2.5)", tab.name, sec, r.X, wantX, delta)
							}
							if referenceSectionX == nil {
								x := r.X
								referenceSectionX = &x
							} else if tab.name != "capabilities" && math.Abs(float64(r.X-*referenceSectionX)) > 2.5 {
								t.Fatalf("tab=%s section %q X=%.2f inconsistent with baseline %.2f", tab.name, sec, r.X, *referenceSectionX)
							}
						}
						for _, label := range tab.cardLabels {
							r, ok := tr.Find(label)
							if !ok {
								t.Fatalf("tab=%s missing card label %q: %v", tab.name, label, tr.Texts())
							}
							if referenceCardLabelX == nil {
								x := r.X
								referenceCardLabelX = &x
							} else if math.Abs(float64(r.X-*referenceCardLabelX)) > 2.5 {
								t.Fatalf("tab=%s card label %q X=%.2f inconsistent with General card baseline %.2f", tab.name, label, r.X, *referenceCardLabelX)
							}
						}
					}
				})
			}
		}
	}
}

func TestVisualGuardProviderModalFormAlignment(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		msgs := locales.In(locale)
		for _, mode := range []string{"saved", "new-preset"} {
			t.Run(locale+"/"+mode, func(t *testing.T) {
				pool := visualGuardSeededPool()
				a := providerUITestApp(t, pool)
				a.windowWidth = 1040
				if mode == "saved" {
					a.selectProvider(pool.Accounts[0])
				} else {
					a.openProviderPreset(pi.ProductProviderList{Policy: pi.ProductRoutingBalanced}, "openai")
				}
				tr := ui.NewTester(a.view, 1040, 720)
				tr.SetLocale(locale)
				tr.SetFocused(true)
				tr.Frame()

				focusInputMidY := func(label string) float32 {
					t.Helper()
					for range 48 {
						active, _ := tr.TextInputActive()
						if tr.Focused(label) && active {
							caret, ok := tr.TextCaret()
							if !ok {
								t.Fatalf("focused input %q has no active caret", label)
							}
							return caret.Y + caret.H/2
						}
						tr.Key(0, ui.KeyTab)
						tr.Frame()
					}
					t.Fatalf("input %q was not focusable inside provider modal", label)
					return 0
				}

				for _, label := range []string{msgs.FieldName(), msgs.ProviderBaseURL(), msgs.ProviderDefaultModel()} {
					labelRect, ok := tr.Find(label)
					if !ok {
						t.Fatalf("modal label %q missing", label)
					}
					labelMidY := labelRect.Y + labelRect.H/2
					controlMidY := focusInputMidY(label)
					if delta := math.Abs(float64(labelMidY - controlMidY)); delta > 4.0 {
						t.Fatalf("row %q misaligned: labelMidY=%.2f controlMidY=%.2f (delta=%.2f > 4)", label, labelMidY, controlMidY, delta)
					}
				}

				keyLabelRect, ok := tr.Find(msgs.ConnectionFieldAPIKey())
				if !ok {
					t.Fatalf("API key row label missing")
				}
				keyLabelMidY := keyLabelRect.Y + keyLabelRect.H/2
				var keyControlMidY float32
				if mode == "saved" {
					badgeRect, okB := tr.Find(msgs.ProviderKeyInUse())
					if !okB {
						t.Fatalf("saved API key masked strip missing %q", msgs.ProviderKeyInUse())
					}
					keyControlMidY = badgeRect.Y + badgeRect.H/2
				} else {
					keyControlMidY = focusInputMidY(msgs.ConnectionFieldAPIKey())
				}
				if delta := math.Abs(float64(keyLabelMidY - keyControlMidY)); delta > 4.0 {
					t.Fatalf("API key row misaligned: labelMidY=%.2f controlMidY=%.2f (delta=%.2f > 4)", keyLabelMidY, keyControlMidY, delta)
				}

				poolLabelRect, okP := tr.Find(msgs.ProviderModelPool())
				selectAllRect, okS := tr.Find(msgs.ProviderSelectAll())
				if !okP || !okS {
					t.Fatalf("Model pool row missing label=%v selectAll=%v", okP, okS)
				}
				if delta := math.Abs(float64((poolLabelRect.Y + poolLabelRect.H/2) - (selectAllRect.Y + selectAllRect.H/2))); delta > 4.0 {
					t.Fatalf("Model pool row misaligned: labelMidY=%.2f controlMidY=%.2f (delta=%.2f > 4)",
						poolLabelRect.Y+poolLabelRect.H/2, selectAllRect.Y+selectAllRect.H/2, delta)
				}
			})
		}
	}
}

func closeRGBA(a, b [4]uint8) bool {
	for i := range 4 {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		if d > 5 {
			return false
		}
	}
	return true
}

func TestVisualGuardModalsRoomyNoClippingAndSinglePrimaryCTA(t *testing.T) {
	sizes := [][2]int{{1440, 900}, {1040, 720}, {720, 640}}
	rev2 := uint64(2)

	for _, locale := range []string{"en", "zh-Hans"} {
		msgs := locales.In(locale)
		for _, dark := range []bool{false, true} {
			for _, size := range sizes {
				tag := fmt.Sprintf("%s-dark%t-%dx%d", locale, dark, size[0], size[1])
				t.Run(tag, func(t *testing.T) {
					w, h := size[0], size[1]
					assertNoInternalIDs := func(tr *ui.Tester, scene string) {
						t.Helper()
						for _, txt := range tr.Texts() {
							if strings.Contains(txt, "prov-internal-") || strings.Contains(txt, "provider-") {
								t.Fatalf("scene=%s leaked internal provider ID in UI text: %q", scene, txt)
							}
						}
					}
					assertInViewport := func(tr *ui.Tester, scene string, labels ...string) {
						t.Helper()
						for _, label := range labels {
							r, ok := tr.Find(label)
							if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(w)+0.5 || r.Y+r.H > float32(h)+0.5 || r.H < theme.ControlHit || r.W < theme.ControlHit {
								t.Fatalf("scene=%s control %q clipped or under hit target: rect=%+v ok=%v viewport=%dx%d", scene, label, r, ok, w, h)
							}
						}
					}

					gen := providerUITestApp(t, visualGuardSeededPool())
					gen.windowWidth = w
					gen.openSettingsTab(settingsTabAppearance, settingsSourceRail)
					tr := ui.NewTesterWith(gen.view, w, h, ui.TesterOptions{Dark: dark})
					tr.SetLocale(locale)
					tr.SetFocused(true)
					tr.Frame()
					assertNoInternalIDs(tr, "general")
					assertInViewport(tr, "general", msgs.AppearanceSystem(), msgs.ThemeImportAction(), msgs.ThemeReload())
					hintRect, okHint := tr.Find(msgs.SettingsPandoraBoxVisibilityHint())
					if !okHint || (locale == "en" && w == 1040 && hintRect.H < 30) {
						t.Fatalf("scene=general PandoraBox hint did not wrap cleanly: ok=%v rect=%+v", okHint, hintRect)
					}
					focusedToggle := false
					for range 20 {
						if tr.Focused(msgs.SettingsPandoraBoxVisibility()) {
							focusedToggle = true
							break
						}
						tr.Key(0, ui.KeyTab)
						tr.Frame()
					}
					if !focusedToggle {
						t.Fatal("scene=general PandoraBox ToggleSwitch not keyboard-focusable")
					}
					tr.Key(0, ui.KeySpace)
					tr.Frame()
					if !gen.hideBuiltinArchitect {
						t.Fatal("scene=general PandoraBox ToggleSwitch did not toggle visibility")
					}

					v2Cfg := connectionConfig{
						API: connectionAPIResponses, Endpoint: "https://api.openai.com/v1", APIKey: "sk-hidden",
						Model: "gpt-4o", ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2,
					}
					v2 := providerUITestApp(t, pi.ProductProviderList{Policy: pi.ProductRoutingBalanced})
					v2.connectionSaved = &v2Cfg
					v2.windowWidth = w
					trV2 := ui.NewTesterWith(v2.view, w, h, ui.TesterOptions{Dark: dark})
					trV2.SetLocale(locale)
					trV2.Frame()
					assertNoInternalIDs(trV2, "0acc-v2import")
					assertInViewport(trV2, "0acc-v2import", msgs.ProviderV2ImportTitle())

					pool := visualGuardSeededPool()
					pool.SavedRevision = &rev2
					dir := providerUITestApp(t, pool)
					dir.windowWidth = w
					trDir := ui.NewTesterWith(dir.view, w, h, ui.TesterOptions{Dark: dark})
					trDir.SetLocale(locale)
					trDir.Frame()
					assertNoInternalIDs(trDir, "2acc-folded")
					assertInViewport(trDir, "2acc-folded", msgs.ProviderApplyChanges(), msgs.ProviderFoldSummary(10))

					foldRect, okFold := trDir.Find(msgs.ProviderFoldSummary(10))
					if !okFold {
						t.Fatal("missing folded provider summary control")
					}
					foldTheme := theme.SettingsTheme(theme.ResolveWorkbenchTheme(dark).Theme)
					foldImg := trDir.Image()
					foldBg := foldImg.RGBAAt(int(foldRect.X+4), int(foldRect.Y+4))
					wantBg := foldTheme.Surface
					if !closeRGBA([4]uint8{foldBg.R, foldBg.G, foldBg.B, foldBg.A}, [4]uint8{wantBg.R, wantBg.G, wantBg.B, wantBg.A}) {
						t.Fatalf("folded provider summary bg = %+v, want %+v", foldBg, wantBg)
					}
					bestContrast := 0.0
					scanX0 := max(0, int(foldRect.X+foldRect.W)-140)
					scanX1 := min(foldImg.Bounds().Max.X, int(foldRect.X+foldRect.W)-8)
					for y := max(0, int(foldRect.Y+6)); y < min(foldImg.Bounds().Max.Y, int(foldRect.Y+foldRect.H)-6); y++ {
						for x := scanX0; x < scanX1; x++ {
							c := foldImg.RGBAAt(x, y)
							if ratio := theme.PaletteContrast(ui.RGB(c.R, c.G, c.B), foldTheme.Surface); ratio > bestContrast {
								bestContrast = ratio
							}
						}
					}
					if bestContrast < 4.5 {
						t.Fatalf("folded provider summary label contrast = %.2f, want >= 4.5 (rect=%+v)", bestContrast, foldRect)
					}

					applyBeforeRect, okApply := trDir.Find(msgs.ProviderApplyChanges())
					if !okApply {
						t.Fatal("pending Apply provider changes CTA missing")
					}
					applyFillBefore := cloneRGBA(trDir.Image()).RGBAAt(int(applyBeforeRect.X+4), int(applyBeforeRect.Y+4))

					dir.providers.catalogFolds = map[string]bool{"cloud": false}
					trDir.Frame()
					assertNoInternalIDs(trDir, "2acc-expanded")

					dir.selectProvider(pool.Accounts[0])
					trDir.Frame()
					assertNoInternalIDs(trDir, "modal-saved")
					assertInViewport(trDir, "modal-saved",
						msgs.CreateBotCancel(),
						msgs.ProviderSave(),
						msgs.ProviderSaveApply(),
						msgs.ProviderDeleteAccount("OpenAI Studio"),
					)
					applyDuringRect, okDuring := trDir.Find(msgs.ProviderApplyChanges())
					if okDuring {
						applyFillDuring := cloneRGBA(trDir.Image()).RGBAAt(int(applyDuringRect.X+4), int(applyDuringRect.Y+4))
						if applyFillDuring == applyFillBefore {
							t.Fatalf("opening provider modal did not demote background Apply CTA: before=%+v during=%+v", applyFillBefore, applyFillDuring)
						}
					}

					dir.dismissProviderDialog()
					dir.addProvider()
					trDir.Frame()
					assertNoInternalIDs(trDir, "modal-new")
					assertInViewport(trDir, "modal-new", msgs.CreateBotCancel(), msgs.ProviderSave(), msgs.ProviderSaveApply())

					dir.dismissProviderDialog()
					dir.providers.tab = providerTabRouting
					trDir.Frame()
					assertNoInternalIDs(trDir, "routing")
					assertInViewport(trDir, "routing", msgs.ProviderApplyChanges())
				})
			}
		}
	}
}

func TestVisualGuardBrandSVGAssetsAndThemeContrast(t *testing.T) {
	cat := connection.LoadProviderCatalog("")
	if len(cat.Warnings) != 0 {
		t.Fatalf("built-in catalog warnings: %v", cat.Warnings)
	}
	fallbackSVG, err := ui.ParseSVG(cat.FallbackIconSVG)
	if err != nil || len(fallbackSVG.Unsupported()) != 0 {
		t.Fatalf("fallback custom.svg parse failed: err=%v unsupported=%v", err, fallbackSVG.Unsupported())
	}

	if len(cat.Sections) != 2 || cat.Sections[0].ID != "local" || cat.Sections[1].ID != "cloud" {
		t.Fatalf("section ordering mismatch: %+v", cat.Sections)
	}
	var gotLocal, gotCloud []string
	for _, p := range cat.SectionProviders("local") {
		gotLocal = append(gotLocal, p.ID)
	}
	for _, p := range cat.SectionProviders("cloud") {
		gotCloud = append(gotCloud, p.ID)
	}
	wantLocal := []string{"ollama", "lmstudio", "custom"}
	wantCloud := []string{"openai", "google", "deepseek", "xai", "moonshot", "zhipu", "minimax", "volcengine", "qwen", "openrouter"}
	if !slices.Equal(gotLocal, wantLocal) {
		t.Fatalf("local presets = %v, want %v", gotLocal, wantLocal)
	}
	if !slices.Equal(gotCloud, wantCloud) {
		t.Fatalf("cloud presets = %v, want %v", gotCloud, wantCloud)
	}
	for _, preset := range cat.Presets {
		parsed, err := ui.ParseSVG(preset.IconSVG)
		if err != nil || len(parsed.Unsupported()) != 0 {
			t.Fatalf("preset %q SVG %q unsupported or invalid: err=%v unsupported=%v", preset.ID, preset.Icon, err, parsed.Unsupported())
		}
	}

	for _, tc := range []struct {
		name string
		dark bool
	}{{"Paper Light", false}, {"Moss Dark", true}} {
		wb := theme.ResolveWorkbenchTheme(tc.dark)
		st := theme.SettingsTheme(wb.Theme)
		for _, surface := range []struct {
			name  string
			color ui.Color
		}{
			{"Settings.Background", st.Background},
			{"Settings.Surface", st.Surface},
			{"Settings.SurfaceHover", st.SurfaceHover},
			{"Settings.SurfacePressed", st.SurfacePressed},
			{"Workbench.Reading", wb.Reading},
		} {
			for _, ink := range []struct {
				name  string
				color ui.Color
			}{
				{"Text", st.Text},
				{"TextMuted", st.TextMuted},
				{"Success", st.Success},
				{"Warning", st.Warning},
				{"Danger", st.Danger},
			} {
				if ratio := theme.PaletteContrast(ink.color, surface.color); ratio < 4.5 {
					t.Fatalf("%s: %s on %s contrast = %.2f, want >= 4.5", tc.name, ink.name, surface.name, ratio)
				}
			}
		}
		if ratio := theme.PaletteContrast(st.AccentText, st.Accent); ratio < 4.5 {
			t.Fatalf("%s: AccentText on Accent contrast = %.2f, want >= 4.5", tc.name, ratio)
		}
	}
}

func TestVisualGuardStaticThemePrimitivesAndLocaleParity(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbiddenRaw := map[string]bool{
		"TextInputBase": true,
		"TextAreaBase":  true,
		"SelectBase":    true,
		"SwitchBase":    true,
	}
	fset := token.NewFileSet()
	composerTextAreaCount := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.Ident:
				if n.Name == "providerPresets" {
					pos := fset.Position(n.Pos())
					t.Errorf("hardcoded providerPresets symbol forbidden in %s:%d", pos.Filename, pos.Line)
				}
			case *ast.SelectorExpr:
				ident, ok := n.X.(*ast.Ident)
				if !ok || ident.Name != "ui" || !forbiddenRaw[n.Sel.Name] {
					return true
				}
				if name == "view.go" && n.Sel.Name == "TextAreaBase" {
					composerTextAreaCount++
					return true
				}
				pos := fset.Position(n.Pos())
				t.Errorf("raw ui.%s forbidden outside internal/theme (found at %s:%d)", n.Sel.Name, pos.Filename, pos.Line)
			}
			return true
		})
	}
	if composerTextAreaCount != 1 {
		t.Fatalf("allowlisted composer ui.TextAreaBase count in view.go = %d, want 1", composerTextAreaCount)
	}

	readCatalog := func(path string) map[string]string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		out := make(map[string]string, len(doc))
		for k, v := range doc {
			if strings.HasPrefix(k, "@") {
				continue
			}
			s, ok := v.(string)
			if !ok {
				t.Fatalf("locale %s key %q is not a string", path, k)
			}
			out[k] = s
		}
		return out
	}

	root := filepath.Join("..", "..", "locales")
	en := readCatalog(filepath.Join(root, "en.json"))
	zh := readCatalog(filepath.Join(root, "zh-Hans.json"))
	if len(en) != len(zh) {
		t.Fatalf("locale key counts differ: en=%d zh-Hans=%d", len(en), len(zh))
	}
	for key, enVal := range en {
		zhVal, ok := zh[key]
		if !ok {
			t.Errorf("key %q present in en.json but missing in zh-Hans.json", key)
			continue
		}
		enPH := extractICUPlaceholders(enVal)
		zhPH := extractICUPlaceholders(zhVal)
		if !slices.Equal(enPH, zhPH) {
			t.Errorf("placeholder mismatch for key %q: en=%v zh-Hans=%v", key, enPH, zhPH)
		}
	}
	for key := range zh {
		if _, ok := en[key]; !ok {
			t.Errorf("key %q present in zh-Hans.json but missing in en.json", key)
		}
	}
}

func extractICUPlaceholders(msg string) []string {
	seen := make(map[string]struct{})
	var walk func(s string)
	walk = func(s string) {
		for i := 0; i < len(s); i++ {
			if s[i] != '{' {
				continue
			}
			depth := 0
			end := -1
			for j := i; j < len(s); j++ {
				if s[j] == '{' {
					depth++
				} else if s[j] == '}' {
					depth--
					if depth == 0 {
						end = j
						break
					}
				}
			}
			if end < 0 {
				break
			}
			inner := s[i+1 : end]
			if comma := strings.IndexByte(inner, ','); comma >= 0 {
				name := strings.TrimSpace(inner[:comma])
				if name != "" {
					seen[name] = struct{}{}
				}
				rest := inner[comma+1:]
				if secondComma := strings.IndexByte(rest, ','); secondComma >= 0 {
					branches := rest[secondComma+1:]
					for k := 0; k < len(branches); k++ {
						if branches[k] != '{' {
							continue
						}
						bDepth := 0
						bEnd := -1
						for m := k; m < len(branches); m++ {
							if branches[m] == '{' {
								bDepth++
							} else if branches[m] == '}' {
								bDepth--
								if bDepth == 0 {
									bEnd = m
									break
								}
							}
						}
						if bEnd < 0 {
							break
						}
						walk(branches[k+1 : bEnd])
						k = bEnd
					}
				}
			} else if name := strings.TrimSpace(inner); name != "" {
				seen[name] = struct{}{}
			}
			i = end
		}
	}
	walk(msg)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestVisualGuardInputBorderDisciplineAST(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checkedCalls := 0
	isFocusExpr := func(expr ast.Expr, focusVars map[string]bool) bool {
		switch e := expr.(type) {
		case *ast.SelectorExpr:
			return e.Sel.Name == "Focus"
		case *ast.Ident:
			return focusVars[e.Name]
		}
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			focusVars := make(map[string]bool)
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.AssignStmt:
					for i, rhs := range n.Rhs {
						if i < len(n.Lhs) {
							if id, ok := n.Lhs[i].(*ast.Ident); ok && isFocusExpr(rhs, focusVars) {
								focusVars[id.Name] = true
							}
						}
					}
				case *ast.CallExpr:
					var callee string
					switch fun := n.Fun.(type) {
					case *ast.Ident:
						callee = fun.Name
					case *ast.SelectorExpr:
						if pkg, ok := fun.X.(*ast.Ident); ok && pkg.Name == "theme" {
							callee = fun.Sel.Name
						}
					}
					switch callee {
					case "preferenceInput", "formTextArea", "PreferenceInput", "FormTextArea":
						checkedCalls++
						for _, arg := range n.Args[min(3, len(n.Args)):] {
							if isFocusExpr(arg, focusVars) {
								pos := fset.Position(arg.Pos())
								t.Errorf("%s:%d: %s must not use .Focus as resting input border; use a.workTheme.InputBorder", pos.Filename, pos.Line, callee)
							}
						}
					}
				}
				return true
			})
		}
	}
	if checkedCalls < 10 {
		t.Fatalf("expected >= 10 preferenceInput/formTextArea call sites, scanned %d", checkedCalls)
	}
}

func TestVisualGuardStyleFrostedModalAST(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	dialogCount := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "ui" || sel.Sel.Name != "DialogBase" {
				return true
			}
			dialogCount++
			pos := fset.Position(call.Pos())
			if len(call.Args) < 3 {
				t.Errorf("%s:%d: ui.DialogBase missing modal callback argument", pos.Filename, pos.Line)
				return true
			}
			lit, ok := call.Args[2].(*ast.FuncLit)
			if !ok || lit.Body == nil {
				t.Errorf("%s:%d: ui.DialogBase callback must be a literal calling theme.StyleFrostedModal", pos.Filename, pos.Line)
				return true
			}
			panelParam := ""
			if lit.Type != nil && lit.Type.Params != nil {
				var params []string
				for _, field := range lit.Type.Params.List {
					for _, id := range field.Names {
						params = append(params, id.Name)
					}
				}
				if len(params) >= 2 {
					panelParam = params[1]
				}
			}
			hasStyleFrostedModal := false
			ast.Inspect(lit.Body, func(inner ast.Node) bool {
				ic, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				isel, ok := ic.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if ipkg, ok := isel.X.(*ast.Ident); ok && ipkg.Name == "theme" {
					if isel.Sel.Name == "StyleFrostedModal" {
						hasStyleFrostedModal = true
					}
					if isel.Sel.Name == "StyleFrostedModalBackdrop" {
						ipos := fset.Position(ic.Pos())
						t.Errorf("%s:%d: ui.DialogBase modal must call theme.StyleFrostedModal instead of theme.StyleFrostedModalBackdrop", ipos.Filename, ipos.Line)
					}
				}
				if panelParam != "" && isel.Sel.Name == "Radius" {
					var root ast.Expr = isel.X
					for {
						switch r := root.(type) {
						case *ast.CallExpr:
							if rs, ok := r.Fun.(*ast.SelectorExpr); ok {
								root = rs.X
								continue
							}
						case *ast.Ident:
							if r.Name == panelParam {
								ipos := fset.Position(ic.Pos())
								t.Errorf("%s:%d: ui.DialogBase modal panel must not override Radius outside theme.StyleFrostedModal", ipos.Filename, ipos.Line)
							}
						}
						break
					}
				}
				return true
			})
			if !hasStyleFrostedModal {
				t.Errorf("%s:%d: ui.DialogBase modal callback must call theme.StyleFrostedModal", pos.Filename, pos.Line)
			}
			return true
		})
	}
	if dialogCount < 4 {
		t.Fatalf("expected >= 4 ui.DialogBase modals in internal/app, found %d", dialogCount)
	}
}

func phase2SeededCapabilitiesProfileApp(t *testing.T) (*appModel, *contact, pi.ProductSkillSummary, pi.ProductSkillApprovalRequest) {
	t.Helper()
	a, ct := capabilitiesUITestApp(t)
	ct.Loaded = true
	readOnlyTrue := true
	destructiveTrue := true
	poolPID := 4242

	globalSummary := pi.ProductSkillSummary{
		Source:      "global",
		SkillID:     "field-guide",
		Name:        "Field guide",
		Description: "Collect structured intake notes and workspace context.",
		SHA256:      "sha-global-1",
	}
	localSummary := pi.ProductSkillSummary{
		Source:      "local",
		SkillID:     "local-ref",
		Name:        "Local reference",
		Description: "Read-only discovered local project guide.",
		SHA256:      "sha-local-1",
	}
	privateSkill := pi.ProductSkillSummary{
		Source:             "private",
		SkillID:            "case-script",
		Name:               "Case files script",
		Description:        "Summarize case workspace files for review.",
		SHA256:             "sha-private-1",
		ApprovalDigest:     "digest-abc123456789",
		PermissionsDigest:  "perm-xyz",
		ApprovalStatus:     "approval_required",
		CanonicalWorkspace: "/tmp/canonical-ws",
		Scripts: map[string]pi.ProductSkillScript{
			"run": {
				Entry:       "scripts/run.mjs",
				Description: "Summarize workspace",
				TimeoutMS:   10000,
				Permissions: pi.ProductSkillPermissions{WorkspaceRead: true, NetworkOrigins: []string{"http://127.0.0.1:58423"}},
			},
		},
	}

	globalKey := pi.ProductSkillKey{Source: "global", SkillID: "field-guide"}
	localKey := pi.ProductSkillKey{Source: "local", SkillID: "local-ref"}
	a.capabilities.discoverLocal = true
	a.capabilities.library = capabilityRead[pi.ProductSkillList]{
		ready: true,
		value: pi.ProductSkillList{
			DiscoverLocalSkills: true,
			Global:              []pi.ProductSkillSummary{globalSummary},
			Local:               []pi.ProductSkillSummary{localSummary},
		},
	}
	a.capabilities.selectedSkill = globalKey
	a.capabilities.skillDetails = map[pi.ProductSkillKey]*skillDetailRead{
		globalKey: {
			digest: "sha-global-1",
			capabilityRead: capabilityRead[pi.ProductSkillDetail]{
				ready: true,
				value: pi.ProductSkillDetail{
					ProductSkillSummary: globalSummary,
					Content:             "## Intake checklist\n\n- Confirm workspace path\n- Capture reproducible evidence",
				},
			},
		},
		localKey: {
			digest: "sha-local-1",
			capabilityRead: capabilityRead[pi.ProductSkillDetail]{
				ready: true,
				value: pi.ProductSkillDetail{
					ProductSkillSummary: localSummary,
					Content:             "Local read-only skill body.",
				},
			},
		},
	}

	serverView := pi.ProductMcpServerView{
		ProductMcpServerConfig: pi.ProductMcpServerConfig{
			ServerID:         "mcp-studio",
			Name:             "studio-tools",
			Transport:        "http",
			Isolation:        "shared",
			Enabled:          true,
			URL:              "https://mcp.example.invalid/mcp",
			SecretHeaderKeys: []string{"authorization"},
		},
		Revision:         3,
		CredentialStatus: "ok",
		Catalog: &pi.ProductMcpCatalog{
			FetchedAt: "2026-11-26T09:30:00Z",
			Tools: []pi.ProductMcpTool{
				{Name: "list_docs", Description: "List workspace docs", ReadOnlyHint: &readOnlyTrue},
				{Name: "tag_doc", Description: "Apply classification tags"},
				{Name: "purge_doc", Description: "Delete archived document", DestructiveHint: &destructiveTrue},
			},
		},
		Pools: []pi.ProductMcpPool{
			{ServerID: "mcp-studio", ScopeKey: "shared:mcp-studio", Isolation: "shared", State: "connected", RefCount: 2, PID: &poolPID},
		},
	}
	a.capabilities.servers = capabilityRead[[]pi.ProductMcpServerView]{ready: true, value: []pi.ProductMcpServerView{serverView}}
	a.capabilities.selectedMcp = serverView.ServerID
	a.capabilities.mcpEditors = map[string]*mcpEditor{serverView.ServerID: newMcpEditor(serverView)}

	customTools := []string{"read", "bash"}
	ct.ToolPreset = pi.ToolPresetCustom
	ct.BuiltinTools = &customTools
	ct.MountedSkills = []pi.ProductMountedSkill{
		{Source: "global", SkillID: "field-guide"},
		{Source: "global", SkillID: "missing-skill"},
	}
	ct.McpBindings = []pi.ProductMcpBinding{
		{ServerID: "mcp-studio", Policy: "allowlist", AllowedTools: []string{"list_docs"}},
	}
	ct.capabilities.skills = capabilityRead[pi.ProductSkillList]{
		ready: true,
		value: pi.ProductSkillList{
			Global:  []pi.ProductSkillSummary{globalSummary},
			Private: []pi.ProductSkillSummary{privateSkill},
		},
	}
	req := pi.ProductSkillApprovalRequest{
		RequestID:          "req-p2-1",
		ActivityID:         "act-p2-1",
		BotID:              ct.ID,
		Source:             "private",
		SkillID:            "case-script",
		Script:             "run",
		State:              "open",
		Revision:           1,
		InstallationID:     "inst-p2-1",
		ApprovalDigest:     "digest-abc123456789",
		PermissionsDigest:  "perm-xyz",
		CanonicalWorkspace: "/tmp/canonical-ws",
	}
	ct.capabilities.requests = map[string]pi.ProductSkillApprovalRequest{req.RequestID: req}
	ct.memory = botMemoryViewState{
		loaded: true,
		data: pi.ProductBotMemory{
			BotID:         ct.ID,
			RulesRevision: 2,
			PendingLearnings: []pi.ProductPendingLearning{
				{
					LearningID: "learn-p2-1",
					BotID:      ct.ID,
					Kind:       "rule",
					Text:       "Always verify theme switch contrast on the first rendered frame.",
					Reason:     "Caught during Phase 2 Design System review.",
					State:      "pending",
				},
			},
			Rules: []pi.ProductMemoryRule{
				{RuleID: "rule-p2-1", BotID: ct.ID, Text: "Keep all frosted modals unified at 16 DIP radius.", Source: "user"},
			},
			WorkLogs: []pi.ProductWorkLog{
				{LogID: "log-p2-1", BotID: ct.ID, Date: "2026-11-26", Summary: "Completed DS2 Phase 2 Capabilities and Bot Profile alignment.", Tags: []string{"ds2", "qa"}},
			},
		},
	}
	a.profileEditors = map[string]*contactProfileEditor{ct.ID: newContactProfileEditor(contactProfileDraft(ct))}
	return a, ct, privateSkill, req
}

func TestVisualGuardCapabilitiesProfileAndModals(t *testing.T) {
	for _, tc := range []struct {
		name string
		dark bool
	}{{"Paper Light", false}, {"Moss Dark", true}} {
		wb := theme.ResolveWorkbenchTheme(tc.dark)
		st := theme.SettingsTheme(wb.Theme)
		for _, tset := range []*ui.Theme{wb.Theme, &st} {
			for _, factor := range []float32{.12, .18, .24} {
				bg := tset.Surface.Mix(tset.Accent, factor)
				if ratio := theme.PaletteContrast(tset.Text, bg); ratio < 4.5 {
					t.Fatalf("%s: memoryApproveButton Text on Surface.Mix(Accent, %.2f) contrast = %.2f, want >= 4.5", tc.name, factor, ratio)
				}
			}
		}
	}

	closeRGBA := func(a, b [4]uint8) bool {
		for i := range 4 {
			d := int(a[i]) - int(b[i])
			if d < 0 {
				d = -d
			}
			if d > 4 {
				return false
			}
		}
		return true
	}

	sizes := [][2]int{{1040, 720}, {720, 640}}
	for _, locale := range []string{"en", "zh-Hans"} {
		msgs := locales.In(locale)
		for _, dark := range []bool{false, true} {
			wb := theme.ResolveWorkbenchTheme(dark)
			st := theme.SettingsTheme(wb.Theme)
			accentRGBA := [4]uint8{st.Accent.R, st.Accent.G, st.Accent.B, st.Accent.A}
			for _, size := range sizes {
				tag := fmt.Sprintf("%s-dark%t-%dx%d", locale, dark, size[0], size[1])
				t.Run(tag, func(t *testing.T) {
					w, h := size[0], size[1]
					assertControl := func(tr *ui.Tester, scene string, labels ...string) {
						t.Helper()
						for _, label := range labels {
							r, ok := tr.Find(label)
							if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(w)+0.5 || r.Y+r.H > float32(h)+0.5 || r.W < theme.ControlHit || r.H < theme.ControlHit {
								t.Fatalf("scene=%s control %q invalid/clipped: rect=%+v ok=%v viewport=%dx%d", scene, label, r, ok, w, h)
							}
						}
					}
					assertFillIsAccent := func(tr *ui.Tester, scene, label string, want bool) {
						t.Helper()
						r, ok := tr.Find(label)
						if !ok {
							t.Fatalf("scene=%s missing CTA %q", scene, label)
						}
						c := tr.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+3))
						got := [4]uint8{c.R, c.G, c.B, c.A}
						isAccent := closeRGBA(got, accentRGBA)
						if isAccent != want {
							t.Fatalf("scene=%s CTA %q accent fill = %v (got=%v accent=%v), want %v", scene, label, isAccent, got, accentRGBA, want)
						}
					}
					assertRowInputAligned := func(tr *ui.Tester, scene, label string) {
						t.Helper()
						r, ok := tr.Find(label)
						if !ok {
							t.Fatalf("scene=%s missing form row label %q", scene, label)
						}
						tr.ClickAt(r.X+r.W+24, r.Y+r.H/2)
						tr.Frame()
						caret, okC := tr.TextCaret()
						if !okC {
							t.Fatalf("scene=%s row %q input had no active caret", scene, label)
						}
						labelMidY := r.Y + r.H/2
						controlMidY := caret.Y + caret.H/2
						if delta := math.Abs(float64(labelMidY - controlMidY)); delta > 4.0 {
							t.Fatalf("scene=%s row %q misaligned: labelMidY=%.2f controlMidY=%.2f (delta=%.2f > 4)", scene, label, labelMidY, controlMidY, delta)
						}
					}

					a, ct, privateSkill, req := phase2SeededCapabilitiesProfileApp(t)
					a.windowWidth = w
					a.settingsOpen = true
					a.settingsTab = settingsTabCapabilities
					a.capabilities.tab = capabilityTabSkills
					tr := ui.NewTesterWith(a.view, w, h, ui.TesterOptions{Dark: dark})
					tr.SetLocale(locale)
					tr.SetFocused(true)
					tr.Frame()
					assertControl(tr, "skills", msgs.CapLocalDiscovery(), msgs.CapNewSkill(), msgs.CapEdit(), msgs.CapDeleteSkill())

					a.capabilities.newSkill = &skillEditor{key: pi.ProductSkillKey{Source: "global"}, name: "Audit guide", summary: "Verify DS2 forms."}
					tr.Frame()
					assertControl(tr, "new-skill-modal", msgs.CreateBotCancel(), msgs.CapSaveSkill())
					assertFillIsAccent(tr, "new-skill-modal", msgs.CapSaveSkill(), true)
					if w >= 1040 {
						assertRowInputAligned(tr, "new-skill-modal", msgs.FieldName())
						assertRowInputAligned(tr, "new-skill-modal", msgs.CapSkillSlug())
						assertRowInputAligned(tr, "new-skill-modal", msgs.CapDescription())
					}
					a.capabilities.newSkill = nil

					a.capabilities.tab = capabilityTabMCP
					a.settingsScroll.Y = 0
					tr.Frame()
					assertControl(tr, "mcp-detail", msgs.ProviderAccountStatus(), msgs.CapMcpTestRefresh())
					if w >= 1040 {
						assertControl(tr, "mcp-detail-footer", msgs.CapDeleteMcp(), msgs.CapSaveMcp())
						assertFillIsAccent(tr, "mcp-detail", msgs.CapSaveMcp(), true)
					}

					a.capabilities.newMcp = newMcpEditor(pi.ProductMcpServerView{
						ProductMcpServerConfig: pi.ProductMcpServerConfig{Transport: "http", Isolation: "shared", Enabled: true},
					})
					tr.Frame()
					if w >= 1040 {
						assertFillIsAccent(tr, "add-mcp-modal-bg-demoted", msgs.CapSaveMcp(), false)
					}
					savedServers, savedSelected := a.capabilities.servers.value, a.capabilities.selectedMcp
					a.capabilities.servers.value, a.capabilities.selectedMcp = nil, ""
					tr.Frame()
					assertControl(tr, "add-mcp-modal", msgs.CreateBotCancel(), msgs.CapSaveMcp())
					assertFillIsAccent(tr, "add-mcp-modal", msgs.CapSaveMcp(), true)
					if w >= 1040 {
						assertRowInputAligned(tr, "add-mcp-modal", msgs.FieldName())
						assertRowInputAligned(tr, "add-mcp-modal", msgs.CapServerURL())
						for _, pair := range [][2]string{{msgs.CapTransport(), "stdio"}, {msgs.CapIsolation(), msgs.CapShared()}} {
							lr, okL := tr.Find(pair[0])
							cr, okC := tr.Find(pair[1])
							if !okL || !okC {
								t.Fatalf("add-mcp-modal missing row %q(%v) or control %q(%v)", pair[0], okL, pair[1], okC)
							}
							if delta := math.Abs(float64((lr.Y + lr.H/2) - (cr.Y + cr.H/2))); delta > 4.0 {
								t.Fatalf("add-mcp-modal row %q misaligned: labelMidY=%.2f controlMidY=%.2f (delta=%.2f > 4)",
									pair[0], lr.Y+lr.H/2, cr.Y+cr.H/2, delta)
							}
						}
					}
					a.capabilities.servers.value, a.capabilities.selectedMcp = savedServers, savedSelected
					a.capabilities.newMcp = nil

					a.settingsOpen = false
					a.contactProfileID = ct.ID
					a.openDetail(detailProfile)
					a.profileScrollFor(ct.ID).Y = 0
					tr.Frame()
					assertControl(tr, "bot-profile-footer", msgs.ContactSaveChanges(), msgs.ProfileArchive())
					assertFillIsAccent(tr, "bot-profile-footer", msgs.ContactSaveChanges(), true)

					scrollProfileTo := func(label string) ui.Rect {
						t.Helper()
						for y := float32(0); y <= 2600; y += 180 {
							a.profileScrollFor(ct.ID).Y = y
							tr.Frame()
							if r, ok := tr.Find(label); ok && r.W >= theme.ControlHit && r.H >= theme.ControlHit && r.Y >= 56 && r.Y+r.H <= float32(h)-90 {
								return r
							}
						}
						t.Fatalf("bot-profile could not scroll %q into visible viewport (%dx%d)", label, w, h)
						return ui.Rect{}
					}

					scrollProfileTo("read")
					assertControl(tr, "bot-profile-custom-tools", "read", "write")

					scrollProfileTo("list_docs")
					assertControl(tr, "bot-profile-mcp-allowlist", "list_docs")

					approveRect := scrollProfileTo(msgs.MemoryApprove())
					assertControl(tr, "bot-profile-memory", msgs.MemoryApprove(), msgs.MemoryReject())
					assertFillIsAccent(tr, "bot-profile-memory", msgs.MemoryApprove(), false)

					if w == 1040 {
						tr.SetDark(!dark)
						otherWB := theme.ResolveWorkbenchTheme(!dark)
						wantFill := otherWB.Theme.Surface.Mix(otherWB.Theme.Accent, .12)
						gotC := tr.Image().RGBAAt(int(approveRect.X+approveRect.W/2), int(approveRect.Y+3))
						if !closeRGBA([4]uint8{gotC.R, gotC.G, gotC.B, gotC.A}, [4]uint8{wantFill.R, wantFill.G, wantFill.B, wantFill.A}) {
							t.Fatalf("memoryApproveButton did not snap on first SetDark(%v) frame: got=%+v want=%+v", !dark, gotC, wantFill)
						}
						tr.SetDark(dark)
						tr.Advance(theme.ControlTransition.Duration)
						tr.Frame()
					}

					a.capabilities.review = &skillReviewState{
						key:     pi.ProductSkillKey{Source: "private", BotID: ct.ID, SkillID: "case-script"},
						name:    privateSkill.Name,
						request: &req,
						file:    "scripts/run.mjs",
						read: capabilityRead[pi.ProductSkillDetail]{
							ready: true,
							value: pi.ProductSkillDetail{
								ProductSkillSummary: privateSkill,
								Files: map[string]string{
									"SKILL.md":        "---\nname: Case files script\n---\n",
									"scripts/run.mjs": "export default async function() { return { ok: true }; }\n",
								},
							},
						},
					}
					tr.Frame()
					assertControl(tr, "skill-review-modal-footer", msgs.CreateBotCancel(), msgs.CapApproveBot())
					assertFillIsAccent(tr, "skill-review-modal", msgs.CapApproveBot(), true)
					a.capabilities.review.scroll.Y = 240
					tr.Frame()
					assertControl(tr, "skill-review-modal-body", msgs.CopyCodeLabel())
					a.capabilities.review = nil

					a.closeDetail()
					a.openCreateBotDialog()
					tr.Frame()
					assertControl(tr, "create-bot-modal", msgs.CreateBotCancel(), msgs.CreateBotAction())
					assertFillIsAccent(tr, "create-bot-modal", msgs.CreateBotAction(), true)
					a.createBot = nil

					a.openComposerMaterial(ct, materials.Item{})
					tr.Frame()
					assertControl(tr, "composer-material-modal", msgs.TaskContextFormCancel(), msgs.ComposerAddNoteAction())
					assertFillIsAccent(tr, "composer-material-modal", msgs.ComposerAddNoteAction(), true)
					a.composerMaterial = nil
				})
			}
		}
	}
}

type phase3SeededControlPlane struct {
	app           *appModel
	contact       *contact
	questionAct   *pi.ProductActivity
	approvalAct   *pi.ProductActivity
	completedAct  *pi.ProductActivity
	autoAnswerAct *pi.ProductActivity
	steerAct      *pi.ProductActivity
}

func phase3StubProductHost(t *testing.T, a *appModel, pool pi.ProductProviderList) *piAppHost {
	t.Helper()
	dir := t.TempDir()
	if a != nil && a.storePath == "" {
		a.storePath = filepath.Join(dir, "contacts.json")
	}
	stub := filepath.Join(dir, "stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	client, err := pi.StartProduct(pi.ProductOptions{
		NodePath:        "/bin/sh",
		WorkerPath:      stub,
		StateDir:        dir,
		ProfilePath:     filepath.Join(dir, "profile.sqlite"),
		CredentialsPath: filepath.Join(dir, "connection.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = client.Kill(ctx)
		<-client.CallbacksDone()
	})
	return &piAppHost{
		generation:       1,
		product:          client,
		productProviders: pool,
	}
}

func phase3SeededControlPlaneApp(t *testing.T) phase3SeededControlPlane {
	t.Helper()
	a, ct := workspaceTestApp(t)
	a.demo = false
	a.runtimeStatus = runtimeStateReady
	a.connectionGeneration = 1
	host := phase3StubProductHost(t, a, visualGuardSeededPool())
	a.pi = host
	client := host.product
	ct.Name = "Cedar"
	ct.Draft = "Keep this conversation draft"
	ct.SessionID = "conv-cp-origin"
	ct.Loaded = true
	a.profileEditors = map[string]*contactProfileEditor{ct.ID: newContactProfileEditor(contactProfileDraft(ct))}

	allowFalse := false
	okTrue := true
	nextCur := "trace-page-2"

	questionAct := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{
			ActivityID:           "act-cp-question",
			OwnerBotID:           ct.ID,
			OriginConversationID: ct.SessionID,
			Revision:             2,
			State:                "waitingForAnswer",
			Goal:                 "Verify the intake inventory",
			OpenQuestionIDs:      []string{"q-cp-1"},
			LatestProgress:       &pi.ProductActivityProgress{ExecutionID: "exec-q-1", ProgressSeq: 3, Stage: "Checking intake records", Step: 2, TotalSteps: 4},
		},
		Questions: []pi.ProductQuestion{{
			QuestionID:    "q-cp-1",
			ExecutionID:   "exec-q-1",
			Text:          "Which intake batch should I verify?",
			Revision:      1,
			State:         "open",
			AllowFreeText: &allowFalse,
			Options: []pi.ProductQuestionOption{
				{ID: "batch_14", Label: "Batch fourteen", Description: "Primary verified batch", Recommended: true},
				{ID: "batch_16", Label: "Batch sixteen", Description: "Fallback intake lot"},
			},
		}},
		Executions: []pi.ProductExecution{{ExecutionID: "exec-q-1", Revision: 1, State: "running"}},
	}

	approvalReq := pi.ProductSkillApprovalRequest{
		RequestID:          "req-cp-1",
		ActivityID:         "act-cp-approval",
		BotID:              ct.ID,
		Source:             "private",
		SkillID:            "verify-intake",
		Script:             "check",
		State:              "open",
		Revision:           1,
		InstallationID:     "inst-cp-1",
		ApprovalDigest:     "digest-cp-1",
		PermissionsDigest:  "perm-cp-1",
		CanonicalWorkspace: "/tmp/ws",
	}
	approvalAct := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{
			ActivityID:                "act-cp-approval",
			OwnerBotID:                ct.ID,
			OriginConversationID:      ct.SessionID,
			Revision:                  2,
			State:                     "waitingForAnswer",
			Goal:                      "Verify source documents",
			PendingApprovalRequestIDs: []string{approvalReq.RequestID},
		},
		ApprovalRequests: []pi.ProductSkillApprovalRequest{approvalReq},
		Executions:       []pi.ProductExecution{{ExecutionID: "exec-app-1", Revision: 1, State: "running"}},
	}

	completedAct := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{
			ActivityID:           "act-cp-done",
			OwnerBotID:           ct.ID,
			OriginConversationID: ct.SessionID,
			Revision:             4,
			State:                "completed",
			Goal:                 "Summarize supplier audit",
			LatestProgress:       &pi.ProductActivityProgress{ExecutionID: "exec-done-1", ProgressSeq: 5, Stage: "Finished verification", Step: 4, TotalSteps: 4},
		},
		Executions: []pi.ProductExecution{{ExecutionID: "exec-done-1", Revision: 2, State: "completed"}},
	}

	autoAnswerAct := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{
			ActivityID:           "act-cp-auto",
			OwnerBotID:           ct.ID,
			OriginConversationID: ct.SessionID,
			Revision:             3,
			State:                "running",
			Goal:                 "Format intake notes",
		},
		Questions: []pi.ProductQuestion{{
			QuestionID:       "q-cp-auto",
			ExecutionID:      "exec-auto-1",
			Text:             "Which date format should I use?",
			Revision:         2,
			State:            "answered",
			Provenance:       "bot_auto",
			SelectedOptionID: "iso_dates",
			Delivery:         "delivered",
			Answer:           &pi.ProductText{Text: "ISO dates"},
		}},
		Executions: []pi.ProductExecution{{ExecutionID: "exec-auto-1", Revision: 1, State: "running"}},
	}

	steerAct := &pi.ProductActivity{
		ProductActivitySummary: pi.ProductActivitySummary{
			ActivityID:           "act-cp-steer",
			OwnerBotID:           ct.ID,
			OriginConversationID: ct.SessionID,
			Revision:             5,
			State:                "running",
			Goal:                 "Compare supplier lots",
			LatestProgress:       &pi.ProductActivityProgress{ExecutionID: "exec-steer-1", ProgressSeq: 7, Stage: "Matching source lots", Step: 2, TotalSteps: 3},
		},
		Executions: []pi.ProductExecution{{ExecutionID: "exec-steer-1", Revision: 1, State: "running"}},
		Steers: []pi.ProductActivitySteer{
			{OperationID: "op-steer-1", SteerID: "steer-1", ActivityID: "act-cp-steer", ExecutionID: "exec-steer-1", State: "delivered", Revision: 5},
			{OperationID: "op-steer-2", SteerID: "steer-2", ActivityID: "act-cp-steer", ExecutionID: "exec-steer-1", State: "unknown", Revision: 5, Reason: "worker_lost"},
		},
	}

	ct.activeActivities = map[string]pi.ProductActivitySummary{
		questionAct.ActivityID:   questionAct.ProductActivitySummary,
		approvalAct.ActivityID:   approvalAct.ProductActivitySummary,
		completedAct.ActivityID:  completedAct.ProductActivitySummary,
		autoAnswerAct.ActivityID: autoAnswerAct.ProductActivitySummary,
		steerAct.ActivityID:      steerAct.ProductActivitySummary,
	}
	ct.activity = productActivityPanel{
		client:   client,
		revision: a.productActivityRevision(),
		loaded:   true,
		items: []pi.ProductActivitySummary{
			questionAct.ProductActivitySummary,
			steerAct.ProductActivitySummary,
			completedAct.ProductActivitySummary,
		},
		traces: map[pi.ProductActivityExecutionParams]*activityTraceState{
			productActivityExecution(steerAct.ActivityID, "exec-steer-1"): {
				open:     true,
				loaded:   true,
				client:   client,
				observed: 3,
				page: pi.ProductActivityTrace{
					ProductPage: pi.ProductPage[pi.ProductActivityTraceItem]{
						Items: []pi.ProductActivityTraceItem{
							{Seq: 1, ActivityID: steerAct.ActivityID, ExecutionID: "exec-steer-1", Kind: "tool_started", Tool: "activity_progress", Target: "Step 1 of 3"},
							{Seq: 2, ActivityID: steerAct.ActivityID, ExecutionID: "exec-steer-1", Kind: "tool_finished", Tool: "activity_progress", OK: &okTrue, Excerpt: "Matched 14 source rows"},
							{Seq: 3, ActivityID: steerAct.ActivityID, ExecutionID: "exec-steer-1", Kind: "file_confirmed", Path: "/tmp/ws/lots.md"},
						},
						NextCursor: &nextCur,
					},
				},
			},
		},
	}
	ct.activityNotices = activityNoticeState{
		client:  client,
		version: 1,
		loaded:  1,
		details: map[string]*pi.ProductActivity{
			questionAct.ActivityID:   questionAct,
			approvalAct.ActivityID:   approvalAct,
			completedAct.ActivityID:  completedAct,
			autoAnswerAct.ActivityID: autoAnswerAct,
		},
	}
	return phase3SeededControlPlane{
		app:           a,
		contact:       ct,
		questionAct:   questionAct,
		approvalAct:   approvalAct,
		completedAct:  completedAct,
		autoAnswerAct: autoAnswerAct,
		steerAct:      steerAct,
	}
}

func TestVisualGuardControlPlaneWorkbench(t *testing.T) {
	for _, tc := range []struct {
		name string
		dark bool
	}{{"Paper Light", false}, {"Moss Dark", true}} {
		wb := theme.ResolveWorkbenchTheme(tc.dark)
		th := wb.Theme
		for _, pair := range []struct {
			label string
			fg    ui.Color
			bg    ui.Color
		}{
			{"botStatusAction waiting", th.Warning, th.Surface},
			{"botStatusAction muted", th.TextMuted, th.Surface},
			{"capabilityBadge muted", th.TextMuted, th.Surface.Mix(th.TextMuted, .08)},
			{"capabilityBadge success", th.Success, th.Surface.Mix(th.Success, .08)},
			{"capabilityBadge warning", th.Warning, th.Surface.Mix(th.Warning, .08)},
			{"capabilityBadge danger", th.Danger, th.Surface.Mix(th.Danger, .08)},
			{"TextAction default", th.TextMuted, th.Surface},
			{"TextAction danger", th.Danger, th.Surface},
			{"TagPill unselected", th.TextMuted, th.Surface},
		} {
			if ratio := theme.PaletteContrast(pair.fg, pair.bg); ratio < 4.5 {
				t.Fatalf("%s %s contrast = %.2f, want >= 4.5", tc.name, pair.label, ratio)
			}
		}
		pillBg := th.Surface.Mix(th.Success, .09)
		if tc.dark {
			pillBg = th.Surface.Mix(th.Accent, .12)
		}
		if ratio := theme.PaletteContrast(th.Text, pillBg); ratio < 4.5 {
			t.Fatalf("%s TagPill selected contrast = %.2f, want >= 4.5", tc.name, ratio)
		}
	}

	for _, locale := range []string{"en", "zh-Hans"} {
		msgs := locales.In(locale)
		for _, dark := range []bool{false, true} {
			wb := theme.ResolveWorkbenchTheme(dark)
			accentRGBA := [4]uint8{wb.Theme.Accent.R, wb.Theme.Accent.G, wb.Theme.Accent.B, wb.Theme.Accent.A}

			t.Run(fmt.Sprintf("%s-dark%t-960x640-profile-activity", locale, dark), func(t *testing.T) {
				fx := phase3SeededControlPlaneApp(t)
				fx.app.windowWidth = 960
				fx.app.rightPaneWidth = 440
				fx.app.contactProfileID = fx.contact.ID
				fx.app.openDetail(detailProfile)
				fx.contact.activity.open = true
				fx.contact.activity.activityID = fx.steerAct.ActivityID
				fx.contact.activity.activity = fx.steerAct

				tr := ui.NewTesterWith(fx.app.view, 960, 640, ui.TesterOptions{Dark: dark})
				tr.SetLocale(locale)
				tr.SetFocused(true)
				tr.Frame()

				comp, ok := tr.Find(msgs.ComposerLabel(fx.contact.Name))
				if !ok || comp.W <= 0 || comp.H <= 0 || comp.X < 0 || comp.X+comp.W > 960.5 || comp.Y+comp.H > 640.5 {
					t.Fatalf("960x640 composer missing or clipped: ok=%v rect=%+v", ok, comp)
				}
				if netW := comp.W - 14; netW < 440 {
					t.Fatalf("960x640 profile+activity net editable composer width = %.1f DIP, want >= 440", netW)
				}
				if r, ok := tr.Find(msgs.ContactSaveChanges()); !ok || r.W < theme.ControlHit || r.H < theme.ControlHit || r.X+r.W > 960.5 {
					t.Fatalf("960x640 profile Save changes invalid: ok=%v rect=%+v", ok, r)
				}
				sendRect, okSend := tr.Find(msgs.SendAction())
				if !okSend || sendRect.W < theme.ControlHit || sendRect.H < theme.ControlHit {
					t.Fatalf("960x640 outlined Send missing: ok=%v rect=%+v", okSend, sendRect)
				}
				l1Dist := func(r, g, b uint8, target ui.Color) int {
					abs := func(v int) int {
						if v < 0 {
							return -v
						}
						return v
					}
					return abs(int(r)-int(target.R)) + abs(int(g)-int(target.G)) + abs(int(b)-int(target.B))
				}
				sendPx := tr.Image().RGBAAt(int(sendRect.X+sendRect.W/2), int(sendRect.Y))
				if dIn, dFocus := l1Dist(sendPx.R, sendPx.G, sendPx.B, wb.InputBorder), l1Dist(sendPx.R, sendPx.G, sendPx.B, wb.Theme.Focus); dIn >= dFocus {
					t.Fatalf("960x640 resting outlined Send border %+v is closer to Focus %+v (d=%d) than InputBorder %+v (d=%d)", sendPx, wb.Theme.Focus, dFocus, wb.InputBorder, dIn)
				}
				attachRect, okAttach := tr.Find(msgs.ComposerAttach())
				if !okAttach {
					t.Fatal("960x640 composer attach button missing")
				}
				tr.SetDark(!dark)
				otherComposer := theme.ResolveWorkbenchTheme(!dark)
				attachBg := tr.Image().RGBAAt(int(attachRect.X+6), int(attachRect.Y+6))
				wantAttachBg := [4]uint8{otherComposer.ComposerSurface.R, otherComposer.ComposerSurface.G, otherComposer.ComposerSurface.B, otherComposer.ComposerSurface.A}
				if !closeRGBA([4]uint8{attachBg.R, attachBg.G, attachBg.B, attachBg.A}, wantAttachBg) {
					t.Fatalf("attach button did not snap ComposerSurface on first SetDark(%v) frame: got=%+v want=%+v", !dark, attachBg, wantAttachBg)
				}
				tr.SetDark(dark)
				tr.Advance(theme.ControlTransition.Duration)
				tr.Frame()
			})

			for _, size := range [][2]int{{1040, 720}, {720, 640}} {
				w, h := size[0], size[1]
				t.Run(fmt.Sprintf("%s-dark%t-%dx%d", locale, dark, w, h), func(t *testing.T) {
					assertHit := func(tr *ui.Tester, scene string, labels ...string) {
						t.Helper()
						for _, label := range labels {
							r, ok := tr.Find(label)
							if !ok || r.X < -0.5 || r.Y < -0.5 || r.X+r.W > float32(w)+0.5 || r.Y+r.H > float32(h)+0.5 || r.W < theme.ControlHit || r.H < theme.ControlHit {
								t.Fatalf("scene=%s control %q invalid/overflow: ok=%v rect=%+v viewport=%dx%d", scene, label, ok, r, w, h)
							}
						}
					}
					assertNotAccentFill := func(tr *ui.Tester, scene string, labels ...string) {
						t.Helper()
						for _, label := range labels {
							r, ok := tr.Find(label)
							if !ok {
								t.Fatalf("scene=%s missing control %q", scene, label)
							}
							c := tr.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+3))
							if closeRGBA([4]uint8{c.R, c.G, c.B, c.A}, accentRGBA) {
								t.Fatalf("scene=%s non-primary control %q unexpectedly used solid Accent fill: got=%+v", scene, label, c)
							}
						}
					}

					fx := phase3SeededControlPlaneApp(t)
					fx.app.windowWidth = w
					fx.contact.activity.open = true
					fx.contact.activityNotices.selected = activityNoticeKey{fx.questionAct.ActivityID, "question_open", "q-cp-1"}
					tr := ui.NewTesterWith(fx.app.view, w, h, ui.TesterOptions{Dark: dark})
					tr.SetLocale(locale)
					tr.SetFocused(true)
					tr.Frame()

					assertHit(tr, "drawer+notice-strip", msgs.WorkActivity(), msgs.ReloadAction(), msgs.ActivityAnswer(), "Batch fourteen", "Batch sixteen", msgs.SendAction())
					assertNotAccentFill(tr, "drawer+notice-strip", msgs.WorkActivity(), msgs.ActivityAnswer(), "Batch fourteen", msgs.BotStatusAnswer())

					compRect, okComp := tr.Find(msgs.ComposerLabel(fx.contact.Name))
					if !okComp || compRect.X < 0 || compRect.Y < 0 || compRect.X+compRect.W > float32(w)+0.5 || compRect.Y+compRect.H > float32(h)+0.5 {
						t.Fatalf("composer clipped with drawer+notice strip open: ok=%v rect=%+v viewport=%dx%d", okComp, compRect, w, h)
					}
					if err := tr.Click(msgs.ComposerLabel(fx.contact.Name)); err != nil {
						t.Fatalf("composer not focusable with drawer+notice strip open: %v", err)
					}
					tr.Frame()
					if _, hasCaret := tr.TextCaret(); !hasCaret {
						t.Fatal("composer has no active caret after click with drawer+notice strip open")
					}
					beforeDraft := fx.contact.Draft
					fx.app.pi.productDrafts = map[string]pi.ProductDraft{fx.contact.ID: {Text: beforeDraft + "!"}}
					tr.Type("!")
					tr.Frame()
					if fx.contact.Draft != beforeDraft+"!" {
						t.Fatalf("composer typing failed with drawer+notice strip open: got=%q want=%q", fx.contact.Draft, beforeDraft+"!")
					}

					if err := tr.Click("Batch fourteen"); err != nil {
						t.Fatalf("click option pill Batch fourteen: %v", err)
					}
					tr.Move(0, 0)
					tr.Advance(theme.ControlTransition.Duration)
					tr.Frame()

					headerRect, okHeader := tr.Find(msgs.BotStatusAnswer())
					pillRect, okPill := tr.Find("Batch fourteen")
					if !okHeader || !okPill {
						t.Fatalf("missing header(%v) or pill(%v) before theme switch snap check", okHeader, okPill)
					}
					tr.SetDark(!dark)
					otherWB := theme.ResolveWorkbenchTheme(!dark)
					gotHeader := tr.Image().RGBAAt(int(headerRect.X+headerRect.W/2), int(headerRect.Y+3))
					wantHeader := otherWB.Theme.Surface
					if !closeRGBA([4]uint8{gotHeader.R, gotHeader.G, gotHeader.B, gotHeader.A}, [4]uint8{wantHeader.R, wantHeader.G, wantHeader.B, wantHeader.A}) {
						t.Fatalf("botStatusAction did not snap on first SetDark(%v) frame: got=%+v want=%+v", !dark, gotHeader, wantHeader)
					}
					gotPill := tr.Image().RGBAAt(int(pillRect.X+pillRect.W/2), int(pillRect.Y+3))
					wantPill := otherWB.Theme.Surface.Mix(otherWB.Theme.Success, .09)
					if !dark {
						wantPill = otherWB.Theme.Surface.Mix(otherWB.Theme.Accent, .12)
					}
					if !closeRGBA([4]uint8{gotPill.R, gotPill.G, gotPill.B, gotPill.A}, [4]uint8{wantPill.R, wantPill.G, wantPill.B, wantPill.A}) {
						t.Fatalf("selected option TagPill did not snap on first SetDark(%v) frame: got=%+v want=%+v", !dark, gotPill, wantPill)
					}
					tr.SetDark(dark)
					tr.Advance(theme.ControlTransition.Duration)
					tr.Frame()

					fx.contact.activity.open = false
					for _, notice := range []struct {
						name   string
						key    activityNoticeKey
						labels []string
					}{
						{"strip-approval", activityNoticeKey{fx.approvalAct.ActivityID, "approval_required", "req-cp-1"}, []string{msgs.CapReviewApprove(), msgs.CapDeny()}},
						{"strip-completed", activityNoticeKey{fx.completedAct.ActivityID, "completed", "exec-done-1"}, []string{msgs.ActivityAcknowledge(), msgs.ActivityOpen()}},
						{"strip-auto-answer", activityNoticeKey{fx.autoAnswerAct.ActivityID, "question_auto_saved", "q-cp-auto"}, []string{msgs.ActivityDismissHint()}},
					} {
						fx.contact.activityNotices.selected = notice.key
						fx.contact.activityNotices.scroll.Y = 0
						tr.Frame()
						assertHit(tr, notice.name, notice.labels...)
						assertNotAccentFill(tr, notice.name, notice.labels...)
						actRect, okAct := tr.Find(msgs.WorkActivity())
						if !okAct || actRect.W >= 220 {
							t.Fatalf("scene=%s collapsed Activity toggle width = %.1f DIP, want intrinsic < 220 (ok=%v)", notice.name, actRect.W, okAct)
						}
					}

					fx.contact.activity.open = true
					fx.contact.activity.activityID = fx.steerAct.ActivityID
					fx.contact.activity.activity = fx.steerAct
					fx.contact.activity.scroll.Y = 0
					tr.Frame()
					assertHit(tr, "steer-detail", msgs.BackLabel(), msgs.WorkActivity(), msgs.ReloadAction(), msgs.SourceChat(), msgs.WorkStopIssueTask(), msgs.ActivitySteer())
					assertNotAccentFill(tr, "steer-detail", msgs.SourceChat(), msgs.WorkStopIssueTask(), msgs.ActivitySteer())
					if sourceRect, okSource := tr.Find(msgs.SourceChat()); !okSource || sourceRect.W >= 220 {
						t.Fatalf("steer-detail SourceChat button stretched: ok=%v rect=%+v", okSource, sourceRect)
					}
				})
			}
		}
	}
}

func visualIntPtr(v int) *int {
	return &v
}

func visualUint64Ptr(v uint64) *uint64 {
	return &v
}

func visualIntrinsicHeadingWidth(text, locale string, dark bool) float32 {
	var measured float32
	tr := ui.NewTesterWith(func(c *ui.Context) {
		wb := theme.ResolveWorkbenchTheme(dark)
		c.SetTheme(wb.Theme)
		ui.Row(c).Children(func() {
			ui.Text(c, text).FontSize(16).FontWeight(600).FixedLineHeight(24).TextColor(wb.Theme.Text).SingleLine()
		})
	}, 800, 80, ui.TesterOptions{Dark: dark})
	tr.SetLocale(locale)
	tr.Frame()
	if r, ok := tr.Find(text); ok {
		measured = r.W
	}
	return measured
}

func seedVisualSessionInspectorFixture(t *testing.T) (phase3SeededControlPlane, pi.ProductSessionSummary, pi.ProductSessionSummary) {
	t.Helper()
	fx := phase3SeededControlPlaneApp(t)
	fx.contact.activeActivities = nil
	fx.contact.activity.items = nil
	fx.contact.activityNotices = activityNoticeState{}

	ptySummary := pi.ProductSessionSummary{
		SessionID:            "sess-pty-visual",
		OwnerBotID:           fx.contact.ID,
		OriginConversationID: fx.contact.SessionID,
		OriginActivityID:     fx.steerAct.ActivityID,
		CreatingOperationID:  "op-sess-pty",
		Generation:           1,
		Revision:             4,
		Label:                "PTY build console",
		CommandSummary:       "/bin/zsh -lc 'make verify'",
		Cwd:                  "/tmp/nagi-workspace",
		Mode:                 pi.ProductSessionPTY,
		Cols:                 visualIntPtr(120),
		Rows:                 visualIntPtr(36),
		State:                pi.ProductSessionRunning,
		TransportStatus:      pi.ProductTransportLive,
		NextOffset:           12288,
		AuthPause: &pi.ProductSessionAuthPause{
			Paused: true,
			Reason: "auth_prompt",
		},
		Watches: []pi.ProductSessionWatchSummary{
			{
				WatchID:      "watch-pty-1",
				SessionID:    "sess-pty-visual",
				Generation:   1,
				Revision:     2,
				State:        "armed",
				Conditions:   []string{"exit", "match", "idle", "timeout"},
				MatchLiteral: "BUILD_DONE",
				IdleMS:       visualUint64Ptr(1500),
				TimeoutMS:    visualUint64Ptr(30000),
			},
		},
	}
	pipeSummary := pi.ProductSessionSummary{
		SessionID:            "sess-pipe-visual",
		OwnerBotID:           fx.contact.ID,
		OriginConversationID: fx.contact.SessionID,
		OriginActivityID:     fx.steerAct.ActivityID,
		CreatingOperationID:  "op-sess-pipe",
		Generation:           2,
		Revision:             6,
		Label:                "Pipe test runner",
		CommandSummary:       "node scripts/run-suite.mjs",
		Cwd:                  "/tmp/nagi-workspace",
		Mode:                 pi.ProductSessionPipe,
		State:                pi.ProductSessionExited,
		ExitCode:             visualIntPtr(0),
		TransportStatus:      pi.ProductTransportEOF,
		FirstAvailableOffset: 2048,
		NextOffset:           12288,
		EOF:                  true,
		EvictedBytes:         2048,
		Watches: []pi.ProductSessionWatchSummary{
			{
				WatchID:        "watch-pipe-1",
				SessionID:      "sess-pipe-visual",
				Generation:     2,
				Revision:       3,
				State:          "fired",
				Conditions:     []string{"match"},
				MatchLiteral:   "PASS",
				FiredKind:      "match",
				MatchedExcerpt: "All checks PASS",
			},
		},
	}

	bot := fx.app.sessions.botSession(fx.contact.ID)
	fx.app.sessions.client = fx.app.pi.product
	fx.app.sessions.active = map[string]map[string]pi.ProductSessionSummary{
		fx.contact.ID: {
			ptySummary.SessionID: ptySummary,
		},
	}
	bot.items = []pi.ProductSessionSummary{ptySummary, pipeSummary}
	bot.nextCursor = "cursor-page-2"
	return fx, ptySummary, pipeSummary
}

func TestVisualGuardSessionInspection(t *testing.T) {
	for _, tc := range []struct {
		name string
		dark bool
	}{{"Paper Light", false}, {"Moss Dark", true}} {
		wb := theme.ResolveWorkbenchTheme(tc.dark)
		th := wb.Theme
		for _, pair := range []struct {
			label string
			fg    ui.Color
		}{
			{"sessionBadge running", th.Success},
			{"sessionBadge failed", th.Danger},
			{"sessionBadge starting", wb.WorkAttention},
			{"sessionBadge closed", th.TextMuted},
		} {
			if ratio := theme.PaletteContrast(pair.fg, wb.Reading); ratio < 4.5 {
				t.Fatalf("%s %s contrast = %.2f, want >= 4.5", tc.name, pair.label, ratio)
			}
		}
	}

	for _, locale := range []string{"en", "zh-Hans"} {
		msgs := locales.In(locale)
		for _, dark := range []bool{false, true} {
			for _, size := range [][2]int{{1040, 720}, {960, 640}, {720, 640}} {
				w, h := size[0], size[1]
				t.Run(fmt.Sprintf("%s-dark%t-%dx%d", locale, dark, w, h), func(t *testing.T) {
					assertHit := func(tr *ui.Tester, scene string, labels ...string) {
						t.Helper()
						for _, label := range labels {
							r, ok := tr.Find(label)
							if !ok || r.X < -0.5 || r.Y < -0.5 || r.X+r.W > float32(w)+0.5 || r.Y+r.H > float32(h)+0.5 || r.W < theme.ControlHit || r.H < theme.ControlHit {
								t.Fatalf("scene=%s control %q invalid/overflow: ok=%v rect=%+v viewport=%dx%d", scene, label, ok, r, w, h)
							}
						}
					}

					fx, ptySummary, pipeSummary := seedVisualSessionInspectorFixture(t)
					fx.app.windowWidth = w
					tr := ui.NewTesterWith(fx.app.view, w, h, ui.TesterOptions{Dark: dark})
					tr.SetLocale(locale)
					tr.SetFocused(true)
					tr.Frame()

					if tr.HasText(msgs.SessionInspectTitle()) || tr.HasText(msgs.OrchestrationTitle()) || tr.HasText("Sessions") || tr.HasText(msgs.SessionInspectMenuCount(1)) {
						t.Fatalf("idle chat header should stay clean without permanent work text buttons: texts=%v", tr.Texts())
					}
					overviewBtn, okOverview := tr.Find(msgs.BotOverviewLabel())
					if !okOverview || overviewBtn.W < theme.ControlHit || overviewBtn.H < theme.ControlHit || overviewBtn.W > 48 || overviewBtn.Y < 0 || overviewBtn.Y+overviewBtn.H > 64 || overviewBtn.X < 0 || overviewBtn.X+overviewBtn.W > float32(w)+0.5 {
						t.Fatalf("header Bot overview button invalid: ok=%v rect=%+v viewport=%dx%d", okOverview, overviewBtn, w, h)
					}

					if err := tr.Click(msgs.BotOverviewLabel()); err != nil {
						t.Fatalf("click Bot overview button: %v", err)
					}
					tr.Frame()
					if !fx.app.headerMenuOpen {
						t.Fatal("Bot overview popover menu did not open")
					}
					for _, menuItem := range []string{
						msgs.BotMenuActivities(0),
						msgs.OrchestrationMenuCount(0),
						msgs.SessionInspectMenuCount(1),
						msgs.SchedWorkTitle(),
						msgs.ContactProfileAction(),
					} {
						r, ok := tr.Find(menuItem)
						if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > float32(w)+0.5 || r.Y < 0 || r.Y+r.H > float32(h)+0.5 {
							t.Fatalf("Bot overview popover item %q missing or clipped at %dx%d: ok=%v rect=%+v", menuItem, w, h, ok, r)
						}
					}
					tr.Key(0, ui.KeyEscape)
					tr.Frame()
					if fx.app.headerMenuOpen {
						t.Fatal("Escape did not close Bot overview popover menu")
					}

					fx.app.openBotSessions(fx.contact.ID, "header-overview:"+fx.contact.ID)
					bot := fx.app.sessions.botSession(fx.contact.ID)
					bot.items = []pi.ProductSessionSummary{ptySummary, pipeSummary}
					bot.nextCursor = "cursor-page-2"
					bot.listLoading = false
					bot.listFailed = false
					tr.Frame()

					if fx.app.detail != detailProfile || !fx.app.workspaceOpen || fx.app.sessions.activeBotID != fx.contact.ID {
						t.Fatalf("session inspection did not open in right drawer: detail=%q workspaceOpen=%v activeBotID=%q", fx.app.detail, fx.app.workspaceOpen, fx.app.sessions.activeBotID)
					}
					assertHit(tr, "session-list", msgs.SessionInspectBack(), msgs.SessionInspectReload(), ptySummary.Label+", "+msgs.SessionInspectRunning(), pipeSummary.Label+", "+msgs.SessionInspectExited(), msgs.SessionInspectLoadMore())
					wantSessTitleW := visualIntrinsicHeadingWidth(msgs.SessionInspectTitle(), locale, dark)
					titleRect, okTitle := tr.Find(msgs.SessionInspectTitle())
					if !okTitle || !slices.Contains(tr.Texts(), msgs.SessionInspectTitle()) || titleRect.W < 40 || titleRect.X < 0 || titleRect.X+titleRect.W > float32(w)+0.5 {
						t.Fatalf("session list heading missing or out of bounds: ok=%v rect=%+v texts=%v", okTitle, titleRect, tr.Texts())
					}
					if titleRect.W+0.5 < wantSessTitleW {
						t.Fatalf("session list heading %q optically truncated at %dx%d: allocated=%.1f < intrinsic=%.1f", msgs.SessionInspectTitle(), w, h, titleRect.W, wantSessTitleW)
					}
					backRect, _ := tr.Find(msgs.SessionInspectBack())
					reloadRect, _ := tr.Find(msgs.SessionInspectReload())
					ptyRowRect, _ := tr.Find(ptySummary.Label + ", " + msgs.SessionInspectRunning())
					if reloadRect.W >= 220 || math.Abs(float64(reloadRect.Y-backRect.Y)) > 6 || reloadRect.Y >= ptyRowRect.Y {
						t.Fatalf("list header Reload misaligned or stretched: reload=%+v back=%+v firstRow=%+v", reloadRect, backRect, ptyRowRect)
					}
					if w >= 900 {
						sendRect, okSend := tr.Find(msgs.SendAction())
						if !okSend || backRect.X < float32(w)-360 || sendRect.X+sendRect.W > backRect.X {
							t.Fatalf("side-by-side drawer geometry invalid at %dx%d: send=%+v okSend=%v back=%+v", w, h, sendRect, okSend, backRect)
						}
					}

					bot.detail = &sessionDetailState{
						summary: ptySummary,
						ready:   true,
						output: sessionOutputState{
							loaded:         true,
							from:           4096,
							next:           8192,
							highWater:      12288,
							firstAvailable: 2048,
							gapBytes:       2048,
							browsing:       true,
							revision:       ptySummary.Revision,
							authPause:      ptySummary.AuthPause,
							chunks:         []pi.ProductSessionChunk{{Stream: "pty", Offset: 4096, NextOffset: 4120, Text: "PTY_BUILD_OUTPUT\n"}},
						},
					}
					tr.Frame()

					assertHit(tr, "session-detail-pty", msgs.SessionInspectBack(), msgs.SessionInspectKill(), msgs.SessionInspectSourceChat(), msgs.SessionInspectSourceActivity())
					detailTitleRect, okDetailTitle := tr.Find(msgs.SessionInspectTitle())
					if !okDetailTitle || detailTitleRect.W < 40 || detailTitleRect.X+detailTitleRect.W > float32(w)+0.5 {
						t.Fatalf("session detail heading missing or out of bounds: ok=%v rect=%+v", okDetailTitle, detailTitleRect)
					}
					if detailTitleRect.W+0.5 < wantSessTitleW {
						t.Fatalf("session detail heading %q optically truncated at %dx%d: allocated=%.1f < intrinsic=%.1f", msgs.SessionInspectTitle(), w, h, detailTitleRect.W, wantSessTitleW)
					}
					killRect, _ := tr.Find(msgs.SessionInspectKill())
					if killRect.W >= 220 || math.Abs(float64(killRect.Y-backRect.Y)) > 6 {
						t.Fatalf("detail header Kill session misaligned or stretched: kill=%+v back=%+v", killRect, backRect)
					}
					sourceChatRect, _ := tr.Find(msgs.SessionInspectSourceChat())
					sourceActRect, _ := tr.Find(msgs.SessionInspectSourceActivity())
					cmdLabelRect, okCmd := tr.Find(msgs.SessionInspectCommand())
					if !okCmd || sourceChatRect.W >= 220 || sourceActRect.W >= 220 || math.Abs(float64(sourceChatRect.X-cmdLabelRect.X)) > 2.5 {
						t.Fatalf("source links stretched or misaligned from Command: sourceChat=%+v sourceAct=%+v cmd=%+v (ok=%v)", sourceChatRect, sourceActRect, cmdLabelRect, okCmd)
					}
					dimWanted := msgs.SessionInspectDimensions(120, 36)
					if !tr.HasText(dimWanted) {
						t.Fatalf("localized dimensions %q missing in texts=%v", dimWanted, tr.Texts())
					}
					for _, txt := range tr.Texts() {
						if strings.Contains(txt, "%d") || strings.Contains(txt, "%s") || strings.Contains(txt, "{cols}") || strings.Contains(txt, "{bytes}") {
							t.Fatalf("unformatted placeholder in session detail text %q", txt)
						}
					}
					for y := float32(0); y <= 600; y += 120 {
						bot.detail.scroll.Y = y
						tr.Frame()
						if r, ok := tr.Find(msgs.SessionInspectEarlierOutput()); ok && r.Y >= 0 && r.Y+r.H <= float32(h)+0.5 {
							break
						}
					}
					assertHit(tr, "session-detail-pty-output", msgs.SessionInspectEarlierOutput(), msgs.SessionInspectLaterOutput(), msgs.SessionInspectLatestOutput())

					if err := tr.Click(msgs.SessionInspectBack()); err != nil {
						t.Fatalf("click Back from detail: %v", err)
					}
					tr.Frame()
					if bot.detail != nil || !fx.app.workspaceOpen {
						t.Fatalf("Back from detail did not return to session list inside drawer: detail=%v open=%v", bot.detail, fx.app.workspaceOpen)
					}
					if err := tr.Click(msgs.SessionInspectBack()); err != nil {
						t.Fatalf("click Back from session list: %v", err)
					}
					tr.Frame()
					if fx.app.sessions.activeBotID != "" || fx.app.workspaceOpen || fx.app.detail != "" {
						t.Fatalf("Back from root session list did not collapse drawer: activeBotID=%q workspaceOpen=%v detail=%q", fx.app.sessions.activeBotID, fx.app.workspaceOpen, fx.app.detail)
					}
				})
			}
		}
	}
}

func seedVisualScheduledWorkFixture(t *testing.T) (phase3SeededControlPlane, pi.ProductWatchDetail, pi.ProductWatchSummary, pi.ProductWatchGrant) {
	t.Helper()
	fx := phase3SeededControlPlaneApp(t)
	fx.app.contactProfileID = fx.contact.ID
	fx.app.openDetail(detailProfile)
	interval := 300
	timeout := 5000
	exitZero := 0

	grant := pi.ProductWatchGrant{
		GrantID:           "grant-bg2-exec",
		OwnerBotID:        fx.contact.ID,
		State:             "active",
		Revision:          2,
		Purpose:           "Check workspace inventory status",
		ArtifactKind:      "executable",
		Executable:        "/usr/bin/true",
		ExecutableDigest:  "sha256:4f8d22e91c",
		AllowedArgs:       []string{"--check"},
		CanonicalCWD:      "/tmp/nagi-workspace",
		WorkspaceRead:     true,
		MinCadenceSeconds: 60,
		MaxTimeoutMS:      5000,
		NetworkOrigins:    []string{"https://inventory.invalid"},
		CreatedAt:         "2026-11-27T08:00:00Z",
		UpdatedAt:         "2026-11-27T08:00:00Z",
	}

	activeWatch := pi.ProductWatchSummary{
		WatchID:              "watch-bg2-sched",
		OwnerBotID:           fx.contact.ID,
		TargetConversationID: fx.contact.SessionID,
		Kind:                 pi.ProductWatchSchedule,
		State:                pi.ProductWatchActive,
		Armed:                true,
		Source:               pi.ProductWatchSourceUser,
		Revision:             3,
		Label:                "Inventory check",
		Purpose:              "Check workspace inventory status",
		Prompt:               "Summarize any inventory deltas.",
		NextDueAt:            "2026-12-01T09:00:00Z",
		Schedule: &pi.ProductScheduleSpec{
			CadenceKind:     pi.ProductScheduleInterval,
			IntervalSeconds: &interval,
			TimeZone:        "UTC",
		},
		Precheck: &pi.ProductWatchPrecheck{
			Kind:             "executable",
			Executable:       grant.Executable,
			ExecutableDigest: grant.ExecutableDigest,
			AllowedArgs:      grant.AllowedArgs,
			WorkspaceRead:    true,
			TimeoutMS:        &timeout,
			EmitInitial:      true,
		},
		GrantID: grant.GrantID,
	}

	dormantWatch := pi.ProductWatchSummary{
		WatchID:              "watch-bg2-dormant",
		OwnerBotID:           fx.contact.ID,
		TargetConversationID: fx.contact.SessionID,
		Kind:                 pi.ProductWatchSchedule,
		State:                pi.ProductWatchRestored,
		Source:               pi.ProductWatchSourceModel,
		StatusReason:         "grant_required",
		Revision:             1,
		Label:                "Imported supplier audit",
		Purpose:              "Audit imported supplier snapshot",
		Prompt:               "Check supplier compliance.",
		Schedule: &pi.ProductScheduleSpec{
			CadenceKind: pi.ProductScheduleCalendar,
			Frequency:   "daily",
			TimeZone:    "Asia/Shanghai",
			Hour:        visualIntPtr(9),
			Minute:      visualIntPtr(30),
		},
		Precheck: &pi.ProductWatchPrecheck{
			Kind:             "executable",
			Executable:       "/usr/local/bin/audit",
			ExecutableDigest: "sha256:9aa3b4c5d6",
			AllowedArgs:      []string{"--dry-run"},
			WorkspaceRead:    false,
			TimeoutMS:        &timeout,
		},
	}

	detail := pi.ProductWatchDetail{
		ProductWatchSummary: activeWatch,
		Grant:               &grant,
		RecentOccurrences: []pi.ProductWatchOccurrence{
			{
				OccurrenceID:  "occ-bg2-1",
				WatchID:       activeWatch.WatchID,
				OwnerBotID:    fx.contact.ID,
				WatchRevision: 3,
				ScheduledAt:   "2026-11-27T08:00:00Z",
				Outcome:       "changed",
				State:         "settled",
				ExitCode:      &exitZero,
				Digest:        "sha256:abcd1234",
				Excerpt:       "Detected 2 updated rows",
				Truncated:     true,
				CreatedAt:     "2026-11-27T08:00:00Z",
				SettledAt:     "2026-11-27T08:00:02Z",
			},
		},
	}

	bot := fx.app.schedules.bot(fx.contact.ID)
	bot.client = fx.app.pi.product
	bot.watches = scheduledRead[pi.ProductPage[pi.ProductWatchSummary]]{
		ready: true,
		value: pi.ProductPage[pi.ProductWatchSummary]{
			Items: []pi.ProductWatchSummary{activeWatch, dormantWatch},
		},
	}
	bot.grants = scheduledRead[pi.ProductPage[pi.ProductWatchGrant]]{
		ready: true,
		value: pi.ProductPage[pi.ProductWatchGrant]{
			Items: []pi.ProductWatchGrant{grant},
		},
	}
	bot.detail = scheduledRead[pi.ProductWatchDetail]{
		ready: true,
		value: detail,
	}
	return fx, detail, dormantWatch, grant
}

func TestVisualGuardScheduledWork(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		msgs := locales.In(locale)
		m := scheduledWorkCopy{locale: locale}
		for _, dark := range []bool{false, true} {
			for _, size := range [][2]int{{1040, 720}, {960, 640}, {720, 640}} {
				w, h := size[0], size[1]
				t.Run(fmt.Sprintf("%s-dark%t-%dx%d", locale, dark, w, h), func(t *testing.T) {
					assertBtn := func(tr *ui.Tester, scene, label string) {
						t.Helper()
						r, ok := tr.Find(label)
						if !ok || r.X < -0.5 || r.Y < -0.5 || r.X+r.W > float32(w)+0.5 || r.Y+r.H > float32(h)+0.5 || r.H < theme.ControlHit || r.W < theme.ControlHit {
							t.Fatalf("scene=%s button %q invalid/clipped: ok=%v rect=%+v viewport=%dx%d", scene, label, ok, r, w, h)
						}
					}

					fx, detail, dormantWatch, _ := seedVisualScheduledWorkFixture(t)
					fx.app.windowWidth = w
					tr := ui.NewTesterWith(fx.app.view, w, h, ui.TesterOptions{Dark: dark})
					tr.SetLocale(locale)
					tr.SetFocused(true)
					tr.Frame()

					scrollTo := func(scroll *ui.ScrollState, target string) {
						for y := float32(0); y <= 3600; y += 100 {
							scroll.Y = y
							tr.Frame()
							if r, ok := tr.Find(target); ok && r.W >= theme.ControlHit && r.H >= theme.ControlHit && r.Y >= 0 && r.Y+r.H <= float32(h) {
								return
							}
						}
					}
					scrollTo(fx.app.profileScrollFor(fx.contact.ID), msgs.SchedWorkOpenAction())
					assertBtn(tr, "profile-opener", msgs.SchedWorkOpenAction())

					fx.app.scheduledWorkBotID = fx.contact.ID
					tr.Frame()
					assertBtn(tr, "sched-list-back", msgs.BackLabel())
					assertBtn(tr, "sched-list-new", m.text("New timer or schedule", "新建计时器或计划"))
					assertBtn(tr, "sched-list-grants", m.text("Standing grants", "常驻授权"))
					newBtn, _ := tr.Find(m.text("New timer or schedule", "新建计时器或计划"))
					if newBtn.W >= 280 {
						t.Fatalf("sched-list primary action stretched horizontally: rect=%+v", newBtn)
					}

					bot := fx.app.schedules.bot(fx.contact.ID)
					bot.detail.value = detail
					bot.editor = &scheduledWatchEditor{
						kind:      string(pi.ProductWatchSchedule),
						cadence:   string(pi.ProductScheduleCalendar),
						frequency: "weekly",
						precheck:  "executable",
						zone:      "Asia/Shanghai",
						hour:      "9",
						minute:    "30",
					}
					scrollTo(&bot.scroll, m.text("Review local grant", "审核本机授权"))
					assertBtn(tr, "sched-editor-submit", m.text("Review local grant", "审核本机授权"))

					bot.editor = nil
					bot.review = &scheduledWatchReview{watch: dormantWatch, needed: true}
					bot.scroll.Y = 0
					tr.Frame()
					if !tr.HasText(m.recoveryScope()) {
						t.Fatalf("dormant review missing recovery scope warning in %v", tr.Texts())
					}
					scrollTo(&bot.scroll, m.text("Approve local grant and activate", "批准本机授权并启用"))
					assertBtn(tr, "sched-grant-approve", m.text("Approve local grant and activate", "批准本机授权并启用"))

					fx.app.scheduledWorkBotID = ""
					fx.app.openSettingsTab(settingsTabAppearance, settingsSourceCommand)
					s := fx.app.ensureDataRecovery()
					s.target = &storeRecoveryTarget{}
					s.canonical = fx.app.storePath
					s.viewGeneration = fx.app.folderChoiceGeneration
					s.owner = fx.app.selectedContact()
					s.copy = &storeRecoveryCopy{
						Path:           "/tmp/nagi-backup.profile",
						Contacts:       2,
						Issues:         1,
						Timers:         1,
						Schedules:      2,
						DormantWatches: 3,
						ModifiedAt:     time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC),
					}
					s.scopeOpen = true
					scrollTo(&fx.app.settingsScroll, msgs.RecoveryConfirmAction())
					wantCounts := msgs.RecoveryPreviewWatchCounts(1, 2, 3)
					if !tr.HasText(wantCounts) || !tr.HasText(msgs.RecoveryScopeWatches()) {
						t.Fatalf("settings recovery preview missing watch counts=%q or scope in texts=%v", wantCounts, tr.Texts())
					}
					assertBtn(tr, "settings-recovery-confirm", msgs.RecoveryConfirmAction())
				})
			}
		}
	}
}

func TestVisualGuardNativeLifecycleDraining(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		msgs := locales.In(locale)
		for _, dark := range []bool{false, true} {
			for _, size := range [][2]int{{1040, 720}, {960, 640}, {720, 640}} {
				w, h := size[0], size[1]
				t.Run(fmt.Sprintf("%s-dark%t-%dx%d", locale, dark, w, h), func(t *testing.T) {
					fx, _, _, _ := seedVisualScheduledWorkFixture(t)
					fx.app.windowWidth = w
					fx.app.shutdownStarted = true
					fx.app.shutdownDrainStarted = true
					tr := ui.NewTesterWith(fx.app.nativeWindowView, w, h, ui.TesterOptions{Dark: dark})
					tr.SetLocale(locale)
					tr.SetFocused(true)
					tr.Frame()

					if !tr.HasText(msgs.RuntimeStatusDraining()) || !tr.HasText(msgs.RuntimeDraining()) {
						t.Fatalf("draining view missing localized status/body: texts=%v", tr.Texts())
					}
					for _, text := range []string{msgs.RuntimeStatusDraining(), msgs.RuntimeDraining()} {
						r, ok := tr.Find(text)
						if !ok || r.X < -0.5 || r.Y < -0.5 || r.X+r.W > float32(w)+0.5 || r.Y+r.H > float32(h)+0.5 {
							t.Fatalf("draining text %q clipped: ok=%v rect=%+v viewport=%dx%d", text, ok, r, w, h)
						}
					}

					fx.app.shutdownDrainStarted = false
					fx.app.runtimeErrKind = runtimeNoticeRuntimeShutdown
					fx.app.runtimeErr = "drain timeout"
					tr.Frame()

					errText := fx.app.runtimeErrorDisplay(msgs)
					if !tr.HasText(errText) {
						t.Fatalf("drain retry view missing error=%q: texts=%v", errText, tr.Texts())
					}
					r, ok := tr.Find(msgs.RecoveryQuitAction())
					if !ok || r.X < -0.5 || r.Y < -0.5 || r.X+r.W > float32(w)+0.5 || r.Y+r.H > float32(h)+0.5 || r.H < theme.ControlHit || r.W < theme.ControlHit || r.W >= float32(w-80) {
						t.Fatalf("drain retry button invalid/stretched: ok=%v rect=%+v viewport=%dx%d", ok, r, w, h)
					}
				})
			}
		}
	}
}

func TestVisualGuardMessageReadStatus(t *testing.T) {
	countText := func(tr *ui.Tester, target string) int {
		count := 0
		for _, text := range tr.Texts() {
			if text == target {
				count++
			}
		}
		return count
	}
	for _, locale := range []string{"en", "zh-Hans"} {
		msgs := locales.In(locale)
		readLabel := msgs.MessageReadStatus()
		for _, dark := range []bool{false, true} {
			for _, size := range [][2]int{{1040, 720}, {960, 640}, {720, 640}} {
				w, h := size[0], size[1]
				t.Run(fmt.Sprintf("%s-dark%t-%dx%d", locale, dark, w, h), func(t *testing.T) {
					a, ct := workspaceTestApp(t)
					a.windowWidth = w
					ct.Messages = []*message{{ID: 1, Kind: msgUser, Text: "Unconfirmed user draft", ReadByBot: false, History: false}}
					tr := ui.NewTesterWith(a.view, w, h, ui.TesterOptions{Dark: dark})
					tr.SetLocale(locale)
					tr.Frame()
					if tr.HasText(readLabel) {
						t.Fatalf("unconfirmed user message unexpectedly showed %q: %v", readLabel, tr.Texts())
					}

					ct.Status = statusWorking
					ct.Messages = []*message{
						{ID: 1, Kind: msgUser, Text: "First user turn", ReadByBot: true},
						{Key: "live:read-1:text:pi", Kind: msgContact, Text: "Streaming first line...", Streaming: true, liveReveal: true},
					}
					tr.Frame()
					if countText(tr, readLabel) != 1 || !tr.HasText(msgs.BotTyping()) {
						t.Fatalf("typing turn should show 1 %q and typing indicator: texts=%v", readLabel, tr.Texts())
					}

					ct.Status = statusReady
					ct.Messages = []*message{
						{ID: 1, Kind: msgUser, Text: "First user turn", ReadByBot: true},
						{ID: 2, Kind: msgUser, Text: "Second consecutive user turn", ReadByBot: true},
					}
					tr.Frame()
					if got := countText(tr, readLabel); got != 1 {
						t.Fatalf("consecutive user messages should render exactly 1 %q, got %d: texts=%v", readLabel, got, tr.Texts())
					}
					u2Rect, okU2 := tr.Find("Second consecutive user turn")
					readRect, okRead := tr.Find(readLabel)
					if !okU2 || !okRead || readRect.W <= 0 || readRect.H <= 0 || readRect.Y < u2Rect.Y+u2Rect.H-2 ||
						readRect.X < -0.5 || readRect.X+readRect.W > float32(w)+0.5 || readRect.Y+readRect.H > float32(h)+0.5 ||
						readRect.X+readRect.W < u2Rect.X {
						t.Fatalf("read badge invalid geometry: u2=%+v(%v) read=%+v(%v) viewport=%dx%d", u2Rect, okU2, readRect, okRead, w, h)
					}

					ct.Messages = append(ct.Messages, &message{ID: 3, Kind: msgContact, Text: "Agent reply arrived.", History: true})
					tr.Frame()
					if tr.HasText(readLabel) {
						t.Fatalf("read indicator remained after Bot reply appeared: texts=%v", tr.Texts())
					}

					ct.Messages = append(ct.Messages, &message{ID: 4, Kind: msgUser, Text: "Third user follow-up", ReadByBot: true})
					ct.Messages = append(ct.Messages, &message{
						Key:       "notice:n-follow",
						Kind:      msgNotice,
						Text:      "Background check note",
						Timestamp: time.Now(),
						Notice:    &pi.ProductConversationNotice{BotID: ct.ID, MandateID: "mandate-follow", Urgency: "normal"},
					})
					tr.Frame()
					if got := countText(tr, readLabel); got != 1 || !tr.HasText("Background check note") {
						t.Fatalf("follow-up user turn with notice should keep 1 %q, got %d: texts=%v", readLabel, got, tr.Texts())
					}
					u3Rect, okU3 := tr.Find("Third user follow-up")
					readRect3, okRead3 := tr.Find(readLabel)
					if !okU3 || !okRead3 || readRect3.Y < u3Rect.Y+u3Rect.H-2 {
						t.Fatalf("follow-up read badge not positioned below third turn: u3=%+v read=%+v", u3Rect, readRect3)
					}

					ct.Messages = append(ct.Messages, &message{ID: 5, Kind: msgContact, Text: "Reply after notice.", History: true})
					tr.Frame()
					if tr.HasText(readLabel) {
						t.Fatalf("read indicator remained after Bot reply after notice: texts=%v", tr.Texts())
					}
				})
			}
		}
	}
}

func TestVisualGuardOrchestrationAndTimeline(t *testing.T) {
	sizes := [][2]int{{1040, 720}, {960, 640}, {720, 640}}
	for _, locale := range []string{"en", "zh-Hans"} {
		msgs := locales.In(locale)
		for _, dark := range []bool{false, true} {
			for _, size := range sizes {
				w, h := size[0], size[1]
				t.Run(fmt.Sprintf("%s-dark%t-%dx%d", locale, dark, w, h), func(t *testing.T) {
					a, ct := workspaceTestApp(t)
					a.windowWidth = w
					a.demo = false
					a.runtimeStatus = runtimeStateReady
					a.connectionGeneration = 1
					a.pi = phase3StubProductHost(t, a, visualGuardSeededPool())
					a.profileEditors = map[string]*contactProfileEditor{ct.ID: newContactProfileEditor(contactProfileDraft(ct))}
					ct.transcriptError = true
					ct.Messages = []*message{
						{
							Key:       "notice:n-urgent",
							Kind:      msgNotice,
							Text:      "Staging checks passed; awaiting confirmation.",
							Timestamp: time.Now(),
							Notice: &pi.ProductConversationNotice{
								BotID:     ct.ID,
								MandateID: "watch:w-release",
								Urgency:   "high",
								Dormant:   true,
							},
						},
						{ID: 1, Kind: msgUser, Text: "Is the release ready?", ReadByBot: true},
					}
					ct.SendState = sendQueued
					ct.SendQueuedPosition = 2
					ct.ActiveTaskID = "exec-queued"
					ct.Draft = "Follow up on canary checks"
					decisionItem := pi.ProductOrchestrationSummary{
						MandateID:  "watch:w-release",
						OwnerBotID: ct.ID,
						SourceKind: pi.ProductMandateSourceWatch,
						Purpose:    "Monitor release readiness",
						State:      pi.ProductMandateWaitingDecision,
						Generation: 2,
						Revision:   7,
						PendingDecision: &pi.ProductOrchestrationDecision{
							DecisionID: "dec-release",
							MandateID:  "watch:w-release",
							OwnerBotID: ct.ID,
							Revision:   1,
							State:      "open",
							Kind:       "approval",
							Purpose:    "Approve rollout",
							Summary:    "Confirm whether to publish release notes.",
							Options: []pi.ProductOrchestrationDecisionOption{
								{ID: "publish", Label: "Publish notes"},
								{ID: "hold", Label: "Hold draft"},
							},
						},
						Metrics: pi.ProductOrchestrationMetrics{TotalWakes: 4, Calls: 3, TotalTokens: 980},
					}

					tr := ui.NewTesterWith(a.view, w, h, ui.TesterOptions{Dark: dark})
					tr.SetLocale(locale)
					tr.SetFocused(true)
					tr.Frame()

					for _, expected := range []string{
						msgs.TimelineTranscriptErrorNotice(),
						msgs.TimelineNoticeUrgent(),
						msgs.TimelineNoticeReadOnly(),
						msgs.MessageReadStatus(),
						msgs.SendQueuedPosition(2),
					} {
						r, ok := tr.Find(expected)
						if !ok || r.W <= 0 || r.H <= 0 || r.X < -0.5 || r.X+r.W > float32(w)+0.5 || r.Y < -0.5 || r.Y+r.H > float32(h)+0.5 {
							t.Fatalf("chat timeline text %q out of viewport %dx%d: ok=%v rect=%+v", expected, w, h, ok, r)
						}
					}

					ct.orchestration.items = []pi.ProductOrchestrationSummary{decisionItem}
					tr.Frame()

					texts := tr.Texts()
					if slices.Contains(texts, msgs.OrchestrationTitle()) || tr.HasText(msgs.OrchestrationHeaderCount(1)) || slices.Contains(texts, msgs.SessionInspectTitle()) {
						t.Fatalf("chat header should stay clean without permanent Background work button: texts=%v", texts)
					}
					overviewRect, okOverview := tr.Find(msgs.BotOverviewLabel())
					if !okOverview || overviewRect.W < theme.ControlHit || overviewRect.H < theme.ControlHit || overviewRect.W > 48 || overviewRect.Y < 0 || overviewRect.Y+overviewRect.H > 64 || overviewRect.X+overviewRect.W > float32(w)+0.5 {
						t.Fatalf("header Bot overview button invalid at %dx%d: ok=%v rect=%+v", w, h, okOverview, overviewRect)
					}
					if err := tr.Click(msgs.BotOverviewLabel()); err != nil {
						t.Fatalf("click Bot overview button: %v", err)
					}
					tr.Frame()
					menuRect, okMenu := tr.Find(msgs.OrchestrationMenuCount(1))
					if !okMenu || !a.headerMenuOpen || menuRect.X < 0 || menuRect.X+menuRect.W > float32(w)+0.5 || menuRect.Y < 0 || menuRect.Y+menuRect.H > float32(h)+0.5 {
						t.Fatalf("popover item %q missing or clipped at %dx%d: ok=%v open=%v rect=%+v texts=%v", msgs.OrchestrationMenuCount(1), w, h, okMenu, a.headerMenuOpen, menuRect, tr.Texts())
					}
					tr.Key(0, ui.KeyEscape)
					tr.Frame()

					for _, expected := range []string{
						"Approve rollout",
						"Publish notes",
						msgs.OrchestrationApprove(),
						msgs.OrchestrationReject(),
						msgs.SendQueuedPosition(2),
					} {
						r, ok := tr.Find(expected)
						if !ok || r.W <= 0 || r.H <= 0 || r.X < -0.5 || r.X+r.W > float32(w)+0.5 || r.Y < -0.5 || r.Y+r.H > float32(h)+0.5 {
							t.Fatalf("chat decision text %q out of viewport %dx%d: ok=%v rect=%+v", expected, w, h, ok, r)
						}
					}

					a.openContactProfile(ct.ID)
					a.backgroundWork.panel = &backgroundWorkPanel{
						bot:    ct,
						opener: "header-overview:" + ct.ID,
						items:  ct.orchestration.items,
					}
					tr.Frame()
					for _, expected := range []string{msgs.OrchestrationTitle(), "Monitor release readiness", msgs.DetailsAction()} {
						r, ok := tr.Find(expected)
						if !ok || r.W <= 0 || r.H <= 0 || r.X < -0.5 || r.X+r.W > float32(w)+0.5 || r.Y < -0.5 || r.Y+r.H > float32(h)+0.5 {
							t.Fatalf("background list text %q out of viewport %dx%d: ok=%v rect=%+v", expected, w, h, ok, r)
						}
					}
					wantBgTitleW := visualIntrinsicHeadingWidth(msgs.OrchestrationTitle(), locale, dark)
					bgTitleRect, _ := tr.Find(msgs.OrchestrationTitle())
					if !slices.Contains(tr.Texts(), msgs.OrchestrationTitle()) {
						t.Fatalf("background work heading missing untruncated text %q: texts=%v", msgs.OrchestrationTitle(), tr.Texts())
					}
					if bgTitleRect.W+0.5 < wantBgTitleW {
						t.Fatalf("background work list heading %q optically truncated at %dx%d: allocated=%.1f < intrinsic=%.1f", msgs.OrchestrationTitle(), w, h, bgTitleRect.W, wantBgTitleW)
					}
					if w >= 900 {
						decRect, okDec := tr.Find(msgs.OrchestrationDecisionBadge())
						if !okDec || bgTitleRect.X < float32(w)-360 || decRect.X+decRect.W > bgTitleRect.X {
							t.Fatalf("background drawer geometry invalid at %dx%d: title=%+v decision=%+v okDec=%v", w, h, bgTitleRect, decRect, okDec)
						}
					}

					detailSummary := ct.orchestration.items[0]
					a.backgroundWork.panel.detailID = detailSummary.MandateID
					a.backgroundWork.panel.detail = &pi.ProductOrchestrationDetail{
						ProductOrchestrationSummary: detailSummary,
						Decisions:                   []pi.ProductOrchestrationDecision{*detailSummary.PendingDecision},
						RecentRuns: []pi.ProductOrchestrationRun{
							{RunID: "run-1", MandateID: detailSummary.MandateID, Generation: 2, State: "completed", RouteRole: "utility", Disposition: pi.ProductDispositionNeedsDecision, Summary: "Requested rollout decision.", Calls: 2, TotalTokens: 480},
						},
						RecentReceipts: []pi.ProductOrchestrationEffectReceipt{
							{ReceiptID: "rcpt-1", MandateID: detailSummary.MandateID, RunID: "run-1", CallID: "c1", Tool: "conversation_notify", Target: "conv-1", State: "settled_ok"},
						},
					}
					tr.Frame()
					bgDetailTitleRect, okBgDetailTitle := tr.Find(msgs.OrchestrationTitle())
					if !okBgDetailTitle || bgDetailTitleRect.W+0.5 < wantBgTitleW {
						t.Fatalf("background detail heading %q missing or truncated at %dx%d: ok=%v allocated=%.1f < intrinsic=%.1f", msgs.OrchestrationTitle(), w, h, okBgDetailTitle, bgDetailTitleRect.W, wantBgTitleW)
					}
					if !tr.HasText(msgs.OrchestrationMetricsTitle()) {
						t.Fatalf("background detail missing metrics heading: %v", tr.Texts())
					}
					var receiptsVisible bool
					for y := float32(0); y <= 1400; y += 160 {
						a.backgroundWork.panel.detailScroll.Y = y
						tr.Frame()
						if tr.HasText(msgs.OrchestrationReceiptsTitle()) && tr.HasText("conversation_notify") {
							receiptsVisible = true
							break
						}
					}
					if !receiptsVisible {
						t.Fatalf("scrolling background detail did not reveal receipts: %v", tr.Texts())
					}
					a.closeDetail()

					a.openSettingsTab(settingsTabAppearance, settingsSourceCommand)
					s := a.ensureDataRecovery()
					s.target = &storeRecoveryTarget{}
					s.canonical = a.storePath
					s.viewGeneration = a.folderChoiceGeneration
					s.owner = ct
					s.scopeOpen = true
					s.copy = &storeRecoveryCopy{
						Path: "/Documents/Nagi.backup.json", Contacts: 2, Issues: 1,
						Timers: 1, Schedules: 1, DormantWatches: 2,
						DormantMandates: 3, Decisions: 2, Notices: 4, ModifiedAt: time.Now(),
					}
					wantCounts := msgs.RecoveryPreviewOrchestrationCounts(3, 2, 4)
					var foundCounts bool
					for y := float32(0); y <= 2400; y += 160 {
						a.settingsScroll.Y = y
						tr.Frame()
						if r, ok := tr.Find(wantCounts); ok && r.W > 0 && r.H > 0 && r.X >= -0.5 && r.X+r.W <= float32(w)+0.5 && r.Y >= 0 && r.Y+r.H <= float32(h)+0.5 {
							foundCounts = true
							break
						}
					}
					if !foundCounts || !tr.HasText(msgs.RecoveryScopeOrchestration()) {
						t.Fatalf("settings recovery did not render v14 orchestration counts %q cleanly: %v", wantCounts, tr.Texts())
					}
				})
			}
		}
	}
}

func TestVisualGuardComposerPlusAlignment(t *testing.T) {
	assertComposerPlusAlignmentAndChrome(t)
}

func TestVisualGuardFloatingBackToLatestAndCollapsedRail(t *testing.T) {
	assertFloatingBackToLatestOverlay(t)
	assertCollapsedRailTopLeftToggleAndZeroTopStrip(t)
}
