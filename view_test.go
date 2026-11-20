package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestViewRendersBothThemes(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	for _, want := range []string{"Nagi", "Mio", "Ren", "Send", "Settings"} {
		if !tr.HasText(want) {
			t.Fatalf("light frame is missing %q", want)
		}
	}
	tr.SetDark(true)
	tr.Frame()
	if !tr.HasText("Nagi") {
		t.Fatal("dark frame should render the selected contact")
	}
	tr.SetDark(false)
	tr.Frame()
}

func TestTitleBarKeepsNavigationOutsideNativeControls(t *testing.T) {
	for _, width := range []int{720, 899, 900, 901, 960, 1040, 1179, 1180, 1181} {
		for _, right := range []float32{0, 138} {
			for _, scene := range []string{"chat", "collapsed", "profile", "settings"} {
				t.Run(fmt.Sprintf("%d/right-%g/%s", width, right, scene), func(t *testing.T) {
					a := testApp(t)
					a.windowWidth = width
					a.workspaceOpen = false
					ct := a.selectedContact()
					ct.Draft = "keep the titlebar draft"
					labels := []string{"Composer for Nagi", "Chat model", "Send"}
					closeLabel := ""
					switch scene {
					case "collapsed":
						a.railCollapsed = true
						labels = append(labels, "Contacts")
					case "profile":
						a.openDetail(detailProfile)
						closeLabel = "Close profile"
						labels = []string{closeLabel}
					case "settings":
						a.openSettingsTab(settingsTabAppearance, settingsSourceComposer)
						closeLabel = "← Back to workbench"
						labels = []string{closeLabel, "General"}
					}
					tr := ui.NewTester(a.view, width, 640)
					bar := ui.TitleBar{Height: 38, Left: 80, Right: right}
					tr.SetTitleBar(bar)
					for _, label := range labels {
						r, ok := tr.Find(label)
						overlapsControls := r.Y < bar.Height && (r.X < bar.Left || r.X+r.W > float32(width)-bar.Right)
						if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > float32(width) || r.Y < 0 || overlapsControls || r.Y+r.H > 640 {
							t.Fatalf("%q collides with native controls or leaves the window: visible=%v bounds=%+v", label, ok, r)
						}
						if label == "Send" && (r.W < 32 || r.H < 32) {
							t.Fatalf("Send target was clipped: %+v", r)
						}
					}
					if closeLabel != "" {
						if err := tr.Click(closeLabel); err != nil {
							t.Fatal(err)
						}
						tr.Frame()
						if a.settingsOpen || a.workspaceOpen || ct.Draft != "keep the titlebar draft" {
							t.Fatalf("return did not preserve its route and draft: settings=%v detail=%q draft=%q", a.settingsOpen, a.activeDetail(), ct.Draft)
						}
					}
				})
			}
		}
	}
}

