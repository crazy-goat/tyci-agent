package opencodezen

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/decodo/tyci-agent/providers"
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
	"gpt-5":               true,
	"gpt-5-codex":         true,
	"gpt-5-nano":          true,
}

type provider struct{}

func init() {
	providers.Register(&provider{})
}

func (p *provider) Name() string {
	return "opencodezen"
}

func (p *provider) IsConfigured() bool {
	key := os.Getenv("OPENCODE_ZEN_API_KEY")
	return key != ""
}

func (p *provider) Models() []string {
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
		"minimax-m2.5", "minimax-m2.5-free",
		"big-pickle",
		"mimo-v2-pro-free", "mimo-v2-omni-free",
		"qwen3.6-plus-free", "nemotron-3-super-free",
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

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequestBody struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type anthropicRequestBody struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicStreamChunk struct {
	Type  string `json:"type"`
	Delta struct {
		Text string `json:"text"`
	} `json:"delta"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

type anthropicMessageEvent struct {
	Type    string `json:"type"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

type responsesMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type responsesRequestBody struct {
	Model  string          `json:"model"`
	Stream bool            `json:"stream"`
	Input  responsesInput  `json:"input"`
	Tools  []responsesTool `json:"tools,omitempty"`
}

type responsesInput struct {
	Messages []responsesMessage `json:"messages"`
}

type responsesTool struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters"`
}

type responsesStreamChunk struct {
	Type   string `json:"type"`
	Output *struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages,omitempty"`
	} `json:"output,omitempty"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

type responsesFinalChunk struct {
	Type  string `json:"type"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

type geminiRequestBody struct {
	Contents          []geminiContent `json:"contents"`
	Stream            bool            `json:"stream"`
	SystemInstruction *struct {
		Parts []geminiPart `json:"parts"`
	} `json:"systemInstruction,omitempty"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
	Role  string       `json:"role,omitempty"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiStreamChunk struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata,omitempty"`
}

func (p *provider) Send(ctx context.Context, model, prompt, system string, handler providers.StreamHandler) error {
	apiKey := os.Getenv("OPENCODE_ZEN_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("OPENCODE_ZEN_API_KEY not set")
	}

	endpoint := modelEndpoint(model)

	if claudeModels[model] {
		return p.sendAnthropic(ctx, apiKey, endpoint, model, prompt, system, handler)
	}
	if geminiModels[model] {
		return p.sendGemini(ctx, apiKey, endpoint, model, prompt, system, handler)
	}
	if responsesAPIModels[model] {
		return p.sendResponses(ctx, apiKey, endpoint, model, prompt, system, handler)
	}
	return p.sendChat(ctx, apiKey, endpoint, model, prompt, system, handler)
}

func (p *provider) sendChat(ctx context.Context, apiKey, endpoint, model, prompt, system string, handler providers.StreamHandler) error {
	body := chatRequestBody{
		Model:    model,
		Stream:   true,
		Messages: []chatMessage{},
	}
	if system != "" {
		body.Messages = append(body.Messages, chatMessage{Role: "system", Content: system})
	}
	body.Messages = append(body.Messages, chatMessage{Role: "user", Content: prompt})

	return p.doRequest(ctx, apiKey, "POST", endpoint, body, handler, func(data string, h providers.StreamHandler) error {
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil
		}
		if len(chunk.Choices) > 0 {
			content := chunk.Choices[0].Delta.Content
			if content != "" {
				h.Chunk(content)
			}
		}
		return nil
	})
}

func (p *provider) sendAnthropic(ctx context.Context, apiKey, endpoint, model, prompt, system string, handler providers.StreamHandler) error {
	messages := []anthropicMessage{
		{Role: "user", Content: []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{{Type: "text", Text: prompt}}},
	}

	body := anthropicRequestBody{
		Model:     model,
		MaxTokens: 4096,
		Stream:    true,
		System:    system,
		Messages:  messages,
	}

	return p.doRequest(ctx, apiKey, "POST", endpoint, body, handler, func(data string, h providers.StreamHandler) error {
		if strings.HasPrefix(data, "[") {
			var chunks []anthropicStreamChunk
			if err := json.Unmarshal([]byte(data), &chunks); err != nil {
				return nil
			}
			for _, chunk := range chunks {
				if chunk.Type == "content_block_delta" {
					h.Chunk(chunk.Delta.Text)
				}
				if chunk.Type == "message_stop" && chunk.Usage != nil {
					h.Summary(providers.UsageInfo{
						InputTokens:  chunk.Usage.InputTokens,
						OutputTokens: chunk.Usage.OutputTokens,
					})
				}
			}
			return nil
		}

		var chunk anthropicStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil
		}
		if chunk.Type == "content_block_delta" {
			h.Chunk(chunk.Delta.Text)
		}
		if chunk.Type == "message_stop" && chunk.Usage != nil {
			h.Summary(providers.UsageInfo{
				InputTokens:  chunk.Usage.InputTokens,
				OutputTokens: chunk.Usage.OutputTokens,
			})
		}
		return nil
	})
}

func (p *provider) sendResponses(ctx context.Context, apiKey, endpoint, model, prompt, system string, handler providers.StreamHandler) error {
	messages := []responsesMessage{
		{Role: "user", Content: []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{{Type: "text", Text: prompt}}},
	}
	if system != "" {
		messages = append([]responsesMessage{
			{Role: "system", Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{{Type: "text", Text: system}}},
		}, messages...)
	}

	body := responsesRequestBody{
		Model:  model,
		Stream: true,
		Input: responsesInput{
			Messages: messages,
		},
	}

	return p.doRequest(ctx, apiKey, "POST", endpoint, body, handler, func(data string, h providers.StreamHandler) error {
		var chunk responsesStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil
		}
		if chunk.Type == "response.output_text.delta" && chunk.Output != nil {
			for _, msg := range chunk.Output.Messages {
				for _, c := range msg.Content {
					if c.Type == "text" {
						h.Chunk(c.Text)
					}
				}
			}
		}
		if chunk.Type == "response.done" && chunk.Usage != nil {
			h.Summary(providers.UsageInfo{
				InputTokens:  chunk.Usage.InputTokens,
				OutputTokens: chunk.Usage.OutputTokens,
			})
		}
		return nil
	})
}

func (p *provider) sendGemini(ctx context.Context, apiKey, endpoint, model, prompt, system string, handler providers.StreamHandler) error {
	contents := []geminiContent{
		{Parts: []geminiPart{{Text: prompt}}},
	}

	body := geminiRequestBody{
		Contents: contents,
		Stream:   true,
	}
	if system != "" {
		body.SystemInstruction = &struct {
			Parts []geminiPart `json:"parts"`
		}{Parts: []geminiPart{{Text: system}}}
	}

	return p.doRequest(ctx, apiKey, "POST", endpoint, body, handler, func(data string, h providers.StreamHandler) error {
		var chunk geminiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil
		}
		for _, c := range chunk.Candidates {
			for _, part := range c.Content.Parts {
				h.Chunk(part.Text)
			}
		}
		return nil
	})
}

type chunkParser func(data string, h providers.StreamHandler) error

func (p *provider) doRequest(ctx context.Context, apiKey, method, url string, body any, handler providers.StreamHandler, parser chunkParser) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		if err := parser(data, handler); err != nil {
			return err
		}
	}

	handler.Summary(providers.UsageInfo{})
	handler.End()
	return nil
}
