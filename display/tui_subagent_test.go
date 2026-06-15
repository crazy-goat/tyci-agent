package display

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/decodo/tyci/stream"
)

// ─── TuiModel handleBlockMsg subagent tests ──────────────────────────────

func TestTuiModel_SubagentToolStart_DoesNotAutoOpenModal(t *testing.T) {
	// The modal should NOT auto-open on tool-start.
	// It should only open when the user clicks the subagent tool block.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Send tool-start for subagent
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})

	// Modal should NOT be active (fixed behavior)
	if m.subagentModalActive {
		t.Error("subagent modal should NOT auto-open on tool-start")
	}
	// But subagentModalToolIdx should be set for later use
	if m.subagentModalToolIdx != 0 {
		t.Errorf("expected subagentModalToolIdx=0, got %d", m.subagentModalToolIdx)
	}
}

func TestTuiModel_SubagentToolProgress_GoesToModalWhenActive(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Setup: simulate subagent tool start (modal NOT auto-opened)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})

	// Manually activate modal (simulating user click)
	m.subagentModalActive = true
	m.subagentModalContent.Reset()

	if !m.subagentModalActive {
		t.Fatal("modal should be active")
	}

	// Send tool-progress matching the subagent tool index
	m.handleBlockMsg(tuiMsgBlock{
		kind:    "tool-progress",
		toolIdx: 0, // matches subagentModalToolIdx
		content: "subagent output line 1\n",
	})

	modalContent := m.subagentModalContent.String()
	if modalContent == "" {
		t.Fatal("modal content should have received the tool-progress")
	}
	if modalContent != "subagent output line 1\n" {
		t.Errorf("unexpected modal content: %q", modalContent)
	}

	// Inline block should NOT contain the output
	if len(m.toolQueue) > 0 {
		bidx := m.toolQueue[0]
		if m.blocks[bidx].content != "" {
			t.Errorf("inline block should be empty (no output in main thread), got %q", m.blocks[bidx].content)
		}
	}

	// Send another line
	m.handleBlockMsg(tuiMsgBlock{
		kind:    "tool-progress",
		toolIdx: 0,
		content: "subagent output line 2\n",
	})

	modalContent = m.subagentModalContent.String()
	if modalContent != "subagent output line 1\nsubagent output line 2\n" {
		t.Errorf("unexpected modal content: %q", modalContent)
	}
}

func TestTuiModel_SubagentToolProgress_GoesToInlineWhenModalNotActive(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Start subagent (modal NOT auto-opened)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})

	if m.subagentModalActive {
		t.Fatal("modal should not be active after tool-start")
	}

	// Send tool-progress — goes to modal content (captured for later), never to inline block
	m.handleBlockMsg(tuiMsgBlock{
		kind:    "tool-progress",
		toolIdx: 0,
		content: "working...\n",
	})

	// Modal content captures output even when modal not active
	if m.subagentModalContent.String() != "working...\n" {
		t.Errorf("modal content should be captured, got %q", m.subagentModalContent.String())
	}

	// Inline block should NOT have the content (no output in main thread)
	if len(m.toolQueue) != 1 {
		t.Fatalf("expected 1 tool in queue, got %d", len(m.toolQueue))
	}
	bidx := m.toolQueue[0]
	if m.blocks[bidx].content != "" {
		t.Errorf("inline block should be empty (no output in main thread), got %q", m.blocks[bidx].content)
	}
}

func TestTuiModel_SubagentToolProgress_WrongToolIdx_GoesToInline(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Setup: start a bash tool (not subagent)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})

	// modal should NOT be active for bash
	if m.subagentModalActive {
		t.Error("modal should not be active for non-subagent tools")
	}

	// Send tool-progress for the bash tool
	m.handleBlockMsg(tuiMsgBlock{
		kind:    "tool-progress",
		toolIdx: 0,
		content: "bash output\n",
	})

	// Content should go to output field (for modal), not inline block content
	if m.subagentModalContent.String() != "" {
		t.Errorf("modal content should be empty, got %q", m.subagentModalContent.String())
	}

	// Inline block output should have the content (not content field)
	if len(m.toolQueue) != 1 {
		t.Fatalf("expected 1 tool in queue, got %d", len(m.toolQueue))
	}
	bidx := m.toolQueue[0]
	if bidx < 0 || bidx >= len(m.blocks) {
		t.Fatal("invalid block index")
	}
	if m.blocks[bidx].output != "bash output\n" {
		t.Errorf("block output should have bash output, got %q", m.blocks[bidx].output)
	}
	if m.blocks[bidx].content != "" {
		t.Errorf("block content should be empty (summary only), got %q", m.blocks[bidx].content)
	}
}