func TestTitleBarChangesKeepComposerAndSettingsFocus(t *testing.T) {
	a := testApp(t)
	a.workspaceOpen = false
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	bar := ui.TitleBar{Height: 38, Left: 80}
	tr.SetTitleBar(bar)
	focusComposer(t, tr, "Composer for Nagi")
	tr.Type("draft before fullscreen")
	before, present := tr.Find("Composer for Nagi")
	tr.SetTitleBar(ui.TitleBar{})
	after, ok := tr.Find("Composer for Nagi")
	if !present || !ok || after.W <= 0 || after.H <= 0 {
		t.Fatalf("composer geometry missing after native metrics changed: before=%+v after=%+v", before, after)
	}
	if _, active := tr.TextCaret(); !active {
		t.Fatal("changing native titlebar metrics lost the composer caret")
	}
	tr.Type(" and after")
	tr.SetTitleBar(bar)
	if a.selectedContact().Draft != "draft before fullscreen and after" {
		t.Fatalf("titlebar transition changed the draft: %q", a.selectedContact().Draft)
	}
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen || !tr.Focused("← Back to workbench") {
		t.Fatal("Settings shortcut did not retain its visible return control")
	}
	tr.SetTitleBar(ui.TitleBar{})
	tr.SetTitleBar(bar)
	if !tr.Focused("← Back to workbench") {
		t.Fatal("changing native titlebar metrics stole Settings focus")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if _, active := tr.TextCaret(); !active {
		t.Fatal("Settings did not return the caret to the composer")
	}
}

// TestMinimumWindowRenders checks the 960x640 window minimum with the
// sidebar, chat header, and composer. The right workspace starts closed.
func TestMinimumWindowRenders(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	if !tr.HasText("Send") || !tr.HasText("Nagi") {
		t.Fatal("chat should render at the 960x640 minimum")
	}
	a.openSettings()
	tr.Frame()
	if !tr.HasText("Save changes") {
		t.Fatal("settings should render at the 960x640 minimum")
	}
	a.closeSettings()
	tr.Frame()
	a.addDefaultContact()
	tr.Frame()
	if !tr.HasText("New contact 1") {
		t.Fatal("the default contact should render at the 960x640 minimum")
	}
}

// focusComposer clicks the labeled composer and verifies it holds the IME
// caret.
func focusComposer(t *testing.T, tr *ui.Tester, label string) {
	t.Helper()
	if err := tr.Click(label); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if _, active := tr.TextCaret(); !active {
		t.Fatalf("%s should hold the IME caret", label)
	}
}

// TestComposerCompositionDoesNotSendUntilCommit exercises the IME path: a
// composition must not send, and Enter after the committed text does.
func TestComposerCompositionDoesNotSendUntilCommit(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	focusComposer(t, tr, "Composer for Nagi")
	before := len(a.selectedContact().Messages)
	tr.Compose("你好 Saki", 4)
	tr.Frame()
	if len(a.selectedContact().Messages) != before {
		t.Fatal("composing must not send a message")
	}
	tr.Compose("", 0)  // end the composition
	tr.Type("你好 Saki") // commit the text
	tr.Frame()
	if got := a.selectedContact().Draft; got != "你好 Saki" {
		t.Fatalf("committed draft = %q, want %q", got, "你好 Saki")
	}
	// Shift+Enter breaks the line instead of sending.
	tr.Key(ui.Shift, ui.KeyEnter)
	tr.Type("second line")
	tr.Frame()
	const payload = "你好 Saki\nsecond line"
	if got := a.selectedContact().Draft; got != payload {
		t.Fatalf("multiline draft = %q, want %q", got, payload)
	}
	// A bare Enter sends the exact multiline payload.
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	msgs := a.selectedContact().Messages
	if len(msgs) != before+2 {
		t.Fatalf("Enter should send once and append the demo reply: %d -> %d", before, len(msgs))
	}
	if got := msgs[len(msgs)-2].Text; got != payload {
		t.Fatalf("sent text = %q, want %q", got, payload)
	}
	if a.selectedContact().Draft != "" {
		t.Fatalf("accepted send should clear the draft, got %q", a.selectedContact().Draft)
	}
}

// TestDraftSwitchThroughTester drives the labeled view: type a Mio draft,
// switch to Saki and type a Saki draft, switch back to Mio, then send the
// restored Mio draft with Enter. That exercises the editor's e.Text(), not
// just the state fields.
func TestDraftSwitchThroughTester(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	if err := tr.Click("Contact Mio"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.selected != 1 {
		t.Fatalf("labeled click selected contact %d, want 1", a.selected)
	}
	focusComposer(t, tr, "Composer for Mio")
	tr.Type("draft for mio")
	tr.Frame()
	if a.contacts[1].Draft != "draft for mio" {
		t.Fatalf("Mio draft = %q", a.contacts[1].Draft)
	}
	if err := tr.Click("Contact Nagi"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.selected != 0 {
		t.Fatal("did not switch to Nagi")
	}
	focusComposer(t, tr, "Composer for Nagi")
	tr.Type("saki draft")
	tr.Frame()
	if a.contacts[0].Draft != "saki draft" {
		t.Fatalf("Saki draft = %q", a.contacts[0].Draft)
	}
	if a.contacts[1].Draft != "draft for mio" {
		t.Fatal("typing for Saki changed Mio's draft")
	}
	if err := tr.Click("Contact Mio"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.selected != 1 {
		t.Fatal("did not return to Mio")
	}
	focusComposer(t, tr, "Composer for Mio")
	before := len(a.contacts[1].Messages)
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	msgs := a.contacts[1].Messages
	if len(msgs) != before+2 {
		t.Fatalf("Enter should send Mio's restored draft: %d -> %d", before, len(msgs))
	}
	if got := msgs[len(msgs)-2].Text; got != "draft for mio" {
		t.Fatalf("sent %q, want the restored Mio draft", got)
	}
	if a.contacts[1].Draft != "" {
		t.Fatalf("accepted send should clear Mio's draft, got %q", a.contacts[1].Draft)
	}
	if a.contacts[0].Draft != "saki draft" {
		t.Fatalf("Saki draft changed: %q", a.contacts[0].Draft)
	}
}

// TestAddContactFocusesComposerAndSends clicks Add contact twice through the
// labeled view. Each time it types and presses Enter without any extra click;
// each message must belong to the contact just created, and every existing
// contact's messages and draft must stay untouched. The second creation is
// the one the reviewer asked for: it proves the one-shot focus works again
// and that the two new contacts do not share drafts or messages.
func TestAddContactFocusesComposerAndSends(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	beforeMsgs := make([]int, len(a.contacts))
	beforeDrafts := make([]string, len(a.contacts))
	for i := range a.contacts {
		beforeMsgs[i] = len(a.contacts[i].Messages)
		beforeDrafts[i] = a.contacts[i].Draft
	}
	assertExistingUnchanged := func(when string) {
		t.Helper()
		for i := range beforeMsgs {
			if len(a.contacts[i].Messages) != beforeMsgs[i] || a.contacts[i].Draft != beforeDrafts[i] {
				t.Fatalf("%s: existing contact %d changed: msgs %d->%d draft %q->%q", when, i, beforeMsgs[i], len(a.contacts[i].Messages), beforeDrafts[i], a.contacts[i].Draft)
			}
		}
	}

	// First creation.
	if err := tr.Click("Add contact"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	first := a.selectedContact()
	if first.Name != "New contact 1" || first.Role != "Assistant" {
		t.Fatalf("created contact = %q role=%q", first.Name, first.Role)
	}
	if !tr.Focused("Composer for " + first.Name) {
		t.Fatal("first creation should focus its composer without a click")
	}
	tr.Type("hello default")
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if len(first.Messages) != 2 {
		t.Fatalf("first contact messages = %d, want user + demo reply", len(first.Messages))
	}
	if got := first.Messages[0]; got.Kind != msgUser || got.Text != "hello default" {
		t.Fatalf("first message = %+v", got)
	}
	assertExistingUnchanged("after first send")

	// Second creation through the same UI path, again with no extra click.
	if err := tr.Click("Add contact"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	second := a.selectedContact()
	if second == first {
		t.Fatal("the second Add contact must create a new contact")
	}
	if second.Name != "New contact 2" || second.Role != "Assistant" {
		t.Fatalf("second contact = %q role=%q", second.Name, second.Role)
	}
	if !tr.Focused("Composer for " + second.Name) {
		t.Fatal("second creation should focus its composer without a click")
	}
	tr.Type("second hello")
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if len(second.Messages) != 2 {
		t.Fatalf("second contact messages = %d, want user + demo reply", len(second.Messages))
	}
	if got := second.Messages[0]; got.Kind != msgUser || got.Text != "second hello" {
		t.Fatalf("second contact message = %+v, want the exact typed input", got)
	}
	// The first new contact still holds exactly its own message and no
	// draft; the second send must not have leaked into it.
	if len(first.Messages) != 2 || first.Messages[0].Text != "hello default" {
		t.Fatalf("first new contact was changed by the second send: %+v", first.Messages[0])
	}
	if first.Draft != "" || second.Draft != "" {
		t.Fatalf("sent send should clear both drafts: first=%q second=%q", first.Draft, second.Draft)
	}
	assertExistingUnchanged("after second send")
}

// TestSettingsFieldsReceiveInputAndDoNotStealComposerFocus verifies through
// the UI, not private flags, that a Settings field really takes typed input
// while the composer keeps its draft, and that Tab plus further frames do not
// pull focus back to the composer.
func TestSettingsFieldsReceiveInputAndDoNotStealComposerFocus(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	if err := tr.Click("Add contact"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	ct := a.selectedContact()
	tr.Type("keep me")
	tr.Frame()
	if ct.Draft != "keep me" {
		t.Fatalf("composer draft = %q", ct.Draft)
	}
	if err := tr.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click(locales.In("en").SettingsContact()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Frame() // a second frame must not re-focus the composer
	if tr.Focused("Composer for " + ct.Name) {
		t.Fatal("opening Settings must not keep composer focus")
	}

	// Explicitly focus the Settings name field through the UI and type.
	nameBefore := a.contactProfile.Draft.Name
	if err := tr.Click("Contact name"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Contact name") {
		t.Fatal("clicking the Settings name field should focus it")
	}
	tr.Type(" edited")
	tr.Frame()
	if a.contactProfile.Draft.Name != nameBefore+" edited" {
		t.Fatalf("Settings name field did not receive the typed input: %q", a.contactProfile.Draft.Name)
	}
	if ct.Draft != "keep me" {
		t.Fatalf("typing in Settings changed the composer draft: %q", ct.Draft)
	}

	// Tab moves focus to the next control. More frames must not steal it
	// back to the composer, and typing must not land in the composer.
	tr.Key(0, ui.KeyTab)
	tr.Frame()
	tr.Frame()
	if tr.Focused("Composer for " + ct.Name) {
		t.Fatal("frames after Tab must not return focus to the composer")
	}
	tr.Type("qq")
	tr.Frame()
	if ct.Draft != "keep me" {
		t.Fatalf("typing after Tab leaked into the composer: %q", ct.Draft)
	}
	if a.contactProfile.Draft.Name != nameBefore+" edited" {
		t.Fatalf("typing after Tab changed the name field: %q", a.contactProfile.Draft.Name)
	}
}

// TestSettingsSavePreservesMessagesHistoryAndDraft modifies and saves the
// profile of a contact that already has non-empty messages, history and a
// draft, and checks those are preserved; an empty name is still rejected.
func TestSettingsSavePreservesMessagesHistoryAndDraft(t *testing.T) {
	a := testApp(t)
	ct := a.selectedContact() // Saki is seeded with messages and history
	if len(ct.Messages) == 0 || len(ct.History) == 0 {
		t.Fatal("fixture must start with non-empty messages and history")
	}
	ct.Draft = "unsent draft"
	beforeMsgs := len(ct.Messages)
	beforeHist := len(ct.History)
	beforeLast := ct.Messages[beforeMsgs-1].Text

	a.openSettings()
	draft := a.contactProfile.Draft
	draft.Name = "Saki Renamed"
	draft.Purpose = "Keeps earlier decisions in view."
	draft.Workspace = t.TempDir()
	if !a.saveContactProfileDraft(draft, false) {
		t.Fatal("settings save should succeed with a name")
	}
	if ct.Name != "Saki Renamed" {
		t.Fatalf("name = %q", ct.Name)
	}
	if len(ct.Messages) != beforeMsgs || ct.Messages[beforeMsgs-1].Text != beforeLast {
		t.Fatalf("saving settings changed messages: %d -> %d", beforeMsgs, len(ct.Messages))
	}
	if len(ct.History) != beforeHist {
		t.Fatalf("saving settings changed history: %d -> %d", beforeHist, len(ct.History))
	}
	if ct.Draft != "unsent draft" {
		t.Fatalf("saving settings cleared the draft: %q", ct.Draft)
	}

	a.openSettings()
	a.contactProfile.Draft.Name = "  "
	if a.saveContactProfileDraft(a.contactProfile.Draft, false) || a.contactProfile.Errors["Name"] == "" {
		t.Fatal("empty settings name must be rejected")
	}
	if ct.Name != "Saki Renamed" {
		t.Fatalf("a rejected save must not rename the contact: %q", ct.Name)
	}
}

// TestPreviewImages writes headless preview PNGs for design review. It is
// inert unless NAGI_PREVIEW_DIR points at a scratch directory.
func TestPreviewImages(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set NAGI_PREVIEW_DIR to write native preview PNGs")
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, scene := range []string{"chat", "new-contact", "settings"} {
			a := testApp(t)
			switch scene {
			case "new-contact":
				a.addDefaultContact()
			case "settings":
				a.openSettings()
			}
			tr := ui.NewTester(a.view, 1040, 720)
			tr.SetDark(dark)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("native-%s-%s.png", theme, scene)), tr.Image())
		}
	}
	writeV07AppearancePreviews(t, dir)

	// Minimum window proof for the two densest panels.
	for _, scene := range []string{"chat", "settings"} {
		a := testApp(t)
		if scene == "settings" {
			a.openSettings()
		}
		tr := ui.NewTester(a.view, 960, 640)
		tr.Frame()
		writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("native-light-min-%s.png", scene)), tr.Image())
	}
}

func writePreviewPNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}

func TestLiveTaskStatesRenderWithoutWaitingConflict(t *testing.T) {
	a, ct, _ := piRunStartedTestApp(t)
	a.windowWidth = 1040
	ct.SubmittedDraft = "hello"
	event := pi.ProductExecutionEvent{ConversationID: ct.SessionID, ExecutionID: ct.ActiveTaskID}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	applyTestProductEvent(t, a, "execution.started", event)
	tr.Frame()
	if ct.SendState != sendAccepted || !tr.HasText("hello") {
		t.Fatal("admitted Pi run did not show the submitted message")
	}
	event.Text = "Hel"
	applyTestProductEvent(t, a, "execution.textDelta", event)
	tr.Frame()
	if !tr.HasText("Hel") || ct.ActiveTaskID != event.ExecutionID {
		t.Fatal("Pi stream did not render while its run remained active")
	}
	event.State = "completed"
	applyTestProductEvent(t, a, "execution.settled", event)
	ct.Messages = []*message{{Key: "saved-user", Kind: msgUser, Text: "hello", History: true}, {Key: "saved-reply", Kind: msgContact, Text: "Hello", History: true}}
	tr.Frame()
	if ct.SendState != sendIdle || ct.ActiveTaskID != "" || !tr.HasText("Hello") || len(ct.Messages) != 2 || ct.Messages[1].Streaming {
		t.Fatalf("settlement left contradictory chat state: %+v", ct.Messages)
	}
}

func TestLiveSendUnknownAfterDisconnectSaysRestart(t *testing.T) {
	ct := &contact{ID: "bound", Name: "Nagi", SessionID: "s1", PiBinding: recoveryConversationBinding("bound", "/tmp", "s1"), Loaded: true, SendState: sendUnknown}
	a := &appModel{demo: false, runtimeStatus: runtimeStateExited, contacts: []*contact{ct}}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText(locales.In("en").SendDisconnectedState()) {
		t.Fatal("disconnected sendUnknown should tell the user to restart")
	}
	if tr.HasText(locales.In("en").SendUnknownState()) {
		t.Fatal("disconnected client must not offer an ineffective Reload instruction")
	}

	cases := []struct {
		name, cause string
		clearSend   bool
		status      string
	}{
		{name: "protocol", cause: "protocol"},
		{name: "exit", cause: "exit"},
		{name: "no-detail", cause: "exit", clearSend: true},
		{name: "distinct-details", cause: "exit", status: "Retained transport status diagnostic"},
	}
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, tc := range cases {
			t.Run(locale+"/"+tc.name, func(t *testing.T) {
				a, ct, issue := disconnectedRecoveryTestApp(t, tc.cause)
				if tc.clearSend {
					ct.clearSendError()
				}
				if tc.status != "" {
					ct.StatusDetail, ct.StatusDetailKind = tc.status, "status-connection-lost"
				}
				rawSend, rawStatus := ct.SendError, ct.StatusDetail
				taskID, sessionID, draft := ct.ActiveTaskID, ct.SessionID, ct.Draft
				record := recoveryRecordSnapshot(t, ct)
				tr := ui.NewTester(a.view, 1040, 1200)
				tr.SetLocale(locale)
				tr.SetFocused(true)
				tr.Frame()
				msgs := locales.In(locale)
				if !tr.HasText(msgs.SendDisconnectedState()) || tr.HasText(msgs.SendUnknownState()) || tr.HasText(msgs.ReloadAction()) || tr.HasText(msgs.SendNoticeConnectionLost()) {
					t.Fatalf("disconnected producer state offered unusable Reload guidance: %v", tr.Texts())
				}
				diagnostics := []string{}
				for _, diagnostic := range []string{rawSend, rawStatus} {
					if diagnostic == "" || slices.Contains(diagnostics, diagnostic) {
						continue
					}
					diagnostics = append(diagnostics, diagnostic)
					if tr.HasText(diagnostic) {
						t.Fatalf("diagnostic shown before explicit disclosure: %q", diagnostic)
					}
				}
				composer := msgs.ComposerLabel(ct.Name)
				texts := tr.Texts()
				composerIndex := slices.Index(texts, composer)
				if composerIndex < 0 || slices.Contains(texts[composerIndex+1:], msgs.DetailsAction()) != (len(diagnostics) != 0) {
					t.Fatalf("composer disclosure does not match the retained diagnostics: %v", texts)
				}
				if len(diagnostics) != 0 {
					if err := tr.Click(composer); err != nil {
						t.Fatal(err)
					}
					if !tr.Focused(composer) {
						t.Fatalf("conversation composer did not take focus: %v", tr.Texts())
					}
					for i := 0; i < 60 && !tr.Focused(msgs.DetailsAction()); i++ {
						tr.Key(0, ui.KeyTab)
					}
					if !tr.Focused(msgs.DetailsAction()) {
						t.Fatalf("unknown-delivery disclosure was unreachable from the composer: %v", tr.Texts())
					}
					tr.Key(0, ui.KeySpace)
					tr.Frame()
					for _, diagnostic := range diagnostics {
						if !tr.HasText(diagnostic) {
							t.Fatalf("composer disclosure did not reveal %q: %v", diagnostic, tr.Texts())
						}
					}
				}
				if ct.SendError != rawSend || ct.StatusDetail != rawStatus || ct.SendState != sendUnknown || ct.ActiveTaskID != taskID || ct.SessionID != sessionID || ct.Draft != draft || ct.Messages[0].Text != "original message" || ct.History[0].Text != "original history" || a.contactHistoryInFlight != 0 || issue.Executions[0].Status != issueExecutionUnknown {
					t.Fatal("disclosure changed unknown state/raw records/history or issued a request")
				}
				if recoveryRecordSnapshot(t, ct) != record || a.submissionInFlight != 0 {
					t.Fatal("disclosure changed the retained receipt or submitted work")
				}
			})
		}
	}
}

func disconnectedRecoveryTestApp(t *testing.T, cause string) (*appModel, *contact, *productIssue) {
	t.Helper()
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	a.runtimeStatus = runtimeStateExited
	a.runtimeErrKind = runtimeNoticeConnectionLost
	applyProductExecution(ct, &pi.ProductExecution{ExecutionID: "chat-task", State: "unknown"})
	ct.Draft = "retained unsent draft"
	ct.Messages = []*message{{Kind: msgUser, Text: "original message"}}
	ct.History = []historyEntry{{ID: "history-original", Text: "original history"}}
	issue := &productIssue{ID: "recovery-issue", ContactID: ct.ID, Goal: "Retain original goal", Status: issueStatusInProgress, RequirementsRevision: 1}
	attachIssueToolTestExecution(issue)
	issue.Executions[0].Status, issue.Executions[0].Prompt = issueExecutionUnknown, "original run prompt"
	issue.Work = issueWorkSession{Loaded: true, SendState: sendUnknown}
	ct.Issues = []*productIssue{issue}
	if cause == "protocol" {
		a.runtimeErr = "Pi worker protocol error"
	}
	issue.Work.setSendError("Pi disconnected; activity outcome is unknown.", "send-uncertain", "")
	return a, ct, issue
}

func TestToolFailureIsVisibleBeforeDetails(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
		raw    string
	}{
		{"exit", `{"output":"normal text","exit_code":1,"timed_out":false}`, "Failed (exit 1)", "exit_code"},
		{"timeout", `{"output":"still working","timed_out":true}`, "Timed out", "timed_out"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ct := &contact{ID: "nagi", Name: "Nagi", Role: "Assistant", Loaded: true}
			ct.Messages = []*message{{ID: 1, Kind: msgActivity, ToolName: "bash", ToolArgs: `{"command":"false"}`, ToolOutput: tc.output}}
			a := &appModel{runtimeStatus: runtimeStateReady, windowWidth: 1040, contacts: []*contact{ct}}
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			if !tr.HasText(tc.want) || !tr.HasText("normal text") && !tr.HasText("still working") {
				t.Fatalf("failure summary not visible: %v", tr.Texts())
			}
			if err := tr.Click("Details"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tr.HasText(tc.raw) {
				t.Fatalf("raw tool result not visible after Details: %v", tr.Texts())
			}
		})
	}
}

