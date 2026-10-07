package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// loopWF mirrors the builtin code/ci/merge loop with an ask state.
func loopWF(codeLimit, ciLimit, def int) *Workflow {
	return &Workflow{Name: "demo", Start: "code", Defaults: Defaults{MaxVisits: def}, States: map[string]State{
		"code":  {Agent: "coder", MaxVisits: codeLimit, On: map[string]string{"done": "ci"}},
		"ci":    {Check: "ci.sh", MaxVisits: ciLimit, On: map[string]string{"green": "merge", "red": "code"}},
		"merge": {Check: "merge.sh", On: map[string]string{"merged": "end"}},
		"ask":   {Ask: "The run needs a human decision.", On: map[string]string{"retry": "code", "stop": "end"}},
		"end":   {End: true},
	}}
}

func manyKeys(k string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = k
	}
	return out
}

func runLoop(t *testing.T, wf *Workflow) (*Runner, *RunState, *fakeChecks, error) {
	t.Helper()
	checks := &fakeChecks{keys: map[string][]string{"ci.sh": manyKeys("red", 20), "merge.sh": {"merged"}}}
	agents := &fakeAgents{keys: map[string][]string{"code": manyKeys("done", 20)}}
	r := &Runner{WF: wf, Checks: checks, Agents: agents, Store: &memStore{}}
	st := newRun("")
	err := r.Run(context.Background(), st)
	return r, st, checks, err
}

func TestRunner_MaxVisitsGoesToAsk(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "code", States: map[string]State{
		"code":   {Agent: "coder", MaxVisits: 2, On: map[string]string{"done": "review"}},
		"review": {Agent: "reviewer", On: map[string]string{"changes": "code"}},
		"ask":    {Ask: "help"},
		"end":    {End: true},
	}}
	r := &Runner{WF: wf, Store: &memStore{},
		Agents: &fakeAgents{keys: map[string][]string{"code": manyKeys("done", 5), "review": manyKeys("changes", 5)}}}
	st := newRun("")
	if err := r.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if st.Status != "paused" || st.Ask == nil || st.Ask.Reason != "max_visits:code" {
		t.Fatalf("status %q ask %+v", st.Status, st.Ask)
	}
	if st.Visits["code"] != 2 || st.Current != "ask" {
		t.Fatalf("visits %v current %q", st.Visits, st.Current)
	}
}

func TestRunner_RedCIThreeTimesEndsInAsk(t *testing.T) {
	_, st, checks, err := runLoop(t, loopWF(3, 3, 0))
	if !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if st.Ask.Reason != "max_visits:code" || st.Visits["ci"] != 3 || st.Visits["code"] != 3 {
		t.Fatalf("ask %+v visits %v", st.Ask, st.Visits)
	}
	for _, s := range historyStates(st) {
		if s == "merge" {
			t.Fatal("merge ran")
		}
	}
	for _, c := range checks.calls {
		if c == "merge.sh" {
			t.Fatal("merge checker called")
		}
	}
}

func TestRunner_CILimitFirstGivesCIReason(t *testing.T) {
	_, st, _, err := runLoop(t, loopWF(0, 1, 0))
	if !errors.Is(err, ErrPaused) || st.Ask.Reason != "max_visits:ci" {
		t.Fatalf("err %v ask %+v", err, st.Ask)
	}
	if st.Visits["ci"] != 1 {
		t.Fatalf("visits %v", st.Visits)
	}
}

func TestRunner_DefaultsMaxVisitsApplies(t *testing.T) {
	_, st, _, err := runLoop(t, loopWF(0, 0, 2))
	if !errors.Is(err, ErrPaused) || st.Ask.Reason != "max_visits:code" || st.Visits["code"] != 2 {
		t.Fatalf("err %v ask %+v visits %v", err, st.Ask, st.Visits)
	}
}

func TestRunner_UnlimitedWhenZero(t *testing.T) {
	wf := loopWF(0, 0, 0)
	checks := &fakeChecks{keys: map[string][]string{"ci.sh": append(manyKeys("red", 10), "green"), "merge.sh": {"merged"}}}
	r := &Runner{WF: wf, Checks: checks, Store: &memStore{},
		Agents: &fakeAgents{keys: map[string][]string{"code": manyKeys("done", 20)}}}
	st := newRun("")
	if err := r.Run(context.Background(), st); err != nil || st.Status != "done" {
		t.Fatalf("err %v status %q", err, st.Status)
	}
	if st.Visits["code"] != 11 {
		t.Fatalf("visits %v", st.Visits)
	}
}

func pausedRun(t *testing.T, wf *Workflow) (*Runner, *RunState, *memStore) {
	t.Helper()
	store := &memStore{}
	r := &Runner{WF: wf, Store: store,
		Checks: &fakeChecks{keys: map[string][]string{"ci.sh": manyKeys("red", 20)}},
		Agents: &fakeAgents{keys: map[string][]string{"code": manyKeys("done", 20)}}}
	st := newRun("")
	if err := r.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	return r, st, store
}

func TestResume_UnknownAnswerKeepsPaused(t *testing.T) {
	r, st, _ := pausedRun(t, loopWF(1, 0, 0))
	err := r.Resume(context.Background(), st, "bogus")
	if err == nil || !strings.Contains(err.Error(), "retry, stop") {
		t.Fatalf("err = %v", err)
	}
	if st.Status != "paused" || st.Current != "ask" {
		t.Fatalf("status %q current %q", st.Status, st.Current)
	}
}

