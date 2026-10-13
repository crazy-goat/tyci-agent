package flow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAskShowsRunDirTable checks that the ask message carries the ask-table.md of the run dir,
// and that a run without that file gets the plain message.
func TestAskShowsRunDirTable(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "ask", States: map[string]State{
		"ask": {Ask: "Answer yes or no.", On: map[string]string{"yes": "end"}},
		"end": {End: true},
	}}
	run := func(dir string) *RunState {
		r := &Runner{WF: wf, Store: &memStore{}, RunDir: dir}
		st := newRun("")
		if err := r.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
			t.Fatalf("Run err = %v, want ErrPaused", err)
		}
		return st
	}

	dir := t.TempDir()
	table := "| Issue | Title | Milestone |\n| #1 | first | v0.10.0 |\n"
	if err := os.WriteFile(filepath.Join(dir, askTable), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	st := run(dir)
	if !strings.HasSuffix(st.Ask.Message, "| #1 | first | v0.10.0 |") {
		t.Fatalf("message = %q, want the table at the end", st.Ask.Message)
	}

	plain := run(t.TempDir())
	if plain.Ask.Message != "Answer yes or no." {
		t.Fatalf("message = %q, want the plain question", plain.Ask.Message)
	}
}
