package providers

import "context"

type providerCtxKey struct{}
type modelCtxKey struct{}

// WithProvider returns a child context that carries the given Provider.
func WithProvider(ctx context.Context, p Provider) context.Context {
	return context.WithValue(ctx, providerCtxKey{}, p)
}

// ProviderFromContext extracts the Provider from ctx, or returns nil.
func ProviderFromContext(ctx context.Context) Provider {
	if p, ok := ctx.Value(providerCtxKey{}).(Provider); ok {
		return p
	}
	return nil
}

// WithModel returns a child context that carries the given model string.
func WithModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, modelCtxKey{}, model)
}

// ModelFromContext extracts the model string from ctx, or returns "".
func ModelFromContext(ctx context.Context) string {
	if m, ok := ctx.Value(modelCtxKey{}).(string); ok {
		return m
	}
	return ""
}
