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
	for _, name := range []string{"real-session.jsonl", JournalFileName} {
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
