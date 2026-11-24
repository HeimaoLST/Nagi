package pi

import "context"

const (
	ToolPresetNone      = "none"
	ToolPresetReadonly  = "readonly"
	ToolPresetWorkspace = "workspace"
	ToolPresetCustom    = "custom"
)

func ResolveCodingTools(codingTools []string, preset string) []string {
	if codingTools != nil {
		return codingTools
	}
	switch preset {
	case ToolPresetNone:
		return []string{}
	case ToolPresetReadonly:
		return []string{"find", "grep", "ls", "read"}
	case ToolPresetWorkspace:
		return []string{"bash", "edit", "find", "grep", "ls", "read", "write"}
	default:
		return nil
	}
}

type ProductMountedSkill struct {
	Source         string `json:"source"`
	SkillID        string `json:"skillId"`
	ExpectedDigest string `json:"expectedDigest,omitempty"`
}

type ProductSkillKey struct {
	Source  string `json:"source"`
	BotID   string `json:"botId,omitempty"`
	SkillID string `json:"skillId"`
}

type ProductSkillSummary struct {
	Source      string `json:"source"`
	SkillID     string `json:"skillId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SHA256      string `json:"sha256"`
}

type ProductSkillDetail struct {
	ProductSkillSummary
	Content  string `json:"content"`
	Markdown string `json:"markdown"`
}

type ProductMountedSkillView struct {
	ProductMountedSkill
	Status string               `json:"status"`
	Skill  *ProductSkillSummary `json:"skill,omitempty"`
}

type ProductSkillList struct {
	Global              []ProductSkillSummary     `json:"global"`
	Workspace           []ProductSkillSummary     `json:"workspace"`
	Local               []ProductSkillSummary     `json:"local"`
	Private             []ProductSkillSummary     `json:"private"`
	Mounted             []ProductMountedSkillView `json:"mounted"`
	DiscoverLocalSkills bool                      `json:"discoverLocalSkills"`
}

type ProductSkillSaveParams struct {
	ProductSkillKey
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	OperationID string `json:"operationId"`
}

type ProductSkillDeleteParams struct {
	ProductSkillKey
	OperationID string `json:"operationId"`
}

type ProductSkillPromoteParams struct {
	BotID         string `json:"botId"`
	SkillID       string `json:"skillId"`
	TargetSkillID string `json:"targetSkillId,omitempty"`
	OperationID   string `json:"operationId"`
}

type ProductSkillPreferences struct {
	DiscoverLocalSkills bool   `json:"discoverLocalSkills"`
	OperationID         string `json:"operationId"`
}

type ProductSkillChange struct {
	ProductSkillKey
	Change string `json:"change"`
}

func (event ProductSkillChange) valid() bool {
	switch event.Change {
	case "saved", "deleted":
		return event.SkillID != "" && (event.Source == "global" || event.Source == "private" && event.BotID != "")
	case "promoted":
		return event.Source == "global" && event.SkillID != "" && event.BotID != ""
	case "preferences":
		return event.Source == "global" && event.SkillID == "" && event.BotID == ""
	default:
		return false
	}
}

func (p *ProductClient) ListSkills(ctx context.Context, botID string) (ProductSkillList, error) {
	return productCall[ProductSkillList](ctx, p, "skill.list", struct {
		BotID string `json:"botId,omitempty"`
	}{BotID: botID})
}

func (p *ProductClient) GetSkill(ctx context.Context, key ProductSkillKey) (ProductSkillDetail, error) {
	reply, err := productCall[struct {
		Skill ProductSkillDetail `json:"skill"`
	}](ctx, p, "skill.get", key)
	return reply.Skill, err
}

func (p *ProductClient) SaveSkill(ctx context.Context, params ProductSkillSaveParams) (ProductSkillChange, error) {
	return productCall[ProductSkillChange](ctx, p, "skill.save", params)
}

func (p *ProductClient) DeleteSkill(ctx context.Context, params ProductSkillDeleteParams) (ProductSkillChange, error) {
	return productCall[ProductSkillChange](ctx, p, "skill.delete", params)
}

func (p *ProductClient) PromoteSkill(ctx context.Context, params ProductSkillPromoteParams) (ProductSkillChange, error) {
	return productCall[ProductSkillChange](ctx, p, "skill.promoteGlobal", params)
}

func (p *ProductClient) SetSkillPreferences(ctx context.Context, params ProductSkillPreferences) (ProductSkillChange, error) {
	return productCall[ProductSkillChange](ctx, p, "skill.setPreferences", params)
}
