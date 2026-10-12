package flow

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/agent"
)

// roleAgents is a scripted AgentRunner for replacement tests. Each role
// either finishes at once, blocks until its context ends (stuck), or blocks
// until its gate closes and ignores the context (hold).
type roleAgents struct {
	mu      sync.Mutex
	events  []string
	started chan string
	stuck   map[string]bool
	hold    map[string]chan struct{}
	// holdErr is returned by a held role when its gate closes (a cancelled agent).
	holdErr map[string]error
	keys    map[string]string
}

func newRoleAgents() *roleAgents {
	return &roleAgents{
		started: make(chan string, 32),
		stuck:   map[string]bool{},
		hold:    map[string]chan struct{}{},
		holdErr: map[string]error{},
		keys:    map[string]string{"worker": "done", "worker-critical": "done"},
	}
}

func (a *roleAgents) record(ev string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
}

func (a *roleAgents) log() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.events...)
}

func (a *roleAgents) Run(ctx context.Context, role, _ string, _ RunContext) (string, string, error) {
	a.record("start " + role)
	a.started <- role
	if gate, ok := a.hold[role]; ok {
		<-gate
		if err := a.holdErr[role]; err != nil {
			a.record("stop " + role)
			return "", "", err
		}
		a.record("finish " + role)
		return a.keys[role], "sess-" + role, nil
	}
	if a.stuck[role] {
		<-ctx.Done()
		a.record("stop " + role)
		return "", "", ctx.Err()
	}
	a.record("finish " + role)
	return a.keys[role], "sess-" + role, nil
}

// waitStart waits until role has started, or fails the test.
func waitStart(t *testing.T, a *roleAgents, role string) {
	t.Helper()
	select {
	case got := <-a.started:
		if got != role {
			t.Fatalf("started %q, want %q", got, role)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("agent %q did not start", role)
	}
}

// replaceWorkflow runs one agent state "code" that ends the run on "done".
func replaceWorkflow(maxVisits int) *Workflow {
	return &Workflow{Name: "demo", Start: "code", States: map[string]State{
		"code": {Agent: "worker", MaxVisits: maxVisits, On: map[string]string{"done": "end", "error": "end"}},
		"end":  {End: true},
	}}
}

func validateRoles(role string) error {
	if role == "worker-critical" {
		return nil
	}
	return fmt.Errorf("role %q is not defined", role)
}

// startReplaceRun runs the workflow in a goroutine and returns the runner,
// the run state and the channel that receives the Run result.
func startReplaceRun(t *testing.T, a *roleAgents, store *memStore, wf *Workflow) (*Runner, *RunState, chan error) {
	t.Helper()
	r := &Runner{WF: wf, Agents: a, Store: store, Checks: &fakeChecks{}}
	r.slot = newAgentSlot("test-run", validateRoles)
	st := newRun("code")
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), st) }()
	return r, st, done
}

func waitRun(t *testing.T, done chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not finish")
	}
}

func TestReplaceValidatesRole(t *testing.T) {
	a := newRoleAgents()
	a.stuck["worker"] = true
	store := &memStore{}
	r, st, done := startReplaceRun(t, a, store, replaceWorkflow(0))
	waitStart(t, a, "worker")

	if err := r.slot.replace("test-run/worker", "nope", "stuck"); err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("replace with unknown role: err = %v", err)
	}
	if got := a.log(); len(got) != 1 {
		t.Fatalf("old agent touched by a refused role: events %v", got)
	}
	if err := r.slot.replace("test-run/worker", "worker-critical", "stuck"); err != nil {
		t.Fatalf("valid replace: %v", err)
	}
	waitRun(t, done)
	if len(st.Replaced) != 1 {
		t.Fatalf("replaced = %d, want 1", len(st.Replaced))
	}
}

func TestReplaceOnlyOncePerState(t *testing.T) {
	a := newRoleAgents()
	a.stuck["worker"] = true
	gate := make(chan struct{})
	a.hold["worker-critical"] = gate
	store := &memStore{}
	r, st, done := startReplaceRun(t, a, store, replaceWorkflow(0))
	waitStart(t, a, "worker")

	if err := r.slot.replace("test-run/worker", "worker-critical", "stuck"); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	waitStart(t, a, "worker-critical")
	err := r.slot.replace("test-run/worker-critical", "worker-critical", "again")
	if err == nil || !strings.Contains(err.Error(), "already replaced") {
		t.Fatalf("second replace in the same visit: err = %v", err)
	}
	close(gate)
	waitRun(t, done)
	if len(st.Replaced) != 1 {
		t.Fatalf("replaced = %d, want 1", len(st.Replaced))
	}
}

