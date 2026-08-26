package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCanceler records which id Cancel was called with and answers
// promptly, standing in for jobs.Registry through the SetJobCanceler hook.
type fakeCanceler struct {
	mu        sync.Mutex
	cancelled []string
	fail      bool // when true, Cancel returns false (job not running)
}

func (f *fakeCanceler) Cancel(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return false
	}
	f.cancelled = append(f.cancelled, id)
	return true
}

func (f *fakeCanceler) got(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cancelled {
		if c == id {
			return true
		}
	}
	return false
}

// fakeJob is one registered job for the inside-child parent walk.
type fakeJob struct {
	id       string
	parentID string
}

func (j fakeJob) ID() string       { return j.id }
func (j fakeJob) ParentID() string { return j.parentID }

type fakeLister struct{ jobs []JobKindSource }

func (f *fakeLister) ListJobs() []JobKindSource { return f.jobs }

func withKillWiring(t *testing.T, c JobCanceler, l JobLister) {
	// RESTORE what was there, do not blank it. Going through the setters is
	// right (the globals are mutex-guarded now, so assigning them directly
	// from a test is itself a race), but pairing that with an unconditional
	// SetXxx(nil) on cleanup would leave any outer wiring destroyed rather
	// than restored — the same shape of cross-test leak that made
	// TestInitCommon_UntrustedProject_... fail under -count>1. Snapshot via
	// the getters so this composes with any caller that already had hooks
	// wired.
	oldC, oldL := getJobCanceler(), getJobLister()
	t.Cleanup(func() {
		SetJobCanceler(oldC)
		SetJobLister(oldL)
	})
	SetJobCanceler(c)
	SetJobLister(l)
}

func childCtx(jobID string) context.Context {
	ctx := context.Background()
	ctx = context.WithValue(ctx, JobIDCtxKey{}, jobID)
	return context.WithValue(ctx, SubagentSinkCtxKey{}, true)
}

// TestKillJob_SubagentPathStopsChild: with the registry hooks wired, a
// kill_job call against a subagent kind reaches the canceler and reads as
// "stopped" success. Revert check: drop the jobCanceler dispatch from
// Run and this test fails (no success text, Cancel never called).
func TestKillJob_SubagentPathStopsChild(t *testing.T) {
	c := &fakeCanceler{}
	lister := &fakeLister{jobs: []JobKindSource{fakeJob{id: "job-child-9", parentID: ""}}}
	withKillWiring(t, c, lister)

	tool := &KillJobTool{}
	res := tool.Run(context.Background(), map[string]any{"job_id": "job-child-9"})
	if !res.Success {
		t.Fatalf("expected success stopping a subagent, got error %q", res.Error)
	}
	if !c.got("job-child-9") {
		t.Error("Registry.Cancel was not called with the target id — revert check: subagent dispatch missing")
	}
	if !strings.Contains(res.Content, "stopped") {
		t.Errorf(`expected success text to mention "stopped", got %q`, res.Content)
	}
}

// TestKillJob_BashPathKeepsOldMessage: a bash job stops via the pre-item-26
// background path (process-group) with its exact message, and must NOT reach
// the new registry canceler. Revert check: make Run prefer the registry
// cancel for bash and the message changes to the subagent wording / the fake
// canceler sees the id.
func TestKillJob_BashPathKeepsOldMessage(t *testing.T) {
	reg, _ := bgTestEnv(t)
	c := &fakeCanceler{}
	// Wire the canceler, but bash dispatch must bypass it entirely.
	withKillWiring(t, c, nil)

	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command":           "sleep 30; echo never",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected background command to start, got %q", res.Error)
	}
	id := jobIDFromResult(t, res.Content)

	killRes := (&KillJobTool{}).Run(context.Background(), map[string]any{"job_id": id})
	if !killRes.Success {
		t.Fatalf("kill_job on a bash job failed: %s", killRes.Error)
	}
	if !strings.Contains(killRes.Content, "SIGKILL to the process group") {
		t.Errorf(`bash kind must keep the process-group message, got %q`, killRes.Content)
	}
	if c.got(id) {
		t.Error("bash job must NOT go through the registry canceler — revert check: bash dispatch bypassed")
	}

	// Wait for the registry side so cleanup slots drain.
	waitForJob(t, reg, id, 5*time.Second)
}

// TestKillJob_InsideChildRefusesUnrelatedTarget: a child (sink ctx set)
// may not stop a job from a different subtree. Revert check: disable the
// safety gate and the refusal disappears; the test then expects failure and
// sees success.
func TestKillJob_InsideChildRefusesUnrelatedTarget(t *testing.T) {
	c := &fakeCanceler{}
	lister := &fakeLister{jobs: []JobKindSource{
		fakeJob{id: "job-a-1", parentID: ""},        // main's child
		fakeJob{id: "job-b-2", parentID: "job-b-3"}, // another subtree root b
		fakeJob{id: "job-b-3", parentID: "job-b-4"}, // middle
		fakeJob{id: "job-b-4", parentID: "job-b-5"}, // hash chain
		fakeJob{id: "job-b-5", parentID: ""},        // root b
	}}
	withKillWiring(t, c, lister)

	// caller is job-a-1; target is a member of subtree rooted at b.
	ctx := childCtx("job-a-1")
	tool := &KillJobTool{}
	res := tool.Run(ctx, map[string]any{"job_id": "job-b-2"})
	if res.Success {
		t.Fatal("expected refusal for an unrelated job")
	}
	if !strings.Contains(res.Error, "refused") || !strings.Contains(res.Error, "job-a-1") {
		t.Errorf(`refusal should name its boundary, got %q`, res.Error)
	}
	if c.got("job-b-2") {
		t.Error("Cancel must not fire on a refused target")
	}
}

