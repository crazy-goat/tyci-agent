package display

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/crazy-goat/tyci-agent/jobs"
)

// AgentInput is the app side of the input of the agent view. The methods run
// on a tea.Cmd goroutine, never in Update, because they publish on the bus and
// the bus reaches the TUI through the program.
type AgentInput interface {
	// Post puts text into the mailbox of the running agent jobID.
	Post(jobID, text string) error
	// ResumeCheck returns the context size in tokens and the estimated cost of
	// resuming the finished agent jobID. err is the reason it cannot resume.
	ResumeCheck(jobID string) (tokens int, usd float64, err error)
	// Resume starts a new job that continues the finished agent jobID with
	// text, and returns the id of the new job.
	Resume(jobID, text string) (newJobID string, err error)
}

// tuiSetAgentInputMsg delivers the AgentInput (see TUI.SetAgentInput).
type tuiSetAgentInputMsg struct {
	in AgentInput
}

// agentPostDoneMsg is the result of AgentInput.Post. text is the sent text, so
// it can be put back into the input after a failure.
type agentPostDoneMsg struct {
	jobID, text string
	err         error
}

// agentResumeDoneMsg is the result of AgentInput.Resume.
type agentResumeDoneMsg struct {
	oldID, label, text string
	newID              string
	err                error
}

// SetAgentInput gives the agent view the app side of its input. Call it once,
// before the first user input.
func (t *TUI) SetAgentInput(in AgentInput) {
	t.prog.Send(tuiSetAgentInputMsg{in: in})
}

// handleAgentViewKey handles the keys that the open agent view takes for the
// viewed agent: Enter sends the input, and Enter again confirms a resume. It
// returns handled false when the key is for the usual handling. Ctrl+C is never
// taken, so the quit key always works.
func (m TuiModel) handleAgentViewKey(msg tea.KeyMsg) (bool, TuiModel, tea.Cmd) {
	av := m.agentView
	if msg.Type == tea.KeyCtrlC {
		return false, m, nil
	}
	if av.confirming {
		if msg.Type == tea.KeyEnter && !msg.Alt {
			next, cmd := m.confirmAgentResume()
			return true, next, cmd
		}
		// Any other key cancels the resume. The key is used up, so Esc does
		// not also close the view.
		av.confirming = false
		av.confirmText = ""
		m.agentViewNotice("block", "Resume cancelled.")
		return true, m, nil
	}
	if msg.Type != tea.KeyEnter || msg.Alt {
		return false, m, nil
	}
	next, cmd := m.sendAgentViewInput()
	return true, next, cmd
}

// sendAgentViewInput sends the input to the viewed agent. A running agent gets
// the text as a message. A finished agent is resumed only after a second Enter,
// so the first Enter shows the context size and the estimated cost.
func (m TuiModel) sendAgentViewInput() (TuiModel, tea.Cmd) {
	av := m.agentView
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	row, ok := m.viewedAgentRow()
	if !ok {
		m.agentViewNotice("error", "the agent is not tracked, so it cannot get the message")
		return m, nil
	}
	if m.agentInput == nil {
		m.agentViewNotice("error", "the agent input is not connected")
		return m, nil
	}
	in, jobID := m.agentInput, av.jobID
	switch row.job.Status {
	case jobs.StatusRunning, jobs.StatusWaitingAnswer:
		m.resetInput()
		return m, func() tea.Msg {
			return agentPostDoneMsg{jobID: jobID, text: text, err: in.Post(jobID, text)}
		}
	}
	tokens, usd, err := in.ResumeCheck(jobID)
	if err != nil {
		m.agentViewNotice("error", "cannot resume "+jobID+": "+err.Error())
		return m, nil
	}
	av.confirming = true
	av.confirmText = text
	m.agentViewNotice("block", fmt.Sprintf(
		"Resume %s: context about %s tok, estimated cost about $%s. "+
			"The workflow run does not read the reply. Press Enter again to resume; any other key cancels.",
		jobID, fmtTokens(tokens), fmtUSD(usd)))
	return m, nil
}

// confirmAgentResume resumes the viewed agent with the text that was confirmed.
func (m TuiModel) confirmAgentResume() (TuiModel, tea.Cmd) {
	av := m.agentView
	text := av.confirmText
	av.confirming = false
	av.confirmText = ""
	m.resetInput()
	in, jobID, label := m.agentInput, av.jobID, av.label
	return m, func() tea.Msg {
		newID, err := in.Resume(jobID, text)
		return agentResumeDoneMsg{oldID: jobID, label: label, text: text, newID: newID, err: err}
	}
}

// handleAgentInputMsg handles the messages of the agent view input. handled is
// false for any other message.
func (m TuiModel) handleAgentInputMsg(msg tea.Msg) (next TuiModel, cmd tea.Cmd, handled bool) {
	switch msg := msg.(type) {
	case tuiSetAgentInputMsg:
		m.agentInput = msg.in
		return m, nil, true
	case agentPostDoneMsg:
		if msg.err != nil {
			m.restoreInput(msg.text)
			m.agentNotice(msg.jobID, "error", "message not sent: "+msg.err.Error())
			return m, nil, true
		}
		m.agentNotice(msg.jobID, "block", "message queued for "+msg.jobID+"; the agent reads it before its next turn")
		return m, nil, true
	case agentResumeDoneMsg:
		if msg.err != nil {
			m.restoreInput(msg.text)
			m.agentNotice(msg.oldID, "error", "resume failed: "+msg.err.Error())
			return m, nil, true
		}
		m.resumedFrom[msg.newID] = msg.oldID
		note := fmt.Sprintf("side conversation: %s continues %s. The workflow run does not read its reply.", msg.newID, msg.oldID)
		m.handleBlockMsg(tuiMsgBlock{kind: "block", content: note})
		if m.agentView != nil && m.agentView.jobID == msg.oldID {
			m.showAgentView(msg.newID, msg.label)
			m.agentViewNotice("block", note)
			return m, m.armStatusTick(), true
		}
		return m, nil, true
	}
	return m, nil, false
}

// resetInput empties the input box the way submit does.
func (m *TuiModel) resetInput() {
	m.input.Reset()
	m.input.SetHeight(1)
}

// restoreInput puts text back into an empty input box, after a failed send.
func (m *TuiModel) restoreInput(text string) {
	if m.input.Value() == "" {
		m.input.SetValue(text)
	}
}

// agentViewNotice adds a notice of kind (block or error) to the open agent view.
func (m *TuiModel) agentViewNotice(kind, text string) {
	av := m.agentView
	if av == nil {
		return
	}
	av.model.handleBlockMsg(tuiMsgBlock{kind: kind, content: text})
	av.model.invalidateMessageRegion()
}

// agentNotice shows a notice about jobID in the agent view when the view shows
// that job, and in the main conversation otherwise.
func (m *TuiModel) agentNotice(jobID, kind, text string) {
	if m.agentView != nil && m.agentView.jobID == jobID {
		m.agentViewNotice(kind, text)
		return
	}
	m.handleBlockMsg(tuiMsgBlock{kind: kind, content: text})
}
