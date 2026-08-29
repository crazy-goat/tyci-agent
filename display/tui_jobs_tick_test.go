package display

import (
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// Item 57: the status tick chain (item 56) is what keeps a background job's
// elapsed/quiet time (tui_jobs_panel.go, tui_sidebar.go, tui_sidebar_view.go)
// live once the turn that started it has ended (done -> reading=true). These
// tests pin wantsStatusTick/hasLiveJobsToPaint and the arm/interval logic
// that drives it.

func newIdleTestModelForJobsTick() TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = true // idle: no turn in flight
	return m
}

// ─── hasLiveJobsToPaint / wantsStatusTick ───────────────────────────────

func TestWantsStatusTick_IdleWithRunningJobAndTasksTabVisible(t *testing.T) {
	m := newIdleTestModelForJobsTick()
	m.applyJobUpdate(jobs.Job{ID: "j1", Status: jobs.StatusRunning, StartedAt: time.Now()})
	m.sidebarActive = true
	m.sidebarTab = sidebarTabTasks

	if !m.wantsStatusTick() {
		t.Error("idle model with a running job and the Tasks tab open should still want the tick")
	}
}

func TestWantsStatusTick_IdleWithRunningJobButNothingVisible(t *testing.T) {
	m := newIdleTestModelForJobsTick()
	m.applyJobUpdate(jobs.Job{ID: "j1", Status: jobs.StatusRunning, StartedAt: time.Now()})
	// Sidebar closed, no jobs modal — but the inline jobs panel is still
	// visible (renderJobsPanel renders whenever a running job exists and no
	// overlay replaces the main view), so this must still want the tick.
	if !m.wantsStatusTick() {
		t.Error("a running job always paints in the inline jobs panel unless some overlay hides the main view")
	}

	// Now hide the panel too, by putting a full-screen overlay over the
	// main view — the one case where a live job truly paints nowhere.
	m.todoModalActive = true
	if m.wantsStatusTick() {
		t.Error("idle model with a running job hidden behind a full-screen overlay should not want the tick")
	}
}

func TestWantsStatusTick_JobReachesTerminalStatusStopsWanting(t *testing.T) {
	m := newIdleTestModelForJobsTick()
	m.applyJobUpdate(jobs.Job{ID: "j1", Status: jobs.StatusRunning, StartedAt: time.Now()})
	if !m.wantsStatusTick() {
		t.Fatal("expected wantsStatusTick while the job is running")
	}
	m.applyJobUpdate(jobs.Job{ID: "j1", Status: jobs.StatusDone, StartedAt: time.Now(), FinishedAt: time.Now()})
	if m.wantsStatusTick() {
		t.Error("wantsStatusTick should be false once the only job has reached a terminal status")
	}
}

func TestWantsStatusTick_TrueWhileTurnInFlightRegardlessOfJobs(t *testing.T) {
	m := newIdleTestModelForJobsTick()
	m.reading = false // turn in flight
	if !m.wantsStatusTick() {
		t.Error("a turn in flight should want the tick even with no jobs at all")
	}
}

// ─── tick chain keeps chaining / stops and clears the flag ──────────────

func TestStatusTickMsg_ChainsWhileTasksTabShowsRunningJob(t *testing.T) {
	m := newIdleTestModelForJobsTick()
	m.applyJobUpdate(jobs.Job{ID: "j1", Status: jobs.StatusRunning, StartedAt: time.Now()})
	m.sidebarActive = true
	m.sidebarTab = sidebarTabTasks
	m.statusTickArmed = true

	result, cmd := m.Update(statusTickMsg{})
	if cmd == nil {
		t.Fatal("expected the tick chain to keep going while a live job is shown in the Tasks tab")
	}
	m2 := result.(TuiModel)
	if !m2.statusTickArmed {
		t.Error("statusTickArmed should remain true while the chain keeps ticking")
	}
}

func TestStatusTickMsg_StopsAndClearsFlagWhenNothingLiveIsVisible(t *testing.T) {
	m := newIdleTestModelForJobsTick()
	// No jobs at all, sidebar closed, no jobs modal: nothing to paint.
	m.statusTickArmed = true

	result, cmd := m.Update(statusTickMsg{})
	if cmd != nil {
		t.Fatal("expected the tick chain to stop when idle with no live jobs visible anywhere")
	}
	m2 := result.(TuiModel)
	if m2.statusTickArmed {
		t.Error("statusTickArmed should be cleared once the chain decides to stop")
	}
}

func TestStatusTickMsg_StopsOnceJobReachesTerminalStatus(t *testing.T) {
	m := newIdleTestModelForJobsTick()
	m.applyJobUpdate(jobs.Job{ID: "j1", Status: jobs.StatusRunning, StartedAt: time.Now()})
	m.statusTickArmed = true

	// First tick: job is still running, chain should keep going.
	result, cmd := m.Update(statusTickMsg{})
	if cmd == nil {
		t.Fatal("expected the chain to keep ticking while the job is running")
	}
	m = result.(TuiModel)

	// Job finishes.
	m.applyJobUpdate(jobs.Job{ID: "j1", Status: jobs.StatusDone, StartedAt: m.backgroundJobs["j1"].StartedAt, FinishedAt: time.Now()})

	// Next tick: nothing live left, chain must stop and clear the flag.
	result, cmd = m.Update(statusTickMsg{})
	if cmd != nil {
		t.Error("expected the chain to stop on the tick after the job reached a terminal status")
	}
	m2 := result.(TuiModel)
	if m2.statusTickArmed {
		t.Error("statusTickArmed should be cleared once the job is terminal and the chain stops")
	}
}

