package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/session"
)

// TestEnsureLazySession_EmptyPathReturnsNil verifies the no-session path:
// when --no-session is in effect or no path was resolved, the helper must
// NOT create anything on disk. This guards against the "empty session jsonl
// left behind by an abandoned REPL" regression.
func TestEnsureLazySession_EmptyPathReturnsNil(t *testing.T) {
	got, path, err := ensureLazySession(nil, "", "/tmp", "m", "p")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil session for empty path, got %v", got)
	}
	if path != "" {
		t.Errorf("expected empty returned path, got %q", path)
	}
}

// TestEnsureLazySession_ExistingSessionReturned verifies that a
// pre-existing session is returned untouched (no double Open).
func TestEnsureLazySession_ExistingSessionReturned(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "existing.jsonl")
	existing, err := session.Open(p, dir, "m", "p")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	defer existing.Close()

	got, gotPath, err := ensureLazySession(existing, p, dir, "m", "p")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != existing {
		t.Errorf("helper returned a different *session.Session; expected the same pointer")
	}
	if gotPath != p {
		t.Errorf("path = %q, want %q", gotPath, p)
	}
}

// TestEnsureLazySession_CreatesFreshFileIs the core regression test for the
// "empty sessions in history" bug: providing a path without an existing
// session must create the file (lazy-open), so we can write the first prompt
// to it.
func TestEnsureLazySession_CreatesFreshFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "fresh.jsonl")

	got, gotPath, err := ensureLazySession(nil, p, dir, "model-x", "prov-y")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == nil {
		t.Fatal("expected a session, got nil")
	}
	t.Cleanup(func() { _ = got.Close() })
	if gotPath != p {
		t.Errorf("path = %q, want %q", gotPath, p)
	}
	if got.IsResume() {
		t.Errorf("expected fresh session (IsResume=false), got true")
	}

	// Verify header was written: defaults to a session row (no full Open
	// roundtrip — we trust session.Open covered that case in its own tests).
	if _, err := os.Stat(p); err != nil {
		t.Errorf("expected session file at %q, stat err: %v", p, err)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), `"type":"session"`) {
		t.Errorf("expected header line in %q, got %q", p, string(data))
	}
}

// TestEnsureLazySession_ResumesExistingFile covers the rare case where the
// caller gave us a path that already exists on disk but did not pre-open
// it (e.g. an auto-generated path that got reused across runs). The helper
// must resume rather than overwrite.
func TestEnsureLazySession_ResumesExistingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "preexisting.jsonl")

	seed, err := session.Open(p, dir, "m", "p")
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	_ = seed.WriteMessage("user", []session.ContentBlock{{Type: "text", Text: "one"}}, nil)
	_ = seed.Close()

	got, _, err := ensureLazySession(nil, p, dir, "m", "p")
	if err != nil {
		t.Fatalf("ensureLazySession: %v", err)
	}
	t.Cleanup(func() { _ = got.Close() })
	if !got.IsResume() {
		t.Errorf("expected resume (IsResume=true) for pre-existing file")
	}

	// The seeded user message must still be there.
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), `"one"`) {
		t.Errorf("seeded user message disappeared after lazy open:\n%s", string(data))
	}
}

// TestEnsureLazySession_OpenErrorYieldsNil covers the broken-file
// regression: if session.Open fails, ensureLazySession must NOT propagate
// the error (the REPL must keep running) and must clear sessionPath so the
// rest of the session switches to no-session mode.
func TestEnsureLazySession_OpenErrorYieldsNil(t *testing.T) {
	// Make session.Open fail by pointing at a path inside a directory
	// that exists but cannot contain a file (the path is a directory).
	dir := t.TempDir()
	got, gotPath, err := ensureLazySession(nil, dir, dir, "m", "p")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil session on open error, got %v", got)
	}
	if gotPath != "" {
		t.Errorf("expected empty path on open error, got %q", gotPath)
	}
}
