package flow

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const stateFile = "state.json"

// Store writes RunState to <Dir>/state.json atomically.
type Store struct{ Dir string }

// runCounterFile holds the last run number of a repository. It lives in the
// runs dir of the repository, next to the run dirs.
const runCounterFile = "counter"

// RepoKey returns the runs dir name of repo ("owner/name", or a GitLab path with
// groups): every "/" becomes "+". No GitHub or GitLab name has a "+", so two
// repos never share a runs dir (#846).
func RepoKey(repo string) string { return strings.ReplaceAll(repo, "/", "+") }

// legacyKey returns the runs dir name that key had before #846 (the name without
// the owner) and the repo of key. ok is false for a key without an owner.
func legacyKey(key string) (legacy, repo string, ok bool) {
	i := strings.LastIndex(key, "+")
	if i < 0 {
		return "", "", false
	}
	return key[i+1:], strings.ReplaceAll(key, "+", "/"), true
}

// legacyRunOf reports whether st, read from the legacy runs dir, is a run of
// repo. A state without a repo is taken as one, as before #846. It fills in the
// repo of such a state.
func legacyRunOf(st *RunState, repo string) bool {
	if st.Repo == "" {
		st.Repo = repo
	}
	return st.Repo == repo
}

// NewRunID reserves the next run number of repository repoName and returns it
// as the run id, for example "17". The number is kept in runCounterFile under the
// runs lock, so parallel starts get different numbers, and a number is never
// reused, even after its run dir is deleted. A missing counter starts after the
// highest existing run number, also of the legacy runs dir (#846), so a new run
// number never shadows an old run of the repo.
func NewRunID(home, repoName string) (string, error) {
	base := filepath.Join(home, ".tyci", "runs", repoName)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	unlock, err := lockRun(base)
	if err != nil {
		return "", err
	}
	defer unlock()
	path := filepath.Join(base, runCounterFile)
	last, err := readRunCounter(path, base)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		if legacy, _, ok := legacyKey(repoName); ok {
			old := filepath.Join(home, ".tyci", "runs", legacy)
			if n, err := readRunCounter(filepath.Join(old, runCounterFile), old); err == nil {
				last = max(last, n)
			}
		}
	}
	next := last + 1
	if err := writeFileAtomic(path, []byte(strconv.Itoa(next)+"\n")); err != nil {
		return "", err
	}
	return strconv.Itoa(next), nil
}

// writeFileAtomic writes data to path (0600) through a temp file in the same
// dir and a rename. A crash or a full disk leaves the old file or the new one,
// never a truncated file.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
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
	return os.Rename(tmp.Name(), path)
}

// readRunCounter returns the last run number in path. When path does not exist,
// it returns the highest numeric run dir in base, or 0.
func readRunCounter(path, base string) (int, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		n, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return 0, fmt.Errorf("flow: bad run counter %s: %w", path, err)
		}
		return n, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0, err
	}
	highest := 0
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); e.IsDir() && err == nil && n > highest {
			highest = n
		}
	}
	return highest, nil
}

