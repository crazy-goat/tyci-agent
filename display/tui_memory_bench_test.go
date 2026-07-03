package display

import (
	"fmt"
	"strings"
	"testing"
)

// ─── Memory benchmarks: long-session heap residency ───────────────────────

// BenchmarkLongSessionBlocks measures the heap retained by a long conversation
// capped at tuiMaxHistory blocks. Without compaction, m.blocks (and its cache
// maps) grow without bound; with compaction, residency is bounded by the cap.
// Run with -benchmem to see the per-run alloc delta; the more meaningful
// number is the steady-state heap, which this benchmark approximates by
// building the session once and then measuring the cost of an additional
// block+render cycle at the cap.
func BenchmarkLongSessionBlocks(b *testing.B) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 100
	m.height = 40

	// Fill past the cap so compaction is active on every subsequent append.
	for i := 0; i < tuiMaxHistory+50; i++ {
		kind := "text"
		if i%3 == 0 {
			kind = "thinking"
		}
		m.appendOrAppend("text", fmt.Sprintf("block %d content\n", i))
		if i%5 == 0 {
			kind = "tool"
		}
		_ = kind
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	b.ResetTimer()
	b.ReportAllocs()

	for n := 0; n < b.N; n++ {
		// Each iteration adds a block at the cap, triggering compaction, and
		// renders. The cost is O(cap), not O(total history).
		m.appendOrAppend("text", "extra block content to force compaction\n")
		_ = m.View()
	}
}

// BenchmarkLongSessionWithToolOutput measures residency when tools emit large
// outputs (the worst case for memory). The tool output cap bounds each block's
// .output to tuiMaxToolOutput; without it a single bash call can hold megabytes.
func BenchmarkLongSessionWithToolOutput(b *testing.B) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 100
	m.height = 40

	// Simulate many tool calls each producing a large output.
	for i := 0; i < 100; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
		// Stream a chunk of output to the tool block.
		m.appendTool(len(m.toolQueue)-1, strings.Repeat("x", tuiMaxToolOutput/4))
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done"})
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	// Total tool output resident should be ~100 * tuiMaxToolOutput/4 capped
	// per-block, not unbounded.
	b.ResetTimer()
	b.ReportAllocs()

	for n := 0; n < b.N; n++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
		m.appendTool(len(m.toolQueue)-1, strings.Repeat("y", tuiMaxToolOutput/2))
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done"})
		_ = m.View()
	}
}

// BenchmarkSubagentModalBuffer measures the cost of streaming into the
// subagent modal accumulator past the cap. Without capModalBuffer the builder
// grows unbounded; with it, residency is bounded by tuiMaxModalBuffer.
func BenchmarkSubagentModalBuffer(b *testing.B) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 100
	m.height = 40

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})

	b.ResetTimer()
	b.ReportAllocs()

	chunk := strings.Repeat("z", 4096)
	for n := 0; n < b.N; n++ {
		m.handleBlockMsg(tuiMsgBlock{
			kind:    "tool-progress",
			toolIdx: m.subagentModalToolIdx,
			content: chunk,
		})
	}
}
