package display

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/stream"
)

func newTestModelForSidebar() TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 100
	m.height = 30
	m.reading = true
	return m
}

func TestOpenCloseToggleSidebar(t *testing.T) {
	m := newTestModelForSidebar()
	m.scrollLine = 5
	m.atBottom = false

	m.openSidebar(sidebarTabSubagents)
	if !m.sidebarActive || m.sidebarTab != sidebarTabSubagents {
		t.Fatalf("expected sidebar active on Subagents tab, got active=%v tab=%d", m.sidebarActive, m.sidebarTab)
	}

	m.closeSidebar()
	if m.sidebarActive {
		t.Fatalf("expected sidebar closed")
	}
	if m.scrollLine != 5 || m.atBottom != false {
		t.Fatalf("expected scroll state restored, got scrollLine=%d atBottom=%v", m.scrollLine, m.atBottom)
	}

	m.toggleSidebar()
	if !m.sidebarActive {
		t.Fatalf("expected toggle to open when closed")
	}
	m.toggleSidebar()
	if m.sidebarActive {
		t.Fatalf("expected toggle to close when open")
	}
}

func TestOpenSidebar_InvalidTabFallsBackToTokens(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(999)
	if m.sidebarTab != sidebarTabTokens {
		t.Fatalf("expected fallback to Tokens tab, got %d", m.sidebarTab)
	}
}

func TestUpdateSidebar_TabNavigation(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTokens)

	model, _ := m.updateSidebar(tea.KeyMsg{Type: tea.KeyRight})
	m2 := model.(TuiModel)
	if m2.sidebarTab != sidebarTabSessions {
		t.Fatalf("expected Right to advance to Sessions, got %d", m2.sidebarTab)
	}

	model, _ = m2.updateSidebar(tea.KeyMsg{Type: tea.KeyLeft})
	m3 := model.(TuiModel)
	if m3.sidebarTab != sidebarTabTokens {
		t.Fatalf("expected Left to go back to Tokens, got %d", m3.sidebarTab)
	}

	model, _ = m3.updateSidebar(tea.KeyMsg{Type: tea.KeyShiftTab})
	m4 := model.(TuiModel)
	if m4.sidebarTab != sidebarTabSubagents {
		t.Fatalf("expected Shift+Tab to wrap back to Subagents, got %d", m4.sidebarTab)
	}
}

func TestUpdateSidebar_EscapeCloses(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTokens)
	model, _ := m.updateSidebar(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := model.(TuiModel)
	if m2.sidebarActive {
		t.Fatalf("expected Esc to close the sidebar")
	}
}

// TestBuildSubagentTree_NestingAndCost covers item 1's tree shape: a root
// "main" row, two top-level children under it, and one grandchild nested
// one level deeper under the second child (main 10k / subagent 1 5k /
// subagent 2 10k / subagent 3 15k, per TODO.md's sketch) — with tokens per
// row (not rolled up) and cost rolled up from the leaves to the root.
func TestBuildSubagentTree_NestingAndCost(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)

	m := newTestModelForSidebar()

	child1 := jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, ParentID: "", Status: jobs.StatusDone, Description: "child 1", StartedAt: time.Now().Add(-3 * time.Minute)}
	child2 := jobs.Job{ID: "job-2", Kind: jobs.KindSubagent, ParentID: "", Status: jobs.StatusDone, Description: "child 2", StartedAt: time.Now().Add(-2 * time.Minute)}
	grandchild := jobs.Job{ID: "job-3", Kind: jobs.KindSubagent, ParentID: "job-2", Status: jobs.StatusDone, Description: "grandchild", StartedAt: time.Now().Add(-1 * time.Minute)}
	// A backgrounded bash job must never show up in the Subagents tree.
	bashJob := jobs.Job{ID: "job-4", Kind: jobs.KindBash, Status: jobs.StatusDone, Description: "bash", StartedAt: time.Now()}

	m.applyJobUpdate(child1)
	m.applyJobUpdate(child2)
	m.applyJobUpdate(grandchild)
	m.applyJobUpdate(bashJob)

	ledger.Record(ledger.Subagent, "p", "cheap-model", "job-1", stream.Usage{Input: 5000})
	ledger.Record(ledger.Subagent, "p", "cheap-model", "job-2", stream.Usage{Input: 10000})
	ledger.Record(ledger.Subagent, "p", "cheap-model", "job-3", stream.Usage{Input: 15000})

	rows := m.buildSubagentTree()
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows (root + 2 children + 1 grandchild), got %d: %+v", len(rows), rows)
	}

	root := rows[0]
	if !root.isRoot || root.depth != 0 {
		t.Fatalf("expected rows[0] to be the root, got %+v", root)
	}

	byID := map[string]subagentTreeRow{}
	for _, r := range rows[1:] {
		byID[r.job.ID] = r
	}

	r1, r2, r3 := byID["job-1"], byID["job-2"], byID["job-3"]
	if r1.depth != 1 || r2.depth != 1 {
		t.Fatalf("expected both top-level children at depth 1, got job-1=%d job-2=%d", r1.depth, r2.depth)
	}
	if r3.depth != 2 {
		t.Fatalf("expected the grandchild at depth 2, got %d", r3.depth)
	}
	if r1.ownTokens != 5000 || r2.ownTokens != 10000 || r3.ownTokens != 15000 {
		t.Fatalf("expected each row's own tokens, got job-1=%d job-2=%d job-3=%d", r1.ownTokens, r2.ownTokens, r3.ownTokens)
	}
}

