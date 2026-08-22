package connect

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeModelsDevDoer is an HTTPDoer that returns a canned models.dev response
// regardless of the request, so RefreshModels/EnsureProvidersJSON can be
// exercised without a network call.
type fakeModelsDevDoer struct {
	body       string
	statusCode int
}

func (f fakeModelsDevDoer) Do(req *http.Request) (*http.Response, error) {
	status := f.statusCode
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

// withFakeModelsDev swaps the package-level HTTP client for the duration of
// the test and restores it afterward.
func withFakeModelsDev(t *testing.T, body string) {
	t.Helper()
	restore := SetHTTPClientForTests(fakeModelsDevDoer{body: body})
	t.Cleanup(restore)
}

// priced is a models.dev-shaped payload for a single provider/model with
// cost and limit populated, matching what the real API sends.
const pricedPayload = `{
  "anthropic": {
    "id": "anthropic",
    "npm": "@ai-sdk/anthropic",
    "name": "Anthropic",
    "api": "anthropic",
    "models": {
      "claude-x": {
        "id": "claude-x",
        "name": "Claude X",
        "cost": {"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75},
        "limit": {"context": 200000, "output": 8192}
      }
    }
  }
}`

// The full round trip through pricing.Lookup — the regression that started
// this — is covered in internal/pricing (TestRefreshModels_RoundTripPreservesPrices
// in pricing_roundtrip_test.go), since pricing imports connect and a test
// here importing pricing back would be an import cycle.
func TestRefreshModels_FetchCarriesCostAndLimitOnDisk(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)
	withFakeModelsDev(t, pricedPayload)

	if _, _, err := RefreshModels("", false); err != nil {
		t.Fatalf("RefreshModels() error: %v", err)
	}

	got := readCatalog(t)
	m, ok := got["anthropic"].Models["claude-x"]
	if !ok {
		t.Fatalf("claude-x missing from written catalog: %+v", got)
	}
	if m.Cost.Input != 3 || m.Cost.Output != 15 || m.Cost.CacheRead != 0.3 || m.Cost.CacheWrite != 3.75 {
		t.Errorf("Cost = %+v, want {3 15 0.3 3.75}", m.Cost)
	}
	if m.Limit.Context != 200000 || m.Limit.Output != 8192 {
		t.Errorf("Limit = %+v, want {200000 8192}", m.Limit)
	}
}

func TestRefreshModels_PreservesHandAddedProvider(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)
	withFakeModelsDev(t, pricedPayload)

	// A hand-maintained provider models.dev has never heard of — e.g. a
	// private gateway with its own real prices.
	existing := map[string]ModelsDevProvider{
		"nexos": {
			ID:   "nexos",
			NPM:  "",
			Name: "Nexos",
			API:  "openai",
			Models: map[string]ModelsDevModel{
				"nexos-model": {
					ID:   "nexos-model",
					Name: "Nexos Model",
					Cost: ModelsDevCost{Input: 1, Output: 2},
				},
			},
		},
	}
	writeCatalog(t, existing)

	if _, _, err := RefreshModels("", false); err != nil {
		t.Fatalf("RefreshModels() error: %v", err)
	}

	got := readCatalog(t)
	nexos, ok := got["nexos"]
	if !ok {
		t.Fatalf("nexos provider was dropped by refresh, catalog: %+v", got)
	}
	if nexos.Models["nexos-model"].Cost.Input != 1 {
		t.Errorf("nexos prices were altered: %+v", nexos.Models["nexos-model"])
	}
	if _, ok := got["anthropic"]; !ok {
		t.Errorf("fetched anthropic provider missing from merged catalog")
	}
}

func TestRefreshModels_ReplacesProviderFetchAlsoReturns(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)
	withFakeModelsDev(t, pricedPayload)

	// Existing cached copy of "anthropic" with stale/different data — the
	// fetch should overwrite it, not merge fields within it.
	existing := map[string]ModelsDevProvider{
		"anthropic": {
			ID:   "anthropic",
			NPM:  "@ai-sdk/anthropic",
			Name: "Anthropic (stale)",
			Models: map[string]ModelsDevModel{
				"claude-old": {ID: "claude-old", Name: "Claude Old"},
			},
		},
	}
	writeCatalog(t, existing)

	imported, keptUnchanged, err := RefreshModels("", false)
	if err != nil {
		t.Fatalf("RefreshModels() error: %v", err)
	}
	if keptUnchanged != 0 {
		t.Fatalf("keptUnchanged = %d, want 0", keptUnchanged)
	}
	if len(imported) != 1 || !imported[0].Replaced {
		t.Fatalf("imported = %+v, want one entry marked Replaced", imported)
	}

	got := readCatalog(t)
	anthropic := got["anthropic"]
	if anthropic.Name != "Anthropic" {
		t.Errorf("anthropic.Name = %q, want fresh %q", anthropic.Name, "Anthropic")
	}
	if _, hasOld := anthropic.Models["claude-old"]; hasOld {
		t.Errorf("stale model claude-old survived the replace")
	}
	if _, hasNew := anthropic.Models["claude-x"]; !hasNew {
		t.Errorf("fresh model claude-x missing after replace")
	}
}

