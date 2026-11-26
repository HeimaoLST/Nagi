package app

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func pandoraBoxTestApp(t *testing.T) (*appModel, *contact, *contact) {
	t.Helper()
	a := testApp(t)
	pandora := &contact{
		ID: "builtin:pandorabox", Name: "PandoraBox", Role: "Bot Architect",
		Purpose:    "Design, create, and refine focused Nagi Bots through conversation.",
		SystemKind: pi.SystemKindBotArchitect, ProfileRevision: 1, Loaded: true, Status: statusReady,
	}
	specialist := &contact{
		ID: "bot-auditor", Name: "Release Auditor", Role: "Auditor",
		Purpose: "Audit release checklists.", Instructions: "Stay concise.",
		ProfileRevision: 1, Loaded: true, Status: statusReady, Draft: "keep specialist draft",
	}
	a.contacts = []*contact{pandora, specialist}
	a.selected = 0
	a.workspaceOpen = false
	a.windowWidth = 1280
	a.bindTaskContexts()
	return a, pandora, specialist
}

func TestPandoraBoxRailVisibilityMenusAndProfileWithoutArchive(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dark%t", locale, dark), func(t *testing.T) {
				a, pandora, specialist := pandoraBoxTestApp(t)
				msgs := locales.In(locale)
				tr := ui.NewTester(a.view, 1280, 720)
				tr.SetLocale(locale)
				tr.SetDark(dark)
				tr.SetFocused(true)
				tr.Frame()

				if !tr.HasText("PandoraBox") {
					t.Fatalf("PandoraBox missing in full rail: %v", tr.Texts())
				}
				if tr.HasText(locales.In("en").ProfileBuiltinBadge()) || tr.HasText(locales.In("zh-Hans").ProfileBuiltinBadge()) {
					t.Fatalf("retired Built-in badge rendered in full rail: %v", tr.Texts())
				}

				if err := tr.RightClick(msgs.AvatarContactLabel("PandoraBox")); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if !tr.HasText(msgs.ContactProfileAction()) || !tr.HasText(msgs.BotMenuHideFromTeam()) {
					t.Fatalf("PandoraBox menu missing Profile or Hide item: %v", tr.Texts())
				}
				if tr.HasText(msgs.BotMenuArchive()) || tr.HasText(msgs.RestoreContactAction()) {
					t.Fatalf("PandoraBox menu exposed archive/restore: %v", tr.Texts())
				}
				tr.Key(0, ui.KeyEscape)
				tr.Frame()

				if err := tr.RightClick(msgs.AvatarContactLabel(specialist.Name)); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if !tr.HasText(msgs.BotMenuArchive()) {
					t.Fatalf("ordinary Bot menu missing Archive action: %v", tr.Texts())
				}
				tr.Key(0, ui.KeyEscape)
				tr.Frame()

				a.windowWidth = 1040
				a.openContactProfile(pandora.ID)
				trCompact := ui.NewTester(a.view, 1040, 720)
				trCompact.SetLocale(locale)
				trCompact.SetDark(dark)
				trCompact.Frame()
				if !a.avatarRailActive() {
					t.Fatal("1040 with profile open should use compact avatar rail")
				}
				showPreviewTooltip(t, trCompact, msgs.AvatarContactLabel("PandoraBox"))
				for _, txt := range trCompact.Texts() {
					if strings.Contains(txt, msgs.AvatarContactLabel("PandoraBox")) &&
						(strings.Contains(txt, locales.In("en").ProfileBuiltinBadge()) || strings.Contains(txt, locales.In("zh-Hans").ProfileBuiltinBadge())) {
						t.Fatalf("retired Built-in badge leaked into compact avatar tooltip: %q", txt)
					}
				}
				if _, foundEn := trCompact.Find(locales.In("en").ProfileBuiltinBadge()); foundEn {
					t.Fatalf("retired Built-in element appeared in compact view: %v", trCompact.Texts())
				}
				if _, foundZh := trCompact.Find(locales.In("zh-Hans").ProfileBuiltinBadge()); foundZh {
					t.Fatalf("retired 内置 element appeared in compact view: %v", trCompact.Texts())
				}
				if !trCompact.HasText(msgs.ProfileBuiltinArchitectNote()) || !trCompact.HasText(msgs.BotMenuHideFromTeam()) {
					t.Fatalf("PandoraBox profile missing architect note or visibility toggle: %v", trCompact.Texts())
				}
				if trCompact.HasText(msgs.ProfileArchive()) || trCompact.HasText(msgs.ProfileRestore()) {
					t.Fatalf("PandoraBox profile exposed archive or restore action: %v", trCompact.Texts())
				}
				if a.archiveContact(pandora.ID) || pandora.Archived {
					t.Fatal("archiveContact archived built-in Bot")
				}
			})
		}
	}
}

