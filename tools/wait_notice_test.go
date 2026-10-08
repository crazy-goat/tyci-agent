package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/jobs"
)

// A wait ends early when a new notice reaches the agent that calls it: the
// shared notice queue for the main agent, the job's own mailbox for a
// subagent. A notice that was queued before the wait started does not count,
// and the wait never takes a notice away from its queue.

// waitNoticeEnv wires a real notice queue and a real job registry for one
// test, the same way main.go does.
func waitNoticeEnv(t *testing.T) (*jobs.Notifier, *jobs.Registry) {
	t.Helper()
	notices := jobs.NewNotifier()
	reg := jobs.NewRegistry()
	SetJobNotifier(notices)
	SetJobMailbox(realJobMailbox{reg})
	t.Cleanup(func() {
		SetJobNotifier(nil)
		SetJobMailbox(nil)
	})
	return notices, reg
}

// boundedCtx ends a wait that should have ended on a notice after 10s. A
// broken wait then fails its test instead of hanging for the full 30 minutes.
func boundedCtx(t *testing.T, parent context.Context) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// startSubagentJob starts a job that runs until the test ends and returns its
// id, so a test can post to its mailbox.
func startSubagentJob(t *testing.T, reg *jobs.Registry) string {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	job := reg.Start(context.Background(), "subagent", jobs.KindSubagent, "", func(context.Context, string) (string, bool, error) {
		<-release
		return "done", false, nil
	})
	return job.ID
}

func TestWaitTool_JobWaitEndsOnNewMainNotice(t *testing.T) {
	notices, _ := waitNoticeEnv(t)
	tool := &WaitTool{Waiter: &scriptedWaiter{known: true}}
	go func() {
		time.Sleep(100 * time.Millisecond)
		notices.Notify("[watchdog] Agent job-7 has shown no activity for 3m0s.")
	}()

	start := time.Now()
	res := tool.Run(boundedCtx(t, context.Background()), map[string]any{"job_id": "job-1", "seconds": 1800})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited %v after a notice had arrived", elapsed)
	}
	if !res.Success || !strings.Contains(res.Content, "new notice arrived") || !strings.Contains(res.Content, "job-1") {
		t.Fatalf("unexpected result: success=%v content=%q error=%q", res.Success, res.Content, res.Error)
	}
	if got := notices.Drain(); len(got) != 1 {
		t.Fatalf("the wait must leave the notice queued, got %v", got)
	}
}

func TestWaitTool_JobWaitEndsOnNewMessageForSubagent(t *testing.T) {
	_, reg := waitNoticeEnv(t)
	subID := startSubagentJob(t, reg)
	tool := &WaitTool{Waiter: &scriptedWaiter{known: true}}
	ctx := context.WithValue(boundedCtx(t, context.Background()), JobIDCtxKey{}, subID)
	go func() {
		time.Sleep(100 * time.Millisecond)
		reg.Post(subID, "[watchdog] Agent job-7 has shown no activity for 3m0s.")
	}()

	start := time.Now()
	res := tool.Run(ctx, map[string]any{"job_id": "job-1", "seconds": 1800})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("subagent waited %v after a message had arrived", elapsed)
	}
	if !res.Success || !strings.Contains(res.Content, "new notice arrived") {
		t.Fatalf("unexpected result: success=%v content=%q error=%q", res.Success, res.Content, res.Error)
	}
}

func TestWaitTool_PlainWaitEndsOnNewMainNotice(t *testing.T) {
	notices, _ := waitNoticeEnv(t)
	go func() {
		time.Sleep(100 * time.Millisecond)
		notices.Notify("[scheduled job] finished.")
	}()

	start := time.Now()
	res := (&WaitTool{}).Run(boundedCtx(t, context.Background()), map[string]any{"seconds": 60})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("plain wait lasted %v after a notice had arrived", elapsed)
	}
	if !res.Success || !strings.Contains(res.Content, "new notice arrived") {
		t.Fatalf("unexpected result: success=%v content=%q error=%q", res.Success, res.Content, res.Error)
	}
}

// A subagent's message must not end the main agent's plain wait.
func TestWaitTool_MainPlainWaitIgnoresMessageForSubagent(t *testing.T) {
	_, reg := waitNoticeEnv(t)
	subID := startSubagentJob(t, reg)
	go func() {
		time.Sleep(100 * time.Millisecond)
		reg.Post(subID, "a message for the subagent")
	}()

	res := (&WaitTool{}).Run(context.Background(), map[string]any{"seconds": 1})
	if !res.Success || !strings.Contains(res.Content, "waited 1s") {
		t.Fatalf("main wait must not end on a subagent message, got content=%q error=%q", res.Content, res.Error)
	}
}

// A main-queue notice must not end a subagent's plain wait.
func TestWaitTool_SubagentPlainWaitIgnoresMainNotice(t *testing.T) {
	notices, reg := waitNoticeEnv(t)
	subID := startSubagentJob(t, reg)
	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, subID)
	go func() {
		time.Sleep(100 * time.Millisecond)
		notices.Notify("[watchdog] Agent job-7 has shown no activity for 3m0s.")
	}()

	res := (&WaitTool{}).Run(ctx, map[string]any{"seconds": 1})
	if !res.Success || !strings.Contains(res.Content, "waited 1s") {
		t.Fatalf("subagent wait must not end on a main notice, got content=%q error=%q", res.Content, res.Error)
	}
}

// A notice that was already queued before the wait started is delivered at the
// next iteration boundary; it must not end the wait before it starts.
func TestWaitTool_NoticeQueuedBeforeTheWaitDoesNotEndIt(t *testing.T) {
	notices, _ := waitNoticeEnv(t)
	notices.Notify("queued earlier")

	res := (&WaitTool{}).Run(context.Background(), map[string]any{"seconds": 1})
	if !res.Success || !strings.Contains(res.Content, "waited 1s") {
		t.Fatalf("a notice queued earlier must not end the wait, got content=%q error=%q", res.Content, res.Error)
	}
	if got := notices.Drain(); len(got) != 1 {
		t.Fatalf("the wait must not consume the queued notice, got %v", got)
	}
}
