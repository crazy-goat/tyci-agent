package display

import (
	"fmt"
	"sort"
	"strings"
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
	empty := treeModel(nil)
	if got := empty.buildTree(); len(got) != 0 {
		t.Fatalf("empty model tree = %+v, want none", got)
	}
}

func TestBuildTree_FinishedRunsCapped(t *testing.T) {
	var runs []TuiRunRow
	for i := 1; i <= 8; i++ {
		runs = append(runs, treeTestRun(fmt.Sprintf("#%d", i), "done", i))
	}
	runs = append(runs, treeTestRun("#9", "running", 0))
	withRuns := treeModel(runs)
	got := withRuns.buildTree()
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
		{jobs.StatusTruncated, false, lipgloss.Color("245")},
		{jobs.StatusRunning, true, lipgloss.Color("220")},
		{jobs.StatusRunning, false, lipgloss.Color("42")},
		{jobs.StatusWaitingAnswer, false, lipgloss.Color("42")},
		{jobs.Status(""), false, lipgloss.NoColor{}},
	}
	for _, c := range cases {
		if got := pingColour(treeTestJob("a", "", "", c.status, 1), c.missed); got != c.want {
			t.Fatalf("%s missed=%v colour = %v, want %v", c.status, c.missed, got, c.want)
		}
	}
}

// TestBuildTree_AgentsInTreeOrder checks that the agents of a run follow the
// Subagents tree order: a child is drawn under its parent. Worker A starts
// first, then B, then C, a child of A. B is finished, so the active A comes
// first with its child C, and the finished B comes last.
func TestBuildTree_AgentsInTreeOrder(t *testing.T) {
	m := treeModel([]TuiRunRow{treeTestRun("#1", "running", 3)},
		treeTestJob("a", "#1", "", jobs.StatusRunning, 1),
		treeTestJob("b", "#1", "", jobs.StatusDone, 2),
		treeTestJob("c", "#1", "a", jobs.StatusRunning, 3),
	)
	got := m.buildTree()
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	want := []struct {
		id    string
		depth int
	}{{"a", 1}, {"c", 2}, {"b", 1}}
	agents := got[0].agents
	if len(agents) != len(want) {
		t.Fatalf("agents = %v, want %v", agentIDs(agents), want)
	}
	for i, w := range want {
		if agents[i].job.ID != w.id || agents[i].depth != w.depth {
			t.Fatalf("agent %d = %s at depth %d, want %s at depth %d (order %v)",
				i, agents[i].job.ID, agents[i].depth, w.id, w.depth, agentIDs(agents))
		}
	}
}

// TestBuildTree_ResumedJobUnderOriginal checks that a resumed job is drawn
// under the job it continues, as on the Tasks tab.
func TestBuildTree_ResumedJobUnderOriginal(t *testing.T) {
	m := treeModel([]TuiRunRow{treeTestRun("#1", "running", 3)},
		treeTestJob("a", "#1", "", jobs.StatusDone, 1),
		treeTestJob("b", "#1", "", jobs.StatusDone, 2),
		treeTestJob("r", "#1", "", jobs.StatusRunning, 3),
	)
	m.resumedFrom = map[string]string{"r": "a"}
	got := m.buildTree()
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	agents := got[0].agents
	if len(agents) != 3 {
		t.Fatalf("agents = %v, want 3", agentIDs(agents))
	}
	if agents[0].job.ID != "a" || agents[0].depth != 1 ||
		agents[1].job.ID != "r" || agents[1].depth != 2 ||
		agents[2].job.ID != "b" || agents[2].depth != 1 {
		t.Fatalf("order = %v with depths %d, %d, %d; want a(1), r(2), b(1)",
			agentIDs(agents), agents[0].depth, agents[1].depth, agents[2].depth)
	}
}

// medianBuildTreeCost returns the median time of buildTree over `calls` calls.
// The median ignores scheduler noise.
func medianBuildTreeCost(m *TuiModel, calls int) time.Duration {
	costs := make([]time.Duration, 0, calls)
	for range calls {
		start := time.Now()
		_ = m.buildTree()
		costs = append(costs, time.Since(start))
	}
	sort.Slice(costs, func(i, j int) bool { return costs[i] < costs[j] })
	return costs[len(costs)/2]
}

