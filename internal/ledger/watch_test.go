package ledger

import (
	"testing"

	"github.com/decodo/tyci/stream"
)

// silentSink implements Sink doing nothing, for embedding into the two
// Phase-shaped test doubles below.
type silentSink struct{}

func (silentSink) Request(string)                     {}
func (silentSink) Thinking(string)                    {}
func (silentSink) Text(string)                        {}
func (silentSink) ToolCallStart(string)               {}
func (silentSink) ToolCallDelta(string)               {}
func (silentSink) ToolCallEnd(string, string)         {}
func (silentSink) ToolFinish()                        {}
func (silentSink) ToolBlock(string)                   {}
func (silentSink) Summary(stream.Usage, stream.Stats) {}
func (silentSink) Total(stream.Usage)                 {}
func (silentSink) Error(error)                        {}
func (silentSink) End()                               {}

// phaselessSink is a bare Sink with no Phase method — the shape every
// production Sink except display.TUI has. Watch's wrapper embeds Sink as an
// interface value, so it must type-assert to see through to Phase rather
// than relying on embedding to promote it.
type phaselessSink struct{ silentSink }

// phasedSink additionally implements Phase, recording calls made to it.
type phasedSink struct {
	silentSink
	phases []string
}

func (s *phasedSink) Phase(name string) { s.phases = append(s.phases, name) }

func TestWatch_PhaseForwardsWhenInnerImplementsIt(t *testing.T) {
	inner := &phasedSink{}
	w := Watch(inner, Main, "anthropic", "claude", "")

	w.(interface{ Phase(string) }).Phase("waiting")

	if len(inner.phases) != 1 || inner.phases[0] != "waiting" {
		t.Fatalf("inner.phases = %v, want [\"waiting\"]", inner.phases)
	}
}

func TestWatch_PhaseIsANoOpWhenInnerLacksIt(t *testing.T) {
	inner := &phaselessSink{}
	w := Watch(inner, Main, "anthropic", "claude", "")

	// Must not panic: phaselessSink does not implement Phase, and *watcher
	// must degrade to a no-op rather than assuming every Sink has one.
	w.(interface{ Phase(string) }).Phase("waiting")
}
