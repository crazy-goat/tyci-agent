package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/stream"
)

// recordingProvider is a minimal Provider that remembers the last Request it
// was streamed, so tests can check what clientAdapter.Stream actually forwards.
type recordingProvider struct {
	name string
	last Request
}

func (r *recordingProvider) Name() string         { return r.name }
func (r *recordingProvider) IsConfigured() bool   { return true }
func (r *recordingProvider) Models() []string     { return nil }
func (r *recordingProvider) FreeModels() []string { return nil }
func (r *recordingProvider) Stream(ctx context.Context, req Request) (<-chan stream.Event, error) {
	r.last = req
	return nil, errors.New("recordingProvider does not stream")
}

func TestClient_ProviderAndModel(t *testing.T) {
	p := &recordingProvider{name: "acme"}
	mc := Client(p, "big-1")

	if got := mc.Provider(); got != "acme" {
		t.Errorf("Provider() = %q, want %q", got, "acme")
	}
	if got := mc.Model(); got != "big-1" {
		t.Errorf("Model() = %q, want %q", got, "big-1")
	}
}

// Stream must always send the bound model, never whatever happens to be set
// on the incoming Request — a caller cannot accidentally send a client's
// request to the wrong model.
func TestClient_StreamForcesBoundModel(t *testing.T) {
	p := &recordingProvider{name: "acme"}
	mc := Client(p, "big-1")

	_, _ = mc.Stream(context.Background(), connector.Request{Model: "some-other-model"})

	if p.last.Model != "big-1" {
		t.Errorf("forwarded Request.Model = %q, want the bound model %q", p.last.Model, "big-1")
	}
}

// WithHTTP must forward to the wrapped Provider's HTTPInjector when present.
// This is the regression this stage explicitly calls out: main.go's
// withIsolatedPool type-asserts a ModelClient to connector.HTTPInjector, and
// if clientAdapter did not forward, that assertion would always fail —
// silently turning off subagent connection-pool isolation for every model
// client, with no compile error to catch it.
func TestClient_WithHTTPForwardsToProvider(t *testing.T) {
	reg, seen := capturingRegistry(connector.KindOpenAI)
	base := newDynamicProvider("injected", []ModelEntry{
		{Name: "m", URI: "openai://m@sk-tok@api.example.invalid"},
	}, Deps{Connectors: reg})

	mc := Client(base, "m")
	inj, ok := mc.(connector.HTTPInjector)
	if !ok {
		t.Fatal("clientAdapter does not implement connector.HTTPInjector")
	}

	doer := &stubDoer{}
	bound := inj.WithHTTP(doer)

	if _, err := bound.Stream(context.Background(), connector.Request{Model: "m"}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if seen.HTTP != connector.HTTPDoer(doer) {
		t.Errorf("Endpoint.HTTP = %v, want the injected doer", seen.HTTP)
	}
	// The bound copy must keep its identity — same provider, same model.
	if bound.Provider() != "injected" || bound.Model() != "m" {
		t.Errorf("WithHTTP changed identity: Provider()=%q Model()=%q", bound.Provider(), bound.Model())
	}
}

// A ModelClient wrapping a Provider that does NOT implement HTTPInjector
// (every fake in the agent/main test suites) must return an unchanged,
// still-usable client instead of panicking or losing the binding — the same
// "no isolation, but no crash" contract providers.HTTPInjector documents.
func TestClient_WithHTTPNoopWhenProviderIsNotInjector(t *testing.T) {
	p := &catalogStub{name: "no-injector", configured: true}
	mc := Client(p, "m1")

	inj, ok := mc.(connector.HTTPInjector)
	if !ok {
		t.Fatal("clientAdapter does not implement connector.HTTPInjector")
	}
	bound := inj.WithHTTP(&stubDoer{})

	if bound.Provider() != "no-injector" || bound.Model() != "m1" {
		t.Errorf("no-op WithHTTP changed identity: Provider()=%q Model()=%q", bound.Provider(), bound.Model())
	}
}
