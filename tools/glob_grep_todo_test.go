package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobTool_DefaultExcludeAndSort(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "b.txt"), "")
	writeFile(t, filepath.Join(dir, "a.txt"), "")
	mustMkdir(t, filepath.Join(dir, "node_modules"))
	writeFile(t, filepath.Join(dir, "node_modules", "z.txt"), "")

	res := (&GlobTool{}).Run(context.Background(), map[string]any{"cwd": dir, "pattern": "**/*.txt"})
	if !res.Success {
		t.Fatalf("glob failed: %s", res.Error)
	}
	if strings.Contains(res.Content, "node_modules") {
		t.Fatalf("expected node_modules excluded, got: %s", res.Content)
	}
	if strings.Index(res.Content, "a.txt") > strings.Index(res.Content, "b.txt") {
		t.Fatalf("expected sorted output, got: %s", res.Content)
	}
}

func TestGrepTool_DefaultExcludeCountLimitAndMergedContext(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "one\nhit\nhit\nfive\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "hit\n")
	mustMkdir(t, filepath.Join(dir, "node_modules"))
	writeFile(t, filepath.Join(dir, "node_modules", "z.txt"), "hit\n")

	res := (&GrepTool{}).Run(context.Background(), map[string]any{"cwd": dir, "pattern": "hit", "output": "count", "limit": 1})
	if !res.Success {
		t.Fatalf("grep count failed: %s", res.Error)
	}
	if strings.Contains(res.Content, "node_modules") || strings.Contains(res.Content, "b.txt") {
		t.Fatalf("expected exclude and count limit, got: %s", res.Content)
	}

	res = (&GrepTool{}).Run(context.Background(), map[string]any{"cwd": dir, "pattern": "hit", "include": "a.txt", "context": 1})
	if !res.Success {
		t.Fatalf("grep context failed: %s", res.Error)
	}
	if strings.Count(res.Content, "a.txt:") != 1 {
		t.Fatalf("expected merged context block, got: %s", res.Content)
	}
}

func TestTodoTool_StatusAliases(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	res := tool.Run(context.Background(), map[string]any{"action": "add", "content": "x"})
	if !res.Success {
		t.Fatalf("add failed: %s", res.Error)
	}
	res = tool.Run(context.Background(), map[string]any{"action": "doing", "id": 1})
	if !res.Success || !strings.Contains(res.Content, "[doing]") {
		t.Fatalf("doing failed: %v %s %s", res.Success, res.Content, res.Error)
	}
}

func mustMkdir(t testing.TB, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
}
