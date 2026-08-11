package display

import (
	"strings"
	"testing"
)

// ─── scrollback disk cache ────────────────────────────────────────────────
//
// These tests cover the paging scheme in tui_scrollback.go: old rendered
// blocks are flushed to a temp file and paged back in on scroll-up/resize,
// keeping resident memory bounded without dropping history.

func TestScrollbackFlushAndPageIn(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	m.height = 24

	// Create a block with rendered lines, then force-flush it.
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "line one\nline two\nline three"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"}) // finalize render
	m.forceRenderDirtyBlocks()

	// Ensure block 0 has cached lines.
	linesBefore := m.getBlockLines(0, false)
	if len(linesBefore) == 0 {
		t.Fatal("expected block 0 to have rendered lines")
	}
	// Manually flush block 0 to the cache.
	m.scrollback.flushBlock(&m.blocks[0], m.width)
	if !m.blocks[0].flushed {
		t.Fatal("expected block flushed=true after flushBlock")
	}
	if m.blocks[0].cachedLines != nil {
		t.Error("expected cachedLines nil after flush")
	}
	if m.blocks[0].cachedLineCount == 0 {
		t.Error("cachedLineCount must survive flush (needed for scroll math)")
	}

	// Page it back in via getBlockLines (the normal scroll-up path).
	linesAfter := m.getBlockLines(0, false)
	if linesAfter == nil {
		t.Fatal("pageIn returned nil — block lost its lines")
	}
	if len(linesAfter) != len(linesBefore) {
		t.Errorf("pageIn line count = %d, want %d", len(linesAfter), len(linesBefore))
	}
	for i := range linesBefore {
		if linesAfter[i] != linesBefore[i] {
			t.Errorf("pageIn line %d mismatch:\n got %q\nwant %q", i, linesAfter[i], linesBefore[i])
		}
	}
	if m.blocks[0].flushed {
		t.Error("expected flushed=false after pageIn")
	}

	// Cleanup.
	m.scrollback.close()
}

