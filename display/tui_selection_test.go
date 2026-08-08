package display

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func withClipboardStub(t *testing.T) *string {
	t.Helper()
	old := copyToClipboard
	var copied string
	copyToClipboard = func(text string) error {
		copied = text
		return nil
	}
	t.Cleanup(func() { copyToClipboard = old })
	return &copied
}

func newSelectionTestModel() TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	m.height = 20
	m.ready = true
	return m
}

func TestTuiSelection_CopyUsesRenderBuffer(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = RenderBuffer{Lines: []RenderLine{
		{PlainText: "zero", SourceKind: "text", Y: 0},
		{PlainText: "one", SourceKind: "text", Y: 1},
		{PlainText: "two", SourceKind: "text", Y: 2},
	}}
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 1, CursorX: 3, CursorY: 2}

	m = m.copySelection()

	if *copied != "one\ntwo" {
		t.Fatalf("copied text mismatch: %q", *copied)
	}
	if !m.selectionFlash {
		t.Fatal("expected selection flash after copy")
	}
	if !strings.Contains(m.statusMessage, "copied selection") {
		t.Fatalf("expected copied status, got %q", m.statusMessage)
	}
}

func TestTuiSelection_MouseReleaseCopiesSelection(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.blocks = []block{{kind: "text", content: "line one\nline two\nline three", dirty: true}}
	m.dirtyBlocks[0] = true
	m.invalidateAllBlockLineCounts()

	// Screen Y=1 is first message line (after fix for #87: adjY = msg.Y - 1)
	model, _ := m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 1})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 1, Y: 2})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 1, Y: 2})
	m = model.(TuiModel)

	if *copied == "" {
		t.Fatal("expected copied selection")
	}
	if !m.selectionFlash {
		t.Fatal("expected selection flash")
	}
}

func TestTuiSelection_ClickToolOpensModalButDragDoesNot(t *testing.T) {
	m := newSelectionTestModel()
	m.blocks = []block{{kind: "tool", toolName: "bash", toolState: "done", collapsed: true, content: "echo hi", output: "hi"}}
	m.invalidateAllBlockLineCounts()

	// Screen Y=1 is first message line (after fix for #87: adjY = msg.Y - 1)
	model, _ := m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 1})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 1, Y: 1})
	m = model.(TuiModel)
	if !m.subagentModalActive {
		t.Fatal("expected click on done tool to open modal")
	}

	copied := withClipboardStub(t)
	m = newSelectionTestModel()
	m.blocks = []block{{kind: "tool", toolName: "bash", toolState: "done", collapsed: true, content: "echo hi", output: "hi"}}
	m.invalidateAllBlockLineCounts()
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 1})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 8, Y: 1})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 8, Y: 1})
	m = model.(TuiModel)
	if m.subagentModalActive {
		t.Fatal("drag over tool should not open modal")
	}
	if *copied == "" {
		t.Fatal("expected drag over tool line to copy selection")
	}
}

func TestTuiSelection_ModalCopyUsesModalFallbackBuffer(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.subagentModalActive = true
	m.subagentModalContent.WriteString("alpha\nbeta\ngamma")
	layout := m.subagentModalLayout()
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: layout.contentTop, CursorX: 4, CursorY: layout.contentTop + 1}

	m = m.copySelection()

	if *copied != "alpha\nbeta" {
		t.Fatalf("copied modal text mismatch: %q", *copied)
	}
	if !m.selectionFlash {
		t.Fatal("expected modal selection flash")
	}
}

// ─── Off-by-one fix (issue #87) ──────────────────────────────────────────

func TestTuiSelection_TopBarClickIgnored(t *testing.T) {
	m := newSelectionTestModel()
	m.blocks = []block{{kind: "text", content: "line one", dirty: true}}
	m.dirtyBlocks[0] = true
	m.invalidateAllBlockLineCounts()

	// Click on screen Y=0 (top bar) — must not start a selection
	model, _ := m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 0})
	m = model.(TuiModel)

	if m.selection.Candidate || m.selection.Active {
		t.Fatal("click on top bar should not start a selection (issue #87)")
	}
}

func TestTuiSelection_FirstMessageLineSelectable(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.blocks = []block{{kind: "text", content: "first line content", dirty: true}}
	m.dirtyBlocks[0] = true
	m.invalidateAllBlockLineCounts()

	// Screen Y=1 is first message line (adjY=0)
	model, _ := m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 1})
	m = model.(TuiModel)
	if !m.selection.Candidate {
		t.Fatal("press on first message line should start selection candidate (issue #87)")
	}

	// Drag on the same line
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 10, Y: 1})
	m = model.(TuiModel)
	if !m.selection.Active {
		t.Fatal("motion on first line should promote to active selection (issue #87)")
	}

	// Release should copy
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 10, Y: 1})
	m = model.(TuiModel)
	if *copied == "" {
		t.Fatal("drag on first message line should copy text (issue #87)")
	}
}

