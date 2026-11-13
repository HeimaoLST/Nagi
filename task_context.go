package main

// Task context is the GUI-owned list of named text snippets and explicit
// UTF-8 text-file snapshots sent with a user turn. It stays independent of the
// app model and of the runtime wire client: the caller projects snapshot
// values onto the runtime protocol at send time.

import (
	"bytes"
	"crypto/rand"
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

	"github.com/HeimaoLST/Nagi/locales"
)

// The runtime bounds the sum of serialized caller-context wrappers carried by
// one user turn (src/agent/message.rs MAX_CONTEXT_BYTES). Validate the same
// budget here so a failed send cannot be the first place an oversize snapshot
// is reported.
const (
	taskContextMaxBytes  = 1024 * 1024
	taskContextReadLimit = taskContextMaxBytes + 1
)

var (
	errTaskContextName        = errors.New("context name is required")
	errTaskContextUTF8        = errors.New("context text is not valid UTF-8")
	errTaskContextNotText     = errors.New("file is not a UTF-8 text file")
	errTaskContextOversize    = errors.New("context exceeds the 1 MiB send limit")
	errTaskContextUnsupported = errors.New("unsupported file format; only UTF-8 text files are supported")
	errTaskContextRead        = errors.New("could not read context file")
	errTaskContextMissing     = errors.New("context item no longer exists")
	errTaskContextNoSource    = errors.New("this context item has no file source")
)

type taskContextError struct {
	kind   error
	detail string
}

func (e *taskContextError) Error() string {
	if e.detail == "" {
		return e.kind.Error()
	}
	return e.kind.Error() + ": " + e.detail
}

func (e *taskContextError) Unwrap() error { return e.kind }

func taskContextDetail(kind error, detail string) error {
	return &taskContextError{kind: kind, detail: detail}
}

type taskContextItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Source   string `json:"source,omitempty"`
	Text     string `json:"text"`
	Revision uint64 `json:"revision"`
}

type taskContextFileForm struct {
	pathRevision uint64
}

// taskContext keeps the saved snapshots together with the small, explicit UI
// editor state. Saved item text is never re-read from disk implicitly.
type taskContext struct {
	items []taskContextItem

	formOpen  bool
	formMode  string // "snippet", "file", or "edit"
	formID    string
	formName  string
	formText  string
	formPath  string
	formError error

	fileForm           *taskContextFileForm
	fileRequest        uint64
	fileChoosing       bool
	fileChoiceFailed   bool
	focusFileChooser   bool
	chooseFile         func()
	fileChooserBlocked bool

	previewID   string
	actionError error

	// onChange is the single app-owned persistence path for committed snapshots.
	// UI editor state is intentionally not persisted.
	onChange func() error
}

func newTaskContextID() string {
	return "context-" + rand.Text()
}

// inputName is the single label projection used for both the size budget and
// the caller-context wire value. Named snippets stay just their name; file
// snapshots make the explicit source path visible without changing Text.
func (item taskContextItem) inputName() string {
	if item.Source == "" {
		return item.Name
	}
	return item.Name + " · file " + item.Source
}

func cloneTaskContextItems(items []taskContextItem) []taskContextItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]taskContextItem, len(items))
	copy(out, items)
	return out
}

func (tc *taskContext) count() int { return len(tc.items) }

func (tc *taskContext) snapshot() []taskContextItem {
	return cloneTaskContextItems(tc.items)
}

func (tc *taskContext) persistChange(previous []taskContextItem) error {
	if tc.onChange == nil {
		return nil
	}
	if err := tc.onChange(); err != nil {
		tc.items = previous
		return err
	}
	return nil
}

func (tc *taskContext) itemByID(id string) (taskContextItem, bool) {
	for _, item := range tc.items {
		if item.ID == id {
			return item, true
		}
	}
	return taskContextItem{}, false
}

func (tc *taskContext) addItem(name, source, text string) (taskContextItem, error) {
	item := taskContextItem{
		ID: newTaskContextID(), Name: strings.TrimSpace(name), Source: source,
		Text: text, Revision: 1,
	}
	previous := tc.snapshot()
	next := append(previous, item)
	if err := validateTaskContext(next); err != nil {
		return taskContextItem{}, err
	}
	tc.items = next
	if err := tc.persistChange(previous); err != nil {
		return taskContextItem{}, err
	}
	return item, nil
}

