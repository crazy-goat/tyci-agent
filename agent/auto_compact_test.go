package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/stream"
)

// F5 (inbox): item 10's spec called for an auto-compact threshold, not just
// the model-facing reminder — the model could ignore the reminder
// indefinitely. These tests inject usage counts directly (via
// connectortest.Fake) rather than needing a real 100k-token conversation.

func newAutoCompactSession(t *testing.T) *session.Session {
	t.Helper()
	dir := t.TempDir()
	sess, err := session.Open(filepath.Join(dir, "session.jsonl"), dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func TestRun_AutoCompact_TriggersPastThreshold(t *testing.T) {
	sess := newAutoCompactSession(t)
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "working"},
			// past HardLimit (170000).
			stream.Finish{Usage: stream.Usage{Input: 180000, Output: 1000}},
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
	var compactCalls int
	compactor := func(summary, focus string) (string, error) {
		compactCalls++
		return CompactSession(sess, &msgs, summary, focus)
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
		HardLimit:    170000,
		Session:      sess,
		Compactor:    compactor,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if compactCalls != 1 {
		t.Fatalf("compactCalls = %d, want 1", compactCalls)
	}
	// The compacted history's lead message must be a harness-authored
	// summary, not the original 8-message-plus history verbatim.
	if len(msgs) == 0 || !strings.Contains(msgs[0].Content[0].Text, "Automatic compaction triggered") {
		t.Fatalf("msgs[0] = %#v, want the auto-compact summary as the lead message", msgs)
	}
	if !strings.Contains(msgs[0].Content[0].Text, "181000") || !strings.Contains(msgs[0].Content[0].Text, "170000") {
		t.Fatalf("summary = %q, want the measured 181000 and 170000 figures", msgs[0].Content[0].Text)
	}
}

func TestRun_AutoCompact_NoTriggerBelowThreshold(t *testing.T) {
	sess := newAutoCompactSession(t)
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			// 60% of 200000 — under the default soft (80%) and hard (95%) limits.
			stream.Finish{Usage: stream.Usage{Input: 120000, Output: 0}},
		},
	}
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	var compactCalls int
	compactor := func(summary, focus string) (string, error) {
		compactCalls++
		return CompactSession(sess, &msgs, summary, focus)
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
		SoftLimit:    100000,
		Session:      sess,
		Compactor:    compactor,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if compactCalls != 0 {
		t.Fatalf("compactCalls = %d, want 0 (below the auto-compact threshold)", compactCalls)
	}
	if got := countReminderLines(msgs); got != 1 {
		t.Fatalf("reminder count = %d, want 1 (the plain reminder must still fire)", got)
	}
}

func TestRun_AutoCompact_DisabledByNegativePercent(t *testing.T) {
	sess := newAutoCompactSession(t)
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "working"},
			stream.Finish{Usage: stream.Usage{Input: 190000, Output: 1000}},
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
	var compactCalls int
	compactor := func(summary, focus string) (string, error) {
		compactCalls++
		return CompactSession(sess, &msgs, summary, focus)
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries:         1,
		ContextLimit:       200000,
		Session:            sess,
		Compactor:          compactor,
		AutoCompactPercent: -1,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if compactCalls != 0 {
		t.Fatalf("compactCalls = %d, want 0 (AutoCompactPercent < 0 disables auto-compaction)", compactCalls)
	}
	if got := countReminderLines(msgs); got != 1 {
		t.Fatalf("reminder count = %d, want 1 (the plain reminder must still work when disabled)", got)
	}
}

func TestRun_AutoCompact_CustomPercent(t *testing.T) {
	sess := newAutoCompactSession(t)
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "working"},
			// 60% of 200000 — would not trigger the default hard limit (95%).
			stream.Finish{Usage: stream.Usage{Input: 120000, Output: 0}},
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
	var compactCalls int
	compactor := func(summary, focus string) (string, error) {
		compactCalls++
		return CompactSession(sess, &msgs, summary, focus)
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries:         1,
		ContextLimit:       200000,
		Session:            sess,
		Compactor:          compactor,
		AutoCompactPercent: 50,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if compactCalls != 1 {
		t.Fatalf("compactCalls = %d, want 1 (a config-tunable lower threshold must be honored)", compactCalls)
	}
}

// TestRun_AutoCompact_DoesNotReinvokeProviderAfterCompacting guards a review
// finding: the model has already finished its answer for this turn when
// auto-compaction fires (!more && !drained), so re-invoking the provider
// right after compacting would send a request whose history ends on the
// assistant's own just-delivered message — which connectortest.Fake's
// OnExhausted slot would answer if Run asked for a second turn. Anthropic
// treats a trailing assistant message as an invalid prefill; the fix is to
// fall through and return after a successful auto-compaction rather than
// `continue` the loop. If this regresses, Run would consume OnExhausted and
// this test's provider-call counter would read 2.
func TestRun_AutoCompact_DoesNotReinvokeProviderAfterCompacting(t *testing.T) {
	sess := newAutoCompactSession(t)
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "working"},
			stream.Finish{Usage: stream.Usage{Input: 180000, Output: 1000}},
		}},
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "should not be called"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	compactor := func(summary, focus string) (string, error) {
		return CompactSession(sess, &msgs, summary, focus)
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
		HardLimit:    170000,
		Session:      sess,
		Compactor:    compactor,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := p.Calls(); got != 1 {
		t.Fatalf("provider Calls() = %d, want 1 (auto-compaction must not trigger a second provider call ending on an assistant message)", got)
	}
}

