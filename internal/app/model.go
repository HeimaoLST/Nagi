package app

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

type contactStatus int

const (
	statusReady contactStatus = iota
	statusWorking
	statusAttention
)

type messageKind int

const (
	msgUser messageKind = iota
	msgContact
	msgActivity
)

type sendState int

const (
	sendIdle sendState = iota
	sendSubmitting
	sendAccepted
	sendUnknown
)

type historyEntry struct {
	ID      string
	Date    string
	From    string // "me" or "contact"
	Text    string
	Context []taskContextItem
}

type message struct {
	ID          int
	Kind        messageKind
	Text        string
	Time        string
	Timestamp   time.Time
	AlertTitle  string
	AlertDetail string
	Retry       bool
	RetryDone   bool
	Expanded    bool

	// Key identifies a Pi history entry or a live run item.
	Key        string
	TaskID     string
	MessageID  string
	ItemID     string
	CallID     string
	ToolName   string
	ToolArgs   string
	ToolOutput string
	History    bool
	Streaming  bool
	Context    []taskContextItem
	Quote      *pi.ProductQuote

	liveReveal      bool
	revealedBubbles int
	nextRevealAt    time.Time
}

type runtimeBuiltinTool struct {
	Name        string
	Description string
}

type contact struct {
	ID                 string
	Name               string
	Role               string
	Purpose            string
	Instructions       string
	StarterPrompts     []string
	Workspace          string
	BuiltinTools       *[]string
	SystemKind         string
	ProfileRevision    uint64
	PendingLearnings   int
	Status             contactStatus
	completedUntil     time.Time
	recentCompletion   bool
	Unread             int
	Work               string
	WorkArg            string
	WorkLabel          string
	Messages           []*message
	History            []historyEntry
	Draft              string
	DraftQuote         *pi.ProductQuote
	Archived           bool
	ArchivedAt         int64
	PiBinding          *piConversationBinding
	PreviousPiBindings []piConversationBinding
	Context            taskContext
	activity           productActivityPanel
	memory             botMemoryViewState
	replyIndex         int
	list               ui.ListState
	recall             quoteRecall
	Works              []*workItem
	Activities         []*productActivity
	SelectedActivityID string

	activeActivities       map[string]pi.ProductActivitySummary
	activityStatusRevision uint64

	// A Pi conversation is opened lazily on its first send.
	SessionID         string
	Loaded            bool
	LoadErr           string
	Unavailable       string
	ActiveTaskID      string
	LastTaskID        string
	SendState         sendState
	SendError         string
	SendErrorKind     string
	SendErrorDetail   string
	SubmittedDraft    string
	SubmittedQuote    *pi.ProductQuote
	SubmittedRev      int
	pendingSend       *pendingSend
	draftRev          int
	StatusDetail      string
	StatusDetailKind  string
	SessionWorkingDir string
	SessionModel      string
	SessionAPI        string
	SessionState      string
	historyGen        int
}

func (ct *contact) isBuiltin() bool {
	return ct != nil && ct.SystemKind != ""
}

type profileDraft struct {
	Name           string
	Purpose        string
	Instructions   string
	StarterPrompts []string
	Workspace      string
	BuiltinTools   *[]string
}

