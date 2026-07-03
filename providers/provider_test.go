package providers

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/stream"
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
	_ = os.Setenv("HOME", dir)
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })

	// Write auth.json
	authDir := filepath.Join(dir, ".tyci")
	_ = os.MkdirAll(authDir, 0755)
	authPath := filepath.Join(authDir, "auth.json")
	_ = os.WriteFile(authPath, []byte(`{"test-provider":"sk-test-key-123"}`), 0600)

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
	_ = os.Setenv("HOME", dir)
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })

	// Unset relevant env vars to ensure clean state
	_ = os.Unsetenv(strings.ToUpper("test-provider") + "_API_KEY")
	_ = os.Unsetenv("OPENCODE_API_KEY")

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
	_ = os.Setenv("HOME", dir)
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })

	// Unset env vars to ensure we're testing auth.json path
	_ = os.Unsetenv(strings.ToUpper("test-provider") + "_API_KEY")
	_ = os.Unsetenv("OPENCODE_API_KEY")

	// Write auth.json with a key
	authDir := filepath.Join(dir, ".tyci")
	_ = os.MkdirAll(authDir, 0755)
	authPath := filepath.Join(authDir, "auth.json")
	_ = os.WriteFile(authPath, []byte(`{"test-provider":"sk-auth-key"}`), 0600)

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
			// Any other error (connection refused, canceled) is fine
			return
		}
	}
}

func TestDynamicProviderStream_authJSONFallbackToEnvVar(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", dir)
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })

	// No auth.json — key should come from env var
	// Provider name "test-provider" maps to "TEST-PROVIDER_API_KEY" env var
	_ = os.Setenv("TEST-PROVIDER_API_KEY", "sk-env-key")
	t.Cleanup(func() { _ = os.Unsetenv("TEST-PROVIDER_API_KEY") })
	_ = os.Unsetenv("OPENCODE_API_KEY")

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

// =============================================================================
// Regression tests: $ENV_VAR references in auth.json
// =============================================================================
//
// Bug: `provider auth set nexos '$NEXOS_API_KEY'` (single-quoted in shell)
// stored the literal "$NEXOS_API_KEY" in auth.json. At request time, only
// the URI token was treated as a $VAR reference, so the literal was sent
// as the bearer token => API returned 401 => fallback model triggered.
//
// The fixes:
//  1. `provider auth set` now resolves "$VAR" before saving.
//  2. `dynamicProvider.Stream()` runs `connect.ResolveToken` on the key it
//     reads from auth.json too, so existing literal entries self-heal.
//  3. `IsConfigured()` likewise skips the literal fallback — it asks
//     "can I actually authenticate?" rather than "is anything non-empty
//     in auth.json?".

// Regression: literal "$VAR" in auth.json + env set -> IsConfigured reports true.
func TestDynamicProviderIsConfigured_authJSONLiteralEnvRef_setInEnv(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", dir)
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })
	_ = os.Unsetenv("NEXOS_API_KEY")
	const envName = "TYCI_TEST_LITERAL_ENVREF"
	t.Setenv(envName, "real-nexos-key-from-env")
	_ = os.Unsetenv("OPENCODE_API_KEY")

	authDir := filepath.Join(dir, ".tyci")
	_ = os.MkdirAll(authDir, 0755)
	authPath := filepath.Join(authDir, "auth.json")
	// Literal "$VAR" — the bug scenario.
	if err := os.WriteFile(authPath, []byte(`{"nexos":"$`+envName+`"}`), 0600); err != nil {
		t.Fatal(err)
	}

	p := &dynamicProvider{
		name: "nexos",
		entries: []ModelEntry{
			{Name: "MiniMax M3", URI: "openai://MiniMax M3@@api.nexos.ai"},
		},
	}
	if !p.IsConfigured() {
		t.Error("IsConfigured() returned false; should be true because $VAR resolves via env")
	}
}

