package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/crazy-goat/tyci-agent/session"
)

// tailResult keeps the last max bytes of content. It reports whether content
// was cut. A content of exactly max bytes is not cut. The cut moves forward to
// the next UTF-8 rune start, so the tail may be a few bytes shorter than max.
func tailResult(content string, max int) (tail string, truncated bool) {
	if len(content) <= max {
		return content, false
	}
	start := len(content) - max
	for start < len(content) && !utf8.RuneStart(content[start]) {
		start++
	}
	return content[start:], true
}

// truncationNote is the first line of a truncated result. It tells the model
// how much it sees and where the full output is. An empty path means that the
// full output could not be saved.
func truncationNote(max, total int, path string) string {
	if path == "" {
		return fmt.Sprintf("[truncated: showing the last %d of %d bytes; full output could not be saved]", max, total)
	}
	return fmt.Sprintf("[truncated: showing the last %d of %d bytes; full output: %s]", max, total, path)
}

// writeFull saves the full output of a call to <dir>/<callID>.txt and returns
// the path. The directory is private to the user, and so is the file. dir is
// empty when no session is open, so nothing is saved.
func writeFull(dir, callID, content string) (string, error) {
	if dir == "" {
		return "", errors.New("no result directory: no session is open")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, callID+".txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// toolResultDir returns the directory of the full results of a session:
// ~/.tyci/tool-results/<session-id>. It is empty when no session is open.
// The session id is unique, so the call ids c1, c2, ... of two sessions do
// not collide.
func toolResultDir(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".tyci", "tool-results", sess.ID())
}
