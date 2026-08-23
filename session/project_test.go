package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ─── test helpers: a real git repo, isolated $HOME ─────────────────────────

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// initGitRepo creates a repo with one commit on branch "main" inside its
// own directory (not under the isolated $HOME, so it survives regardless of
// which test sets HOME).
func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "f")
	runGit(t, dir, "commit", "-qm", "one")
	return dir
}

// isolatedHome points $HOME (and so UserHomeDir(), and so every
// ~/.tyci/sessions/... path this package computes) at a fresh temp
// directory for the duration of one test, so tests never read or write the
// developer's real session history.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserHomeDir on Windows reads USERPROFILE instead; harmless to set
	// both since this repo only needs to build on mac/Linux, but keeps the
	// helper honest about what it's actually overriding.
	t.Setenv("USERPROFILE", home)
	return home
}

// ─── ProjectKey / worktrees ─────────────────────────────────────────────────

// A linked worktree (git worktree add) must resolve to the same project key
// as its main repository — that's the whole point of keying sessions on the
// git toplevel instead of the exact cwd: repo/, repo/sub/, and every linked
// worktree of repo should share one session pool.
func TestProjectKey_WorktreeSharesPoolWithMainRepo(t *testing.T) {
	isolatedHome(t)
	main := initGitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, main, "worktree", "add", "-q", "-b", "tyci/child", wt)
	t.Cleanup(func() { runGit(t, main, "worktree", "remove", "--force", wt) })

	mainKey, err := ProjectKey(main)
	if err != nil {
		t.Fatalf("ProjectKey(main): %v", err)
	}
	wtKey, err := ProjectKey(wt)
	if err != nil {
		t.Fatalf("ProjectKey(worktree): %v", err)
	}
	if mainKey == "" || wtKey != mainKey {
		t.Fatalf("ProjectKey(worktree) = %q, ProjectKey(main) = %q, want equal and non-empty", wtKey, mainKey)
	}

	mainDir, err := SessionDir(main)
	if err != nil {
		t.Fatalf("SessionDir(main): %v", err)
	}
	wtDir, err := SessionDir(wt)
	if err != nil {
		t.Fatalf("SessionDir(worktree): %v", err)
	}
	if mainDir != wtDir {
		t.Fatalf("SessionDir(worktree) = %q, SessionDir(main) = %q, want the same directory", wtDir, mainDir)
	}
}

// A subdirectory of a repo (no worktree involved) must also share the
// project's pool, not get one of its own.
func TestProjectKey_SubdirectorySharesPoolWithRoot(t *testing.T) {
	isolatedHome(t)
	root := initGitRepo(t)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	rootDir, err := SessionDir(root)
	if err != nil {
		t.Fatal(err)
	}
	subDir, err := SessionDir(sub)
	if err != nil {
		t.Fatal(err)
	}
	if rootDir != subDir {
		t.Fatalf("SessionDir(sub) = %q, SessionDir(root) = %q, want the same directory", subDir, rootDir)
	}
}

// Outside a git repository, ProjectKey must fall back to the absolute cwd —
// the pre-existing per-directory behavior, unchanged.
func TestProjectKey_NonGitFallsBackToAbsCWD(t *testing.T) {
	dir := t.TempDir() // not a git repo
	key, err := ProjectKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	gotResolved, err := filepath.EvalSymlinks(key)
	if err != nil {
		t.Fatal(err)
	}
	if gotResolved != want {
		t.Fatalf("ProjectKey(non-git) = %q, want %q", key, want)
	}
}

// ─── Migration: old per-exact-cwd sessions must not be orphaned ────────────

