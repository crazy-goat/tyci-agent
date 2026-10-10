package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crazy-goat/tyci-agent/session"
)

// TestSessionBrowserAdapter_UsesHomeSessions writes one session file into the
// session dir of a private cwd and checks that the adapter lists it.
func TestSessionBrowserAdapter_UsesHomeSessions(t *testing.T) {
	// The session dir is under $HOME. A private home keeps parallel test runs
	// from touching the real sessions.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	cwd := t.TempDir()
	dir, err := session.SessionDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session","id":"a","projectRoot":"` + cwd + `"}` + "\n" +
		`{"type":"message","id":"x1","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}` + "\n" +
		`{"type":"title","id":"a","timestamp":"2026-10-10T08:00:00Z","title":"Plan\u0007 API"}` + "\n"
	path := filepath.Join(dir, "one.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := sessionBrowserAdapter{}.List(cwd, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].Path != path || items[0].FirstPrompt != "hello" || items[0].Title != "Plan API" {
		t.Errorf("item = %+v", items[0])
	}
}