func TestScrollbackBudgetEvictsOldBlocks(t *testing.T) {
	// With the 256 KiB resident budget, large blocks force eviction of the
	// oldest while the newest stay resident. History (block count) must NOT
	// shrink — only the heavy rendered-line content is paged to disk.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	m.height = 24

	// Each block ~32 KiB of rendered content → 8+ blocks exceed the 256 KiB
	// budget and trigger eviction of the oldest.
	big := strings.Repeat("line of content here\n", 1600) // ~32 KiB
	for i := 0; i < 12; i++ {
		if i%2 == 0 {
			m.appendOrAppend("text", "You: "+itoa(i)+" "+big)
		} else {
			m.appendOrAppend("text", "agent "+itoa(i)+" "+big)
		}
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.forceRenderDirtyBlocks()

	// Block count must be intact — we don't drop history.
	if got := len(m.blocks); got != 12 {
		t.Errorf("len(blocks) = %d, want 12 (history must not be dropped)", got)
	}
	flushedCount := 0
	for i := range m.blocks {
		if m.blocks[i].flushed {
			flushedCount++
		}
	}
	if flushedCount == 0 {
		t.Error("expected some blocks to be flushed over the 256 KiB budget")
	}
	// The most recent block should be resident.
	if m.blocks[len(m.blocks)-1].flushed {
		t.Error("the newest block should stay resident, not flushed")
	}
	// Scrolling to the oldest block should page it back in and stay readable.
	got := m.getBlockLines(0, false)
	if got == nil {
		t.Fatal("oldest block should page back in on getBlockLines")
	}
	if m.blocks[0].flushed {
		t.Error("oldest block should be resident after getBlockLines")
	}
	// After paging the oldest back in, something older-than-it (nothing here,
	// it's the oldest) would be evicted; but a middle block may now be flushed
	// to make room. The newest must still be readable.
	if got := m.getBlockLines(len(m.blocks)-1, false); got == nil {
		t.Fatal("newest block should still be readable after paging oldest in")
	}
	m.scrollback.close()
}

// mdCacheRenderedBytes totals the byte length of all cached rendered-ANSI
// strings held in the mdCacheRendered map. This map mirrors cachedLines and is
// the largest per-block cache, so it must be bounded alongside the resident
// window — otherwise a long session leaks a full copy of every rendered block.
func mdCacheRenderedBytes(m *TuiModel) int {
	n := 0
	for _, s := range m.mdCacheRendered {
		n += len(s)
	}
	return n
}

func TestScrollbackDropsRenderCachesOnFlush(t *testing.T) {
	// Regression: mdCacheRendered/toolDisplayCache/streamWraps held a full
	// duplicate of every flushed block's rendered output. flushBlock dropped
	// cachedLines but left these maps, so resident RAM was ~2x the budget for
	// resident blocks and grew without bound (one entry per historical block).
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	m.height = 24

	big := strings.Repeat("line of content here\n", 1600) // ~32 KiB rendered
	for i := 0; i < 24; i++ {
		if i%2 == 0 {
			m.appendOrAppend("text", "You: "+itoa(i)+" "+big)
		} else {
			m.appendOrAppend("text", "agent "+itoa(i)+" "+big)
		}
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.forceRenderDirtyBlocks()
	// Trigger eviction of old blocks past the resident budget.
	m.maybeFlushOldBlocks()

	flushedCount := 0
	for i := range m.blocks {
		if m.blocks[i].flushed {
			flushedCount++
			if _, ok := m.mdCacheRendered[i]; ok {
				t.Errorf("flushed block %d still holds an mdCacheRendered entry", i)
			}
			if _, ok := m.toolDisplayCache[i]; ok {
				t.Errorf("flushed block %d still holds a toolDisplayCache entry", i)
			}
			if _, ok := m.streamWraps[i]; ok {
				t.Errorf("flushed block %d still holds a streamWraps entry", i)
			}
		}
	}
	if flushedCount == 0 {
		t.Fatal("expected some blocks flushed over the budget")
	}

	// The render-cache map must stay near the resident budget, not grow to the
	// full history size. Allow a little slack for the resident window.
	if got := mdCacheRenderedBytes(&m); got > tuiScrollbackResidentBudget*2 {
		t.Errorf("mdCacheRendered bytes = %d, want <= %d (leak: unbounded render cache)",
			got, tuiScrollbackResidentBudget*2)
	}

	// Paging an old block back in must still work (render survives via disk).
	if got := m.getBlockLines(0, false); got == nil {
		t.Fatal("flushed block should page back in even after its render cache was dropped")
	}
	m.scrollback.close()
}

func TestScrollbackResizeRewrapsPagedLines(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	m.height = 24

	// A long line that wraps at width 80 into multiple sub-lines.
	long := strings.Repeat("word ", 40) // ~200 chars → 3 lines at 80
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: long})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.forceRenderDirtyBlocks()

	lines80 := m.getBlockLines(0, false)
	if len(lines80) < 2 {
		t.Fatalf("expected wrapping at width 80, got %d lines", len(lines80))
	}

	// Flush at width 80.
	m.scrollback.flushBlock(&m.blocks[0], 80)

	// Resize narrower — the paged-in lines must re-wrap for the new width.
	m.width = 40
	m.invalidateAllBlockLineCounts()
	lines40 := m.getBlockLines(0, false)
	if lines40 == nil {
		t.Fatal("pageIn after resize returned nil")
	}
	// Narrower width → more sub-lines (or at least as many).
	if len(lines40) < len(lines80) {
		t.Errorf("narrower width produced fewer lines: got %d, want >= %d", len(lines40), len(lines80))
	}
	// The content should still be present (not truncated to empty).
	joined := strings.Join(lines40, " ")
	if !strings.Contains(joined, "word") {
		t.Errorf("re-wrapped content lost text: %q", joined)
	}

	m.scrollback.close()
}

func TestScrollbackResetOnNew(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	m.height = 24
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "history"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.forceRenderDirtyBlocks()
	m.scrollback.flushBlock(&m.blocks[0], m.width)
	if m.scrollback.file == nil {
		t.Fatal("expected cache file open after flush")
	}

	// /new resets everything including the scrollback cache.
	m.handleBlockMsg(tuiMsgBlock{kind: "reset"})
	if m.scrollback.file != nil {
		t.Error("expected cache file closed after reset")
	}
	if len(m.blocks) != 0 {
		t.Errorf("expected blocks cleared after reset, got %d", len(m.blocks))
	}
}

