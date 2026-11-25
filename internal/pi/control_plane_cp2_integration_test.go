//go:build integration

package pi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

type cp2ToolCall struct {
	ID   string
	Name string
	Args string
}

type cp2Reply struct {
	Text        string
	Calls       []cp2ToolCall
	HTTPStatus  int
	RedirectURL string
	RawError    string
}

type cp2WireMsg struct {
	Role       string
	Type       string
	Content    string
	ToolCallID string
	Raw        json.RawMessage
}

type cp2Request struct {
	API          string
	IsTriage     bool
	IsActivity   bool
	System       string
	Instructions string
	MaxTokens    int
	Tools        []string
	Messages     []cp2WireMsg
	LastRole     string
	LastToolID   string
	LastContent  string
	RawUserText  string
	BaseUserText string
	BoardText    string
	ToolResults  map[string]string
	Ctx          context.Context
}

type cp2Provider struct {
	t          *testing.T
	server     *httptest.Server
	secret     string
	totalCalls atomic.Int64
	fgCalls    atomic.Int64
	actCalls   atomic.Int64
	triageCall atomic.Int64
	inFlight   atomic.Int64
	maxFlight  atomic.Int64
	seq        atomic.Int64
	mu         sync.Mutex
	handler    func(req cp2Request) cp2Reply
	requests   []cp2Request
	problems   []string
	forbid     []string
}

func newCP2Provider(t *testing.T, secret string, handler func(req cp2Request) cp2Reply) *cp2Provider {
	t.Helper()
	p := &cp2Provider{t: t, secret: secret, handler: handler}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

func (p *cp2Provider) setHandler(fn func(req cp2Request) cp2Reply) {
	p.mu.Lock()
	p.handler = fn
	p.mu.Unlock()
}

func (p *cp2Provider) addForbiddenSecret(value string) {
	p.mu.Lock()
	p.forbid = append(p.forbid, value)
	p.mu.Unlock()
}

func (p *cp2Provider) recordProblem(msg string) {
	p.mu.Lock()
	p.problems = append(p.problems, msg)
	p.mu.Unlock()
}

func (p *cp2Provider) assertNoProblems() {
	p.t.Helper()
	p.mu.Lock()
	probs := append([]string(nil), p.problems...)
	p.mu.Unlock()
	if len(probs) > 0 {
		p.t.Fatalf("cp2 provider problems: %v", probs)
	}
}

func (p *cp2Provider) snapshotRequests() []cp2Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]cp2Request(nil), p.requests...)
}

func (p *cp2Provider) resetMaxFlight() {
	p.maxFlight.Store(0)
}

func extractCP2Board(text string) string {
	start := strings.Index(text, "<active_activities>\n")
	if start < 0 {
		return ""
	}
	end := strings.Index(text[start:], "</active_activities>")
	if end < 0 {
		return ""
	}
	return text[start : start+end+len("</active_activities>")]
}

func stripCP2Board(text string) string {
	if idx := strings.Index(text, "\n\n<active_activities>"); idx >= 0 {
		return text[:idx]
	}
	return text
}

func decodeCP2Content(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(part.Text)
		}
		return b.String()
	}
	return string(raw)
}

func (p *cp2Provider) serve(w http.ResponseWriter, r *http.Request) {
	var api string
	switch r.URL.Path {
	case "/v1/chat/completions":
		api = "completions"
	case "/v1/responses":
		api = "responses"
	default:
		p.recordProblem("unexpected path " + r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	cur := p.inFlight.Add(1)
	defer p.inFlight.Add(-1)
	for {
		prev := p.maxFlight.Load()
		if cur <= prev || p.maxFlight.CompareAndSwap(prev, cur) {
			break
		}
	}
	p.totalCalls.Add(1)
	if p.secret != "" && r.Header.Get("Authorization") != "Bearer "+p.secret {
		p.recordProblem("unexpected auth header")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		p.recordProblem("read provider request: " + err.Error())
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	forbid := append([]string(nil), p.forbid...)
	handler := p.handler
	p.mu.Unlock()
	for _, secret := range forbid {
		if secret != "" && bytes.Contains(rawBody, []byte(secret)) {
			p.recordProblem("forbidden secret leaked into outbound provider body")
		}
	}
	var body struct {
		Model               string            `json:"model"`
		Instructions        string            `json:"instructions"`
		MaxTokens           int               `json:"max_tokens"`
		MaxCompletionTokens int               `json:"max_completion_tokens"`
		MaxOutputTokens     int               `json:"max_output_tokens"`
		Messages            []json.RawMessage `json:"messages"`
		Input               []json.RawMessage `json:"input"`
		Tools               []struct {
			Name     string `json:"name"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rawBody, &body); err != nil {
		p.recordProblem("decode provider request: " + err.Error())
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	rawItems := body.Messages
	if api == "responses" {
		rawItems = body.Input
	}
	msgs := make([]cp2WireMsg, 0, len(rawItems))
	var sysParts []string
	if body.Instructions != "" {
		sysParts = append(sysParts, body.Instructions)
	}
	var rawUser string
	toolResults := map[string]string{}
	for _, rawItem := range rawItems {
		var item struct {
			Role       string          `json:"role"`
			Type       string          `json:"type"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			CallID     string          `json:"call_id"`
			Output     json.RawMessage `json:"output"`
		}
		_ = json.Unmarshal(rawItem, &item)
		text := decodeCP2Content(item.Content)
		if item.Type == "function_call_output" {
			text = decodeCP2Content(item.Output)
		}
		callID := item.ToolCallID
		if callID == "" {
			callID = item.CallID
		}
		wm := cp2WireMsg{
			Role:       item.Role,
			Type:       item.Type,
			Content:    text,
			ToolCallID: callID,
			Raw:        rawItem,
		}
		msgs = append(msgs, wm)
		switch {
		case item.Role == "system" || item.Role == "developer":
			sysParts = append(sysParts, text)
		case item.Role == "user":
			rawUser = text
		case item.Role == "tool" || item.Type == "function_call_output":
			if callID != "" {
				toolResults[callID] = text
			}
		}
	}
	tools := make([]string, 0, len(body.Tools))
	for _, item := range body.Tools {
		if item.Name != "" {
			tools = append(tools, item.Name)
		} else if item.Function.Name != "" {
			tools = append(tools, item.Function.Name)
		}
	}
	if !slices.IsSorted(tools) {
		p.recordProblem(fmt.Sprintf("tools not sorted: %v", tools))
	}
	sysJoined := strings.Join(sysParts, "\n")
	isTriage := strings.Contains(sysJoined, "Nagi preference triage.")
	isActivity := slices.Contains(tools, "activity_ask")
	switch {
	case isTriage:
		p.triageCall.Add(1)
	case isActivity:
		p.actCalls.Add(1)
	default:
		p.fgCalls.Add(1)
	}
	maxTokens := body.MaxTokens
	if body.MaxCompletionTokens > maxTokens {
		maxTokens = body.MaxCompletionTokens
	}
	if body.MaxOutputTokens > maxTokens {
		maxTokens = body.MaxOutputTokens
	}
	var lastRole, lastToolID, lastContent string
	if len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		lastRole = last.Role
		if lastRole == "" {
			lastRole = last.Type
		}
		lastToolID = last.ToolCallID
		lastContent = last.Content
	}
	req := cp2Request{
		API:          api,
		IsTriage:     isTriage,
		IsActivity:   isActivity,
		System:       sysJoined,
		Instructions: body.Instructions,
		MaxTokens:    maxTokens,
		Tools:        tools,
		Messages:     msgs,
		LastRole:     lastRole,
		LastToolID:   lastToolID,
		LastContent:  lastContent,
		RawUserText:  rawUser,
		BaseUserText: stripCP2Board(rawUser),
		BoardText:    extractCP2Board(rawUser),
		ToolResults:  toolResults,
		Ctx:          r.Context(),
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	var reply cp2Reply
	if handler != nil {
		reply = handler(req)
	}
	if r.Context().Err() != nil {
		return
	}
	if reply.RedirectURL != "" {
		status := reply.HTTPStatus
		if status == 0 {
			status = http.StatusTemporaryRedirect
		}
		w.Header().Set("Location", reply.RedirectURL)
		w.WriteHeader(status)
		return
	}
	if reply.HTTPStatus >= 400 {
		msg := reply.RawError
		if msg == "" {
			msg = "controlled provider failure"
		}
		http.Error(w, msg, reply.HTTPStatus)
		return
	}
	if api == "responses" {
		p.writeResponsesSSE(w, reply)
		return
	}
	writeCP1SSE(w, cp1Reply{
		Text:  reply.Text,
		Calls: cp2ToCP1Calls(reply.Calls),
	})
}

func cp2ToCP1Calls(calls []cp2ToolCall) []cp1ToolCall {
	out := make([]cp1ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, cp1ToolCall{ID: c.ID, Name: c.Name, Args: c.Args})
	}
	return out
}

func (p *cp2Provider) writeResponsesSSE(w http.ResponseWriter, reply cp2Reply) {
	w.Header().Set("Content-Type", "text/event-stream")
	n := p.seq.Add(1)
	respID := fmt.Sprintf("resp_cp2_%d", n)
	send := func(evType string, data map[string]any) {
		data["type"] = evType
		raw, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evType, raw)
	}
	var output []map[string]any
	if len(reply.Calls) > 0 {
		for i, c := range reply.Calls {
			args := c.Args
			if args == "" {
				args = "{}"
			}
			output = append(output, map[string]any{
				"type":      "function_call",
				"id":        fmt.Sprintf("fc_cp2_%d_%d", n, i),
				"call_id":   c.ID,
				"name":      c.Name,
				"arguments": args,
				"status":    "completed",
			})
		}
	} else {
		text := reply.Text
		if text == "" {
			text = "OK"
		}
		output = append(output, map[string]any{
			"type":   "message",
			"id":     fmt.Sprintf("msg_cp2_%d", n),
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			}},
		})
	}
	send("response.created", map[string]any{"response": map[string]any{"id": respID, "status": "in_progress"}})
	for i, item := range output {
		send("response.output_item.done", map[string]any{"output_index": i, "item": item})
	}
	send("response.completed", map[string]any{
		"response": map[string]any{
			"id":     respID,
			"status": "completed",
			"output": output,
			"usage":  map[string]any{"input_tokens": 25, "output_tokens": 10, "total_tokens": 35},
		},
	})
}

func cp2ConfigureConnection(t *testing.T, ctx context.Context, client *pi.ProductClient, wire pi.WireAPI, baseURL, apiKey, ws string, capacity int) {
	t.Helper()
	saved, err := client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: fmt.Sprintf("cp2-conn-save-%d", time.Now().UnixNano()),
		Configuration: pi.ProductConnectionInput{
			APIKey: apiKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI:          wire,
				BaseURL:          baseURL,
				Model:            productModel,
				ContextTokens:    262144,
				OutputTokens:     4096,
				WorkingDirectory: ws,
				MaxActiveTasks:   capacity,
			},
		},
	})
	if err != nil || saved.SavedRevision == nil {
		t.Fatalf("SaveConnection = %+v, %v", saved, err)
	}
	if _, err := client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{
		OperationID:   fmt.Sprintf("cp2-conn-apply-%d", time.Now().UnixNano()),
		SavedRevision: *saved.SavedRevision,
	}); err != nil {
		t.Fatalf("ApplyConnection = %v", err)
	}
}

func cp2WaitDigest(t *testing.T, ctx context.Context, p *cp1Process, match func(d pi.ProductActivityDigest) bool) pi.ProductActivityDigest {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		for _, ev := range p.events {
			if ev.Name == "activity.digest" && ev.ActivityDigest != nil && match(*ev.ActivityDigest) {
				got := *ev.ActivityDigest
				p.mu.Unlock()
				return got
			}
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(15 * time.Millisecond):
		}
	}
	t.Fatal("timed out waiting for matching activity.digest event")
	return pi.ProductActivityDigest{}
}

