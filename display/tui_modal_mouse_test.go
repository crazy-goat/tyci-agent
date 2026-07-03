package display

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── Subagent modal: mouse event blocking ────────────────────────────────

func TestSubagentModal_MouseClick_DoesNotOpenToolModal(t *testing.T) {
	// Set up a model with a tool block behind the modal.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	// Add a "done" bash tool block so clicking on it would normally open a generic modal.
	m.blocks = append(m.blocks, block{kind: "tool", toolName: "bash", toolState: "done", content: "output"})
	m.toolQueue = append(m.toolQueue, 0)

	// Open subagent modal
	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true

	// Simulate a left-click at y=5 (would be on the background tool block)
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	// The subagent modal should still be active (not replaced by tool modal)
	if !m2.subagentModalActive {
		t.Fatal("subagent modal should still be active after click — clicks should be blocked")
	}
}

func TestSubagentModal_MouseRelease_DoesNotOpenToolModal(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.blocks = append(m.blocks, block{kind: "tool", toolName: "bash", toolState: "done", content: "output"})
	m.toolQueue = append(m.toolQueue, 0)

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true

	// Simulate a mouse release (click-release cycle)
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if !m2.subagentModalActive {
		t.Fatal("subagent modal should still be active after mouse release")
	}
}

func TestSubagentModal_MouseMotion_DoesNotOpenToolModal(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.blocks = append(m.blocks, block{kind: "tool", toolName: "bash", toolState: "done", content: "output"})

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true

	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if !m2.subagentModalActive {
		t.Fatal("subagent modal should still be active after mouse motion")
	}
}

func TestSubagentModal_WheelUp_ScrollsContent(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	// Fill with enough lines to make scrolling possible
	longContent := ""
	for i := 0; i < 100; i++ {
		longContent += "line\n"
	}
	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString(longContent)
	m.subagentModalDone = true
	m.subagentModalScroll = 0

	msg := tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if m2.subagentModalScroll <= 0 {
		t.Fatalf("expected scroll > 0 after wheel up, got %d", m2.subagentModalScroll)
	}
	if !m2.subagentModalActive {
		t.Fatal("modal should remain active after wheel up")
	}
}

func TestSubagentModal_WheelDown_ScrollsContent(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	longContent := ""
	for i := 0; i < 100; i++ {
		longContent += "line\n"
	}
	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString(longContent)
	m.subagentModalDone = true
	m.subagentModalScroll = 50 // scrolled up

	msg := tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if m2.subagentModalScroll >= 50 {
		t.Fatalf("expected scroll < 50 after wheel down, got %d", m2.subagentModalScroll)
	}
}

func TestSubagentModal_WheelDown_ClampsAtZero(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("short")
	m.subagentModalDone = true
	m.subagentModalScroll = 0

	msg := tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if m2.subagentModalScroll != 0 {
		t.Fatalf("expected scroll to clamp at 0, got %d", m2.subagentModalScroll)
	}
}

func TestSubagentModal_WheelUp_ClampsAtMax(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	longContent := ""
	for i := 0; i < 100; i++ {
		longContent += "line\n"
	}
	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString(longContent)
	m.subagentModalDone = true
	m.subagentModalScroll = m.subagentModalMaxScroll() // already at max

	msg := tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	maxScroll := m2.subagentModalMaxScroll()
	if m2.subagentModalScroll > maxScroll {
		t.Fatalf("scroll %d exceeded max %d", m2.subagentModalScroll, maxScroll)
	}
}

// ─── Model picker: mouse event blocking ──────────────────────────────────

var testProvidersForMouse = []ProviderModels{
	{Name: "openai", Models: []string{"gpt-4o", "gpt-4o-mini"}},
	{Name: "anthropic", Models: []string{"claude-sonnet-4-20250514", "claude-haiku"}},
}

