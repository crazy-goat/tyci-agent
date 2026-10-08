package display

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// runsModel returns a model whose Runs tab lists rows, in the given order.
func runsModel(rows ...TuiRunRow) TuiModel {
	m := newTestModelForSidebar()
	m.runLister = func() []TuiRunRow { return rows }
	m.openSidebar(sidebarTabRuns)
	return m
}

func TestSidebarRunsTab(t *testing.T) {
	m := TuiModel{}
	if got := strings.Join(m.renderSidebarRuns(60), "\n"); !strings.Contains(got, "not wired") {
		t.Fatalf("unwired: %q", got)
	}
	m.runLister = func() []TuiRunRow { return nil }
	if got := strings.Join(m.renderSidebarRuns(60), "\n"); !strings.Contains(got, "No workflow runs") {
		t.Fatalf("empty: %q", got)
	}
}

func TestSidebarRunsTab_OrderActiveThenFinished(t *testing.T) {
	base := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	m := runsModel(
		TuiRunRow{ID: "f1", Issue: 2, Status: "done", State: "end", Result: "merged",
			Started: base, Ended: base.Add(time.Hour), Took: time.Minute},
		TuiRunRow{ID: "a1", Issue: 1, Status: "running", State: "code", Role: "worker", Started: base},
		TuiRunRow{ID: "f2", Issue: 4, Status: "failed", State: "code",
			Started: base, Ended: base.Add(2 * time.Hour), Took: time.Minute},
		TuiRunRow{ID: "a2", Issue: 3, Status: "paused", State: "review", Started: base.Add(time.Minute)},
	)
	lines := m.renderSidebarRuns(60)
	if len(lines) != 5 {
		t.Fatalf("want 4 runs and a separator, got %d lines: %q", len(lines), lines)
	}
	wantPrefix := []string{" #3 ", " #1 ", "─", " #4 ", " #2 "}
	for i, want := range wantPrefix {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to contain %q", i, lines[i], want)
		}
	}
}

func TestSidebarRunsTab_CollapsedLines(t *testing.T) {
	now := time.Now()
	m := runsModel(
		TuiRunRow{ID: "r1", Issue: 632, Status: "running", State: "code", Role: "worker",
			Started: now.Add(-time.Minute), Since: now.Add(-19 * time.Second), Cost: "$0.01"},
		TuiRunRow{ID: "r2", Issue: 633, Status: "running", State: "ci_wait",
			Started: now.Add(-time.Minute), Since: now.Add(-5 * time.Second), Cost: "$0.00"},
		TuiRunRow{ID: "r3", Issue: 472, Status: "done", State: "end", Result: "merged",
			Started: now.Add(-time.Hour), Ended: now.Add(-time.Minute), Took: 12*time.Minute + 3*time.Second,
			Cost: "$0.52", Steps: []TuiRunStep{{Text: "merge -> merged", Cost: "$0.01"}}},
	)
	lines := m.renderSidebarRuns(60)
	if len(lines) != 4 {
		t.Fatalf("collapsed runs want one line each and a separator, got %d: %q", len(lines), lines)
	}
	checks := []struct{ line, label, dur, cost string }{
		{lines[0], " #632 code (worker)", "19s", "$0.01"},
		{lines[1], " #633 ci_wait (script)", "5s", "$0.00"},
		{lines[3], " #472 done (merged)", "12m3s", "$0.52"},
	}
	for _, c := range checks {
		if !strings.HasPrefix(c.line, c.label) || !strings.HasSuffix(c.line, c.dur+"   "+c.cost) {
			t.Errorf("line %q: want %q at the start and %q before the cost %q at the end", c.line, c.label, c.dur, c.cost)
		}
	}
	if w := lipgloss.Width(lines[0]); w != 60 {
		t.Errorf("cost is right-aligned: line width %d, want 60: %q", w, lines[0])
	}
	got := strings.Join(lines, "\n")
	for _, hidden := range []string{"r1", "r3", "merge -> merged"} {
		if strings.Contains(got, hidden) {
			t.Errorf("collapsed run must not show %q: %q", hidden, got)
		}
	}
}

