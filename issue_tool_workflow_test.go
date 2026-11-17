package main

import (
	"errors"
	"strings"
	"testing"
)

func TestAddWorkflowGuidanceUpgradesReleasedPrefixesWithoutProfileLoss(t *testing.T) {
	profile := "Purpose: Keep the custom brief.\nInstructions: Preserve this exact text."
	cases := []struct {
		name     string
		current  string
		guidance string
	}{
		{"conversation-initial", previousIssueToolConversationGuidance, issueToolConversationGuidance},
		{"conversation-207", previousIssueToolConversationGuidanceV2, issueToolConversationGuidance},
		{"conversation-2026-11-03", previousIssueToolConversationGuidanceV3, issueToolConversationGuidance},
		{"work-released", previousIssueToolWorkGuidance, issueToolWorkGuidance},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.guidance + "\n\n" + profile
			got := addWorkflowGuidance(tc.current+"\n\n"+profile, tc.guidance)
			if got != want {
				t.Fatalf("upgrade lost profile or stacked the old role:\n%s", got)
			}
			if again := addWorkflowGuidance(got, tc.guidance); again != got {
				t.Fatal("repeated ensure changed an already-current prompt")
			}
		})
	}
}

func TestIssueToolNewSessionPromptCombinesRoleAndProfile(t *testing.T) {
	ct := &contact{ID: "profile-contact", Purpose: "Keep the launch notes.", Instructions: "Cite earlier decisions."}
	a := &appModel{}
	if got := a.issueToolConversationSystemPrompt(ct); got != "Purpose: Keep the launch notes.\nInstructions: Cite earlier decisions." {
		t.Fatalf("disabled issue tools changed the profile prompt: %q", got)
	}
	a.issueToolServer = &issueToolIPCServer{}
	a.issueToolEndpoint, a.issueToolHelper = "/tmp/issue.sock", "/tmp/helper"
	a.connectionManaged = true
	got := a.issueToolConversationSystemPrompt(ct)
	if !strings.HasPrefix(got, issueToolConversationGuidance+"\n\n") ||
		!strings.Contains(got, "Purpose: Keep the launch notes.") ||
		!strings.Contains(got, "Instructions: Cite earlier decisions.") {
		t.Fatalf("combined prompt = %q", got)
	}
	if ct.Purpose != "Keep the launch notes." || ct.Instructions != "Cite earlier decisions." {
		t.Fatal("prompt construction mutated the contact profile")
	}
	empty := &contact{ID: "empty-profile"}
	if got := a.issueToolConversationSystemPrompt(empty); got != issueToolConversationGuidance {
		t.Fatalf("empty-profile prompt = %q", got)
	}
	if got := a.issueToolWorkSystemPrompt(empty); got != issueToolWorkGuidance {
		t.Fatalf("empty-profile work prompt = %q", got)
	}
}

func TestIssueToolWorkflowGuidanceRequiresManagedConnection(t *testing.T) {
	ct := &contact{Purpose: "Keep profile", Instructions: "Keep instructions"}
	a := &appModel{
		issueToolServer: &issueToolIPCServer{}, issueToolEndpoint: "/tmp/issue.sock",
		issueToolHelper: "/tmp/helper", connectionManaged: false,
	}
	profile := "Purpose: Keep profile\nInstructions: Keep instructions"
	if got := a.issueToolConversationSystemPrompt(ct); got != profile {
		t.Fatalf("legacy conversation prompt = %q", got)
	}
	if got := a.issueToolWorkSystemPrompt(ct); got != profile {
		t.Fatalf("legacy work prompt = %q", got)
	}
	a.connectionManaged = true
	if got := a.issueToolConversationSystemPrompt(ct); !strings.Contains(got, issueToolConversationGuidance) {
		t.Fatalf("managed conversation prompt missing guidance: %q", got)
	}
	if got := a.issueToolWorkSystemPrompt(ct); !strings.Contains(got, issueToolWorkGuidance) {
		t.Fatalf("managed work prompt missing guidance: %q", got)
	}
	a.connectionManaged = false
	if got := a.issueToolConversationSystemPrompt(ct); got != profile {
		t.Fatalf("legacy conversation prompt changed after managed toggle: %q", got)
	}
}

func TestIssueToolTypedErrorOnlyMarksSchemaRefresh(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	rt := &runtimeClient{}
	a.noteIssueToolTaskError(rt, "session-typed", &runtimeError{Code: "tool_error", Message: "not schema"})
	if a.issueToolRefreshRequired(rt, "session-typed") {
		t.Fatal("non-schema error marked refresh")
	}
	a.noteIssueToolTaskError(rt, "session-typed", &runtimeError{Code: "tool_schema_changed", Message: "schema"})
	if !a.issueToolRefreshRequired(rt, "session-typed") {
		t.Fatal("typed schema error did not mark refresh")
	}
}

