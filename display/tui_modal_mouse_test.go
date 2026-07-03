package display

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── Subagent modal: mouse event blocking ────────────────────────────────

func TestSubagentModal_OutsideClick_ClosesModal(t *testing.T) {
	// Set up a model with a tool block behind the modal.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
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
	m.savedAtBottom = true
	m.savedScrollLine = 42

	// Click at (0, 0) — outside the modal body (modal at 120x40 starts at left=6, top=3)
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: 0}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if m2.subagentModalActive {
		t.Fatal("subagent modal should be closed after outside click")
	}
	// Verify scroll state restored
	if !m2.atBottom {
		t.Fatal("atBottom should be restored to savedAtBottom value")
	}
	if m2.scrollLine != 42 {
		t.Fatalf("scrollLine should be restored to 42, got %d", m2.scrollLine)
	}
}

func TestSubagentModal_InsideBodyClick_DoesNotClose(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 120
	m.height = 40

	// Modal at 120x40: left=6, top=3, popupWidth=108, boxHeight=34
	// Content area: x=[6,114), y=[5,34] — click in the middle
	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true

	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 50, Y: 20}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if !m2.subagentModalActive {
		t.Fatal("subagent modal should remain active after click inside body")
	}
}

func TestSubagentModal_TitleBarClick_DoesNotClose(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 120
	m.height = 40

	// Modal at 120x40: left=6, top=3 — click on the title bar (top row of modal)
	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true

	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 50, Y: 3}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if !m2.subagentModalActive {
		t.Fatal("subagent modal should remain active after click on title bar")
	}
}

func TestSubagentModal_MouseMotion_DoesNotClose(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true

	// Motion outside modal — should NOT close (only press closes)
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 0, Y: 0}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if !m2.subagentModalActive {
		t.Fatal("modal should remain active after mouse motion")
	}
}

func TestSubagentModal_MouseRelease_DoesNotClose(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true

	// Release outside modal — should NOT close (we dismiss on press, not release)
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 0, Y: 0}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if !m2.subagentModalActive {
		t.Fatal("modal should remain active after mouse release")
	}
}

func TestSubagentModal_OutsideClick_NoLeakToBackgroundBlocks(t *testing.T) {
	// Regression: clicking outside the modal must NOT leak to background
	// tool block handlers (issue #75 property).
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 120
	m.height = 40

	m.blocks = append(m.blocks, block{kind: "tool", toolName: "bash", toolState: "done", content: "output"})
	m.toolQueue = append(m.toolQueue, 0)

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true

	// Click outside — modal closes, but the click should NOT have leaked
	// to the block-click handler (it was consumed by the modal handler).
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: 0}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if m2.subagentModalActive {
		t.Fatal("modal should be closed")
	}
	// The click was consumed — no tool modal should have been opened.
	// (If it leaked, openToolModalAt would have set subagentModalActive=true again)
}

func TestSubagentModal_OutsideClick_ClearsSelection(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 5, CursorX: 10, CursorY: 5}

	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: 0}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if m2.selection.Active {
		t.Fatal("selection should be cleared on modal close")
	}
}

func TestSubagentModal_OutsideClick_Idempotent(t *testing.T) {
	// Closing an already-closed modal should be a no-op.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 120
	m.height = 40

	// Modal is NOT active — simulate what happens if a stale press arrives
	m.subagentModalActive = false

	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: 0}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if m2.subagentModalActive {
		t.Fatal("modal should remain inactive (idempotent close)")
	}
}

func TestSubagentModal_StillStreaming_OutsideClickStillCloses(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 120
	m.height = 40

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("partial output")
	m.subagentModalDone = false // still streaming

	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: 0}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if m2.subagentModalActive {
		t.Fatal("modal should close on outside click even while streaming")
	}
}

func TestSubagentModal_WheelUp_ScrollsContent(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 120
	m.height = 40

	m.blocks = append(m.blocks, block{kind: "tool", toolName: "bash", toolState: "done", content: "output"})
	m.toolQueue = append(m.toolQueue, 0)

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true

	// Click inside the modal body through Update() — should NOT leak
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 50, Y: 20}
	result, _ := m.Update(msg)
	m2 := result.(TuiModel)

	if !m2.subagentModalActive {
		t.Fatal("subagent modal should still be active after inside click through Update()")
	}
}

