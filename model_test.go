package main

import (
	"strings"
	"testing"

	"github.com/HeimaoLST/Nagi/locales"
)

func testApp(t *testing.T) *appModel {
	t.Helper()
	return &appModel{
		demo: true,
		contacts: []*contact{
			{
				ID: "nagi", Name: "Nagi", Role: "Research partner",
				Purpose:      "Long-running product and research questions.",
				Instructions: "Keep replies short and concrete.",
				WorkLabel:    "Looking through this thread",
				Status:       statusReady,
				Messages: []*message{
					{ID: 1, Kind: msgUser, Text: "Where did we land on the MyGo decision before the desktop work starts?", Time: "9:42"},
					{ID: 3, Kind: msgContact, Text: "We agreed to use MyGo for the desktop and Pi for execution. Each contact keeps its own profile and history.", Time: "9:43"},
					{ID: 4, Kind: msgUser, Text: "Let's keep the contact list and drafts visible.", Time: "9:44"},
					{ID: 5, Kind: msgContact, Text: "Profile edits affect later replies and leave saved messages intact.", Time: "9:44"},
				},
				History: []historyEntry{
					{ID: "nagi-h1", Date: "Sep 27", From: "me", Text: "Let's settle the first scope for the MyGo desktop."},
					{ID: "nagi-h2", Date: "Sep 27", From: "contact", Text: "Use Pi for execution and MyGo for the desktop. Each contact keeps its own profile and history."},
					{ID: "nagi-h3", Date: "Oct 3", From: "me", Text: "So no shared memory between contacts yet."},
					{ID: "nagi-h4", Date: "Oct 3", From: "contact", Text: "Right. Each contact keeps its own history. Revisit collaboration after the single-contact workflow is useful."},
					{ID: "nagi-h5", Date: "Oct 5", From: "contact", Text: "Profile edits affect future replies; saved history stays intact."},
				},
			},
			{
				ID: "mio", Name: "Mio", Role: "Writing partner",
				Purpose:      "Tightening launch notes while keeping the voice consistent.",
				Instructions: "Prefer shorter sentences and concrete claims. Flag open questions at the end.",
				WorkLabel:    "Tightening the draft",
				Status:       statusReady,
				Unread:       1,
				Messages: []*message{
					{ID: 1, Kind: msgUser, Text: "Can you tighten the opening of the launch note? It still feels slow.", Time: "Yesterday"},
					{ID: 2, Kind: msgContact, Text: "Done. I cut the first paragraph in half and kept the two-sentence rhythm you liked. One open question: does the title still need the word “notes”?", Time: "Yesterday"},
					{ID: 3, Kind: msgUser, Text: "Keep the word for now. I will read the rest tonight.", Time: "Yesterday"},
					{ID: 4, Kind: msgContact, Text: "Sounds good. The draft stays where it is until you say otherwise.", Time: "8:15"},
				},
				History: []historyEntry{
					{ID: "mio-h1", Date: "Sep 30", From: "contact", Text: "I kept the closing short so the ask stands out."},
					{ID: "mio-h2", Date: "Oct 10", From: "me", Text: "The intro should promise less and show more."},
					{ID: "mio-h3", Date: "Oct 10", From: "contact", Text: "Agreed — I will lead with the concrete example."},
				},
			},
			{
				ID: "ren", Name: "Ren", Role: "Coding partner",
				Purpose:      "Small parser and runtime changes, reported as test results rather than guesses.",
				Instructions: "Report failing cases with the diff context. Do not claim a fix until the tests pass.",
				Workspace:    "~/Workspace/Nagi",
				WorkLabel:    "Running the parser tests",
				Status:       statusWorking,
				Work:         "Running the parser tests",
				Messages: []*message{
					{ID: 1, Kind: msgUser, Text: "Run the parser tests again after the trailing-newline change.", Time: "10:02"},
					{ID: 2, Kind: msgContact, Text: "Starting now. I will come back with the failing cases and the diff context.", Time: "10:02"},
				},
				History: []historyEntry{
					{ID: "ren-h1", Date: "Oct 8", From: "me", Text: "The empty-input case still crashes."},
					{ID: "ren-h2", Date: "Oct 8", From: "contact", Text: "Fixed — the parser now returns an empty list instead of unwrapping."},
					{ID: "ren-h3", Date: "Oct 11", From: "contact", Text: "I added a regression test for the roundtrip case."},
				},
			},
		},
		appearanceMode: appearanceSystem,
		seq:            100,
	}
}

func TestDraftsAreScopedPerContact(t *testing.T) {
	a := testApp(t)
	a.selectContact(1)
	a.selectedContact().Draft = "draft for mio"
	a.selectContact(0)
	a.selectedContact().Draft = "草稿 for nagi"
	if a.contacts[1].Draft != "draft for mio" {
		t.Fatalf("Mio draft changed: %q", a.contacts[1].Draft)
	}
	if a.contacts[0].Draft != "草稿 for nagi" {
		t.Fatalf("Nagi draft changed: %q", a.contacts[0].Draft)
	}
}

