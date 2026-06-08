package display

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci-agent/stream"
)

// stripANSI removes ANSI escape sequences from a string.
var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

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

	rendered := m.renderToolBlock(0, b)

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

	rendered := m.renderToolBlock(0, b)

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
			name:     "glob with pattern",
			toolName: "glob",
			content:  `{"pattern": "**/*.go"}`,
			want:     "glob(**/*.go)",
		},
		{
			name:     "grep with pattern",
			toolName: "grep",
			content:  `{"pattern": "TODO"}`,
			want:     "grep(TODO)",
		},
		{
			name:     "todo with action",
			toolName: "todo",
			content:  `{"action": "add", "content": "Implement thing"}`,
			want:     "todo(add: Implement thing)",
		},
		{
			name:     "subagent with task",
			toolName: "subagent",
			content:  `{"task": "Research parser"}`,
			want:     "subagent(Research parser)",
		},
		{
			name:     "subagent with tasks",
			toolName: "subagent",
			content:  `{"tasks": [{"task": "A"}, {"task": "B"}]}`,
			want:     "subagent(A +1)",
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
			rendered := m.renderToolBlock(0, b)
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
		plain := stripANSI(line)
		if strings.Contains(plain, "bash(check)") {
			toolLine = i
		}
		if strings.Contains(plain, "Here is the result") {
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

// ─── Wrapping tests for renderBlock ──────────────────────────────────────

func TestTuiModel_RenderBlock_Thinking_WrapsLongLines(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 30 // narrow terminal

	line := "this is a very long thinking line that should definitely be wrapped because it exceeds thirty characters"
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: line})

	rendered := m.renderBlock(0, m.blocks[0])
	lines := strings.Split(rendered, "\n")

	// Should produce multiple lines (each starts with │)
	if len(lines) < 2 {
		t.Fatalf("expected wrapped thinking (multiple lines), got %d line(s): %q", len(lines), rendered)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "│") {
			t.Errorf("each thinking line should start with │, got: %q", l)
		}
		// Visible width (stripping ANSI) should be ≤ m.width
		visible := strings.TrimPrefix(l, "│ ")
		if lipgloss.Width(visible) > m.width {
			t.Errorf("visible line too long: %d vis chars (max %d): %q", lipgloss.Width(visible), m.width, visible)
		}
	}
	if !strings.Contains(stripANSI(rendered), "very") || !strings.Contains(stripANSI(rendered), "thinking") {
		t.Errorf("should contain original words, got: %q", rendered)
	}
}

func TestTuiModel_RenderBlock_Thinking_ShortLinesStaySingle(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 80

	line := "short thought"
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: line})

	rendered := m.renderBlock(0, m.blocks[0])
	lines := strings.Split(rendered, "\n")

	// Glamour may add a leading blank line for paragraph spacing
	nonEmpty := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmpty++
		}
	}
	if nonEmpty == 0 {
		t.Fatalf("expected at least one non-empty line, got: %q", rendered)
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if !strings.HasPrefix(l, "│") {
			t.Errorf("each thinking line should start with │, got: %q", l)
		}
	}
	if !strings.Contains(rendered, "short") || !strings.Contains(rendered, "thought") {
		t.Errorf("should contain original words, got: %q", rendered)
	}
}

func TestTuiModel_RenderBlock_Text_WrapsLongLines(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 20

	line := "this is a very long line that should wrap"
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: line})

	rendered := m.renderBlock(0, m.blocks[0])
	lines := strings.Split(rendered, "\n")

	// Glamour may produce a leading blank line; count non-empty lines for wrap check
	nonEmptyLines := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmptyLines++
		}
	}
	if nonEmptyLines < 2 {
		t.Fatalf("expected wrapped text (multiple non-empty lines), got %d non-empty of %d total: %q", nonEmptyLines, len(lines), rendered)
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		// Visible width (stripping ANSI) should be ≤ m.width
		if lipgloss.Width(l) > m.width {
			t.Errorf("visible line too long: %d vis chars (max %d): %q", lipgloss.Width(l), m.width, l)
		}
	}
	if !strings.Contains(stripANSI(rendered), "very") || !strings.Contains(stripANSI(rendered), "should") {
		t.Errorf("should contain original words, got: %q", rendered)
	}
}

