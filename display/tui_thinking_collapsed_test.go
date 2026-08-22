package display

import (
	"strings"
	"testing"
)

// ─── Thinking blocks render collapsed, like a tool call (TODO item 19) ────
//
// These tests build models through the real message path (newModel then
// handleBlockMsg), as tui_memory_test.go does — a hand-built TuiModel
// panics on nil caches.

// TestThinkingBlockCollapsesToOneLine: the whole point of this change. A
// thinking block must render as a single line, not the full reasoning text.
func TestThinkingBlockCollapsesToOneLine(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80

	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "Find all usages of validateOptions across the codebase and check each call site"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	lines := m.getBlockLines(0, false)
	if len(lines) != 1 {
		t.Fatalf("expected exactly one line, got %d: %q", len(lines), lines)
	}
	if m.blocks[0].cachedLineCount != 1 {
		t.Fatalf("cachedLineCount = %d, want 1", m.blocks[0].cachedLineCount)
	}
}

// TestThinkingBlockNamesASummary: the collapsed line must name a summary
// drawn from the block's opening words, the way a tool line names its args.
func TestThinkingBlockNamesASummary(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80

	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "Find all usages of validateOptions"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	line := stripANSI(m.getBlockLines(0, false)[0])
	if !strings.Contains(line, "thinking(") {
		t.Fatalf("expected a thinking(...) summary, got %q", line)
	}
	if !strings.Contains(line, "Find all usages of validateOptions") {
		t.Fatalf("summary should be drawn from the opening words, got %q", line)
	}
}

// TestThinkingBlockShowsDuration: block.duration is only ever set by
// tool-end, so the TUI has to time a thinking block itself. Once the block
// finishes (here: the turn ends), a duration must appear on the line.
func TestThinkingBlockShowsDuration(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80

	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "reasoning about the fix"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if m.blocks[0].duration <= 0 {
		t.Fatalf("expected a measured duration > 0, got %v", m.blocks[0].duration)
	}
	line := stripANSI(m.getBlockLines(0, false)[0])
	if !strings.Contains(line, "click to display") {
		t.Fatalf("finished thinking block should offer to display, got %q", line)
	}
	// formatDuration always emits a unit suffix ("ms" or "s").
	if !strings.Contains(line, "ms") && !strings.Contains(line, "s)") && !strings.Contains(line, "s ") {
		t.Fatalf("expected a formatted duration on the line, got %q", line)
	}
}

// TestThinkingBlockWhileStreamingShowsProgress: before the block has
// finished, there is no duration yet — it should read like a running tool
// (spinner, "click for progress"), not claim a duration it doesn't have.
func TestThinkingBlockWhileStreamingShowsProgress(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80

	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "still working through this"})

	line := stripANSI(m.renderBlock(0, m.blocks[0]))
	if !strings.Contains(line, "click for progress") {
		t.Fatalf("still-streaming thinking block should read as running, got %q", line)
	}
	if strings.Contains(line, "click to display") {
		t.Fatalf("should not claim to be finished yet: %q", line)
	}
}

// TestThinkingBlockFullTextSurvivesForModal: the collapsed line is a
// summary, but nothing may be dropped from the block's content — the full
// text must still be reachable through the same modal a tool block uses.
func TestThinkingBlockFullTextSurvivesForModal(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80

	full := "Find all usages of validateOptions across the codebase.\nThen check each call site for the old three-argument form."
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: full})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if m.blocks[0].content != full {
		t.Fatalf("block content was altered:\n got %q\nwant %q", m.blocks[0].content, full)
	}

	m.openToolBlockModal(0)
	if !m.subagentModalActive {
		t.Fatal("expected the modal to be active")
	}
	if got := m.subagentModalText(); got != full {
		t.Fatalf("modal text = %q, want the full thinking content %q", got, full)
	}
}

// TestClickOnThinkingBlockOpensModal: a thinking block must reuse the exact
// modal a tool block opens, not a new widget.
func TestClickOnThinkingBlockOpensModal(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80

	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "reasoning about the approach"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if m.subagentModalActive {
		t.Fatal("modal should not be open before the click")
	}

	m.openToolBlockModal(0)

	if !m.subagentModalActive {
		t.Fatal("modal should be active after the click")
	}
	if m.subagentModalBlockIdx != 0 {
		t.Fatalf("modal should point at block 0, got %d", m.subagentModalBlockIdx)
	}
	if !m.subagentModalDone {
		t.Fatal("a finished thinking block's modal should read as done")
	}
}

// TestThinkingBlockSummaryStableAcrossDeltas: the core streaming
// constraint. Once the summary is decided from a block's opening words, more
// deltas must not change it — otherwise the collapsed line flickers as the
// model keeps thinking.
func TestThinkingBlockSummaryStableAcrossDeltas(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80

	// Send enough text in the first delta to cross the freeze threshold.
	first := strings.Repeat("word ", 20) // 100 chars, well past thinkingSummaryMaxLen
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: first})

	summaryAfterFirst := m.blocks[0].thinkingSummary
	if summaryAfterFirst == "" {
		t.Fatal("expected the summary to be frozen once enough text has arrived")
	}
	lineAfterFirst := stripANSI(m.renderBlock(0, m.blocks[0]))

	// More deltas keep arriving.
	for i := 0; i < 5; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "more and more reasoning text arrives here "})
	}

	if m.blocks[0].thinkingSummary != summaryAfterFirst {
		t.Fatalf("summary changed after more deltas:\n got %q\nwant %q", m.blocks[0].thinkingSummary, summaryAfterFirst)
	}
	lineAfterMore := stripANSI(m.renderBlock(0, m.blocks[0]))
	if lineAfterMore != lineAfterFirst {
		t.Fatalf("collapsed line changed while streaming (flicker):\n got %q\nwant %q", lineAfterMore, lineAfterFirst)
	}
}

// TestThinkingBlockShortContentFreezesOnFinish: a thinking block that never
// reaches the freeze threshold must still get a summary once it finishes,
// using whatever text it has.
func TestThinkingBlockShortContentFreezesOnFinish(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80

	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "short thought"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if m.blocks[0].thinkingSummary == "" {
		t.Fatal("expected a summary to be frozen at finish even for short content")
	}
	line := stripANSI(m.getBlockLines(0, false)[0])
	if !strings.Contains(line, "short thought") {
		t.Fatalf("summary should contain the original words, got %q", line)
	}
}

// TestThinkingBlockLineCountMatchesLayout: cachedLineCount is what the
// scroll/layout math trusts. If it disagrees with the actual rendered line
// count, scrolling and mouse hit-testing silently land on the wrong row —
// exactly the failure mode a width-keyed, per-block cache invites.
func TestThinkingBlockLineCountMatchesLayout(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80

	// Multi-paragraph content that would have wrapped to many lines under
	// the old full-render behaviour.
	content := strings.Repeat("this is a long line of reasoning that keeps going. ", 10)
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: content})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	lines := m.getBlockLines(0, false)
	if len(lines) != m.blocks[0].cachedLineCount {
		t.Fatalf("cachedLineCount (%d) disagrees with actual rendered lines (%d)", m.blocks[0].cachedLineCount, len(lines))
	}
	if m.blocks[0].cachedLineCount != 1 {
		t.Fatalf("expected the collapsed block to occupy exactly one row, got %d", m.blocks[0].cachedLineCount)
	}
}
