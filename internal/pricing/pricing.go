// Package pricing answers two questions about the model in use: what a token
// costs, and how many of them fit.
//
// Both answers come from the models.dev catalog already cached at
// ~/.tyci/providers.json, so this package adds no network calls and no second
// source of truth. What it does add is tolerance for a catalog that cannot
// answer: older tyci versions re-marshalled the catalog through a struct that
// had no cost or limit fields, so an existing cache is likely to be silently
// stripped of both. That case is reported as "unknown" rather than as zero —
// a session that cost real money must not display "$0.00".
package pricing

import (
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/decodo/tyci/internal/connect"
)

// Rates is the price of a million tokens, in USD, split by how the tokens
// were counted. CacheRead/CacheWrite are zero for providers that do not price
// caching separately, in which case Input applies.
type Rates struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Known reports whether the catalog actually priced this model. An all-zero
// Rates is indistinguishable from a free model otherwise.
func (r Rates) Known() bool {
	return r.Input > 0 || r.Output > 0 || r.CacheRead > 0 || r.CacheWrite > 0
}

// Limits is the model's context window and maximum output, in tokens. Zero
// means the catalog did not say.
type Limits struct {
	Context int
	Output  int
}

var (
	mu     sync.Mutex
	loaded bool
	cat    map[string]connect.ModelsDevProvider
)

// catalog loads and caches ~/.tyci/providers.json. A missing or unparsable
// catalog is a permanent empty answer for this process: the file does not
// change under a running session, and retrying a failed read on every status
// repaint would be the wrong trade.
func catalog() map[string]connect.ModelsDevProvider {
	mu.Lock()
	defer mu.Unlock()
	if loaded {
		return cat
	}
	loaded = true
	data, err := os.ReadFile(connect.ProvidersJSONPath())
	if err != nil {
		return nil
	}
	var parsed map[string]connect.ModelsDevProvider
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	cat = parsed
	return cat
}

// Reset drops the cached catalog. Tests use it; nothing else should.
func Reset() {
	mu.Lock()
	loaded, cat = false, nil
	mu.Unlock()
}

// Lookup returns the rates and limits for a model. provider may be empty, in
// which case every provider is searched — useful because the TUI knows the
// display name of the model long before it knows which provider served it.
//
// A model id is matched exactly first, then case-insensitively, then by the
// catalog's display name, because the name shown in the status bar comes from
// the user's model.json and need not be the catalog's id.
func Lookup(provider, model string) (Rates, Limits) {
	c := catalog()
	if c == nil || model == "" {
		return Rates{}, Limits{}
	}
	if provider != "" {
		if p, ok := c[provider]; ok {
			if m, ok := findModel(p, model); ok {
				return rates(m), limits(m)
			}
		}
		// Fall through: a mismatched provider name is not a reason to give up
		// on a model id that is unique across the catalog anyway.
	}
	for _, p := range c {
		if m, ok := findModel(p, model); ok {
			return rates(m), limits(m)
		}
	}
	return Rates{}, Limits{}
}

func findModel(p connect.ModelsDevProvider, model string) (connect.ModelsDevModel, bool) {
	if m, ok := p.Models[model]; ok {
		return m, true
	}
	want := strings.ToLower(model)
	for id, m := range p.Models {
		if strings.ToLower(id) == want || strings.ToLower(m.ID) == want || strings.ToLower(m.Name) == want {
			return m, true
		}
	}
	return connect.ModelsDevModel{}, false
}

func rates(m connect.ModelsDevModel) Rates {
	return Rates{
		Input:      m.Cost.Input,
		Output:     m.Cost.Output,
		CacheRead:  m.Cost.CacheRead,
		CacheWrite: m.Cost.CacheWrite,
	}
}

func limits(m connect.ModelsDevModel) Limits {
	return Limits{Context: m.Limit.Context, Output: m.Limit.Output}
}

// ProviderNeedsPrices reports whether provider exists in the catalog, has at
// least one model, but carries no cost data for any of them — the signature
// of a provider whose pricing was stripped by an old tyci build (see doc
// comment on connect.ModelsDevModel) or was hand-added without price data.
//
// This is deliberately scoped to one provider rather than the whole catalog:
// a single priced provider (a hand-maintained gateway, say) must not
// suppress the warning for every other provider forever, and a provider that
// does carry some prices should not have a model with a genuine $0 cost
// flagged as missing data — only "this provider has priced nothing at all"
// is a reliable signal that its data was never captured.
//
// Returns false if provider is missing from the catalog: there is nothing
// to warn about a provider tyci does not know.
func ProviderNeedsPrices(provider string) bool {
	p, ok := catalog()[provider]
	if !ok || len(p.Models) == 0 {
		return false
	}
	for _, m := range p.Models {
		if m.Cost.Input > 0 || m.Cost.Output > 0 {
			return false
		}
	}
	return true
}
