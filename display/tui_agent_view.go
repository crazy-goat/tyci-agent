package display

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/crazy-goat/tyci-agent/tools"
)

// agentView shows the live conversation of one subagent job in the main
// window, in place of the main conversation. The view has its own block list
// (model), built from the job's events in tools/transcript_live.go. It renders
// with the same block pipeline as the main chat.
//
// Scroll position and text selection are shared with the main model while the
// view is open, and mainScroll/mainAtBottom keep the main conversation's own
// position for the way back. The input box and the main conversation are not
// part of the view: typed text and new messages go to the main model.
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
	m.pullAgentView()
	return true
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
