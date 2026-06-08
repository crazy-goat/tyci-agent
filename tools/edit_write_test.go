package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditTool_RequiresUniqueByDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, "foo\nbar\nfoo\n")

	res := (&EditTool{}).Run(context.Background(), map[string]any{
		"path": path, "oldString": "foo", "newString": "baz",
	})
	if res.Success {
		t.Fatalf("expected ambiguous edit to fail")
	}
	if !strings.Contains(res.Error, "matched 2 times") {
		t.Fatalf("expected match count, got %q", res.Error)
	}
}

func TestEditTool_OccurrenceDryRunAndAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, "foo\nbar\nfoo\n")
	tool := &EditTool{}

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