// TestBuildTree_CostDoesNotGrowWithFlushedBlocks checks that building the
// Runs tab does not copy the whole model. The value receivers of
// runRowsCapped and treeAgents did that once per run row. The test compares a
// session with about 1000 flushed blocks to one with about 5000, both with the
// same runs and agents. The limit is 2.5, as in the scroll-wheel cost test.
func TestBuildTree_CostDoesNotGrowWithFlushedBlocks(t *testing.T) {
	build := func(blocks int) *TuiModel {
		m := newWheelBenchModel(t, blocks)
		var runs []TuiRunRow
		m.backgroundJobs = map[string]jobs.Job{}
		for r := 1; r <= 20; r++ {
			id := fmt.Sprintf("#%d", r)
			runs = append(runs, treeTestRun(id, "done", r))
			for a := 1; a <= 3; a++ {
				j := treeTestJob(fmt.Sprintf("%s-%d", id, a), id, "", jobs.StatusDone, a)
				m.backgroundJobs[j.ID] = j
			}
		}
		m.runLister = func() []TuiRunRow { return runs }
		return &m
	}
	small, large := build(1000), build(5000)
	smallFlushed, largeFlushed := countFlushed(*small), countFlushed(*large)
	if smallFlushed < 800 || largeFlushed < 4000 {
		t.Fatalf("flushed blocks: small=%d large=%d, want at least 800 and 4000", smallFlushed, largeFlushed)
	}
	if got := len(small.buildTree()); got != treeFinishedRuns {
		t.Fatalf("rows = %d, want %d", got, treeFinishedRuns)
	}
	smallCost := medianBuildTreeCost(small, 200)
	largeCost := medianBuildTreeCost(large, 200)
	if largeCost > smallCost*5/2 {
		t.Fatalf("buildTree cost grows with flushed blocks: %v with %d flushed, %v with %d flushed; want ratio at most 2.5",
			smallCost, smallFlushed, largeCost, largeFlushed)
	}
}

func TestTreeState_PingBeforeJob(t *testing.T) {
	m := treeModel([]TuiRunRow{treeTestRun("#1", "running", 1)}, treeTestJob("a", "#1", "", jobs.StatusRunning, 1))
	m.tree = newTreeState()
	m.tree.markMissed("a", false)
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
		s.markMissed(fmt.Sprintf("job-%d", i), false)
	}
	if len(s.missed) != treeMaxMissed {
		t.Fatalf("missed = %d entries, want %d", len(s.missed), treeMaxMissed)
	}
	s.markMissed("known", true)
	if !s.isMissed("known") {
		t.Fatal("a ping of a known job is not capped")
	}
}

// TestTreeState_ForgetsEndedJobs checks that the missed mark of a job is
// dropped when the job leaves the running state.
func TestTreeState_ForgetsEndedJobs(t *testing.T) {
	s := newTreeState()
	s.markMissed("a", true)
	s.forget("a")
	if s.isMissed("a") {
		t.Fatal("the missed mark stays after the job ended")
	}
	var nilTree *treeState
	nilTree.forget("a")
	nilTree.markMissed("a", false)
}

// TestAppMissedPing checks the update path: a ping of a running job is marked,
// a ping of a finished job is not, and the mark is dropped when the job ends.
func TestAppMissedPing(t *testing.T) {
	m := treeModel(nil, treeTestJob("a", "", "", jobs.StatusRunning, 1), treeTestJob("b", "", "", jobs.StatusDone, 2))
	m.tree = newTreeState()
	model, _ := m.update(busPingMissedMsg{Agent: "a"})
	m = model.(TuiModel)
	model, _ = m.update(busPingMissedMsg{Agent: "b"})
	m = model.(TuiModel)
	if !m.tree.isMissed("a") || m.tree.isMissed("b") {
		t.Fatalf("missed a=%v b=%v, want true and false", m.tree.isMissed("a"), m.tree.isMissed("b"))
	}
	done := treeTestJob("a", "", "", jobs.StatusDone, 1)
	m.applyJobUpdate(done)
	if m.tree.isMissed("a") {
		t.Fatal("the mark stays after the agent is done")
	}
}

// agentColour returns the ping colour of agent id in the tree of m.
func agentColour(t *testing.T, m TuiModel, id string) lipgloss.TerminalColor {
	t.Helper()
	for _, r := range m.buildTree() {
		for _, a := range r.agents {
			if a.job.ID == id {
				return a.colour
			}
		}
	}
	t.Fatalf("agent %s is not in the tree", id)
	return nil
}

