package app

import (
	"image"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func diffPixelsInRect(a, b *image.RGBA, x0, y0, x1, y1 int) int {
	changed := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			i := a.PixOffset(x, y)
			if a.Pix[i] != b.Pix[i] || a.Pix[i+1] != b.Pix[i+1] || a.Pix[i+2] != b.Pix[i+2] || a.Pix[i+3] != b.Pix[i+3] {
				changed++
			}
		}
	}
	return changed
}

func cloneRGBA(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Rect)
	copy(dst.Pix, src.Pix)
	return dst
}

func TestBotStatusSixStatePriorityAndSurfaces(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	a := &appModel{}

	makeBot := func(id, name string) *contact {
		return &contact{ID: id, Name: name, Purpose: "Focused specialist.", Loaded: true, Status: statusReady}
	}

	ct := makeBot("b1", "Priority Bot")
	if got := a.botVisualState(ct, now); got.kind != botVisualIdle {
		t.Fatalf("idle kind = %v, want %v", got.kind, botVisualIdle)
	}

	ct.completedUntil = now.Add(time.Second)
	if got := a.botVisualState(ct, now); got.kind != botVisualDone {
		t.Fatalf("done kind = %v, want %v", got.kind, botVisualDone)
	}

	ct.activeActivities = map[string]pi.ProductActivitySummary{
		"run-1": {ActivityID: "run-1", OwnerBotID: ct.ID, State: "running"},
		"run-2": {ActivityID: "run-2", OwnerBotID: ct.ID, State: "running"},
	}
	if got := a.botVisualState(ct, now); got.kind != botVisualWorking || got.workingCount != 2 {
		t.Fatalf("working state = %+v, want working/2", got)
	}

	ct.Status = statusWorking
	if got := a.botVisualState(ct, now); got.kind != botVisualReplying || got.workingCount != 2 {
		t.Fatalf("replying state = %+v, want replying/2", got)
	}

	ct.activeActivities["wait-1"] = pi.ProductActivitySummary{
		ActivityID: "wait-1", OwnerBotID: ct.ID, State: "waitingForAnswer", OpenQuestionIDs: []string{"q-1"},
	}
	if got := a.botVisualState(ct, now); got.kind != botVisualWaiting || got.waitingCount != 1 || got.waitingActivity != "wait-1" || got.questionID != "q-1" {
		t.Fatalf("waiting state = %+v, want waiting/1/wait-1/q-1", got)
	}

	ct.Archived = true
	if got := a.botVisualState(ct, now); got.kind != botVisualArchived {
		t.Fatalf("archived state = %+v, want archived", got)
	}

	for _, tc := range []struct {
		name   string
		locale string
		dark   bool
		width  int
		height int
		narrow bool
	}{
		{"en-dark-1280", "en", true, 1280, 720, false},
		{"zh-light-1040", "zh-Hans", false, 1040, 720, false},
		{"zh-light-960-compact", "zh-Hans", false, 960, 640, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, pandora, _ := pandoraBoxTestApp(t)
			app.windowWidth = tc.width
			tr := ui.NewTester(app.view, tc.width, tc.height)
			tr.SetLocale(tc.locale)
			tr.SetDark(tc.dark)
			frameNow := tr.Now()

			replying := makeBot("b-reply", "Release Auditor")
			replying.Status = statusWorking
			working := makeBot("b-work", "Build Engineer")
			working.activeActivities = map[string]pi.ProductActivitySummary{
				"a1": {ActivityID: "a1", OwnerBotID: working.ID, State: "running"},
				"a2": {ActivityID: "a2", OwnerBotID: working.ID, State: "running"},
			}
			waiting := makeBot("b-wait", "Writing Editor")
			waiting.activeActivities = map[string]pi.ProductActivitySummary{
				"w1": {ActivityID: "w1", OwnerBotID: waiting.ID, State: "waitingForAnswer", OpenQuestionIDs: []string{"q1"}},
			}
			done := makeBot("b-done", "Data Analyst")
			done.completedUntil = frameNow.Add(1800 * time.Millisecond)
			done.Messages = []*message{{ID: 1, Kind: msgContact, Text: "Verified release metrics."}}
			archived := makeBot("b-arch", "Archive Bot")
			archived.Archived = true

			app.contacts = []*contact{pandora, replying, working, waiting, done, archived}
			app.selected = 0
			if tc.narrow {
				app.openContactProfile(pandora.ID)
			}
			tr.Frame()

			msgs := locales.In(tc.locale)
			texts := tr.Texts()
			for _, retired := range []string{"▶", "!", msgs.StatusReady(), "Bot Architect"} {
				if slices.Contains(texts, retired) {
					t.Fatalf("retired text %q rendered in %s: %v", retired, tc.name, texts)
				}
			}
			if tc.narrow {
				for _, bot := range app.contacts {
					r, ok := tr.Find(msgs.AvatarContactLabel(bot.Name))
					if !ok || r.X < 0 || r.X+r.W > avatarRailWidth || r.H < 36 {
						t.Fatalf("compact avatar rail bounds invalid for %q: rect=%+v ok=%v", bot.Name, r, ok)
					}
				}
			} else {
				for _, want := range []string{
					msgs.PandoraBoxPurpose(),
					msgs.BotStatusReplying(),
					msgs.BotStatusRailWorking(2),
					msgs.BotStatusRailWaiting(1),
					msgs.BotStatusRailDoneWithSnippet("Verified release metrics."),
					msgs.ProfileArchivedBadge(),
				} {
					if !tr.HasText(want) {
						t.Fatalf("full rail missing %q in %s: %v", want, tc.name, tr.Texts())
					}
				}
			}
		})
	}
}

