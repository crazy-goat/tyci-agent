package tools

// A blocking subagent call hands its children to the background after
// SubagentBackgroundAfterSec, the same way bash does at 30s. Without it a slow
// child holds the whole turn open, and the person at the keyboard cannot type
// anything until it returns.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// handoffEnv wires a job registry and notifier, and overrides
// SubagentBackgroundAfterSec to after — a real 60s wait would otherwise make
// every test either wait it out or exercise the wait's early-exit paths
// (UserPending, ctx.Done()) exclusively, leaving the timer.C arm of
// runWithHandoff's select with no coverage at all.
func handoffEnv(t *testing.T, after time.Duration) (*jobs.Registry, *recordingNotifier) {
	t.Helper()
	reg := jobs.NewRegistry()
	notifier := &recordingNotifier{}
	SetJobStarter(testJobStarter{reg})
	SetJobNotifier(notifier)
	SetBackgroundBashEnabled(true) // the per-mode flag both handoffs share

	prevAfter := SubagentBackgroundAfterSec
	SubagentBackgroundAfterSec = after
	t.Cleanup(func() {
		SetJobStarter(nil)
		SetJobNotifier(nil)
		SetBackgroundBashEnabled(false)
		SubagentBackgroundAfterSec = prevAfter
	})
	return reg, notifier
}

func handoffTool(t *testing.T, work func() (string, error)) *SubagentTool {
	t.Helper()
	return &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _ string, _ string, _ SubagentOptions) (string, error) {
			return work()
		},
	}}
}

// TestBlockingCallReturnsInlineWhenTheChildIsQuick is the common case and must
// be untouched: a child that finishes in time is returned as text, exactly as
// before the handoff existed.
func TestBlockingCallReturnsInlineWhenTheChildIsQuick(t *testing.T) {
	_, notifier := handoffEnv(t, time.Minute)
	tool := handoffTool(t, func() (string, error) { return "the answer", nil })

	res, handed := tool.runWithHandoff(context.Background(), []subagentTask{{Task: "quick"}}, true)
	if handed {
		t.Fatalf("a quick child should not be handed over: %+v", res)
	}
	// The result must be usable directly — before the item-20 fix this branch
	// returned a bare ToolResult{} and relied on the caller re-running the
	// task via runTasks to get real content.
	if !res.Success || res.Content != "the answer" {
		t.Fatalf("expected the collected result inline, got %+v", res)
	}
	// And nothing is notified: the parent is about to read the result inline,
	// so a notice would be pure noise.
	if n := notifier.all(); len(n) != 0 {
		t.Fatalf("unexpected notices: %v", n)
	}
}

// TestBlockingCallHandsOverASlowChild: the turn ends, the child keeps going,
// and the message says so.
func TestBlockingCallHandsOverASlowChild(t *testing.T) {
	reg, notifier := handoffEnv(t, 0)
	release := make(chan struct{})
	tool := handoffTool(t, func() (string, error) {
		<-release
		return "eventually", nil
	})

	// Drive the private path with a short window rather than waiting 60s.
	spawned := []*spawnedTask{tool.spawn(context.Background(), subagentTask{Task: "the slow one"}, false, true)}
	time.Sleep(20 * time.Millisecond)
	res := tool.handOff(spawned)

	if !res.Success {
		t.Fatalf("handoff failed: %s", res.Error)
	}
	for _, want := range []string{
		"running in the background",
		"talk to them",   // the person gets their prompt back
		"answer(job_id=", // a blocked child needs answering
		"wait(job_id=",   // how to read the result
		"Do not call wait before you are told",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("the handoff message is missing %q:\n%s", want, res.Content)
		}
	}

	// The id must be usable, and the job must still be alive.
	var ids []struct {
		Task  string `json:"task"`
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(res.Content, "\n", 2)[0]), &ids); err != nil {
		t.Fatalf("the first line must be the id list: %v", err)
	}
	if len(ids) != 1 || ids[0].JobID == "" {
		t.Fatalf("got %+v", ids)
	}

	// Now let it finish: the parent is no longer waiting, so it must be told.
	close(release)
	job, ok := reg.Wait(context.Background(), ids[0].JobID, 5*time.Second)
	if !ok || job.Result != "eventually" {
		t.Fatalf("the handed-over child did not finish properly: %+v", job)
	}
	notices := notifier.all()
	if len(notices) != 1 || !strings.Contains(notices[0], "finished") {
		t.Fatalf("expected one completion notice, got %v", notices)
	}
	if !strings.Contains(notices[0], ids[0].JobID) {
		t.Errorf("the notice must carry the id: %q", notices[0])
	}
}

