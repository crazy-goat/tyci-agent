package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSessionLists_SkipJournal puts one session file and one bus journal in a
// session directory. Both lists must return only the session.
func TestSessionLists_SkipJournal(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"real-session.jsonl", "bus-1-1700000000.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	list, err := ListEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "real-session.jsonl" {
		t.Fatalf("ListEntries = %+v, want only real-session.jsonl", list)
	}

	resume, err := resumeEntriesInDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(resume) != 1 || filepath.Base(resume[0].Path) != "real-session.jsonl" {
		t.Fatalf("resumeEntriesInDir = %+v, want only real-session.jsonl", resume)
	}
}

// TestJournalPID_ExactName checks that only names in the form
// bus-<pid>-<start>.jsonl are journals, and that the pid is read from them.
func TestJournalPID_ExactName(t *testing.T) {
	pid, ok := JournalPID("bus-42-1700000000.jsonl")
	if !ok || pid != 42 {
		t.Fatalf("JournalPID(bus-42-...) = %d, %v; want 42, true", pid, ok)
	}
	for _, name := range []string{"business.jsonl", "bus.jsonl", "bus-abc-1.jsonl", "bus-1000.jsonl", "bus-0-1.jsonl", "bus-1-1.jsonl.bak"} {
		if _, ok := JournalPID(name); ok {
			t.Errorf("JournalPID(%q) reports a journal", name)
		}
		if IsJournalFile(name) {
			t.Errorf("IsJournalFile(%q) = true", name)
		}
	}
}