// RunDir returns <home>/.tyci/runs/<repo>/<run>. When that dir has no state
// but the legacy runs dir (before #846) has this run of the repo, it returns
// the legacy dir, so old runs still load and resume.
func RunDir(home, repoName, runID string) string {
	dir := filepath.Join(home, ".tyci", "runs", repoName, runID)
	legacy, repo, ok := legacyKey(repoName)
	if !ok {
		return dir
	}
	if _, err := os.Stat(StatePath(dir)); err == nil {
		return dir
	}
	old := filepath.Join(home, ".tyci", "runs", legacy, runID)
	if st, err := Load(old); err == nil && legacyRunOf(st, repo) {
		return old
	}
	return dir
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
	if id, ok := ParseRunID(runID); ok {
		matches, _ = filepath.Glob(StatePath(RunDir(home, "*", id)))
	}
	switch len(matches) {
	case 0:
		return nil, "", fmt.Errorf("run not found: %s", runID)
	case 1:
		st, err := Load(filepath.Dir(matches[0]))
		return st, matches[0], err
	default:
		return nil, "", fmt.Errorf("run %s is in more than one repository, so it is ambiguous; these state files match: %s", ShowRunID(runID), strings.Join(matches, ", "))
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

// LatestRun returns the newest run directory of a repo. See runDirsNewestFirst
// for the order.
func LatestRun(home, repoName string) (string, error) {
	dirs := runDirsNewestFirst(home, repoName)
	if len(dirs) == 0 {
		return "", fmt.Errorf("flow: no runs in %s", filepath.Join(home, ".tyci", "runs", repoName))
	}
	return dirs[0], nil
}

// newerRunFirst orders run dir names newest first. Run numbers compare by value
// and come before old ids, which are timestamps (YYYYMMDD-HHMMSS-<issue>) and
// compare as strings.
func newerRunFirst(a, b string) int {
	na, aNum := runNumber(a)
	nb, bNum := runNumber(b)
	switch {
	case aNum && bNum:
		return cmp.Compare(nb, na)
	case aNum != bNum:
		if aNum {
			return -1
		}
		return 1
	default:
		return cmp.Compare(b, a)
	}
}

// runNumber returns the number of a run number name, such as "17". ok is false
// for an old id.
func runNumber(name string) (n int, ok bool) {
	if !runNumberPattern.MatchString(name) {
		return 0, false
	}
	n, err := strconv.Atoi(name)
	return n, err == nil
}

// runDirsNewestFirst returns the run directories of a repo, newest first: run
// numbers by value, then old ids by their timestamp. It adds the runs of the
// repo in the legacy runs dir (before #846), and the legacy dirs whose state
// cannot be read. A missing repo directory gives no dirs.
func runDirsNewestFirst(home, repoName string) []string {
	runs := filepath.Join(home, ".tyci", "runs")
	var dirs []string
	add := func(base string, keep func(dir string) bool) {
		entries, err := os.ReadDir(base)
		if err != nil {
			return
		}
		for _, e := range entries {
			if dir := filepath.Join(base, e.Name()); e.IsDir() && keep(dir) {
				dirs = append(dirs, dir)
			}
		}
	}
	add(filepath.Join(runs, repoName), func(string) bool { return true })
	if legacy, repo, ok := legacyKey(repoName); ok {
		// An unreadable state may be a run of the repo: keep it, so the
		// callers skip or report it as they do in the repo's own dir.
		add(filepath.Join(runs, legacy), func(dir string) bool {
			st, err := Load(dir)
			if err != nil {
				return !errors.Is(err, fs.ErrNotExist)
			}
			return legacyRunOf(st, repo)
		})
	}
	slices.SortFunc(dirs, func(a, b string) int { return newerRunFirst(filepath.Base(a), filepath.Base(b)) })
	return dirs
}

// loadOf is Load for a dir of runDirsNewestFirst: a state without a repo gets
// the repo of repoName.
func loadOf(dir, repoName string) (*RunState, error) {
	st, err := Load(dir)
	if err == nil && st.Repo == "" {
		st.Repo = strings.ReplaceAll(repoName, "+", "/")
	}
	return st, err
}

// RecentRuns returns the saved states of the n newest runs of a repo, newest
// first. Runs with an unreadable state.json are skipped.
func RecentRuns(home, repoName string, n int) []*RunState {
	var out []*RunState
	for _, dir := range runDirsNewestFirst(home, repoName) {
		if len(out) == n {
			break
		}
		if st, err := loadOf(dir, repoName); err == nil {
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
	var all []*RunState
	for _, dir := range runDirsNewestFirst(home, repoName) {
		st, err := loadOf(dir, repoName)
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
		out.Runs = append(out.Runs, RunRow{Run: ShowRunID(st.Run), Workflow: st.Workflow, Params: st.Params,
			Status: st.Status, State: st.Current, StartedAt: st.StartedAt})
	}
	return out, nil
}

// RunByID returns the state of the run runID of repository repoName. ok is
// false when runID is not a run id, when that run has no state file, or when
// the run path is not a directory. err is set when the state file exists but
// cannot be read.
func RunByID(home, repoName, runID string) (st *RunState, ok bool, err error) {
	id, ok := ParseRunID(runID)
	if !ok {
		return nil, false, nil
	}
	st, err = loadOf(RunDir(home, repoName, id), repoName)
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
	if _, err := os.ReadDir(base); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, false, fmt.Errorf("flow: read runs of %s: %w", repoName, err)
	}
	var unreadable error
	for _, dir := range runDirsNewestFirst(home, repoName) {
		run, err := loadOf(dir, repoName)
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
