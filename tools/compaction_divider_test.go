package tools

import (
	"context"
	"testing"

	"github.com/crazy-goat/tyci-agent/session"
)

func TestCompactToolPassesToolKind(t *testing.T) {
	var got session.CompactMeta
	ctx := WithCompactor(context.Background(), func(summary, focus string, meta session.CompactMeta) (string, error) {
		got = meta
		return "/tmp/dump.md", nil
	})
	if res := (&CompactTool{}).Run(ctx, map[string]any{"summary": "keep"}); !res.Success {
		t.Fatalf("unexpected result: %+v", res)
	}
	if got.Kind != session.CompactKindTool || got.Summarized || got.At.IsZero() {
		t.Fatalf("meta = %+v, want tool kind with a time", got)
	}
}

func TestStreamingCollectorRecordsCompaction(t *testing.T) {
	lt := &liveTranscript{}
	s := &streamingCollector{collector: newCollector(), live: lt}
	s.Compaction(session.CompactMeta{Kind: session.CompactKindInLoop, Summarized: true, TokensBefore: 170000})
	if len(lt.events) != 1 {
		t.Fatalf("live events = %d, want 1", len(lt.events))
	}
	ev := lt.events[0]
	if ev.Kind != "compaction" || ev.Compaction.Kind != session.CompactKindInLoop || !ev.Compaction.Summarized || ev.Compaction.TokensBefore != 170000 {
		t.Fatalf("event = %+v", ev)
	}
}
