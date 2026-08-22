package tools

// A blocking subagent call hands its children to the background after
// SubagentBackgroundAfterSec, the same way bash does at 30s. Without it a slow
// child holds the whole turn open, and the person at the keyboard cannot type
// anything until it returns.

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// handoffEnv wires a job registry and notifier, and shrinks the handoff window
// so a test does not have to wait a minute for it.
func handoffEnv(t *testing.T, after time.Duration) (*jobs.Registry, *recordingNotifier) {
	t.Helper()
	reg := jobs.NewRegistry()
	notifier := &recordingNotifier{}
	SetJobStarter(testJobStarter{reg})
	SetJobNotifier(notifier)
	SetBackgroundBashEnabled(true) // the per-mode flag both handoffs share
	t.Cleanup(func() {
		SetJobStarter(nil)
		SetJobNotifier(nil)
		SetBackgroundBashEnabled(false)
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

	res, handed := tool.runWithHandoff(context.Background(), []subagentTask{{Task: "quick"}})
	if handed {
		t.Fatalf("a quick child should not be handed over: %+v", res)
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
	handoffEnv(t, 0)
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
		res, handed := tool.runWithHandoff(context.Background(), []subagentTask{{Task: "slow"}})
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
