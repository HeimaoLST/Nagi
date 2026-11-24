package app

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func memoryProfileTestApp(t *testing.T, populated bool) (*appModel, *contact, *piAppHost) {
	t.Helper()
	a, _, specialist := pandoraBoxTestApp(t)
	a.demo = false
	a.runtimeStatus = runtimeStateReady
	a.shutdownStarted = true
	host := &piAppHost{
		product:       &pi.ProductClient{},
		productBots:   map[string]pi.ProductBot{},
		productDrafts: map[string]pi.ProductDraft{},
	}
	a.pi = host
	specialist.memory.loaded = true
	if populated {
		specialist.PendingLearnings = 1
		specialist.memory.data = pi.ProductBotMemory{
			BotID:         specialist.ID,
			RulesRevision: 2,
			Rules: []pi.ProductMemoryRule{
				{RuleID: "r1", BotID: specialist.ID, Text: "Use UTC in release notes.", Source: "explicit"},
				{RuleID: "r2", BotID: specialist.ID, Text: "Cite commit SHAs.", Source: "approved"},
			},
			PendingLearnings: []pi.ProductPendingLearning{
				{LearningID: "l1", BotID: specialist.ID, Kind: "rule", Text: "Put decisions before caveats.", Reason: "Based on your feedback.", State: "pending"},
			},
			WorkLogs: []pi.ProductWorkLog{
				{LogID: "w1", BotID: specialist.ID, Date: "2026-11-18", Summary: "发布核验完成，证据已记录。", Tags: []string{"release"}},
			},
		}
	}
	return a, specialist, host
}

func TestBotMemoryProfileRenderingMenusAndFixedFooter(t *testing.T) {
	for _, tc := range []struct {
		name      string
		locale    string
		dark      bool
		width     int
		height    int
		populated bool
	}{
		{"en-dark-populated-1280", "en", true, 1280, 720, true},
		{"zh-light-populated-1040", "zh-Hans", false, 1040, 720, true},
		{"en-dark-empty-1280", "en", true, 1280, 720, false},
		{"zh-light-empty-min-720", "zh-Hans", false, 720, 640, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, specialist, _ := memoryProfileTestApp(t, tc.populated)
			a.windowWidth = tc.width
			msgs := locales.In(tc.locale)
			tr := ui.NewTester(a.view, tc.width, tc.height)
			tr.SetLocale(tc.locale)
			tr.SetDark(tc.dark)
			tr.SetFocused(true)
			tr.Frame()

			if err := tr.RightClick(msgs.AvatarContactLabel(specialist.Name)); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			wantMenu := msgs.ContactProfileAction()
			if tc.populated {
				wantMenu = msgs.MemoryProfileAction(1)
			}
			if !tr.HasText(wantMenu) {
				t.Fatalf("Bot row menu missing %q: %v", wantMenu, tr.Texts())
			}
			tr.Key(0, ui.KeyEscape)
			tr.Frame()

			a.openContactProfile(specialist.ID)
			a.profileScrollFor(specialist.ID).Y = 1000
			tr.Frame()

			archiveBefore, ok := tr.Find(msgs.ProfileArchive())
			if !ok || archiveBefore.H < 32 || archiveBefore.Y+archiveBefore.H > float32(tc.height) {
				t.Fatalf("fixed Archive footer missing or clipped: rect=%+v ok=%v", archiveBefore, ok)
			}
			if !tr.HasText(msgs.MemoryTitle()) || !tr.HasText(msgs.MemoryRules()) || !tr.HasText(msgs.MemoryWorkLog()) {
				t.Fatalf("memory headings missing: %v", tr.Texts())
			}
			if tc.populated {
				for _, want := range []string{
					msgs.MemoryReviewCount(1), "Put decisions before caveats.", msgs.MemoryApprove(), msgs.MemoryReject(),
					"Use UTC in release notes.", msgs.MemorySourceExplicit(), msgs.MemorySourceApproved(),
					"2026-11-18", "发布核验完成，证据已记录。",
				} {
					if !tr.HasText(want) {
						t.Fatalf("populated memory panel missing %q: %v", want, tr.Texts())
					}
				}
			} else {
				if !tr.HasText(msgs.MemoryNoRules()) || !tr.HasText(msgs.MemoryNoWorkLogs()) || tr.HasText(msgs.MemoryApprove()) {
					t.Fatalf("empty memory panel state invalid: %v", tr.Texts())
				}
			}

			specialist.memory.loaded = false
			specialist.memory.loading = true
			tr.Frame()
			archiveLoading, ok := tr.Find(msgs.ProfileArchive())
			if !ok || archiveLoading != archiveBefore {
				t.Fatalf("Archive footer shifted across async memory load: before=%+v loading=%+v", archiveBefore, archiveLoading)
			}
		})
	}
}

