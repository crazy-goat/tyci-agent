package display

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/crazy-goat/tyci-agent/internal/ledger"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/tools"
)

// agentView shows the live conversation of one subagent job in the main
// window, in place of the main conversation. The view has its own block list
// (model), built from the job's events in tools/transcript_live.go. It renders
// with the same block pipeline as the main chat.
//
// Scroll position and text selection are shared with the main model while the
// view is open, and mainScroll/mainAtBottom keep the main conversation's own
// position for the way back. The input box belongs to the view: Enter sends the
// text to the viewed agent (see tui_agent_input.go). The main conversation is
// not part of the view.
type agentView struct {
	jobID string
	// label is the job's description (for a workflow worker, its role name).
	// The header shows it after the job id.
	label string
	// model holds the blocks and caches of the viewed conversation. Its
	// render buffer is the main model's, so mouse hit-testing and copy act on
	// the rows on screen.
	model *TuiModel
	// seen is how many events of the job were already replayed into model.
	seen int
	// mainScroll and mainAtBottom are the main conversation's scroll position
	// from before the view opened.
	mainScroll   int
	mainAtBottom bool
	// confirming is true after Enter on a finished agent: the next Enter
	// resumes it with confirmText, any other key cancels.
	confirming  bool
	confirmText string
	// checking is true while the resume check of a finished agent runs: an
	// Enter then does nothing, so a second Enter starts no second check.
	checking bool
}

// newAgentViewModel returns an empty block model with its own caches. It is
// not newModel: newModel loads the input history, and a view needs none of it.
func newAgentViewModel() *TuiModel {
	return &TuiModel{
		blocks:                make([]block, 0, 256),
		toolQueue:             make([]int, 0, 16),
		atBottom:              true,
		subagentToolIdx:       -1,
		subagentModalBlockIdx: -1,
		dirtyBlocks:           make(map[int]bool),
		mdCacheRendered:       make(map[int]string),
		streamWraps:           make(map[int]*streamWrap),
		mdStreamState:         make(map[int]*mdStreamState),
		toolDisplayCache:      make(map[int]string),
		cachedTotalLines:      -1,
		scrollback:            &scrollbackCache{},
		messageRegion:         &messageRegionCache{},
		renderBuffer:          &RenderBuffer{},
		modalRenderBuffer:     &RenderBuffer{},
	}
}

// openAgentView switches the main window to the live conversation of jobID.
// label is the job's description, shown in the header. It returns false, and
// changes nothing, when the job has no transcript. Opening a second agent
// while one is shown replaces the first view; the main conversation's saved
// position is kept.
func (m *TuiModel) openAgentView(jobID, label string) bool {
	if !tools.HasLiveTranscript(jobID) {
		return false
	}
	m.showAgentView(jobID, label)
	return true
}

// showAgentView is openAgentView without the transcript check. A job that was
// just resumed may not have its transcript yet: its goroutine creates it, and
// pullAgentView fills the view on the next status tick.
func (m *TuiModel) showAgentView(jobID, label string) {
	av := &agentView{jobID: jobID, label: label, model: newAgentViewModel()}
	if old := m.agentView; old != nil {
		av.mainScroll, av.mainAtBottom = old.mainScroll, old.mainAtBottom
		old.model.scrollback.close()
	} else {
		av.mainScroll, av.mainAtBottom = m.scrollLine, m.atBottom
	}
	m.agentView = av
	m.scrollLine = 0
	m.atBottom = true
	m.selectionVersion++
	m.selection = SelectionState{}
	m.selectionFlash = false
	m.invalidateMessageRegion()
	m.input.Placeholder = agentViewPlaceholder(jobID, label)
	m.pullAgentView()
	// The catch-up replay arrives in one go. Counting it would show the
	// throughput of the whole transcript over a fraction of a second, so only
	// the deltas that arrive after the view opened count as this round's.
	av.model.roundBytes = 0
	av.model.roundFirstDeltaAt = time.Time{}
}

// agentViewPlaceholder is the input hint while an agent view is open. It says
// who receives the text, for example "message to 20261008-125053/review".
func agentViewPlaceholder(jobID, label string) string {
	target := jobID
	if label = strings.Join(strings.Fields(label), " "); label != "" {
		target += "/" + label
	}
	return "message to " + target + " (Esc: back to main)"
}

// closeAgentView switches the main window back to the main conversation and
// restores its scroll position. It does nothing when no view is open.
func (m *TuiModel) closeAgentView() {
	av := m.agentView
	if av == nil {
		return
	}
	av.model.scrollback.close()
	m.agentView = nil
	m.input.Placeholder = inputPlaceholder
	m.scrollLine = av.mainScroll
	m.atBottom = av.mainAtBottom
	m.selectionVersion++
	m.selection = SelectionState{}
	m.selectionFlash = false
	m.invalidateMessageRegion()
}

