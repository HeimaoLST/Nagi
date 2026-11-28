package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

const (
	sessionPageSize    = 20
	sessionReadSize    = 16 * 1024
	sessionOutputLimit = 4 * sessionReadSize
	sessionReadTimeout = 5 * time.Second
)

type sessionInspectionCopy map[string]string

var defaultSessionInspectionCopy = sessionInspectionCopy{
	"title": "Terminal sessions", "back": "Back", "reload": "Reload", "loadMore": "Load more", "previousPage": "Previous page",
	"loadingList": "Loading sessions…", "emptyList": "This Bot has no sessions yet.", "listError": "Couldn’t load sessions. Reload to try again.",
	"loadingDetail": "Loading session details…", "detailError": "Couldn’t load session details. Reload to try again.",
	"session": "Session", "starting": "Starting", "running": "Running", "exited": "Exited", "closed": "Closed", "disconnected": "Disconnected", "failed": "Failed",
	"readOnly": "Read-only session history", "sourceChat": "Source chat", "sourceActivity": "Source activity",
	"command": "Command", "directory": "Working directory", "mode": "Mode", "pty": "PTY", "pipe": "Pipe", "size": "Terminal size", "dimensions": "%d × %d",
	"exitCode": "Exit code", "exitSignal": "Exit signal", "transport": "Transport", "live": "Connected", "eof": "Output ended",
	"transportEOF": "Stream closed", "supervisor_lost": "Supervisor disconnected", "worker_restarted": "Worker restarted", "closed_by_user": "Closed by you",
	"closed_by_bot": "Closed by the Bot", "closed_on_shutdown": "Closed on shutdown", "exec_failed": "Couldn’t start the process",
	"output": "Output", "stdout": "stdout", "stderr": "stderr", "reading": "Reading output…", "emptyOutput": "No output yet.",
	"outputError": "Couldn’t read output. Reload to try again.", "outputGap": "%s bytes of earlier output are unavailable.",
	"hostDisconnected": "The session host is disconnected. Available history is read-only.",
	"earlierOutput":    "Earlier output", "laterOutput": "Later output", "latestOutput": "Latest output", "newOutput": "New output is available.",
	"authPaused": "Waiting for external authentication", "auth_prompt": "The process asked for authentication.",
	"canonical_no_echo": "The process is waiting for hidden terminal input.", "authAction": "Complete authentication outside Xuhu or kill this session.",
	"watches": "Watches", "noWatches": "No watches for this run.", "armed": "Armed", "fired": "Fired", "cancelled": "Cancelled", "target_lost": "Target lost",
	"conditions": "Conditions", "exit": "Process exit", "match": "Text match", "idle": "Idle", "timeout": "Timeout",
	"matchLiteral": "Match text", "idleTime": "Idle threshold", "timeoutTime": "Timeout", "milliseconds": "%s ms", "firedKind": "Triggered by", "matchedExcerpt": "Matched output",
	"kill": "Kill session", "killBlocked": "Session actions are temporarily unavailable.", "killPending": "Waiting for the host to confirm the close…",
	"killUnknown": "Couldn’t confirm the close. Check its status before taking another action.", "checkClose": "Check close status",
	"killAccepted": "Close accepted.", "killTerminated": "Process terminated and reaped.", "killSettled": "The session had already ended.",
	"killUnreaped": "Process reaping is unconfirmed.", "killReaped": "Process reaping confirmed.",
	"previousClose":       "Close request for an earlier run",
	"killStaleGeneration": "The session run changed before it could be closed.", "killStaleRevision": "The session changed before it could be closed.",
	"killRejected": "The host rejected the close.", "killNotSent": "The close request couldn’t be sent.", "killSaveError": "Couldn’t save the close request. Try again.",
}

func (copy sessionInspectionCopy) text(key string) string {
	if value := copy[key]; value != "" {
		return value
	}
	return defaultSessionInspectionCopy[key]
}

type sessionInspectorDeps struct {
	client                        *pi.ProductClient
	botID                         string
	wb                            theme.WorkbenchTheme
	blocked                       bool
	copy                          sessionInspectionCopy
	formatDimensions              func(cols, rows int) string
	formatGap                     func(bytes string) string
	formatMS                      func(ms string) string
	updateUI                      func(func())
	persistCloseIntents           func([]pi.ProductSessionCloseParams) error
	onBack                        func()
	onOpenConversation            func(conversationID string)
	onOpenActivity                func(activityID string)
	showContacts                  func(*ui.Context, *ui.Theme)
	headerLeftPad, headerRightPad float32
}

type sessionWorkspaceState struct {
	bots                map[string]*botSessionState
	active              map[string]map[string]pi.ProductSessionSummary
	activeBotID         string
	client              *pi.ProductClient
	readEpoch           uint64
	blocked             bool
	updateUI            func(func())
	persistCloseIntents func([]pi.ProductSessionCloseParams) error
}

type botSessionState struct {
	botID           string
	items           []pi.ProductSessionSummary
	cursors         []string
	page            int
	nextCursor      string
	listLoading     bool
	listFailed      bool
	listScroll      ui.ScrollState
	detail          *sessionDetailState
	closes          map[sessionRun]*sessionCloseIntent
	focusBack       bool
	focusRow        string
	opener          string
	activeChanges   map[string]pi.ProductSessionSummary
	focusSourceChat bool
}

type sessionRun struct {
	id         string
	generation uint64
}

type sessionDetailState struct {
	summary pi.ProductSessionSummary
	loading bool
	ready   bool
	failed  bool
	scroll  ui.ScrollState
	output  sessionOutputState
}

type sessionOutputState struct {
	chunks                  []pi.ProductSessionChunk
	from, next, highWater   uint64
	firstAvailable, evicted uint64
	gapBytes                uint64
	loaded, loading, failed bool
	eof, browsing           bool
	revision                uint64
	authPause               *pi.ProductSessionAuthPause
	scroll                  ui.ScrollState
	text                    string
	stdout, stderr          string
	textDirty               bool
}

type sessionCloseIntent struct {
	params   pi.ProductSessionCloseParams
	status   string
	checking bool
	result   *pi.ProductSessionCloseResult
}

func (intent *sessionCloseIntent) unresolved() bool {
	return intent.status == "killPending" || intent.status == "killUnknown"
}

func (s *sessionWorkspaceState) botSession(botID string) *botSessionState {
	if s.bots == nil {
		s.bots = make(map[string]*botSessionState)
	}
	bot := s.bots[botID]
	if bot == nil {
		bot = &botSessionState{botID: botID, cursors: []string{""}, closes: make(map[sessionRun]*sessionCloseIntent)}
		s.bots[botID] = bot
	}
	return bot
}