func TestSidebarRunsTab_DurationColumnLinesUp(t *testing.T) {
	now := time.Now()
	m := runsModel(
		TuiRunRow{ID: "r1", Issue: 621, Status: "running", State: "code", Role: "worker",
			Started: now.Add(-time.Hour), Since: now.Add(-(18*time.Minute + 36*time.Second)), Cost: "$0.00"},
		TuiRunRow{ID: "r2", Issue: 584, Status: "running", State: "findings", Role: "reviewer",
			Started: now.Add(-time.Hour), Since: now.Add(-6 * time.Second), Cost: "$12.34"},
		TuiRunRow{ID: "r3", Issue: 638, Status: "done", State: "end", Result: "merged",
			Started: now.Add(-2 * time.Hour), Ended: now.Add(-time.Minute),
			Took: time.Hour + 5*time.Minute, Cost: "$0.96"},
	)
	lines := m.renderSidebarRuns(60)
	if len(lines) != 4 {
		t.Fatalf("want two active runs, a separator and a finished run: got %d: %q", len(lines), lines)
	}
	checks := []struct{ line, label, cost string }{
		{lines[0], " #621 code (worker)", "$0.00"},
		{lines[1], " #584 findings (reviewer)", "$12.34"},
		{lines[3], " #638 done (merged)", "$0.96"},
	}
	// The text before the cost ends with the duration. Its width is the
	// column where the duration ends, so it does not depend on the clock.
	want := -1
	for _, c := range checks {
		if w := lipgloss.Width(c.line); w != 60 {
			t.Errorf("line %q: width %d, want 60", c.line, w)
		}
		if !strings.HasPrefix(c.line, c.label) || !strings.HasSuffix(c.line, c.cost) {
			t.Errorf("line %q: want %q at the start and %q at the end", c.line, c.label, c.cost)
		}
		end := lipgloss.Width(strings.TrimRight(strings.TrimSuffix(c.line, c.cost), " "))
		if want < 0 {
			want = end
		} else if end != want {
			t.Errorf("line %q: duration ends at column %d, want column %d", c.line, end, want)
		}
	}
	if !strings.Contains(lines[3], "1h5m0s") {
		t.Errorf("finished run: line %q, want its duration 1h5m0s", lines[3])
	}
}

func TestSidebarRunsTab_NarrowRowDropsDurationThenCost(t *testing.T) {
	m := runsModel(TuiRunRow{ID: "r1", Issue: 638, Status: "done", State: "end", Result: "merged",
		Took: time.Hour + 5*time.Minute, Cost: "$0.96"})
	// The header is " #638 done (merged)" and the columns are "1h5m0s   $0.96".
	tests := []struct {
		width    int
		wantDur  bool
		wantCost bool
	}{
		{width: 16, wantDur: true, wantCost: true},
		{width: 15, wantDur: false, wantCost: true},
		{width: 6, wantDur: false, wantCost: false},
	}
	for _, tt := range tests {
		lines := m.renderSidebarRuns(tt.width)
		line := lines[0]
		if w := lipgloss.Width(line); w != tt.width {
			t.Errorf("width %d: line %q has width %d", tt.width, line, w)
		}
		if got := strings.Contains(line, "1h5m0s"); got != tt.wantDur {
			t.Errorf("width %d: duration shown = %v, want %v: %q", tt.width, got, tt.wantDur, line)
		}
		if got := strings.Contains(line, "$0.96"); got != tt.wantCost {
			t.Errorf("width %d: cost shown = %v, want %v: %q", tt.width, got, tt.wantCost, line)
		}
	}
}

