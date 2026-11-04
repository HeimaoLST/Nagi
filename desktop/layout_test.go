package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
)

func layoutAppAt(t *testing.T, width int) (*appModel, *contact) {
	t.Helper()
	a, ct := workspaceTestApp(t)
	a.windowWidth = width
	a.workspaceOpen = true
	return a, ct
}

func TestLayoutNewAndLegacyStoresUseClosedRightPaneDefaults(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if store.state.Layout != nil {
		t.Fatal("new store should not invent a layout preference")
	}
	a := &appModel{}
	applyStoreSelection(a, store)
	if a.workspaceOpen || a.railWidth() != defaultLeftPaneWidth || a.workWidth() != defaultRightPaneWidth {
		t.Fatalf("new-store layout = open=%v left=%v right=%v", a.workspaceOpen, a.railWidth(), a.workWidth())
	}

	store.state.Layout = &storedLayout{LeftWidth: 240, RightWidth: 380, WorkspaceOpen: true}
	applyStoreSelection(a, store)
	if !a.workspaceOpen || a.railWidth() != 240 || a.workWidth() != 380 {
		t.Fatalf("stored layout = open=%v left=%v right=%v", a.workspaceOpen, a.railWidth(), a.workWidth())
	}
	store.state.Layout = nil
	applyStoreSelection(a, store)
	if a.workspaceOpen {
		t.Fatal("legacy store without layout must start with the right pane closed")
	}
}

func TestLayoutEntryPointsStartWithRightPaneClosed(t *testing.T) {
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.workspaceOpen {
		t.Fatal("demo app started with the right pane open")
	}
	u := newUnavailableApp(errors.New("unreadable contact store"))
	if u.workspaceOpen {
		t.Fatal("unavailable app started with the right pane open")
	}

	storePath := filepath.Join(t.TempDir(), "contacts.json")
	t.Setenv("SAKI_GUI_STORE", storePath)
	t.Setenv("SAKI_SESSION_DIR", t.TempDir())
	t.Setenv("SAKI_GUI_WORKDIR", t.TempDir())
	r, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if r.workspaceOpen {
		t.Fatal("first runtime app started with the right pane open")
	}
}

func TestLayoutPersistsGlobalPreferenceAndReloads(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store}
	applyStoreSelection(a, store)
	a.leftPaneWidth = 225
	a.rightPaneWidth = 365
	a.workspaceOpen = true
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Layout == nil || reloaded.state.Layout.LeftWidth != 225 ||
		reloaded.state.Layout.RightWidth != 365 || !reloaded.state.Layout.WorkspaceOpen {
		t.Fatalf("stored layout = %+v", reloaded.state.Layout)
	}
	b := &appModel{}
	applyStoreSelection(b, reloaded)
	if !b.workspaceOpen || b.railWidth() != 225 || b.workWidth() != 365 {
		t.Fatalf("reloaded layout = open=%v left=%v right=%v", b.workspaceOpen, b.railWidth(), b.workWidth())
	}
}

func TestLayoutDragClampsBothEdgesAndKeepsCenterWidest(t *testing.T) {
	a, ct := layoutAppAt(t, 1180)
	ct.Draft = "keep this draft"
	ct.ActiveTaskID = "task-keep"
	ct.LastTaskID = "task-keep"
	ct.Messages = []*message{{ID: 1, Kind: msgContact, Text: "keep this message"}}
	tr := ui.NewTester(a.view, 1180, 720)
	tr.Frame()

	tr.Press(200, 300)
	tr.Move(260, 300)
	// Still held: OnChange must clamp before the release/rebuild.
	if center := float32(1180) - a.railWidth() - a.workWidth() - 2*paneSeparatorWidth; center < minCenterPaneWidth || center <= a.railWidth() || center <= a.workWidth() {
		t.Fatalf("left held center = %v, left=%v right=%v", center, a.railWidth(), a.workWidth())
	}
	tr.Release(260, 300)
	tr.Frame()
	if a.railWidth() < minLeftPaneWidth || a.railWidth() > maxLeftPaneWidth {
		t.Fatalf("left drag width = %v", a.railWidth())
	}
	if center := float32(1180) - a.railWidth() - a.workWidth() - 2*paneSeparatorWidth; center < minCenterPaneWidth || center <= a.railWidth() || center <= a.workWidth() {
		t.Fatalf("left drag center = %v, left=%v right=%v", center, a.railWidth(), a.workWidth())
	}
	if ct.Draft != "keep this draft" || ct.ActiveTaskID != "task-keep" || len(ct.Messages) != 1 {
		t.Fatal("left divider drag changed chat/task state")
	}
	if r, ok := tr.Find("Close workspace"); !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > 1180 {
		t.Fatalf("close-workspace control after left drag = ok=%v rect=%+v", ok, r)
	}

	a, ct = layoutAppAt(t, 1180)
	tr = ui.NewTester(a.view, 1180, 720)
	tr.Frame()
	rightX := float32(1180) - a.workWidth() - paneSeparatorWidth
	tr.Press(rightX, 300)
	tr.Move(rightX-100, 300)
	if center := float32(1180) - a.railWidth() - a.workWidth() - 2*paneSeparatorWidth; center < minCenterPaneWidth || center <= a.railWidth() || center <= a.workWidth() {
		t.Fatalf("right held center = %v, left=%v right=%v", center, a.railWidth(), a.workWidth())
	}
	tr.Release(rightX-100, 300)
	tr.Frame()
	if a.workWidth() < minRightPaneWidth || a.workWidth() > maxRightPaneWidth {
		t.Fatalf("right drag width = %v", a.workWidth())
	}
	if center := float32(1180) - a.railWidth() - a.workWidth() - 2*paneSeparatorWidth; center < minCenterPaneWidth || center <= a.railWidth() || center <= a.workWidth() {
		t.Fatalf("right drag center = %v, left=%v right=%v", center, a.railWidth(), a.workWidth())
	}
	if ct.Draft != "" || ct.ActiveTaskID != "" || len(ct.Messages) != 0 {
		t.Fatal("right divider drag changed chat/task state")
	}
}

