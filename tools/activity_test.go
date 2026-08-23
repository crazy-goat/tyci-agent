package tools

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// recordingToucher is a fake JobActivityToucher that just records every id
// it was touched with, standing in for jobs.Registry in tests that only
// care about "was the touch-point reached", not the registry's own storage.
type recordingToucher struct {
	mu   sync.Mutex
	seen []string
}

func (r *recordingToucher) TouchActivity(id string) {
	r.mu.Lock()
	r.seen = append(r.seen, id)
	r.mu.Unlock()
}

func (r *recordingToucher) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

// withActivityToucher wires t as the package-level jobActivityToucher for the
// duration of one test and restores the previous (nil, in every test here)
// value afterwards — activity touching is process-global state like the
// other Set* wiring in this package.
func withActivityToucher(t *testing.T, toucher JobActivityToucher) {
	t.Helper()
	SetJobActivityToucher(toucher)
	t.Cleanup(func() { SetJobActivityToucher(nil) })
}

// TestStreamingCollector_TextTouchesActivity guards item 25's core streaming
// touch-point: every Text call on a job-bound streamingCollector must reach
// JobActivityToucher.TouchActivity with that job's id. Revert the touch call
// in streamingCollector.Text and this fails with zero recorded touches.
func TestStreamingCollector_TextTouchesActivity(t *testing.T) {
	rec := &recordingToucher{}
	withActivityToucher(t, rec)

	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, "job-1-1")
	sc := newStreamingCollector(ctx, 0)
	sc.Text("hello")

	if rec.count() != 1 {
		t.Fatalf("expected exactly 1 touch, got %d: %v", rec.count(), rec.seen)
	}
	if rec.seen[0] != "job-1-1" {
		t.Fatalf("expected touch for job-1-1, got %q", rec.seen[0])
	}
}

// TestStreamingCollector_AllSinkMethodsTouchActivity guards the same
// contract for Thinking/ToolCallStart/ToolCallDelta/ToolCallEnd — the full
// set of SubagentSink methods item 25 calls "the real heartbeat". Revert any
// one of these overrides back to the embedded *collector's no-op/counting
// behavior and this fails because that call no longer touches.
func TestStreamingCollector_AllSinkMethodsTouchActivity(t *testing.T) {
	rec := &recordingToucher{}
	withActivityToucher(t, rec)

	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, "job-1-2")
	sc := newStreamingCollector(ctx, 0)

	sc.Thinking("thinking")
	sc.ToolCallStart("bash")
	sc.ToolCallDelta("delta")
	sc.ToolCallEnd("bash", "result")

	if rec.count() != 4 {
		t.Fatalf("expected 4 touches (Thinking, ToolCallStart, ToolCallDelta, ToolCallEnd), got %d: %v", rec.count(), rec.seen)
	}
	for _, id := range rec.seen {
		if id != "job-1-2" {
			t.Fatalf("expected every touch to carry job-1-2, got %q in %v", id, rec.seen)
		}
	}
}

// TestStreamingCollector_NoJobIDNoTouch guards against a subagent call that
// was never handed a job id (e.g. a blocking call under a mode with
// backgrounding disabled) panicking or recording a bogus empty-string touch.
func TestStreamingCollector_NoJobIDNoTouch(t *testing.T) {
	rec := &recordingToucher{}
	withActivityToucher(t, rec)

	sc := newStreamingCollector(context.Background(), 0)
	sc.Text("hello") // no JobIDCtxKey in ctx

	if rec.count() != 0 {
		t.Fatalf("expected no touch without a job id, got %v", rec.seen)
	}
}

// TestTouchJobActivity_NilToucherIsNoop guards the "not wired yet" case
// (e.g. `tyci run` without job support): touching must be silently harmless,
// not a panic.
func TestTouchJobActivity_NilToucherIsNoop(t *testing.T) {
	SetJobActivityToucher(nil)
	touchJobActivity("job-1-1") // must not panic
}

// TestBashSetProgress_TouchesActivity guards item 25's other prescribed
// touch-point: tools/bash.go already calls SetProgress per backgrounded
// output line, and that same call must also touch "last activity" so a
// backgrounded shell command's output counts as a sign of life without any
// per-tool wiring. Revert the touchJobActivity call added alongside
// reporter.SetProgress in bashRun.setProgress and this fails.
func TestBashSetProgress_TouchesActivity(t *testing.T) {
	rec := &recordingToucher{}
	withActivityToucher(t, rec)

	reporter := &recordingProgressReporter{}
	jobProgressReporter = reporter
	t.Cleanup(func() { jobProgressReporter = nil })

	run := &bashRun{}
	run.setJobID("job-1-3")
	run.setProgress("some output line")

	if rec.count() != 1 {
		t.Fatalf("expected 1 activity touch from setProgress, got %d: %v", rec.count(), rec.seen)
	}
	if rec.seen[0] != "job-1-3" {
		t.Fatalf("expected touch for job-1-3, got %q", rec.seen[0])
	}
}

// TestBashSetProgress_NoJobIDNoTouch guards against touching activity for a
// bashRun that was never handed a job id (foreground / not backgrounded).
func TestBashSetProgress_NoJobIDNoTouch(t *testing.T) {
	rec := &recordingToucher{}
	withActivityToucher(t, rec)

	reporter := &recordingProgressReporter{}
	jobProgressReporter = reporter
	t.Cleanup(func() { jobProgressReporter = nil })

	run := &bashRun{} // no setJobID call
	run.setProgress("some output line")

	if rec.count() != 0 {
		t.Fatalf("expected no activity touch without a job id, got %v", rec.seen)
	}
}

// recordingProgressReporter is a minimal JobProgressReporter fake so
// TestBashSetProgress_* don't depend on a real jobs.Registry.
type recordingProgressReporter struct {
	mu   sync.Mutex
	seen []string
}

func (r *recordingProgressReporter) SetProgress(id, text string) bool {
	r.mu.Lock()
	r.seen = append(r.seen, id)
	r.mu.Unlock()
	return true
}

// TestRealRegistry_SatisfiesJobActivityToucher is a light integration check
// that jobs.Registry (the real production type wired in main.go) satisfies
// the tools.JobActivityToucher interface end to end: a touch through the
// interface must be observable on the registry's own Snapshot.
func TestRealRegistry_SatisfiesJobActivityToucher(t *testing.T) {
	reg := jobs.NewRegistry()
	withActivityToucher(t, reg)

	release := make(chan struct{})
	job := reg.Start(context.Background(), "demo", jobs.KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "", false, nil
	})
	defer close(release)

	before := job.Snapshot().LastActivity
	time.Sleep(5 * time.Millisecond)
	touchJobActivity(job.ID)
	after := job.Snapshot().LastActivity

	if !after.After(before) {
		t.Fatalf("expected LastActivity to advance through the real registry, before=%s after=%s", before, after)
	}
}
