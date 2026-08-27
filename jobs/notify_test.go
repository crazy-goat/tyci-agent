package jobs

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNotifierDrainReturnsFIFOAndEmpties(t *testing.T) {
	n := NewNotifier()

	if got := n.Drain(); got != nil {
		t.Fatalf("expected nil from an empty queue, got %v", got)
	}

	n.Notify("first")
	n.Notify("second")

	got := n.Drain()
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("expected FIFO order, got %v", got)
	}
	if again := n.Drain(); again != nil {
		t.Fatalf("expected the queue to be empty after draining, got %v", again)
	}
}

func TestNotifierSignalsOnNotify(t *testing.T) {
	n := NewNotifier()

	select {
	case <-n.Signal():
		t.Fatal("signal fired with nothing queued")
	default:
	}

	n.Notify("something finished")

	select {
	case <-n.Signal():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the wakeup signal")
	}
}

// TestNotifierDrainClearsStaleSignal covers the case that would otherwise
// wake an idle REPL for nothing: a notice queued during an in-flight turn is
// taken by the NextMessages drain, so the wakeup edge it armed must not
// survive to start an empty turn afterwards.
func TestNotifierDrainClearsStaleSignal(t *testing.T) {
	n := NewNotifier()

	n.Notify("taken by the in-flight turn")
	if got := n.Drain(); len(got) != 1 {
		t.Fatalf("expected one notice, got %v", got)
	}

	select {
	case <-n.Signal():
		t.Fatal("drain left a stale wakeup edge armed")
	default:
	}
}

func TestNotifierClearDropsQueuedNoticeAndSignal(t *testing.T) {
	n := NewNotifier()
	n.Notify("old completion")
	n.Clear()
	if got := n.Drain(); got != nil {
		t.Fatalf("Clear left queued notices: %v", got)
	}
	select {
	case <-n.Signal():
		t.Fatal("Clear left a stale signal armed")
	default:
	}
}

func TestNotifierNotifyNeverBlocks(t *testing.T) {
	n := NewNotifier()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more notices than the signal channel's capacity of 1, with
		// nobody receiving.
		for i := 0; i < maxPendingNotices*3; i++ {
			n.Notify(fmt.Sprintf("notice %d", i))
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify blocked with nobody draining")
	}

	got := n.Drain()
	if len(got) != maxPendingNotices {
		t.Fatalf("expected the queue to be capped at %d, got %d", maxPendingNotices, len(got))
	}
	// The cap drops the OLDEST notices: the newest are the ones still worth
	// acting on.
	want := fmt.Sprintf("notice %d", maxPendingNotices*3-1)
	if got[len(got)-1] != want {
		t.Fatalf("expected the newest notice to survive (%q), got %q", want, got[len(got)-1])
	}
}

func TestNotifierIgnoresEmptyText(t *testing.T) {
	n := NewNotifier()
	n.Notify("")
	if got := n.Drain(); got != nil {
		t.Fatalf("expected an empty notice to be ignored, got %v", got)
	}
}

// TestNotifierMarkQuestionShown_BeforeNotify covers the "handOff wins the
// race" ordering: MarkQuestionShown runs before the matching NotifyQuestion
// call ever reaches the queue — see MarkQuestionShown's doc comment for why
// this order genuinely happens (onEvent and handOff run on different
// goroutines with no ordering guarantee between them). The later
// NotifyQuestion call for the same jobID/seq must be dropped, not queued.
func TestNotifierMarkQuestionShown_BeforeNotify(t *testing.T) {
	n := NewNotifier()
	n.MarkQuestionShown("job-1", 1)
	n.NotifyQuestion("job-1", 1, "job-1 is blocked: which branch?")

	if got := n.Drain(); got != nil {
		t.Fatalf("expected the notice to be suppressed, got %v", got)
	}
}

