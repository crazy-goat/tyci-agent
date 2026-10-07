package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeChecks struct {
	keys  map[string][]string
	errs  map[string]error
	calls []string
	panic string
}

func (f *fakeChecks) Run(_ context.Context, s State, _ []string, _ string) (string, CheckResult, error) {
	f.calls = append(f.calls, s.Check)
	if f.panic != "" {
		panic(f.panic)
	}
	if err, ok := f.errs[s.Check]; ok && err != nil {
		return "", CheckResult{}, err
	}
	q := f.keys[s.Check]
	if len(q) == 0 {
		return "", CheckResult{}, errors.New("fakeChecks: no scripted key for " + s.Check)
	}
	f.keys[s.Check] = q[1:]
	exit := 0
	return q[0], CheckResult{Exit: &exit}, nil
}

type fakeAgents struct {
	keys  map[string][]string
	errs  map[string]error
	calls []string
}

func (f *fakeAgents) Run(_ context.Context, role, _ string, rc RunContext) (string, string, error) {
	f.calls = append(f.calls, rc.StateName)
	if err, ok := f.errs[rc.StateName]; ok && err != nil {
		return "", "", err
	}
	q := f.keys[rc.StateName]
	if len(q) == 0 {
		return "", "", errors.New("fakeAgents: no scripted key for " + rc.StateName)
	}
	f.keys[rc.StateName] = q[1:]
	return q[0], "sess-" + rc.StateName, nil
}

type memStore struct {
	saves int
	last  *RunState
}

func (m *memStore) Save(st *RunState) error {
	m.saves++
	cp := *st
	m.last = &cp
	return nil
}

func newRun(current string) *RunState {
	return &RunState{
		Version:  1,
		Run:      "test-run",
		Workflow: "demo",
		Repo:     "o/r",
		Issue:    1,
		Branch:   "issue-1",
		Worktree: "/tmp/wt",
		Status:   "running",
		Current:  current,
		Visits:   map[string]int{},
	}
}

func historyStates(st *RunState) []string {
	out := make([]string, 0, len(st.History))
	for _, h := range st.History {
		out = append(out, h.State)
	}
	return out
}

func TestRunner_HappyPath_VisitsEveryStateInOrder(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Check: "checks/x.sh", On: map[string]string{"go": "b"}},
		"b":   {Agent: "worker", On: map[string]string{"done": "end"}},
		"end": {End: true},
	}}
	store := &memStore{}
	r := &Runner{WF: wf, Checks: &fakeChecks{keys: map[string][]string{"checks/x.sh": {"go"}}}, Agents: &fakeAgents{keys: map[string][]string{"b": {"done"}}}, Store: store}
	st := newRun("a")
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != "done" {
		t.Fatalf("status = %q, want done", st.Status)
	}
	got := historyStates(st)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("history order = %v, want [a b]", got)
	}
	if st.History[0].Seq != 1 || st.History[1].Seq != 2 {
		t.Fatalf("seqs = %d,%d, want 1,2", st.History[0].Seq, st.History[1].Seq)
	}
	if st.History[1].Role != "worker" || st.History[1].Session == "" {
		t.Fatalf("agent step missing role/session: %+v", st.History[1])
	}
	if store.saves == 0 {
		t.Fatal("expected Store.Save to be called")
	}
}

func TestRunner_DefaultKeyFallback(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Check: "x.sh", On: map[string]string{"default": "end"}},
		"end": {End: true},
	}}
	r := &Runner{WF: wf, Checks: &fakeChecks{keys: map[string][]string{"x.sh": {"surprise"}}}, Store: &memStore{}}
	st := newRun("a")
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != "done" || st.Current != "end" {
		t.Fatalf("status = %q current = %q, want done/end", st.Status, st.Current)
	}
	if st.History[0].Key != "surprise" || st.History[0].To != "end" {
		t.Fatalf("step = %+v, want key surprise to end", st.History[0])
	}
}

func TestRunner_UnknownKeyFailsRun(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Check: "x.sh", On: map[string]string{"go": "end"}},
		"end": {End: true},
	}}
	r := &Runner{WF: wf, Checks: &fakeChecks{keys: map[string][]string{"x.sh": {"bogus"}}}, Store: &memStore{}}
	st := newRun("a")
	err := r.Run(context.Background(), st)
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
	if st.Status != "failed" {
		t.Fatalf("status = %q, want failed", st.Status)
	}
	want := `unknown transition key "bogus" in state "a"`
	if st.Reason != want {
		t.Fatalf("Reason = %q, want %q", st.Reason, want)
	}
}

