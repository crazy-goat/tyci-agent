// Package connector holds one implementation per model-provider wire protocol
// (OpenAI chat-completions, Anthropic messages, Gemini generateContent).
//
// A connector owns exactly one concern: turning a canonical Request into the
// protocol's request body and turning the response back into stream.Event.
// Everything that is *configuration* — parsing provider URIs, resolving API
// keys from auth.json/env, the model catalog — stays in package providers.
//
// This package must never import providers: providers imports connector, and
// the canonical message types therefore live here (see message.go), with
// aliases left behind in providers so existing consumers keep compiling.
package connector

import (
	"context"
	"fmt"
	"net/http"

	"github.com/decodo/tyci/stream"
)

// Connector kinds. These match the api_type of a provider URI
// (see internal/tyciconfig.ProviderURI).
const (
	KindOpenAI    = "openai"
	KindAnthropic = "anthropic"
	KindGemini    = "gemini"
)

// HTTPDoer is the minimal HTTP surface a connector needs. It exists so an
// Endpoint can carry an injected client instead of reaching for a global one.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Endpoint is the resolved, protocol-agnostic address of a model API: where to
// send the request and how to authenticate.
type Endpoint struct {
	// BaseURL is the scheme+host part, e.g. "https://api.openai.com".
	BaseURL string
	// Path is the request path. Today it is computed by providers.parseURI,
	// which still knows the per-apiType defaults; moving that knowledge into
	// the connectors is Etap 3 of docs/architecture-refactor.md.
	Path string
	// APIKey is the already-resolved credential (URI → auth.json → env).
	APIKey string
	// Headers are extra headers to send. Not consumed yet — the api.StreamX
	// functions still set their own headers (Etap 3).
	Headers map[string]string
	// HTTP is the client to use; nil means "use the default". Not consumed
	// yet either — api.ClientFromContext still decides (Etap 3).
	HTTP HTTPDoer
	// Options carries connector-specific switches resolved from the provider
	// URI query string (currently only "reasoning" for the OpenAI connector).
	// Kept as a string map so Endpoint stays protocol-agnostic.
	Options map[string]string
}

// URL returns the full request URL.
func (e Endpoint) URL() string { return e.BaseURL + e.Path }

// option returns the value of a connector option, or "" when unset.
func (e Endpoint) option(name string) string {
	if e.Options == nil {
		return ""
	}
	return e.Options[name]
}

// Connector streams one request against one provider protocol. Implementations
// are cheap values built per request by a Factory.
type Connector interface {
	// Kind reports the protocol this connector speaks (KindOpenAI, ...).
	Kind() string
	// Stream sends req and calls emit for every event produced. It returns
	// when the stream is finished or fails; the caller owns any channel
	// plumbing and goroutine.
	Stream(ctx context.Context, req Request, emit func(stream.Event) error) error
}

// Factory builds a Connector bound to a resolved Endpoint.
type Factory func(Endpoint) (Connector, error)

// Registry maps a connector kind to its Factory. It is a value, not a package
// global: callers (and tests) build their own and can register fakes.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Register adds (or replaces) the factory for a kind.
func (r *Registry) Register(kind string, f Factory) {
	r.factories[kind] = f
}

// Has reports whether a kind is registered.
func (r *Registry) Has(kind string) bool {
	_, ok := r.factories[kind]
	return ok
}

// Kinds returns the registered kinds, unordered.
func (r *Registry) Kinds() []string {
	kinds := make([]string, 0, len(r.factories))
	for k := range r.factories {
		kinds = append(kinds, k)
	}
	return kinds
}

// New builds the connector for kind, bound to ep.
func (r *Registry) New(kind string, ep Endpoint) (Connector, error) {
	f, ok := r.factories[kind]
	if !ok {
		return nil, fmt.Errorf("unknown connector kind %q", kind)
	}
	return f(ep)
}

// DefaultRegistry returns a Registry with the built-in connectors registered.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(KindOpenAI, NewOpenAI)
	r.Register(KindAnthropic, NewAnthropic)
	r.Register(KindGemini, NewGemini)
	return r
}