func listBotActiveSessions(ctx context.Context, client *pi.ProductClient, botID string) (map[string]pi.ProductSessionSummary, error) {
	params := pi.ProductSessionListParams{BotID: botID, Limit: sessionPageSize,
		States: []pi.ProductSessionState{pi.ProductSessionStarting, pi.ProductSessionRunning}}
	active := make(map[string]pi.ProductSessionSummary)
	for {
		page, err := client.ListSessions(ctx, params)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if item.OwnerBotID == botID && sessionIsActive(item) {
				active[item.SessionID] = item
			}
		}
		if page.NextCursor == nil {
			return active, nil
		}
		params.Cursor = *page.NextCursor
	}
}

func sessionIsActive(item pi.ProductSessionSummary) bool {
	return item.State == pi.ProductSessionStarting || item.State == pi.ProductSessionRunning
}

func (s *sessionWorkspaceState) activeCount(botID string) int {
	return len(s.active[botID])
}

func updateActiveSession(active map[string]pi.ProductSessionSummary, item pi.ProductSessionSummary) {
	if current, ok := active[item.SessionID]; ok && sessionSummaryNewer(current, item) {
		return
	}
	if sessionIsActive(item) {
		active[item.SessionID] = item
	} else {
		delete(active, item.SessionID)
	}
}

func (s *sessionWorkspaceState) openSessionInspection(deps sessionInspectorDeps) {
	if deps.botID == "" {
		return
	}
	s.blocked = deps.blocked
	s.updateUI = deps.updateUI
	s.persistCloseIntents = deps.persistCloseIntents
	bot := s.botSession(deps.botID)
	bot.focusBack = true
	s.activeBotID = bot.botID
	if s.client != deps.client {
		s.setSessionInspectorClient(deps.client)
		return
	}
	s.refreshSessionInspection()
}

func (s *sessionWorkspaceState) setSessionInspectorClient(client *pi.ProductClient) {
	if s.client == client {
		return
	}
	s.client = client
	if client == nil {
		clear(s.active)
	}
	s.readEpoch++
	for _, bot := range s.bots {
		bot.listLoading = false
		bot.activeChanges = nil
		if detail := bot.detail; detail != nil {
			detail.loading, detail.ready, detail.output.loading = false, false, false
		}
	}
	s.refreshSessionInspection()
}

func (s *sessionWorkspaceState) refreshSessionInspection() {
	if s.client == nil || s.updateUI == nil {
		return
	}
	if bot := s.bots[s.activeBotID]; bot != nil {
		s.loadSessionPage(bot, bot.page, bot.cursors[bot.page])
		if bot.detail != nil {
			s.loadSessionDetail(bot, bot.detail)
		}
	}
	if s.blocked {
		return
	}
	for _, bot := range s.bots {
		for _, intent := range bot.closes {
			if intent.status == "killUnknown" {
				s.reconcileSessionClose(bot, intent)
			}
		}
	}
}

func (s *sessionWorkspaceState) backSessionInspection(deps sessionInspectorDeps) {
	bot := s.bots[deps.botID]
	if bot.detail != nil {
		bot.focusRow, bot.detail = bot.detail.summary.SessionID, nil
		return
	}
	s.activeBotID = ""
	if deps.onBack != nil {
		deps.onBack()
	}
}

func runSessionRequest[T any](updateUI func(func()), timeout time.Duration, call func(context.Context) (T, error), complete func(T, error)) {
	if updateUI == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		result, err := call(ctx)
		cancel()
		updateUI(func() { complete(result, err) })
	}()
}

func (s *sessionWorkspaceState) loadSessionPage(bot *botSessionState, page int, cursor string) {
	client, epoch := s.client, s.readEpoch
	if client == nil || bot.listLoading {
		return
	}
	bot.listLoading, bot.listFailed = true, false
	params := pi.ProductSessionListParams{BotID: bot.botID, Cursor: cursor, Limit: sessionPageSize}
	type sessionPage struct {
		pi.ProductPage[pi.ProductSessionSummary]
		active map[string]pi.ProductSessionSummary
	}
	if page == 0 {
		bot.activeChanges = make(map[string]pi.ProductSessionSummary)
	}
	runSessionRequest(s.updateUI, sessionReadTimeout, func(ctx context.Context) (sessionPage, error) {
		result, err := client.ListSessions(ctx, params)
		var active map[string]pi.ProductSessionSummary
		if err == nil && page == 0 && result.NextCursor != nil {
			active, _ = listBotActiveSessions(ctx, client, params.BotID)
		}
		return sessionPage{ProductPage: result, active: active}, err
	}, func(result sessionPage, err error) {
		if s.readEpoch != epoch || s.bots[bot.botID] != bot {
			return
		}
		bot.listLoading, bot.listFailed = false, err != nil
		changes := bot.activeChanges
		bot.activeChanges = nil
		if err != nil {
			return
		}
		items := result.Items[:0]
		for _, item := range result.Items {
			if item.OwnerBotID != bot.botID {
				continue
			}
			for _, current := range bot.items {
				if current.SessionID == item.SessionID && sessionSummaryNewer(current, item) {
					item = current
					break
				}
			}
			items = append(items, item)
		}
		bot.items = items
		if page == 0 {
			active := result.active
			if result.NextCursor == nil {
				active = make(map[string]pi.ProductSessionSummary)
				for _, item := range items {
					updateActiveSession(active, item)
				}
			}
			if active != nil {
				for _, item := range changes {
					updateActiveSession(active, item)
				}
				if s.active == nil {
					s.active = make(map[string]map[string]pi.ProductSessionSummary)
				}
				s.active[bot.botID] = active
			}
		}
		if page != bot.page {
			bot.listScroll.Y = 0
			bot.focusBack = true
		}
		bot.page, bot.nextCursor = page, ""
		if result.NextCursor != nil {
			bot.nextCursor = *result.NextCursor
		}
		if page == len(bot.cursors) {
			bot.cursors = append(bot.cursors, cursor)
		}
		bot.cursors = bot.cursors[:page+1]
	})
}

func sessionSummaryNewer(a, b pi.ProductSessionSummary) bool {
	return a.Generation > b.Generation || a.Generation == b.Generation && a.Revision > b.Revision
}

func (s *sessionWorkspaceState) selectSession(bot *botSessionState, summary pi.ProductSessionSummary) {
	bot.detail = &sessionDetailState{summary: summary}
	bot.focusBack = true
	s.loadSessionDetail(bot, bot.detail)
}

