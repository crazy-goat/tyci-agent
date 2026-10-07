package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// nexosModelsURL lists the nexos models with context_length and pricing.
const nexosModelsURL = "https://api.nexos.ai/v1/models"

// nexosCacheTTL is how long nexos_models.json is used before a refetch.
const nexosCacheTTL = 24 * time.Hour

// NexosModelsPath returns the path to the cached nexos model list. The file
// has the providers.json shape ({"nexos": {...}}) so internal/pricing can
// merge it into the catalog.
func NexosModelsPath() string {
	return filepath.Join(tyciDir(), "nexos_models.json")
}

// ParseNexosModels converts the body of GET /v1/models into a catalog
// provider. Nexos prices are USD per token (strings); the catalog uses USD
// per million tokens.
func ParseNexosModels(body []byte) (ModelsDevProvider, error) {
	var resp struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			MaxTokens     int    `json:"max_tokens"`
			Pricing       struct {
				Input      string `json:"input_cost_per_token"`
				Output     string `json:"output_cost_per_token"`
				CacheRead  string `json:"cache_read_cost_per_token"`
				CacheWrite string `json:"cache_write_cost_per_token"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ModelsDevProvider{}, fmt.Errorf("parsing nexos models: %w", err)
	}
	perM := func(s string) float64 {
		v, _ := strconv.ParseFloat(s, 64)
		return v * 1e6
	}
	p := ModelsDevProvider{ID: "nexos", Name: "nexos", Models: map[string]ModelsDevModel{}}
	for _, m := range resp.Data {
		if m.ID == "" {
			continue
		}
		p.Models[m.ID] = ModelsDevModel{
			ID:   m.ID,
			Name: m.Name,
			Cost: ModelsDevCost{
				Input:      perM(m.Pricing.Input),
				Output:     perM(m.Pricing.Output),
				CacheRead:  perM(m.Pricing.CacheRead),
				CacheWrite: perM(m.Pricing.CacheWrite),
			},
			Limit: ModelsDevLimit{Context: m.ContextLength, Output: m.MaxTokens},
		}
	}
	return p, nil
}

// RefreshNexosModels fetches the nexos model list and caches it, unless the
// cache is younger than 24 hours. apiKey "" means no nexos account: nothing
// happens. Errors are returned for the caller to ignore or report.
func RefreshNexosModels(apiKey string) error {
	if apiKey == "" {
		return nil
	}
	path := NexosModelsPath()
	if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < nexosCacheTTL {
		return nil
	}
	// A failed fetch leaves a marker, so it is retried at most once per TTL.
	attempt := path + ".attempt"
	if st, err := os.Stat(attempt); err == nil && time.Since(st.ModTime()) < nexosCacheTTL {
		return nil
	}
	err := fetchNexosModels(apiKey, path)
	if err != nil {
		_ = os.WriteFile(attempt, nil, 0o600)
	} else {
		_ = os.Remove(attempt)
	}
	return err
}

func fetchNexosModels(apiKey, path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nexosModelsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := getDefaultHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", nexosModelsURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching %s: %s", nexosModelsURL, resp.Status)
	}
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return fmt.Errorf("reading nexos models: %w", err)
	}
	p, err := ParseNexosModels(raw)
	if err != nil {
		return err
	}
	if len(p.Models) == 0 {
		return fmt.Errorf("nexos models: empty list")
	}
	out, err := json.Marshal(map[string]ModelsDevProvider{"nexos": p})
	if err != nil {
		return err
	}
	return writeCatalogAtomically(path, out)
}

// NexosAPIKey returns the nexos key from auth.json or NEXOS_API_KEY, or "".
func NexosAPIKey() string {
	if k, ok, err := GetKey("nexos"); err == nil && ok && k != "" {
		return ResolveToken(k)
	}
	return os.Getenv("NEXOS_API_KEY")
}
