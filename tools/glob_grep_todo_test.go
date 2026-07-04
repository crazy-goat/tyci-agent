package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindTool_Glob_DefaultExcludeAndSort(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "b.txt"), "")
	writeFile(t, filepath.Join(dir, "a.txt"), "")
	mustMkdir(t, filepath.Join(dir, "node_modules"))
	writeFile(t, filepath.Join(dir, "node_modules", "z.txt"), "")

	res := (&FindTool{}).Run(context.Background(), map[string]any{"method": "glob", "cwd": dir, "pattern": "**/*.txt"})
	if !res.Success {
		t.Fatalf("find glob failed: %s", res.Error)
	}
	if strings.Contains(res.Content, "node_modules") {
		t.Fatalf("expected node_modules excluded, got: %s", res.Content)
	}
	if strings.Index(res.Content, "a.txt") > strings.Index(res.Content, "b.txt") {
		t.Fatalf("expected sorted output, got: %s", res.Content)
	}
}

func TestFindTool_Grep_DefaultExcludeCountLimitAndMergedContext(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "one\nhit\nhit\nfive\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "hit\n")
	mustMkdir(t, filepath.Join(dir, "node_modules"))
	writeFile(t, filepath.Join(dir, "node_modules", "z.txt"), "hit\n")

	res := (&FindTool{}).Run(context.Background(), map[string]any{"method": "grep", "cwd": dir, "pattern": "hit", "output": "count", "limit": 1})
	if !res.Success {
		t.Fatalf("find grep count failed: %s", res.Error)
	}
	if strings.Contains(res.Content, "node_modules") || strings.Contains(res.Content, "b.txt") {
		t.Fatalf("expected exclude and count limit, got: %s", res.Content)
	}

	res = (&FindTool{}).Run(context.Background(), map[string]any{"method": "grep", "cwd": dir, "pattern": "hit", "include": "a.txt", "context": 1})
	if !res.Success {
		t.Fatalf("find grep context failed: %s", res.Error)
	}
	if strings.Count(res.Content, "a.txt:") != 1 {
		t.Fatalf("expected merged context block, got: %s", res.Content)
	}
}

func TestFindTool_Glob_GitignoreFiltering(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".gitignore"), "secret.txt\nbuild/\n")
	writeFile(t, filepath.Join(dir, ".aiignore"), "*.ai.txt\n")
	writeFile(t, filepath.Join(dir, "keep.txt"), "")
	writeFile(t, filepath.Join(dir, "secret.txt"), "")
	writeFile(t, filepath.Join(dir, "notes.ai.txt"), "")
	mustMkdir(t, filepath.Join(dir, "build"))
	writeFile(t, filepath.Join(dir, "build", "out.txt"), "")

	res := (&FindTool{}).Run(context.Background(), map[string]any{"method": "glob", "cwd": dir, "pattern": "**/*.txt"})
	if !res.Success {
		t.Fatalf("find glob failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "keep.txt") {
		t.Fatalf("expected keep.txt present, got: %s", res.Content)
	}
	for _, hidden := range []string{"secret.txt", "notes.ai.txt", "build/out.txt"} {
		if strings.Contains(res.Content, hidden) {
			t.Fatalf("expected %s ignored, got: %s", hidden, res.Content)
		}
	}
	if !strings.Contains(res.Content, "hidden by .gitignore/.aiignore") {
		t.Fatalf("expected filtered-count note, got: %s", res.Content)
	}

	// Opt out: ignored files reappear and the note is gone.
	res = (&FindTool{}).Run(context.Background(), map[string]any{"method": "glob", "cwd": dir, "pattern": "**/*.txt", "respectGitignore": false})
	if !res.Success {
		t.Fatalf("find glob (opt-out) failed: %s", res.Error)
	}
	// build/ stays hidden — it is a builtin exclude, not just gitignore.
	if !strings.Contains(res.Content, "secret.txt") || !strings.Contains(res.Content, "notes.ai.txt") {
		t.Fatalf("expected ignored files with opt-out, got: %s", res.Content)
	}
	if strings.Contains(res.Content, "hidden by") {
		t.Fatalf("did not expect note with opt-out, got: %s", res.Content)
	}
}

