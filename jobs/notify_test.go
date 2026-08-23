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