func cp2WaitQuestion(t *testing.T, ctx context.Context, p *cp1Process, activityID, state string) pi.ProductQuestion {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		act, err := p.client.GetActivity(ctx, activityID)
		if err == nil {
			for _, q := range act.Questions {
				if state == "" || q.State == state {
					return q
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(15 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for activity %s question state %q", activityID, state)
	return pi.ProductQuestion{}
}

func cp2WaitNewQuestion(t *testing.T, ctx context.Context, p *cp1Process, activityID, state string, seen map[string]bool) pi.ProductQuestion {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		act, err := p.client.GetActivity(ctx, activityID)
		if err == nil {
			for _, q := range act.Questions {
				if !seen[q.QuestionID] && (state == "" || q.State == state) {
					return q
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(15 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for new question state %q on activity %s", state, activityID)
	return pi.ProductQuestion{}
}

func cp2FindActivityByGoal(t *testing.T, ctx context.Context, p *cp1Process, botID, goal string) pi.ProductActivity {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		list, err := p.client.ListActivities(ctx, pi.ProductActivityListParams{BotID: botID})
		if err == nil {
			for _, item := range list.Items {
				if item.Goal == goal {
					act, err := p.client.GetActivity(ctx, item.ActivityID)
					if err == nil && len(act.Executions) > 0 {
						return act
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(15 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for activity with goal %q", goal)
	return pi.ProductActivity{}
}

func cp2TriageAccounting(t *testing.T, nodePath, dbPath, activityID string) (attempts int, autoAnswers int, found bool) {
	t.Helper()
	out := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const row = db.prepare('SELECT attempts_used, auto_answers_used FROM activity_triage_accounting WHERE activity_id = ?').get(JSON.stringify(process.argv[2]));
db.close();
console.log(JSON.stringify(row ? { found: true, attempts: row.attempts_used, autoAnswers: row.auto_answers_used } : { found: false }));
`, activityID)
	var parsed struct {
		Found       bool `json:"found"`
		Attempts    int  `json:"attempts"`
		AutoAnswers int  `json:"autoAnswers"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse triage accounting %q: %v", out, err)
	}
	return parsed.Attempts, parsed.AutoAnswers, parsed.Found
}

func cp2AssertNoBoardPersisted(t *testing.T, ctx context.Context, p *cp1Process, stateDir, convID string) {
	t.Helper()
	var jsonlCount int
	err := filepath.WalkDir(filepath.Join(stateDir, "sessions"), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		jsonlCount++
		raw := productRead(t, path)
		if bytes.Contains(raw, []byte("</active_activities>")) || bytes.Contains(raw, []byte("Untrusted activity telemetry.")) {
			t.Fatalf("ephemeral whiteboard leaked into Pi session transcript %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk sessions: %v", err)
	}
	if jsonlCount == 0 {
		t.Fatal("expected at least one Pi session JSONL file")
	}
	hist, err := p.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
		ProductPageParams:         pi.ProductPageParams{Limit: 100},
	})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	rawHist, _ := json.Marshal(hist)
	if bytes.Contains(rawHist, []byte("<active_activities>")) || bytes.Contains(rawHist, []byte("Untrusted activity telemetry.")) {
		t.Fatalf("ephemeral whiteboard leaked into conversation.history: %s", rawHist)
	}
}

func cp2SessionRefFromProfile(t *testing.T, profilePath, convID string) string {
	t.Helper()
	var doc struct {
		Conversations []struct {
			ConversationID string `json:"conversationId"`
			Binding        *struct {
				SessionRef string `json:"sessionRef"`
			} `json:"binding"`
		} `json:"conversations"`
	}
	if err := json.Unmarshal(productRead(t, profilePath), &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Conversations {
		if c.ConversationID == convID && c.Binding != nil && c.Binding.SessionRef != "" {
			return c.Binding.SessionRef
		}
	}
	t.Fatalf("sessionRef not found for conversation %s", convID)
	return ""
}

func cp2AppendSessionLine(t *testing.T, sessionPath string, build func(leafID string, entries []map[string]any) map[string]any) {
	t.Helper()
	raw := productRead(t, sessionPath)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	entries := make([]map[string]any, 0, len(lines))
	var leafID string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("parse session line: %v", err)
		}
		entries = append(entries, obj)
		if typ, _ := obj["type"].(string); typ != "session" {
			if id, _ := obj["id"].(string); id != "" {
				leafID = id
			}
		}
	}
	if leafID == "" {
		t.Fatalf("no leaf entry found in %s", sessionPath)
	}
	next := build(leafID, entries)
	encoded, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(sessionPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(encoded, '\n')); err != nil {
		t.Fatal(err)
	}
}

func cp2FormattingAskArgs(extra map[string]any) string {
	args := map[string]any{
		"text": "Which output format should I use?",
		"options": []map[string]any{
			{"id": "markdown", "label": "Markdown"},
			{"id": "plain_text", "label": "Plain text"},
		},
		"allow_free_text": false,
		"preference_key":  "formatting",
	}
	for k, v := range extra {
		args[k] = v
	}
	raw, _ := json.Marshal(args)
	return string(raw)
}

func cp2SHA256File(t *testing.T, path string) string {
	t.Helper()
	sum := sha256.Sum256(productRead(t, path))
	return hex.EncodeToString(sum[:])
}

func TestPiControlPlaneCP2WhiteboardProjectionCompactionAndDigests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	nodePath, workerPath := cp1WorkerPaths(t)
	cases := []struct {
		name     string
		wire     pi.WireAPI
		api      string
		capacity int
	}{
		{name: "completions-cap1", wire: pi.OpenAICompletions, api: "completions", capacity: 1},
		{name: "responses-cap2", wire: pi.OpenAIResponses, api: "responses", capacity: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			ws := filepath.Join(root, "ws")
			if err := os.MkdirAll(ws, 0o700); err != nil {
				t.Fatal(err)
			}
			const secret = "cp2-board-secret-X9mK4vP2"

			var mu sync.Mutex
			var botBID string
			var parkedTargetID string
			var seq atomic.Int64
			nextCall := func(prefix string) string {
				return fmt.Sprintf("%s_%d", prefix, seq.Add(1))
			}

			provider := newCP2Provider(t, secret, func(req cp2Request) cp2Reply {
				mu.Lock()
				targetBotB := botBID
				targetAct := parkedTargetID
				mu.Unlock()

				if req.IsActivity {
					switch {
					case strings.Contains(req.BaseUserText, "CP2_PARK_"):
						if req.LastRole == "user" {
							args, _ := json.Marshal(map[string]any{
								"text": "Préférences <user> & \"漢字\" 'q'",
							})
							return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: string(args)}}}
						}
						return cp2Reply{Text: "Human response processed."}
					case strings.Contains(req.BaseUserText, "CP2_DONE_"):
						return cp2Reply{Text: "Résumé 漢字 🙂 <example> & 'ok'"}
					default:
						return cp2Reply{Text: "Activity finished."}
					}
				}

				switch {
				case strings.HasPrefix(req.BaseUserText, "CP2_EMPTY"):
					return cp2Reply{Text: "Empty board reply."}
				case strings.HasPrefix(req.BaseUserText, "CP2_DEL_"):
					if req.LastRole == "user" {
						goal := strings.TrimPrefix(req.BaseUserText, "CP2_DEL_")
						args, _ := json.Marshal(map[string]any{
							"key":         "key_" + goal,
							"goal":        goal,
							"assignee_id": targetBotB,
						})
						return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("del"), Name: "activity_delegate", Args: string(args)}}}
					}
					return cp2Reply{Text: "Delegated."}
				case strings.HasPrefix(req.BaseUserText, "CP2_BOARD_PROBE"):
					if req.LastRole == "user" {
						getArgs, _ := json.Marshal(map[string]any{"activity_id": targetAct})
						return cp2Reply{Calls: []cp2ToolCall{
							{ID: nextCall("list"), Name: "activity_list", Args: `{}`},
							{ID: nextCall("get"), Name: "activity_get", Args: string(getArgs)},
						}}
					}
					return cp2Reply{Text: "Foreground board inspected."}
				case strings.HasPrefix(req.BaseUserText, "CP2_REOPEN_MID"):
					if req.LastRole == "user" {
						getArgs, _ := json.Marshal(map[string]any{"activity_id": targetAct})
						return cp2Reply{Calls: []cp2ToolCall{
							{ID: nextCall("rlist"), Name: "activity_list", Args: `{}`},
							{ID: nextCall("rget"), Name: "activity_get", Args: string(getArgs)},
						}}
					}
					return cp2Reply{Text: "Reopened mid-history foreground."}
				default:
					return cp2Reply{Text: "Foreground OK."}
				}
			})
			provider.addForbiddenSecret(secret)

			opts := pi.ProductOptions{
				NodePath:        nodePath,
				WorkerPath:      workerPath,
				StateDir:        filepath.Join(root, "pi"),
				ProfilePath:     filepath.Join(root, "contacts.json"),
				CredentialsPath: filepath.Join(root, "connection.json"),
				FreshProfile:    true,
			}
			proc := startCP1Process(t, opts)
			_ = proc.open(t, ctx)
			cp2ConfigureConnection(t, ctx, proc.client, tc.wire, provider.server.URL+"/v1", secret, ws, tc.capacity)

			botA, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
				OperationID: "cp2-create-a",
				Profile: pi.ProductBotProfile{
					Name: "OriginA", Role: "Coordinator", Purpose: "CP2 foreground",
					Instructions: "Coordinate cleanly.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			botB, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
				OperationID: "cp2-create-b",
				Profile: pi.ProductBotProfile{
					Name: "AssigneeB", Role: "Specialist", Purpose: "CP2 worker",
					Instructions: "Execute delegated work.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			botBID = botB.BotID
			mu.Unlock()

			if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "empty", "CP2_EMPTY", nil); settled.State != "completed" {
				t.Fatalf("empty turn settled = %+v", settled)
			}
			reqs := provider.snapshotRequests()
			if len(reqs) != 1 || reqs[0].BoardText != "" {
				t.Fatalf("empty board should omit whiteboard, got reqs=%d board=%q", len(reqs), reqs[0].BoardText)
			}

			var completedActs []pi.ProductActivity
			for i := 0; i < 6; i++ {
				goal := fmt.Sprintf("CP2_DONE_%d_漢字_<tag>&", i)
				fgBefore := provider.fgCalls.Load()
				if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, fmt.Sprintf("del-done-%d", i), "CP2_DEL_"+goal, nil); settled.State != "completed" {
					t.Fatalf("delegate done %d settled = %+v", i, settled)
				}
				fgAfterDelegate := provider.fgCalls.Load()
				if fgAfterDelegate-fgBefore != 2 {
					t.Fatalf("delegate turn fgCalls delta = %d, want 2", fgAfterDelegate-fgBefore)
				}
				act := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, goal)
				execID := act.Executions[len(act.Executions)-1].ExecutionID
				if settled := proc.waitSettled(t, ctx, execID); settled.State != "completed" {
					t.Fatalf("done activity %d settled = %+v", i, settled)
				}
				digest := cp2WaitDigest(t, ctx, proc, func(d pi.ProductActivityDigest) bool {
					return d.ActivityID == act.ActivityID && d.Kind == "completed"
				})
				if digest.OwnerBotID != botB.BotID || digest.OriginConversationID != botA.CurrentConversationID || digest.ExecutionID != execID || digest.FactID == "" {
					t.Fatalf("completed digest mismatch: %+v", digest)
				}
				if provider.fgCalls.Load() != fgAfterDelegate || provider.triageCall.Load() != 0 {
					t.Fatalf("digest triggered unexpected LLM call: fg=%d vs %d triage=%d", provider.fgCalls.Load(), fgAfterDelegate, provider.triageCall.Load())
				}
				completedActs = append(completedActs, act)
			}

			type parkedItem struct {
				act pi.ProductActivity
				q   pi.ProductQuestion
			}
			var parked []parkedItem
			for i := 0; i < tc.capacity; i++ {
				goal := fmt.Sprintf("CP2_PARK_%d", i)
				if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, fmt.Sprintf("del-park-%d", i), "CP2_DEL_"+goal, nil); settled.State != "completed" {
					t.Fatalf("delegate park %d settled = %+v", i, settled)
				}
				fgAfterDelegate := provider.fgCalls.Load()
				act := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, goal)
				q := cp2WaitQuestion(t, ctx, proc, act.ActivityID, "open")
				digest := cp2WaitDigest(t, ctx, proc, func(d pi.ProductActivityDigest) bool {
					return d.ActivityID == act.ActivityID && d.Kind == "question_open" && d.QuestionID == q.QuestionID
				})
				if digest.OwnerBotID != botB.BotID || digest.OriginConversationID != botA.CurrentConversationID {
					t.Fatalf("question_open digest mismatch: %+v", digest)
				}
				if provider.fgCalls.Load() != fgAfterDelegate {
					t.Fatalf("question_open digest triggered foreground LLM call")
				}
				parked = append(parked, parkedItem{act: act, q: q})
			}

			mu.Lock()
			parkedTargetID = parked[0].act.ActivityID
			mu.Unlock()
			provider.resetMaxFlight()
			reqStart := len(provider.snapshotRequests())

			if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "board-probe", "CP2_BOARD_PROBE", nil); settled.State != "completed" {
				t.Fatalf("saturated foreground board probe settled = %+v", settled)
			}
			if gotMax := provider.maxFlight.Load(); gotMax > int64(tc.capacity) {
				t.Fatalf("capacity exceeded during saturated foreground turn: maxInFlight=%d limit=%d", gotMax, tc.capacity)
			}
			allReqs := provider.snapshotRequests()
			probeReqs := allReqs[reqStart:]
			if len(probeReqs) != 2 {
				t.Fatalf("expected 2 foreground requests in multi-tool probe, got %d", len(probeReqs))
			}
			board := probeReqs[0].BoardText
			if board == "" || len(board) > 1536 || !utf8.ValidString(board) {
				t.Fatalf("whiteboard invalid/oversized: len=%d board=%q", len(board), board)
			}
			for _, want := range []string{
				"Untrusted activity telemetry.",
				"more active/unread activities",
				"&lt;user&gt; &amp; &quot;漢字&quot; &apos;q&apos;",
			} {
				if !strings.Contains(board, want) {
					t.Fatalf("whiteboard missing %q: %s", want, board)
				}
			}
			for _, item := range parked {
				if !strings.Contains(board, item.act.ActivityID) || !strings.Contains(board, item.q.QuestionID) {
					t.Fatalf("whiteboard missing exact parked IDs %s / %s: %s", item.act.ActivityID, item.q.QuestionID, board)
				}
			}
			if probeReqs[1].RawUserText != probeReqs[0].RawUserText {
				t.Fatalf("multi-tool turn mutated decorated user message: req0=%q req1=%q", probeReqs[0].RawUserText, probeReqs[1].RawUserText)
			}
			userIdx := -1
			for i := len(probeReqs[0].Messages) - 1; i >= 0; i-- {
				if probeReqs[0].Messages[i].Role == "user" {
					userIdx = i
					break
				}
			}
			if userIdx < 0 || len(probeReqs[1].Messages) <= userIdx {
				t.Fatalf("invalid user index %d in multi-tool requests", userIdx)
			}
			for i := 0; i <= userIdx; i++ {
				if !bytes.Equal(probeReqs[0].Messages[i].Raw, probeReqs[1].Messages[i].Raw) {
					t.Fatalf("multi-tool prefix message[%d] drifted:\nreq0=%s\nreq1=%s", i, probeReqs[0].Messages[i].Raw, probeReqs[1].Messages[i].Raw)
				}
			}
			if !slices.Equal(probeReqs[0].Tools, probeReqs[1].Tools) {
				t.Fatalf("multi-tool definitions drifted: %v vs %v", probeReqs[0].Tools, probeReqs[1].Tools)
			}

			for i, item := range parked {
				ansRes, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
					ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
						ProductActivityParams: pi.ProductActivityParams{ActivityID: item.act.ActivityID},
						ExecutionID:           item.q.ExecutionID,
					},
					ProductText:      pi.ProductText{Text: fmt.Sprintf("Human reply %d", i)},
					QuestionID:       item.q.QuestionID,
					QuestionRevision: item.q.Revision,
					OperationID:      fmt.Sprintf("cp2-ans-park-%d", i),
				})
				if err != nil || !ansRes.AnswerSaved || ansRes.Provenance != "user" {
					t.Fatalf("AnswerQuestion parked[%d] = %+v, %v", i, ansRes, err)
				}
				if settled := proc.waitSettled(t, ctx, item.q.ExecutionID); settled.State != "completed" {
					t.Fatalf("parked activity %d settled = %+v", i, settled)
				}
				gotAct, err := proc.client.GetActivity(ctx, item.act.ActivityID)
				if err != nil || len(gotAct.Questions) != 1 || gotAct.Questions[0].Delivery != "delivered" {
					t.Fatalf("parked activity %d delivery = %+v, %v", i, gotAct.Questions, err)
				}
				if _, err := proc.client.AcknowledgeActivity(ctx, pi.ProductActivityAcknowledgeParams{
					ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
						ProductActivityParams: pi.ProductActivityParams{ActivityID: gotAct.ActivityID},
						ExecutionID:           item.q.ExecutionID,
					},
					ExpectedRevision: gotAct.Revision,
					OperationID:      fmt.Sprintf("cp2-ack-park-%d", i),
				}); err != nil {
					t.Fatalf("ack parked[%d]: %v", i, err)
				}
			}
			for i, act := range completedActs {
				latest, err := proc.client.GetActivity(ctx, act.ActivityID)
				if err != nil {
					t.Fatal(err)
				}
				execID := latest.Executions[len(latest.Executions)-1].ExecutionID
				if _, err := proc.client.AcknowledgeActivity(ctx, pi.ProductActivityAcknowledgeParams{
					ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
						ProductActivityParams: pi.ProductActivityParams{ActivityID: latest.ActivityID},
						ExecutionID:           execID,
					},
					ExpectedRevision: latest.Revision,
					OperationID:      fmt.Sprintf("cp2-ack-done-%d", i),
				}); err != nil {
					t.Fatalf("ack done[%d]: %v", i, err)
				}
			}
			unreadAfterAck, err := proc.client.ListActivities(ctx, pi.ProductActivityListParams{
				BotID:              botA.BotID,
				UnacknowledgedOnly: true,
			})
			if err != nil || len(unreadAfterAck.Items) != 0 {
				t.Fatalf("expected 0 unacknowledged activities, got %+v, %v", unreadAfterAck.Items, err)
			}

			beforeCleanTurn := len(provider.snapshotRequests())
			if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "after-ack", "CP2_CLEAN_NEXT_TURN", nil); settled.State != "completed" {
				t.Fatalf("clean next turn settled = %+v", settled)
			}
			cleanReqs := provider.snapshotRequests()[beforeCleanTurn:]
			if len(cleanReqs) != 1 || cleanReqs[0].BoardText != "" {
				t.Fatalf("next turn after ack had unexpected whiteboard: %q", cleanReqs[0].BoardText)
			}
			for _, msg := range cleanReqs[0].Messages {
				if msg.Role != "system" && msg.Role != "developer" && strings.Contains(msg.Content, "<active_activities>") {
					t.Fatalf("prior whiteboard leaked into next turn history: %q", msg.Content)
				}
			}

			if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-fresh", "CP2_DEL_CP2_DONE_FRESH", nil); settled.State != "completed" {
				t.Fatalf("delegate fresh settled = %+v", settled)
			}
			freshAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_DONE_FRESH")
			if settled := proc.waitSettled(t, ctx, freshAct.Executions[len(freshAct.Executions)-1].ExecutionID); settled.State != "completed" {
				t.Fatalf("fresh activity settled = %+v", settled)
			}
			cp2AssertNoBoardPersisted(t, ctx, proc, opts.StateDir, botA.CurrentConversationID)
			proc.shutdown(t, ctx)

			sessionPath := cp2SessionRefFromProfile(t, opts.ProfilePath, botA.CurrentConversationID)
			const midSysMarker = "CP2_MID_HISTORY_SYSTEM_CHANGE"
			cp2AppendSessionLine(t, sessionPath, func(leafID string, _ []map[string]any) map[string]any {
				return map[string]any{
					"type":      "message",
					"id":        "cp2-mid-sys-1",
					"parentId":  leafID,
					"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
					"message": map[string]any{
						"role":      "system",
						"content":   midSysMarker,
						"timestamp": time.Now().UnixMilli(),
						"sections":  map[string]string{"cp2-probe": "changed"},
						"toolsAdded": []map[string]any{{
							"name":        "cp2_archived_tool",
							"description": "Controlled historical tool",
							"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
						}},
					},
				}
			})

			callsBeforeReopen := provider.totalCalls.Load()
			opts.FreshProfile = false
			proc2 := startCP1Process(t, opts)
			_ = proc2.open(t, ctx)
			if provider.totalCalls.Load() != callsBeforeReopen {
				t.Fatalf("reopen triggered unexpected provider calls: before=%d after=%d", callsBeforeReopen, provider.totalCalls.Load())
			}
			midReqStart := len(provider.snapshotRequests())
			if settled := cp1SendTurn(t, ctx, proc2, botA.CurrentConversationID, "reopen-mid", "CP2_REOPEN_MID", nil); settled.State != "completed" {
				t.Fatalf("reopen mid-history turn settled = %+v", settled)
			}
			midReqs := provider.snapshotRequests()[midReqStart:]
			if len(midReqs) != 2 {
				t.Fatalf("expected 2 requests for reopened multi-tool turn, got %d", len(midReqs))
			}
			firstUserIdx, midSysIdx, lastUserIdx := -1, -1, -1
			for i, m := range midReqs[0].Messages {
				if m.Role == "user" {
					if firstUserIdx < 0 {
						firstUserIdx = i
					}
					lastUserIdx = i
				}
				if (m.Role == "system" || m.Role == "developer") && strings.Contains(m.Content, midSysMarker) {
					midSysIdx = i
				}
			}
			if firstUserIdx < 0 || midSysIdx <= firstUserIdx || midSysIdx >= lastUserIdx {
				t.Fatalf("mid-history system message position not preserved on %s: firstUser=%d midSys=%d lastUser=%d", tc.api, firstUserIdx, midSysIdx, lastUserIdx)
			}
			if midReqs[0].BoardText == "" || midReqs[1].RawUserText != midReqs[0].RawUserText {
				t.Fatalf("reopened turn whiteboard mismatch: board0=%q user0=%q user1=%q", midReqs[0].BoardText, midReqs[0].RawUserText, midReqs[1].RawUserText)
			}
			for i := 0; i <= lastUserIdx; i++ {
				if !bytes.Equal(midReqs[0].Messages[i].Raw, midReqs[1].Messages[i].Raw) {
					t.Fatalf("reopened multi-tool prefix[%d] drifted", i)
				}
			}
			cp2AssertNoBoardPersisted(t, ctx, proc2, opts.StateDir, botA.CurrentConversationID)
			proc2.shutdown(t, ctx)

			const compactSummary = "CP2_COMPACTED_HISTORY_SUMMARY"
			cp2AppendSessionLine(t, sessionPath, func(leafID string, entries []map[string]any) map[string]any {
				var keptID string
				for _, entry := range entries {
					if entry["type"] != "message" {
						continue
					}
					msg, _ := entry["message"].(map[string]any)
					if msg["role"] != "user" {
						continue
					}
					rawContent, _ := json.Marshal(msg["content"])
					if strings.Contains(decodeCP2Content(rawContent), "CP2_REOPEN_MID") {
						keptID, _ = entry["id"].(string)
					}
				}
				if keptID == "" {
					t.Fatal("failed to locate kept user entry ID for compaction")
				}
				return map[string]any{
					"type":             "compaction",
					"id":               "cp2-compaction-1",
					"parentId":         leafID,
					"timestamp":        time.Now().UTC().Format(time.RFC3339Nano),
					"summary":          compactSummary,
					"firstKeptEntryId": keptID,
					"tokensBefore":     2048,
				}
			})

			callsBeforeCompact := provider.totalCalls.Load()
			proc3 := startCP1Process(t, opts)
			_ = proc3.open(t, ctx)
			if provider.totalCalls.Load() != callsBeforeCompact {
				t.Fatalf("compacted reopen triggered provider calls")
			}
			compactReqStart := len(provider.snapshotRequests())
			if settled := cp1SendTurn(t, ctx, proc3, botA.CurrentConversationID, "after-compact", "CP2_AFTER_COMPACTION", nil); settled.State != "completed" {
				t.Fatalf("post-compaction turn settled = %+v", settled)
			}
			compactReqs := provider.snapshotRequests()[compactReqStart:]
			if len(compactReqs) != 1 || compactReqs[0].BoardText == "" {
				t.Fatalf("post-compaction turn missing fresh whiteboard: reqs=%d board=%q", len(compactReqs), compactReqs[0].BoardText)
			}
			var sawSummary, sawDroppedEmpty bool
			var boardCount int
			for _, m := range compactReqs[0].Messages {
				if strings.Contains(m.Content, compactSummary) {
					sawSummary = true
				}
				if strings.Contains(m.Content, "CP2_EMPTY") {
					sawDroppedEmpty = true
				}
				if m.Role != "system" && m.Role != "developer" && extractCP2Board(m.Content) != "" {
					boardCount++
				}
			}
			if !sawSummary || sawDroppedEmpty || boardCount != 1 {
				t.Fatalf("compaction projection mismatch: sawSummary=%v sawDroppedEmpty=%v boardCount=%d", sawSummary, sawDroppedEmpty, boardCount)
			}
			cp2AssertNoBoardPersisted(t, ctx, proc3, opts.StateDir, botA.CurrentConversationID)
			proc3.shutdown(t, ctx)
			provider.assertNoProblems()
		})
	}
}

