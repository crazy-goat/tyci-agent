package display

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/tools"
)

// ownerTestModel builds the Tasks tab fixture for the owner filter. Subagent
// sub-a runs under main, and sub-b runs under sub-a. Each owner has bash jobs:
// main has bash-main, sub-a has bash-a, and sub-b has bash-b1 then bash-b2.
// The Subagents rows are main, sub-a, sub-b, in that order.
func ownerTestModel(t *testing.T) TuiModel {
	t.Helper()
	m := newTestModelForSidebar()
	now := time.Now()
	m.applyJobUpdate(jobs.Job{ID: "sub-a", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "run/worker-a", StartedAt: now.Add(-5 * time.Minute)})
	m.applyJobUpdate(jobs.Job{ID: "sub-b", Kind: jobs.KindSubagent, ParentID: "sub-a", Status: jobs.StatusDone, Description: "run/nested-b", StartedAt: now.Add(-4 * time.Minute)})
	m.applyJobUpdate(jobs.Job{ID: "bash-main", Kind: jobs.KindBash, Status: jobs.StatusDone, Description: "main command", StartedAt: now.Add(-6 * time.Minute)})
	m.applyJobUpdate(jobs.Job{ID: "bash-a", Kind: jobs.KindBash, ParentID: "sub-a", Status: jobs.StatusDone, Description: "a command", StartedAt: now.Add(-3 * time.Minute)})
	m.applyJobUpdate(jobs.Job{ID: "bash-b1", Kind: jobs.KindBash, ParentID: "sub-b", Status: jobs.StatusDone, Description: "b first", StartedAt: now.Add(-2 * time.Minute)})
	m.applyJobUpdate(jobs.Job{ID: "bash-b2", Kind: jobs.KindBash, ParentID: "sub-b", Status: jobs.StatusDone, Description: "b second", StartedAt: now.Add(-1 * time.Minute)})
	m.openSidebar(sidebarTabTasks)
	return m
}

// taskBashIDs returns the job ids of the Bash rows the Tasks tab lists now.
func taskBashIDs(m TuiModel) []string {
	var ids []string
	for _, row := range m.sidebarTaskRows(m.sidebarLayout().contentWidth) {
		if row.group == "Bash" && row.job != nil {
			ids = append(ids, row.job.ID)
		}
	}
	return ids
}

// taskLuaLines returns the plain text of the Lua rows the Tasks tab lists now.
func taskLuaLines(m TuiModel) []string {
	var lines []string
	for _, row := range m.sidebarTaskRows(m.sidebarLayout().contentWidth) {
		if row.group == "Lua" && !row.isHeading {
			lines = append(lines, ansi.Strip(row.line))
		}
	}
	return lines
}

// cursorRowID returns the job id under the Tasks cursor, or "main".
func cursorRowID(m TuiModel) string {
	width := m.sidebarLayout().contentWidth
	rows := m.sidebarTaskRows(width)
	jobRows := m.sidebarTaskJobRows(width)
	if m.sidebarCursor < 0 || m.sidebarCursor >= len(jobRows) {
		return ""
	}
	row := rows[jobRows[m.sidebarCursor]]
	if row.isMain {
		return "main"
	}
	return row.job.ID
}

// recordLuaRunForOwner runs a one-line Lua tool with ownerID as the job id of
// the caller, so the run lands in tools.LuaRunHistory with that owner.
func recordLuaRunForOwner(t *testing.T, name, ownerID string) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`return {
  schema = {name = %q, description = "test", parameters = {}},
  run = function(ctx, args) return {success = true, content = "ok"} end
}`, name)
	if err := os.WriteFile(filepath.Join(dir, name+".lua"), []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := tools.LoadLuaTools(dir)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("LoadLuaTools: err=%v, tools=%d", err, len(loaded))
	}
	ctx := context.WithValue(context.Background(), tools.JobIDCtxKey{}, ownerID)
	loaded[0].Run(ctx, map[string]any{})
}

