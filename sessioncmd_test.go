package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/session"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// whatever it wrote. runSessionShow writes straight to os.Stdout (matching
// the rest of sessioncmd.go), so tests need to intercept that stream rather
// than a returned string.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return buf.String()
}

// TestRunSessionShow_ReadsRealEventFormat is the regression test for the bug
// where runSessionShow matched raw["type"] == "header" and a top-level
// "blocks" field, neither of which the session package ever writes. A real
// session has a type:"session" header line and message events whose tool
// calls are typed "toolCall" blocks (see session/session.go).
//
// The tool-result message is built exactly the way
// agent/session_log.go's writeToolResultSessionEvent builds it: role
// "toolResult", block Type "text" — production never writes a block whose
// Type is "toolResult", only whose message ROLE is. A fixture that invented
// a {Type:"toolResult"} block would let a double-counting bug in
// runSessionShow (counting both "toolCall" and "toolResult" block types)
// pass silently.
func TestRunSessionShow_ReadsRealEventFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	s, err := session.Open(path, "/some/project", "gpt-5", "openai")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	if err := s.WriteMessage("user", []session.ContentBlock{
		{Type: "text", Text: "hello"},
	}, nil); err != nil {
		t.Fatalf("WriteMessage user: %v", err)
	}
	if err := s.WriteMessage("assistant", []session.ContentBlock{
		{Type: "toolCall", ID: "tc1", Name: "read", Arguments: []byte(`{"path":"x"}`)},
	}, nil); err != nil {
		t.Fatalf("WriteMessage assistant: %v", err)
	}
	// Matches writeToolResultSessionEvent (agent/session_log.go): role
	// "toolResult", block Type "text".
	if err := s.WriteMessage("toolResult", []session.ContentBlock{
		{Type: "text", Text: "file contents", ToolCallID: "tc1", ToolName: "read"},
	}, nil); err != nil {
		t.Fatalf("WriteMessage toolResult: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cmd := sessionShowCmd
	out := captureStdout(t, func() {
		if err := runSessionShow(cmd, []string{path}); err != nil {
			t.Fatalf("runSessionShow: %v", err)
		}
	})

	if !strings.Contains(out, "CWD:     /some/project") {
		t.Errorf("expected header CWD to be read from the type:\"session\" line, got:\n%s", out)
	}
	if !strings.Contains(out, "Model:   gpt-5") {
		t.Errorf("expected header Model to be populated, got:\n%s", out)
	}
	if !strings.Contains(out, "Provider: openai") {
		t.Errorf("expected header Provider to be populated, got:\n%s", out)
	}
	if !strings.Contains(out, "3 messages") {
		t.Errorf("expected 3 message events counted, got:\n%s", out)
	}
	// Exactly 1: the single "toolCall" block. The tool-result message above
	// carries a "text" block, not a second "toolCall"/"toolResult" block, so
	// counting it too would be double-counting one tool invocation as two.
	if !strings.Contains(out, "1 tool calls") {
		t.Errorf("expected exactly the one toolCall block to be counted, got:\n%s", out)
	}
}

// TestRunSessionShow_SkipsCorruptAndTruncatedLines is the regression test for
// the hang: json.Decoder does not resynchronize after a syntax error, so a
// naive "continue on error, retry Decode" loop keeps handing it the same
// broken byte offset forever. A corrupt line in the middle of the file and a
// truncated final line (tyci killed mid-write, giving io.ErrUnexpectedEOF —
// not io.EOF) must each be skipped, not spun on, and the counts from the
// surrounding good lines must still come through.
func TestRunSessionShow_SkipsCorruptAndTruncatedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.jsonl")

	lines := []string{
		`{"type":"session","version":1,"id":"abc","timestamp":"2024-01-01T00:00:00Z","cwd":"/proj","model":"m1","provider":"p1"}`,
		`{"type":"message","id":"1","timestamp":"2024-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
		// Corrupt: truncated mid-object.
		`{"type":"mess`,
		`{"type":"message","id":"2","timestamp":"2024-01-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"tc1","name":"read"}]}}`,
	}
	content := strings.Join(lines, "\n") + "\n" +
		// Truncated final line: no closing braces, no trailing newline —
		// exactly what's left on disk if the process died mid-write.
		`{"type":"message","id":"3","timestamp":"2024-01-01T00:00:03Z","message":{"role"`

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Run runSessionShow with stdout redirected to a pipe, on its own
	// goroutine, so a regression back to the hanging json.Decoder loop
	// fails this test instead of blocking `go test` forever. t.Fatal must
	// only ever be called from the test's own goroutine, so errors from the
	// background goroutine are reported through the result struct instead.
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)

	orig := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("pipe: %v", pipeErr)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	go func() {
		res := result{err: runSessionShow(sessionShowCmd, []string{path})}
		w.Close()
		var buf bytes.Buffer
		io.Copy(&buf, r)
		res.out = buf.String()
		done <- res
	}()

	var out string
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("runSessionShow: %v", res.err)
		}
		out = res.out
	case <-time.After(5 * time.Second):
		t.Fatal("runSessionShow did not return — it hung on a corrupt/truncated line")
	}

	if !strings.Contains(out, "CWD:     /proj") {
		t.Errorf("expected header from the good first line to still be read, got:\n%s", out)
	}
	// Only the two well-formed message lines count; the corrupt middle line
	// and the truncated last line are skipped, not counted.
	if !strings.Contains(out, "2 messages") {
		t.Errorf("expected 2 messages counted (corrupt/truncated lines skipped), got:\n%s", out)
	}
	if !strings.Contains(out, "1 tool calls") {
		t.Errorf("expected 1 tool call counted from the good assistant line, got:\n%s", out)
	}
}