// Regression: literal "$VAR" in auth.json + env unset -> IsConfigured reports false.
func TestDynamicProviderIsConfigured_authJSONLiteralEnvRef_unsetInEnv(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", dir)
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })
	const envName = "TYCI_TEST_UNSET_LITERAL_ENVREF"
	_ = os.Unsetenv(envName)
	_ = os.Unsetenv("OPENCODE_API_KEY")

	authDir := filepath.Join(dir, ".tyci")
	_ = os.MkdirAll(authDir, 0755)
	authPath := filepath.Join(authDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"nexos":"$`+envName+`"}`), 0600); err != nil {
		t.Fatal(err)
	}

	p := &dynamicProvider{
		name: "nexos",
		entries: []ModelEntry{
			{Name: "MiniMax M3", URI: "openai://MiniMax M3@@api.nexos.ai"},
		},
	}
	if p.IsConfigured() {
		t.Error("IsConfigured() returned true; should be false because $VAR does not resolve")
	}
}

// Regression: literal "$VAR" in auth.json + env set -> Stream resolves it
// (does not return "no API key") and sends the real value as bearer.
func TestDynamicProviderStream_authJSONLiteralEnvRef_resolvesAtRuntime(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", dir)
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })
	_ = os.Unsetenv("NEXOS_API_KEY")
	const envName = "TYCI_TEST_LITERAL_ENVREF_STREAM"
	const realKey = "nexos-real-key-9876"
	t.Setenv(envName, realKey)
	_ = os.Unsetenv("OPENCODE_API_KEY")

	// Capture the Authorization header the provider sends by routing every
	// request through a recording transport.
	captured := make(chan string, 1)
	tr := &recordingTransport{captured: captured, inner: &http.Transport{}}
	customClient := &http.Client{Transport: tr}
	ctx := context.WithValue(context.Background(), api.HTTPClientKey{}, customClient)

	// Verify the auth.json path that connect.AuthPath() resolves to.
	authDir := filepath.Join(dir, ".tyci")
	authPath := filepath.Join(authDir, "auth.json")
	if err := os.MkdirAll(authDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"nexos":"$`+envName+`"}`), 0600); err != nil {
		t.Fatal(err)
	}

	p := &dynamicProvider{
		name: "nexos",
		entries: []ModelEntry{
			// We use a non-routable host so the request will fail — we only
			// care that the bearer token MIME-match the expected resolved value.
			// dynProvider sends the request from a goroutine, then closes the
			// channel; the goroutine will hit a connect error which surfaces
			// as a stream.StreamError event.
			{Name: "MiniMax M3", URI: "openai://MiniMax M3@@127.0.0.1:1"},
		},
	}

	ch, err := p.Stream(ctx, Request{Model: "MiniMax M3"})
	if err != nil {
		t.Fatalf("Stream() returned error directly: %v", err)
	}

	// Drain events; capture the auth header from the recording transport.
	var apiKeyErr bool
	for evt := range ch {
		if se, ok := evt.(stream.StreamError); ok {
			if strings.Contains(se.Err.Error(), "no API key") {
				apiKeyErr = true
			}
		}
	}

	if apiKeyErr {
		t.Error("Stream() returned 'no API key' error: $VAR literal in auth.json was not resolved")
	}

	select {
	case got := <-captured:
		want := "Bearer " + realKey
		if got != want {
			t.Errorf("Authorization header = %q, want %q (literal $VAR was sent as bearer token)", got, want)
		}
		if strings.Contains(got, "$"+envName) {
			t.Errorf("Authorization header still contains unresolved literal $%s: %q", envName, got)
		}
	default:
		t.Error("recordingTransport saw no request — Stream() short-circuited before hitting the API layer")
	}
}

// recordingTransport is a tiny http.RoundTripper stub that captures the
// Authorization header of the first request and returns a fake response so
// the goroutine in dynamicProvider.Stream can finish without panicking.
type recordingTransport struct {
	captured chan string
	inner    http.RoundTripper
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case r.captured <- req.Header.Get("Authorization"):
	default:
	}
	// Return a dummy 401 body so the chat client gives up cleanly. We are not
	// asserting on response handling here — only that the bearer token the
	// provider *would* have sent is correct.
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"unauthorized"}}`)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}
