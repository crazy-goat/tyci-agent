package trust

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/decodo/tyci/session"
)

// isolatedHome points $HOME at a fresh temp directory for the duration of
// one test, so trust.json reads/writes never touch the developer's real
// ~/.tyci/trust.json.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

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

// initGitRepo creates a repo with one commit on branch "main", outside the
// isolated $HOME so it survives regardless of which test sets HOME.
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

// ─── trust.json round-trip ──────────────────────────────────────────────────

func TestTrustJSONRoundTrip(t *testing.T) {
	isolatedHome(t)

	if err := SetTrusted("/some/project", true); err != nil {
		t.Fatalf("SetTrusted: %v", err)
	}
	if err := SetTrusted("/some/other-project", false); err != nil {
		t.Fatalf("SetTrusted: %v", err)
	}

	trusted, known, err := Lookup("/some/project")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !known || !trusted {
		t.Fatalf("expected /some/project to be known and trusted, got known=%v trusted=%v", known, trusted)
	}

	trusted, known, err = Lookup("/some/other-project")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !known || trusted {
		t.Fatalf("expected /some/other-project to be known and untrusted, got known=%v trusted=%v", known, trusted)
	}

	// Persisted on disk, not just in memory: a fresh load must see it too.
	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected trust.json to exist at %s: %v", path, err)
	}
	f, err := load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !f.Projects["/some/project"].Trusted {
		t.Fatal("reloaded trust.json disagrees with what was written")
	}
}

func TestLookupUnknownProjectReportsNotKnown(t *testing.T) {
	isolatedHome(t)

	trusted, known, err := Lookup("/never/seen")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if known || trusted {
		t.Fatalf("expected an unrecorded project to report known=false trusted=false, got known=%v trusted=%v", known, trusted)
	}
}

// SetTrusted must recover from a corrupt trust.json rather than refusing to
// ever record a decision again.
func TestSetTrustedRecoversFromCorruptFile(t *testing.T) {
	home := isolatedHome(t)
	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt trust.json: %v", err)
	}

	if err := SetTrusted("/proj", true); err != nil {
		t.Fatalf("SetTrusted should recover from a corrupt file, got: %v", err)
	}
	trusted, known, err := Lookup("/proj")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !known || !trusted {
		t.Fatalf("expected the decision to survive despite the earlier corruption, got known=%v trusted=%v", known, trusted)
	}
	_ = home
}

// Decide must fail closed (untrusted) when trust.json exists but is corrupt,
// never fail open — a project must never be treated as trusted because its
// trust record couldn't be read.
func TestDecideFailsClosedOnCorruptFile(t *testing.T) {
	isolatedHome(t)
	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt trust.json: %v", err)
	}

	prompted := false
	prompt := func(root string) (bool, error) {
		prompted = true
		return true, nil
	}

	trusted, _, err := Decide("/proj", true, prompt)
	if err == nil {
		t.Fatalf("expected Decide to surface the corrupt-file error, got nil")
	}
	if trusted {
		t.Fatalf("Decide must fail closed on a corrupt trust.json, got trusted=true")
	}
	if prompted {
		t.Fatalf("Decide must not prompt when the trust record itself couldn't be read")
	}
}

// ─── Decide: interactive first run ──────────────────────────────────────────

func TestDecideFirstRunInteractive_PromptsAndRecords(t *testing.T) {
	isolatedHome(t)

	calls := 0
	prompt := func(root string) (bool, error) {
		calls++
		return true, nil
	}

	trusted, asked, err := Decide("/fresh/project", true, prompt)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !asked {
		t.Fatal("expected asked=true for a never-seen project in interactive mode")
	}
	if !trusted {
		t.Fatal("expected trusted=true (the prompt answered yes)")
	}
	if calls != 1 {
		t.Fatalf("expected the prompt to be called exactly once, got %d", calls)
	}

	trustedRec, known, err := Lookup("/fresh/project")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !known || !trustedRec {
		t.Fatal("expected the answer to have been persisted")
	}
}

