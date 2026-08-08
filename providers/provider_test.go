package providers

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/connector"
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
	ctx := context.Background()

	// Verify the auth.json path that connect.AuthPath() resolves to.
	authDir := filepath.Join(dir, ".tyci")
	authPath := filepath.Join(authDir, "auth.json")
	if err := os.MkdirAll(authDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"nexos":"$`+envName+`"}`), 0600); err != nil {
		t.Fatal(err)
	}

	// The recording client is injected through the provider's own Deps.HTTP,
	// which is the only injection path now that api.HTTPClientKey is gone.
	p := NewProvider("nexos", []ModelEntry{
		// We use a non-routable host so the request will fail — we only
		// care that the bearer token MIME-match the expected resolved value.
		// dynProvider sends the request from a goroutine, then closes the
		// channel; the goroutine will hit a connect error which surfaces
		// as a stream.StreamError event.
		{Name: "MiniMax M3", URI: "openai://MiniMax M3@@127.0.0.1:1"},
	}, Deps{HTTP: customClient})

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

// =============================================================================
// kindFor — build-tag exclusions must not fall back to another protocol
// =============================================================================

// A binary built with -tags noanthropic has no anthropic connector, so
// DefaultRegistry() ends up holding only openai (+gemini). kindFor must then
// fail loudly instead of routing the request through the OpenAI connector,
// which would POST an Anthropic-shaped body to /v1/messages and get a
// confusing 400 back. The registry is injected here so the test runs in the
// default (untagged) build too.
func TestDynamicProviderKindFor_excludedConnectorErrors(t *testing.T) {
	onlyOpenAI := connector.NewRegistry()
	onlyOpenAI.Register(connector.KindOpenAI, connector.NewOpenAI)

	p := &dynamicProvider{
		name:     "kindfor-excluded",
		entries:  []ModelEntry{{Name: "claude-x", URI: "anthropic://claude-x@sk-tok@api.example.invalid"}},
		registry: onlyOpenAI,
	}

	if _, err := p.kindFor(connector.KindAnthropic); err == nil {
		t.Fatal("kindFor returned no error for a connector missing from the registry")
	} else if got, want := err.Error(), "anthropic support excluded at build time (rebuild without -tags noanthropic)"; got != want {
		t.Fatalf("kindFor error = %q, want %q", got, want)
	}

	// The same must hold end to end: Stream may not reach the network.
	if _, err := p.Stream(context.Background(), Request{Model: "claude-x"}); err == nil {
		t.Fatal("Stream returned no error for an excluded connector kind")
	} else if !strings.Contains(err.Error(), "excluded at build time") {
		t.Fatalf("Stream error = %q, want the build-time exclusion message", err.Error())
	}
}

// Every connector actually compiled into this binary still resolves to itself
// — the guard above must not turn into "everything errors". Driven off the
// default registry rather than a literal list, so it holds under
// -tags noanthropic/nogemini as well.
func TestDynamicProviderKindFor_registeredKindPassesThrough(t *testing.T) {
	p := &dynamicProvider{name: "kindfor-ok"}
	kinds := defaultConnectors.Kinds()
	if len(kinds) == 0 {
		t.Fatal("default registry is empty")
	}
	for _, kind := range kinds {
		got, err := p.kindFor(kind)
		if err != nil {
			t.Fatalf("kindFor(%q): %v", kind, err)
		}
		if got != kind {
			t.Errorf("kindFor(%q) = %q, want %q", kind, got, kind)
		}
	}
}

// An api_type that is not a protocol we implement must error too, never
// silently become openai. In practice tyciconfig.Parse already normalizes any
// unrecognised URI scheme to "openai" (see TestParseURI_table), so this branch
// is unreachable through parseURI — it guards direct callers.
func TestDynamicProviderKindFor_unknownKindErrors(t *testing.T) {
	p := &dynamicProvider{name: "kindfor-unknown"}
	if _, err := p.kindFor("cohere"); err == nil {
		t.Fatal("kindFor returned no error for an unimplemented api_type")
	} else if !strings.Contains(err.Error(), `unsupported api_type "cohere"`) {
		t.Fatalf("kindFor error = %q, want unsupported api_type", err.Error())
	}
}

// =============================================================================
// Injected dependencies: the provider carries them, it does not fetch globals
// =============================================================================

// capturingFactory records the Endpoint it was handed and returns a connector
// that does nothing. It is what proves an injected dependency actually travels
// from Deps all the way to the connector.
type capturingConnector struct{ ep connector.Endpoint }

func (c *capturingConnector) Kind() string { return connector.KindOpenAI }
func (c *capturingConnector) Stream(context.Context, Request, func(stream.Event) error) error {
	return nil
}

func capturingRegistry(kind string) (*connector.Registry, *connector.Endpoint) {
	var seen connector.Endpoint
	r := connector.NewRegistry()
	r.Register(kind, func(ep connector.Endpoint) (connector.Connector, error) {
		seen = ep
		return &capturingConnector{ep: ep}, nil
	})
	return r, &seen
}

// Deps.HTTP must land in connector.Endpoint.HTTP — that is the whole point of
// the provider becoming an explicitly constructed struct.
func TestNewProvider_InjectsHTTPIntoEndpoint(t *testing.T) {
	reg, seen := capturingRegistry(connector.KindOpenAI)
	doer := &stubDoer{}

	p := NewProvider("injected-http", []ModelEntry{
		{Name: "m", URI: "openai://m@sk-tok@api.example.invalid"},
	}, Deps{Connectors: reg, HTTP: doer})

	ch, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}

	if seen.HTTP == nil {
		t.Fatal("Endpoint.HTTP is nil — the provider's HTTP client never reached the connector")
	}
	if seen.HTTP != connector.HTTPDoer(doer) {
		t.Errorf("Endpoint.HTTP = %v, want the injected doer", seen.HTTP)
	}
	// The rest of the endpoint is still resolved from the URI.
	if seen.APIKey != "sk-tok" {
		t.Errorf("Endpoint.APIKey = %q, want %q", seen.APIKey, "sk-tok")
	}
	if got := seen.URL(); got != "https://api.example.invalid/v1/chat/completions" {
		t.Errorf("Endpoint.URL() = %q", got)
	}
}

// A provider built without an HTTP client leaves Endpoint.HTTP nil, which is
// how the api layer's shared default client stays the production path.
func TestNewProvider_NilHTTPStaysNil(t *testing.T) {
	reg, seen := capturingRegistry(connector.KindOpenAI)

	p := NewProvider("no-http", []ModelEntry{
		{Name: "m", URI: "openai://m@sk-tok@api.example.invalid"},
	}, Deps{Connectors: reg})

	ch, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	if seen.HTTP != nil {
		t.Errorf("Endpoint.HTTP = %v, want nil", seen.HTTP)
	}
}

// A zero Deps must reproduce the production defaults rather than leaving nil
// fields behind — the registration path (RegisterProvidersFromConfig) relies
// on it.
func TestNewProvider_ZeroDepsGetsDefaults(t *testing.T) {
	p := newDynamicProvider("defaults", nil, Deps{})
	if p.registry != defaultConnectors {
		t.Error("zero Deps did not get the default connector registry")
	}
	if p.auth == nil {
		t.Error("zero Deps did not get the default AuthSource")
	}
	if p.http != nil {
		t.Error("zero Deps invented an HTTP client; nil is the meaningful default")
	}
}

// WithHTTP must return a COPY. Parallel subagents share one provider value, so
// mutating the receiver would let one child's connection pool leak into
// another's requests.
func TestWithHTTP_ReturnsCopy(t *testing.T) {
	reg, _ := capturingRegistry(connector.KindOpenAI)
	base := newDynamicProvider("copy-me", []ModelEntry{{Name: "m", URI: "openai://m@sk@h"}}, Deps{Connectors: reg})

	a := &stubDoer{}
	b := &stubDoer{}
	withA := base.WithHTTP(a)
	withB := base.WithHTTP(b)

	if base.http != nil {
		t.Error("WithHTTP mutated the receiver")
	}
	if withA == Provider(base) || withB == Provider(base) {
		t.Error("WithHTTP returned the receiver instead of a copy")
	}
	if got := withA.(*dynamicProvider).http; got != connector.HTTPDoer(a) {
		t.Errorf("first copy bound to %v, want a", got)
	}
	if got := withB.(*dynamicProvider).http; got != connector.HTTPDoer(b) {
		t.Errorf("second copy bound to %v, want b", got)
	}
	// The copies still serve the same catalog.
	if withA.Name() != "copy-me" || len(withA.(*dynamicProvider).entries) != 1 {
		t.Error("WithHTTP copy lost the catalog")
	}
}

// The provider built by NewProvider satisfies the optional HTTPInjector
// interface main.go type-asserts against.
func TestNewProvider_ImplementsHTTPInjector(t *testing.T) {
	p := NewProvider("injector", nil, Deps{})
	inj, ok := p.(HTTPInjector)
	if !ok {
		t.Fatal("NewProvider result does not implement HTTPInjector")
	}
	doer := &stubDoer{}
	bound := inj.WithHTTP(doer)
	if bound.(*dynamicProvider).http != connector.HTTPDoer(doer) {
		t.Error("WithHTTP via HTTPInjector did not bind the client")
	}
}

// stubDoer is a do-nothing HTTPDoer used as an identity marker.
type stubDoer struct{ calls int }

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n")),
		Request:    req,
	}, nil
}