// TestAppPingOK_TurnsGreen checks that a missed agent turns green on ping.ok,
// and yellow again on the next ping.missed.
func TestAppPingOK_TurnsGreen(t *testing.T) {
	m := treeModel(nil, treeTestJob("a", "", "", jobs.StatusRunning, 1))
	m.tree = newTreeState()
	model, _ := m.update(busPingMissedMsg{Agent: "a"})
	m = model.(TuiModel)
	if c := agentColour(t, m, "a"); c != lipgloss.Color("220") {
		t.Fatalf("colour = %v, want yellow 220 after ping.missed", c)
	}
	model, _ = m.update(busPingOKMsg{Agent: "a"})
	m = model.(TuiModel)
	if c := agentColour(t, m, "a"); c != lipgloss.Color("42") {
		t.Fatalf("colour = %v, want green 42 after ping.ok", c)
	}
	model, _ = m.update(busPingMissedMsg{Agent: "a"})
	m = model.(TuiModel)
	if c := agentColour(t, m, "a"); c != lipgloss.Color("220") {
		t.Fatalf("colour = %v, want yellow 220 after a second ping.missed", c)
	}
}

// TestAppMissedPing_WaitingAnswerClears checks that a missed agent that waits
// for an answer is not yellow, and that it is green again once it runs.
func TestAppMissedPing_WaitingAnswerClears(t *testing.T) {
	m := treeModel(nil, treeTestJob("a", "", "", jobs.StatusRunning, 1))
	m.tree = newTreeState()
	model, _ := m.update(busPingMissedMsg{Agent: "a"})
	m = model.(TuiModel)
	m.applyJobUpdate(treeTestJob("a", "", "", jobs.StatusWaitingAnswer, 1))
	if c := agentColour(t, m, "a"); c != lipgloss.Color("42") {
		t.Fatalf("colour = %v, want green 42 while waiting for an answer", c)
	}
	m.applyJobUpdate(treeTestJob("a", "", "", jobs.StatusRunning, 1))
	if c := agentColour(t, m, "a"); c != lipgloss.Color("42") {
		t.Fatalf("colour = %v, want green 42 after the answer", c)
	}
}

func treeRunsModel() TuiModel {
	ask := treeTestJob("b", "#1", "", jobs.StatusWaitingAnswer, 2)
	ask.Progress = "which branch?\x1b[31m\nnext line of the question that is long enough to be cut"
	long := treeTestJob("a", "#1", "", jobs.StatusRunning, 1)
	long.Description = "a worker with a very long description \t and\x1b]0;title\x07 control codes"
	m := treeModel([]TuiRunRow{treeTestRun("#1", "running", 1)}, long, ask)
	m.tree = newTreeState()
	m.tree.markMissed("a", true)
	m.sidebarRunsExpanded = map[string]bool{"#1": true}
	return m
}

// TestRunsTab_RowsFitWidth checks that every line of the Runs tab, with an
// expanded run and agents, fits the sidebar width.
func TestRunsTab_RowsFitWidth(t *testing.T) {
	m := treeRunsModel()
	for w := 10; w <= 80; w++ {
		for i, line := range m.renderSidebarRuns(w) {
			if got := lipgloss.Width(line); got > w {
				t.Fatalf("width %d: line %d is %d cells: %q", w, i, got, line)
			}
		}
	}
}

func TestRunsTab_AgentShowsQuestion(t *testing.T) {
	got := strings.Join(treeRunsModel().renderSidebarRuns(200), "\n")
	if !strings.Contains(got, "asks: which branch?") {
		t.Fatalf("runs tab has no question:\n%s", got)
	}
	if strings.Contains(got, "\x1b]") || strings.Contains(got, "\t") {
		t.Fatalf("runs tab shows control codes:\n%s", got)
	}
}

// TestRunsTab_AdHocWithoutRunSource checks that the Runs tab shows an ad-hoc
// agent with no run source, and no "not wired up" note.
func TestRunsTab_AdHocWithoutRunSource(t *testing.T) {
	m := treeModel(nil, treeTestJob("x", "", "", jobs.StatusRunning, 1))
	got := strings.Join(m.renderSidebarRuns(60), "\n")
	if !strings.Contains(got, "ad-hoc") {
		t.Fatalf("runs tab has no ad-hoc row:\n%s", got)
	}
	m.sidebarRunsExpanded = map[string]bool{treeAdHocID: true}
	got = strings.Join(m.renderSidebarRuns(60), "\n")
	if !strings.Contains(got, "worker x") || strings.Contains(got, "not wired up") {
		t.Fatalf("runs tab must show the ad-hoc agent and no error:\n%s", got)
	}
}

