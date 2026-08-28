package api

import (
	"context"
	"net/http/httptrace"
	"sync"

	"github.com/decodo/tyci/stream"
)

// phaseTrace owns the hand-off between the transport's hook goroutines and
// the emit callback. Hooks never call emit directly: they hand the event to
// a tiny buffered channel that a dedicated forwarder goroutine drains, and
// stop() closes that channel and waits for the forwarder to finish before
// returning. That is what makes it safe for a streamer to close its events
// channel right after Do() returns — see stop's doc comment below.
type phaseTrace struct {
	mu     sync.Mutex
	closed bool
	ch     chan stream.Event
	wg     sync.WaitGroup

	wroteOnce     sync.Once
	firstByteOnce sync.Once
}

// send is called from the transport's write/read-loop goroutines (see the
// ClientTrace hooks below). It must never block and must never touch ch
// after stop has closed it, so both the closed check and the send happen
// under the same lock.
func (pt *phaseTrace) send(ev stream.Event) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if pt.closed {
		return
	}
	select {
	case pt.ch <- ev:
	default:
		// Channel is cap 2 and holds at most one RequestSent and one
		// ResponseStarted; a full buffer means both already queued, so
		// dropping is fine — there is nothing left worth telling the bar.
	}
}

// stop closes the forwarding channel and blocks until the forwarder
// goroutine has drained it. Callers MUST call this immediately after their
// Do(req) call returns (success or error) and before doing anything else —
// that guarantees every phase emit has already happened by the time Stream
// can return, so the streamer's own defer close(ch) on its events channel
// (see providers/config.go) can never race a hook's send. It is also
// deferred at the call site to cover the case where Do is never reached
// (e.g. http.NewRequestWithContext itself fails); calling it twice is safe.
func (pt *phaseTrace) stop() {
	pt.mu.Lock()
	if pt.closed {
		pt.mu.Unlock()
		return
	}
	pt.closed = true
	close(pt.ch)
	pt.mu.Unlock()
	pt.wg.Wait()
}

// withPhaseTrace returns a child of ctx carrying an httptrace.ClientTrace
// that emits the two transport-phase events (stream.RequestSent,
// stream.ResponseStarted) the display's status bar needs to tell "sending
// request" apart from "waiting for response" apart from the model's own
// output — see those types' doc comments in package stream. Every streamer
// (ChatStreamer, ResponsesStreamer, AnthropicStreamer, GeminiStreamer) wraps
// its ctx with this before building the *http.Request, and MUST call the
// returned stop func right after its Do(req) call returns (see stop's doc
// comment) — deferring it too, so a failure before Do is reached still
// cleans up the forwarder goroutine.
//
// Each hook is wrapped in its own sync.Once: net/http can retry a request
// (a dead keep-alive connection, an HTTP/2 GOAWAY) and would otherwise fire
// WroteRequest or GotFirstResponseByte a second time for the same logical
// call, which would rewind the status bar's elapsed clock mid-phase.
//
// Goroutine safety: per `go doc net/http/httptrace.ClientTrace`, these hooks
// "may be called concurrently from different goroutines and some may be
// called after the request has completed or failed" — net/http gives NO
// ordering or completion guarantee here. The safety is entirely ours: send
// (above) is a lock-guarded, non-blocking hand-off to a buffered channel, so
// it is safe to call from any goroutine at any time, including after Do()
// has returned or concurrently with another hook. The forwarder goroutine
// started in withPhaseTrace is the only thing that calls emit, and stop
// waits for it to exit before the streamer proceeds, so emit is always
// called from one goroutine that is provably done before Stream returns.
func withPhaseTrace(ctx context.Context, emit func(stream.Event) error) (context.Context, func()) {
	pt := &phaseTrace{ch: make(chan stream.Event, 2)}
	pt.wg.Add(1)
	go func() {
		defer pt.wg.Done()
		for ev := range pt.ch {
			_ = emit(ev)
		}
	}()

	trace := &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				// The write failed, so the request never actually finished
				// sending — leave the phase alone and let the surrounding
				// error path (a non-200 response or the transport error
				// returned to the caller) speak for what happened instead.
				return
			}
			pt.wroteOnce.Do(func() { pt.send(stream.RequestSent{}) })
		},
		GotFirstResponseByte: func() {
			pt.firstByteOnce.Do(func() { pt.send(stream.ResponseStarted{}) })
		},
	}
	return httptrace.WithClientTrace(ctx, trace), pt.stop
}
