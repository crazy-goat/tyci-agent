package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// maxResumes is how many times a run is resumed after a restart before it
// pauses for a human. It is not configurable.
const maxResumes = 3

// RunsDir returns <home>/.tyci/runs.
func RunsDir(home string) string {
	return filepath.Join(home, ".tyci", "runs")
}

// ScanResumable returns the runs of repo with status running whose owner
// process is gone, oldest first.
func ScanResumable(runsDir, repo string) ([]*RunState, error) {
	return scanRuns(runsDir, repo, ownerGone)
}

// ScanAsk returns the runs of repo with status paused (they stay paused), oldest first.
func ScanAsk(runsDir, repo string) ([]*RunState, error) {
	return scanRuns(runsDir, repo, func(st *RunState) bool { return st.Status == "paused" })
}

// scanRuns reads <runsDir>/*/*/state.json and keeps the readable states of repo
// that keep accepts.
func scanRuns(runsDir, repo string, keep func(*RunState) bool) ([]*RunState, error) {
	paths, err := filepath.Glob(filepath.Join(runsDir, "*", "*", stateFile))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []*RunState
	for _, p := range paths {
		st, err := Load(filepath.Dir(p))
		if err != nil || st.Repo != repo || !keep(st) {
			continue
		}
		out = append(out, st)
	}
	return out, nil
}

// ownerGone reports whether st is running and its owner process is dead. A live
// PID means another tyci owns the run. EPERM means the process exists. A state
// without a PID (written before the field existed) has no owner.
func ownerGone(st *RunState) bool {
	if st.Status != "running" {
		return false
	}
	if st.PID <= 0 {
		return true
	}
	return errors.Is(syscall.Kill(st.PID, 0), syscall.ESRCH)
}

// claim writes self as the owner of the run in dir, counts the resume and adds
// a "resumed" history step.
func claim(dir string, st *RunState, self int) error {
	now := time.Now()
	st.PID = self
	st.Resumed++
	st.History = append(st.History, Step{
		Seq:       len(st.History) + 1,
		State:     st.Current,
		Kind:      "resume",
		Key:       "resumed",
		To:        st.Current,
		StartedAt: now,
		EndedAt:   now,
	})
	return (&Store{Dir: dir}).Save(st)
}

// owns re-reads the state in dir and reports whether self still owns the run.
// When two instances claim at the same time, the last writer wins.
func owns(dir string, self int) bool {
	st, err := Load(dir)
	return err == nil && st.PID == self
}

// resumeRun continues a run whose owner process is gone at its saved state,
// without a new visit. After maxResumes resumes, or when the worktree is gone,
// it pauses the run instead. It returns false when the run did not start: it
// is paused now, or another instance claimed it. m.mu must be held.
func (m *Manager) resumeRun(info RepoInfo, st *RunState) (bool, error) {
	dir := RunDir(info.Home, info.Name(), st.Run)
	wf, err := m.Workflow(info, st.Workflow)
	if err != nil {
		return false, err
	}
	if st.Resumed >= maxResumes {
		m.pauseStale(dir, wf, st, fmt.Sprintf("resumed %d times, please check", maxResumes), true)
		return false, nil
	}
	self := os.Getpid()
	if err := claim(dir, st, self); err != nil {
		return false, err
	}
	if !owns(dir, self) {
		return false, nil
	}
	if _, err := os.Stat(st.Worktree); err != nil {
		m.pauseStale(dir, wf, st, "worktree "+st.Worktree+" is missing", true)
		return false, nil
	}
	m.launch(info, wf, st, false, func(ctx context.Context, r *Runner) error { return r.Continue(ctx, st) })
	return true, nil
}

// pauseStale pauses a run that cannot be resumed in the "ask" state of its
// workflow; without an ask state the run fails. Reason names the saved state,
// so the user can answer "goto <state>".
// With notify false it sends no notice for a paused run; a failed run always
// gets its notice.
func (m *Manager) pauseStale(dir string, wf *Workflow, st *RunState, msg string, notify bool) {
	r := &Runner{WF: wf, Store: &Store{Dir: dir}}
	if s, ok := wf.States["ask"]; ok && s.Ask != "" {
		reason := "resume:" + st.Current
		st.Current = "ask"
		_ = r.pause(st, msg, reason)
	} else {
		_ = r.fail(context.Background(), st, msg, nil)
		// A failed run is not in the start-up question: always tell the user.
		notify = true
	}
	if notify {
		m.notify(st, wf)
	}
}

// AskUnfinished is the start-up routine. It resumes nothing. It pauses every
// running run of the current repo whose owner process is gone (in the "ask"
// state, reason "resume:<state>"), then sends one notice that lists all paused
// runs and asks the user what to do: resume, stop or leave paused. A paused run
// blocks a new run of its issue, so nothing starts for it before the answer.
// With no unfinished runs it sends nothing.
func (m *Manager) AskUnfinished() {
	info, err := m.Info()
	if err != nil {
		return
	}
	runs := RunsDir(info.Home)
	var notes []string
	stale, _ := ScanResumable(runs, info.Repo)
	for _, st := range stale {
		wf, err := m.Workflow(info, st.Workflow)
		if err != nil {
			notes = append(notes, "cannot pause run "+st.Run+": "+err.Error())
			continue
		}
		m.pauseStale(RunDir(info.Home, info.Name(), st.Run), wf, st, "tyci restarted", false)
	}
	paused, _ := ScanAsk(runs, info.Repo)
	if len(paused) > 0 {
		notes = append(notes, m.startupQuestion(info, paused))
	}
	if m.Notify != nil {
		for _, s := range notes {
			m.Notify(s)
		}
	}
}

