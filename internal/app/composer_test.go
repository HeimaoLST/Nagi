package app

import (
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/materials"
	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestComposerSurfaceHeightLayoutAndRetiredChrome(t *testing.T) {
	if defaultLeftPaneWidth != 272 {
		t.Fatalf("defaultLeftPaneWidth = %g, want 272", defaultLeftPaneWidth)
	}
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dark%t", locale, dark), func(t *testing.T) {
				a := testApp(t)
				a.windowWidth = 1040
				a.workspaceOpen = false
				tr := ui.NewTester(a.view, 1040, 720)
				tr.SetLocale(locale)
				tr.SetDark(dark)
				tr.Frame()

				if a.railWidth() != 272 {
					t.Fatalf("railWidth = %g, want 272", a.railWidth())
				}
				msgs := locales.In(locale)
				input, ok := tr.Find(msgs.ComposerLabel("Xuhu"))
				bar, barOK := tr.Find(msgs.ComposerTools())
				attach, attachOK := tr.Find(msgs.ComposerAttach())
				send, sendOK := tr.Find(msgs.SendAction())
				if !ok || !barOK || !attachOK || !sendOK {
					t.Fatalf("composer controls missing: input=%v bar=%v attach=%v send=%v", ok, barOK, attachOK, sendOK)
				}
				if bar.H != 32 || attach.W != 32 || attach.H != 32 || send.W != 32 || send.H != 32 {
					t.Fatalf("toolbar target geometry mismatch: bar=%+v attach=%+v send=%+v", bar, attach, send)
				}
				if attach.Y != bar.Y || send.Y != bar.Y || attach.X+attach.W >= send.X {
					t.Fatalf("toolbar horizontal/vertical alignment invalid: bar=%+v attach=%+v send=%+v", bar, attach, send)
				}
				cardSpan := (bar.Y + bar.H) - input.Y
				if cardSpan > 66 {
					t.Fatalf("resting composer inner span = %g, want compact <= 66 (<= 86 card)", cardSpan)
				}
				for _, retired := range []string{"Chat model", msgs.TaskContextCount(0), "Voice", "Microphone", "More"} {
					if tr.HasText(retired) {
						t.Fatalf("retired chrome %q rendered in composer: %v", retired, tr.Texts())
					}
				}
			})
		}
	}
}

func composerTwoBotApp(t *testing.T) (*appModel, *contact, *contact) {
	t.Helper()
	a, first := taskContextApp(t)
	second := &contact{ID: "mio-context", Name: "Mio", Role: "Reviewer", SessionID: "s2", PiBinding: recoveryConversationBinding("mio-context", "/tmp", "s2"), Loaded: true}
	a.contacts = append(a.contacts, second)
	a.bindTaskContexts()
	return a, first, second
}

