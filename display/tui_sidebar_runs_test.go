package display

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	checks := []struct{ line, text, cost string }{
		{lines[0], " #632 code (worker) 19s", "$0.01"},
		{lines[1], " #633 ci_wait (script) 5s", "$0.00"},
		{lines[3], " #472 done (merged) 12m3s", "$0.52"},
	}
	for _, c := range checks {
		if !strings.HasPrefix(c.line, c.text) || !strings.HasSuffix(c.line, c.cost) {
			t.Errorf("line %q: want %q at the start and %q at the end", c.line, c.text, c.cost)
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
