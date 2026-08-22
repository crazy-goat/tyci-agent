package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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

// TestThinkingBlockAdjacentToToolBlock_HitTestingLandsCorrectly: a finished
// thinking block directly followed by a tool block, with no blank line
// between them (display/tui_render_buffer.go's spacerAfter rule). Review
// finding 2 on this item was scroll accounting in tui_scroll.go not having
// learned that rule — already fixed on main and covered there
// (tui_scroll_spacer_test.go). This test covers the same adjacency from the
// thinking block's own side: its collapsed line must still be exactly one
// row, and a click on either row must land on the right block.
func TestThinkingBlockAdjacentToToolBlock_HitTestingLandsCorrectly(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width, m.height = 80, 24

	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "deciding which file to open first"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", toolName: "read", content: "ok"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	thinkingLines := m.getBlockLines(0, false)
	if len(thinkingLines) != 1 {
		t.Fatalf("thinking block should be exactly one line, got %d: %q", len(thinkingLines), thinkingLines)
	}
	toolLines := m.getBlockLines(1, false)
	if len(toolLines) != 1 {
		t.Fatalf("tool block should be exactly one line, got %d: %q", len(toolLines), toolLines)
	}

	flat := m.buildAllFlatRenderLines()
	if len(flat) != 2 {
		t.Fatalf("expected no spacer between an adjacent thinking and tool block, got %d flat lines: %+v", len(flat), flat)
	}
	if flat[0].BlockIndex != 0 || flat[0].SourceKind != "thinking" {
		t.Fatalf("row 0 should belong to the thinking block, got index=%d kind=%q", flat[0].BlockIndex, flat[0].SourceKind)
	}
	if flat[1].BlockIndex != 1 || flat[1].SourceKind != "tool" {
		t.Fatalf("row 1 should belong to the tool block, got index=%d kind=%q", flat[1].BlockIndex, flat[1].SourceKind)
	}
	if got := m.blockAtVisibleLine(0); got != 0 {
		t.Fatalf("blockAtVisibleLine(0) = %d, want 0 (the thinking block)", got)
	}
	if got := m.blockAtVisibleLine(1); got != 1 {
		t.Fatalf("blockAtVisibleLine(1) = %d, want 1 (the tool block)", got)
	}
}

// TestThinkingBlockRetruncatesAfterWidthChange: the collapsed line's
// display-time truncation (renderThinkingBlock budgeting the summary
// against m.width) must not be stuck at whatever width the block first
// rendered at. A resize invalidates every block's cachedLines
// (invalidateAllBlockLineCounts in tui_update.go's handleResizeFlush) —
// simulate that directly here rather than the full resize plumbing, which
// isn't specific to thinking blocks.
func TestThinkingBlockRetruncatesAfterWidthChange(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 120

	content := "this summary is long enough that a narrow terminal will have to cut it down substantially to fit"
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: content})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	wide := m.getBlockLines(0, false)[0]
	if lipgloss.Width(stripANSI(wide)) > m.width {
		t.Fatalf("wide render already overflows m.width=%d: %q", m.width, wide)
	}

	m.width = 40
	m.invalidateAllBlockLineCounts()

	narrow := m.getBlockLines(0, false)[0]
	if lipgloss.Width(stripANSI(narrow)) > m.width {
		t.Fatalf("narrow render overflows the new m.width=%d: %q", m.width, narrow)
	}
	if narrow == wide {
		t.Fatalf("render did not change after narrowing the width — stale cache?")
	}
	if got := len(m.getBlockLines(0, false)); got != 1 {
		t.Fatalf("still expected exactly one line after the width change, got %d", got)
	}
}