func TestResponsiveWorkbenchGeometryFirstSlice(t *testing.T) {
	for _, tc := range []struct {
		width       int
		left, right float32
		wantAvatar  bool
		wantPage    bool
	}{
		{width: 899, left: 200, right: 340, wantPage: true},
		{width: 900, left: 200, right: 440, wantAvatar: true},
		{width: 901, left: 200, right: 440, wantAvatar: true},
		{width: 960, left: 200, right: 340, wantAvatar: true},
		{width: 960, left: 280, right: 440, wantAvatar: true},
		{width: 1040, left: 200, right: 340, wantAvatar: true},
		{width: 1040, left: 280, right: 440, wantAvatar: true},
		{width: 1179, left: 280, right: 440, wantAvatar: true},
		{width: 1180, left: 200, right: 340},
		{width: 1180, left: 280, right: 440},
		{width: 1181, left: 200, right: 340},
		{width: 1181, left: 280, right: 440},
	} {
		t.Run(fmt.Sprintf("%d/%g-%g", tc.width, tc.left, tc.right), func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			a.windowWidth = tc.width
			a.leftPaneWidth = tc.left
			a.rightPaneWidth = tc.right
			a.workspaceOpen = true
			tr := ui.NewTester(a.view, tc.width, 640)
			tr.Frame()
			t.Logf("width=%d left=%g right=%g overlay=%v avatar=%v page=%v effectiveRail=%g effectiveDetail=%g center=%g",
				tc.width, tc.left, tc.right, a.workOverlay(), a.avatarRailActive(), a.detailPageActive(),
				a.effectiveRailWidth(), a.effectiveDetailWidth(), a.centerPaneWidth)
			if a.workOverlay() != tc.wantPage || a.avatarRailActive() != tc.wantAvatar || a.detailPageActive() != tc.wantPage {
				t.Fatalf("mode mismatch width=%d", tc.width)
			}
			if !tc.wantPage {
				composer, ok := tr.Find("Composer for Saki")
				if !ok || composer.W <= 0 {
					t.Fatalf("composer missing at %d: %+v/%v", tc.width, composer, ok)
				}
				t.Logf("composer rect=%+v textareaW=%g contentW=%g", composer, composer.W, composer.W-4)
				mainStart := float32(0)
				if a.avatarRailActive() {
					mainStart = avatarRailWidth + paneSeparatorWidth
					avatar, avatarOK := tr.Find(locales.In("en").AvatarContactLabel("Saki"))
					if !avatarOK || avatar.W <= 0 || avatar.X < 0 || avatar.X+avatar.W > avatarRailWidth {
						t.Fatalf("avatar button rect=%+v ok=%v", avatar, avatarOK)
					}
				} else {
					mainStart = a.effectiveRailWidth() + paneSeparatorWidth
				}
				if composer.X < mainStart || composer.X+composer.W > mainStart+a.centerPaneWidth {
					t.Fatalf("composer outside center: rect=%+v mainStart=%g center=%g", composer, mainStart, a.centerPaneWidth)
				}
				if close, ok := tr.Find("Close workspace"); ok {
					t.Logf("close rect=%+v", close)
					if close.W <= 0 || close.H <= 0 || close.X < 0 || close.X+close.W > float32(tc.width) {
						t.Fatalf("close workspace outside window: %+v", close)
					}
				} else {
					t.Fatal("Close workspace missing from open detail")
				}
				if empty, ok := tr.Find("No issues in Unfinished."); ok {
					t.Logf("empty rect=%+v", empty)
				}
				if tc.width == 960 && tc.left == 280 && tc.right == 440 {
					if composer.W-4 < 440 {
						t.Fatalf("960 max pref editable content=%g, want >=440", composer.W-4)
					}
					focusComposer(t, tr, "Composer for Saki")
					caret, caretOK := tr.TextCaret()
					if !caretOK || caret.X < composer.X+2 || caret.X+caret.W > composer.X+composer.W-2 {
						t.Fatalf("composer caret/content inset = caret=%+v ok=%v composer=%+v", caret, caretOK, composer)
					}
					tr.Type("responsive draft")
					tr.Frame()
					if ct.Draft != "responsive draft" {
						t.Fatalf("960 composer did not accept typing: draft=%q", ct.Draft)
					}
				}
			} else if tr.HasText("Composer for Saki") {
				t.Fatal("composer mounted on 899 detail page")
			}
		})
	}
}

