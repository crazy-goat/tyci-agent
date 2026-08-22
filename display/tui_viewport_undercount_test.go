package display

import (
	"fmt"
	"strings"
	"testing"
)

// TestResizeAfterFlushKeepsTotalLinesConsistent reproduces issue #30: a
// finished transcript sitting near the top of the viewport, followed by
// dozens of blank "viewport-pad" rows, even though the person is scrolled up
// into real history that should be visible.
//
// The path: a long conversation flushes its oldest blocks to the scrollback
// file (see tui_scrollback.go) to bound RAM. Each flushed block keeps its
// cachedLineCount, computed for the width it was flushed at. On resize,
// invalidateAllBlockLineCounts deliberately leaves flushed blocks' cached
// counts untouched (tui_scroll.go: "stay paged out; re-wrapped lazily on
// view") and only resets m.cachedTotalLines so the total gets recomputed.
//
// But totalRenderedLines()'s recompute (tui_scroll.go) reads a flushed
// block's *stale*, old-width cachedLineCount directly whenever it is
// non-zero — it only calls getBlockLines (which pages the block back in and
// re-wraps it for the *current* width) when cachedLineCount is exactly 0.
// Meanwhile buildAllFlatRenderLines/buildFlatRenderLinesInRange always call
// getBlockLines for every block, so they always see the correctly re-wrapped,
// current-width line count. After a resize that changes how many lines old
// flushed blocks wrap to, the two disagree: totalRenderedLines() reports a
// total based on the old width, the real renderer reports a total based on
// the new width, and the difference between them shows up as either missing
// real content or bogus viewport-pad rows.
func TestResizeAfterFlushKeepsTotalLinesConsistent(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 200
	m.height = 40
	m.status = "idle"

	// Build enough long-line "text" blocks, at a wide width, to blow past
	// the scrollback resident budget and force the oldest ones to flush.
	// Flushed at this wide width, each block's lines are individually
	// narrower than 200 cols but wider than the later, narrower resize
	// target — so narrowing the terminal will force them to re-wrap into
	// MORE lines than they were flushed with.
	longLine := strings.Repeat("word ", 200)
	for i := 0; i < 200; i++ {
		content := fmt.Sprintf("agent turn %d: %s", i, longLine)
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: content})
		// A tool call between turns forces a distinct block per turn — two
		// consecutive "text" kinds with nothing between them merge into one
		// block (appendOrAppend's streaming hot path), which wouldn't
		// exercise the per-block flush/resident bookkeeping this test needs.
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok"})
		m.handleBlockMsg(tuiMsgBlock{kind: "done"})
		m.status = "responding"
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	// Sanity check: some blocks actually got flushed to scrollback, otherwise
	// this test isn't exercising the path at all.
	flushedCount := 0
	for i := range m.blocks {
		if m.blocks[i].flushed {
			flushedCount++
		}
	}
	if flushedCount == 0 {
		t.Fatal("setup: expected some blocks to be flushed to scrollback; widen the test content")
	}

	// Resize much narrower: previously-flushed blocks will re-wrap to far
	// MORE lines than they were flushed at.
	m.width = 20
	m.invalidateAllBlockLineCounts()
	m.clampScroll()

	gotTotal := m.totalRenderedLines()
	realLines := m.buildAllFlatRenderLines()
	if gotTotal != len(realLines) {
		t.Fatalf("totalRenderedLines() = %d but buildAllFlatRenderLines() has %d real lines "+
			"(flushed=%d/%d blocks) — the cached total disagrees with what actually renders",
			gotTotal, len(realLines), flushedCount, len(m.blocks))
	}

	// Now scroll up, simulating the "↑N lines" in the reported screenshot,
	// and check the viewport isn't padded with blanks when real content
	// should fill it.
	msgHeight := 20
	m.atBottom = false
	m.scrollLine = 12
	m.clampScroll()

	rows := m.buildViewportRows(msgHeight)
	padCount := 0
	for _, r := range rows {
		if r.SourceKind == "viewport-pad" {
			padCount++
		}
	}
	if padCount > 0 && gotTotal >= msgHeight {
		t.Errorf("buildViewportRows returned %d viewport-pad rows out of %d while scrolled up "+
			"with totalRenderedLines()=%d >= msgHeight=%d — real content should fill the viewport",
			padCount, msgHeight, gotTotal, msgHeight)
	}
}

