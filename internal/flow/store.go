package flow

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const stateFile = "state.json"

// Store writes RunState to <Dir>/state.json atomically.
type Store struct{ Dir string }

// NewRunID returns the run id YYYYMMDD-HHMMSS-<issue> (UTC). A run without an
// issue (issue 0) gets a random suffix, so two such runs in one second differ.
func NewRunID(issue int, now time.Time) string {
	id := now.UTC().Format("20060102-150405") + "-" + strconv.Itoa(issue)
	if issue > 0 {
		return id
	}
	var b [3]byte
	_, _ = rand.Read(b[:]) // crypto/rand does not fail on supported platforms
	return id + "-" + hex.EncodeToString(b[:])
}

// RunDir returns <home>/.tyci/runs/<repo>/<run>.
func RunDir(home, repoName, runID string) string {
	return filepath.Join(home, ".tyci", "runs", repoName, runID)
}

// StatePath returns the state.json of a run dir.
func StatePath(runDir string) string {
	return filepath.Join(runDir, stateFile)
}

// FindRun returns the saved state of run runID and the path of its state.json.
// It searches the runs of every repository under home. It fails when no
// repository or more than one repository has the run.
func FindRun(home, runID string) (*RunState, string, error) {
	var matches []string
	if runIDPattern.MatchString(runID) {
		matches, _ = filepath.Glob(StatePath(RunDir(home, "*", runID)))
	}
	switch len(matches) {
	case 0:
		return nil, "", fmt.Errorf("run not found: %s", runID)
	case 1:
		st, err := Load(filepath.Dir(matches[0]))
		return st, matches[0], err
	default:
		return nil, "", fmt.Errorf("run id is ambiguous: %s", strings.Join(matches, ", "))
	}
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
		h.Note = MaskSecrets(h.Note)
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
	// A state saved before params existed has none. The issue is then the
	// only param that can be filled, so templates still read {{.Params.issue}}.
	if st.Params == nil {
		st.Params = map[string]string{}
	}
	if _, ok := st.Params[IssueParam]; !ok && st.Issue > 0 {
		st.Params[IssueParam] = strconv.Itoa(st.Issue)
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

// runDirsNewestFirst returns the names of the run directories of a repo, newest
// first. A missing repo directory gives no names.
func runDirsNewestFirst(home, repoName string) (base string, names []string) {
	base = filepath.Join(home, ".tyci", "runs", repoName)
	entries, err := os.ReadDir(base)
	if err != nil {
		return base, nil
	}
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return base, names
}

// RecentRuns returns the saved states of the n newest runs of a repo, newest
// first. Runs with an unreadable state.json are skipped.
func RecentRuns(home, repoName string, n int) []*RunState {
	base, names := runDirsNewestFirst(home, repoName)
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

// DefaultRunListLimit is the page size of ListRuns when the caller gives none.
const DefaultRunListLimit = 20

// RunRow is one run of a run list. The CLI and the chat tool print these fields.
type RunRow struct {
	Run       string            `json:"run"`
	Workflow  string            `json:"workflow"`
	Params    map[string]string `json:"params,omitempty"`
	Status    string            `json:"status"`
	State     string            `json:"state"`
	StartedAt time.Time         `json:"started_at"`
}

// RunList is one page of the runs of a repo, newest first.
type RunList struct {
	Runs   []RunRow `json:"runs"`
	Total  int      `json:"total"`  // runs that match the filter, on all pages
	Offset int      `json:"offset"` // runs skipped before this page
	Limit  int      `json:"limit"`
	More   bool     `json:"more"` // runs follow this page
}

// ListRuns returns one page of the runs of repoName. Without archived it lists
// only running and paused runs. With archived it also lists done, stopped and
// failed runs. A limit of 0 means DefaultRunListLimit. Runs with an unreadable
// state.json are skipped.
func ListRuns(home, repoName string, archived bool, limit, offset int) (RunList, error) {
	if limit == 0 {
		limit = DefaultRunListLimit
	}
	if limit < 0 {
		return RunList{}, errors.New("limit must be a positive number")
	}
	if offset < 0 {
		return RunList{}, errors.New("offset must not be negative")
	}
	base, names := runDirsNewestFirst(home, repoName)
	var all []*RunState
	for _, name := range names {
		st, err := Load(filepath.Join(base, name))
		if err != nil {
			continue
		}
		if archived || st.Status == "running" || st.Status == "paused" {
			all = append(all, st)
		}
	}
	start := min(offset, len(all))
	end := min(start+limit, len(all))
	out := RunList{Runs: []RunRow{}, Total: len(all), Offset: offset, Limit: limit, More: end < len(all)}
	for _, st := range all[start:end] {
		out.Runs = append(out.Runs, RunRow{Run: st.Run, Workflow: st.Workflow, Params: st.Params,
			Status: st.Status, State: st.Current, StartedAt: st.StartedAt})
	}
	return out, nil
}

// RunByID returns the state of the run runID of repository repoName. ok is
// false when runID is not a run id, when that run has no state file, or when
// the run path is not a directory. err is set when the state file exists but
// cannot be read.
func RunByID(home, repoName, runID string) (st *RunState, ok bool, err error) {
	if !runIDPattern.MatchString(runID) {
		return nil, false, nil
	}
	st, err = Load(filepath.Join(home, ".tyci", "runs", repoName, runID))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return st, true, nil
}

// RunOfSession returns the run that has an agent step whose session is one of
// sessions (job ids of agents). ok is false when no run of repoName has such a
// step, for example an agent started outside a workflow. A run directory
// without a state file is not a run. err is set when a state file exists but
// cannot be read, and no run has the session: the agent may belong to that run.
func RunOfSession(home, repoName string, sessions []string) (st *RunState, ok bool, err error) {
	want := map[string]bool{}
	for _, s := range sessions {
		if s != "" {
			want[s] = true
		}
	}
	if len(want) == 0 {
		return nil, false, nil
	}
	base := filepath.Join(home, ".tyci", "runs", repoName)
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("flow: read runs of %s: %w", repoName, err)
	}
	var unreadable error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		run, err := Load(filepath.Join(base, e.Name()))
		if err != nil {
			if unreadable == nil && !errors.Is(err, fs.ErrNotExist) {
				unreadable = err
			}
			continue
		}
		for _, h := range run.History {
			if want[h.Session] {
				return run, true, nil
			}
		}
	}
	if unreadable != nil {
		return nil, false, fmt.Errorf("flow: a run of %s cannot be read, so the agent may belong to it: %w", repoName, unreadable)
	}
	return nil, false, nil
}
