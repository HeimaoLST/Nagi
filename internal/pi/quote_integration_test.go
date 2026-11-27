//go:build integration

package pi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
)

func intPtr(v int) *int {
	return &v
}

func TestPiProductQuoteSpeechActRedactionAndBackup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const inactiveKey = "sk-im-quote-inactive-secret-9876543210"
	const rawText = "Keep this exact user text.\nSecond line."
	material := pi.ProductMaterial{ID: "material-1", Revision: 1, Name: "Reference", Text: "Unchanged Material."}

	nodePath := os.Getenv("NAGI_TEST_NODE")
	workerPath := os.Getenv("NAGI_TEST_WORKER")
	if os.Getenv("NAGI_PI_PAYLOAD") == "" {
		t.Setenv("NAGI_PI_PAYLOAD", filepath.Dir(filepath.Dir(nodePath)))
	}

	root := t.TempDir()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var convID string
	var capturedSystems []string
	var capturedUsers []string
	toolResults := map[string]string{}
	var providerErrs []string
	var providerCalls atomic.Int32
	quoteEntered := make(chan struct{})
	quoteRelease := make(chan struct{})
	var enteredOnce sync.Once

	recordErr := func(format string, args ...any) {
		mu.Lock()
		providerErrs = append(providerErrs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		var body struct {
			Messages []struct {
				Role       string          `json:"role"`
				ToolCallID string          `json:"tool_call_id"`
				Content    json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		var sys, lastUser string
		for _, m := range body.Messages {
			if m.Role == "system" || m.Role == "developer" {
				sys += unmarshalToolText(m.Content)
			}
			if m.Role == "user" {
				lastUser = unmarshalToolText(m.Content)
			}
		}
		last := body.Messages[len(body.Messages)-1]
		if last.Role == "tool" && last.ToolCallID != "" {
			mu.Lock()
			toolResults[last.ToolCallID] = unmarshalToolText(last.Content)
			mu.Unlock()
		}

		mu.Lock()
		capturedSystems = append(capturedSystems, sys)
		capturedUsers = append(capturedUsers, lastUser)
		cid := convID
		mu.Unlock()

		if strings.Contains(lastUser, inactiveKey) {
			recordErr("provider user prompt leaked inactive key: %q", lastUser)
		}

		if strings.HasPrefix(lastUser, rawText) {
			if last.Role == "user" {
				enteredOnce.Do(func() { close(quoteEntered) })
				select {
				case <-quoteRelease:
				case <-r.Context().Done():
					return
				}
				args, _ := json.Marshal(map[string]any{"conversation_id": cid, "max_bytes": 4096})
				piChatResponse(w, "", "conversation_read", "q-read", string(args))
				return
			}
			if last.ToolCallID == "q-read" {
				args, _ := json.Marshal(map[string]any{"conversation_id": cid, "query": inactiveKey})
				piChatResponse(w, "", "conversation_search", "q-search", string(args))
				return
			}
			piChatResponse(w, "Acknowledged quoted turn.", "", "", "")
			return
		}
		piChatResponse(w, "Reply to quote later.", "", "", "")
	}))
	t.Cleanup(provider.Close)

	opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(root, "pi"),
		ProfilePath:     filepath.Join(root, "contacts.json"),
		CredentialsPath: filepath.Join(root, "connection.json"),
		FreshProfile:    true,
	}
	writeProductFixture(t, opts.CredentialsPath, map[string]any{
		"version": 2, "credential_source": productSource, "model": productModel,
		"context_tokens": 65536, "output_tokens": 4096, "working_directory": workspace, "max_active_tasks": 4,
		"sources": map[string]any{
			productSource:      map[string]string{"api": "openai-chat-completions", "endpoint": provider.URL + "/v1", "api_key": productKey},
			"inactive-archive": map[string]string{"api": "openai-chat-completions", "endpoint": "http://127.0.0.1:9/v1", "api_key": inactiveKey},
		},
	})

	proc := startProductProcess(t, opts)
	proc.open(t, ctx)

	savedConn, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-conn-save",
		Configuration: pi.ProductConnectionInput{
			APIKey: productKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI: pi.OpenAICompletions, BaseURL: provider.URL + "/v1", Model: productModel,
				ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: workspace, MaxActiveTasks: 4,
			},
		},
	})
	if err != nil || savedConn.SavedRevision == nil {
		t.Fatalf("SaveConnection = %+v, %v", savedConn, err)
	}
	if _, err := proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{
		SavedRevision: *savedConn.SavedRevision, OperationID: "op-conn-apply",
	}); err != nil {
		t.Fatal(err)
	}

	bot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-cedar",
		Profile:     pi.ProductBotProfile{Name: "Cedar", Workspace: workspace, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	convID = bot.CurrentConversationID
	mu.Unlock()

	firstDraft, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "Original question.", Materials: []pi.ProductMaterial{}},
		ExpectedRevision:          0, OperationID: "op-d1",
	})
	if err != nil || firstDraft.Quote != nil {
		t.Fatalf("initial SaveDraft = %+v, %v", firstDraft, err)
	}
	firstReceipt, err := proc.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		DraftRevision:             firstDraft.Revision, OperationID: "op-s1",
	})
	if err != nil || firstReceipt.Execution == nil {
		t.Fatalf("first Send = %+v, %v", firstReceipt, err)
	}
	waitSettledExecution(t, ctx, proc, firstReceipt.Execution.ExecutionID)

	hist1, err := proc.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ProductPageParams:         pi.ProductPageParams{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	var earlierMsgID string
	for _, item := range hist1.Items {
		if item.Quote != nil {
			t.Fatalf("unquoted history item unexpectedly carried quote: %+v", item)
		}
		if item.Role == "assistant" {
			earlierMsgID = item.MessageID
		}
	}
	if earlierMsgID == "" {
		t.Fatalf("assistant reply missing from history: %+v", hist1.Items)
	}

	convAfter1, err := proc.client.GetConversation(ctx, bot.CurrentConversationID)
	if err != nil || convAfter1.Draft == nil {
		t.Fatalf("GetConversation after turn 1 = %+v, %v", convAfter1, err)
	}
	baseRev := convAfter1.Draft.Revision

	invalidQuotes := []pi.ProductQuote{
		{MessageID: "", BubbleIndex: intPtr(0), Sender: "bot", AuthorName: "Cedar", Text: "ok"},
		{MessageID: earlierMsgID, BubbleIndex: intPtr(-1), Sender: "bot", AuthorName: "Cedar", Text: "ok"},
		{MessageID: earlierMsgID, BubbleIndex: intPtr(0), Sender: "assistant", AuthorName: "Cedar", Text: "ok"},
		{MessageID: earlierMsgID, BubbleIndex: intPtr(0), Sender: "bot", AuthorName: "   ", Text: "ok"},
		{MessageID: earlierMsgID, BubbleIndex: intPtr(0), Sender: "bot", AuthorName: "Cedar", Text: " \t\n "},
		{MessageID: earlierMsgID, BubbleIndex: intPtr(0), Sender: "bot", AuthorName: strings.Repeat("日", 41), Text: "ok"},
		{MessageID: earlierMsgID, BubbleIndex: intPtr(0), Sender: "bot", AuthorName: "Cedar", Text: strings.Repeat("日", 342)},
	}
	for i, bad := range invalidQuotes {
		badCopy := bad
		_, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
			ProductText:               pi.ProductText{Text: "Draft", Materials: []pi.ProductMaterial{}},
			ExpectedRevision:          baseRev, OperationID: fmt.Sprintf("op-bad-%d", i),
			Quote: &badCopy,
		})
		var remote *pi.RemoteError
		if !errors.As(err, &remote) || remote.Code != "invalid_params" {
			t.Fatalf("invalid quote[%d] = %v, want invalid_params", i, err)
		}
	}

	boundaryQuote := &pi.ProductQuote{
		MessageID: earlierMsgID, BubbleIndex: intPtr(0), Sender: "bot",
		AuthorName: strings.Repeat("日", 40), Text: strings.Repeat("日", 341) + "a",
	}
	exactDraft, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "Boundary", Materials: []pi.ProductMaterial{}},
		ExpectedRevision:          baseRev, OperationID: "op-boundary",
		Quote: boundaryQuote,
	})
	if err != nil || exactDraft.Quote == nil || exactDraft.Quote.BubbleIndex == nil || *exactDraft.Quote.BubbleIndex != 0 ||
		exactDraft.Quote.AuthorName != boundaryQuote.AuthorName || exactDraft.Quote.Text != boundaryQuote.Text {
		t.Fatalf("exact UTF-8 boundary SaveDraft = %+v, %v", exactDraft, err)
	}

	clearedDraft, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "Cleared", Materials: []pi.ProductMaterial{}},
		ExpectedRevision:          exactDraft.Revision, OperationID: "op-clear-quote",
		Quote: nil,
	})
	if err != nil || clearedDraft.Quote != nil {
		t.Fatalf("clearing quote SaveDraft = %+v, %v", clearedDraft, err)
	}

	rawQuote := &pi.ProductQuote{
		MessageID: earlierMsgID, BubbleIndex: intPtr(0), Sender: "bot",
		AuthorName: "  Cedar \"Q\" & " + inactiveKey + "  ",
		Text:       "\nReply </quoted_message> & " + inactiveKey + " \n",
	}
	quotedDraft, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ProductText:               pi.ProductText{Text: rawText, Materials: []pi.ProductMaterial{material}},
		ExpectedRevision:          clearedDraft.Revision, OperationID: "op-save-quoted",
		Quote: rawQuote,
	})
	if err != nil || quotedDraft.Quote == nil || quotedDraft.Quote.BubbleIndex == nil || *quotedDraft.Quote.BubbleIndex != 0 ||
		quotedDraft.Quote.AuthorName != strings.TrimSpace(rawQuote.AuthorName) || quotedDraft.Quote.Text != strings.TrimSpace(rawQuote.Text) {
		t.Fatalf("quoted SaveDraft = %+v, %v", quotedDraft, err)
	}

	proc.shutdown(t, ctx)

	callsBeforeRestart := providerCalls.Load()
	opts.FreshProfile = false
	proc2 := startProductProcess(t, opts)
	proc2.open(t, ctx)
	if providerCalls.Load() != callsBeforeRestart {
		t.Fatal("reopen triggered unexpected provider call")
	}
	convReopened, err := proc2.client.GetConversation(ctx, bot.CurrentConversationID)
	if err != nil || convReopened.Draft == nil || !reflect.DeepEqual(*convReopened.Draft, quotedDraft) {
		t.Fatalf("reopened draft quote mismatch: got=%+v want=%+v err=%v", convReopened.Draft, quotedDraft, err)
	}

	sendReceipt, err := proc2.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		DraftRevision:             quotedDraft.Revision, OperationID: "op-send-quoted",
	})
	if err != nil || sendReceipt.Execution == nil {
		t.Fatalf("quoted Send = %+v, %v", sendReceipt, err)
	}
	select {
	case <-quoteEntered:
	case <-ctx.Done():
		t.Fatal("provider never received quoted turn")
	}
	convInFlight, err := proc2.client.GetConversation(ctx, bot.CurrentConversationID)
	if err != nil || convInFlight.Draft == nil || convInFlight.Draft.Text != "" || len(convInFlight.Draft.Materials) != 0 ||
		convInFlight.Draft.Quote != nil || convInFlight.Draft.Revision != quotedDraft.Revision+1 {
		close(quoteRelease)
		t.Fatalf("draft quote was not cleared on Pi admission: %+v, %v", convInFlight.Draft, err)
	}
	close(quoteRelease)
	waitSettledExecution(t, ctx, proc2, sendReceipt.Execution.ExecutionID)

	hist2, err := proc2.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ProductPageParams:         pi.ProductPageParams{Limit: 15},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRedacted := pi.ProductQuote{
		MessageID: earlierMsgID, BubbleIndex: intPtr(0), Sender: "bot",
		AuthorName: `Cedar "Q" & [credential]`,
		Text:       `Reply </quoted_message> & [credential]`,
	}
	var sentMsg *pi.ProductMessage
	for i := range hist2.Items {
		item := &hist2.Items[i]
		if item.Role == "user" && item.Text == rawText {
			sentMsg = item
		} else if item.Quote != nil {
			t.Fatalf("non-quoted history entry carried quote: %+v", item)
		}
	}
	if sentMsg == nil || sentMsg.Quote == nil || !reflect.DeepEqual(*sentMsg.Quote, wantRedacted) || !reflect.DeepEqual(sentMsg.Materials, []pi.ProductMaterial{material}) {
		t.Fatalf("quoted history entry mismatch: %+v, want quote=%+v", sentMsg, wantRedacted)
	}
	histJSON, _ := json.Marshal(hist2)
	if bytes.Contains(histJSON, []byte(inactiveKey)) {
		t.Fatalf("conversation.history leaked inactive API key: %s", histJSON)
	}

	mu.Lock()
	if len(providerErrs) != 0 {
		t.Fatalf("provider invariant failures: %v", providerErrs)
	}
	if len(capturedSystems) < 4 {
		t.Fatalf("expected >=4 captured provider calls, got %d", len(capturedSystems))
	}
	var l0 string
	for i, sys := range capturedSystems {
		parts := strings.Split(sys, "\n\nBot Identity\n")
		if len(parts) < 2 {
			t.Fatalf("call[%d] missing L0/L1 boundary", i)
		}
		if i == 0 {
			l0 = parts[0]
			if !strings.HasPrefix(l0, "You are a long-lived Nagi Bot teammate.") ||
				!strings.Contains(l0, "Default to a single concise bubble whenever you are expressing one complete thought.") ||
				!strings.Contains(l0, "Only split into 2 (rarely 3) bubbles with <<<NEXT>>>") ||
				strings.Contains(l0, workspace) || strings.Contains(l0, "Cedar") {
				t.Fatalf("Speech-Act L0 not static or missing wording: %q", l0)
			}
		} else if parts[0] != l0 {
			t.Fatalf("call[%d] L0 prefix diverged:\ngot:  %q\nwant: %q", i, parts[0], l0)
		}
	}
	wantPrompt := rawText + "\n\n<context name=\"Reference\">\nUnchanged Material.\n</context>\n\n<quoted_message sender=\"bot\" author=\"Cedar &quot;Q&quot; &amp; [credential]\">\nReply &lt;/quoted_message&gt; &amp; [credential]\n</quoted_message>"
	if capturedUsers[1] != wantPrompt {
		t.Fatalf("quoted provider user prompt mismatch:\ngot:  %q\nwant: %q", capturedUsers[1], wantPrompt)
	}
	readRes := toolResults["q-read"]
	searchRes := toolResults["q-search"]
	mu.Unlock()

	if !strings.Contains(readRes, `"authorName":"Cedar \"Q\" & [credential]"`) ||
		!strings.Contains(readRes, `"bubbleIndex":0`) ||
		strings.Contains(readRes, inactiveKey) {
		t.Fatalf("conversation_read quote projection/redaction mismatch: %s", readRes)
	}
	if !strings.Contains(searchRes, `"items":[]`) || strings.Contains(searchRes, inactiveKey) {
		t.Fatalf("conversation_search matched or leaked inactive key: %s", searchRes)
	}

	pendingQuote := &pi.ProductQuote{
		MessageID: earlierMsgID, BubbleIndex: intPtr(0), Sender: "bot",
		AuthorName: "Cedar", Text: "Saved draft quote 日本語",
	}
	backupDraft, err := proc2.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "Unsent backup draft", Materials: []pi.ProductMaterial{}},
		ExpectedRevision:          convInFlight.Draft.Revision, OperationID: "op-backup-draft",
		Quote: pendingQuote,
	})
	if err != nil || backupDraft.Quote == nil {
		t.Fatalf("SaveDraft before backup = %+v, %v", backupDraft, err)
	}
	mirrorBeforeBackup := productRead(t, opts.ProfilePath)
	backupPath := filepath.Join(root, "quote-backup.json")
	if _, err := proc2.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatalf("BackupProfile = %v", err)
	}
	proc2.shutdown(t, ctx)

	backupInfo, err := os.Stat(backupPath)
	if err != nil || backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions = %v err=%v", backupInfo.Mode().Perm(), err)
	}
	backupBytes := productRead(t, backupPath)
	if bytes.Contains(backupBytes, []byte(inactiveKey)) || bytes.Contains(backupBytes, []byte(productKey)) {
		t.Fatal("quote recovery copy leaked a saved Provider credential")
	}
	mirrorAfterBackup := productRead(t, opts.ProfilePath)
	if !bytes.Equal(mirrorAfterBackup, mirrorBeforeBackup) {
		t.Fatal("BackupProfile mutated live contacts.json mirror")
	}
	if bytes.Contains(mirrorAfterBackup, []byte(`"backup"`)) {
		t.Fatal("compact contacts.json mirror included backup envelope")
	}

	callsBeforeRestore := providerCalls.Load()
	restoreRoot := t.TempDir()
	restoreProfile := filepath.Join(restoreRoot, "contacts.json")
	if err := os.WriteFile(restoreProfile, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	recoveryCopy, err := store.InspectStoreRecoveryCopy(backupPath)
	if err != nil || recoveryCopy.Contacts != 1 {
		t.Fatalf("InspectStoreRecoveryCopy = %+v, %v", recoveryCopy, err)
	}
	target, err := store.InspectStoreRecoveryTarget(restoreProfile)
	if err != nil {
		t.Fatal(err)
	}
	res, err := store.RestoreStoreRecoveryCopyWithRename(recoveryCopy, target, os.Rename)
	if err != nil || res.Outcome != store.StoreRecoveryVerifiedInstalled {
		t.Fatalf("RestoreStoreRecoveryCopyWithRename = %+v, %v", res, err)
	}

	restoreOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(restoreRoot, "pi"),
		ProfilePath:     restoreProfile,
		CredentialsPath: filepath.Join(restoreRoot, "connection.json"),
	}
	restored1 := startProductProcess(t, restoreOpts)
	restored1.open(t, ctx)
	got1, err := restored1.client.GetConversation(ctx, bot.CurrentConversationID)
	if err != nil || got1.Draft == nil || !reflect.DeepEqual(*got1.Draft, backupDraft) {
		t.Fatalf("restored draft quote mismatch: got=%+v want=%+v err=%v", got1.Draft, backupDraft, err)
	}
	resaved, err := restored1.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "Resaved after restore", Materials: []pi.ProductMaterial{}},
		ExpectedRevision:          got1.Draft.Revision, OperationID: "op-resave-restored",
		Quote: pendingQuote,
	})
	if err != nil || resaved.Quote == nil || resaved.Quote.BubbleIndex == nil || *resaved.Quote.BubbleIndex != 0 {
		t.Fatalf("re-save after restore failed: %+v, %v", resaved, err)
	}
	restored1.shutdown(t, ctx)

	restored2 := startProductProcess(t, restoreOpts)
	restored2.open(t, ctx)
	got2, err := restored2.client.GetConversation(ctx, bot.CurrentConversationID)
	if err != nil || got2.Draft == nil || !reflect.DeepEqual(*got2.Draft, resaved) {
		t.Fatalf("second reopen draft quote mismatch: got=%+v want=%+v err=%v", got2.Draft, resaved, err)
	}
	restored2.shutdown(t, ctx)

	for _, dbFile := range []string{filepath.Join(root, "nagi.db"), filepath.Join(restoreRoot, "nagi.db")} {
		schema := runNodeSQLite(t, nodePath, dbFile, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.close();
console.log(JSON.stringify({ v, fk }));
`)
		if !strings.Contains(schema, `"v":14`) || !strings.Contains(schema, `"fk":0`) {
			t.Fatalf("SQLite %s schema/FK mismatch = %s", dbFile, schema)
		}
	}
	if bytes.Contains(productRead(t, restoreProfile), []byte(`"backup"`)) {
		t.Fatal("restored contacts.json mirror retained backup envelope")
	}
	if providerCalls.Load() != callsBeforeRestore {
		t.Fatalf("restore/reopen triggered provider calls: before=%d after=%d", callsBeforeRestore, providerCalls.Load())
	}
}
