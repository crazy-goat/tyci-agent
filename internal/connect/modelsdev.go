package connect

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ModelsDevProvider represents a provider from models.dev API.
type ModelsDevProvider struct {
	ID     string                    `json:"id"`
	Env    []string                  `json:"env"`
	NPM    string                    `json:"npm"`
	Name   string                    `json:"name"`
	API    string                    `json:"api"`
	Models map[string]ModelsDevModel `json:"models"`
}

// ModelsDevModel represents a model from models.dev API.
//
// Cost and Limit are kept because the catalog is re-marshalled through this
// struct when it is cached (see RefreshModels): a field that is absent here is
// a field that is silently dropped on disk. The status bar cannot price a
// session or show how full the context is without them.
type ModelsDevModel struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Cost  ModelsDevCost  `json:"cost"`
	Limit ModelsDevLimit `json:"limit"`
}

// ModelsDevCost is USD per million tokens, as models.dev publishes it.
type ModelsDevCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// ModelsDevLimit is the model's context window and max output, in tokens.
type ModelsDevLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// modelsDevURL is the canonical endpoint for models.dev API.
const modelsDevURL = "https://models.dev/api.json"

// npmToAPIType maps npm package names to tyci API types.
var npmToAPIType = map[string]string{
	"@ai-sdk/openai":            "openai",
	"@ai-sdk/anthropic":         "anthropic",
	"@ai-sdk/gemini":            "gemini",
	"@ai-sdk/openai-compatible": "openai",
}

// RefreshProvider holds the result of importing a provider.
type RefreshProvider struct {
	Name     string
	Type     string
	Models   int
	Replaced bool // true if this id already existed in the cached catalog
}

// ProvidersJSONPath returns the path to the cached providers catalog.
func ProvidersJSONPath() string {
	return filepath.Join(tyciDir(), "providers.json")
}

// ModelJSONPath returns the path to model.json (legacy, used by `tyci connect`).
func ModelJSONPath() string {
	return filepath.Join(tyciDir(), "model.json")
}

func tyciDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
		if home == "" {
			home = "/tmp"
		}
	}
	return filepath.Join(home, ".tyci")
}

// EnsureProvidersJSON downloads and caches the models.dev catalog if not present.
// Returns nil if the catalog is already on disk or after a successful download.
// Network errors are returned so the caller can decide whether to abort or warn.
func EnsureProvidersJSON() error {
	path := ProvidersJSONPath()
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat providers.json: %w", err)
	}
	body, err := fetchModelsDev(defaultHTTPClient)
	if err != nil {
		return err
	}
	return writeCatalogAtomically(path, body)
}

