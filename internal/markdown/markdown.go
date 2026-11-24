package markdown

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"

	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

type LinkTarget struct {
	Scope  string
	Block  int
	Inline int
	Dest   string
}

// ReaderOptions configures Reader. The source is the only content it reads:
// no app, contact or runtime state is involved.
type ReaderOptions struct {
	// OpenLocalFile receives only explicit path-link activations; the caller owns the source directory and errors.
	OpenLocalFile func(destination string) error
	// PlainText shows source literally and never parses it as Markdown.
	PlainText bool
	// Streaming shows source as a reply still being written.
	Streaming bool
	// CopyLabel is the accessible name and idle tooltip of the icon-only
	// full-source copy control, as "Copy message" or "Copy received".
	CopyLabel string
	// CopyText writes text and reports whether it stuck. When nil,
	// Reader writes the native clipboard on the UI thread and reads it back.
	CopyText func(text string) error
	// OnCopyResult receives every full-source and code-copy result.
	OnCopyResult func(err error)
	// CopyHandle binds the full-source copy control, not code-block copies.
	CopyHandle *ui.Handle
	// CodeCopyHandles binds code-block copy controls by block index.
	CodeCopyHandles map[int]*ui.Handle
	// Theme overrides the Markdown foreground roles, as the workspace
	// surface requires.
	Theme *markdown.Theme
	// MessageBubble places the source copy in an external gutter and paints
	// the reader on a bubble surface. Nil keeps the compact reader layout.
	MessageBubble *MessageBubble
	// LinkScope is stable identity for this Markdown document; LinkFocus is
	// the app-owned FocusBind field for its inline links. Both are optional.
	LinkScope string
	LinkFocus *LinkTarget
	// LinkReturn and LinkReturnSeen report whether this document contains the
	// exact link a Settings return is waiting for; the caller decides once,
	// after the workbench build, whether to consume or cancel that request.
	LinkReturn     *LinkTarget
	LinkReturnSeen *bool
}

var ErrClipboardReadback = errors.New("clipboard readback did not match")

const (
	readerCopiedFor     = 2 * time.Second
	readerCopyFailedFor = 4 * time.Second
)

type MessageBubble struct {
	Background     ui.Color
	PaddingX       float32
	PaddingY       float32
	Radius         float32
	ContextualCopy bool
	MenuOnlyCopy   bool
	BeforeCopyMenu func(*ui.Menu)
	Highlight      float32
	Header         func(*ui.Context)
	Footer         func(*ui.Context)
	BodyHandle     *ui.Handle
}

type readerState struct {
	opts markdown.Options

	copyText        func(string) error
	onCopyResult    func(error)
	streaming       bool
	codeCopyHandles map[int]*ui.Handle

	sourceFeedback readerCopyFeedback
	codeFeedback   map[int]*readerCopyFeedback

	linkError  error
	linkSource string
	linkScope  string
	ready      bool
}

type readerCopyFeedback struct {
	at  time.Time
	err error
}

func (f *readerCopyFeedback) show(now time.Time) (visible, failed bool) {
	if f.at.IsZero() {
		return false, false
	}
	d := readerCopiedFor
	if f.err != nil {
		d = readerCopyFailedFor
	}
	if now.Sub(f.at) >= d {
		return false, false
	}
	return true, f.err != nil
}

