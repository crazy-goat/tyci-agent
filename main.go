package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/tools"
)

// agentRunner implements tools.SubAgentRunner by wrapping agent.Run.
type agentRunner struct{}

// resolveProviderModel picks the provider and bare model name for a subagent.
//
// An explicit "provider/model" override is resolved via the registry. Otherwise
// the subagent inherits the parent's provider from context — which is already
// configured with a valid API key — instead of re-guessing via FindModel, whose
// bare-name lookup iterates the provider map in random order and can land on a
// different (unconfigured) provider that happens to list the same model.
func resolveProviderModel(ctx context.Context, model string) (providers.Provider, string, error) {
	if strings.Contains(model, "/") {
		if prov, mName, ok := providers.FindModel(model); ok {
			return prov, mName, nil
		}
		return nil, "", fmt.Errorf("no provider available for model %q", model)
	}

	prov := providers.ProviderFromContext(ctx)
	mName := model
	if mName == "" {
		mName = providers.ModelFromContext(ctx)
	}
	if prov == nil {
		// No parent provider in context (e.g. tests) — fall back to lookup.
		if p, m, ok := providers.FindModel(mName); ok {
			return p, m, nil
		}
		return nil, "", fmt.Errorf("no provider available for model %q", model)
	}
	if mName == "" {
		return nil, "", fmt.Errorf("no model specified")
	}
	return prov, mName, nil
}

func (r *agentRunner) RunTask(ctx context.Context, task string, model string, temperature float64) (string, error) {
	prov, mName, err := resolveProviderModel(ctx, model)
	if err != nil {
		return "", err
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

	_, err = agent.Run(ctx, prov, c, &msgs, cfg)
	if err != nil {
		return "", err
	}
	return c.text.String(), nil
}

func (r *agentRunner) RunTaskWithSystem(ctx context.Context, task string, model string, temperature float64, system string) (string, error) {
	prov, mName, err := resolveProviderModel(ctx, model)
	if err != nil {
		return "", err
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

	_, err = agent.Run(ctx, prov, c, &msgs, cfg)
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
	// Register the subagent runner so the "subagent" tool (advertised in the
	// tool schema) is actually executable. Without this, RunTool returns
	// "unknown tool: subagent".
	tools.SetSubAgentRunner(&agentRunner{})

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
