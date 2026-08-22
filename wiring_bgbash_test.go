package main

// Integration coverage for background shell commands, driven through the real
// composition-root wiring (wireTools, JobRegistry, JobNotices) rather than
// package-local fakes — the unit tests in tools/bash_bg_test.go already cover
// the tool's own behaviour, so what matters here is the seam between the
// tool, the job registry, and the two paths a completion notice reaches the
// model by.
//
// Same harness rules as wiring_test.go: shared package-level state, so no
// t.Parallel.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/tools"
)

// enableBackgroundBash turns the feature on for one test the way an
// interactive mode does, and cleans up any survivors afterwards.
func enableBackgroundBash(t *testing.T) {
	t.Helper()
	tools.SetBackgroundBashEnabled(true)
	t.Cleanup(func() {
		tools.KillAllBackgroundBash()
		tools.SetBackgroundBashEnabled(false)
	})
}

func bgJobID(t *testing.T, content string) string {
	t.Helper()
	const marker = `job_id="`
	i := strings.Index(content, marker)
	if i < 0 {
		t.Fatalf("no job_id in handoff message: %q", content)
	}
	rest := content[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("unterminated job_id in handoff message: %q", content)
	}
	return rest[:j]
}

// TestWiring_BG1_BackgroundCommandNoticeReachesTheAgentLoop is the end-to-end
// path the whole feature exists for: a command is backgrounded, the tool call
// returns immediately, and when the command finishes its notice is delivered
// to the model through the same NextMessages drain the TUI composes.
func TestWiring_BG1_BackgroundCommandNoticeReachesTheAgentLoop(t *testing.T) {
	reg, _ := withTestWiring(t)
	enableBackgroundBash(t)

	res := tools.RunTool(context.Background(), "bash", map[string]any{
		"command":           "sleep 0.3; echo bg-done",
		"description":       "the slow one",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected a successful handoff, got error: %s", res.Error)
	}
	id := bgJobID(t, res.Content)

	// The registry is the durable record: the notice is only a pointer to it.
	job, ok := reg.Wait(context.Background(), id, 5*time.Second)
	if !ok {
		t.Fatalf("job %q unknown to the shared registry", id)
	}
	if job.Result != "bg-done" {
		t.Fatalf("expected the command's output in the job result, got %q", job.Result)
	}

	// notify() runs before the job function returns, so by the time Wait has
	// observed the job as finished the notice is already queued — no polling
	// needed here.
	select {
	case <-JobNotices.Signal():
	default:
		t.Fatal("no wakeup signal armed; an idle REPL would never start a turn for this")
	}

	// This is the callback the TUI installs as agent.Config.NextMessages.
	nextMessages := mergeNextMessages(nil, JobNotices.Drain)
	pending := nextMessages()
	if len(pending) != 1 {
		t.Fatalf("expected exactly one pending message, got %d: %v", len(pending), pending)
	}
	notice := pending[0]
	for _, want := range []string{"the slow one", "exit 0", id} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice %q is missing %q", notice, want)
		}
	}
	// The notice must stay a pointer to the output, not the output itself:
	// it is injected without the model asking and lives in the history for
	// the rest of the session.
	if strings.Contains(notice, "bg-done") {
		t.Fatalf("notice should summarise, not carry the output: %q", notice)
	}

	if again := nextMessages(); len(again) != 0 {
		t.Fatalf("notice was delivered twice: %v", again)
	}
}

// TestWiring_BG2_UserLineIsDeliveredBeforeBackgroundNotice pins the ordering
// mergeNextMessages promises: what the user actually typed comes first.
func TestWiring_BG2_UserLineIsDeliveredBeforeBackgroundNotice(t *testing.T) {
	withTestWiring(t)

	userQueue := func() []string { return []string{"what the user typed"} }
	JobNotices.Notify("[background command] something finished")

	got := mergeNextMessages(userQueue, JobNotices.Drain)()
	if len(got) != 2 {
		t.Fatalf("expected both sources drained, got %v", got)
	}
	if got[0] != "what the user typed" {
		t.Fatalf("expected the user's line first, got %q", got[0])
	}
}