func TestSubagentModal_Update_OutsideClick_Closes(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 120
	m.height = 40

	m.blocks = append(m.blocks, block{kind: "tool", toolName: "bash", toolState: "done", content: "output"})
	m.toolQueue = append(m.toolQueue, 0)

	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true

	// Click outside modal through Update() — should close modal
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: 0}
	result, _ := m.Update(msg)
	m2 := result.(TuiModel)

	if m2.subagentModalActive {
		t.Fatal("subagent modal should be closed after outside click through Update()")
	}
}

// ─── Subagent modal: text selection (issue #76) ─────────────────────────

func newModalSelectionModel(content string) TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 120
	m.height = 40
	m.subagentModalActive = true
	m.subagentModalTitle = "subagent task"
	m.subagentModalContent.WriteString(content)
	m.subagentModalDone = true
	// Pre-build the modal render buffer so selection can resolve text.
	_ = m.renderSubagentModalView()
	return m
}

// modalContentCoords returns the (x, y) of the first character cell in the
// modal content area for the 120×40 default test terminal.
func modalContentCoords() (int, int) {
	// left=6, contentTop=5, content padding=2 → first char at X=8, Y=5
	return 8, 5
}

func TestModalMouse_PressInContent_StartsSelectionCandidate(t *testing.T) {
	m := newModalSelectionModel("line one\nline two\nline three")
	cx, cy := modalContentCoords()

	// Press on first character of first line
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: cx, Y: cy}
	result, _ := m.handleModalMouseMsg(msg)
	m2 := result.(TuiModel)

	if !m2.selection.Candidate {
		t.Fatal("expected selection candidate after press in content area")
	}
	if m2.selection.Active {
		t.Fatal("selection should not be active on press alone")
	}
}

func TestModalMouse_PressOnTitleBar_DoesNotStartSelection(t *testing.T) {
	m := newModalSelectionModel("line one\nline two\nline three")

	// Title bar is at Y=3 (top), content starts at Y=5
	// Press at X=50, Y=3 (title bar area)
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 50, Y: 3}
	result, _ := m.handleModalMouseMsg(msg)
	m2 := result.(TuiModel)

	if m2.selection.Candidate {
		t.Fatal("selection should not start on title bar click")
	}
	if m2.selection.Active {
		t.Fatal("selection should not be active on title bar click")
	}
}

func TestModalMouse_MotionPromotesToActive(t *testing.T) {
	m := newModalSelectionModel("line one\nline two\nline three")
	cx, cy := modalContentCoords()

	// Press
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: cx, Y: cy}
	result, _ := m.handleModalMouseMsg(msg)
	m = result.(TuiModel)

	// Motion to different X
	msg2 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: cx + 5, Y: cy}
	result, _ = m.handleModalMouseMsg(msg2)
	m = result.(TuiModel)

	if !m.selection.Active {
		t.Fatal("selection should be promoted to active after motion")
	}
	if !m.selection.Dragging {
		t.Fatal("selection should be dragging after motion")
	}
}

func TestModalMouse_Motion_UpdatesCursorCoordinates(t *testing.T) {
	m := newModalSelectionModel("line one\nline two\nline three")
	cx, cy := modalContentCoords()

	// Press
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: cx, Y: cy}
	result, _ := m.handleModalMouseMsg(msg)
	m = result.(TuiModel)

	// Motion to different position
	newX := cx + 10 // screen X = 18, modal X = 10
	newY := cy + 1  // screen Y = 6
	msg2 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: newX, Y: newY}
	result, _ = m.handleModalMouseMsg(msg2)
	m = result.(TuiModel)

	if m.selection.CursorY != newY {
		t.Fatalf("expected CursorY=%d, got %d", newY, m.selection.CursorY)
	}
	if m.selection.CursorX != 10 {
		t.Fatalf("expected CursorX=10, got %d", m.selection.CursorX)
	}
}

