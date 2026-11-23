package materials

// Materials hold the GUI-owned list of named text snippets and explicit
// UTF-8 text-file snapshots sent with a user turn. It stays independent of the
// app model and of the runtime wire client: the caller projects snapshot
// values onto the runtime protocol at send time.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/markdown"
	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

const (
	MaxBytes  = 1024 * 1024
	ReadLimit = MaxBytes + 1
)

var (
	ErrName        = errors.New("a Material name is required")
	ErrUTF8        = errors.New("Material text is not valid UTF-8")
	ErrNotText     = errors.New("file is not a UTF-8 text file")
	ErrOversize    = errors.New("Materials exceed the 1 MiB send limit")
	ErrUnsupported = errors.New("unsupported file format; only UTF-8 text files are supported")
	ErrRead        = errors.New("could not read the Material file")
	ErrMissing     = errors.New("the Material no longer exists")
	ErrNoSource    = errors.New("this Material has no file source")
)

type Error struct {
	Kind   error
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Kind.Error()
	}
	return e.Kind.Error() + ": " + e.Detail
}

func (e *Error) Unwrap() error { return e.Kind }

func Detail(kind error, detail string) error {
	return &Error{Kind: kind, Detail: detail}
}

type Item = store.TaskContextItem

type FileForm struct {
	PathRevision uint64
}

// Context keeps saved snapshots together with explicit UI editor state.
// Saved item text is never re-read from disk implicitly; OnChange is the
// app-owned persistence path for committed snapshots.
type Context struct {
	Items []Item

	FormOpen  bool
	FormMode  string
	FormID    string
	FormName  string
	FormText  string
	FormPath  string
	FormError error

	FileForm           *FileForm
	FileRequest        uint64
	FileChoosing       bool
	FileChoiceFailed   bool
	FocusFileChooser   bool
	ChooseFile         func()
	FileChooserBlocked bool

	PreviewID   string
	ActionError error

	OnChange func() error
}

func (tc *Context) Count() int { return len(tc.Items) }

func (tc *Context) Snapshot() []Item {
	return store.CloneTaskContextItems(tc.Items)
}

func (tc *Context) persistChange(previous []Item) error {
	if tc.OnChange == nil {
		return nil
	}
	if err := tc.OnChange(); err != nil {
		tc.Items = previous
		return err
	}
	return nil
}

func (tc *Context) ItemByID(id string) (Item, bool) {
	for _, item := range tc.Items {
		if item.ID == id {
			return item, true
		}
	}
	return Item{}, false
}

func (tc *Context) addItem(name, source, text string) (Item, error) {
	item := Item{
		ID: store.NewTaskContextID(), Name: strings.TrimSpace(name), Source: source,
		Text: text, Revision: 1,
	}
	previous := tc.Snapshot()
	next := append(previous, item)
	if err := Validate(next); err != nil {
		return Item{}, err
	}
	tc.Items = next
	if err := tc.persistChange(previous); err != nil {
		return Item{}, err
	}
	return item, nil
}

// AddSnippet saves typed text. The name labels the source; Source stays empty
// because there is no file to refresh.
func (tc *Context) AddSnippet(name, text string) (Item, error) {
	return tc.addItem(name, "", text)
}

