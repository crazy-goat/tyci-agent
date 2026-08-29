package tools

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
)

// TestScoutToolProfile_AllowsProfileDeniesRest pins scout's own runtime
// tool profile: item 21 asked for read-only bash, but this codebase has no
// bash command-parsing/allowlisting mechanism anywhere to build "read-only"
// out of, so bash is omitted entirely — the profile is find/read/help,
// nothing else. Critically this must go through ScoutGate, NOT
// AllowOnlySubagent(scoutToolProfile): the latter unconditionally folds
// alwaysAllowedTools ("lua") back in, and lua can dispatch tool("bash", ...)
// internally (a script's tool() call reaches RunTool directly — see
// toolgate.go's package doc comment), which would silently hand a scout a
// live shell again through the one path this profile is supposed to close.
// See scoutToolProfile's doc comment for the fuller history.
func TestScoutToolProfile_AllowsProfileDeniesRest(t *testing.T) {
	gate := ScoutGate()
	if gate == nil {
		t.Fatal("expected a non-nil gate for scout's tool profile")
	}

	for _, name := range []string{"find", "read", "help"} {
		if err := gate(name); err != nil {
			t.Errorf("expected %q allowed in a scout's tool profile, got: %v", name, err)
		}
	}
	for _, name := range []string{"bash", "lua", "write", "subagent", "agents", "scout", "todo"} {
		if err := gate(name); err == nil {
			t.Errorf("expected %q denied in a scout's tool profile, but it was allowed", name)
		}
	}
}

// scoutCapturingRunner records every ctx/opts pair RunTask was called with,
// so a test can inspect what ScoutTool.Run actually handed to
// runSingleTask/main.go's agentRunner.
type scoutCapturingRunner struct {
	mu      sync.Mutex
	ctxs    []context.Context
	opts    []SubagentOptions
	delay   time.Duration
	content string
}

func (r *scoutCapturingRunner) RunTask(ctx context.Context, task string, model string, opts SubagentOptions) (string, error) {
	r.mu.Lock()
	r.ctxs = append(r.ctxs, ctx)
	r.opts = append(r.opts, opts)
	delay := r.delay
	content := r.content
	r.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
	}
	if content == "" {
		content = "scouted"
	}
	return content, nil
}

func (r *scoutCapturingRunner) RunTaskWithSystem(ctx context.Context, task, model, system string, opts SubagentOptions) (string, error) {
	return r.RunTask(ctx, task, model, opts)
}

func scoutTestCtx() context.Context {
	return connector.WithModelClient(context.Background(), fakeModelClient("scout-model"))
}

// TestScoutTool_PassesToolsOverrideAndMaxIterationsCap pins the two knobs
// that make a scout "crippled" rather than a plain subagent: a fixed tool
// whitelist and MaxIterations 15, both handed to runSingleTask through
// subagentTask's private toolsOverride/maxIterationsCap fields regardless
// of the task text.
func TestScoutTool_PassesToolsOverrideAndMaxIterationsCap(t *testing.T) {
	r := &scoutCapturingRunner{}
	st := &ScoutTool{Runner: r}

	res := st.Run(scoutTestCtx(), map[string]any{"task": "look around"})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if len(r.opts) != 1 {
		t.Fatalf("expected exactly one RunTask call, got %d", len(r.opts))
	}
	if !reflect.DeepEqual(r.opts[0].Tools, scoutToolProfile) {
		t.Errorf("expected opts.Tools == scoutToolProfile, got %v", r.opts[0].Tools)
	}
	if r.opts[0].MaxIterationsCap != scoutMaxIterations {
		t.Errorf("expected MaxIterationsCap == %d, got %d", scoutMaxIterations, r.opts[0].MaxIterationsCap)
	}
}