func TestResponsiveAvatarRailKeyboardAndStatus(t *testing.T) {
	a, saki := workspaceTestApp(t)
	a.windowWidth = 960
	a.workspaceOpen = true
	saki.Status = statusWorking
	waiting := &contact{ID: "contact-waiting", Name: "Waiting", Role: "Assistant", Status: statusReady, Loaded: true}
	waiting.Issues = []*productIssue{{ID: "issue-waiting", ContactID: waiting.ID, Goal: "waiting task",
		Status: issueStatusWaitingUser, Questions: []*issueQuestion{{Ref: "wait-q", Text: "answer", State: issueQuestionOpen}}}}
	attention := &contact{ID: "contact-attention", Name: "Attention", Role: "Assistant", Status: statusReady, Loaded: true}
	attention.Issues = []*productIssue{{ID: "issue-attention", ContactID: attention.ID, Goal: "attention task",
		Status: issueStatusNeedsAttention}}
	active := &contact{ID: "contact-active", Name: "Active", Role: "Assistant", Status: statusWorking, Loaded: true}
	active.Issues = []*productIssue{{ID: "issue-active", ContactID: active.ID, Goal: "active task",
		Status: issueStatusInProgress, Work: issueWorkSession{Loaded: true, ActiveTaskID: "active-task", SendState: sendAccepted}}}
	a.contacts = append(a.contacts, waiting, attention, active)
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	for _, label := range []string{locales.In("en").AvatarAddContact(), locales.In("en").AvatarContactLabel("Saki"), locales.In("en").AvatarContactLabel("Waiting"), locales.In("en").AvatarContactLabel("Attention"), locales.In("en").AvatarContactLabel("Active"), "Settings"} {
		if r, ok := tr.Find(label); !ok || r.X < 0 || r.X+r.W > avatarRailWidth {
			t.Fatalf("avatar rail control %q rect=%+v ok=%v", label, r, ok)
		}
	}
	saki.Draft = "draft-saki"
	waiting.Draft = "draft-waiting"
	attention.Draft = "draft-attention"
	if err := tr.Click(locales.In("en").AvatarContactLabel("Saki")); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.selectedContact().ID != saki.ID || saki.Draft != "draft-saki" || !a.detailIs(detailWork) {
		t.Fatalf("select Saki = selected=%q draft=%q detail=%q", a.selectedContact().ID, saki.Draft, a.activeDetail())
	}
	if texts := strings.Join(tr.Texts(), "\n"); !strings.Contains(texts, "Working") {
		t.Fatalf("working avatar status not rendered: %v", tr.Texts())
	}

	tr.Key(0, ui.KeyTab)
	tr.Frame()
	waitingLabel := locales.In("en").AvatarContactLabel("Waiting")
	if !tr.Focused(waitingLabel) || a.selectedContact().ID != saki.ID {
		t.Fatalf("Tab did not focus Waiting without selecting: focused=%v selected=%q", tr.Focused(waitingLabel), a.selectedContact().ID)
	}
	if texts := strings.Join(tr.Texts(), "\n"); !strings.Contains(texts, "Waiting for you") {
		t.Fatalf("waiting avatar status not rendered: %v", tr.Texts())
	}
	tr.Key(0, ui.KeySpace)
	tr.Frame()
	if a.selectedContact().ID != waiting.ID || waiting.Draft != "draft-waiting" || !a.detailIs(detailWork) {
		t.Fatalf("Space did not select Waiting: selected=%q draft=%q detail=%q", a.selectedContact().ID, waiting.Draft, a.activeDetail())
	}

	tr.Key(0, ui.KeyTab)
	tr.Frame()
	attentionLabel := locales.In("en").AvatarContactLabel("Attention")
	if !tr.Focused(attentionLabel) || a.selectedContact().ID != waiting.ID {
		t.Fatalf("Tab did not focus Attention without selecting: focused=%v selected=%q", tr.Focused(attentionLabel), a.selectedContact().ID)
	}
	if texts := strings.Join(tr.Texts(), "\n"); !strings.Contains(texts, "Needs attention") {
		t.Fatalf("attention avatar status not rendered: %v", tr.Texts())
	}
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if a.selectedContact().ID != attention.ID || attention.Draft != "draft-attention" || !a.detailIs(detailWork) {
		t.Fatalf("Enter did not select Attention: selected=%q draft=%q detail=%q", a.selectedContact().ID, attention.Draft, a.activeDetail())
	}

	activeLabel := locales.In("en").AvatarContactLabel("Active")
	sakiLabel := locales.In("en").AvatarContactLabel("Saki")
	tr.Key(0, ui.KeyTab)
	tr.Frame()
	if !tr.Focused(activeLabel) {
		t.Fatalf("Tab did not reach Active avatar: %v", tr.Texts())
	}
	if texts := strings.Join(tr.Texts(), "\n"); !strings.Contains(texts, "In progress") ||
		!strings.Contains(texts, "1 active task") || !strings.Contains(texts, "active task") {
		t.Fatalf("focused active avatar tooltip/AX = %v", tr.Texts())
	}
	for _, step := range []struct {
		mods ui.Modifiers
		want string
	}{
		{mods: ui.Shift, want: attentionLabel},
		{mods: ui.Shift, want: waitingLabel},
		{mods: ui.Shift, want: sakiLabel},
	} {
		tr.Key(step.mods, ui.KeyTab)
		tr.Frame()
		if !tr.Focused(step.want) {
			t.Fatalf("ShiftTab did not reach %q: %v", step.want, tr.Texts())
		}
	}
	if got := a.avatarRailStatus(locales.In("en"), active); !strings.Contains(got, "In progress") || !strings.Contains(got, "1 active task") {
		t.Fatalf("active avatar typed status = %q", got)
	}
	if got := a.avatarRailStatus(locales.In("en"), waiting); !strings.Contains(got, "Waiting for you") {
		t.Fatalf("waiting avatar typed status = %q", got)
	}
	if got := a.avatarRailStatus(locales.In("en"), attention); !strings.Contains(got, "Needs attention") {
		t.Fatalf("attention avatar typed status = %q", got)
	}
	if got := a.avatarRailStatus(locales.In("en"), saki); !strings.Contains(got, "Working") {
		t.Fatalf("working avatar typed status = %q", got)
	}

}

