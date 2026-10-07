package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultWorkflow is the workflow a start request uses when none is named.
const DefaultWorkflow = "issue-to-merge"

var runIDPattern = regexp.MustCompile(`^\d{8}-\d{6}-\d+$`)

// RepoInfo describes the repository a run works in.
type RepoInfo struct {
	Home          string // user home; runs live under <Home>/.tyci/runs
	Root          string // repository root
	Repo          string // owner/name
	DefaultBranch string
	Trusted       bool
}

// Name returns the repository name without the owner.
func (i RepoInfo) Name() string {
	if _, n, ok := strings.Cut(i.Repo, "/"); ok {
		return n
	}
	return i.Repo
}

// StartRequest says which workflow to start.
type StartRequest struct {
	Workflow string
	Issue    int
}

// Manager starts and resumes runs in goroutines. v0.3.0 allows ONE active run.
type Manager struct {
	// Info detects the repository. Called for every request.
	Info func() (RepoInfo, error)
	// Prepare validates the workflow and creates the worktree and run state
	// (production: PrepareRun). On error nothing is created.
	Prepare func(ctx context.Context, info RepoInfo, req StartRequest) (*RunState, *Workflow, []string, error)
	// Workflow loads a workflow by name, for Resume.
	Workflow func(info RepoInfo, name string) (*Workflow, error)
	// NewRunner builds the runner of one run.
	NewRunner func(info RepoInfo, wf *Workflow, st *RunState) *Runner
	// Notify receives the one-line notices. Optional.
	Notify func(string)

	mu     sync.Mutex
	base   context.Context
	active map[string]context.CancelFunc
}

// SetBase sets the context that cancels all runs (TUI quit).
func (m *Manager) SetBase(ctx context.Context) {
	m.mu.Lock()
	m.base = ctx
	m.mu.Unlock()
}

