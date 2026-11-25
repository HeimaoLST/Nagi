package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestLanguageModeReopensFromInjectedSource(t *testing.T) {
	a, _ := workspaceTestApp(t)
	oldRead := loadLanguageMode
	defer func() { loadLanguageMode = oldRead }()
	calls := 0
	loadLanguageMode = func() string {
		calls++
		if calls == 1 {
			return "zh-Hans"
		}
		return "en"
	}
	a.languageMode = "zh-CN"
	a.languageModeLoaded = true
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	if a.languageModeLoaded {
		t.Fatal("opening General kept the old loaded language mode")
	}
	a.ensureLanguageMode()
	if calls != 1 || a.languageMode != "zh-Hans" {
		t.Fatalf("first General load = calls=%d mode=%q", calls, a.languageMode)
	}
	a.closeSettings()
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	a.ensureLanguageMode()
	if calls != 2 || a.languageMode != "en" {
		t.Fatalf("reopened General load = calls=%d mode=%q", calls, a.languageMode)
	}
}

func TestGeneralLanguageUsesStableValuesAndPreservesState(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Draft = "keep the conversation draft"
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	a.settingsScroll.Y = 37
	a.languageMode = ""
	a.languageModeLoaded = true

	oldApply := applyAppLocale
	defer func() { applyAppLocale = oldApply }()
	var applied []string
	applyAppLocale = func(tag string) { applied = append(applied, tag) }

	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if err := tr.Click("System (English)"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("General") || !tr.HasText("Appearance") || !tr.HasText("Language") ||
		!tr.HasText("System (English)") || !tr.HasText("English") || !tr.HasText("简体中文") {
		t.Fatalf("General/Language labels missing: %v", tr.Texts())
	}
	if err := tr.Click("English"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(applied) != 1 || applied[0] != "en" || a.languageMode != "en" {
		t.Fatalf("English action = applied=%v mode=%q", applied, a.languageMode)
	}
	if a.settingsTab != settingsTabAppearance || ct.Draft != "keep the conversation draft" {
		t.Fatalf("English action reset UI state: tab=%q draft=%q", a.settingsTab, ct.Draft)
	}
	if err := tr.Click("English"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("System (English)"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(applied) != 2 || applied[1] != "" || a.languageMode != "" {
		t.Fatalf("System action = applied=%v mode=%q", applied, a.languageMode)
	}
	if ct.Draft != "keep the conversation draft" {
		t.Fatalf("System action reset UI state: draft=%q", ct.Draft)
	}

	tr.SetLocale("zh-Hans")
	tr.Frame()
	if err := tr.Click("系统（简体中文）"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("通用") || !tr.HasText("外观") || !tr.HasText("语言") ||
		!tr.HasText("系统（简体中文）") || !tr.HasText("简体中文") || !tr.HasText("English") {
		t.Fatalf("Chinese General/Language labels missing: %v", tr.Texts())
	}
	if a.languageMode != "" || a.settingsTab != settingsTabAppearance ||
		ct.Draft != "keep the conversation draft" {
		t.Fatalf("Chinese render changed state: mode=%q tab=%q draft=%q", a.languageMode, a.settingsTab, ct.Draft)
	}
	// Explicit Simplified Chinese and System have the same effective language
	// here; choosing System must still switch the stored mode and apply the
	// empty tag rather than staying on the explicit choice.
	if err := tr.Click("简体中文"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(applied) != 3 || applied[2] != "zh-Hans" || a.languageMode != "zh-Hans" {
		t.Fatalf("explicit Chinese action = applied=%v mode=%q", applied, a.languageMode)
	}
	if err := tr.Click("简体中文"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("系统（简体中文）"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(applied) != 4 || applied[3] != "" || a.languageMode != "" {
		t.Fatalf("System with the same effective language = applied=%v mode=%q", applied, a.languageMode)
	}
	if !tr.HasText("系统（简体中文）") || ct.Draft != "keep the conversation draft" {
		t.Fatalf("System choice lost its selected language or draft: %v", tr.Texts())
	}
}

func TestLanguageSwitchKeepsNonzeroSettingsScrollAndActualFocus(t *testing.T) {
	a, _ := workspaceTestApp(t)
	config := connectionConfig{API: connectionAPIChatCompletions, Endpoint: "https://provider.example.invalid/v1/very/long/endpoint", APIKey: "fixture", Model: "fixture", ContextTokens: 1000, OutputTokens: 100, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
	a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
	a.connectionSaved = &config
	a.connectionEffective = &config
	a.connectionForm.Error = strings.Repeat("A long form error that scrolls the settings page. ", 12)
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	a.connectionForm.FocusField = "Endpoint"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.Focused("API endpoint") {
		t.Fatalf("API endpoint did not take focus: %v", tr.Texts())
	}
	tr.Scroll(520, 400, 0, 100)
	tr.Frame()
	scrollBefore := a.settingsScroll.Y
	if scrollBefore <= 0 {
		t.Fatalf("settings page did not scroll: %v", scrollBefore)
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if a.settingsScroll.Y != scrollBefore || a.settingsTab != settingsTabConnection {
		t.Fatalf("language switch changed middle scroll/category: scroll=%v/%v tab=%q", scrollBefore, a.settingsScroll.Y, a.settingsTab)
	}
	if !tr.Focused("API 端点") {
		t.Fatalf("language switch lost actual Chinese focus: %v", tr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.Focused("API endpoint") {
		t.Fatalf("switching back lost the same input focus: %v", tr.Texts())
	}
	if a.settingsScroll.Y != scrollBefore || a.settingsTab != settingsTabConnection {
		t.Fatalf("switching back changed middle scroll/category: scroll=%v/%v tab=%q", scrollBefore, a.settingsScroll.Y, a.settingsTab)
	}
	if a.connectionForm.Draft.Endpoint != config.Endpoint {
		t.Fatalf("language switch changed the API draft: %q", a.connectionForm.Draft.Endpoint)
	}
	if _, active := tr.TextCaret(); !active {
		t.Fatal("switching back lost the text caret")
	}
}

func TestLanguageModeReadUsesInjectedSource(t *testing.T) {
	oldRead := loadLanguageMode
	defer func() { loadLanguageMode = oldRead }()
	loadLanguageMode = func() string { return "zh-Hans" }
	a := &appModel{}
	a.ensureLanguageMode()
	if a.languageMode != "zh-Hans" || !a.languageModeLoaded {
		t.Fatalf("injected language mode = %q loaded=%v", a.languageMode, a.languageModeLoaded)
	}
	b := &appModel{}
	loadLanguageMode = func() string { return "zh-CN" }
	b.ensureLanguageMode()
	if b.languageMode != "zh-CN" {
		t.Fatalf("unsupported explicit tag was discarded: %q", b.languageMode)
	}
	if got := languageModeIndex("zh-CN"); got != 1 {
		t.Fatalf("zh-CN maps to %d, want Hans", got)
	}
	if got := languageModeIndex("zh-SG"); got != 1 {
		t.Fatalf("zh-SG maps to %d, want Hans", got)
	}
	if got := languageModeIndex("zh-Hant"); got != 2 {
		t.Fatalf("zh-Hant maps to %d, want English fallback", got)
	}
	if got := languageModeIndex("fr"); got != 2 {
		t.Fatalf("fr maps to %d, want English fallback", got)
	}
}

func TestCatalogLanguageMatchingAndFallback(t *testing.T) {
	supported := []string{"en", "zh-Hans"}
	for _, preferred := range []string{"zh-Hans", "zh-CN", "zh-SG"} {
		if got := i18n.Match([]string{preferred}, supported); got != "zh-Hans" {
			t.Errorf("Match(%s) = %q, want zh-Hans", preferred, got)
		}
	}
	for _, preferred := range []string{"en", "en-US", "en-GB"} {
		if got := i18n.Match([]string{preferred}, supported); got != "en" {
			t.Errorf("Match(%s) = %q, want en", preferred, got)
		}
	}
	for _, preferred := range []string{"fr", "ja", "zh-Hant", "zh-TW", "zh-HK"} {
		if got := i18n.Match([]string{preferred}, supported); got != "" {
			t.Errorf("Match(%s) = %q, want no supported match", preferred, got)
		}
	}

	for _, tag := range []string{"zh-Hans", "zh-CN", "zh-SG"} {
		if got := locales.In(tag).SettingsTitle(); got != "设置" {
			t.Errorf("locales.In(%s).SettingsTitle() = %q", tag, got)
		}
	}
	for _, tag := range []string{"en", "en-US", "fr", "zh-Hant", "zh-TW", "zh-HK"} {
		if got := locales.In(tag).SettingsTitle(); got != "Settings" {
			t.Errorf("locales.In(%s).SettingsTitle() = %q, want English fallback", tag, got)
		}
	}
	if got := locales.Catalog.Languages(); !slices.Equal(got, []string{"en", "zh-Hans"}) {
		t.Errorf("catalog languages = %v", got)
	}
}

func TestUIStringsFollowTesterLocaleAndKeepDraftAndFocus(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Draft = "keep the conversation draft"
	a.openContactProfile(ct.ID)
	editor := a.profileEditorFor(ct.ID)
	editor.Draft.Name = "Locale draft"
	en, zh := locales.In("en"), locales.In("zh-Hans")

	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText(en.ContactProfileAction()) || !tr.HasText(ct.Name) || a.settingsOpen {
		t.Fatalf("English settings labels missing: %v", tr.Texts())
	}
	if err := tr.Click(en.FieldNameAccessible()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused(en.FieldNameAccessible()) {
		t.Fatalf("contact field did not take focus: %v", tr.Texts())
	}
	if _, active := tr.TextCaret(); !active {
		t.Fatal("contact field should hold the text caret")
	}

	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText(zh.ContactProfileAction()) || !tr.HasText(ct.Name) || a.settingsOpen {
		t.Fatalf("Chinese settings labels missing: %v", tr.Texts())
	}
	if editor.Draft.Name != "Locale draft" {
		t.Fatalf("locale change rebuilt the profile draft: %q", editor.Draft.Name)
	}
	if ct.Draft != "keep the conversation draft" {
		t.Fatalf("locale change rebuilt the conversation draft: %q", ct.Draft)
	}
	if !tr.Focused(zh.FieldNameAccessible()) {
		t.Fatalf("locale change lost field focus: %v", tr.Texts())
	}
	if _, active := tr.TextCaret(); !active {
		t.Fatal("locale change lost the text caret")
	}

	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText(en.ContactProfileAction()) || !tr.HasText(ct.Name) || a.settingsOpen {
		t.Fatalf("English labels did not return: %v", tr.Texts())
	}
	if !tr.Focused(en.FieldNameAccessible()) || editor.Draft.Name != "Locale draft" {
		t.Fatal("switching back rebuilt the field or draft")
	}
}

func TestSelectedSettingsAXFollowsTesterLocale(t *testing.T) {
	a, _ := workspaceTestApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("App actions") {
		t.Fatalf("English Settings rail AX missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("应用操作") || tr.HasText("App actions") {
		t.Fatalf("Chinese Settings rail AX missing: %v", tr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	a.settingsFocusBack = true
	tr.Frame()
	if !tr.Focused("← Back to workbench") || !tr.HasText("Settings navigation") {
		t.Fatalf("English Settings page AX missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.Focused("← 返回工作台") || !tr.HasText("设置导航") {
		t.Fatalf("Chinese Settings page AX missing: %v", tr.Texts())
	}
	if tr.HasText("Settings navigation") || tr.HasText("← Back to workbench") {
		t.Fatalf("English Settings page AX remained: %v", tr.Texts())
	}
}

func TestMarkdownCopyFeedbackFollowsTesterLocale(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		en   string
		zh   string
	}{
		{name: "success", en: "Copied", zh: "已复制"},
		{name: "failure", err: errors.New("clipboard unavailable"), en: "Copy failed: clipboard unavailable", zh: "复制失败：clipboard unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := ui.NewTester(func(c *ui.Context) {
				markdownReader(c, "source", markdownReaderOptions{
					CopyLabel: locales.In(c.Locale()).CopyMessageLabel(),
					CopyText:  func(string) error { return tc.err },
				})
			}, 500, 400)
			tr.SetLocale("en")
			tr.Frame()
			if err := tr.Click("Copy message"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			revealCopyTooltip(t, tr, "Copy message")
			if !tr.HasText(tc.en) {
				t.Fatalf("English copy feedback missing: %v", tr.Texts())
			}
			tr.SetLocale("zh-Hans")
			tr.Frame()
			revealCopyTooltip(t, tr, "复制消息")
			if !tr.HasText(tc.zh) {
				t.Fatalf("Chinese copy feedback missing: %v", tr.Texts())
			}
		})
	}
}

func revealCopyTooltip(t *testing.T, tr *ui.Tester, label string) {
	t.Helper()
	r, ok := tr.Find(label)
	if !ok {
		t.Fatalf("copy control %q not found: %v", label, tr.Texts())
	}
	tr.Move(1, 1)
	tr.Move(r.X+r.W/2, r.Y+r.H/2)
	tr.Advance(700 * time.Millisecond)
	tr.Frame()
}

func TestCopyLabelsFollowTesterLocale(t *testing.T) {
	a := testApp(t)
	ct := a.selectedContact()
	ct.Messages = []*message{{
		ID: 1, Kind: msgContact, Text: "Summary line\n\n```go\nprintln(1)\n```",
	}}
	tr := ui.NewTester(a.view, 1040, 720)
	for _, tc := range []struct {
		locale   string
		wantMenu string
		wantCode string
	}{
		{"en", "Copy message", "Copy code"},
		{"zh-Hans", "复制消息", "复制代码"},
		{"en", "Copy message", "Copy code"},
	} {
		tr.SetLocale(tc.locale)
		tr.Frame()
		if !tr.HasText(tc.wantCode) {
			t.Fatalf("%s code copy label %q missing: %v", tc.locale, tc.wantCode, tr.Texts())
		}
		if tr.HasText(tc.wantMenu) {
			t.Fatalf("%s outer message copy button unexpectedly visible before right-click: %v", tc.locale, tr.Texts())
		}
		if err := tr.RightClick("Summary line"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if !tr.HasText(tc.wantMenu) {
			t.Fatalf("%s popover copy label %q missing: %v", tc.locale, tc.wantMenu, tr.Texts())
		}
		tr.Key(0, ui.KeyEscape)
		tr.Frame()
	}
}

func TestPublishedCatalogContract(t *testing.T) {
	en := readCatalogFile(t, "en.json")
	zh := readCatalogFile(t, "zh-Hans.json")
	if err := checkCatalogContract(en, zh); err != nil {
		t.Fatal(err)
	}
	if got := readCatalogLocale(t, "zh-Hans.json"); got != "zh-Hans" {
		t.Fatalf("zh-Hans @@locale = %q", got)
	}
	entries, err := os.ReadDir(filepath.Join("..", "..", "locales"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") && strings.Contains(strings.ToLower(entry.Name()), "xa") {
			t.Fatalf("pseudo locale must not be published: %s", entry.Name())
		}
	}
}

func TestCatalogContractAllowsArgumentReorder(t *testing.T) {
	en := map[string]string{
		"summary": "{name} has {count, plural, one {# file} other {# files}}",
	}
	zh := map[string]string{
		"summary": "{count, plural, other {# 个文件}}：{name}",
	}
	if err := checkCatalogContract(en, zh); err != nil {
		t.Fatalf("a legal argument reorder was rejected: %v", err)
	}
}

func TestCatalogContractRejectsMissingKeyAndWrongArgument(t *testing.T) {
	en := map[string]string{"greeting": "Hello {name}", "copyFailed": "Copy failed: {error}"}
	if err := checkCatalogContract(en, map[string]string{"greeting": "你好 {name}"}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing key contract = %v", err)
	}
	if err := checkCatalogContract(en, map[string]string{"greeting": "你好 {other}", "copyFailed": "复制失败：{error}"}); err == nil || !strings.Contains(err.Error(), "arguments") {
		t.Fatalf("argument contract = %v", err)
	}
}

func readCatalogLocale(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "locales", name))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	var tag string
	if err := json.Unmarshal(raw["@@locale"], &tag); err != nil {
		t.Fatal(err)
	}
	return tag
}

func readCatalogFile(t *testing.T, name string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "locales", name))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for key, value := range raw {
		if strings.HasPrefix(key, "@") {
			continue
		}
		var message string
		if err := json.Unmarshal(value, &message); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		out[key] = message
	}
	return out
}

func checkCatalogContract(en, translated map[string]string) error {
	var missing, extra []string
	for key := range en {
		if _, ok := translated[key]; !ok {
			missing = append(missing, key)
		}
	}
	for key := range translated {
		if _, ok := en[key]; !ok {
			extra = append(extra, key)
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	if len(missing) > 0 {
		return fmt.Errorf("translation missing keys: %s", strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		return fmt.Errorf("translation has extra keys: %s", strings.Join(extra, ", "))
	}
	for _, key := range sortedMessageKeys(en) {
		source, err := i18n.ParseMessage(en[key])
		if err != nil {
			return fmt.Errorf("%s source: %w", key, err)
		}
		target, err := i18n.ParseMessage(translated[key])
		if err != nil {
			return fmt.Errorf("%s translation: %w", key, err)
		}
		if !sameArgs(source, target) {
			return fmt.Errorf("%s translation arguments differ: source %v, translation %v", key, source.Args(), target.Args())
		}
	}
	return nil
}

func sameArgs(source, target *i18n.Message) bool {
	sourceArgs := map[string]bool{}
	for _, arg := range source.Args() {
		sourceArgs[arg.Name+"\x00"+arg.Kind] = true
	}
	targetArgs := map[string]bool{}
	for _, arg := range target.Args() {
		targetArgs[arg.Name+"\x00"+arg.Kind] = true
	}
	if len(sourceArgs) != len(targetArgs) {
		return false
	}
	for arg := range sourceArgs {
		if !targetArgs[arg] {
			return false
		}
	}
	return true
}

func sortedMessageKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestShellAndNewContactFollowLocale(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Draft = "keep the draft"
	a.appearanceStatus = appearanceStatusApplied
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Applied.") {
		t.Fatalf("English appearance notice missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("已应用。") {
		t.Fatalf("stored appearance notice did not re-localize: %v", tr.Texts())
	}
	if !tr.HasText("连接与模型") || !tr.HasText("外观") || !tr.HasText("语言") {
		t.Fatalf("Chinese settings shell missing: %v", tr.Texts())
	}
	a.closeSettings()
	a.openContactProfile(ct.ID)
	tr.Frame()
	if msgs := locales.In("zh-Hans"); !tr.HasText(msgs.ContactProfileAction()) || !tr.HasText(msgs.FieldNameAccessible()) ||
		!tr.HasText(msgs.FieldPurposeAccessible()) || !tr.HasText(msgs.FieldInstructionsAccessible()) ||
		!tr.HasText(msgs.ContactSaveChanges()) || tr.HasText(msgs.FieldWorkspaceAccessible()) {
		t.Fatalf("Chinese contact profile missing or retained retired workspace field: %v", tr.Texts())
	}
	a.closeDetail()
	tr.Frame()
	if !tr.HasText("Bot Team") || !tr.HasText(locales.In("zh-Hans").ComposerLabel(ct.Name)) || !tr.HasText("还没有消息") {
		t.Fatalf("Chinese workbench shell missing: %v", tr.Texts())
	}
	if ct.Draft != "keep the draft" {
		t.Fatalf("locale frame changed the draft: %q", ct.Draft)
	}

	demo := testApp(t)
	demoTr := ui.NewTester(demo.view, 1040, 720)
	demoTr.SetLocale("zh-Hans")
	demoTr.Frame()
	demo.addDefaultContact()
	demoTr.Frame()
	last := demo.contacts[len(demo.contacts)-1]
	if last.Name != locales.In("zh-Hans").NewContactName(1) {
		t.Fatalf("new contact name = %q, want the frame language", last.Name)
	}
	if demo.contacts[0].Name != "Nagi" {
		t.Fatalf("existing contact name was rewritten: %q", demo.contacts[0].Name)
	}
}

func TestConnectionFeedbackFollowLocale(t *testing.T) {
	a, _ := workspaceTestApp(t)
	config := connectionConfig{API: connectionAPIChatCompletions, Endpoint: "https://provider.example.invalid/v1", APIKey: "fixture", Model: "fixture", ContextTokens: 131072, OutputTokens: 8192, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
	a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
	a.connectionSaved = &config
	a.connectionForm.Status = connectionStatusApplied
	a.connectionForm.Errors = map[string]string{"Endpoint": connectionValidationEndpointURL}
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText(locales.In("en").ConnectionStatusApplied()) ||
		!tr.HasText(locales.In("en").ConnectionBlockedStore()) ||
		!tr.HasText("Enter an absolute http(s) URL with a host.") {
		t.Fatalf("English connection feedback missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText(locales.In("zh-Hans").ConnectionStatusApplied()) ||
		!tr.HasText(locales.In("zh-Hans").ConnectionBlockedStore()) ||
		!tr.HasText("请输入带主机的绝对 http(s) URL。") {
		t.Fatalf("Chinese connection feedback missing: %v", tr.Texts())
	}
	if a.connectionForm.Draft.Endpoint != config.Endpoint {
		t.Fatalf("locale switch changed the connection draft: %q", a.connectionForm.Draft.Endpoint)
	}

}

func TestRuntimeStartupFeedbackFollowsLocale(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.runtimeStatus = runtimeStateFailed
	a.runtimeStage = "Connection configuration"
	a.runtimeErrKind = runtimeNoticeConnectionRequired
	a.runtimeErr = ""
	a.runtimeHintKind = startupHintEnterConnection
	a.openAdvancedSettings()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Runtime: Failed") ||
		!tr.HasText("Connection configuration: Connection settings are required before the runtime can start.") ||
		!tr.HasText("Enter a connection, model and working directory, then save and apply.") {
		t.Fatalf("English runtime startup feedback missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("运行时：失败") ||
		!tr.HasText("连接配置：启动运行时前需要连接设置。") ||
		!tr.HasText("请输入连接、模型和工作目录，然后保存并应用。") {
		t.Fatalf("Chinese runtime startup feedback missing: %v", tr.Texts())
	}

}

func TestChatProgressFollowsLocale(t *testing.T) {
	workApp, workContact := workspaceTestApp(t)
	workApp.workspaceOpen = false
	workContact.Status = statusWorking
	workContact.Work, workContact.WorkArg = workNoticeRunningTool, "read"
	workTr := ui.NewTester(workApp.view, 1040, 720)
	workTr.SetLocale("zh-Hans")
	workTr.Frame()
	if !workTr.HasText(locales.In("zh-Hans").BotStatusReplying()) {
		t.Fatalf("Chinese work progress missing: %v", workTr.Texts())
	}
	workTr.SetLocale("en")
	workTr.Frame()
	if !workTr.HasText(locales.In("en").BotStatusReplying()) {
		t.Fatalf("English work progress did not return: %v", workTr.Texts())
	}
}

func TestTaskContextLabelsFollowLocale(t *testing.T) {
	tc := &taskContext{}
	item, err := tc.AddSnippet("Release notes", "Line one")
	if err != nil {
		t.Fatal(err)
	}
	tc.PreviewID = item.ID
	tr := taskContextTester(tc, false)
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("资料") || !tr.HasText("添加片段") || !tr.HasText("命名片段") ||
		!tr.HasText("预览") || !tr.HasText("编辑") || !tr.HasText("移除") || !tr.HasText("Release notes") {
		t.Fatalf("Chinese task context labels or raw name missing: %v", tr.Texts())
	}
	tc.CancelForm()
	tc.OpenSnippetForm()
	tr.Frame()
	if !tr.HasText("名称") || !tr.HasText("文本") || !tr.HasText("保存资料片段") {
		t.Fatalf("Chinese task context form labels missing: %v", tr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Materials") || !tr.HasText("Add snippet") || !tr.HasText("Save snippet") {
		t.Fatalf("English task context labels did not return: %v", tr.Texts())
	}

	missing := &taskContext{}
	missing.OpenFileForm()
	missing.FormPath = "/definitely/missing/context.txt"
	missing.SaveForm()
	missingTr := taskContextTester(missing, false)
	missingTr.SetLocale("zh-Hans")
	missingTr.Frame()
	if !missingTr.HasText(locales.In("zh-Hans").TaskContextErrReadDetail("open /definitely/missing/context.txt")) {
		t.Fatalf("task context read detail was lost: %v", missingTr.Texts())
	}
}

func taskContextTester(tc *taskContext, dark bool) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		theme := resolveWorkbenchTheme(dark)
		c.SetTheme(theme.Theme)
		ui.Scroll(c).Fill().Padding(16).Background(theme.Theme.Background).Children(func() {
			tc.View(c, theme)
		})
	}, 720, 1200)
}

func TestPiDisconnectNoticeFollowsLocaleWithoutChangingRaw(t *testing.T) {
	a, ct := workspaceTestApp(t)
	recoveryTestStore(t, a)
	a.runtimeStatus = runtimeStateExited
	applyProductExecution(ct, &pi.ProductExecution{ExecutionID: "t1", State: "unknown"})
	raw := ct.SendError
	before := recoveryRecordSnapshot(t, ct)
	tr := ui.NewTester(a.view, 1040, 720)
	for _, language := range []string{"en", "zh-Hans"} {
		tr.SetLocale(language)
		tr.Frame()
		msgs := locales.In(language)
		summary := msgs.SendDisconnectedState()
		detailsLabel, hideLabel, reloadLabel := msgs.DetailsAction(), msgs.HideDetailsAction(), msgs.ReloadAction()
		for _, stage := range []struct {
			action   string
			expanded bool
		}{
			{},
			{action: detailsLabel, expanded: true},
			{action: hideLabel},
		} {
			if stage.action != "" {
				if err := tr.Click(stage.action); err != nil {
					t.Fatalf("%s %q activation: %v", language, stage.action, err)
				}
				tr.Frame()
			}
			summaryCount := 0
			for _, text := range tr.Texts() {
				if text == summary {
					summaryCount++
				}
			}
			if summaryCount != 1 {
				t.Fatalf("%s expanded=%v uncertainty summaries = %d, want 1: %v", language, stage.expanded, summaryCount, tr.Texts())
			}
			if visible := tr.HasText(ct.SendError); visible != stage.expanded {
				t.Fatalf("%s expanded=%v complete send diagnostic visible=%v: %v", language, stage.expanded, visible, tr.Texts())
			}
			detailsBounds, detailsFound := tr.Find(detailsLabel)
			hideBounds, hideFound := tr.Find(hideLabel)
			if detailsFound == stage.expanded || hideFound != stage.expanded {
				t.Fatalf("%s expanded=%v disclosure controls: Details=%v Hide=%v", language, stage.expanded, detailsFound, hideFound)
			}
			actionBounds := detailsBounds
			if stage.expanded {
				actionBounds = hideBounds
			}
			if actionBounds.W <= 0 || actionBounds.H <= 0 || actionBounds.X < 0 || actionBounds.Y < 0 || actionBounds.X+actionBounds.W > 1040 || actionBounds.Y+actionBounds.H > 720 {
				t.Fatalf("%s expanded=%v disclosure control outside viewport: %+v", language, stage.expanded, actionBounds)
			}
			if _, found := tr.Find(reloadLabel); found {
				t.Fatalf("%s disconnected notice exposed Reload: %v", language, tr.Texts())
			}
		}
	}
	if ct.SendError != raw {
		t.Fatal("locale switch changed the original Pi disconnect diagnostic")
	}
	if recoveryRecordSnapshot(t, ct) != before || a.submissionInFlight != 0 || ct.SendState != sendUnknown || ct.ActiveTaskID != "" {
		t.Fatal("disconnected disclosure changed execution facts or replayed work")
	}
}

func TestCancelFailureNoticeReplacesRequestAndClears(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t1"
	ct.SendState = sendAccepted
	ct.setSendError("Cancellation requested", "send-cancelled", "")
	ct.setSendError("Cancel failed: backend down", "send-cancel-failed", "backend down")
	if ct.SendErrorKind != "send-cancel-failed" || ct.SendErrorDetail != "backend down" {
		t.Fatalf("cancel failure = %q / %q", ct.SendErrorKind, ct.SendErrorDetail)
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText(locales.In("zh-Hans").IssueSendCancelFailed("")) || tr.HasText("backend down") || tr.HasText("已请求取消") {
		t.Fatalf("Chinese cancel failure missing or stale: %v", tr.Texts())
	}
	if err := tr.Click(locales.In("zh-Hans").DetailsAction()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Cancel failed: backend down") || ct.SendErrorKind != "send-cancel-failed" || ct.SendErrorDetail != "backend down" {
		t.Fatalf("Chinese disclosure lost the original cancel failure: %v", tr.Texts())
	}
	if err := tr.Click(locales.In("zh-Hans").HideDetailsAction()); err != nil {
		t.Fatal(err)
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText(locales.In("en").IssueSendCancelFailed("")) || tr.HasText("backend down") || tr.HasText("Cancellation requested") {
		t.Fatalf("English cancel failure missing or stale: %v", tr.Texts())
	}
	if err := tr.Click(locales.In("en").DetailsAction()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Cancel failed: backend down") || ct.SendError != "Cancel failed: backend down" || ct.ActiveTaskID != "t1" || ct.SendState != sendAccepted {
		t.Fatal("English disclosure changed the original cancel failure or active task")
	}
	ct.setSendError("raw provider 500", "", "")
	tr.Frame()
	if !tr.HasText(locales.In("en").RecoveryRequestFailed()) || tr.HasText("raw provider 500") || tr.HasText("Cancel failed:") {
		t.Fatalf("next raw failure revived the old kind: %v", tr.Texts())
	}
	if err := tr.Click(locales.In("en").DetailsAction()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("raw provider 500") || tr.HasText("backend down") || ct.SendErrorKind != "" || ct.SendErrorDetail != "" {
		t.Fatalf("next raw failure lost its own diagnostic: %v", tr.Texts())
	}
	ct.clearSendError()
	tr.SetLocale("zh-Hans")
	tr.Frame()
	tr.SetLocale("en")
	tr.Frame()
	if tr.HasText("raw provider 500") || tr.HasText("Cancel failed: backend down") {
		t.Fatalf("cleared send notice revived: %v", tr.Texts())
	}
}

func TestGuardedActionsKeepCanonicalRawAndKind(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Archived = true
	if a.send(ct, "blocked") {
		t.Fatal("archived send was accepted")
	}
	if ct.SendError != locales.In("en").CanSendArchived() || ct.SendErrorKind != blockArchivedSend {
		t.Fatalf("send guard raw/kind = %q / %q", ct.SendError, ct.SendErrorKind)
	}
}

func TestChatScrollSurvivesLocaleSwitch(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Draft = "keep the chat draft"
	for index := 0; index < 40; index++ {
		ct.Messages = append(ct.Messages, &message{
			Key:  fmt.Sprintf("scroll-message-%02d", index),
			Kind: msgContact,
			Text: fmt.Sprintf("Visible chat message %02d that keeps its rendered row while the language changes.", index+1),
		})
	}
	cfg := connectionConfig{API: connectionAPIChatCompletions, Endpoint: "https://provider.example.invalid/v1/very/long/endpoint", APIKey: "fixture", Model: "fixture-provider-model", ContextTokens: 131072, OutputTokens: 8192, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
	a.connectionForm = newConnectionSettings(&cfg, cfg.WorkingDirectory)
	a.connectionSaved = &cfg
	a.connectionEffective = &cfg
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	viewport, ok := tr.Find(locales.In("en").ConversationMessagesLabel())
	if !ok || viewport.W <= 0 || viewport.H <= 0 {
		t.Fatalf("chat viewport not visible: %+v", viewport)
	}
	tr.Scroll(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 0, -600)
	tr.Frame()
	first, last := ct.list.Visible()
	if first <= 0 || last <= first {
		t.Fatalf("chat did not scroll to a middle region: first=%d last=%d", first, last)
	}
	anchorKey := ct.Messages[first].Key
	anchorText := ct.Messages[first].Text
	if !tr.HasText(anchorText) {
		t.Fatalf("chat anchor row is not visible: %q", anchorText)
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	nextFirst, nextLast := ct.list.Visible()
	if nextFirst != first || nextLast != last || ct.Messages[nextFirst].Key != anchorKey || !tr.HasText(anchorText) {
		t.Fatalf("locale switch moved the chat anchor: first=%d->%d last=%d->%d key=%q", first, nextFirst, last, nextLast, anchorKey)
	}
	if ct.Draft != "keep the chat draft" || a.selectedContact() != ct {
		t.Fatalf("locale switch changed the conversation or draft: contact=%q draft=%q", a.selectedID(), ct.Draft)
	}
	if a.connectionForm.Draft.Endpoint != cfg.Endpoint || a.connectionForm.Draft.Model != cfg.Model || a.connectionSaved.Model != cfg.Model || a.connectionEffective.Endpoint != cfg.Endpoint {
		t.Fatalf("locale switch changed stable connection values: %+v", a.connectionForm.Draft)
	}
	if !tr.HasText(locales.In("zh-Hans").BackToLatest()) {
		t.Fatalf("Chinese chat label missing: %v", tr.Texts())
	}

	tr.SetLocale("en")
	tr.Frame()
	nextFirst, nextLast = ct.list.Visible()
	if nextFirst != first || nextLast != last || ct.Messages[nextFirst].Key != anchorKey || !tr.HasText(anchorText) {
		t.Fatalf("switching back moved chat scroll: first=%d last=%d", nextFirst, nextLast)
	}
	if !tr.HasText(locales.In("en").BackToLatest()) {
		t.Fatalf("English chat label did not return: %v", tr.Texts())
	}
}

func TestSelectedTextSurvivesLocaleSwitchInContactField(t *testing.T) {
	a, ct := workspaceTestApp(t)
	other := &contact{ID: "contact-b", Name: "B contact", Role: "Assistant", Status: statusReady, Loaded: true, Workspace: t.TempDir()}
	a.contacts = append(a.contacts, other)
	a.openContactProfile(other.ID)
	otherEditor := a.profileEditorFor(other.ID)
	otherEditor.Draft.Purpose = "keep B purpose"
	a.openContactProfile(ct.ID)
	editor := a.profileEditorFor(ct.ID)
	editor.Draft.Purpose = "keep A purpose"
	editor.Draft.Instructions = "keep A instructions"
	nameLabel := locales.In("en").FieldNameAccessible()
	ct.Draft = "keep conversation draft"
	ct.Context.OpenSnippetForm()
	ct.Context.FormName = "keep context form name"
	ct.Context.FormText = "keep context form text"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText(locales.In("en").FieldPurposeAccessible()) || editor.Draft.Purpose != "keep A purpose" {
		t.Fatalf("contact profile purpose is not visible: %v", tr.Texts())
	}
	if err := tr.Click(nameLabel); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused(nameLabel) {
		t.Fatalf("contact name field did not take focus: %v", tr.Texts())
	}
	if _, active := tr.TextCaret(); !active {
		t.Fatal("contact name field has no text caret")
	}
	originalName := ct.Name
	tr.Key(ui.Cmd, ui.KeyA)
	tr.Command("copy")
	if got := tr.Clipboard(); got != originalName {
		t.Fatalf("Cmd+A did not select the contact name: clipboard=%q want=%q", got, originalName)
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	tr.Type("替换")
	tr.Frame()
	if got := editor.Draft.Name; got != "替换" {
		t.Fatalf("typing after the locale switch did not replace the selection: %q", got)
	}
	if ct.Name != originalName {
		t.Fatalf("typing into the profile draft changed the saved contact: %q", ct.Name)
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.Focused(nameLabel) {
		t.Fatalf("switching back lost the contact field focus: %v", tr.Texts())
	}
	if _, active := tr.TextCaret(); !active {
		t.Fatal("switching back lost the text caret")
	}
	if editor.Draft.Name != "替换" || a.contactProfileID != ct.ID || a.settingsOpen {
		t.Fatalf("selection/focus repair changed target or draft: name=%q Bot=%q", editor.Draft.Name, a.contactProfileID)
	}
	if editor.Draft.Purpose != "keep A purpose" || editor.Draft.Instructions != "keep A instructions" || otherEditor.Draft.Purpose != "keep B purpose" {
		t.Fatal("locale switch changed A/B profile drafts")
	}
	if ct.Draft != "keep conversation draft" {
		t.Fatalf("locale switch changed the conversation draft: %q", ct.Draft)
	}
	if !ct.Context.FormOpen || ct.Context.FormName != "keep context form name" || ct.Context.FormText != "keep context form text" {
		t.Fatalf("locale switch changed the context form draft: open=%v name=%q text=%q", ct.Context.FormOpen, ct.Context.FormName, ct.Context.FormText)
	}
}
