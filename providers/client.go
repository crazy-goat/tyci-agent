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
// one provider bound to one model. It is the ONLY exported way to send a
// request through a provider — the catalog answers questions about models,
// this answers requests.
type modelClient struct {
	p     Provider
	model string
}

func (c *modelClient) Provider() string { return c.p.Name() }
func (c *modelClient) Model() string    { return c.model }

// Stream forwards to the wrapped provider, forcing req.Model to the bound
// model so a caller can never send a client's request to the wrong model.
func (c *modelClient) Stream(ctx context.Context, req connector.Request) (<-chan stream.Event, error) {
	req.Model = c.model
	return c.p.Stream(ctx, req)
}

// WithHTTP implements connector.HTTPInjector whenever the wrapped provider
// implements HTTPInjector, and is a no-op otherwise. This forwarding is the
// whole point of modelClient existing as a struct rather than a closure:
// main.go's withIsolatedPool type-asserts a ModelClient against
// connector.HTTPInjector, and without this method that assertion would
// always fail — silently dropping subagent connection-pool isolation for
// every ModelClient, since none of them would satisfy the interface. See
// TestClient_WithHTTPForwardsToProvider.
func (c *modelClient) WithHTTP(h connector.HTTPDoer) connector.ModelClient {
	inj, ok := c.p.(HTTPInjector)
	if !ok {
		return c
	}
	// The rebound provider mints its own client, so there is exactly one
	// description of how a provider becomes a ModelClient.
	return inj.WithHTTP(h).Client(c.model)
}
