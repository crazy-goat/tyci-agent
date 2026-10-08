package display

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/tools"
)

// newAgentViewTestModel returns a model with the Tasks tab open, one running
// subagent job whose transcript holds a single text event, and the cursor on
// that job's row.
func newAgentViewTestModel(t *testing.T, jobID string) TuiModel {
	t.Helper()
	m := newTestModelForSidebar()
	m.applyJobUpdate(jobs.Job{ID: jobID, Kind: jobs.KindSubagent, Status: jobs.StatusRunning, Description: "worker task", StartedAt: time.Now()})
	tools.RecordLiveEvent(jobID, tools.LiveEvent{Kind: "text", Content: "agent says hello"})
	m.openSidebar(sidebarTabTasks)
	selectTaskRow(t, &m, func(r sidebarTaskRow) bool { return r.subagent })
	return m
}

// selectTaskRow moves the Tasks cursor to the first selectable row for which
// match returns true.
func selectTaskRow(t *testing.T, m *TuiModel, match func(sidebarTaskRow) bool) {
	t.Helper()
	width := m.sidebarLayout().contentWidth
	rows := m.sidebarTaskRows(width)
	for i, idx := range m.sidebarTaskJobRows(width) {
		if match(rows[idx]) {
			m.sidebarCursor = i
			return
		}
	}
	t.Fatal("no task row matches")
}

func TestAgentView_CursorLandsOnMainAndEnterIsNoOp(t *testing.T) {
	m := newAgentViewTestModel(t, "agent-main")
	selectTaskRow(t, &m, func(r sidebarTaskRow) bool { return r.isMain })

	model, _ := m.sidebarActivateRow()
	got := model.(TuiModel)
	if got.agentView != nil {
		t.Fatal("Enter on main while the main chat is shown must not open a view")
	}
	if !got.sidebarActive {
		t.Fatal("Enter on main must keep the sidebar open")
	}
}

func TestAgentView_EnterOnSubagentShowsItsLiveConversation(t *testing.T) {
	m := newAgentViewTestModel(t, "agent-live")
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	if m2.agentView == nil || m2.agentView.jobID != "agent-live" {
		t.Fatalf("expected the agent view for agent-live, got %+v", m2.agentView)
	}
	if !m2.sidebarActive {
		t.Fatal("the sidebar must stay open so Enter on main can bring the main conversation back")
	}
	if got := m2.agentView.model.blocks; len(got) != 1 || got[0].content != "agent says hello" {
		t.Fatalf("view blocks = %+v, want the agent's text", got)
	}
	if len(m2.blocks) != 0 {
		t.Fatalf("the main conversation must not change, got %d blocks", len(m2.blocks))
	}

	// A new event from the agent shows up on the next status tick, without
	// reopening the view.
	tools.RecordLiveEvent("agent-live", tools.LiveEvent{Kind: "text", Content: " more"})
	model, _ = m2.Update(statusTickMsg{})
	m3 := model.(TuiModel)
	if got := m3.agentView.model.blocks[0].content; got != "agent says hello more" {
		t.Fatalf("view did not follow the agent live, got %q", got)
	}
}

func TestAgentView_HeaderNamesTheViewedAgent(t *testing.T) {
	m := newAgentViewTestModel(t, "agent-header")
	m.width = 120
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	header := m2.buildTopBar()
	if !strings.Contains(header, "viewing: agent-header/worker task") || !strings.Contains(header, "Enter on main to go back") {
		t.Fatalf("header = %q, want the viewed agent and the way back", header)
	}
}

// A subagent's description is its full task text, and it can span several
// lines. The header is one row of the frame, so it must stay one line.
func TestAgentView_HeaderStaysOneLineForMultilineDescription(t *testing.T) {
	m := newTestModelForSidebar()
	m.width = 120
	m.applyJobUpdate(jobs.Job{ID: "agent-nl", Kind: jobs.KindSubagent, Status: jobs.StatusRunning, Description: "Fix bug\n\nDetails here", StartedAt: time.Now()})
	tools.RecordLiveEvent("agent-nl", tools.LiveEvent{Kind: "text", Content: "agent says hello"})
	m.openSidebar(sidebarTabTasks)
	selectTaskRow(t, &m, func(r sidebarTaskRow) bool { return r.subagent })
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	if m2.agentView == nil {
		t.Fatal("expected the agent view to open")
	}
	header := m2.agentViewHeader()
	if strings.Contains(header, "\n") {
		t.Fatalf("header has %d lines, want one: %q", strings.Count(header, "\n")+1, header)
	}
	if !strings.Contains(header, "viewing: agent-nl/Fix bug Details here") {
		t.Fatalf("header = %q, want the description with its words joined on one line", header)
	}
}

func TestAgentView_EnterOnMainRestoresMainScroll(t *testing.T) {
	m := newAgentViewTestModel(t, "agent-back")
	m.scrollLine = 7
	m.atBottom = false
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	if m2.agentView == nil {
		t.Fatal("expected the agent view to open")
	}

	selectTaskRow(t, &m2, func(r sidebarTaskRow) bool { return r.isMain })
	model, _ = m2.sidebarActivateRow()
	m3 := model.(TuiModel)
	if m3.agentView != nil {
		t.Fatal("Enter on main must switch back to the main conversation")
	}
	if m3.scrollLine != 7 || m3.atBottom {
		t.Fatalf("main scroll = %d atBottom=%v, want 7 and false", m3.scrollLine, m3.atBottom)
	}
}

