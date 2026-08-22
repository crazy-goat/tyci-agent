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
	d, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(d, ".git")
		if fi, err := os.Stat(candidate); err == nil {
			if fi.IsDir() {
				return candidate
			}
			return resolveGitFile(d, candidate)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
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
