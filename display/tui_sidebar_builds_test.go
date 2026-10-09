package display

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/crazy-goat/tyci-agent/jobs"
)

// countTaskRowBuilds returns how many times fn builds the Tasks rows.
func countTaskRowBuilds(fn func()) int64 {
	before := sidebarTaskRowBuilds.Load()
	fn()
	return sidebarTaskRowBuilds.Load() - before
}

func TestTaskRowBuildsPerJobEvent(t *testing.T) {
	m := tasksFixtureAllDone(t)
	m.sidebarCursor = 3
	got := countTaskRowBuilds(func() {
		m.applyJobUpdate(jobs.Job{
			ID:          "bash-000",
			Kind:        jobs.KindBash,
			Status:      jobs.StatusDone,
			Description: "go test ./pkg000/...",
			StartedAt:   time.Now().Add(-time.Hour),
			FinishedAt:  time.Now(),
			EventSeq:    1 << 40,
		})
	})
	t.Logf("builds per job event: %d", got)
	if got != 2 {
		t.Fatalf("applyJobUpdate built the Tasks rows %d times, want 2", got)
	}
}

func TestTaskRowBuildsPerKeyAndClick(t *testing.T) {
	m := tasksFixtureAllDone(t)
	m.sidebarCursor = 0
	if got := countTaskRowBuilds(func() { m.sidebarMoveCursor(1) }); got != 1 {
		t.Fatalf("Down built the Tasks rows %d times, want 1", got)
	}
	layout := m.sidebarLayout()
	if got := countTaskRowBuilds(func() {
		m.updateSidebar(tea.MouseMsg{X: layout.contentLeft, Y: layout.contentTop, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	}); got > 2 {
		t.Fatalf("click built the Tasks rows %d times, want at most 2", got)
	}
}
