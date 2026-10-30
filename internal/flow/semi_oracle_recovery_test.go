package flow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/tyciconfig"
)

type humanFixRecoveryChecks struct {
	calls []string
}

func (c *humanFixRecoveryChecks) Run(_ context.Context, s State, _ []string, _ string) (string, CheckResult, error) {
	c.calls = append(c.calls, s.Check)
	return "", CheckResult{}, fmt.Errorf("recorded check %s", s.Check)
}

func TestHumanFixRecoveryBlocksOraclePublishingRoutes(t *testing.T) {
	for _, target := range []string{"lock", "update", "post_review", "ci", "comments", "rebase", "merge", "merge_approved", "findings", "open_pr", "check_done", "plan"} {
		t.Run(target, func(t *testing.T) {
			home, project := t.TempDir(), t.TempDir()
			wf := semiRecoveryBuiltin(t, home, project)
			store := &Store{Dir: filepath.Join(home, "pending-human-fix")}
			agents := &semiRecoveryAgents{steps: []semiRecoveryAgentStep{
				{state: "code", role: "worker", key: "done"},
				{state: "review", role: "review", key: "ACCEPT"},
				{state: "code", role: "worker", err: errors.New("simulated failed human fix")},
				{state: "ask", role: "oracle", key: "goto:" + target + "\nContinue the previously accepted change."},
			}}
			checks := &humanFixRecoveryChecks{}
			st := newRun("code")
			st.Workflow, st.Worktree, st.PR = "issue-to-merge", t.TempDir(), 0
			runner := &Runner{WF: wf, Agents: agents, Checks: checks, Store: store}
			if err := runner.Run(context.Background(), st); !errors.Is(err, ErrPaused) || st.Current != "human_review" {
				t.Fatalf("initial human gate = %+v, error = %v", st, err)
			}
			err := runner.Resume(context.Background(), st, "fix resolve the human request")
			if len(checks.calls) != 0 {
				t.Fatalf("oracle bypassed pending human review through %s: checks=%v, error=%v", target, checks.calls, err)
			}
			if !errors.Is(err, ErrPaused) || st.Status != "paused" || st.Current != "human_review" || !isHumanReviewPause(st) {
				t.Fatalf("pending human request must keep its gate: state=%+v error=%v", st, err)
			}
			if got := agents.gotos[len(agents.gotos)-1]; got != "code, human_review" {
				t.Fatalf("oracle targets = %q, want only code and human_review", got)
			}
			saved, err := Load(store.Dir)
			if err != nil || saved.Current != "human_review" || saved.Visits["code"] != 2 || !isHumanReviewPause(saved) {
				t.Fatalf("saved recovery state = %+v, error = %v", saved, err)
			}
		})
	}
}

func TestAutoRecoveryKeepsOraclePublishingRoutes(t *testing.T) {
	for _, target := range []string{"lock", "update", "ci", "comments", "rebase"} {
		t.Run(target, func(t *testing.T) {
			home, project := t.TempDir(), t.TempDir()
			wf := semiRecoveryBuiltin(t, home, project)
			wf.Mode = tyciconfig.ModeAuto
			agents := &semiRecoveryAgents{steps: []semiRecoveryAgentStep{
				{state: "code", role: "worker", err: errors.New("simulated auto worker failure")},
				{state: "ask", role: "oracle", key: "goto:" + target + "\nContinue auto recovery."},
			}}
			checks := &humanFixRecoveryChecks{}
			st := newRun("code")
			runner := &Runner{WF: wf, Agents: agents, Checks: checks}
			_ = runner.Run(context.Background(), st)
			if len(checks.calls) != 1 || checks.calls[0] != wf.States[target].Check {
				t.Fatalf("auto recovery changed: target=%s checks=%v state=%+v", target, checks.calls, st)
			}
		})
	}
}

func TestHumanFixRecoveryWithoutOracleKeepsHumanGate(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	wf := semiRecoveryBuiltin(t, home, project)
	zero := 0
	wf.Defaults.OracleAnswers = &zero
	agents := &semiRecoveryAgents{steps: []semiRecoveryAgentStep{
		{state: "code", role: "worker", key: "done"},
		{state: "review", role: "review", key: "ACCEPT"},
		{state: "code", role: "worker", err: errors.New("simulated failed human fix")},
	}}
	st := newRun("code")
	store := &Store{Dir: t.TempDir()}
	runner := &Runner{WF: wf, Agents: agents, Store: store}
	if err := runner.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatal(err)
	}
	if err := runner.Resume(context.Background(), st, "fix the human request"); !errors.Is(err, ErrPaused) || st.Current != "human_review" || !isHumanReviewPause(st) || st.Visits["code"] != 2 {
		t.Fatalf("failed fix lost its human gate without oracle: state=%+v error=%v", st, err)
	}
}

func TestHumanFixRecoveryOracleCanReturnToHumanGate(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	wf := semiRecoveryBuiltin(t, home, project)
	agents := &semiRecoveryAgents{steps: []semiRecoveryAgentStep{
		{state: "code", role: "worker", key: "done"},
		{state: "review", role: "review", key: "ACCEPT"},
		{state: "code", role: "worker", err: errors.New("simulated failed human fix")},
		{state: "ask", role: "oracle", key: "goto:human_review\nA human must decide the failed fix."},
	}}
	st := newRun("code")
	runner := &Runner{WF: wf, Agents: agents, Store: &Store{Dir: t.TempDir()}}
	if err := runner.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatal(err)
	}
	if err := runner.Resume(context.Background(), st, "fix the human request"); !errors.Is(err, ErrPaused) || st.Current != "human_review" || !isHumanReviewPause(st) || st.Visits["code"] != 2 {
		t.Fatalf("oracle return to human gate reset visits: state=%+v error=%v", st, err)
	}
}
