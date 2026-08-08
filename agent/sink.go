package agent

import "github.com/decodo/tyci/stream"

// Sink is the event interface the agent loop writes to. It is defined here,
// on the consumer side, rather than by the package that implements it — the
// agent must not depend on any concrete frontend/display package to be
// buildable. display.TUI, display.Terminal, display.Minimal (and the
// collector in cmd_interactive.go) satisfy this interface structurally,
// without importing agent.
type Sink interface {
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
