package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestStartAndGet(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})

	job := r.Start(context.Background(), "demo", func(ctx context.Context) (string, bool, error) {
		<-release
		return "hello", false, nil
	})

	got, ok := r.Get(job.ID)
	if !ok {
		t.Fatalf("expected job to be found")
	}
	if got.ID != job.ID {
		t.Fatalf("expected same job ID")
	}

	final, ok := r.Wait(context.Background(), job.ID, 0)
	if !ok {
		t.Fatalf("expected wait to find job")
	}
	if final.Status != StatusRunning {
		t.Fatalf("expected running before release, got %s", final.Status)
	}

	close(release)

	final, ok = r.Wait(context.Background(), job.ID, time.Second)
	if !ok {
		t.Fatalf("expected wait to find job")
	}
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s", final.Status)
	}
	if final.Result != "hello" {
		t.Fatalf("expected result 'hello', got %q", final.Result)
	}
}

func TestWaitBlocksUntilDone(t *testing.T) {
	r := NewRegistry()
	job := r.Start(context.Background(), "demo", func(ctx context.Context) (string, bool, error) {
		time.Sleep(50 * time.Millisecond)
		return "done-result", false, nil
	})

	start := time.Now()
	final, ok := r.Wait(context.Background(), job.ID, time.Second)
	elapsed := time.Since(start)

	if !ok {
		t.Fatalf("expected wait to find job")
	}
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s", final.Status)
	}
	if final.Result != "done-result" {
		t.Fatalf("expected result 'done-result', got %q", final.Result)
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("expected wait to block until job finished, elapsed=%s", elapsed)
	}
}

func TestWaitTimeoutReturnsRunning(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)

	job := r.Start(context.Background(), "long", func(ctx context.Context) (string, bool, error) {
		<-release
		return "eventually", false, nil
	})

	final, ok := r.Wait(context.Background(), job.ID, 20*time.Millisecond)
	if !ok {
		t.Fatalf("expected wait to find job")
	}
	if final.Status != StatusRunning {
		t.Fatalf("expected running after timeout, got %s", final.Status)
	}
}

func TestUnknownID(t *testing.T) {
	r := NewRegistry()

	if _, ok := r.Get("unknown"); ok {
		t.Fatalf("expected Get to return false for unknown ID")
	}

	if _, ok := r.Wait(context.Background(), "unknown", time.Second); ok {
		t.Fatalf("expected Wait to return false for unknown ID")
	}
}

func TestJobFails(t *testing.T) {
	r := NewRegistry()
	job := r.Start(context.Background(), "failing", func(ctx context.Context) (string, bool, error) {
		return "", false, errors.New("boom")
	})

	final, ok := r.Wait(context.Background(), job.ID, time.Second)
	if !ok {
		t.Fatalf("expected wait to find job")
	}
	if final.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", final.Status)
	}
	if final.Err != "boom" {
		t.Fatalf("expected err 'boom', got %q", final.Err)
	}
}

func TestJobTruncated(t *testing.T) {
	r := NewRegistry()
	job := r.Start(context.Background(), "truncated", func(ctx context.Context) (string, bool, error) {
		return "partial output", true, nil
	})

	final, ok := r.Wait(context.Background(), job.ID, time.Second)
	if !ok {
		t.Fatalf("expected wait to find job")
	}
	if final.Status != StatusTruncated {
		t.Fatalf("expected truncated, got %s", final.Status)
	}
	if final.Result != "partial output" {
		t.Fatalf("expected result 'partial output', got %q", final.Result)
	}
}

func TestListReturnsSnapshots(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)

	job := r.Start(context.Background(), "listed", func(ctx context.Context) (string, bool, error) {
		<-release
		return "", false, nil
	})

	list := r.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 job in list, got %d", len(list))
	}
	if list[0].ID != job.ID {
		t.Fatalf("expected job ID %s, got %s", job.ID, list[0].ID)
	}
}

func TestSetOnEvent_CalledOnStartAndCompletion(t *testing.T) {
	r := NewRegistry()

	var mu sync.Mutex
	var statuses []Status
	done := make(chan struct{})
	r.SetOnEvent(func(j Job) {
		mu.Lock()
		statuses = append(statuses, j.Status)
		if len(statuses) == 2 {
			close(done)
		}
		mu.Unlock()
	})

	r.Start(context.Background(), "hooked", func(ctx context.Context) (string, bool, error) {
		return "ok", false, nil
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for both onEvent calls")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(statuses) != 2 {
		t.Fatalf("expected exactly 2 onEvent calls, got %d: %v", len(statuses), statuses)
	}
	if statuses[0] != StatusRunning {
		t.Fatalf("expected first event to be running, got %s", statuses[0])
	}
	if statuses[1] != StatusDone {
		t.Fatalf("expected second event to be the final status, got %s", statuses[1])
	}
}

func TestSetOnEvent_NilIsNoop(t *testing.T) {
	r := NewRegistry()
	// nil is the default; explicitly setting it back to nil must not panic.
	r.SetOnEvent(nil)

	job := r.Start(context.Background(), "no-hook", func(ctx context.Context) (string, bool, error) {
		return "ok", false, nil
	})

	if _, ok := r.Wait(context.Background(), job.ID, time.Second); !ok {
		t.Fatalf("expected wait to find job")
	}
}

// TestSetOnEvent_CanCallBackIntoRegistry ensures the hook fires outside any
// internal lock: calling Get/List from within the callback must not deadlock.
func TestSetOnEvent_CanCallBackIntoRegistry(t *testing.T) {
	r := NewRegistry()
	done := make(chan struct{})

	r.SetOnEvent(func(j Job) {
		if j.Status != StatusDone {
			return
		}
		if _, ok := r.Get(j.ID); !ok {
			t.Errorf("expected Get to find job %q from within onEvent callback", j.ID)
		}
		_ = r.List()
		close(done)
	})

	r.Start(context.Background(), "callback", func(ctx context.Context) (string, bool, error) {
		return "ok", false, nil
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out — onEvent callback likely deadlocked calling back into the registry")
	}
}

func TestWaitRespectsContextCancellation(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)

	job := r.Start(context.Background(), "long", func(ctx context.Context) (string, bool, error) {
		<-release
		return "eventually", false, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	final, ok := r.Wait(ctx, job.ID, time.Minute)
	elapsed := time.Since(start)

	if !ok {
		t.Fatalf("expected wait to find job")
	}
	if final.Status != StatusRunning {
		t.Fatalf("expected running after ctx cancellation, got %s", final.Status)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected wait to return promptly after cancellation, elapsed=%s", elapsed)
	}
}