// TestThinkingBlockCJKSummary_FreezesByRunesNotBytes: a byte-length freeze
// threshold cuts a multi-byte (CJK) summary off far earlier than the
// intended rune count, and — since the freeze is permanent — keeps it that
// short forever. Each of these characters is 3 bytes in UTF-8 but 1 rune, so
// a byte-based threshold would freeze at a third of the intended length.
func TestThinkingBlockCJKSummary_FreezesByRunesNotBytes(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 200 // wide enough that display-time truncation isn't also in play

	cjkWord := "思考" // 2 runes, 6 bytes
	// 21 runes (63 bytes) — enough to freeze on a byte-count threshold of 60,
	// nowhere near enough on a rune-count threshold of 60.
	shortBurst := strings.Repeat(cjkWord, 10) + "思"
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: shortBurst})

	if m.blocks[0].thinkingSummary != "" {
		t.Fatalf("froze at %d runes (%d bytes) — the threshold is counting bytes, not runes: %q",
			len([]rune(m.blocks[0].thinkingSummary)), len(m.blocks[0].thinkingSummary), m.blocks[0].thinkingSummary)
	}

	// Enough more text to cross a genuine 60-rune threshold.
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: strings.Repeat(cjkWord, 25)})

	if m.blocks[0].thinkingSummary == "" {
		t.Fatal("expected the summary to be frozen once it has crossed the rune threshold")
	}
	if got := len([]rune(m.blocks[0].thinkingSummary)); got > thinkingSummaryMaxLen {
		t.Fatalf("frozen summary is %d runes, want at most %d", got, thinkingSummaryMaxLen)
	}
}

// TestThinkingBlockCJKSummary_DisplayTruncationByWidthNotRunes: a frozen
// CJK summary can be well within thinkingSummaryMaxLen runes and still be
// far too wide to fit a normal terminal, because each CJK character is 2
// display columns. renderThinkingBlock's own truncation must budget by
// lipgloss.Width, not rune count, or the line overflows m.width.
func TestThinkingBlockCJKSummary_DisplayTruncationByWidthNotRunes(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 40

	// 21 CJK runes, well under thinkingSummaryMaxLen (60), but ~42 display
	// columns on its own — before even the gutter, label, duration and hint.
	content := strings.Repeat("思考", 10) + "中"
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: content})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	line := m.getBlockLines(0, false)[0]
	if got := lipgloss.Width(stripANSI(line)); got > m.width {
		t.Fatalf("collapsed line is %d display columns wide, want at most %d: %q", got, m.width, stripANSI(line))
	}
}

// TestClickOnThinkingBlockModal_FullTextReachableViaWrapping is the fix for
// review finding 1: the modal used to hard-truncate each *logical* line to
// popupWidth-4 with no ellipsis and no way to scroll to the rest. A thinking
// block's reasoning is prose — one paragraph is a single logical line that
// can run to hundreds of characters — so that silently dropped most of it.
// The modal must wrap instead, and the wrapped line count (not the raw
// newline count) must drive how far the modal can scroll.
func TestClickOnThinkingBlockModal_FullTextReachableViaWrapping(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	// A short terminal, so the wrapped content doesn't fit in one screen and
	// scrolling is actually exercised, not just wrapping.
	m.width, m.height = 100, 12

	// One long logical line (no newlines) — the shape a real reasoning
	// paragraph takes. Comfortably more than one popup-width's worth.
	word := "reasoning "
	var b strings.Builder
	for b.Len() < 554 {
		b.WriteString(word)
	}
	full := strings.TrimSpace(b.String())

	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: full})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.openToolBlockModal(0)
	if !m.subagentModalActive {
		t.Fatal("expected the modal to open")
	}

	wrapped := m.subagentModalWrappedLines()
	rejoined := strings.Join(wrapped, " ")
	// Every word from the original content must still be present somewhere
	// in the wrapped output — nothing silently cut off.
	for _, word := range strings.Fields(full) {
		if !strings.Contains(rejoined, word) {
			t.Fatalf("word %q from the original content is missing from the wrapped modal lines", word)
		}
	}
	// Wrapping must actually have happened — a 554-character logical line
	// does not fit in one ~96-column row.
	if len(wrapped) < 2 {
		t.Fatalf("expected the long logical line to wrap into multiple rows, got %d", len(wrapped))
	}
	// The line count that drives scrolling must be the wrapped count, not
	// the raw (== 1, since there are no newlines) logical line count.
	if got := m.subagentModalLineCount(); got != len(wrapped) {
		t.Fatalf("subagentModalLineCount() = %d, want %d (the wrapped line count)", got, len(wrapped))
	}
	if m.subagentModalMaxScroll() <= 0 {
		t.Fatal("expected the modal to be scrollable to reach the rest of the wrapped content")
	}
}
