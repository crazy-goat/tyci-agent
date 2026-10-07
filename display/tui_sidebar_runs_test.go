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
