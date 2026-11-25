package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/locales"
)

func capabilitiesUITestApp(t *testing.T) (*appModel, *contact) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	client, err := pi.StartProduct(pi.ProductOptions{
		NodePath:        "/bin/sh",
		WorkerPath:      stub,
		StateDir:        dir,
		ProfilePath:     filepath.Join(dir, "profile.sqlite"),
		CredentialsPath: filepath.Join(dir, "connection.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = client.Kill(ctx)
		<-client.CallbacksDone()
	})
	a, ct := workspaceTestApp(t)
	a.demo = false
	a.runtimeStatus = runtimeStateReady
	a.connectionGeneration = 1
	a.pi = &piAppHost{
		generation: 1,
		product:    client,
	}
	return a, ct
}

func TestCapabilitiesOfflineHidesTabAndStandaloneProfileShowsErrors(t *testing.T) {
	msgs := locales.In("en")
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.openSettingsTab(settingsTabCapabilities, settingsSourceRail)
	if a.settingsTab != settingsTabAppearance {
		t.Fatalf("offline openSettingsTab(capabilities) = %q, want %q", a.settingsTab, settingsTabAppearance)
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if tr.HasText(msgs.SettingsCapabilities()) {
		t.Fatalf("offline settings sidebar unexpectedly showed Capabilities: %v", tr.Texts())
	}

	a.closeSettings()
	a.openContactProfile(ct.ID)
	editor := a.profileEditorFor(ct.ID)
	editor.Error = profileNoticeErrorSave
	tr.Frame()
	if !tr.HasText(profileNoticeText(msgs, profileNoticeErrorSave)) {
		t.Fatalf("standalone profile editor did not surface save error: %v", tr.Texts())
	}
}

func TestCapabilitiesSettingsSkillsAndMcpMasterDetailVaultAndPrimaryCTA(t *testing.T) {
	msgs := locales.In("en")
	a, _ := capabilitiesUITestApp(t)
	a.windowWidth = 1040
	a.settingsOpen = true
	a.settingsTab = settingsTabCapabilities
	a.capabilities.tab = capabilityTabSkills
	a.capabilities.discoverLocal = true

	globalSummary := pi.ProductSkillSummary{
		Source:      "global",
		SkillID:     "field-guide",
		Name:        "Field guide",
		Description: "Collect intake notes.",
		SHA256:      "sha-global-1",
	}
	localSummary := pi.ProductSkillSummary{
		Source:      "local",
		SkillID:     "local-ref",
		Name:        "Local reference",
		Description: "Read-only local note.",
		SHA256:      "sha-local-1",
	}
	a.capabilities.library = capabilityRead[pi.ProductSkillList]{
		ready: true,
		value: pi.ProductSkillList{
			DiscoverLocalSkills: true,
			Global:              []pi.ProductSkillSummary{globalSummary},
			Local:               []pi.ProductSkillSummary{localSummary},
		},
	}
	globalKey := pi.ProductSkillKey{Source: "global", SkillID: "field-guide"}
	localKey := pi.ProductSkillKey{Source: "local", SkillID: "local-ref"}
	a.capabilities.selectedSkill = globalKey
	a.capabilities.skillDetails = map[pi.ProductSkillKey]*skillDetailRead{
		globalKey: {
			digest: "sha-global-1",
			capabilityRead: capabilityRead[pi.ProductSkillDetail]{
				ready: true,
				value: pi.ProductSkillDetail{
					ProductSkillSummary: globalSummary,
					Content:             "## Checklist\n\n- Inspect notes",
				},
			},
		},
		localKey: {
			digest: "sha-local-1",
			capabilityRead: capabilityRead[pi.ProductSkillDetail]{
				ready: true,
				value: pi.ProductSkillDetail{
					ProductSkillSummary: localSummary,
					Content:             "Local read-only instructions.",
				},
			},
		},
	}

	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.SetFocused(true)
	tr.Frame()

	for _, want := range []string{
		msgs.SettingsCapabilities(),
		msgs.CapSkillsCount(2),
		msgs.CapMcpCount(0),
		msgs.CapSkillGlobal(),
		msgs.CapLocalGroup(),
		msgs.CapPreview(),
		msgs.CapEdit(),
		msgs.CapDeleteSkill(),
	} {
		if !tr.HasText(want) {
			t.Fatalf("Skills tab missing %q: %v", want, tr.Texts())
		}
	}

	if err := tr.Click("Local reference"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if tr.HasText(msgs.CapEdit()) || tr.HasText(msgs.CapDeleteSkill()) || !tr.HasText(msgs.CapLocalReadOnly()) {
		t.Fatalf("local skill should be read-only without Edit or Delete: %v", tr.Texts())
	}

	if err := tr.Click("Field guide"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click(msgs.CapEdit()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	saveRect, okSave := tr.Find(msgs.CapSaveSkill())
	if !okSave {
		t.Fatalf("Edit mode missing Save skill CTA: %v", tr.Texts())
	}
	editImg := cloneRGBA(tr.Image())
	primaryFill := editImg.RGBAAt(int(saveRect.X+4), int(saveRect.Y+4))

	if err := tr.Click(msgs.CapPreview()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()

	if err := tr.Click(msgs.CapNewSkill()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.capabilities.newSkill == nil {
		t.Fatal("New skill frosted modal did not open")
	}
	nameLabel, okName := tr.Find(msgs.FieldName())
	if !okName {
		t.Fatal("New skill modal missing Name label")
	}
	tr.ClickAt(nameLabel.X+nameLabel.W+24, nameLabel.Y+nameLabel.H/2)
	tr.Type("Field Guide 2026!")
	tr.Frame()
	if a.capabilities.newSkill.key.SkillID != "field-guide-2026" {
		t.Fatalf("auto slug = %q, want field-guide-2026", a.capabilities.newSkill.key.SkillID)
	}
	slugLabel, okSlug := tr.Find(msgs.CapSkillSlug())
	if !okSlug {
		t.Fatal("New skill modal missing Slug label")
	}
	tr.ClickAt(slugLabel.X+slugLabel.W+24, slugLabel.Y+slugLabel.H/2)
	tr.Key(ui.Cmd, ui.KeyA)
	tr.Type("custom-slug-id")
	tr.Frame()
	tr.ClickAt(nameLabel.X+nameLabel.W+24, nameLabel.Y+nameLabel.H/2)
	tr.Type(" Revised")
	tr.Frame()
	if a.capabilities.newSkill.key.SkillID != "custom-slug-id" {
		t.Fatalf("manual slug was overwritten after name change: %q", a.capabilities.newSkill.key.SkillID)
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.capabilities.newSkill != nil {
		t.Fatal("Escape did not dismiss New skill modal")
	}
	if !tr.Focused(msgs.CapNewSkill()) {
		t.Fatal("Escape did not return focus to New skill button")
	}

	readOnlyTrue := true
	destructiveTrue := true
	poolPID := 4242
	serverView := pi.ProductMcpServerView{
		ProductMcpServerConfig: pi.ProductMcpServerConfig{
			ServerID:         "mcp-studio",
			Name:             "studio-tools",
			Transport:        "http",
			Isolation:        "shared",
			Enabled:          true,
			URL:              "https://mcp.example.invalid/mcp",
			SecretHeaderKeys: []string{"authorization"},
		},
		Revision:         3,
		CredentialStatus: "ok",
		Catalog: &pi.ProductMcpCatalog{
			FetchedAt: "2026-11-25T10:00:00Z",
			Tools: []pi.ProductMcpTool{
				{Name: "list_docs", Description: "List docs", ReadOnlyHint: &readOnlyTrue},
				{Name: "tag_doc", Description: "Tag doc"},
				{Name: "purge_doc", Description: "Delete doc", DestructiveHint: &destructiveTrue},
			},
		},
		Pools: []pi.ProductMcpPool{
			{ServerID: "mcp-studio", ScopeKey: "shared:mcp-studio", Isolation: "shared", State: "connected", RefCount: 2, PID: &poolPID},
		},
	}
	a.capabilities.tab = capabilityTabMCP
	a.capabilities.servers = capabilityRead[[]pi.ProductMcpServerView]{ready: true, value: []pi.ProductMcpServerView{serverView}}
	a.capabilities.selectedMcp = serverView.ServerID
	a.capabilities.mcpEditors = map[string]*mcpEditor{serverView.ServerID: newMcpEditor(serverView)}
	tr.Frame()

	for _, want := range []string{
		"studio-tools",
		msgs.CapReadOnly(),
		msgs.CapMutating(),
		msgs.CapDestructive(),
		msgs.CapLivePools(),
		msgs.CapPoolConnected(),
		msgs.CapPoolReferences(2),
		msgs.CapPoolPID("4242"),
		msgs.CapSaveMcp(),
	} {
		if !tr.HasText(want) {
			t.Fatalf("MCP tab missing %q: %v", want, tr.Texts())
		}
	}

	ed := a.capabilities.mcpEditors[serverView.ServerID]
	inBlank, secBlank, okBlank := ed.input(msgs)
	if !okBlank || !slices.Equal(inBlank.SecretHeaderKeys, []string{"authorization"}) || secBlank != nil {
		t.Fatalf("blank saved secret should keep vault value with nil patch: ok=%v keys=%v sec=%+v", okBlank, inBlank.SecretHeaderKeys, secBlank)
	}

	ed.secretHTTP[0].clear = true
	_, secClear, okClear := ed.input(msgs)
	if !okClear || secClear == nil {
		t.Fatalf("explicit Clear should emit secret patch: ok=%v sec=%+v", okClear, secClear)
	}
	if ptr, present := secClear.Headers["authorization"]; !present || ptr != nil {
		t.Fatalf("explicit Clear must send nil pointer entry: present=%v ptr=%v", present, ptr)
	}

	ed.secretHTTP[0].clear = false
	ed.secretHTTP[0].value = "Bearer sk-mcp-secret-98765"
	redacted := a.redactConnectionText("auth error Bearer sk-mcp-secret-98765 and token sk-mcp-secret-98765")
	if strings.Contains(redacted, "sk-mcp-secret-98765") || !strings.Contains(redacted, "[redacted]") {
		t.Fatalf("MCP secret and Bearer token were not redacted: %q", redacted)
	}

	ed.headers = "Authorization: public-collision\n"
	if _, _, ok := ed.input(msgs); ok || ed.fields["secrets"] == "" {
		t.Fatalf("public/secret header collision should fail validation: ok=%v fields=%+v", ok, ed.fields)
	}
	ed.headers = ""

	ed.config.Transport = "stdio"
	ed.config.Command = "/usr/local/bin/mcp-server"
	ed.secretEnv = []mcpSecretDraft{{key: "API_TOKEN", value: "tok-123"}}
	inStdio, secStdio, okStdio := ed.input(msgs)
	if !okStdio || inStdio.Command != "/usr/local/bin/mcp-server" || secStdio == nil || secStdio.Env["API_TOKEN"] == nil || *secStdio.Env["API_TOKEN"] != "tok-123" {
		t.Fatalf("stdio input validation mismatch: ok=%v in=%+v sec=%+v", okStdio, inStdio, secStdio)
	}
	ed.config.Transport = "http"

	ed.stale = true
	tr.Frame()
	if !tr.HasText(msgs.CapMcpChanged()) || !tr.HasText(msgs.CapReloadServer()) {
		t.Fatalf("stale_revision banner missing: %v", tr.Texts())
	}
	if err := tr.Click(msgs.CapReloadServer()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.capabilities.mcpEditors[serverView.ServerID].stale {
		t.Fatal("Load saved version did not clear stale editor state")
	}

	saveMcpRect, _ := tr.Find(msgs.CapSaveMcp())
	mcpImg := cloneRGBA(tr.Image())
	if mcpImg.RGBAAt(int(saveMcpRect.X+4), int(saveMcpRect.Y+4)) != primaryFill {
		t.Fatal("Save server button did not render with primary fill")
	}

	a.windowWidth = 720
	tr.SetSize(720, 640)
	tr.SetLocale("zh-Hans")
	tr.SetDark(true)
	tr.Frame()
	zh := locales.In("zh-Hans")
	listRect, okL := tr.Find("studio-tools")
	urlRect, okU := tr.Find(zh.CapServerURL())
	if !okL || !okU || urlRect.Y <= listRect.Y || urlRect.X+urlRect.W > 720 {
		t.Fatalf("narrow 720x640 MCP layout failed stack/width check: list=%+v(%v) url=%+v(%v)", listRect, okL, urlRect, okU)
	}

	a.capabilities.servers.value = nil
	a.capabilities.selectedMcp = ""
	tr.Frame()
	if err := tr.Click(zh.CapAddMcp()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.capabilities.newMcp == nil {
		t.Fatal("Add server modal did not open")
	}
	for _, label := range []string{zh.CreateBotCancel(), zh.CapSaveMcp()} {
		r, ok := tr.Find(label)
		if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 720 || r.Y+r.H > 640 {
			t.Fatalf("narrow 720x640 Add server footer control %q clipped/missing: %+v ok=%v", label, r, ok)
		}
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.capabilities.newMcp != nil {
		t.Fatal("Escape did not dismiss Add server modal")
	}
}

func TestBotProfileThreeLayerCapabilitiesFooterAndM3ReviewModal(t *testing.T) {
	msgs := locales.In("en")
	a, ct := capabilitiesUITestApp(t)
	a.windowWidth = 1040

	readOnlyTrue := true
	mcpServer := pi.ProductMcpServerView{
		ProductMcpServerConfig: pi.ProductMcpServerConfig{
			ServerID: "mcp-1", Name: "studio-tools", Transport: "http", Isolation: "shared", Enabled: true,
		},
		Revision:         1,
		CredentialStatus: "ok",
		Catalog: &pi.ProductMcpCatalog{
			Tools: []pi.ProductMcpTool{
				{Name: "list_docs", Description: "List docs", ReadOnlyHint: &readOnlyTrue},
				{Name: "tag_doc", Description: "Tag doc"},
			},
		},
	}
	a.capabilities.servers = capabilityRead[[]pi.ProductMcpServerView]{ready: true, value: []pi.ProductMcpServerView{mcpServer}}

	globalSkill := pi.ProductSkillSummary{Source: "global", SkillID: "field-guide", Name: "Field guide", Description: "Guide", SHA256: "sha-g1"}
	privateSkill := pi.ProductSkillSummary{
		Source:             "private",
		SkillID:            "case-script",
		Name:               "Case files script",
		Description:        "Summarize workspace.",
		SHA256:             "sha-p1",
		ApprovalDigest:     "digest-abc123456789",
		PermissionsDigest:  "perm-xyz",
		ApprovalStatus:     "approval_required",
		CanonicalWorkspace: "/tmp/canonical-ws",
		Scripts: map[string]pi.ProductSkillScript{
			"run": {
				Entry:       "scripts/run.mjs",
				Description: "Summarize workspace",
				TimeoutMS:   10000,
				Permissions: pi.ProductSkillPermissions{WorkspaceRead: true, NetworkOrigins: []string{"http://127.0.0.1:58423"}},
			},
		},
	}
	ct.ToolPreset = pi.ToolPresetReadonly
	ct.MountedSkills = []pi.ProductMountedSkill{
		{Source: "global", SkillID: "field-guide"},
		{Source: "global", SkillID: "missing-skill"},
	}
	ct.McpBindings = []pi.ProductMcpBinding{
		{ServerID: "mcp-1", Policy: "allowlist", AllowedTools: []string{"list_docs"}},
	}
	ct.capabilities.skills = capabilityRead[pi.ProductSkillList]{
		ready: true,
		value: pi.ProductSkillList{
			Global:  []pi.ProductSkillSummary{globalSkill},
			Private: []pi.ProductSkillSummary{privateSkill},
		},
	}
	req := pi.ProductSkillApprovalRequest{
		RequestID:          "req-1",
		ActivityID:         "act-1",
		BotID:              ct.ID,
		Source:             "private",
		SkillID:            "case-script",
		Script:             "run",
		State:              "open",
		Revision:           1,
		InstallationID:     "inst-1",
		ApprovalDigest:     "digest-abc123456789",
		PermissionsDigest:  "perm-xyz",
		CanonicalWorkspace: "/tmp/canonical-ws",
	}
	ct.capabilities.requests = map[string]pi.ProductSkillApprovalRequest{req.RequestID: req}

	a.profileEditors = map[string]*contactProfileEditor{ct.ID: newContactProfileEditor(contactProfileDraft(ct))}
	a.contactProfileID = ct.ID
	a.openDetail(detailProfile)
	editor := a.profileEditorFor(ct.ID)

	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.SetFocused(true)
	tr.Frame()

	footerSaveBefore, okBefore := tr.Find(msgs.ContactSaveChanges())
	footerArchiveBefore, okArchBefore := tr.Find(msgs.ProfileArchive())
	if !okBefore || !okArchBefore {
		t.Fatalf("fixed profile footer missing Save changes or Archive: save=%v archive=%v", okBefore, okArchBefore)
	}

	a.profileScrollFor(ct.ID).Y = 1200
	tr.Frame()
	footerSaveAfter, _ := tr.Find(msgs.ContactSaveChanges())
	footerArchiveAfter, _ := tr.Find(msgs.ProfileArchive())
	if footerSaveAfter != footerSaveBefore || footerArchiveAfter != footerArchiveBefore {
		t.Fatalf("fixed Bot Profile footer moved on scroll: save %+v->%+v archive %+v->%+v",
			footerSaveBefore, footerSaveAfter, footerArchiveBefore, footerArchiveAfter)
	}

	for _, want := range []string{
		msgs.CapBuiltInTools(),
		msgs.CapSkills(),
		msgs.CapMcp(),
		msgs.CapAvailable(),
		msgs.CapMissing(),
		msgs.CapPendingApproval("Case files script", "run"),
		msgs.CapDeny(),
		msgs.CapPrivateSkills(),
		msgs.CapPromoteGlobal(),
	} {
		if !tr.HasText(want) {
			t.Fatalf("Bot Profile missing capability item %q: %v", want, tr.Texts())
		}
	}

	a.profileScrollFor(ct.ID).Y = 450
	tr.Frame()
	if err := tr.Click(msgs.CapCustom()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if editor.Draft.ToolPreset != pi.ToolPresetCustom || editor.Draft.BuiltinTools == nil {
		t.Fatalf("switching to Custom preset did not initialize BuiltinTools: preset=%q tools=%v", editor.Draft.ToolPreset, editor.Draft.BuiltinTools)
	}
	if err := tr.Click("write"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !slices.Contains(*editor.Draft.BuiltinTools, "write") || profileDraftEqual(editor.Draft, editor.initial) {
		t.Fatalf("toggling custom tool chip did not dirty draft: %v", *editor.Draft.BuiltinTools)
	}

	if err := tr.Click(msgs.CapNone()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if editor.Draft.ToolPreset != pi.ToolPresetNone || editor.Draft.BuiltinTools != nil {
		t.Fatalf("named preset None must clear BuiltinTools to nil: preset=%q tools=%v", editor.Draft.ToolPreset, editor.Draft.BuiltinTools)
	}

	if err := tr.Click(msgs.CapReadOnly()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()

	a.profileScrollFor(ct.ID).Y = 1400
	tr.Frame()
	if err := tr.Click(msgs.CapAll()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if editor.Draft.McpBindings[0].Policy != "all" || len(editor.Draft.McpBindings[0].AllowedTools) != 0 {
		t.Fatalf("switching MCP scope to All did not clear AllowedTools: %+v", editor.Draft.McpBindings[0])
	}

	ct.capabilities.promotion = &skillPromotion{
		key:     pi.ProductSkillKey{Source: "private", BotID: ct.ID, SkillID: "case-script"},
		target:  "INVALID SLUG",
		problem: msgs.CapSkillSlugTaken(),
	}
	a.profileScrollFor(ct.ID).Y = 950
	tr.Frame()
	if !tr.HasText(msgs.CapSkillSlugTaken()) || !tr.HasText(msgs.CapPromote()) {
		t.Fatalf("private promotion duplicate-ID recovery UI missing: %v", tr.Texts())
	}
	if err := tr.Click(msgs.CapPromote()); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.capabilities.promotion.problem != msgs.CapSkillSlugInvalid() {
		t.Fatalf("invalid alternate slug did not report validation error: %q", ct.capabilities.promotion.problem)
	}

	const exactScriptSource = "export default async function(input, ctx) {\n  return { workspace: ctx.workspace };\n}\n"
	review := &skillReviewState{
		key:     pi.ProductSkillKey{Source: "private", BotID: ct.ID, SkillID: "case-script"},
		name:    "Case files script",
		request: &req,
		file:    "scripts/run.mjs",
		read: capabilityRead[pi.ProductSkillDetail]{
			ready: true,
			value: pi.ProductSkillDetail{
				ProductSkillSummary: privateSkill,
				Files: map[string]string{
					"SKILL.md":        "---\nname: Case files script\n---\n",
					"scripts/run.mjs": exactScriptSource,
				},
			},
		},
	}
	a.capabilities.review = review
	tr.Frame()

	scriptsCount := 0
	copyCount := 0
	for _, txt := range tr.Texts() {
		if txt == msgs.CapScripts() {
			scriptsCount++
		}
		if txt == msgs.CopyCodeLabel() {
			copyCount++
		}
	}
	if scriptsCount != 1 {
		t.Fatalf("M3 Permission Review should render 1 Scripts heading, got %d", scriptsCount)
	}
	if copyCount != 1 {
		t.Fatalf("M3 Permission Review should render 1 Copy button, got %d", copyCount)
	}
	if !a.canApproveSkill(review) {
		t.Fatal("matching open approval request should be approvable")
	}
	if err := tr.Click(msgs.CopyCodeLabel()); err != nil {
		t.Fatal(err)
	}
	if tr.Clipboard() != exactScriptSource {
		t.Fatalf("M3 source Copy wrote %q, want exact bytes %q", tr.Clipboard(), exactScriptSource)
	}

	drifted := req
	drifted.Revision = 2
	a.applySkillApprovalRequests([]pi.ProductSkillApprovalRequest{drifted})
	tr.Frame()
	if a.canApproveSkill(review) {
		t.Fatal("request revision drift must disable approval")
	}
	if !tr.HasText(msgs.CapReviewRequestChanged()) {
		t.Fatalf("request drift warning missing: %v", tr.Texts())
	}

	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.capabilities.review != nil {
		t.Fatal("Escape did not dismiss M3 Permission Review modal")
	}
	if !a.detailIs(detailProfile) {
		t.Fatal("Escape on M3 modal unexpectedly closed underlying Bot Profile")
	}
}

func TestCapabilitiesHelpersValidationAndReviewFileFallback(t *testing.T) {
	if got := skillSlug("  Field Guide 2026!!  "); got != "field-guide-2026" {
		t.Fatalf("skillSlug = %q, want field-guide-2026", got)
	}
	longSlug := skillSlug("a-" + strings.Repeat("b", 80))
	if len(longSlug) != 63 || strings.HasSuffix(longSlug, "-") {
		t.Fatalf("long skillSlug = %q (len=%d)", longSlug, len(longSlug))
	}

	msgs := locales.In("en")
	ed := newMcpEditor(pi.ProductMcpServerView{
		ProductMcpServerConfig: pi.ProductMcpServerConfig{
			ServerID:  "mcp-test",
			Name:      "stdio-tool",
			Transport: "stdio",
			Isolation: "shared",
			Command:   "/usr/local/bin/mcp-tool",
		},
	})
	ed.args = "first\r\n\r\nsecond\r\n"
	in, _, ok := ed.input(msgs)
	if !ok || !slices.Equal(in.Args, []string{"first", "second"}) {
		t.Fatalf("CRLF args parsing = ok=%v args=%#v", ok, in.Args)
	}

	ed.args = "bad\x00arg"
	if _, _, ok := ed.input(msgs); ok || ed.fields["args"] == "" {
		t.Fatalf("NUL arg should fail validation: ok=%v fields=%+v", ok, ed.fields)
	}

	ed.args = ""
	ed.config.Command = "relative/bin"
	ed.config.Cwd = "relative/cwd"
	if _, _, ok := ed.input(msgs); ok || ed.fields["command"] == "" || ed.fields["cwd"] == "" {
		t.Fatalf("relative stdio paths should fail validation: ok=%v fields=%+v", ok, ed.fields)
	}

	ed.config.Transport = "http"
	ed.config.URL = "https://user:pass@mcp.example.invalid/mcp"
	if _, _, ok := ed.input(msgs); ok || ed.fields["url"] == "" {
		t.Fatalf("HTTP URL with userinfo should fail validation: ok=%v fields=%+v", ok, ed.fields)
	}

	for _, loc := range []string{"en", "zh-Hans"} {
		locMsgs := locales.In(loc)
		for _, tool := range pi.ResolveCodingTools(nil, pi.ToolPresetWorkspace) {
			if builtinToolDescription(locMsgs, tool) == "" {
				t.Fatalf("missing localized description for tool %q in %q", tool, loc)
			}
		}
		if got := builtinToolDescription(locMsgs, "unknown"); got != "" {
			t.Fatalf("unknown tool description = %q, want empty", got)
		}
	}

	a, ct := capabilitiesUITestApp(t)
	a.windowWidth = 1040
	const onlyScript = "export default async function() { return {}; }\n"
	a.capabilities.review = &skillReviewState{
		key:  pi.ProductSkillKey{Source: "private", BotID: ct.ID, SkillID: "script-only"},
		name: "Script only",
		file: "SKILL.md",
		read: capabilityRead[pi.ProductSkillDetail]{
			ready: true,
			value: pi.ProductSkillDetail{
				ProductSkillSummary: pi.ProductSkillSummary{
					Source:  "private",
					SkillID: "script-only",
					Name:    "Script only",
					Scripts: map[string]pi.ProductSkillScript{
						"run": {Entry: "scripts/run.mjs", Description: "Run"},
					},
				},
				Files: map[string]string{
					"scripts/run.mjs": onlyScript,
				},
			},
		},
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if a.capabilities.review.file != "scripts/run.mjs" {
		t.Fatalf("skillReviewDialog file fallback = %q, want scripts/run.mjs", a.capabilities.review.file)
	}
	if err := tr.Click(msgs.CopyCodeLabel()); err != nil {
		t.Fatal(err)
	}
	if tr.Clipboard() != onlyScript {
		t.Fatalf("fallback Copy wrote %q, want %q", tr.Clipboard(), onlyScript)
	}
}
