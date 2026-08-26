package tools

// B2: seven package-level "job hook" globals (jobStarter, jobAsker,
// jobAnswerer, jobProgressReporter, jobMailbox, jobResumer,
// jobActivityToucher) used to be plain vars written once by SetXxx from the
// setup path and read from job goroutines that outlive the tool call that
// started them — a data race whether or not it was ever actually observed,
// exactly the shape jobNotifier/jobNotifierMu (bgbash.go) already existed to
// avoid. Each was given the same sync.RWMutex-guarded pattern jobNotifier
// already used: a private getXxx() (or, for jobActivityToucher, its single
// caller touchJobActivity) copies the interface value out under RLock and
// returns/uses the local copy, never holding the lock across the call into
// the interface itself.
//
// These tests hammer every one of the seven with concurrent Set.../get...
// goroutines under `go test -race`. Verified manually that reverting any one
// of the seven vars back to a plain (unguarded) var and re-running
// `-race` makes its subtest fail with a DATA RACE report — see the fix
// commit's summary for which files/lines were reverted to check this.
//
// F11: four more of the same shape were found unguarded after that batch —
// jobExtensionRequester (extension.go), jobCanceler and jobLister
// (killjob.go), and jobPromoter (btw_readonly.go). Same pattern, same
// verification method (each subtest below was manually confirmed to report
// a DATA RACE when its var is reverted to unguarded — see the F11 fix
// commit's summary).
//
// F24: two more races, different shape from the seven/four above.
//
//  1. toolRegistry (tool.go) is a *map*, not a scalar/interface var —
//     written after init by SetSubAgentRunner and registerLuaToolsFromDir,
//     read on every tool call (runToolInner), every dispatcher concurrency
//     check (MaxParallelFor) and the Lua schema builder (luaToolsSchema).
//     A racing map read/write in Go doesn't quietly tear a value; it's
//     `fatal error: concurrent map read and map write`, which aborts the
//     process outright. Guarded by toolRegistryMu (tool.go) with
//     lookupTool/registerTool/unregisterTool/toolNames as the only ways in
//     or out — including from tests, which used to reach into the map
//     directly.
//  2. WaitTool.Waiter (wait.go) is a struct field mutated in place by
//     SetJobWaiter after the "wait" tool is already registered and being
//     read by other goroutines via Run/waitForJob. Guarding toolRegistry
//     does nothing for this one — the race is on the field, not the map
//     slot — so it gets its own waiterMu, in the same job-hook shape as
//     jobNotifier et al., just scoped to one WaitTool instance instead of a
//     package-level var.
//
// Verified manually for both: reverting toolRegistryMu or waiterMu back to
// no-ops and re-running `-race` reproduces a real failure for each test
// below — see the fix commit's summary for the exact output.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/jobs"
)

// The fakes below are minimal, behavior-free implementations of each of the
// seven interfaces — this file only exercises the concurrent Set/read path,
// not what a real adapter does with a call (that is covered by each tool's
// own tests: ask_test.go, message_test.go, wait_test.go, etc).

type raceJobStarter struct{}

func (raceJobStarter) Start(ctx context.Context, description, kind, parentID string, fn func(context.Context, string) (string, bool, error)) JobHandle {
	return raceJobHandle{}
}

type raceJobHandle struct{}

func (raceJobHandle) ID() string { return "race-job" }

type raceJobAsker struct{}

func (raceJobAsker) Ask(ctx context.Context, id, question string) (string, bool, bool) {
	return "", false, false
}

type raceJobAnswerer struct{}

func (raceJobAnswerer) Answer(id, text string, fromUser bool) bool { return false }

type raceJobProgressReporter struct{}

func (raceJobProgressReporter) SetProgress(id, text string) bool { return false }

type raceJobMailbox struct{}

func (raceJobMailbox) Resolve(id string) (string, bool) { return "", false }
func (raceJobMailbox) Post(id, text string) bool        { return false }
func (raceJobMailbox) IsLive(id string) bool            { return false }
func (raceJobMailbox) Drain(id string) []string         { return nil }

type raceJobResumer struct{}

func (raceJobResumer) Resume(ctx context.Context, jobID, task string) (JobHandle, error) {
	return raceJobHandle{}, nil
}

type raceJobActivityToucher struct{}

func (raceJobActivityToucher) TouchActivity(id string) {}