// Reader shows source as finished or streaming Markdown with one copy control
// for the exact source and one per code block. It reuses the pinned Markdown
// view, so selection, links, parsing and streaming keep their established behavior.
func Reader(c *ui.Context, source string, options ReaderOptions) ui.Element {
	var root ui.Element
	if options.MessageBubble == nil {
		root = ui.Column(c).AlignSelf(ui.Stretch)
	} else {
		root = ui.Row(c).AlignItems(ui.Start).Gap(4).Shrink(1)
	}
	root.Font("system-ui").FontSize(theme.TypeBody).FixedLineHeight(theme.LineBody)
	st := ui.Local(root, readerStateKey{}, func() readerState {
		return readerState{codeFeedback: map[int]*readerCopyFeedback{}}
	})
	st.copyText = options.CopyText
	st.onCopyResult = options.OnCopyResult
	st.streaming = options.Streaming
	st.codeCopyHandles = options.CodeCopyHandles
	if !st.ready {
		st.ready = true
		st.opts.Extensions = parser.Default &^ (parser.ExtMentions | parser.ExtReferences)
		st.opts.WrapCode = true
		st.opts.Reveal = &markdown.Reveal{Unit: markdown.AtOnce, Fade: -1}
		st.opts.CodeBlock = func(c *ui.Context, b *markdown.CodeBlockView) {
			readerCodeBlock(c, b, st)
		}
		st.opts.Block = func(c *ui.Context, d *parser.Document, i int) bool {
			if d.Block(i).Kind != parser.Table {
				return false
			}
			readerTable(c, d, i, readerTheme(c, st.opts.Theme))
			return true
		}
	}
	if st.linkSource != source || st.linkScope != options.LinkScope {
		st.linkError = nil
		st.opts.Anchor = ""
		st.linkSource, st.linkScope = source, options.LinkScope
	}
	st.opts.OnLink = nil
	if options.OpenLocalFile != nil {
		st.opts.OnLink = func(dest string) {
			st.linkError = nil
			switch {
			case strings.HasPrefix(dest, "#"):
				st.opts.Anchor = strings.TrimPrefix(dest, "#")
				st.opts.AnchorVersion++
			case ui.SafeToOpen(dest):
				c.OpenURL(dest)
			case !strings.ContainsAny(dest, "\\\x00") && !strings.HasPrefix(dest, "//"):
				if i := strings.IndexAny(dest, ":/?#"); i < 0 || dest[i] != ':' {
					st.linkError = options.OpenLocalFile(dest)
				}
			}
		}
	}
	st.opts.Theme = normalizedMarkdownTheme(c, options.Theme)
	menuOnlyCopy := options.MessageBubble != nil && options.MessageBubble.MenuOnlyCopy
	st.opts.ShareSelection = menuOnlyCopy
	st.opts.LinkElement = nil
	if !options.PlainText {
		scope, focus, returnTarget, returnSeen := options.LinkScope, options.LinkFocus, options.LinkReturn, options.LinkReturnSeen
		st.opts.LinkElement = func(c *ui.Context, block, inline int, dest string, element ui.Element) {
			element.Underline()
			if scope == "" || focus == nil {
				return
			}
			value := LinkTarget{Scope: scope, Block: block, Inline: inline, Dest: dest}
			element.FocusBind(focus, value)
			if returnTarget != nil && *returnTarget == value {
				element.Focus()
				if returnSeen != nil {
					*returnSeen = true
				}
			}
		}
	}

	label := options.CopyLabel
	if label == "" {
		label = locales.In(c.Locale()).CopyMessageLabel()
	}
	copyMenu := func(m *ui.Menu) {
		if bubble := options.MessageBubble; bubble != nil && bubble.BeforeCopyMenu != nil {
			bubble.BeforeCopyMenu(m)
		}
		if m.Item(label).Chosen() {
			st.copySource(c, c.Now(), source, &st.sourceFeedback)
		}
		if options.PlainText {
			m.Separator()
			m.EditItems()
		}
	}

	drawBody := func(c *ui.Context) {
		switch {
		case options.PlainText:
			text := ui.Text(c, source).Selectable().Font("system-ui").FontSize(theme.TypeBody).FixedLineHeight(theme.LineBody)
			if menuOnlyCopy {
				text.PopoverMenu(copyMenu)
			} else if options.MessageBubble != nil && options.MessageBubble.ContextualCopy {
				text.ContextMenu(copyMenu)
			}
		case options.Streaming:
			markdown.Stream(c, source, &st.opts)
		default:
			markdown.View(c, source, &st.opts)
		}
		if st.linkError != nil {
			ui.Text(c, st.linkError.Error()).FontSize(theme.TypeAction).LineHeight(1.4).TextColor(c.Theme().Danger)
		}
	}
	if options.MessageBubble == nil {
		root.Children(func() {
			st.sourceCopyButton(c, c.Now(), label, func() string { return source }, &st.sourceFeedback, options.CopyHandle, false, nil)
			ui.Column(c).Padding(0, 32, 0, 0).Children(func() { drawBody(c) })
		})
		return root
	}
	bubble := options.MessageBubble
	root.Children(func() {
		body := ui.Column(c).Grow(1).MinWidth(0).Padding(bubble.PaddingY, bubble.PaddingX).
			Radius(bubble.Radius).Background(bubble.Background).Gap(4)
		if bubble.BodyHandle != nil {
			body.Bind(bubble.BodyHandle)
		}
		if bubble.Highlight > 0 {
			color := c.Theme().Accent.Alpha(bubble.Highlight)
			body.DrawOver(func(p *ui.Painter, r ui.Rect) {
				p.Stroke(r, color, bubble.Radius, 2)
			})
		}
		if menuOnlyCopy {
			if !options.PlainText {
				body.Selectable().CopyData(copyBubbleSelection)
			}
			body.PopoverMenu(copyMenu)
		}
		body.Children(func() {
			if bubble.Header != nil {
				bubble.Header(c)
			}
			drawBody(c)
			if bubble.Footer != nil {
				bubble.Footer(c)
			}
		})
		if menuOnlyCopy {
			return
		}
		reveal := func() bool {
			if !bubble.ContextualCopy {
				return true
			}
			visible, _ := st.sourceFeedback.show(c.Now())
			return root.Hovered() || root.FocusWithin() || visible
		}
		st.sourceCopyButton(c, c.Now(), label, func() string { return source }, &st.sourceFeedback, options.CopyHandle, true, reveal)
	})
	return root
}

