package providers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci-agent/stream"
)

func TestBuildSystemPrompt_noAgentsMd(t *testing.T) {
	// No AGENTS.md file present — prompt should not contain the separator
	prompt := BuildSystemPrompt()
	if strings.Contains(prompt, "Additional instructions from AGENTS.md") {
		t.Errorf("expected no AGENTS.md content when file is missing, but found it")
	}
}

func TestBuildSystemPrompt_withAgentsMd(t *testing.T) {
	// Create temp dir with AGENTS.md
	dir := t.TempDir()
	content := "Use tabs for indentation.\nPrefer table-driven tests."
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Save original wd, change to temp dir
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	prompt := BuildSystemPrompt()
	if !strings.Contains(prompt, "Additional instructions from AGENTS.md") {
		t.Errorf("expected AGENTS.md content to be appended, but it's missing")
	}
	if !strings.Contains(prompt, "Use tabs for indentation.") {
		t.Errorf("expected AGENTS.md content to include file text")
	}
}

func TestBuildSystemPrompt_emptyAgentsMd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("   \n\n  "), 0644); err != nil {
		t.Fatal(err)
	}

	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	prompt := BuildSystemPrompt()
	if strings.Contains(prompt, "Additional instructions from AGENTS.md") {
		t.Errorf("expected no AGENTS.md content when file is empty/whitespace")
	}
}

func TestDynamicProviderIsConfigured_withAuthJSON(t *testing.T) {
	// Setup: create a temporary HOME with auth.json containing a key
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	// Write auth.json
	authDir := filepath.Join(dir, ".tyci")
	os.MkdirAll(authDir, 0755)
	authPath := filepath.Join(authDir, "auth.json")
	os.WriteFile(authPath, []byte(`{"test-provider":"sk-test-key-123"}`), 0600)

	// Create dynamicProvider with a URI that has no embedded token
	p := &dynamicProvider{
		name: "test-provider",
		entries: []ModelEntry{
			{
				Name: "gpt-4",
				URI:  "openai://gpt-4@api.openai.com", // no token in URI
			},
		},
	}

	if !p.IsConfigured() {
		t.Error("IsConfigured() should return true when auth.json has the key")
	}
}

func TestDynamicProviderIsConfigured_withURIOnly(t *testing.T) {
	p := &dynamicProvider{
		name: "test-provider",
		entries: []ModelEntry{
			{
				Name: "gpt-4",
				URI:  "openai://gpt-4@sk-uri-token@api.openai.com", // token in URI
			},
		},
	}

	if !p.IsConfigured() {
		t.Error("IsConfigured() should return true when URI has embedded token")
	}
}

func TestDynamicProviderIsConfigured_notConfigured(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	// Unset relevant env vars to ensure clean state
	os.Unsetenv(strings.ToUpper("test-provider") + "_API_KEY")
	os.Unsetenv("OPENCODE_API_KEY")

	// No auth.json, no URI token, no env vars
	p := &dynamicProvider{
		name: "test-provider",
		entries: []ModelEntry{
			{
				Name: "gpt-4",
				URI:  "openai://gpt-4@api.openai.com",
			},
		},
	}

	if p.IsConfigured() {
		t.Error("IsConfigured() should return false when no key is available")
	}
}

func TestDynamicProviderStream_usesAuthJSON(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	// Unset env vars to ensure we're testing auth.json path
	os.Unsetenv(strings.ToUpper("test-provider") + "_API_KEY")
	os.Unsetenv("OPENCODE_API_KEY")

	// Write auth.json with a key
	authDir := filepath.Join(dir, ".tyci")
	os.MkdirAll(authDir, 0755)
	authPath := filepath.Join(authDir, "auth.json")
	os.WriteFile(authPath, []byte(`{"test-provider":"sk-auth-key"}`), 0600)

	p := &dynamicProvider{
		name: "test-provider",
		entries: []ModelEntry{
			{
				Name: "gpt-4",
				URI:  "openai://gpt-4@api.openai.com",
			},
		},
	}

	// Stream() returns a channel immediately. Cancel context before API call
	// and check the channel for a non-"no API key" error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch, err := p.Stream(ctx, Request{Model: "gpt-4"})
	if err != nil {
		// If Stream returns an error directly, it should NOT be "no API key"
		if strings.Contains(err.Error(), "no API key") {
			t.Errorf("Stream() should not fail with 'no API key' when auth.json has a key: %v", err)
		}
		return
	}

	// Read from channel to see what happens
	for evt := range ch {
		if err, ok := evt.(stream.StreamError); ok {
			if strings.Contains(err.Err.Error(), "no API key") {
				t.Errorf("Stream() should not fail with 'no API key' when auth.json has a key: %v", err.Err)
			}
			// Any other error (connection refused, cancelled) is fine
			return
		}
	}
}

