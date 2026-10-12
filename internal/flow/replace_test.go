package flow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
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
	keys    map[string]string
}

func newRoleAgents() *roleAgents {
	return &roleAgents{
		started: make(chan string, 32),
		stuck:   map[string]bool{},
		hold:    map[string]chan struct{}{},
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
