package tools

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// multiWaiter answers per job id, so one test can run jobs in different states.
type multiWaiter struct {
	mu   sync.Mutex
	jobs map[string]JobStatus
}

func (w *multiWaiter) Wait(ctx context.Context, id string, timeout time.Duration) (JobStatus, bool) {
	w.mu.Lock()
	st, ok := w.jobs[id]
	w.mu.Unlock()
	if !ok {
		return JobStatus{}, false
	}
	if !st.Done {
		select {
		case <-ctx.Done():
		case <-time.After(timeout):
		}
	}
	return st, true
}

func (w *multiWaiter) set(id string, s JobStatus) {
	w.mu.Lock()
	w.jobs[id] = s
	w.mu.Unlock()
}

func TestWaitIDsAny(t *testing.T) {
	w := &multiWaiter{jobs: map[string]JobStatus{
		"job-a": {Done: true, Success: true, Content: "result-a"},
		"job-b": {},
	}}
	tool := &WaitTool{Waiter: w}

	start := time.Now()
	res := tool.Run(context.Background(), map[string]any{"ids": []any{"job-a", "job-b"}, "mode": "any", "seconds": 600})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("mode any waited %v after the first job finished", elapsed)
	}
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	for _, want := range []string{"job_id=job-a: finished: result-a", "job_id=job-b: still running"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("result is missing %q: %q", want, res.Content)
		}
	}
}

func TestWaitIDsAll(t *testing.T) {
	w := &multiWaiter{jobs: map[string]JobStatus{
		"job-a": {Done: true, Success: true, Content: "result-a"},
		"job-b": {},
	}}
	tool := &WaitTool{Waiter: w}
	go func() {
		time.Sleep(300 * time.Millisecond)
		w.set("job-b", JobStatus{Done: true, Success: false, Error: "boom"})
	}()

	start := time.Now()
	res := tool.Run(context.Background(), map[string]any{"ids": []any{"job-a", "job-b", "job-a"}, "mode": "all", "seconds": 600})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("mode all waited too long: %v", elapsed)
	}
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	for _, want := range []string{"job_id=job-a: finished: result-a", "job_id=job-b: failed: boom"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("result is missing %q: %q", want, res.Content)
		}
	}
	if strings.Count(res.Content, "job_id=job-a") != 1 {
		t.Errorf("a repeated id must be listed once: %q", res.Content)
	}
}

func TestWaitIDsRejectsBadMode(t *testing.T) {
	tool := &WaitTool{Waiter: &multiWaiter{jobs: map[string]JobStatus{}}}
	res := tool.Run(context.Background(), map[string]any{"ids": []any{"job-a"}, "mode": "some", "seconds": 60})
	if res.Success {
		t.Fatalf("expected an error for mode %q", "some")
	}
}

func TestWaitIDsRejectsEmptyList(t *testing.T) {
	tool := &WaitTool{Waiter: &multiWaiter{jobs: map[string]JobStatus{}}}
	res := tool.Run(context.Background(), map[string]any{"ids": []any{}, "seconds": 60})
	if res.Success {
		t.Fatalf("an empty ids list must fail, got %q", res.Content)
	}
}

func TestWaitIDsWakesOnUserMessage(t *testing.T) {
	w := &multiWaiter{jobs: map[string]JobStatus{"job-a": {}, "job-b": {}}}
	tool := &WaitTool{Waiter: w}

	var typed atomic.Bool
	SetUserPending(typed.Load)
	defer SetUserPending(nil)
	go func() {
		time.Sleep(150 * time.Millisecond)
		typed.Store(true)
	}()

	start := time.Now()
	res := tool.Run(context.Background(), map[string]any{"ids": []any{"job-a", "job-b"}, "mode": "all", "seconds": 600})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("kept waiting for %v after someone typed", elapsed)
	}
	if !strings.Contains(res.Content, "someone typed") {
		t.Errorf("result does not say the wait stopped for a user message: %q", res.Content)
	}
}

// TestWaitJobIDAlias: job_id still works, and it gives the same result as the
// same id in ids.
func TestWaitJobIDAlias(t *testing.T) {
	w := &multiWaiter{jobs: map[string]JobStatus{"job-a": {Done: true, Success: true, Content: "same"}}}
	tool := &WaitTool{Waiter: w}

	single := tool.Run(context.Background(), map[string]any{"job_id": "job-a"})
	if !single.Success || !strings.Contains(single.Content, "same") {
		t.Fatalf("job_id alias failed: %+v", single)
	}
	listed := tool.Run(context.Background(), map[string]any{"ids": []any{"job-a"}})
	if !listed.Success || !strings.Contains(listed.Content, "job_id=job-a: finished: same") {
		t.Fatalf("ids form failed: %+v", listed)
	}
	both := tool.Run(context.Background(), map[string]any{"ids": []any{"job-a"}, "job_id": "job-a"})
	if !both.Success || strings.Count(both.Content, "job_id=job-a") != 1 {
		t.Fatalf("ids plus job_id must dedupe: %+v", both)
	}
}
