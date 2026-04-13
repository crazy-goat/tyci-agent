package opencodezen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/decodo/tyci-agent/api"
	"github.com/decodo/tyci-agent/providers"
	"github.com/decodo/tyci-agent/tools"
)

const baseURL = "https://opencode.ai/zen/v1"

var claudeModels = map[string]bool{
	"claude-opus-4-6":   true,
	"claude-opus-4-5":   true,
	"claude-opus-4-1":   true,
	"claude-sonnet-4-6": true,
	"claude-sonnet-4-5": true,
	"claude-sonnet-4":   true,
	"claude-haiku-4-5":  true,
	"claude-3-5-haiku":  true,
}

var geminiModels = map[string]bool{
	"gemini-3.1-pro": true,
	"gemini-3-flash": true,
}

var responsesAPIModels = map[string]bool{
	"gpt-5.4":             true,
	"gpt-5.4-pro":         true,
	"gpt-5.4-mini":        true,
	"gpt-5.4-nano":        true,
	"gpt-5.3-codex":       true,
	"gpt-5.3-codex-spark": true,
	"gpt-5.2":             true,
	"gpt-5.2-codex":       true,
	"gpt-5.1":             true,
	"gpt-5.1-codex":       true,
	"gpt-5.1-codex-max":   true,
	"gpt-5.1-codex-mini":  true,
	"gpt-5-codex":         true,
	"gpt-5-nano":          true,
}

var freeModels = map[string]bool{
	"big-pickle":            true,
	"mimo-v2-pro-free":      true,
	"mimo-v2-omni-free":     true,
	"qwen3.6-plus-free":     true,
	"nemotron-3-super-free": true,
	"minimax-m2.5-free":     true,
}

type provider struct{}

func init() {
	providers.Register(&provider{})
}

func (p *provider) Name() string {
	return "opencode-zen"
}

func (p *provider) IsConfigured() bool {
	return true
}

func isFreeModel(model string) bool {
	return freeModels[model]
}

type modelListResponse struct {
	Object string `json:"object"`
	Data   []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (p *provider) fetchModels() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://opencode.ai/zen/v1/models", nil)
	if err != nil {
		return nil
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var list modelListResponse
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
		"gpt-5.4", "gpt-5.4-pro", "gpt-5.4-mini", "gpt-5.4-nano",
		"gpt-5.3-codex", "gpt-5.3-codex-spark",
		"gpt-5.2", "gpt-5.2-codex",
		"gpt-5.1", "gpt-5.1-codex", "gpt-5.1-codex-max", "gpt-5.1-codex-mini",
		"gpt-5", "gpt-5-codex", "gpt-5-nano",
		"claude-opus-4-6", "claude-opus-4-5", "claude-opus-4-1",
		"claude-sonnet-4-6", "claude-sonnet-4-5", "claude-sonnet-4",
		"claude-haiku-4-5", "claude-3-5-haiku",
		"gemini-3.1-pro", "gemini-3-flash",
		"glm-5.1", "glm-5", "kimi-k2.5",
		"minimax-m2.5",
	}
}

func (p *provider) FreeModels() []string {
	return []string{
		"big-pickle",
		"mimo-v2-pro-free",
		"mimo-v2-omni-free",
		"qwen3.6-plus-free",
		"nemotron-3-super-free",
		"minimax-m2.5-free",
	}
}

func modelEndpoint(model string) string {
	if claudeModels[model] {
		return baseURL + "/messages"
	}
	if geminiModels[model] {
		return baseURL + "/models/" + model
	}
	if responsesAPIModels[model] {
		return baseURL + "/responses"
	}
	return baseURL + "/chat/completions"
}

func convertToolCalls(apiCalls []api.ToolCall) []providers.ToolCall {
	result := make([]providers.ToolCall, len(apiCalls))
	for i, tc := range apiCalls {
		result[i] = providers.ToolCall{Name: tc.Name, Arguments: tc.Argument}
	}
	return result
}

type textCollector struct {
	text string
}

func (t *textCollector) Chunk(text string)     { t.text += text }
func (t *textCollector) Summary(api.UsageInfo) {}
func (t *textCollector) End()                  {}
func (t *textCollector) Error(err error)       {}

