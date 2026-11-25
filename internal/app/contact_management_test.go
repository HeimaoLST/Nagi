package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func TestContactWorkspaceDefaultsRespectConnectionSource(t *testing.T) {
	for _, source := range []string{"managed", "unconfigured", "invalid", "demo"} {
		t.Run(source, func(t *testing.T) {
			launchFolder := t.TempDir()
			isolateConnectionApp(t, launchFolder, filepath.Join(launchFolder, "unused-runtime"))
			want := launchFolder
			var a *appModel
			if source == "demo" {
				a = &appModel{demo: true, workDir: launchFolder, contacts: []*contact{{ID: "demo-contact", Name: "Saki"}}}
			} else {
				want = ""
				if source == "managed" || source == "invalid" {
					config := testConnectionConfig(t)
					if err := saveConnectionConfig(connectionConfigPath(guiStorePath()), config); err != nil {
						t.Fatal(err)
					}
					want = config.WorkingDirectory
					if source == "invalid" {
						if err := os.Remove(config.WorkingDirectory); err != nil {
							t.Fatal(err)
						}
						want = ""
					}
				}
				var err error
				a, err = newRuntimeApp()
				if err != nil {
					t.Fatal(err)
				}
				if a.selectedContact().Workspace != want {
					t.Errorf("%s initial contact workspace=%q, want %q", source, a.selectedContact().Workspace, want)
				}
			}
			a.addRuntimeContact()
			if got := a.selectedContact().Workspace; got != want {
				t.Errorf("%s added contact workspace=%q, want %q", source, got, want)
			}
			if a.pi != nil || a.providerBusy() {
				t.Fatal("contact defaults started Runtime")
			}
		})
	}
}

