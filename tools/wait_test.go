package tools

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// noSleep makes plain-wait tests instant: it "elapses" the requested
// duration without actually sleeping, unless ctx is already cancelled.
func noSleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	default:
		return true
	}
}

func TestWaitTool_PlainWait(t *testing.T) {
	tool := &WaitTool{Sleep: noSleep}
	res := tool.Run(context.Background(), map[string]any{"seconds": 5, "note": "deploy finishing"})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "waited 5s") || !strings.Contains(res.Content, "deploy finishing") {
		t.Fatalf("unexpected content: %q", res.Content)
	}
}

func TestWaitTool_PlainWaitNoNote(t *testing.T) {
	tool := &WaitTool{Sleep: noSleep}
	res := tool.Run(context.Background(), map[string]any{"seconds": 5})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "waited 5s") {
		t.Fatalf("unexpected content: %q", res.Content)
	}
}

func TestWaitTool_ClampsHigh(t *testing.T) {
	tool := &WaitTool{Sleep: noSleep}
	res := tool.Run(context.Background(), map[string]any{"seconds": MaxWaitSeconds + 500})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "clamped to maximum") {
		t.Fatalf("expected clamp note, got: %q", res.Content)
	}
}

func TestWaitTool_ClampsLow(t *testing.T) {
	tool := &WaitTool{Sleep: noSleep}
	res := tool.Run(context.Background(), map[string]any{"seconds": 0})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "clamped to minimum") {
		t.Fatalf("expected clamp note, got: %q", res.Content)
	}
}

func TestWaitTool_MissingSeconds(t *testing.T) {
	tool := &WaitTool{Sleep: noSleep}
	res := tool.Run(context.Background(), map[string]any{})
	if res.Success {
		t.Fatal("expected failure when seconds is missing")
	}
}

func TestWaitTool_CancelledByContext(t *testing.T) {
	tool := &WaitTool{} // real defaultSleep
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before Run
	res := tool.Run(ctx, map[string]any{"seconds": 5})
	if !res.Success {
		t.Fatalf("cancellation should not be an error, got: %s", res.Error)
	}
	if !strings.Contains(res.Content, "wait cancelled") {
		t.Fatalf("unexpected content: %q", res.Content)
	}
}

func TestWaitTool_DefaultSleepRespectsContextCancelMidway(t *testing.T) {
	tool := &WaitTool{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := tool.Run(ctx, map[string]any{"seconds": MinWaitSeconds}) // 1s of "real" wait
	elapsed := time.Since(start)
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected cancellation to cut wait short, took %v", elapsed)
	}
	if !strings.Contains(res.Content, "wait cancelled") {
		t.Fatalf("unexpected content: %q", res.Content)
	}
}

// mockJobWaiter is a controllable JobWaiter for testing the job_id path.
type mockJobWaiter struct {
	status JobStatus
	ok     bool
}

func (m *mockJobWaiter) Wait(ctx context.Context, id string, timeout time.Duration) (JobStatus, bool) {
	return m.status, m.ok
}

func TestWaitTool_JobIDWithoutWaiter(t *testing.T) {
	tool := &WaitTool{}
	res := tool.Run(context.Background(), map[string]any{"seconds": 5, "job_id": "abc"})
	if res.Success {
		t.Fatal("expected failure when Waiter is nil")
	}
	if !strings.Contains(res.Error, "job registry unavailable") {
		t.Fatalf("unexpected error: %s", res.Error)
	}
}

func TestWaitTool_JobIDUnknown(t *testing.T) {
	tool := &WaitTool{Waiter: &mockJobWaiter{ok: false}}
	res := tool.Run(context.Background(), map[string]any{"seconds": 5, "job_id": "abc"})
	if res.Success {
		t.Fatal("expected failure for unknown job_id")
	}
	if !strings.Contains(res.Error, "unknown job_id") {
		t.Fatalf("unexpected error: %s", res.Error)
	}
}

func TestWaitTool_JobIDDoneSuccess(t *testing.T) {
	tool := &WaitTool{Waiter: &mockJobWaiter{ok: true, status: JobStatus{
		ID: "abc", Done: true, Success: true, Content: "built artifact.zip",
	}}}
	res := tool.Run(context.Background(), map[string]any{"seconds": 5, "job_id": "abc"})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "job finished: built artifact.zip") {
		t.Fatalf("unexpected content: %q", res.Content)
	}
}

