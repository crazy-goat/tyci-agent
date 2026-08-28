package api

import (
	"context"
	"net/http/httptrace"
	"sync"
	"testing"

	"github.com/decodo/tyci/stream"
)

// TestWithPhaseTrace_HookAfterStopIsNoOp is the fix for the round-1 finding:
// `go doc net/http/httptrace.ClientTrace` says hooks "may be called
// concurrently from different goroutines and some may be called after the
// request has completed or failed" — the opposite of what the old comment
// claimed. stop (called right after Do() returns, see its doc comment)
// must make any later hook call a silent no-op instead of a send on a
// closed channel, which used to panic.
func TestWithPhaseTrace_HookAfterStopIsNoOp(t *testing.T) {
	var mu sync.Mutex
	var emitted []stream.Event
	emit := func(e stream.Event) error {
		mu.Lock()
		emitted = append(emitted, e)
		mu.Unlock()
		return nil
	}

	ctx, stop := withPhaseTrace(context.Background(), emit)
	trace := httptrace.ContextClientTrace(ctx)
	if trace == nil {
		t.Fatal("expected a ClientTrace on the returned context")
	}

	// Simulate Stream() having already returned: the streamer calls stop
	// right after its Do(req) call comes back.
	stop()

	// A hook firing after stop, from another goroutine (exactly what
	// net/http's own doc says can happen), must be a no-op: no panic from a
	// send on a closed channel, and nothing forwarded to emit.
	done := make(chan struct{})
	go func() {
		defer close(done)
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		trace.GotFirstResponseByte()
	}()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 0 {
		t.Errorf("expected no events emitted after stop, got %d: %#v", len(emitted), emitted)
	}
}

// TestWithPhaseTrace_StopWaitsForForwarder pins the other half of the fix:
// stop must not return until every event already handed to the forwarder
// goroutine has been passed to emit, so a streamer that calls stop right
// after Do() and then proceeds can rely on both phase events (if they fired)
// having already been emitted.
func TestWithPhaseTrace_StopWaitsForForwarder(t *testing.T) {
	var mu sync.Mutex
	var emitted []stream.Event
	emit := func(e stream.Event) error {
		mu.Lock()
		emitted = append(emitted, e)
		mu.Unlock()
		return nil
	}

	ctx, stop := withPhaseTrace(context.Background(), emit)
	trace := httptrace.ContextClientTrace(ctx)

	trace.WroteRequest(httptrace.WroteRequestInfo{})
	trace.GotFirstResponseByte()
	stop()

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 2 {
		t.Fatalf("expected both phase events emitted by the time stop returns, got %d: %#v", len(emitted), emitted)
	}
	if _, ok := emitted[0].(stream.RequestSent); !ok {
		t.Errorf("emitted[0] = %T, want stream.RequestSent", emitted[0])
	}
	if _, ok := emitted[1].(stream.ResponseStarted); !ok {
		t.Errorf("emitted[1] = %T, want stream.ResponseStarted", emitted[1])
	}
}
