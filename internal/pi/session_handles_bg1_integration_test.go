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
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
)

const bg1PtyFixtureSource = `#define _DARWIN_C_SOURCE
#include <errno.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/ioctl.h>
#include <termios.h>
#include <unistd.h>

static volatile sig_atomic_t interrupted;
static volatile sig_atomic_t resized;
static void onint(int sig) { (void)sig; interrupted++; }
static void onwinch(int sig) { (void)sig; resized = 1; }

int main(void) {
    struct termios normal, raw, auth;
    if (!isatty(0) || !isatty(1) || !isatty(2) || tcgetattr(0, &normal) < 0) return 2;
    struct sigaction sa = {.sa_handler = onint};
    sigemptyset(&sa.sa_mask);
    if (sigaction(SIGINT, &sa, NULL) < 0) return 3;
    sa.sa_handler = onwinch;
    if (sigaction(SIGWINCH, &sa, NULL) < 0) return 4;
    setvbuf(stdout, NULL, _IONBF, 0);
    const char *path = getenv("PATH");
    const char *sentinel = getenv("NAGI_BG1_SENTINEL");
    printf("PTY_READY:%ld\nPTY_PATH:%s\nPTY_SENTINEL:%s\n", (long)getpid(), path ? path : "", sentinel ? sentinel : "unset");
    while (!resized || !interrupted) {
        struct winsize size;
        if (resized == 1 && ioctl(0, TIOCGWINSZ, &size) == 0) {
            printf("RESIZED:%ux%u\n", size.ws_col, size.ws_row);
            resized = 2;
        }
        if (!interrupted || !resized) usleep(10000);
    }
    puts("CANONICAL_SIGINT");
    raw = normal;
    cfmakeraw(&raw);
    if (tcsetattr(0, TCSANOW, &raw) < 0) return 5;
    puts("RAW_READY");
    unsigned char byte;
    if (read(0, &byte, 1) != 1 || byte != 3) return 6;
    puts("RAW_BYTE_03");
    auth = normal;
    auth.c_lflag &= ~ECHO;
    if (tcsetattr(0, TCSANOW, &auth) < 0) return 7;
    puts("Password:");
    for (int tick = 0; tick < 500; tick++) {
        if (interrupted >= 2) break;
        usleep(10000);
    }
    if (tcsetattr(0, TCSANOW, &normal) < 0) return 8;
    puts("ECHO_RESTORED");
    for (;;) {
        if (read(0, &byte, 1) != 1) return 9;
        if (byte == '\n') break;
    }
    puts("PTY_DONE");
    return 37;
}
`

func bg1CompilePtyFixture(t *testing.T, dir string) string {
	t.Helper()
	src := filepath.Join(dir, "pty-fixture.c")
	bin := filepath.Join(dir, "pty-fixture")
	if err := os.WriteFile(src, []byte(bg1PtyFixtureSource), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/usr/bin/xcrun", "clang", "-std=c11", "-O2", "-Wall", "-Wextra", "-Werror", src, "-o", bin).CombinedOutput()
	if err != nil {
		t.Fatalf("compile pty fixture: %v\n%s", err, out)
	}
	return bin
}

func bg1PipeFixtureScript(partA, partB, rotProvPartA, rotProvPartB, rotMcpPartA, rotMcpPartB, actMcpPartA, actMcpPartB, inactivePartA, inactivePartB, nextProvSecret string) string {
	return fmt.Sprintf(`import { createInterface } from 'node:readline';
console.log('PID:' + process.pid);
console.log('ENV_KEYS:' + Object.keys(process.env).sort().join(','));
console.log('ENV_PATH:' + (process.env.PATH ?? ''));
process.on('SIGHUP', () => {});
process.on('SIGTERM', () => {});
if (process.argv[2] === 'linger') setInterval(() => {}, 1000);
const rl = createInterface({ input: process.stdin });
rl.on('line', line => {
  if (line === 'split_all') {
    process.stdout.write(Buffer.from([0xf0, 0x9f]));
    setTimeout(() => {
      process.stdout.write(Buffer.concat([Buffer.from([0x98, 0x80]), Buffer.from('\x1b[3')]));
      setTimeout(() => {
        process.stdout.write('1m' + %q);
        setTimeout(() => {
          process.stderr.write(%q + ' ' + %q);
          setTimeout(() => {
            process.stdout.write(%q + ' ' + %q);
            setTimeout(() => {
              process.stderr.write(%q + ' ' + %q);
              setTimeout(() => {
                process.stdout.write(%q + ' ' + %q);
                setTimeout(() => {
                  process.stderr.write(%q + ' MAT');
                  setTimeout(() => {
                    process.stdout.write('CHED_TOKEN SINGLE_');
                    setTimeout(() => {
                      process.stdout.write('WINNER\x1b]52;c;clip');
                      setTimeout(() => {
                        process.stdout.write('board\x1b\\\n');
                      }, 15);
                    }, 15);
                  }, 15);
                }, 15);
              }, 15);
            }, 15);
          }, 15);
        }, 15);
      }, 15);
    }, 15);
  } else if (line === 'rotated_secret') {
    process.stdout.write('ROT_OUT:' + %q + '\n');
  } else if (line === 'overflow') {
    process.stdout.write('界'.repeat(100000) + 'TAIL_MARK\n');
  }
});
rl.on('close', () => {
  process.stderr.write('STDERR_AFTER_EOF\n');
  process.exitCode = 21;
});
`, partA, partB, rotProvPartA, rotProvPartB, rotMcpPartA, rotMcpPartB, actMcpPartA, actMcpPartB, inactivePartA, inactivePartB, nextProvSecret)
}

func bg1WaitUntil(t *testing.T, ctx context.Context, label string, probe func() bool) {
	t.Helper()
	for {
		if probe() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s: %v", label, ctx.Err())
		case <-time.After(15 * time.Millisecond):
		}
	}
}

func bg1ParentPID(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("/bin/ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps ppid(%d): %v", pid, err)
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || ppid <= 0 {
		t.Fatalf("invalid ppid %q for pid %d: %v", string(out), pid, err)
	}
	return ppid
}

func bg1ProcessGone(pid int) bool {
	err := syscall.Kill(pid, 0)
	return errors.Is(err, syscall.ESRCH)
}

func bg1JoinedOutput(res pi.ProductSessionReadResult) string {
	var b strings.Builder
	for _, chunk := range res.Chunks {
		b.WriteString(chunk.Text)
	}
	return b.String()
}

func bg1WaitReadContains(t *testing.T, ctx context.Context, client *pi.ProductClient, botID, sessID string, gen uint64, offset *uint64, substr string) pi.ProductSessionReadResult {
	t.Helper()
	var latest pi.ProductSessionReadResult
	bg1WaitUntil(t, ctx, "output containing "+substr, func() bool {
		res, err := client.ReadSession(ctx, pi.ProductSessionReadParams{
			BotID: botID, SessionID: sessID, Generation: gen, Offset: offset, Limit: 65536,
		})
		if err != nil {
			t.Fatalf("ReadSession(%s): %v", sessID, err)
		}
		latest = res
		return strings.Contains(bg1JoinedOutput(res), substr)
	})
	return latest
}

func bg1WaitSession(t *testing.T, ctx context.Context, client *pi.ProductClient, botID, sessID, label string, pred func(pi.ProductSessionSummary) bool) pi.ProductSessionSummary {
	t.Helper()
	var got pi.ProductSessionSummary
	bg1WaitUntil(t, ctx, label, func() bool {
		sum, err := client.GetSession(ctx, botID, sessID)
		if err != nil {
			t.Fatalf("GetSession(%s): %v", sessID, err)
		}
		got = sum
		return pred(sum)
	})
	return got
}

func bg1ExtractIntAfter(t *testing.T, text, prefix string) int {
	t.Helper()
	_, after, ok := strings.Cut(text, prefix)
	if !ok {
		t.Fatalf("prefix %q missing in %q", prefix, text)
	}
	end := strings.IndexAny(after, "\r\n")
	if end >= 0 {
		after = after[:end]
	}
	val, err := strconv.Atoi(strings.TrimSpace(after))
	if err != nil || val <= 0 {
		t.Fatalf("invalid integer after %q in %q: %v", prefix, text, err)
	}
	return val
}

