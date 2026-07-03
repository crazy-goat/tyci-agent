package display

import (
	"strings"
	"testing"
)

// ─── scrollback compaction ────────────────────────────────────────────────

func TestCompactBlocksDropsOldestAndReindexesCaches(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 80
	m.height = 24

	// Build 5 distinct blocks by alternating user/agent text (appendOrAppend
	// only merges same-source blocks, so alternating forces new blocks).
	for i := 0; i < 5; i++ {
		if i%2 == 0 {
			m.appendOrAppend("text", "You: turn "+string(rune('A'+i)))
		} else {
			m.appendOrAppend("text", "agent "+string(rune('A'+i)))
		}
	}
	if got := len(m.blocks); got != 5 {
		t.Fatalf("setup: len(blocks) = %d, want 5", got)
	}
	// Add a tool block at index 5 and put it on the queue so we can verify the
	// queue is reindexed too.
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	if got := len(m.blocks); got != 6 {
		t.Fatalf("setup: len(blocks) after tool = %d, want 6", got)
	}
	queueIdx := len(m.toolQueue) - 1
	origBlockIdx := m.toolQueue[queueIdx]

	// Prime caches AFTER tool-start so forceRenderDirtyBlocks (called by
	// tool-start) doesn't wipe them. mdCacheRendered and toolDisplayCache
	// survive forceRenderDirtyBlocks for the blocks we care about; we also
	// seed dirtyBlocks/streamWraps directly to verify they reindex.
	for i := range m.blocks {
		m.mdCacheRendered[i] = "cached-" + string(rune('A'+i))
		m.toolDisplayCache[i] = "tool-" + string(rune('A'+i))
		m.streamWraps[i] = &streamWrap{}
		m.dirtyBlocks[i] = true
	}

	// Compact to 3 blocks: drops indices 0..2 (3 dropped), keeps [3,4,5].
	m.compactBlocks(3)

	if got := len(m.blocks); got != 3 {
		t.Fatalf("len(blocks) = %d, want 3", got)
	}
	// The block that was at index 3 ("agent D") is now at index 0.
	if got, want := m.blocks[0].content, "agent D"; got != want {
		t.Errorf("blocks[0].content = %q, want %q", got, want)
	}
	// Caches for evicted blocks (indices 0..2) must be gone; the entry that was
	// at index 3 must have moved to key 0.
	if v, ok := m.mdCacheRendered[0]; !ok || v != "cached-D" {
		t.Errorf("mdCacheRendered[0] = %q, ok=%v, want cached-D", v, ok)
	}
	if _, ok := m.mdCacheRendered[3]; ok {
		t.Error("mdCacheRendered[3] still present after compaction (evicted block)")
	}
	if v, ok := m.toolDisplayCache[0]; !ok || v != "tool-D" {
		t.Errorf("toolDisplayCache[0] = %q, ok=%v, want tool-D", v, ok)
	}
	if _, ok := m.streamWraps[3]; ok {
		t.Error("streamWraps[3] still present after compaction")
	}
	if _, ok := m.streamWraps[0]; !ok {
		t.Error("streamWraps[0] missing after compaction (should have shifted from 3)")
	}
	// dirtyBlocks should be reindexed too.
	if !m.dirtyBlocks[0] {
		t.Error("dirtyBlocks[0] missing after compaction")
	}
	if _, ok := m.dirtyBlocks[3]; ok {
		t.Error("dirtyBlocks[3] still present after compaction")
	}
	// The tool queue entry should point to the shifted block index.
	newBlockIdx := m.toolQueue[queueIdx]
	if newBlockIdx != origBlockIdx-3 {
		t.Errorf("toolQueue block index = %d, want %d (shifted by -3)", newBlockIdx, origBlockIdx-3)
	}
	// Total line cache must be invalidated so it recomputes.
	if m.cachedTotalLines >= 0 {
		t.Error("cachedTotalLines should be invalidated after compaction")
	}
}

func TestCompactBlocksNoOpWhenUnderLimit(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "hello"})
	before := len(m.blocks)
	m.compactBlocks(100)
	if got := len(m.blocks); got != before {
		t.Errorf("len(blocks) = %d, want %d (no-op under limit)", got, before)
	}
}