func TestResponsiveManualHideAndWideDrag(t *testing.T) {
	for _, tc := range []struct {
		width         int
		wantDetailMin float32
	}{
		{width: 900, wantDetailMin: 300},
		{width: 960, wantDetailMin: 300},
		{width: 1180, wantDetailMin: 280},
	} {
		t.Run(fmt.Sprintf("hidden-%d", tc.width), func(t *testing.T) {
			a, _ := workspaceTestApp(t)
			a.windowWidth = tc.width
			a.workspaceOpen = true
			a.railCollapsed = true
			a.rightPaneWidth = 440
			tr := ui.NewTester(a.view, tc.width, 640)
			tr.Frame()
			if _, ok := tr.Find(locales.In("en").AvatarContactLabel("Saki")); ok {
				t.Fatal("manual hide still mounted an avatar")
			}
			if a.avatarRailActive() || a.effectiveRailWidth() != 0 || a.effectiveDetailWidth() < tc.wantDetailMin {
				t.Fatalf("%d hidden geometry avatar=%v rail=%v detail=%v", tc.width, a.avatarRailActive(), a.effectiveRailWidth(), a.effectiveDetailWidth())
			}
			composer, ok := tr.Find("Composer for Saki")
			if !ok || composer.W-4 < 440 {
				t.Fatalf("%d hidden composer=%+v ok=%v content=%g", tc.width, composer, ok, composer.W-4)
			}
			if err := tr.Click("Contacts"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.railCollapsed {
				t.Fatal("manual Contacts did not restore the rail")
			}
			if tc.width < 1180 {
				if !a.avatarRailActive() {
					t.Fatal("manual Contacts did not restore the avatar rail in mid band")
				}
			} else if a.avatarRailActive() {
				t.Fatal("manual Contacts restored avatar rail at full-rail width")
			}
			contactLabel := "Contact Saki"
			addLabel := "Add contact"
			if tc.width < 1180 {
				contactLabel = locales.In("en").AvatarContactLabel("Saki")
				addLabel = locales.In("en").AvatarAddContact()
			}
			if _, ok := tr.Find(contactLabel); !ok {
				t.Fatal("restored rail lacks its contact")
			}
			if _, ok := tr.Find(addLabel); !ok {
				t.Fatal("restored rail lacks Add contact")
			}
			if _, ok := tr.Find("Settings"); !ok && tc.width < 1180 {
				t.Fatal("restored avatar rail lacks Settings")
			}
			if _, ok := tr.Find("App actions"); !ok && tc.width >= 1180 {
				t.Fatal("restored full rail lacks Settings toolbar group")
			}
		})
	}

	a, _ := workspaceTestApp(t)
	a.windowWidth = 1180
	a.leftPaneWidth = 200
	a.rightPaneWidth = 440
	a.workspaceOpen = true
	tr := ui.NewTester(a.view, 1180, 720)
	tr.Frame()
	beforeRight := a.workWidth()
	tr.Press(200, 300)
	tr.Move(400, 300)
	tr.Release(400, 300)
	tr.Frame()
	if a.railWidth() != 280 || a.workWidth() != beforeRight || a.effectiveDetailWidth() != 406 || a.centerPaneWidth != 492 {
		t.Fatalf("wide left drag geometry = left=%v prefRight=%v detail=%v center=%v", a.railWidth(), a.workWidth(), a.effectiveDetailWidth(), a.centerPaneWidth)
	}
}
func TestResponsiveChatScrollSurvivesResizeAndDetailClose(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 960
	a.workspaceOpen = true
	for i := 0; i < 40; i++ {
		ct.Messages = append(ct.Messages, &message{Key: fmt.Sprintf("message-%02d", i), Kind: msgContact,
			Text: fmt.Sprintf("visible responsive message %02d", i)})
	}
	ct.list.ScrollTo(10, ui.Start)
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	first, last := ct.list.Visible()
	anchorKey := ct.Messages[first].Key
	anchorText := ct.Messages[first].Text
	if first <= 0 || last <= first || !tr.HasText(anchorText) {
		t.Fatalf("initial chat scroll first=%d last=%d messages=%v", first, last, tr.Texts())
	}
	for _, width := range []int{1040, 1180, 960} {
		a.windowWidth = width
		tr.SetSize(width, 640)
		tr.Frame()
		nextFirst, nextLast := ct.list.Visible()
		if nextFirst != first || nextLast <= nextFirst || ct.Messages[nextFirst].Key != anchorKey || !tr.HasText(anchorText) {
			t.Fatalf("width %d changed chat anchor first=%d->%d last=%d key=%q anchor=%q texts=%v",
				width, first, nextFirst, nextLast, ct.Messages[nextFirst].Key, anchorKey, tr.Texts())
		}
	}
	a.closeDetail()
	tr.Frame()
	nextFirst, nextLast := ct.list.Visible()
	if nextFirst != first || nextLast <= nextFirst || ct.Messages[nextFirst].Key != anchorKey || !tr.HasText(anchorText) {
		t.Fatalf("detail close changed chat anchor first=%d->%d last=%d key=%q anchor=%q", first, nextFirst, nextLast, ct.Messages[nextFirst].Key, anchorKey)
	}
}
func TestResponsiveResizePreservesDetailContext(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	requirements := ""
	for i := 0; i < 30; i++ {
		requirements += fmt.Sprintf("responsive requirement %d\n", i)
	}
	issue := &productIssue{ID: "i-responsive", ContactID: ct.ID, Goal: "responsive context", Status: issueStatusInProgress,
		Requirements: parseIssueRequirements(requirements)}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.Draft = "keep responsive draft"
	nav := ct.issueNavigation()
	nav.Filter = issueFilterAll
	nav.DetailID = issue.ID
	nav.OverviewScroll.Y = 77
	openIssueDetailForTest(a, ct, issue.ID)
	nav.OverviewScroll.Y = 77
	nav.DetailScroll.Y = 0
	tr := ui.NewTester(a.view, 1040, 640)
	tr.Frame()
	tr.Scroll(800, 350, 0, 220)
	tr.Frame()
	detailY := nav.DetailScroll.Y
	if detailY <= 0 {
		t.Fatalf("detail content did not actually scroll: %v", detailY)
	}
	for _, width := range []int{899, 900, 1040, 1180, 960} {
		a.windowWidth = width
		tr.SetSize(width, 640)
		tr.Frame()
		if !a.workspaceOpen || nav.DetailID != issue.ID || nav.Filter != issueFilterAll ||
			ct.SelectedIssueID != issue.ID || ct.Draft != "keep responsive draft" || nav.OverviewScroll.Y != 77 || nav.DetailScroll.Y != detailY {
			t.Fatalf("width %d changed detail context: open=%v nav=%+v selected=%q draft=%q", width, a.workspaceOpen, nav, ct.SelectedIssueID, ct.Draft)
		}
	}
}
func TestResponsiveClosedDetailKeepsFullRailAndNoAutoOpen(t *testing.T) {
	for _, width := range []int{899, 900, 901, 960, 1040, 1179, 1180, 1181} {
		t.Run(fmt.Sprintf("%d", width), func(t *testing.T) {
			a, _ := workspaceTestApp(t)
			a.windowWidth = width
			a.leftPaneWidth = 280
			a.rightPaneWidth = 440
			a.workspaceOpen = false
			tr := ui.NewTester(a.view, width, 640)
			tr.Frame()
			if a.avatarRailActive() || a.detailPageActive() || !tr.HasText("Contact Saki") {
				t.Fatalf("closed detail at %d mounted avatar/page or lost full rail", width)
			}
			if tr.HasText("Close workspace") {
				t.Fatalf("closed detail at %d rendered a detail close", width)
			}
			if r, ok := tr.Find("Composer for Saki"); !ok || r.W-4 < 440 {
				t.Fatalf("closed detail composer at %d = %+v ok=%v", width, r, ok)
			}
			a.windowWidth = 1180
			tr.SetSize(1180, 640)
			tr.Frame()
			if a.workspaceOpen || a.avatarRailActive() {
				t.Fatalf("resize opened detail at %d", width)
			}
			if !tr.HasText("Contact Saki") {
				t.Fatal("closed resize lost full rail")
			}
		})
	}
}

func TestLayoutPreferenceIsNotOverwrittenByNarrowViewport(t *testing.T) {
	a, _ := layoutAppAt(t, 1380)
	a.leftPaneWidth = 280
	a.rightPaneWidth = 440
	tr := ui.NewTester(a.view, 1380, 900)
	tr.Frame()
	if a.workOverlay() {
		t.Fatal("1380 with minimum center should remain three-pane")
	}
	a.windowWidth = 960
	tr.SetSize(960, 640)
	tr.Frame()
	if a.workOverlay() || !a.avatarRailActive() || a.effectiveDetailWidth() != 410 || a.centerPaneWidth != 492 {
		t.Fatalf("960 avatar geometry = overlay=%v avatar=%v detail=%v center=%v",
			a.workOverlay(), a.avatarRailActive(), a.effectiveDetailWidth(), a.centerPaneWidth)
	}
	if a.railWidth() != 280 || a.workWidth() != 440 {
		t.Fatalf("narrow viewport rewrote preference: left=%v right=%v", a.railWidth(), a.workWidth())
	}
	a.windowWidth = 1380
	tr.SetSize(1380, 900)
	tr.Frame()
	if a.workOverlay() || a.railWidth() != 280 || a.workWidth() != 440 {
		t.Fatalf("wide viewport did not restore preference: overlay=%v left=%v right=%v", a.workOverlay(), a.railWidth(), a.workWidth())
	}
}

func TestLayoutOverlayBelow900ClosesWithEscape(t *testing.T) {
	for _, width := range []int{899, 720} {
		a, _ := layoutAppAt(t, width)
		tr := ui.NewTester(a.view, width, 640)
		tr.Frame()
		if !a.workOverlay() || !tr.HasText("Back to chat") {
			t.Fatalf("%d overlay missing: overlay=%v", width, a.workOverlay())
		}
		tr.Key(0, ui.KeyEscape)
		tr.Frame()
		if a.workspaceOpen || !tr.Focused("Work") {
			t.Fatalf("%d Escape = open=%v focusedWork=%v", width, a.workspaceOpen, tr.Focused("Work"))
		}
	}
}

func TestResponsivePseudoExpandedSettingsLabel(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 1180
	a.workspaceOpen = true
	tr := ui.NewTester(a.view, 1180, 640)
	tr.SetLocale("en")
	tr.Frame()
	en := locales.In("en").SettingsTitle()
	if !tr.HasText(en) {
		t.Fatalf("English Settings label missing: %v", tr.Texts())
	}
	tr.SetLocale("en-XA")
	tr.Frame()
	xa := locales.In("en-XA").SettingsTitle()
	if xa == en || len(xa) <= len(en) {
		t.Fatalf("en-XA Settings label did not expand: en=%q xa=%q", en, xa)
	}
	r, ok := tr.Find(xa)
	if !ok || r.W <= 0 || r.X < 0 || r.X+r.W > 1180 {
		t.Fatalf("expanded Settings label rect=%+v ok=%v", r, ok)
	}
	if tr.HasText(en) {
		t.Fatalf("en-XA fell back to English Settings label: %v", tr.Texts())
	}
	a.windowWidth = 960
	tr.SetSize(960, 640)
	tr.Frame()
	enAdd := locales.In("en").AvatarAddContact()
	xaAdd := locales.In("en-XA").AvatarAddContact()
	enContact := locales.In("en").AvatarContactLabel("Saki")
	xaContact := locales.In("en-XA").AvatarContactLabel("Saki")
	if xaAdd == enAdd || len(xaAdd) <= len(enAdd) || xaContact == enContact || len(xaContact) <= len(enContact) {
		t.Fatalf("en-XA avatar labels did not expand: add %q/%q contact %q/%q", enAdd, xaAdd, enContact, xaContact)
	}
	for _, label := range []string{xaAdd, xaContact} {
		if r, ok := tr.Find(label); !ok || r.X < 0 || r.X+r.W > avatarRailWidth {
			t.Fatalf("en-XA avatar label %q rect=%+v ok=%v", label, r, ok)
		}
	}
}
func TestResponsive899To900FocusCounterpart(t *testing.T) {
	for _, kind := range []detailKind{detailWork, detailHistory, detailProfile} {
		t.Run(string(kind), func(t *testing.T) {
			a, _ := workspaceTestApp(t)
			a.windowWidth = 899
			a.workspaceOpen = false
			a.detail = ""
			a.openDetail(kind)
			tr := ui.NewTester(a.view, 899, 640)
			tr.Frame()
			pageLabel := "Close history"
			if kind == detailWork {
				pageLabel = "Back to chat"
			} else if kind == detailProfile {
				pageLabel = "Close profile"
			}
			if !a.detailPageActive() || !tr.Focused(pageLabel) {
				t.Fatalf("899 %s page focus = page=%v focused=%v", kind, a.detailPageActive(), tr.Focused(pageLabel))
			}
			a.windowWidth = 900
			tr.SetSize(900, 640)
			tr.Frame()
			wideLabel := "Close history"
			if kind == detailWork {
				wideLabel = "Close workspace"
			} else if kind == detailProfile {
				wideLabel = "Close profile"
			}
			if a.detailPageActive() || a.workOverlay() || !tr.Focused(wideLabel) {
				t.Fatalf("900 %s counterpart focus = page=%v overlay=%v focused=%v", kind, a.detailPageActive(), a.workOverlay(), tr.Focused(wideLabel))
			}
			a.windowWidth = 899
			tr.SetSize(899, 640)
			tr.Frame()
			if !a.detailPageActive() || !tr.Focused(pageLabel) {
				t.Fatalf("899 return %s focus = page=%v focused=%v", kind, a.detailPageActive(), tr.Focused(pageLabel))
			}
		})
	}
}

func TestLayoutEventsAndContactSwitchDoNotToggleWork(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	a.contacts = append(a.contacts, &contact{ID: "mio", Name: "Mio", Role: "Assistant"})
	a.selectContact(1)
	if a.workspaceOpen {
		t.Fatal("contact switch opened work")
	}
	a.applyEvent(ct, runtimeEventEnvelope{Version: 1, Type: "event", TaskID: "t1",
		Event: &runtimeEvent{Type: "task_started"}})
	if a.workspaceOpen {
		t.Fatal("task start opened work")
	}
	a.applyEvent(ct, runtimeEventEnvelope{Version: 1, Type: "event", TaskID: "t1",
		Event: &runtimeEvent{Type: "task_finished", Status: "completed"}})
	if a.workspaceOpen {
		t.Fatal("task finish opened work")
	}
	a.workspaceOpen = true
	a.selectContact(0)
	a.applyEvent(ct, runtimeEventEnvelope{Version: 1, Type: "event", TaskID: "t2",
		Event: &runtimeEvent{Type: "task_started"}})
	if !a.workspaceOpen {
		t.Fatal("task start closed work")
	}
}

func TestClosedWorkEntryShowsRealWorkStatus(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	ct.Status = statusWorking
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Working") || len(ct.Messages) != 0 || issueOpenCount(ct) != 0 || a.issueWorkNotice(ct).ActiveCount != 0 {
		t.Fatalf("contact working hint = %v messages=%d open=%d", tr.Texts(), len(ct.Messages), issueOpenCount(ct))
	}
	ct.Status = statusAttention
	tr.Frame()
	if !tr.HasText("Needs attention") || len(ct.Messages) != 0 || issueOpenCount(ct) != 0 || a.issueWorkNotice(ct).AttentionCount != 0 {
		t.Fatalf("contact attention hint = %v messages=%d open=%d", tr.Texts(), len(ct.Messages), issueOpenCount(ct))
	}
}