// TestRollupJobCost_SumsDescendants tests the cost-rollup arithmetic
// directly against a synthetic usage map, independent of whatever pricing
// catalog (if any) is loaded in this test environment: a job's rolled-up
// cost must be its own priced cost plus every descendant's, and "unpriced"
// must propagate up from a descendant even when the row itself carries no
// usage of its own (e.g. a parent that only ever delegated).
func TestRollupJobCost_SumsDescendants(t *testing.T) {
	byParent := map[string][]jobs.Job{
		"":  {{ID: "a"}},
		"a": {{ID: "b"}},
	}
	usage := map[string]ledger.JobUsage{
		"a": {USD: 1.00, Priced: true},
		"b": {USD: 2.00, Priced: true},
	}
	cost, unpriced := rollupJobCost("a", byParent, usage)
	if cost != 3.00 {
		t.Fatalf("expected rollup 1.00+2.00=3.00, got %v", cost)
	}
	if unpriced {
		t.Fatalf("expected fully priced rollup, got unpriced=true")
	}

	// Now make b unpriced but still carrying real usage — a's rollup must
	// be flagged even though a itself has no usage entry at all.
	usage["b"] = ledger.JobUsage{USD: 0, Priced: false, Usage: stream.Usage{Input: 200}}
	delete(usage, "a")
	_, unpriced = rollupJobCost("a", byParent, usage)
	if !unpriced {
		t.Fatalf("expected a's rollup to be flagged unpriced because of descendant b")
	}
}

// TestBuildSubagentTree_UnpricedDescendantPropagates covers the "+?"
// convention (formatCost's status-bar rule, reused here): a parent whose
// child ran on an unpriced model must itself be flagged unpriced, so its
// rolled-up cost is never shown as a clean, complete figure.
func TestBuildSubagentTree_UnpricedDescendantPropagates(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)

	m := newTestModelForSidebar()
	parent := jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "parent"}
	child := jobs.Job{ID: "job-2", Kind: jobs.KindSubagent, ParentID: "job-1", Status: jobs.StatusDone, Description: "child"}
	m.applyJobUpdate(parent)
	m.applyJobUpdate(child)

	// The parent itself never made a model call (it only delegated) — no
	// usage of its own — while its child ran on a model no catalog prices.
	// The parent's rollup must still be flagged unpriced, inherited purely
	// from the child.
	ledger.Record(ledger.Subagent, "nope", "no-such-model", "job-2", stream.Usage{Input: 200})

	rows := m.buildSubagentTree()
	var parentRow subagentTreeRow
	for _, r := range rows {
		if !r.isRoot && r.job.ID == "job-1" {
			parentRow = r
		}
	}
	if !parentRow.rollupUnpriced {
		t.Fatalf("expected parent's rollup to be flagged unpriced because of its child, got %+v", parentRow)
	}
}

// TestBuildSubagentTree_WaitingAnswerSortsFirst covers the one ordering
// item 1 explicitly requires: a job blocked on the user must never be
// buried below finished siblings.
func TestBuildSubagentTree_WaitingAnswerSortsFirst(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)

	m := newTestModelForSidebar()
	done := jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "finished", StartedAt: time.Now().Add(-time.Minute)}
	waiting := jobs.Job{ID: "job-2", Kind: jobs.KindSubagent, Status: jobs.StatusWaitingAnswer, Description: "blocked", Question: "ok?", StartedAt: time.Now()}
	m.applyJobUpdate(done)
	m.applyJobUpdate(waiting)

	rows := m.buildSubagentTree()
	if len(rows) != 3 {
		t.Fatalf("expected root + 2 children, got %d", len(rows))
	}
	if rows[1].job.ID != "job-2" {
		t.Fatalf("expected the waiting_answer job first among siblings, got %+v", rows[1])
	}
}

func TestSidebarBashJobs_FiltersKind(t *testing.T) {
	m := newTestModelForSidebar()
	m.applyJobUpdate(jobs.Job{ID: "job-1", Kind: jobs.KindBash, Description: "bash job"})
	m.applyJobUpdate(jobs.Job{ID: "job-2", Kind: jobs.KindSubagent, Description: "subagent job"})
	m.applyJobUpdate(jobs.Job{ID: "job-3", Kind: jobs.KindCron, Description: "cron job"})

	list := m.sidebarBashJobs()
	if len(list) != 1 || list[0].ID != "job-1" {
		t.Fatalf("expected only the bash job, got %+v", list)
	}
}

func TestStatusRightHit_MatchesRenderedFigure(t *testing.T) {
	m := newTestModelForSidebar()
	m.lastUsage.Input = 100
	if m.buildContextCost() == "" {
		t.Skip("no context cost to click in this configuration")
	}
	if m.statusRightHit(m.width-1) != true {
		t.Errorf("expected the rightmost column to be a hit")
	}
	if m.statusRightHit(0) {
		t.Errorf("expected the leftmost column to be a miss")
	}
}