func TestRejectedSendKeepsDraft(t *testing.T) {
	a := testApp(t)
	a.selectContact(2) // Ren is working in the seed
	ct := a.selectedContact()
	ct.Draft = "while working"
	before := len(ct.Messages)
	if a.sendCurrent() {
		t.Fatal("send should be rejected while the contact is working")
	}
	if ct.Draft != "while working" {
		t.Fatalf("draft must be retained, got %q", ct.Draft)
	}
	if len(ct.Messages) != before {
		t.Fatal("no message should be appended for a rejected send")
	}
}

func TestRetryRejectedWhileWorking(t *testing.T) {
	a := testApp(t)
	ren := a.contactByID("ren")
	a.selectContact(2)
	m := &message{ID: 999, Kind: msgContact, Retry: true}
	if a.retry(m) {
		t.Fatal("retry should be rejected while the contact is working")
	}
	if m.RetryDone {
		t.Fatal("a rejected retry must not be marked done")
	}
	a.failRun(ren)
	if !a.retry(m) {
		t.Fatal("retry should be accepted after the run failed")
	}
}

func TestAddDefaultContactIsReadyAndUnique(t *testing.T) {
	a := testApp(t)
	a.addDefaultContact()
	first := a.selectedContact()
	if first.Name != locales.In("en").NewContactName(1) || first.Role != "Assistant" || first.Status != statusReady {
		t.Fatalf("default contact = %q role=%q status=%v", first.Name, first.Role, first.Status)
	}
	if a.workspaceOpen || !a.focusComposer {
		t.Fatal("default contact should open a chat and request composer focus")
	}
	a.addDefaultContact()
	second := a.selectedContact()
	if second.Name != locales.In("en").NewContactName(2) {
		t.Fatalf("second default name = %q", second.Name)
	}
	if first.ID == second.ID || first == second {
		t.Fatal("created contacts must be distinct objects with unique IDs")
	}
	if len(a.contacts) != 5 {
		t.Fatalf("contacts = %d, want 5", len(a.contacts))
	}
}

func TestConsecutiveDefaultContactsAreIsolated(t *testing.T) {
	a := testApp(t)
	a.addDefaultContact()
	first := a.selectedContact()
	first.Draft = "first draft"
	a.addDefaultContact()
	second := a.selectedContact()
	second.Draft = "second draft"
	a.seq++
	first.Messages = append(first.Messages, &message{ID: a.seq, Kind: msgUser, Text: "first message", Time: "10:00"})
	if len(second.Messages) != 0 || len(second.History) != 0 {
		t.Fatal("new contacts must not share mutable state")
	}
	a.selectContact(len(a.contacts) - 2)
	if a.selectedContact().Draft != "first draft" {
		t.Fatal("first contact draft lost")
	}
	if len(a.selectedContact().Messages) != 1 {
		t.Fatal("first contact messages changed")
	}
	a.selectContact(len(a.contacts) - 1)
	if a.selectedContact().Draft != "second draft" {
		t.Fatal("second contact draft lost")
	}
	if len(a.selectedContact().Messages) != 0 {
		t.Fatal("second contact received the first contact's message")
	}
}

// TestReplyStaysInOriginatingContact covers the synchronous demo path (no
// window): the reply lands in the contact the send started from, and other
// contacts' messages are untouched. The real timed switch is exercised
// against the running app.
func TestReplyStaysInOriginatingContact(t *testing.T) {
	a := testApp(t)
	nagi := a.contactByID("nagi")
	mio := a.contactByID("mio")
	nagiCount := len(nagi.Messages)
	nagiLastID := nagi.Messages[nagiCount-1].ID
	a.selectContact(1)
	mio.Draft = "ping"
	if !a.sendCurrent() {
		t.Fatal("send should be accepted for a ready contact")
	}
	if mio.Draft != "" {
		t.Fatalf("accepted send should clear the draft, got %q", mio.Draft)
	}
	if mio.Messages[len(mio.Messages)-1].Kind != msgContact {
		t.Fatal("reply must land in the originating contact")
	}
	if len(nagi.Messages) != nagiCount || nagi.Messages[nagiCount-1].ID != nagiLastID {
		t.Fatal("another contact's messages must not change")
	}
	a.selectContact(0)
	if len(a.selectedContact().Messages) != nagiCount {
		t.Fatal("contacts must not share messages")
	}
}

func TestBackgroundWorkMarksUnread(t *testing.T) {
	a := testApp(t)
	ren := a.contactByID("ren")
	a.selectContact(0)
	before := ren.Unread
	a.startWork(ren, "Re-running the parser tests", 0, func() { ren.Status = statusReady })
	if ren.Unread != before+1 {
		t.Fatalf("background completion should mark unread: %d -> %d", before, ren.Unread)
	}
}

func TestSnippetTruncatesByRunes(t *testing.T) {
	c := &contact{Name: "X"}
	c.Messages = []*message{{Kind: msgUser, Text: strings.Repeat("你", 80)}}
	a := &appModel{contacts: []*contact{c}}
	got := a.snippet(c, locales.In("en"))
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("long CJK snippet should be truncated: %q", got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("snippet broke a rune: %q", got)
	}
}
