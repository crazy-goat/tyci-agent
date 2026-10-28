package rolememory

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPath_RejectsTraversal(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".tyci", "memory")

	bad := []struct{ repo, role string }{
		{"", "worker"},
		{"tyci-agent", ""},
		{"..", "worker"},
		{"tyci-agent", ".."},
		{"../x", "worker"},
		{"tyci-agent", "../../escape"},
		{"a/b", "worker"},
		{"tyci-agent", "a/b"},
		{`a\b`, "worker"},
		{"tyci-agent", `a\b`},
		{".hidden", "worker"},
		{"tyci-agent", ".hidden"},
		{".", "worker"},
	}
	for _, tc := range bad {
		if _, err := Path(home, tc.repo, tc.role); !errors.Is(err, ErrInvalid) {
			t.Errorf("Path(%q, %q): want ErrInvalid, got %v", tc.repo, tc.role, err)
		}
	}

	if _, err := Path("", "tyci-agent", "worker"); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty home: want ErrInvalid, got %v", err)
	}

	p, err := Path(home, "tyci-agent", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "tyci-agent", "worker.md"); p != want {
		t.Errorf("got %q, want %q", p, want)
	}
}

func TestWriteReadRoundTripAndMode(t *testing.T) {
	home := t.TempDir()

	if err := Write(home, "tyci-agent", "worker", "run make check first"); err != nil {
		t.Fatal(err)
	}
	got, err := Read(home, "tyci-agent", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if got != "run make check first" {
		t.Fatalf("round trip: got %q", got)
	}

	p, _ := Path(home, "tyci-agent", "worker")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode %o, want 600", mode)
	}
	dirSt, err := os.Stat(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if mode := dirSt.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode %o, want 700", mode)
	}
}

// TestWriteReplacesAFileWithLooserMode: a file a person created with 0644 must
// come back as 0600 after the next write.
func TestWriteReplacesAFileWithLooserMode(t *testing.T) {
	home := t.TempDir()
	p, _ := Path(home, "tyci-agent", "worker")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Write(home, "tyci-agent", "worker", "new"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode %o, want 600", mode)
	}
	if got, _ := Read(home, "tyci-agent", "worker"); got != "new" {
		t.Errorf("got %q", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("a temporary file was left behind: %v", entries)
	}
}

func TestWriteRefusesOverCap(t *testing.T) {
	home := t.TempDir()
	err := Write(home, "tyci-agent", "worker", strings.Repeat("x", MaxInject+1))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if _, err := Read(home, "tyci-agent", "worker"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused note must not create a file: %v", err)
	}
	if err := Write(home, "tyci-agent", "worker", strings.Repeat("x", MaxInject)); err != nil {
		t.Errorf("a note of exactly MaxInject bytes must be accepted: %v", err)
	}
}

func TestWriteRefusesEmpty(t *testing.T) {
	if err := Write(t.TempDir(), "tyci-agent", "worker", " \n\t"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

// TestBlock_CapsAtMaxInject uses a hand-edited 40 KiB file: the head must be
// gone, the tail must stay, and the block must fit the cap.
func TestBlock_CapsAtMaxInject(t *testing.T) {
	home := t.TempDir()
	p, _ := Path(home, "tyci-agent", "worker")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "HEAD-MARKER\n" + strings.Repeat("line of notes\n", 40*1024/14) + "TAIL-MARKER\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got := Block(home, "tyci-agent", "worker", nil)
	if len(got) > MaxInject {
		t.Fatalf("block is %d bytes, cap is %d", len(got), MaxInject)
	}
	if strings.Contains(got, "HEAD-MARKER") {
		t.Error("the head of an oversized file must not be injected")
	}
	if !strings.HasSuffix(got, "TAIL-MARKER") {
		t.Error("the tail of the file must be injected")
	}
	if !strings.HasPrefix(got, "line of notes") {
		t.Errorf("the block must start on a line boundary, starts with %q", got[:20])
	}
}

// TestBlock_CutsOnRuneBoundary: a window without a line break must not start
// in the middle of a multi-byte rune.
func TestBlock_CutsOnRuneBoundary(t *testing.T) {
	home := t.TempDir()
	p, _ := Path(home, "tyci-agent", "worker")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	// 3-byte runes, no newline: the window of MaxInject bytes starts mid-rune
	// unless the cut moves forward.
	body := strings.Repeat("ż", MaxInject)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Block(home, "tyci-agent", "worker", nil)
	if !utf8.ValidString(got) {
		t.Fatal("block is not valid UTF-8")
	}
	if len(got) > MaxInject {
		t.Fatalf("block is %d bytes, cap is %d", len(got), MaxInject)
	}
}

func TestBlockMissingFileIsEmpty(t *testing.T) {
	if got := Block(t.TempDir(), "tyci-agent", "worker", nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestBlockInvalidNameIsEmpty(t *testing.T) {
	if got := Block(t.TempDir(), "tyci-agent", "../worker", nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

// TestTwoRolesSeeOnlyTheirOwnFile: the file of one role is never the file of
// another, and a role name cannot reach another role's file.
func TestTwoRolesSeeOnlyTheirOwnFile(t *testing.T) {
	home := t.TempDir()
	if err := Write(home, "tyci-agent", "worker", "worker note"); err != nil {
		t.Fatal(err)
	}
	if err := Write(home, "tyci-agent", "review", "review note"); err != nil {
		t.Fatal(err)
	}
	if got := Block(home, "tyci-agent", "worker", nil); got != "worker note" {
		t.Errorf("worker sees %q", got)
	}
	if got := Block(home, "tyci-agent", "review", nil); got != "review note" {
		t.Errorf("review sees %q", got)
	}
	if got := Block(home, "tyci-agent", "../review", nil); got != "" {
		t.Errorf("a traversal name must not reach another file: %q", got)
	}
	if got := Block(home, "other-repo", "worker", nil); got != "" {
		t.Errorf("another repository must not see the note: %q", got)
	}
}
