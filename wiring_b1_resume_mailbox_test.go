package main

// B1: a resumed job used to drain the WRONG (dead) mailbox, because
// jobResumerAdapter.Resume (btw.go) reused the stashed resumableEntry's
// agent.Config verbatim — including its NextMessages closure, which
// tools.JobMailboxNextMessages had bound to the ORIGINAL job id, not the
// brand-new one Resume actually starts. A message posted to the new job_id
// (via the "message" tool) landed in the registry's mailbox for that new
// id, but the resumed agent loop kept draining the terminal original job's
// (now-empty-forever) mailbox instead — so "message" reported success while
// the child never actually saw it.
//
// These tests drive the fix through the REAL production wiring
// (withTestWiring, exactly like wiring_test.go), the same way the M1 test
// there proves ordinary mailbox delivery to a running async subagent.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// resumeJobID extracts the "job_id" field the "resume" tool's result JSON
// carries on its first line (see ResumeTool.Run).
func resumeJobID(t *testing.T, content string) string {
	t.Helper()
	id, err := parseResumeJobID(content)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return id
}

// parseResumeJobID is resumeJobID's non-fatal half: safe to call from a
// goroutine OTHER than the test's own (t.Fatal/t.Fatalf from a non-test
// goroutine calls runtime.Goexit, which silently kills that goroutine
// without ever sending on a result channel a WaitGroup/select elsewhere is
// blocked on — turning a real parse failure into a test hang instead of a
// reported failure). Callers running off the test goroutine must report
// this error over a channel themselves; see
// TestWiring_B1_ConcurrentResumesOfSameJobDoNotCrossTalk's launch closure.
func parseResumeJobID(content string) (string, error) {
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(firstLineOf(content)), &out); err != nil {
		return "", fmt.Errorf("unmarshal resume result %q: %w", content, err)
	}
	if out.JobID == "" {
		return "", fmt.Errorf("resume result carried no job_id: %q", content)
	}
	return out.JobID, nil
}