// ImportFile reads one explicit path into a snapshot. Later file changes do
// not affect the saved snapshot until Refresh is called.
func (tc *Context) ImportFile(path, name string) (Item, error) {
	text, source, err := readFile(path)
	if err != nil {
		return Item{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(source)
	}
	return tc.addItem(name, source, text)
}

// Refresh re-reads the original source path into a new revision. On any
// failure the previous snapshot is left unchanged.
func (tc *Context) Refresh(id string) (Item, error) {
	for index, current := range tc.Items {
		if current.ID != id {
			continue
		}
		if current.Source == "" {
			return Item{}, ErrNoSource
		}
		text, source, err := readFile(current.Source)
		if err != nil {
			return Item{}, err
		}
		previous := tc.Snapshot()
		next := current
		next.Source, next.Text, next.Revision = source, text, current.Revision+1
		items := store.CloneTaskContextItems(previous)
		items[index] = next
		if err := Validate(items); err != nil {
			return Item{}, err
		}
		tc.Items = items
		if err := tc.persistChange(previous); err != nil {
			return Item{}, err
		}
		return next, nil
	}
	return Item{}, ErrMissing
}

// Edit changes the label and text of a saved snapshot without touching disk.
func (tc *Context) Edit(id, name, text string) (Item, error) {
	for index, current := range tc.Items {
		if current.ID != id {
			continue
		}
		previous := tc.Snapshot()
		next := current
		next.Name, next.Text, next.Revision = strings.TrimSpace(name), text, current.Revision+1
		items := store.CloneTaskContextItems(previous)
		items[index] = next
		if err := Validate(items); err != nil {
			return Item{}, err
		}
		tc.Items = items
		if err := tc.persistChange(previous); err != nil {
			return Item{}, err
		}
		return next, nil
	}
	return Item{}, ErrMissing
}

func (tc *Context) Remove(id string) bool {
	for index, item := range tc.Items {
		if item.ID != id {
			continue
		}
		previous := tc.Snapshot()
		tc.Items = append(tc.Items[:index], tc.Items[index+1:]...)
		if err := tc.persistChange(previous); err != nil {
			tc.ActionError = err
			return false
		}
		if tc.PreviewID == id {
			tc.PreviewID = ""
		}
		if tc.FormID == id {
			tc.CancelForm()
		}
		return true
	}
	return false
}

// ClearSubmitted removes only the exact versions handed to an accepted send.
// A snapshot edited while the send was pending survives for the next turn.
func (tc *Context) ClearSubmitted(submitted []Item) int {
	kept := tc.Items[:0]
	removed := 0
	for _, current := range tc.Items {
		matched := false
		for _, sent := range submitted {
			if sent.ID == current.ID && sent.Revision == current.Revision {
				matched = true
				break
			}
		}
		if matched {
			removed++
			if tc.PreviewID == current.ID {
				tc.PreviewID = ""
			}
			continue
		}
		kept = append(kept, current)
	}
	tc.Items = kept
	return removed
}

func (tc *Context) CancelForm() {
	tc.FileForm = nil
	tc.FileRequest++
	tc.FileChoosing, tc.FileChoiceFailed, tc.FocusFileChooser = false, false, false
	tc.FormOpen = false
	tc.FormMode, tc.FormID = "", ""
	tc.FormName, tc.FormText, tc.FormPath, tc.FormError = "", "", "", nil
}

func (tc *Context) OpenSnippetForm() {
	tc.ActionError = nil
	tc.CancelForm()
	tc.FormOpen, tc.FormMode = true, "snippet"
}

func (tc *Context) OpenFileForm() {
	tc.ActionError = nil
	tc.CancelForm()
	tc.FormOpen, tc.FormMode = true, "file"
	tc.FileForm = &FileForm{}
}

func (tc *Context) OpenEditForm(id string) {
	item, ok := tc.ItemByID(id)
	if !ok {
		tc.ActionError = ErrMissing
		return
	}
	tc.ActionError = nil
	tc.CancelForm()
	tc.FormOpen, tc.FormMode, tc.FormID = true, "edit", item.ID
	tc.FormName, tc.FormText, tc.FormPath = item.Name, item.Text, item.Source
}

func (tc *Context) SaveForm() {
	var (
		item Item
		err  error
	)
	switch tc.FormMode {
	case "snippet":
		item, err = tc.AddSnippet(tc.FormName, tc.FormText)
	case "file":
		item, err = tc.ImportFile(tc.FormPath, tc.FormName)
	case "edit":
		item, err = tc.Edit(tc.FormID, tc.FormName, tc.FormText)
	default:
		return
	}
	if err != nil {
		tc.FormError = err
		return
	}
	tc.ActionError = nil
	tc.CancelForm()
	tc.PreviewID = item.ID
}

func (tc *Context) refreshItem(id string) {
	if _, err := tc.Refresh(id); err != nil {
		tc.ActionError = err
		return
	}
	tc.ActionError = nil
}

func Validate(items []Item) error {
	total := 0
	for _, item := range items {
		if strings.TrimSpace(item.Name) == "" {
			return ErrName
		}
		if !utf8.ValidString(item.Text) {
			return ErrUTF8
		}
		total += WrapperBytes(item.InputName(), item.Text)
		if total > MaxBytes {
			return Detail(ErrOversize, fmt.Sprintf("combined Materials need %d bytes", total))
		}
	}
	return nil
}

func WrapperBytes(name, text string) int {
	data, _ := json.Marshal(pi.ContextInput{Name: name, Text: text})
	return len(data) + 1
}

var binaryExtensions = map[string]bool{
	".pdf": true, ".doc": true, ".docx": true, ".odt": true, ".rtf": true,
	".xls": true, ".xlsx": true, ".ods": true, ".ppt": true, ".pptx": true, ".odp": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".tif": true, ".tiff": true,
	".zip": true, ".gz": true, ".tar": true, ".7z": true, ".rar": true,
	".bin": true, ".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".o": true, ".a": true, ".wasm": true, ".mp3": true, ".mp4": true,
	".mov": true, ".avi": true, ".sqlite": true, ".db": true,
}

func readFile(path string) (text, source string, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", Detail(ErrRead, "enter a file path")
	}
	ext := strings.ToLower(filepath.Ext(path))
	if binaryExtensions[ext] {
		return "", "", Detail(ErrUnsupported, ext)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", Detail(ErrRead, err.Error())
	}
	file, err := os.Open(absolute)
	if err != nil {
		return "", "", Detail(ErrRead, err.Error())
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, ReadLimit))
	if err != nil {
		return "", "", Detail(ErrRead, err.Error())
	}
	if len(data) > MaxBytes {
		return "", "", Detail(ErrOversize, filepath.Base(absolute)+" is larger than 1 MiB")
	}
	if !utf8.Valid(data) {
		return "", "", Detail(ErrUTF8, filepath.Base(absolute))
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", "", Detail(ErrNotText, filepath.Base(absolute))
	}
	return string(data), absolute, nil
}