func TestBotMemoryEditControlsLocalizedErrorsAndEventProjection(t *testing.T) {
	a, specialist, host := memoryProfileTestApp(t, true)
	msgs := locales.In("en")
	renderPanel := func(c *ui.Context) {
		wb := a.resolveAppearanceTheme(false).Palette
		a.workTheme = wb
		c.SetTheme(wb.Theme)
		lmsgs := locales.In(c.Locale())
		ui.Scroll(c).Fill().Padding(16).Background(wb.Theme.Background).Children(func() {
			ui.Column(c).FillWidth().Gap(12).Children(func() {
				a.memoryLearningList(c, wb.Theme, specialist, lmsgs, true)
				a.memoryRuleList(c, wb.Theme, specialist, lmsgs, true)
				a.memoryWorkLogs(c, wb.Theme, specialist, lmsgs, true)
			})
		})
	}
	tr := ui.NewTester(renderPanel, 480, 720)
	tr.SetLocale("en")
	tr.SetFocused(true)
	tr.Frame()

	if err := tr.Click(msgs.MemoryEdit()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if specialist.memory.editRuleID != "r1" || specialist.memory.addDraft != "Use UTC in release notes." || !tr.HasText(msgs.MemorySave()) {
		t.Fatalf("Edit rule did not populate editor: id=%q draft=%q", specialist.memory.editRuleID, specialist.memory.addDraft)
	}
	if err := tr.Click(msgs.MemoryCancelEdit()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if specialist.memory.editRuleID != "" || specialist.memory.addDraft != "" || !tr.HasText(msgs.MemoryAdd()) {
		t.Fatalf("Cancel edit did not reset rule editor: id=%q draft=%q", specialist.memory.editRuleID, specialist.memory.addDraft)
	}

	for _, locale := range []string{"en", "zh-Hans"} {
		lmsgs := locales.In(locale)
		tr.SetLocale(locale)
		for _, tc := range []struct {
			code string
			want string
		}{
			{"memory_secret", lmsgs.MemoryErrorSecret()},
			{"memory_temporary", lmsgs.MemoryErrorTemporary()},
			{"memory_duplicate", lmsgs.MemoryErrorDuplicate()},
			{"memory_invalid", lmsgs.MemoryErrorInvalid()},
			{"memory_rule_limit", lmsgs.MemoryErrorRuleLimit()},
			{"memory_reviewed", lmsgs.MemoryErrorChanged()},
		} {
			t.Run(fmt.Sprintf("%s/%s", locale, tc.code), func(t *testing.T) {
				specialist.memory.problem = tc.code
				tr.Frame()
				if !tr.HasText(tc.want) {
					t.Fatalf("localized error %q missing for code %q: %v", tc.want, tc.code, tr.Texts())
				}
			})
		}
	}

	raw, err := json.Marshal(pi.ProductMemoryChangedEvent{BotID: specialist.ID, RulesRevision: 5, PendingCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	a.applyProductEvent(host, pi.ProductEvent{Name: "memory.changed", Data: raw})
	if specialist.PendingLearnings != 3 {
		t.Fatalf("memory.changed did not update PendingLearnings: got %d want 3", specialist.PendingLearnings)
	}
}
