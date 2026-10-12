package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/crazy-goat/tyci-agent/tools"
)

// TestBackgroundSurvivesRunCancel checks that a backgrounded call is not
// stopped when the Run that started it ends. Its real result must come as a
// late result.
func TestBackgroundSurvivesRunCancel(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	sub := b.Subscribe("test", bus.Filter{To: testSelf, Kinds: []bus.Kind{bus.KindToolResult}})
	defer sub.Close()
	d := newDispatcher(b, testSelf, sub)

	runCtx, cancelRun := context.WithCancel(context.Background())
	release := make(chan struct{})
	id := d.Fire(runCtx, func(c context.Context) (string, bool) {
		<-release
		if c.Err() != nil {
			return "cancelled: the turn was interrupted", true
		}
		return "real", false
	})
	if out := d.Await(runCtx, id, 10*time.Millisecond); !out.Background {
		t.Fatalf("outcome = %+v, want background", out)
	}
	cancelRun()
	close(release)

	got := waitToolResult(t, d, id)
	if got.Failed || got.Content != "real" {
		t.Errorf("late result = %+v, want real", got)
	}
}

// TestCancelBackgroundStopsBackgroundCall checks the ESC path for a call
// that is already backgrounded: cancelBackground ends it with a cancelled result.
func TestCancelBackgroundStopsBackgroundCall(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	sub := b.Subscribe("test", bus.Filter{To: testSelf, Kinds: []bus.Kind{bus.KindToolResult}})
	defer sub.Close()
	d := newDispatcher(b, testSelf, sub)

	id := d.Fire(context.Background(), func(c context.Context) (string, bool) {
		<-c.Done()
		return "cancelled: the turn was interrupted", true
	})
	if out := d.Await(context.Background(), id, 10*time.Millisecond); !out.Background {
		t.Fatalf("outcome = %+v, want background", out)
	}
	d.cancelBackground()

	got := waitToolResult(t, d, id)
	if !got.Failed || got.Content != "cancelled: the turn was interrupted" {
		t.Errorf("cancelled result = %+v", got)
	}
}

// TestNoDeadlineIgnoresWakeAndUser checks that a blocking call (noDeadline)
// is never backgrounded by a wake message or by the user.
func TestNoDeadlineIgnoresWakeAndUser(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	sub := b.Subscribe("test", bus.Filter{To: testSelf, Kinds: []bus.Kind{bus.KindToolResult}})
	defer sub.Close()
	d := newDispatcher(b, testSelf, sub)
	d.userPending = func() bool { return true }

	id := d.Fire(context.Background(), func(context.Context) (string, bool) {
		time.Sleep(100 * time.Millisecond)
		return "blocking", false
	})
	if _, err := bus.Publish(b, bus.KindToolResult, testSelf, testSelf, bus.OriginSystem,
		bus.ToolResult{CallID: "other", Content: "wake"}); err != nil {
		t.Fatal(err)
	}
	out := d.Await(context.Background(), id, noDeadline)
	if out.Background || out.Text != "blocking" {
		t.Errorf("outcome = %+v, want inline blocking result", out)
	}
}

// TestSerialCallsKeepOrder checks that two serial calls (todo) run one after
// the other, and that a wake does not put the first one in the background.
func TestSerialCallsKeepOrder(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	sub := b.Subscribe("test", bus.Filter{To: testSelf, Kinds: []bus.Kind{bus.KindToolResult}})
	defer sub.Close()
	d := newDispatcher(b, testSelf, sub)

	var mu sync.Mutex
	var events []string
	runner := fakeRunner{fn: func(_ context.Context, name string, args map[string]any) (string, error) {
		label, _ := args["label"].(string)
		mu.Lock()
		events = append(events, "start "+label)
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		events = append(events, "end "+label)
		mu.Unlock()
		return label, nil
	}}
	if _, err := bus.Publish(b, bus.KindToolResult, testSelf, testSelf, bus.OriginSystem,
		bus.ToolResult{CallID: "other", Content: "wake"}); err != nil {
		t.Fatal(err)
	}
	calls := []stream.ToolCall{
		{ID: "t1", Name: "todo", Arguments: `{"label":"first"}`},
		{ID: "t2", Name: "todo", Arguments: `{"label":"second"}`},
	}
	results, _, failed := executeToolsDispatched(context.Background(), runner, calls, d)
	for i := range results {
		if failed[i] || strings.HasPrefix(results[i], placeholderPrefix) {
			t.Errorf("result %d = %q failed=%v, want inline", i, results[i], failed[i])
		}
	}
	want := []string{"start first", "end first", "start second", "end second"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
}

// TestChildCallNeverBackgrounds checks that a subagent call is not moved to
// the background, even when its wait is over and a user is pending. The short
// timeout is the test hook: it stands in for the stub timeouts of 2.5 s and 60 s.
func TestChildCallNeverBackgrounds(t *testing.T) {
	isolateHome(t)
	d := NewChildDispatcher()
	d.userPending = func() bool { return true }

	id := d.Fire(context.Background(), func(context.Context) (string, bool) {
		time.Sleep(100 * time.Millisecond)
		return "child result", false
	})
	out := d.Await(context.Background(), id, 10*time.Millisecond)
	if out.Background || out.Failed || out.Text != "child result" {
		t.Errorf("outcome = %+v, want inline child result", out)
	}
	if n := d.Pending(); n != 0 {
		t.Errorf("pending = %d, want 0", n)
	}
}

// TestDispatchedMutationsKeepCallOrder checks that two writes of one batch
// run in call order, even when the first one starts late. The second write
// is edit B->C: it fails unless the first one (A->B) has run before it.
func TestDispatchedMutationsKeepCallOrder(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	sub := b.Subscribe("test", bus.Filter{To: testSelf, Kinds: []bus.Kind{bus.KindToolResult}})
	defer sub.Close()
	d := newDispatcher(b, testSelf, sub)

	// Each call has its own worktree, so the FIFO is not shared with other tests.
	ctx := tools.WithWorkdir(context.Background(), t.TempDir())
	var mu sync.Mutex
	content := "A"
	runner := fakeRunner{fn: func(ctx context.Context, name string, args map[string]any) (string, error) {
		from, _ := args["from"].(string)
		to, _ := args["to"].(string)
		if from == "A" {
			time.Sleep(50 * time.Millisecond) // this call reaches the FIFO late
		}
		release := tools.AcquireMutation(ctx)
		if release == nil {
			return "", ctx.Err()
		}
		defer release()
		mu.Lock()
		defer mu.Unlock()
		if content != from {
			return "oldString not found", nil
		}
		content = to
		return "ok", nil
	}}
	calls := []stream.ToolCall{
		{ID: "t1", Name: "write", Arguments: `{"from":"A","to":"B"}`},
		{ID: "t2", Name: "write", Arguments: `{"from":"B","to":"C"}`},
	}
	results, _, failed := executeToolsDispatched(ctx, runner, calls, d)
	for i, r := range results {
		if failed[i] || r != "ok" {
			t.Errorf("result %d = %q failed=%v, want ok", i, r, failed[i])
		}
	}
	if content != "C" {
		t.Errorf("content = %q, want C", content)
	}
}
