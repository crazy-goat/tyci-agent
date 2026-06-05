package stream

import "time"

type Event interface{ sealed() }

type ThinkingDelta struct{ Text string }
func (ThinkingDelta) sealed() {}

type TextDelta struct{ Text string }
func (TextDelta) sealed() {}

type ToolCall struct {
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
