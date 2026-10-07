package main

import (
	"context"
	"testing"

	"github.com/crazy-goat/tyci-agent/agent"
	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/tools"
)

// #304: a child agent (subagent, flow role) gets in-loop compaction, the
// model window and its limits; an unset limit falls back to config.json.
func TestAgentRunnerRun_ChildCompactLimits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := agent.SaveTyciConfig(agent.TyciConfig{CompactSoftLimit: 100000, CompactHardLimit: 150000, AutoCompactPercent: -1}); err != nil {
		t.Fatal(err)
	}
	ctx := connector.WithModelClient(context.Background(), connectortest.Text("ok"))
	jobID := "job-compact-limits-1"
	ctx = context.WithValue(ctx, tools.JobIDCtxKey{}, jobID)
	if _, err := (&agentRunner{}).run(ctx, "task", "", "", tools.SubagentOptions{HardLimit: 50000}); err != nil {
		t.Fatal(err)
	}
	resumableMu.Lock()
	entry, ok := resumable[jobID]
	resumableMu.Unlock()
	if !ok {
		t.Fatal("no resumable entry")
	}
	cfg := entry.cfg
	if !cfg.InLoopCompaction || cfg.ContextLimitFor == nil {
		t.Fatalf("InLoopCompaction = %v, ContextLimitFor set = %v", cfg.InLoopCompaction, cfg.ContextLimitFor != nil)
	}
	if cfg.SoftLimit != 100000 || cfg.HardLimit != 50000 {
		t.Fatalf("limits = %d, %d, want 100000 (config.json), 50000 (option)", cfg.SoftLimit, cfg.HardLimit)
	}
	if cfg.AutoCompactPercent != 0 {
		t.Fatalf("AutoCompactPercent = %d, want 0 (a hard limit is set)", cfg.AutoCompactPercent)
	}
}

// #304: with no hard limit, the legacy auto_compact_percent from config.json
// reaches the child config, so -1 disables the child's auto-compaction.
func TestAgentRunnerRun_ChildLegacyAutoCompactPercent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := agent.SaveTyciConfig(agent.TyciConfig{AutoCompactPercent: -1}); err != nil {
		t.Fatal(err)
	}
	ctx := connector.WithModelClient(context.Background(), connectortest.Text("ok"))
	jobID := "job-compact-limits-legacy"
	ctx = context.WithValue(ctx, tools.JobIDCtxKey{}, jobID)
	if _, err := (&agentRunner{}).run(ctx, "task", "", "", tools.SubagentOptions{}); err != nil {
		t.Fatal(err)
	}
	resumableMu.Lock()
	entry, ok := resumable[jobID]
	resumableMu.Unlock()
	if !ok {
		t.Fatal("no resumable entry")
	}
	if entry.cfg.AutoCompactPercent != -1 {
		t.Fatalf("AutoCompactPercent = %d, want -1 (config.json)", entry.cfg.AutoCompactPercent)
	}
}
