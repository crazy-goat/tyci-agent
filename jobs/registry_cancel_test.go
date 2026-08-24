package jobs

// Tests for item 26's kill switch: Registry.Start deriving a cancel,
// Cancel(id) refusing terminal jobs, the subtree cascade (children BEFORE
// parent, any depth), cycle safety, short-id resolution, and the
// "stopped by user" error rewrite. Each test's comment names how to verify
// it fails when its fix is reverted.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockerFn returns a job fn that records its start on the channel and then
// blocks until its own ctx is done, returning ctx.Err(). The returned func
// also counts how many fns have observed cancellation so cascade tests can
// assert every subtree member actually died.
type blockerSet struct {
	mu       sync.Mutex
	started  map[string]bool
	finished []string // job ids in the order their fn returned

	startedCh chan string
}

func newBlockerSet() *blockerSet {
	return &blockerSet{
		started:   make(map[string]bool),
		startedCh: make(chan string, 64),
	}
}

func (b *blockerSet) fn(ctx context.Context, jobID string) (string, bool, error) {
	b.mu.Lock()
	b.started[jobID] = true
	b.mu.Unlock()
	b.startedCh <- jobID
	<-ctx.Done()
	b.mu.Lock()
	b.finished = append(b.finished, jobID)
	b.mu.Unlock()
	return "partial work of " + jobID, false, ctx.Err()
}

func (b *blockerSet) finishOrder() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.finished))
	copy(out, b.finished)
	return out
}

// waitStarted blocks until every id has been seen started (with a timeout),
// so a Cancel issued before a goroutine gets scheduled can't flake.
func (b *blockerSet) waitStarted(t *testing.T, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	seen := make(map[string]bool)
	for len(seen) < want {
		select {
		case id := <-b.startedCh:
			seen[id] = true
		case <-deadline:
			t.Fatalf("only %d/%d job fns started", len(seen), want)
		}
	}
}

// TestCancelStopsRunningJob: Cancel must reach a job whose fn blocks on
// ctx.Done(). Revert check: drop WithCancel from Start (pass ctx straight
// through) and this hangs until the test timeout — nothing outside can stop
// the job any more.
func TestCancelStopsRunningJob(t *testing.T) {
	r := NewRegistry()
	blockers := newBlockerSet()

	job := r.Start(context.Background(), "blocker", KindSubagent, "", blockers.fn)
	blockers.waitStarted(t, 1)

	if !r.Cancel(job.ID) {
		t.Fatal("Cancel returned false for a running job")
	}
	snap, ok := r.Wait(context.Background(), job.ID, 2*time.Second)
	if !ok || snap.Status != StatusFailed {
		t.Fatalf("expected failed terminal status after Cancel, got ok=%v status=%v", ok, snap.Status)
	}
	if snap.Err != ErrStoppedByUser.Error() {
		t.Fatalf("expected stopped error text %q, got %q", ErrStoppedByUser.Error(), snap.Err)
	}
}

func TestCancelAllStopsLiveJobsAndReturnsKnownIDs(t *testing.T) {
	r := NewRegistry()
	started := make(chan struct{})
	job := r.Start(context.Background(), "old", KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		close(started)
		<-ctx.Done()
		return "", false, ctx.Err()
	})
	<-started
	ids := r.CancelAll()
	if len(ids) != 1 || ids[0] != job.ID {
		t.Fatalf("CancelAll IDs = %v, want [%s]", ids, job.ID)
	}
	snap, ok := r.Wait(context.Background(), job.ID, time.Second)
	if !ok || snap.Status != StatusFailed {
		t.Fatalf("job not terminal after CancelAll: ok=%v snap=%+v", ok, snap)
	}
	if got := r.CancelAll(); len(got) != 1 || got[0] != job.ID {
		t.Fatalf("second CancelAll IDs = %v, want the known old job", got)
	}
}

