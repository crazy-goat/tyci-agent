package tools

import (
	"context"
	"fmt"

	"github.com/decodo/tyci-agent/agent"
	"github.com/decodo/tyci-agent/display"
	"github.com/decodo/tyci-agent/providers"
)

type SubagentTool struct{}

func (t *SubagentTool) Name() string {
	return "subagent"
}

func (t *SubagentTool) Run(ctx context.Context, input map[string]any) ToolResult {
	task, ok := input["task"].(string)
	if !ok || task == "" {
		return ToolResult{Type: "result", Success: false, Error: "task required"}
	}

	model := ""
	if m, ok := input["model"].(string); ok && m != "" {
		model = m
	}

	provider := GetProvider()
	if provider == nil {
		return ToolResult{Type: "result", Success: false, Error: "no LLM provider available (start with --model)"}
	}

	modelName := model
	if modelName == "" {
		modelName = GetCurrentModel()
		if modelName == "" {
			return ToolResult{Type: "result", Success: false, Error: "no model specified and no default model set"}
		}
	}

	var prov providers.Provider
	var mName string
	if p, m, ok := providers.FindModel(modelName); ok {
		prov = p
		mName = m
	} else {
		prov = provider
		mName = modelName
	}

	_ = prov
	_ = mName
	_ = providers.BuildSystemPrompt()

	d := display.NewSilent()
	msgs := []providers.Message{{Role: "user", Content: task}}
	if err := agent.Run(ctx, provider, d, &msgs, agent.Config{Model: mName, MaxRetries: 1, Debug: false}); err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("subagent error: %v", err)}
	}

	text := d.Text2()
	if text == "" {
		return ToolResult{Type: "result", Success: false, Error: "subagent returned no text"}
	}
	return ToolResult{Type: "result", Success: true, Content: text}
}
