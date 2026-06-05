package display

import "github.com/decodo/tyci-agent/stream"

type ToolResult struct {
	Success bool
	Content string
	Error   string
}

type Display interface {
	Thinking(text string)
	Text(text string)
	ToolCallStart(name, args string)
	ToolCallEnd(name string, result string)
	Summary(usage stream.Usage, stats stream.Stats)
	Error(err error)
	End()
}
