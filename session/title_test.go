package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTitle_WriteAppendsEventAndReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	s, err := Open(path, "/tmp", "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if err := s.WriteTitle("first"); err != nil {
		t.Fatalf("WriteTitle() error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type":"title"`) {
		t.Errorf("expected title event, got %q", data)
	}
	if got := ReadTitle(path); got != "first" {
		t.Errorf("ReadTitle() = %q, want %q", got, "first")
	}
}

func TestTitle_LastEventWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	s, err := Open(path, "/tmp", "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if err := s.WriteTitle("first"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteTitle("second"); err != nil {
		t.Fatal(err)
	}
	if got := ReadTitle(path); got != "second" {
		t.Errorf("ReadTitle() = %q, want %q", got, "second")
	}
}

func TestTitle_OldFileHasNoTitle(t *testing.T) {
	isolatedHome(t)
	cwd := t.TempDir()
	dir, err := SessionDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "old.jsonl")
	writeSession(t, path, []string{
		`{"type":"session","id":"a","projectRoot":"` + cwd + `"}`,
		`{"type":"message","id":"x1","message":{"role":"user","content":[{"type":"text","text":"hello world"}]}}`,
	})

	if got := ReadTitle(path); got != "" {
		t.Errorf("ReadTitle() = %q, want empty", got)
	}
	entries, err := ResumeEntries(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].FirstPrompt != "hello world" || entries[0].Title != "" {
		t.Errorf("entry = %+v, want FirstPrompt %q and empty Title", entries[0], "hello world")
	}
}

func TestTitle_ResumeEntryShowsTitle(t *testing.T) {
	isolatedHome(t)
	cwd := t.TempDir()
	dir, err := SessionDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "named.jsonl")
	writeSession(t, path, []string{
		`{"type":"session","id":"a","projectRoot":"` + cwd + `"}`,
		`{"type":"message","id":"x1","message":{"role":"user","content":[{"type":"text","text":"hello world"}]}}`,
		`{"type":"title","id":"a","timestamp":"2026-10-10T00:00:00Z","title":"My name"}`,
	})

	entries, err := ResumeEntries(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Title != "My name" || entries[0].FirstPrompt != "hello world" {
		t.Errorf("entries = %+v, want Title %q and FirstPrompt %q", entries, "My name", "hello world")
	}
}

func TestTitle_LoadForReplayIgnoresTitleLine(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.jsonl")
	withTitle := filepath.Join(dir, "titled.jsonl")
	header := `{"type":"session","id":"a","version":1}`
	msg := `{"type":"message","id":"x1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`
	writeSession(t, plain, []string{header, msg})
	writeSession(t, withTitle, []string{header, `{"type":"title","id":"a","title":"t"}`, msg})

	_, msgsA, _, _, err := LoadForReplay(plain)
	if err != nil {
		t.Fatal(err)
	}
	_, msgsB, _, _, err := LoadForReplay(withTitle)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgsA) != len(msgsB) {
		t.Errorf("message count = %d, want %d", len(msgsB), len(msgsA))
	}
}

func TestTitle_MarkdownDumpStillWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	s, err := Open(path, "/tmp", "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.WriteTitle("named"); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteMarkdownDump(path); err != nil {
		t.Errorf("WriteMarkdownDump() error: %v", err)
	}
}

func TestTitle_CorruptLineIsSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeSession(t, path, []string{
		`{"type":"session","id":"a"}`,
		`{not json`,
		`{"type":"title","id":"a","title":"valid"}`,
	})
	if got := ReadTitle(path); got != "valid" {
		t.Errorf("ReadTitle() = %q, want %q", got, "valid")
	}
}
