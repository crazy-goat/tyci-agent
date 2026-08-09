package agentdefs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// builtinNames is the fixed set of definitions this test suite expects to
// find embedded. Keeping it in one place means a change to
// internal/agentdefs/builtin/*.md shows up as a single, obvious diff here
// instead of a dozen unrelated-looking assertions.
var builtinNames = []string{"implementer", "locator", "reviewer"}

func mustSync(t *testing.T, dir string, force bool) SyncResult {
	t.Helper()
	res, err := Sync(dir, force)
	if err != nil {
		t.Fatalf("Sync(%q, %v) returned error: %v", dir, force, err)
	}
	return res
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(data)
}

func containsAll(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	set := make(map[string]bool, len(got))
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// 1. Empty directory: everything is installed.
func TestSync_EmptyDir_InstallsEverything(t *testing.T) {
	dir := t.TempDir()

	res := mustSync(t, dir, false)

	if !containsAll(res.Installed, builtinNames) {
		t.Errorf("Installed = %v, want %v", res.Installed, builtinNames)
	}
	if len(res.Updated) != 0 || len(res.SkippedModified) != 0 || len(res.SkippedDeleted) != 0 || len(res.Unchanged) != 0 {
		t.Errorf("unexpected non-Installed results: %+v", res)
	}

	builtin, err := Builtin()
	if err != nil {
		t.Fatalf("Builtin(): %v", err)
	}
	for _, def := range builtin {
		got := readFile(t, filepath.Join(dir, def.Name+".md"))
		want := readFile(t, filepath.Join("builtin", def.Name+".md"))
		if got != want {
			t.Errorf("%s: on-disk content does not match embedded content", def.Name)
		}
	}

	statePath := filepath.Join(dir, managedStateFile)
	if _, err := os.Stat(statePath); err != nil {
		t.Errorf("expected state file at %s: %v", statePath, err)
	}
}

// 2. Second sync with no changes: everything Unchanged, files untouched.
func TestSync_SecondSync_Unchanged(t *testing.T) {
	dir := t.TempDir()
	mustSync(t, dir, false)

	// Capture mtimes after the first sync so we can prove the second sync
	// does not rewrite anything.
	mtimes := map[string]time.Time{}
	for _, name := range builtinNames {
		path := filepath.Join(dir, name+".md")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q): %v", path, err)
		}
		mtimes[name] = info.ModTime()
	}

	res := mustSync(t, dir, false)

	if !containsAll(res.Unchanged, builtinNames) {
		t.Errorf("Unchanged = %v, want %v", res.Unchanged, builtinNames)
	}
	if len(res.Installed) != 0 || len(res.Updated) != 0 || len(res.SkippedModified) != 0 || len(res.SkippedDeleted) != 0 {
		t.Errorf("unexpected non-Unchanged results: %+v", res)
	}

	for _, name := range builtinNames {
		path := filepath.Join(dir, name+".md")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q): %v", path, err)
		}
		if !info.ModTime().Equal(mtimes[name]) {
			t.Errorf("%s: mtime changed on a no-op sync (was %v, now %v)", name, mtimes[name], info.ModTime())
		}
	}
}

