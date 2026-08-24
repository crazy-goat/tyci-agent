package connect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The catalog holds the only copy of any hand-maintained provider, so the
// write must never be able to leave a truncated file behind.
func TestWriteCatalogAtomically_LeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	if err := writeCatalogAtomically(path, []byte(`{"a":{}}`)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != `{"a":{}}` {
		t.Fatalf("content = %q, err = %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

// Overwriting must replace the old content wholesale, not merge bytes.
func TestWriteCatalogAtomically_ReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	if err := os.WriteFile(path, []byte("a much longer previous catalog"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeCatalogAtomically(path, []byte("short")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "short" {
		t.Fatalf("content = %q, want the new bytes only", got)
	}
}

// A refresh that imports nothing must not rewrite the file at all: there is
// no benefit, and every write is a chance to lose the catalog.
func TestRefreshModels_ImportsNothingLeavesFileUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".tyci"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".tyci", "providers.json")
	original := `{
  "nexos": {
    "id": "nexos",
    "models": {}
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// A fetch whose every provider is dropped by the npm filter.
	restore := SetHTTPClientForTests(fakeModelsDevDoer{body: `{"weird":{"id":"weird","npm":"@nope/none","models":{}}}`})
	t.Cleanup(restore)

	imported, kept, _, err := RefreshModels("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 0 {
		t.Fatalf("imported = %d, want 0", len(imported))
	}
	if kept != 1 {
		t.Fatalf("keptUnchanged = %d, want 1", kept)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("file was rewritten:\n%s", got)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("file was rewritten (mtime changed) even though nothing was imported")
	}
}