// RefreshModels fetches models from models.dev and merges them into the
// cached catalog. providerFilter is an optional comma-separated list of
// provider IDs to keep; if non-empty, providers outside the filter are
// dropped before writing.
//
// This is a merge, not an overwrite: any provider already in the cached
// catalog that the fetch does not mention (npm-filtered out, provider-filter
// excluded, or simply unknown to models.dev — a hand-added gateway provider,
// for example) survives untouched. A provider models.dev does carry replaces
// the cached copy, which is the point of a refresh — that is how a stale or
// price-less cache (see doc comment on ModelsDevModel) gets repaired.
//
// dryRun if true, only reports what would be imported without writing.
// keptUnchanged is the number of existing providers the fetch left alone.
// skippedZeroModels is the number of existing providers that were fetched but
// arrived with zero models, and were therefore kept as-is rather than emptied
// (see the loop below). These are distinct from keptUnchanged: those were not
// carried by models.dev at all, while skipped ones were fetched and indicate
// a degraded fetch — the caller should say so, because the cached prices may
// be stale.
func RefreshModels(providerFilter string, dryRun bool) (imported []RefreshProvider, keptUnchanged int, skippedZeroModels int, err error) {
	body, err := fetchModelsDev(defaultHTTPClient)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("fetching models.dev: %w", err)
	}

	var fetched map[string]ModelsDevProvider
	if err := json.Unmarshal(body, &fetched); err != nil {
		return nil, 0, 0, fmt.Errorf("parsing models.dev: %w", err)
	}

	existing, err := readExistingCatalog()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("reading existing providers.json: %w", err)
	}

	filter := parseFilter(providerFilter)

	merged := make(map[string]ModelsDevProvider, len(existing)+len(fetched))
	for id, p := range existing {
		merged[id] = p
	}

	replacedIDs := make(map[string]bool)
	for id, p := range fetched {
		if filter != nil && !filter[id] {
			continue
		}
		if _, ok := npmToAPIType[p.NPM]; !ok {
			continue
		}
		cachedP, existed := existing[id]
		// A degraded or partial fetch can serve a provider with zero models.
		// Swapping it in would silently empty a cached entry that had models
		// (and prices) — the exact data loss this whole function exists to
		// prevent. Skip the swap in that one case; every other case,
		// including "incoming has models, cached had none", still replaces.
		if len(p.Models) == 0 && len(cachedP.Models) > 0 {
			skippedZeroModels++
			continue
		}
		merged[id] = p
		replacedIDs[id] = existed
		imported = append(imported, RefreshProvider{
			Name:     p.Name,
			Type:     npmToAPIType[p.NPM],
			Models:   len(p.Models),
			Replaced: existed,
		})
	}

	replaced := 0
	for _, wasExisting := range replacedIDs {
		if wasExisting {
			replaced++
		}
	}
	keptUnchanged = len(existing) - replaced - skippedZeroModels

	if dryRun {
		return imported, keptUnchanged, skippedZeroModels, nil
	}

	// Nothing was imported — a typo'd --provider, a filter that matched
	// nothing, an empty response. Rewriting the file with exactly what was
	// read would be all risk and no benefit, so don't touch it at all.
	if len(imported) == 0 {
		return imported, keptUnchanged, skippedZeroModels, nil
	}

	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("encoding providers.json: %w", err)
	}
	if err := writeCatalogAtomically(ProvidersJSONPath(), out); err != nil {
		return nil, 0, 0, err
	}
	return imported, keptUnchanged, skippedZeroModels, nil
}

// writeCatalogAtomically replaces providers.json in one step.
//
// os.WriteFile opens with O_TRUNC, which destroys the old catalog the instant
// the file is opened and only then starts writing the new bytes. A Ctrl-C, a
// full disk or a sleeping laptop half way through leaves a truncated file —
// and this file holds the only copy of any hand-maintained provider (gateway
// prices models.dev does not carry). Worse, the merge path refuses to run
// against an unparsable catalog, so a half-write would leave refresh unable
// to repair the very thing it broke. internal/cron/store.go's Save takes the
// same precaution for the same reason.
func writeCatalogAtomically(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "providers.json.tmp*")
	if err != nil {
		return fmt.Errorf("writing providers.json: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing providers.json: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing providers.json: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("writing providers.json: %w", err)
	}
	return nil
}

// readExistingCatalog reads the cached providers.json, if any. A missing
// file is not an error — there is simply nothing to merge into — but a file
// that exists and fails to parse is: merging on top of "empty" in that case
// would silently overwrite whatever unreadable content was there, which is
// exactly the kind of data loss this function exists to avoid.
func readExistingCatalog() (map[string]ModelsDevProvider, error) {
	data, err := os.ReadFile(ProvidersJSONPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var existing map[string]ModelsDevProvider
	if err := json.Unmarshal(data, &existing); err != nil {
		return nil, err
	}
	return existing, nil
}

func parseFilter(providerFilter string) map[string]bool {
	if providerFilter == "" {
		return nil
	}
	filter := make(map[string]bool)
	for _, p := range strings.Split(providerFilter, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			filter[p] = true
		}
	}
	return filter
}

// SetHTTPClientForTests overrides the HTTP client that fetchModelsDev (and
// the rest of this package) uses, returning a func that restores the
// previous one. It exists so tests in other packages — internal/pricing in
// particular, which imports connect and therefore cannot be imported back
// by a connect test without a cycle — can fake the models.dev response
// without a network call.
func SetHTTPClientForTests(d HTTPDoer) (restore func()) {
	orig := defaultHTTPClient
	defaultHTTPClient = d
	return func() { defaultHTTPClient = orig }
}

// fetchModelsDev fetches the models.dev API.
func fetchModelsDev(doer HTTPDoer) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", modelsDevURL, err)
	}

	resp, err := doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", modelsDevURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	return body, nil
}

// writeModelJSON writes the config to model.json (used by `tyci connect`).
func writeModelJSON(path string, cfg map[string]map[string]uriEntry) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}