// Start prepares a run and starts it in a goroutine. It returns at once.
func (m *Manager) Start(ctx context.Context, req StartRequest) (string, []string, error) {
	if req.Workflow == "" {
		req.Workflow = DefaultWorkflow
	}
	if req.Issue <= 0 {
		return "", nil, fmt.Errorf("issue must be a positive number, got %d", req.Issue)
	}
	info, err := m.Info()
	if err != nil {
		return "", nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.refuse(info, req.Issue); err != nil {
		return "", nil, err
	}
	st, wf, warnings, err := m.Prepare(ctx, info, req)
	if err != nil {
		return "", warnings, err
	}
	m.launch(info, wf, st, func(ctx context.Context, r *Runner) error { return r.Run(ctx, st) })
	return st.Run, warnings, nil
}

// refuse returns an error when a run is active or the issue has a running or
// paused run. A running state without an active goroutine is stale and does
// not block. m.mu must be held.
func (m *Manager) refuse(info RepoInfo, issue int) error {
	for id := range m.active {
		return fmt.Errorf("run %s is active", id)
	}
	entries, _ := os.ReadDir(filepath.Dir(RunDir(info.Home, info.Name(), "x")))
	for _, e := range entries {
		st, err := Load(RunDir(info.Home, info.Name(), e.Name()))
		if err != nil || st.Issue != issue {
			continue
		}
		// "running" here is stale: refuse returned above when a run is active in
		// this process, so no goroutine owns it. Only a paused run blocks.
		if st.Status == "paused" {
			return fmt.Errorf("issue %d already has run %s (%s)", issue, st.Run, st.Status)
		}
	}
	return nil
}

// launch registers the run as active and runs it. m.mu must be held.
func (m *Manager) launch(info RepoInfo, wf *Workflow, st *RunState, do func(context.Context, *Runner) error) {
	parent := m.base
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	if m.active == nil {
		m.active = map[string]context.CancelFunc{}
	}
	m.active[st.Run] = cancel
	r := m.NewRunner(info, wf, st)
	go func() {
		defer func() {
			cancel()
			m.mu.Lock()
			delete(m.active, st.Run)
			m.mu.Unlock()
		}()
		defer func() {
			if p := recover(); p != nil {
				st.Status = "failed"
				st.Reason = fmt.Sprintf("panic: %v", p)
				if r.Store != nil {
					_ = r.Store.Save(st)
				}
				m.notify(st, wf)
			}
		}()
		err := do(ctx, r)
		if err != nil && st.Status == "running" {
			st.Status = "failed"
			st.Reason = err.Error()
			if r.Store != nil {
				_ = r.Store.Save(st)
			}
		}
		m.notify(st, wf)
	}()
}

// notify sends the one-line notice for the final or paused state.
func (m *Manager) notify(st *RunState, wf *Workflow) {
	if m.Notify == nil {
		return
	}
	text := "workflow run " + st.Run
	switch st.Status {
	case "done":
		text += " done"
		if st.PR > 0 {
			text += ": merged " + prURL(st)
		}
	case "paused":
		msg := ""
		if st.Ask != nil {
			msg = st.Ask.Message
		}
		text += " paused: " + msg
		if keys := answerKeys(wf, st.Current); len(keys) > 0 {
			text += " (answer with workflow_resume: " + strings.Join(keys, "|") + ")"
		}
	default:
		text += " " + st.Status
		if st.Reason != "" {
			text += ": " + st.Reason
		}
	}
	m.Notify(text)
}

func prURL(st *RunState) string {
	return fmt.Sprintf("https://github.com/%s/pull/%d", st.Repo, st.PR)
}

// answerKeys returns the sorted keys of the on map of an ask state.
func answerKeys(wf *Workflow, state string) []string {
	if wf == nil {
		return nil
	}
	var keys []string
	for k := range wf.States[state].On {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Resume answers a paused run. A bad answer returns an error that lists the
// allowed keys; the run stays paused.
func (m *Manager) Resume(runID, answer string) error {
	info, err := m.Info()
	if err != nil {
		return err
	}
	st, err := loadRun(info, runID)
	if err != nil {
		return err
	}
	if st.Status != "paused" {
		return fmt.Errorf("run %s is %s, not paused", st.Run, st.Status)
	}
	wf, err := m.Workflow(info, st.Workflow)
	if err != nil {
		return err
	}
	s := wf.States[st.Current]
	if _, ok := s.On[answer]; !ok {
		if _, ok := s.On["*"]; !ok && len(s.On) > 0 {
			return fmt.Errorf("unknown answer %q, allowed: %s", answer, strings.Join(answerKeys(wf, st.Current), ", "))
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.active {
		return fmt.Errorf("run %s is active", id)
	}
	m.launch(info, wf, st, func(ctx context.Context, r *Runner) error { return r.Resume(ctx, st, answer) })
	return nil
}

// Status returns the saved state of a run; an empty id means the newest run.
func (m *Manager) Status(runID string) (*RunState, error) {
	info, err := m.Info()
	if err != nil {
		return nil, err
	}
	return loadRun(info, runID)
}

func loadRun(info RepoInfo, runID string) (*RunState, error) {
	if runID == "" {
		dir, err := LatestRun(info.Home, info.Name())
		if err != nil {
			return nil, err
		}
		return Load(dir)
	}
	if !runIDPattern.MatchString(runID) {
		return nil, fmt.Errorf("bad run id %q", runID)
	}
	st, err := Load(RunDir(info.Home, info.Name(), runID))
	if err != nil {
		return nil, errors.New("run " + runID + " not found")
	}
	return st, nil
}

// Shutdown cancels every active run (state failed, reason cancelled) and
// waits up to wait for the goroutines to save it.
func (m *Manager) Shutdown(wait time.Duration) {
	m.mu.Lock()
	for _, cancel := range m.active {
		cancel()
	}
	m.mu.Unlock()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		n := len(m.active)
		m.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
