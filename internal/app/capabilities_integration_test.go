//go:build integration

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func TestPiAppCapabilitiesSkillsMcpProfileAndM3Approval(t *testing.T) {
	const (
		mcpAuthHeader = "Bearer sk-cap-mcp-loopback-777"
		chatKey       = "xuhu-product-loopback"
		activityID    = "act-cap-m3"
		activityGoal  = "Execute private case script"
		scriptSource  = "export default async function(input, ctx) {\n  return { workspace: ctx.workspace };\n}\n"
		helperSource  = "export const version = 1;\n"
	)
	node, worker := os.Getenv("NAGI_TEST_NODE"), os.Getenv("NAGI_TEST_WORKER")
	payload := filepath.Dir(filepath.Dir(node))
	if !filepath.IsAbs(node) || node != filepath.Join(payload, "bin", "node") || worker != filepath.Join(payload, "dist", "worker.js") {
		t.Fatal("NAGI_TEST_NODE and NAGI_TEST_WORKER must identify one pinned payload")
	}

	root := t.TempDir()
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	localSkillDir := filepath.Join(fakeHome, ".agents", "skills", "local-ref")
	if err := os.MkdirAll(localSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localSkillDir, "SKILL.md"), []byte("---\nname: Local reference\ndescription: Offline local note.\n---\nRead-only local instructions.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var (
		mu             sync.Mutex
		mcpAuthCount   int
		mcpDeniedCount int
		activeSessions = make(map[string]struct{})
		activityTurns  int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			if r.Header.Get("Authorization") != mcpAuthHeader {
				mu.Lock()
				mcpDeniedCount++
				mu.Unlock()
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if r.Method == http.MethodDelete {
				mu.Lock()
				delete(activeSessions, r.Header.Get("Mcp-Session-Id"))
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var rpc struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
				Params struct {
					ProtocolVersion string `json:"protocolVersion"`
				} `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			session := r.Header.Get("Mcp-Session-Id")
			var resp map[string]any
			switch rpc.Method {
			case "initialize":
				mu.Lock()
				mcpAuthCount++
				next := fmt.Sprintf("mcp-sess-%d", mcpAuthCount)
				activeSessions[next] = struct{}{}
				mu.Unlock()
				w.Header().Set("Mcp-Session-Id", next)
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      rpc.ID,
					"result": map[string]any{
						"protocolVersion": rpc.Params.ProtocolVersion,
						"capabilities":    map[string]any{"tools": map[string]any{}},
						"serverInfo":      map[string]any{"name": "cap-http-fixture", "version": "1"},
					},
				}
			case "notifications/initialized":
				w.WriteHeader(http.StatusAccepted)
				return
			case "tools/list":
				mu.Lock()
				_, active := activeSessions[session]
				mu.Unlock()
				if !active {
					http.Error(w, "missing session", http.StatusBadRequest)
					return
				}
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      rpc.ID,
					"result": map[string]any{
						"tools": []map[string]any{
							{
								"name":        "list_documents",
								"description": "List case documents.",
								"annotations": map[string]any{"readOnlyHint": true},
								"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
							},
							{
								"name":        "tag_document",
								"description": "Tag a case document.",
								"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
							},
							{
								"name":        "purge_document",
								"description": "Delete a case document.",
								"annotations": map[string]any{"destructiveHint": true},
								"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
							},
						},
					},
				}
			default:
				resp = map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "error": map[string]any{"code": -32601, "message": "not found"}}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions" {
			body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
			var req struct {
				Messages []struct {
					Role string `json:"role"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(body, &req)
			mu.Lock()
			activityTurns++
			turn := activityTurns
			mu.Unlock()
			var delta map[string]any
			finish := "stop"
			if turn == 1 && len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role != "tool" {
				args, _ := json.Marshal(map[string]any{"skill_id": "case-script", "script": "run", "input": map[string]any{}})
				delta = map[string]any{
					"role": "assistant",
					"tool_calls": []any{map[string]any{
						"index": 0, "id": "call-exec-case", "type": "function",
						"function": map[string]string{"name": "skill_exec", "arguments": string(args)},
					}},
				}
				finish = "tool_calls"
			} else {
				delta = map[string]any{"role": "assistant", "content": "Activity finished after denial."}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, choice := range []map[string]any{
				{"index": 0, "delta": delta, "finish_reason": nil},
				{"index": 0, "delta": map[string]any{}, "finish_reason": finish},
			} {
				chunk, _ := json.Marshal(map[string]any{
					"id": "cap-loopback", "object": "chat.completion.chunk", "created": 1,
					"model": "xuhu-product-fixture", "choices": []any{choice},
				})
				fmt.Fprintf(w, "data: %s\n\n", chunk)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	isolateConnectionApp(t, root, payload)
	if err := saveConnectionConfig(connectionConfigPath(guiStorePath()), connectionConfig{
		API: connectionAPIChatCompletions, Endpoint: server.URL + "/v1", APIKey: chatKey,
		Model: "xuhu-product-fixture", ContextTokens: 65536, OutputTokens: 4096, WorkingDirectory: root, MaxActiveTasks: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeProfileJSON(guiStorePath(), storedState{
		Version:  contactStoreVersion,
		Selected: "cedar",
		Contacts: []storedContact{{ID: "cedar", Name: "Cedar", Workspace: root}},
		Issues:   []storedActivity{{ID: activityID, ContactID: "cedar", Goal: activityGoal, Status: activityStatusNotStarted, RequirementsRevision: 1}},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	msgs := locales.In("en")

	w := openProductWorkbench(t)
	var cedar *contact
	w.pump.do(func() { cedar = w.app.contactByID("cedar") })
	w.wait(func() bool { return cedar != nil && cedar.Loaded })

	w.frame(func() { w.app.openSettingsTab(settingsTabCapabilities, settingsSourceRail) })
	w.wait(func() bool {
		return w.app.settingsOpen && w.app.settingsTab == settingsTabCapabilities && w.app.capabilities.library.ready
	})

	w.click(msgs.CapLocalDiscovery())
	w.wait(func() bool {
		return !w.app.capabilitiesBusy() && w.app.capabilities.discoverLocal && len(w.app.capabilities.library.value.Local) == 1
	})

	w.click(msgs.CapNewSkill())
	w.wait(func() bool { return w.app.capabilities.newSkill != nil })
	w.frame(func() {
		e := w.app.capabilities.newSkill
		e.name = "Field guide"
		e.key.SkillID = "field-guide"
		e.summary = "Collect intake notes."
		e.body = "## Intake\n\n- Check files"
	})
	w.click(msgs.CapSaveSkill())
	w.wait(func() bool {
		return !w.app.capabilitiesBusy() && w.app.capabilities.newSkill == nil && len(w.app.capabilities.library.value.Global) == 1
	})

	w.click("Local reference")
	w.wait(func() bool {
		return w.app.capabilities.selectedSkill.Source == "local" && !w.view.HasText(msgs.CapEdit()) && !w.view.HasText(msgs.CapDeleteSkill())
	})

	w.click("Field guide")
	w.wait(func() bool {
		read := w.app.capabilities.skillDetails[pi.ProductSkillKey{Source: "global", SkillID: "field-guide"}]
		return read != nil && read.ready
	})
	w.click(msgs.CapEdit())
	w.wait(func() bool { return w.app.capabilities.skillEdit != nil })
	w.frame(func() { w.app.capabilities.skillEdit.body = "## Intake Revised\n\n- Check files and logs" })
	w.click(msgs.CapSaveSkill())
	w.wait(func() bool { return !w.app.capabilitiesBusy() && w.app.capabilities.skillEdit == nil })

	w.click(msgs.CapMcpCount(0))
	w.wait(func() bool { return w.app.capabilities.tab == capabilityTabMCP && w.app.capabilities.servers.ready })
	w.click(msgs.CapAddMcp())
	w.wait(func() bool { return w.app.capabilities.newMcp != nil })
	w.frame(func() {
		e := w.app.capabilities.newMcp
		e.config.Name = "studio-tools"
		e.config.URL = server.URL + "/mcp"
		e.secretHTTP = []mcpSecretDraft{{key: "Authorization", value: mcpAuthHeader}}
	})
	w.click(msgs.CapTestConnection())
	w.wait(func() bool {
		return !w.app.capabilitiesBusy() && w.app.capabilities.newMcp != nil && w.app.capabilities.newMcp.test != nil && len(w.app.capabilities.newMcp.test.Tools) == 3
	})
	w.click(msgs.CapSaveMcp())
	w.wait(func() bool {
		return !w.app.capabilitiesBusy() && w.app.capabilities.newMcp == nil && len(w.app.capabilities.servers.value) == 1
	})
	var serverID string
	w.pump.do(func() {
		serverID = w.app.capabilities.servers.value[0].ServerID
		if ed := w.app.capabilities.mcpEditors[serverID]; ed == nil || len(ed.secretHTTP) != 1 || ed.secretHTTP[0].value != "" {
			t.Errorf("accepted MCP save did not scrub plaintext secret: %+v", ed)
		}
	})

	w.click(msgs.CapMcpTestRefresh())
	w.wait(func() bool {
		ed := w.app.capabilities.mcpEditors[serverID]
		return !w.app.capabilitiesBusy() && ed != nil && ed.test != nil && ed.test.OK
	})
	w.click(msgs.CapRefreshStatus())
	w.wait(func() bool { return !w.app.capabilitiesBusy() })

	w.frame(func() {
		ed := w.app.capabilities.mcpEditors[serverID]
		ed.config.Isolation = "per_workspace"
		ed.changed("isolation")
	})
	w.click(msgs.CapSaveMcp())
	w.wait(func() bool {
		return !w.app.capabilitiesBusy() && w.app.capabilities.servers.value[0].Isolation == "per_workspace" &&
			w.app.capabilities.servers.value[0].CredentialStatus == "ok"
	})

	w.click(msgs.CapClear())
	w.click(msgs.CapSaveMcp())
	w.wait(func() bool {
		return !w.app.capabilitiesBusy() && w.app.capabilities.servers.value[0].CredentialStatus == "credential_missing"
	})
	w.click(msgs.CapMcpTestRefresh())
	w.wait(func() bool {
		ed := w.app.capabilities.mcpEditors[serverID]
		return !w.app.capabilitiesBusy() && ed != nil && ed.failure.detail != ""
	})
	mu.Lock()
	gotAuth, gotDenied := mcpAuthCount, mcpDeniedCount
	mu.Unlock()
	if gotAuth < 2 || gotDenied != 0 {
		t.Fatalf("expected authenticated probes >= 2 and fail-closed cleared-secret probe (0 unauthenticated hits), got auth=%d denied=%d", gotAuth, gotDenied)
	}

	w.frame(func() {
		ed := w.app.capabilities.mcpEditors[serverID]
		ed.secretHTTP[0].clear = false
		ed.secretHTTP[0].value = mcpAuthHeader
		ed.changed("secrets")
	})
	w.click(msgs.CapSaveMcp())
	w.wait(func() bool {
		return !w.app.capabilitiesBusy() && w.app.capabilities.servers.value[0].CredentialStatus == "ok"
	})
	w.click(msgs.CapMcpTestRefresh())
	w.wait(func() bool {
		ed := w.app.capabilities.mcpEditors[serverID]
		return !w.app.capabilitiesBusy() && ed != nil && ed.test != nil && ed.test.OK
	})
	mu.Lock()
	gotAuthAfterRestore := mcpAuthCount
	mu.Unlock()
	if gotAuthAfterRestore <= gotAuth {
		t.Fatalf("restoring MCP secret did not succeed authenticated probe: before=%d after=%d", gotAuth, gotAuthAfterRestore)
	}

	if _, err := w.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "private", BotID: "cedar", SkillID: "case-script"},
		Name:            "Case files script",
		Description:     "Summarize workspace location.",
		Content:         "Run scripts/run.mjs.",
		Manifest: &pi.ProductSkillManifest{
			Version: 1,
			Scripts: map[string]pi.ProductSkillScript{
				"run": {
					Entry:       "scripts/run.mjs",
					Description: "Summarize workspace.",
					TimeoutMS:   10000,
					Permissions: pi.ProductSkillPermissions{WorkspaceRead: true, NetworkOrigins: []string{server.URL}},
				},
			},
		},
		Scripts: map[string]string{
			"scripts/run.mjs":    scriptSource,
			"scripts/helper.mjs": helperSource,
		},
		OperationID: "op-save-private-case-script",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.client.SaveSkill(ctx, pi.ProductSkillSaveParams{
		ProductSkillKey: pi.ProductSkillKey{Source: "global", SkillID: "case-script"},
		Name:            "Existing conflict",
		Description:     "Blocks direct promotion slug.",
		Content:         "Existing global skill.",
		OperationID:     "op-save-global-conflict",
	}); err != nil {
		t.Fatal(err)
	}

	w.click(msgs.SettingsBack())
	w.wait(func() bool { return !w.app.settingsOpen })
	w.frame(func() { w.app.openContactProfile("cedar") })
	w.wait(func() bool {
		return w.app.detailIs(detailProfile) && cedar.capabilities.skills.ready && w.app.capabilities.servers.ready
	})

	w.frame(func() {
		ed := w.app.profileEditorFor("cedar")
		ed.Draft.ToolPreset = pi.ToolPresetCustom
		tools := []string{"find", "grep", "ls", "read", "write"}
		ed.Draft.BuiltinTools = &tools
		ed.Draft.MountedSkills = []pi.ProductMountedSkill{{Source: "global", SkillID: "field-guide"}}
		ed.Draft.McpBindings = []pi.ProductMcpBinding{{ServerID: serverID, Policy: "allowlist", AllowedTools: []string{"list_documents"}}}
	})
	w.click(msgs.ContactSaveChanges())
	w.wait(func() bool {
		return cedar.ToolPreset == pi.ToolPresetCustom &&
			cedar.BuiltinTools != nil && slices.Contains(*cedar.BuiltinTools, "write") &&
			len(cedar.MountedSkills) == 1 && len(cedar.McpBindings) == 1 &&
			slices.Equal(cedar.McpBindings[0].AllowedTools, []string{"list_documents"})
	})

	if _, err := w.client.ContinueActivity(ctx, pi.ProductActivityContinueParams{
		ProductActivityParams: pi.ProductActivityParams{ActivityID: activityID},
		ProductText:           pi.ProductText{Text: "Run private case script"},
		OperationID:           "op-continue-m3-request",
		ExpectedRevision:      1,
	}); err != nil {
		t.Fatal(err)
	}
	w.wait(func() bool {
		for _, r := range cedar.capabilities.requests {
			if r.State == "open" && r.SkillID == "case-script" {
				return true
			}
		}
		return false
	})
	w.frame(func() { w.app.profileScrollFor("cedar").Y = 600 })
	w.click(msgs.CapDeny())
	w.wait(func() bool {
		if w.app.capabilitiesBusy() {
			return false
		}
		for _, r := range cedar.capabilities.requests {
			if r.State == "open" && r.SkillID == "case-script" {
				return false
			}
		}
		return true
	})

	w.frame(func() { w.app.profileScrollFor("cedar").Y = 1200 })
	w.click(msgs.CapReviewApprove())
	w.wait(func() bool {
		return w.app.capabilities.review != nil && w.app.capabilities.review.read.ready && !w.app.capabilities.review.read.loading
	})
	w.frame(func() { w.app.capabilities.review.file = "scripts/run.mjs" })
	w.click(msgs.CopyCodeLabel())
	var clip string
	w.pump.do(func() { clip = w.view.Clipboard() })
	if clip != scriptSource {
		t.Fatalf("M3 review copy = %q, want %q", clip, scriptSource)
	}
	w.click(msgs.CapApproveBot())
	w.wait(func() bool {
		if w.app.capabilitiesBusy() || w.app.capabilities.review != nil {
			return false
		}
		row, ok := botSkill(cedar.capabilities.skills.value, "private", "case-script")
		return ok && row.ApprovalStatus == "user_approved"
	})

	w.frame(func() { w.app.profileScrollFor("cedar").Y = 1200 })
	w.click(msgs.CapReviewApprove())
	w.wait(func() bool { return w.app.capabilities.review != nil && w.app.capabilities.review.read.ready })
	w.click(msgs.CapRevokeApproval())
	w.wait(func() bool {
		if w.app.capabilitiesBusy() || w.app.capabilities.review != nil {
			return false
		}
		row, ok := botSkill(cedar.capabilities.skills.value, "private", "case-script")
		return ok && row.ApprovalStatus != "user_approved"
	})

	w.frame(func() { w.app.profileScrollFor("cedar").Y = 1200 })
	w.click(msgs.CapPromoteGlobal())
	w.wait(func() bool {
		return !w.app.capabilitiesBusy() && cedar.capabilities.promotion != nil && cedar.capabilities.promotion.problem != ""
	})
	w.frame(func() { cedar.capabilities.promotion.target = "case-script-v2" })
	w.click(msgs.CapPromote())
	w.wait(func() bool {
		if w.app.capabilitiesBusy() || cedar.capabilities.promotion != nil {
			return false
		}
		_, ok := botSkill(cedar.capabilities.skills.value, "global", "case-script-v2")
		return ok
	})
	promoted, err := w.client.GetSkill(ctx, pi.ProductSkillKey{Source: "global", SkillID: "case-script-v2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, wantFile := range []string{"SKILL.md", "scripts/run.mjs", "scripts/helper.mjs"} {
		if _, ok := promoted.Files[wantFile]; !ok {
			t.Fatalf("promoted global bundle missing %q: %v", wantFile, promoted.Files)
		}
	}

	w.shutdown(ctx)

	reopened := openProductWorkbench(t)
	var cedar2 *contact
	reopened.pump.do(func() { cedar2 = reopened.app.contactByID("cedar") })
	reopened.wait(func() bool { return cedar2 != nil && cedar2.Loaded })
	reopened.frame(func() { reopened.app.openContactProfile("cedar") })
	reopened.wait(func() bool {
		return cedar2.capabilities.skills.ready && reopened.app.capabilities.servers.ready
	})
	if !reopened.app.capabilities.discoverLocal {
		t.Fatal("reopen did not preserve DiscoverLocalSkills preference")
	}
	if cedar2.ToolPreset != pi.ToolPresetCustom || cedar2.BuiltinTools == nil || !slices.Contains(*cedar2.BuiltinTools, "write") {
		t.Fatalf("reopen lost custom tool selection: preset=%q tools=%v", cedar2.ToolPreset, cedar2.BuiltinTools)
	}
	if len(cedar2.MountedSkills) != 1 || cedar2.MountedSkills[0].SkillID != "field-guide" {
		t.Fatalf("reopen lost mounted skill: %+v", cedar2.MountedSkills)
	}
	if len(cedar2.McpBindings) != 1 || cedar2.McpBindings[0].Policy != "allowlist" || !slices.Equal(cedar2.McpBindings[0].AllowedTools, []string{"list_documents"}) {
		t.Fatalf("reopen lost MCP allowlist binding: %+v", cedar2.McpBindings)
	}
	if row, ok := botSkill(cedar2.capabilities.skills.value, "private", "case-script"); !ok || row.ApprovalStatus == "user_approved" {
		t.Fatalf("reopen did not preserve revoked approval state: ok=%v row=%+v", ok, row)
	}
	if len(reopened.app.capabilities.servers.value) != 1 || reopened.app.capabilities.servers.value[0].CredentialStatus != "ok" {
		t.Fatalf("reopen lost MCP credential status: %+v", reopened.app.capabilities.servers.value)
	}
	reopened.shutdown(ctx)
}