func TestContactRailControlsStayVisibleAtNarrowWidths(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Name = "Cedar"
	a.workspaceOpen = false
	a.detail = ""
	for _, width := range []int{1040, 960} {
		a.windowWidth = width
		tr := ui.NewTester(a.view, width, 720)
		tr.Frame()
		limit := a.railWidth()
		if limit != defaultLeftPaneWidth {
			t.Fatalf("%d rail width = %v", width, limit)
		}
		for _, removed := range []string{"Nagi", "Contact scope", "Active", "Archived", "All"} {
			if bounds, ok := tr.Find(removed); ok && bounds.W > 0 && bounds.H > 0 && bounds.X+bounds.W <= limit {
				t.Fatalf("redundant sidebar control %q is still shown", removed)
			}
		}
		for _, label := range []string{"Add contact", "Search contacts", "Settings"} {
			r, ok := tr.Find(label)
			if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > limit || r.Y < 0 || r.Y+r.H > 720 {
				t.Fatalf("%dx720 contact rail %q out of rail: ok=%v rect=%+v", width, label, ok, r)
			}
		}
	}
}

func TestComposerInlineContextReturnFocus(t *testing.T) {
	a := testApp(t)
	ct := a.selectedContact()
	ct.Context.items = []taskContextItem{
		{ID: "p1", Name: "资料一", Text: "第一条"},
		{ID: "p2", Name: "资料二", Text: "第二条"},
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()

	if err := tr.Click("Task context: 2"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "task-context" || !a.composerInlineAux() || !tr.HasText("Chat: Nagi") || !tr.HasText("资料一") {
		t.Fatalf("inline context = aux=%q panel=%q texts=%v", a.aux, a.composerPanel, tr.Texts())
	}
	if err := tr.Click("Close task context"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "" || a.composerPanel != "" || !tr.Focused("Task context: 2") {
		t.Fatalf("context close = aux=%q panel=%q focused=%v", a.aux, a.composerPanel, tr.Focused("Task context: 2"))
	}

	if err := tr.Click("Task context: 2"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	a.selectContact(1)
	tr.Frame()
	if a.aux != "" || a.composerPanel != "" || a.composerInlineAux() {
		t.Fatalf("contact switch kept stale inline context: aux=%q panel=%q texts=%v", a.aux, a.composerPanel, tr.Texts())
	}
	a.selectContact(0)
	tr.Frame()

	// The visible model label is the selected contact's value, not a stale
	// value left by another contact.
	a.contacts[1].SessionID = "session-mio"
	a.contacts[1].SessionModel = "model-mio"
	a.selectContact(1)
	tr.Frame()
	if !tr.HasText("model-mio") || tr.HasText("model-default") {
		t.Fatalf("selected contact model label crossed contacts: %v", tr.Texts())
	}
}

func TestResponsiveComposerAt960WithLoadedSession(t *testing.T) {
	a, ct := appReadingTestApp(t)
	a.windowWidth = 960
	a.openDetail(detailProfile)
	a.leftPaneWidth = 280
	a.rightPaneWidth = 440
	a.runtimeStatus = runtimeStateReady
	a.runtimeCapabilitiesLoaded = true
	ct.Loaded = true
	ct.SessionID = "responsive-loaded-session"
	ct.SessionModel = "provider/very-long-responsive-model-name-with-stable-suffix-omega"
	ct.Draft = "responsive loaded draft"
	ct.Context.items = []taskContextItem{{ID: "responsive-context", Name: "资料", Text: "loaded context"}}
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	if a.workOverlay() || !a.avatarRailActive() {
		t.Fatal("960 should use the avatar three-pane layout")
	}
	composer, ok := tr.Find("Composer for Nagi")
	if !ok || composer.W <= 0 || composer.W-4 < 440 {
		t.Fatalf("960 loaded composer=%+v ok=%v content=%g", composer, ok, composer.W-4)
	}
	within := func(label string) ui.Rect {
		t.Helper()
		r, ok := tr.Find(label)
		if !ok || r.W <= 0 || r.H <= 0 || r.X < composer.X || r.X+r.W > composer.X+composer.W {
			t.Fatalf("960 composer control %q rect=%+v ok=%v composer=%+v", label, r, ok, composer)
		}
		return r
	}
	model := within("Chat model")
	within("Chat")
	within(contextLabelForTest(1))
	send := within("Send")
	if send.X < model.X+model.W {
		t.Fatalf("960 Send overlaps model: send=%+v model=%+v", send, model)
	}
	head, tail := a.conversationModelDisplayParts(locales.In("en"), ct)
	visibleModel := head
	if tail != "" {
		visibleModel = tail
	}
	if visibleModel == "" || !tr.HasText(visibleModel) {
		t.Fatalf("960 visible model suffix %q missing: %v", visibleModel, tr.Texts())
	}
	showPreviewTooltip(t, tr, "Chat model")
	if !tr.HasText(ct.SessionModel) {
		t.Fatalf("960 model tooltip missing full ID: %v", tr.Texts())
	}

	a.demo = false
	ct.ActiveTaskID = "responsive-loaded-task"
	ct.Status = statusWorking
	tr.Frame()
	composer, ok = tr.Find("Composer for Nagi")
	if !ok || composer.W-4 < 440 {
		t.Fatalf("960 Stop composer=%+v ok=%v content=%g", composer, ok, composer.W-4)
	}
	stop := within("Stop chat")
	model = within("Chat model")
	if stop.X < model.X+model.W || tr.HasText("Send") {
		t.Fatalf("960 Stop geometry = stop=%+v model=%+v texts=%v", stop, model, tr.Texts())
	}
}

func TestComposerKeepsModelScopeAndPrimaryActionAtNarrowCenter(t *testing.T) {
	a := testApp(t)
	a.windowWidth = 1040
	a.leftPaneWidth = defaultLeftPaneWidth
	a.rightPaneWidth = 358 // leaves the real minimum 480px center beside the work pane
	a.openDetail(detailProfile)
	ct := a.selectedContact()
	ct.SessionID = "long-model-session"
	ct.SessionModel = "provider/very-long-model-name-that-must-not-push-the-action-out-of-the-composer"
	ct.Draft = "保留这段草稿"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if a.workOverlay() {
		t.Fatal("test setup must keep the three-pane center")
	}
	composer, ok := tr.Find("Composer for Nagi")
	if !ok || composer.W <= 0 {
		t.Fatalf("composer bounds missing: %+v ok=%v", composer, ok)
	}
	left, right := composer.X, composer.X+composer.W
	withinComposer := func(label string) ui.Rect {
		t.Helper()
		r, ok := tr.Find(label)
		if !ok || r.W <= 0 || r.H <= 0 || r.X < left || r.X+r.W > right {
			t.Fatalf("%s is outside the composer bounds: rect=%+v ok=%v composer=%+v", label, r, ok, composer)
		}
		return r
	}
	model := withinComposer("Chat model")
	scope := withinComposer("Chat")
	if scope.X < model.X || scope.X+scope.W > model.X+model.W {
		t.Fatalf("model scope is outside its chip: scope=%+v model=%+v", scope, model)
	}
	head, tail := a.conversationModelDisplayParts(locales.In("en"), ct)
	visibleModel := head
	if tail != "" {
		visibleModel = tail
	}
	if visibleModel == "" || !tr.HasText(visibleModel) {
		t.Fatalf("model text %q missing: %v", visibleModel, tr.Texts())
	}
	send := withinComposer("Send")
	if send.X < model.X+model.W {
		t.Fatalf("send action overlaps the model chip: send=%+v model=%+v", send, model)
	}

	// A running conversation replaces Send with the same fixed Stop target.
	a.demo = false
	a.runtimeStatus = runtimeStateReady
	a.runtimeCapabilitiesLoaded = true
	ct.ActiveTaskID = "conversation-task"
	ct.Status = statusWorking
	tr.Frame()
	composer, ok = tr.Find("Composer for Nagi")
	if !ok || composer.W <= 0 {
		t.Fatalf("composer bounds missing after stop switch: %+v ok=%v", composer, ok)
	}
	left, right = composer.X, composer.X+composer.W
	model = withinComposer("Chat model")
	stop := withinComposer("Stop chat")
	if stop.X < model.X+model.W {
		t.Fatalf("stop action overlaps the model chip: stop=%+v model=%+v", stop, model)
	}
	if tr.HasText("Send") {
		t.Fatal("running conversation kept the Send action instead of Stop chat")
	}

}

// TestPreviewComposerTypography always exercises the composer geometry matrix;
// it writes the review PNGs only when NAGI_PREVIEW_DIR is set.
func TestPreviewComposerTypography(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	type scene struct {
		name, draft string
		context     int
		active      bool
		width       int
		height      int
	}
	scenes := []scene{
		{name: "empty", width: 1040, height: 720},
		{name: "single", draft: "帮我整理这周的发布说明。", context: 2, width: 1040, height: 720},
		{name: "multiline", draft: "第一行：保留项目代号。\n第二行：确认负责人。\n第三行：列出下一步。", context: 2, width: 720, height: 640},
		{name: "long", draft: "把这周的资料整理成一份可以执行的计划：先核对项目代号和负责人，再把风险、依赖和下一步写清楚，最后给出可以直接继续处理的最小行动。", context: 3, width: 720, height: 640},
		{name: "stop", draft: "当前对话正在运行；这个位置应显示停止动作。", context: 1, active: true, width: 720, height: 640},
	}
	for _, sc := range scenes {
		for _, dark := range []bool{false, true} {
			a := testApp(t)
			a.windowWidth = sc.width
			a.workspaceOpen = false
			a.runtimeStatus = runtimeStateReady
			a.runtimeCapabilitiesLoaded = true
			a.runtimeMaxActiveTasks = 2
			ct := a.selectedContact()
			ct.SessionID = "composer-preview-session"
			ct.SessionModel = "deepseek/deepseek-chat"
			ct.Draft = sc.draft
			ct.Context.items = nil
			for i := 0; i < sc.context; i++ {
				ct.Context.items = append(ct.Context.items, taskContextItem{Name: "资料 " + fmt.Sprint(i+1), Text: "预览资料"})
			}
			if sc.active {
				a.demo = false
				ct.ActiveTaskID = "composer-preview-task"
				ct.Status = statusWorking
			}
			tr := ui.NewTester(a.view, sc.width, sc.height)
			tr.SetDark(dark)
			tr.Frame()
			visible := func(label string) ui.Rect {
				t.Helper()
				r, ok := tr.Find(label)
				if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(sc.width) || r.Y+r.H > float32(sc.height) {
					t.Fatalf("%s: %s bounds=%+v ok=%v", sc.name, label, r, ok)
				}
				return r
			}
			visible("Composer for Nagi")
			visible(contextLabelForTest(sc.context))
			visible("Chat model")
			if sc.active {
				visible("Stop chat")
			} else {
				visible("Send")
			}
			head, tail := a.conversationModelDisplayParts(locales.In("en"), ct)
			if !tr.HasText(head) || (tail != "" && !tr.HasText(tail)) {
				t.Fatalf("%s: actual model label missing: %v", sc.name, tr.Texts())
			}
			if sc.active && sc.width == 720 {
				composer := visible("Composer for Nagi")
				context := visible(contextLabelForTest(sc.context))
				model := visible("Chat model")
				stop := visible("Stop chat")
				if !tr.HasText("Stop chat") {
					t.Fatalf("%s: Stop text was hidden: %v", sc.name, tr.Texts())
				}
				if context.X+context.W > model.X || model.X+model.W > stop.X ||
					stop.X+stop.W > composer.X+composer.W ||
					context.X < composer.X || model.X < composer.X || stop.X < composer.X {
					t.Fatalf("%s: composer controls overlap/overflow composer=%+v context=%+v model=%+v stop=%+v",
						sc.name, composer, context, model, stop)
				}
				scope, ok := tr.Find("Chat")
				if !ok || scope.X < model.X || scope.X+scope.W > model.X+model.W {
					t.Fatalf("%s: model scope outside model button: scope=%+v ok=%v model=%+v", sc.name, scope, ok, model)
				}
				head, tail := a.conversationModelDisplayParts(locales.In("en"), ct)
				actualLabel := head
				if tail != "" {
					actualLabel = tail
				}
				actual, ok := tr.Find(actualLabel)
				if !ok || actual.X < model.X || actual.X+actual.W > model.X+model.W {
					t.Fatalf("%s: model text outside model button: actual=%+v ok=%v model=%+v", sc.name, actual, ok, model)
				}
			}
			if dir != "" {
				theme := "light"
				if dark {
					theme = "dark"
				}
				writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("headless-composer-%s-%dx%d-%s.png", sc.name, sc.width, sc.height, theme)), tr.Image())
			}
		}
	}
	longPrefix := "provider/" + strings.Repeat("shared-prefix-", 4)
	for _, width := range []int{720, 960} {
		for _, dark := range []bool{false, true} {
			for _, suffix := range []string{"alpha", "omega"} {
				a := testApp(t)
				a.windowWidth = width
				a.workspaceOpen = false
				a.runtimeStatus = runtimeStateReady
				a.runtimeCapabilitiesLoaded = true
				a.runtimeMaxActiveTasks = 2
				ct := a.selectedContact()
				ct.SessionID = "composer-preview-session"
				ct.SessionModel = longPrefix + suffix
				tr := ui.NewTester(a.view, width, 640)
				tr.SetDark(dark)
				tr.Frame()
				if !tr.HasText(suffix) {
					t.Fatalf("%d %s: distinguishing model tail missing: %v", width, suffix, tr.Texts())
				}
				theme := "light"
				if dark {
					theme = "dark"
				}
				writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("headless-composer-long-model-%s-%dx640-%s.png", suffix, width, theme)), tr.Image())
			}
		}
	}
}

