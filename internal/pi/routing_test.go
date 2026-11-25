package pi

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestResolveCodingToolsPresetsAndExplicitOverrides(t *testing.T) {
	if got := ResolveCodingTools(nil, ToolPresetNone); got == nil || len(got) != 0 {
		t.Fatalf("ToolPresetNone = %#v, want non-nil empty slice", got)
	}
	if got := ResolveCodingTools(nil, ToolPresetReadonly); !slices.Equal(got, []string{"find", "grep", "ls", "read"}) {
		t.Fatalf("ToolPresetReadonly = %v", got)
	}
	if got := ResolveCodingTools(nil, ToolPresetWorkspace); !slices.Equal(got, []string{"bash", "edit", "find", "grep", "ls", "read", "write"}) {
		t.Fatalf("ToolPresetWorkspace = %v", got)
	}
	if got := ResolveCodingTools(nil, ToolPresetCustom); got != nil {
		t.Fatalf("ToolPresetCustom = %#v, want nil", got)
	}
	if got := ResolveCodingTools([]string{}, ToolPresetWorkspace); got == nil || len(got) != 0 {
		t.Fatalf("explicit empty override = %#v, want empty slice", got)
	}
	if got := ResolveCodingTools([]string{"read"}, ToolPresetNone); !slices.Equal(got, []string{"read"}) {
		t.Fatalf("explicit override = %v", got)
	}
}

func TestProductChangeEventValidators(t *testing.T) {
	rev1, rev2 := uint64(1), uint64(2)

	for _, change := range []string{"saved", "deleted", "applied", "revoked", "policy"} {
		ev := ProductProviderChange{
			ProductConnectionRevisions: ProductConnectionRevisions{SavedRevision: &rev2, EffectiveRevision: &rev1},
			Change:                     change,
		}
		if !ev.valid() {
			t.Fatalf("ProductProviderChange(%q) unexpectedly invalid", change)
		}
	}
	if (ProductProviderChange{Change: "saved"}).valid() {
		t.Fatal("ProductProviderChange without SavedRevision must be invalid")
	}
	if (ProductProviderChange{
		ProductConnectionRevisions: ProductConnectionRevisions{SavedRevision: &rev1, EffectiveRevision: &rev2},
		Change:                     "saved",
	}).valid() {
		t.Fatal("ProductProviderChange with EffectiveRevision > SavedRevision must be invalid")
	}
	if (ProductProviderChange{
		ProductConnectionRevisions: ProductConnectionRevisions{SavedRevision: &rev1},
		Change:                     "unknown",
	}).valid() {
		t.Fatal("ProductProviderChange with unknown change must be invalid")
	}

	if !(ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "global", SkillID: "guide"}, Change: "saved"}).valid() {
		t.Fatal("global skill saved change should be valid")
	}
	if (ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "private", SkillID: "guide"}, Change: "saved"}).valid() {
		t.Fatal("private skill saved change without BotID must be invalid")
	}
	if !(ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "global"}, Change: "preferences"}).valid() {
		t.Fatal("preferences skill change should be valid")
	}
	if !(ProductSkillChange{ProductSkillKey: ProductSkillKey{Source: "local", BotID: "b1", SkillID: "guide"}, Change: "approved"}).valid() {
		t.Fatal("local skill approved change with BotID should be valid")
	}

	if !(ProductMcpChange{ServerID: "mcp-1", Change: "tested"}).valid() {
		t.Fatal("mcp tested change should be valid")
	}
	if (ProductMcpChange{ServerID: "", Change: "saved"}).valid() || (ProductMcpChange{ServerID: "mcp-1", Change: "other"}).valid() {
		t.Fatal("invalid ProductMcpChange accepted")
	}
}

func TestProductRoutingOverridesPatchPreservesExplicitNullClear(t *testing.T) {
	patch := ProductRoutingOverridesPatch{
		"dialogue": nil,
		"coding":   {Mode: "model", CanonicalModelID: "openai/gpt-5"},
	}
	raw, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded["dialogue"]) != "null" {
		t.Fatalf("cleared override serialized as %q, want null", string(decoded["dialogue"]))
	}
	if _, hasReasoning := decoded["reasoning"]; hasReasoning {
		t.Fatalf("omitted role unexpectedly encoded: %s", string(raw))
	}
}