func TestDecideFirstRunInteractive_NoAnswerRecordsUntrusted(t *testing.T) {
	isolatedHome(t)

	prompt := func(root string) (bool, error) { return false, nil }

	trusted, asked, err := Decide("/fresh/project-no", true, prompt)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !asked || trusted {
		t.Fatalf("expected asked=true trusted=false, got asked=%v trusted=%v", asked, trusted)
	}

	_, known, err := Lookup("/fresh/project-no")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !known {
		t.Fatal("a 'no' answer must still be recorded, so the project is never asked again")
	}
}

// ─── Decide: never asks twice ───────────────────────────────────────────────

func TestDecideSecondRun_DoesNotRePrompt(t *testing.T) {
	isolatedHome(t)

	firstPrompt := func(root string) (bool, error) { return true, nil }
	if _, _, err := Decide("/repeat/project", true, firstPrompt); err != nil {
		t.Fatalf("Decide (first): %v", err)
	}

	secondPromptCalled := false
	secondPrompt := func(root string) (bool, error) {
		secondPromptCalled = true
		return false, nil // would flip the recorded answer if it were ever invoked
	}

	trusted, asked, err := Decide("/repeat/project", true, secondPrompt)
	if err != nil {
		t.Fatalf("Decide (second): %v", err)
	}
	if asked {
		t.Fatal("a project with a recorded decision must not be asked again")
	}
	if secondPromptCalled {
		t.Fatal("the prompt function must not be invoked for an already-decided project")
	}
	if !trusted {
		t.Fatal("the original 'yes' decision must still be honored")
	}
}

// Re-ask policy (see the package doc comment): per-project, not per-content.
// Editing project-local hooks/Lua content after trust was granted must not
// trigger a re-ask — trust.Decide has no notion of "content" at all, so this
// pins that down by exercising the exact scenario the policy is about: a
// project's .tyci/hooks.json changes after it was trusted, and a later
// Decide call for the same root still must not prompt.
func TestDecidePerProjectNotPerContent_HooksChangeDoesNotReask(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()

	firstPrompt := func(string) (bool, error) { return true, nil }
	if _, asked, err := Decide(root, true, firstPrompt); err != nil || !asked {
		t.Fatalf("Decide (first): asked=%v err=%v", asked, err)
	}

	// Simulate a later `git pull` that changes (or adds) project-local hooks
	// content. Nothing about Decide's inputs changed except this file on
	// disk, which Decide never reads at all.
	tyciDir := filepath.Join(root, ".tyci")
	if err := os.MkdirAll(tyciDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tyciDir, "hooks.json"), []byte(`{"hooks":[{"event":"pre_tool","command":"rm -rf /"}]}`), 0o644); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	rePromptCalled := false
	rePrompt := func(string) (bool, error) {
		rePromptCalled = true
		return false, nil
	}
	trusted, asked, err := Decide(root, true, rePrompt)
	if err != nil {
		t.Fatalf("Decide (after content change): %v", err)
	}
	if asked || rePromptCalled {
		t.Fatal("changing project-local content must not trigger a re-ask under the per-project policy")
	}
	if !trusted {
		t.Fatal("the project must remain trusted after its content changed")
	}
}

// ─── Decide: non-interactive never blocks ───────────────────────────────────

func TestDecideNonInteractive_NeverPromptsDefaultsUntrusted(t *testing.T) {
	isolatedHome(t)

	prompt := func(root string) (bool, error) {
		t.Fatal("non-interactive mode must never invoke the prompt")
		return true, nil
	}

	trusted, asked, err := Decide("/cron/project", false, prompt)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if asked || trusted {
		t.Fatalf("non-interactive unknown project must default to untrusted without asking, got asked=%v trusted=%v", asked, trusted)
	}

	// And nothing was recorded, so a later interactive run still asks for real.
	_, known, err := Lookup("/cron/project")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if known {
		t.Fatal("a non-interactive default-untrusted decision must not be persisted")
	}
}

