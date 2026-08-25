package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestReadTool_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "hello world")

	r := &ReadTool{}
	res := r.Run(context.Background(), map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if res.Content != "hello world" {
		t.Fatalf("expected 'hello world', got %q", res.Content)
	}
}

func TestReadTool_WithOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "line1\nline2\nline3\nline4\nline5")

	r := &ReadTool{}
	// offset=2 means start from line 2 (1-indexed), so expect "line2\nline3\nline4\nline5"
	res := r.Run(context.Background(), map[string]any{"path": path, "offset": 2})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	expected := "line2\nline3\nline4\nline5"
	if res.Content != expected {
		t.Fatalf("expected %q, got %q", expected, res.Content)
	}
}

func TestReadTool_WithLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "line1\nline2\nline3\nline4\nline5")

	r := &ReadTool{}
	// limit=3 means max 3 lines, but file has more → continuation hint
	res := r.Run(context.Background(), map[string]any{"path": path, "limit": 3})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	expected := "line1\nline2\nline3\n\n[Showing lines 1-3 of 5. 2 more lines available. Use offset=4 to continue.]"
	if res.Content != expected {
		t.Fatalf("expected %q, got %q", expected, res.Content)
	}
}

func TestReadTool_WithOffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "line1\nline2\nline3\nline4\nline5")

	r := &ReadTool{}
	// offset=2 (line 2), limit=3 → lines 2-4, 1 more line
	res := r.Run(context.Background(), map[string]any{"path": path, "offset": 2, "limit": 3})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	expected := "line2\nline3\nline4\n\n[Showing lines 2-4 of 5. 1 more lines available. Use offset=5 to continue.]"
	if res.Content != expected {
		t.Fatalf("expected %q, got %q", expected, res.Content)
	}
}

func TestReadTool_OffsetBeyondFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "hi")

	r := &ReadTool{}
	res := r.Run(context.Background(), map[string]any{"path": path, "offset": 10})
	if res.Success {
		t.Fatal("expected failure for offset beyond file")
	}
	if !strings.Contains(res.Error, "beyond end of file") {
		t.Errorf("expected error about beyond end of file, got: %s", res.Error)
	}
}

func TestReadTool_OffsetString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "alpha\nbravo\ncharlie\ndelta\necho")

	r := &ReadTool{}
	// offset "3" → line 3 (charlie), limit "2" → 2 lines (charlie, delta), 1 more line
	res := r.Run(context.Background(), map[string]any{"path": path, "offset": "3", "limit": "2"})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	expected := "charlie\ndelta\n\n[Showing lines 3-4 of 5. 1 more lines available. Use offset=5 to continue.]"
	if res.Content != expected {
		t.Fatalf("expected %q, got %q", expected, res.Content)
	}
}

func TestReadTool_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	writeFile(t, path, "")

	r := &ReadTool{}
	res := r.Run(context.Background(), map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if res.Content != "" {
		t.Fatalf("expected empty string, got %q", res.Content)
	}
}

func TestReadTool_Directory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "")
	writeFile(t, filepath.Join(dir, "b.txt"), "")
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)

	r := &ReadTool{}
	res := r.Run(context.Background(), map[string]any{"path": dir})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}

	if !strings.Contains(res.Content, "a.txt") {
		t.Errorf("expected listing to contain a.txt, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "b.txt") {
		t.Errorf("expected listing to contain b.txt, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "sub/") {
		t.Errorf("expected listing to contain sub/, got: %s", res.Content)
	}
}

func TestReadTool_SymlinkToFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	writeFile(t, target, "symlink content")
	if err := os.Symlink("target.txt", link); err != nil {
		t.Fatal(err)
	}

	r := &ReadTool{}
	res := r.Run(context.Background(), map[string]any{"path": link})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if res.Content != "symlink content" {
		t.Fatalf("expected 'symlink content', got %q", res.Content)
	}
}

func TestReadTool_SymlinkToDirectory(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "realdir")
	link := filepath.Join(dir, "linkdir")
	os.MkdirAll(targetDir, 0755)
	writeFile(t, filepath.Join(targetDir, "inside.txt"), "")
	if err := os.Symlink("realdir", link); err != nil {
		t.Fatal(err)
	}

	r := &ReadTool{}
	res := r.Run(context.Background(), map[string]any{"path": link})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "inside.txt") {
		t.Errorf("expected listing to contain inside.txt, got: %s", res.Content)
	}
}

func TestReadTool_FileNotFound(t *testing.T) {
	r := &ReadTool{}
	res := r.Run(context.Background(), map[string]any{"path": "/nonexistent/path"})
	if res.Success {
		t.Fatal("expected failure for nonexistent path")
	}
	if res.Error == "" {
		t.Fatal("expected error message, got empty")
	}
}

