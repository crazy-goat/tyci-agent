package display

import "github.com/decodo/tyci/stream"

type ToolResult struct {
	Success bool
	Content string
	Error   string
}

type Display interface {
	Thinking(text string)
	Text(text string)
	ToolCallStart(name string)
	ToolCallDelta(delta string)
	ToolCallEnd(name string, result string)
	ToolBlock(msg string)
	Summary(usage stream.Usage, stats stream.Stats)
	Error(err error)
	End()
}
