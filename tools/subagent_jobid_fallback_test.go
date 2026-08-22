package tools

// Item 17: every child gets a job id, in every mode — including the
// fallback path SubagentTool.Run takes when jobStarter is wired but the
// mode cannot hand a blocking call's children to the background (no
// SetBackgroundBashEnabled — `tyci run` / `--print`; see main.go/
// commands.go). Before the fix, that branch fell straight to plain
// runTasks with no job id at all, so report_progress/ask/wait all refused
// to work on these children.
//
// ask is the one exception, deliberately: giving these children a job id
// must not make ask BLOCK for its full SubagentTimeoutSec with no way for
// an answer to ever arrive (see AskUnroutableCtxKey in ask.go) — a
// `tyci run` invocation never drains JobNotices and the blocking "subagent"
// tool call here never returns until every child finishes, so nobody is
// ever free to call "answer".

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// printModeEnv wires a real job registry the same way main() does, but
// deliberately leaves background bash (and therefore backgroundAllowed)
// off — the one flag `tyci run` / `--print` never turns on. jobs.Registry
// itself satisfies JobAsker/JobAnswerer/JobProgressReporter structurally
// (same shapes, no adapter needed), same as testJobStarter wraps it for
// JobStarter.
func printModeEnv(t *testing.T) *jobs.Registry {
	t.Helper()
	reg := jobs.NewRegistry()
	SetJobStarter(testJobStarter{reg})
	SetJobAsker(reg)
	SetJobAnswerer(reg)
	SetJobProgressReporter(reg)
	SetBackgroundBashEnabled(false)
	t.Cleanup(func() {
		SetJobStarter(nil)
		SetJobAsker(nil)
		SetJobAnswerer(nil)
		SetJobProgressReporter(nil)
		SetBackgroundBashEnabled(false)
	})
	return reg
}

// TestPrintModeChildIsStillRegisteredAsAJob is the core of item 17: a
// blocking subagent call in a mode with no handoff (jobStarter wired,
// backgroundAllowed false) must still register its child through
// jobStarter — not silently fall back to the old no-job-id runTasks path —
// even though it never hands the child to the background.
func TestPrintModeChildIsStillRegisteredAsAJob(t *testing.T) {
	reg := printModeEnv(t)
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(context.Context, string, string, SubagentOptions) (string, error) {
			return "the answer", nil
		},
	}}

	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "do it"})
	if !res.Success || res.Content != "the answer" {
		t.Fatalf("unexpected result: %+v", res)
	}

	regJobs := reg.List()
	if len(regJobs) != 1 {
		t.Fatalf("expected the child to be registered as exactly one job, got %d: %+v", len(regJobs), regJobs)
	}
	job := waitTerminal(t, reg, regJobs[0].ID)
	if job.Status != jobs.StatusDone {
		t.Fatalf("expected the job to finish done, got %s: %+v", job.Status, job)
	}
	if job.Result != "the answer" {
		t.Fatalf("expected job.Result to carry the child's answer, got %q", job.Result)
	}
}

// TestPrintModeChildCanReportProgress guards the same fix from
// report_progress's side: it needs a job id, which this mode did not
// provide before. The tool.Run call above only returns once every spawned
// child's job.done has fired (spawn's finish() runs before that close, see
// waitTerminal's doc comment), so reading reportRes without extra
// synchronization here is safe — that ordering is what makes it safe.
func TestPrintModeChildCanReportProgress(t *testing.T) {
	printModeEnv(t)
	var reportRes ToolResult
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _, _ string, _ SubagentOptions) (string, error) {
			reportRes = (&ReportProgressTool{}).Run(ctx, map[string]any{"text": "halfway"})
			return "done", nil
		},
	}}

	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "do it"})
	if !res.Success {
		t.Fatalf("subagent call failed: %s", res.Error)
	}
	if !reportRes.Success {
		t.Fatalf("report_progress failed inside a print-mode child: %s", reportRes.Error)
	}
}

// TestAskFailsFastWhenTheSpawningCallCannotHandOff is blocker (a) from
// review: giving these children a job id must not make "ask" pass its
// job-id gate and then block for its whole timeout on the one path where an
// answer can structurally never arrive (the "subagent" tool call here does
// not return until the child itself finishes — there is no handoff to free
// it up, and no drained JobNotices either). It must fail immediately,
// exactly as it did before this job id existed at all.
func TestAskFailsFastWhenTheSpawningCallCannotHandOff(t *testing.T) {
	printModeEnv(t)
	var askRes ToolResult
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _, _ string, _ SubagentOptions) (string, error) {
			askRes = (&AskTool{}).Run(ctx, map[string]any{"question": "what color?"})
			return "done", nil
		},
	}}

	done := make(chan struct{})
	go func() {
		tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "ask something"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ask blocked instead of failing fast — nobody in this mode could ever answer it")
	}

	if askRes.Success {
		t.Fatalf("expected ask to fail fast, got success: %+v", askRes)
	}
	if !strings.Contains(askRes.Error, "cannot get an answer") {
		t.Fatalf("unexpected ask error: %q", askRes.Error)
	}
}

// TestNoJobRegistryStillHasNoJobID is the other side of the fix: with no
// job registry at all (jobStarter == nil — only reachable in tests; main.go
// always wires one), there is genuinely no id to hand out, so ask/
// report_progress must still refuse with "no job id", exactly as before.
func TestNoJobRegistryStillHasNoJobID(t *testing.T) {
	SetJobStarter(nil)
	SetBackgroundBashEnabled(false)
	t.Cleanup(func() { SetBackgroundBashEnabled(false) })

	var askRes, reportRes ToolResult
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _, _ string, _ SubagentOptions) (string, error) {
			askRes = (&AskTool{}).Run(ctx, map[string]any{"question": "what color?"})
			reportRes = (&ReportProgressTool{}).Run(ctx, map[string]any{"text": "halfway"})
			return "done", nil
		},
	}}

	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "do it"})
	if !res.Success {
		t.Fatalf("subagent call failed: %s", res.Error)
	}
	if askRes.Success || !strings.Contains(askRes.Error, "no job id") {
		t.Fatalf("expected ask to fail with 'no job id', got: %+v", askRes)
	}
	if reportRes.Success || !strings.Contains(reportRes.Error, "no job id") {
		t.Fatalf("expected report_progress to fail with 'no job id', got: %+v", reportRes)
	}
}