func TestAgentView_EscGoesBackWithSidebarFocused(t *testing.T) {
	m := newAgentViewTestModel(t, "agent-esc-focus")
	m.scrollLine = 3
	m.atBottom = false
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	m2.sidebarFocused = true

	model, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m3 := model.(TuiModel)
	if m3.agentView != nil {
		t.Fatal("Esc with the sidebar focused must leave the agent view")
	}
	if !m3.sidebarActive {
		t.Fatal("Esc in the agent view must keep the sidebar open")
	}
	if m3.scrollLine != 3 || m3.atBottom {
		t.Fatalf("main scroll = %d atBottom=%v, want 3 and false", m3.scrollLine, m3.atBottom)
	}
}

func TestAgentView_EscGoesBackWithSidebarUnfocused(t *testing.T) {
	m := newAgentViewTestModel(t, "agent-esc-main")
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	m2.sidebarFocused = false

	model, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if model.(TuiModel).agentView != nil {
		t.Fatal("Esc with the input focused must leave the agent view")
	}
}

func TestAgentView_EscDoesNotCancelMainTurn(t *testing.T) {
	m := newAgentViewTestModel(t, "agent-esc-busy")
	cancel := make(chan struct{}, 1)
	m.cancelCh = cancel
	m.reading = false // a main turn is running
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	m2.sidebarFocused = false

	model, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if model.(TuiModel).agentView != nil {
		t.Fatal("Esc must leave the agent view while a turn runs")
	}
	if len(cancel) != 0 {
		t.Fatal("Esc in the agent view must not cancel the main turn")
	}
}

func TestAgentView_TypingGoesToMainConversation(t *testing.T) {
	m := newAgentViewTestModel(t, "agent-input")
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	m2.sidebarFocused = false
	viewBlocks := len(m2.agentView.model.blocks)

	model, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	m3 := model.(TuiModel)
	if got := m3.input.Value(); got != "hi" {
		t.Fatalf("input = %q, want the typed text in the main input", got)
	}
	if m3.agentView == nil || len(m3.agentView.model.blocks) != viewBlocks {
		t.Fatal("typing must not change the viewed agent's conversation")
	}
}

func TestAgentView_FinishedAgentKeepsFinalTranscript(t *testing.T) {
	m := newAgentViewTestModel(t, "agent-done")
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	m2.applyJobUpdate(jobs.Job{ID: "agent-done", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "worker task", StartedAt: time.Now()})

	model, _ = m2.Update(statusTickMsg{})
	m3 := model.(TuiModel)
	if m3.agentView == nil {
		t.Fatal("the view must stay open after the agent ends")
	}
	if got := m3.agentView.model.blocks; len(got) != 1 || got[0].content != "agent says hello" {
		t.Fatalf("final transcript = %+v, want the agent's text", got)
	}
}

func TestAgentView_ClosingSidebarEndsView(t *testing.T) {
	m := newAgentViewTestModel(t, "agent-close")
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	m2.closeSidebar()
	if m2.agentView != nil {
		t.Fatal("closing the sidebar must end the agent view, its way back is in the sidebar")
	}
}

func TestAgentView_SubagentWithoutTranscriptFallsBackToResultModal(t *testing.T) {
	m := newTestModelForSidebar()
	m.applyJobUpdate(jobs.Job{ID: "agent-none", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "old task", StartedAt: time.Now()})
	m.openSidebar(sidebarTabTasks)
	selectTaskRow(t, &m, func(r sidebarTaskRow) bool { return r.subagent })

	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	if m2.agentView != nil {
		t.Fatal("a job without a transcript must not open the agent view")
	}
	if !m2.subagentModalActive {
		t.Fatal("expected the job result modal")
	}
}

func TestAgentView_BashRowStillOpensResultModal(t *testing.T) {
	m := newTestModelForSidebar()
	m.applyJobUpdate(jobs.Job{ID: "bash-1", Kind: jobs.KindBash, Status: jobs.StatusDone, Description: "ls", StartedAt: time.Now()})
	m.openSidebar(sidebarTabTasks)
	selectTaskRow(t, &m, func(r sidebarTaskRow) bool { return r.job != nil && r.job.Kind == jobs.KindBash })

	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	if m2.agentView != nil {
		t.Fatal("a Bash row must not open the agent view")
	}
	if !m2.subagentModalActive {
		t.Fatal("expected the job result modal for a Bash row")
	}
}

func TestAgentView_OpeningViewArmsStatusTickWhenIdle(t *testing.T) {
	m := newTestModelForSidebar()
	m.applyJobUpdate(jobs.Job{ID: "agent-tick", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "worker task", StartedAt: time.Now()})
	tools.RecordLiveEvent("agent-tick", tools.LiveEvent{Kind: "text", Content: "done"})
	m.openSidebar(sidebarTabTasks)
	selectTaskRow(t, &m, func(r sidebarTaskRow) bool { return r.subagent })
	m.statusTickArmed = false

	_, cmd := m.sidebarActivateRow()
	if cmd == nil {
		t.Fatal("opening the view while idle must start the status tick, so the view follows the agent")
	}
}
