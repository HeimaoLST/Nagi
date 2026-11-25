package pi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHelperMockWorker(t *testing.T) {
	mode := os.Getenv("NAGI_MOCK_WORKER_MODE")
	if mode == "" {
		return
	}
	ready := `{"v":1,"type":"ready","workerEpoch":"epoch-1","engine":{"name":"pi","version":"1.5.2"},"runtime":{"name":"node","version":"v22.23.5"},"builtinCatalog":{"supported":[{"name":"read","description":"Read file"}],"defaults":["read"]}}`
	writeLine := func(line string) {
		fmt.Fprintln(os.Stdout, line)
	}
	switch mode {
	case "duplicate-ready":
		writeLine(ready)
		writeLine(ready)
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "bad-run-seq":
		writeLine(ready)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var req struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(scanner.Bytes(), &req) != nil {
				continue
			}
			switch req.Method {
			case "run.start":
				writeLine(fmt.Sprintf(`{"v":1,"type":"response","id":%q,"ok":true,"result":{"contextId":"ctx-1","runId":"run-1","admitted":true}}`, req.ID))
				writeLine(`{"v":1,"type":"event","workerEpoch":"epoch-1","contextId":"ctx-1","runId":"run-1","seq":1,"event":"run.started","data":{"started":true}}`)
				writeLine(`{"v":1,"type":"event","workerEpoch":"epoch-1","contextId":"ctx-1","runId":"run-1","seq":1,"event":"text.delta","data":{"text":"dup"}}`)
			case "shutdown":
				writeLine(fmt.Sprintf(`{"v":1,"type":"response","id":%q,"ok":true,"result":{"shuttingDown":true}}`, req.ID))
				os.Exit(0)
			}
		}
		os.Exit(0)
	case "core-correlation":
		writeLine(ready)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var req struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(scanner.Bytes(), &req) != nil {
				continue
			}
			switch req.Method {
			case "configure":
				writeLine(fmt.Sprintf(`{"v":1,"type":"response","id":%q,"ok":true,"result":{"configured":true,"workerEpoch":"epoch-1","engineVersion":"1.5.2","pricingKnown":false,"credentialSource":"src-1"}}`, req.ID))
			case "run.cancel":
				writeLine(fmt.Sprintf(`{"v":1,"type":"response","id":%q,"ok":false,"error":{"code":"run_not_found","message":"missing run"}}`, req.ID))
			case "shutdown":
				writeLine(fmt.Sprintf(`{"v":1,"type":"response","id":%q,"ok":true,"result":{"shuttingDown":true}}`, req.ID))
				os.Exit(0)
			}
		}
		os.Exit(0)
	case "product-stream-gap":
		writeLine(ready)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var req struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(scanner.Bytes(), &req) != nil {
				continue
			}
			switch req.Method {
			case "app.open":
				writeLine(fmt.Sprintf(`{"v":1,"type":"response","id":%q,"ok":true,"result":{"version":1,"streamId":"stream-1","eventSeq":0,"selected":{"bot":null,"conversationId":"","activityId":""},"bots":[],"providerEffectiveRevision":null,"counts":{"conversations":0,"activities":0,"executions":0}}}`, req.ID))
				writeLine(`{"v":1,"type":"event","streamId":"stream-1","seq":1,"event":"mcp.changed","data":{"serverId":"srv-1","change":"saved"}}`)
				writeLine(`{"v":1,"type":"event","streamId":"stream-1","seq":3,"event":"mcp.changed","data":{"serverId":"srv-2","change":"saved"}}`)
				writeLine(`{"v":1,"type":"event","streamId":"stream-1","seq":4,"event":"mcp.changed","data":{"serverId":"srv-3","change":"saved"}}`)
			case "app.shutdown":
				writeLine(fmt.Sprintf(`{"v":1,"type":"response","id":%q,"ok":true,"result":{"drained":true}}`, req.ID))
				os.Exit(0)
			}
		}
		os.Exit(0)
	}
	os.Exit(2)
}