func (p *provider) Send(ctx context.Context, model, prompt, system string, debug bool) (*providers.SendResult, error) {
	apiKey := os.Getenv("OPENCODE_ZEN_API_KEY")
	if apiKey == "" && !isFreeModel(model) {
		return nil, fmt.Errorf("OPENCODE_ZEN_API_KEY not set (or use a free model: big-pickle, mimo-v2-*-free, etc.)")
	}

	endpoint := modelEndpoint(model)
	collector := &textCollector{}
	handler := &api.DebugHandler{Inner: collector, Debug: debug}

	if claudeModels[model] {
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

	if geminiModels[model] {
		contents := []api.GeminiContent{
			{Parts: []api.GeminiPart{{Text: prompt}}},
		}
		body := api.GeminiRequest{
			Contents: contents,
			Stream:   true,
		}
		if system != "" {
			body.SystemInstruction = &struct {
				Parts []api.GeminiPart `json:"parts"`
			}{Parts: []api.GeminiPart{{Text: system}}}
		}
		err := api.StreamGemini(ctx, apiKey, endpoint, body, handler)
		return &providers.SendResult{Text: collector.text, ToolCalls: convertToolCalls(handler.GetToolCalls())}, err
	}

	if responsesAPIModels[model] {
		messages := []api.ResponsesMessage{
			{Role: "user", Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{{Type: "text", Text: prompt}}},
		}
		if system != "" {
			messages = append([]api.ResponsesMessage{
				{Role: "system", Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{{Type: "text", Text: system}}},
			}, messages...)
		}
		toolsJSON, _ := json.Marshal(tools.GetToolsSchemaForResponses())
		body := api.ResponsesRequest{
			Model:  model,
			Stream: true,
			Input:  api.ResponsesInput{Messages: messages},
			Tools:  toolsJSON,
		}
		err := api.StreamResponses(ctx, apiKey, endpoint, body, handler)
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
		Tools:    tools.GetToolsSchemaJSON(),
	}
	err := api.StreamChat(ctx, apiKey, endpoint, body, handler)
	return &providers.SendResult{Text: collector.text, ToolCalls: convertToolCalls(handler.GetToolCalls())}, err
}

func (p *provider) SendWithMessages(ctx context.Context, model, prompt, system string, messages []providers.Message, debug bool) (*providers.SendResult, error) {
	apiKey := os.Getenv("OPENCODE_ZEN_API_KEY")
	if apiKey == "" && !isFreeModel(model) {
		return nil, fmt.Errorf("OPENCODE_ZEN_API_KEY not set (or use a free model: big-pickle, mimo-v2-*-free, etc.)")
	}

	endpoint := modelEndpoint(model)
	collector := &textCollector{}
	handler := &api.DebugHandler{Inner: collector, Debug: debug}

	if claudeModels[model] {
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

	if geminiModels[model] {
		contents := []api.GeminiContent{
			{Parts: []api.GeminiPart{{Text: prompt}}},
		}
		body := api.GeminiRequest{
			Contents: contents,
			Stream:   true,
		}
		if system != "" {
			body.SystemInstruction = &struct {
				Parts []api.GeminiPart `json:"parts"`
			}{Parts: []api.GeminiPart{{Text: system}}}
		}
		err := api.StreamGemini(ctx, apiKey, endpoint, body, handler)
		return &providers.SendResult{Text: collector.text, ToolCalls: convertToolCalls(handler.GetToolCalls())}, err
	}

	if responsesAPIModels[model] {
		responsesMsgs := make([]api.ResponsesMessage, 0, len(messages))
		for _, m := range messages {
			var content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			content = append(content, struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "text", Text: m.Content})
			responsesMsgs = append(responsesMsgs, api.ResponsesMessage{Role: m.Role, Content: content})
		}
		toolsJSON, _ := json.Marshal(tools.GetToolsSchemaForResponses())
		body := api.ResponsesRequest{
			Model:  model,
			Stream: true,
			Input:  api.ResponsesInput{Messages: responsesMsgs},
			Tools:  toolsJSON,
		}
		err := api.StreamResponses(ctx, apiKey, endpoint, body, handler)
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
		Tools:    tools.GetToolsSchemaJSON(),
	}
	err := api.StreamChat(ctx, apiKey, endpoint, body, handler)
	return &providers.SendResult{Text: collector.text, ToolCalls: convertToolCalls(handler.GetToolCalls())}, err
}