func copyBubbleSelection(parts []ui.SelectedText) transfer.Data {
	var text strings.Builder
	for i, part := range parts {
		if i > 0 {
			text.WriteString("\n\n")
		}
		start, end := len(part.Text), len(part.Text)
		position := 0
		for offset := range part.Text {
			if position == part.Start {
				start = offset
			}
			if position == part.End {
				end = offset
				break
			}
			position++
		}
		text.WriteString(part.Text[start:end])
	}
	return transfer.TextData(text.String())
}

type readerStateKey struct{}

// readerCodeBlock renders a fenced code block; b.Code copies the block content without fences (LF-normalized by CommonMark), while full-source copy retains original source bytes.
func readerCodeBlock(c *ui.Context, b *markdown.CodeBlockView, st *readerState) {
	th := readerTheme(c, st.opts.Theme)
	em := func(v float32) float32 { return v * th.FontSize }

	box := ui.Column(c).
		Direction(ui.LeftToRight).
		Background(th.CodeBlockBackground).
		Border(1, th.CodeBlockBorder).
		Radius(th.CodeBlockRadius).
		AlignSelf(ui.Stretch).
		Clip().
		Key("code-" + strconv.Itoa(b.Index))
	box.Role(ui.RoleGroup).Label(locales.In(c.Locale()).MarkdownCodeGroup())
	box.Children(func() {
		head := ui.Row(c).AlignItems(ui.Center).Padding(em(0.35), em(0.4), 0, em(th.CodeBlockPadding)).Key("code-head")
		head.Children(func() {
			if b.Language != "" {
				ui.Text(c, b.Language).FontSize(theme.TypeMeta).TextColor(th.Muted).Unselectable()
			}
			ui.Spacer(c)
			accessible := locales.In(c.Locale()).CopyCodeLabel()
			if st.streaming && b.Open {
				accessible = locales.In(c.Locale()).CopyReceivedCodeLabel()
			}
			var handle *ui.Handle
			if st.codeCopyHandles != nil {
				handle = st.codeCopyHandles[b.Index]
				if handle == nil {
					handle = &ui.Handle{}
					st.codeCopyHandles[b.Index] = handle
				}
			}
			st.copyButton(c, c.Now(), accessible, locales.In(c.Locale()).CopyActionLabel(), b.Code, st.codeFeedbackFor(b.Index), th, handle)
		})
		b.Body(c)
	})
}