// TestScoutTool_RejectsMissingTask is the input-schema half of "exactly one
// field, task (string)": a call missing it must fail validation before ever
// reaching the runner.
func TestScoutTool_RejectsMissingTask(t *testing.T) {
	r := &scoutCapturingRunner{}
	st := &ScoutTool{Runner: r}

	res := st.Run(scoutTestCtx(), map[string]any{})
	if res.Success {
		t.Fatal("expected a missing task to be rejected")
	}
	if len(r.opts) != 0 {
		t.Error("expected the runner never to be called for an invalid input")
	}
}

// TestScoutTool_StripsCallerJobID is the regression test for the
// resumable-stash/mailbox/heartbeat corruption a scout would otherwise
// cause: if it ran with its caller's own job id still in context,
// main.go's agentRunner.run would treat the scout's own transcript as if
// it belonged to that job. ScoutTool.Run must clear it before recursing.
func TestScoutTool_StripsCallerJobID(t *testing.T) {
	r := &scoutCapturingRunner{}
	st := &ScoutTool{Runner: r}

	ctx := context.WithValue(scoutTestCtx(), JobIDCtxKey{}, "caller-job-1")
	res := st.Run(ctx, map[string]any{"task": "look around"})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if len(r.ctxs) != 1 {
		t.Fatalf("expected exactly one RunTask call, got %d", len(r.ctxs))
	}
	if jobID, _ := r.ctxs[0].Value(JobIDCtxKey{}).(string); jobID != "" {
		t.Errorf("expected the caller's job id to be stripped, got %q", jobID)
	}
}

// --- deadline inheritance ----------------------------------------------

func TestScoutDeadline_InheritsShorterCallerDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	child, childCancel := scoutDeadline(parent)
	defer childCancel()

	dl, ok := child.Deadline()
	if !ok {
		t.Fatal("expected the scout context to carry a deadline")
	}
	remaining := time.Until(dl)
	if remaining <= 0 || remaining > 5*time.Second+500*time.Millisecond {
		t.Errorf("expected the scout deadline to inherit the caller's ~5s remaining budget, got %v", remaining)
	}
}

func TestScoutDeadline_CapsAt180sWhenCallerDeadlineLonger(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	child, childCancel := scoutDeadline(parent)
	defer childCancel()

	dl, ok := child.Deadline()
	if !ok {
		t.Fatal("expected the scout context to carry a deadline")
	}
	remaining := time.Until(dl)
	if remaining > scoutMaxDeadline+2*time.Second || remaining < scoutMaxDeadline-2*time.Second {
		t.Errorf("expected the scout deadline capped at ~%v (not the caller's much longer budget), got %v", scoutMaxDeadline, remaining)
	}
}

func TestScoutDeadline_NoCallerDeadlineFallsBackTo180s(t *testing.T) {
	child, childCancel := scoutDeadline(context.Background())
	defer childCancel()

	dl, ok := child.Deadline()
	if !ok {
		t.Fatal("expected a fallback deadline when the caller ctx carries none")
	}
	remaining := time.Until(dl)
	if remaining > scoutMaxDeadline+2*time.Second || remaining < scoutMaxDeadline-2*time.Second {
		t.Errorf("expected the fallback deadline at ~%v, got %v", scoutMaxDeadline, remaining)
	}
}

// --- concurrency semaphore ----------------------------------------------

// TestAcquireScoutSlot_PerCallerCap pins the 2-per-caller limit: a third
// concurrent scout from the SAME caller is rejected, but a different
// caller is unaffected by it.
func TestAcquireScoutSlot_PerCallerCap(t *testing.T) {
	defer SnapshotScoutConcurrencyForTesting()()

	release1, ok1 := acquireScoutSlot("caller-A")
	if !ok1 {
		t.Fatal("expected the first slot for caller-A to succeed")
	}
	defer release1()

	release2, ok2 := acquireScoutSlot("caller-A")
	if !ok2 {
		t.Fatal("expected the second concurrent slot for caller-A to succeed (cap is 2)")
	}
	defer release2()

	if _, ok3 := acquireScoutSlot("caller-A"); ok3 {
		t.Fatal("expected a third concurrent slot for caller-A to be rejected (per-caller cap is 2)")
	}

	release4, ok4 := acquireScoutSlot("caller-B")
	if !ok4 {
		t.Fatal("expected caller-B's own slot to be unaffected by caller-A's exhausted cap")
	}
	defer release4()
}

