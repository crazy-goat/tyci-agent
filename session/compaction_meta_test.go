package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
)

// compactionLines returns the stored compaction events of path, decoded.
func compactionLines(t *testing.T, path string) []CompactionEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []CompactionEvent
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.Contains(line, `"type":"compaction"`) {
			continue
		}
		var ev CompactionEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func TestWriteCompactionStoresMeta(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/meta.jsonl"
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	meta := CompactMeta{Kind: CompactKindAuto, Summarized: true, TokensBefore: 412000, TokensAfter: 9000}
	if err := s.WriteCompaction("summary", "", nil, 0, meta); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	evs := compactionLines(t, path)
	if len(evs) != 1 {
		t.Fatalf("compaction events = %d, want 1", len(evs))
	}
	got := evs[0].CompactMeta
	if got.Kind != CompactKindAuto || !got.Summarized || got.TokensBefore != 412000 || got.TokensAfter != 9000 {
		t.Fatalf("stored meta = %+v", got)
	}
}

func TestWriteCompactionOmitsEmptyMeta(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/plain.jsonl"
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compact("summary", "", []connector.Message{}, 0, CompactMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, key := range []string{`"kind"`, `"summarized"`, `"tokens_before"`, `"tokens_after"`} {
		if strings.Contains(string(data), key) {
			t.Fatalf("file has %s for empty meta", key)
		}
	}
}

func TestCompactionEventWithoutKindLoads(t *testing.T) {
	const line = `{"type":"compaction","id":"c1","timestamp":"2026-01-01T00:00:00Z","summary":{"role":"user","content":[{"type":"text","text":"old"}]}}`
	var ev CompactionEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Kind != "" || ev.Summarized || ev.TokensBefore != 0 {
		t.Fatalf("old event meta = %+v, want zero", ev.CompactMeta)
	}
}