// raceJobExtensionRequester, raceJobCanceler, raceJobLister and
// raceJobPromoter are the F11 follow-up to the batch above: four more
// job-hook globals (jobExtensionRequester, jobCanceler, jobLister,
// jobPromoter) that got the same jobNotifier-style treatment but were missed
// by the original B2 audit. Same rule: minimal, behavior-free fakes, only
// exercising the concurrent Set/read path.

type raceJobExtensionRequester struct{}

func (raceJobExtensionRequester) RequestExtension(id string, seconds time.Duration, reason string) (string, bool) {
	return "", false
}

func (raceJobExtensionRequester) WaitExtension(ctx context.Context, id, requestID string) (bool, bool) {
	return false, false
}

func (raceJobExtensionRequester) ResolveExtension(id, requestID string, approve bool) bool {
	return false
}

type raceJobCanceler struct{}

func (raceJobCanceler) Cancel(id string) bool { return false }

// raceJobLister answers ListJobs with one fixed job ("race-child", parented
// on "race-parent") so parentIDOf(...) has something to actually walk during
// the concurrent test below, instead of trivially short-circuiting on an
// empty slice.
type raceJobLister struct{}

func (raceJobLister) ListJobs() []JobKindSource {
	return []JobKindSource{fakeJob{id: "race-child", parentID: "race-parent"}}
}

type raceJobPromoter struct{}

func (raceJobPromoter) Promote(ctx context.Context, jobID string) (JobHandle, error) {
	return raceJobHandle{}, nil
}

// runConcurrentSetGet spawns goroutines that call set and get in a tight
// loop for a bounded number of iterations, and fails the test if they do
// not both finish within the timeout (a hang, not a race, would show up as
// that). The actual race detection is `go test -race`'s job — it aborts the
// whole binary the instant it observes a race, which is what makes this
// test fail on the pre-fix plain-var code and pass on the fixed,
// mutex-guarded code.
func runConcurrentSetGet(t *testing.T, set func(), get func()) {
	t.Helper()
	const goroutines = 8
	const iterations = 5000

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				set()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				get()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent set/get did not finish within 10s")
	}
}

// TestJobGlobals_ConcurrentSetGet_RaceFree is table-driven over the seven
// globals B2 covers, one subtest per global, each hammering its SetXxx
// against its read path (the accessor every real call site now goes
// through — see e.g. tools/subagent.go's getJobStarter, tools/ask.go's
// getJobAsker/getJobAnswerer).
func TestJobGlobals_ConcurrentSetGet_RaceFree(t *testing.T) {
	t.Cleanup(func() {
		SetJobStarter(nil)
		SetJobAsker(nil)
		SetJobAnswerer(nil)
		SetJobProgressReporter(nil)
		SetJobMailbox(nil)
		SetJobResumer(nil)
		SetJobActivityToucher(nil)
		SetJobExtensionRequester(nil)
		SetJobCanceler(nil)
		SetJobLister(nil)
		SetJobPromoter(nil)
	})

	cases := []struct {
		name string
		set  func()
		get  func()
	}{
		{
			name: "jobStarter",
			set:  func() { SetJobStarter(raceJobStarter{}) },
			get:  func() { _ = getJobStarter() },
		},
		{
			name: "jobAsker",
			set:  func() { SetJobAsker(raceJobAsker{}) },
			get:  func() { _ = getJobAsker() },
		},
		{
			name: "jobAnswerer",
			set:  func() { SetJobAnswerer(raceJobAnswerer{}) },
			get:  func() { _ = getJobAnswerer() },
		},
		{
			name: "jobProgressReporter",
			set:  func() { SetJobProgressReporter(raceJobProgressReporter{}) },
			get:  func() { _ = getJobProgressReporter() },
		},
		{
			name: "jobMailbox",
			set:  func() { SetJobMailbox(raceJobMailbox{}) },
			get:  func() { _ = getJobMailbox() },
		},
		{
			name: "jobResumer",
			set:  func() { SetJobResumer(raceJobResumer{}) },
			get:  func() { _ = getJobResumer() },
		},
		{
			// touchJobActivity is jobActivityToucher's only reader — there is
			// no exported getter for it (see activity.go), so exercise the
			// real call site directly.
			name: "jobActivityToucher",
			set:  func() { SetJobActivityToucher(raceJobActivityToucher{}) },
			get:  func() { touchJobActivity("race-job") },
		},
		{
			// F11: request_timeout_extension/answer_job's RequestExtension/
			// WaitExtension/ResolveExtension path (extension.go, ask.go).
			name: "jobExtensionRequester",
			set:  func() { SetJobExtensionRequester(raceJobExtensionRequester{}) },
			get:  func() { _ = getJobExtensionRequester() },
		},
		{
			// F11: kill_job's Cancel dispatch (killjob.go).
			name: "jobCanceler",
			set:  func() { SetJobCanceler(raceJobCanceler{}) },
			get:  func() { _ = getJobCanceler() },
		},
		{
			// F11: the getter itself, which is the single read point for
			// jobLister's two production sites (killjob.go's Run and, via
			// parentIDOf, notifyToParent). Like most entries in this table
			// this exercises the getter in isolation, NOT those call sites —
			// only parentIDOf has real-call-path coverage, in
			// TestParentIDOf_ConcurrentSetJobListerAndRealCall_RaceFree
			// below. That is sufficient while the getter stays the sole
			// read point; it would not catch a NEW call site that read the
			// global directly.
			name: "jobLister",
			set:  func() { SetJobLister(raceJobLister{}) },
			get:  func() { _ = getJobLister() },
		},
		{
			// F11: promote_btw's Promote dispatch (btw_readonly.go).
			name: "jobPromoter",
			set:  func() { SetJobPromoter(raceJobPromoter{}) },
			get:  func() { _ = getJobPromoter() },
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runConcurrentSetGet(t, c.set, c.get)
		})
	}
}

