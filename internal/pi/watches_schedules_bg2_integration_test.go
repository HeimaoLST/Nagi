//go:build integration

package pi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

const bg2PrecheckFixtureSource = `#define _DARWIN_C_SOURCE
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <spawn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <unistd.h>

static int denied_read(const char *path) {
    int fd = open(path, O_RDONLY);
    if (fd >= 0) {
        close(fd);
        return 0;
    }
    return errno == EACCES || errno == EPERM;
}

static int check_isolation(char **argv) {
    int read_conn = denied_read(argv[2]);
    int read_vault = denied_read(argv[3]);
    int read_ws_blocked = strcmp(argv[5], "noread") == 0 ? denied_read("state.txt") : !denied_read("state.txt");
    int fd = open("should-never-exist", O_WRONLY | O_CREAT, 0600);
    int write_blocked = fd < 0 && (errno == EACCES || errno == EPERM);
    if (fd >= 0) {
        close(fd);
    }
    const char *tmp = getenv("TMPDIR");
    char scratch[4096];
    if (!tmp || snprintf(scratch, sizeof(scratch), "%s/own-output", tmp) >= (int)sizeof(scratch)) {
        return 4;
    }
    fd = open(scratch, O_WRONLY | O_CREAT, 0600);
    int scratch_ok = fd >= 0;
    if (fd >= 0) {
        close(fd);
    }
    struct sockaddr_in local = {
        .sin_len = sizeof(local),
        .sin_family = AF_INET,
        .sin_port = htons((unsigned short)atoi(argv[4])),
        .sin_addr = {.s_addr = htonl(INADDR_LOOPBACK)}
    };
    int sock = socket(AF_INET, SOCK_STREAM, 0);
    int net_blocked = sock < 0 ? (errno == EPERM || errno == EACCES) :
        connect(sock, (struct sockaddr *)&local, sizeof(local)) < 0 && (errno == EPERM || errno == EACCES);
    if (sock >= 0) {
        close(sock);
    }
    pid_t launched = 0;
    char *const args[] = {"/usr/bin/true", NULL};
    int spawn_rc = posix_spawn(&launched, args[0], NULL, NULL, args, NULL);
    if (spawn_rc == 0) {
        waitpid(launched, NULL, 0);
    }
    int clean_env = !getenv("NAGI_TEST_CLOCK_FILE") && !getenv("NAGI_BG2_ENV_SECRET") && !getenv("OPENAI_API_KEY");
    printf("isolation=ok conn=%d vault=%d wsmode=%d write=%d scratch=%d net=%d spawn=%d env=%d\n",
        read_conn, read_vault, read_ws_blocked, write_blocked, scratch_ok, net_blocked, spawn_rc != 0, clean_env);
    return (read_conn && read_vault && read_ws_blocked && write_blocked && scratch_ok && net_blocked && spawn_rc != 0 && clean_env) ? 0 : 5;
}

int main(int argc, char **argv) {
    if (argc == 6 && strcmp(argv[1], "isolation") == 0) {
        return check_isolation(argv);
    }
    FILE *input = fopen("state.txt", "r");
    if (!input) {
        return 6;
    }
    int mode = fgetc(input);
    fclose(input);
    if (mode == 'T' || mode == 'S') {
        const char *tmp = getenv("TMPDIR");
        char marker[4096];
        if (!tmp || snprintf(marker, sizeof(marker), "%s/pid", tmp) >= (int)sizeof(marker)) {
            return 10;
        }
        FILE *file = fopen(marker, "w");
        if (!file) {
            return 11;
        }
        fprintf(file, "%ld\n", (long)getpid());
        fclose(file);
        sleep(30);
        return 12;
    }
    if (mode == 'F') {
        fputs("fixed failure\n", stderr);
        return 9;
    }
    char data[1024];
    memset(data, 'x', sizeof(data));
    for (int i = 0; i < 72; i++) {
        fwrite(data, sizeof(data), 1, stdout);
    }
    printf("tail %c\n", mode);
    return 0;
}
`

func bg2CompilePrecheckFixture(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "precheck-observer.c")
	bin := filepath.Join(dir, "precheck-observer")
	if err := os.WriteFile(src, []byte(bg2PrecheckFixtureSource), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/usr/bin/xcrun", "clang", "-std=c11", "-O2", "-Wall", "-Wextra", "-Werror", src, "-o", bin).CombinedOutput()
	if err != nil {
		t.Fatalf("compile bg2 precheck fixture: %v\n%s", err, out)
	}
	return bin
}

func bg2WriteClock(t *testing.T, path string, ms int64) {
	t.Helper()
	tmp := path + ".tmp"
	payload := fmt.Sprintf(`{"nowMs":%d}`, ms)
	if err := os.WriteFile(tmp, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func bg2ParseISO(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("parse ISO %q: %v", value, err)
	}
	return parsed.UnixMilli()
}

func bg2RunNodeJSON(t *testing.T, nodePath, script string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"--input-type=module", "-e", script}, args...)
	cmd := exec.Command(nodePath, cmdArgs...)
	cmd.Env = []string{"PATH=/nonexistent", "HOME=" + t.TempDir()}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node module script failed: %v\nstdout=%s\nstderr=%s", err, out, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func bg2WaitOccurrence(t *testing.T, ctx context.Context, client *pi.ProductClient, botID, watchID, scheduledAt, wantOutcome string) (pi.ProductWatchDetail, pi.ProductWatchOccurrence) {
	t.Helper()
	var detail pi.ProductWatchDetail
	var found pi.ProductWatchOccurrence
	bg1WaitUntil(t, ctx, "watch occurrence "+wantOutcome+" "+watchID, func() bool {
		got, err := client.GetWatch(ctx, botID, watchID)
		if err != nil {
			t.Fatalf("GetWatch(%s): %v", watchID, err)
		}
		for _, occ := range got.RecentOccurrences {
			if occ.ScheduledAt == scheduledAt && occ.State != "running" {
				if wantOutcome != "" && occ.Outcome != wantOutcome {
					t.Fatalf("watch %s occurrence at %s outcome=%q error=%q exit=%v, want %q", watchID, scheduledAt, occ.Outcome, occ.Error, occ.ExitCode, wantOutcome)
				}
				detail = got
				found = occ
				return true
			}
		}
		return false
	})
	return detail, found
}

func bg2WaitPrecheckPID(t *testing.T, ctx context.Context, scratchBase string, existing map[string]struct{}) int {
	t.Helper()
	var pid int
	bg1WaitUntil(t, ctx, "precheck scratch pid file", func() bool {
		entries, err := os.ReadDir(scratchBase)
		if err != nil {
			return false
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, "xuhu-precheck-") {
				continue
			}
			if _, ok := existing[name]; ok {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(scratchBase, name, "scratch", "pid"))
			if err != nil {
				continue
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err == nil && parsed > 0 {
				pid = parsed
				return true
			}
		}
		return false
	})
	return pid
}

func bg2SnapshotScratchEntries(t *testing.T, scratchBase string) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(scratchBase)
	if err != nil {
		t.Fatal(err)
	}
	res := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		res[entry.Name()] = struct{}{}
	}
	return res
}

func bg2SQLQuote(s string) string {
	encoded, _ := json.Marshal(s)
	return "'" + strings.ReplaceAll(string(encoded), "'", "''") + "'"
}

func bg2CountRows(t *testing.T, nodePath, dbPath, table, where string) int {
	t.Helper()
	query := fmt.Sprintf("SELECT count(*) AS n FROM %s", table)
	if where != "" {
		query += " WHERE " + where
	}
	raw := runNodeSQLite(t, nodePath, dbPath, fmt.Sprintf(`
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
console.log(db.prepare(%q).get().n);
db.close();
`, query))
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("parse count %q for %s: %v", raw, table, err)
	}
	return n
}

