package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestBotStoneAvatarSurfacesNoInitialsAndIdleRingless(t *testing.T) {
	makeTeam := func(t *testing.T, now time.Time) (*appModel, []*contact) {
		t.Helper()
		a, pandora, _ := pandoraBoxTestApp(t)
		pandora.Purpose = locales.In("en").PandoraBoxPurpose()
		replying := &contact{ID: "bot-reply", Name: "Release Auditor", Purpose: "Audits releases.", Loaded: true, Status: statusWorking}
		working := &contact{
			ID: "bot-work", Name: "Build Engineer", Purpose: "Runs builds.", Loaded: true, Status: statusReady,
			activeActivities: map[string]pi.ProductActivitySummary{
				"a1": {ActivityID: "a1", OwnerBotID: "bot-work", State: "running"},
			},
		}
		waiting := &contact{
			ID: "bot-wait", Name: "Writing Editor", Purpose: "Edits drafts.", Loaded: true, Status: statusReady,
			activeActivities: map[string]pi.ProductActivitySummary{
				"w1": {ActivityID: "w1", OwnerBotID: "bot-wait", State: "waitingForAnswer", OpenQuestionIDs: []string{"q1"}},
			},
		}
		done := &contact{
			ID: "bot-done", Name: "Data Analyst", Purpose: "Tracks metrics.", Loaded: true, Status: statusReady,
			completedUntil: now.Add(1800 * time.Millisecond),
			Messages:       []*message{{Key: "m-done-1", ID: 1, Kind: msgContact, Text: "Release check passed."}},
		}
		archived := &contact{ID: "bot-arch", Name: "Archive Keeper", Purpose: "Retired helper.", Loaded: true, Archived: true}
		a.contacts = []*contact{pandora, replying, working, waiting, done, archived}
		a.selected = 0
		return a, a.contacts
	}

	captureDir := os.Getenv("NAGI_AVATAR_CAPTURE_DIR")
	if captureDir != "" {
		if err := os.MkdirAll(captureDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name   string
		locale string
		dark   bool
	}{
		{"en-light", "en", false},
		{"en-dark", "en", true},
		{"zh-Hans-light", "zh-Hans", false},
		{"zh-Hans-dark", "zh-Hans", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := makeTeam(t, time.Now())
			a.windowWidth = 1040
			tr := ui.NewTesterWith(a.view, 1040, 720, ui.TesterOptions{Dark: tc.dark})
			tr.SetLocale(tc.locale)
			a.contacts[4].completedUntil = tr.Now().Add(1800 * time.Millisecond)
			tr.Frame()

			texts := tr.Texts()
			for _, legacyInitial := range []string{"P", "PB", "RA", "BE", "WE", "DA", "AK"} {
				if slices.Contains(texts, legacyInitial) {
					t.Fatalf("legacy initial token %q unexpectedly rendered in %s: %v", legacyInitial, tc.name, texts)
				}
			}

			mainStart := int(a.effectiveRailWidth() + paneSeparatorWidth)
			headerImg := cloneRGBA(tr.Image())
			headerBg := headerImg.RGBAAt(mainStart+300, 32)
			avatarTopY := 14
			avatarLeftX := mainStart + 16
			idleRingPixels := 0
			for x := avatarLeftX + 6; x <= avatarLeftX+30; x++ {
				if headerImg.RGBAAt(x, avatarTopY) != headerBg {
					idleRingPixels++
				}
			}
			if idleRingPixels != 0 {
				t.Fatalf("idle header stone avatar retained %d outer ring pixels at y=%d", idleRingPixels, avatarTopY)
			}

			a.contacts[0].Status = statusWorking
			tr.Frame()
			activeImg := cloneRGBA(tr.Image())
			activeHaloDiff := diffPixelsInRect(headerImg, activeImg, avatarLeftX-2, avatarTopY-4, avatarLeftX+42, avatarTopY+42)
			if activeHaloDiff == 0 {
				t.Fatalf("replying halo did not paint around header stone avatar in %s", tc.name)
			}
			a.contacts[0].Status = statusReady
			tr.Frame()

			emptyNonBg := 0
			cx, cy := mainStart+(1040-mainStart)/2, 300
			emptyBg := headerImg.RGBAAt(mainStart+40, 200)
			for y := cy - 120; y < cy+120; y++ {
				for x := cx - 32; x < cx+32; x++ {
					if headerImg.RGBAAt(x, y) != emptyBg {
						emptyNonBg++
					}
				}
			}
			if emptyNonBg < 120 {
				t.Fatalf("empty state 48x48 bare stone avatar rendered too few pixels: %d", emptyNonBg)
			}

			if captureDir != "" {
				writePreviewPNG(t, filepath.Join(captureDir, fmt.Sprintf("app-full-empty-%s-1040x720.png", tc.name)), tr.Image())
			}

			if tc.locale == "en" {
				a.windowWidth = 960
				a.openContactProfile(a.contacts[0].ID)
				trCompact := ui.NewTesterWith(a.view, 960, 640, ui.TesterOptions{Dark: tc.dark})
				trCompact.SetLocale(tc.locale)
				a.contacts[4].completedUntil = trCompact.Now().Add(1800 * time.Millisecond)
				trCompact.Frame()
				compactTexts := trCompact.Texts()
				for _, legacyInitial := range []string{"P", "PB", "RA", "BE", "WE", "DA", "AK"} {
					if slices.Contains(compactTexts, legacyInitial) {
						t.Fatalf("legacy initial %q rendered in compact/profile %s: %v", legacyInitial, tc.name, compactTexts)
					}
				}
				if captureDir != "" {
					themeName := "light"
					if tc.dark {
						themeName = "dark"
					}
					writePreviewPNG(t, filepath.Join(captureDir, fmt.Sprintf("app-compact-profile-6states-%s-960x640.png", themeName)), trCompact.Image())
				}

				aChat, _ := makeTeam(t, time.Now())
				aChat.windowWidth = 1040
				one := 1
				ct := aChat.contacts[0]
				ct.Messages = []*message{
					{Key: "q-u1", MessageID: "q-u1", ID: 1, Kind: msgUser, Text: "Show the formula for π."},
					{Key: "q-b1", MessageID: "q-b1", ID: 2, Kind: msgContact, Text: "圆周率可以这样计算（就是那个 <<<NEXT>>> 分句的规矩）。\n<<<NEXT>>>这是用 Machin 公式算的，条件是 1 < 2。"},
					{
						Key: "q-u2", MessageID: "q-u2", ID: 3, Kind: msgUser, Text: "Explain why it converges.",
						Quote: &pi.ProductQuote{MessageID: "q-b1", Sender: "bot", AuthorName: ct.Name, Text: "这是用 Machin 公式算的，条件是 1 < 2。", BubbleIndex: &one},
					},
				}
				trChat := ui.NewTesterWith(aChat.view, 1040, 720, ui.TesterOptions{Dark: tc.dark})
				trChat.SetLocale("en")
				trChat.Frame()
				if err := trChat.Click(locales.In("en").QuoteJump(ct.Name)); err != nil {
					t.Fatal(err)
				}
				trChat.Frame()
				if captureDir != "" {
					themeName := "light"
					if tc.dark {
						themeName = "dark"
					}
					writePreviewPNG(t, filepath.Join(captureDir, fmt.Sprintf("app-chat-quote-recall-%s-1040x720.png", themeName)), trChat.Image())
				}
			}
		})
	}

	t.Run("empty-rail-pandorabox-restore-button-bare-stone", func(t *testing.T) {
		a, pandora, _ := pandoraBoxTestApp(t)
		a.contacts = []*contact{pandora}
		a.hideBuiltinArchitect = true
		a.windowWidth = 960
		a.openContactProfile(pandora.ID)
		tr := ui.NewTester(a.view, 960, 640)
		tr.SetLocale("en")
		tr.Frame()
		r, ok := tr.Find(locales.In("en").RailShowPandoraBox())
		if !ok || r.W < 24 || r.H < 24 {
			t.Fatalf("empty compact rail PandoraBox restore button missing: %+v ok=%v", r, ok)
		}
		if slices.Contains(tr.Texts(), "P") || slices.Contains(tr.Texts(), "PB") {
			t.Fatalf("empty compact rail restore button leaked legacy initials: %v", tr.Texts())
		}
		if captureDir != "" {
			writePreviewPNG(t, filepath.Join(captureDir, "app-empty-compact-rail-restore-light-960x640.png"), tr.Image())
		}
	})
}