// TestJobMailboxNextMessages_ConcurrentSetAndDrain_RaceFree pins the
// mailbox's other read path: the NextMessages closure JobMailboxNextMessages
// returns is handed to a background subagent's own agent.Config and called
// repeatedly from that job's long-lived goroutine, entirely independent of
// whatever SetJobMailbox calls happen concurrently from the setup path (or,
// in a test like this one, a scenario that keeps re-wiring it).
func TestJobMailboxNextMessages_ConcurrentSetAndDrain_RaceFree(t *testing.T) {
	t.Cleanup(func() { SetJobMailbox(nil) })
	SetJobMailbox(raceJobMailbox{})

	drain := JobMailboxNextMessages("race-job")
	if drain == nil {
		t.Fatal("expected a non-nil drain closure once a mailbox is wired")
	}

	runConcurrentSetGet(t, func() { SetJobMailbox(raceJobMailbox{}) }, func() { _ = drain() })
}

// ─── F5 (review round 2): race a setter against a REAL call site ──────────
//
// The subtests above race SetXxx against getXxx directly — the accessor
// pair the B2 fix itself introduced. That is necessary but not sufficient:
// it cannot catch a regression where a PRODUCTION call site reads the bare
// package var instead of going through the accessor, which is exactly what
// tools/bash.go, tools/cron.go and tools/bgbash.go were doing before this
// fix (an unlisted-by-the-original-audit gap). The tests below race
// SetJobStarter against the real entry points instead: SubagentTool.Run's
// async spawn, BashTool.Run's background handoff, and CronTool.Run's
// "run_now" spawn — the three places tools/subagent.go, tools/bash.go and
// tools/cron.go actually call getJobStarter().Start.

// flipJobStarter spins, alternating SetJobStarter between two real
// jobs.Registry-backed starters, until stop is closed. This is the
// "setter" side of every test below; the caller-side loop below runs on
// the test's own goroutine so at least one production call site genuinely
// overlaps a concurrent SetJobStarter, the same shape
// TestJobMailboxNextMessages_ConcurrentSetAndDrain_RaceFree already used
// for jobMailbox's real NextMessages call path.
func flipJobStarter(stop <-chan struct{}, regA, regB *jobs.Registry) {
	toggle := false
	for {
		select {
		case <-stop:
			return
		default:
		}
		if toggle {
			SetJobStarter(testJobStarter{regA})
		} else {
			SetJobStarter(testJobStarter{regB})
		}
		toggle = !toggle
	}
}

