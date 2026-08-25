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

import (
	"context"
	"sync"
	"testing"
	"time"
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
