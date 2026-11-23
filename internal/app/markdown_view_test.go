package app

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
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
			Text: "# 标题\n\n", Streaming: true,
		}}
		tr := ui.NewTester(a.view, size.width, size.height)
		tr.Frame()
		if !tr.HasText("标题") || !tr.HasText("Copy message") {
			t.Fatalf("%dx%d streaming markdown missing: %q", size.width, size.height, tr.Texts())
		}

		applyTestProductEvent(t, a, "execution.textDelta", pi.ProductExecutionEvent{
			ConversationID: ct.SessionID, ExecutionID: execution, Offset: uint64(len("# 标题\n\n")), Text: "列表：\n\n- 第一项\n- 第二项\n\n",
		})
		tr.Frame()
		if !tr.HasText("第一项") {
			t.Fatal("the Product delta did not extend the rendered Markdown")
		}
		applyTestProductEvent(t, a, "execution.settled", pi.ProductExecutionEvent{ConversationID: ct.SessionID, ExecutionID: execution, State: "completed"})
		ct.Messages = []*message{{Key: "saved-markdown", Kind: msgContact, Text: md, History: true}}
		tr.Frame()
		for _, want := range []string{"标题", "第一项", "第二项", "引用", "链接", "fmt.Println", "名称", "中文"} {
			if !tr.HasText(want) {
				t.Fatalf("%dx%d final markdown missing %q: %q", size.width, size.height, want, tr.Texts())
			}
		}
		// The 15px/1.8 reading measure can exceed a short viewport. Scroll the
		// real reading list until the source control is laid out, then click it.
		if !scrollToVisibleControl(tr, "Copy message", -160) {
			t.Fatalf("%dx%d full-source control is not reachable", size.width, size.height)
		}
		if err := tr.Click("Copy message"); err != nil {
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
	if err := tr.Click("Copy message"); err != nil {
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
		for _, label := range []string{"Copy message", "Copy code"} {
			bounds, ok := tr.Find(label)
			if !ok {
				t.Fatalf("%dx%d %q missing: %q", size.width, size.height, label, tr.Texts())
			}
			if bounds.X < 0 || bounds.X+bounds.W > float32(size.width) {
				t.Fatalf("%dx%d %q overflows: %+v", size.width, size.height, label, bounds)
			}
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