// TestKillJob_InsideChildAllowsOwnSubtree: a child may stop a job inside
// its own subtree, deep or at any middle node. Revert check: make
// killAllowedInsideChild fail closed for everyone and the allowance half of
// this test errors.
func TestKillJob_InsideChildAllowsOwnSubtree(t *testing.T) {
	c := &fakeCanceler{}
	lister := &fakeLister{jobs: []JobKindSource{
		fakeJob{id: "job-b-1", parentID: "job-b-2"},
		fakeJob{id: "job-b-2", parentID: ""},
	}}
	withKillWiring(t, c, lister)

	targetID := "job-b-1"
	ctx := childCtx("job-b-2")
	tool := &KillJobTool{}
	res := tool.Run(ctx, map[string]any{"job_id": targetID})
	if !res.Success {
		t.Fatalf("expected allowance for a job in my own subtree, got error %q", res.Error)
	}
	if !c.got(targetID) {
		t.Error("Cancel was not called for the allowed target")
	}
}

// TestKillJob_InsideChildMayStopSelf: a child can always stop itself even
// when parentage is empty/unverifiable. Revert check: drop the
// targetID==callerJobID short-circuit and this fails against an empty
// caller.
func TestKillJob_InsideChildMayStopSelf(t *testing.T) {
	c := &fakeCanceler{}
	lister := &fakeLister{jobs: []JobKindSource{fakeJob{id: "job-self-1", parentID: ""}}}
	withKillWiring(t, c, lister)

	ctx := childCtx("job-self-1")
	tool := &KillJobTool{}
	res := tool.Run(ctx, map[string]any{"job_id": "job-self-1"})
	if !res.Success {
		t.Fatalf("expected a child to stop itself even with no lister, got %q", res.Error)
	}
	if !c.got("job-self-1") {
		t.Error("Cancel was not called for self-stop")
	}
}

// TestKillJob_MainAgentUnrestricted: the main agent (no sink key) may stop
// any job. Revert check: make the gate apply to the main path too and the
// subagent cable fails.
func TestKillJob_MainAgentUnrestricted(t *testing.T) {
	c := &fakeCanceler{}
	lister := &fakeLister{jobs: []JobKindSource{fakeJob{id: "job-anywhere-42", parentID: ""}}}
	withKillWiring(t, c, lister)

	ctx := context.Background() // no JobIDCtxKey, no SubagentSinkCtxKey
	tool := &KillJobTool{}
	res := tool.Run(ctx, map[string]any{"job_id": "job-anywhere-42"})
	if !res.Success {
		t.Fatalf("main agent should kill any running job, got error %q", res.Error)
	}
	if !c.got("job-anywhere-42") {
		t.Error("Cancel was not called on the main-agent path")
	}
}

// TestKillJob_ShortIDResolvesViaLister: a "#N" id resolves against the
// registry's own id (the single source of truth for known jobs) so the
// canceler receives the full id. Revert check: remove the short-id walk in
// Run and the canceler (fake grumbles on a #7 id) doesn't see the full id /
// the call errors as unknown.
func TestKillJob_ShortIDResolvesViaLister(t *testing.T) {
	c := &fakeCanceler{}
	lister := &fakeLister{jobs: []JobKindSource{
		fakeJob{id: "job-12345-7", parentID: ""},
	}}
	withKillWiring(t, c, lister)

	tool := &KillJobTool{}
	res := tool.Run(context.Background(), map[string]any{"job_id": "#7"})
	if !res.Success {
		t.Fatalf("expected short id to resolve, got error %q", res.Error)
	}
	if !c.got("job-12345-7") {
		t.Error("Cancel was called with the unresolved #7 instead of the full id")
	}
	if strings.Contains(res.Content, "#7") {
		t.Errorf(`message should name the resolved full id, got %q`, res.Content)
	}
}

// TestKillJob_NotRunningError: an id that is neither a background command
// nor a resolvable running job yields the pre-item-26 "not a running job"
// failure. Revert check: remove killJobNotRunningError and the call fails
// with bare "false" (no guidance).
func TestKillJob_NotRunningError(t *testing.T) {
	c := &fakeCanceler{fail: true}
	withKillWiring(t, c, nil)

	tool := &KillJobTool{}
	res := tool.Run(context.Background(), map[string]any{"job_id": "job-dead-0"})
	if res.Success {
		t.Fatal("expected failure for a non-running id")
	}
	if !strings.Contains(res.Error, "not a running job") || !strings.Contains(res.Error, "check it with wait") {
		t.Errorf(`non-running error should guide with wait, got %q`, res.Error)
	}
}