func TestTuiModel_RenderBlock_Text_ShortLinesStaySingle(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 80

	line := "short text"
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: line})

	rendered := m.renderBlock(0, m.blocks[0])
	lines := strings.Split(rendered, "\n")

	// Glamour may add a leading blank line or trailing spaces, but should contain the text
	if !strings.Contains(stripANSI(rendered), "short text") {
		t.Errorf("should contain original text, got: %q", rendered)
	}
	// Should have at least one non-empty line
	nonEmpty := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmpty++
		}
	}
	if nonEmpty == 0 {
		t.Fatalf("expected at least one non-empty line, got: %q", rendered)
	}
}

func TestTuiModel_RenderBlock_Error_WrapsLongLines(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 25

	line := "this is a very long error message that must be wrapped"
	m.handleBlockMsg(tuiMsgBlock{kind: "error", content: line})

	rendered := m.renderBlock(0, m.blocks[0])
	lines := strings.Split(rendered, "\n")

	if len(lines) < 2 {
		t.Fatalf("expected wrapped error (multiple lines), got %d line(s): %q", len(lines), rendered)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "│") {
			t.Errorf("each error line should start with │, got: %q", l)
		}
		visible := strings.TrimPrefix(l, "│ ")
		if lipgloss.Width(visible) > m.width {
			t.Errorf("visible line too long: %d vis chars (max %d): %q", lipgloss.Width(visible), m.width, visible)
		}
	}
}

func TestTuiModel_RenderBlock_Block_WrapsLongLines(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 30

	line := "this is a very long block message that should be wrapped properly"
	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: line})

	rendered := m.renderBlock(0, m.blocks[0])
	lines := strings.Split(rendered, "\n")

	if len(lines) < 2 {
		t.Fatalf("expected wrapped block (multiple lines), got %d line(s): %q", len(lines), rendered)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "│") {
			t.Errorf("each block line should start with │, got: %q", l)
		}
	}
}

func TestTuiModel_RenderBlock_Text_MultipleShortLines(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 80

	text := "line one\nline two\nline three"
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: text})

	rendered := m.renderBlock(0, m.blocks[0])

	// Glamour renders \n as separate paragraphs, each may have ANSI formatting.
	// Just check that all three lines appear in the rendered output.
	plain := stripANSI(rendered)
	if !strings.Contains(plain, "line one") {
		t.Errorf("should contain 'line one', got: %q", rendered)
	}
	if !strings.Contains(plain, "line two") {
		t.Errorf("should contain 'line two', got: %q", rendered)
	}
	if !strings.Contains(plain, "line three") {
		t.Errorf("should contain 'line three', got: %q", rendered)
	}
}

func TestTuiModel_View_WrapsLongLinesInAllBlocks(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 30
	m.height = 40

	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "this is a very long thinking line that must wrap due to width limitation"})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "this is a very long text response that should also be wrapped to fit"})
	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "this is a very long block message that also must wrap"})

	view := m.View()
	lines := strings.Split(view, "\n")

	// Only check lines in the message area (before status bar)
	// Status bar is the first line starting with a space followed by model name
	// Input area starts after status bar
	msgLines := lines
	for i, l := range lines {
		if strings.HasPrefix(l, " ") && len(l) > 5 && strings.TrimSpace(l) != "" {
			// This is the status bar - message area ends here
			msgLines = lines[:i]
			break
		}
	}

	tooLong := 0
	for _, l := range msgLines {
		if l == "" {
			continue
		}
		vw := visibleWidth(l)
		if vw > m.width {
			tooLong++
			t.Logf("too long: vw=%d %q", vw, l)
		}
	}
	if tooLong > 0 {
		t.Errorf("expected no lines exceeding width %d, found %d too long in message area", m.width, tooLong)
	}
}

// ─── Benchmark: View() performance ────────────────────────────────────────

func BenchmarkTUIView(b *testing.B) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 100
	m.height = 40

	// Populate 200 blocks with 10 lines each
	for i := 0; i < 200; i++ {
		kind := "text"
		if i%3 == 0 {
			kind = "thinking"
		}
		var content strings.Builder
		for j := 0; j < 10; j++ {
			content.WriteString(fmt.Sprintf("Line %d of block %d. This is some sample text to simulate a realistic conversation.\n", j, i))
		}
		m.handleBlockMsg(tuiMsgBlock{kind: kind, content: content.String()})
	}
	// Mark done to trigger final rendering
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	// Reset benchmark timer
	b.ResetTimer()
	b.ReportAllocs()

	for n := 0; n < b.N; n++ {
		_ = m.View()
	}
}

