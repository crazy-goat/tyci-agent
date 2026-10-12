package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// funcPublisher adapts a plain function to EventPublisher for tests.
type funcPublisher func(Job)

func (f funcPublisher) JobEvent(j Job) { f(j) }

// setPublisher replaces r.pub. Tests call it before any job starts. nil clears it.
func setPublisher(r *Registry, fn func(Job)) {
	if fn == nil {
		r.pub = nil
		return
	}
	r.pub = funcPublisher(fn)
}

func TestStartAndGet(t *testing.T) {
	r := NewRegistry(nil)
	release := make(chan struct{})

	job := r.Start(context.Background(), "demo", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
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

// TestStart_RecordsKindAndParentID covers item 1's plumbing: a child job
// created while another job is "in flight" must record which job spawned it
// and what kind of job it is, so a consumer (the Subagents/Bash sidebar
// tabs) can filter and reconstruct a tree via a parent-link walk.
func TestStart_RecordsKindAndParentID(t *testing.T) {
	r := NewRegistry(nil)

	parent := r.Start(context.Background(), "parent", KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		return "done", false, nil
	})
	r.Wait(context.Background(), parent.ID, time.Second)

	child := r.Start(context.Background(), "child", KindBash, parent.ID, func(ctx context.Context, _ string) (string, bool, error) {
		return "done", false, nil
	})

	// Wait's returned *Job is a Snapshot()-produced copy, safe to read
	// without the registry lock — unlike r.Get, which hands back the live
	// *Job that the goroutine above may still be concurrently writing
	// (Status/Result/Err/FinishedAt) until it finishes. Reading that live
	// pointer raced under `go test -race` even though Kind/ParentID
	// themselves are set once at Start and never mutated again.
	got, ok := r.Wait(context.Background(), child.ID, time.Second)
	if !ok {
		t.Fatalf("expected child job to be found")
	}
	if got.Kind != KindBash {
		t.Fatalf("expected Kind=%q, got %q", KindBash, got.Kind)
	}
	if got.ParentID != parent.ID {
		t.Fatalf("expected ParentID=%q, got %q", parent.ID, got.ParentID)
	}
}

// TestStart_TopLevelJobHasEmptyParentID covers the root case: a job spawned
// directly from the main conversation (not from inside another job) records
// no parent — that is how the Subagents tree tells a root row from a nested
// one, per item 1's spec.
func TestStart_TopLevelJobHasEmptyParentID(t *testing.T) {
	r := NewRegistry(nil)
	job := r.Start(context.Background(), "root", KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		return "done", false, nil
	})
	got, _ := r.Get(job.ID)
	if got.ParentID != "" {
		t.Fatalf("expected empty ParentID for a top-level job, got %q", got.ParentID)
	}
}

