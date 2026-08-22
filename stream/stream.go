package stream

import (
	"context"
	"time"
)

type Event interface{ sealed() }

type ThinkingDelta struct{ Text string }

func (ThinkingDelta) sealed() {}

type TextDelta struct{ Text string }

func (TextDelta) sealed() {}

type ToolCallStart struct {
	ID   string
	Name string
}

func (ToolCallStart) sealed() {}

type ToolCallDelta struct {
	ID    string
	Delta string
}

func (ToolCallDelta) sealed() {}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func (ToolCall) sealed() {}

type Finish struct {
	Reason string
	Usage  Usage
}

func (Finish) sealed() {}

type StreamError struct{ Err error }

func (StreamError) sealed() {}

// There is deliberately no Retry event. Retrying is the agent loop's job, not
// the stream's: api.RetryableError travels up from the connector and
// agent.Run re-runs the whole request, reporting each attempt through the
// Sink (see agent/agent.go's backoff loop). A mid-stream retry event would
// need semantics this pipeline does not have — the consumer accumulates text,
// thinking and tool deltas as they arrive, so an event meaning "discard
// everything so far and start over" would have to reset all of them, and one
// emitted after the first token would otherwise splice two attempts into one
// garbled reply. A Retry type existed here for a while, unemitted by anything
// and quietly inviting exactly that bug.

type Usage struct {
	Input      int
	Output     int
	Reasoning  int
	CacheRead  int
	CacheWrite int
}

// Add accumulates another Usage into this one.
func (u *Usage) Add(other Usage) {
	u.Input += other.Input
	u.Output += other.Output
	u.Reasoning += other.Reasoning
	u.CacheRead += other.CacheRead
	u.CacheWrite += other.CacheWrite
}

type Stats struct {
	Duration   time.Duration
	FirstToken time.Duration
}

// ToolIdxCtxKey is the context key for passing tool index to streaming tools.
type ToolIdxCtxKey struct{}

// OutputFunc is called by streaming tools (e.g. bash) to forward a line of
// output to the display layer.
type OutputFunc func(toolIdx int, line string)

type outputCtxKey struct{}

// WithOutput returns a child context that carries the given OutputFunc.
func WithOutput(ctx context.Context, f OutputFunc) context.Context {
	return context.WithValue(ctx, outputCtxKey{}, f)
}

// Output extracts the OutputFunc from ctx, or returns nil.
func Output(ctx context.Context) OutputFunc {
	if ctx == nil {
		return nil
	}
	if f, ok := ctx.Value(outputCtxKey{}).(OutputFunc); ok {
		return f
	}
	return nil
}
