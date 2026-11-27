//go:build integration

package pi_test

import (
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
	"unicode/utf8"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

func drainBotChangedEvents(proc *productProcess) []pi.ProductBotChangedEvent {
	var out []pi.ProductBotChangedEvent
	for {
		select {
		case ev := <-proc.events:
			if ev.Name == "bot.changed" {
				var changed pi.ProductBotChangedEvent
				if json.Unmarshal(ev.Data, &changed) == nil {
					out = append(out, changed)
				}
			}
		default:
			return out
		}
	}
}

func TestPiProductPandoraBoxSQLiteV1ToV2EnsureAndArchiveDenial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	root := t.TempDir()
	profileDir := filepath.Join(root, "profile")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
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
    retained_json TEXT
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
  PRAGMA user_version = 1;
`+"`"+`);
const profile = {
  activities: [],
  appliedRevision: null,
  bots: [{
    archived: false,
    botId: 'legacy-bot',
    currentConversationId: 'legacy-conv',
    profile: { codingTools: null, instructions: 'Keep legacy behavior.', name: 'Legacy', purpose: 'Old helper', role: 'Assistant', workspace: '' },
    profileRevision: 1,
  }],
  connections: [],
  conversations: [{
    botId: 'legacy-bot',
    conversationId: 'legacy-conv',
    draft: { materials: [], revision: 2, text: 'legacy unsent draft' },
    revision: 1,
  }],
  executions: [],
  operations: [],
  savedRevision: null,
  version: 1,
};
const snapshot = Buffer.from(JSON.stringify(profile) + '\n');
const digest = createHash('sha256').update(snapshot).digest('hex');
db.exec('BEGIN IMMEDIATE');
db.prepare('INSERT INTO bots VALUES (?, ?, ?, ?, ?, ?, ?)').run(
  JSON.stringify('legacy-bot'), 0, JSON.stringify(profile.bots[0].profile), 1, 0, JSON.stringify('legacy-conv'), null,
);
db.prepare('INSERT INTO conversations VALUES (?, ?, ?, ?, ?, ?, ?)').run(
  JSON.stringify('legacy-conv'), 0, JSON.stringify('legacy-bot'), 1, JSON.stringify(profile.conversations[0].draft), null, null,
);
db.prepare('INSERT INTO meta (key, value) VALUES (?, ?)').run('snapshot_sha256', digest);
db.prepare('INSERT INTO meta (key, value) VALUES (?, ?)').run('saved_revision', 'null');
db.prepare('INSERT INTO meta (key, value) VALUES (?, ?)').run('applied_revision', 'null');
db.exec('COMMIT');
db.close();
writeFileSync(process.argv[2], snapshot, { mode: 0o600 });
`, opts.ProfilePath)

	proc := startProductProcess(t, opts)
	opened := proc.open(t, ctx)
	if len(opened.Snapshot.Bots) != 1 || opened.Snapshot.Bots[0].BotID != "legacy-bot" {
		t.Fatalf("raw app.open changed saved bot count or identity: %+v", opened.Snapshot.Bots)
	}
	legacyConv, err := proc.client.GetConversation(ctx, "legacy-conv")
	if err != nil || legacyConv.Draft.Text != "legacy unsent draft" {
		t.Fatalf("v1->v2 migration lost legacy draft: %+v, %v", legacyConv, err)
	}
	versionAfter := runNodeSQLite(t, opts.NodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const version = db.prepare('PRAGMA user_version').get().user_version;
const cols = db.prepare('PRAGMA table_info(bots)').all().map(c => c.name);
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.close();
console.log(JSON.stringify({ version, hasSystemKind: cols.includes('system_kind'), fk }));
`)
	if !strings.Contains(versionAfter, `"version":12`) || !strings.Contains(versionAfter, `"hasSystemKind":true`) || !strings.Contains(versionAfter, `"fk":0`) {
		t.Fatalf("v1->v12 SQLite upgrade state = %s", versionAfter)
	}

	first, err := proc.client.EnsureBuiltinBot(ctx, pi.ProductBotEnsureBuiltinParams{
		Kind: pi.SystemKindBotArchitect, OperationID: "op-ensure-1",
	})
	if err != nil {
		t.Fatalf("EnsureBuiltinBot failed: %v", err)
	}
	if first.BotID != "builtin:pandorabox" || first.SystemKind != pi.SystemKindBotArchitect || first.ProfileRevision != 1 || first.Profile.CodingTools == nil || len(first.Profile.CodingTools) != 0 {
		t.Fatalf("ensured builtin bot = %+v", first)
	}
	replay, err := proc.client.EnsureBuiltinBot(ctx, pi.ProductBotEnsureBuiltinParams{
		Kind: pi.SystemKindBotArchitect, OperationID: "op-ensure-1",
	})
	if err != nil || replay.BotID != first.BotID || replay.CurrentConversationID != first.CurrentConversationID {
		t.Fatalf("same-op EnsureBuiltinBot replay mismatch: %+v, %v", replay, err)
	}
	secondOp, err := proc.client.EnsureBuiltinBot(ctx, pi.ProductBotEnsureBuiltinParams{
		Kind: pi.SystemKindBotArchitect, OperationID: "op-ensure-2",
	})
	if err != nil || secondOp.BotID != first.BotID || secondOp.ProfileRevision != 1 {
		t.Fatalf("new-op EnsureBuiltinBot did not reuse existing builtin: %+v, %v", secondOp, err)
	}
	listed, err := proc.client.ListBots(ctx, pi.ProductPageParams{})
	if err != nil || len(listed.Items) != 2 || listed.Items[0].BotID != "builtin:pandorabox" || listed.Items[1].BotID != "legacy-bot" {
		t.Fatalf("ListBots after ensure = %+v, %v", listed.Items, err)
	}

	_, archiveErr := proc.client.ArchiveBot(ctx, pi.ProductBotRevisionParams{
		BotID: first.BotID, ExpectedRevision: first.ProfileRevision, OperationID: "op-archive-builtin",
	})
	var remoteErr *pi.RemoteError
	if !errors.As(archiveErr, &remoteErr) || remoteErr.Code != "scope_denied" {
		t.Fatalf("ArchiveBot on builtin = %v, want scope_denied", archiveErr)
	}

	updatedProfile := first.Profile
	updatedProfile.Purpose = "Design and refine team Bots."
	edited, err := proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			BotID: first.BotID, ExpectedRevision: first.ProfileRevision, OperationID: "op-edit-builtin",
		},
		Profile: updatedProfile,
	})
	if err != nil || edited.ProfileRevision != 2 || edited.SystemKind != pi.SystemKindBotArchitect {
		t.Fatalf("UpdateBot on builtin = %+v, %v", edited, err)
	}

	backupPath := filepath.Join(root, "pandora-backup.json")
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatalf("BackupProfile failed: %v", err)
	}
	for _, path := range []string{dbPath, opts.ProfilePath, backupPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 0600", path, info.Mode().Perm())
		}
	}

	outcome, err := proc.client.Shutdown(ctx)
	proc.closed = true
	proc.exit(t, ctx, outcome, true, err)

	botEvents := drainBotChangedEvents(proc)
	createdSeen, updatedSeen := false, false
	for _, ev := range botEvents {
		if ev.Bot.BotID == first.BotID && ev.Change == "created" {
			createdSeen = true
		}
		if ev.Bot.BotID == first.BotID && ev.Change == "updated" && ev.Bot.ProfileRevision == 2 {
			updatedSeen = true
		}
	}
	if !createdSeen || !updatedSeen {
		t.Fatalf("expected bot.changed created+updated events for builtin: %+v", botEvents)
	}

	if err := os.Remove(opts.ProfilePath); err != nil {
		t.Fatal(err)
	}
	proc2 := startProductProcess(t, opts)
	reopened := proc2.open(t, ctx)
	if len(reopened.Snapshot.Bots) != 2 || reopened.Snapshot.Bots[0].BotID != "builtin:pandorabox" ||
		reopened.Snapshot.Bots[0].SystemKind != pi.SystemKindBotArchitect || reopened.Snapshot.Bots[0].ProfileRevision != 2 ||
		reopened.Snapshot.Bots[0].Profile.Purpose != "Design and refine team Bots." {
		t.Fatalf("reopened builtin bot state = %+v", reopened.Snapshot.Bots)
	}
	outcome, err = proc2.client.Shutdown(ctx)
	proc2.closed = true
	proc2.exit(t, ctx, outcome, true, err)
}

