package providers

import (
	"context"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/stream"
)

// clientAdapter binds a Provider to one model, so it can be handed to
// package agent as a connector.ModelClient without agent needing to know
// about the provider catalog, model.json, or auth resolution.
type clientAdapter struct {
	p     Provider
	model string
}

// Client returns a connector.ModelClient bound to model on provider p. It is
// the seam between the provider catalog and the agent loop: the caller (CLI,
// main.go, workflow engine) resolves "provider/model" to a Provider and a
// bare model name exactly as before, then wraps them with Client before
// handing the result to agent.Run.
func Client(p Provider, model string) connector.ModelClient {
	return &clientAdapter{p: p, model: model}
}

func (c *clientAdapter) Provider() string { return c.p.Name() }
func (c *clientAdapter) Model() string    { return c.model }

// Stream forwards to the wrapped Provider, forcing req.Model to the bound
// model so a caller can never send a client's request to the wrong model.
func (c *clientAdapter) Stream(ctx context.Context, req connector.Request) (<-chan stream.Event, error) {
	req.Model = c.model
	return c.p.Stream(ctx, req)
}

// WithHTTP implements connector.HTTPInjector whenever the wrapped Provider
// implements HTTPInjector, and is a no-op otherwise. This forwarding is the
// whole point of clientAdapter existing as a struct rather than a closure:
// main.go's withIsolatedPool type-asserts a ModelClient against
// connector.HTTPInjector, and without this method that assertion would
// always fail — silently dropping subagent connection-pool isolation for
// every ModelClient, since none of them would satisfy the interface. See
// TestClient_WithHTTPForwardsToProvider.
func (c *clientAdapter) WithHTTP(h connector.HTTPDoer) connector.ModelClient {
	inj, ok := c.p.(HTTPInjector)
	if !ok {
		return c
	}
	return &clientAdapter{p: inj.WithHTTP(h), model: c.model}
}
