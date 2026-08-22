package ledger

import "github.com/decodo/tyci/stream"

// Sink is the event interface the agent loop writes to, restated here so this
// package never has to import agent (which would be a cycle: the conductor
// imports both). It is a structural copy of agent.Sink — a value satisfying
// this satisfies that.
type Sink interface {
	Request(content string)
	Thinking(text string)
	Text(text string)
	ToolCallStart(name string)
	ToolCallDelta(delta string)
	ToolCallEnd(name string, result string)
	ToolFinish()
	ToolBlock(msg string)
	Summary(usage stream.Usage, stats stream.Stats)
	Total(usage stream.Usage)
	Error(err error)
	End()
}

// Watch wraps a Sink so every model call records against the ledger as it
// completes, rather than the whole turn being recorded once at the end.
//
// The difference is what a person sees. A turn can be one model call or forty
// interleaved with tool runs, and recording only its total means the status
// bar shows no cost at all until everything finishes — for a long turn, no
// cost for minutes, then all of it at once. Summary fires once per model call
// with that call's usage (agent/run_once.go), which is exactly the granularity
// the bar wants: a figure that appears with the first response and grows.
//
// Every other method is forwarded untouched by the embedded interface.
func Watch(inner Sink, kind Kind, provider, model string) Sink {
	if inner == nil {
		return nil
	}
	return &watcher{Sink: inner, kind: kind, provider: provider, model: model}
}

type watcher struct {
	Sink
	kind     Kind
	provider string
	model    string
}

func (w *watcher) Summary(usage stream.Usage, stats stream.Stats) {
	Record(w.kind, w.provider, w.model, usage)
	w.Sink.Summary(usage, stats)
}