// waitRegistriesIdle polls every given registry until none of its jobs are
// still Running, or the deadline passes — used so a test does not return
// (and let its t.TempDir() get removed) while a background job spawned
// during the race is still executing.
func waitRegistriesIdle(t *testing.T, regs ...*jobs.Registry) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		anyRunning := false
		for _, reg := range regs {
			for _, j := range reg.List() {
				if j.Status == jobs.StatusRunning {
					anyRunning = true
				}
			}
		}
		if !anyRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("registries still had a running job after 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSubagentToolRun_ConcurrentSetJobStarterAndRealAsyncSpawn_RaceFree
// drives tools/subagent.go's real async-spawn path (SubagentTool.Run with
// async=true -> runAsync -> spawn -> getJobStarter().Start) concurrently
// with SetJobStarter swaps.
func TestSubagentToolRun_ConcurrentSetJobStarterAndRealAsyncSpawn_RaceFree(t *testing.T) {
	t.Cleanup(func() {
		unregisterTool("subagent")
		subagentToolInstance = nil
		SetJobStarter(nil)
		SetJobNotifier(nil)
	})

	regA := jobs.NewRegistry()
	regB := jobs.NewRegistry()
	SetJobNotifier(jobs.NewNotifier())
	SetSubAgentRunner(&mockRunner{
		RunTaskFunc: func(ctx context.Context, task, model string, opts SubagentOptions) (string, error) {
			return "ok", nil
		},
	})

	// Set a starter synchronously BEFORE the flipper goroutine and the
	// caller loop below both start touching it: without this, jobStarter is
	// still nil from the previous test's cleanup (SetJobStarter(nil)), and
	// nothing synchronizes the flipper's first SetJobStarter against the
	// first RunTool call below — so on an unlucky (or single-core, where
	// the flipper simply never gets scheduled first) run, runAsync sees a
	// nil starter and the async spawn fails outright, not flakily.
	SetJobStarter(testJobStarter{regA})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		flipJobStarter(stop, regA, regB)
	}()

	ctx := connector.WithModelClient(context.Background(), fakeModelClient("test/model"))
	const n = 200
	for i := 0; i < n; i++ {
		res := RunTool(ctx, "subagent", map[string]any{"task": "hi", "async": true})
		if !res.Success {
			t.Fatalf("async spawn %d failed: %s", i, res.Error)
		}
	}
	close(stop)
	wg.Wait()

	waitRegistriesIdle(t, regA, regB)
}

// TestBashToolRun_ConcurrentSetJobStarterAndRealHandoff_RaceFree drives
// tools/bash.go's real handoff path (BashTool.Run with
// run_in_background=true -> handoff -> getJobStarter().Start) concurrently
// with SetJobStarter swaps.
func TestBashToolRun_ConcurrentSetJobStarterAndRealHandoff_RaceFree(t *testing.T) {
	regA := jobs.NewRegistry()
	regB := jobs.NewRegistry()
	SetJobProgressReporter(regA)
	SetJobActivityToucher(regA)
	SetJobNotifier(jobs.NewNotifier())
	SetBackgroundBashEnabled(true)
	t.Cleanup(func() {
		KillAllBackgroundBash()
		deadline := time.Now().Add(5 * time.Second)
		for backgroundSlotsInUse() > 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		SetBackgroundBashEnabled(false)
		SetJobStarter(nil)
		SetJobProgressReporter(nil)
		SetJobActivityToucher(nil)
		SetJobNotifier(nil)
	})

	// See the identical comment in
	// TestSubagentToolRun_ConcurrentSetJobStarterAndRealAsyncSpawn_RaceFree
	// above: set a starter synchronously before racing it, so this test
	// does not depend on the flipper goroutine winning a scheduling race
	// against the first BashTool.Run call below. This path happens to
	// degrade to a foreground run (not a hard failure) when jobStarter is
	// nil, which is exactly why this particular test never surfaced the bug
	// even though it had it too.
	SetJobStarter(testJobStarter{regA})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		flipJobStarter(stop, regA, regB)
	}()

	const n = 40
	for i := 0; i < n; i++ {
		res := (&BashTool{}).Run(context.Background(), map[string]any{
			"command":           "echo hi",
			"run_in_background": true,
		})
		if !res.Success {
			t.Fatalf("handoff %d failed: %s", i, res.Error)
		}
	}
	close(stop)
	wg.Wait()

	waitRegistriesIdle(t, regA, regB)
}

