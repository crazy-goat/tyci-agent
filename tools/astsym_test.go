package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

const astFixture = `package sample

// Widget does things. mention of Helper here is a comment, not a reference.
type Widget struct {
	Name string
}

func Helper(x int) int {
	return x * 2
}

func (w *Widget) Run() int {
	s := "Helper is only a string here"
	_ = s
	return Helper(w.Name())
}
`

func writeAstFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	writeFile(t, path, astFixture)
	return path
}

func TestReadTool_Outline(t *testing.T) {
	path := writeAstFixture(t)
	res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path, "outline": true})
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	for _, want := range []string{"type Widget struct", "func Helper(x int) int", "func (w *Widget) Run() int"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("outline missing %q; got:\n%s", want, res.Content)
		}
	}
	// Struct fields are members and should surface in the outline too.
	if !strings.Contains(res.Content, "Name string") {
		t.Fatalf("outline should list struct fields; got:\n%s", res.Content)
	}
}

// phpFixture mirrors a PHPUnit test class where each method carries a #[Test]
// attribute on the line above the signature.
const phpFixture = `<?php

namespace App\Tests;

use PHPUnit\Framework\TestCase;

final class TupleCompareTest extends TestCase
{
    #[Test]
    public function compareEmptyTuplesReturnsZero(): void
    {
        self::assertSame(0, 1);
    }

    #[Test]
    public function compareIdenticalTuplesReturnsZero(): void
    {
        self::assertSame(0, 2);
    }
}
`

func TestReadTool_OutlinePHPAttributes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TupleCompareTest.php")
	writeFile(t, path, phpFixture)
	res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path, "outline": true})
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	// The outline must show real signatures AND the #[Test] attribute lines above them.
	for _, want := range []string{
		"final class TupleCompareTest",
		"public function compareEmptyTuplesReturnsZero(): void",
		"public function compareIdenticalTuplesReturnsZero(): void",
		"#[Test]",
	} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("outline missing %q; got:\n%s", want, res.Content)
		}
	}
	// Both #[Test] methods should surface their attribute line, not just one.
	if strings.Count(res.Content, "#[Test]") != 2 {
		t.Fatalf("expected both #[Test] attribute lines; got:\n%s", res.Content)
	}
}

func TestReadTool_OutlineConstants(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "consts.go")
	writeFile(t, path, "package sample\n\nconst MaxRetries = 3\n\nfunc Do() {}\n")
	res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path, "outline": true})
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	if !strings.Contains(res.Content, "MaxRetries") {
		t.Fatalf("outline should list constants; got:\n%s", res.Content)
	}
}

func TestReadTool_Symbol(t *testing.T) {
	path := writeAstFixture(t)
	res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path, "symbol": "Helper"})
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	if !strings.Contains(res.Content, "return x * 2") {
		t.Fatalf("symbol body missing; got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "func (w *Widget) Run") {
		t.Fatalf("symbol returned too much (leaked other definitions); got:\n%s", res.Content)
	}
}

func TestReadTool_SymbolNotFound(t *testing.T) {
	path := writeAstFixture(t)
	res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path, "symbol": "Nope"})
	if res.Success {
		t.Fatalf("expected failure for missing symbol")
	}
}

func TestReadTool_OutlineFallbackNonCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	writeFile(t, path, "just text\nno symbols")
	res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path, "outline": true})
	if !res.Success {
		t.Fatalf("expected graceful fallback, got: %s", res.Error)
	}
	// Falls back to a normal read of the file contents.
	if !strings.Contains(res.Content, "just text") {
		t.Fatalf("expected normal read fallback; got:\n%s", res.Content)
	}
}

// largeGoFile builds a syntactically valid Go file with n filler lines plus a
// couple of real definitions, so it exceeds autoOutlineThreshold.
func largeGoFile(t *testing.T, n int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("package big\n\nfunc Alpha() int {\n")
	for i := 0; i < n; i++ {
		b.WriteString("\t_ = 1 // filler\n")
	}
	b.WriteString("\treturn 0\n}\n\nfunc Beta() int { return 1 }\n")
	dir := t.TempDir()
	path := filepath.Join(dir, "big.go")
	writeFile(t, path, b.String())
	return path
}

func TestReadTool_AutoOutlineLargeFile(t *testing.T) {
	path := largeGoFile(t, 400) // well over 300 lines
	res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	if !strings.Contains(res.Content, "showing outline only") {
		t.Fatalf("expected auto-outline for large file; got head:\n%s", res.Content[:min(200, len(res.Content))])
	}
	if !strings.Contains(res.Content, "func Alpha() int") || !strings.Contains(res.Content, "func Beta() int") {
		t.Fatalf("outline missing symbols; got:\n%s", res.Content)
	}
}

func TestReadTool_AutoOutlineOptOut(t *testing.T) {
	path := largeGoFile(t, 400)
	// full=true must return real contents, not the outline.
	res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path, "full": true})
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	if strings.Contains(res.Content, "showing outline only") {
		t.Fatalf("full=true should override auto-outline")
	}
	if !strings.Contains(res.Content, "// filler") {
		t.Fatalf("full=true should return file body; got head:\n%s", res.Content[:min(200, len(res.Content))])
	}
}

func TestReadTool_SmallCodeFileAutoOutline(t *testing.T) {
	path := writeAstFixture(t) // small, but still a code file
	res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	// Outline is now the default for any code file with symbols, regardless of size.
	if !strings.Contains(res.Content, "showing outline only") {
		t.Fatalf("code file should auto-outline by default; got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "func Helper(x int) int") {
		t.Fatalf("expected outline to list symbols; got:\n%s", res.Content)
	}
}

func TestReadTool_LargeFileWithRangeNoAutoOutline(t *testing.T) {
	path := largeGoFile(t, 400)
	// Explicit offset/limit means the caller wants content, not the outline.
	res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path, "offset": 1, "limit": 3})
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	if strings.Contains(res.Content, "showing outline only") {
		t.Fatalf("explicit offset/limit should override auto-outline")
	}
}


