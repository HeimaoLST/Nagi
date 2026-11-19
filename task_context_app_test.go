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

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func taskContextApp(t *testing.T) (*appModel, *contact) {
	t.Helper()
	store, err := openContactStore(filepath.Join(t.TempDir(), "contacts.json"), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	ct := &contact{ID: "nagi-context", Name: "Nagi", Role: "Assistant", SessionID: "s1", PiBinding: recoveryConversationBinding("nagi-context", "/tmp", "s1"), Loaded: true}
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
	a, ct, event := piRunStartedTestApp(t)
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
	ct.SubmittedDraft, ct.SubmittedRev = "prompt", ct.draftRev
	ct.pendingSend = &pendingSend{Draft: "prompt", Context: submitted, State: "submitting"}
	a.applyPiEvent(a.connectionGeneration, event)

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
	if ct.Messages[0].Context[0] != submitted[0] || ct.Messages[0].Context[1] != submitted[1] {
		t.Fatalf("message did not retain the exact submitted values: %+v", ct.Messages[0].Context)
	}
	if ct.pendingSend == nil || ct.pendingSend.State != "unknown" || ct.pendingSend.Draft != "prompt" {
		t.Fatalf("admitted send lost unsettled intent: %+v", ct.pendingSend)
	}
	saved := a.store.state.Contacts[0].PendingSend
	if saved == nil || saved.State != "unknown" || len(saved.Context) != 2 || saved.Context[0] != submitted[0] || saved.Context[1] != submitted[1] {
		t.Fatalf("saved intent lost admitted materials before settlement: %+v", saved)
	}
}

func TestTaskContextAcceptedSaveFailureKeepsNextDraftVisible(t *testing.T) {
	a, ct, event := piRunStartedTestApp(t)
	item, err := ct.Context.AddSnippet("Keep", "keep after acceptance")
	if err != nil {
		t.Fatal(err)
	}
	ct.Draft, ct.SubmittedDraft = "next draft", "prompt"
	ct.SubmittedRev = ct.draftRev
	ct.draftRev++
	ct.pendingSend = &pendingSend{Draft: "prompt", Context: ct.Context.snapshot(), State: "submitting"}
	a.store.path = t.TempDir()
	a.applyPiEvent(a.connectionGeneration, event)
	if got := ct.Context.snapshot(); len(got) != 1 || got[0] != item {
		t.Fatalf("admitted+savefailure cleared user data: %+v", got)
	}
	if ct.Draft != "next draft" {
		t.Fatalf("admitted+savefailure changed the later draft: %q", ct.Draft)
	}
	if !strings.Contains(ct.SendError, "admitted") || ct.SendErrorKind != "send-uncertain" {
		t.Fatalf("admitted save failure was not explicit: %q / %q", ct.SendError, ct.SendErrorKind)
	}
	if ct.SendState != sendUnknown || ct.ActiveTaskID != event.RunID || ct.LastTaskID != event.RunID {
		t.Fatalf("save failure lost the known run: state=%v active=%q last=%q", ct.SendState, ct.ActiveTaskID, ct.LastTaskID)
	}
	if ct.pendingSend == nil || ct.pendingSend.State != "unknown" || len(ct.pendingSend.Context) != 1 || ct.pendingSend.Context[0] != item {
		t.Fatalf("save failure lost unsettled materials: %+v", ct.pendingSend)
	}
	if len(ct.Messages) != 1 || ct.Messages[0].Text != "prompt" || ct.Messages[0].TaskID != event.RunID || len(ct.Messages[0].Context) != 1 || ct.Messages[0].Context[0] != item {
		t.Fatalf("save failure lost admitted message context: %+v", ct.Messages)
	}
}

func TestTaskContextRejectedSendKeepsMaterials(t *testing.T) {
	a, ct, event := piRunStartedTestApp(t)
	item, err := ct.Context.AddSnippet("Keep", "rejected context")
	if err != nil {
		t.Fatal(err)
	}
	ct.Draft = "rejected"
	ct.SubmittedDraft = ct.Draft
	ct.pendingSend = &pendingSend{Draft: ct.Draft, Context: ct.Context.snapshot(), State: "submitting"}
	a.failPiSubmission(a.pi, ct.ID, event.RunID,
		&pi.CommandError{Method: "run.start", Delivery: pi.Rejected, Err: &pi.RemoteError{Code: "capacity", Message: "full"}}, false)
	items := ct.Context.snapshot()
	if len(items) != 1 || items[0] != item || ct.pendingSend != nil || ct.Draft != "rejected" {
		t.Fatalf("rejected send changed materials: context=%+v draft=%q pending=%+v", items, ct.Draft, ct.pendingSend)
	}
	if ct.SendState != sendIdle {
		t.Fatalf("confirmed rejection state = %v", ct.SendState)
	}
}

func TestTaskContextUnknownFailurePersistsPendingIntent(t *testing.T) {
	a, ct, event := piRunStartedTestApp(t)
	item, err := ct.Context.AddSnippet("Maybe sent", "possibly delivered")
	if err != nil {
		t.Fatal(err)
	}
	ct.Draft = "uncertain prompt"
	ct.pendingSend = &pendingSend{Draft: ct.Draft, Context: ct.Context.snapshot(), State: "submitting"}
	ct.SubmittedDraft = ct.Draft
	a.failPiSubmission(a.pi, ct.ID, event.RunID,
		&pi.CommandError{Method: "run.start", Delivery: pi.Unanswered, Err: errors.New("disconnected")}, true)
	if ct.SendState != sendUnknown || ct.pendingSend == nil || ct.pendingSend.State != "unknown" {
		t.Fatalf("unknown failure = state %v pending %+v", ct.SendState, ct.pendingSend)
	}
	items := ct.Context.snapshot()
	if len(items) != 1 || items[0] != item || ct.Draft != "uncertain prompt" {
		t.Fatalf("unknown failure lost materials: context=%+v draft=%q", items, ct.Draft)
	}
	store, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	saved := store.state.Contacts[0].PendingSend
	if saved == nil || saved.State != "unknown" || saved.Draft != "uncertain prompt" || len(saved.Context) != 1 || saved.Context[0] != item {
		t.Fatalf("unknown intent or its materials were not persisted: %+v", saved)
	}
}

func TestTaskContextAppAndReadOnlyChatCopy(t *testing.T) {
	a, ct := taskContextApp(t)
	if _, err := ct.Context.AddSnippet("Conversation notes", "conversation body"); err != nil {
		t.Fatal(err)
	}
	a.openConversationTaskContext()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	for _, want := range []string{"Task context", "Chat: Nagi", "Conversation notes", "Close task context"} {
		if !tr.HasText(want) {
			t.Fatalf("conversation modal missing %q: %v", want, tr.Texts())
		}
	}

	ct.Draft = "draft remains local"
	a.closeAux()
	ct.Messages = []*message{{
		Key: "live:readonly", Kind: msgUser, Text: "main prompt",
		Context: []taskContextItem{{Name: "Doc · file /tmp/doc.txt", Text: "read only text"}},
	}}
	tr = ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("main prompt") || !tr.HasText("read only text") {
		t.Fatalf("live context missing: %v", tr.Texts())
	}
	if err := tr.Click("Copy context text"); err != nil {
		t.Fatal(err)
	}
	if tr.Clipboard() != "read only text" || ct.Draft != "draft remains local" || a.selectedContact() != ct {
		t.Fatal("copy changed the submitted context, draft or owning contact")
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
	dir := os.Getenv("NAGI_PREVIEW_DIR")
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

func TestTaskContextRestoredSubmittingIntentRemainsUnknown(t *testing.T) {
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
	if reloaded.pi != nil {
		t.Fatal("restart unknown state created a runtime or replayed a send")
	}
}

func contextFileChooserTestApp(t *testing.T) (*appModel, *contact, *taskContext, *ui.Tester, chan func()) {
	t.Helper()
	a, ct := taskContextApp(t)
	a.uiLocale = "en"
	callbacks := make(chan func(), 8)
	a.setUIHook(func(fn func()) { callbacks <- fn })
	target := &ct.Context
	a.openConversationTaskContext()
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
			a, _, tc, tr, callbacks := contextFileChooserTestApp(t)
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
			if tc.formPath != path || tc.formName != "Latest edited name" || tc.count() != 0 || tc.formError != nil || string(recoverySavedBytes(t, a.store.path)) != string(before) || a.pi != nil {
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
			a, _, tc, tr, callbacks := contextFileChooserTestApp(t)
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
	for _, scenario := range []string{"contact-roundtrip", "contact-replaced", "context-replaced", "cancel-reopen", "close-reopen", "settings-roundtrip", "path-roundtrip", "shutdown"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/error-%t", scenario, fail), func(t *testing.T) {
				a, ct, tc, tr, callbacks := contextFileChooserTestApp(t)
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
				if tc.formName != name || tc.formPath != path || tc.formError != formErr || tc.fileChoiceFailed || tc.focusFileChooser || tc.count() != 0 || a.contextFilePickers != 0 || string(recoverySavedBytes(t, a.store.path)) != string(disk) || a.pi != nil {
					t.Fatal("stale chooser changed editor/data/focus or leaked reservation")
				}
			})
		}
	}
}

func TestTaskContextChooserLatestRequestOwnsPathAndFocus(t *testing.T) {
	a, ct, tc, tr, callbacks := contextFileChooserTestApp(t)
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
			a, _, tc, tr, callbacks := contextFileChooserTestApp(t)
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
			if !tc.formOpen || tc.formPath != path || tc.formError == nil || tc.count() != 1 || tc.snapshot()[0] != old || string(recoverySavedBytes(t, a.store.path)) != string(before) || a.pi != nil {
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
	for _, language := range []string{"en", "zh-Hans"} {
		for _, size := range [][2]int{{720, 640}, {960, 640}, {1040, 720}} {
			for _, dark := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/dark-%t", language, size[0], dark), func(t *testing.T) {
					a, ct := taskContextApp(t)
					a.windowWidth = size[0]
					a.uiLocale = language
					if dark {
						a.appearanceMode = appearanceDark
					} else {
						a.appearanceMode = appearanceLight
					}
					target := &ct.Context
					a.openConversationTaskContext()
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
							_, full := ui.ShapeText(hint, ui.Font{Size: typeMeta, LineHeight: lineMeta / typeMeta, Lang: language}, r.W).Size()
							bounds["hint"] = contextFileReach(t, tr, hint, size[0], size[1], full-0.5)
							if state == "error" {
								r, ok := tr.Find(msgs.TaskContextChooseFileFailed())
								if !ok {
									t.Fatal("error missing")
								}
								_, full := ui.ShapeText(msgs.TaskContextChooseFileFailed(), ui.Font{Size: typeMeta, LineHeight: lineMeta / typeMeta, Lang: language}, r.W).Size()
								bounds["error"] = contextFileReach(t, tr, msgs.TaskContextChooseFileFailed(), size[0], size[1], full-0.5)
							}
							bounds["save"] = contextFileReach(t, tr, msgs.TaskContextSaveFile(), size[0], size[1], 32)
						} else {
							if target.count() != 1 || target.previewID == "" {
								t.Fatal("explicit Save did not open the snapshot preview")
							}
							bounds["copy"] = contextFileReach(t, tr, msgs.CopyContextTextLabel(), size[0], size[1], 32)
						}
						if dir := os.Getenv("NAGI_PREVIEW_DIR"); dir != "" {
							if err := os.MkdirAll(dir, 0755); err != nil {
								t.Fatal(err)
							}
							name := fmt.Sprintf("chooser-%s-%d-dark-%t-%s", language, size[0], dark, state)
							writePreviewPNG(t, filepath.Join(dir, name+".png"), tr.Image())
							raw, _ := json.MarshalIndent(bounds, "", "  ")
							if err := os.WriteFile(filepath.Join(dir, name+".bounds.json"), raw, 0600); err != nil {
								t.Fatal(err)
							}
						}
						if state == "form" {
							a.chooseContextFile = func(mygo.OpenDialogOptions) ([]string, error) { return nil, errors.New("controlled panel failure") }
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

func TestTaskContextChooserKeyboardAtMinimum(t *testing.T) {
	for _, language := range []string{"en", "zh-Hans"} {
		for _, dark := range []bool{false, true} {
			for _, activation := range []struct {
				name string
				key  ui.Key
			}{{"Enter", ui.KeyEnter}, {"Space", ui.KeySpace}} {
				for _, outcome := range []string{"selection", "cancel"} {
					t.Run(fmt.Sprintf("%s/dark-%t/%s/%s", language, dark, activation.name, outcome), func(t *testing.T) {
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
						a.openConversationTaskContext()
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
						if calls.Load() != 1 || target.formPath != wantPath || target.formName != "Keep the optional name" || target.formError != originalError || target.count() != 1 || target.snapshot()[0] != original || ct.Draft != draft || !tr.Focused(msgs.TaskContextChooseFile()) || a.contextFilePickers != 0 || a.pi != nil || a.submissionInFlight != 0 || string(recoverySavedBytes(t, a.store.path)) != string(before) {
							t.Fatal("keyboard choice/cancel changed saved material, draft, errors, execution or focus")
						}
						if dir := os.Getenv("NAGI_PREVIEW_DIR"); dir != "" {
							if err := os.MkdirAll(dir, 0755); err != nil {
								t.Fatal(err)
							}
							name := fmt.Sprintf("keyboard-%s-720-dark-%t-%s-%s", language, dark, activation.name, outcome)
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