func (s *sessionWorkspaceState) loadSessionDetail(bot *botSessionState, detail *sessionDetailState) {
	client, epoch := s.client, s.readEpoch
	if client == nil || detail.loading {
		return
	}
	detail.loading, detail.failed = true, false
	id := detail.summary.SessionID
	runSessionRequest(s.updateUI, sessionReadTimeout, func(ctx context.Context) (pi.ProductSessionSummary, error) {
		return client.GetSession(ctx, bot.botID, id)
	}, func(summary pi.ProductSessionSummary, err error) {
		if s.readEpoch != epoch || s.bots[bot.botID] != bot || bot.detail != detail {
			return
		}
		detail.loading = false
		detail.ready = err == nil && summary.OwnerBotID == bot.botID && summary.SessionID == id
		detail.failed = !detail.ready
		if detail.ready {
			if !sessionSummaryNewer(detail.summary, summary) {
				s.applySessionChanged(summary)
			}
			detail.output.failed = false
		}
	})
}

func (s *sessionWorkspaceState) applySessionChanged(summary pi.ProductSessionSummary) {
	active := s.active[summary.OwnerBotID]
	if active == nil && sessionIsActive(summary) {
		if s.active == nil {
			s.active = make(map[string]map[string]pi.ProductSessionSummary)
		}
		active = make(map[string]pi.ProductSessionSummary)
		s.active[summary.OwnerBotID] = active
	}
	updateActiveSession(active, summary)
	bot := s.bots[summary.OwnerBotID]
	if bot == nil {
		return
	}
	if bot.activeChanges != nil {
		if current := bot.activeChanges[summary.SessionID]; !sessionSummaryNewer(current, summary) {
			bot.activeChanges[summary.SessionID] = summary
		}
	}
	for i, item := range bot.items {
		if item.SessionID == summary.SessionID && !sessionSummaryNewer(item, summary) {
			bot.items[i] = summary
			break
		}
	}
	detail := bot.detail
	if detail == nil || detail.summary.SessionID != summary.SessionID || sessionSummaryNewer(detail.summary, summary) {
		return
	}
	if detail.summary.Generation != summary.Generation {
		*detail = sessionDetailState{summary: summary, ready: detail.ready, loading: detail.loading}
	}
	detail.summary = summary
	detail.output.updateWatermark(summary.FirstAvailableOffset, summary.NextOffset, summary.EvictedBytes, summary.EOF)
}

func (s *sessionWorkspaceState) applySessionOutputUpdated(event pi.ProductSessionOutputUpdated) {
	if bot := s.bots[event.OwnerBotID]; bot != nil {
		detail := bot.detail
		if detail != nil && detail.summary.SessionID == event.SessionID && detail.summary.Generation == event.Generation {
			detail.output.updateWatermark(event.FirstAvailableOffset, event.NextOffset, event.EvictedBytes, event.EOF)
		}
	}
}

func (output *sessionOutputState) updateWatermark(first, next, evicted uint64, eof bool) {
	output.firstAvailable = max(output.firstAvailable, first)
	output.highWater = max(output.highWater, next)
	output.evicted = max(output.evicted, evicted)
	output.eof = output.eof || eof
}

func (output *sessionOutputState) atTail() bool {
	return !output.browsing && output.scroll.Y >= output.scroll.MaxY-1
}

type sessionOutputRead uint8

const (
	sessionOutputLatest sessionOutputRead = iota
	sessionOutputFollow
	sessionOutputEarlier
	sessionOutputLater
)

func (s *sessionWorkspaceState) loadSessionOutput(bot *botSessionState, detail *sessionDetailState, read sessionOutputRead) {
	client, epoch := s.client, s.readEpoch
	output := &detail.output
	if client == nil || !detail.ready || output.loading {
		return
	}
	params := pi.ProductSessionReadParams{BotID: bot.botID, SessionID: detail.summary.SessionID, Generation: detail.summary.Generation, Limit: sessionReadSize}
	var offset uint64
	switch read {
	case sessionOutputFollow, sessionOutputLater:
		offset = output.next
		params.Offset = &offset
	case sessionOutputEarlier:
		offset = output.from - min(output.from-output.firstAvailable, sessionReadSize)
		params.Offset = &offset
	}
	output.loading, output.failed = true, false
	runSessionRequest(s.updateUI, sessionReadTimeout, func(ctx context.Context) (pi.ProductSessionReadResult, error) {
		return client.ReadSession(ctx, params)
	}, func(result pi.ProductSessionReadResult, err error) {
		if s.readEpoch != epoch || s.bots[bot.botID] != bot || bot.detail != detail || detail.summary.Generation != params.Generation {
			return
		}
		output.loading = false
		if err != nil || result.SessionID != params.SessionID || result.Generation != params.Generation {
			output.failed = true
			if sessionRemoteCode(err) == "stale_generation" {
				detail.ready = false
				s.loadSessionDetail(bot, detail)
			}
			return
		}
		output.updateWatermark(result.FirstAvailableOffset, result.HighWaterOffset, result.FirstAvailableOffset, result.EOF)
		if result.Revision >= output.revision {
			output.revision, output.authPause = result.Revision, result.AuthPause
		}
		if result.Revision > detail.summary.Revision {
			s.loadSessionDetail(bot, detail)
		}
		if read == sessionOutputFollow && !output.atTail() {
			return
		}
		if read == sessionOutputFollow && result.NextOffset <= offset && result.HighWaterOffset > offset {
			output.failed = true
			return
		}
		output.applyRead(result, read)
	})
}

func (output *sessionOutputState) applyRead(result pi.ProductSessionReadResult, read sessionOutputRead) {
	if result.Gap {
		output.gapBytes = max(output.gapBytes, result.GapBytes)
	}
	if read != sessionOutputFollow || result.FromOffset != output.next {
		clear(output.chunks)
		output.chunks = output.chunks[:0]
		output.from = result.FromOffset
	}
	output.chunks = append(output.chunks, result.Chunks...)
	output.next, output.loaded, output.textDirty = result.NextOffset, true, true
	keep := 0
	for keep < len(output.chunks) && output.next-output.chunks[keep].Offset > sessionOutputLimit {
		output.from = output.chunks[keep].NextOffset
		keep++
	}
	if keep > 0 {
		remaining := copy(output.chunks, output.chunks[keep:])
		clear(output.chunks[remaining:])
		output.chunks = output.chunks[:remaining]
	}
	output.browsing = read == sessionOutputEarlier || read == sessionOutputLater && output.next < output.highWater
	output.scroll.Y = 0
	if !output.browsing {
		output.scroll.Y = math.MaxFloat32
	}
}

