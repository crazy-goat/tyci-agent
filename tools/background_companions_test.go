package tools

// Tests for #435: a subagent that may run bash also gets wait and kill_job,
// so it can collect or stop the background commands it started. Inside a
// subagent, wait follows the same subtree rule as kill_job.

import (
	"context"
	"sync"
	"testing"
	"time"
)

// countingWaiter is a JobWaiter that records every id it is asked about and
// reports each job as finished, so a wait returns at once.
type countingWaiter struct {
	mu  sync.Mutex
	ids []string
}

func (w *countingWaiter) Wait(_ context.Context, id string, _ time.Duration) (JobStatus, bool) {
	w.mu.Lock()
	w.ids = append(w.ids, id)
	w.mu.Unlock()
	return JobStatus{ID: id, Done: true, Success: true, Content: "done"}, true
}

func (w *countingWaiter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.ids)
}

// TestWaitInsideSubagentRefusesSiblingJob: a child may not wait on a job
// that is not in its own subtree, even when the id is the "#N" short form.
// Revert check: drop the inOwnSubtree call in WaitTool.Run and the waiter
// is called.
func TestWaitInsideSubagentRefusesSiblingJob(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		fakeJob{id: "job-me", parentID: "job-root"},
		fakeJob{id: "job-sib", parentID: "job-root"},
	}}
	withKillWiring(t, nil, lister)
	waiter := &countingWaiter{}
	tool := &WaitTool{Waiter: waiter}

	for _, id := range []string{"job-sib", "#sib"} {
		res := tool.Run(childCtx("job-me"), map[string]any{"job_id": id, "seconds": 60})
		if res.Success {
			t.Fatalf("expected a refusal for %q, got success %q", id, res.Content)
		}
	}
	if waiter.count() != 0 {
		t.Fatalf("refused wait must not reach the waiter, got calls %v", waiter.ids)
	}
}

// TestWaitInsideSubagentAllowsOwnSubJob: a child may wait on a job it
// started, in full or "#N" short form.
func TestWaitInsideSubagentAllowsOwnSubJob(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		fakeJob{id: "job-me", parentID: "job-root"},
		fakeJob{id: "job-sub", parentID: "job-me"},
	}}
	withKillWiring(t, nil, lister)
	waiter := &countingWaiter{}
	tool := &WaitTool{Waiter: waiter}

	for _, id := range []string{"job-sub", "#sub"} {
		res := tool.Run(childCtx("job-me"), map[string]any{"job_id": id, "seconds": 60})
		if !res.Success {
			t.Fatalf("expected wait on own job %q to succeed, got error %q", id, res.Error)
		}
	}
	if waiter.count() != 2 {
		t.Fatalf("expected two waiter calls, got %v", waiter.ids)
	}
}

// TestWaitInsideSubagentUnknownIDIsNotRefused: an id the registry does not
// know gets the unknown-id error, not a subtree refusal, and never reaches
// the waiter. With no lister wired the child fails closed the same way.
func TestWaitInsideSubagentUnknownIDIsNotRefused(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		fakeJob{id: "job-me", parentID: "job-root"},
	}}
	for name, l := range map[string]JobLister{"with lister": lister, "without lister": nil} {
		t.Run(name, func(t *testing.T) {
			withKillWiring(t, nil, l)
			waiter := &countingWaiter{}
			tool := &WaitTool{Waiter: waiter}

			res := tool.Run(childCtx("job-me"), map[string]any{"job_id": "job-typo", "seconds": 60})
			if res.Success {
				t.Fatalf("expected a failure for an unknown id, got success %q", res.Content)
			}
			if res.Error != unknownJobIDError {
				t.Fatalf("expected the unknown-id error, got %q", res.Error)
			}
			if waiter.count() != 0 {
				t.Fatalf("unknown id must not reach the waiter, got calls %v", waiter.ids)
			}
		})
	}
}

// TestWaitMainAgentMayWaitAnyJob: the main agent (no subagent sink) keeps
// the old behaviour and may wait on any registered job.
func TestWaitMainAgentMayWaitAnyJob(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		fakeJob{id: "job-sib", parentID: "job-root"},
	}}
	withKillWiring(t, nil, lister)
	tool := &WaitTool{Waiter: &countingWaiter{}}

	res := tool.Run(context.Background(), map[string]any{"job_id": "job-sib", "seconds": 60})
	if !res.Success {
		t.Fatalf("expected the main agent to wait on any job, got error %q", res.Error)
	}
}

