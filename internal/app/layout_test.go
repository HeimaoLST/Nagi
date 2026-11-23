package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func layoutAppAt(t *testing.T, width int) (*appModel, *contact) {
	t.Helper()
	a, ct := workspaceTestApp(t)
	a.windowWidth = width
	a.openContactProfile(ct.ID)
	return a, ct
}

func TestLayoutNewAndCurrentStoresUseClosedRightPaneDefaults(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if store.State.Layout != nil {
		t.Fatal("new store should not invent a layout preference")
	}
	a := &appModel{}
	applyStoreSelection(a, store)
	if a.workspaceOpen || a.railWidth() != defaultLeftPaneWidth || a.workWidth() != defaultRightPaneWidth {
		t.Fatalf("new-store layout = open=%v left=%v right=%v", a.workspaceOpen, a.railWidth(), a.workWidth())
	}

	store.State.Layout = &storedLayout{LeftWidth: 240, RightWidth: 380, WorkspaceOpen: true}
	if err := store.SaveState(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	applyStoreSelection(a, reopened)
	if a.workspaceOpen || a.railWidth() != 240 || a.workWidth() != 380 {
		t.Fatalf("current-store layout = open=%v left=%v right=%v", a.workspaceOpen, a.railWidth(), a.workWidth())
	}
}

func TestLayoutEntryPointsStartWithRightPaneClosed(t *testing.T) {
	u := newUnavailableApp(errors.New("unreadable contact store"))
	if u.workspaceOpen {
		t.Fatal("unavailable app started with the right pane open")
	}

	storePath := filepath.Join(t.TempDir(), "contacts.json")
	t.Setenv("NAGI_GUI_STORE", storePath)
	t.Setenv("NAGI_GUI_WORKDIR", t.TempDir())
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
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Layout == nil || reloaded.State.Layout.LeftWidth != 225 ||
		reloaded.State.Layout.RightWidth != 365 {
		t.Fatalf("stored layout = %+v", reloaded.State.Layout)
	}
	b := &appModel{}
	applyStoreSelection(b, reloaded)
	if b.workspaceOpen || b.railWidth() != 225 || b.workWidth() != 365 {
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

	leftDivider := a.railWidth()
	tr.Press(leftDivider, 300)
	tr.Move(leftDivider+60, 300)
	// Still held: OnChange must clamp before the release/rebuild.
	if center := float32(1180) - a.railWidth() - a.workWidth() - 2*paneSeparatorWidth; center < minCenterPaneWidth || center <= a.railWidth() || center <= a.workWidth() {
		t.Fatalf("left held center = %v, left=%v right=%v", center, a.railWidth(), a.workWidth())
	}
	tr.Release(leftDivider+60, 300)
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
	if r, ok := tr.Find("Close profile"); !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > 1180 {
		t.Fatalf("close-profile control after left drag = ok=%v rect=%+v", ok, r)
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
			a.openContactProfile(a.selectedID())
			tr := ui.NewTester(a.view, tc.width, 640)
			tr.Frame()
			t.Logf("width=%d left=%g right=%g overlay=%v avatar=%v page=%v effectiveRail=%g effectiveDetail=%g center=%g",
				tc.width, tc.left, tc.right, a.workOverlay(), a.avatarRailActive(), a.detailPageActive(),
				a.effectiveRailWidth(), a.effectiveDetailWidth(), a.centerPaneWidth)
			if a.workOverlay() != tc.wantPage || a.avatarRailActive() != tc.wantAvatar || a.detailPageActive() != tc.wantPage {
				t.Fatalf("mode mismatch width=%d", tc.width)
			}
			if !tc.wantPage {
				composer, ok := tr.Find("Composer for Nagi")
				if !ok || composer.W <= 0 {
					t.Fatalf("composer missing at %d: %+v/%v", tc.width, composer, ok)
				}
				t.Logf("composer rect=%+v textareaW=%g contentW=%g", composer, composer.W, composer.W-4)
				mainStart := float32(0)
				if a.avatarRailActive() {
					mainStart = avatarRailWidth + paneSeparatorWidth
					avatar, avatarOK := tr.Find(locales.In("en").AvatarContactLabel("Nagi"))
					if !avatarOK || avatar.W <= 0 || avatar.X < 0 || avatar.X+avatar.W > avatarRailWidth {
						t.Fatalf("avatar button rect=%+v ok=%v", avatar, avatarOK)
					}
				} else {
					mainStart = a.effectiveRailWidth() + paneSeparatorWidth
				}
				if composer.X < mainStart || composer.X+composer.W > mainStart+a.centerPaneWidth {
					t.Fatalf("composer outside center: rect=%+v mainStart=%g center=%g", composer, mainStart, a.centerPaneWidth)
				}
				if close, ok := tr.Find("Close profile"); ok {
					t.Logf("close rect=%+v", close)
					if close.W <= 0 || close.H <= 0 || close.X < 0 || close.X+close.W > float32(tc.width) {
						t.Fatalf("close profile outside window: %+v", close)
					}
				} else {
					t.Fatal("Close profile missing from open detail")
				}
				if tc.width == 960 && tc.left == 280 && tc.right == 440 {
					if composer.W-4 < 440 {
						t.Fatalf("960 max pref editable content=%g, want >=440", composer.W-4)
					}
					focusComposer(t, tr, "Composer for Nagi")
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
			} else if tr.HasText("Composer for Nagi") {
				t.Fatal("composer mounted on 899 detail page")
			}
		})
	}
}

func TestPreviewAvatarStatusMarkers(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write avatar marker previews")
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		a, saki := workspaceTestApp(t)
		a.windowWidth = 960
		a.openContactProfile(a.selectedID())
		saki.Status = statusWorking
		waiting := &contact{ID: "contact-waiting", Name: "Waiting", Role: "Assistant", Status: statusReady, Loaded: true}
		attention := &contact{ID: "contact-attention", Name: "Attention", Role: "Assistant", Status: statusAttention, Loaded: true}
		active := &contact{ID: "contact-active", Name: "Active", Role: "Assistant", Status: statusWorking, Loaded: true}
		a.contacts = append(a.contacts, waiting, attention, active)
		tr := ui.NewTester(a.view, 960, 640)
		tr.SetDark(dark)
		tr.Frame()
		writePreviewPNG(t, filepath.Join(dir, "avatar-status-"+theme+"-960x640.png"), tr.Image())
	}
}

