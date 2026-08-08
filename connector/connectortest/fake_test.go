package connectortest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/stream"
)

// collect drains ch until it closes and returns everything it saw.
func collect(t *testing.T, ch <-chan stream.Event) []stream.Event {
	t.Helper()
	var out []stream.Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

// The zero value has to be usable, because most call sites only care about
// "a model that answers something".
func TestFake_ZeroValueAnswers(t *testing.T) {
	f := &Fake{}
	if f.Provider() != "fake" || f.Model() != "fake-1" {
		t.Fatalf("identity = %s/%s, want fake/fake-1", f.Provider(), f.Model())
	}

	ch, err := f.Stream(context.Background(), connector.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got := collect(t, ch)
	if len(got) != 1 || got[0] != (stream.Finish{Reason: "stop"}) {
		t.Fatalf("events = %#v, want one Finish{stop}", got)
	}
	if f.Calls() != 1 {
		t.Errorf("Calls() = %d, want 1", f.Calls())
	}
}

func TestFake_IdentityOverrides(t *testing.T) {
	f := &Fake{ProviderName: "p", ModelName: "m"}
	if got := connector.FullModel(f); got != "p/m" {
		t.Errorf("FullModel = %q, want p/m", got)
	}
}

// Turns[n] belongs to the n-th call, not to the first one repeated.
func TestFake_ReplaysOneTurnPerCall(t *testing.T) {
	f := &Fake{Turns: [][]stream.Event{
		{stream.TextDelta{Text: "first"}, stream.Finish{Reason: "tool_calls"}},
		{stream.TextDelta{Text: "second"}, stream.Finish{Reason: "stop"}},
	}}

	for i, want := range []string{"first", "second"} {
		ch, err := f.Stream(context.Background(), connector.Request{})
		if err != nil {
			t.Fatalf("call %d: Stream: %v", i, err)
		}
		got := collect(t, ch)
		if len(got) != 2 {
			t.Fatalf("call %d: %d events, want 2", i, len(got))
		}
		if td, ok := got[0].(stream.TextDelta); !ok || td.Text != want {
			t.Errorf("call %d: first event = %#v, want TextDelta{%q}", i, got[0], want)
		}
	}
	if f.Calls() != 2 {
		t.Errorf("Calls() = %d, want 2", f.Calls())
	}
}

// A nil OnExhausted finishes the turn, so an agent loop that keeps asking
// stops instead of spinning. This is what every hand-written fake did.
func TestFake_ExhaustedNilFinishes(t *testing.T) {
	f := &Fake{Turns: [][]stream.Event{{stream.Finish{Reason: "tool_calls"}}}}

	_, _ = f.Stream(context.Background(), connector.Request{}) // consume turn 0
	ch, err := f.Stream(context.Background(), connector.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got := collect(t, ch)
	if len(got) != 1 || got[0] != (stream.Finish{Reason: "stop"}) {
		t.Fatalf("events = %#v, want one Finish{stop}", got)
	}
}

// An empty-but-non-nil OnExhausted means the opposite: emit nothing, just
// close. The two forms differ on purpose and this is the test that says so.
func TestFake_ExhaustedEmptySliceEmitsNothing(t *testing.T) {
	f := &Fake{OnExhausted: []stream.Event{}}

	ch, err := f.Stream(context.Background(), connector.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := collect(t, ch); len(got) != 0 {
		t.Fatalf("events = %#v, want none", got)
	}
}

func TestFake_ExhaustedCustomScript(t *testing.T) {
	f := &Fake{OnExhausted: []stream.Event{stream.TextDelta{Text: "again"}, stream.Finish{}}}

	for i := 0; i < 3; i++ {
		ch, _ := f.Stream(context.Background(), connector.Request{})
		got := collect(t, ch)
		if len(got) != 2 {
			t.Fatalf("call %d: %d events, want 2", i, len(got))
		}
	}
}

// StreamErr fails before any channel exists — the shape agent/fallback.go
// reacts to. The call is still counted, so per-call assertions keep working.
func TestFake_StreamErr(t *testing.T) {
	sentinel := errors.New("boom")
	f := &Fake{StreamErr: sentinel, Turns: [][]stream.Event{{stream.Finish{}}}}

	ch, err := f.Stream(context.Background(), connector.Request{Model: "m"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if ch != nil {
		t.Errorf("Stream returned a channel alongside an error")
	}
	if f.Calls() != 1 {
		t.Errorf("Calls() = %d, want 1 (a failed call is still a call)", f.Calls())
	}
	if got := f.Requests(); len(got) != 1 || got[0].Model != "m" {
		t.Errorf("Requests() = %#v, want the failed request recorded", got)
	}
}

// BlockUntilCancel is the mode that makes Conductor.Interrupt and every ESC
// path testable: the turn stays in flight until someone cancels it, and the
// cancellation arrives as a stream.StreamError, exactly as a real connector
// reports it.
func TestFake_BlockUntilCancel(t *testing.T) {
	f := &Fake{BlockUntilCancel: true, Turns: [][]stream.Event{{stream.TextDelta{Text: "ignored"}}}}
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := f.Stream(ctx, connector.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	select {
	case ev, ok := <-ch:
		t.Fatalf("stream produced %#v (open=%v) before cancellation", ev, ok)
	default:
	}

	cancel()
	got := collect(t, ch)
	if len(got) != 1 {
		t.Fatalf("events = %#v, want one StreamError", got)
	}
	se, ok := got[0].(stream.StreamError)
	if !ok || !errors.Is(se.Err, context.Canceled) {
		t.Fatalf("event = %#v, want StreamError{context.Canceled}", got[0])
	}
}

// A cancelled context stops the replay instead of blocking forever on a
// channel nobody reads.
func TestFake_CancelStopsReplay(t *testing.T) {
	long := make([]stream.Event, 100)
	for i := range long {
		long[i] = stream.TextDelta{Text: "x"}
	}
	f := &Fake{Turns: [][]stream.Event{long}}

	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := f.Stream(ctx, connector.Request{})
	cancel()

	// The goroutine must reach close(ch); how many events made it into the
	// buffer first is a race we do not care about.
	if got := collect(t, ch); len(got) > len(long) {
		t.Fatalf("got %d events, more than the script had", len(got))
	}
}

func TestFake_RecordsRequestsInOrder(t *testing.T) {
	f := &Fake{}
	for _, m := range []string{"a", "b", "c"} {
		ch, _ := f.Stream(context.Background(), connector.Request{Model: m})
		collect(t, ch)
	}
	got := f.Requests()
	if len(got) != 3 || got[0].Model != "a" || got[2].Model != "c" {
		t.Fatalf("Requests() = %#v", got)
	}
}

// Requests hands back a copy: a test that sorts or truncates what it got must
// not be able to corrupt the fake's own record.
func TestFake_RequestsReturnsCopy(t *testing.T) {
	f := &Fake{}
	ch, _ := f.Stream(context.Background(), connector.Request{Model: "original"})
	collect(t, ch)

	got := f.Requests()
	got[0].Model = "clobbered"

	if again := f.Requests(); again[0].Model != "original" {
		t.Fatalf("mutating the returned slice changed the fake: %q", again[0].Model)
	}
}

// Calls and Requests are read from the test goroutine while Stream runs in
// another. Under -race this is the test that would catch an unguarded field.
func TestFake_ConcurrentAccessIsSafe(t *testing.T) {
	f := &Fake{BlockUntilCancel: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.Stream(ctx, connector.Request{Model: "m"})
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = f.Calls()
			_ = f.Requests()
		}()
	}
	wg.Wait()

	if f.Calls() != 8 {
		t.Errorf("Calls() = %d, want 8", f.Calls())
	}
}

// Fake must stay outside connector.HTTPInjector. Callers type-assert to that
// interface and silently keep their shared HTTP client when the assertion
// fails; tests covering that fallback need a client that genuinely lacks the
// method, and this is the assertion that keeps one available.
func TestFake_DoesNotImplementHTTPInjector(t *testing.T) {
	var mc connector.ModelClient = &Fake{}
	if _, ok := mc.(connector.HTTPInjector); ok {
		t.Fatal("Fake implements connector.HTTPInjector; the silent-fallback tests would stop testing it")
	}
}

func TestText(t *testing.T) {
	f := Text("hello ", "world")
	ch, err := f.Stream(context.Background(), connector.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got := collect(t, ch)
	if len(got) != 3 {
		t.Fatalf("events = %#v, want 2 deltas and a Finish", got)
	}
	if td, ok := got[0].(stream.TextDelta); !ok || td.Text != "hello " {
		t.Errorf("got[0] = %#v", got[0])
	}
	if td, ok := got[1].(stream.TextDelta); !ok || td.Text != "world" {
		t.Errorf("got[1] = %#v", got[1])
	}
	// Zero usage, on purpose: a turn that reports usage says so at the call
	// site rather than inheriting numbers from a helper.
	if got[2] != (stream.Finish{Reason: "stop"}) {
		t.Errorf("got[2] = %#v, want Finish{stop} with no usage", got[2])
	}
}

func TestFailing(t *testing.T) {
	sentinel := errors.New("nope")
	f := Failing(sentinel)
	if _, err := f.Stream(context.Background(), connector.Request{}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if _, err := f.Stream(context.Background(), connector.Request{}); !errors.Is(err, sentinel) {
		t.Fatalf("second call err = %v, want the same error", err)
	}
}