func Inputs(items []Item, extra ...pi.ContextInput) []pi.ContextInput {
	inputs := make([]pi.ContextInput, 0, len(items)+len(extra))
	for _, item := range items {
		inputs = append(inputs, pi.ContextInput{Name: item.InputName(), Text: item.Text})
	}
	return append(inputs, extra...)
}

// ValidateRuntimeInputs includes app-injected context in the Pi request budget.
func ValidateRuntimeInputs(inputs []pi.ContextInput) error {
	total := 0
	for _, input := range inputs {
		total += WrapperBytes(input.Name, input.Text)
		if total > MaxBytes {
			return Detail(ErrOversize, fmt.Sprintf("combined caller context needs %d bytes", total))
		}
	}
	return nil
}

// ReadOnlyView renders history materials without editor/resend controls.
func ReadOnlyView(c *ui.Context, wb theme.WorkbenchTheme, items []Item) {
	if len(items) == 0 {
		return
	}
	t := wb.Theme
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, locales.In(c.Locale()).TaskContextItemsLabel(len(items))).FontSize(theme.TypeLabel).FontWeight(400).FixedLineHeight(theme.LineLabel).TextColor(t.TextMuted)
		for index, item := range items {
			item := item
			key := item.ID
			if key == "" {
				key = fmt.Sprintf("readonly-context-%d", index)
			}
			ui.Column(c).Key(key).Padding(8, 0).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Gap(4).Children(func() {
				ui.Text(c, item.Name).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
				if item.Source != "" {
					ui.Text(c, locales.In(c.Locale()).TaskContextSource(item.Source)).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
				}
				ui.Scroll(c).Height(96).Padding(4, 0).Children(func() {
					markdown.Reader(c, item.Text, markdown.ReaderOptions{PlainText: true, CopyLabel: locales.In(c.Locale()).CopyContextTextLabel()}).
						FontSize(theme.TypeBody).FixedLineHeight(theme.LineBody).TextColor(t.Text)
				})
			})
		}
	})
}

