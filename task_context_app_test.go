package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func taskContextApp(t *testing.T) (*appModel, *contact) {
	t.Helper()
	store, err := openContactStore(filepath.Join(t.TempDir(), "contacts.json"), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	ct := &contact{ID: "saki-context", Name: "Saki", Role: "Assistant", SessionID: "s1", Loaded: true}
	a := &appModel{
		store: store, runtimeStatus: runtimeStateReady, workspaceOpen: true,
		windowWidth: 1040, scrollTo: -1, contacts: []*contact{ct}, selected: 0,
	}
	a.bindTaskContexts()
	return a, ct
}

func TestTaskContextStoreRoundTripAndOwnedLists(t *testing.T) {
	a, ct := taskContextApp(t)
	contactItem, err := ct.Context.AddSnippet("Contact notes", "contact text")
	if err != nil {
		t.Fatal(err)
	}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "Goal", Status: issueStatusNotStarted}
	issue.Context.onChange = a.persist
	issueItem, err := issue.Context.AddSnippet("Issue notes", "issue text")
	if err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "e1", Context: []taskContextItem{issueItem}}
	issue.Executions = []*issueExecution{execution}
	ct.Issues = []*productIssue{issue}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}

	store, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.state.Contacts) != 1 || len(store.state.Contacts[0].TaskContext) != 1 || store.state.Contacts[0].TaskContext[0].ID != contactItem.ID {
		t.Fatalf("stored contact context = %+v", store.state.Contacts)
	}
	storedIssue := store.state.Issues[0]
	if len(storedIssue.TaskContext) != 1 || storedIssue.TaskContext[0].Text != "issue text" {
		t.Fatalf("stored issue context = %+v", storedIssue)
	}
	if len(storedIssue.Executions) != 1 || len(storedIssue.Executions[0].Context) != 1 || storedIssue.Executions[0].Context[0].Text != "issue text" {
		t.Fatalf("stored execution context = %+v", storedIssue.Executions)
	}

	reloaded := &appModel{contacts: contactsFromStore(store)}
	reloaded.loadStoredTaskContexts(store)
	reloaded.bindTaskContexts()
	if len(reloaded.contacts[0].Context.snapshot()) != 1 || reloaded.contacts[0].Context.snapshot()[0].Text != "contact text" {
		t.Fatalf("reloaded contact context = %+v", reloaded.contacts[0].Context.snapshot())
	}
	reloadedIssue := reloaded.contacts[0].issueByID("i1")
	if reloadedIssue == nil || len(reloadedIssue.Context.snapshot()) != 1 || reloadedIssue.Executions[0].Context[0].Text != "issue text" {
		t.Fatalf("reloaded issue = %+v", reloadedIssue)
	}
}

func TestTaskContextAcceptedSendClearsOnlyExactSubmittedVersions(t *testing.T) {
	a, ct := taskContextApp(t)
	first, err := ct.Context.AddSnippet("First", "old first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ct.Context.AddSnippet("Second", "remove me")
	if err != nil {
		t.Fatal(err)
	}
	submitted := ct.Context.snapshot()
	if _, err := ct.Context.Edit(first.ID, first.Name, "newer first"); err != nil {
		t.Fatal(err)
	}
	ct.Draft = "prompt"
	ct.SubmittedRev = ct.draftRev
	ct.pendingSend = &pendingSend{Draft: "prompt", Context: submitted, State: "submitting"}
	a.acceptTask(ct, "prompt", runtimeSendResult{TaskID: "t1", MessageID: "m1"})

	left := ct.Context.snapshot()
	if len(left) != 1 || left[0].ID != first.ID || left[0].Text != "newer first" || left[0].Revision != 2 {
		t.Fatalf("accepted clear changed newer context: %+v", left)
	}
	if _, ok := ct.Context.itemByID(second.ID); ok {
		t.Fatal("exact submitted sibling was not removed")
	}
	if len(ct.Messages) != 1 || len(ct.Messages[0].Context) != 2 || ct.Messages[0].Context[0].Text != "old first" {
		t.Fatalf("live message context = %+v", ct.Messages)
	}
	if ct.Messages[0].Context[0].Revision != submitted[0].Revision || ct.Messages[0].Context[1].Text != "remove me" {
		t.Fatalf("message did not retain the exact submitted values: %+v", ct.Messages[0].Context)
	}
	if ct.pendingSend != nil {
		t.Fatalf("accepted send left pending intent: %+v", ct.pendingSend)
	}
}

func TestTaskContextAcceptedSaveFailureKeepsNextDraftVisible(t *testing.T) {
	a, ct := taskContextApp(t)
	item, err := ct.Context.AddSnippet("Keep", "keep after acceptance")
	if err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "prompt", Context: ct.Context.snapshot(), State: "submitting"}
	a.store = &contactStore{path: t.TempDir()}
	a.acceptTask(ct, "prompt", runtimeSendResult{TaskID: "t1", MessageID: "m1"})
	if got := ct.Context.snapshot(); len(got) != 1 || got[0].ID != item.ID {
		t.Fatalf("accepted+savefailure cleared user data: %+v", got)
	}
	if !strings.Contains(ct.SendError, "accepted") {
		t.Fatalf("accepted save failure was not explicit: %q", ct.SendError)
	}
	if ct.SendState != sendAccepted || ct.ActiveTaskID != "t1" {
		t.Fatalf("accepted state was not retained: state=%v task=%q", ct.SendState, ct.ActiveTaskID)
	}
}

func TestTaskContextRejectedSendKeepsMaterials(t *testing.T) {
	a, ct := taskContextApp(t)
	if _, err := ct.Context.AddSnippet("Keep", "rejected context"); err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "rejected", Context: ct.Context.snapshot(), State: "submitting"}
	a.failSubmit(ct.ID, &runtimeError{Code: "capacity", Message: "full"}, false)
	if len(ct.Context.snapshot()) != 1 || ct.pendingSend != nil {
		t.Fatalf("rejected send changed context: %+v", ct.Context.snapshot())
	}
	if ct.SendState != sendIdle {
		t.Fatalf("confirmed rejection state = %v", ct.SendState)
	}
}

func TestTaskContextUnknownFailurePersistsPendingIntent(t *testing.T) {
	a, ct := taskContextApp(t)
	if _, err := ct.Context.AddSnippet("Maybe sent", "possibly delivered"); err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "uncertain prompt", Context: ct.Context.snapshot(), State: "submitting"}
	ct.SubmittedDraft = "uncertain prompt"
	a.failSubmit(ct.ID, &runtimeError{Code: "protocol_error", Message: "disconnected"}, true)
	if ct.SendState != sendUnknown || ct.pendingSend == nil || ct.pendingSend.State != "unknown" {
		t.Fatalf("unknown failure = state %v pending %+v", ct.SendState, ct.pendingSend)
	}
	if len(ct.Context.snapshot()) != 1 {
		t.Fatalf("unknown failure lost context: %+v", ct.Context.snapshot())
	}
	store, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if store.state.Contacts[0].PendingSend == nil || store.state.Contacts[0].PendingSend.State != "unknown" {
		t.Fatalf("unknown intent was not persisted: %+v", store.state.Contacts[0].PendingSend)
	}
}

