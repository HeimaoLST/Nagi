package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestUnifiedEmptyConversationCompositionAndDraftFill(t *testing.T) {
	for _, tc := range []struct {
		name       string
		locale     string
		dark       bool
		width      int
		height     int
		usePandora bool
	}{
		{"en-dark-pandora-1280", "en", true, 1280, 720, true},
		{"zh-light-pandora-1040", "zh-Hans", false, 1040, 720, true},
		{"en-dark-specialist-1280", "en", true, 1280, 720, false},
		{"zh-light-specialist-720", "zh-Hans", false, 720, 640, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, pandora, specialist := pandoraBoxTestApp(t)
			specialist.Draft = ""
			specialist.StarterPrompts = []string{
				"Review my launch checklist",
				"Compare the candidate against release notes",
				"Summarize unresolved release risks",
				"Check rollback criteria",
			}
			ct := specialist
			a.selected = 1
			if tc.usePandora {
				ct = pandora
				a.selected = 0
				pandora.StarterPrompts = []string{
					locales.In("en").PandoraBoxStarterWriting(),
					locales.In("en").PandoraBoxStarterCodeReview(),
					locales.In("en").PandoraBoxStarterAudit(),
				}
			}
			a.windowWidth = tc.width
			msgs := locales.In(tc.locale)
			tr := ui.NewTester(a.view, tc.width, tc.height)
			tr.SetLocale(tc.locale)
			tr.SetDark(tc.dark)
			tr.SetFocused(true)
			tr.Frame()

			if !tr.HasText(msgs.EmptyBotGreeting(ct.Name)) {
				t.Fatalf("empty greeting missing: %v", tr.Texts())
			}
			wantPurpose := ct.Purpose
			if tc.usePandora {
				wantPurpose = msgs.PandoraBoxPurpose()
			}
			if !tr.HasText(wantPurpose) {
				t.Fatalf("purpose %q missing: %v", wantPurpose, tr.Texts())
			}

			wantPrompts := specialist.StarterPrompts
			if tc.usePandora {
				defaults := pandoraBoxStarters(msgs)
				wantPrompts = defaults[:]
			}
			composerRect, composerOK := tr.Find(msgs.ComposerLabel(ct.Name))
			if !composerOK {
				t.Fatal("composer missing")
			}
			var prevBottom float32
			for _, prompt := range wantPrompts {
				r, ok := tr.Find(prompt)
				if !ok || r.H < 34 || r.X < 0 || r.X+r.W > float32(tc.width) || r.Y < prevBottom || r.Y+r.H > composerRect.Y+4 {
					t.Fatalf("starter prompt %q geometry invalid: rect=%+v prevBottom=%g composer=%+v", prompt, r, prevBottom, composerRect)
				}
				prevBottom = r.Y + r.H
			}

			beforeRev := ct.draftRev
			chosen := wantPrompts[0]
			if err := tr.Click(chosen); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if ct.Draft != chosen || ct.draftRev != beforeRev+1 || len(ct.Messages) != 0 || a.pi != nil {
				t.Fatalf("clicking starter prompt did not fill draft without sending: draft=%q rev=%d->%d msgs=%d", ct.Draft, beforeRev, ct.draftRev, len(ct.Messages))
			}
			if a.composerCaretToEnd {
				t.Fatal("composerCaretToEnd was not cleared after applying starter prompt")
			}
			if !tr.Focused(msgs.ComposerLabel(ct.Name)) {
				t.Fatal("clicking starter prompt did not focus composer")
			}
			tr.Type("!")
			tr.Frame()
			if ct.Draft != chosen+"!" || len(ct.Messages) != 0 {
				t.Fatalf("typing after starter click did not append at end: got %q want %q", ct.Draft, chosen+"!")
			}
			tr.Key(0, ui.KeyBackspace)
			tr.Frame()
			if ct.Draft != chosen {
				t.Fatalf("backspace did not restore starter prompt: got %q want %q", ct.Draft, chosen)
			}
			tr.Key(0, ui.KeyLeft)
			tr.Key(0, ui.KeyLeft)
			tr.Frame()
			if err := tr.Click(chosen); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.composerCaretToEnd || !tr.Focused(msgs.ComposerLabel(ct.Name)) {
				t.Fatalf("re-clicking starter prompt did not refocus and clear caret flag: flag=%v focused=%v", a.composerCaretToEnd, tr.Focused(msgs.ComposerLabel(ct.Name)))
			}
			tr.Type("?")
			tr.Frame()
			if ct.Draft != chosen+"?" || len(ct.Messages) != 0 {
				t.Fatalf("typing after re-clicking identical starter prompt did not append at end: got %q want %q", ct.Draft, chosen+"?")
			}
		})
	}
}

