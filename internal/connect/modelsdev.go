package connect

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/decodo/tyci-agent/internal/tyciconfig"
)

// ModelsDevProvider represents a provider from models.dev API.
type ModelsDevProvider struct {
	ID     string                         `json:"id"`
	Env    []string                       `json:"env"`
	NPM    string                         `json:"npm"`
	Name   string                         `json:"name"`
	API    string                         `json:"api"`
	Models map[string]ModelsDevModel      `json:"models"`
}

// ModelsDevModel represents a model from models.dev API.
type ModelsDevModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// modelsDevURLs defines fallback URLs for models.dev API.
var modelsDevURLs = []string{
	"https://models.dev/api.json",
	"https://raw.githubusercontent.com/sst/models.dev/main/api.json",
	"https://raw.githubusercontent.com/anomalyco/models.dev/main/api.json",
}

// npmToAPIType maps npm package names to tyci API types.
var npmToAPIType = map[string]string{
	"@ai-sdk/openai":           "openai",
	"@ai-sdk/anthropic":        "anthropic",
	"@ai-sdk/gemini":           "gemini",
	"@ai-sdk/openai-compatible": "openai",
}

// npmToHost maps npm package names to default API hosts.
var npmToHost = map[string]string{
	"@ai-sdk/openai":    "api.openai.com",
	"@ai-sdk/anthropic": "api.anthropic.com",
	"@ai-sdk/gemini":    "generativelanguage.googleapis.com",
}

// RefreshProvider holds the result of importing a provider.
type RefreshProvider struct {
	Name   string
	Type   string
	Models int
}

// RefreshModels fetches models from models.dev and imports them.
// providerFilter is an optional comma-separated list of provider IDs to import.
// dryRun if true, only prints what would be imported without writing.
func RefreshModels(providerFilter string, dryRun bool) ([]RefreshProvider, error) {
	// Parse filter
	var filter map[string]bool
	if providerFilter != "" {
		filter = make(map[string]bool)
		for _, p := range strings.Split(providerFilter, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				filter[p] = true
			}
		}
	}

	// Fetch from models.dev
	providers, err := fetchModelsDev()
	if err != nil {
		return nil, fmt.Errorf("fetching models.dev: %w", err)
	}

	// Load existing config
	configPath := ModelJSONPath()
	cfg := make(map[string]map[string]uriEntry)
	if data, err := os.ReadFile(configPath); err == nil {
		json.Unmarshal(data, &cfg)
	}
	if cfg == nil {
		cfg = make(map[string]map[string]uriEntry)
	}

	var imported []RefreshProvider

	// Sort provider IDs for deterministic output
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		p := providers[id]

		// Skip if filter specified and not in filter
		if filter != nil && !filter[id] {
			continue
		}

		// Map npm package to API type
		apiType, ok := npmToAPIType[p.NPM]
		if !ok {
			// Skip unknown API types
			continue
		}

		// Extract host from API URL or use default
		host := ""
		if p.API != "" {
			host = extractHost(p.API)
		} else {
			host = npmToHost[p.NPM]
		}
		if host == "" {
			continue
		}

		// Build model entries
		models := cfg[id]
		if models == nil {
			models = make(map[string]uriEntry)
		}

		// Remove existing entries for this provider (they'll be replaced)
		for key := range models {
			delete(models, key)
		}

		// Sort model IDs for deterministic output
		modelIDs := make([]string, 0, len(p.Models))
		for mid := range p.Models {
			modelIDs = append(modelIDs, mid)
		}
		sort.Strings(modelIDs)

		for _, mid := range modelIDs {
			uri := tyciconfig.ProviderURI{
				APIType:   apiType,
				Model:     mid,
				AuthToken: "", // No token - stored in auth.json
				Host:      host,
			}
			models[mid] = uriEntry{URI: uri.String()}
		}

		if len(models) > 0 {
			cfg[id] = models
			imported = append(imported, RefreshProvider{
				Name:   p.Name,
				Type:   apiType,
				Models: len(models),
			})
		}
	}

	if dryRun {
		return imported, nil
	}

	// Write config
	if err := writeModelJSON(configPath, cfg); err != nil {
		return nil, err
	}

	return imported, nil
}

// fetchModelsDev fetches the models.dev API with fallback URLs.
func fetchModelsDev() (map[string]ModelsDevProvider, error) {
	client := &http.Client{}

	for _, url := range modelsDevURLs {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			continue
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			continue
		}

		var providers map[string]ModelsDevProvider
		if err := json.Unmarshal(body, &providers); err != nil {
			continue
		}

		return providers, nil
	}

	return nil, fmt.Errorf("failed to fetch from all models.dev URLs")
}

// extractHost extracts the host from a URL string.
func extractHost(apiURL string) string {
	// Remove protocol
	host := apiURL
	if idx := strings.Index(host, "://"); idx >= 0 {
		host = host[idx+3:]
	}
	// Remove path
	if idx := strings.Index(host, "/"); idx >= 0 {
		host = host[:idx]
	}
	// Remove port if present (for cleaner URIs)
	// Keep as-is for port-based services
	return host
}

// ModelJSONPath returns the path to model.json.
func ModelJSONPath() string {
	return filepath.Join(os.Getenv("HOME"), ".tyci", "model.json")
}

// writeModelJSON writes the config to model.json.
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