// View renders the compact list and its explicit editor, depending only on the workbench theme.
func (tc *Context) View(c *ui.Context, wb theme.WorkbenchTheme) {
	t := wb.Theme
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.In(c.Locale()).TaskContextHeading()).FontSize(theme.TypeLabel).FontWeight(400).FixedLineHeight(theme.LineLabel).TextColor(t.Text)
			ui.Spacer(c)
			ui.Text(c, locales.In(c.Locale()).TaskContextSize(len(tc.Items), sizeLabel(tc))).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
		})
		ui.Text(c, locales.In(c.Locale()).TaskContextIntro()).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)

		if tc.ActionError != nil {
			ui.Text(c, ErrorText(locales.In(c.Locale()), tc.ActionError)).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.Danger)
		}
		if !tc.FormOpen {
			ui.Row(c).Gap(8).Children(func() {
				theme.SecondaryButton(c, locales.In(c.Locale()).TaskContextAddSnippet()).OnClick(func() { tc.OpenSnippetForm() })
				theme.SecondaryButton(c, locales.In(c.Locale()).TaskContextAddFile()).OnClick(func() { tc.OpenFileForm() })
			})
		} else {
			tc.formView(c, wb)
		}

		if len(tc.Items) == 0 {
			ui.Text(c, locales.In(c.Locale()).TaskContextEmpty()).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
		}
		for _, item := range tc.Items {
			item := item
			ui.Column(c).Key(item.ID).Padding(8, 0).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Gap(4).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, item.Name).FontSize(theme.TypeLabel).FontWeight(400).FixedLineHeight(theme.LineLabel).TextColor(t.Text)
					ui.Spacer(c)
					ui.Text(c, sourceLabel(locales.In(c.Locale()), item)).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
				})
				if item.Source != "" {
					ui.Text(c, locales.In(c.Locale()).TaskContextSource(item.Source)).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
				}
				ui.Row(c).Gap(8).Children(func() {
					theme.SecondaryButton(c, locales.In(c.Locale()).TaskContextPreview()).OnClick(func() { tc.PreviewID = item.ID })
					theme.SecondaryButton(c, locales.In(c.Locale()).TaskContextEdit()).OnClick(func() { tc.OpenEditForm(item.ID) })
					if item.Source != "" {
						theme.SecondaryButton(c, locales.In(c.Locale()).TaskContextRefreshFile()).OnClick(func() { tc.refreshItem(item.ID) })
					}
					theme.SecondaryButton(c, locales.In(c.Locale()).TaskContextRemove()).OnClick(func() { tc.Remove(item.ID) })
				})
				if tc.PreviewID == item.ID {
					tc.previewView(c, wb, item)
				}
			})
		}
	})
}

func (tc *Context) formView(c *ui.Context, wb theme.WorkbenchTheme) {
	t := wb.Theme
	ui.Column(c).Padding(12, 0).Gap(4).Children(func() {
		title := locales.In(c.Locale()).TaskContextFormSnippet()
		if tc.FormMode == "file" {
			title = locales.In(c.Locale()).TaskContextFormFile()
		} else if tc.FormMode == "edit" {
			title = locales.In(c.Locale()).TaskContextFormEdit()
		}
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(theme.TypeLabel).FontWeight(400).FixedLineHeight(theme.LineLabel).TextColor(t.Text)
			ui.Spacer(c)
			theme.SecondaryButton(c, locales.In(c.Locale()).TaskContextFormCancel()).OnClick(func() { tc.CancelForm() })
		})
		switch tc.FormMode {
		case "file":
			ui.Text(c, locales.In(c.Locale()).TaskContextFileNameOptional()).FontSize(theme.TypeLabel).FontWeight(400).FixedLineHeight(theme.LineLabel).TextColor(t.Text)
			theme.PreferenceInput(c, &tc.FormName, false, wb.InputBorder).Label(locales.In(c.Locale()).TaskContextFileLabel()).Placeholder(locales.In(c.Locale()).TaskContextFilePlaceholder())
			msgs := locales.In(c.Locale())
			choose := theme.SecondaryButton(c, msgs.TaskContextChooseFile()).Label(msgs.TaskContextChooseFile()).Disabled(tc.FileChoosing || tc.FileChooserBlocked || tc.ChooseFile == nil).OnClick(func() { tc.ChooseFile() })
			if tc.FocusFileChooser {
				choose.Focus().ScrollIntoView()
				tc.FocusFileChooser = false
			}
			if tc.FileChoosing {
				ui.Text(c, msgs.TaskContextChoosingFile()).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
			}
			if tc.FileChoiceFailed {
				ui.Text(c, msgs.TaskContextChooseFileFailed()).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.Danger)
			}
			ui.Text(c, msgs.TaskContextChooseFileHint()).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
			ui.Text(c, locales.In(c.Locale()).TaskContextPath()).FontSize(theme.TypeLabel).FontWeight(400).FixedLineHeight(theme.LineLabel).TextColor(t.Text)
			theme.PreferenceInput(c, &tc.FormPath, false, wb.InputBorder).Label(locales.In(c.Locale()).TaskContextFilePathLabel()).Placeholder(locales.In(c.Locale()).TaskContextFilePathPlaceholder()).OnChange(func() {
				tc.FileForm.PathRevision++
				tc.FileRequest++
				tc.FileChoosing, tc.FocusFileChooser = false, false
			})
			ui.Text(c, locales.In(c.Locale()).TaskContextFileRules()).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
		default:
			ui.Text(c, locales.In(c.Locale()).TaskContextName()).FontSize(theme.TypeLabel).FontWeight(400).FixedLineHeight(theme.LineLabel).TextColor(t.Text)
			theme.PreferenceInput(c, &tc.FormName, false, wb.InputBorder).Label(locales.In(c.Locale()).TaskContextNameLabel()).Placeholder(locales.In(c.Locale()).TaskContextNamePlaceholder())
			ui.Text(c, locales.In(c.Locale()).TaskContextText()).FontSize(theme.TypeLabel).FontWeight(400).FixedLineHeight(theme.LineLabel).TextColor(t.Text)
			theme.FormTextArea(c, &tc.FormText, false, wb.InputBorder).Label(locales.In(c.Locale()).TaskContextTextLabel()).Height(84).Placeholder(locales.In(c.Locale()).TaskContextTextPlaceholder())
			if tc.FormMode == "edit" && tc.FormPath != "" {
				ui.Text(c, locales.In(c.Locale()).TaskContextEditHint()).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
			}
		}
		if tc.FormError != nil {
			ui.Text(c, ErrorText(locales.In(c.Locale()), tc.FormError)).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.Danger)
		}
		msgs := locales.In(c.Locale())
		label := msgs.TaskContextSaveSnippet()
		if tc.FormMode == "file" {
			label = msgs.TaskContextSaveFile()
		} else if tc.FormMode == "edit" {
			label = msgs.TaskContextSaveChanges()
		}
		theme.SecondaryButton(c, label).Label(label).OnClick(func() { tc.SaveForm() })
	})
}