// TestCancelRefusesTerminalAndUnknown: false means "not running", never a
// fake success. Revert check: remove the status guard from Cancel and the
// already-terminal case flips this test's second half to true.
func TestCancelRefusesTerminalAndUnknown(t *testing.T) {
	r := NewRegistry()

	if r.Cancel("job-nope") {
		t.Fatal("Cancel claimed success on an unknown id")
	}

	release := make(chan struct{})
	job := r.Start(context.Background(), "quick", KindSubagent, "", func(ctx context.Context, jobID string) (string, bool, error) {
		<-release
		return "done", false, nil
	})
	close(release)
	snap, ok := r.Wait(context.Background(), job.ID, 2*time.Second)
	if !ok || snap.Status != StatusDone {
		t.Fatalf("setup: job did not complete cleanly: %+v", snap)
	}
	if r.Cancel(job.ID) {
		t.Fatal("Cancel claimed success on an already-finished job — revert check: Cancel no longer refuses terminal jobs")
	}
}

// TestCancelCascadeChildrenBeforeParentAnyDepth: killing a subagent stops
// everything it spawned. The production guarantee is the ORDER OF CANCEL
// CALLS (descendants first), not the order in which independent job
// goroutines happen to return — once three separate contexts are cancelled,
// goroutine wake-up order belongs to the scheduler and no implementation
// should be judged by it. So this test pins the kill order white-box
// (subtreeOrderLocked) and separately asserts every subtree member actually
// died. Revert checks: (a) drop WithCancel from Start → the terminal-wait
// loop fatals with jobs still running; (b) lose the subtree walk → only the
// root dies; (c) flip the walk to parent-first → the order assertion fails.
func TestCancelCascadeChildrenBeforeParentAnyDepth(t *testing.T) {
	r := NewRegistry()
	blockers := newBlockerSet()

	grandparent := r.Start(context.Background(), "gp", KindSubagent, "", blockers.fn).ID
	parent := r.Start(context.Background(), "p", KindBash, grandparent, blockers.fn).ID
	child := r.Start(context.Background(), "c", KindSubagent, parent, blockers.fn).ID
	blockers.waitStarted(t, 3)

	r.mu.Lock()
	target, ok := r.jobs[grandparent]
	if !ok {
		r.mu.Unlock()
		t.Fatalf("target %s vanished from the registry", grandparent)
	}
	killOrder := r.subtreeOrderLocked(target)
	r.mu.Unlock()
	if len(killOrder) != 3 ||
		killOrder[0].ID != child || killOrder[1].ID != parent || killOrder[2].ID != grandparent {
		var got []string
		for _, j := range killOrder {
			got = append(got, j.ID)
		}
		t.Fatalf("expected deepest-first kill order [child parent root], got %v — revert check: subtree walk lost or parent-first", got)
	}

	// The cascade covers descendants only, so cancelling the subtree ROOT
	// kills all three; a mid-chain kill leaving its own ancestors alone is
	// pinned separately in TestCancelLeavesAncestorsRunning.
	if !r.Cancel(grandparent) {
		t.Fatal("Cancel returned false for the subtree root")
	}
	for _, id := range []string{grandparent, parent, child} {
		waitTerminal(t, r, id, 2*time.Second)
	}
}

// TestCancelLeavesAncestorsRunning: the cascade goes DOWN only — stopping a
// mid-chain job stops its descendants and never the job (or chain) it
// spawned from. Revert check: make the subtree walk follow ParentID upward
// and the final assertion fails with the grandparent gone terminal.
func TestCancelLeavesAncestorsRunning(t *testing.T) {
	r := NewRegistry()
	blockers := newBlockerSet()

	grandparent := r.Start(context.Background(), "gp", KindSubagent, "", blockers.fn).ID
	parent := r.Start(context.Background(), "p", KindBash, grandparent, blockers.fn).ID
	blockers.waitStarted(t, 2)

	if !r.Cancel(parent) {
		t.Fatal("Cancel returned false for the middle node")
	}
	waitTerminal(t, r, parent, 2*time.Second)
	// The ancestor must stay running long enough to prove the cascade is
	// downward-only (200ms of polling, race-free snapshots).
	stillRunning(t, r, grandparent, 200*time.Millisecond)
}

// TestCancelCycleGuardTerminates: fabricated ParentID links (a→b→a) must
// terminate the walk, not hang. Revert check: drop the visited set from the
// DFS and this test deadlocks until the go test timeout kills it.
func TestCancelCycleGuardTerminates(t *testing.T) {
	r := NewRegistry()
	blockers := newBlockerSet()

	a := r.Start(context.Background(), "a", KindOther, "", blockers.fn).ID
	bJob := r.Start(context.Background(), "b", KindOther, a, blockers.fn).ID
	blockers.waitStarted(t, 2)

	// Forge the cycle directly through the live jobs (test-only access via
	// Get; the registry never produces cycles itself).
	aJob, _ := r.Get(a)
	aJob.ParentID = bJob

	done := make(chan bool, 1)
	go func() { done <- r.Cancel(a) }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("Cancel returned false despite a running target")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel hung on cyclic ParentID links — revert check: visited set removed")
	}
}

