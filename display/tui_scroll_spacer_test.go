package display

import "testing"

// The line-count functions in tui_scroll.go and the flat-line builders in
// tui_render_buffer.go must agree exactly. When they disagree the viewport
// scrolls past the end of the transcript and every row comes back as padding
// — a blank screen, with nothing that looks like an error. A thinking block
// next to a tool block is the case that broke it: the builders learned that
// the pair packs without a spacer, these two did not.
func TestTotalRenderedLines_AgreesWithTheFlatLineBuilder(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width, m.height = 80, 24

	// Six thinking/tool pairs — the shape a real turn produces.
	for i := 0; i < 6; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "weighing the options here"})
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", toolName: "read", content: "ok"})
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "and here is the answer"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.forceRenderDirtyBlocks()
	m.invalidateTotalLines()

	want := len(m.buildAllFlatRenderLines())
	if got := m.totalRenderedLines(); got != want {
		t.Fatalf("totalRenderedLines = %d, flat builder produced %d lines", got, want)
	}
}

// blockAtVisibleLine walks the same accounting; a mismatch there sends a
// click to the wrong block rather than blanking the screen.
func TestBlockAtVisibleLine_LandsOnTheRightBlockAcrossACompactRun(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width, m.height = 80, 24
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "first thought"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "find"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", toolName: "find", content: "ok"})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "answer"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.forceRenderDirtyBlocks()
	m.invalidateTotalLines()

	flat := m.buildAllFlatRenderLines()
	for line, fl := range flat {
		got := m.blockAtVisibleLine(line)
		want := fl.BlockIndex
		if fl.SourceKind == "spacer" {
			want = -1
		}
		if got != want {
			t.Fatalf("line %d (%s): blockAtVisibleLine = %d, flat builder says %d",
				line, fl.SourceKind, got, want)
		}
	}
}

// A single block must not be reported as occupying its lines plus a spacer:
// there is nothing after it to separate from.
func TestTotalRenderedLines_NoSpacerAfterTheLastBlock(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width, m.height = 80, 24
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "one line"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.forceRenderDirtyBlocks()
	m.invalidateTotalLines()

	if got, want := m.totalRenderedLines(), len(m.buildAllFlatRenderLines()); got != want {
		t.Fatalf("totalRenderedLines = %d, want %d", got, want)
	}
}