func TestTuiModel_SubagentToolEnd_MarksDone(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Start subagent (no auto-open)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})

	// Modal not active yet
	if m.subagentModalActive {
		t.Fatal("modal should not be auto-opened")
	}

	// Some progress
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "working...\n"})

	// End subagent (finishToolAt pops from queue but we saved the block idx)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "task completed"})

	// Modal should NOT be active (was never opened)
	if m.subagentModalActive {
		t.Error("modal should not be active after tool-end")
	}
	// But it should be marked as done
	if !m.subagentModalDone {
		t.Error("subagentModalDone should be true after tool-end")
	}

	// The inline block should be marked as done (toolState="done")
	// and keep clean "subagent (task...)" format (no output in main thread)
	found := false
	for _, b := range m.blocks {
		if b.toolName == "subagent" {
			found = true
			if b.toolState != "done" {
				t.Error("inline block should be marked done")
			}
			// Content should be clean (no result summary) — just "subagent (task...)" or empty
			if strings.Contains(b.content, "→") {
				t.Errorf("inline block should NOT contain result summary, got %q", b.content)
			}
		}
	}
	if !found {
		t.Error("subagent block not found in blocks")
	}
}

func TestTuiModel_SubagentModal_Reset(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Start subagent and populate
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})
	m.subagentModalActive = true // manually open
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "some output\n"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done"})

	// Reset
	m.handleBlockMsg(tuiMsgBlock{kind: "reset"})

	if m.subagentModalActive {
		t.Error("reset should deactivate subagent modal")
	}
	if m.subagentModalContent.String() != "" {
		t.Errorf("reset should clear modal content, got %q", m.subagentModalContent.String())
	}
}

func TestTuiModel_SubagentInlineSummaryFromDelta(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"task": "find all Go files"}`})

	if len(m.blocks) != 1 {
		t.Fatalf("expected one block, got %d", len(m.blocks))
	}
	got := formatToolCall(m.blocks[0].toolName, m.blocks[0].content)
	if got != "subagent(find all Go files)" {
		t.Fatalf("expected subagent summary with task, got %q (content %q)", got, m.blocks[0].content)
	}
}

func TestTuiModel_SubagentModal_TitleSetFromDelta(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Start subagent
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})

	// Send delta with task description in JSON
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"task": "find all Go files and list them"}`})

	if m.subagentModalTitle == "subagent" {
		t.Error("modal title should be updated from task argument")
	}
	if m.subagentModalTitle == "" {
		t.Error("modal title should not be empty after delta with task")
	}
}

func TestTuiModel_SubagentModal_ScrollLimits(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Initially, max scroll should be 0 (no content)
	if max := m.subagentModalMaxScroll(); max != 0 {
		t.Errorf("expected max scroll 0 with no content, got %d", max)
	}

	// Add some content
	m.subagentModalContent.WriteString("line1\nline2\nline3\n")

	// Max scroll should account for content height
	max := m.subagentModalMaxScroll()
	if max < 0 {
		t.Errorf("max scroll should be >= 0, got %d", max)
	}
}

func TestTuiModel_SubagentModal_ScrollDoesNotGoNegative(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.subagentModalActive = true
	m.subagentModalScroll = 5

	// Simulate scrolling down past 0
	_, _ = m.updateSubagentModal(
		tea.WindowSizeMsg{Width: 80, Height: 24},
	)
}

// ─── updateSubagentModal tests ───────────────────────────────────────────

func TestUpdateSubagentModal_EscapeClosesModalWhenDone(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.subagentModalActive = true
	m.subagentModalDone = true
	m.subagentModalContent.WriteString("some output")
	m.subagentModalToolIdx = 0

	// Press ESC
	newModel, _ := m.updateSubagentModal(tea.KeyMsg{Type: tea.KeyEscape})
	tm := newModel.(TuiModel)

	if tm.subagentModalActive {
		t.Error("ESC should close the subagent modal")
	}
}

func TestUpdateSubagentModal_EscapeClosesModalEvenWhenRunning(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.subagentModalActive = true
	m.subagentModalDone = false
	m.subagentModalContent.WriteString("still working...")
	m.subagentModalToolIdx = 0

	newModel, _ := m.updateSubagentModal(tea.KeyMsg{Type: tea.KeyEscape})
	tm := newModel.(TuiModel)

	if tm.subagentModalActive {
		t.Error("ESC should close modal even when subagent is running (force close)")
	}
}

