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
// The Subagents rows are main, sub-a, sub-b, in that order. sub-a and sub-b
// have a live transcript, so Enter on either one selects it and keeps the
// sidebar open.
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
	tools.RecordLiveEvent("sub-a", tools.LiveEvent{Kind: "text", Content: "a"})
	tools.RecordLiveEvent("sub-b", tools.LiveEvent{Kind: "text", Content: "b"})
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

// pressSidebarKey sends one key to the Tasks tab, as the user does, and
// returns the model after it.
func pressSidebarKey(t *testing.T, m TuiModel, key tea.KeyType) TuiModel {
	t.Helper()
	model, _ := m.updateSidebar(tea.KeyMsg{Type: key})
	return model.(TuiModel)
}

// wheelSidebar sends one mouse wheel step to the Tasks tab.
func wheelSidebar(t *testing.T, m TuiModel, button tea.MouseButton) TuiModel {
	t.Helper()
	model, _ := m.updateSidebar(tea.MouseMsg{Button: button, Action: tea.MouseActionPress})
	return model.(TuiModel)
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

// TestSidebarTasks_CursorAloneKeepsOwner: moving the cursor with the arrow
// keys or the mouse wheel does not change the Bash and Lua rows. Only Enter or
// a click on a Subagents row selects an agent.
func TestSidebarTasks_CursorAloneKeepsOwner(t *testing.T) {
	defer tools.SnapshotLuaRunHistoryForTesting()()
	recordLuaRunForOwner(t, "lua-main", "")
	recordLuaRunForOwner(t, "lua-a", "sub-a")

	m := ownerTestModel(t)
	check := func(stage string) {
		t.Helper()
		if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-main"}) {
			t.Fatalf("%s: want Bash [bash-main], got %v", stage, got)
		}
		lines := taskLuaLines(m)
		if len(lines) != 1 || !strings.Contains(lines[0], "lua-main") {
			t.Fatalf("%s: want one Lua row named lua-main, got %q", stage, lines)
		}
	}
	check("open")

	// main, sub-a, sub-b, then bash-main: the cursor walks past every agent.
	for i := 0; i < 3; i++ {
		m = pressSidebarKey(t, m, tea.KeyDown)
	}
	if got := cursorRowID(m); got != "bash-main" {
		t.Fatalf("cursor on %q, want bash-main", got)
	}
	check("cursor on bash-main")

	m = pressSidebarKey(t, m, tea.KeyUp) // sub-b
	m = wheelSidebar(t, m, tea.MouseButtonWheelUp)
	m = wheelSidebar(t, m, tea.MouseButtonWheelUp) // main
	if got := cursorRowID(m); got != "main" {
		t.Fatalf("cursor on %q, want main", got)
	}
	check("wheel up to main")

	m = wheelSidebar(t, m, tea.MouseButtonWheelDown) // sub-a
	if got := cursorRowID(m); got != "sub-a" {
		t.Fatalf("cursor on %q, want sub-a", got)
	}
	check("wheel down to sub-a")
}

