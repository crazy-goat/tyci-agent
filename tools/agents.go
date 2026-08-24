package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/decodo/tyci/internal/agentdefs"
)

// AgentsTool lets the model discover named agents on demand — the "agent"
// parameter of the "subagent" tool. Without this, the only place named
// agents are ever listed for the model is a one-time injection into the
// top-level agent's system prompt at session start (see
// providers.BuildSystemPrompt); a definition added or edited mid-session is
// invisible until the next session. Mirrors the "skills" tool's shape:
// no "name" lists everything, a "name" loads that one definition's details.
type AgentsTool struct{}

func (t *AgentsTool) Name() string { return "agents" }

func (t *AgentsTool) Run(_ context.Context, input map[string]any) ToolResult {
	name, hasName := input["name"].(string)

	if hasName && name != "" {
		def, ok := agentdefs.Get("", name)
		if !ok {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("agent %q not found (looked in %s and %s)", name, agentdefs.GlobalDir(), agentdefs.ProjectDir(""))}
		}

		var b strings.Builder
		fmt.Fprintf(&b, "# Agent: %s\n", def.Name)
		if def.Description != "" {
			fmt.Fprintf(&b, "\nDescription: %s\n", def.Description)
		}
		if def.Model != "" {
			fmt.Fprintf(&b, "Model: %s\n", def.Model)
		}
		if len(def.Fallback) > 0 {
			fmt.Fprintf(&b, "Fallback models: %s\n", strings.Join(def.Fallback, ", "))
		}
		if def.Tools == nil {
			fmt.Fprintf(&b, "Tools: unrestricted (every tool except subagent)\n")
		} else {
			fmt.Fprintf(&b, "Tools: %s\n", strings.Join(def.Tools, ", "))
		}
		if def.MaxIterations > 0 {
			fmt.Fprintf(&b, "Max iterations: %d (legacy field; ignored for subagents; execution is unlimited)\n", def.MaxIterations)
		}
		if def.Temperature != nil {
			fmt.Fprintf(&b, "Temperature: %g\n", *def.Temperature)
		}
		fmt.Fprintf(&b, "\n---\n\n%s", def.SystemPrompt)

		return ToolResult{Type: "result", Success: true, Content: b.String()}
	}

	defs := agentdefs.List("")
	if len(defs) == 0 {
		return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("No named agents available. Define one as a markdown file in %s or %s.", agentdefs.GlobalDir(), agentdefs.ProjectDir(""))}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Available agents for subagent(agent=\"name\") (%d):\n", len(defs))
	for _, def := range defs {
		if def.Description != "" {
			fmt.Fprintf(&b, "  - %s: %s\n", def.Name, def.Description)
		} else {
			fmt.Fprintf(&b, "  - %s\n", def.Name)
		}
	}

	return ToolResult{Type: "result", Success: true, Content: b.String()}
}
