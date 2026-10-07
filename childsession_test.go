package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
)

func TestChildSessionWrittenInAgentsDirAndReopened(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "do it"}}}}
	s := openChildSession(msgs, "m", "p")
	if s == nil {
		t.Fatal("no session")
	}
	if filepath.Base(filepath.Dir(s.Path())) != "agents" {
		t.Fatalf("path %s not in agents dir", s.Path())
	}
	_ = s.Close()
	s2 := reopenChildSession(s, "m", "p")
	if s2 == nil || s2.Path() != s.Path() {
		t.Fatal("reopen failed")
	}
	writeChildMessages(s2, msgs)
	_ = s2.Close()
	b, _ := os.ReadFile(s.Path())
	if n := strings.Count(string(b), `"do it"`); n != 2 {
		t.Fatalf("want 2 user messages, got %d", n)
	}
}
