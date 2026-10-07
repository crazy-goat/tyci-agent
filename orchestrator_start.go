package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crazy-goat/tyci-agent/agent"
	"github.com/crazy-goat/tyci-agent/display"
	"github.com/crazy-goat/tyci-agent/internal/debug"
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/orchestrator"
	"github.com/crazy-goat/tyci-agent/internal/redact"
	"github.com/crazy-goat/tyci-agent/internal/runlog"
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

// startRunLogHousekeeping registers the provider keys with the redactor and
// removes old finished runs, now and every 24 hours until ctx ends. It runs
// before any agent starts. A config error leaves the runs alone.
func startRunLogHousekeeping(ctx context.Context) {
	for _, p := range providers.ListProviders() {
		redact.Add(providers.DefaultAuth().Key(p.Name()))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	fc, err := flowconfig.Load(home, "", false)
	if err != nil {
		if l := debug.FromContext(ctx); l != nil {
			fmt.Fprintf(l, "runlog prune: config: %v\n", err)
		}
		return
	}
	days, dir := fc.RetentionDays(), flow.RunsDir(home)
	prune := func() {
		n, err := runlog.Prune(dir, days, time.Now())
		if l := debug.FromContext(ctx); l != nil {
			if err != nil {
				fmt.Fprintf(l, "runlog prune: %v\n", err)
			} else {
				fmt.Fprintf(l, "runlog prune: removed %d runs\n", n)
			}
		}
	}
	prune()
	go func() {
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				prune()
			}
		}
	}()
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

// orchestratorChatConfig turns the chat config into the orchestrator config:
// the orchestrator prompt.
func orchestratorChatConfig(cfg agent.Config) agent.Config {
	cfg.System = orchestratorSystemPrompt()
	return cfg
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
			r.Steps = append(r.Steps, stepLine(s))
		}
		if total, _ := st.Totals(); total.Tokens > 0 {
			r.Totals = fmt.Sprintf("%s tok · $%.2f · %s", fmtTok(total.Tokens), total.CostUSD, total.Duration.Round(time.Second))
		}
		rows = append(rows, r)
	}
	return rows
}

// stepLine is one Runs tab line: "state -> key", then role, model, tokens, cost
// and time when known. A check step shows only its duration.
func stepLine(s flow.Step) string {
	line := s.State + " -> " + s.Key
	var parts []string
	if s.Role != "" {
		parts = append(parts, s.Role)
	}
	if s.Stats != nil {
		model := s.Stats.Model
		if _, name, ok := strings.Cut(model, "/"); ok {
			model = name
		}
		if s.Stats.Effort != "" {
			model += " (" + s.Stats.Effort + ")"
		}
		parts = append(parts, model, fmtTok(s.Stats.Tokens())+" tok", fmt.Sprintf("$%.2f", s.Stats.CostUSD))
	}
	if !s.EndedAt.IsZero() && !s.StartedAt.IsZero() {
		parts = append(parts, s.EndedAt.Sub(s.StartedAt).Round(time.Second).String())
	}
	if len(parts) == 0 {
		return line
	}
	return line + ": " + strings.Join(parts, " · ")
}

func fmtTok(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}
