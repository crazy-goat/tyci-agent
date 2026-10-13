package flow

import (
	"context"
	"errors"
	"testing"
)

func TestRebalanceTemplateValidates(t *testing.T) {
	wf := initWF(t, t.TempDir(), "rebalance", "rebalance")
	if _, err := Validate(wf, releaseFlowCfg(), wfResolver(wf)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !wf.States["ask"].Human || wf.States["declined"].Result != "declined" {
		t.Fatalf("ask human or declined result missing")
	}
	if wf.States["split"].MaxVisits != 3 {
		t.Fatalf("split max_visits = %d, want 3", wf.States["split"].MaxVisits)
	}
}

func TestRebalanceFlowDecline(t *testing.T) {
	wf := initWF(t, t.TempDir(), "rebalance", "rebalance")
	checks := &fakeChecks{keys: map[string][]string{
		"checks/rebalance_collect.sh":  {"found"},
		"checks/rebalance_validate.sh": {"ok"},
	}}
	agents := &fakeAgents{keys: map[string][]string{"split": {"done"}}}
	r := &Runner{WF: wf, RunDir: t.TempDir(), Checks: checks, Agents: agents}
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
		if c == "checks/rebalance_apply.sh" {
			t.Fatalf("checks run = %v, apply ran without a yes", checks.calls)
		}
	}
}

func TestRebalanceFlowInvalidLoop(t *testing.T) {
	wf := initWF(t, t.TempDir(), "rebalance", "rebalance")
	checks := &fakeChecks{keys: map[string][]string{
		"checks/rebalance_collect.sh":  {"found"},
		"checks/rebalance_validate.sh": {"invalid", "invalid", "invalid"},
	}}
	agents := &fakeAgents{keys: map[string][]string{"split": manyKeys("done", 5)}}
	r := &Runner{WF: wf, RunDir: t.TempDir(), Checks: checks, Agents: agents}
	st := newRun("")
	if err := r.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatalf("Run err = %v, want ErrPaused", err)
	}
	if st.Status != "paused" || st.Ask == nil || st.Ask.Reason != "max_visits:split" {
		t.Fatalf("status %q ask %+v", st.Status, st.Ask)
	}
	if st.Visits["split"] != 3 || st.Current != "ask" {
		t.Fatalf("visits %v current %q", st.Visits, st.Current)
	}
	for _, c := range checks.calls {
		if c == "checks/rebalance_apply.sh" {
			t.Fatalf("checks run = %v, apply ran after invalid splits", checks.calls)
		}
	}
}
