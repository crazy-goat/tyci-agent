package display

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newModalTestModel builds a model sized for a modal with a usable content area.
func newModalTestModel() TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width, m.height = 100, 40
	return m
}

// A second subagent starting must not wipe what the open modal shows.
func TestModal_NewSubagentStart_KeepsViewedContent(t *testing.T) {
	m := newModalTestModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})
	first := m.toolQueue[0]
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "agent A line\n"})
	m.openToolBlockModal(first)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "A done"})

	// Model fires off a second subagent while the user reads the first one.
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})

	if m.subagentModalBlockIdx != first {
		t.Fatalf("modal should still view block %d, got %d", first, m.subagentModalBlockIdx)
	}
	if got := m.subagentModalText(); got != "agent A line\n" {
		t.Errorf("modal lost the content it was showing: %q", got)
	}

	// Second subagent's stream must land in its own block, not in the viewed one.
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "agent B line\n"})
	if got := m.subagentModalText(); got != "agent A line\n" {
		t.Errorf("second subagent bled into the viewed block: %q", got)
	}
	if got := m.blocks[m.toolQueue[0]].output; got != "agent B line\n" {
		t.Errorf("second subagent output = %q", got)
	}
}

// ESC then reopening a still-running subagent must show the stream continuing.
func TestModal_ReopenRunningSubagent_ShowsContinuedStream(t *testing.T) {
	m := newModalTestModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})
	bidx := m.toolQueue[0]
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "before close\n"})
	m.openToolBlockModal(bidx)

	updated, _ := m.updateSubagentModal(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(TuiModel)
	if m.subagentModalActive {
		t.Fatal("ESC should close the modal")
	}

	// The subagent keeps streaming while the modal is closed.
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "after close\n"})

	m.openToolBlockModal(bidx)
	got := m.subagentModalText()
	if !strings.Contains(got, "before close") || !strings.Contains(got, "after close") {
		t.Errorf("reopened modal should show the whole stream, got %q", got)
	}
	if m.subagentModalDone {
		t.Error("reopened modal should report the subagent as still running")
	}

	// Further progress still reaches the reopened modal.
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "after reopen\n"})
	if !strings.Contains(m.subagentModalText(), "after reopen") {
		t.Errorf("reopened modal stopped receiving the stream: %q", m.subagentModalText())
	}
}

// Opening another tool's output must not touch the subagent's buffer.
func TestModal_OpenOtherToolBlock_KeepsSubagentBuffer(t *testing.T) {
	m := newModalTestModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})
	subIdx := m.toolQueue[0]
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	bashIdx := m.toolQueue[1]
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "subagent working\n"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 1, content: "bash working\n"})
	m.openToolBlockModal(subIdx)

	m.blocks[bashIdx].toolState = "done"
	m.openToolBlockModal(bashIdx)
	if got := m.subagentModalText(); got != "bash working\n" {
		t.Errorf("modal should show the bash block, got %q", got)
	}
	if got := m.blocks[subIdx].output; got != "subagent working\n" {
		t.Errorf("subagent buffer was clobbered by the bash modal: %q", got)
	}

	// The subagent keeps streaming into its own block while bash is on screen.
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "still working\n"})
	if got := m.subagentModalText(); got != "bash working\n" {
		t.Errorf("subagent stream leaked into the bash modal: %q", got)
	}
	m.openToolBlockModal(subIdx)
	if got := m.subagentModalText(); got != "subagent working\nstill working\n" {
		t.Errorf("subagent history incomplete after switching back: %q", got)
	}
}

// A manually scrolled viewport stays on the text the user is reading.
func TestModal_ScrolledViewportStaysAnchored(t *testing.T) {
	m := newModalTestModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: strings.Repeat("old\n", 200)})
	m.openToolBlockModal(m.toolQueue[0])
	m.subagentModalScroll = 100

	before := m.visibleModalRenderBufferSnapshot()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: strings.Repeat("new\n", 20)})
	after := m.visibleModalRenderBufferSnapshot()

	if len(before.Lines) != len(after.Lines) {
		t.Fatalf("visible line counts differ: %d vs %d", len(before.Lines), len(after.Lines))
	}
	for i := range before.Lines {
		if before.Lines[i].Text != after.Lines[i].Text {
			t.Fatalf("visible line %d changed from %q to %q while scrolled up",
				i, before.Lines[i].Text, after.Lines[i].Text)
		}
	}
}

// The block the modal shows must stay resident: flushing drops .output.
func TestModal_ViewedBlockIsNotFlushed(t *testing.T) {
	m := newModalTestModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "kept output"})
	m.forceRenderDirtyBlocks()
	_ = m.getBlockLines(0, false)
	m.openToolBlockModal(0)

	// Grow the transcript past the resident budget so eviction runs.
	for i := 0; i < 200; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "block", content: strings.Repeat("filler ", 200)})
		m.forceRenderDirtyBlocks()
	}
	m.maybeFlushOldBlocks()

	if m.blocks[0].flushed {
		t.Error("the block on screen was flushed, wiping the modal content")
	}
	if got := m.subagentModalText(); got != "kept output" {
		t.Errorf("modal content = %q, want %q", got, "kept output")
	}
	m.scrollback.close()
}
