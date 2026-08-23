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

// conversationCtxKey is the context key for WithConversation/
// ConversationFromContext.
type conversationCtxKey struct{}

// WithConversation returns a child context carrying msgs — the conversation
// history as it stands at the point a tool call is made. agent/run_once.go
// stamps this once per round, alongside WithModelClient, so a tool (in
// practice: the "subagent" tool's inherit_history option, tools/subagent.go)
// can read back "the transcript up to here" without the tools package
// importing agent or session to get at it.
//
// The round boundary this is stamped at is always a clean one — it never
// lands mid tool-call/result pair, because the previous round's tool results
// are fully appended to msgs before the next round (and its tool calls)
// begin — so a reader does not need to sanitize it the way a truncated
// prefix cut (ForkAtIndex/ForkAtEventID in package session) does.
//
// This snapshot is a slice header, not a deep copy: it is safe to read from
// an async subagent goroutine after the parent keeps running only because
// nothing in this codebase mutates an already-appended Message in place —
// the parent only ever appends past this snapshot's captured length. Do not
// add in-place mutation of an existing slice element (e.g. rewriting a
// message during compaction) without revisiting this.
func WithConversation(ctx context.Context, msgs []Message) context.Context {
	return context.WithValue(ctx, conversationCtxKey{}, msgs)
}

// ConversationFromContext extracts the conversation history carried by ctx
// (see WithConversation), or nil if none was stamped.
func ConversationFromContext(ctx context.Context) []Message {
	if msgs, ok := ctx.Value(conversationCtxKey{}).([]Message); ok {
		return msgs
	}
	return nil
}

// HTTPInjector is the optional half of ModelClient: an implementation that
// can return a copy of itself bound to a specific HTTP client.
//
// It is separate from ModelClient on purpose, and it is the ONLY interface in
// the injection path: the agent must not know that HTTP exists, and every fake
// ModelClient in the test suite would otherwise have to implement a method it
// has no use for. Callers that need their own transport — today only the
// subagent runner, which gives each child its own connection pool —
// type-assert to this interface and silently keep the shared default client
// when the assertion fails.
//
// That silent fallback is intended only for fakes. Every client the providers
// package hands out satisfies this interface, guaranteed at build time by a
// `var _ connector.HTTPInjector` assertion in providers/client.go, so the
// production path cannot lose isolation by accident.
type HTTPInjector interface {
	WithHTTP(HTTPDoer) ModelClient
}