func TestWaitTool_JobIDDoneFailure(t *testing.T) {
	tool := &WaitTool{Waiter: &mockJobWaiter{ok: true, status: JobStatus{
		ID: "abc", Done: true, Success: false, Error: "build failed: exit 1",
	}}}
	res := tool.Run(context.Background(), map[string]any{"seconds": 5, "job_id": "abc"})
	if res.Success {
		t.Fatal("expected failure to propagate")
	}
	if res.Error != "build failed: exit 1" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
}

func TestWaitTool_JobIDStillRunning(t *testing.T) {
	tool := &WaitTool{Waiter: &mockJobWaiter{ok: true, status: JobStatus{
		ID: "abc", Done: false,
	}}}
	res := tool.Run(context.Background(), map[string]any{"seconds": 5, "job_id": "abc"})
	if !res.Success {
		t.Fatalf("still-running should not be an error, got: %s", res.Error)
	}
	if !strings.Contains(res.Content, "still running") || !strings.Contains(res.Content, "job_id=abc") {
		t.Fatalf("unexpected content: %q", res.Content)
	}
}

func TestWaitTool_Name(t *testing.T) {
	tool := &WaitTool{}
	if tool.Name() != "wait" {
		t.Fatalf("expected name 'wait', got %q", tool.Name())
	}
}

func TestWaitTool_RegisteredInToolRegistry(t *testing.T) {
	tool, ok := toolRegistry["wait"]
	if !ok {
		t.Fatal("wait tool not registered in toolRegistry")
	}
	if tool.Name() != "wait" {
		t.Fatalf("registered tool has wrong name: %s", tool.Name())
	}
}

// A wait on a job is a wait for the RESULT, not a sleep. Treating it as a
// sleep is what made this tool waste turns: one observed session asked for a
// second, got "still running after 1s", and had learned nothing a notice would
// not have delivered free. Two minutes later it did the same and got the same.

type scriptedWaiter struct {
	mu     sync.Mutex
	status JobStatus
	known  bool
	calls  int
}

func (w *scriptedWaiter) Wait(ctx context.Context, id string, timeout time.Duration) (JobStatus, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if !w.known {
		return JobStatus{}, false
	}
	if !w.status.Done && !w.status.Waiting {
		// Mimic the registry: block for the slice it was given.
		w.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(timeout):
		}
		w.mu.Lock()
	}
	return w.status, true
}

func (w *scriptedWaiter) set(s JobStatus) {
	w.mu.Lock()
	w.status = s
	w.mu.Unlock()
}

func TestWaitForJobReturnsTheResultNotAStatus(t *testing.T) {
	w := &scriptedWaiter{known: true}
	tool := &WaitTool{Waiter: w}

	// Finishes shortly after the wait starts.
	go func() {
		time.Sleep(300 * time.Millisecond)
		w.set(JobStatus{Done: true, Success: true, Content: "the answer"})
	}()

	res := tool.Run(context.Background(), map[string]any{"job_id": "job-1", "seconds": 60})
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if !strings.Contains(res.Content, "the answer") {
		t.Fatalf("expected the job's result, got %q", res.Content)
	}
}

// TestWaitForJobNeedsNoSeconds: the caller wants a result, and inventing a
// duration for it was never the point.
func TestWaitForJobNeedsNoSeconds(t *testing.T) {
	w := &scriptedWaiter{known: true}
	tool := &WaitTool{Waiter: w}
	go func() {
		time.Sleep(200 * time.Millisecond)
		w.set(JobStatus{Done: true, Success: true, Content: "done without a duration"})
	}()

	res := tool.Run(context.Background(), map[string]any{"job_id": "job-1"})
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if !strings.Contains(res.Content, "done without a duration") {
		t.Fatalf("got %q", res.Content)
	}
}

// TestPlainWaitStillNeedsSeconds: without a job there is nothing to wait for,
// so a duration is the only instruction there is.
func TestPlainWaitStillNeedsSeconds(t *testing.T) {
	tool := &WaitTool{}
	res := tool.Run(context.Background(), map[string]any{})
	if res.Success {
		t.Fatal("expected an error")
	}
	if !strings.Contains(res.Error, "seconds is required") {
		t.Fatalf("got %q", res.Error)
	}
}

// TestShortJobWaitIsRaised is the exact wasted call from the session: one
// second on a job cannot do anything except report what a notice reports free.
func TestShortJobWaitIsRaised(t *testing.T) {
	w := &scriptedWaiter{known: true}
	tool := &WaitTool{Waiter: w}
	go func() {
		time.Sleep(150 * time.Millisecond)
		w.set(JobStatus{Done: true, Success: true, Content: "quick"})
	}()

	res := tool.Run(context.Background(), map[string]any{"job_id": "job-1", "seconds": 1})
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if !strings.Contains(res.Content, "raised to") {
		t.Errorf("the caller should be told its duration was raised: %q", res.Content)
	}
}

