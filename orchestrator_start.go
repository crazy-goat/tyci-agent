package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/crazy-goat/tyci-agent/display"
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/orchestrator"
	"github.com/crazy-goat/tyci-agent/providers"
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
	oc, err := loadOrchestratorConfig(info)
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

func loadOrchestratorConfig(info flow.RepoInfo) (orchestrator.Config, error) {
	projectCfg := ""
	if info.Trusted {
		projectCfg = filepath.Join(info.Root, ".tyci", "config.json")
	}
	return orchestrator.LoadConfig(filepath.Join(info.Home, ".tyci", "config.json"), projectCfg)
}

// resumeWorkflowRuns is the start-up resume of runs that a crashed or killed
// tyci left running. At most the orchestrator worker limit is resumed. It runs
// before the greeting, so the orchestrator adopts the resumed runs.
func resumeWorkflowRuns() {
	limit := 0
	if info, err := flow.DetectRepo(); err == nil {
		if oc, err := loadOrchestratorConfig(info); err == nil {
			limit = oc.Workers
		}
	}
	workflowManager.ResumeAll(limit)
}

// orchestratorSystemPrompt is the TUI chat prompt. The worker limit comes from
// the orchestrator config; outside a repository or on a config error it is the
// default, 3 (startTUIOrchestrator reports the error to the user).
func orchestratorSystemPrompt() string {
	workers := 3
	if info, err := flow.DetectRepo(); err == nil {
		if oc, err := loadOrchestratorConfig(info); err == nil {
			workers = oc.Workers
		}
	}
	return providers.BuildOrchestratorSystemPrompt(workers)
}

// runRows converts saved runs to sidebar rows with the last three steps.
func runRows(views []flow.RunView) []display.TuiRunRow {
	rows := make([]display.TuiRunRow, 0, len(views))
	for _, v := range views {
		st := v.State
		r := display.TuiRunRow{ID: st.Run, Issue: st.Issue, Status: st.Status, State: st.Current, Role: v.Role, Since: st.UpdatedAt}
		h := st.History
		if len(h) > 3 {
			h = h[len(h)-3:]
		}
		for _, s := range h {
			r.Steps = append(r.Steps, s.State+" -> "+s.Key)
		}
		rows = append(rows, r)
	}
	return rows
}
