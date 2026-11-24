package materials

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestTaskContextFileSnapshotRefreshAndDiskIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brief.txt")
	original := "First line\n第二行 — café\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	tc := &Context{}
	item, err := tc.ImportFile(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "brief.txt" || item.Source != path || item.Text != original || item.Revision != 1 {
		t.Fatalf("imported Snapshot = %+v", item)
	}

	changed := "changed on disk\r\nwith a new line\n"
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := tc.Snapshot()[0]; got.Text != original || got.Revision != 1 {
		t.Fatalf("file change mutated the saved Snapshot: %+v", got)
	}

	refreshed, err := tc.Refresh(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Text != changed || refreshed.Revision != 2 || refreshed.Source != path {
		t.Fatalf("refreshed Snapshot = %+v", refreshed)
	}

	edited, err := tc.Edit(item.ID, "Edited brief", "edited only in the Snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Text != "edited only in the Snapshot" || edited.Revision != 3 || edited.Source != path {
		t.Fatalf("edited Snapshot = %+v", edited)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != changed {
		t.Fatalf("editing the Snapshot wrote the file: %q", onDisk)
	}
}

func TestTaskContextNamedTextAndSubmittedRevision(t *testing.T) {
	tc := &Context{}
	original := "First line\n第二行 — café 🍵"
	item, err := tc.AddSnippet("Release notes", original)
	if err != nil {
		t.Fatal(err)
	}
	if item.Text != original || item.Source != "" || item.Name != "Release notes" || item.Revision != 1 {
		t.Fatalf("named snippet = %+v", item)
	}

	submitted := tc.Snapshot()
	if _, err := tc.Edit(item.ID, "Release notes v2", "new text"); err != nil {
		t.Fatal(err)
	}
	if submitted[0].Text != original || submitted[0].Name != "Release notes" || submitted[0].Revision != 1 {
		t.Fatalf("submitted Snapshot changed after a later edit: %+v", submitted[0])
	}
	if removed := tc.ClearSubmitted(submitted); removed != 0 {
		t.Fatalf("cleared a newer revision: %d", removed)
	}
	current := tc.Snapshot()
	if len(current) != 1 || current[0].Text != "new text" || current[0].Revision != 2 {
		t.Fatalf("newer revision was not preserved: %+v", current)
	}
	if removed := tc.ClearSubmitted(current); removed != 1 {
		t.Fatalf("exact accepted revision was not cleared: %d", removed)
	}
	if len(tc.Snapshot()) != 0 {
		t.Fatal("cleared accepted Snapshot remained in the list")
	}
}

func TestTaskContextFailuresPreservePreviousValues(t *testing.T) {
	tc := &Context{}
	item, err := tc.AddSnippet("Keep me", "unchanged")
	if err != nil {
		t.Fatal(err)
	}
	before := tc.Snapshot()

	if _, err := tc.AddSnippet("", "text"); !errors.Is(err, ErrName) {
		t.Fatalf("blank name error = %v", err)
	}
	if _, err := tc.AddSnippet("large", strings.Repeat("a", MaxBytes+10)); !errors.Is(err, ErrOversize) {
		t.Fatalf("oversize error = %v", err)
	}
	// The raw text is below 1 MiB, but its serialized caller-context wrapper is
	// not; size checks must account for the name and wrapper bytes.
	rawBelowLimit := strings.Repeat("a", MaxBytes-100)
	if _, err := tc.AddSnippet(strings.Repeat("n", 400), rawBelowLimit); !errors.Is(err, ErrOversize) {
		t.Fatalf("serialized-size error = %v", err)
	}
	chunk := strings.Repeat("a", 600*1024)
	if _, err := tc.AddSnippet("half", chunk); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.AddSnippet("overflow", chunk); !errors.Is(err, ErrOversize) {
		t.Fatalf("aggregate error = %v", err)
	}
	if got := len(tc.Snapshot()); got != 2 {
		t.Fatalf("failed additions changed the list: %d items", got)
	}
	if _, err := tc.Edit(item.ID, "", "text"); !errors.Is(err, ErrName) {
		t.Fatalf("failed edit error = %v", err)
	}
	after, ok := tc.ItemByID(item.ID)
	if !ok || after != before[0] {
		t.Fatalf("failed edit changed the Snapshot: %+v", after)
	}
	if _, err := tc.Edit("missing", "name", "text"); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing edit error = %v", err)
	}
	if _, err := tc.Refresh(item.ID); !errors.Is(err, ErrNoSource) {
		t.Fatalf("named refresh error = %v", err)
	}
}

func TestTaskContextEmptyFileAndInputNameProjection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tc := &Context{}
	fileItem, err := tc.ImportFile(path, "Empty brief")
	if err != nil {
		t.Fatalf("empty UTF-8 file was rejected: %v", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if fileItem.Text != "" || fileItem.Source != absolute {
		t.Fatalf("empty file Snapshot = %+v", fileItem)
	}
	if want := "Empty brief · file " + absolute; fileItem.InputName() != want {
		t.Fatalf("file input name = %q, want %q", fileItem.InputName(), want)
	}
	snippet, err := tc.AddSnippet("Empty snippet", "")
	if err != nil {
		t.Fatalf("empty named snippet was rejected: %v", err)
	}
	if snippet.InputName() != "Empty snippet" {
		t.Fatalf("snippet input name = %q", snippet.InputName())
	}
	if err := os.WriteFile(path, []byte("now has text"), 0o600); err != nil {
		t.Fatal(err)
	}
	refreshed, err := tc.Refresh(fileItem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Text != "now has text" || refreshed.Revision != 2 {
		t.Fatalf("refreshed empty file = %+v", refreshed)
	}

	// The same name projection is used for the aggregate budget: a snippet
	// that fits as typed text does not fit once the explicit file source path
	// is part of the caller-context label.
	base := WrapperBytes("x", "")
	text := strings.Repeat("a", MaxBytes-base-10)
	budget := &Context{}
	if _, err := budget.AddSnippet("x", text); err != nil {
		t.Fatalf("projection-free snippet did not fit: %v", err)
	}
	fileText := filepath.Join(dir, "projection-budget.txt")
	if err := os.WriteFile(fileText, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	fileBudget := &Context{}
	if _, err := fileBudget.ImportFile(fileText, "x"); !errors.Is(err, ErrOversize) {
		t.Fatalf("file source was not included in the size budget: %v", err)
	}
}

func TestTaskContextFileValidationErrors(t *testing.T) {
	dir := t.TempDir()
	tc := &Context{}
	item, err := tc.AddSnippet("Keep me", "unchanged")
	if err != nil {
		t.Fatal(err)
	}
	before := tc.Snapshot()

	pdf := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(dir, "invalid.txt")
	if err := os.WriteFile(invalid, []byte{0xff, 0xfe, 0xfd}, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "binary.txt")
	if err := os.WriteFile(binary, []byte("text\x00more"), 0o600); err != nil {
		t.Fatal(err)
	}
	oversize := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(oversize, []byte(strings.Repeat("a", MaxBytes+10)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
		want error
	}{
		{"unsupported", pdf, ErrUnsupported},
		{"invalid utf8", invalid, ErrUTF8},
		{"not text", binary, ErrNotText},
		{"missing", filepath.Join(dir, "missing.txt"), ErrRead},
		{"oversize", oversize, ErrOversize},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := tc.ImportFile(test.path, ""); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if got := tc.Snapshot(); len(got) != 1 || got[0] != before[0] {
				t.Fatalf("failed import changed the list: %+v", got)
			}
		})
	}

	svg := filepath.Join(dir, "diagram.svg")
	if err := os.WriteFile(svg, []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"><text>still text</text></svg>"), 0o600); err != nil {
		t.Fatal(err)
	}
	svgContext := &Context{}
	if _, err := svgContext.ImportFile(svg, ""); err != nil {
		t.Fatalf("UTF-8 SVG text was rejected: %v", err)
	}

	path := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(path, []byte("old Snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileItem, err := tc.ImportFile(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Refresh(fileItem.ID); !errors.Is(err, ErrRead) {
		t.Fatalf("missing refresh error = %v", err)
	}
	kept, ok := tc.ItemByID(fileItem.ID)
	if !ok || kept.Text != "old Snapshot" || kept.Revision != 1 {
		t.Fatalf("failed refresh lost the old Snapshot: %+v", kept)
	}
	if item, ok := tc.ItemByID(item.ID); !ok || item.Text != "unchanged" {
		t.Fatalf("failed refresh changed another item: %+v", item)
	}
	if !tc.Remove(fileItem.ID) || tc.Remove(fileItem.ID) {
		t.Fatal("remove did not report exact removal")
	}
	if _, ok := tc.ItemByID(fileItem.ID); ok {
		t.Fatal("removed item remained")
	}
}

func TestTaskContextViewEditsPreviewsRefreshesAndReportsErrors(t *testing.T) {
	for _, dark := range []bool{false, true} {
		dark := dark
		name := "light"
		if dark {
			name = "dark"
		}
		t.Run(name, func(t *testing.T) {
			exerciseTaskContextView(t, dark)
		})
	}
}

func exerciseTaskContextView(t *testing.T, dark bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("old file text"), 0o600); err != nil {
		t.Fatal(err)
	}
	tc := &Context{}
	tr := taskContextTester(tc, dark)

	tr.Frame()
	if !tr.HasText(locales.In("en").TaskContextHeading()) || !tr.HasText(locales.In("en").TaskContextEmpty()) {
		t.Fatalf("empty component state missing: %v", tr.Texts())
	}
	if err := tr.Click("Add snippet"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click(locales.In("en").TaskContextNameLabel()); err != nil {
		t.Fatal(err)
	}
	tr.Type("Release notes")
	if err := tr.Click(locales.In("en").TaskContextTextLabel()); err != nil {
		t.Fatal(err)
	}
	tr.Type("Line one · 你好")
	if err := tr.Click("Save snippet"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(tc.Snapshot()) != 1 || tc.Snapshot()[0].Text != "Line one · 你好" {
		t.Fatalf("UI add snippet = %+v", tc.Snapshot())
	}
	if !tr.HasText("Release notes") || !tr.HasText("Named snippet") {
		t.Fatalf("snippet card missing: %v", tr.Texts())
	}
	if err := tr.Click("Preview"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Line one · 你好") || !tr.HasText("Close preview") {
		t.Fatalf("preview did not show saved text: %v", tr.Texts())
	}

	if err := tr.Click("Edit"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click(locales.In("en").TaskContextNameLabel()); err != nil {
		t.Fatal(err)
	}
	tr.Command("selectAll")
	tr.Type("Renamed snippet")
	if err := tr.Click("Save changes"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if got := tc.Snapshot()[0]; got.Name != "Renamed snippet" || got.Text != "Line one · 你好" || got.Revision != 2 {
		t.Fatalf("UI edit = %+v", got)
	}
	if !tr.HasText("Renamed snippet") {
		t.Fatalf("edited name missing: %v", tr.Texts())
	}

	if err := tr.Click("Add file"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click(locales.In("en").TaskContextFilePathLabel()); err != nil {
		t.Fatal(err)
	}
	tr.Type(path)
	if err := tr.Click("Save file"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(tc.Snapshot()) != 2 || tc.Snapshot()[1].Source != path || tc.Snapshot()[1].Name != "notes.txt" {
		t.Fatalf("UI file import = %+v", tc.Snapshot())
	}
	if !tr.HasText("Source: "+path) || !tr.HasText("old file text") {
		t.Fatalf("file card or preview missing: %v", tr.Texts())
	}
	if err := os.WriteFile(path, []byte("new file text"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("old file text") || tr.HasText("new file text") {
		t.Fatalf("file changed without an explicit refresh: %v", tr.Texts())
	}
	if err := tr.Click("Refresh from file"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("new file text") || tc.Snapshot()[1].Text != "new file text" || tc.Snapshot()[1].Revision != 2 {
		t.Fatalf("UI refresh = %+v", tc.Snapshot())
	}

	if err := tr.Click("Add snippet"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Save snippet"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText(locales.In("en").TaskContextErrName()) || !tr.HasText("Save snippet") {
		t.Fatalf("validation error did not preserve the form: %v", tr.Texts())
	}
	if len(tc.Snapshot()) != 2 {
		t.Fatalf("failed UI form changed the list: %+v", tc.Snapshot())
	}
	if err := tr.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()

	if err := tr.Click("Remove"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(tc.Snapshot()) != 1 {
		t.Fatalf("UI remove changed the wrong item: %+v", tc.Snapshot())
	}
	if err := tr.Click("Remove"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(tc.Snapshot()) != 0 || !tr.HasText(locales.In("en").TaskContextEmpty()) {
		t.Fatalf("final remove = %+v / %v", tc.Snapshot(), tr.Texts())
	}
}

func taskContextTester(tc *Context, dark bool) *ui.Tester {
	return taskContextTesterSize(tc, dark, 720, 1200)
}

func taskContextTesterSize(tc *Context, dark bool, width, height int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		wb := theme.ResolveWorkbenchTheme(dark)
		c.SetTheme(wb.Theme)
		ui.Scroll(c).Fill().Padding(16).Background(wb.Theme.Background).Children(func() {
			tc.View(c, wb)
		})
	}, width, height)
}

func TestTaskContextEmptyFilePreviewCopyAndRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tc := &Context{}
	item, err := tc.ImportFile(path, "Empty brief")
	if err != nil {
		t.Fatal(err)
	}
	tc.PreviewID = item.ID
	tr := taskContextTester(tc, false)
	tr.Frame()
	if !tr.HasText("Preview: Empty brief") || !tr.HasText("Source: "+path) {
		t.Fatalf("empty Snapshot preview missing: %v", tr.Texts())
	}
	if err := tr.Click(locales.In("en").CopyContextTextLabel()); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != "" {
		t.Fatalf("empty Snapshot copy = %q", got)
	}
	if err := os.WriteFile(path, []byte("filled later"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if tr.HasText("filled later") {
		t.Fatal("empty Snapshot reread the file without refresh")
	}
	if err := tr.Click("Refresh from file"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("filled later") {
		t.Fatalf("refreshed empty file preview missing: %v", tr.Texts())
	}
	if err := tr.Click(locales.In("en").CopyContextTextLabel()); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != "filled later" {
		t.Fatalf("refreshed Snapshot copy = %q", got)
	}
}

func TestTaskContextHeadlessPreviews(t *testing.T) {
	dir := os.Getenv("NAGI_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	path := filepath.Join(t.TempDir(), "interface-notes.txt")
	if err := os.WriteFile(path, []byte("Compact cards keep the exact saved text.\nRefresh is explicit; later file changes stay out of the Snapshot.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tc := &Context{}
	fileItem, err := tc.ImportFile(path, "Interface notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tc.AddSnippet("Review checklist", "Confirm Unicode: 你好 — café\nPreserve both lines."); err != nil {
		t.Fatal(err)
	}
	tc.PreviewID = fileItem.ID
	for _, dark := range []bool{false, true} {
		themeName := "light"
		if dark {
			themeName = "dark"
		}
		for _, width := range []int{340, 600} {
			tr := taskContextTesterSize(tc, dark, width, 900)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("task-context-%s-%d.png", themeName, width)), tr.Image())
		}
	}
}

func TestTaskContextSaveFailureKeepsEditorAndValues(t *testing.T) {
	tc := &Context{OnChange: func() error { return errors.New("store unavailable") }}
	tr := taskContextTester(tc, false)
	tr.Frame()
	if err := tr.Click("Add snippet"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click(locales.In("en").TaskContextNameLabel()); err != nil {
		t.Fatal(err)
	}
	tr.Type("Unsaved name")
	if err := tr.Click(locales.In("en").TaskContextTextLabel()); err != nil {
		t.Fatal(err)
	}
	tr.Type("Unsaved body")
	if err := tr.Click("Save snippet"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tc.FormOpen || tc.FormName != "Unsaved name" || tc.FormText != "Unsaved body" {
		t.Fatalf("save failure lost the editor: %+v", tc)
	}
	if len(tc.Snapshot()) != 0 {
		t.Fatalf("save failure committed an item: %+v", tc.Snapshot())
	}
	if !tr.HasText("store unavailable") || !tr.HasText("Save snippet") {
		t.Fatalf("save failure was not visible inline: %v", tr.Texts())
	}
}

func TestAttachFileSnapshotsPathReferencesBudgetAndRefresh(t *testing.T) {
	dir := t.TempDir()
	utf8Path := filepath.Join(dir, "notes.md")
	utf8Body := "Alpha\n中文 — café\n"
	if err := os.WriteFile(utf8Path, []byte(utf8Body), 0o600); err != nil {
		t.Fatal(err)
	}
	pdfPath := filepath.Join(dir, "report.pdf")
	pdfBytes := []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n")
	if err := os.WriteFile(pdfPath, pdfBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	badUTF8Path := filepath.Join(dir, "invalid.txt")
	if err := os.WriteFile(badUTF8Path, []byte{0xff, 0xfe, 0xfd, 0x01}, 0o600); err != nil {
		t.Fatal(err)
	}
	nulPath := filepath.Join(dir, "binary.txt")
	if err := os.WriteFile(nulPath, []byte("head\x00tail"), 0o600); err != nil {
		t.Fatal(err)
	}
	oversizePath := filepath.Join(dir, "large.log")
	oversizeBytes := make([]byte, MaxBytes+64)
	for i := range oversizeBytes {
		oversizeBytes[i] = 'x'
	}
	if err := os.WriteFile(oversizePath, oversizeBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(dir, "folder")
	if err := os.Mkdir(subDir, 0o700); err != nil {
		t.Fatal(err)
	}

	tc := &Context{}
	snap, err := tc.AttachFile(utf8Path, "")
	if err != nil {
		t.Fatalf("AttachFile(utf8) = %v", err)
	}
	if IsPathReference(snap) || PathReferenceSize(snap) != "" || snap.Text != utf8Body || snap.Source != utf8Path || snap.Name != "notes.md" {
		t.Fatalf("UTF-8 attachment = %+v", snap)
	}

	for _, tcCase := range []struct {
		name      string
		path      string
		size      int64
		strictErr error
	}{
		{"pdf", pdfPath, int64(len(pdfBytes)), ErrUnsupported},
		{"invalid-utf8", badUTF8Path, 4, ErrUTF8},
		{"nul", nulPath, 9, ErrNotText},
		{"oversize", oversizePath, int64(len(oversizeBytes)), ErrOversize},
	} {
		strict := &Context{}
		if _, err := strict.ImportFile(tcCase.path, ""); !errors.Is(err, tcCase.strictErr) {
			t.Fatalf("strict ImportFile(%s) = %v, want %v", tcCase.name, err, tcCase.strictErr)
		}
		ref, err := tc.AttachFile(tcCase.path, "")
		if err != nil {
			t.Fatalf("AttachFile(%s) = %v", tcCase.name, err)
		}
		if !IsPathReference(ref) || PathReferenceSize(ref) != FormatSize(tcCase.size) {
			t.Fatalf("path reference(%s) = %+v size=%q want=%q", tcCase.name, ref, PathReferenceSize(ref), FormatSize(tcCase.size))
		}
		if !strings.Contains(ref.Text, strconv.Quote(tcCase.path)) || ref.Source != tcCase.path {
			t.Fatalf("path reference(%s) body/source mismatch = %+v", tcCase.name, ref)
		}
	}

	beforeCount := tc.Count()
	for _, bad := range []string{"", "   ", filepath.Join(dir, "does-not-exist.bin"), subDir} {
		if _, err := tc.AttachFile(bad, ""); !errors.Is(err, ErrRead) {
			t.Fatalf("AttachFile(%q) = %v, want ErrRead", bad, err)
		}
	}
	if tc.Count() != beforeCount {
		t.Fatalf("rejected paths mutated list: got %d want %d", tc.Count(), beforeCount)
	}

	pdfItem := tc.Snapshot()[1]
	edited, err := tc.Edit(pdfItem.ID, "Renamed PDF", "attempted body overwrite")
	if err != nil {
		t.Fatalf("Edit(pathRef) = %v", err)
	}
	if edited.Name != "Renamed PDF" || edited.Text != pdfItem.Text || !IsPathReference(edited) || edited.Revision != 2 {
		t.Fatalf("Edit(pathRef) did not keep read-only canonical body: %+v", edited)
	}

	grownPDF := append(pdfBytes, make([]byte, 2048)...)
	if err := os.WriteFile(pdfPath, grownPDF, 0o600); err != nil {
		t.Fatal(err)
	}
	refreshedRef, err := tc.Refresh(pdfItem.ID)
	if err != nil {
		t.Fatalf("Refresh(grown pathRef) = %v", err)
	}
	if refreshedRef.ID != pdfItem.ID || refreshedRef.Revision != 3 || !IsPathReference(refreshedRef) || PathReferenceSize(refreshedRef) != FormatSize(int64(len(grownPDF))) {
		t.Fatalf("Refresh(grown pathRef) = %+v", refreshedRef)
	}

	upgradedText := "now small utf-8 text\n"
	if err := os.WriteFile(oversizePath, []byte(upgradedText), 0o600); err != nil {
		t.Fatal(err)
	}
	oversizeID := tc.Snapshot()[4].ID
	upgraded, err := tc.Refresh(oversizeID)
	if err != nil {
		t.Fatalf("Refresh(upgrade to utf8) = %v", err)
	}
	if upgraded.ID != oversizeID || upgraded.Revision != 2 || IsPathReference(upgraded) || upgraded.Text != upgradedText {
		t.Fatalf("Refresh(upgrade to utf8) = %+v", upgraded)
	}

	if err := os.Remove(pdfPath); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Refresh(pdfItem.ID); !errors.Is(err, ErrRead) {
		t.Fatalf("Refresh(deleted pathRef) = %v, want ErrRead", err)
	}
	kept, ok := tc.ItemByID(pdfItem.ID)
	if !ok || kept.Text != refreshedRef.Text || kept.Revision != refreshedRef.Revision {
		t.Fatalf("failed refresh lost prior pathRef: %+v", kept)
	}

	if IsPathReference(Item{Name: "spoof", Text: refreshedRef.Text}) {
		t.Fatal("snippet without source was classified as path reference")
	}
	if IsPathReference(Item{Name: "spoof", Source: utf8Path, Text: refreshedRef.Text}) {
		t.Fatal("mismatched source was classified as path reference")
	}

	budget := &Context{}
	bigChunk := strings.Repeat("b", MaxBytes-WrapperBytes("fill", "")-32)
	if _, err := budget.AddSnippet("fill", bigChunk); err != nil {
		t.Fatal(err)
	}
	if _, err := budget.AttachFile(utf8Path, ""); !errors.Is(err, ErrOversize) {
		t.Fatalf("AttachFile exceeding combined budget = %v, want ErrOversize", err)
	}
	if _, err := budget.AttachFile(badUTF8Path, ""); !errors.Is(err, ErrOversize) {
		t.Fatalf("path-reference AttachFile exceeding combined budget = %v, want ErrOversize", err)
	}
	if budget.Count() != 1 {
		t.Fatalf("budget overflow mutated items: %d", budget.Count())
	}

	failing := &Context{OnChange: func() error { return errors.New("disk full") }}
	if _, err := failing.AttachFile(utf8Path, ""); err == nil || failing.Count() != 0 {
		t.Fatalf("OnChange failure did not roll back UTF-8 attach: err=%v count=%d", err, failing.Count())
	}
	if _, err := failing.AttachFile(badUTF8Path, ""); err == nil || failing.Count() != 0 {
		t.Fatalf("OnChange failure did not roll back path-reference attach: err=%v count=%d", err, failing.Count())
	}
}

func writePreviewPNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}
