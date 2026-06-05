package opencodego

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/decodo/tyci-agent/api"
	"github.com/decodo/tyci-agent/providers"
	"github.com/decodo/tyci-agent/stream"
	"github.com/decodo/tyci-agent/tools"
)

const baseURL = "https://opencode.ai/zen/go/v1"

var anthropicModels = map[string]bool{
	"minimax-m2.7": true,
	"minimax-m2.5": true,
}

func modelEndpoint(model string) string {
	if anthropicModels[model] {
		return baseURL + "/messages"
	}
	return baseURL + "/chat/completions"
}

type provider struct{}

func init() {
	providers.Register(&provider{})
}

func (p *provider) Name() string {
	return "opencode-go"
}

func (p *provider) IsConfigured() bool {
	key := os.Getenv("OPENCODE_GO_API_KEY")
	if key == "" {
		key = os.Getenv("OPENCODE_API_KEY")
	}
	return key != ""
}

func (p *provider) fetchModels() []string {
	apiKey := os.Getenv("OPENCODE_GO_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("OPENCODE_API_KEY")
	}
	if apiKey == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil
	}

	var list struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil
	}
	var models []string
	for _, m := range list.Data {
		models = append(models, m.ID)
	}
	return models
}

func (p *provider) Models() []string {
	models := p.fetchModels()
	if len(models) > 0 {
		return models
	}
	return []string{
		"glm-5.1", "glm-5", "kimi-k2.5",
		"mimo-v2-pro", "mimo-v2-omni",
		"minimax-m2.7", "minimax-m2.5",
	}
}

func (p *provider) FreeModels() []string {
	return nil
}

func (p *provider) Stream(ctx context.Context, req providers.Request) (<-chan stream.Event, error) {
	apiKey := os.Getenv("OPENCODE_GO_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("OPENCODE_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("OPENCODE_GO_API_KEY not set")
	}

	endpoint := modelEndpoint(req.Model)

	chatMsgs := make([]api.ChatMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		chatMsgs = append(chatMsgs, api.ChatMessage{Role: "system", Content: req.System})
	} else {
		chatMsgs = append(chatMsgs, api.ChatMessage{
			Role:    "system",
			Content: providers.BuildSystemPrompt(),
		})
	}
	for _, m := range req.Messages {
		chatMsgs = append(chatMsgs, api.ChatMessage{Role: m.Role, Content: m.Content})
	}

	body := api.ChatRequest{
		Model:     req.Model,
		Stream:    true,
		Messages:  chatMsgs,
		Tools:     tools.GetToolsSchemaJSON(),
		Reasoning: true,
	}

	if anthropicModels[req.Model] {
		anthropicMsgs := make([]api.AnthropicMessage, 0, len(req.Messages))
		for _, m := range req.Messages {
			var content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			content = append(content, struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "text", Text: m.Content})
			anthropicMsgs = append(anthropicMsgs, api.AnthropicMessage{Role: m.Role, Content: content})
		}
		anthropicBody := api.AnthropicRequest{
			Model:     req.Model,
			MaxTokens: 4096,
			Stream:    true,
			System:    req.System,
			Messages:  anthropicMsgs,
		}

		ch := make(chan stream.Event, 64)
		go func() {
			defer close(ch)
			if err := api.StreamAnthropic(ctx, apiKey, endpoint, anthropicBody, func(e stream.Event) error {
				select {
				case ch <- e:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}); err != nil {
				ch <- stream.StreamError{Err: err}
			}
		}()
		return ch, nil
	}

	ch := make(chan stream.Event, 64)
	go func() {
		defer close(ch)
		if err := api.StreamChat(ctx, apiKey, endpoint, body, func(e stream.Event) error {
			select {
			case ch <- e:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}); err != nil {
			ch <- stream.StreamError{Err: err}
		}
	}()
	return ch, nil
}
