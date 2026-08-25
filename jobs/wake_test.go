package jobs

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Batch 2 (question/notice delivery) — B6, half of B7, and review-round C1/C2.
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
// above this package (see wiring_b7_dedup_test.go in package main); this
// file only pins the registry-level signal those decisions are built on.
//
// C1 (review round 1): only Wait counts toward QuestionHasWaiter.
// WaitObserve — used by a caller that watches a job for its own purposes
// without itself reporting the question to anyone (tools/subagent.go's
// runWithHandoff watcher) — must NOT. Coupling the two through one counter
// made Ask suppress the only delivery a question had whenever a blocking
// subagent call was in flight, which is nearly always.
//
// C2 (review round 1): the waiters-- must happen in the SAME critical
// section as Wait's own snapshot, not after a second, separate Lock/Unlock.
// Otherwise a Wait call whose timeout elapsed (so its own snapshot shows
// Status==StatusRunning, no question) could still be counted as a waiter a
// moment longer, letting Ask see waiters>0 and suppress the notice for a
// caller who had, in fact, already returned without ever seeing the
// question.

// waitUntilRegistered polls WaiterCount (a real, synchronized count, not a
// sleep-and-hope) until it reports at least one caller blocked in Wait for
// id, or fails the test. Used wherever a test needs to know a Wait call has
// actually reached the point of registering itself before triggering the
// transition it wants that call to catch — a fixed sleep here would (per
// review) risk the goroutine still not having reached
// `statusCh := job.statusChanged` by the time the transition happens, in
// which case it captures the NEW channel and blocks for the full timeout
// instead of waking.
func waitUntilRegistered(t *testing.T, r *Registry, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if r.WaiterCount(id) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Wait call never registered as a waiter on the job")
		}
		time.Sleep(time.Millisecond)
	}
}

