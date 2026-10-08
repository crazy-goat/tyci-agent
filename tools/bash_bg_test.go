package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/jobs"
)

// recordingNotifier captures the completion notices a background command
// produces, standing in for the app's jobs.Notifier. It also records
// MarkQuestionShown calls (jobID -> seq) so tests can assert whether handOff
// told the notifier a question was already delivered via the handoff
// message — see subagent_handoff_test.go's question-notice tests. Keyed on
// seq (jobs.Job.QuestionSeq), not question text, matching the real
// jobs.Notifier's key (item 54 review finding 1).
type recordingNotifier struct {
	mu    sync.Mutex
	seen  []string
	shown map[string]int
}

func (n *recordingNotifier) Notify(text string) {
	n.mu.Lock()
	n.seen = append(n.seen, text)
	n.mu.Unlock()
}

func (n *recordingNotifier) MarkQuestionShown(jobID string, seq int) {
	n.mu.Lock()
	if n.shown == nil {
		n.shown = make(map[string]int)
	}
	n.shown[jobID] = seq
	n.mu.Unlock()
}

func (n *recordingNotifier) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.seen))
	copy(out, n.seen)
	return out
}

func (n *recordingNotifier) shownFor(jobID string) (int, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	seq, ok := n.shown[jobID]
	return seq, ok
}

// The three values every background-bash test here is built around: the two
// caps on waiting, and the command that has to be killed.
//
// What is known: on the macOS -race job, TestKillJobStopsBackgroundCommand and
// TestKillJob_BashPathKeepsOldMessage were seen failing with the job still
// running after 5s, and cleanup then reporting "1 background slot(s) still in
// use". The mechanism could NOT be reproduced locally — thousands of
// start-then-group-kill cycles stayed far under a second — so it is assumed to
// be a loaded runner, not a lost SIGKILL (see findings.md).
//
// What these caps must therefore never do is hide a lost kill. That is what
// bgSleeper is for: a command that outlives both caps by 20x, so if the SIGKILL
// does nothing, the job cannot reach a terminal status inside bgFinishCap and
// the test fails instead of passing on the command's own exit. With the old 5s
// cap and a "sleep 30" command that gap held; raising the cap to 30s without
// lengthening the command would have removed it (a no-op kill then "passes" at
// 30.0s). Every test that expects its backgrounded command to be stopped uses
// bgSleeper; the ones that let a command finish on its own use a sleep well
// inside bgFinishCap.
const (
	bgFinishCap  = 30 * time.Second
	bgCleanupCap = 30 * time.Second
	bgSleeper    = "sleep 600"
)

// killBackgroundBashAndWait empties the process-global background state: it
// signals every backgrounded command and waits until no slot is occupied.
//
// A slot is released by the job goroutine reacting to the kill, not by the
// kill itself, so a test that returned without waiting left its state behind
// and the NEXT test started on top of it — which is how the next test's
// KillAllBackgroundBash came to report "expected to kill 2 commands, killed 3"
// for the two commands it had started itself (TestKillAllBackgroundBash).
// Background state is process-global, so an incomplete cleanup in one test is a
// failure in another; every environment here that can start a backgrounded
// command therefore calls this on the way out, from one place.
//
// Waiting for zero slots needs no second condition: a job's defers run LIFO,
// so unregisterBackgroundBash always runs BEFORE releaseBackgroundSlot (see
// handoff in bash.go) — no slot can be free while its id is still registered.
// The ids are read again only for the leak message, where naming the stuck ones
// is the point.
func killBackgroundBashAndWait(t *testing.T) {
	t.Helper()
	KillAllBackgroundBash()
	deadline := time.Now().Add(bgCleanupCap)
	for backgroundSlotsInUse() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if backgroundSlotsInUse() != 0 {
		t.Errorf("background state leaked past cleanup: %d slot(s) in use, still registered: %v",
			backgroundSlotsInUse(), runningBackgroundBash())
	}
}