func bg1VerifyStandaloneEnvI(t *testing.T, ctx context.Context, nodePath, workerPath, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, nodePath, workerPath,
		"--state-dir", filepath.Join(dir, "pi"),
		"--product-profile", filepath.Join(dir, "contacts.json"),
		"--credentials", filepath.Join(dir, "connection.json"),
		"--fresh-product-profile",
	)
	cmd.Env = []string{"PATH=/nonexistent", "HOME=" + dir, "TMPDIR=" + dir, "LANG=en_US.UTF-8"}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	dec := json.NewDecoder(stdout)
	var ready struct {
		Type    string `json:"type"`
		Runtime struct {
			Version string `json:"version"`
		} `json:"runtime"`
	}
	if err := dec.Decode(&ready); err != nil || ready.Type != "ready" || ready.Runtime.Version != "v22.23.5" {
		t.Fatalf("env -i ready=%+v err=%v stderr=%s", ready, err, stderr.String())
	}
	sendFrame := func(id, method string, params any) {
		b, _ := json.Marshal(map[string]any{"v": 1, "type": "request", "id": id, "method": method, "params": params})
		b = append(b, '\n')
		if _, err := stdin.Write(b); err != nil {
			t.Fatal(err)
		}
		var resp struct {
			ID string `json:"id"`
			OK bool   `json:"ok"`
		}
		if err := dec.Decode(&resp); err != nil || resp.ID != id || !resp.OK {
			t.Fatalf("env -i %s resp=%+v err=%v stderr=%s", method, resp, err, stderr.String())
		}
	}
	sendFrame("1", "app.open", map[string]any{"supportedVersions": []int{1}})
	sendFrame("2", "app.shutdown", map[string]any{})
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("env -i shutdown exit: %v stderr=%s", err, stderr.String())
	}
}