func contextLabelForTest(count int) string {
	if count > 0 {
		return fmt.Sprintf("Task context: %d", count)
	}
	return "Task context"
}

func TestComposerZeroContextKeepsIconWithoutZeroText(t *testing.T) {
	a, ct := appReadingTestApp(t)
	if ct.SessionID == "" || ct.Context.count() != 0 {
		t.Fatal("fixture needs a session with zero draft context")
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	if tr.HasText("Task context: 0") {
		t.Fatalf("zero context rendered an independent zero label: %v", tr.Texts())
	}
	if zero, ok := tr.Find("0"); ok {
		t.Fatalf("zero context rendered an independent zero: %+v texts=%v", zero, tr.Texts())
	}
	button, ok := tr.Find("Task context")
	if !ok || button.W < 32 || button.H < 32 {
		t.Fatalf("zero-context control missing or under 32px: %+v ok=%v", button, ok)
	}
	for i := 0; i < 24 && !tr.Focused("Task context"); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused("Task context") {
		t.Fatalf("zero-context control is not keyboard reachable: %v", tr.Texts())
	}
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if a.aux != "task-context" || !a.composerInlineAux() {
		t.Fatalf("keyboard did not open task context: aux=%q panel=%q", a.aux, a.composerPanel)
	}
	if err := tr.Click("Close task context"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Task context") {
		t.Fatalf("task context did not restore keyboard focus: %v", tr.Texts())
	}
	ct.Messages = append(ct.Messages, &message{
		Key: "live-session-context", Kind: msgUser, Text: "session prompt",
		Context: []taskContextItem{{Name: "Session doc", Text: "session-scoped context"}},
	})
	ct.History = append(ct.History, historyEntry{
		ID: "history-session-context", Date: "Today", From: "me", Text: "history prompt",
		Context: []taskContextItem{{Name: "History doc", Text: "history-scoped context"}},
	})
	tr.Frame()
	if zero, ok := tr.Find("0"); ok {
		t.Fatalf("session/history context rendered as draft zero: %+v texts=%v", zero, tr.Texts())
	}
	ct.Context.items = []taskContextItem{{ID: "c1", Name: "资料", Text: "已有会话不计入草稿"}}
	tr.Frame()
	if !tr.HasText("Task context: 1") {
		t.Fatalf("non-zero context count missing: %v", tr.Texts())
	}
}

func TestComposerModelTooltipKeepsFullActualID(t *testing.T) {
	a, ct := appReadingTestApp(t)
	prefix := "provider/" + strings.Repeat("shared-prefix-", 4)
	a.connectionEffective = &connectionConfig{Model: prefix + "alpha"}
	ct.SessionID = "s1"
	ct.SessionModel = prefix + "omega"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	showPreviewTooltip(t, tr, "Chat model")
	if !tr.HasText(ct.SessionModel) || tr.HasText(prefix+"alpha") {
		t.Fatalf("loaded session model tooltip = %v", tr.Texts())
	}
	ct.SessionModel = ""
	tr.Frame()
	showPreviewTooltip(t, tr, "Chat model")
	if !tr.HasText("Session model not loaded") || tr.HasText(prefix+"alpha") {
		t.Fatalf("unloaded session fell back to connection default: %v", tr.Texts())
	}
}

func TestComposerModelsKeepActualSuffixAt720And960(t *testing.T) {
	prefix := "provider/" + strings.Repeat("shared-prefix-", 4)
	models := []string{prefix + "alpha", prefix + "omega", "deepseek/deepseek-chat"}
	for _, width := range []int{720, 960} {
		for _, model := range models {
			a, ct := appReadingTestApp(t)
			a.windowWidth = width
			ct.SessionID = "s1"
			ct.SessionModel = model
			tr := ui.NewTester(a.view, width, 640)
			tr.Frame()
			modelRect, ok := tr.Find("Chat model")
			if !ok || modelRect.W <= 0 {
				t.Fatalf("%d %q: model control missing: %+v", width, model, modelRect)
			}
			runes := []rune(model)
			tail := string(runes[len(runes)-8:])
			tailRect, ok := tr.Find(tail)
			if !ok || tailRect.W <= 0 || tailRect.X < modelRect.X || tailRect.X+tailRect.W > modelRect.X+modelRect.W {
				t.Fatalf("%d %q: actual suffix %q missing/outside model control: tail=%+v ok=%v model=%+v texts=%v",
					width, model, tail, tailRect, ok, modelRect, tr.Texts())
			}
		}
	}
}

func recoveryRecordSnapshot(t *testing.T, ct *contact) string {
	t.Helper()
	issues := []any{}
	for _, issue := range ct.Issues {
		executions := []any{}
		for _, e := range issue.Executions {
			executions = append(executions, []any{e.ID, e.SessionID, e.TaskID, e.MessageID, e.Prompt, e.Status, e.Detail, e.Context})
		}
		issues = append(issues, []any{issue.ID, issue.ContactID, issue.Goal, issue.Status, issue.WorkSessionID, issue.Work.ActiveTaskID, issue.Work.LastTaskID, issue.Work.SendState, issue.Work.SendError, issue.Work.SendErrorKind, issue.Work.SendErrorDetail, executions})
	}
	data, err := json.Marshal([]any{ct.ID, ct.SessionID, ct.ActiveTaskID, ct.LastTaskID, ct.SendState, ct.SessionState, ct.SendError, ct.SendErrorKind, ct.SendErrorDetail, ct.StatusDetail, ct.StatusDetailKind, ct.Draft, ct.SubmittedDraft, ct.SubmittedRev, clonePendingSend(ct.pendingSend), ct.Messages, ct.History, issues})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func recoveryTestStore(t *testing.T, a *appModel) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "contacts.json")
	store, err := openContactStore(path, root)
	if err != nil {
		t.Fatal(err)
	}
	a.store, a.storePath, a.workDir = store, path, root
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
}