// bgTestEnv wires the background-bash feature onto a fresh job registry and
// notifier, and restores the previous globals afterwards. Backgrounding is
// process-global state, so every test that touches it must go through here or
// it will leak into the next one.
func bgTestEnv(t *testing.T) (*jobs.Registry, *recordingNotifier) {
	t.Helper()
	reg := jobs.NewRegistry()
	notifier := &recordingNotifier{}

	SetJobStarter(testJobStarter{reg})
	SetJobProgressReporter(reg)
	SetJobNotifier(notifier)
	SetBackgroundBashEnabled(true)

	t.Cleanup(func() {
		killBackgroundBashAndWait(t)
		SetBackgroundBashEnabled(false)
		SetJobStarter(nil)
		SetJobProgressReporter(nil)
		SetJobNotifier(nil)
	})
	return reg, notifier
}

// waitForJob blocks until the job reaches a terminal status and returns its
// snapshot. It goes through Registry.Wait rather than Get + Snapshot on
// purpose: Get hands out the live *Job and Snapshot does not take the
// registry lock, so polling that way races the goroutine writing the job's
// result. Wait snapshots under the lock, which is also what every production
// consumer uses.
func waitForJob(t *testing.T, reg *jobs.Registry, id string, timeout time.Duration) jobs.Job {
	t.Helper()
	snap, ok := reg.Wait(context.Background(), id, timeout)
	if !ok {
		t.Fatalf("job %q not found in registry", id)
	}
	if snap.Status == jobs.StatusRunning || snap.Status == jobs.StatusWaitingAnswer {
		t.Fatalf("job %q did not finish within %v (status %s)", id, timeout, snap.Status)
	}
	return *snap
}

// jobIDFromResult pulls the job_id out of the handoff message the model sees.
func jobIDFromResult(t *testing.T, content string) string {
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

// TestBashForegroundUnchanged is the regression guard for the whole feature:
// a command that finishes quickly must still return its output inline, with
// no job involved, exactly as before backgrounding existed.
func TestBashForegroundUnchanged(t *testing.T) {
	bgTestEnv(t)

	res := (&BashTool{}).Run(context.Background(), map[string]any{"command": "echo hello"})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if res.Content != "hello" {
		t.Fatalf("expected inline output %q, got %q", "hello", res.Content)
	}
	if strings.Contains(res.Content, "job_id") {
		t.Fatalf("fast command should not have been backgrounded: %q", res.Content)
	}
}

// TestBashRunInBackgroundReturnsImmediately covers the explicit flag: the
// tool call returns without waiting, and the command keeps running and lands
// its result in the registry afterwards.
func TestBashRunInBackgroundReturnsImmediately(t *testing.T) {
	reg, notifier := bgTestEnv(t)

	start := time.Now()
	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command":           "sleep 0.4; echo done-late",
		"description":       "slow echo",
		"run_in_background": true,
	})
	elapsed := time.Since(start)

	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("run_in_background blocked for %v; it must not wait for the command", elapsed)
	}
	if !strings.Contains(res.Content, "Do NOT run this command again") {
		t.Fatalf("handoff message should warn against re-running: %q", res.Content)
	}

	id := jobIDFromResult(t, res.Content)
	job := waitForJob(t, reg, id, bgFinishCap)
	if job.Status != jobs.StatusDone {
		t.Fatalf("expected job done, got %s (err=%q)", job.Status, job.Err)
	}
	if job.Result != "done-late" {
		t.Fatalf("expected job result %q, got %q", "done-late", job.Result)
	}
	if !strings.Contains(job.Description, "slow echo") {
		t.Fatalf("job description should carry the label: %q", job.Description)
	}

	notices := notifier.all()
	if len(notices) != 1 {
		t.Fatalf("expected exactly one completion notice, got %d: %v", len(notices), notices)
	}
	if !strings.Contains(notices[0], "exit 0") || !strings.Contains(notices[0], id) {
		t.Fatalf("notice should name the outcome and the job id: %q", notices[0])
	}
}

