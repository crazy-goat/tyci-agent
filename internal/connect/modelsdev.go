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
type ModelsDevModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
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
	Name   string
	Type   string
	Models int
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
	body, err := fetchModelsDev()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	if err := os.WriteFile(path, body, 0644); err != nil {
		return fmt.Errorf("writing providers.json: %w", err)
	}
	return nil
}

// RefreshModels fetches models from models.dev and overwrites the cached catalog.
// providerFilter is an optional comma-separated list of provider IDs to keep;
// if non-empty, providers outside the filter are dropped before writing.
// dryRun if true, only reports what would be imported without writing.
func RefreshModels(providerFilter string, dryRun bool) ([]RefreshProvider, error) {
	body, err := fetchModelsDev()
	if err != nil {
		return nil, fmt.Errorf("fetching models.dev: %w", err)
	}

	var all map[string]ModelsDevProvider
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, fmt.Errorf("parsing models.dev: %w", err)
	}

	filter := parseFilter(providerFilter)

	kept := make(map[string]ModelsDevProvider, len(all))
	var imported []RefreshProvider
	for id, p := range all {
		if filter != nil && !filter[id] {
			continue
		}
		if _, ok := npmToAPIType[p.NPM]; !ok {
			continue
		}
		kept[id] = p
		imported = append(imported, RefreshProvider{
			Name:   p.Name,
			Type:   npmToAPIType[p.NPM],
			Models: len(p.Models),
		})
	}

	if dryRun {
		return imported, nil
	}

	out, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding providers.json: %w", err)
	}
	path := ProvidersJSONPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("creating config dir: %w", err)
	}
	if err := os.WriteFile(path, out, 0644); err != nil {
		return nil, fmt.Errorf("writing providers.json: %w", err)
	}
	return imported, nil
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

// fetchModelsDev fetches the models.dev API.
func fetchModelsDev() ([]byte, error) {
	client := &http.Client{}

	resp, err := client.Get(modelsDevURL)
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