func TestIssueToolNativeTaskGuidanceRequiresExactReadback(t *testing.T) {
	connectionID, executionID := "connection-native", "connection-native"
	base := runtimeConnectionConfig{SessionID: "native-session", ConfigRevision: 7, StorageSchema: 2}
	base.EffectiveConfig.SystemPrompt = previousIssueToolConversationGuidanceV2 + "\n\nPROFILE BYTES"
	base.EffectiveConfig.ConnectionID = &connectionID
	base.EffectiveConfig.ExecutionConnectionID = &executionID
	base.EffectiveConfig.ConnectionBinding.State = "bound"
	base.EffectiveConfig.ToolSelection = runtimeToolSelection{
		Builtin: []string{"read"},
		External: []runtimeExternalToolBinding{{
			SourceID: issueToolSourceID, ToolName: "issue_get", ExposedName: "issue_get",
		}},
	}
	updated := runtimeConfigUpdated{runtimeConnectionConfig: base}
	nativeErr := &runtimeError{Code: "continuation_not_supported", Message: "native"}
	if got := issueToolNativeTaskGuidance(base, updated, nativeErr, issueToolConversationGuidance, true, false); got != issueToolConversationGuidance {
		t.Fatalf("valid native guidance = %q", got)
	}
	withReadbackError := updated
	withReadbackError.ReadbackError = errors.New("readback failed")
	if got := issueToolNativeTaskGuidance(base, withReadbackError, nativeErr, issueToolConversationGuidance, true, false); got != "" {
		t.Fatalf("readback error accepted: %q", got)
	}
	withRevision := updated
	withRevision.ConfigRevision++
	if got := issueToolNativeTaskGuidance(base, withRevision, nativeErr, issueToolConversationGuidance, true, false); got != "" {
		t.Fatalf("revision change accepted: %q", got)
	}
	withPrompt := updated
	withPrompt.EffectiveConfig.SystemPrompt += " changed"
	if got := issueToolNativeTaskGuidance(base, withPrompt, nativeErr, issueToolConversationGuidance, true, false); got != "" {
		t.Fatalf("readback prompt change accepted: %q", got)
	}
	withTools := updated
	withTools.EffectiveConfig.ToolSelection.Builtin = append([]string{}, withTools.EffectiveConfig.ToolSelection.Builtin...)
	withTools.EffectiveConfig.ToolSelection.Builtin = append(withTools.EffectiveConfig.ToolSelection.Builtin, "edit")
	if got := issueToolNativeTaskGuidance(base, withTools, nativeErr, issueToolConversationGuidance, true, false); got != "" {
		t.Fatalf("readback tool change accepted: %q", got)
	}
	if got := issueToolNativeTaskGuidance(base, updated, &runtimeError{Code: "tool_schema_changed", Message: "schema"}, issueToolConversationGuidance, true, false); got != "" {
		t.Fatalf("non-native error accepted: %q", got)
	}
	if got := issueToolNativeTaskGuidance(base, updated, nativeErr, issueToolConversationGuidance, false, false); got != "" {
		t.Fatalf("missing tools accepted: %q", got)
	}
	if got := issueToolNativeTaskGuidance(base, updated, nativeErr, issueToolConversationGuidance, true, true); got != "" {
		t.Fatalf("refresh accepted: %q", got)
	}
	mixed := base
	mixed.EffectiveConfig.SystemPrompt = previousIssueToolConversationGuidanceV2 + " trailing mixed text"
	if got := issueToolNativeTaskGuidance(mixed, runtimeConfigUpdated{runtimeConnectionConfig: mixed}, nativeErr, issueToolConversationGuidance, true, false); got != "" {
		t.Fatalf("mixed prompt accepted: %q", got)
	}
	quoted := base
	quoted.EffectiveConfig.SystemPrompt = previousIssueToolConversationGuidanceV2 + "\n\nPROFILE: quote " + issueToolConversationGuidance
	if got := issueToolNativeTaskGuidance(quoted, runtimeConfigUpdated{runtimeConnectionConfig: quoted}, nativeErr, issueToolConversationGuidance, true, false); got != issueToolConversationGuidance {
		t.Fatalf("profile containing current role rejected: %q", got)
	}
}

func TestAddWorkflowGuidancePreservesProfileContainingCurrentRole(t *testing.T) {
	profile := "Purpose: quote the current rule without replacing it: " + issueToolConversationGuidance
	want := issueToolConversationGuidance + "\n\n" + profile
	if got := addWorkflowGuidance(previousIssueToolConversationGuidanceV2+"\n\n"+profile, issueToolConversationGuidance); got != want {
		t.Fatalf("profile containing current role was not preserved: %q", got)
	}
}