// 3. User-modified file: SkippedModified, content on disk left untouched.
func TestSync_UserModified_Skipped(t *testing.T) {
	dir := t.TempDir()
	mustSync(t, dir, false)

	target := filepath.Join(dir, "locator.md")
	customContent := "---\ndescription: my own locator\n---\n\nCustom body.\n"
	if err := os.WriteFile(target, []byte(customContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	res := mustSync(t, dir, false)

	found := false
	for _, n := range res.SkippedModified {
		if n == "locator" {
			found = true
		}
	}
	if !found {
		t.Errorf("SkippedModified = %v, want to contain %q", res.SkippedModified, "locator")
	}

	if got := readFile(t, target); got != customContent {
		t.Errorf("user-modified file was overwritten: got %q, want %q", got, customContent)
	}
}

// 4. User-deleted file (state entry remains): SkippedDeleted, not resurrected.
func TestSync_UserDeleted_NotResurrected(t *testing.T) {
	dir := t.TempDir()
	mustSync(t, dir, false)

	target := filepath.Join(dir, "reviewer.md")
	if err := os.Remove(target); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	res := mustSync(t, dir, false)

	found := false
	for _, n := range res.SkippedDeleted {
		if n == "reviewer" {
			found = true
		}
	}
	if !found {
		t.Errorf("SkippedDeleted = %v, want to contain %q", res.SkippedDeleted, "reviewer")
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("expected %s to remain deleted, stat err = %v", target, err)
	}
}

// 5. Simulated new tyci version: old content + old state hash on disk =>
// Sync updates to the current embedded content.
func TestSync_NewVersion_UpdatesOurFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	oldContent := "---\ndescription: old locator\ntemperature: 0\n---\n\nOld body.\n"
	path := filepath.Join(dir, "locator.md")
	if err := os.WriteFile(path, []byte(oldContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := saveManagedState(filepath.Join(dir, managedStateFile), map[string]string{
		"locator.md": sha256Hex([]byte(oldContent)),
	}); err != nil {
		t.Fatalf("saveManagedState: %v", err)
	}

	res := mustSync(t, dir, false)

	found := false
	for _, n := range res.Updated {
		if n == "locator" {
			found = true
		}
	}
	if !found {
		t.Errorf("Updated = %v, want to contain %q", res.Updated, "locator")
	}

	want := readFile(t, filepath.Join("builtin", "locator.md"))
	if got := readFile(t, path); got != want {
		t.Errorf("locator.md not updated to embedded content: got %q, want %q", got, want)
	}
}

// 6. Same "new version" scenario, but the user also edited the file after
// tyci installed it: their edit must survive, not the update.
func TestSync_NewVersion_ButUserModified_SkipsUpdate(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	oldContent := "---\ndescription: old locator\ntemperature: 0\n---\n\nOld body.\n"
	if err := saveManagedState(filepath.Join(dir, managedStateFile), map[string]string{
		"locator.md": sha256Hex([]byte(oldContent)),
	}); err != nil {
		t.Fatalf("saveManagedState: %v", err)
	}

	// The file on disk diverges from both the recorded hash's content AND
	// the current embedded content — the user's own edit.
	userContent := "---\ndescription: user's locator\n---\n\nUser body.\n"
	path := filepath.Join(dir, "locator.md")
	if err := os.WriteFile(path, []byte(userContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	res := mustSync(t, dir, false)

	found := false
	for _, n := range res.SkippedModified {
		if n == "locator" {
			found = true
		}
	}
	if !found {
		t.Errorf("SkippedModified = %v, want to contain %q", res.SkippedModified, "locator")
	}
	if got := readFile(t, path); got != userContent {
		t.Errorf("user content was overwritten: got %q, want %q", got, userContent)
	}
}

// 7. force=true overwrites a modified file and resurrects a deleted one.
func TestSync_Force_OverwritesModifiedAndResurrectsDeleted(t *testing.T) {
	dir := t.TempDir()
	mustSync(t, dir, false)

	modifiedPath := filepath.Join(dir, "locator.md")
	userContent := "---\ndescription: user's locator\n---\n\nUser body.\n"
	if err := os.WriteFile(modifiedPath, []byte(userContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	deletedPath := filepath.Join(dir, "reviewer.md")
	if err := os.Remove(deletedPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	res := mustSync(t, dir, true)

	wantUpdated := map[string]bool{"locator": true, "reviewer": true}
	for name := range wantUpdated {
		inUpdated := false
		for _, n := range res.Updated {
			if n == name {
				inUpdated = true
			}
		}
		inInstalled := false
		for _, n := range res.Installed {
			if n == name {
				inInstalled = true
			}
		}
		if !inUpdated && !inInstalled {
			t.Errorf("%s: expected force sync to write it (Updated or Installed), got %+v", name, res)
		}
	}
	if len(res.SkippedModified) != 0 || len(res.SkippedDeleted) != 0 {
		t.Errorf("force sync must never skip: %+v", res)
	}

	locatorWant := readFile(t, filepath.Join("builtin", "locator.md"))
	if got := readFile(t, modifiedPath); got != locatorWant {
		t.Errorf("force sync did not overwrite modified file: got %q, want %q", got, locatorWant)
	}
	if _, err := os.Stat(deletedPath); err != nil {
		t.Errorf("force sync did not resurrect deleted file: %v", err)
	}
}

// 8. Corrupt .managed.json: no hard error, existing files are not clobbered.
func TestSync_CorruptState_NoHardErrorNoClobber(t *testing.T) {
	dir := t.TempDir()
	mustSync(t, dir, false)

	statePath := filepath.Join(dir, managedStateFile)
	if err := os.WriteFile(statePath, []byte("{not valid json"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	res, err := Sync(dir, false)
	if err != nil {
		t.Fatalf("Sync returned a hard error on corrupt state: %v", err)
	}

	// With state gone, every already-installed file looks untracked, so
	// Sync must treat it as "not ours" and leave it alone rather than
	// guessing and overwriting it.
	if !containsAll(res.SkippedModified, builtinNames) {
		t.Errorf("SkippedModified = %v, want %v", res.SkippedModified, builtinNames)
	}

	for _, name := range builtinNames {
		want := readFile(t, filepath.Join("builtin", name+".md"))
		got := readFile(t, filepath.Join(dir, name+".md"))
		if got != want {
			t.Errorf("%s: file content changed despite corrupt state (expected untouched)", name)
		}
	}
}

// 9. .managed.json must never be picked up by LoadDir as an agent
// definition — it has no .md suffix, but this test pins that behavior down
// explicitly since it's exactly the kind of thing a future refactor could
// break without noticing.
func TestSync_ManagedStateFileNotSeenAsAgent(t *testing.T) {
	dir := t.TempDir()
	mustSync(t, dir, false)

	defs, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	for _, def := range defs {
		if def.Name == ".managed" || def.Name == managedStateFile {
			t.Errorf("LoadDir picked up the managed state file as an agent: %+v", def)
		}
	}
	if len(defs) != len(builtinNames) {
		t.Errorf("LoadDir returned %d defs, want %d: %+v", len(defs), len(builtinNames), defs)
	}
}

// 10. Builtin() parses every embedded definition without error and returns
// exactly the three shipped agents.
func TestBuiltin_ParsesAllWithoutError(t *testing.T) {
	defs, err := Builtin()
	if err != nil {
		t.Fatalf("Builtin() returned error: %v", err)
	}
	if len(defs) != 3 {
		t.Fatalf("Builtin() returned %d defs, want 3: %+v", len(defs), defs)
	}
	got := make([]string, len(defs))
	for i, d := range defs {
		got[i] = d.Name
	}
	if !containsAll(got, builtinNames) {
		t.Errorf("Builtin() names = %v, want %v", got, builtinNames)
	}
}