// waitForWaitingAnswer polls reg until id is StatusWaitingAnswer, failing
// the test on timeout — mirroring the M1 test's identical wait loop, the
// deterministic point at which posting to that job's mailbox is guaranteed
// to be observed at its next iteration boundary.
func waitForWaitingAnswer(t *testing.T, reg *jobs.Registry, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		j, ok := snapshotByID(reg, id)
		if !ok {
			t.Fatalf("job %s vanished from registry", id)
		}
		if j.Status == jobs.StatusWaitingAnswer {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for job %s to reach waiting_answer, last status: %s", id, j.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWiring_B1_ResumedJobMailboxBoundToNewJobID pins the core fix: a
// message posted to the job_id resume() actually returns must reach that
// resumed agent's next iteration, not silently vanish into the dead
// original job's mailbox.
func TestWiring_B1_ResumedJobMailboxBoundToNewJobID(t *testing.T) {
	reg, _ := withTestWiring(t)

	childFake := &connectortest.Fake{ProviderName: "b1-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		switch turn {
		case 0:
			// The original spawn's only turn.
			return []stream.Event{
				stream.TextDelta{Text: "first answer"},
				stream.Finish{Reason: "stop"},
			}
		case 1:
			// The resumed run's first turn: block in ask_parent so the test
			// can deterministically post to the NEW job's mailbox before
			// releasing it, exactly like TestWiring_M1's synchronization.
			return []stream.Event{
				stream.ToolCall{ID: "ask-resumed", Name: "ask_parent", Arguments: `{"question":"proceed?"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		default:
			// By this turn, the mailbox drain (after the ask_parent turn's
			// runOnce, once answered) must already have appended the posted
			// message as a new user turn — IF it was bound to this run's
			// own new job id rather than the dead original one.
			seen := containsUserText(req, "steer the resumed child")
			text := "did not see the steered message"
			if seen {
				text = "saw the steered message"
			}
			return []stream.Event{
				stream.TextDelta{Text: text},
				stream.Finish{Reason: "stop"},
			}
		}
	}
	providers.Register(&fixedClientProvider{name: "b1-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	spawnRes := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "tell me something", "async": true, "model": "b1-child-prov/m",
	})
	if !spawnRes.Success {
		t.Fatalf("spawn subagent: %s", spawnRes.Error)
	}
	origJobID := jobIDPattern.FindStringSubmatch(spawnRes.Content)[1]

	orig, ok := reg.Wait(context.Background(), origJobID, 2*time.Second)
	if !ok || orig.Status != jobs.StatusDone {
		t.Fatalf("original job did not finish done: %+v", orig)
	}

	resumeRes := tools.RunTool(context.Background(), "resume", map[string]any{
		"job_id": origJobID, "task": "continue please",
	})
	if !resumeRes.Success {
		t.Fatalf("resume: %s", resumeRes.Error)
	}
	newJobID := resumeJobID(t, resumeRes.Content)
	if newJobID == origJobID {
		t.Fatalf("expected a new, distinct job id from resume, got the original back")
	}

	waitForWaitingAnswer(t, reg, newJobID)

	// Post to the NEW job's id — the one the model was actually given.
	msgRes := tools.RunTool(context.Background(), "message", map[string]any{
		"job_id": newJobID, "text": "steer the resumed child",
	})
	if !msgRes.Success {
		t.Fatalf("message: %s", msgRes.Error)
	}

	if !reg.Answer(newJobID, "go ahead", true) {
		t.Fatal("expected Answer to succeed against a job currently waiting")
	}

	final, ok := reg.Wait(context.Background(), newJobID, 2*time.Second)
	if !ok {
		t.Fatal("resumed job vanished from registry")
	}
	if final.Status != jobs.StatusDone {
		t.Fatalf("resumed job status = %s (err=%q), want done", final.Status, final.Err)
	}
	if final.Result != "saw the steered message" {
		t.Fatalf("resumed job result = %q — the message posted to its own new job_id never reached it (drained the dead original job's mailbox instead)", final.Result)
	}
}

// TestWiring_B1_ChainedResumeMailboxBoundToNewestJobID pins the chained
// case: resuming an ALREADY-resumed job must keep rebinding to the newest
// id at every hop, not just the first one. Before the fix this would have
// been correct by accident in some shapes (whichever id happened to be
// stashed) — this test nails down that hop 2's mailbox is bound to hop 2's
// own job id, not hop 1's.
func TestWiring_B1_ChainedResumeMailboxBoundToNewestJobID(t *testing.T) {
	reg, _ := withTestWiring(t)

	childFake := &connectortest.Fake{ProviderName: "b1-chain-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		switch turn {
		case 0:
			// Original spawn.
			return []stream.Event{
				stream.TextDelta{Text: "first"},
				stream.Finish{Reason: "stop"},
			}
		case 1:
			// First resume (hop 1): finishes cleanly, no ask — this is the
			// entry that gets chain-resumed again below.
			return []stream.Event{
				stream.TextDelta{Text: "second"},
				stream.Finish{Reason: "stop"},
			}
		case 2:
			// Second resume (hop 2): block in ask_parent.
			return []stream.Event{
				stream.ToolCall{ID: "ask-hop2", Name: "ask_parent", Arguments: `{"question":"proceed?"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		default:
			seen := containsUserText(req, "steer hop two")
			text := "hop2 did not see the steered message"
			if seen {
				text = "hop2 saw the steered message"
			}
			return []stream.Event{
				stream.TextDelta{Text: text},
				stream.Finish{Reason: "stop"},
			}
		}
	}
	providers.Register(&fixedClientProvider{name: "b1-chain-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	spawnRes := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "start", "async": true, "model": "b1-chain-child-prov/m",
	})
	if !spawnRes.Success {
		t.Fatalf("spawn subagent: %s", spawnRes.Error)
	}
	jobID0 := jobIDPattern.FindStringSubmatch(spawnRes.Content)[1]
	if _, ok := reg.Wait(context.Background(), jobID0, 2*time.Second); !ok {
		t.Fatal("original job vanished")
	}

	resume1 := tools.RunTool(context.Background(), "resume", map[string]any{"job_id": jobID0, "task": "hop one"})
	if !resume1.Success {
		t.Fatalf("resume hop1: %s", resume1.Error)
	}
	jobID1 := resumeJobID(t, resume1.Content)
	hop1, ok := reg.Wait(context.Background(), jobID1, 2*time.Second)
	if !ok || hop1.Status != jobs.StatusDone {
		t.Fatalf("hop1 job did not finish done: %+v", hop1)
	}

	resume2 := tools.RunTool(context.Background(), "resume", map[string]any{"job_id": jobID1, "task": "hop two"})
	if !resume2.Success {
		t.Fatalf("resume hop2: %s", resume2.Error)
	}
	jobID2 := resumeJobID(t, resume2.Content)
	if jobID2 == jobID1 || jobID2 == jobID0 {
		t.Fatalf("expected a third distinct job id, got %q (hop0=%q hop1=%q)", jobID2, jobID0, jobID1)
	}

	waitForWaitingAnswer(t, reg, jobID2)

	msgRes := tools.RunTool(context.Background(), "message", map[string]any{
		"job_id": jobID2, "text": "steer hop two",
	})
	if !msgRes.Success {
		t.Fatalf("message to hop2: %s", msgRes.Error)
	}
	if !reg.Answer(jobID2, "go ahead", true) {
		t.Fatal("expected Answer to succeed against hop2, which is waiting")
	}

	final, ok := reg.Wait(context.Background(), jobID2, 2*time.Second)
	if !ok {
		t.Fatal("hop2 job vanished from registry")
	}
	if final.Status != jobs.StatusDone {
		t.Fatalf("hop2 job status = %s (err=%q), want done", final.Status, final.Err)
	}
	if final.Result != "hop2 saw the steered message" {
		t.Fatalf("hop2 result = %q — the chained resume's mailbox was not bound to its own newest job id", final.Result)
	}
}

// TestWiring_B1_ConcurrentResumesOfSameJobDoNotCrossTalk resumes the SAME
// original job twice, concurrently, and checks that:
//   - each new job's mailbox delivers only the message posted to ITS OWN
//     job id (no cross-talk between the two resumed conversations), and
//   - doing this concurrently is race-free (run this file's tests with
//     `-race`) — i.e. jobResumerAdapter.Resume must never mutate the shared
//     stashed resumableEntry.cfg in place, only ever a fresh local copy,
//     since the same entry can be looked up and resumed more than once.
func TestWiring_B1_ConcurrentResumesOfSameJobDoNotCrossTalk(t *testing.T) {
	reg, _ := withTestWiring(t)

	childFake := &connectortest.Fake{ProviderName: "b1-dual-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			// The original spawn's only turn.
			return []stream.Event{
				stream.TextDelta{Text: "first answer"},
				stream.Finish{Reason: "stop"},
			}
		}
		// Every other turn belongs to one of the two resumed runs. Branch on
		// CONTENT, not turn index, since the two resumed runs' turns can
		// interleave in either order: a request that carries no answer yet
		// is that run's first turn (block in ask_parent); once answered,
		// decide by which steer marker (if any) the mailbox drain appended.
		if lastToolResultText(req) == "" {
			return []stream.Event{
				stream.ToolCall{ID: fmt.Sprintf("ask-%d", turn), Name: "ask_parent", Arguments: `{"question":"proceed?"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		marker := "neither"
		switch {
		case containsUserText(req, "steerA"):
			marker = "A"
		case containsUserText(req, "steerB"):
			marker = "B"
		}
		return []stream.Event{
			stream.TextDelta{Text: "got " + marker},
			stream.Finish{Reason: "stop"},
		}
	}
	providers.Register(&fixedClientProvider{name: "b1-dual-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	spawnRes := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "start", "async": true, "model": "b1-dual-child-prov/m",
	})
	if !spawnRes.Success {
		t.Fatalf("spawn subagent: %s", spawnRes.Error)
	}
	origJobID := jobIDPattern.FindStringSubmatch(spawnRes.Content)[1]
	if _, ok := reg.Wait(context.Background(), origJobID, 2*time.Second); !ok {
		t.Fatal("original job vanished")
	}

	// Resume the SAME original job id twice, concurrently.
	type resumeOutcome struct {
		jobID string
		err   error
	}
	results := make(chan resumeOutcome, 2)
	launch := func(task string) {
		res := tools.RunTool(context.Background(), "resume", map[string]any{"job_id": origJobID, "task": task})
		if !res.Success {
			results <- resumeOutcome{err: fmt.Errorf("resume(%q): %s", task, res.Error)}
			return
		}
		// Use the non-fatal parseResumeJobID here, NOT resumeJobID(t, ...):
		// this closure runs on its own goroutine (via go launch(...) below),
		// and t.Fatalf from a non-test goroutine would call runtime.Goexit
		// without ever sending on results — turning a malformed resume
		// result into a 10-minute test-package timeout with no message
		// instead of a reported failure.
		id, err := parseResumeJobID(res.Content)
		if err != nil {
			results <- resumeOutcome{err: fmt.Errorf("resume(%q): %w", task, err)}
			return
		}
		results <- resumeOutcome{jobID: id}
	}
	go launch("go A")
	go launch("go B")

	var jobIDs []string
	for i := 0; i < 2; i++ {
		out := <-results
		if out.err != nil {
			t.Fatal(out.err)
		}
		jobIDs = append(jobIDs, out.jobID)
	}
	jobA, jobB := jobIDs[0], jobIDs[1]
	if jobA == jobB {
		t.Fatalf("expected two distinct resumed job ids, got the same one twice: %q", jobA)
	}

	waitForWaitingAnswer(t, reg, jobA)
	waitForWaitingAnswer(t, reg, jobB)

	if res := tools.RunTool(context.Background(), "message", map[string]any{"job_id": jobA, "text": "steerA"}); !res.Success {
		t.Fatalf("message to A: %s", res.Error)
	}
	if res := tools.RunTool(context.Background(), "message", map[string]any{"job_id": jobB, "text": "steerB"}); !res.Success {
		t.Fatalf("message to B: %s", res.Error)
	}
	if !reg.Answer(jobA, "ok", true) {
		t.Fatal("expected Answer to succeed against A")
	}
	if !reg.Answer(jobB, "ok", true) {
		t.Fatal("expected Answer to succeed against B")
	}

	finalA, ok := reg.Wait(context.Background(), jobA, 2*time.Second)
	if !ok || finalA.Status != jobs.StatusDone {
		t.Fatalf("job A did not finish done: %+v", finalA)
	}
	finalB, ok := reg.Wait(context.Background(), jobB, 2*time.Second)
	if !ok || finalB.Status != jobs.StatusDone {
		t.Fatalf("job B did not finish done: %+v", finalB)
	}
	if finalA.Result != "got A" {
		t.Errorf("job A result = %q, want %q (no cross-talk, own mailbox delivered)", finalA.Result, "got A")
	}
	if finalB.Result != "got B" {
		t.Errorf("job B result = %q, want %q (no cross-talk, own mailbox delivered)", finalB.Result, "got B")
	}
}