func TestDynamicProviderStream_authJSONFallbackToEnvVar(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	// No auth.json — key should come from env var
	// Provider name "test-provider" maps to "TEST-PROVIDER_API_KEY" env var
	os.Setenv("TEST-PROVIDER_API_KEY", "sk-env-key")
	t.Cleanup(func() { os.Unsetenv("TEST-PROVIDER_API_KEY") })
	os.Unsetenv("OPENCODE_API_KEY")

	p := &dynamicProvider{
		name: "test-provider",
		entries: []ModelEntry{
			{
				Name: "gpt-4",
				URI:  "openai://gpt-4@api.openai.com",
			},
		},
	}

	if !p.IsConfigured() {
		t.Error("IsConfigured() should return true when env var is set")
	}

	// Verify Stream uses env var key (not "no API key" error)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch, err := p.Stream(ctx, Request{Model: "gpt-4"})
	if err != nil {
		if strings.Contains(err.Error(), "no API key") {
			t.Errorf("Stream() should not fail with 'no API key' when env var has a key: %v", err)
		}
		return
	}

	for evt := range ch {
		if err, ok := evt.(stream.StreamError); ok {
			if strings.Contains(err.Err.Error(), "no API key") {
				t.Errorf("Stream() should not fail with 'no API key' when env var has a key: %v", err.Err)
			}
			return
		}
	}
}

func TestParseURI_withoutToken(t *testing.T) {
	// New format without token: model@host
	apiType, token, baseURL, endpointPath, err := parseURI("openai://gpt-4@api.openai.com")
	if err != nil {
		t.Fatalf("parseURI() error: %v", err)
	}
	if apiType != "openai" {
		t.Errorf("apiType = %q, want %q", apiType, "openai")
	}
	if token != "" {
		t.Errorf("token = %q, want empty string", token)
	}
	if baseURL != "https://api.openai.com" {
		t.Errorf("baseURL = %q, want %q", baseURL, "https://api.openai.com")
	}
	if endpointPath != "/v1/chat/completions" {
		t.Errorf("endpointPath = %q, want %q", endpointPath, "/v1/chat/completions")
	}
}

func TestParseURI_withToken(t *testing.T) {
	// Legacy format with token: model@token@host
	apiType, token, baseURL, endpointPath, err := parseURI("openai://gpt-4@sk-token@api.openai.com")
	if err != nil {
		t.Fatalf("parseURI() error: %v", err)
	}
	if apiType != "openai" {
		t.Errorf("apiType = %q, want %q", apiType, "openai")
	}
	if token != "sk-token" {
		t.Errorf("token = %q, want %q", token, "sk-token")
	}
	if baseURL != "https://api.openai.com" {
		t.Errorf("baseURL = %q, want %q", baseURL, "https://api.openai.com")
	}
	if endpointPath != "/v1/chat/completions" {
		t.Errorf("endpointPath = %q, want %q", endpointPath, "/v1/chat/completions")
	}
}

func TestParseURI_anthropic(t *testing.T) {
	apiType, token, baseURL, endpointPath, err := parseURI("anthropic://claude-sonnet-4@api.anthropic.com")
	if err != nil {
		t.Fatalf("parseURI() error: %v", err)
	}
	if apiType != "anthropic" {
		t.Errorf("apiType = %q, want %q", apiType, "anthropic")
	}
	if token != "" {
		t.Errorf("token = %q, want empty", token)
	}
	if baseURL != "https://api.anthropic.com" {
		t.Errorf("baseURL = %q, want %q", baseURL, "https://api.anthropic.com")
	}
	if endpointPath != "/v1/messages" {
		t.Errorf("endpointPath = %q, want %q", endpointPath, "/v1/messages")
	}
}

