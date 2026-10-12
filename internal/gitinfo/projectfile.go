package gitinfo

import (
	"os"
	"path/filepath"
	"strings"
)

// ProjectFile returns the path of rel inside the .tyci directory of the
// project that wd belongs to, or "" when neither the worktree nor the main
// checkout has it.
//
// The worktree root (git toplevel of wd) is checked first, then the main
// checkout (ProjectRoot). The rule is per entry, not per directory: a
// worktree that has its own .tyci/ does not hide the main checkout's
// config.json or hooks.json. Symlinks do not count, so a tracked link
// ".tyci -> elsewhere" is ignored.
func ProjectFile(wd, rel string) string {
	if files := ProjectFiles(wd, rel); len(files) > 0 {
		return files[0]
	}
	return ""
}

// ProjectFiles returns the path of rel in every root of the project of wd that
// has it, worktree first. Use it to merge a directory from both checkouts, for
// example the workflow names of a listing.
func ProjectFiles(wd, rel string) []string {
	worktree, main := projectRoots(wd)
	var files []string
	for _, root := range uniqueRoots(worktree, main) {
		if isRealEntry(root, rel) {
			files = append(files, filepath.Join(root, ".tyci", rel))
		}
	}
	return files
}

// ProjectPath returns ProjectFile(wd, rel), or, when no such entry exists,
// the path it would have in the main checkout. Use it for a missing file a
// caller only reads (the read fails as "not exist", as before) and for a
// target a caller creates.
func ProjectPath(wd, rel string) string {
	if p := ProjectFile(wd, rel); p != "" {
		return p
	}
	return filepath.Join(ProjectDir(wd), ".tyci", rel)
}

// ProjectDir returns the main checkout of the project of wd, the place for
// state shared by every worktree. It falls back to the worktree root, and
// then to the absolute wd, when git cannot tell (no repository, bare
// repository, deleted main checkout).
func ProjectDir(wd string) string {
	_, main := projectRoots(wd)
	return main
}

// projectRoots returns the worktree root of wd and the main checkout root.
// Both are equal outside a linked worktree, and both are wd when wd is not
// inside a repository. It returns "" only when wd cannot be made absolute.
func projectRoots(wd string) (worktree, main string) {
	abs, err := filepath.Abs(wd)
	if err != nil {
		return "", ""
	}
	worktree = abs
	if top, _ := locateGitDir(abs); top != "" {
		worktree = top
	}
	main = ProjectRoot(abs)
	if !isDir(main) {
		// A deleted or moved main checkout is not a root: its git pointer is
		// still there, but the directory is gone.
		main = worktree
	}
	// In a plain checkout the two roots name the same directory. Keep the
	// resolved main spelling so callers see one path, as before.
	if resolved, err := filepath.EvalSymlinks(worktree); err == nil && resolved == main {
		worktree = main
	}
	return worktree, main
}

// uniqueRoots drops the second root when it equals the first, so a plain
// checkout is looked at once.
func uniqueRoots(worktree, main string) []string {
	if worktree == main {
		return []string{worktree}
	}
	return []string{worktree, main}
}

// isRealEntry reports whether <root>/.tyci/<rel> exists and no component
// from .tyci down is a symlink. The .tyci directory itself must be a real
// directory.
func isRealEntry(root, rel string) bool {
	cur := filepath.Join(root, ".tyci")
	fi, err := os.Lstat(cur)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if rel == "" {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, part)
		if !isNotSymlink(cur) {
			return false
		}
	}
	return true
}

// isNotSymlink reports whether p exists and is not a symbolic link.
func isNotSymlink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink == 0
}

// isDir reports whether path is an existing directory. An empty path is not.
func isDir(path string) bool {
	st, err := os.Stat(path)
	return path != "" && err == nil && st.IsDir()
}
