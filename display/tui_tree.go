package display

import (
	"sort"

	"github.com/charmbracelet/lipgloss"
	"github.com/crazy-goat/tyci-agent/jobs"
)

// treeFinishedRuns is how many finished runs the Runs tab shows. The older
// finished runs are left out, so the tab stays short.
const treeFinishedRuns = 5

// treeAdHocID is the run ID of the synthetic row that holds the agents
// without a workflow run (jobs.Job.RunID is empty for them).
const treeAdHocID = "ad-hoc"

// treeAgent is one subagent job under a run row of the Runs tab.
type treeAgent struct {
	job   jobs.Job
	depth int // 1 for an agent of the run, 2 for its child, and so on
	// colour is the ping colour of the agent row, see pingColour.
	colour lipgloss.TerminalColor
}

// treeRun is one run row of the Runs tab with the agents that work for it.
type treeRun struct {
	run    TuiRunRow
	agents []treeAgent
}

// buildTree groups the subagent jobs under the workflow runs they work for
// (jobs.Job.RunID). The rows are in the order of sidebarRunRows: active runs
// first, then at most treeFinishedRuns finished runs. The agents without a
// run form the last row, the ad-hoc row. With no run source (runLister nil)
// only the ad-hoc row is returned, and it is empty when there are no
// ad-hoc agents.
func (m TuiModel) buildTree() []treeRun {
	byRun := map[string][]jobs.Job{}
	var adHoc []jobs.Job
	for _, j := range m.sortedBackgroundJobs() {
		if j.Kind != jobs.KindSubagent {
			continue
		}
		if j.RunID == "" {
			adHoc = append(adHoc, j)
			continue
		}
		byRun[j.RunID] = append(byRun[j.RunID], j)
	}

	var out []treeRun
	for _, r := range m.runRowsCapped() {
		out = append(out, treeRun{run: r, agents: m.treeAgents(byRun[r.ID])})
	}
	if len(adHoc) > 0 {
		out = append(out, treeRun{run: adHocRow(adHoc), agents: m.treeAgents(adHoc)})
	}
	return out
}

// runRowsCapped returns the runs of the run source in display order, with the
// finished runs cut to treeFinishedRuns. It returns nil with no run source.
func (m TuiModel) runRowsCapped() []TuiRunRow {
	if m.runLister == nil {
		return nil
	}
	var active, finished []TuiRunRow
	for _, r := range m.runLister() {
		if runFinished(r) {
			finished = append(finished, r)
		} else {
			active = append(active, r)
		}
	}
	sortRuns(active, finished)
	if len(finished) > treeFinishedRuns {
		finished = finished[:treeFinishedRuns]
	}
	return append(active, finished...)
}

// sortRuns sorts the active runs by start, newest first, and the finished
// runs by end, newest first.
func sortRuns(active, finished []TuiRunRow) {
	sort.SliceStable(active, func(i, j int) bool { return active[i].Started.After(active[j].Started) })
	sort.SliceStable(finished, func(i, j int) bool { return finished[i].Ended.After(finished[j].Ended) })
}

// adHocRow is the synthetic run row of the agents without a run. It is
// active while one of its agents is live.
func adHocRow(agents []jobs.Job) TuiRunRow {
	r := TuiRunRow{ID: treeAdHocID, Status: "done"}
	for _, j := range agents {
		if subagentLive(j.Status) {
			r.Status = "running"
		}
		if r.Started.IsZero() || j.StartedAt.Before(r.Started) {
			r.Started = j.StartedAt
		}
	}
	return r
}

// treeAgents turns the jobs of one run into rows. The depth follows the
// ParentID chain inside the run. The order of jobs is kept.
func (m TuiModel) treeAgents(jobsOfRun []jobs.Job) []treeAgent {
	if len(jobsOfRun) == 0 {
		return nil
	}
	parent := map[string]string{}
	for _, j := range jobsOfRun {
		parent[j.ID] = j.ParentID
	}
	out := make([]treeAgent, 0, len(jobsOfRun))
	for _, j := range jobsOfRun {
		depth := 1
		for id, guard := parent[j.ID], 0; id != "" && guard < len(jobsOfRun); guard++ {
			p, inRun := parent[id]
			if !inRun {
				break
			}
			depth++
			id = p
		}
		out = append(out, treeAgent{job: j, depth: depth, colour: pingColour(j, m.tree.isMissed(j.ID))})
	}
	return out
}

// pingColour returns the colour of an agent row. A failed job is red, a done
// job is grey, a job after ping.missed is yellow. Any other job has no colour.
func pingColour(j jobs.Job, missed bool) lipgloss.TerminalColor {
	switch {
	case j.Status == jobs.StatusFailed:
		return lipgloss.Color("196")
	case j.Status == jobs.StatusDone:
		return lipgloss.Color("245")
	case missed:
		return lipgloss.Color("220")
	default:
		return lipgloss.NoColor{}
	}
}

// treeState keeps what the Runs tab learns from the bus about the agents.
// The TUI holds it by pointer, so a model copy shares it. A nil treeState
// knows no ping.missed.
type treeState struct {
	missed map[string]bool
}

// isMissed reports whether the agent with the job ID id was reported missed.
func (t *treeState) isMissed(id string) bool {
	return t != nil && t.missed[id]
}

// askText returns the question of an agent that waits for an answer. It is
// empty for any other agent.
func askText(j jobs.Job) string {
	if j.Status != jobs.StatusWaitingAnswer {
		return ""
	}
	return j.Progress
}
