package tools

// A blocking subagent call runs its children as jobs and waits for all of
// them (runBlocking). It never hands a child to the background, so the call
// ends only when every child is done, or when the call's context ends. Then
// the children that still run are cancelled with ErrCancelledByParent.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/jobs"
)

// handoffEnv wires a job registry and notifier for a test. The cleanup DRAINS
// the registry before it tears the notifier down. A child that is still
// running when the test returns would post its completion notice later, into
// the next test's recordingNotifier.
//
// Waiting for terminal status is enough: spawn's job function posts the
// notice itself, before it returns (see subagent.go).
func handoffEnv(t *testing.T) (*jobs.Registry, *recordingNotifier) {
	t.Helper()
	reg := jobs.NewRegistry(nil)
	notifier := &recordingNotifier{}
	SetJobStarter(testJobStarter{reg})
	SetJobNotifier(notifier)
	SetBackgroundBashEnabled(true)

	t.Cleanup(func() {
		drainJobs(t, reg)
		SetJobStarter(nil)
		SetJobNotifier(nil)
		SetBackgroundBashEnabled(false)
	})
	return reg, notifier
}

// drainJobs waits for every job in reg to reach a terminal status. It reports
// through t.Errorf, not t.Fatalf, so the rest of the cleanup still runs. A test
// whose child waits on a release channel must close it in the test body.
func drainJobs(t *testing.T, reg *jobs.Registry) {
	t.Helper()
	for _, j := range reg.List() {
		if !jobLiveStatus(j.Status) {
			continue
		}
		job, ok := reg.Wait(context.Background(), j.ID, 5*time.Second)
		if !ok {
			t.Errorf("cleanup: job %s vanished from the registry", j.ID)
			continue
		}
		if jobLiveStatus(job.Status) {
			t.Errorf("cleanup: job %s was still %s 5s after the test ended — it can post its notice into the NEXT test's notifier", j.ID, job.Status)
		}
	}
}

func jobLiveStatus(s jobs.Status) bool {
	return s == jobs.StatusRunning || s == jobs.StatusWaitingAnswer
}

func handoffTool(t *testing.T, work func() (string, error)) *SubagentTool {
	t.Helper()
	return &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _ string, _ string, _ SubagentOptions) (string, error) {
			return work()
		},
	}}
}

// TestBlockingCallReturnsInlineWhenTheChildIsQuick: a child that finishes at
// once is returned as text.
func TestBlockingCallReturnsInlineWhenTheChildIsQuick(t *testing.T) {
	handoffEnv(t)
	tool := handoffTool(t, func() (string, error) { return "quick", nil })

	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "quick"})
	if !res.Success || res.Content != "quick" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// TestBlockingCallWaitsForASlowChild: a slow child is not handed over. The
// call returns only with its result, and the typing signal does not end it.
// Revert check: a handoff path would return the "running in the background"
// text before the child finishes.
func TestBlockingCallWaitsForASlowChild(t *testing.T) {
	handoffEnv(t)
	release := make(chan struct{})
	tool := handoffTool(t, func() (string, error) {
		<-release
		return "eventually", nil
	})

	var typed atomic.Bool
	typed.Store(true)
	SetUserPending(typed.Load)
	defer SetUserPending(nil)

	done := make(chan ToolResult, 1)
	go func() {
		done <- tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "slow"})
	}()

	select {
	case res := <-done:
		t.Fatalf("the call returned before the child finished: %+v", res)
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	select {
	case res := <-done:
		if !res.Success || res.Content != "eventually" {
			t.Fatalf("unexpected result: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not return after the child finished")
	}
}

// TestBlockingCallCancelStopsChildrenWithParentCause: when the call's context
// ends, the running child is cancelled with ErrCancelledByParent as its cause,
// and the call returns at once. Revert check: without the cause, isStoppedByUser
// would report the child as a kill_job stop.
func TestBlockingCallCancelStopsChildrenWithParentCause(t *testing.T) {
	handoffEnv(t)
	causeCh := make(chan error, 1)
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _ string, _ string, _ SubagentOptions) (string, error) {
			<-ctx.Done()
			causeCh <- context.Cause(ctx)
			return "", ctx.Err()
		},
	}}

	ctx, cancel := context.WithCancel(ctxWithParentModel("test/model"))
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	res := tool.Run(ctx, map[string]any{"task": "slow"})
	if res.Success {
		t.Fatalf("expected the cancelled call to fail, got %+v", res)
	}

	select {
	case cause := <-causeCh:
		if !errors.Is(cause, ErrCancelledByParent) {
			t.Fatalf("child cause = %v, want ErrCancelledByParent", cause)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the child was not cancelled")
	}
}

// TestBlockingCallIgnoresUserPending: typing does not end a blocking wait. The
// call stays on until its child is done.
func TestBlockingCallIgnoresUserPending(t *testing.T) {
	handoffEnv(t)
	release := make(chan struct{})
	defer close(release)
	tool := handoffTool(t, func() (string, error) {
		<-release
		return "eventually", nil
	})

	SetUserPending(func() bool { return true })
	defer SetUserPending(nil)

	done := make(chan ToolResult, 1)
	go func() {
		done <- tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "slow"})
	}()
	select {
	case res := <-done:
		t.Fatalf("typing ended the blocking wait: %+v", res)
	case <-time.After(300 * time.Millisecond):
	}
}

// effects happened twice, and two jobs were registered.

// waitTerminal blocks until the registry's one job reaches a terminal
// status, so the assertions below are not racing the goroutine that sets it
// (Start's wrapper closes job.done only after status is set — see
// jobs/registry.go).
// --- Item 20: a blocking child must run exactly once, through Run's full path
// (jobStarter configured). Before the fix, Run ran the tasks twice.

// waitTerminal blocks until the given job is done, failed, or truncated.
// Registry.Wait's bool return is not that signal — it is false only for an
// unknown id, and true even on its own 5s timeout — so the terminal check
// has to be done here, against the returned snapshot's Status.
// TestUserPendingIsOffByDefault: with no frontend wired, nobody can be waiting.
func TestUserPendingIsOffByDefault(t *testing.T) {
	SetUserPending(nil)
	if UserPending() {
		t.Fatal("with no frontend wired, nobody can be waiting")
	}
}

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
	reg, _ := handoffEnv(t)
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
	reg, _ := handoffEnv(t)
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
	reg, _ := handoffEnv(t)
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
	reg, _ := handoffEnv(t)
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