// pullAgentView replays the job's events that the view has not shown yet. It
// runs on every status tick while a view is open, so the view follows the
// agent live. A finished job has no new events, so the view keeps its final
// transcript.
func (m *TuiModel) pullAgentView() {
	av := m.agentView
	if av == nil {
		return
	}
	events, _ := tools.LiveTranscriptSince(av.jobID, av.seen)
	if len(events) == 0 {
		return
	}
	for _, ev := range events {
		av.model.handleBlockMsg(tuiMsgBlock{kind: ev.Kind, content: ev.Content, toolName: ev.ToolName})
	}
	av.seen += len(events)
	av.model.invalidateMessageRegion()
}

// shownModel returns the model whose blocks are on screen: the agent view
// while one is open, otherwise m itself.
func (m *TuiModel) shownModel() *TuiModel {
	if m.agentView != nil {
		return m.agentView.model
	}
	return m
}

// agentViewRegion renders the message region of the open agent view. Scroll
// and selection state live on the main model, so they are copied into the
// view before it renders.
func (m *TuiModel) agentViewRegion(msgHeight int) string {
	v := m.agentView.model
	if w := m.renderWidth(); v.width != w {
		v.width = w
		v.invalidateAllBlockLineCounts()
		v.invalidateTotalLines()
	}
	v.scrollLine = m.scrollLine
	v.atBottom = m.atBottom
	v.selection = m.selection
	v.selectionVersion = m.selectionVersion
	v.selectionFlash = m.selectionFlash
	v.renderBuffer = m.renderBuffer
	return v.buildMessageRegionCached(msgHeight)
}

// agentViewHeader is the top line while an agent view is open. It names the
// viewed job, so the human knows the main window shows another conversation.
// The header is one row of the frame, so the label is joined into one line
// before it is cut.
func (m TuiModel) agentViewHeader() string {
	name := m.agentView.jobID
	if label := strings.Join(strings.Fields(m.agentView.label), " "); label != "" {
		name += "/" + truncateString(label, 40)
	}
	text := "viewing: " + name + " — Enter on main to go back"
	return lipgloss.NewStyle().MaxWidth(m.width).Render(text)
}

// viewedAgentRow returns the Subagents row of the job the agent view shows.
// The row is built by buildSubagentTree, the same source as the Tasks tab, so
// its figures agree with the row. ok is false when the job is not tracked.
func (m TuiModel) viewedAgentRow() (subagentTreeRow, bool) {
	for _, row := range m.buildSubagentTree() {
		if !row.isRoot && row.job.ID == m.agentView.jobID {
			return row, true
		}
	}
	return subagentTreeRow{}, false
}

// viewedAgentModel returns "provider/model" of the viewed subagent. The job
// does not record its model, so the ledger is the source. It is empty until
// the job has made its first call. When the job fell back to another model,
// the row seen last is the one it moved to.
func viewedAgentModel(jobID string) string {
	name := ""
	for _, r := range ledger.Get().Rows {
		if r.JobID == jobID && r.Kind == ledger.Subagent {
			name = r.Provider + "/" + r.Model
		}
	}
	return name
}

// viewedAgentState is the state part of the status bar while an agent view is
// open. A running agent shows the state its transcript has reached. An agent
// that ended shows how it ended. An agent that is no longer tracked shows
// nothing, because its transcript cannot tell whether it still runs.
func (m TuiModel) viewedAgentState() string {
	row, ok := m.viewedAgentRow()
	if !ok {
		return ""
	}
	switch row.job.Status {
	case jobs.StatusRunning:
		// The replayed transcript has no request-start event, so the view
		// may have no start time yet. The job's own start stands in for it.
		v := *m.agentView.model
		if v.requestStartTime.IsZero() {
			v.requestStartTime = row.job.StartedAt
		}
		// A tool that ended leaves the status at "tool". The replay has no event
		// for the next request, so the agent waits for its response.
		if v.status == "tool" && len(v.toolQueue) == 0 {
			v.status = "waiting"
		}
		return v.liveStatus()
	case jobs.StatusWaitingAnswer:
		return "waiting for answer"
	case jobs.StatusDone:
		return "done"
	case jobs.StatusFailed:
		return "failed"
	case jobs.StatusTruncated:
		return "truncated"
	}
	return ""
}

// buildAgentViewRight is the right-hand side of the status bar while an agent
// view is open. It shows the viewed agent's tokens and cost, as its row in the
// Subagents list shows them, and then the session total. The total is the
// first item, so fitStatusRight keeps it when the bar is narrow. The agent's
// figures are left out until it has used tokens.
func (m TuiModel) buildAgentViewRight() string {
	parts := []string{"total " + fmtUSD(ledger.Get().TotalUSD()) + "$"}
	if row, ok := m.viewedAgentRow(); ok && row.ownTokens > 0 {
		parts = append(parts, fmt.Sprintf("%s tok, %s$", fmtTokens(row.ownTokens), fmtUSD(row.rollupUSD)))
	}
	return fitStatusRight(strings.Join(parts, statusSep), statusRightBudget(m.width))
}