// startupQuestion is the one start-up notice for the paused runs. It is
// written for the chat model: it says what to ask and which tool to call.
func (m *Manager) startupQuestion(info RepoInfo, paused []*RunState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tyci started. %d workflow run(s) of %s are not finished and are paused. Nothing resumes until the user answers.\n", len(paused), info.Repo)
	for _, st := range paused {
		fmt.Fprintf(&b, "- run %s, issue #%d", st.Run, st.Issue)
		if n := len(st.History); n > 0 {
			h := st.History[n-1]
			fmt.Fprintf(&b, ", last step %s -> %s", h.State, h.Key)
		}
		if st.PR > 0 {
			fmt.Fprintf(&b, ", PR #%d", st.PR)
		}
		answers := []string{}
		if resumeState(st) != "" {
			answers = append(answers, "resume")
		}
		wf, _ := m.Workflow(info, st.Workflow)
		answers = append(answers, answerKeys(wf, st.Current)...)
		if st.Ask != nil && st.Ask.Message != "" {
			b.WriteString(", reason: " + st.Ask.Message)
		}
		b.WriteString("; answers: " + strings.Join(answers, ", ") + "\n")
	}
	b.WriteString("Ask the user now, in one message: resume, stop or leave paused, for each run or for all. " +
		"Then call workflow_resume(run, answer) once for each run the user chose (\"resume\" or \"stop\"). " +
		"Do not call workflow_resume before the user answers. Without an answer the runs stay paused.")
	return b.String()
}

// resumeState returns the saved state of a run that AskUnfinished (or the
// resume limit) paused, or "" for any other pause.
func resumeState(st *RunState) string {
	if st.Ask == nil {
		return ""
	}
	s, _ := strings.CutPrefix(st.Ask.Reason, "resume:")
	if s == st.Ask.Reason {
		return ""
	}
	return s
}

// markAdoptable lets the next Start of the run's issue return the run, so the
// orchestrator watches it and counts it as a worker. Resume calls it for a
// "resume" answer, unless an Adopt returned the run already. m.mu must be held.
func (m *Manager) markAdoptable(run string) {
	if m.adopted[run] {
		return
	}
	if a, ok := m.active[run]; ok {
		a.adoptable = true
		m.active[run] = a
	}
}

// adopt returns an active run of the issue resumed with the answer "resume" and not
// returned by Start yet. m.mu must be held.
func (m *Manager) adopt(issue int) (string, bool) {
	for id, a := range m.active {
		if a.issue == issue && a.adoptable {
			a.adoptable = false
			m.active[id] = a
			if m.adopted == nil {
				m.adopted = map[string]bool{}
			}
			m.adopted[id] = true
			return id, true
		}
	}
	return "", false
}

// pausedRuns returns the paused runs of the current repo that have no
// goroutine and that no Adopt returned yet. m.mu must be held.
func (m *Manager) pausedRuns() []*RunState {
	info, err := m.Info()
	if err != nil {
		return nil
	}
	paused, _ := ScanAsk(RunsDir(info.Home), info.Repo)
	var out []*RunState
	for _, st := range paused {
		if _, ok := m.active[st.Run]; !ok && !m.adopted[st.Run] {
			out = append(out, st)
		}
	}
	return out
}

// Adoptable returns the issues of the runs that the orchestrator must watch
// before it starts new runs: runs resumed with the answer "resume" and paused
// runs (for example the runs that AskUnfinished paused). No Start or Adopt
// returned them yet. The issues are sorted.
func (m *Manager) Adoptable() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []int
	for _, a := range m.active {
		if a.adoptable {
			out = append(out, a.issue)
		}
	}
	for _, st := range m.pausedRuns() {
		out = append(out, st.Issue)
	}
	sort.Ints(out)
	return out
}

// Adopt returns the run of the issue resumed with the answer "resume", or else
// its paused run, once. paused is true for a paused run: it waits for the
// user's answer. Unlike Start it never starts a run: ok is false when there is
// no such run (for example, it ended already). An answer to an adopted paused
// run continues it as usual, and the caller sees its events.
func (m *Manager) Adopt(issue int) (id string, paused, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.adopt(issue); ok {
		return id, false, true
	}
	for _, st := range m.pausedRuns() {
		if st.Issue == issue {
			if m.adopted == nil {
				m.adopted = map[string]bool{}
			}
			m.adopted[st.Run] = true
			return st.Run, true, true
		}
	}
	return "", false, false
}

// resumeStale resumes a run of the issue whose owner process is gone. found is
// false when there is no such run. m.mu must be held.
func (m *Manager) resumeStale(info RepoInfo, issue int) (run string, found bool, err error) {
	stale, _ := ScanResumable(RunsDir(info.Home), info.Repo)
	for _, st := range stale {
		if st.Issue != issue {
			continue
		}
		ok, err := m.resumeRun(info, st)
		if err != nil {
			return "", true, err
		}
		if !ok {
			return "", true, fmt.Errorf("run %s of issue %d was not resumed (%s)", st.Run, issue, st.Status)
		}
		return st.Run, true, nil
	}
	return "", false, nil
}
