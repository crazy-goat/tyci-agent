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
	_ = os.Setenv("TEST_FETCH_TOKEN", "sk-env-token")
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

// ─── AddProvider ──────────────────────────────────────────────────────────

func TestAddProvider_SavesAuthAndModelFile(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4"},{"id":"gpt-4o"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := AddProvider("test-provider", "openai", srv.URL, "sk-token", false, "")
	if err != nil {
		t.Fatalf("AddProvider() error: %v", err)
	}

	// Auth key saved to auth.json
	key, ok, err := GetKey("test-provider")
	if err != nil || !ok || key != "sk-token" {
		t.Errorf("expected key 'sk-token' in auth.json, got %q (ok=%v, err=%v)", key, ok, err)
	}

	// Models saved to model.json without token in URI
	data, err := os.ReadFile(filepath.Join(dir, ".tyci", "model.json"))
	if err != nil {
		t.Fatalf("read model.json: %v", err)
	}
	var cfg map[string]map[string]uriEntry
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal model.json: %v", err)
	}
	models := cfg["test-provider"]
	if models == nil || len(models) != 2 {
		t.Fatalf("expected 2 models, got %+v", models)
	}
	for name, entry := range models {
		if strings.Contains(entry.URI, "sk-token") {
			t.Errorf("model %q URI should not contain token: %q", name, entry.URI)
		}
		if !strings.HasPrefix(entry.URI, "openai://") {
			t.Errorf("model %q URI prefix wrong: %q", name, entry.URI)
		}
	}
}

func TestAddProvider_NoModelsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	err := AddProvider("test", "openai", srv.URL, "", false, "")
	if err == nil {
		t.Fatal("expected error for empty models list")
	}
	if !strings.Contains(err.Error(), "no models returned") {
		t.Errorf("expected 'no models returned', got %v", err)
	}
}

func TestAddProvider_EmptyName(t *testing.T) {
	err := AddProvider("", "openai", "http://localhost", "", false, "")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestAddProvider_EmptyURL(t *testing.T) {
	err := AddProvider("test", "openai", "", "", false, "")
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestAddProvider_ReplacesExistingPrefix(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	// Existing model.json with same provider, different api type
	existingCfg := map[string]map[string]uriEntry{
		"test-provider": {
			"gpt-4":  {URI: "openai://gpt-4@@old.api.com"},
			"claude": {URI: "anthropic://claude@@ant.api.com"},
		},
	}
	configDir := filepath.Join(dir, ".tyci")
	_ = os.MkdirAll(configDir, 0755)
	existingData, _ := json.MarshalIndent(existingCfg, "", "  ")
	_ = os.WriteFile(filepath.Join(configDir, "model.json"), existingData, 0644)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4"},{"id":"gpt-4o"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := AddProvider("test-provider", "openai", srv.URL, "new-token", false, ""); err != nil {
		t.Fatalf("AddProvider() error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(configDir, "model.json"))
	var cfg map[string]map[string]uriEntry
	_ = json.Unmarshal(data, &cfg)

	models := cfg["test-provider"]
	if models == nil {
		t.Fatal("test-provider should exist")
	}

	// gpt-4 should have new URI; claude (anthropic) should remain
	if !strings.HasPrefix(models["gpt-4"].URI, "openai://gpt-4@") {
		t.Errorf("gpt-4 URI should start with 'openai://gpt-4@', got %q", models["gpt-4"].URI)
	}
	if models["claude"].URI != "anthropic://claude@@ant.api.com" {
		t.Errorf("claude URI changed: %q", models["claude"].URI)
	}
}

func TestAddProvider_TokenEnvVarExpansion(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	_ = os.Setenv("TEST_MODEL_TOKEN", "sk-env-expanded")
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

	if err := AddProvider("test-provider", "openai", srv.URL, "$TEST_MODEL_TOKEN", false, ""); err != nil {
		t.Fatalf("AddProvider() error: %v", err)
	}

	// The env var should be resolved when fetching models (server gets expanded value)
	// and the resolved token should be stored in auth.json
	key, ok, _ := GetKey("test-provider")
	if !ok || key != "sk-env-expanded" {
		t.Errorf("expected resolved token 'sk-env-expanded' in auth.json, got %q", key)
	}
}
