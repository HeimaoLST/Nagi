package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/locales"
)

func containsText(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func TestSettingsContactPickerIsolatesProfileUndo(t *testing.T) {
	isolateConnectionApp(t, t.TempDir(), "")
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	tr := ui.NewTester(app.view, 1040, 720)
	tr.SetLocale("en")
	tr.SetFocused(true)
	tr.Frame()
	click := func(label string) {
		t.Helper()
		if err := tr.Click(label); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
	}
	pickProfile := func(name string) {
		t.Helper()
		click("Edit contact")
		click(name)
	}
	expectProfile := func(id, name string) {
		t.Helper()
		if app.settingsContactID != id {
			t.Fatalf("profile picker selected %q, want %q", app.settingsContactID, id)
		}
		if got := app.settingsContactEditor().Draft.Name; got != name {
			t.Fatalf("profile %q name draft = %q, want %q", id, got, name)
		}
	}
	editName := func(name string) {
		tr.Key(ui.Cmd, ui.KeyA)
		tr.Type(name)
		tr.Frame()
	}

	const conversationDraft = "Unsent message for A"
	const profileDraftA = "Unsaved profile name for A"
	const savedNameB = "Saved profile name for B"
	msgs := locales.In("en")
	a := app.selectedContact()
	nameA := a.Name
	click("← Back to workbench")
	focusComposer(t, tr, msgs.ComposerLabel(nameA))
	tr.Type(conversationDraft)
	tr.Frame()
	click("Add contact")
	b := app.selectedContact()
	nameB := b.Name
	if a.ID == b.ID || a.Draft != conversationDraft || b.Draft != "" {
		t.Fatalf("adding B did not isolate drafts: A (%q) = %q, B (%q) = %q", a.ID, a.Draft, b.ID, b.Draft)
	}
	click(msgs.AvatarContactLabel(nameA))
	click("Settings")
	click("Contact profile")
	expectProfile(a.ID, nameA)
	click("Contact name")
	editName(profileDraftA)
	expectProfile(a.ID, profileDraftA)
	tr.Key(ui.Cmd, ui.KeyZ)
	tr.Frame()
	expectProfile(a.ID, nameA)
	tr.Key(ui.Cmd|ui.Shift, ui.KeyZ)
	tr.Frame()
	expectProfile(a.ID, profileDraftA)

	pickProfile(nameB)
	expectProfile(b.ID, nameB)
	click("Contact name")
	if active, _ := tr.TextInputActive(); !tr.Focused("Contact name") || !active {
		t.Fatal("B's name input did not receive keyboard focus")
	}
	tr.Key(ui.Cmd, ui.KeyZ)
	tr.Frame()
	expectProfile(b.ID, nameB)
	pickProfile(nameA)
	expectProfile(a.ID, profileDraftA)
	pickProfile(nameB)
	click("Contact name")
	editName(savedNameB)
	if err := clickSettingsAction(tr, "Save changes"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.Name != nameA || b.Name != savedNameB || app.selectedID() != a.ID || a.Draft != conversationDraft || b.Draft != "" {
		t.Fatalf("B save crossed Bot boundaries: A name=%q draft=%q, B name=%q draft=%q, selected=%q", a.Name, a.Draft, b.Name, b.Draft, app.selectedID())
	}

	app, err = newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	tr = ui.NewTester(app.view, 1040, 720)
	tr.SetLocale("en")
	tr.SetFocused(true)
	tr.Frame()
	click("Contact profile")
	expectProfile(a.ID, nameA)
	pickProfile(savedNameB)
	expectProfile(b.ID, savedNameB)
	if reopenedA, reopenedB := app.contactByID(a.ID), app.contactByID(b.ID); reopenedA.Draft != conversationDraft || reopenedB.Draft != "" {
		t.Fatalf("reopened drafts crossed Bot boundaries: A=%q B=%q", reopenedA.Draft, reopenedB.Draft)
	}
}