func TestTaskContextExplicitReloadRetiresUnknownWithoutRemovingMaterials(t *testing.T) {
	a, ct := taskContextApp(t)
	if _, err := ct.Context.AddSnippet("Keep", "after explicit reload"); err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "uncertain", Context: ct.Context.snapshot(), State: "unknown"}
	ct.SendState = sendUnknown
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	a.applyReload(ct.ID, ct.SessionID, 0, runtimeSession{SessionID: ct.SessionID, State: "idle", Status: "completed"}, nil, "completed", nil, true)
	if ct.SendState != sendIdle || ct.pendingSend != nil {
		t.Fatalf("explicit reload state = %v / %+v", ct.SendState, ct.pendingSend)
	}
	if len(ct.Context.snapshot()) != 1 || !strings.Contains(ct.StatusDetail, "History reloaded; no retry was sent") {
		t.Fatalf("explicit reload materials/copy = %+v / %q", ct.Context.snapshot(), ct.StatusDetail)
	}
	store, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if store.state.Contacts[0].PendingSend != nil {
		t.Fatalf("retired unknown intent reappeared in the store: %+v", store.state.Contacts[0].PendingSend)
	}
}

func TestTaskContextIssueAcceptedClearsPendingOnly(t *testing.T) {
	a, ct := taskContextApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "Goal", Status: issueStatusInProgress}
	issue.Context.onChange = a.persist
	item, err := issue.Context.AddSnippet("Issue notes", "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	issue.Context.openSnippetForm()
	issue.Context.formName = "next run draft"
	execution := &issueExecution{ID: "e1", Context: []taskContextItem{item}}
	issue.Executions = []*issueExecution{execution}
	ct.Issues = []*productIssue{issue}
	a.acceptIssueTask(ct, issue, execution, "prompt", runtimeSendResult{TaskID: "t1", MessageID: "m1"})
	if got := issue.Context.snapshot(); len(got) != 0 {
		t.Fatalf("accepted issue context was not cleared: %+v", got)
	}
	if len(execution.Context) != 1 || execution.Context[0].Text != "snapshot" {
		t.Fatalf("execution context was not immutable: %+v", execution.Context)
	}
	if !issue.Context.formOpen || issue.Context.formName != "next run draft" {
		t.Fatal("accepted clear destroyed an unsaved next-run editor")
	}
}