func startMockWorkerWrapper(t *testing.T, mode string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "mock-worker.sh")
	body := fmt.Sprintf("#!/bin/sh\nexec env NAGI_MOCK_WORKER_MODE=%s %q -test.run='^TestHelperMockWorker$'\n", mode, os.Args[0])
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return "/bin/sh", script, dir
}

func TestReadFrameBoundsAndChunking(t *testing.T) {
	exact := bytes.Repeat([]byte("a"), MaxFrameBytes-1)
	exact = append(exact, '\n')
	got, err := readFrame(bufio.NewReaderSize(bytes.NewReader(exact), 64))
	if err != nil {
		t.Fatalf("readFrame exact MaxFrameBytes failed: %v", err)
	}
	if len(got) != MaxFrameBytes-1 {
		t.Fatalf("len(got) = %d, want %d", len(got), MaxFrameBytes-1)
	}

	oversized := append(bytes.Repeat([]byte("b"), MaxFrameBytes), '\n')
	if _, err := readFrame(bufio.NewReaderSize(bytes.NewReader(oversized), 64)); err == nil || !strings.Contains(err.Error(), "exceeds 8 MiB") {
		t.Fatalf("oversized err = %v, want 8 MiB error", err)
	}

	multiChunk := append(bytes.Repeat([]byte("x"), 9000), '\n')
	chunked, err := readFrame(bufio.NewReaderSize(bytes.NewReader(multiChunk), 128))
	if err != nil || len(chunked) != 9000 {
		t.Fatalf("chunked readFrame = (%d, %v), want (9000, nil)", len(chunked), err)
	}

	if _, err := readFrame(bufio.NewReader(strings.NewReader(""))); !errors.Is(err, io.EOF) {
		t.Fatalf("empty EOF err = %v, want io.EOF", err)
	}

	if _, err := readFrame(bufio.NewReader(strings.NewReader("partial-without-lf"))); err == nil || !strings.Contains(err.Error(), "incomplete frame") {
		t.Fatalf("incomplete EOF err = %v, want incomplete frame", err)
	}
}

