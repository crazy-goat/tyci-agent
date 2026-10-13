package display

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/session"
)

// writeAgentLog writes a session file at path with one assistant turn that
// calls bash and one tool result that holds a secret body.
func writeAgentLog(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := session.Open(path, t.TempDir(), "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"command":"ls -la"}`)
	if err := s.WriteMessage("user", []session.ContentBlock{{Type: "text", Text: "do the task"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("assistant", []session.ContentBlock{
		{Type: "thinking", Thinking: "I should list files"},
		{Type: "toolCall", ID: "c1", Name: "bash", Arguments: args},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("toolResult", []session.ContentBlock{
		{Type: "toolResult", ToolCallID: "c1", Text: "SECRET-BODY"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentLogProvider_ReadsRedactedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs", "#1", "agents", "001-coder.jsonl")
	writeAgentLog(t, path)

	got := strings.Join(loadAgentLog(path), "\n")
	for _, want := range []string{"operator: do the task", "thinking: I should list files", "tool bash(ls -la)  ok"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "SECRET-BODY") {
		t.Errorf("tool result body shown:\n%s", got)
	}
}

func TestAgentLogProvider_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents", "001-coder.jsonl")
	got := loadAgentLog(path)
	if len(got) != 1 || got[0] != "(no log yet)" {
		t.Errorf("got %q, want (no log yet)", got)
	}
}

func TestAgentLogProvider_NoPath(t *testing.T) {
	got := loadAgentLog("")
	if len(got) != 1 || got[0] != "(no log yet)" {
		t.Errorf("got %q, want (no log yet)", got)
	}
}

func TestAgentLogProvider_Unreadable(t *testing.T) {
	// A path below a regular file fails with ENOTDIR, not "not exist". This
	// does not depend on the user, unlike a file with mode 0 that root reads.
	base := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(base, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadAgentLog(filepath.Join(base, "001-coder.jsonl"))
	if len(got) != 1 || !strings.HasPrefix(got[0], "(log unavailable: ") || !strings.HasSuffix(got[0], ")") {
		t.Errorf("got %q, want (log unavailable: <err>)", got)
	}
}

func TestAgentLogProvider_SkipsPartialLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents", "001-coder.jsonl")
	writeAgentLog(t, path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"message","message":{"role":"assi`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got := strings.Join(loadAgentLog(path), "\n")
	if !strings.Contains(got, "tool bash(ls -la)  ok") || strings.Contains(got, "unavailable") {
		t.Errorf("partial last line must be skipped, got:\n%s", got)
	}
}
