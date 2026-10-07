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

// Manager starts and resumes runs in goroutines. Several runs may be active, but
// only one per issue.
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
	// Text runs a one-agent workflow on a text input and returns the final agent
	// text (production: the roadmap workflow). It creates no run state. Optional.
	Text func(ctx context.Context, info RepoInfo, wf *Workflow, input string) (string, error)

	mu     sync.Mutex
	base   context.Context
	active map[string]activeRun
	subs   map[int]func(RunEvent)
	nsub   int
}

// activeRun is a run with a goroutine in this process.
type activeRun struct {
	cancel context.CancelFunc
	issue  int
}

// RunEvent tells a subscriber that a run started again (Status running, after
// Resume) or stopped (done, failed or paused).
type RunEvent struct {
	Run    string
	Status string
	PR     int
	Reason string
}

// Subscribe registers fn for the run events. fn runs on the run goroutine and must
// not block or call the Manager. The returned function removes it.
func (m *Manager) Subscribe(fn func(RunEvent)) func() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subs == nil {
		m.subs = map[int]func(RunEvent){}
	}
	m.nsub++
	id := m.nsub
	m.subs[id] = fn
	return func() {
		m.mu.Lock()
		delete(m.subs, id)
		m.mu.Unlock()
	}
}

func (m *Manager) emit(ev RunEvent) {
	m.mu.Lock()
	fns := make([]func(RunEvent), 0, len(m.subs))
	for _, fn := range m.subs {
		fns = append(fns, fn)
	}
	m.mu.Unlock()
	for _, fn := range fns {
		fn(ev)
	}
}

// RunText runs the one-agent workflow on input and returns the final agent text.
// It blocks until the agent ends or ctx is done.
func (m *Manager) RunText(ctx context.Context, workflow, input string) (string, error) {
	if m.Text == nil {
		return "", errors.New("text runs are not supported")
	}
	info, err := m.Info()
	if err != nil {
		return "", err
	}
	wf, err := m.Workflow(info, workflow)
	if err != nil {
		return "", err
	}
	return m.Text(ctx, info, wf, input)
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
	m.launch(info, wf, st, false, func(ctx context.Context, r *Runner) error { return r.Run(ctx, st) })
	return st.Run, warnings, nil
}

// ErrBusy is returned (wrapped) when a run is already active.
var ErrBusy = errors.New("manager busy")

// refuse returns an error when the issue has an active or paused run. A running
// state without an active goroutine is stale and does not block. m.mu must be held.
func (m *Manager) refuse(info RepoInfo, issue int) error {
	for id, a := range m.active {
		if a.issue == issue {
			return fmt.Errorf("%w: run %s is active for issue %d", ErrBusy, id, issue)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(RunDir(info.Home, info.Name(), "x")))
	for _, e := range entries {
		st, err := Load(RunDir(info.Home, info.Name(), e.Name()))
		if err != nil || st.Issue != issue {
			continue
		}
		// "running" here is stale: the loop above found no active run of this
		// issue, so no goroutine owns it. Only a paused run blocks.
		if st.Status == "paused" {
			return fmt.Errorf("issue %d already has run %s (%s)", issue, st.Run, st.Status)
		}
	}
	return nil
}

// launch registers the run as active and runs it. m.mu must be held.
func (m *Manager) launch(info RepoInfo, wf *Workflow, st *RunState, resumed bool, do func(context.Context, *Runner) error) {
	parent := m.base
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	if m.active == nil {
		m.active = map[string]activeRun{}
	}
	m.active[st.Run] = activeRun{cancel: cancel, issue: st.Issue}
	r := m.NewRunner(info, wf, st)
	if r.Warn == nil {
		r.Warn = m.Notify
	}
	go func() {
		defer func() {
			cancel()
			m.mu.Lock()
			delete(m.active, st.Run)
			m.mu.Unlock()
			m.emit(RunEvent{Run: st.Run, Status: st.Status, PR: st.PR, Reason: st.Reason})
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
		if resumed {
			m.emit(RunEvent{Run: st.Run, Status: "running"})
		}
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
	if _, ok := m.active[st.Run]; ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: run %s is active", ErrBusy, st.Run)
	}
	m.launch(info, wf, st, true, func(ctx context.Context, r *Runner) error { return r.Resume(ctx, st, answer) })
	m.mu.Unlock()
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

// RunView is a saved run with the role of the agent in its current state.
type RunView struct {
	State *RunState
	Role  string
}

// Recent returns the n newest runs of the current repo, newest first.
func (m *Manager) Recent(n int) []RunView {
	info, err := m.Info()
	if err != nil {
		return nil
	}
	var out []RunView
	wfs := map[string]*Workflow{}
	for _, st := range RecentRuns(info.Home, info.Name(), n) {
		v := RunView{State: st}
		if st.Status == "running" {
			wf, ok := wfs[st.Workflow]
			if !ok {
				wf, _ = m.Workflow(info, st.Workflow)
				wfs[st.Workflow] = wf
			}
			if wf != nil {
				v.Role = wf.States[st.Current].Agent
			}
		}
		out = append(out, v)
	}
	return out
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
	for _, a := range m.active {
		a.cancel()
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