// TestRun_AutoCompact_SkipsWhenNoOwnSession guards the review's HIGH-3
// finding: a /btw or fork/resume child keeps the PARENT's Compactor (it
// closes over the main conversation) while its own cfg.Session is nil.
// Without gating on cfg.Session != nil, a long-running child would
// auto-compact the live main conversation on the harness's own initiative.
func TestRun_AutoCompact_SkipsWhenNoOwnSession(t *testing.T) {
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 180000, Output: 1000}},
		},
	}
	d := &silentDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	var compactCalls int
	compactor := func(summary, focus string) (string, error) {
		compactCalls++
		return "", nil
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
		HardLimit:    170000,
		Session:      nil, // btwConfig's shape: parent's Compactor, no own Session
		Compactor:    compactor,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if compactCalls != 0 {
		t.Fatalf("compactCalls = %d, want 0 (must not compact the parent's conversation from a child with no session of its own)", compactCalls)
	}
}

func TestCompactThresholds(t *testing.T) {
	tests := []struct {
		name                    string
		window, soft, hard, pct int
		wantSoft, wantHard      int
	}{
		{"automatic", 1000000, 0, 0, 0, 800000, 950000},
		{"user limits", 1000000, 100000, 150000, 0, 100000, 150000},
		{"user limits capped by window", 1000, 5000, 9000, 0, 1000, 1000},
		{"legacy percent", 1000000, 0, 0, 50, 800000, 500000},
		{"hard wins over legacy percent", 1000000, 0, 150000, 50, 800000, 150000},
		{"negative percent disables hard", 1000000, 0, 0, -1, 800000, 0},
		{"unknown window, user limits only", 0, 100, 200, 0, 100, 200},
		{"unknown window, nothing set", 0, 0, 0, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, h := compactThresholds(tt.window, tt.soft, tt.hard, tt.pct)
			if s != tt.wantSoft || h != tt.wantHard {
				t.Fatalf("got (%d, %d), want (%d, %d)", s, h, tt.wantSoft, tt.wantHard)
			}
		})
	}
}

