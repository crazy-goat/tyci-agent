package main

import (
	"io"
	"os"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/providers"
)

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it. There is no existing helper for this in the
// repo's test suite (main_test.go only ever reads a subprocess's Stderr via
// exec.Cmd), so this is a small in-process redirect built from os.Pipe.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(data)
}

// =============================================================================
// resolveFallbacksQuiet — resolves without reporting
// =============================================================================

func TestResolveFallbacksQuiet_AllResolve(t *testing.T) {
	prov := &fakeProvider{name: "quiet-ok-prov", configured: true, models: []string{"m1", "m2"}}
	providers.Register(prov)

	var clients []connector.ModelClient
	stderr := captureStderr(t, func() {
		got, unresolved := resolveFallbacksQuiet([]string{"quiet-ok-prov/m1", "quiet-ok-prov/m2"})
		if len(got) != 2 {
			t.Fatalf("expected 2 resolved clients, got %d", len(got))
		}
		if len(unresolved) != 0 {
			t.Fatalf("expected no unresolved specs, got %v", unresolved)
		}
		clients = got
	})
	if stderr != "" {
		t.Errorf("resolveFallbacksQuiet wrote to stderr: %q", stderr)
	}
	if clients[0].Model() != "m1" || clients[1].Model() != "m2" {
		t.Errorf("unexpected resolved models: %s, %s", clients[0].Model(), clients[1].Model())
	}
}

func TestResolveFallbacksQuiet_UnresolvedSpecReportsNothing(t *testing.T) {
	var got []connector.ModelClient
	var unresolved []string
	stderr := captureStderr(t, func() {
		got, unresolved = resolveFallbacksQuiet([]string{"quiet-ghost-prov/does-not-exist"})
	})
	if len(got) != 0 {
		t.Errorf("expected no resolved clients, got %d", len(got))
	}
	if len(unresolved) != 1 || unresolved[0] != "quiet-ghost-prov/does-not-exist" {
		t.Fatalf("expected the spec in unresolved, got %v", unresolved)
	}
	if stderr != "" {
		t.Errorf("resolveFallbacksQuiet must never write to stderr itself, got %q", stderr)
	}
}

func TestResolveFallbacksQuiet_MixedValidAndInvalid(t *testing.T) {
	prov := &fakeProvider{name: "quiet-mixed-prov", configured: true, models: []string{"good"}}
	providers.Register(prov)

	// FindModel resolves a "provider/model" spec by provider name alone — it
	// does not check the model against p.Models() (see Catalog.FindModel) —
	// so the only way a spec fails to resolve is an unregistered provider,
	// not an unlisted model name.
	var clients int
	var unresolved []string
	stderr := captureStderr(t, func() {
		got, unres := resolveFallbacksQuiet([]string{"quiet-mixed-prov/good", "quiet-nonexistent-prov/bad"})
		clients = len(got)
		unresolved = unres
	})
	if clients != 1 {
		t.Errorf("expected 1 resolved client, got %d", clients)
	}
	if len(unresolved) != 1 || unresolved[0] != "quiet-nonexistent-prov/bad" {
		t.Fatalf("expected the bad spec in unresolved, got %v", unresolved)
	}
	if stderr != "" {
		t.Errorf("resolveFallbacksQuiet wrote to stderr: %q", stderr)
	}
}