// TestWiring_BG3_BackgroundCommandOutlivesItsToolCall covers the reason the
// tool detaches its process from the caller's context: the dispatcher cancels
// that context as soon as the tool returns.
func TestWiring_BG3_BackgroundCommandOutlivesItsToolCall(t *testing.T) {
	reg, _ := withTestWiring(t)
	enableBackgroundBash(t)

	ctx, cancel := context.WithCancel(context.Background())
	res := tools.RunTool(ctx, "bash", map[string]any{
		"command":           "sleep 0.3; echo survived",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected a successful handoff, got error: %s", res.Error)
	}
	cancel() // what agent/tools_exec.go's deferred cancel does

	job, ok := reg.Wait(context.Background(), bgJobID(t, res.Content), 5*time.Second)
	if !ok {
		t.Fatal("job unknown to the shared registry")
	}
	if job.Result != "survived" {
		t.Fatalf("command died with its tool call; job result was %q (err=%q)", job.Result, job.Err)
	}
}

// TestWiring_BG4_BackgroundCommandIsPollableWithWait: the notice deliberately
// omits the output, so the "wait" tool has to be able to produce it.
func TestWiring_BG4_BackgroundCommandIsPollableWithWait(t *testing.T) {
	withTestWiring(t)
	enableBackgroundBash(t)

	res := tools.RunTool(context.Background(), "bash", map[string]any{
		"command":           "sleep 0.2; echo readable",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected a successful handoff, got error: %s", res.Error)
	}
	id := bgJobID(t, res.Content)

	waitRes := tools.RunTool(context.Background(), "wait", map[string]any{
		"seconds": 5,
		"job_id":  id,
	})
	if !waitRes.Success {
		t.Fatalf("wait on a background command failed: %s", waitRes.Error)
	}
	if !strings.Contains(waitRes.Content, "readable") {
		t.Fatalf("expected wait to return the command's output, got %q", waitRes.Content)
	}
}

// TestWiring_BG5_KillJobStopsABackgroundCommand exercises the tool the model
// is told about in the handoff message, over the real registry.
func TestWiring_BG5_KillJobStopsABackgroundCommand(t *testing.T) {
	reg, _ := withTestWiring(t)
	enableBackgroundBash(t)

	res := tools.RunTool(context.Background(), "bash", map[string]any{
		"command":           "sleep 60",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected a successful handoff, got error: %s", res.Error)
	}
	id := bgJobID(t, res.Content)

	killRes := tools.RunTool(context.Background(), "kill_job", map[string]any{"job_id": id})
	if !killRes.Success {
		t.Fatalf("kill_job failed: %s", killRes.Error)
	}

	job, ok := reg.Wait(context.Background(), id, 5*time.Second)
	if !ok {
		t.Fatal("job unknown to the shared registry")
	}
	if job.Status == jobs.StatusRunning {
		t.Fatal("command was still running after kill_job")
	}
	if !strings.Contains(job.Err, "stopped before it finished") {
		t.Fatalf("expected the job to record why it stopped, got %q", job.Err)
	}
}

// TestWiring_BG6_BlockedQuestionReachesTheParent covers the channel that used
// to be a dead end: a child calls "ask_parent", blocks, and nothing told the parent.
// A parent that never polls left the child blocked until its wall-clock limit,
// at which point the whole child run was thrown away.
func TestWiring_BG6_BlockedQuestionReachesTheParent(t *testing.T) {
	reg, _ := withTestWiring(t)

	asked := make(chan struct{})
	go func() {
		handle := reg.Start(context.Background(), "review the auth flow",
			func(ctx context.Context, jobID string) (string, bool, error) {
				close(asked)
				ans, _, ok := reg.Ask(ctx, jobID, "should I also change the tests?")
				if !ok {
					return "", false, context.Canceled
				}
				return "answered: " + ans, false, nil
			})
		_ = handle
	}()
	<-asked

	// The notice must arrive without the parent asking for it.
	deadline := time.After(5 * time.Second)
	var notices []string
	for len(notices) == 0 {
		select {
		case <-JobNotices.Signal():
			notices = JobNotices.Drain()
		case <-deadline:
			t.Fatal("no notice: a blocked child would sit there until it timed out")
		}
	}

	notice := strings.Join(notices, "\n")
	if !strings.Contains(notice, "should I also change the tests?") {
		t.Errorf("the question itself must reach the parent: %q", notice)
	}
	if !strings.Contains(notice, "answer_job(job_id=") {
		t.Errorf("the notice must say how to reply: %q", notice)
	}
	// The notice must tell the model to relay the question to the user and
	// wait for their reply in the conversation, rather than instructing it
	// to answer unconditionally — the old wording ("Reply with
	// answer_job(...)") is exactly what drove the model to invent an answer
	// on the user's behalf. There is no dedicated slash command any more:
	// the model itself relays the reply via answer_job once it has one.
	if !strings.Contains(notice, "Relay this question to the user") {
		t.Errorf("the notice must tell the model to relay to the user, not invent an answer: %q", notice)
	}
	if strings.Contains(notice, "/answer") {
		t.Errorf("the notice must not point at a /answer command — it no longer exists: %q", notice)
	}
}