func (output *sessionOutputState) projectedText(mode pi.ProductSessionMode, words sessionInspectionCopy) string {
	stdout, stderr := words.text("stdout"), words.text("stderr")
	if !output.textDirty && output.stdout == stdout && output.stderr == stderr {
		return output.text
	}
	var text strings.Builder
	if output.next > output.from {
		text.Grow(int(min(output.next-output.from, sessionOutputLimit)))
	}
	var stream string
	endOfLine := true
	for _, chunk := range output.chunks {
		if mode == pi.ProductSessionPipe && chunk.Stream != stream {
			if !endOfLine {
				text.WriteByte('\n')
			}
			label := stdout
			if chunk.Stream == "stderr" {
				label = stderr
			}
			text.WriteByte('[')
			text.WriteString(label)
			text.WriteString("]\n")
			stream = chunk.Stream
		}
		text.WriteString(chunk.Text)
		endOfLine = strings.HasSuffix(chunk.Text, "\n")
	}
	output.text, output.textDirty, output.stdout, output.stderr = text.String(), false, stdout, stderr
	return output.text
}

func sessionRemoteCode(err error) string {
	var remote *pi.RemoteError
	if errors.As(err, &remote) {
		return remote.Code
	}
	return ""
}

func (s *sessionWorkspaceState) pendingSessionCloses() []pi.ProductSessionCloseParams {
	var pending []pi.ProductSessionCloseParams
	for _, bot := range s.bots {
		for _, intent := range bot.closes {
			if intent.unresolved() {
				pending = append(pending, intent.params)
			}
		}
	}
	slices.SortFunc(pending, func(a, b pi.ProductSessionCloseParams) int {
		if order := cmp.Compare(a.BotID, b.BotID); order != 0 {
			return order
		}
		if order := cmp.Compare(a.SessionID, b.SessionID); order != 0 {
			return order
		}
		if order := cmp.Compare(a.Generation, b.Generation); order != 0 {
			return order
		}
		return cmp.Compare(a.OperationID, b.OperationID)
	})
	return pending
}

func (s *sessionWorkspaceState) restoreSessionCloses(pending []pi.ProductSessionCloseParams) {
	for _, params := range pending {
		bot := s.botSession(params.BotID)
		run := sessionRun{params.SessionID, params.Generation}
		if bot.closes[run] == nil {
			bot.closes[run] = &sessionCloseIntent{params: params, status: "killUnknown"}
		}
	}
}

func (detail *sessionDetailState) canClose(intent *sessionCloseIntent) bool {
	state := detail.summary.State
	if !detail.ready || detail.loading || detail.output.revision > detail.summary.Revision || state != pi.ProductSessionStarting && state != pi.ProductSessionRunning {
		return false
	}
	return intent == nil || !intent.unresolved() && intent.result == nil
}

func (s *sessionWorkspaceState) closeSession(bot *botSessionState, detail *sessionDetailState, blocked bool) {
	client := s.client
	summary := detail.summary
	run := sessionRun{summary.SessionID, summary.Generation}
	if client == nil || blocked || s.activeBotID != bot.botID || bot.detail != detail || !detail.canClose(bot.closes[run]) {
		return
	}
	params := pi.ProductSessionCloseParams{BotID: bot.botID, SessionID: run.id, Generation: run.generation, ExpectedRevision: &summary.Revision, OperationID: newContactID()}
	intent := &sessionCloseIntent{params: params, status: "killPending"}
	bot.closes[run] = intent
	if s.persistCloseIntents != nil && s.persistCloseIntents(s.pendingSessionCloses()) != nil {
		intent.status = "killSaveError"
		return
	}
	runSessionRequest(s.updateUI, 15*time.Second, func(ctx context.Context) (pi.ProductReceipt, error) {
		return client.CloseSession(ctx, params)
	}, func(receipt pi.ProductReceipt, err error) {
		if s.bots[bot.botID] != bot {
			return
		}
		s.applySessionCloseReceipt(intent, receipt, err)
		s.applySessionCloseSummary(bot, intent)
		if intent.unresolved() {
			intent.status = "killUnknown"
			s.reconcileSessionClose(bot, intent)
			return
		}
		s.persistResolvedSessionCloses()
		s.readSessionCloseTarget(bot, intent)
	})
}

func (s *sessionWorkspaceState) reconcileSessionClose(bot *botSessionState, intent *sessionCloseIntent) {
	client := s.client
	if client == nil || intent.checking || intent.status != "killUnknown" {
		return
	}
	intent.checking = true
	s.readSessionCloseTarget(bot, intent)
	runSessionRequest(s.updateUI, sessionReadTimeout, func(ctx context.Context) (pi.ProductReceipt, error) {
		return client.Operation(ctx, intent.params.OperationID)
	}, func(receipt pi.ProductReceipt, err error) {
		if s.bots[bot.botID] != bot {
			return
		}
		intent.checking = false
		if err == nil {
			s.applySessionCloseReceipt(intent, receipt, nil)
			s.applySessionCloseSummary(bot, intent)
		}
		if !intent.unresolved() {
			s.persistResolvedSessionCloses()
		} else if s.client != client {
			s.reconcileSessionClose(bot, intent)
		}
	})
}

func (s *sessionWorkspaceState) readSessionCloseTarget(bot *botSessionState, intent *sessionCloseIntent) {
	if detail := bot.detail; detail != nil && detail.summary.SessionID == intent.params.SessionID {
		s.loadSessionDetail(bot, detail)
		return
	}
	client, epoch := s.client, s.readEpoch
	if client == nil {
		return
	}
	runSessionRequest(s.updateUI, sessionReadTimeout, func(ctx context.Context) (pi.ProductSessionSummary, error) {
		return client.GetSession(ctx, intent.params.BotID, intent.params.SessionID)
	}, func(summary pi.ProductSessionSummary, err error) {
		if err == nil && s.readEpoch == epoch && s.bots[bot.botID] == bot && summary.OwnerBotID == intent.params.BotID && summary.SessionID == intent.params.SessionID {
			s.applySessionChanged(summary)
		}
	})
}

