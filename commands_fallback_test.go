package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/providers"
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

func TestInitCommon_ExplicitModelDoesNotInheritDefaultAgentFallback(t *testing.T) {
	writeWiringTestHome(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".tyci", "agents.json"), []byte(`{"default":{"fallback":["explicit-fallback-prov/fallback-model"]}}`), 0600); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}

	providers.Register(&fakeProvider{name: "explicit-primary-prov", configured: true, models: []string{"primary-model"}})
	providers.Register(&fakeProvider{name: "explicit-fallback-prov", configured: true, models: []string{"fallback-model"}})

	cmd := newInitCommonTestCmd(t)
	if err := cmd.Flags().Set("model", "explicit-primary-prov/primary-model"); err != nil {
		t.Fatalf("set model: %v", err)
	}
	if err := cmd.Flags().Set("no-mcp", "true"); err != nil {
		t.Fatalf("set no-mcp: %v", err)
	}

	_, _, cfg, _, _, _, _, dl, shutdown, err := initCommon(cmd, false, false)
	if err != nil {
		t.Fatalf("initCommon: %v", err)
	}
	defer shutdown()
	if dl != nil {
		defer dl.Close()
	}
	if len(cfg.Fallbacks) != 0 {
		t.Fatalf("explicit --model inherited %d fallback(s): %v", len(cfg.Fallbacks), cfg.Fallbacks)
	}
}

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

// =============================================================================
// resolveFallbacks — today's behavior, preserved: reports on stderr
// =============================================================================

func TestResolveFallbacks_AllResolveNoWarning(t *testing.T) {
	prov := &fakeProvider{name: "loud-ok-prov", configured: true, models: []string{"m1"}}
	providers.Register(prov)

	var got []connector.ModelClient
	stderr := captureStderr(t, func() {
		clients := resolveFallbacks([]string{"loud-ok-prov/m1"})
		if len(clients) != 1 {
			t.Fatalf("expected 1 resolved client, got %d", len(clients))
		}
		got = clients
	})
	if stderr != "" {
		t.Errorf("expected no warning for a fully-resolved list, got %q", stderr)
	}
	if len(got) != 1 || got[0].Provider() != "loud-ok-prov" {
		t.Errorf("unexpected resolved provider: %v", got)
	}
}

// This pins the exact behavior resolveFallbacks must keep after being
// rewritten to call resolveFallbacksQuiet internally: an unresolved spec
// still produces the same stderr warning it always did.
func TestResolveFallbacks_UnresolvedSpecWarnsOnStderr(t *testing.T) {
	var clients []connector.ModelClient
	stderr := captureStderr(t, func() {
		clients = resolveFallbacks([]string{"loud-ghost-prov/nope"})
	})
	if len(clients) != 0 {
		t.Errorf("expected no resolved clients, got %d", len(clients))
	}
	if !strings.Contains(stderr, `Warning: fallback model "loud-ghost-prov/nope" not found, skipping`) {
		t.Errorf("stderr = %q, want the unresolved-fallback warning", stderr)
	}
}