// User limits work when the catalog knows no window (a custom model).
func TestRun_HardLimit_WithoutKnownWindow(t *testing.T) {
	sess := newAutoCompactSession(t)
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 150001, Output: 1}},
		},
	}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	var compactCalls int
	if _, err := Run(context.Background(), p, &silentDisplay{}, &msgs, Config{
		MaxRetries: 1,
		HardLimit:  150000,
		Session:    sess,
		Compactor: func(summary, focus string) (string, error) {
			compactCalls++
			return "", nil
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if compactCalls != 1 {
		t.Fatalf("compactCalls = %d, want 1", compactCalls)
	}
}

// A child without Compactor and Session must not be told about auto-compaction.
func TestRun_SoftNotice_NoHardLimitClaimWithoutCompactor(t *testing.T) {
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 120001, Output: 1}},
		},
	}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	_, _ = Run(context.Background(), p, &silentDisplay{}, &msgs, Config{
		MaxRetries: 1,
		SoftLimit:  100000,
		HardLimit:  150000,
	})
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "hard limit") {
				t.Fatalf("notice names the hard limit: %q", b.Text)
			}
		}
	}
}

// toolRound is one model round that calls read and reports usage.
func toolRound(id string, input int) []stream.Event {
	return []stream.Event{
		stream.ToolCallStart{ID: id, Name: "read"},
		stream.ToolCall{ID: id, Name: "read", Arguments: `{}`},
		stream.Finish{Usage: stream.Usage{Input: input}, Reason: "tool_calls"},
	}
}

// #304: an agent without a session (subagent, flow role) compacts in memory
// after a tool round past the hard limit, and keeps its task.
func TestRun_InLoopCompaction_HardLimitKeepsTask(t *testing.T) {
	runner := newMockToolRunner()
	runner.SetResult("read", "file content")
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{
			toolRound("t1", 1000), toolRound("t2", 1000), toolRound("t3", 1000),
			toolRound("t4", 1000), toolRound("t5", 180000),
		},
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "the task"}}},
	}
	if _, err := Run(context.Background(), p, &silentDisplay{}, &msgs, Config{
		MaxRetries:       1,
		ContextLimit:     1000000,
		SoftLimit:        150000,
		HardLimit:        170000,
		Tools:            runner,
		InLoopCompaction: true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if msgs[0].Content[0].Text != "the task" {
		t.Fatalf("msgs[0] = %q, want the task", msgs[0].Content[0].Text)
	}
	if !strings.Contains(msgs[1].Content[0].Text, "automated context compaction") ||
		!strings.Contains(msgs[1].Content[0].Text, "180000") {
		t.Fatalf("msgs[1] = %q, want the compaction note", msgs[1].Content[0].Text)
	}
	// task + note + 8 kept messages + the final answer.
	if len(msgs) != 11 {
		t.Fatalf("len(msgs) = %d, want 11", len(msgs))
	}
	if got := countReminderLines(msgs); got != 0 {
		t.Fatalf("reminder count = %d, want 0 (the compaction replaces the notice)", got)
	}
	if last := msgs[len(msgs)-1]; last.Role != "assistant" || last.Content[0].Text != "done" {
		t.Fatalf("last = %#v, want the final answer", last)
	}
}

// #304: the soft notice of a session-less agent comes after a tool round,
// not after its final answer (which would force one more round).
func TestRun_InLoopCompaction_SoftNoticeMidTurn(t *testing.T) {
	runner := newMockToolRunner()
	runner.SetResult("read", "file content")
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns:        [][]stream.Event{toolRound("t1", 120000)},
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			// Past the soft limit again: no notice after the final answer.
			stream.Finish{Usage: stream.Usage{Input: 130000}},
		},
	}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	if _, err := Run(context.Background(), p, &silentDisplay{}, &msgs, Config{
		MaxRetries:       1,
		ContextLimit:     200000,
		SoftLimit:        100000,
		Tools:            runner,
		InLoopCompaction: true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := countReminderLines(msgs); got != 1 {
		t.Fatalf("reminder count = %d, want 1", got)
	}
	// go, tool call, tool result, notice, final answer.
	if len(msgs) != 5 || !strings.Contains(msgs[3].Content[0].Text, "190000 tokens (hard limit)") {
		t.Fatalf("msgs = %#v", msgs)
	}
}