// TestBashBackgroundedCommandSurvivesToolCallCancellation is the point of the
// context surgery in handoff: the caller's context dies the moment the tool
// call returns, and the command must not die with it.
func TestBashBackgroundedCommandSurvivesToolCallCancellation(t *testing.T) {
	reg, _ := bgTestEnv(t)

	marker := filepath.Join(t.TempDir(), "survived")

	ctx, cancel := context.WithCancel(context.Background())
	res := (&BashTool{}).Run(ctx, map[string]any{
		"command":           fmt.Sprintf("sleep 0.4; echo yes > %q", marker),
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	// Exactly what the dispatcher's deferred cancel does on return.
	cancel()

	id := jobIDFromResult(t, res.Content)
	if job := waitForJob(t, reg, id, bgFinishCap); job.Status != jobs.StatusDone {
		t.Fatalf("expected job done, got %s (err=%q)", job.Status, job.Err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("backgrounded command did not finish its work after the tool call was cancelled: %v", err)
	}
}

// TestBashAutoBackgroundAfterThreshold covers the automatic handoff: a
// command still running at the threshold is moved to the background instead
// of blocking the agent for its full runtime.
func TestBashAutoBackgroundAfterThreshold(t *testing.T) {
	reg, _ := bgTestEnv(t)

	start := time.Now()
	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command":          "sleep 2; echo eventually",
		"background_after": 1,
	})
	elapsed := time.Since(start)

	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "still running after 1s") {
		t.Fatalf("expected a threshold handoff message, got %q", res.Content)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("handoff took %v; it should happen at the threshold", elapsed)
	}

	id := jobIDFromResult(t, res.Content)
	job := waitForJob(t, reg, id, bgFinishCap)
	if job.Result != "eventually" {
		t.Fatalf("expected the full output in the job result, got %q", job.Result)
	}
}

// TestBashNoAutoBackgroundBeforeThreshold guards the other side of the
// threshold: a command that finishes just before it must return inline.
func TestBashNoAutoBackgroundBeforeThreshold(t *testing.T) {
	bgTestEnv(t)

	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command":          "echo quick",
		"background_after": 5,
	})
	if !res.Success || res.Content != "quick" {
		t.Fatalf("expected inline output %q, got success=%v content=%q err=%q", "quick", res.Success, res.Content, res.Error)
	}
}

// TestBashExplicitTimeoutStillBackgrounds is a regression test for the reason
// this feature looked broken in real use: models pass optional parameters as a
// matter of habit, and one observed session sent "timeout": 600 on every
// single bash call. Treating that as "willing to block" disabled the handoff
// almost every time. A timeout is a limit on total runtime, nothing more.
func TestBashExplicitTimeoutStillBackgrounds(t *testing.T) {
	bgTestEnv(t)

	start := time.Now()
	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command":          bgSleeper,
		"timeout":          600,
		"background_after": 1,
	})
	if !res.Success {
		t.Fatalf("expected a handoff, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "job_id") {
		t.Fatalf("a large timeout must not disable the handoff, got %q", res.Content)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("blocked for %s; the handoff should have returned at the threshold", elapsed)
	}
}

// TestBashBackgroundAfterZeroKeepsForeground is the explicit opt-out that
// replaced the inference above: a caller that really wants to block says so.
func TestBashBackgroundAfterZeroKeepsForeground(t *testing.T) {
	bgTestEnv(t)

	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command":          "echo waited",
		"timeout":          60,
		"background_after": 0,
	})
	if !res.Success || res.Content != "waited" {
		t.Fatalf("expected inline output, got success=%v content=%q err=%q", res.Success, res.Content, res.Error)
	}
}

// TestBashShortTimeoutSkipsHandoff: when the command would hit its own
// deadline before the threshold, arming the handoff could never pay off.
func TestBashShortTimeoutSkipsHandoff(t *testing.T) {
	bgTestEnv(t)

	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command": "sleep 30",
		"timeout": 1,
	})
	if res.Success {
		t.Fatal("expected the timeout to kill the command")
	}
	if strings.Contains(res.Error, "job_id") {
		t.Fatalf("should not have been backgrounded: %q", res.Error)
	}
	if !strings.Contains(res.Error, "timed out") {
		t.Fatalf("got %q", res.Error)
	}
}

