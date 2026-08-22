package pricing

import (
	"os"
	"path/filepath"
	"testing"
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

func TestMissingCatalogIsSilent(t *testing.T) {
	withCatalog(t, "")
	if r, l := Lookup("anthropic", "claude-sonnet-5"); r.Known() || l.Context != 0 {
		t.Fatal("a missing catalog should answer unknown, not panic")
	}
	if CatalogPriced() {
		t.Fatal("CatalogPriced with no catalog should be false")
	}
}

func TestCorruptCatalogIsSilent(t *testing.T) {
	withCatalog(t, "{not json")
	if r, _ := Lookup("", "anything"); r.Known() {
		t.Fatal("a corrupt catalog should answer unknown")
	}
}

func TestCatalogPriced(t *testing.T) {
	withCatalog(t, testCatalog)
	if !CatalogPriced() {
		t.Fatal("catalog with costs should report priced")
	}
	withCatalog(t, `{"openai":{"id":"openai","models":{"m":{"id":"m","name":"m"}}}}`)
	if CatalogPriced() {
		t.Fatal("a stripped catalog should report unpriced")
	}
}