type appModel struct {
	contacts []*contact
	selected int
	aux      string

	detail         detailKind
	profileScrolls map[string]*ui.ScrollState

	defaultContactN        int
	focusComposer          bool
	composerCaretToEnd     bool
	priorChats             *productConversationBrowser
	composerHandle         ui.Handle
	composerAttachMenuOpen bool
	focusComposerAttach    bool
	focusComposerMaterial  string
	composerMaterial       *composerMaterialForm

	settingsOpen           bool
	settingsTab            string
	settingsReturnFocus    string
	settingsReturnLink     markdownLinkTarget
	markdownLinkFocus      markdownLinkTarget
	linkReturnTarget       markdownLinkTarget
	linkReturnSeen         bool
	settingsScroll         ui.ScrollState
	settingsFocusBack      bool
	settingsFocusTab       string
	contextFileGeneration  uint64
	contextFilePickers     int
	chooseContextFile      func(mygo.OpenDialogOptions) ([]string, error)
	folderChoiceGeneration uint64
	chooseFolder           func(mygo.OpenDialogOptions) ([]string, error)
	taskContextScroll      ui.ScrollState

	profileEditors         map[string]*contactProfileEditor
	contactProfileID       string
	contactSearch          string
	hideBuiltinArchitect   bool
	createBot              *createBotForm
	contactHistoryInFlight int
	submissionInFlight     int

	appearanceThemeIDs appearanceThemeIDs
	appearanceThemes   customThemeCatalog
	themeImport        *appearanceThemeImport
	dataRecovery       *dataRecoveryState
	activityIntents    map[string]productActivityIntent
	chooseRecoveryFile func(mygo.OpenDialogOptions) ([]string, error)
	revealRecoveryFile func(string) error
	chooseThemeFile    func(mygo.OpenDialogOptions) ([]string, error)
	appearanceMode     string
	languageMode       string
	languageModeLoaded bool
	uiLocale           string
	appearanceStatus   string
	appearanceError    string
	systemDark         bool
	storeInitAllowed   bool
	localSaveErr       string
	localSavePrevErr   string
	localSavePrevKind  string

	// Runtime mode. demo keeps the local fixture path for tests/screenshots;
	// the shipped app is runtime-backed.
	demo                      bool
	store                     *contactStore
	pi                        *piAppHost
	runtimeStatus             string
	runtimeErr                string
	runtimeErrKind            string
	runtimeStage              string
	runtimeHint               string
	runtimeHintKind           string
	runtimeHintArgs           []string
	runtimeBinary             string
	runtimeMaxActiveTasks     int
	runtimeCapabilitiesLoaded bool
	runtimeBuiltinTools       []runtimeBuiltinTool
	runtimeBuiltinDefaults    []string
	runtimeBuiltinToolsLoaded bool
	runtimeBuiltinToolsErr    string
	workDir                   string
	storePath                 string
	uiHookMu                  sync.Mutex
	uiHook                    func(func()) // test seam for deterministic UI-thread ordering
	requestQuit               func()       // set by main to mygo.App.Quit
	quitReady                 bool
	shutdownStarted           bool

	connectionPath         string
	connectionForm         *connectionSettings
	connectionSaved        *connectionConfig
	connectionEffective    *connectionConfig
	connectionManaged      bool
	connectionGeneration   int
	connectionApplying     bool
	connectionDetailsOpen  map[string]bool
	connectionSaveInFlight bool
	connectionTesting      bool
	connectionTestCancel   func()
	connectionProbe        *piConnectionProbe
	startupDetailsOpen     bool
	shutdownDrainStarted   bool
	providers              providerSettings

	openPath func(string) error

	activityToolLedger *activityToolOperationLedger
	activityToolScopes map[string]activityToolScope

	// Layout is a GUI-wide preference, not a contact or runtime fact. Widths
	// are persisted in the contact store; windowWidth is only the current
	// native viewport and never overwrites a preferred width when it shrinks.
	windowWidth          int
	leftPaneWidth        float32
	rightPaneWidth       float32
	centerPaneWidth      float32
	workspaceOpen        bool
	railCollapsed        bool
	focusDetailClose     bool
	focusRail            bool
	focusSettings        bool
	focusProfileRow      string
	focusAuxBack         bool
	detailPageMode       bool
	focusConnectionEntry bool
	focusConfiguration   bool
	workTheme            workbenchTheme

	seq int
	win *mygo.Window
}

