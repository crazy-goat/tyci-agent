package display

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

// TestSidebarTabAtX_MatchesRenderedTabPositions is the regression test for
// the hit-test/renderer offset mismatch a review round caught: sidebarTabAtX
// used to divide by layout.width and assume the tab row started at
// layout.left, while renderSidebarTabs actually laid tabs out over
// layout.contentWidth starting at layout.contentLeft (border + padding
// past layout.left) — so a click on "Bash" selected "Sessions", and so on.
// This renders the real sidebar, finds each tab label's actual on-screen
// column, and checks sidebarTabAtX maps a click there back to that same
// tab — for every tab, not just the first (which happened to still work by
// coincidence under the old, wrong formula).
func TestSidebarTabAtX_MatchesRenderedTabPositions(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTokens)
	layout := m.sidebarLayout()

	rendered := m.renderSidebarView()
	rows := strings.Split(rendered, "\n")
	tabRow := ansi.Strip(rows[layout.top+1])

	// Cells can be narrower than a tab's full name (renderSidebarTabs
	// truncates with an ellipsis — e.g. "Sessions" -> "Sessio…" at this
	// test's width), so search for whatever it actually rendered, exactly
	// as it computes it, rather than the untruncated name.
	cell := layout.contentWidth / sidebarTabCount
	for tab, name := range sidebarTabNames {
		label := truncateToWidth(name, cell)
		byteIdx := strings.Index(tabRow, label)
		if byteIdx < 0 {
			t.Fatalf("tab %d's rendered label %q not found in tab row: %q", tab, label, tabRow)
		}
		// tabRow contains multi-byte runes (the border character), so a
		// byte offset from strings.Index is not a screen column — convert
		// via display width of everything before the match, same unit
		// sidebarTabAtX's x parameter (a mouse event's column) is in.
		col := lipgloss.Width(tabRow[:byteIdx])
		if got := sidebarTabAtX(layout, col); got != tab {
			t.Errorf("sidebarTabAtX(%d) [start of %q's rendered label] = %d, want %d", col, label, got, tab)
		}
		// A click anywhere else within the same label's text must also
		// resolve to this tab, not just its very first column.
		lastCol := col + lipgloss.Width(label) - 1
		if got := sidebarTabAtX(layout, lastCol); got != tab {
			t.Errorf("sidebarTabAtX(%d) [end of %q's rendered label] = %d, want %d", lastCol, label, got, tab)
		}
	}
}

