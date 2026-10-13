package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/session"
)

// TestBusJournalPath_OwnFileAndOldRemoved checks that each process gets its own
// journal file, and that journals older than journalMaxAge are removed when
// the path is made. Other files in the session directory stay.
func TestBusJournalPath_OwnFileAndOldRemoved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wd := t.TempDir()
	t.Chdir(wd)
	dir, err := session.SessionDir(wd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	old := filepath.Join(dir, "bus-1-1000.jsonl")
	fresh := filepath.Join(dir, "bus-2-2000.jsonl")
	realSession := filepath.Join(dir, "real-session.jsonl")
	for _, p := range []string{old, fresh, realSession} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-(journalMaxAge + time.Hour))
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(realSession, past, past); err != nil {
		t.Fatal(err)
	}

	got := busJournalPath()
	if filepath.Dir(got) != dir || !session.IsJournalFile(filepath.Base(got)) {
		t.Fatalf("busJournalPath() = %q, want a journal file in %q", got, dir)
	}
	if got == old || got == fresh {
		t.Fatalf("busJournalPath() = %q reuses another journal", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old journal still exists (err %v)", err)
	}
	for _, p := range []string{fresh, realSession} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s was removed: %v", p, err)
		}
	}
}
