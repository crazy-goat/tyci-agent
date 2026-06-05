package display

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
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

func TestJSON_Chunk_BuffersText(t *testing.T) {
	j := NewJSON()
	j.Chunk("hello ")
	j.Chunk("world")

	if got := j.Text(); got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestJSON_End_OutputsEnvelope(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	j := NewJSON()
	j.Chunk("some response text")
	j.End()
	sync()

	var parsed map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("expected valid JSON, got error: %v\noutput: %s", err, stdout.String())
	}
	if got, ok := parsed["response"].(string); !ok || got != "some response text" {
		t.Errorf("expected response 'some response text', got %v", parsed["response"])
	}
}

func TestJSON_End_PassesThroughValidJSON(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	j := NewJSON()
	j.Chunk(`{"foo": "bar", "n": 42}`)
	j.End()
	sync()

	var parsed map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("expected valid JSON, got error: %v\noutput: %s", err, stdout.String())
	}
	if got := parsed["foo"]; got != "bar" {
		t.Errorf("expected foo=bar, got %v", got)
	}
}

func TestJSON_End_IncludesToolCalls(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	j := NewJSON()
	j.Chunk("I'll read the file")
	j.ToolCallStart("read")
	j.ToolCallArg(`{"path": "x.txt"}`)
	j.EndToolCall()
	j.ToolResult("read", &ToolResult{Success: true, Content: "file content"})
	j.End()
	sync()

	var parsed map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("expected valid JSON, got error: %v\noutput: %s", err, stdout.String())
	}
	if got, ok := parsed["response"].(string); !ok || got != "I'll read the file" {
		t.Errorf("expected response, got %v", parsed["response"])
	}
	calls, ok := parsed["tool_calls"].([]interface{})
	if !ok || len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %v", parsed["tool_calls"])
	}
	tc := calls[0].(map[string]interface{})
	if tc["Name"] != "read" {
		t.Errorf("expected name 'read', got %v", tc["Name"])
	}
	if tc["Arguments"] != `{"path": "x.txt"}` {
		t.Errorf("expected arguments, got %v", tc["Arguments"])
	}
}

func TestJSON_End_EmptyTextNoOutput(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	j := NewJSON()
	j.End()
	sync()

	if stdout.Len() != 0 {
		t.Errorf("expected no output for empty text, got %q", stdout.String())
	}
}

func TestJSON_ThinkingIgnored(t *testing.T) {
	j := NewJSON()
	j.Thinking("internal reasoning")
	j.EndThinking()

	if got := j.Text(); got != "" {
		t.Errorf("expected empty text, got %q", got)
	}
}

func TestJSON_ToolCallArgWithoutStartIgnored(t *testing.T) {
	j := NewJSON()
	j.ToolCallArg("orphaned arg")

	if len(j.toolCalls) != 0 {
		t.Errorf("expected no buffered tool calls, got %d", len(j.toolCalls))
	}
}

func TestJSON_AccumulatedArguments(t *testing.T) {
	j := NewJSON()
	j.ToolCallStart("bash")
	j.ToolCallArg(`{"command": "ls`)
	j.ToolCallArg(` -la"}`)
	j.EndToolCall()

	if len(j.toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(j.toolCalls))
	}
	expected := `{"command": "ls -la"}`
	if j.toolCalls[0].Arguments != expected {
		t.Errorf("expected arguments %q, got %q", expected, j.toolCalls[0].Arguments)
	}
}

func TestSilent_Chunk_BuffersText(t *testing.T) {
	s := NewSilent()
	s.Chunk("hello ")
	s.Chunk("world")

	if got := s.Text(); got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestSilent_End_ProducesNoOutput(t *testing.T) {
	stdout, stderr, sync, restore := captureOutput(t)
	defer restore()

	s := NewSilent()
	s.Chunk("some text")
	s.ToolCallStart("read")
	s.ToolCallArg(`{"path":"x.txt"}`)
	s.EndToolCall()
	s.ToolResult("read", &ToolResult{Success: true, Content: "content"})
	s.Summary(UsageInfo{InputTokens: 10, OutputTokens: 20})
	s.Error(nil)
	s.End()
	sync()

	if stdout.Len() != 0 {
		t.Errorf("expected no stdout, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no stderr, got %q", stderr.String())
	}
}

func TestSilent_ToolCalls(t *testing.T) {
	s := NewSilent()
	s.ToolCallStart("read")
	s.ToolCallArg(`{"path": "a.txt"}`)
	s.EndToolCall()
	s.ToolCallStart("bash")
	s.ToolCallArg(`{"command": "ls"}`)
	s.EndToolCall()

	calls := s.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}
	if calls[0].Name != "read" || calls[1].Name != "bash" {
		t.Errorf("expected names [read, bash], got [%s, %s]", calls[0].Name, calls[1].Name)
	}
}

