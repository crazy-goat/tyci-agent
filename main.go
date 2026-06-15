package main

import (
	"context"
	"fmt"
	"os"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/tools"
)

// agentRunner implements tools.SubAgentRunner by wrapping agent.Run.
type agentRunner struct{}

func (r *agentRunner) RunTask(ctx context.Context, task string, model string, temperature float64) (string, error) {
	// Resolve provider and model
	prov, mName, ok := providers.FindModel(model)
	if !ok {
		// Fallback to context values
		prov = providers.ProviderFromContext(ctx)
		mName = providers.ModelFromContext(ctx)
	}
	if prov == nil {
		return "", fmt.Errorf("no provider available for model %q", model)
	}
	if mName == "" {
		return "", fmt.Errorf("no model specified")
	}

	// Create collector to capture output
	c := &collector{}
	msgs := []providers.RichMessage{
		{
			Role:    "user",
			Content: []providers.ContentBlock{{Type: "text", Text: task}},
		},
	}

	cfg := agent.Config{
		Model:         mName,
		System:        providers.BuildSystemPrompt(),
		MaxRetries:    1,
		MaxIterations: 10,
		Debug:         false,
		Tools:         &subagentToolRunner{},
		Schema:        tools.GetSubagentToolsSchemaJSON(),
	}

	_, err := agent.Run(ctx, prov, c, &msgs, cfg)
	if err != nil {
		return "", err
	}
	return c.text.String(), nil
}

func (r *agentRunner) RunTaskWithSystem(ctx context.Context, task string, model string, temperature float64, system string) (string, error) {
	// Resolve provider and model
	prov, mName, ok := providers.FindModel(model)
	if !ok {
		// Fallback to context values
		prov = providers.ProviderFromContext(ctx)
		mName = providers.ModelFromContext(ctx)
	}
	if prov == nil {
		return "", fmt.Errorf("no provider available for model %q", model)
	}
	if mName == "" {
		return "", fmt.Errorf("no model specified")
	}

	// Create collector to capture output
	c := &collector{}
	msgs := []providers.RichMessage{
		{
			Role:    "user",
			Content: []providers.ContentBlock{{Type: "text", Text: task}},
		},
	}

	cfg := agent.Config{
		Model:         mName,
		System:        system,
		MaxRetries:    1,
		MaxIterations: 10,
		Debug:         false,
		Tools:         &subagentToolRunner{},
		Schema:        tools.GetSubagentToolsSchemaJSON(),
	}

	_, err := agent.Run(ctx, prov, c, &msgs, cfg)
	if err != nil {
		return "", err
	}
	return c.text.String(), nil
}

// subagentToolRunner wraps the global tool registry so subagents can use tools.
type subagentToolRunner struct{}

func (r *subagentToolRunner) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	if name == "subagent" {
		return "", fmt.Errorf("subagent tool is not available to subagents (recursion denied)")
	}
	res := tools.RunTool(ctx, name, args)
	if res.Success {
		return res.Content, nil
	}
	return res.Content, fmt.Errorf("%s", res.Error)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
