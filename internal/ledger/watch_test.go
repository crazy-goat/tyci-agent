package ledger

import (
	"testing"

	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/stream"
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

// compactionRecordingSink is a Sink that also implements Compaction, recording
// the metadata it receives.
type compactionRecordingSink struct {
	silentSink
	metas []session.CompactMeta
}

func (s *compactionRecordingSink) Compaction(meta session.CompactMeta) {
	s.metas = append(s.metas, meta)
}

// TestWatch_CompactionForwardsWhenInnerImplementsIt checks that the in-loop
// divider reaches the sink of a subagent through the ledger wrapper.
func TestWatch_CompactionForwardsWhenInnerImplementsIt(t *testing.T) {
	inner := &compactionRecordingSink{}
	w := Watch(inner, Subagent, "p", "m", "job-1")
	cs, ok := w.(interface{ Compaction(session.CompactMeta) })
	if !ok {
		t.Fatal("watch wrapper does not expose Compaction")
	}
	cs.Compaction(session.CompactMeta{Kind: session.CompactKindInLoop, Summarized: true, TokensBefore: 170000})
	if len(inner.metas) != 1 {
		t.Fatalf("inner got %d compaction events, want 1", len(inner.metas))
	}
	if m := inner.metas[0]; m.Kind != session.CompactKindInLoop || !m.Summarized || m.TokensBefore != 170000 {
		t.Fatalf("forwarded meta = %+v", m)
	}
}

// TestWatch_CompactionIsANoOpWhenInnerLacksIt checks that a sink without
// Compaction does not panic and gets no call.
func TestWatch_CompactionIsANoOpWhenInnerLacksIt(t *testing.T) {
	w := Watch(phaselessSink{}, Subagent, "p", "m", "job-2")
	cs := w.(interface{ Compaction(session.CompactMeta) })
	cs.Compaction(session.CompactMeta{Kind: session.CompactKindInLoop})
}