func TestValidBuiltinCatalog(t *testing.T) {
	cases := []struct {
		name    string
		catalog *BuiltinCatalog
		want    bool
	}{
		{"nil", nil, false},
		{"empty-supported", &BuiltinCatalog{Supported: []BuiltinTool{}, Defaults: []string{}}, false},
		{"nil-defaults", &BuiltinCatalog{Supported: []BuiltinTool{{Name: "read", Description: "Read"}}, Defaults: nil}, false},
		{"leading-space-name", &BuiltinCatalog{Supported: []BuiltinTool{{Name: " read", Description: "Read"}}, Defaults: []string{}}, false},
		{"blank-desc", &BuiltinCatalog{Supported: []BuiltinTool{{Name: "read", Description: "   "}}, Defaults: []string{}}, false},
		{"duplicate-supported", &BuiltinCatalog{Supported: []BuiltinTool{{Name: "read", Description: "Read"}, {Name: "read", Description: "Read 2"}}, Defaults: []string{}}, false},
		{"unknown-default", &BuiltinCatalog{Supported: []BuiltinTool{{Name: "read", Description: "Read"}}, Defaults: []string{"edit"}}, false},
		{"duplicate-default", &BuiltinCatalog{Supported: []BuiltinTool{{Name: "read", Description: "Read"}}, Defaults: []string{"read", "read"}}, false},
		{"valid-empty-defaults", &BuiltinCatalog{Supported: []BuiltinTool{{Name: "read", Description: "Read"}}, Defaults: []string{}}, true},
		{"valid-populated", &BuiltinCatalog{Supported: []BuiltinTool{{Name: "read", Description: "Read"}, {Name: "edit", Description: "Edit"}}, Defaults: []string{"read"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validBuiltinCatalog(tc.catalog); got != tc.want {
				t.Fatalf("validBuiltinCatalog = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveCodingToolsUnit(t *testing.T) {
	explicitEmpty := []string{}
	if got := ResolveCodingTools(explicitEmpty, ToolPresetWorkspace); got == nil || len(got) != 0 {
		t.Fatalf("ResolveCodingTools([]string{}, workspace) = %#v, want empty non-nil", got)
	}
	if got := ResolveCodingTools([]string{"read"}, ToolPresetNone); !slices.Equal(got, []string{"read"}) {
		t.Fatalf("ResolveCodingTools([read], none) = %v, want [read]", got)
	}
	if got := ResolveCodingTools(nil, ToolPresetNone); got == nil || len(got) != 0 {
		t.Fatalf("ResolveCodingTools(nil, none) = %#v, want empty non-nil", got)
	}
	if got := ResolveCodingTools(nil, ToolPresetReadonly); !slices.Equal(got, []string{"find", "grep", "ls", "read"}) {
		t.Fatalf("ResolveCodingTools(nil, readonly) = %v", got)
	}
	if got := ResolveCodingTools(nil, ToolPresetWorkspace); !slices.Equal(got, []string{"bash", "edit", "find", "grep", "ls", "read", "write"}) {
		t.Fatalf("ResolveCodingTools(nil, workspace) = %v", got)
	}
	if got := ResolveCodingTools(nil, ToolPresetCustom); got != nil {
		t.Fatalf("ResolveCodingTools(nil, custom) = %#v, want nil", got)
	}
	if got := ResolveCodingTools(nil, "unknown"); got != nil {
		t.Fatalf("ResolveCodingTools(nil, unknown) = %#v, want nil", got)
	}
}

func TestProductChangeValidationAndErrors(t *testing.T) {
	skillCases := []struct {
		name  string
		event ProductSkillChange
		want  bool
	}{
		{"saved-global", ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "global", SkillID: "s1"}, Change: "saved"}, true},
		{"saved-private-missing-bot", ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "private", SkillID: "s1"}, Change: "saved"}, false},
		{"saved-private-valid", ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "private", BotID: "b1", SkillID: "s1"}, Change: "saved"}, true},
		{"promoted-global", ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "global", BotID: "b1", SkillID: "s1"}, Change: "promoted"}, true},
		{"promoted-private", ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "private", BotID: "b1", SkillID: "s1"}, Change: "promoted"}, false},
		{"preferences-clean", ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "global"}, Change: "preferences"}, true},
		{"preferences-extra-id", ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "global", SkillID: "s1"}, Change: "preferences"}, false},
		{"tested-workspace", ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "workspace", BotID: "b1", SkillID: "s1"}, Change: "tested"}, true},
		{"unknown-change", ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "global", SkillID: "s1"}, Change: "archived"}, false},
	}
	for _, tc := range skillCases {
		t.Run("skill/"+tc.name, func(t *testing.T) {
			if got := tc.event.valid(); got != tc.want {
				t.Fatalf("ProductSkillChange.valid() = %v, want %v", got, tc.want)
			}
		})
	}

	if !(ProductMcpChange{ServerID: "srv-1", Change: "saved"}).valid() || (ProductMcpChange{ServerID: "", Change: "saved"}).valid() || (ProductMcpChange{ServerID: "srv-1", Change: "other"}).valid() {
		t.Fatal("ProductMcpChange validation mismatch")
	}

	r1, r2 := uint64(1), uint64(2)
	if !(ProductProviderChange{ProductConnectionRevisions: ProductConnectionRevisions{SavedRevision: &r2, EffectiveRevision: &r1}, Change: "applied"}).valid() {
		t.Fatal("expected valid ProductProviderChange with effective <= saved")
	}
	if (ProductProviderChange{ProductConnectionRevisions: ProductConnectionRevisions{SavedRevision: &r1, EffectiveRevision: &r2}, Change: "applied"}).valid() {
		t.Fatal("expected invalid ProductProviderChange with effective > saved")
	}
	if (ProductProviderChange{ProductConnectionRevisions: ProductConnectionRevisions{SavedRevision: nil}, Change: "saved"}).valid() {
		t.Fatal("expected invalid ProductProviderChange with nil SavedRevision")
	}

	remote := &RemoteError{Code: "rate_limited", Message: "slow down"}
	cmdErr := &CommandError{Method: "send", RequestID: "nagi-1", Delivery: Rejected, Err: remote}
	if !errors.Is(cmdErr, remote) || !strings.Contains(cmdErr.Error(), "rejected") {
		t.Fatalf("CommandError unwrap/format failed: %v", cmdErr)
	}
	prodErr := &ProductRemoteError{RemoteError: *remote}
	if prodErr.Unwrap().Error() != remote.Error() {
		t.Fatalf("ProductRemoteError.Unwrap mismatch: %v", prodErr.Unwrap())
	}

	t.Setenv("NAGI_PI_PAYLOAD", "relative/payload")
	if _, _, err := PayloadPaths(); err == nil {
		t.Fatal("expected error for relative NAGI_PI_PAYLOAD")
	}
	absRoot := t.TempDir()
	t.Setenv("NAGI_PI_PAYLOAD", absRoot)
	node, worker, err := PayloadPaths()
	if err != nil || node != filepath.Join(absRoot, "bin", "node") || worker != filepath.Join(absRoot, "dist", "worker.js") {
		t.Fatalf("PayloadPaths = (%q, %q, %v)", node, worker, err)
	}
}

