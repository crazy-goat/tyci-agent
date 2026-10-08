package display

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/crazy-goat/tyci-agent/jobs"
)

// subagentOrderJob returns a subagent job with the given status and times. A
// zero finished time leaves the job without an end.
func subagentOrderJob(id, parent string, status jobs.Status, started, finished time.Time) jobs.Job {
	return jobs.Job{
		ID:          id,
		Kind:        jobs.KindSubagent,
		ParentID:    parent,
		Status:      status,
		Description: id,
		StartedAt:   started,
		FinishedAt:  finished,
	}
}

// treeIDs returns the job IDs of the subagent tree in display order. The root
// row is "main".
func treeIDs(rows []subagentTreeRow) string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.isRoot {
			ids = append(ids, "main")
			continue
		}
		ids = append(ids, r.job.ID)
	}
	return strings.Join(ids, ",")
}

// separatorBeforeIDs returns the job IDs of the rows that have a separator
// above them.
func separatorBeforeIDs(rows []subagentTreeRow) string {
	var ids []string
	for _, r := range rows {
		if r.separatorBefore {
			ids = append(ids, r.job.ID)
		}
	}
	return strings.Join(ids, ",")
}

// TestSubagentTree_ActiveFirstThenFinishedByEnd: active subagents come first,
// newest start first. Finished subagents follow, most recently finished first.
// A failed or truncated job counts as finished. A separator sits above the
// first finished row.
func TestSubagentTree_ActiveFirstThenFinishedByEnd(t *testing.T) {
	now := time.Now()
	m := newTestModelForSidebar()
	m.applyJobUpdate(subagentOrderJob("f1", "", jobs.StatusDone, now.Add(-20*time.Minute), now.Add(-2*time.Minute)))
	m.applyJobUpdate(subagentOrderJob("f2", "", jobs.StatusDone, now.Add(-1*time.Minute), now.Add(-9*time.Minute)))
	m.applyJobUpdate(subagentOrderJob("f3", "", jobs.StatusFailed, now.Add(-30*time.Minute), now.Add(-1*time.Minute)))
	m.applyJobUpdate(subagentOrderJob("t1", "", jobs.StatusTruncated, now.Add(-40*time.Minute), now.Add(-5*time.Minute)))
	m.applyJobUpdate(subagentOrderJob("a1", "", jobs.StatusRunning, now.Add(-10*time.Minute), time.Time{}))
	m.applyJobUpdate(subagentOrderJob("a2", "", jobs.StatusWaitingAnswer, now.Add(-5*time.Minute), time.Time{}))

	rows := m.buildSubagentTree()
	if got, want := treeIDs(rows), "main,a2,a1,f3,f1,t1,f2"; got != want {
		t.Fatalf("order: want %s, got %s", want, got)
	}
	if got, want := separatorBeforeIDs(rows), "f3"; got != want {
		t.Fatalf("separator: want above %s, got %q", want, got)
	}
}

// TestSubagentTree_SeparatorOnlyBetweenNonEmptyParts: no separator when one
// part of a sibling group is empty.
func TestSubagentTree_SeparatorOnlyBetweenNonEmptyParts(t *testing.T) {
	now := time.Now()
	active := subagentOrderJob("a", "", jobs.StatusRunning, now.Add(-time.Minute), time.Time{})
	done := subagentOrderJob("d", "", jobs.StatusDone, now.Add(-2*time.Minute), now.Add(-time.Minute))
	tests := []struct {
		name string
		list []jobs.Job
		want string
	}{
		{"no subagents", nil, ""},
		{"only active", []jobs.Job{active}, ""},
		{"only finished", []jobs.Job{done}, ""},
		{"both parts", []jobs.Job{active, done}, "d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModelForSidebar()
			for _, j := range tt.list {
				m.applyJobUpdate(j)
			}
			if got := separatorBeforeIDs(m.buildSubagentTree()); got != tt.want {
				t.Fatalf("separator: want %q, got %q", tt.want, got)
			}
		})
	}
}

// TestSubagentTree_NestedGroupsFollowTheRule: a parent with an active child
// counts as active in its own sibling group. Every sibling group gets the same
// order and the same separator.
func TestSubagentTree_NestedGroupsFollowTheRule(t *testing.T) {
	now := time.Now()
	m := newTestModelForSidebar()
	// p1 is finished, but its child c1 still runs, so p1 counts as active.
	m.applyJobUpdate(subagentOrderJob("p1", "", jobs.StatusDone, now.Add(-20*time.Minute), now.Add(-2*time.Minute)))
	m.applyJobUpdate(subagentOrderJob("c1", "p1", jobs.StatusRunning, now.Add(-15*time.Minute), time.Time{}))
	m.applyJobUpdate(subagentOrderJob("c2", "p1", jobs.StatusDone, now.Add(-18*time.Minute), now.Add(-16*time.Minute)))
	m.applyJobUpdate(subagentOrderJob("p2", "", jobs.StatusDone, now.Add(-3*time.Minute), now.Add(-1*time.Minute)))
	m.applyJobUpdate(subagentOrderJob("p3", "", jobs.StatusRunning, now.Add(-30*time.Minute), time.Time{}))

	rows := m.buildSubagentTree()
	if got, want := treeIDs(rows), "main,p1,c1,c2,p3,p2"; got != want {
		t.Fatalf("order: want %s, got %s", want, got)
	}
	if got, want := separatorBeforeIDs(rows), "c2,p2"; got != want {
		t.Fatalf("separator: want above %s, got %q", want, got)
	}
}

