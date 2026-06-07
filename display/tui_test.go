package display

import (
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci-agent/stream"
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

// ─── ShowTotalUsage tests (via handleBlockMsg) ──────────────────────────

func TestTuiModel_ShowTotalUsage_BlocksAdded(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	usage := stream.Usage{Input: 1000, Output: 500, CacheRead: 200}
	line := buildUsageLineNoTiming(usage)

	// Simulate what ShowTotalUsage posts
	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "───── new conversation ─────"})
	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "📊 Session total: " + line})

	if len(m.blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(m.blocks))
	}
	if m.blocks[0].content != "───── new conversation ─────" {
		t.Errorf("expected block[0] content %q, got %q", "───── new conversation ─────", m.blocks[0].content)
	}
	expectedTotal := "📊 Session total: in=800 (+200 cache) out=500"
	if m.blocks[1].content != expectedTotal {
		t.Errorf("expected block[1] content %q, got %q", expectedTotal, m.blocks[1].content)
	}
	if m.blocks[0].kind != "block" || m.blocks[1].kind != "block" {
		t.Errorf("both blocks should have kind 'block'")
	}
}

func TestTuiModel_ShowTotalUsage_NoTimingInOutput(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	usage := stream.Usage{Input: 500, Output: 300}
	line := buildUsageLineNoTiming(usage)
	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "───── new conversation ─────"})
	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "📊 Session total: " + line})

	totalBlock := m.blocks[len(m.blocks)-1]
	if hasTiming(totalBlock.content) {
		t.Errorf("session total should not contain timing, got %q", totalBlock.content)
	}
	if !strings.Contains(totalBlock.content, "📊 Session total: in=500 out=300") {
		t.Errorf("unexpected total line, got %q", totalBlock.content)
	}
}

func TestTuiModel_ShowTotalUsage_AllUsageFields(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	usage := stream.Usage{Input: 1000, Output: 600, Reasoning: 50, CacheRead: 200, CacheWrite: 30}
	line := buildUsageLineNoTiming(usage)
	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "───── new conversation ─────"})
	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "📊 Session total: " + line})

	totalBlock := m.blocks[len(m.blocks)-1]
	if !strings.Contains(totalBlock.content, "in=800") {
		t.Errorf("expected in=800 in total, got %q", totalBlock.content)
	}
	if !strings.Contains(totalBlock.content, "out=600") {
		t.Errorf("expected out=600 in total, got %q", totalBlock.content)
	}
	if !strings.Contains(totalBlock.content, "r=50") {
		t.Errorf("expected r=50 in total, got %q", totalBlock.content)
	}
	if !strings.Contains(totalBlock.content, "(+200 cache)") {
		t.Errorf("expected cache in total, got %q", totalBlock.content)
	}
	if !strings.Contains(totalBlock.content, "cache_w=30") {
		t.Errorf("expected cache_w=30 in total, got %q", totalBlock.content)
	}
	if hasTiming(totalBlock.content) {
		t.Errorf("should not contain timing stats, got %q", totalBlock.content)
	}
}

func TestTuiModel_ShowTotalUsage_ZeroUsage(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "───── new conversation ─────"})
	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "📊 Session total: in=0 out=0"})

	if len(m.blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(m.blocks))
	}
	if m.blocks[1].content != "📊 Session total: in=0 out=0" {
		t.Errorf("unexpected total line, got %q", m.blocks[1].content)
	}
}

func TestTuiModel_ShowTotalUsage_AfterExistingBlocks(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)

	// Pre-populate with some blocks (simulating existing conversation)
	// Text block + tool-start creates blocks; tool-delta/end modify existing
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "Hello"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"command": "ls"}`})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "file.txt"})

	// Now simulate ShowTotalUsage
	usage := stream.Usage{Input: 300, Output: 150}
	line := buildUsageLineNoTiming(usage)
	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "───── new conversation ─────"})
	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "📊 Session total: " + line})
	// tool-delta and tool-end don't create new blocks, so we have:
	// text(1) + tool-start(1) + block(1) + block(1) = 4
	if len(m.blocks) != 4 {
		t.Fatalf("expected 4 blocks total (text + tool-start + 2 ShowTotalUsage), got %d", len(m.blocks))
	}
	// Check that the total block is last
	last := m.blocks[len(m.blocks)-1]
	if last.content != "📊 Session total: in=300 out=150" {
		t.Errorf("unexpected total line, got %q", last.content)
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
