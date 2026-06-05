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
)

type uriEntry struct {
	URI string `json:"uri"`
}

func Run(name, apiType, baseURL, token string) error {
	if name == "" {
		return fmt.Errorf("provider name is required")
	}
	if apiType == "" {
		return fmt.Errorf("API type is required")
	}
	if baseURL == "" {
		return fmt.Errorf("URL is required")
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	host := u.Host + strings.TrimRight(u.Path, "/")

	modelIDs, err := fetchOpenAIModels(baseURL, token)
	if err != nil {
		return fmt.Errorf("fetching models: %w", err)
	}
	if len(modelIDs) == 0 {
		return fmt.Errorf("no models returned")
	}

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

	for _, m := range modelIDs {
		tok := token
		uri := fmt.Sprintf("%s://%s@%s@%s", apiType, m, tok, host)
		models[m] = uriEntry{URI: uri}
	}

	cfg[name] = models

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Saved %d models for provider %q to %s\n", len(modelIDs), name, configPath)
	return nil
}

func fetchOpenAIModels(baseURL, token string) ([]string, error) {
	client := &http.Client{}

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

		resp, err := client.Do(req)
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
		if resp.StatusCode == http.StatusNotFound {
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil, fmt.Errorf("no models endpoint found at %s", baseURL)
}
