package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestStartAndGet(t *testing.T) {
	r := NewRegistry()
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
	r := NewRegistry()

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
	r := NewRegistry()
	job := r.Start(context.Background(), "root", KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		return "done", false, nil
	})
	got, _ := r.Get(job.ID)
	if got.ParentID != "" {
		t.Fatalf("expected empty ParentID for a top-level job, got %q", got.ParentID)
	}
}

func TestWaitBlocksUntilDone(t *testing.T) {
	r := NewRegistry()
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
	r := NewRegistry()
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
	r := NewRegistry()
	notices := make(chan Job, 4)
	r.SetOnEvent(func(job Job) { notices <- job })

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
	r := NewRegistry()
	notices := make(chan Job, 2)
	r.SetOnEvent(func(job Job) { notices <- job })
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
	r := NewRegistry()
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
	r := NewRegistry()
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

func TestSetOnEvent_NilIsNoop(t *testing.T) {
	r := NewRegistry()
	// nil is the default; explicitly setting it back to nil must not panic.
	r.SetOnEvent(nil)

	job := r.Start(context.Background(), "no-hook", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
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
	r := NewRegistry()
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
	var gotFromUser bool
	var gotOK bool

	job := r.Start(context.Background(), "asker", KindOther, "", func(ctx context.Context, jobID string) (string, bool, error) {
		gotAnswer, gotFromUser, gotOK = r.Ask(ctx, jobID, "what should I do?")
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

	if !r.Answer(job.ID, "do the thing", true) {
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
	if !gotFromUser {
		t.Fatal("expected fromUser=true for an Answer call made with fromUser=true")
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

	job := r.Start(context.Background(), "asker", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	})

	askCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	answer, _, ok := r.Ask(askCtx, job.ID, "anyone there?")
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
	answer, _, ok := r.Ask(context.Background(), "unknown", "q")
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

	job := r.Start(context.Background(), "not-waiting", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	})

	if r.Answer(job.ID, "nobody asked", true) {
		t.Fatal("expected Answer to return false for a job that isn't waiting")
	}
}

// TestAnswer_UnknownIDReturnsFalse covers Answer against an id the registry
// has never seen.
func TestAnswer_UnknownIDReturnsFalse(t *testing.T) {
	r := NewRegistry()
	if r.Answer("unknown", "text", true) {
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
	r := NewRegistry()

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

// TestAsk_TimeoutReplacesAnswerChannelSoStaleAnswersCannotLeak guards a
// narrow race: ctx.Done() can win Ask's select a moment before a
// concurrent Answer call (which reads job.answerCh/job.Status under the
// same lock Ask uses to reset them) manages to buffer a value into the
// OLD channel. Without replacing the channel on the timeout path, that
// stale value sits in the cap-1 buffer for the NEXT Ask on this same job
// to receive as if it were its own answer.
//
// This white-box test skips reproducing the exact timing (the window is
// too narrow to hit reliably) and instead injects the race's outcome
// directly — same effect, deterministic — then checks a fresh Ask on the
// same job does not receive it.
func TestAsk_TimeoutReplacesAnswerChannelSoStaleAnswersCannotLeak(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)

	job := r.Start(context.Background(), "asker", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	})

	askCtx, cancel := context.WithCancel(context.Background())
	cancel() // already done: Ask takes the ctx.Done() branch immediately
	if _, _, ok := r.Ask(askCtx, job.ID, "first question"); ok {
		t.Fatal("expected the first Ask to time out (ok=false) with an already-cancelled ctx")
	}

	// Inject the race's outcome: a stale answer landing in whatever
	// channel the job's answerCh field held right after the timeout.
	r.mu.Lock()
	staleCh := job.answerCh
	r.mu.Unlock()
	if staleCh != nil {
		select {
		case staleCh <- jobAnswer{text: "stale", fromUser: true}:
		default:
			t.Fatal("expected room to inject the stale answer into a fresh cap-1 channel")
		}
	}

	// A fresh Ask on the same job must not receive the stale value. If the
	// channel were reused (the pre-fix behavior), this returns instantly
	// with answer="stale"; with the fix, answerCh was reset to nil, so
	// this Ask lazily creates a brand-new empty channel and genuinely
	// times out instead.
	shortCtx, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	answer, _, ok := r.Ask(shortCtx, job.ID, "second question")
	if ok && answer == "stale" {
		t.Fatal("the second Ask received a stale answer left over from the first, timed-out Ask")
	}
}