func TestPiProductBG1SessionHandlesSupervisorAndWatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	nodePath, workerPath := mcpRequirePinnedWorker(t)
	supervisorBin := filepath.Join(filepath.Dir(filepath.Dir(nodePath)), "bin", "nagi-pty-supervisor")
	if info, err := os.Stat(supervisorBin); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("missing executable bundled supervisor %s: %v", supervisorBin, err)
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	ptyFixture := bg1CompilePtyFixture(t, root)
	bg1VerifyStandaloneEnvI(t, ctx, nodePath, workerPath, filepath.Join(root, "envi-check"))

	t.Setenv("HOME", root)
	t.Setenv("TMPDIR", root)
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("NAGI_BG1_SENTINEL", "private-nagi-control-sentinel")

	const (
		activeSecret   = "sk-bg1-active-provider-secret-1111"
		inactiveSecret = "sk-bg1-inactive-provider-2222"
		rotProvSecret  = "sk-bg1-rotated-provider-3333"
		nextProvSecret = "sk-bg1-next-provider-4444"
		rotMcpSecret   = "sk-bg1-rotated-mcp-vault-5555"
		actMcpSecret   = "sk-bg1-active-mcp-vault-6666"
	)

	var (
		turnSeq     atomic.Int64
		reqMu       sync.Mutex
		turnReplies = map[string]cp1Reply{}
		turnTools   = map[string][]string{}
		toolResults = map[string]string{}
	)
	provider := newCP1Provider(t, activeSecret, func(req cp1Request) cp1Reply {
		reqMu.Lock()
		defer reqMu.Unlock()
		if req.LastRole == "tool" && req.LastToolID != "" {
			toolResults[req.LastToolID] = req.ToolResults[req.LastToolID]
			return cp1Reply{Text: "BG1_STEP_DONE"}
		}
		if req.LastRole == "user" {
			turnTools[req.UserText] = append([]string(nil), req.Tools...)
			if reply, ok := turnReplies[req.UserText]; ok {
				delete(turnReplies, req.UserText)
				return reply
			}
		}
		return cp1Reply{Text: "BG1_OK"}
	})
	for _, secret := range []string{activeSecret, inactiveSecret, rotProvSecret, nextProvSecret, rotMcpSecret, actMcpSecret} {
		provider.addForbiddenSecret(secret)
	}

	callModelTool := func(proc *cp1Process, convID, toolName string, args map[string]any) (string, []string) {
		t.Helper()
		token := fmt.Sprintf("BG1TURN_%d", turnSeq.Add(1))
		callID := "call_" + token
		reqMu.Lock()
		if toolName == "" {
			turnReplies[token] = cp1Reply{Text: "BG1_INVENTORY"}
		} else {
			rawArgs, err := json.Marshal(args)
			if err != nil {
				reqMu.Unlock()
				t.Fatal(err)
			}
			turnReplies[token] = cp1Reply{Calls: []cp1ToolCall{{ID: callID, Name: toolName, Args: string(rawArgs)}}}
		}
		reqMu.Unlock()
		settled := cp1SendTurn(t, ctx, proc, convID, token, token, nil)
		if settled.State != "completed" {
			t.Fatalf("turn %s (%s) settled = %+v", token, toolName, settled)
		}
		reqMu.Lock()
		defer reqMu.Unlock()
		return toolResults[callID], turnTools[token]
	}

	runActivityTool := func(proc *cp1Process, bot pi.ProductBot, toolName string, args map[string]any) (string, string) {
		t.Helper()
		actToken := fmt.Sprintf("BG1ACT_%d", turnSeq.Add(1))
		actCallID := "call_" + actToken
		rawArgs, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		reqMu.Lock()
		turnReplies[actToken] = cp1Reply{Calls: []cp1ToolCall{{ID: actCallID, Name: toolName, Args: string(rawArgs)}}}
		reqMu.Unlock()
		delRes, _ := callModelTool(proc, bot.CurrentConversationID, "activity_delegate", map[string]any{
			"key":         actToken,
			"goal":        actToken,
			"assignee_id": bot.BotID,
		})
		var receipt pi.ProductReceipt
		if err := json.Unmarshal([]byte(delRes), &receipt); err != nil || receipt.Target.ActivityID == "" || receipt.Execution == nil {
			t.Fatalf("activity_delegate result=%s err=%v", delRes, err)
		}
		settled := proc.waitSettled(t, ctx, receipt.Execution.ExecutionID)
		if settled.State != "completed" {
			t.Fatalf("activity %s settled=%+v", receipt.Target.ActivityID, settled)
		}
		reqMu.Lock()
		res := toolResults[actCallID]
		reqMu.Unlock()
		return receipt.Target.ActivityID, res
	}

	mustDecode := func(raw string, dst any) {
		t.Helper()
		if err := json.Unmarshal([]byte(raw), dst); err != nil {
			t.Fatalf("unmarshal %q: %v", raw, err)
		}
	}

	profileDir := filepath.Join(root, "live-profile")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(profileDir, "pi"),
		ProfilePath:     filepath.Join(profileDir, "contacts.json"),
		CredentialsPath: filepath.Join(profileDir, "connection.json"),
		FreshProfile:    true,
	}
	proc := startCP1Process(t, opts)
	_ = proc.open(t, ctx)

	cp1ConfigureConnection(t, ctx, proc.client, provider.server.URL+"/v1", activeSecret, workspace)

	if _, err := proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		OperationID: "bg1-save-inactive-prov",
		ProductProviderTestParams: pi.ProductProviderTestParams{
			Account: pi.ProductProviderAccount{
				ProviderID: "bg1-inactive", Name: "Inactive", WireAPI: pi.OpenAICompletions,
				BaseURL: provider.server.URL + "/v1", DefaultModel: productModel,
				ContextTokens: 65536, OutputTokens: 4096, Enabled: false,
			},
			APIKey: inactiveSecret,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		OperationID: "bg1-save-rot-prov-1",
		ProductProviderTestParams: pi.ProductProviderTestParams{
			Account: pi.ProductProviderAccount{
				ProviderID: "bg1-rotated", Name: "Rotated", WireAPI: pi.OpenAICompletions,
				BaseURL: provider.server.URL + "/v1", DefaultModel: productModel,
				ContextTokens: 65536, OutputTokens: 4096, Enabled: false,
			},
			APIKey: rotProvSecret,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveProvider(ctx, pi.ProductProviderSaveParams{
		OperationID: "bg1-save-rot-prov-2",
		ProductProviderTestParams: pi.ProductProviderTestParams{
			Account: pi.ProductProviderAccount{
				ProviderID: "bg1-rotated", Name: "Rotated", WireAPI: pi.OpenAICompletions,
				BaseURL: provider.server.URL + "/v1", DefaultModel: productModel,
				ContextTokens: 65536, OutputTokens: 4096, Enabled: false,
			},
			APIKey: nextProvSecret,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.RevokeProviderCredential(ctx, pi.ProductProviderTargetParams{
		ProviderID: "bg1-rotated", OperationID: "bg1-revoke-rot-prov",
	}); err != nil {
		t.Fatal(err)
	}
	mcpInput := pi.ProductMcpServerInput{
		ProductMcpServerConfig: pi.ProductMcpServerConfig{
			ServerID: "bg1-vault", Name: "bg1vault", Transport: "stdio", Isolation: "shared",
			Enabled: false, Command: nodePath, SecretEnvKeys: []string{"VAULT_TOK"},
		},
	}
	savedMcp, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server:      mcpInput,
		Secrets:     &pi.ProductMcpSecrets{Env: map[string]*string{"VAULT_TOK": mcpStrPtr(rotMcpSecret)}},
		OperationID: "bg1-save-mcp-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	mcpRev1 := savedMcp.Revision
	mcpInput.Revision = &mcpRev1
	if _, err := proc.client.SaveMcpServer(ctx, pi.ProductMcpSaveParams{
		Server:      mcpInput,
		Secrets:     &pi.ProductMcpSecrets{Env: map[string]*string{"VAULT_TOK": mcpStrPtr(actMcpSecret)}},
		OperationID: "bg1-save-mcp-2",
	}); err != nil {
		t.Fatal(err)
	}

	createBot := func(name, preset string) pi.ProductBot {
		bot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
			OperationID: "bot-" + name,
			Profile: pi.ProductBotProfile{
				Name: name, Role: "Tester", Purpose: "BG-1", Workspace: workspace, ToolPreset: preset,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return bot
	}
	ownerBot := createBot("OwnerBot", pi.ToolPresetWorkspace)
	otherBot := createBot("OtherBot", pi.ToolPresetWorkspace)
	readonlyBot := createBot("ReadonlyBot", pi.ToolPresetReadonly)
	noneBot := createBot("NoneBot", pi.ToolPresetNone)
	revocableBot := createBot("RevocableBot", pi.ToolPresetWorkspace)

	for _, restricted := range []pi.ProductBot{readonlyBot, noneBot} {
		_, tools := callModelTool(proc, restricted.CurrentConversationID, "", nil)
		for _, tool := range tools {
			if strings.HasPrefix(tool, "session_") {
				t.Fatalf("restricted bot %s exposed session tool %s", restricted.BotID, tool)
			}
		}
	}

	_, revTools := callModelTool(proc, revocableBot.CurrentConversationID, "", nil)
	if !slices.Contains(revTools, "session_open") {
		t.Fatalf("workspace bot missing session_open in %v", revTools)
	}
	if _, err := proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			BotID: revocableBot.BotID, ExpectedRevision: revocableBot.ProfileRevision, OperationID: "revoke-bash-op",
		},
		Profile: pi.ProductBotProfile{
			Name: "RevocableBot", Role: "Tester", Purpose: "BG-1", Workspace: workspace, ToolPreset: pi.ToolPresetReadonly,
		},
		StartNewConversation: false,
	}); err != nil {
		t.Fatal(err)
	}
	revokedRes, _ := callModelTool(proc, revocableBot.CurrentConversationID, "session_open", map[string]any{
		"command": nodePath, "mode": "pipe",
	})
	var errPayload struct {
		Code string `json:"code"`
	}
	mustDecode(revokedRes, &errPayload)
	if errPayload.Code != "scope_denied" {
		t.Fatalf("revoked bash bot session_open = %s, want scope_denied", revokedRes)
	}

	missingRes, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_open", map[string]any{
		"command": "/nonexistent/bg1-missing-binary", "mode": "pipe",
	})
	mustDecode(missingRes, &errPayload)
	if errPayload.Code != "exec_failed" {
		t.Fatalf("missing binary session_open = %s, want exec_failed", missingRes)
	}
	failedList, err := proc.client.ListSessions(ctx, pi.ProductSessionListParams{
		BotID: ownerBot.BotID, States: []pi.ProductSessionState{pi.ProductSessionFailed},
	})
	if err != nil || len(failedList.Items) != 1 || failedList.Items[0].TransportStatus != pi.ProductTransportExecFailed {
		t.Fatalf("ListSessions(failed) = %+v, %v", failedList, err)
	}

	pipeScript := filepath.Join(root, "pipe-fixture.mjs")
	if err := os.WriteFile(pipeScript, []byte(bg1PipeFixtureScript(
		activeSecret[:14], activeSecret[14:],
		rotProvSecret[:13], rotProvSecret[13:],
		rotMcpSecret[:12], rotMcpSecret[12:],
		actMcpSecret[:11], actMcpSecret[11:],
		inactiveSecret[:15], inactiveSecret[15:],
		nextProvSecret,
	)), 0o600); err != nil {
		t.Fatal(err)
	}

	openPipeRes, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_open", map[string]any{
		"command": nodePath, "args": []string{pipeScript}, "mode": "pipe", "label": "pipe " + activeSecret + " " + actMcpSecret,
	})
	var pipeSum pi.ProductSessionSummary
	mustDecode(openPipeRes, &pipeSum)
	if pipeSum.State != pi.ProductSessionRunning || pipeSum.Mode != pi.ProductSessionPipe ||
		strings.Contains(pipeSum.Label, activeSecret) || strings.Contains(pipeSum.Label, actMcpSecret) {
		t.Fatalf("pipe session open = %+v", pipeSum)
	}

	readyOut := bg1JoinedOutput(bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, pipeSum.SessionID, pipeSum.Generation, nil, "ENV_PATH:"))
	_ = bg1ExtractIntAfter(t, readyOut, "PID:")
	if !strings.Contains(readyOut, "ENV_PATH:/nonexistent\n") {
		t.Fatalf("child PATH not isolated: %q", readyOut)
	}
	for line := range strings.SplitSeq(readyOut, "\n") {
		if keys, ok := strings.CutPrefix(line, "ENV_KEYS:"); ok {
			for k := range strings.SplitSeq(keys, ",") {
				if strings.HasPrefix(k, "NAGI_") || strings.HasPrefix(k, "API_") {
					t.Fatalf("child env leaked variable %q in %q", k, keys)
				}
			}
		}
	}

	armWatch := func(bot pi.ProductBot, sessID string, gen uint64, extra map[string]any) pi.ProductSessionWatchSummary {
		args := map[string]any{"session_id": sessID, "generation": gen, "action": "arm"}
		for k, v := range extra {
			args[k] = v
		}
		raw, _ := callModelTool(proc, bot.CurrentConversationID, "session_watch", args)
		var w pi.ProductSessionWatchSummary
		mustDecode(raw, &w)
		return w
	}

	exitWatch := armWatch(ownerBot, pipeSum.SessionID, pipeSum.Generation, map[string]any{"on_exit": true})
	matchWatch := armWatch(ownerBot, pipeSum.SessionID, pipeSum.Generation, map[string]any{"match_literal": "MATCHED_TOKEN"})
	multiWinnerWatch := armWatch(ownerBot, pipeSum.SessionID, pipeSum.Generation, map[string]any{
		"on_exit": true, "match_literal": "SINGLE_WINNER", "idle_ms": 2000, "timeout_ms": 4000,
	})
	idleWatch := armWatch(ownerBot, pipeSum.SessionID, pipeSum.Generation, map[string]any{"idle_ms": 150})
	timeoutWatch := armWatch(ownerBot, pipeSum.SessionID, pipeSum.Generation, map[string]any{"timeout_ms": 120})
	cancelTarget := armWatch(ownerBot, pipeSum.SessionID, pipeSum.Generation, map[string]any{"timeout_ms": 30000})
	cancelRaw, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_watch", map[string]any{
		"session_id": pipeSum.SessionID, "generation": pipeSum.Generation, "action": "cancel", "watch_id": cancelTarget.WatchID,
	})
	var cancelledWatch pi.ProductSessionWatchSummary
	mustDecode(cancelRaw, &cancelledWatch)
	if cancelledWatch.State != "cancelled" {
		t.Fatalf("cancelled watch = %+v", cancelledWatch)
	}

	zeroOffset := uint64(0)
	writeRes, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_write", map[string]any{
		"session_id": pipeSum.SessionID, "generation": pipeSum.Generation, "text": "split_all\n",
	})
	var writeAck struct {
		AcceptedBytes int `json:"acceptedBytes"`
	}
	mustDecode(writeRes, &writeAck)
	if writeAck.AcceptedBytes != len("split_all\n") {
		t.Fatalf("write ack = %s", writeRes)
	}
	_ = bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, pipeSum.SessionID, pipeSum.Generation, &zeroOffset, "SINGLE_WINNER\n")
	_, _ = callModelTool(proc, ownerBot.CurrentConversationID, "session_write", map[string]any{
		"session_id": pipeSum.SessionID, "generation": pipeSum.Generation, "text": "rotated_secret\n",
	})

	splitRes := bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, pipeSum.SessionID, pipeSum.Generation, &zeroOffset, "ROT_OUT:")
	splitText := bg1JoinedOutput(splitRes)
	if !strings.Contains(splitText, "😀[credential]") || !strings.Contains(splitText, "MATCHED_TOKEN SINGLE_WINNER\n") ||
		!strings.Contains(splitText, "ROT_OUT:[credential]\n") {
		t.Fatalf("split normalized/redacted output mismatch: %q", splitText)
	}
	if strings.Contains(splitText, "\x1b") || strings.Contains(splitText, "clipboard") {
		t.Fatalf("ANSI/OSC sequence leaked into projected output: %q", splitText)
	}
	for _, secret := range []string{activeSecret, inactiveSecret, rotProvSecret, nextProvSecret, rotMcpSecret, actMcpSecret} {
		if strings.Contains(splitText, secret) {
			t.Fatalf("secret %q leaked into projected output: %q", secret, splitText)
		}
	}
	var sawStdout, sawStderr bool
	for _, chunk := range splitRes.Chunks {
		if chunk.Stream == "stdout" {
			sawStdout = true
		} else if chunk.Stream == "stderr" {
			sawStderr = true
		}
		if len(chunk.Text) != int(chunk.NextOffset-chunk.Offset) || !utf8.ValidString(chunk.Text) {
			t.Fatalf("invalid UTF-8 chunk bounds: %+v", chunk)
		}
	}
	if !sawStdout || !sawStderr {
		t.Fatalf("expected both stdout and stderr chunks in %+v", splitRes.Chunks)
	}

	emojiPos := strings.Index(splitText, "😀")
	if emojiPos < 0 {
		t.Fatalf("missing emoji in %q", splitText)
	}
	midCodepoint := uint64(emojiPos + 1)
	clampedRes, err := proc.client.ReadSession(ctx, pi.ProductSessionReadParams{
		BotID: ownerBot.BotID, SessionID: pipeSum.SessionID, Generation: pipeSum.Generation, Offset: &midCodepoint, Limit: 2048,
	})
	if err != nil || clampedRes.FromOffset != uint64(emojiPos+4) || clampedRes.Gap || !utf8.ValidString(bg1JoinedOutput(clampedRes)) {
		t.Fatalf("UTF-8 boundary clamp mismatch: %+v, err=%v", clampedRes, err)
	}

	bg1WaitUntil(t, ctx, "match, multiWinner, idle, timeout watches fired", func() bool {
		raw, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_watch", map[string]any{
			"session_id": pipeSum.SessionID, "generation": pipeSum.Generation, "action": "list",
		})
		var wl struct {
			Watches []pi.ProductSessionWatchSummary `json:"watches"`
		}
		mustDecode(raw, &wl)
		byID := map[string]pi.ProductSessionWatchSummary{}
		for _, w := range wl.Watches {
			byID[w.WatchID] = w
		}
		return byID[matchWatch.WatchID].FiredKind == "match" &&
			byID[multiWinnerWatch.WatchID].FiredKind == "match" &&
			byID[idleWatch.WatchID].FiredKind == "idle" &&
			byID[timeoutWatch.WatchID].FiredKind == "timeout"
	})

	_, _ = callModelTool(proc, ownerBot.CurrentConversationID, "session_write", map[string]any{
		"session_id": pipeSum.SessionID, "generation": pipeSum.Generation, "text": "overflow\n",
	})
	_ = bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, pipeSum.SessionID, pipeSum.Generation, nil, "TAIL_MARK")
	evictedRead, err := proc.client.ReadSession(ctx, pi.ProductSessionReadParams{
		BotID: ownerBot.BotID, SessionID: pipeSum.SessionID, Generation: pipeSum.Generation, Offset: &zeroOffset, Limit: 4096,
	})
	if err != nil || !evictedRead.Gap || evictedRead.FirstAvailableOffset == 0 ||
		evictedRead.GapBytes != evictedRead.FirstAvailableOffset || evictedRead.FromOffset != evictedRead.FirstAvailableOffset ||
		!utf8.ValidString(bg1JoinedOutput(evictedRead)) || strings.ContainsRune(bg1JoinedOutput(evictedRead), '\ufffd') {
		t.Fatalf("ring eviction read mismatch: %+v, err=%v", evictedRead, err)
	}

	_, _ = callModelTool(proc, ownerBot.CurrentConversationID, "session_eof", map[string]any{
		"session_id": pipeSum.SessionID, "generation": pipeSum.Generation,
	})
	settledPipe := bg1WaitSession(t, ctx, proc.client, ownerBot.BotID, pipeSum.SessionID, "pipe exited", func(s pi.ProductSessionSummary) bool {
		return s.State == pi.ProductSessionExited && s.EOF
	})
	if settledPipe.ExitCode == nil || *settledPipe.ExitCode != 21 || settledPipe.TransportStatus != pi.ProductTransportEOF ||
		settledPipe.EvictedBytes != settledPipe.FirstAvailableOffset {
		t.Fatalf("settled pipe mismatch: %+v", settledPipe)
	}
	pipeTailAfterEOF := bg1JoinedOutput(bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, pipeSum.SessionID, pipeSum.Generation, nil, "STDERR_AFTER_EOF"))
	if !strings.Contains(pipeTailAfterEOF, "STDERR_AFTER_EOF") {
		t.Fatalf("missing stderr after eof: %q", pipeTailAfterEOF)
	}
	var pipeExitWatchFound bool
	for _, w := range settledPipe.Watches {
		if w.WatchID == exitWatch.WatchID {
			pipeExitWatchFound = w.FiredKind == "exit"
		}
		if w.WatchID == multiWinnerWatch.WatchID && w.FiredKind != "match" {
			t.Fatalf("multi-condition watch overwrote match winner: %+v", w)
		}
	}
	if !pipeExitWatchFound {
		t.Fatalf("exit watch did not fire on pipe exit: %+v", settledPipe.Watches)
	}

	immMatch := armWatch(ownerBot, pipeSum.SessionID, pipeSum.Generation, map[string]any{
		"start_offset": settledPipe.FirstAvailableOffset, "match_literal": "TAIL_MARK",
	})
	immExit := armWatch(ownerBot, pipeSum.SessionID, pipeSum.Generation, map[string]any{"on_exit": true})
	if immMatch.FiredKind != "match" || immExit.FiredKind != "exit" {
		t.Fatalf("immediate watch fire mismatch: match=%+v exit=%+v", immMatch, immExit)
	}

	openPtyRes, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_open", map[string]any{
		"command": ptyFixture, "mode": "pty", "cols": 80, "rows": 24,
	})
	var ptySum pi.ProductSessionSummary
	mustDecode(openPtyRes, &ptySum)
	ptyInitOut := bg1JoinedOutput(bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, ptySum.SessionID, ptySum.Generation, nil, "PTY_SENTINEL:"))
	if !strings.Contains(ptyInitOut, "PTY_PATH:/nonexistent\n") || !strings.Contains(ptyInitOut, "PTY_SENTINEL:unset\n") {
		t.Fatalf("pty child env check failed: %q", ptyInitOut)
	}
	resizeRes, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_resize", map[string]any{
		"session_id": ptySum.SessionID, "generation": ptySum.Generation, "cols": 101, "rows": 41,
	})
	var resizedSum pi.ProductSessionSummary
	mustDecode(resizeRes, &resizedSum)
	if resizedSum.Cols == nil || *resizedSum.Cols != 101 || resizedSum.Rows == nil || *resizedSum.Rows != 41 {
		t.Fatalf("session_resize result = %+v", resizedSum)
	}
	_ = bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, ptySum.SessionID, ptySum.Generation, nil, "RESIZED:101x41")

	_, _ = callModelTool(proc, ownerBot.CurrentConversationID, "session_write", map[string]any{
		"session_id": ptySum.SessionID, "generation": ptySum.Generation, "data_base64": "Aw==",
	})
	rawReadyRead := bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, ptySum.SessionID, ptySum.Generation, nil, "RAW_READY")
	if !strings.Contains(bg1JoinedOutput(rawReadyRead), "CANONICAL_SIGINT") || rawReadyRead.AuthPause != nil {
		t.Fatalf("raw mode unexpectedly paused auth or missed canonical SIGINT: %+v", rawReadyRead)
	}
	if sum, err := proc.client.GetSession(ctx, ownerBot.BotID, ptySum.SessionID); err != nil || sum.AuthPause != nil {
		t.Fatalf("raw mode GetSession.AuthPause = %+v, err=%v", sum.AuthPause, err)
	}
	_, _ = callModelTool(proc, ownerBot.CurrentConversationID, "session_write", map[string]any{
		"session_id": ptySum.SessionID, "generation": ptySum.Generation, "data_base64": "Aw==",
	})
	_ = bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, ptySum.SessionID, ptySum.Generation, nil, "RAW_BYTE_03")

	pausedSummary := bg1WaitSession(t, ctx, proc.client, ownerBot.BotID, ptySum.SessionID, "pty authPause true", func(s pi.ProductSessionSummary) bool {
		return s.AuthPause != nil && s.AuthPause.Paused
	})
	if pausedSummary.AuthPause.Message == "" {
		t.Fatalf("authPause missing message: %+v", pausedSummary.AuthPause)
	}
	pausedRead, err := proc.client.ReadSession(ctx, pi.ProductSessionReadParams{
		BotID: ownerBot.BotID, SessionID: ptySum.SessionID, Generation: ptySum.Generation,
	})
	if err != nil || pausedRead.AuthPause == nil || !pausedRead.AuthPause.Paused {
		t.Fatalf("ReadSession authPause mismatch: %+v, err=%v", pausedRead, err)
	}
	pausedWriteRes, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_write", map[string]any{
		"session_id": ptySum.SessionID, "generation": ptySum.Generation, "text": "blocked-secret-input\n",
	})
	mustDecode(pausedWriteRes, &errPayload)
	if errPayload.Code != "auth_paused" {
		t.Fatalf("session_write while authPaused = %s, want auth_paused", pausedWriteRes)
	}

	_, _ = callModelTool(proc, ownerBot.CurrentConversationID, "session_signal", map[string]any{
		"session_id": ptySum.SessionID, "generation": ptySum.Generation, "signal": "SIGINT",
	})
	_ = bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, ptySum.SessionID, ptySum.Generation, nil, "ECHO_RESTORED")
	_ = bg1WaitSession(t, ctx, proc.client, ownerBot.BotID, ptySum.SessionID, "pty authPause cleared", func(s pi.ProductSessionSummary) bool {
		return s.AuthPause == nil
	})
	_, _ = callModelTool(proc, ownerBot.CurrentConversationID, "session_write", map[string]any{
		"session_id": ptySum.SessionID, "generation": ptySum.Generation, "text": "finish\n",
	})
	ptyExit := bg1WaitSession(t, ctx, proc.client, ownerBot.BotID, ptySum.SessionID, "pty exit 37", func(s pi.ProductSessionSummary) bool {
		return s.State == pi.ProductSessionExited && s.EOF
	})
	if ptyExit.ExitCode == nil || *ptyExit.ExitCode != 37 || ptyExit.AuthPause != nil {
		t.Fatalf("pty exit mismatch: %+v", ptyExit)
	}
	ptyFinalOut := bg1JoinedOutput(bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, ptySum.SessionID, ptySum.Generation, &zeroOffset, "PTY_DONE"))
	if strings.Contains(ptyFinalOut, "blocked-secret-input") || len(ptyFinalOut) == 0 {
		t.Fatalf("pty final output contained blocked write: %q", ptyFinalOut)
	}

	act1ID, act1OpenRaw := runActivityTool(proc, ownerBot, "session_open", map[string]any{
		"command": nodePath, "args": []string{pipeScript, "linger"}, "mode": "pipe", "label": "activity-owned",
	})
	var actSess pi.ProductSessionSummary
	mustDecode(act1OpenRaw, &actSess)
	if actSess.OriginActivityID != act1ID || actSess.OwnerBotID != ownerBot.BotID {
		t.Fatalf("activity session ownership = %+v, want act=%s", actSess, act1ID)
	}
	actReady := bg1JoinedOutput(bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, actSess.SessionID, actSess.Generation, nil, "PID:"))
	actChildPID := bg1ExtractIntAfter(t, actReady, "PID:")

	act2ID, act2WriteRaw := runActivityTool(proc, ownerBot, "session_write", map[string]any{
		"session_id": actSess.SessionID, "generation": actSess.Generation, "text": "sibling-write\n",
	})
	if act2ID == act1ID {
		t.Fatal("expected distinct sibling activity ID")
	}
	mustDecode(act2WriteRaw, &errPayload)
	if errPayload.Code != "scope_denied" {
		t.Fatalf("sibling activity session_write = %s, want scope_denied", act2WriteRaw)
	}
	_, act2ReadRaw := runActivityTool(proc, ownerBot, "session_read", map[string]any{
		"session_id": actSess.SessionID, "generation": actSess.Generation,
	})
	mustDecode(act2ReadRaw, &errPayload)
	if errPayload.Code != "scope_denied" {
		t.Fatalf("sibling activity session_read = %s, want scope_denied", act2ReadRaw)
	}

	otherWriteRaw, _ := callModelTool(proc, otherBot.CurrentConversationID, "session_write", map[string]any{
		"session_id": actSess.SessionID, "generation": actSess.Generation, "text": "cross-bot\n",
	})
	mustDecode(otherWriteRaw, &errPayload)
	if errPayload.Code != "scope_denied" {
		t.Fatalf("cross-bot session_write = %s, want scope_denied", otherWriteRaw)
	}
	otherReadRaw, _ := callModelTool(proc, otherBot.CurrentConversationID, "session_read", map[string]any{
		"session_id": actSess.SessionID, "generation": actSess.Generation,
	})
	mustDecode(otherReadRaw, &errPayload)
	if errPayload.Code != "scope_denied" {
		t.Fatalf("cross-bot session_read = %s, want scope_denied", otherReadRaw)
	}
	if _, err := proc.client.GetSession(ctx, otherBot.BotID, actSess.SessionID); err == nil {
		t.Fatal("cross-bot GetSession unexpectedly succeeded")
	} else {
		cp1AssertRemoteCode(t, err, "not_found")
	}

	fgReadRaw, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_read", map[string]any{
		"session_id": actSess.SessionID, "generation": actSess.Generation,
	})
	var fgRead pi.ProductSessionReadResult
	mustDecode(fgReadRaw, &fgRead)
	if fgRead.SessionID != actSess.SessionID {
		t.Fatalf("owning-bot foreground session_read = %+v", fgRead)
	}
	fgWatchListRaw, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_watch", map[string]any{
		"session_id": actSess.SessionID, "generation": actSess.Generation, "action": "list",
	})
	if !strings.Contains(fgWatchListRaw, `"watches"`) {
		t.Fatalf("owning-bot foreground session_watch(list) = %s", fgWatchListRaw)
	}
	fgWriteRaw, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_write", map[string]any{
		"session_id": actSess.SessionID, "generation": actSess.Generation, "text": "fg-mutate-activity\n",
	})
	mustDecode(fgWriteRaw, &errPayload)
	if errPayload.Code != "scope_denied" {
		t.Fatalf("owning-bot foreground write on activity session = %s, want scope_denied", fgWriteRaw)
	}

	page1, err := proc.client.ListSessions(ctx, pi.ProductSessionListParams{BotID: ownerBot.BotID, Limit: 1})
	if err != nil || len(page1.Items) != 1 || page1.Items[0].SessionID != actSess.SessionID || page1.NextCursor == nil {
		t.Fatalf("ListSessions page1 = %+v, err=%v", page1, err)
	}
	page2, err := proc.client.ListSessions(ctx, pi.ProductSessionListParams{
		BotID: ownerBot.BotID, Cursor: *page1.NextCursor, Limit: 1,
	})
	if err != nil || len(page2.Items) != 1 || page2.Items[0].SessionID == actSess.SessionID {
		t.Fatalf("ListSessions page2 = %+v, err=%v", page2, err)
	}
	if _, err := proc.client.ListSessions(ctx, pi.ProductSessionListParams{
		BotID: ownerBot.BotID, States: []pi.ProductSessionState{pi.ProductSessionRunning}, Cursor: *page1.NextCursor,
	}); err == nil {
		t.Fatal("ListSessions accepted cursor across mismatched state filter")
	} else {
		cp1AssertRemoteCode(t, err, "invalid_cursor")
	}
	if _, err := proc.client.ListSessions(ctx, pi.ProductSessionListParams{
		BotID: otherBot.BotID, Cursor: *page1.NextCursor,
	}); err == nil {
		t.Fatal("ListSessions accepted cursor across mismatched botId")
	} else {
		cp1AssertRemoteCode(t, err, "invalid_cursor")
	}
	if _, err := proc.client.ReadSession(ctx, pi.ProductSessionReadParams{
		BotID: ownerBot.BotID, SessionID: actSess.SessionID, Generation: 2,
	}); err == nil {
		t.Fatal("ReadSession accepted stale generation 2")
	} else {
		cp1AssertRemoteCode(t, err, "stale_generation")
	}

	preCloseSum, err := proc.client.GetSession(ctx, ownerBot.BotID, actSess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	closeOpID := "bg1-close-op-1"
	closeParams := pi.ProductSessionCloseParams{
		BotID: ownerBot.BotID, SessionID: actSess.SessionID, Generation: 1,
		ExpectedRevision: &preCloseSum.Revision, OperationID: closeOpID,
	}
	closeReceipt, err := proc.client.CloseSession(ctx, closeParams)
	if err != nil || closeReceipt.State != "accepted" || closeReceipt.Target.SessionID != actSess.SessionID {
		t.Fatalf("CloseSession receipt = %+v, err=%v", closeReceipt, err)
	}
	closeRes, err := pi.DecodeSessionCloseResult(closeReceipt)
	if err != nil || closeRes.State != pi.ProductSessionClosed || closeRes.CloseDisposition != "terminated" ||
		!closeRes.ChildReaped || closeRes.ExitSignal != "SIGKILL" || closeRes.TransportStatus != pi.ProductTransportClosedByUser {
		t.Fatalf("DecodeSessionCloseResult = %+v, err=%v", closeRes, err)
	}
	if !bg1ProcessGone(actChildPID) {
		t.Fatalf("GUI session.close did not reap child pid %d", actChildPID)
	}
	replayClose, err := proc.client.CloseSession(ctx, closeParams)
	if err != nil || !reflect.DeepEqual(replayClose, closeReceipt) {
		t.Fatalf("duplicate CloseSession mismatch: got=%+v want=%+v err=%v", replayClose, closeReceipt, err)
	}
	opGet, err := proc.client.Operation(ctx, closeOpID)
	if err != nil || !reflect.DeepEqual(opGet, closeReceipt) {
		t.Fatalf("operation.get mismatch: got=%+v want=%+v err=%v", opGet, closeReceipt, err)
	}
	if _, err := proc.client.CloseSession(ctx, pi.ProductSessionCloseParams{
		BotID: ownerBot.BotID, SessionID: actSess.SessionID, Generation: 2, OperationID: closeOpID,
	}); err == nil {
		t.Fatal("CloseSession accepted conflicting params with same operationId")
	} else {
		cp1AssertRemoteCode(t, err, "operation_conflict")
	}
	if _, err := proc.client.CloseSession(ctx, pi.ProductSessionCloseParams{
		BotID: ownerBot.BotID, SessionID: actSess.SessionID, Generation: 2, OperationID: "bg1-close-stale-gen",
	}); err == nil {
		t.Fatal("CloseSession accepted stale generation")
	} else {
		cp1AssertRemoteCode(t, err, "stale_generation")
	}
	alreadySettledReceipt, err := proc.client.CloseSession(ctx, pi.ProductSessionCloseParams{
		BotID: ownerBot.BotID, SessionID: actSess.SessionID, Generation: 1, OperationID: "bg1-close-already",
	})
	if err != nil {
		t.Fatalf("already settled close err: %v", err)
	}
	alreadySettledRes, err := pi.DecodeSessionCloseResult(alreadySettledReceipt)
	if err != nil || alreadySettledRes.CloseDisposition != "already_settled" || !alreadySettledRes.ChildReaped {
		t.Fatalf("already settled result = %+v, err=%v", alreadySettledRes, err)
	}

	var sawSessionChanged, sawSessionOutput bool
	proc.mu.Lock()
	for _, ev := range proc.events {
		if ev.Name == "session.changed" && ev.SessionChanged != nil && ev.SessionChanged.SessionID == actSess.SessionID {
			sawSessionChanged = true
		}
		if ev.Name == "session.outputUpdated" && ev.SessionOutputUpdated != nil && ev.SessionOutputUpdated.SessionID == pipeSum.SessionID {
			sawSessionOutput = true
		}
	}
	proc.mu.Unlock()
	if !sawSessionChanged || !sawSessionOutput {
		t.Fatalf("missing typed session events: changed=%v outputUpdated=%v", sawSessionChanged, sawSessionOutput)
	}

	openBackupRes, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_open", map[string]any{
		"command": nodePath, "args": []string{pipeScript, "linger"}, "mode": "pipe", "label": "idle-backup-session",
	})
	var backupSess pi.ProductSessionSummary
	mustDecode(openBackupRes, &backupSess)
	backupOut := bg1JoinedOutput(bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, backupSess.SessionID, backupSess.Generation, nil, "PID:"))
	backupChildPID := bg1ExtractIntAfter(t, backupOut, "PID:")
	backupWatch := armWatch(ownerBot, backupSess.SessionID, backupSess.Generation, map[string]any{"on_exit": true})

	ownerConv, err := proc.client.GetConversation(ctx, ownerBot.CurrentConversationID)
	if err != nil || ownerConv.Draft == nil {
		t.Fatalf("GetConversation = %+v, %v", ownerConv, err)
	}
	if _, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: ownerBot.CurrentConversationID},
		ProductText:               pi.ProductText{Text: "draft during active session"},
		ExpectedRevision:          ownerConv.Draft.Revision,
		OperationID:               "bg1-save-draft-survives",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			BotID: ownerBot.BotID, ExpectedRevision: ownerBot.ProfileRevision, OperationID: "bg1-update-bot-survives",
		},
		Profile: pi.ProductBotProfile{
			Name: "OwnerBot Updated", Role: "Tester", Purpose: "BG-1", Workspace: workspace, ToolPreset: pi.ToolPresetWorkspace,
		},
		StartNewConversation: false,
	}); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(profileDir, "nagi.db")
	survivalJSON := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const sessions = db.prepare('SELECT count(*) AS n FROM runtime_sessions').get().n;