// TestSidebarTasks_SeparatorIsNotSelectable: the separator is a row of its own,
// the cursor skips it, and a click on it does nothing.
func TestSidebarTasks_SeparatorIsNotSelectable(t *testing.T) {
	now := time.Now()
	m := newTestModelForSidebar()
	m.applyJobUpdate(subagentOrderJob("a1", "", jobs.StatusRunning, now.Add(-time.Minute), time.Time{}))
	m.applyJobUpdate(subagentOrderJob("f1", "", jobs.StatusDone, now.Add(-2*time.Minute), now.Add(-time.Minute)))
	m.openSidebar(sidebarTabTasks)
	layout := m.sidebarLayout()
	width := layout.contentWidth

	rows := m.sidebarTaskRows(width)
	sep := -1
	for i, row := range rows {
		if row.isSeparator {
			sep = i
		}
	}
	if sep < 0 {
		t.Fatal("expected a separator row between the active and the finished subagent")
	}
	if rows[sep].job != nil || rows[sep].isMain || rows[sep].isHeading {
		t.Fatalf("expected the separator to carry no job and no heading flag, got %+v", rows[sep])
	}
	if got := lipgloss.Width(rows[sep].line); got != width {
		t.Fatalf("expected the separator to cover the full width %d, got %d", width, got)
	}
	for _, idx := range m.sidebarTaskJobRows(width) {
		if idx == sep {
			t.Fatal("the separator is listed among the selectable rows")
		}
	}

	// Down from the active row lands on the finished row, not on the separator.
	m.sidebarCursor = 1 // main is row 0, a1 is row 1
	m.sidebarMoveCursor(1)
	if got := m.sidebarCursorJobID(); got != "f1" {
		t.Fatalf("expected Down to select f1, got %q", got)
	}

	// A click on the separator line keeps the cursor and opens nothing.
	m.sidebarCursor = 1
	model, _ := m.updateSidebar(tea.MouseMsg{
		X: layout.contentLeft, Y: layout.contentTop + sep,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	got := model.(TuiModel)
	if got.sidebarCursor != 1 {
		t.Fatalf("expected a click on the separator to keep the cursor at 1, got %d", got.sidebarCursor)
	}
	if got.agentView != nil || got.subagentModalActive {
		t.Fatal("expected a click on the separator to open nothing")
	}
}

// TestSidebarTasks_SelectionFollowsJobIntoFinishedPart: when a job moves from
// the active part to the finished part, the cursor stays on that job.
func TestSidebarTasks_SelectionFollowsJobIntoFinishedPart(t *testing.T) {
	now := time.Now()
	m := newTestModelForSidebar()
	m.applyJobUpdate(subagentOrderJob("f1", "", jobs.StatusDone, now.Add(-20*time.Minute), now.Add(-15*time.Minute)))
	m.applyJobUpdate(subagentOrderJob("a1", "", jobs.StatusRunning, now.Add(-10*time.Minute), time.Time{}))
	m.applyJobUpdate(subagentOrderJob("b1", "", jobs.StatusRunning, now.Add(-5*time.Minute), time.Time{}))
	m.openSidebar(sidebarTabTasks)

	// Task rows: main, b1, a1, f1. The cursor is on a1.
	m.sidebarCursor = 2
	if got := m.sidebarCursorJobID(); got != "a1" {
		t.Fatalf("setup: expected the cursor on a1, got %q", got)
	}

	// b1 finishes. It moves below a1, so a1 is now the first job row.
	done := subagentOrderJob("b1", "", jobs.StatusDone, now.Add(-5*time.Minute), now)
	done.EventSeq = 2
	m.applyJobUpdate(done)

	if got := m.sidebarCursorJobID(); got != "a1" {
		t.Fatalf("expected the cursor to follow a1, got %q", got)
	}
	if m.sidebarCursor != 1 {
		t.Fatalf("expected the cursor at row 1 after a1 moved up, got %d", m.sidebarCursor)
	}
}

// TestSidebarTasks_SelectionFollowsSelectedJobIntoFinishedPart: the cursor is
// on b1 and b1 finishes. b1 moves into the finished part, and the cursor stays
// on b1 in its new row.
func TestSidebarTasks_SelectionFollowsSelectedJobIntoFinishedPart(t *testing.T) {
	now := time.Now()
	m := newTestModelForSidebar()
	m.applyJobUpdate(subagentOrderJob("f1", "", jobs.StatusDone, now.Add(-20*time.Minute), now.Add(-15*time.Minute)))
	m.applyJobUpdate(subagentOrderJob("a1", "", jobs.StatusRunning, now.Add(-10*time.Minute), time.Time{}))
	m.applyJobUpdate(subagentOrderJob("b1", "", jobs.StatusRunning, now.Add(-5*time.Minute), time.Time{}))
	m.openSidebar(sidebarTabTasks)

	// Task rows: main, b1, a1, f1. The cursor is on b1.
	m.sidebarCursor = 1
	if got := m.sidebarCursorJobID(); got != "b1" {
		t.Fatalf("setup: expected the cursor on b1, got %q", got)
	}

	// b1 finishes. It moves into the finished part, below a1.
	done := subagentOrderJob("b1", "", jobs.StatusDone, now.Add(-5*time.Minute), now)
	done.EventSeq = 2
	m.applyJobUpdate(done)

	// Task rows: main, a1, b1, f1. The cursor follows b1 to row 2.
	if got := m.sidebarCursorJobID(); got != "b1" {
		t.Fatalf("expected the cursor to follow b1, got %q", got)
	}
	if m.sidebarCursor != 2 {
		t.Fatalf("expected the cursor at row 2 after b1 moved down, got %d", m.sidebarCursor)
	}
}