func TestModalMouse_DragRelease_CopiesSelection(t *testing.T) {
	copied := withClipboardStub(t)
	m := newModalSelectionModel("alpha\nbeta\ngamma")
	cx, cy := modalContentCoords()

	// Press on "alpha"
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: cx, Y: cy}
	result, _ := m.handleModalMouseMsg(msg)
	m = result.(TuiModel)

	// Drag to second line "beta"
	msg2 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: cx + 4, Y: cy + 1}
	result, _ = m.handleModalMouseMsg(msg2)
	m = result.(TuiModel)

	// Release
	msg3 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: cx + 4, Y: cy + 1}
	result, _ = m.handleModalMouseMsg(msg3)
	m = result.(TuiModel)

	if *copied == "" {
		t.Fatal("expected text to be copied to clipboard after drag release")
	}
	if !m.selectionFlash {
		t.Fatal("expected selection flash after copy")
	}
	if !m.selection.Active {
		t.Fatal("selection should remain active after release (ready for next action)")
	}
	if m.selection.Candidate {
		t.Fatal("candidate should be cleared after release")
	}
}

func TestModalMouse_ClickWithoutDrag_ClearsSelection(t *testing.T) {
	copied := withClipboardStub(t)
	m := newModalSelectionModel("line one\nline two\nline three")
	cx, cy := modalContentCoords()

	// Press
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: cx, Y: cy}
	result, _ := m.handleModalMouseMsg(msg)
	m = result.(TuiModel)

	if !m.selection.Candidate {
		t.Fatal("expected candidate after press")
	}

	// Release at same spot (no drag)
	msg2 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: cx, Y: cy}
	result, _ = m.handleModalMouseMsg(msg2)
	m = result.(TuiModel)

	if m.selection.Active || m.selection.Candidate {
		t.Fatal("selection should be cleared after click without drag")
	}
	if *copied != "" {
		t.Fatal("nothing should be copied on click without drag")
	}
}

func TestModalMouse_MotionOutsideContent_Clamped(t *testing.T) {
	m := newModalSelectionModel("line one\nline two\nline three")
	cx, cy := modalContentCoords()

	// Press inside content
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: cx, Y: cy}
	result, _ := m.handleModalMouseMsg(msg)
	m = result.(TuiModel)

	// Motion to Y above content area (Y=0)
	msg2 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: cx, Y: 0}
	result, _ = m.handleModalMouseMsg(msg2)
	m = result.(TuiModel)

	// Y should be clamped to contentTop
	layout := m.subagentModalLayout()
	if m.selection.CursorY != layout.contentTop {
		t.Fatalf("expected CursorY clamped to contentTop=%d, got %d", layout.contentTop, m.selection.CursorY)
	}
}

func TestModalMouse_MotionOutsideContentBelow_Clamped(t *testing.T) {
	m := newModalSelectionModel("line one")
	cx, cy := modalContentCoords()

	// Press inside content
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: cx, Y: cy}
	result, _ := m.handleModalMouseMsg(msg)
	m = result.(TuiModel)

	// Motion far below
	msg2 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: cx, Y: 999}
	result, _ = m.handleModalMouseMsg(msg2)
	m = result.(TuiModel)

	layout := m.subagentModalLayout()
	if m.selection.CursorY > layout.contentBottom {
		t.Fatalf("CursorY %d exceeds contentBottom %d", m.selection.CursorY, layout.contentBottom)
	}
}

func TestModalMouse_DragUpdatesVersion(t *testing.T) {
	m := newModalSelectionModel("line one\nline two\nline three")
	cx, cy := modalContentCoords()

	v1 := m.selectionVersion
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: cx, Y: cy}
	result, _ := m.handleModalMouseMsg(msg)
	m = result.(TuiModel)

	if m.selectionVersion <= v1 {
		t.Fatal("selectionVersion should increment on press")
	}
	v2 := m.selectionVersion

	msg2 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: cx + 5, Y: cy}
	result, _ = m.handleModalMouseMsg(msg2)
	m = result.(TuiModel)

	if m.selectionVersion <= v2 {
		t.Fatal("selectionVersion should increment on motion")
	}
}

func TestModalMouse_ShiftClick_Ignored(t *testing.T) {
	m := newModalSelectionModel("line one\nline two\nline three")
	cx, cy := modalContentCoords()

	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: cx, Y: cy, Shift: true}
	result, _ := m.handleModalMouseMsg(msg)
	m = result.(TuiModel)

	if m.selection.Candidate {
		t.Fatal("shift+click should not start selection")
	}
}

func TestModalMouse_WheelUp_ClearsSelection(t *testing.T) {
	m := newModalSelectionModel("line one\nline two\nline three")
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 5, CursorX: 10, CursorY: 5}

	msg := tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if m2.selection.Active || m2.selection.Candidate {
		t.Fatal("wheel up should clear active selection")
	}
}