const watches = db.prepare('SELECT count(*) AS n FROM session_watches').get().n;
const occ = db.prepare('SELECT count(*) AS n FROM runtime_watch_occurrences').get().n;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
const m4 = db.prepare('SELECT tool_name, tool_class FROM execution_mutation_admissions').all();
db.close();
console.log(JSON.stringify({ sessions, watches, occ, fk, m4 }));
`)
	var survival struct {
		Sessions int `json:"sessions"`
		Watches  int `json:"watches"`
		Occ      int `json:"occ"`
		FK       int `json:"fk"`
		M4       []struct {
			ToolName  string `json:"tool_name"`
			ToolClass string `json:"tool_class"`
		} `json:"m4"`
	}
	mustDecode(survivalJSON, &survival)
	if survival.Sessions < 4 || survival.Watches < 9 || survival.Occ < 7 || survival.FK != 0 {
		t.Fatalf("SQLite session/watch survival check = %+v", survival)
	}
	m4Seen := map[string]string{}
	for _, row := range survival.M4 {
		key := row.ToolName + ":" + row.ToolClass
		m4Seen[key] = row.ToolClass
	}
	for _, want := range []string{"session_read:readonly", "session_watch:readonly", "session_watch:mutating", "session_write:mutating", "session_open:mutating"} {
		if m4Seen[want] == "" {
			t.Fatalf("missing M4 tool admission class %s in %+v", want, survival.M4)
		}
	}

	mirrorRaw := productRead(t, opts.ProfilePath)
	if bytes.Contains(mirrorRaw, []byte("runtimeSessions")) {
		t.Fatal("contacts.json canonical mirror leaked runtimeSessions")
	}

	liveMirrorBefore := cp1FileSHA256(t, opts.ProfilePath)
	backupPath := filepath.Join(root, "bg1-backup.json")
	callsBeforeBackup := provider.calls.Load()
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatalf("BackupProfile with idle session open failed: %v", err)
	}
	if cp1FileSHA256(t, opts.ProfilePath) != liveMirrorBefore {
		t.Fatal("BackupProfile altered live contacts.json bytes")
	}
	backupRaw := productRead(t, backupPath)
	for _, forbidden := range []string{activeSecret, inactiveSecret, rotProvSecret, nextProvSecret, rotMcpSecret, actMcpSecret, "PID:"} {
		if bytes.Contains(backupRaw, []byte(forbidden)) {
			t.Fatalf("backup leaked %q", forbidden)
		}
	}
	var backupDoc struct {
		Backup struct {
			RuntimeSessions []pi.ProductSessionSummary `json:"runtimeSessions"`
		} `json:"backup"`
	}
	mustDecode(string(backupRaw), &backupDoc)
	var foundBackupSess bool
	for _, rs := range backupDoc.Backup.RuntimeSessions {
		if rs.SessionID == backupSess.SessionID {
			foundBackupSess = true
			if rs.State != pi.ProductSessionDisconnected || rs.AuthPause != nil {
				t.Fatalf("backup session projection not dormant: %+v", rs)
			}
			for _, w := range rs.Watches {
				if w.State == "armed" {
					t.Fatalf("backup exported armed watch: %+v", w)
				}
			}
		}
	}
	if !foundBackupSess {
		t.Fatal("backup missing open idle session history")
	}

	restoreDir := filepath.Join(root, "restored-profile")
	if err := os.MkdirAll(restoreDir, 0o700); err != nil {
		t.Fatal(err)
	}
	restoreProfile := filepath.Join(restoreDir, "contacts.json")
	if err := os.WriteFile(restoreProfile, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	recCopy, err := store.InspectStoreRecoveryCopy(backupPath)
	if err != nil {
		t.Fatalf("InspectStoreRecoveryCopy: %v", err)
	}
	recTarget, err := store.InspectStoreRecoveryTarget(restoreProfile)
	if err != nil {
		t.Fatalf("InspectStoreRecoveryTarget: %v", err)
	}
	if _, err := store.RestoreStoreRecoveryCopy(recCopy, recTarget); err != nil {
		t.Fatalf("RestoreStoreRecoveryCopy: %v", err)
	}
	restoreOpts := pi.ProductOptions{
		NodePath: nodePath, WorkerPath: workerPath,
		StateDir: filepath.Join(restoreDir, "pi"), ProfilePath: restoreProfile,
		CredentialsPath: filepath.Join(restoreDir, "connection.json"),
	}
	for round := 1; round <= 2; round++ {
		rp := startCP1Process(t, restoreOpts)
		_ = rp.open(t, ctx)
		list, err := rp.client.ListSessions(ctx, pi.ProductSessionListParams{BotID: ownerBot.BotID})
		if err != nil || len(list.Items) != len(backupDoc.Backup.RuntimeSessions) {
			t.Fatalf("restore round %d ListSessions = %+v, err=%v", round, list, err)
		}
		var sawBackupRestored bool
		for _, item := range list.Items {
			if !item.EOF || item.FirstAvailableOffset != item.NextOffset || item.AuthPause != nil {
				t.Fatalf("restored session %s not dormant: %+v", item.SessionID, item)
			}
			if item.SessionID == backupSess.SessionID {
				sawBackupRestored = true
				for _, w := range item.Watches {
					if w.WatchID == backupWatch.WatchID && w.State != "cancelled" {
						t.Fatalf("restored armed watch state = %+v, want cancelled", w)
					}
				}
			}
		}
		if !sawBackupRestored {
			t.Fatal("restored profile missing idle session projection")
		}
		rp.shutdown(t, ctx)
	}
	if provider.calls.Load() != callsBeforeBackup {
		t.Fatalf("backup/restore triggered provider calls: before=%d after=%d", callsBeforeBackup, provider.calls.Load())
	}
	restoreCheck := runNodeSQLite(t, nodePath, filepath.Join(restoreDir, "nagi.db"), `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.close();
