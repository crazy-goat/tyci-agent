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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
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
	m.selection = SelectionState{Active: true, AnchorY: 1, CursorY: 2}

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

	model, _ := m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 0})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 1, Y: 1})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 1, Y: 1})
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

	model, _ := m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 0})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 1, Y: 0})
	m = model.(TuiModel)
	if !m.subagentModalActive {
		t.Fatal("expected click on done tool to open modal")
	}

	copied := withClipboardStub(t)
	m = newSelectionTestModel()
	m.blocks = []block{{kind: "tool", toolName: "bash", toolState: "done", collapsed: true, content: "echo hi", output: "hi"}}
	m.invalidateAllBlockLineCounts()
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 0})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 2, Y: 0})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 2, Y: 0})
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
	m.selection = SelectionState{Active: true, AnchorY: layout.contentTop, CursorY: layout.contentTop + 1}

	m = m.copySelection()

	if *copied != "alpha\nbeta" {
		t.Fatalf("copied modal text mismatch: %q", *copied)
	}
	if !m.selectionFlash {
		t.Fatal("expected modal selection flash")
	}
}
