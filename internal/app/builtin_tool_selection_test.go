package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func builtinToolsForTest() []runtimeBuiltinTool {
	return []runtimeBuiltinTool{
		{Name: "bash", Description: "Run a command."},
		{Name: "edit", Description: "Edit an existing file."},
		{Name: "read", Description: "Read a file."},
		{Name: "write", Description: "Write a file."},
	}
}

func storedBuiltinToolsForTest(t *testing.T, path string) (present bool, raw string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Contacts []map[string]json.RawMessage `json:"contacts"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Contacts) != 1 {
		t.Fatalf("contacts = %d", len(state.Contacts))
	}
	value, ok := state.Contacts[0]["builtin_tools"]
	return ok, string(value)
}

func freshBuiltinProfileTestApp(t *testing.T) (*appModel, *contact) {
	t.Helper()
	root := t.TempDir()
	store, err := openContactStore(filepath.Join(root, "contacts.json"), root)
	if err != nil {
		t.Fatal(err)
	}
	syntheticBuiltinDefaults := []string{"bash", "edit", "read", "write"}
	a := &appModel{
		storePath: store.Path, workDir: root, windowWidth: 1040,
		connectionManaged: true, runtimeStatus: runtimeStateReady,
		runtimeBuiltinTools: builtinToolsForTest(), runtimeBuiltinToolsLoaded: true,
		runtimeBuiltinDefaults: syntheticBuiltinDefaults,
	}
	applyStoreSelection(a, store)
	a.bindTaskContexts()
	return a, a.selectedContact()
}

func TestContactStorePreservesDefaultAndExplicitEmptyBuiltinTools(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "contacts.json")
	store, err := openContactStore(path, root)
	if err != nil {
		t.Fatal(err)
	}
	ct := contactsFromStore(store)[0]
	if ct.BuiltinTools != nil {
		t.Fatalf("missing field did not use Runtime default: %v", ct.BuiltinTools)
	}
	if err := saveContacts(store, []*contact{ct}, ct.ID); err != nil {
		t.Fatal(err)
	}
	if present, raw := storedBuiltinToolsForTest(t, path); present {
		t.Fatalf("default save wrote explicit built-ins: %s", raw)
	}

	empty := []string{}
	ct.BuiltinTools = &empty
	if err := saveContacts(store, []*contact{ct}, ct.ID); err != nil {
		t.Fatal(err)
	}
	if present, raw := storedBuiltinToolsForTest(t, path); !present || strings.TrimSpace(raw) != "[]" {
		t.Fatalf("explicit empty store shape = present=%v raw=%q", present, raw)
	}
	reloaded, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	got := contactsFromStore(reloaded)[0].BuiltinTools
	if got == nil || len(*got) != 0 {
		t.Fatalf("reloaded explicit empty = %#v", got)
	}
}

func TestContactProfileBuiltinSelectionIsPerContact(t *testing.T) {
	a, first := freshBuiltinProfileTestApp(t)
	second := &contact{ID: "second", Name: "Mio", Role: "Assistant"}
	read := []string{"read"}
	second.BuiltinTools = &read
	a.contacts = append(a.contacts, second)
	a.openContactProfile(first.ID)
	editor := a.profileEditorFor(first.ID)
	if editor == nil {
		t.Fatal("first contact profile editor is unavailable")
	}
	empty := []string{}
	if !a.saveContactProfileDraftFor(first.ID, profileDraft{Name: first.Name, BuiltinTools: &empty}, false) {
		t.Fatalf("save first = %q; local=%q", editor.Error, a.localSaveErr)
	}
	if second.BuiltinTools == nil || len(*second.BuiltinTools) != 1 || (*second.BuiltinTools)[0] != "read" {
		t.Fatalf("saving first changed second: %v", second.BuiltinTools)
	}
	if first.BuiltinTools == nil || len(*first.BuiltinTools) != 0 {
		t.Fatalf("first selection = %#v", first.BuiltinTools)
	}
}

func TestProfileSavePreservesLegacyWorkspaceAndRetiredTools(t *testing.T) {
	a, ct := freshBuiltinProfileTestApp(t)
	a.connectionManaged = false
	a.runtimeBuiltinToolsLoaded = false
	retired := []string{"retired_tool", "read"}
	ct.Workspace = filepath.Join(t.TempDir(), "missing-legacy-folder")
	ct.BuiltinTools = &retired
	a.openContactProfile(ct.ID)
	draft := contactProfileDraft(ct)
	draft.Name = "Updated Name"
	draft.Purpose = "Updated Purpose"
	if !a.saveContactProfileDraftFor(ct.ID, draft, false) {
		t.Fatalf("editing name/purpose rejected preserved legacy workspace/tools: %+v", a.profileEditorFor(ct.ID))
	}
	if ct.Name != "Updated Name" || ct.Purpose != "Updated Purpose" || ct.Workspace != draft.Workspace {
		t.Fatalf("preserved workspace lost: %+v", ct)
	}
	if ct.BuiltinTools == nil || len(*ct.BuiltinTools) != 2 || (*ct.BuiltinTools)[0] != "retired_tool" || (*ct.BuiltinTools)[1] != "read" {
		t.Fatalf("preserved tools lost: %v", ct.BuiltinTools)
	}
}

func TestFirstUseBuiltinSelectionRequiresManagedConnection(t *testing.T) {
	a, ct := freshBuiltinProfileTestApp(t)
	a.connectionManaged = false
	a.openContactProfile(ct.ID)
	editor := a.profileEditorFor(ct.ID)
	if editor == nil {
		t.Fatal("first-use contact profile editor is unavailable")
	}
	read := []string{"read"}
	if a.saveContactProfileDraftFor(ct.ID, profileDraft{Name: ct.Name, BuiltinTools: &read}, false) {
		t.Fatal("legacy connection accepted an explicit built-in selection")
	}
	if editor.Error != builtinNoticeManaged {
		t.Fatalf("first-use selection rejected for the wrong reason: %q", editor.Error)
	}
	if ct.BuiltinTools != nil {
		t.Fatalf("legacy rejection changed profile: %v", ct.BuiltinTools)
	}
}
