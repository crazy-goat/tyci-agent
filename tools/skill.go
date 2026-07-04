package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/decodo/tyci/internal/skills"
)

type SkillsTool struct{}

func (t *SkillsTool) Name() string {
	return "skills"
}

func (t *SkillsTool) Run(ctx context.Context, input map[string]any) ToolResult {
	name, hasName := input["name"].(string)

	// If name provided, load the skill
	if hasName && name != "" {
		dir := skills.SkillsDir()
		skill, err := skills.LoadSkill(dir, name)
		if err != nil {
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}

		var b strings.Builder
		fmt.Fprintf(&b, "# Skill: %s\n", skill.Name)
		if skill.Description != "" {
			fmt.Fprintf(&b, "\nDescription: %s\n", skill.Description)
		}
		fmt.Fprintf(&b, "\n---\n\n%s", skill.Content)

		return ToolResult{Type: "result", Success: true, Content: b.String()}
	}

	// No name — list available skills
	dir := skills.SkillsDir()
	names, err := skills.ListSkills(dir)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	if len(names) == 0 {
		return ToolResult{Type: "result", Success: true, Content: "No skills available. Create skills in " + dir + "/<name>/SKILL.md"}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Available skills (%d):\n", len(names))
	for _, name := range names {
		skill, err := skills.LoadSkill(dir, name)
		if err != nil {
			fmt.Fprintf(&b, "  - %s\n", name)
			continue
		}
		if skill.Description != "" {
			fmt.Fprintf(&b, "  - %s: %s\n", name, skill.Description)
		} else {
			fmt.Fprintf(&b, "  - %s\n", name)
		}
	}

	return ToolResult{Type: "result", Success: true, Content: b.String()}
}