func (s *sessionWorkspaceState) applySessionCloseReceipt(intent *sessionCloseIntent, receipt pi.ProductReceipt, err error) {
	if err != nil {
		var command *pi.CommandError
		switch sessionRemoteCode(err) {
		case "stale_generation":
			intent.status = "killStaleGeneration"
		case "stale_revision":
			intent.status = "killStaleRevision"
		case "not_found", "invalid_params", "operation_conflict", "scope_denied":
			intent.status = "killRejected"
		default:
			if errors.As(err, &command) && command.Delivery == pi.NotSent {
				intent.status = "killNotSent"
			}
		}
		return
	}
	params := intent.params
	if receipt.OperationID != params.OperationID || receipt.Target.BotID != params.BotID || receipt.Target.SessionID != params.SessionID {
		return
	}
	if receipt.State == "rejected" {
		intent.status = "killRejected"
		return
	}
	if receipt.State != "accepted" && receipt.State != "unknown" {
		return
	}
	result, decodeErr := pi.DecodeSessionCloseResult(receipt)
	if decodeErr != nil || result.SessionID != params.SessionID || result.Generation != params.Generation || params.ExpectedRevision != nil && result.Revision < *params.ExpectedRevision {
		return
	}
	if result.CloseDisposition != "terminated" && result.CloseDisposition != "already_settled" {
		return
	}
	intent.result = &result
	switch {
	case receipt.State == "unknown":
		intent.status = "killUnknown"
	case result.CloseDisposition == "already_settled":
		intent.status = "killSettled"
	case result.ChildReaped:
		intent.status = "killTerminated"
	default:
		intent.status = "killAccepted"
	}
}

func (s *sessionWorkspaceState) persistResolvedSessionCloses() {
	if s.persistCloseIntents != nil {
		_ = s.persistCloseIntents(s.pendingSessionCloses())
	}
}

func (s *sessionWorkspaceState) applySessionCloseSummary(bot *botSessionState, intent *sessionCloseIntent) {
	result, detail := intent.result, bot.detail
	if result == nil || detail == nil {
		return
	}
	summary := detail.summary
	if summary.SessionID != result.SessionID || summary.Generation != result.Generation || summary.Revision > result.Revision {
		return
	}
	summary.State, summary.Revision = result.State, result.Revision
	summary.ExitCode, summary.ExitSignal, summary.TransportStatus = result.ExitCode, result.ExitSignal, result.TransportStatus
	summary.AuthPause = nil
	s.applySessionChanged(summary)
}

func (s *sessionWorkspaceState) sessionInspectionView(c *ui.Context, deps sessionInspectorDeps) {
	bot := s.bots[deps.botID]
	if bot == nil || s.activeBotID != bot.botID {
		return
	}
	words, wb := deps.copy, deps.wb
	ui.Column(c).Key("session-inspector").Fill().MinWidth(0).MinHeight(0).Background(wb.Panel).Children(func() {
		ui.Row(c).FillWidth().MinHeight(max(float32(48), c.TitleBar().Height)).Padding(8, max(12, deps.headerRightPad), 8, max(12, deps.headerLeftPad)).AlignItems(ui.Center).Gap(8).Shrink(0).DragWindow().Children(func() {
			if deps.showContacts != nil {
				deps.showContacts(c, wb.Theme)
			}
			back := detailBackButton(c, wb.Theme, words.text("back"), words.text("back"), words.text("back")).OnClick(func() { s.backSessionInspection(deps) })
			if bot.focusBack {
				back.Focus()
				bot.focusBack = false
			}
			title := words.text("title")
			heading := ui.Text(c, title).Role(ui.RoleHeading).Level(1).FontSize(16).FontWeight(600).
				FixedLineHeight(24).TextColor(wb.Theme.Text).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			appTooltip(c, heading, title)
			if detail := bot.detail; detail != nil {
				intent := bot.closes[sessionRun{detail.summary.SessionID, detail.summary.Generation}]
				theme.DangerButton(c, words.text("kill")).Shrink(0).
					Disabled(deps.blocked || s.client == nil || !detail.canClose(intent)).
					OnClick(func() { s.closeSession(bot, detail, deps.blocked) })
			} else {
				theme.SecondaryButton(c, words.text("reload")).Key("session-list-reload").Shrink(0).
					Disabled(s.client == nil || bot.listLoading).
					OnClick(func() { s.loadSessionPage(bot, 0, "") })
			}
		})
		if bot.detail == nil {
			s.sessionListView(c, bot, deps)
		} else {
			s.sessionDetailView(c, bot, bot.detail, deps)
		}
	})
}

func sessionBody(c *ui.Context, key string, scroll *ui.ScrollState, contents func()) {
	ui.Scroll(c).Key(key).FillWidth().Grow(1).MinHeight(0).TrackScroll(scroll).Children(func() {
		ui.Row(c).FillWidth().Justify(ui.Center).Children(func() {
			ui.Column(c).FillWidth().MinWidth(0).MaxWidth(conversationMaxWidth).Padding(12, 16, 32).Gap(16).Children(contents)
		})
	})
}

func (s *sessionWorkspaceState) sessionListView(c *ui.Context, bot *botSessionState, deps sessionInspectorDeps) {
	words, wb := deps.copy, deps.wb
	sessionBody(c, "session-list", &bot.listScroll, func() {
		switch {
		case s.client == nil:
			sessionNotice(c, wb, words.text("hostDisconnected"))
		case bot.listLoading && len(bot.items) == 0:
			sessionNotice(c, wb, words.text("loadingList"))
		case bot.listFailed:
			sessionNotice(c, wb, words.text("listError"))
		case len(bot.items) == 0:
			sessionNotice(c, wb, words.text("emptyList"))
		}
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			for _, item := range bot.items {
				title := sessionTitle(item, words)
				button := theme.NavigationControl(c, ui.Button(c, "").Key(item.SessionID).FillWidth().MinHeight(theme.ControlHit).
					Padding(8, 12).Label(title+", "+words.text(string(item.State))), wb, false).
					OnClick(func() { s.selectSession(bot, item) })
				if bot.focusRow == item.SessionID {
					button.Focus().ScrollIntoView()
					bot.focusRow = ""
				}
				button.Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
						appTooltip(c, ui.Text(c, title).TextColor(wb.Theme.Text).FontSize(typeAction).FontWeight(500).
							FixedLineHeight(lineLabel).SingleLine().Ellipsis("…").Grow(1).MinWidth(0), title)
						sessionBadge(c, wb, words.text(string(item.State)), string(item.State))
					})
				})
			}
		})
		if bot.page > 0 || bot.nextCursor != "" {
			ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
				if bot.page > 0 {
					theme.SecondaryButton(c, words.text("previousPage")).Disabled(s.client == nil || bot.listLoading).OnClick(func() {
						s.loadSessionPage(bot, bot.page-1, bot.cursors[bot.page-1])
					})
				}
				if bot.nextCursor != "" {
					theme.SecondaryButton(c, words.text("loadMore")).Disabled(s.client == nil || bot.listLoading).OnClick(func() {
						s.loadSessionPage(bot, bot.page+1, bot.nextCursor)
					})
				}
			})
		}
	})
}

