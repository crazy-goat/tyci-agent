package connect

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── fetchOpenAIModels ──────────────────────────────────────────────────

func TestFetchOpenAIModels_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4"},{"id":"gpt-4o"},{"id":"gpt-3.5-turbo"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	models, err := fetchOpenAIModels(srv.URL, "")
	if err != nil {
		t.Fatalf("fetchOpenAIModels() error: %v", err)
	}

	expected := []string{"gpt-4", "gpt-4o", "gpt-3.5-turbo"}
	if len(models) != len(expected) {
		t.Fatalf("got %d models, want %d: %v", len(models), len(expected), models)
	}
	for i, m := range models {
		if m != expected[i] {
			t.Errorf("models[%d] = %q, want %q", i, m, expected[i])
		}
	}
}

func TestFetchOpenAIModels_FallbackToModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"custom-model"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	models, err := fetchOpenAIModels(srv.URL, "")
	if err != nil {
		t.Fatalf("fetchOpenAIModels() error: %v", err)
	}
	if len(models) != 1 || models[0] != "custom-model" {
		t.Fatalf("expected [custom-model], got %v", models)
	}
}

func TestFetchOpenAIModels_AllPathsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchOpenAIModels(srv.URL, "")
	if err == nil {
		t.Fatal("expected error when all paths return 404")
	}
	if !strings.Contains(err.Error(), "no models endpoint found") {
		t.Errorf("expected 'no models endpoint found', got %v", err)
	}
}

func TestFetchOpenAIModels_WithAuthHeader(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchOpenAIModels(srv.URL, "sk-test-token")
	if err != nil {
		t.Fatalf("fetchOpenAIModels() error: %v", err)
	}
	if receivedAuth != "Bearer sk-test-token" {
		t.Errorf("expected 'Bearer sk-test-token', got %q", receivedAuth)
	}
}

func TestFetchOpenAIModels_EnvVarToken(t *testing.T) {
	os.Setenv("TEST_FETCH_TOKEN", "sk-env-token")
	t.Cleanup(func() { _ = os.Unsetenv("TEST_FETCH_TOKEN") })

	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchOpenAIModels(srv.URL, "$TEST_FETCH_TOKEN")
	if err != nil {
		t.Fatalf("fetchOpenAIModels() error: %v", err)
	}
	if receivedAuth != "Bearer sk-env-token" {
		t.Errorf("expected 'Bearer sk-env-token', got %q", receivedAuth)
	}
}

func TestFetchOpenAIModels_NonJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	_, err := fetchOpenAIModels(srv.URL, "")
	if err == nil {
		t.Fatal("expected error for non-JSON response")
	}
}

// ─── Run (config merging) ───────────────────────────────────────────────

func TestRun_CreatesModelFile(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	// Start a test server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4"},{"id":"gpt-4o"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := Run("test-provider", "openai", srv.URL, "sk-token")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify model.json was created
	configPath := filepath.Join(dir, ".tyci", "model.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read model.json: %v", err)
	}

	var cfg map[string]map[string]uriEntry
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal model.json: %v", err)
	}

	if len(cfg) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(cfg))
	}

	models := cfg["test-provider"]
	if models == nil {
		t.Fatal("expected test-provider in config")
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(models), models)
	}

	// Check URI format: openai://gpt-4@sk-token@<host>
	for name, entry := range models {
		if !strings.HasPrefix(entry.URI, "openai://") {
			t.Errorf("model %q URI prefix wrong: %q", name, entry.URI)
		}
		if !strings.Contains(entry.URI, "sk-token") {
			t.Errorf("model %q URI missing token: %q", name, entry.URI)
		}
	}
}