func TestClientHandshakeCorrelationAndFailures(t *testing.T) {
	node, script, state := startMockWorkerWrapper(t, "core-correlation")
	client, err := Start(Options{NodePath: node, WorkerPath: script, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := client.Configure(ctx, ConfigureParams{
		WireAPI: OpenAICompletions, BaseURL: "https://example.com/v1", APIKey: "sk-test", Model: "gpt-4o",
		ContextTokens: 8192, OutputTokens: 1024, CredentialSource: "src-1",
	})
	if err != nil || !res.Configured || res.WorkerEpoch != "epoch-1" {
		t.Fatalf("Configure = (%+v, %v)", res, err)
	}
	_, err = client.CancelRun(ctx, CancelRunParams{ContextID: "ctx-1", RunID: "run-1"})
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) || cmdErr.Delivery != Rejected {
		t.Fatalf("CancelRun err = %v, want Rejected CommandError", err)
	}
	if _, err := client.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	<-client.CallbacksDone()

	for _, tc := range []struct {
		name string
		mode string
	}{
		{"duplicate-ready", "duplicate-ready"},
		{"bad-run-seq", "bad-run-seq"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			protoErrCh := make(chan error, 1)
			n, s, d := startMockWorkerWrapper(t, tc.mode)
			c, err := Start(Options{
				NodePath: n, WorkerPath: s, StateDir: d,
				Handlers: Handlers{OnProtocolError: func(err error) {
					select {
					case protoErrCh <- err:
					default:
					}
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer ccancel()
				_, _ = c.Kill(cctx)
				<-c.CallbacksDone()
			})
			if tc.mode == "bad-run-seq" {
				_, _ = c.StartRun(ctx, StartRunParams{ContextID: "ctx-1", RunID: "run-1", Text: "hi"})
			}
			select {
			case gotErr := <-protoErrCh:
				if gotErr == nil {
					t.Fatal("expected non-nil protocol error")
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out waiting for protocol error in mode %s", tc.mode)
			}
		})
	}
}

func TestProductClientStreamGap(t *testing.T) {
	node, script, state := startMockWorkerWrapper(t, "product-stream-gap")
	var mu sync.Mutex
	var events []ProductEvent
	gapCh := make(chan struct{}, 2)

	client, err := StartProduct(ProductOptions{
		NodePath: node, WorkerPath: script, StateDir: state,
		ProfilePath:     filepath.Join(state, "profile.sqlite"),
		CredentialsPath: filepath.Join(state, "connection.json"),
		Handlers: ProductHandlers{
			OnEvent: func(event ProductEvent) {
				mu.Lock()
				events = append(events, event)
				mu.Unlock()
			},
			OnStreamGap: func() {
				gapCh <- struct{}{}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Open(ctx, []int{ProductVersion}); err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	select {
	case <-gapCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OnStreamGap")
	}
	if _, err := client.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	<-client.CallbacksDone()

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].Seq != 1 || events[0].McpChanged == nil || events[0].McpChanged.ServerID != "srv-1" {
		t.Fatalf("events = %+v, want single seq=1 srv-1 event before gap", events)
	}
}
