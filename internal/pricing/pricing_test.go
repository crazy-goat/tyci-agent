package pricing

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/connect"
)

// withCatalog points the package at a temporary providers.json. HOME is what
// connect.ProvidersJSONPath resolves against, so redirecting it is enough.
func withCatalog(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".tyci"), 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, ".tyci", "providers.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", dir)
	Reset()
	t.Cleanup(Reset)
}

const testCatalog = `{
  "anthropic": {"id":"anthropic","npm":"@ai-sdk/anthropic","name":"Anthropic","models":{
    "claude-sonnet-5":{"id":"claude-sonnet-5","name":"Claude Sonnet 5",
      "cost":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75},
      "limit":{"context":200000,"output":64000}}}},
  "openai": {"id":"openai","npm":"@ai-sdk/openai","name":"OpenAI","models":{
    "gpt-nope":{"id":"gpt-nope","name":"No Prices"}}}
}`

func TestLookup_ByProviderAndModel(t *testing.T) {
	withCatalog(t, testCatalog)
	r, l := Lookup("anthropic", "claude-sonnet-5")
	if !r.Known() || r.Input != 3 || r.CacheRead != 0.3 {
		t.Fatalf("rates = %+v", r)
	}
	if l.Context != 200000 || l.Output != 64000 {
		t.Fatalf("limits = %+v", l)
	}
}

// The status bar knows a model name but not always which provider served it.
func TestLookup_WithoutProviderSearchesAll(t *testing.T) {
	withCatalog(t, testCatalog)
	if r, _ := Lookup("", "claude-sonnet-5"); !r.Known() {
		t.Fatal("model should be found without a provider")
	}
}

// A model.json name need not match the catalog id's case, or may be the
// catalog's display name.
func TestLookup_ByDisplayNameAndCase(t *testing.T) {
	withCatalog(t, testCatalog)
	if r, _ := Lookup("", "Claude Sonnet 5"); !r.Known() {
		t.Fatal("display name should resolve")
	}
	if r, _ := Lookup("", "CLAUDE-SONNET-5"); !r.Known() {
		t.Fatal("lookup should be case-insensitive")
	}
}

// A wrong provider must not hide a model that exists elsewhere.
func TestLookup_WrongProviderStillFindsModel(t *testing.T) {
	withCatalog(t, testCatalog)
	if r, _ := Lookup("openai", "claude-sonnet-5"); !r.Known() {
		t.Fatal("mismatched provider should fall back to a full search")
	}
}

func TestLookup_UnknownModelIsNotKnown(t *testing.T) {
	withCatalog(t, testCatalog)
	r, l := Lookup("anthropic", "no-such-thing")
	if r.Known() || l.Context != 0 {
		t.Fatalf("unknown model returned %+v %+v", r, l)
	}
}

// A model present but unpriced is the case an older cache produces: it must
// read as unknown, never as free.
func TestLookup_PresentButUnpriced(t *testing.T) {
	withCatalog(t, testCatalog)
	if r, _ := Lookup("openai", "gpt-nope"); r.Known() {
		t.Fatal("a model with no cost data must not report Known()")
	}
}

// Two providers list the same model id with different limits and rates. The
// catalog is a map, so without a provider the entry must still be the same on
// every call.
func TestLookup_EmptyProviderIsStableWhenProvidersAgree(t *testing.T) {
	withCatalog(t, `{
  "alpha": {"id":"alpha","name":"Alpha","models":{
    "shared-model":{"id":"shared-model","name":"Shared","cost":{"input":1,"output":2},
      "limit":{"context":100000,"output":1000}}}},
  "beta": {"id":"beta","name":"Beta","models":{
    "shared-model":{"id":"shared-model","name":"Shared","cost":{"input":5,"output":6},
      "limit":{"context":200000,"output":2000}}}}
}`)
	wantRates, wantLimits := Lookup("", "shared-model")
	for i := range 100 {
		r, l := Lookup("", "shared-model")
		if r != wantRates || l != wantLimits {
			t.Fatalf("call %d: got %+v %+v, want %+v %+v", i, r, l, wantRates, wantLimits)
		}
	}
}

func TestMissingCatalogIsSilent(t *testing.T) {
	withCatalog(t, "")
	if r, l := Lookup("anthropic", "claude-sonnet-5"); r.Known() || l.Context != 0 {
		t.Fatal("a missing catalog should answer unknown, not panic")
	}
	if ProviderNeedsPrices("anthropic") {
		t.Fatal("ProviderNeedsPrices with no catalog should be false")
	}
}

func TestCorruptCatalogIsSilent(t *testing.T) {
	withCatalog(t, "{not json")
	if r, _ := Lookup("", "anything"); r.Known() {
		t.Fatal("a corrupt catalog should answer unknown")
	}
}

func TestProviderNeedsPrices(t *testing.T) {
	withCatalog(t, testCatalog)
	// anthropic carries prices; openai in the same catalog does not — the
	// check must be scoped to the provider asked about, not the catalog.
	if ProviderNeedsPrices("anthropic") {
		t.Fatal("provider with costs should report false")
	}
	if !ProviderNeedsPrices("openai") {
		t.Fatal("provider with no cost data on any model should report true")
	}
	if ProviderNeedsPrices("no-such-provider") {
		t.Fatal("a provider absent from the catalog should report false")
	}
}

// Two models in one provider match the same lower-case query (id or display
// name) but not the exact key. Map iteration must not pick a different one
// on each call; the walk is sorted by model id.
func TestFindModel_CaseFallbackIsStable(t *testing.T) {
	p := connect.ModelsDevProvider{Models: map[string]connect.ModelsDevModel{
		"Model-A": {ID: "Model-A", Name: "Shared Name", Cost: connect.ModelsDevCost{Input: 1}},
		"model-a": {ID: "model-a", Name: "Other", Cost: connect.ModelsDevCost{Input: 9}},
		"zebra":   {ID: "zebra", Name: "shared name", Cost: connect.ModelsDevCost{Input: 4}},
	}}
	first, ok := findModel(p, "MODEL-A")
	if !ok {
		t.Fatal("expected a case-insensitive match")
	}
	// "Model-A" sorts before "model-a".
	if first.ID != "Model-A" || first.Cost.Input != 1 {
		t.Fatalf("tie-break = %+v, want Model-A", first)
	}
	for i := range 50 {
		got, ok := findModel(p, "MODEL-A")
		if !ok || got.ID != first.ID || got.Cost != first.Cost {
			t.Fatalf("call %d: got %+v ok=%v, want id %s", i, got, ok, first.ID)
		}
	}
	byName, ok := findModel(p, "SHARED NAME")
	if !ok || byName.ID != "Model-A" {
		t.Fatalf("display-name tie-break = %+v ok=%v, want Model-A", byName, ok)
	}
}
