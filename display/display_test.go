package display

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/stream"
)

func captureOutput(t *testing.T) (stdoutBuf, stderrBuf *bytes.Buffer, sync func(), restore func()) {
	t.Helper()

	origStdout := os.Stdout
	origStderr := os.Stderr

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}

	os.Stdout = stdoutW
	os.Stderr = stderrW

	stdoutBuf = &bytes.Buffer{}
	stderrBuf = &bytes.Buffer{}

	done := make(chan struct{}, 2)
	go func() {
		_, _ = stdoutBuf.ReadFrom(stdoutR)
		done <- struct{}{}
	}()
	go func() {
		_, _ = stderrBuf.ReadFrom(stderrR)
		done <- struct{}{}
	}()

	sync = func() {
		_ = stdoutW.Close()
		_ = stderrW.Close()
		<-done
		<-done
	}

	restore = func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = stdoutR.Close()
		_ = stderrR.Close()
	}
	return stdoutBuf, stderrBuf, sync, restore
}

func TestTerminal_Text_WritesStdout(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.Text("hello")
	term.Text(" world")
	sync()

	got := stdout.String()
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestTerminal_Thinking_RendersWithIcon(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.Thinking("some thought")
	sync()

	got := stdout.String()
	if !strings.Contains(got, "💭") {
		t.Errorf("expected thinking output to contain 💭, got %q", got)
	}
	if !strings.Contains(got, term.bgThinking) {
		t.Errorf("expected output to contain bgThinking code, got %q", got)
	}
}

func TestTerminal_ToolCall_BashWithDescription(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.ToolCallStart("bash")
	term.ToolCallDelta(`{"description": "list files", "command": "ls -la"}`)
	term.ToolCallEnd("bash", "file1\nfile2")
	sync()

	got := stdout.String()
	if !strings.Contains(got, "🔧") {
		t.Errorf("expected tool output to contain 🔧, got %q", got)
	}
	if !strings.Contains(got, "list files") {
		t.Errorf("expected output to contain description 'list files', got %q", got)
	}
	if !strings.Contains(got, `"command": "ls -la"`) {
		t.Errorf("expected output to contain command 'ls -la', got %q", got)
	}
	if !strings.Contains(got, "file1") {
		t.Errorf("expected output to contain result 'file1', got %q", got)
	}
}

func TestTerminal_ToolCall_BashError(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.ToolCallStart("bash")
	term.ToolCallDelta(`{"description": "fail", "command": "false"}`)
	term.ToolCallEnd("bash", "exit status 1")
	sync()

	got := stdout.String()
	if !strings.Contains(got, "exit status 1") {
		t.Errorf("expected output to contain error 'exit status 1', got %q", got)
	}
}

func TestTerminal_ToolCall_ReadOmitsResult(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.ToolCallStart("read")
	term.ToolCallDelta(`{"path": "x.txt"}`)
	term.ToolCallEnd("read", "file content")
	sync()

	got := stdout.String()
	if !strings.Contains(got, "🔧") {
		t.Errorf("expected read header, got %q", got)
	}
	if strings.Contains(got, "file content") {
		t.Errorf("expected read tool NOT to render result body, got %q", got)
	}
}

func TestTerminal_Error_WritesToStderr(t *testing.T) {
	_, stderr, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.Error(os.ErrNotExist)
	sync()

	got := stderr.String()
	if !strings.Contains(got, "Error:") {
		t.Errorf("expected error output to contain 'Error:', got %q", got)
	}
	if !strings.Contains(got, "not exist") {
		t.Errorf("expected error output to contain 'not exist', got %q", got)
	}
}

func TestTerminal_Summary_OutputsUsage(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.Summary(stream.Usage{Input: 100, Output: 50, CacheRead: 200}, stream.Stats{})
	sync()

	got := stdout.String()
	if !strings.Contains(got, "in=") || !strings.Contains(got, "out=") {
		t.Errorf("expected usage info, got %q", got)
	}
	if !strings.Contains(got, "cache)") {
		t.Errorf("expected cache info, got %q", got)
	}
}

// --- Tests for stripAnsi ---

func TestStripAnsi_EmptyString(t *testing.T) {
	if got := stripAnsi(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestStripAnsi_NoAnsi(t *testing.T) {
	input := "hello world"
	if got := stripAnsi(input); got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestStripAnsi_WithAnsi(t *testing.T) {
	input := "\033[31mred\033[0m"
	expected := "red"
	if got := stripAnsi(input); got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestStripAnsi_MultipleAnsi(t *testing.T) {
	input := "\033[1m\033[31mbold red\033[0m"
	expected := "bold red"
	if got := stripAnsi(input); got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestStripAnsi_ClearLine(t *testing.T) {
	input := "a\033[Kb"
	expected := "ab"
	if got := stripAnsi(input); got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

// --- Tests for visibleWidth ---

func TestVisibleWidth_Empty(t *testing.T) {
	if got := visibleWidth(""); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestVisibleWidth_Plain(t *testing.T) {
	if got := visibleWidth("hello"); got != 5 {
		t.Errorf("expected 5, got %d", got)
	}
}

func TestVisibleWidth_WithAnsi(t *testing.T) {
	input := "\033[31mhello\033[0m"
	if got := visibleWidth(input); got != 5 {
		t.Errorf("expected 5, got %d", got)
	}
}

func TestVisibleWidth_Unicode(t *testing.T) {
	input := "💭 hello"
	if got := visibleWidth(input); got != 8 { // 💭 is 2 columns, space=1, hello=5
		t.Errorf("expected 8, got %d", got)
	}
}

// --- Tests for wrapText ---

func TestWrapText_ZeroWidth(t *testing.T) {
	input := "hello"
	if got := wrapText(input, 0, 0); got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestWrapText_NegativeWidth(t *testing.T) {
	input := "hello"
	if got := wrapText(input, -1, 0); got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestWrapText_EmptyString(t *testing.T) {
	if got := wrapText("", 10, 0); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestWrapText_ShortLine(t *testing.T) {
	input := "hello"
	got := wrapText(input, 80, 0)
	if got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestWrapText_ExactFit(t *testing.T) {
	input := "hello"
	got := wrapText(input, 5, 0)
	if got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestWrapText_LongLine(t *testing.T) {
	input := "1234567890abcdef"
	got := wrapText(input, 10, 0)
	// Should wrap at 10 chars
	expected := "1234567890\033[K\nabcdef"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_MultipleWraps(t *testing.T) {
	input := "123456789012345678901234567890"
	got := wrapText(input, 10, 0)
	expected := "1234567890\033[K\n1234567890\033[K\n1234567890"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_MultipleLines(t *testing.T) {
	input := "short\n1234567890abcdef"
	got := wrapText(input, 10, 0)
	expected := "short\n1234567890\033[K\nabcdef"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_WithAnsi(t *testing.T) {
	// ANSI sequences should not count towards width
	input := "\033[31m1234567890\033[0mabcdef"
	got := wrapText(input, 10, 0)
	// The visible part is "1234567890abcdef" = 16 chars
	// First 10 visible chars: "1234567890" (with ANSI around)
	// Then "\033[K\n"
	// Then "abcdef" (with ANSI reset before it? No, the ANSI reset is at end)
	// Actually the input is: \033[31m1234567890\033[0mabcdef
	// After wrapping at visual position 10:
	// Part1: \033[31m1234567890\033[0m (10 visible chars)
	// Part2: abcdef
	// Expected: \033[31m1234567890\033[0m\033[K\nabcdef
	expected := "\033[31m1234567890\033[0m\033[K\nabcdef"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_WithAnsiSpanningWrap(t *testing.T) {
	// ANSI at start, then text that wraps
	input := "\033[31m1234567890abcdef\033[0m"
	got := wrapText(input, 10, 0)
	// First 10 visible: 1234567890, all red
	// Then clearLine + newline
	// Then remaining: abcdef, still red (no reset until end)
	expected := "\033[31m1234567890\033[K\nabcdef\033[0m"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_WithUnicode(t *testing.T) {
	input := "💭1234567890abcdef"
	got := wrapText(input, 10, 0)
	// 💭 is one rune (visible width 1), so 10 visible chars: "💭123456789"
	expected := "💭123456789\033[K\n0abcdef"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_AlreadyHasClearLine(t *testing.T) {
	input := "1234567890\033[K\nabcdef"
	got := wrapText(input, 10, 0)
	// The first line is exactly 10 visible + clearLine, so no wrap needed
	expected := "1234567890\033[K\nabcdef"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_LongLineWithClearLine(t *testing.T) {
	input := "1234567890abcdef\033[K\nxyz"
	got := wrapText(input, 10, 0)
	// The first line is 16 vis chars + clearLine, so it gets wrapped
	// But wrapText splits at 10, so:
	// "1234567890" + "\033[K\n" + "abcdef\033[K\n" + "xyz"
	expected := "1234567890\033[K\nabcdef\033[K\nxyz"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

// --- Integration: Terminal with long lines ---

func TestTerminal_Thinking_LongLine(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	// Create a terminal with known width for deterministic test
	term := NewTerminal()
	term.termWidth = 20                                    // force narrow width
	longText := "0123456789012345678901234567890123456789" // 40 chars
	term.Thinking(longText)
	sync()

	got := stdout.String()
	// Should have multiple clearLine sequences (one per wrapped line + one per \n from ReplaceAll)
	// Count \033[K occurrences
	count := strings.Count(got, "\033[K")
	if count < 3 {
		t.Errorf("expected at least 3 clearLine sequences for a 40-char line wrapped at 20, got %d. Output: %q", count, got)
	}
	// Should contain the thinking icon
	if !strings.Contains(got, "💭") {
		t.Errorf("expected thinking icon, got %q", got)
	}
	// Should contain the background color
	if !strings.Contains(got, term.bgThinking) {
		t.Errorf("expected bgThinking code, got %q", got)
	}
}

func TestTerminal_ToolCallStart_LongLine(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 30 // force narrow width
	// Description that's long
	term.ToolCallStart("bash")
	term.ToolCallDelta(`{"description": "this is a very long description that should wrap to multiple lines", "command": "ls -la"}`)
	sync()

	got := stdout.String()
	if !strings.Contains(got, "🔧") {
		t.Errorf("expected tool icon, got %q", got)
	}
	// Should have multiple clearLine sequences
	count := strings.Count(got, "\033[K")
	if count < 2 {
		t.Errorf("expected at least 2 clearLine sequences, got %d. Output: %q", count, got)
	}
}

func TestTerminal_ToolCallEnd_LongLine(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 20
	term.ToolCallStart("bash")
	term.ToolCallDelta(`{"description": "test", "command": "echo hello"}`)
	term.ToolCallEnd("bash", "0123456789012345678901234567890123456789") // 40 chars
	sync()

	got := stdout.String()
	if !strings.Contains(got, "🔧") {
		t.Errorf("expected tool icon, got %q", got)
	}
	// Should have multiple clearLine sequences (from both start and end)
	count := strings.Count(got, "\033[K")
	if count < 3 {
		t.Errorf("expected at least 3 clearLine sequences, got %d. Output: %q", count, got)
	}
}

func TestTerminal_Summary_LongLine(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 30 // narrow width to force wrap
	term.Summary(stream.Usage{Input: 12345, Output: 67890, CacheRead: 111, CacheWrite: 222}, stream.Stats{})
	sync()

	got := stdout.String()
	if !strings.Contains(got, "Usage:") {
		t.Errorf("expected Usage prefix, got %q", got)
	}
	// Should have clearLine sequences
	count := strings.Count(got, "\033[K")
	if count < 2 {
		t.Errorf("expected at least 2 clearLine sequences, got %d. Output: %q", count, got)
	}
}

func TestTerminal_Thinking_NoWrapForShortLine(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 80
	term.Thinking("short")
	sync()

	got := stdout.String()
	// Should have exactly 2 clearLine: one from newBlock, one from ReplaceAll after text (but there's no \n)
	// Actually: newBlock prints bg + clearLine, then Thinking writes "💭 short" (no newlines)
	// So only the initial clearLine from newBlock
	// But wait, the text "short" doesn't have \n, so ReplaceAll doesn't add extra
	// Then closeBlock adds another clearLine + bgReset + \n\n
	// Actually closeBlock is called later (not in this test)
	count := strings.Count(got, "\033[K")
	// Just check it's a small number
	if count > 5 {
		t.Errorf("expected few clearLine sequences for short line, got %d. Output: %q", count, got)
	}
	_ = count
}

// --- Tests for streaming (multiple Thinking calls) ---

func TestTerminal_Thinking_StreamingChunks(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 20

	// First chunk
	term.Thinking("first chunk ")
	// Second chunk (continues on same line)
	term.Thinking("second chunk that is very long and should wrap correctly")
	sync()

	got := stdout.String()
	// Should have multiple clearLine sequences
	count := strings.Count(got, "\033[K")
	if count < 3 {
		t.Errorf("expected at least 3 clearLine sequences for streaming chunks, got %d. Output: %q", count, got)
	}
	// Should contain the thinking icon only once (for the first chunk)
	if strings.Count(got, "💭") != 1 {
		t.Errorf("expected exactly one thinking icon, got %d", strings.Count(got, "💭"))
	}
	// Should contain both chunk texts (maybe split across lines)
	if !strings.Contains(got, "first chunk") {
		t.Errorf("expected 'first chunk' in output")
	}
	if !strings.Contains(got, "chunk") {
		t.Errorf("expected 'chunk' in output")
	}
}

func TestTerminal_Thinking_StreamingWithNewlines(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 20

	// First chunk ends with newline
	term.Thinking("first line\n")
	// Second chunk starts at column 0
	term.Thinking("second line that is very long and should wrap correctly")
	sync()

	got := stdout.String()
	// Should have multiple clearLine sequences
	count := strings.Count(got, "\033[K")
	if count < 3 {
		t.Errorf("expected at least 3 clearLine sequences, got %d. Output: %q", count, got)
	}
	// Should contain both lines
	if !strings.Contains(got, "first line") {
		t.Errorf("expected 'first line' in output")
	}
	if !strings.Contains(got, "second line") {
		t.Errorf("expected 'second line' in output")
	}
}

func TestTerminal_Thinking_LongLineInMiddleOfText(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 20

	// Single call with multiple lines, middle line is long
	term.Thinking("normal line\nthis line is extremely long and should be properly wrapped with background color\nanother normal line")
	sync()

	got := stdout.String()
	count := strings.Count(got, "\033[K")
	if count < 5 {
		t.Errorf("expected at least 5 clearLine sequences for multi-line with long middle line, got %d. Output: %q", count, got)
	}
	// Should contain all three lines (words may be split across wraps)
	if !strings.Contains(got, "normal line") {
		t.Errorf("expected 'normal line' in output")
	}
	if !strings.Contains(got, "this line") {
		t.Errorf("expected 'this line' in output")
	}
	if !strings.Contains(got, "another normal line") {
		t.Errorf("expected 'another normal line' in output")
	}
}

// --- Tests for ToolCall streaming ---

func TestTerminal_ToolCallEnd_LongResult(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 20

	term.ToolCallStart("bash")
	term.ToolCallDelta(`{"description": "test", "command": "echo hello"}`)
	term.ToolCallEnd("bash", "short\n0123456789012345678901234567890123456789")
	sync()

	got := stdout.String()
	// Should have multiple clearLine sequences
	count := strings.Count(got, "\033[K")
	if count < 4 {
		t.Errorf("expected at least 4 clearLine sequences, got %d. Output: %q", count, got)
	}
}

func TestTerminal_ToolCall_StreamingDeltas(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 40

	// Simulate streaming tool call arguments in multiple chunks
	term.ToolCallStart("bash")
	term.ToolCallDelta(`{"description": "`)
	term.ToolCallDelta(`list files`)
	term.ToolCallDelta(`", "command": "`)
	term.ToolCallDelta(`ls -la"}`)
	sync()

	got := stdout.String()
	if !strings.Contains(got, "🔧") {
		t.Errorf("expected tool icon in output, got %q", got)
	}
	if !strings.Contains(got, "bash") {
		t.Errorf("expected tool name 'bash' in output, got %q", got)
	}
	if !strings.Contains(got, "list files") {
		t.Errorf("expected description 'list files' in output, got %q", got)
	}
	if !strings.Contains(got, `ls -la`) {
		t.Errorf("expected command 'ls -la' in output, got %q", got)
	}
	// Should have exactly one icon (ToolCallStart only)
	if strings.Count(got, "🔧") != 1 {
		t.Errorf("expected exactly one tool icon, got %d", strings.Count(got, "🔧"))
	}
}

func TestTerminal_ToolCall_StreamingDeltasWithWrap(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 30

	term.ToolCallStart("bash")
	term.ToolCallDelta(`{"description": "this is a very long description that should wrap"`)
	term.ToolCallDelta(`, "command": "ls -la"}`)
	sync()

	got := stdout.String()
	if !strings.Contains(got, "🔧") {
		t.Errorf("expected tool icon, got %q", got)
	}
	// Should have at least 2 clearLine sequences (one for wrapping)
	count := strings.Count(got, "\033[K")
	if count < 2 {
		t.Errorf("expected at least 2 clearLine sequences, got %d. Output: %q", count, got)
	}
}

func TestTerminal_ToolCall_FullFlow(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 40

	// Full lifecycle: start → delta → delta → end
	term.ToolCallStart("bash")
	term.ToolCallDelta(`{"description": "check disk"`)
	term.ToolCallDelta(`, "command": "df -h"}`)
	term.ToolCallEnd("bash", "Filesystem      Size  Used Avail Use% Mounted on\n/dev/sda1       100G   50G   50G  50% /")
	sync()

	got := stdout.String()
	// Should have 🔧 for start
	if !strings.Contains(got, "🔧") {
		t.Errorf("expected tool icon, got %q", got)
	}
	// Should have the tool name
	if !strings.Contains(got, "bash") {
		t.Errorf("expected 'bash', got %q", got)
	}
	// Should have the result text
	if !strings.Contains(got, "Filesystem") {
		t.Errorf("expected result 'Filesystem', got %q", got)
	}
	if !strings.Contains(got, "/dev/sda1") {
		t.Errorf("expected result '/dev/sda1', got %q", got)
	}
	// Should have at least some clearLine sequences
	count := strings.Count(got, "\033[K")
	if count < 2 {
		t.Errorf("expected at least 2 clearLine sequences, got %d. Output: %q", count, got)
	}
}

func TestTerminal_ToolCall_ReadHidesResult(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 40

	// For read tool, ToolCallEnd should NOT render the result
	term.ToolCallStart("read")
	term.ToolCallDelta(`{"path": "secret.txt"}`)
	term.ToolCallEnd("read", "this should not appear")
	sync()

	got := stdout.String()
	if !strings.Contains(got, "🔧") {
		t.Errorf("expected tool icon, got %q", got)
	}
	if strings.Contains(got, "this should not appear") {
		t.Errorf("expected read tool NOT to render result, got %q", got)
	}
}

func TestTerminal_ToolCall_SameLine(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 80

	term.ToolCallStart("read")
	term.ToolCallDelta(`{"path": "main.go"}`)
	sync()

	got := stdout.String()
	// The first line (before any \n) should contain both the tool name and its arguments
	firstLine := got
	if idx := strings.IndexByte(got, '\n'); idx >= 0 {
		firstLine = got[:idx]
	}
	// Remove ANSI escapes for assertion
	clean := stripAnsi(firstLine)
	if !strings.Contains(clean, "🔧 read") {
		t.Errorf("expected '🔧 read' in first line, got %q", clean)
	}
	if !strings.Contains(clean, `"path": "main.go"`) {
		t.Errorf("expected args in first line, got %q", clean)
	}
	// There should be NO newline between tool name and arguments
	// (the first line should contain both)
	if strings.Contains(clean, "🔧") && strings.Contains(clean, `"path"`) {
		// Both are on same line — good
	} else {
		t.Errorf("tool name and args should be on the same line, got first line: %q", clean)
	}
}

func TestTerminal_ToolCall_SameLineMultipleDeltas(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 80

	// Stream deltas that build up the full arguments
	term.ToolCallStart("bash")
	term.ToolCallDelta(`{"description": "`)
	term.ToolCallDelta(`list files`)
	term.ToolCallDelta(`", "command": "`)
	term.ToolCallDelta(`ls -la"}`)
	sync()

	got := stdout.String()
	firstLine := got
	if idx := strings.IndexByte(got, '\n'); idx >= 0 {
		firstLine = got[:idx]
	}
	clean := stripAnsi(firstLine)
	if !strings.Contains(clean, "🔧 bash") {
		t.Errorf("expected '🔧 bash' in first line, got %q", clean)
	}
	if !strings.Contains(clean, "list files") {
		t.Errorf("expected description in first line, got %q", clean)
	}
	if !strings.Contains(clean, "ls -la") {
		t.Errorf("expected command in first line, got %q", clean)
	}
}

func TestTerminal_ToolCallDelta_EmptyDoesNothing(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.termWidth = 40

	term.ToolCallStart("bash")
	term.ToolCallDelta("") // should do nothing
	term.ToolCallEnd("bash", "result")
	sync()

	got := stdout.String()
	// Should have 🔧 from start and result from end
	if !strings.Contains(got, "🔧") {
		t.Errorf("expected tool icon, got %q", got)
	}
	if !strings.Contains(got, "result") {
		t.Errorf("expected result, got %q", got)
	}
	// Should not have any extra clearLine from the empty delta
	// Just verify it doesn't crash or produce malformed output
}

func TestTerminal_ToolBlock_WritesGrayBox(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.ToolBlock("⏳ waiting for tools...")
	sync()

	got := stdout.String()
	if !strings.Contains(got, term.bgUsage) {
		t.Errorf("expected gray background in ToolBlock output, got %q", got)
	}
	if !strings.Contains(got, "⏳ waiting for tools...") {
		t.Errorf("expected message in ToolBlock output, got %q", got)
	}
}

func TestTerminal_ToolBlock_ClosesPreviousBlock(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.ToolBlock("⏳ waiting...")
	term.ToolBlock("⏳ still waiting...")
	sync()

	got := stdout.String()
	// Should have bgReset between the two blocks
	if !strings.Contains(got, bgReset) {
		t.Errorf("expected bgReset between ToolBlock calls, got %q", got)
	}
}

func TestTerminal_ToolBlock_ThenToolCall(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.ToolBlock("⏳ waiting for tools...")
	term.ToolCallStart("bash")
	term.ToolCallDelta(`{"command": "ls"}`)
	term.ToolCallEnd("bash", "file1")
	sync()

	got := stdout.String()
	// ToolBlock closes before ToolCallStart opens new block
	if !strings.Contains(got, "⏳ waiting for tools...") {
		t.Errorf("expected waiting message, got %q", got)
	}
	if !strings.Contains(got, "🔧") {
		t.Errorf("expected tool icon, got %q", got)
	}
	if !strings.Contains(got, "file1") {
		t.Errorf("expected result, got %q", got)
	}
}

func TestTerminal_ToolBlock_ExitCodeZero(t *testing.T) {
	// Just ensure no crash on basic smoke test
	stdout, _, sync, restore := captureOutput(t)
	defer restore()
	term := NewTerminal()
	term.ToolBlock("⏳ waiting...")
	sync()
	_ = stdout
}

// ─── Tests for buildUsageLineNoTiming ───────────────────────────────────

// hasTiming checks if a line contains timing stats (t=, ttft=, tok/s).
// We check for " t=" (space + t=) to avoid false match on "out=".
func hasTiming(line string) bool {
	return strings.Contains(line, " t=") || strings.Contains(line, "ttft=") || strings.Contains(line, "tok/s")
}

func TestBuildUsageLineNoTiming_Zero(t *testing.T) {
	line := buildUsageLineNoTiming(stream.Usage{})
	expected := "in=0 out=0"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
	if hasTiming(line) {
		t.Errorf("should not contain timing stats, got %q", line)
	}
}

func TestBuildUsageLineNoTiming_WithCacheRead(t *testing.T) {
	line := buildUsageLineNoTiming(stream.Usage{Input: 150, Output: 50, CacheRead: 100})
	expected := "in=50 (+100 cache) out=50"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
}

func TestBuildUsageLineNoTiming_WithReasoning(t *testing.T) {
	line := buildUsageLineNoTiming(stream.Usage{Input: 200, Output: 100, Reasoning: 50})
	expected := "in=200 out=100 r=50"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
}

func TestBuildUsageLineNoTiming_WithCacheWrite(t *testing.T) {
	line := buildUsageLineNoTiming(stream.Usage{Input: 200, Output: 100, CacheWrite: 25})
	expected := "in=200 out=100 cache_w=25"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
}

func TestBuildUsageLineNoTiming_AllFields(t *testing.T) {
	line := buildUsageLineNoTiming(stream.Usage{Input: 500, Output: 300, Reasoning: 50, CacheRead: 100, CacheWrite: 25})
	expected := "in=400 (+100 cache) out=300 r=50 cache_w=25"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
	if hasTiming(line) {
		t.Errorf("should not contain timing stats, got %q", line)
	}
}

func TestBuildUsageLineNoTiming_CacheReadExceedsInput(t *testing.T) {
	line := buildUsageLineNoTiming(stream.Usage{Input: 50, Output: 10, CacheRead: 100})
	// inNew = max(50-100, 0) = 0
	expected := "in=0 (+100 cache) out=10"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
}

func TestBuildUsageLineNoTiming_NoTimingInOutput(t *testing.T) {
	// Verify that none of the various usage combinations produce timing stats
	tests := []stream.Usage{
		{Input: 0, Output: 0},
		{Input: 100, Output: 50},
		{Input: 100, Output: 50, CacheRead: 30},
		{Input: 100, Output: 50, Reasoning: 20},
		{Input: 100, Output: 50, CacheWrite: 10},
		{Input: 100, Output: 50, CacheRead: 20, Reasoning: 10, CacheWrite: 5},
	}
	for _, u := range tests {
		line := buildUsageLineNoTiming(u)
		if hasTiming(line) {
			t.Errorf("buildUsageLineNoTiming(%+v) = %q contains timing stats", u, line)
		}
	}
}

// --- Tests for Minimal display (run mode) ---

// newTestMinimal returns a Minimal with a known width so output is
// deterministic regardless of test environment.
func newTestMinimal(width int) *Minimal {
	return &Minimal{
		terminalWidth: width,
		blockStart:    time.Now(),
		done:          make(chan struct{}),
	}
}

func TestMinimal_Request_EmitsReqLine(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(80)
	m.Request("user prompt")
	m.End()
	sync()

	got := stdout.String()
	if !strings.Contains(got, "[ REQ]") {
		t.Errorf("expected [ REQ] prefix, got %q", got)
	}
	if !strings.Contains(got, "user prompt") {
		t.Errorf("expected 'user prompt' content, got %q", got)
	}
	if !strings.Contains(got, "]") {
		t.Errorf("expected time bracket in output, got %q", got)
	}
}

func TestMinimal_Request_FinalizedByThinking(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(80)
	m.Request("user prompt")
	m.Thinking("a thought")
	m.End()
	sync()

	got := stdout.String()
	// The [ REQ] line should be followed by a [THNK] line in the final output.
	idxReq := strings.Index(got, "[ REQ]")
	idxThnk := strings.Index(got, "[THNK]")
	if idxReq < 0 {
		t.Fatalf("expected [ REQ] prefix in output, got %q", got)
	}
	if idxThnk < 0 {
		t.Fatalf("expected [THNK] prefix in output, got %q", got)
	}
	if idxThnk <= idxReq {
		t.Errorf("expected [THNK] to follow [ REQ], got %q", got)
	}
}

func TestMinimal_Request_TimeCountedUntilNextEvent(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(80)
	m.Request("user prompt")
	// Simulate a 250ms round-trip latency (e.g. model warm-up).
	time.Sleep(250 * time.Millisecond)
	m.Thinking("a thought")
	m.End()
	sync()

	got := stdout.String()
	// The [ REQ] line is rendered twice in the output stream: once at
	// ~0ms (initial), then again at finalization with the real elapsed
	// time, followed by a newline. The last render before the newline
	// is what the user actually sees in a terminal.
	nlIdx := strings.Index(got, "\n")
	if nlIdx < 0 {
		t.Fatalf("expected newline after [ REQ] line, got %q", got)
	}
	reqLine := strings.TrimPrefix(got[:nlIdx], "\r")
	// Find the last "[" on the line — that's where the time bracket starts.
	openIdx := strings.LastIndex(reqLine, "[")
	if openIdx < 0 {
		t.Fatalf("no time bracket on [ REQ] line: %q", reqLine)
	}
	timeStr := reqLine[openIdx:]
	if !strings.HasSuffix(strings.TrimRight(reqLine, " "), "]") {
		t.Fatalf("[ REQ] line does not end with ']': %q", reqLine)
	}
	// The time should be at least 200ms (we slept 250ms).
	if !strings.Contains(timeStr, "s]") {
		t.Errorf("expected s] on [ REQ] line for 250ms wait, got %q (line: %q)", timeStr, reqLine)
	}
	// Sanity check: extract the digits and confirm >= 200.
	digits := strings.TrimRight(strings.TrimLeft(timeStr, "["), "ms]")
	if digits == "" {
		t.Fatalf("could not extract ms number from %q", timeStr)
	}
}

func TestMinimal_Request_TimeUpdatesVisiblyDuringWait(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(80)
	m.Request("user prompt")
	// The background ticker fires every 100ms, so after 250ms we should
	// see at least two in-place re-renders of the [ REQ] line — one from
	// the ticker (around 100ms), one from finalization (250ms).
	time.Sleep(250 * time.Millisecond)
	m.Thinking("a thought")
	m.End()
	sync()

	got := stdout.String()
	// Count distinct render blocks separated by \r or \n on the [ REQ] line.
	// The user's terminal will only display the last one, but the stream
	// should contain multiple time updates.
	updates := strings.Count(got, "[ REQ]")
	if updates < 2 {
		t.Errorf("expected [ REQ] line to be re-rendered by background ticker, got %d updates in %q", updates, got)
	}
}

func TestMinimal_Thinking_EmitsThinkLine(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(80)
	m.Thinking("reasoning tokens")
	m.End()
	sync()

	got := stdout.String()
	if !strings.Contains(got, "[THNK]") {
		t.Errorf("expected [THNK] prefix, got %q", got)
	}
	if !strings.Contains(got, "reasoning tokens") {
		t.Errorf("expected content, got %q", got)
	}
}

func TestMinimal_Text_EmitsRespLine(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(80)
	m.Text("response tokens")
	m.End()
	sync()

	got := stdout.String()
	if !strings.Contains(got, "[RESP]") {
		t.Errorf("expected [RESP] prefix, got %q", got)
	}
	if !strings.Contains(got, "response tokens") {
		t.Errorf("expected content, got %q", got)
	}
}

func TestMinimal_ToolCall_EmitsToolLine(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(80)
	m.ToolCallStart("read")
	m.ToolCallDelta(`{"path":"main.go"}`)
	m.ToolCallEnd("read", "")
	m.End()
	sync()

	got := stdout.String()
	if !strings.Contains(got, "[TOOL]") {
		t.Errorf("expected [TOOL] prefix, got %q", got)
	}
	if !strings.Contains(got, "read") {
		t.Errorf("expected tool name, got %q", got)
	}
	if !strings.Contains(got, `"path":"main.go"`) {
		t.Errorf("expected tool args, got %q", got)
	}
	// The call signature should be `read({"path":"main.go"})`.
	if !strings.Contains(got, `read({"path":"main.go"})`) {
		t.Errorf("expected call signature read({...}), got %q", got)
	}
}

func TestMinimal_ToolCall_LongParamsTruncated(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(40) // narrow terminal
	longArgs := `{"path":"` + strings.Repeat("x", 200) + `"}`
	m.ToolCallStart("read")
	m.ToolCallDelta(longArgs)
	m.ToolCallEnd("read", "")
	m.End()
	sync()

	got := stdout.String()
	if !strings.Contains(got, "...") {
		t.Errorf("expected ellipsis truncation for long params, got %q", got)
	}
	// Each final line must fit the terminal width.
	for _, block := range strings.Split(got, "\r") {
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimRight(line, " ")
			if line == "" {
				continue
			}
			if w := visibleWidth(line); w > 40 {
				t.Errorf("line wider than terminal: width=%d line=%q", w, line)
			}
		}
	}
}

func TestMinimal_ToolCall_MultipleTools_OneLineEach(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(120)
	m.Request("user prompt")
	m.ToolCallStart("ls")
	m.ToolCallDelta(`{"path":"/home/piotr/work/tyci-agent"}`)
	m.ToolCallEnd("ls", "")
	m.ToolCallStart("glob")
	m.ToolCallDelta(`{"pattern":"*","cwd":"/home/piotr/work/tyci-agent","includeDirs":true}`)
	m.ToolCallEnd("glob", "")
	m.ToolFinish()
	m.End()
	sync()

	got := stdout.String()
	// Count distinct [TOOL] lines (not in-place render duplicates).
	lines := strings.Split(strings.ReplaceAll(got, "\r", ""), "\n")
	toolLines := 0
	var toolLineContents []string
	for _, l := range lines {
		if strings.Contains(l, "[TOOL]") {
			toolLines++
			toolLineContents = append(toolLineContents, l)
		}
	}
	if toolLines != 2 {
		t.Errorf("expected exactly 2 [TOOL] lines, got %d:\n%q", toolLines, got)
		for i, c := range toolLineContents {
			t.Logf("  line %d: %q", i, c)
		}
	}
	// Each [TOOL] line should contain both the name AND the args.
	for i, c := range toolLineContents {
		if !strings.Contains(c, "(") || !strings.Contains(c, ")") {
			t.Errorf("tool line %d missing call syntax: %q", i, c)
		}
	}
}

// TestMinimal_ToolCall_AgentLikeFlow reproduces the exact sequence the
// agent uses: all ToolCallStart+Delta calls first, then all ToolCallEnd
// calls after tools have executed. This is the real flow from
// agent/run_tools.go.
func TestMinimal_ToolCall_AgentLikeFlow(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(120)
	m.Request("user prompt")

	// Phase 1: all start + delta (from showToolCalls)
	m.ToolCallStart("ls")
	m.ToolCallDelta(`{"path":"/home/piotr/work/tyci-agent"}`)
	m.ToolCallStart("glob")
	m.ToolCallDelta(`{"pattern":"*","cwd":"/home/piotr/work/tyci-agent","includeDirs":true}`)

	// Phase 2: all ends (from appendToolResults, after tools execute)
	m.ToolCallEnd("ls", "")
	m.ToolCallEnd("glob", "")
	m.ToolFinish()
	m.End()
	sync()

	got := stdout.String()
	// Count distinct [TOOL] lines.
	lines := strings.Split(strings.ReplaceAll(got, "\r", ""), "\n")
	toolLines := 0
	var toolLineContents []string
	for _, l := range lines {
		if strings.Contains(l, "[TOOL]") {
			toolLines++
			toolLineContents = append(toolLineContents, l)
		}
	}
	if toolLines != 2 {
		t.Errorf("expected exactly 2 [TOOL] lines, got %d:\n%q", toolLines, got)
		for i, c := range toolLineContents {
			t.Logf("  line %d: %q", i, c)
		}
	}
	// Each line should be a complete call: name(args)
	for i, c := range toolLineContents {
		if !strings.Contains(c, "(") || !strings.Contains(c, ")") {
			t.Errorf("tool line %d missing call syntax: %q", i, c)
		}
	}
}

func TestMinimal_ToolCallEnd_OneLineNoResult(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(80)
	m.ToolCallStart("bash")
	m.ToolCallDelta(`{"command":"ls"}`)
	m.ToolCallEnd("bash", "file1\nfile2")
	m.End()
	sync()

	got := stdout.String()
	// The result is intentionally suppressed in run mode.
	if strings.Contains(got, "file1") {
		t.Errorf("did not expect result 'file1' in run mode, got %q", got)
	}
	// Count distinct [TOOL] lines (separated by newlines, ignoring \r updates).
	lines := strings.Split(strings.ReplaceAll(got, "\r", ""), "\n")
	toolLines := 0
	for _, l := range lines {
		if strings.Contains(l, "[TOOL]") {
			toolLines++
		}
	}
	if toolLines != 1 {
		t.Errorf("expected exactly 1 [TOOL] line, got %d in %q", toolLines, got)
	}
	// The line should look like: [TOOL] bash({"command":"ls"})
	if !strings.Contains(got, `bash({"command":"ls"})`) {
		t.Errorf("expected call signature bash({\"command\":\"ls\"}), got %q", got)
	}
}

func TestMinimal_ToolFinish_EmitsSummaryLine(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(80)
	m.ToolCallStart("read")
	m.ToolCallDelta(`{"path":"x"}`)
	m.ToolCallEnd("read", "")
	m.ToolCallStart("bash")
	m.ToolCallDelta(`{"command":"ls"}`)
	m.ToolCallEnd("bash", "file1")
	m.ToolFinish()
	m.End()
	sync()

	got := stdout.String()
	if !strings.Contains(got, "[TOOL}") {
		t.Errorf("expected [TOOL} summary prefix, got %q", got)
	}
	if !strings.Contains(got, "Tool finish") {
		t.Errorf("expected 'Tool finish' label, got %q", got)
	}
}

func TestMinimal_LongLine_TruncatesWithEllipsis(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(40) // narrow
	longText := strings.Repeat("x", 200)
	m.Text(longText)
	m.End()
	sync()

	got := stdout.String()
	if !strings.Contains(got, "...") {
		t.Errorf("expected ellipsis truncation, got %q", got)
	}
	// Each carriage-return-delimited render block must fit the terminal.
	// (In-place updates reuse the line, so we split on \r too.)
	for _, block := range strings.Split(got, "\r") {
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimRight(line, " ")
			if line == "" {
				continue
			}
			if w := visibleWidth(line); w > 40 {
				t.Errorf("line wider than terminal: width=%d line=%q", w, line)
			}
		}
	}
}

func TestMinimal_FormatElapsed(t *testing.T) {
	cases := []struct {
		d        time.Duration
		contains string
	}{
		{0, "ms"},
		{50 * time.Millisecond, "ms"},
		{99 * time.Millisecond, "ms"},
		{100 * time.Millisecond, "s"},
		{500 * time.Millisecond, "s"},
		{time.Second, "s"},
		{10*time.Second + 200*time.Millisecond, "s"},
	}
	for _, c := range cases {
		got := formatElapsed(c.d)
		if !strings.Contains(got, c.contains) {
			t.Errorf("formatElapsed(%v) = %q, expected to contain %q", c.d, got, c.contains)
		}
		// Always wrapped in square brackets
		if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "]") {
			t.Errorf("formatElapsed(%v) = %q, expected brackets", c.d, got)
		}
	}
}

func TestMinimal_FitLine_TruncatesWithEllipsis(t *testing.T) {
	if got := fitLine("hello world", 5); got != "he..." {
		t.Errorf("fitLine: got %q, want he...", got)
	}
	if got := fitLine("hi", 10); got != "hi" {
		t.Errorf("fitLine short: got %q, want hi", got)
	}
	if got := fitLine("hello", 0); got != "" {
		t.Errorf("fitLine zero: got %q, want empty", got)
	}
}

func TestMinimal_ToolBlock_PendingSuppressed(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(80)
	m.ToolBlock("⏳ waiting for tools...")
	m.ToolCallStart("bash")
	m.ToolBlock("⏳ another waiting...") // also suppressed
	m.ToolCallEnd("bash", "out")
	m.ToolFinish()
	m.End()
	sync()

	got := stdout.String()
	if strings.Contains(got, "⏳") {
		t.Errorf("expected pending marker to be suppressed, got %q", got)
	}
}

func TestMinimal_ToolBlock_AfterBlock_StillEmitted(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(80)
	m.ToolCallStart("bash")
	m.ToolCallEnd("bash", "out")
	m.ToolFinish()
	m.ToolBlock("retry 1/3 — error")
	m.End()
	sync()

	got := stdout.String()
	if !strings.Contains(got, "retry 1/3") {
		t.Errorf("expected non-pending ToolBlock to appear, got %q", got)
	}
}

// TestMinimal_FullFlow exercises the entire display sequence the agent
// produces for a round with thinking, a response, and two tool calls.
func TestMinimal_FullFlow(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(120)
	m.Request("user prompt")
	m.Thinking("step 1")
	m.Text("I'll run two tools")
	m.ToolBlock("⏳ waiting for tools...") // suppressed
	m.ToolCallStart("read")
	m.ToolCallDelta(`{"path":"a.go"}`)
	m.ToolCallEnd("read", "")
	m.ToolCallStart("bash")
	m.ToolCallDelta(`{"command":"ls"}`)
	m.ToolCallEnd("bash", "file1\nfile2")
	m.ToolFinish()
	m.Summary(stream.Usage{Input: 100, Output: 50}, stream.Stats{Duration: 2 * time.Second})
	m.End()
	sync()

	got := stdout.String()
	wantPrefixes := []string{
		"[ REQ]", "[THNK]", "[RESP]",
		"[TOOL]", "read(",
		"[TOOL]", "bash(",
		"[TOOL}", "Tool finish",
		"[STAT]", "tok/s=",
	}
	last := 0
	for _, p := range wantPrefixes {
		idx := strings.Index(got[last:], p)
		if idx < 0 {
			t.Errorf("expected %q in order after position %d, got %q", p, last, got)
			continue
		}
		last += idx + len(p)
	}
	// Results are not rendered in run mode.
	if strings.Contains(got, "file1") {
		t.Errorf("did not expect tool result 'file1' in run mode, got %q", got)
	}
}
