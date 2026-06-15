package display

import "github.com/decodo/tyci/stream"

type ToolResult struct {
	Success bool
	Content string
	Error   string
}

type Display interface {
	// Request marks the start of a round: the input sent to the model
	// (user prompt for the first round, tool results for subsequent rounds).
	Request(content string)
	Thinking(text string)
	Text(text string)
	ToolCallStart(name string)
	ToolCallDelta(delta string)
	ToolCallEnd(name string, result string)
	// ToolFinish closes the current tool block with a single summary line
	// spanning from the first tool start to the last tool end.
	ToolFinish()
	ToolBlock(msg string)
	Summary(usage stream.Usage, stats stream.Stats)
	Error(err error)
	End()
}