// F6 (item 10 inbox): "tyci session export --markdown" did not exist as a
// subcommand, so a session that never got compacted had no way to produce
// its searchable markdown dump on demand — only compaction ever called
// session.WriteMarkdownDump. This is the regression test for that gap.
func TestRunSessionExport_Markdown_WritesAndPrintsDump(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.jsonl")

	s, err := session.Open(path, "/some/project", "gpt-5", "openai")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	if err := s.WriteMessage("user", []session.ContentBlock{
		{Type: "text", Text: "find the bug in main.go"},
	}, nil); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cmd := sessionExportCmd
	if err := cmd.Flags().Set("markdown", "true"); err != nil {
		t.Fatalf("Set markdown flag: %v", err)
	}
	out := captureStdout(t, func() {
		if err := runSessionExport(cmd, []string{path}); err != nil {
			t.Fatalf("runSessionExport: %v", err)
		}
	})

	if !strings.Contains(out, "find the bug in main.go") {
		t.Errorf("expected the dump content on stdout, got:\n%s", out)
	}
	if !strings.Contains(out, "# tyci session dump") {
		t.Errorf("expected the dump's own header in stdout, got:\n%s", out)
	}

	dumpPath := strings.TrimSuffix(path, ".jsonl") + ".md"
	data, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("dump file was not written to disk: %v", err)
	}
	if string(data) != out {
		t.Errorf("stdout should be exactly the regenerated dump file's content:\nstdout:\n%s\nfile:\n%s", out, data)
	}
}

// Without --markdown, export must refuse rather than silently doing
// something else — there is currently exactly one supported format, and a
// caller that forgot the flag should get an actionable error, not output
// in a format they didn't ask for.
func TestRunSessionExport_RequiresMarkdownFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.jsonl")
	s, err := session.Open(path, "/some/project", "gpt-5", "openai")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cmd := sessionExportCmd
	if err := cmd.Flags().Set("markdown", "false"); err != nil {
		t.Fatalf("Set markdown flag: %v", err)
	}
	if err := runSessionExport(cmd, []string{path}); err == nil {
		t.Fatal("expected an error when --markdown is not set, got nil")
	}
}

// TestRunSessionShow_ReportsScanErrorInsteadOfSwallowingIt is the
// regression test for silently dropping scanner.Err(): bufio.Scanner does
// not resynchronize after bufio.ErrTooLong (a line bigger than the 8 MB
// buffer) — Scan() just returns false, and every line after the oversized
// one goes unread. Before this fix, runSessionShow printed only the counts
// from before the oversized line and exited 0 with no indication anything
// was skipped. Now it must say so.
func TestRunSessionShow_ReportsScanErrorInsteadOfSwallowingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "toolong.jsonl")

	header := `{"type":"session","version":1,"id":"abc","timestamp":"2024-01-01T00:00:00Z","cwd":"/proj","model":"m1","provider":"p1"}`
	goodMsg := `{"type":"message","id":"1","timestamp":"2024-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`
	// Bigger than the 8 MB scanner buffer set in runSessionShow.
	huge := `{"type":"message","id":"2","timestamp":"2024-01-01T00:00:02Z","message":{"role":"user","content":[{"type":"text","text":"` +
		strings.Repeat("x", 9*1024*1024) + `"}]}}`
	// Would count as a tool call if the scan somehow reached it — it must
	// NOT be counted, because a scanner that hit bufio.ErrTooLong cannot
	// resynchronize and never reaches lines after the oversized one.
	afterHuge := `{"type":"message","id":"3","timestamp":"2024-01-01T00:00:03Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"tc1","name":"read"}]}}`

	content := strings.Join([]string{header, goodMsg, huge, afterHuge}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runSessionShow(sessionShowCmd, []string{path}); err != nil {
			t.Fatalf("runSessionShow: %v", err)
		}
	})

	if !strings.Contains(strings.ToLower(out), "warning") {
		t.Errorf("expected a warning that the scan stopped early, got:\n%s", out)
	}
	// The header and the one good message before the oversized line still
	// came through.
	if !strings.Contains(out, "CWD:     /proj") {
		t.Errorf("expected header from the good first line to still be read, got:\n%s", out)
	}
	if !strings.Contains(out, "1 messages") {
		t.Errorf("expected only the 1 message before the oversized line to be counted, got:\n%s", out)
	}
	if strings.Contains(out, "1 tool calls") {
		t.Errorf("expected the toolCall after the oversized line to be unread, not counted, got:\n%s", out)
	}
}