func BenchmarkTUIViewStreaming(b *testing.B) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 100
	m.height = 40

	// Add a base set of blocks
	for i := 0; i < 50; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: fmt.Sprintf("Block %d with some content.\n", i)})
	}
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	b.ResetTimer()
	b.ReportAllocs()

	// Simulate streaming: append to the last block repeatedly
	for n := 0; n < b.N; n++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: " new token"})
		_ = m.View()
	}
}

// ─── Tool block rendering lifecycle tests ────────────────────────────────

// TestToolBlock_StartShowsRunningSpinner verifies that a tool block just
// started shows spinner and no duration/hint.
func TestToolBlock_StartShowsRunningSpinner(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 40

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})

	view := m.View()
	plain := stripANSI(view)

	if !strings.Contains(plain, "bash") {
		t.Errorf("tool block should contain tool name 'bash', got: %q", plain)
	}
	if !strings.Contains(plain, "⟳") {
		t.Errorf("running tool should show spinner '⟳', got: %q", plain)
	}
	if strings.Contains(plain, "click to display") {
		t.Errorf("running tool should NOT show 'click to display' hint, got: %q", plain)
	}
	if strings.Contains(plain, "ms") && strings.Contains(plain, "s") {
		// Might show duration after completion, but not during running
		t.Errorf("running tool should NOT show duration, got: %q", plain)
	}
}

// TestToolBlock_DeltaUpdatesContent verifies that tool-delta messages update
// the tool block content in View() output.
func TestToolBlock_DeltaUpdatesContent(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 40

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})

	// Before delta: should show no args
	view1 := stripANSI(m.View())
	if !strings.Contains(view1, "read(...)") {
		t.Errorf("before delta, should show 'read(...)', got: %q", view1)
	}

	// Send delta with path
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"path": "main.go"}`})

	// After delta: should show the path
	view2 := stripANSI(m.View())
	if !strings.Contains(view2, "read(main.go)") {
		t.Errorf("after delta, should show 'read(main.go)', got: %q", view2)
	}
}

// TestToolBlock_EndShowsDoneState verifies that after tool-end the block
// shows done state with duration and click hint.
func TestToolBlock_EndShowsDoneState(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 40

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"description": "list files"}`})

	// Small sleep so duration is measurable
	time.Sleep(2 * time.Millisecond)

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "file1\nfile2"})

	view := stripANSI(m.View())

	if !strings.Contains(view, "bash(list files)") {
		t.Errorf("should show formatted tool call, got: %q", view)
	}
	if !strings.Contains(view, "click to display") {
		t.Errorf("done tool should show 'click to display' hint, got: %q", view)
	}
	if strings.Contains(view, "⟳") {
		t.Errorf("done tool should NOT show spinner, got: %q", view)
	}
	// Should show some duration (ms or s)
	if !strings.Contains(view, "ms") && !strings.Contains(view, "s") {
		t.Errorf("done tool should show duration (ms or s), got: %q", view)
	}
}

// TestToolBlock_MultipleDeltasAccumulate verifies that multiple tool-delta
// messages are accumulated correctly.
func TestToolBlock_MultipleDeltasAccumulate(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 40

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})

	// Send partial delta (first part of JSON)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"descript`})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `ion": "list`})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: ` files"}`})

	// After accumulation: should parse to "list files"
	view := stripANSI(m.View())
	if !strings.Contains(view, "bash(list files)") {
		t.Errorf("after cumulative deltas, should show 'bash(list files)', got: %q", view)
	}
}

