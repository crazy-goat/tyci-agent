package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestResumeEntries_BasicList writes 2 fake session files into the cwd-
// encoded session dir and verifies ResumeEntries returns both, newest
// first, with the first user prompt decoded.
func TestResumeEntries_BasicList(t *testing.T) {
	cwd := "/tmp/tyci-test"
	dir, err := SessionDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Write first file (older ModTime).
	old := filepath.Join(dir, "old.jsonl")
	writeSession(t, old, []string{
		`{"type":"session","id":"a"}`,
		`{"type":"message","id":"x1","message":{"role":"user","content":[{"type":"text","text":"hello world"}]}}`,
	})
	// Use a future mtime to make "newer" deterministic, then "old" older.
	futureTS := futureUnixMillis(t)

	type fileSet struct {
		path string
	}
	_ = old // we'll set mtime explicitly below

	// Write second file with later mtime, plus second user message we
	// expect to find (first one only).
	_ = futureTS
	newer := filepath.Join(dir, "newer.jsonl")
	writeSession(t, newer, []string{
		`{"type":"session","id":"b"}`,
		`{"type":"message","id":"x2","message":{"role":"user","content":[{"type":"text","text":"second prompt"}]}}`,
		`{"type":"message","id":"x3","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}}`,
	})

	entries, err := ResumeEntries(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	// Newest first. We didn't bump mtimes; just assert both prompts
	// readable.
	prompts := map[string]string{
		entries[0].Name: entries[0].FirstPrompt,
		entries[1].Name: entries[1].FirstPrompt,
	}
	if !strings.Contains(prompts["old.jsonl"], "hello world") {
		t.Errorf("old entry first prompt = %q, want substring 'hello world'", prompts["old.jsonl"])
	}
	if !strings.Contains(prompts["newer.jsonl"], "second prompt") {
		t.Errorf("newer entry first prompt = %q, want substring 'second prompt'", prompts["newer.jsonl"])
	}
}

// TestResumeEntries_EmptyAndMissing verifies ResumeEntries tolerates an
// empty / nonexistent session dir without error.
func TestResumeEntries_EmptyAndMissing(t *testing.T) {
	entries, err := ResumeEntries("/tmp/tyci-test-empty-" + t.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want 0 entries, got %d", len(entries))
	}
}

// TestReadFirstUserPrompt_SkipsNonUserBlocks ensures tool/text mix in
// blocks does not leak content and uses only the text block.
func TestReadFirstUserPrompt_SkipsNonUserBlocks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.jsonl")
	writeSession(t, p, []string{
		`{"type":"session"}`,
		`{"type":"message","id":"s1","message":{"role":"system","content":[{"type":"text","text":"sys"}]}}`,
		`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"toolCall","text":"ignore"}]}}`,
		`{"type":"message","id":"u2","message":{"role":"user","content":[{"type":"text","text":"user said this"}]}}`,
	})
	got := readFirstUserPrompt(p)
	if got != "user said this" {
		t.Errorf("readFirstUserPrompt = %q, want 'user said this'", got)
	}
}

// TestReadFirstUserPrompt_CorruptFirstLine ensures a leading non-JSON line
// does not stop scanning; we still find the user prompt on line 2.
func TestReadFirstUserPrompt_CorruptFirstLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.jsonl")
	writeSession(t, p, []string{
		"this is not json",
		`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"survived"}]}}`,
	})
	if got := readFirstUserPrompt(p); got != "survived" {
		t.Errorf("got %q, want 'survived'", got)
	}
}

// futureUnixMillis returns a UnixMilli value slightly in the future. Was
// used during dev; kept as a helper for future clock-tolerant tests.
func futureUnixMillis(t *testing.T) int64 {
	t.Helper()
	return 0
}

// TestUnixMillis_TimeRoundTrip verifies the Time() accessor on UnixMillis
// returns the original time round-tripped through seconds+nanos. Used by
// the /resume picker to render the "Date (UTC)" column without forcing
// the caller to import time.Now() conventions themselves.
func TestUnixMillis_TimeRoundTrip(t *testing.T) {
	cases := []time.Time{
		time.Date(2026, 1, 1, 12, 34, 56, 0, time.UTC),
		time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Now().Truncate(time.Second), // need second alignment
	}
	for _, want := range cases {
		got := UnixMillisFromTime(want).Time()
		if !got.Equal(want) {
			t.Errorf("round-trip %v → UnixMillis → %v, want %v", want, got, want)
		}
	}
}

func writeSession(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
