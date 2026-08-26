package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
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
			// 90% of 200000 — past defaultAutoCompactPercent (85).
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
	if !strings.Contains(msgs[0].Content[0].Text, "181000") || !strings.Contains(msgs[0].Content[0].Text, "200000") {
		t.Fatalf("summary = %q, want the measured 181000/200000 figures", msgs[0].Content[0].Text)
	}
}

func TestRun_AutoCompact_NoTriggerBelowThreshold(t *testing.T) {
	sess := newAutoCompactSession(t)
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			// 60% of 200000 — over the reminder threshold (50%) but under
			// the default auto-compact threshold (85%).
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
			// 60% of 200000 — would not trigger the default (85%) threshold.
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
		Session:      nil, // btwConfig's shape: parent's Compactor, no own Session
		Compactor:    compactor,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if compactCalls != 0 {
		t.Fatalf("compactCalls = %d, want 0 (must not compact the parent's conversation from a child with no session of its own)", compactCalls)
	}
}
