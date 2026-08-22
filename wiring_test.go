package main

// This file drives the async job/eventbus/lock subsystem through the REAL
// production composition-root wiring (wireTools, agentRunner,
// jobStarterAdapter/jobWaiterAdapter, JobRegistry, jobEventBus) via
// agent.Run + connectortest.Fake, instead of hand-rolled mocks. See
// docs/subagent-testing-plan.md for the full manual scenario catalog this
// automates; scenario IDs are cited in comments below.
//
// Harness notes (apply to every test in this file):
//   - withTestWiring(t) swaps JobRegistry/jobEventBus for fresh instances
//     and re-runs wireTools() so each test gets an isolated job registry and
//     event bus, restoring the originals on cleanup.
//   - These tests share package-level mutable state (JobRegistry,
//     jobEventBus, providers.Default, the tools package's singletons) and
//     must NOT run under t.Parallel with each other.
//   - Prefer channel-gated synchronization (jobs.Registry.Wait, explicit
//     "release" channels) over time.Sleep for determinism; a bounded
//     timeout used only as a safety cap (not as the actual synchronization)
//     is fine and is not "sleeping to coordinate".
//   - No goleak dependency in this repo (checked go.mod/go.sum); leak checks
//     are hand-rolled against runtime.NumGoroutine() with a short settle
//     loop, matching this codebase's "don't add a dependency for a few
//     lines" convention (see connectortest's own doc comment).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/eventbus"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// withTestWiring gives a test its own JobRegistry and jobEventBus, wired via
// the same wireTools() main() calls, and restores the previous globals (plus
// production wiring over them) on cleanup so later tests — and any real
// wiring done elsewhere in this package's test binary — are unaffected.
//
// jobs.Registry.Start's completing goroutine closes job.done and THEN, a
// moment later in that same goroutine, invokes onEvent — which reads the
// PACKAGE-LEVEL jobEventBus at call time (see wireTools). So a job observed
// "done" via reg.Wait can still have its terminal onEvent call in flight for
// a brief window afterwards. Swapping jobEventBus back under that in-flight
// call would race with its read. Cleanup therefore subscribes to the test's
// bus for the test's whole lifetime and, before swapping anything back,
// waits until every job's LATEST status has actually been observed as an
// event — not a fixed sleep, an actual drain of the real notification.
func withTestWiring(t *testing.T) (*jobs.Registry, *eventbus.Bus) {
	t.Helper()
	origReg, origBus, origNotices := JobRegistry, jobEventBus, JobNotices

	reg := jobs.NewRegistry()
	bus := eventbus.New(64)
	// A fresh notice queue too: wireTools points the tools package at
	// whatever JobNotices currently is, so leaving the production one in
	// place would let one test's background-command notices show up in
	// another's.
	JobRegistry, jobEventBus, JobNotices = reg, bus, jobs.NewNotifier()
	wireTools()

	evCh, unsub := bus.Subscribe("job.updated")
	var mu sync.Mutex
	seen := make(map[string]jobs.Status)
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for ev := range evCh {
			if j, ok := ev.Payload.(jobs.Job); ok {
				mu.Lock()
				seen[j.ID] = j.Status
				mu.Unlock()
			}
		}
	}()

	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for {
			pending := false
			for _, j := range reg.List() {
				mu.Lock()
				s, ok := seen[j.ID]
				mu.Unlock()
				if !ok || s != j.Status {
					pending = true
					break
				}
			}
			if !pending {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("withTestWiring cleanup: timed out waiting for job.updated events to catch up with job state")
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		unsub()
		<-drainDone

		JobRegistry, jobEventBus, JobNotices = origReg, origBus, origNotices
		wireTools()
	})
	return reg, bus
}

// waitForGoroutineSettle polls runtime.NumGoroutine() until it drops to at
// most `before` (background goroutines from finished jobs/watchers can take
// a moment to actually exit) or the deadline passes, in which case it fails
// the test. This is the hand-rolled equivalent of goleak.VerifyNone for a
// single before/after snapshot around one test's work.
func waitForGoroutineSettle(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		after := runtime.NumGoroutine()
		if after <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutine leak: before=%d after=%d (did not settle within 2s)", before, after)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// testSink is a minimal, thread-safe agent.Sink recording final text and
// tool call outcomes, for tests that drive a full agent.Run round trip and
// need to assert on what the parent actually produced/saw.
type testSink struct {
	mu        sync.Mutex
	text      strings.Builder
	toolCalls []string // "name" per ToolCallStart, in order
	toolEnds  []toolEnd
	errs      []error
}

type toolEnd struct {
	name   string
	result string
}

func (s *testSink) Request(string) {}
func (s *testSink) Thinking(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text.WriteString(text)
}
func (s *testSink) Text(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text.WriteString(text)
}
func (s *testSink) ToolCallStart(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCalls = append(s.toolCalls, name)
}
func (s *testSink) ToolCallDelta(string) {}
func (s *testSink) ToolCallEnd(name, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolEnds = append(s.toolEnds, toolEnd{name, result})
}
func (s *testSink) ToolFinish()                        {}
func (s *testSink) ToolBlock(string)                   {}
func (s *testSink) Summary(stream.Usage, stream.Stats) {}
func (s *testSink) Total(stream.Usage)                 {}
func (s *testSink) Error(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
}
func (s *testSink) End() {}

func (s *testSink) CollectedText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text.String()
}

func (s *testSink) ToolResults() []toolEnd {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]toolEnd, len(s.toolEnds))
	copy(out, s.toolEnds)
	return out
}

// lastToolResultText scans req.Messages back-to-front for the most recent
// toolResult content block's text — how a Fake.Script reads back what a
// previous turn's tool call actually produced.
func lastToolResultText(req connector.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role != "toolResult" {
			continue
		}
		for _, c := range m.Content {
			if c.Text != "" {
				return c.Text
			}
		}
	}
	return ""
}

var jobIDPattern = regexp.MustCompile(`"job_id"\s*:\s*"([^"]+)"`)

