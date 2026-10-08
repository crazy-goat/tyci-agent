package display

import (
	"strings"
	"testing"
	"time"
)

func TestSidebarRunsTab(t *testing.T) {
	m := TuiModel{}
	if got := strings.Join(m.renderSidebarRuns(60), "\n"); !strings.Contains(got, "not wired") {
		t.Fatalf("unwired: %q", got)
	}
	m.runLister = func() []TuiRunRow { return nil }
	if got := strings.Join(m.renderSidebarRuns(60), "\n"); !strings.Contains(got, "No workflow runs") {
		t.Fatalf("empty: %q", got)
	}
	m.runLister = func() []TuiRunRow {
		return []TuiRunRow{{ID: "r1", Issue: 296, Status: "running", State: "implement", Role: "coder",
			Since: time.Now().Add(-90 * time.Second), Steps: []string{"plan -> ok"}}}
	}
	got := strings.Join(m.renderSidebarRuns(60), "\n")
	for _, want := range []string{"#296", "running", "implement (coder)", "1m", "r1", "plan -> ok"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestSidebarRunsTab_FinishedRunIsOneLine(t *testing.T) {
	m := TuiModel{}
	m.runLister = func() []TuiRunRow {
		return []TuiRunRow{
			{ID: "r2", Issue: 472, Status: "done", State: "end", Result: "merged",
				Totals: "1.0k tok", Steps: []string{"merge -> merged"}},
			{ID: "r3", Issue: 473, Status: "failed", State: "code", Steps: []string{"code -> fail"}},
		}
	}
	lines := m.renderSidebarRuns(60)
	if len(lines) != 2 {
		t.Fatalf("finished runs want one line each, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "#472 done (merged)") {
		t.Errorf("merged run line: %q", lines[0])
	}
	if !strings.Contains(lines[1], "#473 failed") || strings.Contains(lines[1], "(") {
		t.Errorf("failed run line: %q", lines[1])
	}
	got := strings.Join(lines, "\n")
	for _, hidden := range []string{"r2", "r3", "1.0k tok", "merge -> merged", "code -> fail"} {
		if strings.Contains(got, hidden) {
			t.Errorf("finished run must not show %q: %q", hidden, got)
		}
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