func TestAppendOrAppendCompactsPastHistoryCap(t *testing.T) {
	// Drive appendOrAppend past tuiMaxHistory by creating many distinct blocks
	// (alternating user/agent so they don't merge). Verify the block count is
	// capped and caches don't leak entries for evicted indices.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 80
	m.height = 24

	// Each pair creates two blocks (user "You: ..." then agent text).
	for i := 0; i < tuiMaxHistory+50; i++ {
		// alternate so last.kind != kind forces new block
		if i%2 == 0 {
			m.appendOrAppend("text", "You: turn "+itoa(i))
		} else {
			m.appendOrAppend("text", "agent "+itoa(i))
		}
	}
	if got := len(m.blocks); got > tuiMaxHistory {
		t.Errorf("len(blocks) = %d, exceeds cap %d", got, tuiMaxHistory)
	}
	// No cache map should have keys beyond len(blocks)-1.
	for k := range m.mdCacheRendered {
		if k >= len(m.blocks) {
			t.Errorf("mdCacheRendered has stale key %d >= len(blocks) %d", k, len(m.blocks))
		}
	}
	for k := range m.toolDisplayCache {
		if k >= len(m.blocks) {
			t.Errorf("toolDisplayCache has stale key %d >= len(blocks) %d", k, len(m.blocks))
		}
	}
	for k := range m.streamWraps {
		if k >= len(m.blocks) {
			t.Errorf("streamWraps has stale key %d >= len(blocks) %d", k, len(m.blocks))
		}
	}
}

// itoa is a tiny strconv.Itoa replacement to avoid the import in this test.
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

// ─── tool output cap ─────────────────────────────────────────────────────

func TestCapToolOutputKeepsTail(t *testing.T) {
	// Under the cap: unchanged.
	s := "short"
	if got := capToolOutput(s, 1<<20); got != s {
		t.Errorf("capToolOutput under cap changed the string")
	}
	// Over the cap: tail kept, and trimmed to a line boundary when possible.
	big := strings.Repeat("line\n", 100000) // ~500KB * 5 = 2.5MB
	got := capToolOutput(big, 1<<20)
	if len(got) > 1<<20 {
		t.Errorf("capped output len = %d, want <= %d", len(got), 1<<20)
	}
	if !strings.HasPrefix(got, "line\n") {
		t.Errorf("capped output should start at a line boundary, got %q...", got[:20])
	}
	// The tail content is preserved (last line is still there).
	if !strings.HasSuffix(got, "line\n") {
		t.Errorf("capped output lost the tail, got %q...", got[len(got)-20:])
	}
}

func TestAppendToolCapsOutput(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	// Stream a huge amount of output to the tool block via appendTool.
	huge := strings.Repeat("x", tuiMaxToolOutput*2)
	m.appendTool(0, huge)
	if got := len(m.blocks[0].output); got > tuiMaxToolOutput {
		t.Errorf("tool output len = %d, exceeds cap %d", got, tuiMaxToolOutput)
	}
	// The tail (most recent 'x's) must be preserved.
	if !strings.HasSuffix(m.blocks[0].output, "x") {
		t.Error("tool output tail was lost after capping")
	}
}

// ─── subagent modal buffer cap ────────────────────────────────────────────

func TestCapModalBufferKeepsTail(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Repeat("y", tuiMaxModalBuffer*2))
	capModalBuffer(&b, tuiMaxModalBuffer)
	if got := b.Len(); got > tuiMaxModalBuffer {
		t.Errorf("modal buffer len = %d, exceeds cap %d", got, tuiMaxModalBuffer)
	}
	// Empty/small builder is a no-op.
	var b2 strings.Builder
	b2.WriteString("small")
	capModalBuffer(&b2, tuiMaxModalBuffer)
	if b2.String() != "small" {
		t.Errorf("small buffer changed to %q", b2.String())
	}
	// nil is safe.
	capModalBuffer(nil, 1<<20)
}

func TestToolProgressCapsModalBuffer(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})
	// Send progress messages that together exceed the cap.
	for i := 0; i < 10; i++ {
		m.handleBlockMsg(tuiMsgBlock{
			kind:    "tool-progress",
			toolIdx: m.subagentModalToolIdx,
			content: strings.Repeat("z", tuiMaxModalBuffer/4),
		})
	}
	if got := m.subagentModalContent.Len(); got > tuiMaxModalBuffer {
		t.Errorf("modal content len = %d, exceeds cap %d", got, tuiMaxModalBuffer)
	}
}
