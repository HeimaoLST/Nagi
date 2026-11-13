package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestUnconfiguredContactWorkspacesFollowFirstConnection(t *testing.T) {
	launchFolder, connectionFolder, contactFolder := t.TempDir(), t.TempDir(), t.TempDir()
	isolateConnectionApp(t, launchFolder, filepath.Join(launchFolder, "unused-runtime"))
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	firstID := a.selectedID()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	focusComposer(t, tr, "Composer for Saki")
	tr.Type("Keep the launch-folder draft")
	tr.Frame()
	if err := tr.Click("Add contact"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	second := a.selectedContact()
	secondID := second.ID
	second.Draft = "Keep the added-contact draft"
	a.draftChanged(second)
	draft := contactProfileDraft(second)
	draft.Name = "Inherited folder contact"
	if !a.saveContactProfileDraft(draft, false) {
		t.Fatal("local profile Save failed before connection")
	}
	a.addRuntimeContact()
	explicitID := a.selectedID()
	if !a.saveContactProfileDraft(profileDraft{Name: "Explicit folder contact", Workspace: contactFolder}, false) {
		t.Fatal("explicit contact folder Save failed before connection")
	}
	beforeConnection, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if beforeConnection.effectiveContactWorkspace(beforeConnection.contactByID(firstID)) != launchFolder {
		t.Fatal("local display lost its launch-folder fallback")
	}
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	config := testConnectionConfig(t)
	config.WorkingDirectory = connectionFolder
	config.Endpoint = provider.URL + "/v1"
	pump := newUIPump(t, beforeConnection)
	defer pump.close()
	pump.do(func() { beforeConnection.saveConnection(config) })
	pump.wait(func() bool { return !beforeConnection.connectionSaveInFlight })
	var connectionError string
	pump.do(func() { connectionError = beforeConnection.connectionForm.Error })
	if connectionError != "" {
		t.Fatal(connectionError)
	}
	reopened, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.contacts) != 3 || reopened.selectedID() != explicitID || reopened.contactByID(firstID) == nil || reopened.contactByID(secondID) == nil || reopened.contactByID(explicitID) == nil {
		t.Fatal("connection Save/reopen changed local IDs or selection")
	}
	if reopened.contactByID(firstID).Draft != "Keep the launch-folder draft" || reopened.contactByID(secondID).Draft != "Keep the added-contact draft" || reopened.contactByID(secondID).Name != "Inherited folder contact" {
		t.Fatal("connection Save/reopen changed local text")
	}
	for _, id := range []string{firstID, secondID} {
		ct := reopened.contactByID(id)
		if got := reopened.effectiveContactWorkspace(ct); ct.Workspace != "" || got != connectionFolder {
			t.Errorf("unconfirmed contact %q pinned launch folder: stored=%q effective=%q; want inherited %q", id, ct.Workspace, got, connectionFolder)
		}
	}
	if ct := reopened.contactByID(explicitID); ct.Workspace != contactFolder || reopened.effectiveContactWorkspace(ct) != contactFolder {
		t.Fatal("explicit contact folder C was replaced by connection folder B")
	}
	for _, app := range []*appModel{a, beforeConnection, reopened} {
		if app.rt != nil || app.runtimeHandshake || app.runtimeHelperChecked || app.runtimeBinary != "" {
			t.Fatal("local workspace selection started Runtime")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("local workspace selection called the provider")
	}
}

func TestContactWorkspaceDefaultsRespectConnectionSource(t *testing.T) {
	for _, source := range []string{"managed", "legacy", "invalid", "demo"} {
		t.Run(source, func(t *testing.T) {
			launchFolder := t.TempDir()
			isolateConnectionApp(t, launchFolder, filepath.Join(launchFolder, "unused-runtime"))
			t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", "")
			want := launchFolder
			var a *appModel
			if source == "demo" {
				a = &appModel{demo: true, workDir: launchFolder, contacts: []*contact{{ID: "demo-contact", Name: "Saki"}}, scrollTo: -1}
			} else {
				if source == "legacy" || source == "invalid" {
					t.Setenv("API_KEY", "offline-fixture")
				}
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
			if a.rt != nil || a.runtimeHelperChecked {
				t.Fatal("contact defaults started Runtime")
			}
		})
	}
}

func TestLocalStateBeforeConnectionCreateSaveAndRestart(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, filepath.Join(root, "unused-runtime"))
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", "")
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
	focusComposer(t, tr, "Composer for Saki")
	tr.Type("Keep the first local draft")
	tr.Frame()
	if err := tr.Click("Add contact"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	second := a.selectedContact()
	secondID := second.ID
	if err := tr.Click("Contact profile"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Edit full profile"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Contact name"); err != nil {
		t.Fatal(err)
	}
	tr.Key(ui.Cmd, ui.KeyA)
	tr.Type("Local contact")
	if err := clickSettingsAction(tr, "Save changes"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if second.Name != "Local contact" || a.settingsContactEditor().Error != "" {
		t.Fatalf("pre-connection profile save failed: name=%q editor=%+v", second.Name, a.settingsContactEditor())
	}
	a.settingsContactEditor().Draft.Name = "Intentionally unsaved editor"
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
	pump := newUIPump(t, restarted)
	defer pump.close()
	pump.do(func() { restarted.saveConnection(config) })
	pump.wait(func() bool { return !restarted.connectionSaveInFlight })
	var connectionError string
	var stateRetained bool
	pump.do(func() {
		connectionError = restarted.connectionForm.Error
		first := restarted.contactByID(firstID)
		stateRetained = first != nil && restarted.selectedID() == secondID && first.Draft == "Keep the first local draft" && restarted.selectedContact().Draft == second.Draft
	})
	if connectionError != "" || !stateRetained {
		t.Fatalf("first connection Save replaced local state: error=%q", connectionError)
	}
	afterConnection, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if afterConnection.selectedID() != secondID || afterConnection.contactByID(firstID).Draft != "Keep the first local draft" || afterConnection.selectedContact().Name != "Local contact" {
		t.Fatal("connection Save/reopen lost local contact state")
	}
	for _, app := range []*appModel{a, restarted, afterConnection} {
		if app.rt != nil || app.runtimeHandshake || app.runtimeHelperChecked || app.runtimeBinary != "" {
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
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	id := a.selectedID()
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Contact name"); err != nil {
		t.Fatal(err)
	}
	tr.Key(ui.Cmd, ui.KeyA)
	tr.Type("Saved without appearance")
	tr.Frame()
	if _, err := os.Stat(a.storePath); !os.IsNotExist(err) {
		t.Fatal("unsaved profile-editor input unexpectedly wrote a store")
	}
	if err := clickSettingsAction(tr, "Save changes"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.settingsContactEditor().Error != "" {
		t.Fatalf("explicit local profile Save was refused: %q", a.settingsContactEditor().Error)
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
	a, bound, _ := testRuntimeModel(t)
	workspace := t.TempDir()
	if _, err := bound.Context.AddSnippet("Keep", "keep context"); err != nil {
		t.Fatal(err)
	}
	before := len(bound.Messages)

	a.openSettings()
	if !a.saveContactProfileDraft(profileDraft{
		Name: "  Renamed  ", Purpose: "future purpose",
		Instructions: "line one\n  indented line", Workspace: workspace,
	}, false) {
		t.Fatalf("save failed: %+v", a.contactProfile)
	}
	if bound.Name != "Renamed" || bound.Purpose != "future purpose" || bound.Workspace != workspace {
		t.Fatalf("profile = %+v", bound)
	}
	if bound.Instructions != "line one\n  indented line" {
		t.Fatalf("instructions were rewritten: %q", bound.Instructions)
	}
	if bound.SessionID != "s1" || len(bound.Messages) != before || bound.Draft != "keep me" || len(bound.Context.snapshot()) != 1 {
		t.Fatalf("session-owned state changed: %+v", bound)
	}
	reloaded, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	stored := reloaded.state.Contacts[0]
	if stored.Name != "Renamed" || stored.Purpose != "future purpose" || stored.Instructions != bound.Instructions || stored.SessionID != "s1" {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestContactManagementInstructionsKeepOriginalWhitespaceInPrompt(t *testing.T) {
	ct := &contact{Name: "Saki", Instructions: "  Keep indentation\n\tand this line exactly.  "}
	prompt := profileSystemPrompt(ct)
	if !strings.Contains(prompt, "Instructions: "+ct.Instructions) {
		t.Fatalf("profile prompt rewrote instructions: %q", prompt)
	}
}

func TestContactManagementSaveAndNewDetachesOnlyCurrentConversation(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	workspace := t.TempDir()
	if _, err := bound.Context.AddSnippet("Keep", "unsent context"); err != nil {
		t.Fatal(err)
	}
	issue := &productIssue{ID: "issue-1", ContactID: bound.ID, Goal: "keep issue", WorkSessionID: "issue-session"}
	bound.Issues = []*productIssue{issue}
	bound.SelectedWorkTaskID = "old-work"

	a.openSettings()
	if !a.saveContactProfileDraft(profileDraft{Name: "Renamed", Purpose: "new purpose", Workspace: workspace}, true) {
		t.Fatalf("save and new failed: %+v", a.contactProfile)
	}
	if bound.SessionID != "" || len(bound.Messages) != 0 || len(bound.History) != 0 {
		t.Fatalf("current conversation was not detached: %+v", bound)
	}
	if len(bound.PreviousSessions) != 1 || bound.PreviousSessions[0] != "s1" {
		t.Fatalf("old session reference = %+v", bound.PreviousSessions)
	}
	if bound.Draft != "keep me" || len(bound.Context.snapshot()) != 1 {
		t.Fatal("save and new consumed the unsent draft or context")
	}
	if len(bound.Issues) != 1 || bound.Issues[0].WorkSessionID != "issue-session" || bound.Issues[0].Goal != "keep issue" {
		t.Fatalf("issues changed: %+v", bound.Issues)
	}
	if bound.SelectedWorkTaskID != "" || bound.SendState != sendIdle || bound.Status != statusReady {
		t.Fatalf("stale conversation state kept: %+v", bound)
	}
	reloaded, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	stored := reloaded.state.Contacts[0]
	if stored.SessionID != "" || len(stored.PreviousSessions) != 1 || stored.PreviousSessions[0] != "s1" {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestContactManagementSaveFailureRollsBackFieldsAndKeepsDraft(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	oldPurpose := bound.Purpose
	oldWorkspace := bound.Workspace
	broken := t.TempDir()
	a.store.path = broken // rename onto a directory must fail after the in-memory snapshot changes
	workspace := t.TempDir()

	a.openSettings()
	draft := profileDraft{Name: "Cannot Save", Purpose: "new purpose", Workspace: workspace}
	if a.saveContactProfileDraft(draft, false) {
		t.Fatal("save unexpectedly succeeded with an unusable store")
	}
	if bound.Name != "Saki" || bound.Purpose != oldPurpose || bound.Workspace != oldWorkspace {
		t.Fatalf("failed save changed the contact: %+v", bound)
	}
	if a.contactProfile == nil || a.contactProfile.Error == "" || a.contactProfile.Draft.Name != "Cannot Save" {
		t.Fatalf("failed save did not report an error or preserve the draft: %+v", a.contactProfile)
	}
	if a.contactProfile.Status != "" {
		t.Fatal("failed save reported success")
	}
	if a.store.state.Contacts[0].Name != "Saki" {
		t.Fatal("failed save left the store's in-memory snapshot changed")
	}
}

func TestContactManagementSaveAndNewFailureKeepsOldSessionReference(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	broken := t.TempDir()
	a.store.path = broken
	workspace := t.TempDir()
	a.openSettings()
	if a.saveContactProfileDraft(profileDraft{Name: "Renamed", Workspace: workspace}, true) {
		t.Fatal("save and new unexpectedly succeeded")
	}
	if bound.SessionID != "s1" || len(bound.PreviousSessions) != 0 || len(bound.Messages) != 1 {
		t.Fatalf("failed save and new changed the current conversation: %+v", bound)
	}
}

func TestContactManagementArchiveRestorePersistsAndBlocksActiveWork(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	bound.SendState = sendSubmitting
	a.openSettings()
	if a.archiveContact(bound.ID) {
		t.Fatal("archive unexpectedly succeeded with a loaded conversation")
	}
	if bound.Archived || a.contactArchiveBlocked(bound) == "" {
		t.Fatal("archive did not explain the active work")
	}

	bound.SessionID = ""
	bound.Loaded = false
	bound.SendState = sendIdle
	if !a.archiveContact(bound.ID) {
		t.Fatalf("archive failed: %+v", a.contactProfile)
	}
	if !bound.Archived || bound.ArchivedAt == 0 {
		t.Fatal("contact was not archived")
	}
	if !a.contactProfile.Archived || a.contactScope != "archived" {
		t.Fatal("archive did not switch the visible read-only state")
	}
	reloaded, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.state.Contacts[0].Archived || reloaded.state.Contacts[0].ArchivedAt == 0 {
		t.Fatal("archive was not persisted")
	}
	if reason := a.canSend(bound); !strings.Contains(reason, "archived") {
		t.Fatalf("send guard = %q", reason)
	}
	if !a.restoreContact(bound.ID) || bound.Archived || bound.ArchivedAt != 0 {
		t.Fatalf("restore failed: %+v", bound)
	}
	if a.contactScope != "active" {
		t.Fatal("restore did not return to the active scope")
	}
}

func TestContactManagementSidebarScopeAndSearchKeepStableIndices(t *testing.T) {
	a, bound, unbound := testRuntimeModel(t)
	archive := &contact{ID: "archive", Name: "Archive", Purpose: "old budget notes", Archived: true, Status: statusReady}
	a.contacts = append(a.contacts, archive)
	a.contactScope = "active"
	a.contactSearch = "purpose"
	if got := a.visibleContactIndices(); len(got) != 1 || a.contacts[got[0]] != bound {
		t.Fatalf("active search = %v", got)
	}
	a.contactSearch = "budget"
	if got := a.visibleContactIndices(); len(got) != 0 {
		t.Fatalf("archived contact leaked into active search: %v", got)
	}
	a.setContactScope("archived")
	if got := a.visibleContactIndices(); len(got) != 1 || a.contacts[got[0]] != archive {
		t.Fatalf("archived search = %v", got)
	}
	a.contactScope = "all"
	a.contactSearch = "new contact"
	if got := a.visibleContactIndices(); len(got) != 1 || a.contacts[got[0]] != unbound {
		t.Fatalf("all search = %v", got)
	}
}

func TestContactStoreReadsVersionSixWithoutNewFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contacts.json")
	legacy := `{"version":6,"selected":"c1","contacts":[{"id":"c1","name":"Old","session_id":"s1"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if store.state.Version != 6 || len(store.state.Contacts) != 1 {
		t.Fatalf("state = %+v", store.state)
	}
	ct := store.state.Contacts[0]
	if ct.Archived || len(ct.PreviousSessions) != 0 || ct.SessionID != "s1" {
		t.Fatalf("legacy contact = %+v", ct)
	}
}

func TestContactManagementNonDemoWithoutStoreNeverClaimsPersistence(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki", Workspace: t.TempDir(), Status: statusReady}
	a := &appModel{contacts: []*contact{ct}, selected: 0, scrollTo: -1}
	a.openSettings()
	if a.saveContactProfileDraft(profileDraft{Name: "Renamed", Workspace: ct.Workspace}, false) {
		t.Fatal("save without a contact store claimed success")
	}
	if ct.Name != "Saki" || a.contactProfile == nil || a.contactProfile.Error == "" {
		t.Fatalf("save without a store mutated or failed to report: ct=%+v editor=%+v", ct, a.contactProfile)
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
	a, _, unbound := testRuntimeModel(t)
	a.selectContact(1)
	reloaded, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Selected != unbound.ID || a.selectedID() != unbound.ID {
		t.Fatalf("selected=%q store=%q want=%q", a.selectedID(), reloaded.state.Selected, unbound.ID)
	}
}

func TestContactsFromStoreKeepsEmptyWorkspaceAsInheritedDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, "/fallback")
	if err != nil {
		t.Fatal(err)
	}
	store.state.Contacts[0].Workspace = ""
	if err := store.saveState(); err != nil {
		t.Fatal(err)
	}
	contacts := contactsFromStore(store)
	if len(contacts) != 1 || contacts[0].Workspace != "" {
		t.Fatalf("empty workspace was materialized on load: %+v", contacts)
	}
	a := &appModel{store: store, contacts: contacts, selected: 0, workDir: "/applied", scrollTo: -1}
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
	if reloaded.state.Contacts[0].Workspace != "" {
		t.Fatalf("empty workspace was persisted as %q", reloaded.state.Contacts[0].Workspace)
	}
}

func TestContactManagementAppPreviews(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
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
			a.openSettings()
			a.contactProfile.Draft.Name = "Research partner with a long name"
			a.contactProfile.Draft.Purpose = longPurpose
			a.contactProfile.Draft.Instructions = longInstructions
			a.contactProfile.Draft.Workspace = t.TempDir()
			tr := ui.NewTester(a.view, width, 720)
			tr.SetDark(dark)
			tr.Frame()
			if !tr.HasText("Contact profile") || !tr.HasText("Save changes") {
				t.Fatalf("settings preview missing content at width %d", width)
			}
			theme := "light"
			if dark {
				theme = "dark"
			}
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("app-settings-%s-%d.png", theme, width)), tr.Image())
		}
	}

	archived := testApp(t)
	archived.contactScope = "archived"
	archived.selectedContact().Archived = true
	archived.openSettings()
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
	if !tr.HasText("Settings") || archived.selectedContact().Archived {
		t.Fatal("restored preview did not return to the editable app")
	}
	writePreviewPNG(t, filepath.Join(dir, "app-archive-restored-1280.png"), tr.Image())

	empty := testApp(t)
	empty.contactScope = "archived"
	empty.contactSearch = "no-such-contact"
	tr = ui.NewTester(empty.view, 1280, 720)
	tr.Frame()
	if !tr.HasText("No contacts match this scope and search.") {
		t.Fatal("empty filter preview does not explain the result")
	}
	writePreviewPNG(t, filepath.Join(dir, "app-empty-filter-1280.png"), tr.Image())

	history := testApp(t)
	history.demo = false
	history.runtimeStatus = runtimeStateReady
	history.selectedContact().SessionID = "saved-session"
	history.selectedContact().Loaded = true
	history.openHistory()
	history.contactHistoryFull = &contactHistoryFull{
		SessionID: "saved-session", EntryID: "entry-1", Date: "Oct 1", From: "me",
		Text:    "The retained message with its submitted context.",
		Context: []taskContextItem{{ID: "ctx-1", Name: "Retained input", Text: "old context", Revision: 1}},
	}
	tr = ui.NewTester(history.view, 1280, 720)
	tr.Frame()
	if !tr.HasText("Full saved message") || !tr.HasText("Retained input") {
		t.Fatal("history preview did not render the full message and Context")
	}
	writePreviewPNG(t, filepath.Join(dir, "app-history-full-context-1280.png"), tr.Image())
}
