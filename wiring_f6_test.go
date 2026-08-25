package main

// F6 (review round 2, held-back finding 2): three invariants this commit's
// own suite left unpinned — one of which (b) is exactly how F1's
// pruneResumableLocked race slipped past a green `-race` run in round 1.

import (
	"context"
	"fmt"
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

// floodRegistryPastTerminalCap starts and waits out enough quick, terminal
// filler jobs to push every job finished before this call out of reg's live
// jobs map (jobs.MaxRetainedTerminalJobs retains only the most recently
// finished ones, regardless of Kind — see pruneTerminalLocked). Used to
// force a specific earlier job into "pruned from the registry, but still
// resumable / (if it was a subagent) tombstoned" territory.
func floodRegistryPastTerminalCap(t *testing.T, reg *jobs.Registry, label string) {
	t.Helper()
	for i := 0; i < jobs.MaxRetainedTerminalJobs+5; i++ {
		job := reg.Start(context.Background(), fmt.Sprintf("%s filler %d", label, i), jobs.KindBash, "", func(context.Context, string) (string, bool, error) {
			return "filler", false, nil
		})
		if _, ok := reg.Wait(context.Background(), job.ID, 2*time.Second); !ok {
			t.Fatalf("filler job %d vanished while waiting for it to finish", i)
		}
	}
}

// TestWiring_F6a_ResumeSurvivesJobsRegistryPruning pins the one
// cross-feature invariant this whole commit rests on: `resumable` (main.go)
// is a SEPARATE pool from jobs.Registry's own terminal-job retention, with
// its own, larger cap (resumableCap, 200) — specifically so resume(job_id=
// ...) keeps working on a job long after jobs.Registry itself has pruned
// (and, since it is a subagent, tombstoned) that same id. The assertion
// checks the CAP RELATIONSHIP, not just today's two numbers, so a future
// change to either cap that breaks the relationship fails this test
// instead of silently breaking resume.
func TestWiring_F6a_ResumeSurvivesJobsRegistryPruning(t *testing.T) {
	if resumableCap <= jobs.MaxRetainedTerminalJobs {
		t.Fatalf("resumableCap (%d) must exceed jobs.MaxRetainedTerminalJobs (%d): resumable's whole reason to exist as a SEPARATE pool is to outlive the registry's own terminal-job retention, so resume(job_id=...) keeps working on a job the registry has already pruned", resumableCap, jobs.MaxRetainedTerminalJobs)
	}

	reg, _ := withTestWiring(t)

	childFake := &connectortest.Fake{ProviderName: "f6a-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.TextDelta{Text: "the secret is 99"},
				stream.Finish{Reason: "stop"},
			}
		}
		// The resumed call: prove it is genuinely continuing the SAME
		// conversation, not a fresh one — resumable, not just "resume
		// technically returns success".
		sawEarlier := false
		for _, msg := range req.Messages {
			for _, c := range msg.Content {
				if strings.Contains(c.Text, "secret is 99") {
					sawEarlier = true
				}
			}
		}
		if !sawEarlier {
			t.Errorf("resumed request did not carry the earlier exchange forward: %+v", req.Messages)
		}
		return []stream.Event{
			stream.TextDelta{Text: "yes, 99"},
			stream.Finish{Reason: "stop"},
		}
	}
	providers.Register(&fixedClientProvider{name: "f6a-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	spawnRes := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "tell me a secret", "async": true, "model": "f6a-child-prov/m",
	})
	if !spawnRes.Success {
		t.Fatalf("spawn subagent: %s", spawnRes.Error)
	}
	origJobID := jobIDPattern.FindStringSubmatch(spawnRes.Content)[1]
	if _, ok := reg.Wait(context.Background(), origJobID, 2*time.Second); !ok {
		t.Fatal("original job vanished")
	}

	// Push it out of the registry's own live map. It is a subagent job, so
	// it is tombstoned too (B3a) — but that is a DIFFERENT mechanism from
	// resumable, and this test is specifically about resumable surviving on
	// its own, independent of what the registry did with its copy.
	floodRegistryPastTerminalCap(t, reg, "f6a")
	if _, ok := reg.Get(origJobID); ok {
		t.Fatalf("expected the original job to have been pruned from the registry's live map by now")
	}

	resumeRes := tools.RunTool(context.Background(), "resume", map[string]any{
		"job_id": origJobID, "task": "what was the secret?",
	})
	if !resumeRes.Success {
		t.Fatalf("resume on a job already pruned from the registry: %s", resumeRes.Error)
	}
	newJobID := resumeJobID(t, resumeRes.Content)

	final, ok := reg.Wait(context.Background(), newJobID, 2*time.Second)
	if !ok || final.Status != jobs.StatusDone {
		t.Fatalf("resumed job did not finish done: %+v", final)
	}
	if final.Result != "yes, 99" {
		t.Fatalf("resumed job result = %q, want %q", final.Result, "yes, 99")
	}
}

// TestWiring_F6c_WaitToolReturnsTombstonedSubagentResultPromptly drives the
// tombstone fallback (B3a) through the REAL production path: the "wait"
// tool -> jobWaiterAdapter -> jobs.Registry.Wait, not just jobs.Registry
// directly. This matters because WaitTool.waitForJob (tools/wait.go) LOOPS
// on Wait in jobPollInterval slices until it sees Done or Waiting — a
// tombstone fallback that ever came back with Done=false would make the
// tool spin hot for its full (up to 30 minute) timeout instead of returning
// immediately with the real result.
func TestWiring_F6c_WaitToolReturnsTombstonedSubagentResultPromptly(t *testing.T) {
	reg, _ := withTestWiring(t)

	childFake := &connectortest.Fake{ProviderName: "f6c-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		return []stream.Event{
			stream.TextDelta{Text: "the tombstoned answer"},
			stream.Finish{Reason: "stop"},
		}
	}
	providers.Register(&fixedClientProvider{name: "f6c-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	spawnRes := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "answer", "async": true, "model": "f6c-child-prov/m",
	})
	if !spawnRes.Success {
		t.Fatalf("spawn subagent: %s", spawnRes.Error)
	}
	jobID := jobIDPattern.FindStringSubmatch(spawnRes.Content)[1]
	if _, ok := reg.Wait(context.Background(), jobID, 2*time.Second); !ok {
		t.Fatal("job vanished while waiting for it to finish")
	}

	floodRegistryPastTerminalCap(t, reg, "f6c")
	if _, ok := reg.Get(jobID); ok {
		t.Fatalf("expected the job to have been pruned from the registry's live map by now")
	}

	start := time.Now()
	waitRes := tools.RunTool(context.Background(), "wait", map[string]any{
		"job_id": jobID, "seconds": tools.JobMinWaitSeconds,
	})
	elapsed := time.Since(start)

	if !waitRes.Success {
		t.Fatalf("wait on a pruned-but-tombstoned job: %s", waitRes.Error)
	}
	if !strings.Contains(waitRes.Content, "the tombstoned answer") {
		t.Fatalf("wait result = %q, want it to carry the tombstoned result", waitRes.Content)
	}
	// A tool that had to loop out its full (clamped-up-to) timeout would
	// take on the order of tools.JobMinWaitSeconds; returning near-instantly
	// is what proves the very first Wait() call already saw Done=true, not
	// a spin that happened to get lucky.
	if elapsed > 2*time.Second {
		t.Fatalf("wait took %v to return a tombstoned result — it should return on its first poll, not loop", elapsed)
	}
}
