package display

import (
	"fmt"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/crazy-goat/tyci-agent/jobs"
)

// treeTestJob returns a subagent job of run runID, started at a time that
// depends on n, so that newer jobs have a larger n.
func treeTestJob(id, runID, parent string, status jobs.Status, n int) jobs.Job {
	return jobs.Job{
		ID: id, Kind: jobs.KindSubagent, RunID: runID, ParentID: parent,
		Description: "worker " + id, Status: status,
		StartedAt: time.Unix(1000+int64(n), 0),
	}
}

func treeTestRun(id string, status string, n int) TuiRunRow {
	return TuiRunRow{ID: id, Status: status, Started: time.Unix(1000+int64(n), 0), Ended: time.Unix(1000+int64(n), 0)}
}

func treeModel(runs []TuiRunRow, list ...jobs.Job) TuiModel {
	m := TuiModel{backgroundJobs: map[string]jobs.Job{}}
	for _, j := range list {
		m.backgroundJobs[j.ID] = j
	}
	if runs != nil {
		m.runLister = func() []TuiRunRow { return runs }
	}
	return m
}

func agentIDs(agents []treeAgent) []string {
	var ids []string
	for _, a := range agents {
		ids = append(ids, a.job.ID)
	}
	return ids
}

func TestBuildTree_ThreeWorkersThreeRuns(t *testing.T) {
	m := treeModel([]TuiRunRow{
		treeTestRun("#1", "running", 3), treeTestRun("#2", "running", 2), treeTestRun("#3", "running", 1),
	},
		treeTestJob("a", "#1", "", jobs.StatusRunning, 1),
		treeTestJob("b", "#2", "", jobs.StatusRunning, 2),
		treeTestJob("c", "#3", "", jobs.StatusDone, 3),
	)
	got := m.buildTree()
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3", len(got))
	}
	for i, want := range []string{"#1", "#2", "#3"} {
		if got[i].run.ID != want || len(got[i].agents) != 1 {
			t.Fatalf("row %d = %s with %v, want %s with one agent", i, got[i].run.ID, agentIDs(got[i].agents), want)
		}
	}
}

func TestBuildTree_OrphanAgentGoesToAdHoc(t *testing.T) {
	m := treeModel([]TuiRunRow{treeTestRun("#1", "running", 1)},
		treeTestJob("a", "#1", "", jobs.StatusRunning, 1),
		treeTestJob("x", "", "", jobs.StatusRunning, 2),
	)
	got := m.buildTree()
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	last := got[len(got)-1]
	if last.run.ID != treeAdHocID || len(last.agents) != 1 || last.agents[0].job.ID != "x" {
		t.Fatalf("ad-hoc row = %+v, want agent x", last)
	}
}

func TestBuildTree_WaitingAnswerShowsQuestion(t *testing.T) {
	j := treeTestJob("a", "#1", "", jobs.StatusWaitingAnswer, 1)
	j.Progress = "which branch?"
	m := treeModel([]TuiRunRow{treeTestRun("#1", "running", 1)}, j)
	got := m.buildTree()
	if len(got) != 1 || len(got[0].agents) != 1 {
		t.Fatalf("tree = %+v, want one run with one agent", got)
	}
	if q := askText(got[0].agents[0].job); q != "which branch?" {
		t.Fatalf("ask = %q, want the question", q)
	}
}

func TestBuildTree_NoRunSource(t *testing.T) {
	m := treeModel(nil, treeTestJob("x", "", "", jobs.StatusRunning, 1))
	got := m.buildTree()
	if len(got) != 1 || got[0].run.ID != treeAdHocID {
		t.Fatalf("tree = %+v, want only the ad-hoc row", got)
	}
	if got := treeModel(nil).buildTree(); len(got) != 0 {
		t.Fatalf("empty model tree = %+v, want none", got)
	}
}

func TestBuildTree_FinishedRunsCapped(t *testing.T) {
	var runs []TuiRunRow
	for i := 1; i <= 8; i++ {
		runs = append(runs, treeTestRun(fmt.Sprintf("#%d", i), "done", i))
	}
	runs = append(runs, treeTestRun("#9", "running", 0))
	got := treeModel(runs).buildTree()
	if len(got) != treeFinishedRuns+1 {
		t.Fatalf("rows = %d, want %d (one active, %d finished)", len(got), treeFinishedRuns+1, treeFinishedRuns)
	}
	if got[0].run.ID != "#9" || got[1].run.ID != "#8" {
		t.Fatalf("order = %s, %s; want #9 then #8 (newest finished first)", got[0].run.ID, got[1].run.ID)
	}
}

func TestBuildTree_PingColours(t *testing.T) {
	cases := []struct {
		status jobs.Status
		missed bool
		want   lipgloss.TerminalColor
	}{
		{jobs.StatusFailed, true, lipgloss.Color("196")},
		{jobs.StatusFailed, false, lipgloss.Color("196")},
		{jobs.StatusDone, false, lipgloss.Color("245")},
		{jobs.StatusRunning, true, lipgloss.Color("220")},
		{jobs.StatusRunning, false, lipgloss.NoColor{}},
	}
	for _, c := range cases {
		if got := pingColour(treeTestJob("a", "", "", c.status, 1), c.missed); got != c.want {
			t.Fatalf("%s missed=%v colour = %v, want %v", c.status, c.missed, got, c.want)
		}
	}
}

func TestTreeState_PingBeforeJob(t *testing.T) {
	m := treeModel([]TuiRunRow{treeTestRun("#1", "running", 1)}, treeTestJob("a", "#1", "", jobs.StatusRunning, 1))
	m.tree = newTreeState()
	m.tree.markMissed("a")
	got := m.buildTree()
	if len(got) != 1 || len(got[0].agents) != 1 {
		t.Fatalf("tree = %+v, want one agent", got)
	}
	if c := got[0].agents[0].colour; c != lipgloss.Color("220") {
		t.Fatalf("colour = %v, want yellow 220 after a missed ping", c)
	}
	var nilTree *treeState
	if nilTree.isMissed("a") {
		t.Fatal("nil treeState reports a missed ping")
	}
}

func TestTreeState_CapsUnknownPings(t *testing.T) {
	s := newTreeState()
	for i := range treeMaxMissed + 10 {
		s.markMissed(fmt.Sprintf("job-%d", i))
	}
	if len(s.missed) != treeMaxMissed {
		t.Fatalf("missed = %d entries, want %d", len(s.missed), treeMaxMissed)
	}
}
