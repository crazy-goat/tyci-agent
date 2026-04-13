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
	return key != ""
}

func (p *provider) fetchModels() []string {
	apiKey := os.Getenv("OPENCODE_GO_API_KEY")
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

type textCollector struct {
	text string
}

func (t *textCollector) Chunk(text string)       { t.text += text }
func (t *textCollector) Thinking(text string)    {}
func (t *textCollector) EndThinking()            {}
func (t *textCollector) LogToolCallStart(string) {}
func (t *textCollector) ToolCallArg(string)      {}
func (t *textCollector) EndToolCall()            {}
func (t *textCollector) Summary(api.UsageInfo)   {}
func (t *textCollector) End()                    {}
func (t *textCollector) Error(err error)         {}

func convertToolCalls(apiCalls []api.ToolCall) []providers.ToolCall {
	result := make([]providers.ToolCall, len(apiCalls))
	for i, tc := range apiCalls {
		result[i] = providers.ToolCall{Name: tc.Name, Arguments: tc.Argument}
	}
	return result
}

func (p *provider) Send(ctx context.Context, model, prompt, system string, debug bool) (*providers.SendResult, error) {
	apiKey := os.Getenv("OPENCODE_GO_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENCODE_GO_API_KEY not set")
	}

	endpoint := modelEndpoint(model)
	collector := &textCollector{}
	handler := &api.DebugHandler{Inner: collector, Debug: debug}

	if anthropicModels[model] {
		messages := []api.AnthropicMessage{
			{Role: "user", Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{{Type: "text", Text: prompt}}},
		}
		body := api.AnthropicRequest{
			Model:     model,
			MaxTokens: 4096,
			Stream:    true,
			System:    system,
			Messages:  messages,
		}
		err := api.StreamAnthropic(ctx, apiKey, endpoint, body, handler)
		return &providers.SendResult{Text: collector.text, ToolCalls: convertToolCalls(handler.GetToolCalls())}, err
	}

	chatMessages := []api.ChatMessage{}
	if system != "" {
		chatMessages = append(chatMessages, api.ChatMessage{Role: "system", Content: system})
	}
	chatMessages = append(chatMessages, api.ChatMessage{Role: "user", Content: prompt})
	body := api.ChatRequest{
		Model:    model,
		Stream:   true,
		Messages: chatMessages,
	}
	err := api.StreamChat(ctx, apiKey, endpoint, body, handler)
	return &providers.SendResult{Text: collector.text, ToolCalls: convertToolCalls(handler.GetToolCalls())}, err
}

func (p *provider) SendWithMessages(ctx context.Context, model, prompt, system string, messages []providers.Message, debug bool) (*providers.SendResult, error) {
	apiKey := os.Getenv("OPENCODE_GO_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENCODE_GO_API_KEY not set")
	}

	endpoint := modelEndpoint(model)
	collector := &textCollector{}
	handler := &api.DebugHandler{Inner: collector, Debug: debug}

	if anthropicModels[model] {
		anthropicMsgs := make([]api.AnthropicMessage, 0, len(messages))
		for _, m := range messages {
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
		body := api.AnthropicRequest{
			Model:     model,
			MaxTokens: 4096,
			Stream:    true,
			System:    system,
			Messages:  anthropicMsgs,
		}
		err := api.StreamAnthropic(ctx, apiKey, endpoint, body, handler)
		return &providers.SendResult{Text: collector.text, ToolCalls: convertToolCalls(handler.GetToolCalls())}, err
	}

	chatMsgs := make([]api.ChatMessage, 0, len(messages))
	for _, m := range messages {
		chatMsgs = append(chatMsgs, api.ChatMessage{Role: m.Role, Content: m.Content})
	}
	body := api.ChatRequest{
		Model:    model,
		Stream:   true,
		Messages: chatMsgs,
	}
	err := api.StreamChat(ctx, apiKey, endpoint, body, handler)
	return &providers.SendResult{Text: collector.text, ToolCalls: convertToolCalls(handler.GetToolCalls())}, err
}

func (p *provider) SendWithHandler(model string, messages []providers.Message, handler providers.OutputHandler, debug bool) (*providers.SendResult, error) {
	apiKey := os.Getenv("OPENCODE_GO_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENCODE_GO_API_KEY not set")
	}

	endpoint := modelEndpoint(model)
	collector := &textCollector{}
	debugHandler := &api.DebugHandler{Inner: collector, Debug: debug}

	chatMsgs := make([]api.ChatMessage, 0, len(messages))
	for _, m := range messages {
		chatMsgs = append(chatMsgs, api.ChatMessage{Role: m.Role, Content: m.Content})
	}

	body := api.ChatRequest{
		Model:    model,
		Stream:   true,
		Messages: chatMsgs,
	}

	err := api.StreamChat(context.Background(), apiKey, endpoint, body, debugHandler)
	return &providers.SendResult{Text: collector.text, ToolCalls: convertToolCalls(debugHandler.GetToolCalls())}, err
}
