package connectortest

import (
	"context"
	"io"
	"sync"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/stream"
)

// Failure describes what happens on one Stream call of a Flaky.
type Failure struct {
	// Err is the failure to inject. A zero Failure (Err == nil) means the
	// call passes through to the wrapped client untouched, which is how a
	// script like "429, 429, then success" is written.
	Err error

	// MidStream selects WHERE Err is injected, and the agent handles the
	// two places with different code:
	//
	//	false → Stream itself returns Err and no channel. This is what
	//	        agent/fallback.go reacts to.
	//	true  → the wrapped client's stream starts normally, the first
	//	        AfterEvents events are forwarded, and then
	//	        stream.StreamError{Err: Err} ends the turn. This is what
	//	        agent/agent.go's retry loop reacts to.
	//
	// The mid-stream case is the one no hand-written fake could produce:
	// a partial answer followed by a 429.
	MidStream bool

	// AfterEvents is how many of the wrapped client's events to forward
	// before injecting Err. Only meaningful when MidStream is true; 0 means
	// the stream fails before emitting anything. If the wrapped stream ends
	// on its own before AfterEvents, Err is injected right after it.
	AfterEvents int
}

// Flaky decorates any connector.ModelClient with per-call injected failures.
// Failures[n] applies to the n-th Stream call; calls past the end of the
// slice pass straight through, so a script ends by simply running out.
//
// Identity (Provider, Model) is the wrapped client's: a flaky model is still
// the same model, and the fallback messages the agent renders must say so.
type Flaky struct {
	// Client is the wrapped client. Required.
	Client connector.ModelClient
	// Failures is the per-call script; see Failure.
	Failures []Failure

	mu    sync.Mutex
	calls int
}

var _ connector.ModelClient = (*Flaky)(nil)

// Provider implements connector.ModelClient.
func (f *Flaky) Provider() string { return f.Client.Provider() }

// Model implements connector.ModelClient.
func (f *Flaky) Model() string { return f.Client.Model() }

// Calls reports how many times Stream was called.
func (f *Flaky) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Stream implements connector.ModelClient.
func (f *Flaky) Stream(ctx context.Context, req connector.Request) (<-chan stream.Event, error) {
	f.mu.Lock()
	n := f.calls
	f.calls++
	f.mu.Unlock()

	var fail Failure
	if n < len(f.Failures) {
		fail = f.Failures[n]
	}

	// Fail before the request goes out: the wrapped client is never called,
	// so its own script is not consumed either.
	if fail.Err != nil && !fail.MidStream {
		return nil, fail.Err
	}

	ch, err := f.Client.Stream(ctx, req)
	if err != nil || fail.Err == nil {
		return ch, err
	}

	out := make(chan stream.Event, 16)
	go func() {
		defer close(out)
		// Whatever happens below, the wrapped client's goroutine must not
		// be left blocked on a send nobody will receive.
		defer drain(ch)

		sent := 0
		for sent < fail.AfterEvents {
			ev, ok := <-ch
			if !ok {
				break
			}
			select {
			case out <- ev:
				sent++
			case <-ctx.Done():
				return
			}
		}
		select {
		case out <- stream.StreamError{Err: fail.Err}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

// drain consumes the rest of ch in the background.
func drain(ch <-chan stream.Event) {
	go func() {
		for range ch {
		}
	}()
}

// The constructors below produce errors that api.IsRetryable recognises, so a
// Flaky built from them drives the agent's retry loop rather than its error
// path. TestFlaky_InjectedErrorsAreRetryable pins that.

// RateLimited returns the 429 a provider sends when it wants the client to
// back off. retryAfter is the raw Retry-After header value ("10" for ten
// seconds); api.CalcBackoff honours it. Pass "" to leave the header out.
func RateLimited(retryAfter string) error {
	return &api.RetryableError{
		Code:       429,
		RetryAfter: retryAfter,
		Message:    "429 rate limited: injected by connectortest",
	}
}

// ServerError returns the 500 a provider sends when it fails on its own side.
func ServerError() error {
	return &api.RetryableError{
		Code:    500,
		Message: "500 server error: injected by connectortest",
	}
}

// UnexpectedEOF returns the error a stream cut in half produces. Unlike the
// two above it is not an api.RetryableError — it is the plain io error, which
// is exactly what makes it worth having: api.IsRetryable has a separate
// branch for it and that branch deserves a fake that reaches it.
func UnexpectedEOF() error { return io.ErrUnexpectedEOF }
