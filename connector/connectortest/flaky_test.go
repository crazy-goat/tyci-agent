package connectortest

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/stream"
)

// scripted returns a Fake that emits the same three-event turn every call.
func scripted() *Fake {
	turn := []stream.Event{
		stream.TextDelta{Text: "partial "},
		stream.TextDelta{Text: "answer"},
		stream.Finish{Reason: "stop"},
	}
	return &Fake{
		ProviderName: "p",
		ModelName:    "m",
		OnExhausted:  turn,
	}
}

// Identity passes through: a flaky model is still the same model, and the
// fallback messages the agent renders quote provider/model.
func TestFlaky_DelegatesIdentity(t *testing.T) {
	f := &Flaky{Client: &Fake{ProviderName: "prov", ModelName: "mod"}}
	if connector.FullModel(f) != "prov/mod" {
		t.Fatalf("FullModel = %q, want prov/mod", connector.FullModel(f))
	}
}

// With no failures scripted, Flaky is transparent.
func TestFlaky_PassesThroughWhenNotScripted(t *testing.T) {
	inner := scripted()
	f := &Flaky{Client: inner}

	ch, err := f.Stream(context.Background(), connector.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := collect(t, ch); len(got) != 3 {
		t.Fatalf("events = %#v, want the wrapped client's 3", got)
	}
	if inner.Calls() != 1 {
		t.Errorf("wrapped client called %d times, want 1", inner.Calls())
	}
}

// Failure with MidStream false is the fallback path: Stream itself fails and
// the wrapped client is never reached.
func TestFlaky_ErrorFromStreamItself(t *testing.T) {
	inner := scripted()
	f := &Flaky{Client: inner, Failures: []Failure{{Err: ServerError()}}}

	ch, err := f.Stream(context.Background(), connector.Request{})
	if err == nil {
		t.Fatal("Stream succeeded, want the injected error")
	}
	if ch != nil {
		t.Error("Stream returned a channel alongside an error")
	}
	if inner.Calls() != 0 {
		t.Errorf("wrapped client was called %d times; a request that never left should not consume its script", inner.Calls())
	}
}

// The case that justifies this type: N events of a real answer, then a 429.
// No hand-written fake in the suite could produce it, and it is the only
// input that drives agent/agent.go's retry loop from a partial response.
func TestFlaky_ErrorMidStream(t *testing.T) {
	inner := scripted()
	injected := RateLimited("2")
	f := &Flaky{Client: inner, Failures: []Failure{{
		Err:         injected,
		MidStream:   true,
		AfterEvents: 2,
	}}}

	ch, err := f.Stream(context.Background(), connector.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got := collect(t, ch)
	if len(got) != 3 {
		t.Fatalf("events = %#v, want 2 forwarded + 1 StreamError", got)
	}
	if td, ok := got[0].(stream.TextDelta); !ok || td.Text != "partial " {
		t.Errorf("got[0] = %#v, want the wrapped client's first event", got[0])
	}
	if td, ok := got[1].(stream.TextDelta); !ok || td.Text != "answer" {
		t.Errorf("got[1] = %#v, want the wrapped client's second event", got[1])
	}
	se, ok := got[2].(stream.StreamError)
	if !ok || !errors.Is(se.Err, injected) {
		t.Fatalf("got[2] = %#v, want StreamError carrying the injected error", got[2])
	}
	// The wrapped client's Finish must NOT have been forwarded: a turn that
	// finished cleanly is not a turn that failed.
	for _, ev := range got {
		if _, ok := ev.(stream.Finish); ok {
			t.Errorf("a Finish leaked past the injected failure: %#v", got)
		}
	}
}

// AfterEvents 0 fails the stream before anything is emitted, which is still a
// mid-stream failure as far as the agent is concerned: Stream succeeded.
func TestFlaky_MidStreamBeforeAnyEvent(t *testing.T) {
	f := &Flaky{Client: scripted(), Failures: []Failure{{
		Err:       UnexpectedEOF(),
		MidStream: true,
	}}}

	ch, err := f.Stream(context.Background(), connector.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got := collect(t, ch)
	if len(got) != 1 {
		t.Fatalf("events = %#v, want only the StreamError", got)
	}
	if se, ok := got[0].(stream.StreamError); !ok || !errors.Is(se.Err, io.ErrUnexpectedEOF) {
		t.Fatalf("got[0] = %#v", got[0])
	}
}

// AfterEvents past the end of the wrapped turn still injects: the failure is
// appended once the wrapped stream runs out.
func TestFlaky_MidStreamAfterMoreEventsThanExist(t *testing.T) {
	f := &Flaky{Client: scripted(), Failures: []Failure{{
		Err:         ServerError(),
		MidStream:   true,
		AfterEvents: 99,
	}}}

	ch, _ := f.Stream(context.Background(), connector.Request{})
	got := collect(t, ch)
	if len(got) != 4 {
		t.Fatalf("events = %#v, want the whole turn plus a StreamError", got)
	}
	if _, ok := got[3].(stream.StreamError); !ok {
		t.Fatalf("last event = %#v, want StreamError", got[3])
	}
}

// Failures are per call, so "fail twice then succeed" — the shape every retry
// test needs — is a literal.
func TestFlaky_FailuresArePerCall(t *testing.T) {
	inner := scripted()
	f := &Flaky{Client: inner, Failures: []Failure{
		{Err: RateLimited("1")},
		{Err: RateLimited("1")},
		{}, // nil Err: this one goes through
	}}

	for i := 0; i < 2; i++ {
		if _, err := f.Stream(context.Background(), connector.Request{}); err == nil {
			t.Fatalf("call %d succeeded, want the injected 429", i)
		}
	}
	ch, err := f.Stream(context.Background(), connector.Request{})
	if err != nil {
		t.Fatalf("third call: %v", err)
	}
	if got := collect(t, ch); len(got) != 3 {
		t.Fatalf("third call events = %#v, want the wrapped client's 3", got)
	}
	// A fourth call is past the end of the script and also passes through.
	if _, err := f.Stream(context.Background(), connector.Request{}); err != nil {
		t.Fatalf("fourth call: %v", err)
	}
	if f.Calls() != 4 || inner.Calls() != 2 {
		t.Errorf("calls: flaky=%d inner=%d, want 4 and 2", f.Calls(), inner.Calls())
	}
}

// The point of the error constructors is not that they exist but that the
// real consumer classifies them as retryable. Without this the whole type
// would be a fake driving a code path it never actually reaches.
func TestFlaky_InjectedErrorsAreRetryable(t *testing.T) {
	cases := map[string]error{
		"429 with Retry-After": RateLimited("10"),
		"429 without header":   RateLimited(""),
		"500":                  ServerError(),
		"unexpected EOF":       UnexpectedEOF(),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if !api.IsRetryable(err) {
				t.Fatalf("api.IsRetryable(%v) = false; the agent would take the error path, not the retry one", err)
			}
		})
	}
}

// RateLimited's Retry-After has to survive as far as api.CalcBackoff, which is
// what actually reads it.
func TestFlaky_RateLimitedRetryAfterReachesBackoff(t *testing.T) {
	err := RateLimited("7")
	got := api.CalcBackoff(0, err, api.RetryConfig{}.WithDefaults())
	if got.Seconds() != 7 {
		t.Fatalf("CalcBackoff = %v, want 7s from the Retry-After header", got)
	}
}

// A mid-stream failure that reaches the consumer as a StreamError must still
// be classifiable: agent/run_once.go returns StreamError.Err as the run's
// error and agent.go hands that to api.IsRetryable.
func TestFlaky_MidStreamErrorSurvivesAsRetryable(t *testing.T) {
	f := &Flaky{Client: scripted(), Failures: []Failure{{
		Err: RateLimited("1"), MidStream: true, AfterEvents: 1,
	}}}
	ch, _ := f.Stream(context.Background(), connector.Request{})
	got := collect(t, ch)

	se, ok := got[len(got)-1].(stream.StreamError)
	if !ok {
		t.Fatalf("last event = %#v, want StreamError", got[len(got)-1])
	}
	if !api.IsRetryable(se.Err) {
		t.Fatal("the error carried by StreamError is not retryable")
	}
}

// A canceled context must not leave the injecting goroutine wedged.
func TestFlaky_CancelDuringInjection(t *testing.T) {
	f := &Flaky{Client: &Fake{BlockUntilCancel: true}, Failures: []Failure{{
		Err: ServerError(), MidStream: true, AfterEvents: 1,
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := f.Stream(ctx, connector.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	cancel()
	collect(t, ch) // must return, not hang
}