func TestReadTool_PathRequired(t *testing.T) {
	r := &ReadTool{}
	res := r.Run(context.Background(), map[string]any{})
	if res.Success {
		t.Fatal("expected failure for missing path")
	}
	if !strings.Contains(res.Error, "path required") {
		t.Errorf("expected 'path required', got: %s", res.Error)
	}
	if !res.validationError {
		t.Fatal("missing path should be marked as a validation error")
	}
}

func TestReadTool_TruncationByLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")

	// Write file with more lines than DefaultMaxLines
	var lines []string
	for i := 0; i < DefaultMaxLines+100; i++ {
		lines = append(lines, "line "+strconv.Itoa(i+1))
	}
	content := strings.Join(lines, "\n")
	writeFile(t, path, content)

	r := &ReadTool{}
	res := r.Run(context.Background(), map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}

	// Should have continuation hint
	if !strings.Contains(res.Content, "to continue") {
		t.Errorf("expected continuation hint in truncated output, got: ...%s", res.Content[len(res.Content)-60:])
	}
	// Should contain the first DefaultMaxLines lines
	if !strings.Contains(res.Content, "line 1") {
		t.Errorf("expected first line in output")
	}
}

func TestReadTool_TruncationByBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wide.txt")

	// Single line that exceeds byte limit
	writeFile(t, path, strings.Repeat("x", DefaultMaxBytes+1000))

	r := &ReadTool{}
	res := r.Run(context.Background(), map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}

	// Should tell user to use bash
	if !strings.Contains(res.Content, "Use bash") {
		t.Errorf("expected bash hint for oversized single line, got: %s", res.Content)
	}
}

func TestReadTool_TruncationContinuationHint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "medium.txt")

	// Write 100 lines
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i+1))
	}
	content := strings.Join(lines, "\n")
	writeFile(t, path, content)

	r := &ReadTool{}
	// Read first 3 lines
	res := r.Run(context.Background(), map[string]any{"path": path, "offset": 1, "limit": 3})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}

	// Should say "97 more lines"
	if !strings.Contains(res.Content, "97 more lines") {
		t.Errorf("expected '97 more lines' hint, got: %s", res.Content)
	}
	// Should mention next offset
	if !strings.Contains(res.Content, "offset=4") {
		t.Errorf("expected 'offset=4' hint, got: %s", res.Content)
	}
}

func TestReadTool_LineNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "numbered.txt")
	writeFile(t, path, "a\nb\nc")

	r := &ReadTool{}
	res := r.Run(context.Background(), map[string]any{"path": path, "offset": 2, "limit": 2, "lineNumbers": true})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	expected := "2| b\n3| c"
	if res.Content != expected {
		t.Fatalf("expected %q, got %q", expected, res.Content)
	}
}

func TestReadTool_ByteTruncationShowsContinuation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bytes.txt")
	line := strings.Repeat("x", 100)
	writeFile(t, path, strings.Repeat(line+"\n", 700))

	r := &ReadTool{}
	res := r.Run(context.Background(), map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "Output hit 50KB limit") {
		t.Fatalf("expected byte truncation hint, got suffix: %q", res.Content[len(res.Content)-120:])
	}
	if !strings.Contains(res.Content, "Use offset=") {
		t.Fatalf("expected continuation offset, got: %s", res.Content)
	}
}

func TestReadTool_OffsetIsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "full content")

	r := &ReadTool{}
	// Explicit offset=0 should return full content
	res := r.Run(context.Background(), map[string]any{"path": path, "offset": 0})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if res.Content != "full content" {
		t.Fatalf("expected 'full content', got %q", res.Content)
	}
}

func TestReadTool_IntParamEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		key      string
		def      int
		expected int
	}{
		{"float64", map[string]any{"x": float64(42)}, "x", 0, 42},
		{"int", map[string]any{"x": 42}, "x", 0, 42},
		{"string", map[string]any{"x": "42"}, "x", 0, 42},
		{"invalid string", map[string]any{"x": "abc"}, "x", 10, 10},
		{"missing", map[string]any{}, "x", 10, 10},
		{"bool ignored", map[string]any{"x": true}, "x", 5, 5},
		{"nil ignored", map[string]any{"x": nil}, "x", 5, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := intParam(tt.input, tt.key, tt.def)
			if got != tt.expected {
				t.Errorf("intParam(%v, %q, %d) = %d, want %d", tt.input, tt.key, tt.def, got, tt.expected)
			}
		})
	}
}

// helpers

// writeFile lays down a fixture file and marks it as already seen by the
// agent (see tools/filestamp.go). A fixture is content the test itself
// authored, so the write tool's freshness guard has nothing to protect here;
// without the stamp every edit-mode test would fail on "you have not read
// it". The guard's own behaviour is covered in filestamp_test.go, which
// deliberately does not go through this helper.
func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	recordFileStamp(path)
}
