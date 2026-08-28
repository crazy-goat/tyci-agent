package api

import (
	"context"
	"net/http/httptrace"
	"sync"

	"github.com/decodo/tyci/stream"
)

// withPhaseTrace returns a child of ctx carrying an httptrace.ClientTrace
// that emits the two transport-phase events (stream.RequestSent,
// stream.ResponseStarted) the display's status bar needs to tell "sending
// request" apart from "waiting for response" apart from the model's own
// output — see those types' doc comments in package stream. Every streamer
// (ChatStreamer, ResponsesStreamer, AnthropicStreamer, GeminiStreamer) wraps
// its ctx with this before building the *http.Request.
//
// Each hook is wrapped in its own sync.Once: net/http can retry a request
// (a dead keep-alive connection, an HTTP/2 GOAWAY) and would otherwise fire
// WroteRequest or GotFirstResponseByte a second time for the same logical
// call, which would rewind the status bar's elapsed clock mid-phase.
//
// Goroutine safety: these hooks run on the transport's own write/read-loop
// goroutines, never the streamer's. That is safe here because emit (see
// providers/config.go's forward, the only production implementation) does
// nothing but a select-guarded send on a buffered channel — safe to call
// concurrently from any goroutine — and because net/http guarantees both
// hooks complete before the http.Client.Do call that triggered them
// returns. Do() has not returned yet, so the streamer's own goroutine is
// still blocked inside it: it cannot have started its read loop, emitted
// any other event, or closed the channel, so there is no possible send
// after close and no interleaving with the streamer's own emits to race
// against.
func withPhaseTrace(ctx context.Context, emit func(stream.Event) error) context.Context {
	var wroteOnce, firstByteOnce sync.Once
	trace := &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				// The write failed, so the request never actually finished
				// sending — leave the phase alone and let the surrounding
				// error path (a non-200 response or the transport error
				// returned to the caller) speak for what happened instead.
				return
			}
			wroteOnce.Do(func() { _ = emit(stream.RequestSent{}) })
		},
		GotFirstResponseByte: func() {
			firstByteOnce.Do(func() { _ = emit(stream.ResponseStarted{}) })
		},
	}
	return httptrace.WithClientTrace(ctx, trace)
}
