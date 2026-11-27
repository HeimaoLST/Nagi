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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
)

var availableSkillsBlockRe = regexp.MustCompile(`(?s)<available_skills>\n(.*?)\n</available_skills>`)

func parseAvailableSkillIDs(system string) []string {
	matches := availableSkillsBlockRe.FindAllStringSubmatch(system, -1)
	if len(matches) == 0 {
		return nil
	}
	m := matches[len(matches)-1]
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(m[1]), "\n") {
		if line == "" {
			continue
		}
		var entry struct {
			SkillID string `json:"skill_id"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err == nil && entry.SkillID != "" {
			ids = append(ids, entry.SkillID)
		}
	}
	return ids
}

func filterCodingTools(tools []string) []string {
	known := []string{"bash", "edit", "find", "grep", "ls", "read", "write"}
	var out []string
	for _, name := range tools {
		if slices.Contains(known, name) {
			out = append(out, name)
		}
	}
	return out
}

func sha256HexBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeSkillFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func drainSkillChangedEvents(proc *productProcess) []pi.ProductSkillChange {
	var out []pi.ProductSkillChange
	for {
		select {
		case ev := <-proc.events:
			if ev.Name == "skill.changed" && ev.SkillChanged != nil {
				out = append(out, *ev.SkillChanged)
			}
		default:
			return out
		}
	}
}

func TestPiProductSkillsPresetsCatalogAndSecurity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if got := pi.ResolveCodingTools(nil, pi.ToolPresetNone); got == nil || len(got) != 0 {
		t.Fatalf("ResolveCodingTools(nil, none) = %#v, want empty non-nil slice", got)
	}
	wantReadonly := []string{"find", "grep", "ls", "read"}
	if got := pi.ResolveCodingTools(nil, pi.ToolPresetReadonly); !reflect.DeepEqual(got, wantReadonly) {
		t.Fatalf("ResolveCodingTools(nil, readonly) = %v, want %v", got, wantReadonly)
	}
	wantWorkspace := []string{"bash", "edit", "find", "grep", "ls", "read", "write"}
	if got := pi.ResolveCodingTools(nil, pi.ToolPresetWorkspace); !reflect.DeepEqual(got, wantWorkspace) {
		t.Fatalf("ResolveCodingTools(nil, workspace) = %v, want %v", got, wantWorkspace)
	}
	if got := pi.ResolveCodingTools([]string{"read"}, pi.ToolPresetWorkspace); !reflect.DeepEqual(got, []string{"read"}) {
		t.Fatalf("ResolveCodingTools(explicit, workspace) = %v, want [read]", got)
	}
	if got := pi.ResolveCodingTools([]string{}, pi.ToolPresetWorkspace); got == nil || len(got) != 0 {
		t.Fatalf("ResolveCodingTools(empty, workspace) = %#v, want empty non-nil slice", got)
	}
	if got := pi.ResolveCodingTools(nil, pi.ToolPresetCustom); got != nil {
		t.Fatalf("ResolveCodingTools(nil, custom) = %#v, want nil", got)
	}
	if got := pi.ResolveCodingTools(nil, ""); got != nil {
		t.Fatalf("ResolveCodingTools(nil, empty) = %#v, want nil", got)
	}

	const inactiveKey = "sk-m1-inactive-secret-9876543210"
	const globalBody = "GLOBAL_ON_DEMAND_BODY_MARKER"
	const unmountedBody = "UNMOUNTED_GLOBAL_SECRET_BODY"
	const workspaceBody = "WORKSPACE_ON_DEMAND_BODY_MARKER"
	const privateBody = "PRIVATE_ON_DEMAND_BODY\n日本語\n"
	const coldBody = "COLD_REOPEN_PRIVATE_BODY"
	const otherBody = "FOREIGN_BOT_PRIVATE_BODY"

	root := t.TempDir()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	wsSkill := "---\nname: Workspace Guide\ndescription: Read only workspace guide\n---\n" + workspaceBody + "\n"
	writeSkillFile(t, filepath.Join(workspace, ".nagi", "skills", "ws-guide", "SKILL.md"), wsSkill)
	outsidePath := filepath.Join(root, "outside", "SKILL.md")
	writeSkillFile(t, outsidePath, "---\nname: Escaped\ndescription: Outside workspace\n---\nESCAPED_BODY\n")
	escapePath := filepath.Join(workspace, ".nagi", "skills", "escape", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(escapePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, escapePath); err != nil {
		t.Fatal(err)
	}

	type capturedReq struct {
		bot    string
		system string
		tools  []string
	}
	var mu sync.Mutex
	var captured []capturedReq
	toolResults := map[string]string{}
	var providerErrs []string
	botSteps := map[string]int{}
	var openCallCount atomic.Int32
	var coldOpenPhase atomic.Bool

	recordErr := func(format string, args ...any) {
		mu.Lock()
		providerErrs = append(providerErrs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if coldOpenPhase.Load() {
			openCallCount.Add(1)
		}
		var body struct {
			Messages []struct {
				Role       string          `json:"role"`
				ToolCallID string          `json:"tool_call_id"`
				Content    json.RawMessage `json:"content"`
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
				sys += unmarshalToolText(m.Content)
			}
		}
		toolNames := make([]string, 0, len(body.Tools))
		for _, tl := range body.Tools {
			toolNames = append(toolNames, tl.Function.Name)
		}
		if !slices.IsSorted(toolNames) {
			recordErr("tools not sorted alphabetically: %v", toolNames)
		}
		var skillTools []string
		for _, name := range toolNames {
			if strings.HasPrefix(name, "skill_") {
				skillTools = append(skillTools, name)
			}
		}
		wantSkillTools := []string{"skill_delete", "skill_exec", "skill_list", "skill_read", "skill_save", "skill_test"}
		if !slices.Equal(skillTools, wantSkillTools) {
			recordErr("conversation skill tools = %v, want %v", skillTools, wantSkillTools)
		}

		last := body.Messages[len(body.Messages)-1]
		if last.Role == "tool" && last.ToolCallID != "" {
			mu.Lock()
			toolResults[last.ToolCallID] = unmarshalToolText(last.Content)
			mu.Unlock()
		}

		botName := ""
		for _, candidate := range []string{"Writer", "PandoraBox", "Specialist", "FullPreset", "CustomOverride", "LegacyNull"} {
			if strings.Contains(sys, "Bot Identity\nName: "+candidate) {
				botName = candidate
				break
			}
		}
		if botName == "" {
			recordErr("unrecognized bot in system prompt: %q", sys)
			http.Error(w, "unknown bot", http.StatusBadRequest)
			return
		}
		mu.Lock()
		captured = append(captured, capturedReq{bot: botName, system: sys, tools: toolNames})
		botSteps[botName]++
		step := botSteps[botName]
		mu.Unlock()

		switch botName {
		case "Writer":
			switch step {
			case 1:
				piChatResponse(w, "", "skill_read", "w-read-global", `{"skill_id":"reference","source":"global"}`)
			case 2:
				piChatResponse(w, "", "skill_read", "w-read-unmounted", `{"skill_id":"unmounted","source":"global"}`)
			case 3:
				piChatResponse(w, "", "skill_read", "w-read-foreign", `{"skill_id":"other-private","source":"private"}`)
			case 4:
				piChatResponse(w, "", "skill_read", "w-read-escape", `{"skill_id":"escape","source":"workspace"}`)
			case 5:
				piChatResponse(w, "", "skill_read", "w-read-traversal", `{"skill_id":"../reference","source":"global"}`)
			case 6:
				args, _ := json.Marshal(map[string]any{
					"skill_id": "secret-leak", "name": "Leak", "description": "Must reject key",
					"content": "Do not store " + inactiveKey,
				})
				piChatResponse(w, "", "skill_save", "w-save-secret", string(args))
			case 7:
				args, _ := json.Marshal(map[string]any{
					"skill_id": "notes", "name": "Writer Notes", "description": "Reusable writer notes",
					"content": privateBody,
				})
				piChatResponse(w, "", "skill_save", "w-save-notes", string(args))
			case 8:
				args, _ := json.Marshal(map[string]any{
					"skill_id": "temp-skill", "name": "Temp Skill", "description": "Short lived skill",
					"content": "TEMP_SKILL_BODY",
				})
				piChatResponse(w, "", "skill_save", "w-save-temp", string(args))
			case 9:
				piChatResponse(w, "", "skill_read", "w-read-private", `{"skill_id":"notes","source":"private"}`)
			case 10:
				piChatResponse(w, "", "skill_list", "w-list", `{}`)
			case 11:
				piChatResponse(w, "", "skill_delete", "w-del-global", `{"skill_id":"reference"}`)
			case 12:
				piChatResponse(w, "", "skill_delete", "w-del-foreign", `{"skill_id":"other-private"}`)
			case 13:
				piChatResponse(w, "", "skill_delete", "w-del-temp", `{"skill_id":"temp-skill"}`)
			case 14:
				piChatResponse(w, "", "skill_read", "w-read-deleted", `{"skill_id":"temp-skill","source":"private"}`)
			case 15:
				piChatResponse(w, "Turn 1 complete.", "", "", "")
			case 16:
				piChatResponse(w, "Turn 2 hot complete.", "", "", "")
			case 17:
				args, _ := json.Marshal(map[string]any{
					"skill_id": "cold-note", "name": "Cold Note", "description": "Added before cold reopen",
					"content": coldBody,
				})
				piChatResponse(w, "", "skill_save", "w-save-cold", string(args))
			case 18:
				piChatResponse(w, "Turn 3 mount reload complete.", "", "", "")
			case 19:
				piChatResponse(w, "Turn 4 hot after cold-note save.", "", "", "")
			default:
				piChatResponse(w, "Turn 5 cold reopen complete.", "", "", "")
			}
		case "PandoraBox":
			switch step {
			case 1:
				args, _ := json.Marshal(map[string]any{
					"name": "Specialist", "purpose": "Audit code changes", "job": "Review repository diffs",
					"voice": "Concise and direct", "anti_jobs": []string{"Never edit production files"},
					"tool_preset": "readonly", "skill_ids": []string{"reference"},
				})
				piChatResponse(w, "", "bot_create", "p-create-specialist", string(args))
			default:
				piChatResponse(w, "Created readonly Specialist.", "", "", "")
			}
		default:
			piChatResponse(w, "Preset check done.", "", "", "")
		}
	}))
	t.Cleanup(provider.Close)

	opts := pi.ProductOptions{
		NodePath:        os.Getenv("NAGI_TEST_NODE"),
		WorkerPath:      os.Getenv("NAGI_TEST_WORKER"),
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
	if _, err := proc.client.ApplyConnection(ctx, pi.ProductConnectionApplyParams{SavedRevision: *savedConn.SavedRevision, OperationID: "op-conn-apply"}); err != nil {
		t.Fatal(err)
	}

	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "global", SkillID: "reference"},
		Name:            "Reference Skill", Description: "Metadata for reference", Content: globalBody, OperationID: "op-save-ref",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "global", SkillID: "unmounted"},
		Name:            "Unmounted Skill", Description: "Not mounted on Writer", Content: unmountedBody, OperationID: "op-save-unmounted",
	}); err != nil {
		t.Fatal(err)
	}
	_, rpcSecretErr := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "global", SkillID: "rpc-secret"},
		Name:            "Secret Skill", Description: "Must be rejected", Content: "key=" + inactiveKey, OperationID: "op-save-rpc-secret",
	})
	var remoteErr *pi.RemoteError
	if !errors.As(rpcSecretErr, &remoteErr) || remoteErr.Code != "skill_secret" {
		t.Fatalf("RPC SaveSkill with inactive key = %v, want skill_secret", rpcSecretErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "skills", "rpc-secret", "SKILL.md")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected secret skill wrote file on disk: %v", statErr)
	}

	writer, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-writer",
		Profile: pi.ProductBotProfile{
			Name: "Writer", Workspace: workspace, ToolPreset: pi.ToolPresetNone,
			MountedSkills: []pi.ProductMountedSkill{
				{Source: "global", SkillID: "reference"},
				{Source: "workspace", SkillID: "escape"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-other",
		Profile:     pi.ProductBotProfile{Name: "Other", Workspace: workspace, CodingTools: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: other.BotID, SkillID: "other-private"},
		Name:            "Other Private", Description: "Owned by Other", Content: otherBody, OperationID: "op-save-other-priv",
	}); err != nil {
		t.Fatal(err)
	}
	fullBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-full",
		Profile:     pi.ProductBotProfile{Name: "FullPreset", Workspace: workspace, ToolPreset: pi.ToolPresetWorkspace},
	})
	if err != nil {
		t.Fatal(err)
	}
	customBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-custom",
		Profile:     pi.ProductBotProfile{Name: "CustomOverride", Workspace: workspace, ToolPreset: pi.ToolPresetWorkspace, CodingTools: []string{"read"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyBot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-bot-legacy",
		Profile:     pi.ProductBotProfile{Name: "LegacyNull", Workspace: workspace},
	})
	if err != nil {
		t.Fatal(err)
	}
	pandora, err := proc.client.EnsureBuiltinBot(ctx, pi.ProductBotEnsureBuiltinParams{
		Kind: pi.SystemKindBotArchitect, OperationID: "op-ensure-pandora",
	})
	if err != nil {
		t.Fatal(err)
	}

	sendTurn := func(p *productProcess, convID, opPrefix, text string) {
		t.Helper()
		cur, err := p.client.GetConversation(ctx, convID)
		if err != nil || cur.Draft == nil {
			t.Fatalf("GetConversation(%s) = %+v, %v", convID, cur, err)
		}
		draft, err := p.client.SaveDraft(ctx, pi.ProductSaveDraftParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
			ProductText:               pi.ProductText{Text: text},
			ExpectedRevision:          cur.Draft.Revision, OperationID: opPrefix + "-draft",
		})
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := p.client.Send(ctx, pi.ProductSendParams{
			ProductConversationParams: pi.ProductConversationParams{ConversationID: convID},
			DraftRevision:             draft.Revision, OperationID: opPrefix + "-send",
		})
		if err != nil || receipt.Execution == nil {
			t.Fatalf("Send(%s) = %+v, %v", opPrefix, receipt, err)
		}
		settled := waitSettledExecution(t, ctx, p, receipt.Execution.ExecutionID)
		if settled.State != "completed" {
			t.Fatalf("turn %s settled state = %q, want completed", opPrefix, settled.State)
		}
	}

	sendTurn(proc, writer.CurrentConversationID, "w-t1", "Exercise skill tools on zero-coding-tools bot.")
	sendTurn(proc, writer.CurrentConversationID, "w-t2-hot", "Hot follow-up right after skill_save.")

	updatedWriterProfile := writer.Profile
	updatedWriterProfile.Name = "Writer Renamed"
	updatedWriterProfile.MountedSkills = []pi.ProductMountedSkill{
		{Source: "global", SkillID: "reference"},
		{Source: "workspace", SkillID: "ws-guide"},
	}
	if _, err := proc.client.UpdateBot(ctx, pi.ProductBotUpdateParams{
		ProductBotRevisionParams: pi.ProductBotRevisionParams{
			BotID: writer.BotID, ExpectedRevision: writer.ProfileRevision, OperationID: "op-writer-mount-ws",
		},
		Profile: updatedWriterProfile,
	}); err != nil {
		t.Fatal(err)
	}
	sendTurn(proc, writer.CurrentConversationID, "w-t3-mount", "Turn after explicit mount update.")
	sendTurn(proc, writer.CurrentConversationID, "w-t4-hot2", "Hot follow-up after saving cold-note.")

	sendTurn(proc, pandora.CurrentConversationID, "p-t1", "Create a readonly Specialist bot with reference skill.")
	bots, err := proc.client.ListBots(ctx, pi.ProductPageParams{})
	if err != nil {
		t.Fatal(err)
	}
	var specialist *pi.ProductBot
	for i := range bots.Items {
		if bots.Items[i].Profile.Name == "Specialist" {
			specialist = &bots.Items[i]
			break
		}
	}
	if specialist == nil || specialist.Profile.ToolPreset != pi.ToolPresetReadonly || specialist.Profile.CodingTools != nil ||
		len(specialist.Profile.MountedSkills) != 1 || specialist.Profile.MountedSkills[0].SkillID != "reference" ||
		specialist.Profile.MountedSkills[0].Source != "global" {
		t.Fatalf("PandoraBox created specialist = %+v", specialist)
	}
	sendTurn(proc, specialist.CurrentConversationID, "sp-t1", "Verify readonly coding tools.")
	sendTurn(proc, fullBot.CurrentConversationID, "full-t1", "Verify workspace preset.")
	sendTurn(proc, customBot.CurrentConversationID, "custom-t1", "Verify custom precedence.")
	sendTurn(proc, legacyBot.CurrentConversationID, "legacy-t1", "Verify legacy null default.")

	proc.shutdown(t, ctx)

	coldOpenPhase.Store(true)
	opts.FreshProfile = false
	proc2 := startProductProcess(t, opts)
	proc2.open(t, ctx)
	if got := openCallCount.Load(); got != 0 {
		t.Fatalf("cold disk reopen triggered %d provider calls on open", got)
	}
	coldOpenPhase.Store(false)
	sendTurn(proc2, writer.CurrentConversationID, "w-t5-cold", "Cold reopen turn should refresh catalog with cold-note.")
	proc2.shutdown(t, ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(providerErrs) != 0 {
		t.Fatalf("provider invariant failures: %v", providerErrs)
	}

	var l0Prefix string
	byBot := map[string][]capturedReq{}
	for idx, req := range captured {
		parts := strings.Split(req.system, "\n\nBot Identity\n")
		if len(parts) < 2 {
			t.Fatalf("request[%d] missing L0/L1 boundary", idx)
		}
		if l0Prefix == "" {
			l0Prefix = parts[0]
			if !strings.HasPrefix(l0Prefix, "You are a long-lived Nagi Bot teammate.") || strings.Contains(l0Prefix, "<available_skills>") {
				t.Fatalf("L0 prefix mutated or leaked skills: %q", l0Prefix)
			}
		} else if parts[0] != l0Prefix {
			t.Fatalf("request[%d] L0 prefix diverged:\ngot:  %q\nwant: %q", idx, parts[0], l0Prefix)
		}
		byBot[req.bot] = append(byBot[req.bot], req)
	}

	writerReqs := byBot["Writer"]
	if len(writerReqs) != 20 {
		t.Fatalf("Writer request count = %d, want 20", len(writerReqs))
	}
	if got := filterCodingTools(writerReqs[0].tools); len(got) != 0 {
		t.Fatalf("Writer (toolPreset=none) advertised coding tools: %v", got)
	}
	for i := 1; i <= 15; i++ {
		if writerReqs[i].system != writerReqs[0].system {
			t.Fatalf("Writer request[%d] busted hot KV cache before boundary", i)
		}
	}
	if ids := parseAvailableSkillIDs(writerReqs[0].system); !reflect.DeepEqual(ids, []string{"reference"}) {
		t.Fatalf("Writer turn 1 L1 catalog = %v, want [reference]", ids)
	}
	for _, leaked := range []string{globalBody, unmountedBody, otherBody, workspaceBody, privateBody, coldBody, inactiveKey} {
		if strings.Contains(writerReqs[0].system, leaked) {
			t.Fatalf("Writer turn 1 L1 leaked body or secret %q", leaked)
		}
	}

	if !strings.Contains(toolResults["w-read-global"], globalBody) {
		t.Fatalf("skill_read on mounted global failed: %s", toolResults["w-read-global"])
	}
	for _, deniedID := range []string{"w-read-unmounted", "w-read-foreign", "w-read-escape", "w-read-traversal", "w-del-global", "w-del-foreign", "w-read-deleted"} {
		if !strings.Contains(toolResults[deniedID], `"scope_denied"`) {
			t.Fatalf("%s did not return scope_denied: %s", deniedID, toolResults[deniedID])
		}
	}
	if !strings.Contains(toolResults["w-save-secret"], `"skill_secret"`) {
		t.Fatalf("model skill_save with inactive key did not return skill_secret: %s", toolResults["w-save-secret"])
	}
	if !strings.Contains(toolResults["w-save-notes"], `"saved"`) || !strings.Contains(toolResults["w-del-temp"], `"deleted"`) {
		t.Fatalf("private skill save/delete mismatch: save=%s del=%s", toolResults["w-save-notes"], toolResults["w-del-temp"])
	}
	if !strings.Contains(toolResults["w-read-private"], "PRIVATE_ON_DEMAND_BODY") || !strings.Contains(toolResults["w-read-private"], "日本語") {
		t.Fatalf("private skill_read mismatch: %s", toolResults["w-read-private"])
	}
	if !strings.Contains(toolResults["w-list"], `"notes"`) || !strings.Contains(toolResults["w-list"], `"temp-skill"`) {
		t.Fatalf("skill_list missing private skills: %s", toolResults["w-list"])
	}

	turn3MountSys := writerReqs[16].system
	turn4HotSys := writerReqs[18].system
	turn5ColdSys := writerReqs[19].system
	if ids := parseAvailableSkillIDs(turn3MountSys); !reflect.DeepEqual(ids, []string{"notes", "reference", "ws-guide"}) {
		t.Fatalf("Writer mount-change L1 catalog = %v, want [notes reference ws-guide]", ids)
	}
	if strings.Contains(turn3MountSys, privateBody) || strings.Contains(turn3MountSys, workspaceBody) || strings.Contains(turn3MountSys, "Writer Renamed") {
		t.Fatalf("mount-change L1 leaked skill bodies or mutated captured bot identity")
	}
	if turn4HotSys != turn3MountSys {
		t.Fatalf("Writer turn 4 hot prompt busted KV cache after skill_save(cold-note)")
	}
	if ids := parseAvailableSkillIDs(turn5ColdSys); !reflect.DeepEqual(ids, []string{"cold-note", "notes", "reference", "ws-guide"}) {
		t.Fatalf("Writer cold-reopen L1 catalog = %v, want [cold-note notes reference ws-guide]", ids)
	}
	if strings.Contains(turn5ColdSys, coldBody) || strings.Contains(turn5ColdSys, privateBody) || strings.Contains(turn5ColdSys, "Writer Renamed") {
		t.Fatalf("cold-reopen L1 leaked skill bodies or mutated captured bot identity")
	}

	if got := filterCodingTools(byBot["Specialist"][0].tools); !reflect.DeepEqual(got, wantReadonly) {
		t.Fatalf("Specialist (PandoraBox readonly) coding tools = %v, want %v", got, wantReadonly)
	}
	if ids := parseAvailableSkillIDs(byBot["Specialist"][0].system); !reflect.DeepEqual(ids, []string{"reference"}) {
		t.Fatalf("Specialist L1 catalog = %v, want [reference]", ids)
	}
	if got := filterCodingTools(byBot["FullPreset"][0].tools); !reflect.DeepEqual(got, wantWorkspace) {
		t.Fatalf("FullPreset coding tools = %v, want %v", got, wantWorkspace)
	}
	if got := filterCodingTools(byBot["CustomOverride"][0].tools); !reflect.DeepEqual(got, []string{"read"}) {
		t.Fatalf("CustomOverride coding tools = %v, want [read]", got)
	}
	legacyTools := filterCodingTools(byBot["LegacyNull"][0].tools)
	if !slices.Contains(legacyTools, "read") || !slices.Contains(legacyTools, "write") || !slices.Contains(legacyTools, "bash") {
		t.Fatalf("LegacyNull coding tools lost worker defaults: %v", legacyTools)
	}
}

func TestPiProductSkillsRPCBackupAndRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const inactiveKey = "sk-m1-backup-inactive-0011223344"
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
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	wsSkillPath := filepath.Join(workspace, ".nagi", "skills", "ws-only", "SKILL.md")
	wsSkillText := "---\nname: Workspace Only\ndescription: External workspace mount\n---\nWS_ONLY_BODY\n"
	writeSkillFile(t, wsSkillPath, wsSkillText)
	localSkillPath := filepath.Join(home, ".agents", "skills", "local-only", "SKILL.md")
	writeSkillFile(t, localSkillPath, "---\nname: Local Only\ndescription: Opt in home skill\n---\nLOCAL_BODY\n")

	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		http.Error(w, "no provider calls expected", http.StatusBadRequest)
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
		"context_tokens": 65536, "output_tokens": 4096, "working_directory": workspace, "max_active_tasks": 2,
		"sources": map[string]any{
			productSource:      map[string]string{"api": "openai-chat-completions", "endpoint": provider.URL + "/v1", "api_key": productKey},
			"inactive-archive": map[string]string{"api": "openai-chat-completions", "endpoint": "http://127.0.0.1:9/v1", "api_key": inactiveKey},
		},
	})

	proc := startProductProcess(t, opts)
	proc.open(t, ctx)

	savedGlobal, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "global", SkillID: "global-ref"},
		Name:            "Global Reference", Description: "Library reference", Content: "GLOBAL_LIB_BODY\n", OperationID: "op-save-global",
	})
	if err != nil || savedGlobal.Source != "global" || savedGlobal.SkillID != "global-ref" || savedGlobal.Change != "saved" {
		t.Fatalf("SaveSkill(global) = %+v, %v", savedGlobal, err)
	}
	gotGlobal, err := proc.client.GetSkill(ctx, pi.ProductSkillKey{Source: "global", SkillID: "global-ref"})
	if err != nil || gotGlobal.Content != "GLOBAL_LIB_BODY\n" || gotGlobal.SHA256 != sha256HexBytes([]byte(gotGlobal.Markdown)) {
		t.Fatalf("GetSkill(global) = %+v, %v", gotGlobal, err)
	}

	bot, err := proc.client.CreateBot(ctx, pi.ProductBotCreateParams{
		OperationID: "op-create-bot",
		Profile: pi.ProductBotProfile{
			Name: "Archivist", Workspace: workspace, ToolPreset: pi.ToolPresetNone,
			MountedSkills: []pi.ProductMountedSkill{
				{Source: "global", SkillID: "global-ref"},
				{Source: "workspace", SkillID: "ws-only"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	listBefore, err := proc.client.ListSkills(ctx, bot.BotID)
	if err != nil || listBefore.DiscoverLocalSkills || len(listBefore.Local) != 0 ||
		len(listBefore.Global) != 1 || len(listBefore.Workspace) != 1 || len(listBefore.Mounted) != 2 ||
		listBefore.Mounted[0].Status != "available" || listBefore.Mounted[1].Status != "available" {
		t.Fatalf("ListSkills(before local opt-in) = %+v, %v", listBefore, err)
	}

	prefReply, err := proc.client.SetSkillPreferences(ctx, pi.ProductSkillPreferences{
		DiscoverLocalSkills: true, OperationID: "op-pref-on",
	})
	if err != nil || prefReply.Source != "global" || prefReply.SkillID != "" || prefReply.Change != "preferences" {
		t.Fatalf("SetSkillPreferences(true) = %+v, %v", prefReply, err)
	}
	listAfterPref, err := proc.client.ListSkills(ctx, bot.BotID)
	if err != nil || !listAfterPref.DiscoverLocalSkills || len(listAfterPref.Local) != 1 || listAfterPref.Local[0].SkillID != "local-only" {
		t.Fatalf("ListSkills(after local opt-in) = %+v, %v", listAfterPref, err)
	}

	privateContent := "RETAINED_PRIVATE_CONTENT\n日本語チェック\n"
	savedPriv, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "alpha-note"},
		Name:            "Alpha Note", Description: "Bot private checklist", Content: privateContent, OperationID: "op-save-priv",
	})
	if err != nil || savedPriv.Source != "private" || savedPriv.BotID != bot.BotID || savedPriv.SkillID != "alpha-note" || savedPriv.Change != "saved" {
		t.Fatalf("SaveSkill(private) = %+v, %v", savedPriv, err)
	}
	if _, err := proc.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "temp-delete"},
		Name:            "Temp Delete", Description: "Removed via RPC", Content: "TEMP\n", OperationID: "op-save-temp",
	}); err != nil {
		t.Fatal(err)
	}
	deletedPriv, err := proc.client.DeleteSkill(ctx, pi.ProductSkillDeleteParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "temp-delete"},
		OperationID:     "op-del-temp",
	})
	if err != nil || deletedPriv.Change != "deleted" {
		t.Fatalf("DeleteSkill(private) = %+v, %v", deletedPriv, err)
	}
	var remoteErr *pi.RemoteError
	if _, err := proc.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "temp-delete"}); !errors.As(err, &remoteErr) || remoteErr.Code != "skill_missing" {
		t.Fatalf("GetSkill(deleted) = %v, want skill_missing", err)
	}

	promoted, err := proc.client.PromoteSkill(ctx, pi.ProductSkillPromoteParams{
		BotID: bot.BotID, SkillID: "alpha-note", OperationID: "op-promote-1",
	})
	if err != nil || promoted.Source != "global" || promoted.BotID != bot.BotID || promoted.SkillID != "alpha-note" || promoted.Change != "promoted" {
		t.Fatalf("PromoteSkill(1) = %+v, %v", promoted, err)
	}
	promotedReplay, err := proc.client.PromoteSkill(ctx, pi.ProductSkillPromoteParams{
		BotID: bot.BotID, SkillID: "alpha-note", OperationID: "op-promote-1",
	})
	if err != nil || promotedReplay != promoted {
		t.Fatalf("PromoteSkill(replay) = %+v, %v", promotedReplay, err)
	}
	_, collisionErr := proc.client.PromoteSkill(ctx, pi.ProductSkillPromoteParams{
		BotID: bot.BotID, SkillID: "alpha-note", OperationID: "op-promote-collision",
	})
	if !errors.As(collisionErr, &remoteErr) || remoteErr.Code != "already_exists" {
		t.Fatalf("PromoteSkill(collision) = %v, want already_exists", collisionErr)
	}
	promotedTarget, err := proc.client.PromoteSkill(ctx, pi.ProductSkillPromoteParams{
		BotID: bot.BotID, SkillID: "alpha-note", TargetSkillID: "alpha-note-global", OperationID: "op-promote-2",
	})
	if err != nil || promotedTarget.SkillID != "alpha-note-global" || promotedTarget.Change != "promoted" {
		t.Fatalf("PromoteSkill(targetSkillId) = %+v, %v", promotedTarget, err)
	}

	origPrivBytes := productRead(t, filepath.Join(root, "bots", bot.BotID, "skills", "alpha-note", "SKILL.md"))
	origPrivSHA := sha256HexBytes(origPrivBytes)
	if !bytes.Equal(productRead(t, filepath.Join(root, "skills", "alpha-note", "SKILL.md")), origPrivBytes) ||
		!bytes.Equal(productRead(t, filepath.Join(root, "skills", "alpha-note-global", "SKILL.md")), origPrivBytes) {
		t.Fatal("promoted global skills did not preserve private SKILL.md bytes verbatim")
	}

	backupPath := filepath.Join(root, "m1-backup.json")
	if _, err := proc.client.BackupProfile(ctx, backupPath); err != nil {
		t.Fatalf("BackupProfile = %v", err)
	}

	proc.shutdown(t, ctx)

	skillEvents := drainSkillChangedEvents(proc)
	var sawSaved, sawDeleted, sawPromoted, sawPref bool
	for _, ev := range skillEvents {
		switch ev.Change {
		case "saved":
			if ev.Source == "private" && ev.BotID == bot.BotID && ev.SkillID == "alpha-note" {
				sawSaved = true
			}
		case "deleted":
			if ev.Source == "private" && ev.BotID == bot.BotID && ev.SkillID == "temp-delete" {
				sawDeleted = true
			}
		case "promoted":
			if ev.Source == "global" && ev.BotID == bot.BotID && ev.SkillID == "alpha-note-global" {
				sawPromoted = true
			}
		case "preferences":
			if ev.Source == "global" && ev.SkillID == "" && ev.BotID == "" {
				sawPref = true
			}
		}
	}
	if !sawSaved || !sawDeleted || !sawPromoted || !sawPref {
		t.Fatalf("missing typed skill.changed events: saved=%v deleted=%v promoted=%v pref=%v all=%+v",
			sawSaved, sawDeleted, sawPromoted, sawPref, skillEvents)
	}

	schemaCheck := runNodeSQLite(t, nodePath, filepath.Join(root, "nagi.db"), `
const { DatabaseSync } = require('node:sqlite');
const db = new DatabaseSync(process.argv[1]);
const v = db.prepare('PRAGMA user_version').get().user_version;
const fk = db.prepare('PRAGMA foreign_key_check').all().length;
db.close();
console.log(JSON.stringify({ v, fk }));
`)
	if !strings.Contains(schemaCheck, `"v":13`) || !strings.Contains(schemaCheck, `"fk":0`) {
		t.Fatalf("SQLite schema version/FK mismatch = %s", schemaCheck)
	}

	backupBytes := productRead(t, backupPath)
	if bytes.Contains(backupBytes, []byte(inactiveKey)) || bytes.Contains(backupBytes, []byte(productKey)) {
		t.Fatal("backup leaked API key bytes")
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(backupBytes, &env); err != nil {
		t.Fatal(err)
	}
	if len(env["backup"]) == 0 || len(env["recovery"]) == 0 {
		t.Fatal("backup missing top-level backup or recovery envelope")
	}
	if bytes.Contains(env["recovery"], []byte("privateSkillBundles")) {
		t.Fatal("privateSkillBundles must sit inside top-level backup, not recovery")
	}
	if bytes.Contains(productRead(t, opts.ProfilePath), []byte(`"backup"`)) {
		t.Fatal("normal contacts.json mirror must exclude backup envelope")
	}

	var backupParsed struct {
		Backup struct {
			Version             int `json:"version"`
			PrivateSkillBundles []struct {
				OwnerBotID   string `json:"ownerBotId"`
				SkillID      string `json:"skillId"`
				RelativePath string `json:"relativePath"`
				ByteLength   int    `json:"byteLength"`
				SHA256       string `json:"sha256"`
				Content      string `json:"content"`
			} `json:"privateSkillBundles"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(backupBytes, &backupParsed); err != nil {
		t.Fatal(err)
	}
	if backupParsed.Backup.Version != 1 || len(backupParsed.Backup.PrivateSkillBundles) != 1 {
		t.Fatalf("unexpected privateSkillBundles: %+v", backupParsed.Backup)
	}
	bundle := backupParsed.Backup.PrivateSkillBundles[0]
	if bundle.OwnerBotID != bot.BotID || bundle.SkillID != "alpha-note" || bundle.RelativePath != "SKILL.md" ||
		bundle.ByteLength != len(origPrivBytes) || bundle.SHA256 != origPrivSHA || bundle.Content != string(origPrivBytes) {
		t.Fatalf("privateSkillBundle mismatch: %+v", bundle)
	}

	restoreRoot := t.TempDir()
	restoreProfile := filepath.Join(restoreRoot, "contacts.json")
	if err := os.WriteFile(restoreProfile, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	var tamperedDigest map[string]any
	if err := json.Unmarshal(backupBytes, &tamperedDigest); err != nil {
		t.Fatal(err)
	}
	tamperedDigest["backup"].(map[string]any)["privateSkillBundles"].([]any)[0].(map[string]any)["sha256"] = strings.Repeat("0", 64)
	tamperedPath := filepath.Join(root, "tampered-digest.json")
	writeProductFixture(t, tamperedPath, tamperedDigest)
	if _, err := store.InspectStoreRecoveryCopy(tamperedPath); err == nil {
		t.Fatal("InspectStoreRecoveryCopy accepted tampered privateSkillBundles SHA-256")
	}
	if string(productRead(t, restoreProfile)) != "{corrupt" {
		t.Fatal("rejected tampered backup modified target profile")
	}

	var tamperedOwner map[string]any
	if err := json.Unmarshal(backupBytes, &tamperedOwner); err != nil {
		t.Fatal(err)
	}
	tamperedOwner["backup"].(map[string]any)["privateSkillBundles"].([]any)[0].(map[string]any)["ownerBotId"] = "nonexistent-bot"
	tamperedOwnerPath := filepath.Join(root, "tampered-owner.json")
	writeProductFixture(t, tamperedOwnerPath, tamperedOwner)
	if _, err := store.InspectStoreRecoveryCopy(tamperedOwnerPath); err == nil {
		t.Fatal("InspectStoreRecoveryCopy accepted invalid privateSkillBundles ownerBotId")
	}

	if err := os.Remove(wsSkillPath); err != nil {
		t.Fatal(err)
	}

	recoveryCopy, err := store.InspectStoreRecoveryCopy(backupPath)
	if err != nil || recoveryCopy.Contacts != 1 {
		t.Fatalf("InspectStoreRecoveryCopy(valid) = %+v, %v", recoveryCopy, err)
	}
	target, err := store.InspectStoreRecoveryTarget(restoreProfile)
	if err != nil {
		t.Fatal(err)
	}
	installRes, err := store.RestoreStoreRecoveryCopyWithRename(recoveryCopy, target, os.Rename)
	if err != nil || installRes.Outcome != store.StoreRecoveryVerifiedInstalled {
		t.Fatalf("RestoreStoreRecoveryCopyWithRename = %+v, %v", installRes, err)
	}

	restoreOpts := pi.ProductOptions{
		NodePath:        nodePath,
		WorkerPath:      workerPath,
		StateDir:        filepath.Join(restoreRoot, "pi"),
		ProfilePath:     restoreProfile,
		CredentialsPath: filepath.Join(restoreRoot, "connection.json"),
	}
	if _, err := os.Stat(restoreOpts.CredentialsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore unexpectedly created connection.json: %v", err)
	}

	restored1 := startProductProcess(t, restoreOpts)
	open1 := restored1.open(t, ctx)
	if open1.Snapshot.Preferences == nil || !open1.Snapshot.Preferences.DiscoverLocalSkills {
		t.Fatalf("restored app.open lost DiscoverLocalSkills preference: %+v", open1.Snapshot.Preferences)
	}
	restoredDiskPath := filepath.Join(restoreRoot, "bots", bot.BotID, "skills", "alpha-note", "SKILL.md")
	restoredDiskBytes := productRead(t, restoredDiskPath)
	if !bytes.Equal(restoredDiskBytes, origPrivBytes) || sha256HexBytes(restoredDiskBytes) != origPrivSHA {
		t.Fatalf("restored private SKILL.md digest mismatch: got=%s want=%s", sha256HexBytes(restoredDiskBytes), origPrivSHA)
	}
	list1, err := restored1.client.ListSkills(ctx, bot.BotID)
	if err != nil || len(list1.Private) != 1 || list1.Private[0].SkillID != "alpha-note" || list1.Private[0].SHA256 != origPrivSHA ||
		len(list1.Mounted) != 2 || list1.Mounted[0].Status != "missing" || list1.Mounted[0].Skill != nil ||
		list1.Mounted[1].Status != "missing" || list1.Mounted[1].Skill != nil {
		t.Fatalf("first restore ListSkills = %+v, %v", list1, err)
	}
	if bytes.Contains(productRead(t, restoreProfile), []byte(`"backup"`)) {
		t.Fatal("restored contacts.json mirror retained backup envelope")
	}
	restored1.shutdown(t, ctx)

	restored2 := startProductProcess(t, restoreOpts)
	open2 := restored2.open(t, ctx)
	if open2.Snapshot.Preferences == nil || !open2.Snapshot.Preferences.DiscoverLocalSkills {
		t.Fatalf("second reopen lost DiscoverLocalSkills preference: %+v", open2.Snapshot.Preferences)
	}
	got2, err := restored2.client.GetSkill(ctx, pi.ProductSkillKey{Source: "private", BotID: bot.BotID, SkillID: "alpha-note"})
	if err != nil || got2.Markdown != string(origPrivBytes) || got2.SHA256 != origPrivSHA {
		t.Fatalf("second reopen GetSkill(private) = %+v, %v", got2, err)
	}
	list2, err := restored2.client.ListSkills(ctx, bot.BotID)
	if err != nil || len(list2.Mounted) != 2 || list2.Mounted[0].Status != "missing" || list2.Mounted[1].Status != "missing" {
		t.Fatalf("second reopen missing mounts changed: %+v, %v", list2.Mounted, err)
	}
	restored2.shutdown(t, ctx)

	if got := providerCalls.Load(); got != 0 {
		t.Fatalf("RPC/backup/restore/reopen triggered %d provider calls, want 0", got)
	}
}
