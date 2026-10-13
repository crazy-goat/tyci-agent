package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/session"
)

// deadPID returns the pid of a process that has exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// setupJournalDir sets HOME and the working directory to temp dirs and returns
// the session directory of the working directory.
func setupJournalDir(t *testing.T) string {
	t.Helper()
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
	return dir
}

// writeOld writes a file in dir and sets its modification time to past
// journalMaxAge ago.
func writeOld(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-(journalMaxAge + time.Hour))
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestBusJournalPath_OwnFileAndOldRemoved checks that each process gets its own
// journal file, and that a journal of a dead process older than journalMaxAge
// is removed when the path is made. Other files in the session directory stay.
func TestBusJournalPath_OwnFileAndOldRemoved(t *testing.T) {
	dir := setupJournalDir(t)

	old := writeOld(t, dir, "bus-"+strconv.Itoa(deadPID(t))+"-1000.jsonl")
	fresh := filepath.Join(dir, "bus-2-2000.jsonl")
	realSession := filepath.Join(dir, "real-session.jsonl")
	for _, p := range []string{fresh, realSession} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-(journalMaxAge + time.Hour))
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
		t.Fatalf("old journal of a dead process still exists (err %v)", err)
	}
	for _, p := range []string{fresh, realSession} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s was removed: %v", p, err)
		}
	}
}

// TestRemoveOldJournals_LiveProcessKept checks that a journal of a live
// process is kept even when it is older than journalMaxAge.
func TestRemoveOldJournals_LiveProcessKept(t *testing.T) {
	dir := setupJournalDir(t)

	live := writeOld(t, dir, "bus-"+strconv.Itoa(os.Getpid())+"-1000.jsonl")
	removeOldJournals(dir)

	if _, err := os.Stat(live); err != nil {
		t.Fatalf("journal of a live process was removed: %v", err)
	}
}

// TestRemoveOldJournals_OnlyJournalNames checks that a user file that only
// starts with "bus" is never removed, even when it is old.
func TestRemoveOldJournals_OnlyJournalNames(t *testing.T) {
	dir := setupJournalDir(t)

	names := []string{"business.jsonl", "bus.jsonl", "bus-abc-1000.jsonl", "bus-1000.jsonl"}
	for _, name := range names {
		writeOld(t, dir, name)
	}
	removeOldJournals(dir)

	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s was removed: %v", name, err)
		}
	}
}