func TestTerminal_Chunk_WritesStdout(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal(false, false, false)
	term.Chunk("hello")
	term.Chunk(" world")
	sync()

	got := stdout.String()
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestTerminal_Thinking_RespectsHideThinking(t *testing.T) {
	_, stderr, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal(true, false, false)
	term.Thinking("internal reasoning")
	term.EndThinking()
	sync()

	if stderr.Len() != 0 {
		t.Errorf("expected no stderr output when hideThinking, got %q", stderr.String())
	}
}

func TestTerminal_Thinking_RendersWithIconAndBackground(t *testing.T) {
	_, stderr, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal(false, false, false)
	term.Thinking("some thought")
	term.EndThinking()
	sync()

	got := stderr.String()
	if !strings.Contains(got, "💭") {
		t.Errorf("expected thinking output to contain 💭, got %q", got)
	}
	if !strings.Contains(got, term.bgThinking) {
		t.Errorf("expected output to contain bgThinking code %q, got %q", term.bgThinking, got)
	}
	if !strings.Contains(got, clearLine) {
		t.Errorf("expected output to contain clearLine, got %q", got)
	}
}

func TestTerminal_EndThinking_ClosesBlock(t *testing.T) {
	_, stderr, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal(false, false, false)
	term.Thinking("a")
	term.EndThinking()
	sync()

	got := stderr.String()
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("expected thinking block to end with blank line, got %q", got)
	}
}

func TestTerminal_ToolResult_BashWithDescription(t *testing.T) {
	_, stderr, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal(false, false, false)
	term.ToolCallStart("bash")
	term.ToolCallArg(`{"description": "list files", "command": "ls -la"}`)
	term.EndToolCall()
	term.ToolResult("bash", &ToolResult{Success: true, Content: "file1\nfile2"})
	term.End()
	sync()

	got := stderr.String()
	if !strings.Contains(got, "🔧") {
		t.Errorf("expected tool output to contain 🔧, got %q", got)
	}
	if !strings.Contains(got, "list files") {
		t.Errorf("expected output to contain description 'list files', got %q", got)
	}
	if !strings.Contains(got, "$ ls -la") {
		t.Errorf("expected output to contain command '$ ls -la', got %q", got)
	}
	if !strings.Contains(got, "file1") {
		t.Errorf("expected output to contain result 'file1', got %q", got)
	}
}

func TestTerminal_ToolResult_BashError(t *testing.T) {
	_, stderr, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal(false, false, false)
	term.ToolCallStart("bash")
	term.ToolCallArg(`{"description": "fail", "command": "false"}`)
	term.EndToolCall()
	term.ToolResult("bash", &ToolResult{Success: false, Error: "exit status 1"})
	term.End()
	sync()

	got := stderr.String()
	if !strings.Contains(got, "exit status 1") {
		t.Errorf("expected output to contain error 'exit status 1', got %q", got)
	}
}

func TestTerminal_ToolResult_ReadOnlyHeader(t *testing.T) {
	_, stderr, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal(false, false, false)
	term.ToolCallStart("read")
	term.ToolCallArg(`{"path": "x.txt"}`)
	term.EndToolCall()
	term.ToolResult("read", &ToolResult{Success: true, Content: "file content"})
	term.End()
	sync()

	got := stderr.String()
	if !strings.Contains(got, "🔧 read(") {
		t.Errorf("expected read header, got %q", got)
	}
	if strings.Contains(got, "file content") {
		t.Errorf("expected read tool NOT to render result body, got %q", got)
	}
}

func TestTerminal_ToolResult_RespectsHideTools(t *testing.T) {
	_, stderr, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal(false, true, false)
	term.ToolCallStart("bash")
	term.ToolCallArg(`{"description": "list", "command": "ls"}`)
	term.EndToolCall()
	term.ToolResult("bash", &ToolResult{Success: true, Content: "x"})
	term.End()
	sync()

	if stderr.Len() != 0 {
		t.Errorf("expected no stderr output when hideTools, got %q", stderr.String())
	}
}

func TestTerminal_Error_WritesToStderr(t *testing.T) {
	_, stderr, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal(false, false, false)
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
