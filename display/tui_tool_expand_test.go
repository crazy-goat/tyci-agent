package display

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// plainLines returns the rendered lines of block idx without styling.
func plainLines(m *TuiModel, idx int) []string {
	var out []string
	for _, l := range m.getBlockLines(idx, false) {
		out = append(out, ansi.Strip(l))
	}
	return out
}

func doneBashBlock(output string) block {
	return block{kind: "tool", toolName: "bash", toolState: "done", content: "", output: output, duration: time.Second}
}

func TestToolBlock_ClickExpandsInPlace(t *testing.T) {
	m := newSelectionTestModel()
	m.blocks = []block{doneBashBlock("hi there")}
	m.invalidateAllBlockLineCounts()

	model, _ := m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 1, Y: 1})
	m = model.(TuiModel)
	model, _ = m.handleMouseMsg(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 1, Y: 1})
	m = model.(TuiModel)

	if m.subagentModalActive {
		t.Fatal("a click on a tool line must not open a modal")
	}
	lines := plainLines(&m, 0)
	if len(lines) < 2 {
		t.Fatalf("expanded tool has no body below the header: %q", lines)
	}
	if !strings.Contains(strings.Join(lines[1:], "\n"), "hi there") {
		t.Errorf("body does not contain the output: %q", lines)
	}
}

func TestToolBlock_SecondClickFolds(t *testing.T) {
	m := newTestModel()
	m.blocks = []block{doneBashBlock("hi there")}

	m.toggleBlockAt(0)
	m.toggleBlockAt(0)

	if m.blocks[0].expanded {
		t.Fatal("a second click should fold the block")
	}
	if lines := plainLines(&m, 0); len(lines) != 1 {
		t.Errorf("folded tool should render one line, got %q", lines)
	}
}

func TestThinkingBlock_ClickExpandsInPlace(t *testing.T) {
	m := newTestModel()
	m.blocks = []block{{kind: "thinking", content: "full reasoning text", toolState: "done", duration: time.Second}}
	freezeThinkingSummary(&m.blocks[0], true)

	m.toggleBlockAt(0)

	lines := plainLines(&m, 0)
	if len(lines) < 2 || !strings.Contains(strings.Join(lines[1:], "\n"), "full reasoning text") {
		t.Fatalf("expanded thinking block should show its full text below the line: %q", lines)
	}
	if !strings.Contains(lines[0], "click to fold") {
		t.Errorf("header should say 'click to fold', got %q", lines[0])
	}
}

func TestToolBlock_ExpandedOutputIsCapped(t *testing.T) {
	var out []string
	for i := 0; i < 30; i++ {
		out = append(out, fmt.Sprintf("line %d", i))
	}
	m := newTestModel()
	m.blocks = []block{doneBashBlock(strings.Join(out, "\n"))}
	m.toggleBlockAt(0)

	joined := strings.Join(plainLines(&m, 0), "\n")
	if !strings.Contains(joined, "... 10 more lines") {
		t.Errorf("expected the cut marker for 10 hidden lines, got:\n%s", joined)
	}
	if !strings.Contains(joined, "line 29") {
		t.Errorf("the newest line must stay visible, got:\n%s", joined)
	}
	if strings.Contains(joined, "line 09") {
		t.Errorf("the oldest lines must be cut, got:\n%s", joined)
	}
}

func TestToolBlock_RunningShowsLiveOutput(t *testing.T) {
	m := newTestModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.toggleBlockAt(0)

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "step two"})

	if !strings.Contains(strings.Join(plainLines(&m, 0), "\n"), "step two") {
		t.Fatal("the expanded body does not show the live output")
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done", duration: time.Second})
	if !m.blocks[0].expanded {
		t.Error("the body should stay open when the tool ends")
	}
}

func TestToolBlock_HintsChange(t *testing.T) {
	m := newTestModel()
	m.blocks = []block{doneBashBlock("out")}
	if line := plainLines(&m, 0)[0]; !strings.Contains(line, "click to expand") {
		t.Errorf("folded done tool: %q", line)
	}
	m.toggleBlockAt(0)
	if line := plainLines(&m, 0)[0]; !strings.Contains(line, "click to fold") {
		t.Errorf("expanded done tool: %q", line)
	}

	m.blocks[0].toolState = "running"
	m.blocks[0].expanded = false
	m.toggleBlockAt(0)
	if line := plainLines(&m, 0)[0]; !strings.Contains(line, "click to fold") {
		t.Errorf("expanded running tool: %q", line)
	}
	m.toggleBlockAt(0)
	if line := plainLines(&m, 0)[0]; !strings.Contains(line, "click for progress") {
		t.Errorf("folded running tool: %q", line)
	}
}

