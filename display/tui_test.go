package display

import (
	"strings"
	"testing"
	"time"
)

// ─── Tool duration freeze tests ──────────────────────────────────────────

func TestTuiModel_ToolDuration_FrozenAtEnd(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Start a bash tool
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})

	if len(m.toolQueue) != 1 {
		t.Fatalf("expected 1 tool in queue, got %d", len(m.toolQueue))
	}
	bIdx := m.toolQueue[0]
	if m.blocks[bIdx].duration != 0 {
		t.Fatal("duration should be 0 while tool is running")
	}

	// Wait a tiny bit so there's measurable duration
	time.Sleep(2 * time.Millisecond)

	// End the tool
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done"})

	// Duration should now be frozen (non-zero)
	if m.blocks[bIdx].duration == 0 {
		t.Fatal("duration should be set (frozen) after tool-end")
	}
	if m.blocks[bIdx].toolState != "done" {
		t.Fatal("toolState should be 'done'")
	}
}

func TestTuiModel_ToolDuration_FrozenNeverChanges(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	bIdx := m.toolQueue[0]

	time.Sleep(5 * time.Millisecond)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done"})

	frozen := m.blocks[bIdx].duration
	if frozen == 0 {
		t.Fatal("duration should be non-zero")
	}

	// Sleep again — the frozen duration must stay the same
	time.Sleep(10 * time.Millisecond)

	if m.blocks[bIdx].duration != frozen {
		t.Errorf("duration changed after being frozen: was %v, now %v", frozen, m.blocks[bIdx].duration)
	}
}

func TestTuiModel_ToolDuration_SubagentFrozenAtEnd(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Start subagent tool
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})

	if len(m.toolQueue) != 1 {
		t.Fatalf("expected 1 tool in queue, got %d", len(m.toolQueue))
	}
	bIdx := m.toolQueue[0]
	if m.blocks[bIdx].duration != 0 {
		t.Fatal("duration should be 0 while tool is running")
	}

	time.Sleep(2 * time.Millisecond)

	// End subagent (goes through the isSubagentEnd path)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "task done"})

	if m.blocks[bIdx].duration == 0 {
		t.Fatal("duration should be set after subagent tool-end")
	}
	if m.blocks[bIdx].toolState != "done" {
		t.Fatal("toolState should be 'done' for subagent")
	}
}

// ─── renderToolBlock duration display tests ──────────────────────────────

func TestTuiModel_RenderToolBlock_ShowsFrozenDuration(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Build a block manually with a known frozen duration
	b := block{
		kind:      "tool",
		toolName:  "bash",
		toolState: "done",
		duration:  1234 * time.Millisecond, // 1.23s
	}

	rendered := m.renderToolBlock(b)

	if !strings.Contains(rendered, "1.23s") {
		t.Errorf("expected rendered block to contain '1.23s', got %q", rendered)
	}
	if !strings.Contains(rendered, "- click to display") {
		t.Errorf("expected rendered block to contain hint, got %q", rendered)
	}
}

func TestTuiModel_RenderToolBlock_RunningShowsSpinner(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	b := block{
		kind:      "tool",
		toolName:  "bash",
		toolState: "running",
		duration:  0,
	}

	rendered := m.renderToolBlock(b)

	if !strings.Contains(rendered, "⟳") {
		t.Errorf("expected running tool to show spinner, got %q", rendered)
	}
	if strings.Contains(rendered, "click to display") {
		t.Errorf("running tool should not have 'click to display', got %q", rendered)
	}
}

func TestTuiModel_RenderToolBlock_FormatToolCall(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		content  string
		want     string // substring expected in output
	}{
		{
			name:     "read with path",
			toolName: "read",
			content:  `{"path": "main.go"}`,
			want:     "read(main.go)",
		},
		{
			name:     "bash with description",
			toolName: "bash",
			content:  `{"description": "list files"}`,
			want:     "bash(list files)",
		},
		{
			name:     "empty args",
			toolName: "read",
			content:  "",
			want:     "read(...)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
			b := block{
				kind:      "tool",
				toolName:  tt.toolName,
				content:   tt.content,
				toolState: "done",
				duration:  time.Second,
			}
			rendered := m.renderToolBlock(b)
			if !strings.Contains(rendered, tt.want) {
				t.Errorf("expected %q in rendered output, got %q", tt.want, rendered)
			}
		})
	}
}

// ─── View() output: no "calling tools:" header ───────────────────────────

func TestTuiModel_View_NoCallingToolsHeader(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 100
	m.height = 40

	// Add two consecutive tool blocks (like the old behavior with header)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"path": "main.go"}`})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "file content"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"description": "build"}`})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok"})

	view := m.View()

	if strings.Contains(view, "calling tools") {
		t.Error("View() should NOT contain 'calling tools' header")
	}
}

// ─── Blank line between consecutive tool blocks ──────────────────────────

func TestTuiModel_View_NoBlankLineBetweenConsecutiveTools(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 100
	m.height = 40

	// Add two consecutive tool blocks
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"path": "x.txt"}`})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "content"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"description": "check"}`})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok"})

	view := m.View()
	lines := strings.Split(view, "\n")

	// Find the two tool lines and check there's no blank line between them
	var toolLines []int
	for i, line := range lines {
		if strings.Contains(line, "read(x.txt)") || strings.Contains(line, "bash(check)") {
			toolLines = append(toolLines, i)
		}
	}

	if len(toolLines) < 2 {
		t.Fatalf("expected 2 tool lines in view, found %d: %v", len(toolLines), toolLines)
	}

	// Check that between toolLines[0] and toolLines[1] there's no blank line
	for i := toolLines[0] + 1; i < toolLines[1]; i++ {
		if strings.TrimSpace(lines[i]) == "" {
			t.Errorf("found blank line between consecutive tool blocks at line %d", i)
			return
		}
	}
}

func TestTuiModel_View_BlankLineBetweenToolAndText(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 100
	m.height = 40

	// Add a tool block followed by a text block
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"description": "check"}`})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok"})
	// Text block simulates assistant response after tools
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "Here is the result"})

	view := m.View()
	lines := strings.Split(view, "\n")

	// Find tool line and text line
	toolLine := -1
	textLine := -1
	for i, line := range lines {
		if strings.Contains(line, "bash(check)") {
			toolLine = i
		}
		if strings.Contains(line, "Here is the result") {
			textLine = i
		}
	}

	if toolLine == -1 || textLine == -1 {
		t.Fatalf("could not find tool or text line in view")
	}

	// There should be at least one blank line between them
	foundBlank := false
	for i := toolLine + 1; i < textLine; i++ {
		if strings.TrimSpace(lines[i]) == "" {
			foundBlank = true
			break
		}
	}
	if !foundBlank {
		t.Error("expected blank line between tool block and text block, but none found")
	}
}
