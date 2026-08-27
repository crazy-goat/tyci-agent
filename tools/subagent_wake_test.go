package tools

// B5 (batch-2 audit): runWithHandoff's blocking-call select used to be
// blind to a spawned child entering StatusWaitingAnswer — it only noticed a
// blocked child indirectly, by sitting out the rest of SubagentBackgroundAfterSec
// (or the whole call, without handoff) before the handoff even let the
// parent see the queued ask-notice. These tests assert on the wake itself
// (a channel closing / a goroutine unblocking), not on wall-clock luck.
//
// Review round 1 (C1) found that the fix as first written broke worse than
// the bug: the watcher used jobs.Registry.Wait, which counts toward
// QuestionHasWaiter, so Ask suppressed the onEvent notice for essentially
// every blocking subagent call — and the watcher itself delivers the
// question nowhere. The fix is JobObserver (backed by
// jobs.Registry.WaitObserve), which does not count, PLUS handOff itself
// now asks the observer what each still-running child's pending question
// is (pendingQuestions) and puts it in the handoff message — so the
// message's own promise ("you will be told... when one is BLOCKED on a
// question") is something it can make good on directly, not only
// something the onEvent notice might.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// realJobObserver wraps a real jobs.Registry via WaitObserve — the
// non-counting path runWithHandoff's watcher and handOff's pendingQuestions
// peek actually use in production (see main.go's jobObserverAdapter). Using
// WaitObserve (not Wait) here is what makes these tests exercise the real
// fix for C1 rather than the pre-fix shape — see JobObserver's doc comment
// for the distinction.
type realJobObserver struct{ reg *jobs.Registry }

func (w realJobObserver) Observe(ctx context.Context, id string, timeout time.Duration) (JobStatus, bool) {
	job, ok := w.reg.WaitObserve(ctx, id, timeout)
	if !ok {
		return JobStatus{}, false
	}
	return JobStatus{
		ID:          job.ID,
		Done:        job.Status != jobs.StatusRunning && job.Status != jobs.StatusWaitingAnswer,
		Success:     job.Status == jobs.StatusDone || job.Status == jobs.StatusTruncated,
		Content:     job.Result,
		Error:       job.Err,
		Waiting:     job.Status == jobs.StatusWaitingAnswer,
		Question:    job.Question,
		QuestionSeq: job.QuestionSeq,
	}, true
}

// wakeEnv wires a real registry plus a JobObserver over it (SetJobObserver)
// — matching production wiring, and specifically the non-counting path
// runWithHandoff's watcher actually uses.
func wakeEnv(t *testing.T) *jobs.Registry {
	t.Helper()
	reg := jobs.NewRegistry()
	SetJobStarter(testJobStarter{reg})
	SetJobObserver(realJobObserver{reg})
	SetJobNotifier(&recordingNotifier{})
	SetBackgroundBashEnabled(true)
	t.Cleanup(func() {
		SetJobStarter(nil)
		SetJobObserver(nil)
		SetJobNotifier(nil)
		SetBackgroundBashEnabled(false)
	})
	return reg
}

// TestWatchForWaiting_WakesWhenAlreadyWaitingBeforeCalled is B5's explicit
// "asks before the select is entered" case, isolated to the exact
// mechanism responsible for it: watchForWaiting's own first call uses
// timeout 0 specifically so a question already pending by the time it
// starts watching is not missed (see its doc comment). The question is
// asked and confirmed pending (via jobs.Registry's onEvent hook — a real
// synchronization point, not a sleep) strictly BEFORE watchForWaiting is
// ever invoked, so there is nothing here for a select to race against: if
// this test is flaky or hangs, the "ask before watching starts" case is
// broken.
func TestWatchForWaiting_WakesWhenAlreadyWaitingBeforeCalled(t *testing.T) {
	reg := jobs.NewRegistry()
	sawWaiting := make(chan struct{})
	var sawWaitingOnce sync.Once
	reg.SetOnEvent(func(j jobs.Job) {
		if j.Status == jobs.StatusWaitingAnswer {
			sawWaitingOnce.Do(func() { close(sawWaiting) })
		}
	})

	job := reg.Start(context.Background(), "asker", jobs.KindSubagent, "", func(ctx context.Context, jobID string) (string, bool, error) {
		_, _, _ = reg.Ask(ctx, jobID, "which branch?")
		return "done", false, nil
	})

	select {
	case <-sawWaiting:
	case <-time.After(2 * time.Second):
		t.Fatal("job never reached StatusWaitingAnswer")
	}

	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		watchForWaiting(context.Background(), realJobObserver{reg}, job.ID, wake)
		close(done)
	}()

	select {
	case <-wake:
	case <-time.After(2 * time.Second):
		t.Fatal("watchForWaiting did not wake for a question that was already pending before it started watching")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchForWaiting did not return after sending its wake")
	}

	reg.Answer(job.ID, "main", true)
}

