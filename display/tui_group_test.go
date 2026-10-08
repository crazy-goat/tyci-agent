package display

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── Consecutive tool and thinking blocks form one group (issue #399) ─────
//
// These tests build models through the real message path (newModel then
// handleBlockMsg), as tui_thinking_collapsed_test.go does.

// newGroupTestModel returns a ready model with a fixed size, so mouse rows
// map to the message area as in a real terminal.
func newGroupTestModel() TuiModel {
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.ready = true
	m.width, m.height = 80, 24
	return m
}

// addFinishedTool adds one tool block that has already finished.
func addFinishedTool(m *TuiModel, name, delta string) {
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: name})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: delta})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok"})
}

// flatKinds returns the source kind of every flat row, in order.
func flatKinds(flat []flatRenderLine) []string {
	kinds := make([]string, 0, len(flat))
	for _, line := range flat {
		kinds = append(kinds, line.SourceKind)
	}
	return kinds
}

func TestGroup_ClosedByDefaultDrawsOneHeaderLine(t *testing.T) {
	m := newGroupTestModel()
	addFinishedTool(&m, "read", `{"path": "a.txt"}`)
	addFinishedTool(&m, "bash", `{"description": "build"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	flat := m.buildAllFlatRenderLines()
	if len(flat) != 1 {
		t.Fatalf("closed group should draw one line, got %d: %v", len(flat), flatKinds(flat))
	}
	if flat[0].SourceKind != "group" || flat[0].BlockIndex != 0 {
		t.Fatalf("header should be a group line of block 0, got kind=%q index=%d", flat[0].SourceKind, flat[0].BlockIndex)
	}
	header := stripANSI(flat[0].Text)
	if !strings.Contains(header, "✓ 2 steps (2 tool, 0 thinking)") {
		t.Errorf("finished header should name the counts, got %q", header)
	}
	if !strings.Contains(header, "▸ expand") {
		t.Errorf("closed header should offer expand, got %q", header)
	}
}

func TestGroup_HeaderCountsThinkingSteps(t *testing.T) {
	m := newGroupTestModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "deciding which file to open first"})
	addFinishedTool(&m, "read", `{"path": "a.txt"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	flat := m.buildAllFlatRenderLines()
	if len(flat) != 1 {
		t.Fatalf("closed group should draw one line, got %d: %v", len(flat), flatKinds(flat))
	}
	if header := stripANSI(flat[0].Text); !strings.Contains(header, "✓ 2 steps (1 tool, 1 thinking)") {
		t.Errorf("header should count both kinds, got %q", header)
	}
}

func TestGroup_ActiveHeaderShowsLatestStep(t *testing.T) {
	m := newGroupTestModel()
	addFinishedTool(&m, "read", `{"path": "a.txt"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"description": "build"}`})

	flat := m.buildAllFlatRenderLines()
	if len(flat) != 1 {
		t.Fatalf("active closed group should draw one line, got %d", len(flat))
	}
	header := stripANSI(flat[0].Text)
	if !strings.Contains(header, "⟳ 2 steps · tool bash(build)") {
		t.Errorf("running header should name the step count and latest step, got %q", header)
	}
	if strings.Contains(header, "✓") {
		t.Errorf("running header must not show the finished mark, got %q", header)
	}
}

func TestGroup_SingleBlockIsNotGrouped(t *testing.T) {
	m := newGroupTestModel()
	addFinishedTool(&m, "read", `{"path": "a.txt"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	flat := m.buildAllFlatRenderLines()
	if len(flat) != 1 {
		t.Fatalf("one tool should draw one line, got %d: %v", len(flat), flatKinds(flat))
	}
	if flat[0].SourceKind != "tool" {
		t.Fatalf("one tool must not become a group, got kind=%q", flat[0].SourceKind)
	}
	if strings.Contains(stripANSI(flat[0].Text), "steps") {
		t.Errorf("one tool must not show a group header, got %q", stripANSI(flat[0].Text))
	}
	if m.latestGroup() != -1 {
		t.Errorf("latestGroup() = %d, want -1 when there is no group", m.latestGroup())
	}
}

func TestGroup_TextBlockEndsTheRun(t *testing.T) {
	m := newGroupTestModel()
	addFinishedTool(&m, "read", `{"path": "a.txt"}`)
	addFinishedTool(&m, "bash", `{"description": "build"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "Build is done."})
	addFinishedTool(&m, "read", `{"path": "b.txt"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	flat := m.buildAllFlatRenderLines()
	groups := 0
	for _, line := range flat {
		if line.SourceKind == "group" {
			groups++
		}
	}
	if groups != 1 {
		t.Fatalf("text should split the run into one group and one single tool, got %d group rows: %v", groups, flatKinds(flat))
	}
	last := flat[len(flat)-1]
	if last.SourceKind != "tool" || last.BlockIndex != 3 {
		t.Errorf("the tool after the text should be a single tool row, got kind=%q index=%d", last.SourceKind, last.BlockIndex)
	}
}

func TestGroup_OpenShowsStepLines(t *testing.T) {
	m := newGroupTestModel()
	addFinishedTool(&m, "read", `{"path": "a.txt"}`)
	addFinishedTool(&m, "bash", `{"description": "build"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.toggleGroup(0)

	flat := m.buildAllFlatRenderLines()
	if len(flat) != 3 {
		t.Fatalf("open group should draw header and two steps, got %d: %v", len(flat), flatKinds(flat))
	}
	if flat[1].BlockIndex != 0 || flat[2].BlockIndex != 1 {
		t.Fatalf("step rows should belong to blocks 0 and 1, got %d and %d", flat[1].BlockIndex, flat[2].BlockIndex)
	}
	if header := stripANSI(flat[0].Text); !strings.Contains(header, "▾ collapse") {
		t.Errorf("open header should offer collapse, got %q", header)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "read(a.txt)") || !strings.Contains(view, "bash(build)") {
		t.Errorf("open group should show both steps in View(), got %q", view)
	}
}

func TestGroup_ClosedChildrenStayMeasured(t *testing.T) {
	// The scrollback flush only writes out blocks whose lines are cached, so a
	// closed group must still measure its steps.
	m := newGroupTestModel()
	addFinishedTool(&m, "read", `{"path": "a.txt"}`)
	addFinishedTool(&m, "bash", `{"description": "build"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	_ = m.totalRenderedLines()
	for i := range m.blocks {
		if m.blocks[i].cachedLineCount == 0 {
			t.Errorf("block %d of a closed group has no cached line count", i)
		}
	}
}

func TestGroup_CtrlOTogglesLatestGroup(t *testing.T) {
	m := newGroupTestModel()
	addFinishedTool(&m, "read", `{"path": "a.txt"}`)
	addFinishedTool(&m, "bash", `{"description": "build"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	next, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = next.(TuiModel)
	if !m.blocks[0].groupOpen {
		t.Fatal("Ctrl+O should open the latest group")
	}
	if got := len(m.buildAllFlatRenderLines()); got != 3 {
		t.Fatalf("after Ctrl+O the group should show three rows, got %d", got)
	}

	next, _ = m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = next.(TuiModel)
	if m.blocks[0].groupOpen {
		t.Fatal("a second Ctrl+O should close the group again")
	}
}

func TestGroup_CtrlOWithoutGroupDoesNothing(t *testing.T) {
	m := newGroupTestModel()
	addFinishedTool(&m, "read", `{"path": "a.txt"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	next, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = next.(TuiModel)
	if m.subagentModalActive {
		t.Fatal("Ctrl+O must not open a modal")
	}
	if len(m.buildAllFlatRenderLines()) != 1 {
		t.Fatal("Ctrl+O without a group must not change the transcript")
	}
}

func TestGroup_ClickOnHeaderTogglesWithoutModal(t *testing.T) {
	m := newGroupTestModel()
	addFinishedTool(&m, "read", `{"path": "a.txt"}`)
	addFinishedTool(&m, "bash", `{"description": "build"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	// Screen row 1 is the first message row, where the header is.
	next, _ := m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 1})
	m = next.(TuiModel)
	next, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 1, Y: 1})
	m = next.(TuiModel)

	if !m.blocks[0].groupOpen {
		t.Fatal("a click on the group header should open the group")
	}
	if m.subagentModalActive {
		t.Fatal("a click on the group header must not open a tool modal")
	}

	next, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 1})
	m = next.(TuiModel)
	next, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 1, Y: 1})
	m = next.(TuiModel)
	if m.blocks[0].groupOpen {
		t.Fatal("a second click on the header should close the group")
	}
}

