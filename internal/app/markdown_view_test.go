package app

import (
	"errors"
	"fmt"
	"image"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

func appReadingTestApp(t *testing.T) (*appModel, *contact) {
	t.Helper()
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	recoveryTestStore(t, a)
	return a, ct
}

func scrollToVisibleControl(tr *ui.Tester, label string, dy float32) bool {
	composer, composerOK := tr.Find("Composer for Nagi")
	messages, messagesOK := tr.Find("Chat messages")
	if !composerOK || !messagesOK || composer.W <= 0 || composer.H <= 0 ||
		messages.W <= 0 || messages.H <= 0 {
		return false
	}
	// Scroll inside the real message viewport. The old Contact-profile
	// coordinate was a guess and no longer identifies the reading column.
	x := messages.X + messages.W/2
	y := messages.Y + messages.H/2
	for i := 0; i < 40; i++ {
		r, ok := tr.Find(label)
		if ok && r.W > 0 && r.H > 0 &&
			r.Y >= messages.Y && r.Y+r.H <= messages.Y+messages.H {
			return true
		}
		tr.Scroll(x, y, 0, dy)
		tr.Frame()
	}
	return false
}

func TestAppAssistantMarkdownStreamsFinalAndCopies(t *testing.T) {
	const md = "# 标题\n\n列表：\n\n- 第一项\n- 第二项\n\n> 引用\n\n[链接](https://example.com/docs)\n\n" +
		"```go\n\tfmt.Println(\"x\")\n```\n\n| 名称 | 值 |\n| --- | --- |\n| 语言 | 中文 |\n"

	for _, size := range []struct {
		width, height int
	}{{1040, 720}, {720, 640}} {
		a, ct, _ := piRunStartedTestApp(t)
		execution := ct.ActiveTaskID
		a.windowWidth = size.width
		ct.Messages = []*message{{
			Key: "live:" + execution + ":text:pi", Kind: msgContact, TaskID: execution, ItemID: "pi",
			Text: "# 标题\n\n", Streaming: true, liveReveal: true,
		}}
		tr := ui.NewTester(a.view, size.width, size.height)
		tr.Frame()
		if !tr.HasText(locales.In("en").BotTyping()) || tr.HasText("标题") || tr.HasText("Copy message") {
			t.Fatalf("%dx%d streaming bubble did not hold behind typing indicator: %q", size.width, size.height, tr.Texts())
		}

		applyTestProductEvent(t, a, "execution.textDelta", pi.ProductExecutionEvent{
			ConversationID: ct.SessionID, ExecutionID: execution, Offset: uint64(len("# 标题\n\n")), Text: "列表：\n\n- 第一项\n- 第二项\n\n",
		})
		tr.Frame()
		if !tr.HasText(locales.In("en").BotTyping()) || !strings.Contains(ct.Messages[0].Text, "第一项") {
			t.Fatal("the Product delta did not accumulate while holding behind typing indicator")
		}
		applyTestProductEvent(t, a, "execution.settled", pi.ProductExecutionEvent{ConversationID: ct.SessionID, ExecutionID: execution, State: "completed"})
		ct.Messages = []*message{{Key: "saved-markdown", Kind: msgContact, Text: md, History: true}}
		tr.Frame()
		for _, want := range []string{"标题", "第一项", "第二项", "引用", "链接", "fmt.Println", "名称", "中文"} {
			if !tr.HasText(want) {
				t.Fatalf("%dx%d final markdown missing %q: %q", size.width, size.height, want, tr.Texts())
			}
		}
		if tr.HasText("Copy message") {
			t.Fatalf("%dx%d outer Copy message button unexpectedly visible: %q", size.width, size.height, tr.Texts())
		}
		if !scrollToVisibleControl(tr, "标题", -160) {
			t.Fatalf("%dx%d bubble heading is not reachable for context menu", size.width, size.height)
		}
		if err := tr.RightClick("标题"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if err := choosePopoverMenuItem(tr, "Copy message"); err != nil {
			t.Fatal(err)
		}
		if got := tr.Clipboard(); got != md {
			t.Fatalf("%dx%d full message copy = %q, want exact source", size.width, size.height, got)
		}
		if tr.HasText("Back to latest") {
			if err := tr.Click("Back to latest"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
		}
		if !scrollToVisibleControl(tr, "Copy code", 160) {
			t.Fatalf("%dx%d code copy control is not visible after returning to latest: %v", size.width, size.height, tr.Texts())
		}
		if err := tr.Click("Copy code"); err != nil {
			t.Fatal(err)
		}
		if got, want := tr.Clipboard(), "\tfmt.Println(\"x\")\n"; got != want {
			t.Fatalf("%dx%d code copy = %q, want %q", size.width, size.height, got, want)
		}

	}
}

func TestAppUserMessageStaysLiteralAndCopiesRaw(t *testing.T) {
	const raw = "# 不是标题\n\n- 不是列表\n\n```\nnot code\n```"
	a, ct := appReadingTestApp(t)
	ct.Messages = []*message{{Key: "live:t1:user", Kind: msgUser, Text: raw}}
	tr := ui.NewTester(a.view, 720, 640)
	tr.Frame()
	if !tr.HasText("# 不是标题") || !tr.HasText("not code") {
		t.Fatalf("literal user text missing: %q", tr.Texts())
	}
	if tr.HasText("Copy code") {
		t.Fatalf("user text was parsed as code Markdown: %q", tr.Texts())
	}
	if _, ok := tr.Find("Table, wrapped plain text"); ok {
		t.Fatalf("user text was parsed as a Markdown table: %q", tr.Texts())
	}
	if tr.HasText("Copy message") {
		t.Fatalf("outer user Copy message button unexpectedly visible: %q", tr.Texts())
	}
	if err := tr.RightClick(raw); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := choosePopoverMenuItem(tr, "Copy message"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != raw {
		t.Fatalf("user copy = %q, want raw %q", got, raw)
	}
}

func TestAppEmptyChatHasNoBackToLatest(t *testing.T) {
	a, _ := appReadingTestApp(t)
	tr := ui.NewTester(a.view, 720, 640)
	tr.Frame()
	if tr.HasText("Back to latest") {
		t.Fatalf("empty chat showed Back to latest: %q", tr.Texts())
	}
}

func TestAppFourColumnTableAndCodeFitReadingBubble(t *testing.T) {
	md := "# 宽表\n\n| 名称 | 说明 | 标记 | 状态 |\n| --- | --- | --- | --- |\n| 中文长内容需要换行显示 | " +
		strings.Repeat("abcdefghij", 18) + " | **重点** | `code` |\n\n```go\n" +
		strings.Repeat("long-code-token-", 20) + "\n```\n"
	cells := []string{"名称", "说明", "标记", "状态", "中文长内容需要换行显示", strings.Repeat("abcdefghij", 18), "**重点**", "`code`"}
	for _, size := range []struct {
		width, height int
	}{{1040, 720}, {720, 640}} {
		a, ct := appReadingTestApp(t)
		a.windowWidth = size.width
		ct.Messages = []*message{{ID: 1, Kind: msgContact, Text: md}}
		tr := ui.NewTester(a.view, size.width, size.height)
		tr.Frame()
		for _, want := range cells {
			bounds, ok := tr.Find(want)
			if !ok {
				t.Fatalf("%dx%d table cell %q missing: %q", size.width, size.height, want, tr.Texts())
			}
			if bounds.X < 0 || bounds.X+bounds.W > float32(size.width) {
				t.Fatalf("%dx%d table cell %q overflows: %+v", size.width, size.height, want, bounds)
			}
		}
		codeLine := strings.Repeat("long-code-token-", 20)
		if bounds, ok := tr.Find(codeLine); !ok || bounds.X < 0 || bounds.X+bounds.W > float32(size.width) {
			t.Fatalf("%dx%d code line missing or overflowing: %+v ok=%v", size.width, size.height, bounds, ok)
		}
		if tr.HasText("Copy message") {
			t.Fatalf("%dx%d outer Copy message unexpectedly visible: %q", size.width, size.height, tr.Texts())
		}
		bounds, ok := tr.Find("Copy code")
		if !ok {
			t.Fatalf("%dx%d Copy code missing: %q", size.width, size.height, tr.Texts())
		}
		if bounds.X < 0 || bounds.X+bounds.W > float32(size.width) {
			t.Fatalf("%dx%d Copy code overflows: %+v", size.width, size.height, bounds)
		}
	}
}

func TestAppChatScrollUpSurvivesStreamAppendAndBackToLatest(t *testing.T) {
	a, ct := appReadingTestApp(t)
	recoveryTestStore(t, a)
	for i := 0; i < 50; i++ {
		ct.Messages = append(ct.Messages, &message{
			Key: fmt.Sprintf("m-%02d", i), Kind: msgContact,
			Text: fmt.Sprintf("message %02d\n\nwith a second line", i),
		})
	}
	ct.Messages = append(ct.Messages, &message{
		Key: "live:t1:text:pi", Kind: msgContact, TaskID: "t1", ItemID: "pi", Text: "live start", Streaming: true,
	})
	ct.ActiveTaskID, ct.SendState = "t1", sendAccepted
	a.pi = &piAppHost{}
	appendDelta := func(text string) {
		message := ct.Messages[len(ct.Messages)-1]
		applyTestProductEvent(t, a, "execution.textDelta", pi.ProductExecutionEvent{
			ConversationID: ct.SessionID, ExecutionID: "t1", Offset: uint64(len(message.Text)), Text: text,
		})
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !ct.list.AtEnd() {
		t.Fatal("chat did not start following its end")
	}
	for i := 0; i < 3 && ct.list.AtEnd(); i++ {
		tr.Scroll(500, 300, 0, -4000)
		tr.Frame()
	}
	if ct.list.AtEnd() || !tr.HasText("Back to latest") {
		t.Fatalf("scrolling up did not pause follow/back control: atEnd=%v", ct.list.AtEnd())
	}
	ct.Draft = "draft stays across appearance switch"
	first, _ := ct.list.Visible()
	composerLabel := locales.In("en").ComposerLabel(ct.Name)
	viewport, viewportOK := tr.Find(locales.In("en").ConversationMessagesLabel())
	if !viewportOK || viewport.W <= 0 || viewport.H <= 0 {
		t.Fatalf("reading viewport bounds unavailable: %+v found=%v", viewport, viewportOK)
	}
	// list.Visible may report a partially clipped first row; choose the first
	// row whose label is fully inside the actual reading viewport and clickable.
	clicked := -1
	var row ui.Rect
	for i := first; i < len(ct.Messages); i++ {
		candidate, ok := tr.Find(fmt.Sprintf("message %02d", i))
		if ok && candidate.W > 0 && candidate.H > 0 &&
			candidate.Y >= viewport.Y && candidate.Y+candidate.H <= viewport.Y+viewport.H {
			clicked, row = i, candidate
			break
		}
	}
	if clicked < 0 {
		t.Fatalf("no fully visible clickable message row from %d: viewport=%+v texts=%q", first, viewport, tr.Texts())
	}
	// Click the visible text itself (not its left edge) and verify the selected
	// source actually reaches the clipboard.
	tr.ClickAt(row.X+row.W/2, row.Y+row.H/2)
	tr.Command("selectAll")
	tr.Command("copy")
	selected := tr.Clipboard()
	if selected == "" || !strings.Contains(selected, fmt.Sprintf("message %02d", clicked)) {
		t.Fatalf("selection copy = %q, want text from visible row %02d (row=%+v)", selected, clicked, row)
	}

	// Reuse the same scrolled/selected view for the appearance regression:
	// neither the visible anchor, the exact selection, nor the current contact
	// and draft may change before the existing stream-append check.
	anchorBeforeAppearance, _ := ct.list.Visible()
	selectedIDBeforeAppearance := a.selectedContact().ID
	draftBeforeAppearance := ct.Draft
	a.setAppearanceMode(appearanceDark)
	tr.Frame()
	anchorAfterAppearance, _ := ct.list.Visible()
	if anchorAfterAppearance != anchorBeforeAppearance {
		t.Fatalf("appearance switch moved visible anchor: %d -> %d", anchorBeforeAppearance, anchorAfterAppearance)
	}
	if a.selectedContact().ID != selectedIDBeforeAppearance {
		t.Fatalf("appearance switch changed selected contact: %q -> %q", selectedIDBeforeAppearance, a.selectedContact().ID)
	}
	if ct.Draft != draftBeforeAppearance {
		t.Fatalf("appearance switch changed draft: %q -> %q", draftBeforeAppearance, ct.Draft)
	}
	if got := tr.Clipboard(); got != selected {
		t.Fatalf("appearance switch changed copied selection: %q -> %q", selected, got)
	}
	tr.SetClipboard("")
	tr.Command("copy")
	if got := tr.Clipboard(); got != selected {
		t.Fatalf("appearance switch lost the active selection before append: %q -> %q", selected, got)
	}

	firstBeforeAppend, _ := ct.list.Visible()

	appendDelta(" appended")
	tr.Frame()
	if ct.list.AtEnd() {
		t.Fatal("appending while scrolled up jumped back to the end")
	}
	afterFirst, _ := ct.list.Visible()
	if afterFirst != firstBeforeAppend {
		t.Fatalf("appending moved the visible window: %d -> %d", firstBeforeAppend, afterFirst)
	}
	tr.SetClipboard("")
	tr.Command("copy")
	if got := tr.Clipboard(); got != selected {
		t.Fatalf("append changed the earlier selection: %q -> %q", selected, got)
	}

	if err := tr.Click(composerLabel); err != nil {
		t.Fatal(err)
	}
	if !tr.Focused(composerLabel) {
		t.Fatal("composer did not take focus")
	}
	appendDelta(" more")
	tr.Frame()
	if !tr.Focused(composerLabel) {
		t.Fatal("streaming text stole composer focus")
	}

	if err := tr.Click("Back to latest"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !ct.list.AtEnd() || tr.HasText("Back to latest") {
		t.Fatal("Back to latest did not resume following")
	}
	if !tr.Focused(composerLabel) {
		t.Fatal("Back to latest did not return meaningful focus to the composer")
	}
	appendDelta(" after back")
	tr.Frame()
	if !ct.list.AtEnd() || !tr.Focused(composerLabel) {
		t.Fatal("following/focus did not persist after returning to the latest")
	}

	tr.Scroll(500, 300, 0, -4000)
	tr.Frame()
	if ct.list.AtEnd() || !tr.HasText("Back to latest") {
		t.Fatal("second scroll-up did not expose Back to latest for keyboard use")
	}
	tr.Key(ui.Shift, ui.KeyTab)
	if !tr.Focused("Back to latest") {
		t.Fatal("Back to latest is not reachable by keyboard")
	}
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if !ct.list.AtEnd() || !tr.Focused(composerLabel) {
		t.Fatal("keyboard activation of Back to latest did not resume following with composer focus")
	}
}

func TestIntegratedMarkdownPreview(t *testing.T) {
	dir := os.Getenv("NAGI_READER_INTEGRATED_DIR")
	if dir == "" {
		t.Skip("set NAGI_READER_INTEGRATED_DIR to write integrated headless previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "# 阅读\n\n段落、**重点**、[链接](https://example.com)。\n\n> 引用\n\n- 第一项\n- 第二项\n\n" +
		"```go\n" + "func long() {\n\tfmt.Println(\"wrapped at the reading width\")\n}\n" + "```\n\n" +
		"| 名称 | 说明 | 标记 | 状态 |\n| --- | --- | --- | --- |\n| 中文长内容需要换行 | " +
		strings.Repeat("abcdefghij", 18) + " | `code` | 完成 |\n"
	for _, scene := range []string{"chat", "details"} {
		for _, dark := range []bool{false, true} {
			theme := "light"
			if dark {
				theme = "dark"
			}
			for _, size := range []struct {
				name          string
				width, height int
			}{{"1040x720", 1040, 720}, {"720x640", 720, 640}} {
				a, ct := workspaceTestApp(t)
				a.windowWidth = size.width
				a.workspaceOpen = false
				ct.Draft = "Draft kept while reading"
				ct.Messages = []*message{
					{ID: 1, Kind: msgUser, Text: "# literal user text"},
					{ID: 2, Kind: msgContact, Text: md},
				}
				if scene == "details" {
					ct.Messages = append(ct.Messages, &message{
						Key: "inline-read", Kind: msgActivity, CallID: "k1", ToolName: "read", ToolArgs: `{"path":"README.md"}`,
						ToolOutput: strings.Repeat("received line\n", 80) + "# tail", Expanded: true,
					})
				}
				tr := ui.NewTester(a.view, size.width, size.height)
				if dark {
					tr.SetDark(true)
				}
				tr.Frame()
				if scene == "chat" {
					ct.list.ScrollTo(0, ui.Start)
					tr.Frame()
				}
				writePreviewPNG(t, filepath.Join(dir,
					fmt.Sprintf("integrated-%s-%s-%s.png", scene, theme, size.name)), tr.Image())
			}
		}
	}
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, dark := range []bool{false, true} {
			theme := "light"
			if dark {
				theme = "dark"
			}
			for _, size := range []struct {
				name          string
				width, height int
			}{{"1040x720", 1040, 720}, {"960x640", 960, 640}} {
				a, ct := workspaceTestApp(t)
				a.windowWidth = size.width
				a.workspaceOpen = false
				ct.Messages = []*message{
					{ID: 1, Kind: msgUser, Text: "一句话用户消息，复制入口在气泡外。"},
					{ID: 2, Kind: msgContact, Text: md},
				}
				tr := ui.NewTester(a.view, size.width, size.height)
				tr.SetLocale(locale)
				tr.SetDark(dark)
				tr.SetFocused(true)
				tr.Frame()
				ct.list.ScrollTo(0, ui.Start)
				tr.Frame()
				writePreviewPNG(t, filepath.Join(dir,
					fmt.Sprintf("message-affordance-%s-%s-%s.png", locale, theme, size.name)), tr.Image())
				copyLabel := "Copy message"
				if locale == "zh-Hans" {
					copyLabel = "复制消息"
				}
				for i := 0; i < 64 && !tr.Focused(copyLabel); i++ {
					tr.Key(0, ui.KeyTab)
					tr.Frame()
				}
				if !tr.Focused(copyLabel) {
					t.Fatalf("%s/%s/%s: user copy was not reachable by Tab", locale, theme, size.name)
				}
				tr.Frame()
				writePreviewPNG(t, filepath.Join(dir,
					fmt.Sprintf("message-affordance-user-focus-%s-%s-%s.png", locale, theme, size.name)), tr.Image())
			}
		}
	}
}

func TestConversationLocalFileLinkUsesSessionDirectory(t *testing.T) {
	a, ct := appReadingTestApp(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "native-output.md"), []byte("output"), 0600); err != nil {
		t.Fatal(err)
	}
	ct.SessionWorkingDir = root
	ct.Workspace = t.TempDir()
	ct.Messages = []*message{{ID: 1, Kind: msgContact, Text: "已写入 [native-output.md](native-output.md)。 [网页](https://example.com/docs)"}}
	ct.Draft = "keep this draft"
	var opened []string
	a.openPath = func(path string) error { opened = append(opened, path); return nil }
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	if err := tr.Click("网页"); err != nil {
		t.Fatal(err)
	}
	if got := tr.OpenedURLs(); len(got) != 1 || got[0] != "https://example.com/docs" {
		t.Fatalf("https positive control = %q", got)
	}
	if err := tr.Click("native-output.md"); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != filepath.Join(root, "native-output.md") {
		t.Fatalf("file opener = %q, want one request for session directory %q", opened, root)
	}
	tabToFocused(t, tr, "native-output.md")
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if len(opened) != 2 || opened[1] != filepath.Join(root, "native-output.md") {
		t.Fatalf("Enter file opener = %q", opened)
	}
	if ct.Draft != "keep this draft" {
		t.Fatalf("link changed draft: %q", ct.Draft)
	}
}

func TestConversationFileLinkOwnersAndErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	a, first := appReadingTestApp(t)
	roots := []string{t.TempDir(), t.TempDir()}
	const name = "中文 计划.md"
	for _, root := range roots {
		if err := os.WriteFile(filepath.Join(root, name), []byte(root), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first.SessionWorkingDir, first.Draft = roots[0], "draft A"
	first.Context.Items = []taskContextItem{{ID: "material", Name: "notes", Text: "preserve attachment"}}
	second := &contact{ID: "second", Name: "Other", SessionID: "s2", Loaded: true, SessionWorkingDir: roots[1], Draft: "draft B"}
	text := "查看[本地](" + url.PathEscape(name) + ")。"
	first.Messages = []*message{{ID: 1, Kind: msgContact, Text: text}}
	second.Messages = []*message{{ID: 2, Kind: msgContact, Text: text}}
	a.contacts = append(a.contacts, second)
	var opened []string
	a.openPath = func(path string) error { opened = append(opened, path); return nil }
	tr := ui.NewTester(a.view, 1040, 720)
	if err := tr.Click("本地"); err != nil {
		t.Fatal(err)
	}
	a.selectContact(1)
	tr.Frame()
	if err := tr.Click("本地"); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 2 || opened[0] != filepath.Join(roots[0], name) || opened[1] != filepath.Join(roots[1], name) {
		t.Fatalf("cross-contact paths = %q", opened)
	}
	if first.Draft != "draft A" || second.Draft != "draft B" || len(first.Context.Items) != 1 || first.Context.Items[0].Text != "preserve attachment" {
		t.Fatal("link changed drafts or attached material")
	}
	tr.Frame()
	if len(opened) != 2 {
		t.Fatalf("frame retried file open: %q", opened)
	}

	for _, tc := range []struct {
		name, base, target, want string
		openerErr                error
		calls                    int
	}{
		{name: "unknown source", target: url.PathEscape(name), want: "source session working directory is unknown"},
		{name: "missing", base: roots[0], target: "missing.md", want: "No file exists at the reported path"},
		{name: "opener failure", base: roots[0], target: url.PathEscape(name), want: "fixture opener failed", openerErr: errors.New("fixture opener failed"), calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, ct := appReadingTestApp(t)
			ct.SessionWorkingDir, ct.Workspace = tc.base, roots[1]
			ct.Messages = []*message{{ID: 1, Kind: msgContact, Text: "查看[文件](" + tc.target + ")。"}}
			count := 0
			app.openPath = func(string) error { count++; return tc.openerErr }
			tester := ui.NewTester(app.view, 1040, 720)
			if err := tester.Click("文件"); err != nil {
				t.Fatal(err)
			}
			tester.Frame()
			if count != tc.calls || !tester.HasText(tc.want) {
				t.Fatalf("calls=%d texts=%q", count, tester.Texts())
			}
			tester.Frame()
			if count != tc.calls {
				t.Fatal("error caused an automatic retry")
			}
		})
	}
}

func TestSaveFailureAppearanceKeepsReaderSelection(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			a, ct := appReadingTestApp(t)
			a.windowWidth, a.uiLocale, a.appearanceMode = 1040, locale, appearanceLight
			a.appearanceThemes = loadCustomThemes(t.TempDir())
			ct.Draft = "Draft retained across a failed appearance save — 你好"
			for i := 0; i < 50; i++ {
				ct.Messages = append(ct.Messages, &message{Key: fmt.Sprintf("m-%02d", i), Kind: msgContact, Text: fmt.Sprintf("message %02d\n\nwith a second line", i)})
			}
			ct.Messages = append(ct.Messages, &message{Key: "live:t1:text:a1", Kind: msgContact, TaskID: "t1", ItemID: "a1", Text: "live start", Streaming: true})
			recoveryTestStore(t, a)
			goodPath := a.store.Path
			goodBytes, err := os.ReadFile(goodPath)
			if err != nil {
				t.Fatal(err)
			}
			if a.localSaveErr != "" || a.pi != nil {
				t.Fatal("valid isolated-store setup failed")
			}
			tr := ui.NewTester(a.view, 1040, 720)
			tr.SetLocale(locale)
			tr.Frame()
			msgs := locales.In(locale)
			viewport, ok := tr.Find(msgs.ConversationMessagesLabel())
			if !ok || viewport.W <= 0 || viewport.H <= 0 {
				t.Fatal("actual chat viewport missing")
			}
			for i := 0; i < 3 && ct.list.AtEnd(); i++ {
				tr.Scroll(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 0, -4000)
				tr.Frame()
			}
			if ct.list.AtEnd() || !tr.HasText(msgs.BackToLatest()) {
				t.Fatal("reader fixture did not pause above latest")
			}
			first, _ := ct.list.Visible()
			clicked := -1
			var row ui.Rect
			for i := first; i < len(ct.Messages)-1; i++ {
				candidate, found := tr.Find(fmt.Sprintf("message %02d", i))
				if found && candidate.W > 0 && candidate.H > 0 && candidate.Y >= viewport.Y && candidate.Y+candidate.H <= viewport.Y+viewport.H {
					clicked, row = i, candidate
					break
				}
			}
			if clicked < 0 {
				t.Fatal("no fully visible earlier Markdown paragraph to select")
			}
			tr.ClickAt(row.X+row.W/2, row.Y+row.H/2)
			tr.Command("selectAll")
			tr.SetClipboard("")
			tr.Command("copy")
			selected := tr.Clipboard()
			if selected == "" || selected != ct.Messages[clicked].Text {
				t.Fatalf("reader precondition copied %q, want original %q", selected, ct.Messages[clicked].Text)
			}
			ownerID, sessionID, draft := ct.ID, ct.SessionID, ct.Draft
			anchor, _ := ct.list.Visible()
			capture := func(phase string) {
				current, _ := ct.list.Visible()
				t.Logf("phase=%s anchor=%d paused=%t owner=%q draft=%q localSaveErr=%q appearanceError=%q clipboard=%q", phase, current, !ct.list.AtEnd(), a.selectedContact().ID, ct.Draft, a.localSaveErr, a.appearanceError, tr.Clipboard())
				if dir := os.Getenv("NAGI_SAVE_ERROR_PREVIEW_DIR"); dir != "" {
					if err := os.MkdirAll(dir, 0755); err != nil {
						t.Fatal(err)
					}
					writePreviewPNG(t, filepath.Join(dir, "reader-"+locale+"-"+phase+".png"), tr.Image())
				}
			}
			capture("selected-before-failure")
			blockedParent := filepath.Join(t.TempDir(), "regular-file-parent")
			if err := os.WriteFile(blockedParent, []byte("This is a file, not a directory."), 0600); err != nil {
				t.Fatal(err)
			}
			a.store.Path = filepath.Join(blockedParent, "contacts.json")
			// Invoke the existing appearance action without a deliberate focus-changing navigation.
			a.setAppearanceMode(appearanceDark)
			tr.Frame()
			capture("appearance-error-reflow")
			if a.localSaveErr == "" || a.appearanceError != appearanceStatusSaveFailed || !tr.HasText(msgs.RecoveryLocalSaveFailed()) {
				t.Fatal("appearance action did not expose the genuine failed filesystem save")
			}
			if got, err := os.ReadFile(goodPath); err != nil || string(got) != string(goodBytes) {
				t.Fatal("failed appearance save changed original good disk bytes")
			}
			after, _ := ct.list.Visible()
			if a.selectedContact() != ct || ct.ID != ownerID || ct.SessionID != sessionID || ct.Draft != draft || ct.list.AtEnd() || after != anchor {
				t.Fatal("failed appearance save changed owner/draft or paused anchor")
			}
			tr.SetClipboard("")
			tr.Command("copy")
			capture("fresh-copy-after-error")
			if got := tr.Clipboard(); got != selected {
				t.Fatalf("AC-SAVE1: failed-save error reflow lost active reader selection before any stream delta: copied %q, want %q", got, selected)
			}
			ct.Messages[len(ct.Messages)-1].Text += " appended"
			tr.Frame()
			after, _ = ct.list.Visible()
			tr.SetClipboard("")
			tr.Command("copy")
			capture("fresh-copy-after-stream")
			if ct.list.AtEnd() || after != anchor || tr.Clipboard() != selected || a.selectedContact() != ct || ct.ID != ownerID || ct.SessionID != sessionID || ct.Draft != draft {
				t.Fatal("AC-SAVE1: controlled stream delta changed active selection, anchor or owned draft")
			}
			if got, err := os.ReadFile(goodPath); err != nil || string(got) != string(goodBytes) || a.pi != nil {
				t.Fatal("controlled stream changed original saved file or launched Runtime")
			}
		})
	}
}

func assertFloatingBackToLatestOverlay(t *testing.T) {
	t.Helper()
	pxDist := func(a, b [4]uint8) int {
		abs := func(v int) int {
			if v < 0 {
				return -v
			}
			return v
		}
		return abs(int(a[0])-int(b[0])) + abs(int(a[1])-int(b[1])) + abs(int(a[2])-int(b[2]))
	}
	rgbaOf := func(c ui.Color) [4]uint8 {
		return [4]uint8{c.R, c.G, c.B, c.A}
	}
	imgRGBA := func(img image.Image, x, y int) [4]uint8 {
		p := img.(*image.RGBA).RGBAAt(x, y)
		return [4]uint8{p.R, p.G, p.B, p.A}
	}
	countBubblePixels := func(img image.Image, xStart, xEnd, y int, bubble [4]uint8) int {
		hits := 0
		for x := xStart; x <= xEnd; x++ {
			if pxDist(imgRGBA(img, x, y), bubble) <= 6 {
				hits++
			}
		}
		return hits
	}

	line := strings.Repeat("Wide continuous assistant prose occupying the full reading bubble width behind the overlay control. ", 2)
	body := strings.Join([]string{line, line, line, line, line, line}, "\n")

	for _, locale := range []string{"en", "zh-Hans"} {
		msgs := locales.In(locale)
		for _, dark := range []bool{false, true} {
			wb := theme.ResolveWorkbenchTheme(dark)
			surf := rgbaOf(wb.ComposerSurface)
			inBorder := rgbaOf(wb.InputBorder)
			bubble := rgbaOf(wb.BubbleContact)
			reading := rgbaOf(wb.Reading)
			otherWB := theme.ResolveWorkbenchTheme(!dark)
			otherSurf := rgbaOf(otherWB.ComposerSurface)

			for _, size := range [][2]int{{720, 640}, {960, 640}, {1040, 720}, {1180, 720}} {
				w, h := size[0], size[1]
				t.Run(fmt.Sprintf("%s-dark%t-%dx%d", locale, dark, w, h), func(t *testing.T) {
					a, ct := appReadingTestApp(t)
					a.windowWidth = w
					a.workspaceOpen = false
					a.focusComposer = false
					ct.Draft = "keep scroll draft"
					ct.Messages = make([]*message, 0, 36)
					for i := 0; i < 36; i++ {
						ct.Messages = append(ct.Messages, &message{
							Key:  fmt.Sprintf("overlay-msg-%02d", i),
							Kind: msgContact,
							Text: fmt.Sprintf("Message %02d\n%s", i, body),
						})
					}

					tr := ui.NewTesterWith(a.view, w, h, ui.TesterOptions{Dark: dark})
					tr.SetLocale(locale)
					tr.SetFocused(true)
					tr.Move(-1, -1)
					tr.Frame()

					if !ct.list.AtEnd() || tr.HasText(msgs.BackToLatest()) {
						t.Fatalf("at end state invalid: atEnd=%v hasBack=%v", ct.list.AtEnd(), tr.HasText(msgs.BackToLatest()))
					}
					viewportAtEnd, okEnd := tr.Find(msgs.ConversationMessagesLabel())
					if !okEnd || viewportAtEnd.W <= 0 || viewportAtEnd.H <= 0 {
						t.Fatalf("viewport at end missing: ok=%v rect=%+v", okEnd, viewportAtEnd)
					}

					for i := 0; i < 3 && ct.list.AtEnd(); i++ {
						tr.Scroll(viewportAtEnd.X+viewportAtEnd.W/2, viewportAtEnd.Y+viewportAtEnd.H/2, 0, -640)
						tr.Frame()
					}
					if ct.list.AtEnd() {
						t.Fatal("failed to scroll up away from latest message")
					}
					backRect, okBack := tr.Find(msgs.BackToLatest())
					if !okBack {
						t.Fatalf("floating %q missing when scrolled up", msgs.BackToLatest())
					}
					for step := 0; step < 6; step++ {
						imgTry := tr.Image()
						by := int(backRect.Y + 16)
						if countBubblePixels(imgTry, int(backRect.X)-64, int(backRect.X)-8, by, bubble) >= 12 &&
							countBubblePixels(imgTry, int(backRect.X+backRect.W)+8, int(backRect.X+backRect.W)+64, by, bubble) >= 12 {
							break
						}
						tr.Scroll(viewportAtEnd.X+viewportAtEnd.W/2, viewportAtEnd.Y+viewportAtEnd.H/2, 0, -28)
						tr.Frame()
						backRect, _ = tr.Find(msgs.BackToLatest())
					}

					viewportScrolled, okScrolled := tr.Find(msgs.ConversationMessagesLabel())
					if !okScrolled {
						t.Fatal("scrolled viewport missing")
					}
					if viewportScrolled != viewportAtEnd {
						t.Fatalf("Back to latest shrank or shifted viewport: atEnd=%+v scrolled=%+v", viewportAtEnd, viewportScrolled)
					}
					if backRect.W != 32 || backRect.H != 32 {
						t.Fatalf("Back to latest size = %.1fx%.1f, want 32x32", backRect.W, backRect.H)
					}
					if dx := math.Abs(float64((backRect.X + backRect.W/2) - (viewportScrolled.X + viewportScrolled.W/2))); dx > 1.0 {
						t.Fatalf("Back to latest not horizontally centered: back=%+v viewport=%+v dx=%.2f", backRect, viewportScrolled, dx)
					}
					wantY := viewportScrolled.Y + viewportScrolled.H - 40
					if dy := math.Abs(float64(backRect.Y - wantY)); dy > 1.0 {
						t.Fatalf("Back to latest not anchored 8 DIP above viewport bottom: gotY=%.1f wantY=%.1f back=%+v viewport=%+v", backRect.Y, wantY, backRect, viewportScrolled)
					}

					img := tr.Image()
					bx, by := int(backRect.X), int(backRect.Y)
					cornerPx := imgRGBA(img, bx+1, by+1)
					if pxDist(cornerPx, inBorder) <= 12 || pxDist(cornerPx, surf) <= 6 {
						t.Fatalf("outer corner (%d,%d) looks square instead of Radius(16) circle: got=%v border=%v surf=%v", bx+1, by+1, cornerPx, inBorder, surf)
					}
					for _, pt := range [][2]int{{bx + 16, by + 5}, {bx + 5, by + 16}, {bx + 26, by + 16}} {
						if got := imgRGBA(img, pt[0], pt[1]); pxDist(got, surf) > 5 {
							t.Fatalf("circular face interior (%d,%d) mismatch: got=%v want=%v", pt[0], pt[1], got, surf)
						}
					}
					maxGlyphContrast := 0
					for y := by + 8; y <= by+24; y++ {
						for x := bx + 8; x <= bx+24; x++ {
							if d := pxDist(imgRGBA(img, x, y), surf); d > maxGlyphContrast {
								maxGlyphContrast = d
							}
						}
					}
					if maxGlyphContrast < 180 {
						t.Fatalf("down-arrow glyph contrast too low inside Back to latest: maxL1=%d", maxGlyphContrast)
					}

					midY := by + 16
					leftBubbleHits := countBubblePixels(img, bx-64, bx-8, midY, bubble)
					rightBubbleHits := countBubblePixels(img, bx+32+8, bx+32+64, midY, bubble)
					leftReadingHits := countBubblePixels(img, bx-64, bx-8, midY, reading)
					if leftBubbleHits < 12 || rightBubbleHits < 12 || leftReadingHits > 0 {
						t.Fatalf("detected opaque cutout band behind Back to latest at y=%d: leftBubble=%d rightBubble=%d leftReading=%d", midY, leftBubbleHits, rightBubbleHits, leftReadingHits)
					}

					firstBefore, _ := ct.list.Visible()
					tr.Scroll(backRect.X-40, backRect.Y+16, 0, -1400)
					tr.Frame()
					firstLeftWheel, _ := ct.list.Visible()
					if firstLeftWheel >= firstBefore {
						t.Fatalf("left-side wheel beside Back to latest did not scroll up: %d -> %d", firstBefore, firstLeftWheel)
					}
					tr.Scroll(backRect.X+backRect.W+40, backRect.Y+16, 0, 500)
					tr.Frame()
					firstRightWheel, _ := ct.list.Visible()
					if firstRightWheel <= firstLeftWheel || ct.list.AtEnd() {
						t.Fatalf("right-side wheel beside Back to latest did not scroll cleanly: %d -> %d (atEnd=%v)", firstLeftWheel, firstRightWheel, ct.list.AtEnd())
					}

					tr.Move(-1, -1)
					tr.Advance(theme.ControlTransition.Duration)
					tr.Frame()
					tr.SetDark(!dark)
					snapImg := tr.Image()
					snapRect, _ := tr.Find(msgs.BackToLatest())
					sx, sy := int(snapRect.X), int(snapRect.Y)
					if got := imgRGBA(snapImg, sx+16, sy+5); pxDist(got, otherSurf) > 5 {
						t.Fatalf("Back to latest face did not snap on frame 1 of SetDark(%v): got=%v want=%v", !dark, got, otherSurf)
					}
					maxSnapGlyph := 0
					for y := sy + 8; y <= sy+24; y++ {
						for x := sx + 8; x <= sx+24; x++ {
							if d := pxDist(imgRGBA(snapImg, x, y), otherSurf); d > maxSnapGlyph {
								maxSnapGlyph = d
							}
						}
					}
					if maxSnapGlyph < 180 {
						t.Fatalf("Back to latest glyph did not snap contrast on frame 1 of SetDark(%v): maxL1=%d", !dark, maxSnapGlyph)
					}
					tr.SetDark(dark)
					tr.Advance(theme.ControlTransition.Duration)
					tr.Frame()

					countLabel := func() int {
						n := 0
						for _, txt := range tr.Texts() {
							if txt == msgs.BackToLatest() {
								n++
							}
						}
						return n
					}
					if got := countLabel(); got != 1 {
						t.Fatalf("expected 1 resting occurrence of %q, got %d", msgs.BackToLatest(), got)
					}
					backRect, _ = tr.Find(msgs.BackToLatest())
					tr.Move(1, 1)
					tr.Move(backRect.X+16, backRect.Y+16)
					tr.Advance(700 * time.Millisecond)
					tr.Frame()
					if got := countLabel(); got < 2 {
						t.Fatalf("hovering Back to latest did not reveal %q tooltip: occurrences=%d texts=%v", msgs.BackToLatest(), got, tr.Texts())
					}

					if err := tr.Click(msgs.BackToLatest()); err != nil {
						t.Fatal(err)
					}
					tr.Frame()
					if !ct.list.AtEnd() || tr.HasText(msgs.BackToLatest()) || !tr.Focused(msgs.ComposerLabel(ct.Name)) || ct.Draft != "keep scroll draft" {
						t.Fatalf("clicking Back to latest failed state restore: atEnd=%v hasBack=%v compFocus=%v draft=%q", ct.list.AtEnd(), tr.HasText(msgs.BackToLatest()), tr.Focused(msgs.ComposerLabel(ct.Name)), ct.Draft)
					}

					tr.Move(-1, -1)
					tr.Scroll(viewportAtEnd.X+viewportAtEnd.W/2, viewportAtEnd.Y+viewportAtEnd.H/2, 0, -1400)
					tr.Frame()
					if err := tr.Click(msgs.ComposerLabel(ct.Name)); err != nil {
						t.Fatal(err)
					}
					tr.Frame()
					tr.Key(ui.Shift, ui.KeyTab)
					tr.Advance(theme.ControlTransition.Duration)
					tr.Frame()
					if !tr.Focused(msgs.BackToLatest()) {
						t.Fatalf("Shift+Tab from composer did not focus %q: texts=%v", msgs.BackToLatest(), tr.Texts())
					}
					ringImg := tr.Image()
					backRect, _ = tr.Find(msgs.BackToLatest())
					var foundRing bool
					for x := int(backRect.X) - 6; x <= int(backRect.X)-1; x++ {
						if pxDist(imgRGBA(ringImg, x, int(backRect.Y)+16), surf) >= 90 {
							foundRing = true
							break
						}
					}
					if !foundRing {
						t.Fatal("keyboard-focused Back to latest missing visible DS 2.1 outer focus ring")
					}
					tr.Key(0, ui.KeyEnter)
					tr.Frame()
					if !ct.list.AtEnd() || tr.HasText(msgs.BackToLatest()) || !tr.Focused(msgs.ComposerLabel(ct.Name)) || ct.Draft != "keep scroll draft" {
						t.Fatalf("Shift+Tab + Enter on Back to latest failed restore: atEnd=%v hasBack=%v compFocus=%v draft=%q", ct.list.AtEnd(), tr.HasText(msgs.BackToLatest()), tr.Focused(msgs.ComposerLabel(ct.Name)), ct.Draft)
					}
				})
			}
		}
	}
}

func TestFloatingBackToLatestOverlayGeometryPixelsAndScroll(t *testing.T) {
	assertFloatingBackToLatestOverlay(t)
}
