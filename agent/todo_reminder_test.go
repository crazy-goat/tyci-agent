package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/stream"
)

// countingText returns a Fake that answers every call — not just the first —
// with the same text-only turn, which is what these tests need: the agent
// re-runs after each todo nudge and must get the same "I'm done" answer back.
// The script lives in OnExhausted rather than Turns precisely because
// OnExhausted covers turn 0 onwards when Turns is empty.
//
// Usage{Input: 1, Output: 1} is not decoration: runOnce only emits
// Summary/Total when hasUsage is true, so it keeps the display traffic
// identical to the hand-written double this replaced.
func countingText() *connectortest.Fake {
	return &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
}

// When todos remain open, the agent should nudge itself exactly
// maxTodoReminders times (so runOnce runs 1 + maxTodoReminders times) and
// inject a system-reminder user message each time.
func TestRun_TodoReminder_NudgesUpToLimit(t *testing.T) {
	p := countingText()
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "do it"}}},
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries: 1,
		PendingTodos: func() []string {
			return []string{"1. [doing] high finish the thing"}
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got, want := p.Calls(), 1+maxTodoReminders; got != want {
		t.Errorf("Stream calls = %d, want %d", got, want)
	}

	reminders := 0
	for _, m := range msgs {
		if m.Role == "user" && len(m.Content) > 0 &&
			strings.Contains(m.Content[0].Text, "<system-reminder>") &&
			strings.Contains(m.Content[0].Text, "finish the thing") {
			reminders++
		}
	}
	if reminders != maxTodoReminders {
		t.Errorf("injected reminders = %d, want %d", reminders, maxTodoReminders)
	}
}

// With no open todos, the agent finishes after a single turn and injects nothing.
func TestRun_TodoReminder_NoNudgeWhenEmpty(t *testing.T) {
	p := countingText()
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "do it"}}},
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries:   1,
		PendingTodos: func() []string { return nil },
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := p.Calls(); got != 1 {
		t.Errorf("Stream calls = %d, want 1", got)
	}
	for _, m := range msgs {
		if m.Role == "user" && len(m.Content) > 0 && strings.Contains(m.Content[0].Text, "<system-reminder>") {
			t.Errorf("unexpected reminder injected: %q", m.Content[0].Text)
		}
	}
}

// The agent stops nudging once the todos are resolved, even if the limit
// hasn't been reached — modeling the model actually completing the work.
func TestRun_TodoReminder_StopsWhenResolved(t *testing.T) {
	p := countingText()
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "do it"}}},
	}

	remaining := 1 // pending on the first finish, resolved thereafter
	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries: 1,
		PendingTodos: func() []string {
			if remaining > 0 {
				remaining--
				return []string{"1. [todo] normal wrap up"}
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Initial turn + one nudge (which then finds nothing pending) = 2 calls.
	if got := p.Calls(); got != 2 {
		t.Errorf("Stream calls = %d, want 2", got)
	}
}
