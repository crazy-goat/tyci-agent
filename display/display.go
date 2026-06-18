package display

import "github.com/decodo/tyci/stream"

type ToolResult struct {
	Success bool
	Content string
	Error   string
}

type Display interface {
	// Request marks the start of a round (user prompt for the first
	// round, tool results for subsequent rounds). Run-mode Minimal uses
	// this to emit the [ REQ] line; other displays may treat it as a
	// no-op.
	Request(content string)
	Thinking(text string)
	Text(text string)
	ToolCallStart(name string)
	ToolCallDelta(delta string)
	ToolCallEnd(name string, result string)
	// ToolFinish closes the current tool block with a single summary
	// line. Run-mode Minimal emits "[TOOL} Tool finish" here; other
	// displays may treat it as a no-op.
	ToolFinish()
	ToolBlock(msg string)
	Summary(usage stream.Usage, stats stream.Stats)
	Total(usage stream.Usage)
	Error(err error)
	End()
}
