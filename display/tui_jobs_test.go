package display

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/jobs"
)

func newTestModelForJobs() TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = true
	return m
}

// ─── applyJobUpdate / Update(tuiMsgJobUpdate) ──────────────────────────────

func TestApplyJobUpdate_InsertsAndUpdatesByID(t *testing.T) {
	m := newTestModelForJobs()
	j := jobs.Job{ID: "job-1", Description: "do a thing", Status: jobs.StatusRunning, StartedAt: time.Now()}
	m.applyJobUpdate(j)
	if len(m.backgroundJobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(m.backgroundJobs))
	}
	if got := m.backgroundJobs["job-1"].Status; got != jobs.StatusRunning {
		t.Errorf("status = %s, want running", got)
	}

	// Same ID, later update (completion) — must overwrite, not duplicate.
	j.Status = jobs.StatusDone
	j.Result = "done result"
	m.applyJobUpdate(j)
	if len(m.backgroundJobs) != 1 {
		t.Fatalf("expected still 1 job after update, got %d", len(m.backgroundJobs))
	}
	if got := m.backgroundJobs["job-1"].Status; got != jobs.StatusDone {
		t.Errorf("status after update = %s, want done", got)
	}
}

// TestApplyJobUpdate_PrunesTerminalJobsBeyondTheRegistryBound mirrors
// jobs.Registry's own eviction (pruneTerminalLocked): backgroundJobs is a
// mirror fed by SetJobEventBus, not the registry itself, so without its own
// pruning it would grow unboundedly and could keep listing a job the real
// registry already dropped — see pruneBackgroundJobsLocked's doc comment.
func TestApplyJobUpdate_PrunesTerminalJobsBeyondTheRegistryBound(t *testing.T) {
	m := newTestModelForJobs()

	// One job still running — must survive pruning regardless of age.
	m.applyJobUpdate(jobs.Job{ID: "still-running", Status: jobs.StatusRunning, StartedAt: time.Now().Add(-time.Hour)})

	base := time.Now()
	for i := 0; i < jobs.MaxRetainedTerminalJobs+10; i++ {
		m.applyJobUpdate(jobs.Job{
			ID:         fmt.Sprintf("terminal-%d", i),
			Status:     jobs.StatusDone,
			StartedAt:  base.Add(time.Duration(i) * time.Second),
			FinishedAt: base.Add(time.Duration(i) * time.Second),
		})
	}

	terminalCount := 0
	for _, j := range m.backgroundJobs {
		if j.Status != jobs.StatusRunning {
			terminalCount++
		}
	}
	if terminalCount != jobs.MaxRetainedTerminalJobs {
		t.Fatalf("expected exactly %d retained terminal jobs, got %d (total backgroundJobs=%d)",
			jobs.MaxRetainedTerminalJobs, terminalCount, len(m.backgroundJobs))
	}
	if _, ok := m.backgroundJobs["still-running"]; !ok {
		t.Fatalf("expected the still-running job to survive pruning")
	}
	// The oldest terminal jobs (lowest i, earliest FinishedAt) must be the
	// ones evicted, not an arbitrary subset.
	if _, ok := m.backgroundJobs["terminal-0"]; ok {
		t.Fatalf("expected the oldest terminal job to have been pruned")
	}
	if _, ok := m.backgroundJobs[fmt.Sprintf("terminal-%d", jobs.MaxRetainedTerminalJobs+9)]; !ok {
		t.Fatalf("expected the newest terminal job to have survived")
	}
}

func TestUpdate_TuiMsgJobUpdate_AppliesRegardlessOfActiveModal(t *testing.T) {
	m := newTestModelForJobs()
	m.todoModalActive = true // an unrelated overlay is open

	j := jobs.Job{ID: "job-1", Description: "x", Status: jobs.StatusRunning, StartedAt: time.Now()}
	model, _ := m.Update(tuiMsgJobUpdate{Job: j})
	m2 := model.(TuiModel)

	if len(m2.backgroundJobs) != 1 {
		t.Fatalf("expected job applied even with todoModalActive, got %d jobs", len(m2.backgroundJobs))
	}
	// The unrelated overlay must remain untouched.
	if !m2.todoModalActive {
		t.Errorf("todoModalActive should remain true, job update must not affect other overlays")
	}
}

// ─── sortedBackgroundJobs ───────────────────────────────────────────────────