func TestPiControlPlaneCP2ApprovalParkingDigestsAndCP1P3Cleanups(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	nodePath, workerPath := cp1WorkerPaths(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "cp2-p3-approval-secret-Q8wN3rV5"

	var mu sync.Mutex
	var botBID, smallActID, largeActID string
	var smallGetJSON, largeGetJSON string
	var seq atomic.Int64
	nextCall := func(prefix string) string {
		return fmt.Sprintf("%s_%d", prefix, seq.Add(1))
	}

	provider := newCP2Provider(t, secret, func(req cp2Request) cp2Reply {
		mu.Lock()
		targetB := botBID
		sID := smallActID
		lID := largeActID
		mu.Unlock()

		if req.IsActivity {
			switch {
			case strings.Contains(req.BaseUserText, "CP2_SKILL_APPROVAL"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{
						ID:   nextCall("sk"),
						Name: "skill_exec",
						Args: `{"skill_id":"cp2-ws-skill","source":"private","script":"main"}`,
					}}}
				}
				return cp2Reply{Text: "Skill approval denied."}
			case strings.Contains(req.BaseUserText, "CP2_SMALL_ACT"):
				return cp2Reply{Text: "Small complete result."}
			case strings.Contains(req.BaseUserText, "CP2_LARGE_ACT"):
				return cp2Reply{Text: strings.Repeat("Large activity output segment. ", 700)}
			default:
				return cp2Reply{Text: "Done."}
			}
		}

		switch {
		case strings.HasPrefix(req.BaseUserText, "CP2_DEL_"):
			if req.LastRole == "user" {
				goal := strings.TrimPrefix(req.BaseUserText, "CP2_DEL_")
				args, _ := json.Marshal(map[string]any{
					"key":         "k_" + goal,
					"goal":        goal,
					"assignee_id": targetB,
				})
				return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("del"), Name: "activity_delegate", Args: string(args)}}}
			}
			return cp2Reply{Text: "Delegated."}
		case strings.HasPrefix(req.BaseUserText, "CP2_INSPECT_P3"):
			switch {
			case req.LastRole == "user":
				args, _ := json.Marshal(map[string]any{"activity_id": sID})
				return cp2Reply{Calls: []cp2ToolCall{{ID: "get-small", Name: "activity_get", Args: string(args)}}}
			case req.LastToolID == "get-small":
				mu.Lock()
				smallGetJSON = req.LastContent
				mu.Unlock()
				args, _ := json.Marshal(map[string]any{"activity_id": lID})
				return cp2Reply{Calls: []cp2ToolCall{{ID: "get-large", Name: "activity_get", Args: string(args)}}}
			default:
				mu.Lock()
				largeGetJSON = req.LastContent
				mu.Unlock()
				return cp2Reply{Text: "Inspected both."}
			}
		default:
			return cp2Reply{Text: "Foreground OK."}
		}
	})
	provider.addForbiddenSecret(secret)

	opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(root, "pi"),
		ProfilePath:     filepath.Join(root, "contacts.json"),
		CredentialsPath: filepath.Join(root, "connection.json"),
		FreshProfile:    true,
	}
	proc := startCP1Process(t, opts)
	_ = proc.open(t, ctx)
	cp2ConfigureConnection(t, ctx, proc.client, pi.OpenAICompletions, provider.server.URL+"/v1", secret, ws, 1)

	botA, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp2-p3-bot-a",
		Profile: pi.ProductBotProfile{
			Name: "OriginNone", Role: "Coordinator", Purpose: "No-read inspection",
			Instructions: "Coordinate.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	botB, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp2-p3-bot-b",
		Profile: pi.ProductBotProfile{
			Name: "WorkerWS", Role: "Engineer", Purpose: "Workspace skill",
			Instructions: "Execute skills.", Workspace: ws, ToolPreset: pi.ToolPresetWorkspace,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = botB.BotID
	mu.Unlock()

	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{
			Source:  "private",
			BotID:   botB.BotID,
			SkillID: "cp2-ws-skill",
		},
		Name:        "Workspace write skill",
		Description: "Needs approval",
		Content:     "Skill requesting workspace write approval.",
		Manifest: &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"main": {
					Entry:       "scripts/main.mjs",
					Description: "Write check",
					Permissions: pi.ProductSkillPermissions{WorkspaceRead: true, WorkspaceWrite: true},
				},
			},
		},
		Scripts:     map[string]string{"scripts/main.mjs": "export default () => ({ ok: true });"},
		OperationID: "cp2-p3-save-skill",
	}); err != nil {
		t.Fatalf("SaveSkill: %v", err)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-app", "CP2_DEL_CP2_SKILL_APPROVAL", nil); settled.State != "completed" {
		t.Fatalf("delegate approval settled = %+v", settled)
	}
	appAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_SKILL_APPROVAL")
	appDigest := cp2WaitDigest(t, ctx, proc, func(d pi.ProductActivityDigest) bool {
		return d.ActivityID == appAct.ActivityID && d.Kind == "approval_required"
	})
	if appDigest.RequestID == "" || appDigest.ExecutionID == "" {
		t.Fatalf("approval_required digest missing ids: %+v", appDigest)
	}
	appDetails, err := proc.client.GetActivity(ctx, appAct.ActivityID)
	if err != nil || len(appDetails.ApprovalRequests) != 1 || appDetails.ApprovalRequests[0].State != "open" {
		t.Fatalf("approval request not open: %+v, %v", appDetails.ApprovalRequests, err)
	}
	card := appDetails.ApprovalRequests[0]

	provider.resetMaxFlight()
	fgStart := len(provider.snapshotRequests())
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "fg-during-app", "CP2_FG_DURING_APPROVAL", nil); settled.State != "completed" {
		t.Fatalf("foreground turn during approval wait at cap=1 failed: %+v", settled)
	}
	if provider.maxFlight.Load() > 1 {
		t.Fatalf("capacity=1 exceeded during approval parking: maxInFlight=%d", provider.maxFlight.Load())
	}
	fgReqs := provider.snapshotRequests()[fgStart:]
	if len(fgReqs) != 1 || !strings.Contains(fgReqs[0].BoardText, card.RequestID) {
		t.Fatalf("foreground whiteboard during approval missing requestId %s: %q", card.RequestID, fgReqs[0].BoardText)
	}

	const expandSecret = "k9z2x8q4"
	longReason := strings.Repeat(expandSecret+" ", 55) + "end"
	if _, err := proc.client.DenySkillApprovalRequest(ctx, pi.ProductSkillDenyApprovalRequestParams{
		RequestID:       card.RequestID,
		RequestRevision: card.Revision,
		Reason:          longReason,
		OperationID:     "cp2-deny-app",
	}); err != nil {
		t.Fatalf("DenySkillApprovalRequest: %v", err)
	}
	if settled := proc.waitSettled(t, ctx, card.ExecutionID); settled.State != "completed" {
		t.Fatalf("denied approval execution settled = %+v", settled)
	}
	naDigest := cp2WaitDigest(t, ctx, proc, func(d pi.ProductActivityDigest) bool {
		return d.ActivityID == appAct.ActivityID && d.Kind == "needs_attention"
	})
	if naDigest.FactID == "" {
		t.Fatalf("needs_attention digest invalid: %+v", naDigest)
	}
	if _, err := proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{
			Account: pi.ProductProviderAccount{
				ProviderID:    "cp2-expand-secret",
				Name:          "Expansion Secret Check",
				WireAPI:       pi.OpenAICompletions,
				BaseURL:       provider.server.URL + "/v1",
				DefaultModel:  productModel,
				ContextTokens: 262144,
				OutputTokens:  4096,
				Enabled:       true,
			},
			APIKey: expandSecret,
		},
		OperationID: "cp2-save-expand-secret",
	}); err != nil {
		t.Fatalf("SaveProvider(expandSecret): %v", err)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-small", "CP2_DEL_CP2_SMALL_ACT", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	smallAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_SMALL_ACT")
	if settled := proc.waitSettled(t, ctx, smallAct.Executions[len(smallAct.Executions)-1].ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-large", "CP2_DEL_CP2_LARGE_ACT", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	largeAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_LARGE_ACT")
	if settled := proc.waitSettled(t, ctx, largeAct.Executions[len(largeAct.Executions)-1].ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	smallActID = smallAct.ActivityID
	largeActID = largeAct.ActivityID
	mu.Unlock()

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "inspect-p3", "CP2_INSPECT_P3", nil); settled.State != "completed" {
		t.Fatalf("inspect P3 turn settled = %+v", settled)
	}
	mu.Lock()
	gotSmall := smallGetJSON
	gotLarge := largeGetJSON
	mu.Unlock()

	var smallParsed, largeParsed struct {
		Truncated bool `json:"truncated"`
		Results   []struct {
			Truncated    bool   `json:"truncated"`
			OverflowPath string `json:"overflowPath"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(gotSmall), &smallParsed); err != nil {
		t.Fatalf("decode small activity_get %s: %v", gotSmall, err)
	}
	if smallParsed.Truncated || len(smallParsed.Results) != 1 || smallParsed.Results[0].Truncated || smallParsed.Results[0].OverflowPath != "" {
		t.Fatalf("small activity_get must report truncated=false without overflow: %+v raw=%s", smallParsed, gotSmall)
	}
	if err := json.Unmarshal([]byte(gotLarge), &largeParsed); err != nil {
		t.Fatalf("decode large activity_get %s: %v", gotLarge, err)
	}
	if !largeParsed.Truncated || len(largeParsed.Results) != 1 || !largeParsed.Results[0].Truncated || largeParsed.Results[0].OverflowPath != "" {
		t.Fatalf("large activity_get with toolPreset=none must report truncated=true and omit overflowPath: %+v raw=%s", largeParsed, gotLarge)
	}
	if err := filepath.WalkDir(opts.StateDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && strings.Contains(path, string(filepath.Separator)+"overflow"+string(filepath.Separator)) {
			t.Fatalf("toolPreset=none activity_get wrote overflow spill file: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	unreadBefore, err := proc.client.ListActivities(ctx, pi.ProductActivityListParams{
		BotID:              botA.BotID,
		States:             []string{"completed"},
		UnacknowledgedOnly: true,
	})
	if err != nil || len(unreadBefore.Items) != 2 {
		t.Fatalf("unread completed before ack = %+v, %v", unreadBefore.Items, err)
	}
	var smallSummary pi.ProductActivitySummary
	for _, row := range unreadBefore.Items {
		if row.ActivityID == smallAct.ActivityID {
			smallSummary = row
		}
	}
	if _, err := proc.client.AcknowledgeActivity(ctx, pi.ProductActivityAcknowledgeParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: smallAct.ActivityID},
			ExecutionID:           smallAct.Executions[len(smallAct.Executions)-1].ExecutionID,
		},
		ExpectedRevision: smallSummary.Revision,
		OperationID:      "cp2-ack-small",
	}); err != nil {
		t.Fatal(err)
	}
	unreadAfter, err := proc.client.ListActivities(ctx, pi.ProductActivityListParams{
		BotID:              botA.BotID,
		States:             []string{"completed"},
		UnacknowledgedOnly: true,
	})
	if err != nil || len(unreadAfter.Items) != 1 || unreadAfter.Items[0].ActivityID != largeAct.ActivityID {
		t.Fatalf("unread completed after ack = %+v, %v", unreadAfter.Items, err)
	}

	backupPath := filepath.Join(root, "cp2-p3-backup.json")
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatalf("BackupProfile: %v", err)
	}
	backupRaw := productRead(t, backupPath)
	if bytes.Contains(backupRaw, []byte(secret)) || bytes.Contains(backupRaw, []byte(expandSecret)) {
		t.Fatal("backup leaked secret bytes")
	}
	var backupDoc struct {
		Backup struct {
			SkillApprovalRequests []struct {
				RequestID string `json:"requestId"`
				Reason    string `json:"reason"`
			} `json:"skillApprovalRequests"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(backupRaw, &backupDoc); err != nil {
		t.Fatal(err)
	}
	if len(backupDoc.Backup.SkillApprovalRequests) != 1 {
		t.Fatalf("expected 1 skill approval request in backup, got %+v", backupDoc.Backup.SkillApprovalRequests)
	}
	gotReason := backupDoc.Backup.SkillApprovalRequests[0].Reason
	if len(gotReason) == 0 || len(gotReason) > 512 || !utf8.ValidString(gotReason) || !strings.Contains(gotReason, "[credential]") {
		t.Fatalf("backup approval reason must be redacted valid UTF-8 <= 512 bytes: len=%d reason=%q", len(gotReason), gotReason)
	}

	proc.shutdown(t, ctx)
	provider.assertNoProblems()
}

func TestPiControlPlaneCP2TriageAuthorityRacesAndSecurity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	nodePath, workerPath := cp1WorkerPaths(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "cp2-triage-secret-T5kL9mR2"

	var unauthCalls atomic.Int64
	unauthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		unauthCalls.Add(1)
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer unauthSrv.Close()

	type triageFixture struct {
		fn func(req cp2Request) cp2Reply
	}
	var mu sync.Mutex
	var botBID string
	var triageQueue []triageFixture
	var fgToolsSeen, actToolsSeen []string
	var lastAutoAskResult, lastParallelResults, lastFwdResult string
	var forwardCallArgs string
	var budgetAskCount int
	var seq atomic.Int64
	nextCall := func(prefix string) string {
		return fmt.Sprintf("%s_%d", prefix, seq.Add(1))
	}

	enqueueTriage := func(fn func(req cp2Request) cp2Reply) {
		mu.Lock()
		triageQueue = append(triageQueue, triageFixture{fn: fn})
		mu.Unlock()
	}

	provider := newCP2Provider(t, secret, func(req cp2Request) cp2Reply {
		if req.IsTriage {
			if len(req.Tools) != 0 {
				t.Errorf("triage request must carry zero tools, got %v", req.Tools)
			}
			userMsgs := 0
			for _, m := range req.Messages {
				if m.Role == "user" {
					userMsgs++
				}
			}
			if userMsgs != 1 || req.BoardText != "" || req.MaxTokens == 0 || req.MaxTokens > 512 {
				t.Errorf("triage request violated contract: userMsgs=%d board=%q maxTokens=%d", userMsgs, req.BoardText, req.MaxTokens)
			}
			mu.Lock()
			if len(triageQueue) == 0 {
				mu.Unlock()
				t.Errorf("unexpected triage call")
				return cp2Reply{HTTPStatus: 500}
			}
			fx := triageQueue[0]
			triageQueue = triageQueue[1:]
			mu.Unlock()
			return fx.fn(req)
		}

		mu.Lock()
		targetB := botBID
		fwdArgs := forwardCallArgs
		mu.Unlock()

		if req.IsActivity {
			mu.Lock()
			if len(actToolsSeen) == 0 {
				actToolsSeen = append([]string(nil), req.Tools...)
			}
			mu.Unlock()
			switch {
			case strings.Contains(req.BaseUserText, "CP2_EXPLICIT_RULE"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(nil)}}}
				}
				return cp2Reply{Text: "Explicit rule did not auto-answer."}
			case strings.Contains(req.BaseUserText, "CP2_TIER1_AUTO"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(nil)}}}
				}
				mu.Lock()
				lastAutoAskResult = req.LastContent
				mu.Unlock()
				return cp2Reply{Text: "Tier1 auto delivered."}
			case strings.Contains(req.BaseUserText, "CP2_CONFLICT_RULES"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(nil)}}}
				}
				return cp2Reply{Text: "Conflict opened to human."}
			case strings.Contains(req.BaseUserText, "CP2_TIER2_NOMINATED"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(nil)}}}
				}
				mu.Lock()
				lastAutoAskResult = req.LastContent
				mu.Unlock()
				return cp2Reply{Text: "Tier2 nominated delivered."}
			case strings.Contains(req.BaseUserText, "CP2_HUMAN_RACE"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(nil)}}}
				}
				mu.Lock()
				lastAutoAskResult = req.LastContent
				mu.Unlock()
				return cp2Reply{Text: "Human beat triage."}
			case strings.Contains(req.BaseUserText, "CP2_RULE_RACE"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(nil)}}}
				}
				mu.Lock()
				lastAutoAskResult = req.LastContent
				mu.Unlock()
				return cp2Reply{Text: "Rule update invalidated triage."}
			case strings.Contains(req.BaseUserText, "CP2_PARALLEL_ASKS"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{
						{ID: "par-triage", Name: "activity_ask", Args: cp2FormattingAskArgs(nil)},
						{ID: "par-human", Name: "activity_ask", Args: cp2FormattingAskArgs(map[string]any{"requires_user_decision": true})},
					}}
				}
				mu.Lock()
				raw, _ := json.Marshal(req.ToolResults)
				lastParallelResults = string(raw)
				mu.Unlock()
				return cp2Reply{Text: "Parallel asks done."}
			case strings.Contains(req.BaseUserText, "CP2_BUDGET_FOUR"):
				mu.Lock()
				budgetAskCount++
				n := budgetAskCount
				mu.Unlock()
				if n <= 4 {
					return cp2Reply{Calls: []cp2ToolCall{{ID: fmt.Sprintf("budget-ask-%d", n), Name: "activity_ask", Args: cp2FormattingAskArgs(nil)}}}
				}
				return cp2Reply{Text: "All 4 budget asks handled."}
			case strings.Contains(req.BaseUserText, "CP2_REDIRECT_BLOCK"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(nil)}}}
				}
				return cp2Reply{Text: "Redirect blocked."}
			case strings.Contains(req.BaseUserText, "CP2_SENSITIVE_NONDOMAIN"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(map[string]any{
						"text": "Which private key should I transmit?",
					})}}}
				}
				return cp2Reply{Text: "Sensitive handled."}
			case strings.Contains(req.BaseUserText, "CP2_FREETEXT_ASK"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(map[string]any{
						"allow_free_text":        true,
						"requires_user_decision": true,
					})}}}
				}
				mu.Lock()
				lastAutoAskResult = req.LastContent
				mu.Unlock()
				return cp2Reply{Text: "Free text accepted."}
			case strings.Contains(req.BaseUserText, "CP2_FWD_TARGET"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(map[string]any{
						"requires_user_decision": true,
					})}}}
				}
				mu.Lock()
				lastAutoAskResult = req.LastContent
				mu.Unlock()
				return cp2Reply{Text: "Forwarded answer received."}
			case strings.Contains(req.BaseUserText, "CP2_FWD_SECOND"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(map[string]any{
						"requires_user_decision": true,
					})}}}
				}
				return cp2Reply{Text: "Second open answered."}
			default:
				return cp2Reply{Text: "Activity OK."}
			}
		}

		mu.Lock()
		if len(fgToolsSeen) == 0 {
			fgToolsSeen = append([]string(nil), req.Tools...)
		}
		mu.Unlock()

		switch {
		case strings.HasPrefix(req.BaseUserText, "CP2_SAVE_EXPLICIT"):
			if req.LastRole == "user" {
				return cp2Reply{Calls: []cp2ToolCall{{
					ID:   nextCall("mem"),
					Name: "memory_save_rule",
					Args: `{"text":"formatting: Markdown"}`,
				}}}
			}
			return cp2Reply{Text: "Saved explicit rule."}
		case strings.HasPrefix(req.BaseUserText, "CP2_PROPOSE_RULE"):
			if req.LastRole == "user" {
				return cp2Reply{Calls: []cp2ToolCall{{
					ID:   nextCall("prop"),
					Name: "memory_propose_learning",
					Args: `{"text":"formatting: Markdown","reason":"User prefers Markdown output."}`,
				}}}
			}
			return cp2Reply{Text: "Proposed learning."}
		case strings.HasPrefix(req.BaseUserText, "CP2_DEL_"):
			if req.LastRole == "user" {
				goal := strings.TrimPrefix(req.BaseUserText, "CP2_DEL_")
				args, _ := json.Marshal(map[string]any{
					"key":         "k_" + goal,
					"goal":        goal,
					"assignee_id": targetB,
				})
				return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("del"), Name: "activity_delegate", Args: string(args)}}}
			}
			return cp2Reply{Text: "Delegated."}
		case strings.HasPrefix(req.BaseUserText, "CP2_CALL_FWD") || strings.HasPrefix(req.BaseUserText, "Answer"):
			if req.LastRole == "user" {
				return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("fwd"), Name: "activity_answer", Args: fwdArgs}}}
			}
			mu.Lock()
			lastFwdResult = req.LastContent
			mu.Unlock()
			return cp2Reply{Text: "Forward tool called."}
		default:
			return cp2Reply{Text: "OK."}
		}
	})
	provider.addForbiddenSecret(secret)

	opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(root, "pi"),
		ProfilePath:     filepath.Join(root, "contacts.json"),
		CredentialsPath: filepath.Join(root, "connection.json"),
		FreshProfile:    true,
	}
	dbPath := filepath.Join(root, "nagi.db")
	proc := startCP1Process(t, opts)
	_ = proc.open(t, ctx)
	cp2ConfigureConnection(t, ctx, proc.client, pi.OpenAICompletions, provider.server.URL+"/v1", secret, ws, 2)

	botA, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp2-tr-a",
		Profile: pi.ProductBotProfile{
			Name: "OriginA", Role: "Coordinator", Purpose: "Triage test",
			Instructions: "Coordinate.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	botB, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp2-tr-b",
		Profile: pi.ProductBotProfile{
			Name: "AssigneeB", Role: "Worker", Purpose: "Triage assignee",
			Instructions: "Work cleanly.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	botC, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp2-tr-c",
		Profile: pi.ProductBotProfile{
			Name: "ForeignC", Role: "Outsider", Purpose: "ACL test",
			Instructions: "Isolated.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = botB.BotID
	mu.Unlock()

	if settled := cp1SendTurn(t, ctx, proc, botB.CurrentConversationID, "save-exp", "CP2_SAVE_EXPLICIT", nil); settled.State != "completed" {
		t.Fatalf("save explicit rule turn = %+v", settled)
	}
	memB, err := proc.client.GetBotMemory(ctx, botB.BotID)
	if err != nil || len(memB.Rules) != 1 || memB.Rules[0].Source != "explicit" {
		t.Fatalf("expected 1 explicit rule on botB: %+v, %v", memB.Rules, err)
	}
	explicitRuleID := memB.Rules[0].RuleID

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-exp", "CP2_DEL_CP2_EXPLICIT_RULE", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	expAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_EXPLICIT_RULE")
	expQ := cp2WaitQuestion(t, ctx, proc, expAct.ActivityID, "open")
	if provider.triageCall.Load() != 0 {
		t.Fatalf("explicit memory_save_rule must not trigger triage, calls=%d", provider.triageCall.Load())
	}
	if _, _, found := cp2TriageAccounting(t, nodePath, dbPath, expAct.ActivityID); found {
		t.Fatal("explicit rule question should not create triage accounting row")
	}
	if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: expAct.ActivityID},
			ExecutionID:           expQ.ExecutionID,
		},
		QuestionID:       expQ.QuestionID,
		QuestionRevision: expQ.Revision,
		SelectedOptionID: "markdown",
		OperationID:      "cp2-ans-exp",
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, expQ.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}

	mu.Lock()
	gotFGTools := append([]string(nil), fgToolsSeen...)
	gotActTools := append([]string(nil), actToolsSeen...)
	mu.Unlock()
	if !slices.Contains(gotFGTools, "activity_answer") {
		t.Fatalf("foreground tools missing activity_answer: %v", gotFGTools)
	}
	if slices.Contains(gotActTools, "activity_answer") {
		t.Fatalf("activity tools must not expose activity_answer: %v", gotActTools)
	}

	if _, err := proc.client.DeleteMemoryRule(ctx, pi.ProductMemoryDeleteRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: botB.BotID, OperationID: "cp2-del-exp-rule"},
		RuleID:                 explicitRuleID,
	}); err != nil {
		t.Fatal(err)
	}
	if settled := cp1SendTurn(t, ctx, proc, botB.CurrentConversationID, "prop-rule", "CP2_PROPOSE_RULE", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	memB, err = proc.client.GetBotMemory(ctx, botB.BotID)
	if err != nil || len(memB.PendingLearnings) != 1 {
		t.Fatalf("expected 1 pending learning: %+v, %v", memB.PendingLearnings, err)
	}
	if _, err := proc.client.ReviewLearning(ctx, pi.ProductMemoryReviewLearningParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: botB.BotID, OperationID: "cp2-approve-learning"},
		LearningID:             memB.PendingLearnings[0].LearningID,
		Decision:               "approve",
	}); err != nil {
		t.Fatal(err)
	}
	memB, err = proc.client.GetBotMemory(ctx, botB.BotID)
	if err != nil || len(memB.Rules) != 1 || memB.Rules[0].Source != "approved" {
		t.Fatalf("expected 1 approved rule: %+v, %v", memB.Rules, err)
	}
	approvedRule := memB.Rules[0]
	approvedRev := memB.RulesRevision

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-t1", "CP2_DEL_CP2_TIER1_AUTO", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	t1Act := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_TIER1_AUTO")
	if settled := proc.waitSettled(t, ctx, t1Act.Executions[len(t1Act.Executions)-1].ExecutionID); settled.State != "completed" {
		t.Fatalf("tier1 auto activity settled = %+v", settled)
	}
	if provider.triageCall.Load() != 0 {
		t.Fatalf("deterministic Tier-1 match must use 0 triage LLM calls, got %d", provider.triageCall.Load())
	}
	t1Done, err := proc.client.GetActivity(ctx, t1Act.ActivityID)
	if err != nil || len(t1Done.Questions) != 1 {
		t.Fatalf("tier1 done = %+v, %v", t1Done, err)
	}
	t1Q := t1Done.Questions[0]
	if t1Q.Provenance != "bot_auto" || t1Q.EvidenceID != approvedRule.RuleID || t1Q.EvidenceRevision == nil ||
		*t1Q.EvidenceRevision != approvedRev || t1Q.SelectedOptionID != "markdown" || t1Q.Delivery != "delivered" ||
		t1Q.Answer == nil || t1Q.Answer.Text != "Markdown" {
		t.Fatalf("tier1 durable question mismatch: %+v", t1Q)
	}
	mu.Lock()
	t1ToolRaw := lastAutoAskResult
	mu.Unlock()
	var t1Tool struct {
		Text             string `json:"text"`
		Provenance       string `json:"provenance"`
		EvidenceID       string `json:"evidenceId"`
		EvidenceRevision uint64 `json:"evidenceRevision"`
		SelectedOptionID string `json:"selectedOptionId"`
	}
	if err := json.Unmarshal([]byte(t1ToolRaw), &t1Tool); err != nil {
		t.Fatalf("decode tier1 tool result %s: %v", t1ToolRaw, err)
	}
	if t1Tool.Text != "Markdown" || t1Tool.Provenance != "bot_auto" || t1Tool.EvidenceID != approvedRule.RuleID ||
		t1Tool.EvidenceRevision != approvedRev || t1Tool.SelectedOptionID != "markdown" {
		t.Fatalf("tier1 tool result mismatch: %+v", t1Tool)
	}
	autoDigest := cp2WaitDigest(t, ctx, proc, func(d pi.ProductActivityDigest) bool {
		return d.ActivityID == t1Act.ActivityID && d.Kind == "question_auto_saved" && d.QuestionID == t1Q.QuestionID
	})
	if autoDigest.FactID == "" {
		t.Fatalf("question_auto_saved digest invalid: %+v", autoDigest)
	}
	if att, auto, ok := cp2TriageAccounting(t, nodePath, dbPath, t1Act.ActivityID); !ok || att != 1 || auto != 1 {
		t.Fatalf("tier1 accounting = (%d,%d,%v), want (1,1,true)", att, auto, ok)
	}

	if _, err := proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: botB.BotID, OperationID: "cp2-add-conflict"},
		Text:                   "Prefer formatting: Plain text",
	}); err != nil {
		t.Fatal(err)
	}
	memB, _ = proc.client.GetBotMemory(ctx, botB.BotID)
	var conflictRuleID string
	for _, r := range memB.Rules {
		if r.RuleID != approvedRule.RuleID {
			conflictRuleID = r.RuleID
		}
	}
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-conflict", "CP2_DEL_CP2_CONFLICT_RULES", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	confAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_CONFLICT_RULES")
	confQ := cp2WaitQuestion(t, ctx, proc, confAct.ActivityID, "open")
	if provider.triageCall.Load() != 0 {
		t.Fatalf("conflicting rules must fail closed with 0 triage calls, got %d", provider.triageCall.Load())
	}
	if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: confAct.ActivityID},
			ExecutionID:           confQ.ExecutionID,
		},
		QuestionID:       confQ.QuestionID,
		QuestionRevision: confQ.Revision,
		SelectedOptionID: "plain_text",
		OperationID:      "cp2-ans-conflict",
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, confQ.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}
	if _, err := proc.client.DeleteMemoryRule(ctx, pi.ProductMemoryDeleteRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: botB.BotID, OperationID: "cp2-del-conflict"},
		RuleID:                 conflictRuleID,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: botB.BotID, OperationID: "cp2-add-match2"},
		Text:                   "Prefer formatting: Markdown",
	}); err != nil {
		t.Fatal(err)
	}

	enqueueTriage(func(req cp2Request) cp2Reply {
		var payload struct {
			Candidates []struct {
				EvidenceID string `json:"evidenceId"`
				OptionID   string `json:"optionId"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(req.BaseUserText), &payload); err != nil || len(payload.Candidates) != 2 {
			t.Errorf("unexpected tier2 nomination payload: %s", req.BaseUserText)
			return cp2Reply{HTTPStatus: 400}
		}
		out, _ := json.Marshal(map[string]string{
			"evidenceId": payload.Candidates[0].EvidenceID,
			"optionId":   "markdown",
		})
		return cp2Reply{Text: string(out)}
	})
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-t2", "CP2_DEL_CP2_TIER2_NOMINATED", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	t2Act := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_TIER2_NOMINATED")
	if settled := proc.waitSettled(t, ctx, t2Act.Executions[len(t2Act.Executions)-1].ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}
	if provider.triageCall.Load() != 1 {
		t.Fatalf("expected 1 triage call for Tier-2 nomination, got %d", provider.triageCall.Load())
	}
	t2Done, err := proc.client.GetActivity(ctx, t2Act.ActivityID)
	if err != nil || len(t2Done.Questions) != 1 || t2Done.Questions[0].Provenance != "bot_auto" || t2Done.Questions[0].Delivery != "delivered" {
		t.Fatalf("tier2 done mismatch: %+v, %v", t2Done.Questions, err)
	}

	humanEntered := make(chan struct{})
	humanCancelled := make(chan struct{})
	enqueueTriage(func(req cp2Request) cp2Reply {
		close(humanEntered)
		select {
		case <-req.Ctx.Done():
			close(humanCancelled)
			return cp2Reply{}
		case <-time.After(15 * time.Second):
			return cp2Reply{Text: `{"decision":"escalate_user"}`}
		}
	})
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-hrace", "CP2_DEL_CP2_HUMAN_RACE", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	hrAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_HUMAN_RACE")
	select {
	case <-humanEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for human race triage entry")
	}
	hrPending := cp2WaitQuestion(t, ctx, proc, hrAct.ActivityID, "triage_pending")
	if att, auto, ok := cp2TriageAccounting(t, nodePath, dbPath, hrAct.ActivityID); !ok || att != 1 || auto != 0 {
		t.Fatalf("in-flight triage accounting = (%d,%d,%v), want (1,0,true)", att, auto, ok)
	}
	ansRace, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: hrAct.ActivityID},
			ExecutionID:           hrPending.ExecutionID,
		},
		ProductText:      pi.ProductText{Text: "ignored free text"},
		QuestionID:       hrPending.QuestionID,
		QuestionRevision: hrPending.Revision,
		SelectedOptionID: "plain_text",
		OperationID:      "cp2-human-beat-triage",
	})
	if err != nil || ansRace.Delivery != "saved" || ansRace.Provenance != "user" {
		t.Fatalf("human answer during triage_pending = %+v, %v", ansRace, err)
	}
	select {
	case <-humanCancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("human answer did not abort in-flight triage HTTP request")
	}
	if settled := proc.waitSettled(t, ctx, hrPending.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}
	hrDone, err := proc.client.GetActivity(ctx, hrAct.ActivityID)
	if err != nil || hrDone.Questions[0].Provenance != "user" || hrDone.Questions[0].Answer == nil || hrDone.Questions[0].Answer.Text != "Plain text" {
		t.Fatalf("human race final question = %+v, %v", hrDone.Questions, err)
	}
	if att, auto, ok := cp2TriageAccounting(t, nodePath, dbPath, hrAct.ActivityID); !ok || att != 1 || auto != 0 {
		t.Fatalf("human race final accounting = (%d,%d,%v), want (1,0,true)", att, auto, ok)
	}

	ruleEntered := make(chan string, 1)
	ruleRelease := make(chan struct{})
	enqueueTriage(func(req cp2Request) cp2Reply {
		var payload struct {
			Candidates []struct {
				EvidenceID string `json:"evidenceId"`
			} `json:"candidates"`
		}
		_ = json.Unmarshal([]byte(req.BaseUserText), &payload)
		ruleEntered <- payload.Candidates[0].EvidenceID
		select {
		case <-ruleRelease:
		case <-req.Ctx.Done():
			return cp2Reply{}
		}
		out, _ := json.Marshal(map[string]string{"evidenceId": payload.Candidates[0].EvidenceID})
		return cp2Reply{Text: string(out)}
	})
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-rrace", "CP2_DEL_CP2_RULE_RACE", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	rrAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_RULE_RACE")
	select {
	case <-ruleEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for rule race triage entry")
	}
	rrPending := cp2WaitQuestion(t, ctx, proc, rrAct.ActivityID, "triage_pending")
	if _, err := proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: botB.BotID, OperationID: "cp2-bump-rev-race"},
		Text:                   "Always include verification dates in release audits.",
	}); err != nil {
		t.Fatal(err)
	}
	close(ruleRelease)
	rrOpened := cp2WaitQuestion(t, ctx, proc, rrAct.ActivityID, "open")
	if rrOpened.Revision <= rrPending.Revision {
		t.Fatalf("invalidated triage did not bump revision: pending=%d opened=%d", rrPending.Revision, rrOpened.Revision)
	}
	_, staleErr := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: rrAct.ActivityID},
			ExecutionID:           rrPending.ExecutionID,
		},
		QuestionID:       rrPending.QuestionID,
		QuestionRevision: rrPending.Revision,
		SelectedOptionID: "markdown",
		OperationID:      "cp2-stale-rev-ans",
	})
	var remoteErr *pi.RemoteError
	if !errors.As(staleErr, &remoteErr) || remoteErr.Code != "stale_revision" {
		t.Fatalf("stale revision answer = %v, want stale_revision", staleErr)
	}
	if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: rrAct.ActivityID},
			ExecutionID:           rrOpened.ExecutionID,
		},
		QuestionID:       rrOpened.QuestionID,
		QuestionRevision: rrOpened.Revision,
		SelectedOptionID: "markdown",
		OperationID:      "cp2-valid-rev-ans",
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, rrOpened.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}

	parEntered := make(chan string, 1)
	parRelease := make(chan struct{})
	enqueueTriage(func(req cp2Request) cp2Reply {
		var payload struct {
			Candidates []struct {
				EvidenceID string `json:"evidenceId"`
			} `json:"candidates"`
		}
		_ = json.Unmarshal([]byte(req.BaseUserText), &payload)
		parEntered <- payload.Candidates[0].EvidenceID
		select {
		case <-parRelease:
		case <-req.Ctx.Done():
			return cp2Reply{}
		}
		out, _ := json.Marshal(map[string]string{"evidenceId": payload.Candidates[0].EvidenceID, "optionId": "markdown"})
		return cp2Reply{Text: string(out)}
	})
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-par", "CP2_DEL_CP2_PARALLEL_ASKS", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	parAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_PARALLEL_ASKS")
	select {
	case <-parEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for parallel triage entry")
	}
	parOpen := cp2WaitQuestion(t, ctx, proc, parAct.ActivityID, "open")
	parPend := cp2WaitQuestion(t, ctx, proc, parAct.ActivityID, "triage_pending")
	if parOpen.QuestionID == parPend.QuestionID {
		t.Fatalf("parallel asks collided on questionId %s", parOpen.QuestionID)
	}
	if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: parAct.ActivityID},
			ExecutionID:           parOpen.ExecutionID,
		},
		QuestionID:       parOpen.QuestionID,
		QuestionRevision: parOpen.Revision,
		SelectedOptionID: "plain_text",
		OperationID:      "cp2-ans-par-human",
	}); err != nil {
		t.Fatal(err)
	}
	close(parRelease)
	if settled := proc.waitSettled(t, ctx, parOpen.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	parRes := lastParallelResults
	mu.Unlock()
	var parMap map[string]string
	if err := json.Unmarshal([]byte(parRes), &parMap); err != nil {
		t.Fatalf("decode parallel results %s: %v", parRes, err)
	}
	if !strings.Contains(parMap["par-triage"], `"provenance":"bot_auto"`) || !strings.Contains(parMap["par-human"], `"provenance":"user"`) {
		t.Fatalf("parallel asks tool results missing both provenances: %+v", parMap)
	}

	for i := 0; i < 3; i++ {
		enqueueTriage(func(_ cp2Request) cp2Reply {
			return cp2Reply{HTTPStatus: 400, RawError: "forced triage failure"}
		})
	}
	triageBeforeBudget := provider.triageCall.Load()
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-budget", "CP2_DEL_CP2_BUDGET_FOUR", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	budAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_BUDGET_FOUR")
	seenBud := map[string]bool{}
	for i := 1; i <= 4; i++ {
		q := cp2WaitNewQuestion(t, ctx, proc, budAct.ActivityID, "open", seenBud)
		seenBud[q.QuestionID] = true
		wantAtt := i
		if wantAtt > 3 {
			wantAtt = 3
		}
		if att, auto, ok := cp2TriageAccounting(t, nodePath, dbPath, budAct.ActivityID); !ok || att != wantAtt || auto != 0 {
			t.Fatalf("budget step %d accounting = (%d,%d,%v), want (%d,0,true)", i, att, auto, ok, wantAtt)
		}
		if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
			ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
				ProductActivityParams: pi.ProductActivityParams{ActivityID: budAct.ActivityID},
				ExecutionID:           q.ExecutionID,
			},
			QuestionID:       q.QuestionID,
			QuestionRevision: q.Revision,
			SelectedOptionID: "markdown",
			OperationID:      fmt.Sprintf("cp2-budget-ans-%d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if settled := proc.waitSettled(t, ctx, budAct.Executions[len(budAct.Executions)-1].ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}
	if delta := provider.triageCall.Load() - triageBeforeBudget; delta != 3 {
		t.Fatalf("expected exactly 3 triage calls before 4th bypass, got %d", delta)
	}

	enqueueTriage(func(_ cp2Request) cp2Reply {
		return cp2Reply{HTTPStatus: http.StatusTemporaryRedirect, RedirectURL: unauthSrv.URL + "/redirected"}
	})
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-redir", "CP2_DEL_CP2_REDIRECT_BLOCK", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	redirAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_REDIRECT_BLOCK")
	redirQ := cp2WaitQuestion(t, ctx, proc, redirAct.ActivityID, "open")
	if unauthCalls.Load() != 0 {
		t.Fatalf("triage followed HTTP 307 redirect to unauthorized host: calls=%d", unauthCalls.Load())
	}
	if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: redirAct.ActivityID},
			ExecutionID:           redirQ.ExecutionID,
		},
		QuestionID:       redirQ.QuestionID,
		QuestionRevision: redirQ.Revision,
		SelectedOptionID: "plain_text",
		OperationID:      "cp2-ans-redir",
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, redirQ.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}

	triageBeforeSens := provider.triageCall.Load()
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-sens", "CP2_DEL_CP2_SENSITIVE_NONDOMAIN", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	sensAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_SENSITIVE_NONDOMAIN")
	sensQ := cp2WaitQuestion(t, ctx, proc, sensAct.ActivityID, "open")
	if provider.triageCall.Load() != triageBeforeSens {
		t.Fatal("non-domain sensitive question invoked triage")
	}
	if _, _, found := cp2TriageAccounting(t, nodePath, dbPath, sensAct.ActivityID); found {
		t.Fatal("sensitive non-domain question charged triage accounting")
	}
	_, badOptErr := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: sensAct.ActivityID},
			ExecutionID:           sensQ.ExecutionID,
		},
		ProductText:      pi.ProductText{Text: "free"},
		QuestionID:       sensQ.QuestionID,
		QuestionRevision: sensQ.Revision,
		SelectedOptionID: "nonexistent_option",
		OperationID:      "cp2-bad-opt",
	})
	if !errors.As(badOptErr, &remoteErr) || remoteErr.Code != "invalid_params" {
		t.Fatalf("unknown selectedOptionId = %v, want invalid_params", badOptErr)
	}
	if _, err := proc.client.StopActivity(ctx, pi.ProductActivityStopParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: sensAct.ActivityID},
			ExecutionID:           sensQ.ExecutionID,
		},
		OperationID: "cp2-stop-sens",
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, sensQ.ExecutionID); settled.State != "interrupted" {
		t.Fatal(settled)
	}
	sensDone, err := proc.client.GetActivity(ctx, sensAct.ActivityID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.CloseActivity(ctx, pi.ProductActivityCloseParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: sensDone.ActivityID},
		ExpectedRevision:      sensDone.Revision,
		OperationID:           "cp2-close-sens",
	}); err != nil {
		t.Fatal(err)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-free", "CP2_DEL_CP2_FREETEXT_ASK", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	freeAct := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_FREETEXT_ASK")
	freeQ := cp2WaitQuestion(t, ctx, proc, freeAct.ActivityID, "open")
	if _, err := proc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: freeAct.ActivityID},
			ExecutionID:           freeQ.ExecutionID,
		},
		ProductText:      pi.ProductText{Text: "Custom YAML format"},
		QuestionID:       freeQ.QuestionID,
		QuestionRevision: freeQ.Revision,
		OperationID:      "cp2-ans-free",
	}); err != nil {
		t.Fatal(err)
	}
	if settled := proc.waitSettled(t, ctx, freeQ.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	freeRes := lastAutoAskResult
	mu.Unlock()
	if !strings.Contains(freeRes, `"text":"Custom YAML format"`) || !strings.Contains(freeRes, `"provenance":"user"`) {
		t.Fatalf("free-text answer mismatch: %s", freeRes)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-fwd1", "CP2_DEL_CP2_FWD_TARGET", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	fwdAct1 := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_FWD_TARGET")
	fwdQ1 := cp2WaitQuestion(t, ctx, proc, fwdAct1.ActivityID, "open")

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "del-fwd2", "CP2_DEL_CP2_FWD_SECOND", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	fwdAct2 := cp2FindActivityByGoal(t, ctx, proc, botA.BotID, "CP2_FWD_SECOND")
	fwdQ2 := cp2WaitQuestion(t, ctx, proc, fwdAct2.ActivityID, "open")

	setFwd := func(actID string, q pi.ProductQuestion, rev uint64, text, opt string) {
		m := map[string]any{
			"activity_id":       actID,
			"execution_id":      q.ExecutionID,
			"question_id":       q.QuestionID,
			"question_revision": rev,
			"text":              text,
		}
		if opt != "" {
			m["selected_option_id"] = opt
		}
		raw, _ := json.Marshal(m)
		mu.Lock()
		forwardCallArgs = string(raw)
		mu.Unlock()
	}

	setFwd(fwdAct1.ActivityID, fwdQ1, fwdQ1.Revision, "Plain text", "plain_text")
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "fwd-unrel", "CP2_CALL_FWD_UNRELATED", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	gotUnrel := lastFwdResult
	mu.Unlock()
	if !strings.Contains(gotUnrel, `"state":"confirmation_required"`) {
		t.Fatalf("unrelated turn activity_answer must require confirmation, got %s", gotUnrel)
	}
	if checkQ := cp2WaitQuestion(t, ctx, proc, fwdAct1.ActivityID, "open"); checkQ.Revision != fwdQ1.Revision {
		t.Fatalf("unrelated confirmation_required mutated question revision: %d vs %d", checkQ.Revision, fwdQ1.Revision)
	}

	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "fwd-ambig", "Answer: option:plain_text", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	gotAmbig := lastFwdResult
	mu.Unlock()
	if !strings.Contains(gotAmbig, `"state":"confirmation_required"`) {
		t.Fatalf("ambiguous shorthand with 2 open questions must require confirmation, got %s", gotAmbig)
	}

	if settled := cp1SendTurn(t, ctx, proc, botC.CurrentConversationID, "fwd-foreign", fmt.Sprintf("Answer %s: Plain text", fwdQ1.QuestionID), nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	gotForeign := lastFwdResult
	mu.Unlock()
	if !strings.Contains(gotForeign, `"code":"scope_denied"`) {
		t.Fatalf("foreign bot activity_answer must fail with scope_denied, got %s", gotForeign)
	}

	setFwd(fwdAct1.ActivityID, fwdQ1, fwdQ1.Revision+99, "Plain text", "plain_text")
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "fwd-stale", fmt.Sprintf("Answer %s: Plain text", fwdQ1.QuestionID), nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	gotStale := lastFwdResult
	mu.Unlock()
	if !strings.Contains(gotStale, `"code":"stale_revision"`) {
		t.Fatalf("stale revision activity_answer must fail with stale_revision, got %s", gotStale)
	}

	setFwd(fwdAct1.ActivityID, fwdQ1, fwdQ1.Revision, "Plain text", "plain_text")
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "fwd-exact", fmt.Sprintf("Answer %s: Plain text", fwdQ1.QuestionID), nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	gotExact := lastFwdResult
	mu.Unlock()
	if !strings.Contains(gotExact, `"provenance":"user"`) || !strings.Contains(gotExact, `"answerSaved":true`) {
		t.Fatalf("exact questionId evidence failed to commit: %s", gotExact)
	}
	if settled := proc.waitSettled(t, ctx, fwdQ1.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}

	setFwd(fwdAct2.ActivityID, fwdQ2, fwdQ2.Revision, "red herring text", "plain_text")
	if settled := cp1SendTurn(t, ctx, proc, botA.CurrentConversationID, "fwd-single", "Answer: option:plain_text", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	mu.Lock()
	gotSingle := lastFwdResult
	mu.Unlock()
	if !strings.Contains(gotSingle, `"provenance":"user"`) || !strings.Contains(gotSingle, `"selectedOptionId":"plain_text"`) {
		t.Fatalf("single-open option evidence failed to commit: %s", gotSingle)
	}
	if settled := proc.waitSettled(t, ctx, fwdQ2.ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}
	fwd2Done, err := proc.client.GetActivity(ctx, fwdAct2.ActivityID)
	if err != nil || fwd2Done.Questions[0].Answer == nil || fwd2Done.Questions[0].Answer.Text != "Plain text" {
		t.Fatalf("option-only foreground forward did not derive canonical label: %+v, %v", fwd2Done.Questions, err)
	}

	proc.shutdown(t, ctx)

	revRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	revWS := filepath.Join(revRoot, "ws")
	if err := os.MkdirAll(revWS, 0o700); err != nil {
		t.Fatal(err)
	}
	revEntered := make(chan struct{})
	revAborted := make(chan struct{})
	enqueueTriage(func(req cp2Request) cp2Reply {
		close(revEntered)
		select {
		case <-req.Ctx.Done():
			close(revAborted)
			return cp2Reply{}
		case <-time.After(15 * time.Second):
			return cp2Reply{Text: `{"decision":"escalate_user"}`}
		}
	})
	revOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(revRoot, "pi"),
		ProfilePath:     filepath.Join(revRoot, "contacts.json"),
		CredentialsPath: filepath.Join(revRoot, "connection.json"),
		FreshProfile:    true,
	}
	revProc := startCP1Process(t, revOpts)
	_ = revProc.open(t, ctx)
	savedProv, err := revProc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		ProductProviderTestParams: pi.ProductProviderTestParams{
			Account: pi.ProductProviderAccount{
				ProviderID:    "cp2-local-resp",
				Name:          "Local Responses",
				WireAPI:       pi.OpenAIResponses,
				BaseURL:       provider.server.URL + "/v1",
				DefaultModel:  productModel,
				ContextTokens: 262144,
				OutputTokens:  4096,
				Enabled:       true,
			},
			APIKey: secret,
		},
		OperationID: "cp2-rev-save-prov",
	})
	if err != nil || savedProv.SavedRevision == nil {
		t.Fatalf("SaveProvider: %+v, %v", savedProv, err)
	}
	if _, err := revProc.client.ApplyProviders(ctx, pi.ProductConnectionApplyParams{
		SavedRevision: *savedProv.SavedRevision,
		OperationID:   "cp2-rev-apply-prov",
	}); err != nil {
		t.Fatal(err)
	}
	revA, err := revProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp2-rev-a",
		Profile: pi.ProductBotProfile{
			Name: "RevOrigin", Role: "Coordinator", Purpose: "Revocation test",
			Instructions: "Coordinate.", Workspace: revWS, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	revB, err := revProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp2-rev-b",
		Profile: pi.ProductBotProfile{
			Name: "RevWorker", Role: "Worker", Purpose: "Revocation assignee",
			Instructions: "Work.", Workspace: revWS, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = revB.BotID
	mu.Unlock()
	if _, err := revProc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: revB.BotID, OperationID: "cp2-rev-r1"},
		Text:                   "formatting: Markdown",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := revProc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: revB.BotID, OperationID: "cp2-rev-r2"},
		Text:                   "Prefer formatting: Markdown",
	}); err != nil {
		t.Fatal(err)
	}

	actCallsBeforeRev := provider.actCalls.Load()
	if settled := cp1SendTurn(t, ctx, revProc, revA.CurrentConversationID, "del-rev", "CP2_DEL_CP2_TIER2_NOMINATED", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	revAct := cp2FindActivityByGoal(t, ctx, revProc, revA.BotID, "CP2_TIER2_NOMINATED")
	select {
	case <-revEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for responses triage entry")
	}
	if _, err := revProc.client.RevokeProviderCredential(ctx, pi.ProductProviderTargetParams{
		ProviderID:  "cp2-local-resp",
		OperationID: "cp2-rev-revoke",
	}); err != nil {
		t.Fatalf("RevokeProviderCredential: %v", err)
	}
	select {
	case <-revAborted:
	case <-time.After(10 * time.Second):
		t.Fatal("revoking credential did not abort in-flight responses triage request")
	}
	revQ := cp2WaitQuestion(t, ctx, revProc, revAct.ActivityID, "open")
	revAns, err := revProc.client.AnswerQuestion(ctx, pi.ProductAnswerParams{
		ProductActivityExecutionParams: pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: revAct.ActivityID},
			ExecutionID:           revQ.ExecutionID,
		},
		QuestionID:       revQ.QuestionID,
		QuestionRevision: revQ.Revision,
		SelectedOptionID: "markdown",
		OperationID:      "cp2-rev-ans",
	})
	if err != nil || revAns.Delivery != "saved" || revAns.DeliveryBlocker != pi.ProductCredentialUnavailable {
		t.Fatalf("answer after credential revocation = %+v, %v", revAns, err)
	}
	if settled := revProc.waitSettled(t, ctx, revQ.ExecutionID); settled.State != "interrupted" {
		t.Fatalf("revoked activity settled = %+v, want interrupted", settled)
	}
	if provider.actCalls.Load() != actCallsBeforeRev+1 {
		t.Fatalf("revoked activity made extra provider calls: got %d want %d", provider.actCalls.Load(), actCallsBeforeRev+1)
	}
	revProc.shutdown(t, ctx)
	provider.assertNoProblems()
}

func TestPiControlPlaneCP2RecoveryMigrationAndPortableRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	nodePath, workerPath := cp1WorkerPaths(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "cp2-recovery-secret-R4mN8pQ2"

	crashEntered := make(chan struct{}, 1)
	var mu sync.Mutex
	var botBID string
	var seq atomic.Int64
	nextCall := func(prefix string) string {
		return fmt.Sprintf("%s_%d", prefix, seq.Add(1))
	}

	provider := newCP2Provider(t, secret, func(req cp2Request) cp2Reply {
		if req.IsTriage {
			select {
			case crashEntered <- struct{}{}:
			default:
			}
			<-req.Ctx.Done()
			return cp2Reply{}
		}
		mu.Lock()
		targetB := botBID
		mu.Unlock()
		if req.IsActivity {
			switch {
			case strings.Contains(req.BaseUserText, "CP2_CRASH_TRIAGE"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(nil)}}}
				}
				return cp2Reply{Text: "Should not finish after SIGKILL."}
			case strings.Contains(req.BaseUserText, "CP2_PORTABLE_AUTO"):
				if req.LastRole == "user" {
					return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("ask"), Name: "activity_ask", Args: cp2FormattingAskArgs(nil)}}}
				}
				return cp2Reply{Text: "Portable auto settled with " + secret}
			default:
				return cp2Reply{Text: "OK."}
			}
		}
		if strings.HasPrefix(req.BaseUserText, "CP2_DEL_") {
			if req.LastRole == "user" {
				goal := strings.TrimPrefix(req.BaseUserText, "CP2_DEL_")
				args, _ := json.Marshal(map[string]any{
					"key":         "k_" + goal,
					"goal":        goal,
					"assignee_id": targetB,
				})
				return cp2Reply{Calls: []cp2ToolCall{{ID: nextCall("del"), Name: "activity_delegate", Args: string(args)}}}
			}
			return cp2Reply{Text: "Delegated."}
		}
		return cp2Reply{Text: "OK."}
	})
	provider.addForbiddenSecret(secret)

	crashDir := filepath.Join(root, "crash-triage")
	if err := os.MkdirAll(crashDir, 0o700); err != nil {
		t.Fatal(err)
	}
	crashOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(crashDir, "pi"),
		ProfilePath:     filepath.Join(crashDir, "contacts.json"),
		CredentialsPath: filepath.Join(crashDir, "connection.json"),
		FreshProfile:    true,
	}
	crashProc := startCP1Process(t, crashOpts)
	_ = crashProc.open(t, ctx)
	cp2ConfigureConnection(t, ctx, crashProc.client, pi.OpenAICompletions, provider.server.URL+"/v1", secret, ws, 2)
	cA, err := crashProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp2-cr-a",
		Profile: pi.ProductBotProfile{
			Name: "CrashA", Role: "Coord", Purpose: "Crash test",
			Instructions: "Coordinate.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cB, err := crashProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp2-cr-b",
		Profile: pi.ProductBotProfile{
			Name: "CrashB", Role: "Worker", Purpose: "Crash assignee",
			Instructions: "Work.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = cB.BotID
	mu.Unlock()
	if _, err := crashProc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: cB.BotID, OperationID: "cp2-cr-r1"},
		Text:                   "formatting: Markdown",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := crashProc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: cB.BotID, OperationID: "cp2-cr-r2"},
		Text:                   "Prefer formatting: Markdown",
	}); err != nil {
		t.Fatal(err)
	}
	if settled := cp1SendTurn(t, ctx, crashProc, cA.CurrentConversationID, "del-cr", "CP2_DEL_CP2_CRASH_TRIAGE", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	crAct := cp2FindActivityByGoal(t, ctx, crashProc, cA.BotID, "CP2_CRASH_TRIAGE")
	select {
	case <-crashEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for crash triage entry")
	}
	_ = cp2WaitQuestion(t, ctx, crashProc, crAct.ActivityID, "triage_pending")
	callsBeforeKill := provider.totalCalls.Load()
	_, _ = crashProc.client.Kill(ctx)
	crashProc.closed = true

	crashOpts.FreshProfile = false
	reCrash := startCP1Process(t, crashOpts)
	_ = reCrash.open(t, ctx)
	if provider.totalCalls.Load() != callsBeforeKill {
		t.Fatalf("triage_pending crash reopen replayed provider requests: before=%d after=%d", callsBeforeKill, provider.totalCalls.Load())
	}
	recCrAct, err := reCrash.client.GetActivity(ctx, crAct.ActivityID)
	if err != nil || recCrAct.State != "needsAttention" || len(recCrAct.Executions) != 1 || recCrAct.Executions[0].State != "unknown" ||
		len(recCrAct.Questions) != 1 || recCrAct.Questions[0].State != "open" {
		t.Fatalf("triage_pending crash recovery mismatch: %+v, %v", recCrAct, err)
	}
	if att, auto, ok := cp2TriageAccounting(t, nodePath, filepath.Join(crashDir, "nagi.db"), crAct.ActivityID); !ok || att != 1 || auto != 0 {
		t.Fatalf("triage_pending recovery accounting = (%d,%d,%v), want (1,0,true)", att, auto, ok)
	}
	reCrash.shutdown(t, ctx)

	for _, boundary := range []struct {
		name         string
		delivery     string
		wantDelivery string
	}{
		{name: "saved", delivery: "saved", wantDelivery: "saved"},
		{name: "dispatching", delivery: "dispatching", wantDelivery: "unknown"},
	} {
		bDir := filepath.Join(root, "boundary-"+boundary.name)
		if err := os.MkdirAll(bDir, 0o700); err != nil {
			t.Fatal(err)
		}
		bOpts := pi.ProductOptions{
			NodePath:        nodePath,
			WorkerPath:      workerPath,
			StateDir:        filepath.Join(bDir, "pi"),
			ProfilePath:     filepath.Join(bDir, "contacts.json"),
			CredentialsPath: filepath.Join(bDir, "connection.json"),
			FreshProfile:    true,
		}
		seedProc := startCP1Process(t, bOpts)
		_ = seedProc.open(t, ctx)
		cp2ConfigureConnection(t, ctx, seedProc.client, pi.OpenAICompletions, provider.server.URL+"/v1", secret, ws, 2)
		bBot, err := seedProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
			OperationID: "cp2-b-bot-" + boundary.name,
			Profile: pi.ProductBotProfile{
				Name: "BoundaryBot", Role: "Worker", Purpose: "Boundary recovery",
				Instructions: "Work.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		botBID = bBot.BotID
		mu.Unlock()
		if _, err := seedProc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
			ProductMemoryOperation: pi.ProductMemoryOperation{BotID: bBot.BotID, OperationID: "cp2-b-rule-" + boundary.name},
			Text:                   "formatting: Markdown",
		}); err != nil {
			t.Fatal(err)
		}
		if settled := cp1SendTurn(t, ctx, seedProc, bBot.CurrentConversationID, "del-b-"+boundary.name, "CP2_DEL_CP2_PORTABLE_AUTO", nil); settled.State != "completed" {
			t.Fatal(settled)
		}
		bSeedAct := cp2FindActivityByGoal(t, ctx, seedProc, bBot.BotID, "CP2_PORTABLE_AUTO")
		if settled := seedProc.waitSettled(t, ctx, bSeedAct.Executions[len(bSeedAct.Executions)-1].ExecutionID); settled.State != "completed" {
			t.Fatal(settled)
		}
		seedProc.shutdown(t, ctx)

		bDB := filepath.Join(bDir, "nagi.db")
		runNodeSQLite(t, nodePath, bDB, `
const { createHash } = require('node:crypto');
const { readFileSync, writeFileSync } = require('node:fs');
const { DatabaseSync } = require('node:sqlite');
const [dbPath, mirrorPath, actID, delivery] = process.argv.slice(1);
const canonical = value => value === null || typeof value !== 'object' ? JSON.stringify(value) :
  Array.isArray(value) ? '[' + value.map(canonical).join(',') + ']' :
  '{' + Object.keys(value).filter(k => value[k] !== undefined).sort().map(k => JSON.stringify(k) + ':' + canonical(value[k])).join(',') + '}';
const profile = JSON.parse(readFileSync(mirrorPath, 'utf8'));
const act = profile.activities.find(row => row.activityId === actID);
const execID = act.executionIds[act.executionIds.length - 1];
const exec = profile.executions.find(row => row.executionId === execID);
exec.state = 'running';
act.state = 'waitingForAnswer';
act.results = [];
act.questions[0].delivery = delivery;
const mirrorData = Buffer.from(canonical(profile) + '\n');
const digest = createHash('sha256').update(mirrorData).digest('hex');
writeFileSync(mirrorPath, mirrorData, { mode: 0o600 });
const db = new DatabaseSync(dbPath);
db.exec('BEGIN IMMEDIATE');
db.prepare('UPDATE executions SET state = ? WHERE execution_id = ?').run('running', JSON.stringify(execID));
db.prepare('UPDATE activities SET state = ?, results_json = ?, questions_json = ? WHERE activity_id = ?').run('waitingForAnswer', '[]', JSON.stringify(act.questions), JSON.stringify(actID));
db.prepare("UPDATE meta SET value = ? WHERE key = 'snapshot_sha256'").run(digest);
db.exec('COMMIT');
db.close();
`, bOpts.ProfilePath, bSeedAct.ActivityID, boundary.delivery)

		bOpts.FreshProfile = false
		callsBeforeBoundary := provider.totalCalls.Load()
		bProc := startCP1Process(t, bOpts)
		_ = bProc.open(t, ctx)
		if provider.totalCalls.Load() != callsBeforeBoundary {
			t.Fatalf("%s boundary reopen replayed provider requests", boundary.name)
		}
		bAct, err := bProc.client.GetActivity(ctx, bSeedAct.ActivityID)
		if err != nil || bAct.State != "needsAttention" || len(bAct.Executions) != 1 || bAct.Executions[0].State != "unknown" ||
			len(bAct.Questions) != 1 || bAct.Questions[0].Delivery != boundary.wantDelivery || bAct.Questions[0].Provenance != "bot_auto" ||
			bAct.Questions[0].Answer == nil || bAct.Questions[0].Answer.Text != "Markdown" {
			t.Fatalf("%s boundary recovery mismatch: %+v, %v", boundary.name, bAct, err)
		}
		bProc.shutdown(t, ctx)
	}

	v8Dir := filepath.Join(root, "v8-migration")
	if err := os.MkdirAll(v8Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	v8Opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(v8Dir, "pi"),
		ProfilePath:     filepath.Join(v8Dir, "contacts.json"),
		CredentialsPath: filepath.Join(v8Dir, "connection.json"),
	}
	writeProductFixture(t, v8Opts.CredentialsPath, map[string]any{
		"version": 2, "credential_source": "src-v8", "model": productModel,
		"context_tokens": 262144, "output_tokens": 4096, "working_directory": ws, "max_active_tasks": 2,
		"sources": map[string]any{"src-v8": map[string]string{"api": "openai-chat-completions", "endpoint": provider.server.URL + "/v1", "api_key": secret}},
	})
	v8DB := filepath.Join(v8Dir, "nagi.db")
	runNodeSQLite(t, nodePath, v8DB, `
const { createHash } = require('node:crypto');
const { writeFileSync } = require('node:fs');
const { DatabaseSync } = require('node:sqlite');
const [dbPath, mirrorPath, ws, baseURL] = process.argv.slice(1);
const canonical = value => value === null || typeof value !== 'object' ? JSON.stringify(value) :
  Array.isArray(value) ? '[' + value.map(canonical).join(',') + ']' :
  '{' + Object.keys(value).filter(k => value[k] !== undefined).sort().map(k => JSON.stringify(k) + ':' + canonical(value[k])).join(',') + '}';
const profile = {
  version: 1,
  bots: [{ botId: 'bot-v8', profile: { name: 'LegacyV8', role: '', purpose: 'v8 migration', instructions: 'Legacy.', workspace: ws, codingTools: [] }, profileRevision: 1, archived: false, currentConversationId: 'conv-v8' }],
  conversations: [{ conversationId: 'conv-v8', botId: 'bot-v8', revision: 1, draft: { revision: 0, text: '', materials: [] } }],
  activities: [{ activityId: 'act-v8', ownerBotId: 'bot-v8', originConversationId: 'conv-v8', revision: 1, state: 'completed', goal: 'Legacy v8 task', requirements: [], questions: [], materials: [], inputFiles: [], executionIds: ['exec-v8'], results: [] }],
  executions: [{ executionId: 'exec-v8', revision: 1, state: 'completed', operationId: 'op-v8', input: { revision: 0, text: 'Legacy v8 task', materials: [] }, retained: { id: 'exec-v8' } }],
  operations: [],
  connections: [{ revision: 1, sourceId: 'src-v8', configuration: { wireApi: 'openai-completions', baseUrl: baseURL, model: 'nagi-local-integration', contextTokens: 262144, outputTokens: 4096, workingDirectory: ws, maxActiveTasks: 2 } }],
  savedRevision: 1, appliedRevision: 1
};
const mirrorData = Buffer.from(canonical(profile) + '\n');
const digest = createHash('sha256').update(mirrorData).digest('hex');
writeFileSync(mirrorPath, mirrorData, { mode: 0o600 });
const db = new DatabaseSync(dbPath);
db.exec('PRAGMA foreign_keys = ON;');
db.exec(`+"`"+`
  CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
  CREATE TABLE bots (bot_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, profile_json TEXT NOT NULL, profile_revision INTEGER NOT NULL, archived INTEGER NOT NULL CHECK (archived IN (0, 1)), current_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED, retained_json TEXT, system_kind TEXT) STRICT;
  CREATE TABLE conversations (conversation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, revision INTEGER NOT NULL, draft_json TEXT NOT NULL, binding_json TEXT, latest_execution_id TEXT REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED) STRICT;
  CREATE TABLE activities (activity_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id), origin_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id), revision INTEGER NOT NULL, state TEXT NOT NULL, goal_json TEXT NOT NULL, requirements_json TEXT NOT NULL, questions_json TEXT NOT NULL, materials_json TEXT NOT NULL, input_files_json TEXT NOT NULL, execution_ids_json TEXT NOT NULL, results_json TEXT NOT NULL, binding_json TEXT, retained_json TEXT, granted_linked_activity_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(granted_linked_activity_ids_json) AND json_type(granted_linked_activity_ids_json) = 'array'), routing_json TEXT, acknowledged_at TEXT, acknowledged_execution_id TEXT, closed_at TEXT) STRICT;
  CREATE TABLE executions (execution_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, revision INTEGER NOT NULL, state TEXT NOT NULL, operation_id TEXT NOT NULL, input_json TEXT NOT NULL, error_json TEXT, engine_run_id TEXT, imported_activity_id TEXT REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, retained_json TEXT) STRICT;
  CREATE TABLE operations (operation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, method_json TEXT NOT NULL, fingerprint TEXT NOT NULL, receipt_json TEXT NOT NULL, reply_json TEXT NOT NULL, reply_error_json TEXT, connection_revision INTEGER REFERENCES connections(revision), retained_json TEXT) STRICT;
  CREATE TABLE connections (revision INTEGER PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, source_id TEXT NOT NULL, configuration_json TEXT NOT NULL) STRICT;
  CREATE TABLE bot_memory_rules (rule_id TEXT PRIMARY KEY, bot_id TEXT NOT NULL REFERENCES bots(bot_id) ON DELETE CASCADE, record_json TEXT NOT NULL) STRICT;
  CREATE TABLE bot_work_logs (log_id TEXT PRIMARY KEY, bot_id TEXT NOT NULL REFERENCES bots(bot_id) ON DELETE CASCADE, date TEXT NOT NULL, created_at TEXT NOT NULL, summary TEXT NOT NULL, tags_text TEXT NOT NULL, record_json TEXT NOT NULL) STRICT;
  CREATE INDEX bot_work_logs_by_date ON bot_work_logs(bot_id, date DESC, created_at DESC);
  CREATE VIRTUAL TABLE bot_work_logs_fts USING fts5(summary, tags_text, content='bot_work_logs', content_rowid='rowid');
  CREATE TRIGGER bot_work_logs_insert AFTER INSERT ON bot_work_logs BEGIN INSERT INTO bot_work_logs_fts(rowid, summary, tags_text) VALUES (new.rowid, new.summary, new.tags_text); END;
  CREATE TRIGGER bot_work_logs_delete AFTER DELETE ON bot_work_logs BEGIN INSERT INTO bot_work_logs_fts(bot_work_logs_fts, rowid, summary, tags_text) VALUES ('delete', old.rowid, old.summary, old.tags_text); END;
  CREATE TABLE bot_pending_learnings (learning_id TEXT PRIMARY KEY, bot_id TEXT NOT NULL REFERENCES bots(bot_id) ON DELETE CASCADE, record_json TEXT NOT NULL) STRICT;
  CREATE TABLE bot_memory_meta (bot_id TEXT PRIMARY KEY REFERENCES bots(bot_id) ON DELETE CASCADE, record_json TEXT NOT NULL) STRICT;
  CREATE TABLE activity_traces (seq INTEGER PRIMARY KEY AUTOINCREMENT, activity_id TEXT NOT NULL REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, at TEXT NOT NULL, kind TEXT NOT NULL CHECK (kind IN ('tool_started','tool_finished','file_confirmed','state_changed')), detail_json TEXT NOT NULL CHECK (json_valid(detail_json) AND length(CAST(detail_json AS BLOB)) <= 1024)) STRICT;
  CREATE INDEX activity_traces_by_activity ON activity_traces(activity_id, seq);
  CREATE INDEX activity_traces_by_execution ON activity_traces(activity_id, execution_id, seq);
  CREATE TABLE activity_trace_meta (execution_id TEXT PRIMARY KEY REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, activity_id TEXT NOT NULL REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, coverage TEXT NOT NULL CHECK (coverage IN ('recorded','unrecorded')), evicted_through_seq INTEGER NOT NULL DEFAULT 0) STRICT;
  CREATE INDEX activity_trace_meta_by_activity ON activity_trace_meta(activity_id);
  CREATE TABLE mcp_servers (server_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, config_json TEXT NOT NULL, catalog_json TEXT) STRICT;
  CREATE TABLE skill_approvals (grant_key TEXT PRIMARY KEY, owner_bot_id TEXT NOT NULL, source TEXT NOT NULL, skill_id TEXT NOT NULL, approval_digest TEXT NOT NULL, permissions_digest TEXT NOT NULL, mode TEXT NOT NULL CHECK (mode IN ('pure_compute_tested','user_approved')), installation_id TEXT NOT NULL, approved_at TEXT NOT NULL, record_json TEXT NOT NULL) STRICT;
  CREATE TABLE provider_accounts (provider_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, config_json TEXT NOT NULL) STRICT;
  CREATE TABLE execution_mutation_admissions (admission_id INTEGER PRIMARY KEY AUTOINCREMENT, execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, activity_id TEXT REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, call_id TEXT NOT NULL, tool_name TEXT NOT NULL, tool_class TEXT NOT NULL CHECK (tool_class IN ('readonly','mutating')), state TEXT NOT NULL CHECK (state IN ('admitted','non_dispatched','settled_ok','settled_error','unknown')), at TEXT NOT NULL) STRICT;
  CREATE INDEX mutation_admissions_by_execution ON execution_mutation_admissions(execution_id, tool_class, call_id);
  CREATE TABLE execution_admission_coverage (execution_id TEXT PRIMARY KEY REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED) STRICT;
  CREATE TABLE activity_escalations (activity_id TEXT PRIMARY KEY REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, prior_execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, escalated_execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, from_role TEXT NOT NULL, to_role TEXT NOT NULL, at TEXT NOT NULL) STRICT;
  CREATE TABLE activity_progress (activity_id TEXT PRIMARY KEY REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, call_id TEXT NOT NULL, progress_seq INTEGER NOT NULL CHECK (progress_seq > 0), record_json TEXT NOT NULL) STRICT;
  CREATE TABLE skill_approval_requests (request_id TEXT PRIMARY KEY, activity_id TEXT NOT NULL REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, execution_id TEXT NOT NULL REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED, call_id TEXT NOT NULL, bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, revision INTEGER NOT NULL CHECK (revision > 0), state TEXT NOT NULL CHECK (state IN ('open','approved','denied','expired','cancelled')), record_json TEXT NOT NULL, UNIQUE (execution_id, call_id)) STRICT;
  CREATE INDEX skill_approval_requests_by_activity ON skill_approval_requests(activity_id, state);
`+"`"+`);
db.exec('BEGIN IMMEDIATE');
db.prepare('INSERT INTO bots VALUES (?, ?, ?, ?, ?, ?, ?, ?)').run(JSON.stringify('bot-v8'), 0, JSON.stringify(profile.bots[0].profile), 1, 0, JSON.stringify('conv-v8'), null, null);
db.prepare('INSERT INTO conversations VALUES (?, ?, ?, ?, ?, ?, ?)').run(JSON.stringify('conv-v8'), 0, JSON.stringify('bot-v8'), 1, JSON.stringify(profile.conversations[0].draft), null, null);
db.prepare('INSERT INTO executions VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)').run(JSON.stringify('exec-v8'), 0, 1, 'completed', JSON.stringify('op-v8'), JSON.stringify(profile.executions[0].input), null, null, null, JSON.stringify({ id: 'exec-v8' }));
db.prepare('INSERT INTO activities VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)').run(JSON.stringify('act-v8'), 0, JSON.stringify('bot-v8'), JSON.stringify('conv-v8'), 1, 'completed', JSON.stringify('Legacy v8 task'), '[]', '[]', '[]', '[]', '["exec-v8"]', '[]', null, null, '[]', null, null, null, null);
db.prepare('INSERT INTO connections VALUES (?, ?, ?, ?)').run(1, 0, JSON.stringify('src-v8'), JSON.stringify(profile.connections[0].configuration));
for (const [k, v] of [['saved_revision', '1'], ['applied_revision', '1'], ['routing_policy', '"balanced"'], ['trace_generation', 'v8-gen'], ['snapshot_sha256', digest]]) {
  db.prepare('INSERT INTO meta VALUES (?, ?)').run(k, v);
}
db.exec('COMMIT');
db.exec('PRAGMA user_version = 8');
db.close();
`, v8Opts.ProfilePath, ws, provider.server.URL+"/v1")

	v8MirrorBefore := productRead(t, v8Opts.ProfilePath)
	callsBeforeV8 := provider.totalCalls.Load()
	v8Proc := startCP1Process(t, v8Opts)
	_ = v8Proc.open(t, ctx)
	if provider.totalCalls.Load() != callsBeforeV8 {
		t.Fatal("v8->v9 migration triggered provider calls")
	}
	if !bytes.Equal(productRead(t, v8Opts.ProfilePath), v8MirrorBefore) {
		t.Fatal("v8->v9 open altered original canonical contacts.json bytes")
	}
	v8Check := runNodeSQLite(t, nodePath, v8DB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
const cols = db.prepare('PRAGMA table_info(activities)').all().map(r => r.name);
const rows = db.prepare('SELECT count(*) AS n FROM activity_triage_accounting').get().n;
db.close();
console.log(JSON.stringify({ v, fk, hasUpdatedAt: cols.includes('updated_at'), rows }));
`)
	if v8Check != `{"v":11,"fk":0,"hasUpdatedAt":true,"rows":0}` {
		t.Fatalf("v8->v11 migration check = %s", v8Check)
	}
	v8Proc.shutdown(t, ctx)

	runNodeSQLite(t, nodePath, v8DB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec('PRAGMA user_version = 12');
db.close();
`)
	v10Client, startErr := pi.StartProduct(v8Opts)
	if startErr == nil {
		if _, readyErr := v10Client.Ready(ctx); readyErr == nil {
			if _, openErr := v10Client.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
				t.Fatal("worker admitted SQLite user_version=12")
			}
		}
		_, _ = v10Client.Wait(ctx)
	}
	v10After := runNodeSQLite(t, nodePath, v8DB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const bots = db.prepare('SELECT count(*) AS n FROM bots').get().n;
db.close();
console.log(JSON.stringify({ v, bots }));
`)
	if v10After != `{"v":12,"bots":1}` {
		t.Fatalf("v12 refusal mutated DB: %s", v10After)
	}

	portDir := filepath.Join(root, "portable-live")
	if err := os.MkdirAll(portDir, 0o700); err != nil {
		t.Fatal(err)
	}
	portOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(portDir, "pi"),
		ProfilePath:     filepath.Join(portDir, "contacts.json"),
		CredentialsPath: filepath.Join(portDir, "connection.json"),
		FreshProfile:    true,
	}
	portProc := startCP1Process(t, portOpts)
	_ = portProc.open(t, ctx)
	cp2ConfigureConnection(t, ctx, portProc.client, pi.OpenAICompletions, provider.server.URL+"/v1", secret, ws, 2)
	pA, err := portProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp2-port-a",
		Profile: pi.ProductBotProfile{
			Name: "PortA", Role: "Coord", Purpose: "Backup source",
			Instructions: "Coordinate.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pB, err := portProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "cp2-port-b",
		Profile: pi.ProductBotProfile{
			Name: "PortB", Role: "Worker", Purpose: "Backup worker",
			Instructions: "Work.", Workspace: ws, CodingTools: []string{}, ToolPreset: pi.ToolPresetNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	botBID = pB.BotID
	mu.Unlock()
	if _, err := portProc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: pB.BotID, OperationID: "cp2-port-rule"},
		Text:                   "formatting: Markdown",
	}); err != nil {
		t.Fatal(err)
	}
	if settled := cp1SendTurn(t, ctx, portProc, pA.CurrentConversationID, "del-port", "CP2_DEL_CP2_PORTABLE_AUTO", nil); settled.State != "completed" {
		t.Fatal(settled)
	}
	pAct := cp2FindActivityByGoal(t, ctx, portProc, pA.BotID, "CP2_PORTABLE_AUTO")
	if settled := portProc.waitSettled(t, ctx, pAct.Executions[len(pAct.Executions)-1].ExecutionID); settled.State != "completed" {
		t.Fatal(settled)
	}
	liveAct, err := portProc.client.GetActivity(ctx, pAct.ActivityID)
	if err != nil || len(liveAct.Questions) != 1 || liveAct.Questions[0].Provenance != "bot_auto" {
		t.Fatalf("live portable activity = %+v, %v", liveAct, err)
	}

	liveMirrorHash := cp2SHA256File(t, portOpts.ProfilePath)
	liveDBHash := cp2SHA256File(t, filepath.Join(portDir, "nagi.db"))
	backupPath := filepath.Join(root, "cp2-portable.json")
	if _, err := portProc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatalf("BackupProfile: %v", err)
	}
	if cp2SHA256File(t, portOpts.ProfilePath) != liveMirrorHash || cp2SHA256File(t, filepath.Join(portDir, "nagi.db")) != liveDBHash {
		t.Fatal("BackupProfile mutated live contacts.json or nagi.db bytes")
	}
	backupRaw := productRead(t, backupPath)
	if bytes.Contains(backupRaw, []byte(secret)) {
		t.Fatal("portable backup leaked secret")
	}
	portProc.shutdown(t, ctx)

	restoreDir := filepath.Join(root, "portable-restored")
	if err := os.MkdirAll(restoreDir, 0o700); err != nil {
		t.Fatal(err)
	}
	restoreProfile := filepath.Join(restoreDir, "contacts.json")
	if err := os.WriteFile(restoreProfile, backupRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	restoreOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(restoreDir, "pi"),
		ProfilePath:     restoreProfile,
		CredentialsPath: filepath.Join(restoreDir, "connection.json"),
	}
	callsBeforeRestore := provider.totalCalls.Load()
	for round := 1; round <= 2; round++ {
		rProc := startCP1Process(t, restoreOpts)
		_ = rProc.open(t, ctx)
		rAct, err := rProc.client.GetActivity(ctx, pAct.ActivityID)
		if err != nil || len(rAct.Questions) != 1 {
			t.Fatalf("restore round %d GetActivity = %+v, %v", round, rAct, err)
		}
		rq := rAct.Questions[0]
		lq := liveAct.Questions[0]
		if rq.Provenance != "bot_auto" || rq.EvidenceID != lq.EvidenceID || rq.SelectedOptionID != "markdown" ||
			rq.EvidenceRevision == nil || lq.EvidenceRevision == nil || *rq.EvidenceRevision != *lq.EvidenceRevision ||
			rq.Answer == nil || rq.Answer.Text != "Markdown" {
			t.Fatalf("restore round %d question provenance mismatch: got=%+v want=%+v", round, rq, lq)
		}
		rRes, err := rProc.client.ActivityResult(ctx, pi.ProductActivityExecutionParams{
			ProductActivityParams: pi.ProductActivityParams{ActivityID: pAct.ActivityID},
			ExecutionID:           rAct.Executions[len(rAct.Executions)-1].ExecutionID,
		})
		if err != nil || strings.Contains(rRes.Text, secret) || !strings.Contains(rRes.Text, "[credential]") {
			t.Fatalf("restore round %d result redaction mismatch: %+v, %v", round, rRes, err)
		}
		rProc.shutdown(t, ctx)
	}
	if provider.totalCalls.Load() != callsBeforeRestore {
		t.Fatalf("independent restore across 2 reopens triggered provider calls: before=%d after=%d", callsBeforeRestore, provider.totalCalls.Load())
	}
	if att, auto, ok := cp2TriageAccounting(t, nodePath, filepath.Join(restoreDir, "nagi.db"), pAct.ActivityID); !ok || att != 1 || auto != 1 {
		t.Fatalf("restored triage accounting = (%d,%d,%v), want (1,1,true)", att, auto, ok)
	}
	restoreSchema := runNodeSQLite(t, nodePath, filepath.Join(restoreDir, "nagi.db"), `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.close();
console.log(JSON.stringify({ v, fk }));
`)
	if restoreSchema != `{"v":11,"fk":0}` {
		t.Fatalf("restored schema check = %s", restoreSchema)
	}
	provider.assertNoProblems()
}
