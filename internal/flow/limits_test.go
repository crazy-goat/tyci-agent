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