func TestReplaceKeepsVisits(t *testing.T) {
	a := newRoleAgents()
	a.stuck["worker"] = true
	store := &memStore{}
	r, st, done := startReplaceRun(t, a, store, replaceWorkflow(1))
	waitStart(t, a, "worker")
	if err := r.slot.replace("test-run/worker", "worker-critical", "stuck"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	waitRun(t, done)
	if st.Status != "done" {
		t.Fatalf("status = %q, want done", st.Status)
	}
	if st.Visits["code"] != 1 {
		t.Fatalf("visits = %d, want 1: a replacement must not add a visit", st.Visits["code"])
	}
}

func TestReplaceRefusedIfOldAgentWontStop(t *testing.T) {
	old := replaceWait
	replaceWait = 50 * time.Millisecond
	t.Cleanup(func() { replaceWait = old })

	a := newRoleAgents()
	gate := make(chan struct{})
	a.hold["worker"] = gate
	a.holdErr["worker"] = errors.New("cancelled")
	store := &memStore{}
	r, st, done := startReplaceRun(t, a, store, replaceWorkflow(0))
	waitStart(t, a, "worker")

	err := r.slot.replace("test-run/worker", "worker-critical", "stuck")
	if err == nil || !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("replace of a hung agent: err = %v", err)
	}
	close(gate)
	waitRun(t, done)
	if len(st.Replaced) != 0 {
		t.Fatalf("refused replacement was recorded: %+v", st.Replaced)
	}
	// The old agent was cancelled before the wait, so its step ends as an error.
	if len(st.History) != 1 || st.History[0].Key != "error" || st.History[0].Role != "worker" {
		t.Fatalf("history after refusal = %+v, want one worker step with key error", st.History)
	}
	for _, ev := range a.log() {
		if strings.HasPrefix(ev, "start worker-critical") {
			t.Fatal("new agent started after a refused replacement")
		}
	}
}

