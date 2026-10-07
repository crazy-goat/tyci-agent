package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/orchestrator"
)

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
	projectCfg := ""
	if info.Trusted {
		projectCfg = filepath.Join(info.Root, ".tyci", "config.json")
	}
	oc, err := orchestrator.LoadConfig(filepath.Join(info.Home, ".tyci", "config.json"), projectCfg)
	if err != nil {
		post("orchestrator config: " + err.Error())
		return nil
	}
	f, err := newForge(fc.Forge)
	if err != nil {
		post(orchestrator.FormatSpecial(orchestrator.ForgeError, err.Error()))
		return nil
	}
	return startOrchestrator(ctx, false, oc, f, orchestrator.NewRunner(workflowManager), post)
}
