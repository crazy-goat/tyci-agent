package stream

import "time"

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

// OnOutput is set by the display (TUI) to receive streaming tool output.
// toolIdx is the 0-based index of the tool in the current batch.
var OnOutput func(toolIdx int, line string)
