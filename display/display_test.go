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

func TestSilent_Text_BuffersText(t *testing.T) {
	s := NewSilent()
	s.Text("hello ")
	s.Text("world")

	if got := s.Text2(); got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestSilent_AllMethods_NoOutput(t *testing.T) {
	stdout, stderr, sync, restore := captureOutput(t)
	defer restore()

	s := NewSilent()
	s.Thinking("ignored")
	s.ToolCall("read", `{"path": "x"}`, "content")
	s.Summary(stream.Usage{Input: 10, Output: 20})
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
	term.ToolCall("bash", `{"description": "list files", "command": "ls -la"}`, "file1\nfile2")
	sync()

	got := stdout.String()
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

func TestTerminal_ToolCall_BashError(t *testing.T) {
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	term := NewTerminal()
	term.ToolCall("bash", `{"description": "fail", "command": "false"}`, "exit status 1")
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
	term.ToolCall("read", `{"path": "x.txt"}`, "file content")
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
	term.Summary(stream.Usage{Input: 100, Output: 50, CacheRead: 200})
	sync()

	got := stdout.String()
	if !strings.Contains(got, "in=") || !strings.Contains(got, "out=") {
		t.Errorf("expected usage info, got %q", got)
	}
	if !strings.Contains(got, "cache_rd=") {
		t.Errorf("expected cache info, got %q", got)
	}
}