func TestFindTool_Grep_GitignoreFiltering(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	writeFile(t, filepath.Join(dir, "kept.txt"), "hit\n")
	writeFile(t, filepath.Join(dir, "ignored.txt"), "hit\n")

	res := (&FindTool{}).Run(context.Background(), map[string]any{"method": "grep", "cwd": dir, "pattern": "hit"})
	if !res.Success {
		t.Fatalf("find grep failed: %s", res.Error)
	}
	if strings.Contains(res.Content, "ignored.txt") {
		t.Fatalf("expected ignored.txt skipped, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "kept.txt") || !strings.Contains(res.Content, "hidden by") {
		t.Fatalf("expected kept.txt and note, got: %s", res.Content)
	}

	res = (&FindTool{}).Run(context.Background(), map[string]any{"method": "grep", "cwd": dir, "pattern": "hit", "respectGitignore": false})
	if !res.Success {
		t.Fatalf("find grep (opt-out) failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "ignored.txt") {
		t.Fatalf("expected ignored.txt searched with opt-out, got: %s", res.Content)
	}
}

func TestFindTool_Glob_GitignoreNegationAndNested(t *testing.T) {
	dir := t.TempDir()
	// Root ignores all logs but re-includes keep.log.
	writeFile(t, filepath.Join(dir, ".gitignore"), "*.log\n!keep.log\n")
	writeFile(t, filepath.Join(dir, "drop.log"), "")
	writeFile(t, filepath.Join(dir, "keep.log"), "")
	// Nested ignore applies only to its subtree.
	mustMkdir(t, filepath.Join(dir, "sub"))
	writeFile(t, filepath.Join(dir, "sub", ".gitignore"), "local.txt\n")
	writeFile(t, filepath.Join(dir, "sub", "local.txt"), "")
	writeFile(t, filepath.Join(dir, "sub", "shared.txt"), "")
	writeFile(t, filepath.Join(dir, "top.txt"), "")

	res := (&FindTool{}).Run(context.Background(), map[string]any{"method": "glob", "cwd": dir, "pattern": "**/*"})
	if !res.Success {
		t.Fatalf("find glob failed: %s", res.Error)
	}
	for _, want := range []string{"keep.log", "top.txt", "sub/shared.txt"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("expected %s present, got: %s", want, res.Content)
		}
	}
	for _, notWant := range []string{"drop.log", "sub/local.txt"} {
		if strings.Contains(res.Content, notWant) {
			t.Fatalf("expected %s ignored, got: %s", notWant, res.Content)
		}
	}
	// Nested ignore must not leak to the root: a root local.txt survives.
	writeFile(t, filepath.Join(dir, "local.txt"), "")
	res = (&FindTool{}).Run(context.Background(), map[string]any{"method": "glob", "cwd": dir, "pattern": "local.txt"})
	if !res.Success || !strings.Contains(res.Content, "local.txt") {
		t.Fatalf("expected root local.txt present, got: %s", res.Content)
	}
}

func TestFindTool_Glob_CharacterClassesAndBraces(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "")
	writeFile(t, filepath.Join(dir, "b.go"), "")
	writeFile(t, filepath.Join(dir, "1.go"), "")
	writeFile(t, filepath.Join(dir, "main.rs"), "")
	writeFile(t, filepath.Join(dir, "notes.txt"), "")
	mustMkdir(t, filepath.Join(dir, "src"))
	writeFile(t, filepath.Join(dir, "src", "foo.go"), "")
	writeFile(t, filepath.Join(dir, "src", "bar.go"), "")
	writeFile(t, filepath.Join(dir, "src", "0_test.go"), "")

	// Character class [a-z]
	res := (&FindTool{}).Run(context.Background(), map[string]any{"cwd": dir, "pattern": "[a-z].go", "limit": 100})
	if !res.Success {
		t.Fatalf("find [a-z].go failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "a.go") || !strings.Contains(res.Content, "b.go") {
		t.Fatalf("expected a.go and b.go, got: %s", res.Content)
	}
	if strings.Contains(res.Content, "1.go") || strings.Contains(res.Content, "main.rs") {
		t.Fatalf("expected only .go files matching [a-z], got: %s", res.Content)
	}

	// Negation [!a-z]
	res = (&FindTool{}).Run(context.Background(), map[string]any{"cwd": dir, "pattern": "[!a-z].go", "limit": 100})
	if !res.Success {
		t.Fatalf("find [!a-z].go failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "1.go") {
		t.Fatalf("expected 1.go (non-alpha start), got: %s", res.Content)
	}
	if strings.Contains(res.Content, "a.go") || strings.Contains(res.Content, "b.go") {
		t.Fatalf("expected no alpha-start .go files, got: %s", res.Content)
	}

	// Brace expansion {go,rs}
	res = (&FindTool{}).Run(context.Background(), map[string]any{"cwd": dir, "pattern": "*.{go,rs}", "limit": 100})
	if !res.Success {
		t.Fatalf("find *.{go,rs} failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "a.go") || !strings.Contains(res.Content, "main.rs") {
		t.Fatalf("expected a.go and main.rs, got: %s", res.Content)
	}
	if strings.Contains(res.Content, "notes.txt") {
		t.Fatalf("expected no .txt files, got: %s", res.Content)
	}

	// Subdirectory with character class
	res = (&FindTool{}).Run(context.Background(), map[string]any{"cwd": dir, "pattern": "src/[a-z]*.go", "limit": 100})
	if !res.Success {
		t.Fatalf("find src/[a-z]*.go failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "src/foo.go") || !strings.Contains(res.Content, "src/bar.go") {
		t.Fatalf("expected src/foo.go and src/bar.go, got: %s", res.Content)
	}
	if strings.Contains(res.Content, "src/0_test.go") {
		t.Fatalf("expected no src/0_test.go, got: %s", res.Content)
	}
}

func TestFindTool_DefaultMethodIsGlob(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "test.txt"), "content")
	res := (&FindTool{}).Run(context.Background(), map[string]any{"cwd": dir, "pattern": "*.txt"})
	if !res.Success {
		t.Fatalf("find default method failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "test.txt") {
		t.Fatalf("expected test.txt, got: %s", res.Content)
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