func TestRun_MergesWithExistingConfig(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	// Create an existing model.json with a different provider
	existingCfg := map[string]map[string]uriEntry{
		"existing-provider": {
			"existing-model": {URI: "openai://existing-model@existing.com"},
		},
	}
	configDir := filepath.Join(dir, ".tyci")
	_ = os.MkdirAll(configDir, 0755)
	existingData, _ := json.MarshalIndent(existingCfg, "", "  ")
	_ = os.WriteFile(filepath.Join(configDir, "model.json"), existingData, 0644)

	// Now run connect for a different provider
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"new-model"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := Run("new-provider", "openai", srv.URL, "sk-token")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify both providers exist
	data, err := os.ReadFile(filepath.Join(configDir, "model.json"))
	if err != nil {
		t.Fatalf("read model.json: %v", err)
	}
	var cfg map[string]map[string]uriEntry
	_ = json.Unmarshal(data, &cfg)

	if len(cfg) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(cfg))
	}
	if _, ok := cfg["existing-provider"]; !ok {
		t.Error("existing-provider should remain in config")
	}
	if _, ok := cfg["new-provider"]; !ok {
		t.Error("new-provider should be added to config")
	}
}

func TestRun_ReplaceExistingPrefix(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	// Create existing config for the same provider with different api type
	existingCfg := map[string]map[string]uriEntry{
		"test-provider": {
			"gpt-4": {URI: "openai://gpt-4@old-token@old.api.com"},
			"claude": {URI: "anthropic://claude@old-key@ant.api.com"},
		},
	}
	configDir := filepath.Join(dir, ".tyci")
	_ = os.MkdirAll(configDir, 0755)
	existingData, _ := json.MarshalIndent(existingCfg, "", "  ")
	_ = os.WriteFile(filepath.Join(configDir, "model.json"), existingData, 0644)

	// Run with openai type - should remove existing openai:// entries but keep anthropic
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4"},{"id":"gpt-4o"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := Run("test-provider", "openai", srv.URL, "new-token")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "model.json"))
	if err != nil {
		t.Fatalf("read model.json: %v", err)
	}
	var cfg map[string]map[string]uriEntry
	_ = json.Unmarshal(data, &cfg)

	models := cfg["test-provider"]
	if models == nil {
		t.Fatal("test-provider should exist")
	}

	// gpt-4 should now have the new URI (openai prefix was replaced)
	if !strings.HasPrefix(models["gpt-4"].URI, "openai://gpt-4@new-token@") {
		t.Errorf("gpt-4 URI should start with 'openai://gpt-4@new-token@', got %q", models["gpt-4"].URI)
	}

	// claude (anthropic) should remain unchanged
	if models["claude"].URI != "anthropic://claude@old-key@ant.api.com" {
		t.Errorf("claude URI changed: %q", models["claude"].URI)
	}
}

func TestRun_NoModelsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	err := Run("test", "openai", srv.URL, "")
	if err == nil {
		t.Fatal("expected error for empty models list")
	}
	if !strings.Contains(err.Error(), "no models returned") {
		t.Errorf("expected 'no models returned', got %v", err)
	}
}

func TestRun_InvalidURL(t *testing.T) {
	err := Run("test", "openai", "://invalid", "")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestRun_EmptyName(t *testing.T) {
	err := Run("", "openai", "http://localhost", "")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestRun_EmptyAPI(t *testing.T) {
	err := Run("test", "", "http://localhost", "")
	if err == nil {
		t.Fatal("expected error for empty API type")
	}
}

func TestRun_EmptyURL(t *testing.T) {
	err := Run("test", "openai", "", "")
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestRun_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server error"}`))
	}))
	defer srv.Close()

	err := Run("test", "openai", srv.URL, "")
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

func TestRun_TokenEnvVarExpansion(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	os.Setenv("TEST_MODEL_TOKEN", "sk-env-expanded")
	t.Cleanup(func() { _ = os.Unsetenv("TEST_MODEL_TOKEN") })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := Run("test-provider", "openai", srv.URL, "$TEST_MODEL_TOKEN")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify the URI contains the expanded token
	configPath := filepath.Join(dir, ".tyci", "model.json")
	data, _ := os.ReadFile(configPath)
	content := string(data)

	if !strings.Contains(content, "$TEST_MODEL_TOKEN") {
		t.Error("URI should contain the env var reference $TEST_MODEL_TOKEN, not the expanded value")
	}
}
