// Package rolememory keeps the notes a workflow role writes for its own next
// session. There is one file per repository and role:
//
//	<home>/.tyci/memory/<repo>/<role>.md
//
// The notes live in the user's home directory, not in the repository, so they
// do not show up in git status and are shared by every worktree of the
// repository. The repo key is the base name of the repository directory, the
// same key that worktree.AddIssue uses.
//
// Block is the only part that reaches the model: it returns the tail of the
// file for the system prompt of the role. The caller passes home explicitly,
// so tests can use t.TempDir() and never touch the real ~/.tyci.
package rolememory

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/crazy-goat/tyci-agent/internal/debug"
)

// ErrInvalid marks an input the caller can fix: a bad repo or role name, an
// empty note, or a note over MaxInject. The memory tool reports these as
// validation errors.
var ErrInvalid = errors.New("invalid role memory input")

// MaxInject caps the note text that Block returns, in bytes. The text is sent
// with every request of the role, so a larger file is cut down to its tail.
// Write refuses a note over this size, so only a hand-edited file can be
// larger.
const MaxInject = 16 << 10

// Path returns the note file of role in repo under home. It refuses names that
// could leave the memory directory, and it checks the joined path once more so
// that a future change to the rules cannot open a traversal.
func Path(home, repo, role string) (string, error) {
	if home == "" {
		return "", fmt.Errorf("%w: home directory is empty", ErrInvalid)
	}
	if err := checkName("repo", repo); err != nil {
		return "", err
	}
	if err := checkName("role", role); err != nil {
		return "", err
	}
	root := filepath.Join(home, ".tyci", "memory")
	p := filepath.Join(root, repo, role+".md")
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q leaves %s", ErrInvalid, p, root)
	}
	return p, nil
}

// checkName accepts a single path element that does not start with a dot.
// That rules out "", "..", "." and hidden names, and any separator rules out
// a nested path.
func checkName(what, name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: %s is empty", ErrInvalid, what)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("%w: %s %q starts with a dot", ErrInvalid, what, name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%w: %s %q contains a path separator", ErrInvalid, what, name)
	}
	return nil
}

// Read returns the whole note file. A missing file gives an error that
// matches fs.ErrNotExist.
func Read(home, repo, role string) (string, error) {
	p, err := Path(home, repo, role)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Write replaces the note file with content. The file is written to a temporary
// file in the same directory and renamed over the old one, so a reader never
// sees half a note. The mode is 0600 even when the file already existed.
func Write(home, repo, role, content string) error {
	p, err := Path(home, repo, role)
	if err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("%w: content is empty", ErrInvalid)
	}
	if len(content) > MaxInject {
		return fmt.Errorf("%w: note is %d bytes, the limit is %d", ErrInvalid, len(content), MaxInject)
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, role+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(content)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Block returns the text to add to the system prompt of role, or "" when there
// is nothing to add. It returns at most MaxInject bytes: the tail of the file,
// moved forward to the start of a line or, when the window has no line break,
// to the start of a rune. A file over the limit is logged to dl as a warning.
// dl may be nil.
func Block(home, repo, role string, dl *debug.Logger) string {
	p, err := Path(home, repo, role)
	if err != nil {
		warnf(dl, "role memory skipped: %v", err)
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			warnf(dl, "role memory skipped: %v", err)
		}
		return ""
	}
	if len(data) > MaxInject {
		warnf(dl, "warning: %s is %d bytes; only the last %d bytes go into the prompt", p, len(data), MaxInject)
		data = tail(data, MaxInject)
	}
	return strings.TrimSpace(string(data))
}

// tail returns the last n bytes of data, starting on a line or rune boundary.
// The first line of the window is dropped when a full line follows it, so the
// text never starts in the middle of a line.
func tail(data []byte, n int) []byte {
	data = data[len(data)-n:]
	if i := bytes.IndexByte(data, '\n'); i >= 0 && i+1 < len(data) {
		return data[i+1:]
	}
	for len(data) > 0 && !utf8.RuneStart(data[0]) {
		data = data[1:]
	}
	return data
}

func warnf(dl *debug.Logger, format string, args ...any) {
	if dl != nil {
		fmt.Fprintf(dl, format+"\n", args...)
	}
}