// writeOldFormatSession writes a session file exactly as it looked before
// this change: a header with CWD set and no ProjectRoot field at all (not
// just empty — genuinely absent, the way json.Marshal produced it before
// the field existed).
func writeOldFormatSession(t *testing.T, path, cwd string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	header := map[string]any{
		"type":      "session",
		"version":   1,
		"id":        "old-sess",
		"timestamp": "2025-01-01T00:00:00Z",
		"cwd":       cwd,
		"model":     "test-model",
		"provider":  "test-provider",
	}
	hData, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	msg := `{"type":"message","id":"m1","timestamp":"2025-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"old prompt"}]}}`
	content := string(hData) + "\n" + msg + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A session recorded before this change, filed under the old
// encoded-exact-cwd directory for a subdirectory of a repo, must still turn
// up once sessions are listed for the repo's project key: migrateLegacyDirs
// recovers the project from the old session's Header.CWD and folds it into
// the project's directory.
func TestMigrateLegacyDirs_OldSubdirSessionDiscoveredUnderProject(t *testing.T) {
	home := isolatedHome(t)
	root := initGitRepo(t)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Old layout: ~/.tyci/sessions/<encoded exact sub-cwd>/old.jsonl
	oldEncoded := encodeKey(sub)
	oldPath := filepath.Join(home, ".tyci", "sessions", oldEncoded, "old.jsonl")
	writeOldFormatSession(t, oldPath, sub)

	// New lookup, from the repo root this time (a different exact cwd than
	// the one the old session recorded).
	entries, err := ResumeEntries(root)
	if err != nil {
		t.Fatalf("ResumeEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry after migration, got %d", len(entries))
	}
	if entries[0].FirstPrompt != "old prompt" {
		t.Fatalf("FirstPrompt = %q, want %q", entries[0].FirstPrompt, "old prompt")
	}

	// The old directory should be gone (fully merged) and the file now
	// lives under the project's directory.
	if _, err := os.Stat(filepath.Join(home, ".tyci", "sessions", oldEncoded)); !os.IsNotExist(err) {
		t.Errorf("expected old legacy dir to be removed, stat err = %v", err)
	}
	projectDir, err := SessionDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "old.jsonl")); err != nil {
		t.Errorf("expected old.jsonl under project dir %s: %v", projectDir, err)
	}
}

// A linked worktree's old per-exact-cwd session must also be found once
// listed from the main repo (or from the worktree itself) — the same
// migration path, just triggered by a worktree cwd instead of a
// subdirectory.
func TestMigrateLegacyDirs_OldWorktreeSessionDiscoveredUnderProject(t *testing.T) {
	home := isolatedHome(t)
	main := initGitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, main, "worktree", "add", "-q", "-b", "tyci/child", wt)
	t.Cleanup(func() { runGit(t, main, "worktree", "remove", "--force", wt) })

	oldEncoded := encodeKey(wt)
	oldPath := filepath.Join(home, ".tyci", "sessions", oldEncoded, "old.jsonl")
	writeOldFormatSession(t, oldPath, wt)

	entries, err := ResumeEntries(main)
	if err != nil {
		t.Fatalf("ResumeEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry after migration, got %d", len(entries))
	}
}

// A legacy session whose recorded cwd does NOT belong to the project being
// looked up must be left alone — migration must not sweep unrelated
// projects' history into whichever directory happens to be resolved first.
func TestMigrateLegacyDirs_UnrelatedSessionLeftInPlace(t *testing.T) {
	home := isolatedHome(t)
	projectA := initGitRepo(t)
	unrelatedCWD := filepath.Join(t.TempDir(), "unrelated") // not a git repo
	if err := os.MkdirAll(unrelatedCWD, 0o755); err != nil {
		t.Fatal(err)
	}

	oldEncoded := encodeKey(unrelatedCWD)
	oldPath := filepath.Join(home, ".tyci", "sessions", oldEncoded, "old.jsonl")
	writeOldFormatSession(t, oldPath, unrelatedCWD)

	entries, err := ResumeEntries(projectA)
	if err != nil {
		t.Fatalf("ResumeEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want 0 entries for projectA, got %d (unrelated session leaked in)", len(entries))
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("unrelated legacy session should be untouched: %v", err)
	}
}
