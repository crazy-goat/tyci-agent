package display

import (
	"fmt"
	"testing"
)

// TestIncrementalTotalMatchesFullRecompute verifies that the streaming hot path
// in appendOrAppend, which updates cachedTotalLines incrementally instead of
// re-summing every block, produces the exact same total as a full recompute.
// This guards the CPU fix (avoid O(total blocks) work per streamed token)
// against silently drifting the line count and breaking scroll math.
func TestIncrementalTotalMatchesFullRecompute(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 60
	m.height = 40

	// Some prior history so the total is non-trivial.
	for i := 0; i < 20; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: fmt.Sprintf("agent line %d\n", i)})
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok"})
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.status = "responding"
	// Start a fresh streaming block.
	m.appendOrAppend("text", "start\n")

	chunks := []string{
		"tok ", "more ", "words\n", "a very long line that will soft wrap across the width boundary for sure ",
		"and more\n", "x", "y", "z\n", "\n", "final chunk",
	}
	for _, c := range chunks {
		m.appendOrAppend("text", c)
		got := m.totalRenderedLines() // uses the incremental cache
		m.cachedTotalLines = -1
		want := m.totalRenderedLines() // full recompute
		if got != want {
			t.Fatalf("after chunk %q: incremental total = %d, full recompute = %d", c, got, want)
		}
	}
}

// buildStreamModelWithHistory returns a model with nBlocks of cached history and
// an active streaming text block, used by the regression benchmark below.
func buildStreamModelWithHistory(nBlocks int) TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 100
	m.height = 40
	for i := 0; i < nBlocks; i++ {
		content := fmt.Sprintf("agent block %d\nwith a couple of lines\nand more text here", i)
		if i%2 == 0 {
			content = fmt.Sprintf("You: user block %d some question text", i)
		}
		m.blocks = append(m.blocks, block{kind: "text", content: content, dirty: true})
		m.dirtyBlocks[len(m.blocks)-1] = true
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.status = "idle"
	for i := range m.blocks {
		_ = m.getBlockLines(i, false)
	}
	_ = m.totalRenderedLines()
	m.status = "responding"
	m.blocks = append(m.blocks, block{kind: "text", content: "streaming ", dirty: true})
	m.dirtyBlocks[len(m.blocks)-1] = true
	return m
}

// BenchmarkStreamTokenTotal measures the per-streamed-token cost of maintaining
// the total-line cache as conversation length grows. With the incremental fix
// this stays flat with block count; the pre-fix full re-sum was O(total blocks)
// and drove idle/stream CPU up as context grew (1% → 10-20% at ~100k context).
func benchStreamTokenTotal(b *testing.B, nBlocks int) {
	m := buildStreamModelWithHistory(nBlocks)
	_ = m.totalRenderedLines()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		m.appendOrAppend("text", "tok ")
		_ = m.totalRenderedLines()
	}
}

func BenchmarkStreamTokenTotal10(b *testing.B)   { benchStreamTokenTotal(b, 10) }
func BenchmarkStreamTokenTotal1000(b *testing.B) { benchStreamTokenTotal(b, 1000) }
func BenchmarkStreamTokenTotal4000(b *testing.B) { benchStreamTokenTotal(b, 4000) }