func TestTuiSelection_ScreenYMapping(t *testing.T) {
	m := newSelectionTestModel()
	// Verify that selectionYRange returns 0, visibleLines()-1 (message-area coords)
	// after the fix: Y from mouse is adjusted at entry, so this range stays 0-based.
	start, end := m.selectionYRange()
	if start != 0 {
		t.Fatalf("selectionYRange start = %d, want 0 (issue #87)", start)
	}
	wantEnd := m.visibleLines() - 1
	if end != wantEnd {
		t.Fatalf("selectionYRange end = %d, want %d (issue #87)", end, wantEnd)
	}

	// transcriptY should reject top bar (Y=0 adjY=-1) and accept first message line (Y=1 adjY=0)
	if m.transcriptY(-1) {
		t.Fatal("transcriptY(-1) should be false (top bar row, issue #87)")
	}
	if !m.transcriptY(0) {
		t.Fatal("transcriptY(0) should be true (first message line, issue #87)")
	}
	if !m.transcriptY(end) {
		t.Fatal("transcriptY(end) should be true (last message line, issue #87)")
	}
	if m.transcriptY(end + 1) {
		t.Fatal("transcriptY(end+1) should be false (past last line, issue #87)")
	}

	// openToolModalAt should accept message-area Y in range [0, visibleLines()-1]
	// and reject negative values (which would come from top bar clicks).
	m.openToolModalAt(-1)               // should be no-op (top bar)
	m.openToolModalAt(m.visibleLines()) // should be no-op (past end)
	// No crash means success.
}

// TestBlockAtVisibleLine_SpacerReturnsMinusOne verifies that the fallback
// path in blockAtVisibleLine correctly identifies spacer lines (the blank
// line between a text block and a tool block) as belonging to no block.
// Before the fix, the spacer was attributed to the next block, so clicking
// the blank line above a tool opened that tool's modal (issue #87).
func TestBlockAtVisibleLine_SpacerReturnsMinusOne(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 40

	// text block (1 line) + spacer (1 line) + tool block (1 line)
	m.blocks = []block{
		{kind: "text", content: "hello", cachedLines: []string{"hello"}, cachedLineCount: 1},
		{kind: "tool", toolName: "bash", toolState: "done", content: "{}", output: "ok",
			cachedLines: []string{"tool line"}, cachedLineCount: 1},
	}
	m.cachedTotalLines = -1
	total := m.totalRenderedLines() // 1 (text) + 1 (spacer) + 1 (tool) = 3, minus trailing = 2? let's see

	// At bottom (scrollLine=0, atBottom=true): startLine = total - msgHeight.
	// For height=40, input.Height()=1, msgHeight = 40-1-2 = 37. total < msgHeight
	// so startLine=0. Line layout:
	//   visY 0 → text block line 0  (targetLine 0)
	//   visY 1 → spacer             (targetLine 1)
	//   visY 2 → tool block line 0  (targetLine 2)
	_ = total

	// text block
	if idx := m.blockAtVisibleLine(0); idx != 0 {
		t.Fatalf("blockAtVisibleLine(0) = %d, want 0 (text block)", idx)
	}
	// spacer — should be -1, NOT the tool block
	if idx := m.blockAtVisibleLine(1); idx != -1 {
		t.Fatalf("blockAtVisibleLine(1) = %d, want -1 (spacer line, issue #87)", idx)
	}
	// tool block
	if idx := m.blockAtVisibleLine(2); idx != 1 {
		t.Fatalf("blockAtVisibleLine(2) = %d, want 1 (tool block)", idx)
	}
}

// TestClickSpacerLineDoesNotOpenToolModal is an end-to-end test: clicking the
// blank spacer line between a text block and a tool block must NOT open the
// tool modal. Before the fix, the spacer was mapped to the next block (issue #87).
func TestClickSpacerLineDoesNotOpenToolModal(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 40

	m.blocks = []block{
		{kind: "text", content: "hello", cachedLines: []string{"hello"}, cachedLineCount: 1},
		{kind: "tool", toolName: "bash", toolState: "done", content: "{}", output: "ok",
			cachedLines: []string{"tool line"}, cachedLineCount: 1},
	}
	m.cachedTotalLines = -1

	// Layout: screen Y=1 → text (adjY=0), screen Y=2 → spacer (adjY=1), screen Y=3 → tool (adjY=2)
	// Click on spacer (screen Y=2)
	model, _ := m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 2})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 1, Y: 2})
	m = model.(TuiModel)

	if m.subagentModalActive {
		t.Fatal("clicking spacer line should NOT open tool modal (issue #87)")
	}

	// Sanity: clicking the actual tool line (screen Y=3) should open the modal
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 3})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 1, Y: 3})
	m = model.(TuiModel)

	if !m.subagentModalActive {
		t.Fatal("clicking tool line should open tool modal (issue #87)")
	}
}

