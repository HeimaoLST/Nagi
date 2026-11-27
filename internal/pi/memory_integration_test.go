//go:build integration

package pi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func drainMemoryChangedEvents(proc *productProcess) []pi.ProductMemoryChangedEvent {
	var out []pi.ProductMemoryChangedEvent
	for {
		select {
		case ev := <-proc.events:
			if ev.Name == "memory.changed" {
				var changed pi.ProductMemoryChangedEvent
				if json.Unmarshal(ev.Data, &changed) == nil {
					out = append(out, changed)
				}
			}
		default:
			return out
		}
	}
}

func TestPiProductMemorySQLiteV2ToV3IsolationExclusionAndCaps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	root := t.TempDir()
	profileDir := filepath.Join(root, "profile")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opts := pi.ProductOptions{
		NodePath:        os.Getenv("NAGI_TEST_NODE"),
		WorkerPath:      os.Getenv("NAGI_TEST_WORKER"),
		StateDir:        filepath.Join(root, "pi"),
		ProfilePath:     filepath.Join(profileDir, "contacts.json"),
		CredentialsPath: filepath.Join(profileDir, "connection.json"),
	}
	dbPath := filepath.Join(profileDir, "nagi.db")
	runNodeSQLite(t, opts.NodePath, dbPath, `
const { createHash } = require('node:crypto');
const { writeFileSync } = require('node:fs');
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec(`+"`"+`
  PRAGMA foreign_keys = ON;
  CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
  CREATE TABLE bots (
    bot_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, profile_json TEXT NOT NULL,
    profile_revision INTEGER NOT NULL, archived INTEGER NOT NULL CHECK (archived IN (0, 1)),
    current_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    retained_json TEXT,
    system_kind TEXT
  ) STRICT;
  CREATE TABLE conversations (
    conversation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE,
    bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, revision INTEGER NOT NULL,
    draft_json TEXT NOT NULL, binding_json TEXT,
    latest_execution_id TEXT REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED
  ) STRICT;
  CREATE TABLE activities (
    activity_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE,
    owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id), origin_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
    revision INTEGER NOT NULL, state TEXT NOT NULL, goal_json TEXT NOT NULL,
    requirements_json TEXT NOT NULL, questions_json TEXT NOT NULL, materials_json TEXT NOT NULL,
    input_files_json TEXT NOT NULL, execution_ids_json TEXT NOT NULL, results_json TEXT NOT NULL,
    binding_json TEXT, retained_json TEXT
  ) STRICT;
  CREATE TABLE executions (
    execution_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, revision INTEGER NOT NULL, state TEXT NOT NULL,
    operation_id TEXT NOT NULL, input_json TEXT NOT NULL, error_json TEXT, engine_run_id TEXT,
    imported_activity_id TEXT REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, retained_json TEXT
  ) STRICT;
  CREATE TABLE operations (
    operation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, method_json TEXT NOT NULL, fingerprint TEXT NOT NULL,
    receipt_json TEXT NOT NULL, reply_json TEXT NOT NULL, reply_error_json TEXT,
    connection_revision INTEGER REFERENCES connections(revision), retained_json TEXT
  ) STRICT;
  CREATE TABLE connections (
    revision INTEGER PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, source_id TEXT NOT NULL,
    configuration_json TEXT NOT NULL
  ) STRICT;
  PRAGMA user_version = 2;
`+"`"+`);
const profile = {
  activities: [],
  appliedRevision: null,
  bots: [
    {
      archived: false,
      botId: 'bot-a',
      currentConversationId: 'conv-a',
      profile: { codingTools: null, instructions: 'Always output concise bullet points.', name: 'Alpha', purpose: 'Audit releases', role: 'Auditor', workspace: '' },
      profileRevision: 1,
    },
    {
      archived: false,
      botId: 'bot-b',
      currentConversationId: 'conv-b',
      profile: { codingTools: null, instructions: 'Draft checklists.', name: 'Beta', purpose: 'Write checklists', role: 'Writer', workspace: '' },
      profileRevision: 1,
    },
  ],
  connections: [],
  conversations: [
    { botId: 'bot-a', conversationId: 'conv-a', draft: { materials: [], revision: 1, text: 'alpha draft' }, revision: 1 },
    { botId: 'bot-b', conversationId: 'conv-b', draft: { materials: [], revision: 1, text: 'beta draft' }, revision: 1 },
  ],
  executions: [],
  operations: [],
  savedRevision: null,
  version: 1,
};
const snapshot = Buffer.from(JSON.stringify(profile) + '\n');
const digest = createHash('sha256').update(snapshot).digest('hex');
db.exec('BEGIN IMMEDIATE');
profile.bots.forEach((b, i) => {
  db.prepare('INSERT INTO bots VALUES (?, ?, ?, ?, ?, ?, ?, ?)').run(
    JSON.stringify(b.botId), i, JSON.stringify(b.profile), b.profileRevision, 0, JSON.stringify(b.currentConversationId), null, null,
  );
});
profile.conversations.forEach((c, i) => {
  db.prepare('INSERT INTO conversations VALUES (?, ?, ?, ?, ?, ?, ?)').run(
    JSON.stringify(c.conversationId), i, JSON.stringify(c.botId), c.revision, JSON.stringify(c.draft), null, null,
  );
});
db.prepare('INSERT INTO meta (key, value) VALUES (?, ?)').run('snapshot_sha256', digest);
db.prepare('INSERT INTO meta (key, value) VALUES (?, ?)').run('saved_revision', 'null');
db.prepare('INSERT INTO meta (key, value) VALUES (?, ?)').run('applied_revision', 'null');
db.exec('COMMIT');
db.close();
writeFileSync(process.argv[2], snapshot, { mode: 0o600 });
`, opts.ProfilePath)

	proc := startProductProcess(t, opts)
	opened := proc.open(t, ctx)
	if len(opened.Snapshot.Bots) != 2 || opened.Snapshot.Bots[0].BotID != "bot-a" || opened.Snapshot.Bots[1].BotID != "bot-b" {
		t.Fatalf("v2->v3 open altered bots: %+v", opened.Snapshot.Bots)
	}
	schemaState := runNodeSQLite(t, opts.NodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const version = db.prepare('PRAGMA user_version').get().user_version;
const tables = db.prepare("SELECT name FROM sqlite_master WHERE type IN ('table')").all().map(r => r.name);
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.close();
console.log(JSON.stringify({
  version,
  fk,
  hasAll: ['bot_memory_rules', 'bot_work_logs', 'bot_work_logs_fts', 'bot_pending_learnings', 'bot_memory_meta'].every(n => tables.includes(n)),
}));
`)
	if !strings.Contains(schemaState, `"version":14`) || !strings.Contains(schemaState, `"fk":0`) || !strings.Contains(schemaState, `"hasAll":true`) {
		t.Fatalf("v2->v14 SQLite migration state = %s", schemaState)
	}

	const configuredKey = "custom-provider-key-xyz98765"
	savedConn, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-conn-save",
		Configuration: pi.ProductConnectionInput{
			APIKey: configuredKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI: pi.OpenAICompletions, BaseURL: "http://127.0.0.1:9/v1", Model: productModel,
				ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: workspace, MaxActiveTasks: 2,
			},
		},
	})
	if err != nil || savedConn.SavedRevision == nil {
		t.Fatalf("SaveConnection = %+v, %v", savedConn, err)
	}

	added, err := proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: "bot-a", OperationID: "op-rule-1"},
		Text:                   "Use UTC timestamps in release notes.",
	})
	if err != nil || added.BotID != "bot-a" || added.RulesRevision != 1 {
		t.Fatalf("AddMemoryRule(bot-a) = %+v, %v", added, err)
	}

	expectCode := func(opID, text, wantCode string) {
		t.Helper()
		_, err := proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
			ProductMemoryOperation: pi.ProductMemoryOperation{BotID: "bot-a", OperationID: opID},
			Text:                   text,
		})
		var remote *pi.RemoteError
		if !errors.As(err, &remote) || remote.Code != wantCode {
			t.Fatalf("AddMemoryRule(%q) = %v, want code %q", text, err, wantCode)
		}
	}
	expectCode("op-ex-cfg", "Remember to pass "+configuredKey+" when testing.", "memory_secret")
	expectCode("op-ex-sk", "Use token sk-live9876543210 for staging.", "memory_secret")
	expectCode("op-ex-bearer", "Header Bearer tok_abcdef123456 works.", "memory_secret")
	expectCode("op-ex-tmp-en", "Currently running the release check.", "memory_temporary")
	expectCode("op-ex-tmp-zh", "这次先跳过构建验证。", "memory_temporary")
	expectCode("op-ex-doc", "always output concise bullet points", "memory_duplicate")
	expectCode("op-ex-dup", "  USE utc timestamps in release notes! ", "memory_duplicate")
	expectCode("op-ex-big", strings.Repeat("a", 305), "memory_invalid")
	expectCode("op-ex-short", "x", "memory_invalid")

	memA, err := proc.client.GetBotMemory(ctx, "bot-a")
	if err != nil || len(memA.Rules) != 1 || memA.Rules[0].Text != "Use UTC timestamps in release notes." || memA.Rules[0].Source != "manual" {
		t.Fatalf("GetBotMemory(bot-a) = %+v, %v", memA, err)
	}
	memB, err := proc.client.GetBotMemory(ctx, "bot-b")
	if err != nil || len(memB.Rules) != 0 || len(memB.WorkLogs) != 0 || len(memB.PendingLearnings) != 0 {
		t.Fatalf("GetBotMemory(bot-b) leaked bot-a memory: %+v, %v", memB, err)
	}

	_, crossUpdateErr := proc.client.UpdateMemoryRule(ctx, pi.ProductMemoryUpdateRuleParams{
		ProductMemoryAddRuleParams: pi.ProductMemoryAddRuleParams{
			ProductMemoryOperation: pi.ProductMemoryOperation{BotID: "bot-b", OperationID: "op-cross-upd"},
			Text:                   "Cross bot rewrite attempt.",
		},
		RuleID: memA.Rules[0].RuleID,
	})
	var remoteErr *pi.RemoteError
	if !errors.As(crossUpdateErr, &remoteErr) || remoteErr.Code != "not_found" {
		t.Fatalf("cross-bot UpdateMemoryRule = %v, want not_found", crossUpdateErr)
	}
	_, crossDeleteErr := proc.client.DeleteMemoryRule(ctx, pi.ProductMemoryDeleteRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: "bot-b", OperationID: "op-cross-del"},
		RuleID:                 memA.Rules[0].RuleID,
	})
	if !errors.As(crossDeleteErr, &remoteErr) || remoteErr.Code != "not_found" {
		t.Fatalf("cross-bot DeleteMemoryRule = %v, want not_found", crossDeleteErr)
	}

	backupPath := filepath.Join(root, "memory-backup.json")
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatalf("BackupProfile = %v", err)
	}
	for _, path := range []string{dbPath, opts.ProfilePath, backupPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 0600", path, info.Mode().Perm())
		}
		if bytes.Contains(productRead(t, path), []byte(configuredKey)) {
			t.Fatalf("configured API key leaked into %s", path)
		}
	}

	outcome, err := proc.client.Shutdown(ctx)
	proc.closed = true
	proc.exit(t, ctx, outcome, true, err)

	if err := os.Remove(opts.ProfilePath); err != nil {
		t.Fatal(err)
	}
	proc2 := startProductProcess(t, opts)
	_ = proc2.open(t, ctx)
	reopenedA, err := proc2.client.GetBotMemory(ctx, "bot-a")
	if err != nil || reopenedA.RulesRevision != 1 || len(reopenedA.Rules) != 1 || reopenedA.Rules[0].Text != "Use UTC timestamps in release notes." {
		t.Fatalf("reopened bot-a memory = %+v, %v", reopenedA, err)
	}
	reopenedB, err := proc2.client.GetBotMemory(ctx, "bot-b")
	if err != nil || len(reopenedB.Rules) != 0 {
		t.Fatalf("reopened bot-b memory = %+v, %v", reopenedB, err)
	}
	outcome, err = proc2.client.Shutdown(ctx)
	proc2.closed = true
	proc2.exit(t, ctx, outcome, true, err)

	runNodeSQLite(t, opts.NodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec('PRAGMA user_version = 15');
db.close();
`)
	badClient, startErr := pi.StartProduct(opts)
	if startErr == nil {
		_, readyErr := badClient.Ready(ctx)
		if readyErr == nil {
			_, openErr := badClient.Open(ctx, []int{pi.ProductVersion})
			if openErr == nil {
				t.Fatal("worker admitted SQLite user_version=15")
			}
		}
		_, _ = badClient.Wait(ctx)
	}
	ruleCountAfter := runNodeSQLite(t, opts.NodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const row = db.prepare('SELECT COUNT(*) AS n FROM bot_memory_rules').get();
db.close();
console.log(row.n);
`)
	if ruleCountAfter != "1" {
		t.Fatalf("v5 refusal mutated bot_memory_rules count: %s", ruleCountAfter)
	}
}

func TestPiProductMemoryToolsZeroCacheBustReloadAndConsolidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	type capturedReq struct {
		bot    string
		system string
		tools  []string
		role   string
		last   string
	}
	var mu sync.Mutex
	var captured []capturedReq
	var listRulesPayload string
	var searchEnPayload string
	var searchZhPayload string
	var searchLitMatchPayload string
	var searchLitMissPayload string
	var searchDateMissPayload string
	var botBListPayload string
	var botBSearchPayload string
	var tempRuleID string
	botAStep := 0
	botBStep := 0

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		var sys string
		for _, m := range body.Messages {
			if m.Role == "system" || m.Role == "developer" {
				var s string
				if json.Unmarshal(m.Content, &s) == nil {
					sys += s
				} else {
					sys += string(m.Content)
				}
			}
		}
		toolNames := make([]string, 0, len(body.Tools))
		for _, tl := range body.Tools {
			toolNames = append(toolNames, tl.Function.Name)
		}
		last := body.Messages[len(body.Messages)-1]
		lastContent := string(last.Content)
		isActivity := slices.Contains(toolNames, "activity_ask")
		isBotA := strings.Contains(sys, "Bot Identity\nName: Alpha")
		botLabel := "bot-b"
		if isBotA {
			botLabel = "bot-a"
		}
		if isActivity {
			botLabel = "activity"
		}
		mu.Lock()
		captured = append(captured, capturedReq{bot: botLabel, system: sys, tools: toolNames, role: last.Role, last: lastContent})
		var step int
		if isActivity {
			step = 0
		} else if isBotA {
			botAStep++
			step = botAStep
		} else {
			botBStep++
			step = botBStep
		}
		mu.Unlock()

		if isActivity {
			piChatResponse(w, "Checked build artifacts and checksums.", "", "", "")
			return
		}
		if !isBotA {
			switch step {
			case 1:
				piChatResponse(w, "", "memory_list_rules", "b-list-1", `{}`)
			case 2:
				mu.Lock()
				botBListPayload = lastContent
				mu.Unlock()
				piChatResponse(w, "", "memory_search_log", "b-search-1", `{"query":"evidence"}`)
			case 3:
				mu.Lock()
				botBSearchPayload = lastContent
				mu.Unlock()
				piChatResponse(w, "Bot B isolated check done.", "", "", "")
			case 4:
				piChatResponse(w, "Noted your checksum preference.", "", "", "")
			case 5:
				piChatResponse(w, "", "activity_delegate", "b-del-1", `{"key":"verify-artifacts","goal":"Verify release artifacts"}`)
			default:
				piChatResponse(w, "Delegated artifact check.", "", "", "")
			}
			return
		}

		switch step {
		case 1:
			piChatResponse(w, "", "memory_save_rule", "a-save-1", `{"text":"Use UTC in release notes."}`)
		case 2:
			piChatResponse(w, "", "memory_save_rule", "a-save-2", `{"text":"Temporary throwaway rule to delete."}`)
		case 3:
			piChatResponse(w, "", "memory_propose_learning", "a-prop-1", `{"text":"Put decisions before caveats.","reason":"User asked for decision-first summaries."}`)
		case 4:
			piChatResponse(w, "", "memory_list_rules", "a-list-1", `{}`)
		case 5:
			var parsed struct {
				Rules []struct {
					RuleID string `json:"ruleId"`
					Text   string `json:"text"`
				} `json:"rules"`
			}
			var inner string
			if json.Unmarshal(last.Content, &inner) == nil {
				_ = json.Unmarshal([]byte(inner), &parsed)
			}
			mu.Lock()
			listRulesPayload = lastContent
			for _, rl := range parsed.Rules {
				if strings.Contains(rl.Text, "throwaway") {
					tempRuleID = rl.RuleID
				}
			}
			delID := tempRuleID
			mu.Unlock()
			args, _ := json.Marshal(map[string]any{"rule_id": delID})
			piChatResponse(w, "", "memory_forget_rule", "a-forget-1", string(args))
		case 6:
			piChatResponse(w, "", "memory_log_work", "a-log-1", `{"summary":"发布核验完成 100%_exact evidence recorded.","date":"2026-11-18","tags":["release","audit"]}`)
		case 7:
			piChatResponse(w, "", "memory_search_log", "a-s-en", `{"query":"evidence"}`)
		case 8:
			mu.Lock()
			searchEnPayload = lastContent
			mu.Unlock()
			piChatResponse(w, "", "memory_search_log", "a-s-zh", `{"query":"核验完成"}`)
		case 9:
			mu.Lock()
			searchZhPayload = lastContent
			mu.Unlock()
			piChatResponse(w, "", "memory_search_log", "a-s-lit-hit", `{"query":"100%_exact","date_from":"2026-11-18","date_to":"2026-11-18"}`)
		case 10:
			mu.Lock()
			searchLitMatchPayload = lastContent
			mu.Unlock()
			piChatResponse(w, "", "memory_search_log", "a-s-lit-miss", `{"query":"100X_exact"}`)
		case 11:
			mu.Lock()
			searchLitMissPayload = lastContent
			mu.Unlock()
			piChatResponse(w, "", "memory_search_log", "a-s-date-miss", `{"query":"evidence","date_from":"2026-11-19","date_to":"2026-11-20"}`)
		case 12:
			mu.Lock()
			searchDateMissPayload = lastContent
			mu.Unlock()
			piChatResponse(w, "Turn 1 all six memory tools done.", "", "", "")
		case 13:
			piChatResponse(w, "Turn 2 hot reply.", "", "", "")
		case 14:
			piChatResponse(w, "Turn 3 reloaded reply.", "", "", "")
		case 15:
			piChatResponse(w, "", "memory_save_rule", "a-cold-save-1", `{"text":"Prefer concise diffs."}`)
		case 16:
			piChatResponse(w, "Cold phase turn 1 done.", "", "", "")
		case 17:
			piChatResponse(w, "Cold phase turn 2 after TTL done.", "", "", "")
		case 18:
			piChatResponse(w, "", "memory_save_rule", "a-cold-save-2", `{"text":"Link every modified file."}`)
		case 19:
			piChatResponse(w, "Cold phase turn 3 before restart done.", "", "", "")
		default:
			piChatResponse(w, "Cold phase turn 4 after disk reopen done.", "", "", "")
		}
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
		ProfilePath:     filepath.Join(root, "profile", "contacts.json"),
		CredentialsPath: filepath.Join(root, "profile", "connection.json"),
		FreshProfile:    true,
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
			t.Fatalf("Send(%s) = %+v, %v", sendOp, receipt, err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			cur, err := p.client.GetConversation(ctx, convID)
			if err != nil {
				t.Fatal(err)
			}
			if cur.LatestExecution != nil && cur.LatestExecution.ExecutionID == receipt.Execution.ExecutionID && cur.LatestExecution.State == "completed" {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("Send(%s) timed out", sendOp)
	}

	proc := startProductProcess(t, opts)
	_ = proc.open(t, ctx)
	savedConn, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-conn-save",
		Configuration: pi.ProductConnectionInput{
			APIKey: productKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI: pi.OpenAICompletions, BaseURL: provider.URL + "/v1", Model: productModel,
				ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: workspace, MaxActiveTasks: 2,
			},
		},
	})
	if err != nil || savedConn.SavedRevision == nil {
		t.Fatalf("SaveConnection = %+v, %v", savedConn, err)
	}
	if _, err := proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{OperationID: "op-conn-apply", SavedRevision: *savedConn.SavedRevision}); err != nil {
		t.Fatalf("ApplyConnection = %v", err)
	}
	botA, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-create-a",
		Profile:     pi.ProductBotProfile{Name: "Alpha", Role: "Auditor", Purpose: "Audit releases", Instructions: "Stay focused."},
	})
	if err != nil {
		t.Fatal(err)
	}
	botB, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-create-b",
		Profile:     pi.ProductBotProfile{Name: "Beta", Role: "Writer", Purpose: "Write checklists", Instructions: "Stay brief."},
	})
	if err != nil {
		t.Fatal(err)
	}

	sendAndWait(proc, botA.CurrentConversationID, "Exercise all six memory tools.", "d-a1", "s-a1")
	sendAndWait(proc, botA.CurrentConversationID, "Hot follow-up right after save.", "d-a2", "s-a2")

	memA, err := proc.client.GetBotMemory(ctx, botA.BotID)
	if err != nil || len(memA.Rules) != 1 || len(memA.PendingLearnings) != 1 || len(memA.WorkLogs) != 1 {
		t.Fatalf("botA memory after turn 1 = %+v, %v", memA, err)
	}
	if _, err := proc.client.ReviewLearning(ctx, pi.ProductMemoryReviewLearningParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: botA.BotID, OperationID: "op-approve-1"},
		LearningID:             memA.PendingLearnings[0].LearningID,
		Decision:               "approve",
	}); err != nil {
		t.Fatalf("ReviewLearning(approve) = %v", err)
	}
	if _, err := proc.client.AddMemoryRule(ctx, pi.ProductMemoryAddRuleParams{
		ProductMemoryOperation: pi.ProductMemoryOperation{BotID: botA.BotID, OperationID: "op-manual-rule"},
		Text:                   "Cite commit SHAs.",
	}); err != nil {
		t.Fatalf("AddMemoryRule = %v", err)
	}

	sendAndWait(proc, botA.CurrentConversationID, "Send after explicit GUI rule approval and add.", "d-a3", "s-a3")

	histA, err := proc.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botA.CurrentConversationID},
		ProductPageParams:         pi.ProductPageParams{Limit: 20},
	})
	if err != nil || len(histA.Items) < 6 {
		t.Fatalf("botA transcript history lost turns across reload: count=%d err=%v", len(histA.Items), err)
	}

	sendAndWait(proc, botB.CurrentConversationID, "Verify Bot B memory isolation.", "d-b1", "s-b1")
	sendAndWait(proc, botB.CurrentConversationID, "From now on, always include checksums in summaries.", "d-b2", "s-b2")
	sendAndWait(proc, botB.CurrentConversationID, "Delegate an artifact check activity.", "d-b3", "s-b3")

	deadline := time.Now().Add(15 * time.Second)
	var consolidatedB pi.ProductBotMemory
	for time.Now().Before(deadline) {
		consolidatedB, err = proc.client.GetBotMemory(ctx, botB.BotID)
		if err == nil && len(consolidatedB.PendingLearnings) >= 1 && len(consolidatedB.WorkLogs) >= 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(consolidatedB.PendingLearnings) != 1 || !strings.Contains(consolidatedB.PendingLearnings[0].Text, "checksums") ||
		len(consolidatedB.WorkLogs) != 1 || !slices.Contains(consolidatedB.WorkLogs[0].Tags, "activity") {
		t.Fatalf("post-settlement consolidation missing learning or activity log: %+v", consolidatedB)
	}

	memEvents := drainMemoryChangedEvents(proc)
	if len(memEvents) < 4 {
		t.Fatalf("expected memory.changed events across tools/GUI/consolidation, got %+v", memEvents)
	}

	outcome, err := proc.client.Shutdown(ctx)
	proc.closed = true
	proc.exit(t, ctx, outcome, true, err)

	mu.Lock()
	var botAReqs []capturedReq
	var botBReqs []capturedReq
	for _, req := range captured {
		if !slices.IsSorted(req.tools) {
			t.Fatalf("tool list not sorted alphabetically: %v", req.tools)
		}
		if req.bot == "bot-a" {
			botAReqs = append(botAReqs, req)
		} else if req.bot == "bot-b" {
			botBReqs = append(botBReqs, req)
		}
	}
	if len(botAReqs) < 14 || len(botBReqs) < 3 {
		t.Fatalf("unexpected request counts: botA=%d botB=%d", len(botAReqs), len(botBReqs))
	}
	turn1Sys := botAReqs[0].system
	turn2HotSys := botAReqs[12].system
	turn3ReloadSys := botAReqs[13].system
	if turn2HotSys != turn1Sys {
		t.Fatalf("HOT turn busted KV cache after explicit memory_save_rule:\nturn1: %q\nturn2: %q", turn1Sys, turn2HotSys)
	}
	if !slices.Equal(botAReqs[0].tools, botAReqs[12].tools) || !slices.Equal(botAReqs[0].tools, botAReqs[13].tools) {
		t.Fatalf("tool descriptors mutated across turns: %v vs %v vs %v", botAReqs[0].tools, botAReqs[12].tools, botAReqs[13].tools)
	}
	l0A := strings.Split(turn1Sys, "\n\nBot Identity\n")[0]
	l0Reload := strings.Split(turn3ReloadSys, "\n\nBot Identity\n")[0]
	l0B := strings.Split(botBReqs[0].system, "\n\nBot Identity\n")[0]
	if l0A != l0Reload || l0A != l0B || !strings.HasPrefix(l0A, "You are a long-lived Nagi Bot teammate.") {
		t.Fatalf("L0 static prefix diverged across reload or Bots")
	}
	if strings.Contains(turn1Sys, "Lasting Rules:") || !strings.Contains(turn3ReloadSys, "Lasting Rules:") ||
		!strings.Contains(turn3ReloadSys, "- Use UTC in release notes.") ||
		!strings.Contains(turn3ReloadSys, "- Put decisions before caveats.") ||
		!strings.Contains(turn3ReloadSys, "- Cite commit SHAs.") ||
		strings.Contains(turn3ReloadSys, "throwaway") {
		t.Fatalf("reloaded L1.5 Lasting Rules mismatch:\nturn1: %q\nturn3: %q", turn1Sys, turn3ReloadSys)
	}
	unquote := func(raw string) string {
		var s string
		if json.Unmarshal([]byte(raw), &s) == nil {
			return s
		}
		return raw
	}
	if strings.Contains(botBReqs[0].system, "Use UTC in release notes.") || !strings.Contains(unquote(botBListPayload), `"rules":[]`) || !strings.Contains(unquote(botBSearchPayload), `"items":[]`) {
		t.Fatalf("Bot B saw Bot A memory: sys=%q list=%s search=%s", botBReqs[0].system, botBListPayload, botBSearchPayload)
	}
	if !strings.Contains(unquote(listRulesPayload), "Use UTC in release notes.") || !strings.Contains(unquote(listRulesPayload), `"pending_count":1`) ||
		!strings.Contains(unquote(searchEnPayload), "100%_exact") || !strings.Contains(unquote(searchZhPayload), "核验完成") ||
		!strings.Contains(unquote(searchLitMatchPayload), "100%_exact") || !strings.Contains(unquote(searchLitMissPayload), `"items":[]`) ||
		!strings.Contains(unquote(searchDateMissPayload), `"items":[]`) {
		t.Fatalf("memory search/list tool assertions failed: list=%s en=%s zh=%s litHit=%s litMiss=%s dateMiss=%s",
			listRulesPayload, searchEnPayload, searchZhPayload, searchLitMatchPayload, searchLitMissPayload, searchDateMissPayload)
	}
	mu.Unlock()

	t.Setenv("NAGI_MEMORY_COLD_TTL_MS", "10")
	procCold := startProductProcess(t, opts)
	_ = procCold.open(t, ctx)
	sendAndWait(procCold, botA.CurrentConversationID, "Save a cold-phase rule.", "d-c1", "s-c1")
	time.Sleep(40 * time.Millisecond)
	sendAndWait(procCold, botA.CurrentConversationID, "Send after cold TTL boundary.", "d-c2", "s-c2")
	sendAndWait(procCold, botA.CurrentConversationID, "Save a pre-restart rule.", "d-c3", "s-c3")
	outcome, err = procCold.client.Shutdown(ctx)
	procCold.closed = true
	procCold.exit(t, ctx, outcome, true, err)

	time.Sleep(40 * time.Millisecond)
	procReopen := startProductProcess(t, opts)
	_ = procReopen.open(t, ctx)
	sendAndWait(procReopen, botA.CurrentConversationID, "Send after cold disk reopen.", "d-c4", "s-c4")
	histAfterCold, err := procReopen.client.History(ctx, pi.ProductHistoryParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: botA.CurrentConversationID},
		ProductPageParams:         pi.ProductPageParams{Limit: 30},
	})
	if err != nil || len(histAfterCold.Items) < 14 {
		t.Fatalf("cold TTL / reopen lost transcript history: count=%d err=%v", len(histAfterCold.Items), err)
	}
	outcome, err = procReopen.client.Shutdown(ctx)
	procReopen.closed = true
	procReopen.exit(t, ctx, outcome, true, err)

	mu.Lock()
	defer mu.Unlock()
	botAReqs = nil
	for _, req := range captured {
		if req.bot == "bot-a" {
			botAReqs = append(botAReqs, req)
		}
	}
	afterTTLSys := botAReqs[16].system
	afterDiskReopenSys := botAReqs[len(botAReqs)-1].system
	if !strings.Contains(afterTTLSys, "- Prefer concise diffs.") || strings.Split(afterTTLSys, "\n\nBot Identity\n")[0] != l0A {
		t.Fatalf("cold TTL turn did not reload L1.5 cleanly: %q", afterTTLSys)
	}
	if !strings.Contains(afterDiskReopenSys, "- Link every modified file.") || strings.Split(afterDiskReopenSys, "\n\nBot Identity\n")[0] != l0A {
		t.Fatalf("cold disk reopen did not reload L1.5 cleanly: %q", afterDiskReopenSys)
	}
}