// snapshotByID returns the Snapshot() (a value copy, safe to read without
// the registry's own lock) for id, or ok=false if unknown. Unlike
// Registry.Get (which returns the LIVE *Job pointer, still mutated by the
// registry under its own mutex), this is the only safe way for a test to
// poll a job's evolving Status/Question/Progress fields without racing the
// registry's writer goroutines.
func snapshotByID(reg *jobs.Registry, id string) (jobs.Job, bool) {
	for _, j := range reg.List() {
		if j.ID == id {
			return j, true
		}
	}
	return jobs.Job{}, false
}

// extractJobID pulls the job_id a subagent(async=true) tool call minted out
// of req's message history — the one thing a static Fake.Turns script
// cannot do, and the reason Fake.Script exists.
func extractJobID(req connector.Request) (string, bool) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		for _, c := range req.Messages[i].Content {
			if m := jobIDPattern.FindStringSubmatch(c.Text); m != nil {
				return m[1], true
			}
		}
	}
	return "", false
}

// waitToolArgs builds the JSON arguments for a "wait" tool call.
//
// Callers pass tools.JobMinWaitSeconds rather than some small number: a job
// wait below that floor is raised, and the raise appends an explanatory note
// to the tool result. That note is correct behaviour but it is not what these
// tests are about, and it would show up inside every asserted result string.
func waitToolArgs(jobID string, seconds int) string {
	data, _ := json.Marshal(map[string]any{"job_id": jobID, "seconds": seconds})
	return string(data)
}

// =============================================================================
// R-1 (single most valuable test): full-stack async round trip through real
// production wiring.
// =============================================================================

