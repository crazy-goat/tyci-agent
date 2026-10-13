package flow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/agent"
	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/tools"
)

func TestSpawnWithLimits_RoundsCapPassedAndMapped(t *testing.T) {
	var gotRounds int
	r := &SubagentRunner{Spawn: func(_ context.Context, s tools.TaskSpec) (string, string, error) {
		gotRounds = s.MaxRounds
		return "", "", tools.ErrSubagentTruncated
	}}
	_, _, err := r.spawnWithLimits(context.Background(), tools.TaskSpec{}, flowconfig.Limits{Rounds: 7})
	var lim *agent.ErrLimit
	if !errors.As(err, &lim) || lim.Kind != "rounds" {
		t.Fatalf("err = %v, want ErrLimit rounds", err)
	}
	if gotRounds != 7 {
		t.Fatalf("MaxRounds = %d, want 7", gotRounds)
	}
}

func TestSpawnWithLimits_NoRoundsLimitKeepsError(t *testing.T) {
	r := &SubagentRunner{Spawn: func(context.Context, tools.TaskSpec) (string, string, error) {
		return "", "", tools.ErrSubagentTruncated
	}}
	_, _, err := r.spawnWithLimits(context.Background(), tools.TaskSpec{}, flowconfig.Limits{})
	if !errors.Is(err, tools.ErrSubagentTruncated) {
		t.Fatalf("err = %v, want ErrSubagentTruncated", err)
	}
}

func TestSpawnWithLimits_TimeDeadlineOnRun(t *testing.T) {
	var hadDeadline bool
	var left time.Duration
	r := &SubagentRunner{Spawn: func(ctx context.Context, _ tools.TaskSpec) (string, string, error) {
		dl, ok := ctx.Deadline()
		hadDeadline = ok
		left = time.Until(dl)
		return "", "", tools.ErrSubagentTimedOut
	}}
	_, _, err := r.spawnWithLimits(context.Background(), tools.TaskSpec{}, flowconfig.Limits{TimeMin: 5})
	var lim *agent.ErrLimit
	if !errors.As(err, &lim) || lim.Kind != "time" {
		t.Fatalf("err = %v, want ErrLimit time", err)
	}
	if !hadDeadline || left <= 4*time.Minute || left > 5*time.Minute {
		t.Fatalf("deadline ok=%v left=%v", hadDeadline, left)
	}
}

func TestSpawnWithLimits_ParentCancelIsNotLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &SubagentRunner{Spawn: func(context.Context, tools.TaskSpec) (string, string, error) {
		return "", "", context.Canceled
	}}
	_, _, err := r.spawnWithLimits(ctx, tools.TaskSpec{}, flowconfig.Limits{TimeMin: 5})
	var lim *agent.ErrLimit
	if errors.As(err, &lim) {
		t.Fatalf("cancel mapped to limit %v", lim)
	}
}

func TestSpawnWithLimits_ToolCallsCapPassedAndMapped(t *testing.T) {
	var gotCalls int
	r := &SubagentRunner{Spawn: func(_ context.Context, s tools.TaskSpec) (string, string, error) {
		gotCalls = s.MaxToolCalls
		return "", "", tools.ErrSubagentToolCalls
	}}
	_, _, err := r.spawnWithLimits(context.Background(), tools.TaskSpec{}, flowconfig.Limits{ToolCalls: 2})
	var lim *agent.ErrLimit
	if !errors.As(err, &lim) || lim.Kind != "tool_calls" {
		t.Fatalf("err = %v, want ErrLimit tool_calls", err)
	}
	if gotCalls != 2 {
		t.Fatalf("MaxToolCalls = %d, want 2", gotCalls)
	}
}

func TestSpawnWithLimits_ToolCallsZeroIsUnlimited(t *testing.T) {
	var gotCalls int
	r := &SubagentRunner{Spawn: func(_ context.Context, s tools.TaskSpec) (string, string, error) {
		gotCalls = s.MaxToolCalls
		return "", "", tools.ErrSubagentToolCalls
	}}
	_, _, err := r.spawnWithLimits(context.Background(), tools.TaskSpec{}, flowconfig.Limits{Rounds: 3})
	var lim *agent.ErrLimit
	if errors.As(err, &lim) {
		t.Fatalf("tool_calls 0 mapped to limit %v", lim)
	}
	if gotCalls != 0 {
		t.Fatalf("MaxToolCalls = %d, want 0", gotCalls)
	}
}
