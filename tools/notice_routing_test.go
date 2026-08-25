package tools

// B4 (batch-2 audit): notice delivery must be addressed to the job that
// spawned the work, not unconditionally to the main, process-wide queue —
// otherwise a job spawned from an independent fork (a /btw side-conversation,
// or a subagent nested inside one) notifies the wrong conversation. These
// tests exercise the real production notify path (notifyToParent, reached
// through the bash tool's background handoff) against a real jobs.Registry,
// wired the same way main() wires it (SetJobMailbox/SetJobStarter/
// SetJobNotifier), rather than reimplementing the routing decision.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// realJobMailbox mirrors main.go's jobMailboxAdapter (over a real
// jobs.Registry) so these tests exercise notifyToParent's actual delivery
// path instead of a hand-rolled stand-in.
type realJobMailbox struct{ reg *jobs.Registry }

func (m realJobMailbox) Resolve(id string) (string, bool) { return m.reg.Resolve(id) }
func (m realJobMailbox) Post(id, text string) bool        { return m.reg.Post(id, text) }
func (m realJobMailbox) IsLive(id string) bool            { return m.reg.IsLive(id) }
func (m realJobMailbox) Drain(id string) []string         { return m.reg.DrainMessages(id) }

// noticeRoutingEnv wires a fresh registry, mailbox, starter and main
// notifier, and restores everything on cleanup.
func noticeRoutingEnv(t *testing.T) (*jobs.Registry, *recordingNotifier) {
	t.Helper()
	reg := jobs.NewRegistry()
	notifier := &recordingNotifier{}
	SetJobStarter(testJobStarter{reg})
	SetJobMailbox(realJobMailbox{reg})
	SetJobNotifier(notifier)
	SetBackgroundBashEnabled(true)
	t.Cleanup(func() {
		KillAllBackgroundBash()
		deadline := time.Now().Add(5 * time.Second)
		for backgroundSlotsInUse() > 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		SetJobStarter(nil)
		SetJobMailbox(nil)
		SetJobNotifier(nil)
		SetBackgroundBashEnabled(false)
	})
	return reg, notifier
}

// TestNotifyToParent_RoutesToForkMailbox_NotMainQueue is the headline B4
// scenario: a job (here, a backgrounded bash command — the same mechanism a
// subagent(async=true) spawn uses) started with a job id in ctx that names
// a still-running "fork" (standing in for a /btw side-conversation) must
// have its completion notice delivered into THAT job's own mailbox, and
// must NOT appear on the main queue at all.
func TestNotifyToParent_RoutesToForkMailbox_NotMainQueue(t *testing.T) {
	reg, mainNotices := noticeRoutingEnv(t)

	release := make(chan struct{})
	fork := reg.Start(context.Background(), "fork", jobs.KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "fork done", false, nil
	})

	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, fork.ID)
	res := (&BashTool{}).Run(ctx, map[string]any{
		"command":           "echo done-in-fork",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	id := jobIDFromResult(t, res.Content)
	_ = waitForJob(t, reg, id, 5*time.Second)

	// Give the (already-finished) job's onEvent-driven notify a moment to
	// land — it fires synchronously inside the job's own goroutine before
	// Start's completion path returns, so waitForJob having already
	// observed the terminal status is enough, but a short deadline loop
	// keeps this robust either way.
	deadline := time.Now().Add(2 * time.Second)
	var forkMailbox []string
	for time.Now().Before(deadline) {
		forkMailbox = reg.DrainMessages(fork.ID)
		if len(forkMailbox) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if len(mainNotices.all()) != 0 {
		t.Fatalf("expected NO notice on the main queue, got %v", mainNotices.all())
	}
	if len(forkMailbox) != 1 {
		t.Fatalf("expected exactly one notice delivered to the fork's own mailbox, got %v", forkMailbox)
	}
	if !strings.Contains(forkMailbox[0], id) {
		t.Fatalf("fork mailbox notice should mention the finished job's id %q: %q", id, forkMailbox[0])
	}

	close(release)
	reg.Wait(context.Background(), fork.ID, time.Second)
}

// TestNotifyToParent_NoParent_GoesToMain is the baseline: work spawned
// directly from the top-level conversation (no job id in ctx at all) must
// still notify main — nothing changes for the common case.
func TestNotifyToParent_NoParent_GoesToMain(t *testing.T) {
	_, mainNotices := noticeRoutingEnv(t)

	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command":           "echo top-level",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(mainNotices.all()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(mainNotices.all()) != 1 {
		t.Fatalf("expected exactly one notice on the main queue, got %v", mainNotices.all())
	}
}

// TestNotifyToParent_ParentAlreadyGone_ForwardsToMainTagged covers the
// explicit design choice for when the intended recipient no longer exists:
// the parent job named in ctx finished BEFORE the background command's own
// notice was ready. The notice must not be dropped — it is forwarded to
// main, tagged so it is clear it was not meant for this queue originally.
func TestNotifyToParent_ParentAlreadyGone_ForwardsToMainTagged(t *testing.T) {
	reg, mainNotices := noticeRoutingEnv(t)

	// A parent job that finishes immediately — by the time the background
	// bash command completes, it is long gone.
	goneParent := reg.Start(context.Background(), "gone", jobs.KindSubagent, "", func(context.Context, string) (string, bool, error) {
		return "already done", false, nil
	})
	reg.Wait(context.Background(), goneParent.ID, time.Second)

	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, goneParent.ID)
	res := (&BashTool{}).Run(ctx, map[string]any{
		"command":           "echo orphaned",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	id := jobIDFromResult(t, res.Content)
	_ = waitForJob(t, reg, id, 5*time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(mainNotices.all()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	all := mainNotices.all()
	if len(all) != 1 {
		t.Fatalf("expected the orphaned notice to be forwarded to main exactly once, got %v", all)
	}
	if !strings.Contains(all[0], goneParent.ID) || !strings.Contains(all[0], "forwarded") {
		t.Fatalf("expected the forwarded notice to say which job it was meant for and that it was forwarded, got %q", all[0])
	}
}