func TestRefreshModels_NPMFilteredProviderDoesNotWipeExisting(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)
	// The fetch carries a provider with an npm package tyci does not
	// recognize (not in npmToAPIType), so it is filtered out of import.
	withFakeModelsDev(t, `{
		"mystery": {
			"id": "mystery",
			"npm": "@ai-sdk/unknown-vendor",
			"name": "Mystery Vendor",
			"models": {}
		}
	}`)

	existing := map[string]ModelsDevProvider{
		"mystery": {
			ID:   "mystery",
			Name: "Mystery Vendor (hand-added)",
			Models: map[string]ModelsDevModel{
				"m1": {ID: "m1", Name: "M1", Cost: ModelsDevCost{Input: 5}},
			},
		},
	}
	writeCatalog(t, existing)

	imported, keptUnchanged, err := RefreshModels("", false)
	if err != nil {
		t.Fatalf("RefreshModels() error: %v", err)
	}
	if len(imported) != 0 {
		t.Fatalf("imported = %+v, want none (npm-filtered)", imported)
	}
	if keptUnchanged != 1 {
		t.Fatalf("keptUnchanged = %d, want 1", keptUnchanged)
	}

	got := readCatalog(t)
	mystery, ok := got["mystery"]
	if !ok {
		t.Fatalf("npm-filtered fetch wiped the existing mystery provider")
	}
	if mystery.Name != "Mystery Vendor (hand-added)" {
		t.Errorf("mystery.Name = %q, want untouched hand-added value", mystery.Name)
	}
	if mystery.Models["m1"].Cost.Input != 5 {
		t.Errorf("mystery model prices were altered")
	}
}

func TestRefreshModels_DryRunReportsKeptVsReplaced(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)
	withFakeModelsDev(t, pricedPayload)

	existing := map[string]ModelsDevProvider{
		"anthropic": {ID: "anthropic", NPM: "@ai-sdk/anthropic", Name: "stale"},
		"nexos":     {ID: "nexos", Name: "Nexos"},
	}
	writeCatalog(t, existing)

	imported, keptUnchanged, err := RefreshModels("", true)
	if err != nil {
		t.Fatalf("RefreshModels(dryRun) error: %v", err)
	}
	if len(imported) != 1 || !imported[0].Replaced {
		t.Fatalf("imported = %+v, want one Replaced entry", imported)
	}
	if keptUnchanged != 1 {
		t.Fatalf("keptUnchanged = %d, want 1 (nexos)", keptUnchanged)
	}

	// Dry run must not write anything.
	got := readCatalog(t)
	if got["anthropic"].Name != "stale" {
		t.Errorf("dry run modified the catalog on disk: %+v", got)
	}
}

// A single-provider refresh (`--provider anthropic`) must not touch any
// other provider already in the catalog — in particular it must not strip
// prices off providers the fetch never mentions.
func TestRefreshModels_SingleProviderFilterLeavesOthersUntouched(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)
	withFakeModelsDev(t, pricedPayload)

	existing := map[string]ModelsDevProvider{
		"anthropic": {ID: "anthropic", NPM: "@ai-sdk/anthropic", Name: "stale"},
		"nexos": {
			ID:   "nexos",
			Name: "Nexos",
			Models: map[string]ModelsDevModel{
				"nexos-model": {ID: "nexos-model", Name: "Nexos Model", Cost: ModelsDevCost{Input: 1, Output: 2}},
			},
		},
		"openai": {
			ID:   "openai",
			Name: "OpenAI (hand-added)",
			Models: map[string]ModelsDevModel{
				"gpt-hand": {ID: "gpt-hand", Name: "GPT Hand", Cost: ModelsDevCost{Input: 4}},
			},
		},
	}
	writeCatalog(t, existing)

	imported, keptUnchanged, err := RefreshModels("anthropic", false)
	if err != nil {
		t.Fatalf("RefreshModels(--provider anthropic) error: %v", err)
	}
	if len(imported) != 1 || imported[0].Name != "Anthropic" {
		t.Fatalf("imported = %+v, want exactly the filtered anthropic entry", imported)
	}
	if keptUnchanged != 2 {
		t.Fatalf("keptUnchanged = %d, want 2 (nexos, openai)", keptUnchanged)
	}

	got := readCatalog(t)
	if got["anthropic"].Name != "Anthropic" {
		t.Errorf("anthropic not refreshed: %+v", got["anthropic"])
	}
	nexos, ok := got["nexos"]
	if !ok || nexos.Models["nexos-model"].Cost.Input != 1 {
		t.Errorf("nexos provider/prices did not survive a --provider anthropic refresh: %+v", nexos)
	}
	openai, ok := got["openai"]
	if !ok || openai.Name != "OpenAI (hand-added)" || openai.Models["gpt-hand"].Cost.Input != 4 {
		t.Errorf("openai provider/prices did not survive a --provider anthropic refresh: %+v", openai)
	}
}

// RefreshModels must refuse to run against an existing providers.json it
// cannot parse, rather than merging on top of "empty" and overwriting the
// unreadable file with a partial fetch — the data-loss guard this backlog
// item exists to add a test for.
func TestRefreshModels_RefusesUnparsableExistingCatalog(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)
	withFakeModelsDev(t, pricedPayload)

	path := ProvidersJSONPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const corrupt = "{not valid json"
	if err := os.WriteFile(path, []byte(corrupt), 0644); err != nil {
		t.Fatalf("write corrupt catalog: %v", err)
	}

	_, _, err := RefreshModels("", false)
	if err == nil {
		t.Fatal("RefreshModels() with an unparsable existing catalog: want error, got nil")
	}

	// The corrupt file must survive untouched — no partial fetch written
	// over it.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read catalog after refusal: %v", readErr)
	}
	if string(data) != corrupt {
		t.Errorf("catalog was modified despite refusal: %q", data)
	}
}

func writeCatalog(t *testing.T, m map[string]ModelsDevProvider) {
	t.Helper()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	path := ProvidersJSONPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
}

func readCatalog(t *testing.T) map[string]ModelsDevProvider {
	t.Helper()
	data, err := os.ReadFile(ProvidersJSONPath())
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	var m map[string]ModelsDevProvider
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal catalog: %v", err)
	}
	return m
}