func TestPiProductBG2StandingGrantsWatchesSchedulesAndEventInbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	nodePath, workerPath := cp1WorkerPaths(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "ws")
	siblingWS := filepath.Join(root, "sibling-ws")
	movedWS := filepath.Join(root, "moved-ws")
	profileDir := filepath.Join(root, "profile")
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{ws, siblingWS, movedWS, profileDir, binDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	observerBin := bg2CompilePrecheckFixture(t, binDir)
	clockPath := filepath.Join(root, "clock.json")
	currentEpoch := bg2ParseISO(t, "2026-03-07T06:00:00.000Z")
	setClock := func(ms int64) {
		currentEpoch = ms
		bg2WriteClock(t, clockPath, ms)
	}
	setClock(currentEpoch)
	t.Setenv("NAGI_TEST_CLOCK_FILE", clockPath)
	t.Setenv("NAGI_WATCH_POLL_MS", "25")
	t.Setenv("NAGI_BG2_ENV_SECRET", "bg2-env-sentinel-9876")

	var mu sync.Mutex
	type plannedToolCall struct {
		Name   string
		Args   string
		Result string
		Seen   bool
	}
	plans := map[string]*plannedToolCall{}
	calls := map[string]*plannedToolCall{}
	providerKey := "bg2-provider-key-alpha-1234"
	mcpSecret := "bg2-mcp-secret-omega-5678"

	provider := newCP1Provider(t, providerKey, func(req cp1Request) cp1Reply {
		mu.Lock()
		defer mu.Unlock()
		if req.LastToolID != "" {
			if planned := calls[req.LastToolID]; planned != nil {
				planned.Result = req.ToolResults[req.LastToolID]
				delete(calls, req.LastToolID)
			}
			return cp1Reply{Text: "Tool finished"}
		}
		for key, plan := range plans {
			if strings.Contains(req.UserText, key) {
				delete(plans, key)
				plan.Seen = true
				id := "call_" + strings.ReplaceAll(key, "-", "")
				if len(id) > 22 {
					id = id[:22]
				}
				calls[id] = plan
				return cp1Reply{
					Calls: []cp1ToolCall{{ID: id, Name: plan.Name, Args: plan.Args}},
				}
			}
		}
		return cp1Reply{Text: "Unhandled fixture turn"}
	})
	provider.addForbiddenSecret(providerKey)
	provider.addForbiddenSecret(mcpSecret)

	var skillHTTPMu sync.Mutex
	var skillHTTPMethods []string
	skillServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		skillHTTPMu.Lock()
		skillHTTPMethods = append(skillHTTPMethods, r.Method)
		skillHTTPMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer skillServer.Close()

	_, providerPortStr, err := net.SplitHostPort(strings.TrimPrefix(provider.server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(root, "pi"),
		ProfilePath:     filepath.Join(profileDir, "contacts.json"),
		CredentialsPath: filepath.Join(profileDir, "connection.json"),
		FreshProfile:    true,
	}
	dbPath := filepath.Join(profileDir, "xuhu.db")
	proc := startCP1Process(t, opts)
	_ = proc.open(t, ctx)

	cp1ConfigureConnection(t, ctx, proc.client, provider.server.URL+"/v1", providerKey, ws)
	if _, mcpErr := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server: pi.ProductMcpServerInput{
			ProductMcpServerConfig: pi.ProductMcpServerConfig{
				ServerID:         "bg2-mcp",
				Name:             "bg2-mcp",
				Isolation:        "per_bot",
				Enabled:          true,
				Transport:        "http",
				URL:              skillServer.URL + "/mcp",
				HeaderKeys:       []string{"X-Test-Header"},
				SecretHeaderKeys: []string{"X-Secret-Header"},
			},
		},
		Secrets: &pi.ProductMcpSecrets{
			Headers: map[string]*string{"X-Secret-Header": &mcpSecret},
		},
		OperationID: "bg2-save-mcp",
	}); mcpErr != nil {
		t.Fatalf("SaveMcpServer: %v", mcpErr)
	}

	bot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "bg2-bot-create",
		Profile: pi.ProductBotProfile{
			Name: "WatchOwner", Role: "Watcher", Purpose: "Own standing watches",
			Instructions: "Use watch tools carefully.", Workspace: ws, CodingTools: nil, ToolPreset: "workspace",
		},
	})
	if err != nil {
		t.Fatalf("CreateBot: %v", err)
	}
	sibling, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "bg2-sibling-create",
		Profile: pi.ProductBotProfile{
			Name: "Sibling", Role: "Other", Purpose: "Isolation target",
			Instructions: "Stay isolated.", Workspace: siblingWS, CodingTools: nil, ToolPreset: "workspace",
		},
	})
	if err != nil {
		t.Fatalf("CreateBot sibling: %v", err)
	}

	runToolTurn := func(targetBot pi.ProductBot, convID, toolName string, args any) string {
		t.Helper()
		argBytes, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("bg2tool-%s-%d", toolName, time.Now().UnixNano())
		plan := &plannedToolCall{Name: toolName, Args: string(argBytes)}
		mu.Lock()
		plans[key] = plan
		mu.Unlock()
		settled := cp1SendTurn(t, ctx, proc, convID, key, key, nil)
		if settled.State != "completed" {
			t.Fatalf("tool turn %s settled state=%q err=%q", toolName, settled.State, settled.Error)
		}
		mu.Lock()
		res := plan.Result
		seen := plan.Seen
		mu.Unlock()
		if !seen || res == "" {
			t.Fatalf("tool %s was not invoked or empty result: seen=%v res=%q", toolName, seen, res)
		}
		return res
	}

	interval10 := &pi.ProductScheduleSpec{
		CadenceKind: pi.ProductScheduleInterval, IntervalSeconds: intPtr(10), TimeZone: "UTC", JitterSeconds: intPtr(0),
	}
	precheckObs := &pi.ProductWatchPrecheck{
		Kind: "executable", Executable: observerBin, AllowedArgs: []string{}, WorkspaceRead: true, TimeoutMS: intPtr(10000),
	}

	_, err = proc.client.ListWatches(ctx, pi.ProductWatchListParams{
		BotID: bot.BotID, States: []pi.ProductWatchState{pi.ProductWatchActive, pi.ProductWatchActive},
	})
	cp1AssertRemoteCode(t, err, "invalid_params")

	for _, badTZ := range []string{"Invalid/Zone", "+08:00"} {
		_, err = proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
			BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Bad zone",
			Schedule:    &pi.ProductScheduleSpec{CadenceKind: pi.ProductScheduleInterval, IntervalSeconds: intPtr(10), TimeZone: badTZ},
			OperationID: "bg2-bad-tz-" + badTZ,
		})
		cp1AssertRemoteCode(t, err, "invalid_params")
	}

	_, err = proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Too frequent",
		Schedule:    &pi.ProductScheduleSpec{CadenceKind: pi.ProductScheduleInterval, IntervalSeconds: intPtr(9), TimeZone: "UTC"},
		OperationID: "bg2-fast-cadence",
	})
	cp1AssertRemoteCode(t, err, "invalid_params")

	secretSaves := []pi.ProductWatchSaveParams{
		{BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Label: providerKey, Purpose: "Secret in label", Schedule: interval10, OperationID: "sec-1"},
		{BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: mcpSecret, Schedule: interval10, OperationID: "sec-2"},
		{BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Secret in prompt", Prompt: providerKey, Schedule: interval10, OperationID: "sec-3"},
		{BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Secret in args", Schedule: interval10,
			Precheck: &pi.ProductWatchPrecheck{Kind: "executable", Executable: observerBin, AllowedArgs: []string{mcpSecret}}, OperationID: "sec-4"},
		{BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Secret in skill input", Schedule: interval10,
			Precheck: &pi.ProductWatchPrecheck{Kind: "skill", SkillID: "watch-skill", SkillSource: "private", ScriptName: "observe", Input: map[string]any{"token": providerKey}}, OperationID: "sec-5"},
	}
	for _, params := range secretSaves {
		_, err = proc.client.SaveWatch(ctx, params)
		cp1AssertRemoteCode(t, err, "secret_in_definition")
	}

	proposalRaw := runToolTurn(bot, bot.CurrentConversationID, "watch_create", map[string]any{
		"kind": "schedule", "purpose": "Observe workspace fixture", "label": "Watch proposal",
		"schedule": interval10, "precheck": precheckObs,
	})
	var proposalReceipt pi.ProductReceipt
	if err := json.Unmarshal([]byte(proposalRaw), &proposalReceipt); err != nil {
		t.Fatal(err)
	}
	proposalRes, err := pi.DecodeWatchMutationResult(proposalReceipt)
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalRes.Watch
	if proposal.State != pi.ProductWatchProposed || proposal.Armed || proposal.StatusReason != "grant_required" || proposal.Source != pi.ProductWatchSourceModel {
		t.Fatalf("unexpected model precheck proposal: %+v", proposal)
	}
	grantsPage, err := proc.client.ListWatchGrants(ctx, pi.ProductWatchGrantListParams{BotID: bot.BotID})
	if err != nil || len(grantsPage.Items) != 0 {
		t.Fatalf("expected 0 grants for proposed watch, got %+v err=%v", grantsPage, err)
	}

	_, err = proc.client.GetWatch(ctx, sibling.BotID, proposal.WatchID)
	cp1AssertRemoteCode(t, err, "not_found")
	_, err = proc.client.ActivateWatch(ctx, pi.ProductWatchActivateParams{
		BotID: sibling.BotID, WatchID: proposal.WatchID, ExpectedRevision: proposal.Revision, ApproveGrant: true, OperationID: "bg2-sib-act",
	})
	cp1AssertRemoteCode(t, err, "not_found")

	crossConvRaw := runToolTurn(bot, bot.CurrentConversationID, "watch_create", map[string]any{
		"kind": "schedule", "purpose": "Cross bot target", "targetConversationId": sibling.CurrentConversationID,
		"schedule": interval10,
	})
	var crossConvErr map[string]any
	if err := json.Unmarshal([]byte(crossConvRaw), &crossConvErr); err != nil || crossConvErr["code"] != "scope_denied" {
		t.Fatalf("cross conversation watch_create = %s", crossConvRaw)
	}

	_, err = proc.client.ActivateWatch(ctx, pi.ProductWatchActivateParams{
		BotID: bot.BotID, WatchID: proposal.WatchID, ExpectedRevision: proposal.Revision, ApproveGrant: false, OperationID: "bg2-act-nogrant",
	})
	cp1AssertRemoteCode(t, err, "grant_required")

	opActivate := "bg2-act-approve-1"
	actParams := pi.ProductWatchActivateParams{
		BotID: bot.BotID, WatchID: proposal.WatchID, ExpectedRevision: proposal.Revision, ApproveGrant: true, OperationID: opActivate,
	}
	actReceipt, err := proc.client.ActivateWatch(ctx, actParams)
	if err != nil {
		t.Fatalf("ActivateWatch: %v", err)
	}
	actResult, err := pi.DecodeWatchMutationResult(actReceipt)
	if err != nil || actResult.Watch.State != pi.ProductWatchActive || !actResult.Watch.Armed || actResult.Grant == nil ||
		actResult.Grant.OperationClass != "precheck_executable" || actResult.Grant.InstallationID == "" || actResult.Grant.ExecutableDigest == "" {
		t.Fatalf("unexpected activation result: %+v err=%v", actResult, err)
	}

	proc.waitEvent(t, ctx, func(ev pi.ProductEvent) bool {
		return ev.Name == "watch.changed" && ev.WatchChanged != nil &&
			ev.WatchChanged.Change == "activated" && ev.WatchChanged.WatchID == proposal.WatchID && ev.WatchChanged.State == pi.ProductWatchActive
	})

	replayReceipt, err := proc.client.ActivateWatch(ctx, actParams)
	if err != nil {
		t.Fatalf("ActivateWatch replay: %v", err)
	}
	replayResult, err := pi.DecodeWatchMutationResult(replayReceipt)
	if err != nil || replayResult.Watch.Revision != actResult.Watch.Revision || replayResult.Grant.GrantID != actResult.Grant.GrantID {
		t.Fatalf("ActivateWatch replay mismatch: got=%+v want=%+v", replayResult, actResult)
	}
	opGot, err := proc.client.Operation(ctx, opActivate)
	if err != nil || string(opGot.Result) != string(actReceipt.Result) {
		t.Fatalf("operation.get mismatch: got=%+v want=%+v err=%v", opGot, actReceipt, err)
	}
	_, err = proc.client.ActivateWatch(ctx, pi.ProductWatchActivateParams{
		BotID: bot.BotID, WatchID: proposal.WatchID, ExpectedRevision: proposal.Revision, ApproveGrant: false, OperationID: opActivate,
	})
	cp1AssertRemoteCode(t, err, "operation_conflict")
	_, err = proc.client.PauseWatch(ctx, pi.ProductWatchStateMutationParams{
		BotID: bot.BotID, WatchID: proposal.WatchID, ExpectedRevision: proposal.Revision, OperationID: "bg2-stale-pause",
	})
	cp1AssertRemoteCode(t, err, "stale_revision")

	reuseRaw := runToolTurn(bot, bot.CurrentConversationID, "watch_create", map[string]any{
		"kind": "schedule", "purpose": "Observe workspace fixture",
		"schedule": interval10, "precheck": precheckObs,
	})
	var reuseReceipt pi.ProductReceipt
	if err := json.Unmarshal([]byte(reuseRaw), &reuseReceipt); err != nil {
		t.Fatal(err)
	}
	reuseRes, err := pi.DecodeWatchMutationResult(reuseReceipt)
	if err != nil || reuseRes.Watch.State != pi.ProductWatchActive || !reuseRes.Watch.Armed || reuseRes.Watch.GrantID != actResult.Grant.GrantID {
		t.Fatalf("expected grant reuse auto-active watch, got=%+v err=%v", reuseRes, err)
	}

	pauseToolRaw := runToolTurn(bot, bot.CurrentConversationID, "watch_pause", map[string]any{
		"watch_id": reuseRes.Watch.WatchID, "expected_revision": reuseRes.Watch.Revision,
	})
	var pauseToolReceipt pi.ProductReceipt
	if err := json.Unmarshal([]byte(pauseToolRaw), &pauseToolReceipt); err != nil {
		t.Fatal(err)
	}
	pauseToolRes, err := pi.DecodeWatchMutationResult(pauseToolReceipt)
	if err != nil || pauseToolRes.Watch.State != pi.ProductWatchPaused {
		t.Fatalf("watch_pause failed: %+v err=%v", pauseToolRes, err)
	}

	listToolRaw := runToolTurn(bot, bot.CurrentConversationID, "watch_list", map[string]any{})
	if !strings.Contains(listToolRaw, proposal.WatchID) || !strings.Contains(listToolRaw, reuseRes.Watch.WatchID) {
		t.Fatalf("watch_list missing created watches: %s", listToolRaw)
	}
	getToolRaw := runToolTurn(bot, bot.CurrentConversationID, "watch_get", map[string]any{"watch_id": proposal.WatchID})
	if !strings.Contains(getToolRaw, `"state":"active"`) {
		t.Fatalf("watch_get unexpected: %s", getToolRaw)
	}
	cancelToolRaw := runToolTurn(bot, bot.CurrentConversationID, "watch_cancel", map[string]any{
		"watch_id": reuseRes.Watch.WatchID, "expected_revision": pauseToolRes.Watch.Revision,
	})
	var cancelToolReceipt pi.ProductReceipt
	if err := json.Unmarshal([]byte(cancelToolRaw), &cancelToolReceipt); err != nil {
		t.Fatal(err)
	}
	cancelToolRes, err := pi.DecodeWatchMutationResult(cancelToolReceipt)
	if err != nil || cancelToolRes.Watch.State != pi.ProductWatchCancelled {
		t.Fatalf("watch_cancel failed: %+v err=%v", cancelToolRes, err)
	}

	repeatCancel, err := proc.client.CancelWatch(ctx, pi.ProductWatchStateMutationParams{
		BotID: bot.BotID, WatchID: cancelToolRes.Watch.WatchID, ExpectedRevision: cancelToolRes.Watch.Revision, OperationID: "bg2-repeat-cancel",
	})
	if err != nil {
		t.Fatalf("repeat CancelWatch: %v", err)
	}
	repeatCancelRes, err := pi.DecodeWatchMutationResult(repeatCancel)
	if err != nil || repeatCancelRes.Disposition != "already_settled" {
		t.Fatalf("repeat cancel disposition=%q want already_settled", repeatCancelRes.Disposition)
	}
	_, err = proc.client.PauseWatch(ctx, pi.ProductWatchStateMutationParams{
		BotID: bot.BotID, WatchID: cancelToolRes.Watch.WatchID, ExpectedRevision: cancelToolRes.Watch.Revision, OperationID: "bg2-pause-settled",
	})
	cp1AssertRemoteCode(t, err, "invalid_state")

	m4JSON := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const rows = db.prepare('SELECT tool_name, tool_class FROM execution_mutation_admissions').all();
db.close();
console.log(JSON.stringify(rows));
`)
	for _, wantPair := range []string{
		`{"tool_name":"watch_list","tool_class":"readonly"}`,
		`{"tool_name":"watch_get","tool_class":"readonly"}`,
		`{"tool_name":"watch_create","tool_class":"mutating"}`,
		`{"tool_name":"watch_pause","tool_class":"mutating"}`,
		`{"tool_name":"watch_cancel","tool_class":"mutating"}`,
	} {
		if !strings.Contains(m4JSON, wantPair) {
			t.Fatalf("missing M4 admission %s in %s", wantPair, m4JSON)
		}
	}

	writeWorkspaceState := func(dir, mode string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "state.txt"), []byte(mode), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tickWatch := func(watchID, mode, wantOutcome string) (pi.ProductWatchDetail, pi.ProductWatchOccurrence) {
		t.Helper()
		if mode != "" {
			writeWorkspaceState(ws, mode)
		}
		before, err := proc.client.GetWatch(ctx, bot.BotID, watchID)
		if err != nil {
			t.Fatalf("GetWatch before tick(%s): %v", watchID, err)
		}
		if before.NextDueAt == "" {
			t.Fatalf("watch %s missing nextDueAt: %+v", watchID, before)
		}
		setClock(bg2ParseISO(t, before.NextDueAt))
		return bg2WaitOccurrence(t, ctx, proc.client, bot.BotID, watchID, before.NextDueAt, wantOutcome)
	}

	baseDetail, baseOcc := tickWatch(proposal.WatchID, "A", "baseline")
	if baseDetail.LastGoodDigest != baseOcc.Digest || baseOcc.ExitCode == nil || *baseOcc.ExitCode != 0 || !baseOcc.Truncated || len(baseOcc.Excerpt) != 1024 ||
		!utf8.ValidString(baseOcc.Excerpt) || baseOcc.EventID != "" {
		t.Fatalf("unexpected baseline occurrence: detail=%+v occ=%+v", baseDetail, baseOcc)
	}
	_, unchOcc := tickWatch(proposal.WatchID, "A", "unchanged")
	if unchOcc.Digest != baseOcc.Digest || unchOcc.EventID != "" {
		t.Fatalf("unchanged mismatch: got=%+v baseline=%+v", unchOcc, baseOcc)
	}
	chgDetail, chgOcc := tickWatch(proposal.WatchID, "B", "changed")
	if !chgOcc.Truncated || chgOcc.Digest == baseOcc.Digest || chgOcc.Excerpt != baseOcc.Excerpt || chgOcc.EventID == "" {
		t.Fatalf("tail-only >64KiB diff mismatch: got=%+v baseline=%+v", chgOcc, baseOcc)
	}
	failDetail, failOcc := tickWatch(proposal.WatchID, "F", "failed")
	if failOcc.ExitCode == nil || *failOcc.ExitCode != 9 || failDetail.LastGoodDigest != chgOcc.Digest || failOcc.EventID == "" {
		t.Fatalf("first failure mismatch: detail=%+v occ=%+v", failDetail, failOcc)
	}
	inboxAfterFail := bg2CountRows(t, nodePath, dbPath, "bot_event_inbox", "source_id = "+bg2SQLQuote(proposal.WatchID))
	_, supOcc := tickWatch(proposal.WatchID, "F", "suppressed_failure")
	if supOcc.EventID != "" || bg2CountRows(t, nodePath, dbPath, "bot_event_inbox", "source_id = "+bg2SQLQuote(proposal.WatchID)) != inboxAfterFail {
		t.Fatalf("suppressed failure emitted event unexpectedly: %+v", supOcc)
	}
	toDetail, toOcc := tickWatch(proposal.WatchID, "T", "failed")
	if toOcc.Error != "timeout" || toDetail.LastGoodDigest != chgOcc.Digest || toOcc.EventID == "" {
		t.Fatalf("timeout failure mismatch: detail=%+v occ=%+v", toDetail, toOcc)
	}
	recDetail, recOcc := tickWatch(proposal.WatchID, "B", "recovered")
	if recDetail.ConsecutiveFailures != 0 || recDetail.LastGoodDigest != chgDetail.LastGoodDigest || recOcc.EventID == "" {
		t.Fatalf("recovery mismatch: detail=%+v occ=%+v", recDetail, recOcc)
	}
	if _, err := proc.client.PauseWatch(ctx, pi.ProductWatchStateMutationParams{
		BotID: bot.BotID, WatchID: proposal.WatchID, ExpectedRevision: recDetail.Revision, OperationID: "bg2-pause-proposal",
	}); err != nil {
		t.Fatal(err)
	}

	mcpSecretsPath := filepath.Join(profileDir, "mcp-secrets.json")
	isoReadReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Seatbelt isolation read=true", Schedule: interval10,
		Precheck: &pi.ProductWatchPrecheck{
			Kind: "executable", Executable: observerBin, WorkspaceRead: true, EmitInitial: true, TimeoutMS: intPtr(10000),
			AllowedArgs: []string{"isolation", opts.CredentialsPath, mcpSecretsPath, providerPortStr, "read"},
		},
		OperationID: "bg2-save-iso-read",
	})
	if err != nil {
		t.Fatal(err)
	}
	isoReadRes, _ := pi.DecodeWatchMutationResult(isoReadReceipt)
	isoDetail, isoOcc := tickWatch(isoReadRes.Watch.WatchID, "", "initial")
	if isoOcc.ExitCode == nil || *isoOcc.ExitCode != 0 ||
		!strings.Contains(isoOcc.Excerpt, "isolation=ok conn=1 vault=1 wsmode=1 write=1 scratch=1 net=1 spawn=1 env=1") {
		t.Fatalf("Seatbelt isolation(read) failed: occ=%+v", isoOcc)
	}
	if _, err := os.Stat(filepath.Join(ws, "should-never-exist")); !os.IsNotExist(err) {
		t.Fatalf("precheck created file in workspace: err=%v", err)
	}
	if _, err := proc.client.PauseWatch(ctx, pi.ProductWatchStateMutationParams{
		BotID: bot.BotID, WatchID: isoReadRes.Watch.WatchID, ExpectedRevision: isoDetail.Revision, OperationID: "bg2-pause-isoread",
	}); err != nil {
		t.Fatal(err)
	}

	isoNoReadReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Seatbelt isolation read=false", Schedule: interval10,
		Precheck: &pi.ProductWatchPrecheck{
			Kind: "executable", Executable: observerBin, WorkspaceRead: false, EmitInitial: true, TimeoutMS: intPtr(10000),
			AllowedArgs: []string{"isolation", opts.CredentialsPath, mcpSecretsPath, providerPortStr, "noread"},
		},
		OperationID: "bg2-save-iso-noread",
	})
	if err != nil {
		t.Fatal(err)
	}
	isoNoReadRes, _ := pi.DecodeWatchMutationResult(isoNoReadReceipt)
	isoNoReadDetail, isoNoReadOcc := tickWatch(isoNoReadRes.Watch.WatchID, "", "initial")
	if isoNoReadOcc.ExitCode == nil || *isoNoReadOcc.ExitCode != 0 ||
		!strings.Contains(isoNoReadOcc.Excerpt, "wsmode=1") {
		t.Fatalf("Seatbelt isolation(noread) failed: occ=%+v", isoNoReadOcc)
	}

	revokeReceipt, err := proc.client.RevokeWatchGrant(ctx, pi.ProductWatchRevokeGrantParams{
		BotID: bot.BotID, GrantID: isoNoReadRes.Grant.GrantID, ExpectedRevision: isoNoReadRes.Grant.Revision, OperationID: "bg2-revoke-grant",
	})
	if err != nil {
		t.Fatal(err)
	}
	revokeRes, err := pi.DecodeWatchRevokeGrantResult(revokeReceipt)
	if err != nil || !slices.Contains(revokeRes.InvalidatedWatchIDs, isoNoReadRes.Watch.WatchID) || revokeRes.Grant.State != "revoked" {
		t.Fatalf("RevokeWatchGrant unexpected: %+v err=%v", revokeRes, err)
	}
	afterRevoke, err := proc.client.GetWatch(ctx, bot.BotID, isoNoReadRes.Watch.WatchID)
	if err != nil || afterRevoke.State != pi.ProductWatchInvalidated || afterRevoke.StatusReason != "grant_revoked" {
		t.Fatalf("watch after grant revoke: %+v err=%v want revision=%d", afterRevoke, err, isoNoReadDetail.Revision)
	}

	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "bg2-rw-skill"},
		Name:            "RW",
		Description:     "Disallowed write permission",
		Content:         "Disallow",
		OperationID:     "bg2-save-rw-skill",
		Manifest: &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"bad": {
					Entry:       "scripts/bad.mjs",
					Description: "bad",
					Permissions: pi.ProductSkillPermissions{WorkspaceWrite: true},
				},
			},
		},
		Scripts: map[string]string{"scripts/bad.mjs": `export default () => "no";`},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Disallowed skill write", Schedule: interval10,
		Precheck:    &pi.ProductWatchPrecheck{Kind: "skill", SkillID: "bg2-rw-skill", SkillSource: "private", ScriptName: "bad"},
		OperationID: "bg2-watch-rw-skill",
	})
	cp1AssertRemoteCode(t, err, "scope_denied")

	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "bg2-cred-skill"},
		Name:            "Cred",
		Description:     "Disallowed creds",
		Content:         "Disallow creds",
		OperationID:     "bg2-save-cred-skill",
		Manifest: &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"badcred": {
					Entry:       "scripts/badcred.mjs",
					Description: "badcred",
					Permissions: pi.ProductSkillPermissions{
						NetworkOrigins: []string{skillServer.URL},
						CredentialRefs: []string{"mcp:bg2-mcp:X-Secret-Header"},
					},
				},
			},
		},
		Scripts: map[string]string{"scripts/badcred.mjs": `export default () => "no";`},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Disallowed skill creds", Schedule: interval10,
		Precheck:    &pi.ProductWatchPrecheck{Kind: "skill", SkillID: "bg2-cred-skill", SkillSource: "private", ScriptName: "badcred"},
		OperationID: "bg2-watch-cred-skill",
	})
	cp1AssertRemoteCode(t, err, "scope_denied")

	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "bg2-skill-obs"},
		Name:            "Skill Observer",
		Description:     "Read ws + GET",
		Content:         "Observe safely",
		OperationID:     "bg2-save-obs-skill",
		Manifest: &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"observe": {
					Entry:       "scripts/observe.mjs",
					Description: "Read file and GET",
					TimeoutMS:   5000,
					Permissions: pi.ProductSkillPermissions{WorkspaceRead: true, NetworkOrigins: []string{skillServer.URL}},
				},
				"mutate": {
					Entry:       "scripts/mutate.mjs",
					Description: "Attempt POST",
					TimeoutMS:   5000,
					Permissions: pi.ProductSkillPermissions{NetworkOrigins: []string{skillServer.URL}},
				},
			},
		},
		Scripts: map[string]string{
			"scripts/observe.mjs": fmt.Sprintf(`import {readFileSync} from 'node:fs';
import {join} from 'node:path';
export default async (input, ctx) => {
  const s = readFileSync(join(ctx.workspace, input.file), 'utf8');
  const r = await ctx.fetch(%q + '/obs');
  return s + ':' + (await r.text());
};`, skillServer.URL),
			"scripts/mutate.mjs": fmt.Sprintf(`export default async (_input, ctx) => {
  const r = await ctx.fetch(%q + '/mut', { method: 'POST', body: 'x' });
  return r.status;
};`, skillServer.URL),
		},
	}); err != nil {
		t.Fatal(err)
	}
	skillWatchReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Skill read+GET precheck", Schedule: interval10,
		Precheck: &pi.ProductWatchPrecheck{
			Kind: "skill", SkillID: "bg2-skill-obs", SkillSource: "private", ScriptName: "observe",
			Input: map[string]any{"file": "state.txt"}, EmitInitial: true, TimeoutMS: intPtr(5000),
		},
		OperationID: "bg2-watch-skill-obs",
	})
	if err != nil {
		t.Fatal(err)
	}
	skillWatchRes, _ := pi.DecodeWatchMutationResult(skillWatchReceipt)
	if skillWatchRes.Grant == nil || skillWatchRes.Grant.OperationClass != "precheck_skill" ||
		skillWatchRes.Grant.ApprovalDigest == "" || skillWatchRes.Grant.PermissionsDigest == "" {
		t.Fatalf("unexpected skill grant: %+v", skillWatchRes.Grant)
	}
	skillDetail, skillOcc := tickWatch(skillWatchRes.Watch.WatchID, "B", "initial")
	if skillOcc.ExitCode == nil || *skillOcc.ExitCode != 0 || !strings.Contains(skillOcc.Excerpt, `\"status\":\"ok\"`) {
		t.Fatalf("skill GET precheck failed: occ=%+v", skillOcc)
	}
	if _, err := proc.client.PauseWatch(ctx, pi.ProductWatchStateMutationParams{
		BotID: bot.BotID, WatchID: skillWatchRes.Watch.WatchID, ExpectedRevision: skillDetail.Revision, OperationID: "bg2-pause-skill-obs",
	}); err != nil {
		t.Fatal(err)
	}

	skillMutReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Skill blocked POST precheck", Schedule: interval10,
		Precheck: &pi.ProductWatchPrecheck{
			Kind: "skill", SkillID: "bg2-skill-obs", SkillSource: "private", ScriptName: "mutate", TimeoutMS: intPtr(5000),
		},
		OperationID: "bg2-watch-skill-mut",
	})
	if err != nil {
		t.Fatal(err)
	}
	skillMutRes, _ := pi.DecodeWatchMutationResult(skillMutReceipt)
	skillMutDetail, _ := tickWatch(skillMutRes.Watch.WatchID, "", "failed")
	if _, err := proc.client.PauseWatch(ctx, pi.ProductWatchStateMutationParams{
		BotID: bot.BotID, WatchID: skillMutRes.Watch.WatchID, ExpectedRevision: skillMutDetail.Revision, OperationID: "bg2-pause-skill-mut",
	}); err != nil {
		t.Fatal(err)
	}
	skillHTTPMu.Lock()
	sawPOST := slices.Contains(skillHTTPMethods, "POST")
	sawGET := slices.Contains(skillHTTPMethods, "GET")
	skillHTTPMu.Unlock()
	if !sawGET || sawPOST {
		t.Fatalf("mediated HTTP methods=%v, want GET present and POST absent", skillHTTPMethods)
	}

	mutableBin := filepath.Join(binDir, "mutable-observer")
	if err := os.WriteFile(mutableBin, productRead(t, observerBin), 0o700); err != nil {
		t.Fatal(err)
	}
	driftReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Binary drift check", Schedule: interval10,
		Precheck:    &pi.ProductWatchPrecheck{Kind: "executable", Executable: mutableBin, TimeoutMS: intPtr(5000)},
		OperationID: "bg2-save-drift",
	})
	if err != nil {
		t.Fatal(err)
	}
	driftRes, _ := pi.DecodeWatchMutationResult(driftReceipt)
	f, err := os.OpenFile(mutableBin, os.O_WRONLY|os.O_APPEND, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("drift")
	_ = f.Close()
	setClock(bg2ParseISO(t, driftRes.Watch.NextDueAt))
	var afterDrift pi.ProductWatchDetail
	bg1WaitUntil(t, ctx, "binary drift invalidation", func() bool {
		got, err := proc.client.GetWatch(ctx, bot.BotID, driftRes.Watch.WatchID)
		if err != nil {
			t.Fatal(err)
		}
		if got.StatusReason == "executable_changed" {
			afterDrift = got
			return true
		}
		return false
	})
	if afterDrift.State != pi.ProductWatchInvalidated || afterDrift.Grant == nil || afterDrift.Grant.State != "revoked" || len(afterDrift.RecentOccurrences) != 0 {
		t.Fatalf("unexpected drift state: %+v", afterDrift)
	}

	proc.shutdown(t, ctx)
	t.Run("HeldInboxBurstCoalescing", func(t *testing.T) {
		t.Setenv("NAGI_ORCHESTRATION_HOLD", "1")
		opts.FreshProfile = false
		proc = startCP1Process(t, opts)
		_ = proc.open(t, ctx)
		mirrorBeforeTicks := productRead(t, opts.ProfilePath)
		plainReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
			BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Plain recurring tick", Schedule: interval10, OperationID: "bg2-save-plain",
		})
		if err != nil {
			t.Fatal(err)
		}
		plainRes, _ := pi.DecodeWatchMutationResult(plainReceipt)
		tickWatch(plainRes.Watch.WatchID, "", "tick")
		secondTick, _ := tickWatch(plainRes.Watch.WatchID, "", "tick")
		setClock(bg2ParseISO(t, secondTick.NextDueAt) + 120_000)
		var afterSkip pi.ProductWatchDetail
		bg1WaitUntil(t, ctx, "offline schedule backlog skip", func() bool {
			got, err := proc.client.GetWatch(ctx, bot.BotID, plainRes.Watch.WatchID)
			if err != nil {
				t.Fatal(err)
			}
			if got.NextDueAt != "" && bg2ParseISO(t, got.NextDueAt) > currentEpoch {
				afterSkip = got
				return true
			}
			return false
		})
		if len(afterSkip.RecentOccurrences) != 3 {
			t.Fatalf("expected 3 occurrences after skip, got %d", len(afterSkip.RecentOccurrences))
		}
		plainInboxCount := bg2CountRows(t, nodePath, dbPath, "bot_event_inbox", "source_id = "+bg2SQLQuote(plainRes.Watch.WatchID))
		if plainInboxCount != 1 {
			t.Fatalf("expected 1 coalesced inbox row, got %d", plainInboxCount)
		}
		coalescedVal := runNodeSQLite(t, nodePath, dbPath, fmt.Sprintf(`
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
console.log(db.prepare('SELECT coalesced_count FROM bot_event_inbox WHERE source_id = ?').get(JSON.stringify(%q)).coalesced_count);
db.close();
`, plainRes.Watch.WatchID))
		if coalescedVal != "3" {
			t.Fatalf("coalesced_count = %s, want 3", coalescedVal)
		}
		setClock(currentEpoch - 120_000)
		rollbackCheck, err := proc.client.GetWatch(ctx, bot.BotID, plainRes.Watch.WatchID)
		if err != nil || rollbackCheck.NextDueAt != afterSkip.NextDueAt {
			t.Fatalf("clock rollback altered nextDueAt: got=%q want=%q err=%v", rollbackCheck.NextDueAt, afterSkip.NextDueAt, err)
		}
		if !bytes.Equal(productRead(t, opts.ProfilePath), mirrorBeforeTicks) {
			t.Fatal("watch mutations/ticks altered canonical contacts.json bytes")
		}
		if _, err := proc.client.PauseWatch(ctx, pi.ProductWatchStateMutationParams{
			BotID: bot.BotID, WatchID: plainRes.Watch.WatchID, ExpectedRevision: rollbackCheck.Revision, OperationID: "bg2-pause-plain",
		}); err != nil {
			t.Fatal(err)
		}
		proc.shutdown(t, ctx)
	})
	opts.FreshProfile = false
	proc = startCP1Process(t, opts)
	_ = proc.open(t, ctx)

	setClock(bg2ParseISO(t, "2026-03-07T06:00:00.000Z"))
	springReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "NY DST spring",
		Schedule: &pi.ProductScheduleSpec{
			CadenceKind: pi.ProductScheduleCalendar, Frequency: "daily", Hour: intPtr(2), Minute: intPtr(30), TimeZone: "America/New_York",
		},
		OperationID: "bg2-dst-spring",
	})
	if err != nil {
		t.Fatal(err)
	}
	springRes, _ := pi.DecodeWatchMutationResult(springReceipt)
	if springRes.Watch.NextDueAt != "2026-03-07T07:30:00.000Z" {
		t.Fatalf("spring initial nextDueAt = %q", springRes.Watch.NextDueAt)
	}
	afterSpring, _ := tickWatch(springRes.Watch.WatchID, "", "tick")
	if afterSpring.NextDueAt != "2026-03-09T06:30:00.000Z" {
		t.Fatalf("spring nextDueAt = %q, want 2026-03-09T06:30:00.000Z", afterSpring.NextDueAt)
	}
	if _, err := proc.client.PauseWatch(ctx, pi.ProductWatchStateMutationParams{
		BotID: bot.BotID, WatchID: springRes.Watch.WatchID, ExpectedRevision: afterSpring.Revision, OperationID: "bg2-pause-spring",
	}); err != nil {
		t.Fatal(err)
	}

	setClock(bg2ParseISO(t, "2026-11-01T04:00:00.000Z"))
	fallReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "NY DST fall",
		Schedule: &pi.ProductScheduleSpec{
			CadenceKind: pi.ProductScheduleCalendar, Frequency: "daily", Hour: intPtr(1), Minute: intPtr(30), TimeZone: "America/New_York",
		},
		OperationID: "bg2-dst-fall",
	})
	if err != nil {
		t.Fatal(err)
	}
	fallRes, _ := pi.DecodeWatchMutationResult(fallReceipt)
	if fallRes.Watch.NextDueAt != "2026-11-01T05:30:00.000Z" {
		t.Fatalf("fall initial nextDueAt = %q", fallRes.Watch.NextDueAt)
	}
	afterFall, _ := tickWatch(fallRes.Watch.WatchID, "", "tick")
	if afterFall.NextDueAt != "2026-11-02T06:30:00.000Z" {
		t.Fatalf("fall nextDueAt = %q, want 2026-11-02T06:30:00.000Z", afterFall.NextDueAt)
	}
	setClock(bg2ParseISO(t, "2026-11-01T06:30:00.000Z"))
	time.Sleep(100 * time.Millisecond)
	noOverlap, err := proc.client.GetWatch(ctx, bot.BotID, fallRes.Watch.WatchID)
	if err != nil || len(noOverlap.RecentOccurrences) != 1 {
		t.Fatalf("fall overlap double-fired: %+v err=%v", noOverlap, err)
	}
	if _, err := proc.client.PauseWatch(ctx, pi.ProductWatchStateMutationParams{
		BotID: bot.BotID, WatchID: fallRes.Watch.WatchID, ExpectedRevision: noOverlap.Revision, OperationID: "bg2-pause-fall",
	}); err != nil {
		t.Fatal(err)
	}

	onTimeFireAt := time.UnixMilli(currentEpoch + 10_000).UTC().Format("2006-01-02T15:04:05.000Z")
	onTimeTimerReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchTimer, Purpose: "On-time skip policy timer",
		Timer:       &pi.ProductTimerSpec{FireAt: onTimeFireAt, MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg2-timer-ontime",
	})
	if err != nil {
		t.Fatal(err)
	}
	onTimeRes, _ := pi.DecodeWatchMutationResult(onTimeTimerReceipt)
	setClock(currentEpoch + 10_000)
	bg1WaitUntil(t, ctx, "on-time timer fired", func() bool {
		got, err := proc.client.GetWatch(ctx, bot.BotID, onTimeRes.Watch.WatchID)
		if err != nil {
			t.Fatal(err)
		}
		return got.State == pi.ProductWatchSettled && got.LastOutcome == "fired"
	})

	missedFireAt := time.UnixMilli(currentEpoch + 10_000).UTC().Format("2006-01-02T15:04:05.000Z")
	skipTimerReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchTimer, Purpose: "Missed skip timer",
		Timer:       &pi.ProductTimerSpec{FireAt: missedFireAt, MissedPolicy: pi.ProductMissedSkip},
		OperationID: "bg2-timer-missed-skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	skipTimerRes, _ := pi.DecodeWatchMutationResult(skipTimerReceipt)
	coalTimerReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchTimer, Purpose: "Missed fire-once timer",
		Timer:       &pi.ProductTimerSpec{FireAt: missedFireAt, MissedPolicy: pi.ProductMissedFireOnceCoalesced},
		OperationID: "bg2-timer-missed-fire",
	})
	if err != nil {
		t.Fatal(err)
	}
	coalTimerRes, _ := pi.DecodeWatchMutationResult(coalTimerReceipt)
	proc.shutdown(t, ctx)

	setClock(currentEpoch + 30_000)
	opts.FreshProfile = false
	proc = startCP1Process(t, opts)
	_ = proc.open(t, ctx)
	bg1WaitUntil(t, ctx, "missed timers reconciled", func() bool {
		a, err1 := proc.client.GetWatch(ctx, bot.BotID, skipTimerRes.Watch.WatchID)
		b, err2 := proc.client.GetWatch(ctx, bot.BotID, coalTimerRes.Watch.WatchID)
		return err1 == nil && err2 == nil && a.State == pi.ProductWatchSettled && b.State == pi.ProductWatchSettled
	})
	skipGot, _ := proc.client.GetWatch(ctx, bot.BotID, skipTimerRes.Watch.WatchID)
	coalGot, _ := proc.client.GetWatch(ctx, bot.BotID, coalTimerRes.Watch.WatchID)
	if skipGot.LastOutcome != "skipped_missed" || coalGot.LastOutcome != "fired" {
		t.Fatalf("missed timer outcomes skip=%+v coal=%+v", skipGot, coalGot)
	}
	if bg2CountRows(t, nodePath, dbPath, "bot_event_inbox", "source_id = "+bg2SQLQuote(skipTimerRes.Watch.WatchID)) != 0 ||
		bg2CountRows(t, nodePath, dbPath, "bot_event_inbox", "source_id = "+bg2SQLQuote(coalTimerRes.Watch.WatchID)) != 1 {
		t.Fatal("missed timer inbox row mismatch")
	}

	listPage1, err := proc.client.ListWatches(ctx, pi.ProductWatchListParams{BotID: bot.BotID, Limit: 1})
	if err != nil || listPage1.NextCursor == nil || *listPage1.NextCursor == "" {
		t.Fatalf("ListWatches page1 = %+v err=%v", listPage1, err)
	}
	_, err = proc.client.ListWatches(ctx, pi.ProductWatchListParams{BotID: bot.BotID, Kind: pi.ProductWatchTimer, Cursor: *listPage1.NextCursor})
	cp1AssertRemoteCode(t, err, "invalid_cursor")
	_, err = proc.client.ListWatches(ctx, pi.ProductWatchListParams{BotID: sibling.BotID, Cursor: *listPage1.NextCursor})
	cp1AssertRemoteCode(t, err, "invalid_cursor")

	grantsPage1, err := proc.client.ListWatchGrants(ctx, pi.ProductWatchGrantListParams{BotID: bot.BotID, IncludeRevoked: true, Limit: 1})
	if err != nil || grantsPage1.NextCursor == nil || *grantsPage1.NextCursor == "" {
		t.Fatalf("ListWatchGrants page1 = %+v err=%v", grantsPage1, err)
	}
	_, err = proc.client.ListWatchGrants(ctx, pi.ProductWatchGrantListParams{BotID: bot.BotID, IncludeRevoked: false, Cursor: *grantsPage1.NextCursor})
	cp1AssertRemoteCode(t, err, "invalid_cursor")

	sessOpenRaw := runToolTurn(bot, bot.CurrentConversationID, "session_open", map[string]any{"mode": "pipe", "command": "/usr/bin/true"})
	var sessSum pi.ProductSessionSummary
	if err := json.Unmarshal([]byte(sessOpenRaw), &sessSum); err != nil {
		t.Fatal(err)
	}
	sessWatchRaw := runToolTurn(bot, bot.CurrentConversationID, "session_watch", map[string]any{
		"action": "arm", "session_id": sessSum.SessionID, "generation": 1, "on_exit": true,
	})
	var sessWatchObj struct {
		WatchID string `json:"watchId"`
	}
	if err := json.Unmarshal([]byte(sessWatchRaw), &sessWatchObj); err != nil || sessWatchObj.WatchID == "" {
		t.Fatalf("session_watch raw=%s err=%v", sessWatchRaw, err)
	}
	bg1WaitUntil(t, ctx, "session_watch_fired in inbox", func() bool {
		return bg2CountRows(t, nodePath, dbPath, "bot_event_inbox",
			"source_kind = 'session_watch_fired' AND source_id = "+bg2SQLQuote(sessWatchObj.WatchID)) > 0
	})

	actScopeKey := "bg2-act-goal-key"
	actScopePlan := &plannedToolCall{
		Name: "watch_create",
		Args: fmt.Sprintf(`{"kind":"schedule","purpose":"Activity owned watch","schedule":{"cadenceKind":"interval","intervalSeconds":10,"timeZone":"UTC","jitterSeconds":0}}`),
	}
	mu.Lock()
	plans[actScopeKey] = actScopePlan
	mu.Unlock()
	delegRaw := runToolTurn(bot, bot.CurrentConversationID, "activity_delegate", map[string]any{
		"key": "bg2-act-key", "goal": actScopeKey, "assignee_id": bot.BotID,
	})
	var delegReceipt pi.ProductReceipt
	if err := json.Unmarshal([]byte(delegRaw), &delegReceipt); err != nil || delegReceipt.Target.ActivityID == "" {
		t.Fatalf("activity_delegate raw=%s err=%v", delegRaw, err)
	}
	actWatchReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Dormant backup check",
		Schedule: interval10, Precheck: precheckObs, OperationID: "bg2-save-live-backup",
	})
	if err != nil {
		t.Fatal(err)
	}
	actWatchRes, err := pi.DecodeWatchMutationResult(actWatchReceipt)
	if err != nil || actWatchRes.Watch.State != pi.ProductWatchActive || actWatchRes.Watch.GrantID == "" {
		t.Fatalf("expected active watch for backup/workspace check: %+v err=%v", actWatchRes, err)
	}
	bg1WaitUntil(t, ctx, "activity_milestone in inbox", func() bool {
		return bg2CountRows(t, nodePath, dbPath, "bot_event_inbox",
			"source_kind = 'activity_milestone' AND source_id = "+bg2SQLQuote(delegReceipt.Target.ActivityID)) > 0
	})
	mu.Lock()
	actCreatedRaw := actScopePlan.Result
	mu.Unlock()
	var actCreatedReceipt pi.ProductReceipt
	if err := json.Unmarshal([]byte(actCreatedRaw), &actCreatedReceipt); err != nil || actCreatedReceipt.Target.WatchID == "" {
		t.Fatalf("activity watch_create result=%q err=%v", actCreatedRaw, err)
	}
	fgCrossRaw := runToolTurn(bot, bot.CurrentConversationID, "watch_pause", map[string]any{
		"watch_id": actCreatedReceipt.Target.WatchID, "expected_revision": 1,
	})
	var fgCrossErr map[string]any
	if err := json.Unmarshal([]byte(fgCrossRaw), &fgCrossErr); err != nil || fgCrossErr["code"] != "scope_denied" {
		t.Fatalf("foreground watch_pause on activity watch = %s, want scope_denied", fgCrossRaw)
	}
	if _, err := proc.client.PauseWatch(ctx, pi.ProductWatchStateMutationParams{
		BotID: bot.BotID, WatchID: actCreatedReceipt.Target.WatchID, ExpectedRevision: 1, OperationID: "bg2-gui-pause-act-watch",
	}); err != nil {
		t.Fatal(err)
	}

	bg1WaitUntil(t, ctx, "orchestration idle before backup", func() bool {
		for _, botID := range []string{bot.BotID, sibling.BotID} {
			orchs, err := proc.client.ListOrchestrations(ctx, pi.ProductOrchestrationListParams{BotID: botID, Limit: 50})
			if err != nil {
				return false
			}
			for _, item := range orchs.Items {
				if item.CurrentRunID != "" {
					return false
				}
			}
		}
		return bg2CountRows(t, nodePath, dbPath, "bot_event_inbox", "mandate_id IS NOT NULL AND state IN ('pending','claimed')") == 0
	})

	backupPath := filepath.Join(root, "bg2-backup.json")
	callsBeforeBackup := provider.calls.Load()
	canonicalBeforeBackup := productRead(t, opts.ProfilePath)
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(productRead(t, opts.ProfilePath), canonicalBeforeBackup) {
		t.Fatal("profile.backup modified contacts.json mirror")
	}
	backupRaw := productRead(t, backupPath)
	for _, forbidden := range []string{providerKey, mcpSecret, actWatchRes.Watch.GrantID, `"watchGrants"`, `"botEventInbox"`, `"botEventBatches"`, `"installationId"`} {
		if forbidden != "" && bytes.Contains(backupRaw, []byte(forbidden)) {
			t.Fatalf("backup leaked forbidden token %q", forbidden)
		}
	}

	var backupEnvelope struct {
		Backup struct {
			Watches []pi.ProductWatchSummary `json:"watches"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(backupRaw, &backupEnvelope); err != nil {
		t.Fatal(err)
	}
	expectedScheds := 0
	expectedTimers := 0
	expectedDormant := 0
	for _, w := range backupEnvelope.Backup.Watches {
		if w.State == pi.ProductWatchCancelled {
			continue
		}
		if w.Kind == pi.ProductWatchSchedule {
			expectedScheds++
		} else {
			expectedTimers++
		}
		if w.State == pi.ProductWatchRestored {
			expectedDormant++
		}
	}
	inspectJSON := bg2RunNodeJSON(t, nodePath, fmt.Sprintf(`
import { readFileSync } from 'node:fs';
const { inspectProductRecovery } = await import(%q);
console.log(JSON.stringify(inspectProductRecovery(JSON.parse(readFileSync(process.argv[1], 'utf8')))));
`, "file://"+filepath.Join(filepath.Dir(workerPath), "product-profile.js")), backupPath)
	var recoveredCounts struct {
		Contacts        int `json:"contacts"`
		Activities      int `json:"activities"`
		Timers          int `json:"timers"`
		Schedules       int `json:"schedules"`
		DormantWatches  int `json:"dormantWatches"`
		Notices         int `json:"notices"`
		Decisions       int `json:"decisions"`
		DormantMandates int `json:"dormantMandates"`
	}
	if err := json.Unmarshal([]byte(inspectJSON), &recoveredCounts); err != nil {
		t.Fatal(err)
	}
	if recoveredCounts.Contacts != 2 || recoveredCounts.Activities != 1 || recoveredCounts.Timers != expectedTimers ||
		recoveredCounts.Schedules != expectedScheds || recoveredCounts.DormantWatches != expectedDormant || expectedDormant == 0 ||
		recoveredCounts.DormantMandates == 0 {
		t.Fatalf("inspectProductRecovery mismatch: got=%+v want timers=%d schedules=%d dormant=%d",
			recoveredCounts, expectedTimers, expectedScheds, expectedDormant)
	}

	restoreDir := filepath.Join(root, "restored")
	if err := os.MkdirAll(restoreDir, 0o700); err != nil {
		t.Fatal(err)
	}
	restoreProfile := filepath.Join(restoreDir, "contacts.json")
	if err := os.WriteFile(restoreProfile, backupRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	restoreOpts := pi.ProductOptions{
		NodePath: nodePath, WorkerPath: workerPath,
		StateDir: filepath.Join(restoreDir, "pi"), ProfilePath: restoreProfile, CredentialsPath: filepath.Join(restoreDir, "connection.json"),
	}
	restoreDB := filepath.Join(restoreDir, "xuhu.db")
	for round := 0; round < 2; round++ {
		rProc := startCP1Process(t, restoreOpts)
		_ = rProc.open(t, ctx)
		rWatches, err := rProc.client.ListWatches(ctx, pi.ProductWatchListParams{BotID: bot.BotID, Limit: 50})
		if err != nil || len(rWatches.Items) == 0 {
			t.Fatalf("restored ListWatches round %d = %+v err=%v", round, rWatches, err)
		}
		for _, rw := range rWatches.Items {
			if rw.Armed || rw.GrantID != "" || rw.NextDueAt != "" {
				t.Fatalf("restored watch not dormant in round %d: %+v", round, rw)
			}
			if rw.WatchID == actWatchRes.Watch.WatchID && (rw.State != pi.ProductWatchRestored || rw.StatusReason != "restored_dormant") {
				t.Fatalf("expected active watch restored dormant, got %+v", rw)
			}
		}
		if bg2CountRows(t, nodePath, restoreDB, "watch_grants", "") != 0 ||
			bg2CountRows(t, nodePath, restoreDB, "bot_event_inbox", "") != 0 ||
			bg2CountRows(t, nodePath, restoreDB, "bot_event_batches", "") != 0 {
			t.Fatalf("restored DB contained grants/inbox/batches in round %d", round)
		}
		rProc.shutdown(t, ctx)
	}
	if provider.calls.Load() != callsBeforeBackup {
		t.Fatalf("backup/restore triggered %d provider calls, want 0", provider.calls.Load()-callsBeforeBackup)
	}

	convNow, err := proc.client.GetConversation(ctx, bot.CurrentConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: bot.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "Unrelated draft survives watches", Materials: []pi.ProductMaterial{}},
		ExpectedRevision:          convNow.Draft.Revision,
		OperationID:               "bg2-unrelated-draft-ok",
	}); err != nil {
		t.Fatal(err)
	}
	stillWatch, err := proc.client.GetWatch(ctx, bot.BotID, actWatchRes.Watch.WatchID)
	if err != nil || stillWatch.State != pi.ProductWatchActive {
		t.Fatalf("unrelated SaveDraft harmed active watch: %+v err=%v", stillWatch, err)
	}

	botsList, err := proc.client.ListBots(ctx, pi.ProductPageParams{})
	if err != nil {
		t.Fatal(err)
	}
	var curBot pi.ProductBot
	for _, item := range botsList.Items {
		if item.BotID == bot.BotID {
			curBot = item
		}
	}
	movedProf := curBot.Profile
	movedProf.Workspace = movedWS
	updatedBot, err := proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			OperationID: "bg2-move-ws", BotID: bot.BotID, ExpectedRevision: curBot.ProfileRevision,
		},
		Profile: movedProf,
	})
	if err != nil {
		t.Fatal(err)
	}
	wsInv, err := proc.client.GetWatch(ctx, bot.BotID, actWatchRes.Watch.WatchID)
	if err != nil || wsInv.State != pi.ProductWatchInvalidated || wsInv.StatusReason != "workspace_changed" || wsInv.Grant.State != "revoked" {
		t.Fatalf("workspace change did not invalidate precheck watch: %+v err=%v", wsInv, err)
	}

	writeWorkspaceState(movedWS, "S")
	scratchRoot, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	priorScratch := bg2SnapshotScratchEntries(t, scratchRoot)
	shutdownReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Shutdown reaps active child", Schedule: interval10,
		Precheck:    &pi.ProductWatchPrecheck{Kind: "executable", Executable: observerBin, WorkspaceRead: true, TimeoutMS: intPtr(30000)},
		OperationID: "bg2-save-shutdown",
	})
	if err != nil {
		t.Fatal(err)
	}
	shutdownRes, _ := pi.DecodeWatchMutationResult(shutdownReceipt)
	setClock(bg2ParseISO(t, shutdownRes.Watch.NextDueAt))
	shutdownPID := bg2WaitPrecheckPID(t, ctx, scratchRoot, priorScratch)
	if bg1ProcessGone(shutdownPID) {
		t.Fatalf("expected precheck child %d alive before shutdown", shutdownPID)
	}
	proc.shutdown(t, ctx)
	bg1WaitUntil(t, ctx, "graceful shutdown precheck child reaped", func() bool {
		return bg1ProcessGone(shutdownPID)
	})

	proc = startCP1Process(t, opts)
	_ = proc.open(t, ctx)
	afterShutdownWatch, err := proc.client.GetWatch(ctx, bot.BotID, shutdownRes.Watch.WatchID)
	if err != nil || len(afterShutdownWatch.RecentOccurrences) == 0 || afterShutdownWatch.RecentOccurrences[0].State != "cancelled" {
		t.Fatalf("shutdown occurrence not cancelled: %+v err=%v", afterShutdownWatch, err)
	}
	if _, err := proc.client.PauseWatch(ctx, pi.ProductWatchStateMutationParams{
		BotID: bot.BotID, WatchID: shutdownRes.Watch.WatchID, ExpectedRevision: afterShutdownWatch.Revision, OperationID: "bg2-pause-shutdown-w",
	}); err != nil {
		t.Fatal(err)
	}

	priorScratch2 := bg2SnapshotScratchEntries(t, scratchRoot)
	crashReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "SIGKILL crash unknown test", Schedule: interval10,
		Precheck:    &pi.ProductWatchPrecheck{Kind: "executable", Executable: observerBin, WorkspaceRead: true, TimeoutMS: intPtr(30000)},
		OperationID: "bg2-save-crash",
	})
	if err != nil {
		t.Fatal(err)
	}
	crashRes, _ := pi.DecodeWatchMutationResult(crashReceipt)
	setClock(bg2ParseISO(t, crashRes.Watch.NextDueAt))
	crashedPID := bg2WaitPrecheckPID(t, ctx, scratchRoot, priorScratch2)
	killOutcome, killErr := proc.client.Kill(ctx)
	proc.exit(t, ctx, killOutcome, false, killErr)
	bg1WaitUntil(t, ctx, "SIGKILL worker precheck child reaped", func() bool {
		return bg1ProcessGone(crashedPID)
	})

	t.Setenv("NAGI_FORCE_SANDBOX_UNAVAILABLE", "1")
	proc = startCP1Process(t, opts)
	_ = proc.open(t, ctx)
	afterCrashWatch, err := proc.client.GetWatch(ctx, bot.BotID, crashRes.Watch.WatchID)
	if err != nil || len(afterCrashWatch.RecentOccurrences) != 1 || afterCrashWatch.RecentOccurrences[0].State != "unknown" {
		t.Fatalf("SIGKILL interrupted occurrence not reconciled to unknown: %+v err=%v", afterCrashWatch, err)
	}
	_, forcedOcc := tickWatch(crashRes.Watch.WatchID, "", "failed")
	if forcedOcc.Error != "sandbox_unavailable" {
		t.Fatalf("forced sandbox unavailability error = %q, want sandbox_unavailable", forcedOcc.Error)
	}
	t.Setenv("NAGI_FORCE_SANDBOX_UNAVAILABLE", "")
	pauseCrash, _ := proc.client.GetWatch(ctx, bot.BotID, crashRes.Watch.WatchID)
	if _, err := proc.client.PauseWatch(ctx, pi.ProductWatchStateMutationParams{
		BotID: bot.BotID, WatchID: crashRes.Watch.WatchID, ExpectedRevision: pauseCrash.Revision, OperationID: "bg2-pause-crash-w",
	}); err != nil {
		t.Fatal(err)
	}

	archPlainReceipt, err := proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Archive pause fence", Schedule: interval10, OperationID: "bg2-save-archive-fence",
	})
	if err != nil {
		t.Fatal(err)
	}
	archPlainRes, _ := pi.DecodeWatchMutationResult(archPlainReceipt)
	_, err = proc.client.ArchiveBot(ctx, pi.ProductBotRevisionParams{
		OperationID: "bg2-archive-bot", BotID: bot.BotID, ExpectedRevision: updatedBot.ProfileRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	archivedWatch, err := proc.client.GetWatch(ctx, bot.BotID, archPlainRes.Watch.WatchID)
	if err != nil || archivedWatch.State != pi.ProductWatchPaused || archivedWatch.StatusReason != "bot_archived" {
		t.Fatalf("archive did not pause watch: %+v err=%v", archivedWatch, err)
	}
	_, err = proc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: bot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Archived bot blocked", Schedule: interval10, OperationID: "bg2-archived-save",
	})
	cp1AssertRemoteCode(t, err, "invalid_state")
	proc.shutdown(t, ctx)

	sqliteModuleURL := "file://" + filepath.Join(filepath.Dir(workerPath), "product-sqlite.js")
	stagedJSON := bg2RunNodeJSON(t, nodePath, fmt.Sprintf(`
const { ProductSQLite } = await import(%q);
const { join } = await import('node:path');
const [dir, botId] = process.argv.slice(1);
const paths = { profilePath: join(dir, 'contacts.json'), credentialsPath: join(dir, 'connection.json'), fresh: false };
const s = new ProductSQLite(paths.profilePath, paths.credentialsPath);
s.open(paths, s.credentials(), 'bg2-batch-test');
s.recoverRuntimeSessions();
const batch = s.claimEventBatch(botId, 'lease-staged');
s.close();
console.log(JSON.stringify(batch));
`, sqliteModuleURL), profileDir, bot.BotID)
	var stagedBatch struct {
		BatchID  string   `json:"batchId"`
		Stage    string   `json:"stage"`
		EventIDs []string `json:"eventIds"`
	}
	if err := json.Unmarshal([]byte(stagedJSON), &stagedBatch); err != nil || stagedBatch.Stage != "staged" || len(stagedBatch.EventIDs) == 0 {
		t.Fatalf("claimEventBatch staged = %s err=%v", stagedJSON, err)
	}
	t.Run("HeldBatchReconciliation", func(t *testing.T) {
		t.Setenv("NAGI_ORCHESTRATION_HOLD", "1")
		callsBeforeBatches := provider.calls.Load()
		proc = startCP1Process(t, opts)
		_ = proc.open(t, ctx)
		proc.shutdown(t, ctx)
		releasedStage := runNodeSQLite(t, nodePath, dbPath, fmt.Sprintf(`
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const stage = db.prepare('SELECT stage FROM bot_event_batches WHERE batch_id = ?').get(JSON.stringify(%q)).stage;
const ids = %s;
const placeholders = ids.map(() => '?').join(',');
const pending = db.prepare('SELECT count(*) AS n FROM bot_event_inbox WHERE event_id IN (' + placeholders + ") AND state = 'pending'").get(...ids.map(id => JSON.stringify(id))).n;
db.close();
console.log(JSON.stringify({ stage, pending }));
`, stagedBatch.BatchID, func() string {
			b, _ := json.Marshal(stagedBatch.EventIDs)
			return string(b)
		}()))
		if releasedStage != fmt.Sprintf(`{"stage":"released","pending":%d}`, len(stagedBatch.EventIDs)) {
			t.Fatalf("staged batch reopen mismatch = %s", releasedStage)
		}

		dispJSON := bg2RunNodeJSON(t, nodePath, fmt.Sprintf(`
const { ProductSQLite } = await import(%q);
const { join } = await import('node:path');
const [dir, botId] = process.argv.slice(1);
const paths = { profilePath: join(dir, 'contacts.json'), credentialsPath: join(dir, 'connection.json'), fresh: false };
const s = new ProductSQLite(paths.profilePath, paths.credentialsPath);
s.open(paths, s.credentials(), 'bg2-batch-test');
s.recoverRuntimeSessions();
const batch = s.claimEventBatch(botId, 'lease-dispatched');
const disp = s.markBatchDispatched(batch.batchId);
s.close();
console.log(JSON.stringify(disp));
`, sqliteModuleURL), profileDir, bot.BotID)
		var dispBatch struct {
			BatchID  string   `json:"batchId"`
			Stage    string   `json:"stage"`
			EventIDs []string `json:"eventIds"`
		}
		if err := json.Unmarshal([]byte(dispJSON), &dispBatch); err != nil || dispBatch.Stage != "dispatched" {
			t.Fatalf("markBatchDispatched = %s err=%v", dispJSON, err)
		}
		proc = startCP1Process(t, opts)
		_ = proc.open(t, ctx)
		proc.shutdown(t, ctx)
		unknownStage := runNodeSQLite(t, nodePath, dbPath, fmt.Sprintf(`
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const bid = JSON.stringify(%q);
const stage = db.prepare('SELECT stage FROM bot_event_batches WHERE batch_id = ?').get(bid).stage;
const unk = db.prepare("SELECT count(*) AS n FROM bot_event_inbox WHERE batch_id = ? AND state = 'unknown'").get(bid).n;
db.close();
console.log(JSON.stringify({ stage, unk }));
`, dispBatch.BatchID))
		if unknownStage != fmt.Sprintf(`{"stage":"unknown","unk":%d}`, len(dispBatch.EventIDs)) {
			t.Fatalf("dispatched batch reopen mismatch = %s", unknownStage)
		}
		if provider.calls.Load() != callsBeforeBatches {
			t.Fatalf("batch reconciliation replayed provider calls: before=%d after=%d", callsBeforeBatches, provider.calls.Load())
		}
	})

	for _, fromVer := range []int{11, 12} {
		mDir := filepath.Join(root, fmt.Sprintf("mig-v%d", fromVer))
		if err := os.MkdirAll(mDir, 0o700); err != nil {
			t.Fatal(err)
		}
		mOpts := pi.ProductOptions{
			NodePath: nodePath, WorkerPath: workerPath,
			StateDir: filepath.Join(mDir, "pi"), ProfilePath: filepath.Join(mDir, "contacts.json"), CredentialsPath: filepath.Join(mDir, "connection.json"),
			FreshProfile: true,
		}
		mProc := startCP1Process(t, mOpts)
		_ = mProc.open(t, ctx)
		if _, err := mProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
			OperationID: fmt.Sprintf("mig-bot-%d", fromVer),
			Profile: pi.ProductBotProfile{
				Name: "MigBot", Role: "R", Purpose: "P", Instructions: "I", Workspace: ws, CodingTools: nil,
			},
		}); err != nil {
			t.Fatal(err)
		}
		mProc.shutdown(t, ctx)

		mDB := filepath.Join(mDir, "xuhu.db")
		dropSQL := "DROP TABLE orchestration_admissions; DROP TABLE conversation_notices; DROP TABLE conversation_timelines; DROP TABLE orchestration_decisions; DROP TABLE orchestration_runs; DROP TABLE orchestration_checkpoints; DROP TABLE orchestration_backoff; DROP TABLE queued_conversation_sends; DROP TABLE bot_event_inbox; DROP TABLE orchestration_mandates; DELETE FROM meta WHERE key GLOB 'orchestration_operation:*'; ALTER TABLE activities DROP COLUMN origin_mandate_id; DROP TABLE bot_event_batches; DROP TABLE watch_occurrences; DROP TABLE runtime_watches; DROP TABLE watch_grants; DELETE FROM meta WHERE key GLOB 'watch_operation:*'; PRAGMA user_version = 12;"
		if fromVer == 11 {
			dropSQL = "DROP TABLE orchestration_admissions; DROP TABLE conversation_notices; DROP TABLE conversation_timelines; DROP TABLE orchestration_decisions; DROP TABLE orchestration_runs; DROP TABLE orchestration_checkpoints; DROP TABLE orchestration_backoff; DROP TABLE queued_conversation_sends; DROP TABLE bot_event_inbox; DROP TABLE orchestration_mandates; DELETE FROM meta WHERE key GLOB 'orchestration_operation:*'; ALTER TABLE activities DROP COLUMN origin_mandate_id; DROP TABLE bot_event_batches; DROP TABLE watch_occurrences; DROP TABLE runtime_watches; DROP TABLE watch_grants; DELETE FROM meta WHERE key GLOB 'watch_operation:*'; DROP TABLE runtime_watch_occurrences; DROP TABLE session_watches; DROP TABLE runtime_sessions; DELETE FROM meta WHERE key = 'runtime_session_epoch'; PRAGMA user_version = 11;"
		}
		runNodeSQLite(t, nodePath, mDB, fmt.Sprintf(`
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec(%q);
db.close();
`, dropSQL))
		canonBefore := productRead(t, mOpts.ProfilePath)
		mOpts.FreshProfile = false
		mProc = startCP1Process(t, mOpts)
		_ = mProc.open(t, ctx)
		mProc.shutdown(t, ctx)
		if !bytes.Equal(productRead(t, mOpts.ProfilePath), canonBefore) {
			t.Fatalf("v%d->v14 altered contacts.json bytes", fromVer)
		}
		vCheck := runNodeSQLite(t, nodePath, mDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.exec('PRAGMA user_version = 15');
db.close();
console.log(JSON.stringify({ v, fk }));
`)
		if vCheck != `{"v":14,"fk":0}` {
			t.Fatalf("v%d->v14 schema check = %s", fromVer, vCheck)
		}
		v14Client, startErr := pi.StartProduct(mOpts)
		if startErr == nil {
			if _, readyErr := v14Client.Ready(ctx); readyErr == nil {
				if _, openErr := v14Client.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
					t.Fatalf("worker admitted user_version=15 on v%d dir", fromVer)
				}
			}
			_, _ = v14Client.Wait(ctx)
		}
		after14 := runNodeSQLite(t, nodePath, mDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const bots = db.prepare('SELECT count(*) AS n FROM bots').get().n;
db.close();
console.log(JSON.stringify({ v, bots }));
`)
		if after14 != `{"v":15,"bots":1}` {
			t.Fatalf("v15 refusal mutated DB: %s", after14)
		}
	}

	txDir := filepath.Join(root, "tx-rollback")
	if err := os.MkdirAll(txDir, 0o700); err != nil {
		t.Fatal(err)
	}
	txOpts := pi.ProductOptions{
		NodePath: nodePath, WorkerPath: workerPath,
		StateDir: filepath.Join(txDir, "pi"), ProfilePath: filepath.Join(txDir, "contacts.json"), CredentialsPath: filepath.Join(txDir, "connection.json"),
		FreshProfile: true,
	}
	txProc := startCP1Process(t, txOpts)
	_ = txProc.open(t, ctx)
	txBot, err := txProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "tx-bot",
		Profile:     pi.ProductBotProfile{Name: "TxBot", Role: "R", Purpose: "P", Instructions: "I", Workspace: ws, CodingTools: nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	txSchedReceipt, err := txProc.client.SaveWatch(ctx, pi.ProductWatchSaveParams{
		BotID: txBot.BotID, Kind: pi.ProductWatchSchedule, Purpose: "Atomic trigger check", Schedule: interval10, OperationID: "tx-save-sched",
	})
	if err != nil {
		t.Fatal(err)
	}
	txSchedRes, _ := pi.DecodeWatchMutationResult(txSchedReceipt)
	txProc.shutdown(t, ctx)

	txDB := filepath.Join(txDir, "xuhu.db")
	runNodeSQLite(t, nodePath, txDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec("CREATE TRIGGER force_inbox_abort BEFORE INSERT ON bot_event_inbox WHEN NEW.source_kind = 'schedule_tick' BEGIN SELECT RAISE(ABORT, 'inject inbox failure'); END;");
db.close();
`)
	txOpts.FreshProfile = false
	txClient, startErr := pi.StartProduct(txOpts)
	if startErr != nil {
		t.Fatal(startErr)
	}
	if _, err := txClient.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := txClient.Open(ctx, []int{pi.ProductVersion}); err != nil {
		t.Fatal(err)
	}
	setClock(bg2ParseISO(t, txSchedRes.Watch.NextDueAt))
	txOut, _ := txClient.Wait(ctx)
	if !txOut.Reaped || txOut.ExitCode == 0 {
		t.Fatalf("expected non-zero exit after injected inbox abort, got %+v", txOut)
	}
	txState := runNodeSQLite(t, nodePath, txDB, fmt.Sprintf(`
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const row = JSON.parse(db.prepare('SELECT record_json FROM runtime_watches WHERE watch_id = ?').get(JSON.stringify(%q)).record_json);
const occs = db.prepare("SELECT count(*) AS n FROM watch_occurrences").get().n;
const inbox = db.prepare("SELECT count(*) AS n FROM bot_event_inbox").get().n;
db.close();
console.log(JSON.stringify({ nextDueAt: row.nextDueAt, occs, inbox }));
`, txSchedRes.Watch.WatchID))
	if txState != fmt.Sprintf(`{"nextDueAt":%q,"occs":0,"inbox":0}`, txSchedRes.Watch.NextDueAt) {
		t.Fatalf("single-tx rollback invariant violated: %s", txState)
	}
	provider.assertNoProblems()
}
