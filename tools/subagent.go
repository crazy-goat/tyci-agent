package tools

import (
	"context"
	"fmt"

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

	// Resolve model
	modelName := model
	if modelName == "" {
		modelName = GetCurrentModel()
		if modelName == "" {
			return ToolResult{Type: "result", Success: false, Error: "no model specified and no default model set"}
		}
	}

	// If model contains "/" it's provider/model format
	var prov providers.Provider
	var mName string
	if p, m, ok := providers.FindModel(modelName); ok {
		prov = p
		mName = m
	} else {
		// Try as just model name within same provider
		prov = provider
		mName = modelName
	}

	system := providers.BuildSystemPrompt()

	messages := []providers.Message{
		{Role: "user", Content: task},
	}

	result, err := prov.SendWithMessages(ctx, mName, task, system, messages, false)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("subagent error: %v", err)}
	}

	if result == nil {
		return ToolResult{Type: "result", Success: false, Error: "subagent returned nil result"}
	}

	return ToolResult{Type: "result", Success: true, Content: result.Text}
}
