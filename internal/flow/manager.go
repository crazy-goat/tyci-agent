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

// preparingPrefix starts the m.active key of an issue whose run is being
// prepared. The rest of the key is the issue number, not a run ID.
const preparingPrefix = "preparing:"

var runIDPattern = regexp.MustCompile(`^\d{8}-\d{6}-\d+(-[0-9a-f]{6})?$`)

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

// StartRequest says which workflow to start and its positional param values.
type StartRequest struct {
	Workflow string
	Params   []string
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

	mu   sync.Mutex
	base context.Context
	// nprep numbers the preparations of runs without an issue (their active keys).
	nprep  int
	active map[string]activeRun
	subs   map[int]func(RunEvent)
	nsub   int
	// adopted: runs that Adopt or Start returned to the orchestrator.
	adopted map[string]bool
	// workers: the limit of active runs for a "resume": the answer resume of a
	// start-up pause, and a workflow_start that resumes a stale run. 0 = unlimited.
	workers int
}

// activeRun is a run with a goroutine in this process.
type activeRun struct {
	cancel context.CancelFunc
	issue  int
	// adoptable: resumed with the answer "resume"; the next Start of the issue returns it.
	adoptable bool
	// stopped and stopReason are set by Stop before cancel. The run goroutine reads them.
	stopped    bool
	stopReason string
	// done is closed when the run goroutine ends. It is nil for a preparing entry
	// and for a run that Resume runs in the caller's goroutine.
	done chan struct{}
	// st is the run state of the goroutine. It is valid after done is closed.
	st *RunState
}

// DefaultStopReason is the reason of a run stopped without a reason.
const DefaultStopReason = "stopped by user"

// RunEvent tells a subscriber that a run started again (Status running, after
// Resume) or stopped (done, failed, paused or stopped).
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

// SetWorkers sets the limit of active runs: a "resume" (the answer resume of a
// start-up pause, or a workflow_start that resumes a stale run) is refused while
// that many runs are active (production: orchestrator.workers). 0 means unlimited.
func (m *Manager) SetWorkers(n int) {
	m.mu.Lock()
	m.workers = n
	m.mu.Unlock()
}

// StartIssue starts workflow for issue. The issue goes to the param named "issue",
// so the workflow must declare it (see IssueArgs). Use it for callers that only
// have an issue number: the CLI and the orchestrator.
func (m *Manager) StartIssue(ctx context.Context, workflow string, issue int) (string, []string, error) {
	if workflow == "" {
		return "", nil, errors.New("workflow name is required: start a workflow by name, or create one with tyci workflow init")
	}
	info, err := m.Info()
	if err != nil {
		return "", nil, err
	}
	def, err := m.Workflow(info, workflow)
	if err != nil {
		return "", nil, err
	}
	args, err := IssueArgs(def, issue)
	if err != nil {
		return "", nil, err
	}
	return m.Start(ctx, StartRequest{Workflow: workflow, Params: args})
}

// Start prepares a run and starts it in a goroutine. The param "issue" of the
// workflow, when it has one, is the issue of the run. A run of that issue resumed
// with the answer "resume" is returned instead of a new one, and a run of that
// issue whose owner process is gone is resumed. A run without an issue is always
// new.
//
// Prepare runs the repository's setup script, which can take minutes. Start
// does not hold m.mu meanwhile. The run is reserved in m.active while it is
// prepared, so Shutdown cancels the preparation like an active run.
func (m *Manager) Start(ctx context.Context, req StartRequest) (string, []string, error) {
	if req.Workflow == "" {
		return "", nil, errors.New("workflow name is required: start a workflow by name, or create one with tyci workflow init")
	}
	info, err := m.Info()
	if err != nil {
		return "", nil, err
	}
	def, err := m.Workflow(info, req.Workflow)
	if err != nil {
		return "", nil, err
	}
	params, err := BindParams(def, req.Params)
	if err != nil {
		return "", nil, err
	}
	issue := issueOf(params)
	prepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.mu.Lock()
	if issue > 0 {
		if id, ok := m.adopt(issue); ok {
			m.mu.Unlock()
			return id, nil, nil
		}
		if err := m.refuse(info, issue); err != nil {
			m.mu.Unlock()
			return "", nil, err
		}
		if id, found, err := m.resumeStale(info, issue); found {
			m.mu.Unlock()
			return id, nil, err
		}
	}
	key := fmt.Sprintf("%s%d", preparingPrefix, issue)
	if issue == 0 {
		m.nprep++
		key = fmt.Sprintf("%s0.%d", preparingPrefix, m.nprep)
	}
	if m.active == nil {
		m.active = map[string]activeRun{}
	}
	m.active[key] = activeRun{cancel: cancel, issue: issue}
	m.mu.Unlock()

	st, wf, warnings, err := m.Prepare(prepCtx, info, req)

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.active, key)
	if err != nil {
		return "", warnings, err
	}
	m.launch(info, wf, st, false, func(ctx context.Context, r *Runner) error { return r.Run(ctx, st) })
	return st.Run, warnings, nil
}