func (tc *Context) previewView(c *ui.Context, wb theme.WorkbenchTheme, item Item) {
	t := wb.Theme
	ui.Column(c).Gap(4).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.In(c.Locale()).TaskContextPreviewTitle(item.Name)).FontSize(theme.TypeLabel).FontWeight(400).FixedLineHeight(theme.LineLabel).TextColor(t.Text)
			ui.Spacer(c)
			theme.SecondaryButton(c, locales.In(c.Locale()).TaskContextClosePreview()).OnClick(func() { tc.PreviewID = "" })
		})
		if item.Source != "" {
			ui.Text(c, item.Source).FontSize(theme.TypeMeta).FixedLineHeight(theme.LineMeta).TextColor(t.TextMuted)
		}
		ui.Scroll(c).Height(220).Padding(4, 0).Children(func() {
			markdown.Reader(c, item.Text, markdown.ReaderOptions{PlainText: true, CopyLabel: locales.In(c.Locale()).CopyContextTextLabel()}).
				FontSize(theme.TypeBody).FixedLineHeight(theme.LineBody).TextColor(t.Text)
		})
	})
}

func ErrorText(msgs locales.Messages, err error) string {
	detail := ""
	var detailErr *Error
	if errors.As(err, &detailErr) {
		detail = detailErr.Detail
	}
	switch {
	case errors.Is(err, ErrName):
		return msgs.TaskContextErrName()
	case errors.Is(err, ErrUTF8):
		if detail != "" {
			return msgs.TaskContextErrUTF8Detail(detail)
		}
		return msgs.TaskContextErrUTF8()
	case errors.Is(err, ErrNotText):
		if detail != "" {
			return msgs.TaskContextErrNotTextDetail(detail)
		}
		return msgs.TaskContextErrNotText()
	case errors.Is(err, ErrOversize):
		if detail != "" {
			return msgs.TaskContextErrOversizeDetail(detail)
		}
		return msgs.TaskContextErrOversize()
	case errors.Is(err, ErrUnsupported):
		if detail != "" {
			return msgs.TaskContextErrUnsupportedDetail(detail)
		}
		return msgs.TaskContextErrUnsupported()
	case errors.Is(err, ErrRead):
		if detail != "" {
			return msgs.TaskContextErrReadDetail(detail)
		}
		return msgs.TaskContextErrRead()
	case errors.Is(err, ErrMissing):
		return msgs.TaskContextErrMissing()
	case errors.Is(err, ErrNoSource):
		return msgs.TaskContextErrNoSource()
	default:
		return err.Error()
	}
}

func sourceLabel(msgs locales.Messages, item Item) string {
	if item.Source == "" {
		return msgs.TaskContextSourceNamed()
	}
	return msgs.TaskContextSourceFile(strconv.FormatUint(item.Revision, 10))
}

func sizeLabel(tc *Context) string {
	total := 0
	for _, item := range tc.Items {
		total += WrapperBytes(item.InputName(), item.Text)
	}
	if total < 1024 {
		return fmt.Sprintf("%d B", total)
	}
	return fmt.Sprintf("%.1f KiB", float64(total)/1024)
}