// TestWiring_R1_FullStackAsyncRoundTrip drives: parent agent.Run (real Fake,
// real toolsAdapter/tools.RunTool) -> subagent(async:true) -> real
// agentRunner resolving the child's Fake via connector.ModelClientFromContext
// / providers.FindModel -> real jobStarterAdapter/JobRegistry -> job.updated
// events observed on the real jobEventBus -> parent's wait(job_id) through
// the real jobWaiterAdapter returns the child's result -> parent's final
// turn incorporates it into its answer.
func TestWiring_R1_FullStackAsyncRoundTrip(t *testing.T) {
	reg, bus := withTestWiring(t)
	// Baseline taken AFTER withTestWiring, whose own event-drain goroutine
	// lives for the rest of the test — otherwise it would look like a leak.
	before := runtime.NumGoroutine()

	// Subscribe before anything runs so we're guaranteed to see both the
	// running and terminal events for the job this test spawns (see C-7).
	evCh, unsub := bus.Subscribe("job.updated")
	t.Cleanup(unsub)

	childFake := connectortest.Text("child answer")
	providers.Register(&fixedClientProvider{name: "r1-child-prov", client: childFake})

	parentFake := &connectortest.Fake{ProviderName: "r1-parent-prov", ModelName: "r1-parent-model"}
	parentFake.Script = func(turn int, req connector.Request) []stream.Event {
		switch turn {
		case 0:
			return []stream.Event{
				stream.ToolCall{ID: "tc-spawn", Name: "subagent", Arguments: `{"task":"say hi","async":true,"model":"r1-child-prov/child-model"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		case 1:
			jobID, ok := extractJobID(req)
			if !ok {
				t.Fatalf("turn 1: could not find job_id in request history: %+v", req.Messages)
			}
			return []stream.Event{
				stream.ToolCall{ID: "tc-wait", Name: "wait", Arguments: waitToolArgs(jobID, tools.JobMinWaitSeconds)},
				stream.Finish{Reason: "tool_calls"},
			}
		default:
			return []stream.Event{
				stream.TextDelta{Text: "parent saw: " + lastToolResultText(req)},
				stream.Finish{Reason: "stop"},
			}
		}
	}

	ctx := connector.WithModelClient(context.Background(), parentFake)
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "please delegate to a subagent"}}},
	}
	sink := &testSink{}
	cfg := agent.Config{
		MaxRetries:    1,
		MaxIterations: 10,
		Tools:         toolsAdapter{},
		Schema:        tools.GetToolsSchemaJSON(),
	}

	_, err := agent.Run(ctx, parentFake, sink, &msgs, cfg)
	if err != nil {
		t.Fatalf("parent agent.Run: %v", err)
	}

	wantText := "parent saw: job finished: child answer"
	if got := sink.CollectedText(); got != wantText {
		t.Fatalf("parent final text = %q, want %q", got, wantText)
	}

	// The job itself must be visible, real, and Done in the shared registry.
	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 job in the registry, got %d: %+v", len(list), list)
	}
	job := list[0]
	if job.Status != jobs.StatusDone {
		t.Fatalf("job status = %s, want done", job.Status)
	}
	if job.Result != "child answer" {
		t.Fatalf("job result = %q, want %q", job.Result, "child answer")
	}

	// Drain the subscribed events (published asynchronously; give them a
	// moment to land, bounded — this is a safety cap, not the sync
	// mechanism, since the actual coordination above already happened via
	// agent.Run/wait() returning).
	var statuses []jobs.Status
	deadline := time.After(time.Second)
collect:
	for {
		select {
		case ev := <-evCh:
			j, ok := ev.Payload.(jobs.Job)
			if !ok {
				t.Fatalf("event payload type = %T, want jobs.Job (value, not pointer) — see X-5", ev.Payload)
			}
			if j.ID != job.ID {
				continue
			}
			statuses = append(statuses, j.Status)
			if j.Status != jobs.StatusRunning {
				break collect
			}
		case <-deadline:
			break collect
		}
	}
	if len(statuses) < 2 || statuses[0] != jobs.StatusRunning || statuses[len(statuses)-1] != jobs.StatusDone {
		t.Errorf("job.updated events for this job = %v, want [running ... done]", statuses)
	}

	waitForGoroutineSettle(t, before)
}

// TestWiring_R6_WireToolsIdempotent calling wireTools() twice must not
// double-deliver job.updated events — SetOnEvent replaces the hook, it does
// not accumulate.
func TestWiring_R6_WireToolsIdempotent(t *testing.T) {
	_, bus := withTestWiring(t)
	wireTools()
	wireTools()

	evCh, unsub := bus.Subscribe("job.updated")
	defer unsub()

	release := make(chan struct{})
	job := JobRegistry.Start(context.Background(), "idempotent", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "ok", false, nil
	})
	close(release)

	var got []jobs.Status
	timeout := time.After(2 * time.Second)
loop:
	for {
		select {
		case ev := <-evCh:
			j := ev.Payload.(jobs.Job)
			if j.ID != job.ID {
				continue
			}
			got = append(got, j.Status)
			if j.Status != jobs.StatusRunning {
				break loop
			}
		case <-timeout:
			t.Fatal("timed out waiting for job.updated events")
		}
	}
	// Exactly one running + one terminal event, not two of each (which a
	// duplicated SetOnEvent hook would produce).
	if len(got) != 2 {
		t.Fatalf("events = %v, want exactly 2 (one running, one terminal) despite wireTools() called 3x total", got)
	}
}

// =============================================================================
// L-5 (high priority): a no-TTL lock taken inside an async subagent is
// released shortly after the job reaches a terminal status, even though the
// agent itself never called unlock.
// =============================================================================

func TestWiring_L5_NoTTLLockInAsyncJobReleasedOnJobTermination(t *testing.T) {
	reg, _ := withTestWiring(t)
	// Baseline taken AFTER withTestWiring, whose own event-drain goroutine
	// lives for the rest of the test — otherwise it would look like a leak.
	before := runtime.NumGoroutine()

	childFake := &connectortest.Fake{ProviderName: "l5-child-prov"}
	childFake.Turns = [][]stream.Event{
		{
			stream.ToolCall{ID: "tc-lock", Name: "lock", Arguments: `{"path":"/shared/thing"}`},
			stream.Finish{Reason: "tool_calls"},
		},
		{
			// Child deliberately never calls unlock — the job's deferred
			// cancel (context.WithoutCancel + locks.Registry's ctx-watcher)
			// must release the lock anyway once the job goes terminal.
			stream.TextDelta{Text: "locked, done"},
			stream.Finish{Reason: "stop"},
		},
	}
	providers.Register(&fixedClientProvider{name: "l5-child-prov", client: childFake})

	parentFake := &connectortest.Fake{ProviderName: "l5-parent-prov"}
	parentFake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.ToolCall{ID: "tc-spawn", Name: "subagent", Arguments: `{"task":"lock it","async":true,"model":"l5-child-prov/child-model"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		if turn == 1 {
			jobID, ok := extractJobID(req)
			if !ok {
				t.Fatalf("could not find job_id: %+v", req.Messages)
			}
			return []stream.Event{
				stream.ToolCall{ID: "tc-wait", Name: "wait", Arguments: waitToolArgs(jobID, tools.JobMinWaitSeconds)},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		return []stream.Event{stream.TextDelta{Text: "ok"}, stream.Finish{Reason: "stop"}}
	}

	ctx := connector.WithModelClient(context.Background(), parentFake)
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	sink := &testSink{}
	cfg := agent.Config{MaxRetries: 1, MaxIterations: 10, Tools: toolsAdapter{}, Schema: tools.GetToolsSchemaJSON()}

	if _, err := agent.Run(ctx, parentFake, sink, &msgs, cfg); err != nil {
		t.Fatalf("parent agent.Run: %v", err)
	}

	list := reg.List()
	if len(list) != 1 || list[0].Status != jobs.StatusDone {
		t.Fatalf("expected exactly 1 done job, got %+v", list)
	}

	// The job is terminal (confirmed by the parent's own wait() returning
	// "job finished"), so the deferred cancel on the job's context should
	// have already fired. Poll briefly to give the ctx-watcher goroutine a
	// moment to actually run — this is not the synchronization for the
	// job's completion (that already happened above), just for the
	// asynchronous release that follows it.
	deadline := time.Now().Add(time.Second)
	for {
		if _, locked := tools.LockRegistry.IsLocked("/shared/thing"); !locked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock taken inside the async job was never released after the job went terminal")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A second holder can now acquire it, proving it's really free, not
	// just reporting free due to a bug in IsLocked.
	release, ok, _ := tools.LockRegistry.Acquire(context.Background(), "/shared/thing", "someone-else", 0)
	if !ok {
		t.Fatal("expected a fresh acquire to succeed after the job's lock was released")
	}
	release()

	// The ctx-watcher goroutine locks.Registry spawned for the no-TTL lock
	// must also have exited, not accumulated.
	waitForGoroutineSettle(t, before)
}

// =============================================================================
// C-1 (high priority, flagship): two async subagents race for the same lock.
// =============================================================================

// TestWiring_C1_TwoAsyncSubagentsRaceForSameLock spawns two async subagents
// in one subagent(tasks:[...], async:true) call; both try to lock the same
// path. Exactly one must win; the loser must see an informative conflict
// naming the winner, then (per L-5's release-on-terminal mechanism) succeed
// once the winner's job finishes and its no-TTL lock auto-releases.
func TestWiring_C1_TwoAsyncSubagentsRaceForSameLock(t *testing.T) {
	const iterations = 20
	for i := 0; i < iterations; i++ {
		t.Run(fmt.Sprintf("iter-%d", i), func(t *testing.T) {
			runC1Iteration(t)
		})
	}
}

func runC1Iteration(t *testing.T) {
	reg, _ := withTestWiring(t)

	// Both children run the EXACT SAME script — "winner"/"loser" is decided
	// by which one's lock tool call actually succeeds (the real race), not
	// by which fake we hand-labeled ahead of time. Whichever succeeds first
	// signals holderChan (buffered 1: only the true winner's send lands)
	// and blocks until the test releases it, so the other one is
	// GUARANTEED to observe a real conflict at least once instead of maybe
	// winning the underlying race itself. The one that sees "already
	// locked" retries — the wait+retry idiom (C-3) — until it succeeds.
	holderChan := make(chan string, 1)
	release := make(chan struct{})

	childScript := func(name string) func(int, connector.Request) []stream.Event {
		attempts := 0
		return func(turn int, req connector.Request) []stream.Event {
			if turn == 0 {
				return []stream.Event{
					stream.ToolCall{ID: "l0", Name: "lock", Arguments: `{"path":"/race/path"}`},
					stream.Finish{Reason: "tool_calls"},
				}
			}
			result := lastToolResultText(req)
			if strings.Contains(result, "already locked") {
				attempts++
				if attempts > 500 {
					t.Fatalf("%s retried lock too many times without ever winning", name)
				}
				return []stream.Event{
					stream.ToolCall{ID: fmt.Sprintf("l%d", turn), Name: "lock", Arguments: `{"path":"/race/path"}`},
					stream.Finish{Reason: "tool_calls"},
				}
			}
			// This call's lock succeeded: we are the (real) winner this
			// iteration. Announce it and hold until told to let go.
			select {
			case holderChan <- name:
			default:
			}
			<-release
			return []stream.Event{stream.TextDelta{Text: name + " done"}, stream.Finish{Reason: "stop"}}
		}
	}

	fakeA := &connectortest.Fake{ProviderName: "c1-a"}
	fakeA.Script = childScript("a")
	providers.Register(&fixedClientProvider{name: "c1-a-prov", client: fakeA})

	fakeB := &connectortest.Fake{ProviderName: "c1-b"}
	fakeB.Script = childScript("b")
	providers.Register(&fixedClientProvider{name: "c1-b-prov", client: fakeB})

	parentFake := &connectortest.Fake{ProviderName: "c1-parent"}
	parentFake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.ToolCall{ID: "spawn", Name: "subagent", Arguments: `{"tasks":[
					{"task":"grab the lock (a)","async":true,"model":"c1-a-prov/m"},
					{"task":"grab the lock (b)","async":true,"model":"c1-b-prov/m"}
				]}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		return []stream.Event{stream.TextDelta{Text: "spawned both"}, stream.Finish{Reason: "stop"}}
	}

	ctx := connector.WithModelClient(context.Background(), parentFake)
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	sink := &testSink{}
	cfg := agent.Config{MaxRetries: 1, MaxIterations: 5, Tools: toolsAdapter{}, Schema: tools.GetToolsSchemaJSON()}

	if _, err := agent.Run(ctx, parentFake, sink, &msgs, cfg); err != nil {
		t.Fatalf("parent agent.Run: %v", err)
	}

	// Wait for whichever child actually won to report holding the lock
	// before releasing it, so the other one has something real to contend
	// with.
	select {
	case <-holderChan:
	case <-time.After(2 * time.Second):
		t.Fatal("neither subagent ever reported holding the lock")
	}

	if _, locked := tools.LockRegistry.IsLocked("/race/path"); !locked {
		t.Fatal("expected /race/path to be locked by the winner at this point")
	}

	close(release)

	// Both jobs must eventually finish; poll List() until both terminal.
	deadline := time.Now().Add(5 * time.Second)
	for {
		list := reg.List()
		done := 0
		for _, j := range list {
			if j.Status != jobs.StatusRunning {
				done++
			}
		}
		if done == len(list) && len(list) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("jobs did not both finish in time: %+v", list)
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, j := range reg.List() {
		if j.Status != jobs.StatusDone {
			t.Errorf("job %q ended %s (%s), want done", j.Description, j.Status, j.Err)
		}
	}
	if _, locked := tools.LockRegistry.IsLocked("/race/path"); locked {
		t.Error("lock still held after both subagents finished")
	}
}

// =============================================================================
// A-8: an async job survives and completes even after the PARENT turn's
// context is cancelled — proves context.WithoutCancel is really in effect.
// =============================================================================

func TestWiring_A8_JobOutlivesParentContextCancellation(t *testing.T) {
	reg, _ := withTestWiring(t)

	childRelease := make(chan struct{})
	childStarted := make(chan struct{})
	childFake := &connectortest.Fake{ProviderName: "a8-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		close2(childStarted)
		<-childRelease
		return []stream.Event{stream.TextDelta{Text: "survived parent cancellation"}, stream.Finish{Reason: "stop"}}
	}
	providers.Register(&fixedClientProvider{name: "a8-child-prov", client: childFake})

	parentFake := &connectortest.Fake{ProviderName: "a8-parent"}
	parentFake.Turns = [][]stream.Event{
		{
			stream.ToolCall{ID: "spawn", Name: "subagent", Arguments: `{"task":"long job","async":true,"model":"a8-child-prov/m"}`},
			stream.Finish{Reason: "tool_calls"},
		},
	}

	// The parent's OWN context — the thing that dies "with this tool call's
	// turn" per runAsync's doc comment.
	parentCtx, cancelParent := context.WithCancel(context.Background())
	ctx := connector.WithModelClient(parentCtx, parentFake)
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	sink := &testSink{}
	cfg := agent.Config{MaxRetries: 1, MaxIterations: 3, Tools: toolsAdapter{}, Schema: tools.GetToolsSchemaJSON()}

	_, _ = agent.Run(ctx, parentFake, sink, &msgs, cfg)

	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 spawned job, got %d", len(list))
	}
	jobID := list[0].ID

	select {
	case <-childStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("child job never started")
	}

	// Cancel the parent's turn context NOW, before the child finishes.
	cancelParent()

	// Give the (wrongly-linked, if the bug regressed) cancellation a moment
	// to propagate, then let the child produce its answer.
	time.Sleep(50 * time.Millisecond)
	close(childRelease)

	final, ok := reg.Wait(context.Background(), jobID, 3*time.Second)
	if !ok {
		t.Fatal("job vanished from the registry")
	}
	if final.Status != jobs.StatusDone {
		t.Fatalf("job status = %s (err=%q), want done — the parent's context cancellation must not have propagated to the job", final.Status, final.Err)
	}
	if final.Result != "survived parent cancellation" {
		t.Fatalf("job result = %q, want the child's real answer", final.Result)
	}
}

func close2(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// =============================================================================
// C-6 (high priority): a slow/non-draining eventbus subscriber must not
// block job completion for anyone else.
// =============================================================================

func TestWiring_C6_SlowSubscriberDoesNotBlockOtherJobs(t *testing.T) {
	reg, bus := withTestWiring(t)

	// A subscriber that never reads its channel at all — the worst case.
	stalledCh, unsub := bus.Subscribe("job.updated")
	defer unsub()
	_ = stalledCh // deliberately never drained

	const n = 5
	var wg sync.WaitGroup
	results := make([]bool, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job := reg.Start(context.Background(), fmt.Sprintf("job-%d", i), func(ctx context.Context, _ string) (string, bool, error) {
				return "ok", false, nil
			})
			final, ok := reg.Wait(context.Background(), job.ID, 2*time.Second)
			results[i] = ok && final.Status == jobs.StatusDone
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	for i, ok := range results {
		if !ok {
			t.Errorf("job %d did not finish successfully", i)
		}
	}
	if elapsed > time.Second {
		t.Errorf("jobs took %v to finish with a stalled subscriber present — Publish must be non-blocking", elapsed)
	}
}

// =============================================================================
// A-4 / A-5: truncated-vs-failed on the iteration cap, through the real
// async job chain (subagent tool -> agentRunner -> agent.Run ->
// ErrMaxIterations -> jobs.Registry status).
// =============================================================================

// alwaysToolCallFake keeps emitting a harmless tool call forever, forcing the
// child to hit its MaxIterations cap.
func alwaysToolCallFake(providerName string) *connectortest.Fake {
	return &connectortest.Fake{
		ProviderName: providerName,
		OnExhausted: []stream.Event{
			stream.ToolCall{ID: "noop", Name: "todo", Arguments: `{"action":"list"}`},
			stream.Finish{Reason: "tool_calls"},
		},
	}
}

// TestWiring_A4_TruncatedWithTextIsJobTruncatedAndWaitSuccess: the child
// produces SOME text before hitting the cap (a TextDelta on the very last
// scripted turn, followed by more tool calls past max_iterations) — the job
// must end Truncated, and wait() must report Success:true (partial content).
func TestWiring_A4_TruncatedWithTextIsJobTruncatedAndWaitSuccess(t *testing.T) {
	reg, _ := withTestWiring(t)

	childFake := &connectortest.Fake{ProviderName: "a4-child"}
	childFake.Turns = [][]stream.Event{
		{stream.TextDelta{Text: "partial progress"}, stream.ToolCall{ID: "t0", Name: "todo", Arguments: `{"action":"list"}`}, stream.Finish{Reason: "tool_calls"}},
	}
	childFake.OnExhausted = []stream.Event{
		stream.ToolCall{ID: "keep-going", Name: "todo", Arguments: `{"action":"list"}`},
		stream.Finish{Reason: "tool_calls"},
	}
	providers.Register(&fixedClientProvider{name: "a4-child-prov", client: childFake})

	parentFake := &connectortest.Fake{ProviderName: "a4-parent"}
	parentFake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.ToolCall{ID: "spawn", Name: "subagent", Arguments: `{"task":"grind forever","async":true,"model":"a4-child-prov/m","max_iterations":2}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		if turn == 1 {
			jobID, ok := extractJobID(req)
			if !ok {
				t.Fatalf("no job_id found: %+v", req.Messages)
			}
			return []stream.Event{
				stream.ToolCall{ID: "w", Name: "wait", Arguments: waitToolArgs(jobID, tools.JobMinWaitSeconds)},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		return []stream.Event{stream.TextDelta{Text: lastToolResultText(req)}, stream.Finish{Reason: "stop"}}
	}

	ctx := connector.WithModelClient(context.Background(), parentFake)
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	sink := &testSink{}
	cfg := agent.Config{MaxRetries: 1, MaxIterations: 10, Tools: toolsAdapter{}, Schema: tools.GetToolsSchemaJSON()}

	if _, err := agent.Run(ctx, parentFake, sink, &msgs, cfg); err != nil {
		t.Fatalf("parent agent.Run: %v", err)
	}

	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 job, got %d", len(list))
	}
	if list[0].Status != jobs.StatusTruncated {
		t.Fatalf("job status = %s, want truncated", list[0].Status)
	}

	// wait()'s report to the parent must read as success (a usable partial
	// result), not an error.
	if !strings.HasPrefix(sink.CollectedText(), "job finished:") {
		t.Fatalf("parent's final text = %q, want it to start with wait()'s success-shaped report", sink.CollectedText())
	}
	if strings.Contains(sink.CollectedText(), "still running") {
		t.Fatalf("wait() reported still-running instead of the truncated result: %q", sink.CollectedText())
	}
}

// TestWiring_A5_HardFailureWithNoTextIsJobFailedAndWaitFailure: the child
// fails outright (its model errors before producing anything) — no
// ErrMaxIterations involved, no text ever written to the sink — so
// agentRunner.run returns the raw error, unwrapped, and the job must end
// Failed with wait() reporting Success:false with an Error.
//
// NB: this does NOT exercise "hit MaxIterations with zero text", because
// that path turns out to be unreachable in the current code — see the
// finding recorded in TestWiring_A5_IterationCapAlwaysProducesSomeText
// below, which pins WHY.
func TestWiring_A5_HardFailureWithNoTextIsJobFailedAndWaitFailure(t *testing.T) {
	reg, _ := withTestWiring(t)

	childFake := connectortest.Failing(errors.New("child model unreachable"))
	providers.Register(&fixedClientProvider{name: "a5-child-prov", client: childFake})

	parentFake := &connectortest.Fake{ProviderName: "a5-parent"}
	parentFake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.ToolCall{ID: "spawn", Name: "subagent", Arguments: `{"task":"this will fail outright","async":true,"model":"a5-child-prov/m"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		if turn == 1 {
			jobID, ok := extractJobID(req)
			if !ok {
				t.Fatalf("no job_id found: %+v", req.Messages)
			}
			return []stream.Event{
				stream.ToolCall{ID: "w", Name: "wait", Arguments: waitToolArgs(jobID, tools.JobMinWaitSeconds)},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		return []stream.Event{stream.TextDelta{Text: "wait result: " + lastToolResultText(req)}, stream.Finish{Reason: "stop"}}
	}

	ctx := connector.WithModelClient(context.Background(), parentFake)
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	sink := &testSink{}
	cfg := agent.Config{MaxRetries: 1, MaxIterations: 10, Tools: toolsAdapter{}, Schema: tools.GetToolsSchemaJSON()}

	if _, err := agent.Run(ctx, parentFake, sink, &msgs, cfg); err != nil {
		t.Fatalf("parent agent.Run: %v", err)
	}

	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 job, got %d", len(list))
	}
	if list[0].Status != jobs.StatusFailed {
		t.Fatalf("job status = %s, want failed (no text was ever produced)", list[0].Status)
	}
	if list[0].Err == "" {
		t.Error("expected a non-empty Err on the failed job")
	}

	// The "wait" tool's job_id path returns Success:false + Error for a
	// Failed job (tools/wait.go), which shows up in the toolResult message
	// as an "Error: ..." prefixed string (see agent/tools_exec.go's
	// appendToolResults: IsError is set from that prefix).
	toolResults := sink.ToolResults()
	var sawWaitError bool
	for _, te := range toolResults {
		if te.name == "wait" && strings.HasPrefix(te.result, "Error:") {
			sawWaitError = true
		}
	}
	if !sawWaitError {
		t.Fatalf("expected the wait tool's result to surface as an error, got %+v", toolResults)
	}
}

// TestWiring_A5_IterationCapAlwaysProducesSomeText is a FINDING, not a
// regression guard for desired behavior: it pins that agent.Run, on hitting
// MaxIterations, unconditionally calls d.Text(...) with a "possible infinite
// loop" warning (agent/agent.go, the `if cfg.MaxIterations > 0` branch right
// before `return totalUsage, ErrMaxIterations`). Because the child's Sink IS
// the text accumulator agentRunner.run reads back, that warning line means
// text is NEVER empty when ErrMaxIterations fires through the normal
// "child kept calling tools" path — so agentRunner.run's
// `if text == "" { return hard error }` branch (the one meant to produce
// "job Failed when cap hit WITHOUT any text", catalog scenario A-5) looks to
// be dead code on this path. It can only be reached some other way (e.g. a
// Sink whose Text() doesn't accumulate — not the case for any real caller
// today). Left here deliberately as documentation; see this session's final
// report for the recommendation to a human (harmless as-is, worth a
// conscious decision on whether the warning line should count as "no
// answer" rather than "some text").
func TestWiring_A5_IterationCapAlwaysProducesSomeText(t *testing.T) {
	withTestWiring(t)

	childFake := alwaysToolCallFake("a5b-child")
	providers.Register(&fixedClientProvider{name: "a5b-child-prov", client: childFake})

	r := &agentRunner{}
	ctx := connector.WithModelClient(context.Background(), childFake)
	maxIter := 2
	text, err := r.run(ctx, "grind forever, no explicit text", "", "be helpful", tools.SubagentOptions{MaxIterations: &maxIter})

	if !errors.Is(err, tools.ErrSubagentTruncated) {
		t.Fatalf("err = %v, want ErrSubagentTruncated — confirming the \"text=='' -> hard fail\" branch was NOT taken despite the child never emitting its own text", err)
	}
	if !strings.Contains(text, "possible infinite loop") {
		t.Fatalf("text = %q, want it to contain agent.Run's iteration-cap warning — that's the text that keeps the \"no text\" branch from ever firing here", text)
	}
}

// =============================================================================
// R-2: lock/unlock through production wiring share ONE registry with async
// children — not accidentally split per-context.
// =============================================================================

func TestWiring_R2_LockSharedBetweenMainThreadAndAsyncChild(t *testing.T) {
	reg, _ := withTestWiring(t)

	// Main thread takes the lock directly against the SAME registry the
	// tools package's "lock" tool uses (lockRegistry, tools/tool.go).
	release, ok, _ := tools.LockRegistry.Acquire(context.Background(), "/r2/path", "main-thread", 0)
	if !ok {
		t.Fatal("main thread failed to acquire its own lock")
	}
	defer release()

	childFake := &connectortest.Fake{ProviderName: "r2-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.ToolCall{ID: "l", Name: "lock", Arguments: `{"path":"/r2/path"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		// Report back what the lock attempt actually said, instead of
		// failing the run outright — a single Error: tool_result does not
		// fail an agent turn by itself (see F-3); only a model that gives
		// up without producing text would fail the job.
		return []stream.Event{stream.TextDelta{Text: lastToolResultText(req)}, stream.Finish{Reason: "stop"}}
	}
	providers.Register(&fixedClientProvider{name: "r2-child-prov", client: childFake})

	parentFake := &connectortest.Fake{ProviderName: "r2-parent"}
	parentFake.Turns = [][]stream.Event{
		{
			stream.ToolCall{ID: "spawn", Name: "subagent", Arguments: `{"task":"try to lock what main holds","async":true,"model":"r2-child-prov/m"}`},
			stream.Finish{Reason: "tool_calls"},
		},
	}

	ctx := connector.WithModelClient(context.Background(), parentFake)
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	sink := &testSink{}
	cfg := agent.Config{MaxRetries: 1, MaxIterations: 3, Tools: toolsAdapter{}, Schema: tools.GetToolsSchemaJSON()}
	if _, err := agent.Run(ctx, parentFake, sink, &msgs, cfg); err != nil {
		t.Fatalf("parent agent.Run: %v", err)
	}

	var jobID string
	for _, j := range reg.List() {
		jobID = j.ID
	}
	if jobID == "" {
		t.Fatal("no job spawned")
	}
	final, ok := reg.Wait(context.Background(), jobID, 2*time.Second)
	if !ok {
		t.Fatal("job vanished")
	}
	// The child's lock attempt must have been rejected against the SAME
	// registry the main thread locked — proving one shared locks.Registry,
	// not one per goroutine/context. The child handled the rejection
	// gracefully (reported it back as its answer), so the job itself is
	// still Done — the conflict shows up in the RESULT, not the job status.
	if final.Status != jobs.StatusDone {
		t.Fatalf("expected the child's job to finish done (it reports the conflict, doesn't crash), got %s: result=%q err=%q", final.Status, final.Result, final.Err)
	}
	if !strings.Contains(final.Result, "already locked") {
		t.Fatalf("expected a lock-conflict error, got %q", final.Err)
	}
}

// =============================================================================
// Q-1: ask/answer round trip through real production wiring — an async
// subagent blocks in "ask", the test (playing "the parent") answers it via
// the real JobRegistry, and the job's own result reflects the exact answer
// text it received back.
// =============================================================================

func TestWiring_Q1_AskAnswerRoundTrip(t *testing.T) {
	reg, _ := withTestWiring(t)
	before := runtime.NumGoroutine()

	childFake := &connectortest.Fake{ProviderName: "q1-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.ToolCall{ID: "ask0", Name: "ask", Arguments: `{"question":"what color?"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		return []stream.Event{
			stream.TextDelta{Text: "answer was: " + lastToolResultText(req)},
			stream.Finish{Reason: "stop"},
		}
	}
	providers.Register(&fixedClientProvider{name: "q1-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	spawnRes := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "ask a question and report the answer", "async": true, "model": "q1-child-prov/child-model",
	})
	if !spawnRes.Success {
		t.Fatalf("spawn subagent: %s", spawnRes.Error)
	}
	m := jobIDPattern.FindStringSubmatch(spawnRes.Content)
	if m == nil {
		t.Fatalf("could not find job_id in spawn result: %q", spawnRes.Content)
	}
	jobID := m[1]

	deadline := time.Now().Add(2 * time.Second)
	for {
		j, ok := snapshotByID(reg, jobID)
		if !ok {
			t.Fatal("job vanished from registry")
		}
		if j.Status == jobs.StatusWaitingAnswer {
			if j.Question != "what color?" {
				t.Fatalf("expected question %q, got %q", "what color?", j.Question)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for job to reach waiting_answer, last status: %s", j.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !reg.Answer(jobID, "blue") {
		t.Fatal("expected Answer to succeed against a job currently waiting")
	}

	final, ok := reg.Wait(context.Background(), jobID, 2*time.Second)
	if !ok {
		t.Fatal("job vanished from registry")
	}
	if final.Status != jobs.StatusDone {
		t.Fatalf("job status = %s (err=%q), want done", final.Status, final.Err)
	}
	if final.Result != "answer was: blue" {
		t.Fatalf("job result = %q, want it to reflect the exact answer text", final.Result)
	}

	waitForGoroutineSettle(t, before)
}

// =============================================================================
// Q-2: an "ask" that's never answered must not hang forever — it unblocks
// via the job's own wall-clock limit (modeled here with a short-deadline ctx
// instead of waiting out the real 600s subagent timeout).
// =============================================================================

func TestWiring_Q2_AskNeverAnsweredUnblocksViaOwnTimeout(t *testing.T) {
	reg, _ := withTestWiring(t)

	release := make(chan struct{})

	job := reg.Start(context.Background(), "never answered", func(ctx context.Context, jobID string) (string, bool, error) {
		<-release
		return "done", false, nil
	})

	// A short-deadline ctx carrying the job's id, exactly the shape the real
	// "ask" tool builds (ctx = the job's own ctx, which the caller controls
	// the deadline of) — exercises AskTool -> jobAsker -> JobRegistry.Ask
	// without waiting out subagent.SubagentTimeoutSec (600s).
	askCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	askCtx = context.WithValue(askCtx, tools.JobIDCtxKey{}, job.ID)

	start := time.Now()
	res := tools.RunTool(askCtx, "ask", map[string]any{"question": "anyone there?"})
	elapsed := time.Since(start)

	if res.Success {
		t.Fatalf("expected ask to fail when never answered, got success: %+v", res)
	}
	if res.Error == "" {
		t.Fatal("expected a non-empty, actionable error message")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("ask took too long to unblock after its ctx deadline: %s", elapsed)
	}

	// Let the job finish (and wait for it) before returning, so
	// withTestWiring's cleanup never races the job's own terminal onEvent
	// call against swapping JobRegistry/jobEventBus back.
	close(release)
	if _, ok := reg.Wait(context.Background(), job.ID, time.Second); !ok {
		t.Fatal("job vanished from registry")
	}
}

// =============================================================================
// P-1: report_progress surfaces via JobRegistry.Get while the job is still
// running, and via wait()'s "still running" message.
// =============================================================================

func TestWiring_P1_ReportProgressVisibleWhileRunning(t *testing.T) {
	reg, _ := withTestWiring(t)

	release := make(chan struct{})
	childFake := &connectortest.Fake{ProviderName: "p1-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.ToolCall{ID: "rp0", Name: "report_progress", Arguments: `{"text":"halfway done"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		<-release
		return []stream.Event{stream.TextDelta{Text: "finished"}, stream.Finish{Reason: "stop"}}
	}
	providers.Register(&fixedClientProvider{name: "p1-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	spawnRes := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "report progress then finish", "async": true, "model": "p1-child-prov/m",
	})
	if !spawnRes.Success {
		t.Fatalf("spawn subagent: %s", spawnRes.Error)
	}
	m := jobIDPattern.FindStringSubmatch(spawnRes.Content)
	if m == nil {
		t.Fatalf("could not find job_id in spawn result: %q", spawnRes.Content)
	}
	jobID := m[1]

	deadline := time.Now().Add(2 * time.Second)
	for {
		j, ok := snapshotByID(reg, jobID)
		if !ok {
			t.Fatal("job vanished from registry")
		}
		if j.Progress == "halfway done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for progress to be recorded, last snapshot: %+v", j)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// wait()'s still-running response must surface the same progress note.
	waitRes := tools.RunTool(context.Background(), "wait", map[string]any{"job_id": jobID, "seconds": 1})
	if !waitRes.Success {
		t.Fatalf("wait: %s", waitRes.Error)
	}
	if !strings.Contains(waitRes.Content, "halfway done") {
		t.Fatalf("expected wait's still-running message to mention the reported progress, got %q", waitRes.Content)
	}

	close(release)

	final, ok := reg.Wait(context.Background(), jobID, 2*time.Second)
	if !ok {
		t.Fatal("job vanished from registry")
	}
	if final.Status != jobs.StatusDone {
		t.Fatalf("job status = %s (err=%q), want done", final.Status, final.Err)
	}
	if final.Progress != "halfway done" {
		t.Fatalf("expected progress to persist after job finished, got %q", final.Progress)
	}
}

// =============================================================================
// U-1 / U-2: "resume" continues a finished async job's conversation as a
// brand-new job, with visible access to the earlier context; a bogus/
// unresumable job_id fails cleanly.
// =============================================================================

func TestWiring_U1_ResumeContinuesWithEarlierContext(t *testing.T) {
	reg, _ := withTestWiring(t)

	childFake := &connectortest.Fake{ProviderName: "u1-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.TextDelta{Text: "the secret number is 42"},
				stream.Finish{Reason: "stop"},
			}
		}
		// This is the resumed call (turn 1 on the SAME Fake instance, since
		// Resume reuses the original job's already-resolved model client).
		// Prove the forked conversation carried the original exchange
		// forward, not just the new task alone.
		sawEarlier := false
		for _, msg := range req.Messages {
			for _, c := range msg.Content {
				if strings.Contains(c.Text, "secret number is 42") {
					sawEarlier = true
				}
			}
		}
		if !sawEarlier {
			t.Errorf("resumed request did not carry the earlier exchange forward: %+v", req.Messages)
		}
		return []stream.Event{
			stream.TextDelta{Text: "yes, it was 42"},
			stream.Finish{Reason: "stop"},
		}
	}
	providers.Register(&fixedClientProvider{name: "u1-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	spawnRes := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "tell me a secret number", "async": true, "model": "u1-child-prov/m",
	})
	if !spawnRes.Success {
		t.Fatalf("spawn subagent: %s", spawnRes.Error)
	}
	m := jobIDPattern.FindStringSubmatch(spawnRes.Content)
	if m == nil {
		t.Fatalf("could not find job_id in spawn result: %q", spawnRes.Content)
	}
	origJobID := m[1]

	orig, ok := reg.Wait(context.Background(), origJobID, 2*time.Second)
	if !ok || orig.Status != jobs.StatusDone {
		t.Fatalf("original job did not finish done: %+v", orig)
	}

	resumeRes := tools.RunTool(context.Background(), "resume", map[string]any{
		"job_id": origJobID, "task": "what was the secret number?",
	})
	if !resumeRes.Success {
		t.Fatalf("resume: %s", resumeRes.Error)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	// The JSON is the first line; the prose after it explains that the resumed
	// job kept its conversation, which is the fact a model most often misses.
	if err := json.Unmarshal([]byte(firstLineOf(resumeRes.Content)), &out); err != nil {
		t.Fatalf("unmarshal resume result %q: %v", resumeRes.Content, err)
	}
	if out.JobID == "" || out.JobID == origJobID {
		t.Fatalf("expected a new distinct job_id, got %q (original %q)", out.JobID, origJobID)
	}

	final, ok := reg.Wait(context.Background(), out.JobID, 2*time.Second)
	if !ok {
		t.Fatal("resumed job vanished from registry")
	}
	if final.Status != jobs.StatusDone {
		t.Fatalf("resumed job status = %s (err=%q), want done", final.Status, final.Err)
	}
	if final.Result != "yes, it was 42" {
		t.Fatalf("resumed job result = %q, want %q", final.Result, "yes, it was 42")
	}

	// Poll it via "wait" too, same as any other async job.
	waitRes := tools.RunTool(context.Background(), "wait", map[string]any{"job_id": out.JobID, "seconds": 1})
	if !waitRes.Success {
		t.Fatalf("wait on resumed job: %s", waitRes.Error)
	}
	if !strings.Contains(waitRes.Content, "yes, it was 42") {
		t.Fatalf("expected wait to surface the resumed job's result, got %q", waitRes.Content)
	}
}

func TestWiring_U2_ResumeUnknownJobIDFailsCleanly(t *testing.T) {
	withTestWiring(t)
	before := runtime.NumGoroutine()

	res := tools.RunTool(context.Background(), "resume", map[string]any{"job_id": "no-such-job", "task": "x"})
	if res.Success {
		t.Fatal("expected resume on an unknown job_id to fail")
	}
	if res.Error == "" {
		t.Fatal("expected a non-empty, clean error message")
	}

	waitForGoroutineSettle(t, before)
}

// fixedClientProviderCheck is a compile-time reminder that fixedClientProvider
// (defined in main_resolve_test.go) implements providers.Provider — if that
// type ever moves or is renamed, this file fails to build loudly instead of
// silently losing coverage.
var _ providers.Provider = (*fixedClientProvider)(nil)

// firstLineOf returns everything before the first newline. Several tool
// results are "machine-readable first line, then advice for the model", so a
// test that wants the data takes the first line rather than the whole thing.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