// readerTable wraps a parsed table into the available width instead of
// the pinned renderer's horizontal scroll. Rows, headers, alignment and every
// cell stay, with raw cell source so Markdown syntax and link destinations are
// still inspectable.
func readerTable(c *ui.Context, d *parser.Document, i int, th markdown.Theme) {
	b := d.Block(i)
	padX, padY := th.TableCellPadding[0]*th.FontSize, th.TableCellPadding[1]*th.FontSize
	wrap := ui.Column(c).AlignSelf(ui.Stretch)
	wrap.Children(func() {
		table := ui.Column(c).AlignSelf(ui.Stretch).Border(1, th.TableBorder).Radius(th.FontSize * 0.3).Clip().Role(ui.RoleTable).Label(locales.In(c.Locale()).MarkdownTableGroup())
		table.Children(func() {
			n := 0
			for r := range d.Children(i) {
				row := d.Block(r)
				rowEl := ui.Row(c).AlignSelf(ui.Stretch).AlignItems(ui.Stretch).Role(ui.RoleRow)
				if row.Header {
					rowEl.Background(th.TableHeader)
				} else if n%2 == 1 && th.TableStripe.A > 0 {
					rowEl.Background(th.TableStripe)
				}
				if n > 0 {
					rowEl.BorderWidth(0.5, 0, 0, 0).BorderColor(th.TableBorder)
				}
				header := row.Header
				rowEl.Children(func() {
					k := 0
					cellAt := func(cell, index int) {
						role := ui.RoleCell
						weight := 0
						if header {
							role = ui.RoleColumnHeader
							weight = th.StrongWeight
						}
						box := ui.Column(c).Grow(1).Basis(0).Shrink(1).MinWidth(0).Padding(padY, padX).Role(role)
						if index > 0 {
							box.BorderWidth(0, 0, 0, 0.5).BorderColor(th.TableBorder)
						}
						cellBlock := d.Block(cell)
						text := d.Source()[cellBlock.Start:cellBlock.End]
						align := ui.Start
						switch b.Align[index] {
						case parser.AlignCenter:
							align = ui.Center
						case parser.AlignRight:
							align = ui.End
						}
						box.Children(func() {
							ui.Text(c, text).FontSize(th.FontSize).LineHeight(th.LineHeight).TextColor(th.Text).FontWeight(weight).TextAlign(align)
						})
					}
					for cell := range d.Children(r) {
						cellAt(cell, k)
						k++
					}
				})
				n++
			}
		})
	})
}

func (st *readerState) codeFeedbackFor(index int) *readerCopyFeedback {
	fb := st.codeFeedback[index]
	if fb == nil {
		fb = &readerCopyFeedback{}
		st.codeFeedback[index] = fb
	}
	return fb
}

func (st *readerState) sourceCopyButton(c *ui.Context, now time.Time, accessible string, text func() string, fb *readerCopyFeedback, handle *ui.Handle, outside bool, reveal func() bool) ui.Element {
	feedbackVisible, failed := fb.show(now)
	btn := ui.Button(c, "").Label(accessible)
	if handle != nil {
		btn.Bind(handle)
	}
	t := c.Theme()
	btn.Key("source-copy")
	btn = theme.IconControlFace(c, btn, theme.IconResponse, false)
	if !outside {
		btn.Absolute().Top(0).Right(0)
	}
	color := readerTheme(c, st.opts.Theme).Muted
	icon := theme.MarkdownCopyIcon
	tooltip := accessible
	if feedbackVisible {
		if failed {
			color, icon = t.Danger, theme.MarkdownCopyFailedIcon
			tooltip = locales.In(c.Locale()).CopyFailed(fb.err.Error())
		} else {
			color, icon = t.Success, theme.MarkdownCopiedIcon
			tooltip = locales.In(c.Locale()).Copied()
		}
	}
	theme.AppTooltip(c, btn, tooltip)
	showIcon := reveal == nil || reveal() || btn.Hovered() || btn.FocusVisible()
	if showIcon {
		btn.Children(func() {
			theme.ControlIcon(c, icon, theme.IconResponse).TextColor(color)
		})
	}
	if btn.Clicked() {
		st.copySource(c, now, text(), fb)
	}
	return btn
}

func (st *readerState) copySource(c *ui.Context, now time.Time, text string, fb *readerCopyFeedback) {
	fb.at = now
	fb.err = st.performCopy(c, text)
	if st.onCopyResult != nil {
		st.onCopyResult(fb.err)
	}
	if fb.err != nil {
		c.After(readerCopyFailedFor)
	} else {
		c.After(readerCopiedFor)
	}
}

