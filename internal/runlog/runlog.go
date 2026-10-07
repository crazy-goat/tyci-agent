// Package runlog writes one redacted transcript per agent visit of a workflow
// run, and removes the runs that are old.
package runlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/redact"
	"github.com/crazy-goat/tyci-agent/session"
)

// Writer is the transcript file of one agent visit
// (<run dir>/agents/NNN-<role>.jsonl, session JSONL format).
type Writer struct {
	s *session.Session
}

// Path returns <runDir>/agents/NNN-<role>.jsonl.
func Path(runDir, role string, seq int) string {
	return filepath.Join(runDir, "agents", fmt.Sprintf("%03d-%s.jsonl", seq, role))
}

// Open creates the transcript file (dir 0700, file 0600). It fails when the
// file exists: an old transcript is never appended to.
func Open(runDir, role string, seq int, cwd, model, provider string) (*Writer, error) {
	return OpenPath(Path(runDir, role, seq), cwd, model, provider)
}

// OpenPath is Open for a path that Path returned.
func OpenPath(p, cwd, model, provider string) (*Writer, error) {
	if _, err := os.Stat(p); err == nil {
		return nil, fmt.Errorf("transcript %s exists", p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	s, err := session.Open(p, cwd, model, provider)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(p, 0o600); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.SetBlockFilter(redactBlocks)
	return &Writer{s: s}, nil
}

// Session returns the underlying session. Messages written to it are redacted
// too, so it can be set as the agent's Config.Session.
func (w *Writer) Session() *session.Session { return w.s }

// Write appends one message. Every string in the blocks is redacted first.
func (w *Writer) Write(role string, blocks []session.ContentBlock) error {
	return w.s.WriteMessage(role, blocks, nil)
}

// Close closes the file.
func (w *Writer) Close() error { return w.s.Close() }

// redactBlocks returns a redacted copy of blocks.
func redactBlocks(blocks []session.ContentBlock) []session.ContentBlock {
	out := make([]session.ContentBlock, len(blocks))
	for i, b := range blocks {
		b.Text = redact.Redact(b.Text)
		b.Thinking = redact.Redact(b.Thinking)
		b.Name = redact.Redact(b.Name)
		b.ToolName = redact.Redact(b.ToolName)
		if len(b.Arguments) > 0 {
			// Redact the JSON text. A secret never holds a quote or a
			// backslash, so the result stays valid JSON unless a private key
			// with escaped newlines is cut; then keep it valid as a string.
			r := redact.Redact(string(b.Arguments))
			if json.Valid([]byte(r)) {
				b.Arguments = json.RawMessage(r)
			} else {
				q, _ := json.Marshal(r)
				b.Arguments = q
			}
		}
		out[i] = b
	}
	return out
}

// Prune removes <runsDir>/<repo>/<run> directories whose state.json says done
// or failed and whose mtime is older than days. Days 0 keeps everything. It
// returns the number of removed runs.
func Prune(runsDir string, days int, now time.Time) (removed int, err error) {
	if days < 0 {
		return 0, fmt.Errorf("logs.retention_days: %d is negative", days)
	}
	if days == 0 {
		return 0, nil
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	paths, err := filepath.Glob(filepath.Join(runsDir, "*", "*", "state.json"))
	if err != nil {
		return 0, err
	}
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var st struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(b, &st) != nil || (st.Status != "done" && st.Status != "failed") {
			continue
		}
		if err := os.RemoveAll(filepath.Dir(p)); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