func TestTaskContextUserContentSeparatesOnlyActualEnvelope(t *testing.T) {
	envelope, err := json.Marshal(map[string]string{
		"type": "caller_context", "label": "Caller-provided context (user data)",
		"name": "notes · file /tmp/notes.txt", "text": "exact\nbody",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`[{"type":"text","text":"main text"},{"type":"text","text":` + string(mustJSON(t, string(envelope))) + `}]`)
	text, contexts := taskContextUserContent(raw)
	if text != "main text" || len(contexts) != 1 || contexts[0].Name != "notes · file /tmp/notes.txt" || contexts[0].Text != "exact\nbody" {
		t.Fatalf("parsed = %q / %+v", text, contexts)
	}
	ordinary := []byte(`[{"type":"text","text":"{\"type\":\"caller_context\",\"name\":\"ordinary\"}"}]`)
	text, contexts = taskContextUserContent(ordinary)
	if text != `{"type":"caller_context","name":"ordinary"}` || len(contexts) != 0 {
		t.Fatalf("ordinary JSON became context: %q / %+v", text, contexts)
	}
	bodyJSON, err := json.Marshal(map[string]string{
		"type": "caller_context", "label": "Caller-provided context (user data)",
		"name": "body-as-json", "text": "literal body",
	})
	if err != nil {
		t.Fatal(err)
	}
	realJSON, err := json.Marshal(map[string]string{
		"type": "caller_context", "label": "Caller-provided context (user data)",
		"name": "Real context", "text": "real body",
	})
	if err != nil {
		t.Fatal(err)
	}
	both := []byte(`[{"type":"text","text":` + string(mustJSON(t, string(bodyJSON))) + `},{"type":"text","text":` + string(mustJSON(t, string(realJSON))) + `}]`)
	text, contexts = taskContextUserContent(both)
	if text != string(bodyJSON) || len(contexts) != 1 || contexts[0].Name != "Real context" || contexts[0].Text != "real body" {
		t.Fatalf("envelope-like body was hidden or parsed: %q / %+v", text, contexts)
	}
	text, contexts = taskContextUserContent([]byte(`"main only"`))
	if text != "main only" || len(contexts) != 0 {
		t.Fatalf("plain content = %q / %+v", text, contexts)
	}
}

func mustJSON(t *testing.T, text string) []byte {
	t.Helper()
	data, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTaskContextAppModalAndReadOnlyHistoryCopy(t *testing.T) {
	a, ct := taskContextApp(t)
	if _, err := ct.Context.AddSnippet("Conversation notes", "conversation body"); err != nil {
		t.Fatal(err)
	}
	a.openConversationTaskContext()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	for _, want := range []string{"Task context", "Chat: Saki", "Conversation notes", "Close task context"} {
		if !tr.HasText(want) {
			t.Fatalf("conversation modal missing %q: %v", want, tr.Texts())
		}
	}

	ct.Messages = nil
	ct.History = []historyEntry{{
		ID: "hist:readonly", From: "me", Text: "history prompt",
		Context: []taskContextItem{{Name: "Doc · file /tmp/doc.txt", Text: "history exact"}},
	}}
	ct.Draft = "draft remains local"
	ownerID := a.selectedContact().ID
	a.aux = ""
	a.openHistory()
	tr.Frame()
	if !tr.HasText("history prompt") || !tr.HasText("Read full message") {
		t.Fatalf("history summary/list entry missing: %v", tr.Texts())
	}
	if err := tr.Click("Read full message"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("history exact") || !tr.HasText("Doc · file /tmp/doc.txt") || a.contactHistoryFull == nil {
		t.Fatalf("history full message/context missing: full=%+v texts=%v", a.contactHistoryFull, tr.Texts())
	}
	if err := tr.Click("Copy context text"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != "history exact" {
		t.Fatalf("history context copy = %q", got)
	}
	if err := tr.Click("← Back to history"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.contactHistoryFull != nil || !a.detailIs(detailHistory) {
		t.Fatalf("history Back lost the list: full=%+v detail=%q", a.contactHistoryFull, a.activeDetail())
	}
	if ct.Draft != "draft remains local" || a.selectedContact().ID != ownerID {
		t.Fatalf("history reading changed draft/owner: draft=%q owner=%q", ct.Draft, a.selectedContact().ID)
	}
	if err := tr.Click("Close history"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	ct.Messages = []*message{{
		Key: "live:readonly", Kind: msgUser, Text: "main prompt",
		Context: []taskContextItem{{Name: "Doc · file /tmp/doc.txt", Text: "read only text"}},
	}}
	a.aux = ""
	tr = ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("main prompt") || !tr.HasText("read only text") {
		t.Fatalf("live context missing: %v", tr.Texts())
	}
}

func TestTaskContextRefreshStaysInOwningContactAndIssue(t *testing.T) {
	a, ct := taskContextApp(t)
	second := &contact{ID: "second", Name: "Mio", Role: "Assistant"}
	a.contacts = append(a.contacts, second)
	a.bindTaskContexts()
	path := filepath.Join(t.TempDir(), "shared.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	contactItem, err := ct.Context.ImportFile(path, "Contact file")
	if err != nil {
		t.Fatal(err)
	}
	secondItem, err := second.Context.ImportFile(path, "Second file")
	if err != nil {
		t.Fatal(err)
	}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "Goal"}
	issue.Context.onChange = a.persist
	issueItem, err := issue.Context.ImportFile(path, "Issue file")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ct.Context.Refresh(contactItem.ID); err != nil {
		t.Fatal(err)
	}
	if got := ct.Context.snapshot()[0]; got.Text != "new" || got.Revision != 2 {
		t.Fatalf("contact refresh = %+v", got)
	}
	if got := second.Context.snapshot()[0]; got.Text != "old" || got.Revision != 1 || got.ID != secondItem.ID {
		t.Fatalf("refresh leaked into second contact: %+v", got)
	}
	if got := issue.Context.snapshot()[0]; got.Text != "old" || got.Revision != 1 || got.ID != issueItem.ID {
		t.Fatalf("refresh leaked into issue: %+v", got)
	}
}

func TestTaskContextHistoryProjectionUsesOnlyRealEnvelope(t *testing.T) {
	contextJSON, err := json.Marshal(map[string]string{
		"type": "caller_context", "label": "Caller-provided context (user data)",
		"name": "Doc · file /tmp/doc.txt", "text": "history body",
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal([]any{
		map[string]string{"type": "text", "text": "main prompt"},
		map[string]string{"type": "text", "text": string(contextJSON)},
	})
	if err != nil {
		t.Fatal(err)
	}
	records := []historyRecord{
		{Offset: 0, Record: journalRecord{Type: "task_started", TaskID: "t1", MessageID: "m1"}},
		{Offset: 1, Record: journalRecord{Type: "messages", Items: []inputItem{{
			Type: "message", Role: "user", Content: content,
		}}}},
	}
	projection := parseHistory("s1", records, nil)
	if len(projection.messages) != 1 || len(projection.messages[0].Context) != 1 {
		t.Fatalf("projection = %+v", projection)
	}
	message := projection.messages[0]
	if message.Text != "main prompt" || message.Context[0].Text != "history body" || message.Context[0].Name != "Doc · file /tmp/doc.txt" {
		t.Fatalf("message projection = %+v", message)
	}
	if len(projection.entries) != 1 || len(projection.entries[0].Context) != 1 || projection.entries[0].Text != "main prompt" {
		t.Fatalf("entry projection = %+v", projection.entries)
	}
}

func TestTaskContextUnsavedEditorSurvivesContactSwitch(t *testing.T) {
	a, ct := taskContextApp(t)
	second := &contact{ID: "second", Name: "Mio", Role: "Assistant"}
	second.Context.onChange = a.persist
	a.contacts = append(a.contacts, second)
	ct.Context.openSnippetForm()
	ct.Context.formName = "unsaved editor"
	a.selectContact(1)
	a.selectContact(0)
	if !ct.Context.formOpen || ct.Context.formName != "unsaved editor" {
		t.Fatalf("contact switch lost unsaved editor: %+v", ct.Context)
	}
	if second.Context.count() != 0 || second.Context.formOpen {
		t.Fatalf("editor leaked to another contact: %+v", second.Context)
	}
}

func TestTaskContextAppPreviewScreenshots(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release-notes.txt")
	if err := os.WriteFile(path, []byte("Exact saved file text.\nSecond line with Unicode: 你好 — café.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, size := range []struct {
			name          string
			width, height int
		}{{"narrow", 720, 640}, {"wide", 1040, 720}} {
			a, ct := taskContextApp(t)
			if _, err := ct.Context.AddSnippet("Review checklist", "Confirm the exact snippet survives copy and send."); err != nil {
				t.Fatal(err)
			}
			if _, err := ct.Context.ImportFile(path, "Release notes"); err != nil {
				t.Fatal(err)
			}
			a.windowWidth = size.width
			a.openConversationTaskContext()
			tr := ui.NewTester(a.view, size.width, size.height)
			tr.SetDark(dark)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("task-context-app-%s-%s.png", theme, size.name)), tr.Image())
		}
	}
}

func TestTaskContextIssueDetailsOpenOwnEditor(t *testing.T) {
	a, ct := taskContextApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "Ship the issue context", Status: issueStatusNotStarted}
	issue.Context.onChange = a.persist
	if _, err := issue.Context.AddSnippet("Issue notes", "issue-only body"); err != nil {
		t.Fatal(err)
	}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	a.workspaceOpen = true
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	if err := tr.Click("Task context: 1"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	for _, want := range []string{"Ship the issue context", "Issue notes", "Close task context"} {
		if !tr.HasText(want) {
			t.Fatalf("issue context modal missing %q: %v", want, tr.Texts())
		}
	}
	if ct.Context.count() != 0 {
		t.Fatalf("issue modal edited conversation context: %+v", ct.Context.snapshot())
	}
}

func TestTaskContextExecutionFallbackUsesOnlyItsOwnedSession(t *testing.T) {
	ct := &contact{ID: "c", SessionID: "conversation-session"}
	issue := &productIssue{ID: "i", WorkSessionID: "issue-session"}
	execution := &issueExecution{ID: "e", TaskID: "t1", SessionID: "other-session"}
	ct.Messages = []*message{{TaskID: "t1", Context: []taskContextItem{{Name: "conversation", Text: "wrong"}}}}
	issue.Work.Messages = []*message{{TaskID: "t1", Context: []taskContextItem{{Name: "issue", Text: "wrong"}}}}
	if got := executionTaskContextItems(ct, issue, execution); len(got) != 0 {
		t.Fatalf("arbitrary session leaked context: %+v", got)
	}
	execution.SessionID = "issue-session"
	if got := executionTaskContextItems(ct, issue, execution); len(got) != 1 || got[0].Name != "issue" {
		t.Fatalf("issue session fallback = %+v", got)
	}
	execution.SessionID = "conversation-session"
	if got := executionTaskContextItems(ct, issue, execution); len(got) != 1 || got[0].Name != "conversation" {
		t.Fatalf("conversation session fallback = %+v", got)
	}
}

func TestTaskContextAutomaticReloadKeepsUnknownIntent(t *testing.T) {
	a, ct := taskContextApp(t)
	if _, err := ct.Context.AddSnippet("Keep", "still uncertain"); err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "uncertain", Context: ct.Context.snapshot(), State: "unknown"}
	ct.SendState = sendIdle // a terminal event may have cleared the state before automatic reload
	a.applyReload(ct.ID, ct.SessionID, 0, runtimeSession{SessionID: ct.SessionID, State: "idle", Status: "completed"}, nil, "completed", nil, false)
	if ct.SendState != sendUnknown || ct.pendingSend == nil || ct.pendingSend.State != "unknown" || len(ct.Context.snapshot()) != 1 {
		t.Fatalf("automatic reload retired unknown intent: state=%v pending=%+v context=%+v", ct.SendState, ct.pendingSend, ct.Context.snapshot())
	}
}

func TestTaskContextRestoredSubmittingIntentSurvivesAutomaticReload(t *testing.T) {
	a, ct := taskContextApp(t)
	if _, err := ct.Context.AddSnippet("Restored in flight", "crash before reply"); err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "in flight", Context: ct.Context.snapshot(), State: "submitting"}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	reloaded := &appModel{store: store, contacts: contactsFromStore(store)}
	reloaded.loadStoredTaskContexts(store)
	reloaded.bindTaskContexts()
	restored := reloaded.contacts[0]
	if restored.pendingSend == nil || restored.pendingSend.State != "unknown" || restored.SendState != sendUnknown {
		t.Fatalf("restored submitting intent was not normalized: state=%v pending=%+v", restored.SendState, restored.pendingSend)
	}
	reloaded.applyReload(restored.ID, restored.SessionID, 0, runtimeSession{SessionID: restored.SessionID, State: "idle", Status: "completed"}, nil, "completed", nil, false)
	if restored.SendState != sendUnknown || restored.pendingSend == nil || restored.pendingSend.State != "unknown" || len(restored.Context.snapshot()) != 1 {
		t.Fatalf("automatic reload retired restored intent: state=%v pending=%+v context=%+v", restored.SendState, restored.pendingSend, restored.Context.snapshot())
	}
}

func TestTaskContextUnknownSendSurvivesStoreRestartWithoutAutoReplay(t *testing.T) {
	a, ct := taskContextApp(t)
	if _, err := ct.Context.AddSnippet("Maybe sent", "possibly delivered"); err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{
		Draft: "uncertain prompt", Context: ct.Context.snapshot(), State: "unknown",
	}
	ct.SendState = sendUnknown
	ct.SendError = "Delivery not confirmed."
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if store.state.Version != contactStoreVersion {
		t.Fatalf("store version = %d", store.state.Version)
	}
	reloaded := &appModel{store: store, contacts: contactsFromStore(store)}
	reloaded.loadStoredTaskContexts(store)
	reloaded.bindTaskContexts()
	got := reloaded.contacts[0]
	if got.SendState != sendUnknown || got.pendingSend == nil || got.pendingSend.State != "unknown" {
		t.Fatalf("restart unknown state = state %v pending %+v", got.SendState, got.pendingSend)
	}
	if len(got.Context.snapshot()) != 1 || !strings.Contains(got.SendError, "may have been sent") {
		t.Fatalf("restart context/error = %+v / %q", got.Context.snapshot(), got.SendError)
	}
	if reloaded.rt != nil {
		t.Fatal("restart unknown state created a runtime or replayed a send")
	}
}

func TestTaskContextStoreV6ReadsOldV4WithoutContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	legacy := storedState{
		Version: 4, Selected: "c1",
		Contacts: []storedContact{{ID: "c1", Name: "Saki", Draft: "old draft"}},
	}
	if err := writeStoredState(path, legacy); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.state.Contacts) != 1 || store.state.Contacts[0].Draft != "old draft" || len(store.state.Contacts[0].TaskContext) != 0 {
		t.Fatalf("v4 read = %+v", store.state)
	}
	if err := store.save([]*contact{{ID: "c1", Name: "Saki"}}, "c1"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Version != contactStoreVersion {
		t.Fatalf("old v4 data was not saved as the current version: %d", reloaded.state.Version)
	}
}

func TestTaskContextAppIssueAndReadOnlyPreviewScreenshots(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(scene string) {
		for _, dark := range []bool{false, true} {
			theme := "light"
			if dark {
				theme = "dark"
			}
			for _, size := range []struct {
				name          string
				width, height int
			}{{"narrow", 720, 640}, {"wide", 1040, 720}} {
				a, ct := taskContextApp(t)
				issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "Show task context", Status: issueStatusInProgress}
				issue.Context.onChange = a.persist
				item, err := issue.Context.AddSnippet("Issue notes", "exact issue text\n第二行")
				if err != nil {
					t.Fatal(err)
				}
				issue.Executions = []*issueExecution{{
					ID: "e1", SessionID: "issue-session", TaskID: "t1", MessageID: "m1",
					Prompt: "Sent issue snapshot", Status: issueExecutionCompleted,
					Context: []taskContextItem{item},
				}}
				issue.WorkSessionID = "issue-session"
				ct.Issues = []*productIssue{issue}
				ct.SelectedIssueID = issue.ID
				a.workspaceOpen = true
				a.windowWidth = size.width
				if scene == "issue" {
					a.openIssueTaskContext(issue.ID)
				} else {
					issue.Executions[0].ContextExpanded = true
				}
				tr := ui.NewTester(a.view, size.width, size.height)
				tr.SetDark(dark)
				tr.Frame()
				writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("task-context-app-%s-%s-%s.png", scene, theme, size.name)), tr.Image())
			}
		}
	}
	write("issue")
	write("readonly")
}

