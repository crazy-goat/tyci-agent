// Package connectortest provides test doubles for connector.ModelClient.
//
// Before this package every test that needed a model wrote its own fake:
// sixteen of them across nine files, each replaying a script, returning an
// error or hanging until cancellation, and each slightly different in ways
// the tests around them quietly depended on. Fake is the union of what those
// doubles actually did; Flaky (flaky.go) covers the one thing none of them
// could do — failing in the middle of a stream.
//
// Configuration is by struct literal, matching conductor.Options, agent.Config
// and connector.Endpoint. There are no functional options here on purpose:
// a test double whose configuration reads like the rest of the codebase is one
// less thing to learn.
package connectortest

import (
	"context"
	"sync"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/stream"
)

// Fake is a connector.ModelClient that replays one scripted slice of
// stream.Event per Stream call and records the requests it was given.
//
// The zero value is usable: it answers as "fake/fake-1" and finishes every
// turn immediately with Finish{Reason: "stop"}.
//
// Fake deliberately does NOT implement connector.HTTPInjector. That interface
// has an intentionally silent fallback on a failed type assertion (see the
// comment on connector.HTTPInjector), and several tests exist precisely to
// pin the behavior of a client that does not implement it — main.go's
// withIsolatedPool keeping the shared HTTP client, for one. A Fake that
// satisfied HTTPInjector would stop those tests from testing anything. A
// variant that needs WithHTTP belongs in a separate type.
type Fake struct {
	// ProviderName is what Provider() returns; empty means "fake".
	ProviderName string
	// ModelName is what Model() returns; empty means "fake-1".
	ModelName string

	// Turns[n] is the event sequence replayed by the n-th Stream call.
	// Calls past the end of Turns replay OnExhausted.
	Turns [][]stream.Event

	// OnExhausted is what to emit once Turns has run out. The distinction
	// between the two empty forms is load-bearing:
	//
	//	nil                  → emit Finish{Reason: "stop"}, so an agent
	//	                       loop that keeps calling stops instead of
	//	                       spinning. This is what every hand-written
	//	                       fake did.
	//	[]stream.Event{}     → emit nothing at all; the channel is simply
	//	                       closed. A turn with no Finish and no content.
	//
	// If you want "close the channel silently", you must write the empty
	// literal; leaving the field out gives you a Finish.
	OnExhausted []stream.Event

	// StreamErr, when non-nil, makes Stream return that error and no
	// channel — the shape a real client has when the request never got off
	// the ground (bad credentials, unreachable host). The script is not
	// consulted. The call is still counted and its request still recorded.
	StreamErr error

	// BlockUntilCancel makes Stream ignore the script, hang, and report the
	// cancellation the way every real connector does: as a single
	// stream.StreamError carrying ctx.Err(). Without this there is no way
	// to test Conductor.Interrupt or any ESC path, because nothing else
	// keeps a turn in flight long enough to interrupt it.
	BlockUntilCancel bool

	mu       sync.Mutex
	calls    int
	requests []connector.Request
}

// Fake is a ModelClient and nothing more — see the type comment for why it is
// not also an HTTPInjector.
var _ connector.ModelClient = (*Fake)(nil)

// Provider implements connector.ModelClient.
func (f *Fake) Provider() string {
	if f.ProviderName == "" {
		return "fake"
	}
	return f.ProviderName
}

// Model implements connector.ModelClient.
func (f *Fake) Model() string {
	if f.ModelName == "" {
		return "fake-1"
	}
	return f.ModelName
}

// Stream implements connector.ModelClient.
func (f *Fake) Stream(ctx context.Context, req connector.Request) (<-chan stream.Event, error) {
	f.mu.Lock()
	turn := f.calls
	f.calls++
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	if f.StreamErr != nil {
		return nil, f.StreamErr
	}

	events := f.eventsFor(turn)

	ch := make(chan stream.Event, 16)
	go func() {
		defer close(ch)
		if f.BlockUntilCancel {
			<-ctx.Done()
			ch <- stream.StreamError{Err: ctx.Err()}
			return
		}
		for _, ev := range events {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// eventsFor picks the script for the turn-th call. See OnExhausted for why
// nil and the empty slice mean different things.
func (f *Fake) eventsFor(turn int) []stream.Event {
	if turn < len(f.Turns) {
		return f.Turns[turn]
	}
	if f.OnExhausted == nil {
		return []stream.Event{stream.Finish{Reason: "stop"}}
	}
	return f.OnExhausted
}

// Calls reports how many times Stream was called, including calls that
// returned StreamErr.
func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Requests returns a copy of the requests Stream was handed, in order. It is
// a copy so a test goroutine can read it while another Stream is running.
func (f *Fake) Requests() []connector.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]connector.Request, len(f.requests))
	copy(out, f.requests)
	return out
}

// Text returns a Fake whose single turn emits one TextDelta per chunk and
// then Finish{Reason: "stop"} with no usage.
//
// The zero Usage is deliberate. Hand-written fakes disagreed about it —
// agent's mockProvider ends on Finish{Usage: {Input: 1, Output: 1}}, its
// mockToolProvider on Finish{Usage: {}} — and tests assert on the numbers.
// A helper that guessed would make that difference invisible at the call
// site, so a turn with usage is written out as a Turns literal instead.
func Text(chunks ...string) *Fake {
	turn := make([]stream.Event, 0, len(chunks)+1)
	for _, c := range chunks {
		turn = append(turn, stream.TextDelta{Text: c})
	}
	turn = append(turn, stream.Finish{Reason: "stop"})
	return &Fake{Turns: [][]stream.Event{turn}}
}

// Failing returns a Fake whose every Stream call fails with err before any
// channel exists — the fallback path in agent/fallback.go, not the retry one.
// For a failure part-way through a stream, use Flaky.
func Failing(err error) *Fake {
	return &Fake{StreamErr: err}
}