func recoveryComposerTester(a *appModel, width, height int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		a.workTheme = resolveWorkbenchTheme(c.Theme().Dark)
		c.SetTheme(a.workTheme.Theme)
		ui.Scroll(c).Fill().Children(func() { a.composer(c, c.Theme(), connectionViewHealthy, true) })
	}, width, height)
}

func TestRP1ConversationAvailabilityMatrix(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, state := range []string{runtimeStateFailed, runtimeStateExited, runtimeStateStarting, runtimeStateReady} {
			for _, diagnostic := range []string{"empty", "protocol", "delivery"} {
				t.Run(locale+"/"+state+"/"+diagnostic, func(t *testing.T) {
					cause := "exit"
					if diagnostic == "protocol" {
						cause = "protocol"
					}
					a, ct, _ := disconnectedRecoveryTestApp(t, cause)
					ct.pendingSend = &pendingSend{Draft: "original message", State: "unknown"}
					if diagnostic == "empty" {
						ct.clearSendError()
					}
					a.runtimeStatus = state
					before := recoveryRecordSnapshot(t, ct)
					tr := recoveryComposerTester(a, 720, 640)
					tr.SetLocale(locale)
					tr.SetFocused(true)
					tr.Frame()
					msgs := locales.In(locale)
					wantSummary := msgs.SendDisconnectedState()
					for _, summary := range []string{msgs.SendDisconnectedState(), msgs.SendUnknownState(), msgs.IssueExecutionUnknownDetail(), msgs.SendNoticeConnectionLost()} {
						if tr.HasText(summary) != (summary == wantSummary) {
							t.Fatalf("want only %q for %s: %v", wantSummary, state, tr.Texts())
						}
					}
					if _, ok := tr.Find(msgs.ReloadAction()); ok {
						t.Fatalf("Reload availability contradicts %s: %v", state, tr.Texts())
					}
					if recoveryRecordSnapshot(t, ct) != before || a.submissionInFlight != 0 || a.contactHistoryInFlight != 0 {
						t.Fatal("presentation changed original records or issued a request")
					}
					if dir := os.Getenv("NAGI_RP_PREVIEW_DIR"); dir != "" && diagnostic == "delivery" && state == runtimeStateExited {
						writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("conversation-%s-%s.png", locale, state)), tr.Image())
					}
				})
			}
		}
	}
}

