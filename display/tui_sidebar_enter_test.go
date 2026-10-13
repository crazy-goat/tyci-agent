package display

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/crazy-goat/tyci-agent/jobs"
)

// Enter on a Tasks row that opens the job result modal must keep the
// sidebar open, with its focus and cursor unchanged. Only the sidebar
// shortcut (Ctrl+T) may close the sidebar.

func TestSidebarEnter_BashRowKeepsSidebarOpen(t *testing.T) {
	m := newTestModelForSidebar()
	m.applyJobUpdate(jobs.Job{ID: "bash-1", Kind: jobs.KindBash, Status: jobs.StatusDone, Description: "ls", StartedAt: time.Now()})
	m.openSidebar(sidebarTabTasks)
	selectTaskRow(t, &m, func(r sidebarTaskRow) bool { return r.job != nil && r.job.Kind == jobs.KindBash })
	m.sidebarFocused = true
	cursor := m.sidebarCursor

	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	if !m2.subagentModalActive {
		t.Fatal("expected the job result modal for a Bash row")
	}
	if !m2.sidebarActive {
		t.Fatal("Enter on a Bash row must not close the sidebar")
	}
	if !m2.sidebarFocused {
		t.Fatal("Enter on a Bash row must keep the sidebar focused")
	}
	if m2.sidebarCursor != cursor {
		t.Fatalf("expected cursor %d to stay, got %d", cursor, m2.sidebarCursor)
	}
}

func TestSidebarEnter_SubagentWithoutTranscriptKeepsSidebarOpen(t *testing.T) {
	m := newTestModelForSidebar()
	m.applyJobUpdate(jobs.Job{ID: "agent-none", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "old task", StartedAt: time.Now()})
	m.openSidebar(sidebarTabTasks)
	selectTaskRow(t, &m, func(r sidebarTaskRow) bool { return r.subagent })
	m.sidebarFocused = true

	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	if !m2.subagentModalActive {
		t.Fatal("expected the job result modal")
	}
	if !m2.sidebarActive {
		t.Fatal("Enter on a subagent row without a transcript must not close the sidebar")
	}
}

// Esc on the open result modal closes only the modal. The sidebar keeps
// focus, so the key must not reach the sidebar while the modal is open.
func TestSidebarEnter_EscClosesResultModalOnly(t *testing.T) {
	m := newTestModelForSidebar()
	m.applyJobUpdate(jobs.Job{ID: "bash-1", Kind: jobs.KindBash, Status: jobs.StatusDone, Description: "ls", StartedAt: time.Now()})
	m.openSidebar(sidebarTabTasks)
	selectTaskRow(t, &m, func(r sidebarTaskRow) bool { return r.job != nil && r.job.Kind == jobs.KindBash })
	m.sidebarFocused = true

	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)

	model, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m3 := model.(TuiModel)
	if m3.subagentModalActive {
		t.Fatal("expected Esc to close the result modal")
	}
	if !m3.sidebarActive {
		t.Fatal("Esc on the result modal must not close the sidebar")
	}
	if !m3.sidebarFocused {
		t.Fatal("Esc on the result modal must keep the sidebar focused")
	}
}

// A refused Enter on the Sessions tab leaves the sidebar open.
func TestSidebarEnter_SessionsRefusalKeepsSidebarOpen(t *testing.T) {
	m := newTestModelForSidebar()
	m.reading = false // a turn is in flight
	m.openSidebar(sidebarTabSessions)

	model, _ := m.sidebarSubmitResume()
	m2 := model.(TuiModel)
	if !m2.sidebarActive {
		t.Fatal("a refused /resume must not close the sidebar")
	}
	if m2.statusMessage == "" {
		t.Fatal("expected a status message explaining the refusal")
	}
}

func TestSidebarEnter_SessionsInputRefusalKeepsSidebarOpen(t *testing.T) {
	m := newTestModelForSidebar()
	m.reading = true
	m.input.SetValue("a half-written message")
	m.openSidebar(sidebarTabSessions)

	model, _ := m.sidebarSubmitResume()
	m2 := model.(TuiModel)
	if !m2.sidebarActive {
		t.Fatal("a refused /resume must not close the sidebar")
	}
}