// AddSnippet saves typed text. The name labels the source; Source stays empty
// because there is no file to refresh.
func (tc *taskContext) AddSnippet(name, text string) (taskContextItem, error) {
	return tc.addItem(name, "", text)
}

// ImportFile reads one explicit path into a snapshot. Later file changes do
// not affect the saved snapshot until Refresh is called.
func (tc *taskContext) ImportFile(path, name string) (taskContextItem, error) {
	text, source, err := readTaskContextFile(path)
	if err != nil {
		return taskContextItem{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(source)
	}
	return tc.addItem(name, source, text)
}

// Refresh re-reads the original source path into a new revision. On any
// failure the previous snapshot is left unchanged.
func (tc *taskContext) Refresh(id string) (taskContextItem, error) {
	for index, current := range tc.items {
		if current.ID != id {
			continue
		}
		if current.Source == "" {
			return taskContextItem{}, errTaskContextNoSource
		}
		text, source, err := readTaskContextFile(current.Source)
		if err != nil {
			return taskContextItem{}, err
		}
		previous := tc.snapshot()
		next := current
		next.Source, next.Text, next.Revision = source, text, current.Revision+1
		items := cloneTaskContextItems(previous)
		items[index] = next
		if err := validateTaskContext(items); err != nil {
			return taskContextItem{}, err
		}
		tc.items = items
		if err := tc.persistChange(previous); err != nil {
			return taskContextItem{}, err
		}
		return next, nil
	}
	return taskContextItem{}, errTaskContextMissing
}

// Edit changes the label and text of a saved snapshot without touching disk.
func (tc *taskContext) Edit(id, name, text string) (taskContextItem, error) {
	for index, current := range tc.items {
		if current.ID != id {
			continue
		}
		previous := tc.snapshot()
		next := current
		next.Name, next.Text, next.Revision = strings.TrimSpace(name), text, current.Revision+1
		items := cloneTaskContextItems(previous)
		items[index] = next
		if err := validateTaskContext(items); err != nil {
			return taskContextItem{}, err
		}
		tc.items = items
		if err := tc.persistChange(previous); err != nil {
			return taskContextItem{}, err
		}
		return next, nil
	}
	return taskContextItem{}, errTaskContextMissing
}

func (tc *taskContext) Remove(id string) bool {
	for index, item := range tc.items {
		if item.ID != id {
			continue
		}
		previous := tc.snapshot()
		tc.items = append(tc.items[:index], tc.items[index+1:]...)
		if err := tc.persistChange(previous); err != nil {
			tc.actionError = err
			return false
		}
		if tc.previewID == id {
			tc.previewID = ""
		}
		if tc.formID == id {
			tc.cancelForm()
		}
		return true
	}
	return false
}

// clearSubmitted removes only the exact versions handed to an accepted send.
// A snapshot edited while the send was pending survives for the next turn.
func (tc *taskContext) clearSubmitted(submitted []taskContextItem) int {
	kept := tc.items[:0]
	removed := 0
	for _, current := range tc.items {
		matched := false
		for _, sent := range submitted {
			if sent.ID == current.ID && sent.Revision == current.Revision {
				matched = true
				break
			}
		}
		if matched {
			removed++
			if tc.previewID == current.ID {
				tc.previewID = ""
			}
			continue
		}
		kept = append(kept, current)
	}
	tc.items = kept
	return removed
}

func (tc *taskContext) cancelForm() {
	tc.fileForm = nil
	tc.fileRequest++
	tc.fileChoosing, tc.fileChoiceFailed, tc.focusFileChooser = false, false, false
	tc.formOpen = false
	tc.formMode, tc.formID = "", ""
	tc.formName, tc.formText, tc.formPath, tc.formError = "", "", "", nil
}

func (tc *taskContext) openSnippetForm() {
	tc.actionError = nil
	tc.cancelForm()
	tc.formOpen, tc.formMode = true, "snippet"
}

func (tc *taskContext) openFileForm() {
	tc.actionError = nil
	tc.cancelForm()
	tc.formOpen, tc.formMode = true, "file"
	tc.fileForm = &taskContextFileForm{}
}

func (tc *taskContext) openEditForm(id string) {
	item, ok := tc.itemByID(id)
	if !ok {
		tc.actionError = errTaskContextMissing
		return
	}
	tc.actionError = nil
	tc.cancelForm()
	tc.formOpen, tc.formMode, tc.formID = true, "edit", item.ID
	tc.formName, tc.formText, tc.formPath = item.Name, item.Text, item.Source
}

func (tc *taskContext) saveForm() {
	var (
		item taskContextItem
		err  error
	)
	switch tc.formMode {
	case "snippet":
		item, err = tc.AddSnippet(tc.formName, tc.formText)
	case "file":
		item, err = tc.ImportFile(tc.formPath, tc.formName)
	case "edit":
		item, err = tc.Edit(tc.formID, tc.formName, tc.formText)
	default:
		return
	}
	if err != nil {
		tc.formError = err
		return
	}
	tc.actionError = nil
	tc.cancelForm()
	tc.previewID = item.ID
}

func (tc *taskContext) refreshItem(id string) {
	if _, err := tc.Refresh(id); err != nil {
		tc.actionError = err
		return
	}
	tc.actionError = nil
}

func validateTaskContext(items []taskContextItem) error {
	total := 0
	for _, item := range items {
		if strings.TrimSpace(item.Name) == "" {
			return errTaskContextName
		}
		if !utf8.ValidString(item.Text) {
			return errTaskContextUTF8
		}
		total += taskContextWrapperBytes(item.inputName(), item.Text)
		if total > taskContextMaxBytes {
			return taskContextDetail(errTaskContextOversize, fmt.Sprintf("combined context needs %d bytes", total))
		}
	}
	return nil
}

// taskContextWrapperBytes mirrors the runtime's serialized caller_context
// wrapper for budget purposes. Go's JSON escaping is a little more eager than
// serde's, so the estimate is conservative; it is not a wire DTO.
func taskContextWrapperBytes(name, text string) int {
	data, _ := json.Marshal(struct {
		Type  string `json:"type"`
		Label string `json:"label"`
		Name  string `json:"name"`
		Text  string `json:"text"`
	}{
		Type:  "caller_context",
		Label: "Caller-provided context (user data)",
		Name:  name,
		Text:  text,
	})
	return len(data)
}

var taskContextBinaryExtensions = map[string]bool{
	".pdf": true, ".doc": true, ".docx": true, ".odt": true, ".rtf": true,
	".xls": true, ".xlsx": true, ".ods": true, ".ppt": true, ".pptx": true, ".odp": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".tif": true, ".tiff": true,
	".zip": true, ".gz": true, ".tar": true, ".7z": true, ".rar": true,
	".bin": true, ".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".o": true, ".a": true, ".wasm": true, ".mp3": true, ".mp4": true,
	".mov": true, ".avi": true, ".sqlite": true, ".db": true,
}

func readTaskContextFile(path string) (text, source string, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", taskContextDetail(errTaskContextRead, "enter a file path")
	}
	ext := strings.ToLower(filepath.Ext(path))
	if taskContextBinaryExtensions[ext] {
		return "", "", taskContextDetail(errTaskContextUnsupported, ext)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", taskContextDetail(errTaskContextRead, err.Error())
	}
	file, err := os.Open(absolute)
	if err != nil {
		return "", "", taskContextDetail(errTaskContextRead, err.Error())
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, taskContextReadLimit))
	if err != nil {
		return "", "", taskContextDetail(errTaskContextRead, err.Error())
	}
	if len(data) > taskContextMaxBytes {
		return "", "", taskContextDetail(errTaskContextOversize, filepath.Base(absolute)+" is larger than 1 MiB")
	}
	if !utf8.Valid(data) {
		return "", "", taskContextDetail(errTaskContextUTF8, filepath.Base(absolute))
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", "", taskContextDetail(errTaskContextNotText, filepath.Base(absolute))
	}
	return string(data), absolute, nil
}

