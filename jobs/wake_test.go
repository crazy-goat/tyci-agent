package jobs

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Batch 2 (question/notice delivery) — B6 and half of B7.
//
// B6: Wait must notice a job entering StatusWaitingAnswer while it is
// blocked, instead of only finding out on the next poll slice. These tests
// assert on an actual wake (Wait returning well before its timeout), not on
// wall-clock luck, so a regression back to polling (jobPollInterval-style)
// would show up as the assertions below timing out or reporting a much
// longer elapsed time than the margin allows.
//
// B7 (registry half): QuestionHasWaiter records whether a Wait call was
// already blocked on this exact job before Ask posed the question, which is
// what lets a caller upstream (main.go's onEvent hook) decide whether the
// async notice path would double up with a Wait call about to report the
// same question synchronously. The suppression/dedup decision itself lives
// above this package (see wiring_b4_notice_routing_test.go and
// wiring_b7_dedup_test.go in package main); this file only pins the
// registry-level signal those decisions are built on.

// TestWait_WakesPromptlyWhenJobEntersWaitingAnswer is B6's core case: a
// Wait call already blocked (with a generous timeout) must return as soon
// as the job asks a question, not after the timeout elapses and not only
// on a later, separate call.
func TestWait_WakesPromptlyWhenJobEntersWaitingAnswer(t *testing.T) {
	r := NewRegistry()
	askStarted := make(chan struct{})
	release := make(chan struct{})

	job := r.Start(context.Background(), "asker", KindOther, "", func(ctx context.Context, jobID string) (string, bool, error) {
		<-release
		close(askStarted)
		_, _, _ = r.Ask(ctx, jobID, "what color?")
		return "done", false, nil
	})

	const longTimeout = 10 * time.Second
	waitReturned := make(chan *Job, 1)
	go func() {
		snap, ok := r.Wait(context.Background(), job.ID, longTimeout)
		if !ok {
			t.Error("expected Wait to find the job")
			return
		}
		waitReturned <- snap
	}()

	// Give the goroutine above a moment to actually enter its select before
	// the job asks — this is what proves the wake, not a race won by luck:
	// if Wait were still polling under the hood, letting the job run only
	// after Wait has committed to blocking would make that obvious.
	time.Sleep(20 * time.Millisecond)

	start := time.Now()
	close(release)
	<-askStarted

	select {
	case snap := <-waitReturned:
		elapsed := time.Since(start)
		if snap.Status != StatusWaitingAnswer {
			t.Fatalf("expected StatusWaitingAnswer, got %s", snap.Status)
		}
		if snap.Question != "what color?" {
			t.Fatalf("expected question recorded, got %q", snap.Question)
		}
		// Comfortably below longTimeout: a regression to polling would
		// still land within jobPollInterval-scale delay in this package
		// (there is no such polling here — a caller doing its own slicing,
		// e.g. tools.WaitTool, is what jobPollInterval bounds), but must
		// never approach the full 10s timeout.
		if elapsed > 2*time.Second {
			t.Fatalf("Wait took %s to notice the job asking a question; expected a prompt wake, not something close to the %s timeout", elapsed, longTimeout)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return promptly after the job asked a question")
	}
}

// TestWait_AnsweredThenWaitStillReachesDone guards against a regression
// where signaling statusCh on the way OUT of StatusWaitingAnswer (not just
// the way in) would wake an unrelated, later Wait call the instant the
// question is answered and hand it a stale "running" snapshot instead of
// blocking on to the real completion it asked for.
func TestWait_AnsweredThenWaitStillReachesDone(t *testing.T) {
	r := NewRegistry()
	sawWaiting := make(chan struct{})
	var closedOnce sync.Once
	r.SetOnEvent(func(j Job) {
		if j.Status == StatusWaitingAnswer {
			closedOnce.Do(func() { close(sawWaiting) })
		}
	})

	job := r.Start(context.Background(), "asker", KindOther, "", func(ctx context.Context, jobID string) (string, bool, error) {
		_, _, _ = r.Ask(ctx, jobID, "continue?")
		return "done", false, nil
	})

	select {
	case <-sawWaiting:
	case <-time.After(2 * time.Second):
		t.Fatal("job never reached StatusWaitingAnswer")
	}

	if !r.Answer(job.ID, "yes", true) {
		t.Fatal("expected Answer to succeed")
	}

	final, ok := r.Wait(context.Background(), job.ID, 2*time.Second)
	if !ok || final.Status != StatusDone {
		t.Fatalf("expected job to finish, got ok=%v status=%v", ok, final)
	}
}

// TestAsk_QuestionHasWaiter_TrueWhenAWaitIsAlreadyBlocked is B7's
// registry-level half: a Wait call already in flight for this exact job,
// before Ask is ever invoked, must be reflected on the resulting snapshot.
func TestAsk_QuestionHasWaiter_TrueWhenAWaitIsAlreadyBlocked(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	askedCh := make(chan Job, 1)

	r.SetOnEvent(func(j Job) {
		if j.Status == StatusWaitingAnswer {
			select {
			case askedCh <- j:
			default:
			}
		}
	})

	job := r.Start(context.Background(), "asker", KindOther, "", func(ctx context.Context, jobID string) (string, bool, error) {
		<-release
		_, _, _ = r.Ask(ctx, jobID, "continue?")
		return "done", false, nil
	})

	waitEntered := make(chan struct{})
	go func() {
		close(waitEntered)
		r.Wait(context.Background(), job.ID, 5*time.Second)
	}()
	<-waitEntered
	// Give the Wait goroutine a moment to actually register itself as a
	// waiter (increment job.waiters) before the job asks its question.
	time.Sleep(20 * time.Millisecond)
	close(release)

	select {
	case j := <-askedCh:
		if !j.QuestionHasWaiter {
			t.Fatal("expected QuestionHasWaiter=true when a Wait call was already blocked on this job")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the job to ask its question")
	}

	r.Answer(job.ID, "yes", true)
}

// TestAsk_QuestionHasWaiter_FalseWhenNobodyIsWaiting is the other half of
// B7's dedup input: with no concurrent Wait call, the question must not be
// marked as already covered by one.
func TestAsk_QuestionHasWaiter_FalseWhenNobodyIsWaiting(t *testing.T) {
	r := NewRegistry()
	askedCh := make(chan Job, 1)
	var mu sync.Mutex
	var seen bool

	r.SetOnEvent(func(j Job) {
		if j.Status == StatusWaitingAnswer {
			mu.Lock()
			if !seen {
				seen = true
				askedCh <- j
			}
			mu.Unlock()
		}
	})

	job := r.Start(context.Background(), "asker", KindOther, "", func(ctx context.Context, jobID string) (string, bool, error) {
		_, _, _ = r.Ask(ctx, jobID, "continue?")
		return "done", false, nil
	})

	select {
	case j := <-askedCh:
		if j.QuestionHasWaiter {
			t.Fatal("expected QuestionHasWaiter=false with no concurrent Wait call")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the job to ask its question")
	}

	r.Answer(job.ID, "yes", true)
}
