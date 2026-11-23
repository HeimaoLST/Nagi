package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
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

func clickProfileAction(t *testing.T, tr *ui.Tester, label string) {
	t.Helper()
	if err := clickBotProfileAction(tr, label); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
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

// This regression is expressed through the actual profile UI, not by assigning
// the draft field. The old UI fails because it has no built-in-tool entry.
func TestContactProfileShowsBuiltinToolSelection(t *testing.T) {
	a, ct := freshBuiltinProfileTestApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	openBotProfileFromRail(t, tr, locales.In("en").AvatarContactLabel(ct.Name))
	editor := a.profileEditorFor(ct.ID)
	if !tr.HasText("Built-in tools") || !tr.HasText("Use host defaults (bash, edit, read, write)") {
		t.Fatalf("contact profile has no built-in tool selection entry: %v", tr.Texts())
	}
	if ct.BuiltinTools != nil {
		t.Fatalf("default contact unexpectedly has an explicit selection: %v", ct.BuiltinTools)
	}

	// Save the omitted/default value: the store must keep the field absent.
	clickProfileAction(t, tr, "Save changes")
	if editor.Error != "" || editor.Status != profileNoticeSavedDefaults || a.localSaveErr != "" {
		t.Fatalf("default profile save failed: editor=%+v local=%q", editor, a.localSaveErr)
	}
	if ct.BuiltinTools != nil {
		t.Fatalf("saving default changed it into an explicit selection: %v", ct.BuiltinTools)
	}
	if present, raw := storedBuiltinToolsForTest(t, a.store.Path); present {
		t.Fatalf("default selection was persisted as explicit %s", raw)
	}

	// Turn the host default into an explicit subset through real checkbox hits.
	clickProfileAction(t, tr, "Use host defaults (bash, edit, read, write)")
	for _, name := range []string{"bash", "edit", "write"} {
		clickProfileAction(t, tr, name)
	}
	if got := editor.Draft.BuiltinTools; got == nil || len(*got) != 1 || (*got)[0] != "read" {
		t.Fatalf("UI subset draft = %v", got)
	}
	clickProfileAction(t, tr, "Save changes")
	if editor.Error != "" || editor.Status != profileNoticeSavedDefaults || a.localSaveErr != "" {
		t.Fatalf("subset profile save failed: editor=%+v local=%q", editor, a.localSaveErr)
	}
	if ct.BuiltinTools == nil || len(*ct.BuiltinTools) != 1 || (*ct.BuiltinTools)[0] != "read" {
		t.Fatalf("saved subset = %v", ct.BuiltinTools)
	}
	present, raw := storedBuiltinToolsForTest(t, a.store.Path)
	if !present {
		t.Fatal("explicit subset store field is missing")
	}
	var subset []string
	if err := json.Unmarshal([]byte(raw), &subset); err != nil {
		t.Fatalf("explicit subset is not a string array: raw=%q err=%v", raw, err)
	}
	if subset == nil || len(subset) != 1 || subset[0] != "read" {
		t.Fatalf("explicit subset store shape = %#v", subset)
	}

	// Empty the explicit subset. This is not the host default and must persist
	// as a non-nil empty slice.
	clickProfileAction(t, tr, "read")
	if got := editor.Draft.BuiltinTools; got == nil || len(*got) != 0 {
		t.Fatalf("UI empty draft = %v", got)
	}
	clickProfileAction(t, tr, "Save changes")
	if editor.Error != "" || editor.Status != profileNoticeSavedDefaults || a.localSaveErr != "" {
		t.Fatalf("empty profile save failed: editor=%+v local=%q", editor, a.localSaveErr)
	}
	if ct.BuiltinTools == nil || len(*ct.BuiltinTools) != 0 {
		t.Fatalf("saved empty selection = %v", ct.BuiltinTools)
	}
	if present, raw := storedBuiltinToolsForTest(t, a.store.Path); !present || strings.TrimSpace(raw) != "[]" {
		t.Fatalf("explicit empty selection persisted as present=%v raw=%q", present, raw)
	}
	reloaded, err := openContactStore(a.store.Path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.State.Contacts) != 1 || reloaded.State.Contacts[0].BuiltinTools == nil || len(*reloaded.State.Contacts[0].BuiltinTools) != 0 {
		t.Fatalf("reloaded empty selection = %+v", reloaded.State.Contacts)
	}
}

func TestContactProfileBuiltinChecksAreKeyboardReachable(t *testing.T) {
	editor := newContactProfileEditor(profileDraftForTest(t))
	editor.BuiltinTools = builtinToolsForTest()
	editor.BuiltinDefaults = []string{"bash", "edit", "read", "write"}
	editor.BuiltinToolsLoaded = true
	editor.BuiltinToolsSupported = true
	tr := contactProfileTester(editor, contactProfileActions{}, 560, 1200)
	tr.Frame()
	for i := 0; i < 20 && !tr.Focused("Use host defaults (bash, edit, read, write)"); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused("Use host defaults (bash, edit, read, write)") {
		t.Fatalf("host-default checkbox was not keyboard reachable: %v", tr.Texts())
	}
	tr.Key(0, ui.KeySpace)
	tr.Frame()
	if editor.Draft.BuiltinTools == nil || len(*editor.Draft.BuiltinTools) != 4 {
		t.Fatalf("Space did not turn the default into explicit all: %v", editor.Draft.BuiltinTools)
	}
	for i := 0; i < 8 && !tr.Focused("read"); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused("read") {
		t.Fatal("individual tool checkbox is not keyboard reachable")
	}
	tr.Key(0, ui.KeySpace)
	tr.Frame()
	if builtinToolSelectionContains(editor.Draft.BuiltinTools, "read") {
		t.Fatal("Space did not disable the focused read tool")
	}
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

func TestContactProfileBuiltinSaveFailureKeepsDraft(t *testing.T) {
	a, ct := freshBuiltinProfileTestApp(t)
	a.store.Path = t.TempDir()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	openBotProfileFromRail(t, tr, locales.In("en").AvatarContactLabel(ct.Name))
	editor := a.profileEditorFor(ct.ID)
	read := []string{"read"}
	editor.Draft.BuiltinTools = &read
	tr.Frame()
	if err := clickBotProfileAction(tr, locales.In("en").ContactSaveChanges()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.BuiltinTools != nil {
		t.Fatalf("save failure committed selection: %v", ct.BuiltinTools)
	}
	if editor.Draft.BuiltinTools == nil || len(*editor.Draft.BuiltinTools) != 1 || (*editor.Draft.BuiltinTools)[0] != "read" {
		t.Fatalf("save failure lost selection draft: %v", editor.Draft.BuiltinTools)
	}
	if editor.Error != profileNoticeErrorSave {
		t.Fatalf("save failure error = %q", editor.Error)
	}
}

func TestBuiltinToolCatalogErrorDoesNotOfferDeadRetry(t *testing.T) {
	a, ct := freshBuiltinProfileTestApp(t)
	a.connectionManaged = true
	a.runtimeStatus = runtimeStateFailed
	a.runtimeBuiltinToolsLoaded = false
	a.runtimeBuiltinToolsErr = "Runtime tool list unavailable."
	a.openContactProfile(ct.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Runtime tool list unavailable.") || tr.HasText("Retry built-in tools") {
		t.Fatalf("unavailable runtime rendered a dead retry: %v", tr.Texts())
	}

	config := testConnectionConfig(t)
	a.connectionPath = connectionConfigPath(a.store.Path)
	if err := saveConnectionConfig(a.connectionPath, config); err != nil {
		t.Fatal(err)
	}
	saved, err := loadConnectionConfig(a.connectionPath)
	if err != nil {
		t.Fatal(err)
	}
	a.connectionSaved = &saved
	a.runtimeBuiltinToolsErr = "Catalog could not be read."
	tr.Frame()
	if !tr.HasText("Catalog could not be read.") || tr.HasText("Retry built-in tools") {
		t.Fatalf("saved settings without a recovery path rendered a dead retry: %v", tr.Texts())
	}
	a.runtimeStage = startupStageLocalStore
	tr.Frame()
	if !tr.HasText("Catalog could not be read.") || !tr.HasText("Retry built-in tools") {
		t.Fatalf("configured startup recovery did not expose retry: %v", tr.Texts())
	}
	if a.pi != nil || a.connectionProbe != nil || a.connectionApplying || a.connectionTesting || a.runtimeAvailableForView() {
		t.Fatal("catalog error presentation started a host/probe/apply or fabricated runtime readiness")
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
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Save and Apply a managed Nagi connection") || tr.HasText("Use host defaults") {
		t.Fatalf("first use did not explain the managed-connection requirement: %v", tr.Texts())
	}
}

func TestPreviewBuiltinToolSelection(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, size := range []struct {
			name string
			w, h int
		}{{"1040x720", 1040, 720}, {"720x640", 720, 640}} {
			a, _ := freshBuiltinProfileTestApp(t)
			a.windowWidth = size.w
			a.openContactProfile(a.selectedID())
			read := []string{"read"}
			a.profileEditorFor(a.selectedID()).Draft.BuiltinTools = &read
			tr := ui.NewTester(a.view, size.w, size.h)
			tr.SetDark(dark)
			tr.Frame()
			for attempt := 0; attempt < 32; attempt++ {
				header, headerOK := tr.Find("Built-in tools")
				last, lastOK := tr.Find("write")
				if headerOK && header.Y >= 0 && header.Y < 120 && lastOK && last.Y >= 0 && last.Y+last.H <= float32(size.h) {
					break
				}
				tr.Scroll(float32(size.w-40), float32(size.h)/2, 0, 120)
				tr.Frame()
			}
			writePreviewPNG(t, filepath.Join(dir, "builtin-tool-selection-"+theme+"-"+size.name+".png"), tr.Image())
		}
	}
}
