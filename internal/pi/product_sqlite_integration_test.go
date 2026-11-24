//go:build integration

package pi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func runNodeSQLite(t *testing.T, nodePath, dbPath, script string, extraArgs ...string) string {
	t.Helper()
	args := append([]string{"-e", script, dbPath}, extraArgs...)
	cmd := exec.Command(nodePath, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("node sqlite script failed: %v\n%s\n%s", err, string(out), string(exitErr.Stderr))
		}
		t.Fatalf("node sqlite script failed: %v\n%s", err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func TestPiProductSQLiteSavedKeyAndInitialInstructions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var mu sync.Mutex
	var capturedSystems []string
	var authHeaders []string
	failWithEcho := false
	replyText := "OK"

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		auth := r.Header.Get("Authorization")
		var systemText string
		for _, m := range body.Messages {
			if m.Role == "system" || m.Role == "developer" {
				var s string
				if err := json.Unmarshal(m.Content, &s); err == nil {
					systemText += s + "\n"
				} else {
					systemText += string(m.Content) + "\n"
				}
			}
		}
		mu.Lock()
		shouldFail := failWithEcho
		reply := replyText
		authHeaders = append(authHeaders, auth)
		if systemText != "" {
			capturedSystems = append(capturedSystems, systemText)
		}
		mu.Unlock()
		if shouldFail {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("rejected bearer " + productKey + " upstream"))
			return
		}
		piChatResponse(w, reply, "", "", "")
	}))
	t.Cleanup(provider.Close)

	root := t.TempDir()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opts := pi.ProductOptions{
		NodePath:        os.Getenv("NAGI_TEST_NODE"),
		WorkerPath:      os.Getenv("NAGI_TEST_WORKER"),
		StateDir:        filepath.Join(root, "pi"),
		ProfilePath:     filepath.Join(root, "contacts.json"),
		CredentialsPath: filepath.Join(root, "connection.json"),
	}
	for _, p := range []string{opts.NodePath, opts.WorkerPath} {
		if info, err := os.Stat(p); err != nil || !filepath.IsAbs(p) || !info.Mode().IsRegular() {
			t.Fatalf("NAGI_TEST_NODE and NAGI_TEST_WORKER must name pinned regular absolute inputs: %q: %v", p, err)
		}
	}

	credential := map[string]string{"api": "openai-chat-completions", "endpoint": provider.URL + "/v1", "api_key": productKey}
	writeProductFixture(t, opts.CredentialsPath, map[string]any{
		"version": 2, "credential_source": productSource, "model": productModel,
		"context_tokens": 65536, "output_tokens": 4096, "working_directory": workspace, "max_active_tasks": 3,
		"sources": map[string]any{productSource: credential},
	})
	writeProductFixture(t, opts.ProfilePath, map[string]any{
		"version": 10, "selected": "legacy-bot",
		"contacts": []map[string]any{{
			"id": "legacy-bot", "name": "Legacy Bot", "role": "Assistant",
			"purpose": "Imported from v10", "workspace": workspace,
		}},
	})

	dbPath := filepath.Join(root, "nagi.db")
	proc := startProductProcess(t, opts)
	opened := proc.open(t, ctx)
	if len(opened.Snapshot.Bots) != 1 || opened.Snapshot.Bots[0].BotID != "legacy-bot" {
		t.Fatalf("v10 legacy import failed: %+v", opened.Snapshot.Bots)
	}
	conn, err := proc.client.Connection(ctx)
	if err != nil || conn.Saved == nil || !conn.Saved.CredentialPresent {
		t.Fatalf("expected CredentialPresent=true after v10 import: %+v, %v", conn, err)
	}

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("nagi.db not created: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("nagi.db permissions = %o, want 0600", mode)
	}
	inspectJSON := runNodeSQLite(t, opts.NodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const mode = db.prepare('PRAGMA journal_mode').get().journal_mode;
const version = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all();
const digest = db.prepare("SELECT value FROM meta WHERE key = 'snapshot_sha256'").get()?.value;
db.close();
console.log(JSON.stringify({ mode, version, fkCount: fk.length, digest }));
`)
	var inspected struct {
		Mode    string `json:"mode"`
		Version int    `json:"version"`
		FKCount int    `json:"fkCount"`
		Digest  string `json:"digest"`
	}
	if err := json.Unmarshal([]byte(inspectJSON), &inspected); err != nil {
		t.Fatalf("decode sqlite inspect %q: %v", inspectJSON, err)
	}
	mirrorBytes := productRead(t, opts.ProfilePath)
	sum := sha256.Sum256(mirrorBytes)
	mirrorDigest := hex.EncodeToString(sum[:])
	if !strings.EqualFold(inspected.Mode, "wal") || inspected.Version != 1 || inspected.FKCount != 0 || inspected.Digest != mirrorDigest {
		t.Fatalf("sqlite metadata mismatch: %+v mirrorDigest=%s", inspected, mirrorDigest)
	}

	backupPath := filepath.Join(root, "backup.json")
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	for _, checkPath := range []string{dbPath, opts.ProfilePath, backupPath} {
		if bytes.Contains(productRead(t, checkPath), []byte(productKey)) {
			t.Fatalf("plaintext API key leaked into %s", checkPath)
		}
	}

	blankConfig := pi.ProductConnectionInput{
		APIKey: "",
		ProductConnectionSettings: pi.ProductConnectionSettings{
			WireAPI: pi.OpenAICompletions, BaseURL: provider.URL + "/v1", Model: "updated-model",
			ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: workspace, MaxActiveTasks: 3,
		},
	}
	savedRev, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-save-blank-key", Configuration: blankConfig,
	})
	if err != nil || savedRev.SavedRevision == nil {
		t.Fatalf("blank-key SaveConnection failed: %+v, %v", savedRev, err)
	}
	if _, err := proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{
		OperationID: "op-apply-blank-key", SavedRevision: *savedRev.SavedRevision,
	}); err != nil {
		t.Fatalf("ApplyConnection failed: %v", err)
	}
	if err := proc.client.TestConnection(ctx, blankConfig); err != nil {
		t.Fatalf("blank-key TestConnection failed: %v", err)
	}

	mu.Lock()
	failWithEcho = true
	mu.Unlock()
	testErr := proc.client.TestConnection(ctx, blankConfig)
	mu.Lock()
	failWithEcho = false
	mu.Unlock()
	if testErr == nil {
		t.Fatal("expected HTTP 401 failure on blank-key TestConnection")
	}
	if strings.Contains(testErr.Error(), productKey) {
		t.Fatalf("TestConnection error leaked saved API key: %v", testErr)
	}

	created, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-create-cedar",
		Profile: pi.ProductBotProfile{
			Name: "  Cedar  ", Purpose: "  Track releases  ", Instructions: "  Be brief.  ",
		},
	})
	if err != nil {
		t.Fatalf("CreateBot failed: %v", err)
	}

	sendAndWait := func(p *productProcess, convID, text, draftOp, sendOp string) {
		t.Helper()
		conv, err := p.client.GetConversation(ctx, convID)
		if err != nil {
			t.Fatal(err)
		}
		draft, err := p.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
			ProductText:               pi.ProductText{Text: text},
			ExpectedRevision:          conv.Draft.Revision,
			OperationID:               draftOp,
		})
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := p.client.Send(ctx, pi.ProductSendParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
			DraftRevision:             draft.Revision,
			OperationID:               sendOp,
		})
		if err != nil || receipt.Execution == nil {
			t.Fatalf("Send failed: %+v, %v", receipt, err)
		}
		execID := receipt.Execution.ExecutionID
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			cur, err := p.client.GetConversation(ctx, convID)
			if err != nil {
				t.Fatal(err)
			}
			if cur.LatestExecution != nil && cur.LatestExecution.ExecutionID == execID && cur.LatestExecution.State == "completed" {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("send %s did not complete", sendOp)
	}

	const multiBubbleReply = "First short bubble.\n<<<NEXT>>>\nSecond short bubble."
	mu.Lock()
	replyText = multiBubbleReply
	mu.Unlock()
	sendAndWait(proc, created.CurrentConversationID, "hello one", "op-d1", "op-s1")
	mu.Lock()
	replyText = "OK"
	sysCount1 := len(capturedSystems)
	var firstSys string
	if sysCount1 > 0 {
		firstSys = capturedSystems[sysCount1-1]
	}
	mu.Unlock()
	expectedBoundary := "Default working directory: " + workspace + ". Do not modify or write files outside this directory unless the user explicitly asks."
	idxName := strings.Index(firstSys, "You are Cedar.")
	idxPurpose := strings.Index(firstSys, "Purpose:\nTrack releases")
	idxIM := strings.Index(firstSys, "<<<NEXT>>>")
	idxInst := strings.Index(firstSys, "Be brief.")
	idxDir := strings.Index(firstSys, expectedBoundary)
	if idxName < 0 || idxPurpose <= idxName || idxIM <= idxPurpose || idxInst <= idxIM || idxDir <= idxInst || !strings.Contains(firstSys, "at most three bubbles") {
		t.Fatalf("initial system prompt missing ordered IM sections: %q", firstSys)
	}

	updated, err := proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			OperationID: "op-update-cedar", BotID: created.BotID, ExpectedRevision: created.ProfileRevision,
		},
		Profile: pi.ProductBotProfile{
			Name: "Cedar Prime", Purpose: "Review PRs", Instructions: "Verify invariants.",
		},
		StartNewConversation: false,
	})
	if err != nil {
		t.Fatalf("UpdateBot failed: %v", err)
	}

	outcome, err := proc.client.Shutdown(ctx)
	proc.closed = true
	proc.exit(t, ctx, outcome, true, err)

	if err := os.Remove(opts.ProfilePath); err != nil {
		t.Fatalf("remove mirror: %v", err)
	}
	proc2 := startProductProcess(t, opts)
	reopened := proc2.open(t, ctx)
	if len(reopened.Snapshot.Bots) != 2 || reopened.Snapshot.Connection.Saved == nil || reopened.Snapshot.Connection.Saved.Model != "updated-model" {
		t.Fatalf("standalone SQLite reopen without mirror lost state: %+v", reopened.Snapshot)
	}
	page, err := proc2.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: created.CurrentConversationID},
		ProductPageParams:         pi.ProductPageParams{Limit: 10},
	})
	if err != nil || len(page.Items) != 2 || page.Items[1].Text != multiBubbleReply {
		t.Fatalf("reopened messages did not preserve raw <<<NEXT>>> separator: %+v, %v", page.Items, err)
	}
	for i, item := range page.Items {
		if _, err := time.Parse(time.RFC3339Nano, item.Timestamp); err != nil {
			t.Fatalf("reopened message[%d] missing valid RFC3339 timestamp %q: %v", i, item.Timestamp, err)
		}
	}

	sendAndWait(proc2, created.CurrentConversationID, "hello two", "op-d2", "op-s2")
	mu.Lock()
	secondSys := capturedSystems[len(capturedSystems)-1]
	mu.Unlock()
	if !strings.Contains(secondSys, "You are Cedar.") || !strings.Contains(secondSys, "<<<NEXT>>>") || strings.Contains(secondSys, "Cedar Prime") {
		t.Fatalf("existing binding did not preserve captured initial instructions after edit+restart: %q", secondSys)
	}

	rotated, err := proc2.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			OperationID: "op-rotate-cedar", BotID: updated.BotID, ExpectedRevision: updated.ProfileRevision,
		},
		Profile:              updated.Profile,
		StartNewConversation: true,
	})
	if err != nil || rotated.CurrentConversationID == created.CurrentConversationID {
		t.Fatalf("StartNewConversation failed: %+v, %v", rotated, err)
	}
	sendAndWait(proc2, rotated.CurrentConversationID, "hello three", "op-d3", "op-s3")
	mu.Lock()
	thirdSys := capturedSystems[len(capturedSystems)-1]
	mu.Unlock()
	if !strings.Contains(thirdSys, "You are Cedar Prime.") || !strings.Contains(thirdSys, "Purpose:\nReview PRs") ||
		!strings.Contains(thirdSys, "Verify invariants.") || !strings.Contains(thirdSys, expectedBoundary) {
		t.Fatalf("new conversation did not compose updated instructions: %q", thirdSys)
	}

	outcome, err = proc2.client.Shutdown(ctx)
	proc2.closed = true
	proc2.exit(t, ctx, outcome, true, err)

	staleMirror := []byte("{\"version\":1,\"bots\":[]}\n")
	if err := os.WriteFile(opts.ProfilePath, staleMirror, 0o600); err != nil {
		t.Fatal(err)
	}
	staleSum := sha256.Sum256(staleMirror)
	staleDigest := hex.EncodeToString(staleSum[:])
	expectedDigest := runNodeSQLite(t, opts.NodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.prepare("INSERT OR REPLACE INTO meta (key, value) VALUES ('pending_snapshot_previous', ?)").run(JSON.stringify(process.argv[2]));
const digest = db.prepare("SELECT value FROM meta WHERE key = 'snapshot_sha256'").get().value;
db.close();
console.log(digest);
`, staleDigest)

	proc3 := startProductProcess(t, opts)
	repairedOpen := proc3.open(t, ctx)
	if len(repairedOpen.Snapshot.Bots) != 2 {
		t.Fatalf("interrupted mirror repair lost bots: %+v", repairedOpen.Snapshot.Bots)
	}
	repairedMirror := productRead(t, opts.ProfilePath)
	repairedSum := sha256.Sum256(repairedMirror)
	if hex.EncodeToString(repairedSum[:]) != expectedDigest {
		t.Fatalf("interrupted mirror was not repaired: got %s want %s", hex.EncodeToString(repairedSum[:]), expectedDigest)
	}
	outcome, err = proc3.client.Shutdown(ctx)
	proc3.closed = true
	proc3.exit(t, ctx, outcome, true, err)

	runNodeSQLite(t, opts.NodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec('PRAGMA user_version = 2');
db.close();
`)
	badClient, startErr := pi.StartProduct(opts)
	if startErr == nil {
		_, readyErr := badClient.Ready(ctx)
		if readyErr == nil {
			_, openErr := badClient.Open(ctx, []int{pi.ProductVersion})
			if openErr == nil {
				t.Fatal("worker admitted a SQLite database with newer user_version=2")
			}
		}
		_, _ = badClient.Wait(ctx)
	}
	botCountAfterRefusal := runNodeSQLite(t, opts.NodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const row = db.prepare('SELECT COUNT(*) AS n FROM bots').get();
db.close();
console.log(row.n);
`)
	if botCountAfterRefusal != "2" {
		t.Fatalf("newer-version refusal altered SQLite bots count: %s", botCountAfterRefusal)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, h := range authHeaders {
		if h != "Bearer "+productKey {
			t.Fatalf("unexpected authorization header: %q", h)
		}
	}
}
