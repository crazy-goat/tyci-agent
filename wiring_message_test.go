package main

// M-1: a message posted to a running async subagent's mailbox is delivered
// at that job's own agent loop's next iteration boundary — the same
// mechanism agent.Config.NextMessages already provides for the main agent,
// wired per-job via tools.JobMailboxNextMessages (see agentRunner.run in
// main.go). Mirrors TestWiring_Q1_AskAnswerRoundTrip's synchronization
// trick: turn 0 blocks inside "ask_parent" so the test can deterministically
// post to the job's mailbox and confirm delivery through the REAL
// production wiring, before releasing it.

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

func TestWiring_M1_MessagePostedWhileRunningReachesNextIteration(t *testing.T) {
	reg, _ := withTestWiring(t)
	before := runtime.NumGoroutine()

	childFake := &connectortest.Fake{ProviderName: "m1-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.ToolCall{ID: "ask0", Name: "ask_parent", Arguments: `{"question":"proceed?"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		// By turn 1, the mailbox drain (after turn 0's runOnce) must
		// already have appended the posted message as a new user turn.
		return []stream.Event{
			stream.TextDelta{Text: "saw " + strconv.Itoa(countUserMessages(req)) + " user messages"},
			stream.Finish{Reason: "stop"},
		}
	}
	providers.Register(&fixedClientProvider{name: "m1-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	spawnRes := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "wait to be steered", "async": true, "model": "m1-child-prov/child-model",
	})
	if !spawnRes.Success {
		t.Fatalf("spawn subagent: %s", spawnRes.Error)
	}
	m := jobIDPattern.FindStringSubmatch(spawnRes.Content)
	if m == nil {
		t.Fatalf("could not find job_id in spawn result: %q", spawnRes.Content)
	}
	jobID := m[1]

	// Wait for turn 0's ask_parent to block, exactly as Q1 does — this is
	// the deterministic point at which posting to the mailbox is guaranteed
	// to land before turn 0's runOnce finishes and the drain runs.
	deadline := time.Now().Add(2 * time.Second)
	for {
		j, ok := snapshotByID(reg, jobID)
		if !ok {
			t.Fatal("job vanished from registry")
		}
		if j.Status == jobs.StatusWaitingAnswer {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for job to reach waiting_answer, last status: %s", j.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !reg.Post(jobID, "steer this way") {
		t.Fatal("expected Post to succeed against a known, running job")
	}
	// Also exercise the short-id form Resolve supports, matching what the
	// "message" tool / "/msg" slash command actually do before posting.
	if _, ok := reg.Resolve("#" + jobs.ShortID(jobID)); !ok {
		t.Fatalf("expected Resolve to find job %s via its short id", jobID)
	}

	if !reg.Answer(jobID, "yes", true) {
		t.Fatal("expected Answer to succeed against a job currently waiting")
	}

	final, ok := reg.Wait(context.Background(), jobID, 2*time.Second)
	if !ok {
		t.Fatal("job vanished from registry")
	}
	if final.Status != jobs.StatusDone {
		t.Fatalf("job status = %s (err=%q), want done", final.Status, final.Err)
	}
	if !strings.Contains(final.Result, "saw 2 user messages") {
		t.Fatalf("job result = %q, want it to reflect the mailbox message having been appended as a second user turn", final.Result)
	}

	reqs := childFake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 model calls (turn 0 + turn 1 after the drain), got %d", len(reqs))
	}
	if !containsUserText(reqs[1], "steer this way") {
		t.Fatalf("turn 1's request does not contain the posted message; messages: %#v", reqs[1].Messages)
	}

	waitForGoroutineSettle(t, before)
}

// countUserMessages counts user-role messages in req.Messages — the initial
// task plus one per drained mailbox message.
func countUserMessages(req connector.Request) int {
	n := 0
	for _, m := range req.Messages {
		if m.Role == "user" {
			n++
		}
	}
	return n
}

// containsUserText reports whether any user-role message in req carries
// text exactly matching want.
func containsUserText(req connector.Request, want string) bool {
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		for _, c := range m.Content {
			if c.Text == want {
				return true
			}
		}
	}
	return false
}

// TestWiring_M2_PostToUnknownJobFails guards the negative path through the
// real wiring: posting to a job id nothing in the registry recognizes
// (never existed, or already pruned) must fail rather than silently
// succeed — a caller (the "message" tool, or "/msg") needs to know its
// message was not queued anywhere.
func TestWiring_M2_PostToUnknownJobFails(t *testing.T) {
	reg, _ := withTestWiring(t)
	if reg.Post("job-does-not-exist-1", "hi") {
		t.Fatal("expected Post to fail for an unknown job id")
	}
	if _, ok := reg.Resolve("job-does-not-exist-1"); ok {
		t.Fatal("expected Resolve to fail for an unknown job id")
	}
}