// TestSidebarTasks_KeyboardReachesBashOfEveryOwner: with the keyboard only, the
// cursor reaches the Bash rows of main and of each subagent. Enter on a
// Subagents row selects that agent first.
func TestSidebarTasks_KeyboardReachesBashOfEveryOwner(t *testing.T) {
	m := ownerTestModel(t)
	for i := 0; i < 3; i++ {
		m = pressSidebarKey(t, m, tea.KeyDown)
	}
	if got := cursorRowID(m); got != "bash-main" {
		t.Fatalf("cursor on %q, want bash-main", got)
	}

	// Select sub-a: Up twice, then Enter. Then Down reaches its Bash row.
	m = pressSidebarKey(t, m, tea.KeyUp)
	m = pressSidebarKey(t, m, tea.KeyUp)
	if got := cursorRowID(m); got != "sub-a" {
		t.Fatalf("cursor on %q, want sub-a", got)
	}
	m = pressSidebarKey(t, m, tea.KeyEnter)
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-a"}) {
		t.Fatalf("Enter on sub-a: want [bash-a], got %v", got)
	}
	m = pressSidebarKey(t, m, tea.KeyDown) // sub-b
	m = pressSidebarKey(t, m, tea.KeyDown) // bash-a
	if got := cursorRowID(m); got != "bash-a" {
		t.Fatalf("cursor on %q, want bash-a", got)
	}

	// Select sub-b, which is nested under sub-a: Up once, then Enter.
	m = pressSidebarKey(t, m, tea.KeyUp)
	if got := cursorRowID(m); got != "sub-b" {
		t.Fatalf("cursor on %q, want sub-b", got)
	}
	m = pressSidebarKey(t, m, tea.KeyEnter)
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-b1", "bash-b2"}) {
		t.Fatalf("Enter on sub-b: want [bash-b1 bash-b2], got %v", got)
	}
	m = pressSidebarKey(t, m, tea.KeyDown)
	if got := cursorRowID(m); got != "bash-b1" {
		t.Fatalf("cursor on %q, want bash-b1", got)
	}

	// Back to main: Up to the main row, then Enter. Its Bash row is reachable again.
	for i := 0; i < 4; i++ {
		m = pressSidebarKey(t, m, tea.KeyUp)
	}
	if got := cursorRowID(m); got != "main" {
		t.Fatalf("cursor on %q, want main", got)
	}
	m = pressSidebarKey(t, m, tea.KeyEnter)
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-main"}) {
		t.Fatalf("Enter on main: want [bash-main], got %v", got)
	}
}

// TestSidebarTasks_LuaFollowsSelectedAgent: the Lua rows follow the agent that
// Enter selected, as the Bash rows do. The owner comes from the job id in the
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
	m = pressSidebarKey(t, m, tea.KeyDown) // sub-a
	m = pressSidebarKey(t, m, tea.KeyEnter)
	check("sub-a selected", "lua-a")
	m = pressSidebarKey(t, m, tea.KeyDown) // sub-b: the cursor alone keeps sub-a
	check("cursor on sub-b", "lua-a")
	m = pressSidebarKey(t, m, tea.KeyEnter)
	check("sub-b selected", "lua-b")
}

// TestSidebarTasks_EnterOnAgentRowOpensViewAndKeepsSidebar: Enter on a
// subagent with a live transcript selects it, opens its agent view and keeps
// the sidebar open. Enter on main selects main again and shows the main
// conversation.
func TestSidebarTasks_EnterOnAgentRowOpensViewAndKeepsSidebar(t *testing.T) {
	m := ownerTestModel(t)
	m = pressSidebarKey(t, m, tea.KeyDown) // sub-a
	m = pressSidebarKey(t, m, tea.KeyEnter)
	if !m.sidebarActive || m.agentView == nil {
		t.Fatalf("Enter on sub-a: want the sidebar open with the agent view, got sidebarActive=%v agentView=%v", m.sidebarActive, m.agentView != nil)
	}
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-a"}) {
		t.Fatalf("Enter on sub-a: want [bash-a], got %v", got)
	}

	m = pressSidebarKey(t, m, tea.KeyUp) // main
	m = pressSidebarKey(t, m, tea.KeyEnter)
	if !m.sidebarActive || m.agentView != nil {
		t.Fatalf("Enter on main: want the sidebar open without the agent view, got sidebarActive=%v agentView=%v", m.sidebarActive, m.agentView != nil)
	}
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-main"}) {
		t.Fatalf("Enter on main: want [bash-main], got %v", got)
	}
}