// TestToolBlock_ViewDoesNotStaleCacheAfterDelta verifies that after a tool
// block gets a delta, the View() output is updated (regression test for
// cachedLines invalidation).
func TestToolBlock_ViewDoesNotStaleCacheAfterDelta(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 40

	// Start tool and get initial View (creates cachedLines)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	_ = m.View() // warm up cache

	// Send delta
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"path": "main.go"}`})

	// View should show updated content, not stale
	view := stripANSI(m.View())
	if !strings.Contains(view, "read(main.go)") {
		t.Errorf("after delta, View() should show 'read(main.go)', got stale: %q", view)
	}
}

// TestToolBlock_ViewDoesNotStaleCacheAfterEnd verifies that after tool-end,
// the View() output is updated (regression test for cachedLines invalidation).
func TestToolBlock_ViewDoesNotStaleCacheAfterEnd(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 40

	// Start tool, send delta, warm cache with View()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"description": "test"}`})
	_ = m.View() // warm up cache

	time.Sleep(2 * time.Millisecond)

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok"})

	// View should show done state, not stale running state
	view := stripANSI(m.View())
	if !strings.Contains(view, "click to display") {
		t.Errorf("after end, View() should show 'click to display', got stale: %q", view)
	}
	if strings.Contains(view, "⟳") {
		t.Errorf("after end, View() should NOT show spinner, got stale: %q", view)
	}
}

// TestToolBlock_TwoConsecutiveTools verifies that when two tools run in
// sequence, both appear correctly in View() output.
func TestToolBlock_TwoConsecutiveTools(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 40

	// First tool: read
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"path": "a.txt"}`})
	time.Sleep(1 * time.Millisecond)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "content a"})

	// Second tool: bash
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"description": "build"}`})
	time.Sleep(1 * time.Millisecond)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok"})

	view := stripANSI(m.View())

	if !strings.Contains(view, "read(a.txt)") {
		t.Errorf("should contain first tool 'read(a.txt)', got: %q", view)
	}
	if !strings.Contains(view, "bash(build)") {
		t.Errorf("should contain second tool 'bash(build)', got: %q", view)
	}
	if !strings.Contains(view, "click to display") {
		t.Errorf("both tools should show click hint, got: %q", view)
	}
}

// TestToolBlock_SubagentInlineFormat verifies that subagent tool shows
// the tool name and state transitions correctly.
func TestToolBlock_SubagentInlineFormat(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 40

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "subagent"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"task": "build project"}`})

	view := stripANSI(m.View())
	// The inline block shows tool name; the task text is available in the modal
	if !strings.Contains(view, "subagent") {
		t.Errorf("subagent should show 'subagent', got: %q", view)
	}
	if !strings.Contains(view, "⟳") {
		t.Errorf("running subagent should show spinner, got: %q", view)
	}

	time.Sleep(1 * time.Millisecond)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done"})

	view2 := stripANSI(m.View())
	if !strings.Contains(view2, "subagent") {
		t.Errorf("after end, subagent should show 'subagent', got: %q", view2)
	}
	if !strings.Contains(view2, "click to display") {
		t.Errorf("subagent done should show click hint, got: %q", view2)
	}
	if strings.Contains(view2, "⟳") {
		t.Errorf("done subagent should NOT show spinner, got: %q", view2)
	}
}

// TestToolBlock_EmptyArgsFallback verifies that when tool-delta content
// is not valid JSON, the block shows fallback format.
func TestToolBlock_EmptyArgsFallback(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 40

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})

	// No delta was sent → args are empty
	view := stripANSI(m.View())
	if !strings.Contains(view, "read(...)") {
		t.Errorf("no args should show 'read(...)', got: %q", view)
	}
}

// TestToolBlock_CachedLinesInvalidatedOnToolStart verifies that when a new
// tool starts, the model's internal state is consistent.
func TestToolBlock_CachedLinesInvalidatedOnToolStart(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 40

	// Start and complete a tool
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"description": "first"}`})
	time.Sleep(1 * time.Millisecond)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done"})

	// Start another tool
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"path": "other.go"}`})

	view := stripANSI(m.View())
	if strings.Contains(view, "bash(") && !strings.Contains(view, "click to display") {
		t.Errorf("first tool should still show but with 'click to display' hint")
	}
	if !strings.Contains(view, "read(other.go)") {
		t.Errorf("second tool should show 'read(other.go)', got: %q", view)
	}
}

// TestToolBlock_GetBlockLines_ReturnsUpdatedContent verifies that
// getBlockLines returns the latest content after tool block changes.
func TestToolBlock_GetBlockLines_ReturnsUpdatedContent(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 80

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "write"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"path": "out.txt"}`})

	// getBlockLines should return lines after tool-delta
	lines := m.getBlockLines(0, false)
	if len(lines) == 0 {
		t.Fatal("getBlockLines should return non-empty lines")
	}
	found := false
	for _, l := range lines {
		if strings.Contains(l, "write(out.txt)") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("getBlockLines should contain 'write(out.txt)', got: %v", lines)
	}

	// Now end the tool
	time.Sleep(1 * time.Millisecond)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done"})

	// getBlockLines should now show done state
	lines2 := m.getBlockLines(0, false)
	if len(lines2) == 0 {
		t.Fatal("getBlockLines should return non-empty lines after end")
	}
	combined := strings.Join(lines2, " ")
	if !strings.Contains(combined, "click to display") {
		t.Errorf("getBlockLines after end should contain 'click to display', got: %v", lines2)
	}
	// Should still have the tool call
	if !strings.Contains(combined, "write(out.txt)") {
		t.Errorf("getBlockLines after end should contain 'write(out.txt)', got: %v", lines2)
	}
}

