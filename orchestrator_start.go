package main

import (
	"context"
	"fmt"

	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/orchestrator"
)

// defaultWorkers is the worker limit until the config key exists.
const defaultWorkers = 3

// startOrchestrator starts one orchestrator whose notices and plan go to post.
// It returns nil, and starts nothing, on a resumed session.
func startOrchestrator(ctx context.Context, resumed bool, cfg orchestrator.Config, f forge.Forge, r orchestrator.Runner, post func(string)) *orchestrator.Orchestrator {
	if resumed {
		return nil
	}
	o := orchestrator.New(cfg, f, r, orchestrator.Hooks{
		Notify:    post,
		PlanReady: func(p orchestrator.PlanReady) { post(p.Text()) },
	})
	o.Start(ctx)
	return o
}

// newForge builds the forge of the config. Only "github" exists.
func newForge(c flowconfig.Forge) (forge.Forge, error) {
	if c.Kind != "" && c.Kind != "github" {
		return nil, fmt.Errorf("forge.kind: unknown kind %q", c.Kind)
	}
	return forge.NewGitHub(c.Repo)
}

// startTUIOrchestrator wires the config, forge and flow manager. Every failure
// posts one line and the chat keeps working.
func startTUIOrchestrator(ctx context.Context, resumed bool, post func(string)) *orchestrator.Orchestrator {
	if resumed {
		return nil
	}
	info, err := flow.DetectRepo()
	if err != nil {
		post(orchestrator.FormatSpecial(orchestrator.ForgeError, err.Error()))
		return nil
	}
	fc, err := flowconfig.Load(info.Home, info.Root, info.Trusted)
	if err != nil {
		post("orchestrator config: " + err.Error())
		return nil
	}
	f, err := newForge(fc.Forge)
	if err != nil {
		post(orchestrator.FormatSpecial(orchestrator.ForgeError, err.Error()))
		return nil
	}
	return startOrchestrator(ctx, false, orchestrator.Config{Workers: defaultWorkers}, f, orchestrator.NewRunner(workflowManager), post)
}