func TestResponsiveAvatarRailKeyboardAndStatus(t *testing.T) {
	a, saki := workspaceTestApp(t)
	a.windowWidth = 960
	a.openContactProfile(a.selectedID())
	a.pi = presentationPiHost(a)
	saki.Status = statusWorking
	saki.Unread = 7
	waiting := &contact{ID: "contact-waiting", Name: "Waiting", Role: "Assistant", Status: statusReady, Loaded: true}
	attention := &contact{ID: "contact-attention", Name: "Attention", Role: "Assistant", Status: statusAttention, Loaded: true}
	active := &contact{ID: "contact-active", Name: "Active", Role: "Assistant", Status: statusWorking, Loaded: true}
	a.contacts = append(a.contacts, waiting, attention, active)
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	for _, marker := range []string{"▶", "!"} {
		if !tr.HasText(marker) {
			t.Fatalf("static avatar marker %q missing: %v", marker, tr.Texts())
		}
	}
	if tr.HasText("▶7") || tr.HasText("▶1") || tr.HasText("!1") {
		t.Fatalf("avatar status marker contains a task or unread count: %v", tr.Texts())
	}
	for _, label := range []string{locales.In("en").AvatarAddContact(), locales.In("en").AvatarContactLabel("Nagi"), locales.In("en").AvatarContactLabel("Waiting"), locales.In("en").AvatarContactLabel("Attention"), locales.In("en").AvatarContactLabel("Active"), "Settings"} {
		if r, ok := tr.Find(label); !ok || r.X < 0 || r.X+r.W > avatarRailWidth {
			t.Fatalf("avatar rail control %q rect=%+v ok=%v", label, r, ok)
		}
	}
	saki.Draft = "draft-saki"
	waiting.Draft = "draft-waiting"
	attention.Draft = "draft-attention"
	if err := tr.Click(locales.In("en").AvatarContactLabel("Nagi")); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.selectedContact() != saki || saki.Draft != "draft-saki" || !a.detailIs(detailProfile) {
		t.Fatalf("select Nagi = selected=%q draft=%q detail=%q", a.selectedID(), saki.Draft, a.activeDetail())
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
	if texts := strings.Join(tr.Texts(), "\n"); !strings.Contains(texts, "Ready") {
		t.Fatalf("ready avatar status not rendered: %v", tr.Texts())
	}
	tr.Key(0, ui.KeySpace)
	tr.Frame()
	if a.selectedContact() != waiting || waiting.Draft != "draft-waiting" || !a.detailIs(detailProfile) {
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
	if a.selectedContact() != attention || attention.Draft != "draft-attention" || !a.detailIs(detailProfile) {
		t.Fatalf("Enter did not select Attention: selected=%q draft=%q detail=%q", a.selectedContact().ID, attention.Draft, a.activeDetail())
	}

	activeLabel := locales.In("en").AvatarContactLabel("Active")
	sakiLabel := locales.In("en").AvatarContactLabel("Nagi")
	tr.Key(0, ui.KeyTab)
	tr.Frame()
	if !tr.Focused(activeLabel) {
		t.Fatalf("Tab did not reach Active avatar: %v", tr.Texts())
	}
	if texts := strings.Join(tr.Texts(), "\n"); !strings.Contains(texts, "Working") {
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
			a.openContactProfile(a.selectedID())
			a.railCollapsed = true
			a.rightPaneWidth = 440
			tr := ui.NewTester(a.view, tc.width, 640)
			tr.Frame()
			if _, ok := tr.Find(locales.In("en").AvatarContactLabel("Nagi")); ok {
				t.Fatal("manual hide still mounted an avatar")
			}
			if a.avatarRailActive() || a.effectiveRailWidth() != 0 || a.effectiveDetailWidth() < tc.wantDetailMin {
				t.Fatalf("%d hidden geometry avatar=%v rail=%v detail=%v", tc.width, a.avatarRailActive(), a.effectiveRailWidth(), a.effectiveDetailWidth())
			}
			composer, ok := tr.Find("Composer for Nagi")
			if !ok || composer.W-4 < 440 {
				t.Fatalf("%d hidden composer=%+v ok=%v content=%g", tc.width, composer, ok, composer.W-4)
			}
			if err := tr.Click(locales.In("en").RailContactsTitle()); err != nil {
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
			contactLabel := locales.In("en").AvatarContactLabel("Nagi")
			addLabel := locales.In("en").AvatarAddContact()
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
	a.openContactProfile(a.selectedID())
	tr := ui.NewTester(a.view, 1180, 720)
	tr.Frame()
	beforeRight := a.workWidth()
	tr.Press(200, 300)
	tr.Move(400, 300)
	tr.Release(400, 300)
	tr.Frame()
	composer, found := tr.Find(locales.In("en").ComposerLabel(a.selectedContact().Name))
	if a.railWidth() <= 200 || a.railWidth() >= 400 || a.workWidth() != beforeRight || a.effectiveDetailWidth() < 280 || !found || composer.W-4 < 440 {
		t.Fatalf("wide left drag geometry = left=%v prefRight=%v detail=%v center=%v", a.railWidth(), a.workWidth(), a.effectiveDetailWidth(), a.centerPaneWidth)
	}
}
func TestResponsiveChatScrollSurvivesResizeAndDetailClose(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 960
	a.openContactProfile(a.selectedID())
	for i := range 40 {
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
			if a.avatarRailActive() || a.detailPageActive() || !tr.HasText(locales.In("en").AvatarContactLabel("Nagi")) {
				t.Fatalf("closed detail at %d mounted avatar/page or lost full rail", width)
			}
			if tr.HasText("Close profile") {
				t.Fatalf("closed detail at %d rendered a detail close", width)
			}
			if r, ok := tr.Find("Composer for Nagi"); !ok || r.W-4 < 440 {
				t.Fatalf("closed detail composer at %d = %+v ok=%v", width, r, ok)
			}
			a.windowWidth = 1180
			tr.SetSize(1180, 640)
			tr.Frame()
			if a.workspaceOpen || a.avatarRailActive() {
				t.Fatalf("resize opened detail at %d", width)
			}
			if !tr.HasText(locales.In("en").AvatarContactLabel("Nagi")) {
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
	composer, found := tr.Find(locales.In("en").ComposerLabel(a.selectedContact().Name))
	if a.workOverlay() || !a.avatarRailActive() || a.effectiveDetailWidth() < 280 || !found || composer.W-4 < 440 {
		t.Fatalf("960 avatar geometry = overlay=%v avatar=%v detail=%v composer=%+v",
			a.workOverlay(), a.avatarRailActive(), a.effectiveDetailWidth(), composer)
	}
	if a.railWidth() != 280 || a.workWidth() != 440 {
		t.Fatalf("narrow viewport rewrote preference: left=%v right=%v", a.railWidth(), a.workWidth())
	}
	a.windowWidth = 1380
	tr.SetSize(1380, 900)
	tr.Frame()
	if a.workOverlay() || a.avatarRailActive() || a.railWidth() != 280 || a.workWidth() != 440 || a.effectiveDetailWidth() != 440 {
		t.Fatalf("wide viewport did not restore preference: overlay=%v left=%v right=%v", a.workOverlay(), a.railWidth(), a.workWidth())
	}
}

func TestLayoutOverlayBelow900ClosesWithEscape(t *testing.T) {
	for _, width := range []int{899, 720} {
		a, _ := layoutAppAt(t, width)
		tr := ui.NewTester(a.view, width, 640)
		tr.Frame()
		if !a.detailPageActive() || !tr.Focused("Close profile") {
			t.Fatalf("%d profile page was not active with its close control focused", width)
		}
		tr.Key(0, ui.KeyEscape)
		tr.Frame()
		if a.workspaceOpen || !tr.HasText("Composer for Nagi") {
			t.Fatalf("%d Escape did not return to chat: %v", width, tr.Texts())
		}
	}
}

func TestResponsivePseudoExpandedSettingsLabel(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 1180
	a.openContactProfile(a.selectedID())
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
	enContact := locales.In("en").AvatarContactLabel("Nagi")
	xaContact := locales.In("en-XA").AvatarContactLabel("Nagi")
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
	a, ct := workspaceTestApp(t)
	a.windowWidth = 899
	a.workspaceOpen = false
	a.openContactProfile(ct.ID)
	tr := ui.NewTester(a.view, 899, 640)
	tr.Frame()
	closeLabel := locales.In("en").ProfileClose()
	if !a.detailPageActive() || !tr.Focused(closeLabel) {
		t.Fatalf("899 profile page focus: page=%v focused=%v", a.detailPageActive(), tr.Focused(closeLabel))
	}
	a.windowWidth = 900
	tr.SetSize(900, 640)
	tr.Frame()
	if a.detailPageActive() || a.workOverlay() || !tr.Focused(closeLabel) {
		t.Fatalf("900 profile close focus: page=%v overlay=%v focused=%v", a.detailPageActive(), a.workOverlay(), tr.Focused(closeLabel))
	}
	a.windowWidth = 899
	tr.SetSize(899, 640)
	tr.Frame()
	if !a.detailPageActive() || !tr.Focused(closeLabel) {
		t.Fatalf("899 return focus: page=%v focused=%v", a.detailPageActive(), tr.Focused(closeLabel))
	}
}

func TestLayoutEventsAndContactSwitchDoNotToggleWork(t *testing.T) {
	a, ct, _ := piRunStartedTestApp(t)
	a.workspaceOpen = false
	a.contacts = append(a.contacts, &contact{ID: "mio", Name: "Mio", Role: "Assistant"})
	a.selectContact(1)
	if a.workspaceOpen {
		t.Fatal("contact switch opened work")
	}
	ct.SubmittedDraft = "first"
	event := pi.ProductExecutionEvent{ConversationID: ct.SessionID, ExecutionID: ct.ActiveTaskID}
	applyTestProductEvent(t, a, "execution.started", event)
	if a.workspaceOpen {
		t.Fatal("task start opened work")
	}
	event.State = "completed"
	applyTestProductEvent(t, a, "execution.settled", event)
	if a.workspaceOpen {
		t.Fatal("task finish opened work")
	}
	a.selectContact(0)
	a.openContactProfile(ct.ID)
	event.ExecutionID = "second-run"
	ct.ActiveTaskID, ct.SendState, ct.SubmittedDraft = event.ExecutionID, sendSubmitting, "second"
	applyTestProductEvent(t, a, "execution.started", event)
	if !a.detailIs(detailProfile) {
		t.Fatal("task start closed the profile")
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
	a.openContactProfile(a.selectedID())
	tr := ui.NewTester(a.view, 1180, 720)
	tr.Frame()
	leftDivider := a.railWidth()
	tr.Press(leftDivider, 300)
	tr.Move(leftDivider+40, 300)
	tr.Release(leftDivider+40, 300)
	tr.Frame()
	if a.railWidth() == defaultLeftPaneWidth {
		t.Fatal("drag did not change the in-memory left width")
	}
	if store.State.Layout != nil {
		t.Fatal("dragging wrote the store before an explicit boundary")
	}
	a.closeDetail()
	if store.State.Layout != nil {
		t.Fatal("closing the profile persisted the layout before quit")
	}
	a.beginShutdown()
	if store.State.Layout == nil || store.State.Layout.WorkspaceOpen || store.State.Layout.LeftWidth != a.railWidth() {
		t.Fatalf("layout after quit = %+v", store.State.Layout)
	}
	reloaded, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Layout == nil || reloaded.State.Layout.WorkspaceOpen || reloaded.State.Layout.LeftWidth != a.railWidth() {
		t.Fatalf("reloaded explicit layout = %+v", reloaded.State.Layout)
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
	tr.Press(a.railWidth(), 400)
	tr.Move(500, 400)
	tr.Release(500, 400)
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
	tr.Press(a.railWidth(), 300)
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

func TestBotProfileNavigationDoesNotWriteLayout(t *testing.T) {
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
	before := recoverySavedBytes(t, storePath)
	a.openContactProfile(a.selectedID())
	if !a.detailIs(detailProfile) || string(recoverySavedBytes(t, storePath)) != string(before) {
		t.Fatal("opening the Bot profile failed or wrote the store")
	}
	a.closeDetail()
	if a.workspaceOpen || string(recoverySavedBytes(t, storePath)) != string(before) {
		t.Fatal("closing the Bot profile failed or wrote the store")
	}
}

func TestLayoutRuntimeEntryLoadsSavedPreference(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "contacts.json")
	store, err := openContactStore(storePath, dir)
	if err != nil {
		t.Fatal(err)
	}
	store.State.Layout = &storedLayout{LeftWidth: 230, RightWidth: 360, WorkspaceOpen: true}
	if err := store.SaveState(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NAGI_GUI_STORE", storePath)
	t.Setenv("NAGI_GUI_WORKDIR", dir)
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.workspaceOpen || a.railWidth() != 230 || a.workWidth() != 360 {
		t.Fatalf("runtime entry layout = open=%v left=%v right=%v", a.workspaceOpen, a.railWidth(), a.workWidth())
	}
}

func TestHeaderGroupsStatusAndRoleWithLongRawText(t *testing.T) {
	const shortRole = "Assistant"
	const longEnglishRole = "Assistant for release engineering, cross-functional verification and runtime diagnostics"
	const longChineseRole = "负责发布工程、跨团队验证、运行时诊断与发布后回归分析的助理，同时维护跨团队交付记录和异常跟进"
	cases := []struct {
		name   string
		locale string
		role   string
		status string
		wrap   bool
		dark   bool
		setup  func(a *appModel, ct *contact)
	}{
		{
			name: "ready-960", locale: "en", role: shortRole, status: "Ready",
			setup: func(a *appModel, ct *contact) {
				a.pi = presentationPiHost(a)
				a.runtimeStatus = runtimeStateReady
			},
		},
		{
			name: "not-connected-960", locale: "en", role: shortRole, status: "Not connected", dark: true,
			setup: func(a *appModel, ct *contact) {
				cfg := connectionConfig{API: connectionAPIChatCompletions, Endpoint: "http://example.test/v1", APIKey: "fixture", Model: "fixture", ContextTokens: 1000, OutputTokens: 100, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
				a.runtimeStatus = runtimeStateExited
				a.runtimeStage = "Connection configuration"
				a.connectionSaved = &cfg
				a.connectionEffective = &cfg
				a.ensureConnectionForm()
			},
		},
		{
			name: "attention-long-960", locale: "en", role: longEnglishRole, status: "Needs attention", wrap: true, dark: true,
			setup: func(a *appModel, ct *contact) {
				a.pi = presentationPiHost(a)
				a.runtimeStatus = runtimeStateReady
				ct.Status = statusAttention
				ct.SendState = sendUnknown
			},
		},
		{
			name: "hans-long-960", locale: "zh-Hans", role: longChineseRole, status: locales.In("zh-Hans").StatusReady(), wrap: true,
			setup: func(a *appModel, ct *contact) {
				a.pi = presentationPiHost(a)
				a.runtimeStatus = runtimeStateReady
			},
		},
		{
			name: "en-XA-owned-status-960", locale: "en-XA", role: longEnglishRole, status: locales.In("en-XA").StatusReady(), wrap: true, dark: true,
			setup: func(a *appModel, ct *contact) {
				a.pi = presentationPiHost(a)
				a.runtimeStatus = runtimeStateReady
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			a.windowWidth = 960
			ct.Role = tc.role
			a.openContactProfile(ct.ID)
			tc.setup(a, ct)
			tr := ui.NewTester(a.view, 960, 640)
			tr.SetLocale(tc.locale)
			tr.SetDark(tc.dark)
			tr.Frame()
			msgs := locales.In(tc.locale)
			if !tr.HasText(tc.status) || !tr.HasText(tc.role) {
				t.Fatalf("header status/role missing: %v", tr.Texts())
			}
			status, statusOK := tr.Find(tc.status)
			role, roleOK := tr.Find(tc.role)
			if !statusOK || !roleOK || status.W <= 0 || status.H <= 0 || role.W <= 0 || role.H <= 0 {
				t.Fatalf("header geometry status=%+v/%v role=%+v/%v", status, statusOK, role, roleOK)
			}
			mainStart := float32(0)
			if a.avatarRailActive() {
				mainStart = avatarRailWidth + paneSeparatorWidth
			} else {
				mainStart = a.effectiveRailWidth() + paneSeparatorWidth
			}
			centerRight := mainStart + a.centerPaneWidth
			for _, item := range []struct {
				name string
				r    ui.Rect
			}{{"status", status}, {"role", role}} {
				if item.r.X < mainStart || item.r.X+item.r.W > centerRight || item.r.Y < 0 || item.r.Y+item.r.H > 640 {
					t.Fatalf("header %s outside center: %+v center=[%.1f,%.1f]", item.name, item.r, mainStart, centerRight)
				}
			}
			if status.X < role.X+role.W && role.X < status.X+status.W && status.Y < role.Y+role.H && role.Y < status.Y+status.H {
				t.Fatalf("header status/role overlap status=%+v role=%+v", status, role)
			}
			if status.Y < role.Y+role.H && role.Y < status.Y+status.H && role.X-(status.X+status.W) < 11.5 {
				t.Fatalf("inline status/role separation below 12 logical pixels: status=%+v role=%+v", status, role)
			}
			if tc.wrap {
				if role.H <= 24 {
					t.Fatalf("long role did not wrap: %+v", role)
				}
			} else if status.Y >= role.Y+role.H || role.Y >= status.Y+status.H {
				t.Fatalf("short status/role not on one row: status=%+v role=%+v", status, role)
			}
			composer, ok := tr.Find(msgs.ComposerLabel(ct.Name))
			if !ok || composer.W-4 < 440 {
				t.Fatalf("960 composer content changed: %+v ok=%v", composer, ok)
			}
			if a.selectedContact() != ct {
				t.Fatal("header grouping changed the selected contact scope")
			}
		})
	}
}