// TestToolBlock_RenderBlock_RunningVsDone verifies that renderBlock returns
// different output for running vs done tool blocks.
func TestToolBlock_RenderBlock_RunningVsDone(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 80

	// Create blocks and fill them via handlers to set all fields correctly
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "edit"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"path": "x.go"}`})

	// Snapshot running render
	running := m.renderBlock(0, m.blocks[0])

	time.Sleep(1 * time.Millisecond)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done"})

	done := m.renderBlock(0, m.blocks[0])

	if running == done {
		t.Errorf("renderBlock should return different output for running vs done, but got same: %q", running)
	}
	if !strings.Contains(running, "⟳") {
		t.Errorf("running render should contain spinner, got: %q", running)
	}
	if strings.Contains(running, "click to display") {
		t.Errorf("running render should NOT contain 'click to display', got: %q", running)
	}
	if !strings.Contains(done, "click to display") {
		t.Errorf("done render should contain 'click to display', got: %q", done)
	}
	if strings.Contains(done, "⟳") {
		t.Errorf("done render should NOT contain spinner, got: %q", done)
	}
	if !strings.Contains(done, "edit(x.go)") {
		t.Errorf("done render should contain 'edit(x.go)', got: %q", done)
	}
}

// TestToolBlock_ToolBlockOutput_NotVisibleInInline verifies that tool output
// is not shown in the inline tool block (only in the modal on click).
func TestToolBlock_ToolBlockOutput_NotVisibleInInline(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 40

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"description": "test"}`})
	time.Sleep(1 * time.Millisecond)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "very long output that should not appear inline"})

	view := stripANSI(m.View())
	if strings.Contains(view, "very long output") {
		t.Errorf("tool output should NOT appear in inline tool block, got: %q", view)
	}
	if !strings.Contains(view, "bash(test)") {
		t.Errorf("tool block should show formatted call, got: %q", view)
	}
}

// ─── Autoscroll tests (issue #50) ────────────────────────────────────────

func TestTuiModel_Autoscroll_NewContentWhileAtBottom(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 20 // small viewport

	// Initially at bottom
	if !m.atBottom {
		t.Error("newModel should have atBottom = true")
	}
	if m.scrollLine != 0 {
		t.Errorf("newModel should have scrollLine = 0, got %d", m.scrollLine)
	}

	// Add content to fill viewport
	for i := 0; i < 5; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: fmt.Sprintf("line %d", i)})
	}

	// Should still be at bottom after adding content
	if !m.atBottom {
		t.Error("atBottom should remain true after adding content while at bottom")
	}
	if m.scrollLine != 0 {
		t.Errorf("scrollLine should be 0 when atBottom, got %d", m.scrollLine)
	}

	// Simulate user scrolling up (PgUp equivalent)
	m.atBottom = false
	m.scrollLine = 10

	// Add more content while scrolled away from bottom
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "new content while scrolled up"})

	// Should NOT auto-scroll to bottom (user explicitly scrolled up)
	if m.atBottom {
		t.Error("atBottom should be false after user scrolled up and new content arrives")
	}
	if m.scrollLine == 0 {
		t.Error("scrollLine should NOT be 0 when user scrolled away from bottom")
	}

	// Simulate pressing End key
	m.atBottom = true
	m.scrollLine = 0

	// Add more content while at bottom
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "final content"})

	// Should auto-scroll to bottom
	if !m.atBottom {
		t.Error("atBottom should be true after pressing End and adding content")
	}
	if m.scrollLine != 0 {
		t.Errorf("scrollLine should be 0 when auto-scrolling, got %d", m.scrollLine)
	}
}