func TestLayoutDragIsNotPersistedPerMove(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store}
	applyStoreSelection(a, store)
	a.windowWidth = 1180
	a.workspaceOpen = true
	tr := ui.NewTester(a.view, 1180, 720)
	tr.Frame()
	tr.Press(200, 300)
	tr.Move(240, 300)
	tr.Release(240, 300)
	tr.Frame()
	if a.railWidth() == defaultLeftPaneWidth {
		t.Fatal("drag did not change the in-memory left width")
	}
	if store.state.Layout != nil {
		t.Fatal("dragging wrote the store before an explicit boundary")
	}
	a.setWorkspaceOpen(false)
	if store.state.Layout == nil || store.state.Layout.WorkspaceOpen || store.state.Layout.LeftWidth != a.railWidth() {
		t.Fatalf("explicit close layout = %+v", store.state.Layout)
	}
	reloaded, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Layout == nil || reloaded.state.Layout.WorkspaceOpen || reloaded.state.Layout.LeftWidth != a.railWidth() {
		t.Fatalf("reloaded explicit layout = %+v", reloaded.state.Layout)
	}
}

func TestLayoutExplicitIssueExecutionOpensAndClosesWork(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	issue := &productIssue{ID: "i-layout", ContactID: ct.ID, Goal: "open execution", Status: issueStatusInProgress, RequirementsRevision: 1}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.Works = []*workItem{{TaskID: "t-layout", State: workCompleted}}
	issue.Executions = []*issueExecution{{ID: "e-layout", TaskID: "t-layout", Status: issueExecutionCompleted}}
	a.showIssueWork(issue.ID, "t-layout")
	if !a.workspaceOpen || ct.SelectedWorkTaskID != "t-layout" {
		t.Fatalf("explicit execution open = open=%v selected=%q", a.workspaceOpen, ct.SelectedWorkTaskID)
	}
	a.closeWorkspace()
	if a.workspaceOpen {
		t.Fatal("explicit execution close did not close work")
	}
}