func TestComposerAttachPopoverMenuAndOverlayLifecycle(t *testing.T) {
	a, ct, _ := composerTwoBotApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	ct.Draft = "draft stays intact"
	msgs := locales.In("en")
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.SetFocused(true)
	tr.Frame()

	attachRect, ok := tr.Find(msgs.ComposerAttach())
	if !ok {
		t.Fatal("attach button missing")
	}
	if err := tr.Click(msgs.ComposerAttach()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.composerAttachMenuOpen {
		t.Fatal("clicking + did not open attach popover menu")
	}
	fileItem, fileOK := tr.Find(msgs.ComposerAttachFile())
	noteItem, noteOK := tr.Find(msgs.ComposerAddTextNote())
	if !fileOK || !noteOK {
		t.Fatalf("attach menu items missing: file=%v note=%v texts=%v", fileOK, noteOK, tr.Texts())
	}
	if fileItem.Y+fileItem.H > attachRect.Y+4 || noteItem.Y+noteItem.H > attachRect.Y+4 || fileItem.Y < 0 {
		t.Fatalf("attach popover did not open above + trigger inside viewport: attach=%+v file=%+v note=%+v", attachRect, fileItem, noteItem)
	}

	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.composerAttachMenuOpen || !tr.Focused(msgs.ComposerAttach()) {
		t.Fatalf("Escape did not dismiss popover and restore focus to +: open=%v focused=%v", a.composerAttachMenuOpen, tr.Focused(msgs.ComposerAttach()))
	}

	if err := tr.Click(msgs.ComposerAttach()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	a.selectContact(1)
	tr.Frame()
	if a.composerAttachMenuOpen {
		t.Fatal("Bot switch left attach popover open")
	}

	a.selectContact(0)
	tr.Frame()
	if err := tr.Click(msgs.ComposerAttach()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	tr.Frame()
	if a.composerAttachMenuOpen {
		t.Fatal("opening Settings left attach popover open")
	}

	a.closeSettings()
	tr.Frame()
	if err := tr.Click(msgs.ComposerAttach()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	a.beginShutdown()
	tr.Frame()
	if a.composerAttachMenuOpen || ct.Draft != "draft stays intact" {
		t.Fatalf("shutdown left popover open or mutated draft: open=%v draft=%q", a.composerAttachMenuOpen, ct.Draft)
	}
}

func TestComposerMultiFileChooserAndStaleRejection(t *testing.T) {
	dir := t.TempDir()
	utf8Path := filepath.Join(dir, "spec.txt")
	if err := os.WriteFile(utf8Path, []byte("Spec content 中文\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pdfPath := filepath.Join(dir, "deck.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(dir, "missing.txt")

	t.Run("mixed-batch-and-dismiss", func(t *testing.T) {
		a, ct := taskContextApp(t)
		a.uiLocale = "en"
		a.win = &mygo.Window{}
		callbacks := make(chan func(), 8)
		a.setUIHook(func(fn func()) { callbacks <- fn })
		requests := make(chan mygo.OpenDialogOptions, 1)
		results := make(chan []string, 1)
		a.chooseContextFile = func(opts mygo.OpenDialogOptions) ([]string, error) {
			requests <- opts
			return <-results, nil
		}

		msgs := locales.In("en")
		tr := ui.NewTester(a.view, 1040, 720)
		tr.SetLocale("en")
		tr.SetFocused(true)
		tr.Frame()

		if err := tr.Click(msgs.ComposerAttach()); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if err := choosePopoverMenuItem(tr, msgs.ComposerAttachFile()); err != nil {
			t.Fatal(err)
		}
		opts := <-requests
		if opts.Parent != a.win || !opts.Multiple || opts.Directory || len(opts.Filters) != 0 {
			t.Fatalf("composer chooser options = %+v", opts)
		}
		if !ct.Context.FileChoosing || a.contextFilePickers != 1 {
			t.Fatalf("in-flight chooser flags = choosing:%v pickers:%d", ct.Context.FileChoosing, a.contextFilePickers)
		}

		results <- []string{utf8Path, pdfPath, missingPath}
		themeImportCompletion(t, callbacks)()
		tr.Frame()

		if ct.Context.FileChoosing || a.contextFilePickers != 0 {
			t.Fatalf("completed chooser left flags set: choosing=%v pickers=%d", ct.Context.FileChoosing, a.contextFilePickers)
		}
		items := ct.Context.Snapshot()
		if len(items) != 2 || items[0].Name != "spec.txt" || materials.IsPathReference(items[0]) || items[1].Name != "deck.pdf" || !materials.IsPathReference(items[1]) {
			t.Fatalf("mixed batch results = %+v", items)
		}
		if !errors.Is(ct.Context.ActionError, materials.ErrRead) || !tr.HasText(materials.ErrorText(msgs, ct.Context.ActionError)) {
			t.Fatalf("missing file error not surfaced inline: err=%v texts=%v", ct.Context.ActionError, tr.Texts())
		}
		if err := tr.Click(msgs.ComposerDismissAttachmentError()); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if ct.Context.ActionError != nil || !tr.Focused(msgs.ComposerAttach()) {
			t.Fatalf("dismiss did not clear error and restore focus: err=%v focused=%v", ct.Context.ActionError, tr.Focused(msgs.ComposerAttach()))
		}
	})

	t.Run("chooser-error", func(t *testing.T) {
		a, ct := taskContextApp(t)
		a.uiLocale = "en"
		callbacks := make(chan func(), 8)
		a.setUIHook(func(fn func()) { callbacks <- fn })
		a.chooseContextFile = func(mygo.OpenDialogOptions) ([]string, error) {
			return nil, errors.New("dialog unavailable")
		}
		msgs := locales.In("en")
		tr := ui.NewTester(a.view, 1040, 720)
		tr.SetLocale("en")
		tr.SetFocused(true)
		tr.Frame()

		a.chooseComposerFiles(ct)
		themeImportCompletion(t, callbacks)()
		tr.Frame()
		if !errors.Is(ct.Context.ActionError, materials.ErrFileChooser) || !tr.HasText(msgs.ComposerFileChooserFailed()) || !tr.Focused(msgs.ComposerAttach()) {
			t.Fatalf("chooser error handling failed: err=%v focused=%v texts=%v", ct.Context.ActionError, tr.Focused(msgs.ComposerAttach()), tr.Texts())
		}
	})

	for _, action := range []string{"switch-bot", "open-settings", "shutdown"} {
		t.Run("stale-"+action, func(t *testing.T) {
			a, ct, second := composerTwoBotApp(t)
			a.uiLocale = "en"
			callbacks := make(chan func(), 8)
			a.setUIHook(func(fn func()) { callbacks <- fn })
			release := make(chan struct{})
			a.chooseContextFile = func(mygo.OpenDialogOptions) ([]string, error) {
				<-release
				return []string{utf8Path}, nil
			}
			a.chooseComposerFiles(ct)
			switch action {
			case "switch-bot":
				a.selectContact(1)
			case "open-settings":
				a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
			case "shutdown":
				a.beginShutdown()
			}
			close(release)
			themeImportCompletion(t, callbacks)()
			if ct.Context.FileChoosing || a.contextFilePickers != 0 || ct.Context.Count() != 0 || second.Context.Count() != 0 {
				t.Fatalf("%s accepted stale result or leaked picker: choosing=%v pickers=%d c0=%d c1=%d",
					action, ct.Context.FileChoosing, a.contextFilePickers, ct.Context.Count(), second.Context.Count())
			}
		})
	}
}

func TestComposerFileDropMixedBatchWrapAndShutdownGuard(t *testing.T) {
	dir := t.TempDir()
	utf8Path := filepath.Join(dir, "plan.txt")
	if err := os.WriteFile(utf8Path, []byte("Plan details\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pngPath := filepath.Join(dir, "mock.png")
	if err := os.WriteFile(pngPath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subDir, 0o700); err != nil {
		t.Fatal(err)
	}

	a, ct := taskContextApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	msgs := locales.In("en")
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.SetFocused(true)
	tr.Frame()

	inputRect, ok := tr.Find(msgs.ComposerLabel("Xuhu"))
	if !ok {
		t.Fatal("composer input missing")
	}
	taken := tr.DropFiles(inputRect.X+inputRect.W/2, inputRect.Y+inputRect.H/2, utf8Path, pngPath, subDir)
	tr.Frame()
	if !taken {
		t.Fatal("composer card did not accept dropped files")
	}
	items := ct.Context.Snapshot()
	if len(items) != 2 || items[0].Name != "plan.txt" || materials.IsPathReference(items[0]) || items[1].Name != "mock.png" || !materials.IsPathReference(items[1]) {
		t.Fatalf("dropped files = %+v", items)
	}
	if !errors.Is(ct.Context.ActionError, materials.ErrRead) {
		t.Fatalf("directory drop error = %v, want ErrRead", ct.Context.ActionError)
	}
	chipRect, chipOK := tr.Find("plan.txt")
	inputAfter, _ := tr.Find(msgs.ComposerLabel("Xuhu"))
	if !chipOK || chipRect.Y+chipRect.H > inputAfter.Y+4 {
		t.Fatalf("chip strip was not positioned above textarea: chip=%+v input=%+v", chipRect, inputAfter)
	}

	if err := tr.Click(msgs.ComposerRemoveAttachment("plan.txt")); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.Context.Count() != 1 || ct.Context.Snapshot()[0].Name != "mock.png" || !tr.Focused(msgs.ComposerAttach()) {
		t.Fatalf("chip remove did not remove exact item or restore focus: items=%+v focused=%v", ct.Context.Snapshot(), tr.Focused(msgs.ComposerAttach()))
	}

	ct.Context.ActionError = nil
	for i := 1; i <= 5; i++ {
		if _, err := ct.Context.AddSnippet(fmt.Sprintf("Attachment-%d-long-name", i), "body"); err != nil {
			t.Fatal(err)
		}
	}
	a.windowWidth = 720
	trMin := ui.NewTester(a.view, 720, 640)
	trMin.SetLocale("en")
	trMin.Frame()
	attachMin, aOK := trMin.Find(msgs.ComposerAttach())
	sendMin, sOK := trMin.Find(msgs.SendAction())
	if !aOK || !sOK || attachMin.W != 32 || attachMin.H != 32 || sendMin.W != 32 || sendMin.H != 32 || sendMin.Y+sendMin.H > 640 || sendMin.X+sendMin.W > 720 {
		t.Fatalf("minimum 720x640 composer clipped targets with wrapped chips: attach=%+v send=%+v", attachMin, sendMin)
	}

	a.beginShutdown()
	tr.Frame()
	beforeCount := ct.Context.Count()
	inputRect, _ = tr.Find(msgs.ComposerLabel("Xuhu"))
	if tr.DropFiles(inputRect.X+inputRect.W/2, inputRect.Y+inputRect.H/2, utf8Path) || ct.Context.Count() != beforeCount {
		t.Fatal("shutdown composer accepted dropped files")
	}
}

func TestComposerFrostedModalsLifecycleAndFocus(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "payload.txt")
	if err := os.WriteFile(binPath, []byte{0x00, 0x01, 0x02, 0x03}, 0o600); err != nil {
		t.Fatal(err)
	}

	a, ct, _ := composerTwoBotApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	msgs := locales.In("en")
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.SetFocused(true)
	tr.Frame()

	if err := tr.Click(msgs.ComposerAttach()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := choosePopoverMenuItem(tr, msgs.ComposerAddTextNote()); err != nil {
		t.Fatal(err)
	}
	if a.composerMaterial == nil || !tr.HasText(msgs.ComposerTextNoteTitle()) || !tr.Focused(msgs.TaskContextNameLabel()) {
		t.Fatalf("Add text note modal did not open with Name focused: modal=%+v focused=%v", a.composerMaterial, tr.Focused(msgs.TaskContextNameLabel()))
	}

	if err := tr.Click(msgs.ComposerAddNoteAction()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.composerMaterial == nil || !tr.HasText(msgs.TaskContextErrName()) || !tr.Focused(msgs.TaskContextNameLabel()) || ct.Context.Count() != 0 {
		t.Fatalf("blank note submit did not validate and refocus Name: modal=%+v focused=%v count=%d", a.composerMaterial, tr.Focused(msgs.TaskContextNameLabel()), ct.Context.Count())
	}

	tr.Type("Release checklist")
	if err := tr.Click(msgs.TaskContextTextLabel()); err != nil {
		t.Fatal(err)
	}
	tr.Type("First note body")
	if err := tr.Click(msgs.ComposerAddNoteAction()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.composerMaterial != nil || ct.Context.Count() != 1 || !tr.Focused(msgs.ComposerAttach()) {
		t.Fatalf("Add text note did not save, close, and return focus to +: modal=%+v count=%d focused=%v", a.composerMaterial, ct.Context.Count(), tr.Focused(msgs.ComposerAttach()))
	}

	if err := tr.Click("Release checklist"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.composerMaterial == nil || !tr.HasText(msgs.ComposerMaterialTitle()) {
		t.Fatalf("clicking chip did not open Edit material modal: modal=%+v", a.composerMaterial)
	}
	if err := tr.Click(msgs.TaskContextNameLabel()); err != nil {
		t.Fatal(err)
	}
	tr.Command("selectAll")
	tr.Type("Checklist v2")
	if err := tr.Click(msgs.ComposerMaterialSave()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.composerMaterial != nil || ct.Context.Snapshot()[0].Name != "Checklist v2" || !tr.Focused("Checklist v2") {
		t.Fatalf("Save did not update snippet and restore focus to chip: modal=%+v items=%+v focused=%v", a.composerMaterial, ct.Context.Snapshot(), tr.Focused("Checklist v2"))
	}

	if err := tr.Click("Checklist v2"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.composerMaterial != nil || !tr.Focused("Checklist v2") {
		t.Fatalf("Escape on edit modal did not close and return focus to chip: modal=%+v focused=%v", a.composerMaterial, tr.Focused("Checklist v2"))
	}

	refItem, err := ct.Context.AttachFile(binPath, "")
	if err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click(refItem.Name); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if _, hasTextArea := tr.Find(msgs.TaskContextTextLabel()); hasTextArea {
		t.Fatal("path-reference modal exposed editable body textarea")
	}
	if !tr.HasText(msgs.TaskContextSource(binPath)) || !tr.HasText(msgs.ComposerPathReferenceDetails(materials.FormatSize(4))) {
		t.Fatalf("path-reference modal missing source or details: %v", tr.Texts())
	}
	if err := tr.Click(msgs.TaskContextNameLabel()); err != nil {
		t.Fatal(err)
	}
	tr.Command("selectAll")
	tr.Type("Archive renamed")
	if err := tr.Click(msgs.ComposerMaterialSave()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	updatedRef, ok := ct.Context.ItemByID(refItem.ID)
	if !ok || updatedRef.Name != "Archive renamed" || updatedRef.Text != refItem.Text || !materials.IsPathReference(updatedRef) {
		t.Fatalf("path-reference rename mutated body: %+v", updatedRef)
	}

	if err := os.WriteFile(binPath, []byte("upgraded utf8 body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tr.Click("Archive renamed"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click(msgs.ComposerMaterialRefresh()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	upgraded, _ := ct.Context.ItemByID(refItem.ID)
	if materials.IsPathReference(upgraded) || upgraded.Text != "upgraded utf8 body" {
		t.Fatalf("modal Refresh did not upgrade path reference to UTF-8 snapshot: %+v", upgraded)
	}
	if _, hasTextArea := tr.Find(msgs.TaskContextTextLabel()); !hasTextArea {
		t.Fatal("upgraded UTF-8 snapshot did not reveal editable text area in modal")
	}

	if err := os.Remove(binPath); err != nil {
		t.Fatal(err)
	}
	if err := tr.Click(msgs.ComposerMaterialRefresh()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.composerMaterial == nil || !errors.Is(a.composerMaterial.err, materials.ErrRead) || !tr.HasText(materials.ErrorText(msgs, a.composerMaterial.err)) {
		t.Fatalf("failed modal Refresh did not surface read error: modal=%+v texts=%v", a.composerMaterial, tr.Texts())
	}
	stillKept, _ := ct.Context.ItemByID(refItem.ID)
	if stillKept.Text != "upgraded utf8 body" {
		t.Fatalf("failed modal Refresh lost prior snapshot: %+v", stillKept)
	}

	if err := tr.Click(msgs.TaskContextRemove()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.composerMaterial != nil || ct.Context.Count() != 1 || !tr.Focused(msgs.ComposerAttach()) {
		t.Fatalf("modal Remove did not remove item and return focus to +: modal=%+v count=%d focused=%v", a.composerMaterial, ct.Context.Count(), tr.Focused(msgs.ComposerAttach()))
	}

	if err := tr.Click("Checklist v2"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	a.selectContact(1)
	tr.Frame()
	if a.composerMaterial != nil {
		t.Fatal("Bot switch did not auto-close composer material modal")
	}
}

func assertComposerPlusAlignmentAndChrome(t *testing.T) {
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

	for _, locale := range []string{"en", "zh-Hans"} {
		msgs := locales.In(locale)
		for _, dark := range []bool{false, true} {
			wb := theme.ResolveWorkbenchTheme(dark)
			surf := rgbaOf(wb.ComposerSurface)
			hover := rgbaOf(theme.StrongHoverFill(wb.Theme))
			chipBg := rgbaOf(wb.ComposerChipBg)
			for _, size := range [][2]int{{1040, 720}, {720, 640}} {
				w, h := size[0], size[1]
				t.Run(fmt.Sprintf("%s-dark%t-%dx%d", locale, dark, w, h), func(t *testing.T) {
					a, ct := taskContextApp(t)
					a.windowWidth = w
					a.workspaceOpen = false
					a.focusComposer = false
					ct.Draft = ""
					ct.DraftQuote = nil
					ct.Context.Items = nil

					tr := ui.NewTesterWith(a.view, w, h, ui.TesterOptions{Dark: dark})
					tr.SetLocale(locale)
					tr.SetFocused(false)
					tr.Move(-1, -1)
					tr.Frame()

					if _, hasCaret := tr.TextCaret(); hasCaret {
						t.Fatal("unfocused composer unexpectedly painted active text caret")
					}

					input, okInput := tr.Find(msgs.ComposerLabel(ct.Name))
					bar, okBar := tr.Find(msgs.ComposerTools())
					attach, okAttach := tr.Find(msgs.ComposerAttach())
					send, okSend := tr.Find(msgs.SendAction())
					if !okInput || !okBar || !okAttach || !okSend {
						t.Fatalf("composer controls missing: input=%v bar=%v attach=%v send=%v", okInput, okBar, okAttach, okSend)
					}
					if bar.H != 32 || attach.W != 32 || attach.H != 32 || send.W != 32 || send.H != 32 {
						t.Fatalf("composer toolbar hit sizes invalid: bar=%+v attach=%+v send=%+v", bar, attach, send)
					}
					if attach.Y != bar.Y || send.Y != bar.Y || math.Abs(float64(attach.X-input.X)) > 0.5 {
						t.Fatalf("composer toolbar row alignment invalid: input=%+v bar=%+v attach=%+v send=%+v", input, bar, attach, send)
					}

					img := tr.Image()
					if img.Bounds().Dx() != w || img.Bounds().Dy() != h {
						t.Fatalf("unexpected render size %dx%d, want %dx%d", img.Bounds().Dx(), img.Bounds().Dy(), w, h)
					}

					ax, ay, aw, ah := int(attach.X), int(attach.Y), int(attach.W), int(attach.H)
					for _, d := range []int{6, 12, 16, 20, 25} {
						for _, pt := range [][2]int{
							{ax, ay + d},
							{ax + aw - 1, ay + d},
							{ax + d, ay},
							{ax + d, ay + ah - 1},
						} {
							if got := imgRGBA(img, pt[0], pt[1]); pxDist(got, surf) > 4 {
								t.Fatalf("resting attach outer perimeter (%d,%d) not borderless ComposerSurface: got=%v want=%v", pt[0], pt[1], got, surf)
							}
						}
					}
					for _, pt := range [][2]int{
						{ax + 16, ay + 4},
						{ax + 16, ay + 27},
						{ax + 4, ay + 16},
						{ax + 27, ay + 16},
						{ax + 6, ay + 6},
						{ax + 25, ay + 6},
						{ax + 6, ay + 25},
						{ax + 25, ay + 25},
					} {
						if got := imgRGBA(img, pt[0], pt[1]); pxDist(got, surf) > 4 {
							t.Fatalf("resting attach inset face (%d,%d) has visible box/border: got=%v want=%v", pt[0], pt[1], got, surf)
						}
					}

					textLeftX := -1
					for x := int(input.X); x < int(input.X+input.W) && textLeftX < 0; x++ {
						for y := int(input.Y) + 1; y < int(input.Y+input.H)-1; y++ {
							if pxDist(imgRGBA(img, x, y), surf) >= 24 {
								textLeftX = x
								break
							}
						}
					}
					plusLeftX := -1
					for x := ax; x < ax+aw && plusLeftX < 0; x++ {
						for y := ay; y < ay+ah; y++ {
							if pxDist(imgRGBA(img, x, y), surf) >= 24 {
								plusLeftX = x
								break
							}
						}
					}
					if textLeftX < 0 || plusLeftX < 0 {
						t.Fatalf("failed to detect rendered ink pixels: textLeftX=%d plusLeftX=%d", textLeftX, plusLeftX)
					}
					deltaX := plusLeftX - textLeftX
					if deltaX < 0 {
						deltaX = -deltaX
					}
					if deltaX > 2 {
						t.Fatalf("optical left misalignment: plusLeftX=%d textLeftX=%d (delta=%d > 2; attach=%+v input=%+v)", plusLeftX, textLeftX, deltaX, attach, input)
					}

					tr.SetFocused(true)
					tr.Move(attach.X+16, attach.Y+16)
					tr.Advance(theme.ControlTransition.Duration)
					tr.Frame()
					hoverImg := tr.Image()
					for _, pt := range [][2]int{
						{ax + 16, ay + 5},
						{ax + 16, ay + 26},
						{ax + 5, ay + 16},
						{ax + 26, ay + 16},
					} {
						got := imgRGBA(hoverImg, pt[0], pt[1])
						if pxDist(got, surf) < 30 || pxDist(got, hover) > 8 {
							t.Fatalf("hovered attach inset 26x26 face (%d,%d) mismatch: got=%v surf=%v wantHover=%v", pt[0], pt[1], got, surf, hover)
						}
					}
					for _, pt := range [][2]int{
						{ax + 16, ay + 1},
						{ax + 16, ay + 30},
						{ax + 1, ay + 16},
						{ax + 30, ay + 16},
					} {
						if got := imgRGBA(hoverImg, pt[0], pt[1]); pxDist(got, surf) > 4 {
							t.Fatalf("hovered attach outer edge (%d,%d) should remain unshaded outside 26x26 face: got=%v want=%v", pt[0], pt[1], got, surf)
						}
					}

					attach, _ = tr.Find(msgs.ComposerAttach())
					tr.ClickAt(attach.X+1, attach.Y+1)
					tr.Frame()
					if !a.composerAttachMenuOpen || !tr.HasText(msgs.ComposerAttachFile()) || !tr.HasText(msgs.ComposerAddTextNote()) {
						t.Fatalf("edge click (%g,%g) did not open attach popover: open=%v texts=%v", attach.X+1, attach.Y+1, a.composerAttachMenuOpen, tr.Texts())
					}
					tr.Key(0, ui.KeyEscape)
					tr.Frame()
					if a.composerAttachMenuOpen || !tr.Focused(msgs.ComposerAttach()) {
						t.Fatalf("Escape did not dismiss popover and restore focus to attach: open=%v focused=%v", a.composerAttachMenuOpen, tr.Focused(msgs.ComposerAttach()))
					}

					tr.Move(-1, -1)
					if err := tr.Click(msgs.ComposerLabel(ct.Name)); err != nil {
						t.Fatal(err)
					}
					tr.Frame()
					for i := 0; i < 24 && !tr.Focused(msgs.ComposerAttach()); i++ {
						tr.Key(0, ui.KeyTab)
						tr.Frame()
					}
					if !tr.Focused(msgs.ComposerAttach()) {
						t.Fatalf("Tab did not reach attach control: texts=%v", tr.Texts())
					}
					tr.Key(0, ui.KeyEnter)
					tr.Frame()
					if !a.composerAttachMenuOpen {
						t.Fatal("Enter on focused attach control did not open attach popover")
					}
					tr.Key(0, ui.KeyEscape)
					tr.Advance(theme.ControlTransition.Duration)
					tr.Frame()
					if a.composerAttachMenuOpen || !tr.Focused(msgs.ComposerAttach()) {
						t.Fatalf("Escape after keyboard open did not restore attach focus: open=%v focused=%v", a.composerAttachMenuOpen, tr.Focused(msgs.ComposerAttach()))
					}

					ringImg := tr.Image()
					var foundLeftRing, foundBottomRing bool
					for x := ax - 4; x <= ax+2; x++ {
						if pxDist(imgRGBA(ringImg, x, ay+16), surf) >= 80 {
							foundLeftRing = true
							break
						}
					}
					for y := ay + ah - 2; y <= ay+ah+4; y++ {
						if pxDist(imgRGBA(ringImg, ax+16, y), surf) >= 80 {
							foundBottomRing = true
							break
						}
					}
					if !foundLeftRing || !foundBottomRing {
						t.Fatalf("focused attach control missing DS 2.1 focus ring around 26x26 face: left=%v bottom=%v", foundLeftRing, foundBottomRing)
					}

					if err := tr.Click(msgs.ComposerLabel(ct.Name)); err != nil {
						t.Fatal(err)
					}
					tr.Frame()
					caret, hasCaret := tr.TextCaret()
					inputAfter, _ := tr.Find(msgs.ComposerLabel(ct.Name))
					if !hasCaret || math.Abs(float64(caret.X-(inputAfter.X+12))) > 1.5 {
						t.Fatalf("focused composer caret inset mismatch: has=%v caret=%+v input=%+v wantX=%.1f", hasCaret, caret, inputAfter, inputAfter.X+12)
					}

					ct.DraftQuote = &pi.ProductQuote{
						MessageID:  "q-align",
						Sender:     "bot",
						AuthorName: ct.Name,
						Text:       "Confirm optical alignment across quote and material strips.",
					}
					if _, err := ct.Context.AddSnippet("Design spec", "Spec body"); err != nil {
						t.Fatal(err)
					}
					tr.SetFocused(false)
					tr.Move(-1, -1)
					tr.Advance(theme.ControlTransition.Duration)
					tr.Frame()

					inputNow, okIn := tr.Find(msgs.ComposerLabel(ct.Name))
					chip, okChip := tr.Find("Design spec")
					quoteRemove, okQuoteRemove := tr.Find(msgs.QuoteRemove())
					if !okIn || !okChip || !okQuoteRemove {
						t.Fatalf("quote/chip controls missing: input=%v chip=%v quoteRemove=%v", okIn, okChip, okQuoteRemove)
					}
					if chip.X < inputNow.X+6 || chip.X+chip.W > inputNow.X+inputNow.W-1 {
						t.Fatalf("material chip strip not aligned with composer insets: chip=%+v input=%+v", chip, inputNow)
					}
					if quoteRemove.X+quoteRemove.W > inputNow.X+inputNow.W-2 {
						t.Fatalf("quote remove button overflows composer right inset: quoteRemove=%+v input=%+v", quoteRemove, inputNow)
					}
					quoteImg := tr.Image()
					qy := int(quoteRemove.Y + quoteRemove.H/2)
					quoteLeftX, quoteRightX := -1, -1
					for x := int(inputNow.X) - 2; x <= int(inputNow.X+inputNow.W)+12; x++ {
						if pxDist(imgRGBA(quoteImg, x, qy), chipBg) <= 6 {
							if quoteLeftX < 0 {
								quoteLeftX = x
							}
							quoteRightX = x
						}
					}
					if quoteLeftX < 0 || quoteRightX < 0 {
						t.Fatalf("failed to detect composerQuote chip background at y=%d", qy)
					}
					wantLeft := int(inputNow.X) + 8
					wantRight := int(inputNow.X+inputNow.W) - 2
					if quoteLeftX < wantLeft-2 || quoteLeftX > wantLeft+2 || quoteRightX > wantRight+1 || quoteRightX < wantRight-4 {
						t.Fatalf("composerQuote bounds [%d..%d] misaligned with input insets [%d..%d]", quoteLeftX, quoteRightX, wantLeft, wantRight)
					}
				})
			}

			t.Run(fmt.Sprintf("%s-dark%t-960x640-profile", locale, dark), func(t *testing.T) {
				a, ct := workspaceTestApp(t)
				a.windowWidth = 960
				a.rightPaneWidth = 440
				a.openContactProfile(ct.ID)
				tr := ui.NewTesterWith(a.view, 960, 640, ui.TesterOptions{Dark: dark})
				tr.SetLocale(locale)
				tr.Frame()
				comp, okComp := tr.Find(msgs.ComposerLabel(ct.Name))
				if !okComp || comp.W-14 < 440 {
					t.Fatalf("960x640 profile net editable composer width = %.1f DIP, want >= 440 (comp=%+v)", comp.W-14, comp)
				}
			})
		}
	}
}

func TestComposerBorderlessPlusOpticalAlignmentAndRowInsets(t *testing.T) {
	assertComposerPlusAlignmentAndChrome(t)
}