func (s *sessionWorkspaceState) sessionDetailView(c *ui.Context, bot *botSessionState, detail *sessionDetailState, deps sessionInspectorDeps) {
	words, wb, summary := deps.copy, deps.wb, detail.summary
	intent := bot.closes[sessionRun{summary.SessionID, summary.Generation}]
	sessionBody(c, "session-detail", &detail.scroll, func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
				title := sessionTitle(summary, words)
				appTooltip(c, sessionHeading(c, wb, title).Grow(1).SingleLine().Ellipsis("…"), title)
				sessionBadge(c, wb, words.text(string(summary.State)), string(summary.State))
			})
			if summary.State != pi.ProductSessionStarting && summary.State != pi.ProductSessionRunning {
				sessionNotice(c, wb, words.text("readOnly"))
			}
			switch {
			case s.client == nil || summary.State == pi.ProductSessionDisconnected:
				sessionNotice(c, wb, words.text("hostDisconnected"))
			case detail.failed:
				sessionNotice(c, wb, words.text("detailError"))
			case detail.loading && !detail.ready:
				sessionNotice(c, wb, words.text("loadingDetail"))
			case deps.blocked && detail.canClose(intent):
				sessionNotice(c, wb, words.text("killBlocked"))
			}
			if intent != nil {
				s.sessionCloseView(c, bot, intent, deps)
			} else {
				var previous *sessionCloseIntent
				for run, candidate := range bot.closes {
					if run.id == summary.SessionID && run.generation < summary.Generation && (previous == nil || run.generation > previous.params.Generation) {
						previous = candidate
					}
				}
				if previous != nil {
					sessionNotice(c, wb, words.text("previousClose"))
					s.sessionCloseView(c, bot, previous, deps)
				}
			}
			ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
				if detail.failed || detail.output.failed {
					theme.SecondaryButton(c, words.text("reload")).Disabled(s.client == nil || detail.loading).
						OnClick(func() { s.loadSessionDetail(bot, detail) })
				}
				if summary.OriginConversationID != "" && deps.onOpenConversation != nil {
					link := theme.LinkButton(c, words.text("sourceChat")).TextColor(wb.Link).Padding(0).Justify(ui.Start).
						OnClick(func() {
							bot.focusSourceChat = true
							deps.onOpenConversation(summary.OriginConversationID)
						})
					if bot.focusSourceChat {
						link.Focus()
						bot.focusSourceChat = false
					}
				}
				if summary.OriginActivityID != "" && deps.onOpenActivity != nil {
					theme.LinkButton(c, words.text("sourceActivity")).TextColor(wb.Link).Padding(0).Justify(ui.Start).
						OnClick(func() { deps.onOpenActivity(summary.OriginActivityID) })
				}
			})
		})
		sessionMetadata(c, wb, deps, summary)
		s.sessionOutputView(c, bot, detail, deps)
		sessionWatchesView(c, wb, deps, summary)
	})
}

func sessionTitle(summary pi.ProductSessionSummary, words sessionInspectionCopy) string {
	if summary.Label != "" {
		return summary.Label
	}
	if summary.CommandSummary != "" {
		return summary.CommandSummary
	}
	return words.text("session")
}

func sessionMetadata(c *ui.Context, wb theme.WorkbenchTheme, deps sessionInspectorDeps, summary pi.ProductSessionSummary) {
	words := deps.copy
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		sessionMetadataText(c, wb, words.text("command"), summary.CommandSummary)
		sessionMetadataText(c, wb, words.text("directory"), summary.Cwd)
		ui.Row(c).FillWidth().Gap(16).Wrap().Children(func() {
			sessionField(c, wb, words.text("mode"), words.text(string(summary.Mode)))
			if summary.Cols != nil && summary.Rows != nil {
				dimensions := ""
				if deps.formatDimensions != nil {
					dimensions = deps.formatDimensions(*summary.Cols, *summary.Rows)
				} else {
					dimensions = fmt.Sprintf(words.text("dimensions"), *summary.Cols, *summary.Rows)
				}
				sessionField(c, wb, words.text("size"), dimensions)
			}
			transport := string(summary.TransportStatus)
			if summary.TransportStatus == pi.ProductTransportEOF {
				transport = "transportEOF"
			}
			sessionField(c, wb, words.text("transport"), words.text(transport))
			sessionExitFields(c, wb, words, summary.ExitCode, summary.ExitSignal)
		})
	})
}

func sessionExitFields(c *ui.Context, wb theme.WorkbenchTheme, words sessionInspectionCopy, code *int, signal string) {
	if code != nil {
		sessionField(c, wb, words.text("exitCode"), fmt.Sprint(*code))
	}
	sessionField(c, wb, words.text("exitSignal"), signal)
}

func (s *sessionWorkspaceState) sessionCloseView(c *ui.Context, bot *botSessionState, intent *sessionCloseIntent, deps sessionInspectorDeps) {
	words, wb := deps.copy, deps.wb
	status := intent.status
	if intent.checking {
		status = "killPending"
	}
	sessionNotice(c, wb, words.text(status))
	if result := intent.result; result != nil && status != "killTerminated" && status != "killPending" {
		key := "killUnreaped"
		if result.ChildReaped {
			key = "killReaped"
		}
		sessionNotice(c, wb, words.text(key))
	}
	if intent.status == "killUnknown" {
		theme.SecondaryButton(c, words.text("checkClose")).AlignSelf(ui.Start).
			Disabled(s.client == nil || intent.checking).OnClick(func() { s.reconcileSessionClose(bot, intent) })
	}
}