func TestPiProductPandoraBoxKVCachingToolsAndMutations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	type capturedReq struct {
		system string
		tools  []string
		role   string
		last   string
	}
	var mu sync.Mutex
	var captured []capturedReq
	var createdBotID string
	var createdRev uint64
	var inspectPayload string
	var builtinUpdateResult string
	var archivedUpdateResult string
	architectStep := 0
	ordinaryStep := 0

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role       string          `json:"role"`
				Content    json.RawMessage `json:"content"`
				ToolCallID string          `json:"tool_call_id"`
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
		mu.Lock()
		captured = append(captured, capturedReq{system: sys, tools: toolNames, role: last.Role, last: lastContent})
		isPandora := strings.Contains(sys, "Bot Identity\nName: PandoraBox")
		isActivity := slices.Contains(toolNames, "activity_ask")
		var step int
		if isPandora && !isActivity {
			architectStep++
			step = architectStep
		} else if !isPandora && !isActivity {
			ordinaryStep++
			step = ordinaryStep
		}
		mu.Unlock()

		if isActivity {
			piChatResponse(w, "ACTIVITY_DONE", "", "", "")
			return
		}
		if !isPandora {
			switch step {
			case 1:
				piChatResponse(w, "", "activity_delegate", "call-delegate-1", `{"key":"check-1","goal":"Run isolated activity check"}`)
			default:
				piChatResponse(w, "Ordinary turn complete.", "", "", "")
			}
			return
		}

		switch step {
		case 1:
			piChatResponse(w, "", "bot_create", "call-create-1", `{"name":"Release Auditor","purpose":"Audit release checklists","role":"Auditor","job":"Verify release checklists before merge","voice":"Calm, direct, evidence-first","anti_jobs":["Never claim tests passed without running them","Never modify production files"],"workflow":"Inspect -> Verify -> Report"}`)
		case 2:
			var parsed struct {
				BotID string `json:"bot_id"`
			}
			var text string
			if json.Unmarshal(last.Content, &text) == nil {
				_ = json.Unmarshal([]byte(text), &parsed)
			}
			mu.Lock()
			createdBotID = parsed.BotID
			mu.Unlock()
			piChatResponse(w, "", "bot_inspect_team", "call-inspect-1", `{}`)
		case 3:
			mu.Lock()
			inspectPayload = lastContent
			mu.Unlock()
			piChatResponse(w, "", "bot_update", "call-update-builtin", `{"bot_id":"builtin:pandorabox","purpose":"Unauthorized rewrite"}`)
		case 4:
			mu.Lock()
			builtinUpdateResult = lastContent
			targetID := createdBotID
			mu.Unlock()
			args, _ := json.Marshal(map[string]any{"bot_id": targetID, "workflow": "Read diff -> Run checks -> Verdict"})
			piChatResponse(w, "", "bot_update", "call-update-partial", string(args))
		case 5:
			var parsed struct {
				ProfileRevision uint64 `json:"profile_revision"`
			}
			var text string
			if json.Unmarshal(last.Content, &text) == nil {
				_ = json.Unmarshal([]byte(text), &parsed)
			}
			mu.Lock()
			createdRev = parsed.ProfileRevision
			mu.Unlock()
			piChatResponse(w, "Release Auditor created and refined.", "", "", "")
		case 6:
			mu.Lock()
			targetID := createdBotID
			mu.Unlock()
			args, _ := json.Marshal(map[string]any{"bot_id": targetID, "purpose": "Edit archived bot"})
			piChatResponse(w, "", "bot_update", "call-update-archived", string(args))
		default:
			mu.Lock()
			archivedUpdateResult = lastContent
			mu.Unlock()
			piChatResponse(w, "Cannot update archived bot.", "", "", "")
		}
	}))
	t.Cleanup(provider.Close)

	root := t.TempDir()
	freshProfileDir := filepath.Join(root, "fresh-profile")
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opts := pi.ProductOptions{
		NodePath:        os.Getenv("NAGI_TEST_NODE"),
		WorkerPath:      os.Getenv("NAGI_TEST_WORKER"),
		StateDir:        filepath.Join(root, "pi"),
		ProfilePath:     filepath.Join(freshProfileDir, "contacts.json"),
		CredentialsPath: filepath.Join(freshProfileDir, "connection.json"),
		FreshProfile:    true,
	}

	proc := startProductProcess(t, opts)
	_ = proc.open(t, ctx)

	dirInfo, err := os.Stat(freshProfileDir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("fresh profile dir mode = %o, want 0700", dirInfo.Mode().Perm())
	}

	cfg := pi.ProductConnectionInput{
		APIKey: productKey,
		ProductConnectionSettings: pi.ProductConnectionSettings{
			WireAPI: pi.OpenAICompletions, BaseURL: provider.URL + "/v1", Model: productModel,
			ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: workspace, MaxActiveTasks: 2,
		},
	}
	saved, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{OperationID: "op-conn-save", Configuration: cfg})
	if err != nil || saved.SavedRevision == nil {
		t.Fatalf("SaveConnection = %+v, %v", saved, err)
	}
	if _, err := proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{OperationID: "op-conn-apply", SavedRevision: *saved.SavedRevision}); err != nil {
		t.Fatalf("ApplyConnection = %v", err)
	}

	pandora, err := proc.client.EnsureBuiltinBot(ctx, pi.ProductBotEnsureBuiltinParams{
		Kind: pi.SystemKindBotArchitect, OperationID: "op-ensure-pb",
	})
	if err != nil {
		t.Fatalf("EnsureBuiltinBot = %v", err)
	}
	ordinary, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-create-ordinary",
		Profile:     pi.ProductBotProfile{Name: "Ordinary Helper", Role: "Helper", Purpose: "Assist with coding", Instructions: "Stay concise."},
	})
	if err != nil {
		t.Fatalf("CreateBot(ordinary) = %v", err)
	}

	sendAndWait := func(convID, text, draftOp, sendOp string) {
		t.Helper()
		conv, err := proc.client.GetConversation(ctx, convID)
		if err != nil {
			t.Fatal(err)
		}
		draft, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
			ProductText:               pi.ProductText{Text: text},
			ExpectedRevision:          conv.Draft.Revision,
			OperationID:               draftOp,
		})
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := proc.client.Send(ctx, pi.ProductSendParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
			DraftRevision:             draft.Revision,
			OperationID:               sendOp,
		})
		if err != nil || receipt.Execution == nil {
			t.Fatalf("Send(%s) = %+v, %v", sendOp, receipt, err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			cur, err := proc.client.GetConversation(ctx, convID)
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

	sendAndWait(pandora.CurrentConversationID, "Design and create Release Auditor", "d-pb-1", "s-pb-1")
	sendAndWait(ordinary.CurrentConversationID, "Run a check activity", "d-ord-1", "s-ord-1")

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		acts, err := proc.client.ListActivities(ctx, pi.ProductActivityListParams{BotID: ordinary.BotID})
		if err == nil && len(acts.Items) == 1 && (acts.Items[0].State == "needsAttention" || acts.Items[0].State == "completed") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	mu.Lock()
	targetID := createdBotID
	targetRev := createdRev
	mu.Unlock()
	if targetID == "" || targetRev != 2 {
		t.Fatalf("architect created/updated target = id:%q rev:%d", targetID, targetRev)
	}

	listed, err := proc.client.ListBots(ctx, pi.ProductPageParams{})
	if err != nil {
		t.Fatal(err)
	}
	var auditor *pi.ProductBot
	for i := range listed.Items {
		if listed.Items[i].BotID == targetID {
			auditor = &listed.Items[i]
		}
	}
	if auditor == nil {
		t.Fatalf("created bot %q not found in ListBots: %+v", targetID, listed.Items)
	}
	if auditor.Profile.Name != "Release Auditor" || auditor.ProfileRevision != 2 ||
		!strings.Contains(auditor.Profile.Instructions, "Job:\nVerify release checklists before merge") ||
		!strings.Contains(auditor.Profile.Instructions, "Voice:\nCalm, direct, evidence-first") ||
		!strings.Contains(auditor.Profile.Instructions, "- Never claim tests passed without running them") ||
		!strings.Contains(auditor.Profile.Instructions, "Workflow:\nRead diff -> Run checks -> Verdict") {
		t.Fatalf("partial bot_update did not preserve structured sections: %+v", auditor.Profile)
	}

	if _, err := proc.client.ArchiveBot(ctx, pi.ProductBotRevisionParams{
		BotID: targetID, ExpectedRevision: auditor.ProfileRevision, OperationID: "op-archive-auditor",
	}); err != nil {
		t.Fatalf("ArchiveBot(auditor) = %v", err)
	}
	sendAndWait(pandora.CurrentConversationID, "Try updating archived auditor", "d-pb-2", "s-pb-2")

	outcome, err := proc.client.Shutdown(ctx)
	proc.closed = true
	proc.exit(t, ctx, outcome, true, err)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(inspectPayload, "Release Auditor") || !strings.Contains(inspectPayload, "bot_architect") {
		t.Fatalf("bot_inspect_team payload missing saved profiles: %s", inspectPayload)
	}
	if !strings.Contains(builtinUpdateResult, "scope_denied") {
		t.Fatalf("bot_update on builtin was not denied: %s", builtinUpdateResult)
	}
	if !strings.Contains(archivedUpdateResult, "scope_denied") {
		t.Fatalf("bot_update on archived bot was not denied: %s", archivedUpdateResult)
	}

	var convL0Prefix, actL0Prefix string
	var pandoraTools []string
	var ordinaryChecked, activityChecked bool
	for idx, req := range captured {
		if !slices.IsSorted(req.tools) {
			t.Fatalf("request[%d] tools not sorted alphabetically: %v", idx, req.tools)
		}
		parts := strings.Split(req.system, "\n\nBot Identity\n")
		if len(parts) < 2 {
			t.Fatalf("request[%d] missing L0/L1 Bot Identity boundary: %q", idx, req.system)
		}
		isActivity := slices.Contains(req.tools, "activity_ask")
		if isActivity {
			if actL0Prefix == "" {
				actL0Prefix = parts[0]
				if !strings.HasPrefix(actL0Prefix, "You are a long-lived Nagi Bot teammate.") || strings.Contains(actL0Prefix, "PandoraBox") || strings.Contains(actL0Prefix, workspace) || strings.Contains(actL0Prefix, "Use activity_delegate") {
					t.Fatalf("Activity L0 prefix is not static or leaked foreground/dynamic content: %q", actL0Prefix)
				}
			} else if parts[0] != actL0Prefix {
				t.Fatalf("activity request[%d] L0 prefix diverged (KV cache break):\ngot:  %q\nwant: %q", idx, parts[0], actL0Prefix)
			}
		} else {
			if convL0Prefix == "" {
				convL0Prefix = parts[0]
				if !strings.HasPrefix(convL0Prefix, "You are a long-lived Nagi Bot teammate.") || strings.Contains(convL0Prefix, "PandoraBox") || strings.Contains(convL0Prefix, workspace) || !strings.Contains(convL0Prefix, "Use activity_delegate") {
					t.Fatalf("Conversation L0 prefix is not static or missing foreground guidance: %q", convL0Prefix)
				}
			} else if parts[0] != convL0Prefix {
				t.Fatalf("conversation request[%d] L0 prefix diverged (KV cache break):\ngot:  %q\nwant: %q", idx, parts[0], convL0Prefix)
			}
		}
		if convL0Prefix != "" && actL0Prefix != "" && !strings.HasPrefix(convL0Prefix, actL0Prefix+"\n\n") {
			t.Fatalf("Conversation L0 prefix does not extend static master L0 base:\nconv=%q\nact=%q", convL0Prefix, actL0Prefix)
		}

		isPandora := strings.Contains(req.system, "Bot Identity\nName: PandoraBox")
		if isActivity {
			activityChecked = true
			for _, forbidden := range []string{"bot_create", "bot_inspect_team", "bot_update"} {
				if slices.Contains(req.tools, forbidden) {
					t.Fatalf("activity request[%d] exposed architect tool %q: %v", idx, forbidden, req.tools)
				}
			}
		} else if isPandora {
			for _, required := range []string{"bot_create", "bot_inspect_team", "bot_update"} {
				if !slices.Contains(req.tools, required) {
					t.Fatalf("PandoraBox request[%d] missing architect tool %q: %v", idx, required, req.tools)
				}
			}
			for _, coding := range []string{"bash", "edit", "read", "write"} {
				if slices.Contains(req.tools, coding) {
					t.Fatalf("PandoraBox request[%d] exposed coding tool %q: %v", idx, coding, req.tools)
				}
			}
			if !strings.Contains(req.system, "You are PandoraBox, Nagi's built-in Bot Architect.") {
				t.Fatalf("PandoraBox request[%d] missing Architect instructions", idx)
			}
			if pandoraTools == nil {
				pandoraTools = req.tools
			} else if !slices.Equal(pandoraTools, req.tools) {
				t.Fatalf("PandoraBox tool list mutated mid-session: %v vs %v", pandoraTools, req.tools)
			}
		} else {
			ordinaryChecked = true
			if strings.Contains(req.system, "You are PandoraBox, Nagi's built-in Bot Architect.") {
				t.Fatalf("ordinary Bot request[%d] leaked Architect instructions", idx)
			}
			for _, forbidden := range []string{"bot_create", "bot_inspect_team", "bot_update"} {
				if slices.Contains(req.tools, forbidden) {
					t.Fatalf("ordinary Bot request[%d] exposed architect tool %q: %v", idx, forbidden, req.tools)
				}
			}
		}
	}
	if convL0Prefix == "" || actL0Prefix == "" || pandoraTools == nil || !ordinaryChecked || !activityChecked {
		t.Fatalf("incomplete coverage across scopes: convL0=%v actL0=%v pandora=%v ordinary=%v activity=%v", convL0Prefix != "", actL0Prefix != "", pandoraTools != nil, ordinaryChecked, activityChecked)
	}

	botEvents := drainBotChangedEvents(proc)
	var sawAuditorCreate, sawAuditorUpdate, sawAuditorArchive bool
	for _, ev := range botEvents {
		if ev.Bot.BotID == targetID {
			switch ev.Change {
			case "created":
				sawAuditorCreate = true
			case "updated":
				if ev.Bot.ProfileRevision == 2 {
					sawAuditorUpdate = true
				}
			case "archived":
				sawAuditorArchive = ev.Bot.Archived
			}
		}
	}
	if !sawAuditorCreate || !sawAuditorUpdate || !sawAuditorArchive {
		t.Fatalf("missing bot.changed lifecycle events for specialist: create=%v update=%v archive=%v all=%+v",
			sawAuditorCreate, sawAuditorUpdate, sawAuditorArchive, botEvents)
	}
}

func TestPiProductPandoraBoxStarterPromptsAndVisibility(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var mu sync.Mutex
	var createdID string
	var inspectPayload string
	step := 0

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		last := body.Messages[len(body.Messages)-1]
		mu.Lock()
		step++
		cur := step
		mu.Unlock()
		switch cur {
		case 1:
			piChatResponse(w, "", "bot_create", "call-sp-create", `{"name":"Prompt Specialist","purpose":"Craft concise checklists","job":"Draft concise checklists","voice":"Direct","anti_jobs":["No fluff"],"starter_prompts":["  Draft checklist  ","","Draft checklist","Review release notes","Check rollback plan","Audit test coverage","Fifth prompt dropped"]}`)
		case 2:
			var parsed struct {
				BotID string `json:"bot_id"`
			}
			var inner string
			if json.Unmarshal(last.Content, &inner) == nil {
				_ = json.Unmarshal([]byte(inner), &parsed)
			}
			mu.Lock()
			createdID = parsed.BotID
			mu.Unlock()
			piChatResponse(w, "", "bot_inspect_team", "call-sp-inspect", `{}`)
		case 3:
			mu.Lock()
			inspectPayload = string(last.Content)
			targetID := createdID
			mu.Unlock()
			longMultibyte := strings.Repeat("审查", 19) + "🚀" + strings.Repeat("端", 10)
			args, _ := json.Marshal(map[string]any{
				"bot_id":          targetID,
				"starter_prompts": []string{longMultibyte, "  Updated second prompt  "},
			})
			piChatResponse(w, "", "bot_update", "call-sp-update", string(args))
		default:
			piChatResponse(w, "Prompt Specialist created and updated.", "", "", "")
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
	proc := startProductProcess(t, opts)
	_ = proc.open(t, ctx)

	saved, err := proc.client.SaveConnection(ctx, pi.ProductConnectionSaveParams{
		OperationID: "op-sp-conn-save",
		Configuration: pi.ProductConnectionInput{
			APIKey: productKey,
			ProductConnectionSettings: pi.ProductConnectionSettings{
				WireAPI: pi.OpenAICompletions, BaseURL: provider.URL + "/v1", Model: productModel,
				ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: workspace, MaxActiveTasks: 2,
			},
		},
	})
	if err != nil || saved.SavedRevision == nil {
		t.Fatalf("SaveConnection = %+v, %v", saved, err)
	}
	if _, err := proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{OperationID: "op-sp-conn-apply", SavedRevision: *saved.SavedRevision}); err != nil {
		t.Fatalf("ApplyConnection = %v", err)
	}

	pandora, err := proc.client.EnsureBuiltinBot(ctx, pi.ProductBotEnsureBuiltinParams{
		Kind: pi.SystemKindBotArchitect, OperationID: "op-sp-ensure",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pandora.Profile.StarterPrompts) != 3 {
		t.Fatalf("PandoraBox default starterPrompts = %q, want 3", pandora.Profile.StarterPrompts)
	}

	longUTF8 := strings.Repeat("测试", 25) + "🚀tail"
	direct, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-sp-direct",
		Profile: pi.ProductBotProfile{
			Name:    "Direct Bot",
			Purpose: "Verify starter normalization",
			StarterPrompts: []string{
				"  First prompt  ",
				"   ",
				"First prompt",
				longUTF8,
				"Third prompt",
				"Fourth prompt",
				"Fifth dropped",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(direct.Profile.StarterPrompts) != 4 || direct.Profile.StarterPrompts[0] != "First prompt" ||
		direct.Profile.StarterPrompts[2] != "Third prompt" || direct.Profile.StarterPrompts[3] != "Fourth prompt" {
		t.Fatalf("normalized direct starterPrompts = %q", direct.Profile.StarterPrompts)
	}
	cut := direct.Profile.StarterPrompts[1]
	if !utf8.ValidString(cut) || len(cut) == 0 || len(cut) > 120 {
		t.Fatalf("UTF-8 120-byte truncation invalid: valid=%v bytes=%d text=%q", utf8.ValidString(cut), len(cut), cut)
	}

	conv, err := proc.client.GetConversation(ctx, pandora.CurrentConversationID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: pandora.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "Create and update Prompt Specialist"},
		ExpectedRevision:          conv.Draft.Revision,
		OperationID:               "d-sp-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := proc.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: pandora.CurrentConversationID},
		DraftRevision:             draft.Revision,
		OperationID:               "s-sp-1",
	})
	if err != nil || receipt.Execution == nil {
		t.Fatalf("Send = %+v, %v", receipt, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := proc.client.GetConversation(ctx, pandora.CurrentConversationID)
		if err != nil {
			t.Fatal(err)
		}
		if cur.LatestExecution != nil && cur.LatestExecution.ExecutionID == receipt.Execution.ExecutionID && cur.LatestExecution.State == "completed" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	mu.Lock()
	targetID := createdID
	inspected := inspectPayload
	mu.Unlock()
	if targetID == "" || !strings.Contains(inspected, "starter_prompts") || !strings.Contains(inspected, "Draft checklist") || strings.Contains(inspected, "Fifth prompt dropped") {
		t.Fatalf("bot_inspect_team starter_prompts mismatch: id=%q payload=%s", targetID, inspected)
	}

	dbPath := filepath.Join(root, "profile", "nagi.db")
	version := runNodeSQLite(t, opts.NodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
console.log(db.prepare('PRAGMA user_version').get().user_version);
db.close();
`)
	if version != "12" {
		t.Fatalf("SQLite schema version = %q, want 12", version)
	}

	backupPath := filepath.Join(root, "sp-backup.json")
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	outcome, err := proc.client.Shutdown(ctx)
	proc.closed = true
	proc.exit(t, ctx, outcome, true, err)

	if err := os.Remove(opts.ProfilePath); err != nil {
		t.Fatal(err)
	}
	proc2 := startProductProcess(t, opts)
	reopened := proc2.open(t, ctx)
	var foundSpec *pi.ProductBot
	for i := range reopened.Snapshot.Bots {
		if reopened.Snapshot.Bots[i].BotID == targetID {
			foundSpec = &reopened.Snapshot.Bots[i]
		}
	}
	if foundSpec == nil || len(foundSpec.Profile.StarterPrompts) != 2 || foundSpec.Profile.StarterPrompts[1] != "Updated second prompt" {
		t.Fatalf("reopened specialist starterPrompts = %+v", foundSpec)
	}
	if !utf8.ValidString(foundSpec.Profile.StarterPrompts[0]) || len(foundSpec.Profile.StarterPrompts[0]) > 120 {
		t.Fatalf("reopened UTF-8 starter prompt exceeded 120 bytes: %q (%d)", foundSpec.Profile.StarterPrompts[0], len(foundSpec.Profile.StarterPrompts[0]))
	}
	outcome, err = proc2.client.Shutdown(ctx)
	proc2.closed = true
	proc2.exit(t, ctx, outcome, true, err)
}