// checkTotalsAgree is the shared invariant used by the streaming-finalization
// scenarios below: totalRenderedLines() must always equal the number of real
// lines buildAllFlatRenderLines() actually produces.
func checkTotalsAgree(t *testing.T, m *TuiModel, label string) {
	t.Helper()
	gotTotal := m.totalRenderedLines()
	realLines := m.buildAllFlatRenderLines()
	if gotTotal != len(realLines) {
		t.Errorf("%s: totalRenderedLines() = %d but buildAllFlatRenderLines() has %d real lines",
			label, gotTotal, len(realLines))
	}
}

// markdownHeavyContent is streamed a token at a time as raw text, then
// finalized through forceRenderDirtyBlocks — glamour's rendering of headers,
// bullets and fenced code typically adds blank-line spacing that the raw
// streamed text didn't have, so the finalized line count usually differs
// from the streamed line count.
const markdownHeavyContent = "# Heading One\n\nSome intro text.\n\n" +
	"## Heading Two\n\n- bullet one\n- bullet two\n- bullet three\n\n" +
	"```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```\n\n" +
	"### Heading Three\n\nMore prose after the code block.\n"

func streamInChunks(m *TuiModel, kind, content string, chunkSize int) {
	for i := 0; i < len(content); i += chunkSize {
		end := i + chunkSize
		if end > len(content) {
			end = len(content)
		}
		m.handleBlockMsg(tuiMsgBlock{kind: kind, content: content[i:end]})
	}
}

// TestStreamedMarkdownFinalizationKeepsTotalLinesConsistent streams markdown
// whose glamour-rendered line count differs from its raw streamed line count,
// finalizes it (via a tool-start, matching the real flow), and checks the
// total-line bookkeeping never disagrees with the real render — including
// once the transcript is long enough to require scrolling.
func TestStreamedMarkdownFinalizationKeepsTotalLinesConsistent(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 60
	m.height = 40
	m.status = "responding"

	streamInChunks(&m, "text", markdownHeavyContent, 7)
	checkTotalsAgree(t, &m, "mid-stream, before finalization")

	// Finalize via tool-start, exactly like a real tool call arriving after
	// the model's prose.
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	checkTotalsAgree(t, &m, "immediately after finalization")
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	checkTotalsAgree(t, &m, "after done")

	// Add enough further turns that the transcript exceeds the viewport and
	// scroll up, simulating the reported "↑N lines" state.
	m.status = "responding"
	for i := 0; i < 10; i++ {
		streamInChunks(&m, "text", markdownHeavyContent, 11)
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok"})
		m.handleBlockMsg(tuiMsgBlock{kind: "done"})
		m.status = "responding"
	}
	checkTotalsAgree(t, &m, "after many turns")

	msgHeight := 15
	total := m.totalRenderedLines()
	if total <= msgHeight {
		t.Fatalf("setup: transcript (%d lines) must exceed msgHeight (%d) to test scrolling", total, msgHeight)
	}
	m.atBottom = false
	m.scrollLine = 12
	m.clampScroll()

	rows := m.buildViewportRows(msgHeight)
	for _, r := range rows {
		if r.SourceKind == "viewport-pad" {
			t.Errorf("buildViewportRows returned a viewport-pad row while scrolled up with "+
				"totalRenderedLines()=%d >= msgHeight=%d — real content should fill the viewport", total, msgHeight)
			break
		}
	}
}

// TestStreamedMarkdownFinalizationAcrossMultipleTurns specifically checks
// whether a stale total from one finalized turn leaks into the NEXT turn's
// streaming math — the user's screenshot was well into an ongoing session,
// not the first exchange, so a single-turn test might miss a bug that only
// shows up once a second streaming block starts after the first one's
// glamour line count has already diverged from its raw streamed count.
func TestStreamedMarkdownFinalizationAcrossMultipleTurns(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 60
	m.height = 40
	m.status = "responding"

	// Turn 1: stream markdown-heavy content and finalize it.
	streamInChunks(&m, "text", markdownHeavyContent, 9)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	checkTotalsAgree(t, &m, "after turn 1 finalized")

	// Turn 2: a fresh streaming block, checked after every chunk — this is
	// exactly what TestIncrementalTotalMatchesFullRecompute checks for the
	// incremental delta path, but starting from a model whose most recent
	// finalized block went through the raw->glamour line-count change,
	// which that other test does not exercise.
	m.status = "responding"
	chunks := []string{"# New turn\n\n", "- item a\n", "- item b\n\n", "some more ", "prose here.\n"}
	content := ""
	for _, c := range chunks {
		content += c
		m.appendOrAppend("text", c)
		checkTotalsAgree(t, &m, "turn 2, mid-stream chunk "+content)
	}

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	checkTotalsAgree(t, &m, "turn 2 finalized")
}