// TestSidebarMouse_TabClickAndBorderMargin exercises the same fix through
// the actual mouse handler (not just the raw sidebarTabAtX function): a
// click squarely inside a tab's cell selects it, a click on the panel's own
// border/left-padding column (part of the panel, just not a tab or content
// cell) does neither — it must not misfire as a tab click, and per the
// review finding it must NOT close the sidebar either, since it is still
// inside the panel's own bounding box. Only a click truly outside that box
// closes it (covered by TestUpdateSidebar's mouse-outside-closes case,
// unchanged by this fix).
func TestSidebarMouse_TabClickAndBorderMargin(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTokens)
	layout := m.sidebarLayout()

	// Click squarely inside the Subagents cell of the tab row.
	cell := layout.contentWidth / sidebarTabCount
	x := layout.contentLeft + sidebarTabSubagents*cell + cell/2
	model, _ := m.updateSidebar(tea.MouseMsg{
		X: x, Y: layout.top + 1,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m2 := model.(TuiModel)
	if m2.sidebarTab != sidebarTabSubagents {
		t.Fatalf("expected clicking the Subagents cell to select it, got tab %d", m2.sidebarTab)
	}
	if !m2.sidebarActive {
		t.Fatalf("expected the sidebar to stay open after a tab click")
	}

	// Click on the border column: inside the panel's bounding box
	// (layout.left), but left of layout.contentLeft — must not close the
	// panel and must not change the tab.
	model, _ = m2.updateSidebar(tea.MouseMsg{
		X: layout.left, Y: layout.top + 1,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m3 := model.(TuiModel)
	if !m3.sidebarActive {
		t.Fatalf("expected a click on the panel's own border column to leave the sidebar open")
	}
	if m3.sidebarTab != sidebarTabSubagents {
		t.Fatalf("expected the border-column click to leave the tab selection unchanged, got %d", m3.sidebarTab)
	}

	// A click truly outside the panel's footprint must close it.
	model, _ = m3.updateSidebar(tea.MouseMsg{
		X: layout.left - 1, Y: layout.top + 1,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m4 := model.(TuiModel)
	if m4.sidebarActive {
		t.Fatalf("expected a click outside the panel's footprint to close it")
	}
}

// TestSidebarScroll_SelectableTabKeepsCursorVisible covers the missing-
// scroll-offset bug: on a long list (more bash jobs than fit), moving the
// cursor past the visible window must move sidebarScroll to keep it in
// view, and the rendered content must actually reflect that offset (not
// just the first contentHeight lines every time).
func TestSidebarScroll_SelectableTabKeepsCursorVisible(t *testing.T) {
	m := newTestModelForSidebar()
	m.height = 15 // small height -> small contentHeight, so the list overflows easily
	for i := 0; i < 30; i++ {
		m.applyJobUpdate(jobs.Job{
			ID:        jobs.ShortID(fmt.Sprintf("job-%d", i)),
			Kind:      jobs.KindBash,
			Status:    jobs.StatusDone,
			StartedAt: time.Now().Add(-time.Duration(30-i) * time.Minute),
		})
	}
	m.openSidebar(sidebarTabBash)
	layout := m.sidebarLayout()
	if layout.contentHeight >= 30 {
		t.Skip("terminal too tall for this test to exercise overflow")
	}

	for i := 0; i < 29; i++ {
		m.sidebarMoveCursor(1)
	}
	if m.sidebarCursor != 29 {
		t.Fatalf("expected cursor at the last row (29), got %d", m.sidebarCursor)
	}
	if m.sidebarScroll+layout.contentHeight <= m.sidebarCursor {
		t.Fatalf("expected sidebarScroll to keep the cursor in view: scroll=%d contentHeight=%d cursor=%d",
			m.sidebarScroll, layout.contentHeight, m.sidebarCursor)
	}

	// The rendered content must actually start at sidebarScroll, not 0.
	lines := m.sidebarBashJobs()
	rendered := m.renderSidebarView()
	rows := strings.Split(rendered, "\n")
	firstContentRow := ansi.Strip(rows[layout.contentTop])
	lastJobShortID := jobs.ShortID(lines[m.sidebarScroll].ID)
	if !strings.Contains(firstContentRow, "#"+lastJobShortID) {
		t.Fatalf("expected the first visible content row to show job at scroll offset %d (id %s), got %q",
			m.sidebarScroll, lastJobShortID, firstContentRow)
	}
}

// TestSidebarResize_ReclampsStaleScrollAndClickMapping covers the review
// finding that a resize could leave sidebarScroll stale: scrolled to the
// bottom of a long list at a small height, then the terminal grows enough
// that the whole list fits without scrolling — sidebarScroll must be
// re-clamped immediately (on the WindowSizeMsg itself), and a subsequent
// row click must map against that corrected value, not the pre-resize one.
func TestSidebarResize_ReclampsStaleScrollAndClickMapping(t *testing.T) {
	m := newTestModelForSidebar()
	m.width = 100
	m.height = 15
	for i := 0; i < 10; i++ {
		m.applyJobUpdate(jobs.Job{
			ID:          fmt.Sprintf("job-%d", i),
			Kind:        jobs.KindBash,
			Status:      jobs.StatusDone,
			Description: fmt.Sprintf("job-%d desc", i),
			StartedAt:   time.Now().Add(-time.Duration(10-i) * time.Minute),
		})
	}
	m.openSidebar(sidebarTabBash)
	smallLayout := m.sidebarLayout()
	if smallLayout.contentHeight >= 10 {
		t.Skip("terminal too tall for this test to exercise overflow at the small size")
	}
	for i := 0; i < 9; i++ {
		m.sidebarMoveCursor(1)
	}
	if m.sidebarScroll == 0 {
		t.Fatalf("expected a non-zero scroll before resizing (the list should have overflowed)")
	}

	model, _ := m.updateSidebar(tea.WindowSizeMsg{Width: 100, Height: 60})
	m2 := model.(TuiModel)
	bigLayout := m2.sidebarLayout()
	if bigLayout.contentHeight < 10 {
		t.Skip("expected the larger height to comfortably fit all 10 rows")
	}
	if m2.sidebarScroll != 0 {
		t.Fatalf("expected sidebarScroll to be re-clamped to 0 once everything fits, got %d", m2.sidebarScroll)
	}

	// A click on the first content row must open row 0's job (the newest —
	// sortedBackgroundJobs is newest-first — "job-9"), not some row offset
	// by the stale, pre-resize scroll (9), which would either open the
	// wrong job or hit nothing at all. sidebarActivateRow closes the
	// sidebar and resets sidebarCursor to 0 unconditionally on success, so
	// the modal's own title (not the cursor field) is what proves which row
	// was actually picked.
	model, _ = m2.updateSidebar(tea.MouseMsg{
		X: bigLayout.contentLeft, Y: bigLayout.contentTop,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m3 := model.(TuiModel)
	if !m3.subagentModalActive {
		t.Fatalf("expected the click to open a job result modal")
	}
	if want := "job-9 desc"; m3.subagentModalTitle != want {
		t.Fatalf("expected the click to open %q (row 0 post-resize), got %q", want, m3.subagentModalTitle)
	}
}

// TestSidebarScroll_NonSelectableTabScrollsWithoutACursor covers the Tokens/
// Lua tabs: sidebarRowCount is 0 for them (no selectable rows — see
// sidebarSelectable), so Up/Down/wheel must scroll the view directly
// instead of being no-ops just because there's no cursor to move.
func TestSidebarScroll_NonSelectableTabScrollsWithoutACursor(t *testing.T) {
	m := newTestModelForSidebar()
	m.height = 15
	m.openSidebar(sidebarTabTokens)
	layout := m.sidebarLayout()

	lineCount := m.sidebarLineCount(layout.contentWidth)
	if lineCount <= layout.contentHeight {
		// Pad the ledger with enough rows that Tokens overflows the panel.
		for i := 0; i < 20; i++ {
			ledger.Record(ledger.Subagent, "p", fmt.Sprintf("model-%d", i), fmt.Sprintf("job-%d", i), stream.Usage{Input: 100, Output: 10})
		}
		t.Cleanup(ledger.Reset)
		lineCount = m.sidebarLineCount(layout.contentWidth)
		if lineCount <= layout.contentHeight {
			t.Skip("could not produce enough Tokens content to overflow the panel in this configuration")
		}
	}

	m.sidebarMoveCursor(1)
	if m.sidebarScroll == 0 {
		t.Fatalf("expected Down to scroll a non-selectable tab with overflowing content")
	}
	if m.sidebarCursor != 0 {
		t.Fatalf("expected sidebarCursor to stay 0 on a non-selectable tab, got %d", m.sidebarCursor)
	}
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

// TestBuildSubagentTree_OrphanUnderNonSubagentParentStillRenders covers the
// review finding that a subagent whose ParentID points at a job that isn't
// itself a tracked subagent — a cron run, or a parent since evicted from
// backgroundJobs — used to be silently unreachable from the root: byParent
// only got walked starting from root's own subagent children, so a group
// keyed by a non-subagent (or missing) parent id was never visited. It
// must now show up as a depth-1 orphan instead of vanishing.
func TestBuildSubagentTree_OrphanUnderNonSubagentParentStillRenders(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)

	m := newTestModelForSidebar()
	cronParent := jobs.Job{ID: "cron-1", Kind: jobs.KindCron, Status: jobs.StatusDone, Description: "scheduled run"}
	child := jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, ParentID: "cron-1", Status: jobs.StatusDone, Description: "spawned by cron"}
	m.applyJobUpdate(cronParent)
	m.applyJobUpdate(child)

	rows := m.buildSubagentTree()
	var found *subagentTreeRow
	for i := range rows {
		if !rows[i].isRoot && rows[i].job.ID == "job-1" {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatalf("expected the orphaned subagent to still appear in the tree, got rows: %+v", rows)
	}
	if found.depth != 1 {
		t.Fatalf("expected the orphan to render at depth 1, got %d", found.depth)
	}

	// Also cover a parent id with no job at all (e.g. evicted from
	// backgroundJobs) — same expectation, no crash, still a depth-1 row.
	m2 := newTestModelForSidebar()
	orphan := jobs.Job{ID: "job-2", Kind: jobs.KindSubagent, ParentID: "long-gone", Status: jobs.StatusDone, Description: "parent evicted"}
	m2.applyJobUpdate(orphan)
	rows2 := m2.buildSubagentTree()
	if len(rows2) != 2 {
		t.Fatalf("expected root + 1 orphan row, got %d: %+v", len(rows2), rows2)
	}
	if rows2[1].job.ID != "job-2" || rows2[1].depth != 1 {
		t.Fatalf("expected job-2 at depth 1, got %+v", rows2[1])
	}
}

// TestBuildSubagentTree_ChainedOrphansDoNotDuplicate covers the review
// finding that orphan emission could list the same job twice: group "P"
// (an unreached, non-subagent parent) contains subagent "A"; "A" is ITSELF
// a byParent key because subagent "B" has ParentID="A". Both "A" and "P"
// end up in orphanParents (collected once, before either is walked).
// Walking "A" first (alphabetically before "P") already reaches "B"; the
// unfixed code then walked "P" too, re-adding "B" a second time as if "A"
// had no children of its own yet. B must appear exactly once.
func TestBuildSubagentTree_ChainedOrphansDoNotDuplicate(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)

	m := newTestModelForSidebar()
	// "P" is never actually created as a job — same as the "parent evicted"
	// case above, an orphan chain doesn't require its root to exist.
	a := jobs.Job{ID: "A", Kind: jobs.KindSubagent, ParentID: "P", Status: jobs.StatusDone, Description: "a"}
	b := jobs.Job{ID: "B", Kind: jobs.KindSubagent, ParentID: "A", Status: jobs.StatusDone, Description: "b"}
	m.applyJobUpdate(a)
	m.applyJobUpdate(b)

	rows := m.buildSubagentTree()
	counts := map[string]int{}
	for _, r := range rows {
		if !r.isRoot {
			counts[r.job.ID]++
		}
	}
	if counts["A"] != 1 {
		t.Fatalf("expected job A to appear exactly once, got %d (rows: %+v)", counts["A"], rows)
	}
	if counts["B"] != 1 {
		t.Fatalf("expected job B to appear exactly once, got %d (rows: %+v)", counts["B"], rows)
	}
}

// TestBuildSubagentTree_ParentIDCycleDoesNotHang covers the defensive cycle
// guard: a ParentID loop (A's parent is B, B's parent is A) must not hang
// or crash the tree build, even though this cannot occur through the normal
// spawn path today.
func TestBuildSubagentTree_ParentIDCycleDoesNotHang(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)

	m := newTestModelForSidebar()
	a := jobs.Job{ID: "A", Kind: jobs.KindSubagent, ParentID: "B", Status: jobs.StatusDone, Description: "a"}
	b := jobs.Job{ID: "B", Kind: jobs.KindSubagent, ParentID: "A", Status: jobs.StatusDone, Description: "b"}
	m.applyJobUpdate(a)
	m.applyJobUpdate(b)

	done := make(chan []subagentTreeRow, 1)
	go func() { done <- m.buildSubagentTree() }()
	select {
	case rows := <-done:
		counts := map[string]int{}
		for _, r := range rows {
			if !r.isRoot {
				counts[r.job.ID]++
			}
		}
		if counts["A"] != 1 || counts["B"] != 1 {
			t.Fatalf("expected each of A and B exactly once, got A=%d B=%d (rows: %+v)", counts["A"], counts["B"], rows)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("buildSubagentTree did not return — likely an infinite recursion on the ParentID cycle")
	}
}

// TestRollupJobCost_CycleDoesNotHang mirrors the same guard directly on
// rollupJobCost's own independent recursion over byParent.
func TestRollupJobCost_CycleDoesNotHang(t *testing.T) {
	byParent := map[string][]jobs.Job{
		"A": {{ID: "B"}},
		"B": {{ID: "A"}},
	}
	done := make(chan struct{})
	go func() {
		rollupJobCost("A", byParent, map[string]ledger.JobUsage{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("rollupJobCost did not return — likely an infinite recursion on the cycle")
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

// TestSidebarSubmitResume_RefusesMidTurn covers the review finding that
// submitting "/resume" while the agent is mid-turn (m.reading == false)
// would either queue the literal string "/resume" as a real user message or
// otherwise misfire — it must refuse instead, leaving the input untouched.
// TestSidebarRefusalMessages_FitStatusBarTruncation covers the review
// finding that the refusal status messages were long enough that
// buildStatus's 60-column hard truncation (tui_status.go) cut off the
// actionable half mid-sentence. Every message these two actions can set
// must fit comfortably within that cap.
func TestSidebarRefusalMessages_FitStatusBarTruncation(t *testing.T) {
	const statusBarCap = 60

	m := newTestModelForSidebar()
	m.reading = false
	m.openSidebar(sidebarTabSessions)
	model, _ := m.sidebarSubmitResume()
	msg1 := model.(TuiModel).statusMessage

	m2 := newTestModelForSidebar()
	m2.reading = true
	m2.input.SetValue("x")
	m2.openSidebar(sidebarTabSessions)
	model, _ = m2.sidebarSubmitResume()
	msg2 := model.(TuiModel).statusMessage

	m3 := newTestModelForSidebar()
	m3.input.SetValue("x")
	m3.applyJobUpdate(jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, Status: jobs.StatusDone})
	m3.openSidebar(sidebarTabSubagents)
	m3.sidebarCursor = 1
	model, _ = m3.sidebarResumeSubagentRow()
	msg3 := model.(TuiModel).statusMessage

	for _, msg := range []string{msg1, msg2, msg3} {
		if msg == "" {
			t.Fatalf("expected a non-empty refusal message")
		}
		if lipgloss.Width(msg) > statusBarCap {
			t.Errorf("refusal message %q is %d columns wide, want <= %d", msg, lipgloss.Width(msg), statusBarCap)
		}
	}
}

func TestSidebarSubmitResume_RefusesMidTurn(t *testing.T) {
	m := newTestModelForSidebar()
	m.reading = false // a turn is in flight
	m.openSidebar(sidebarTabSessions)

	model, _ := m.sidebarSubmitResume()
	m2 := model.(TuiModel)
	if m2.sidebarActive {
		t.Fatalf("expected the sidebar to close even when refusing")
	}
	if m2.input.Value() != "" {
		t.Fatalf("expected the input box to stay untouched, got %q", m2.input.Value())
	}
	if m2.statusMessage == "" {
		t.Fatalf("expected a status message explaining the refusal")
	}
}

// TestSidebarSubmitResume_RefusesWhenInputNotEmpty covers the review
// finding that Enter on the Sessions tab used to clobber whatever the user
// had half-typed with the literal "/resume" — it must refuse and leave the
// draft alone instead.
func TestSidebarSubmitResume_RefusesWhenInputNotEmpty(t *testing.T) {
	m := newTestModelForSidebar()
	m.reading = true
	m.input.SetValue("a half-written message")
	m.openSidebar(sidebarTabSessions)

	model, _ := m.sidebarSubmitResume()
	m2 := model.(TuiModel)
	if m2.input.Value() != "a half-written message" {
		t.Fatalf("expected the half-written input to survive, got %q", m2.input.Value())
	}
}

// TestSidebarResumeSubagentRow_RefusesWhenInputNotEmpty mirrors the same
// guard for the 'r' key on a Subagents row: it only drafts text, so
// clobbering a half-written message would be a pure loss.
func TestSidebarResumeSubagentRow_RefusesWhenInputNotEmpty(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)

	m := newTestModelForSidebar()
	m.input.SetValue("don't overwrite me")
	m.applyJobUpdate(jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "child"})
	m.openSidebar(sidebarTabSubagents)
	m.sidebarCursor = 1 // the one real row past the synthetic root

	model, _ := m.sidebarResumeSubagentRow()
	m2 := model.(TuiModel)
	if m2.input.Value() != "don't overwrite me" {
		t.Fatalf("expected the existing input to survive, got %q", m2.input.Value())
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
