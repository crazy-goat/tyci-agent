package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/stream"
)

// TestBashBackgroundedProgress_SurfacedByWaitAsHistorySequence covers review
// E6's first gap: the dominant real-world source of progress notes is NOT
// an explicit "report_progress" call, it is a backgrounded shell command's
// own output (bashRun.setProgress in bash.go, throttled to at most one
// note per bgProgressInterval — see its doc comment), and that path had no
// test at all before this. It runs a real backgrounded command that prints
// several distinct lines a bit more than a second apart (bgProgressInterval
// is 1s), then calls the real WaitTool — through a local mirror of
// jobWaiterAdapter (btw.go's own adapter lives in package main, which this
// package must not import — see JobWaiter's own doc comment) — and checks
// that more than one distinct line survives into the still-running
// response, newline-separated rather than run together.
//
// This intentionally does not try to exceed the registry's retention cap
// (progressHistoryCap in jobs/registry.go, unexported and not worth
// exporting just for this): at one note per second minimum, doing that
// here would need >20 real seconds just to populate the history, on top of
// the ~30s a still-running wait() call already costs (JobMinWaitSeconds —
// see TestWaitTool_JobIDStillRunning and its siblings in wait_test.go,
// which already pay that same cost). Cap eviction itself is covered
// deterministically, without any wall-clock dependency, by
// jobs.TestSetProgress_CapEvictsOldestAndMarksTruncated and by
// TestJobWaiterAdapter_TranslatesProgressHistoryAndTruncation in
// btw_test.go (package main), both of which drive SetProgress directly
// past the cap. What this test alone proves is that the real bash-output
// pipeline (process -> pump -> bashRun.setProgress -> registry -> adapter
// -> wait()) actually carries more than the latest note end to end.
func TestBashBackgroundedProgress_SurfacedByWaitAsHistorySequence(t *testing.T) {
	reg, _ := bgTestEnv(t)

	// bash.go's pump only calls bashRun.setProgress when a per-tool-call
	// stream.Output func is present in ctx (see bash.go's runCommand: with
	// none, output goes straight to the buffer with no line-by-line pump at
	// all) — the same context every real tool call actually gets from the
	// agent loop. Without this, the command would still background and
	// finish fine, but silently never post a single progress note, which is
	// exactly the gap this test exists to catch.
	ctx := stream.WithOutput(context.Background(), func(int, string) {})
	res := (&BashTool{}).Run(ctx, map[string]any{
		"command":           "for i in 1 2 3 4 5; do echo \"progress line $i\"; sleep 1.1; done; sleep 40",
		"description":       "chatty backgrounded command",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	id := jobIDFromResult(t, res.Content)

	// Poll the registry directly (not through wait()) until at least two
	// distinct progress lines have been recorded, so the WaitTool call
	// below is not spent on notes that simply haven't arrived yet.
	deadline := time.Now().Add(15 * time.Second)
	var seen []string
	for {
		found := false
		for _, snap := range reg.List() {
			if snap.ID == id {
				seen = snap.ProgressHistory
				found = true
			}
		}
		if !found {
			t.Fatalf("job %s vanished from the registry", id)
		}
		if len(seen) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for at least 2 progress notes, got %v", seen)
		}
		time.Sleep(100 * time.Millisecond)
	}

	waitTool := &WaitTool{Waiter: regJobWaiterForBashTest{reg: reg}}
	waitRes := waitTool.Run(context.Background(), map[string]any{"seconds": 30, "job_id": id})
	if !waitRes.Success {
		t.Fatalf("expected wait() to succeed while job is still running, got error: %s", waitRes.Error)
	}
	if !strings.Contains(waitRes.Content, "still running") {
		t.Fatalf("expected a still-running response, got: %q", waitRes.Content)
	}

	distinctLines := 0
	for i := 1; i <= 5; i++ {
		if strings.Contains(waitRes.Content, fmt.Sprintf("progress line %d", i)) {
			distinctLines++
		}
	}
	if distinctLines < 2 {
		t.Fatalf("expected at least 2 distinct progress lines in the still-running response, got %d in: %q", distinctLines, waitRes.Content)
	}
	// Sequence, not a single collapsed sentence: bulleted lines separated
	// by real newlines, not glued together with " -> " (review E1 #1 — a
	// backgrounded command's lines are the dominant case a " -> "-joined
	// single line would have made hardest to read).
	if !strings.Contains(waitRes.Content, "- progress line") {
		t.Fatalf("expected bulleted entries in the rendered history, got: %q", waitRes.Content)
	}
	if strings.Contains(waitRes.Content, "progress line 1 -> progress line") {
		t.Fatalf("history must not be \" -> \"-joined: %q", waitRes.Content)
	}
}

// regJobWaiterForBashTest is a local, test-only mirror of btw.go's
// jobWaiterAdapter (package main, which this package must not import — see
// JobWaiter's own doc comment). It copies every JobStatus field, so it does
// not diverge in what it carries — but being a mirror it can never catch a
// regression in the real adapter. That is deliberate, not an oversight:
// this test's job is the bash -> registry -> renderProgressHistory pipeline,
// and the production adapters are covered separately by btw_test.go's
// TestJobWaiterAdapter_TranslatesProgressHistoryAndTruncation and its
// observer twin, which drive the real jobWaiterAdapter/jobObserverAdapter.
type regJobWaiterForBashTest struct{ reg *jobs.Registry }

func (a regJobWaiterForBashTest) Wait(ctx context.Context, id string, timeout time.Duration) (JobStatus, bool) {
	job, ok := a.reg.Wait(ctx, id, timeout)
	if !ok {
		return JobStatus{}, false
	}
	return JobStatus{
		ID:                       job.ID,
		Done:                     job.Status != jobs.StatusRunning && job.Status != jobs.StatusWaitingAnswer,
		Success:                  job.Status == jobs.StatusDone || job.Status == jobs.StatusTruncated,
		Content:                  job.Result,
		Error:                    job.Err,
		Waiting:                  job.Status == jobs.StatusWaitingAnswer,
		Question:                 job.Question,
		Progress:                 job.Progress,
		ProgressHistory:          job.ProgressHistory,
		ProgressHistoryTruncated: job.ProgressHistoryTruncated,
	}, true
}
