package agent

// Item 15: a subagent that runs for a while without calling report_progress
// must get a periodic, harness-authored nudge asking it to post one — but
// only a subagent (something with cfg.ProgressHeartbeat wired), never the
// main conversation, and never crowding out the real conversation (at most
// once per interval, and never on the same iteration as the last-step
// warning, which forbids tool calls).
//
// These tests exercise agent.Run's injection point directly against a fake
// cfg.ProgressHeartbeat callback — the time-based gating itself (at most one
// nudge per SubagentBackgroundAfterSec) is jobs.Registry's responsibility
// and is pinned by jobs/heartbeat_test.go; Run only needs to know "inject
// when the callback says so, and never otherwise".

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/stream"
)

// countProgressHeartbeatReminders counts how many of the harness-authored
// progress-heartbeat reminders (buildProgressHeartbeatReminder) appear in
// msgs.
func countProgressHeartbeatReminders(msgs []connector.Message) int {
	count := 0
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		for _, c := range m.Content {
			if c.Type == "text" && strings.Contains(c.Text, "posting a status update") {
				count++
			}
		}
	}
	return count
}

// TestRun_InjectsProgressHeartbeat_WhenSignaled pins the basic wiring: when
// cfg.ProgressHeartbeat reports true, Run injects the reminder — asking the
// model to call report_progress — into msgs before the next model call.
func TestRun_InjectsProgressHeartbeat_WhenSignaled(t *testing.T) {
	calls := 0
	heartbeat := func() bool {
		calls++
		return calls == 1 // fire on the very first check only
	}

	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	_, err := Run(context.Background(), alwaysTool(), &silentDisplay{}, &msgs, Config{
		MaxRetries:        1,
		MaxIterations:     3,
		Tools:             newMockToolRunner(),
		ProgressHeartbeat: heartbeat,
	})
	if !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("expected ErrMaxIterations, got %v", err)
	}
	if calls == 0 {
		t.Fatal("expected cfg.ProgressHeartbeat to have been called at least once")
	}
	if got := countProgressHeartbeatReminders(msgs); got != 1 {
		t.Fatalf("expected exactly 1 progress-heartbeat reminder, got %d: %#v", got, msgs)
	}
}

// TestRun_ProgressHeartbeat_NotInjected_WhenCallbackReturnsFalse pins the
// negative case: a child that IS reporting (or simply hasn't gone quiet
// long enough — jobs.Registry's job) must never see this reminder.
func TestRun_ProgressHeartbeat_NotInjected_WhenCallbackReturnsFalse(t *testing.T) {
	heartbeat := func() bool { return false }

	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	_, err := Run(context.Background(), alwaysTool(), &silentDisplay{}, &msgs, Config{
		MaxRetries:        1,
		MaxIterations:     3,
		Tools:             newMockToolRunner(),
		ProgressHeartbeat: heartbeat,
	})
	if !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("expected ErrMaxIterations, got %v", err)
	}
	if got := countProgressHeartbeatReminders(msgs); got != 0 {
		t.Fatalf("expected no progress-heartbeat reminder, got %d: %#v", got, msgs)
	}
}

// TestRun_ProgressHeartbeat_NilCallback_NeverFires pins the main-conversation
// case: cfg.ProgressHeartbeat is left nil for the top-level agent (only a
// subagent's own loop gets it wired, via tools.JobProgressHeartbeatCheck in
// main.go) — Run must tolerate the nil callback and never inject the
// reminder, no matter how many iterations run.
func TestRun_ProgressHeartbeat_NilCallback_NeverFires(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	_, err := Run(context.Background(), alwaysTool(), &silentDisplay{}, &msgs, Config{
		MaxRetries:    1,
		MaxIterations: 3,
		Tools:         newMockToolRunner(),
		// ProgressHeartbeat intentionally left nil.
	})
	if !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("expected ErrMaxIterations, got %v", err)
	}
	if got := countProgressHeartbeatReminders(msgs); got != 0 {
		t.Fatalf("expected no progress-heartbeat reminder with a nil callback, got %d: %#v", got, msgs)
	}
}

// TestRun_ProgressHeartbeat_NeverFiresAlongsideLastStepWarning pins the
// mutual-exclusion the `else` branch in Run enforces: the last-step warning
// explicitly forbids tool calls this turn (there is no next runOnce left to
// see their result), while report_progress IS a tool call — the two must
// never land in the same injected turn, or the harness would be telling the
// model both "call report_progress now" and "do not call any tools now" at
// once.
func TestRun_ProgressHeartbeat_NeverFiresAlongsideLastStepWarning(t *testing.T) {
	// A deadline well inside lastStepDeadlineThreshold so the very first
	// iteration already triggers the last-step warning.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// A model that finishes immediately (no tool call) — same shape as
	// TestRun_InjectsLastStepWarning_OnDeadline in maxiter_test.go.
	p := &connectortest.Fake{ProviderName: "p", ModelName: "m", Turns: [][]stream.Event{
		{
			stream.TextDelta{Text: "ok"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}}

	// Always signals "yes, nudge" — if the mutual exclusion were broken, this
	// would show up as a reminder immediately.
	heartbeat := func() bool { return true }

	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	_, err := Run(ctx, p, &silentDisplay{}, &msgs, Config{
		MaxRetries:        1,
		ProgressHeartbeat: heartbeat,
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if idx := findLastStepWarning(msgs); idx < 0 {
		t.Fatalf("expected the last-step warning to fire on the near deadline, got none: %#v", msgs)
	}
	if got := countProgressHeartbeatReminders(msgs); got != 0 {
		t.Fatalf("expected no progress-heartbeat reminder alongside the last-step warning, got %d: %#v", got, msgs)
	}
}
