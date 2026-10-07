package display

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// TuiRunRow is one workflow run in the sidebar Runs tab.
type TuiRunRow struct {
	ID     string
	Issue  int
	Status string // running|paused|done|failed
	State  string // current state
	Role   string // role of the running agent, may be empty
	Since  time.Time
	Steps  []string // last history steps, "state -> key"
	Totals string   // tokens, cost and time of the whole run, may be empty
}

func (m TuiModel) renderSidebarRuns(width int) []string {
	if m.runLister == nil {
		return []string{"", "  Workflow runs are not wired up in this build."}
	}
	rows := m.runLister()
	if len(rows) == 0 {
		return []string{"", "  No workflow runs yet."}
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	var out []string
	for _, r := range rows {
		cur := r.State
		if r.Role != "" {
			cur += " (" + r.Role + ")"
		}
		if r.Status == "paused" {
			cur = "ask: " + cur
		}
		line := fmt.Sprintf(" #%d %s  %s", r.Issue, r.Status, cur)
		if !r.Since.IsZero() && (r.Status == "running" || r.Status == "paused") {
			line += " " + formatDurationShort(time.Since(r.Since))
		}
		out = append(out, truncateToWidth(line, width), dim.Render(truncateToWidth("   "+r.ID, width)))
		if r.Totals != "" {
			out = append(out, dim.Render(truncateToWidth("   total: "+r.Totals, width)))
		}
		for _, s := range r.Steps {
			out = append(out, dim.Render(truncateToWidth("   "+s, width)))
		}
	}
	return out
}