func TestRP1DisclosurePreservesRecords(t *testing.T) {
	for _, cause := range []string{"protocol", "exit"} {
		t.Run(cause, func(t *testing.T) {
			a, ct, _ := disconnectedRecoveryTestApp(t, cause)
			config := testConnectionConfig(t)
			config.APIKey = "controlled-record-secret"
			a.connectionSaved = &config
			ct.SubmittedDraft = "original submitted text"
			ct.SubmittedRev = 8
			ct.SendError += "; API_KEY=" + config.APIKey + "; " + strings.Repeat("long diagnostic ", 80)
			before := recoveryRecordSnapshot(t, ct)
			tr := recoveryComposerTester(a, 1040, 900)
			tr.SetFocused(true)
			tr.Frame()
			if err := tr.Click(locales.In("en").DetailsAction()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if err := tr.Click(locales.In("en").CopyErrorDetailsLabel()); err != nil {
				t.Fatal(err)
			}
			copied := tr.Clipboard()
			if strings.Contains(copied, config.APIKey) || !strings.Contains(copied, "[redacted]") || len([]rune(copied)) > 501 {
				t.Fatalf("detail was not bounded/redacted: %q", copied)
			}
			if !tr.HasText(locales.In("en").RecoveryTechnicalDetailsHint()) {
				t.Fatal("diagnostic is not marked as technical")
			}
			tr.SetLocale("zh-Hans")
			tr.Frame()
			if !tr.HasText(locales.In("zh-Hans").RecoveryTechnicalDetailsHint()) || strings.Contains(strings.Join(tr.Texts(), " "), config.APIKey) {
				t.Fatal("locale change leaked diagnostic or lost technical label")
			}
			if err := tr.Click(locales.In("zh-Hans").HideDetailsAction()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if err := tr.Click(locales.In("zh-Hans").ConnectionSettingsAction()); err != nil {
				t.Fatal(err)
			}
			if !a.settingsOpen || a.settingsTab != settingsTabConnection {
				t.Fatal("recovery settings navigation failed")
			}
			a.closeSettings()
			a.runtimeStatus = runtimeStateReady
			tr.Frame()
			if recoveryRecordSnapshot(t, ct) != before || a.contactHistoryInFlight != 0 || a.submissionInFlight != 0 {
				t.Fatal("disclosure/language/navigation/readiness altered original records or issued RPC")
			}
		})
	}
}

func TestComposerDeclaredToolbarKeepsRealEdgeTargets(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, dark := range []bool{false, true} {
			for _, size := range [][2]int{{720, 640}, {1040, 720}} {
				t.Run(fmt.Sprintf("%s/%dx%d/dark%t", locale, size[0], size[1], dark), func(t *testing.T) {
					a, ct := taskContextApp(t)
					a.windowWidth, a.workspaceOpen = size[0], false
					ct.Draft = "  exact unsaved composer draft 中文  "
					item, err := ct.Context.AddSnippet("Context", "exact context body")
					if err != nil {
						t.Fatal(err)
					}
					tr := ui.NewTester(a.view, size[0], size[1])
					tr.SetLocale(locale)
					tr.SetDark(dark)
					tr.SetFocused(true)
					tr.Frame()
					msgs := locales.In(locale)
					contextLabel := msgs.TaskContextCount(1)
					bar, ok := tr.Find(msgs.ComposerTools())
					button, found := tr.Find(contextLabel)
					if !ok || !found || bar.H != 42 || button.W != 32 || button.H != 32 || button.Y-bar.Y != 5 {
						t.Fatalf("declared42/py10 bar did not center the full target: bar=%+v target=%+v", bar, button)
					}
					if button.X < bar.X || button.X+button.W > bar.X+bar.W || button.Y < 0 || button.Y+button.H > float32(size[1]) {
						t.Fatal("composer target is clipped or outside its row/window")
					}
					for edge := 0; edge < 2; edge++ {
						button, _ = tr.Find(contextLabel)
						y := button.Y + 1
						if edge == 1 {
							y = button.Y + button.H - 1
						}
						generation := a.contextFileGeneration
						tr.ClickAt(button.X+1, y)
						tr.Frame()
						if a.aux != "task-context" || !a.composerInlineAux() || a.contextFileGeneration != generation+1 {
							t.Fatal("full target edge did not open exactly its own context panel")
						}
						if err := tr.Click(msgs.CloseTaskContext()); err != nil {
							t.Fatal(err)
						}
						tr.Frame()
						if a.aux != "" || !tr.Focused(contextLabel) {
							t.Fatal("context Close lost the actual initiating control focus")
						}
					}
					items := ct.Context.snapshot()
					if ct.Draft != "  exact unsaved composer draft 中文  " || len(items) != 1 || items[0].ID != item.ID || items[0].Revision != item.Revision || a.pi != nil || ct.pendingSend != nil || len(ct.Messages) != 0 {
						t.Fatal("edge navigation changed draft/snapshot or admitted execution")
					}
					if directory := os.Getenv("NAGI_PREVIEW_DIR"); directory != "" {
						name := fmt.Sprintf("composer-declared-%s-%dx%d-dark%t", locale, size[0], size[1], dark)
						writePreviewPNG(t, filepath.Join(directory, name+".png"), tr.Image())
						data, _ := json.MarshalIndent(map[string]ui.Rect{"toolbar": bar, "context_target": button}, "", "  ")
						if err := os.WriteFile(filepath.Join(directory, name+".json"), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}