var cannedReplies = map[string][]string{
	"nagi": {
		"Noted — I kept the earlier MyGo scope in view while answering, and nothing earlier was rewritten.",
		"That fits the current scope. I will treat it as the next check-in point rather than a new decision.",
	},
	"mio": {
		"Got it. I will fold that into the draft and leave the earlier version untouched.",
		"Understood — I will adjust that section and keep the rest of the note as it is.",
	},
	"ren": {
		"Done. Focused tests pass: 64 passed, 0 failed. I will wait for the next change before touching the parser again.",
		"Understood. I will keep the last passing state until you ask for another run.",
	},
}

func (a *appModel) selectedContact() *contact { return a.contacts[a.selected] }

func (a *appModel) contactByID(id string) *contact {
	for _, c := range a.contacts {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (a *appModel) selectContact(i int) {
	if i < 0 || i >= len(a.contacts) || i == a.selected {
		return
	}
	a.contextFileGeneration++
	a.contacts[i].Unread = 0
	a.selected = i
	a.priorChats = nil
	if !a.demo {
		a.persist()
	}
	a.aux = ""
	a.resetComposerOverlays()
	ct := a.contacts[i]
	ct.recentCompletion = false
	if !ct.Loaded && ct.ActiveTaskID == "" && a.canReloadPiConversation(ct) {
		a.reloadContactByID(ct.ID)
	}
}

func (a *appModel) snippet(c *contact, msgs locales.Messages) string {
	if c.Status == statusWorking && c.Work != "" {
		return workNoticeText(msgs, c.Work, c.WorkArg)
	}
	if !a.demo && c.SessionID != "" && !c.Loaded {
		return msgs.HistoryNotLoadedCompact()
	}
	for i := len(c.Messages) - 1; i >= 0; i-- {
		m := c.Messages[i]
		if m.Kind == msgActivity {
			continue
		}
		source := m.Text
		if m.Kind == msgContact {
			bubbles := splitReplyBubbles(m.Text, m.Streaming)
			if len(bubbles) == 0 {
				continue
			}
			source = bubbles[0]
			source = strings.ReplaceAll(source, replyBubbleSeparator, "")
		}
		text := strings.Join(strings.Fields(source), " ")
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > 36 {
			text = strings.TrimSpace(string(runes[:35])) + "…"
		}
		if m.Kind == msgUser {
			return msgs.SnippetUser(text)
		}
		return text
	}
	return msgs.EmptyNoMessages()
}

// addDefaultContact creates and selects a ready contact with a usable
// default name, then asks the composer to take focus once.
func (a *appModel) addDefaultContact() {
	if !a.demo {
		a.addRuntimeContact()
		return
	}
	a.seq++
	a.defaultContactN++
	c := &contact{
		ID:        fmt.Sprintf("contact-%d", a.seq),
		Name:      locales.In(a.uiLocale).NewContactName(a.defaultContactN),
		Role:      "Assistant",
		Status:    statusReady,
		WorkLabel: "Preparing a reply",
	}
	a.contacts = append(a.contacts, c)
	a.selected = len(a.contacts) - 1
	a.contactSearch = ""
	a.aux = ""
	a.resetComposerOverlays()
	a.focusComposer = true
}

func (a *appModel) openAdvancedSettings() {
	a.startupDetailsOpen = true
	a.openSettingsTab(settingsTabAdvanced, settingsSourceConfiguration)
}

func (a *appModel) closeAux() {
	if a.settingsOpen {
		a.closeSettings()
		return
	}
	if a.aux == "task-context" {
		a.contextFileGeneration++
	}
	a.aux = ""
}

func (a *appModel) send(ct *contact, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if !a.demo {
		if ct == nil || a.contactByID(ct.ID) != ct {
			return false
		}
		if reason := a.canSend(ct); reason != "" {
			ct.setSendError(sendBlockRaw(reason), reason, "")
			return false
		}
		if err := validateTaskContext(ct.Context.Items); err != nil {
			ct.setSendError(err.Error(), "", "")
			return false
		}
		return a.sendProductConversation(a.productHost(), ct)
	}
	if ct.Status == statusWorking {
		return false
	}
	a.seq++
	stamp := time.Now()
	ct.Messages = append(ct.Messages, &message{ID: a.seq, Kind: msgUser, Text: text, Time: stamp.Format("15:04"), Timestamp: stamp, Quote: ct.DraftQuote})
	ct.DraftQuote = nil
	label := ct.WorkLabel
	if label == "" {
		label = "Preparing a reply"
	}
	a.startWork(ct, label, 1500*time.Millisecond, func() {
		ct.Status = statusReady
		a.seq++
		stamp := time.Now()
		ct.Messages = append(ct.Messages, &message{ID: a.seq, Kind: msgContact, Text: a.canned(ct), Time: stamp.Format("15:04"), Timestamp: stamp, liveReveal: true})
		a.recordBotCompletion(ct)
	})
	return true
}

func (ct *contact) setSendError(raw, kind, detail string) {
	ct.SendError = raw
	ct.SendErrorKind = kind
	ct.SendErrorDetail = detail
}

func (ct *contact) clearSendError() { ct.setSendError("", "", "") }

func (ct *contact) setStatusDetail(raw, kind string) {
	ct.StatusDetail = raw
	ct.StatusDetailKind = kind
}

func (w *activityWorkSession) setSendError(raw, kind, detail string) {
	w.SendError = raw
	w.SendErrorKind = kind
	w.SendErrorDetail = detail
}

func (w *activityWorkSession) clearSendError() { w.setSendError("", "", "") }

func (a *appModel) sendCurrent() bool {
	ct := a.selectedContact()
	text := ct.Draft
	if !a.send(ct, text) {
		return false
	}
	if a.demo {
		ct.Draft = ""
	}
	return true
}

func (a *appModel) canned(ct *contact) string {
	replies := cannedReplies[ct.ID]
	if len(replies) == 0 {
		return "Noted. I will keep this thread in view and follow up here."
	}
	reply := replies[ct.replyIndex%len(replies)]
	ct.replyIndex++
	return reply
}

func (a *appModel) startWork(ct *contact, label string, d time.Duration, finish func()) {
	ct.Status = statusWorking
	ct.Work = label
	if a.win == nil {
		ct.Work = ""
		finish()
		if a.contacts[a.selected] != ct {
			ct.Unread++
		}
		return
	}
	win := a.win
	time.AfterFunc(d, func() {
		win.Update(func() {
			ct.Work = ""
			finish()
			if a.contacts[a.selected] != ct {
				ct.Unread++
			}
		})
	})
}

func (a *appModel) failRun(ct *contact) {
	ct.Status = statusAttention
	a.seq++
	stamp := time.Now()
	ct.Messages = append(ct.Messages, &message{
		ID: a.seq, Kind: msgContact, Time: stamp.Format("15:04"), Timestamp: stamp,
		Text:        "Two failures in parser_roundtrip: expected eof, got newline. The trailing-newline change is the likely cause.",
		AlertTitle:  "Run failed: 2 failing tests",
		AlertDetail: "cargo test — parser_roundtrip",
		Retry:       true,
	})
}

func (a *appModel) retry(m *message) bool {
	ct := a.selectedContact()
	if !m.Retry || m.RetryDone || ct.Status == statusWorking {
		return false
	}
	m.RetryDone = true
	a.startWork(ct, "Re-running the parser tests", 2*time.Second, func() {
		ct.Status = statusReady
		a.seq++
		stamp := time.Now()
		ct.Messages = append(ct.Messages, &message{ID: a.seq, Kind: msgContact, Text: "Fixed the trailing-newline branch. Parser tests pass now: 64 passed, 0 failed.", Time: stamp.Format("15:04"), Timestamp: stamp})
		a.recordBotCompletion(ct)
	})
	return true
}
