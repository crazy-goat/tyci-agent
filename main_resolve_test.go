package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
)

// fakeProvider is a minimal providers.Provider for exercising
// resolveProviderModel without any network access.
type fakeProvider struct {
	name       string
	configured bool
	models     []string
	free       []string
}

func (f *fakeProvider) Name() string         { return f.name }
func (f *fakeProvider) IsConfigured() bool   { return f.configured }
func (f *fakeProvider) Models() []string     { return f.models }
func (f *fakeProvider) FreeModels() []string { return f.free }
func (f *fakeProvider) Stream(context.Context, providers.Request) (<-chan stream.Event, error) {
	return nil, nil
}

func TestResolveProviderModel_ExplicitOverride(t *testing.T) {
	prov := &fakeProvider{name: "explicit-prov", configured: true, models: []string{"m1"}}
	providers.Register(prov)

	got, model, err := resolveProviderModel(context.Background(), "explicit-prov/m1")
	if err != nil {
		t.Fatalf("resolveProviderModel: %v", err)
	}
	if got != prov {
		t.Errorf("expected provider %q, got %v", prov.name, got)
	}
	if model != "m1" {
		t.Errorf("expected model %q, got %q", "m1", model)
	}
}

func TestResolveProviderModel_UnknownExplicitOverride(t *testing.T) {
	_, _, err := resolveProviderModel(context.Background(), "does-not-exist/m1")
	if err == nil {
		t.Fatal("expected error for unknown provider in explicit override")
	}
}

// TestResolveProviderModel_InheritsParentProvider is the regression test for the
// auth bug: with a bare model name, the subagent must reuse the parent's
// provider from context — NOT re-guess via FindModel, which iterates the
// provider map in random order and could land on a different provider that
// happens to list the same model (and lacks a valid key).
func TestResolveProviderModel_InheritsParentProvider(t *testing.T) {
	parent := &fakeProvider{name: "parent-prov", configured: true, models: []string{"shared-model"}}
	other := &fakeProvider{name: "other-prov", configured: true, models: []string{"shared-model"}}
	providers.Register(parent)
	providers.Register(other)

	ctx := providers.WithProvider(context.Background(), parent)
	ctx = providers.WithModel(ctx, "shared-model")

	got, model, err := resolveProviderModel(ctx, "shared-model")
	if err != nil {
		t.Fatalf("resolveProviderModel: %v", err)
	}
	if got != parent {
		t.Errorf("expected parent provider to be inherited, got %v", got)
	}
	if model != "shared-model" {
		t.Errorf("expected model %q, got %q", "shared-model", model)
	}
}

func TestResolveProviderModel_EmptyModelUsesContext(t *testing.T) {
	parent := &fakeProvider{name: "ctx-prov", configured: true, models: []string{"ctx-model"}}
	providers.Register(parent)

	ctx := providers.WithProvider(context.Background(), parent)
	ctx = providers.WithModel(ctx, "ctx-model")

	got, model, err := resolveProviderModel(ctx, "")
	if err != nil {
		t.Fatalf("resolveProviderModel: %v", err)
	}
	if got != parent {
		t.Errorf("expected context provider, got %v", got)
	}
	if model != "ctx-model" {
		t.Errorf("expected model from context %q, got %q", "ctx-model", model)
	}
}

func TestResolveProviderModel_NoContextFallsBackToLookup(t *testing.T) {
	prov := &fakeProvider{name: "lookup-prov", configured: true, models: []string{"lookup-model"}}
	providers.Register(prov)

	// No provider in context → must fall back to FindModel on the bare name.
	got, model, err := resolveProviderModel(context.Background(), "lookup-model")
	if err != nil {
		t.Fatalf("resolveProviderModel: %v", err)
	}
	if got != prov {
		t.Errorf("expected looked-up provider, got %v", got)
	}
	if model != "lookup-model" {
		t.Errorf("expected model %q, got %q", "lookup-model", model)
	}
}

func TestResolveProviderModel_NoContextNoMatch(t *testing.T) {
	_, _, err := resolveProviderModel(context.Background(), "totally-unknown-model")
	if err == nil {
		t.Fatal("expected error when no context provider and no registry match")
	}
}

// =============================================================================
// withIsolatedPool — the subagent's own connection pool
// =============================================================================

// A provider that does not implement providers.HTTPInjector (every fake in the
// suite, and any future third-party implementation) must pass through
// untouched, keeping today's "no isolation" behaviour instead of failing.
func TestWithIsolatedPool_PassesThroughNonInjector(t *testing.T) {
	prov := &fakeProvider{name: "not-an-injector"}
	if got := withIsolatedPool(prov); got != providers.Provider(prov) {
		t.Errorf("withIsolatedPool replaced a non-injector provider: %v", got)
	}
}

// recordingInjector is a Provider that also implements HTTPInjector and
// remembers every client it was handed.
type recordingInjector struct {
	fakeProvider
	got []connector.HTTPDoer
}

func (r *recordingInjector) WithHTTP(h connector.HTTPDoer) providers.Provider {
	r.got = append(r.got, h)
	return r
}

// Each call must mint a FRESH pool: two subagents running in parallel may not
// end up sharing one. This is the property that made the original code put the
// client inside runSingleTask rather than at process start.
func TestWithIsolatedPool_FreshClientPerCall(t *testing.T) {
	inj := &recordingInjector{fakeProvider: fakeProvider{name: "injector"}}

	withIsolatedPool(inj)
	withIsolatedPool(inj)

	if len(inj.got) != 2 {
		t.Fatalf("WithHTTP called %d times, want 2", len(inj.got))
	}
	if inj.got[0] == nil || inj.got[1] == nil {
		t.Fatal("withIsolatedPool injected a nil client")
	}
	if inj.got[0] == inj.got[1] {
		t.Error("two calls shared one client — parallel subagents would share a connection pool")
	}
}

// The transport settings are the contract: a small, private pool. These are the
// exact numbers the code carried in tools/subagent.go before the move.
func TestWithIsolatedPool_TransportSettings(t *testing.T) {
	inj := &recordingInjector{fakeProvider: fakeProvider{name: "injector"}}
	withIsolatedPool(inj)

	cl, ok := inj.got[0].(*http.Client)
	if !ok {
		t.Fatalf("injected doer is %T, want *http.Client", inj.got[0])
	}
	tr, ok := cl.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", cl.Transport)
	}
	if tr.MaxIdleConns != 2 {
		t.Errorf("MaxIdleConns = %d, want 2", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != 1 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 1", tr.MaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout != 30*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 30s", tr.IdleConnTimeout)
	}
	// The pool must be private, never the api layer's shared transport.
	if tr == http.DefaultTransport {
		t.Error("isolated client reuses http.DefaultTransport")
	}
}

// A real provider from providers.NewProvider is an HTTPInjector, so the
// production path really does get isolation — and the original value is not
// mutated, because parallel children share it.
func TestWithIsolatedPool_RealProviderGetsCopy(t *testing.T) {
	base := providers.NewProvider("real", []providers.ModelEntry{
		{Name: "m", URI: "openai://m@sk@api.example.invalid"},
	}, providers.Deps{})

	a := withIsolatedPool(base)
	b := withIsolatedPool(base)

	if a == base || b == base {
		t.Error("withIsolatedPool returned the shared provider instead of a copy")
	}
	if a == b {
		t.Error("two children got the same provider value")
	}
	if a.Name() != "real" || b.Name() != "real" {
		t.Error("the copies lost the provider identity")
	}
}