// TestCronRunNow_ConcurrentSetJobStarterAndRealSpawn_RaceFree drives
// tools/cron.go's real "run_now" spawn path (CronTool.Run ->
// t.runNow -> getJobStarter().Start) concurrently with SetJobStarter
// swaps — the one call site the original B2 audit did not list.
//
// cronRunner() normally points at os.Executable(), which under `go test`
// IS the compiled test binary — exec'ing that recursively with "run
// --prompt ..." args is not something to risk in a test. cronRunnerExeOverride
// (tools/cron.go, test-only seam added for this test) points it at
// "/bin/echo" instead: fast, harmless, and still exercises the exact
// getJobStarter().Start call this test is about.
func TestCronRunNow_ConcurrentSetJobStarterAndRealSpawn_RaceFree(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	prevOverride := cronRunnerExeOverride
	cronRunnerExeOverride = "/bin/echo"
	t.Cleanup(func() { cronRunnerExeOverride = prevOverride })

	wd := t.TempDir()
	addRes := (&CronTool{}).Run(WithWorkdir(context.Background(), wd), map[string]any{
		"action": "add", "name": "race-job", "prompt": "hello", "schedule": "at 02:00",
	})
	if !addRes.Success {
		t.Fatalf("add: %s", addRes.Error)
	}

	regA := jobs.NewRegistry()
	regB := jobs.NewRegistry()
	SetJobNotifier(jobs.NewNotifier())
	t.Cleanup(func() {
		SetJobStarter(nil)
		SetJobNotifier(nil)
	})

	// See the identical comment in
	// TestSubagentToolRun_ConcurrentSetJobStarterAndRealAsyncSpawn_RaceFree
	// above: without this, jobStarter is nil from the previous test's
	// cleanup and runNow's "no registry: run it inline" fallback would mask
	// whether the flipper actually won the race to be scheduled first —
	// this path happens not to fail outright either way (it degrades to a
	// synchronous inline run), but it would silently skip exercising
	// getJobStarter().Start on that call.
	SetJobStarter(testJobStarter{regA})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		flipJobStarter(stop, regA, regB)
	}()

	const n = 30
	for i := 0; i < n; i++ {
		res := (&CronTool{}).Run(context.Background(), map[string]any{"action": "run_now", "name": "race-job"})
		if !res.Success {
			t.Fatalf("run_now %d failed: %s", i, res.Error)
		}
	}
	close(stop)
	wg.Wait()

	waitRegistriesIdle(t, regA, regB)
}

// TestParentIDOf_ConcurrentSetJobListerAndRealCall_RaceFree races
// SetJobLister against parentIDOf (killjob.go) — the second, easy-to-miss
// read site for jobLister: it reads the global directly rather than going
// through kill_job's Run, so a fix that only guarded the Run/
// killAllowedInsideChild path (via getJobLister there) would still leave
// this one racy.
func TestParentIDOf_ConcurrentSetJobListerAndRealCall_RaceFree(t *testing.T) {
	t.Cleanup(func() { SetJobLister(nil) })
	SetJobLister(raceJobLister{})

	runConcurrentSetGet(t,
		func() { SetJobLister(raceJobLister{}) },
		func() { _ = parentIDOf("race-child") },
	)
}

// Not driven here: cron's in-session ticker (StartCronTicker, tools/cron.go)
// also calls cronRunner and can spawn jobs via getJobStarter, but only on a
// real wall-clock schedule, guarded by a package-level "already running"
// latch (cronTickerRunning) that is not designed to be started twice in one
// test binary. TestCronRunNow_ConcurrentSetJobStarterAndRealSpawn_RaceFree
// above already exercises the exact same getJobStarter().Start call site the
// ticker would reach through cronRunner/runNow's shared code — driving the
// ticker itself would only add flakiness for no new coverage.