// TestSidebarTasks_EnterWithoutTranscriptClosesSidebar pins the existing Enter
// behaviour for a subagent without a live transcript: the sidebar closes and
// the result modal opens. closeSidebar clears the owner, so the Bash rows show
// main when the sidebar opens again.
func TestSidebarTasks_EnterWithoutTranscriptClosesSidebar(t *testing.T) {
	m := ownerTestModel(t)
	m.applyJobUpdate(jobs.Job{ID: "sub-c", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "run/worker-c", StartedAt: time.Now().Add(-2 * time.Minute)})
	for i := 0; i < 10 && cursorRowID(m) != "sub-c"; i++ {
		m = pressSidebarKey(t, m, tea.KeyDown)
	}
	if got := cursorRowID(m); got != "sub-c" {
		t.Fatalf("cursor on %q, want sub-c", got)
	}

	m = pressSidebarKey(t, m, tea.KeyEnter)
	if m.sidebarActive || !m.subagentModalActive {
		t.Fatalf("Enter on sub-c: want the sidebar closed and the result modal open, got sidebarActive=%v subagentModalActive=%v", m.sidebarActive, m.subagentModalActive)
	}
	if m.sidebarTaskOwner != "" {
		t.Fatalf("Enter on sub-c: want owner cleared by closeSidebar, got %q", m.sidebarTaskOwner)
	}
}

// TestSidebarTasks_OrphanRowsListedUnderMain: a Bash job or a Lua run whose
// owner is not a Subagents row (its subagent was evicted from the job list) is
// listed under main, and under no subagent.
func TestSidebarTasks_OrphanRowsListedUnderMain(t *testing.T) {
	defer tools.SnapshotLuaRunHistoryForTesting()()
	recordLuaRunForOwner(t, "lua-gone", "gone")

	m := ownerTestModel(t)
	m.applyJobUpdate(jobs.Job{ID: "bash-gone", Kind: jobs.KindBash, ParentID: "gone", Status: jobs.StatusRunning, Description: "orphan command", StartedAt: time.Now().Add(-7 * time.Minute)})

	if got := taskBashIDs(m); !slices.Contains(got, "bash-gone") || !slices.Contains(got, "bash-main") {
		t.Fatalf("main selected: want bash-gone and bash-main, got %v", got)
	}
	if lines := taskLuaLines(m); len(lines) != 1 || !strings.Contains(lines[0], "lua-gone") {
		t.Fatalf("main selected: want one Lua row named lua-gone, got %q", lines)
	}

	m = pressSidebarKey(t, m, tea.KeyDown) // sub-a
	m = pressSidebarKey(t, m, tea.KeyEnter)
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-a"}) {
		t.Fatalf("sub-a selected: want [bash-a], got %v", got)
	}
	if lines := taskLuaLines(m); len(lines) != 0 {
		t.Fatalf("sub-a selected: want no Lua rows, got %q", lines)
	}
}

// TestSidebarTasks_ClickOnAgentRowSelectsItsBash: a click on a Subagents row
// selects that agent, as Enter does. The row has a live transcript, so the
// click opens its agent view and the sidebar stays open.
func TestSidebarTasks_ClickOnAgentRowSelectsItsBash(t *testing.T) {
	m := ownerTestModel(t)
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
	m = pressSidebarKey(t, m, tea.KeyDown) // sub-a
	m = pressSidebarKey(t, m, tea.KeyEnter)
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-a"}) {
		t.Fatalf("setup: want [bash-a] on sub-a, got %v", got)
	}

	m.sidebarSwitchTab(sidebarTabTokens)
	m.sidebarSwitchTab(sidebarTabTasks)
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-main"}) {
		t.Fatalf("after switching tabs: want [bash-main], got %v", got)
	}

	m = pressSidebarKey(t, m, tea.KeyDown) // sub-a again
	m = pressSidebarKey(t, m, tea.KeyEnter)
	m.closeSidebar()
	m.openSidebar(sidebarTabTasks)
	if got := taskBashIDs(m); !slices.Equal(got, []string{"bash-main"}) {
		t.Fatalf("after reopening: want [bash-main], got %v", got)
	}
}