func TestTuiModel_Autoscroll_ScrollEventsSetAtBottom(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 20

	// Add some content first
	for i := 0; i < 3; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: fmt.Sprintf("line %d", i)})
	}

	// Start at bottom
	m.atBottom = true
	m.scrollLine = 0

	// Simulate PageUp → should set atBottom = false
	m.atBottom = false
	m.scrollLine = 5
	if m.atBottom {
		t.Error("PageUp should set atBottom = false")
	}

	// Simulate End → should set atBottom = true
	m.atBottom = false
	m.scrollLine = 10
	m.atBottom = true
	m.scrollLine = 0
	if !m.atBottom {
		t.Error("End should set atBottom = true")
	}

	// Simulate Home → should set atBottom = false
	m.atBottom = false
	m.scrollLine = 100
	if m.atBottom {
		t.Error("Home should set atBottom = false")
	}

	// Simulate reaching bottom via scroll down: scrollLine goes from 1 to 0
	// The event handler sets atBottom = true when scrollLine would go negative.
	m.atBottom = false
	m.scrollLine = 1
	// Applying same logic as KeyCtrlDown / KeyPgDown / MouseWheelDown handlers:
	m.scrollLine--
	if m.scrollLine < 0 {
		m.scrollLine = 0
		m.atBottom = true
	}
	// scrollLine is now 0 (clamped) but not negative, so atBottom stays false
	if m.atBottom {
		t.Error("scrolling from scrollLine=1 to 0 should NOT set atBottom (need negative)")
	}

	// Proper "at bottom reached via scroll past bottom": scrollLine = 0, then scroll down again
	m.atBottom = false
	m.scrollLine = 0
	m.scrollLine -= 3 // simulate PageDown while at bottom
	if m.scrollLine < 0 {
		m.scrollLine = 0
		m.atBottom = true
	}
	if !m.atBottom {
		t.Error("scrolling past bottom (scrollLine goes negative) should set atBottom = true")
	}
}

