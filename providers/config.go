package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/api"
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

// dynamicProvider implements Provider using config entries.
type dynamicProvider struct {
	name    string
	entries []ModelEntry
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
	var entry *ModelEntry
	for _, e := range p.entries {
		if e.Name == req.Model {
			entry = &e
			break
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("model %q not found in provider %q", req.Model, p.name)
	}

	apiType, apiKey, baseURL, endpointPath, err := parseURI(entry.URI)
	if err != nil {
		return nil, err
	}

	// Resolve $ENV_VAR references in token
	apiKey = connect.ResolveToken(apiKey)

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
		return nil, fmt.Errorf("no API key for %q (set via 'tyci provider auth set', %s_API_KEY env var, OPENCODE_API_KEY, or use a free model)", p.name, strings.ToUpper(p.name))
	}

	endpoint := baseURL + endpointPath
	ch := make(chan stream.Event, 64)

	switch apiType {
	case "anthropic":
		anthropicMsgs := RichMessagesToAnthropic(req.Messages)
		body := api.AnthropicRequest{
			Model:     req.Model,
			MaxTokens: 4096,
			Stream:    true,
			System:    req.System,
			Messages:  anthropicMsgs,
			Tools:     api.ConvertToolsToAnthropic(req.Tools),
		}
		go func() {
			defer close(ch)
			if err := api.StreamAnthropic(ctx, apiKey, endpoint, body, forward(ch, ctx)); err != nil {
				select {
				case ch <- stream.StreamError{Err: err}:
				case <-ctx.Done():
				}
			}
		}()

	case "gemini":
		contents, system := RichMessagesToGemini(req.Messages)
		body := api.GeminiRequest{
			Contents: contents,
			Stream:   true,
		}
		if system != "" {
			body.SystemInstruction = &struct {
				Parts []api.GeminiPart `json:"parts"`
			}{Parts: []api.GeminiPart{{Text: system}}}
		} else if req.System != "" {
			body.SystemInstruction = &struct {
				Parts []api.GeminiPart `json:"parts"`
			}{Parts: []api.GeminiPart{{Text: req.System}}}
		}
		// Convert tools from OpenAI format to Gemini functionDeclarations
		if len(req.Tools) > 0 && string(req.Tools) != "null" && string(req.Tools) != "[]" {
			body.Tools = convertToolsToGemini(req.Tools)
		}
		go func() {
			defer close(ch)
			if err := api.StreamGemini(ctx, apiKey, endpoint, body, forward(ch, ctx)); err != nil {
				select {
				case ch <- stream.StreamError{Err: err}:
				case <-ctx.Done():
				}
			}
		}()

	default: // "openai" or any other chat-completion-like API
		chatMsgs := RichMessagesToChat(req.Messages, req.System)
		body := api.ChatRequest{
			Model:     req.Model,
			Stream:    true,
			Messages:  chatMsgs,
			Tools:     req.Tools,
			Reasoning: true,
		}
		go func() {
			defer close(ch)
			if err := api.StreamChat(ctx, apiKey, endpoint, body, forward(ch, ctx)); err != nil {
				ch <- stream.StreamError{Err: err}
			}
		}()
	}

	return ch, nil
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

// convertToolsToGemini converts tool schemas from OpenAI format to Gemini functionDeclarations format.
// OpenAI format:  [{"type":"function","function":{"name":"...","description":"...","parameters":{...}}}]
// Gemini format: [{"functionDeclarations":[{"name":"...","description":"...","parameters":{...}}]}]
func convertToolsToGemini(tools json.RawMessage) []api.GeminiTools {
	var openaiTools []map[string]any
	if err := json.Unmarshal(tools, &openaiTools); err != nil {
		return nil
	}

	declarations := make([]api.GeminiToolDeclaration, 0, len(openaiTools))
	for _, t := range openaiTools {
		fn, ok := t["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		desc, _ := fn["description"].(string)

		var params json.RawMessage
		if p, ok := fn["parameters"]; ok {
			if data, err := json.Marshal(p); err == nil {
				params = data
			}
		}

		declarations = append(declarations, api.GeminiToolDeclaration{
			Name:        name,
			Description: desc,
			Parameters:  params,
		})
	}

	if len(declarations) == 0 {
		return nil
	}

	return []api.GeminiTools{{FunctionDeclarations: declarations}}
}