// taskContextInputs is the single snapshot-to-wire projection used by every
// sender (conversation, issue run/continue, and module tests).
func taskContextInputs(items []taskContextItem) []runtimeContextInput {
	inputs := make([]runtimeContextInput, 0, len(items))
	for _, item := range items {
		inputs = append(inputs, runtimeContextInput{Name: item.inputName(), Text: item.Text})
	}
	return inputs
}

// validateRuntimeContextInputs checks the actual caller-context wire budget,
// including app-injected internal context. It never truncates or drops user
// material; the caller must fail before task.send and preserve the draft.
func validateRuntimeContextInputs(inputs []runtimeContextInput) error {
	total := 0
	for _, input := range inputs {
		total += taskContextWrapperBytes(input.Name, input.Text)
		if total > taskContextMaxBytes {
			return taskContextDetail(errTaskContextOversize, fmt.Sprintf("combined caller context needs %d bytes", total))
		}
	}
	return nil
}

// taskContextUserContent separates a Runtime user message's ordinary text from
// actual caller_context parts. Ordinary text that merely resembles JSON stays
// text; only the Runtime envelope type and label are recognized.
func taskContextUserContent(raw json.RawMessage) (string, []taskContextItem) {
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return plain, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil
	}
	var text strings.Builder
	var items []taskContextItem
	bodySeen := false
	for _, part := range parts {
		switch part.Type {
		case "image":
			text.WriteString("[image]")
		case "text":
			if !bodySeen {
				// Runtime places the nonempty GUI body first, then caller
				// context wrappers. The wire has no origin tag, so a first
				// context-only wrapper from another caller stays literal.
				text.WriteString(part.Text)
				bodySeen = true
				continue
			}
			if item, ok := taskContextEnvelope(part.Text); ok {
				items = append(items, item)
			} else {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String(), items
}

func taskContextEnvelope(text string) (taskContextItem, bool) {
	var envelope struct {
		Type  string `json:"type"`
		Label string `json:"label"`
		Name  string `json:"name"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		return taskContextItem{}, false
	}
	if envelope.Type != "caller_context" || envelope.Label != "Caller-provided context (user data)" {
		return taskContextItem{}, false
	}
	return taskContextItem{Name: envelope.Name, Text: envelope.Text, Revision: 1}, true
}

// taskContextReadOnlyView renders history materials without editor/resend
// controls. The item shape is the same immutable snapshot value type.
func taskContextReadOnlyView(c *ui.Context, theme workbenchTheme, items []taskContextItem) {
	if len(items) == 0 {
		return
	}
	t := theme.Theme
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, locales.In(c.Locale()).TaskContextItemsLabel(len(items))).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.TextMuted)
		for index, item := range items {
			item := item
			key := item.ID
			if key == "" {
				key = fmt.Sprintf("readonly-context-%d", index)
			}
			ui.Column(c).Key(key).Padding(8, 0).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Gap(4).Children(func() {
				ui.Text(c, item.Name).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				if item.Source != "" {
					ui.Text(c, locales.In(c.Locale()).TaskContextSource(item.Source)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				}
				ui.Scroll(c).Height(96).Padding(4, 0).Children(func() {
					markdownReader(c, item.Text, markdownReaderOptions{PlainText: true, CopyLabel: locales.In(c.Locale()).CopyContextTextLabel()}).
						FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
				})
			})
		}
	})
}

// view renders the compact list and its explicit editor. It depends only on
// the workbench theme, not on the app model.
func (tc *taskContext) view(c *ui.Context, theme workbenchTheme) {
	t := theme.Theme
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.In(c.Locale()).TaskContextHeading()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			ui.Spacer(c)
			ui.Text(c, locales.In(c.Locale()).TaskContextSize(len(tc.items), taskContextSizeLabel(tc))).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		})
		ui.Text(c, locales.In(c.Locale()).TaskContextIntro()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)

		if tc.actionError != nil {
			ui.Text(c, taskContextErrorText(locales.In(c.Locale()), tc.actionError)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
		}
		if !tc.formOpen {
			ui.Row(c).Gap(8).Children(func() {
				secondaryButton(c, locales.In(c.Locale()).TaskContextAddSnippet()).OnClick(func() { tc.openSnippetForm() })
				secondaryButton(c, locales.In(c.Locale()).TaskContextAddFile()).OnClick(func() { tc.openFileForm() })
			})
		} else {
			tc.formView(c, theme)
		}

		if len(tc.items) == 0 {
			ui.Text(c, locales.In(c.Locale()).TaskContextEmpty()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
		for _, item := range tc.items {
			item := item
			ui.Column(c).Key(item.ID).Padding(8, 0).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Gap(4).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, item.Name).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
					ui.Spacer(c)
					ui.Text(c, taskContextSourceLabel(locales.In(c.Locale()), item)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				})
				if item.Source != "" {
					ui.Text(c, locales.In(c.Locale()).TaskContextSource(item.Source)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
				}
				ui.Row(c).Gap(8).Children(func() {
					secondaryButton(c, locales.In(c.Locale()).TaskContextPreview()).OnClick(func() { tc.previewID = item.ID })
					secondaryButton(c, locales.In(c.Locale()).TaskContextEdit()).OnClick(func() { tc.openEditForm(item.ID) })
					if item.Source != "" {
						secondaryButton(c, locales.In(c.Locale()).TaskContextRefreshFile()).OnClick(func() { tc.refreshItem(item.ID) })
					}
					secondaryButton(c, locales.In(c.Locale()).TaskContextRemove()).OnClick(func() { tc.Remove(item.ID) })
				})
				if tc.previewID == item.ID {
					tc.previewView(c, theme, item)
				}
			})
		}
	})
}

func (tc *taskContext) formView(c *ui.Context, theme workbenchTheme) {
	t := theme.Theme
	ui.Column(c).Padding(12, 0).Gap(4).Children(func() {
		title := locales.In(c.Locale()).TaskContextFormSnippet()
		if tc.formMode == "file" {
			title = locales.In(c.Locale()).TaskContextFormFile()
		} else if tc.formMode == "edit" {
			title = locales.In(c.Locale()).TaskContextFormEdit()
		}
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			ui.Spacer(c)
			secondaryButton(c, locales.In(c.Locale()).TaskContextFormCancel()).OnClick(func() { tc.cancelForm() })
		})
		switch tc.formMode {
		case "file":
			ui.Text(c, locales.In(c.Locale()).TaskContextFileNameOptional()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			preferenceInput(c, &tc.formName, false).Label(locales.In(c.Locale()).TaskContextFileLabel()).Placeholder(locales.In(c.Locale()).TaskContextFilePlaceholder())
			msgs := locales.In(c.Locale())
			choose := secondaryButton(c, msgs.TaskContextChooseFile()).Label(msgs.TaskContextChooseFile()).Disabled(tc.fileChoosing || tc.fileChooserBlocked || tc.chooseFile == nil).OnClick(func() { tc.chooseFile() })
			if tc.focusFileChooser {
				choose.Focus().ScrollIntoView()
				tc.focusFileChooser = false
			}
			if tc.fileChoosing {
				ui.Text(c, msgs.TaskContextChoosingFile()).FontSize(typeAction).TextColor(t.TextMuted)
			}
			if tc.fileChoiceFailed {
				ui.Text(c, msgs.TaskContextChooseFileFailed()).FontSize(typeAction).LineHeight(1.5).TextColor(t.Danger)
			}
			ui.Text(c, msgs.TaskContextChooseFileHint()).FontSize(typeAction).LineHeight(1.5).TextColor(t.TextMuted)
			ui.Text(c, locales.In(c.Locale()).TaskContextPath()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			preferenceInput(c, &tc.formPath, false).Label(locales.In(c.Locale()).TaskContextFilePathLabel()).Placeholder(locales.In(c.Locale()).TaskContextFilePathPlaceholder()).OnChange(func() {
				tc.fileForm.pathRevision++
				tc.fileRequest++
				tc.fileChoosing, tc.focusFileChooser = false, false
			})
			ui.Text(c, locales.In(c.Locale()).TaskContextFileRules()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		default:
			ui.Text(c, locales.In(c.Locale()).TaskContextName()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			preferenceInput(c, &tc.formName, false).Label(locales.In(c.Locale()).TaskContextNameLabel()).Placeholder(locales.In(c.Locale()).TaskContextNamePlaceholder())
			ui.Text(c, locales.In(c.Locale()).TaskContextText()).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			formTextArea(c, &tc.formText, false).Label(locales.In(c.Locale()).TaskContextTextLabel()).Height(84).Placeholder(locales.In(c.Locale()).TaskContextTextPlaceholder())
			if tc.formMode == "edit" && tc.formPath != "" {
				ui.Text(c, locales.In(c.Locale()).TaskContextEditHint()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
		}
		if tc.formError != nil {
			ui.Text(c, taskContextErrorText(locales.In(c.Locale()), tc.formError)).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.Danger)
		}
		msgs := locales.In(c.Locale())
		label := msgs.TaskContextSaveSnippet()
		if tc.formMode == "file" {
			label = msgs.TaskContextSaveFile()
		} else if tc.formMode == "edit" {
			label = msgs.TaskContextSaveChanges()
		}
		secondaryButton(c, label).Label(label).OnClick(func() { tc.saveForm() })
	})
}

func (tc *taskContext) previewView(c *ui.Context, theme workbenchTheme, item taskContextItem) {
	t := theme.Theme
	ui.Column(c).Gap(4).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.In(c.Locale()).TaskContextPreviewTitle(item.Name)).FontSize(typeLabel).FontWeight(400).FixedLineHeight(lineLabel).TextColor(t.Text)
			ui.Spacer(c)
			secondaryButton(c, locales.In(c.Locale()).TaskContextClosePreview()).OnClick(func() { tc.previewID = "" })
		})
		if item.Source != "" {
			ui.Text(c, item.Source).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
		}
		ui.Scroll(c).Height(220).Padding(4, 0).Children(func() {
			markdownReader(c, item.Text, markdownReaderOptions{PlainText: true, CopyLabel: locales.In(c.Locale()).CopyContextTextLabel()}).
				FontSize(typeBody).FixedLineHeight(lineBody).TextColor(t.Text)
		})
	})
}

func taskContextErrorText(msgs locales.Messages, err error) string {
	detail := ""
	var detailErr *taskContextError
	if errors.As(err, &detailErr) {
		detail = detailErr.detail
	}
	switch {
	case errors.Is(err, errTaskContextName):
		return msgs.TaskContextErrName()
	case errors.Is(err, errTaskContextUTF8):
		if detail != "" {
			return msgs.TaskContextErrUTF8Detail(detail)
		}
		return msgs.TaskContextErrUTF8()
	case errors.Is(err, errTaskContextNotText):
		if detail != "" {
			return msgs.TaskContextErrNotTextDetail(detail)
		}
		return msgs.TaskContextErrNotText()
	case errors.Is(err, errTaskContextOversize):
		if detail != "" {
			return msgs.TaskContextErrOversizeDetail(detail)
		}
		return msgs.TaskContextErrOversize()
	case errors.Is(err, errTaskContextUnsupported):
		if detail != "" {
			return msgs.TaskContextErrUnsupportedDetail(detail)
		}
		return msgs.TaskContextErrUnsupported()
	case errors.Is(err, errTaskContextRead):
		if detail != "" {
			return msgs.TaskContextErrReadDetail(detail)
		}
		return msgs.TaskContextErrRead()
	case errors.Is(err, errTaskContextMissing):
		return msgs.TaskContextErrMissing()
	case errors.Is(err, errTaskContextNoSource):
		return msgs.TaskContextErrNoSource()
	default:
		return err.Error()
	}
}

func taskContextSourceLabel(msgs locales.Messages, item taskContextItem) string {
	if item.Source == "" {
		return msgs.TaskContextSourceNamed()
	}
	return msgs.TaskContextSourceFile(strconv.FormatUint(item.Revision, 10))
}

func taskContextSizeLabel(tc *taskContext) string {
	total := 0
	for _, item := range tc.items {
		total += taskContextWrapperBytes(item.inputName(), item.Text)
	}
	if total < 1024 {
		return fmt.Sprintf("%d B", total)
	}
	return fmt.Sprintf("%.1f KiB", float64(total)/1024)
}