// TestHandoffReportsChildrenThatAlreadyFinished: a batch can be half done when
// the timer fires, and throwing those results away would make the parent wait
// for answers it already has.
func TestHandoffReportsChildrenThatAlreadyFinished(t *testing.T) {
	handoffEnv(t, 0)
	release := make(chan struct{})
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(_ context.Context, task string, _ string, _ SubagentOptions) (string, error) {
			if strings.Contains(task, "slow") {
				<-release
				return "late", nil
			}
			return "early", nil
		},
	}}

	quick := tool.spawn(context.Background(), subagentTask{Task: "quick one"}, false, true)
	slow := tool.spawn(context.Background(), subagentTask{Task: "slow one"}, false, true)
	<-quick.done
	res := tool.handOff([]*spawnedTask{quick, slow})
	// Let the handed-over child finish and wait for it: a job goroutine that
	// outlives the test would still be notifying after cleanup tore the
	// notifier down.
	close(release)
	<-slow.done

	if !strings.Contains(res.Content, "finished before the handoff") {
		t.Errorf("the finished child's result was dropped:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "early") {
		t.Errorf("the finished child's answer is missing:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, slow.jobID) {
		t.Errorf("the still-running child's id is missing:\n%s", res.Content)
	}
	if strings.Contains(res.Content, quick.jobID) {
		t.Errorf("a finished child should not be listed as still running:\n%s", res.Content)
	}
}

// TestHandoffOfASingleAlreadyFinishedTaskIsPlainText covers the rare race
// where every child finishes between the timer firing and handOff's loop
// running: with nothing left to hand over, handOff must shape its result the
// same way every other all-finished case does (resultsToToolResult) — a
// single task as plain text, not a one-element JSON array.
func TestHandoffOfASingleAlreadyFinishedTaskIsPlainText(t *testing.T) {
	handoffEnv(t, 0)
	tool := handoffTool(t, func() (string, error) { return "the answer", nil })

	st := tool.spawn(context.Background(), subagentTask{Task: "quick"}, false, true)
	<-st.done

	res := tool.handOff([]*spawnedTask{st})
	if !res.Success || res.Content != "the answer" {
		t.Fatalf("expected plain text for a single already-finished task, got %+v", res)
	}
}

// TestFinishAndHandRaceHasOneWinner. The timer can fire at the same instant a
// child completes, and exactly one outcome is allowed: either the result is
// returned inline, or the parent is notified later. Both means the parent is
// told twice; neither means the result is lost.
func TestFinishAndHandRaceHasOneWinner(t *testing.T) {
	st := &spawnedTask{done: make(chan struct{})}

	// finish first: hand must decline, and finish must not ask for a notice.
	if notified := st.finish(subagentResult{Content: "done"}); notified {
		t.Error("finishing before the handoff must not notify")
	}
	if st.hand() {
		t.Error("a finished task must not be handed over")
	}

	// hand first: finish must then ask for the notice.
	st2 := &spawnedTask{done: make(chan struct{})}
	if !st2.hand() {
		t.Fatal("an unfinished task must be handed over")
	}
	if notified := st2.finish(subagentResult{Content: "done"}); !notified {
		t.Error("a handed-over task must notify when it finishes")
	}
}

// TestHandoffStopsStreamingIntoAClosedBlock: the parent's tool block is gone
// once the call returns, so anything sent afterwards paints over finished
// output — the same hazard the bash tool solves with its handed flag.
func TestHandoffStopsStreamingIntoAClosedBlock(t *testing.T) {
	handoffEnv(t, 0)
	release := make(chan struct{})
	tool := handoffTool(t, func() (string, error) {
		<-release
		return "late", nil
	})

	st := tool.spawn(context.Background(), subagentTask{Task: "slow"}, false, true)
	if st.stopStream == nil {
		t.Fatal("no stream-stop flag was installed")
	}
	if st.stopStream.Load() {
		t.Fatal("streaming should be on while the parent is still waiting")
	}

	tool.handOff([]*spawnedTask{st})
	if !st.stopStream.Load() {
		t.Fatal("streaming was not stopped at the handoff")
	}
	close(release)
}

// TestNoHandoffWithoutAJobRegistry: in a one-shot run there is no next turn to
// deliver a notice into, so blocking is the only correct behaviour.
func TestNoHandoffWithoutAJobRegistry(t *testing.T) {
	SetJobStarter(nil)
	SetBackgroundBashEnabled(false)

	tool := handoffTool(t, func() (string, error) { return "inline", nil })
	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "do it"})

	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if res.Content != "inline" {
		t.Fatalf("expected the plain inline result, got %q", res.Content)
	}
}