// TestBlockAtVisibleLine_ConsecutiveToolsNoSpacer verifies that consecutive
// tool blocks (no spacer between them) are mapped correctly — no off-by-one.
func TestBlockAtVisibleLine_ConsecutiveToolsNoSpacer(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 40

	m.blocks = []block{
		{kind: "tool", toolName: "bash", toolState: "done", content: "{}", output: "ok",
			cachedLines: []string{"tool1"}, cachedLineCount: 1},
		{kind: "tool", toolName: "read", toolState: "done", content: "{}", output: "ok",
			cachedLines: []string{"tool2"}, cachedLineCount: 1},
	}
	m.cachedTotalLines = -1

	// No spacer between consecutive tools: visY 0 → tool1, visY 1 → tool2
	if idx := m.blockAtVisibleLine(0); idx != 0 {
		t.Fatalf("blockAtVisibleLine(0) = %d, want 0 (tool1)", idx)
	}
	if idx := m.blockAtVisibleLine(1); idx != 1 {
		t.Fatalf("blockAtVisibleLine(1) = %d, want 1 (tool2)", idx)
	}
}

// ─── Trailing-whitespace trimming (selectedText change) ─────────────────

// TestTuiSelection_TrailingSpacesTrimmed verifies that each selected line
// has trailing spaces and tabs stripped before joining (regression: the
// selectedText change added strings.TrimRight(…, " \t") per line).
func TestTuiSelection_TrailingSpacesTrimmed(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = RenderBuffer{Lines: []RenderLine{
		{PlainText: "hello     ", SourceKind: "text", Y: 0},
		{PlainText: "world\t\t", SourceKind: "text", Y: 1},
		{PlainText: "foo  bar", SourceKind: "text", Y: 2},
	}}
	// Select full width of all three lines.
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 100, CursorY: 2}

	m = m.copySelection()

	// Each line's trailing whitespace should be trimmed.
	if *copied != "hello\nworld\nfoo  bar" {
		t.Fatalf("expected trailing whitespace trimmed, got %q", *copied)
	}
}

// TestTuiSelection_TrailingWhitespaceInPartialSelection verifies that even
// when the selection starts/ends mid-line, the trailing part of each full
// selection row is still trimmed of spaces/tabs.
func TestTuiSelection_TrailingWhitespaceInPartialSelection(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = RenderBuffer{Lines: []RenderLine{
		{PlainText: "  leading", SourceKind: "text", Y: 0},
		{PlainText: "mid  ", SourceKind: "text", Y: 1},
		{PlainText: "trailing   ", SourceKind: "text", Y: 2},
	}}
	// Select from (2,0) to (5,2) — partial on first and last line.
	m.selection = SelectionState{Active: true, AnchorX: 2, AnchorY: 0, CursorX: 5, CursorY: 2}

	m = m.copySelection()

	// Line 0 (Y=0): "  leading" → cutCells("  leading", 2, width) = "leading" → TrimRight → "leading"
	// Line 1 (Y=1): "mid  " → cutCells("mid  ", 0, width) = "mid  " → TrimRight → "mid"
	// Line 2 (Y=2): "trailing   " → cutCells("trailing   ", 0, 5) = "trail" → TrimRight → "trail"
	want := "leading\nmid\ntrail"
	if *copied != want {
		t.Fatalf("got %q, want %q", *copied, want)
	}
}

// TestTuiSelection_NoTrailingWhitespace verifies that lines without trailing
// whitespace are not affected by the TrimRight change.
func TestTuiSelection_NoTrailingWhitespace(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = RenderBuffer{Lines: []RenderLine{
		{PlainText: "clean", SourceKind: "text", Y: 0},
		{PlainText: "lines", SourceKind: "text", Y: 1},
		{PlainText: "here", SourceKind: "text", Y: 2},
	}}
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 5, CursorY: 1}

	m = m.copySelection()

	if *copied != "clean\nlines" {
		t.Fatalf("unexpected copy: %q", *copied)
	}
}