func TestPandoraBoxVisibilityToggleHiddenSummonAndEmptyRailRecovery(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			a, pandora, specialist := pandoraBoxTestApp(t)
			msgs := locales.In(locale)
			tr := ui.NewTester(a.view, 1280, 720)
			tr.SetLocale(locale)
			tr.SetFocused(true)
			tr.Frame()

			if err := tr.Click(msgs.SettingsTitle()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tr.HasText(msgs.SettingsPandoraBoxVisibility()) {
				t.Fatalf("General settings missing PandoraBox visibility toggle: %v", tr.Texts())
			}
			focusedToggle := false
			for range 20 {
				if tr.Focused(msgs.SettingsPandoraBoxVisibility()) {
					focusedToggle = true
					break
				}
				tr.Key(0, ui.KeyTab)
				tr.Frame()
			}
			if !focusedToggle {
				t.Fatal("PandoraBox visibility ToggleSwitch was not keyboard-focusable")
			}
			tr.Key(0, ui.KeySpace)
			tr.Frame()
			if !a.hideBuiltinArchitect || a.selectedContact() != specialist {
				t.Fatalf("hiding selected PandoraBox did not switch to first visible Bot: hidden=%v selected=%q", a.hideBuiltinArchitect, a.selectedID())
			}
			if err := tr.Click(msgs.SettingsBackAccessible()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if slices.Contains(a.visibleContactIndices(), 0) || tr.HasText(msgs.AvatarContactLabel("PandoraBox")) {
				t.Fatalf("hidden PandoraBox remained visible in rail: visible=%v", a.visibleContactIndices())
			}

			if err := tr.Click(msgs.AvatarAddContact()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if err := tr.Click(msgs.CreateBotWithPandoraBox()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.createBot != nil || a.selectedContact() != pandora || !a.hideBuiltinArchitect || slices.Contains(a.visibleContactIndices(), 0) {
				t.Fatalf("summoned PandoraBox did not stay hidden in rail: modal=%v selected=%q hidden=%v visible=%v",
					a.createBot != nil, a.selectedID(), a.hideBuiltinArchitect, a.visibleContactIndices())
			}
			if !tr.Focused(msgs.ComposerLabel("PandoraBox")) {
				t.Fatal("summoning hidden PandoraBox did not focus its composer")
			}

			if err := tr.RightClick("PandoraBox"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tr.HasText(msgs.ContactProfileAction()) || !tr.HasText(msgs.BotMenuShowInTeam()) {
				t.Fatalf("summoned PandoraBox header menu missing profile or show action: %v", tr.Texts())
			}
			if err := choosePopoverMenuItem(tr, msgs.ContactProfileAction()); err != nil {
				t.Fatal(err)
			}
			if !a.detailIs(detailProfile) || !tr.HasText(msgs.BotMenuShowInTeam()) {
				t.Fatalf("summoned PandoraBox profile did not open with Show in Bot Team action: detail=%q texts=%v", a.activeDetail(), tr.Texts())
			}
			a.profileScrollFor(pandora.ID).Y = 1000
			tr.Frame()
			if err := tr.Click(msgs.BotMenuShowInTeam()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.hideBuiltinArchitect || !slices.Contains(a.visibleContactIndices(), 0) {
				t.Fatalf("Show in Bot Team from profile did not restore PandoraBox to rail: hidden=%v visible=%v", a.hideBuiltinArchitect, a.visibleContactIndices())
			}
			a.closeDetail()
			tr.Frame()

			if err := tr.RightClick(msgs.AvatarContactLabel("PandoraBox")); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if err := choosePopoverMenuItem(tr, msgs.BotMenuHideFromTeam()); err != nil {
				t.Fatal(err)
			}
			if !a.hideBuiltinArchitect || a.selectedContact() != specialist {
				t.Fatalf("Hide from Bot Team menu did not hide and switch selection: hidden=%v selected=%q", a.hideBuiltinArchitect, a.selectedID())
			}

			a.contacts = []*contact{pandora}
			a.selected = 0
			a.contactSearch = "filter"
			tr.Frame()
			if !tr.HasText(msgs.RailShowPandoraBox()) {
				t.Fatalf("empty full rail missing Show PandoraBox recovery action: %v", tr.Texts())
			}
			if err := tr.Click(msgs.RailShowPandoraBox()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.hideBuiltinArchitect || a.contactSearch != "" || !slices.Contains(a.visibleContactIndices(), 0) {
				t.Fatalf("empty-rail Show PandoraBox did not restore visibility and clear search: hidden=%v search=%q visible=%v",
					a.hideBuiltinArchitect, a.contactSearch, a.visibleContactIndices())
			}

			a.hideBuiltinArchitect = true
			a.windowWidth = 1040
			a.openContactProfile(pandora.ID)
			trCompact := ui.NewTester(a.view, 1040, 720)
			trCompact.SetLocale(locale)
			trCompact.Frame()
			if r, ok := trCompact.Find(msgs.RailShowPandoraBox()); !ok || r.W < 32 || r.H < 32 {
				t.Fatalf("empty compact avatar rail missing 32x32 Show PandoraBox button: rect=%+v ok=%v", r, ok)
			}
			if err := trCompact.Click(msgs.RailShowPandoraBox()); err != nil {
				t.Fatal(err)
			}
			trCompact.Frame()
			if a.hideBuiltinArchitect {
				t.Fatal("compact avatar rail Show PandoraBox did not restore PandoraBox")
			}
		})
	}
}

func TestCreateBotWithPandoraBoxShortcutSwitchesAndFocusesComposer(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			a, pandora, specialist := pandoraBoxTestApp(t)
			a.selected = 1
			a.openContactProfile(specialist.ID)
			msgs := locales.In(locale)
			tr := ui.NewTester(a.view, 1280, 720)
			tr.SetLocale(locale)
			tr.SetFocused(true)
			tr.Frame()

			if err := tr.Click(msgs.AvatarAddContact()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			shortcutRect, ok := tr.Find(msgs.CreateBotWithPandoraBox())
			if !ok || shortcutRect.H < 32 {
				t.Fatalf("Create with PandoraBox shortcut missing or under 32 DIP: rect=%+v ok=%v", shortcutRect, ok)
			}
			if err := tr.Click(msgs.CreateBotWithPandoraBox()); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.createBot != nil || a.activeDetail() != "" || a.selectedContact() != pandora {
				t.Fatalf("shortcut did not close modal/detail and switch to PandoraBox: modal=%v detail=%q selected=%q",
					a.createBot != nil, a.activeDetail(), a.selectedID())
			}
			if specialist.Draft != "keep specialist draft" {
				t.Fatalf("shortcut mutated other Bot draft: %q", specialist.Draft)
			}
			if !tr.Focused(msgs.ComposerLabel("PandoraBox")) {
				t.Fatal("shortcut did not focus PandoraBox composer")
			}
		})
	}

	a := testApp(t)
	a.windowWidth = 1280
	tr := ui.NewTester(a.view, 1280, 720)
	tr.SetLocale("en")
	tr.Frame()
	msgs := locales.In("en")
	if err := tr.Click(msgs.AvatarAddContact()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if tr.HasText(msgs.CreateBotWithPandoraBox()) {
		t.Fatal("Create with PandoraBox shortcut appeared without an active Architect Bot")
	}
}

func TestBotChangedEventProjectionSelectionAndDirtyEditorGuard(t *testing.T) {
	a, pandora, specialist := pandoraBoxTestApp(t)
	a.demo = false
	host := &piAppHost{
		productBots:   map[string]pi.ProductBot{},
		productDrafts: map[string]pi.ProductDraft{},
	}
	a.pi = host

	emit := func(change string, bot pi.ProductBot) {
		t.Helper()
		raw, err := json.Marshal(pi.ProductBotChangedEvent{Bot: bot, Change: change})
		if err != nil {
			t.Fatal(err)
		}
		a.applyProductEvent(host, pi.ProductEvent{Name: "bot.changed", Data: raw})
	}

	created := pi.ProductBot{
		BotID: "bot-new", ProfileRevision: 1, CurrentConversationID: "conv-new",
		Profile: pi.ProductBotProfile{Name: "QA Scout", Role: "Scout", Purpose: "Check diffs", Instructions: "Be brief."},
	}
	emit("created", created)
	if len(a.contacts) != 3 || a.contacts[2].ID != "bot-new" || !a.contacts[2].Loaded || a.selectedID() != pandora.ID {
		t.Fatalf("created event hijacked selection or failed append: count=%d selected=%q", len(a.contacts), a.selectedID())
	}

	a.openContactProfile(specialist.ID)
	cleanEditor := a.profileEditorFor(specialist.ID)
	if cleanEditor.profileRevision != 1 {
		t.Fatalf("initial editor revision = %d, want 1", cleanEditor.profileRevision)
	}
	rev2 := pi.ProductBot{
		BotID: specialist.ID, ProfileRevision: 2, CurrentConversationID: specialist.SessionID,
		Profile: pi.ProductBotProfile{Name: "Release Auditor v2", Role: specialist.Role, Purpose: "Updated purpose", Instructions: "Updated rules."},
	}
	emit("updated", rev2)
	if specialist.Name != "Release Auditor v2" || specialist.ProfileRevision != 2 ||
		cleanEditor.Draft.Name != "Release Auditor v2" || cleanEditor.Draft.Purpose != "Updated purpose" || cleanEditor.profileRevision != 2 {
		t.Fatalf("clean editor did not sync with rev2: bot=%+v editor=%+v", specialist, cleanEditor)
	}

	cleanEditor.Draft.Purpose = "Unsaved local purpose edit"
	rev3 := pi.ProductBot{
		BotID: specialist.ID, ProfileRevision: 3, CurrentConversationID: specialist.SessionID,
		Profile: pi.ProductBotProfile{Name: "Release Auditor v3", Role: specialist.Role, Purpose: "Remote architect purpose", Instructions: "Remote rules."},
	}
	emit("updated", rev3)
	if specialist.Name != "Release Auditor v3" || specialist.ProfileRevision != 3 {
		t.Fatalf("bot state did not advance to rev3: %+v", specialist)
	}
	if cleanEditor.Draft.Purpose != "Unsaved local purpose edit" || cleanEditor.profileRevision != 2 {
		t.Fatalf("dirty editor was overwritten or advanced revision: draft=%+v rev=%d", cleanEditor.Draft, cleanEditor.profileRevision)
	}
	if a.updateProductBot(host, specialist, cleanEditor, cleanEditor.Draft, false) {
		t.Fatal("stale dirty editor save was allowed past revision mismatch")
	}
	if cleanEditor.Error != profileNoticeChangedElsewhere {
		t.Fatalf("stale save error = %q, want %q", cleanEditor.Error, profileNoticeChangedElsewhere)
	}

	tr := ui.NewTester(a.view, 1280, 720)
	tr.SetLocale("en")
	tr.Frame()
	msgs := locales.In("en")
	if !tr.HasText(msgs.ProfileChangedElsewhere()) {
		t.Fatalf("profile conflict notice missing: %v", tr.Texts())
	}
	if err := tr.Click(msgs.ProfileDiscard()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if cleanEditor.Draft.Name != "Release Auditor v3" || cleanEditor.Draft.Purpose != "Remote architect purpose" || cleanEditor.profileRevision != 3 || cleanEditor.Error != "" {
		t.Fatalf("Discard did not reload saved rev3 profile: draft=%+v rev=%d err=%q", cleanEditor.Draft, cleanEditor.profileRevision, cleanEditor.Error)
	}

	staleRev := rev2
	emit("updated", staleRev)
	if specialist.ProfileRevision != 3 || specialist.Name != "Release Auditor v3" {
		t.Fatalf("stale bot.changed downgraded bot state: %+v", specialist)
	}

	a.contacts = []*contact{specialist}
	a.selected = 0
	emit("created", pi.ProductBot{
		BotID: "builtin:pandorabox", SystemKind: pi.SystemKindBotArchitect, ProfileRevision: 1, CurrentConversationID: "conv-pb",
		Profile: pi.ProductBotProfile{Name: "PandoraBox", Role: "Bot Architect", Purpose: "Architect"},
	})
	if len(a.contacts) != 2 || a.contacts[0].ID != "builtin:pandorabox" || a.selectedID() != specialist.ID {
		t.Fatalf("builtin insert shifted active selection: contacts=%d first=%q selected=%q", len(a.contacts), a.contacts[0].ID, a.selectedID())
	}
}