// TestSidebarTasks_BashFollowsSelectedAgent: the Bash rows show only the jobs
// of the agent the cursor last selected on the Subagents rows. Moving the
// cursor into the Bash rows keeps that agent, so the list does not change.
func TestSidebarTasks_BashFollowsSelectedAgent(t *testing.T) {
	m := ownerTestModel(t)
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-main"}) {
		t.Fatalf("main selected: want [bash-main], got %v", got)
	}

	m.sidebarMoveCursor(1) // sub-a
	if got := cursorRowID(m); got != "sub-a" {
		t.Fatalf("cursor on %q, want sub-a", got)
	}
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-a"}) {
		t.Fatalf("sub-a selected: want [bash-a], got %v", got)
	}

	m.sidebarMoveCursor(1) // sub-b, nested under sub-a
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-b1", "bash-b2"}) {
		t.Fatalf("sub-b selected: want [bash-b1 bash-b2], got %v", got)
	}

	// Into the Bash rows: the owner must stay sub-b.
	m.sidebarMoveCursor(1)
	if got := cursorRowID(m); got != "bash-b1" {
		t.Fatalf("cursor on %q, want bash-b1", got)
	}
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-b1", "bash-b2"}) {
		t.Fatalf("cursor in Bash rows changed the list: got %v", got)
	}
	m.sidebarMoveCursor(1)
	if got := cursorRowID(m); got != "bash-b2" {
		t.Fatalf("cursor on %q, want bash-b2", got)
	}
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-b1", "bash-b2"}) {
		t.Fatalf("cursor in Bash rows changed the list: got %v", got)
	}

	// Back up to the Subagents rows: sub-b, then sub-a, then main.
	m.sidebarMoveCursor(-1)
	m.sidebarMoveCursor(-1)
	if got := cursorRowID(m); got != "sub-b" {
		t.Fatalf("cursor on %q, want sub-b", got)
	}
	m.sidebarMoveCursor(-1)
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-a"}) {
		t.Fatalf("back on sub-a: want [bash-a], got %v", got)
	}
	m.sidebarMoveCursor(-1)
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-main"}) {
		t.Fatalf("back on main: want [bash-main], got %v", got)
	}
}

// TestSidebarTasks_LuaFollowsSelectedAgent: the Lua rows follow the same
// selected agent as the Bash rows. The owner comes from the job id in the
// Lua call's context.
func TestSidebarTasks_LuaFollowsSelectedAgent(t *testing.T) {
	defer tools.SnapshotLuaRunHistoryForTesting()()
	recordLuaRunForOwner(t, "lua-main", "")
	recordLuaRunForOwner(t, "lua-a", "sub-a")
	recordLuaRunForOwner(t, "lua-b", "sub-b")

	m := ownerTestModel(t)
	check := func(stage string, want string) {
		t.Helper()
		lines := taskLuaLines(m)
		if len(lines) != 1 || !strings.Contains(lines[0], want) {
			t.Fatalf("%s: want one Lua row named %q, got %q", stage, want, lines)
		}
	}
	check("main", "lua-main")
	m.sidebarMoveCursor(1) // sub-a
	check("sub-a", "lua-a")
	m.sidebarMoveCursor(1) // sub-b
	check("sub-b", "lua-b")
	m.sidebarMoveCursor(1) // bash-b1: the Lua rows stay on sub-b
	check("cursor in Bash rows", "lua-b")
}

// TestSidebarTasks_ClickOnAgentRowSelectsItsBash: a click on a Subagents row
// selects that agent, as the cursor does. The row has a live transcript, so
// the click opens its agent view and the sidebar stays open.
func TestSidebarTasks_ClickOnAgentRowSelectsItsBash(t *testing.T) {
	m := ownerTestModel(t)
	tools.RecordLiveEvent("sub-a", tools.LiveEvent{Kind: "thinking", Content: "planning"})
	layout := m.sidebarLayout()
	line := -1
	for i, row := range m.sidebarTaskRows(layout.contentWidth) {
		if row.job != nil && row.job.ID == "sub-a" {
			line = i
		}
	}
	if line < 0 {
		t.Fatal("sub-a row not found")
	}
	model, _ := m.updateSidebar(tea.MouseMsg{
		X: layout.contentLeft, Y: layout.contentTop + line - m.sidebarVisibleScroll(layout),
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m2 := model.(TuiModel)
	if !m2.sidebarActive || m2.agentView == nil {
		t.Fatalf("expected the click to open the agent view with the sidebar open")
	}
	if got := taskBashIDs(m2); !slices.Equal(got, []string{"bash-a"}) {
		t.Fatalf("click on sub-a: want [bash-a], got %v", got)
	}
}

// TestSidebarTasks_OwnerResetsToMain: opening the sidebar, or switching to
// the Tasks tab, puts the cursor on main, so the Bash rows show main again.
func TestSidebarTasks_OwnerResetsToMain(t *testing.T) {
	m := ownerTestModel(t)
	m.sidebarMoveCursor(1) // sub-a
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-a"}) {
		t.Fatalf("setup: want [bash-a] on sub-a, got %v", got)
	}

	m.sidebarSwitchTab(sidebarTabTokens)
	m.sidebarSwitchTab(sidebarTabTasks)
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-main"}) {
		t.Fatalf("after switching tabs: want [bash-main], got %v", got)
	}

	m.sidebarMoveCursor(1) // sub-a again
	m.closeSidebar()
	m.openSidebar(sidebarTabTasks)
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-main"}) {
		t.Fatalf("after reopening: want [bash-main], got %v", got)
	}
}
