package opencodego

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
	return "opencodego"
}

func (p *provider) IsConfigured() bool {
	key := os.Getenv("OPENCODE_GO_API_KEY")
	return key != ""
}

func (p *provider) Models() []string {
	return []string{
		"glm-5.1", "glm-5", "kimi-k2.5",
		"mimo-v2-pro", "mimo-v2-omni",
		"minimax-m2.7", "minimax-m2.5",
	}
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

func (p *provider) Send(ctx context.Context, model, prompt, system string, handler providers.StreamHandler) error {
	apiKey := os.Getenv("OPENCODE_GO_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("OPENCODE_GO_API_KEY not set")
	}

	endpoint := modelEndpoint(model)

	if anthropicModels[model] {
		return p.sendAnthropic(ctx, apiKey, endpoint, model, prompt, system, handler)
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
