package tools

import (
	"context"
	"strings"
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
