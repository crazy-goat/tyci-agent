package providers

import (
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/internal/connect"
)

// AuthSource resolves the credential for one provider. Returning "" means
// "I have nothing" — that is what makes sources chainable, and it is also why
// an unresolvable "$ENV_VAR" reference is indistinguishable from no key at all.
type AuthSource interface {
	Key(provider string) string
}

// LiteralAuth is a credential handed over inline. Today's only producer is the
// token embedded in a provider URI ("openai://model@sk-tok@host"), but tests
// and embedders can hand one in directly.
//
// "$ENV_VAR" references are expanded, so a stored literal "$FOO" counts as a
// credential only while FOO is actually exported.
type LiteralAuth string

func (l LiteralAuth) Key(string) string { return connect.ResolveToken(string(l)) }

// AuthFile resolves credentials from ~/.tyci/auth.json (see connect.AuthPath).
//
// A broken or unreadable auth.json is never fatal: the error is reported and
// the next source in the chain gets its turn, exactly as before.
type AuthFile struct {
	// Warn receives the error when auth.json cannot be read or parsed.
	// nil means the default: "Warning: reading auth.json: %v" on stderr.
	Warn func(error)
}

func (a AuthFile) Key(provider string) string {
	key, ok, err := connect.GetKey(provider)
	if err != nil {
		if a.Warn != nil {
			a.Warn(err)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: reading auth.json: %v\n", err)
		}
		return ""
	}
	if !ok {
		return ""
	}
	// Resolve "$ENV_VAR" refs stored in auth.json too. This allows entries
	// like "nexos": "$NEXOS_API_KEY" to work even when the user accidentally
	// single-quoted the value at `provider auth set` time.
	return connect.ResolveToken(key)
}

// EnvAuth resolves credentials from the environment: the provider-specific
// <PROVIDER>_API_KEY first, then the shared OPENCODE_API_KEY.
//
// Values are taken verbatim — unlike auth.json entries they are not run
// through connect.ResolveToken, because an env var holding "$OTHER" is a
// literal, not a reference.
type EnvAuth struct{}

func (EnvAuth) Key(provider string) string {
	if v := os.Getenv(strings.ToUpper(provider) + "_API_KEY"); v != "" {
		return v
	}
	return os.Getenv("OPENCODE_API_KEY")
}

// AuthChain asks each source in order and returns the first non-empty
// credential. It is itself an AuthSource, so chains nest.
type AuthChain []AuthSource

func (c AuthChain) Key(provider string) string {
	for _, s := range c {
		if s == nil {
			continue
		}
		if k := s.Key(provider); k != "" {
			return k
		}
	}
	return ""
}

// DefaultAuth is the lookup a provider uses unless it was built with its own:
// auth.json, then the environment.
//
// The URI token is deliberately NOT part of it. That credential belongs to a
// single ModelEntry rather than to the provider, so Stream prepends its own
// LiteralAuth for the entry it is about to use.
func DefaultAuth() AuthSource { return AuthChain{AuthFile{}, EnvAuth{}} }

// defaultAuthSource is the package default used by providers built without an
// explicit AuthSource.
var defaultAuthSource = DefaultAuth()
