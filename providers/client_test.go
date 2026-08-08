package providers

import (
	"context"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/stream"
)

// requestRecorder is a connector that remembers every Request it was asked to
// stream, so a test can see what a ModelClient actually forwarded down to the
// wire layer.
type requestRecorder struct{ seen *[]Request }

func (c *requestRecorder) Kind() string { return connector.KindOpenAI }
func (c *requestRecorder) Stream(_ context.Context, req Request, _ func(stream.Event) error) error {
	*c.seen = append(*c.seen, req)
	return nil
}

// recordingClientProvider builds a real provider whose only connector records
// requests. The test goes through the production path (Provider.Client →
// modelClient.Stream → connector) rather than a hand-written provider stub,
// because after the catalog/transport split a stub Provider mints its OWN
// client and would never exercise modelClient at all.
func recordingClientProvider(t *testing.T, name string) (Provider, *[]Request) {
	t.Helper()
	var seen []Request
	reg := connector.NewRegistry()
	reg.Register(connector.KindOpenAI, func(ep connector.Endpoint) (connector.Connector, error) {
		return &requestRecorder{seen: &seen}, nil
	})
	p := NewProvider(name, []ModelEntry{
		{Name: "big-1", URI: "openai://big-1@sk-tok@api.example.invalid"},
	}, Deps{Connectors: reg})
	return p, &seen
}

// drainEvents waits for the provider's streaming goroutine to finish. Stream
// hands back a channel and closes it when the connector returns, so draining
// it is the synchronisation point for anything the connector recorded.
func drainEvents(ch <-chan stream.Event) {
	for range ch {
	}
}

func TestClient_ProviderAndModel(t *testing.T) {
	p, _ := recordingClientProvider(t, "acme")
	mc := p.Client("big-1")

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
	p, seen := recordingClientProvider(t, "acme")
	mc := p.Client("big-1")

	ch, err := mc.Stream(context.Background(), connector.Request{Model: "some-other-model"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drainEvents(ch)

	if len(*seen) != 1 {
		t.Fatalf("connector saw %d requests, want 1", len(*seen))
	}
	if (*seen)[0].Model != "big-1" {
		t.Errorf("forwarded Request.Model = %q, want the bound model %q", (*seen)[0].Model, "big-1")
	}
}

// WithHTTP must actually reach the wire: the injected client has to land in
// connector.Endpoint.HTTP. This is the regression this stage calls out —
// main.go's withIsolatedPool type-asserts a ModelClient to
// connector.HTTPInjector, and anything that dropped the client along the way
// would silently turn off subagent connection-pool isolation with no compile
// error to catch it. The assertion itself can no longer fail (see the
// `var _ connector.HTTPInjector` line in client.go); this test covers the
// rest of the hop, which no signature can prove.
func TestClient_WithHTTPForwardsToProvider(t *testing.T) {
	reg, seen := capturingRegistry(connector.KindOpenAI)
	base := newDynamicProvider("injected", []ModelEntry{
		{Name: "m", URI: "openai://m@sk-tok@api.example.invalid"},
	}, Deps{Connectors: reg})

	mc := base.Client("m")
	inj, ok := mc.(connector.HTTPInjector)
	if !ok {
		t.Fatal("modelClient does not implement connector.HTTPInjector")
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
