package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/stream"
)

// TestEscCancelPublishesCancelledResult cancels the turn while a call runs.
// The call must end with one failed tool.result that says it was cancelled.
func TestEscCancelPublishesCancelledResult(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	sub := b.Subscribe("test", bus.Filter{To: testSelf, Kinds: []bus.Kind{bus.KindToolResult}})
	defer sub.Close()
	d := newDispatcher(b, testSelf, sub)

	// "other_tool" waits 2.5s, so the cancel at 2.6s hits a backgrounded call.
	// The cancelled result must then reach the bus.
	ctx, cancel := context.WithCancel(context.Background())
	runner := fakeRunner{fn: func(ctx context.Context, _ string, _ map[string]any) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	time.AfterFunc(2600*time.Millisecond, cancel)
	executeToolsDispatched(ctx, runner, []stream.ToolCall{{ID: "t1", Name: "other_tool"}}, d)

	got := waitToolResult(t, d, "c1")
	if !got.Failed || got.Content != "cancelled: the turn was interrupted" {
		t.Errorf("cancelled result = %+v", got)
	}
}

// TestJournalOnlyFallbackOnClosedBus checks that a late result is kept in
// memory when the bus cannot take it.
func TestJournalOnlyFallbackOnClosedBus(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	sub := b.Subscribe("test", bus.Filter{To: testSelf, Kinds: []bus.Kind{bus.KindToolResult}})
	d := newDispatcher(b, testSelf, sub)

	release := make(chan struct{})
	id := d.Fire(context.Background(), func(context.Context) (string, bool) {
		<-release
		return "kept", false
	})
	if out := d.Await(context.Background(), id, 0); !out.Background {
		t.Fatalf("outcome = %+v, want background", out)
	}
	b.Close()
	close(release)
	got := waitToolResult(t, d, id)
	if got.Content != "kept" {
		t.Errorf("late result = %+v", got)
	}
}

// TestDispatcherWaitsForInFlightCalls checks the headless wait.
func TestDispatcherWaitsForInFlightCalls(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	sub := b.Subscribe("test", bus.Filter{To: testSelf, Kinds: []bus.Kind{bus.KindToolResult}})
	defer sub.Close()
	d := newDispatcher(b, testSelf, sub)

	id := d.Fire(context.Background(), func(context.Context) (string, bool) {
		time.Sleep(30 * time.Millisecond)
		return "done", false
	})
	d.Await(context.Background(), id, 0)
	d.Wait(context.Background())
	if n := d.Pending(); n != 0 {
		t.Errorf("pending = %d after Wait, want 0", n)
	}
}

// TestBadJSONIsInlineNoPlaceholder checks that bad arguments never get a
// placeholder and never start a call.
func TestBadJSONIsInlineNoPlaceholder(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	sub := b.Subscribe("test", bus.Filter{To: testSelf, Kinds: []bus.Kind{bus.KindToolResult}})
	defer sub.Close()
	d := newDispatcher(b, testSelf, sub)

	ran := false
	runner := fakeRunner{fn: func(context.Context, string, map[string]any) (string, error) {
		ran = true
		return "", nil
	}}
	results, _, failed := executeToolsDispatched(context.Background(), runner,
		[]stream.ToolCall{{ID: "t1", Name: "read", Arguments: "{bad"}}, d)
	if !failed[0] || !strings.HasPrefix(results[0], "Error: invalid arguments") {
		t.Errorf("result = %q failed=%v, want inline error", results[0], failed[0])
	}
	if ran || d.Pending() != 0 {
		t.Errorf("call ran=%v pending=%d, want neither", ran, d.Pending())
	}
}

// TestResumeClosesOpenCalls checks that a restarted session gets a failed
// result for every tool call that has none.
func TestResumeClosesOpenCalls(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "toolCall", ID: "t1", Name: "bash"}}},
	}
	out := CloseOpenToolCalls(msgs)
	if len(out) != 3 {
		t.Fatalf("got %d messages, want 3", len(out))
	}
	last := out[2].Content[0]
	if out[2].Role != "toolResult" || last.ToolCallID != "t1" || !last.IsError ||
		last.Text != "cancelled: session was restarted" {
		t.Errorf("synthetic result = %+v", out[2])
	}
	if len(CloseOpenToolCalls(out)) != 3 {
		t.Error("a second pass must not add a result")
	}
}