// ─── Gutter stripping (selectedText change) ────────────────────────────

// TestTuiSelection_StripsTextGutter verifies that the "  " (2-space) prefix
// glamour adds to every text line is stripped when copying.
func TestTuiSelection_StripsTextGutter(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = RenderBuffer{Lines: []RenderLine{
		{PlainText: "  Hello world", SourceKind: "text", Y: 0},
		{PlainText: "  Line two", SourceKind: "text", Y: 1},
	}}
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 100, CursorY: 1}
	m = m.copySelection()
	if *copied != "Hello world\nLine two" {
		t.Fatalf("expected gutter stripped, got %q", *copied)
	}
}

// TestTuiSelection_StripsTextGutterPartialSelection verifies that coordinate
// adjustment works correctly when the user selects part of a line with gutter.
func TestTuiSelection_StripsTextGutterPartialSelection(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = RenderBuffer{Lines: []RenderLine{
		{PlainText: "  Hello world", SourceKind: "text", Y: 0},
	}}
	// Select from column 2 to column 7  →  "Hello " (after strip: col 0→5 = "Hello")
	m.selection = SelectionState{Active: true, AnchorX: 2, AnchorY: 0, CursorX: 7, CursorY: 0}
	m = m.copySelection()
	if *copied != "Hello" {
		t.Fatalf("expected 'Hello' after gutter adjustment, got %q", *copied)
	}
}

// TestTuiSelection_StripsThinkingGutter verifies that the "│ " prefix on
// thinking blocks is stripped when copying.
func TestTuiSelection_StripsThinkingGutter(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = RenderBuffer{Lines: []RenderLine{
		{PlainText: "│ thinking content", SourceKind: "thinking", Y: 0},
		{PlainText: "│ more thinking", SourceKind: "thinking", Y: 1},
	}}
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 100, CursorY: 1}
	m = m.copySelection()
	if *copied != "thinking content\nmore thinking" {
		t.Fatalf("expected '│ ' stripped, got %q", *copied)
	}
}

// TestTuiSelection_StripsToolGutter verifies that the "┃ tool " prefix on
// tool blocks is stripped when copying.
func TestTuiSelection_StripsToolGutter(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = RenderBuffer{Lines: []RenderLine{
		{PlainText: "┃ tool bash(Test) 153ms", SourceKind: "tool", Y: 0},
	}}
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 100, CursorY: 0}
	m = m.copySelection()
	if *copied != "bash(Test) 153ms" {
		t.Fatalf("expected '┃ tool ' stripped, got %q", *copied)
	}
}

// TestTuiSelection_StripsErrorGutter verifies that the "│ " prefix on error
// blocks is stripped when copying.
func TestTuiSelection_StripsErrorGutter(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = RenderBuffer{Lines: []RenderLine{
		{PlainText: "│ error message", SourceKind: "error", Y: 0},
	}}
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 100, CursorY: 0}
	m = m.copySelection()
	if *copied != "error message" {
		t.Fatalf("expected '│ ' stripped, got %q", *copied)
	}
}

// TestTuiSelection_StripsBlockGutter verifies that the "│ " prefix on info
// blocks is stripped when copying.
func TestTuiSelection_StripsBlockGutter(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = RenderBuffer{Lines: []RenderLine{
		{PlainText: "│ info block", SourceKind: "block", Y: 0},
	}}
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 100, CursorY: 0}
	m = m.copySelection()
	if *copied != "info block" {
		t.Fatalf("expected '│ ' stripped, got %q", *copied)
	}
}

// TestTuiSelection_DoesNotStripUserLine verifies that "user" source kind
// (which has no visual gutter) is copied as-is.
func TestTuiSelection_DoesNotStripUserLine(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = RenderBuffer{Lines: []RenderLine{
		{PlainText: "You: hello", SourceKind: "user", Y: 0},
	}}
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 100, CursorY: 0}
	m = m.copySelection()
	if *copied != "You: hello" {
		t.Fatalf("expected unchanged, got %q", *copied)
	}
}

// TestTuiSelection_TextLineWithoutGutter verifies that text lines that do
// NOT start with "  " (e.g. during streaming before glamour) are not affected.
func TestTuiSelection_TextLineWithoutGutter(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = RenderBuffer{Lines: []RenderLine{
		{PlainText: "No prefix here", SourceKind: "text", Y: 0},
	}}
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 100, CursorY: 0}
	m = m.copySelection()
	if *copied != "No prefix here" {
		t.Fatalf("expected unchanged, got %q", *copied)
	}
}