func TestScrollbackPageInRestoresToolPayloads(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	m.height = 24
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"command":"ls -la"}`})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "line1\nline2\nline3"})
	m.forceRenderDirtyBlocks()

	_ = m.getBlockLines(0, false)
	beforeContent := m.blocks[0].content
	beforeOutput := m.blocks[0].output
	m.scrollback.flushBlock(&m.blocks[0], m.width)
	if m.blocks[0].content != "" || m.blocks[0].output != "" {
		t.Fatal("flush should drop in-memory tool payloads")
	}

	lines := m.getBlockLines(0, false)
	if len(lines) == 0 {
		t.Fatal("expected paged-in tool block to keep rendered lines")
	}
	if m.blocks[0].content != beforeContent {
		t.Fatalf("restored content = %q, want %q", m.blocks[0].content, beforeContent)
	}
	if m.blocks[0].output != beforeOutput {
		t.Fatalf("restored output = %q, want %q", m.blocks[0].output, beforeOutput)
	}
	m.scrollback.close()
}

func TestOpenToolBlockModalPagesInFlushedTool(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	m.height = 24
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"command":"ls -la"}`})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "full tool output"})
	m.forceRenderDirtyBlocks()
	_ = m.getBlockLines(0, false)
	m.scrollback.flushBlock(&m.blocks[0], m.width)

	m.openToolBlockModal(0)
	if !m.subagentModalActive {
		t.Fatal("expected modal active after opening flushed tool")
	}
	if got := m.subagentModalText(); got != "full tool output" {
		t.Fatalf("modal content = %q, want %q", got, "full tool output")
	}
	m.scrollback.close()
}

// itoa is a tiny strconv.Itoa replacement to keep this test file import-light.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ─── tool output cap (kept from the previous commit) ──────────────────────

func TestCapToolOutputKeepsTail(t *testing.T) {
	s := "short"
	if got := capToolOutput(s, 1<<20); got != s {
		t.Errorf("capToolOutput under cap changed the string")
	}
	big := strings.Repeat("line\n", 100000) // ~2.5MB
	got := capToolOutput(big, 1<<20)
	if len(got) > 1<<20 {
		t.Errorf("capped output len = %d, want <= %d", len(got), 1<<20)
	}
	if !strings.HasPrefix(got, "line\n") {
		t.Errorf("capped output should start at a line boundary, got %q...", got[:20])
	}
	if !strings.HasSuffix(got, "line\n") {
		t.Errorf("capped output lost the tail, got %q...", got[len(got)-20:])
	}
}

func TestAppendToolCapsOutput(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	huge := strings.Repeat("x", tuiMaxToolOutput*2)
	m.appendTool(0, huge)
	if got := len(m.blocks[0].output); got > tuiMaxToolOutput {
		t.Errorf("tool output len = %d, exceeds cap %d", got, tuiMaxToolOutput)
	}
	if !strings.HasSuffix(m.blocks[0].output, "x") {
		t.Error("tool output tail was lost after capping")
	}
}

// ─── subagent output cap (per block, shown by the modal) ───────────────────

func TestToolProgressCapsSubagentOutput(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width, m.height = 80, 24
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})
	m.openToolBlockModal(m.toolQueue[0])
	m.subagentModalScroll = 5
	for i := 0; i < 10; i++ {
		m.handleBlockMsg(tuiMsgBlock{
			kind:    "tool-progress",
			toolIdx: m.subagentToolIdx,
			content: strings.Repeat("z\n", tuiMaxToolOutput/8),
		})
	}
	if got := len(m.subagentModalText()); got > tuiMaxToolOutput {
		t.Errorf("modal content len = %d, exceeds cap %d", got, tuiMaxToolOutput)
	}
	// Capping drops lines from the top: the scroll offset must stay inside the
	// remaining text so the modal never renders an empty range.
	if maxScroll := m.subagentModalMaxScroll(); m.subagentModalScroll > maxScroll {
		t.Errorf("scroll %d exceeds max %d after capping", m.subagentModalScroll, maxScroll)
	}
}
