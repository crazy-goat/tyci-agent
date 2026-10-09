package agent

import (
	"context"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/stream"
)

// dividerSink records the compaction metadata sent by the agent.
type dividerSink struct {
	silentDisplay
	metas []session.CompactMeta
}

func (d *dividerSink) Compaction(meta session.CompactMeta) { d.metas = append(d.metas, meta) }

// runInLoopHardLimit runs a subagent-style turn (no own session) that passes
// the hard limit through tool rounds, and returns the sink.
func runInLoopHardLimit(t *testing.T, reply func(context.Context) (<-chan stream.Event, error)) *dividerSink {
	t.Helper()
	runner := newMockToolRunner()
	runner.SetResult("read", "file content")
	fake := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{
			toolRound("t1", 1000), toolRound("t2", 1000), toolRound("t3", 1000),
			toolRound("t4", 1000), toolRound("t5", 180000),
		},
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
	mc := &summaryStub{Fake: fake, stub: reply}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "the task"}}},
	}
	d := &dividerSink{}
	if _, err := Run(context.Background(), mc, d, &msgs, Config{
		MaxRetries:       1,
		ContextLimit:     1000000,
		SoftLimit:        150000,
		HardLimit:        170000,
		Tools:            runner,
		InLoopCompaction: true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return d
}

func TestCompactDivider_InLoopEmitsWithoutSummary(t *testing.T) {
	d := runInLoopHardLimit(t, summaryReply(""))
	if len(d.metas) != 1 {
		t.Fatalf("compaction events = %d, want 1", len(d.metas))
	}
	m := d.metas[0]
	if m.Kind != session.CompactKindInLoop || m.Summarized || m.TokensBefore != 180000 {
		t.Fatalf("meta = %+v, want in-loop, not summarized, 180000 tokens before", m)
	}
}

func TestCompactDivider_InLoopSummarizedOnlyWithSummary(t *testing.T) {
	d := runInLoopHardLimit(t, summaryReply("SUMMARY TEXT"))
	if len(d.metas) != 1 {
		t.Fatalf("compaction events = %d, want 1", len(d.metas))
	}
	if m := d.metas[0]; m.Kind != session.CompactKindInLoop || !m.Summarized {
		t.Fatalf("meta = %+v, want in-loop and summarized", m)
	}
}

// autoCompactMeta runs one hard-limit turn over n history messages and
// returns the metadata that the agent passes to the compactor.
func autoCompactMeta(t *testing.T, mc connector.ModelClient, n int) session.CompactMeta {
	t.Helper()
	sess := newAutoCompactSession(t)
	msgs := historyOf(n)
	var got []session.CompactMeta
	if _, err := Run(context.Background(), mc, &silentDisplay{}, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
		HardLimit:    170000,
		Session:      sess,
		Compactor: func(summary, focus string, meta session.CompactMeta) (string, error) {
			got = append(got, meta)
			return CompactSession(sess, &msgs, summary, focus, meta)
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("compactor calls = %d, want 1", len(got))
	}
	return got[0]
}

func TestCompactDivider_AutoLabelFollowsSummary(t *testing.T) {
	tests := []struct {
		name       string
		mc         connector.ModelClient
		n          int
		summarized bool
	}{
		{"summary written", hardLimitTurn(summaryReply("SUMMARY TEXT")), 9, true},
		{"summary call returns no text", hardLimitTurn(summaryReply("")), 9, false},
		{"summary call does not run", hardLimitTurn(nil), 7, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := autoCompactMeta(t, tt.mc, tt.n)
			if m.Kind != session.CompactKindAuto || m.Summarized != tt.summarized || m.TokensBefore != 181000 {
				t.Fatalf("meta = %+v, want auto, summarized=%v, 181000 tokens before", m, tt.summarized)
			}
		})
	}
}