func (s *sessionWorkspaceState) sessionOutputView(c *ui.Context, bot *botSessionState, detail *sessionDetailState, deps sessionInspectorDeps) {
	words, wb, output := deps.copy, deps.wb, &detail.output
	newOutput := output.loaded && output.next < output.highWater
	if !output.failed {
		switch {
		case !output.loaded:
			s.loadSessionOutput(bot, detail, sessionOutputLatest)
		case newOutput && output.atTail():
			s.loadSessionOutput(bot, detail, sessionOutputFollow)
		}
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
			sessionHeading(c, wb, words.text("output")).Grow(1)
			if output.loaded && output.from > output.firstAvailable {
				theme.SecondaryButton(c, words.text("earlierOutput")).Disabled(output.loading || s.client == nil || !detail.ready).
					OnClick(func() { s.loadSessionOutput(bot, detail, sessionOutputEarlier) })
			}
			if output.browsing && newOutput {
				theme.SecondaryButton(c, words.text("laterOutput")).Disabled(output.loading || s.client == nil || !detail.ready).
					OnClick(func() { s.loadSessionOutput(bot, detail, sessionOutputLater) })
			}
			if output.browsing || newOutput && !output.atTail() {
				theme.SecondaryButton(c, words.text("latestOutput")).Disabled(output.loading || s.client == nil || !detail.ready).
					OnClick(func() { s.loadSessionOutput(bot, detail, sessionOutputLatest) })
			}
		})
		auth := detail.summary.AuthPause
		if output.revision >= detail.summary.Revision && output.loaded {
			auth = output.authPause
		}
		if auth != nil && auth.Paused {
			ui.Column(c).FillWidth().Gap(4).Background(wb.WorkWarningBg).Radius(theme.CardRadius).Padding(8, 12).Children(func() {
				ui.Text(c, words.text("authPaused")).FontSize(typeAction).FontWeight(500).TextColor(wb.WorkAttention)
				sessionNotice(c, wb, words.text(auth.Reason))
				sessionNotice(c, wb, words.text("authAction"))
			})
		}
		if gap := max(output.gapBytes, output.evicted); gap > 0 {
			formattedGap := ""
			if deps.formatGap != nil {
				formattedGap = deps.formatGap(sessionNumber(gap))
			} else {
				formattedGap = fmt.Sprintf(words.text("outputGap"), sessionNumber(gap))
			}
			sessionNotice(c, wb, formattedGap)
		}
		ui.Scroll(c).Key("projected-output").FillWidth().Height(224).MinWidth(0).Clip().TrackScroll(&output.scroll).
			Background(wb.Reading).Border(1, wb.InputBorder).Radius(theme.CardRadius).Children(func() {
			ui.Column(c).FillWidth().MinWidth(0).Padding(12, 16, 32).Children(func() {
				text := output.projectedText(detail.summary.Mode, words)
				if text != "" {
					ui.Text(c, text).FillWidth().MinWidth(0).Selectable().Font("monospace").FontSize(typeAction).
						FixedLineHeight(lineLabel).TextColor(wb.Theme.Text)
				} else if output.loaded {
					sessionNotice(c, wb, words.text("emptyOutput"))
				}
			})
		})
		status := ""
		switch {
		case output.loading:
			status = "reading"
		case output.failed:
			status = "outputError"
		case newOutput && !output.atTail():
			status = "newOutput"
		case output.eof && output.loaded && !newOutput:
			status = "eof"
		}
		ui.Text(c, words.text(status)).FillWidth().MinHeight(lineLabel).FontSize(typeMeta).FixedLineHeight(lineLabel).TextColor(wb.Theme.TextMuted)
	})
}

func sessionWatchesView(c *ui.Context, wb theme.WorkbenchTheme, deps sessionInspectorDeps, summary pi.ProductSessionSummary) {
	words := deps.copy
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		sessionHeading(c, wb, words.text("watches"))
		count := 0
		for _, watch := range summary.Watches {
			if watch.SessionID != summary.SessionID || watch.Generation != summary.Generation {
				continue
			}
			count++
			ui.Column(c).Key(watch.WatchID).FillWidth().MinWidth(0).Gap(8).Padding(8, 0).Children(func() {
				sessionBadge(c, wb, words.text(watch.State), watch.State).AlignSelf(ui.Start)
				conditions := make([]string, 0, len(watch.Conditions))
				for _, condition := range watch.Conditions {
					conditions = append(conditions, words.text(condition))
				}
				sessionField(c, wb, words.text("conditions"), strings.Join(conditions, ", "))
				ui.Row(c).FillWidth().Gap(16).Wrap().Children(func() {
					if watch.IdleMS != nil {
						idleText := ""
						if deps.formatMS != nil {
							idleText = deps.formatMS(sessionNumber(*watch.IdleMS))
						} else {
							idleText = fmt.Sprintf(words.text("milliseconds"), sessionNumber(*watch.IdleMS))
						}
						sessionField(c, wb, words.text("idleTime"), idleText)
					}
					if watch.TimeoutMS != nil {
						timeoutText := ""
						if deps.formatMS != nil {
							timeoutText = deps.formatMS(sessionNumber(*watch.TimeoutMS))
						} else {
							timeoutText = fmt.Sprintf(words.text("milliseconds"), sessionNumber(*watch.TimeoutMS))
						}
						sessionField(c, wb, words.text("timeoutTime"), timeoutText)
					}
					sessionField(c, wb, words.text("firedKind"), words.text(watch.FiredKind))
				})
				sessionField(c, wb, words.text("matchLiteral"), watch.MatchLiteral)
				sessionField(c, wb, words.text("matchedExcerpt"), watch.MatchedExcerpt)
			})
		}
		if count == 0 {
			sessionNotice(c, wb, words.text("noWatches"))
		}
	})
}