func TestBotStatusHeaderPurposeCenteringAndDecoupledActions(t *testing.T) {
	a, pandora, specialist := pandoraBoxTestApp(t)
	specialist.Purpose = ""
	specialist.Role = "HiddenRoleName"
	a.windowWidth = 1040

	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	msgs := locales.In("en")
	tr.Frame()

	if !tr.HasText(msgs.PandoraBoxPurpose()) || tr.HasText(msgs.StatusReady()) || tr.HasText("Bot Architect") {
		t.Fatalf("PandoraBox idle header mismatch: %v", tr.Texts())
	}

	a.selectContact(1)
	tr.Frame()
	if tr.HasText(msgs.StatusReady()) || tr.HasText("HiddenRoleName") {
		t.Fatalf("empty-purpose header leaked retired status or role: %v", tr.Texts())
	}
	if nameRect, ok := tr.Find(specialist.Name); !ok || nameRect.Y <= 0 || nameRect.H > 26 {
		t.Fatalf("empty-purpose header name geometry invalid: %+v ok=%v", nameRect, ok)
	}

	specialist.activeActivities = map[string]pi.ProductActivitySummary{
		"act-run": {ActivityID: "act-run", OwnerBotID: specialist.ID, State: "running"},
	}
	tr.Frame()
	if !tr.HasText(msgs.BotStatusWorking(1)) || !tr.HasText(msgs.BotStatusViewTasks()) {
		t.Fatalf("working header missing status or view-tasks action: %v", tr.Texts())
	}
	if err := tr.Click(msgs.BotStatusViewTasks()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !specialist.activity.open {
		t.Fatal("clicking View tasks did not open activity panel")
	}

	specialist.activity.open = false
	specialist.activeActivities["act-wait"] = pi.ProductActivitySummary{
		ActivityID: "act-wait", OwnerBotID: specialist.ID, State: "waitingForAnswer", OpenQuestionIDs: []string{"q-focus-1"},
	}
	tr.Frame()
	if !tr.HasText(msgs.BotStatusWaiting(1)) || !tr.HasText(msgs.BotStatusAnswer()) {
		t.Fatalf("waiting header missing status or Answer action: %v", tr.Texts())
	}
	if err := tr.Click(msgs.BotStatusAnswer()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !specialist.activity.open || specialist.activity.activityID != "act-wait" || specialist.activity.focusQuestion != "q-focus-1" {
		t.Fatalf("clicking Answer did not route to exact waiting activity/question: open=%v act=%q q=%q",
			specialist.activity.open, specialist.activity.activityID, specialist.activity.focusQuestion)
	}

	specialist.activeActivities = nil
	specialist.Purpose = "Audit release checklists."
	a.demo = false
	a.runtimeStatus = runtimeStateExited
	a.runtimeStage = "Connection configuration"
	tr.Frame()
	if !tr.HasText(specialist.Purpose) || !tr.HasText(msgs.ConnectionStatusNotConnected()) {
		t.Fatalf("non-ready connection header missing purpose or decoupled connection button: %v", tr.Texts())
	}
	if err := tr.Click(msgs.ConnectionStatusNotConnected()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabConnection {
		t.Fatalf("clicking header connection button did not open Settings -> Connection: open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
	_ = pandora
}

func TestBotStatusCompletionReceiptExpiryAndBackgroundHintClear(t *testing.T) {
	a, pandora, specialist := pandoraBoxTestApp(t)
	specialist.Purpose = "Audit release checklists."
	specialist.Messages = []*message{{ID: 1, Kind: msgContact, Text: "Checklist passed."}}
	a.selected = 0
	a.windowWidth = 1040

	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	msgs := locales.In("en")
	start := tr.Now()

	pandora.completedUntil = start.Add(1800 * time.Millisecond)
	specialist.completedUntil = start.Add(1800 * time.Millisecond)
	specialist.recentCompletion = true
	tr.Frame()

	if !tr.HasText(msgs.BotStatusDone()) {
		t.Fatalf("active Bot header missing Done receipt at t=0: %v", tr.Texts())
	}
	if !tr.HasText(msgs.BotStatusRailDoneWithSnippet("Checklist passed.")) {
		t.Fatalf("background Bot rail missing Done hint at t=0: %v", tr.Texts())
	}

	tr.Advance(1700 * time.Millisecond)
	if !tr.HasText(msgs.BotStatusDone()) {
		t.Fatalf("Done receipt expired too early at 1.7s: %v", tr.Texts())
	}

	tr.Advance(150 * time.Millisecond)
	if slices.Contains(tr.Texts(), msgs.BotStatusDone()) || !tr.HasText(msgs.PandoraBoxPurpose()) {
		t.Fatalf("Done receipt did not revert to idle purpose after 1.85s: %v", tr.Texts())
	}
	if !tr.HasText(msgs.BotStatusRailDoneWithSnippet("Checklist passed.")) {
		t.Fatalf("background Bot rail hint disappeared before selection: %v", tr.Texts())
	}

	a.selectContact(1)
	tr.Frame()
	if specialist.recentCompletion || tr.HasText(msgs.BotStatusRailDoneWithSnippet("Checklist passed.")) {
		t.Fatalf("selecting background Bot did not clear recentCompletion hint: flag=%v texts=%v", specialist.recentCompletion, tr.Texts())
	}
	if !tr.HasText(specialist.Purpose) {
		t.Fatalf("selected specialist did not show idle purpose after clearing hint: %v", tr.Texts())
	}
}

func TestBotStatusReplyingHaloAnimationAndReduceMotion(t *testing.T) {
	for _, tc := range []struct {
		name         string
		reduceMotion bool
		wantChanged  bool
	}{
		{"normal-motion-breathes", false, true},
		{"reduce-motion-steady", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, pandora, _ := pandoraBoxTestApp(t)
			pandora.Status = statusWorking
			a.selected = 0
			a.windowWidth = 1040

			tr := ui.NewTesterWith(a.view, 1040, 720, ui.TesterOptions{
				Dark:        true,
				Preferences: ui.Preferences{ReduceMotion: tc.reduceMotion},
			})
			tr.SetLocale("en")
			tr.Frame()

			mainStart := int(a.effectiveRailWidth() + paneSeparatorWidth)
			x0, y0, x1, y1 := mainStart+12, 4, mainStart+56, 48

			before := cloneRGBA(tr.Image())
			tr.Advance(300 * time.Millisecond)
			tr.Frame()
			mid := cloneRGBA(tr.Image())
			tr.Advance(300 * time.Millisecond)
			tr.Frame()
			after := cloneRGBA(tr.Image())

			changed := max(diffPixelsInRect(before, mid, x0, y0, x1, y1), diffPixelsInRect(before, after, x0, y0, x1, y1))
			if tc.wantChanged && changed == 0 {
				t.Fatalf("replying halo did not animate under normal motion in ROI [%d,%d,%d,%d]", x0, y0, x1, y1)
			}
			if !tc.wantChanged && changed != 0 {
				t.Fatalf("replying halo changed %d pixels under ReduceMotion in ROI [%d,%d,%d,%d]", changed, x0, y0, x1, y1)
			}
		})
	}
}