// ErrBusy is returned (wrapped) when a run is already active.
var ErrBusy = errors.New("manager busy")

// refuse returns an ErrBusy error when the issue has an active, preparing or paused
// run, or a running run owned by another live process (another tyci). A running state
// with a dead owner is stale and does not block: resumeStale takes it. m.mu must be held.
func (m *Manager) refuse(info RepoInfo, issue int) error {
	for id, a := range m.active {
		if a.issue != issue {
			continue
		}
		if strings.HasPrefix(id, preparingPrefix) {
			return fmt.Errorf("%w: issue %d is being prepared", ErrBusy, issue)
		}
		return fmt.Errorf("%w: run %s is active for issue %d", ErrBusy, id, issue)
	}
	entries, _ := os.ReadDir(filepath.Dir(RunDir(info.Home, info.Name(), "x")))
	for _, e := range entries {
		st, err := Load(RunDir(info.Home, info.Name(), e.Name()))
		if err != nil || st.Issue != issue {
			continue
		}
		owned := st.Status == "running" && st.PID != os.Getpid() && !ownerGone(st)
		if st.Status == "paused" || owned {
			return fmt.Errorf("%w: issue %d already has run %s (%s)", ErrBusy, issue, st.Run, st.Status)
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
	done := make(chan struct{})
	m.active[st.Run] = activeRun{cancel: cancel, issue: st.Issue, done: done, st: st}
	r := m.NewRunner(info, wf, st)
	if r.Warn == nil {
		r.Warn = m.Notify
	}
	go func() {
		defer func() {
			cancel()
			m.mu.Lock()
			delete(m.active, st.Run)
			// A paused run stays adopted: the orchestrator still watches it.
			if st.Status != "paused" {
				delete(m.adopted, st.Run)
			}
			m.mu.Unlock()
			close(done)
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
		m.mu.Lock()
		a := m.active[st.Run]
		m.mu.Unlock()
		switch {
		case a.stopped && st.Status != "done":
			// The runner already saved the cancel as failed. A user stop wins.
			st.Status = StatusStopped
			st.Reason = a.stopReason
			st.UpdatedAt = time.Now()
			if r.Store != nil {
				_ = r.Store.Save(st)
			}
		case err != nil && st.Status == "running":
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
		switch {
		case WasMerged(st):
			text += " done: merged " + prURL(st)
		case st.PR > 0:
			text += " stopped: PR " + prURL(st) + " is still open"
		case !ranAgent(st) && !endedByAsk(st):
			text += " skipped"
		default:
			text += " stopped: no PR"
		}
	case StatusStopped:
		if st.PR > 0 {
			text += " stopped: PR " + prURL(st) + " is still open"
		} else {
			text += " stopped: no PR"
		}
	case "paused":
		msg := ""
		if st.Ask != nil {
			msg = st.Ask.Message
		}
		text += " paused: " + msg + " (needs a human"
		if keys := answerKeys(wf, st.Current); len(keys) > 0 {
			if st.Ask != nil && st.Ask.Proposal != "" {
				keys = append([]string{"apply", "reject"}, keys...)
			}
			text += "; answer with workflow_resume: " + strings.Join(keys, "|") + "|retry <note>|goto <state>"
		}
		text += ")"
	default:
		text += " " + st.Status
		if st.Reason != "" {
			text += ": " + st.Reason
		}
		if st.Current != "" {
			text += " (step " + st.Current + ")"
		}
	}
	m.Notify(text)
}

// endedByAsk reports whether the last history step is an answered ask.
func endedByAsk(st *RunState) bool {
	n := len(st.History)
	return n > 0 && st.History[n-1].Kind == "ask"
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
	if (answer == "apply" || answer == "reject") && st.Ask != nil && st.Ask.Proposal != "" {
		return m.answerProposal(info, wf, st, answer)
	}
	// "resume" continues a run paused at start-up at its saved state.
	saved := resumeState(st)
	if strings.TrimSpace(answer) == "resume" && saved != "" {
		answer = "goto " + saved
	}
	s := wf.States[st.Current]
	key, isGoto := answer, false
	word, rest, _ := strings.Cut(strings.TrimSpace(answer), " ")
	rest = strings.TrimSpace(rest)
	switch {
	case word == "goto" && rest != "":
		if err := checkGoto(wf, rest); err != nil {
			return err
		}
		isGoto = true
	case word == "retry" && rest != "":
		key = word
	}
	if _, ok := s.On[key]; !ok && !isGoto {
		if _, ok := s.On["*"]; !ok && len(s.On) > 0 {
			return fmt.Errorf("unknown answer %q, allowed: %s", answer, strings.Join(answerKeys(wf, st.Current), ", "))
		}
	}
	m.mu.Lock()
	if _, ok := m.active[st.Run]; ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: run %s is active", ErrBusy, st.Run)
	}
	// Every run in m.active takes a worker slot. An apply or reject of a workflow
	// proposal is one too: answerProposal keeps its run in m.active.
	startup := saved != "" && answer == "goto "+saved
	if startup && m.workers > 0 && len(m.active) >= m.workers {
		m.mu.Unlock()
		return fmt.Errorf("run %s of issue %d is not resumed: orchestrator.workers is %d and %d run(s) are active (an apply or reject of a workflow proposal counts as an active run); answer resume again when one ends",
			st.Run, st.Issue, m.workers, len(m.active))
	}
	m.launch(info, wf, st, true, func(ctx context.Context, r *Runner) error { return r.Resume(ctx, st, answer) })
	if startup {
		m.markAdoptable(st.Run)
	}
	m.mu.Unlock()
	return nil
}

// answerProposal applies or rejects the workflow proposal of a paused run.
// The run stays paused and waits for its normal answer; a new notice says so.
func (m *Manager) answerProposal(info RepoInfo, wf *Workflow, st *RunState, answer string) error {
	if answer == "apply" && wf.Source != "" && !strings.HasPrefix(wf.Source, info.Root+string(filepath.Separator)) {
		return fmt.Errorf("the run uses %s, outside the repository: a proposal can change only the workflow files in the repository's .tyci/workflows/", wf.Source)
	}
	// Reserve the run, so no other answer runs or saves it at the same time.
	parent := m.base
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	m.mu.Lock()
	if _, ok := m.active[st.Run]; ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: run %s is active", ErrBusy, st.Run)
	}
	if m.active == nil {
		m.active = map[string]activeRun{}
	}
	m.active[st.Run] = activeRun{cancel: cancel, issue: st.Issue}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.active, st.Run)
		m.mu.Unlock()
	}()
	// Another answer can have changed the run before the reservation.
	proposal := st.Ask.Proposal
	cur, err := loadRun(info, st.Run)
	if err != nil {
		return err
	}
	if cur.Status != "paused" || cur.Ask == nil || cur.Ask.Proposal != proposal {
		return fmt.Errorf("run %s changed, its proposal is no longer open", st.Run)
	}
	st = cur
	runDir := RunDir(info.Home, info.Name(), st.Run)
	note := " Workflow proposal rejected."
	if answer == "apply" {
		url, err := ApplyProposal(ctx, info, st, st.Ask.Proposal)
		if err != nil {
			return fmt.Errorf("apply the workflow proposal: %w", err)
		}
		note = " Workflow proposal applied: " + url
	} else if err := RejectProposal(runDir, st.Ask.Proposal); err != nil {
		return fmt.Errorf("reject the workflow proposal: %w", err)
	}
	st.Ask.Message, _, _ = strings.Cut(st.Ask.Message, proposalWaits)
	st.Ask.Message += note
	st.Ask.Proposal = ""
	st.UpdatedAt = time.Now()
	if err := (&Store{Dir: runDir}).Save(st); err != nil {
		return err
	}
	m.notify(st, wf)
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
	return m.RecentIn(info, n)
}

