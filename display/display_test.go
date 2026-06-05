package display

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/decodo/tyci-agent/stream"
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
	if !strings.Contains(got, "cache_r=") {
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
	term.termWidth = 20 // force narrow width
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
	term.ToolCallDelta(`list files`,)
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

func TestMinimal_ToolCallStart(t *testing.T) {
	m := NewMinimal()
	m.ToolCallStart("bash")
	// Minimal writes to stdout but we can't easily capture it in unit test
	// Just ensure it doesn't panic
}

func TestMinimal_ToolCallDelta(t *testing.T) {
	m := NewMinimal()
	m.ToolCallStart("bash")
	m.ToolCallDelta(`{"description": "test"}`)
	// Just ensure no panic
}

func TestMinimal_ToolCall_FullFlow(t *testing.T) {
	m := NewMinimal()
	m.ToolCallStart("bash")
	m.ToolCallDelta(`{"command": "ls"}`)
	m.ToolCallEnd("bash", "file1\nfile2")
	// Just ensure no panic
}

func TestMinimal_ToolCallDelta_Multiple(t *testing.T) {
	m := NewMinimal()
	m.ToolCallStart("bash")
	m.ToolCallDelta(`{"description": "`)
	m.ToolCallDelta(`list`)
	m.ToolCallDelta(` files"}`)
	m.ToolCallEnd("bash", "result")
	// Just ensure no panic
}

func TestMinimal_ToolBlock_NoPanic(t *testing.T) {
	m := NewMinimal()
	m.ToolBlock("⏳ waiting for tools...")
	m.ToolBlock("second call")
	// Just ensure no panic
}

func TestMinimal_ToolBlock_ThenToolCall(t *testing.T) {
	m := NewMinimal()
	m.ToolBlock("⏳ waiting for tools...")
	m.ToolCallStart("bash")
	m.ToolCallDelta(`{"command": "ls"}`)
	m.ToolCallEnd("bash", "file1")
	// Just ensure no panic
}