func TestGroup_ClickOnStepOpensToolModal(t *testing.T) {
	m := newGroupTestModel()
	addFinishedTool(&m, "read", `{"path": "a.txt"}`)
	addFinishedTool(&m, "bash", `{"description": "build"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.toggleGroup(0)

	// Screen row 2 is the first step line, below the header.
	next, _ := m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 2})
	m = next.(TuiModel)
	next, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 1, Y: 2})
	m = next.(TuiModel)

	if !m.subagentModalActive || m.subagentModalBlockIdx != 0 {
		t.Fatalf("a click on a step should open that step's modal, active=%v block=%d", m.subagentModalActive, m.subagentModalBlockIdx)
	}
}

func TestGroup_LineCountsAgreeWithFlatLines(t *testing.T) {
	m := newGroupTestModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "Starting."})
	addFinishedTool(&m, "read", `{"path": "a.txt"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "deciding what to do next"})
	addFinishedTool(&m, "bash", `{"description": "build"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "Done."})
	addFinishedTool(&m, "read", `{"path": "b.txt"}`)
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	for _, open := range []bool{false, true} {
		m.blocks[1].groupOpen = open
		m.invalidateTotalLines()

		flat := m.buildAllFlatRenderLines()
		if got := m.totalRenderedLines(); got != len(flat) {
			t.Fatalf("open=%v: totalRenderedLines() = %d, flat rows = %d", open, got, len(flat))
		}
		for y, line := range flat {
			if line.SourceKind == "spacer" {
				continue
			}
			if got := m.blockAtVisibleLine(y); got != line.BlockIndex {
				t.Fatalf("open=%v: blockAtVisibleLine(%d) = %d, flat row says %d", open, y, got, line.BlockIndex)
			}
		}
	}
}

func TestGroup_TotalTimeEndsWithTheLastStepToFinish(t *testing.T) {
	// A parallel batch: bash runs for 10s and read for 1s. Both steps start
	// with the batch. The results arrive in block order after the batch ends,
	// so the last block (read) is not the one that ends last.
	m := newGroupTestModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	batchStart := time.Now().Add(-10 * time.Second)
	m.blocks[0].startTime = batchStart
	m.blocks[1].startTime = batchStart
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok", duration: 10 * time.Second})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok", duration: time.Second})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if span := m.groupSpan(0, 1); span < 10*time.Second || span > 11*time.Second {
		t.Fatalf("group total = %v, want about 10s: the slow call ends the batch", span)
	}
	header := stripANSI(m.buildAllFlatRenderLines()[0].Text)
	if !strings.Contains(header, "✓ 2 steps (2 tool, 0 thinking)") {
		t.Errorf("finished header should name the counts, got %q", header)
	}
}

func TestGroup_TotalTimeAddsSerialCalls(t *testing.T) {
	// Two bash calls of 10s run one after the other. Both started with the
	// batch, so the total is 20s, not the 10s of a single call.
	m := newGroupTestModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	batchStart := time.Now().Add(-20 * time.Second)
	m.blocks[0].startTime = batchStart
	m.blocks[1].startTime = batchStart
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok", duration: 10 * time.Second})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok", duration: 10 * time.Second})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if span := m.groupSpan(0, 1); span < 20*time.Second || span > 21*time.Second {
		t.Fatalf("group total = %v, want about 20s for two serial calls", span)
	}
}