func TestRunner_ReviewChangesLoopsToCode(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "code", States: map[string]State{
		"code":   {Agent: "worker", On: map[string]string{"done": "review"}},
		"review": {Agent: "review", On: map[string]string{"CHANGES": "code", "ACCEPT": "end"}},
		"end":    {End: true},
	}}
	r := &Runner{WF: wf, Agents: &fakeAgents{keys: map[string][]string{
		"code":   {"done", "done"},
		"review": {"CHANGES", "ACCEPT"},
	}}, Store: &memStore{}}
	st := newRun("code")
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := historyStates(st)
	want := []string{"code", "review", "code", "review"}
	if len(got) != len(want) {
		t.Fatalf("history = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history = %v, want %v", got, want)
		}
	}
	if st.Status != "done" {
		t.Fatalf("status = %q, want done", st.Status)
	}
}

func TestRunner_AgentErrorUsesErrorKey(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "code", States: map[string]State{
		"code": {Agent: "worker", On: map[string]string{"error": "end"}},
		"end":  {End: true},
	}}
	r := &Runner{WF: wf, Agents: &fakeAgents{errs: map[string]error{"code": errors.New("boom")}}, Store: &memStore{}}
	st := newRun("code")
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != "done" || st.Current != "end" {
		t.Fatalf("status = %q current = %q, want done/end", st.Status, st.Current)
	}
	if st.History[0].Key != "error" {
		t.Fatalf("key = %q, want error", st.History[0].Key)
	}
}

func TestRunner_AgentErrorWithoutRouteFails(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "code", States: map[string]State{
		"code": {Agent: "worker", On: map[string]string{"done": "end"}},
		"end":  {End: true},
	}}
	r := &Runner{WF: wf, Agents: &fakeAgents{errs: map[string]error{"code": errors.New("boom")}}, Store: &memStore{}}
	st := newRun("code")
	err := r.Run(context.Background(), st)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if st.Status != "failed" {
		t.Fatalf("status = %q, want failed", st.Status)
	}
	if st.Reason == "" {
		t.Fatal("expected a Reason for agent failure without route")
	}
}

func TestRunner_CheckErrorFailsRun(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Check: "x.sh", On: map[string]string{"go": "end"}},
		"end": {End: true},
	}}
	r := &Runner{WF: wf, Checks: &fakeChecks{errs: map[string]error{"x.sh": errors.New("exec failed")}}, Store: &memStore{}}
	st := newRun("a")
	err := r.Run(context.Background(), st)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if st.Status != "failed" {
		t.Fatalf("status = %q, want failed", st.Status)
	}
	if st.Reason == "" {
		t.Fatal("expected a Reason for check runner error")
	}
}

func TestRunner_CancelMarksFailed(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Check: "x.sh", On: map[string]string{"go": "end"}},
		"end": {End: true},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &Runner{WF: wf, Checks: &fakeChecks{keys: map[string][]string{"x.sh": {"go"}}}, Store: &memStore{}}
	st := newRun("a")
	_ = r.Run(ctx, st)
	if st.Status != "failed" {
		t.Fatalf("status = %q, want failed", st.Status)
	}
	if st.Reason != "cancelled" {
		t.Fatalf("Reason = %q, want cancelled", st.Reason)
	}
}

func TestRunner_PanicMarksFailed(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Check: "x.sh", On: map[string]string{"go": "end"}},
		"end": {End: true},
	}}
	r := &Runner{WF: wf, Checks: &fakeChecks{panic: "kaboom"}, Store: &memStore{}}
	st := newRun("a")
	err := r.Run(context.Background(), st)
	if err == nil {
		t.Fatal("expected error after panic, got nil")
	}
	if st.Status != "failed" {
		t.Fatalf("status = %q, want failed", st.Status)
	}
	if !strings.Contains(st.Reason, "kaboom") {
		t.Fatalf("Reason = %q, want panic text", st.Reason)
	}
}

