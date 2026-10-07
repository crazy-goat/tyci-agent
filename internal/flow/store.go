package flow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

const stateFile = "state.json"

// Store writes RunState to <Dir>/state.json atomically.
type Store struct{ Dir string }

// NewRunID returns the run id YYYYMMDD-HHMMSS-<issue> (UTC).
func NewRunID(issue int, now time.Time) string {
	return now.UTC().Format("20060102-150405") + "-" + strconv.Itoa(issue)
}

// RunDir returns <home>/.tyci/runs/<repo>/<run>.
func RunDir(home, repoName, runID string) string {
	return filepath.Join(home, ".tyci", "runs", repoName, runID)
}

var secretRe = regexp.MustCompile(`ghp_[A-Za-z0-9]+|github_pat_[A-Za-z0-9_]+|sk-[A-Za-z0-9_-]+|Bearer\s+\S+`)

// MaskSecrets replaces token-looking strings with ***.
func MaskSecrets(s string) string {
	return secretRe.ReplaceAllString(s, "***")
}

// Save sets st.UpdatedAt and writes a masked copy of st (0600, dir 0700)
// through a temp file and rename, so a reader never sees a partial file.
func (s *Store) Save(st *RunState) error {
	st.UpdatedAt = time.Now().UTC()
	cp := *st
	cp.Reason = MaskSecrets(st.Reason)
	if st.Ask != nil {
		a := *st.Ask
		a.Message = MaskSecrets(a.Message)
		a.Reason = MaskSecrets(a.Reason)
		cp.Ask = &a
	}
	cp.History = make([]Step, len(st.History))
	for i, h := range st.History {
		h.StderrTail = MaskSecrets(h.StderrTail)
		h.Error = MaskSecrets(h.Error)
		if h.Warnings != nil {
			w := make([]string, len(h.Warnings))
			for j, x := range h.Warnings {
				w[j] = MaskSecrets(x)
			}
			h.Warnings = w
		}
		cp.History[i] = h
	}
	data, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, stateFile+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.Dir, stateFile))
}

// Load reads <dir>/state.json.
func Load(dir string) (*RunState, error) {
	data, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		return nil, fmt.Errorf("flow: read state: %w", err)
	}
	var st RunState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("flow: parse state: %w", err)
	}
	if st.Version != 1 {
		return nil, fmt.Errorf("flow: unsupported state version %d", st.Version)
	}
	return &st, nil
}

// LatestRun returns the newest run directory of a repo (names start with a
// timestamp, so string order is time order).
func LatestRun(home, repoName string) (string, error) {
	base := filepath.Join(home, ".tyci", "runs", repoName)
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("flow: no runs: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("flow: no runs in %s", base)
	}
	sort.Strings(names)
	return filepath.Join(base, names[len(names)-1]), nil
}

// RecentRuns returns the saved states of the n newest runs of a repo, newest
// first. Runs with an unreadable state.json are skipped.
func RecentRuns(home, repoName string, n int) []*RunState {
	base := filepath.Join(home, ".tyci", "runs", repoName)
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	var out []*RunState
	for _, name := range names {
		if len(out) == n {
			break
		}
		if st, err := Load(filepath.Join(base, name)); err == nil {
			out = append(out, st)
		}
	}
	return out
}
