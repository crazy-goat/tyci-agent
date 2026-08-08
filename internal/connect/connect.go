package connect

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/decodo/tyci/internal/tyciconfig"
)

type uriEntry struct {
	URI string `json:"uri"`
}

// HTTPDoer is the HTTP surface this package needs. It mirrors api.HTTPDoer
// and internal/mcp's injected client; declared locally so connect keeps its
// dependency-free position at the bottom of the import graph.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// defaultHTTPClient is the client the CLI entry points hand to the fetchers
// when the caller has nothing better. One shared client — and therefore one
// connection pool — instead of a fresh &http.Client{} (with its own
// Transport) constructed inside every function. Deliberately without a
// Timeout, which is what the replaced literals had.
var defaultHTTPClient HTTPDoer = &http.Client{}

// AddProvider adds a provider with auth separation:
// 1. Fetches models from the API
// 2. Saves the key to auth.json (not in URIs)
// 3. Writes models to model.json without tokens
// 4. Optionally tests connectivity
func AddProvider(name, apiType, baseURL, token string, test bool, testModel string) error {
	if name == "" {
		return fmt.Errorf("provider name is required")
	}
	if apiType == "" {
		apiType = "openai"
	}
	if baseURL == "" {
		return fmt.Errorf("URL is required")
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	host := u.Host

	// Resolve token (handle $ENV_VAR references)
	resolvedToken := ResolveToken(token)

	// Fetch models
	modelIDs, err := fetchOpenAIModels(defaultHTTPClient, baseURL, resolvedToken)
	if err != nil {
		return fmt.Errorf("fetching models: %w", err)
	}
	if len(modelIDs) == 0 {
		return fmt.Errorf("no models returned")
	}

	// Save key to auth.json (if token provided)
	if resolvedToken != "" {
		if err := SetKey(name, resolvedToken); err != nil {
			return fmt.Errorf("saving API key: %w", err)
		}
		fmt.Fprintf(os.Stdout, "\u2713 Saved API key to %s\n", AuthPath())
	}

	// Write models to model.json (without tokens in URIs)
	configDir := filepath.Join(os.Getenv("HOME"), ".tyci")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	configPath := filepath.Join(configDir, "model.json")

	cfg := make(map[string]map[string]uriEntry)
	if data, err := os.ReadFile(configPath); err == nil {
		json.Unmarshal(data, &cfg)
	}
	if cfg == nil {
		cfg = make(map[string]map[string]uriEntry)
	}

	// Remove existing models for this provider
	prefix := apiType + "://"
	models := cfg[name]
	if models == nil {
		models = make(map[string]uriEntry)
	}
	for key, entry := range models {
		if strings.HasPrefix(entry.URI, prefix) {
			delete(models, key)
		}
	}

	// Add new models WITHOUT tokens in URIs
	for _, m := range modelIDs {
		uri := tyciconfig.ProviderURI{
			APIType:   apiType,
			Model:     m,
			AuthToken: "", // No token in URI - stored in auth.json
			Host:      host,
		}
		models[m] = uriEntry{URI: uri.String()}
	}

	cfg[name] = models

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	fmt.Fprintf(os.Stdout, "\u2713 Fetched %d models from %s\n", len(modelIDs), name)
	fmt.Fprintf(os.Stdout, "\u2713 Wrote %d models to %s\n", len(modelIDs), configPath)

	// Optional connectivity test
	if test {
		modelName := testModel
		if modelName == "" && len(modelIDs) > 0 {
			modelName = modelIDs[0]
		}
		if modelName != "" {
			if err := testConnectivity(defaultHTTPClient, baseURL, resolvedToken, modelName); err != nil {
				fmt.Fprintf(os.Stdout, "\u26a0\ufe0f Connectivity test failed: %v\n", err)
			} else {
				fmt.Fprintf(os.Stdout, "\u2713 Connectivity check passed (%s returned 200)\n", modelName)
			}
		}
	}

	return nil
}

// testConnectivity makes a lightweight API call to verify the endpoint works
func testConnectivity(doer HTTPDoer, baseURL, token, model string) error {
	// Try OpenAI-compatible chat completions endpoint
	for _, path := range []string{"/v1/chat/completions", "/chat/completions"} {
		payload := map[string]any{
			"model":      model,
			"messages":   []map[string]string{{"role": "user", "content": "hi"}},
			"max_tokens": 1,
			"stream":     false,
		}
		body, _ := json.Marshal(payload)

		req, err := http.NewRequest("POST", baseURL+path, strings.NewReader(string(body)))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := doer.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return nil
		}
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
			continue
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return fmt.Errorf("no working endpoint found at %s", baseURL)
}

func fetchOpenAIModels(doer HTTPDoer, baseURL, token string) ([]string, error) {
	for _, path := range []string{"/models", "/v1/models"} {
		req, err := http.NewRequest("GET", baseURL+path, nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			key := token
			if strings.HasPrefix(key, "$") {
				key = os.Getenv(strings.TrimPrefix(key, "$"))
			}
			req.Header.Set("Authorization", "Bearer "+key)
		}

		resp, err := doer.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return nil, err
			}
			var result struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				return nil, err
			}
			ids := make([]string, len(result.Data))
			for i, m := range result.Data {
				ids[i] = m.ID
			}
			return ids, nil
		}
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil, fmt.Errorf("no models endpoint found at %s", baseURL)
}
