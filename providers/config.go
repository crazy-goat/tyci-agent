package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci-agent/api"
	"github.com/decodo/tyci-agent/internal/connect"
	"github.com/decodo/tyci-agent/stream"
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
		// Check auth.json (ignore read errors; env vars still work as fallback)
		if key, ok, err := connect.GetKey(p.name); err == nil && ok && key != "" {
			return true
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
	if strings.HasPrefix(apiKey, "$") {
		apiKey = os.Getenv(strings.TrimPrefix(apiKey, "$"))
	}

	// If no API key in URI, try auth.json
	if apiKey == "" {
		if key, ok, err := connect.GetKey(p.name); err == nil && ok {
			apiKey = key
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
		return nil, fmt.Errorf("no API key for %q (set via 'tyci-agent provider auth set', %s_API_KEY env var, OPENCODE_API_KEY, or use a free model)", p.name, strings.ToUpper(p.name))
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
			Tools:     req.Tools,
		}
		go func() {
			defer close(ch)
			if err := api.StreamAnthropic(ctx, apiKey, endpoint, body, forward(ch, ctx)); err != nil {
				ch <- stream.StreamError{Err: err}
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
		go func() {
			defer close(ch)
			if err := api.StreamGemini(ctx, apiKey, endpoint, body, forward(ch, ctx)); err != nil {
				ch <- stream.StreamError{Err: err}
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

// parseURI parses the URI format:
//
//	api_type://model@auth_token@host:port/path
//
// api_type can be: openai, anthropic, gemini (defaults to openai).
// The auth_token can be empty for free models.
func parseURI(uri string) (apiType, authToken, baseURL, endpointPath string, err error) {
	parts := strings.SplitN(uri, "://", 2)
	if len(parts) != 2 {
		return "", "", "", "", fmt.Errorf("invalid URI: missing ://")
	}
	scheme := parts[0]
	rest := parts[1]

	apiType = scheme
	switch apiType {
	case "openai", "anthropic", "gemini":
		// known
	default:
		apiType = "openai"
	}

	// Split by '@' - format: model@token@hostportpath
	atParts := strings.SplitN(rest, "@", 3)
	if len(atParts) < 2 {
		return "", "", "", "", fmt.Errorf("invalid URI: expected model@token@host:port/path, got %q", rest)
	}
	modelName := atParts[0]
	if len(atParts) >= 3 {
		authToken = atParts[1]
	} else {
		authToken = ""
	}

	hostPortPath := atParts[len(atParts)-1]

	var host string
	var path string
	if idx := strings.Index(hostPortPath, "/"); idx >= 0 {
		host = hostPortPath[:idx]
		path = hostPortPath[idx:]
	} else {
		host = hostPortPath
		path = ""
	}

	baseURL = "https://" + host
	endpointPath = path

	switch apiType {
	case "anthropic":
		endpointPath += "/v1/messages"
	case "gemini":
	default:
		endpointPath += "/v1/chat/completions"
	}

	_ = modelName
	return
}

// parseModel extracts the model name from a URI.
func parseModel(uri string) string {
	parts := strings.SplitN(uri, "://", 2)
	if len(parts) != 2 {
		return ""
	}
	rest := parts[1]
	atParts := strings.SplitN(rest, "@", 3)
	if len(atParts) < 2 {
		return ""
	}
	if len(atParts) >= 3 {
		return atParts[0]
	}
	return atParts[0] // model@host:port/path (no token)
}
