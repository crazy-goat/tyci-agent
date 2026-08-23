package providers

import (
	"sort"
	"strings"
)

// Catalog is a set of providers keyed by name. It is a value, not a package
// global: the CLI uses the package-level Default, while tests (and any future
// embedder) build their own and stay isolated from each other.
type Catalog struct {
	providers map[string]Provider
}

// NewCatalog returns an empty Catalog.
func NewCatalog() *Catalog {
	return &Catalog{providers: make(map[string]Provider)}
}

// Default is the process-wide catalog the CLI registers into and reads from.
// The package-level Register/ListProviders/GetProvider/FindModel functions are
// thin wrappers over it.
var Default = NewCatalog()

// Register adds (or replaces) a provider under its own name.
func (c *Catalog) Register(p Provider) {
	c.providers[p.Name()] = p
}

// ListProviders returns every registered provider, sorted by name.
func (c *Catalog) ListProviders() []Provider {
	var result []Provider
	for _, p := range c.providers {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})
	return result
}

// GetProvider looks a provider up by exact name.
func (c *Catalog) GetProvider(name string) (Provider, bool) {
	p, ok := c.providers[name]
	return p, ok
}

// FindModel resolves a model spec to a provider and a bare model name.
//
// A "provider/model" spec is resolved by exact provider name. A bare model
// name is searched for among CONFIGURED providers only, in map iteration
// order, which is deliberately unspecified. A bare name listed solely by
// providers without a credential does not resolve.
//
// Model IDs may themselves contain slashes (e.g. openrouter's
// "stealth/ox-alpha"), so a spec containing "/" is ambiguous: it is first
// tried verbatim as a full model ID, and only when no provider lists it is it
// split on the FIRST slash as provider/model. This keeps
// "openrouter/stealth/ox-alpha" resolving to provider "openrouter", model
// "stealth/ox-alpha", while never shadowing an explicit provider prefix with
// a same-named model.
func (c *Catalog) FindModel(model string) (Provider, string, bool) {
	if strings.Contains(model, "/") {
		// Full model IDs can contain slashes: try the whole string first.
		for _, p := range c.providers {
			if p.IsConfigured() {
				for _, m := range p.Models() {
					if m == model {
						return p, model, true
					}
				}
			}
		}

		parts := strings.SplitN(model, "/", 2)
		if p, ok := c.providers[parts[0]]; ok {
			return p, parts[1], true
		}
		return nil, "", false
	}
	for _, p := range c.providers {
		if p.IsConfigured() {
			for _, m := range p.Models() {
				if m == model {
					return p, model, true
				}
			}
		}
	}
	return nil, "", false
}

// The package-level functions below operate on Default. They exist so the CLI
// and the agent keep a single well-known catalog without threading it through
// every call site.

func Register(p Provider) { Default.Register(p) }

func ListProviders() []Provider { return Default.ListProviders() }

func GetProvider(name string) (Provider, bool) { return Default.GetProvider(name) }

func FindModel(model string) (Provider, string, bool) { return Default.FindModel(model) }