// TestTypingEndsTheWaitImmediately is the reason the handoff has a second
// trigger. A person can type at any moment, and making them wait out the rest
// of a 60-second window for work they did not ask to wait for is the whole
// complaint. The children are untouched; only the waiting ends.
func TestTypingEndsTheWaitImmediately(t *testing.T) {
	handoffEnv(t, time.Minute)
	release := make(chan struct{})
	defer close(release)
	tool := handoffTool(t, func() (string, error) {
		<-release
		return "eventually", nil
	})

	// atomic because the tool polls this from its own goroutine.
	var typed atomic.Bool
	SetUserPending(typed.Load)
	defer SetUserPending(nil)

	// Nobody typing: the wait is still on after well past the poll interval.
	done := make(chan ToolResult, 1)
	go func() {
		res, handed := tool.runWithHandoff(context.Background(), []subagentTask{{Task: "slow"}}, true)
		if handed {
			done <- res
		}
	}()

	select {
	case res := <-done:
		t.Fatalf("handed over with nobody waiting: %s", res.Content)
	case <-time.After(3 * userPendingPoll):
	}

	typed.Store(true)
	select {
	case res := <-done:
		if !strings.Contains(res.Content, "running in the background") {
			t.Fatalf("unexpected handoff message: %s", res.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("typing did not end the wait")
	}
}

func TestUserPendingIsOffByDefault(t *testing.T) {
	SetUserPending(nil)
	if UserPending() {
		t.Fatal("with no frontend wired, nobody can be waiting")
	}
}

// --- Item 20: a blocking child that finishes inside the handoff window must
// run exactly once, through Run's full path (jobStarter configured, the
// handoff branch taken), not just runWithHandoff in isolation. Before the
// fix, Run fell through to runTasks after runWithHandoff had already run
// (and thrown away) the same tasks, so the runner was invoked twice, side
// effects happened twice, and two jobs were registered.

// waitTerminal blocks until the registry's one job reaches a terminal
// status, so the assertions below are not racing the goroutine that sets it
// (Start's wrapper closes job.done only after status is set — see
// jobs/registry.go).
// waitTerminal blocks until the given job is done, failed, or truncated.
// Registry.Wait's bool return is not that signal — it is false only for an
// unknown id, and true even on its own 5s timeout — so the terminal check
// has to be done here, against the returned snapshot's Status.
func waitTerminal(t *testing.T, reg *jobs.Registry, id string) *jobs.Job {
	t.Helper()
	job, ok := reg.Wait(context.Background(), id, 5*time.Second)
	if !ok {
		t.Fatalf("job %s is not registered", id)
	}
	if job.Status == jobs.StatusRunning || job.Status == jobs.StatusWaitingAnswer {
		t.Fatalf("job %s did not reach a terminal status within 5s: %+v", id, job)
	}
	return job
}

func TestBlockingSingleTaskRunsExactlyOnce(t *testing.T) {
	reg, _ := handoffEnv(t, time.Minute)
	var calls atomic.Int32
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(context.Context, string, string, SubagentOptions) (string, error) {
			calls.Add(1)
			return "the answer", nil
		},
	}}

	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "do it"})

	if !res.Success || res.Content != "the answer" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("runner invoked %d times, want exactly 1", n)
	}
	regJobs := reg.List()
	if len(regJobs) != 1 {
		t.Fatalf("expected exactly one registered job, got %d: %+v", len(regJobs), regJobs)
	}
	job := waitTerminal(t, reg, regJobs[0].ID)
	if job.Status != jobs.StatusDone {
		t.Fatalf("expected the job to finish done, got %q", job.Status)
	}
}

func TestBlockingBatchRunsEachTaskExactlyOnce(t *testing.T) {
	reg, _ := handoffEnv(t, time.Minute)
	var calls atomic.Int32
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(_ context.Context, task string, _ string, _ SubagentOptions) (string, error) {
			calls.Add(1)
			return "answer for " + task, nil
		},
	}}

	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{
		"tasks": []any{
			map[string]any{"task": "one"},
			map[string]any{"task": "two"},
		},
	})

	if !res.Success {
		t.Fatalf("unexpected failure: %s", res.Error)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("runner invoked %d times, want exactly 2 (one per task)", n)
	}
	var results []subagentResult
	if err := json.Unmarshal([]byte(res.Content), &results); err != nil {
		t.Fatalf("expected a JSON array of results: %v\ncontent: %s", err, res.Content)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if got := reg.List(); len(got) != 2 {
		t.Fatalf("expected exactly 2 registered jobs, got %d: %+v", len(got), got)
	}
}

