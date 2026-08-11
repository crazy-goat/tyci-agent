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

	job := r.Start(context.Background(), "demo", func(ctx context.Context, _ string) (string, bool, error) {
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
	job := r.Start(context.Background(), "demo", func(ctx context.Context, _ string) (string, bool, error) {
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

	job := r.Start(context.Background(), "long", func(ctx context.Context, _ string) (string, bool, error) {
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
	job := r.Start(context.Background(), "failing", func(ctx context.Context, _ string) (string, bool, error) {
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
	job := r.Start(context.Background(), "truncated", func(ctx context.Context, _ string) (string, bool, error) {
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

	job := r.Start(context.Background(), "listed", func(ctx context.Context, _ string) (string, bool, error) {
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

	r.Start(context.Background(), "hooked", func(ctx context.Context, _ string) (string, bool, error) {
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

	job := r.Start(context.Background(), "no-hook", func(ctx context.Context, _ string) (string, bool, error) {
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

	r.Start(context.Background(), "callback", func(ctx context.Context, _ string) (string, bool, error) {
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

	job := r.Start(context.Background(), "long", func(ctx context.Context, _ string) (string, bool, error) {
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

// TestAskThenAnswer_UnblocksWithRightTextAndStatusFlow drives Ask/Answer
// through the deterministic onEvent hook (not sleeps) to observe the exact
// status sequence: running -> waiting_answer -> running again, with the
// right answer text delivered back to the blocked Ask call.
func TestAskThenAnswer_UnblocksWithRightTextAndStatusFlow(t *testing.T) {
	r := NewRegistry()

	var mu sync.Mutex
	var statuses []Status
	sawWaiting := make(chan struct{})
	sawRunningAgain := make(chan struct{})
	var waitingClosedOnce, runningAgainClosedOnce bool

	r.SetOnEvent(func(j Job) {
		mu.Lock()
		statuses = append(statuses, j.Status)
		n := len(statuses)
		mu.Unlock()
		if j.Status == StatusWaitingAnswer && !waitingClosedOnce {
			waitingClosedOnce = true
			close(sawWaiting)
		}
		if n >= 3 && j.Status == StatusRunning && !runningAgainClosedOnce {
			runningAgainClosedOnce = true
			close(sawRunningAgain)
		}
	})

	askDone := make(chan struct{})
	var gotAnswer string
	var gotOK bool

	job := r.Start(context.Background(), "asker", func(ctx context.Context, jobID string) (string, bool, error) {
		gotAnswer, gotOK = r.Ask(ctx, jobID, "what should I do?")
		close(askDone)
		return "finished", false, nil
	})

	select {
	case <-sawWaiting:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StatusWaitingAnswer event")
	}

	snap, ok := r.Get(job.ID)
	if !ok {
		t.Fatal("expected job to be found")
	}
	if snap.Status != StatusWaitingAnswer {
		t.Fatalf("expected StatusWaitingAnswer, got %s", snap.Status)
	}
	if snap.Question != "what should I do?" {
		t.Fatalf("expected question to be recorded, got %q", snap.Question)
	}

	if !r.Answer(job.ID, "do the thing") {
		t.Fatal("expected Answer to succeed")
	}

	select {
	case <-askDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Ask to return")
	}

	if !gotOK {
		t.Fatal("expected Ask to report ok=true")
	}
	if gotAnswer != "do the thing" {
		t.Fatalf("expected answer %q, got %q", "do the thing", gotAnswer)
	}

	final, ok := r.Wait(context.Background(), job.ID, time.Second)
	if !ok {
		t.Fatal("expected wait to find job")
	}
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s", final.Status)
	}
	if final.Question != "" {
		t.Fatalf("expected question cleared after answer, got %q", final.Question)
	}

	mu.Lock()
	defer mu.Unlock()
	// running (Start) -> waiting_answer (Ask) -> running (unblocked) -> done (finish)
	if len(statuses) < 4 {
		t.Fatalf("expected at least 4 status events, got %v", statuses)
	}
	if statuses[0] != StatusRunning {
		t.Fatalf("expected first event running, got %s", statuses[0])
	}
	if statuses[1] != StatusWaitingAnswer {
		t.Fatalf("expected second event waiting_answer, got %s", statuses[1])
	}
	foundRunningAgain := false
	for _, s := range statuses[2:] {
		if s == StatusRunning {
			foundRunningAgain = true
		}
	}
	if !foundRunningAgain {
		t.Fatalf("expected a running event after waiting_answer, got %v", statuses)
	}
	if statuses[len(statuses)-1] != StatusDone {
		t.Fatalf("expected last event done, got %s", statuses[len(statuses)-1])
	}
}

// TestAsk_UnblockedByContextCancellationReturnsNotOK proves a job's own
// wall-clock timeout (modeled here as an explicit short-deadline ctx) is
// enough to unblock a forgotten Ask, instead of hanging forever.
func TestAsk_UnblockedByContextCancellationReturnsNotOK(t *testing.T) {
	r := NewRegistry()

	release := make(chan struct{})
	defer close(release)

	job := r.Start(context.Background(), "asker", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	})

	askCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	answer, ok := r.Ask(askCtx, job.ID, "anyone there?")
	elapsed := time.Since(start)

	if ok {
		t.Fatalf("expected ok=false after ctx cancellation, got answer %q", answer)
	}
	if answer != "" {
		t.Fatalf("expected empty answer, got %q", answer)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected Ask to return promptly after ctx deadline, elapsed=%s", elapsed)
	}

	snap, ok := r.Get(job.ID)
	if !ok {
		t.Fatal("expected job to be found")
	}
	if snap.Status != StatusRunning {
		t.Fatalf("expected job status reset to running, got %s", snap.Status)
	}
}

// TestAsk_UnknownIDReturnsNotOK covers Ask against an id the registry has
// never seen.
func TestAsk_UnknownIDReturnsNotOK(t *testing.T) {
	r := NewRegistry()
	answer, ok := r.Ask(context.Background(), "unknown", "q")
	if ok || answer != "" {
		t.Fatalf("expected (\"\", false), got (%q, %v)", answer, ok)
	}
}

// TestAnswer_OnJobNotWaitingReturnsFalse covers a job that exists but isn't
// currently blocked on Ask.
func TestAnswer_OnJobNotWaitingReturnsFalse(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)

	job := r.Start(context.Background(), "not-waiting", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	})

	if r.Answer(job.ID, "nobody asked") {
		t.Fatal("expected Answer to return false for a job that isn't waiting")
	}
}

// TestAnswer_UnknownIDReturnsFalse covers Answer against an id the registry
// has never seen.
func TestAnswer_UnknownIDReturnsFalse(t *testing.T) {
	r := NewRegistry()
	if r.Answer("unknown", "text") {
		t.Fatal("expected Answer to return false for an unknown id")
	}
}

// TestSetProgress_UpdatesSnapshotAndUnknownIDReturnsFalse covers SetProgress
// updating Snapshot().Progress, persisting after the job finishes, and
// reporting false for an unknown id.
func TestSetProgress_UpdatesSnapshotAndUnknownIDReturnsFalse(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	setOK := make(chan bool, 1)

	job := r.Start(context.Background(), "progressive", func(ctx context.Context, jobID string) (string, bool, error) {
		setOK <- r.SetProgress(jobID, "halfway there")
		<-release
		return "done", false, nil
	})

	if !<-setOK {
		t.Fatal("expected SetProgress to succeed for a known job")
	}

	deadline := time.Now().Add(time.Second)
	for {
		snap, ok := r.Get(job.ID)
		if !ok {
			t.Fatal("expected job to be found")
		}
		if snap.Progress == "halfway there" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for progress to be recorded, last snapshot: %+v", snap)
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(release)

	final, ok := r.Wait(context.Background(), job.ID, time.Second)
	if !ok {
		t.Fatal("expected wait to find job")
	}
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s", final.Status)
	}
	if final.Progress != "halfway there" {
		t.Fatalf("expected progress to persist after job finished, got %q", final.Progress)
	}

	if r.SetProgress("unknown", "x") {
		t.Fatal("expected SetProgress to return false for an unknown id")
	}
}
