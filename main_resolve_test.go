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
// resolveModelClient without any network access.
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

func TestResolveModelClient_ExplicitOverride(t *testing.T) {
	prov := &fakeProvider{name: "explicit-prov", configured: true, models: []string{"m1"}}
	providers.Register(prov)

	got, err := resolveModelClient(context.Background(), "explicit-prov/m1")
	if err != nil {
		t.Fatalf("resolveModelClient: %v", err)
	}
	if got.Provider() != "explicit-prov" {
		t.Errorf("expected provider %q, got %q", "explicit-prov", got.Provider())
	}
	if got.Model() != "m1" {
		t.Errorf("expected model %q, got %q", "m1", got.Model())
	}
}

func TestResolveModelClient_UnknownExplicitOverride(t *testing.T) {
	_, err := resolveModelClient(context.Background(), "does-not-exist/m1")
	if err == nil {
		t.Fatal("expected error for unknown provider in explicit override")
	}
}

// TestResolveModelClient_InheritsParentProvider is the regression test for the
// auth bug: with a bare model name, the subagent must reuse the parent's
// provider from context — NOT re-guess via FindModel, which iterates the
// provider map in random order and could land on a different provider that
// happens to list the same model (and lacks a valid key).
func TestResolveModelClient_InheritsParentProvider(t *testing.T) {
	parent := &fakeProvider{name: "parent-prov", configured: true, models: []string{"shared-model"}}
	other := &fakeProvider{name: "other-prov", configured: true, models: []string{"shared-model"}}
	providers.Register(parent)
	providers.Register(other)

	ctx := connector.WithModelClient(context.Background(), providers.Client(parent, "shared-model"))

	got, err := resolveModelClient(ctx, "shared-model")
	if err != nil {
		t.Fatalf("resolveModelClient: %v", err)
	}
	if got.Provider() != "parent-prov" {
		t.Errorf("expected parent provider to be inherited, got %q", got.Provider())
	}
	if got.Model() != "shared-model" {
		t.Errorf("expected model %q, got %q", "shared-model", got.Model())
	}
}

func TestResolveModelClient_EmptyModelUsesContext(t *testing.T) {
	parent := &fakeProvider{name: "ctx-prov", configured: true, models: []string{"ctx-model"}}
	providers.Register(parent)

	ctx := connector.WithModelClient(context.Background(), providers.Client(parent, "ctx-model"))

	got, err := resolveModelClient(ctx, "")
	if err != nil {
		t.Fatalf("resolveModelClient: %v", err)
	}
	if got.Provider() != "ctx-prov" {
		t.Errorf("expected context provider, got %q", got.Provider())
	}
	if got.Model() != "ctx-model" {
		t.Errorf("expected model from context %q, got %q", "ctx-model", got.Model())
	}
}

// TestResolveModelClient_BareOverrideDifferentModelReusesProvider covers the
// case a plain "inherit the context ModelClient" cannot: an explicit bare
// model name that differs from the parent's current model. The parent's
// already-resolved provider (its credential) must still be reused, bound to
// the new model — this is the one path that has to fall back to a catalog
// lookup by provider name, since a ModelClient only carries ONE model.
func TestResolveModelClient_BareOverrideDifferentModelReusesProvider(t *testing.T) {
	parent := &fakeProvider{name: "multi-model-prov", configured: true, models: []string{"big-model", "small-model"}}
	providers.Register(parent)

	ctx := connector.WithModelClient(context.Background(), providers.Client(parent, "big-model"))

	got, err := resolveModelClient(ctx, "small-model")
	if err != nil {
		t.Fatalf("resolveModelClient: %v", err)
	}
	if got.Provider() != "multi-model-prov" {
		t.Errorf("expected the parent's provider to be reused, got %q", got.Provider())
	}
	if got.Model() != "small-model" {
		t.Errorf("expected the override model %q, got %q", "small-model", got.Model())
	}
}

func TestResolveModelClient_NoContextFallsBackToLookup(t *testing.T) {
	prov := &fakeProvider{name: "lookup-prov", configured: true, models: []string{"lookup-model"}}
	providers.Register(prov)

	// No model client in context → must fall back to FindModel on the bare name.
	got, err := resolveModelClient(context.Background(), "lookup-model")
	if err != nil {
		t.Fatalf("resolveModelClient: %v", err)
	}
	if got.Provider() != "lookup-prov" {
		t.Errorf("expected looked-up provider, got %q", got.Provider())
	}
	if got.Model() != "lookup-model" {
		t.Errorf("expected model %q, got %q", "lookup-model", got.Model())
	}
}

func TestResolveModelClient_NoContextNoMatch(t *testing.T) {
	_, err := resolveModelClient(context.Background(), "totally-unknown-model")
	if err == nil {
		t.Fatal("expected error when no context model client and no registry match")
	}
}

// =============================================================================
// withIsolatedPool — the subagent's own connection pool
// =============================================================================

// bareModelClient is a connector.ModelClient that does NOT implement
// connector.HTTPInjector — the shape of every hand-written fake ModelClient in
// the agent/tools test suites. It must pass through withIsolatedPool
// untouched, keeping today's "no isolation" behaviour instead of failing.
type bareModelClient struct{ name, model string }

func (b bareModelClient) Provider() string { return b.name }
func (b bareModelClient) Model() string    { return b.model }
func (b bareModelClient) Stream(context.Context, connector.Request) (<-chan stream.Event, error) {
	return nil, nil
}

func TestWithIsolatedPool_PassesThroughNonInjector(t *testing.T) {
	mc := bareModelClient{name: "not-an-injector"}
	if got := withIsolatedPool(mc); got != connector.ModelClient(mc) {
		t.Errorf("withIsolatedPool replaced a non-injector ModelClient: %v", got)
	}
}

// recordingInjector is a Provider that also implements providers.HTTPInjector
// and remembers every client it was handed. providers.Client(...) forwards
// WithHTTP to it, so wrapping it and calling withIsolatedPool exercises the
// whole chain: main.go's type-assert against connector.HTTPInjector →
// providers.clientAdapter.WithHTTP → this provider's WithHTTP.
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
	mc := providers.Client(inj, "m")

	withIsolatedPool(mc)
	withIsolatedPool(mc)

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
	mc := providers.Client(inj, "m")
	withIsolatedPool(mc)

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
	mc := providers.Client(base, "m")

	a := withIsolatedPool(mc)
	b := withIsolatedPool(mc)

	if a == mc || b == mc {
		t.Error("withIsolatedPool returned the shared client instead of a copy")
	}
	if a == b {
		t.Error("two children got the same client value")
	}
	if a.Provider() != "real" || b.Provider() != "real" {
		t.Error("the copies lost the provider identity")
	}
}
