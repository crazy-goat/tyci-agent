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
//
// jobID attributes the recorded usage to a specific job — pass "" for the
// main conversation (which is not a job); a subagent call passes its own
// job id so its tokens are trackable per-child instead of collapsing into a
// shared "subagent" bucket for the model (see Row's doc comment).
func Watch(inner Sink, kind Kind, provider, model, jobID string) Sink {
	if inner == nil {
		return nil
	}
	return &watcher{Sink: inner, kind: kind, provider: provider, model: model, jobID: jobID}
}

type watcher struct {
	Sink
	kind     Kind
	provider string
	model    string
	jobID    string
}

func (w *watcher) Summary(usage stream.Usage, stats stream.Stats) {
	Record(w.kind, w.provider, w.model, w.jobID, usage)
	w.Sink.Summary(usage, stats)
}

// Phase forwards to the wrapped Sink's Phase, if it has one. The embedded
// Sink field is an interface value, so Go does not promote a method the
// underlying concrete type (display.TUI) has but the Sink interface itself
// doesn't declare — agent.PhaseSink is optional precisely so most Sinks can
// skip it, and *watcher must type-assert the same way agent/run_once.go
// does rather than relying on embedding to see through it.
func (w *watcher) Phase(name string) {
	if ps, ok := w.Sink.(interface{ Phase(string) }); ok {
		ps.Phase(name)
	}
}
