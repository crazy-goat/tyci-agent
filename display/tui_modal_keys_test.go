package display

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSubagentModal_Y_CopiesFullBuffer(t *testing.T) {
	copied := withClipboardStub(t)
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("hello\nworld\nfoo")
	m.subagentModalDone = true

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if *copied != "hello\nworld\nfoo" {
		t.Fatalf("expected full buffer copied, got %q", *copied)
	}
	if !strings.Contains(m2.statusMessage, "copied modal") {
		t.Fatalf("expected 'copied modal' status, got %q", m2.statusMessage)
	}
	if !m2.subagentModalActive {
		t.Fatal("modal should remain active after y")
	}
}

func TestSubagentModal_Y_TrimsTrailingNewline(t *testing.T) {
	copied := withClipboardStub(t)
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("hello\nworld\n")
	m.subagentModalDone = true

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, _ := m.updateSubagentModal(msg)
	_ = result.(TuiModel)

	if strings.HasSuffix(*copied, "\n") {
		t.Fatalf("copied text should not have trailing newline, got %q", *copied)
	}
	if *copied != "hello\nworld" {
		t.Fatalf("expected 'hello\\nworld', got %q", *copied)
	}
}

func TestSubagentModal_Y_EmptyBuffer_ReportsNothingToCopy(t *testing.T) {
	copied := withClipboardStub(t)
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("")
	m.subagentModalDone = true

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if *copied != "" {
		t.Fatalf("should not copy empty buffer, got %q", *copied)
	}
	if m2.statusMessage != "nothing to copy" {
		t.Fatalf("expected 'nothing to copy', got %q", m2.statusMessage)
	}
}

func TestSubagentModal_Y_WhitespaceOnlyBuffer_ReportsNothingToCopy(t *testing.T) {
	copied := withClipboardStub(t)
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("   \n  \n")
	m.subagentModalDone = true

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if *copied != "" {
		t.Fatalf("should not copy whitespace-only buffer, got %q", *copied)
	}
	if m2.statusMessage != "nothing to copy" {
		t.Fatalf("expected 'nothing to copy', got %q", m2.statusMessage)
	}
}

func TestSubagentModal_Y_DoesNotClearSelection(t *testing.T) {
	copied := withClipboardStub(t)
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("alpha\nbeta\ngamma")
	m.subagentModalDone = true
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 3, CursorY: 0}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if !m2.selection.Active {
		t.Fatal("selection should remain active after y (y copies full buffer, not selection)")
	}
	if *copied != "alpha\nbeta\ngamma" {
		t.Fatalf("expected full buffer copied, got %q", *copied)
	}
}

func TestSubagentModal_Y_BufferWithMultipleTrailingNewlines(t *testing.T) {
	copied := withClipboardStub(t)
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("line1\nline2\n\n\n")
	m.subagentModalDone = true

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, _ := m.updateSubagentModal(msg)
	_ = result.(TuiModel)

	if strings.HasSuffix(*copied, "\n") {
		t.Fatalf("copied text should have no trailing newlines, got %q", *copied)
	}
	if *copied != "line1\nline2" {
		t.Fatalf("expected 'line1\\nline2', got %q", *copied)
	}
}

func TestSubagentModal_Y_ReportLineCount(t *testing.T) {
	copied := withClipboardStub(t)
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("a\nb\nc\nd\ne")
	m.subagentModalDone = true

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if *copied != "a\nb\nc\nd\ne" {
		t.Fatalf("expected full buffer, got %q", *copied)
	}
	if !strings.Contains(m2.statusMessage, "5 lines") {
		t.Fatalf("expected '5 lines' in status, got %q", m2.statusMessage)
	}
}

func TestSubagentModal_Y_NotActiveOnMainView(t *testing.T) {
	// When modal is NOT active, pressing 'y' should NOT trigger modal copy.
	// It should fall through to the main key handler.
	copied := withClipboardStub(t)
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40
	m.reading = true

	// Fill the modal buffer (even though it's not active)
	m.subagentModalContent.WriteString("modal content")

	// Press 'y' through the top-level Update (not through updateSubagentModal)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, _ := m.Update(msg)
	_ = result.(TuiModel)

	// copyToClipboard should NOT have been called
	if *copied != "" {
		t.Fatalf("y on main view should not copy modal buffer, but copied %q", *copied)
	}
}

func TestSubagentModal_Y_ModalStillStreaming(t *testing.T) {
	copied := withClipboardStub(t)
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("partial output so far")
	m.subagentModalDone = false // still streaming

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if *copied != "partial output so far" {
		t.Fatalf("expected partial output copied, got %q", *copied)
	}
	if !strings.Contains(m2.statusMessage, "copied modal") {
		t.Fatalf("expected 'copied modal' status, got %q", m2.statusMessage)
	}
}

func TestSubagentModal_FooterContainsYCopyAll(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("some content here\nline 2\nline 3")
	m.subagentModalDone = true

	view := m.renderSubagentModalView()
	stripped := stripANSI(view)

	if !strings.Contains(stripped, "y copy all") {
		t.Fatalf("modal footer should contain 'y copy all', got:\n%s", stripped)
	}
}
