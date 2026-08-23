// Package gitinfo answers one question cheaply: which branch is this
// directory on?
//
// The top status bar redraws on every keystroke, so the obvious
// implementation — shell out to `git rev-parse --abbrev-ref HEAD` — is the
// wrong one: a process spawn per frame is a visible cost for a string that
// changes a few times an hour. Reading .git/HEAD directly is two syscalls,
// and it is the same file git itself would consult.
//
// Worktrees are the reason this is more than one ReadFile. In a linked
// worktree .git is a *file* holding "gitdir: /path/to/main/.git/worktrees/x",
// and the HEAD that matters lives there, not in the main repository. Missing
// that would label every tyci worktree with the parent's branch — precisely
// the case where knowing the branch matters most.
package gitinfo

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// cacheTTL bounds how stale a displayed branch can be. Short enough that a
// `git checkout` in another pane shows up while you are still looking at the
// bar, long enough that a burst of redraws costs one read.
const cacheTTL = 2 * time.Second

type entry struct {
	branch string
	at     time.Time
}

var (
	mu    sync.Mutex
	cache = map[string]entry{}
	now   = time.Now // swapped in tests
)

// Branch returns the branch dir is on, the short commit for a detached HEAD,
// or "" when dir is not in a git repository (or the repository is unreadable —
// a status bar has nothing useful to say about that either way).
func Branch(dir string) string {
	if dir == "" {
		return ""
	}
	mu.Lock()
	if e, ok := cache[dir]; ok && now().Sub(e.at) < cacheTTL {
		mu.Unlock()
		return e.branch
	}
	mu.Unlock()

	b := readBranch(dir)

	mu.Lock()
	cache[dir] = entry{branch: b, at: now()}
	mu.Unlock()
	return b
}

func readBranch(dir string) string {
	gitDir := findGitDir(dir)
	if gitDir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(data))
	if ref, ok := strings.CutPrefix(head, "ref:"); ok {
		ref = strings.TrimSpace(ref)
		// refs/heads/feature/x -> feature/x; anything else (refs/tags/...)
		// is shown as-is rather than mangled.
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			return name
		}
		return ref
	}
	// Detached HEAD: a raw object id. Show it the way git does.
	if len(head) >= 7 && isHex(head) {
		return head[:7]
	}
	return ""
}

// findGitDir walks up from dir looking for .git, and resolves the linked-
// worktree indirection when it finds a file instead of a directory.
func findGitDir(dir string) string {
	_, gitDir := locateGitDir(dir)
	return gitDir
}

// locateGitDir walks up from dir looking for .git and returns both the
// directory that contains it (the working-tree root git considers dir to be
// part of) and the resolved git dir itself (following the linked-worktree
// indirection when .git is a file, not a directory). Both are "" when dir is
// not inside a git repository.
func locateGitDir(dir string) (workRoot, gitDir string) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", ""
	}
	for {
		candidate := filepath.Join(d, ".git")
		if fi, err := os.Stat(candidate); err == nil {
			if fi.IsDir() {
				return d, candidate
			}
			return d, resolveGitFile(d, candidate)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", ""
		}
		d = parent
	}
}

// ProjectRoot returns the top-level working directory of the git repository
// containing dir, resolving a linked worktree (created via `git worktree
// add`) to the main repository it belongs to — so every worktree of one
// project shares the same root. Returns "" when dir is not inside a git
// repository (or the repository is unreadable), leaving the caller to fall
// back to its own default (e.g. the absolute cwd).
//
// A linked worktree's resolved git dir looks like
// "<main>/.git/worktrees/<name>"; detecting that shape is what lets this
// answer "which project" rather than "which worktree checkout".
func ProjectRoot(dir string) string {
	workRoot, gitDir := locateGitDir(dir)
	if gitDir == "" {
		return ""
	}
	root := workRoot
	if mainGitDir := mainGitDirFromWorktree(gitDir); mainGitDir != "" {
		root = filepath.Dir(mainGitDir)
	}
	// Linked-worktree gitdir pointer files are written by git with fully
	// resolved (symlink-free) absolute paths, while workRoot above comes
	// straight from filepath.Abs(dir) — no symlink resolution. On a system
	// where the path to a repo runs through a symlink (macOS's /tmp ->
	// /private/tmp is the everyday example), those two would otherwise
	// disagree about "the same project"'s root and split one project into
	// two session pools. Normalize away that mismatch; if the path can't be
	// resolved (already gone, permissions), fall back to the unresolved
	// value rather than losing the answer entirely.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return root
}

// mainGitDirFromWorktree returns the main repository's ".git" directory when
// gitDir points into "<main>/.git/worktrees/<name>", or "" when gitDir is
// not shaped like a linked worktree's git dir (e.g. it already is a main
// repo's ".git").
func mainGitDirFromWorktree(gitDir string) string {
	worktrees := filepath.Dir(gitDir)
	if filepath.Base(worktrees) != "worktrees" {
		return ""
	}
	dotGit := filepath.Dir(worktrees)
	if filepath.Base(dotGit) != ".git" {
		return ""
	}
	return dotGit
}

// resolveGitFile reads a "gitdir: <path>" pointer file. The path may be
// relative, in which case it is relative to the directory holding the file.
func resolveGitFile(base, file string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	p, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
