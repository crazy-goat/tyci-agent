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

	// If name provided, load the skill — global then project-local
	// (./.tyci/skills), project-local winning on a name collision, the same
	// precedence agents/ already uses.
	if hasName && name != "" {
		skill, err := skills.GetSkillMerged("", name)
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

	// No name — list available skills, global union project-local.
	found, err := skills.ListSkillsMerged("")
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	if len(found) == 0 {
		return ToolResult{Type: "result", Success: true, Content: "No skills available. Create skills in " + skills.SkillsDir() + "/<name>/SKILL.md or ./.tyci/skills/<name>/SKILL.md"}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Available skills (%d):\n", len(found))
	for _, skill := range found {
		if skill.Description != "" {
			fmt.Fprintf(&b, "  - %s: %s\n", skill.Name, skill.Description)
		} else {
			fmt.Fprintf(&b, "  - %s\n", skill.Name)
		}
	}

	return ToolResult{Type: "result", Success: true, Content: b.String()}
}