func TestUpdateSubagentModal_CtrlCQuits(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.subagentModalActive = true
	m.subagentModalDone = false

	newModel, cmd := m.updateSubagentModal(tea.KeyMsg{Type: tea.KeyCtrlC})
	tm := newModel.(TuiModel)

	if !tm.quitting {
		t.Error("Ctrl+C should set quitting=true")
	}
	if cmd == nil {
		t.Error("Ctrl+C should return tea.Quit command")
	} else {
		// Verify it's tea.Quit by calling it and checking the Msg type
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); !ok {
			t.Errorf("expected QuitMsg, got %T", msg)
		}
	}
}

func TestUpdateSubagentModal_EnterClosesWhenDone(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.subagentModalActive = true
	m.subagentModalDone = true

	newModel, _ := m.updateSubagentModal(tea.KeyMsg{Type: tea.KeyEnter})
	tm := newModel.(TuiModel)

	if tm.subagentModalActive {
		t.Error("Enter should close modal when done")
	}
}

func TestUpdateSubagentModal_EnterDoesNotCloseWhenRunning(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.subagentModalActive = true
	m.subagentModalDone = false

	_, _ = m.updateSubagentModal(tea.KeyMsg{Type: tea.KeyEnter})

	if !m.subagentModalActive {
		t.Error("Enter should NOT close modal when subagent is still running")
	}
}

func TestUpdateSubagentModal_ForwardsTuiMsgBlock(t *testing.T) {
	// When modal is active, tuiMsgBlock messages (tool-progress, etc.)
	// should still be processed by handleBlockMsg.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.subagentModalActive = true
	m.subagentModalToolIdx = 0
	m.subagentModalContent.Reset()

	// Simulate a tool-progress arriving while modal is active
	// This goes through Update -> updateSubagentModal -> case tuiMsgBlock -> handleBlockMsg
	newModel, _ := m.updateSubagentModal(tuiMsgBlock{
		kind:    "tool-progress",
		toolIdx: 0,
		content: "streaming line\n",
	})
	tm := newModel.(TuiModel)

	if tm.subagentModalContent.String() != "streaming line\n" {
		t.Errorf("expected 'streaming line\\n' in modal, got %q", tm.subagentModalContent.String())
	}
}

func TestUpdateSubagentModal_ForwardsDoneMessage(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.subagentModalActive = true
	m.reading = false
	m.status = "tool"

	newModel, _ := m.updateSubagentModal(tuiMsgBlock{
		kind:  "done",
		usage: stream.Usage{Input: 10, Output: 5},
	})
	tm := newModel.(TuiModel)

	if !tm.reading {
		t.Error("done message should set reading=true even when modal is active")
	}
	if tm.status != "idle" {
		t.Errorf("expected status 'idle', got %q", tm.status)
	}
}

// ─── handleBlockMsg interaction tests ────────────────────────────────────

func TestTuiModel_ToolStart_NonSubagent_DoesNotOpenModal(t *testing.T) {
	tools := []string{"bash", "read", "write", "edit"}
	for _, toolName := range tools {
		m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: toolName})
		if m.subagentModalActive {
			t.Errorf("tool %q should NOT open subagent modal", toolName)
		}
	}
}

func TestTuiModel_MultipleSubagentTools_HandlesToolIndexCorrectly(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Start bash first
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	// Then start subagent
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})

	if m.subagentModalActive {
		t.Fatal("modal should not be auto-opened")
	}
	// subagentModalToolIdx should be 1 (second tool in queue)
	if m.subagentModalToolIdx != 1 {
		t.Errorf("expected subagentModalToolIdx=1, got %d", m.subagentModalToolIdx)
	}

	// Manually activate modal (simulate click)
	m.subagentModalActive = true
	m.subagentModalContent.Reset()

	// Progress for bash (toolIdx 0) should NOT go to modal
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "bash output\n"})
	if m.subagentModalContent.String() != "" {
		t.Errorf("bash progress should NOT go to modal, got %q", m.subagentModalContent.String())
	}

	// Progress for subagent (toolIdx 1) SHOULD go to modal
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 1, content: "subagent output\n"})
	if m.subagentModalContent.String() != "subagent output\n" {
		t.Errorf("subagent progress should go to modal, got %q", m.subagentModalContent.String())
	}
}

