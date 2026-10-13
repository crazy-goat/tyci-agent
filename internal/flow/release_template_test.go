package flow

import (
	"context"
	"errors"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
)

// releaseFlowCfg gives every agent role of the release template a model.
func releaseFlowCfg() *flowconfig.Config {
	return &flowconfig.Config{
		Models: map[string]string{"m": "x://1"},
		Roles: map[string]flowconfig.Role{
			"worker": {Model: "m"},
			"fixer":  {Model: "m"},
			"oracle": {Model: "m"},
		},
	}
}

func TestReleaseTemplateValidates(t *testing.T) {
	wf := initWF(t, t.TempDir(), "release", "release")
	if _, err := Validate(wf, releaseFlowCfg(), wfResolver(wf)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !wf.States["ask_release"].Human || wf.States["declined"].Result != "declined" {
		t.Fatalf("ask_release human or declined result missing")
	}
	if wf.States["tag"].Check != "checks/release_tag.sh" {
		t.Fatalf("tag state check = %q", wf.States["tag"].Check)
	}
}

func TestReleaseFlowDeclined(t *testing.T) {
	wf := initWF(t, t.TempDir(), "release", "release")
	checks := &fakeChecks{keys: map[string][]string{"checks/release_preflight.sh": {"ready"}}}
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
	if len(checks.calls) != 1 || checks.calls[0] != "checks/release_preflight.sh" {
		t.Fatalf("checks run = %v, want only the preflight", checks.calls)
	}
}

// TestReleaseFlowNoTagWithoutAsk walks the graph from start and does not go on past a human
// state. Without a human yes the run cannot reach the tag state.
func TestReleaseFlowNoTagWithoutAsk(t *testing.T) {
	wf := initWF(t, t.TempDir(), "release", "release")
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
	if seen["tag"] {
		t.Fatalf("tag is reachable without a human answer; reached %v", seen)
	}
	if !seen["ask_release"] {
		t.Fatalf("ask_release is not reached from start")
	}
}
