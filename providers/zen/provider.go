package zen

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

const baseURL = "https://opencode.ai/zen/go"

type provider struct{}

func init() {
	providers.Register(&provider{})
}

func (p *provider) Name() string {
	return "zen"
}

func (p *provider) IsConfigured() bool {
	key := os.Getenv("ZEN_API_KEY")
	return key != ""
}

func (p *provider) Models() []string {
	return []string{"glm-5.1", "glm-5", "kimi-k2.5", "mimo-v2-pro", "mimo-v2-omni"}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type requestBody struct {
	Model    string    `json:"model"`
	Stream   bool      `json:"stream"`
	Messages []message `json:"messages"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

func (p *provider) Send(ctx context.Context, model, prompt, system string, handler providers.StreamHandler) error {
	apiKey := os.Getenv("ZEN_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ZEN_API_KEY not set")
	}

	body := requestBody{
		Model:    model,
		Stream:   true,
		Messages: []message{},
	}
	if system != "" {
		body.Messages = append(body.Messages, message{Role: "system", Content: system})
	}
	body.Messages = append(body.Messages, message{Role: "user", Content: prompt})

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/chat/completions", strings.NewReader(string(jsonBody)))
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

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			content := chunk.Choices[0].Delta.Content
			if content != "" {
				handler.Chunk(content)
			}
		}
	}

	handler.Summary(providers.UsageInfo{})
	handler.End()
	return nil
}
