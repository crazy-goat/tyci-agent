package display

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crazy-goat/tyci-agent/jobs"
)

// agentLogMsg carries one read of an agent's transcript. running is the
// job status seen before the read started, so a read that follows the
// job's end is the last one.
type agentLogMsg struct {
	jobID   string
	lines   []string
	running bool
}

// openAgentLog shows the transcript of one agent in the subagent modal. The
// title is "<run> / <agent>". An agent without a transcript (an ad-hoc
// agent) shows "(no log yet)" and is not tailed. Otherwise the first read
// runs in a tea.Cmd, and the status tick keeps it live while the agent runs.
func (m *TuiModel) openAgentLog(run TuiRunRow, a treeAgent) (tea.Model, tea.Cmd) {
	m.savedScrollLine = m.scrollLine
	m.savedAtBottom = m.atBottom
	m.subagentModalActive = true
	m.subagentModalBlockIdx = -1
	m.subagentModalScroll = 0
	m.subagentModalTitle = truncateString(run.ID+" / "+a.job.Description, 80)
	m.agentLogJob = a.job.ID
	m.agentLogPath = a.job.Transcript
	m.agentLogReading = false
	if m.agentLogPath == "" {
		m.subagentModalStaticText = "(no log yet)"
		m.subagentModalDone = true
		return *m, nil
	}
	m.subagentModalStaticText = ""
	m.subagentModalDone = a.job.Status != jobs.StatusRunning
	m.agentLogReading = true
	return *m, tea.Batch(m.agentLogCmd(), m.armStatusTick())
}

// agentLogTailing reports whether the open modal still follows a running
// agent's transcript.
func (m TuiModel) agentLogTailing() bool {
	return m.subagentModalActive && m.agentLogJob != "" && m.agentLogPath != "" && !m.subagentModalDone
}

// agentLogTick starts one read when the modal tails a running agent and no
// read is in flight. The read itself runs in the returned tea.Cmd.
func (m TuiModel) agentLogTick() (TuiModel, tea.Cmd) {
	if !m.agentLogTailing() || m.agentLogReading {
		return m, nil
	}
	m.agentLogReading = true
	return m, m.agentLogCmd()
}

// agentLogCmd reads the transcript off the Update loop. The status is taken
// before the read, see agentLogMsg.
func (m TuiModel) agentLogCmd() tea.Cmd {
	id, path := m.agentLogJob, m.agentLogPath
	running := m.agentLogJobRunning()
	return func() tea.Msg {
		return agentLogMsg{jobID: id, lines: loadAgentLog(path), running: running}
	}
}

// agentLogJobRunning looks the open agent up in the Runs tree. An agent that
// is no longer in the tree counts as finished.
func (m TuiModel) agentLogJobRunning() bool {
	for _, r := range (&m).buildTree() {
		for _, a := range r.agents {
			if a.job.ID == m.agentLogJob {
				return a.job.Status == jobs.StatusRunning
			}
		}
	}
	return false
}

// applyAgentLog puts a finished read into the modal. A read for another
// agent, or one that arrives after the modal closed, is dropped. A scrolled
// view keeps its place: the scroll counts from the bottom, so the growth of
// the text is added to the offset.
func (m TuiModel) applyAgentLog(msg agentLogMsg) TuiModel {
	m.agentLogReading = false
	if !m.subagentModalActive || msg.jobID != m.agentLogJob {
		return m
	}
	text := strings.Join(msg.lines, "\n")
	m.subagentModalDone = !msg.running
	if text == m.subagentModalStaticText {
		return m
	}
	before := m.subagentModalLineCount()
	m.subagentModalStaticText = text
	if m.subagentModalScroll > 0 {
		m.subagentModalScroll += m.subagentModalLineCount() - before
	}
	m.clampSubagentModalScroll()
	return m
}