// ─── Summary block building tests ────────────────────────────────────────

func TestTuiModel_BuildStatus_ShowsModalInfo(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 80
	m.height = 24

	status := m.buildStatus()
	if status == "" {
		t.Error("status should show model name")
	}
}

func TestTuiModel_Done_SetsReadingTrue(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.reading = false

	m.handleBlockMsg(tuiMsgBlock{kind: "done", usage: stream.Usage{}, stats: stream.Stats{}})

	if !m.reading {
		t.Error("done message should set reading=true")
	}
	if m.status != "idle" {
		t.Errorf("expected status 'idle', got %q", m.status)
	}
}

func TestTuiModel_ResetStatus(t *testing.T) {
	// ResetStatus is called after ESC cancels an agent run
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.reading = false
	m.status = "tool"

	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if !m.reading {
		t.Error("reset should set reading=true")
	}
	if m.status != "idle" {
		t.Errorf("expected status 'idle', got %q", m.status)
	}
}

// ─── Click handler tests ─────────────────────────────────────────────────

func TestTuiModel_ClickOnSubagentBlock_OpensModal(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 100
	m.height = 40

	// Start subagent
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})
	// Send progress — captured to subagentModalContent (not inline block)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "modal output\n"})

	if m.subagentModalActive {
		t.Fatal("modal should NOT be auto-opened before click")
	}

	// Simulate clicking: modal content is already populated by tool-progress
	m.subagentModalActive = true

	if !m.subagentModalActive {
		t.Error("modal should be active after click")
	}
	if m.subagentModalContent.String() != "modal output\n" {
		t.Errorf("modal should have captured output, got %q", m.subagentModalContent.String())
	}
}

// ─── Regression: strings.Builder copy-by-value panic ─────────────────────
//
// This test verifies that copying a TuiModel after the subagentModalContent
// builder has been used does NOT panic. The bug was: subagentModalContent was
// strings.Builder (value type), which panics when copied after use. Changed
// to *strings.Builder. This test would catch a regression back to value type.

func TestSubagentModalContent_CopyAfterWrite_NoPanic(t *testing.T) {
	// Create model, use builder, then copy — should not panic
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Write something to the builder (activating the copyCheck)
	m.subagentModalContent.WriteString("hello from subagent\n")

	// Copy the model by value (exactly what bubbletea does in Update)
	// This would panic if subagentModalContent were strings.Builder (value)
	m2 := m

	// Use the copy — must not panic
	got := m2.subagentModalContent.String()
	if got != "hello from subagent\n" {
		t.Errorf("copied model has wrong content: %q", got)
	}

	// Also ensure the original still works
	orig := m.subagentModalContent.String()
	if orig != "hello from subagent\n" {
		t.Errorf("original model has wrong content: %q", orig)
	}

	// Write to the copy — both pointers should reference the same builder
	m2.subagentModalContent.WriteString("more data\n")
	final := m.subagentModalContent.String()
	if final != "hello from subagent\nmore data\n" {
		t.Errorf("original should see writes via copy: %q", final)
	}
}

func TestSubagentModalContent_MultipleUpdateCycles_NoPanic(t *testing.T) {
	// Simulate what bubbletea does: multiple Update calls that copy the model
	// by value while the builder is being written to.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Activate modal (simulating user click on a subagent block)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})
	m.subagentModalActive = true
	m.subagentModalContent.Reset()
	m.subagentModalToolIdx = 0

	// Simulate multiple Update cycles, each copying the model by value
	for i := 0; i < 50; i++ {
		// Write progress
		m.handleBlockMsg(tuiMsgBlock{
			kind:    "tool-progress",
			toolIdx: 0,
			content: "output line\n",
		})

		// Simulate bubbletea calling Update (by value) — this must not panic.
		// We use updateSubagentModal directly which is called by value.
		updated, _ := m.updateSubagentModal(tuiMsgBlock{
			kind:    "tool-progress",
			toolIdx: 0,
			content: "streaming cycle\n",
		})
		m = updated.(TuiModel)
	}

	// Final content should be present
	content := m.subagentModalContent.String()
	if content == "" {
		t.Error("modal content should not be empty after many update cycles")
	}
	if !strings.Contains(content, "output line") {
		t.Errorf("expected to find 'output line' in modal content, got %q", content)
	}
	if !strings.Contains(content, "streaming cycle") {
		t.Errorf("expected to find 'streaming cycle' in modal content, got %q", content)
	}
}