// TestRunWithHandoff_WakesWhenChildAsksMidCall is B5's main scenario: a
// child that asks a question shortly after runWithHandoff's select has
// already committed to blocking must make the call return promptly,
// instead of only after SubagentBackgroundAfterSec (here set far longer
// than the assertion's margin, so a pass proves the wake fired — it cannot
// be explained by the timer arm winning instead).
//
// Review round 1 flagged the original version of this test for asserting
// only the wake, not the delivery — exactly the gap C1 slipped through:
// the call returned promptly, but the question reached nobody. This
// version additionally asserts the handoff message itself carries the
// question text (see handOff's pendingQuestions), which is what actually
// closes that gap.
func TestRunWithHandoff_WakesWhenChildAsksMidCall(t *testing.T) {
	reg := wakeEnv(t)
	t.Cleanup(SetSubagentBackgroundAfterSecForTests(30 * time.Second))

	askedAt100ms := make(chan struct{})
	answered := make(chan struct{})
	const question = "which environment?"
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _ string, _ string, _ SubagentOptions) (string, error) {
			jobID, _ := ctx.Value(JobIDCtxKey{}).(string)
			time.Sleep(100 * time.Millisecond)
			close(askedAt100ms)
			answer, _, _ := reg.Ask(ctx, jobID, question)
			close(answered)
			return "answer was: " + answer, nil
		},
	}}

	start := time.Now()
	resultCh := make(chan struct {
		res    ToolResult
		handed bool
	}, 1)
	go func() {
		res, handed := tool.runWithHandoff(context.Background(), []subagentTask{{Task: "ask something"}}, true)
		resultCh <- struct {
			res    ToolResult
			handed bool
		}{res, handed}
	}()

	select {
	case <-askedAt100ms:
	case <-time.After(2 * time.Second):
		t.Fatal("child never reached the point of asking")
	}

	select {
	case r := <-resultCh:
		elapsed := time.Since(start)
		if !r.handed {
			t.Fatalf("expected the call to hand off once the child asked a question, got inline result: %+v", r.res)
		}
		if !r.res.Success {
			t.Fatalf("handoff result was not success: %+v", r.res)
		}
		// Comfortably below SubagentBackgroundAfterSec (30s): the wake, not
		// the timer, must be what ended this call.
		if elapsed > 5*time.Second {
			t.Fatalf("runWithHandoff took %s to return after the child asked; expected a prompt wake well under the %s handoff timer", elapsed, SubagentBackgroundAfterSec())
		}
		// C1: the wake alone is not enough — the handoff message must
		// actually carry the question, or the ONLY thing delivered here
		// would be the fact that a child exists and is running, not what
		// it is blocked on. (Separately: this registry has no onEvent hook
		// wired — see wakeEnv — so there is no ask-notice in play here to
		// worry about duplicating. Production wires one via main.go's
		// wireTools, and item 54's fix is what keeps that notice from
		// repeating what this same handoff message already says; see
		// TestWiring_54_* in wiring_ask_notice_dedup_test.go for that,
		// end to end.)
		if !strings.Contains(r.res.Content, question) {
			t.Fatalf("handoff message does not mention the pending question %q — it only promises one will come, without delivering it: %s", question, r.res.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithHandoff did not return promptly after the child asked a question")
	}

	// Let the child finish so it does not leak past the test.
	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("expected exactly one job, got %d", len(list))
	}
	if !reg.Answer(list[0].ID, "staging", true) {
		t.Fatal("expected Answer to succeed")
	}
	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		t.Fatal("child never received its answer")
	}
}