func TestBlockingSingleTaskFailureRunsExactlyOnce(t *testing.T) {
	reg, _ := handoffEnv(t, time.Minute)
	var calls atomic.Int32
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(context.Context, string, string, SubagentOptions) (string, error) {
			calls.Add(1)
			return "", errors.New("agent failed")
		},
	}}

	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "do it"})

	if res.Success {
		t.Fatalf("expected failure, got success: %+v", res)
	}
	if !strings.Contains(res.Error, "agent failed") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("runner invoked %d times, want exactly 1", n)
	}
	if got := reg.List(); len(got) != 1 {
		t.Fatalf("expected exactly one registered job, got %d: %+v", len(got), got)
	}
}

func TestBlockingSingleTaskTruncatedRunsExactlyOnce(t *testing.T) {
	reg, _ := handoffEnv(t, time.Minute)
	var calls atomic.Int32
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(context.Context, string, string, SubagentOptions) (string, error) {
			calls.Add(1)
			return "partial answer", fmt.Errorf("hit cap: %w", ErrSubagentTruncated)
		},
	}}

	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "do it"})

	if !res.Success {
		t.Fatalf("expected success (truncated is still a completion), got error: %s", res.Error)
	}
	if !res.Truncated {
		t.Fatal("expected Truncated=true to reach the ToolResult")
	}
	if res.Content != "partial answer" {
		t.Fatalf("unexpected content: %q", res.Content)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("runner invoked %d times, want exactly 1", n)
	}
	if got := reg.List(); len(got) != 1 {
		t.Fatalf("expected exactly one registered job, got %d: %+v", len(got), got)
	}
}

// TestBlockingCallStillHandsOffWhenSlow guards the other side of the fix:
// making the all-finished path return inline must not stop a genuinely slow
// child from being handed to the background through Run's real entry point.
func TestBlockingCallStillHandsOffWhenSlow(t *testing.T) {
	// A long window: this test exercises the UserPending arm specifically,
	// so the timer must not be the one that fires first.
	reg, notifier := handoffEnv(t, time.Minute)
	release := make(chan struct{})
	var calls atomic.Int32
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(context.Context, string, string, SubagentOptions) (string, error) {
			calls.Add(1)
			<-release
			return "eventually", nil
		},
	}}

	// A person typing ends the wait immediately, through Run's real path.
	var typed atomic.Bool
	SetUserPending(typed.Load)
	defer SetUserPending(nil)

	done := make(chan ToolResult, 1)
	go func() {
		done <- tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "slow"})
	}()

	time.Sleep(20 * time.Millisecond)
	typed.Store(true)

	var res ToolResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("typing did not end the wait")
	}
	if !strings.Contains(res.Content, "running in the background") {
		t.Fatalf("expected a handoff message, got: %s", res.Content)
	}
	if got := reg.List(); len(got) != 1 {
		t.Fatalf("expected exactly one registered job, got %d: %+v", len(got), got)
	}
	close(release)
	job := waitTerminal(t, reg, reg.List()[0].ID)
	if job.Result != "eventually" || job.Status != jobs.StatusDone {
		t.Fatalf("handed-over job did not finish properly: %+v", job)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("runner invoked %d times, want exactly 1", n)
	}
	if n := len(notifier.all()); n != 1 {
		t.Fatalf("expected exactly one completion notice, got %d: %v", n, notifier.all())
	}
}

