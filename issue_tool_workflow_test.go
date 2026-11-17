package main

import (
	"strings"
	"testing"
)

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

func TestCombineWorkflowPromptPreservesProfileContainingCurrentRole(t *testing.T) {
	profile := "Purpose: quote the current rule without replacing it: " + issueToolConversationGuidance
	want := issueToolConversationGuidance + "\n\n" + profile
	if got := combineWorkflowPrompt(issueToolConversationGuidance, profile, true); got != want {
		t.Fatalf("profile containing current role was not preserved: %q", got)
	}
	if got := combineWorkflowPrompt(issueToolConversationGuidance, profile, false); got != profile {
		t.Fatalf("disabled workflow changed the profile: %q", got)
	}
}