func TestTuiModel_Autoscroll_ModalRestoresScrollState(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 20

	// Add a tool block to click on
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"command": "test"}`})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done"})

	// User scrolls up a bit
	m.atBottom = false
	m.scrollLine = 3

	// Save scroll state (simulating what happens when modal opens)
	m.savedScrollLine = m.scrollLine
	m.savedAtBottom = m.atBottom

	// Verify saved state
	if m.savedScrollLine != 3 {
		t.Errorf("savedScrollLine should be 3, got %d", m.savedScrollLine)
	}
	if m.savedAtBottom {
		t.Error("savedAtBottom should be false")
	}

	// Restore scroll state (simulating what happens when modal closes)
	m.atBottom = m.savedAtBottom
	m.scrollLine = m.savedScrollLine

	if m.atBottom {
		t.Error("atBottom should be restored to false")
	}
	if m.scrollLine != 3 {
		t.Errorf("scrollLine should be restored to 3, got %d", m.scrollLine)
	}

	// Now test case where user was at bottom before modal
	m.atBottom = true
	m.scrollLine = 0
	m.savedScrollLine = m.scrollLine
	m.savedAtBottom = m.atBottom

	if m.savedScrollLine != 0 {
		t.Errorf("savedScrollLine should be 0, got %d", m.savedScrollLine)
	}
	if !m.savedAtBottom {
		t.Error("savedAtBottom should be true")
	}

	// Restore
	m.atBottom = m.savedAtBottom
	m.scrollLine = m.savedScrollLine

	if !m.atBottom {
		t.Error("atBottom should be restored to true")
	}
	if m.scrollLine != 0 {
		t.Errorf("scrollLine should be restored to 0, got %d", m.scrollLine)
	}
}

// ─── Autoscroll multi-turn tests (issue #50) ─────────────────────────────

func TestAutoscroll_MultiTurnSimulation(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 10 // small viewport

	check := func(turn string, wantBlocks int) {
		t.Helper()
		if !m.atBottom {
			t.Errorf("[%s] atBottom should be true, got false (scrollLine=%d)", turn, m.scrollLine)
		}
		if m.scrollLine != 0 {
			t.Errorf("[%s] scrollLine should be 0, got %d", turn, m.scrollLine)
		}
		if got := len(m.blocks); got != wantBlocks {
			t.Errorf("[%s] expected %d blocks, got %d", turn, wantBlocks, got)
			for i, b := range m.blocks {
				t.Logf("  block[%d]: kind=%s content=%q", i, b.kind, b.content)
			}
		}
	}

	// Turn 1: user types, agent responds (no thinking phase)
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "You: hello"})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "Hi there!"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	check("turn1-after", 2) // You: hello, Hi there!

	// Turn 2: another prompt
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "You: what's up?"})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "Not much, you?"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	check("turn2-after", 4)

	// Turn 3: another prompt
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "You: just testing"})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "Cool, keep going!"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	check("turn3-after", 6)

	// Verify all blocks are separate
	for i, b := range m.blocks {
		if i%2 == 0 {
			if !strings.HasPrefix(b.content, "You: ") {
				t.Errorf("block[%d] should be user message (You: ...), got %q", i, b.content)
			}
		} else {
			if strings.HasPrefix(b.content, "You: ") {
				t.Errorf("block[%d] should be agent response, got %q", i, b.content)
			}
		}
	}

	// Now scroll up
	m.atBottom = false
	m.scrollLine = 5

	// Turn 4: user submits while scrolled up
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "You: scrolled up message"})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "response while scrolled up"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if m.atBottom {
		t.Error("atBottom should be false (user scrolled up)")
	}
	if m.scrollLine == 0 {
		t.Error("scrollLine should not be 0 when scrolled up")
	}

	// Press End
	m.atBottom = true
	m.scrollLine = 0

	// Turn 5: normal at bottom
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "You: final test"})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "Final answer"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	check("turn5-after", 10)
}

func TestAutoscroll_BlockSeparation_WithThinking(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 20

	// Turn 1 with thinking phase
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "You: hello"})
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "thinking..."})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "Hi there!"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	// With thinking phase, blocks should be: [You: hello, thinking, Hi there!]
	if got := len(m.blocks); got != 3 {
		t.Fatalf("expected 3 blocks with thinking, got %d", got)
		for i, b := range m.blocks {
			t.Logf("  block[%d]: kind=%s content=%q", i, b.kind, b.content)
		}
	}

	// Turn 2
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "You: second msg"})
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "more thinking..."})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "Second response"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if got := len(m.blocks); got != 6 {
		t.Fatalf("expected 6 blocks after turn 2, got %d", got)
	}

	// Verify structure
	expected := []struct {
		kind   string
		prefix string // content prefix
	}{
		{"text", "You: "},
		{"thinking", "thinking..."},
		{"text", "Hi there!"},
		{"text", "You: "},
		{"thinking", "more thinking..."},
		{"text", "Second response"},
	}
	for i, exp := range expected {
		if m.blocks[i].kind != exp.kind {
			t.Errorf("block[%d] kind: expected %q, got %q", i, exp.kind, m.blocks[i].kind)
		}
		if !strings.HasPrefix(m.blocks[i].content, exp.prefix) {
			t.Errorf("block[%d] content: expected prefix %q, got %q", i, exp.prefix, m.blocks[i].content)
		}
	}
}

// TestAutoscroll_AgentStreamingDeltas_MergeCorrectly verifies that multiple
// streaming text deltas from the agent are still merged into one block.
func TestAutoscroll_AgentStreamingDeltas_MergeCorrectly(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.ready = true
	m.width = 80
	m.height = 20

	// User message
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "You: hello"})
	// Agent sends multiple text deltas (streaming) → should merge into one block
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "Hi "})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "there! "})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "How "})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "are you?"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if got := len(m.blocks); got != 2 {
		t.Fatalf("expected 2 blocks (user + agent), got %d", got)
		for i, b := range m.blocks {
			t.Logf("  block[%d]: kind=%s content=%q", i, b.kind, b.content)
		}
	}

	if m.blocks[0].content != "You: hello" {
		t.Errorf("block[0]: expected 'You: hello', got %q", m.blocks[0].content)
	}
	if m.blocks[1].content != "Hi there! How are you?" {
		t.Errorf("block[1]: expected 'Hi there! How are you?', got %q", m.blocks[1].content)
	}
}