// TestBashBackgroundDisabledRunsInForeground: with the feature off (the
// default, and what one-shot runs use) an explicit run_in_background must
// still produce the command's output, plus a note explaining why.
func TestBashBackgroundDisabledRunsInForeground(t *testing.T) {
	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command":           "echo foreground",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.HasPrefix(res.Content, "foreground") {
		t.Fatalf("expected the command's own output first, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "unavailable here") {
		t.Fatalf("expected a note explaining the fallback, got %q", res.Content)
	}
}

// regMailbox is the test twin of main's jobMailboxAdapter (btw.go): it wires
// a real jobs.Registry in as the JobMailbox.
type regMailbox struct{ reg *jobs.Registry }

func (m regMailbox) Resolve(id string) (string, bool) { return m.reg.Resolve(id) }
func (m regMailbox) Post(id, text string) bool        { return m.reg.Post(id, text) }
func (m regMailbox) IsLive(id string) bool            { return m.reg.IsLive(id) }
func (m regMailbox) Drain(id string) []string         { return m.reg.DrainMessages(id) }

// TestBashBackgroundInsideSubagent: a child agent hands a command to the
// background like the main agent does. Its completion notice goes to the
// child's own mailbox, never to the main queue.
func TestBashBackgroundInsideSubagent(t *testing.T) {
	reg, notifier := bgTestEnv(t)
	SetJobMailbox(regMailbox{reg})
	t.Cleanup(func() { SetJobMailbox(nil) })

	release := make(chan struct{})
	defer close(release)
	parent := reg.Start(context.Background(), "subagent", jobs.KindSubagent, "", func(context.Context, string) (string, bool, error) {
		<-release
		return "", false, nil
	})

	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, parent.ID)
	ctx = context.WithValue(ctx, SubagentSinkCtxKey{}, &streamingCollector{collector: newCollector()})
	res := (&BashTool{}).Run(ctx, map[string]any{
		"command":           "echo child",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	id := jobIDFromResult(t, res.Content)
	waitForJob(t, reg, id, bgFinishCap)

	mail := reg.DrainMessages(parent.ID)
	if len(mail) != 1 || !strings.Contains(mail[0], "[background command]") {
		t.Fatalf("expected the completion notice in the subagent mailbox, got %q", mail)
	}
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	for _, n := range notifier.seen {
		if strings.Contains(n, "[background command]") {
			t.Fatalf("subagent notice leaked to the main queue: %q", n)
		}
	}
}

// TestBashHandoffInsideSubagentTellsItToCollect: a subagent does not get a
// completion notice unless it makes another tool call, and its background
// commands stop when it returns its answer. So its handoff must name wait,
// and must not tell it to skip wait the way the main agent's handoff does.
// A resumed subagent carries JobIDCtxKey but no SubagentSinkCtxKey; its
// commands stop with its job too, so it needs the same text.
func TestBashHandoffInsideSubagentTellsItToCollect(t *testing.T) {
	cases := []struct {
		name     string
		withSink bool
	}{
		{name: "subagent with sink", withSink: true},
		{name: "resumed subagent without sink", withSink: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, _ := bgTestEnv(t)
			SetJobMailbox(regMailbox{reg})
			t.Cleanup(func() { SetJobMailbox(nil) })

			release := make(chan struct{})
			defer close(release)
			parent := reg.Start(context.Background(), "subagent", jobs.KindSubagent, "", func(context.Context, string) (string, bool, error) {
				<-release
				return "", false, nil
			})

			ctx := context.WithValue(context.Background(), JobIDCtxKey{}, parent.ID)
			if tc.withSink {
				ctx = context.WithValue(ctx, SubagentSinkCtxKey{}, &streamingCollector{collector: newCollector()})
			}
			res := (&BashTool{}).Run(ctx, map[string]any{
				"command":           "echo child",
				"run_in_background": true,
			})
			if !res.Success {
				t.Fatalf("expected success, got error: %s", res.Error)
			}
			id := jobIDFromResult(t, res.Content)
			for _, want := range []string{"stop when you return your answer", fmt.Sprintf("call wait(job_id=%q)", id)} {
				if !strings.Contains(res.Content, want) {
					t.Errorf("subagent handoff should contain %q, got %q", want, res.Content)
				}
			}
			for _, banned := range []string{"Do NOT call wait", "a notice reaches you"} {
				if strings.Contains(res.Content, banned) {
					t.Errorf("subagent handoff must not contain %q, got %q", banned, res.Content)
				}
			}
			waitForJob(t, reg, id, bgFinishCap)
		})
	}
}