// TestToolRegistry_ConcurrentRegistrationAndRealToolCalls_RaceFree drives
// concurrent writers into toolRegistry — SetSubAgentRunner (replaces the
// "subagent" slot with a brand-new *SubagentTool) and registerLuaToolsFromDir
// (which after the first iteration takes its collision branch — see below)
// — concurrently with the real production read paths: RunTool (->
// runToolInner -> tool.Run), MaxParallelFor, and GetToolsSchema (which
// calls luaToolsSchema).
//
// Note on the lua writer: the fixture directory is registered once before
// the goroutines start, so every later iteration hits
// registerLuaToolsFromDir's collision branch and never inserts again. What
// it contributes for the rest of the run is a locked map READ plus a stderr
// warning, not a stream of inserts. That is enough for what this test is
// for — an unguarded read racing SetSubAgentRunner's write is already the
// bug — but do not read it as "insert vs read" coverage: the insert happens
// exactly once.
//
// An unguarded map here does not merely risk a torn
// value the way the job-hook globals above do: a concurrent Go map
// read/write is `fatal error: concurrent map read and map write`, which
// aborts the whole process — see the fix commit's summary for the real
// -race/-count=1 output with toolRegistryMu reverted to a no-op.
func TestToolRegistry_ConcurrentRegistrationAndRealToolCalls_RaceFree(t *testing.T) {
	t.Cleanup(func() {
		unregisterTool("subagent")
		unregisterTool("f24-race-lua")
		subagentToolInstance = nil
	})

	// A real Lua tool directory the writer goroutine (re)loads on every
	// iteration — registerLuaToolsFromDir's "does this collide with a
	// built-in" check and its insert must be one atomic critical section
	// against concurrent readers and the other writer goroutine below.
	tmpDir := t.TempDir()
	toolContent := `return {
  schema = {
    name = "f24-race-lua",
    description = "F24 concurrency test tool",
    parameters = {}
  },
  run = function(ctx, args)
    return {success = true, content = "ok"}
  end
}`
	if err := os.WriteFile(filepath.Join(tmpDir, "f24.lua"), []byte(toolContent), 0644); err != nil {
		t.Fatalf("write test lua tool: %v", err)
	}

	// Seed the registry before the reader loop starts, same reasoning as
	// TestSubagentToolRun_ConcurrentSetJobStarterAndRealAsyncSpawn_RaceFree's
	// pre-seed: nothing otherwise synchronizes the first writer pass against
	// the first read below.
	SetSubAgentRunner(&mockRunner{})
	registerLuaToolsFromDir(tmpDir)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				SetSubAgentRunner(&mockRunner{})
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				registerLuaToolsFromDir(tmpDir)
			}
		}
	}()

	ctx := context.Background()
	const n = 500
	for i := 0; i < n; i++ {
		// Real call path: RunTool -> runToolInner -> lookupTool -> tool.Run.
		// "help" with no args builds the index, which reaches the registry a
		// SECOND time: helpIndex -> GetAllToolsSchema -> GetToolsSchema ->
		// luaToolsSchema -> toolRegistryMu.RLock. That is deliberate and it
		// makes this test stronger, not weaker — it covers both the
		// single-entry lookup and the whole-map iteration, which are the two
		// shapes that abort the process when unguarded. It also proves the
		// two are not nested: lookupTool's lock is released before Run, so
		// luaToolsSchema takes a fresh RLock rather than recursing (RWMutex
		// is not reentrant, so a nested acquire would deadlock here).
		res := RunTool(ctx, "help", map[string]any{})
		if !res.Success {
			t.Fatalf("help %d failed: %s", i, res.Error)
		}
		// Real call path: MaxParallelFor -> lookupTool.
		_ = MaxParallelFor("bash")
		// Real call path: GetToolsSchema -> luaToolsSchema -> toolRegistry
		// snapshot.
		_ = GetToolsSchema()
	}

	close(stop)
	wg.Wait()
}

// TestWaitToolRun_ConcurrentSetJobWaiterAndRealJobIDWait_RaceFree drives the
// registered "wait" singleton's real job_id path (RunTool("wait", ...) ->
// WaitTool.Run -> waitForJob -> waiter.Wait) concurrently with SetJobWaiter
// swaps. This is race B from F24: SetJobWaiter used to write wt.Waiter
// directly on the registered *WaitTool while Run/waitForJob read the same
// field from other goroutines — a data race on the struct field itself, not
// on the toolRegistry map slot, so guarding the map (race A, above) does
// nothing for it. Guarded instead by WaitTool.waiterMu (wait.go).
func TestWaitToolRun_ConcurrentSetJobWaiterAndRealJobIDWait_RaceFree(t *testing.T) {
	t.Cleanup(func() { SetJobWaiter(nil) })

	waiterA := &mockJobWaiter{ok: true, status: JobStatus{ID: "job-a", Done: true, Success: true, Content: "a"}}
	waiterB := &mockJobWaiter{ok: true, status: JobStatus{ID: "job-b", Done: true, Success: true, Content: "b"}}
	SetJobWaiter(waiterA)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		flip := false
		for {
			select {
			case <-stop:
				return
			default:
				if flip {
					SetJobWaiter(waiterA)
				} else {
					SetJobWaiter(waiterB)
				}
				flip = !flip
			}
		}
	}()

	ctx := context.Background()
	const n = 500
	for i := 0; i < n; i++ {
		// Real call path: RunTool -> runToolInner -> lookupTool("wait") ->
		// WaitTool.Run -> waitForJob -> waiter().Wait. Both waiters report
		// Done immediately, so this returns without any real sleep.
		res := RunTool(ctx, "wait", map[string]any{"seconds": JobMinWaitSeconds, "job_id": "job-x"})
		if !res.Success {
			t.Fatalf("wait %d failed: %s", i, res.Error)
		}
	}
	close(stop)
	wg.Wait()
}
