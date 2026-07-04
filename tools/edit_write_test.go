package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Write mode tests (content + optional range)
// ---------------------------------------------------------------------------

func TestWriteTool_WriteAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	tool := &WriteTool{}

	res := tool.Run(context.Background(), map[string]any{
		"path": path, "content": "hello",
	})
	if !res.Success {
		t.Fatalf("write all failed: %s", res.Error)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
}

func TestWriteTool_Append(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, "hello")
	tool := &WriteTool{}

	res := tool.Run(context.Background(), map[string]any{
		"path": path, "content": " world", "range": "append",
	})
	if !res.Success {
		t.Fatalf("append failed: %s", res.Error)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", string(data))
	}
}

func TestWriteTool_InsertBeforeAfter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, "a\nc\n")
	tool := &WriteTool{}

	res := tool.Run(context.Background(), map[string]any{"path": path, "content": "b", "range": "before:2"})
	if !res.Success {
		t.Fatalf("insert before failed: %s", res.Error)
	}
	res = tool.Run(context.Background(), map[string]any{"path": path, "content": "d", "range": "after:3"})
	if !res.Success {
		t.Fatalf("insert after failed: %s", res.Error)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "a\nb\nc\nd\n" {
		t.Fatalf("unexpected file: %q", string(data))
	}
}

func TestWriteTool_ReplaceLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, "a\nb\nc\nd\n")
	tool := &WriteTool{}

	res := tool.Run(context.Background(), map[string]any{
		"path": path, "content": "x\ny", "range": "2...3",
	})
	if !res.Success {
		t.Fatalf("replace lines failed: %s", res.Error)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "a\nx\ny\nd\n" {
		t.Fatalf("unexpected file: %q", string(data))
	}
}

// ---------------------------------------------------------------------------
// Edit mode tests (oldString + newString)
// ---------------------------------------------------------------------------

func TestWriteTool_EditRequiresUniqueByDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, "foo\nbar\nfoo\n")

	res := (&WriteTool{}).Run(context.Background(), map[string]any{
		"path": path, "oldString": "foo", "newString": "baz",
	})
	if res.Success {
		t.Fatalf("expected ambiguous edit to fail")
	}
	if !strings.Contains(res.Error, "matched 2 times") {
		t.Fatalf("expected match count, got %q", res.Error)
	}
}

func TestWriteTool_EditOccurrenceDryRunAndAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, "foo\nbar\nfoo\n")
	tool := &WriteTool{}

	res := tool.Run(context.Background(), map[string]any{
		"path": path, "oldString": "foo", "newString": "baz", "occurrence": 2, "dryRun": true,
	})
	if !res.Success || !strings.Contains(res.Content, "would replace 1 occurrence") || !strings.Contains(res.Content, "line 3") {
		t.Fatalf("bad dry run result: success=%v content=%q error=%q", res.Success, res.Content, res.Error)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "foo\nbar\nfoo\n" {
		t.Fatalf("dry run changed file")
	}

	res = tool.Run(context.Background(), map[string]any{
		"path": path, "oldString": "foo", "newString": "baz", "occurrence": "all",
	})
	if !res.Success || !strings.Contains(res.Content, "replaced 2 occurrence") {
		t.Fatalf("bad replace all result: success=%v content=%q error=%q", res.Success, res.Content, res.Error)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "baz\nbar\nbaz\n" {
		t.Fatalf("unexpected file: %q", string(data))
	}
}

func TestWriteTool_EditNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, "hello world")

	res := (&WriteTool{}).Run(context.Background(), map[string]any{
		"path": path, "oldString": "zzz", "newString": "aaa",
	})
	if res.Success {
		t.Fatalf("expected failure for missing oldString")
	}
	if !strings.Contains(res.Error, "not found") {
		t.Fatalf("expected 'not found', got %q", res.Error)
	}
}

func TestWriteTool_EditSingleOccurrence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, "foo\nbar\nbaz\n")

	res := (&WriteTool{}).Run(context.Background(), map[string]any{
		"path": path, "oldString": "bar", "newString": "qux",
	})
	if !res.Success {
		t.Fatalf("edit failed: %s", res.Error)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "foo\nqux\nbaz\n" {
		t.Fatalf("unexpected file: %q", string(data))
	}
}

// ---------------------------------------------------------------------------
// Mixed: edit + write both work through one tool
// ---------------------------------------------------------------------------

func TestWriteTool_EditThenWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, "hello\nworld\n")

	tool := &WriteTool{}
	res := tool.Run(context.Background(), map[string]any{
		"path": path, "oldString": "hello", "newString": "hi",
	})
	if !res.Success {
		t.Fatalf("edit failed: %s", res.Error)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hi\nworld\n" {
		t.Fatalf("after edit: %q", string(data))
	}

	res = tool.Run(context.Background(), map[string]any{
		"path": path, "content": "overwritten",
	})
	if !res.Success {
		t.Fatalf("write failed: %s", res.Error)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "overwritten" {
		t.Fatalf("after write: %q", string(data))
	}
}

func TestWriteTool_EditMultiple(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, "x x x")

	res := (&WriteTool{}).Run(context.Background(), map[string]any{
		"path": path, "oldString": "x", "newString": "y", "occurrence": "all",
	})
	if !res.Success {
		t.Fatalf("edit all failed: %s", res.Error)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "y y y" {
		t.Fatalf("unexpected file: %q", string(data))
	}
}