// TestRunsTab_CursorUsesDrawnRows checks that the cursor, Enter and the stop
// key use the rows that are drawn: 8 finished runs and one ad-hoc agent give
// 6 rows, and Enter on the last row opens the ad-hoc agents.
func TestRunsTab_CursorUsesDrawnRows(t *testing.T) {
	var runs []TuiRunRow
	for i := 1; i <= 8; i++ {
		runs = append(runs, treeTestRun(fmt.Sprintf("#%d", i), "done", i))
	}
	m := treeModel(runs, treeTestJob("x", "", "", jobs.StatusRunning, 1))
	m.sidebarTab = sidebarTabRuns
	if got := m.sidebarRowCount(); got != treeFinishedRuns+1 {
		t.Fatalf("row count = %d, want %d drawn rows", got, treeFinishedRuns+1)
	}
	for range 20 {
		m.sidebarMoveCursor(1)
	}
	if m.sidebarCursor != treeFinishedRuns {
		t.Fatalf("cursor = %d, want the ad-hoc row %d", m.sidebarCursor, treeFinishedRuns)
	}
	model, _ := m.sidebarActivateRow()
	m = model.(TuiModel)
	if !m.sidebarRunsExpanded[treeAdHocID] || m.sidebarRunsExpanded["#5"] || len(m.sidebarRunsExpanded) != 1 {
		t.Fatalf("Enter must toggle the ad-hoc row only: %v", m.sidebarRunsExpanded)
	}
	if got := strings.Join(m.renderSidebarRuns(60), "\n"); !strings.Contains(got, "worker x") {
		t.Fatalf("ad-hoc agent not drawn after Enter:\n%s", got)
	}
}

// TestRunsTab_StopKeyNotOnAdHoc checks that the stop key does nothing on the
// ad-hoc row, even when its agents are live.
func TestRunsTab_StopKeyNotOnAdHoc(t *testing.T) {
	m := treeModel([]TuiRunRow{treeTestRun("#1", "running", 1)}, treeTestJob("x", "", "", jobs.StatusRunning, 1))
	m.sidebarTab = sidebarTabRuns
	m.sidebarCursor = 1
	m = m.sidebarAskStop()
	if m.sidebarStopRun != "" {
		t.Fatalf("stop asked on the ad-hoc row: %q", m.sidebarStopRun)
	}
}

// TestRunsTab_CostBoundedByVisibleRuns checks that the Runs tab does not grow
// with the old finished runs: only treeFinishedRuns of them are laid out.
func TestRunsTab_CostBoundedByVisibleRuns(t *testing.T) {
	lines := func(n int) int {
		var runs []TuiRunRow
		for i := range n {
			runs = append(runs, treeTestRun(fmt.Sprintf("#%d", i), "done", i))
		}
		return len(treeModel(runs).runsTab(40).lines)
	}
	if small, large := lines(10), lines(2000); small != large {
		t.Fatalf("lines with 10 finished runs = %d, with 2000 = %d; want equal", small, large)
	}
}

// runsFollowModel returns a Runs tab over the runs in *rows. A test changes
// *rows to move a run, the way a run that finishes changes the list.
func runsFollowModel(rows *[]TuiRunRow) TuiModel {
	m := newTestModelForSidebar()
	m.backgroundJobs = map[string]jobs.Job{}
	m.runLister = func() []TuiRunRow { return *rows }
	m.openSidebar(sidebarTabRuns)
	return m
}

// selectedRunRow returns the ID of the run and the ID of the agent under the
// Runs cursor. The agent ID is empty on a run row.
func selectedRunRow(t *testing.T, m TuiModel) (runID, agentID string) {
	t.Helper()
	tree, it, ok := m.runsCursorItem()
	if !ok {
		t.Fatalf("cursor %d is on no Runs row", m.sidebarCursor)
	}
	if it.agent >= 0 {
		agentID = tree.agents[it.agent].job.ID
	}
	return tree.run.ID, agentID
}

// tickRuns sends one status tick, the message that re-finds the Runs cursor.
func tickRuns(t *testing.T, m TuiModel) TuiModel {
	t.Helper()
	model, _ := m.Update(statusTickMsg{})
	return model.(TuiModel)
}

