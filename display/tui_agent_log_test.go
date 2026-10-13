package display

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/jobs"
)

// TestRunsTab_AgentRowsAreSelectable checks that the cursor walks the run
// rows and the agent rows of expanded runs, and that Enter on a run row still
// toggles the run.
func TestRunsTab_AgentRowsAreSelectable(t *testing.T) {
	j1 := treeTestJob("a1", "#1", "", jobs.StatusRunning, 1)
	j2 := treeTestJob("a2", "#1", "", jobs.StatusDone, 2)
	m := treeModel([]TuiRunRow{treeTestRun("#1", "running", 1)}, j1, j2)
	m.sidebarTab = sidebarTabRuns
	m.sidebarRunsExpanded = map[string]bool{"#1": true}
	if got := m.sidebarRowCount(); got != 3 {
		t.Fatalf("row count = %d, want run row plus two agent rows", got)
	}
	m.sidebarMoveCursor(1)
	m.sidebarMoveCursor(1)
	if m.sidebarCursor != 2 {
		t.Fatalf("cursor = %d, want the second agent row 2", m.sidebarCursor)
	}
	before := m.sidebarAskStop()
	if before.sidebarStopRun != "" {
		t.Fatalf("stop asked on an agent row: %q", before.sidebarStopRun)
	}
	m.sidebarCursor = 0
	model, _ := m.sidebarActivateRow()
	m = model.(TuiModel)
	if m.sidebarRunsExpanded["#1"] {
		t.Fatalf("Enter on the run row must collapse the run")
	}
}

// TestTree_EnterOnAgentOpensViewer checks that Enter on an agent row opens the
// log viewer titled "<run> / <agent>", and that an agent without a transcript
// shows "(no log yet)".
func TestTree_EnterOnAgentOpensViewer(t *testing.T) {
	j := treeTestJob("a1", "#1", "", jobs.StatusRunning, 1)
	j.Transcript = filepath.Join(t.TempDir(), "a1.jsonl")
	m := treeModel([]TuiRunRow{treeTestRun("#1", "running", 1)}, j)
	m.sidebarTab = sidebarTabRuns
	m.sidebarRunsExpanded = map[string]bool{"#1": true}
	m.sidebarCursor = 1
	model, cmd := m.sidebarActivateRow()
	m = model.(TuiModel)
	if !m.subagentModalActive || m.subagentModalTitle != "#1 / worker a1" {
		t.Fatalf("viewer not open or wrong title: active=%v title=%q", m.subagentModalActive, m.subagentModalTitle)
	}
	if m.agentLogJob != "a1" || m.agentLogPath != j.Transcript || cmd == nil {
		t.Fatalf("viewer not wired to the transcript: job=%q path=%q cmd=%v", m.agentLogJob, m.agentLogPath, cmd)
	}
	if !m.agentLogTailing() {
		t.Fatalf("a running agent must be tailed")
	}

	// An ad-hoc agent has no transcript.
	adhoc := treeModel(nil, treeTestJob("x", "", "", jobs.StatusDone, 1))
	adhoc.sidebarTab = sidebarTabRuns
	adhoc.sidebarRunsExpanded = map[string]bool{treeAdHocID: true}
	adhoc.sidebarCursor = 1
	model, _ = adhoc.sidebarActivateRow()
	adhoc = model.(TuiModel)
	if !adhoc.subagentModalActive || adhoc.subagentModalText() != "(no log yet)" {
		t.Fatalf("ad-hoc agent must show (no log yet), got %q", adhoc.subagentModalText())
	}
	if adhoc.agentLogTailing() {
		t.Fatalf("an agent without transcript must not be tailed")
	}
}

// TestTranscriptViewer_LiveTailKeepsScroll checks that a grown transcript
// replaces the text and keeps a scrolled view on the same lines, and that a
// read for another agent is dropped.
func TestTranscriptViewer_LiveTailKeepsScroll(t *testing.T) {
	lines := func(n int) []string {
		var out []string
		for i := 1; i <= n; i++ {
			out = append(out, fmt.Sprintf("line %d", i))
		}
		return out
	}
	m := newTestModel()
	m.subagentModalActive = true
	m.subagentModalBlockIdx = -1
	m.agentLogJob = "a1"
	m.agentLogPath = "/unused"
	m.agentLogReading = true
	m.subagentModalStaticText = strings.Join(lines(100), "\n")
	m.subagentModalScroll = 5

	m = m.applyAgentLog(agentLogMsg{jobID: "a1", lines: lines(102), running: true})
	if m.agentLogReading {
		t.Fatalf("reading flag not cleared after the read")
	}
	if !strings.HasSuffix(m.subagentModalText(), "line 102") {
		t.Fatalf("text not replaced by the new read")
	}
	if m.subagentModalScroll != 7 {
		t.Fatalf("scroll = %d, want 7: the two new lines must keep the view in place", m.subagentModalScroll)
	}

	m = m.applyAgentLog(agentLogMsg{jobID: "other", lines: lines(40), running: true})
	if strings.HasSuffix(m.subagentModalText(), "line 40") {
		t.Fatalf("read for another agent was applied")
	}

	m = m.applyAgentLog(agentLogMsg{jobID: "a1", lines: lines(102), running: false})
	if m.agentLogTailing() {
		t.Fatalf("a finished agent must stop the tail")
	}
}