// TestSubagentEndStopsItsBackgroundCommand: a subagent ends while its
// background command still runs. The command is stopped with the subagent, so
// no process outlives it. bgSleeper outlives bgFinishCap, so a lost kill fails
// the test instead of passing on the command's own exit. The stopped command
// must not send a notice to the main queue: the main agent never started it.
func TestSubagentEndStopsItsBackgroundCommand(t *testing.T) {
	reg, notifier := bgTestEnv(t)
	SetJobMailbox(regMailbox{reg})
	t.Cleanup(func() { SetJobMailbox(nil) })

	child := reg.Start(context.Background(), "subagent", jobs.KindSubagent, "", func(ctx context.Context, jobID string) (string, bool, error) {
		ctx = context.WithValue(ctx, JobIDCtxKey{}, jobID)
		ctx = context.WithValue(ctx, SubagentSinkCtxKey{}, &streamingCollector{collector: newCollector()})
		res := (&BashTool{}).Run(ctx, map[string]any{
			"command":           bgSleeper,
			"run_in_background": true,
		})
		return res.Content, false, nil
	})
	ended := waitForJob(t, reg, child.ID, bgFinishCap)

	cmd := waitForJob(t, reg, jobIDFromResult(t, ended.Result), bgFinishCap)
	if cmd.Status != jobs.StatusFailed {
		t.Fatalf("expected the command of an ended subagent to be stopped, got status %s", cmd.Status)
	}
	if !strings.Contains(cmd.Err, "stopped before it finished") {
		t.Fatalf("expected the error to say it was stopped, got %q", cmd.Err)
	}
	if !strings.Contains(cmd.Err, "its parent job ended") {
		t.Fatalf("expected the error to name the parent's end as a cause, got %q", cmd.Err)
	}
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	for _, n := range notifier.seen {
		if strings.Contains(n, "[background command]") {
			t.Fatalf("notice of a command stopped by its subagent's end reached the main queue: %q", n)
		}
	}
}

// TestKillJobStopsBackgroundCommand covers kill_job end to end: the process
// group dies, the job is recorded as failed, and the id stops being killable.
func TestKillJobStopsBackgroundCommand(t *testing.T) {
	reg, _ := bgTestEnv(t)

	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command":           bgSleeper + "; echo never",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	id := jobIDFromResult(t, res.Content)

	killRes := (&KillJobTool{}).Run(context.Background(), map[string]any{"job_id": id})
	if !killRes.Success {
		t.Fatalf("kill_job failed: %s", killRes.Error)
	}

	job := waitForJob(t, reg, id, bgFinishCap)
	if job.Status != jobs.StatusFailed {
		t.Fatalf("expected a killed command to be recorded as failed, got %s", job.Status)
	}
	if !strings.Contains(job.Err, "stopped before it finished") {
		t.Fatalf("expected the error to say it was stopped, got %q", job.Err)
	}

	// Killing it again must not claim success — it is no longer running.
	if again := (&KillJobTool{}).Run(context.Background(), map[string]any{"job_id": id}); again.Success {
		t.Fatal("kill_job on an already-finished command should fail, not report success")
	}
}

// TestKillAllBackgroundBash is the shutdown path: nothing else reaps these
// processes, so a session ending must take them with it.
func TestKillAllBackgroundBash(t *testing.T) {
	reg, _ := bgTestEnv(t)

	var ids []string
	for i := 0; i < 2; i++ {
		res := (&BashTool{}).Run(context.Background(), map[string]any{
			"command":           bgSleeper,
			"run_in_background": true,
		})
		if !res.Success {
			t.Fatalf("handoff %d failed: %s", i, res.Error)
		}
		ids = append(ids, jobIDFromResult(t, res.Content))
	}

	if n := KillAllBackgroundBash(); n != 2 {
		t.Fatalf("expected to kill 2 commands, killed %d", n)
	}
	for _, id := range ids {
		if job := waitForJob(t, reg, id, bgFinishCap); job.Status != jobs.StatusFailed {
			t.Fatalf("job %s: expected failed after KillAll, got %s", id, job.Status)
		}
	}
}