func TestAgentView_ToolClickExpands(t *testing.T) {
	m := newTestModel()
	m.agentView = &agentView{jobID: "expand-agent-view", label: "worker", model: newAgentViewModel()}
	m.agentView.model.blocks = []block{doneBashBlock("agent output")}
	m.agentView.model.invalidateAllBlockLineCounts()
	m.View()

	row := -1
	for y, rl := range m.renderBuffer.Lines {
		if rl.BlockIndex == 0 && rl.SourceKind != "group" {
			row = y
			break
		}
	}
	if row < 0 {
		t.Fatal("the agent view does not show its tool block")
	}

	m.toggleBlockAt(row)

	if !m.agentView.model.blocks[0].expanded {
		t.Fatal("a click in the agent view should expand its tool block")
	}
	if len(m.blocks) > 0 && m.blocks[0].expanded {
		t.Error("the main transcript must not change")
	}
}

func TestToolBlock_ExpandWhileScrolledUpKeepsHeaderRow(t *testing.T) {
	m := newTestModel()
	for i := 0; i < 60; i++ {
		m.blocks = append(m.blocks, block{kind: "block", content: fmt.Sprintf("filler %d", i)})
	}
	m.blocks = append(m.blocks, doneBashBlock("a\nb\nc"))
	tool := len(m.blocks) - 1
	for i := 0; i < 6; i++ {
		m.blocks = append(m.blocks, block{kind: "block", content: fmt.Sprintf("after %d", i)})
	}
	m.scrollLine = 10

	headerRow := func() int {
		m.View()
		for y, rl := range m.renderBuffer.Lines {
			if rl.BlockIndex == tool && rl.SourceKind != "group" {
				return y
			}
		}
		return -1
	}
	before := headerRow()
	if before < 0 {
		t.Fatal("setup: the tool header is not on screen")
	}

	m.toggleBlockAt(before)

	if !m.blocks[tool].expanded {
		t.Fatal("setup: the block should be expanded")
	}
	if after := headerRow(); after != before {
		t.Errorf("the header moved from row %d to row %d when the body opened", before, after)
	}
}

// TestToolBlock_ExpandAtBottomKeepsHeaderRow checks that a click on a tool at
// the bottom of the transcript does not move its header row up.
func TestToolBlock_ExpandAtBottomKeepsHeaderRow(t *testing.T) {
	m := newTestModel()
	for i := 0; i < 60; i++ {
		m.blocks = append(m.blocks, block{kind: "block", content: fmt.Sprintf("filler %d", i)})
	}
	m.blocks = append(m.blocks, doneBashBlock("a\nb\nc"))
	tool := len(m.blocks) - 1
	m.scrollLine = 0
	m.atBottom = true

	headerRow := func() int {
		m.View()
		for y, rl := range m.renderBuffer.Lines {
			if rl.BlockIndex == tool && rl.SourceKind != "group" {
				return y
			}
		}
		return -1
	}
	before := headerRow()
	if before < 0 {
		t.Fatal("setup: the tool header is not on screen")
	}

	m.toggleBlockAt(before)

	if !m.blocks[tool].expanded {
		t.Fatal("setup: the block should be expanded")
	}
	if after := headerRow(); after != before {
		t.Errorf("the header moved from row %d to row %d when the body opened at the bottom", before, after)
	}
	if m.atBottom {
		t.Error("the view should leave the bottom so the header stays in place")
	}
}

// TestToolBlock_CopyIncludesExpandedBody checks that a selection over an
// expanded tool copies its body without the bar gutter.
func TestToolBlock_CopyIncludesExpandedBody(t *testing.T) {
	copied := withClipboardStub(t)
	m := newSelectionTestModel()
	m.renderBuffer = &RenderBuffer{Lines: []RenderLine{
		{PlainText: "┃ tool bash(echo hi) 0.00s - click to fold", SourceKind: "tool", Y: 0},
		{PlainText: "┃ hi there", SourceKind: "tool", Y: 1},
	}}
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 100, CursorY: 1}
	m = m.copySelection()
	if !strings.HasSuffix(*copied, "\nhi there") {
		t.Fatalf("expected the body line without its gutter, got %q", *copied)
	}
}
