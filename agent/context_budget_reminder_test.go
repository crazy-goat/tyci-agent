package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/stream"
)

// Item 10, design point (b): the budget reminder must state a measured fact
// ("you are at N of M tokens") rather than diagnose the model, and it must
// use the same per-call usage the status bar's context figure reads — not a
// re-derived character count — per design point (c) it only fires at a turn
// boundary (!more && !drained), with a one-per-Run cap so it cannot crowd out
// the real conversation the way an uncapped nag would.

func countReminderLines(msgs []connector.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "user" && len(m.Content) > 0 &&
			strings.Contains(m.Content[0].Text, "automated context budget reminder") {
			n++
		}
	}
	return n
}

func TestRun_ContextBudgetReminder_FiresOnceThenFinishes(t *testing.T) {
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "working"},
			stream.Finish{Usage: stream.Usage{Input: 150000, Output: 1000}},
		}},
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// One call crosses the threshold and triggers the reminder, forcing a
	// second call; the second call's usage is small, so it must not trigger
	// a second reminder and the loop must stop there.
	if got, want := p.Calls(), 2; got != want {
		t.Fatalf("Stream calls = %d, want %d", got, want)
	}
	if got := countReminderLines(msgs); got != 1 {
		t.Fatalf("reminder count = %d, want 1", got)
	}
	for _, m := range msgs {
		if m.Role != "user" || len(m.Content) == 0 || !strings.Contains(m.Content[0].Text, "automated context budget reminder") {
			continue
		}
		if !strings.Contains(m.Content[0].Text, "151000") || !strings.Contains(m.Content[0].Text, "200000") {
			t.Fatalf("reminder text = %q, want the measured 151000/200000 figures", m.Content[0].Text)
		}
	}
}

func TestRun_ContextBudgetReminder_NoNudgeBelowThreshold(t *testing.T) {
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 10, Output: 10}},
		},
	}
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := p.Calls(), 1; got != want {
		t.Fatalf("Stream calls = %d, want %d", got, want)
	}
	if got := countReminderLines(msgs); got != 0 {
		t.Fatalf("reminder count = %d, want 0", got)
	}
}

func TestRun_ContextBudgetReminder_DisabledWithoutKnownLimit(t *testing.T) {
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 999999, Output: 999999}},
		},
	}
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}

	// ContextLimit left at zero: the catalog has no known window for this
	// model, and a percentage of an unknown limit is nonsense — see
	// display.TuiModel.contextUsed's identical "ok=false" rule.
	if _, err := Run(context.Background(), p, d, &msgs, Config{MaxRetries: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := countReminderLines(msgs); got != 0 {
		t.Fatalf("reminder count = %d, want 0", got)
	}
}

// Cached prompt tokens (Anthropic reports these separately from Input via
// CacheRead/CacheWrite, and prompt caching is on by default) still occupy
// the context window and must count toward the reminder threshold, or a
// long conversation running mostly off a cached prefix would never trip it.
func TestRun_ContextBudgetReminder_CountsCacheTokens(t *testing.T) {
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "working"},
			stream.Finish{Usage: stream.Usage{Input: 10, CacheRead: 140000, CacheWrite: 10000, Output: 1000}},
		}},
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Input(10)+Output(1000) alone is 1010, well under 50% of 200000 — the
	// reminder must only fire once CacheRead/CacheWrite are added in too
	// (total 151010).
	if got := countReminderLines(msgs); got != 1 {
		t.Fatalf("reminder count = %d, want 1 (cache tokens must count toward the budget)", got)
	}
	for _, m := range msgs {
		if m.Role != "user" || len(m.Content) == 0 || !strings.Contains(m.Content[0].Text, "automated context budget reminder") {
			continue
		}
		if !strings.Contains(m.Content[0].Text, "151010") {
			t.Fatalf("reminder text = %q, want it to include the cache-inclusive total 151010", m.Content[0].Text)
		}
	}
}

// A round that produces no usage at all (e.g. Finish{} with every counter
// zero) must not zero out the caller's tracked "last round" usage — that
// would make one usage-less round anywhere in a multi-tool turn permanently
// blind the reminder for the rest of it, exactly backwards from the intent
// of tracking the last known usage.
func TestRun_ContextBudgetReminder_UsagelessRoundDoesNotClearLastRoundUsage(t *testing.T) {
	runner := newMockToolRunner()
	runner.SetResult("read", "file content")
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{{
			stream.ToolCallStart{ID: "tc1", Name: "read"},
			stream.ToolCall{ID: "tc1", Name: "read", Arguments: `{}`},
			stream.Finish{Usage: stream.Usage{Input: 150000, Output: 1000}, Reason: "tool_calls"},
		}, {
			// Second round: text-only finish with zero usage. If this
			// overwrote lastRoundUsage with the zero value, the reminder
			// below would never fire.
			stream.TextDelta{Text: "done"},
			stream.Finish{},
		}},
	}
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
		Tools:        runner,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := countReminderLines(msgs); got != 1 {
		t.Fatalf("reminder count = %d, want 1 (a usage-less round must not clear the remembered usage)", got)
	}
}

// tryFallback must thread runOnce's usage return back to the caller the same
// way the primary path does, so a turn that fails over to a fallback model
// mid-turn does not leave the caller's "last round" usage stale (agent/
// fallback.go:47 used to discard it entirely with "_").
func TestRun_ContextBudgetReminder_SurvivesFallback(t *testing.T) {
	runner := newMockToolRunner()
	runner.SetResult("read", "file content")
	primary := &connectortest.Fake{ProviderName: "fb-primary", ModelName: "primary-1", StreamErr: errors.New("primary down")}
	fallback := &connectortest.Fake{
		ProviderName: "fb-fallback",
		ModelName:    "fb-1",
		Turns: [][]stream.Event{{
			stream.ToolCallStart{ID: "tc1", Name: "read"},
			stream.ToolCall{ID: "tc1", Name: "read", Arguments: `{}`},
			stream.Finish{Usage: stream.Usage{Input: 150000, Output: 1000}, Reason: "tool_calls"},
		}},
		OnExhausted: []stream.Event{
			// The round right after the fallback switch produces no usage;
			// the reminder must still see the fallback round's usage.
			stream.TextDelta{Text: "done"},
			stream.Finish{},
		},
	}
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}

	if _, err := Run(context.Background(), primary, d, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
		Tools:        runner,
		Fallbacks:    []connector.ModelClient{fallback},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := countReminderLines(msgs); got != 1 {
		t.Fatalf("reminder count = %d, want 1 (fallback round's usage must not be discarded)", got)
	}
}

func TestRun_ContextBudgetReminder_UsesContextLimitForOverStaticLimit(t *testing.T) {
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "working"},
			stream.Finish{Usage: stream.Usage{Input: 60, Output: 10}},
		}},
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}

	calls := 0
	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000, // would not trigger on its own
		ContextLimitFor: func(provider, model string) int {
			calls++
			return 100 // the per-model lookup wins, and 70/100 crosses 50%
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls == 0 {
		t.Fatal("ContextLimitFor was never consulted")
	}
	if got := countReminderLines(msgs); got != 1 {
		t.Fatalf("reminder count = %d, want 1", got)
	}
}