func TestPicker_MouseClick_DoesNotLeak(t *testing.T) {
	m := newPickerTestModel(testProvidersForMouse, nil, "")
	m.openModelPicker()

	// Simulate a click — should be swallowed, picker stays open
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	result, _ := m.updatePicker(msg)
	m2 := result.(TuiModel)

	if !m2.pickerActive {
		t.Fatal("picker should remain active after mouse click — click should be blocked")
	}
}

func TestPicker_MouseRelease_DoesNotLeak(t *testing.T) {
	m := newPickerTestModel(testProvidersForMouse, nil, "")
	m.openModelPicker()

	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease}
	result, _ := m.updatePicker(msg)
	m2 := result.(TuiModel)

	if !m2.pickerActive {
		t.Fatal("picker should remain active after mouse release")
	}
}

func TestPicker_WheelUp_NavigatesUp(t *testing.T) {
	m := newPickerTestModel(testProvidersForMouse, nil, "")
	m.openModelPicker()
	m.pickerCursor = 2 // start somewhere in the middle

	msg := tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress}
	result, _ := m.updatePicker(msg)
	m2 := result.(TuiModel)

	if m2.pickerCursor != 1 {
		t.Fatalf("cursor = %d after wheel up, want 1", m2.pickerCursor)
	}
}

func TestPicker_WheelDown_NavigatesDown(t *testing.T) {
	m := newPickerTestModel(testProvidersForMouse, nil, "")
	m.openModelPicker()
	m.pickerCursor = 1

	msg := tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}
	result, _ := m.updatePicker(msg)
	m2 := result.(TuiModel)

	if m2.pickerCursor != 2 {
		t.Fatalf("cursor = %d after wheel down, want 2", m2.pickerCursor)
	}
}

func TestPicker_WheelUp_ClampsAtZero(t *testing.T) {
	m := newPickerTestModel(testProvidersForMouse, nil, "")
	m.openModelPicker()
	m.pickerCursor = 0

	msg := tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress}
	result, _ := m.updatePicker(msg)
	m2 := result.(TuiModel)

	if m2.pickerCursor != 0 {
		t.Fatalf("cursor = %d, should clamp at 0", m2.pickerCursor)
	}
}

func TestPicker_WheelDown_ClampsAtMax(t *testing.T) {
	m := newPickerTestModel(testProvidersForMouse, nil, "")
	m.openModelPicker()
	m.pickerCursor = m.pickerModelCount() - 1 // last model

	msg := tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}
	result, _ := m.updatePicker(msg)
	m2 := result.(TuiModel)

	if m2.pickerCursor != m2.pickerModelCount()-1 {
		t.Fatalf("cursor = %d, should clamp at %d", m2.pickerCursor, m2.pickerModelCount()-1)
	}
}

func TestPicker_WheelDown_StaysActive(t *testing.T) {
	m := newPickerTestModel(testProvidersForMouse, nil, "")
	m.openModelPicker()

	msg := tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}
	result, _ := m.updatePicker(msg)
	m2 := result.(TuiModel)

	if !m2.pickerActive {
		t.Fatal("picker should remain active after scroll wheel")
	}
}

// ─── Subagent modal: Update routing blocks mouse ─────────────────────────
// These tests go through the top-level Update() to verify the routing.

func TestSubagentModal_Update_MouseClickBlocked(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 120
	m.height = 40

	m.blocks = append(m.blocks, block{kind: "tool", toolName: "bash", toolState: "done", content: "output"})
	m.toolQueue = append(m.toolQueue, 0)

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true

	// Go through Update() to test routing
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	result, _ := m.Update(msg)
	m2 := result.(TuiModel)

	if !m2.subagentModalActive {
		t.Fatal("subagent modal should still be active after click through Update()")
	}
}

func TestPicker_Update_MouseClickBlocked(t *testing.T) {
	m := newPickerTestModel(testProvidersForMouse, nil, "")
	m.openModelPicker()

	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	result, _ := m.Update(msg)
	m2 := result.(TuiModel)

	if !m2.pickerActive {
		t.Fatal("picker should still be active after click through Update()")
	}
}