func TestWaitBlocksUntilDone(t *testing.T) {
	r := NewRegistry(nil)
	job := r.Start(context.Background(), "demo", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
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
	r := NewRegistry(nil)
	release := make(chan struct{})
	defer close(release)

	job := r.Start(context.Background(), "long", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
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
	r := NewRegistry(nil)

	if _, ok := r.Get("unknown"); ok {
		t.Fatalf("expected Get to return false for unknown ID")
	}

	if _, ok := r.Wait(context.Background(), "unknown", time.Second); ok {
		t.Fatalf("expected Wait to return false for unknown ID")
	}
}

func TestJobFails(t *testing.T) {
	r := NewRegistry(nil)
	job := r.Start(context.Background(), "failing", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
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

func TestJobPanicIsRecoveredAndRegistryStaysUsable(t *testing.T) {
	r := NewRegistry(nil)
	notices := make(chan Job, 4)
	setPublisher(r, func(job Job) { notices <- job })

	panicked := r.Start(context.Background(), "panicking", KindOther, "", func(context.Context, string) (string, bool, error) {
		panic("boom")
	})

	failed, ok := r.Wait(context.Background(), panicked.ID, time.Second)
	if !ok {
		t.Fatal("expected wait to find panicking job")
	}
	if failed.Status != StatusFailed {
		t.Fatalf("expected failed status after panic, got %s", failed.Status)
	}
	if failed.Err != "job function panicked: boom" {
		t.Fatalf("expected panic error, got %q", failed.Err)
	}
	if failed.FinishedAt.IsZero() {
		t.Fatal("expected panic job to have a completion time")
	}
	select {
	case notice := <-notices:
		if notice.Status != StatusRunning || notice.ID != panicked.ID {
			t.Fatalf("unexpected start notice: %+v", notice)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for start notice")
	}
	select {
	case notice := <-notices:
		if notice.Status != StatusFailed || notice.ID != panicked.ID || notice.Err != failed.Err {
			t.Fatalf("unexpected panic completion notice: %+v", notice)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for panic completion notice")
	}

	// A recovered panic must not take down the registry's worker machinery or
	// the process: a later job still starts and completes normally.
	after := r.Start(context.Background(), "after panic", KindOther, "", func(context.Context, string) (string, bool, error) {
		return "alive", false, nil
	})
	finished, ok := r.Wait(context.Background(), after.ID, time.Second)
	if !ok || finished.Status != StatusDone || finished.Result != "alive" {
		t.Fatalf("registry unusable after panic: ok=%v job=%+v", ok, finished)
	}
}

type panicFormattingValue struct{}

func (panicFormattingValue) Error() string          { panic("error formatter panic") }
func (panicFormattingValue) String() string         { panic("string formatter panic") }
func (panicFormattingValue) Format(fmt.State, rune) { panic("format formatter panic") }

func TestJobPanicWithUnprintableValueIsRecovered(t *testing.T) {
	r := NewRegistry(nil)
	notices := make(chan Job, 2)
	setPublisher(r, func(job Job) { notices <- job })
	jobContext := make(chan context.Context, 1)

	job := r.Start(context.Background(), "unprintable panic", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		jobContext <- ctx
		panic(panicFormattingValue{})
	})

	failed, ok := r.Wait(context.Background(), job.ID, time.Second)
	if !ok || failed.Status != StatusFailed {
		t.Fatalf("expected panic job to fail, got ok=%v job=%+v", ok, failed)
	}
	if failed.Err != "job function panicked: <unprintable panic value>" {
		t.Fatalf("unexpected panic error: %q", failed.Err)
	}
	select {
	case ctx := <-jobContext:
		select {
		case <-ctx.Done():
		default:
			t.Fatal("job context was not cancelled after panic completion")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job context")
	}

	select {
	case start := <-notices:
		if start.ID != job.ID || start.Status != StatusRunning {
			t.Fatalf("unexpected start notice: %+v", start)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for start notice")
	}
	select {
	case done := <-notices:
		if done.ID != job.ID || done.Status != StatusFailed || done.Err != failed.Err {
			t.Fatalf("unexpected completion notice: %+v", done)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completion notice")
	}

	// The worker completed and its done signal was closed, so cancellation is
	// no longer accepted; a subsequent job proves the process remains alive.
	if r.Cancel(job.ID) {
		t.Fatal("Cancel should reject the terminal panic job")
	}
	after := r.Start(context.Background(), "after unprintable panic", KindOther, "", func(context.Context, string) (string, bool, error) {
		return "alive", false, nil
	})
	alive, ok := r.Wait(context.Background(), after.ID, time.Second)
	if !ok || alive.Status != StatusDone || alive.Result != "alive" {
		t.Fatalf("registry unusable after unprintable panic: ok=%v job=%+v", ok, alive)
	}
}

func TestJobTruncated(t *testing.T) {
	r := NewRegistry(nil)
	job := r.Start(context.Background(), "truncated", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
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
	r := NewRegistry(nil)
	release := make(chan struct{})
	defer close(release)

	job := r.Start(context.Background(), "listed", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
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

func TestRegistry_PublishesStartAndTerminalEvents_InOrder(t *testing.T) {
	r := NewRegistry(nil)

	var mu sync.Mutex
	var statuses []Status
	done := make(chan struct{})
	setPublisher(r, func(j Job) {
		mu.Lock()
		statuses = append(statuses, j.Status)
		if len(statuses) == 2 {
			close(done)
		}
		mu.Unlock()
	})

	r.Start(context.Background(), "hooked", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
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

// A SetProgress snapshot taken while the job is running can reach onEvent after
// the job's terminal snapshot (#131). EventSeq lets a subscriber tell that the
// late one is older.
func TestRegistry_EventSeqOrdersLateSnapshotBeforeTerminal(t *testing.T) {
	r := NewRegistry(nil)

	var mu sync.Mutex
	var events []Job
	inHook := make(chan struct{})
	release := make(chan struct{})
	setPublisher(r, func(j Job) {
		if j.Status == StatusRunning && j.Progress == "late" {
			close(inHook)
			<-release
		}
		mu.Lock()
		events = append(events, j)
		mu.Unlock()
	})

	finish := make(chan struct{})
	job := r.Start(context.Background(), "racy", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-finish
		return "ok", false, nil
	})

	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		r.SetProgress(job.ID, "late")
	}()
	select {
	case <-inHook:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the progress snapshot to reach onEvent")
	}

	close(finish)
	select {
	case <-job.done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the job to finish")
	}
	// The terminal event is published after job.done closes; wait for it.
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the terminal event, have %d events", n)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	<-progressDone

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 3 {
		t.Fatalf("expected 3 events (start, terminal, late progress), got %d", len(events))
	}
	terminal, late := events[1], events[2]
	if terminal.Status != StatusDone || late.Status != StatusRunning {
		t.Fatalf("unexpected delivery order: %s then %s", terminal.Status, late.Status)
	}
	if late.EventSeq >= terminal.EventSeq {
		t.Fatalf("late snapshot EventSeq %d must be lower than terminal EventSeq %d", late.EventSeq, terminal.EventSeq)
	}
	if events[0].EventSeq >= late.EventSeq {
		t.Fatalf("EventSeq must grow: start %d, late %d", events[0].EventSeq, late.EventSeq)
	}
}

func TestRegistry_NilPublisher_NoPanic(t *testing.T) {
	r := NewRegistry(nil)

	job := r.Start(context.Background(), "no-hook", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		return "ok", false, nil
	})

	if _, ok := r.Wait(context.Background(), job.ID, time.Second); !ok {
		t.Fatalf("expected wait to find job")
	}
}

// TestRegistry_PublisherMayCallBackIntoRegistry ensures the hook fires outside any
// internal lock: calling Get/List from within the callback must not deadlock.
func TestRegistry_PublisherMayCallBackIntoRegistry(t *testing.T) {
	r := NewRegistry(nil)
	done := make(chan struct{})

	setPublisher(r, func(j Job) {
		if j.Status != StatusDone {
			return
		}
		if _, ok := r.Get(j.ID); !ok {
			t.Errorf("expected Get to find job %q from within onEvent callback", j.ID)
		}
		_ = r.List()
		close(done)
	})

	r.Start(context.Background(), "callback", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		return "ok", false, nil
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out — onEvent callback likely deadlocked calling back into the registry")
	}
}

func TestWaitRespectsContextCancellation(t *testing.T) {
	r := NewRegistry(nil)
	release := make(chan struct{})
	defer close(release)

	job := r.Start(context.Background(), "long", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
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

// TestSetProgress_UpdatesSnapshotAndUnknownIDReturnsFalse covers SetProgress
// updating Snapshot().Progress, persisting after the job finishes, and
// reporting false for an unknown id.
func TestSetProgress_UpdatesSnapshotAndUnknownIDReturnsFalse(t *testing.T) {
	r := NewRegistry(nil)
	release := make(chan struct{})
	setOK := make(chan bool, 1)

	job := r.Start(context.Background(), "progressive", KindOther, "", func(ctx context.Context, jobID string) (string, bool, error) {
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

// TestRegistryPrunesOldTerminalJobs guards the retained-history bound. Each
// finished job holds its full result — up to the bash output cap for a
// backgrounded shell command — so an unbounded map is a session-long leak.
func TestRegistryPrunesOldTerminalJobs(t *testing.T) {
	r := NewRegistry(nil)

	// A job that stays running must never be pruned, however much finishes
	// around it: something is still waiting on it.
	block := make(chan struct{})
	defer close(block)
	live := r.Start(context.Background(), "long runner", KindOther, "", func(context.Context, string) (string, bool, error) {
		<-block
		return "", false, nil
	})

	total := maxRetainedTerminalJobs + 10
	for i := 0; i < total; i++ {
		job := r.Start(context.Background(), fmt.Sprintf("quick %d", i), KindOther, "", func(context.Context, string) (string, bool, error) {
			return "done", false, nil
		})
		if _, ok := r.Wait(context.Background(), job.ID, 5*time.Second); !ok {
			t.Fatalf("job %d not found while waiting", i)
		}
	}

	list := r.List()
	terminal := 0
	foundLive := false
	for _, job := range list {
		if job.ID == live.ID {
			foundLive = true
			continue
		}
		terminal++
	}
	if !foundLive {
		t.Fatal("a still-running job was pruned")
	}
	if terminal > maxRetainedTerminalJobs {
		t.Fatalf("expected at most %d finished jobs retained, got %d", maxRetainedTerminalJobs, terminal)
	}
	if terminal < maxRetainedTerminalJobs {
		t.Fatalf("pruned too eagerly: expected %d finished jobs retained, got %d", maxRetainedTerminalJobs, terminal)
	}
}

func TestPendingLinesIsEmptyWhenNothingIsOutstanding(t *testing.T) {
	r := NewRegistry(nil)
	job := r.Start(context.Background(), "quick", KindOther, "", func(ctx context.Context, id string) (string, bool, error) {
		return "ok", false, nil
	})
	if _, ok := r.Wait(context.Background(), job.ID, 2*time.Second); !ok {
		t.Fatal("job never finished")
	}

	if lines := r.PendingLines(); len(lines) != 0 {
		t.Fatalf("expected nothing outstanding, got %v", lines)
	}
}