func TestSidebarRunsTab_ExpandedShowsEveryVisit(t *testing.T) {
	m := runsModel(TuiRunRow{ID: "r1", Issue: 472, Status: "done", State: "end", Result: "merged",
		Took: 5*time.Minute + 12*time.Second, Cost: "$0.86",
		Steps: []TuiRunStep{
			{Text: "code -> done: worker · opus · 1m52s", Cost: "$0.31"},
			{Text: "review -> fail: reviewer · opus · 40s", Cost: "$0.08"},
			{Text: "code -> done: worker · opus · 2m5s", Cost: "$0.40"},
			{Text: "review -> merged: reviewer · opus · 35s", Cost: "$0.07"},
		}})
	m.sidebarRunsExpanded = map[string]bool{"r1": true}
	lines := m.renderSidebarRuns(70)
	if len(lines) != 6 {
		t.Fatalf("want the run line, the id, four visits: got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[1], "r1") {
		t.Errorf("run id line: %q", lines[1])
	}
	visits := []struct{ text, cost string }{
		{"code -> done", "$0.31"},
		{"review -> fail", "$0.08"},
		{"code -> done", "$0.40"},
		{"review -> merged", "$0.07"},
	}
	for i, v := range visits {
		line := lines[2+i]
		if !strings.Contains(line, v.text) {
			t.Errorf("visit %d = %q, want %q", i, line, v.text)
		}
		if !strings.HasSuffix(line, v.cost) {
			t.Errorf("visit %d = %q, want its cost %q at the end", i, line, v.cost)
		}
	}
}

func TestSidebarRunsTab_ExpandedActiveRunEndsWithCurrentStep(t *testing.T) {
	now := time.Now()
	m := runsModel(TuiRunRow{ID: "r1", Issue: 632, Status: "running", State: "review", Role: "reviewer",
		Since: now.Add(-10 * time.Second), Cost: "$0.12",
		Steps: []TuiRunStep{{Text: "code -> done: worker · opus · 1m52s", Cost: "$0.12"}}})
	m.sidebarRunsExpanded = map[string]bool{"r1": true}
	lines := m.renderSidebarRuns(70)
	if len(lines) != 4 {
		t.Fatalf("want header, id, one visit and the current step: got %d: %q", len(lines), lines)
	}
	if last := lines[3]; !strings.Contains(last, "review (reviewer) 10s") || strings.Contains(last, "$") {
		t.Errorf("current step line = %q, want the running step with its time and no cost", last)
	}
}

func TestSidebarRunsTab_EnterTogglesRun(t *testing.T) {
	m := runsModel(
		TuiRunRow{ID: "r1", Issue: 1, Status: "running", State: "code", Role: "worker", Since: time.Now(),
			Steps: []TuiRunStep{{Text: "plan -> ok"}}},
		TuiRunRow{ID: "r2", Issue: 2, Status: "running", State: "code", Role: "worker", Since: time.Now().Add(-time.Minute)},
	)
	if strings.Contains(strings.Join(m.renderSidebarRuns(60), "\n"), "plan -> ok") {
		t.Fatal("a run is collapsed by default")
	}
	model, _ := m.sidebarActivateRow()
	m = model.(TuiModel)
	if !strings.Contains(strings.Join(m.renderSidebarRuns(60), "\n"), "plan -> ok") {
		t.Fatal("Enter must expand the run under the cursor")
	}
	m.sidebarMoveCursor(1)
	model, _ = m.sidebarActivateRow()
	m = model.(TuiModel)
	if !m.sidebarRunsExpanded["r2"] || !m.sidebarRunsExpanded["r1"] {
		t.Fatalf("Enter on the second run must expand it and keep the first one open: %v", m.sidebarRunsExpanded)
	}
	model, _ = m.sidebarActivateRow()
	m = model.(TuiModel)
	if m.sidebarRunsExpanded["r2"] {
		t.Fatal("Enter again must collapse the run")
	}
}

func TestSidebarRunsTab_ClickTogglesRun(t *testing.T) {
	m := runsModel(
		TuiRunRow{ID: "r1", Issue: 1, Status: "running", State: "code", Role: "worker", Since: time.Now(),
			Steps: []TuiRunStep{{Text: "plan -> ok"}}},
	)
	layout := m.sidebarLayout()
	model, _ := m.updateSidebar(tea.MouseMsg{
		X: layout.contentLeft + 1, Y: layout.contentTop,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m2 := model.(TuiModel)
	if !m2.sidebarRunsExpanded["r1"] {
		t.Fatal("a click on the run line must expand the run")
	}
}

func TestRunsTabWantsTick(t *testing.T) {
	m := TuiModel{reading: true, sidebarActive: true, sidebarTab: sidebarTabRuns}
	if !m.wantsStatusTick() {
		t.Fatal("Runs tab must tick")
	}
	m.sidebarTab = sidebarTabTokens
	if m.wantsStatusTick() {
		t.Fatal("other tab must not tick")
	}
}

// runsRowsForBackground returns a Runs tab with every kind of row: an active
// run with a cost and steps with and without a cost, a finished run with a
// duration and a cost, and a failed run with neither. All of them are expanded.
func runsRowsForBackground() TuiModel {
	now := time.Now()
	m := runsModel(
		TuiRunRow{ID: "r1", Issue: 632, Status: "running", State: "code", Role: "worker",
			Started: now.Add(-time.Minute), Since: now.Add(-19 * time.Second), Cost: "$0.01",
			Steps: []TuiRunStep{{Text: "lock -> ok: 0s"}, {Text: "update -> ok: 25s", Cost: "$0.02"}}},
		TuiRunRow{ID: "r3", Issue: 472, Status: "done", State: "end", Result: "merged",
			Started: now.Add(-time.Hour), Ended: now.Add(-time.Minute), Took: 12 * time.Minute,
			Cost: "$0.52", Steps: []TuiRunStep{{Text: "check_done -> go: 1s"}}},
		TuiRunRow{ID: "r4", Issue: 9, Status: "failed", State: "code",
			Started: now.Add(-time.Hour), Ended: now.Add(-time.Minute)},
	)
	m.sidebarRunsExpanded = map[string]bool{"r1": true, "r3": true, "r4": true}
	return m
}

// TestSidebarRunsTab_EveryLineFillsWidth checks that every row, also a row
// without a cost or a duration, is exactly as wide as the tab. A shorter row
// leaves its right end without the sidebar background.
func TestSidebarRunsTab_EveryLineFillsWidth(t *testing.T) {
	m := runsRowsForBackground()
	for _, width := range []int{60, 14} {
		lines := m.renderSidebarRuns(width)
		if len(lines) != 11 {
			t.Fatalf("width %d: want 11 lines, got %d: %q", width, len(lines), lines)
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w != width {
				t.Errorf("width %d: line %d has width %d: %q", width, i, w, l)
			}
		}
	}
}

// cellBackgrounds returns the background of every visible cell of line. ""
// is the terminal default. It reads the SGR codes that lipgloss emits here:
// reset, 256-color background (48;5;N), default background (49).
func cellBackgrounds(line string) []string {
	var bgs []string
	bg := ""
	for line != "" {
		if strings.HasPrefix(line, "\x1b[") {
			end := strings.IndexByte(line, 'm')
			if end < 0 {
				break
			}
			bg = applySGRBackground(bg, line[2:end])
			line = line[end+1:]
			continue
		}
		_, size := utf8.DecodeRuneInString(line)
		bgs = append(bgs, bg)
		line = line[size:]
	}
	return bgs
}

// applySGRBackground returns the background after the SGR parameters params.
func applySGRBackground(bg, params string) string {
	if params == "" {
		return ""
	}
	p := strings.Split(params, ";")
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case "0", "49":
			bg = ""
		case "48":
			if i+2 < len(p) && p[i+1] == "5" {
				bg = p[i+2]
				i += 2
			}
		case "38":
			if i+1 < len(p) && p[i+1] == "5" {
				i += 2
			}
		}
	}
	return bg
}

// TestSidebarRunsTab_RowsKeepSidebarBackground renders the sidebar and checks
// the background of every cell of each Runs row. The sidebar paints
// background 235. A full reset inside a row clears it, so the cells after
// that reset get the terminal default: the band breaks at the reset.
func TestSidebarRunsTab_RowsKeepSidebarBackground(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })
	// The title, the tab row and the separator come before the Runs rows.
	const firstRunsRow = 3
	for _, cursor := range []int{-1, 0} {
		m := runsRowsForBackground()
		m.sidebarCursor = cursor
		layout := m.sidebarLayout()
		view := m.runsTab(layout.contentWidth)
		selectedLine := -1
		if cursor >= 0 {
			selectedLine = view.start[cursor]
		}
		column := strings.Split(m.renderSidebarColumn(), "\n")
		if got := ansi.Strip(column[firstRunsRow]); !strings.Contains(got, "#632") {
			t.Fatalf("cursor %d: row %d is not the first Runs row: %q", cursor, firstRunsRow, got)
		}
		for k := range view.lines {
			row := column[firstRunsRow+k]
			cells := cellBackgrounds(row)
			if len(cells) < 2 {
				t.Fatalf("cursor %d: row %d has no cells: %q", cursor, k, row)
			}
			// Cell 0 is the border. The other cells are the sidebar, or the
			// highlight of the selected run header.
			wrong, highlighted := 0, 0
			for _, bg := range cells[1:] {
				switch {
				case k == selectedLine && bg == "45":
					highlighted++
				case bg != "235":
					wrong++
				}
			}
			if wrong > 0 {
				t.Errorf("cursor %d: row %d %q: %d cells without the sidebar background: %q",
					cursor, k, ansi.Strip(row), wrong, row)
			}
			if k == selectedLine && highlighted != layout.contentWidth {
				t.Errorf("cursor %d: selected row has %d highlighted cells, want %d",
					cursor, highlighted, layout.contentWidth)
			}
		}
		checkSidebarChrome(t, m)
	}
}