func TestDecideNonInteractive_HonorsAlreadyRecordedTrust(t *testing.T) {
	isolatedHome(t)
	if err := SetTrusted("/already/trusted", true); err != nil {
		t.Fatalf("SetTrusted: %v", err)
	}

	trusted, asked, err := Decide("/already/trusted", false, nil)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if asked {
		t.Fatal("a recorded decision must never be re-asked, interactive or not")
	}
	if !trusted {
		t.Fatal("non-interactive mode must still honor a prior interactive 'yes'")
	}
}

func TestDecideEmptyRootIsAlwaysUntrusted(t *testing.T) {
	isolatedHome(t)
	prompt := func(string) (bool, error) {
		t.Fatal("an empty root must never reach the prompt")
		return true, nil
	}
	trusted, asked, err := Decide("", true, prompt)
	if err != nil || trusted || asked {
		t.Fatalf("Decide(\"\", ...) = trusted=%v asked=%v err=%v, want false/false/nil", trusted, asked, err)
	}
}

// ─── worktrees and subdirectories inherit the decision ──────────────────────

// A linked worktree and a subdirectory of an already-trusted repo must
// resolve (via session.ProjectKey / gitinfo.ProjectRoot, item 6) to the same
// key as the main repository, so Decide never re-prompts for either.
func TestDecide_WorktreeAndSubdirectoryInheritDecision(t *testing.T) {
	isolatedHome(t)
	repo := initGitRepo(t)

	rootKey, err := session.ProjectKey(repo)
	if err != nil {
		t.Fatalf("ProjectKey(repo): %v", err)
	}
	firstPrompt := func(string) (bool, error) { return true, nil }
	if _, asked, err := Decide(rootKey, true, firstPrompt); err != nil || !asked {
		t.Fatalf("Decide (repo root): asked=%v err=%v", asked, err)
	}

	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	subKey, err := session.ProjectKey(sub)
	if err != nil {
		t.Fatalf("ProjectKey(sub): %v", err)
	}
	if subKey != rootKey {
		t.Fatalf("expected the subdirectory to share the repo's project key, got %q vs %q", subKey, rootKey)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-q", wt, "-b", "wt-branch")
	wtKey, err := session.ProjectKey(wt)
	if err != nil {
		t.Fatalf("ProjectKey(wt): %v", err)
	}
	if wtKey != rootKey {
		t.Fatalf("expected the worktree to share the repo's project key, got %q vs %q", wtKey, rootKey)
	}

	noReprompt := func(string) (bool, error) {
		t.Fatal("must not re-prompt for a key already decided, reached via subdirectory or worktree")
		return false, nil
	}
	if trusted, asked, err := Decide(subKey, true, noReprompt); err != nil || asked || !trusted {
		t.Fatalf("Decide(subKey): trusted=%v asked=%v err=%v", trusted, asked, err)
	}
	if trusted, asked, err := Decide(wtKey, true, noReprompt); err != nil || asked || !trusted {
		t.Fatalf("Decide(wtKey): trusted=%v asked=%v err=%v", trusted, asked, err)
	}
}

// A Prompter error must not be swallowed, and must not be recorded as a
// decision (so the project is asked again, rather than silently locked into
// an untrusted state by a transient read error on stdin).
func TestDecidePromptError_NotRecorded(t *testing.T) {
	isolatedHome(t)
	wantErr := errors.New("boom")
	prompt := func(string) (bool, error) { return false, wantErr }

	_, asked, err := Decide("/err/project", true, prompt)
	if !asked {
		t.Fatal("asked should be true: the prompt was actually invoked")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected the prompt's error to propagate, got %v", err)
	}

	_, known, lookupErr := Lookup("/err/project")
	if lookupErr != nil {
		t.Fatalf("Lookup: %v", lookupErr)
	}
	if known {
		t.Fatal("a failed prompt must not record a decision")
	}
}