// TestRunsTab_CursorFollowsRunWhenOrderChanges checks that the cursor stays on
// the same run when another run finishes and the rows reorder.
func TestRunsTab_CursorFollowsRunWhenOrderChanges(t *testing.T) {
	base := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	rows := []TuiRunRow{
		{ID: "a", Issue: 1, Status: "running", State: "code", Role: "worker", Started: base, Since: base},
		{ID: "b", Issue: 2, Status: "running", State: "code", Role: "worker", Started: base.Add(time.Minute), Since: base.Add(time.Minute)},
	}
	m := runsFollowModel(&rows)
	// Active runs are listed newest start first: b is row 0, a is row 1.
	m.sidebarMoveCursor(1)
	if id, _ := selectedRunRow(t, m); id != "a" {
		t.Fatalf("selected %q, want a", id)
	}
	// b finishes and moves below a, so a is now row 0.
	rows[1] = TuiRunRow{ID: "b", Issue: 2, Status: "done", State: "end", Result: "merged",
		Started: base.Add(time.Minute), Ended: base.Add(2 * time.Minute), Took: time.Minute}
	m = tickRuns(t, m)
	if id, _ := selectedRunRow(t, m); id != "a" {
		t.Fatalf("after b finished: selected %q, want a", id)
	}
	if m.sidebarCursor != 0 {
		t.Fatalf("cursor = %d, want 0, the new row of a", m.sidebarCursor)
	}
}

// TestRunsTab_CursorStaysInRangeWhenRunGone checks that the cursor is clamped
// to a valid row when the selected run leaves the list.
func TestRunsTab_CursorStaysInRangeWhenRunGone(t *testing.T) {
	base := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	rows := []TuiRunRow{
		{ID: "a", Issue: 1, Status: "running", State: "code", Role: "worker", Started: base, Since: base},
		{ID: "b", Issue: 2, Status: "running", State: "code", Role: "worker", Started: base.Add(time.Minute), Since: base.Add(time.Minute)},
	}
	m := runsFollowModel(&rows)
	m.sidebarMoveCursor(1)
	// a leaves the list. Only b is left, and the cursor was on row 1.
	rows = rows[1:]
	m = tickRuns(t, m)
	if n := m.sidebarRowCount(); m.sidebarCursor < 0 || m.sidebarCursor >= n {
		t.Fatalf("cursor %d is out of range for %d rows", m.sidebarCursor, n)
	}
	if id, _ := selectedRunRow(t, m); id != "b" {
		t.Fatalf("selected %q, want b, the only row left", id)
	}
	// With no run left, the cursor is 0 and nothing is selected.
	rows = nil
	m = tickRuns(t, m)
	if m.sidebarCursor != 0 {
		t.Fatalf("cursor = %d with no runs, want 0", m.sidebarCursor)
	}
}

// TestRunsTab_CursorFollowsAgentRow checks that the cursor stays on an agent
// row under an expanded run when the runs reorder.
func TestRunsTab_CursorFollowsAgentRow(t *testing.T) {
	base := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	rows := []TuiRunRow{
		{ID: "a", Issue: 1, Status: "running", State: "code", Role: "worker", Started: base, Since: base},
		{ID: "b", Issue: 2, Status: "running", State: "code", Role: "worker", Started: base.Add(time.Minute), Since: base.Add(time.Minute)},
	}
	m := runsFollowModel(&rows)
	m.backgroundJobs["w1"] = treeTestJob("w1", "a", "", jobs.StatusRunning, 1)
	m.sidebarRunsExpanded = map[string]bool{"a": true}
	// Rows: b, a, the agent w1 under a. The cursor goes to w1.
	m.sidebarMoveCursor(2)
	if _, agent := selectedRunRow(t, m); agent != "w1" {
		t.Fatalf("selected agent %q, want w1", agent)
	}
	// b finishes. Rows: a, w1, b. w1 is now row 1.
	rows[1] = TuiRunRow{ID: "b", Issue: 2, Status: "done", State: "end", Result: "merged",
		Started: base.Add(time.Minute), Ended: base.Add(2 * time.Minute), Took: time.Minute}
	m = tickRuns(t, m)
	if run, agent := selectedRunRow(t, m); run != "a" || agent != "w1" {
		t.Fatalf("after b finished: selected run %q agent %q, want a and w1", run, agent)
	}
	if m.sidebarCursor != 1 {
		t.Fatalf("cursor = %d, want 1, the new row of w1", m.sidebarCursor)
	}
}
