package display

import (
	"strings"
	"sync"

	"github.com/decodo/tyci/stream"
)

// BtwSink is the agent.Sink for one /btw side-conversation. It streams the
// child run's output live to the TUI (via the same tea.Program the main
// conversation renders through) while collecting the full text so the
// background job can return it as its result — the same shape as
// tools/subagent.go's streamingCollector, minus the tool-queue plumbing that
// only makes sense for a subagent nested inside the main thread's own
// tool-call stream.
//
// A BtwSink is created once per /btw invocation and is safe for concurrent
// use: agent.Run may call it from its own goroutine while CollectedText is
// read from another.
type BtwSink struct {
	t  *TUI
	id string

	mu   sync.Mutex
	text strings.Builder
}

// NewBtwSink builds a BtwSink that streams to the /btw entry identified by
// id. id must have already been registered via TUI.OpenBtw.
func NewBtwSink(t *TUI, id string) *BtwSink {
	return &BtwSink{t: t, id: id}
}

func (s *BtwSink) post(kind, content string) {
	s.t.prog.Send(tuiBtwStreamMsg{id: s.id, kind: kind, content: content})
}

func (s *BtwSink) Request(string) {}

func (s *BtwSink) Thinking(text string) { s.post("thinking", text) }

func (s *BtwSink) Text(text string) {
	s.mu.Lock()
	s.text.WriteString(text)
	s.mu.Unlock()
	s.post("text", text)
}

func (s *BtwSink) ToolCallStart(name string)          { s.post("tool", "▶ "+name+"\n") }
func (s *BtwSink) ToolCallDelta(delta string)         {}
func (s *BtwSink) ToolCallEnd(name, result string)    {}
func (s *BtwSink) ToolFinish()                        {}
func (s *BtwSink) ToolBlock(msg string)               { s.post("block", msg+"\n") }
func (s *BtwSink) Summary(stream.Usage, stream.Stats) {}
func (s *BtwSink) Total(stream.Usage)                 {}
func (s *BtwSink) Error(err error)                    { s.post("error", err.Error()+"\n") }
func (s *BtwSink) End()                               {}

// StreamProgress forwards a tool's incremental output (e.g. bash lines) into
// the same stream as Text/Thinking. agent/run_tools.go's installToolStreaming
// asserts for this method and wires it up automatically whenever the Sink
// implements it — the same mechanism the subagent modal uses.
func (s *BtwSink) StreamProgress(_ int, line string) { s.post("text", line+"\n") }

// CollectedText returns the full text accumulated via Text so far.
func (s *BtwSink) CollectedText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text.String()
}

// MarkDone signals that the child run has finished (agent.Run returned),
// successfully or not. There is no such call in the agent.Sink interface
// itself — End()/Error() are called mid-run (Error on a retried failure,
// not necessarily a fatal one) — so the caller (btw.go's startBtw) calls
// this explicitly once agent.Run returns.
func (s *BtwSink) MarkDone(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.post("done", msg)
}
