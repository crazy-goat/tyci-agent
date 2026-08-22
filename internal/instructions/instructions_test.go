package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Finding AGENTS.md
// ---------------------------------------------------------------------------

func TestSourcesFindsGlobalAndProject(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	write(t, filepath.Join(home, ".tyci", FileName), "global rules")
	write(t, filepath.Join(project, FileName), "project rules")

	got := Sources(home, project)
	if len(got) != 2 {
		t.Fatalf("expected both sources, got %v", got)
	}
	// Global first: the project file is read after, so it has the last word.
	if !strings.HasPrefix(got[0], home) {
		t.Errorf("global source should come first, got %v", got)
	}
}

// TestSourcesWalksUpToTheRepositoryRoot is the case the previous
// implementation got wrong: running tyci from a subdirectory found no
// AGENTS.md at all, because it only ever looked in the working directory.
func TestSourcesWalksUpToTheRepositoryRoot(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main")
	write(t, filepath.Join(root, FileName), "repo rules")
	sub := filepath.Join(root, "internal", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got := Sources("", sub)
	if len(got) != 1 {
		t.Fatalf("expected the repository's AGENTS.md, got %v", got)
	}
	if got[0] != filepath.Join(root, FileName) {
		t.Errorf("got %q", got[0])
	}
}

// TestSourcesStopsAtTheRepositoryRoot: a file above the repository belongs to
// something else, and picking it up silently would be a surprise.
func TestSourcesStopsAtTheRepositoryRoot(t *testing.T) {
	outer := t.TempDir()
	write(t, filepath.Join(outer, FileName), "someone else's rules")

	repo := filepath.Join(outer, "repo")
	write(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main")

	if got := Sources("", repo); len(got) != 0 {
		t.Fatalf("expected nothing above the repository root, got %v", got)
	}
}

func TestSourcesWithNothingToFind(t *testing.T) {
	if got := Sources("", t.TempDir()); len(got) != 0 {
		t.Fatalf("expected no sources, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

func TestLoadIsEmptyWithNothingConfigured(t *testing.T) {
	if got := Load(t.TempDir(), t.TempDir()); got != "" {
		t.Fatalf("expected nothing to add to the prompt, got %q", got)
	}
}

func TestLoadIncludesBothFilesAndNotes(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	write(t, filepath.Join(home, ".tyci", FileName), "always use tabs")
	write(t, filepath.Join(project, FileName), "run tests with make check")
	write(t, filepath.Join(MemoryDir(project), "layering.md"), "tools must not import jobs")

	got := Load(home, project)
	for _, want := range []string{
		"Project instructions",
		"always use tabs",
		"run tests with make check",
		"tools must not import jobs",
		"note: layering",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestLoadSkipsWhitespaceOnlyFiles: an empty AGENTS.md should not produce a
// header promising instructions that are not there.
func TestLoadSkipsWhitespaceOnlyFiles(t *testing.T) {
	project := t.TempDir()
	write(t, filepath.Join(project, FileName), "   \n\n\t")

	if got := Load("", project); got != "" {
		t.Fatalf("expected nothing, got %q", got)
	}
}

// TestLoadIsCapped: the block is re-sent on every single request, so an
// oversized AGENTS.md must not be pasted in unbounded.
func TestLoadIsCapped(t *testing.T) {
	project := t.TempDir()
	write(t, filepath.Join(project, FileName), strings.Repeat("x", maxTotalBytes*2))

	got := Load("", project)
	if !strings.Contains(got, "truncated") {
		t.Error("an oversized file should say it was truncated")
	}
	if len(got) > maxTotalBytes+500 {
		t.Errorf("block is %d bytes, cap is %d", len(got), maxTotalBytes)
	}
}

// ---------------------------------------------------------------------------
// Notes
// ---------------------------------------------------------------------------

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"test-command":     "test-command",
		"Test Command":     "test-command",
		"WHY_WE_DO_THIS":   "why-we-do-this", // one separator only, so a-b and a_b cannot become two notes about the same thing
		"../../etc/passwd": "etcpasswd",
		"a//b":             "ab",
		"..":               "",
		"":                 "",
		"///":              "",
	}
	for in, want := range cases {
		got, err := SanitizeName(in)
		if want == "" {
			if err == nil {
				t.Errorf("%q should be rejected, got %q", in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// TestWriteStaysInsideTheMemoryDir is the reason SanitizeName is strict: the
// model picks the name, so a traversal attempt must not become a file
// somewhere else.
func TestWriteStaysInsideTheMemoryDir(t *testing.T) {
	project := t.TempDir()
	name, err := Write(project, "../../escape", "nope")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.ContainsAny(name, "/\\.") {
		t.Fatalf("name %q still contains path characters", name)
	}
	if _, err := os.Stat(filepath.Join(MemoryDir(project), name+".md")); err != nil {
		t.Fatalf("note was not written where expected: %v", err)
	}
}

func TestWriteReadListDelete(t *testing.T) {
	project := t.TempDir()

	if names, err := List(project); err != nil || len(names) != 0 {
		t.Fatalf("a project with no notes should list nothing: %v %v", names, err)
	}

	if _, err := Write(project, "Test Command", "make check runs everything"); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(project, "layering", "tools must not import jobs"); err != nil {
		t.Fatal(err)
	}

	names, err := List(project)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted, so the prompt is stable between sessions.
	if len(names) != 2 || names[0] != "layering" || names[1] != "test-command" {
		t.Fatalf("got %v", names)
	}

	body, err := Read(project, "test-command")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "make check") {
		t.Fatalf("got %q", body)
	}

	// Writing the same name again is a correction, not a duplicate.
	if _, err := Write(project, "test-command", "actually it is make test"); err != nil {
		t.Fatal(err)
	}
	if names, _ := List(project); len(names) != 2 {
		t.Fatalf("rewriting a note should not add one: %v", names)
	}
	if body, _ := Read(project, "test-command"); !strings.Contains(body, "make test") {
		t.Fatalf("the correction did not stick: %q", body)
	}

	if _, err := Delete(project, "layering"); err != nil {
		t.Fatal(err)
	}
	if names, _ := List(project); len(names) != 1 {
		t.Fatalf("got %v", names)
	}
}

// TestDeleteMissingNoteIsAnError: succeeding silently would let the model
// believe it had corrected something it had not.
func TestDeleteMissingNoteIsAnError(t *testing.T) {
	if _, err := Delete(t.TempDir(), "never-existed"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestWriteRejectsEmptyContent(t *testing.T) {
	if _, err := Write(t.TempDir(), "x", "   \n "); err == nil {
		t.Fatal("expected an error pointing at delete")
	}
}

func TestWriteRejectsAnOversizedNote(t *testing.T) {
	_, err := Write(t.TempDir(), "big", strings.Repeat("x", maxMemoryFileBytes+1))
	if err == nil {
		t.Fatal("expected the note to be refused")
	}
	if !strings.Contains(err.Error(), "conclusion") {
		t.Errorf("the error should say what a note is for: %v", err)
	}
}

// TestWriteEnforcesTheFileCountCap, and the exception that makes it usable:
// correcting an existing note must never be blocked by the cap.
func TestWriteEnforcesTheFileCountCap(t *testing.T) {
	project := t.TempDir()
	for i := 0; i < maxMemoryFiles; i++ {
		if _, err := Write(project, "note"+string(rune('a'+i%26))+string(rune('a'+i/26)), "x"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Write(project, "one-more", "x"); err == nil {
		t.Fatal("expected the cap to be enforced")
	}
	if _, err := Write(project, "notea"+"a", "corrected"); err != nil {
		t.Fatalf("replacing an existing note must still work at the cap: %v", err)
	}
}
