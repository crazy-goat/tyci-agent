package main

// Item 54: when a child calls ask_parent during a BLOCKING subagent call
// that then hands off, its question used to surface twice — once inline in
// the handoff message's "question" field (tools/subagent.go's
// spawnedJobsMessage), once again as the separately queued ask-notice
// (jobs.Registry.Ask's onEvent hook, wired in main.go's wireTools). The
// three tests below drive the REAL production wiring (withTestWiring,
// wireTools, tools.RunTool) through the three outcomes that matter:
//
//   - (a) the handoff message DOES carry the question -> the queued notice
//     must be suppressed at drain time, not repeated.
//   - (b) the handoff message does NOT carry the question (here: no
//     JobObserver wired, so handOff's own pendingQuestions peek has
//     nothing to query — one of several ways this can happen, see
//     handOff's doc comment for the others) -> the queued notice is the
//     only delivery and must still surface.
//   - (c) the parent turn is cancelled (Esc) -> the queued notice must
//     survive untouched, because duplicating it there costs far less than
//     risking its only delivery.
//
// See jobs.Notifier.MarkQuestionShown and tools.SubagentTool.handOff's doc
// comments for the mechanism this exercises end to end.

import (
	"context"
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

// childAsksOneQuestionScript is the shared model script for these tests: turn
// 0 calls ask_parent with question, turn 1 reports the answer it got back.
func childAsksOneQuestionScript(question string) func(turn int, req connector.Request) []stream.Event {
	return func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.ToolCall{ID: "ask0", Name: "ask_parent", Arguments: `{"question":"` + question + `"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		return []stream.Event{
			stream.TextDelta{Text: "answer was: " + lastToolResultText(req)},
			stream.Finish{Reason: "stop"},
		}
	}
}

// waitForJobWaitingAnswer polls reg for a job in StatusWaitingAnswer and
// returns its id, instead of guessing with a fixed sleep.
func waitForJobWaitingAnswer(t *testing.T, reg *jobs.Registry) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, j := range reg.List() {
			if j.Status == jobs.StatusWaitingAnswer {
				return j.ID
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no job ever reached waiting_answer")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWiring_54a_HandoffCarriesQuestion_DrainDoesNotDuplicate is case (a):
// the child asks its question early enough that runWithHandoff's wake path
// (waitingWakeC) hands it off immediately, and the resulting message carries
// the question inline (see TestRunWithHandoff_WakesWhenChildAsksMidCall for
// the same mechanism at the tools-package level). The separately queued
// onEvent notice for the exact same job/question must not also appear once
// NextMessages drains — that would be the same question shown twice in one
// turn.
func TestWiring_54a_HandoffCarriesQuestion_DrainDoesNotDuplicate(t *testing.T) {
	reg, _ := withTestWiring(t)
	enableBackgroundBash(t)
	t.Cleanup(tools.SetSubagentBackgroundAfterSecForTests(time.Minute))

	childFake := &connectortest.Fake{ProviderName: "d54a-child"}
	childFake.Script = childAsksOneQuestionScript("which branch?")
	providers.Register(&fixedClientProvider{name: "d54a-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	res := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "ask a question and report the answer", "model": "d54a-child-prov/child-model",
	})
	if !res.Success {
		t.Fatalf("blocking subagent call failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "which branch?") {
		t.Fatalf("expected the handoff message to carry the question: %s", res.Content)
	}
	m := jobIDPattern.FindStringSubmatch(res.Content)
	if m == nil {
		t.Fatalf("could not find job_id in handoff content: %q", res.Content)
	}
	jobID := m[1]

	// The onEvent notice and handOff's markShown call race on different
	// goroutines (see MarkQuestionShown's doc comment) — either can run
	// first, and the dedup is correct either way, so there is no "wait a
	// moment then check" needed for correctness. This sleep only gives a
	// genuinely buggy implementation (one that fails to suppress the later
	// arrival) a fair chance to have already gone wrong before the
	// assertion below.
	time.Sleep(150 * time.Millisecond)
	if pending := JobNotices.Drain(); len(pending) != 0 {
		t.Fatalf("expected no duplicate notice once the handoff message already carries the question, got %v", pending)
	}

	if !reg.Answer(jobID, "main", true) {
		t.Fatal("expected Answer to succeed against a job currently waiting")
	}
	final, ok := reg.Wait(context.Background(), jobID, 2*time.Second)
	if !ok || final.Status != jobs.StatusDone {
		t.Fatalf("child did not finish properly: %+v", final)
	}
}

// TestWiring_54b_HandoffWithoutObserver_DrainSurfacesTheQuestion is case (b):
// with no JobObserver wired, handOff's pendingQuestions peek always returns
// nil (see its doc comment), so the handoff message NEVER carries a
// "question" field no matter what the child is blocked on. The queued
// onEvent notice is then the only delivery the question ever gets, and it
// must still reach the model.
func TestWiring_54b_HandoffWithoutObserver_DrainSurfacesTheQuestion(t *testing.T) {
	reg, _ := withTestWiring(t)
	enableBackgroundBash(t)
	// Short: with no observer wired, runWithHandoff's wake path is disabled
	// (see its doc comment on waitingWakeC), so only this timer can end the
	// wait.
	t.Cleanup(tools.SetSubagentBackgroundAfterSecForTests(20 * time.Millisecond))

	tools.SetJobObserver(nil)
	t.Cleanup(func() { tools.SetJobObserver(jobObserverAdapter{reg: JobRegistry}) })

	childFake := &connectortest.Fake{ProviderName: "d54b-child"}
	childFake.Script = childAsksOneQuestionScript("which environment?")
	providers.Register(&fixedClientProvider{name: "d54b-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	res := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "ask a question and report the answer", "model": "d54b-child-prov/child-model",
	})
	if !res.Success {
		t.Fatalf("blocking subagent call failed: %s", res.Error)
	}
	if strings.Contains(res.Content, "which environment?") {
		t.Fatalf("expected the handoff message to NOT carry the question with no observer wired: %s", res.Content)
	}
	m := jobIDPattern.FindStringSubmatch(res.Content)
	if m == nil {
		t.Fatalf("could not find job_id in handoff content: %q", res.Content)
	}
	jobID := m[1]

	deadline := time.After(3 * time.Second)
	var notices []string
	for len(notices) == 0 {
		select {
		case <-JobNotices.Signal():
			notices = JobNotices.Drain()
		case <-deadline:
			t.Fatal("no notice: the question must still reach the parent when the handoff message did not carry it")
		}
	}
	if !strings.Contains(strings.Join(notices, "\n"), "which environment?") {
		t.Fatalf("expected the question in the drained notice, got %v", notices)
	}

	if !reg.Answer(jobID, "staging", true) {
		t.Fatal("expected Answer to succeed against a job currently waiting")
	}
	final, ok := reg.Wait(context.Background(), jobID, 2*time.Second)
	if !ok || final.Status != jobs.StatusDone {
		t.Fatalf("child did not finish properly: %+v", final)
	}
}

// delayedObserver adds a fixed latency in front of every Observe call. Used
// below to hold back runWithHandoff's own watcher (watchForWaiting), whose
// very first Observe call uses timeout 0 and can otherwise notice an
// already-pending question and fire the wake essentially the instant the
// child asks — before a test's own goroutine can reliably win a cancel()
// race against it. handOff's own pendingQuestions peek goes through the same
// observer, but only runs AFTER the select has already committed to a
// branch, so delaying it too costs this test latency, not correctness.
type delayedObserver struct {
	inner tools.JobObserver
	delay time.Duration
}

func (d delayedObserver) Observe(ctx context.Context, id string, timeout time.Duration) (tools.JobStatus, bool) {
	time.Sleep(d.delay)
	return d.inner.Observe(ctx, id, timeout)
}

// TestWiring_54c_EscCtxDone_NoticeStillSurfaces is case (c): the parent turn
// is cancelled (Esc) while the child is blocked on ask_parent, and
// runWithHandoff's select picks its ctx.Done() branch — not the
// waitingWakeC wake the child's own question would otherwise trigger almost
// instantly (see delayedObserver above, which is what makes that outcome
// reliable rather than a coin flip). That branch still hands the child off
// (children are detached and keep going — see its doc comment) but passes
// markShown=false: on this path, keeping the queued onEvent notice untouched
// is the safer default — a duplicate costs far less than risking its only
// delivery. The queued notice must survive that untouched.
func TestWiring_54c_EscCtxDone_NoticeStillSurfaces(t *testing.T) {
	reg, _ := withTestWiring(t)
	enableBackgroundBash(t)
	t.Cleanup(tools.SetSubagentBackgroundAfterSecForTests(time.Minute))

	tools.SetJobObserver(delayedObserver{inner: jobObserverAdapter{reg: JobRegistry}, delay: 300 * time.Millisecond})
	t.Cleanup(func() { tools.SetJobObserver(jobObserverAdapter{reg: JobRegistry}) })

	childFake := &connectortest.Fake{ProviderName: "d54c-child"}
	childFake.Script = childAsksOneQuestionScript("deploy now?")
	providers.Register(&fixedClientProvider{name: "d54c-child-prov", client: childFake})

	ctx, cancel := context.WithCancel(context.Background())
	ctx = connector.WithModelClient(ctx, connectortest.Text("n/a"))

	resultCh := make(chan tools.ToolResult, 1)
	go func() {
		resultCh <- tools.RunTool(ctx, "subagent", map[string]any{
			"task": "ask a question and report the answer", "model": "d54c-child-prov/child-model",
		})
	}()

	jobID := waitForJobWaitingAnswer(t, reg)
	cancel()

	var res tools.ToolResult
	select {
	case res = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the context did not end the blocking call")
	}
	if !res.Success || !strings.Contains(res.Content, "running in the background") {
		t.Fatalf("expected a handoff message after Esc, got: %+v", res)
	}

	deadline := time.Now().Add(3 * time.Second)
	var notices []string
	for len(notices) == 0 && time.Now().Before(deadline) {
		notices = JobNotices.Drain()
		if len(notices) == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if len(notices) == 0 {
		t.Fatal("expected the ask-notice to still surface after an Esc handoff — it may be the only delivery the question ever gets")
	}
	if !strings.Contains(strings.Join(notices, "\n"), "deploy now?") {
		t.Fatalf("expected the question in the drained notice, got %v", notices)
	}

	reg.Answer(jobID, "yes", true)
	reg.Wait(context.Background(), jobID, 2*time.Second)
}