func TestLayoutRightDividerKeyboardUsesPhysicalDirection(t *testing.T) {
	a, _ := layoutAppAt(t, 1040)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	rightX := float32(1040) - a.workWidth() - paneSeparatorWidth
	tr.ClickAt(rightX, 300)
	tr.Frame()
	before := a.workWidth()
	tr.Key(0, ui.KeyLeft)
	tr.Frame()
	if a.workWidth() <= before {
		t.Fatalf("Left on right divider should widen work: before=%v after=%v", before, a.workWidth())
	}
	tr.Key(0, ui.KeyRight)
	tr.Frame()
	if a.workWidth() != before {
		t.Fatalf("Right on right divider should restore work: before=%v after=%v", before, a.workWidth())
	}
}

func TestLayoutWideDragAllowsPreferredRanges(t *testing.T) {
	a, _ := layoutAppAt(t, 1380)
	tr := ui.NewTester(a.view, 1380, 900)
	tr.Frame()
	tr.Press(200, 400)
	tr.Move(400, 400)
	tr.Release(400, 400)
	tr.Frame()
	if a.railWidth() != maxLeftPaneWidth {
		t.Fatalf("wide left max = %v", a.railWidth())
	}

	a, _ = layoutAppAt(t, 1380)
	tr = ui.NewTester(a.view, 1380, 900)
	tr.Frame()
	rightX := float32(1380) - a.workWidth() - paneSeparatorWidth
	tr.Press(rightX, 400)
	tr.Move(rightX-300, 400)
	tr.Release(rightX-300, 400)
	tr.Frame()
	if a.workWidth() != maxRightPaneWidth {
		t.Fatalf("wide right max = %v", a.workWidth())
	}
	if center := float32(1380) - a.railWidth() - a.workWidth() - 2*paneSeparatorWidth; center < minCenterPaneWidth {
		t.Fatalf("wide center = %v", center)
	}
}

