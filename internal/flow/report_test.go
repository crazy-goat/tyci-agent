package flow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/tools"
)

var reportPathRe = regexp.MustCompile(`MUST write (\S+report\.md)`)

// reportSpawn records the specs and writes report.md (at the path named in
// the last fresh task) when write is nil or returns true for the spec.
type reportSpawn struct {
	specs []tools.TaskSpec
	write func(sp tools.TaskSpec) bool
	last  string // report path of the last fresh task
}

func (s *reportSpawn) spawn(_ context.Context, sp tools.TaskSpec) (string, string, error) {
	s.specs = append(s.specs, sp)
	if m := reportPathRe.FindStringSubmatch(sp.Task); m != nil {
		s.last = m[1]
	}
	if s.write == nil || s.write(sp) {
		if err := os.WriteFile(s.last, []byte("CHANGES\nfix it\n"), 0o600); err != nil {
			return "", "", err
		}
	}
	return "done", "job" + string(rune('0'+len(s.specs))), nil
}

func reportRunner(s *reportSpawn) *SubagentRunner {
	return &SubagentRunner{
		Cfg: testCfg(), Render: fakeRender{}, Spawn: s.spawn,
		IssueContext: func(context.Context, string, int) (string, error) { return "ISSUE", nil },
	}
}

type keyChecks map[string]string

func (k keyChecks) Run(_ context.Context, s State, _ []string, _ string) (string, CheckResult, error) {
	return k[s.Check], CheckResult{Output: "ci log"}, nil
}

// #340: on the second visit the worker task lists the steps since its last
// visit (review CHANGES, ci red) with their artifact files, and its own dir.
func TestWorkerTask_RunSoFarOnSecondVisit(t *testing.T) {
	run := t.TempDir()
	s := &reportSpawn{}
	wf := &Workflow{Start: "code", States: map[string]State{
		"code":   {Agent: "worker", MaxVisits: 2, On: map[string]string{"done": "review"}},
		"review": {Agent: "merge_decision", On: map[string]string{"ask": "ci", "default": "ci"}},
		"ci":     {Check: "ci.sh", On: map[string]string{"red": "code"}},
		"ask":    {Ask: "help"},
	}}
	r := &Runner{WF: wf, Agents: reportRunner(s), Checks: keyChecks{"ci.sh": "red"}, RunDir: run}
	st := &RunState{Version: 1, Current: "code", Status: "running", Visits: map[string]int{}, Worktree: t.TempDir()}
	if err := r.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, history %+v", err, st.History)
	}
	var tasks []string
	for _, sp := range s.specs {
		if strings.Contains(sp.Task, "ISSUE") {
			tasks = append(tasks, sp.Task)
		}
	}
	if len(tasks) != 2 {
		t.Fatalf("worker tasks = %d", len(tasks))
	}
	art := filepath.Join(run, "artifacts")
	first, second := tasks[0], tasks[1]
	if strings.Contains(first, "- 00") || !strings.Contains(first, filepath.Join(art, "001-code", "report.md")) {
		t.Errorf("first task:\n%s", first)
	}
	for _, want := range []string{
		"- 002 review: ask: " + filepath.Join(art, "002-review", "report.md"),
		"- 003 ci: red: " + filepath.Join(art, "003-ci", "output.log"),
		"Your artifact dir: " + filepath.Join(art, "004-code"),
	} {
		if !strings.Contains(second, want) {
			t.Errorf("second task lacks %q:\n%s", want, second)
		}
	}
	if strings.Contains(second, "001 code") {
		t.Errorf("second task lists the step before the last visit:\n%s", second)
	}
}

func TestSubagentRunner_ReminderInSameSession(t *testing.T) {
	art := t.TempDir()
	s := &reportSpawn{write: func(sp tools.TaskSpec) bool { return sp.Resume != "" }}
	key, session, err := reportRunner(s).Run(context.Background(), "worker", "", RunContext{Worktree: t.TempDir(), ArtifactDir: art})
	if err != nil || key != "done" {
		t.Fatalf("key=%q err=%v", key, err)
	}
	if len(s.specs) != 2 {
		t.Fatalf("spawns = %d", len(s.specs))
	}
	want := "You did not leave your artifact at " + filepath.Join(art, "report.md") + ". Write it now."
	if sp := s.specs[1]; sp.Resume != "job1" || sp.Task != want {
		t.Errorf("reminder = %+v", sp)
	}
	if session != "job2" {
		t.Errorf("session = %q", session)
	}
}

func TestSubagentRunner_NoReportAfterTwoReminders(t *testing.T) {
	s := &reportSpawn{write: func(tools.TaskSpec) bool { return false }}
	_, _, err := reportRunner(s).Run(context.Background(), "review", "", RunContext{Worktree: t.TempDir(), ArtifactDir: t.TempDir()})
	if !errors.Is(err, ErrNoArtifact) || err.Error() != "no artifact from review" {
		t.Fatalf("err = %v", err)
	}
	if len(s.specs) != 3 || s.specs[1].Resume != "job1" || s.specs[2].Resume != "job2" {
		t.Errorf("specs = %+v", s.specs)
	}
}

// #340 e2e: a worker that never writes report.md gets two reminders, then the
// run pauses in ask with the reason.
func TestE2E_NoReport_RemindsThenPauses(t *testing.T) {
	e := newE2E(t, nil)
	s := &reportSpawn{write: func(tools.TaskSpec) bool { return false }}
	e.agentRunner = reportRunner(s)
	e.mustPause()
	e.wantStates("check_done, open_pr, code")
	if e.st.Current != "ask" || e.st.Ask == nil || e.st.Ask.Reason != "no artifact from worker" {
		t.Fatalf("current %q ask %+v", e.st.Current, e.st.Ask)
	}
	if len(s.specs) != 3 || !strings.HasPrefix(s.specs[1].Task, "You did not leave your artifact at ") {
		t.Fatalf("specs = %+v", s.specs)
	}
	h := e.st.History[2]
	if h.Artifact != "003-code" || h.Error != "no artifact from worker" || h.To != "ask" {
		t.Errorf("step = %+v", h)
	}
}