func TestReplaceCancelsOldBeforeStartingNew(t *testing.T) {
	a := newRoleAgents()
	a.stuck["worker"] = true
	store := &memStore{}
	r, _, done := startReplaceRun(t, a, store, replaceWorkflow(0))
	waitStart(t, a, "worker")
	if err := r.slot.replace("test-run/worker", "worker-critical", "stuck"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	waitRun(t, done)
	want := []string{"start worker", "stop worker", "start worker-critical", "finish worker-critical"}
	got := a.log()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestReplaceWritesStateJSON(t *testing.T) {
	a := newRoleAgents()
	a.stuck["worker"] = true
	store := &memStore{}
	r, _, done := startReplaceRun(t, a, store, replaceWorkflow(0))
	var notices []string
	r.Notify = func(s string) { notices = append(notices, s) }
	waitStart(t, a, "worker")
	if err := r.slot.replace("test-run/worker", "worker-critical", "stuck for 10 minutes"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	waitRun(t, done)
	if store.last == nil || len(store.last.Replaced) != 1 {
		t.Fatalf("state.json has no replacement: %+v", store.last)
	}
	rep := store.last.Replaced[0]
	if rep.State != "code" || rep.Visit != 1 || rep.FromAgent != "test-run/worker" ||
		rep.ToRole != "worker-critical" || rep.Reason != "stuck for 10 minutes" || rep.At.IsZero() {
		t.Fatalf("replacement = %+v", rep)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "worker-critical") {
		t.Fatalf("notices = %v", notices)
	}
}

func TestReplaceSummaryReachesNewAgent(t *testing.T) {
	a := newRoleAgents()
	a.stuck["worker"] = true
	var seen []RunContext
	agents := &summaryAgents{roleAgents: a, seen: &seen}
	r := &Runner{WF: replaceWorkflow(0), Agents: agents, Store: &memStore{}, Checks: &fakeChecks{}}
	r.slot = newAgentSlot("test-run", validateRoles)
	st := newRun("code")
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), st) }()
	waitStart(t, a, "worker")
	if err := r.slot.replace("test-run/worker", "worker-critical", "stuck"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	waitRun(t, done)
	if len(seen) == 0 || !strings.HasPrefix(seen[len(seen)-1].Summary, "Previous agent (role worker) was stopped: stuck.") {
		t.Fatalf("new agent got no summary: %+v", seen)
	}
}

// summaryAgents records the run context of every agent start.
type summaryAgents struct {
	*roleAgents
	seen *[]RunContext
}

func (s *summaryAgents) Run(ctx context.Context, role, task string, rc RunContext) (string, string, error) {
	*s.seen = append(*s.seen, rc)
	return s.roleAgents.Run(ctx, role, task, rc)
}

// TestReplaceHangingWorkerIntegration is a runner-level stand-in for the stub
// provider run: a hung worker is replaced by worker-critical and the run ends.
func TestReplaceHangingWorkerIntegration(t *testing.T) {
	a := newRoleAgents()
	a.stuck["worker"] = true
	store := &memStore{}
	r, st, done := startReplaceRun(t, a, store, replaceWorkflow(0))
	waitStart(t, a, "worker")
	if err := r.slot.replace("test-run/worker", "worker-critical", "no ping"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	waitRun(t, done)
	if st.Status != "done" || st.Current != "end" {
		t.Fatalf("status = %q current = %q, want done at end", st.Status, st.Current)
	}
}

func TestReplaceAfterRunEndsIsRefused(t *testing.T) {
	m := &Manager{}
	err := m.Replace("test-run/worker", "worker-critical", "stuck")
	if err == nil {
		t.Fatalf("Replace without an active run: err = %v", err)
	}
}

// A replacing agent gets the issue text and the summary of the stopped agent in its task.
func TestReplacedAgentTextHasSummary(t *testing.T) {
	s := &spawnRec{result: "done"}
	r := newRunner(s)
	rc := RunContext{Repo: "o/r", Issue: 5, Summary: "Previous agent (role worker) was stopped: no ping."}
	if _, _, err := r.Text(context.Background(), "worker", "", rc); err != nil {
		t.Fatal(err)
	}
	if len(s.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(s.specs))
	}
	task := s.specs[0].Task
	if !strings.Contains(task, "## Summary of the previous agent") || !strings.Contains(task, rc.Summary) {
		t.Errorf("task lacks the summary:\n%s", task)
	}
	if !strings.Contains(task, "ISSUE") {
		t.Errorf("task lacks the issue text:\n%s", task)
	}
}

// The history step records the role that ran last: worker-critical after a replacement.
func TestReplaceHistoryRecordsReplacingRole(t *testing.T) {
	a := newRoleAgents()
	a.stuck["worker"] = true
	store := &memStore{}
	r, st, done := startReplaceRun(t, a, store, replaceWorkflow(0))
	waitStart(t, a, "worker")
	if err := r.slot.replace("test-run/worker", "worker-critical", "stuck"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	waitRun(t, done)
	if len(st.History) != 1 || st.History[0].Role != "worker-critical" || st.History[0].Key != "done" {
		t.Fatalf("history = %+v, want one worker-critical step with key done", st.History)
	}
}

// lateCallAgents runs the "worker" role like an agent loop: it starts a call
// on a child dispatcher that ignores its context and ends late. The agent
// waits for that call before it returns, as Run does. Other roles use roleAgents.
type lateCallAgents struct {
	*roleAgents
	callDelay time.Duration
	callEnded atomic.Bool
}

func (l *lateCallAgents) Run(ctx context.Context, role, task string, rc RunContext) (string, string, error) {
	if role != "worker" {
		if !l.callEnded.Load() {
			l.record("early start " + role)
		}
		return l.roleAgents.Run(ctx, role, task, rc)
	}
	l.record("start worker")
	l.started <- role
	d := agent.NewChildDispatcher()
	id := d.Fire(context.Background(), func(context.Context) (string, bool) {
		time.Sleep(l.callDelay)
		l.callEnded.Store(true)
		return "late", false
	})
	<-ctx.Done()
	d.Await(ctx, id, time.Duration(math.MaxInt64))
	l.record("stop worker")
	return "", "", ctx.Err()
}

// The new agent starts only after the late call of the old agent has ended.
func TestReplaceNewAgentWaitsForLateToolCall(t *testing.T) {
	a := newRoleAgents()
	late := &lateCallAgents{roleAgents: a, callDelay: 200 * time.Millisecond}
	store := &memStore{}
	r := &Runner{WF: replaceWorkflow(0), Agents: late, Store: store, Checks: &fakeChecks{}}
	r.slot = newAgentSlot("test-run", validateRoles)
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), newRun("code")) }()
	waitStart(t, a, "worker")
	if err := r.slot.replace("test-run/worker", "worker-critical", "stuck"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	waitRun(t, done)
	for _, ev := range a.log() {
		if strings.HasPrefix(ev, "early start") {
			t.Fatalf("new agent started before the late call ended: %v", a.log())
		}
	}
	if !late.callEnded.Load() {
		t.Fatal("the late call did not end")
	}
}

// A late call that outlives the wait refuses the replacement: no new agent starts.
func TestReplaceRefusedIfLateToolCallOutlivesWait(t *testing.T) {
	old := replaceWait
	replaceWait = 50 * time.Millisecond
	t.Cleanup(func() { replaceWait = old })

	a := newRoleAgents()
	late := &lateCallAgents{roleAgents: a, callDelay: 400 * time.Millisecond}
	store := &memStore{}
	r := &Runner{WF: replaceWorkflow(0), Agents: late, Store: store, Checks: &fakeChecks{}}
	r.slot = newAgentSlot("test-run", validateRoles)
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), newRun("code")) }()
	waitStart(t, a, "worker")
	err := r.slot.replace("test-run/worker", "worker-critical", "stuck")
	if err == nil || !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("replace with a late call: err = %v", err)
	}
	waitRun(t, done)
	if !late.callEnded.Load() {
		t.Fatal("the late call did not end before the run ended")
	}
	for _, ev := range a.log() {
		if strings.HasPrefix(ev, "start worker-critical") {
			t.Fatal("new agent started after a refused replacement")
		}
	}
}