// TestCancelAcceptsShortIDs: "#N"/"N" resolve like the jobs panel prints
// them. Revert check: remove the Resolve call from Cancel (exact-match only)
// and both forms fail here.
func TestCancelAcceptsShortIDs(t *testing.T) {
	r := NewRegistry()
	blockers := newBlockerSet()

	j1 := r.Start(context.Background(), "one", KindSubagent, "", blockers.fn)
	j2 := r.Start(context.Background(), "two", KindSubagent, "", blockers.fn)
	blockers.waitStarted(t, 2)

	short2 := ShortID(j2.ID)
	if !r.Cancel("#" + short2) {
		t.Fatalf("Cancel refused short form #%s", short2)
	}
	if !r.Cancel(ShortID(j1.ID)) {
		t.Fatalf("Cancel refused bare short form %s", ShortID(j1.ID))
	}
	for _, j := range []*Job{j1, j2} {
		snap, ok := r.Wait(context.Background(), j.ID, 2*time.Second)
		if !ok || snap.Status != StatusFailed {
			t.Fatalf("job %s not stopped via short id: ok=%v snap=%+v", j.ID, ok, snap)
		}
	}
}

// TestStoppedErrorRewrittenOnlyForUserStop: a cancelled flag set by Cancel
// rewrites the error; a job whose PARENT context dies keeps the honest
// underlying cause. Revert check: remove the errors.Is+cancelled branch in
// Start's completion path and the first assertion sees raw "context
// canceled"; set the flag unconditionally and the second half fails.
func TestStoppedErrorRewrittenOnlyForUserStop(t *testing.T) {
	r := NewRegistry()
	blockers := newBlockerSet()

	killed := r.Start(context.Background(), "killed", KindSubagent, "", blockers.fn)
	parentCtx, cancelParent := context.WithCancel(context.Background())
	orphaned := r.Start(parentCtx, "orphaned", KindSubagent, "", blockers.fn)
	blockers.waitStarted(t, 2)

	r.Cancel(killed.ID)
	cancelParent()

	kSnap, _ := r.Wait(context.Background(), killed.ID, 2*time.Second)
	oSnap, _ := r.Wait(context.Background(), orphaned.ID, 2*time.Second)
	if kSnap.Err != ErrStoppedByUser.Error() {
		t.Fatalf("kill_job'd job should read %q, got %q", ErrStoppedByUser.Error(), kSnap.Err)
	}
	if oSnap.Err == ErrStoppedByUser.Error() || !strings.Contains(oSnap.Err, "context canceled") {
		t.Fatalf("orphaned job should keep the underlying cause, got %q", oSnap.Err)
	}
}

// waitTerminal polls until id leaves StatusRunning (or times out) — Cancel
// signals all subtree contexts and returns before the job goroutines get
// scheduled again, so a single Wait+Get can observe a still-Running
// snapshot that a few milliseconds later reads terminal.
func waitTerminal(t *testing.T, r *Registry, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		// Poll via Wait (short timeout), never via Get().Status: Get returns
		// the live *Job, and reading its fields outside r.mu races with the
		// completion goroutine's writes. Wait snapshots under the lock.
		snap, ok := r.Wait(context.Background(), id, 10*time.Millisecond)
		if ok && snap != nil && snap.Status != StatusRunning {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("job %s not terminal after %s", id, timeout)
		default:
		}
	}
}

// stillRunning polls until the deadline, asserting the job NEVER leaves
// StatusRunning — same snapshot-under-lock discipline as waitTerminal.
// Returns the final (running) snapshot for callers that want its fields.
func stillRunning(t *testing.T, r *Registry, id string, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for {
		snap, ok := r.Wait(context.Background(), id, 10*time.Millisecond)
		if !ok || snap == nil || snap.Status != StatusRunning {
			t.Fatalf("job %s left running state unexpectedly: ok=%v snap=%+v", id, ok, snap)
		}
		if time.Now().After(deadline) {
			return
		}
	}
}
