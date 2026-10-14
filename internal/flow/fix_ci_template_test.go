package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFixCiTemplateValidates(t *testing.T) {
	wf := initWF(t, t.TempDir(), "fix-ci", "fix-ci")
	if _, err := Validate(wf, releaseFlowCfg(), wfResolver(wf)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if wf.States["verify"].MaxVisits != 3 || wf.States["code"].MaxVisits != 3 {
		t.Fatalf("max_visits of verify and code = %d and %d, want 3", wf.States["verify"].MaxVisits, wf.States["code"].MaxVisits)
	}
	if wf.States["merge"].Check != "checks/merge.sh" {
		t.Fatalf("merge check = %q", wf.States["merge"].Check)
	}
}

// TestFixCiGreenEndsAtStart: a green default branch ends the run before any agent or merge.
func TestFixCiGreenEndsAtStart(t *testing.T) {
	wf := initWF(t, t.TempDir(), "fix-ci", "fix-ci")
	checks := &fakeChecks{keys: map[string][]string{"checks/ci_failure.sh": {"green"}}}
	agents := &fakeAgents{}
	r := &Runner{WF: wf, RunDir: t.TempDir(), Checks: checks, Agents: agents}
	st := newRun("")
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Result != "done" || len(agents.calls) != 0 {
		t.Fatalf("Result = %q, agents = %v", st.Result, agents.calls)
	}
	if len(checks.calls) != 1 {
		t.Fatalf("checks run = %v, want only ci_failure", checks.calls)
	}
}

// TestFixCiRedFixesAndMerges: a red branch is fixed, verified, pushed, and merged after ci-ok.
func TestFixCiRedFixesAndMerges(t *testing.T) {
	wf := initWF(t, t.TempDir(), "fix-ci", "fix-ci")
	checks := &fakeChecks{keys: map[string][]string{
		"checks/ci_failure.sh": {"red"},
		"checks/verify.sh":     {"green"},
		"checks/push.sh":       {"ok"},
		"checks/ci_wait.sh":    {"green"},
		"checks/merge.sh":      {"merged"},
	}}
	agents := &fakeAgents{keys: map[string][]string{"code": {"done"}}}
	r := &Runner{WF: wf, RunDir: t.TempDir(), Checks: checks, Agents: agents}
	st := newRun("")
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Result != "done" {
		t.Fatalf("Result = %q, want done", st.Result)
	}
	want := "checks/ci_failure.sh checks/verify.sh checks/push.sh checks/ci_wait.sh checks/merge.sh"
	if got := strings.Join(checks.calls, " "); got != want {
		t.Fatalf("checks run = %q, want %q", got, want)
	}
	if len(agents.calls) != 1 || agents.calls[0] != "code" {
		t.Fatalf("agents run = %v, want only code", agents.calls)
	}
}

// TestFixCiVerifyAsksAfterThreeReds: the run has three code visits (the first fix and two
// retries). The third red verify sends the run to code, which is past its max_visits, so the
// run pauses. The oracle answers "ask" for the pause, so a human decides.
func TestFixCiVerifyAsksAfterThreeReds(t *testing.T) {
	wf := initWF(t, t.TempDir(), "fix-ci", "fix-ci")
	checks := &fakeChecks{keys: map[string][]string{
		"checks/ci_failure.sh": {"red"},
		"checks/verify.sh":     {"red", "red", "red"},
	}}
	agents := &fakeAgents{keys: map[string][]string{"code": manyKeys("done", 5), "ask": {"ask"}}}
	r := &Runner{WF: wf, RunDir: t.TempDir(), Checks: checks, Agents: agents}
	st := newRun("")
	if err := r.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatalf("Run err = %v, want ErrPaused", err)
	}
	if st.Status != "paused" || st.Ask == nil || st.Ask.Reason != "max_visits:code" {
		t.Fatalf("status %q ask %+v", st.Status, st.Ask)
	}
	if st.Visits["verify"] != 3 || st.Current != "ask" {
		t.Fatalf("visits %v current %q", st.Visits, st.Current)
	}
	for _, c := range checks.calls {
		if c == "checks/push.sh" {
			t.Fatalf("push ran after red verifies: %v", checks.calls)
		}
	}
}