func TestLocalStateBeforeConnectionCreateSaveAndRestart(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, filepath.Join(root, "unused-runtime"))
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	firstID := a.selectedContact().ID
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	focusComposer(t, tr, "Composer for Nagi")
	tr.Type("Keep the first local draft")
	tr.Frame()
	if err := tr.Click(locales.In("en").AvatarAddContact()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click(locales.In("en").CreateBotAction()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	second := a.selectedContact()
	secondID := second.ID
	openBotProfileFromRail(t, tr, locales.In("en").AvatarContactLabel(second.Name))
	if err := tr.Click(locales.In("en").FieldNameAccessible()); err != nil {
		t.Fatal(err)
	}
	tr.Key(ui.Cmd, ui.KeyA)
	tr.Type("Local contact")
	if err := clickBotProfileAction(tr, locales.In("en").ContactSaveChanges()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	editor := a.profileEditorFor(secondID)
	if second.Name != "Local contact" || editor.Error != "" {
		t.Fatalf("pre-connection profile save failed: name=%q editor=%+v", second.Name, editor)
	}
	editor.Draft.Name = "Intentionally unsaved editor"
	second.Draft = "Keep the second local draft"
	a.draftChanged(second)
	restarted, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.contacts) != 2 || restarted.selectedID() != secondID || restarted.contactByID(firstID) == nil || restarted.contactByID(firstID).Draft != "Keep the first local draft" || restarted.selectedContact().Name != "Local contact" || restarted.selectedContact().Draft != second.Draft {
		t.Fatalf("fresh app lost local IDs/text/selection or saved the editor draft: %+v", restarted.contacts)
	}
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	config := testConnectionConfig(t)
	config.Endpoint = provider.URL + "/v1"
	if err := saveConnectionConfig(connectionConfigPath(guiStorePath()), config); err != nil {
		t.Fatal(err)
	}
	first := restarted.contactByID(firstID)
	if first == nil || restarted.selectedID() != secondID || first.Draft != "Keep the first local draft" || restarted.selectedContact().Draft != second.Draft {
		t.Fatal("first connection Save replaced local state")
	}
	afterConnection, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if afterConnection.selectedID() != secondID || afterConnection.contactByID(firstID).Draft != "Keep the first local draft" || afterConnection.selectedContact().Name != "Local contact" {
		t.Fatal("connection Save/reopen lost local contact state")
	}
	for _, app := range []*appModel{a, restarted, afterConnection} {
		if app.pi != nil || app.providerBusy() {
			t.Fatal("local exploration or connection Save started Runtime")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("local exploration or connection Save called the provider")
	}
}

func TestLocalStateBeforeConnectionExplicitProfileSave(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	id := a.selectedID()
	a.closeSettings()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	openBotProfileFromRail(t, tr, locales.In("en").AvatarContactLabel(a.selectedContact().Name))
	if err := tr.Click(locales.In("en").FieldNameAccessible()); err != nil {
		t.Fatal(err)
	}
	tr.Key(ui.Cmd, ui.KeyA)
	tr.Type("Saved without appearance")
	tr.Frame()
	if _, err := os.Stat(a.storePath); !os.IsNotExist(err) {
		t.Fatal("unsaved profile-editor input unexpectedly wrote a store")
	}
	if err := clickBotProfileAction(tr, locales.In("en").ContactSaveChanges()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if editor := a.profileEditorFor(id); editor.Error != "" {
		t.Fatalf("explicit local profile Save was refused: %q", editor.Error)
	}
	restarted, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if restarted.selectedID() != id || restarted.selectedContact().Name != "Saved without appearance" || a.appearanceMode != "" {
		t.Fatal("explicit profile Save lost identity/name or required an appearance change")
	}
}

func TestContactManagementSaveUpdatesFutureProfileWithoutReplacingSession(t *testing.T) {
	a, bound, _ := piRunStartedTestApp(t)
	bound.ActiveTaskID, bound.SendState = "", sendIdle
	a.storePath, a.workDir = a.store.Path, filepath.Dir(a.store.Path)
	bound.Draft = "keep me"
	bound.Messages = []*message{{Kind: msgUser, Text: "old message"}}
	bound.History = []historyEntry{{ID: "history-kept", Text: "old message"}}
	sessionRef := bound.SessionID
	binding := clonePiConversationBinding(bound.PiBinding)
	workspace := t.TempDir()
	if _, err := bound.Context.AddSnippet("Keep", "keep context"); err != nil {
		t.Fatal(err)
	}
	before := len(bound.Messages)

	a.openContactProfile(bound.ID)
	if !a.saveContactProfileDraftFor(bound.ID, profileDraft{
		Name: "  Renamed  ", Purpose: "future purpose",
		Instructions: "line one\n  indented line", Workspace: workspace,
	}, false) {
		t.Fatalf("save failed: %+v", a.profileEditorFor(bound.ID))
	}
	if bound.Name != "Renamed" || bound.Purpose != "future purpose" || bound.Workspace != workspace {
		t.Fatalf("profile = %+v", bound)
	}
	if bound.Instructions != "line one\n  indented line" {
		t.Fatalf("instructions were rewritten: %q", bound.Instructions)
	}
	if bound.SessionID != sessionRef || !reflect.DeepEqual(bound.PiBinding, binding) || len(bound.Messages) != before || bound.Draft != "keep me" || len(bound.Context.Snapshot()) != 1 {
		t.Fatalf("session-owned state changed: %+v", bound)
	}
	reloaded, err := openContactStore(a.store.Path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	stored := reloaded.State.Contacts[0]
	if stored.Name != "Renamed" || stored.Purpose != "future purpose" || stored.Instructions != bound.Instructions || stored.SessionID != sessionRef || !reflect.DeepEqual(stored.PiBinding, binding) {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestContactManagementSaveFailureRollsBackFieldsAndKeepsDraft(t *testing.T) {
	a, bound, _ := piRunStartedTestApp(t)
	bound.ActiveTaskID, bound.SendState = "", sendIdle
	a.storePath, a.workDir = a.store.Path, filepath.Dir(a.store.Path)
	bound.Draft = "keep me"
	sessionRef := bound.SessionID
	binding := clonePiConversationBinding(bound.PiBinding)
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	oldPurpose := bound.Purpose
	oldWorkspace := bound.Workspace
	broken := t.TempDir()
	a.store.Path = broken
	workspace := t.TempDir()

	a.openContactProfile(bound.ID)
	draft := profileDraft{Name: "Cannot Save", Purpose: "new purpose", Workspace: workspace}
	if a.saveContactProfileDraftFor(bound.ID, draft, false) {
		t.Fatal("save unexpectedly succeeded with an unusable store")
	}
	if bound.Name != "Nagi" || bound.Purpose != oldPurpose || bound.Workspace != oldWorkspace {
		t.Fatalf("failed save changed the contact: %+v", bound)
	}
	editor := a.profileEditorFor(bound.ID)
	if editor == nil || editor.Error != profileNoticeErrorSave || editor.Draft.Name != "Cannot Save" {
		t.Fatalf("failed save did not report an error or preserve the draft: %+v", editor)
	}
	if editor.Status != "" {
		t.Fatal("failed save reported success")
	}
	if a.store.State.Contacts[0].Name != "Nagi" {
		t.Fatal("failed save left the store's in-memory snapshot changed")
	}
	if bound.Draft != "keep me" || bound.SessionID != sessionRef || !reflect.DeepEqual(bound.PiBinding, binding) {
		t.Fatalf("failed save changed conversation identity or draft: %+v", bound)
	}
}

func TestContactManagementArchiveRestorePersistsAndBlocksActiveWork(t *testing.T) {
	a, bound, _ := piRunStartedTestApp(t)
	a.storePath, a.workDir = a.store.Path, filepath.Dir(a.store.Path)
	sessionRef := bound.SessionID
	binding := clonePiConversationBinding(bound.PiBinding)
	bound.SendState = sendSubmitting
	a.openContactProfile(bound.ID)
	if a.archiveContact(bound.ID) {
		t.Fatal("archive unexpectedly succeeded with an active conversation")
	}
	if bound.Archived || a.contactArchiveBlocked(bound) == "" {
		t.Fatal("archive did not explain the active work")
	}
	bound.ActiveTaskID, bound.SendState = "", sendUnknown
	if a.archiveContact(bound.ID) || bound.Archived || a.contactArchiveBlocked(bound) != archiveNoticeUnknown {
		t.Fatal("archive did not preserve the conversation with unknown delivery")
	}

	bound.ActiveTaskID, bound.SendState = "", sendIdle
	if !a.archiveContact(bound.ID) {
		t.Fatalf("archive failed: %+v", a.profileEditorFor(bound.ID))
	}
	if !bound.Archived || bound.ArchivedAt == 0 {
		t.Fatal("contact was not archived")
	}
	if !a.profileEditorFor(bound.ID).Archived {
		t.Fatal("archive did not update the visible read-only state")
	}
	reloaded, err := openContactStore(a.store.Path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.State.Contacts[0].Archived || reloaded.State.Contacts[0].ArchivedAt == 0 || reloaded.State.Contacts[0].SessionID != sessionRef || !reflect.DeepEqual(reloaded.State.Contacts[0].PiBinding, binding) {
		t.Fatal("archive was not persisted")
	}
	if reason := a.canSend(bound); !strings.Contains(reason, "archived") {
		t.Fatalf("send guard = %q", reason)
	}
	if !a.restoreContact(bound.ID) || bound.Archived || bound.ArchivedAt != 0 {
		t.Fatalf("restore failed: %+v", bound)
	}
	if bound.SessionID != sessionRef || !reflect.DeepEqual(bound.PiBinding, binding) {
		t.Fatal("archive and restore changed the saved conversation binding")
	}
	reloaded, err = openContactStore(a.store.Path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	stored := reloaded.State.Contacts[0]
	if stored.Archived || stored.ArchivedAt != 0 || stored.SessionID != sessionRef || !reflect.DeepEqual(stored.PiBinding, binding) {
		t.Fatal("restore was not persisted with the original conversation binding")
	}
}

func TestContactManagementSidebarSearchKeepsStableIndices(t *testing.T) {
	a, bound, _ := piRunStartedTestApp(t)
	bound.ActiveTaskID, bound.SendState = "", sendIdle
	a.storePath, a.workDir = a.store.Path, filepath.Dir(a.store.Path)
	bound.Purpose = "old purpose"
	unbound := &contact{ID: "new", Name: "New contact 1", Role: "Assistant", Workspace: a.workDir, Status: statusReady}
	a.contacts = append(a.contacts, unbound)
	archive := &contact{ID: "archive", Name: "Archive", Purpose: "old budget notes", Archived: true, Status: statusReady}
	a.contacts = append(a.contacts, archive)
	a.contactSearch = "purpose"
	if got := a.visibleContactIndices(); len(got) != 1 || a.contacts[got[0]] != bound {
		t.Fatalf("name/purpose search = %v", got)
	}
	a.contactSearch = "budget"
	if got := a.visibleContactIndices(); len(got) != 1 || a.contacts[got[0]] != archive {
		t.Fatalf("archived search = %v", got)
	}
	a.contactSearch = "new contact"
	if got := a.visibleContactIndices(); len(got) != 1 || a.contacts[got[0]] != unbound {
		t.Fatalf("contact search = %v", got)
	}
}

func TestContactManagementNonDemoWithoutStoreNeverClaimsPersistence(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki", Workspace: t.TempDir(), Status: statusReady}
	a := &appModel{contacts: []*contact{ct}, selected: 0}
	a.openContactProfile(ct.ID)
	if a.saveContactProfileDraftFor(ct.ID, profileDraft{Name: "Renamed", Workspace: ct.Workspace}, false) {
		t.Fatal("save without a contact store claimed success")
	}
	if editor := a.profileEditorFor(ct.ID); ct.Name != "Saki" || editor == nil || editor.Error == "" {
		t.Fatalf("save without a store mutated or failed to report: ct=%+v editor=%+v", ct, editor)
	}
	ct.Archived = true
	if a.restoreContact(ct.ID) || !ct.Archived {
		t.Fatal("restore without a contact store claimed success")
	}
	ct.Archived = false
	if a.archiveContact(ct.ID) || ct.Archived {
		t.Fatal("archive without a contact store claimed success")
	}
}

func TestContactManagementShutdownWaitsForHistoryCallback(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki"}
	quit := false
	a := &appModel{
		contacts: []*contact{ct}, selected: 0, shutdownStarted: true,
		contactHistoryInFlight: 1, requestQuit: func() { quit = true },
	}
	a.finishShutdown()
	if a.quitReady || quit {
		t.Fatal("shutdown closed the UI while a history callback was still in flight")
	}
	a.historyAsyncFinished()
	if !a.quitReady || !quit {
		t.Fatal("the final history callback did not finish shutdown")
	}
}

func TestSelectContactPersistsTheNewSelectionImmediately(t *testing.T) {
	a, bound, _ := piRunStartedTestApp(t)
	bound.ActiveTaskID, bound.SendState = "", sendIdle
	a.storePath, a.workDir = a.store.Path, filepath.Dir(a.store.Path)
	unbound := &contact{ID: "new", Name: "New contact 1", Role: "Assistant", Workspace: a.workDir, Status: statusReady}
	a.contacts = append(a.contacts, unbound)
	a.selectContact(1)
	reloaded, err := openContactStore(a.store.Path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Selected != unbound.ID || a.selectedID() != unbound.ID {
		t.Fatalf("selected=%q store=%q want=%q", a.selectedID(), reloaded.State.Selected, unbound.ID)
	}
}

func TestContactsFromStoreKeepsEmptyWorkspaceAsInheritedDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, "/fallback")
	if err != nil {
		t.Fatal(err)
	}
	store.State.Contacts[0].Workspace = ""
	if err := store.SaveState(); err != nil {
		t.Fatal(err)
	}
	contacts := contactsFromStore(store)
	if len(contacts) != 1 || contacts[0].Workspace != "" {
		t.Fatalf("empty workspace was materialized on load: %+v", contacts)
	}
	a := &appModel{store: store, contacts: contacts, selected: 0, workDir: "/applied"}
	if got := a.effectiveContactWorkspace(contacts[0]); got != "/applied" {
		t.Fatalf("effective workspace = %q", got)
	}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(path, "/other-fallback")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Contacts[0].Workspace != "" {
		t.Fatalf("empty workspace was persisted as %q", reloaded.State.Contacts[0].Workspace)
	}
}

func TestContactManagementAppPreviews(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	longPurpose := strings.Repeat("Keep the product decisions and evidence in view for future conversations. ", 3)
	longInstructions := "  Preserve this indentation.\n" + strings.Repeat("\tCheck the actual Runtime state before claiming success.\n", 4)
	for _, dark := range []bool{false, true} {
		for _, width := range []int{720, 1280} {
			a := testApp(t)
			a.windowWidth = width
			a.openContactProfile(a.selectedID())
			editor := a.profileEditorFor(a.selectedID())
			editor.Draft.Name = "Research partner with a long name"
			editor.Draft.Purpose = longPurpose
			editor.Draft.Instructions = longInstructions
			editor.Draft.Workspace = t.TempDir()
			tr := ui.NewTester(a.view, width, 720)
			tr.SetDark(dark)
			tr.Frame()
			if !tr.HasText(locales.In("en").ContactProfileAction()) || !tr.HasText(locales.In("en").ContactSaveChanges()) {
				t.Fatalf("Bot profile preview missing content at width %d", width)
			}
			theme := "light"
			if dark {
				theme = "dark"
			}
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("app-settings-%s-%d.png", theme, width)), tr.Image())
		}
	}

	archived := testApp(t)
	archived.selectedContact().Archived = true
	archived.windowWidth = 1280
	archived.openContactProfile(archived.selectedID())
	tr := ui.NewTester(archived.view, 1280, 720)
	tr.Frame()
	if !tr.HasText("Archived") || !tr.HasText("Restore") {
		t.Fatal("archived settings preview is missing the archived state or Restore")
	}
	writePreviewPNG(t, filepath.Join(dir, "app-archive-restore-1280.png"), tr.Image())

	if !archived.restoreContact(archived.selectedContact().ID) {
		t.Fatal("archived preview could not restore the contact")
	}
	tr = ui.NewTester(archived.view, 1280, 720)
	tr.Frame()
	if !tr.HasText(locales.In("en").FieldNameAccessible()) || archived.selectedContact().Archived {
		t.Fatal("restored preview did not return to the editable app")
	}
	writePreviewPNG(t, filepath.Join(dir, "app-archive-restored-1280.png"), tr.Image())

	empty := testApp(t)
	empty.contactSearch = "no-such-contact"
	tr = ui.NewTester(empty.view, 1280, 720)
	tr.Frame()
	if !tr.HasText(locales.In("en").RailNoContactsMatch()) {
		t.Fatal("empty filter preview does not explain the result")
	}
	writePreviewPNG(t, filepath.Join(dir, "app-empty-filter-1280.png"), tr.Image())

	conversation := testApp(t)
	ct := conversation.selectedContact()
	ct.Messages = []*message{{
		Key: "retained-message", Kind: msgUser, Text: "The retained message with its submitted context.",
		Context: []taskContextItem{{ID: "ctx-1", Name: "Retained input", Text: "old context", Revision: 1}},
	}}
	tr = ui.NewTester(conversation.view, 1280, 720)
	tr.Frame()
	if !tr.HasText("The retained message with its submitted context.") {
		t.Fatal("chat preview did not render the retained message")
	}
	writePreviewPNG(t, filepath.Join(dir, "app-chat-retained-context-1280.png"), tr.Image())
}
