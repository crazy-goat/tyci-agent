package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDependencyBumpTemplateValidates(t *testing.T) {
	wf := initWF(t, t.TempDir(), "dependency-bump", "dependency-bump")
	if _, err := Validate(wf, releaseFlowCfg(), wfResolver(wf)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !wf.States["ask_merge"].Human || wf.States["declined"].Result != "declined" {
		t.Fatalf("ask_merge human or declined result missing")
	}
	if wf.States["code"].Task != "fix_bump" {
		t.Fatalf("code task = %q", wf.States["code"].Task)
	}
}

// TestDependencyBumpNoMergeWithoutHuman walks the graph from start and stops at a human state.
// The merge state must not be reachable without the human yes of ask_merge.
func TestDependencyBumpNoMergeWithoutHuman(t *testing.T) {
	wf := initWF(t, t.TempDir(), "dependency-bump", "dependency-bump")
	seen := map[string]bool{}
	queue := []string{wf.Start}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		s := wf.States[name]
		if s.Human {
			continue
		}
		for _, next := range s.On {
			queue = append(queue, next)
		}
	}
	if seen["merge"] {
		t.Fatalf("merge is reachable without a human answer")
	}
	if !seen["ask_merge"] {
		t.Fatalf("ask_merge is not reached from start")
	}
}

func TestDependencyBumpNoChangeEnds(t *testing.T) {
	wf := initWF(t, t.TempDir(), "dependency-bump", "dependency-bump")
	checks := &fakeChecks{keys: map[string][]string{"checks/bump_deps.sh": {"nochange"}}}
	agents := &fakeAgents{}
	r := &Runner{WF: wf, RunDir: t.TempDir(), Checks: checks, Agents: agents}
	st := newRun("")
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Result != "done" || len(checks.calls) != 1 || len(agents.calls) != 0 {
		t.Fatalf("Result = %q, checks = %v, agents = %v", st.Result, checks.calls, agents.calls)
	}
}

// bumpChecks gives the check keys of a green dependency update up to the human ask.
func bumpChecks() *fakeChecks {
	return &fakeChecks{keys: map[string][]string{
		"checks/bump_deps.sh": {"changed"},
		"checks/verify.sh":    {"green"},
		"checks/push.sh":      {"ok"},
		"checks/ci_wait.sh":   {"green"},
		"checks/merge.sh":     {"merged"},
	}}
}

func TestDependencyBumpYesMerges(t *testing.T) {
	wf := initWF(t, t.TempDir(), "dependency-bump", "dependency-bump")
	checks := bumpChecks()
	r := &Runner{WF: wf, RunDir: t.TempDir(), Checks: checks, Agents: &fakeAgents{}}
	st := newRun("")
	if err := r.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatalf("Run err = %v, want ErrPaused", err)
	}
	if st.Current != "ask_merge" || strings.Contains(strings.Join(checks.calls, " "), "merge.sh") {
		t.Fatalf("current %q, checks %v: merge ran before the answer", st.Current, checks.calls)
	}
	if err := r.Resume(context.Background(), st, "yes"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if st.Result != "done" || checks.calls[len(checks.calls)-1] != "checks/merge.sh" {
		t.Fatalf("Result = %q, checks = %v", st.Result, checks.calls)
	}
}

func TestDependencyBumpNoDeclines(t *testing.T) {
	wf := initWF(t, t.TempDir(), "dependency-bump", "dependency-bump")
	checks := bumpChecks()
	r := &Runner{WF: wf, RunDir: t.TempDir(), Checks: checks, Agents: &fakeAgents{}}
	st := newRun("")
	if err := r.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatalf("Run err = %v, want ErrPaused", err)
	}
	if err := r.Resume(context.Background(), st, "no"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if st.Result != "declined" {
		t.Fatalf("Result = %q, want declined", st.Result)
	}
	for _, c := range checks.calls {
		if c == "checks/merge.sh" {
			t.Fatalf("merge ran after a no: %v", checks.calls)
		}
	}
}