// TestNotifierMarkQuestionShown_AfterNotify covers the other ordering: the
// notice is already queued (onEvent won the race) by the time
// MarkQuestionShown runs. It must be removed before the next Drain.
func TestNotifierMarkQuestionShown_AfterNotify(t *testing.T) {
	n := NewNotifier()
	n.NotifyQuestion("job-1", 1, "job-1 is blocked: which branch?")
	n.MarkQuestionShown("job-1", 1)

	if got := n.Drain(); got != nil {
		t.Fatalf("expected the notice to be suppressed, got %v", got)
	}
}

// TestNotifierMarkQuestionShown_OnlySuppressesTheMatchingAsk: a mark for one
// job/seq pair must not swallow an unrelated notice — including a DIFFERENT
// ask from the very same job (e.g. it got answered and then asked again).
func TestNotifierMarkQuestionShown_OnlySuppressesTheMatchingAsk(t *testing.T) {
	n := NewNotifier()
	n.MarkQuestionShown("job-1", 1)
	n.NotifyQuestion("job-1", 1, "suppressed")
	n.NotifyQuestion("job-1", 2, "job-1 asks again: which file?")
	n.NotifyQuestion("job-2", 1, "job-2 asks: which branch?")
	n.Notify("unrelated background notice")

	got := n.Drain()
	if len(got) != 3 {
		t.Fatalf("expected 3 surviving notices, got %v", got)
	}
	for _, want := range []string{"job-1 asks again: which file?", "job-2 asks: which branch?", "unrelated background notice"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q among the surviving notices, got %v", want, got)
		}
	}
}

// TestNotifierMarkQuestionShown_SameQuestionTextTwiceIsNotConfused is item 54
// review finding 1's concrete failure scenario, keyed correctly this time: a
// job asks the exact same question text twice across its lifetime (e.g.
// retried after a timeout). The first ask's handoff-carried mark must not
// swallow the SECOND ask's genuine notice just because the words match —
// only seq identifies which ask is "shown", never the text.
func TestNotifierMarkQuestionShown_SameQuestionTextTwiceIsNotConfused(t *testing.T) {
	n := NewNotifier()
	const question = "job-1 is blocked: which branch?"

	// First ask (seq 1): shown via a handoff message before the notice
	// arrived — the common race-winner ordering.
	n.MarkQuestionShown("job-1", 1)
	n.NotifyQuestion("job-1", 1, question)
	if got := n.Drain(); got != nil {
		t.Fatalf("expected the first ask's notice to be suppressed, got %v", got)
	}

	// Second ask (seq 2), later in the job's lifetime, asks the identical
	// text but was NEVER shown via a handoff message — its notice must
	// reach the model.
	n.NotifyQuestion("job-1", 2, question)
	got := n.Drain()
	if len(got) != 1 || got[0] != question {
		t.Fatalf("expected the second ask's notice to survive despite identical text, got %v", got)
	}
}

// TestNotifierMarkQuestionShown_NoMatchIsANoOp: marking a job/seq that
// nothing ever queues must not affect anything else in the queue, and must
// not itself grow without bound in ordinary use (see maxShownKeys).
func TestNotifierMarkQuestionShown_NoMatchIsANoOp(t *testing.T) {
	n := NewNotifier()
	n.Notify("unrelated")
	n.MarkQuestionShown("job-does-not-exist", 1)

	got := n.Drain()
	if len(got) != 1 || got[0] != "unrelated" {
		t.Fatalf("expected the unrelated notice untouched, got %v", got)
	}
}

// TestNotifierMarkQuestionShown_EmptyJobIDIsIgnored: NotifyQuestion callers
// that pass no jobID (there is none in this codebase today, but the
// contract should not silently misbehave) must never be treated as
// matching each other.
func TestNotifierMarkQuestionShown_EmptyJobIDIgnored(t *testing.T) {
	n := NewNotifier()
	n.MarkQuestionShown("", 1)
	n.NotifyQuestion("", 1, "should still surface")

	got := n.Drain()
	if len(got) != 1 || got[0] != "should still surface" {
		t.Fatalf("expected the notice to survive an empty-jobID mark, got %v", got)
	}
}