func sessionNumber(n uint64) string {
	value := fmt.Sprint(n)
	var grouped strings.Builder
	for i, digit := range value {
		if i > 0 && (len(value)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	return grouped.String()
}

func sessionHeading(c *ui.Context, wb theme.WorkbenchTheme, text string) ui.Element {
	return ui.Text(c, text).MinWidth(0).FontSize(16).FontWeight(600).FixedLineHeight(24).TextColor(wb.Theme.Text)
}

func sessionBadge(c *ui.Context, wb theme.WorkbenchTheme, text, state string) ui.Element {
	ink := wb.Theme.TextMuted
	switch state {
	case "running", "armed", "fired":
		ink = wb.Theme.Success
	case "failed", "disconnected", "target_lost":
		ink = wb.Theme.Danger
	case "starting":
		ink = wb.WorkAttention
	}
	return ui.Text(c, text).Shrink(0).FontSize(typeMeta).FixedLineHeight(lineLabel).TextColor(ink).
		Background(wb.Reading).Border(1, wb.Theme.Border).Radius(theme.PillRadius).Padding(2, 8)
}

func sessionNotice(c *ui.Context, wb theme.WorkbenchTheme, value string) {
	if value != "" {
		ui.Text(c, value).FillWidth().MinWidth(0).FontSize(typeMeta).FixedLineHeight(lineLabel).TextColor(wb.Theme.TextMuted)
	}
}

func sessionField(c *ui.Context, wb theme.WorkbenchTheme, label, value string) {
	if value == "" {
		return
	}
	ui.Column(c).MinWidth(0).Gap(2).Children(func() {
		sessionNotice(c, wb, label)
		ui.Text(c, value).Selectable().MinWidth(0).FontSize(typeAction).FixedLineHeight(lineLabel).TextColor(wb.Theme.Text)
	})
}

func sessionMetadataText(c *ui.Context, wb theme.WorkbenchTheme, label, value string) {
	if value == "" {
		return
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(2).Children(func() {
		sessionNotice(c, wb, label)
		appTooltip(c, ui.Text(c, value).Selectable().FillWidth().MinWidth(0).SingleLine().Ellipsis("…").
			FontSize(typeAction).FixedLineHeight(lineLabel).TextColor(wb.Theme.Text), value)
	})
}

func sessionInspectionLocaleCopy(msgs locales.Messages) sessionInspectionCopy {
	return sessionInspectionCopy{
		"title":               msgs.SessionInspectTitle(),
		"back":                msgs.SessionInspectBack(),
		"reload":              msgs.SessionInspectReload(),
		"loadMore":            msgs.SessionInspectLoadMore(),
		"previousPage":        msgs.SessionInspectPreviousPage(),
		"loadingList":         msgs.SessionInspectLoadingList(),
		"emptyList":           msgs.SessionInspectEmptyList(),
		"listError":           msgs.SessionInspectListError(),
		"loadingDetail":       msgs.SessionInspectLoadingDetail(),
		"detailError":         msgs.SessionInspectDetailError(),
		"session":             msgs.SessionInspectSession(),
		"starting":            msgs.SessionInspectStarting(),
		"running":             msgs.SessionInspectRunning(),
		"exited":              msgs.SessionInspectExited(),
		"closed":              msgs.SessionInspectClosed(),
		"disconnected":        msgs.SessionInspectDisconnected(),
		"failed":              msgs.SessionInspectFailed(),
		"readOnly":            msgs.SessionInspectReadOnly(),
		"sourceChat":          msgs.SessionInspectSourceChat(),
		"sourceActivity":      msgs.SessionInspectSourceActivity(),
		"command":             msgs.SessionInspectCommand(),
		"directory":           msgs.SessionInspectDirectory(),
		"mode":                msgs.SessionInspectMode(),
		"pty":                 msgs.SessionInspectPty(),
		"pipe":                msgs.SessionInspectPipe(),
		"size":                msgs.SessionInspectSize(),
		"exitCode":            msgs.SessionInspectExitCode(),
		"exitSignal":          msgs.SessionInspectExitSignal(),
		"transport":           msgs.SessionInspectTransport(),
		"live":                msgs.SessionInspectLive(),
		"eof":                 msgs.SessionInspectEof(),
		"transportEOF":        msgs.SessionInspectTransportEOF(),
		"supervisor_lost":     msgs.SessionInspectSupervisorLost(),
		"worker_restarted":    msgs.SessionInspectWorkerRestarted(),
		"closed_by_user":      msgs.SessionInspectClosedByUser(),
		"closed_by_bot":       msgs.SessionInspectClosedByBot(),
		"closed_on_shutdown":  msgs.SessionInspectClosedOnShutdown(),
		"exec_failed":         msgs.SessionInspectExecFailed(),
		"output":              msgs.SessionInspectOutput(),
		"stdout":              msgs.SessionInspectStdout(),
		"stderr":              msgs.SessionInspectStderr(),
		"reading":             msgs.SessionInspectReading(),
		"emptyOutput":         msgs.SessionInspectEmptyOutput(),
		"outputError":         msgs.SessionInspectOutputError(),
		"hostDisconnected":    msgs.SessionInspectHostDisconnected(),
		"earlierOutput":       msgs.SessionInspectEarlierOutput(),
		"laterOutput":         msgs.SessionInspectLaterOutput(),
		"latestOutput":        msgs.SessionInspectLatestOutput(),
		"newOutput":           msgs.SessionInspectNewOutput(),
		"authPaused":          msgs.SessionInspectAuthPaused(),
		"auth_prompt":         msgs.SessionInspectAuthPrompt(),
		"canonical_no_echo":   msgs.SessionInspectCanonicalNoEcho(),
		"authAction":          msgs.SessionInspectAuthAction(),
		"watches":             msgs.SessionInspectWatches(),
		"noWatches":           msgs.SessionInspectNoWatches(),
		"armed":               msgs.SessionInspectArmed(),
		"fired":               msgs.SessionInspectFired(),
		"cancelled":           msgs.SessionInspectCancelled(),
		"target_lost":         msgs.SessionInspectTargetLost(),
		"conditions":          msgs.SessionInspectConditions(),
		"exit":                msgs.SessionInspectExit(),
		"match":               msgs.SessionInspectMatch(),
		"idle":                msgs.SessionInspectIdle(),
		"timeout":             msgs.SessionInspectTimeout(),
		"matchLiteral":        msgs.SessionInspectMatchLiteral(),
		"idleTime":            msgs.SessionInspectIdleTime(),
		"timeoutTime":         msgs.SessionInspectTimeoutTime(),
		"firedKind":           msgs.SessionInspectFiredKind(),
		"matchedExcerpt":      msgs.SessionInspectMatchedExcerpt(),
		"kill":                msgs.SessionInspectKill(),
		"killBlocked":         msgs.SessionInspectKillBlocked(),
		"killPending":         msgs.SessionInspectKillPending(),
		"killUnknown":         msgs.SessionInspectKillUnknown(),
		"checkClose":          msgs.SessionInspectCheckClose(),
		"killAccepted":        msgs.SessionInspectKillAccepted(),
		"killTerminated":      msgs.SessionInspectKillTerminated(),
		"killSettled":         msgs.SessionInspectKillSettled(),
		"killUnreaped":        msgs.SessionInspectKillUnreaped(),
		"killReaped":          msgs.SessionInspectKillReaped(),
		"previousClose":       msgs.SessionInspectPreviousClose(),
		"killStaleGeneration": msgs.SessionInspectKillStaleGeneration(),
		"killStaleRevision":   msgs.SessionInspectKillStaleRevision(),
		"killRejected":        msgs.SessionInspectKillRejected(),
		"killNotSent":         msgs.SessionInspectKillNotSent(),
		"killSaveError":       msgs.SessionInspectKillSaveError(),
	}
}