func TestLayoutDragHonorsMinimumPaneWidths(t *testing.T) {
	a, _ := layoutAppAt(t, 1180)
	tr := ui.NewTester(a.view, 1180, 720)
	tr.Frame()
	tr.Press(200, 300)
	tr.Move(0, 300)
	tr.Release(0, 300)
	tr.Frame()
	if a.railWidth() != minLeftPaneWidth {
		t.Fatalf("left minimum = %v", a.railWidth())
	}

	a, _ = layoutAppAt(t, 1180)
	tr = ui.NewTester(a.view, 1180, 720)
	tr.Frame()
	rightX := float32(1180) - a.workWidth() - paneSeparatorWidth
	tr.Press(rightX, 300)
	tr.Move(1180, 300)
	tr.Release(1180, 300)
	tr.Frame()
	if a.workWidth() != minRightPaneWidth {
		t.Fatalf("right minimum = %v", a.workWidth())
	}
}

func TestLayoutExplicitTogglePersistsOpenAndClosed(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store}
	applyStoreSelection(a, store)
	if a.workspaceOpen {
		t.Fatal("new layout app must start closed")
	}
	a.setWorkspaceOpen(true)
	if store.state.Layout == nil || !store.state.Layout.WorkspaceOpen {
		t.Fatalf("open was not persisted: %+v", store.state.Layout)
	}
	a.setWorkspaceOpen(false)
	if store.state.Layout == nil || store.state.Layout.WorkspaceOpen {
		t.Fatalf("close was not persisted: %+v", store.state.Layout)
	}
	reloaded, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Layout == nil || reloaded.state.Layout.WorkspaceOpen {
		t.Fatalf("reloaded closed layout = %+v", reloaded.state.Layout)
	}
}

func TestLayoutRuntimeEntryLoadsSavedPreference(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "contacts.json")
	store, err := openContactStore(storePath, dir)
	if err != nil {
		t.Fatal(err)
	}
	store.state.Layout = &storedLayout{LeftWidth: 230, RightWidth: 360, WorkspaceOpen: true}
	if err := store.saveState(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAKI_GUI_STORE", storePath)
	t.Setenv("SAKI_SESSION_DIR", filepath.Join(dir, "sessions"))
	t.Setenv("SAKI_GUI_WORKDIR", dir)
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if !a.workspaceOpen || a.railWidth() != 230 || a.workWidth() != 360 {
		t.Fatalf("runtime entry layout = open=%v left=%v right=%v", a.workspaceOpen, a.railWidth(), a.workWidth())
	}
}
