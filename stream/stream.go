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

type Retry struct {
	Attempt int
	Reason  string
	Delay   time.Duration
}
func (Retry) sealed() {}

type Usage struct {
	Input          int
	Output         int
	Reasoning      int
	CacheRead      int
	CacheWrite     int
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
