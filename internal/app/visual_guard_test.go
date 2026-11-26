package app

import (
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

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/connection"
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