// checkSidebarChrome checks the tab row, the hint row and the key line of the
// sidebar. Every cell after the border must have the sidebar background, except
// the active tab label, which is highlighted. The rows are drawn on every tab,
// so the caller picks the tab and the focus.
func checkSidebarChrome(t *testing.T, m TuiModel) {
	t.Helper()
	layout := m.sidebarLayout()
	column := strings.Split(m.renderSidebarColumn(), "\n")
	// The separator comes after the content rows, then the hint row, then the key line.
	hintRow := layout.contentTop + layout.contentHeight + 1
	keyRow := hintRow + 1
	// A focused key line is cut at the sidebar width, so only its first word is checked.
	if got := ansi.Strip(column[keyRow]); !strings.Contains(got, strings.Fields(m.sidebarFooter())[0]) {
		t.Fatalf("tab %d: row %d is not the key line: %q", m.sidebarTab, keyRow, got)
	}
	for _, c := range []struct {
		name      string
		row       int
		highlight int
	}{
		{"tab row", 1, lipgloss.Width(sidebarTabLabel(m.sidebarTab))},
		{"hint row", hintRow, 0},
		{"key line", keyRow, 0},
	} {
		line := column[c.row]
		cells := cellBackgrounds(line)
		if len(cells) < 2 {
			t.Fatalf("tab %d: %s has no cells: %q", m.sidebarTab, c.name, line)
		}
		wrong, highlighted := 0, 0
		for _, bg := range cells[1:] {
			switch bg {
			case "235":
			case "45":
				highlighted++
			default:
				wrong++
			}
		}
		if wrong > 0 {
			t.Errorf("tab %d, focused %v, %s %q: %d cells without the sidebar background: %q",
				m.sidebarTab, m.sidebarFocused, c.name, ansi.Strip(line), wrong, line)
		}
		if highlighted != c.highlight {
			t.Errorf("tab %d, focused %v, %s: %d highlighted cells, want %d: %q",
				m.sidebarTab, m.sidebarFocused, c.name, highlighted, c.highlight, line)
		}
	}
}