// TestBashBackgroundSlotCapFallsBackToForeground: the cap exists so a handful
// of concurrent builds can't turn into an unbounded number. Once it is
// reached, a further command must run in the foreground rather than quietly
// exceeding it.
func TestBashBackgroundSlotCapFallsBackToForeground(t *testing.T) {
	bgTestEnv(t)

	for i := 0; i < maxBackgroundBash; i++ {
		res := (&BashTool{}).Run(context.Background(), map[string]any{
			"command":           bgSleeper,
			"run_in_background": true,
		})
		if !res.Success || !strings.Contains(res.Content, "job_id") {
			t.Fatalf("handoff %d should have succeeded: success=%v content=%q err=%q", i, res.Success, res.Content, res.Error)
		}
	}

	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command":           "echo over-cap",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.HasPrefix(res.Content, "over-cap") {
		t.Fatalf("expected the command to run in the foreground, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "slots are busy") {
		t.Fatalf("expected a note about the busy slots, got %q", res.Content)
	}
}

// TestBashForegroundTimeoutStillKills: the dispatcher no longer imposes a
// deadline on bash (it can't, or it would kill backgrounded commands), so the
// tool must enforce its own.
func TestBashForegroundTimeoutStillKills(t *testing.T) {
	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command": "sleep 30",
		"timeout": 1,
	})
	if res.Success {
		t.Fatal("expected a timeout failure, got success")
	}
	if !strings.Contains(res.Error, "bash tool timed out") {
		t.Fatalf("expected a timeout error, got %q", res.Error)
	}
}

// TestBashForegroundCancellationKills: ESC (and any other caller
// cancellation) must still kill a foreground command, which is why the
// context is only detached at the moment of handoff.
func TestBashForegroundCancellationKills(t *testing.T) {
	bgTestEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	res := (&BashTool{}).Run(ctx, map[string]any{"command": "sleep 30"})
	if res.Success {
		t.Fatal("expected cancellation failure, got success")
	}
	if !strings.Contains(res.Error, "cancelled") {
		t.Fatalf("expected a cancellation error, got %q", res.Error)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %v; the process was not killed promptly", elapsed)
	}
}

// TestBashBackgroundFailureNoticeCarriesExitCode: the notice is the only
// thing injected into the conversation, so it has to say whether the command
// actually worked.
func TestBashBackgroundFailureNoticeCarriesExitCode(t *testing.T) {
	reg, notifier := bgTestEnv(t)

	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command":           "sleep 0.2; exit 127",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("the handoff itself should succeed, got error: %s", res.Error)
	}
	id := jobIDFromResult(t, res.Content)
	if job := waitForJob(t, reg, id, bgFinishCap); job.Status != jobs.StatusFailed {
		t.Fatalf("expected failed, got %s", job.Status)
	}

	notices := notifier.all()
	if len(notices) != 1 {
		t.Fatalf("expected one notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "exit 127") || !strings.Contains(notices[0], "command not found") {
		t.Fatalf("notice should carry the exit code and its hint: %q", notices[0])
	}
}

// TestKillJobWithSurvivorHoldingPipe guards #276: a process that left the
// killed group keeps the output pipe open, and the job must still end.
func TestKillJobWithSurvivorHoldingPipe(t *testing.T) {
	reg, _ := bgTestEnv(t)

	// setsid puts the background python in its own session, so the group kill
	// misses it. It holds stdout for 20s.
	res := (&BashTool{}).Run(context.Background(), map[string]any{
		"command":           `python3 -c "import os,time;os.setsid();time.sleep(20)" & ` + bgSleeper,
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	id := jobIDFromResult(t, res.Content)
	time.Sleep(time.Second) // let python reach setsid before the kill
	if !(&KillJobTool{}).Run(context.Background(), map[string]any{"job_id": id}).Success {
		t.Fatal("kill_job failed")
	}
	waitForJob(t, reg, id, 10*time.Second)
}