func (st *readerState) copyButton(c *ui.Context, now time.Time, accessible, idle string, text func() string, fb *readerCopyFeedback, th markdown.Theme, handle *ui.Handle) ui.Element {
	row := ui.Row(c).Shrink(0).Key("copy-button")
	row.Children(func() {
		color, icon := th.Muted, theme.MarkdownCopyIcon
		if visibleNow, failed := fb.show(now); visibleNow {
			if failed {
				color, icon = c.Theme().Danger, theme.MarkdownCopyFailedIcon
			} else {
				color, icon = c.Theme().Success, theme.MarkdownCopiedIcon
			}
		}
		btn := ui.ButtonBase(c).Label(accessible)
		if handle != nil {
			btn.Bind(handle)
		}
		t := c.Theme()
		btn.Size(32, 32).Padding(0).Radius(6).BorderWidth(0).Background(ui.Transparent)
		if btn.Pressed() {
			btn.Background(t.SurfacePressed)
		} else if btn.Hovered() {
			btn.Background(t.SurfaceHover)
		}
		tooltip := idle
		if fb.err != nil {
			if visibleNow, failed := fb.show(now); visibleNow && failed {
				tooltip = locales.In(c.Locale()).CopyFailed(fb.err.Error())
			}
		}
		theme.AppTooltip(c, btn, tooltip)
		btn.Children(func() {
			ui.Icon(c, icon).Size(16, 16).TextColor(color)
		})
		if btn.Clicked() {
			st.copySource(c, now, text(), fb)
		}
	})
	return row
}

func (st *readerState) performCopy(c *ui.Context, text string) error {
	if st.copyText != nil {
		return st.copyText(text)
	}
	return WriteClipboardText(text, c.Services().WriteClipboard, c.Services().ReadClipboard)
}

func WriteClipboardText(text string, write func(string), read func() string) error {
	write(text)
	if read() != text {
		return ErrClipboardReadback
	}
	return nil
}

func normalizedMarkdownTheme(c *ui.Context, override *markdown.Theme) *markdown.Theme {
	th := markdown.Theme{}
	if override != nil {
		th = *override
	}
	if th.Link == (markdown.Color{}) {
		th.Link = ui.Hex("#2D6A4A")
		if c.Theme().Dark {
			th.Link = ui.Hex("#E0C65C")
		}
	}
	th.FontSize = theme.TypeBody
	th.Font = "system-ui"
	th.LineHeight = theme.LineBody / theme.TypeBody
	for i, role := range theme.MarkdownHeadingRoles() {
		h := &th.Headings[i]
		h.Size, h.Weight = role.Size/th.FontSize, role.Weight
		h.FixedLineHeight, h.LetterSpacing = role.LineHeight, role.LetterSpacing
		h.Top, h.Bottom, h.Uppercase = role.Top/th.FontSize, role.Bottom/th.FontSize, role.Uppercase
		if h.Color == (markdown.Color{}) {
			h.Color = role.Tone.Color(c.Theme())
			if role.Tone == theme.HeadingPrimary && th.Text != (markdown.Color{}) {
				h.Color = th.Text
			} else if role.Tone != theme.HeadingPrimary && th.Muted != (markdown.Color{}) {
				h.Color = th.Muted
			}
		}
	}
	if th.CodeSize == 0 {
		th.CodeSize = theme.TypeAction / th.FontSize
	}
	if th.CodeLineHeight == 0 {
		th.CodeLineHeight = 1.5
	}
	return &th
}

// readerTheme resolves only the roles this reader draws itself:
// the code frame and its header. The Markdown view resolves its own full
// theme from the same override.
func readerTheme(c *ui.Context, override *markdown.Theme) markdown.Theme {
	th := *markdown.DefaultTheme(c.Theme())
	if override == nil {
		return th
	}
	if override.FontSize != 0 {
		th.FontSize = override.FontSize
	}
	if override.LineHeight != 0 {
		th.LineHeight = override.LineHeight
	}
	if override.Text != (markdown.Color{}) {
		th.Text = override.Text
	}
	if override.Muted != (markdown.Color{}) {
		th.Muted = override.Muted
	}
	if override.CodeBlockBackground != (markdown.Color{}) {
		th.CodeBlockBackground = override.CodeBlockBackground
	}
	if override.CodeBlockBorder != (markdown.Color{}) {
		th.CodeBlockBorder = override.CodeBlockBorder
	}
	if override.CodeBlockRadius != 0 {
		th.CodeBlockRadius = override.CodeBlockRadius
	}
	if override.CodeBlockPadding != 0 {
		th.CodeBlockPadding = override.CodeBlockPadding
	}
	return th
}