func TestModalMouse_WheelDown_ClearsSelection(t *testing.T) {
	m := newModalSelectionModel("line one\nline two\nline three")
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 5, CursorX: 10, CursorY: 5}

	msg := tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if m2.selection.Active || m2.selection.Candidate {
		t.Fatal("wheel down should clear active selection")
	}
}

func TestModalMouse_EscapeWhileSelecting_ClearsSelectionFirst(t *testing.T) {
	m := newModalSelectionModel("line one\nline two\nline three")
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 5, CursorX: 10, CursorY: 5}

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if m2.selection.Active || m2.selection.Candidate {
		t.Fatal("ESC should clear selection when selecting")
	}
	if !m2.subagentModalActive {
		t.Fatal("modal should remain open after first ESC (selection clear)")
	}
}

func TestModalMouse_EscapeAfterSelectionCleared_ClosesModal(t *testing.T) {
	m := newModalSelectionModel("line one\nline two\nline three")
	// No active selection, just modal open

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, _ := m.updateSubagentModal(msg)
	m2 := result.(TuiModel)

	if m2.subagentModalActive {
		t.Fatal("ESC should close modal when no selection is active")
	}
}

func TestModalMouse_ReleaseAfterDrag_StatusMessage(t *testing.T) {
	copied := withClipboardStub(t)
	m := newModalSelectionModel("hello world")
	cx, cy := modalContentCoords()

	// Press on 'h'
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: cx, Y: cy}
	result, _ := m.handleModalMouseMsg(msg)
	m = result.(TuiModel)

	// Drag to end
	msg2 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: cx + 11, Y: cy}
	result, _ = m.handleModalMouseMsg(msg2)
	m = result.(TuiModel)

	// Release
	msg3 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: cx + 11, Y: cy}
	result, _ = m.handleModalMouseMsg(msg3)
	m = result.(TuiModel)

	if *copied == "" {
		t.Fatal("expected text to be copied")
	}
	if m.statusMessage == "" {
		t.Fatal("expected status message after copy")
	}
}

func TestModalMouse_CopyFromMultiLineSelection(t *testing.T) {
	copied := withClipboardStub(t)
	m := newModalSelectionModel("first line\nsecond line\nthird line\nfourth line")
	cx, cy := modalContentCoords()

	// Press on first line
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: cx + 6, Y: cy}
	result, _ := m.handleModalMouseMsg(msg)
	m = result.(TuiModel)

	// Drag to third line
	msg2 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: cx + 6, Y: cy + 2}
	result, _ = m.handleModalMouseMsg(msg2)
	m = result.(TuiModel)

	// Release
	msg3 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: cx + 6, Y: cy + 2}
	result, _ = m.handleModalMouseMsg(msg3)
	m = result.(TuiModel)

	if *copied == "" {
		t.Fatal("expected text to be copied")
	}
	// Should include "line", "second line", part of "third line"
	if len(*copied) < 10 {
		t.Fatalf("expected multi-line copy, got %q", *copied)
	}
}

func TestModalMouse_NonLeftButton_Ignored(t *testing.T) {
	m := newModalSelectionModel("line one\nline two\nline three")
	cx, cy := modalContentCoords()

	// Right button press
	msg := tea.MouseMsg{Button: tea.MouseButtonRight, Action: tea.MouseActionPress, X: cx, Y: cy}
	result, _ := m.handleModalMouseMsg(msg)
	m2 := result.(TuiModel)

	if m2.selection.Candidate || m2.selection.Active {
		t.Fatal("non-left button should not start selection")
	}
}

func TestModalMouse_SelectionPersistsAfterRelease(t *testing.T) {
	m := newModalSelectionModel("hello world")
	cx, cy := modalContentCoords()

	// Press and drag
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: cx, Y: cy}
	result, _ := m.handleModalMouseMsg(msg)
	m = result.(TuiModel)
	msg2 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: cx + 5, Y: cy}
	result, _ = m.handleModalMouseMsg(msg2)
	m = result.(TuiModel)

	// Release
	msg3 := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: cx + 5, Y: cy}
	result, _ = m.handleModalMouseMsg(msg3)
	m = result.(TuiModel)

	if !m.selection.Active {
		t.Fatal("selection should remain active after release (for visual highlight)")
	}
	if m.selection.Candidate {
		t.Fatal("candidate flag should be cleared after release")
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