func TestResume_RetryResetsCounters(t *testing.T) {
	wf := loopWF(1, 0, 0)
	r, st, store := pausedRun(t, wf)
	// Stop the loop after the reset by making ci green.
	r.Checks = &fakeChecks{keys: map[string][]string{"ci.sh": {"green"}, "merge.sh": {"merged"}}}
	var atTransition map[string]int
	r.Agents = &hookAgents{inner: &fakeAgents{keys: map[string][]string{"code": {"done"}}}, onRun: func() {
		atTransition = map[string]int{}
		for k, v := range store.last.Visits {
			atTransition[k] = v
		}
	}}
	if err := r.Resume(context.Background(), st, "retry"); err != nil {
		t.Fatal(err)
	}
	// The save before entering code has an empty visits map.
	if len(atTransition) != 1 || atTransition["code"] != 1 {
		t.Fatalf("visits at first agent run = %v", atTransition)
	}
	if st.Status != "done" || st.Visits["code"] != 1 {
		t.Fatalf("status %q visits %v", st.Status, st.Visits)
	}
	last := st.History[len(st.History)-1]
	found := false
	for _, h := range st.History {
		if h.Kind == "ask" && h.Key == "retry" && h.To == "code" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no ask step in history, last %+v", last)
	}
}

type hookAgents struct {
	inner AgentRunner
	onRun func()
}

func (h *hookAgents) Run(ctx context.Context, role, task string, rc RunContext) (string, string, error) {
	h.onRun()
	return h.inner.Run(ctx, role, task, rc)
}

func TestResume_StopEndsRun(t *testing.T) {
	r, st, _ := pausedRun(t, loopWF(1, 0, 0))
	if err := r.Resume(context.Background(), st, "stop"); err != nil {
		t.Fatal(err)
	}
	if st.Status != "done" || st.Visits["code"] != 1 {
		t.Fatalf("status %q visits %v", st.Status, st.Visits)
	}
}

func TestResume_AskWithoutOnEndsDone(t *testing.T) {
	wf := loopWF(1, 0, 0)
	wf.States["ask"] = State{Ask: "help"}
	r, st, _ := pausedRun(t, wf)
	if err := r.Resume(context.Background(), st, "anything"); err != nil {
		t.Fatal(err)
	}
	if st.Status != "done" {
		t.Fatalf("status %q", st.Status)
	}
}

func TestResume_NotPausedIsError(t *testing.T) {
	r := &Runner{WF: loopWF(1, 0, 0)}
	st := newRun("code")
	if err := r.Resume(context.Background(), st, "retry"); err == nil {
		t.Fatal("expected error")
	}
}

func TestResume_WildcardKey(t *testing.T) {
	wf := loopWF(1, 0, 0)
	wf.States["ask"] = State{Ask: "help", On: map[string]string{"*": "end"}}
	r, st, _ := pausedRun(t, wf)
	if err := r.Resume(context.Background(), st, "free text"); err != nil {
		t.Fatal(err)
	}
	if st.Status != "done" {
		t.Fatalf("status %q", st.Status)
	}
}

func TestRunner_AgentErrorIsSavedAndShownInAsk(t *testing.T) {
	wf := loopWF(0, 0, 0)
	s := wf.States["code"]
	s.On = map[string]string{"done": "ci", "error": "ask"}
	wf.States["code"] = s
	store := &memStore{}
	r := &Runner{WF: wf, Store: store,
		Agents: &fakeAgents{errs: map[string]error{"code": errors.New("model not in catalog")}}}
	st := newRun("")
	if err := r.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if got := st.History[0].Error; got != "model not in catalog" {
		t.Fatalf("history error = %q", got)
	}
	if !strings.Contains(st.Ask.Message, "code failed: model not in catalog") {
		t.Fatalf("ask message = %q", st.Ask.Message)
	}
}

func TestResume_RetryWithNoteReachesWorker(t *testing.T) {
	wf := loopWF(1, 0, 0)
	wf.States["code"] = State{Agent: "worker", MaxVisits: 1, On: map[string]string{"done": "ci"}}
	r, st, _ := pausedRun(t, wf)
	r.Checks = &fakeChecks{keys: map[string][]string{"ci.sh": {"green"}, "merge.sh": {"merged"}}}
	var note string
	r.Agents = &noteAgents{note: &note}
	if err := r.Resume(context.Background(), st, "retry resolve the CHANGELOG.md conflict"); err != nil {
		t.Fatal(err)
	}
	if note != "resolve the CHANGELOG.md conflict" {
		t.Fatalf("worker note = %q", note)
	}
	if st.Note != "" || st.Status != "done" {
		t.Fatalf("note %q status %q", st.Note, st.Status)
	}
}

type noteAgents struct{ note *string }

func (n *noteAgents) Run(_ context.Context, _, _ string, rc RunContext) (string, string, error) {
	*n.note = rc.Note
	return "done", "", nil
}

func TestResume_GotoState(t *testing.T) {
	r, st, _ := pausedRun(t, loopWF(1, 0, 0))
	r.Checks = &fakeChecks{keys: map[string][]string{"ci.sh": {"green"}, "merge.sh": {"merged"}}}
	if err := r.Resume(context.Background(), st, "goto ci"); err != nil {
		t.Fatal(err)
	}
	if st.Status != "done" || st.Visits["code"] != 0 || st.Visits["ci"] != 1 {
		t.Fatalf("status %q visits %v", st.Status, st.Visits)
	}
}

func TestResume_GotoUnknownStateRejected(t *testing.T) {
	r, st, _ := pausedRun(t, loopWF(1, 0, 0))
	for _, a := range []string{"goto nowhere", "goto ask", "goto end"} {
		if err := r.Resume(context.Background(), st, a); err == nil {
			t.Fatalf("%q: want error", a)
		}
	}
	if st.Status != "paused" {
		t.Fatalf("status %q", st.Status)
	}
}
