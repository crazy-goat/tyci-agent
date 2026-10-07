package flow

import (
	"context"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/crazy-goat/tyci-agent/tools"
)

type statsAgents struct{}

func (statsAgents) Run(_ context.Context, _, _ string, rc RunContext) (string, string, error) {
	*rc.Stats = StepStats{Model: "p/m", Input: 100, Output: 20, CostUSD: 0.5, Turns: 3}
	return "done", "s", nil
}

func TestRunnerStoresAgentStats(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "b", States: map[string]State{
		"b":   {Agent: "worker", On: map[string]string{"done": "end"}},
		"end": {End: true},
	}}
	st := newRun("b")
	r := &Runner{WF: wf, Agents: statsAgents{}, Store: &memStore{}}
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	got := st.History[0].Stats
	if got == nil || got.Tokens() != 120 || got.Turns != 3 {
		t.Fatalf("stats = %+v", got)
	}
}

func TestTotalsPerRole(t *testing.T) {
	t0 := time.Now()
	st := &RunState{History: []Step{
		{Role: "worker", StartedAt: t0, EndedAt: t0.Add(10 * time.Second), Stats: &StepStats{Input: 10, CostUSD: 1}},
		{Kind: "check", StartedAt: t0, EndedAt: t0.Add(5 * time.Second)},
		{Role: "worker", StartedAt: t0, EndedAt: t0.Add(20 * time.Second), Stats: &StepStats{Output: 5, CostUSD: 2}},
	}}
	all, roles := st.Totals()
	if all.Tokens != 15 || all.CostUSD != 3 || all.Seconds != 35 {
		t.Fatalf("all = %+v", all)
	}
	if w := roles["worker"]; w.Tokens != 15 || w.Seconds != 30 {
		t.Fatalf("worker = %+v", w)
	}
	if _, ok := roles[""]; ok {
		t.Fatal("check steps must not make a role")
	}
}

func TestSubagentRunnerReportsStats(t *testing.T) {
	cfg := &flowconfig.Config{DefaultModel: "p/m", Roles: map[string]flowconfig.Role{"oracle": {Prompt: "x"}}}
	spawn := func(_ context.Context, s tools.TaskSpec) (string, string, error) {
		s.OnDone(tools.TaskStats{Usage: stream.Usage{Input: 7, Output: 3}, Turns: 2, ToolCalls: 4})
		return "ok", "id", nil
	}
	var stats StepStats
	r := NewSubagentRunner(cfg, spawn)
	if _, _, err := r.Text(context.Background(), "oracle", "", RunContext{Stats: &stats}); err != nil {
		t.Fatal(err)
	}
	if stats.Model != "p/m" || stats.Input != 7 || stats.Output != 3 || stats.Turns != 2 || stats.ToolCalls != 4 {
		t.Fatalf("stats = %+v", stats)
	}
}