func TestRunner_AskPausesRun(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "ask", States: map[string]State{
		"ask": {Ask: "needs a human"},
		"end": {End: true},
	}}
	store := &memStore{}
	r := &Runner{WF: wf, Store: store}
	st := newRun("ask")
	err := r.Run(context.Background(), st)
	if !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if st.Status != "paused" {
		t.Fatalf("status = %q, want paused", st.Status)
	}
	if st.Ask == nil || st.Ask.Message != "needs a human" {
		t.Fatalf("ask = %+v, want message %q", st.Ask, "needs a human")
	}
	if store.saves == 0 {
		t.Fatal("expected Store.Save to be called on pause")
	}
}

func TestRunner_SkipHookCalledOnlyWhenNoAgentRan(t *testing.T) {
	checksOnly := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Check: "x.sh", On: map[string]string{"go": "end"}},
		"end": {End: true},
	}}
	skipped := 0
	r := &Runner{
		WF:     checksOnly,
		Checks: &fakeChecks{keys: map[string][]string{"x.sh": {"go"}}},
		Store:  &memStore{},
		OnSkip: func(*RunState) { skipped++ },
	}
	if err := r.Run(context.Background(), newRun("a")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("OnSkip calls = %d, want 1 for checks-only run", skipped)
	}

	withAgent := &Workflow{Name: "demo", Start: "b", States: map[string]State{
		"b":   {Agent: "worker", On: map[string]string{"done": "end"}},
		"end": {End: true},
	}}
	skipped = 0
	r2 := &Runner{
		WF:     withAgent,
		Agents: &fakeAgents{keys: map[string][]string{"b": {"done"}}},
		Store:  &memStore{},
		OnSkip: func(*RunState) { skipped++ },
	}
	if err := r2.Run(context.Background(), newRun("b")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("OnSkip calls = %d, want 0 when an agent ran", skipped)
	}
}

func TestRunner_SkipHookCalledAfterMerge(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "merge", States: map[string]State{
		"merge": {Check: "m.sh", On: map[string]string{"merged": "f", "ask": "end"}},
		"f":     {Agent: "worker", On: map[string]string{"done": "end"}},
		"end":   {End: true},
	}}
	called := 0
	r := &Runner{
		WF:     wf,
		Checks: &fakeChecks{keys: map[string][]string{"m.sh": {"merged"}}},
		Agents: &fakeAgents{keys: map[string][]string{"f": {"done"}}},
		Store:  &memStore{},
		OnSkip: func(*RunState) { called++ },
	}
	if err := r.Run(context.Background(), newRun("merge")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called != 1 {
		t.Fatalf("hook calls = %d, want 1 after merged", called)
	}
}

func TestRunner_BuiltinWorkflowRunsToEnd(t *testing.T) {
	data := []byte(`{"name":"issue-to-merge","start":"check_done","defaults":{"max_visits":0},"states":{
		"check_done":{"check":"checks/issue_done.sh","on":{"go":"code","skip":"end"}},
		"code":{"agent":"worker","on":{"done":"review"}},
		"review":{"agent":"review","on":{"ACCEPT":"push","CHANGES":"code"}},
		"push":{"check":"checks/push.sh","on":{"ok":"ci"}},
		"ci":{"check":"checks/ci_wait.sh","on":{"green":"merge"}},
		"merge":{"check":"checks/merge.sh","on":{"merged":"findings"}},
		"findings":{"agent":"review","on":{"done":"end"}},
		"ask":{"ask":"needs a human","on":{"stop":"end"}},
		"end":{"end":true}}}`)
	wf, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if errs := validateStructure(wf); len(errs) != 0 {
		t.Fatalf("validateStructure: %v", errs)
	}
	r := &Runner{
		WF: wf,
		Checks: &fakeChecks{keys: map[string][]string{
			"checks/issue_done.sh": {"go"},
			"checks/push.sh":       {"ok"},
			"checks/ci_wait.sh":    {"green"},
			"checks/merge.sh":      {"merged"},
		}},
		Agents: &fakeAgents{keys: map[string][]string{
			"code":     {"done"},
			"review":   {"ACCEPT"},
			"findings": {"done"},
		}},
		Store: &memStore{},
	}
	st := newRun("check_done")
	st.Workflow = "issue-to-merge"
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != "done" || st.Current != "end" {
		t.Fatalf("status = %q current = %q, want done/end", st.Status, st.Current)
	}
}
