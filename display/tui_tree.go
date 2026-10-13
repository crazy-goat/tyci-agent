package display

import (
	"sort"
	"strings"

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
func (m *TuiModel) buildTree() []treeRun {
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
func (m *TuiModel) runRowsCapped() []TuiRunRow {
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

// treeAgents turns the jobs of one run into rows in the order of the Subagents
// tree: each agent follows the job it continues or its parent, depth-first.
func (m *TuiModel) treeAgents(jobsOfRun []jobs.Job) []treeAgent {
	if len(jobsOfRun) == 0 {
		return nil
	}
	steps, _ := orderSubagentTree(jobsOfRun, m.resumedFrom)
	out := make([]treeAgent, 0, len(steps))
	for _, s := range steps {
		out = append(out, treeAgent{job: s.job, depth: s.depth, colour: pingColour(s.job, m.tree.isMissed(s.job.ID))})
	}
	return out
}

// pingColour returns the colour of an agent row. A failed job is red, a done
// job is grey, a running job after ping.missed is yellow and any other running
// job is green. A job with no status yet has no colour.
func pingColour(j jobs.Job, missed bool) lipgloss.TerminalColor {
	switch {
	case j.Status == jobs.StatusFailed:
		return lipgloss.Color("196")
	case j.Status == jobs.StatusDone:
		return lipgloss.Color("245")
	case missed:
		return lipgloss.Color("220")
	case j.Status == jobs.StatusRunning || j.Status == jobs.StatusWaitingAnswer:
		return lipgloss.Color("42")
	default:
		return lipgloss.NoColor{}
	}
}

// treeMaxMissed caps the missed marks that treeState keeps for the pings of
// unknown jobs. A missed ping of an agent the TUI has not seen yet is kept
// until the agent shows up.
const treeMaxMissed = 256

// busPingMissedMsg reports one ping.missed alarm for the agent Agent.
type busPingMissedMsg struct {
	Agent string
}

// treeState keeps what the Runs tab learns from the bus about the agents.
// The TUI holds it by pointer, so a model copy shares it. A nil treeState
// knows no ping.missed. Only the Bubble Tea goroutine uses it, so it needs
// no lock.
type treeState struct {
	missed map[string]bool
}

func newTreeState() *treeState {
	return &treeState{missed: map[string]bool{}}
}

// markMissed records that the agent id missed its ping. known tells whether the
// TUI has the job. A ping of an unknown job is not recorded once treeMaxMissed
// entries are kept. A nil treeState records nothing.
func (t *treeState) markMissed(id string, known bool) {
	if t == nil || t.missed[id] || (!known && len(t.missed) >= treeMaxMissed) {
		return
	}
	t.missed[id] = true
}

// forget drops the missed mark of the job id. It is called when the job leaves
// the running state, or is removed from the list.
func (t *treeState) forget(id string) {
	if t != nil {
		delete(t.missed, id)
	}
}

// isMissed reports whether the agent with the job ID id was reported missed.
func (t *treeState) isMissed(id string) bool {
	return t != nil && t.missed[id]
}

// agentRowLine is the line of an agent under its run: the indent of its depth,
// the status icon, the description and, for an agent that waits for an answer,
// the question. The line is cut or padded to width and takes the ping colour.
func agentRowLine(a treeAgent, width int) string {
	icon, _ := jobStatusIcon(a.job.Status)
	text := strings.Repeat("  ", a.depth) + icon + " " + sanitizeUntrusted(a.job.Description)
	if q := askText(a.job); q != "" {
		text += " asks: " + sanitizeUntrusted(q)
	}
	return lipgloss.NewStyle().Foreground(a.colour).Render(fillWidth(text, width))
}

// askText returns the question of an agent that waits for an answer. It is
// empty for any other agent.
func askText(j jobs.Job) string {
	if j.Status != jobs.StatusWaitingAnswer {
		return ""
	}
	return j.Progress
}