// RecentIn is Recent for a repo whose info the caller already has. It does not
// call m.Info, so a caller that polls can detect the repo once.
func (m *Manager) RecentIn(info RepoInfo, n int) []RunView {
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

// Shutdown cancels every active run and waits up to wait for the goroutines to
// save them. A run ends as failed with the reason cancelled. A run whose oracle
// answer was cancelled stays paused for a human.
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

// stopHint tells how to end a paused run, which Stop cannot end. The answers
// come from the workflow of the run, because not every workflow has a "stop"
// key. It returns "" when the run is not paused or cannot be read.
func (m *Manager) stopHint(run string) string {
	if run == "" {
		return ""
	}
	info, err := m.Info()
	if err != nil {
		return ""
	}
	st, err := loadRun(info, run)
	if err != nil || st.Status != "paused" {
		return ""
	}
	wf, err := m.Workflow(info, st.Workflow)
	if err != nil {
		return ""
	}
	keys := answerKeys(wf, st.Current)
	if len(keys) == 0 {
		return ""
	}
	return "; for a paused run use workflow_resume with one of the answers: " + strings.Join(keys, ", ")
}

// Stop cancels an active run and records it as stopped. The run keeps its
// worktree and its pull request. It returns the saved state. A stop of a run
// that ends by itself before Stop reads its state keeps the natural result.
func (m *Manager) Stop(run, reason string) (*RunState, error) {
	if reason == "" {
		reason = DefaultStopReason
	}
	m.mu.Lock()
	a, ok := m.active[run]
	if !ok || a.done == nil {
		var ids []string
		for id, other := range m.active {
			if other.done != nil {
				ids = append(ids, id)
			}
		}
		m.mu.Unlock()
		hint := m.stopHint(run)
		if len(ids) == 0 {
			return nil, errors.New("no active runs" + hint)
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("run %q is not active; active runs: %s%s", run, strings.Join(ids, ", "), hint)
	}
	a.stopped = true
	a.stopReason = reason
	m.active[run] = a
	m.mu.Unlock()
	a.cancel()
	select {
	case <-a.done:
		return a.st, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("run %s did not stop within 5s; it is saved as stopped when it ends", run)
	}
}