func TestSortedBackgroundJobs_NewestFirst(t *testing.T) {
	m := newTestModelForJobs()
	now := time.Now()
	m.applyJobUpdate(jobs.Job{ID: "old", StartedAt: now.Add(-time.Minute)})
	m.applyJobUpdate(jobs.Job{ID: "new", StartedAt: now})
	m.applyJobUpdate(jobs.Job{ID: "mid", StartedAt: now.Add(-30 * time.Second)})

	list := m.sortedBackgroundJobs()
	if len(list) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(list))
	}
	wantOrder := []string{"new", "mid", "old"}
	for i, want := range wantOrder {
		if list[i].ID != want {
			t.Errorf("list[%d].ID = %q, want %q (order: %v)", i, list[i].ID, want, list)
		}
	}
}

func TestSortedBackgroundJobs_EmptyReturnsNil(t *testing.T) {
	m := newTestModelForJobs()
	if got := m.sortedBackgroundJobs(); got != nil {
		t.Errorf("expected nil for empty backgroundJobs, got %v", got)
	}
}

// ─── renderJobsPanel / jobsPanelHeight ──────────────────────────────────────

func TestRenderJobsPanel_EmptyRendersNothing(t *testing.T) {
	m := newTestModelForJobs()
	if got := m.renderJobsPanel(80); got != "" {
		t.Fatalf("renderJobsPanel with no jobs = %q, want empty string", got)
	}
	if h := m.jobsPanelHeight(); h != 0 {
		t.Errorf("jobsPanelHeight with no jobs = %d, want 0", h)
	}
}

func TestRenderJobsPanel_OneJobShowsIDStatusAndDescription(t *testing.T) {
	m := newTestModelForJobs()
	m.applyJobUpdate(jobs.Job{
		ID: "job-1700000000-1", Description: "summarize the repo",
		Status: jobs.StatusRunning, StartedAt: time.Now(),
	})
	panel := m.renderJobsPanel(80)
	if !strings.Contains(panel, "summarize the repo") {
		t.Errorf("panel should contain description, got %q", panel)
	}
	if !strings.Contains(panel, "running") {
		t.Errorf("panel should contain status, got %q", panel)
	}
	if !strings.Contains(panel, "1") { // shortJobID suffix
		t.Errorf("panel should contain the short job id, got %q", panel)
	}
	if h := m.jobsPanelHeight(); h != 1 {
		t.Errorf("jobsPanelHeight with 1 job = %d, want 1", h)
	}
}

// TestRenderJobsPanel_FinishedJobDropsOffButStaysInModal locks in the fix
// for finished jobs piling up forever in the always-visible panel: once a
// job leaves StatusRunning it must disappear from renderJobsPanel/
// jobsPanelHeight, while sortedBackgroundJobs (what the Ctrl+B modal lists)
// keeps it for later inspection.
func TestRenderJobsPanel_FinishedJobDropsOffButStaysInModal(t *testing.T) {
	m := newTestModelForJobs()
	started := time.Now().Add(-time.Minute)
	m.applyJobUpdate(jobs.Job{
		ID: "job-1700000000-1", Description: "summarize the repo",
		Status: jobs.StatusRunning, StartedAt: started,
	})
	if panel := m.renderJobsPanel(80); !strings.Contains(panel, "summarize the repo") {
		t.Fatalf("expected the running job in the panel, got %q", panel)
	}

	m.applyJobUpdate(jobs.Job{
		ID: "job-1700000000-1", Description: "summarize the repo",
		Status: jobs.StatusDone, StartedAt: started, FinishedAt: time.Now(),
	})

	if panel := m.renderJobsPanel(80); panel != "" {
		t.Errorf("expected an empty panel once the only job is done, got %q", panel)
	}
	if h := m.jobsPanelHeight(); h != 0 {
		t.Errorf("jobsPanelHeight with only a done job = %d, want 0", h)
	}
	if list := m.sortedBackgroundJobs(); len(list) != 1 || list[0].Status != jobs.StatusDone {
		t.Errorf("expected the done job to remain in sortedBackgroundJobs for the modal, got %+v", list)
	}
}