// TestNotifierMarkQuestionShown_ZeroSeqIsIgnored: seq 0 is not a valid ask id
// (Ask always increments before use — see QuestionSeq's doc comment), so a
// mark or notify with seq 0 must never accidentally match another.
func TestNotifierMarkQuestionShown_ZeroSeqIsIgnored(t *testing.T) {
	n := NewNotifier()
	n.MarkQuestionShown("job-1", 0)
	n.NotifyQuestion("job-1", 0, "should still surface")

	got := n.Drain()
	if len(got) != 1 || got[0] != "should still surface" {
		t.Fatalf("expected the notice to survive a zero-seq mark, got %v", got)
	}
}

// TestNotifierClear_DropsShownKeys is item 54 review finding 3: a
// not-yet-consumed MarkQuestionShown key must not survive Clear (the /new
// conversation boundary), or it could silently swallow a genuinely new
// question notice queued after the boundary.
func TestNotifierClear_DropsShownKeys(t *testing.T) {
	n := NewNotifier()
	n.MarkQuestionShown("job-1", 1)
	n.Clear()
	n.NotifyQuestion("job-1", 1, "should surface after Clear")

	got := n.Drain()
	if len(got) != 1 || got[0] != "should surface after Clear" {
		t.Fatalf("expected the notice to survive because Clear reset the shown record, got %v", got)
	}
}

// TestPendingLinesPutsBlockedJobsFirst: the two cases need opposite responses,
// and only one of them is urgent. A running job is something to wait for; a job
// blocked on a question makes no progress and loses all its work when it times
// out, and only the current turn can unblock it.
func TestPendingLinesPutsBlockedJobsFirst(t *testing.T) {
	r := NewRegistry()

	running := make(chan struct{})
	r.Start(context.Background(), "the long one", KindOther, "", func(ctx context.Context, id string) (string, bool, error) {
		<-running
		return "done", false, nil
	})

	asked := make(chan struct{})
	go func() {
		r.Start(context.Background(), "the blocked one", KindOther, "", func(ctx context.Context, id string) (string, bool, error) {
			close(asked)
			ans, _, _ := r.Ask(ctx, id, "which branch?")
			return ans, false, nil
		})
	}()
	<-asked

	// Ask sets the status from inside the job goroutine, so allow it a moment.
	var lines []string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		lines = r.PendingLines()
		if len(lines) == 2 && strings.HasPrefix(lines[0], "WAITING") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(running)

	if len(lines) != 2 {
		t.Fatalf("expected both jobs listed, got %v", lines)
	}
	if !strings.HasPrefix(lines[0], "WAITING FOR ANSWER") {
		t.Errorf("the blocked job must come first: %v", lines)
	}
	if !strings.Contains(lines[0], "which branch?") {
		t.Errorf("the question itself has to be in the line: %q", lines[0])
	}
	if !strings.Contains(lines[0], "job_id=") {
		t.Errorf("the line must carry the id to answer: %q", lines[0])
	}
	if !strings.Contains(lines[1], "the long one") {
		t.Errorf("running job line = %q", lines[1])
	}
}

func TestPendingLinesIsEmptyWhenNothingIsOutstanding(t *testing.T) {
	r := NewRegistry()
	job := r.Start(context.Background(), "quick", KindOther, "", func(ctx context.Context, id string) (string, bool, error) {
		return "ok", false, nil
	})
	if _, ok := r.Wait(context.Background(), job.ID, 2*time.Second); !ok {
		t.Fatal("job never finished")
	}

	if lines := r.PendingLines(); len(lines) != 0 {
		t.Fatalf("expected nothing outstanding, got %v", lines)
	}
}
