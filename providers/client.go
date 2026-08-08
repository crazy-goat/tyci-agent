package providers

import (
	"context"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/stream"
)

// Client implements Provider.Client: it binds this provider to one model so
// the result can be handed to package agent as a connector.ModelClient,
// without agent needing to know about the provider catalog, model.json, or
// auth resolution.
func (p *dynamicProvider) Client(model string) connector.ModelClient {
	return &modelClient{p: p, model: model}
}

// modelClient is the providers-side implementation of connector.ModelClient:
// one provider bound to one model. It is the ONLY way to send a request
// through a provider — the catalog answers questions about models, this
// answers requests.
//
// The provider field is the CONCRETE type, not the Provider interface. That is
// what makes the HTTP-injection path below a single, compiler-checked hop
// instead of a chain of type assertions that fail silently.
type modelClient struct {
	p     *dynamicProvider
	model string
}

// modelClient must satisfy connector.HTTPInjector. main.go's withIsolatedPool
// gives each subagent its own connection pool by type-asserting a ModelClient
// to that interface, and a failed assertion there is invisible: the request
// just goes out over the shared pool and the isolation is gone with no error
// anywhere. This line turns that runtime failure mode into a build failure.
var _ connector.HTTPInjector = (*modelClient)(nil)

func (c *modelClient) Provider() string { return c.p.Name() }
func (c *modelClient) Model() string    { return c.model }

// Stream forwards to the wrapped provider, forcing req.Model to the bound
// model so a caller can never send a client's request to the wrong model.
func (c *modelClient) Stream(ctx context.Context, req connector.Request) (<-chan stream.Event, error) {
	req.Model = c.model
	return c.p.Stream(ctx, req)
}

// WithHTTP implements connector.HTTPInjector: it returns a client whose every
// request goes out over h.
//
// This is the whole injection chain, one link. It used to be three —
// providers.HTTPInjector → clientAdapter.WithHTTP → connector.HTTPInjector —
// with a type assertion at each hop, so forgetting to forward at any of them
// compiled fine and merely lost subagent pool isolation at runtime. Here both
// hops are static: c.p is the concrete provider, and the rebound provider
// mints its own client, so there is exactly one description of how a provider
// becomes a ModelClient. See TestClient_WithHTTPForwardsToProvider.
func (c *modelClient) WithHTTP(h connector.HTTPDoer) connector.ModelClient {
	return c.p.withHTTP(h).Client(c.model)
}
