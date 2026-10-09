package display

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/muesli/termenv"
)

// tasksFixtureAllDone returns benchTasksModel with every job finished. A
// finished job has no elapsed time that moves between two renders, so the
// output of two renders can be compared byte for byte.
func tasksFixtureAllDone(t *testing.T) TuiModel {
	t.Helper()
	m := benchTasksModel(t)
	for id, j := range m.backgroundJobs {
		j.Status = jobs.StatusDone
		j.FinishedAt = j.StartedAt.Add(time.Second)
		m.backgroundJobs[id] = j
	}
	return m
}

// legacyRenderSidebarTasks is the render as it was before the rows were built
// once: it calls sidebarTaskRows and then sidebarTaskJobRows, which builds the
// rows a second time. It is the reference for the byte-identical check.
func legacyRenderSidebarTasks(m TuiModel, width int) []string {
	rows := m.sidebarTaskRows(width)
	out := make([]string, 0, len(rows))
	cursorLine := -1
	if m.sidebarCursor >= 0 {
		jobRows := m.sidebarTaskJobRows(width)
		if m.sidebarCursor < len(jobRows) {
			cursorLine = jobRows[m.sidebarCursor]
		}
	}
	for i, row := range rows {
		if row.isHeading {
			out = append(out, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245")).Render(fillWidth(row.line, width)))
			continue
		}
		line := row.line
		if i == cursorLine {
			line = ansi.Strip(line)
		} else {
			line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[39m")
		}
		out = append(out, rowStyle(width, i == cursorLine).Render(truncateToWidth(line, width)))
	}
	if len(out) == 0 {
		return []string{"", "  No tasks recorded this session."}
	}
	return out
}

func TestRenderSidebarTasksMatchesLegacyTwoCallPath(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })
	base := tasksFixtureAllDone(t)
	for _, width := range []int{20, 40, 80} {
		for _, cursor := range []int{-1, 0, 1, 5, 40, 500} {
			m := base
			m.sidebarCursor = cursor
			got := m.renderSidebarTasks(width)
			want := legacyRenderSidebarTasks(m, width)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("width %d cursor %d: render differs from the legacy path", width, cursor)
			}
		}
	}
}

// TestSidebarTaskRowSelectionByKeyAndMouse checks that Up/Down selection and a
// click on a line pick the same job row as the rendered list shows.
func TestSidebarTaskRowSelectionByKeyAndMouse(t *testing.T) {
	m := tasksFixtureAllDone(t)
	layout := m.sidebarLayout()
	width := layout.contentWidth
	rows := m.sidebarTaskRows(width)
	jobRows := m.sidebarTaskJobRows(width)
	if len(jobRows) < 3 {
		t.Fatalf("fixture has %d job rows, want at least 3", len(jobRows))
	}

	// Key path: Down moves the cursor one job row at a time.
	m.sidebarCursor = 0
	for i := range jobRows {
		if i > 0 {
			m.sidebarMoveCursor(1)
		}
		if m.sidebarCursor != i {
			t.Fatalf("cursor = %d, want %d", m.sidebarCursor, i)
		}
		wantID := ""
		if rows[jobRows[i]].job != nil {
			wantID = rows[jobRows[i]].job.ID
		}
		if got := m.sidebarCursorJobID(); got != wantID {
			t.Fatalf("row %d: cursor job = %q, want %q", i, got, wantID)
		}
	}

	// Mouse path: a click on the line of each job row opens that job.
	for i, line := range jobRows {
		if rows[line].job == nil {
			continue
		}
		scroll := line - layout.contentHeight + 1
		if scroll < 0 {
			scroll = 0
		}
		c := m
		c.sidebarScroll = scroll
		y := layout.contentTop + line - c.sidebarVisibleScroll(layout)
		if y < layout.contentTop || y >= layout.contentTop+layout.contentHeight {
			t.Fatalf("job row %d (line %d) is outside the visible window", i, line)
		}
		model, _ := c.updateSidebar(tea.MouseMsg{
			X: layout.contentLeft, Y: y,
			Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
		})
		got := model.(TuiModel)
		if !got.subagentModalActive || got.subagentModalTitle != rows[line].job.Description {
			t.Fatalf("click on job row %d (%s): modal title %q, active %v",
				i, rows[line].job.ID, got.subagentModalTitle, got.subagentModalActive)
		}
	}
}

// TestRenderSidebarTasksFollowsJobEvents checks that the render
// follows a job that changes state: the rows that the cursor and the list use
// are the ones of the current job set, not of an earlier one.
func TestRenderSidebarTasksFollowsJobEvents(t *testing.T) {
	m := tasksFixtureAllDone(t)
	m.sidebarCursor = 2
	before := m.renderSidebarTasks(40)

	m.applyJobUpdate(jobs.Job{
		ID:          "bash-new",
		Kind:        jobs.KindBash,
		Status:      jobs.StatusDone,
		Description: "fresh job",
		StartedAt:   time.Now().Add(-time.Minute),
		FinishedAt:  time.Now(),
	})
	after := m.renderSidebarTasks(40)
	if strings.Join(before, "\n") == strings.Join(after, "\n") {
		t.Fatalf("render did not change after a job was added")
	}
	if !strings.Contains(ansi.Strip(strings.Join(after, "\n")), "fresh job") {
		t.Fatalf("added job is missing from the render")
	}
}

// TestSidebarTasksWindowStylesVisibleRowsOnly checks that the lines of the
// Tasks window are the same as the lines of the full render at the same
// scroll position, for the top, the middle, the bottom and a list shorter
// than the window. Only the rows of the window are styled.
func TestSidebarTasksWindowStylesVisibleRowsOnly(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })
	const height = 10
	base := tasksFixtureAllDone(t)
	empty := newTestModelForSidebar()
	empty.openSidebar(sidebarTabTasks)

	cases := []struct {
		name   string
		model  TuiModel
		scroll int
	}{
		{"top", base, 0},
		{"middle", base, 40},
		{"bottom", base, 1 << 20},
		{"fewer rows than the window", empty, 0},
	}
	for _, tc := range cases {
		for _, cursor := range []int{-1, 0, 5, 40, 500} {
			m := tc.model
			m.sidebarScroll = tc.scroll
			m.sidebarCursor = cursor
			width := 60
			layout := sidebarLayoutT{contentHeight: height}

			full := m.renderSidebarTasks(width)
			scroll := m.sidebarVisibleScrollForLineCount(layout, len(full))
			end := min(len(full), scroll+height)
			want := full[scroll:end]
			got := m.sidebarBodyLines(layout, width)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("%s, cursor %d: window differs from the full render lines %d-%d", tc.name, cursor, scroll, end)
			}
			if len(got) != end-scroll {
				t.Fatalf("%s, cursor %d: got %d lines, want %d", tc.name, cursor, len(got), end-scroll)
			}
		}
	}
}