func TestRenderJobsPanel_OverflowShowsMoreLine(t *testing.T) {
	m := newTestModelForJobs()
	now := time.Now()
	for i := 0; i < 6; i++ {
		m.applyJobUpdate(jobs.Job{
			ID:        "job-x-" + string(rune('a'+i)),
			StartedAt: now.Add(time.Duration(i) * time.Second),
			Status:    jobs.StatusRunning,
		})
	}
	panel := m.renderJobsPanel(80)
	lines := strings.Split(strings.TrimRight(panel, "\n"), "\n")
	if len(lines) != jobsPanelMaxLines+1 {
		t.Fatalf("expected %d lines (cap + overflow), got %d: %q", jobsPanelMaxLines+1, len(lines), panel)
	}
	if !strings.Contains(lines[len(lines)-1], "2 more") {
		t.Errorf("expected overflow line to mention '2 more', got %q", lines[len(lines)-1])
	}
	if h := m.jobsPanelHeight(); h != jobsPanelMaxLines+1 {
		t.Errorf("jobsPanelHeight with 6 jobs = %d, want %d", h, jobsPanelMaxLines+1)
	}
}

func TestRenderJobsPanel_NarrowWidthDoesNotExceed(t *testing.T) {
	m := newTestModelForJobs()
	m.applyJobUpdate(jobs.Job{
		ID: "job-1", Description: strings.Repeat("x", 200),
		Status: jobs.StatusDone, StartedAt: time.Now(), FinishedAt: time.Now(),
	})
	panel := m.renderJobsPanel(20)
	for _, line := range strings.Split(strings.TrimRight(panel, "\n"), "\n") {
		if w := lipgloss.Width(line); w > 20 {
			t.Errorf("line width = %d, want <= 20: %q", w, line)
		}
	}
}

// ─── renderFrame integration ────────────────────────────────────────────

func TestRenderFrame_JobsPanelHiddenWhenEmpty(t *testing.T) {
	m := newTestModelForJobs()
	frame := m.renderFrame()
	if strings.Contains(frame, "running") || strings.Contains(frame, "⟳") {
		t.Errorf("renderFrame with no jobs should not show job status glyphs, got:\n%s", frame)
	}
}

func TestRenderFrame_JobsPanelAppearsWhenPopulated(t *testing.T) {
	m := newTestModelForJobs()
	m.applyJobUpdate(jobs.Job{ID: "job-1", Description: "background task", Status: jobs.StatusRunning, StartedAt: time.Now()})
	frame := m.renderFrame()
	if !strings.Contains(frame, "background task") {
		t.Errorf("renderFrame should show the running job, got:\n%s", frame)
	}
}

// ─── openJobResultModal (reuses the subagent modal's static-content path) ──

func TestOpenJobResultModal_DoneShowsResult(t *testing.T) {
	m := newTestModelForJobs()
	j := jobs.Job{ID: "job-1", Description: "task desc", Status: jobs.StatusDone, Result: "the final answer"}
	m.openJobResultModal(j)

	if !m.subagentModalActive {
		t.Fatal("expected subagentModalActive=true")
	}
	if !m.subagentModalDone {
		t.Error("expected subagentModalDone=true (static content, not live)")
	}
	if m.subagentModalBlockIdx != -1 {
		t.Errorf("expected subagentModalBlockIdx=-1 (no live tool block bound), got %d", m.subagentModalBlockIdx)
	}
	if !strings.Contains(m.subagentModalText(), "the final answer") {
		t.Errorf("expected content to show job result, got %q", m.subagentModalText())
	}
	if m.subagentModalTitle != "task desc" {
		t.Errorf("title = %q, want %q", m.subagentModalTitle, "task desc")
	}
}

func TestOpenJobResultModal_FailedShowsError(t *testing.T) {
	m := newTestModelForJobs()
	j := jobs.Job{ID: "job-1", Description: "task", Status: jobs.StatusFailed, Err: "boom"}
	m.openJobResultModal(j)
	if !strings.Contains(m.subagentModalText(), "boom") {
		t.Errorf("expected content to show error, got %q", m.subagentModalText())
	}
}

func TestOpenJobResultModal_RunningShowsStillRunning(t *testing.T) {
	m := newTestModelForJobs()
	j := jobs.Job{ID: "job-1", Description: "task", Status: jobs.StatusRunning}
	m.openJobResultModal(j)
	if !strings.Contains(m.subagentModalText(), "still running") {
		t.Errorf("expected content to say still running, got %q", m.subagentModalText())
	}
}

func TestOpenJobResultModal_TruncatedShowsMarker(t *testing.T) {
	m := newTestModelForJobs()
	j := jobs.Job{ID: "job-1", Description: "task", Status: jobs.StatusTruncated, Result: "partial"}
	m.openJobResultModal(j)
	content := m.subagentModalText()
	if !strings.Contains(content, "partial") || !strings.Contains(content, "truncated") {
		t.Errorf("expected content to show partial result + truncated marker, got %q", content)
	}
}