// ─── tuiMsgJobUpdate arms exactly one chain ─────────────────────────────

func TestTuiMsgJobUpdate_ArmsExactlyOneChainOnIdleUnarmedModel(t *testing.T) {
	m := newIdleTestModelForJobsTick()
	if m.statusTickArmed {
		t.Fatal("test setup: model should start unarmed")
	}

	result, cmd := m.Update(tuiMsgJobUpdate{Job: jobs.Job{ID: "j1", Status: jobs.StatusRunning, StartedAt: time.Now()}})
	if cmd == nil {
		t.Fatal("the first job update on an idle, unarmed model should arm the tick chain")
	}
	m2 := result.(TuiModel)
	if !m2.statusTickArmed {
		t.Fatal("expected statusTickArmed to be set after the first job update")
	}

	// A second update while already armed must not start a second chain.
	result2, cmd2 := m2.Update(tuiMsgJobUpdate{Job: jobs.Job{ID: "j1", Status: jobs.StatusRunning, StartedAt: time.Now(), LastActivity: time.Now()}})
	if cmd2 != nil {
		t.Error("a second job update while the chain is already armed must not arm a second one")
	}
	m3 := result2.(TuiModel)
	if !m3.statusTickArmed {
		t.Error("statusTickArmed should remain set")
	}
}

// ─── tuiMsgJobsReset cannot leave statusTickArmed stuck ─────────────────

func TestJobsReset_DoesNotLeaveTickArmedStuckOnceJobsAreGone(t *testing.T) {
	m := newIdleTestModelForJobsTick()
	m.applyJobUpdate(jobs.Job{ID: "j1", Status: jobs.StatusRunning, StartedAt: time.Now()})
	// Arm the job-only chain the way tuiMsgJobUpdate's handler would.
	result, cmd := m.Update(tuiMsgJobUpdate{Job: m.backgroundJobs["j1"]})
	if cmd == nil {
		t.Fatal("test setup: expected the job update to arm the chain")
	}
	m = result.(TuiModel)
	if !m.statusTickArmed {
		t.Fatal("test setup: expected statusTickArmed to be set")
	}

	// New conversation: jobs are reset out from under the still-armed flag.
	result, _ = m.Update(tuiMsgJobsReset{jobIDs: []string{"j1"}})
	m = result.(TuiModel)
	if len(m.backgroundJobs) != 0 {
		t.Fatalf("expected backgroundJobs cleared by reset, got %+v", m.backgroundJobs)
	}
	// tuiMsgJobsReset does not itself clear statusTickArmed synchronously —
	// but the pending tick it left behind must clear it on its own next
	// fire, since there is nothing left to paint. Simulate that fire.
	result, cmd = m.Update(statusTickMsg{})
	if cmd != nil {
		t.Fatal("the tick pending at reset time should stop once it finds no live jobs left")
	}
	m2 := result.(TuiModel)
	if m2.statusTickArmed {
		t.Error("statusTickArmed must not be left stuck armed after a reset with no jobs and no turn in flight")
	}
}

// ─── interval: 250ms in-flight, 1s job-only ─────────────────────────────

func TestTickInterval_FastWhileTurnInFlight(t *testing.T) {
	m := newIdleTestModelForJobsTick()
	m.reading = false // turn in flight
	if got := m.tickInterval(); got != statusTickInterval {
		t.Errorf("tickInterval() while a turn is in flight = %v, want %v", got, statusTickInterval)
	}
}

func TestTickInterval_SlowWhenOnlyJobsNeedIt(t *testing.T) {
	m := newIdleTestModelForJobsTick()
	m.reading = true // idle, job-only chain
	if got := m.tickInterval(); got != jobsOnlyTickInterval {
		t.Errorf("tickInterval() with only jobs needing it = %v, want %v", got, jobsOnlyTickInterval)
	}
}

// ─── rendered elapsed text actually changes after time passes ──────────

func TestJobDuration_ElapsedTextChangesAfterTimePasses(t *testing.T) {
	j := jobs.Job{ID: "j1", Description: "long task", Status: jobs.StatusRunning,
		StartedAt: time.Now().Add(-10 * time.Second)}
	before := formatJobLine(j, 80)

	j.StartedAt = j.StartedAt.Add(-5 * time.Second) // simulate 5 more elapsed seconds
	after := formatJobLine(j, 80)

	if before == after {
		t.Errorf("expected the rendered job line to change once more time has elapsed, both were: %q", before)
	}
	if !strings.Contains(after, "15s") {
		t.Errorf("expected the later render to show ~15s elapsed, got: %q", after)
	}
}

// ─── sidebar open / jobs-modal open arm the chain ──────────────────────

func TestOpenJobsModal_ArmsTickWhenALiveJobExists(t *testing.T) {
	m := newIdleTestModelForJobsTick()
	m.applyJobUpdate(jobs.Job{ID: "j1", Status: jobs.StatusRunning, StartedAt: time.Now()})
	m.openJobsModal()

	if !m.wantsStatusTick() {
		t.Fatal("test setup: opening the jobs modal with a live job should want the tick")
	}
	if got := m.armStatusTick(); got == nil {
		t.Error("armStatusTick after opening the jobs modal with a live job should arm a chain")
	}
}