console.log(JSON.stringify({ v, fk }));
`)
	if restoreCheck != `{"v":13,"fk":0}` {
		t.Fatalf("restored SQLite check = %s", restoreCheck)
	}

	proc.shutdown(t, ctx)
	bg1WaitUntil(t, ctx, "app.shutdown child reaped", func() bool {
		return bg1ProcessGone(backupChildPID)
	})
	afterShutdownJSON := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const raw = db.prepare('SELECT record_json FROM runtime_sessions WHERE session_id = ?').get(JSON.stringify(process.argv[2]))?.record_json;
db.close();
console.log(raw ?? '');
`, backupSess.SessionID)
	var afterShutdownRow struct {
		State           pi.ProductSessionState           `json:"state"`
		TransportStatus pi.ProductSessionTransportStatus `json:"transportStatus"`
		ChildReaped     bool                             `json:"childReaped"`
	}
	mustDecode(afterShutdownJSON, &afterShutdownRow)
	if afterShutdownRow.State != pi.ProductSessionClosed || afterShutdownRow.TransportStatus != pi.ProductTransportClosedOnShutdown || !afterShutdownRow.ChildReaped {
		t.Fatalf("after shutdown row = %+v", afterShutdownRow)
	}

	opts.FreshProfile = false
	proc = startCP1Process(t, opts)
	_ = proc.open(t, ctx)
	openCrashRes, _ := callModelTool(proc, ownerBot.CurrentConversationID, "session_open", map[string]any{
		"command": nodePath, "args": []string{pipeScript, "linger"}, "mode": "pipe", "label": "crash-reap-session",
	})
	var crashSess pi.ProductSessionSummary
	mustDecode(openCrashRes, &crashSess)
	crashOut := bg1JoinedOutput(bg1WaitReadContains(t, ctx, proc.client, ownerBot.BotID, crashSess.SessionID, crashSess.Generation, nil, "PID:"))
	crashChildPID := bg1ExtractIntAfter(t, crashOut, "PID:")
	helperPID := bg1ParentPID(t, crashChildPID)
	crashWatch := armWatch(ownerBot, crashSess.SessionID, crashSess.Generation, map[string]any{"on_exit": true})

	if err := syscall.Kill(helperPID, syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP helper %d: %v", helperPID, err)
	}
	helperStopped := true
	defer func() {
		if helperStopped {
			_ = syscall.Kill(helperPID, syscall.SIGCONT)
		}
	}()

	writeOpToken := fmt.Sprintf("BG1CRASHWRITE_%d", turnSeq.Add(1))
	writeCallID := "call_" + writeOpToken
	writeArgsRaw, _ := json.Marshal(map[string]any{
		"session_id": crashSess.SessionID, "generation": crashSess.Generation, "text": "in-flight-write\n",
	})
	reqMu.Lock()
	turnReplies[writeOpToken] = cp1Reply{Calls: []cp1ToolCall{{ID: writeCallID, Name: "session_write", Args: string(writeArgsRaw)}}}
	reqMu.Unlock()

	curConv, err := proc.client.GetConversation(ctx, ownerBot.CurrentConversationID)
	if err != nil || curConv.Draft == nil {
		t.Fatal(err)
	}
	savedDraft, err := proc.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: ownerBot.CurrentConversationID},
		ProductText:               pi.ProductText{Text: writeOpToken, Materials: []pi.ProductMaterial{}},
		ExpectedRevision:          curConv.Draft.Revision,
		OperationID:               "bg1-crash-write-draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeSendReceipt, err := proc.client.Send(ctx, pi.ProductSendParams{
		ProductConversationParams: pi.ProductConversationParams{ConversationID: ownerBot.CurrentConversationID},
		DraftRevision:             savedDraft.Revision,
		OperationID:               "bg1-crash-write-send",
	})
	if err != nil || writeSendReceipt.Execution == nil {
		t.Fatalf("in-flight write Send = %+v, err=%v", writeSendReceipt, err)
	}
	inFlightExecID := writeSendReceipt.Execution.ExecutionID

	var admissionID int64
	bg1WaitUntil(t, ctx, "in-flight session_write admitted", func() bool {
		out := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const row = db.prepare("SELECT admission_id FROM execution_mutation_admissions WHERE execution_id = ? AND tool_name = 'session_write' AND state = 'admitted'").get(JSON.stringify(process.argv[2]));
db.close();
console.log(row ? String(row.admission_id) : '');
`, inFlightExecID)
		if out == "" {
			return false
		}
		id, err := strconv.ParseInt(out, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		admissionID = id
		return true
	})

	callsBeforeKill := provider.calls.Load()
	outcome, killErr := proc.client.Kill(ctx)
	proc.exit(t, ctx, outcome, false, killErr)
	if err := syscall.Kill(helperPID, syscall.SIGCONT); err != nil {
		t.Fatalf("SIGCONT helper %d: %v", helperPID, err)
	}
	helperStopped = false
	bg1WaitUntil(t, ctx, "supervisor reaped child after worker SIGKILL", func() bool {
		return bg1ProcessGone(crashChildPID) && bg1ProcessGone(helperPID)
	})

	proc = startCP1Process(t, opts)
	_ = proc.open(t, ctx)
	if provider.calls.Load() != callsBeforeKill {
		t.Fatalf("crash reopen replayed provider calls: before=%d after=%d", callsBeforeKill, provider.calls.Load())
	}
	recoveredSess, err := proc.client.GetSession(ctx, ownerBot.BotID, crashSess.SessionID)
	if err != nil || recoveredSess.State != pi.ProductSessionDisconnected || recoveredSess.TransportStatus != pi.ProductTransportWorkerRestarted || !recoveredSess.EOF {
		t.Fatalf("recovered crashed session = %+v, err=%v", recoveredSess, err)
	}
	var sawTargetLost bool
	for _, w := range recoveredSess.Watches {
		if w.WatchID == crashWatch.WatchID {
			sawTargetLost = w.State == "target_lost" && w.FiredKind == "target_lost"
		}
	}
	if !sawTargetLost {
		t.Fatalf("recovered watch did not settle target_lost: %+v", recoveredSess.Watches)
	}
	reopenOpGet, err := proc.client.Operation(ctx, closeOpID)
	if err != nil || !reflect.DeepEqual(reopenOpGet, closeReceipt) {
		t.Fatalf("operation.get across crash mismatch: got=%+v want=%+v err=%v", reopenOpGet, closeReceipt, err)
	}

	crashDBCheck := runNodeSQLite(t, nodePath, dbPath, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const occ = db.prepare('SELECT count(*) AS n FROM runtime_watch_occurrences WHERE watch_id = ?').get(JSON.stringify(process.argv[2])).n;
const adm = db.prepare('SELECT state FROM execution_mutation_admissions WHERE admission_id = ?').get(Number(process.argv[3])).state;
const exec = db.prepare('SELECT state FROM executions WHERE execution_id = ?').get(JSON.stringify(process.argv[4])).state;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.close();
console.log(JSON.stringify({ occ, adm, exec, fk }));
`, crashWatch.WatchID, strconv.FormatInt(admissionID, 10), inFlightExecID)
	if crashDBCheck != `{"occ":1,"adm":"unknown","exec":"unknown","fk":0}` {
		t.Fatalf("crash recovery DB check = %s", crashDBCheck)
	}
	proc.shutdown(t, ctx)

	migDir := filepath.Join(root, "v11-migration")
	if err := os.MkdirAll(migDir, 0o700); err != nil {
		t.Fatal(err)
	}
	migOpts := pi.ProductOptions{
		NodePath: nodePath, WorkerPath: workerPath,
		StateDir: filepath.Join(migDir, "pi"), ProfilePath: filepath.Join(migDir, "contacts.json"),
		CredentialsPath: filepath.Join(migDir, "connection.json"), FreshProfile: true,
	}
	migProc := startCP1Process(t, migOpts)
	_ = migProc.open(t, ctx)
	migBot, err := migProc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "mig-bot-op",
		Profile:     pi.ProductBotProfile{Name: "MigBot", Role: "Tester", Purpose: "Migrate", Workspace: workspace, ToolPreset: pi.ToolPresetWorkspace},
	})
	if err != nil || migBot.BotID == "" {
		t.Fatalf("create migBot = %+v, err=%v", migBot, err)
	}
	migProc.shutdown(t, ctx)

	migMirrorCanonical := productRead(t, migOpts.ProfilePath)
	staleMirror := []byte("{\"version\":1,\"bots\":[]}\n")
	if err := os.WriteFile(migOpts.ProfilePath, staleMirror, 0o600); err != nil {
		t.Fatal(err)
	}
	staleSum := sha256.Sum256(staleMirror)
	staleDigest := hex.EncodeToString(staleSum[:])
	migDB := filepath.Join(migDir, "nagi.db")
	runNodeSQLite(t, nodePath, migDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec("DROP TABLE bot_event_inbox; DROP TABLE bot_event_batches; DROP TABLE watch_occurrences; DROP TABLE runtime_watches; DROP TABLE watch_grants; DELETE FROM meta WHERE key GLOB 'watch_operation:*'; DROP TABLE runtime_watch_occurrences; DROP TABLE session_watches; DROP TABLE runtime_sessions; DELETE FROM meta WHERE key = 'runtime_session_epoch';");
db.prepare("INSERT OR REPLACE INTO meta (key, value) VALUES ('pending_snapshot_previous', ?)").run(JSON.stringify(process.argv[2]));
db.exec('PRAGMA user_version = 11');
db.close();
`, staleDigest)

	migOpts.FreshProfile = false
	callsBeforeMig := provider.calls.Load()
	migProc = startCP1Process(t, migOpts)
	_ = migProc.open(t, ctx)
	if provider.calls.Load() != callsBeforeMig {
		t.Fatal("v11->v13 migration triggered provider calls")
	}
	if !bytes.Equal(productRead(t, migOpts.ProfilePath), migMirrorCanonical) {
		t.Fatal("v11->v13 pending mirror repair did not restore exact canonical contacts.json bytes")
	}
	migState := runNodeSQLite(t, nodePath, migDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
const pending = !!db.prepare("SELECT value FROM meta WHERE key = 'pending_snapshot_previous'").get();
db.close();
console.log(JSON.stringify({ v, fk, pending }));
`)
	if migState != `{"v":13,"fk":0,"pending":false}` {
		t.Fatalf("v11->v13 migration state = %s", migState)
	}
	migProc.shutdown(t, ctx)

	runNodeSQLite(t, nodePath, migDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
db.exec('PRAGMA user_version = 14');
db.close();
`)
	v13Client, startErr := pi.StartProduct(migOpts)
	if startErr == nil {
		if _, readyErr := v13Client.Ready(ctx); readyErr == nil {
			if _, openErr := v13Client.Open(ctx, []int{pi.ProductVersion}); openErr == nil {
				t.Fatal("worker admitted SQLite user_version=14")
			}
		}
		_, _ = v13Client.Wait(ctx)
	}
	v13After := runNodeSQLite(t, nodePath, migDB, `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const bots = db.prepare('SELECT count(*) AS n FROM bots').get().n;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.close();
console.log(JSON.stringify({ v, bots, fk }));
`)
	if v13After != `{"v":14,"bots":1,"fk":0}` {
		t.Fatalf("v14 refusal mutated DB: %s", v13After)
	}
	provider.assertNoProblems()
}