// ─── updateJobsModal: navigation and Enter opens result modal ─────────────

func TestUpdateJobsModal_EnterOpensSelectedJobResult(t *testing.T) {
	m := newTestModelForJobs()
	now := time.Now()
	m.applyJobUpdate(jobs.Job{ID: "a", Description: "first", Status: jobs.StatusDone, Result: "r-a", StartedAt: now})
	m.applyJobUpdate(jobs.Job{ID: "b", Description: "second", Status: jobs.StatusDone, Result: "r-b", StartedAt: now.Add(time.Second)})
	m.openJobsModal()
	// sortedBackgroundJobs is newest-first, so cursor 0 = "b" (started later).
	model, _ := m.updateJobsModal(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := model.(TuiModel)

	if !m2.subagentModalActive {
		t.Fatal("expected Enter to open the job result modal")
	}
	if m2.jobsModalActive {
		t.Error("expected jobsModalActive to close after Enter")
	}
	if !strings.Contains(m2.subagentModalText(), "r-b") {
		t.Errorf("expected result modal to show newest job's result, got %q", m2.subagentModalText())
	}
}

func TestUpdateJobsModal_DownMovesCursorClampedAtEnd(t *testing.T) {
	m := newTestModelForJobs()
	now := time.Now()
	m.applyJobUpdate(jobs.Job{ID: "a", StartedAt: now})
	m.applyJobUpdate(jobs.Job{ID: "b", StartedAt: now.Add(time.Second)})
	m.openJobsModal()

	model, _ := m.updateJobsModal(tea.KeyMsg{Type: tea.KeyDown})
	m2 := model.(TuiModel)
	if m2.jobsModalCursor != 1 {
		t.Fatalf("cursor after one Down = %d, want 1", m2.jobsModalCursor)
	}

	model, _ = m2.updateJobsModal(tea.KeyMsg{Type: tea.KeyDown})
	m3 := model.(TuiModel)
	if m3.jobsModalCursor != 1 {
		t.Errorf("cursor should clamp at last index (1), got %d", m3.jobsModalCursor)
	}
}

func TestUpdateJobsModal_UpClampsAtZero(t *testing.T) {
	m := newTestModelForJobs()
	m.applyJobUpdate(jobs.Job{ID: "a", StartedAt: time.Now()})
	m.openJobsModal()
	model, _ := m.updateJobsModal(tea.KeyMsg{Type: tea.KeyUp})
	m2 := model.(TuiModel)
	if m2.jobsModalCursor != 0 {
		t.Errorf("cursor should clamp at 0, got %d", m2.jobsModalCursor)
	}
}

func TestUpdateJobsModal_EscapeCloses(t *testing.T) {
	m := newTestModelForJobs()
	m.openJobsModal()
	model, _ := m.updateJobsModal(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := model.(TuiModel)
	if m2.jobsModalActive {
		t.Error("expected ESC to close the jobs modal")
	}
}

func TestUpdateJobsModal_EnterWithNoJobsDoesNothing(t *testing.T) {
	m := newTestModelForJobs()
	m.openJobsModal()
	model, _ := m.updateJobsModal(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := model.(TuiModel)
	if m2.subagentModalActive {
		t.Error("Enter with no jobs must not open a result modal")
	}
	if !m2.jobsModalActive {
		t.Error("jobs modal should remain open when there is nothing to select")
	}
}

// ─── SetJobEventBus ─────────────────────────────────────────────────────

func TestSetJobEventBus_NilBusIsNoop(t *testing.T) {
	// A *TUI with a nil prog would panic on prog.Send; SetJobEventBus(nil)
	// must return before spawning the subscriber goroutine that would call
	// it, so this must not panic even though tui.prog is never set.
	tui := &TUI{done: make(chan struct{})}
	tui.SetJobEventBus(nil)
}

// ─── Ctrl+B opens the jobs modal ───────────────────────────────────────────

func TestHandleGlobalKey_CtrlBOpensJobsModal(t *testing.T) {
	m := newTestModelForJobs()
	handled, model, _ := m.handleGlobalKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	if !handled {
		t.Fatal("expected Ctrl+B to be handled as a global key")
	}
	m2 := model.(TuiModel)
	if !m2.jobsModalActive {
		t.Error("expected Ctrl+B to open the jobs modal")
	}
}
