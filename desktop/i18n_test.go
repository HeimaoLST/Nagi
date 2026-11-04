package main

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

	"saki/desktop/locales"
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
	config := connectionConfig{Version: 1, API: connectionAPIChatCompletions, Endpoint: "https://provider.example.invalid/v1/very/long/endpoint", APIKey: "fixture", Model: "fixture", ContextTokens: 1000, OutputTokens: 100, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
	a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
	a.connectionSaved = &config
	a.connectionEffective = &config
	a.connectionForm.Error = strings.Repeat("A long form error that scrolls the settings page. ", 12)
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	a.connectionForm.focusField = "Endpoint"
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
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	editor := a.settingsContactEditor()
	editor.Draft.Name = "Locale draft"
	a.settingsContactID = ct.ID

	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Settings") || !tr.HasText("Contact profile") {
		t.Fatalf("English settings labels missing: %v", tr.Texts())
	}
	if err := tr.Click("Contact name"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Contact name") {
		t.Fatalf("contact field did not take focus: %v", tr.Texts())
	}
	if _, active := tr.TextCaret(); !active {
		t.Fatal("contact field should hold the text caret")
	}

	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("设置") || !tr.HasText("联系人资料") || !tr.HasText("联系人资料：Saki") {
		t.Fatalf("Chinese settings labels missing: %v", tr.Texts())
	}
	if a.settingsContactEditor().Draft.Name != "Locale draft" {
		t.Fatalf("locale change rebuilt the profile draft: %q", a.settingsContactEditor().Draft.Name)
	}
	if ct.Draft != "keep the conversation draft" {
		t.Fatalf("locale change rebuilt the conversation draft: %q", ct.Draft)
	}
	if !tr.Focused("联系人名称") {
		t.Fatalf("locale change lost field focus: %v", tr.Texts())
	}
	if _, active := tr.TextCaret(); !active {
		t.Fatal("locale change lost the text caret")
	}

	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Settings") || !tr.HasText("Contact profile") {
		t.Fatalf("English labels did not return: %v", tr.Texts())
	}
	if !tr.Focused("Contact name") || a.settingsContactEditor().Draft.Name != "Locale draft" {
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
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Copy message") {
		t.Fatalf("English copy label missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("复制消息") {
		t.Fatalf("Chinese copy label missing: %v", tr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Copy message") {
		t.Fatalf("English copy label did not return: %v", tr.Texts())
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
	entries, err := os.ReadDir("locales")
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
	data, err := os.ReadFile(filepath.Join("locales", name))
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
	data, err := os.ReadFile(filepath.Join("locales", name))
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

func TestShellHistoryAndNewContactFollowLocale(t *testing.T) {
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
	a.openHistory()
	tr.Frame()
	if !tr.HasText("历史：Saki") || !tr.HasText("关闭历史") {
		t.Fatalf("Chinese history shell missing: %v", tr.Texts())
	}
	fullEntry := historyEntry{ID: "h-full", Date: "10月30日", From: "contact", Text: "完整历史原文，保持原样。"}
	ct.History = append(ct.History, fullEntry)
	a.openHistoryEntryFull(fullEntry)
	tr.Frame()
	if !tr.HasText("联系人。10月30日。s1") || !tr.HasText(fullEntry.Text) {
		t.Fatalf("Chinese full history speaker or raw text missing: %v", tr.Texts())
	}
	a.closeContactHistoryFull()
	tr.Frame()
	ownEntry := historyEntry{ID: "h-own", Date: "10月31日", From: "me", Text: "我的原始消息。"}
	ct.History = append(ct.History, ownEntry)
	a.openHistoryEntryFull(ownEntry)
	tr.Frame()
	if !tr.HasText("你。10月31日。s1") || !tr.HasText(ownEntry.Text) {
		t.Fatalf("Chinese full history own speaker or raw text missing: %v", tr.Texts())
	}
	a.closeContactHistoryFull()
	tr.Frame()
	a.closeDetail()
	a.openContactProfile(ct.ID)
	tr.Frame()
	if !tr.HasText("联系人资料") || !tr.HasText("用途") || !tr.HasText("指令") ||
		!tr.HasText("工作目录") || !tr.HasText("编辑完整资料") {
		t.Fatalf("Chinese contact profile missing: %v", tr.Texts())
	}
	a.closeDetail()
	tr.Frame()
	if !tr.HasText("联系人") || !tr.HasText("Saki 的输入框") || !tr.HasText("还没有消息") {
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
	if last.Name != "新联系人 1" {
		t.Fatalf("new contact name = %q, want the frame language", last.Name)
	}
	if demo.contacts[0].Name != "Saki" {
		t.Fatalf("existing contact name was rewritten: %q", demo.contacts[0].Name)
	}
}

func TestConnectionAndModelFeedbackFollowLocale(t *testing.T) {
	a, ct := workspaceTestApp(t)
	config := connectionConfig{Version: 1, API: connectionAPIChatCompletions, Endpoint: "https://provider.example.invalid/v1", APIKey: "fixture", Model: "fixture", ContextTokens: 131072, OutputTokens: 8192, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
	a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
	a.connectionSaved = &config
	a.connectionEffective = &config
	a.rt = &runtimeClient{}
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	a.runtimeStatus = runtimeStateReady
	a.connectionForm.Status = connStatusApplied
	a.connectionForm.Errors = map[string]string{"Endpoint": connectionValidationEndpointURL}
	ct.ActiveTaskID = "active-task"
	ct.SendState = sendAccepted
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Applied. New work uses this connection.") ||
		!tr.HasText("Wait for all chat and task work to finish before applying a new connection.") ||
		!tr.HasText("Enter an absolute http(s) URL with a host.") {
		t.Fatalf("English connection feedback missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("已应用。新工作使用此连接。") ||
		!tr.HasText("请等待所有对话和事项工作结束后再应用新连接。") ||
		!tr.HasText("请输入带主机的绝对 http(s) URL。") {
		t.Fatalf("Chinese connection feedback missing: %v", tr.Texts())
	}
	if a.connectionForm.Draft.Endpoint != config.Endpoint {
		t.Fatalf("locale switch changed the connection draft: %q", a.connectionForm.Draft.Endpoint)
	}

	model, _ := workspaceTestApp(t)
	model.modelForm = newModelSelection(selectionTarget(), selectionCatalog())
	model.aux = "model"
	modelTr := ui.NewTester(model.view, 1040, 720)
	modelTr.SetLocale("zh-Hans")
	modelTr.Frame()
	if !modelTr.HasText("会话模型") || !modelTr.HasText("提供方模型 ID") ||
		!modelTr.HasText("刷新") || !modelTr.HasText("使用运行时目录") {
		t.Fatalf("Chinese model panel labels missing: %v", modelTr.Texts())
	}
	modelTr.SetLocale("en")
	modelTr.Frame()
	if !modelTr.HasText("Session model") || !modelTr.HasText("Provider model ID") ||
		!modelTr.HasText("Refresh") || !modelTr.HasText("Use Runtime catalog") {
		t.Fatalf("English model panel labels did not return: %v", modelTr.Texts())
	}

	form := model.modelForm
	form.Draft.Model = ""
	if _, ok := form.Request(); ok {
		t.Fatal("empty model ID was accepted")
	}
	modelTr.SetLocale("zh-Hans")
	modelTr.Frame()
	if !modelTr.HasText("请输入提供方模型 ID。") || modelTr.HasText("Enter the provider model ID.") {
		t.Fatalf("Chinese model-required validation missing: %v", modelTr.Texts())
	}
	form.Draft.Model = "provider-model"
	form.Draft.CapacityMode = modelCapacityExplicit
	form.Draft.ContextTokens, form.Draft.OutputTokens = "0", "10"
	form.Request()
	modelTr.Frame()
	if !modelTr.HasText("上下文 token 数必须是正整数。") {
		t.Fatalf("Chinese context validation missing: %v", modelTr.Texts())
	}
	form.Draft.ContextTokens, form.Draft.OutputTokens = "10", "0"
	form.Request()
	modelTr.Frame()
	if !modelTr.HasText("输出 token 数必须是正整数。") {
		t.Fatalf("Chinese output validation missing: %v", modelTr.Texts())
	}
	form.Draft.ContextTokens, form.Draft.OutputTokens = "10", "20"
	form.Request()
	modelTr.Frame()
	if !modelTr.HasText("上下文 token 数必须大于输出 token 数。") {
		t.Fatalf("Chinese context order validation missing: %v", modelTr.Texts())
	}
	form.Draft.CapacityMode = modelCapacityCatalog
	form.Draft.CatalogID = ""
	form.Request()
	modelTr.Frame()
	if !modelTr.HasText("请选择运行时目录条目或设置显式限制。") {
		t.Fatalf("Chinese catalog validation missing: %v", modelTr.Texts())
	}
	targetSession := form.Target.Config.SessionID
	form.Draft.CapacityMode = modelCapacityExplicit
	form.Draft.ContextTokens, form.Draft.OutputTokens = "131072", "8192"
	form.Target.Config.SessionID = ""
	form.Request()
	modelTr.Frame()
	if !modelTr.HasText("请先选择会话目标。") {
		t.Fatalf("Chinese session-target validation missing: %v", modelTr.Texts())
	}
	form.Target.Config.SessionID = targetSession
	form.Unconfirmed = true
	form.Request()
	modelTr.Frame()
	if !modelTr.HasText("更新模型前请刷新运行时配置。当前生效值尚未确认。") {
		t.Fatalf("Chinese unconfirmed validation missing: %v", modelTr.Texts())
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

	a.runtimeStage = "Runtime helper"
	a.runtimeErrKind = runtimeNoticeHelperMissing
	a.runtimeHintKind = startupHintHelperInstall
	a.runtimeHintArgs = []string{"/Applications/Saki.app/Contents/MacOS/Saki-runtime, /usr/local/bin/Saki-runtime"}
	tr.Frame()
	if !tr.HasText("运行时辅助进程：未找到运行时辅助进程。") ||
		!tr.HasText("请将 Saki-runtime 放在 Saki 可执行文件旁（预期：/Applications/Saki.app/Contents/MacOS/Saki-runtime, /usr/local/bin/Saki-runtime），或设置 SAKI_RUNTIME_BIN，然后重启 Nagi。") {
		t.Fatalf("Chinese helper feedback missing: %v", tr.Texts())
	}
}

func TestIssueWorkLabelsFollowLocaleAndKeepTargets(t *testing.T) {
	a := issuePreviewApp("expanded")
	a.windowWidth = 1040
	ct := a.selectedContact()
	issue := ct.Issues[0]
	nav := ct.issueNavigation()
	nav.Filter = issueFilterAll
	nav.DetailID = ""
	a.setWorkspaceOpen(true)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("New task") || !tr.HasText("No tasks in All tasks.") && !tr.HasText(issue.Goal) {
		t.Fatalf("English issue work labels missing: %v", tr.Texts())
	}
	tr.Scroll(300, 300, 0, 600)
	tr.Frame()
	scrollBefore := nav.OverviewScroll.Y
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("新建事项") || !tr.HasText(issue.Goal) {
		t.Fatalf("Chinese issue work labels or raw goal missing: %v", tr.Texts())
	}
	if nav.Filter != issueFilterAll || nav.OverviewScroll.Y != scrollBefore {
		t.Fatalf("locale switch changed issue filter/scroll: %+v", nav)
	}

	a.issueFormOpen = true
	a.issueGoal = "keep this goal"
	a.issueRequirements = "keep this requirement"
	tr.Frame()
	if !tr.HasText("目标") || !tr.HasText("要求") || a.issueGoal != "keep this goal" || a.issueRequirements != "keep this requirement" {
		t.Fatalf("Chinese issue form labels or drafts missing: %v", tr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Goal") || !tr.HasText("Requirements") || a.issueGoal != "keep this goal" || a.issueRequirements != "keep this requirement" {
		t.Fatalf("English form did not return or drafts changed: %v", tr.Texts())
	}

	a.issueFormOpen = false
	nav.DetailID = issue.ID
	a.issueDetailOpen = map[string]bool{issue.ID: true}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("执行历史") || !tr.HasText("要求") || !tr.HasText(issue.Goal) {
		t.Fatalf("Chinese issue detail labels or raw goal missing: %v", tr.Texts())
	}
	if nav.DetailID != issue.ID || nav.Filter != issueFilterAll || ct.Issues[0].Goal != issue.Goal {
		t.Fatalf("locale switch changed the issue target or stored goal: %+v", nav)
	}
	workApp, workContact := workspaceTestApp(t)
	workApp.workspaceOpen = false
	workContact.Status = statusWorking
	workContact.Work, workContact.WorkArg = workNoticeRunningTool, "read"
	workTr := ui.NewTester(workApp.view, 1040, 720)
	workTr.SetLocale("zh-Hans")
	workTr.Frame()
	if !workTr.HasText("正在运行 read") {
		t.Fatalf("Chinese work progress missing: %v", workTr.Texts())
	}
	workTr.SetLocale("en")
	workTr.Frame()
	if !workTr.HasText("Running read") {
		t.Fatalf("English work progress did not return: %v", workTr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Run history") || tr.HasText("执行历史") {
		t.Fatalf("English issue detail did not return: %v", tr.Texts())
	}
}

func TestTaskContextAndResultLabelsFollowLocale(t *testing.T) {
	tc := &taskContext{}
	item, err := tc.AddSnippet("Release notes", "Line one")
	if err != nil {
		t.Fatal(err)
	}
	tc.previewID = item.ID
	tr := taskContextTester(tc, false)
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("资料") || !tr.HasText("添加片段") || !tr.HasText("命名片段") ||
		!tr.HasText("预览") || !tr.HasText("编辑") || !tr.HasText("移除") || !tr.HasText("Release notes") {
		t.Fatalf("Chinese task context labels or raw name missing: %v", tr.Texts())
	}
	tc.cancelForm()
	tc.openSnippetForm()
	tr.Frame()
	if !tr.HasText("名称") || !tr.HasText("文本") || !tr.HasText("保存资料片段") {
		t.Fatalf("Chinese task context form labels missing: %v", tr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Task context") || !tr.HasText("Add snippet") || !tr.HasText("Save snippet") {
		t.Fatalf("English task context labels did not return: %v", tr.Texts())
	}

	missing := &taskContext{}
	missing.openFileForm()
	missing.formPath = "/definitely/missing/context.txt"
	missing.saveForm()
	missingTr := taskContextTester(missing, false)
	missingTr.SetLocale("zh-Hans")
	missingTr.Frame()
	if !missingTr.HasText("无法读取资料文件：open /definitely/missing/context.txt") {
		t.Fatalf("task context read detail was lost: %v", missingTr.Texts())
	}

	state := &issueResultPanelState{
		RunNumber: 1, RunGoal: "Ship results", RunStatus: issueExecutionCompleted,
		Response: "Recorded result body.",
		Files:    []issueResultFile{{Path: "/tmp/result.md", WorkingDirectory: "/tmp", ToolName: "write", CallID: "call-1"}},
	}
	resultTr := issueResultPanelTester(state, issueResultPanelActions{}, 1040, 720, false)
	resultTr.SetLocale("zh-Hans")
	resultTr.Frame()
	if !resultTr.HasText("执行结果") || !resultTr.HasText("报告的文件") || !resultTr.HasText("读取文件 →") ||
		!resultTr.HasText("Recorded result body.") || !resultTr.HasText("/tmp/result.md") {
		t.Fatalf("Chinese result labels or raw content missing: %v", resultTr.Texts())
	}
	resultTr.SetLocale("en")
	resultTr.Frame()
	if !resultTr.HasText("Run results") || !resultTr.HasText("Reported files") || !resultTr.HasText("Read file →") {
		t.Fatalf("English result labels did not return: %v", resultTr.Texts())
	}
}

func TestIssueFormNoticeFollowsLocaleAndClearsOnSuccess(t *testing.T) {
	a := issuePreviewApp("expanded")
	a.windowWidth = 1040
	a.selectedContact().issueNavigation().DetailID = ""
	a.issueDetailOpen = nil
	a.openNewIssueForm()
	a.issueGoal = ""
	a.saveIssueForm()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("请输入事项目标。") || tr.HasText("Enter a task goal.") {
		t.Fatalf("Chinese issue form notice missing: %v", tr.Texts())
	}
	if a.issueFormNotice != "enter-goal" || a.issueFormError != "Enter an issue goal." {
		t.Fatalf("issue form notice state = %q/%q", a.issueFormNotice, a.issueFormError)
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Enter a task goal.") || a.issueFormError != "Enter an issue goal." {
		t.Fatalf("English form notice or raw tool error changed: %v / %q", tr.Texts(), a.issueFormError)
	}
	a.issueGoal = "Locale form success"
	a.saveIssueForm()
	tr.Frame()
	if tr.HasText("Enter a task goal.") || a.issueFormNotice != "" || a.issueFormError != "" {
		t.Fatalf("success did not clear the issue notice: %v", tr.Texts())
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.store = &contactStore{path: filepath.Join(blocked, "contacts.json")}
	a.openNewIssueForm()
	a.issueGoal = "Target that cannot be saved"
	a.saveIssueForm()
	if a.issueFormError == "" || a.issueFormNotice != "save-issue-failed" ||
		!strings.Contains(a.issueFormError, "not a directory") {
		t.Fatalf("next raw failure lost its raw bytes or kind: %q / %q", a.issueFormNotice, a.issueFormError)
	}
	rendered := issueNoticeText(locales.In("zh-Hans"), a.issueFormNotice, a.issueFormNoticeDetail)
	if !strings.Contains(rendered, "无法保存此事项：") || strings.Contains(rendered, "请输入事项目标") {
		t.Fatalf("next raw failure renderer kept the old notice: %q", rendered)
	}
	a.store = nil
	a.cancelIssueForm()
	tr.SetLocale("en")
	tr.Frame()
	if tr.HasText("Enter a task goal.") {
		t.Fatalf("language switch revived the cleared notice: %v", tr.Texts())
	}
	found := false
	for _, issue := range a.selectedContact().Issues {
		if issue.Goal == "Locale form success" {
			found = true
		}
	}
	if !found {
		t.Fatal("successful form did not create the raw goal")
	}
}

func TestIssueFormSwitchNoticeReplacesOldAndClears(t *testing.T) {
	a := issuePreviewApp("expanded")
	a.windowWidth = 1040
	a.selectedContact().issueNavigation().DetailID = ""
	a.issueDetailOpen = nil
	a.openNewIssueForm()
	a.issueGoal = ""
	a.saveIssueForm()
	if a.issueFormNotice != "enter-goal" {
		t.Fatalf("initial notice = %q", a.issueFormNotice)
	}
	a.issueGoal = "keep this draft"
	if a.leaveIssueFormForSwitch() {
		t.Fatal("form switch was accepted with a draft")
	}
	if a.issueFormNotice != "form-switch" || a.issueFormError != "Finish or cancel the current issue form before switching contacts." {
		t.Fatalf("switch notice = %q / %q", a.issueFormNotice, a.issueFormError)
	}
	rendered := issueNoticeText(locales.In("zh-Hans"), a.issueFormNotice, a.issueFormNoticeDetail)
	if !strings.Contains(rendered, "切换联系人前请完成或取消当前事项表单。") || strings.Contains(rendered, "请输入事项目标") {
		t.Fatalf("switch notice renderer = %q", rendered)
	}
	a.issueGoal = ""
	a.issueRequirements = ""
	if !a.leaveIssueFormForSwitch() {
		t.Fatal("empty form did not leave")
	}
	if a.issueFormError != "" || a.issueFormNotice != "" || a.issueFormNoticeDetail != "" {
		t.Fatalf("leave did not clear the form notice: %q/%q/%q", a.issueFormError, a.issueFormNotice, a.issueFormNoticeDetail)
	}
}

func TestSendAndStatusNoticesFollowLocaleWithoutChangingRaw(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t1"
	ct.SendState = sendAccepted
	ct.Status = statusWorking
	if w := a.ensureWork(ct, "t1", "m1"); w != nil {
		w.State = workRunning
	}
	a.clearActiveTasksAfterExit()
	if ct.StatusDetailKind != "status-connection-lost" || ct.SendErrorKind != "send-delivery" {
		t.Fatalf("exit kinds = %q / %q", ct.StatusDetailKind, ct.SendErrorKind)
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText(ct.SendError) || !tr.HasText(ct.StatusDetail) {
		t.Fatalf("English send/status notices missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("交付/结果未确认。再次发送前请重新加载。") ||
		!tr.HasText("任务完成前连接已断开；请重新加载核实。") {
		t.Fatalf("Chinese send/status notices missing: %v", tr.Texts())
	}
	if ct.SendError != "Delivery/result not confirmed. Reload before sending again." ||
		ct.StatusDetail != "Connection lost before the task finished; reload to verify." {
		t.Fatal("locale switch changed the canonical send/status raw bytes")
	}
}

func TestCancelFailureNoticeReplacesRequestAndClears(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t1"
	ct.SendState = sendAccepted
	ct.setSendError("Cancellation requested", "send-cancelled", "")
	a.applyCancelResult(ct.ID, "t1", "requested", errors.New("backend down"))
	if ct.SendErrorKind != "send-cancel-failed" || ct.SendErrorDetail != "backend down" {
		t.Fatalf("cancel failure = %q / %q", ct.SendErrorKind, ct.SendErrorDetail)
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("取消失败：backend down") || tr.HasText("已请求取消") {
		t.Fatalf("Chinese cancel failure missing or stale: %v", tr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Cancel failed: backend down") || tr.HasText("Cancellation requested") {
		t.Fatalf("English cancel failure missing or stale: %v", tr.Texts())
	}
	ct.setSendError("raw provider 500", "", "")
	tr.Frame()
	if !tr.HasText("raw provider 500") || tr.HasText("Cancel failed: backend down") {
		t.Fatalf("next raw failure revived the old kind: %v", tr.Texts())
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
	if a.sendRuntimeForIssue(ct, "blocked", "") {
		t.Fatal("archived send was accepted")
	}
	if ct.SendError != "This contact is archived. Restore it before sending a message." || ct.SendErrorKind != blockArchivedSend {
		t.Fatalf("send guard raw/kind = %q / %q", ct.SendError, ct.SendErrorKind)
	}
	issue := &productIssue{ID: "issue-guard", ContactID: ct.ID, Goal: "guard", Status: issueStatusInProgress}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	if a.issueRunCapturedWithOperation(ct, issue, "") {
		t.Fatal("archived issue run was accepted")
	}
	if issue.Work.SendError != "This contact is archived. Restore it before starting issue work." || issue.Work.SendErrorKind != blockArchivedIssue {
		t.Fatalf("issue guard raw/kind = %q / %q", issue.Work.SendError, issue.Work.SendErrorKind)
	}
	if a.issueFormError != "This contact is archived. Restore it before starting issue work." || a.issueFormNotice != blockArchivedIssue {
		t.Fatalf("issue form guard raw/kind = %q / %q", a.issueFormError, a.issueFormNotice)
	}
}

func TestMissingQuestionPageFollowsLocale(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{
		ID: "issue-question", ContactID: ct.ID, Goal: "raw goal", Status: issueStatusWaitingUser,
		Questions: []*issueQuestion{{Ref: "q-1", Text: "", State: issueQuestionOpen}},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.issueNavigation().DetailID = issue.ID
	a.issueDetailOpen = map[string]bool{issue.ID: true}
	a.setWorkspaceOpen(true)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("等待中的问题不可用。") || !tr.HasText("回答前请重新加载此事项。问题文本没有记录。") ||
		!tr.HasText("重新加载事项") || tr.HasText("Answer question") {
		t.Fatalf("Chinese missing-question page missing or answer still offered: %v", tr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("The waiting question is unavailable.") || !tr.HasText("The question text was not recorded.") {
		t.Fatalf("English missing-question page missing: %v", tr.Texts())
	}
}

func TestOverviewScrollAndRealTargetsSurviveLocaleSwitch(t *testing.T) {
	a := issuePreviewApp("issue-overview")
	a.windowWidth = 1040
	ct := a.selectedContact()
	issue := ct.Issues[0]
	const (
		rawGoal        = "Preserve the original overview goal across the language switch"
		rawRequirement = "Keep the stored requirement preview unchanged"
		rawContextName = "Raw context note"
		rawContextText = "Raw context body that must stay untranslated."
		rawFollowUp    = "Raw follow-up text that must stay untranslated."
	)
	issue.Goal = rawGoal
	issue.Requirements = parseIssueRequirements(rawRequirement)
	if _, err := issue.Context.AddSnippet(rawContextName, rawContextText); err != nil {
		t.Fatal(err)
	}
	ledger := newIssueSupplementLedger()
	if _, err := ledger.Add(issueSupplementTargetFor(ct, issue), rawFollowUp, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	issue.Supplements = ledger
	ct.SelectedIssueID = ""
	ct.Draft = "keep the overview conversation draft"
	nav := ct.issueNavigation()
	nav.DetailID = ""
	nav.Filter = issueFilterAll
	nav.OverviewScroll.Y = 0
	for index := 0; index < 24; index++ {
		ct.Issues = append(ct.Issues, &productIssue{
			ID:        fmt.Sprintf("issue-overview-%02d", index),
			ContactID: ct.ID, Status: issueStatusInProgress,
			Goal:          fmt.Sprintf("Overview row %02d keeps its own stable target", index+1),
			Requirements:  parseIssueRequirements("Do not change this row's target."),
			WorkSessionID: "issue-preview-overview", RequirementsRevision: 1,
		})
	}
	a.setWorkspaceOpen(true)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("New task") || !tr.HasText("All") || !tr.HasText(rawGoal) {
		t.Fatalf("English overview scene missing: %v", tr.Texts())
	}
	detailX := float32(a.layoutWidth()) - a.effectiveDetailWidth()/2
	tr.Scroll(detailX, 300, 0, 300)
	tr.Frame()
	firstY := nav.OverviewScroll.Y
	if firstY <= 0 {
		t.Fatalf("overview did not scroll inside the detail pane: %v", firstY)
	}
	tr.Scroll(detailX, 300, 0, 120)
	tr.Frame()
	middleY := nav.OverviewScroll.Y
	if middleY <= firstY {
		t.Fatalf("overview scroll is at its maximum, not a middle position: %v -> %v", firstY, middleY)
	}

	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("新建事项") || !tr.HasText("未完成") || !tr.HasText("已完成") || !tr.HasText("全部") {
		t.Fatalf("Chinese overview labels missing: %v", tr.Texts())
	}
	if nav.OverviewScroll.Y != middleY || nav.Filter != issueFilterAll || ct.SelectedIssueID != "" || nav.DetailID != "" || a.selectedContact() != ct {
		t.Fatalf("locale switch changed overview target/scroll: scroll=%v/%v filter=%q selected=%q detail=%q", middleY, nav.OverviewScroll.Y, nav.Filter, ct.SelectedIssueID, nav.DetailID)
	}
	if issue.Goal != rawGoal || issueRequirementsText(issue) != rawRequirement {
		t.Fatalf("locale switch changed stored goal/requirements: %q / %q", issue.Goal, issueRequirementsText(issue))
	}
	if items := issue.Context.snapshot(); len(items) != 1 || items[0].Name != rawContextName || items[0].Text != rawContextText {
		t.Fatalf("locale switch changed the stored context: %+v", items)
	}
	if got := issue.Supplements.pendingText(issueSupplementTargetFor(ct, issue)); got != rawFollowUp {
		t.Fatalf("locale switch changed the stored follow-up: %q", got)
	}

	tr.SetLocale("en")
	tr.Frame()
	if nav.OverviewScroll.Y != middleY || nav.Filter != issueFilterAll || ct.SelectedIssueID != "" || nav.DetailID != "" {
		t.Fatalf("switching back changed overview target/scroll: scroll=%v/%v filter=%q selected=%q detail=%q", middleY, nav.OverviewScroll.Y, nav.Filter, ct.SelectedIssueID, nav.DetailID)
	}
	if !tr.HasText("New task") || !tr.HasText("All") {
		t.Fatalf("English overview labels did not return: %v", tr.Texts())
	}

	// The real detail target still owns the preserved raw material after the
	// overview interaction.
	openIssueDetailForTest(a, ct, issue.ID)
	a.issueDetailOpen = map[string]bool{issue.ID: true}
	a.issueSupplementFormID = issue.ID
	*a.issueSupplementDraft(issue.ID) = "keep the follow-up form draft"
	issue.Context.openSnippetForm()
	issue.Context.formName = "keep the context form name"
	issue.Context.formText = "keep the context form text"
	tr.Frame()
	if !tr.HasText(rawGoal) || !tr.HasText(rawRequirement) {
		t.Fatalf("opened detail lost raw goal/requirements: %v", tr.Texts())
	}
	if a.issueSupplementDraft(issue.ID) == nil || *a.issueSupplementDraft(issue.ID) != "keep the follow-up form draft" ||
		!issue.Context.formOpen || issue.Context.formName != "keep the context form name" || issue.Context.formText != "keep the context form text" {
		t.Fatalf("detail drafts were not initialized: follow-up=%q context=%v/%q/%q", *a.issueSupplementDraft(issue.ID), issue.Context.formOpen, issue.Context.formName, issue.Context.formText)
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if *a.issueSupplementDraft(issue.ID) != "keep the follow-up form draft" ||
		issue.Context.formName != "keep the context form name" || issue.Context.formText != "keep the context form text" || !issue.Context.formOpen {
		t.Fatal("locale switch changed the detail form drafts")
	}
}

func TestChatDetailAndReaderScrollSurviveLocaleSwitch(t *testing.T) {
	a := issuePreviewApp("issue-detail")
	a.windowWidth = 1040
	ct := a.selectedContact()
	issue := ct.Issues[0]
	var requirements strings.Builder
	for index := 0; index < 30; index++ {
		fmt.Fprintf(&requirements, "Keep requirement line %02d visible and stable across language changes.\n", index+1)
	}
	issue.Requirements = parseIssueRequirements(requirements.String())
	ct.Draft = "keep the chat draft"
	ct.SelectedIssueID = issue.ID
	nav := ct.issueNavigation()
	nav.Filter = issueFilterAll
	nav.DetailID = issue.ID
	for index := 0; index < 40; index++ {
		ct.Messages = append(ct.Messages, &message{
			Key:  fmt.Sprintf("scroll-message-%02d", index),
			Kind: msgContact,
			Text: fmt.Sprintf("Visible chat message %02d that keeps its rendered row while the language changes.", index+1),
		})
	}
	cfg := connectionConfig{Version: 1, API: connectionAPIChatCompletions, Endpoint: "https://provider.example.invalid/v1/very/long/endpoint", APIKey: "fixture", Model: "fixture-provider-model", ContextTokens: 131072, OutputTokens: 8192, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
	a.connectionForm = newConnectionSettings(&cfg, cfg.WorkingDirectory)
	a.connectionSaved = &cfg
	a.connectionEffective = &cfg
	a.workspaceOpen = true
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	chatX := float32(0)
	if a.avatarRailActive() {
		chatX = avatarRailWidth + paneSeparatorWidth
	}
	chatX += a.centerPaneWidth / 2
	tr.Scroll(chatX, 300, 0, -600)
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
	detailX := float32(a.layoutWidth()) - a.effectiveDetailWidth()/2
	tr.Scroll(detailX, 350, 0, 300)
	tr.Frame()
	detailY := nav.DetailScroll.Y
	if detailY <= 0 {
		t.Fatalf("detail did not scroll inside the detail pane: %v", detailY)
	}
	if !tr.HasText("Requirements") {
		t.Fatalf("English detail scene missing: %v", tr.Texts())
	}

	tr.SetLocale("zh-Hans")
	tr.Frame()
	nextFirst, nextLast := ct.list.Visible()
	if nextFirst != first || nextLast != last || ct.Messages[nextFirst].Key != anchorKey || !tr.HasText(anchorText) {
		t.Fatalf("locale switch moved the chat anchor: first=%d->%d last=%d->%d key=%q", first, nextFirst, last, nextLast, anchorKey)
	}
	if nav.DetailScroll.Y != detailY || nav.DetailID != issue.ID || nav.Filter != issueFilterAll || ct.SelectedIssueID != issue.ID || ct.Draft != "keep the chat draft" {
		t.Fatalf("locale switch changed detail target/scroll: y=%v/%v detail=%q filter=%q selected=%q draft=%q", detailY, nav.DetailScroll.Y, nav.DetailID, nav.Filter, ct.SelectedIssueID, ct.Draft)
	}
	if a.connectionForm.Draft.Endpoint != cfg.Endpoint || a.connectionForm.Draft.Model != cfg.Model || a.connectionSaved.Model != cfg.Model || a.connectionEffective.Endpoint != cfg.Endpoint {
		t.Fatalf("locale switch changed stable connection values: %+v", a.connectionForm.Draft)
	}
	if !tr.HasText("要求") || !tr.HasText("回到最新") {
		t.Fatalf("Chinese detail/chat labels missing: %v", tr.Texts())
	}

	tr.SetLocale("en")
	tr.Frame()
	nextFirst, nextLast = ct.list.Visible()
	if nextFirst != first || ct.Messages[nextFirst].Key != anchorKey || !tr.HasText(anchorText) || nav.DetailScroll.Y != detailY {
		t.Fatalf("switching back moved the chat/detail scroll: first=%d last=%d y=%v", nextFirst, nextLast, nav.DetailScroll.Y)
	}
	if !tr.HasText("Requirements") || !tr.HasText("Back to latest") {
		t.Fatalf("English detail/chat labels did not return: %v", tr.Texts())
	}

	execution := issue.Executions[0]
	file := issueResultFile{ExecutionID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID, MessageID: execution.MessageID, CallID: "call-final-reader", ToolName: "write", Path: "/tmp/final-reader.txt", WorkingDirectory: "/tmp", RawResult: `{"path":"/tmp/final-reader.txt","bytes_written":1}`}
	key := issueResultFileKey(file)
	longPreview := strings.Repeat("A long saved file line that must scroll independently in the reader.\n", 120)
	a.aux = "issue-result"
	a.issueResult = issueResultPage{
		Open: true, OwnerContactID: ct.ID, IssueID: issue.ID, ExecutionID: execution.ID,
		Panel: issueResultPanelState{
			RunNumber: 1, RunStatus: issueExecutionCompleted, Files: []issueResultFile{file},
			Inspection: &issueResultFileInspection{Exists: true, Regular: true, Preview: longPreview},
			InspectFor: key, ReaderKey: key,
		},
	}
	tr.Frame()
	tr.Scroll(520, 400, 0, 1200)
	tr.Frame()
	readerY := a.issueResult.Panel.ReaderScroll.Y
	if readerY <= 0 {
		t.Fatalf("reader did not scroll: %v", readerY)
	}
	if !tr.HasText("Check again") || !tr.HasText("← Back to results") {
		t.Fatalf("English reader scene missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if a.issueResult.Panel.ReaderScroll.Y != readerY || !tr.HasText("再次检查") || !tr.HasText("← 返回结果") || !tr.HasText("当前文件：内容可能与此执行不同") {
		t.Fatalf("locale switch changed the reader: y=%v label=%v", a.issueResult.Panel.ReaderScroll.Y, tr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	if a.issueResult.Panel.ReaderScroll.Y != readerY || !tr.HasText("Check again") || !tr.HasText("← Back to results") {
		t.Fatalf("switching back changed the reader: y=%v", a.issueResult.Panel.ReaderScroll.Y)
	}
}

func TestSelectedTextSurvivesLocaleSwitchInContactField(t *testing.T) {
	a, ct := workspaceTestApp(t)
	other := &contact{ID: "contact-b", Name: "B contact", Role: "Assistant", Status: statusReady, Loaded: true, Workspace: t.TempDir()}
	a.contacts = append(a.contacts, other)
	a.ensureSettingsProfileEditor(ct.ID).Draft.Purpose = "keep A purpose"
	a.ensureSettingsProfileEditor(ct.ID).Draft.Instructions = "keep A instructions"
	a.ensureSettingsProfileEditor(other.ID).Draft.Purpose = "keep B purpose"
	ct.Draft = "keep conversation draft"
	issue := &productIssue{ID: "issue-select", ContactID: ct.ID, Goal: "keep raw goal", Status: issueStatusInProgress,
		RequirementsRevision: 1, Requirements: parseIssueRequirements("keep raw requirement"),
		WorkSessionID: "work-select", Work: issueWorkSession{Loaded: true}, Supplements: newIssueSupplementLedger()}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	issue.Context.openSnippetForm()
	issue.Context.formName = "keep context form name"
	issue.Context.formText = "keep context form text"
	a.issueSupplementFormID = issue.ID
	*a.issueSupplementDraft(issue.ID) = "keep follow-up form draft"
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	a.settingsContactID = ct.ID
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Contact purpose") || a.settingsContactEditor().Draft.Purpose != "keep A purpose" {
		t.Fatalf("contact profile purpose is not visible: %v", tr.Texts())
	}
	if err := tr.Click("Contact name"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Contact name") {
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
	if got := a.settingsContactEditor().Draft.Name; got != "替换" {
		t.Fatalf("typing after the locale switch did not replace the selection: %q", got)
	}
	if ct.Name != originalName {
		t.Fatalf("typing into the profile draft changed the saved contact: %q", ct.Name)
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.Focused("Contact name") {
		t.Fatalf("switching back lost the contact field focus: %v", tr.Texts())
	}
	if _, active := tr.TextCaret(); !active {
		t.Fatal("switching back lost the text caret")
	}
	if a.settingsContactEditor().Draft.Name != "替换" || a.settingsContactID != ct.ID || a.settingsTab != settingsTabContact {
		t.Fatalf("selection/focus repair changed target or draft: name=%q contact=%q tab=%q", a.settingsContactEditor().Draft.Name, a.settingsContactID, a.settingsTab)
	}
	if a.ensureSettingsProfileEditor(ct.ID).Draft.Purpose != "keep A purpose" || a.ensureSettingsProfileEditor(ct.ID).Draft.Instructions != "keep A instructions" ||
		a.ensureSettingsProfileEditor(other.ID).Draft.Purpose != "keep B purpose" {
		t.Fatal("locale switch changed A/B profile drafts")
	}
	if ct.Draft != "keep conversation draft" {
		t.Fatalf("locale switch changed the conversation draft: %q", ct.Draft)
	}
	if *a.issueSupplementDraft(issue.ID) != "keep follow-up form draft" {
		t.Fatalf("locale switch changed the follow-up draft: %q", *a.issueSupplementDraft(issue.ID))
	}
	if !issue.Context.formOpen || issue.Context.formName != "keep context form name" || issue.Context.formText != "keep context form text" {
		t.Fatalf("locale switch changed the context form draft: open=%v name=%q text=%q", issue.Context.formOpen, issue.Context.formName, issue.Context.formText)
	}
}
