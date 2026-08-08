package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/internal/connect"
	"github.com/decodo/tyci/internal/tyciconfig"
	"github.com/decodo/tyci/stream"
)

// uriEntry is the inner JSON object with just a URI.
type uriEntry struct {
	URI string `json:"uri"`
}

// LoadConfig reads model.json and returns parsed provider model entries.
// Accepts:
//   - {"provider": {"friendly-name": {"uri": "..."}}}  (new format)
//   - {"providers": {"name": [{"uri": "..."}]}}         (legacy)
//   - {"name": [{"uri": "..."}]}                        (legacy)
func LoadConfig(path string) (map[string][]ModelEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	// New format: {"provider": {"name": {"uri": "..."}}}
	var newFmt map[string]map[string]uriEntry
	if err := json.Unmarshal(data, &newFmt); err == nil && len(newFmt) > 0 {
		result := make(map[string][]ModelEntry)
		for prov, models := range newFmt {
			for name, entry := range models {
				result[prov] = append(result[prov], ModelEntry{Name: name, URI: entry.URI})
			}
		}
		return result, nil
	}

	// Legacy format with "providers" wrapper
	var cfg struct {
		Providers map[string][]struct {
			URI string `json:"uri"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &cfg); err == nil && cfg.Providers != nil {
		result := make(map[string][]ModelEntry)
		for prov, list := range cfg.Providers {
			for _, e := range list {
				result[prov] = append(result[prov], ModelEntry{Name: parseModel(e.URI), URI: e.URI})
			}
		}
		return result, nil
	}

	// Legacy format without wrapper
	var legacy map[string][]struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	result := make(map[string][]ModelEntry)
	for prov, list := range legacy {
		for _, e := range list {
			result[prov] = append(result[prov], ModelEntry{Name: parseModel(e.URI), URI: e.URI})
		}
	}
	return result, nil
}

// MustLoadConfig loads config or returns empty map on error.
func MustLoadConfig(path string) map[string][]ModelEntry {
	cfg, err := LoadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: config error: %v\n", err)
	}
	if cfg == nil {
		cfg = make(map[string][]ModelEntry)
	}
	return cfg
}

// RegisterProvidersFromConfig reads model.json and registers a dynamic provider
// for each group found.
func RegisterProvidersFromConfig(path string) {
	entries := MustLoadConfig(path)
	for groupName, list := range entries {
		if len(list) == 0 {
			continue
		}
		p := &dynamicProvider{
			name:    groupName,
			entries: list,
		}
		Register(p)
	}
}

// npmToAPIType maps npm package names to tyci API types.
var npmToAPIType = map[string]string{
	"@ai-sdk/openai":            "openai",
	"@ai-sdk/anthropic":         "anthropic",
	"@ai-sdk/gemini":            "gemini",
	"@ai-sdk/openai-compatible": "openai",
}

// npmToHost maps npm package names to default API hosts.
var npmToHost = map[string]string{
	"@ai-sdk/openai":    "api.openai.com",
	"@ai-sdk/anthropic": "api.anthropic.com",
	"@ai-sdk/gemini":    "generativelanguage.googleapis.com",
}

// LoadProvidersJSON reads a models.dev api.json format file (cached at
// providers.json) and returns parsed provider model entries with URIs.
func LoadProvidersJSON(path string) (map[string][]ModelEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading providers.json: %w", err)
	}

	var catalog map[string]connect.ModelsDevProvider
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("parsing providers.json: %w", err)
	}

	result := make(map[string][]ModelEntry)
	for id, p := range catalog {
		apiType, ok := npmToAPIType[p.NPM]
		if !ok {
			continue
		}
		host, apiPath := "", ""
		if p.API != "" {
			host, apiPath = splitHostPath(p.API)
		} else {
			host = npmToHost[p.NPM]
		}
		if host == "" {
			continue
		}
		for mid := range p.Models {
			uri := tyciconfig.ProviderURI{
				APIType:   apiType,
				Model:     mid,
				AuthToken: "",
				Host:      host,
				Path:      apiPath,
			}
			result[id] = append(result[id], ModelEntry{Name: mid, URI: uri.String()})
		}
	}
	return result, nil
}

// RegisterProvidersFromProvidersJSON reads providers.json (models.dev api.json
// format) and registers a dynamic provider for each entry.
func RegisterProvidersFromProvidersJSON(path string) error {
	entries, err := LoadProvidersJSON(path)
	if err != nil {
		return err
	}
	for groupName, list := range entries {
		if len(list) == 0 {
			continue
		}
		p := &dynamicProvider{
			name:    groupName,
			entries: list,
		}
		Register(p)
	}
	return nil
}

func splitHostPath(apiURL string) (host, path string) {
	if idx := strings.Index(apiURL, "://"); idx >= 0 {
		apiURL = apiURL[idx+3:]
	}
	if idx := strings.Index(apiURL, "/"); idx >= 0 {
		host = apiURL[:idx]
		path = apiURL[idx:]
	} else {
		host = apiURL
	}
	return host, path
}

// ModelEntry holds a single model's friendly name and URI.
type ModelEntry struct {
	Name string
	URI  string
}

// defaultConnectors is the connector set every dynamicProvider uses unless it
// carries its own. The registry is a value, so this is a package default —
// not a global registry the connector package would have to own.
var defaultConnectors = connector.DefaultRegistry()

// dynamicProvider implements Provider using config entries.
type dynamicProvider struct {
	name    string
	entries []ModelEntry
	// registry is optional; nil means defaultConnectors.
	registry *connector.Registry
}

func (p *dynamicProvider) Name() string { return p.name }

func (p *dynamicProvider) IsConfigured() bool {
	for _, e := range p.entries {
		_, token, _, _, err := parseURI(e.URI)
		if err == nil && token != "" {
			return true
		}
		// Check auth.json. Resolve "$ENV_VAR" references so a literal
		// "$FOO" entry (from single-quoted shell input or hand-edits)
		// is treated as configured only when FOO is actually exported.
		if key, ok, err := connect.GetKey(p.name); err == nil && ok {
			if resolved := connect.ResolveToken(key); resolved != "" {
				return true
			}
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: reading auth.json: %v\n", err)
		}
		if os.Getenv(strings.ToUpper(p.name+"_API_KEY")) != "" {
			return true
		}
		if os.Getenv("OPENCODE_API_KEY") != "" {
			return true
		}
	}
	return false
}

func (p *dynamicProvider) Models() []string {
	var models []string
	for _, e := range p.entries {
		if e.Name != "" {
			models = append(models, e.Name)
		}
	}
	return models
}

func (p *dynamicProvider) FreeModels() []string {
	return nil
}

func (p *dynamicProvider) Stream(ctx context.Context, req Request) (<-chan stream.Event, error) {
	entry := p.findEntry(req.Model)
	if entry == nil {
		return nil, fmt.Errorf("model %q not found in provider %q", req.Model, p.name)
	}

	apiType, uriKey, baseURL, endpointPath, err := parseURI(entry.URI)
	if err != nil {
		return nil, err
	}

	apiKey, err := p.resolveAPIKey(uriKey)
	if err != nil {
		return nil, err
	}

	conn, err := p.connectors().New(p.kindFor(apiType), connector.Endpoint{
		BaseURL: baseURL,
		Path:    endpointPath,
		APIKey:  apiKey,
		Options: uriOptions(entry.URI),
	})
	if err != nil {
		return nil, err
	}

	ch := make(chan stream.Event, 64)
	go func() {
		defer close(ch)
		if err := conn.Stream(ctx, req, forward(ch, ctx)); err != nil {
			select {
			case ch <- stream.StreamError{Err: err}:
			case <-ctx.Done():
			}
		}
	}()

	return ch, nil
}

// findEntry returns the entry for a model name, or nil.
func (p *dynamicProvider) findEntry(model string) *ModelEntry {
	for i := range p.entries {
		if p.entries[i].Name == model {
			return &p.entries[i]
		}
	}
	return nil
}

// connectors returns the registry to build connectors from, defaulting to the
// built-in set. The field exists so callers can inject fakes; until Etap 4
// turns Provider into a struct, nothing sets it.
func (p *dynamicProvider) connectors() *connector.Registry {
	if p.registry != nil {
		return p.registry
	}
	return defaultConnectors
}

// kindFor maps a URI api_type to a connector kind. Anything the registry does
// not know is treated as an OpenAI-style chat-completions API — this preserves
// the `default:` branch of the switch that used to live in Stream.
func (p *dynamicProvider) kindFor(apiType string) string {
	if p.connectors().Has(apiType) {
		return apiType
	}
	return connector.KindOpenAI
}

// uriOptions extracts the connector options encoded in the URI query string.
// Returns nil when there is nothing to pass.
func uriOptions(uri string) map[string]string {
	parsed, err := tyciconfig.Parse(uri)
	if err != nil || !parsed.Reasoning {
		return nil
	}
	return map[string]string{connector.OptReasoning: "true"}
}

// resolveAPIKey resolves the credential for this provider: the token from the
// URI first, then auth.json, then env vars.
func (p *dynamicProvider) resolveAPIKey(uriKey string) (string, error) {
	// Resolve $ENV_VAR references in token
	apiKey := connect.ResolveToken(uriKey)

	// If no API key in URI, try auth.json
	if apiKey == "" {
		if key, ok, err := connect.GetKey(p.name); err == nil && ok {
			// Resolve "$ENV_VAR" refs stored in auth.json, too. This
			// allows entries like "nexos": "$NEXOS_API_KEY" to work
			// even when the user accidentally single-quoted the value
			// at `provider auth set` time.
			apiKey = connect.ResolveToken(key)
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: reading auth.json: %v\n", err)
		}
	}

	// If still no API key, try env vars
	if apiKey == "" {
		envKey := strings.ToUpper(p.name) + "_API_KEY"
		apiKey = os.Getenv(envKey)
		if apiKey == "" {
			apiKey = os.Getenv("OPENCODE_API_KEY")
		}
	}

	if apiKey == "" {
		return "", fmt.Errorf("no API key for %q (set via 'tyci provider auth set', %s_API_KEY env var, OPENCODE_API_KEY, or use a free model)", p.name, strings.ToUpper(p.name))
	}
	return apiKey, nil
}

// forward creates an emit function for stream events.
func forward(ch chan<- stream.Event, ctx context.Context) func(stream.Event) error {
	return func(e stream.Event) error {
		select {
		case ch <- e:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// parseURI parses the URI using the shared tyciconfig.ProviderURI type.
// Kept for backward compatibility with existing callers.
func parseURI(uri string) (apiType, authToken, baseURL, endpointPath string, err error) {
	u, err := tyciconfig.Parse(uri)
	if err != nil {
		return "", "", "", "", err
	}

	endpointPath = u.Path
	switch u.APIType {
	case "anthropic":
		endpointPath = appendChatPath(endpointPath, "/v1/messages")
	case "gemini":
		// Gemini uses different path structure
	default:
		endpointPath = appendChatPath(endpointPath, "/v1/chat/completions")
	}

	return u.APIType, u.AuthToken, "https://" + u.Host, endpointPath, nil
}

// appendChatPath appends the API-specific chat endpoint path to the base path.
// It preserves a non-empty base path (e.g. /zen/go/v1) and only adds the
// default endpoint when the base path is empty or ends with /v1.
func appendChatPath(basePath, defaultEndpoint string) string {
	basePath = strings.TrimSuffix(basePath, "/")
	if basePath == "" {
		return defaultEndpoint
	}
	if strings.HasSuffix(basePath, "/v1") {
		return basePath + strings.TrimPrefix(defaultEndpoint, "/v1")
	}
	return basePath
}

// parseModel extracts the model name from a URI.
func parseModel(uri string) string {
	u, err := tyciconfig.Parse(uri)
	if err != nil {
		return ""
	}
	return u.Model
}
