package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesFileWithMode0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "note.md")
	if err := Write(p, []byte("content")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode %o, want 600", mode)
	}
}

// TestWriteReplacesLooserModeAndLeavesNoTempFile: an existing 0644 file comes
// back as 0600 with the new content, and no temporary file stays behind.
func TestWriteReplacesLooserModeAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "note.md")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Write(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode %o, want 600", mode)
	}
	if got, _ := os.ReadFile(p); string(got) != "new" {
		t.Errorf("got %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("a temporary file was left behind: %v", entries)
	}
}