// TestWaitForJobStopsWhenItBlocksOnAQuestion: only the caller can answer, and
// it cannot answer from inside this call — waiting on would strand both until
// the timeout.
func TestWaitForJobStopsWhenItBlocksOnAQuestion(t *testing.T) {
	w := &scriptedWaiter{known: true}
	tool := &WaitTool{Waiter: w}
	go func() {
		time.Sleep(150 * time.Millisecond)
		w.set(JobStatus{Waiting: true, Question: "which branch?"})
	}()

	start := time.Now()
	res := tool.Run(context.Background(), map[string]any{"job_id": "job-1", "seconds": 600})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited %v on a job that was blocked on a question", elapsed)
	}
	if !strings.Contains(res.Content, "which branch?") {
		t.Fatalf("got %q", res.Content)
	}
	// Item 29: this used to tell the model to "answer(job_id=..., text=...)"
	// as if that were unconditionally the right move. It must now tell the
	// model to relay to the user (or genuinely-known info) unless it truly
	// knows the answer, and use the renamed tool.
	if !strings.Contains(res.Content, "\"answer_job\"") {
		t.Fatalf("expected the renamed answer_job tool to be named, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "unless you truly know the answer") {
		t.Fatalf("expected the nuanced relay-unless-you-know wording, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "Never invent an answer standing in for a human who hasn't replied") {
		t.Fatalf("expected the never-invent wording, got %q", res.Content)
	}
}

// TestWaitForJobStopsWhenSomeoneTypes: a person outranks a job, and the job is
// not disturbed by the wait ending.
func TestWaitForJobStopsWhenSomeoneTypes(t *testing.T) {
	w := &scriptedWaiter{known: true}
	tool := &WaitTool{Waiter: w}

	var typed atomic.Bool
	SetUserPending(typed.Load)
	defer SetUserPending(nil)
	go func() {
		time.Sleep(150 * time.Millisecond)
		typed.Store(true)
	}()

	start := time.Now()
	res := tool.Run(context.Background(), map[string]any{"job_id": "job-1", "seconds": 600})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("kept waiting for %v after someone typed", elapsed)
	}
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	for _, want := range []string{"someone typed", "was not touched", "still running"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("the result is missing %q: %q", want, res.Content)
		}
	}
}

// TestPlainWaitStopsWhenSomeoneTypes: the same rule as a job wait, for the same
// reason. A plain wait of ten minutes was ten minutes in which nothing the
// person typed would be read.
func TestPlainWaitStopsWhenSomeoneTypes(t *testing.T) {
	var typed atomic.Bool
	SetUserPending(typed.Load)
	defer SetUserPending(nil)

	slices := 0
	tool := &WaitTool{Sleep: func(ctx context.Context, d time.Duration) bool {
		slices++
		if slices == 3 {
			typed.Store(true)
		}
		return true
	}}

	res := tool.Run(context.Background(), map[string]any{"seconds": 600})
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if !strings.Contains(res.Content, "someone typed") {
		t.Fatalf("the wait ran on regardless: %q", res.Content)
	}
	// Stopped at the third slice, not after the full ten minutes.
	if slices != 3 {
		t.Errorf("slept %d slices, want 3", slices)
	}
}

// TestPlainWaitCountsTheSlicesItAskedFor. A wall-clock deadline plus an
// injected sleep that does not actually sleep is an infinite loop; this is the
// guard for that.
func TestPlainWaitCountsTheSlicesItAskedFor(t *testing.T) {
	SetUserPending(nil)
	slices := 0
	tool := &WaitTool{Sleep: func(ctx context.Context, d time.Duration) bool {
		slices++
		return true // returns without sleeping, like every test sleep here
	}}

	done := make(chan ToolResult, 1)
	go func() { done <- tool.Run(context.Background(), map[string]any{"seconds": 10}) }()

	select {
	case res := <-done:
		if !strings.Contains(res.Content, "waited 10s") {
			t.Fatalf("got %q", res.Content)
		}
		if want := int(10 * time.Second / jobPollInterval); slices != want {
			t.Errorf("slept %d slices, want %d", slices, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait never finished — a fake sleep against a wall-clock deadline never will")
	}
}
