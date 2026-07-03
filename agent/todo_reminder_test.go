package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
)

// countingTextProvider always returns a text-only response (no tool calls)
// and records how many times Stream was invoked.
type countingTextProvider struct {
	mu    sync.Mutex
	calls int
}

func (m *countingTextProvider) Name() string         { return "count" }
func (m *countingTextProvider) IsConfigured() bool   { return true }
func (m *countingTextProvider) Models() []string     { return []string{"count-1"} }
func (m *countingTextProvider) FreeModels() []string { return nil }

func (m *countingTextProvider) Stream(ctx context.Context, req providers.Request) (<-chan stream.Event, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	ch := make(chan stream.Event, 2)
	ch <- stream.TextDelta{Text: "done"}
	ch <- stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}}
	close(ch)
	return ch, nil
}

func (m *countingTextProvider) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// When todos remain open, the agent should nudge itself exactly
// maxTodoReminders times (so runOnce runs 1 + maxTodoReminders times) and
// inject a system-reminder user message each time.
func TestRun_TodoReminder_NudgesUpToLimit(t *testing.T) {
	p := &countingTextProvider{}
	d := &silentDisplay{}
	msgs := []providers.RichMessage{
		{Role: "user", Content: []providers.ContentBlock{{Type: "text", Text: "do it"}}},
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "count-1",
		MaxRetries: 1,
		PendingTodos: func() []string {
			return []string{"1. [doing] high finish the thing"}
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got, want := p.callCount(), 1+maxTodoReminders; got != want {
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
	p := &countingTextProvider{}
	d := &silentDisplay{}
	msgs := []providers.RichMessage{
		{Role: "user", Content: []providers.ContentBlock{{Type: "text", Text: "do it"}}},
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		Model:        "count-1",
		MaxRetries:   1,
		PendingTodos: func() []string { return nil },
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := p.callCount(); got != 1 {
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
	p := &countingTextProvider{}
	d := &silentDisplay{}
	msgs := []providers.RichMessage{
		{Role: "user", Content: []providers.ContentBlock{{Type: "text", Text: "do it"}}},
	}

	remaining := 1 // pending on the first finish, resolved thereafter
	if _, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "count-1",
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
	if got := p.callCount(); got != 2 {
		t.Errorf("Stream calls = %d, want 2", got)
	}
}
