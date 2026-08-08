package connector

import (
	"context"

	"github.com/decodo/tyci/stream"
)

// ModelClient is one resolved model reachable through one provider:
// everything the agent needs in order to send a request, and nothing about
// where the model was configured or how its credential was found. Package
// providers builds these (see providers.Client); package agent only ever
// consumes them, which is how agent stops importing providers.
type ModelClient interface {
	// Provider names the provider this model is served by (session metadata, UI).
	Provider() string
	// Model is the bare model name to send.
	Model() string
	// Stream sends req and returns the event channel for the response.
	Stream(ctx context.Context, req Request) (<-chan stream.Event, error)
}

// modelClientCtxKey is the context key for WithModelClient/ModelClientFromContext.
type modelClientCtxKey struct{}

// WithModelClient returns a child context carrying mc. This replaces the pair
// of keys providers.WithProvider/WithModel used to require: a ModelClient
// already carries its own model, so one value is enough.
func WithModelClient(ctx context.Context, mc ModelClient) context.Context {
	return context.WithValue(ctx, modelClientCtxKey{}, mc)
}

// ModelClientFromContext extracts the ModelClient carried by ctx, or nil.
func ModelClientFromContext(ctx context.Context) ModelClient {
	if mc, ok := ctx.Value(modelClientCtxKey{}).(ModelClient); ok {
		return mc
	}
	return nil
}

// FullModel returns the "provider/model" display form of mc — the same
// string agent/fallback.go has always rendered in its ToolBlock messages.
func FullModel(mc ModelClient) string {
	return mc.Provider() + "/" + mc.Model()
}

// HTTPInjector is the optional half of ModelClient: an implementation that
// can return a copy of itself bound to a specific HTTP client.
//
// It is separate from ModelClient on purpose, mirroring providers.HTTPInjector
// (the interface it replaces at the agent/main boundary): the agent must not
// know that HTTP exists, and every fake ModelClient in the test suite would
// otherwise have to implement a method it has no use for. Callers that need
// their own transport — today only the subagent runner, which gives each
// child its own connection pool — type-assert to this interface and silently
// keep the shared default client when the assertion fails.
type HTTPInjector interface {
	WithHTTP(HTTPDoer) ModelClient
}