func TestTaskContextFileChooserReachableForBothOwners(t *testing.T) {
	for _, issueOwner := range []bool{false, true} {
		t.Run(fmt.Sprintf("issue-%t", issueOwner), func(t *testing.T) {
			a, ct := taskContextApp(t)
			callbacks := make(chan func(), 1)
			a.setUIHook(func(fn func()) { callbacks <- fn })
			a.chooseContextFile = func(mygo.OpenDialogOptions) ([]string, error) { return nil, nil }
			target := &ct.Context
			if issueOwner {
				issue := &productIssue{ID: "chooser-issue", ContactID: ct.ID, Goal: "Choose issue file", Status: issueStatusNotStarted}
				issue.Context.onChange = a.persist
				ct.Issues = []*productIssue{issue}
				target = &issue.Context
				a.openIssueTaskContext(issue.ID)
			} else {
				a.openConversationTaskContext()
			}
			stored := recoverySavedBytes(t, a.store.path)
			tr := ui.NewTester(a.view, 1040, 720)
			tr.SetLocale("en")
			tr.Frame()
			if err := tr.Click("Add file"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if err := tr.Click("Context file name"); err != nil {
				t.Fatal(err)
			}
			tr.Type("Latest optional label")
			if err := tr.Click("Context file path"); err != nil {
				t.Fatal(err)
			}
			tr.Type("keep-the-manual-path")
			defer func() {
				if target.formName != "Latest optional label" || target.formPath != "keep-the-manual-path" || target.count() != 0 || a.selectedContact() != ct || a.rt != nil || string(recoverySavedBytes(t, a.store.path)) != string(stored) {
					t.Error("file discovery mutated owner, input, snapshots/store or started execution")
				}
			}()
			if err := tr.Click("Choose UTF-8 file…"); err != nil {
				t.Fatalf("TF1: reachable file form has no consumer Choose action: %v", err)
			}
			themeImportCompletion(t, callbacks)()
			tr.Frame()
			if !tr.Focused("Choose UTF-8 file…") {
				t.Fatal("cancel did not return chooser focus")
			}
		})
	}
}

func contextFileChooserTestApp(t *testing.T, issueOwner bool) (*appModel, *contact, *taskContext, *ui.Tester, chan func()) {
	t.Helper()
	a, ct := taskContextApp(t)
	a.uiLocale = "en"
	callbacks := make(chan func(), 8)
	a.setUIHook(func(fn func()) { callbacks <- fn })
	target := &ct.Context
	if issueOwner {
		issue := &productIssue{ID: "file-issue", ContactID: ct.ID, Goal: "File context", Status: issueStatusNotStarted}
		issue.Context.onChange = a.persist
		ct.Issues = []*productIssue{issue}
		target = &issue.Context
		a.openIssueTaskContext(issue.ID)
	} else {
		a.openConversationTaskContext()
	}
	tr := ui.NewTester(a.view, 1040, 900)
	tr.SetLocale("en")
	tr.Frame()
	if err := tr.Click("Add file"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	return a, ct, target, tr, callbacks
}

func TestTaskContextChooserDefersReadUntilSaveAndRefresh(t *testing.T) {
	for index, name := range []string{"资料 with spaces", "drawing.svg", "source.go", "empty"} {
		t.Run(name, func(t *testing.T) {
			a, _, tc, tr, callbacks := contextFileChooserTestApp(t, index%2 == 1)
			path := filepath.Join(t.TempDir(), name)
			before := recoverySavedBytes(t, a.store.path)
			// A missing first path proves no read; other files change between choice and Save.
			if index > 0 {
				if err := os.WriteFile(path, []byte("bytes before selection"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			requests := make(chan mygo.OpenDialogOptions, 1)
			results := make(chan []string, 1)
			a.win = &mygo.Window{}
			a.chooseContextFile = func(opts mygo.OpenDialogOptions) ([]string, error) { requests <- opts; return <-results, nil }
			if err := tr.Click("Choose UTF-8 file…"); err != nil {
				t.Fatal(err)
			}
			opts := <-requests
			if opts.Parent != a.win || opts.Directory || opts.Multiple || len(opts.Filters) != 0 {
				t.Fatal("chooser is not parented, single-file and unrestricted by extension")
			}
			if err := tr.Click("Context file name"); err != nil {
				t.Fatal(err)
			}
			tr.Type("Latest edited name")
			results <- []string{path}
			themeImportCompletion(t, callbacks)()
			tr.Frame()
			if tc.formPath != path || tc.formName != "Latest edited name" || tc.count() != 0 || tc.formError != nil || string(recoverySavedBytes(t, a.store.path)) != string(before) || a.rt != nil {
				t.Fatal("choosing read/saved/dispatched, or lost a later name edit")
			}
			if !tr.Focused("Choose UTF-8 file…") {
				t.Fatal("selection lost initiating control focus")
			}
			text := "saved now — 你好\n<svg>literal text</svg>"
			if name == "empty" {
				text = ""
			}
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if err := tr.Click("Save file"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if tc.formOpen || tc.count() != 1 {
				t.Fatalf("explicit Save did not import: %v", tc.formError)
			}
			item := tc.snapshot()[0]
			if item.Text != text || item.Name != "Latest edited name" || item.Source != path || item.Revision != 1 || item.ID == "" || tc.previewID != item.ID {
				t.Fatalf("saved snapshot = %+v", item)
			}
			if err := os.WriteFile(path, []byte("changed after Save"), 0600); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if tc.snapshot()[0] != item {
				t.Fatal("disk edits silently changed the snapshot")
			}
			if err := tr.Click("Refresh from file"); err != nil {
				t.Fatal(err)
			}
			if refreshed := tc.snapshot()[0]; refreshed.ID != item.ID || refreshed.Revision != item.Revision+1 || refreshed.Text != "changed after Save" || refreshed.Name != item.Name || refreshed.Source != path {
				t.Fatalf("explicit Refresh = %+v", refreshed)
			}
		})
	}
}

func TestTaskContextChooserCancelErrorRetryKeepsEditor(t *testing.T) {
	for _, language := range []string{"en", "zh-Hans"} {
		t.Run(language, func(t *testing.T) {
			a, _, tc, tr, callbacks := contextFileChooserTestApp(t, false)
			a.uiLocale = language
			tr.SetLocale(language)
			msgs := locales.In(language)
			tc.formName, tc.formPath = "keep name", "/old/path"
			tc.formError = errTaskContextRead
			originalError := tc.formError
			before := recoverySavedBytes(t, a.store.path)
			for _, result := range []string{"error", "cancel", "success"} {
				a.chooseContextFile = func(opts mygo.OpenDialogOptions) ([]string, error) {
					if opts.DefaultPath != "/old/path" {
						t.Error("chooser ignored the current absolute draft")
					}
					if result == "error" {
						return nil, errors.New("native panel unavailable")
					}
					if result == "success" {
						return []string{"/new/选择的文件"}, nil
					}
					return nil, nil
				}
				tr.Frame()
				if err := tr.Click(msgs.TaskContextChooseFile()); err != nil {
					t.Fatal(err)
				}
				themeImportCompletion(t, callbacks)()
				tr.Frame()
				if !tr.Focused(msgs.TaskContextChooseFile()) || tc.formName != "keep name" || tc.formError != originalError || tc.count() != 0 || string(recoverySavedBytes(t, a.store.path)) != string(before) {
					t.Fatal("dialog result lost input/error/focus or saved data")
				}
				if result != "success" && tc.formPath != "/old/path" {
					t.Fatal("cancel/error changed the draft path")
				}
				if result != "success" && !tr.HasText(msgs.TaskContextChooseFileFailed()) {
					t.Fatal("error or cancel-after-error lost actionable chooser feedback")
				}
				if result == "success" && (tc.formPath != "/new/选择的文件" || tc.fileChoiceFailed) {
					t.Fatal("retry did not replace only the path")
				}
			}
		})
	}
}

func TestTaskContextChooserStaleResultsNeverMutateOrFocus(t *testing.T) {
	for _, scenario := range []string{"contact-roundtrip", "contact-replaced", "issue-roundtrip", "issue-replaced", "context-replaced", "cancel-reopen", "close-reopen", "settings-roundtrip", "path-roundtrip", "shutdown"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/error-%t", scenario, fail), func(t *testing.T) {
				issueOwner := strings.HasPrefix(scenario, "issue-")
				a, ct, tc, tr, callbacks := contextFileChooserTestApp(t, issueOwner)
				tc.formName, tc.formPath = "original name", "/original/path"
				release := make(chan struct{})
				a.chooseContextFile = func(mygo.OpenDialogOptions) ([]string, error) {
					<-release
					if fail {
						return nil, errors.New("late failure")
					}
					return []string{"/late/path"}, nil
				}
				tr.Frame()
				if err := tr.Click("Choose UTF-8 file…"); err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "contact-roundtrip":
					a.contacts = append(a.contacts, &contact{ID: "other", Name: "Other", Loaded: true})
					a.selectContact(1)
					a.selectContact(0)
					a.openConversationTaskContext()
				case "contact-replaced":
					a.contacts[0] = &contact{ID: ct.ID, Name: "Replacement"}
				case "issue-roundtrip":
					other := &productIssue{ID: "other-issue", ContactID: ct.ID}
					ct.Issues = append(ct.Issues, other)
					a.openIssueTaskContext(other.ID)
					a.openIssueTaskContext("file-issue")
				case "issue-replaced":
					ct.Issues[0] = &productIssue{ID: "file-issue", ContactID: ct.ID}
				case "context-replaced":
					ct.Context = taskContext{}
					ct.Context.openFileForm()
				case "cancel-reopen":
					tc.cancelForm()
					tc.openFileForm()
					tc.formName, tc.formPath = "new name", "/new/form"
				case "close-reopen":
					a.closeAux()
					a.openConversationTaskContext()
				case "settings-roundtrip":
					a.openAppearance()
					a.closeSettings()
				case "path-roundtrip":
					if err := tr.Click("Context file path"); err != nil {
						t.Fatal(err)
					}
					tr.Key(ui.Cmd, ui.KeyA)
					tr.Type("/edited/path")
					tr.Key(ui.Cmd, ui.KeyA)
					tr.Type("/original/path")
				case "shutdown":
					a.beginShutdown()
					if !a.quitReady {
						t.Fatal("draft-only dialog delayed Quit")
					}
				}
				name, path, formErr := tc.formName, tc.formPath, tc.formError
				disk := recoverySavedBytes(t, a.store.path)
				close(release)
				themeImportCompletion(t, callbacks)()
				if tc.formName != name || tc.formPath != path || tc.formError != formErr || tc.fileChoiceFailed || tc.focusFileChooser || tc.count() != 0 || a.contextFilePickers != 0 || string(recoverySavedBytes(t, a.store.path)) != string(disk) || a.rt != nil {
					t.Fatal("stale chooser changed editor/data/focus or leaked reservation")
				}
			})
		}
	}
}

func TestTaskContextChooserLatestRequestOwnsPathAndFocus(t *testing.T) {
	a, ct, tc, tr, callbacks := contextFileChooserTestApp(t, false)
	first, second := make(chan struct{}), make(chan struct{})
	request := make(chan struct{}, 1)
	a.chooseContextFile = func(mygo.OpenDialogOptions) ([]string, error) {
		request <- struct{}{}
		<-first
		return []string{"/old-result"}, nil
	}
	if err := tr.Click("Choose UTF-8 file…"); err != nil {
		t.Fatal(err)
	}
	<-request
	if err := tr.Click("Context file path"); err != nil {
		t.Fatal(err)
	}
	tr.Type("/new draft")
	a.chooseContextFile = func(mygo.OpenDialogOptions) ([]string, error) {
		request <- struct{}{}
		<-second
		return []string{"/new-result"}, nil
	}
	tr.Frame()
	if err := tr.Click("Choose UTF-8 file…"); err != nil {
		t.Fatal(err)
	}
	<-request
	close(second)
	themeImportCompletion(t, callbacks)()
	tr.Frame()
	if tc.formPath != "/new-result" || !tr.Focused("Choose UTF-8 file…") {
		t.Fatal("newer choice did not own path/focus")
	}
	if err := tr.Click("Context file name"); err != nil {
		t.Fatal(err)
	}
	tr.Type("last name")
	close(first)
	themeImportCompletion(t, callbacks)()
	tr.Frame()
	if tc.formPath != "/new-result" || tc.formName != "last name" || !tr.Focused("Context file name") || a.contextFilePickers != 0 || ct.Context.count() != 0 {
		t.Fatal("older result overwrote path/name/focus")
	}
}

func TestTaskContextChooserKeepsReadAndSaveFailureBoundaries(t *testing.T) {
	for _, kind := range []string{"missing", "invalid-utf8", "nul", "unsupported", "oversize", "wrapped-budget", "store-error"} {
		t.Run(kind, func(t *testing.T) {
			a, _, tc, tr, callbacks := contextFileChooserTestApp(t, false)
			old, err := tc.AddSnippet("keep", "original saved material")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "file")
			data := []byte("valid UTF-8")
			switch kind {
			case "invalid-utf8":
				data = []byte{0xff}
			case "nul":
				data = []byte("valid\x00binary")
			case "unsupported":
				path += ".pdf"
			case "oversize":
				data = []byte(strings.Repeat("x", taskContextMaxBytes+1))
			case "wrapped-budget":
				data = []byte(strings.Repeat("<", taskContextMaxBytes/4))
			}
			if kind != "missing" {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := recoverySavedBytes(t, a.store.path)
			a.chooseContextFile = func(mygo.OpenDialogOptions) ([]string, error) { return []string{path}, nil }
			tr.Frame()
			if err := tr.Click("Choose UTF-8 file…"); err != nil {
				t.Fatal(err)
			}
			themeImportCompletion(t, callbacks)()
			tr.Frame()
			if tc.formError != nil || tc.formPath != path || tc.count() != 1 || tc.snapshot()[0] != old || string(recoverySavedBytes(t, a.store.path)) != string(before) {
				t.Fatal("choosing crossed the read/persistence boundary")
			}
			if kind == "store-error" {
				tc.onChange = func() error { return errors.New("controlled local write failed") }
			}
			if err := tr.Click("Save file"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tc.formOpen || tc.formPath != path || tc.formError == nil || tc.count() != 1 || tc.snapshot()[0] != old || string(recoverySavedBytes(t, a.store.path)) != string(before) || a.rt != nil {
				t.Fatalf("explicit failed Save lost prior state: %v", tc.formError)
			}
		})
	}
}

func contextFileReach(t *testing.T, tr *ui.Tester, label string, width, height int, minHeight float32) ui.Rect {
	t.Helper()
	for attempt := 0; attempt < 24; attempt++ {
		if box, ok := tr.Find(label); ok && box.H >= minHeight && box.X >= 0 && box.X+box.W <= float32(width)+1 && box.Y >= 0 && box.Y+box.H <= float32(height) {
			return box
		}
		tr.Scroll(float32(width-80), float32(height/2), 0, 60)
		tr.Frame()
	}
	t.Fatalf("file form control/text %q is not reachable at %dx%d", label, width, height)
	return ui.Rect{}
}

func TestTaskContextChooserBilingualFormsAndPreviews(t *testing.T) {
	for _, issueOwner := range []bool{false, true} {
		for _, language := range []string{"en", "zh-Hans"} {
			for _, size := range [][2]int{{720, 640}, {960, 640}, {1040, 720}} {
				for _, dark := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%d/dark-%t/issue-%t", language, size[0], dark, issueOwner), func(t *testing.T) {
						a, ct := taskContextApp(t)
						a.windowWidth = size[0]
						a.uiLocale = language
						if dark {
							a.appearanceMode = appearanceDark
						} else {
							a.appearanceMode = appearanceLight
						}
						target := &ct.Context
						if issueOwner {
							issue := &productIssue{ID: "minimum-issue", ContactID: ct.ID, Goal: "Choose task material", Status: issueStatusNotStarted}
							issue.Context.onChange = a.persist
							ct.Issues = []*productIssue{issue}
							target = &issue.Context
							a.openIssueTaskContext(issue.ID)
						} else {
							a.openConversationTaskContext()
						}
						callbacks := make(chan func(), 4)
						a.setUIHook(func(fn func()) { callbacks <- fn })
						path := filepath.Join(t.TempDir(), "资料 source.svg")
						if err := os.WriteFile(path, []byte("<svg>Exact UTF-8 text — 你好</svg>\nsecond line"), 0600); err != nil {
							t.Fatal(err)
						}
						tr := ui.NewTester(a.view, size[0], size[1])
						tr.SetLocale(language)
						tr.Frame()
						msgs := locales.In(language)
						if err := tr.Click(msgs.TaskContextAddFile()); err != nil {
							t.Fatal(err)
						}
						tr.Frame()
						for _, state := range []string{"form", "pending", "error", "preview"} {
							switch state {
							case "pending":
								// Hold the picker; capture its real pending form before resolving it.
							case "error":
								themeImportCompletion(t, callbacks)()
								tr.Frame()
							case "preview":
								a.chooseContextFile = func(mygo.OpenDialogOptions) ([]string, error) { return []string{path}, nil }
								if err := tr.Click(msgs.TaskContextChooseFile()); err != nil {
									t.Fatal(err)
								}
								themeImportCompletion(t, callbacks)()
								tr.Frame()
								contextFileReach(t, tr, msgs.TaskContextSaveFile(), size[0], size[1], 32)
								if err := tr.Click(msgs.TaskContextSaveFile()); err != nil {
									t.Fatal(err)
								}
								tr.Frame()
							}
							bounds := map[string]ui.Rect{"window": {W: float32(size[0]), H: float32(size[1])}}
							if state != "preview" {
								bounds["chooser"] = contextFileReach(t, tr, msgs.TaskContextChooseFile(), size[0], size[1], 32)
								hint := msgs.TaskContextChooseFileHint()
								r, ok := tr.Find(hint)
								if !ok {
									t.Fatal("chooser explanation missing")
								}
								_, full := ui.ShapeText(hint, ui.Font{Size: typeAction, LineHeight: 1.5, Lang: language}, r.W).Size()
								bounds["hint"] = contextFileReach(t, tr, hint, size[0], size[1], full-0.5)
								if state == "error" {
									r, ok := tr.Find(msgs.TaskContextChooseFileFailed())
									if !ok {
										t.Fatal("error missing")
									}
									_, full := ui.ShapeText(msgs.TaskContextChooseFileFailed(), ui.Font{Size: typeAction, LineHeight: 1.5, Lang: language}, r.W).Size()
									bounds["error"] = contextFileReach(t, tr, msgs.TaskContextChooseFileFailed(), size[0], size[1], full-0.5)
								}
								bounds["save"] = contextFileReach(t, tr, msgs.TaskContextSaveFile(), size[0], size[1], 32)
							} else {
								if target.count() != 1 || target.previewID == "" {
									t.Fatal("explicit Save did not open the snapshot preview")
								}
								bounds["copy"] = contextFileReach(t, tr, msgs.CopyContextTextLabel(), size[0], size[1], 32)
							}
							if dir := os.Getenv("SAKI_PREVIEW_DIR"); dir != "" {
								if err := os.MkdirAll(dir, 0755); err != nil {
									t.Fatal(err)
								}
								name := fmt.Sprintf("chooser-%s-%d-dark-%t-%s", language, size[0], dark, state)
								if issueOwner {
									name += "-issue"
								}
								writePreviewPNG(t, filepath.Join(dir, name+".png"), tr.Image())
								raw, _ := json.MarshalIndent(bounds, "", "  ")
								if err := os.WriteFile(filepath.Join(dir, name+".bounds.json"), raw, 0600); err != nil {
									t.Fatal(err)
								}
							}
							if state == "form" {
								a.chooseContextFile = func(mygo.OpenDialogOptions) ([]string, error) { return nil, errors.New("controlled panel failure") }
								// The final callback stays queued while the pending surface is inspected.
								contextFileReach(t, tr, msgs.TaskContextChooseFile(), size[0], size[1], 32)
								if err := tr.Click(msgs.TaskContextChooseFile()); err != nil {
									t.Fatal(err)
								}
								tr.Frame()
							}
						}
					})
				}
			}
		}
	}
}