func TestStarterPromptCaretAtEndWithMultibyteEmoji(t *testing.T) {
	a, _, specialist := pandoraBoxTestApp(t)
	prompt := "检查发布进度 🚀"
	specialist.Draft = ""
	specialist.StarterPrompts = []string{prompt}
	a.selected = 1
	a.windowWidth = 1040
	msgs := locales.In("zh-Hans")
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("zh-Hans")
	tr.SetDark(false)
	tr.SetFocused(true)
	tr.Frame()

	if err := tr.Click(prompt); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct := specialist; ct.Draft != prompt || a.composerCaretToEnd || !tr.Focused(msgs.ComposerLabel(ct.Name)) {
		t.Fatalf("multibyte starter click failed: draft=%q flag=%v focused=%v", ct.Draft, a.composerCaretToEnd, tr.Focused(msgs.ComposerLabel(ct.Name)))
	}
	tr.Type("!")
	tr.Frame()
	if specialist.Draft != prompt+"!" {
		t.Fatalf("typing after multibyte emoji starter did not append at end: got %q want %q", specialist.Draft, prompt+"!")
	}
	tr.Key(0, ui.KeyBackspace)
	tr.Key(0, ui.KeyHome)
	tr.Key(0, ui.KeyLeft)
	tr.Frame()
	if err := tr.Click(prompt); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Type("?")
	tr.Frame()
	if specialist.Draft != prompt+"?" || len(specialist.Messages) != 0 {
		t.Fatalf("re-clicking multibyte emoji starter did not reset caret to end: got %q want %q", specialist.Draft, prompt+"?")
	}
}

func TestEmptyConversationFallbackWorkLogsPurposeAndCleanEmpty(t *testing.T) {
	a, pandora, specialist := pandoraBoxTestApp(t)
	specialist.Draft = ""
	specialist.StarterPrompts = nil
	specialist.Purpose = "Audit release readiness."
	specialist.memory.loaded = true
	specialist.memory.data.WorkLogs = []pi.ProductWorkLog{
		{LogID: "w3", BotID: specialist.ID, Date: "2026-11-20", Summary: "Verified arm64 standalone packaging"},
		{LogID: "w2", BotID: specialist.ID, Date: "2026-11-19", Summary: "Checked SQLite migration fixtures"},
		{LogID: "w1", BotID: specialist.ID, Date: "2026-11-18", Summary: "Older log should not appear"},
	}

	msgsEn := locales.In("en")
	got := starterPromptsFor(specialist, msgsEn)
	want := []string{
		msgsEn.StarterFollowUp("Verified arm64 standalone packaging"),
		msgsEn.StarterFollowUp("Checked SQLite migration fixtures"),
		msgsEn.StarterHelpWithPurpose("Audit release readiness."),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("fallback starterPromptsFor = %q, want %q", got, want)
	}

	msgsZh := locales.In("zh-Hans")
	pandora.StarterPrompts = nil
	zhDefaults := pandoraBoxStarters(msgsZh)
	if gotPB := starterPromptsFor(pandora, msgsZh); !slices.Equal(gotPB, zhDefaults[:]) {
		t.Fatalf("legacy empty PandoraBox starters = %q, want %q", gotPB, zhDefaults[:])
	}
	pandora.StarterPrompts = []string{"Custom architect prompt"}
	if gotCustom := starterPromptsFor(pandora, msgsZh); !slices.Equal(gotCustom, []string{"Custom architect prompt"}) {
		t.Fatalf("custom PandoraBox starter prompt was overwritten: %q", gotCustom)
	}

	blank := &contact{ID: "blank-bot", Name: "Quiet Bot", Loaded: true, Status: statusReady}
	blank.memory.loaded = true
	a.contacts = []*contact{blank}
	a.selected = 0
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText(msgsEn.EmptyBotGreeting("Quiet Bot")) {
		t.Fatalf("blank Bot missing greeting: %v", tr.Texts())
	}
	if gotBlank := starterPromptsFor(blank, msgsEn); len(gotBlank) != 0 {
		t.Fatalf("blank Bot produced generic filler prompts: %q", gotBlank)
	}
	if tr.HasText("Follow up on:") || tr.HasText("Help me with:") || tr.HasText(msgsEn.PandoraBoxStarterWriting()) {
		t.Fatalf("blank Bot rendered filler starter text: %v", tr.Texts())
	}

	blank.StarterPrompts = []string{"Archived prompt"}
	blank.Archived = true
	tr.Frame()
	if err := tr.Click("Archived prompt"); err == nil {
		tr.Frame()
		if blank.Draft != "" {
			t.Fatalf("archived Bot starter row filled draft: %q", blank.Draft)
		}
	}
}

func TestBotProfileStarterPromptsEditorSavesLines(t *testing.T) {
	a, _, specialist := pandoraBoxTestApp(t)
	specialist.StarterPrompts = []string{"First prompt"}
	a.openContactProfile(specialist.ID)
	msgs := locales.In("en")
	tr := ui.NewTester(a.view, 1280, 720)
	tr.SetLocale("en")
	tr.SetFocused(true)
	tr.Frame()

	if !tr.HasText(msgs.ProfileStarterPrompts()) || !tr.HasText(msgs.ProfileStarterPromptsHint()) {
		t.Fatalf("Bot profile missing Starter prompts section: %v", tr.Texts())
	}
	if err := tr.Click(msgs.FieldStarterPromptsAccessible()); err != nil {
		t.Fatal(err)
	}
	tr.Command("selectAll")
	tr.Type("Alpha line\nBeta line")
	tr.Frame()
	editor := a.profileEditorFor(specialist.ID)
	if !slices.Equal(editor.Draft.StarterPrompts, []string{"Alpha line", "Beta line"}) {
		t.Fatalf("Starter prompts draft = %q", editor.Draft.StarterPrompts)
	}
	if err := tr.Click(msgs.ContactSaveChanges()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !slices.Equal(specialist.StarterPrompts, []string{"Alpha line", "Beta line"}) || strings.Join(specialist.StarterPrompts, "\n") != "Alpha line\nBeta line" {
		t.Fatalf("saved StarterPrompts = %q", specialist.StarterPrompts)
	}
}
