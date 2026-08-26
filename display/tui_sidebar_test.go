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

	rendered := m.renderSidebarColumn()
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
		// via display width of everything before the match. renderSidebarColumn
		// now returns just the sidebar's own self-contained box (local
		// columns starting at 0), not the old full-screen-width frame, so
		// this local column has to be shifted by layout.left to become the
		// same absolute-screen-X unit sidebarTabAtX's x parameter (a real
		// mouse event's column) is in.
		col := layout.left + lipgloss.Width(tabRow[:byteIdx])
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
	x := layout.contentLeft + sidebarTabTasks*cell + cell/2
	model, _ := m.updateSidebar(tea.MouseMsg{
		X: x, Y: layout.top + 1,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m2 := model.(TuiModel)
	if m2.sidebarTab != sidebarTabTasks {
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
	if m3.sidebarTab != sidebarTabTasks {
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

// TestSidebarVisibleScrollForRenderedLineCount matches the rendered-content
// scroll clamp while reusing the line count already obtained for the frame.
// Keeping this path equivalent to sidebarVisibleScroll preserves click/resize
// scroll semantics while renderSidebarColumn avoids rebuilding tab content.
func TestSidebarVisibleScrollForRenderedLineCount(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTasks)
	layout := m.sidebarLayout()
	m.sidebarScroll = 99
	lineCount := len(m.sidebarTabLines(layout.contentWidth))
	got := m.sidebarVisibleScrollForLineCount(layout, lineCount)
	want := m.sidebarVisibleScroll(layout)
	if got != want {
		t.Fatalf("rendered line-count clamp = %d, regular clamp = %d", got, want)
	}

	m.sidebarScroll = -1
	if got, want := m.sidebarVisibleScrollForLineCount(layout, lineCount), 0; got != want {
		t.Fatalf("negative scroll clamp = %d, want %d", got, want)
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
	m.openSidebar(sidebarTabTasks)
	layout := m.sidebarLayout()
	if layout.contentHeight >= 30 {
		t.Skip("terminal too tall for this test to exercise overflow")
	}

	for i := 0; i < 29; i++ {
		m.sidebarMoveCursor(1)
	}
	if m.sidebarCursor != 29 {
		t.Fatalf("expected cursor at the last Bash row (29), got %d", m.sidebarCursor)
	}
	if m.sidebarScroll+layout.contentHeight <= m.sidebarCursor {
		t.Fatalf("expected sidebarScroll to keep the cursor in view: scroll=%d contentHeight=%d cursor=%d",
			m.sidebarScroll, layout.contentHeight, m.sidebarCursor)
	}

	// The rendered content must actually start at sidebarScroll, not 0.
	lines := m.sidebarBashJobs()
	rendered := m.renderSidebarColumn()
	rows := strings.Split(rendered, "\n")
	firstContentRow := ansi.Strip(rows[layout.contentTop])
	jobRows := m.sidebarTaskJobRows(m.sidebarLayout().contentWidth)
	lineAtScroll := m.sidebarScroll
	jobAtScroll := 0
	for _, row := range jobRows {
		if row >= lineAtScroll {
			jobAtScroll = row
			break
		}
	}
	if jobAtScroll == 0 {
		t.Fatal("expected a task job row at the scroll offset")
	}
	jobIndex := 0
	for _, row := range jobRows {
		if row == jobAtScroll {
			break
		}
		jobIndex++
	}
	lastJobShortID := jobs.ShortID(lines[jobIndex].ID)
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
	m.openSidebar(sidebarTabTasks)
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

// TestSidebarMouse_TaskClickPastJobCountBound is F3's regression test: the
// old hit-test bounded the clicked line against m.sidebarRowCount(), which
// on Tasks counts only job rows (5 here), not the actual rendered line
// count (Subagents heading + root + Bash heading + 5 Bash jobs + Lua
// heading = 9). A click on one of the later Bash rows (line index 6 or 7,
// past the job-count bound of 5 but still well within the 9 real lines)
// was silently dropped even though a job sat right there. It also caught
// the sibling bug of re-adding the scroll offset a second time when
// mapping the click to a job (the old code's `line := row + scroll`, when
// `row` was already scroll-adjusted) — reachable only with a non-zero
// scroll, which this test forces via a one-line contentHeight.
func TestSidebarMouse_TaskClickPastJobCountBound(t *testing.T) {
	m := newTestModelForSidebar()
	m.height = 7 // contentHeight = height-6 = 1, so a small scroll offset is easy to force
	for i := 0; i < 5; i++ {
		m.applyJobUpdate(jobs.Job{
			ID:          fmt.Sprintf("job-%d", i),
			Kind:        jobs.KindBash,
			Status:      jobs.StatusDone,
			Description: fmt.Sprintf("job-%d desc", i),
			StartedAt:   time.Now().Add(-time.Duration(5-i) * time.Minute),
		})
	}
	m.openSidebar(sidebarTabTasks)
	layout := m.sidebarLayout()
	if layout.contentHeight != 1 {
		t.Skip("expected a 1-row content window at this height")
	}

	width := layout.contentWidth
	rows := m.sidebarTaskRows(width)
	jobRows := m.sidebarTaskJobRows(width)
	if len(jobRows) != 5 {
		t.Fatalf("expected 5 job rows, got %d", len(jobRows))
	}
	// The last Bash job's line index (Subagents heading + root + Bash
	// heading + 4 earlier jobs = index 7) exceeds sidebarRowCount()'s job
	// count of 5, which is exactly the bound the old code used.
	lastJobLine := jobRows[len(jobRows)-1]
	if lastJobLine < m.sidebarRowCount() {
		t.Fatalf("test setup didn't reproduce the bug: last job line %d is not past the job-count bound %d", lastJobLine, m.sidebarRowCount())
	}
	wantDesc := rows[lastJobLine].job.Description

	m.sidebarScroll = lastJobLine // scrolled so the last job row is the one visible line
	model, _ := m.updateSidebar(tea.MouseMsg{
		X: layout.contentLeft, Y: layout.contentTop,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m2 := model.(TuiModel)
	if !m2.subagentModalActive {
		t.Fatalf("expected the click on the last Bash row to open a job result modal")
	}
	if m2.subagentModalTitle != wantDesc {
		t.Fatalf("expected the click to open %q, got %q", wantDesc, m2.subagentModalTitle)
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

	m.openSidebar(sidebarTabTasks)
	if !m.sidebarActive || m.sidebarTab != sidebarTabTasks {
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

// TestUpdateSidebar_LeftRightCycleTabsWhileFocused covers the surviving
// navigation once the sidebar has keyboard focus: Left/Right still move
// between tabs exactly as before.
func TestUpdateSidebar_LeftRightCycleTabsWhileFocused(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTokens)
	m.sidebarFocused = true

	model, _ := m.updateSidebar(tea.KeyMsg{Type: tea.KeyRight})
	m2 := model.(TuiModel)
	if m2.sidebarTab != sidebarTabSessions {
		t.Fatalf("expected Right to advance to Sessions, got %d", m2.sidebarTab)
	}
	if !m2.sidebarFocused {
		t.Fatalf("expected focus to remain on the sidebar after a plain tab move")
	}

	model, _ = m2.updateSidebar(tea.KeyMsg{Type: tea.KeyLeft})
	m3 := model.(TuiModel)
	if m3.sidebarTab != sidebarTabTokens {
		t.Fatalf("expected Left to go back to Tokens, got %d", m3.sidebarTab)
	}
}

// TestUpdateSidebar_TabAndShiftTabSwitchModelNotTab covers the reversed
// decision (TODO item 1): Tab/ShiftTab must never switch sidebar tabs —
// they fall through to the same model-switching behavior
// (TuiModel.switchModel) the normal keymap's Tab/Shift+Tab has
// (tui_keys.go), regardless of sidebar focus or which tab is selected. This
// mirrors the assertion style tui_picker_test.go's switchModel tests use
// (read modelName/favIdx directly) rather than asserting on sidebarTab,
// since sidebarTab is exactly what must NOT change.
func TestUpdateSidebar_TabAndShiftTabSwitchModelNotTab(t *testing.T) {
	m := newPickerTestModel(testProviders, []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-20250514"}, "")
	m.reading = true
	m.modelName = "openai/gpt-4o"
	m.favIdx = 0
	m.openSidebar(sidebarTabSessions)
	m.sidebarFocused = true

	model, _ := m.updateSidebar(tea.KeyMsg{Type: tea.KeyTab})
	m2 := model.(TuiModel)
	if m2.sidebarTab != sidebarTabSessions {
		t.Fatalf("expected Tab to leave the sidebar tab unchanged, got %d", m2.sidebarTab)
	}
	if !m2.sidebarActive || !m2.sidebarFocused {
		t.Fatalf("expected Tab to leave the sidebar open and focused")
	}
	if m2.modelName != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("expected Tab to switch the model like the normal keymap, got %q", m2.modelName)
	}

	model, _ = m2.updateSidebar(tea.KeyMsg{Type: tea.KeyShiftTab})
	m3 := model.(TuiModel)
	if m3.sidebarTab != sidebarTabSessions {
		t.Fatalf("expected Shift+Tab to leave the sidebar tab unchanged, got %d", m3.sidebarTab)
	}
	if m3.modelName != "openai/gpt-4o" {
		t.Fatalf("expected Shift+Tab to switch the model back, got %q", m3.modelName)
	}
}

// TestUpdateSidebar_LeftAtFirstTabExitsFocus and
// TestUpdateSidebar_RightAtLastTabExitsFocus cover the legacy focus-boundary
// exit: at the leftmost/rightmost tab, Left/Right walk focus back OUT to the
// conversation instead of wrapping around to the other end. This remains for
// backwards compatibility; the primary focus-exit key is now Ctrl+Left / Ctrl+Right
// from any tab (TestUpdateSidebar_CtrlArrowsExitFocusFromAnyTab below).
func TestUpdateSidebar_LeftAtFirstTabExitsFocus(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTokens) // Tokens is tab 0
	m.sidebarFocused = true

	model, _ := m.updateSidebar(tea.KeyMsg{Type: tea.KeyLeft})
	m2 := model.(TuiModel)
	if m2.sidebarFocused {
		t.Fatalf("expected Left at the first tab to exit focus back to the conversation")
	}
	if !m2.sidebarActive {
		t.Fatalf("expected the sidebar to stay open, just unfocused")
	}
	if m2.sidebarTab != sidebarTabTokens {
		t.Fatalf("expected the tab selection to stay put on exit, got %d", m2.sidebarTab)
	}
}

func TestUpdateSidebar_RightAtLastTabExitsFocus(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTasks) // the last tab
	m.sidebarFocused = true

	model, _ := m.updateSidebar(tea.KeyMsg{Type: tea.KeyRight})
	m2 := model.(TuiModel)
	if m2.sidebarFocused {
		t.Fatalf("expected Right at the last tab to exit focus back to the conversation")
	}
	if m2.sidebarTab != sidebarTabTasks {
		t.Fatalf("expected the tab selection to stay put on exit, got %d", m2.sidebarTab)
	}
}

// TestUpdateSidebar_CtrlArrowsExitFocusFromAnyTab covers the primary
// focus-exit path: Ctrl+Left or Ctrl+Right walk focus back out to the
// conversation from any tab (not just the tab-row boundaries), leaving the
// sidebar open and the tab selection where it was. This is the symmetric
// inverse of the Ctrl+Right entry in routeSidebarMsg.
func TestUpdateSidebar_CtrlArrowsExitFocusFromAnyTab(t *testing.T) {
	for _, keyType := range []tea.KeyType{tea.KeyCtrlLeft, tea.KeyCtrlRight} {
		m := newTestModelForSidebar()
		m.openSidebar(sidebarTabTasks) // middle tab
		m.sidebarFocused = true

		model, _ := m.updateSidebar(tea.KeyMsg{Type: keyType})
		m2 := model.(TuiModel)
		if m2.sidebarFocused {
			t.Fatalf("expected %v to exit focus back to the conversation", keyType)
		}
		if !m2.sidebarActive {
			t.Fatalf("expected %v to leave the sidebar open, just unfocused", keyType)
		}
		if m2.sidebarTab != sidebarTabTasks {
			t.Fatalf("expected %v to keep the tab selection, got %d", keyType, m2.sidebarTab)
		}
	}
}

// ─── Conversation<->sidebar focus routing (Update()) ───────────────────────
//
// These exercise the actual production entry point, m.Update(), rather than
// updateSidebar directly, since the focus gate itself lives in
// routeSidebarMsg (tui_sidebar.go), called from Update() (tui_update.go).

// TestSidebarFocus_DefaultsToConversation covers the default on open: focus
// starts on the conversation, so a typed character reaches the input box
// exactly as if the sidebar were closed.
func TestSidebarFocus_DefaultsToConversation(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTokens)
	if m.sidebarFocused {
		t.Fatalf("expected focus to default to the conversation on open")
	}

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m2 := model.(TuiModel)
	if m2.input.Value() != "x" {
		t.Fatalf("expected the typed character to land in the input box, got %q", m2.input.Value())
	}
	if !m2.sidebarActive {
		t.Fatalf("expected the sidebar to stay open")
	}
	if m2.sidebarFocused {
		t.Fatalf("expected focus to remain on the conversation after an ordinary keystroke")
	}
}

// TestSidebarFocus_RightEntersSidebarFromConversation covers Ctrl+Right
// "walking into" the sidebar from the conversation side, landing on whichever
// tab was already selected rather than resetting to the first tab.
func TestSidebarFocus_RightEntersSidebarFromConversation(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTasks)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlRight})
	m2 := model.(TuiModel)
	if !m2.sidebarFocused {
		t.Fatalf("expected Ctrl+Right from the conversation to move focus onto the sidebar")
	}
	if m2.sidebarTab != sidebarTabTasks {
		t.Fatalf("expected entering focus to keep the already-selected tab, got %d", m2.sidebarTab)
	}
}

// TestSidebarFocus_PlainRightNotHijackedFromConversation covers the reason
// for the Ctrl+Right change: a plain Right typed in the prompt must keep
// reaching the input box (to move the prompt cursor rightward) instead of
// being stolen to enter the sidebar — which is what made it impossible to
// move the prompt cursor right while the sidebar was open.
func TestSidebarFocus_PlainRightNotHijackedFromConversation(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTasks)
	// Put some text in the input and the cursor at the start.
	m.input.SetValue("abc")
	m.input.SetCursor(0)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m2 := model.(TuiModel)
	if m2.sidebarFocused {
		t.Fatalf("expected plain Right to NOT enter the sidebar from the conversation")
	}
	if got := m2.inputCursorOffset(); got != 1 {
		t.Fatalf("expected plain Right to move the prompt cursor to col 1, got %d", got)
	}
}

// TestSidebarFocus_TypingWhileFocusedDoesNotReachInput is the mirror of
// TestSidebarFocus_DefaultsToConversation: once focus has moved to the
// sidebar, typing (including the Subagents tab's 'r' resume key) must not
// land in the input box.
func TestSidebarFocus_TypingWhileFocusedDoesNotReachInput(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTokens)
	m.sidebarFocused = true

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m2 := model.(TuiModel)
	if m2.input.Value() != "" {
		t.Fatalf("expected typing while sidebar-focused to leave the input untouched, got %q", m2.input.Value())
	}
	if !m2.sidebarFocused {
		t.Fatalf("expected focus to remain on the sidebar")
	}
}

// TestSidebarFocus_TabStillSwitchesModelWhenUnfocused covers the other half
// of the reversed decision: Tab/ShiftTab fall through to switchModel
// whether or not the sidebar currently has focus, since routeSidebarMsg
// deliberately never claims them.
func TestSidebarFocus_TabStillSwitchesModelWhenUnfocused(t *testing.T) {
	m := newPickerTestModel(testProviders, []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-20250514"}, "")
	m.reading = true
	m.modelName = "openai/gpt-4o"
	m.favIdx = 0
	m.openSidebar(sidebarTabTokens) // sidebarFocused defaults to false

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m2 := model.(TuiModel)
	if m2.modelName != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("expected Tab to switch the model even while unfocused, got %q", m2.modelName)
	}
	if m2.sidebarTab != sidebarTabTokens {
		t.Fatalf("expected the sidebar tab to stay put, got %d", m2.sidebarTab)
	}
}

// ─── Mouse column dispatch (Update()) ──────────────────────────────────────

// TestSidebarMouse_MainColumnClickRoutesToMainAndUnfocuses covers Update()'s
// physical-column mouse routing: a click at the status bar's context figure
// — physically inside the main column — must reach the main conversation's
// own mouse handling using a shadow model narrowed to mainColumnWidth()
// (not the real, wider m.width), and must return focus to the conversation
// since a click is itself an unambiguous signal of where the user's
// attention just went.
func TestSidebarMouse_MainColumnClickRoutesToMainAndUnfocuses(t *testing.T) {
	m := newTestModelForSidebar()
	m.lastUsage.Input = 100
	if m.buildContextCost() == "" {
		t.Skip("no context cost to click in this configuration")
	}
	m.openSidebar(sidebarTabTasks)
	m.sidebarFocused = true

	mainWidth := m.mainColumnWidth()
	model, _ := m.Update(tea.MouseMsg{
		X: mainWidth - 1, Y: m.statusBarY(),
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m2 := model.(TuiModel)
	if m2.width != m.width {
		t.Fatalf("expected the real width to survive the shadow-model dispatch, got %d want %d", m2.width, m.width)
	}
	if m2.sidebarFocused {
		t.Fatalf("expected the main-column click to return focus to the conversation")
	}
	if m2.sidebarTab != sidebarTabTokens {
		t.Fatalf("expected clicking the context figure to switch to the Tokens tab, got %d", m2.sidebarTab)
	}
}

// TestSidebarMouse_SidebarColumnClickFocusesSidebar is the mirror case: a
// click physically inside the sidebar column (a tab label) must reach
// updateSidebar and set focus onto the sidebar.
func TestSidebarMouse_SidebarColumnClickFocusesSidebar(t *testing.T) {
	m := newTestModelForSidebar()
	m.openSidebar(sidebarTabTokens)
	layout := m.sidebarLayout()

	cell := layout.contentWidth / sidebarTabCount
	x := layout.contentLeft + sidebarTabTasks*cell + cell/2
	model, _ := m.Update(tea.MouseMsg{
		X: x, Y: layout.top + 1,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m2 := model.(TuiModel)
	if !m2.sidebarFocused {
		t.Fatalf("expected a click inside the sidebar column to focus it")
	}
	if m2.sidebarTab != sidebarTabTasks {
		t.Fatalf("expected clicking the Subagents cell to select it, got %d", m2.sidebarTab)
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

// TestBuildSubagentTree_UnpricedDescendantPropagates covers the unpriced
// propagation rule: a parent whose child ran on an unpriced model must
// itself be flagged unpriced. (The UI no longer decorates the cost with a
// "+?" marker — plain dollar figure since 2026-08-23 — but the flag stays
// as data.)
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

// TestBuildSubagentTree_NewestFirst ignores status and keeps the newest job
// first within each sibling group.
func TestBuildSubagentTree_NewestFirst(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)

	m := newTestModelForSidebar()
	olderWaiting := jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, Status: jobs.StatusWaitingAnswer, Description: "older", StartedAt: time.Now().Add(-time.Minute)}
	newerDone := jobs.Job{ID: "job-2", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "newer", StartedAt: time.Now()}
	m.applyJobUpdate(olderWaiting)
	m.applyJobUpdate(newerDone)

	rows := m.buildSubagentTree()
	if len(rows) != 3 {
		t.Fatalf("expected root + 2 children, got %d", len(rows))
	}
	if rows[1].job.ID != "job-2" || rows[2].job.ID != "job-1" {
		t.Fatalf("expected newest-first regardless of status, got %+v", rows[1:])
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

// TestBuildSubagentTree_ChainedOrphansParentFirst guards the orphan-chain
// ordering: the orphan key for child B sorts before the missing parent key P,
// but A (the child of P and parent of B) must still render before B.
func TestBuildSubagentTree_ChainedOrphansParentFirst(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)

	m := newTestModelForSidebar()
	a := jobs.Job{ID: "A", Kind: jobs.KindSubagent, ParentID: "P", Status: jobs.StatusDone, Description: "parent"}
	b := jobs.Job{ID: "B", Kind: jobs.KindSubagent, ParentID: "A", Status: jobs.StatusDone, Description: "child"}
	m.applyJobUpdate(a)
	m.applyJobUpdate(b)

	rows := m.buildSubagentTree()
	if len(rows) != 3 {
		t.Fatalf("expected main plus the orphan chain, got %d rows: %+v", len(rows), rows)
	}
	if rows[1].job.ID != "A" || rows[1].depth != 1 {
		t.Fatalf("expected orphan-chain parent A at depth 1, got %+v", rows[1])
	}
	if rows[2].job.ID != "B" || rows[2].depth != 2 {
		t.Fatalf("expected orphan-chain child B at depth 2, got %+v", rows[2])
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
	m3.openSidebar(sidebarTabTasks)
	m3.sidebarCursor = 0
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
	m.openSidebar(sidebarTabTasks)
	m.sidebarCursor = 2 // Tasks heading + synthetic root + one real row

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

// ─── mainColumnWidth / sidebarColumnWidth split arithmetic ────────────────

// TestMainColumnWidth_ClosedReturnsFullWidth covers the trivial case: with
// no sidebar, the main column is the whole terminal.
func TestMainColumnWidth_ClosedReturnsFullWidth(t *testing.T) {
	m := newTestModelForSidebar()
	if got := m.mainColumnWidth(); got != m.width {
		t.Fatalf("mainColumnWidth() = %d, want m.width (%d) when the sidebar is closed", got, m.width)
	}
}

// TestMainColumnWidth_SplitArithmetic pins down the exact split at a few
// terminal widths, including one (45) that exercises the "shrink the
// sidebar to its floor" fallback and one (30) that exercises the
// no-good-split-exists edge case — see mainColumnWidth's doc comment.
// sidebarLayout's own width/left must always agree with mainColumnWidth(),
// since sidebarLayout derives from it rather than recomputing the split.
func TestMainColumnWidth_SplitArithmetic(t *testing.T) {
	cases := []struct {
		width            int
		wantMain         int
		wantSidebarWidth int
	}{
		{width: 100, wantMain: 59, wantSidebarWidth: 41},
		{width: 45, wantMain: 25, wantSidebarWidth: 20}, // sidebar shrunk to its floor
		{width: 30, wantMain: 10, wantSidebarWidth: 20}, // no split keeps both at their minimum
	}
	for _, c := range cases {
		m := newTestModelForSidebar()
		m.width = c.width
		m.openSidebar(sidebarTabTokens)

		if got := m.mainColumnWidth(); got != c.wantMain {
			t.Errorf("width=%d: mainColumnWidth() = %d, want %d", c.width, got, c.wantMain)
		}
		layout := m.sidebarLayout()
		if layout.width != c.wantSidebarWidth {
			t.Errorf("width=%d: sidebarLayout().width = %d, want %d", c.width, layout.width, c.wantSidebarWidth)
		}
		if layout.left != m.mainColumnWidth() {
			t.Errorf("width=%d: sidebarLayout().left = %d disagrees with mainColumnWidth() = %d",
				c.width, layout.left, m.mainColumnWidth())
		}
		if m.mainColumnWidth()+layout.width != c.width {
			t.Errorf("width=%d: main(%d)+sidebar(%d) != total(%d)", c.width, m.mainColumnWidth(), layout.width, c.width)
		}
	}
}

// ─── renderFrame side-by-side column ───────────────────────────────────────

// TestRenderFrame_SidebarActiveShowsBothColumns covers Change 1's whole
// point: with the sidebar open, the rendered frame must contain recognizable
// content from BOTH the main conversation and the sidebar at once — this
// used to be impossible (the old full-screen overlay replaced the
// transcript entirely).
func TestRenderFrame_SidebarActiveShowsBothColumns(t *testing.T) {
	m := newTestModelForSidebar()
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "unique-marker-xyzzy-in-the-transcript"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.openSidebar(sidebarTabTokens)

	frame := m.renderFrame()
	if !strings.Contains(frame, "xyzzy") {
		t.Fatalf("expected the main column's transcript content to appear in the joined frame")
	}
	if !strings.Contains(frame, "Sidebar") {
		t.Fatalf("expected the sidebar column's title to appear in the joined frame")
	}
}

// TestRenderWidth_DoubleNarrowIsARealFootgun documents the exact failure
// mode F13 fixed: feeding an already-narrowed width (mainColumnWidth's own
// output) back into mainColumnWidth with sidebarActive still true narrows a
// SECOND time, because mainColumnWidth has no way to tell "this width is
// already final" from "this is the real, full terminal width" — both just
// look like some m.width with sidebarActive set. This is exactly the shape
// renderFrame's shadow model would have (tui_view.go: mainM.width =
// m.mainColumnWidth()) if it did not also clear mainM.sidebarActive — see
// TestRenderFrame_MainColumnWrapsAtSingleNarrowWidth for the guard that
// actually exercises renderFrame and would catch that regression.
func TestRenderWidth_DoubleNarrowIsARealFootgun(t *testing.T) {
	m := newTestModelForSidebar()
	m.width = 120
	m.openSidebar(sidebarTabTokens)

	onceNarrowed := m.mainColumnWidth()
	shadow := m
	shadow.width = onceNarrowed // sidebarActive still true — the bug case
	twiceNarrowed := shadow.mainColumnWidth()

	if twiceNarrowed >= onceNarrowed {
		t.Fatalf("test premise broken: expected narrowing an already-narrow width to narrow further (got %d, from %d)",
			twiceNarrowed, onceNarrowed)
	}
	// renderFrame's real shadow model must not be in that bug case: it
	// clears sidebarActive right after copying the narrowed width in.
	shadow.sidebarActive = false
	if got := shadow.renderWidth(); got != onceNarrowed {
		t.Fatalf("renderWidth() on the shadow model = %d, want the single-narrowed %d", got, onceNarrowed)
	}
}

// TestRenderFrame_MainColumnWrapsAtSingleNarrowWidth is the end-to-end
// companion to the above: it drives the actual renderFrame() path (not a
// hand-built shadow model) and checks that wrapped transcript lines land at
// mainColumnWidth(), not at a second, double-narrowed width. Long enough
// text that a double-narrow (120 -> 71 -> 34) would visibly shorten lines
// compared to a single narrow (120 -> 71).
func TestRenderFrame_MainColumnWrapsAtSingleNarrowWidth(t *testing.T) {
	m := newTestModelForSidebar()
	m.width = 120
	m.height = 30
	longLine := strings.Repeat("word ", 40)
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: longLine})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.openSidebar(sidebarTabTokens)

	mainW := m.mainColumnWidth()
	m.renderFrame()

	lines := m.getBlockLines(0, false)
	if len(lines) == 0 {
		t.Fatal("expected wrapped lines for the text block")
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > mainW {
			t.Fatalf("line %d is %d cols wide, wider than the main column (%d): %q", i, w, mainW, l)
		}
	}
	// A double-narrow would wrap so short that a 200-char line needs far
	// more lines than fit in a single mainW-wide wrap.
	wantMax := (len(longLine) / max(1, mainW-2)) + 2
	if len(lines) > wantMax {
		t.Fatalf("block wrapped into %d lines at width ~%d — looks double-narrowed (expected at most %d lines at mainColumnWidth=%d)",
			len(lines), mainW, wantMax, mainW)
	}
}

// TestSidebarToggle_SyncsRealInputWidth is F20's regression test:
// renderFrame only narrows a COPY of the input (mainM.input) for the one
// frame it draws — the real m.input.Width() used to stay at the old, wide
// value until the next keystroke recomputed it (textarea.Model.SetWidth is
// called on every input.Update, not on open/closeSidebar). openSidebar and
// closeSidebar now set the real input's width directly, so it is correct
// immediately, with no render or keystroke required.
func TestSidebarToggle_SyncsRealInputWidth(t *testing.T) {
	m := newTestModelForSidebar()
	m.width = 120

	// textarea.SetWidth(w) does not make Width() return w exactly — it
	// reserves room for the prompt/line numbers first — so the expected
	// values below are derived by driving a scratch copy (same Prompt/
	// ShowLineNumbers config) through the same SetWidth call, not raw
	// arithmetic on mainColumnWidth().
	wantWidthFor := func(raw int) int {
		scratch := m.input
		scratch.SetWidth(max(10, raw-2))
		return scratch.Width()
	}
	// The expected full-width value at m.width=120 — not the textarea's
	// current Width(), which still reflects newTestModelForSidebar's
	// construction-time width since setting m.width directly (as this test
	// does, to isolate the sidebar toggle) does not itself resize the
	// input; only handleResize or openSidebar/closeSidebar do that.
	fullWidth := wantWidthFor(m.width)

	m.openSidebar(sidebarTabTokens)
	mainW := m.mainColumnWidth()
	if mainW >= m.width {
		t.Fatal("expected the sidebar to actually narrow the main column in this configuration")
	}
	if got, want := m.input.Width(), wantWidthFor(mainW); got != want {
		t.Fatalf("expected m.input.Width() to be narrowed to %d immediately on open, got %d", want, got)
	}

	m.closeSidebar()
	if got := m.input.Width(); got != fullWidth {
		t.Fatalf("expected m.input.Width() to be restored to %d immediately on close, got %d", fullWidth, got)
	}
}

// ─── Toggle invalidates the width-keyed transcript wrap cache ─────────────

// TestSidebarToggle_InvalidatesTranscriptWrapCache is the toggle-open analog
// of TestMessageRegionCache_DirtyAfterResize (tui_message_cache_test.go):
// getBlockLines' per-block wrap cache (tui_render_block.go) does not itself
// compare against the current width — it is only invalidated by an
// explicit call, normally invalidateAllBlockLineCounts on a real terminal
// resize. Opening the sidebar narrows the main column exactly the same way
// a real resize narrows the whole screen, so openSidebar must make that
// same call (see its doc comment) or the transcript would keep rendering
// wrapped for the old, wider column.
func TestSidebarToggle_InvalidatesTranscriptWrapCache(t *testing.T) {
	m := newTestModelForSidebar()
	m.width = 100
	m.height = 30
	longLine := strings.Repeat("word ", 40)
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: longLine})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.renderFrame() // populate the per-block wrap cache at the full width

	wideLineCount := m.blocks[0].cachedLineCount
	if wideLineCount == 0 {
		t.Fatal("expected the block to have a cached line count after rendering")
	}

	m.openSidebar(sidebarTabTokens)
	if m.mainColumnWidth() >= m.width {
		t.Fatal("expected the sidebar to actually narrow the main column in this configuration")
	}
	m.renderFrame() // rebuild the main column at the narrower mainColumnWidth()

	narrowLineCount := m.blocks[0].cachedLineCount
	if narrowLineCount <= wideLineCount {
		t.Fatalf("expected re-wrapping at the narrower main column (%d cols, vs %d full) to need MORE wrapped lines: wide=%d narrow=%d",
			m.mainColumnWidth(), m.width, wideLineCount, narrowLineCount)
	}
}

// ─── Streamed blocks must keep landing while the sidebar has focus ────────

// TestSidebarFocused_StreamedBlocksStillLandInTranscript is the regression
// test for a review finding: routeSidebarMsg's default branch forwarded
// every non-Window/Mouse/Key message to updateSidebar while sidebarFocused,
// and updateSidebar's switch had no case for tuiMsgBlock, so it fell
// through to its own "return m, nil" — silently dropping streamed model
// output (text/tool/done blocks) any time the user had focus in the
// sidebar, which is exactly the state a side-by-side layout is meant to
// make normal (browsing Bash/Subagents while the agent keeps working).
// Worse than a cosmetic gap: "done" is what flips m.reading back to true
// (tui_blocks.go), so a dropped done could leave the input stuck
// non-reading indefinitely. tuiMsgBlock must now reach handleBlockMsg
// unconditionally, focus or not — mirroring tui_modal.go's identical
// carve-out for the older subagent modal.
func TestSidebarFocused_StreamedBlocksStillLandInTranscript(t *testing.T) {
	m := newTestModelForSidebar()
	m.reading = false // as if a turn were still in flight
	m.openSidebar(sidebarTabTasks)
	m.sidebarFocused = true // browsing the sidebar while the agent works

	model, _ := m.Update(tuiMsgBlock{kind: "text", content: "unique-marker-streamed-while-focused"})
	m2 := model.(TuiModel)

	model, _ = m2.Update(tuiMsgBlock{kind: "done"})
	m3 := model.(TuiModel)

	found := false
	for _, b := range m3.blocks {
		if strings.Contains(b.content, "unique-marker-streamed-while-focused") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the streamed text block to land in the transcript even while sidebar-focused, got blocks: %+v", m3.blocks)
	}
	if !m3.reading {
		t.Fatalf("expected the \"done\" block to flip m.reading back to true even while sidebar-focused")
	}
	if !m3.sidebarActive || !m3.sidebarFocused {
		t.Fatalf("expected the sidebar to stay open and focused throughout — streamed blocks must not disturb it")
	}
}

func TestSidebarTaskRows_UsesRenderWidth(t *testing.T) {
	m := newTestModelForSidebar()
	m.applyJobUpdate(jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, Status: jobs.StatusRunning, Description: strings.Repeat("long description ", 10), StartedAt: time.Now()})
	rows := m.sidebarTaskRows(24)
	if len(rows) < 3 {
		t.Fatalf("expected Tasks heading, root, and job rows, got %d", len(rows))
	}
	if got := lipgloss.Width(rows[2].line); got > 24 {
		t.Fatalf("task row width = %d, want <= 24: %q", got, rows[2].line)
	}
}

func TestSidebarTasks_SubagentRUsesJobCursor(t *testing.T) {
	m := newTestModelForSidebar()
	m.input.SetValue("")
	m.applyJobUpdate(jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "resume me", StartedAt: time.Now()})
	m.openSidebar(sidebarTabTasks)
	m.sidebarCursor = 0
	model, _ := m.sidebarResumeSubagentRow()
	got := model.(TuiModel)
	if got.input.Value() == "" || !strings.Contains(got.input.Value(), "job 1") {
		t.Fatalf("expected r to draft the selected subagent prompt, got %q", got.input.Value())
	}
}