// TestAcquireScoutSlot_ProcessWideCap pins the 6-process-wide limit: even
// spread across entirely distinct callers (so the per-caller cap alone
// would never trip), a 7th concurrent scout is rejected.
func TestAcquireScoutSlot_ProcessWideCap(t *testing.T) {
	defer SnapshotScoutConcurrencyForTesting()()

	var releases []func()
	defer func() {
		for _, rel := range releases {
			rel()
		}
	}()

	for i := 0; i < maxScoutsProcessWide; i++ {
		rel, ok := acquireScoutSlot(fmt.Sprintf("caller-%d", i))
		if !ok {
			t.Fatalf("expected slot %d/%d to succeed", i+1, maxScoutsProcessWide)
		}
		releases = append(releases, rel)
	}

	if _, ok := acquireScoutSlot("one-more-caller"); ok {
		t.Fatalf("expected the %dth concurrent scout (a fresh caller, so the per-caller cap does not apply) to be rejected by the process-wide cap", maxScoutsProcessWide+1)
	}
}

// TestAcquireScoutSlot_ReleaseFreesSlot pins that release() actually frees
// the slot for reuse — a leak here would make every cap one-way.
func TestAcquireScoutSlot_ReleaseFreesSlot(t *testing.T) {
	defer SnapshotScoutConcurrencyForTesting()()

	release, ok := acquireScoutSlot("caller-X")
	if !ok {
		t.Fatal("expected the first slot to succeed")
	}
	release()

	release2, ok2 := acquireScoutSlot("caller-X")
	if !ok2 {
		t.Fatal("expected the slot to be reusable after release()")
	}
	release2()
}

// TestScoutTool_ConcurrencyCapRejectsExtraCallFromSameCaller drives the cap
// through ScoutTool.Run itself (not acquireScoutSlot directly): two scouts
// from the same caller identity held open concurrently, a third from that
// same caller must be refused without ever reaching the runner.
func TestScoutTool_ConcurrencyCapRejectsExtraCallFromSameCaller(t *testing.T) {
	defer SnapshotScoutConcurrencyForTesting()()

	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(maxScoutsPerCaller)

	r := &blockingScoutRunner{release: release, started: &started}
	st := &ScoutTool{Runner: r}

	ctx := context.WithValue(scoutTestCtx(), scoutCallerCtxKey{}, "same-caller")

	var wg sync.WaitGroup
	results := make([]ToolResult, maxScoutsPerCaller)
	for i := 0; i < maxScoutsPerCaller; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = st.Run(ctx, map[string]any{"task": "hold"})
		}(i)
	}

	started.Wait() // both in-flight calls have reached the runner

	extra := st.Run(ctx, map[string]any{"task": "one too many"})
	if extra.Success {
		t.Error("expected a third concurrent scout from the same caller to be rejected")
	}

	close(release)
	wg.Wait()
	for i, res := range results {
		if !res.Success {
			t.Errorf("expected in-flight scout %d to succeed once released, got error: %s", i, res.Error)
		}
	}
}

// blockingScoutRunner blocks inside RunTask until release is closed,
// signalling started (once per call) the moment it begins blocking — lets
// a test know both concurrent calls are genuinely in flight before it
// tries to exceed the cap.
type blockingScoutRunner struct {
	release chan struct{}
	started *sync.WaitGroup
}

func (r *blockingScoutRunner) RunTask(ctx context.Context, task string, model string, opts SubagentOptions) (string, error) {
	r.started.Done()
	<-r.release
	return "ok", nil
}

func (r *blockingScoutRunner) RunTaskWithSystem(ctx context.Context, task, model, system string, opts SubagentOptions) (string, error) {
	return r.RunTask(ctx, task, model, opts)
}