func TestTaskContextChooserKeyboardAtMinimum(t *testing.T) {
	for _, issueOwner := range []bool{false, true} {
		for _, language := range []string{"en", "zh-Hans"} {
			for _, dark := range []bool{false, true} {
				for _, activation := range []struct {
					name string
					key  ui.Key
				}{{"Enter", ui.KeyEnter}, {"Space", ui.KeySpace}} {
					for _, outcome := range []string{"selection", "cancel"} {
						t.Run(fmt.Sprintf("%s/dark-%t/issue-%t/%s/%s", language, dark, issueOwner, activation.name, outcome), func(t *testing.T) {
							a, ct := taskContextApp(t)
							a.windowWidth = 720
							a.uiLocale = language
							a.win = &mygo.Window{}
							if dark {
								a.appearanceMode = appearanceDark
							} else {
								a.appearanceMode = appearanceLight
							}
							ct.Draft = "Retain this unsent conversation draft — 你好"
							target := &ct.Context
							if issueOwner {
								issue := &productIssue{ID: "keyboard-issue", ContactID: ct.ID, Goal: "Keyboard file choice", Status: issueStatusNotStarted}
								issue.Context.onChange = a.persist
								ct.Issues = []*productIssue{issue}
								target = &issue.Context
								a.openIssueTaskContext(issue.ID)
							} else {
								a.openConversationTaskContext()
							}
							original, err := target.AddSnippet("Existing material", "Keep this saved snapshot")
							if err != nil {
								t.Fatal(err)
							}
							callbacks := make(chan func(), 4)
							a.setUIHook(func(fn func()) { callbacks <- fn })
							requests := make(chan mygo.OpenDialogOptions, 2)
							results := make(chan []string, 1)
							var calls atomic.Int32
							a.chooseContextFile = func(opts mygo.OpenDialogOptions) ([]string, error) {
								calls.Add(1)
								requests <- opts
								return <-results, nil
							}
							path := filepath.Join(t.TempDir(), "选择 UTF8 source.svg")
							if err := os.WriteFile(path, []byte("<svg>not read by choice — 你好</svg>"), 0600); err != nil {
								t.Fatal(err)
							}
							tr := ui.NewTester(a.view, 720, 640)
							tr.SetLocale(language)
							tr.SetFocused(true)
							tr.Frame()
							msgs := locales.In(language)
							if err := tr.Click(msgs.TaskContextAddFile()); err != nil {
								t.Fatal(err)
							}
							tr.Frame()
							if err := tr.Click(msgs.TaskContextFilePathLabel()); err != nil {
								t.Fatal(err)
							}
							tr.Type("/retained/manual")
							if err := tr.Click(msgs.TaskContextFileLabel()); err != nil {
								t.Fatal(err)
							}
							tr.Type("Keep the optional name")
							target.formError = errTaskContextRead
							originalError := target.formError
							tr.Frame()
							if tr.Focused(msgs.TaskContextChooseFile()) {
								t.Fatal("fixture directly focused chooser before Tab")
							}
							before := recoverySavedBytes(t, a.store.path)
							draft := ct.Draft
							events := []string{"Click path input", "Type /retained/manual", "Click name input", "Type Keep the optional name"}
							tabs := 0
							for tabs < 20 && !tr.Focused(msgs.TaskContextChooseFile()) {
								tr.Key(0, ui.KeyTab)
								tr.Frame()
								tabs++
								events = append(events, "Tab")
							}
							if tabs == 0 || !tr.Focused(msgs.TaskContextChooseFile()) {
								t.Fatal("visible chooser is not reachable through Tab navigation")
							}
							bounds, ok := tr.Find(msgs.TaskContextChooseFile())
							if !ok || bounds.W < 32 || bounds.H < 32 || bounds.X < 0 || bounds.Y < 0 || bounds.X+bounds.W > 720 || bounds.Y+bounds.H > 640 {
								t.Fatalf("keyboard reached clipped chooser: %+v", bounds)
							}
							tr.Key(0, activation.key)
							tr.Frame()
							events = append(events, activation.name)
							var opts mygo.OpenDialogOptions
							select {
							case opts = <-requests:
							case <-time.After(5 * time.Second):
								t.Fatal("keyboard activation did not invoke the injected chooser")
							}
							if calls.Load() != 1 || opts.Parent != a.win || opts.Directory || opts.Multiple || len(opts.Filters) != 0 {
								t.Fatal("keyboard activation did not invoke exactly one parented single-file chooser")
							}
							if outcome == "cancel" {
								results <- nil
							} else {
								results <- []string{path}
							}
							themeImportCompletion(t, callbacks)()
							tr.Frame()
							wantPath := "/retained/manual"
							if outcome == "selection" {
								wantPath = path
							}
							if calls.Load() != 1 || target.formPath != wantPath || target.formName != "Keep the optional name" || target.formError != originalError || target.count() != 1 || target.snapshot()[0] != original || ct.Draft != draft || !tr.Focused(msgs.TaskContextChooseFile()) || a.contextFilePickers != 0 || a.rt != nil || a.submissionInFlight != 0 || string(recoverySavedBytes(t, a.store.path)) != string(before) {
								t.Fatal("keyboard choice/cancel changed saved material, draft, errors, execution or focus")
							}
							if dir := os.Getenv("SAKI_PREVIEW_DIR"); dir != "" {
								if err := os.MkdirAll(dir, 0755); err != nil {
									t.Fatal(err)
								}
								name := fmt.Sprintf("keyboard-%s-720-dark-%t-issue-%t-%s-%s", language, dark, issueOwner, activation.name, outcome)
								writePreviewPNG(t, filepath.Join(dir, name+".png"), tr.Image())
								raw, _ := json.MarshalIndent(struct {
									Events                                                  []string
									TabCount                                                int
									ChooserCalls                                            int32
									WindowWidth                                             int
									Width, Height                                           int
									Chooser                                                 ui.Rect
									ParentMatched, SingleFile, FocusReturned, DiskUnchanged bool
								}{events, tabs, calls.Load(), a.windowWidth, 720, 640, bounds, opts.Parent == a.win, !opts.Multiple && !opts.Directory, tr.Focused(msgs.TaskContextChooseFile()), string(recoverySavedBytes(t, a.store.path)) == string(before)}, "", "  ")
								if err := os.WriteFile(filepath.Join(dir, name+".events.json"), raw, 0600); err != nil {
									t.Fatal(err)
								}
							}
						})
					}
				}
			}
		}
	}
}
