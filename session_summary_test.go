package main

import (
	"strings"
	"testing"

	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
)

// TestBuildResumeSummary covers the deterministic text layout of the info
// block. truncateForSummary + strconv formatting must produce a stable,
// scannable string regardless of how chatty the last turn was — the whole
// point of using a single Summary block (instead of replaying every event)
// is that the line is short enough never to flood the terminal.
func TestBuildResumeSummary(t *testing.T) {
	total := session.TotalUsage{Input: 1234, Output: 567}
	got := buildResumeSummary("a1b2c3", 42, total,
		"fix the bug in payment flow please",
		"I patched the refund handler",
		0)
	if !strings.Contains(got, "📋 Resumed session a1b2c3") {
		t.Errorf("missing header: %q", got)
	}
	if !strings.Contains(got, "42 messages") {
		t.Errorf("missing message count: %q", got)
	}
	if !strings.Contains(got, "1234 in / 567 out tokens") {
		t.Errorf("missing token tally: %q", got)
	}
	if !strings.Contains(got, "Last user: fix the bug") {
		t.Errorf("missing last user snippet: %q", got)
	}
	if !strings.Contains(got, "Last assistant: I patched the refund") {
		t.Errorf("missing last assistant snippet: %q", got)
	}
	if !strings.Contains(got, "▶ Continuing from session end") {
		t.Errorf("missing continuation marker: %q", got)
	}
}

// TestBuildResumeSummary_TruncatesLongSnippets is the regression test for the
// blow-up-on-long-sessions behaviour: the previous code replayed every
// thinking / text block which, on a session with 50 turns of 4k tokens each,
// filled the terminal past where PgUp became a pain. We now truncate to 80
// runes + "…" so the info block never exceeds ~4 screen lines.
func TestBuildResumeSummary_TruncatesLongSnippets(t *testing.T) {
	veryLong := strings.Repeat("a", 500)
	got := buildResumeSummary("x", 1, session.TotalUsage{}, veryLong, veryLong, 0)
	// 80 'a's + "…" is exactly what truncateForSummary emits.
	if !strings.Contains(got, "Last user: "+strings.Repeat("a", 80)+"…") {
		t.Errorf("expected user snippet truncated to 80 chars + ellipsis:\n%s", got)
	}
	if !strings.Contains(got, "Last assistant: "+strings.Repeat("a", 80)+"…") {
		t.Errorf("expected assistant snippet truncated to 80 chars + ellipsis:\n%s", got)
	}
	// Sanity: a 500-char field never made it into the summary untrimmed.
	if strings.Contains(got, strings.Repeat("a", 81)) {
		t.Errorf("summary line still contains an untruncated 81-char run of 'a'")
	}
}

// TestBuildResumeSummary_CorruptLineCount makes sure the corner case where
// the file had several unreadable lines still surfaces in the summary — the
// user asked for /resume and we silently swallow them.
func TestBuildResumeSummary_CorruptLineCount(t *testing.T) {
	total := session.TotalUsage{Input: 1, Output: 2, TotalCost: 0.0123}
	got := buildResumeSummary("a", 3, total, "hi", "hello", 2)
	if !strings.Contains(got, "2 corrupt lines skipped") {
		t.Errorf("missing corrupt count: %q", got)
	}
	if !strings.Contains(got, "$0.0123 total") {
		t.Errorf("missing cost: %q", got)
	}
}

// TestSummarizeResume_OnlyOneToolBlock verifies the load-in-background
// contract: even with a long RichMessage slice (representing a real session
// that previously got fully replayed and broke scrolling), the Display only
// sees one ToolBlock call. No Text, no Thinking, no ToolCallStart, no
// ToolCallDelta, no ToolCallEnd. That is what fixes selection/scroll on
// the TUI.
func TestSummarizeResume_OnlyOneToolBlock(t *testing.T) {
	c := newCapture()

	// Build a "big" conversation: 60 messages alternating user/assistant,
	// each with a hefty text block — this is what flooded the display in
	// the previous replaySessionToDisplay path.
	var msgs []providers.RichMessage
	for i := 0; i < 30; i++ {
		msgs = append(msgs,
			providers.RichMessage{
				Role:    "user",
				Content: []providers.ContentBlock{{Type: "text", Text: "q " + strings.Repeat("x", 200)}},
			},
			providers.RichMessage{
				Role:    "assistant",
				Content: []providers.ContentBlock{{Type: "text", Text: "a " + strings.Repeat("y", 200)}},
			},
		)
	}
	summarizeResume(c, "deadbeef", msgs, session.TotalUsage{Input: 9999, Output: 8888}, 0)

	if len(c.toolBlocks) != 1 {
		t.Errorf("expected exactly 1 ToolBlock, got %d", len(c.toolBlocks))
	}
	if len(c.thinking) != 0 {
		t.Errorf("expected NO Thinking calls (full replay would have flooded the UI), got %d", len(c.thinking))
	}
	if len(c.text) != 0 {
		t.Errorf("expected NO Text calls, got %d", len(c.text))
	}
	if len(c.toolStarts) != 0 {
		t.Errorf("expected NO ToolCallStart calls, got %d", len(c.toolStarts))
	}
	if len(c.toolDeltas) != 0 {
		t.Errorf("expected NO ToolCallDelta calls, got %d", len(c.toolDeltas))
	}
	if len(c.toolEnds) != 0 {
		t.Errorf("expected NO ToolCallEnd calls, got %d", len(c.toolEnds))
	}
	if len(c.summaries) != 0 {
		t.Errorf("expected NO Summary calls, got %d", len(c.summaries))
	}
}

// TestSummarizeResume_LastUserAndAssistantIsTheRealLastMessage is the
// regression test that the "Last …" snippets actually point to the
// chronologically-last user and assistant turns — not, say, the first ones.
// A wrong pick here would mean the user types their follow-up with the
// wrong mental model of what the model just said.
func TestSummarizeResume_LastUserAndAssistantIsTheRealLastMessage(t *testing.T) {
	c := newCapture()
	msgs := []providers.RichMessage{
		{Role: "user", Content: []providers.ContentBlock{{Type: "text", Text: "first user"}}},
		{Role: "assistant", Content: []providers.ContentBlock{{Type: "text", Text: "first assistant"}}},
		{Role: "user", Content: []providers.ContentBlock{{Type: "text", Text: "second user"}}},
		{Role: "assistant", Content: []providers.ContentBlock{{Type: "text", Text: "second assistant"}}},
		{Role: "user", Content: []providers.ContentBlock{{Type: "text", Text: "third user"}}},
	}
	summarizeResume(c, "x", msgs, session.TotalUsage{}, 0)
	if len(c.toolBlocks) != 1 {
		t.Fatalf("expected 1 ToolBlock, got %d", len(c.toolBlocks))
	}
	blk := c.toolBlocks[0]
	if !strings.Contains(blk, "Last user: third user") {
		t.Errorf("Last user snippet must be the chronologically last user message:\n%s", blk)
	}
	if !strings.Contains(blk, "Last assistant: second assistant") {
		t.Errorf("Last assistant snippet must be the chronologically last assistant message:\n%s", blk)
	}
}