// TestWaitTouchesCallerActivity: a wait returns as activity for the caller's
// own job, so the watchdog does not read a blocked child as idle. Revert
// check: drop the defer touchJobActivity line in WaitTool.Run and the
// toucher records nothing.
func TestWaitTouchesCallerActivity(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		fakeJob{id: "job-me", parentID: "job-root"},
		fakeJob{id: "job-sub", parentID: "job-me"},
	}}
	withKillWiring(t, nil, lister)
	rec := &recordingToucher{}
	SetJobActivityToucher(rec)
	t.Cleanup(func() { SetJobActivityToucher(nil) })
	tool := &WaitTool{Waiter: &countingWaiter{}}

	res := tool.Run(childCtx("job-me"), map[string]any{"job_id": "job-sub", "seconds": 60})
	if !res.Success {
		t.Fatalf("expected wait on own job to succeed, got error %q", res.Error)
	}
	if rec.count() == 0 || rec.seen[len(rec.seen)-1] != "job-me" {
		t.Fatalf("expected the caller job to be touched after the wait, got %v", rec.seen)
	}
}

// TestInOwnSubtree_NestedCaller: a middle node in the tree may act on its
// own descendants, but not on its ancestors or on a sibling branch.
// Revert check: compare the walk's result to the chain's root instead of
// the caller and the middle-node case fails.
func TestInOwnSubtree_NestedCaller(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		fakeJob{id: "job-top", parentID: ""},
		fakeJob{id: "job-m", parentID: "job-top"},
		fakeJob{id: "job-x", parentID: "job-m"},
		fakeJob{id: "job-sib", parentID: "job-top"},
	}}
	ctx := childCtx("job-m")

	if !inOwnSubtree(ctx, "job-m", "job-x", lister) {
		t.Error("expected a middle node to act on its own descendant")
	}
	if inOwnSubtree(ctx, "job-m", "job-top", lister) {
		t.Error("expected a middle node to be refused on its ancestor")
	}
	if inOwnSubtree(ctx, "job-m", "job-sib", lister) {
		t.Error("expected a middle node to be refused on a sibling branch")
	}
}

// TestAllowOnlySubagentBashGrantsBackgroundCompanions: bash brings wait and
// kill_job with it, and nothing else is added.
func TestAllowOnlySubagentBashGrantsBackgroundCompanions(t *testing.T) {
	gate := AllowOnlySubagent([]string{"bash"})
	for _, name := range []string{"bash", "wait", "kill_job"} {
		if err := gate(name); err != nil {
			t.Fatalf("expected %s to pass the gate with bash granted: %v", name, err)
		}
	}
	if err := gate("read"); err == nil {
		t.Fatal("expected read to stay refused when only bash is granted")
	}
}

// TestAllowOnlySubagentWithoutBashGrantsNoCompanions: wait and kill_job are
// granted only together with bash.
func TestAllowOnlySubagentWithoutBashGrantsNoCompanions(t *testing.T) {
	gate := AllowOnlySubagent([]string{"read"})
	for _, name := range []string{"wait", "kill_job"} {
		if err := gate(name); err == nil {
			t.Fatalf("expected %s to be refused without bash", name)
		}
	}
}

// TestSubagentSchemaListsBackgroundCompanions: the schema the model sees
// matches the gate: wait and kill_job appear with bash, not without it.
func TestSubagentSchemaListsBackgroundCompanions(t *testing.T) {
	withBash := schemaToolNames(t, GetSubagentToolsSchemaJSONFor([]string{"bash"}))
	for _, name := range []string{"bash", "wait", "kill_job"} {
		if !withBash[name] {
			t.Fatalf("expected %s in the schema with bash granted", name)
		}
	}

	withoutBash := schemaToolNames(t, GetSubagentToolsSchemaJSONFor([]string{"read"}))
	for _, name := range []string{"wait", "kill_job"} {
		if withoutBash[name] {
			t.Fatalf("expected %s out of the schema without bash", name)
		}
	}
}
