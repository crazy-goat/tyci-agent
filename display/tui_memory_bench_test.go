package display

import (
	"fmt"
	"strings"
	"testing"
)

// ─── Memory benchmarks: long-session heap residency ───────────────────────

// BenchmarkScrollbackLongSession measures the steady-state cost of appending a
// block at the resident-budget cap, when the oldest blocks are being flushed to
// the scrollback file on every append. The cost is O(flushed), not O(total
// history), and history (block count) keeps growing — only the heavy rendered
// content is paged out, not dropped.
func BenchmarkScrollbackLongSession(b *testing.B) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 100
	m.height = 40

	// Fill past the 256 KiB resident budget so eviction is active on every
	// subsequent append. Each block is ~32 KiB of rendered content.
	big := strings.Repeat("line of content here\n", 1600)
	for i := 0; i < 16; i++ {
		if i%2 == 0 {
			m.appendOrAppend("text", "You: "+fmt.Sprintf("%d", i)+" "+big)
		} else {
			m.appendOrAppend("text", "agent "+fmt.Sprintf("%d", i)+" "+big)
		}
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	b.ResetTimer()
	b.ReportAllocs()

	for n := 0; n < b.N; n++ {
		// Each iteration adds a block at the cap, triggering eviction+flush,
		// and renders. History grows; resident memory stays bounded.
		m.appendOrAppend("text", "extra block "+fmt.Sprintf("%d", n)+" "+big)
		_ = m.View()
	}
}

// BenchmarkScrollbackPageInOnScroll measures the cost of paging an old block
// back from the scrollback file when the viewport scrolls up to it. This is the
// user-perceived cost of scrolling into history.
func BenchmarkScrollbackPageInOnScroll(b *testing.B) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 100
	m.height = 40

	big := strings.Repeat("line of content here\n", 1600)
	for i := 0; i < 16; i++ {
		if i%2 == 0 {
			m.appendOrAppend("text", "You: "+fmt.Sprintf("%d", i)+" "+big)
		} else {
			m.appendOrAppend("text", "agent "+fmt.Sprintf("%d", i)+" "+big)
		}
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	// Force the oldest block to be flushed.
	m.maybeFlushOldBlocks()
	if !m.blocks[0].flushed {
		b.Fatal("setup: expected block 0 flushed")
	}

	b.ResetTimer()
	b.ReportAllocs()

	for n := 0; n < b.N; n++ {
		// Re-flush block 0 so each iteration pages it in fresh.
		m.scrollback.flushBlock(&m.blocks[0], m.width)
		_ = m.getBlockLines(0, false)
	}
}

// BenchmarkScrollbackToolOutput measures residency when tools emit large
// outputs. The per-block .output cap bounds each block to tuiMaxToolOutput.
func BenchmarkScrollbackToolOutput(b *testing.B) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 100
	m.height = 40

	for i := 0; i < 100; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
		m.appendTool(len(m.toolQueue)-1, strings.Repeat("x", tuiMaxToolOutput/4))
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done"})
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
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