// TestBlockingCallStillHandsOffOnContextCancel: an interrupted parent turn
// (Esc) must still detach and hand the children off rather than the item-20
// fix accidentally making the all-finished branch swallow this case too.
func TestBlockingCallStillHandsOffOnContextCancel(t *testing.T) {
	reg, _ := handoffEnv(t, time.Minute)
	release := make(chan struct{})
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(context.Context, string, string, SubagentOptions) (string, error) {
			<-release
			return "eventually", nil
		},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		res    ToolResult
		handed bool
	}, 1)
	go func() {
		res, handed := tool.runWithHandoff(ctx, []subagentTask{{Task: "slow"}}, true)
		done <- struct {
			res    ToolResult
			handed bool
		}{res, handed}
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case out := <-done:
		if !out.handed {
			t.Fatalf("a cancelled parent context must still hand off: %+v", out.res)
		}
		if !strings.Contains(out.res.Content, "running in the background") {
			t.Fatalf("expected a handoff message, got: %s", out.res.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the context did not end the wait")
	}

	// Drain the handed-off child before returning, instead of leaving it
	// blocked on `release` for a deferred close to unblock on the way out.
	// The notifier is package-global (SetJobNotifier), and handoffEnv's
	// cleanup nils it only after this function returns — so a child that
	// finishes on its way out posts its completion notice into whichever
	// notifier the NEXT test has already installed. That is not theoretical:
	// it makes TestBlockingCallHandsOffAtTimerExpiry see two notices for one
	// job and fail, reproducibly, when the two run in sequence.
	close(release)
	if got := reg.List(); len(got) == 1 {
		waitTerminal(t, reg, got[0].ID)
	}
}

// TestBlockingCallHandsOffAtTimerExpiry exercises runWithHandoff's
// `case <-timer.C` arm directly, with nobody typing and no ctx cancellation
// to trigger the other two exits — the arm every other test in this file
// carefully avoids (they all set a window long enough that the timer never
// fires). Made possible by SubagentBackgroundAfterSec being a var: without
// that, this either waits out a real 60s or cannot be tested at all.
func TestBlockingCallHandsOffAtTimerExpiry(t *testing.T) {
	reg, notifier := handoffEnv(t, 20*time.Millisecond)
	release := make(chan struct{})
	var calls atomic.Int32
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(context.Context, string, string, SubagentOptions) (string, error) {
			calls.Add(1)
			<-release
			return "eventually", nil
		},
	}}

	res, handed := tool.runWithHandoff(context.Background(), []subagentTask{{Task: "slow"}}, true)
	if !handed {
		t.Fatalf("expected the timer to hand the child off: %+v", res)
	}
	if !strings.Contains(res.Content, "running in the background") {
		t.Fatalf("expected a handoff message, got: %s", res.Content)
	}
	if got := reg.List(); len(got) != 1 {
		t.Fatalf("expected exactly one registered job, got %d: %+v", len(got), got)
	}

	close(release)
	job := waitTerminal(t, reg, reg.List()[0].ID)
	if job.Result != "eventually" || job.Status != jobs.StatusDone {
		t.Fatalf("handed-over job did not finish properly: %+v", job)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("runner invoked %d times, want exactly 1", n)
	}
	if n := len(notifier.all()); n != 1 {
		t.Fatalf("expected exactly one completion notice, got %d: %v", n, notifier.all())
	}
}

// TestAskAnswerRoundTripWhenHandoffIsAvailable is review finding 2: nothing
// pinned AskUnroutableCtxKey to ONLY the no-handoff path. It must NOT be set
// for a blocking call in a mode where handoff is available (background bash
// enabled, as console/tui do) — a child asking a question there can
// genuinely be answered once the parent gets its turn back (via handoff, or
// simply because the child finishes fast). This is that round trip: a child
// blocks in "ask", the test plays "the parent or the person watching" and
// answers it directly against the real registry, and the child must
// actually receive that answer rather than being told upfront that nothing
// could ever answer it.
func TestAskAnswerRoundTripWhenHandoffIsAvailable(t *testing.T) {
	reg, _ := handoffEnv(t, time.Minute)
	SetJobAsker(reg)
	SetJobAnswerer(reg)
	t.Cleanup(func() {
		SetJobAsker(nil)
		SetJobAnswerer(nil)
	})

	var askRes ToolResult
	askDone := make(chan struct{})
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _, _ string, _ SubagentOptions) (string, error) {
			askRes = (&AskTool{}).Run(ctx, map[string]any{"question": "what color?"})
			close(askDone)
			return "done", nil
		},
	}}

	resultCh := make(chan ToolResult, 1)
	go func() {
		resultCh <- tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "ask something"})
	}()

	// Find the child's job id once it is blocked waiting for an answer.
	var jobID string
	deadline := time.Now().Add(2 * time.Second)
	for jobID == "" {
		if time.Now().After(deadline) {
			t.Fatal("child never reached waiting_answer")
		}
		for _, j := range reg.List() {
			if j.Status == jobs.StatusWaitingAnswer {
				jobID = j.ID
			}
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !reg.Answer(jobID, "blue", true) {
		t.Fatal("Answer failed against a job that should have been waiting")
	}

	select {
	case <-askDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ask never returned after being answered — AskUnroutableCtxKey must have leaked into a handoff-eligible call")
	}
	if !askRes.Success || askRes.Content != "blue" {
		t.Fatalf("expected ask to receive the real answer, got: %+v", askRes)
	}

	select {
	case res := <-resultCh:
		if !res.Success {
			t.Fatalf("subagent call failed: %s", res.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subagent call did not finish after the child was answered")
	}
}