// waitUntilObserverRegistered is waitUntilRegistered's WaitObserve
// counterpart (see Registry.ObserverCount).
func waitUntilObserverRegistered(t *testing.T, r *Registry, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if r.ObserverCount(id) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("WaitObserve call never registered as an observer on the job")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWait_WakesPromptlyWhenJobEntersWaitingAnswer is B6's core case: a
// Wait call already blocked (with a generous timeout) must return as soon
// as the job asks a question, not after the timeout elapses and not only
// on a later, separate call.
func TestWait_WakesPromptlyWhenJobEntersWaitingAnswer(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})

	job := r.Start(context.Background(), "asker", KindOther, "", func(ctx context.Context, jobID string) (string, bool, error) {
		<-release
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

	// Deterministic handshake, not a sleep: only ask once Wait has
	// genuinely registered as a waiter (see waitUntilRegistered's doc
	// comment) — otherwise this could pass or fail depending on whether
	// the goroutine above got scheduled in time, exactly the flake the
	// review flagged.
	waitUntilRegistered(t, r, job.ID)

	start := time.Now()
	close(release)

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
//
// Review round 1 flagged the original version of this test as passing both
// before and after the fix — the job could reach StatusDone before Wait was
// even called, leaving no window for the hypothetical regression to land
// in. This version keeps the job open (blocked on release) past the answer,
// starts Wait BEFORE answering, confirms it has genuinely registered (see
// waitUntilRegistered), and then — before letting the job actually finish —
// asserts nothing has come back yet. If the reverted signal were
// reinstated, that assertion is exactly where this test would catch it.
func TestWait_AnsweredThenWaitStillReachesDone(t *testing.T) {
	r := NewRegistry()
	sawWaiting := make(chan struct{})
	var sawWaitingOnce sync.Once
	r.SetOnEvent(func(j Job) {
		if j.Status == StatusWaitingAnswer {
			sawWaitingOnce.Do(func() { close(sawWaiting) })
		}
	})

	release := make(chan struct{})
	job := r.Start(context.Background(), "asker", KindOther, "", func(ctx context.Context, jobID string) (string, bool, error) {
		_, _, _ = r.Ask(ctx, jobID, "continue?")
		<-release
		return "done", false, nil
	})

	select {
	case <-sawWaiting:
	case <-time.After(2 * time.Second):
		t.Fatal("job never reached StatusWaitingAnswer")
	}

	waitReturned := make(chan *Job, 1)
	go func() {
		snap, ok := r.Wait(context.Background(), job.ID, 3*time.Second)
		if ok {
			waitReturned <- snap
		}
	}()
	waitUntilRegistered(t, r, job.ID)

	if !r.Answer(job.ID, "yes", true) {
		t.Fatal("expected Answer to succeed")
	}

	// The revert-to-Running transition just happened (or, on a regression,
	// just woke the blocked Wait above). Give it a moment to have done so,
	// then confirm nothing has come back — the job is deliberately still
	// held open past this point via release.
	time.Sleep(50 * time.Millisecond)
	select {
	case snap := <-waitReturned:
		t.Fatalf("Wait returned early with status %s right after the question was answered but before the job finished — the revert-to-Running transition must not wake a blocked Wait call", snap.Status)
	default:
	}

	close(release)

	select {
	case snap := <-waitReturned:
		if snap.Status != StatusDone {
			t.Fatalf("expected job to finish as done, got %s", snap.Status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait never returned after the job actually finished")
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

	go func() {
		r.Wait(context.Background(), job.ID, 5*time.Second)
	}()
	// Deterministic handshake (see waitUntilRegistered): the question is
	// only asked once Wait has genuinely registered as a waiter.
	waitUntilRegistered(t, r, job.ID)
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

// TestAsk_QuestionHasWaiter_FalseWhenOnlyAnObserverIsWatching is C1's
// direct regression test at the source: a WaitObserve call blocked on the
// job when it asks its question must NOT set QuestionHasWaiter, unlike an
// equivalent Wait call (see TestAsk_QuestionHasWaiter_TrueWhenAWaitIsAlreadyBlocked
// above). Before WaitObserve existed, runWithHandoff's watcher used Wait,
// so this exact scenario — something merely watching, not reporting —
// always set QuestionHasWaiter and suppressed the only delivery the
// question had (tools.SubagentTool's handoff message did not carry it
// either, at the time). See jobs.Registry.WaitObserve's doc comment.
func TestAsk_QuestionHasWaiter_FalseWhenOnlyAnObserverIsWatching(t *testing.T) {
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

	go func() {
		r.WaitObserve(context.Background(), job.ID, 5*time.Second)
	}()
	// Deterministic handshake via ObserverCount (batch-2 review round 2's
	// "optional if cheap" suggestion) — WaitObserve does not touch
	// job.waiters, so waitUntilRegistered (which polls WaiterCount) cannot
	// be used here, but ObserverCount tracks exactly this call instead of
	// needing a settle sleep.
	waitUntilObserverRegistered(t, r, job.ID)
	close(release)

	select {
	case j := <-askedCh:
		if j.QuestionHasWaiter {
			t.Fatal("expected QuestionHasWaiter=false when only a WaitObserve call is watching — it never reports the question to anyone, so it must not suppress the notice that does")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the job to ask its question")
	}

	r.Answer(job.ID, "yes", true)
}

// TestWait_TimeoutAndAskRace_NoDroppedQuestion is C2's regression coverage:
// a Wait call whose timeout elapses at essentially the same moment Ask
// flips the job into StatusWaitingAnswer must never leave Ask believing
// this caller will report the question (QuestionHasWaiter=true) once that
// caller has ALREADY taken a "still running, no question" snapshot and is
// on its way back to whoever called it.
//
// Before the fix (a deferred waiters-- running in a second, separate
// critical section after the snapshot), this was reachable: Wait's select
// fires via timer.C, its snapshot is taken while the job is still Running,
// and only then — still counted as a waiter a moment longer — does Ask
// (running concurrently, for the same job) see waiters>0 and suppress the
// notice for a caller who will never see the question at all.
//
// The exact interleaving is a handful of instructions wide, so rather than
// one exact reproduction this runs many trials with the Wait timeout set
// right at the edge of when Ask fires, giving a regression a realistic
// chance to be caught — especially under -race, which widens scheduling
// windows rather than narrowing them. The invariant checked on every trial
// is real, not hypothetical: whenever a Wait call's own returned snapshot
// shows Status==StatusRunning (it did not itself see the question), the
// onEvent snapshot for the question that job asked around the same time
// must not have QuestionHasWaiter set because of THIS call.
//
// Honest framing (batch-2 review round 2, D8): this invariant cannot
// false-fail — nothing here can make it fail on correct code — so it is
// harmless to keep. But 200 trials is not a guarantee the race window is
// actually hit even once; this is a probabilistic net across many
// interleavings, not a deterministic reproduction, and it should not be
// read as proof the original C2 bug would have been caught by this test
// before it existed. No cheap way to force the exact interleaving
// deterministically was found (it would need a hook inside waitInternal
// itself to pause between the timer firing and the lock re-acquisition,
// which is not worth adding to production code for a test).
func TestWait_TimeoutAndAskRace_NoDroppedQuestion(t *testing.T) {
	const trials = 200
	for i := 0; i < trials; i++ {
		r := NewRegistry()
		askedCh := make(chan Job, 1)
		r.SetOnEvent(func(j Job) {
			if j.Status == StatusWaitingAnswer {
				select {
				case askedCh <- j:
				default:
				}
			}
		})

		start := make(chan struct{})
		job := r.Start(context.Background(), "asker", KindOther, "", func(ctx context.Context, jobID string) (string, bool, error) {
			<-start
			_, _, _ = r.Ask(ctx, jobID, "continue?")
			return "done", false, nil
		})

		waitReturned := make(chan *Job, 1)
		go func() {
			// A very short timeout so its own timer.C branch is likely to
			// be the one that fires, right around when start is closed
			// below and the job asks its question.
			snap, ok := r.Wait(context.Background(), job.ID, time.Millisecond)
			if ok {
				waitReturned <- snap
			}
		}()
		close(start)

		var waitStatus Status
		select {
		case snap := <-waitReturned:
			waitStatus = snap.Status
		case <-time.After(2 * time.Second):
			t.Fatalf("trial %d: Wait never returned", i)
		}

		select {
		case j := <-askedCh:
			if waitStatus == StatusRunning && j.QuestionHasWaiter {
				t.Fatalf("trial %d: Wait's own snapshot was %s (it never saw the question) but QuestionHasWaiter was still true — the question would have been dropped", i, waitStatus)
			}
			r.Answer(job.ID, "yes", true)
		case <-time.After(2 * time.Second):
			t.Fatalf("trial %d: job never asked its question", i)
		}

		r.Wait(context.Background(), job.ID, 2*time.Second)
	}
}
